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
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/backup"
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
	holdOutcomes       []engine.BalanceOutcome
	submitReject       *domain.OrderReject
	execReportBlocks   []domain.ExecutionAccountBlock
	execReportOutcomes []engine.BalanceOutcome
	reconcileCount     int

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
	commitErr      error
	rollbackErr    error
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

// ReserveHold, CommitHeld, RollbackHeld, SubmitImmediate, and ReconcileOrphans
// satisfy the held-reservation surface of the Engine interface. The node tests
// do not exercise the approval flow, so these are minimal stand-ins.
func (e *fakeEngine) ReserveHold(
	_ context.Context, o domain.Order,
) (engine.HoldResult, error) {
	if e.failSubmit {
		return engine.HoldResult{}, errors.New("reserve hold failed")
	}
	e.submitCalls = append(e.submitCalls, o)
	if e.submitReject != nil {
		return engine.HoldResult{Accepted: false, Rejects: []domain.OrderReject{*e.submitReject}}, nil
	}
	return engine.HoldResult{
		Accepted:   true,
		LockPrices: e.submitLockPrices,
		Outcomes:   e.holdOutcomes,
	}, nil
}

func (e *fakeEngine) CommitHeld(_ context.Context, _ string) error { return e.commitErr }

func (e *fakeEngine) RollbackHeld(_ context.Context, _ string) error { return e.rollbackErr }

func (e *fakeEngine) SubmitImmediate(
	_ context.Context, o domain.Order,
) (engine.ImmediateResult, error) {
	if e.failSubmit {
		return engine.ImmediateResult{}, errors.New("submit immediate failed")
	}
	e.submitCalls = append(e.submitCalls, o)
	if e.submitReject != nil {
		return engine.ImmediateResult{Accepted: false, Rejects: []domain.OrderReject{*e.submitReject}}, nil
	}
	return engine.ImmediateResult{
		Accepted:     true,
		LockPrices:   e.submitLockPrices,
		FillQuantity: o.AmountValue,
	}, nil
}

func (e *fakeEngine) SetReservationStore(_ engine.ReservationStore) {}

func (e *fakeEngine) ReconcileOrphans(_ context.Context) (int, error) {
	return e.reconcileCount, nil
}

