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

package integration_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/openpit/asyncengine"
)

func isolationProviders() []trading.Provider {
	return []trading.Provider{
		{Type: "isolation-test", ExecutionPolicy: true, Build: isolationUnusedBuild},
		{Type: "isolation-venue", ExecutionPolicy: false, Build: isolationUnusedBuild},
	}
}

func isolationUnusedBuild(trading.BuildContext) (trading.Connector, error) {
	return nil, errors.New("fixture connector must not be built")
}

func newIsolationNode(t *testing.T) (node.Node, store.RealmStore, fwengine.Engine, *int, [2]domain.ExternalID) {
	t.Helper()
	build := engine.NewOpenPitEngineBuildFunc()
	var live fwengine.Engine
	builds := new(int)
	n, realm := newLocalNodeBuiltBy(t, func(snapshot fwengine.Snapshot) (fwengine.Engine, error) {
		*builds++
		var err error
		live, err = build(snapshot)
		return live, err
	}, isolationProviders()...)
	ctx := systemCtx()
	for _, code := range []string{"old", "opposite", "empty"} {
		if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: code}, auth.SystemCaller()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := n.CreateAccount(ctx, domain.Account{Code: "A", GroupCode: "old"}, auth.SystemCaller()); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"AAPL", "USD"} {
		if _, err := n.CreateAsset(ctx, domain.Asset{Code: code}, auth.SystemCaller()); err != nil {
			t.Fatal(err)
		}
	}
	var connections [2]domain.ExternalID
	for i, p := range isolationProviders() {
		mode := []domain.TradingMode{domain.TradingModePaper, domain.TradingModeLive}[i]
		c, err := realm.CreateTradingConnection(ctx, domain.TradingConnection{
			Provider: p.Type, Label: p.Type, VenueIdentity: p.Type, Mode: mode, Credentials: "{}",
		})
		if err != nil {
			t.Fatal(err)
		}
		connections[i] = c.ExternalID
	}
	return n, realm, live, builds, connections
}

func requireIsolation409(t *testing.T, err error, account domain.AccountID) {
	t.Helper()
	if err != nil {
		t.Fatalf("mixed access for account %s refused: %v", account, err)
	}
}

func TestMixedTradingAccessTransitionSequence(t *testing.T) {
	n, realm, _, _, connections := newIsolationNode(t)
	ctx := systemCtx()
	var events []domain.AccountID
	if err := n.(node.TradingAccessObserverHost).InstallTradingAccessObserver(func(_ context.Context, account domain.Account, _ domain.Caller) error {
		events = append(events, account.Code)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	own := [2]bool{}
	groups := map[string][2]bool{"old": {}, "opposite": {}}
	group := "old"
	mixed := func() bool { inherited := groups[group]; return (own[0] || inherited[0]) && (own[1] || inherited[1]) }
	wantEvents := 0
	// A deterministic generated sequence models direct and inherited grants,
	// membership, no-ops and repeated unmix/re-mix transitions independently.
	for step := range 140 {
		before := mixed()
		mode := ((step * 37) ^ (step / 3)) % 2
		connection := connections[mode]
		var err error
		switch step % 7 {
		case 0:
			err = n.PutTradingAccess(ctx, domain.TradingAccess{Account: "A", Connection: connection}, false, auth.SystemCaller())
			own[mode] = true
		case 1, 2:
			code := "old"
			if step%7 == 2 {
				code = "opposite"
			}
			err = n.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: code, Connection: connection}, false, auth.SystemCaller())
			grants := groups[code]
			grants[mode] = true
			groups[code] = grants
		case 3:
			err = n.RemoveTradingAccess(ctx, connection, "A", "", false, auth.SystemCaller())
			own[mode] = false
		case 4, 5:
			code := "old"
			if step%7 == 5 {
				code = "opposite"
			}
			err = n.RemoveTradingAccess(ctx, connection, "", code, false, auth.SystemCaller())
			grants := groups[code]
			grants[mode] = false
			groups[code] = grants
		case 6:
			group = "old"
			if (step/14)%2 != 0 {
				group = "opposite"
			}
			err = n.SetAccountGroup(ctx, "A", group, domain.MissingAccountReject, auth.SystemCaller())
		}
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		if !before && mixed() {
			wantEvents++
		}
		if len(events) != wantEvents {
			t.Fatalf("step %d: events=%v want count %d", step, events, wantEvents)
		}
	}
	if wantEvents == 0 {
		t.Fatal("sequence did not exercise a mixed transition")
	}
	for _, connection := range connections {
		if err := n.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: "empty", Connection: connection}, false, auth.SystemCaller()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := n.CreateAccount(ctx, domain.Account{Code: "new", GroupCode: "empty"}, auth.SystemCaller()); err != nil {
		t.Fatal(err)
	}
	if len(events) != wantEvents+1 || events[len(events)-1] != "new" {
		t.Fatalf("create into mixed group: %v", events)
	}
	beforeCount := len(events)
	if err := n.PutTradingAccess(ctx, domain.TradingAccess{Account: "missing", Connection: connections[0]}, false, auth.SystemCaller()); err == nil {
		t.Fatal("missing subject accepted")
	}
	if len(events) != beforeCount {
		t.Fatal("failed grant emitted")
	}
	// Both connections remain disabled throughout: access is independent of health.
	for _, connection := range connections {
		row, _, err := realm.GetTradingConnection(ctx, connection)
		if err != nil || row.Enabled {
			t.Fatalf("disabled fixture: %+v, %v", row, err)
		}
	}
}

