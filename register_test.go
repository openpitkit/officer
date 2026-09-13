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

package officer

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/configure"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/reject"

	frameworkapp "go.openpit.dev/officer/framework/app"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/secret"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/store/sqlite"
	"go.openpit.dev/officer/mcptools"
)

func TestRegisterBuildUsesPopulatedMCPCatalog(t *testing.T) {
	ctx := context.Background()
	builder := frameworkapp.NewBuilder()
	if err := Register(
		builder, Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")},
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	builder.SetNodeBuilder(fakeEngineNodeBuilder)

	app, err := builder.Build(ctx, slog.Default(), failOnFatal(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	commands, err := app.Service().ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	expected := frameworkmcp.NewToolRegistry()
	mcptools.RegisterTools(expected, nil)
	if len(commands) != len(expected.Descriptors()) {
		t.Fatalf("ListMcpAccess returned %d commands, want %d", len(commands), len(expected.Descriptors()))
	}

	getAccount, ok := findCommand(commands, "get_account_state")
	if !ok {
		t.Fatalf("get_account_state missing from composed catalogue")
	}
	if !getAccount.Command.DefaultEnabled || !getAccount.Enabled {
		t.Fatalf("get_account_state default state = %+v", getAccount)
	}
	if err := app.Service().SetMcpAccess(ctx, "get_account_state", false); err != nil {
		t.Fatalf("SetMcpAccess(get_account_state): %v", err)
	}
}

func TestRegisterPassesMasterKeyToSQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	key, err := secret.ParseMasterKey("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}

	builder := frameworkapp.NewBuilder()
	if err := Register(builder, Config{SQLitePath: path, MasterKey: &key}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	builder.SetNodeBuilder(fakeEngineNodeBuilder)
	app, err := builder.Build(ctx, slog.Default(), failOnFatal(t))
	if err != nil {
		t.Fatalf("Build with master key: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	withoutKey := frameworkapp.NewBuilder()
	if err := Register(withoutKey, Config{SQLitePath: path}); err != nil {
		t.Fatalf("Register without key: %v", err)
	}
	withoutKey.SetNodeBuilder(fakeEngineNodeBuilder)
	_, err = withoutKey.Build(ctx, slog.Default(), failOnFatal(t))
	if err == nil {
		t.Fatal("Build without the master key returned nil error for a sealed database")
	}
	if !strings.Contains(err.Error(), "no master key was supplied") {
		t.Fatalf("Build without the master key returned the wrong error: %v", err)
	}
}

func TestBuildRejectsNodeBuilderWithoutEngine(t *testing.T) {
	ctx := context.Background()
	builder := frameworkapp.NewBuilder()
	if err := Register(
		builder, Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")},
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var opened store.Store
	builder.SetNodeBuilder(func(
		buildCtx context.Context, st store.Store, fatalHook frameworkapp.FatalShutdownHook,
	) (node.Node, engine.Engine, error) {
		opened = st
		built, _, err := fakeEngineNodeBuilder(buildCtx, st, fatalHook)
		return built, nil, err
	})

	app, err := builder.Build(ctx, slog.Default(), failOnFatal(t))
	if err == nil {
		_ = app.Close()
		t.Fatal("Build accepted a node hook that returned no engine")
	}
	if !strings.Contains(err.Error(), "no engine") {
		t.Fatalf("Build error = %v, want the missing engine named", err)
	}
	// Build closed the node, and with it the store the node owns.
	if _, err := opened.ForRealm(ctx, domain.DefaultRealm); err == nil ||
		!strings.Contains(err.Error(), "sqlite is closed") {
		t.Fatalf("ForRealm after the rejected Build = %v, want the store closed", err)
	}
}

func TestBuildRejectsMissingAuthorizer(t *testing.T) {
	builder := frameworkapp.NewBuilder()
	if err := Register(
		builder, Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")},
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	builder.SetNodeBuilder(fakeEngineNodeBuilder)
	builder.SetAuthorizer(nil)

	app, err := builder.Build(context.Background(), slog.Default(), failOnFatal(t))
	if err == nil {
		_ = app.Close()
		t.Fatal("Build accepted a nil authorizer")
	}
	if !strings.Contains(err.Error(), "nil authorizer") {
		t.Fatalf("Build error = %v, want the missing authorizer named", err)
	}
}

func TestBuildRejectsMissingCallerResolver(t *testing.T) {
	builder := frameworkapp.NewBuilder()
	if err := Register(
		builder, Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")},
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	builder.SetNodeBuilder(fakeEngineNodeBuilder)
	builder.SetCallerResolver(nil)

	app, err := builder.Build(context.Background(), slog.Default(), failOnFatal(t))
	if err == nil {
		_ = app.Close()
		t.Fatal("Build accepted a nil caller resolver")
	}
	if !strings.Contains(err.Error(), "nil caller resolver") {
		t.Fatalf("Build error = %v, want the missing caller resolver named", err)
	}
}

func TestBuildRejectsNilFatalHook(t *testing.T) {
	builder := frameworkapp.NewBuilder()
	if err := Register(
		builder, Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")},
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	builder.SetStoreFactory(func() (store.Store, error) {
		t.Error("Build opened the store despite a nil fatal shutdown hook")
		return nil, errors.New("store must not be opened")
	})
	builder.SetNodeBuilder(fakeEngineNodeBuilder)

	var nilHook frameworkapp.FatalShutdownHook
	app, err := builder.Build(context.Background(), slog.Default(), nilHook)
	if err == nil {
		_ = app.Close()
		t.Fatal("Build accepted a nil fatal shutdown hook")
	}
	if !strings.Contains(err.Error(), "nil fatal shutdown hook") {
		t.Fatalf("Build error = %v, want the missing fatal shutdown hook named", err)
	}
}

// failOnFatal returns the fatal-shutdown hook of a test that expects no
// fail-stop: any call fails the test.
func failOnFatal(t *testing.T) frameworkapp.FatalShutdownHook {
	t.Helper()
	return func(err error) {
		t.Errorf("unexpected fatal shutdown: %v", err)
	}
}

func TestRESTMutationAuditUsesResolvedPrincipal(t *testing.T) {
	const principal = "alice"
	app, realm := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: principal, Role: "admin"}, nil
	})
	if err := realm.CreatePrincipal(context.Background(), domain.Principal{Code: principal}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	handler, err := app.BuildServeHandler(nil, "/mcp")
	if err != nil {
		t.Fatalf("BuildServeHandler: %v", err)
	}
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/mcp-access/get_account_state",
		strings.NewReader(`{"enabled":false}`),
	)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mutation status = %d, body = %q", rec.Code, rec.Body.String())
	}
	rows, err := app.Service().ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	row := auditRowForAction(t, rows, domain.AuditActionSetMcpAccess)
	if row.Actor != principal || row.Source != domain.SourceAPI {
		t.Fatalf("audit caller = (%q, %q), want (%q, %q)",
			row.Actor, row.Source, principal, domain.SourceAPI)
	}
}

func TestRecordServiceLifecycleUsesContextCaller(t *testing.T) {
	const principal = "lifecycle-admin"
	app, realm := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: domain.PrincipalOperator}, nil
	})
	if err := realm.CreatePrincipal(context.Background(), domain.Principal{Code: principal}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	caller := domain.Caller{Source: domain.SourcePanel, Principal: principal, Role: "admin"}
	if err := app.RecordServiceLifecycle(
		auth.ContextWithCaller(context.Background(), caller),
		domain.AuditActionRestartService, "restart service requested",
	); err != nil {
		t.Fatalf("RecordServiceLifecycle: %v", err)
	}
	rows, err := app.Service().ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	row := auditRowForAction(t, rows, domain.AuditActionRestartService)
	if row.Actor != principal || row.Source != domain.SourcePanel {
		t.Fatalf("audit caller = (%q, %q), want (%q, %q)",
			row.Actor, row.Source, principal, domain.SourcePanel)
	}
}

func TestRecordServiceLifecycleRejectsUnresolvedCaller(t *testing.T) {
	ctx := context.Background()
	app, realm := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: domain.PrincipalOperator}, nil
	})
	before, err := realm.ListAudit(ctx, 1000)
	if err != nil {
		t.Fatalf("ListAudit before: %v", err)
	}
	err = app.RecordServiceLifecycle(
		ctx, domain.AuditActionRestartService, "restart service requested",
	)
	if err == nil || !strings.Contains(err.Error(), "no resolved caller") {
		t.Errorf("RecordServiceLifecycle without a caller = %v, want the missing caller named", err)
	}
	after, err := realm.ListAudit(ctx, 1000)
	if err != nil {
		t.Fatalf("ListAudit after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("audit rows = %d after the rejected request, want %d: %+v",
			len(after), len(before), after)
	}
}

func TestRecordServiceLifecycleRecordsExplicitSystemCaller(t *testing.T) {
	app, _ := buildTestApp(t, func(*http.Request) (domain.Caller, error) {
		return domain.Caller{Principal: domain.PrincipalOperator}, nil
	})
	if err := app.RecordServiceLifecycle(
		auth.ContextWithCaller(
			context.Background(), domain.Caller{Source: domain.SourceSystem},
		),
		domain.AuditActionRestartService, "restart service requested",
	); err != nil {
		t.Fatalf("RecordServiceLifecycle: %v", err)
	}
	rows, err := app.Service().ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	row := auditRowForAction(t, rows, domain.AuditActionRestartService)
	if row.Actor != "" || row.Source != domain.SourceSystem {
		t.Fatalf("audit caller = (%q, %q), want (%q, %q)",
			row.Actor, row.Source, "", domain.SourceSystem)
	}
}

func buildTestApp(
	t *testing.T,
	resolver func(*http.Request) (domain.Caller, error),
) (*frameworkapp.App, store.RealmStore) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.New(
		filepath.Join(t.TempDir(), "officer.db"), domain.DefaultRealm,
	)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	builder := frameworkapp.NewBuilder()
	if err := Register(builder, Config{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	builder.SetStoreFactory(func() (store.Store, error) { return st, nil })
	builder.SetNodeBuilder(fakeEngineNodeBuilder)
	builder.SetCallerResolver(resolver)
	builder.SetSPAFactory(func() (fs.FS, error) {
		return fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}, nil
	})
	app, err := builder.Build(ctx, slog.Default(), failOnFatal(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	return app, realm
}

func auditRowForAction(
	t *testing.T, rows []domain.AuditRow, action domain.AuditAction,
) domain.AuditRow {
	t.Helper()
	for _, row := range rows {
		if row.Action == action {
			return row
		}
	}
	t.Fatalf("audit action %q not found in %+v", action, rows)
	return domain.AuditRow{}
}

func TestBuildStopsEngineReturnedWithoutNode(t *testing.T) {
	builder := frameworkapp.NewBuilder()
	if err := Register(
		builder, Config{SQLitePath: filepath.Join(t.TempDir(), "officer.db")},
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	eng := &fakeEngine{running: true, sink: &fakeSink{}}
	builder.SetNodeBuilder(func(
		context.Context, store.Store, frameworkapp.FatalShutdownHook,
	) (node.Node, engine.Engine, error) {
		return nil, eng, nil
	})

	app, err := builder.Build(context.Background(), slog.Default(), failOnFatal(t))
	if err == nil {
		_ = app.Close()
		t.Fatal("Build accepted a node hook that returned no node")
	}
	if !strings.Contains(err.Error(), "no node") {
		t.Fatalf("Build error = %v, want the missing node named", err)
	}
	if eng.running {
		t.Fatal("engine returned with a nil node was not stopped")
	}
	if len(eng.lifecycle) != 2 ||
		eng.lifecycle[0] != "stop" || eng.lifecycle[1] != "close market data" {
		t.Fatalf("engine shutdown calls = %v, want stop then close market data", eng.lifecycle)
	}
}

// fakeEngineNodeBuilder replaces the composition's node hook with the same local
// node over a fake engine, so these tests need no native runtime.
func fakeEngineNodeBuilder(
	ctx context.Context, st store.Store, fatalHook frameworkapp.FatalShutdownHook,
) (node.Node, engine.Engine, error) {
	return node.NewLocalNode(
		ctx,
		domain.DefaultRealm,
		st,
		func(engine.Snapshot) (engine.Engine, error) {
			return &fakeEngine{running: true, sink: &fakeSink{}}, nil
		},
		fatalHook,
	)
}

func findCommand(commands []backend.McpCommand, name string) (backend.McpCommand, bool) {
	for _, cmd := range commands {
		if cmd.Command.Name == name {
			return cmd, true
		}
	}
	return backend.McpCommand{}, false
}

type fakeSink struct{}

func (s *fakeSink) Push(marketdata.QuoteUpdate) error { return nil }

type fakeEngine struct {
	sink      marketdata.Sink
	running   bool
	lifecycle []string
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
) ([]engine.AdjustmentResult, *domain.AdjustmentOutcomeRejected, error) {
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

func (e *fakeEngine) Stop() {
	e.running = false
	e.lifecycle = append(e.lifecycle, "stop")
}

func (e *fakeEngine) CloseMarketDataService() {
	e.lifecycle = append(e.lifecycle, "close market data")
}
