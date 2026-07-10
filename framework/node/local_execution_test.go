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
	"slices"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

func testOrder(t *testing.T, st store.RealmStore, id domain.AccountID) domain.Order {
	t.Helper()
	seedTestAccount(t, st, id)
	order, err := st.CreateOrder(context.Background(), domain.Order{
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
		{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceResult:    "800",
				RealizedPnlDelta: "12.50",
			},
		},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:        order.ExternalID,
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "2",
		FillPrice:    "400",
		LockPrice:    "400",
		OrderStatus:  domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}

	base, ok, err := st.GetBalance(ctx, id, "AAPL")
	if err != nil || !ok {
		t.Fatalf("GetBalance base: %v ok=%v", err, ok)
	}
	if base.Available != "2" {
		t.Fatalf("base available = %q, want 2", base.Available)
	}
	quote, ok, err := st.GetBalance(ctx, id, "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance quote: %v ok=%v", err, ok)
	}
	if quote.Available != "800" {
		t.Fatalf("quote available = %q, want 800", quote.Available)
	}
	if quote.RealizedPnl != "12.5" {
		t.Fatalf("quote realized_pnl = %q, want 12.5", quote.RealizedPnl)
	}
}

// TestLocalNode_ApplyExecutionReportPostEngineStoreFailureFatals proves the
// execution-report settlement write is a post-engine persistence step: the
// engine applies the report first (execReportCalls==1), then the atomic
// RecordOrderSettlement fails, so the node must fail-stop with the "record
// execution report" operation label, the real account id, and the wrapped cause.
func TestLocalNode_ApplyExecutionReportPostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("record execution report failed")
	real := newMemoryStore("node.db")
	ctx := context.Background()
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })

	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failOrderSettlementRealm{RealmStore: r, err: storeErr}
	})

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))

	// Seed the order through the underlying real store; the failing wrapper only
	// rejects the settlement write, so routing reads and the order create succeed.
	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	order := testOrder(t, realm, "acc-1")

	_, err = n.ApplyExecutionReport(ctx, testKey("acc-1"), domain.ExecutionReportInput{
		Order:        order.ExternalID,
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "2",
		FillPrice:    "400",
		LockPrice:    "400",
		OrderStatus:  domain.OrderStatusFilled,
	}, testCaller)
	if !errors.Is(err, storeErr) {
		t.Fatalf("ApplyExecutionReport error = %v, want store failure", err)
	}
	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine report calls = %+v, want one engine apply", eng.execReportCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine execution-report persistence failure")
	}
	account, ok, err := realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="record execution report"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "record execution report failed") {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
}

func TestLocalNode_ApplyExecutionReportDeletesEmptySettledPosition(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.execReportOutcomes = []engine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceDelta:  "-100.00",
			BalanceResult: "0",
		},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	order := testOrder(t, st, "acc-1")
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD",
		Available: "100",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	_, err := n.ApplyExecutionReport(ctx, testKey("acc-1"), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		OrderStatus:    domain.OrderStatusRejected,
		LeavesQuantity: "0",
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if _, ok, err := st.GetBalance(ctx, "acc-1", "USD"); err != nil || ok {
		t.Fatalf("GetBalance after empty settlement: ok=%v err=%v, want missing", ok, err)
	}
}

func TestLocalNode_ApplyExecutionReportIgnoresReportAssetFields(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:        order.ExternalID,
		BaseAsset:    "GOLD",
		QuoteAsset:   "EUR",
		Side:         domain.OrderSideSell,
		FillQuantity: "2",
		FillPrice:    "400",
		LockPrice:    "400",
		OrderStatus:  domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}

	if _, ok, err := st.GetAsset(ctx, "GOLD"); err != nil || ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want no auto-create", ok, err)
	}
	if len(eng.execReportCalls) != 1 ||
		eng.execReportCalls[0].BaseAsset != order.BaseAsset ||
		eng.execReportCalls[0].QuoteAsset != order.QuoteAsset ||
		eng.execReportCalls[0].Side != order.Side {
		t.Fatalf("engine report calls = %+v, want order instrument", eng.execReportCalls)
	}
}

