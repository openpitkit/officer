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
	"errors"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
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
type fakeRouter struct{ node *fakeNode }

func (r *fakeRouter) Route(node.Key) (node.Node, error) { return r.node, nil }
func (r *fakeRouter) All() []node.Node                  { return []node.Node{r.node} }

func newTestService() (*backend.Service, *fakeNode) {
	fn := &fakeNode{}
	return backend.New(&fakeRouter{node: fn}), fn
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

func TestService_PutLimitChecksAccountExists(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.getAccountErr = domain.ErrNotFound
	ctx := context.Background()

	limit := domain.Limit{
		Target: domain.LimitTarget{
			Policy:  domain.PolicyRateLimit,
			Scope:   domain.ScopeAccountAsset,
			Account: "acc-1",
			Asset:   "AAPL",
		},
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}
	if err := svc.PutLimit(ctx, limit); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound for missing account, got %v", err)
	}
	if len(fn.putLimitCalls) != 0 {
		t.Fatalf("missing account must not reach PutLimit")
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
	if !containsProviderType(status.Providers, domain.MarketDataProviderBinance) {
		t.Fatalf("providers = %+v, want binance", status.Providers)
	}
	if len(status.Instances) != 1 || len(status.Instances[0].Instruments) != 2 {
		t.Fatalf("unexpected status: %+v", status)
	}
	first := status.Instances[0].Instruments[0]
	second := status.Instances[0].Instruments[1]
	if first.Quote == nil || first.Stale {
		t.Fatalf("fresh quoted instrument should not be stale: %+v", first)
	}
	if !second.Stale {
		t.Fatalf("enabled instrument without quote should be stale: %+v", second)
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
