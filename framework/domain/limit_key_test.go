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
	"fmt"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

func TestStoredLimitKey(t *testing.T) {
	t.Parallel()
	limit := domain.LimitRate{Scope: domain.ScopeAsset, Asset: "AAPL"}
	key, err := domain.StoredLimitKey(limit)
	if err != nil {
		t.Fatal(err)
	}
	want, err := limit.Key()
	if err != nil || key != want {
		t.Fatalf("stored key = %v, want %v (error %v)", key, want, err)
	}
	limit.Scope = "corrupt-scope"
	_, err = domain.StoredLimitKey(limit)
	if err == nil || errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("stored key error must be corruption, not ErrInvalid: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%+v", limit)) {
		t.Fatalf("stored key error does not name barrier %+v: %v", limit, err)
	}
}

func TestFindLimit(t *testing.T) {
	t.Parallel()
	first := domain.LimitRate{Scope: domain.ScopeBroker, MaxOrders: 1}
	second := domain.LimitRate{Scope: domain.ScopeBroker, MaxOrders: 2}
	key, err := first.Key()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		limits  []domain.LimitRate
		want    domain.LimitRate
		found   bool
		corrupt bool
	}{
		{name: "found", limits: []domain.LimitRate{first, second}, want: first, found: true},
		{name: "not_found", limits: []domain.LimitRate{{Scope: domain.ScopeAsset, Asset: "AAPL"}}},
		{name: "corrupt", limits: []domain.LimitRate{{Scope: "corrupt-scope"}, first}, corrupt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, found, err := domain.FindLimit(tc.limits, key)
			if got != tc.want || found != tc.found {
				t.Fatalf("FindLimit = %+v, %t, want %+v, %t", got, found, tc.want, tc.found)
			}
			if tc.corrupt {
				if err == nil || errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("FindLimit error must be corruption, not ErrInvalid: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type limitKeyAxes struct {
	policy       string
	mode         domain.RuleMode
	scope        domain.LimitScope
	account      domain.AccountID
	accountGroup string
	asset        string
}

func (a limitKeyAxes) key() (domain.LimitKey, error) {
	return domain.NewLimitKey(
		a.policy, a.mode, a.scope, a.account, a.accountGroup, a.asset,
	)
}

func TestNewLimitKeyValidation(t *testing.T) {
	t.Parallel()
	invalid := ": " + domain.ErrInvalid.Error()
	for _, tc := range []struct {
		name       string
		axes       limitKeyAxes
		pointer    string
		constraint string
		message    string
	}{
		{
			name:    "order_size_empty_mode",
			axes:    limitKeyAxes{policy: domain.PolicyOrderSizeLimit, scope: domain.ScopeBroker},
			pointer: "/mode", constraint: "required",
			message: "mode is required" + invalid,
		},
		{
			name:    "order_size_unknown_mode",
			axes:    limitKeyAxes{policy: domain.PolicyOrderSizeLimit, mode: "unknown", scope: domain.ScopeBroker},
			pointer: "/mode", constraint: "unsupported_value",
			message: "unsupported order_size_limit mode \"unknown\"" + invalid,
		},
		{
			name:    "non_order_size_mode",
			axes:    limitKeyAxes{policy: domain.PolicyRateLimit, mode: domain.RuleModeReject, scope: domain.ScopeBroker},
			pointer: "/mode", constraint: "unsupported_value",
			message: "mode is only valid for order-size limits",
		},
		{
			name:    "unknown_policy",
			axes:    limitKeyAxes{policy: "unknown", scope: domain.ScopeBroker},
			pointer: "/scope", constraint: "unsupported_value",
			message: "unknown policy \"unknown\"" + invalid,
		},
		{
			name:    "scope_not_allowed",
			axes:    limitKeyAxes{policy: domain.PolicyRateLimit, scope: domain.ScopeGlobal},
			pointer: "/scope", constraint: "enum",
			message: "scope \"global\" not allowed for policy \"rate_limit\"" + invalid,
		},
		{
			name:    "missing_account",
			axes:    limitKeyAxes{policy: domain.PolicyRateLimit, scope: domain.ScopeAccount},
			pointer: "/account", constraint: "required",
			message: "scope \"account\" requires an account" + invalid,
		},
		{
			name:    "missing_asset",
			axes:    limitKeyAxes{policy: domain.PolicyRateLimit, scope: domain.ScopeAsset},
			pointer: "/asset", constraint: "required",
			message: "scope \"asset\" requires an asset" + invalid,
		},
		{
			name:    "missing_account_group",
			axes:    limitKeyAxes{policy: domain.PolicySpotFundsPnlBoundsKillSwitch, scope: domain.ScopeAccountGroup},
			pointer: "/accountGroup", constraint: "required",
			message: "scope \"account_group\" requires an account_group" + invalid,
		},
		{
			name:    "superfluous_account",
			axes:    limitKeyAxes{policy: domain.PolicyRateLimit, scope: domain.ScopeBroker, account: "acc"},
			pointer: "/account", constraint: "unsupported_value",
			message: "scope \"broker\" must not have account" + invalid,
		},
		{
			name:    "superfluous_asset",
			axes:    limitKeyAxes{policy: domain.PolicyRateLimit, scope: domain.ScopeBroker, asset: "AAPL"},
			pointer: "/asset", constraint: "unsupported_value",
			message: "scope \"broker\" must not have asset" + invalid,
		},
		{
			name:    "superfluous_account_group",
			axes:    limitKeyAxes{policy: domain.PolicyRateLimit, scope: domain.ScopeBroker, accountGroup: "desk"},
			pointer: "/accountGroup", constraint: "unsupported_value",
			message: "scope \"broker\" must not have account_group" + invalid,
		},
		{
			name:    "mode_before_scope",
			axes:    limitKeyAxes{policy: domain.PolicyOrderSizeLimit, scope: domain.ScopeGlobal},
			pointer: "/mode", constraint: "required",
			message: "mode is required" + invalid,
		},
		{
			name:    "unknown_mode_before_scope",
			axes:    limitKeyAxes{policy: domain.PolicyOrderSizeLimit, mode: "unknown", scope: domain.ScopeGlobal},
			pointer: "/mode", constraint: "unsupported_value",
			message: "unsupported order_size_limit mode \"unknown\"" + invalid,
		},
		{
			name:    "non_order_size_mode_before_unknown_policy",
			axes:    limitKeyAxes{policy: "unknown", mode: domain.RuleModeReject, scope: domain.ScopeGlobal},
			pointer: "/mode", constraint: "unsupported_value",
			message: "mode is only valid for order-size limits",
		},
		{
			name:    "zero_arguments",
			pointer: "/scope", constraint: "unsupported_value",
			message: "unknown policy \"\"" + invalid,
		},
		{
			name: "permanent_spot_funds_limit_mode",
			axes: limitKeyAxes{policy: domain.PolicySpotFundsLimitMode, scope: domain.ScopeGlobal},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, err := tc.axes.key()
			if tc.message == "" {
				if err != nil {
					t.Fatalf("NewLimitKey() error = %v", err)
				}
				if key == (domain.LimitKey{}) {
					t.Fatal("NewLimitKey() returned an invalid zero key")
				}
				assertLimitKeyAxes(t, key, tc.axes)
				return
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("NewLimitKey() error = %v, want ErrInvalid", err)
			}
			if got := domain.ValidationPointer(err); got != tc.pointer {
				t.Fatalf("pointer = %q, want %q", got, tc.pointer)
			}
			if got := domain.ValidationConstraint(err); got != tc.constraint {
				t.Fatalf("constraint = %q, want %q", got, tc.constraint)
			}
			if err.Error() != tc.message {
				t.Fatalf("error = %q, want %q", err, tc.message)
			}
			if key != (domain.LimitKey{}) {
				t.Fatalf("key = %+v, want zero on error", key)
			}
		})
	}
}

func TestLimitKeyAxesSeparateBarriers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		left, right limitKeyAxes
	}{
		{
			"policy",
			limitKeyAxes{policy: domain.PolicySpotFundsPnlBoundsKillSwitch, scope: domain.ScopeAccount, account: "acc"},
			limitKeyAxes{policy: domain.PolicySpotFundsUnrealizedPnlBoundsKillSwitch, scope: domain.ScopeAccount, account: "acc"},
		},
		{
			"mode_review_without_hold",
			limitKeyAxes{policy: domain.PolicyOrderSizeLimit, mode: domain.RuleModeReject, scope: domain.ScopeBroker},
			limitKeyAxes{policy: domain.PolicyOrderSizeLimit, mode: domain.RuleModeReviewWithoutHold, scope: domain.ScopeBroker},
		},
		{
			"mode_review_with_hold",
			limitKeyAxes{policy: domain.PolicyOrderSizeLimit, mode: domain.RuleModeReject, scope: domain.ScopeBroker},
			limitKeyAxes{policy: domain.PolicyOrderSizeLimit, mode: domain.RuleModeReviewWithHold, scope: domain.ScopeBroker},
		},
		{
			"scope",
			limitKeyAxes{policy: domain.PolicyOrderSizeLimit, mode: domain.RuleModeReject, scope: domain.ScopeUnderlyingAsset, asset: "AAPL"},
			limitKeyAxes{policy: domain.PolicyOrderSizeLimit, mode: domain.RuleModeReject, scope: domain.ScopeSettlementAsset, asset: "AAPL"},
		},
		{
			"account",
			limitKeyAxes{policy: domain.PolicyRateLimit, scope: domain.ScopeAccount, account: "acc-one"},
			limitKeyAxes{policy: domain.PolicyRateLimit, scope: domain.ScopeAccount, account: "acc-two"},
		},
		{
			"account_group",
			limitKeyAxes{policy: domain.PolicySpotFundsPnlBoundsKillSwitch, scope: domain.ScopeAccountGroup, accountGroup: "desk-one"},
			limitKeyAxes{policy: domain.PolicySpotFundsPnlBoundsKillSwitch, scope: domain.ScopeAccountGroup, accountGroup: "desk-two"},
		},
		{
			"asset",
			limitKeyAxes{policy: domain.PolicyRateLimit, scope: domain.ScopeAsset, asset: "AAPL"},
			limitKeyAxes{policy: domain.PolicyRateLimit, scope: domain.ScopeAsset, asset: "MSFT"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left, err := tc.left.key()
			if err != nil {
				t.Fatalf("left key: %v", err)
			}
			right, err := tc.right.key()
			if err != nil {
				t.Fatalf("right key: %v", err)
			}
			if left == right {
				t.Errorf("keys differing only in %s compare equal", tc.name)
			}
			if got := len(map[domain.LimitKey]bool{left: true, right: true}); got != 2 {
				t.Errorf("map entries = %d, want 2 for distinct %s", got, tc.name)
			}
			same, err := tc.left.key()
			if err != nil {
				t.Fatalf("identical key: %v", err)
			}
			if same != left {
				t.Fatal("identical arguments produced unequal keys")
			}
			if got := len(map[domain.LimitKey]bool{left: true, same: true}); got != 1 {
				t.Fatalf("map entries = %d, want 1 for identical arguments", got)
			}
		})
	}
}

