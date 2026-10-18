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
// Please see https://officer.openpit.dev and the OWNERS file for details.

package domain_test

import (
	"errors"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"go.openpit.dev/officer/framework/domain"
)

// loosening is the text every refused loosening ends with.
var loosening = domain.ErrLoosening.Error()

// rate builds a rate barrier of maxOrders over window, placed at no scope
// yet (see atScope).
func rate(maxOrders uint64, window time.Duration) domain.LimitRate {
	return domain.LimitRate{MaxOrders: maxOrders, Window: window}
}

// atScope places the rate barrier limit at scope, naming the account acc and
// the asset AAPL where the scope carries them.
func atScope(limit domain.LimitRate, scope domain.LimitScope) domain.LimitRate {
	limit.Scope = scope
	if scope.HasAccountAxis() {
		limit.Account = "acc"
	}
	if scope == domain.ScopeAsset || scope == domain.ScopeAccountAsset {
		limit.Asset = "AAPL"
	}
	return limit
}

// The rate policy's scopes, by how the engine counts a barrier's attempts: in
// a fixed window that opens at the first attempt after the previous window
// closes, or over a sliding window.
var (
	fixedWindowScopes   = []domain.LimitScope{domain.ScopeBroker, domain.ScopeAsset}
	slidingWindowScopes = []domain.LimitScope{
		domain.ScopeAccount, domain.ScopeAccountAsset,
	}
	rateScopes = slices.Concat(fixedWindowScopes, slidingWindowScopes)
)

// orderSize builds a broker order-size barrier; an empty cap is unset.
func orderSize(quantity, notional string) domain.LimitOrderSize {
	return domain.LimitOrderSize{
		Scope: domain.ScopeBroker, MaxQuantity: quantity, MaxNotional: notional,
	}
}

// pnlBounds builds a global Spot Funds P&L-bounds barrier; an empty bound is
// unset.
func pnlBounds(currency, lower, upper string) domain.LimitSpotFundsPnlBounds {
	return domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeGlobal, Currency: currency,
		LowerBound: lower, UpperBound: upper,
	}
}

// requireTightening fails the test unless err is nil when want is empty, or
// refuses a loosening with exactly the text want otherwise.
func requireTightening(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("NoLooserThan = %v, want nil", err)
		}
		return
	}
	if !errors.Is(err, domain.ErrLoosening) || err.Error() != want {
		t.Fatalf("NoLooserThan = %v, want ErrLoosening as %q", err, want)
	}
}

// rateTighteningCases compare a rate barrier, limit, with the one it would
// replace, ref, both placed at each of scopes; want is the refusal's text,
// empty when limit is accepted.
var rateTighteningCases = []struct {
	name       string
	scopes     []domain.LimitScope
	limit, ref domain.LimitRate
	want       string
}{
	{"equal", rateScopes, rate(10, time.Second), rate(10, time.Second), ""},
	{"fewer orders", rateScopes, rate(9, time.Second), rate(10, time.Second), ""},
	{
		"longer window", slidingWindowScopes,
		rate(10, 2*time.Second), rate(10, time.Second), "",
	},
	{
		"longer window", fixedWindowScopes,
		rate(10, 2*time.Second), rate(10, time.Second),
		"window 2s differs from the current 1s on a fixed-window scope: " + loosening,
	},
	{
		"fewest orders over the longest window", slidingWindowScopes,
		rate(1, 24*time.Hour), rate(1_000_000_000, time.Nanosecond), "",
	},
	{
		"fewest orders over the longest window", fixedWindowScopes,
		rate(1, 24*time.Hour), rate(1_000_000_000, time.Nanosecond),
		"window 24h0m0s differs from the current 1ns on a fixed-window scope: " +
			loosening,
	},
	{
		"more orders", rateScopes, rate(11, time.Second), rate(10, time.Second),
		"max_orders 11 above the current 10: " + loosening,
	},
	{
		"shorter window", slidingWindowScopes,
		rate(10, 999*time.Millisecond), rate(10, time.Second),
		"window 999ms below the current 1s: " + loosening,
	},
	{
		"shorter window", fixedWindowScopes,
		rate(10, 999*time.Millisecond), rate(10, time.Second),
		"window 999ms differs from the current 1s on a fixed-window scope: " +
			loosening,
	},
	{
		"more orders over a longer window", slidingWindowScopes,
		rate(20, 2*time.Second), rate(10, time.Second),
		"max_orders 20 above the current 10: " + loosening,
	},
	{
		"more orders over a longer window", fixedWindowScopes,
		rate(20, 2*time.Second), rate(10, time.Second),
		"window 2s differs from the current 1s on a fixed-window scope: " + loosening,
	},
	{
		"fewer orders over a shorter window", slidingWindowScopes,
		rate(5, 500*time.Millisecond), rate(10, time.Second),
		"window 500ms below the current 1s: " + loosening,
	},
	{
		"fewer orders over a shorter window", fixedWindowScopes,
		rate(5, 500*time.Millisecond), rate(10, time.Second),
		"window 500ms differs from the current 1s on a fixed-window scope: " +
			loosening,
	},
}

