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

	"go.openpit.dev/openpit"
	"go.openpit.dev/openpit/asyncengine"
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
		eng, testAsyncEngine(t, eng), service, registered, nil, res,
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
// (holding quote funds), commit it, then apply a fill report carrying filled
// an explicit leaves quantity. It asserts the report settles with no account
// block and produces a per-asset outcome for each spot leg. This locks in that
// the mapper sets leaves quantity and terminal order status; without them the engine
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

	held, err := e.ReserveHold(ctx, testOrder())
	if err != nil || !held.Accepted {
		t.Fatalf("ReserveHold: %v accepted=%v", err, held.Accepted)
	}
	if err := e.CommitHeld(ctx, held.ApprovalID); err != nil {
		t.Fatalf("CommitHeld: %v", err)
	}

	reportCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.ApplyExecutionReport(reportCtx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		FillQuantity:   testQty,
		FillPrice:      held.SettlementLockPrice,
		LeavesQuantity: "0",
		LockPrice:      held.SettlementLockPrice,
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
		OrderStatus:  domain.OrderStatusFilled,
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
	adapter := newOpenPitEngine(
		eng, testAsyncEngine(t, eng), service, registered, nil, res,
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
		OrderStatus:    domain.OrderStatusFilled,
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

// TestResolvedReservationRetainedForDoubleResolve proves the resolved-entry
// guard survives without any TTL: a committed id is remembered so a second
// commit is recognised as already-resolved (conflict), not unknown.
func TestResolvedReservationRetainedForDoubleResolve(t *testing.T) {
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
	e.registry.mu.Unlock()

	// A repeated commit of the same id is a conflict, not ErrNotFound: the terminal
	// outcome is retained for double-resolve detection.
	if err := e.CommitHeld(ctx, res.ApprovalID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second CommitHeld = %v, want ErrConflict", err)
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
	adapter := newOpenPitEngine(
		eng, testAsyncEngine(t, eng), service, registered, nil, res,
	).(*openPitEngine)
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

func TestStopWaitsForMidFlightHoldThenDrains(t *testing.T) {
	adapter := newTestEngine(t)
	ctx := context.Background()

	entered := make(chan HoldResult, 1)
	release := make(chan struct{})
	laneDone := make(chan error, 1)
	go func() {
		laneDone <- adapter.RunAccountSynchronized(
			ctx, domain.AccountID(testAccount), func(lane AccountLane) error {
				held, err := lane.ReserveHold(ctx, testOrder())
				if err != nil {
					return err
				}
				entered <- held
				<-release
				return nil
			})
	}()

	held := <-entered
	if !held.Accepted {
		close(release)
		t.Fatal("mid-flight ReserveHold rejected")
	}
	stopDone := make(chan struct{})
	go func() {
		adapter.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("Stop returned before the in-flight account lane finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-laneDone:
		if err != nil {
			t.Fatalf("lane: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("account lane did not finish")
	}
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish after releasing the account lane")
	}
	if got := adapter.registrySize(); got != 0 {
		t.Fatalf("registry size after Stop = %d, want 0", got)
	}
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

// fakeReservationStore is an in-memory ReservationStore for reconcile tests. It
// applies ResolveOrderReservation atomically against its own maps keyed by the
// order's opaque external id: a single call flips the intent, advances the order
// status, and records the events together. The status WHERE-guard is honoured so
// a resolve that races a committed order yields domain.ErrConflict and writes
// nothing, exactly like the real store.
type fakeReservationStore struct {
	mu      sync.Mutex
	intents map[string]domain.ReservationIntent
	events  []domain.OrderEvent
	status  map[domain.ExternalID]domain.OrderStatus
	// resolveCalls counts ResolveOrderReservation invocations - the single atomic
	// entry point the confirm/cancel paths use.
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
