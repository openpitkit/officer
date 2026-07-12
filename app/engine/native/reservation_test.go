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

// These tests exercise reservation settlement against a real OpenPit engine and
// so require the native runtime dylib at run time (set
// OPENPIT_RUNTIME_LIBRARY_PATH or build the workspace dylib first, as documented
// for the Go bindings). They build one real engine through buildEngine, seed an
// account balance, and drive order and execution-report paths through the adapter.

package native

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/openpit"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade/policies"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
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

func testAsyncEngine(t *testing.T, eng *openpit.Engine) *asyncengine.AsyncEngine {
	t.Helper()
	async, err := asyncengine.NewBuilder(eng).
		WithStopUnderlying(eng.Stop).
		Dynamic().
		Build()
	if err != nil {
		t.Fatalf("build async engine: %v", err)
	}
	return async
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
	adapter := newOpenPitEngine(
		eng, testAsyncEngine(t, eng), service, registered, nil, nil, res,
	).(*openPitEngine)
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

// newUnpricedTestEngine builds the adapter with validation only, deliberately
// omitting SpotFunds and every other policy that could contribute a lock price.
func newUnpricedTestEngine(t *testing.T) *openPitEngine {
	t.Helper()
	snap := Snapshot{Accounts: []domain.Account{account(testAccount)}}
	res, err := newIDResolver(snap.Accounts, snap.Groups)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	eng, err := openpit.NewEngineBuilder().AccountSync().
		Builtin(policies.BuildOrderValidation()).
		Build()
	if err != nil {
		t.Fatalf("build unpriced engine: %v", err)
	}
	adapter := newOpenPitEngine(
		eng,
		testAsyncEngine(t, eng),
		nil,
		map[string]struct{}{},
		nil,
		nil,
		res,
	).(*openPitEngine)
	t.Cleanup(adapter.Stop)
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
}

// TestApplyExecutionReport_SettlesFillNoBlock drives a fill end to end through
// the real engine and the real executionReportFrom mapping: submit a spot BUY
// (holding quote funds), then apply a fill report carrying an explicit leaves
// quantity. It asserts the report settles with no account
// block and produces a per-asset outcome for each spot leg. This locks in that
// the mapper sets leaves quantity and terminal order status; without them the engine
// rejects the fill with missing_required_field and blocks the account.
func TestApplyExecutionReport_SettlesFillNoBlock(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	submitted, err := e.SubmitOrder(ctx, testOrder())
	if err != nil || !submitted.Accepted {
		t.Fatalf("SubmitOrder: %v accepted=%v", err, submitted.Accepted)
	}

	// A full fill of the 5-unit order at the reservation's settlement lock price
	// nets the held quote to zero: leaves is 0 and the fill is final.
	result, err := e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		FillQuantity:   testQty,
		FillPrice:      submitted.SettlementLockPrice,
		LeavesQuantity: "0",
		LockPrice:      submitted.SettlementLockPrice,
		Account:        domain.AccountID(testAccount),
		Side:           domain.OrderSideBuy,
		OrderStatus:    domain.OrderStatusFilled,
	})
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if len(result.Blocks) != 0 {
		t.Fatalf("fill must not block the account, got blocks=%+v", result.Blocks)
	}
	// Both spot legs settle: the base (AAPL) and the quote (USD).
	outcomes := map[string]domain.AdjustmentOutcomeAccepted{}
	for _, o := range result.Outcomes {
		outcomes[o.Asset] = o.Outcome
	}
	if _, ok := outcomes[testBase]; !ok {
		t.Fatalf("want outcomes for %s and %s, got %+v", testBase, testQuote, result.Outcomes)
	}
	if _, ok := outcomes[testQuote]; !ok {
		t.Fatalf("want outcomes for %s and %s, got %+v", testBase, testQuote, result.Outcomes)
	}
	if base := outcomes[testBase]; base.BalanceDelta != testQty || base.BalanceResult != testQty {
		t.Fatalf("base outcome = %+v, want balance +%s result %s", base, testQty, testQty)
	}
	if quote := outcomes[testQuote]; quote.BalanceDelta != "" ||
		quote.HeldDelta != "-500" || quote.HeldResult != "0" {
		t.Fatalf("quote outcome = %+v, want held-only release without balance double count", quote)
	}
}

