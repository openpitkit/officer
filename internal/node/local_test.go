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

package node

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
	"go.openpit.dev/officer/internal/marketdata"
	"go.openpit.dev/officer/internal/store"
)

// fakeEngine is a stand-in Engine that records calls and can be configured to
// fail the next mutating call. It is not the openpit adapter, so the node tests
// run without the native runtime.
type fakeEngine struct {
	running bool

	configureCalls []configureCall
	blockCalls     []blockCall
	unblockCalls   []domain.AccountID

	adjustmentCalls      []adjustmentCall
	submitCalls          []domain.Order
	execReportCalls      []domain.ExecutionReportInput
	registerGroupCalls   []groupCall
	unregisterGroupCalls []groupCall
	blockGroupCalls      []blockGroupCall
	unblockGroupCalls    []string

	// Canned engine outcomes for the trading/spot-funds/group paths.
	adjustmentAccepted *domain.AdjustmentOutcomeAccepted
	adjustmentReject   *domain.AdjustmentOutcomeRejected
	submitLockPrices   []string
	submitReject       *domain.OrderReject
	execReportBlocks   []domain.ExecutionAccountBlock

	// Canned dry-run outcome and recorded probes for the check path.
	checkResult domain.CheckResult
	checkProbes []domain.OrderProbe

	// configureErr, when set, is returned by ConfigurePolicy instead of the
	// generic failure; it lets a test assert a specific wrapped sentinel
	// propagates through the node.
	configureErr error

	failConfigure  bool
	failBlock      bool
	failAdjustment bool
	failSubmit     bool
	failExecReport bool
	failGroup      bool
}

type configureCall struct {
	policy string
	limits []domain.Limit
}

type blockCall struct {
	id     domain.AccountID
	reason string
}

type adjustmentCall struct {
	account domain.AccountID
	req     domain.AdjustmentRequest
}

type groupCall struct {
	accounts []domain.AccountID
	groupID  string
}

type blockGroupCall struct {
	groupID string
	reason  string
}

func newFakeEngine() *fakeEngine { return &fakeEngine{running: true} }

// fakeBuild returns a BuildFunc that records the seed snapshot it was given and
// hands back eng. The captured snapshot lets a test assert the build was seeded
// from the store.
func fakeBuild(eng engine.Engine, captured *engine.Snapshot) engine.BuildFunc {
	return func(snap engine.Snapshot) (engine.Engine, error) {
		*captured = snap
		return eng, nil
	}
}

func (e *fakeEngine) Version() string      { return "fake" }
func (e *fakeEngine) BuildProfile() string { return "test" }
func (e *fakeEngine) Running() bool        { return e.running }

func (e *fakeEngine) ConfigurePolicy(
	_ context.Context, policy string, limits []domain.Limit,
) error {
	if e.configureErr != nil {
		return e.configureErr
	}
	if e.failConfigure {
		return errors.New("configure failed")
	}
	e.configureCalls = append(e.configureCalls, configureCall{policy, limits})
	return nil
}

func (e *fakeEngine) BlockAccount(
	_ context.Context, id domain.AccountID, reason string,
) error {
	if e.failBlock {
		return errors.New("block failed")
	}
	e.blockCalls = append(e.blockCalls, blockCall{id, reason})
	return nil
}

func (e *fakeEngine) UnblockAccount(_ context.Context, id domain.AccountID) error {
	if e.failBlock {
		return errors.New("unblock failed")
	}
	e.unblockCalls = append(e.unblockCalls, id)
	return nil
}

func (e *fakeEngine) ApplyAccountAdjustment(
	_ context.Context, account domain.AccountID, req domain.AdjustmentRequest,
) (engine.AdjustmentResult, error) {
	if e.failAdjustment {
		return engine.AdjustmentResult{}, errors.New("adjustment failed")
	}
	e.adjustmentCalls = append(e.adjustmentCalls, adjustmentCall{account, req})
	if e.adjustmentReject != nil {
		return engine.AdjustmentResult{Rejected: e.adjustmentReject}, nil
	}
	accepted := e.adjustmentAccepted
	if accepted == nil {
		accepted = &domain.AdjustmentOutcomeAccepted{}
	}
	return engine.AdjustmentResult{Accepted: accepted}, nil
}