// TestLocalNode_ApplyExecutionReportUsesOrderAccountOverRouteKey checks that
// the in-lane order read owns the report account even when the caller passed a
// different route key.
func TestLocalNode_ApplyExecutionReportUsesOrderAccountOverRouteKey(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
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

	if _, err := n.ApplyExecutionReport(ctx, testKey("fresh-report"), domain.ExecutionReportInput{
		Order:        order.ExternalID,
		FillQuantity: "2",
		FillPrice:    "100",
		LockPrice:    "100",
		OrderStatus:  domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}

	if _, ok, err := st.GetAccount(ctx, "fresh-report"); err != nil || ok {
		t.Fatalf("GetAccount(fresh-report) = ok %v err %v, want no account", ok, err)
	}
	if len(eng.execReportCalls) != 1 || eng.execReportCalls[0].Account != "acc-1" {
		t.Fatalf("engine report calls = %+v, want order account acc-1", eng.execReportCalls)
	}
}

// TestLocalNode_ApplyExecutionReportAuditsEngineBlock verifies an engine-
// initiated block during settlement is mirrored to the account and recorded as
// a system-sourced block audit row carrying the engine reason.
func TestLocalNode_ApplyExecutionReportAuditsEngineBlock(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.execReportBlocks = []domain.ExecutionAccountBlock{
		{Account: "acc-1", Code: "test_block", Reason: "account block triggered"},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:        order.ExternalID,
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "2",
		FillPrice:    "400",
		LockPrice:    "400",
		OrderStatus:  domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}

	acc, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: %v ok=%v", err, ok)
	}
	wantReason := fmt.Sprintf("account block triggered [code=test_block, order %s]", order.ExternalID)
	if !acc.Blocked || acc.BlockReason != wantReason {
		t.Fatalf("account block reason = %q, want %q", acc.BlockReason, wantReason)
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(detail.Events) != 1 {
		t.Fatalf("events = %d, want 1 fill event", len(detail.Events))
	}
	payload := detail.Events[0].Payload
	if payload.RejectCode != "test_block" ||
		payload.RejectScope != "account" ||
		payload.RejectReason != "account block triggered" {
		t.Fatalf("fill event reject payload = %+v", payload)
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
	// An engine block is system-initiated: SourceSystem marks the channel and the
	// actor is empty (system origin carries no principal dictionary code).
	if rows[0].Source != domain.SourceSystem || rows[0].Actor != "" {
		t.Fatalf("block not attributed to system with empty actor: %+v", rows[0])
	}
	if !strings.Contains(rows[0].Detail, "account block triggered") {
		t.Fatalf("block detail missing reason: %q", rows[0].Detail)
	}
}

func TestLocalNode_ApplyExecutionReportForce(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)
	// A terminal order is the guard force must bypass; forcing another fill still
	// settles a trade and re-advances the status past the AllowedFrom net.
	if err := st.UpdateOrderStatus(ctx, order.ExternalID, domain.OrderStatusFilled); err != nil {
		t.Fatalf("UpdateOrderStatus: %v", err)
	}

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "0",
		LockPrice:      "400",
		Force:          true,
		OrderStatus:    domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport force: %v", err)
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("order status = %q, want filled", detail.Order.Status)
	}

	trades, err := st.ListTrades(ctx, id, domain.SourceAPI, 10)
	if err != nil {
		t.Fatalf("ListTrades: %v", err)
	}
	if len(trades) != 1 {
		t.Fatalf("trade count = %d, want 1", len(trades))
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionExecutionReport},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "forced=true") {
		t.Fatalf("audit rows = %+v, want forced=true execution report", rows)
	}
}

func TestLocalNode_ApplyExecutionReportForceOnOpenOrderDoesNotAuditForced(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "1",
		Force:          true,
		OrderStatus:    domain.OrderStatusPartiallyFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport force open: %v", err)
	}

	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionExecutionReport},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || strings.Contains(rows[0].Detail, "forced=true") {
		t.Fatalf("audit rows = %+v, want no forced=true", rows)
	}
}

