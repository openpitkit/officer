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
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func seedHeldOrder(t *testing.T, st store.RealmStore, account domain.AccountID, approvalID string) domain.Order {
	t.Helper()
	ctx := context.Background()
	seedTestAccount(t, st, account)
	order, err := st.CreateOrder(ctx, domain.Order{
		Account:     account,
		Source:      domain.SourceAPI,
		Principal:   "operator",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       "100",
		Status:      domain.OrderStatusAccepted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if err := st.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID: approvalID,
		Order:      order.ExternalID,
		Account:    order.Account,
		ParamsJSON: "{}",
		IssuedAt:   time.Now().UTC(),
		State:      domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}
	return order
}

func eventTypes(t *testing.T, st store.RealmStore, order domain.ExternalID) []domain.OrderEventType {
	t.Helper()
	evs, err := st.ListOrderEvents(context.Background(), order)
	if err != nil {
		t.Fatalf("ListOrderEvents: %v", err)
	}
	types := make([]domain.OrderEventType, len(evs))
	for i, ev := range evs {
		types[i] = ev.Type
	}
	return types
}

func intentStillHeld(t *testing.T, st store.RealmStore, approvalID string) bool {
	t.Helper()
	open, err := st.ListOpenReservationIntents(context.Background())
	if err != nil {
		t.Fatalf("ListOpenReservationIntents: %v", err)
	}
	for _, i := range open {
		if i.ApprovalID == approvalID {
			return true
		}
	}
	return false
}

// TestConfirmHeld_AtomicSingleResolve proves the confirm path advances the order
// to committed with exactly one reservation_committed event, flips the intent,
// and resolves the native handle exactly once.
func TestConfirmHeld_AtomicSingleResolve(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")

	confirmed, _, err := n.ConfirmHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if err != nil {
		t.Fatalf("ConfirmHeld: %v", err)
	}
	if confirmed.Status != domain.OrderStatusCommitted {
		t.Fatalf("status = %q, want committed", confirmed.Status)
	}
	if intentStillHeld(t, st, "approval-1") {
		t.Fatal("intent still held, want committed")
	}
	types := eventTypes(t, st, order.ExternalID)
	if len(types) != 1 || types[0] != domain.OrderEventReservationCommitted {
		t.Fatalf("events = %+v, want exactly [reservation_committed]", types)
	}
	eng.resolveMu.Lock()
	commits := append([]string(nil), eng.commitHeldCalls...)
	eng.resolveMu.Unlock()
	if len(commits) != 1 || commits[0] != "approval-1" {
		t.Fatalf("commit calls = %+v, want exactly [approval-1]", commits)
	}
}

// TestConfirmHeld_StoreFailureNoPartialAndSingleNativeResolve proves that when
// the atomic ResolveOrderReservation fails after the native CommitHeld already
// succeeded, ConfirmHeld returns an error, leaves the order accepted and the
// intent held (nothing persisted), and does NOT re-resolve the native handle -
// finishResolve already ran inside the single CommitHeld.
func TestConfirmHeld_StoreFailureNoPartialAndSingleNativeResolve(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()

	real := newMemoryStore("node.db")
	ctx := context.Background()
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })

	resolveErr := errors.New("resolve boom")
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failResolveRealm{RealmStore: r, err: resolveErr}
	})
	n := newTestNodeWithStore(t, st, eng)

	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	order := seedHeldOrder(t, realm, "acc-1", "approval-1")

	_, _, err = n.ConfirmHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if err == nil {
		t.Fatal("ConfirmHeld: want error from failing ResolveOrderReservation")
	}
	if !errors.Is(err, resolveErr) {
		t.Fatalf("ConfirmHeld error = %v, want wrapping resolve boom", err)
	}

	// No partial persistence: status accepted, intent held, no events (read the
	// underlying real store, not the failing wrapper).
	detail, err := realm.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusAccepted {
		t.Fatalf("status = %q, want accepted (nothing persisted)", detail.Order.Status)
	}
	if !intentStillHeld(t, realm, "approval-1") {
		t.Fatal("intent flipped despite store failure, want still held")
	}
	if len(detail.Events) != 0 {
		t.Fatalf("events = %+v, want none", detail.Events)
	}
	// Native handle resolved exactly once: the store failure must not trigger a
	// second CommitHeld.
	eng.resolveMu.Lock()
	commits := append([]string(nil), eng.commitHeldCalls...)
	eng.resolveMu.Unlock()
	if len(commits) != 1 {
		t.Fatalf("commit calls = %+v, want exactly one native resolve", commits)
	}
}

