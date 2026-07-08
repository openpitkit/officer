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
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

func TestLocalNode_ApplyAdjustmentHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceResult: "100",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	supplied := externalID(t, "supplied-adj-id")
	req := domain.AdjustmentRequest{
		Asset: "USD",
		Balance: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeDelta, Value: "100",
		},
	}
	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), supplied, req, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.ExternalID != supplied {
		t.Fatalf("record id = %q, want supplied %q", rec.ExternalID, supplied)
	}
	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].ExternalID != supplied {
		t.Fatalf("stored adjustments = %+v, want one under supplied id", records)
	}

	// A second adjustment reusing the supplied id conflicts.
	_, err = n.ApplyAdjustment(ctx, testKey("acc-1"), supplied, req, testCaller)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate supplied id error = %v, want ErrAlreadyExists", err)
	}
}

func TestLocalNode_ApplyAdjustmentNoChangeDoesNotPersist(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentNoop = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "0"},
		}, testCaller)
	if !errors.Is(err, domain.ErrNoChange) {
		t.Fatalf("ApplyAdjustment error = %v, want ErrNoChange", err)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %+v, want one", eng.adjustmentCalls)
	}
	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("stored adjustments = %+v, want none", records)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("audit rows = %+v, want none", rows)
	}
}

func TestLocalNode_ApplyAdjustmentStoreFailureFatalsWithoutCompensation(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("record adjustment failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failAccountAdjustmentRecordRealm{RealmStore: r, err: storeErr}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceDelta:  "5",
		BalanceResult: "15",
	}
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	seedTestAccount(t, n.realm, "acc-1")
	if err := n.realm.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "10", AverageEntryPrice: "42",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:             "USD",
			AverageEntryPrice: "99",
			Balance:           &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "5"},
		}, testCaller)
	if !errors.Is(err, storeErr) {
		t.Fatalf("ApplyAdjustment error = %v, want store failure", err)
	}
	if len(eng.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %+v, want only the requested apply", eng.adjustmentCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine adjustment persistence failure")
	}
	account, ok, err := n.realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="record account adjustment"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "record adjustment failed") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

// TestLocalNode_ApplyAdjustmentGeneratesExternalIDWhenAbsent covers the
// absent-id path: a zero id lets the store mint one.
func TestLocalNode_ApplyAdjustmentGeneratesExternalIDWhenAbsent(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "100"}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""), domain.AdjustmentRequest{
		Asset: "USD",
		Balance: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeDelta, Value: "100",
		},
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.ExternalID.IsZero() {
		t.Fatalf("record id is zero, want a generated id")
	}
}

// TestLocalNode_ApplyAdjustmentRejectedIsRecorded checks that a policy reject
// (e.g. a P&L kill-switch) on an existing account persists the rejected attempt
// to the adjustment history and the audit log, leaving balances untouched.
func TestLocalNode_ApplyAdjustmentRejectedIsRecorded(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentReject = &domain.AdjustmentOutcomeRejected{
		Code:   "pnl_bounds_kill_switch",
		Policy: "pnl_bounds_kill_switch",
		Reason: "upper bound exceeded",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted != nil || rec.Rejected == nil {
		t.Fatalf("record = %+v, want rejected", rec)
	}

	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].Rejected == nil {
		t.Fatalf("stored adjustments = %+v, want one rejected record", records)
	}

	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "rejected") {
		t.Fatalf("audit rows = %+v, want one rejected adjustment", rows)
	}

	if _, ok, err := st.GetBalance(ctx, "acc-1", "USD"); err != nil || ok {
		t.Fatalf("GetBalance after reject = ok %v err %v, want no balance row", ok, err)
	}
}

