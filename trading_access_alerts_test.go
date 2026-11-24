// Copyright The Pit Project Owners. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Please see https://officer.openpit.dev and the OWNERS file for details.

package officer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/alerts"
	frameworkapp "go.openpit.dev/officer/framework/app"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/internal/access"
	"go.openpit.dev/officer/internal/access/accesstest"
	"go.openpit.dev/officer/internal/accountmode"
)

type mixedAlertQuoteSink struct {
	mu      sync.Mutex
	marks   map[[2]domain.EngineAssetID]string
	clears  int
	changed chan struct{}
}

func (s *mixedAlertQuoteSink) Push(update marketdata.QuoteUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.marks == nil {
		s.marks = make(map[[2]domain.EngineAssetID]string)
	}
	s.marks[[2]domain.EngineAssetID{update.Base, update.Quote}] = update.Mark
	close(s.changed)
	s.changed = make(chan struct{})
	return nil
}

func (s *mixedAlertQuoteSink) Clear(base, quote domain.EngineAssetID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.marks, [2]domain.EngineAssetID{base, quote})
	s.clears++
	close(s.changed)
	s.changed = make(chan struct{})
	return nil
}

type mixedAlertTradingRuntime struct {
	backend.TradingRuntime
	restarts int
	stopped  bool
	fail     error
}

func (r *mixedAlertTradingRuntime) Stop() { r.TradingRuntime.Stop(); r.stopped = true }
func (r *mixedAlertTradingRuntime) Restart() error {
	if err := r.TradingRuntime.Restart(); err != nil {
		return err
	}
	r.restarts++
	r.stopped = false
	return r.fail
}

type mixedAlertSigner struct {
	signing.Service
	reloads int
}

func (s *mixedAlertSigner) Reload(ctx context.Context) error {
	if err := s.Service.Reload(ctx); err != nil {
		return err
	}
	s.reloads++
	return nil
}

func TestMixedAlertRestoreCompletesRuntimeAndManualQuoteBeforeDelivery(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	deliveryErr := errors.New("warning delivery failed")
	var rt *mixedAlertTradingRuntime
	var signer *mixedAlertSigner
	app, local, realm, _, _ := mixedAlertApp(t, []alerts.Channel{{Name: "barrier", Deliver: func(context.Context, alerts.Event) error { close(entered); <-release; return deliveryErr }}}, func(b *frameworkapp.Builder) {
		b.SetServiceFactory(func(_ context.Context, n node.Node, md backend.MarketDataRuntime, trading backend.TradingRuntime, s signing.Service, registry *marketdata.Registry, _ *frameworkmcp.ToolRegistry) (backend.ControlPlane, error) {
			rt = &mixedAlertTradingRuntime{TradingRuntime: trading}
			signer = &mixedAlertSigner{Service: s}
			return backend.New(n, md, signer, func([]byte, domain.Order) (string, error) { return "", nil }, backend.WithTradingRuntime(rt), backend.WithMarketDataRegistry(registry), backend.WithAccountScope(backend.EveryAccount{}))
		})
	})
	caller := domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator}
	ctx := auth.ContextWithCaller(t.Context(), caller)
	paper, live := seedMixedAlertConnections(t, realm)
	if err := app.Service().PutTradingAccess(ctx, domain.TradingAccess{Account: "desk", Connection: paper}); err != nil {
		t.Fatal(err)
	}
	archive, err := realm.ExportBackup(ctx, backup.Scope{Sections: []backup.Section{backup.SectionTrading, backup.SectionMarketData}})
	if err != nil {
		t.Fatal(err)
	}
	archive.Manifest.Source = "test"
	archive.Data.TradingAccess = append(archive.Data.TradingAccess, backup.TradingAccess{Account: "desk", Connection: live})
	if len(archive.Data.MarketDataInstruments) != 1 {
		t.Fatalf("manual fixture=%+v", archive.Data.MarketDataInstruments)
	}
	archive.Data.MarketDataInstruments[0].ManualPrice = "125.5"
	type outcome struct {
		summary backup.RestoreSummary
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		summary, err := app.Service().RestoreBackup(ctx, archive, backup.RestoreOptions{Scope: backup.Scope{Sections: []backup.Section{backup.SectionTrading, backup.SectionMarketData}}, Mode: backup.RestoreModeOverwrite})
		done <- outcome{summary, err}
	}()
	defer func() {
		close(release)
		select {
		case result := <-done:
			if !errors.Is(result.err, domain.ErrTradingAccessNotification) || errors.Is(result.err, deliveryErr) || !domain.OnlyTradingAccessNotification(result.err) || len(result.summary.Applied) == 0 {
				t.Errorf("committed summary/warning=%+v %v", result.summary, result.err)
			}
		case <-time.After(3 * time.Second):
			t.Error("restore did not finish")
		}
	}()
	select {
	case <-entered:
	case result := <-done:
		t.Fatalf("restore failed before delivery: %v", result.err)
	case <-time.After(3 * time.Second):
		t.Fatal("restore delivery not reached")
	}
	if rt.stopped || rt.restarts != 1 || signer.reloads != 1 {
		t.Fatalf("warning preceded runtime/signer completion: stopped=%v restarts=%d reloads=%d", rt.stopped, rt.restarts, signer.reloads)
	}
	instruments, err := realm.ListMarketDataInstruments(ctx, "manual")
	if err != nil || len(instruments) != 1 {
		t.Fatalf("restored instrument: %+v %v", instruments, err)
	}
	sink := local.CurrentMarketDataSink().(*mixedAlertQuoteSink)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		sink.mu.Lock()
		mark := sink.marks[[2]domain.EngineAssetID{instruments[0].BaseAssetID, instruments[0].QuoteAssetID}]
		clears, changed := sink.clears, sink.changed
		sink.mu.Unlock()
		if mark == "125.5" && clears > 0 {
			break
		}
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatalf("active runtime did not receive restored price while delivery blocked: mark=%q clears=%d", mark, clears)
		}
	}
	unrelated := make(chan error, 1)
	go func() {
		unrelated <- app.Service().PutTradingAccess(ctx, domain.TradingAccess{Account: "other", Connection: paper})
	}()
	select {
	case err := <-unrelated:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("delivery still holds backend tradingMu")
	}
	joined := errors.Join(errors.New("restart failed"), &domain.TradingAccessNotificationError{Cause: deliveryErr})
	if public := httpx.DescribeErr(joined); public.Code != "trading_access_completion_failed" || public.Status != 500 {
		t.Fatalf("joined operational completion disguised as warning-only: %+v", public)
	}
}