// TestConfirmHeld_PostEngineStoreFailureFatals proves the reservation-resolve
// write is a post-engine persistence step: the native CommitHeld already
// succeeded, so a failing atomic ResolveOrderReservation must fail-stop with the
// "resolve held confirmation" operation label, the real account id, and the
// wrapped cause.
func TestConfirmHeld_PostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()

	real := newMemoryStore("node.db")
	ctx := context.Background()
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })

	resolveErr := errors.New("resolve confirm boom")
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failResolveRealm{RealmStore: r, err: resolveErr}
	})
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))

	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	order := seedHeldOrder(t, realm, "acc-1", "approval-1")

	_, _, err = n.ConfirmHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if !errors.Is(err, resolveErr) {
		t.Fatalf("ConfirmHeld error = %v, want store failure", err)
	}
	// Native handle resolved before the store write failed.
	eng.resolveMu.Lock()
	commits := append([]string(nil), eng.commitHeldCalls...)
	eng.resolveMu.Unlock()
	if len(commits) != 1 {
		t.Fatalf("commit calls = %+v, want exactly one native resolve", commits)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine confirm persistence failure")
	}
	account, ok, err := realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="resolve held confirmation"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "resolve confirm boom") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

// TestCancelHeld_PostEngineStoreFailureFatals mirrors the confirm fatal test for
// the cancel path: the native RollbackHeld already succeeded, so a failing
// atomic ResolveOrderReservation must fail-stop with the "resolve held
// cancellation" operation label, the real account id, and the wrapped cause.
func TestCancelHeld_PostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()

	real := newMemoryStore("node.db")
	ctx := context.Background()
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })

	resolveErr := errors.New("resolve cancel boom")
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failResolveRealm{RealmStore: r, err: resolveErr}
	})
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))

	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	order := seedHeldOrder(t, realm, "acc-1", "approval-1")

	_, _, err = n.CancelHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if !errors.Is(err, resolveErr) {
		t.Fatalf("CancelHeld error = %v, want store failure", err)
	}
	// Native handle rolled back before the store write failed.
	eng.resolveMu.Lock()
	rollbacks := append([]string(nil), eng.rollbackHeldCalls...)
	eng.resolveMu.Unlock()
	if len(rollbacks) != 1 {
		t.Fatalf("rollback calls = %+v, want exactly one native resolve", rollbacks)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine cancel persistence failure")
	}
	account, ok, err := realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="resolve held cancellation"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "resolve cancel boom") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

// TestCancelHeld_AtomicConsistency proves the cancel path advances the order to
// cancelled with both reservation_rolled_back and cancelled events and flips the
// intent - all together.
func TestCancelHeld_AtomicConsistency(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")

	cancelled, _, err := n.CancelHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if err != nil {
		t.Fatalf("CancelHeld: %v", err)
	}
	if cancelled.Status != domain.OrderStatusCancelled {
		t.Fatalf("status = %q, want cancelled", cancelled.Status)
	}
	if intentStillHeld(t, st, "approval-1") {
		t.Fatal("intent still held, want rolled_back")
	}
	types := eventTypes(t, st, order.ExternalID)
	if len(types) != 2 ||
		types[0] != domain.OrderEventReservationRolledBack ||
		types[1] != domain.OrderEventCancelled {
		t.Fatalf("events = %+v, want [reservation_rolled_back cancelled]", types)
	}
}

