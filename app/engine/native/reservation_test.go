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

// These tests exercise the held-reservation registry against a real OpenPit
// engine and so require the native runtime dylib at run time (set
// OPENPIT_RUNTIME_LIBRARY_PATH or build the workspace dylib first, as documented
// for the Go bindings). They build one real engine through buildEngine, seed an
// account balance, and drive the hold/commit/rollback paths through the adapter.

package native

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/openpit/param"

	"go.openpit.dev/officer/framework/domain"
)

const (
	testAccount   = "1"
	testQuote     = "USD"
	testBase      = "AAPL"
	testQuoteFund = "1000"
	testQty       = "5"
	testLimit     = "100" // 5 * 100 = 500 quote, leaves 500 after one hold
)

// testOrderXID returns a deterministic non-zero external id for a reservation
// test order, standing in for the store-assigned handle so the intent carries a
// real order ref. seed is folded into the bytes so distinct orders differ.
func testOrderXID(seed byte) domain.ExternalID {
	var raw [domain.ExternalIDByteLen]byte
	raw[0] = seed
	raw[domain.ExternalIDByteLen-1] = 0x5a
	id, err := domain.ExternalIDFromBytes(raw[:])
	if err != nil {
		panic(err)
	}
	return id
}

// newTestEngine builds a real engine adapter with one account ("1") that carries
// a stored engine id and is seeded with testQuoteFund of the quote asset, enough
// to reserve exactly two test orders. The adapter is stopped via t.Cleanup.
func newTestEngine(t *testing.T) *openPitEngine {
	t.Helper()
	snap := Snapshot{Accounts: []domain.Account{account(testAccount)}}
	res, err := newIDResolver(snap.Accounts, snap.Groups)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	eng, service, registered, err := buildEngine(snap, res)
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	adapter := newOpenPitEngine(eng, service, registered, nil, res).(*openPitEngine)
	t.Cleanup(adapter.Stop)

	if err := seedBalances(eng, []domain.Balance{{
		Account:   domain.AccountID(testAccount),
		Asset:     testQuote,
		Available: testQuoteFund,
	}}, res); err != nil {
		t.Fatalf("seed balance: %v", err)
	}
	return adapter
}

// testOrder is a limit buy that costs 500 quote, so a 1000-quote balance funds
// exactly two of them. A limit price keeps the estimate source "limit" and
// avoids needing a live market quote.
func testOrder() domain.Order {
	return domain.Order{
		Account:     domain.AccountID(testAccount),
		BaseAsset:   testBase,
		QuoteAsset:  testQuote,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: testQty,
		Price:       testLimit,
	}
}

// registrySize reports the live registry entry count.
func (e *openPitEngine) registrySize() int {
	e.registry.mu.Lock()
	defer e.registry.mu.Unlock()
	return len(e.registry.by)
}