// TestLimitRateNoLooserThan accepts a rate barrier with no more orders than
// the barrier it replaces, over the same window at a fixed-window scope and
// over a window no shorter at a sliding-window one, and refuses any other,
// naming the reason.
func TestLimitRateNoLooserThan(t *testing.T) {
	t.Parallel()
	for _, tc := range rateTighteningCases {
		for _, scope := range tc.scopes {
			t.Run(string(scope)+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				limit, ref := atScope(tc.limit, scope), atScope(tc.ref, scope)
				requireTightening(t, limit.NoLooserThan(ref), tc.want)
			})
		}
	}
}

// TestLimitRateNoLooserThanFailsOnUnknownScope answers a rate barrier at a
// scope the rate policy does not allow with an error wrapping ErrInvalid and
// not ErrLoosening, instead of passing it, also when it equals the barrier it
// would replace.
func TestLimitRateNoLooserThanFailsOnUnknownScope(t *testing.T) {
	t.Parallel()
	for name, scope := range map[string]domain.LimitScope{
		"unset":            "",
		"global":           domain.ScopeGlobal,
		"underlying_asset": domain.ScopeUnderlyingAsset,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			limit := rate(10, time.Second)
			limit.Scope = scope
			err := limit.NoLooserThan(limit)
			if !errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrLoosening) {
				t.Fatalf("NoLooserThan = %v, want ErrInvalid and not ErrLoosening", err)
			}
		})
	}
}

// orderSizeTighteningCases compare an order-size barrier, limit, with the one
// it would replace or override, ref; want is the refusal's text, empty when
// limit is accepted.
var orderSizeTighteningCases = []struct {
	name       string
	limit, ref domain.LimitOrderSize
	want       string
}{
	{"equal", orderSize("100", "1000"), orderSize("100", "1000"), ""},
	{"equal by value", orderSize("100.0", "1e3"), orderSize("100", "1000"), ""},
	{"stricter caps", orderSize("99.99", "999"), orderSize("100", "1000"), ""},
	{
		"a cap the current lacks",
		orderSize("100", "1000000"), orderSize("100", ""), "",
	},
	{
		"higher quantity", orderSize("100.01", "1000"), orderSize("100", "1000"),
		"max_quantity 100.01 above the current 100: " + loosening,
	},
	{
		"higher notional beside a stricter quantity",
		orderSize("50", "1000.5"), orderSize("100", "1000"),
		"max_notional 1000.5 above the current 1000: " + loosening,
	},
	{
		"dropped quantity", orderSize("", "500"), orderSize("100", "1000"),
		"max_quantity unset while the current is 100: " + loosening,
	},
	{
		"dropped notional", orderSize("50", ""), orderSize("100", "1000"),
		"max_notional unset while the current is 1000: " + loosening,
	},
}

// TestLimitOrderSizeNoLooserThan accepts an order-size barrier carrying every
// cap of the barrier it replaces or overrides, none above it and compared by
// value, whatever it adds, and refuses any other, naming the looser cap.
func TestLimitOrderSizeNoLooserThan(t *testing.T) {
	t.Parallel()
	for _, tc := range orderSizeTighteningCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireTightening(t, tc.limit.NoLooserThan(tc.ref), tc.want)
		})
	}
}

// pnlBoundsTighteningCases compare a Spot Funds P&L-bounds barrier, limit,
// with the one it would replace or take over from, ref; want is the refusal's
// text, empty when limit is accepted.
var pnlBoundsTighteningCases = []struct {
	name       string
	limit, ref domain.LimitSpotFundsPnlBounds
	want       string
}{
	{
		"equal",
		pnlBounds("USD", "-100", "100"), pnlBounds("USD", "-100", "100"), "",
	},
	{
		"equal by value",
		pnlBounds("USD", "-100.00", "1e2"), pnlBounds("USD", "-100", "100"), "",
	},
	{
		"negative zero",
		pnlBounds("USD", "-0", "0"), pnlBounds("USD", "0", "-0"), "",
	},
	{
		"stricter bounds",
		pnlBounds("USD", "-50", "50"), pnlBounds("USD", "-100", "100"), "",
	},
	{
		"a lower bound the current lacks",
		pnlBounds("USD", "-1000000", "100"), pnlBounds("USD", "", "100"), "",
	},
	{
		"an upper bound the current lacks",
		pnlBounds("USD", "-100", "1000000"), pnlBounds("USD", "-100", ""), "",
	},
	{
		"lower bound lowered",
		pnlBounds("USD", "-100.01", "100"), pnlBounds("USD", "-100", "100"),
		"lower_bound -100.01 below the current -100: " + loosening,
	},
	{
		"lower bound below negative zero",
		pnlBounds("USD", "-0.0001", ""), pnlBounds("USD", "-0", ""),
		"lower_bound -0.0001 below the current -0: " + loosening,
	},
	{
		"upper bound raised",
		pnlBounds("USD", "-100", "100.01"), pnlBounds("USD", "-100", "100"),
		"upper_bound 100.01 above the current 100: " + loosening,
	},
	{
		"dropped lower bound",
		pnlBounds("USD", "", "100"), pnlBounds("USD", "-100", "100"),
		"lower_bound unset while the current is -100: " + loosening,
	},
	{
		"dropped upper bound",
		pnlBounds("USD", "-100", ""), pnlBounds("USD", "-100", "100"),
		"upper_bound unset while the current is 100: " + loosening,
	},
	{
		"another currency",
		pnlBounds("EUR", "-100", "100"), pnlBounds("USD", "-100", "100"),
		"currency EUR differs from the current USD: " + loosening,
	},
	{
		"another currency with stricter bounds",
		pnlBounds("EUR", "-1", "1"), pnlBounds("USD", "-100", "100"),
		"currency EUR differs from the current USD: " + loosening,
	},
}

