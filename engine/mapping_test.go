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

// These tests exercise the OpenPit binding and so require the native runtime
// dylib at run time (set OPENPIT_RUNTIME_LIBRARY_PATH or build the workspace
// dylib first, as documented for the Go bindings). They build one real engine
// and reconfigure it in place; none of them rebuild the engine.

package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/configure"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pkg/optional"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
)

func TestMergeBalanceOutcomes_PreservesOrderedPhaseEffects(t *testing.T) {
	got, err := mergeBalanceOutcomes(
		[]fwengine.BalanceOutcome{
			{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceDelta: "-100", BalanceResult: "900",
				HeldDelta: "100", HeldResult: "100",
				RealizedPnlResult: "7", AverageEntryPrice: "1",
			}},
			{Asset: "BTC", Outcome: domain.AdjustmentOutcomeAccepted{
				IncomingDelta: "1", IncomingResult: "1",
				RealizedPnlResult: "3",
			}},
		},
		[]fwengine.BalanceOutcome{
			{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceDelta: "25", BalanceResult: "925",
				HeldDelta: "-100", HeldResult: "0",
				RealizedPnlDelta: "-1", RealizedPnlResult: "6",
			}},
			{Asset: "BTC", Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceDelta: "1", BalanceResult: "1",
				IncomingDelta: "-1", IncomingResult: "0",
				RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
				AverageEntryPrice:     "100",
			}},
		},
	)
	if err != nil {
		t.Fatalf("mergeBalanceOutcomes: %v", err)
	}
	if len(got) != 2 || got[0].Asset != "USD" || got[1].Asset != "BTC" {
		t.Fatalf("merged outcomes = %+v, want one USD then one BTC", got)
	}
	usd := got[0].Outcome
	if usd.BalanceDelta != "-75" || usd.BalanceResult != "925" ||
		usd.HeldDelta != "0" || usd.HeldResult != "0" ||
		usd.RealizedPnlDelta != "-1" || usd.RealizedPnlResult != "6" ||
		usd.AverageEntryPrice != "1" {
		t.Fatalf("merged USD outcome = %+v", usd)
	}
	btc := got[1].Outcome
	if btc.BalanceDelta != "1" || btc.BalanceResult != "1" ||
		btc.IncomingDelta != "0" || btc.IncomingResult != "0" ||
		btc.RealizedPnlResult != "" ||
		btc.RealizedPnlHaltReason != domain.PnlHaltReasonMissingFx ||
		btc.AverageEntryPrice != "100" {
		t.Fatalf("merged BTC outcome = %+v", btc)
	}
}

func TestSpotFundsAccountPnlFromList_NoOutcomeDoesNotWarn(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	pnl, halt, err := spotFundsAccountPnlFromList(
		param.NewAccountIDFromUint64(7), nil,
	)
	if err != nil || pnl != "" || halt != "" {
		t.Fatalf("spotFundsAccountPnlFromList = (%q, %q, %v), want unchanged", pnl, halt, err)
	}
	if logs.Len() != 0 {
		t.Fatalf("normal missing P&L outcome logged: %s", logs.String())
	}
}

func TestSettlementPrice_MultiplePricesWarnAndEmptyIsSilent(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	price := settlementPrice([]string{"99", "100"}, "order-1")
	if price != "100" {
		t.Fatalf("settlementPrice = %q, want 100", price)
	}
	logLine := logs.String()
	for _, want := range []string{
		"pre-trade lock carries unexpected price count", "count=2", "order=order-1",
	} {
		if !strings.Contains(logLine, want) {
			t.Fatalf("warning = %q, want %q", logLine, want)
		}
	}

	logs.Reset()
	price = settlementPrice(nil, "order-2")
	if price != "" {
		t.Fatalf("empty settlementPrice = %q, want empty", price)
	}
	if got := logs.String(); got != "" {
		t.Fatalf("warning = %q, want none", got)
	}
}

// testAdjustmentOutcome builds a binding account-adjustment outcome tagged with
// asset and carrying no adjusted field; the mapping tests only need the tag.
func testAdjustmentOutcome(t *testing.T, asset string) accountadjustment.Outcome {
	t.Helper()
	tag, err := testResolver().asset(asset)
	if err != nil {
		t.Fatalf("resolver asset %q: %v", asset, err)
	}
	return accountadjustment.Outcome{
		Entry: accountadjustment.AccountOutcomeEntry{Asset: tag},
	}
}

// TestOutcomeAcceptedFromList_DuplicateAssetIsError proves several engine
// outcomes for one asset surface as an error instead of the last one quietly
// winning: Officer cannot merge engine numbers it was not given.
func TestOutcomeAcceptedFromList_DuplicateAssetIsError(t *testing.T) {
	t.Parallel()
	_, _, err := outcomeAcceptedFromList(
		[]accountadjustment.Outcome{
			testAdjustmentOutcome(t, "USD"),
			testAdjustmentOutcome(t, "USD"),
		},
		"USD",
		testResolver(),
	)
	if err == nil {
		t.Fatal("want error for several outcomes on one asset, got nil")
	}
	if !strings.Contains(err.Error(), "several outcomes") {
		t.Fatalf("error = %v, want several outcomes", err)
	}
}

func TestBalanceOutcomesFromList_ReturnsHumanAssetCode(t *testing.T) {
	t.Parallel()
	res := testResolver()
	outcomes, err := balanceOutcomesFromList(
		[]accountadjustment.Outcome{testAdjustmentOutcome(t, "USD")}, res,
	)
	if err != nil {
		t.Fatalf("balanceOutcomesFromList: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Asset != "USD" {
		t.Fatalf("balance outcomes = %+v, want human USD asset code", outcomes)
	}
	settlements := executionBalanceSettlementsFrom(outcomes)
	if len(settlements) != 1 || settlements[0].Asset != "USD" {
		t.Fatalf("balance settlements = %+v, want human USD asset code", settlements)
	}
}

// TestOutcomeAcceptedFromList_OtherAssetsAreNotDuplicates keeps the duplicate
// check scoped to the requested asset.
func TestOutcomeAcceptedFromList_OtherAssetsAreNotDuplicates(t *testing.T) {
	t.Parallel()
	_, found, err := outcomeAcceptedFromList(
		[]accountadjustment.Outcome{
			testAdjustmentOutcome(t, "BTC"),
			testAdjustmentOutcome(t, "USD"),
		},
		"USD",
		testResolver(),
	)
	if err != nil {
		t.Fatalf("outcomeAcceptedFromList: %v", err)
	}
	if !found {
		t.Fatal("the USD outcome must be found")
	}
}

// TestOutcomeRejectedFrom_CarriesFailedAdjustmentIndex proves the batch reject
// keeps the index of the adjustment the engine stopped on.
func TestOutcomeRejectedFrom_CarriesFailedAdjustmentIndex(t *testing.T) {
	t.Parallel()
	rejected, err := outcomeRejectedFrom(reject.AccountAdjustmentBatchError{
		Rejects: []reject.Reject{reject.New(
			reject.CodeAccountAdjustmentBoundsExceeded,
			"spot_funds",
			"balance below lower bound",
			"lower=0",
			reject.ScopeAccount,
		)},
		FailedAdjustmentIndex: 2,
	})
	if err != nil {
		t.Fatalf("outcomeRejectedFrom: %v", err)
	}
	if rejected.FailedAdjustmentIndex != 2 {
		t.Fatalf("failed adjustment index = %d, want 2", rejected.FailedAdjustmentIndex)
	}
	if rejected.Code != rejectCodeName(reject.CodeAccountAdjustmentBoundsExceeded) ||
		rejected.Scope != "account" ||
		rejected.Policy != "spot_funds" ||
		rejected.Reason != "balance below lower bound" ||
		rejected.Details != "lower=0" {
		t.Fatalf("rejected = %+v, want the engine reject transcribed", rejected)
	}
}

// TestOutcomeRejectedFrom_NoRejectIsError proves a batch error that names no
// cause is reported as an error instead of being answered with an
// Officer-authored reject.
func TestOutcomeRejectedFrom_NoRejectIsError(t *testing.T) {
	t.Parallel()
	rejected, err := outcomeRejectedFrom(
		reject.AccountAdjustmentBatchError{FailedAdjustmentIndex: 1},
	)
	if err == nil {
		t.Fatalf("want error for a batch error without rejects, got %+v", rejected)
	}
}

// testEngineAccountID assigns a deterministic, distinct engine account id to a
// test account code so the resolver maps it without hashing. Tests address
// accounts by code; the connector would assign these ids collision-free.
func testEngineAccountID(code string) domain.EngineAccountID {
	switch code {
	case "acc-1", "1":
		return 1
	case "acc-2", "2":
		return 2
	case "acc-3", "3":
		return 3
	default:
		// A stable non-zero fallback for any other code used in a test.
		return domain.EngineAccountID(1000 + len(code))
	}
}

// account builds a snapshot account carrying its stored engine account id so the
// resolver has an entry for code.
func account(code string) domain.Account {
	return domain.Account{Code: domain.AccountID(code), EngineAccountID: testEngineAccountID(code)}
}

func testAsset(code string) domain.Asset {
	ids := map[string]domain.EngineAssetID{
		"AAPL": 1,
		"USD":  2,
		"BTC":  3,
		"USDT": 4,
		"EUR":  5,
		"MSFT": 6,
		"GBP":  7,
	}
	id, ok := ids[code]
	if !ok {
		id = 100
		for _, char := range code {
			id = id*131 + domain.EngineAssetID(char)
		}
	}
	return domain.Asset{Code: code, EngineAssetID: id}
}

// blockedAccount builds a blocked snapshot account with a stored engine id.
func blockedAccount(code, reason string) domain.Account {
	a := account(code)
	a.Blocked = true
	a.BlockReason = reason
	return a
}

func testAssets() []domain.Asset {
	return []domain.Asset{
		testAsset("AAPL"),
		testAsset("USD"),
		testAsset("BTC"),
		testAsset("USDT"),
		testAsset("EUR"),
		testAsset("MSFT"),
		testAsset("GBP"),
	}
}

// testResolver builds an idResolver covering the given account codes, mirroring
// the resolver the OpenPit engine builder builds from a Snapshot. It is used by
// the pure mapping tests that do not build a full engine.
func testResolver(codes ...string) idResolver {
	accounts := make([]domain.Account, 0, len(codes))
	for _, code := range codes {
		accounts = append(accounts, account(code))
	}
	res, err := newIDResolver(accounts, nil, testAssets())
	if err != nil {
		panic(err)
	}
	return res
}

func rateLimit(scope, acct, asset string, maxOrders uint64, window time.Duration) domain.LimitRate {
	return domain.LimitRate{
		Scope:     scope,
		Account:   domain.AccountID(acct),
		Asset:     asset,
		MaxOrders: maxOrders,
		Window:    window,
	}
}

func orderSize(scope, acct, asset, maxQty, maxNotional string) domain.LimitOrderSize {
	return domain.LimitOrderSize{
		Scope:       scope,
		Account:     domain.AccountID(acct),
		Asset:       asset,
		MaxQuantity: maxQty,
		MaxNotional: maxNotional,
	}
}

func TestRateLimitReady_AllAxes(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1")
	limits := []domain.LimitRate{
		rateLimit(domain.ScopeBroker, "", "", 1000, time.Minute),
		rateLimit(domain.ScopeAsset, "", "USD", 500, time.Minute),
		rateLimit(domain.ScopeAccount, "acc-1", "", 200, time.Minute),
		rateLimit(domain.ScopeAccountAsset, "acc-1", "USD", 100, time.Minute),
	}
	ready, err := rateLimitReady(limits, res)
	if err != nil {
		t.Fatalf("rateLimitReady: %v", err)
	}
	if ready == nil {
		t.Fatalf("nil ready builder")
	}
}

// TestRateLimitReady_UnknownAccountInvalid checks an account-scoped barrier for a
// code the resolver does not cover (no stored engine id) wraps domain.ErrInvalid
// rather than hashing the string into an engine id.
func TestRateLimitReady_UnknownAccountInvalid(t *testing.T) {
	t.Parallel()
	res := testResolver() // empty: no accounts
	_, err := rateLimitReady(
		[]domain.LimitRate{rateLimit(domain.ScopeAccount, "ghost", "", 1, time.Minute)}, res)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unknown account, got %v", err)
	}
}

// TestRateLimitReady_SecondBrokerBarrierIsError checks a second broker barrier
// is refused instead of silently replacing the first: the builder takes one
// broker barrier, and dropping the other would lose a configured limit.
func TestRateLimitReady_SecondBrokerBarrierIsError(t *testing.T) {
	t.Parallel()
	_, err := rateLimitReady([]domain.LimitRate{
		rateLimit(domain.ScopeBroker, "", "", 1000, time.Minute),
		rateLimit(domain.ScopeBroker, "", "", 10, time.Minute),
	}, testResolver())
	if err == nil {
		t.Fatal("want error for a second broker barrier, got nil")
	}
	if !strings.Contains(err.Error(), "more than one broker barrier") {
		t.Fatalf("error = %v, want more than one broker barrier", err)
	}
}

// TestOrderSizeReady_SecondBrokerBarrierIsError is the order-size counterpart of
// TestRateLimitReady_SecondBrokerBarrierIsError.
func TestOrderSizeReady_SecondBrokerBarrierIsError(t *testing.T) {
	t.Parallel()
	_, err := orderSizeReady([]domain.LimitOrderSize{
		orderSize(domain.ScopeBroker, "", "", "10", ""),
		orderSize(domain.ScopeBroker, "", "", "1", ""),
	}, testResolver())
	if err == nil {
		t.Fatal("want error for a second broker barrier, got nil")
	}
	if !strings.Contains(err.Error(), "more than one broker barrier") {
		t.Fatalf("error = %v, want more than one broker barrier", err)
	}
}

func TestOrderSizeValue_ParsesBoth(t *testing.T) {
	t.Parallel()
	limit, err := orderSizeValue(orderSize(domain.ScopeBroker, "", "", "10", "1000"))
	if err != nil {
		t.Fatalf("orderSizeValue: %v", err)
	}
	want, err := param.NewQuantityFromString("10")
	if err != nil {
		t.Fatalf("quantity: %v", err)
	}
	maxQuantity, ok := limit.MaxQuantity.Get()
	if !ok || !maxQuantity.Equal(want) {
		t.Fatalf("max_quantity mismatch")
	}
}

func TestOrderSizeValue_OmittedCapIsNone(t *testing.T) {
	t.Parallel()
	limit, err := orderSizeValue(orderSize(
		domain.ScopeBroker, "", "", "10", "",
	))
	if err != nil {
		t.Fatalf("orderSizeValue: %v", err)
	}
	if !limit.MaxQuantity.IsSet() {
		t.Fatal("max_quantity must be set")
	}
	if limit.MaxNotional.IsSet() {
		t.Fatal("max_notional must remain unset")
	}
}