func TestReserveHold_AcceptCapturesEstimate(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	res, err := e.ReserveHold(ctx, testOrder())
	if err != nil {
		t.Fatalf("ReserveHold: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("ReserveHold rejected: %+v", res.Rejects)
	}
	if res.ApprovalID == "" {
		t.Fatal("ReserveHold: empty approval id")
	}
	if res.EstimateSource != domain.EstimateSourceLimit {
		t.Fatalf("estimate source = %q, want %q", res.EstimateSource, domain.EstimateSourceLimit)
	}
	if res.SettlementLockPrice == "" {
		t.Fatal("ReserveHold: empty settlement lock price")
	}
	if len(res.Lock) == 0 {
		t.Fatal("ReserveHold: empty serialized lock")
	}
	// The serialized lock round-trips and its settlement price (last entry)
	// matches the returned estimate.
	prices, err := LockDisplayPrices(res.Lock)
	if err != nil {
		t.Fatalf("LockDisplayPrices: %v", err)
	}
	if len(prices) == 0 {
		t.Fatal("serialized lock carries no prices")
	}
	gotSettle, _ := param.NewPriceFromString(prices[len(prices)-1])
	wantSettle, _ := param.NewPriceFromString(res.SettlementLockPrice)
	if gotSettle.Compare(wantSettle) != 0 {
		t.Fatalf("lock settlement price %s != reported %s", prices[len(prices)-1], res.SettlementLockPrice)
	}
	if !res.ExpiresAt.After(time.Now()) {
		t.Fatal("ReserveHold: expiry is not in the future")
	}
	if got := e.registrySize(); got != 1 {
		t.Fatalf("registry size = %d, want 1", got)
	}
}

func TestReserveHold_HoldsFundsUntilRolledBack(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	first, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !first.Accepted {
		t.Fatalf("first ReserveHold: %v accepted=%v", err, first.Accepted)
	}
	second, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !second.Accepted {
		t.Fatalf("second ReserveHold: %v accepted=%v", err, second.Accepted)
	}
	third, err := e.ReserveHold(ctx, testOrder())
	if err != nil {
		t.Fatalf("third ReserveHold: %v", err)
	}
	if third.Accepted {
		t.Fatal("third ReserveHold accepted but funds should be exhausted")
	}

	if err := e.RollbackHeld(ctx, first.ApprovalID); err != nil {
		t.Fatalf("RollbackHeld: %v", err)
	}
	fourth, err := e.ReserveHold(ctx, testOrder())
	if err != nil {
		t.Fatalf("fourth ReserveHold: %v", err)
	}
	if !fourth.Accepted {
		t.Fatal("fourth ReserveHold rejected after rollback returned funds")
	}
}

func TestCommitHeld_SingleResolveGuard(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	res, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !res.Accepted {
		t.Fatalf("ReserveHold: %v accepted=%v", err, res.Accepted)
	}

	if err := e.CommitHeld(ctx, res.ApprovalID); err != nil {
		t.Fatalf("first CommitHeld: %v", err)
	}
	err = e.CommitHeld(ctx, res.ApprovalID)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second CommitHeld error = %v, want ErrConflict", err)
	}
	if got := e.registrySize(); got != 0 {
		t.Fatalf("registry size after commit = %d, want 0", got)
	}
}

func TestCommitHeld_ConcurrentResolveNoPanic(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	res, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !res.Accepted {
		t.Fatalf("ReserveHold: %v accepted=%v", err, res.Accepted)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = e.CommitHeld(ctx, res.ApprovalID) }()
	go func() { defer wg.Done(); errs[1] = e.RollbackHeld(ctx, res.ApprovalID) }()
	wg.Wait()

	if got := e.registrySize(); got != 0 {
		t.Fatalf("registry size after race = %d, want 0", got)
	}
}

func TestRollbackHeld_UnknownAndIdempotent(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	if err := e.RollbackHeld(ctx, "no-such-id"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("RollbackHeld(unknown) = %v, want ErrNotFound", err)
	}

	res, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !res.Accepted {
		t.Fatalf("ReserveHold: %v accepted=%v", err, res.Accepted)
	}
	if err := e.RollbackHeld(ctx, res.ApprovalID); err != nil {
		t.Fatalf("RollbackHeld: %v", err)
	}
	if err := e.RollbackHeld(ctx, res.ApprovalID); err != nil {
		t.Fatalf("idempotent RollbackHeld: %v", err)
	}
}

func TestSweepExpired_AutoRollsBackAndConfirmConflicts(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	res, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !res.Accepted {
		t.Fatalf("ReserveHold: %v accepted=%v", err, res.Accepted)
	}

	e.sweepExpired(ctx, res.ExpiresAt.Add(time.Second))

	if got := e.registrySize(); got != 0 {
		t.Fatalf("registry size after sweep = %d, want 0", got)
	}
	if err := e.CommitHeld(ctx, res.ApprovalID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("CommitHeld after sweep = %v, want ErrNotFound", err)
	}
	again, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !again.Accepted {
		t.Fatalf("ReserveHold after sweep: %v accepted=%v", err, again.Accepted)
	}
}