// TestLimitSpotFundsPnlBoundsNoLooserThan accepts a Spot Funds P&L-bounds
// barrier in the currency of the barrier it replaces or takes over from,
// carrying each of its bounds with the lower one not below and the upper one
// not above it, compared by value, whatever it adds, and refuses any other,
// naming what loosens.
func TestLimitSpotFundsPnlBoundsNoLooserThan(t *testing.T) {
	t.Parallel()
	for _, tc := range pnlBoundsTighteningCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireTightening(t, tc.limit.NoLooserThan(tc.ref), tc.want)
		})
	}
}

// delta builds an adjustment amount that changes a field by value.
func delta(value string) *domain.AdjustmentAmount {
	return &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: value}
}

// absolute builds an adjustment amount that sets a field to value.
func absolute(value string) *domain.AdjustmentAmount {
	return &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: value}
}

// adjustUSD builds an adjustment of USD naming balance, held and incoming,
// each nil when it leaves the field alone.
func adjustUSD(balance, held, incoming *domain.AdjustmentAmount) domain.AdjustmentRequest {
	return domain.AdjustmentRequest{
		Asset: "USD", Balance: balance, Held: held, Incoming: incoming,
	}
}

// storedUSD is the stored balance of USD the adjustment cases apply to, and
// haltedUSD the same balance with its P&L calculation halted.
var (
	storedUSD = domain.Balance{
		Asset: "USD", Available: "100", Held: "10", Incoming: "20",
		AverageEntryPrice: "1", RealizedPnl: "5",
	}
	haltedUSD = domain.Balance{
		Asset: "USD", Available: "100", Held: "10", Incoming: "20",
		AverageEntryPrice: "1", RealizedPnl: "5",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingCostBasis,
	}
)

// The texts the money and the books refusals of a USD adjustment begin with.
const (
	lowerUSD = "an agent may only lower the balance, held or incoming of the " +
		"account's currency USD; "
	books = "an agent may not change the average entry price or the realized P&L; "
)