func TestNodeTradingIsolationMoveReversed(t *testing.T) {
	n, realm, live, builds, c := newIsolationNode(t)
	ctx := systemCtx()
	if err := realm.PutTradingAccess(ctx, domain.TradingAccess{Account: "A", Connection: c[0]}); err != nil {
		t.Fatal(err)
	}
	if err := realm.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: "opposite", Connection: c[1]}); err != nil {
		t.Fatal(err)
	}
	before, err := realm.ListAudit(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	requireIsolation409(t, n.SetAccountGroup(ctx, "A", "opposite", domain.MissingAccountReject, auth.SystemCaller()), "A")
	stored, ok, err := realm.GetAccount(ctx, "A")
	if err != nil || !ok || stored.GroupCode != "opposite" {
		t.Fatalf("stored account = %+v, %t, %v", stored, ok, err)
	}
	account, err := live.AccountID("A")
	if err != nil {
		t.Fatal(err)
	}
	want, err := live.ResolveGroup("opposite")
	if err != nil {
		t.Fatal(err)
	}
	if err := fwengine.RunExclusive(ctx, live.AsyncEngine(), func(driver asyncengine.Driver) error {
		got, ok := driver.Accounts().GroupOf(account).Get()
		if !ok || got != want {
			t.Errorf("engine group = %v, %t; want old %v", got, ok, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	after, err := realm.ListAudit(ctx, 1000)
	if err != nil || reflect.DeepEqual(before, after) {
		t.Fatalf("successful move must add audit: %v", err)
	}
	_, err = n.CheckOrder(ctx, domain.OrderProbe{Account: "A", BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "100"})
	if err != nil {
		t.Fatalf("trade path after refusal: %v", err)
	}
	if *builds != 1 {
		t.Fatalf("engine builds = %d; refusal must stay online", *builds)
	}
}

func TestNodeTradingIsolationCreateHasNoAlias(t *testing.T) {
	n, realm, live, builds, c := newIsolationNode(t)
	ctx := systemCtx()
	for _, connection := range c {
		if err := realm.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: "empty", Connection: connection}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := n.CreateAccount(ctx, domain.Account{Code: "refused", GroupCode: "empty"}, auth.SystemCaller())
	if err != nil {
		t.Fatalf("create mixed account: %v", err)
	}
	if _, ok, err := realm.GetAccount(ctx, "refused"); err != nil || !ok {
		t.Fatalf("store account exists=%t, %v", ok, err)
	}
	if _, err := live.AccountID("refused"); err != nil {
		t.Fatalf("created account missing engine alias: %v", err)
	}
	if *builds != 1 {
		t.Fatalf("create refusal rebuilt engine %d times", *builds)
	}
}

func TestNodeTradingIsolationSystemCaller(t *testing.T) {
	n, realm, _, _, c := newIsolationNode(t)
	ctx := systemCtx()
	if err := n.PutTradingAccess(ctx, domain.TradingAccess{Account: "A", Connection: c[0]}, false, auth.SystemCaller()); err != nil {
		t.Fatal(err)
	}
	requireIsolation409(t, n.PutTradingAccess(ctx, domain.TradingAccess{Account: "A", Connection: c[1]}, false, auth.SystemCaller()), "A")
	grants, err := realm.ListTradingAccess(ctx, "A")
	if err != nil || len(grants) != 2 {
		t.Fatalf("grants = %+v, %v", grants, err)
	}
}

func TestBackendTradingIsolationPublicMessages(t *testing.T) {
	for _, path := range []string{"account grant", "group grant", "SetAccountGroup", "CreateAccount", "restore"} {
		t.Run(path, func(t *testing.T) {
			n, realm, _, _, c := newIsolationNode(t)
			ctx := systemCtx()
			account := domain.AccountID(strings.Repeat("\U0001F600", 64))
			if path != "CreateAccount" {
				if _, err := n.CreateAccount(ctx, domain.Account{Code: account, GroupCode: "old"}, auth.SystemCaller()); err != nil {
					t.Fatal(err)
				}
				if err := realm.PutTradingAccess(ctx, domain.TradingAccess{Account: account, Connection: c[0]}); err != nil {
					t.Fatal(err)
				}
			}
			var options []backend.Option
			if path != "restore" {
				options = append(options, backend.WithTradingRuntime(&isolationRuntime{}))
			}
			svc, err := backend.New(n, nil, nil, engine.LockSettlementPrice, options...)
			if err != nil {
				t.Fatal(err)
			}
			switch path {
			case "account grant":
				err = svc.PutTradingAccess(ctx, domain.TradingAccess{Account: account, Connection: c[1]})
			case "group grant":
				err = svc.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: "old", Connection: c[1]})
			case "SetAccountGroup", "restore":
				if err := realm.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: "opposite", Connection: c[1]}); err != nil {
					t.Fatal(err)
				}
				if path == "SetAccountGroup" {
					err = svc.SetAccountGroup(ctx, account, "opposite", domain.MissingAccountReject)
				} else {
					scope := backup.Scope{Sections: []backup.Section{backup.SectionAccountsGroups}}
					archive, exportErr := realm.ExportBackup(ctx, scope)
					if exportErr != nil {
						t.Fatal(exportErr)
					}
					archive.Manifest.Source = "isolation message test"
					archive.CredentialForm = backup.CredentialFormPlaintext
					for i := range archive.Data.Accounts {
						if archive.Data.Accounts[i].Code == account.String() {
							archive.Data.Accounts[i].GroupCode = "opposite"
						}
					}
					_, err = svc.RestoreBackup(ctx, archive, backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeOverwrite})
				}
			case "CreateAccount":
				for _, connection := range c {
					if err := realm.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: "empty", Connection: connection}); err != nil {
						t.Fatal(err)
					}
				}
				_, err = svc.CreateAccount(ctx, domain.Account{Code: account, GroupCode: "empty"})
			}
			requireIsolation409(t, err, account)
		})
	}
}