// TestSweepExpired_AtomicResolution proves the TTL sweeper resolves the durable
// state through the store in ONE atomic ResolveOrderReservation call: the intent
// flips to rolled_back, the order advances accepted->rolled_back, and a single
// reservation_rolled_back event is appended together.
func TestSweepExpired_AtomicResolution(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	orderXID := testOrderXID(99)
	store := &fakeReservationStore{
		intents: make(map[string]domain.ReservationIntent),
		// The held order starts accepted so the AllowedFrom={accepted} guard passes.
		status: map[domain.ExternalID]domain.OrderStatus{orderXID: domain.OrderStatusAccepted},
	}
	e.SetReservationStore(store)

	order := testOrder()
	order.ExternalID = orderXID
	res, err := e.ReserveHold(ctx, order)
	if err != nil || !res.Accepted {
		t.Fatalf("ReserveHold: %v accepted=%v", err, res.Accepted)
	}

	e.sweepExpired(ctx, res.ExpiresAt.Add(time.Second))

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.resolveCalls != 1 {
		t.Fatalf("ResolveOrderReservation calls = %d, want exactly 1 (atomic)", store.resolveCalls)
	}
	if got := store.intents[res.ApprovalID].State; got != domain.ReservationIntentStateRolledBack {
		t.Fatalf("intent state = %q, want rolled_back", got)
	}
	if got := store.status[orderXID]; got != domain.OrderStatusRolledBack {
		t.Fatalf("order status = %q, want rolled_back", got)
	}
	if len(store.events) != 1 || store.events[0].Type != domain.OrderEventReservationRolledBack {
		t.Fatalf("events = %+v, want one reservation_rolled_back", store.events)
	}
}

// recordingReservationStore wraps fakeReservationStore and records the names of
// every ReservationStore method the sweeper invokes. It locks in the structural
// fact that the TTL sweeper writes only through the ReservationStore and never
// re-enters node code.
type recordingReservationStore struct {
	fakeReservationStore
	calls []string
}

func (s *recordingReservationStore) record(name string) {
	s.mu.Lock()
	s.calls = append(s.calls, name)
	s.mu.Unlock()
}

func (s *recordingReservationStore) UpsertReservationIntent(
	ctx context.Context, intent domain.ReservationIntent,
) error {
	s.record("UpsertReservationIntent")
	return s.fakeReservationStore.UpsertReservationIntent(ctx, intent)
}

func (s *recordingReservationStore) ListOpenReservationIntents(
	ctx context.Context,
) ([]domain.ReservationIntent, error) {
	s.record("ListOpenReservationIntents")
	return s.fakeReservationStore.ListOpenReservationIntents(ctx)
}

func (s *recordingReservationStore) ResolveOrderReservation(
	ctx context.Context, r domain.ReservationResolution,
) error {
	s.record("ResolveOrderReservation")
	return s.fakeReservationStore.ResolveOrderReservation(ctx, r)
}

// TestSweepExpired_ConcurrentWithResolveNoDeadlockSingleResolution races the TTL
// sweeper against synchronous CommitHeld/RollbackHeld calls on a batch of held
// reservations.
func TestSweepExpired_ConcurrentWithResolveNoDeadlockSingleResolution(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	first, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !first.Accepted {
		t.Fatalf("first ReserveHold: %v accepted=%v", err, first.Accepted)
	}
	second, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !second.Accepted {
		t.Fatalf("second ReserveHold: %v accepted=%v", err, second.Accepted)
	}

	sweepAt := first.ExpiresAt.Add(time.Second)
	if second.ExpiresAt.Add(time.Second).After(sweepAt) {
		sweepAt = second.ExpiresAt.Add(time.Second)
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); e.sweepExpired(ctx, sweepAt) }()
	go func() { defer wg.Done(); _ = e.CommitHeld(ctx, first.ApprovalID) }()
	go func() { defer wg.Done(); _ = e.RollbackHeld(ctx, second.ApprovalID) }()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sweep raced with resolve deadlocked")
	}

	if got := e.registrySize(); got != 0 {
		t.Fatalf("registry size after race = %d, want 0", got)
	}
	if err := e.CommitHeld(ctx, first.ApprovalID); !errors.Is(err, domain.ErrNotFound) &&
		!errors.Is(err, domain.ErrConflict) {
		t.Fatalf("re-resolve first = %v, want ErrNotFound or ErrConflict", err)
	}
	if err := e.RollbackHeld(ctx, second.ApprovalID); err != nil &&
		!errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("re-resolve second = %v, want nil or ErrNotFound", err)
	}
}

