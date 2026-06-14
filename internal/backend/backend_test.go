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

package backend_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/auth"
	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/backup"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
	"go.openpit.dev/officer/internal/marketdata"
	"go.openpit.dev/officer/internal/node"
)

// fakeNode records the commands routed to it and returns canned data. It never
// touches an engine or a store, so the backend validation/routing tests run in
// isolation.
type fakeNode struct {
	version  string
	accounts []domain.Account
	limits   []domain.Limit
	audit    []domain.AuditRow

	putLimitCalls    []domain.Limit
	deleteLimitCalls []domain.LimitTarget
	createCalls      []node.Key
	blockCalls       []blockCall

	checkResult domain.CheckResult
	checkProbes []domain.OrderProbe

	mcpAccess        map[string]bool
	setMcpAccessCall []setMcpAccessCall
	mdInstances      []domain.MarketDataInstance
	mdInstruments    map[string][]domain.MarketDataInstrument
	mdQuotes         []domain.MarketDataQuote

	backupArchive  backup.Archive
	backupScope    backup.Scope
	backupCaller   domain.Caller
	backupErr      error
	restoreOpts    backup.RestoreOptions
	restoreSummary backup.RestoreSummary
	restoreSink    marketdata.Sink
	restoreErr     error
	resetCaller    domain.Caller
	resetSink      marketdata.Sink
	resetErr       error

	getAccountErr error
}

type setMcpAccessCall struct {
	command string
	enabled bool
}

type blockCall struct {
	key     node.Key
	blocked bool
	reason  string
}

func (n *fakeNode) Health(context.Context) (node.Health, error) {
	return node.Health{}, nil
}
func (n *fakeNode) EngineVersion() string { return n.version }
func (n *fakeNode) Owns(node.Key) bool    { return true }

func (n *fakeNode) ListAccounts(context.Context) ([]domain.Account, error) {
	return n.accounts, nil
}

func (n *fakeNode) ExportBackup(
	_ context.Context,
	scope backup.Scope,
	caller domain.Caller,
) (backup.Archive, error) {
	n.backupScope = scope
	n.backupCaller = caller
	return n.backupArchive, n.backupErr
}

func (n *fakeNode) RestoreBackup(
	_ context.Context,
	_ backup.Archive,
	opts backup.RestoreOptions,
	_ domain.Caller,
) (backup.RestoreSummary, marketdata.Sink, error) {
	n.restoreOpts = opts
	summary := n.restoreSummary
	if summary.Applied == nil {
		summary = backup.NewSummary()
	}
	return summary, n.restoreSink, n.restoreErr
}

func (n *fakeNode) ResetDatabase(
	_ context.Context,
	caller domain.Caller,
) (marketdata.Sink, error) {
	n.resetCaller = caller
	return n.resetSink, n.resetErr
}

func (n *fakeNode) CreateAccount(
	_ context.Context, key node.Key, _ domain.Caller,
) (domain.Account, error) {
	n.createCalls = append(n.createCalls, key)
	account := domain.Account{Tenant: key.Tenant, ID: key.Account}
	n.accounts = append(n.accounts, account)
	return account, nil
}

func (n *fakeNode) SetAccountBlocked(
	_ context.Context, key node.Key, blocked bool, reason string, _ domain.Caller,
) error {
	n.blockCalls = append(n.blockCalls, blockCall{key, blocked, reason})
	return nil
}

func (n *fakeNode) GetAccountState(
	_ context.Context, key node.Key,
) (domain.Account, []domain.Limit, error) {
	if n.getAccountErr != nil {
		return domain.Account{}, nil, n.getAccountErr
	}
	return domain.Account{Tenant: key.Tenant, ID: key.Account}, n.limits, nil
}

func (n *fakeNode) ListLimits(
	context.Context, domain.AccountID,
) ([]domain.Limit, error) {
	return n.limits, nil
}

func (n *fakeNode) PutLimit(_ context.Context, limit domain.Limit, _ domain.Caller) error {
	n.putLimitCalls = append(n.putLimitCalls, limit)
	return nil
}

func (n *fakeNode) DeleteLimit(
	_ context.Context, target domain.LimitTarget, _ domain.Caller,
) error {
	n.deleteLimitCalls = append(n.deleteLimitCalls, target)
	return nil
}

func (n *fakeNode) SetAccountGroup(
	context.Context, node.Key, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) SetAccountNotes(
	context.Context, node.Key, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) CreateGroup(context.Context, domain.AccountGroup, domain.Caller) error {
	return nil
}

func (n *fakeNode) ListGroups(
	context.Context, domain.TenantID,
) ([]domain.AccountGroup, error) {
	return nil, nil
}

func (n *fakeNode) GetGroup(
	context.Context, domain.TenantID, string,
) (domain.AccountGroup, []domain.Account, bool, error) {
	return domain.AccountGroup{}, nil, false, nil
}

func (n *fakeNode) SetGroupNotes(
	context.Context, domain.TenantID, string, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) SetGroupBlocked(
	context.Context, domain.TenantID, string, bool, string, domain.Caller,
) error {
	return nil
}

func (n *fakeNode) DeleteGroup(context.Context, domain.TenantID, string, domain.Caller) error {
	return nil
}

func (n *fakeNode) ApplyAdjustment(
	context.Context, node.Key, domain.AdjustmentRequest, domain.Caller,
) (domain.AccountAdjustmentRecord, error) {
	return domain.AccountAdjustmentRecord{}, nil
}

func (n *fakeNode) ListBalances(
	context.Context, domain.TenantID, domain.AccountID, string,
) ([]domain.Balance, error) {
	return nil, nil
}