func TestOrderSizeAxes_MergesComplementaryAssetCaps(t *testing.T) {
	t.Parallel()
	_, assets, _, err := orderSizeAxes([]domain.LimitOrderSize{
		orderSize(domain.ScopeUnderlyingAsset, "", "USD", "10", ""),
		orderSize(domain.ScopeSettlementAsset, "", "USD", "", "1000"),
	}, testResolver())
	if err != nil {
		t.Fatalf("orderSizeAxes: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("asset barriers = %d, want 1 merged barrier", len(assets))
	}
	if !assets[0].Limit.MaxQuantity.IsSet() ||
		!assets[0].Limit.MaxNotional.IsSet() {
		t.Fatalf("merged barrier caps = %+v", assets[0].Limit)
	}
}

func TestOrderSizeAxes_MergesComplementaryAccountAssetCaps(t *testing.T) {
	t.Parallel()
	_, _, accountAssets, err := orderSizeAxes([]domain.LimitOrderSize{
		orderSize(domain.ScopeAccountUnderlyingAsset, "acc-1", "USD", "10", ""),
		orderSize(domain.ScopeAccountSettlementAsset, "acc-1", "USD", "", "1000"),
	}, testResolver("acc-1"))
	if err != nil {
		t.Fatalf("orderSizeAxes: %v", err)
	}
	if len(accountAssets) != 1 {
		t.Fatalf(
			"account-asset barriers = %d, want 1 merged barrier",
			len(accountAssets),
		)
	}
	if !accountAssets[0].Limit.MaxQuantity.IsSet() ||
		!accountAssets[0].Limit.MaxNotional.IsSet() {
		t.Fatalf("merged barrier caps = %+v", accountAssets[0].Limit)
	}
}

func TestOrderSizeAxes_KeepsDifferentAccountsSeparate(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1", "acc-2")
	account1, err := res.account("acc-1")
	if err != nil {
		t.Fatalf("resolve acc-1: %v", err)
	}
	account2, err := res.account("acc-2")
	if err != nil {
		t.Fatalf("resolve acc-2: %v", err)
	}
	asset, err := res.asset("USD")
	if err != nil {
		t.Fatalf("resolve USD: %v", err)
	}
	_, _, accountAssets, err := orderSizeAxes([]domain.LimitOrderSize{
		orderSize(domain.ScopeAccountUnderlyingAsset, "acc-1", "USD", "10", ""),
		orderSize(domain.ScopeAccountUnderlyingAsset, "acc-2", "USD", "20", ""),
	}, res)
	if err != nil {
		t.Fatalf("orderSizeAxes: %v", err)
	}
	if len(accountAssets) != 2 {
		t.Fatalf("account-asset barriers = %d, want 2", len(accountAssets))
	}
	barrierIndexes := make(map[param.AccountID]int, len(accountAssets))
	for index, barrier := range accountAssets {
		barrierIndexes[barrier.AccountID] = index
	}
	for _, want := range []struct {
		accountID   param.AccountID
		maxQuantity string
	}{
		{accountID: account1, maxQuantity: "10"},
		{accountID: account2, maxQuantity: "20"},
	} {
		index, ok := barrierIndexes[want.accountID]
		if !ok {
			t.Fatalf("barrier for account %v not found", want.accountID)
		}
		barrier := accountAssets[index]
		if !barrier.Asset.Equal(asset) {
			t.Fatalf("barrier for account %v asset mismatch", want.accountID)
		}
		maxQuantity, ok := barrier.Limit.MaxQuantity.Get()
		wantMaxQuantity, parseErr := param.NewQuantityFromString(want.maxQuantity)
		if parseErr != nil {
			t.Fatalf("parse max quantity: %v", parseErr)
		}
		if !ok || !maxQuantity.Equal(wantMaxQuantity) {
			t.Fatalf("barrier for account %v max_quantity mismatch", want.accountID)
		}
		if barrier.Limit.MaxNotional.IsSet() {
			t.Fatalf(
				"barrier for account %v unexpectedly carries max_notional",
				want.accountID,
			)
		}
	}
}

func TestOrderSizeAxes_KeepsDifferentAssetsSeparate(t *testing.T) {
	t.Parallel()
	res := testResolver()
	usd, err := res.asset("USD")
	if err != nil {
		t.Fatalf("resolve USD: %v", err)
	}
	aapl, err := res.asset("AAPL")
	if err != nil {
		t.Fatalf("resolve AAPL: %v", err)
	}
	_, assets, _, err := orderSizeAxes([]domain.LimitOrderSize{
		orderSize(domain.ScopeUnderlyingAsset, "", "USD", "10", ""),
		orderSize(domain.ScopeUnderlyingAsset, "", "AAPL", "20", ""),
	}, res)
	if err != nil {
		t.Fatalf("orderSizeAxes: %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("asset barriers = %d, want 2", len(assets))
	}
	barrierIndexes := make(map[string]int, len(assets))
	for index, barrier := range assets {
		barrierIndexes[barrier.Asset.Safe()] = index
	}
	usdIndex, ok := barrierIndexes[usd.Safe()]
	if !ok {
		t.Fatal("barrier for USD not found")
	}
	aaplIndex, ok := barrierIndexes[aapl.Safe()]
	if !ok {
		t.Fatal("barrier for AAPL not found")
	}
	if assets[usdIndex].Asset.Equal(assets[aaplIndex].Asset) {
		t.Fatal("USD and AAPL barriers have the same asset")
	}
	for _, want := range []struct {
		asset       param.Asset
		maxQuantity string
	}{
		{asset: usd, maxQuantity: "10"},
		{asset: aapl, maxQuantity: "20"},
	} {
		index := barrierIndexes[want.asset.Safe()]
		barrier := assets[index]
		if !barrier.Asset.Equal(want.asset) {
			t.Fatalf("barrier for asset %q mismatch", want.asset.Safe())
		}
		maxQuantity, ok := barrier.Limit.MaxQuantity.Get()
		wantMaxQuantity, parseErr := param.NewQuantityFromString(want.maxQuantity)
		if parseErr != nil {
			t.Fatalf("parse max quantity: %v", parseErr)
		}
		if !ok || !maxQuantity.Equal(wantMaxQuantity) {
			t.Fatalf("barrier for asset %q max_quantity mismatch", want.asset.Safe())
		}
	}
}

func TestOrderSizeAxes_KeepsDifferentAccountAssetsSeparate(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1")
	accountID, err := res.account("acc-1")
	if err != nil {
		t.Fatalf("resolve acc-1: %v", err)
	}
	usd, err := res.asset("USD")
	if err != nil {
		t.Fatalf("resolve USD: %v", err)
	}
	aapl, err := res.asset("AAPL")
	if err != nil {
		t.Fatalf("resolve AAPL: %v", err)
	}
	_, _, accountAssets, err := orderSizeAxes([]domain.LimitOrderSize{
		orderSize(domain.ScopeAccountUnderlyingAsset, "acc-1", "USD", "10", ""),
		orderSize(domain.ScopeAccountUnderlyingAsset, "acc-1", "AAPL", "20", ""),
	}, res)
	if err != nil {
		t.Fatalf("orderSizeAxes: %v", err)
	}
	if len(accountAssets) != 2 {
		t.Fatalf("account-asset barriers = %d, want 2", len(accountAssets))
	}
	barrierIndexes := make(map[string]int, len(accountAssets))
	for index, barrier := range accountAssets {
		barrierIndexes[barrier.Asset.Safe()] = index
	}
	usdIndex, ok := barrierIndexes[usd.Safe()]
	if !ok {
		t.Fatal("barrier for acc-1/USD not found")
	}
	aaplIndex, ok := barrierIndexes[aapl.Safe()]
	if !ok {
		t.Fatal("barrier for acc-1/AAPL not found")
	}
	if accountAssets[usdIndex].Asset.Equal(accountAssets[aaplIndex].Asset) {
		t.Fatal("acc-1 USD and AAPL barriers have the same asset")
	}
	for _, want := range []struct {
		asset       param.Asset
		maxQuantity string
	}{
		{asset: usd, maxQuantity: "10"},
		{asset: aapl, maxQuantity: "20"},
	} {
		index := barrierIndexes[want.asset.Safe()]
		barrier := accountAssets[index]
		if barrier.AccountID != accountID {
			t.Fatalf(
				"barrier for asset %q account = %v, want %v",
				want.asset.Safe(),
				barrier.AccountID,
				accountID,
			)
		}
		if !barrier.Asset.Equal(want.asset) {
			t.Fatalf("barrier for asset %q mismatch", want.asset.Safe())
		}
		maxQuantity, ok := barrier.Limit.MaxQuantity.Get()
		wantMaxQuantity, parseErr := param.NewQuantityFromString(want.maxQuantity)
		if parseErr != nil {
			t.Fatalf("parse max quantity: %v", parseErr)
		}
		if !ok || !maxQuantity.Equal(wantMaxQuantity) {
			t.Fatalf("barrier for asset %q max_quantity mismatch", want.asset.Safe())
		}
	}
}

func TestOrderSizeAxes_RejectsDuplicateCapForSDKKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		accounts []string
		limits   []domain.LimitOrderSize
		want     string
	}{
		{
			name: "quantity",
			limits: []domain.LimitOrderSize{
				orderSize(domain.ScopeUnderlyingAsset, "", "USD", "10", ""),
				orderSize(domain.ScopeUnderlyingAsset, "", "USD", "20", ""),
			},
			want: "duplicate order_size_limit max_quantity for asset \"USD\"",
		},
		{
			name: "notional",
			limits: []domain.LimitOrderSize{
				orderSize(domain.ScopeSettlementAsset, "", "USD", "", "1000"),
				orderSize(domain.ScopeSettlementAsset, "", "USD", "", "2000"),
			},
			want: "duplicate order_size_limit max_notional for asset \"USD\"",
		},
		{
			name:     "account quantity",
			accounts: []string{"acc-1"},
			limits: []domain.LimitOrderSize{
				orderSize(
					domain.ScopeAccountUnderlyingAsset,
					"acc-1",
					"USD",
					"10",
					"",
				),
				orderSize(
					domain.ScopeAccountUnderlyingAsset,
					"acc-1",
					"USD",
					"20",
					"",
				),
			},
			want: "duplicate order_size_limit max_quantity for account \"acc-1\" asset \"USD\"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, err := orderSizeAxes(tc.limits, testResolver(tc.accounts...))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

// TestRateLimitAxes_AllAxes checks the rate-limit Configure axes are built with
// always-non-nil slices and the broker barrier set from the broker-scoped
// limit.
func TestRateLimitAxes_AllAxes(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1")
	limits := []domain.LimitRate{
		rateLimit(domain.ScopeBroker, "", "", 1000, time.Minute),
		rateLimit(domain.ScopeAsset, "", "USD", 500, time.Minute),
		rateLimit(domain.ScopeAccount, "acc-1", "", 200, time.Minute),
		rateLimit(domain.ScopeAccountAsset, "acc-1", "USD", 100, time.Minute),
	}
	broker, assets, accounts, accountAssets, err := rateLimitAxes(limits, res)
	if err != nil {
		t.Fatalf("rateLimitAxes: %v", err)
	}
	if broker == nil {
		t.Fatalf("want broker barrier")
	}
	if assets == nil || accounts == nil || accountAssets == nil {
		t.Fatalf("axes must be non-nil so Configure touches each axis")
	}
	if len(assets) != 1 || len(accounts) != 1 || len(accountAssets) != 1 {
		t.Fatalf("axis counts wrong: %d %d %d",
			len(assets), len(accounts), len(accountAssets))
	}
}

// TestOrderSizeAxes_EmptyAxesNonNil checks empty axes are empty non-nil slices
// so a Configure call clears them rather than leaving them unchanged.
func TestOrderSizeAxes_EmptyAxesNonNil(t *testing.T) {
	t.Parallel()
	broker, assets, accountAssets, err := orderSizeAxes(
		[]domain.LimitOrderSize{orderSize(domain.ScopeBroker, "", "", "10", "")}, testResolver())
	if err != nil {
		t.Fatalf("orderSizeAxes: %v", err)
	}
	if broker == nil {
		t.Fatalf("want broker barrier")
	}
	if assets == nil || accountAssets == nil {
		t.Fatalf("empty axes must be non-nil slices")
	}
	if len(assets) != 0 || len(accountAssets) != 0 {
		t.Fatalf("want empty asset/account-asset axes")
	}
}

// TestRateLimitAxes_SecondBrokerBarrierIsError checks the runtime axes refuse a
// second broker barrier instead of quietly configuring the last one.
func TestRateLimitAxes_SecondBrokerBarrierIsError(t *testing.T) {
	t.Parallel()
	_, _, _, _, err := rateLimitAxes([]domain.LimitRate{
		rateLimit(domain.ScopeBroker, "", "", 1000, time.Minute),
		rateLimit(domain.ScopeBroker, "", "", 10, time.Minute),
	}, testResolver())
	if err == nil {
		t.Fatal("want error for a second broker barrier, got nil")
	}
	if !strings.Contains(err.Error(), "more than one broker barrier") {
		t.Fatalf("error = %v, want more than one broker barrier", err)
	}
}

// TestOrderSizeAxes_SecondBrokerBarrierIsError is the order-size counterpart of
// TestRateLimitAxes_SecondBrokerBarrierIsError.
func TestOrderSizeAxes_SecondBrokerBarrierIsError(t *testing.T) {
	t.Parallel()
	_, _, _, err := orderSizeAxes([]domain.LimitOrderSize{
		orderSize(domain.ScopeBroker, "", "", "10", ""),
		orderSize(domain.ScopeBroker, "", "", "1", ""),
	}, testResolver())
	if err == nil {
		t.Fatal("want error for a second broker barrier, got nil")
	}
	if !strings.Contains(err.Error(), "more than one broker barrier") {
		t.Fatalf("error = %v, want more than one broker barrier", err)
	}
}

// TestSpotFundsPnlBoundsAxes_SecondGlobalBarrierIsError checks a second global
// P&L bound is refused instead of overwriting the first one in the loop.
func TestSpotFundsPnlBoundsAxes_SecondGlobalBarrierIsError(t *testing.T) {
	t.Parallel()
	_, _, _, err := spotFundsPnlBoundsAxes(
		[]domain.LimitSpotFundsPnlBounds{
			{Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-1000"},
			{Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-10"},
		},
		testResolver(),
	)
	if err == nil {
		t.Fatal("want error for a second global barrier, got nil")
	}
	if !strings.Contains(err.Error(), "more than one global barrier") {
		t.Fatalf("error = %v, want more than one global barrier", err)
	}
}

func TestSpotFundsPnlBoundsAxes_DistributesAndUsesNonNilSlices(t *testing.T) {
	t.Parallel()
	res, err := newIDResolver(
		[]domain.Account{account("acc-1")},
		[]domain.AccountGroup{{Code: "desk-a", EngineGroupID: 7}},
		testAssets(),
	)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	global, groups, accounts, err := spotFundsPnlBoundsAxes(
		[]domain.LimitSpotFundsPnlBounds{
			{
				Scope:      domain.ScopeGlobal,
				Currency:   "USD",
				LowerBound: "-1000",
			},
			{
				Scope:        domain.ScopeAccountGroup,
				AccountGroup: "desk-a",
				Currency:     "EUR",
				UpperBound:   "500",
			},
			{
				Scope:      domain.ScopeAccount,
				Account:    "acc-1",
				Currency:   "GBP",
				LowerBound: "-100",
				UpperBound: "100",
			},
		},
		res,
	)
	if err != nil {
		t.Fatalf("spotFundsPnlBoundsAxes: %v", err)
	}
	globalBarrier, globalSet := global.Get()
	if !globalSet || globalBarrier == nil || groups == nil || accounts == nil {
		t.Fatalf("axes must set the global barrier and non-nil account slices")
	}
	if len(groups) != 1 || len(accounts) != 1 {
		t.Fatalf(
			"axis counts wrong: global=%+v groups=%d accounts=%d",
			globalBarrier,
			len(groups),
			len(accounts),
		)
	}
	if _, ok := globalBarrier.LowerBound.Get(); !ok {
		t.Fatalf("global lower bound not mapped: %+v", globalBarrier)
	}
	if got := globalBarrier.Currency.String(); got != "2" {
		t.Fatalf("global currency = %q, want decimal engine id 2", got)
	}
	if _, ok := groups[0].Barrier.UpperBound.Get(); !ok {
		t.Fatalf("account-group upper bound not mapped: %+v", groups[0])
	}
	if got := groups[0].Barrier.Currency.String(); got != "5" {
		t.Fatalf("account-group currency = %q, want decimal engine id 5", got)
	}
	if _, ok := accounts[0].Barrier.LowerBound.Get(); !ok {
		t.Fatalf("P&L bounds not mapped: %+v %+v %+v", globalBarrier, groups, accounts)
	}
	if got := accounts[0].Barrier.Currency.String(); got != "7" {
		t.Fatalf("account currency = %q, want decimal engine id 7", got)
	}
}

func TestBuildEngine_RegistersRiskPolicies(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets:          testAssets(),
		Accounts:        []domain.Account{account("acc-1")},
		RateLimits:      []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 100, time.Second)},
		OrderSizeLimits: []domain.LimitOrderSize{orderSize(domain.ScopeBroker, "", "", "10", "")},
	}
	res, err := newIDResolver(snap.Accounts, snap.Groups, snap.Assets)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	eng, service, registered, _, _, err := buildEngine(snap, res, nil)
	if err != nil {
		t.Fatalf("buildEngine: %v", err)
	}
	defer service.Close()
	defer eng.Stop()

	for _, name := range []string{
		nameRateLimit, nameOrderSizeLimit,
	} {
		if _, ok := registered[name]; !ok {
			t.Fatalf("policy %q not registered", name)
		}
	}
}

// TestNewIDResolver_RejectsUnassignedEngineID checks the resolver build rejects
// an account whose stored engine id is unassigned (zero), since that is
// corruption of our own persisted ids, not a hashable input.
func TestNewIDResolver_RejectsUnassignedEngineID(t *testing.T) {
	t.Parallel()
	_, err := newIDResolver(
		[]domain.Account{{Code: "acc-1", EngineAccountID: 0}}, nil, nil)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unassigned engine id, got %v", err)
	}
}

// TestNewIDResolver_UsesStoredEngineIDs checks the resolver maps dictionary
// codes to values constructed from their stored engine ids, never hashes. Asset
// ids become the decimal opaque strings accepted by the native engine.
func TestNewIDResolver_UsesStoredEngineIDs(t *testing.T) {
	t.Parallel()
	res, err := newIDResolver([]domain.Account{
		{Code: "acc-1", EngineAccountID: 7},
	}, []domain.AccountGroup{
		{Code: "grp-1", EngineGroupID: 9},
	}, []domain.Asset{{Code: "human-usd", EngineAssetID: 42}})
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	got, err := res.account("acc-1")
	if err != nil {
		t.Fatalf("resolve account: %v", err)
	}
	if want := param.NewAccountIDFromUint64(7); got.String() != want.String() {
		t.Fatalf("account engine id = %s, want %s (from stored uint, not hash)", got, want)
	}
	grp, err := res.group("grp-1")
	if err != nil {
		t.Fatalf("resolve group: %v", err)
	}
	want, err := param.NewAccountGroupIDFromUint32(9)
	if err != nil {
		t.Fatalf("want group id: %v", err)
	}
	if grp.String() != want.String() {
		t.Fatalf("group engine id = %s, want %s", grp, want)
	}
	asset, err := res.asset("human-usd")
	if err != nil {
		t.Fatalf("resolve asset: %v", err)
	}
	if got := asset.String(); got != "42" {
		t.Fatalf("asset engine id = %q, want decimal stored id 42", got)
	}
	if alias, err := res.assetAlias(asset); err != nil || alias != "human-usd" {
		t.Fatalf("reverse asset alias = (%q, %v), want human-usd", alias, err)
	}
}

func TestIDResolver_RejectsInvalidAssetPublicationsWithoutPartialChange(t *testing.T) {
	t.Parallel()
	res, err := newIDResolver(nil, nil, []domain.Asset{{Code: "usd", EngineAssetID: 2}})
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	for _, mutation := range []struct {
		name  string
		asset domain.Asset
		want  error
	}{
		{
			name:  "duplicate alias",
			asset: domain.Asset{Code: "usd", EngineAssetID: 3},
			want:  domain.ErrAlreadyExists,
		},
		{
			name:  "duplicate engine id",
			asset: domain.Asset{Code: "eur", EngineAssetID: 2},
			want:  domain.ErrInvalid,
		},
		{
			name:  "unassigned engine id",
			asset: domain.Asset{Code: "eur", EngineAssetID: 0},
			want:  domain.ErrInvalid,
		},
	} {
		if err := res.addAssetResolverEntry(mutation.asset); !errors.Is(err, mutation.want) {
			t.Errorf("%s error = %v, want %v", mutation.name, err, mutation.want)
		}
	}
	asset, err := res.asset("usd")
	if err != nil || asset.String() != "2" {
		t.Fatalf("published asset = (%q, %v), want decimal id 2", asset.String(), err)
	}
	if _, err := res.asset("eur"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("partial asset publication error = %v, want ErrInvalid", err)
	}
}

func TestIDResolver_AddsAndRenamesAliases(t *testing.T) {
	t.Parallel()
	res, err := newIDResolver(
		[]domain.Account{{Code: "account-old", EngineAccountID: 7}},
		[]domain.AccountGroup{{Code: "group-old", EngineGroupID: 9}},
		nil,
	)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	if err := res.addAccountResolverEntry(domain.Account{
		Code: "account-added", EngineAccountID: 8,
	}); err != nil {
		t.Fatalf("add account resolver entry: %v", err)
	}
	if err := res.addGroupResolverEntry(domain.AccountGroup{
		Code: "group-added", EngineGroupID: 10,
	}); err != nil {
		t.Fatalf("add group resolver entry: %v", err)
	}
	if err := res.renameAccountResolverEntry("account-old", domain.Account{
		Code: "account-new", EngineAccountID: 7,
	}); err != nil {
		t.Fatalf("rename account resolver entry: %v", err)
	}
	if err := res.renameGroupResolverEntry("group-old", domain.AccountGroup{
		Code: "group-new", EngineGroupID: 9,
	}); err != nil {
		t.Fatalf("rename group resolver entry: %v", err)
	}
	if err := res.addAssetResolverEntry(domain.Asset{
		Code: "asset-old", EngineAssetID: 11,
	}); err != nil {
		t.Fatalf("add asset resolver entry: %v", err)
	}
	assetBefore, err := res.asset("asset-old")
	if err != nil {
		t.Fatalf("resolve asset before rename: %v", err)
	}
	if err := res.renameAssetResolverEntry("asset-old", domain.Asset{
		Code: "asset-new", EngineAssetID: 11,
	}); err != nil {
		t.Fatalf("rename asset resolver entry: %v", err)
	}
	assetAfter, err := res.asset("asset-new")
	if err != nil {
		t.Fatalf("resolve renamed asset: %v", err)
	}
	if !assetAfter.Equal(assetBefore) {
		t.Fatal("asset rename rebuilt the ready engine asset")
	}

	for code, want := range map[domain.AccountID]uint64{
		"account-added": 8,
		"account-new":   7,
	} {
		id, err := res.account(code)
		if err != nil {
			t.Fatalf("resolve account %q: %v", code, err)
		}
		if got := uint64(id.Handle()); got != want {
			t.Errorf("account %q engine id = %d, want %d", code, got, want)
		}
	}
	for code, want := range map[string]uint32{
		"group-added": 10,
		"group-new":   9,
	} {
		id, err := res.group(code)
		if err != nil {
			t.Fatalf("resolve group %q: %v", code, err)
		}
		if got := uint32(id.Handle()); got != want {
			t.Errorf("group %q engine id = %d, want %d", code, got, want)
		}
	}
	if _, err := res.account("account-old"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("old account alias error = %v, want ErrInvalid", err)
	}
	if _, err := res.group("group-old"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("old group alias error = %v, want ErrInvalid", err)
	}
	if _, err := res.asset("asset-old"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("old asset alias error = %v, want ErrInvalid", err)
	}
}

func TestIDResolver_AssetRenameRejectsWithoutChangingAliases(t *testing.T) {
	t.Parallel()
	res, err := newIDResolver(nil, nil, []domain.Asset{
		{Code: "asset-a", EngineAssetID: 11},
		{Code: "asset-b", EngineAssetID: 12},
	})
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	original, err := res.asset("asset-a")
	if err != nil {
		t.Fatalf("resolve original asset: %v", err)
	}

	for _, rename := range []struct {
		oldCode string
		asset   domain.Asset
	}{
		{oldCode: "missing", asset: domain.Asset{Code: "asset-c", EngineAssetID: 11}},
		{oldCode: "asset-a", asset: domain.Asset{Code: "asset-c", EngineAssetID: 99}},
		{oldCode: "asset-a", asset: domain.Asset{Code: "asset-b", EngineAssetID: 11}},
		{oldCode: "asset-a", asset: domain.Asset{Code: "asset-c", EngineAssetID: 0}},
	} {
		if err := res.renameAssetResolverEntry(rename.oldCode, rename.asset); err == nil {
			t.Errorf("rename %+v: want error", rename)
		}
	}

	current, err := res.asset("asset-a")
	if err != nil || !current.Equal(original) {
		t.Fatalf("asset-a after failed renames = (%v, %v)", current, err)
	}
	if alias, err := res.assetAlias(original); err != nil || alias != "asset-a" {
		t.Fatalf("asset-a reverse alias after failed renames = (%q, %v)", alias, err)
	}
	if _, err := res.asset("asset-c"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("partial asset alias published: %v", err)
	}
}

func TestIDResolver_RejectsInvalidMutationsWithoutPartialChange(t *testing.T) {
	t.Parallel()
	res, err := newIDResolver(
		[]domain.Account{
			{Code: "account-a", EngineAccountID: 7},
			{Code: "account-b", EngineAccountID: 8},
		},
		[]domain.AccountGroup{
			{Code: "group-a", EngineGroupID: 9},
			{Code: "group-b", EngineGroupID: 10},
		},
		nil,
	)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}

	accountMutations := []struct {
		name string
		err  error
	}{
		{
			name: "duplicate alias",
			err: res.addAccountResolverEntry(domain.Account{
				Code: "account-a", EngineAccountID: 11,
			}),
		},
		{
			name: "duplicate engine id",
			err: res.addAccountResolverEntry(domain.Account{
				Code: "account-c", EngineAccountID: 7,
			}),
		},
		{
			name: "unknown old alias",
			err: res.renameAccountResolverEntry("account-missing", domain.Account{
				Code: "account-c", EngineAccountID: 7,
			}),
		},
		{
			name: "mismatched target id",
			err: res.renameAccountResolverEntry("account-a", domain.Account{
				Code: "account-c", EngineAccountID: 12,
			}),
		},
		{
			name: "duplicate target alias",
			err: res.renameAccountResolverEntry("account-a", domain.Account{
				Code: "account-b", EngineAccountID: 7,
			}),
		},
		{
			name: "corrupt target id",
			err: res.renameAccountResolverEntry("account-a", domain.Account{
				Code: "account-c", EngineAccountID: 0,
			}),
		},
	}
	for _, mutation := range accountMutations {
		if mutation.err == nil {
			t.Errorf("%s: want error", mutation.name)
		}
	}
	for code, want := range map[domain.AccountID]uint64{
		"account-a": 7,
		"account-b": 8,
	} {
		id, err := res.account(code)
		if err != nil || uint64(id.Handle()) != want {
			t.Errorf("account %q after failed mutations = (%v, %v), want id %d", code, id, err, want)
		}
	}
	if _, err := res.account("account-c"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("partial account alias published: %v", err)
	}

	groupMutations := []error{
		res.addGroupResolverEntry(domain.AccountGroup{
			Code: "group-a", EngineGroupID: 11,
		}),
		res.addGroupResolverEntry(domain.AccountGroup{
			Code: "group-c", EngineGroupID: 9,
		}),
		res.renameGroupResolverEntry("group-missing", domain.AccountGroup{
			Code: "group-c", EngineGroupID: 9,
		}),
		res.renameGroupResolverEntry("group-a", domain.AccountGroup{
			Code: "group-c", EngineGroupID: 12,
		}),
		res.renameGroupResolverEntry("group-a", domain.AccountGroup{
			Code: "group-b", EngineGroupID: 9,
		}),
		res.renameGroupResolverEntry("group-a", domain.AccountGroup{
			Code: "group-c", EngineGroupID: 0,
		}),
	}
	for i, mutationErr := range groupMutations {
		if mutationErr == nil {
			t.Errorf("group mutation %d: want error", i)
		}
	}
	for code, want := range map[string]uint32{"group-a": 9, "group-b": 10} {
		id, err := res.group(code)
		if err != nil || uint32(id.Handle()) != want {
			t.Errorf("group %q after failed mutations = (%v, %v), want id %d", code, id, err, want)
		}
	}
	if _, err := res.group("group-c"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("partial group alias published: %v", err)
	}
}

