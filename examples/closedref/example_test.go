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
)

func TestReferenceCompositionAddReplaceHideRemove(t *testing.T) {
	ctx := context.Background()
	composition := newReferenceComposition()
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

	assertPrivateRouteAdded(t, router)
	assertPrivateToolCatalogued(t, service)
	assertPrivateProvider(t, ctx, service)
	assertRouteAndToolReplacement(t, ctx, router, service)
	assertRouteAndToolHidden(t, router, composition.Authorizer, service)
	assertRouteAndToolRemoved(t, router, service)
}

func assertPrivateRouteAdded(t *testing.T, router http.Handler) {
	t.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/api/v1"+privateRoutePattern, nil),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("private route status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode private route: %v", err)
	}
	if body["example"] != "ok" {
		t.Fatalf("private route body = %#v", body)
	}
}

func assertPrivateToolCatalogued(t *testing.T, service backend.ControlPlane) {
	t.Helper()

	cmd, ok := mcpCommand(t, service, privateToolID)
	if !ok {
		t.Fatalf("private tool missing from catalog")
	}
	if cmd.Command.DefaultEnabled {
		t.Fatalf("private tool should be disabled by default: %+v", cmd)
	}
}

func assertPrivateProvider(
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
		if provider.Type == privateProviderID {
			return
		}
	}
	t.Fatalf("private provider %q missing from %+v", privateProviderID, status.Providers)
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
	if !ok || cmd.Command.Title != "Private health replacement" {
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
		mcp.RegisterDeps{Source: &referenceSource{enabled: true}, Authorizer: authorizer},
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
		t.Fatalf("untouched open tool get_account_state is missing")
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

type referenceSource struct {
	enabled bool
}

func (s *referenceSource) Status(context.Context) (mcp.Status, error) {
	return mcp.Status{}, nil
}

func (s *referenceSource) GetAccountState(context.Context, domain.AccountID) (
	domain.Account,
	node.AccountLimits,
	error,
) {
	return domain.Account{}, node.AccountLimits{}, nil
}

func (s *referenceSource) ListLimits(context.Context, domain.AccountID) (
	node.AccountLimits,
	error,
) {
	return node.AccountLimits{}, nil
}

func (s *referenceSource) ListAudit(context.Context, int) ([]domain.AuditRow, error) {
	return nil, nil
}

func (s *referenceSource) ListAuditFiltered(
	context.Context,
	domain.AuditFilter,
	int,
) ([]domain.AuditRow, error) {
	return nil, nil
}

func (s *referenceSource) CheckOrder(
	context.Context,
	domain.OrderProbe,
) (domain.CheckResult, error) {
	return domain.CheckResult{}, nil
}

func (s *referenceSource) GetOrder(
	context.Context,
	string,
) (domain.OrderDetail, error) {
	return domain.OrderDetail{}, nil
}

func (s *referenceSource) SetMarketDataInstrumentEnabled(
	context.Context,
	string,
	string,
	bool,
) error {
	return nil
}

func (s *referenceSource) CommandEnabled(context.Context, string) (bool, error) {
	return s.enabled, nil
}

func (s *referenceSource) SubmitOrderToken(
	context.Context,
	domain.Order,
	string,
) (mcp.SubmitOrderTokenResult, error) {
	return mcp.SubmitOrderTokenResult{ExpiresAt: time.Now()}, nil
}

func (s *referenceSource) ConfirmExecution(
	context.Context,
	string,
	string,
	bool,
) (domain.Order, mcp.Attestation, error) {
	return domain.Order{}, mcp.Attestation{}, nil
}

func (s *referenceSource) CancelOrder(
	context.Context,
	string,
	string,
	string,
	bool,
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

func (e *fakeEngine) Version() string      { return "fake" }
func (e *fakeEngine) BuildProfile() string { return "test" }
func (e *fakeEngine) Running() bool        { return e.running }

func (e *fakeEngine) ConfigurePolicy(
	context.Context,
	string,
	engine.LimitSet,
) error {
	return nil
}

func (e *fakeEngine) BlockAccount(context.Context, domain.AccountID, string) error {
	return nil
}

func (e *fakeEngine) UnblockAccount(context.Context, domain.AccountID) error {
	return nil
}

func (e *fakeEngine) ApplyAccountAdjustmentBatch(
	context.Context,
	domain.AccountID,
	[]domain.AdjustmentRequest,
) ([]engine.AdjustmentResult, *engine.AdjustmentBatchReject, error) {
	return nil, nil, nil
}

func (e *fakeEngine) ApplyAccountAdjustment(
	context.Context,
	domain.AccountID,
	domain.AdjustmentRequest,
) (engine.AdjustmentResult, error) {
	return engine.AdjustmentResult{}, nil
}

func (e *fakeEngine) SubmitOrder(context.Context, domain.Order) (engine.OrderResult, error) {
	return engine.OrderResult{}, nil
}

func (e *fakeEngine) ReserveHold(context.Context, domain.Order) (engine.HoldResult, error) {
	return engine.HoldResult{}, nil
}

func (e *fakeEngine) CommitHeld(context.Context, string) error { return nil }

func (e *fakeEngine) RollbackHeld(context.Context, string) error { return nil }

func (e *fakeEngine) SubmitImmediate(
	context.Context,
	domain.Order,
) (engine.ImmediateResult, error) {
	return engine.ImmediateResult{}, nil
}

func (e *fakeEngine) SetReservationStore(engine.ReservationStore) {}

func (e *fakeEngine) ReconcileOrphans(context.Context) (int, error) { return 0, nil }

func (e *fakeEngine) RunAccountSynchronized(
	_ context.Context, _ domain.AccountID, fn func(engine.AccountLane) error,
) error {
	return fn(e)
}

func (e *fakeEngine) RunGroupSynchronized(
	_ context.Context, _ string, fn func(engine.GroupLane) error,
) error {
	return fn(e)
}

func (e *fakeEngine) ApplyExecutionReport(
	context.Context,
	domain.ExecutionReportInput,
) (engine.ExecutionReportResult, error) {
	return engine.ExecutionReportResult{}, nil
}

func (e *fakeEngine) RegisterGroup(context.Context, []domain.AccountID, string) error {
	return nil
}

func (e *fakeEngine) UnregisterGroup(context.Context, []domain.AccountID, string) error {
	return nil
}

func (e *fakeEngine) BlockGroup(context.Context, string, string) error { return nil }

func (e *fakeEngine) UnblockGroup(context.Context, string) error { return nil }

func (e *fakeEngine) CheckOrder(
	context.Context,
	domain.OrderProbe,
) (domain.CheckResult, error) {
	return domain.CheckResult{}, nil
}

func (e *fakeEngine) MarketDataSink() marketdata.Sink { return e.sink }

func (e *fakeEngine) Stop() { e.running = false }