func (n *fakeNode) GetBalance(
	context.Context, domain.TenantID, domain.AccountID, string,
) (domain.Balance, bool, error) {
	return domain.Balance{}, false, nil
}

func (n *fakeNode) ListAdjustments(
	context.Context, domain.TenantID, domain.AccountID, domain.Source, int,
) ([]domain.AccountAdjustmentRecord, error) {
	return nil, nil
}

func (n *fakeNode) SubmitOrder(
	context.Context, node.Key, domain.Order, domain.Caller,
) (domain.Order, error) {
	return domain.Order{}, nil
}

func (n *fakeNode) ApplyExecutionReport(
	context.Context, node.Key, domain.ExecutionReportInput, domain.Caller,
) (engine.ExecutionReportResult, error) {
	return engine.ExecutionReportResult{}, nil
}

func (n *fakeNode) GetOrder(
	context.Context, domain.TenantID, int64,
) (domain.OrderDetail, error) {
	return domain.OrderDetail{}, nil
}

func (n *fakeNode) ListOrders(
	context.Context, domain.TenantID, domain.AccountID, domain.Source, int,
) ([]domain.Order, error) {
	return nil, nil
}

func (n *fakeNode) CountOrders(context.Context, domain.TenantID) (int, error) {
	return 0, nil
}

func (n *fakeNode) CountOrdersSince(
	context.Context, domain.TenantID, time.Time,
) (int, error) {
	return 0, nil
}

func (n *fakeNode) ListOrderEvents(
	context.Context, domain.TenantID, int64,
) ([]domain.OrderEvent, error) {
	return nil, nil
}

func (n *fakeNode) ListTrades(
	context.Context, domain.TenantID, domain.AccountID, domain.Source, int,
) ([]domain.Trade, error) {
	return nil, nil
}

func (n *fakeNode) ListAudit(context.Context, int) ([]domain.AuditRow, error) {
	return n.audit, nil
}

func (n *fakeNode) CheckOrder(
	_ context.Context, _ node.Key, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	n.checkProbes = append(n.checkProbes, probe)
	return n.checkResult, nil
}

func (n *fakeNode) ListMcpAccess(context.Context) (map[string]bool, error) {
	return n.mcpAccess, nil
}

func (n *fakeNode) SetMcpAccess(
	_ context.Context, command string, enabled bool, _ domain.Caller,
) error {
	n.setMcpAccessCall = append(n.setMcpAccessCall,
		setMcpAccessCall{command: command, enabled: enabled})
	if n.mcpAccess == nil {
		n.mcpAccess = make(map[string]bool)
	}
	n.mcpAccess[command] = enabled
	return nil
}

func (n *fakeNode) ListMarketDataInstances(
	context.Context,
) ([]domain.MarketDataInstance, error) {
	return n.mdInstances, nil
}

func (n *fakeNode) GetMarketDataInstance(
	_ context.Context, id string,
) (domain.MarketDataInstance, bool, error) {
	for _, instance := range n.mdInstances {
		if instance.ID == id {
			return instance, true, nil
		}
	}
	return domain.MarketDataInstance{}, false, nil
}

func (n *fakeNode) CreateMarketDataInstance(
	_ context.Context, instance domain.MarketDataInstance, _ domain.Caller,
) error {
	n.mdInstances = append(n.mdInstances, instance)
	return nil
}

func (n *fakeNode) SetMarketDataInstanceEnabled(
	_ context.Context, id string, enabled bool, _ domain.Caller,
) error {
	for i := range n.mdInstances {
		if n.mdInstances[i].ID == id {
			n.mdInstances[i].Enabled = enabled
			return nil
		}
	}
	return domain.ErrNotFound
}

func (n *fakeNode) UpdateMarketDataInstanceSettings(
	_ context.Context, id, label, credentials string, _ domain.Caller,
) error {
	for i := range n.mdInstances {
		if n.mdInstances[i].ID == id {
			n.mdInstances[i].Label = label
			n.mdInstances[i].Credentials = credentials
			return nil
		}
	}
	return domain.ErrNotFound
}