func TestIDResolver_RemovesAssetAliasWithStableIDValidation(t *testing.T) {
	t.Parallel()
	asset := domain.Asset{Code: "asset-a", EngineAssetID: 11}
	res, err := newIDResolver(nil, nil, []domain.Asset{asset})
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	ready, err := res.asset(asset.Code)
	if err != nil {
		t.Fatalf("resolve asset: %v", err)
	}

	if err := res.removeAssetResolverEntry(domain.Asset{
		Code: asset.Code, EngineAssetID: 12,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("mismatched removal error = %v, want ErrInvalid", err)
	}
	current, err := res.asset(asset.Code)
	if err != nil || !current.Equal(ready) {
		t.Fatalf("mismatched removal changed code alias = (%v, %v)", current, err)
	}
	byID, err := res.assetByID(asset.EngineAssetID)
	if err != nil || !byID.Equal(ready) {
		t.Fatalf("mismatched removal changed id alias = (%v, %v)", byID, err)
	}
	if alias, err := res.assetAlias(ready); err != nil || alias != asset.Code {
		t.Fatalf("mismatched removal changed reverse alias = (%q, %v)", alias, err)
	}

	if err := res.removeAssetResolverEntry(asset); err != nil {
		t.Fatalf("remove asset resolver entry: %v", err)
	}
	if _, err := res.asset(asset.Code); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("removed code alias error = %v, want ErrInvalid", err)
	}
	if _, err := res.assetByID(asset.EngineAssetID); err == nil {
		t.Fatal("removed id alias lookup succeeded")
	} else {
		if !errors.Is(err, marketdata.ErrUnknownAsset) {
			t.Fatalf("removed id alias error = %v, want ErrUnknownAsset", err)
		}
		if errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("removed id alias error = %v, must not be ErrNotFound", err)
		}
	}
	if _, err := res.assetAlias(ready); err == nil {
		t.Fatal("removed reverse alias lookup succeeded")
	}
}

func TestIDResolver_AssetRemovalRejectsWithoutChangingAliases(t *testing.T) {
	t.Parallel()
	asset := domain.Asset{Code: "asset-a", EngineAssetID: 11}
	res, err := newIDResolver(nil, nil, []domain.Asset{asset})
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	ready, err := res.asset(asset.Code)
	if err != nil {
		t.Fatalf("resolve asset: %v", err)
	}

	for _, removal := range []domain.Asset{
		{Code: "missing", EngineAssetID: asset.EngineAssetID},
		{Code: asset.Code, EngineAssetID: 12},
	} {
		if err := res.removeAssetResolverEntry(removal); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("remove %+v error = %v, want ErrInvalid", removal, err)
		}
	}
	current, err := res.asset(asset.Code)
	if err != nil || !current.Equal(ready) {
		t.Fatalf("failed removals changed code alias = (%v, %v)", current, err)
	}
	byID, err := res.assetByID(asset.EngineAssetID)
	if err != nil || !byID.Equal(ready) {
		t.Fatalf("failed removals changed id alias = (%v, %v)", byID, err)
	}
	if alias, err := res.assetAlias(ready); err != nil || alias != asset.Code {
		t.Fatalf("failed removals changed reverse alias = (%q, %v)", alias, err)
	}
}

func TestIDResolver_UninitializedAssetMutationsAreInternalErrors(t *testing.T) {
	t.Parallel()
	var res idResolver
	for _, test := range []struct {
		name string
		err  error
	}{
		{
			name: "rename",
			err: res.renameAssetResolverEntry("asset-a", domain.Asset{
				Code: "asset-b", EngineAssetID: 11,
			}),
		},
		{
			name: "remove",
			err: res.removeAssetResolverEntry(domain.Asset{
				Code: "asset-a", EngineAssetID: 11,
			}),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.err == nil {
				t.Fatal("error = nil, want internal initialization error")
			}
			if errors.Is(test.err, domain.ErrInvalid) {
				t.Fatalf("error = %v, must not wrap ErrInvalid", test.err)
			}
			const want = "engine: asset resolver is not initialized"
			if test.err.Error() != want {
				t.Fatalf("error = %q, want %q", test.err, want)
			}
		})
	}
}

func TestIDResolver_AssetRemovalRejectsCorruptReverseAlias(t *testing.T) {
	t.Parallel()
	asset := domain.Asset{Code: "asset-a", EngineAssetID: 11}
	res, err := newIDResolver(nil, nil, []domain.Asset{asset})
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	ready, err := res.asset(asset.Code)
	if err != nil {
		t.Fatalf("resolve asset: %v", err)
	}

	res.shared.mu.Lock()
	res.shared.assetAliases[ready.Safe()] = "asset-b"
	res.shared.mu.Unlock()
	before := captureAssetResolverState(res)

	err = res.removeAssetResolverEntry(asset)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("remove error = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "corrupt engine id mapping") {
		t.Fatalf("remove error = %q, want corrupt mapping detail", err)
	}
	after := captureAssetResolverState(res)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("corrupt removal changed resolver: before=%v after=%v", before, after)
	}
}

type assetResolverMutationKind uint8

const (
	assetResolverAdd assetResolverMutationKind = iota
	assetResolverRename
	assetResolverRemove
)

type assetResolverMutation struct {
	kind    assetResolverMutationKind
	oldCode string
	asset   domain.Asset
}

