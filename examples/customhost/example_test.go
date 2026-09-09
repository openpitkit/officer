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
// Please see https://openpit.dev and the OWNERS file for details.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/app"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/configure"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/reject"
)

func TestCustomHostCompositionAddReplaceHideRemove(t *testing.T) {
	ctx := context.Background()
	composition, err := newCustomHostComposition()
	if err != nil {
		t.Fatalf("newCustomHostComposition: %v", err)
	}
	composition.Builder.SetEngineBuildFactory(func(app.Config) engine.BuildFunc {
		return func(engine.Snapshot) (engine.Engine, error) {
			return &fakeEngine{running: true, sink: &fakeSink{}}, nil
		}
	})
	composition.Builder.SetSPAFactory(func() (fs.FS, error) {
		return fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}, nil
	})

	built, err := composition.Builder.Build(
		ctx,
		app.Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")},
		slog.Default(),
		nil,
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = built.Close() })

	router, err := built.BuildServeHandler(nil, "/mcp")
	if err != nil {
		t.Fatalf("BuildServeHandler: %v", err)
	}
	service := built.Service()

	assertHostRouteAdded(t, router)
	assertHostToolCatalogued(t, service)
	assertHostProvider(t, ctx, service)
	assertRouteAndToolReplacement(t, ctx, router, service)
	assertRouteAndToolHidden(t, router, composition.Authorizer, service)
	assertRouteAndToolRemoved(t, router, service)
}

func TestHostConnectorSubscribeEmptyClosesImmediately(t *testing.T) {
	t.Parallel()
	updates, err := newHostConnector().Subscribe(t.Context(), nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	select {
	case _, ok := <-updates:
		if ok {
			t.Fatal("empty subscription produced an update")
		}
	default:
		t.Fatal("empty subscription channel is not closed")
	}
}

func TestHostConnectorSubscribeReturnsRequestedAssetIDs(t *testing.T) {
	t.Parallel()
	const (
		base  domain.EngineAssetID = 17
		quote domain.EngineAssetID = 29
	)
	cases := []struct {
		name string
		stop func(*hostConnector, context.CancelFunc)
	}{
		{
			name: "context cancellation",
			stop: func(_ *hostConnector, cancel context.CancelFunc) {
				cancel()
			},
		},
		{
			name: "Close",
			stop: func(connector *hostConnector, _ context.CancelFunc) {
				connector.Close()
				connector.Close()
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			connector := newHostConnector()
			updates, err := connector.Subscribe(
				ctx,
				[]marketdata.Subscription{{
					External: "AAPL",
					Base:     base,
					Quote:    quote,
				}},
			)
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			waitCtx, stopWaiting := context.WithTimeout(t.Context(), time.Second)
			defer stopWaiting()
			select {
			case update, ok := <-updates:
				if !ok {
					t.Fatal("subscription channel closed before its update")
				}
				if update.Base != base || update.Quote != quote {
					t.Fatalf(
						"update asset ids = %d/%d, want %d/%d",
						update.Base,
						update.Quote,
						base,
						quote,
					)
				}
			case <-waitCtx.Done():
				t.Fatal("timed out waiting for subscription update")
			}
			select {
			case _, ok := <-updates:
				if !ok {
					t.Fatal("subscription channel closed after its update")
				}
				t.Fatal("subscription produced an unexpected extra update")
			default:
			}
			test.stop(connector, cancel)
			closeWaitCtx, stopCloseWait := context.WithTimeout(
				t.Context(),
				time.Second,
			)
			defer stopCloseWait()
			select {
			case _, ok := <-updates:
				if ok {
					t.Fatal("subscription channel remained open after stop")
				}
			case <-closeWaitCtx.Done():
				t.Fatal("timed out waiting for subscription channel close")
			}
		})
	}
}

func assertHostRouteAdded(t *testing.T, router http.Handler) {
	t.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/api/v1"+hostRoutePattern, nil),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("custom host route status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode custom host route: %v", err)
	}
	if body["example"] != "ok" {
		t.Fatalf("custom host route body = %#v", body)
	}
}

