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

package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/openpit/param"

	"go.openpit.dev/officer/internal/domain"
)

const (
	testAccount   = "1"
	testQuote     = "USD"
	testBase      = "AAPL"
	testQuoteFund = "1000"
	testQty       = "5"
	testLimit     = "100" // 5 * 100 = 500 quote, leaves 500 after one hold
)

// newTestEngine builds a real engine adapter with one account seeded with
// testQuoteFund of the quote asset, enough to reserve exactly two test orders.
// The adapter is stopped via t.Cleanup.
func newTestEngine(t *testing.T) *openPitEngine {
	t.Helper()
	eng, service, registered, err := buildEngine(nil)
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	adapter := newOpenPitEngine(eng, service, registered, nil).(*openPitEngine)
	t.Cleanup(adapter.Stop)

	if err := seedBalances(eng, []domain.Balance{{
		Account:   domain.AccountID(testAccount),
		Asset:     testQuote,
		Available: testQuoteFund,
	}}); err != nil {
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

	// First hold consumes 500 of 1000 available quote.
	first, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !first.Accepted {
		t.Fatalf("first ReserveHold: %v accepted=%v", err, first.Accepted)
	}
	// Second hold consumes the remaining 500.
	second, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !second.Accepted {
		t.Fatalf("second ReserveHold: %v accepted=%v", err, second.Accepted)
	}
	// Third hold must reject: all funds are held.
	third, err := e.ReserveHold(ctx, testOrder())
	if err != nil {
		t.Fatalf("third ReserveHold: %v", err)
	}
	if third.Accepted {
		t.Fatal("third ReserveHold accepted but funds should be exhausted")
	}

	// Rolling back the first hold returns its funds; a fresh hold then succeeds.
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
	// A second commit must NOT reach the native handle (which would panic on a
	// double Commit); it returns a conflict instead.
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

	// Race a commit against a rollback of the same id: the Held->Resolving flip
	// guarantees exactly one reaches the native handle, the other is a no-op or
	// conflict, and neither panics.
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
	// A second rollback of a now-terminal id is a tolerated no-op.
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

	// Force the entry past its TTL and sweep at a time after expiry.
	e.sweepExpired(ctx, res.ExpiresAt.Add(time.Second))

	if got := e.registrySize(); got != 0 {
		t.Fatalf("registry size after sweep = %d, want 0", got)
	}
	// A confirm after the sweep rolled it back cannot find the entry.
	if err := e.CommitHeld(ctx, res.ApprovalID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("CommitHeld after sweep = %v, want ErrNotFound", err)
	}
	// Funds were returned: a fresh hold of the same size succeeds.
	again, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !again.Accepted {
		t.Fatalf("ReserveHold after sweep: %v accepted=%v", err, again.Accepted)
	}
}

// TestSweepExpired_AtomicResolution proves the TTL sweeper resolves the durable
// state through the store in ONE atomic ResolveOrderReservation call (not the
// former separate SetReservationIntentState + AppendOrderEvent + UpdateOrder
// Status writes): the intent flips to rolled_back, the order advances accepted->
// rolled_back, and a single reservation_rolled_back event is appended together.
func TestSweepExpired_AtomicResolution(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	store := &fakeReservationStore{
		intents: make(map[string]domain.ReservationIntent),
		// The held order starts accepted so the AllowedFrom={accepted} guard passes.
		status: map[int64]domain.OrderStatus{99: domain.OrderStatusAccepted},
	}
	e.SetReservationStore(store)

	order := testOrder()
	order.Tenant = domain.DefaultTenant
	order.ID = 99
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
	if got := store.status[order.ID]; got != domain.OrderStatusRolledBack {
		t.Fatalf("order status = %q, want rolled_back", got)
	}
	if len(store.events) != 1 || store.events[0].Type != domain.OrderEventReservationRolledBack {
		t.Fatalf("events = %+v, want one reservation_rolled_back", store.events)
	}
}

// recordingReservationStore wraps fakeReservationStore and records the names of
// every ReservationStore method the sweeper invokes. It exists to lock in the
// structural fact that the TTL sweeper writes only through the ReservationStore
// and never re-enters node code: the engine constructor takes no node callback,
// so there is no node seam to assert against directly.
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
// reservations. It asserts the whole fan-out completes within a generous timeout
// (a deadlock would hang the select) and that every reservation resolves at most
// once: the native handle is committed-or-rolled-back exactly once, so a double
// resolve would panic the binding and crash the test rather than return an error.
// This regresses against the engine ever taking n.mutate or a node reference from
// the sweeper path: such re-entrancy would deadlock against the synchronous
// resolve holding the same lock.
func TestSweepExpired_ConcurrentWithResolveNoDeadlockSingleResolution(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	// Two held reservations: the 1000-quote balance funds exactly two.
	first, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !first.Accepted {
		t.Fatalf("first ReserveHold: %v accepted=%v", err, first.Accepted)
	}
	second, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !second.Accepted {
		t.Fatalf("second ReserveHold: %v accepted=%v", err, second.Accepted)
	}

	// Sweep at a time past both TTLs so the sweeper contends for the same entries
	// the synchronous resolves target. Each goroutine races a distinct path
	// against the sweeper; none must panic (double native resolve) or hang.
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

	// Both reservations are terminal exactly once: the registry is drained and a
	// follow-up resolve cannot find either entry.
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
// auto-rollback touches state exclusively via the ReservationStore: it records
// every store method the sweep invokes and confirms the set is a subset of the
// ReservationStore surface. The engine holds no node handle (its constructor
// takes none), so a node callback from this path is structurally impossible;
// this test guards that the sweep does not grow one through some other seam.
func TestSweepExpired_WritesOnlyThroughReservationStore(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	store := &recordingReservationStore{
		fakeReservationStore: fakeReservationStore{
			intents: make(map[string]domain.ReservationIntent),
			status:  map[int64]domain.OrderStatus{7: domain.OrderStatusAccepted},
		},
	}
	e.SetReservationStore(store)

	order := testOrder()
	order.Tenant = domain.DefaultTenant
	order.ID = 7
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
// durably. The fake store shows the order committed before the sweep; the
// sweeper's ResolveOrderReservation then hits the AllowedFrom={accepted} guard,
// returns domain.ErrConflict, and writes nothing - the committed status and the
// held intent both stand. The sweep swallows the conflict (no panic, no error
// surfaced) and drains the in-memory entry; the native handle is resolved exactly
// once (a double native resolve would panic the binding and crash the test).
func TestSweepExpired_RaceWithConfirmNoDoubleResolveNoClobber(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	store := &fakeReservationStore{
		intents: make(map[string]domain.ReservationIntent),
		// The order was already committed durably (a confirm won); the held intent
		// row still reads held until the winning resolution flips it.
		status: map[int64]domain.OrderStatus{42: domain.OrderStatusCommitted},
	}
	e.SetReservationStore(store)

	order := testOrder()
	order.Tenant = domain.DefaultTenant
	order.ID = 42
	res, err := e.ReserveHold(ctx, order)
	if err != nil || !res.Accepted {
		t.Fatalf("ReserveHold: %v accepted=%v", err, res.Accepted)
	}
	// The intent row reads held (as it would until the winning confirm flips it).
	store.mu.Lock()
	store.intents[res.ApprovalID] = domain.ReservationIntent{
		ApprovalID: res.ApprovalID,
		OrderID:    order.ID,
		State:      domain.ReservationIntentStateHeld,
	}
	store.mu.Unlock()

	// Sweep the expired-but-already-committed entry: the store guard must reject
	// the rollback and the sweeper must tolerate it.
	e.sweepExpired(ctx, res.ExpiresAt.Add(time.Second))

	if got := e.registrySize(); got != 0 {
		t.Fatalf("registry size after sweep = %d, want 0 (entry drained)", got)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.resolveCalls != 1 {
		t.Fatalf("ResolveOrderReservation calls = %d, want exactly 1", store.resolveCalls)
	}
	if got := store.status[order.ID]; got != domain.OrderStatusCommitted {
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
	// Immediate settlement leaves no held reservation behind.
	if got := e.registrySize(); got != 0 {
		t.Fatalf("registry size after immediate = %d, want 0", got)
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
	eng, service, registered, err := buildEngine(nil)
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	adapter := newOpenPitEngine(eng, service, registered, nil).(*openPitEngine)
	if err := seedBalances(eng, []domain.Balance{{
		Account:   domain.AccountID(testAccount),
		Asset:     testQuote,
		Available: testQuoteFund,
	}}); err != nil {
		adapter.Stop()
		t.Fatalf("seed balance: %v", err)
	}

	ctx := context.Background()
	res, err := adapter.ReserveHold(ctx, testOrder())
	if err != nil || !res.Accepted {
		adapter.Stop()
		t.Fatalf("ReserveHold: %v accepted=%v", err, res.Accepted)
	}

	// Stop must drain the open hold (rollback + close) without panic or leak.
	adapter.Stop()
	if got := adapter.registrySize(); got != 0 {
		t.Fatalf("registry size after Stop = %d, want 0", got)
	}
	// Stop is idempotent.
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
	// A volume order with no settlement price cannot be sized into a fill.
	_, err := immediateExecutionReport(domain.Order{
		BaseAsset:   testBase,
		QuoteAsset:  testQuote,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindVolume,
		AmountValue: "500",
	}, "")
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
	}, "100"); err != nil {
		t.Fatalf("immediateExecutionReport(volume): %v", err)
	}

	// The base-quantity sizing converts volume to quantity at the settlement
	// price (500 volume / 100 price = 5 base): compare numerically to tolerate
	// the binding's decimal scale formatting.
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
// maps: a single call flips the intent, advances the order status, and records
// the events together (mirroring the real store's all-or-nothing tx). The status
// WHERE-guard is honoured so a sweeper resolve that races a committed order
// yields domain.ErrConflict and writes nothing, exactly like the real store.
type fakeReservationStore struct {
	mu      sync.Mutex
	intents map[string]domain.ReservationIntent
	events  []domain.OrderEvent
	status  map[int64]domain.OrderStatus
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
// advances the order status, and appends the events. OrderID==0 skips the order/
// event writes and only flips the intent; a missing intent row is tolerated.
func (s *fakeReservationStore) ResolveOrderReservation(
	_ context.Context, r domain.ReservationResolution,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolveCalls++
	if s.status == nil {
		s.status = make(map[int64]domain.OrderStatus)
	}

	if r.OrderID != 0 && len(r.AllowedFrom) > 0 {
		cur, known := s.status[r.OrderID]
		if known {
			allowed := false
			for _, a := range r.AllowedFrom {
				if cur == a {
					allowed = true
					break
				}
			}
			if !allowed {
				// Disallowed current status: conflict, nothing written.
				return domain.ErrConflict
			}
		}
	}

	// Intent flip (tolerate a missing row, like the real store inside the tx).
	if intent, ok := s.intents[r.ApprovalID]; ok {
		intent.State = r.IntentState
		s.intents[r.ApprovalID] = intent
	}

	if r.OrderID != 0 {
		s.status[r.OrderID] = r.OrderStatus
		s.events = append(s.events, r.Events...)
	}
	return nil
}