func (m assetResolverMutation) String() string {
	names := [...]string{"add", "rename", "remove"}
	return fmt.Sprintf(
		"%s old=%q code=%q id=%d",
		names[m.kind], m.oldCode, m.asset.Code, m.asset.EngineAssetID,
	)
}

type assetResolverStateSnapshot struct {
	assets       map[string]string
	assetIDs     map[domain.EngineAssetID]string
	assetAliases map[string]string
}

func captureAssetResolverState(res idResolver) assetResolverStateSnapshot {
	res.shared.mu.RLock()
	defer res.shared.mu.RUnlock()

	snapshot := assetResolverStateSnapshot{
		assets:       make(map[string]string, len(res.shared.assets)),
		assetIDs:     make(map[domain.EngineAssetID]string, len(res.shared.assetIDs)),
		assetAliases: make(map[string]string, len(res.shared.assetAliases)),
	}
	for code, ready := range res.shared.assets {
		snapshot.assets[code] = ready.Safe()
	}
	for id, ready := range res.shared.assetIDs {
		snapshot.assetIDs[id] = ready.Safe()
	}
	for id, code := range res.shared.assetAliases {
		snapshot.assetAliases[id] = code
	}
	return snapshot
}

func assertAssetResolverMatchesModel(
	t *testing.T,
	res idResolver,
	model map[string]domain.EngineAssetID,
) {
	t.Helper()
	snapshot := captureAssetResolverState(res)
	if len(snapshot.assets) != len(model) ||
		len(snapshot.assetIDs) != len(model) ||
		len(snapshot.assetAliases) != len(model) {
		t.Fatalf(
			"resolver map sizes = (%d, %d, %d), want %d",
			len(snapshot.assets),
			len(snapshot.assetIDs),
			len(snapshot.assetAliases),
			len(model),
		)
	}
	for code, id := range model {
		readyID := strconv.FormatUint(id.Uint64(), 10)
		if got, ok := snapshot.assets[code]; !ok || got != readyID {
			t.Fatalf("assets[%q] = (%q, %t), want %q", code, got, ok, readyID)
		}
		if got, ok := snapshot.assetIDs[id]; !ok || got != readyID {
			t.Fatalf("assetIDs[%d] = (%q, %t), want %q", id, got, ok, readyID)
		}
		if got, ok := snapshot.assetAliases[readyID]; !ok || got != code {
			t.Fatalf(
				"assetAliases[%q] = (%q, %t), want %q",
				readyID, got, ok, code,
			)
		}
	}
}

func assetResolverModelContainsID(
	model map[string]domain.EngineAssetID,
	target domain.EngineAssetID,
) bool {
	for _, id := range model {
		if id == target {
			return true
		}
	}
	return false
}

func applyAssetResolverMutation(
	t *testing.T,
	res idResolver,
	model map[string]domain.EngineAssetID,
	mutation assetResolverMutation,
) {
	t.Helper()
	before := captureAssetResolverState(res)
	idValid := domain.ValidateEngineAssetID(mutation.asset.EngineAssetID) == nil
	var wantSuccess bool
	var err error

	switch mutation.kind {
	case assetResolverAdd:
		_, codeExists := model[mutation.asset.Code]
		wantSuccess = idValid &&
			!codeExists &&
			!assetResolverModelContainsID(model, mutation.asset.EngineAssetID)
		err = res.addAssetResolverEntry(mutation.asset)
	case assetResolverRename:
		currentID, oldExists := model[mutation.oldCode]
		_, targetExists := model[mutation.asset.Code]
		wantSuccess = idValid &&
			oldExists &&
			currentID == mutation.asset.EngineAssetID &&
			(mutation.oldCode == mutation.asset.Code || !targetExists)
		err = res.renameAssetResolverEntry(mutation.oldCode, mutation.asset)
	case assetResolverRemove:
		currentID, codeExists := model[mutation.asset.Code]
		wantSuccess = idValid &&
			codeExists &&
			currentID == mutation.asset.EngineAssetID
		err = res.removeAssetResolverEntry(mutation.asset)
	default:
		t.Fatalf("unknown mutation kind %d", mutation.kind)
	}

	if (err == nil) != wantSuccess {
		t.Fatalf("%s error = %v, want success %t", mutation, err, wantSuccess)
	}
	if err != nil {
		after := captureAssetResolverState(res)
		if !reflect.DeepEqual(after, before) {
			t.Fatalf(
				"failed %s changed resolver: before=%v after=%v",
				mutation, before, after,
			)
		}
		assertAssetResolverMatchesModel(t, res, model)
		return
	}

	switch mutation.kind {
	case assetResolverAdd:
		model[mutation.asset.Code] = mutation.asset.EngineAssetID
	case assetResolverRename:
		delete(model, mutation.oldCode)
		model[mutation.asset.Code] = mutation.asset.EngineAssetID
	case assetResolverRemove:
		delete(model, mutation.asset.Code)
		if _, err := res.asset(mutation.asset.Code); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("removed code %q resolves with error %v", mutation.asset.Code, err)
		}
	}
	assertAssetResolverMatchesModel(t, res, model)
}

func TestIDResolver_RandomizedAssetMutationsPreserveInvariants(t *testing.T) {
	t.Parallel()
	codes := []string{"asset-a", "asset-b", "asset-c", "asset-d"}
	ids := []domain.EngineAssetID{0, 11, 12, 13}

	for _, seed := range []int64{1, 23, 101, 4099} {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			res := newEmptyIDResolver(0, 0, len(codes))
			model := make(map[string]domain.EngineAssetID, len(codes))

			original := domain.Asset{Code: codes[0], EngineAssetID: ids[1]}
			applyAssetResolverMutation(t, res, model, assetResolverMutation{
				kind: assetResolverAdd, asset: original,
			})
			applyAssetResolverMutation(t, res, model, assetResolverMutation{
				kind: assetResolverRemove, asset: original,
			})
			readded := domain.Asset{Code: codes[1], EngineAssetID: ids[1]}
			applyAssetResolverMutation(t, res, model, assetResolverMutation{
				kind: assetResolverAdd, asset: readded,
			})
			if _, err := res.asset(codes[0]); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("removed code %q resolves with error %v", codes[0], err)
			}
			if _, err := res.assetByID(ids[1]); err != nil {
				t.Fatalf("re-added engine id %d: %v", ids[1], err)
			}

			random := rand.New(rand.NewSource(seed))
			for step := 0; step < 250; step++ {
				applyAssetResolverMutation(t, res, model, assetResolverMutation{
					kind:    assetResolverMutationKind(random.Intn(3)),
					oldCode: codes[random.Intn(len(codes))],
					asset: domain.Asset{
						Code:          codes[random.Intn(len(codes))],
						EngineAssetID: ids[random.Intn(len(ids))],
					},
				})
			}
		})
	}
}

func TestIDResolver_RemovesGroupAliasWithStableIDValidation(t *testing.T) {
	t.Parallel()
	res, err := newIDResolver(nil, []domain.AccountGroup{{
		Code: "group-a", EngineGroupID: 9,
	}}, nil)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}

	if err := res.removeGroupResolverEntry(domain.AccountGroup{
		Code: "group-a", EngineGroupID: 10,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("mismatched removal error = %v, want ErrInvalid", err)
	}
	if id, err := res.group("group-a"); err != nil || uint32(id.Handle()) != 9 {
		t.Fatalf("mismatched removal changed alias = (%v, %v)", id, err)
	}

	if err := res.removeGroupResolverEntry(domain.AccountGroup{
		Code: "group-a", EngineGroupID: 9,
	}); err != nil {
		t.Fatalf("remove group resolver entry: %v", err)
	}
	if _, err := res.group("group-a"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("removed group alias error = %v, want ErrInvalid", err)
	}
	if err := res.removeGroupResolverEntry(domain.AccountGroup{
		Code: "group-a", EngineGroupID: 9,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown removal error = %v, want ErrInvalid", err)
	}
}

func TestIDResolver_ConcurrentLookupAndMutation(t *testing.T) {
	res, err := newIDResolver(
		[]domain.Account{
			{Code: "stable-account", EngineAccountID: 7},
			{Code: "moving-account-a", EngineAccountID: 8},
		},
		[]domain.AccountGroup{
			{Code: "stable-group", EngineGroupID: 9},
			{Code: "moving-group-a", EngineGroupID: 10},
		},
		nil,
	)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}

	const iterations = 500
	errCh := make(chan error, 9)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func(resolver idResolver) {
			defer wg.Done()
			for range iterations {
				if id, err := resolver.account("stable-account"); err != nil ||
					uint64(id.Handle()) != 7 {
					errCh <- fmt.Errorf("stable account lookup = (%v, %v)", id, err)
					return
				}
				if id, err := resolver.group("stable-group"); err != nil ||
					uint32(id.Handle()) != 9 {
					errCh <- fmt.Errorf("stable group lookup = (%v, %v)", id, err)
					return
				}
			}
		}(res)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		accountOld, accountNew := domain.AccountID("moving-account-a"), domain.AccountID("moving-account-b")
		groupOld, groupNew := "moving-group-a", "moving-group-b"
		for range iterations {
			if err := res.renameAccountResolverEntry(accountOld, domain.Account{
				Code: accountNew, EngineAccountID: 8,
			}); err != nil {
				errCh <- err
				return
			}
			accountOld, accountNew = accountNew, accountOld
			if err := res.renameGroupResolverEntry(groupOld, domain.AccountGroup{
				Code: groupNew, EngineGroupID: 10,
			}); err != nil {
				errCh <- err
				return
			}
			groupOld, groupNew = groupNew, groupOld
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestApplyCurrenciesCallsAccountAndGroupTiers(t *testing.T) {
	t.Parallel()
	groupID, err := param.NewAccountGroupIDFromUint32(7)
	if err != nil {
		t.Fatalf("group id: %v", err)
	}
	snap := fwengine.Snapshot{
		Assets: testAssets(),
		Accounts: []domain.Account{
			{
				Code:            "acc-1",
				EngineAccountID: 11,
				Currency:        "GBP",
			},
			{
				Code:            "acc-2",
				EngineAccountID: 12,
			},
		},
		Groups: []domain.AccountGroup{
			{Code: "", Currency: "USD"},
			{Code: "desk-a", EngineGroupID: 7, Currency: "EUR"},
			{Code: "desk-b", EngineGroupID: 8},
		},
	}
	res, err := newIDResolver(snap.Accounts, snap.Groups, snap.Assets)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	handle := &fakeCurrencyAccounts{}
	if err := applyCurrencies(handle, snap.Accounts, snap.Groups, res); err != nil {
		t.Fatalf("applyCurrencies: %v", err)
	}
	accountID := param.NewAccountIDFromUint64(11)
	wantGroups := []currencyCall{
		{id: param.DefaultAccountGroup.String(), currency: "2"},
		{id: groupID.String(), currency: "5"},
	}
	if !slices.Equal(handle.groupCalls, wantGroups) {
		t.Fatalf("group currency calls = %+v, want %+v", handle.groupCalls, wantGroups)
	}
	wantAccounts := []currencyCall{{id: accountID.String(), currency: "7"}}
	if !slices.Equal(handle.accountCalls, wantAccounts) {
		t.Fatalf(
			"account currency calls = %+v, want %+v",
			handle.accountCalls,
			wantAccounts,
		)
	}
}

type currencyCall struct {
	id       string
	currency string
}

type fakeCurrencyAccounts struct {
	groupCalls   []currencyCall
	accountCalls []currencyCall
}

func (h *fakeCurrencyAccounts) SetGroupCurrency(
	id param.AccountGroupID,
	currency param.Asset,
) error {
	h.groupCalls = append(h.groupCalls, currencyCall{
		id:       id.String(),
		currency: currency.String(),
	})
	return nil
}

func (h *fakeCurrencyAccounts) SetCurrency(
	id param.AccountID,
	currency param.Asset,
) error {
	h.accountCalls = append(h.accountCalls, currencyCall{
		id:       id.String(),
		currency: currency.String(),
	})
	return nil
}

// TestOpenPitEngineBuilder_SeedsFromSnapshot builds one engine from a seeded
// snapshot (a blocked account plus a rate-limit barrier) and retunes the
// rate-limit policy in place. It needs the native dylib at run time.
func TestOpenPitEngineBuilder_SeedsFromSnapshot(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets:     testAssets(),
		Accounts:   []domain.Account{blockedAccount("acc-1", "risk"), account("acc-2")},
		RateLimits: []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 100, time.Second)},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()
	ctx := context.Background()

	// Retune the rate-limit policy (broker barrier kept) on the live handle: the
	// axes are replaced wholesale, no rebuild.
	newLimits := fwengine.LimitSet{RateLimits: []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 5, time.Second)}}
	if _, err := eng.ConfigurePolicy(ctx, domain.PolicyRateLimit, newLimits); err != nil {
		t.Fatalf("ConfigurePolicy retune: %v", err)
	}

	// Both accounts are in the snapshot, so the resolver covers them by their
	// stored engine ids - no string hashing.
	if err := unblockAccountOnLane(ctx, eng, "acc-1"); err != nil {
		t.Fatalf("UnblockAccount: %v", err)
	}
	if err := blockAccountOnLane(ctx, eng, "acc-2", "manual"); err != nil {
		t.Fatalf("BlockAccount: %v", err)
	}

	// An account the snapshot does not cover has no stored engine id, so the
	// resolver rejects it as invalid rather than hashing its code.
	if err := blockAccountOnLane(ctx, eng, "ghost", "manual"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("BlockAccount(unknown) = %v, want ErrInvalid", err)
	}
}

// TestConfigurePolicy_RateLimitRetuneUnchangedKeys checks the rate-limit retune
// path: same barrier-key set succeeds on the live handle.
func TestConfigurePolicy_RateLimitRetuneUnchangedKeys(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets: testAssets(),
		RateLimits: []domain.LimitRate{
			rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
			rateLimit(domain.ScopeAsset, "", "USD", 50, time.Second),
		},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()

	same := fwengine.LimitSet{RateLimits: []domain.LimitRate{
		rateLimit(domain.ScopeBroker, "", "", 7, time.Second),
		rateLimit(domain.ScopeAsset, "", "USD", 3, time.Second),
	}}
	if _, err := eng.ConfigurePolicy(context.Background(),
		domain.PolicyRateLimit, same); err != nil {
		t.Fatalf("ConfigurePolicy retune: %v", err)
	}
}

// TestConfigurePolicy_OrderSizeReplacesAxes checks the order-size full-axes
// replace path applies on the live handle.
func TestConfigurePolicy_OrderSizeReplacesAxes(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets:          testAssets(),
		OrderSizeLimits: []domain.LimitOrderSize{orderSize(domain.ScopeBroker, "", "", "10", "")},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()

	replaced := fwengine.LimitSet{OrderSizeLimits: []domain.LimitOrderSize{
		orderSize(domain.ScopeBroker, "", "", "20", ""),
		orderSize(domain.ScopeUnderlyingAsset, "", "USD", "5", ""),
	}}
	if _, err := eng.ConfigurePolicy(context.Background(),
		domain.PolicyOrderSizeLimit, replaced); err != nil {
		t.Fatalf("ConfigurePolicy replace: %v", err)
	}
}

// TestConfigurePolicy_OrderSizeDropsBrokerOnline checks the explicit optional
// update clears a broker axis while an asset axis keeps the policy non-empty.
func TestConfigurePolicy_OrderSizeDropsBrokerOnline(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets: testAssets(),
		OrderSizeLimits: []domain.LimitOrderSize{
			orderSize(domain.ScopeBroker, "", "", "10", ""),
			orderSize(domain.ScopeUnderlyingAsset, "", "USD", "5", ""),
		},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()
	adapter := eng.(*openPitEngine)
	asyncBefore := adapter.async
	sinkBefore := adapter.MarketDataSink()

	dropped := fwengine.LimitSet{OrderSizeLimits: []domain.LimitOrderSize{orderSize(
		domain.ScopeUnderlyingAsset, "", "USD", "5", "",
	)}}
	if _, err := eng.ConfigurePolicy(
		context.Background(), domain.PolicyOrderSizeLimit, dropped,
	); err != nil {
		t.Fatalf("ConfigurePolicy drop order-size broker: %v", err)
	}
	if adapter.async != asyncBefore || adapter.MarketDataSink() != sinkBefore {
		t.Fatal("dropping order-size broker replaced engine or market-data sink")
	}
}