// adjustmentTighteningCases decide a tighten-only caller's adjustment,
// request, of an account whose own currency is currency, against current, the
// stored balance of the asset; want is the refusal's text, empty when request
// is accepted.
var adjustmentTighteningCases = []struct {
	name     string
	currency string
	request  domain.AdjustmentRequest
	current  domain.Balance
	want     string
}{
	{"no field", "USD", adjustUSD(nil, nil, nil), storedUSD, ""},
	{"deltas below zero", "USD", adjustUSD(delta("-1"), delta("-10"), delta("-0.5")), storedUSD, ""},
	{"zero deltas", "USD", adjustUSD(delta("0"), delta("-0"), delta("0.000")), storedUSD, ""},
	{
		"balance delta above zero", "USD", adjustUSD(delta("0.01"), nil, nil), storedUSD,
		lowerUSD + "balance delta 0.01 lies above zero: " + loosening,
	},
	{
		"held delta above zero", "USD", adjustUSD(nil, delta("1"), nil), storedUSD,
		lowerUSD + "held delta 1 lies above zero: " + loosening,
	},
	{
		"incoming delta above zero", "USD", adjustUSD(nil, nil, delta("1e-28")), storedUSD,
		lowerUSD + "incoming delta 1e-28 lies above zero: " + loosening,
	},
	{
		"absolutes below the current", "USD",
		adjustUSD(absolute("99.99"), absolute("-1"), absolute("0")), storedUSD, "",
	},
	{
		"absolutes at the current, spelled otherwise", "USD",
		adjustUSD(absolute("100.00"), absolute("1e1"), absolute("20.0")), storedUSD, "",
	},
	{
		"balance absolute above the current", "USD",
		adjustUSD(absolute("100.01"), nil, nil), storedUSD,
		lowerUSD + "balance 100.01 above the current 100: " + loosening,
	},
	{
		"held absolute above the current", "USD",
		adjustUSD(nil, absolute("10.5"), nil), storedUSD,
		lowerUSD + "held 10.5 above the current 10: " + loosening,
	},
	{
		"incoming absolute above the current", "USD",
		adjustUSD(nil, nil, absolute("21")), storedUSD,
		lowerUSD + "incoming 21 above the current 20: " + loosening,
	},
	{
		"one field of two above", "USD", adjustUSD(delta("-1"), delta("1"), nil), storedUSD,
		lowerUSD + "held delta 1 lies above zero: " + loosening,
	},
	{
		"absolutes at or below zero on no balance", "USD",
		adjustUSD(absolute("0"), absolute("-1"), nil), domain.Balance{}, "",
	},
	{
		"absolute above zero on no balance", "USD",
		adjustUSD(absolute("0.1"), nil, nil), domain.Balance{},
		lowerUSD + "balance 0.1 above the current 0: " + loosening,
	},
	{
		"a position", "USD",
		domain.AdjustmentRequest{Asset: "AAPL", Balance: delta("-1")}, storedUSD,
		"an agent may only lower the balance, held or incoming of the account's " +
			"currency USD; asset AAPL is not it: " + loosening,
	},
	{
		"another currency", "USD",
		domain.AdjustmentRequest{Asset: "EUR", Held: absolute("0")}, storedUSD,
		"an agent may only lower the balance, held or incoming of the account's " +
			"currency USD; asset EUR is not it: " + loosening,
	},
	{
		"an account without a currency of its own", "",
		adjustUSD(delta("-1"), nil, nil), storedUSD,
		"an agent may only lower the balance, held or incoming of the account's " +
			"own currency; the account has none: " + loosening,
	},
	{
		"average entry price at the current, spelled otherwise", "USD",
		domain.AdjustmentRequest{Asset: "USD", AverageEntryPrice: "1.0"}, storedUSD, "",
	},
	{
		"average entry price above the current", "USD",
		domain.AdjustmentRequest{Asset: "USD", AverageEntryPrice: "1.5"}, storedUSD,
		books + "average entry price 1.5 above the current 1: " + loosening,
	},
	{
		"average entry price below the current", "USD",
		domain.AdjustmentRequest{Asset: "USD", AverageEntryPrice: "0.5"}, storedUSD,
		books + "average entry price 0.5 below the current 1: " + loosening,
	},
	{
		"average entry price while the current is unset", "USD",
		domain.AdjustmentRequest{Asset: "USD", AverageEntryPrice: "1"},
		domain.Balance{Asset: "USD", Available: "100"},
		books + "average entry price 1 given while the current is unset: " + loosening,
	},
	{
		"realized P&L at the current, spelled otherwise", "USD",
		domain.AdjustmentRequest{Asset: "USD", RealizedPnl: "5.00"}, storedUSD, "",
	},
	{
		"realized P&L above the current", "USD",
		domain.AdjustmentRequest{Asset: "USD", RealizedPnl: "6"}, storedUSD,
		books + "realized P&L 6 above the current 5: " + loosening,
	},
	{
		"realized P&L below the current", "USD",
		domain.AdjustmentRequest{Asset: "USD", RealizedPnl: "-5"}, storedUSD,
		books + "realized P&L -5 below the current 5: " + loosening,
	},
	{
		"realized P&L while the current is unset", "USD",
		domain.AdjustmentRequest{Asset: "USD", RealizedPnl: "0"}, domain.Balance{},
		books + "realized P&L 0 given while the current is unset: " + loosening,
	},
	{
		"realized P&L at the current of a halted position", "USD",
		domain.AdjustmentRequest{Asset: "USD", RealizedPnl: "5"}, haltedUSD,
		"an agent may not set the realized P&L of a position whose P&L " +
			"calculation is halted for missing_cost_basis, as it would restart it: " +
			loosening,
	},
	{
		"a lowering of a halted position", "USD",
		adjustUSD(delta("-1"), nil, nil), haltedUSD, "",
	},
	{
		"a halt", "USD",
		domain.AdjustmentRequest{
			Asset: "USD", RealizedPnlHaltReason: domain.PnlHaltReasonMissingCostBasis,
		},
		storedUSD,
		"an agent may not halt the position's P&L calculation: " + loosening,
	},
	{
		"a bound beside a lowering", "USD",
		domain.AdjustmentRequest{
			Asset: "USD", Balance: delta("-1"),
			BalanceBounds: &domain.AdjustmentBounds{Lower: "1000", Upper: "2000"},
		},
		storedUSD, "",
	},
}

// TestAdjustmentRequestNoLooserThan accepts a tighten-only caller's
// adjustment of the account's own currency that lowers its balance, held or
// incoming, compared by value, or names its average entry price or realized
// P&L only with the current value, and refuses any other, naming what an
// agent may change.
func TestAdjustmentRequestNoLooserThan(t *testing.T) {
	t.Parallel()
	for _, tc := range adjustmentTighteningCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireTightening(t, tc.request.NoLooserThan(tc.currency, tc.current), tc.want)
		})
	}
}

// TestAdjustmentRequestInExistingAccountCurrency accepts an adjustment of the
// account's own currency, whatever it changes, while that asset exists, and
// refuses one of any other asset, any on an account without a currency of its
// own, and one of the account's currency while no asset of it exists.
func TestAdjustmentRequestInExistingAccountCurrency(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		asset, currency string
		exists          bool
		want            string
	}{
		"the currency": {"USD", "USD", true, ""},
		"a position": {
			"AAPL", "USD", true,
			"an agent may only lower the balance, held or incoming of the " +
				"account's currency USD; asset AAPL is not it: " + loosening,
		},
		"no currency": {
			"USD", "", true,
			"an agent may only lower the balance, held or incoming of the " +
				"account's own currency; the account has none: " + loosening,
		},
		"a currency with no asset": {
			"XAU", "XAU", false,
			"an agent may only lower the balance, held or incoming of the " +
				"account's currency XAU; no such asset exists, and an agent may " +
				"not create one: " + loosening,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raising := domain.AdjustmentRequest{Asset: tc.asset, Balance: delta("1")}
			requireTightening(
				t, raising.InExistingAccountCurrency(tc.currency, tc.exists), tc.want,
			)
		})
	}
}