// TestConfirmHeld_AfterTerminalIntentReturnsConflict proves that a ConfirmHeld
// whose native handle is gone (post-restart) and whose intent is already in a
// terminal state (resolved before the restart) returns ErrConflict (409), not
// ErrNotFound (404). A genuinely unknown approval id still returns ErrNotFound.
func TestConfirmHeld_AfterTerminalIntentReturnsConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	// Native handle is gone: CommitHeld reports ErrNotFound.
	eng.commitErr = fmt.Errorf("engine: reservation %q: %w", "approval-resolved", domain.ErrNotFound)

	n, st := newTestNode(t, eng)
	order := seedHeldOrder(t, st, "acc-1", "approval-resolved")

	// Intent already resolved before the restart: flip it to rolled_back.
	if err := st.SetReservationIntentState(
		ctx, "approval-resolved", domain.ReservationIntentStateRolledBack,
	); err != nil {
		t.Fatalf("SetReservationIntentState: %v", err)
	}

	// Confirm of an already-resolved intent must return ErrConflict.
	_, _, err := n.ConfirmHeld(ctx, order.ExternalID, "approval-resolved", testCaller, false)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("ConfirmHeld after terminal intent: want ErrConflict, got %v", err)
	}

	// A genuinely unknown approval id must still return ErrNotFound.
	_, _, err = n.ConfirmHeld(ctx, order.ExternalID, "approval-unknown", testCaller, false)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ConfirmHeld unknown: want ErrNotFound, got %v", err)
	}
}

// TestCancelHeld_AfterTerminalIntentReturnsConflict proves that a CancelHeld
// whose native handle is gone (post-restart) and whose intent is already in a
// terminal state returns ErrConflict (409), not ErrNotFound (404).
func TestCancelHeld_AfterTerminalIntentReturnsConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	// Native handle is gone: RollbackHeld reports ErrNotFound.
	eng.rollbackErr = fmt.Errorf("engine: reservation %q: %w", "approval-resolved", domain.ErrNotFound)

	n, st := newTestNode(t, eng)
	order := seedHeldOrder(t, st, "acc-1", "approval-resolved")

	// Intent already resolved before the restart: flip it to rolled_back.
	if err := st.SetReservationIntentState(
		ctx, "approval-resolved", domain.ReservationIntentStateRolledBack,
	); err != nil {
		t.Fatalf("SetReservationIntentState: %v", err)
	}

	// Cancel of an already-resolved intent must return ErrConflict.
	_, _, err := n.CancelHeld(ctx, order.ExternalID, "approval-resolved", testCaller, false)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CancelHeld after terminal intent: want ErrConflict, got %v", err)
	}

	// A genuinely unknown approval id must still return ErrNotFound.
	_, _, err = n.CancelHeld(ctx, order.ExternalID, "approval-unknown", testCaller, false)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("CancelHeld unknown: want ErrNotFound, got %v", err)
	}
}