func assertHostToolCatalogued(t *testing.T, service backend.ControlPlane) {
	t.Helper()

	cmd, ok := mcpCommand(t, service, hostToolID)
	if !ok {
		t.Fatalf("custom host tool missing from catalog")
	}
	if cmd.Command.DefaultEnabled {
		t.Fatalf("custom host tool should be disabled by default: %+v", cmd)
	}
}

func assertHostProvider(
	t *testing.T,
	ctx context.Context,
	service backend.ControlPlane,
) {
	t.Helper()

	status, err := service.ListMarketData(ctx)
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	for _, provider := range status.Providers {
		if provider.Type == hostProviderID {
			return
		}
	}
	t.Fatalf("custom host provider %q missing from %+v", hostProviderID, status.Providers)
}

func assertRouteAndToolReplacement(
	t *testing.T,
	ctx context.Context,
	router http.Handler,
	service backend.ControlPlane,
) {
	t.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "replaced") {
		t.Fatalf("replacement route = %d %q", rec.Code, rec.Body.String())
	}
	cmd, ok := mcpCommand(t, service, replacedToolID)
	if !ok || cmd.Command.Title != "Custom host health replacement" {
		t.Fatalf("replacement tool catalog entry = %+v, ok=%v", cmd, ok)
	}
	if err := service.SetMcpAccess(ctx, replacedToolID, false); err != nil {
		t.Fatalf("SetMcpAccess(replaced): %v", err)
	}
}

func assertRouteAndToolHidden(
	t *testing.T,
	router http.Handler,
	authorizer httpx.Authorizer,
	service backend.ControlPlane,
) {
	t.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("hidden route status = %d, want 403", rec.Code)
	}
	if _, ok := mcpCommand(t, service, hiddenToolID); !ok {
		t.Fatalf("hidden tool should remain catalogued")
	}

	hidden := mcp.Guard(
		mcp.ToolDescriptor{Name: hiddenToolID, DefaultEnabled: true},
		mcp.RegisterDeps{Source: &customHostSource{enabled: true}, Authorizer: authorizer},
		func(
			context.Context,
			*sdkmcp.ServerSession,
			struct{},
		) (string, string, error) {
			t.Fatal("hidden tool body must not run")
			return "", "", nil
		},
	)
	res, err := hidden(
		context.Background(),
		nil,
		&sdkmcp.CallToolParamsFor[struct{}]{},
	)
	if err != nil {
		t.Fatalf("hidden tool transport error: %v", err)
	}
	if !res.IsError || !toolContentContains(res.Content, "permission denied") {
		t.Fatalf("hidden tool result = %+v", res)
	}
}

func assertRouteAndToolRemoved(
	t *testing.T,
	router http.Handler,
	service backend.ControlPlane,
) {
	t.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil))
	if rec.Code != http.StatusMethodNotAllowed || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf(
			"removed route response = %d %q, want method not allowed",
			rec.Code,
			rec.Body.String(),
		)
	}
	if _, ok := mcpCommand(t, service, removedToolID); ok {
		t.Fatalf("removed tool %q still catalogued", removedToolID)
	}
	if _, ok := mcpCommand(t, service, "get_account_state"); !ok {
		t.Fatalf("untouched framework tool get_account_state is missing")
	}
}

func mcpCommand(
	t *testing.T,
	service backend.ControlPlane,
	name string,
) (backend.McpCommand, bool) {
	t.Helper()

	commands, err := service.ListMcpAccess(context.Background())
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	for _, cmd := range commands {
		if cmd.Command.Name == name {
			return cmd, true
		}
	}
	return backend.McpCommand{}, false
}

type customHostSource struct {
	enabled bool
}

func (s *customHostSource) Status(context.Context) (mcp.Status, error) {
	return mcp.Status{}, nil
}

func (s *customHostSource) GetAccountState(context.Context, domain.AccountID) (
	domain.Account,
	node.AccountLimits,
	error,
) {
	return domain.Account{}, node.AccountLimits{}, nil
}

func (s *customHostSource) ListGroups(context.Context) (
	[]domain.AccountGroup,
	error,
) {
	return nil, nil
}