func mixedAlertApp(t *testing.T, channels []alerts.Channel, configure ...func(*frameworkapp.Builder)) (*frameworkapp.App, node.Node, store.RealmStore, store.Store, *alerts.Service) {
	t.Helper()
	builder := frameworkapp.NewBuilder()
	var service *alerts.Service
	if err := Register(builder, Config{SQLitePath: filepath.Join(t.TempDir(), "alerts.db"), AlertChannels: channels, AlertServiceReady: func(s *alerts.Service) error { service = s; return nil }}); err != nil {
		t.Fatal(err)
	}
	var opened store.Store
	var local node.Node
	builder.SetNodeBuilder(func(ctx context.Context, st store.Store, fatal frameworkapp.FatalShutdownHook) (node.Node, engine.Engine, error) {
		opened = st
		realm, err := st.ForRealm(ctx, domain.DefaultRealm)
		if err != nil {
			return nil, nil, err
		}
		for _, code := range []domain.AccountID{"default", "desk", "other", "hidden"} {
			if _, err := realm.CreateAccount(ctx, domain.Account{Code: code, Title: map[domain.AccountID]string{"hidden": "Secret desk"}[code]}); err != nil {
				return nil, nil, err
			}
		}
		for _, code := range []string{"AAPL", "USD"} {
			if _, err := realm.CreateAsset(ctx, domain.Asset{Code: code}); err != nil {
				return nil, nil, err
			}
		}
		instance, err := realm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{ExternalID: "manual", Provider: domain.MarketDataProviderBYO, Label: "Manual", Enabled: true, Credentials: `{}`})
		if err != nil {
			return nil, nil, err
		}
		if err := realm.UpsertMarketDataInstrument(ctx, domain.MarketDataInstrument{Instance: instance.ExternalID, ExternalSymbol: "AAPL-USD", BaseAsset: "AAPL", QuoteAsset: "USD", ManualPrice: "100", Enabled: true}); err != nil {
			return nil, nil, err
		}
		sink := &mixedAlertQuoteSink{changed: make(chan struct{})}
		n, eng, err := node.NewLocalNode(ctx, domain.DefaultRealm, st, func(engine.Snapshot) (engine.Engine, error) { return &fakeEngine{running: true, sink: sink}, nil }, fatal)
		local = n
		return n, eng, err
	})
	builder.SetSPAFactory(gateTestSPA)
	for _, configureBuilder := range configure {
		configureBuilder(builder)
	}
	app, err := builder.Build(t.Context(), slog.Default(), failOnFatal(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	realm, err := opened.ForRealm(t.Context(), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	return app, local, realm, opened, service
}

func seedMixedAlertConnections(t *testing.T, realm store.RealmStore) (domain.ExternalID, domain.ExternalID) {
	t.Helper()
	var ids []domain.ExternalID
	for _, mode := range []domain.TradingMode{domain.TradingModePaper, domain.TradingModeLive} {
		c, err := realm.CreateTradingConnection(t.Context(), domain.TradingConnection{Provider: domain.TradingProviderAlpaca, Label: string(mode), Mode: mode, VenueIdentity: string(mode), Credentials: `{}`})
		if err != nil {
			t.Fatal(err)
		}
		if err := realm.SetTradingConnectionFeeAccount(t.Context(), c.ExternalID, "default"); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ExternalID)
	}
	return ids[0], ids[1]
}

type automaticMixedGrantRuntime struct {
	backend.TradingRuntime
	restarts int
}

func (*automaticMixedGrantRuntime) Providers() []trading.Provider {
	return []trading.Provider{{Type: domain.TradingProviderAlpaca, Title: "Local fixture", UnlinkedFees: true}}
}

func (*automaticMixedGrantRuntime) CheckAccount(_ context.Context, connection domain.TradingConnection) (trading.AccountInfo, error) {
	return trading.AccountInfo{Identity: "local-" + connection.Label, Status: "ACTIVE"}, nil
}

func (r *automaticMixedGrantRuntime) Restart() error {
	r.restarts++
	return nil
}

func TestMixedAlertAutomaticGrantsCompleteOutsideBackendLocks(t *testing.T) {
	for _, operation := range []string{"create", "enable regrant"} {
		t.Run(operation, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var runtime *automaticMixedGrantRuntime
			app, local, realm, _, service := mixedAlertApp(t, []alerts.Channel{{Name: "barrier", Deliver: func(context.Context, alerts.Event) error {
				close(entered)
				<-release
				return nil
			}}}, func(b *frameworkapp.Builder) {
				b.AddNodeHook(func(ctx context.Context, st store.Store, n node.Node) error {
					realm, err := st.ForRealm(ctx, n.Realm())
					if err != nil {
						return err
					}
					return realm.SetSetting(ctx, accountmode.SettingKeyMode, "single:default")
				})
				b.SetServiceFactory(func(ctx context.Context, n node.Node, md backend.MarketDataRuntime, underlying backend.TradingRuntime, signer signing.Service, registry *marketdata.Registry, _ *frameworkmcp.ToolRegistry) (backend.ControlPlane, error) {
					scope, err := accountmode.Load(ctx, n)
					if err != nil {
						return nil, err
					}
					runtime = &automaticMixedGrantRuntime{TradingRuntime: underlying}
					return backend.New(n, md, signer, func([]byte, domain.Order) (string, error) { return "", nil }, backend.WithTradingRuntime(runtime), backend.WithMarketDataRegistry(registry), backend.WithAccountScope(scope))
				})
			})
			caller := domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator}
			ctx := auth.ContextWithCaller(t.Context(), caller)
			paper, live := seedMixedAlertConnections(t, realm)
			if err := local.PutTradingAccess(ctx, domain.TradingAccess{Account: "default", Connection: paper}, false, caller); err != nil {
				t.Fatal(err)
			}
			if operation == "enable regrant" {
				if err := realm.PutTradingAccess(ctx, domain.TradingAccess{Account: "default", Connection: live}); err != nil {
					t.Fatal(err)
				}
				if err := app.Service().RemoveTradingAccess(ctx, live, "default", ""); err != nil {
					t.Fatal(err)
				}
			}
			beforeRestarts := runtime.restarts
			type outcome struct {
				connection domain.TradingConnection
				err        error
			}
			done := make(chan outcome, 1)
			go func() {
				if operation == "create" {
					created, err := app.Service().CreateTradingConnection(ctx, domain.TradingConnection{Provider: domain.TradingProviderAlpaca, Label: "auto-live", Mode: domain.TradingModeLive, Enabled: true, Credentials: `{}`}, false)
					done <- outcome{connection: created, err: err}
				} else {
					done <- outcome{err: app.Service().SetTradingConnectionEnabled(ctx, live.String(), true, false)}
				}
			}()
			defer func() {
				close(release)
				select {
				case result := <-done:
					if result.err != nil {
						t.Error(result.err)
					}
					if operation == "create" && (result.connection.ExternalID.IsZero() || result.connection.Credentials != "") {
						t.Errorf("committed connection result lost or credentials exposed: %+v", result.connection)
					}
				case <-time.After(3 * time.Second):
					t.Error("automatic grant did not finish after delivery")
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("automatic mixed grant did not reach delivery")
			}
			if runtime.restarts != beforeRestarts+1 {
				t.Fatalf("automatic grant delivery preceded runtime restart: got %d, want %d", runtime.restarts, beforeRestarts+1)
			}
			other := make(chan error, 1)
			go func() { other <- app.Service().SetTradingConnectionEnabled(ctx, paper.String(), false, false) }()
			select {
			case err := <-other:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("automatic grant delivery holds backend or node locks")
			}
			listed, err := service.List(ctx)
			if err != nil || listed.Total != 1 {
				t.Fatalf("automatic mixed warning not durably visible: %+v %v", listed, err)
			}
		})
	}
}