func (e *fakeEngine) ApplyExecutionReport(
	_ context.Context, in domain.ExecutionReportInput,
) (engine.ExecutionReportResult, error) {
	if e.failExecReport {
		return engine.ExecutionReportResult{}, errors.New("exec report failed")
	}
	e.execReportCalls = append(e.execReportCalls, in)
	return engine.ExecutionReportResult{
		Blocks:   e.execReportBlocks,
		Outcomes: e.execReportOutcomes,
	}, nil
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

type failRestoreAuditStore struct {
	store.Store
}

var errRestoreAuditFailed = errors.New("restore audit failed")

func (s *failRestoreAuditStore) AppendAudit(
	ctx context.Context,
	entry store.AuditEntry,
) error {
	if entry.Action == domain.AuditActionRestoreBackup {
		return errRestoreAuditFailed
	}
	return s.Store.AppendAudit(ctx, entry)
}

type failRollbackRestoreStore struct {
	store.Store
	rollbackErr  error
	restoreCalls int
}

func (s *failRollbackRestoreStore) RestoreBackup(
	ctx context.Context,
	archive backup.Archive,
	opts backup.RestoreOptions,
) (backup.RestoreSummary, error) {
	s.restoreCalls++
	if s.restoreCalls > 1 {
		return backup.RestoreSummary{}, s.rollbackErr
	}
	return s.Store.RestoreBackup(ctx, archive, opts)
}

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

func TestLocalNode_ReconcileOrphansPreservesOrders(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.reconcileCount = 1
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order, err := st.CreateOrder(ctx, domain.Order{
		Tenant:      domain.DefaultTenant,
		Account:     "acc-1",
		Source:      domain.SourceAPI,
		Principal:   "operator",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "10",
		Price:       "100",
		Status:      domain.OrderStatusAccepted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if err := st.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID:     "approval-1",
		OrderID:        order.ID,
		Account:        order.Account,
		ParamsJSON:     `{"id":1}`,
		LockPricesJSON: `["100"]`,
		IssuedAt:       time.Now().UTC(),
		ExpiresAt:      time.Now().UTC().Add(2 * time.Minute),
		State:          domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}

	count, err := n.ReconcileOrphans(ctx)
	if err != nil {
		t.Fatalf("ReconcileOrphans: %v", err)
	}
	if count != 1 {
		t.Fatalf("held intents = %d, want 1", count)
	}
	detail, err := st.GetOrder(ctx, domain.DefaultTenant, order.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusAccepted {
		t.Fatalf("order status = %q, want accepted", detail.Order.Status)
	}
	events, err := st.ListOrderEvents(ctx, domain.DefaultTenant, order.ID)
	if err != nil {
		t.Fatalf("ListOrderEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v, want no rollback events", events)
	}
}

func TestLocalNode_SubmitHoldPersistsHeldBalances(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitLockPrices = []string{"100"}
	eng.holdOutcomes = []engine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceDelta:  "-2000",
			BalanceResult: "8000",
			HeldDelta:     "2000",
			HeldResult:    "2000",
		},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testKey("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Tenant:    domain.DefaultTenant,
		Account:   "acc-1",
		Asset:     "USD",
		Available: "10000",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	order, result, err := n.SubmitHold(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitHold: %v", err)
	}
	if !result.Accepted || order.Status != domain.OrderStatusAccepted {
		t.Fatalf("hold not accepted: order=%+v result=%+v", order, result)
	}
	balance, ok, err := st.GetBalance(ctx, domain.DefaultTenant, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance: %v ok=%v", err, ok)
	}
	if balance.Available != "8000" || balance.Held != "2000" {
		t.Fatalf("balance = %+v, want available=8000 held=2000", balance)
	}
}

func TestLocalNode_CancelHeldFallbackReleasesPersistedHold(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.rollbackErr = fmt.Errorf("engine: reservation %q: %w", "approval-1", domain.ErrNotFound)
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceResult: "10000",
		HeldResult:    "0",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	order, err := st.CreateOrder(ctx, domain.Order{
		Tenant:      domain.DefaultTenant,
		Account:     "acc-1",
		Source:      domain.SourceAPI,
		Principal:   "operator",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
		Status:      domain.OrderStatusAccepted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Tenant:    domain.DefaultTenant,
		Account:   "acc-1",
		Asset:     "USD",
		Available: "8000",
		Held:      "2000",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	outcomes := []engine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceDelta: "-2000",
			HeldDelta:    "2000",
		},
	}}
	payload, err := json.Marshal(reservationIntentPayload{
		Order:    order,
		Outcomes: outcomes,
	})
	if err != nil {
		t.Fatalf("Marshal payload: %v", err)
	}
	if err := st.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID:     "approval-1",
		OrderID:        order.ID,
		Account:        order.Account,
		ParamsJSON:     string(payload),
		LockPricesJSON: `["100"]`,
		IssuedAt:       time.Now().UTC(),
		ExpiresAt:      time.Now().UTC().Add(2 * time.Minute),
		State:          domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}

	cancelled, err := n.CancelHeld(
		ctx, domain.DefaultTenant, order.ID, "approval-1", testCaller)
	if err != nil {
		t.Fatalf("CancelHeld: %v", err)
	}
	if cancelled.Status != domain.OrderStatusCancelled {
		t.Fatalf("order status = %q, want cancelled", cancelled.Status)
	}
	balance, ok, err := st.GetBalance(ctx, domain.DefaultTenant, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance: %v ok=%v", err, ok)
	}
	if balance.Available != "10000" || balance.Held != "0" {
		t.Fatalf("balance = %+v, want available=10000 held=0", balance)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %d, want 1", len(eng.adjustmentCalls))
	}
	req := eng.adjustmentCalls[0].req
	if req.Balance == nil || req.Balance.Value != "2000" ||
		req.Held == nil || req.Held.Value != "-2000" {
		t.Fatalf("release request = %+v, want balance +2000 held -2000", req)
	}
	open, err := st.ListOpenReservationIntents(ctx)
	if err != nil {
		t.Fatalf("ListOpenReservationIntents: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open intents = %+v, want none", open)
	}
}

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
	if _, err := n.PutLimit(ctx, limit, testCaller); err != nil {
		t.Fatalf("PutLimit: %v", err)
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
	if _, err := n.PutLimit(ctx, limit, testCaller); err == nil {
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

func TestLocalNode_PutLimitConfiguresPolicyFromStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
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
	if _, err := n.PutLimit(ctx, limit, testCaller); err != nil {
		t.Fatalf("PutLimit: %v", err)
	}

	if len(eng.configureCalls) != 1 {
		t.Fatalf("configure calls = %d, want 1", len(eng.configureCalls))
	}
	if eng.configureCalls[0].policy != domain.PolicyRateLimit {
		t.Fatalf("configured policy = %q, want rate_limit", eng.configureCalls[0].policy)
	}
	if len(eng.configureCalls[0].limits) != 1 {
		t.Fatalf("configured limits = %d, want 1", len(eng.configureCalls[0].limits))
	}
	if eng.configureCalls[0].limits[0].Target != limit.Target {
		t.Fatalf("configured wrong target: %+v", eng.configureCalls[0].limits[0].Target)
	}
}

func TestLocalNode_PutLimitNotImplementedRebuildsFromStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.configureErr = fmt.Errorf("engine: unregistered policy: %w",
		domain.ErrNotImplemented)
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	next := newFakeEngine()
	var rebuilt engine.Snapshot
	n.build = fakeBuild(next, &rebuilt)

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
	sink, err := n.PutLimit(ctx, limit, testCaller)
	if err != nil {
		t.Fatalf("PutLimit: %v", err)
	}
	if sink == nil {
		t.Fatal("PutLimit returned nil sink after rebuild")
	}
	if eng.running {
		t.Fatal("old engine still running after rebuild")
	}
	if !next.running {
		t.Fatal("replacement engine is not running after rebuild")
	}
	if len(rebuilt.Limits) != 1 || rebuilt.Limits[0].Target != limit.Target {
		t.Fatalf("rebuilt limits = %+v, want the new barrier", rebuilt.Limits)
	}
}

func TestLocalNode_PutLimitSamePolicyDifferentAccountsRetunesOnePolicy(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	buildCalls := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		buildCalls++
		return newFakeEngine(), nil
	}

	first := domain.Limit{
		Target: domain.LimitTarget{
			Tenant:  domain.DefaultTenant,
			Policy:  domain.PolicyRateLimit,
			Scope:   domain.ScopeAccount,
			Account: "acc-1",
		},
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}
	second := domain.Limit{
		Target: domain.LimitTarget{
			Tenant:  domain.DefaultTenant,
			Policy:  domain.PolicyRateLimit,
			Scope:   domain.ScopeAccount,
			Account: "acc-2",
		},
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "10"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}
	if _, err := n.PutLimit(ctx, first, testCaller); err != nil {
		t.Fatalf("PutLimit first: %v", err)
	}
	if _, err := n.PutLimit(ctx, second, testCaller); err != nil {
		t.Fatalf("PutLimit second: %v", err)
	}

	if buildCalls != 0 {
		t.Fatalf("build calls = %d, want 0", buildCalls)
	}
	if len(eng.configureCalls) != 2 {
		t.Fatalf("configure calls = %d, want 2", len(eng.configureCalls))
	}
	last := eng.configureCalls[1]
	if last.policy != domain.PolicyRateLimit {
		t.Fatalf("configured policy = %q, want rate_limit", last.policy)
	}
	if len(last.limits) != 2 {
		t.Fatalf("configured barriers = %d, want 2", len(last.limits))
	}
	got := map[domain.AccountID]bool{}
	for _, limit := range last.limits {
		got[limit.Target.Account] = true
	}
	if !got["acc-1"] || !got["acc-2"] {
		t.Fatalf("configured accounts = %+v, want acc-1 and acc-2", got)
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
	if _, err := n.PutLimit(ctx, first, testCaller); err != nil {
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
	if _, err := n.PutLimit(ctx, second, testCaller); err == nil {
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
	if _, err := n.PutLimit(ctx, domain.Limit{
		Target: target,
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}, testCaller); err != nil {
		t.Fatalf("PutLimit: %v", err)
	}

	if _, err := n.DeleteLimit(ctx, target, testCaller); err != nil {
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

func TestLocalNode_DeleteLastLimitRebuildsFromStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	target := domain.LimitTarget{
		Tenant: domain.DefaultTenant,
		Policy: domain.PolicyRateLimit,
		Scope:  domain.ScopeBroker,
	}
	if _, err := n.PutLimit(ctx, domain.Limit{
		Target: target,
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}, testCaller); err != nil {
		t.Fatalf("PutLimit: %v", err)
	}

	eng.configureErr = fmt.Errorf("engine: empty policy settings: %w",
		domain.ErrNotImplemented)
	next := newFakeEngine()
	var rebuilt engine.Snapshot
	n.build = fakeBuild(next, &rebuilt)
	sink, err := n.DeleteLimit(ctx, target, testCaller)
	if err != nil {
		t.Fatalf("DeleteLimit: %v", err)
	}
	if sink == nil {
		t.Fatal("DeleteLimit returned nil sink after rebuild")
	}
	if eng.running {
		t.Fatal("old engine still running after rebuild")
	}
	if !next.running {
		t.Fatal("replacement engine is not running after rebuild")
	}
	if len(rebuilt.Limits) != 0 {
		t.Fatalf("rebuilt limits = %+v, want no barriers", rebuilt.Limits)
	}
	stored, err := st.ListPolicyLimits(ctx, domain.PolicyRateLimit)
	if err != nil {
		t.Fatalf("ListPolicyLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("stored limits = %+v, want none", stored)
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

// testOrder records a committed buy order so an execution report has a parent
// order row to attach its event and trade to.
func testOrder(t *testing.T, st store.Store, id domain.AccountID) domain.Order {
	t.Helper()
	order, err := st.CreateOrder(context.Background(), domain.Order{
		Tenant:      domain.DefaultTenant,
		Account:     id,
		Source:      domain.SourceAPI,
		Principal:   "operator",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "400",
		Status:      domain.OrderStatusCommitted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	return order
}

// TestLocalNode_ApplyExecutionReportPersistsBothLegs verifies a spot fill
// settles both the base (bought) and quote (cash) legs into their own balance
// rows, not collapsed onto the base asset.
func TestLocalNode_ApplyExecutionReportPersistsBothLegs(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.execReportOutcomes = []engine.BalanceOutcome{
		{Asset: "AAPL", Outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "2"}},
		{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "800"}},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testKey(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		OrderID:      order.ID,
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "2",
		FillPrice:    "400",
		LockPrice:    "400",
		Final:        true,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}

	base, ok, err := st.GetBalance(ctx, domain.DefaultTenant, id, "AAPL")
	if err != nil || !ok {
		t.Fatalf("GetBalance base: %v ok=%v", err, ok)
	}
	if base.Available != "2" {
		t.Fatalf("base available = %q, want 2", base.Available)
	}
	quote, ok, err := st.GetBalance(ctx, domain.DefaultTenant, id, "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance quote: %v ok=%v", err, ok)
	}
	if quote.Available != "800" {
		t.Fatalf("quote available = %q, want 800", quote.Available)
	}
}

// TestLocalNode_ApplyExecutionReportAuditsEngineBlock verifies an engine-
// initiated block during settlement is mirrored to the account and recorded as
// a system-sourced block audit row carrying the engine reason.
func TestLocalNode_ApplyExecutionReportAuditsEngineBlock(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.execReportBlocks = []domain.ExecutionAccountBlock{
		{Account: "acc-1", Code: "pnl_kill_switch", Reason: "loss limit breached"},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testKey(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		OrderID:      order.ID,
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "2",
		FillPrice:    "400",
		LockPrice:    "400",
		Final:        true,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}

	acc, ok, err := st.GetAccount(ctx, domain.DefaultTenant, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: %v ok=%v", err, ok)
	}
	if !acc.Blocked || acc.BlockReason != "loss limit breached" {
		t.Fatalf("account not mirrored blocked: %+v", acc)
	}

	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 block audit row, got %d: %+v", len(rows), rows)
	}
	if rows[0].Source != domain.SourceSystem || rows[0].Actor != "engine" {
		t.Fatalf("block not attributed to engine/system: %+v", rows[0])
	}
	if !strings.Contains(rows[0].Detail, "loss limit breached") {
		t.Fatalf("block detail missing reason: %q", rows[0].Detail)
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
	if _, err := n.PutLimit(ctx, limit, testCaller); err != nil {
		t.Fatalf("PutLimit for non-existent account: %v", err)
	}
	if len(eng.configureCalls) != 1 {
		t.Fatalf("configure calls = %d, want 1", len(eng.configureCalls))
	}
}

func TestLocalNode_ExportBackupAudits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n, st := newTestNode(t, newFakeEngine())

	if _, err := n.ExportBackup(ctx, backup.Scope{All: true}, testCaller); err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if rows[0].Action != domain.AuditActionExportBackup {
		t.Fatalf("latest action = %q, want export_backup", rows[0].Action)
	}
	if rows[0].Actor != testCaller.Principal ||
		rows[0].Source != testCaller.Source {
		t.Fatalf("audit attribution = %+v, want %+v", rows[0], testCaller)
	}
}

func TestLocalNode_RestoreBackupRebuildsEngineFromRestoredStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	n, st := newTestNode(t, oldEngine)
	nextEngine := newFakeEngine()
	var captured engine.Snapshot
	n.build = fakeBuild(nextEngine, &captured)
	archive := backup.NewArchive(
		backupTestTime(),
		"test",
		3,
		backup.Scope{All: true},
		backup.Data{Accounts: []domain.Account{{
			Tenant: domain.DefaultTenant,
			ID:     "restored",
		}}},
	)

	summary, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if !summary.RestartRequired {
		t.Fatalf("RestartRequired = false, want true")
	}
	if oldEngine.running {
		t.Fatalf("old engine still running after restore swap")
	}
	if !nextEngine.running {
		t.Fatalf("next engine not running after restore")
	}
	if len(captured.Accounts) != 1 || captured.Accounts[0].ID != "restored" {
		t.Fatalf("build snapshot accounts = %+v", captured.Accounts)
	}
	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if rows[0].Action != domain.AuditActionRestoreBackup {
		t.Fatalf("latest action = %q, want restore_backup", rows[0].Action)
	}
}

func TestLocalNode_RestoreBackupRollsBackStoreOnRebuildFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	n, st := newTestNode(t, oldEngine)
	if _, err := n.CreateAccount(ctx, testKey("keep"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, errors.New("build failed")
	}
	archive := backup.NewArchive(
		backupTestTime(),
		"test",
		3,
		backup.Scope{All: true},
		backup.Data{Accounts: []domain.Account{{
			Tenant: domain.DefaultTenant,
			ID:     "new",
		}}},
	)

	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller); err == nil {
		t.Fatal("RestoreBackup succeeded, want rebuild failure")
	}
	if !oldEngine.running {
		t.Fatalf("old engine stopped after failed rebuild")
	}
	if _, ok, err := st.GetAccount(ctx, domain.DefaultTenant, "keep"); err != nil || !ok {
		t.Fatalf("keep account ok=%v err=%v, want present", ok, err)
	}
	if _, ok, err := st.GetAccount(ctx, domain.DefaultTenant, "new"); err != nil || ok {
		t.Fatalf("new account ok=%v err=%v, want absent", ok, err)
	}
}

func TestLocalNode_RestoreBackupRollsBackStoreAndEngineOnAuditFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	n, st := newTestNode(t, oldEngine)
	if _, err := n.CreateAccount(ctx, testKey("keep"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	nextEngine := newFakeEngine()
	rollbackEngine := newFakeEngine()
	var buildCount int
	var rollbackSnapshot engine.Snapshot
	n.build = func(snap engine.Snapshot) (engine.Engine, error) {
		buildCount++
		if buildCount == 1 {
			return nextEngine, nil
		}
		rollbackSnapshot = snap
		return rollbackEngine, nil
	}
	n.store = &failRestoreAuditStore{Store: n.store}
	archive := backup.NewArchive(
		backupTestTime(),
		"test",
		3,
		backup.Scope{All: true},
		backup.Data{Accounts: []domain.Account{{
			Tenant: domain.DefaultTenant,
			ID:     "new",
		}}},
	)

	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller); err == nil {
		t.Fatal("RestoreBackup succeeded, want audit failure")
	}
	if nextEngine.running {
		t.Fatalf("restored engine still running after rollback")
	}
	if !rollbackEngine.running {
		t.Fatalf("rollback engine not running")
	}
	if _, ok, err := st.GetAccount(ctx, domain.DefaultTenant, "keep"); err != nil || !ok {
		t.Fatalf("keep account ok=%v err=%v, want present", ok, err)
	}
	if _, ok, err := st.GetAccount(ctx, domain.DefaultTenant, "new"); err != nil || ok {
		t.Fatalf("new account ok=%v err=%v, want absent", ok, err)
	}
	if len(rollbackSnapshot.Accounts) != 1 || rollbackSnapshot.Accounts[0].ID != "keep" {
		t.Fatalf("rollback snapshot accounts = %+v", rollbackSnapshot.Accounts)
	}
}

func TestLocalNode_RestoreBackupRollsBackStoreOnAuditFailureWithoutRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	n, st := newTestNode(t, oldEngine)
	if err := st.SetMcpAccess(ctx, "submit_order", true); err != nil {
		t.Fatalf("SetMcpAccess: %v", err)
	}
	n.store = &failRestoreAuditStore{Store: n.store}
	archive := backup.NewArchive(
		backupTestTime(),
		"test",
		3,
		backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		backup.Data{McpAccess: map[string]bool{"submit_order": false}},
	)

	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller); err == nil {
		t.Fatal("RestoreBackup succeeded, want audit failure")
	}
	if !oldEngine.running {
		t.Fatalf("engine stopped for non-runtime rollback")
	}
	access, err := st.ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	if access["submit_order"] != true {
		t.Fatalf("mcp access = %+v, want rollback to true", access)
	}
}

func TestLocalNode_RestoreBackupJoinsRollbackRestoreFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n, st := newTestNode(t, newFakeEngine())
	if err := st.SetMcpAccess(ctx, "submit_order", true); err != nil {
		t.Fatalf("SetMcpAccess: %v", err)
	}
	rollbackErr := errors.New("rollback restore failed")
	n.store = &failRollbackRestoreStore{
		Store: &failRestoreAuditStore{
			Store: n.store,
		},
		rollbackErr: rollbackErr,
	}
	archive := backup.NewArchive(
		backupTestTime(),
		"test",
		3,
		backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		backup.Data{McpAccess: map[string]bool{"submit_order": false}},
	)

	_, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller)
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want audit and rollback failure")
	}
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("RestoreBackup error = %v, want rollback restore error", err)
	}
	if !errors.Is(err, errRestoreAuditFailed) {
		t.Fatalf("RestoreBackup error = %v, want audit failure", err)
	}
}