// TestConfigurePolicy_OrderSizeNoBrokerReplace checks an order-size set built
// without a broker barrier can be replaced wholesale.
func TestConfigurePolicy_OrderSizeNoBrokerReplace(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets:   testAssets(),
		Accounts: []domain.Account{account("acc-1")},
		OrderSizeLimits: []domain.LimitOrderSize{orderSize(
			domain.ScopeUnderlyingAsset, "", "USD", "5", "",
		)},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()

	replaced := fwengine.LimitSet{OrderSizeLimits: []domain.LimitOrderSize{
		orderSize(domain.ScopeUnderlyingAsset, "", "USD", "7", ""),
		orderSize(domain.ScopeAccountUnderlyingAsset, "acc-1", "USD", "3", ""),
	}}
	if _, err := eng.ConfigurePolicy(context.Background(),
		domain.PolicyOrderSizeLimit, replaced); err != nil {
		t.Fatalf("ConfigurePolicy replace without broker: %v", err)
	}
}

// TestConfigurePolicy_UnregisteredPolicyStub checks a policy absent from the
// cold snapshot still needs a rebuild because the SDK rejects empty policies.
func TestConfigurePolicy_UnregisteredPolicyStub(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets: testAssets(),
		RateLimits: []domain.LimitRate{
			rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
		},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()

	add := fwengine.LimitSet{OrderSizeLimits: []domain.LimitOrderSize{
		orderSize(domain.ScopeBroker, "", "", "10", ""),
	}}
	_, err = eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit, add)
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented for unregistered policy, got %v", err)
	}
}

// TestConfigurePolicy_RemoveLastBarrierStub checks an empty barrier set returns
// ErrNotImplemented before the SDK rejects the resulting empty policy.
func TestConfigurePolicy_RemoveLastBarrierStub(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets: testAssets(),
		RateLimits: []domain.LimitRate{
			rateLimit(domain.ScopeAsset, "", "USD", 100, time.Second),
		},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()

	_, err = eng.ConfigurePolicy(context.Background(), domain.PolicyRateLimit, fwengine.LimitSet{})
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented for empty settings, got %v", err)
	}
}

// TestConfigurePolicy_RateLimitAddRemoveBarrier checks the rate-limit axes are
// replaced wholesale on the live handle.
func TestConfigurePolicy_RateLimitAddRemoveBarrier(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets:     testAssets(),
		RateLimits: []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 100, time.Second)},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()
	ctx := context.Background()

	added := fwengine.LimitSet{RateLimits: []domain.LimitRate{
		rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
		rateLimit(domain.ScopeAsset, "", "USD", 50, time.Second),
	}}
	if _, err := eng.ConfigurePolicy(ctx, domain.PolicyRateLimit, added); err != nil {
		t.Fatalf("ConfigurePolicy add: %v", err)
	}

	removed := fwengine.LimitSet{RateLimits: []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 100, time.Second)}}
	if _, err := eng.ConfigurePolicy(ctx, domain.PolicyRateLimit, removed); err != nil {
		t.Fatalf("ConfigurePolicy remove: %v", err)
	}
}

// TestConfigurePolicy_RateLimitDropsBrokerOnline checks the explicit optional
// update clears a broker axis while an asset axis keeps the policy non-empty.
func TestConfigurePolicy_RateLimitDropsBrokerOnline(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets: testAssets(),
		RateLimits: []domain.LimitRate{
			rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
			rateLimit(domain.ScopeAsset, "", "USD", 50, time.Second),
		},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()
	adapter := eng.(*openPitEngine)
	asyncBefore := adapter.async
	sinkBefore := adapter.MarketDataSink()

	dropped := fwengine.LimitSet{RateLimits: []domain.LimitRate{rateLimit(domain.ScopeAsset, "", "USD", 50, time.Second)}}
	if _, err := eng.ConfigurePolicy(
		context.Background(), domain.PolicyRateLimit, dropped,
	); err != nil {
		t.Fatalf("ConfigurePolicy drop rate-limit broker: %v", err)
	}
	if adapter.async != asyncBefore || adapter.MarketDataSink() != sinkBefore {
		t.Fatal("dropping rate-limit broker replaced engine or market-data sink")
	}
}

func TestPolicyConfigurationBlocksFromCarriesAccountAndFallbackPolicy(t *testing.T) {
	t.Parallel()
	block := reject.NewAccountBlock(
		reject.CodePnlKillSwitchTriggered,
		"",
		"P&L bound breached",
		"lower=-100",
	)
	got := policyConfigurationBlocksFrom(
		[]reject.AccountBlock{block},
		"acc-1",
		domain.PolicySpotFundsPnlBoundsKillSwitch,
	)
	if len(got) != 1 {
		t.Fatalf("blocks = %+v, want one", got)
	}
	if got[0].Account != "acc-1" ||
		got[0].Policy != domain.PolicySpotFundsPnlBoundsKillSwitch ||
		got[0].Code != rejectCodeName(block.Code) ||
		got[0].Reason != block.Reason ||
		got[0].Details != block.Details {
		t.Fatalf("mapped block = %+v, want account and binding block fields", got[0])
	}
}

func TestPolicyConfigurationBlockOutcomesFromResolvesAccount(t *testing.T) {
	t.Parallel()
	resolver, err := newIDResolver([]domain.Account{account("acc-1")}, nil, nil)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	accountID, err := resolver.account("acc-1")
	if err != nil {
		t.Fatalf("resolver account: %v", err)
	}
	block := reject.NewAccountBlock(
		reject.CodePnlKillSwitchTriggered,
		"",
		"P&L bound breached",
		"lower=-100",
	)
	got, err := policyConfigurationBlockOutcomesFrom(
		[]configure.AccountBlockOutcome{{AccountID: accountID, Block: block}},
		resolver,
		domain.PolicySpotFundsPnlBoundsKillSwitch,
	)
	if err != nil {
		t.Fatalf("map configuration outcomes: %v", err)
	}
	if len(got) != 1 || got[0].Account != "acc-1" ||
		got[0].Policy != domain.PolicySpotFundsPnlBoundsKillSwitch {
		t.Fatalf("mapped blocks = %+v, want account acc-1 with fallback policy", got)
	}
}

func TestPolicyConfigurationBlockOutcomesFromReportsUnknownAccountAsInternal(
	t *testing.T,
) {
	t.Parallel()
	resolver, err := newIDResolver([]domain.Account{account("acc-1")}, nil, nil)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	block := reject.NewAccountBlock(
		reject.CodePnlKillSwitchTriggered,
		"spot_funds",
		"P&L bound breached",
		"lower=-100",
	)
	_, err = policyConfigurationBlockOutcomesFrom(
		[]configure.AccountBlockOutcome{{
			AccountID: param.NewAccountIDFromUint64(999),
			Block:     block,
		}},
		resolver,
		domain.PolicySpotFundsPnlBoundsKillSwitch,
	)
	if err == nil {
		t.Fatal("map unknown account outcome succeeded, want internal error")
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("map unknown account outcome = %v, want internal error", err)
	}
}

// TestSpotFundsPnlBoundsAxes_UnsupportedScope checks a scope the SpotFunds P&L
// bounds axes cannot express is rejected rather than silently dropped.
func TestSpotFundsPnlBoundsAxes_UnsupportedScope(t *testing.T) {
	t.Parallel()
	_, _, _, err := spotFundsPnlBoundsAxes(
		[]domain.LimitSpotFundsPnlBounds{
			{
				Scope:      domain.ScopeAsset,
				Currency:   "USD",
				LowerBound: "-100",
			},
		},
		testResolver(),
	)
	if err == nil {
		t.Fatal("want error for unsupported scope, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported scope") {
		t.Fatalf("error = %v, want unsupported scope", err)
	}
}

// fundedBalance seeds an account with absolute holdings on one asset so a spot
// limit order can reserve against it.
func fundedBalance(acct, asset, available string) domain.Balance {
	return domain.Balance{
		Account:   domain.AccountID(acct),
		Asset:     asset,
		Available: available,
	}
}

// checkProbe builds a buy/sell limit OrderProbe for the dry-run tests.
func checkProbe(acct string, side domain.OrderSide, qty, price string) domain.OrderProbe {
	return domain.OrderProbe{
		Account:     domain.AccountID(acct),
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        side,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: qty,
		Price:       price,
	}
}

func blockAccountOnLane(
	ctx context.Context, eng fwengine.Engine, account domain.AccountID, reason string,
) error {
	accountID, err := eng.AccountID(account)
	if err != nil {
		return err
	}
	type state struct{}
	builder := asyncengine.Chain(
		accountID, func(context.Context) (*state, error) { return &state{}, nil },
	)
	builder.BlockAccount(asyncengine.BlockHooks[*state]{
		Reason: func(context.Context, *state) (string, error) {
			return reason, nil
		},
		OnBlocked: func(context.Context, *state) error { return nil },
	})
	_, err = builder.Run(ctx, eng.AsyncEngine()).Await(context.Background())
	return err
}

func unblockAccountOnLane(ctx context.Context, eng fwengine.Engine, account domain.AccountID) error {
	accountID, err := eng.AccountID(account)
	if err != nil {
		return err
	}
	type state struct{}
	builder := asyncengine.Chain(
		accountID, func(context.Context) (*state, error) { return &state{}, nil },
	)
	builder.UnblockAccount(asyncengine.UnblockHooks[*state]{
		OnUnblocked: func(context.Context, *state) error { return nil },
	})
	_, err = builder.Run(ctx, eng.AsyncEngine()).Await(context.Background())
	return err
}

// TestEngine_CheckOrderPassCapturesLock runs a non-mutating dry-run for a funded
// limit order and checks it passes, capturing the would-be reservation lock
// prices the same way SubmitOrder does.
func TestEngine_CheckOrderPassCapturesLock(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets:   testAssets(),
		Accounts: []domain.Account{account("acc-1")},
		Balances: []domain.Balance{
			fundedBalance("acc-1", "USD", "1000000"),
			fundedBalance("acc-1", "AAPL", "1000000"),
		},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()

	out, err := materializeCheckedOrder(eng, checkProbe("acc-1", domain.OrderSideBuy, "1", "100"))
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if !out.Passed {
		t.Fatalf("want pass, got rejects=%+v block=%+v", out.Rejects, out.WouldBlock)
	}
	if len(out.Rejects) != 0 || out.WouldBlock != nil {
		t.Fatalf("pass must carry no rejects/block: %+v %+v", out.Rejects, out.WouldBlock)
	}
}

func TestEngine_CheckOrderUnknownAssetIsInvalid(t *testing.T) {
	t.Parallel()
	eng, err := newTestOpenPitEngineBuildFunc(t)(fwengine.Snapshot{
		Accounts: []domain.Account{account("acc-1")},
		Assets:   []domain.Asset{testAsset("AAPL"), testAsset("USD")},
	})
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()

	probe := checkProbe("acc-1", domain.OrderSideBuy, "1", "100")
	probe.BaseAsset = "unknown"
	if _, err := materializeCheckedOrder(eng, probe); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CheckOrder unknown asset = %v, want ErrInvalid", err)
	}
}

func TestEngine_MarketOrderRejectsStaleSourceQuoteAtBoundary(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets:   testAssets(),
		Accounts: []domain.Account{account("acc-1")},
		Balances: []domain.Balance{
			fundedBalance("acc-1", "USD", "1000000"),
			fundedBalance("acc-1", "AAPL", "1000000"),
		},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()
	adapter := eng.(*openPitEngine)
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	adapter.sink.now = func() time.Time { return now }

	if err := adapter.sink.Push(marketdata.QuoteUpdate{
		AsOf: now.Add(-MarketDataFreshnessTTL),
		Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "100",
	}); err != nil {
		t.Fatalf("Push stale boundary quote: %v", err)
	}
	marketOrder := domain.Order{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
		AmountValue: "1",
	}
	stale, err := materializeOrderResult(eng, marketOrder)
	if err != nil {
		t.Fatalf("SubmitOrder with stale quote: %v", err)
	}
	if stale.Accepted || !hasRejectCode(stale.Rejects, "mark_price_unavailable") {
		t.Fatalf("stale market order = %+v, want mark_price_unavailable reject", stale)
	}

	if err := adapter.sink.Push(marketdata.QuoteUpdate{
		AsOf: now.Add(-MarketDataFreshnessTTL + time.Second),
		Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "100",
	}); err != nil {
		t.Fatalf("Push aged fresh quote: %v", err)
	}
	fresh, err := materializeOrderResult(eng, marketOrder)
	if err != nil {
		t.Fatalf("SubmitOrder with fresh quote: %v", err)
	}
	if !fresh.Accepted {
		t.Fatalf("aged fresh market order rejected: %+v", fresh.Rejects)
	}
}

func TestEngine_CheckOrderMultiplePricesUsesDryRunIdentifier(t *testing.T) {
	price, err := param.NewPriceFromString("100")
	if err != nil {
		t.Fatalf("lock spy price: %v", err)
	}
	e := newLockSpyTestEngine(t, &executionLockSpy{price: price})

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	out, err := materializeCheckedOrder(
		e,
		checkProbe(testAccount, domain.OrderSideBuy, "1", "100"),
	)
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if !out.Passed || out.WouldLockPrice != "100" {
		t.Fatalf("CheckOrder result = %+v, want passing lock price 100", out)
	}
	logLine := logs.String()
	if !strings.Contains(logLine, "order=dry-run:"+testAccount) {
		t.Fatalf("warning = %q, want dry-run account identifier", logLine)
	}
	if strings.Contains(logLine, "<unassigned>") {
		t.Fatalf("warning = %q, must not invent an unassigned order", logLine)
	}
}

// TestEngine_CheckOrderRejectStructured runs a dry-run for an unfunded buy and
// checks it rejects with a structured reject (insufficient funds), not an error.
func TestEngine_CheckOrderRejectStructured(t *testing.T) {
	t.Parallel()
	eng, err := newTestOpenPitEngineBuildFunc(t)(fwengine.Snapshot{
		Assets:   testAssets(),
		Accounts: []domain.Account{account("acc-1")},
	})
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()

	out, err := materializeCheckedOrder(eng, checkProbe("acc-1", domain.OrderSideBuy, "1", "100"))
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if out.Passed {
		t.Fatalf("want reject for unfunded account")
	}
	if len(out.Rejects) == 0 {
		t.Fatalf("reject must carry structured rejects")
	}
	if out.Rejects[0].Code == "" {
		t.Fatalf("reject code must be a stable string: %+v", out.Rejects[0])
	}
}

// TestEngine_CheckOrderStandingBlockIsRejectOnly runs a dry-run for an account
// the engine has kill-switched. The engine latches nothing new, so the report
// carries no would-be block: the standing block reaches the caller as the
// account-scope reject the engine emitted, and WouldBlock stays nil.
func TestEngine_CheckOrderStandingBlockIsRejectOnly(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets:   testAssets(),
		Accounts: []domain.Account{blockedAccount("acc-1", "risk")},
		Balances: []domain.Balance{
			fundedBalance("acc-1", "USD", "1000000"),
			fundedBalance("acc-1", "AAPL", "1000000"),
		},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()

	out, err := materializeCheckedOrder(eng, checkProbe("acc-1", domain.OrderSideBuy, "1", "100"))
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if out.Passed {
		t.Fatalf("want reject for blocked account")
	}
	if len(out.Rejects) != 1 {
		t.Fatalf("rejects = %+v, want the standing block as one reject", out.Rejects)
	}
	if out.Rejects[0].Scope != "account" || out.Rejects[0].Reason != "risk" {
		t.Fatalf("reject = %+v, want the account-scope standing block", out.Rejects[0])
	}
	if out.WouldBlock != nil {
		t.Fatalf("no block was latched, want nil would-block, got %+v", out.WouldBlock)
	}
}