func TestApplyExecutionReport_CanceledContextDoesNotEnterLane(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	submitted, err := e.SubmitOrder(ctx, testOrder())
	if err != nil || !submitted.Accepted {
		t.Fatalf("SubmitOrder: %v accepted=%v", err, submitted.Accepted)
	}

	reportCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.ApplyExecutionReport(reportCtx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		FillQuantity:   testQty,
		FillPrice:      submitted.SettlementLockPrice,
		LeavesQuantity: "0",
		LockPrice:      submitted.SettlementLockPrice,
		Account:        domain.AccountID(testAccount),
		Side:           domain.OrderSideBuy,
		Order:          "order-1",
		OrderStatus:    domain.OrderStatusFilled,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ApplyExecutionReport with canceled context = %v, want context canceled", err)
	}
}

func TestApplyExecutionReport_NoTradeFinalReleasesReservation(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	submitted, err := e.SubmitOrder(ctx, testOrder())
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if !submitted.Accepted {
		t.Fatalf("SubmitOrder rejected: %+v", submitted.Rejects)
	}

	result, err := e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		LeavesQuantity: testQty,
		Lock:           submitted.Lock,
		Account:        domain.AccountID(testAccount),
		Side:           domain.OrderSideBuy,
		OrderStatus:    domain.OrderStatusCancelled,
	})
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if len(result.Blocks) != 0 {
		t.Fatalf("cancel must not block the account, got blocks=%+v", result.Blocks)
	}

	outcomes := map[string]domain.AdjustmentOutcomeAccepted{}
	for _, outcome := range result.Outcomes {
		outcomes[outcome.Asset] = outcome.Outcome
	}
	base := outcomes[testBase]
	if base.IncomingDelta != "" && base.IncomingDelta != "-"+testQty {
		t.Fatalf("base incoming delta = %q, want empty or -%s", base.IncomingDelta, testQty)
	}
	quote := outcomes[testQuote]
	if quote.BalanceDelta != "500" || quote.HeldDelta != "-500" {
		t.Fatalf("quote outcome = %+v, want available +500 held -500", quote)
	}
}

// TestApplyExecutionReport_MissingLeavesRejected proves the mapper treats an
// empty leaves quantity as caller error: the report is rejected with
// domain.ErrInvalid before it reaches the engine, with no account block.
func TestApplyExecutionReport_MissingLeavesRejected(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	submitted, err := e.SubmitOrder(ctx, testOrder())
	if err != nil || !submitted.Accepted {
		t.Fatalf("SubmitOrder: %v accepted=%v", err, submitted.Accepted)
	}

	_, err = e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:    testBase,
		QuoteAsset:   testQuote,
		FillQuantity: testQty,
		FillPrice:    submitted.SettlementLockPrice,
		LockPrice:    submitted.SettlementLockPrice,
		Account:      domain.AccountID(testAccount),
		Side:         domain.OrderSideBuy,
		OrderStatus:  domain.OrderStatusFilled,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyExecutionReport(empty leaves) = %v, want ErrInvalid", err)
	}
}

func newTestEngineWithSpotFundsPnlBounds(t *testing.T) *openPitEngine {
	t.Helper()
	acct := account(testAccount)
	acct.Currency = testQuote
	acct.GroupCode = "desk-a"
	snap := Snapshot{
		Accounts: []domain.Account{acct},
		Groups: []domain.AccountGroup{{
			Code:          "desk-a",
			EngineGroupID: 7,
			Currency:      testQuote,
		}},
		Balances: []domain.Balance{
			{
				Account:   domain.AccountID(testAccount),
				Asset:     testQuote,
				Available: "2000",
			},
			{
				Account:   domain.AccountID(testAccount),
				Asset:     testBase,
				Available: "10",
			},
		},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			{
				Scope:           domain.ScopeGlobal,
				AccountCurrency: testQuote,
				LowerBound:      "-1000000",
			},
			{
				Scope:           domain.ScopeAccountGroup,
				AccountGroup:    "desk-a",
				AccountCurrency: testQuote,
				LowerBound:      "-1000000",
			},
			{
				Scope:           domain.ScopeAccount,
				Account:         domain.AccountID(testAccount),
				AccountCurrency: testQuote,
				LowerBound:      "-6",
				InitialPnl:      "-5",
			},
		},
	}
	engine, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	adapter := engine.(*openPitEngine)
	t.Cleanup(adapter.Stop)
	return adapter
}