func TestMixedAlertChannelReentryAndRetainedContextOwnCompletion(t *testing.T) {
	var control backend.ControlPlane
	var live domain.ExternalID
	var retained context.Context
	var nestedErr error
	nestedLifetimes := make(chan (<-chan struct{}), 1)
	nestedDeliveryErr := errors.New("private nested channel failure")
	delivered := make(map[domain.AccountID]int)
	channel := alerts.Channel{Name: "reentrant", Deliver: func(ctx context.Context, event alerts.Event) error {
		var payload mixedTradingAccessPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		delivered[payload.Account]++
		switch payload.Account {
		case "desk":
			retained = ctx
			done, exited := make(chan error, 1), make(chan struct{})
			nestedLifetimes <- exited
			go func() {
				defer close(exited)
				done <- control.PutTradingAccess(ctx, domain.TradingAccess{Account: "other", Connection: live})
			}()
			select {
			case nestedErr = <-done:
			case <-time.After(2 * time.Second):
				nestedErr = errors.New("nested administrative call blocked during channel delivery")
				return nestedErr
			}
		case "other":
			return nestedDeliveryErr
		}
		return nil
	}}
	app, _, realm, _, service := mixedAlertApp(t, []alerts.Channel{channel})
	control = app.Service()
	defer func() {
		select {
		case exited := <-nestedLifetimes:
			select {
			case <-exited:
			case <-time.After(3 * time.Second):
				t.Error("nested call did not exit after outer completion")
			}
		default:
		}
	}()
	put := func(ctx context.Context, access domain.TradingAccess) error {
		done := make(chan error, 1)
		go func() { done <- control.PutTradingAccess(ctx, access) }()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			return errors.New("administrative call did not finish within deadline")
		}
	}
	ctx := auth.ContextWithCaller(t.Context(), domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator})
	paper, liveConnection := seedMixedAlertConnections(t, realm)
	live = liveConnection
	for _, account := range []domain.AccountID{"desk", "other", "hidden"} {
		if err := control.PutTradingAccess(ctx, domain.TradingAccess{Account: account, Connection: paper}); err != nil {
			t.Fatal(err)
		}
	}
	if err := put(ctx, domain.TradingAccess{Account: "desk", Connection: live}); err != nil {
		t.Fatalf("nested warning failure escaped into outer successful delivery: %v", err)
	}
	if !errors.Is(nestedErr, domain.ErrTradingAccessNotification) || !domain.OnlyTradingAccessNotification(nestedErr) || errors.Is(nestedErr, nestedDeliveryErr) {
		t.Fatalf("nested administrative call lost its synchronous warning failure: %v", nestedErr)
	}
	if retained == nil || delivered["desk"] != 1 || delivered["other"] != 1 {
		t.Fatalf("reentrant delivery lost: delivered=%v retained=%v", delivered, retained != nil)
	}
	list, err := service.List(ctx)
	if err != nil || list.Total != 2 {
		t.Fatalf("reentrant warning persistence: %+v %v", list, err)
	}
	if err := put(retained, domain.TradingAccess{Account: "hidden", Connection: live}); err != nil {
		t.Fatalf("retained channel context could not complete a later call: %v", err)
	}
	list, err = service.List(ctx)
	if err != nil || list.Total != 3 || delivered["hidden"] != 1 {
		t.Fatalf("completed collector swallowed retained-context warning: list=%+v delivered=%v err=%v", list, delivered, err)
	}
}