// TestSweepExpired_WritesOnlyThroughReservationStore asserts the sweeper's
// auto-rollback touches state exclusively via the ReservationStore.
func TestSweepExpired_WritesOnlyThroughReservationStore(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	orderXID := testOrderXID(7)
	store := &recordingReservationStore{
		fakeReservationStore: fakeReservationStore{
			intents: make(map[string]domain.ReservationIntent),
			status:  map[domain.ExternalID]domain.OrderStatus{orderXID: domain.OrderStatusAccepted},
		},
	}
	e.SetReservationStore(store)

	order := testOrder()
	order.ExternalID = orderXID
	res, err := e.ReserveHold(ctx, order)
	if err != nil || !res.Accepted {
		t.Fatalf("ReserveHold: %v accepted=%v", err, res.Accepted)
	}

	e.sweepExpired(ctx, res.ExpiresAt.Add(time.Second))

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.calls) == 0 {
		t.Fatal("sweep recorded no ReservationStore writes")
	}
	allowed := map[string]bool{
		"UpsertReservationIntent":    true,
		"ListOpenReservationIntents": true,
		"ResolveOrderReservation":    true,
	}
	for _, name := range store.calls {
		if !allowed[name] {
			t.Fatalf("sweep invoked non-ReservationStore method %q", name)
		}
	}
}

// TestSweepExpired_RaceWithConfirmNoDoubleResolveNoClobber proves the sweeper's
// store resolve is TOCTOU-safe against a confirm that already committed the order
// durably.
func TestSweepExpired_RaceWithConfirmNoDoubleResolveNoClobber(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	orderXID := testOrderXID(42)
	store := &fakeReservationStore{
		intents: make(map[string]domain.ReservationIntent),
		// The order was already committed durably (a confirm won); the held intent
		// row still reads held until the winning resolution flips it.
		status: map[domain.ExternalID]domain.OrderStatus{orderXID: domain.OrderStatusCommitted},
	}
	e.SetReservationStore(store)

	order := testOrder()
	order.ExternalID = orderXID
	res, err := e.ReserveHold(ctx, order)
	if err != nil || !res.Accepted {
		t.Fatalf("ReserveHold: %v accepted=%v", err, res.Accepted)
	}
	store.mu.Lock()
	store.intents[res.ApprovalID] = domain.ReservationIntent{
		ApprovalID: res.ApprovalID,
		Order:      orderXID,
		State:      domain.ReservationIntentStateHeld,
	}
	store.mu.Unlock()

	e.sweepExpired(ctx, res.ExpiresAt.Add(time.Second))

	if got := e.registrySize(); got != 0 {
		t.Fatalf("registry size after sweep = %d, want 0 (entry drained)", got)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.resolveCalls != 1 {
		t.Fatalf("ResolveOrderReservation calls = %d, want exactly 1", store.resolveCalls)
	}
	if got := store.status[orderXID]; got != domain.OrderStatusCommitted {
		t.Fatalf("order status = %q, want committed preserved (no clobber)", got)
	}
	if got := store.intents[res.ApprovalID].State; got != domain.ReservationIntentStateHeld {
		t.Fatalf("intent state = %q, want held (conflict wrote nothing)", got)
	}
	if len(store.events) != 0 {
		t.Fatalf("events = %+v, want none (conflict wrote nothing)", store.events)
	}
}

func TestSubmitImmediate_NetsHeldToZero(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	res, err := e.SubmitImmediate(ctx, testOrder())
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("SubmitImmediate rejected: %+v", res.Rejects)
	}
	if res.EstimateSource != domain.EstimateSourceLimit {
		t.Fatalf("estimate source = %q, want %q", res.EstimateSource, domain.EstimateSourceLimit)
	}
	if res.SettlementLockPrice == "" {
		t.Fatal("SubmitImmediate: empty settlement lock price")
	}
	if len(res.Lock) == 0 {
		t.Fatal("SubmitImmediate: empty serialized lock")
	}
	if got := e.registrySize(); got != 0 {
		t.Fatalf("registry size after immediate = %d, want 0", got)
	}
}