// TestLocalNode_ApplyAdjustmentAutoCreatesUnknownAccount checks that an
// adjustment to a non-existent account auto-creates it in the default group (no
// group assigned), so the engine resolves it and applies the adjustment, and
// the creation is audited.
func TestLocalNode_ApplyAdjustmentAutoCreatesUnknownAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	// Enforce the resolver so an adjustment to an unknown account would error
	// unless the auto-create runs first and rebuilds the engine.
	eng.enforceResolver = true
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "100"}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	rec, err := n.ApplyAdjustment(ctx, testKey("fresh"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted == nil || rec.Rejected != nil {
		t.Fatalf("record = %+v, want accepted", rec)
	}

	account, ok, err := st.GetAccount(ctx, "fresh")
	if err != nil || !ok {
		t.Fatalf("GetAccount(fresh) = ok %v err %v, want auto-created", ok, err)
	}
	if account.GroupCode != "" {
		t.Fatalf("auto-created account group = %q, want default (empty)", account.GroupCode)
	}

	records, err := st.ListAdjustments(ctx, "fresh", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].Accepted == nil {
		t.Fatalf("stored adjustments = %+v, want one accepted record", records)
	}

	createRows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAccount},
		Account: "fresh",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create): %v", err)
	}
	if len(createRows) != 1 {
		t.Fatalf("create-account audit rows = %+v, want one", createRows)
	}
}

// TestLocalNode_ApplyAdjustmentAutoCreateThenReject checks that a rejected
// adjustment to a non-existent account still auto-creates the account and
// records the rejected attempt in both history and audit.
func TestLocalNode_ApplyAdjustmentAutoCreateThenReject(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	eng.adjustmentReject = &domain.AdjustmentOutcomeRejected{
		Code:   "pnl_bounds_kill_switch",
		Policy: "pnl_bounds_kill_switch",
		Reason: "upper bound exceeded",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	rec, err := n.ApplyAdjustment(ctx, testKey("fresh"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted != nil || rec.Rejected == nil {
		t.Fatalf("record = %+v, want rejected", rec)
	}

	if _, ok, err := st.GetAccount(ctx, "fresh"); err != nil || !ok {
		t.Fatalf("GetAccount(fresh) = ok %v err %v, want auto-created", ok, err)
	}
	records, err := st.ListAdjustments(ctx, "fresh", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].Rejected == nil {
		t.Fatalf("stored adjustments = %+v, want one rejected record", records)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "fresh",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "rejected") {
		t.Fatalf("audit rows = %+v, want one rejected adjustment", rows)
	}
}

// TestLocalNode_ApplyAdjustmentRejectsMalformedAccountID checks that a malformed
// account id is rejected with domain.ErrInvalid and no account is auto-created.
func TestLocalNode_ApplyAdjustmentRejectsMalformedAccountID(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	bad := domain.AccountID("bad-id ")
	_, err := n.ApplyAdjustment(ctx, testKey(bad), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "USD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyAdjustment(malformed) = %v, want ErrInvalid", err)
	}
	if _, ok, err := st.GetAccount(ctx, bad); err != nil || ok {
		t.Fatalf("GetAccount(malformed) = ok %v err %v, want absent", ok, err)
	}
}

// TestLocalNode_ApplyAdjustmentAutoCreatesUnknownAsset checks that an adjustment
// referencing a non-existent asset auto-creates it in the auto-created class, so
// the engine resolves it and applies the adjustment, and the creation is
// audited with the originating operation.
func TestLocalNode_ApplyAdjustmentAutoCreatesUnknownAsset(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "100"}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "GOLD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted == nil || rec.Rejected != nil {
		t.Fatalf("record = %+v, want accepted", rec)
	}

	asset, ok, err := st.GetAsset(ctx, "GOLD")
	if err != nil || !ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want auto-created", ok, err)
	}
	if asset.Title != "" || asset.AssetClass != autoCreatedAssetClassCode {
		t.Fatalf("auto-created asset = %+v, want empty title and %q class", asset,
			autoCreatedAssetClassCode)
	}

	createRows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAsset},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create asset): %v", err)
	}
	if len(createRows) != 1 ||
		createRows[0].Asset != "GOLD" ||
		!strings.Contains(createRows[0].Detail, "auto-created asset GOLD by adjustment") {
		t.Fatalf("create-asset audit rows = %+v, want one for GOLD", createRows)
	}
}