func (e *fakeEngine) SubmitOrder(
	_ context.Context, o domain.Order,
) (engine.OrderResult, error) {
	if e.failSubmit {
		return engine.OrderResult{}, errors.New("submit failed")
	}
	e.submitCalls = append(e.submitCalls, o)
	if e.submitReject != nil {
		return engine.OrderResult{Accepted: false, Rejects: []domain.OrderReject{*e.submitReject}}, nil
	}
	return engine.OrderResult{Accepted: true, LockPrices: e.submitLockPrices}, nil
}

func (e *fakeEngine) ApplyExecutionReport(
	_ context.Context, in domain.ExecutionReportInput,
) (engine.ExecutionReportResult, error) {
	if e.failExecReport {
		return engine.ExecutionReportResult{}, errors.New("exec report failed")
	}
	e.execReportCalls = append(e.execReportCalls, in)
	return engine.ExecutionReportResult{Blocks: e.execReportBlocks}, nil
}

func (e *fakeEngine) RegisterGroup(
	_ context.Context, accounts []domain.AccountID, groupID string,
) error {
	if e.failGroup {
		return errors.New("register group failed")
	}
	e.registerGroupCalls = append(e.registerGroupCalls, groupCall{accounts, groupID})
	return nil
}

func (e *fakeEngine) UnregisterGroup(
	_ context.Context, accounts []domain.AccountID, groupID string,
) error {
	if e.failGroup {
		return errors.New("unregister group failed")
	}
	e.unregisterGroupCalls = append(e.unregisterGroupCalls, groupCall{accounts, groupID})
	return nil
}

func (e *fakeEngine) BlockGroup(_ context.Context, groupID, reason string) error {
	if e.failGroup {
		return errors.New("block group failed")
	}
	e.blockGroupCalls = append(e.blockGroupCalls, blockGroupCall{groupID, reason})
	return nil
}

func (e *fakeEngine) UnblockGroup(_ context.Context, groupID string) error {
	if e.failGroup {
		return errors.New("unblock group failed")
	}
	e.unblockGroupCalls = append(e.unblockGroupCalls, groupID)
	return nil
}

func (e *fakeEngine) CheckOrder(
	_ context.Context, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	e.checkProbes = append(e.checkProbes, probe)
	return e.checkResult, nil
}

// MarketDataSink returns a no-op sink: the node tests do not exercise quote
// ingestion, and the fake engine carries no market-data service.
func (e *fakeEngine) MarketDataSink() marketdata.Sink { return nopSink{} }

func (e *fakeEngine) Stop() { e.running = false }

// nopSink is a Sink that drops every quote; it stands in for the engine's sink
// in node tests that never push.
type nopSink struct{}

func (nopSink) Push(marketdata.QuoteUpdate) error { return nil }

// newTestNode builds a localNode over a real temp SQLite store and the fake
// engine. NewLocalNode seeds the build from the (freshly migrated, empty) store
// and writes one startup hydrate audit row, so a fresh node already has exactly
// one audit row.
func newTestNode(t *testing.T, eng engine.Engine) (*localNode, store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node.db")
	st, err := store.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var seed engine.Snapshot
	n, _, err := NewLocalNode(ctx, st, fakeBuild(eng, &seed))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	return n.(*localNode), st
}

func testKey(id domain.AccountID) Key {
	return Key{Tenant: domain.DefaultTenant, Account: id}
}

// testCaller is the attribution the node tests stamp on mutations.
var testCaller = domain.Caller{Source: domain.SourceAPI, Principal: "operator"}