func TestSpotFundsPnlBoundsBuildConfiguresBasePolicyAndSeed(t *testing.T) {
	e := newTestEngineWithSpotFundsPnlBounds(t)
	ctx := context.Background()

	if _, ok := e.registered[nameSpotFunds]; !ok || len(e.registered) != 1 {
		t.Fatalf("registered policies = %+v, want only %s", e.registered, nameSpotFunds)
	}
	if err := e.sink.Push(marketdata.QuoteUpdate{
		Base: testBase, Quote: testQuote, Mark: "100",
	}); err != nil {
		t.Fatalf("Push quote: %v", err)
	}
	market := testOrder()
	market.Price = ""
	market.AmountValue = "1"
	if result, err := e.SubmitOrder(ctx, market); err != nil || !result.Accepted {
		t.Fatalf("market SubmitOrder: err=%v result=%+v", err, result)
	}

	feeOrder := testOrder()
	feeOrder.AmountValue = "1"
	submitted, err := e.SubmitOrder(ctx, feeOrder)
	if err != nil {
		t.Fatalf("SubmitOrder fee order: %v", err)
	}
	if !submitted.Accepted {
		t.Fatalf("fee order rejected before fill: %+v", submitted.Rejects)
	}

	result, err := e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		FillQuantity:   "1",
		FillPrice:      submitted.SettlementLockPrice,
		LeavesQuantity: "0",
		LockPrice:      submitted.SettlementLockPrice,
		Commission:     &domain.Commission{Amount: "-2", Currency: testQuote},
		Account:        domain.AccountID(testAccount),
		Side:           domain.OrderSideBuy,
		OrderStatus:    domain.OrderStatusFilled,
	})
	if err != nil {
		t.Fatalf("ApplyExecutionReport fee order: %v", err)
	}
	if len(result.Blocks) == 0 {
		t.Fatalf("fee fill did not block account; outcomes=%+v", result.Outcomes)
	}
	if result.Blocks[0].Account != domain.AccountID(testAccount) {
		t.Fatalf("block account = %q, want %q", result.Blocks[0].Account, testAccount)
	}
}

func TestConfigurePolicy_SpotFundsPnlBoundsClearsLastBarrierOnline(t *testing.T) {
	e := newTestEngineWithSpotFundsPnlBounds(t)
	ctx := context.Background()

	engBefore := e.eng
	if err := e.ConfigurePolicy(
		ctx,
		domain.PolicySpotFundsPnlBoundsKillSwitch,
		LimitSet{},
	); err != nil {
		t.Fatalf("ConfigurePolicy clear spot funds pnl bounds: %v", err)
	}
	if e.eng != engBefore {
		t.Fatal("ConfigurePolicy replaced engine handle, want online reconfigure")
	}

	feeOrder := testOrder()
	feeOrder.AmountValue = "1"
	submitted, err := e.SubmitOrder(ctx, feeOrder)
	if err != nil {
		t.Fatalf("SubmitOrder fee order: %v", err)
	}
	if !submitted.Accepted {
		t.Fatalf("fee order rejected before fill: %+v", submitted.Rejects)
	}

	result, err := e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		FillQuantity:   "1",
		FillPrice:      submitted.SettlementLockPrice,
		LeavesQuantity: "0",
		LockPrice:      submitted.SettlementLockPrice,
		Commission:     &domain.Commission{Amount: "-2", Currency: testQuote},
		Account:        domain.AccountID(testAccount),
		Side:           domain.OrderSideBuy,
		OrderStatus:    domain.OrderStatusFilled,
	})
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if len(result.Blocks) != 0 {
		t.Fatalf("cleared spot funds bound still blocked fill: %+v", result.Blocks)
	}
}