func TestMixedTradingAccessBatchFailureAndRestore(t *testing.T) {
	t.Run("all affected accounts attempted after committed failure", func(t *testing.T) {
		n, realm, _, _, connections := newIsolationNode(t)
		ctx := systemCtx()
		if _, err := n.CreateAccount(ctx, domain.Account{Code: "B", GroupCode: "old"}, auth.SystemCaller()); err != nil {
			t.Fatal(err)
		}
		for _, code := range []domain.AccountID{"A", "B"} {
			if err := n.PutTradingAccess(ctx, domain.TradingAccess{Account: code, Connection: connections[0]}, false, auth.SystemCaller()); err != nil {
				t.Fatal(err)
			}
		}
		var attempted []domain.AccountID
		if err := n.(node.TradingAccessObserverHost).InstallTradingAccessObserver(func(_ context.Context, account domain.Account, _ domain.Caller) error {
			attempted = append(attempted, account.Code)
			if account.Code == "A" {
				return errors.New("delivery failed")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		err := n.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: "old", Connection: connections[1]}, false, auth.SystemCaller())
		if !errors.Is(err, domain.ErrTradingAccessNotification) || len(attempted) != 2 || attempted[0] == attempted[1] {
			t.Fatalf("batch publication=%v, %v", attempted, err)
		}
		for _, code := range []domain.AccountID{"A", "B"} {
			access, err := realm.ListTradingAccess(ctx, code)
			if err != nil || len(access) != 2 {
				t.Fatalf("committed access %s=%+v, %v", code, access, err)
			}
		}
	})
	t.Run("actual restore and failed restore", func(t *testing.T) {
		n, realm, _, _, connections := newIsolationNode(t)
		ctx := systemCtx()
		for _, connection := range connections {
			if err := n.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: "empty", Connection: connection}, false, auth.SystemCaller()); err != nil {
				t.Fatal(err)
			}
		}
		var events []domain.AccountID
		if err := n.(node.TradingAccessObserverHost).InstallTradingAccessObserver(func(_ context.Context, account domain.Account, _ domain.Caller) error {
			events = append(events, account.Code)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		scope := backup.Scope{Sections: []backup.Section{backup.SectionAccountsGroups}}
		archive, err := realm.ExportBackup(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		archive.Manifest.Source = "mixed-access restore test"
		archive.CredentialForm = backup.CredentialFormPlaintext
		for index := range archive.Data.Accounts {
			if archive.Data.Accounts[index].Code == "A" {
				archive.Data.Accounts[index].GroupCode = "empty"
			}
		}
		options := backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeOverwrite}
		if _, _, err := n.RestoreBackup(ctx, archive, options, auth.SystemCaller()); err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0] != "A" {
			t.Fatalf("restore events=%v", events)
		}
		if _, _, err := n.RestoreBackup(ctx, archive, options, auth.SystemCaller()); err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 {
			t.Fatalf("no-op restore emitted: %v", events)
		}
		archive.Data.Accounts = append(archive.Data.Accounts, backup.Account{Code: ""})
		if _, _, err := n.RestoreBackup(ctx, archive, options, auth.SystemCaller()); err == nil {
			t.Fatal("invalid restore accepted")
		}
		if len(events) != 1 {
			t.Fatalf("failed restore emitted: %v", events)
		}
	})
}