func (s *customHostSource) ListLimits(context.Context, domain.AccountID) (
	node.AccountLimits,
	error,
) {
	return node.AccountLimits{}, nil
}

func (s *customHostSource) ListAudit(context.Context, int) ([]domain.AuditRow, error) {
	return nil, nil
}

func (s *customHostSource) ListAuditFiltered(
	context.Context,
	domain.AuditFilter,
	int,
) ([]domain.AuditRow, error) {
	return nil, nil
}

func (s *customHostSource) CheckOrder(
	context.Context,
	domain.OrderProbe,
) (domain.CheckResult, error) {
	return domain.CheckResult{}, nil
}

func (s *customHostSource) GetOrder(
	context.Context,
	string,
) (domain.OrderDetail, error) {
	return domain.OrderDetail{}, nil
}

func (s *customHostSource) SetMarketDataInstrumentEnabled(
	context.Context,
	string,
	string,
	bool,
) error {
	return nil
}

func (s *customHostSource) CommandEnabled(context.Context, string) (bool, error) {
	return s.enabled, nil
}

func (s *customHostSource) SubmitOrderToken(
	context.Context,
	domain.Order,
	string,
	domain.MissingAccountPolicy,
) (mcp.SubmitOrderTokenResult, error) {
	return mcp.SubmitOrderTokenResult{}, nil
}

func (s *customHostSource) SubmitDropCopyOrder(
	context.Context, domain.Order, domain.MissingAccountPolicy,
) (mcp.SubmitDropCopyOrderResult, error) {
	return mcp.SubmitDropCopyOrderResult{}, nil
}

func (s *customHostSource) ConfirmExecution(
	context.Context,
	string,
	string,
) (domain.Order, mcp.Attestation, error) {
	return domain.Order{}, mcp.Attestation{}, nil
}

func (s *customHostSource) CancelOrder(
	context.Context,
	string,
	string,
	string,
	string,
) (domain.Order, mcp.Attestation, error) {
	return domain.Order{}, mcp.Attestation{}, nil
}

func toolContentContains(content []sdkmcp.Content, needle string) bool {
	for _, item := range content {
		text, ok := item.(*sdkmcp.TextContent)
		if ok && strings.Contains(text.Text, needle) {
			return true
		}
	}
	return false
}

type fakeSink struct{}

func (s *fakeSink) Push(marketdata.QuoteUpdate) error { return nil }

type fakeEngine struct {
	sink    marketdata.Sink
	running bool
}

var errUnexpectedOrderChain = errors.New("unexpected order chain call")

func (*fakeEngine) AsyncEngine() *asyncengine.AsyncEngine {
	panic(errUnexpectedOrderChain)
}

func (*fakeEngine) AccountID(domain.AccountID) (param.AccountID, error) {
	return param.AccountID{}, errUnexpectedOrderChain
}

func (*fakeEngine) ExecutionReportModel(
	domain.ExecutionReportInput, string,
) (model.ExecutionReport, error) {
	return model.ExecutionReport{}, errUnexpectedOrderChain
}

func (*fakeEngine) SettledExecutionReport(
	domain.ExecutionReportInput,
	param.AccountID,
	pretrade.PostTradeResult,
) (engine.ExecutionReportResult, error) {
	return engine.ExecutionReportResult{}, errUnexpectedOrderChain
}

func (*fakeEngine) AccountAdjustmentModels(
	[]domain.AdjustmentRequest,
) ([]model.AccountAdjustment, error) {
	return nil, errUnexpectedOrderChain
}

func (*fakeEngine) AppliedAccountAdjustmentBatch(
	domain.AccountID,
	[]domain.AdjustmentRequest,
	accountadjustment.BatchResult,
) ([]engine.AdjustmentResult, *engine.AdjustmentBatchReject, error) {
	return nil, nil, errUnexpectedOrderChain
}

func (*fakeEngine) SpotFundsAccountPnlAssignment(
	string,
	domain.PnlHaltReason,
) (asyncengine.SpotFundsAccountPnlAssignment, error) {
	return asyncengine.SpotFundsAccountPnlAssignment{}, errUnexpectedOrderChain
}

