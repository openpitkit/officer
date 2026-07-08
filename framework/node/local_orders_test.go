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
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

func TestLocalNode_ReconcileOrphansPreservesOrders(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.reconcileCount = 1
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	seedTestAccount(t, st, "acc-1")
	order, err := st.CreateOrder(ctx, domain.Order{
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
		ApprovalID: "approval-1",
		Order:      order.ExternalID,
		Account:    order.Account,
		ParamsJSON: `{"id":1}`,
		IssuedAt:   time.Now().UTC(),
		State:      domain.ReservationIntentStateHeld,
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
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusAccepted {
		t.Fatalf("order status = %q, want accepted", detail.Order.Status)
	}
	events, err := st.ListOrderEvents(ctx, order.ExternalID)
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
	eng.submitLock = []byte("lock")
	eng.holdOutcomes = []engine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceResult: "8000",
			HeldResult:    "2000",
		},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "10000",
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
	balance, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance: %v ok=%v", err, ok)
	}
	if balance.Available != "8000" || balance.Held != "2000" {
		t.Fatalf("balance = %+v, want available=8000 held=2000", balance)
	}
}

// TestLocalNode_SubmitOrderPersistsReservationBalances verifies the direct
// submit path mirrors the reservation's balance effects into the snapshot, so
// held funds and incoming quantity show up before any fill settles.
func TestLocalNode_SubmitOrderPersistsReservationBalances(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitLock = []byte("lock")
	eng.submitOutcomes = []engine.BalanceOutcome{
		{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceResult: "8000",
				HeldResult:    "2000",
			},
		},
		{
			Asset: "AAPL",
			Outcome: domain.AdjustmentOutcomeAccepted{
				IncomingResult: "20",
			},
		},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "10000",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("order status = %q, want committed", order.Status)
	}

	quote, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance USD: %v ok=%v", err, ok)
	}
	if quote.Held != "2000" {
		t.Fatalf("USD held = %q, want 2000", quote.Held)
	}
	base, ok, err := st.GetBalance(ctx, "acc-1", "AAPL")
	if err != nil || !ok {
		t.Fatalf("GetBalance AAPL: %v ok=%v", err, ok)
	}
	if base.Incoming != "20" {
		t.Fatalf("AAPL incoming = %q, want 20", base.Incoming)
	}
}

func TestLocalNode_SubmitOrderPostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("record order submission failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failOrderSubmissionAfterApplyRealm{RealmStore: r, err: storeErr}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, testCaller)
	if !errors.Is(err, storeErr) {
		t.Fatalf("SubmitOrder error = %v, want store failure", err)
	}
	if len(eng.submitCalls) != 1 {
		t.Fatalf("submit calls = %+v, want one engine apply", eng.submitCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine order persistence failure")
	}
	account, ok, err := n.realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="record order submission"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "record order submission failed") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

// TestLocalNode_SubmitImmediatePostEngineStoreFailureFatals mirrors the
// SubmitOrder fatal test for the immediate path: the engine applies the order
// inside RecordOrderSubmission's apply callback, then the store write fails, so
// the settlement can never be persisted and the node must fail-stop with the
// "record immediate submission" operation label.
func TestLocalNode_SubmitImmediatePostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("record immediate submission failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failOrderSubmissionAfterApplyRealm{RealmStore: r, err: storeErr}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, _, err := n.SubmitImmediate(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, testCaller)
	if !errors.Is(err, storeErr) {
		t.Fatalf("SubmitImmediate error = %v, want store failure", err)
	}
	if len(eng.submitCalls) != 1 {
		t.Fatalf("submit calls = %+v, want one engine apply", eng.submitCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine immediate persistence failure")
	}
	account, ok, err := n.realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="record immediate submission"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "record immediate submission failed") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

func TestLocalNode_SubmitOrderAutoCreatesUnknownAsset(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "GOLD",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("order status = %q, want committed", order.Status)
	}

	asset, ok, err := st.GetAsset(ctx, "GOLD")
	if err != nil || !ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want auto-created", ok, err)
	}
	if asset.AssetClass != autoCreatedAssetClassCode {
		t.Fatalf("auto-created asset class = %q, want %q", asset.AssetClass,
			autoCreatedAssetClassCode)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAsset},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create asset): %v", err)
	}
	if len(rows) != 1 ||
		rows[0].Asset != "GOLD" ||
		!strings.Contains(rows[0].Detail, "auto-created asset GOLD by submit order") {
		t.Fatalf("create-asset audit rows = %+v, want submit-order auto-create", rows)
	}
}

// TestLocalNode_SubmitOrderAutoCreatesUnknownAccount checks that an order
// submitted for an account Officer does not know yet auto-creates it (like a
// fresh adjustment target), so the engine resolves the account and the order
// is processed rather than rejected as invalid, and the creation is audited.
func TestLocalNode_SubmitOrderAutoCreatesUnknownAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	// Enforce the resolver so a submit against an unknown account would error
	// unless the auto-create runs first and rebuilds the engine.
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order, err := n.SubmitOrder(ctx, testKey("fresh"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("order status = %q, want committed", order.Status)
	}

	if _, ok, err := st.GetAccount(ctx, "fresh"); err != nil || !ok {
		t.Fatalf("GetAccount(fresh) = ok %v err %v, want auto-created", ok, err)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAccount},
		Account: "fresh",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create account): %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "auto-created account fresh by submit order") {
		t.Fatalf("create-account audit rows = %+v, want submit-order auto-create", rows)
	}
}