type isolationSingleScope struct {
	backend.EveryAccount
	account domain.AccountID
}

func (s isolationSingleScope) Features() backend.AccountFeatures {
	return backend.AccountFeatures{Account: s.account}
}

type isolationRuntime struct {
	backend.TradingRuntime
	restarts int
}

func (*isolationRuntime) Providers() []trading.Provider { return isolationProviders() }
func (*isolationRuntime) CheckAccount(context.Context, domain.TradingConnection) (trading.AccountInfo, error) {
	return trading.AccountInfo{Identity: "new-venue"}, nil
}
func (r *isolationRuntime) Restart() error { r.restarts++; return nil }

func TestBackendTradingIsolationSingleCreateKeepsConnection(t *testing.T) {
	n, realm, _, _, c := newIsolationNode(t)
	ctx := systemCtx()
	account := domain.AccountID(strings.Repeat("\U0001F600", 64))
	if _, err := n.CreateAccount(ctx, domain.Account{Code: account}, auth.SystemCaller()); err != nil {
		t.Fatal(err)
	}
	runtime := &isolationRuntime{}
	svc, err := backend.New(n, nil, nil, engine.LockSettlementPrice, backend.WithTradingRuntime(runtime), backend.WithAccountScope(isolationSingleScope{account: account}))
	if err != nil {
		t.Fatal(err)
	}
	if err := realm.PutTradingAccess(ctx, domain.TradingAccess{Account: account, Connection: c[0]}); err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreateTradingConnection(ctx, domain.TradingConnection{Provider: "isolation-venue", Label: "live", Mode: domain.TradingModeLive, Credentials: "{}"}, true)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := realm.ListTradingAccess(ctx, account)
	if err != nil || len(grants) != 2 {
		t.Fatalf("effective grants after create = %+v, %v", grants, err)
	}
	if err := svc.SetTradingConnectionEnabled(ctx, created.ExternalID.String(), true, true); err != nil {
		t.Fatal(err)
	}
}
