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

func TestOrderRejectedSettlementPreservesOrderedRejects(t *testing.T) {
	t.Parallel()
	rejects := []domain.OrderReject{
		{Code: "rate", Scope: "account", Policy: "rate", Details: "first"},
		{Code: "size", Scope: "order", Policy: "size", Details: "second"},
	}
	settlement := orderRejectedSettlement(
		testKey("acc-1"), domain.Order{ExternalID: "order-1"}, rejects, testCaller,
	)
	if len(settlement.Events) != 1 {
		t.Fatalf("events = %+v, want one reject event", settlement.Events)
	}
	got := settlement.Events[0].Payload.Rejects
	if len(got) != len(rejects) {
		t.Fatalf("rejects = %+v, want %+v", got, rejects)
	}
	for i := range rejects {
		if got[i] != rejects[i] {
			t.Fatalf("reject %d = %+v, want %+v", i, got[i], rejects[i])
		}
	}
}

// A pre-trade reject reserved nothing, so the submit records a zero open
// quantity rather than an empty one. The empty value would read as "unknown"
// and make a later forced terminal report demand a leaves the order never had.
func TestLocalNode_SubmitOrderRejectedRecordsZeroLeaves(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitReject = &domain.OrderReject{
		Code: "limit", Scope: "account", Policy: "order-size",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Status != domain.OrderStatusRejected {
		t.Fatalf("order status = %q, want rejected", order.Status)
	}
	if order.Leaves != "0" {
		t.Fatalf("rejected order leaves = %q, want a recorded zero", order.Leaves)
	}
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Leaves != "0" {
		t.Fatalf("stored leaves = %q, want a recorded zero", detail.Order.Leaves)
	}

	// The recorded zero preserves the engine's answer. A later forced terminal
	// no-fill report sends that pre-report value back without caller leaves.
	if _, err := n.ApplyExecutionReport(ctx, testKey("acc-1"), domain.ExecutionReportInput{
		Order:       order.ExternalID,
		Force:       true,
		OrderStatus: domain.OrderStatusCancelled,
	}, testCaller); err != nil {
		t.Fatalf("forced terminal report after reject: %v", err)
	}
	if len(eng.execReportLeaves) != 1 || eng.execReportLeaves[0] != "0" {
		t.Fatalf("engine terminal leaves = %+v, want recorded zero", eng.execReportLeaves)
	}
}

// TestLocalNode_SubmitOrderPersistsPreTradeBalances verifies the direct submit
// path mirrors the engine's balance effects into the snapshot, so held funds
// and incoming quantity show up before any fill settles.
func TestLocalNode_SubmitOrderPersistsPreTradeBalances(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitLock = []byte("lock")
	eng.submitOutcomes = []engine.BalanceOutcome{
		{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceResult: "8000",
				HeldDelta:     "2000",
				HeldResult:    "2000",
			},
		},
		{
			Asset: "AAPL",
			Outcome: domain.AdjustmentOutcomeAccepted{
				IncomingDelta:  "20",
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
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("order status = %q, want committed", order.Status)
	}
	if order.Leaves != "20" {
		t.Fatalf("order leaves = %q, want engine delta 20", order.Leaves)
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

func TestOpeningLeavesComeFromEngineBaseDelta(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		side     domain.OrderSide
		outcomes []engine.BalanceOutcome
		want     string
		wantErr  bool
	}{
		{
			name: "buy incoming", side: domain.OrderSideBuy,
			outcomes: []engine.BalanceOutcome{
				{
					Asset:   "USD",
					Outcome: domain.AdjustmentOutcomeAccepted{HeldDelta: "200"},
				},
				{
					Asset: "AAPL",
					Outcome: domain.AdjustmentOutcomeAccepted{
						HeldDelta: "999", IncomingDelta: "2",
					},
				},
			},
			want: "2",
		},
		{
			name: "sell held", side: domain.OrderSideSell,
			outcomes: []engine.BalanceOutcome{{
				Asset: "AAPL",
				Outcome: domain.AdjustmentOutcomeAccepted{
					HeldDelta: "2", IncomingDelta: "999",
				},
			}},
			want: "2",
		},
		{name: "missing base delta", side: domain.OrderSideBuy, want: "0"},
		{
			name: "unsupported side", side: domain.OrderSide("swap"),
			outcomes: []engine.BalanceOutcome{{
				Asset:   "AAPL",
				Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "2"},
			}},
			wantErr: true,
		},
		{
			name: "several base deltas", side: domain.OrderSideBuy,
			outcomes: []engine.BalanceOutcome{
				{
					Asset:   "AAPL",
					Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "2"},
				},
				{
					Asset:   "AAPL",
					Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "3"},
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := openingLeavesFromOutcomes(domain.Order{
				BaseAsset: "AAPL",
				Side:      tt.side,
			}, tt.outcomes)
			if tt.wantErr {
				if err == nil {
					t.Fatal("openingLeavesFromOutcomes accepted an unusable engine outcome")
				}
				if errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("engine outcome error = %v, want internal error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("openingLeavesFromOutcomes: %v", err)
			}
			if got != tt.want {
				t.Fatalf("opening leaves = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLocalNode_SubmitImmediateNilPersistenceIsInternal(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.emptyImmediatePersistence = true
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	_, _, err := n.SubmitImmediate(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err == nil {
		t.Fatal("SubmitImmediate succeeded with no persistence write set")
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SubmitImmediate error = %v, want internal error", err)
	}
	if len(eng.submitCalls) != 1 {
		t.Fatalf("submit calls = %+v, want one engine apply", eng.submitCalls)
	}
}

// An accepted engine delta sets opening leaves; execution reports later replace
// them with the venue's reported value unchanged.
func TestLocalNode_VolumeOrderRecordsEngineOpeningLeaves(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitLock = []byte("stored-lock")
	eng.submitOutcomes = []engine.BalanceOutcome{{
		Asset:   "AAPL",
		Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "5"},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindVolume, AmountValue: "500", Price: "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Leaves != "5" {
		t.Fatalf("volume order leaves = %q, want engine delta 5", order.Leaves)
	}

	if _, err := n.ApplyExecutionReport(ctx, testKey("acc-1"),
		domain.ExecutionReportInput{
			Order: order.ExternalID, FillQuantity: "2", FillPrice: "100",
			LeavesQuantity: "3", OrderStatus: domain.OrderStatusPartiallyFilled,
		}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Leaves != "3" {
		t.Fatalf("reported leaves = %q, want 3", detail.Order.Leaves)
	}
}

func TestLocalNode_CancelVolumeOrderUsesPreReportLeaves(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitLock = []byte("stored-lock")
	eng.submitOutcomes = []engine.BalanceOutcome{{
		Asset:   "AAPL",
		Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "5"},
	}}
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindVolume, AmountValue: "500", Price: "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Leaves != "5" {
		t.Fatalf("volume order leaves = %q, want engine delta 5", order.Leaves)
	}

	if _, _, err := n.CancelOrder(ctx, order.ExternalID, "0", testCaller); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
	}
	if got := eng.execReportLeaves[0]; got != "5" {
		t.Fatalf("cancellation selected leaves = %q, want stored leaves 5", got)
	}
}

// An engine block does not make Officer derive, withhold, or annotate a
// different pre-report cancellation leaves value.
func TestLocalNode_CancelBlockOnlyResultAuditsPreReportLeaves(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.execReportBlocks = []domain.ExecutionAccountBlock{
		{Account: "acc-1", Code: "test_block", Reason: "account block triggered"},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	order := testOrder(t, st, "acc-1")

	if _, _, err := n.CancelOrder(
		ctx, order.ExternalID, "0", testCaller,
	); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
	}
	if got := eng.execReportLeaves[0]; got != "2" {
		t.Fatalf("cancellation selected leaves = %q, want stored leaves 2", got)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionExecutionReport},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(execution report): %v", err)
	}
	if len(rows) != 1 ||
		!strings.Contains(rows[0].Detail, "leavesQty=0") ||
		!strings.Contains(rows[0].Detail, "sdkLeavesQty=2") ||
		strings.Contains(rows[0].Detail, "releaseApplied=") {
		t.Fatalf(
			"blocked cancellation audit = %+v, want no local release marker",
			rows,
		)
	}
}

func TestLocalNode_SubmitImmediatePassesVolumeToEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	_, result, err := n.SubmitImmediate(
		context.Background(), testKey("acc-1"), domain.Order{
			BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
			AmountKind: domain.OrderAmountKindVolume, AmountValue: "500", Price: "100",
		}, domain.MissingAccountCreate, testCaller,
	)
	if err != nil {
		t.Fatalf("SubmitImmediate(volume): %v", err)
	}
	if !result.Accepted {
		t.Fatalf("SubmitImmediate(volume) = %+v, want accepted", result)
	}
	if len(eng.submitCalls) != 1 {
		t.Fatalf("volume immediate engine calls = %+v, want one", eng.submitCalls)
	}
	got := eng.submitCalls[0]
	if got.AmountKind != domain.OrderAmountKindVolume || got.AmountValue != "500" {
		t.Fatalf(
			"volume immediate engine amount = (%q, %q), want (volume, 500)",
			got.AmountKind,
			got.AmountValue,
		)
	}
}

func TestLocalNode_SubmitImmediateSeparatesTradeAndLockPrices(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitTradePrice = "99"
	eng.submitSettlementLockPrice = "101"
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	order, result, err := n.SubmitImmediate(ctx, testKey("acc-1"), domain.Order{
		BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, AmountValue: "2", Price: "99",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	if result.TradePrice != "99" || result.SettlementLockPrice != "101" {
		t.Fatalf("immediate result = %+v, want trade 99 and lock 101", result)
	}
	if order.Status != domain.OrderStatusFilled || order.Leaves != "0" {
		t.Fatalf("immediate order = %+v, want filled with zero leaves", order)
	}
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(detail.Trades) != 1 || detail.Trades[0].Price != "99" ||
		detail.Trades[0].LockPrice != "101" {
		t.Fatalf("trades = %+v, want price 99 and lock price 101", detail.Trades)
	}
	if detail.Order.Status != domain.OrderStatusFilled || detail.Order.Leaves != "0" {
		t.Fatalf("stored immediate order = %+v, want filled with zero leaves", detail.Order)
	}
	if result.ExecutionReport == nil || result.ExecutionReport.ExternalID.IsZero() {
		t.Fatalf("immediate execution report identity = %+v, want assigned id", result.ExecutionReport)
	}
	foundReport := false
	for _, event := range detail.Events {
		if event.Type != domain.OrderEventFill {
			continue
		}
		foundReport = true
		if event.Payload.FillPrice != "99" ||
			event.Payload.FillLockPrice != "101" ||
			event.Payload.LeavesQuantity != "0" ||
			event.Payload.OrderStatus != string(domain.OrderStatusFilled) ||
			event.Payload.ExecutionReport == nil ||
			event.Payload.ExecutionReport.ExternalID !=
				result.ExecutionReport.ExternalID {
			t.Fatalf("fill event = %+v, want complete linked execution report", event)
		}
	}
	if !foundReport {
		t.Fatal("immediate fill event missing")
	}
	audit, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionExecutionReport},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(execution report): %v", err)
	}
	// The immediate report settles its own fill, so the audited leaves are the
	// zero in the adapter-built report request.
	if len(audit) != 1 || audit[0].Account != "acc-1" ||
		!strings.Contains(audit[0].Detail, "qty=2 leavesQty=0 filled") ||
		!strings.Contains(audit[0].Detail, "sdkLeavesQty=0") {
		t.Fatalf("immediate execution-report audit = %+v", audit)
	}
}

func TestLocalNode_SubmitDropCopyPersistsBlockCallerAndAudit(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitBlocks = []domain.ExecutionAccountBlock{{
		Account: "acc-1", Policy: "pnl_bounds", Code: "account_blocked",
		Reason: "kill-switch tripped", Details: "daily loss",
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	caller := domain.Caller{
		Source: domain.SourceAPI, Principal: domain.PrincipalOperator,
	}
	id := domain.ExternalID("drop-copy-order")
	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		ExternalID: id, Account: "acc-1", DropCopy: true,
		BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "100",
	}, domain.MissingAccountCreate, caller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Source != domain.SourceAPI || !order.DropCopy {
		t.Fatalf("order = %+v", order)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if !account.Blocked || !strings.Contains(account.BlockReason, "kill-switch tripped") {
		t.Fatalf("account = %+v", account)
	}
	detail, err := st.GetOrder(ctx, id)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	for _, event := range detail.Events {
		if event.Source != domain.SourceAPI {
			t.Fatalf("event = %+v, want api source", event)
		}
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Source:  domain.SourceAPI,
		Actions: []domain.AuditAction{domain.AuditActionSubmitDropCopy},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || rows[0].Account != "acc-1" ||
		rows[0].Actor != domain.PrincipalOperator {
		t.Fatalf("drop-copy audit rows = %+v", rows)
	}
}

func TestLocalNode_SubmitImmediatePanelDoesNotInferAccountPnl(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitOutcomes = []engine.BalanceOutcome{
		{
			Asset: "AAPL",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceResult: "2", RealizedPnlResult: "9",
			},
		},
		{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceResult: "800", RealizedPnlResult: "2.5",
			},
		},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Currency: "USD",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	panelCaller := testCaller
	panelCaller.Source = domain.SourcePanel
	order, result, err := n.SubmitImmediate(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, panelCaller)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	if !result.Accepted || order.Source != domain.SourcePanel {
		t.Fatalf("immediate result=%+v order=%+v, want accepted panel order", result, order)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "0" {
		t.Fatalf("account pnl = %q, want unchanged 0", account.Pnl)
	}
	quote, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance USD: ok=%v err=%v", ok, err)
	}
	if quote.RealizedPnl != "2.5" {
		t.Fatalf("quote realized pnl = %q, want 2.5", quote.RealizedPnl)
	}
}

func TestLocalNode_SubmitImmediatePersistsAuthoritativeAccountPnl(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitAccountPnl = "12.340"
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code:          "acc-1",
		Currency:      "USD",
		Pnl:           "7",
		PnlHaltReason: domain.PnlHaltReasonMissingFx,
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, result, err := n.SubmitImmediate(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	if result.AccountPnl != "12.340" || result.AccountPnlHaltReason != "" {
		t.Fatalf("immediate result = %+v, want authoritative account pnl", result)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "12.340" || account.PnlHaltReason != "" {
		t.Fatalf("account = %+v, want pnl 12.340 and cleared halt", account)
	}
}

func TestLocalNode_SubmitImmediatePersistsAuthoritativeAccountPnlHalt(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitAccountPnlHaltReason = domain.PnlHaltReasonArithmeticOverflow
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Currency: "USD", Pnl: "7",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, result, err := n.SubmitImmediate(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	if result.AccountPnl != "" ||
		result.AccountPnlHaltReason != domain.PnlHaltReasonArithmeticOverflow {
		t.Fatalf("immediate result = %+v, want arithmetic-overflow halt", result)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "7" ||
		account.PnlHaltReason != domain.PnlHaltReasonArithmeticOverflow {
		t.Fatalf("account = %+v, want preserved pnl and arithmetic-overflow halt", account)
	}
}

func TestLocalNode_SubmitImmediateLeavesAccountPnlWithoutMatchingOutcome(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitOutcomes = []engine.BalanceOutcome{{
		Asset: "AAPL",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceResult: "2", RealizedPnlResult: "99",
		},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Currency: "USD", Pnl: "7",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, _, err := n.SubmitImmediate(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "7" {
		t.Fatalf("account pnl = %q, want unchanged 7", account.Pnl)
	}
}

func TestLocalNode_SubmitImmediateDoesNotSelectAccountPnlFromBalanceOutcomes(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitOutcomes = []engine.BalanceOutcome{
		{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceResult: "800", RealizedPnlResult: "2.5",
			},
		},
		{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				RealizedPnlResult: "3.5",
			},
		},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Currency: "USD",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, _, err := n.SubmitImmediate(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "0" {
		t.Fatalf("account pnl = %q, want unchanged 0", account.Pnl)
	}
}

func TestLocalNode_SubmitImmediateDoesNotUseEffectiveCurrencyToInferAccountPnl(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitOutcomes = []engine.BalanceOutcome{
		{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				RealizedPnlResult: "2.5",
			},
		},
		{
			Asset: "EUR",
			Outcome: domain.AdjustmentOutcomeAccepted{
				RealizedPnlResult: "4.5",
			},
		},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Currency: "USD",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	n.engineMu.Lock()
	n.engine = &currencyChangingAccountLaneEngine{
		fakeEngine: eng,
		beforeLane: func() error {
			return st.SetAccountCurrency(ctx, "acc-1", "EUR")
		},
	}
	n.engineMu.Unlock()

	_, _, err := n.SubmitImmediate(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.EffectiveCurrency != "EUR" || account.Pnl != "0" {
		t.Fatalf("account = %+v, want effective EUR and unchanged pnl", account)
	}
}

type currencyChangingAccountLaneEngine struct {
	*fakeEngine
	beforeLane func() error
}

func (e *currencyChangingAccountLaneEngine) RunAccountSynchronized(
	ctx context.Context,
	account domain.AccountID,
	fn func(engine.AccountLane) error,
) error {
	if err := e.beforeLane(); err != nil {
		return err
	}
	return e.fakeEngine.RunAccountSynchronized(ctx, account, fn)
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
	}, domain.MissingAccountCreate, testCaller)
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

// TestLocalNode_SubmitImmediateDoesNotReadAccountDuringSubmissionApply guards
// against reading the realm while RecordOrderSubmission owns its transaction.
func TestLocalNode_SubmitImmediateDoesNotReadAccountDuringSubmissionApply(
	t *testing.T,
) {
	t.Parallel()
	var guard *accountReadGuardRealm
	st := newRealmWrapStore(
		newMemoryStore("node.db"),
		func(realm store.RealmStore) store.RealmStore {
			guard = &accountReadGuardRealm{RealmStore: realm}
			return guard
		},
	)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	n := newTestNodeWithStore(t, st, newFakeEngine())
	if _, err := n.CreateAccount(
		ctx, testAccount("acc-1"), testCaller,
	); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	order, result, err := n.SubmitImmediate(
		ctx,
		testKey("acc-1"),
		domain.Order{
			BaseAsset:   "AAPL",
			QuoteAsset:  "USD",
			Side:        domain.OrderSideBuy,
			AmountKind:  domain.OrderAmountKindQuantity,
			AmountValue: "20",
			Price:       "100",
		},
		domain.MissingAccountCreate, testCaller,
	)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("order=%+v result=%+v, want accepted", order, result)
	}
	if guard == nil {
		t.Fatal("account read guard was not installed")
	}
	if guard.accountReadDuringApply {
		t.Fatal("GetAccount was called during order submission apply")
	}
}

// TestLocalNode_SubmitImmediatePostEngineStoreFailureFatals proves the engine evaluates
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
	}, domain.MissingAccountCreate, testCaller)
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
	}, domain.MissingAccountCreate, testCaller)
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

func TestLocalNode_SubmitOrderRejectsUnsupportedAssetAutoCreateBeforeStoreWrite(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	base := newMemoryStore("auto-create-asset-capability.db")
	var probe *assetMutationProbeRealm
	st := newRealmWrapStore(base, func(realm store.RealmStore) store.RealmStore {
		probe = &assetMutationProbeRealm{RealmStore: realm}
		return probe
	})
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seedTestAccount(t, base.realm, "acc-1")
	n := newResolverCapabilityTestNode(t, st)
	if probe == nil {
		t.Fatal("asset mutation probe was not installed")
	}
	probe.createCalls = 0

	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "GOLD",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("SubmitOrder = %v, want ErrNotImplemented", err)
	}
	if probe.createCalls != 0 {
		t.Fatalf("CreateAsset store calls = %d, want 0", probe.createCalls)
	}
	if order.ExternalID != "" {
		t.Fatalf("SubmitOrder returned order %q, want no order", order.ExternalID)
	}
	if _, ok, getErr := n.realm.GetAsset(ctx, "GOLD"); getErr != nil || ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want absent", ok, getErr)
	}
	if _, ok, getErr := n.realm.GetAssetClass(
		ctx, autoCreatedAssetClassCode,
	); getErr != nil || ok {
		t.Fatalf(
			"GetAssetClass(auto-created) = ok %v err %v, want absent",
			ok,
			getErr,
		)
	}
}

// failingAssetResolverEngine forces AddAssetResolverEntry to fail with a
// caller-supplied error while delegating everything else to the wrapped fake,
// so a test can drive the auto-created asset publication failure without
// disturbing the rest of the engine's dictionary state.
type failingAssetResolverEngine struct {
	*fakeEngine
	err error
}

func (e *failingAssetResolverEngine) AddAssetResolverEntry(domain.Asset) error {
	return e.err
}

// TestLocalNode_SubmitOrderAutoCreateAssetRollbackSuccessIsInternal covers the
// successful-rollback branch of rollbackAutoCreatedAssetPublication: the store
// write for the auto-created asset already committed before the resolver
// publish failed, so even though the compensating delete restores a consistent
// state, the failure is Officer's to own. The caller must not see it classified
// as the domain sentinel the resolver failure carried (which would surface as a
// 409 for an order that referenced a perfectly fine, if unknown, asset code),
// while the underlying diagnostic cause must still be reachable for logs.
func TestLocalNode_SubmitOrderAutoCreateAssetRollbackSuccessIsInternal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newMemoryStore("auto-create-asset-rollback.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	base := newFakeEngine()
	diagnosticCause := errors.New("asset dictionary desync detail")
	resolverErr := fmt.Errorf(
		"engine: asset resolver alias %q already exists: %w: %w",
		"GOLD", domain.ErrAlreadyExists, diagnosticCause,
	)
	var captured engine.Snapshot
	inner := fakeBuild(base, &captured)
	nn, _, err := NewLocalNode(ctx, st, func(snap engine.Snapshot) (engine.Engine, error) {
		if _, buildErr := inner(snap); buildErr != nil {
			return nil, buildErr
		}
		return &failingAssetResolverEngine{fakeEngine: base, err: resolverErr}, nil
	})
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n)
	seedTestAccount(t, n.realm, "acc-1")
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	_, submitErr := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "GOLD",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if submitErr == nil {
		t.Fatal("SubmitOrder succeeded, want asset resolver publication failure")
	}
	if errors.Is(submitErr, domain.ErrAlreadyExists) {
		t.Fatalf(
			"SubmitOrder error = %v, must not expose the domain sentinel after a "+
				"committed auto-create rollback",
			submitErr,
		)
	}
	if !errors.Is(submitErr, diagnosticCause) {
		t.Fatalf("SubmitOrder error = %v, want diagnostic cause preserved", submitErr)
	}
	if _, ok, getErr := n.realm.GetAsset(ctx, "GOLD"); getErr != nil || ok {
		t.Fatalf("GetAsset(GOLD) after rollback: ok=%v err=%v, want removed", ok, getErr)
	}
	if fatalErr != nil {
		t.Fatalf(
			"fatal error = %v, want successful rollback without a fatal", fatalErr,
		)
	}
}

func TestLocalNode_AutoCreatedAssetAuditSurvivesRequestCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := newFakeEngine()
	eng.afterAssetResolverAdd = cancel
	n, _ := newTestNode(t, eng)
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	if _, err := n.CreateAccount(ctx, domain.Account{
		Code:     "asset-audit-cancel",
		Currency: "GOLD",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount after request cancellation: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("asset resolver publication did not cancel the request context")
	}
	if fatalErr != nil {
		t.Fatalf("auto-created asset audit triggered fatal path: %v", fatalErr)
	}
	rows, err := n.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, row := range rows {
		if row.Action == domain.AuditActionCreateAsset && row.Asset == "GOLD" {
			return
		}
	}
	t.Fatalf("audit rows = %+v, want auto-created GOLD asset", rows)
}

// TestLocalNode_SubmitOrderAutoCreatesUnknownAccount checks that an order
// submitted for an account Officer does not know yet auto-creates it (like a
// fresh adjustment target), so the engine resolves the account and the order
// is processed rather than rejected as invalid, and the creation is audited.
func TestLocalNode_SubmitOrderAutoCreatesUnknownAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	// Enforce the resolver so a submit against an unknown account would error
	// unless the auto-create publishes the account before the lane starts.
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
	}, domain.MissingAccountCreate, testCaller)
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
	}, domain.MissingAccountCreate, testCaller)
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
	}, domain.MissingAccountCreate, testCaller)
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
	if _, err := n.SubmitOrder(ctx, testKey("acc-1"), mk(), domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("first SubmitOrder: %v", err)
	}
	_, err := n.SubmitOrder(ctx, testKey("acc-1"), mk(), domain.MissingAccountCreate, testCaller)
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
		}, domain.MissingAccountCreate, testCaller)
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
		}, domain.MissingAccountCreate, testCaller)
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

// TestLocalNode_ApplyAdjustmentHonorsSuppliedExternalID covers the user-create
// adjustment path end-to-end: a supplied id is carried onto the record and
// persisted verbatim; a duplicate surfaces domain.ErrAlreadyExists.