// TestLocalNode_SubmitOrderHonorsSuppliedExternalID covers the create-once
// id-honoring path end-to-end through the real store: a caller-supplied order
// external id is used verbatim and the returned order carries it, and exactly
// one order row is created under it.
func TestLocalNode_SubmitOrderHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	supplied := externalID(t, "supplied-order-id")
	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		ExternalID:  supplied,
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.ExternalID != supplied {
		t.Fatalf("returned order id = %q, want supplied %q", order.ExternalID, supplied)
	}
	detail, err := st.GetOrder(ctx, supplied)
	if err != nil {
		t.Fatalf("GetOrder by supplied id: %v", err)
	}
	if detail.Order.ExternalID != supplied {
		t.Fatalf("stored order id = %q, want supplied %q", detail.Order.ExternalID, supplied)
	}
}

// TestLocalNode_SubmitOrderGeneratesExternalIDWhenAbsent covers the absent-id
// path: a zero id leaves the store to mint a canonical one.
func TestLocalNode_SubmitOrderGeneratesExternalIDWhenAbsent(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.ExternalID.IsZero() {
		t.Fatalf("order id is zero, want a generated id")
	}
	if err := domain.ValidateExternalID(order.ExternalID.String()); err != nil {
		t.Fatalf("generated order id not canonical: %v", err)
	}
}

// TestLocalNode_SubmitOrderDuplicateSuppliedIDConflicts covers the conflict
// path: a second submit reusing a supplied id surfaces domain.ErrAlreadyExists
// from the store, propagated unchanged.
func TestLocalNode_SubmitOrderDuplicateSuppliedIDConflicts(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	supplied := externalID(t, "dup-order-id")
	mk := func() domain.Order {
		return domain.Order{
			ExternalID:  supplied,
			BaseAsset:   "AAPL",
			QuoteAsset:  "USD",
			Side:        domain.OrderSideBuy,
			AmountKind:  domain.OrderAmountKindQuantity,
			AmountValue: "20",
			Price:       "100",
		}
	}
	if _, err := n.SubmitOrder(ctx, testKey("acc-1"), mk(), testCaller); err != nil {
		t.Fatalf("first SubmitOrder: %v", err)
	}
	_, err := n.SubmitOrder(ctx, testKey("acc-1"), mk(), testCaller)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate supplied id error = %v, want ErrAlreadyExists", err)
	}
}

func TestLocalNode_SubmitOrderDifferentAccountsProceedConcurrently(t *testing.T) {
	t.Parallel()
	entered := make(chan domain.AccountID, 2)
	release := make(chan struct{})
	eng := newFakeEngine()
	eng.submitEntered = entered
	eng.submitRelease = release
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")
	seedTestAccount(t, st, "acc-2")

	submit := func(account domain.AccountID, errs chan<- error) {
		_, err := n.SubmitOrder(ctx, testKey(account), domain.Order{
			BaseAsset: "AAPL", QuoteAsset: "USD",
			Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
			AmountValue: "1", Price: "10",
		}, testCaller)
		errs <- err
	}
	errs := make(chan error, 2)
	go submit("acc-1", errs)
	if got := <-entered; got != "acc-1" {
		t.Fatalf("first entered account = %s, want acc-1", got)
	}
	go submit("acc-2", errs)
	select {
	case got := <-entered:
		if got != "acc-2" {
			t.Fatalf("second entered account = %s, want acc-2", got)
		}
	case <-time.After(time.Second):
		t.Fatal("second account did not enter SubmitOrder while first account was in-flight")
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("SubmitOrder #%d: %v", i, err)
		}
	}
}

func TestLocalNode_SubmitOrderSameAccountSerializes(t *testing.T) {
	t.Parallel()
	entered := make(chan domain.AccountID, 2)
	release := make(chan struct{})
	eng := newFakeEngine()
	eng.submitEntered = entered
	eng.submitRelease = release
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	submit := func(errs chan<- error) {
		_, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
			BaseAsset: "AAPL", QuoteAsset: "USD",
			Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
			AmountValue: "1", Price: "10",
		}, testCaller)
		errs <- err
	}
	errs := make(chan error, 2)
	go submit(errs)
	if got := <-entered; got != "acc-1" {
		t.Fatalf("first entered account = %s, want acc-1", got)
	}
	go submit(errs)
	select {
	case got := <-entered:
		t.Fatalf("second same-account SubmitOrder entered early as %s", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if got := <-entered; got != "acc-1" {
		t.Fatalf("second entered account = %s, want acc-1", got)
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("SubmitOrder #%d: %v", i, err)
		}
	}
}

// TestLocalNode_SubmitHoldHonorsSuppliedExternalID covers the hold submit path:
// the supplied id is recorded once and confirm/cancel can resolve the same order
// by it.
func TestLocalNode_SubmitHoldHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	supplied := externalID(t, "supplied-hold-id")
	order, _, err := n.SubmitHold(ctx, testKey("acc-1"), domain.Order{
		ExternalID:  supplied,
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
	if order.ExternalID != supplied {
		t.Fatalf("returned order id = %q, want supplied %q", order.ExternalID, supplied)
	}
	if _, err := st.GetOrder(ctx, supplied); err != nil {
		t.Fatalf("GetOrder by supplied id: %v", err)
	}
}

// TestLocalNode_ApplyAdjustmentHonorsSuppliedExternalID covers the user-create
// adjustment path end-to-end: a supplied id is carried onto the record and
// persisted verbatim; a duplicate surfaces domain.ErrAlreadyExists.