func TestLocalNode_ResetDatabaseRecreatesStoreAndAudits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reset.db")
	st, err := store.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	oldEngine := newFakeEngine()
	nextEngine := newFakeEngine()
	builds := 0
	build := func(engine.Snapshot) (engine.Engine, error) {
		builds++
		if builds == 1 {
			return oldEngine, nil
		}
		return nextEngine, nil
	}
	nn, _, err := NewLocalNode(ctx, st, build)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	if _, err := n.CreateAccount(ctx, testKey("reset-me"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	sink, err := n.ResetDatabase(ctx, testCaller)
	if err != nil {
		t.Fatalf("ResetDatabase: %v", err)
	}
	if sink == nil {
		t.Fatalf("ResetDatabase returned nil sink")
	}
	if builds != 2 {
		t.Fatalf("build calls = %d, want 2", builds)
	}
	if oldEngine.running {
		t.Fatalf("old engine still running after reset")
	}
	if !nextEngine.running {
		t.Fatalf("next engine is not running after reset")
	}
	accounts, err := st.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts after reset = %+v, want none", accounts)
	}
	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 || rows[0].Action != domain.AuditActionResetDatabase {
		t.Fatalf("audit after reset = %+v, want reset row only", rows)
	}
	if rows[0].Actor != testCaller.Principal ||
		rows[0].Source != testCaller.Source {
		t.Fatalf("audit attribution = %+v, want %+v", rows[0], testCaller)
	}
}