// TestLocalNode_ApplyExecutionReportStatusOnlyNoAccountWrites drives a
// status-only report through the fake's real persistence mapper (no emptyExecReportPersistence
// shortcut): the report carries no fill, no engine outcomes, and no blocks, so the
// mapper emits nil Balances and empty Blocks. The node must then write zero
// balance rows and zero account-block state/audit rows while still recording the
// status change.
func TestLocalNode_ApplyExecutionReportStatusOnlyNoAccountWrites(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:       order.ExternalID,
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		OrderStatus: domain.OrderStatusCancelled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport status-only: %v", err)
	}

	if _, ok, err := st.GetBalance(ctx, id, "AAPL"); err != nil || ok {
		t.Fatalf("base balance ok=%v err=%v, want no balance row written", ok, err)
	}
	if _, ok, err := st.GetBalance(ctx, id, "USD"); err != nil || ok {
		t.Fatalf("quote balance ok=%v err=%v, want no balance row written", ok, err)
	}
	account, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount ok=%v err=%v", ok, err)
	}
	if account.Blocked {
		t.Fatal("account is blocked, want no account-block write on a status-only report")
	}
	blockRows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(blockRows) != 0 {
		t.Fatalf("block audit rows = %+v, want none", blockRows)
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusCancelled {
		t.Fatalf("status = %q, want cancelled (the venue-owned status change is recorded)",
			detail.Order.Status)
	}
}

func TestLocalNode_ApplyExecutionReportUsesOrderFields(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		Account:        "wrong-account",
		BaseAsset:      "WRONG",
		QuoteAsset:     "NOPE",
		Side:           domain.OrderSideSell,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "1",
		OrderStatus:    domain.OrderStatusPartiallyFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
	}
	call := eng.execReportCalls[0]
	if call.Account != id ||
		call.BaseAsset != order.BaseAsset ||
		call.QuoteAsset != order.QuoteAsset ||
		call.Side != order.Side {
		t.Fatalf("engine call = %+v, want authoritative order fields from %+v",
			call, order)
	}
}

func TestLocalNode_ApplyExecutionReportIgnoresCanceledContext(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	reportCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := n.ApplyExecutionReport(reportCtx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "0",
		OrderStatus:    domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport with canceled context: %v", err)
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("status = %q, want filled", detail.Order.Status)
	}
}

func TestLocalNode_ApplyExecutionReportUsesAccountSyncLane(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "0",
		OrderStatus:    domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if eng.execReportOutsideSync {
		t.Fatal("execution report applied outside the account sync lane")
	}
	if len(eng.accountSyncCalls) == 0 || eng.accountSyncCalls[len(eng.accountSyncCalls)-1] != id {
		t.Fatalf("account sync calls = %+v, want last %s", eng.accountSyncCalls, id)
	}
}

