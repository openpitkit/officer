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

package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/reject"
)

func pnlModeSnapshot(pnl string) fwengine.Snapshot {
	acct := account(testAccount)
	acct.Currency = testQuote
	acct.EffectiveCurrency = testQuote
	acct.GroupCode = "desk-a"
	acct.Pnl = pnl
	return fwengine.Snapshot{
		SpotFundsLimitMode: domain.SpotFundsLimitModeEnforce,
		Assets:             testAssets(), Accounts: []domain.Account{acct},
		Groups:   []domain.AccountGroup{{Code: "desk-a", EngineGroupID: 7, Currency: testQuote}},
		Balances: []domain.Balance{fundedBalance(testAccount, testQuote, "1000000")},
	}
}

func pnlModeLimit(mode domain.RuleMode, scope domain.LimitScope, lower string) domain.LimitSpotFundsPnlBounds {
	limit := domain.LimitSpotFundsPnlBounds{
		Mode: mode, Axis: domain.SpotFundsPnlAxisRealized, Scope: scope,
		Currency: testQuote, LowerBound: lower,
	}
	if scope == domain.ScopeAccountGroup {
		limit.AccountGroup = "desk-a"
	}
	if scope == domain.ScopeAccount {
		limit.Account = testAccount
	}
	return limit
}

func TestSpotFundsPnlBounds_RuleModes(t *testing.T) {
	t.Parallel()
	for _, axis := range []domain.SpotFundsPnlAxis{domain.SpotFundsPnlAxisRealized, domain.SpotFundsPnlAxisUnrealized, domain.SpotFundsPnlAxisTotal} {
		for _, mode := range []domain.RuleMode{
			domain.RuleModeReject, domain.RuleModeReviewWithHold, domain.RuleModeReviewWithoutHold,
		} {
			for _, runtime := range []bool{false, true} {
				path := "build"
				if runtime {
					path = "runtime"
				}
				t.Run(string(axis)+"/"+string(mode)+"/"+path, func(t *testing.T) {
					limits := []domain.LimitSpotFundsPnlBounds{pnlModeLimit(mode, domain.ScopeGlobal, "-3")}
					limits[0].Axis = axis
					if axis == domain.SpotFundsPnlAxisUnrealized {
						limits[0].LowerBound = ""
						limits[0].UpperBound = "-3"
					}
					snap := pnlModeSnapshot("-5")
					if !runtime {
						snap.SpotFundsPnlBoundsLimits = limits
					}
					eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
					if err != nil {
						t.Fatal(err)
					}
					defer eng.Stop()
					if runtime {
						if _, err := eng.ConfigurePolicy(context.Background(), axis.Policy(),
							fwengine.LimitSet{SpotFundsPnlBoundsLimits: limits}); err != nil {
							t.Fatal(err)
						}
					}
					want := domain.VerdictReview
					if mode == domain.RuleModeReject {
						want = domain.VerdictReject
						if err := unblockAccountOnLane(context.Background(), eng, testAccount); err != nil {
							t.Fatal(err)
						}
					}
					order := testOrder()
					order.AmountValue = "1"
					assertCountingLimitOrder(t, eng, order, want, mode, nameSpotFunds, domain.RejectCodePnlKillSwitchTriggered)
					if want == domain.VerdictReview {
						requirePnlAccountUnblocked(t, eng)
					}
				})
			}
		}
	}
}