func TestLocalNode_ConcurrentRestoreSwapAndReads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n, _ := newTestNode(t, newFakeEngine())
	probe := domain.OrderProbe{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := n.CheckOrder(ctx, testKey("acc-1"), probe); err != nil {
				errs <- err
				return
			}
			if _, err := n.Health(ctx); err != nil {
				errs <- err
				return
			}
			_ = n.EngineVersion()
		}
	}()
	for i := 0; i < 25; i++ {
		n.build = func(engine.Snapshot) (engine.Engine, error) {
			return newFakeEngine(), nil
		}
		archive := backup.NewArchive(
			backupTestTime().Add(time.Duration(i)*time.Second),
			"test",
			3,
			backup.Scope{All: true},
			backup.Data{Accounts: []domain.Account{{
				Tenant: domain.DefaultTenant,
				ID:     domain.AccountID(fmt.Sprintf("acc-%d", i)),
			}}},
		)
		if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
			Scope: backup.Scope{All: true},
			Mode:  backup.RestoreModeReplaceAll,
		}, testCaller); err != nil {
			close(stop)
			t.Fatalf("RestoreBackup #%d: %v", i, err)
		}
	}
	close(stop)
	<-done
	select {
	case err := <-errs:
		t.Fatalf("concurrent read error: %v", err)
	default:
	}
}

func backupTestTime() time.Time {
	return time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
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