// newTestEngineGlobalSpotFundsPnlBounds builds a real engine whose SpotFunds P&L
// bounds carry only permissive global and account-group barriers - no
// account-scope barrier. A global or group barrier is enough for the policy to
// accumulate per-account P&L, so an account-scope barrier introduced later at
// runtime observes whatever P&L has already accrued.
func newTestEngineGlobalSpotFundsPnlBounds(t *testing.T) *openPitEngine {
	t.Helper()
	acct := account(testAccount)
	acct.Currency = testQuote
	acct.GroupCode = "desk-a"
	snap := Snapshot{
		Accounts: []domain.Account{acct},
		Groups: []domain.AccountGroup{{
			Code:          "desk-a",
			EngineGroupID: 7,
			Currency:      testQuote,
		}},
		Balances: []domain.Balance{
			{
				Account:   domain.AccountID(testAccount),
				Asset:     testQuote,
				Available: "2000",
			},
			{
				Account:   domain.AccountID(testAccount),
				Asset:     testBase,
				Available: "10",
			},
		},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			{
				Scope:           domain.ScopeGlobal,
				AccountCurrency: testQuote,
				LowerBound:      "-1000000",
			},
			{
				Scope:           domain.ScopeAccountGroup,
				AccountGroup:    "desk-a",
				AccountCurrency: testQuote,
				LowerBound:      "-1000000",
			},
		},
	}
	engine, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	adapter := engine.(*openPitEngine)
	t.Cleanup(adapter.Stop)
	return adapter
}

// commitSpotFundsFeeFill runs one buy fill of the base asset carrying a -2 quote
// commission, so the SpotFunds policy accrues -2 of realized account-currency
// P&L. It returns the execution result so a caller can assert the kill-switch
// block state.
func commitSpotFundsFeeFill(t *testing.T, e *openPitEngine) ExecutionReportResult {
	t.Helper()
	ctx := context.Background()
	order := testOrder()
	order.AmountValue = "1"
	submitted, err := e.SubmitOrder(ctx, order)
	if err != nil {
		t.Fatalf("SubmitOrder fee order: %v", err)
	}
	if !submitted.Accepted {
		t.Fatalf("fee order rejected before fill: %+v", submitted.Rejects)
	}
	result, err := e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		FillQuantity:   "1",
		FillPrice:      submitted.SettlementLockPrice,
		LeavesQuantity: "0",
		LockPrice:      submitted.SettlementLockPrice,
		Commission:     &domain.Commission{Amount: "-2", Currency: testQuote},
		Account:        domain.AccountID(testAccount),
		Side:           domain.OrderSideBuy,
		OrderStatus:    domain.OrderStatusFilled,
	})
	if err != nil {
		t.Fatalf("ApplyExecutionReport fee order: %v", err)
	}
	return result
}

// TestConfigurePolicy_SpotFundsPnlBoundsNewAccountBarrierPreservesLivePnl proves
// that adding an account-scope barrier with no initial_pnl at runtime does not
// reset the account's live accumulated P&L: the barrier arms against P&L already
// accrued rather than starting from zero.
func TestConfigurePolicy_SpotFundsPnlBoundsNewAccountBarrierPreservesLivePnl(t *testing.T) {
	e := newTestEngineGlobalSpotFundsPnlBounds(t)
	ctx := context.Background()

	// The first fill accrues -2 of account P&L under the permissive global/group
	// barriers; nothing breaches yet.
	if first := commitSpotFundsFeeFill(t, e); len(first.Blocks) != 0 {
		t.Fatalf("first fill blocked unexpectedly: %+v", first.Blocks)
	}

	// Introduce an account-scope barrier with no initial_pnl. It must not reset
	// the -2 already accrued, so its -3 lower bound stays armed against live P&L.
	limits := LimitSet{SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
		{
			Scope:           domain.ScopeGlobal,
			AccountCurrency: testQuote,
			LowerBound:      "-1000000",
		},
		{
			Scope:           domain.ScopeAccountGroup,
			AccountGroup:    "desk-a",
			AccountCurrency: testQuote,
			LowerBound:      "-1000000",
		},
		{
			Scope:           domain.ScopeAccount,
			Account:         domain.AccountID(testAccount),
			AccountCurrency: testQuote,
			LowerBound:      "-3",
		},
	}}
	if err := e.ConfigurePolicy(
		ctx, domain.PolicySpotFundsPnlBoundsKillSwitch, limits,
	); err != nil {
		t.Fatalf("ConfigurePolicy add account barrier: %v", err)
	}

	// The second fill drives accrued P&L to -4, breaching the -3 bound. A reset to
	// zero would leave it at -2 and pass, so a block proves the live P&L survived.
	second := commitSpotFundsFeeFill(t, e)
	if len(second.Blocks) == 0 {
		t.Fatal("account barrier did not block on preserved live P&L; " +
			"an empty initial_pnl reset the accumulator")
	}
	if second.Blocks[0].Account != domain.AccountID(testAccount) {
		t.Fatalf("block account = %q, want %q", second.Blocks[0].Account, testAccount)
	}
}