// TestAdjustmentIDOutsideTradingRuntime refuses a tighten-only caller's
// adjustment id in the trading runtime's namespace, the one the runtime books
// a venue fee under, saying it is reserved for the runtime, and accepts any
// other, the zero id the store generates included.
func TestAdjustmentIDOutsideTradingRuntime(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		id   domain.ExternalID
		want string
	}{
		"a venue fee's": {
			"trading:venue-1:fee:f-1",
			`adjustment id "trading:venue-1:fee:f-1": an id starting with ` +
				`"trading:" is reserved for the trading runtime: ` + loosening,
		},
		"another":                     {"desk-1-withdrawal-7", ""},
		"the namespace not leading":   {"desk-1:trading:venue-1:fee:f-1", ""},
		"the namespace without colon": {"trading-desk-1", ""},
		"the zero, store-generated":   {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requireTightening(t, domain.AdjustmentIDOutsideTradingRuntime(tc.id), tc.want)
		})
	}
}

// TestAdjustmentRequestNoLooserThanFailsOnUnknownMode answers an amount in a
// mode that is neither absolute nor delta with an error wrapping ErrInvalid
// and not ErrLoosening, instead of passing it, also for a value that would
// lower the field.
func TestAdjustmentRequestNoLooserThanFailsOnUnknownMode(t *testing.T) {
	t.Parallel()
	request := adjustUSD(&domain.AdjustmentAmount{Mode: "set", Value: "-1"}, nil, nil)
	err := request.NoLooserThan("USD", storedUSD)
	if !errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrLoosening) {
		t.Fatalf("NoLooserThan = %v, want ErrInvalid and not ErrLoosening", err)
	}
}

// TestNoLooserThanFailsOnUnparsableDecimal answers a compared cap, bound or
// adjustment value that is not a decimal the engine can hold, on either side
// of the comparison, with an error wrapping ErrInvalid and not ErrLoosening,
// instead of deciding without it - also when the value would be the stricter
// one, and within a second for an exponent the comparison would otherwise
// rescale by, so a comparison that builds the power fails the test instead of
// hanging it.
func TestNoLooserThanFailsOnUnparsableDecimal(t *testing.T) {
	t.Parallel()
	for name, compare := range map[string]func() error{
		"new quantity": func() error {
			return orderSize("1x", "").NoLooserThan(orderSize("100", ""))
		},
		"current notional": func() error {
			return orderSize("", "100").NoLooserThan(orderSize("", "abc"))
		},
		"new lower bound": func() error {
			return pnlBounds("USD", "-", "").NoLooserThan(pnlBounds("USD", "-100", ""))
		},
		"current upper bound": func() error {
			return pnlBounds("USD", "", "1").NoLooserThan(pnlBounds("USD", "", "1..0"))
		},
		"new quantity finer than the engine scale": func() error {
			return orderSize("0.00000000000000000000000000001", "").
				NoLooserThan(orderSize("100", ""))
		},
		"new quantity above the engine range": func() error {
			return orderSize("1e29", "").NoLooserThan(orderSize("100", ""))
		},
		"new quantity with a huge exponent": func() error {
			return orderSize("1e-2000000000", "").NoLooserThan(orderSize("100", ""))
		},
		"adjustment delta": func() error {
			return adjustUSD(delta("-1x"), nil, nil).NoLooserThan("USD", storedUSD)
		},
		"adjustment absolute": func() error {
			return adjustUSD(nil, absolute("1..0"), nil).NoLooserThan("USD", storedUSD)
		},
		"adjustment delta with a huge exponent": func() error {
			return adjustUSD(nil, nil, delta("-1e-2000000000")).
				NoLooserThan("USD", storedUSD)
		},
		"current held": func() error {
			return adjustUSD(nil, absolute("-1"), nil).
				NoLooserThan("USD", domain.Balance{Held: "abc"})
		},
		"new average entry price": func() error {
			return domain.AdjustmentRequest{Asset: "USD", AverageEntryPrice: "x"}.
				NoLooserThan("USD", storedUSD)
		},
		"current realized P&L": func() error {
			return domain.AdjustmentRequest{Asset: "USD", RealizedPnl: "1"}.
				NoLooserThan("USD", domain.Balance{RealizedPnl: "1e"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var err error
			requireWithinASecond(t, "NoLooserThan", func() { err = compare() })
			if !errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrLoosening) {
				t.Fatalf("NoLooserThan = %v, want ErrInvalid and not ErrLoosening", err)
			}
		})
	}
}

// requireRefused fails the test unless err refuses a loosening.
func requireRefused(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, domain.ErrLoosening) {
		t.Fatalf("%s: NoLooserThan = %v, want ErrLoosening", what, err)
	}
}

// requireAccepted fails the test unless err is nil.
func requireAccepted(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: NoLooserThan = %v, want nil", what, err)
	}
}