// TestCancelHeld_AfterFillConflictPreservesFilled proves a cancel that races a
// fill is rejected: the order was flipped to filled (via a direct settlement),
// so the cancel's AllowedFrom={accepted} guard returns ErrConflict, the filled
// status stands, and no cancel events are written.
func TestCancelHeld_AfterFillConflictPreservesFilled(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")

	// Simulate a fill landing before the cancel: settle the order to filled
	// directly through the store (the same atomic method the fill path uses).
	if err := st.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Account:     "acc-1",
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusFilled,
		Events: []domain.OrderEvent{{
			Order: order.ExternalID, Type: domain.OrderEventFill, Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement (simulated fill): %v", err)
	}

	_, _, err := n.CancelHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if !errors.Is(err, domain.ErrTerminalOrder) {
		t.Fatalf("CancelHeld after fill: want ErrTerminalOrder, got %v", err)
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("status = %q, want filled preserved", detail.Order.Status)
	}
	types := eventTypes(t, st, order.ExternalID)
	if len(types) != 1 || types[0] != domain.OrderEventFill {
		t.Fatalf("events = %+v, want only the fill (no cancel events)", types)
	}
}

// TestConfirmHeld_AfterFillConflictPreservesFilled proves a confirm without
// force on an order a fill already moved to filled is rejected with
// ErrTerminalOrder, the filled status stands, and no confirm events are written.
// It mirrors TestCancelHeld_AfterFillConflictPreservesFilled for the confirm path.
func TestConfirmHeld_AfterFillConflictPreservesFilled(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")

	if err := st.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Account:     "acc-1",
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusFilled,
		Events: []domain.OrderEvent{{
			Order: order.ExternalID, Type: domain.OrderEventFill, Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement (simulated fill): %v", err)
	}

	_, forcedBypass, err := n.ConfirmHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if !errors.Is(err, domain.ErrTerminalOrder) {
		t.Fatalf("ConfirmHeld after fill without force: want ErrTerminalOrder, got %v", err)
	}
	if forcedBypass {
		t.Fatal("forcedBypass = true on a rejected confirm, want false")
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("status = %q, want filled preserved", detail.Order.Status)
	}
	types := eventTypes(t, st, order.ExternalID)
	if len(types) != 1 || types[0] != domain.OrderEventFill {
		t.Fatalf("events = %+v, want only the fill (no confirm events)", types)
	}
}

func TestConfirmHeld_AfterFillForceCommits(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")
	if err := st.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Account:     "acc-1",
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusFilled,
		Events: []domain.OrderEvent{{
			Order: order.ExternalID, Type: domain.OrderEventFill, Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement (simulated fill): %v", err)
	}

	committed, forcedBypass, err := n.ConfirmHeld(ctx, order.ExternalID, "approval-1", testCaller, true)
	if err != nil {
		t.Fatalf("ConfirmHeld force: %v", err)
	}
	if !forcedBypass {
		t.Fatal("ConfirmHeld force over a filled order: forcedBypass = false, want true")
	}
	if committed.Status != domain.OrderStatusCommitted {
		t.Fatalf("status = %q, want committed", committed.Status)
	}
}

func TestCancelHeld_AfterFillForceCancels(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")
	if err := st.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Account:     "acc-1",
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusFilled,
		Events: []domain.OrderEvent{{
			Order: order.ExternalID, Type: domain.OrderEventFill, Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement (simulated fill): %v", err)
	}

	cancelled, forcedBypass, err := n.CancelHeld(ctx, order.ExternalID, "approval-1", testCaller, true)
	if err != nil {
		t.Fatalf("CancelHeld force: %v", err)
	}
	if !forcedBypass {
		t.Fatal("CancelHeld force over a filled order: forcedBypass = false, want true")
	}
	if cancelled.Status != domain.OrderStatusCancelled {
		t.Fatalf("status = %q, want cancelled", cancelled.Status)
	}
}

func TestConfirmHeld_ForcePartiallyFilledRoutesEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")
	if err := st.UpdateOrderStatus(
		ctx, order.ExternalID, domain.OrderStatusPartiallyFilled,
	); err != nil {
		t.Fatalf("UpdateOrderStatus: %v", err)
	}

	confirmed, forcedBypass, err := n.ConfirmHeld(
		ctx, order.ExternalID, "approval-1", testCaller, true,
	)
	if err != nil {
		t.Fatalf("ConfirmHeld force partially_filled: %v", err)
	}
	if forcedBypass {
		t.Fatal("forcedBypass = true, want false for non-terminal status")
	}
	if confirmed.Status != domain.OrderStatusCommitted {
		t.Fatalf("status = %q, want committed", confirmed.Status)
	}
	if len(eng.commitHeldCalls) != 1 {
		t.Fatalf("commit calls = %+v, want one", eng.commitHeldCalls)
	}
}

func TestCancelHeld_ForceCommittedRoutesEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := seedHeldOrder(t, st, "acc-1", "approval-1")
	if err := st.UpdateOrderStatus(
		ctx, order.ExternalID, domain.OrderStatusCommitted,
	); err != nil {
		t.Fatalf("UpdateOrderStatus: %v", err)
	}

	cancelled, forcedBypass, err := n.CancelHeld(
		ctx, order.ExternalID, "approval-1", testCaller, true,
	)
	if err != nil {
		t.Fatalf("CancelHeld force committed: %v", err)
	}
	if forcedBypass {
		t.Fatal("forcedBypass = true, want false for non-terminal status")
	}
	if cancelled.Status != domain.OrderStatusCancelled {
		t.Fatalf("status = %q, want cancelled", cancelled.Status)
	}
	if len(eng.rollbackHeldCalls) != 1 {
		t.Fatalf("rollback calls = %+v, want one", eng.rollbackHeldCalls)
	}
}

// TestReconcileAfterCrash_LeavesFullyHeldOrFullyResolved proves the reconcile/
// retry contract across a simulated crash. A crash that aborts the resolve tx
// leaves intent=held + status=accepted (a clean retry point); a fresh ConfirmHeld
// (driven through the post-restart intent fallback, native handle gone) then ends
// fully committed. A crash AFTER commit leaves intent=committed + status=committed
// with no half state.
func TestReconcileAfterCrash_LeavesFullyHeldOrFullyResolved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// --- Crash BEFORE commit: nothing persisted, then retry completes. ---
	t.Run("crash_before_commit_then_retry_completes", func(t *testing.T) {
		eng := newFakeEngine()
		// The native handle is gone post-restart: CommitHeld reports NotFound so the
		// node drives the intent fallback + the same atomic resolve.
		eng.commitErr = fmt.Errorf("engine: reservation %q: %w", "approval-1", domain.ErrNotFound)

		n, st := newTestNode(t, eng)

		// Simulate the crash: the resolve tx never committed, so the durable state
		// is exactly intent=held + status=accepted (what seedHeldOrder leaves).
		order := seedHeldOrder(t, st, "acc-1", "approval-1")

		// Retry: a fresh confirm drives the fallback and the atomic resolve, ending
		// fully committed.
		confirmed, _, err := n.ConfirmHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
		if err != nil {
			t.Fatalf("retry ConfirmHeld: %v", err)
		}
		if confirmed.Status != domain.OrderStatusCommitted {
			t.Fatalf("status = %q, want committed after retry", confirmed.Status)
		}
		if intentStillHeld(t, st, "approval-1") {
			t.Fatal("intent still held after retry, want committed")
		}
		types := eventTypes(t, st, order.ExternalID)
		if len(types) != 1 || types[0] != domain.OrderEventReservationCommitted {
			t.Fatalf("events = %+v, want [reservation_committed]", types)
		}
	})

	// --- Crash AFTER commit: fully resolved, no half state. ---
	t.Run("crash_after_commit_is_fully_resolved", func(t *testing.T) {
		eng := newFakeEngine()
		n, st := newTestNode(t, eng)
		order := seedHeldOrder(t, st, "acc-1", "approval-1")

		if _, _, err := n.ConfirmHeld(ctx, order.ExternalID, "approval-1", testCaller, false); err != nil {
			t.Fatalf("ConfirmHeld: %v", err)
		}
		// The committed state is durable: status committed, intent committed (absent
		// from the open/held set), one event. No accepted+held half-state remains.
		detail, err := st.GetOrder(ctx, order.ExternalID)
		if err != nil {
			t.Fatalf("GetOrder: %v", err)
		}
		if detail.Order.Status != domain.OrderStatusCommitted {
			t.Fatalf("status = %q, want committed", detail.Order.Status)
		}
		if intentStillHeld(t, st, "approval-1") {
			t.Fatal("intent still held after commit, want committed")
		}
		types := eventTypes(t, st, order.ExternalID)
		if len(types) != 1 || types[0] != domain.OrderEventReservationCommitted {
			t.Fatalf("events = %+v, want [reservation_committed]", types)
		}
	})
}