// TestApplyExecutionReport_SettlesFillNoBlock drives a fill end to end through
// the real engine and the real executionReportFrom mapping: reserve a spot BUY
// (holding quote funds), commit it, then apply a final execution report carrying
// an explicit leaves quantity. It asserts the report settles with no account
// block and produces a per-asset outcome for each spot leg. This locks in that
// the mapper sets leaves quantity and the is-final flag; without them the engine
// rejects the fill with missing_required_field and blocks the account.
func TestApplyExecutionReport_SettlesFillNoBlock(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	held, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !held.Accepted {
		t.Fatalf("ReserveHold: %v accepted=%v", err, held.Accepted)
	}
	if err := e.CommitHeld(ctx, held.ApprovalID); err != nil {
		t.Fatalf("CommitHeld: %v", err)
	}

	// A full fill of the 5-unit order at the reservation's settlement lock price
	// nets the held quote to zero: leaves is 0 and the fill is final.
	result, err := e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		FillQuantity:   testQty,
		FillPrice:      held.SettlementLockPrice,
		LeavesQuantity: "0",
		LockPrice:      held.SettlementLockPrice,
		Account:        domain.AccountID(testAccount),
		Side:           domain.OrderSideBuy,
		Final:          true,
	})
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if len(result.Blocks) != 0 {
		t.Fatalf("fill must not block the account, got blocks=%+v", result.Blocks)
	}
	// Both spot legs settle: the base (AAPL) and the quote (USD).
	assets := map[string]bool{}
	for _, o := range result.Outcomes {
		assets[o.Asset] = true
	}
	if !assets[testBase] || !assets[testQuote] {
		t.Fatalf("want outcomes for %s and %s, got %+v", testBase, testQuote, result.Outcomes)
	}
}

// TestApplyExecutionReport_MissingLeavesRejected proves the mapper treats an
// empty leaves quantity as caller error: the report is rejected with
// domain.ErrInvalid before it reaches the engine, with no account block.
func TestApplyExecutionReport_MissingLeavesRejected(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	held, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !held.Accepted {
		t.Fatalf("ReserveHold: %v accepted=%v", err, held.Accepted)
	}
	if err := e.CommitHeld(ctx, held.ApprovalID); err != nil {
		t.Fatalf("CommitHeld: %v", err)
	}

	_, err = e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:    testBase,
		QuoteAsset:   testQuote,
		FillQuantity: testQty,
		FillPrice:    held.SettlementLockPrice,
		LockPrice:    held.SettlementLockPrice,
		Account:      domain.AccountID(testAccount),
		Side:         domain.OrderSideBuy,
		Final:        true,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyExecutionReport(empty leaves) = %v, want ErrInvalid", err)
	}
}

// newTestEngineWithPnlBounds builds the test engine with an asset-scope P&L-
// bounds kill-switch active, so a fill exercises that policy's post-trade
// reading of the execution report's financial-impact group.
func newTestEngineWithPnlBounds(t *testing.T) *openPitEngine {
	t.Helper()
	snap := Snapshot{
		Accounts: []domain.Account{account(testAccount)},
		PnlBoundsLimits: []domain.LimitPnlBounds{{
			Scope:      domain.ScopeAsset,
			Asset:      testQuote,
			LowerBound: "-1000000",
		}},
	}
	res, err := newIDResolver(snap.Accounts, snap.Groups)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	eng, service, registered, err := buildEngine(snap, res)
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	adapter := newOpenPitEngine(eng, service, registered, nil, res).(*openPitEngine)
	t.Cleanup(adapter.Stop)

	if err := seedBalances(eng, []domain.Balance{{
		Account:   domain.AccountID(testAccount),
		Asset:     testQuote,
		Available: testQuoteFund,
	}}, res); err != nil {
		t.Fatalf("seed balance: %v", err)
	}
	return adapter
}