// TestOpenPitEngineBuilder_RestoresTypedAccountBlockCause is the central
// restart assertion: applyBlocks must reinstall the persisted SDK cause rather
// than degrade it to an Engine/account_blocked reason-only block.
func TestOpenPitEngineBuilder_RestoresTypedAccountBlockCause(t *testing.T) {
	t.Parallel()
	blocked := blockedAccount("acc-1", "lower bound breached: pnl -501 is below -500")
	blocked.BlockPolicy = "SpotFundsPolicy"
	blocked.BlockCode = domain.RejectCodePnlKillSwitchTriggered
	blocked.BlockDetails = "account pnl -501 is below lower bound -500"
	snap := fwengine.Snapshot{
		Assets:   testAssets(),
		Accounts: []domain.Account{blocked},
		Balances: []domain.Balance{
			fundedBalance("acc-1", "USD", "1000000"),
			fundedBalance("acc-1", "AAPL", "1000000"),
		},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{{
			Scope:      domain.ScopeAccount,
			Account:    "acc-1",
			Currency:   "USD",
			LowerBound: "1",
		}},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()

	out, err := materializeCheckedOrder(
		eng, checkProbe("acc-1", domain.OrderSideBuy, "1", "100"),
	)
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if out.Passed || len(out.Rejects) != 1 {
		t.Fatalf("checked order = %+v, want one standing typed reject", out)
	}
	got := out.Rejects[0]
	if got.Policy != blocked.BlockPolicy || got.Code != blocked.BlockCode ||
		got.Reason != blocked.BlockReason || got.Details != blocked.BlockDetails {
		t.Fatalf("restored reject = %+v, want byte-identical cause from %+v", got, blocked)
	}
}

func TestOpenPitEngineBuilder_RejectsUnknownPersistedAccountBlockCode(t *testing.T) {
	t.Parallel()
	blocked := blockedAccount("acc-1", "risk")
	blocked.BlockPolicy = "SpotFundsPolicy"
	blocked.BlockCode = "future_unknown_code"
	_, err := newTestOpenPitEngineBuildFunc(t)(fwengine.Snapshot{
		Assets:   testAssets(),
		Accounts: []domain.Account{blocked},
	})
	if err == nil || !strings.Contains(err.Error(), `account "acc-1"`) ||
		!strings.Contains(err.Error(), `code "future_unknown_code"`) {
		t.Fatalf("NewOpenPitEngineBuildFunc() = %v, want loud account/code error", err)
	}
}

// TestEngine_CheckOrderKeepsShortRejectText proves a short engine reason is
// transcribed instead of being classified as garbage: the operator sees the
// engine's own wording.
func TestEngine_CheckOrderKeepsShortRejectText(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets:   testAssets(),
		Accounts: []domain.Account{blockedAccount("acc-1", "-5%")},
		Balances: []domain.Balance{
			fundedBalance("acc-1", "USD", "1000000"),
			fundedBalance("acc-1", "AAPL", "1000000"),
		},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()

	out, err := materializeCheckedOrder(eng, checkProbe("acc-1", domain.OrderSideBuy, "1", "100"))
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if out.Passed {
		t.Fatalf("want reject for blocked account")
	}
	if len(out.Rejects) != 1 {
		t.Fatalf("rejects len = %d, want 1", len(out.Rejects))
	}
	if out.Rejects[0].Reason != "-5%" {
		t.Fatalf("reject reason = %q, want the engine text -5%%", out.Rejects[0].Reason)
	}
}

// TestAccountBlockFrom_OnlyTranscribesLatchedBlock pins the dry-run block
// mapper: a latched engine block is transcribed and stamped with the probe
// account, and no block at all maps to nil rather than to a manufactured one.
func TestAccountBlockFrom_OnlyTranscribesLatchedBlock(t *testing.T) {
	t.Parallel()
	block := reject.NewAccountBlock(
		reject.CodePnlKillSwitchTriggered,
		"spot_funds",
		"P&L bound breached",
		"lower=-100",
	)
	got := accountBlockFrom(&block, "acc-1")
	if got == nil {
		t.Fatal("latched engine block must be transcribed")
	}
	if got.Account != "acc-1" ||
		got.Policy != block.Policy ||
		got.Code != rejectCodeName(block.Code) ||
		got.Reason != block.Reason ||
		got.Details != block.Details {
		t.Fatalf("mapped block = %+v, want the engine block stamped with the account", got)
	}
	if got := accountBlockFrom(nil, "acc-1"); got != nil {
		t.Fatalf("no latched block must map to nil, got %+v", got)
	}
}

// TestEngine_CheckOrderIsNonMutating runs against the REAL native engine. A
// rate_limit broker barrier of max_orders=1 governs a funded account. Repeated
// CheckOrder dry-runs must consume none of the budget: afterwards the first real
// SubmitOrder still passes, and only the second SubmitOrder is throttled -
// proving the checks reserved nothing and left engine state unchanged.
func TestEngine_CheckOrderIsNonMutating(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets:     testAssets(),
		Accounts:   []domain.Account{account("acc-1")},
		RateLimits: []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 1, time.Minute)},
		Balances: []domain.Balance{
			fundedBalance("acc-1", "USD", "1000000"),
			fundedBalance("acc-1", "AAPL", "1000000"),
		},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()

	for i := 0; i < 5; i++ {
		out, err := materializeCheckedOrder(eng, checkProbe("acc-1", domain.OrderSideBuy, "1", "100"))
		if err != nil {
			t.Fatalf("CheckOrder #%d: %v", i, err)
		}
		if !out.Passed {
			t.Fatalf("dry-run #%d must pass, got rejects=%+v", i, out.Rejects)
		}
	}

	order := domain.Order{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
		AmountValue: "1", Price: "100",
	}

	first, err := materializeOrderResult(eng, order)
	if err != nil {
		t.Fatalf("first SubmitOrder: %v", err)
	}
	if !first.Accepted {
		t.Fatalf("first submit must pass after dry-runs (budget intact), got %+v", first.Rejects)
	}

	second, err := materializeOrderResult(eng, order)
	if err != nil {
		t.Fatalf("second SubmitOrder: %v", err)
	}
	if second.Accepted {
		t.Fatalf("second submit must be throttled by max_orders=1")
	}
	if !hasRejectCode(second.Rejects, "rate_limit_exceeded") {
		t.Fatalf("want rate_limit_exceeded, got %+v", second.Rejects)
	}
}

func TestEngine_CheckOrderMatchesSubmitOrderSizeVerdict(t *testing.T) {
	t.Parallel()
	snap := fwengine.Snapshot{
		Assets:   testAssets(),
		Accounts: []domain.Account{account("acc-1")},
		Balances: []domain.Balance{
			fundedBalance("acc-1", "USD", "1000000"),
			fundedBalance("acc-1", "AAPL", "1000000"),
		},
		OrderSizeLimits: []domain.LimitOrderSize{{
			Scope:       domain.ScopeAccountUnderlyingAsset,
			Account:     "acc-1",
			Asset:       "AAPL",
			MaxQuantity: "1",
		}},
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	defer eng.Stop()

	for _, tc := range []struct {
		quantity string
		accepted bool
	}{{quantity: "1", accepted: true}, {quantity: "2", accepted: false}} {
		quantity := tc.quantity
		probe := checkProbe("acc-1", domain.OrderSideBuy, quantity, "10")
		dryRun, err := materializeCheckedOrder(eng, probe)
		if err != nil {
			t.Fatalf("CheckOrder quantity %s: %v", quantity, err)
		}
		real, err := materializeOrderResult(eng, domain.Order{
			Account:     probe.Account,
			BaseAsset:   probe.BaseAsset,
			QuoteAsset:  probe.QuoteAsset,
			Side:        probe.Side,
			AmountKind:  probe.AmountKind,
			AmountValue: probe.AmountValue,
			Price:       probe.Price,
		})
		if err != nil {
			t.Fatalf("SubmitOrder quantity %s: %v", quantity, err)
		}
		if dryRun.Passed != real.Accepted {
			t.Fatalf(
				"quantity %s verdict mismatch: dry-run=%t real=%t dry rejects=%+v real rejects=%+v",
				quantity, dryRun.Passed, real.Accepted, dryRun.Rejects, real.Rejects,
			)
		}
		if dryRun.Passed != tc.accepted {
			t.Fatalf(
				"quantity %s verdict = %t, want %t; dry rejects=%+v real rejects=%+v",
				quantity, dryRun.Passed, tc.accepted, dryRun.Rejects, real.Rejects,
			)
		}
		if len(dryRun.Rejects) != len(real.Rejects) {
			t.Fatalf(
				"quantity %s reject count mismatch: dry=%+v real=%+v",
				quantity, dryRun.Rejects, real.Rejects,
			)
		}
		for i := range dryRun.Rejects {
			if dryRun.Rejects[i] != real.Rejects[i] {
				t.Fatalf(
					"quantity %s reject %d mismatch: dry=%+v real=%+v",
					quantity, i, dryRun.Rejects[i], real.Rejects[i],
				)
			}
		}
	}
}

// hasRejectCode reports whether rejects carry a reject with the given code.
func hasRejectCode(rejects []domain.OrderReject, code string) bool {
	for _, r := range rejects {
		if r.Code == code {
			return true
		}
	}
	return false
}

// TestResolverUnknownAssetIsInvalid checks a caller asset missing from the
// live dictionary wraps domain.ErrInvalid instead of being passed to the engine.
func TestResolverUnknownAssetIsInvalid(t *testing.T) {
	t.Parallel()
	if _, err := testResolver().asset(" "); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for blank asset, got %v", err)
	}
}

// TestResolverUnknownAccountIsInvalid checks resolving a code the resolver does
// not cover wraps domain.ErrInvalid (so an order for an unknown account is a 400,
// not a 500, and is never silently hashed into an engine id).
func TestResolverUnknownAccountIsInvalid(t *testing.T) {
	t.Parallel()
	if _, err := testResolver().account(domain.AccountID("nope")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unknown account, got %v", err)
	}
}

// TestOrderSide_UnknownIsInvalid checks an unknown order side (caller input)
// wraps domain.ErrInvalid.
func TestOrderSide_UnknownIsInvalid(t *testing.T) {
	t.Parallel()
	if _, err := orderSide(domain.OrderSide("sideways")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unknown side, got %v", err)
	}
}

// TestTradeAmountFrom_InvalidInputs checks the order amount mapper wraps
// domain.ErrInvalid for an unknown amount kind and for a non-decimal
// quantity/volume value.
func TestTradeAmountFrom_InvalidInputs(t *testing.T) {
	t.Parallel()
	if _, err := tradeAmountFrom(domain.OrderAmountKind("base"), "1"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unknown amount kind, got %v", err)
	}
	if _, err := tradeAmountFrom(domain.OrderAmountKindQuantity, "not-a-number"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad quantity, got %v", err)
	}
	if _, err := tradeAmountFrom(domain.OrderAmountKindVolume, "not-a-number"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad volume, got %v", err)
	}
}

// TestOrderModelFrom_InvalidInputs checks the order mapper wraps
// domain.ErrInvalid for a bad asset, a bad amount, and a bad limit price.
func TestOrderModelFrom_InvalidInputs(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1")
	base := domain.Order{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
	}

	badAsset := base
	badAsset.BaseAsset = " "
	if _, err := orderModelFrom(badAsset, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for blank base asset, got %v", err)
	}

	badAmount := base
	badAmount.AmountValue = "not-a-number"
	if _, err := orderModelFrom(badAmount, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad amount, got %v", err)
	}

	badPrice := base
	badPrice.Price = "not-a-number"
	if _, err := orderModelFrom(badPrice, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad price, got %v", err)
	}

	badAccount := base
	badAccount.Account = "ghost"
	if _, err := orderModelFrom(badAccount, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unknown account, got %v", err)
	}
}

// TestExecutionReportFrom_MissingLockDoesNotRebuildFromLockPrice pins the hard
// refusal added when Officer stopped reconstructing engine-produced locks.
func TestExecutionReportFrom_MissingLockDoesNotRebuildFromLockPrice(t *testing.T) {
	t.Parallel()
	_, err := executionReportFrom(domain.ExecutionReportInput{
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Account:        "acc-1",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "100",
		LeavesQuantity: "0",
		LockPrice:      "100",
		OrderStatus:    domain.OrderStatusFilled,
	}, "0", testResolver("acc-1"))
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing stored lock error = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "carries no request or stored order lock") {
		t.Fatalf("missing stored lock error = %v, want explicit lock failure", err)
	}
}

// TestExecutionReportFromCancellationForwardsEngineLeaves pins that request
// leaves remains persistence-only. The mapper sends the separate value the node
// captured from the order before the cancellation.
func TestExecutionReportFromCancellationForwardsEngineLeaves(t *testing.T) {
	t.Parallel()
	report, err := executionReportFrom(domain.ExecutionReportInput{
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Account:        "acc-1",
		Side:           domain.OrderSideBuy,
		LeavesQuantity: "1.23000",
		Lock:           storedExecutionReportLock(t),
		OrderStatus:    domain.OrderStatusCancelled,
	}, "2", testResolver("acc-1"))
	if err != nil {
		t.Fatalf("executionReportFrom: %v", err)
	}
	fill, ok := report.Fill().Get()
	if !ok {
		t.Fatal("Fill unset")
	}
	if _, ok := fill.LastTrade().Get(); ok {
		t.Fatal("LastTrade set for a cancellation without a fill")
	}
	leaves, ok := fill.RemainingReservedQuantity().Get()
	if !ok {
		t.Fatal("Fill.LeavesQuantity unset")
	}
	want, err := param.NewQuantityFromString("2")
	if err != nil {
		t.Fatalf("build wanted quantity: %v", err)
	}
	if !leaves.Equal(want) {
		t.Fatalf("engine leaves = %q, want selected leaves 2", leaves.String())
	}
}

// TestExecutionReportFromFillUsesSelectedEngineLeaves pins that mapping reads
// only the explicit engine-leaves argument, never the persistence-only request
// field.
func TestExecutionReportFromFillUsesSelectedEngineLeaves(t *testing.T) {
	t.Parallel()
	report, err := executionReportFrom(domain.ExecutionReportInput{
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Account:        "acc-1",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "2",
		FillPrice:      "100",
		LeavesQuantity: "3",
		Lock:           storedExecutionReportLock(t),
		OrderStatus:    domain.OrderStatusFilled,
	}, "2", testResolver("acc-1"))
	if err != nil {
		t.Fatalf("executionReportFrom: %v", err)
	}
	fill, ok := report.Fill().Get()
	if !ok {
		t.Fatal("Fill unset")
	}
	leaves, ok := fill.RemainingReservedQuantity().Get()
	if !ok || leaves.String() != "2" {
		t.Fatalf("engine fill leaves = %v, %t, want selected leaves 2", leaves, ok)
	}
	if final, ok := fill.IsFinal().Get(); !ok || !final {
		t.Fatalf("engine fill IsFinal = %t, %t, want true", final, ok)
	}
}

func TestExecutionReportFromFillMissingEngineLeavesIsInternalError(t *testing.T) {
	t.Parallel()
	_, err := executionReportFrom(domain.ExecutionReportInput{
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Account:        "acc-1",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "2",
		FillPrice:      "100",
		LeavesQuantity: "3",
		Lock:           storedExecutionReportLock(t),
		OrderStatus:    domain.OrderStatusFilled,
	}, "", testResolver("acc-1"))
	if err == nil {
		t.Fatal("executionReportFrom accepted a fill without engine leaves")
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing engine leaves error = %v, want internal error", err)
	}
}

// TestExecutionReportFromMalformedEngineLeavesIsInternalError pins that malformed
// engine leaves remain an internal adapter error, not client input.
func TestExecutionReportFromMalformedEngineLeavesIsInternalError(t *testing.T) {
	t.Parallel()
	_, err := executionReportFrom(domain.ExecutionReportInput{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Account:     "acc-1",
		Side:        domain.OrderSideBuy,
		Lock:        storedExecutionReportLock(t),
		OrderStatus: domain.OrderStatusCancelled,
	}, "not-a-number", testResolver("acc-1"))
	if err == nil {
		t.Fatal("executionReportFrom accepted malformed engine leaves")
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("malformed engine leaves error = %v, want internal error", err)
	}
}