func TestLocalNode_ApplyExecutionReportAlwaysUsesEngine(t *testing.T) {
	t.Parallel()
	persistedLock := []byte{0x01, 0x02, 0x03}
	cases := []struct {
		name            string
		input           domain.ExecutionReportInput
		orderStatus     domain.OrderStatus
		heldIntent      bool
		outcomes        []engine.BalanceOutcome
		blocks          []domain.ExecutionAccountBlock
		wantStatus      domain.OrderStatus
		wantLeaves      string
		wantEvent       domain.OrderEventType
		wantTrade       bool
		wantBlocked     bool
		wantNoQuote     bool
		wantCommit      int
		wantRollback    int
		wantIntentState domain.ReservationIntentState
	}{
		{
			name: "committed reject releases funds",
			input: domain.ExecutionReportInput{
				OrderStatus:    domain.OrderStatusRejected,
				LeavesQuantity: "0",
			},
			orderStatus: domain.OrderStatusCommitted,
			outcomes: []engine.BalanceOutcome{{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult: "10000",
					HeldResult:    "0",
				},
			}},
			wantStatus: domain.OrderStatusRejected,
			wantLeaves: "0",
			wantEvent:  domain.OrderEventPreTradeRejected,
		},
		{
			name: "committed cancel releases funds",
			input: domain.ExecutionReportInput{
				OrderStatus:    domain.OrderStatusCancelled,
				LeavesQuantity: "0",
			},
			orderStatus: domain.OrderStatusCommitted,
			outcomes: []engine.BalanceOutcome{{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult: "10000",
					HeldResult:    "0",
				},
			}},
			wantStatus: domain.OrderStatusCancelled,
			wantLeaves: "0",
			wantEvent:  domain.OrderEventCancelled,
		},
		{
			name: "fill stays fill settlement",
			input: domain.ExecutionReportInput{
				FillQuantity:   "2",
				FillPrice:      "400",
				LeavesQuantity: "0",
				OrderStatus:    domain.OrderStatusFilled,
			},
			orderStatus: domain.OrderStatusCommitted,
			outcomes: []engine.BalanceOutcome{
				{Asset: "AAPL", Outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "2"}},
				{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{HeldDelta: "0"}},
			},
			wantStatus:  domain.OrderStatusFilled,
			wantLeaves:  "0",
			wantEvent:   domain.OrderEventFill,
			wantTrade:   true,
			wantNoQuote: true,
		},
		{
			name: "engine block still applies balances",
			input: domain.ExecutionReportInput{
				OrderStatus:    domain.OrderStatusRejected,
				LeavesQuantity: "0",
			},
			orderStatus: domain.OrderStatusCommitted,
			outcomes: []engine.BalanceOutcome{{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult: "10000",
					HeldResult:    "0",
				},
			}},
			blocks: []domain.ExecutionAccountBlock{{
				Account: "acc-1",
				Code:    "post_trade_reject",
				Reason:  "engine rejected report",
			}},
			wantStatus:  domain.OrderStatusRejected,
			wantLeaves:  "0",
			wantEvent:   domain.OrderEventPreTradeRejected,
			wantBlocked: true,
		},
		{
			name: "accepted held fill marks reservation committed",
			input: domain.ExecutionReportInput{
				FillQuantity:   "2",
				FillPrice:      "400",
				LeavesQuantity: "0",
				OrderStatus:    domain.OrderStatusFilled,
			},
			orderStatus: domain.OrderStatusAccepted,
			heldIntent:  true,
			outcomes: []engine.BalanceOutcome{
				{Asset: "AAPL", Outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "2"}},
				{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "10000", HeldResult: "0"}},
			},
			wantStatus:      domain.OrderStatusFilled,
			wantLeaves:      "0",
			wantEvent:       domain.OrderEventFill,
			wantTrade:       true,
			wantIntentState: domain.ReservationIntentStateCommitted,
		},
		{
			name: "accepted held reject applies only report engine adjustments",
			input: domain.ExecutionReportInput{
				OrderStatus:    domain.OrderStatusRejected,
				LeavesQuantity: "0",
			},
			orderStatus: domain.OrderStatusAccepted,
			heldIntent:  true,
			outcomes: []engine.BalanceOutcome{{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult: "10000",
					HeldResult:    "0",
				},
			}},
			wantStatus:      domain.OrderStatusRejected,
			wantLeaves:      "0",
			wantEvent:       domain.OrderEventPreTradeRejected,
			wantIntentState: domain.ReservationIntentStateRolledBack,
		},
		{
			name: "empty engine response changes no balances",
			input: domain.ExecutionReportInput{
				OrderStatus:    domain.OrderStatusRejected,
				LeavesQuantity: "0",
			},
			orderStatus:     domain.OrderStatusAccepted,
			heldIntent:      true,
			wantStatus:      domain.OrderStatusRejected,
			wantLeaves:      "0",
			wantEvent:       domain.OrderEventPreTradeRejected,
			wantIntentState: domain.ReservationIntentStateRolledBack,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			eng.execReportOutcomes = tc.outcomes
			eng.execReportBlocks = tc.blocks
			n, st := newTestNode(t, eng)
			ctx := context.Background()

			const id domain.AccountID = "acc-1"
			if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
				t.Fatalf("CreateAccount: %v", err)
			}
			order, err := st.CreateOrder(ctx, domain.Order{
				Account:     id,
				Source:      domain.SourceAPI,
				Principal:   "operator",
				BaseAsset:   "AAPL",
				QuoteAsset:  "USD",
				Side:        domain.OrderSideBuy,
				AmountKind:  domain.OrderAmountKindQuantity,
				AmountValue: "2",
				Price:       "400",
				Lock:        persistedLock,
				Status:      tc.orderStatus,
			})
			if err != nil {
				t.Fatalf("CreateOrder: %v", err)
			}
			if tc.heldIntent {
				if err := st.UpsertBalance(ctx, domain.Balance{
					Account:   id,
					Asset:     "USD",
					Available: "8000",
					Held:      "2000",
				}); err != nil {
					t.Fatalf("UpsertBalance: %v", err)
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
			}

			in := tc.input
			in.Order = order.ExternalID
			in.BaseAsset = "AAPL"
			in.QuoteAsset = "USD"
			in.Side = domain.OrderSideBuy
			result, err := n.ApplyExecutionReport(ctx, testKey(id), in, testCaller)
			if err != nil {
				t.Fatalf("ApplyExecutionReport: %v", err)
			}
			if len(result.Blocks) != len(tc.blocks) || len(result.Outcomes) != len(tc.outcomes) {
				t.Fatalf("result = %+v, want blocks=%d outcomes=%d",
					result, len(tc.blocks), len(tc.outcomes))
			}
			if len(eng.execReportCalls) != 1 {
				t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
			}
			if len(eng.rollbackHeldCalls) != tc.wantRollback {
				t.Fatalf("rollback calls = %+v, want %d", eng.rollbackHeldCalls, tc.wantRollback)
			}
			if len(eng.commitHeldCalls) != tc.wantCommit {
				t.Fatalf("commit calls = %+v, want %d", eng.commitHeldCalls, tc.wantCommit)
			}
			call := eng.execReportCalls[0]
			if call.Order != order.ExternalID ||
				call.Account != id ||
				!slices.Equal(call.Lock, persistedLock) {
				t.Fatalf("engine call = %+v, want order/account/persisted lock", call)
			}
			if call.LeavesQuantity != in.LeavesQuantity {
				t.Fatalf("engine leaves = %q, want request leaves %q",
					call.LeavesQuantity, in.LeavesQuantity)
			}

			detail, err := st.GetOrder(ctx, order.ExternalID)
			if err != nil {
				t.Fatalf("GetOrder: %v", err)
			}
			if detail.Order.Status != tc.wantStatus {
				t.Fatalf("order status = %q, want %q", detail.Order.Status, tc.wantStatus)
			}
			if detail.Order.Leaves != tc.wantLeaves {
				t.Fatalf("leaves = %q, want %q", detail.Order.Leaves, tc.wantLeaves)
			}
			if len(detail.Events) == 0 || detail.Events[0].Type != tc.wantEvent {
				t.Fatalf("events = %+v, want first %s", detail.Events, tc.wantEvent)
			}
			if gotTrade := len(detail.Trades) == 1; gotTrade != tc.wantTrade {
				t.Fatalf("trade present = %v, want %v: %+v", gotTrade, tc.wantTrade, detail.Trades)
			}
			if tc.heldIntent {
				intent, ok, err := st.GetReservationIntent(ctx, "approval-1")
				if err != nil || !ok {
					t.Fatalf("GetReservationIntent: %v ok=%v", err, ok)
				}
				if intent.State != tc.wantIntentState {
					t.Fatalf("intent state = %q, want %q", intent.State, tc.wantIntentState)
				}
				types := eventTypes(t, st, order.ExternalID)
				if len(types) != 1 || types[0] != tc.wantEvent {
					t.Fatalf("events = %+v, want [%s]", types, tc.wantEvent)
				}
			}

			quote, ok, err := st.GetBalance(ctx, id, "USD")
			if err != nil {
				t.Fatalf("GetBalance quote: %v", err)
			}
			if tc.wantNoQuote {
				if ok {
					t.Fatalf("quote balance = %+v, want missing", quote)
				}
				return
			}
			if !ok {
				t.Fatalf("GetBalance quote ok=false")
			}
			if tc.heldIntent {
				wantAvailable := "10000"
				if len(tc.outcomes) == 0 {
					wantAvailable = "8000"
				}
				if quote.Available != wantAvailable {
					t.Fatalf("quote available = %q, want %s", quote.Available, wantAvailable)
				}
			} else if len(tc.outcomes) > 0 && tc.outcomes[0].Asset == "USD" &&
				quote.Available != "10000" {
				t.Fatalf("quote available = %q, want 10000", quote.Available)
			}
			if tc.heldIntent {
				wantHeld := "0"
				if len(tc.outcomes) == 0 {
					wantHeld = "2000"
				}
				if quote.Held != wantHeld {
					t.Fatalf("quote held = %q, want %s", quote.Held, wantHeld)
				}
			}
			if tc.wantBlocked {
				acc, ok, err := st.GetAccount(ctx, id)
				if err != nil || !ok {
					t.Fatalf("GetAccount: %v ok=%v", err, ok)
				}
				if !acc.Blocked || !strings.Contains(acc.BlockReason, "engine rejected report") {
					t.Fatalf("account block = %+v, want engine reason", acc)
				}
			}
		})
	}
}