func assertLimitKeyAxes(t *testing.T, key domain.LimitKey, want limitKeyAxes) {
	t.Helper()
	got := limitKeyAxes{
		policy: key.Policy(), mode: key.Mode(), scope: key.Scope(),
		account: key.Account(), accountGroup: key.AccountGroup(), asset: key.Asset(),
	}
	if got != want {
		t.Fatalf("key axes = %+v, want %+v", got, want)
	}
}

func TestLimitsKeyAxesAndPayload(t *testing.T) {
	t.Parallel()
	t.Run("rate", func(t *testing.T) {
		limit := domain.LimitRate{
			Scope: domain.ScopeAccountAsset, Account: "acc", Asset: "AAPL",
			Window: time.Second, MaxOrders: 10,
		}
		key, err := limit.Key()
		if err != nil {
			t.Fatal(err)
		}
		assertLimitKeyAxes(t, key, limitKeyAxes{
			policy: domain.PolicyRateLimit, scope: limit.Scope,
			account: limit.Account, asset: limit.Asset,
		})
		for _, payload := range []struct {
			window time.Duration
			count  uint64
		}{{time.Minute, 10}, {time.Second, 20}, {0, 0}} {
			changed := limit
			changed.Window, changed.MaxOrders = payload.window, payload.count
			if got, err := changed.Key(); err != nil || got != key {
				t.Fatalf("payload changed key: key = %+v, error = %v", got, err)
			}
		}
	})
	t.Run("order_size", func(t *testing.T) {
		limit := domain.LimitOrderSize{
			Mode:  domain.RuleModeReviewWithHold,
			Scope: domain.ScopeAccountUnderlyingAsset, Account: "acc",
			Asset: "AAPL", MaxQuantity: "10",
		}
		key, err := limit.Key()
		if err != nil {
			t.Fatal(err)
		}
		assertLimitKeyAxes(t, key, limitKeyAxes{
			policy: domain.PolicyOrderSizeLimit, mode: limit.Mode,
			scope: limit.Scope, account: limit.Account, asset: limit.Asset,
		})
		for _, caps := range [][2]string{{"20", ""}, {"10", "100"}, {"", ""}} {
			changed := limit
			changed.MaxQuantity, changed.MaxNotional = caps[0], caps[1]
			if got, err := changed.Key(); err != nil || got != key {
				t.Fatalf("payload changed key: key = %+v, error = %v", got, err)
			}
		}
	})
	t.Run("position", func(t *testing.T) {
		limit := domain.LimitPosition{
			Scope: domain.ScopeAccountAsset, Account: "acc", Asset: "AAPL",
			MaxPosition: "100",
		}
		key, err := limit.Key()
		if err != nil {
			t.Fatal(err)
		}
		assertLimitKeyAxes(t, key, limitKeyAxes{
			policy: domain.PolicyPositionLimit, scope: limit.Scope,
			account: limit.Account, asset: limit.Asset,
		})
		for _, ceiling := range []string{"200", ""} {
			changed := limit
			changed.MaxPosition = ceiling
			if got, err := changed.Key(); err != nil || got != key {
				t.Fatalf("payload changed key: key = %+v, error = %v", got, err)
			}
		}
	})
	t.Run("pnl_bounds", func(t *testing.T) {
		policies := []string{
			domain.PolicySpotFundsPnlBoundsKillSwitch,
			domain.PolicySpotFundsUnrealizedPnlBoundsKillSwitch,
			domain.PolicySpotFundsTotalPnlBoundsKillSwitch,
		}
		keys := make(map[domain.LimitKey]bool)
		for i, axis := range []domain.SpotFundsPnlAxis{
			domain.SpotFundsPnlAxisRealized,
			domain.SpotFundsPnlAxisUnrealized,
			domain.SpotFundsPnlAxisTotal,
		} {
			for _, scope := range []domain.LimitScope{
				domain.ScopeAccount, domain.ScopeAccountGroup,
			} {
				limit := domain.LimitSpotFundsPnlBounds{
					Axis: axis, Scope: scope, Currency: "USD",
					LowerBound: "-100", UpperBound: "100",
				}
				if scope == domain.ScopeAccount {
					limit.Account = "acc"
				} else {
					limit.AccountGroup = "desk"
				}
				key, err := limit.Key()
				if err != nil {
					t.Fatal(err)
				}
				assertLimitKeyAxes(t, key, limitKeyAxes{
					policy: policies[i], scope: limit.Scope,
					account: limit.Account, accountGroup: limit.AccountGroup,
				})
				if scope == domain.ScopeAccount {
					keys[key] = true
				}
				for _, payload := range [][3]string{
					{"EUR", "-100", "100"}, {"USD", "-200", "100"},
					{"USD", "-100", "200"}, {"", "", ""},
				} {
					changed := limit
					changed.Currency = payload[0]
					changed.LowerBound, changed.UpperBound = payload[1], payload[2]
					if got, err := changed.Key(); err != nil || got != key {
						t.Fatalf("payload changed key: key = %+v, error = %v", got, err)
					}
				}
			}
		}
		if len(keys) != 3 {
			t.Fatalf("P&L axis keys = %d, want 3", len(keys))
		}
	})
}