// TestExecutionReportFrom_InvalidInputs checks the execution-report mapper wraps
// domain.ErrInvalid for malformed fill, commission, and lock inputs.
func TestExecutionReportFrom_InvalidInputs(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1")
	base := domain.ExecutionReportInput{
		BaseAsset: "AAPL", QuoteAsset: "USD", Account: "acc-1", Side: domain.OrderSideBuy,
		FillQuantity: "1", FillPrice: "100", LeavesQuantity: "0",
		Lock:        storedExecutionReportLock(t),
		OrderStatus: domain.OrderStatusFilled,
	}

	badPrice := base
	badPrice.FillPrice = "not-a-number"
	if _, err := executionReportFrom(badPrice, base.LeavesQuantity, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad fill price, got %v", err)
	}

	badQty := base
	badQty.FillQuantity = "not-a-number"
	if _, err := executionReportFrom(badQty, base.LeavesQuantity, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad fill quantity, got %v", err)
	}

	badCommissionAmount := base
	badCommissionAmount.Commission = &domain.Commission{Amount: "not-a-number", Currency: "USD"}
	if _, err := executionReportFrom(badCommissionAmount, base.LeavesQuantity, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad commission amount, got %v", err)
	}

	badCommissionCurrency := base
	badCommissionCurrency.Commission = &domain.Commission{Amount: "-0.12", Currency: ""}
	if _, err := executionReportFrom(badCommissionCurrency, base.LeavesQuantity, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad commission currency, got %v", err)
	}

	badOpaqueLock := base
	badOpaqueLock.Lock = []byte{0x01, 0x02, 0x03}
	if _, err := executionReportFrom(badOpaqueLock, base.LeavesQuantity, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad opaque lock, got %v", err)
	}

	oneSidedFill := base
	oneSidedFill.FillPrice = ""
	if _, err := executionReportFrom(oneSidedFill, base.LeavesQuantity, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for one-sided fill, got %v", err)
	}

	noTradeCancel := base
	noTradeCancel.FillQuantity = ""
	noTradeCancel.FillPrice = ""
	noTradeCancel.LeavesQuantity = "1"
	noTradeCancel.OrderStatus = domain.OrderStatusCancelled
	if _, err := executionReportFrom(noTradeCancel, "1", res); err != nil {
		t.Fatalf("no-trade cancel must map: %v", err)
	}
}

func storedExecutionReportLock(t *testing.T) []byte {
	t.Helper()
	lock, err := marshalLock(lockFromPrices(t, "100"))
	if err != nil {
		t.Fatalf("marshal execution-report lock: %v", err)
	}
	return lock
}

func TestExecutionReportFrom_CommissionPreservesPositiveFeeSign(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1")
	report, err := executionReportFrom(domain.ExecutionReportInput{
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		FillQuantity:   "1",
		FillPrice:      "100",
		LeavesQuantity: "0",
		Lock:           storedExecutionReportLock(t),
		Commission: &domain.Commission{
			Amount:   "2",
			Currency: "GBP",
		},
		Account:     "acc-1",
		Side:        domain.OrderSideBuy,
		OrderStatus: domain.OrderStatusFilled,
	}, "0", res)
	if err != nil {
		t.Fatalf("executionReportFrom: %v", err)
	}
	if _, ok := report.FinancialImpact().Get(); ok {
		t.Fatal("FinancialImpact must remain unset")
	}
	fill, ok := report.Fill().Get()
	if !ok {
		t.Fatal("Fill unset")
	}
	commission, ok := fill.Fee().Get()
	if !ok {
		t.Fatal("Fill.Fee unset")
	}
	if commission.Amount.String() != "2" {
		t.Fatalf("commission amount = %q, want SDK fee 2", commission.Amount.String())
	}
	if commission.Currency.String() != "7" {
		t.Fatalf("commission currency = %q, want decimal engine id 7", commission.Currency.String())
	}
}

func TestExecutionReportFrom_NoTradeCommissionPreservesNegativeRebateSign(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1")
	report, err := executionReportFrom(domain.ExecutionReportInput{
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		LeavesQuantity: "1",
		Lock:           storedExecutionReportLock(t),
		Commission: &domain.Commission{
			Amount:   "-0.50",
			Currency: "USD",
		},
		Account:     "acc-1",
		Side:        domain.OrderSideBuy,
		OrderStatus: domain.OrderStatusCancelled,
	}, "1", res)
	if err != nil {
		t.Fatalf("executionReportFrom: %v", err)
	}
	fill, ok := report.Fill().Get()
	if !ok {
		t.Fatal("Fill unset")
	}
	if _, ok := fill.LastTrade().Get(); ok {
		t.Fatal("LastTrade set for a terminal report without a fill")
	}
	commission, ok := fill.Fee().Get()
	if !ok {
		t.Fatal("Fill.Fee unset")
	}
	if commission.Amount.String() != "-0.50" {
		t.Fatalf("commission amount = %q, want SDK fee -0.50", commission.Amount.String())
	}
	if commission.Currency.String() != "2" {
		t.Fatalf("commission currency = %q, want decimal engine id 2", commission.Currency.String())
	}
}

func TestExecutionReportFrom_WorkflowCommissionOmitsRequestLeaves(t *testing.T) {
	t.Parallel()
	report, err := executionReportFrom(domain.ExecutionReportInput{
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		LeavesQuantity: "1.23000",
		Lock:           storedExecutionReportLock(t),
		Commission: &domain.Commission{
			Amount:   "-0.50",
			Currency: "USD",
		},
		Account:     "acc-1",
		Side:        domain.OrderSideBuy,
		OrderStatus: domain.OrderStatusAccepted,
	}, "", testResolver("acc-1"))
	if err != nil {
		t.Fatalf("executionReportFrom: %v", err)
	}
	fill, ok := report.Fill().Get()
	if !ok {
		t.Fatal("Fill unset")
	}
	if _, ok := fill.RemainingReservedQuantity().Get(); ok {
		t.Fatal("LeavesQuantity set from workflow request leaves")
	}
}

func TestExecutionReportFrom_CommissionFill(t *testing.T) {
	t.Parallel()
	report, err := executionReportFrom(domain.ExecutionReportInput{
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		FillQuantity:   "1",
		FillPrice:      "100",
		LeavesQuantity: "0",
		Lock:           storedExecutionReportLock(t),
		Commission: &domain.Commission{
			Amount:   "1",
			Currency: "USD",
		},
		Account:     "acc-1",
		Side:        domain.OrderSideBuy,
		OrderStatus: domain.OrderStatusPartiallyFilled,
	}, "0", testResolver("acc-1"))
	if err != nil {
		t.Fatalf("executionReportFrom: %v", err)
	}
	fill, ok := report.Fill().Get()
	if !ok {
		t.Fatal("Fill unset")
	}
	if _, ok := fill.LastTrade().Get(); !ok {
		t.Fatal("LastTrade unset for a fill")
	}
	commission, ok := fill.Fee().Get()
	if !ok {
		t.Fatal("Fill.Fee unset")
	}
	if commission.Amount.String() != "1" || commission.Currency.String() != "2" {
		t.Fatalf("commission = %+v, want decimal engine id 2 and fee 1", commission)
	}
}

func TestExecutionReportFrom_FillRequiresSelectedLeaves(t *testing.T) {
	t.Parallel()
	_, err := executionReportFrom(domain.ExecutionReportInput{
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		FillQuantity:   "1",
		FillPrice:      "100",
		LeavesQuantity: "0",
		Lock:           storedExecutionReportLock(t),
		Commission: &domain.Commission{
			Amount:   "0.25",
			Currency: "USD",
		},
		Account:     "acc-1",
		Side:        domain.OrderSideBuy,
		OrderStatus: domain.OrderStatusFilled,
	}, "", testResolver("acc-1"))
	if err == nil {
		t.Fatal("executionReportFrom accepted a fill without selected leaves")
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing selected leaves error = %v, want internal error", err)
	}
}

// TestAdjustmentAmountFrom_InvalidInputs checks the adjustment amount mapper
// wraps domain.ErrInvalid for a non-decimal value and for an unknown mode.
func TestAdjustmentAmountFrom_InvalidInputs(t *testing.T) {
	t.Parallel()
	if _, err := adjustmentAmountFrom(
		domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "not-a-number"}, "balance",
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad adjustment value, got %v", err)
	}
	if _, err := adjustmentAmountFrom(
		domain.AdjustmentAmount{Mode: domain.AdjustmentAmountMode("sideways"), Value: "1"}, "balance",
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unknown adjustment mode, got %v", err)
	}
}

// TestAccountAdjustmentFromRequest_InvalidInputs checks the adjustment request
// mapper wraps domain.ErrInvalid for a bad asset, a bad average-entry-price, a
// bad realized PnL, and a bad bound (all caller input).
func TestAccountAdjustmentFromRequest_InvalidInputs(t *testing.T) {
	t.Parallel()
	delta := func(v string) *domain.AdjustmentAmount {
		return &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: v}
	}

	badAsset := domain.AdjustmentRequest{Asset: " ", Balance: delta("1")}
	if _, err := accountAdjustmentFromRequest(badAsset, testResolver()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for blank asset, got %v", err)
	}

	badPrice := domain.AdjustmentRequest{
		Asset: "USD", Balance: delta("1"), AverageEntryPrice: "not-a-number",
	}
	if _, err := accountAdjustmentFromRequest(badPrice, testResolver()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad average entry price, got %v", err)
	}

	badRealizedPnl := domain.AdjustmentRequest{
		Asset: "USD", Balance: delta("1"), RealizedPnl: "not-a-number",
	}
	if _, err := accountAdjustmentFromRequest(badRealizedPnl, testResolver()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad realized pnl, got %v", err)
	}

	badBound := domain.AdjustmentRequest{
		Asset: "USD", Balance: delta("1"),
		BalanceBounds: &domain.AdjustmentBounds{Lower: "not-a-number"},
	}
	if _, err := accountAdjustmentFromRequest(badBound, testResolver()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad bound, got %v", err)
	}
}

func TestRejectCodeName_ArithmeticOverflow(t *testing.T) {
	t.Parallel()
	if got := rejectCodeName(reject.CodeArithmeticOverflow); got != "arithmetic_overflow" {
		t.Fatalf("reject code name = %q, want arithmetic_overflow", got)
	}
}

func TestRejectCodeNamesAreKnownPersistedCodes(t *testing.T) {
	t.Parallel()
	for sdkCode, name := range rejectCodeNames {
		if !domain.KnownRejectCode(name) {
			t.Errorf("SDK reject code %v maps to unknown persisted name %q", sdkCode, name)
		}
	}
}

func TestExecutionBalanceSettlementsFrom_KeepsPositionPnl(t *testing.T) {
	t.Parallel()
	settlements := executionBalanceSettlementsFrom([]fwengine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceResult:     "100",
			RealizedPnlDelta:  "2",
			RealizedPnlResult: "7",
		},
	}})
	if len(settlements) != 1 {
		t.Fatalf("settlements = %+v, want one", settlements)
	}
	if settlements[0].Outcome.BalanceResult != "100" ||
		settlements[0].Outcome.RealizedPnlDelta != "2" ||
		settlements[0].Outcome.RealizedPnlResult != "7" {
		t.Fatalf("settlement outcome = %+v", settlements[0].Outcome)
	}
}

func TestOutcomeAcceptedFromEntry_PreservesComputedZeroAndAbsence(t *testing.T) {
	t.Parallel()
	res := testResolver()
	asset, err := res.asset("AAPL")
	if err != nil {
		t.Fatalf("NewAsset: %v", err)
	}
	zero, err := param.NewPnlFromString("0")
	if err != nil {
		t.Fatalf("NewPnlFromString: %v", err)
	}
	computed, err := outcomeAcceptedFromEntry(
		accountadjustment.AccountOutcomeEntry{
			Asset: asset,
			RealizedPnl: optional.Some(accountadjustment.NewPnlOutcome(
				accountadjustment.PnlOutcomeAmount{
					Delta:    zero,
					Absolute: zero,
				},
			)),
		},
		res,
	)
	if err != nil {
		t.Fatalf("outcomeAcceptedFromEntry(computed): %v", err)
	}
	if computed.RealizedPnlDelta != "0" || computed.RealizedPnlResult != "0" {
		t.Fatalf("computed realized PnL outcome = %+v, want zero", computed)
	}

	absent, err := outcomeAcceptedFromEntry(
		accountadjustment.AccountOutcomeEntry{Asset: asset},
		res,
	)
	if err != nil {
		t.Fatalf("outcomeAcceptedFromEntry(absent): %v", err)
	}
	if absent.RealizedPnlDelta != "" ||
		absent.RealizedPnlResult != "" ||
		absent.RealizedPnlHaltReason != "" {
		t.Fatalf("absent realized PnL outcome = %+v", absent)
	}
}

func TestAccountAdjustmentFromRequest_ForwardsRealizedPnl(t *testing.T) {
	t.Parallel()
	adjustment, err := accountAdjustmentFromRequest(domain.AdjustmentRequest{
		Asset: "BTC", RealizedPnl: "-12.50",
		Balance: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeAbsolute, Value: "1",
		},
	}, testResolver())
	if err != nil {
		t.Fatalf("accountAdjustmentFromRequest: %v", err)
	}
	operation, ok := adjustment.BalanceOperation().Get()
	if !ok {
		t.Fatal("balance operation is unset")
	}
	pnl, ok := operation.RealizedPnl().Get()
	value, authoritative := pnl.Value()
	if !ok || !authoritative || value.String() != "-12.50" {
		t.Fatalf("realized pnl = (%v, %v) set=%v, want authoritative -12.50", value, authoritative, ok)
	}
}

func TestBalanceSeedAdjustment_RestoresHaltedRealizedPnl(t *testing.T) {
	t.Parallel()
	adjustment, err := balanceSeedAdjustment(domain.Balance{
		Asset:                 "BTC",
		Available:             "1",
		RealizedPnl:           "not-a-number",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
	}, testResolver())
	if err != nil {
		t.Fatalf("balanceSeedAdjustment: %v", err)
	}
	operation, ok := adjustment.BalanceOperation().Get()
	if !ok {
		t.Fatal("balance operation is unset")
	}
	state, ok := operation.RealizedPnl().Get()
	if !ok {
		t.Fatal("realized pnl is unset, want restored halt")
	}
	reason, halted := state.HaltReason()
	if !halted || reason != model.PnlHaltReasonMissingFx {
		t.Fatalf("realized pnl state = (%v, %v), want missing-fx halt", reason, halted)
	}
}

func TestPnlHaltReasonFromSDKMapsEveryReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   model.PnlHaltReason
		want domain.PnlHaltReason
	}{
		{
			name: "missing fx",
			in:   model.PnlHaltReasonMissingFx,
			want: domain.PnlHaltReasonMissingFx,
		},
		{
			name: "missing account currency",
			in:   model.PnlHaltReasonMissingAccountCurrency,
			want: domain.PnlHaltReasonMissingAccountCurrency,
		},
		{
			name: "missing initial pnl",
			in:   model.PnlHaltReasonMissingInitialPnl,
			want: domain.PnlHaltReasonMissingInitialPnl,
		},
		{
			name: "missing cost basis",
			in:   model.PnlHaltReasonMissingCostBasis,
			want: domain.PnlHaltReasonMissingCostBasis,
		},
		{
			name: "arithmetic overflow",
			in:   model.PnlHaltReasonArithmeticOverflow,
			want: domain.PnlHaltReasonArithmeticOverflow,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := pnlHaltReasonFromSDK(test.in)
			if err != nil {
				t.Fatalf("pnlHaltReasonFromSDK(%v): %v", test.in, err)
			}
			if got != test.want {
				t.Fatalf("pnlHaltReasonFromSDK(%v) = %q, want %q", test.in, got, test.want)
			}
			if err := domain.ValidatePnlHaltReason(got); err != nil {
				t.Fatalf("ValidatePnlHaltReason(%q): %v", got, err)
			}
			// A mapped reason must round-trip: the engine rebuild at start
			// replays persisted halts, so a reason this seam accepts but
			// cannot return to the engine would block the next boot.
			if _, err := pnlHaltReasonToSDK(got); err != nil {
				t.Fatalf("pnlHaltReasonToSDK(%q): %v", got, err)
			}
		})
	}
}

// A halt reason the engine gains but this seam does not know must fail at the
// seam rather than reach storage as a value pnlHaltReasonToSDK would reject.
func TestPnlHaltReasonFromSDKRejectsUnrecognizedReason(t *testing.T) {
	t.Parallel()
	got, err := pnlHaltReasonFromSDK(model.PnlHaltReason(255))
	if err == nil {
		t.Fatalf("pnlHaltReasonFromSDK(255) = %q, want error", got)
	}
	if got != "" {
		t.Fatalf("pnlHaltReasonFromSDK(255) = %q, want empty reason", got)
	}
}

func TestPnlHaltReasonToSDKRejectsUnsupportedReason(t *testing.T) {
	t.Parallel()
	got, err := pnlHaltReasonToSDK(domain.PnlHaltReason("unsupported"))
	if err == nil {
		t.Fatalf("pnlHaltReasonToSDK(unsupported) = %v, want error", got)
	}
	if got != 0 {
		t.Fatalf("pnlHaltReasonToSDK(unsupported) = %v, want zero reason", got)
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("pnlHaltReasonToSDK(unsupported) = %v, want ErrInvalid", err)
	}
}