func TestLocalNode_ApplyExecutionReportUsesTargetedReservationLookup(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	baseStore := newMemoryStore("node.db")
	ctx := context.Background()
	if err := baseStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = baseStore.Close() })

	wrapped := newRealmWrapStore(baseStore, func(inner store.RealmStore) store.RealmStore {
		return &noFullScanReservationRealm{RealmStore: inner}
	})
	n := newTestNodeWithStore(t, wrapped, eng)
	realm := wrapped.realm.(*noFullScanReservationRealm)

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order, err := realm.CreateOrder(ctx, domain.Order{
		Account:     id,
		Source:      domain.SourceAPI,
		Principal:   "operator",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "400",
		Status:      domain.OrderStatusAccepted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if err := realm.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID: "approval-1",
		Order:      order.ExternalID,
		Account:    order.Account,
		ParamsJSON: `{"id":1}`,
		IssuedAt:   time.Now().UTC(),
		State:      domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}

	realm.forbidFullScans()
	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		OrderStatus:    domain.OrderStatusCancelled,
		LeavesQuantity: "0",
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	targeted, fullScans := realm.lookupCounts()
	if targeted != 1 || fullScans != 0 {
		t.Fatalf("reservation lookups targeted=%d fullScans=%d, want 1/0", targeted, fullScans)
	}
	if len(eng.rollbackHeldCalls) != 0 || len(eng.commitHeldCalls) != 0 {
		t.Fatalf("commit=%+v rollback=%+v, want no explicit resolve calls",
			eng.commitHeldCalls, eng.rollbackHeldCalls)
	}
}