// TestConfigurePolicy_SpotFundsPnlBoundsNewAccountBarrierExplicitInitialPnlSeeds
// proves that an explicit initial_pnl on an account-scope barrier added at
// runtime still force-sets the live accumulated P&L, overriding what had already
// accrued.
func TestConfigurePolicy_SpotFundsPnlBoundsNewAccountBarrierExplicitInitialPnlSeeds(t *testing.T) {
	e := newTestEngineGlobalSpotFundsPnlBounds(t)
	ctx := context.Background()

	if first := commitSpotFundsFeeFill(t, e); len(first.Blocks) != 0 {
		t.Fatalf("first fill blocked unexpectedly: %+v", first.Blocks)
	}

	// An explicit initial_pnl reseeds the live accumulator to +5, discarding the
	// -2 already accrued.
	limits := LimitSet{SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
		{
			Scope:           domain.ScopeGlobal,
			AccountCurrency: testQuote,
			LowerBound:      "-1000000",
		},
		{
			Scope:           domain.ScopeAccountGroup,
			AccountGroup:    "desk-a",
			AccountCurrency: testQuote,
			LowerBound:      "-1000000",
		},
		{
			Scope:           domain.ScopeAccount,
			Account:         domain.AccountID(testAccount),
			AccountCurrency: testQuote,
			LowerBound:      "-3",
			InitialPnl:      "5",
		},
	}}
	if err := e.ConfigurePolicy(
		ctx, domain.PolicySpotFundsPnlBoundsKillSwitch, limits,
	); err != nil {
		t.Fatalf("ConfigurePolicy add seeded account barrier: %v", err)
	}

	// From the +5 seed the second fill lands at +3, clear of the -3 bound. Without
	// the seed it would sit at -4 and block, so passing proves the seed applied.
	second := commitSpotFundsFeeFill(t, e)
	if len(second.Blocks) != 0 {
		t.Fatalf(
			"seeded barrier blocked though live P&L was reset above the bound: %+v",
			second.Blocks,
		)
	}
}

// TestSubmitOrder_AcceptCapturesSettlement checks SubmitOrder returns the
// durable lock and the canonical settlement inputs a later report needs.
func TestSubmitOrder_AcceptCapturesSettlement(t *testing.T) {
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
	if res.EstimateSource != domain.EstimateSourceLimit {
		t.Fatalf("estimate source = %q, want limit", res.EstimateSource)
	}
	if res.SettlementLockPrice == "" {
		t.Fatal("SubmitOrder: empty settlement lock price")
	}
	gotSettlement, err := param.NewPriceFromString(res.SettlementLockPrice)
	if err != nil {
		t.Fatalf("parse settlement lock price: %v", err)
	}
	wantSettlement, err := param.NewPriceFromString(prices[len(prices)-1])
	if err != nil {
		t.Fatalf("parse serialized settlement lock price: %v", err)
	}
	if gotSettlement.Compare(wantSettlement) != 0 {
		t.Fatalf(
			"settlement lock price = %s, want serialized lock price %s",
			gotSettlement.String(), wantSettlement.String(),
		)
	}
	if res.LeavesQuantity != testQty {
		t.Fatalf("leaves quantity = %q, want %q", res.LeavesQuantity, testQty)
	}
}

func TestSubmitOrder_VolumeCapturesCanonicalBaseLeaves(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	order := testOrder()
	order.AmountKind = domain.OrderAmountKindVolume
	order.AmountValue = "500.00"

	res, err := e.SubmitOrder(ctx, order)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("SubmitOrder rejected: %+v", res.Rejects)
	}
	if res.SettlementLockPrice == "" {
		t.Fatal("SubmitOrder: empty settlement lock price")
	}
	gotLeaves, err := param.NewQuantityFromString(res.LeavesQuantity)
	if err != nil {
		t.Fatalf("parse base leaves quantity: %v", err)
	}
	wantLeaves, _ := param.NewQuantityFromString("5")
	if gotLeaves.Compare(wantLeaves) != 0 {
		t.Fatalf("base leaves quantity = %q, want 5", res.LeavesQuantity)
	}
}