func TestSpotFundsAccountPnlFromListSelectsAuthoritativeOutcome(t *testing.T) {
	t.Parallel()
	accountID := param.NewAccountIDFromUint64(7)
	otherAccountID := param.NewAccountIDFromUint64(8)
	computed := func(
		id param.AccountID,
		deltaValue string,
		absolute string,
	) accountadjustment.AccountPnlOutcome {
		t.Helper()
		delta, err := param.NewPnlFromString(deltaValue)
		if err != nil {
			t.Fatalf("NewPnlFromString(delta): %v", err)
		}
		value, err := param.NewPnlFromString(absolute)
		if err != nil {
			t.Fatalf("NewPnlFromString(absolute): %v", err)
		}
		return accountadjustment.NewAccountPnlOutcome(
			1,
			id,
			accountadjustment.PnlOutcomeAmount{Delta: delta, Absolute: value},
		)
	}
	halted := func(
		id param.AccountID,
		reason model.PnlHaltReason,
	) accountadjustment.AccountPnlOutcome {
		t.Helper()
		outcome, err := accountadjustment.NewAccountPnlHaltedOutcome(
			1,
			id,
			reason,
		)
		if err != nil {
			t.Fatalf("NewAccountPnlHaltedOutcome: %v", err)
		}
		return outcome
	}

	tests := []struct {
		name       string
		outcomes   []accountadjustment.AccountPnlOutcome
		wantPnl    string
		wantReason domain.PnlHaltReason
	}{
		{
			name: "computed",
			outcomes: []accountadjustment.AccountPnlOutcome{
				computed(accountID, "1.250", "42.500"),
			},
			wantPnl: "42.500",
		},
		{
			name: "zero delta does not preempt later halt",
			outcomes: []accountadjustment.AccountPnlOutcome{
				computed(accountID, "0", "7.250"),
				halted(accountID, model.PnlHaltReasonMissingCostBasis),
			},
			wantReason: domain.PnlHaltReasonMissingCostBasis,
		},
		{
			// A halt already selected is the account's answer: a later outcome
			// reporting no change may not restate a number over it.
			name: "zero delta does not restate an earlier halt",
			outcomes: []accountadjustment.AccountPnlOutcome{
				halted(accountID, model.PnlHaltReasonMissingFx),
				computed(accountID, "0", "7.250"),
			},
			wantReason: domain.PnlHaltReasonMissingFx,
		},
		{
			// The first realization against a zero cost basis lands on zero: the
			// engine computed it, so it must reach storage as the authoritative
			// "0" rather than as the absence that leaves the account unset.
			name: "first realization at zero publishes the absolute",
			outcomes: []accountadjustment.AccountPnlOutcome{
				computed(accountID, "0", "0"),
			},
			wantPnl: "0",
		},
		{
			// A fill realizing as much as its commission costs nets to no change,
			// yet the absolute is still the engine's number and Officer may not
			// hold it yet.
			name: "cancelled fill and commission publish the absolute",
			outcomes: []accountadjustment.AccountPnlOutcome{
				computed(accountID, "0", "50.000"),
			},
			wantPnl: "50.000",
		},
		{
			// Repeating an event with no effect restates the same absolute, so
			// the stored value is written back as it stands, never dropped.
			name: "repeated no-effect event restates the same absolute",
			outcomes: []accountadjustment.AccountPnlOutcome{
				computed(accountID, "0", "50.000"),
				computed(accountID, "0", "50.000"),
			},
			wantPnl: "50.000",
		},
		{
			name: "zero delta for another account is not published",
			outcomes: []accountadjustment.AccountPnlOutcome{
				computed(otherAccountID, "0", "9"),
			},
		},
		{
			name: "halted",
			outcomes: []accountadjustment.AccountPnlOutcome{
				halted(accountID, model.PnlHaltReasonMissingCostBasis),
			},
			wantReason: domain.PnlHaltReasonMissingCostBasis,
		},
		{
			name: "mismatched account",
			outcomes: []accountadjustment.AccountPnlOutcome{
				computed(otherAccountID, "1.250", "9"),
			},
		},
		{
			name: "first duplicate wins",
			outcomes: []accountadjustment.AccountPnlOutcome{
				computed(accountID, "1.250", "17.250"),
				halted(accountID, model.PnlHaltReasonArithmeticOverflow),
			},
			wantPnl: "17.250",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			gotPnl, gotReason, err := spotFundsAccountPnlFromList(accountID, test.outcomes)
			if err != nil {
				t.Fatalf("spotFundsAccountPnlFromList: %v", err)
			}
			if gotPnl != test.wantPnl || gotReason != test.wantReason {
				t.Fatalf(
					"spotFundsAccountPnlFromList = (%q, %q), want (%q, %q)",
					gotPnl,
					gotReason,
					test.wantPnl,
					test.wantReason,
				)
			}
		})
	}
}

func TestExecutionReportPersistenceFromCarriesAccountPnlOutcome(t *testing.T) {
	t.Parallel()
	persistence := executionReportPersistenceFrom(
		domain.ExecutionReportInput{},
		nil,
		nil,
		"12.340",
		domain.PnlHaltReasonArithmeticOverflow,
	)
	if persistence.AccountPnl != "12.340" ||
		persistence.AccountPnlHaltReason != domain.PnlHaltReasonArithmeticOverflow {
		t.Fatalf("persistence = %+v, want exact account pnl outcome", persistence)
	}
}

// TestSanitizeText_CleansInvalidUTF8AndControls checks the boundary sanitizer
// drops invalid UTF-8 bytes and strips control characters while preserving
// normal printable text and spaces.
func TestSanitizeText_CleansInvalidUTF8AndControls(t *testing.T) {
	t.Parallel()

	garbage := "account_blocked Engine \x80\x16V \x07\xff\x78x"
	got := sanitizeText(garbage)
	if !utf8.ValidString(got) {
		t.Fatalf("sanitized text must be valid UTF-8, got %q", got)
	}
	for _, r := range got {
		if unicode.IsControl(r) {
			t.Fatalf("sanitized text must carry no control runes, got %q", got)
		}
	}
	if want := "account_blocked Engine V xx"; got != want {
		t.Fatalf("sanitized text = %q, want %q", got, want)
	}

	if got := sanitizeText("insufficient funds"); got != "insufficient funds" {
		t.Fatalf("clean text must pass through, got %q", got)
	}
	if got := sanitizeText("404"); got != "404" {
		t.Fatalf("clean numeric text must pass through, got %q", got)
	}
	// Short engine text is still the engine's answer: only encoding is judged
	// here, never plausibility.
	for _, input := range []string{"0.5", ">10", "<0", "-5%", "1/2", "0}", "}"} {
		if got := sanitizeText(input); got != input {
			t.Fatalf("engine text %q must survive, got %q", input, got)
		}
	}
	if got := sanitizeText("\x80\xff\x01"); got != "" {
		t.Fatalf("text that is entirely invalid must sanitize to empty, got %q", got)
	}
}

// TestExecutionReportPersistenceFrom_StatusOnlyNoAccountWrites proves the real mapper
// folds no account-side persistence into a status-only report: with empty engine
// outcomes and blocks, persistence.Balances is nil and persistence.Blocks is empty, so the
// node writes zero balance rows and zero account-block rows. The venue-owned
// bookkeeping (order status and its status-change event) is still carried.
func TestExecutionReportPersistenceFrom_StatusOnlyNoAccountWrites(t *testing.T) {
	t.Parallel()
	in := domain.ExecutionReportInput{
		Account:        domain.AccountID(testAccount),
		Order:          testOrderXID(0x11),
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		Side:           domain.OrderSideBuy,
		LeavesQuantity: "2",
		OrderStatus:    domain.OrderStatusCancelled,
	}
	persistence := executionReportPersistenceFrom(in, nil, nil, "", "")

	if persistence.Balances != nil {
		t.Fatalf("persistence.Balances = %+v, want nil for a status-only report", persistence.Balances)
	}
	if len(persistence.Blocks) != 0 {
		t.Fatalf("persistence.Blocks = %+v, want empty for a status-only report", persistence.Blocks)
	}
	if persistence.Trade != nil {
		t.Fatalf("persistence.Trade = %+v, want nil for a status-only report", persistence.Trade)
	}
	if persistence.OrderStatus != domain.OrderStatusCancelled {
		t.Fatalf("persistence.OrderStatus = %q, want cancelled", persistence.OrderStatus)
	}
	if persistence.Leaves != "2" {
		t.Fatalf("persistence.Leaves = %q, want caller leaves 2", persistence.Leaves)
	}
	if len(persistence.Events) == 0 {
		t.Fatal("persistence.Events is empty, want the status-change event")
	}
	if persistence.Events[0].Payload.LeavesQuantity != "2" {
		t.Fatalf(
			"event leaves = %q, want request value 2",
			persistence.Events[0].Payload.LeavesQuantity,
		)
	}
}

func TestExecutionReportPersistenceFrom_NoTradeCarriesCommission(t *testing.T) {
	t.Parallel()
	in := domain.ExecutionReportInput{
		Account:        domain.AccountID(testAccount),
		Order:          testOrderXID(0x13),
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		Side:           domain.OrderSideBuy,
		LeavesQuantity: "2",
		OrderStatus:    domain.OrderStatusCancelled,
		Commission:     &domain.Commission{Amount: "-0.30", Currency: "USDT"},
	}
	persistence := executionReportPersistenceFrom(in, nil, nil, "", "")

	if persistence.Trade != nil {
		t.Fatalf("persistence.Trade = %+v, want nil", persistence.Trade)
	}
	if persistence.Commission == nil ||
		persistence.Commission.Amount != "-0.30" ||
		persistence.Commission.Currency != "USDT" {
		t.Fatalf("report commission = %+v, want -0.30/USDT", persistence.Commission)
	}
	if len(persistence.Events) != 2 ||
		persistence.Events[0].Type != domain.OrderEventCommission ||
		persistence.Events[0].Payload.Commission == nil ||
		persistence.Events[1].Type != domain.OrderEventCancelled {
		t.Fatalf("events lost report commission: %+v", persistence.Events)
	}
}

func TestExecutionReportPersistenceFrom_FeeOnlyPartialHasEventWithoutTrade(t *testing.T) {
	t.Parallel()
	in := domain.ExecutionReportInput{
		Account:        domain.AccountID(testAccount),
		Order:          testOrderXID(0x14),
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		Side:           domain.OrderSideBuy,
		LeavesQuantity: "1",
		OrderStatus:    domain.OrderStatusPartiallyFilled,
		Commission:     &domain.Commission{Amount: "1", Currency: testQuote},
	}
	persistence := executionReportPersistenceFrom(in, nil, nil, "", "")

	if persistence.Trade != nil {
		t.Fatalf("persistence.Trade = %+v, want nil", persistence.Trade)
	}
	if len(persistence.Events) != 1 ||
		persistence.Events[0].Type != domain.OrderEventCommission {
		t.Fatalf("fee-only events = %+v, want one commission event", persistence.Events)
	}
	if persistence.Events[0].Payload.Commission != in.Commission {
		t.Fatalf("event commission = %+v, want %+v",
			persistence.Events[0].Payload.Commission, in.Commission)
	}
}

// TestExecutionReportPersistenceFrom_TerminalPreservesCallerLeaves proves that
// engine outcomes and blocks alter neither the caller leaves the order records
// nor the event payload. The stored leaves are selected before this mapper.
func TestExecutionReportPersistenceFrom_TerminalPreservesCallerLeaves(t *testing.T) {
	t.Parallel()
	in := domain.ExecutionReportInput{
		Account:        domain.AccountID(testAccount),
		Order:          testOrderXID(0x15),
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		Side:           domain.OrderSideBuy,
		LeavesQuantity: "2",
		OrderStatus:    domain.OrderStatusCancelled,
	}
	blocks := []domain.ExecutionAccountBlock{{
		Account: domain.AccountID(testAccount),
		Policy:  "spot_funds",
		Code:    "pnl_kill_switch_triggered",
		Reason:  "blocked before the release",
	}}

	blocked := executionReportPersistenceFrom(in, blocks, nil, "", "")
	if blocked.Leaves != "2" {
		t.Fatalf(
			"block-only leaves = %q, want caller leaves 2",
			blocked.Leaves,
		)
	}
	if len(blocked.Events) == 0 ||
		blocked.Events[0].Payload.LeavesQuantity != "2" {
		t.Fatalf("blocked events lost caller leaves: %+v", blocked.Events)
	}

	outcomes := []fwengine.BalanceOutcome{{
		Asset:   testQuote,
		Outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "100"},
	}}
	applied := executionReportPersistenceFrom(in, blocks, outcomes, "", "")
	if applied.Leaves != "2" {
		t.Fatalf("applied terminal leaves = %q, want caller leaves 2", applied.Leaves)
	}
}

// TestExecutionReportPersistenceFrom_FillCarriesCommission proves the real mapper
// copies a fill's structured commission onto both the fill event payload (which
// the attestation is built from) and the persisted trade (which the trades
// listing is built from), so the two agree.
func TestExecutionReportPersistenceFrom_FillCarriesCommission(t *testing.T) {
	t.Parallel()
	in := domain.ExecutionReportInput{
		Account:        domain.AccountID(testAccount),
		Order:          testOrderXID(0x12),
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		Side:           domain.OrderSideBuy,
		FillQuantity:   "3.5",
		FillPrice:      "150.20",
		LeavesQuantity: "6.5",
		OrderStatus:    domain.OrderStatusPartiallyFilled,
		Commission:     &domain.Commission{Amount: "-0.30", Currency: "USDT"},
	}
	persistence := executionReportPersistenceFrom(in, nil, nil, "", "")

	if persistence.Trade == nil {
		t.Fatal("persistence.Trade is nil, want a fill trade")
	}
	if persistence.Trade.Commission == nil ||
		persistence.Trade.Commission.Amount != "-0.30" ||
		persistence.Trade.Commission.Currency != "USDT" {
		t.Fatalf("trade commission = %+v, want -0.30/USDT",
			persistence.Trade.Commission)
	}
	var fill *domain.OrderEvent
	for i := range persistence.Events {
		if persistence.Events[i].Type == domain.OrderEventFill {
			fill = &persistence.Events[i]
		}
	}
	if fill == nil {
		t.Fatalf("no fill event recorded; events=%+v", persistence.Events)
	}
	if fill.Payload.Commission == nil ||
		fill.Payload.Commission.Amount != "-0.30" ||
		fill.Payload.Commission.Currency != "USDT" {
		t.Fatalf("fill event commission = %+v, want -0.30/USDT",
			fill.Payload.Commission)
	}
}

// TestExecutionReportPersistenceFrom_KeepsEveryBlockOffTheEventPayload proves
// the mapper neither truncates the engine's block list nor stamps reject fields
// the engine never produced for the fill: every block travels whole in Blocks
// while the event payload stays free of reject text and scope.
func TestExecutionReportPersistenceFrom_KeepsEveryBlockOffTheEventPayload(t *testing.T) {
	t.Parallel()
	in := domain.ExecutionReportInput{
		Account:        domain.AccountID(testAccount),
		Order:          testOrderXID(0x14),
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "100",
		LeavesQuantity: "0",
		OrderStatus:    domain.OrderStatusFilled,
	}
	blocks := []domain.ExecutionAccountBlock{
		{
			Account: domain.AccountID(testAccount),
			Policy:  "spot_funds",
			Code:    "pnl_kill_switch_triggered",
			Reason:  "first block",
		},
		{
			Account: domain.AccountID(testAccount),
			Policy:  "order_validation",
			Code:    "account_blocked",
			Reason:  "second block",
		},
	}
	persistence := executionReportPersistenceFrom(in, blocks, nil, "", "")

	if len(persistence.Blocks) != 2 {
		t.Fatalf("persistence.Blocks = %+v, want both engine blocks", persistence.Blocks)
	}
	if len(persistence.Events) == 0 {
		t.Fatal("persistence.Events is empty, want the fill event")
	}
	for _, event := range persistence.Events {
		payload := event.Payload
		if payload.RejectCode != "" || payload.RejectScope != "" ||
			payload.RejectReason != "" || payload.RejectDetails != "" {
			t.Fatalf("event %q payload carries reject fields: %+v", event.Type, payload)
		}
	}
}