// TestApplyExecutionReport_SettlesFillWithPnlBoundsNoBlock proves a fill settles
// without a spurious account block when a P&L-bounds kill-switch is active. The
// mapper sets the financial-impact group (P&L and fee) the policy reads, so an
// absent group no longer trips missing_required_field. Spot-funds alone settled
// the fill (it ignores the group); only the P&L policy exposed the gap.
func TestApplyExecutionReport_SettlesFillWithPnlBoundsNoBlock(t *testing.T) {
	e := newTestEngineWithPnlBounds(t)
	ctx := context.Background()

	held, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !held.Accepted {
		t.Fatalf("ReserveHold: %v accepted=%v", err, held.Accepted)
	}
	if err := e.CommitHeld(ctx, held.ApprovalID); err != nil {
		t.Fatalf("CommitHeld: %v", err)
	}

	// RealizedPnl/Fee left empty (→ zero): a position-opening buy realizes nothing.
	result, err := e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		FillQuantity:   testQty,
		FillPrice:      held.SettlementLockPrice,
		LeavesQuantity: "0",
		LockPrice:      held.SettlementLockPrice,
		Account:        domain.AccountID(testAccount),
		Side:           domain.OrderSideBuy,
		Final:          true,
	})
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if len(result.Blocks) != 0 {
		t.Fatalf("fill must not block with pnl_bounds active, got blocks=%+v", result.Blocks)
	}
	if len(result.Outcomes) == 0 {
		t.Fatalf("want settlement outcomes, got none")
	}
}

// TestSubmitOrder_AcceptCapturesSerializedLock checks SubmitOrder returns the
// SDK-serialized lock (not decimal prices) and that the lock round-trips through
// the seam to the prices the order locked.
func TestSubmitOrder_AcceptCapturesSerializedLock(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	res, err := e.SubmitOrder(ctx, testOrder())
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("SubmitOrder rejected: %+v", res.Rejects)
	}
	if len(res.Lock) == 0 {
		t.Fatal("SubmitOrder: empty serialized lock on accept")
	}
	prices, err := LockDisplayPrices(res.Lock)
	if err != nil {
		t.Fatalf("LockDisplayPrices: %v", err)
	}
	if len(prices) == 0 {
		t.Fatal("serialized lock carries no prices")
	}
}

func TestResolvedReservationsArePruned(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	res, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !res.Accepted {
		t.Fatalf("ReserveHold: %v accepted=%v", err, res.Accepted)
	}
	if err := e.CommitHeld(ctx, res.ApprovalID); err != nil {
		t.Fatalf("CommitHeld: %v", err)
	}
	e.registry.mu.Lock()
	if len(e.registry.resolved) != 1 {
		t.Fatalf("resolved len after commit = %d, want 1", len(e.registry.resolved))
	}
	for id, resolved := range e.registry.resolved {
		resolved.expiresAt = time.Now().UTC().Add(-time.Second)
		e.registry.resolved[id] = resolved
	}
	e.registry.mu.Unlock()

	e.sweepExpired(ctx, time.Now().UTC())
	e.registry.mu.Lock()
	defer e.registry.mu.Unlock()
	if len(e.registry.resolved) != 0 {
		t.Fatalf("resolved len after prune = %d, want 0", len(e.registry.resolved))
	}
}

func TestStop_DrainsHeldReservations(t *testing.T) {
	snap := Snapshot{Accounts: []domain.Account{account(testAccount)}}
	res, err := newIDResolver(snap.Accounts, snap.Groups)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	eng, service, registered, err := buildEngine(snap, res)
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	adapter := newOpenPitEngine(eng, service, registered, nil, res).(*openPitEngine)
	if err := seedBalances(eng, []domain.Balance{{
		Account:   domain.AccountID(testAccount),
		Asset:     testQuote,
		Available: testQuoteFund,
	}}, res); err != nil {
		adapter.Stop()
		t.Fatalf("seed balance: %v", err)
	}

	ctx := context.Background()
	held, err := adapter.ReserveHold(ctx, testOrder())
	if err != nil || !held.Accepted {
		adapter.Stop()
		t.Fatalf("ReserveHold: %v accepted=%v", err, held.Accepted)
	}

	adapter.Stop()
	if got := adapter.registrySize(); got != 0 {
		t.Fatalf("registry size after Stop = %d, want 0", got)
	}
	adapter.Stop()
}

func TestReconcileOrphans_PreservesHeldIntents(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	store := &fakeReservationStore{
		intents: map[string]domain.ReservationIntent{
			"orphan-1": {ApprovalID: "orphan-1", State: domain.ReservationIntentStateHeld},
			"orphan-2": {ApprovalID: "orphan-2", State: domain.ReservationIntentStateHeld},
		},
	}
	e.SetReservationStore(store)

	n, err := e.ReconcileOrphans(ctx)
	if err != nil {
		t.Fatalf("ReconcileOrphans: %v", err)
	}
	if n != 2 {
		t.Fatalf("held intents = %d, want 2", n)
	}
	for id, intent := range store.intents {
		if intent.State != domain.ReservationIntentStateHeld {
			t.Fatalf("intent %q state = %q, want held", id, intent.State)
		}
	}
}