func (*fakeEngine) AppliedSpotFundsAccountPnl(
	domain.AccountID,
	string,
	domain.PnlHaltReason,
	configure.PolicyConfigurationResult,
) ([]domain.AccountBlock, error) {
	return nil, errUnexpectedOrderChain
}

func (*fakeEngine) OrderModel(domain.Order) (model.Order, error) {
	return model.Order{}, errUnexpectedOrderChain
}

func (*fakeEngine) CheckOrderModel(domain.OrderProbe) (model.Order, error) {
	return model.Order{}, errUnexpectedOrderChain
}

func (*fakeEngine) RejectedOrder(domain.Order, []reject.Reject) engine.OrderResult {
	panic(errUnexpectedOrderChain)
}

func (*fakeEngine) ReservedOrder(
	domain.Order, asyncengine.OperationResult,
) (engine.OrderResult, error) {
	return engine.OrderResult{}, errUnexpectedOrderChain
}

func (*fakeEngine) AppliedDropCopyOrder(
	domain.Order, asyncengine.DropCopyResult,
) (engine.OrderResult, error) {
	return engine.OrderResult{}, errUnexpectedOrderChain
}

func (*fakeEngine) RejectedImmediate(
	domain.Order, []reject.Reject,
) engine.ImmediateResult {
	panic(errUnexpectedOrderChain)
}

func (*fakeEngine) PrepareImmediateReservation(
	domain.Order, asyncengine.OperationResult,
) (engine.ImmediatePreparation, error) {
	return engine.ImmediatePreparation{}, errUnexpectedOrderChain
}

func (*fakeEngine) PrepareImmediateDropCopy(
	domain.Order, asyncengine.DropCopyResult,
) (engine.ImmediatePreparation, error) {
	return engine.ImmediatePreparation{}, errUnexpectedOrderChain
}

func (*fakeEngine) SettleImmediate(
	domain.Order, engine.ImmediatePreparation, pretrade.PostTradeResult,
) (engine.ImmediateResult, error) {
	return engine.ImmediateResult{}, errUnexpectedOrderChain
}

func (*fakeEngine) CheckedOrder(
	domain.OrderProbe, asyncengine.OrderCheckResult,
) (domain.CheckResult, error) {
	return domain.CheckResult{}, errUnexpectedOrderChain
}

func (e *fakeEngine) Version() string      { return "fake" }
func (e *fakeEngine) BuildProfile() string { return "test" }
func (e *fakeEngine) Running() bool        { return e.running }

func (e *fakeEngine) ConfigurePolicy(
	context.Context,
	string,
	engine.LimitSet,
) (engine.PolicyConfigurationResult, error) {
	return engine.PolicyConfigurationResult{}, nil
}

func (*fakeEngine) AddAccountResolverEntry(domain.Account) error { return nil }
func (*fakeEngine) AddAssetResolverEntry(domain.Asset) error     { return nil }
func (*fakeEngine) AddGroupResolverEntry(domain.AccountGroup) error {
	return nil
}
func (*fakeEngine) RenameAccountResolverEntry(domain.AccountID, domain.Account) error {
	return nil
}
func (*fakeEngine) RenameAssetResolverEntry(string, domain.Asset) error { return nil }
func (*fakeEngine) RenameGroupResolverEntry(string, domain.AccountGroup) error {
	return nil
}
func (*fakeEngine) RemoveAssetResolverEntry(domain.Asset) error { return nil }
func (*fakeEngine) RemoveGroupResolverEntry(domain.AccountGroup) error {
	return nil
}
func (*fakeEngine) ResolveAsset(string) (param.Asset, error) {
	return param.Asset{}, errUnexpectedOrderChain
}
func (*fakeEngine) ResolveGroup(string) (param.AccountGroupID, error) {
	return param.AccountGroupID{}, errUnexpectedOrderChain
}

func (e *fakeEngine) MarketDataSink() marketdata.Sink { return e.sink }

func (e *fakeEngine) Stop() { e.running = false }