func TestSubmitOrder_UnpricedVolumeReturnsRecordedReject(t *testing.T) {
	e := newUnpricedTestEngine(t)
	order := testOrder()
	order.AmountKind = domain.OrderAmountKindVolume
	order.AmountValue = "500"
	order.Price = ""

	res, err := e.SubmitOrder(context.Background(), order)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	assertUnpricedVolumeReject(t, res.Accepted, res.Rejects)
}

func TestSubmitImmediate_UnpricedVolumeReturnsRecordedReject(t *testing.T) {
	e := newUnpricedTestEngine(t)
	order := testOrder()
	order.AmountKind = domain.OrderAmountKindVolume
	order.AmountValue = "500"
	order.Price = ""

	res, err := e.SubmitImmediate(context.Background(), order)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	assertUnpricedVolumeReject(t, res.Accepted, res.Rejects)
}

func assertUnpricedVolumeReject(
	t *testing.T, accepted bool, rejects []domain.OrderReject,
) {
	t.Helper()
	if accepted {
		t.Fatal("unpriced volume order accepted")
	}
	if len(rejects) != 1 {
		t.Fatalf("rejects = %+v, want one", rejects)
	}
	reject := rejects[0]
	if reject.Code != "order_value_calculation_failed" ||
		reject.Scope != "order" ||
		reject.Reason != "volume order requires a settlement price" {
		t.Fatalf("reject = %+v, want order sizing reject", reject)
	}
}

func TestImmediateExecutionReport_VolumeSizing(t *testing.T) {
	res := testResolver(testAccount)
	quantity, err := immediateFillQuantity(domain.Order{
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "5.00",
	}, "")
	if err != nil {
		t.Fatalf("immediateFillQuantity(quantity): %v", err)
	}
	if quantity != "5.00" {
		t.Fatalf("immediateFillQuantity(quantity) = %q, want 5.00", quantity)
	}

	// A volume order with no settlement price cannot be sized into a fill.
	_, err = immediateExecutionReport(domain.Order{
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

// TestRunAccountSynchronized_SerializesSameAccount drives the real SDK account
// lane (not a fake global mutex): two goroutines submit work for the same
// account through RunAccountSynchronized concurrently, and the test asserts the
// lane never runs both callbacks at once. A callback that observed a sibling
// already inside the lane would flip overlap; the lane's per-account ordering
// keeps the increments race-free without any additional lock in the callback.
func TestRunAccountSynchronized_SerializesSameAccount(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	const goroutines = 8
	const iterations = 50

	var (
		active  int
		overlap bool
		counter int
		guard   sync.Mutex
	)
	// The callback intentionally reads-modifies-writes counter without a lock: if
	// the lane serializes, no data race occurs and the final value is exact.
	work := func(AccountLane) error {
		guard.Lock()
		active++
		if active > 1 {
			overlap = true
		}
		guard.Unlock()

		v := counter
		time.Sleep(time.Millisecond)
		counter = v + 1

		guard.Lock()
		active--
		guard.Unlock()
		return nil
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if err := e.RunAccountSynchronized(
					ctx, domain.AccountID(testAccount), work,
				); err != nil {
					t.Errorf("RunAccountSynchronized: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if overlap {
		t.Fatal("account lane ran two callbacks concurrently for one account")
	}
	if want := goroutines * iterations; counter != want {
		t.Fatalf("counter = %d, want %d (lane did not serialize increments)", counter, want)
	}
}

// TestRunAccountSynchronized_RejectsUnknownAccount proves the real lane resolves
// the account before entering the callback: an account the resolver does not
// know rejects with domain.ErrInvalid and the callback never runs, which is the
// seam the node's pre-lane auto-create/rebuild exists to satisfy.
func TestRunAccountSynchronized_RejectsUnknownAccount(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	ran := false
	err := e.RunAccountSynchronized(ctx, "unknown-account", func(AccountLane) error {
		ran = true
		return nil
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RunAccountSynchronized(unknown) error = %v, want ErrInvalid", err)
	}
	if ran {
		t.Fatal("callback ran for an account the resolver does not know")
	}
}