// TestLocalNode_ApplyAdjustmentAutoCreateAssetThenReject checks that a rejected
// adjustment to a non-existent asset still auto-creates the asset and records
// the rejected attempt in both history and audit.
func TestLocalNode_ApplyAdjustmentAutoCreateAssetThenReject(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentReject = &domain.AdjustmentOutcomeRejected{
		Code:   "pnl_bounds_kill_switch",
		Policy: "pnl_bounds_kill_switch",
		Reason: "upper bound exceeded",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	rec, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   "GOLD",
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted != nil || rec.Rejected == nil {
		t.Fatalf("record = %+v, want rejected", rec)
	}

	asset, ok, err := st.GetAsset(ctx, "GOLD")
	if err != nil || !ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want auto-created", ok, err)
	}
	if asset.AssetClass != autoCreatedAssetClassCode {
		t.Fatalf("auto-created asset class = %q, want %q", asset.AssetClass,
			autoCreatedAssetClassCode)
	}
	records, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(records) != 1 || records[0].Rejected == nil {
		t.Fatalf("stored adjustments = %+v, want one rejected record", records)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "rejected") {
		t.Fatalf("audit rows = %+v, want one rejected adjustment", rows)
	}
}

// TestLocalNode_ApplyAdjustmentRejectsMalformedAsset checks that a malformed
// asset id is rejected with domain.ErrInvalid and no asset is auto-created.
func TestLocalNode_ApplyAdjustmentRejectsMalformedAsset(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	bad := "US D"
	_, err := n.ApplyAdjustment(ctx, testKey("acc-1"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset:   bad,
			Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "100"},
		}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyAdjustment(malformed asset) = %v, want ErrInvalid", err)
	}
	if _, ok, err := st.GetAsset(ctx, bad); err != nil || ok {
		t.Fatalf("GetAsset(malformed) = ok %v err %v, want absent", ok, err)
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
	seedTestAccount(t, st, "acc-1")
	order, err := st.CreateOrder(ctx, domain.Order{
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
		Account: "acc-1", Asset: "USD", Available: "8000", Held: "2000",
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
		ApprovalID: "approval-1",
		Order:      order.ExternalID,
		Account:    order.Account,
		ParamsJSON: string(payload),
		IssuedAt:   time.Now().UTC(),
		State:      domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}

	cancelled, _, err := n.CancelHeld(ctx, order.ExternalID, "approval-1", testCaller, false)
	if err != nil {
		t.Fatalf("CancelHeld: %v", err)
	}
	if cancelled.Status != domain.OrderStatusCancelled {
		t.Fatalf("order status = %q, want cancelled", cancelled.Status)
	}
	balance, ok, err := st.GetBalance(ctx, "acc-1", "USD")
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

func TestDecodeReservationIntentPayloadWrapperWorks(t *testing.T) {
	t.Parallel()
	order := domain.Order{
		ExternalID: externalID(t, "held-order-id"),
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
	}
	outcomes := []engine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceDelta: "-100",
			HeldDelta:    "100",
		},
	}}
	raw, err := json.Marshal(reservationIntentPayload{Order: order, Outcomes: outcomes})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	gotOrder, gotOutcomes, err := decodeReservationIntentPayload(string(raw))
	if err != nil {
		t.Fatalf("decodeReservationIntentPayload: %v", err)
	}
	if gotOrder.ExternalID != order.ExternalID || gotOrder.Account != order.Account {
		t.Fatalf("order = %+v, want %+v", gotOrder, order)
	}
	if len(gotOutcomes) != 1 || gotOutcomes[0].Asset != "USD" {
		t.Fatalf("outcomes = %+v, want one USD outcome", gotOutcomes)
	}
}

func TestDecodeReservationIntentPayloadRejectsBareOrder(t *testing.T) {
	t.Parallel()
	order := domain.Order{
		ExternalID: externalID(t, "bare-order-id"),
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
	}
	raw, err := json.Marshal(order)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	_, _, err = decodeReservationIntentPayload(string(raw))
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("decode bare order error = %v, want ErrInvalid", err)
	}
	if err == nil || !strings.Contains(err.Error(), "malformed reservation intent payload") {
		t.Fatalf("decode bare order error = %v, want malformed payload message", err)
	}
}