// FuzzLimitRateNoLooserThan holds every valid rate barrier, at each rate
// scope, no looser than itself, refuses it with more orders or a shorter
// window, and accepts it with fewer orders. It refuses it with a longer window
// at a fixed-window scope and accepts it at a sliding-window one.
func FuzzLimitRateNoLooserThan(f *testing.F) {
	for _, tc := range rateTighteningCases {
		for _, scope := range tc.scopes {
			for _, limit := range []domain.LimitRate{tc.limit, tc.ref} {
				f.Add(
					uint8(slices.Index(rateScopes, scope)),
					limit.MaxOrders, int64(limit.Window), uint64(1), int64(1),
				)
			}
		}
	}
	f.Fuzz(func(
		t *testing.T, scopeIndex uint8, maxOrders uint64, window int64,
		orderStep uint64, windowStep int64,
	) {
		scope := rateScopes[int(scopeIndex)%len(rateScopes)]
		ref := atScope(rate(maxOrders, time.Duration(window)), scope)
		if ref.Validate() != nil {
			t.Skip("not a valid rate barrier")
		}
		requireAccepted(t, "the barrier itself", ref.NoLooserThan(ref))

		step := orderStep%1_000_000_000 + 1
		more, fewer := ref, ref
		more.MaxOrders += step
		fewer.MaxOrders -= min(step, ref.MaxOrders)
		requireRefused(t, "more orders", more.NoLooserThan(ref))
		requireAccepted(t, "fewer orders", fewer.NoLooserThan(ref))

		shift := time.Duration(uint64(windowStep)%uint64(24*time.Hour)) + 1
		longer, shorter := ref, ref
		longer.Window += shift
		shorter.Window -= min(shift, ref.Window)
		requireRefused(t, "a shorter window", shorter.NoLooserThan(ref))
		if slices.Contains(fixedWindowScopes, scope) {
			requireRefused(t, "a longer fixed window", longer.NoLooserThan(ref))
		} else {
			requireAccepted(t, "a longer sliding window", longer.NoLooserThan(ref))
		}
	})
}

// rateCounter is the attempt counter the engine keeps for one rate barrier:
// push records an attempt at now, a time since the policy epoch, under the
// barrier's window and returns the attempt's count, and clone returns an
// independent copy.
type rateCounter interface {
	push(now, window time.Duration) uint64
	clone() rateCounter
}

// fixedWindowCounter models AtomicWindowCounter, the counter of a broker or
// asset barrier (rate_limit.rs lines 359-427): count attempts counted in the
// window that opened at start. A fresh counter's window opens at the policy
// epoch (fresh_slot and AtomicWindowCounter::new, lines 349-357 and 385-390).
type fixedWindowCounter struct {
	start time.Duration
	count uint64
}

// push mirrors AtomicWindowCounter::push (lines 392-404) on one thread, with
// rolled_over (lines 419-426): an attempt a full window or more after the
// window opened opens the next window and counts as its first, and every other
// attempt, a late one earlier than the window's opening too, counts in the
// current window.
func (c *fixedWindowCounter) push(now, window time.Duration) uint64 {
	if now >= c.start && now-c.start >= window {
		c.start, c.count = now, 1
		return 1
	}
	c.count++
	return c.count
}

func (c fixedWindowCounter) clone() rateCounter { return &c }

// slidingWindowLog models the log of an account or account_asset barrier: the
// times of the attempts it has counted, in time order (sliding_window_count,
// lines 820-846).
type slidingWindowLog []time.Duration

// push mirrors the recording path of sliding_window_count (lines 840-845): the
// attempt counts the entries not yet expired, plus itself (window_count, lines
// 921-927), then the expired entries, a prefix, leave the log (advance_window,
// lines 898-905) and the attempt joins it in time order (insert_in_time_order,
// lines 907-919). An entry expires once a full window has elapsed since it,
// and never while it is not in the past (entry_expired, lines 890-896).
func (l *slidingWindowLog) push(now, window time.Duration) uint64 {
	expired := func(entry time.Duration) bool {
		return now >= entry && now-entry >= window
	}
	log := *l
	count := uint64(len(log)-sort.Search(len(log), func(i int) bool {
		return !expired(log[i])
	})) + 1
	for len(log) > 0 && expired(log[0]) {
		log = log[1:]
	}
	*l = slices.Insert(log, sort.Search(len(log), func(i int) bool {
		return log[i] > now
	}), now)
	return count
}

func (l slidingWindowLog) clone() rateCounter {
	log := slices.Clone(l)
	return &log
}

// newRateCounter returns the fresh counter the engine keeps for a barrier at
// scope.
func newRateCounter(t *testing.T, scope domain.LimitScope) rateCounter {
	t.Helper()
	if slices.Contains(fixedWindowScopes, scope) {
		return &fixedWindowCounter{}
	}
	if !slices.Contains(slidingWindowScopes, scope) {
		t.Fatalf("scope %q has no engine counter", scope)
	}
	return &slidingWindowLog{}
}

// replay records attempts, in order, on counter under limit, as the engine's
// check does, and reports which of them the engine admits: every attempt takes
// its slot, admitted or not (check_start, lines 755-794, and the policy's
// contract, lines 434-436), and one whose count exceeds MaxOrders is rejected
// (over_limit, lines 853-858).
func replay(counter rateCounter, limit domain.LimitRate, attempts []time.Duration) []bool {
	admitted := make([]bool, len(attempts))
	for i, at := range attempts {
		admitted[i] = counter.push(at, limit.Window) <= limit.MaxOrders
	}
	return admitted
}