func TestLocalNode_ApplyExecutionReportNilPersistenceErrors(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.emptyExecReportPersistence = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	_, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "0",
		OrderStatus:    domain.OrderStatusFilled,
	}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyExecutionReport nil persistence = %v, want ErrInvalid", err)
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != order.Status || detail.Order.Leaves != order.Leaves {
		t.Fatalf("order = %+v, want status/leaves unchanged from %+v",
			detail.Order, order)
	}
	if len(detail.Events) != 0 || len(detail.Trades) != 0 {
		t.Fatalf("detail = %+v, want no events or trades", detail)
	}
}

// TestLocalNode_ApplyExecutionReportLeavesFollowsFills verifies the persisted
// remaining open quantity starts from the stored order request value, follows a
// partial fill's reported leaves, and reaches "0" on the final fill.
func TestLocalNode_ApplyExecutionReportLeavesFollowsFills(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)
	if order.Leaves != "" {
		t.Fatalf("initial leaves = %q, want request value", order.Leaves)
	}

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "1",
		OrderStatus:    domain.OrderStatusPartiallyFilled,
	}, testCaller); err != nil {
		t.Fatalf("partial fill: %v", err)
	}
	partial, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder partial: %v", err)
	}
	if partial.Order.Status != domain.OrderStatusPartiallyFilled {
		t.Fatalf("status = %q, want partially_filled", partial.Order.Status)
	}
	if partial.Order.Leaves != "1" {
		t.Fatalf("leaves after partial = %q, want 1", partial.Order.Leaves)
	}

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "0",
		OrderStatus:    domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("final fill: %v", err)
	}
	filled, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder final: %v", err)
	}
	if filled.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("status = %q, want filled", filled.Order.Status)
	}
	if filled.Order.Leaves != "0" {
		t.Fatalf("leaves after final = %q, want 0", filled.Order.Leaves)
	}
}