func TestMixedAlertBackendDeliveryReleasesTradingLock(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	app, _, realm, _, _ := mixedAlertApp(t, []alerts.Channel{{Name: "barrier", Deliver: func(context.Context, alerts.Event) error { close(entered); <-release; return nil }}})
	caller := domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator}
	ctx := auth.ContextWithCaller(t.Context(), caller)
	paper, live := seedMixedAlertConnections(t, realm)
	if err := app.Service().PutTradingAccess(ctx, domain.TradingAccess{Account: "desk", Connection: paper}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- app.Service().PutTradingAccess(ctx, domain.TradingAccess{Account: "desk", Connection: live})
	}()
	defer func() {
		close(release)
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("delivery failed to finish")
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("channel not entered")
	}
	unrelated := make(chan error, 1)
	go func() {
		unrelated <- app.Service().PutTradingAccess(ctx, domain.TradingAccess{Account: "other", Connection: paper})
	}()
	select {
	case err := <-unrelated:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow warning holds backend tradingMu or node serialization")
	}
	select {
	case err := <-done:
		t.Fatalf("administrative response did not wait for delivery: %v", err)
	default:
	}
}

func TestMixedAlertInstalledAccountScopeAcrossRESTAndMCP(t *testing.T) {
	for _, mode := range []string{accountmode.ModeSingle, accountmode.ModeMulti} {
		t.Run(mode, func(t *testing.T) {
			delivered := 0
			app, local, realm, opened, service := mixedAlertApp(t, []alerts.Channel{{Name: "trusted", Deliver: func(context.Context, alerts.Event) error { delivered++; return nil }}})
			handler, err := app.BuildServeHandler(nil, "/mcp")
			if err != nil {
				t.Fatal(err)
			}
			accesstest.CompleteOpen(t, handler, accountmode.Request{Mode: mode, Account: func() string {
				if mode == accountmode.ModeSingle {
					return "default"
				}
				return ""
			}()}, false)
			caller := domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator}
			ctx := auth.ContextWithCaller(t.Context(), caller)
			paper, live := seedMixedAlertConnections(t, realm)
			if err := local.PutTradingAccess(ctx, domain.TradingAccess{Account: "hidden", Connection: paper}, false, caller); err != nil {
				t.Fatal(err)
			}
			archive, err := realm.ExportBackup(ctx, backup.Scope{Sections: []backup.Section{backup.SectionTrading}})
			if err != nil {
				t.Fatal(err)
			}
			archive.Manifest.Source = "test"
			archive.Data.TradingAccess = append(archive.Data.TradingAccess, backup.TradingAccess{Account: "hidden", Connection: live})
			if _, err := app.Service().RestoreBackup(ctx, archive, backup.RestoreOptions{Scope: backup.Scope{Sections: []backup.Section{backup.SectionTrading}}, Mode: backup.RestoreModeOverwrite}); err != nil {
				t.Fatal(err)
			}
			rows, err := realm.(alerts.Store).ListAlerts(ctx, domain.PrincipalOperator)
			if err != nil || len(rows) != 1 || delivered != 1 {
				t.Fatalf("trusted persistence/delivery rows=%+v deliveries=%d err=%v", rows, delivered, err)
			}
			id := rows[0].ID
			agentCtx := auth.ContextWithCaller(t.Context(), domain.Caller{Source: domain.SourceMCP, Principal: domain.PrincipalOperator})
			listed, err := service.List(agentCtx)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode == accountmode.ModeSingle {
				want = 0
			}
			if listed.Total != want || listed.Unread != want || len(listed.Alerts) != want {
				t.Fatalf("scoped list=%+v", listed)
			}
			if mode == accountmode.ModeSingle {
				if _, err := service.Detail(agentCtx, id); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("hidden detail: %v", err)
				}
				if err := service.SetRead(agentCtx, id, true); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("hidden read: %v", err)
				}
				if _, err := service.Resolve(agentCtx, id, "acknowledge", json.RawMessage(`{}`)); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("hidden decision: %v", err)
				}
				stored, err := realm.(alerts.Store).GetAlert(ctx, id, domain.PrincipalOperator)
				if err != nil || stored.Read {
					t.Fatalf("hidden state changed: %+v %v", stored, err)
				}
			}
			for _, name := range []string{"list_alerts", "get_alert", "set_alert_read", "resolve_alert"} {
				if err := app.Service().SetMcpAccess(ctx, name, true); err != nil {
					t.Fatal(err)
				}
			}
			const apiToken = "pit_api_alert-scope"
			enableTokenMode(t, opened.(access.Store), apiToken)
			request := func(method, path, body, token, session string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("Authorization", "Bearer "+token)
				if session != "" {
					req.Header.Set("Mcp-Session-Id", session)
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				return rec
			}
			if rec := request("GET", "/api/v1/alerts", "", releasedAgentToken, ""); rec.Code != http.StatusForbidden {
				t.Fatalf("agent REST credential unexpectedly admitted: %d %s", rec.Code, rec.Body)
			}
			rest := request("GET", "/api/v1/alerts", "", apiToken, "")
			if rest.Code != 200 || strings.Contains(rest.Body.String(), "Secret desk") != (want == 1) {
				t.Fatalf("scoped REST list=%d %s", rest.Code, rest.Body)
			}
			if mode == accountmode.ModeSingle {
				for _, test := range []struct{ method, path, body string }{{"GET", "/api/v1/alerts/" + id, ""}, {"PUT", "/api/v1/alerts/" + id + "/read", `{"read":true}`}, {"POST", "/api/v1/alerts/" + id + "/actions/acknowledge", `{"input":{}}`}} {
					rec := request(test.method, test.path, test.body, apiToken, "")
					if rec.Code != 404 || strings.Contains(rec.Body.String(), "hidden") || strings.Contains(rec.Body.String(), "Secret desk") {
						t.Fatalf("hidden REST %s: %d %s", test.path, rec.Code, rec.Body)
					}
				}
			}
			init := request("POST", "/mcp", mcpInitializeBody, releasedAgentToken, "")
			if init.Code != 200 {
				t.Fatalf("MCP init: %d %s", init.Code, init.Body)
			}
			session := init.Header().Get("Mcp-Session-Id")
			request("POST", "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, releasedAgentToken, session)
			for index, test := range []struct {
				name string
				args map[string]any
			}{{"list_alerts", map[string]any{}}, {"get_alert", map[string]any{"id": id}}, {"set_alert_read", map[string]any{"id": id, "read": true}}, {"resolve_alert", map[string]any{"id": id, "action": "acknowledge", "input": map[string]any{}}}} {
				body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": index + 2, "method": "tools/call", "params": map[string]any{"name": test.name, "arguments": test.args}})
				rec := request("POST", "/mcp", string(body), releasedAgentToken, session)
				if rec.Code != 200 {
					t.Fatalf("MCP %s: %d %s", test.name, rec.Code, rec.Body)
				}
				text := rec.Body.String()
				if want == 0 && (strings.Contains(text, "Secret desk") || strings.Contains(text, `\"account\":\"hidden\"`)) {
					t.Fatalf("MCP leaked hidden payload: %s", text)
				}
				if test.name == "list_alerts" {
					if !strings.Contains(text, fmt.Sprintf(`"total":%d`, want)) {
						t.Fatalf("MCP list counts: %s", text)
					}
				} else if strings.Contains(text, `"isError":true`) != (want == 0) {
					t.Fatalf("MCP %s visibility: %s", test.name, text)
				}
			}
		})
	}
}