// TestLimitRateNoLooserThanRefusesLongerFixedWindow refuses a broker barrier
// of one order per 2s in place of one order per 1s, and shows on the engine's
// counter why a longer fixed window is looser: of attempts at 0s, 1.5s and 2s,
// the 1s barrier rejects the last, its window having reopened at 1.5s, while
// the 2s barrier rejects the second and admits the last, which reopens its
// window.
func TestLimitRateNoLooserThanRefusesLongerFixedWindow(t *testing.T) {
	t.Parallel()
	ref := atScope(rate(1, time.Second), domain.ScopeBroker)
	limit := atScope(rate(1, 2*time.Second), domain.ScopeBroker)
	requireRefused(t, "one order per 2s for one per 1s", limit.NoLooserThan(ref))
	attempts := []time.Duration{0, 1500 * time.Millisecond, 2 * time.Second}
	for _, tc := range []struct {
		limit domain.LimitRate
		want  []bool
	}{
		{ref, []bool{true, true, false}},
		{limit, []bool{true, false, true}},
	} {
		got := replay(newRateCounter(t, domain.ScopeBroker), tc.limit, attempts)
		if !slices.Equal(got, tc.want) {
			t.Fatalf(
				"one order per %s admits %v of the attempts at %v, want %v",
				tc.limit.Window, got, attempts, tc.want,
			)
		}
	}
}

// rateTick is the unit FuzzLimitRateNoLooserThanOnEngineCounters counts
// windows and attempt times in.
const rateTick = 250 * time.Millisecond

// FuzzLimitRateNoLooserThanOnEngineCounters holds, at each rate scope, that a
// barrier NoLooserThan accepts in place of ref admits no attempt ref rejects,
// on a model of the counters the engine keeps for rate barriers in pit's
// crates/openpit/src/pretrade/policies/rate_limit.rs (line numbers as of pit
// v0.9.0-13-g67766925): fixedWindowCounter for the broker and asset scopes,
// AtomicWindowCounter's push and rolled_over (lines 392-404 and 419-426), and
// slidingWindowLog for the account and account_asset scopes, the recording
// path of sliding_window_count (lines 840-845) with window_count,
// advance_window, insert_in_time_order and entry_expired (lines 921-927,
// 898-905, 907-919 and 890-896). The engine keeps a barrier's counter across a
// retune - set_broker and set_asset_barriers carry a surviving barrier's
// counter over (lines 239-243 and 271-276), and the account logs live in the
// policy, not in the barriers (lines 286-288 and 311-312) - so ref counts the
// attempts before the retune, and both barriers replay the rest from the
// counter it leaves. Windows and attempt times count rateTick; the attempts
// come in any order, one earlier than an attempt before it being a late clock
// reading.
//
// Plain go test runs only the seeds, each at every rate scope, so besides
// pairs NoLooserThan accepts they hold a pair it refuses for each of its
// reasons - more orders, a shorter window, and a longer window at a
// fixed-window scope - on attempts where that barrier admits one ref rejects:
// a rule that accepted the pair fails them.
//
// The model follows the pit checkout above, not the openpit v0.9.0 runtime
// Officer's go.mod pins. The fixed-window counter and the counter carry-over
// are the same in both. v0.9.0's account log appends a late reading and counts
// the whole log after trimming its front instead of inserting in time order;
// the verdict holds there too, since a barrier whose window is no shorter than
// ref's trims only entries ref has also expired, so ref's log stays a suffix of
// its log and its count is never below ref's.
func FuzzLimitRateNoLooserThanOnEngineCounters(f *testing.F) {
	everyTick := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	lateReadings := []byte{0, 5, 3, 9, 2, 12, 7, 15, 14, 1, 16, 11}
	for scopeIndex := range rateScopes {
		for _, seed := range []struct {
			refMax, refWindow, limitMax, limitWindow, retuneAt uint8
			attempts                                           []byte
		}{
			// Accepted at every scope.
			{1, 2, 1, 2, 0, everyTick},
			{3, 4, 2, 4, 6, everyTick},
			// A longer window: accepted at a sliding-window scope, refused at
			// a fixed-window one.
			{1, 2, 1, 3, 0, everyTick},
			{2, 4, 1, 8, 5, lateReadings},
			// Refused at every scope: more orders, then a shorter window.
			{1, 2, 2, 2, 0, everyTick},
			{1, 2, 1, 1, 0, everyTick},
		} {
			f.Add(
				uint8(scopeIndex), seed.refMax, seed.refWindow,
				seed.limitMax, seed.limitWindow, seed.retuneAt, seed.attempts,
			)
		}
	}
	f.Fuzz(func(
		t *testing.T, scopeIndex, refMax, refWindow, limitMax, limitWindow, retuneAt uint8,
		attempts []byte,
	) {
		scope := rateScopes[int(scopeIndex)%len(rateScopes)]
		ref := atScope(rate(uint64(refMax), time.Duration(refWindow)*rateTick), scope)
		limit := atScope(rate(uint64(limitMax), time.Duration(limitWindow)*rateTick), scope)
		if ref.Validate() != nil || limit.Validate() != nil {
			t.Skip("not two valid rate barriers")
		}
		if limit.NoLooserThan(ref) != nil {
			t.Skip("a barrier NoLooserThan refuses")
		}
		times := make([]time.Duration, len(attempts))
		for i, at := range attempts {
			times[i] = time.Duration(at) * rateTick
		}
		retune := int(retuneAt) % (len(times) + 1)
		counter := newRateCounter(t, scope)
		replay(counter, ref, times[:retune])
		refAdmits := replay(counter.clone(), ref, times[retune:])
		limitAdmits := replay(counter.clone(), limit, times[retune:])
		for i, at := range times[retune:] {
			if limitAdmits[i] && !refAdmits[i] {
				t.Fatalf(
					"%s: at most %d per %s admits the attempt at %s that at most %d per %s "+
						"rejects; attempts %v, the first %d before the retune",
					scope, limit.MaxOrders, limit.Window, at,
					ref.MaxOrders, ref.Window, times, retune,
				)
			}
		}
	})
}

