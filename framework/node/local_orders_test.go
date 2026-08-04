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
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("order status = %q, want committed", order.Status)
	}
	if order.Leaves != "" {
		t.Fatalf("order leaves = %q, want empty before the first report", order.Leaves)
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

// An order has no reported leaves at submit, so the field stays empty until the
// first execution report supplies it. Officer is a registrar here: it records
// the caller's value without inventing one from the submitted amount.
func TestLocalNode_VolumeOrderLeavesComeFromFirstExecutionReport(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitLock = []byte("stored-lock")
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
	if order.Leaves != "" {
		t.Fatalf("volume order leaves = %q, want empty", order.Leaves)
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

func TestLocalNode_SubmitImmediateRejectsVolumeBeforeEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	_, _, err := n.SubmitImmediate(
		context.Background(), testKey("acc-1"), domain.Order{
			BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
			AmountKind: domain.OrderAmountKindVolume, AmountValue: "500", Price: "100",
		}, domain.MissingAccountCreate, testCaller,
	)
	if !errors.Is(err, domain.ErrInvalid) ||
		!strings.Contains(err.Error(), "executed quantity") {
		t.Fatalf("SubmitImmediate error = %v, want invalid executed quantity", err)
	}
	if len(eng.submitCalls) != 0 {
		t.Fatalf("volume immediate reached engine: %+v", eng.submitCalls)
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
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(detail.Trades) != 1 || detail.Trades[0].Price != "99" ||
		detail.Trades[0].LockPrice != "101" {
		t.Fatalf("trades = %+v, want price 99 and lock price 101", detail.Trades)
	}
	for _, event := range detail.Events {
		if event.Type == domain.OrderEventFill &&
			(event.Payload.FillPrice != "99" || event.Payload.FillLockPrice != "101") {
			t.Fatalf("fill event = %+v, want price 99 and lock price 101", event)
		}
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
