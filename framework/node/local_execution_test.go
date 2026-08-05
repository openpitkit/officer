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
	"reflect"
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
		Account:          id,
		Source:           domain.SourceAPI,
		Principal:        "operator",
		BaseAsset:        "AAPL",
		QuoteAsset:       "USD",
		Side:             domain.OrderSideBuy,
		AmountKind:       domain.OrderAmountKindQuantity,
		AmountValue:      "2",
		Price:            "400",
		ReservedQuantity: "2",
		Status:           domain.OrderStatusCommitted,
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
				BalanceResult:     "800",
				RealizedPnlDelta:  "12.50",
				RealizedPnlResult: "12.5",
			},
		},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: id, Currency: "USD",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "2",
		FillPrice:      "400",
		LeavesQuantity: "0",
		LockPrice:      "400",
		OrderStatus:    domain.OrderStatusFilled,
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
	account, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "0" {
		t.Fatalf("account pnl = %q, want unchanged 0 without an account PnL outcome", account.Pnl)
	}
}

func TestLocalNode_ApplyExecutionReportLeavesAccountPnlUnchangedWithoutMatch(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.execReportOutcomes = []engine.BalanceOutcome{{
		Asset: "AAPL",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceResult: "2", RealizedPnlResult: "99",
		},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: id, Currency: "USD", Pnl: "7",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)
	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		FillQuantity:   "2",
		FillPrice:      "400",
		LeavesQuantity: "0",
		LockPrice:      "400",
		OrderStatus:    domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	account, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "7" {
		t.Fatalf("account pnl = %q, want unchanged 7", account.Pnl)
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
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "2",
		FillPrice:      "400",
		LeavesQuantity: "0",
		LockPrice:      "400",
		OrderStatus:    domain.OrderStatusFilled,
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

func TestLocalNode_ApplyExecutionReportDuplicateIDDoesNotReachEngineOrFatal(t *testing.T) {
	t.Parallel()
	st := newMemoryStore("node.db")
	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	ctx := context.Background()
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	firstOrder := testOrder(t, realm, "acc-1")
	secondOrder := firstOrder
	secondOrder.ExternalID = ""
	secondOrder.At = time.Time{}
	secondOrder, err = realm.CreateOrder(ctx, secondOrder)
	if err != nil {
		t.Fatalf("CreateOrder second: %v", err)
	}

	input := domain.ExecutionReportInput{
		Order:          firstOrder.ExternalID,
		FillQuantity:   "2",
		FillPrice:      "400",
		LeavesQuantity: "0",
		LockPrice:      "400",
		OrderStatus:    domain.OrderStatusFilled,
	}
	first, err := n.ApplyExecutionReport(ctx, testKey("acc-1"), input, testCaller)
	if err != nil {
		t.Fatalf("ApplyExecutionReport first: %v", err)
	}
	if first.ReportID.IsZero() {
		t.Fatal("first report id is zero")
	}

	input.Order = secondOrder.ExternalID
	input.ExternalID = first.ReportID
	_, err = n.ApplyExecutionReport(ctx, testKey("acc-1"), input, testCaller)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate ApplyExecutionReport = %v, want ErrAlreadyExists", err)
	}
	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine report calls = %d, want one", len(eng.execReportCalls))
	}
	if fatalErr != nil {
		t.Fatalf("duplicate report triggered fatal hook: %v", fatalErr)
	}
}