// engineHolds reports whether value is unset or a decimal the engine can hold.
func engineHolds(value string) bool {
	if value == "" {
		return true
	}
	_, err := domain.ParseEngineDecimal(value)
	return err == nil
}

// fuzzStep parses raw as a positive decimal step the engine can hold, and
// skips the input otherwise.
func fuzzStep(t *testing.T, raw string) decimal.Decimal {
	t.Helper()
	step, err := domain.ParseEngineDecimal(raw)
	if err != nil || step.IsZero() {
		t.Skip("not a non-zero step the engine can hold")
	}
	return step.Abs()
}

// checkBound asserts the comparison's monotonicity in one bound, value, of a
// reference barrier: compareWith compares the reference with the bound
// replaced by its argument against the reference itself. A bound the
// reference lacks may be added with any value. A bound it carries is refused
// when dropped or moved by step the loose way - down for a floor, up
// otherwise - and accepted when moved the strict way, whenever the engine can
// hold the moved value.
func checkBound(
	t *testing.T, name, value string, step decimal.Decimal, floor bool,
	compareWith func(bound string) error,
) {
	t.Helper()
	if value == "" {
		requireAccepted(t, "an added "+name, compareWith(step.String()))
		return
	}
	requireRefused(t, "a dropped "+name, compareWith(""))
	current, err := domain.ParseEngineDecimal(value)
	if err != nil {
		t.Fatalf("ParseEngineDecimal(%q): %v", value, err)
	}
	raised, lowered := current.Add(step).String(), current.Sub(step).String()
	loose, strict := raised, lowered
	if floor {
		loose, strict = lowered, raised
	}
	if engineHolds(loose) {
		requireRefused(t, name+" "+loose+" from "+value, compareWith(loose))
	}
	if engineHolds(strict) {
		requireAccepted(t, name+" "+strict+" from "+value, compareWith(strict))
	}
}

// FuzzLimitOrderSizeNoLooserThan holds every valid order-size barrier the
// engine can hold no looser than itself, refuses it with a cap raised or
// dropped, and accepts it with a cap lowered or added.
func FuzzLimitOrderSizeNoLooserThan(f *testing.F) {
	for _, tc := range orderSizeTighteningCases {
		for _, limit := range []domain.LimitOrderSize{tc.limit, tc.ref} {
			f.Add(limit.MaxQuantity, limit.MaxNotional, "0.01")
		}
	}
	f.Fuzz(func(t *testing.T, quantity, notional, rawStep string) {
		ref := orderSize(quantity, notional)
		// The engine range check comes first: it bounds the work of every
		// later parse and comparison by the length of the input.
		if !engineHolds(quantity) || !engineHolds(notional) || ref.Validate() != nil {
			t.Skip("not a valid order-size barrier the engine can hold")
		}
		step := fuzzStep(t, rawStep)
		requireAccepted(t, "the barrier itself", ref.NoLooserThan(ref))
		checkBound(t, "max_quantity", quantity, step, false, func(bound string) error {
			limit := ref
			limit.MaxQuantity = bound
			return limit.NoLooserThan(ref)
		})
		checkBound(t, "max_notional", notional, step, false, func(bound string) error {
			limit := ref
			limit.MaxNotional = bound
			return limit.NoLooserThan(ref)
		})
	})
}

// FuzzLimitSpotFundsPnlBoundsNoLooserThan holds every valid Spot Funds
// P&L-bounds barrier the engine can hold no looser than itself, refuses it in
// another currency, with its lower bound lowered, its upper bound raised or
// either dropped, and accepts it with its lower bound raised, its upper bound
// lowered or a bound added.
func FuzzLimitSpotFundsPnlBoundsNoLooserThan(f *testing.F) {
	for _, tc := range pnlBoundsTighteningCases {
		for _, limit := range []domain.LimitSpotFundsPnlBounds{tc.limit, tc.ref} {
			f.Add(limit.LowerBound, limit.UpperBound, "0.5")
		}
	}
	f.Fuzz(func(t *testing.T, lower, upper, rawStep string) {
		ref := pnlBounds("USD", lower, upper)
		// Validate parses both bounds as decimals the engine can hold before
		// it compares them, so it bounds its own work by their length.
		if ref.Validate() != nil {
			t.Skip("not a valid P&L-bounds barrier the engine can hold")
		}
		step := fuzzStep(t, rawStep)
		requireAccepted(t, "the barrier itself", ref.NoLooserThan(ref))
		other := ref
		other.Currency = "EUR"
		requireRefused(t, "another currency", other.NoLooserThan(ref))
		checkBound(t, "lower_bound", lower, step, true, func(bound string) error {
			limit := ref
			limit.LowerBound = bound
			return limit.NoLooserThan(ref)
		})
		checkBound(t, "upper_bound", upper, step, false, func(bound string) error {
			limit := ref
			limit.UpperBound = bound
			return limit.NoLooserThan(ref)
		})
	})
}
