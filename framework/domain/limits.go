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

package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// LimitScope identifies the axis combination a single barrier applies to. It is
// a closed, hardcoded enum stored as plain text; the per-policy allowed sets
// live in allowedScopes.
type LimitScope = string

// LimitRate is the typed rate-limit barrier: at most MaxOrders new orders per
// rolling Window for the addressed scope. It maps to the engine's rate-limit
// policy. The optional Account and Asset axes are present exactly when Scope
// carries them. MaxOrders is a positive count; Window is a positive duration.
//
// Money and size barriers live on the order-size and P&L types; a rate barrier
// carries no decimal value, so its value columns are a plain count and a Go
// duration.
type LimitRate struct {
	// Scope is the axis combination this barrier applies to.
	Scope LimitScope
	// Account is the account axis; empty unless Scope carries it.
	Account AccountID
	// Asset is the asset axis; empty unless Scope carries it.
	Asset string
	// Window is the rolling time window the order count is measured over.
	Window time.Duration
	// MaxOrders is the maximum number of orders allowed within Window.
	MaxOrders uint64
}

// LimitOrderSize is the typed order-size barrier: a per-order ceiling on
// quantity and/or notional for the addressed scope. It maps to the engine's
// order-size policy. At least one of MaxQuantity or MaxNotional is set; an unset
// bound is the empty string. Both are exact decimal strings, never float.
type LimitOrderSize struct {
	// Scope is the axis combination this barrier applies to.
	Scope LimitScope
	// Account is the account axis; empty unless Scope carries it.
	Account AccountID
	// Asset is the asset axis; empty unless Scope carries it.
	Asset string
	// MaxQuantity is the per-order quantity ceiling (exact decimal); empty when
	// unset.
	MaxQuantity string
	// MaxNotional is the per-order notional ceiling (exact decimal); empty when
	// unset.
	MaxNotional string
}

// LimitPnlBounds is the typed P&L kill-switch barrier: lower and/or upper
// accumulated-P&L bounds that trip the kill switch for the addressed scope. It
// maps to the engine's P&L-bounds policy. At least one of LowerBound or
// UpperBound is set; when both are set LowerBound must be <= UpperBound.
// InitialPnl seeds the accumulator and is valid only on the account-asset
// scope. All three are exact decimal strings, never float; an unset bound is the
// empty string.
type LimitPnlBounds struct {
	// Scope is the axis combination this barrier applies to.
	Scope LimitScope
	// Account is the account axis; empty unless Scope carries it.
	Account AccountID
	// Asset is the asset axis; empty unless Scope carries it.
	Asset string
	// LowerBound is the lower accumulated-P&L bound (exact decimal); empty when
	// unset.
	LowerBound string
	// UpperBound is the upper accumulated-P&L bound (exact decimal); empty when
	// unset.
	UpperBound string
	// InitialPnl seeds the per-account accumulated P&L at barrier construction
	// (exact decimal); valid only on the account-asset scope, empty otherwise.
	InitialPnl string
}

// allowedScopes maps each policy to its permitted scopes. It is the engine
// mapping the typed validators enforce; only the storage shape changed when the
// EAV table became three typed barriers.
var allowedScopes = map[string][]LimitScope{
	PolicyRateLimit:           {ScopeBroker, ScopeAsset, ScopeAccount, ScopeAccountAsset},
	PolicyOrderSizeLimit:      {ScopeBroker, ScopeAsset, ScopeAccountAsset},
	PolicyPnlBoundsKillSwitch: {ScopeAsset, ScopeAccountAsset},
}

// Validate checks the rate barrier against the engine mapping: an allowed scope
// for the rate policy, the account/asset axes present consistently with the
// scope, and a positive order count over a positive, at-most-24h window.
func (l LimitRate) Validate() error {
	if err := validateScopeAndAxes(PolicyRateLimit, l.Scope, l.Account, l.Asset); err != nil {
		return err
	}
	// The engine parses the order count with strconv.ParseUint, so the count is
	// modelled as an integer here rather than a decimal string: any in-range
	// value round-trips through the engine without a non-canonical encoding.
	if l.MaxOrders == 0 || l.MaxOrders > 1_000_000_000 {
		return fmt.Errorf("max_orders must be > 0 and <= 1e9: %w", ErrInvalid)
	}
	if l.Window <= 0 {
		return fmt.Errorf("window must be > 0: %w", ErrInvalid)
	}
	if l.Window > 24*time.Hour {
		return fmt.Errorf("window must be <= 24h: %w", ErrInvalid)
	}
	return nil
}