func (n *fakeNode) DeleteMarketDataInstance(
	_ context.Context, id string, _ domain.Caller,
) error {
	for i := range n.mdInstances {
		if n.mdInstances[i].ID == id {
			n.mdInstances = append(n.mdInstances[:i], n.mdInstances[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (n *fakeNode) ListMarketDataInstruments(
	_ context.Context, instanceID string,
) ([]domain.MarketDataInstrument, error) {
	return n.mdInstruments[instanceID], nil
}

func (n *fakeNode) UpsertMarketDataInstrument(
	_ context.Context, instrument domain.MarketDataInstrument, _ domain.Caller,
) error {
	if n.mdInstruments == nil {
		n.mdInstruments = make(map[string][]domain.MarketDataInstrument)
	}
	n.mdInstruments[instrument.InstanceID] = append(n.mdInstruments[instrument.InstanceID], instrument)
	return nil
}

func (n *fakeNode) SetMarketDataInstrumentEnabled(
	_ context.Context, instanceID, externalSymbol string, enabled bool, _ domain.Caller,
) error {
	for i := range n.mdInstruments[instanceID] {
		if n.mdInstruments[instanceID][i].ExternalSymbol == externalSymbol {
			n.mdInstruments[instanceID][i].Enabled = enabled
			return nil
		}
	}
	return domain.ErrNotFound
}

func (n *fakeNode) DeleteMarketDataInstrument(
	_ context.Context, instanceID, externalSymbol string, _ domain.Caller,
) error {
	instruments := n.mdInstruments[instanceID]
	for i := range instruments {
		if instruments[i].ExternalSymbol == externalSymbol {
			n.mdInstruments[instanceID] = append(instruments[:i], instruments[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (n *fakeNode) ListMarketDataQuotes(
	_ context.Context, instanceID string,
) ([]domain.MarketDataQuote, error) {
	if instanceID == "" {
		return n.mdQuotes, nil
	}
	out := make([]domain.MarketDataQuote, 0)
	for _, quote := range n.mdQuotes {
		if quote.InstanceID == instanceID {
			out = append(out, quote)
		}
	}
	return out, nil
}

func (n *fakeNode) Close() error { return nil }

// fakeRouter routes every key to the single fake node.
type fakeRouter struct {
	node     *fakeNode
	routeErr error
}

func (r *fakeRouter) Route(node.Key) (node.Node, error) {
	return r.node, r.routeErr
}
func (r *fakeRouter) All() []node.Node { return []node.Node{r.node} }

type fakeMarketDataRuntime struct {
	statuses   map[string]marketdata.InstanceRuntimeStatus
	applied    map[string]marketdata.AppliedInstanceConfig
	intervals  map[string]time.Duration
	pushed     []domain.MarketDataInstrument
	restarts   int
	stops      int
	sink       marketdata.Sink
	restartErr error
	useSinkErr error
}

type backendTestSink struct{}

func (*backendTestSink) Push(marketdata.QuoteUpdate) error { return nil }

func (r *fakeMarketDataRuntime) InstanceStatuses() map[string]marketdata.InstanceRuntimeStatus {
	return r.statuses
}

func (r *fakeMarketDataRuntime) AppliedConfig() map[string]marketdata.AppliedInstanceConfig {
	return r.applied
}

func (r *fakeMarketDataRuntime) QuoteUpdateInterval(
	instanceID, external string,
) (time.Duration, bool) {
	d, ok := r.intervals[instanceID+"\x00"+external]
	return d, ok
}

func (r *fakeMarketDataRuntime) Restart() error {
	r.restarts++
	return r.restartErr
}

func (r *fakeMarketDataRuntime) Stop() {
	r.stops++
}

func (r *fakeMarketDataRuntime) UseSink(sink marketdata.Sink) error {
	if r.useSinkErr != nil {
		return r.useSinkErr
	}
	r.sink = sink
	return nil
}

func (r *fakeMarketDataRuntime) PushManual(
	_ string, instrument domain.MarketDataInstrument,
) {
	r.pushed = append(r.pushed, instrument)
}

func newTestService() (*backend.Service, *fakeNode) {
	fn := &fakeNode{}
	return backend.New(&fakeRouter{node: fn}, nil), fn
}

func newTestServiceWithMarketDataRuntime(
	md backend.MarketDataRuntime,
) (*backend.Service, *fakeNode) {
	fn := &fakeNode{}
	return backend.New(&fakeRouter{node: fn}, md), fn
}

func TestService_ExportBackupRoutesScopeCallerAndFilename(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	createdAt := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	fn.backupArchive = backup.NewArchive(
		createdAt,
		"test",
		2,
		backup.Scope{All: true},
		backup.Data{},
	)
	scope := backup.Scope{
		Sections: []backup.Section{backup.SectionAccountsGroups},
	}
	caller := domain.Caller{
		Principal: "operator",
		Source:    domain.SourceAPI,
	}

	archive, filename, err := svc.ExportBackup(
		auth.ContextWithCaller(context.Background(), caller),
		scope,
	)
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if archive.Manifest.Source != "test" {
		t.Fatalf("archive source = %q, want test", archive.Manifest.Source)
	}
	if filename != backup.Filename(createdAt) {
		t.Fatalf("filename = %q, want %q", filename, backup.Filename(createdAt))
	}
	if !reflect.DeepEqual(fn.backupScope, scope) {
		t.Fatalf("backup scope = %+v, want %+v", fn.backupScope, scope)
	}
	if fn.backupCaller != caller {
		t.Fatalf("backup caller = %+v, want %+v", fn.backupCaller, caller)
	}
}

func TestService_ExportBackupRouteError(t *testing.T) {
	t.Parallel()
	routeErr := errors.New("route failed")
	svc := backend.New(&fakeRouter{routeErr: routeErr}, nil)

	if _, _, err := svc.ExportBackup(
		context.Background(),
		backup.Scope{All: true},
	); !errors.Is(err, routeErr) {
		t.Fatalf("ExportBackup error = %v, want route error", err)
	}
}

func TestService_ExportBackupNodeError(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	exportErr := errors.New("export failed")
	fn.backupErr = exportErr

	if _, _, err := svc.ExportBackup(
		context.Background(),
		backup.Scope{All: true},
	); !errors.Is(err, exportErr) {
		t.Fatalf("ExportBackup error = %v, want export error", err)
	}
}

func TestService_ResetDatabaseRoutesCallerAndRestartsMarketData(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.resetSink = sink
	caller := domain.Caller{
		Principal: "operator",
		Source:    domain.SourcePanel,
	}

	if err := svc.ResetDatabase(
		auth.ContextWithCaller(context.Background(), caller),
	); err != nil {
		t.Fatalf("ResetDatabase: %v", err)
	}
	if fn.resetCaller != caller {
		t.Fatalf("reset caller = %+v, want %+v", fn.resetCaller, caller)
	}
	if md.stops != 1 || md.restarts != 1 {
		t.Fatalf("market-data stops/restarts = %d/%d, want 1/1",
			md.stops, md.restarts)
	}
	if md.sink != sink {
		t.Fatalf("market-data sink = %T, want reset sink", md.sink)
	}
}

func TestService_ResetDatabaseRouteError(t *testing.T) {
	t.Parallel()
	routeErr := errors.New("route failed")
	svc := backend.New(&fakeRouter{routeErr: routeErr}, nil)

	if err := svc.ResetDatabase(context.Background()); !errors.Is(err, routeErr) {
		t.Fatalf("ResetDatabase error = %v, want route error", err)
	}
}

func TestService_ResetDatabaseNodeError(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	resetErr := errors.New("reset failed")
	fn.resetErr = resetErr

	if err := svc.ResetDatabase(context.Background()); !errors.Is(err, resetErr) {
		t.Fatalf("ResetDatabase error = %v, want reset error", err)
	}
}

func TestService_RestoreBackupGeneralSettingsDoesNotStopMarketData(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, _ := newTestServiceWithMarketDataRuntime(md)
	_, err := svc.RestoreBackup(context.Background(), backup.Archive{}, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if md.stops != 0 || md.restarts != 0 || md.sink != nil {
		t.Fatalf("market-data touched for non-runtime restore: %+v", md)
	}
}

func TestService_RestoreBackupRuntimeReconnectsMarketData(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	fn.restoreSummary = backup.RestoreSummary{
		Applied:         map[backup.Section]int{},
		Skipped:         map[backup.Section]int{},
		RestartRequired: true,
	}
	_, err := svc.RestoreBackup(context.Background(), backup.Archive{}, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionPositions}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if md.stops != 1 || md.restarts != 1 || md.sink != sink {
		t.Fatalf("market-data lifecycle stops=%d restarts=%d sink=%#v",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_RestoreBackupRuntimeErrorRestartsReturnedSink(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	fn.restoreErr = errors.New("restore failed")
	_, err := svc.RestoreBackup(context.Background(), backup.Archive{}, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionPositions}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want error")
	}
	if md.stops != 1 || md.restarts != 1 || md.sink != sink {
		t.Fatalf("market-data recovery stops=%d restarts=%d sink=%#v",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_RestoreBackupRuntimeUseSinkErrorStillRestarts(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{useSinkErr: errors.New("use sink failed")}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.restoreSink = &backendTestSink{}
	fn.restoreSummary = backup.RestoreSummary{
		Applied:         map[backup.Section]int{},
		Skipped:         map[backup.Section]int{},
		RestartRequired: true,
	}
	_, err := svc.RestoreBackup(context.Background(), backup.Archive{}, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionPositions}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want UseSink error")
	}
	if md.stops != 1 || md.restarts != 1 {
		t.Fatalf("market-data recovery stops=%d restarts=%d",
			md.stops, md.restarts)
	}
}

func TestService_RestoreBackupRuntimeRestartErrorIsReturned(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{restartErr: errors.New("restart failed")}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	fn.restoreSummary = backup.RestoreSummary{
		Applied:         map[backup.Section]int{},
		Skipped:         map[backup.Section]int{},
		RestartRequired: true,
	}
	_, err := svc.RestoreBackup(context.Background(), backup.Archive{}, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionPositions}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want Restart error")
	}
	if md.stops != 1 || md.restarts != 1 || md.sink != sink {
		t.Fatalf("market-data recovery stops=%d restarts=%d sink=%#v",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_CreateAccountValidates(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	if _, err := svc.CreateAccount(ctx, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty id, got %v", err)
	}
	if len(fn.createCalls) != 0 {
		t.Fatalf("invalid input must not reach the node")
	}

	if _, err := svc.CreateAccount(ctx, "acc-1"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if len(fn.createCalls) != 1 {
		t.Fatalf("valid create must route to node")
	}
}

func TestService_CheckOrderValidatesBeforeRouting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	bad := []domain.OrderProbe{
		{Account: "", BaseAsset: "AAPL", QuoteAsset: "USD"},
		{Account: "acc-1", BaseAsset: "", QuoteAsset: "USD"},
		{Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "bad asset"},
	}
	for _, probe := range bad {
		svc, fn := newTestService()
		if _, err := svc.CheckOrder(ctx, probe); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("want ErrInvalid for %+v, got %v", probe, err)
		}
		if len(fn.checkProbes) != 0 {
			t.Fatalf("invalid probe must not reach the node")
		}
	}
}

func TestService_CheckOrderRoutesAndReturnsPass(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.checkResult = domain.CheckResult{Passed: true, WouldLockPrices: []string{"100"}}
	ctx := context.Background()

	probe := domain.OrderProbe{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       "100",
	}
	out, err := svc.CheckOrder(ctx, probe)
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if !out.Passed || len(out.WouldLockPrices) != 1 || out.WouldLockPrices[0] != "100" {
		t.Fatalf("pass result not propagated: %+v", out)
	}
	if len(fn.checkProbes) != 1 || fn.checkProbes[0].Account != "acc-1" {
		t.Fatalf("valid probe must route to node once with the account preserved")
	}
}

func TestService_CheckOrderReturnsRejectAndBlock(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.checkResult = domain.CheckResult{
		Passed: false,
		Rejects: []domain.OrderReject{
			{Code: "rate_limit_exceeded", Scope: "account", Policy: "rate_limit"},
		},
		WouldBlock: &domain.ExecutionAccountBlock{
			Account: "acc-1", Code: "account_blocked", Reason: "kill switch",
		},
	}
	ctx := context.Background()

	out, err := svc.CheckOrder(ctx, domain.OrderProbe{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
	})
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if out.Passed {
		t.Fatalf("want passed=false")
	}
	if len(out.Rejects) != 1 || out.Rejects[0].Code != "rate_limit_exceeded" {
		t.Fatalf("reject not propagated: %+v", out.Rejects)
	}
	if out.WouldBlock == nil || out.WouldBlock.Account != "acc-1" {
		t.Fatalf("would-block not propagated: %+v", out.WouldBlock)
	}
}

func TestService_PutLimitValidatesBeforeRouting(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	// account scope on order_size_limit is not allowed - validation must reject
	// before the node is touched.
	bad := domain.Limit{
		Target: domain.LimitTarget{
			Policy:  domain.PolicyOrderSizeLimit,
			Scope:   domain.ScopeAccount,
			Account: "acc-1",
		},
		Values: []domain.LimitValue{{Kind: domain.KindMaxQuantity, Value: "1"}},
	}
	if err := svc.PutLimit(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if len(fn.putLimitCalls) != 0 {
		t.Fatalf("invalid limit must not reach the node")
	}
}

func TestService_PutLimitAcceptsNonExistentAccount(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	// Account "acc-new" does not exist in the fake node (getAccountErr is not set,
	// but no account record exists either). PutLimit must succeed regardless: a
	// policy rule may be created before the account is ever registered.
	limit := domain.Limit{
		Target: domain.LimitTarget{
			Policy:  domain.PolicyRateLimit,
			Scope:   domain.ScopeAccountAsset,
			Account: "acc-new",
			Asset:   "AAPL",
		},
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}
	if err := svc.PutLimit(ctx, limit); err != nil {
		t.Fatalf("PutLimit for non-existent account: %v", err)
	}
	if len(fn.putLimitCalls) != 1 {
		t.Fatalf("want 1 PutLimit call, got %d", len(fn.putLimitCalls))
	}
}

func TestService_PutLimitSetsDefaultTenant(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	limit := domain.Limit{
		Target: domain.LimitTarget{
			Policy: domain.PolicyRateLimit,
			Scope:  domain.ScopeBroker,
		},
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}
	if err := svc.PutLimit(ctx, limit); err != nil {
		t.Fatalf("PutLimit: %v", err)
	}
	if len(fn.putLimitCalls) != 1 {
		t.Fatalf("want one PutLimit call")
	}
	if fn.putLimitCalls[0].Target.Tenant != domain.DefaultTenant {
		t.Fatalf("tenant not defaulted: %q", fn.putLimitCalls[0].Target.Tenant)
	}
}

func TestService_DeleteLimitValidatesTarget(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	// broker scope is not allowed for pnl_bounds; target validation must reject.
	bad := domain.LimitTarget{
		Policy: domain.PolicyPnlBoundsKillSwitch,
		Scope:  domain.ScopeBroker,
	}
	if err := svc.DeleteLimit(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if len(fn.deleteLimitCalls) != 0 {
		t.Fatalf("invalid target must not reach the node")
	}

	good := domain.LimitTarget{
		Policy: domain.PolicyRateLimit,
		Scope:  domain.ScopeBroker,
	}
	if err := svc.DeleteLimit(ctx, good); err != nil {
		t.Fatalf("DeleteLimit: %v", err)
	}
	if len(fn.deleteLimitCalls) != 1 {
		t.Fatalf("valid delete must route to node")
	}
	if fn.deleteLimitCalls[0].Tenant != domain.DefaultTenant {
		t.Fatalf("tenant not defaulted on delete")
	}
}

func TestService_BlockAccountValidates(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	if err := svc.BlockAccount(ctx, "", "risk"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty id, got %v", err)
	}
	if len(fn.blockCalls) != 0 {
		t.Fatalf("invalid id must not reach the node")
	}

	if err := svc.BlockAccount(ctx, "acc-1", "risk"); err != nil {
		t.Fatalf("BlockAccount: %v", err)
	}
	if len(fn.blockCalls) != 1 || !fn.blockCalls[0].blocked ||
		fn.blockCalls[0].reason != "risk" {
		t.Fatalf("block not routed correctly: %+v", fn.blockCalls)
	}
}

func TestService_AggregatesReads(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.accounts = []domain.Account{{ID: "a"}, {ID: "b"}}
	fn.limits = []domain.Limit{{Target: domain.LimitTarget{Policy: domain.PolicyRateLimit}}}
	fn.audit = []domain.AuditRow{{ID: 1}}
	ctx := context.Background()

	accounts, err := svc.ListAccounts(ctx)
	if err != nil || len(accounts) != 2 {
		t.Fatalf("ListAccounts: %v len=%d", err, len(accounts))
	}
	limits, err := svc.ListLimits(ctx, "")
	if err != nil || len(limits) != 1 {
		t.Fatalf("ListLimits: %v len=%d", err, len(limits))
	}
	rows, err := svc.ListAudit(ctx, 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListAudit: %v len=%d", err, len(rows))
	}
}

func TestService_ListMcpAccessMergesDefaults(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	// Override one default-on command off and one default-off command on.
	fn.mcpAccess = map[string]bool{"health": false, "set_limit": true}
	ctx := context.Background()

	commands, err := svc.ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	got := make(map[string]bool, len(commands))
	for _, c := range commands {
		got[c.Command.Name] = c.Enabled
	}
	if got["health"] != false {
		t.Errorf("health override not applied: %v", got["health"])
	}
	if got["set_limit"] != true {
		t.Errorf("set_limit override not applied: %v", got["set_limit"])
	}
	if got["get_limits"] != true {
		t.Errorf("get_limits should default on, got %v", got["get_limits"])
	}
	if got["arm_killswitch"] != false {
		t.Errorf("arm_killswitch should default off, got %v", got["arm_killswitch"])
	}
}

func TestService_SetMcpAccessValidatesCommand(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	if err := svc.SetMcpAccess(ctx, "not_a_command", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound for unknown command, got %v", err)
	}
	if len(fn.setMcpAccessCall) != 0 {
		t.Fatalf("unknown command must not reach the node")
	}

	if err := svc.SetMcpAccess(ctx, "health", false); err != nil {
		t.Fatalf("SetMcpAccess: %v", err)
	}
	if len(fn.setMcpAccessCall) != 1 || fn.setMcpAccessCall[0].command != "health" ||
		fn.setMcpAccessCall[0].enabled != false {
		t.Fatalf("valid set must route to node: %+v", fn.setMcpAccessCall)
	}
}

func TestService_ListMarketDataBuildsStatus(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	now := time.Now().UTC()
	fn.mdInstances = []domain.MarketDataInstance{
		{ID: "mock-1", Type: domain.MarketDataProviderMock, Enabled: true},
		{ID: "mock-2", Type: domain.MarketDataProviderMock, Enabled: false},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		"mock-1": {
			{
				InstanceID:     "mock-1",
				ExternalSymbol: "AAPL",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
			{
				InstanceID:     "mock-1",
				ExternalSymbol: "MSFT",
				BaseAsset:      "MSFT",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
		"mock-2": {
			{
				InstanceID:     "mock-2",
				ExternalSymbol: "TSLA",
				BaseAsset:      "TSLA",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
	}
	fn.mdQuotes = []domain.MarketDataQuote{
		{
			InstanceID:     "mock-1",
			ExternalSymbol: "AAPL",
			BaseAsset:      "AAPL",
			QuoteAsset:     "USD",
			Mark:           "100",
			AsOf:           now,
			ReceivedAt:     now,
		},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	if status.FreshnessSeconds != int(backend.MarketDataFreshnessTTL.Seconds()) {
		t.Fatalf("freshness seconds mismatch: %+v", status)
	}
	for _, want := range []string{
		domain.MarketDataProviderIB,
		domain.MarketDataProviderBinance,
		domain.MarketDataProviderKraken,
		domain.MarketDataProviderCoinbase,
		domain.MarketDataProviderAlpaca,
		domain.MarketDataProviderOKX,
		domain.MarketDataProviderBybit,
		domain.MarketDataProviderOANDA,
		domain.MarketDataProviderFinnhub,
		domain.MarketDataProviderBYO,
		domain.MarketDataProviderMock,
	} {
		if !containsProviderType(status.Providers, want) {
			t.Fatalf("providers = %+v, want %s", status.Providers, want)
		}
	}
	if len(status.Instances) != 2 || len(status.Instances[0].Instruments) != 2 ||
		len(status.Instances[1].Instruments) != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}
	first := status.Instances[0].Instruments[0]
	second := status.Instances[0].Instruments[1]
	disabledSource := status.Instances[1].Instruments[0]
	if first.Quote == nil || first.Stale {
		t.Fatalf("fresh quoted instrument should not be stale: %+v", first)
	}
	if !second.Stale {
		t.Fatalf("enabled instrument without quote should be stale: %+v", second)
	}
	if disabledSource.Stale {
		t.Fatalf("disabled source instrument should not be stale: %+v", disabledSource)
	}
}

func TestMarketDataFreshnessTTLContract(t *testing.T) {
	t.Parallel()

	if marketdata.FreshnessTTL != 70*time.Second {
		t.Fatalf("marketdata.FreshnessTTL = %s, want 70s", marketdata.FreshnessTTL)
	}
	if engine.MarketDataFreshnessTTL != marketdata.FreshnessTTL {
		t.Fatalf(
			"engine.MarketDataFreshnessTTL = %s, want %s",
			engine.MarketDataFreshnessTTL,
			marketdata.FreshnessTTL,
		)
	}
	if backend.MarketDataFreshnessTTL != marketdata.FreshnessTTL {
		t.Fatalf(
			"backend.MarketDataFreshnessTTL = %s, want %s",
			backend.MarketDataFreshnessTTL,
			marketdata.FreshnessTTL,
		)
	}
}

func TestService_ListMarketDataFlagsStaleQuote(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	now := time.Now().UTC()
	fn.mdInstances = []domain.MarketDataInstance{
		{ID: "mock-1", Type: domain.MarketDataProviderMock, Enabled: true},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		"mock-1": {
			{
				InstanceID:     "mock-1",
				ExternalSymbol: "AAPL",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
	}
	fn.mdQuotes = []domain.MarketDataQuote{
		{
			InstanceID:     "mock-1",
			ExternalSymbol: "AAPL",
			BaseAsset:      "AAPL",
			QuoteAsset:     "USD",
			Mark:           "298.01",
			AsOf:           now.Add(-backend.MarketDataFreshnessTTL - time.Second),
			ReceivedAt:     now,
		},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	got := status.Instances[0].Instruments[0]
	if !got.Stale {
		t.Fatalf("instrument should be stale: %+v", got)
	}
	// Quote is kept even when stale so the last known price remains visible.
	if got.Quote == nil {
		t.Fatalf("stale quote must still be present: %+v", got)
	}
	if got.Quote.Mark != "298.01" {
		t.Fatalf("stale quote has unexpected mark: %+v", got.Quote)
	}
}

func TestService_ListMarketDataDetectsRestartRequired(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{
		statuses: map[string]marketdata.InstanceRuntimeStatus{
			"mock-1": {State: marketdata.StateOK},
		},
		applied: map[string]marketdata.AppliedInstanceConfig{
			"mock-1": {
				Type: domain.MarketDataProviderMock,
				Subscriptions: []marketdata.Subscription{
					{External: "AAPL", Base: "AAPL", Quote: "USD"},
				},
			},
		},
	}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{
		{ID: "mock-1", Type: domain.MarketDataProviderMock, Enabled: true},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		"mock-1": {
			{
				InstanceID:     "mock-1",
				ExternalSymbol: "AAPL",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
			{
				InstanceID:     "mock-1",
				ExternalSymbol: "MSFT",
				BaseAsset:      "MSFT",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	if !status.RestartRequired {
		t.Fatal("RestartRequired = false, want true for unapplied instrument")
	}
}

func TestService_ListMarketDataSurfacesUpdateInterval(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{
		statuses: map[string]marketdata.InstanceRuntimeStatus{
			"mock-1": {State: marketdata.StateOK},
		},
		intervals: map[string]time.Duration{
			"mock-1\x00AAPL": 12 * time.Second,
		},
	}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{
		{ID: "mock-1", Type: domain.MarketDataProviderMock, Enabled: true},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		"mock-1": {
			{
				InstanceID:     "mock-1",
				ExternalSymbol: "AAPL",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
			{
				InstanceID:     "mock-1",
				ExternalSymbol: "MSFT",
				BaseAsset:      "MSFT",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	instruments := status.Instances[0].Instruments
	withInterval := instruments[0]
	if withInterval.UpdateInterval == nil {
		t.Fatalf("AAPL interval should be known: %+v", withInterval)
	}
	if *withInterval.UpdateInterval != 12*time.Second {
		t.Fatalf("AAPL interval = %v, want 12s", *withInterval.UpdateInterval)
	}
	withoutInterval := instruments[1]
	if withoutInterval.UpdateInterval != nil {
		t.Fatalf("MSFT interval should be unknown: %+v", withoutInterval)
	}
}

func TestService_CreateMarketDataInstanceGeneratesIDAndDefaultLabel(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	err := svc.CreateMarketDataInstance(context.Background(), domain.MarketDataInstance{
		ID:      "operator-supplied",
		Type:    domain.MarketDataProviderBinance,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if len(fn.mdInstances) != 1 {
		t.Fatalf("created instances = %d, want 1", len(fn.mdInstances))
	}
	got := fn.mdInstances[0]
	if got.ID == "" || got.ID == "operator-supplied" {
		t.Fatalf("instance ID = %q, want generated internal id", got.ID)
	}
	if got.Type != domain.MarketDataProviderBinance || got.Label != "Binance" || !got.Enabled {
		t.Fatalf("created instance = %+v, want Binance default label and enabled", got)
	}
}

func TestService_CreateMarketDataInstancePassesCredentials(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	err := svc.CreateMarketDataInstance(context.Background(), domain.MarketDataInstance{
		Type: domain.MarketDataProviderAlpaca,
		Credentials: `{
			"apiKey": "key",
			"apiSecret": "secret"
		}`,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if len(fn.mdInstances) != 1 {
		t.Fatalf("created instances = %d, want 1", len(fn.mdInstances))
	}
	if fn.mdInstances[0].Credentials == "" {
		t.Fatal("Credentials not persisted")
	}
}

func TestService_CreateMarketDataInstanceRejectsInvalidCredentials(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	err := svc.CreateMarketDataInstance(context.Background(), domain.MarketDataInstance{
		Type: domain.MarketDataProviderAlpaca,
		Credentials: `{
			"apiKey": "key"
		}`,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateMarketDataInstance error = %v, want ErrInvalid", err)
	}
	if len(fn.mdInstances) != 0 {
		t.Fatalf("invalid create changed instances: %+v", fn.mdInstances)
	}
}

func TestService_CreateMarketDataInstanceRejectsDuplicateLabel(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ID: "bn-1", Type: domain.MarketDataProviderBinance, Label: "Binance"},
	}

	err := svc.CreateMarketDataInstance(context.Background(), domain.MarketDataInstance{
		Type:  domain.MarketDataProviderMock,
		Label: " binance ",
	})
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateMarketDataInstance error = %v, want ErrAlreadyExists", err)
	}
	if len(fn.mdInstances) != 1 {
		t.Fatalf("duplicate create changed instances: %+v", fn.mdInstances)
	}
}

func TestService_UpdateMarketDataInstanceSettingsMergesBlankSecret(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{
			ID:          "alpaca-1",
			Type:        domain.MarketDataProviderAlpaca,
			Label:       "Alpaca",
			Credentials: `{"apiKey":"old-key","apiSecret":"old-secret"}`,
		},
	}

	err := svc.UpdateMarketDataInstanceSettings(
		context.Background(),
		"alpaca-1",
		"Alpaca live",
		`{"apiKey":"new-key","apiSecret":""}`,
	)
	if err != nil {
		t.Fatalf("UpdateMarketDataInstanceSettings: %v", err)
	}
	got := fn.mdInstances[0]
	if got.Label != "Alpaca live" {
		t.Fatalf("Label = %q, want updated label", got.Label)
	}
	var credentials map[string]string
	if err := json.Unmarshal([]byte(got.Credentials), &credentials); err != nil {
		t.Fatalf("credentials JSON: %v", err)
	}
	if credentials["apiKey"] != "new-key" || credentials["apiSecret"] != "old-secret" {
		t.Fatalf("credentials = %+v, want new key and preserved secret", credentials)
	}
}

func TestService_UpdateMarketDataInstanceSettingsRejectsDuplicateLabel(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ID: "a", Type: domain.MarketDataProviderBinance, Label: "Primary"},
		{ID: "b", Type: domain.MarketDataProviderBinance, Label: "Backup"},
	}

	err := svc.UpdateMarketDataInstanceSettings(
		context.Background(), "b", "primary", "",
	)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("UpdateMarketDataInstanceSettings error = %v, want ErrAlreadyExists", err)
	}
	if fn.mdInstances[1].Label != "Backup" {
		t.Fatalf("duplicate update changed instance: %+v", fn.mdInstances[1])
	}
}

func TestService_ListMarketDataManualPriceDoesNotRequireRestart(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{
		statuses: map[string]marketdata.InstanceRuntimeStatus{
			"byo-1": {State: marketdata.StateOK},
		},
		applied: map[string]marketdata.AppliedInstanceConfig{
			"byo-1": {
				Type: domain.MarketDataProviderBYO,
				Subscriptions: []marketdata.Subscription{
					{External: "USDT/USD", Base: "USDT", Quote: "USD"},
				},
			},
		},
	}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{
		{ID: "byo-1", Type: domain.MarketDataProviderBYO, Enabled: true},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		"byo-1": {
			{
				InstanceID:     "byo-1",
				ExternalSymbol: "USDT/USD",
				BaseAsset:      "USDT",
				QuoteAsset:     "USD",
				ManualPrice:    "0.9998",
				Enabled:        true,
			},
		},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	if status.RestartRequired {
		t.Fatal("RestartRequired = true, want false for manual price change")
	}
}

func TestService_ListMarketDataSurfacesVerifyCapability(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ID: "mock-1", Type: domain.MarketDataProviderMock, Enabled: true},
		{ID: "bn-1", Type: domain.MarketDataProviderBinance, Enabled: false},
		{ID: "ib-1", Type: domain.MarketDataProviderIB, Enabled: false},
		{ID: "kraken-1", Type: domain.MarketDataProviderKraken, Enabled: false},
		{ID: "coinbase-1", Type: domain.MarketDataProviderCoinbase, Enabled: false},
		{ID: "okx-1", Type: domain.MarketDataProviderOKX, Enabled: false},
		{ID: "bybit-1", Type: domain.MarketDataProviderBybit, Enabled: false},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	byID := make(map[string]backend.MarketDataInstanceStatus, len(status.Instances))
	for _, instance := range status.Instances {
		byID[instance.Instance.ID] = instance
	}
	if byID["mock-1"].VerifiesSymbols {
		t.Fatalf("mock instance VerifiesSymbols = true, want false")
	}
	if byID["ib-1"].VerifiesSymbols {
		t.Fatalf("ib instance VerifiesSymbols = true, want false")
	}
	// Binance is verify-capable even though the instance is disabled: the flag is
	// provider-derived, not runtime-derived.
	if !byID["bn-1"].VerifiesSymbols {
		t.Fatalf("binance instance VerifiesSymbols = false, want true")
	}
	for _, id := range []string{"kraken-1", "coinbase-1", "okx-1", "bybit-1"} {
		if !byID[id].VerifiesSymbols {
			t.Fatalf("%s VerifiesSymbols = false, want true", id)
		}
	}
}

func TestService_VerifyMarketDataSymbolUnsupported(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ID: "mock-1", Type: domain.MarketDataProviderMock, Enabled: true},
	}

	got, err := svc.VerifyMarketDataSymbol(context.Background(), "mock-1", "AAPL")
	if err != nil {
		t.Fatalf("VerifyMarketDataSymbol: %v", err)
	}
	if got.Supported || got.Exists || got.Suggestion != "" {
		t.Fatalf("verification = %+v, want unsupported zero result", got)
	}
}

func TestService_VerifyMarketDataSymbolUnknownInstance(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService()

	_, err := svc.VerifyMarketDataSymbol(context.Background(), "missing", "AAPL")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("VerifyMarketDataSymbol(missing) err = %v, want ErrNotFound", err)
	}
}

func TestService_SearchMarketDataSymbolsUnsupported(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ID: "mock-1", Type: domain.MarketDataProviderMock, Enabled: true},
	}

	got, err := svc.SearchMarketDataSymbols(
		context.Background(), "mock-1",
		backend.MarketDataSymbolSearchInput{Query: "AAPL"},
	)
	if err != nil {
		t.Fatalf("SearchMarketDataSymbols: %v", err)
	}
	if got.Supported || len(got.Matches) != 0 {
		t.Fatalf("search = %+v, want unsupported empty result", got)
	}
}

func TestService_SearchMarketDataSymbolsNotFound(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService()

	_, err := svc.SearchMarketDataSymbols(
		context.Background(), "missing",
		backend.MarketDataSymbolSearchInput{Query: "AAPL"},
	)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SearchMarketDataSymbols(missing) err = %v, want ErrNotFound", err)
	}
}

func containsProviderType(providers []backend.MarketDataProvider, want string) bool {
	for _, provider := range providers {
		if provider.Type == want {
			return true
		}
	}
	return false
}

func TestService_CommandEnabledResolves(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mcpAccess = map[string]bool{"check_order": false}
	ctx := context.Background()

	if enabled, err := svc.CommandEnabled(ctx, "check_order"); err != nil || enabled {
		t.Fatalf("check_order override should disable: enabled=%v err=%v", enabled, err)
	}
	if enabled, err := svc.CommandEnabled(ctx, "health"); err != nil || !enabled {
		t.Fatalf("health should default enabled: enabled=%v err=%v", enabled, err)
	}
	if enabled, err := svc.CommandEnabled(ctx, "submit_order"); err != nil || enabled {
		t.Fatalf("submit_order should default disabled: enabled=%v err=%v", enabled, err)
	}
}