func TestLocalNode_ApplyExecutionReportWorkflowAuditFailureFatalsAfterCommit(
	t *testing.T,
) {
	t.Parallel()
	auditErr := errors.New("workflow execution report audit failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failActionAuditRealm{
			RealmStore: r,
			action:     domain.AuditActionExecutionReport,
			err:        auditErr,
		}
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
	realm := st.realm
	order := testOrder(t, realm, "acc-1")

	_, err := n.ApplyExecutionReport(ctx, testKey("acc-1"), domain.ExecutionReportInput{
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusAccepted,
	}, testCaller)
	if !errors.Is(err, auditErr) {
		t.Fatalf("ApplyExecutionReport error = %v, want audit failure", err)
	}
	if len(eng.execReportCalls) != 0 {
		t.Fatalf("workflow status reached engine: %+v", eng.execReportCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire after committed workflow audit failure")
	}
	account, ok, err := realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="audit workflow execution report"`) ||
		!strings.Contains(msg, fmt.Sprintf("account_id=%d", account.EngineAccountID.Uint64())) ||
		!strings.Contains(msg, "post-commit audit failure") ||
		!strings.Contains(msg, auditErr.Error()) {
		t.Fatalf("fatal error = %q, want operation, account_id, and cause", msg)
	}
	detail, err := realm.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusAccepted || len(detail.Events) != 1 {
		t.Fatalf("committed workflow settlement = %+v", detail)
	}
}

func TestLocalNode_ApplyExecutionReportDeletesComputedZeroSettledPosition(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.execReportOutcomes = []engine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceDelta:      "-100.00",
			BalanceResult:     "0",
			RealizedPnlDelta:  "0",
			RealizedPnlResult: "0",
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
		FillQuantity:   "1",
		FillPrice:      "100",
		LeavesQuantity: "0",
		OrderStatus:    domain.OrderStatusFilled,
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if _, ok, err := st.GetBalance(ctx, "acc-1", "USD"); err != nil || ok {
		t.Fatalf(
			"GetBalance after computed zero settlement: ok=%v err=%v, want missing",
			ok,
			err,
		)
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
		FillQuantity:   "1",
		FillPrice:      "100",
		LeavesQuantity: "0",
		OrderStatus:    domain.OrderStatusFilled,
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
		Order:          order.ExternalID,
		BaseAsset:      "GOLD",
		QuoteAsset:     "EUR",
		Side:           domain.OrderSideSell,
		FillQuantity:   "2",
		FillPrice:      "400",
		LeavesQuantity: "0",
		LockPrice:      "400",
		OrderStatus:    domain.OrderStatusFilled,
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
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}

	if _, err := n.ApplyExecutionReport(ctx, testKey("fresh-report"), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		FillQuantity:   "2",
		FillPrice:      "100",
		LeavesQuantity: "0",
		LockPrice:      "100",
		OrderStatus:    domain.OrderStatusFilled,
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
		Order:          order.ExternalID,
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "2",
		FillPrice:      "400",
		LeavesQuantity: "0",
		LockPrice:      "400",
		OrderStatus:    domain.OrderStatusFilled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}

	acc, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: %v ok=%v", err, ok)
	}
	// The account keeps the engine's own reason, verbatim: the reject code and the
	// triggering order are carried by the block audit line, not composed into the
	// stored reason.
	if !acc.Blocked || acc.BlockReason != "account block triggered" {
		t.Fatalf("account block reason = %q, want the engine reason", acc.BlockReason)
	}

	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(detail.Events) != 1 {
		t.Fatalf("events = %d, want 1 fill event", len(detail.Events))
	}
	if detail.Order.ReservedQuantity != "2" || len(detail.Trades) != 1 {
		t.Fatalf(
			"blocked non-mutating settlement = %+v, want trade with reserve 2",
			detail,
		)
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
	if !strings.Contains(rows[0].Detail, "account block triggered") ||
		!strings.Contains(rows[0].Detail, "code=test_block") ||
		!strings.Contains(rows[0].Detail, order.ExternalID.String()) {
		t.Fatalf("block detail missing reason, code or order: %q", rows[0].Detail)
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

func TestLocalNode_RepeatedForcedTerminalUsesZeroReservation(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.execReportReservedQuantity = "0"
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	order := testOrder(t, st, "acc-1")

	report := domain.ExecutionReportInput{
		Order:          order.ExternalID,
		LeavesQuantity: "3",
		OrderStatus:    domain.OrderStatusCancelled,
	}
	if _, err := n.ApplyExecutionReport(
		ctx, testKey("acc-1"), report, testCaller,
	); err != nil {
		t.Fatalf("first terminal report: %v", err)
	}
	report.Force = true
	if _, err := n.ApplyExecutionReport(
		ctx, testKey("acc-1"), report, testCaller,
	); err != nil {
		t.Fatalf("repeated forced terminal report: %v", err)
	}
	if len(eng.execReportCalls) != 2 {
		t.Fatalf("engine calls = %+v, want two", eng.execReportCalls)
	}
	if eng.execReportCalls[0].ReservedQuantity != "2" ||
		eng.execReportCalls[1].ReservedQuantity != "0" {
		t.Fatalf("reservation contexts = %+v, want 2 then 0", eng.execReportCalls)
	}
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Leaves != "3" || detail.Order.ReservedQuantity != "0" {
		t.Fatalf("terminal order = %+v, want raw leaves 3 and zero reserve", detail.Order)
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

func TestLocalNode_ApplyExecutionReportForceWorkflowAuditsBypass(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	order := testOrder(t, st, id)
	if err := st.UpdateOrderStatus(
		ctx, order.ExternalID, domain.OrderStatusFilled,
	); err != nil {
		t.Fatalf("UpdateOrderStatus: %v", err)
	}

	if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:       order.ExternalID,
		Force:       true,
		OrderStatus: domain.OrderStatusAccepted,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport force workflow: %v", err)
	}
	if len(eng.execReportCalls) != 0 {
		t.Fatalf("workflow status reached engine: %+v", eng.execReportCalls)
	}

	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionExecutionReport},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "forced=true") {
		t.Fatalf("audit rows = %+v, want forced=true workflow report", rows)
	}
}

// TestLocalNode_ApplyExecutionReportStatusOnlyNoAccountWrites verifies a
// workflow-only report records its status without creating balances, blocks, or
// engine execution-report calls.
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
		BaseAsset:   "UNUSED_BASE",
		QuoteAsset:  "UNUSED_QUOTE",
		Side:        domain.OrderSideBuy,
		OrderStatus: domain.OrderStatusCommitted,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport status-only: %v", err)
	}
	if len(eng.execReportCalls) != 0 {
		t.Fatalf("workflow status reached engine: %+v", eng.execReportCalls)
	}

	if _, ok, err := st.GetBalance(ctx, id, "AAPL"); err != nil || ok {
		t.Fatalf("base balance ok=%v err=%v, want no balance row written", ok, err)
	}
	if _, ok, err := st.GetBalance(ctx, id, "USD"); err != nil || ok {
		t.Fatalf("quote balance ok=%v err=%v, want no balance row written", ok, err)
	}
	if _, ok, err := st.GetAsset(ctx, "UNUSED_BASE"); err != nil || ok {
		t.Fatalf("report base asset ok=%v err=%v, want no asset registration", ok, err)
	}
	if _, ok, err := st.GetAsset(ctx, "UNUSED_QUOTE"); err != nil || ok {
		t.Fatalf("report quote asset ok=%v err=%v, want no asset registration", ok, err)
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
	if detail.Order.Status != domain.OrderStatusCommitted {
		t.Fatalf("status = %q, want committed (the venue-owned status change is recorded)",
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

// TestLocalNode_ApplyExecutionReportRoutesFillableStatusesThroughEngine verifies
// a report carrying a fill routes through the engine for every status that
// legally accepts one: the fill statuses and the terminal statuses.
func TestLocalNode_ApplyExecutionReportRoutesFillableStatusesThroughEngine(t *testing.T) {
	t.Parallel()
	cases := []domain.OrderStatus{
		domain.OrderStatusRejected,
		domain.OrderStatusRolledBack,
		domain.OrderStatusFilled,
		domain.OrderStatusPartiallyFilled,
		domain.OrderStatusCancelled,
	}
	for _, status := range cases {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			n, st := newTestNode(t, eng)
			ctx := context.Background()

			const id domain.AccountID = "acc-1"
			order := testOrder(t, st, id)
			if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
				Order:          order.ExternalID,
				FillQuantity:   "1",
				FillPrice:      "400",
				LeavesQuantity: "1",
				OrderStatus:    status,
			}, testCaller); err != nil {
				t.Fatalf("ApplyExecutionReport: %v", err)
			}
			if len(eng.execReportCalls) != 1 {
				t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
			}
			detail, err := st.GetOrder(ctx, order.ExternalID)
			if err != nil {
				t.Fatalf("GetOrder: %v", err)
			}
			// Every status, terminal included, records the leaves the report
			// carried: the report is filed as it arrived.
			if detail.Order.Leaves != "1" {
				t.Fatalf(
					"order leaves = %q, want reported 1 for status %q",
					detail.Order.Leaves,
					status,
				)
			}
		})
	}
}

// TestLocalNode_ApplyExecutionReportRejectsFillWithNonTerminalWorkflowStatus
// verifies a report carrying a fill is rejected before it reaches the engine
// when paired with a non-terminal workflow status: settling such a report
// through the engine while persisting an inconsistent lifecycle status would
// desync the order from its balances.
func TestLocalNode_ApplyExecutionReportRejectsFillWithNonTerminalWorkflowStatus(t *testing.T) {
	t.Parallel()
	cases := []domain.OrderStatus{
		domain.OrderStatusSubmitted,
		domain.OrderStatusAccepted,
		domain.OrderStatusCommitted,
	}
	for _, status := range cases {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			eng.execReportReservedQuantity = "0"
			n, st := newTestNode(t, eng)
			ctx := context.Background()

			const id domain.AccountID = "acc-1"
			order := testOrder(t, st, id)
			_, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
				Order:          order.ExternalID,
				FillQuantity:   "1",
				FillPrice:      "400",
				LeavesQuantity: "1",
				OrderStatus:    status,
			}, testCaller)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("ApplyExecutionReport = %v, want ErrInvalid", err)
			}
			if len(eng.execReportCalls) != 0 {
				t.Fatalf("fill with non-terminal workflow status reached engine: %+v",
					eng.execReportCalls)
			}
		})
	}
}

func TestLocalNode_ApplyExecutionReportRoutesTerminalReportsThroughEngine(t *testing.T) {
	t.Parallel()
	cases := []domain.OrderStatus{
		domain.OrderStatusRejected,
		domain.OrderStatusRolledBack,
		domain.OrderStatusCancelled,
	}
	for _, status := range cases {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			n, st := newTestNode(t, eng)
			ctx := context.Background()

			const id domain.AccountID = "acc-1"
			order := testOrder(t, st, id)
			commission := &domain.Commission{Amount: "-0.25", Currency: "USD"}
			if _, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
				Order:          order.ExternalID,
				LeavesQuantity: "2",
				Commission:     commission,
				OrderStatus:    status,
			}, testCaller); err != nil {
				t.Fatalf("ApplyExecutionReport: %v", err)
			}
			if len(eng.execReportCalls) != 1 {
				t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
			}
			input := eng.execReportCalls[0]
			if input.LeavesQuantity != "2" || input.ReservedQuantity != "2" {
				t.Fatalf("engine reservation context = %+v", input)
			}
			detail, err := st.GetOrder(ctx, order.ExternalID)
			if err != nil {
				t.Fatalf("GetOrder: %v", err)
			}
			// The fake reports no applied base delta, so terminal status alone does
			// not consume the persisted reserve. Leaves still stays venue-reported.
			if detail.Order.Status != status || detail.Order.Leaves != "2" ||
				detail.Order.ReservedQuantity != "2" {
				t.Fatalf("order = %+v, want %s with reported leaves and reserve 2", detail.Order, status)
			}
			if len(detail.Events) != 1 {
				t.Fatalf("events = %+v, want one terminal event", detail.Events)
			}
			payload := detail.Events[0].Payload
			if payload.LeavesQuantity != "2" {
				t.Fatalf("event leaves = %q, want original 2", payload.LeavesQuantity)
			}
			if payload.Commission == nil || payload.Commission.Amount != "-0.25" ||
				payload.Commission.Currency != "USD" {
				t.Fatalf("event commission = %+v, want -0.25/USD", payload.Commission)
			}
			if payload.ExecutionReport == nil ||
				payload.ExecutionReport.Commission == nil {
				t.Fatalf("original report lost commission: %+v", payload.ExecutionReport)
			}
		})
	}
}

func TestLocalNode_ApplyExecutionReportRegistersThirdCommissionAsset(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.execReportOutcomes = []engine.BalanceOutcome{{
		Asset: "BNB",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceResult: "9.99",
		},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	order := testOrder(t, st, "acc-1")

	if _, ok, err := st.GetAsset(ctx, "BNB"); err != nil || ok {
		t.Fatalf("GetAsset(BNB) before report = ok %v err %v, want absent", ok, err)
	}
	if _, err := n.ApplyExecutionReport(ctx, testKey("acc-1"), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		LeavesQuantity: "2",
		Commission:     &domain.Commission{Amount: "-0.01", Currency: "BNB"},
		OrderStatus:    domain.OrderStatusCommitted,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport(fee only): %v", err)
	}

	if _, ok, err := st.GetAsset(ctx, "BNB"); err != nil || !ok {
		t.Fatalf("GetAsset(BNB) after report = ok %v err %v, want registered", ok, err)
	}
	balance, ok, err := st.GetBalance(ctx, "acc-1", "BNB")
	if err != nil || !ok {
		t.Fatalf("GetBalance(BNB) = ok %v err %v, want persisted outcome", ok, err)
	}
	if balance.Available != "9.99" {
		t.Fatalf("BNB available = %q, want 9.99", balance.Available)
	}
	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
	}
	got := eng.execReportCalls[0]
	if got.FillQuantity != "" || got.FillPrice != "" || got.Commission == nil ||
		got.Commission.Amount != "-0.01" || got.Commission.Currency != "BNB" {
		t.Fatalf("engine fee-only report = %+v", got)
	}
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(detail.Trades) != 0 {
		t.Fatalf("fee-only report trades = %+v, want none", detail.Trades)
	}
}

func TestLocalNode_ApplyExecutionReportPersistsAuditSafeOriginalRequest(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order := testOrder(t, st, "acc-1")
	in := domain.ExecutionReportInput{
		BaseAsset:      "caller-base",
		QuoteAsset:     "caller-quote",
		FillQuantity:   "1.25",
		FillPrice:      "101.50",
		LeavesQuantity: "2.75",
		LockPrice:      "100.25",
		Lock:           []byte{0x00, 0x7f, 0xff},
		Commission:     &domain.Commission{Amount: "-0.01", Currency: "EUR"},
		Order:          order.ExternalID,
		Account:        "caller-account",
		Side:           domain.OrderSideSell,
		OrderStatus:    domain.OrderStatusPartiallyFilled,
		Force:          true,
	}
	want := domain.ExecutionReportRequestFromInput(in)
	result, err := n.ApplyExecutionReport(ctx, testKey("acc-1"), in, testCaller)
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(detail.Events) != 1 || detail.Events[0].Type != domain.OrderEventFill {
		t.Fatalf("events = %+v, want one fill event", detail.Events)
	}
	got := detail.Events[0].Payload.ExecutionReport
	if got == nil || got.ExternalID.IsZero() {
		t.Fatalf("execution report snapshot has no generated id: %+v", got)
	}
	if result.ReportID != got.ExternalID {
		t.Fatalf("result id = %q, request id = %q", result.ReportID, got.ExternalID)
	}
	if detail.Events[0].ExternalID == got.ExternalID {
		t.Fatalf("event id reused report id %q", got.ExternalID)
	}
	want.ExternalID = got.ExternalID
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("execution report snapshot = %+v, want %+v", got, want)
	}
	if len(eng.execReportCalls) != 1 ||
		eng.execReportCalls[0].Account != order.Account ||
		eng.execReportCalls[0].BaseAsset != order.BaseAsset ||
		eng.execReportCalls[0].QuoteAsset != order.QuoteAsset ||
		eng.execReportCalls[0].Side != order.Side ||
		len(eng.execReportCalls[0].Lock) == 0 {
		t.Fatalf("engine input was not enriched from order: %+v", eng.execReportCalls)
	}
}

func TestLocalNode_ApplyExecutionReportPersistsWorkflowStatusesWithoutEngine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status domain.OrderStatus
		event  domain.OrderEventType
	}{
		{domain.OrderStatusSubmitted, domain.OrderEventSubmitted},
		{domain.OrderStatusAccepted, domain.OrderEventPreTradeAccepted},
		{domain.OrderStatusCommitted, domain.OrderEventCommitted},
	}
	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			n, st := newTestNode(t, eng)
			ctx := context.Background()
			reportID := domain.ExternalID("workflow-report-" + string(tc.status))

			const id domain.AccountID = "acc-1"
			order := testOrder(t, st, id)
			result, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
				ExternalID:  reportID,
				Order:       order.ExternalID,
				OrderStatus: tc.status,
			}, testCaller)
			if err != nil {
				t.Fatalf("ApplyExecutionReport: %v", err)
			}
			if len(result.Blocks) != 0 || len(result.Outcomes) != 0 || result.Persistence != nil {
				t.Fatalf("result = %+v, want no engine result", result)
			}
			if result.ReportID != reportID {
				t.Fatalf("result report id = %q, want %q", result.ReportID, reportID)
			}
			if len(eng.execReportCalls) != 0 {
				t.Fatalf("workflow status reached engine: %+v", eng.execReportCalls)
			}
			if len(eng.accountSyncCalls) != 1 || eng.accountSyncCalls[0] != id {
				t.Fatalf("account sync calls = %+v, want [%s]", eng.accountSyncCalls, id)
			}
			detail, err := st.GetOrder(ctx, order.ExternalID)
			if err != nil {
				t.Fatalf("GetOrder: %v", err)
			}
			if detail.Order.Status != tc.status {
				t.Fatalf("order status = %q, want %q", detail.Order.Status, tc.status)
			}
			if detail.Order.Leaves != "" {
				t.Fatalf("leaves = %q, want unchanged empty", detail.Order.Leaves)
			}
			if len(detail.Events) != 1 || detail.Events[0].Type != tc.event {
				t.Fatalf("events = %+v, want [%s]", detail.Events, tc.event)
			}
			request := detail.Events[0].Payload.ExecutionReport
			if request == nil || request.ExternalID != reportID {
				t.Fatalf("event report snapshot = %+v, want id %q", request, reportID)
			}
			if detail.Events[0].ExternalID == reportID {
				t.Fatalf("event reused report id %q", reportID)
			}
		})
	}
}

func TestLocalNode_ApplyExecutionReportRejectsInvalidWorkflowReport(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   domain.ExecutionReportInput
	}{
		{
			name: "lock price",
			in: domain.ExecutionReportInput{
				LockPrice:   "400",
				OrderStatus: domain.OrderStatusSubmitted,
			},
		},
		{
			name: "opaque lock",
			in: domain.ExecutionReportInput{
				Lock:        []byte{1},
				OrderStatus: domain.OrderStatusAccepted,
			},
		},
		{
			name: "commission with lock price",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "1",
				LockPrice:      "400",
				Commission: &domain.Commission{
					Amount:   "-1",
					Currency: "USD",
				},
				OrderStatus: domain.OrderStatusCommitted,
			},
		},
		{
			name: "malformed leaves",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "not-a-number",
				OrderStatus:    domain.OrderStatusCommitted,
			},
		},
		{
			name: "negative leaves",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "-1",
				OrderStatus:    domain.OrderStatusAccepted,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			n, st := newTestNode(t, eng)
			ctx := context.Background()
			order := testOrder(t, st, "acc-1")
			in := tc.in
			in.Order = order.ExternalID

			if _, err := n.ApplyExecutionReport(ctx, testKey("acc-1"), in, testCaller); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("ApplyExecutionReport error = %v, want ErrInvalid", err)
			}
			if len(eng.execReportCalls) != 0 || len(eng.accountSyncCalls) != 0 {
				t.Fatalf(
					"invalid workflow report reached account pipeline: engine=%+v sync=%+v",
					eng.execReportCalls,
					eng.accountSyncCalls,
				)
			}
			detail, err := st.GetOrder(ctx, order.ExternalID)
			if err != nil {
				t.Fatalf("GetOrder: %v", err)
			}
			if detail.Order.Status != order.Status || len(detail.Events) != 0 {
				t.Fatalf("invalid workflow report mutated order: %+v", detail)
			}
		})
	}
}

func TestLocalNode_ApplyExecutionReportWorkflowLeavesStaysDBOnly(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	order := testOrder(t, st, "acc-1")

	if _, err := n.ApplyExecutionReport(ctx, testKey("acc-1"), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		LeavesQuantity: "2",
		OrderStatus:    domain.OrderStatusAccepted,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if len(eng.execReportCalls) != 0 {
		t.Fatalf("workflow leaves reached engine: %+v", eng.execReportCalls)
	}
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Leaves != "2" {
		t.Fatalf("order leaves = %q, want reported 2", detail.Order.Leaves)
	}
	if len(detail.Events) != 1 || detail.Events[0].Payload.LeavesQuantity != "2" {
		t.Fatalf("events = %+v, want reported leaves 2", detail.Events)
	}
}

// The engine's post-trade path has no reject channel, so a report settled by
// the engine without leaves must be refused before the account pipeline.
func TestLocalNode_ApplyExecutionReportRequiresLeavesForEngineReports(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   domain.ExecutionReportInput
	}{
		{
			name: "terminal",
			in: domain.ExecutionReportInput{
				OrderStatus: domain.OrderStatusCancelled,
			},
		},
		{
			name: "fill",
			in: domain.ExecutionReportInput{
				FillQuantity: "1",
				FillPrice:    "100",
				OrderStatus:  domain.OrderStatusFilled,
			},
		},
		{
			name: "commission",
			in: domain.ExecutionReportInput{
				Commission: &domain.Commission{
					Amount:   "-1",
					Currency: "USD",
				},
				OrderStatus: domain.OrderStatusAccepted,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			n, st := newTestNode(t, eng)
			ctx := context.Background()
			order := testOrder(t, st, "acc-1")
			in := tc.in
			in.Order = order.ExternalID

			if _, err := n.ApplyExecutionReport(
				ctx, testKey("acc-1"), in, testCaller,
			); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("ApplyExecutionReport error = %v, want ErrInvalid", err)
			}
			if len(eng.execReportCalls) != 0 || len(eng.accountSyncCalls) != 0 {
				t.Fatalf(
					"report without leaves reached account pipeline: engine=%+v sync=%+v",
					eng.execReportCalls,
					eng.accountSyncCalls,
				)
			}
		})
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

	eng.execReportReservedQuantity = "1"
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
	if partial.Order.ReservedQuantity != "1" {
		t.Fatalf("reserve after partial = %q, want applied remainder 1", partial.Order.ReservedQuantity)
	}

	eng.execReportReservedQuantity = "0"
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
	if filled.Order.ReservedQuantity != "0" {
		t.Fatalf("reserve after final = %q, want 0", filled.Order.ReservedQuantity)
	}
	if len(eng.execReportCalls) != 2 {
		t.Fatalf("engine calls = %+v, want two", eng.execReportCalls)
	}
	terminal := eng.execReportCalls[1]
	if terminal.ReservedQuantity != "1" {
		t.Fatalf("terminal reservation context = %+v", terminal)
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

func TestLocalNode_ApplyExecutionReportRejectsFillOnWorkflowStatus(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	order := testOrder(t, st, "acc-1")
	_, err := n.ApplyExecutionReport(ctx, testKey("acc-1"), domain.ExecutionReportInput{
		Order:        order.ExternalID,
		FillQuantity: "1",
		FillPrice:    "400",
		OrderStatus:  domain.OrderStatusAccepted,
	}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyExecutionReport = %v, want invalid", err)
	}
	if len(eng.execReportCalls) != 0 {
		t.Fatalf("workflow fill reached engine: %+v", eng.execReportCalls)
	}
}

func TestLocalNode_ApplyExecutionReportRoutesTerminalFillThroughEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	order := testOrder(t, st, id)
	_, err := n.ApplyExecutionReport(ctx, testKey(id), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		FillQuantity:   "1",
		FillPrice:      "400",
		LeavesQuantity: "1",
		OrderStatus:    domain.OrderStatusCancelled,
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyExecutionReport terminal fill: %v", err)
	}
	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
	}
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(detail.Events) != 2 ||
		detail.Events[0].Type != domain.OrderEventFill ||
		detail.Events[1].Type != domain.OrderEventCancelled {
		t.Fatalf("events = %+v, want [fill cancelled]", detail.Events)
	}
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