func requirePnlAccountUnblocked(t *testing.T, eng fwengine.Engine) {
	t.Helper()
	order, err := eng.OrderModel(testOrder(), fwengine.ReviewFlags{})
	if err != nil {
		t.Fatal(err)
	}
	err = fwengine.RunExclusive(context.Background(), eng.AsyncEngine(), func(driver asyncengine.Driver) error {
		request, rejects, err := driver.StartPreTrade(order)
		if request != nil {
			request.Close()
		}
		if err != nil {
			return err
		}
		for _, rejection := range rejects {
			if rejection.Code == reject.CodeAccountBlocked {
				return fmt.Errorf("review P&L bound latched account block: %+v", rejection)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSpotFundsPnlBounds_SameScopeModesCoexist(t *testing.T) {
	t.Parallel()
	for _, mode := range []domain.RuleMode{domain.RuleModeReviewWithHold, domain.RuleModeReviewWithoutHold} {
		for _, scope := range []domain.LimitScope{domain.ScopeGlobal, domain.ScopeAccountGroup, domain.ScopeAccount} {
			for _, runtime := range []bool{false, true} {
				for _, pnl := range []string{"-5", "-15"} {
					path := "build"
					if runtime {
						path = "runtime"
					}
					t.Run(string(mode)+"/"+string(scope)+"/"+path+"/"+pnl, func(t *testing.T) {
						limits := []domain.LimitSpotFundsPnlBounds{
							pnlModeLimit(domain.RuleModeReject, scope, "-10"), pnlModeLimit(mode, scope, "-3"),
						}
						snap := pnlModeSnapshot(pnl)
						if !runtime {
							snap.SpotFundsPnlBoundsLimits = limits
						}
						eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
						if err != nil {
							t.Fatal(err)
						}
						defer eng.Stop()
						if runtime {
							if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicySpotFundsPnlBoundsKillSwitch,
								fwengine.LimitSet{SpotFundsPnlBoundsLimits: limits}); err != nil {
								t.Fatal(err)
							}
						}
						if len(eng.(*openPitEngine).registered) != 1 {
							t.Fatalf("instances = %+v, want one SpotFunds", eng.(*openPitEngine).registered)
						}
						want := domain.VerdictReview
						if pnl == "-15" {
							want = domain.VerdictReject
						}
						invalid := append(append([]domain.LimitSpotFundsPnlBounds{}, limits...), limits[1])
						if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicySpotFundsPnlBoundsKillSwitch,
							fwengine.LimitSet{SpotFundsPnlBoundsLimits: invalid}); err == nil {
							t.Fatal("duplicate mode accepted")
						}
						if want == domain.VerdictReject {
							if err := unblockAccountOnLane(context.Background(), eng, testAccount); err != nil {
								t.Fatal(err)
							}
						}
						order := testOrder()
						order.AmountValue = "1"
						assertCountingLimitOrder(t, eng, order, want, mode, nameSpotFunds, domain.RejectCodePnlKillSwitchTriggered)
						if want == domain.VerdictReview {
							requirePnlAccountUnblocked(t, eng)
						}
					})
				}
			}
		}
	}
}

func TestSpotFundsPnlBounds_ReviewAccountDoesNotShadowRejectCascade(t *testing.T) {
	t.Parallel()
	for _, mode := range []domain.RuleMode{domain.RuleModeReviewWithHold, domain.RuleModeReviewWithoutHold} {
		for _, scope := range []domain.LimitScope{domain.ScopeGlobal, domain.ScopeAccountGroup} {
			for _, runtime := range []bool{false, true} {
				path := "build"
				if runtime {
					path = "runtime"
				}
				t.Run(string(mode)+"/"+string(scope)+"/"+path, func(t *testing.T) {
					reviewLimit := pnlModeLimit(mode, domain.ScopeAccount, "-100")
					limits := []domain.LimitSpotFundsPnlBounds{pnlModeLimit(domain.RuleModeReject, scope, "-10"), reviewLimit}
					snap := pnlModeSnapshot("-15")
					if !runtime {
						snap.SpotFundsPnlBoundsLimits = limits
					}
					engine, err := newTestOpenPitEngineBuildFunc(t)(snap)
					if err != nil {
						t.Fatal(err)
					}
					eng := engine.(*openPitEngine)
					defer eng.Stop()
					if runtime {
						if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicySpotFundsPnlBoundsKillSwitch,
							fwengine.LimitSet{SpotFundsPnlBoundsLimits: limits}); err != nil {
							t.Fatal(err)
						}
					}
					if err := unblockAccountOnLane(context.Background(), eng, testAccount); err != nil {
						t.Fatal(err)
					}
					order := testOrder()
					order.AmountValue = "1"
					assertCountingLimitOrder(t, eng, order, domain.VerdictReject, mode, nameSpotFunds, domain.RejectCodePnlKillSwitchTriggered)
					// With both bounds breached, reject still wins.
					if _, err := setTestAccountPnlState(context.Background(), eng, "-150", ""); err != nil {
						t.Fatal(err)
					}
					if err := unblockAccountOnLane(context.Background(), eng, testAccount); err != nil {
						t.Fatal(err)
					}
					assertCountingLimitOrder(t, eng, order, domain.VerdictReject, mode, nameSpotFunds, domain.RejectCodePnlKillSwitchTriggered)
					// Clearing reject exposes the account's review mark without changing its mode.
					if err := unblockAccountOnLane(context.Background(), eng, testAccount); err != nil {
						t.Fatal(err)
					}
					if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicySpotFundsPnlBoundsKillSwitch,
						fwengine.LimitSet{SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{reviewLimit}}); err != nil {
						t.Fatal(err)
					}
					assertCountingLimitOrder(t, eng, order, domain.VerdictReview, mode, nameSpotFunds, domain.RejectCodePnlKillSwitchTriggered)
					requirePnlAccountUnblocked(t, eng)
				})
			}
		}
	}
}

// FuzzPnlBoundValidateMatchesEngineParse holds, for every writing
// domain.ParseEngineDecimal accepts, that a global Spot Funds P&L-bounds
// barrier in USD carrying it as its only bound, lower or upper, passes
// Validate exactly when the engine parses the bound: pnlBoundOptions, which
// maps the bounds onto the engine with param.NewPnlFromString, takes it. Its
// seeds are the writings
// TestLimitSpotFundsPnlBounds_ValidateTakesEngineWritingsOnly in
// framework/domain expects Validate to accept or refuse by a reading of
// rust_decimal's source, plus a few plain ones, so go test without -fuzz holds
// those expectations against the engine's own parser.
func FuzzPnlBoundValidateMatchesEngineParse(f *testing.F) {
	// The largest digits an engine decimal holds, 2^96 - 1.
	const maxDigits = "79228162514264337593543950335"
	for _, writing := range []string{
		"-100", "100.5", "-0.0001", "+5", ".5", "5.", "-0",
		"0." + strings.Repeat("0", 27) + "1",
		maxDigits, maxDigits[:1] + "." + maxDigits[1:],
		"-1e2", "1E2", "1.5e-3", ".-5",
		"1." + strings.Repeat("0", 29),
		"0." + strings.Repeat("0", 29),
		"10." + strings.Repeat("0", 28),
		maxDigits + ".0",
		"0", "1", "-1", "0.1", "12345.6789", "-" + maxDigits,
	} {
		f.Add(writing)
	}
	f.Fuzz(func(t *testing.T, writing string) {
		if _, err := domain.ParseEngineDecimal(writing); err != nil {
			t.Skip("not a decimal ParseEngineDecimal accepts")
		}
		for _, bound := range []struct{ name, lower, upper string }{
			{"lower", writing, ""},
			{"upper", "", writing},
		} {
			validateErr := (domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject,
				Axis:  domain.SpotFundsPnlAxisRealized,
				Scope: domain.ScopeGlobal, Currency: "USD",
				LowerBound: bound.lower, UpperBound: bound.upper,
			}).Validate()
			_, _, parseErr := pnlBoundOptions(
				bound.lower, bound.upper, "spot_funds_pnl_bounds",
			)
			if (validateErr == nil) != (parseErr == nil) {
				t.Fatalf(
					"%s bound %q: Validate = %v, while the engine's parse = %v",
					bound.name, writing, validateErr, parseErr,
				)
			}
		}
	})
}
