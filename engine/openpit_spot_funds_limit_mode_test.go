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

// These tests drive the Spot Funds limit mode against a real OpenPit engine,
// so they need the native runtime like the reservation tests do.

package engine

import (
	"context"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
)

// newSpotFundsLimitModeTestEngine builds a real engine whose one account
// carries the quote asset as its currency and holds no balance at all, with
// the Spot Funds policy in TrackOnly when trackOnly and the given P&L-bounds
// barriers.
func newSpotFundsLimitModeTestEngine(
	t *testing.T, trackOnly bool, bounds ...domain.LimitSpotFundsPnlBounds,
) *openPitEngine {
	t.Helper()
	acct := account(testAccount)
	acct.Currency = testQuote
	eng, err := newTestOpenPitEngineBuildFunc(t)(fwengine.Snapshot{
		Assets:                   testAssets(),
		Accounts:                 []domain.Account{acct},
		SpotFundsPnlBoundsLimits: bounds,
		SpotFundsTrackOnly:       trackOnly,
	})
	if err != nil {
		t.Fatalf("NewOpenPitEngineBuildFunc: %v", err)
	}
	e := eng.(*openPitEngine)
	t.Cleanup(e.Stop)
	return e
}

// requireInsufficientFunds fails unless the order was refused with exactly one
// insufficient_funds reject.
func requireInsufficientFunds(t *testing.T, result fwengine.OrderResult, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if result.Accepted || len(result.Rejects) != 1 ||
		result.Rejects[0].Code != domain.RejectCodeInsufficientFunds {
		t.Fatalf("order from zero = %+v, want one insufficient_funds reject", result)
	}
}

// requireTrackedFromZero fails unless the testOrder from zero balances was
// accepted and its 500 quote hold left the quote's available at -500.
func requireTrackedFromZero(t *testing.T, result fwengine.OrderResult, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if !result.Accepted || len(result.Rejects) != 0 {
		t.Fatalf("order from zero = %+v, want accepted without rejects", result)
	}
	for _, outcome := range result.Outcomes {
		if outcome.Asset != testQuote {
			continue
		}
		if outcome.Outcome.BalanceResult != "-500" || outcome.Outcome.HeldResult != "500" {
			t.Fatalf("quote outcome = %+v, want available -500 and held 500", outcome.Outcome)
		}
		return
	}
	t.Fatalf("quote outcome missing: %+v", result.Outcomes)
}

func TestSpotFundsLimitMode_EnforceRefusesOrderFromZero(t *testing.T) {
	t.Parallel()
	e := newSpotFundsLimitModeTestEngine(t, false)

	result, err := materializeOrderResult(e, testOrder())
	requireInsufficientFunds(t, result, err)
}

func TestSpotFundsLimitMode_TrackOnlyBuildAcceptsOrderFromZero(t *testing.T) {
	t.Parallel()
	e := newSpotFundsLimitModeTestEngine(t, true)

	result, err := materializeOrderResult(e, testOrder())
	requireTrackedFromZero(t, result, err)
}

func TestSpotFundsLimitMode_SetLiveSwitchesBothWays(t *testing.T) {
	t.Parallel()
	e := newSpotFundsLimitModeTestEngine(t, false)
	ctx := context.Background()
	asyncBefore := e.async

	result, err := materializeOrderResult(e, testOrder())
	requireInsufficientFunds(t, result, err)

	if err := e.SetSpotFundsTrackOnly(ctx, true); err != nil {
		t.Fatalf("SetSpotFundsTrackOnly(true): %v", err)
	}
	result, err = materializeOrderResult(e, testOrder())
	requireTrackedFromZero(t, result, err)

	if err := e.SetSpotFundsTrackOnly(ctx, false); err != nil {
		t.Fatalf("SetSpotFundsTrackOnly(false): %v", err)
	}
	result, err = materializeOrderResult(e, testOrder())
	requireInsufficientFunds(t, result, err)

	if e.async != asyncBefore {
		t.Fatal("SetSpotFundsTrackOnly replaced the engine handle, want a live change")
	}
}

// TestSpotFundsLimitMode_TrackOnlyKeepsPnlBarrier proves the P&L barrier does
// not depend on the mode: a losing round trip from zero breaches the account's
// lower bound, blocks the account, and the next order is refused although
// TrackOnly refuses nothing on funds.
func TestSpotFundsLimitMode_TrackOnlyKeepsPnlBarrier(t *testing.T) {
	t.Parallel()
	e := newSpotFundsLimitModeTestEngine(t, true, domain.LimitSpotFundsPnlBounds{
		Axis:       domain.SpotFundsPnlAxisRealized,
		Scope:      domain.ScopeAccount,
		Account:    domain.AccountID(testAccount),
		Currency:   testQuote,
		LowerBound: "-5",
	})

	buy := testOrder()
	buy.AmountValue = "1"
	bought, err := materializeImmediateResult(e, buy)
	if err != nil {
		t.Fatalf("buy SubmitImmediate: %v", err)
	}
	if !bought.Accepted || len(bought.Blocks) != 0 {
		t.Fatalf("buy from zero = %+v, want accepted without blocks", bought)
	}

	// Sell the bought unit 10 below its entry: -10 of realized P&L, past -5.
	sell := buy
	sell.Side = domain.OrderSideSell
	sell.Price = "90"
	sold, err := materializeImmediateResult(e, sell)
	if err != nil {
		t.Fatalf("sell SubmitImmediate: %v", err)
	}
	if !sold.Accepted {
		t.Fatalf("losing sell = %+v, want accepted", sold)
	}
	if len(sold.Blocks) != 1 || sold.Blocks[0].Account != domain.AccountID(testAccount) {
		t.Fatalf("losing sell blocks = %+v, want the account kill-switched", sold.Blocks)
	}

	next, err := materializeOrderResult(e, testOrder())
	if err != nil {
		t.Fatalf("SubmitOrder after block: %v", err)
	}
	if next.Accepted || len(next.Rejects) == 0 {
		t.Fatalf("order after P&L block = %+v, want refused", next)
	}
	for _, reject := range next.Rejects {
		if reject.Code == domain.RejectCodeInsufficientFunds {
			t.Fatalf("order after P&L block refused on funds in TrackOnly: %+v", next.Rejects)
		}
	}
}