// Validate checks the order-size barrier against the engine mapping: an allowed
// scope for the order-size policy, consistent axes, and at least one positive
// decimal ceiling.
func (l LimitOrderSize) Validate() error {
	if err := validateScopeAndAxes(PolicyOrderSizeLimit, l.Scope, l.Account, l.Asset); err != nil {
		return err
	}
	if l.MaxQuantity == "" && l.MaxNotional == "" {
		return fmt.Errorf(
			"order_size_limit requires at least max_quantity or max_notional: %w",
			ErrInvalid,
		)
	}
	if l.MaxQuantity != "" {
		if err := validatePositiveDecimal(l.MaxQuantity); err != nil {
			return fmt.Errorf("max_quantity: %w", err)
		}
	}
	if l.MaxNotional != "" {
		if err := validatePositiveDecimal(l.MaxNotional); err != nil {
			return fmt.Errorf("max_notional: %w", err)
		}
	}
	return nil
}

// Validate checks the P&L-bounds barrier against the engine mapping: an allowed
// scope for the P&L policy, consistent axes, at least one bound, lower <= upper
// when both are present, and initial_pnl only on the account-asset scope.
func (l LimitPnlBounds) Validate() error {
	if err := validateScopeAndAxes(PolicyPnlBoundsKillSwitch, l.Scope, l.Account, l.Asset); err != nil {
		return err
	}
	if l.LowerBound == "" && l.UpperBound == "" {
		return fmt.Errorf(
			"pnl_bounds_kill_switch requires at least lower_bound or upper_bound: %w",
			ErrInvalid,
		)
	}
	var lowerD, upperD decimal.Decimal
	var err error
	if l.LowerBound != "" {
		lowerD, err = decimal.NewFromString(l.LowerBound)
		if err != nil {
			return fmt.Errorf("lower_bound is not a valid decimal: %w", ErrInvalid)
		}
	}
	if l.UpperBound != "" {
		upperD, err = decimal.NewFromString(l.UpperBound)
		if err != nil {
			return fmt.Errorf("upper_bound is not a valid decimal: %w", ErrInvalid)
		}
	}
	if l.LowerBound != "" && l.UpperBound != "" && lowerD.GreaterThan(upperD) {
		return fmt.Errorf("lower_bound must be <= upper_bound: %w", ErrInvalid)
	}
	if l.InitialPnl != "" {
		// initial_pnl seeds the per-account accumulated P&L at construction; it
		// only exists on the account-asset barrier and is rejected elsewhere.
		if l.Scope != ScopeAccountAsset {
			return fmt.Errorf(
				"initial_pnl is only valid for the account_asset scope, not %q: %w",
				l.Scope, ErrInvalid,
			)
		}
		if _, err := decimal.NewFromString(l.InitialPnl); err != nil {
			return fmt.Errorf("initial_pnl is not a valid decimal: %w", ErrInvalid)
		}
	}
	return nil
}

// validateScopeAndAxes checks that scope is allowed for policy and that the
// account/asset axes are present exactly when the scope carries them.
func validateScopeAndAxes(policy string, scope LimitScope, account AccountID, asset string) error {
	allowed, ok := allowedScopes[policy]
	if !ok {
		return fmt.Errorf("unknown policy %q: %w", policy, ErrInvalid)
	}
	scopeOK := false
	for _, s := range allowed {
		if s == scope {
			scopeOK = true
			break
		}
	}
	if !scopeOK {
		return fmt.Errorf("scope %q not allowed for policy %q: %w", scope, policy, ErrInvalid)
	}

	needsAccount := scope == ScopeAccount || scope == ScopeAccountAsset
	needsAsset := scope == ScopeAsset || scope == ScopeAccountAsset

	// Only the presence rule is Officer's: the account/asset axis values are
	// parsed and format-checked downstream by the engine barrier build
	// (param.NewAsset / the account resolver), so no charset check here.
	if needsAccount {
		if account == "" {
			return fmt.Errorf("scope %q requires an account: %w", scope, ErrInvalid)
		}
	} else if account != "" {
		return fmt.Errorf("scope %q must not have account: %w", scope, ErrInvalid)
	}
	if needsAsset {
		if asset == "" {
			return fmt.Errorf("scope %q requires an asset: %w", scope, ErrInvalid)
		}
	} else if asset != "" {
		return fmt.Errorf("scope %q must not have asset: %w", scope, ErrInvalid)
	}
	return nil
}