func TestLocalNode_ApplyExecutionReportRejectsInvalidStatusBeforeEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	_, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:        order.ExternalID,
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "1",
		FillPrice:    "400",
		LockPrice:    "400",
		Force:        true,
		OrderStatus:  domain.OrderStatus("bogus"),
	}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyExecutionReport invalid status = %v, want invalid", err)
	}
	if len(eng.execReportCalls) != 0 {
		t.Fatalf("invalid status reached engine: %+v", eng.execReportCalls)
	}
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != order.Status {
		t.Fatalf("order status = %q, want unchanged %q", detail.Order.Status, order.Status)
	}
}

// TestEngineBlockReason verifies the account block_reason composed for an
// engine-initiated block carries the engine reason plus the cause (code and
// triggering order), falling back to the code when no reason is given and
// appending details when present.
func TestEngineBlockReason(t *testing.T) {
	t.Parallel()
	order := mustExternalID(t)
	cases := []struct {
		name  string
		block domain.ExecutionAccountBlock
		want  string
	}{
		{
			name:  "reason and code",
			block: domain.ExecutionAccountBlock{Account: "acc-1", Code: "pnl_kill_switch", Reason: "loss limit breached"},
			want:  fmt.Sprintf("loss limit breached [code=pnl_kill_switch, order %s]", order),
		},
		{
			name:  "empty reason falls back to code",
			block: domain.ExecutionAccountBlock{Account: "acc-1", Code: "pnl_kill_switch"},
			want:  fmt.Sprintf("pnl_kill_switch [code=pnl_kill_switch, order %s]", order),
		},
		{
			name:  "details appended",
			block: domain.ExecutionAccountBlock{Account: "acc-1", Code: "pnl_kill_switch", Reason: "loss limit breached", Details: "upper bound 1000"},
			want:  fmt.Sprintf("loss limit breached [code=pnl_kill_switch, order %s, upper bound 1000]", order),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := engineBlockReason(order, tc.block); got != tc.want {
				t.Fatalf("engineBlockReason = %q, want %q", got, tc.want)
			}
		})
	}
}

// mustExternalID returns a fixed generated external id for the audit-detail
// tests that need a stable order handle to render.
func mustExternalID(t *testing.T) domain.ExternalID {
	t.Helper()
	id, err := domain.ParseExternalID("AAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatalf("ParseExternalID: %v", err)
	}
	return id
}

// externalID derives a deterministic, canonical external id from a short label
// so a test can supply a stable handle and reuse it (e.g. to provoke a
// duplicate).
func externalID(t *testing.T, label string) domain.ExternalID {
	t.Helper()
	id := domain.ExternalID(label)
	if err := domain.ValidateExternalID(id.String()); err != nil {
		t.Fatalf("externalID(%q) not canonical: %v", label, err)
	}
	return id
}

// TestNewLocalNode_SeedsBuildAndAudits verifies the constructor seeds the
// engine build from the store snapshot and writes one startup hydrate audit
// row. The store is pre-populated before the node is built so the seed is
// non-empty.