func TestLocalNode_PutLimitAppliesAndAudits(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	limit := domain.Limit{
		Target: domain.LimitTarget{
			Tenant: domain.DefaultTenant,
			Policy: domain.PolicyRateLimit,
			Scope:  domain.ScopeBroker,
		},
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}
	if err := n.PutLimit(ctx, limit, testCaller); err != nil {
		t.Fatalf("PutLimit: %v", err)
	}

	if len(eng.configureCalls) != 1 {
		t.Fatalf("want 1 configure call, got %d", len(eng.configureCalls))
	}
	if eng.configureCalls[0].policy != domain.PolicyRateLimit {
		t.Fatalf("configured wrong policy: %q", eng.configureCalls[0].policy)
	}
	if len(eng.configureCalls[0].limits) != 1 {
		t.Fatalf("want full barrier set of 1, got %d",
			len(eng.configureCalls[0].limits))
	}

	stored, err := st.ListPolicyLimits(ctx, domain.PolicyRateLimit)
	if err != nil {
		t.Fatalf("ListPolicyLimits: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("want 1 stored barrier, got %d", len(stored))
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// Startup hydrate row plus the set_limit row, newest first.
	if len(rows) != 2 || rows[0].Action != domain.AuditActionSetLimit {
		t.Fatalf("want newest set_limit over startup hydrate, got %+v", rows)
	}
}

func TestLocalNode_PutLimitEngineFailureRevertsStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.failConfigure = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	limit := domain.Limit{
		Target: domain.LimitTarget{
			Tenant: domain.DefaultTenant,
			Policy: domain.PolicyRateLimit,
			Scope:  domain.ScopeBroker,
		},
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}
	if err := n.PutLimit(ctx, limit, testCaller); err == nil {
		t.Fatalf("PutLimit: want error on engine failure")
	}

	stored, err := st.ListPolicyLimits(ctx, domain.PolicyRateLimit)
	if err != nil {
		t.Fatalf("ListPolicyLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("want store reverted to empty, got %d barriers", len(stored))
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// Only the startup hydrate row: the failed mutation audits nothing.
	if len(rows) != 1 || rows[0].Action != domain.AuditActionHydrate {
		t.Fatalf("want only the startup hydrate row, got %+v", rows)
	}
}

// TestLocalNode_PutLimitNotImplementedSurfacesAndReverts verifies the
// no-rebuild contract: when the runtime Configure surface reports the
// not-implemented stub, the node surfaces that error (it does NOT rebuild) and
// reverts the already-written store row, so the barrier does not persist and
// no mutation audit is written. Closing that gap is engine-side SDK work, not
// an officer rebuild.
func TestLocalNode_PutLimitNotImplementedSurfacesAndReverts(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.configureErr = fmt.Errorf(
		"engine: rate_limit add not supported: %w", domain.ErrNotImplemented)
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	limit := domain.Limit{
		Target: domain.LimitTarget{
			Tenant: domain.DefaultTenant,
			Policy: domain.PolicyRateLimit,
			Scope:  domain.ScopeBroker,
		},
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}
	err := n.PutLimit(ctx, limit, testCaller)
	if err == nil {
		t.Fatalf("PutLimit: want error surfacing the not-implemented stub")
	}
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("PutLimit error = %v, want it to wrap ErrNotImplemented", err)
	}

	// The store row is reverted, so the barrier does not persist.
	stored, err := st.ListPolicyLimits(ctx, domain.PolicyRateLimit)
	if err != nil {
		t.Fatalf("ListPolicyLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("want store reverted to 0 barriers, got %d", len(stored))
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// Only the startup hydrate row: the failed mutation writes no audit.
	if len(rows) != 1 {
		t.Fatalf("want only the hydrate audit row, got %+v", rows)
	}
	if rows[0].Action != domain.AuditActionHydrate {
		t.Fatalf("want the only row to be hydrate, got %q", rows[0].Action)
	}
}

func TestLocalNode_PutLimitEngineFailureRestoresPrevious(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	target := domain.LimitTarget{
		Tenant: domain.DefaultTenant,
		Policy: domain.PolicyRateLimit,
		Scope:  domain.ScopeBroker,
	}
	first := domain.Limit{
		Target: target,
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}
	if err := n.PutLimit(ctx, first, testCaller); err != nil {
		t.Fatalf("PutLimit first: %v", err)
	}

	eng.failConfigure = true
	second := domain.Limit{
		Target: target,
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "5"},
			{Kind: domain.KindWindow, Value: "2s"},
		},
	}
	if err := n.PutLimit(ctx, second, testCaller); err == nil {
		t.Fatalf("PutLimit second: want error")
	}

	stored, err := st.ListPolicyLimits(ctx, domain.PolicyRateLimit)
	if err != nil {
		t.Fatalf("ListPolicyLimits: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("want 1 barrier after revert, got %d", len(stored))
	}
	values := map[string]string{}
	for _, v := range stored[0].Values {
		values[v.Kind] = v.Value
	}
	if values[domain.KindMaxOrders] != "100" || values[domain.KindWindow] != "1s" {
		t.Fatalf("barrier not restored to previous: %+v", values)
	}
}

func TestLocalNode_DeleteLimitAppliesAndAudits(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	target := domain.LimitTarget{
		Tenant: domain.DefaultTenant,
		Policy: domain.PolicyRateLimit,
		Scope:  domain.ScopeBroker,
	}
	if err := n.PutLimit(ctx, domain.Limit{
		Target: target,
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}, testCaller); err != nil {
		t.Fatalf("PutLimit: %v", err)
	}

	if err := n.DeleteLimit(ctx, target, testCaller); err != nil {
		t.Fatalf("DeleteLimit: %v", err)
	}

	stored, err := st.ListPolicyLimits(ctx, domain.PolicyRateLimit)
	if err != nil {
		t.Fatalf("ListPolicyLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("want 0 barriers after delete, got %d", len(stored))
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// Startup hydrate, set_limit, delete_limit = 3 rows, newest first.
	if len(rows) != 3 || rows[0].Action != domain.AuditActionDeleteLimit {
		t.Fatalf("want newest row delete_limit, got %+v", rows)
	}
}

func TestLocalNode_BlockUnblockAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testKey(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", testCaller); err != nil {
		t.Fatalf("block: %v", err)
	}
	if len(eng.blockCalls) != 1 || eng.blockCalls[0].reason != "risk" {
		t.Fatalf("engine block not applied: %+v", eng.blockCalls)
	}

	account, _, err := n.GetAccountState(ctx, testKey(id))
	if err != nil {
		t.Fatalf("GetAccountState: %v", err)
	}
	if !account.Blocked || account.BlockReason != "risk" {
		t.Fatalf("account not blocked in store: %+v", account)
	}

	if err := n.SetAccountBlocked(ctx, testKey(id), false, "", testCaller); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	if len(eng.unblockCalls) != 1 {
		t.Fatalf("engine unblock not applied: %+v", eng.unblockCalls)
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// startup hydrate, create_account, block, unblock = 4 rows, newest first.
	if len(rows) != 4 || rows[0].Action != domain.AuditActionUnblock {
		t.Fatalf("unexpected audit trail: %+v", rows)
	}
}

func TestLocalNode_BlockEngineFailureRevertsStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testKey(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	eng.failBlock = true
	if err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", testCaller); err == nil {
		t.Fatalf("block: want error on engine failure")
	}

	account, ok, err := st.GetAccount(ctx, domain.DefaultTenant, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: %v ok=%v", err, ok)
	}
	if account.Blocked {
		t.Fatalf("store not reverted: account still blocked")
	}
}

// TestNewLocalNode_SeedsBuildAndAudits verifies the constructor seeds the
// engine build from the store snapshot and writes one startup hydrate audit
// row. The store is pre-populated before the node is built so the seed is
// non-empty.
func TestNewLocalNode_SeedsBuildAndAudits(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "node.db")
	st, err := store.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Pre-seed the store: one account and one rate-limit barrier.
	if err := st.CreateAccount(ctx, domain.Account{
		Tenant: domain.DefaultTenant, ID: "acc-1",
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.PutLimit(ctx, domain.Limit{
		Target: domain.LimitTarget{
			Tenant: domain.DefaultTenant,
			Policy: domain.PolicyRateLimit,
			Scope:  domain.ScopeBroker,
		},
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}); err != nil {
		t.Fatalf("PutLimit: %v", err)
	}

	eng := newFakeEngine()
	var seed engine.Snapshot
	n, got, err := NewLocalNode(ctx, st, fakeBuild(eng, &seed))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	if got != eng {
		t.Fatalf("NewLocalNode returned a different engine handle")
	}

	// The build must be seeded from the store snapshot.
	if len(seed.Accounts) != 1 || len(seed.Limits) != 1 {
		t.Fatalf("build not seeded from store: %+v", seed)
	}
	if seed.Accounts[0].ID != "acc-1" {
		t.Fatalf("seeded wrong account: %+v", seed.Accounts)
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 ||
		rows[0].Action != domain.AuditActionHydrate || rows[0].Actor != "system" {
		t.Fatalf("want one system hydrate row, got %+v", rows)
	}

	if err := n.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestNewLocalNode_BuildFailureClosesNothingExtra verifies a build failure is
// surfaced and no node is returned.
func TestNewLocalNode_BuildFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "node.db")
	st, err := store.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	failBuild := func(engine.Snapshot) (engine.Engine, error) {
		return nil, errors.New("build boom")
	}
	if _, _, err := NewLocalNode(ctx, st, failBuild); err == nil {
		t.Fatalf("NewLocalNode: want error on build failure")
	}

	// A failed build writes no audit row.
	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("want no audit row on build failure, got %+v", rows)
	}
}

// TestNewLocalNode_NilArgs verifies the constructor rejects nil dependencies.
func TestNewLocalNode_NilArgs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	build := func(engine.Snapshot) (engine.Engine, error) {
		return newFakeEngine(), nil
	}
	if _, _, err := NewLocalNode(ctx, nil, build); err == nil {
		t.Fatalf("want error for nil store")
	}

	path := filepath.Join(t.TempDir(), "node.db")
	st, err := store.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, _, err := NewLocalNode(ctx, st, nil); err == nil {
		t.Fatalf("want error for nil build func")
	}
}

func TestLocalNode_CheckOrderDelegatesToEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.checkResult = domain.CheckResult{Passed: true, WouldLockPrices: []string{"100"}}
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	probe := domain.OrderProbe{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
		AmountValue: "1", Price: "100",
	}
	out, err := n.CheckOrder(ctx, testKey("acc-1"), probe)
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if !out.Passed || len(out.WouldLockPrices) != 1 {
		t.Fatalf("engine result not propagated: %+v", out)
	}
	if len(eng.checkProbes) != 1 || eng.checkProbes[0].Account != "acc-1" {
		t.Fatalf("probe must be forwarded to the engine once")
	}
}

// TestLocalNode_CheckOrderWritesNoAudit asserts the non-mutating check writes
// no audit row and is side-effect-free across repeated calls: the audit count
// is unchanged from before the first check, and the engine is only ever asked
// to dry-run (the node never calls SubmitOrder/commit for a check).
func TestLocalNode_CheckOrderWritesNoAudit(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.checkResult = domain.CheckResult{
		Passed:  false,
		Rejects: []domain.OrderReject{{Code: "rate_limit_exceeded", Scope: "account"}},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	before, err := st.ListAudit(ctx, 1000)
	if err != nil {
		t.Fatalf("ListAudit before: %v", err)
	}

	probe := domain.OrderProbe{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
	}
	for i := 0; i < 3; i++ {
		if _, err := n.CheckOrder(ctx, testKey("acc-1"), probe); err != nil {
			t.Fatalf("CheckOrder #%d: %v", i, err)
		}
	}

	after, err := st.ListAudit(ctx, 1000)
	if err != nil {
		t.Fatalf("ListAudit after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("check must write no audit row: before=%d after=%d", len(before), len(after))
	}
	if len(eng.submitCalls) != 0 || len(eng.execReportCalls) != 0 {
		t.Fatalf("check must not submit or settle: submit=%d exec=%d",
			len(eng.submitCalls), len(eng.execReportCalls))
	}
	if len(eng.checkProbes) != 3 {
		t.Fatalf("want 3 dry-run calls, got %d", len(eng.checkProbes))
	}
}

func TestLocalNode_PutLimitNoAccountRequired(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	// The account "acc-never-created" was never registered. PutLimit must
	// succeed: policy rules are valid before the account exists.
	limit := domain.Limit{
		Target: domain.LimitTarget{
			Tenant:  domain.DefaultTenant,
			Policy:  domain.PolicyRateLimit,
			Scope:   domain.ScopeAccountAsset,
			Account: "acc-never-created",
			Asset:   "AAPL",
		},
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "50"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}
	if err := n.PutLimit(ctx, limit, testCaller); err != nil {
		t.Fatalf("PutLimit for non-existent account: %v", err)
	}
	if len(eng.configureCalls) != 1 {
		t.Fatalf("want 1 configure call, got %d", len(eng.configureCalls))
	}
}

func TestLocalNode_ErrorMessagesNoNodePrefix(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	// GetAccountState on a missing account must surface a not-found error whose
	// message does not start with "node: ".
	_, _, err := n.GetAccountState(ctx, testKey("no-such-account"))
	if err == nil {
		t.Fatal("want error for missing account, got nil")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	msg := err.Error()
	if len(msg) >= 6 && msg[:6] == "node: " {
		t.Fatalf("error message must not start with \"node: \", got: %s", msg)
	}

	// SetAccountBlocked on a missing account must also not carry "node: ".
	err = n.SetAccountBlocked(ctx, testKey("no-such-account"), true, "test", testCaller)
	if err == nil {
		t.Fatal("want error for missing account on block, got nil")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	msg = err.Error()
	if len(msg) >= 6 && msg[:6] == "node: " {
		t.Fatalf("error message must not start with \"node: \", got: %s", msg)
	}
}