func TestImmediateExecutionReport_VolumeSizing(t *testing.T) {
	res := testResolver(testAccount)

	// A volume order with no settlement price cannot be sized into a fill.
	_, err := immediateExecutionReport(domain.Order{
		Account:     domain.AccountID(testAccount),
		BaseAsset:   testBase,
		QuoteAsset:  testQuote,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindVolume,
		AmountValue: "500",
	}, "", res)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("immediateExecutionReport(volume, no price) = %v, want ErrInvalid", err)
	}

	// With a settlement price the volume sizes to base quantity (500/100 = 5).
	if _, err := immediateExecutionReport(domain.Order{
		Account:     domain.AccountID(testAccount),
		BaseAsset:   testBase,
		QuoteAsset:  testQuote,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindVolume,
		AmountValue: "500",
	}, "100", res); err != nil {
		t.Fatalf("immediateExecutionReport(volume): %v", err)
	}

	qty, err := immediateFillQuantity(domain.Order{
		AmountKind:  domain.OrderAmountKindVolume,
		AmountValue: "500",
	}, "100")
	if err != nil {
		t.Fatalf("immediateFillQuantity: %v", err)
	}
	got, err := param.NewQuantityFromString(qty)
	if err != nil {
		t.Fatalf("parse fill quantity %q: %v", qty, err)
	}
	want, _ := param.NewQuantityFromString("5")
	if got.Compare(want) != 0 {
		t.Fatalf("immediateFillQuantity = %q, want 5", qty)
	}
}

// fakeReservationStore is an in-memory ReservationStore for reconcile and
// sweeper tests. It applies ResolveOrderReservation atomically against its own
// maps keyed by the order's opaque external id: a single call flips the intent,
// advances the order status, and records the events together. The status
// WHERE-guard is honoured so a sweeper resolve that races a committed order
// yields domain.ErrConflict and writes nothing, exactly like the real store.
type fakeReservationStore struct {
	mu      sync.Mutex
	intents map[string]domain.ReservationIntent
	events  []domain.OrderEvent
	status  map[domain.ExternalID]domain.OrderStatus
	// resolveCalls counts ResolveOrderReservation invocations - the single atomic
	// entry point the sweeper/confirm/cancel paths use.
	resolveCalls int
}

func (s *fakeReservationStore) UpsertReservationIntent(
	_ context.Context, intent domain.ReservationIntent,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.intents[intent.ApprovalID] = intent
	return nil
}

func (s *fakeReservationStore) ListOpenReservationIntents(
	_ context.Context,
) ([]domain.ReservationIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.ReservationIntent, 0, len(s.intents))
	for _, intent := range s.intents {
		if intent.State == domain.ReservationIntentStateHeld {
			out = append(out, intent)
		}
	}
	return out, nil
}

// ResolveOrderReservation applies one resolution atomically: it enforces the
// AllowedFrom status guard first (no writes on conflict), then flips the intent,
// advances the order status, and appends the events. A zero Order skips the
// order/event writes and only flips the intent; a missing intent row is tolerated.
func (s *fakeReservationStore) ResolveOrderReservation(
	_ context.Context, r domain.ReservationResolution,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolveCalls++
	if s.status == nil {
		s.status = make(map[domain.ExternalID]domain.OrderStatus)
	}

	if !r.Order.IsZero() && len(r.AllowedFrom) > 0 {
		cur, known := s.status[r.Order]
		if known {
			allowed := false
			for _, a := range r.AllowedFrom {
				if cur == a {
					allowed = true
					break
				}
			}
			if !allowed {
				return domain.ErrConflict
			}
		}
	}

	if intent, ok := s.intents[r.ApprovalID]; ok {
		intent.State = r.IntentState
		s.intents[r.ApprovalID] = intent
	}

	if !r.Order.IsZero() {
		s.status[r.Order] = r.OrderStatus
		s.events = append(s.events, r.Events...)
	}
	return nil
}