func TestRegisteredMixedTradingAccessAlertAndAcknowledgement(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"account":"desk","title":null}`, `{"account":null,"title":"Desk"}`} {
		if err := mixedTradingAccessType(func(context.Context, domain.AccountID) (bool, error) { return true, nil }).Validate(json.RawMessage(raw)); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid payload accepted: %s, %v", raw, err)
		}
	}
	var local node.Node
	_, realm, _ := buildTestApp(t, nil, nil, func(builder *frameworkapp.Builder) {
		builder.AddNodeHook(func(_ context.Context, _ store.Store, n node.Node) error { local = n; return nil })
	})
	caller := domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator}
	ctx := auth.ContextWithCaller(context.Background(), caller)
	if _, err := realm.CreateAccount(ctx, domain.Account{Code: "desk", Title: "Desk"}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []domain.TradingMode{domain.TradingModePaper, domain.TradingModeLive} {
		connection, err := realm.CreateTradingConnection(ctx, domain.TradingConnection{Provider: "alpaca", Mode: mode, Label: string(mode), VenueIdentity: string(mode), Credentials: `{}`})
		if err != nil {
			t.Fatal(err)
		}
		if err := local.PutTradingAccess(ctx, domain.TradingAccess{Account: "desk", Connection: connection.ExternalID}, false, caller); err != nil {
			t.Fatal(err)
		}
	}
	state := realm.(alerts.Store)
	service, err := alerts.New(state, []alerts.Type{mixedTradingAccessType(func(context.Context, domain.AccountID) (bool, error) { return true, nil })}, nil)
	if err != nil {
		t.Fatal(err)
	}
	list, err := service.List(ctx)
	if err != nil || list.Total != 1 {
		t.Fatalf("registered publication: %+v, %v", list, err)
	}
	event := list.Alerts[0]
	var payload mixedTradingAccessPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.Account != "desk" || payload.Title != "Desk" || event.Type != mixedTradingAccessAlert {
		t.Fatalf("payload=%+v event=%+v err=%v", payload, event, err)
	}
	if len(event.Deliveries) != 1 || event.Deliveries[0].Channel != "panel" || event.Deliveries[0].Failed {
		t.Fatalf("panel delivery: %+v", event.Deliveries)
	}
	before, err := realm.ListTradingAccess(ctx, "desk")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(ctx, event.ID, "acknowledge", json.RawMessage(`{"changeAccess":true}`)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid acknowledgement: %v", err)
	}
	result, err := service.Resolve(ctx, event.ID, "acknowledge", json.RawMessage(`{}`))
	if err != nil || result.Actor != caller.Principal || result.Source != caller.Source || string(result.Result) != `{"acknowledged":true}` {
		t.Fatalf("acknowledgement=%+v, %v", result, err)
	}
	after, err := realm.ListTradingAccess(ctx, "desk")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("acknowledgement changed access: %+v, %v", after, err)
	}
	list, err = service.List(ctx)
	if err != nil || list.Total != 0 {
		t.Fatalf("resolved list=%+v, %v", list, err)
	}
}

func TestMixedTradingAccessDeliveryFailurePersistsOnePublication(t *testing.T) {
	_, realm, _ := buildTestApp(t, nil, nil)
	ctx := auth.ContextWithCaller(context.Background(), domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator})
	state := realm.(alerts.Store)
	service, err := alerts.New(state, []alerts.Type{mixedTradingAccessType(func(context.Context, domain.AccountID) (bool, error) { return true, nil })}, []alerts.Channel{{Name: "failed", Deliver: func(ctx context.Context, event alerts.Event) error {
		if _, err := state.GetAlert(ctx, event.ID, domain.PrincipalOperator); err != nil {
			t.Fatalf("delivered before persistence: %v", err)
		}
		return errors.New("transport secret")
	}}})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishMixedTradingAccess(service)(ctx, domain.Account{Code: "desk", Title: "Desk"}, caller); err == nil {
		t.Fatal("delivery failure hidden")
	}
	list, err := service.List(ctx)
	if err != nil || list.Total != 1 {
		t.Fatalf("durable failed publication=%+v, %v", list, err)
	}
	event := list.Alerts[0]
	failed := false
	for _, delivery := range event.Deliveries {
		failed = failed || delivery.Channel == "failed" && delivery.Failed
	}
	if !failed {
		t.Fatalf("failure receipt missing: %+v", event.Deliveries)
	}
	if err := service.Publish(ctx, event.Event); err == nil {
		t.Fatal("retry hid channel failure")
	}
	list, err = service.List(ctx)
	if err != nil || list.Total != 1 || list.Alerts[0].ID != event.ID {
		t.Fatalf("same-publication retry duplicated event: %+v, %v", list, err)
	}
}