func TestLimitsKeyInvalidAxes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		key  func() (domain.LimitKey, error)
		axes limitKeyAxes
	}{
		{
			"rate",
			(domain.LimitRate{Scope: domain.ScopeAccount}).Key,
			limitKeyAxes{policy: domain.PolicyRateLimit, scope: domain.ScopeAccount},
		},
		{
			"order_size",
			(domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeUnderlyingAsset}).Key,
			limitKeyAxes{policy: domain.PolicyOrderSizeLimit, mode: domain.RuleModeReject, scope: domain.ScopeUnderlyingAsset},
		},
		{
			"order_size_mode",
			(domain.LimitOrderSize{Scope: domain.ScopeBroker}).Key,
			limitKeyAxes{policy: domain.PolicyOrderSizeLimit, scope: domain.ScopeBroker},
		},
		{
			"position",
			(domain.LimitPosition{Scope: domain.ScopeAccountAsset, Asset: "AAPL"}).Key,
			limitKeyAxes{policy: domain.PolicyPositionLimit, scope: domain.ScopeAccountAsset, asset: "AAPL"},
		},
		{
			"pnl_bounds",
			(domain.LimitSpotFundsPnlBounds{Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeAccountGroup}).Key,
			limitKeyAxes{policy: domain.PolicySpotFundsPnlBoundsKillSwitch, scope: domain.ScopeAccountGroup},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, err := tc.key()
			_, want := tc.axes.key()
			if !errors.Is(err, domain.ErrInvalid) || want == nil {
				t.Fatalf("Key() = %v, constructor error = %v, want ErrInvalid", err, want)
			}
			if err.Error() != want.Error() {
				t.Fatalf("Key() error = %q, want %q", err, want)
			}
			if got := domain.ValidationPointer(err); got != domain.ValidationPointer(want) {
				t.Fatalf("pointer = %q, want %q", got, domain.ValidationPointer(want))
			}
			if got := domain.ValidationConstraint(err); got != domain.ValidationConstraint(want) {
				t.Fatalf("constraint = %q, want %q", got, domain.ValidationConstraint(want))
			}
			if key != (domain.LimitKey{}) {
				t.Fatalf("key = %+v, want zero on error", key)
			}
		})
	}
}

func TestLimitSpotFundsPnlBoundsKeyUnknownAxis(t *testing.T) {
	t.Parallel()
	for _, axis := range []domain.SpotFundsPnlAxis{"", "unknown"} {
		limit := domain.LimitSpotFundsPnlBounds{Axis: axis, Scope: "unknown"}
		key, err := limit.Key()
		want := fmt.Sprintf("unknown spot funds pnl axis %q: %v", axis, domain.ErrInvalid)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Key() error = %v, want ErrInvalid", err)
		}
		if err.Error() != want || err.Error() != limit.Validate().Error() {
			t.Fatalf("Key() error = %q, want Validate() axis error %q", err, want)
		}
		if domain.ValidationPointer(err) != "" || domain.ValidationConstraint(err) != "" {
			t.Fatalf("axis error has field metadata: %v", err)
		}
		if key != (domain.LimitKey{}) {
			t.Fatalf("key = %+v, want zero on error", key)
		}
	}
}
