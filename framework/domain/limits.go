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

package domain

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// LimitScope identifies the axis combination a single barrier applies to. It is
// a closed, hardcoded enum stored as plain text; the per-policy allowed sets
// live in allowedScopes.
type LimitScope string

// HasAccountAxis reports whether the scope carries an account axis, so that a
// barrier of this scope names exactly one account.
func (s LimitScope) HasAccountAxis() bool {
	return s == ScopeAccount || s == ScopeAccountAsset ||
		s == ScopeAccountUnderlyingAsset ||
		s == ScopeAccountSettlementAsset
}

// LimitRate is the typed rate-limit barrier: at most MaxOrders new orders per
// Window for the addressed scope - a fixed window, started by the first order
// after the previous one expires, for the broker and asset scopes, and a
// rolling window for the account and account_asset scopes. It maps to the
// engine's rate-limit policy. The optional Account and Asset axes are present
// exactly when Scope carries them. MaxOrders is a positive count; Window is a
// positive duration.
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
	// Window is the time window the order count is measured over: fixed for
	// the broker and asset scopes, rolling for the account scopes.
	Window time.Duration
	// MaxOrders is the maximum number of orders allowed within Window.
	MaxOrders uint64
}

// LimitOrderSize is the typed order-size barrier: a per-order ceiling on
// quantity and/or notional for the addressed scope. Broker barriers may carry
// either or both caps. Underlying-asset scopes carry quantity; settlement-asset
// scopes carry notional. An unset bound is the empty string. Both are exact
// decimal strings, never float.
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

// LimitSpotFundsPnlBounds configures a currency-valued SpotFunds P&L barrier.
// The engine blocks an account-tier currency mismatch, but skips a nonmatching
// account-group or global barrier. Bounds are never FX-converted; Officer
// passes the currency through unchanged.
type LimitSpotFundsPnlBounds struct {
	// Scope is the cascade tier this barrier applies to.
	Scope LimitScope
	// Account is required only for the account scope.
	Account AccountID
	// AccountGroup is required only for the account_group scope.
	AccountGroup string
	// Currency is the required asset code that denominates both P&L bounds.
	Currency string
	// LowerBound is the lower P&L bound (exact decimal); empty when unset.
	LowerBound string
	// UpperBound is the upper P&L bound (exact decimal); empty when unset.
	UpperBound string
}

// allowedScopes maps each policy to its permitted scopes. It is the engine
// mapping the typed validators enforce; only the storage shape changed when the
// EAV table became three typed barriers.
var allowedScopes = map[string][]LimitScope{
	PolicyRateLimit: {ScopeBroker, ScopeAsset, ScopeAccount, ScopeAccountAsset},
	PolicyOrderSizeLimit: {
		ScopeBroker,
		ScopeUnderlyingAsset,
		ScopeSettlementAsset,
		ScopeAccountUnderlyingAsset,
		ScopeAccountSettlementAsset,
	},
	PolicySpotFundsPnlBoundsKillSwitch: {
		ScopeGlobal, ScopeAccountGroup, ScopeAccount,
	},
}

// Validate checks the rate barrier against the engine mapping: an allowed scope
// for the rate policy, the account/asset axes present consistently with the
// scope, and a positive order count over a positive, at-most-24h window.
func (l LimitRate) Validate() error {
	if err := ValidateLimitScopeAndAxes(
		PolicyRateLimit, l.Scope, l.Account, l.Asset, "",
	); err != nil {
		return err
	}
	// The engine parses the order count with strconv.ParseUint, so the count is
	// modelled as an integer here rather than a decimal string: any in-range
	// value round-trips through the engine without a non-canonical encoding.
	if l.MaxOrders == 0 || l.MaxOrders > 1_000_000_000 {
		return invalidField(
			"/maxOrders", "range",
			fmt.Errorf(
				"max_orders must be > 0 and <= 1e9: %w", ErrInvalid,
			),
		)
	}
	if l.Window <= 0 {
		return invalidField("/windowMs", "range", fmt.Errorf("window must be > 0: %w", ErrInvalid))
	}
	if l.Window > 24*time.Hour {
		return invalidField("/windowMs", "range", fmt.Errorf("window must be <= 24h: %w", ErrInvalid))
	}
	return nil
}

// Validate checks the order-size barrier against the engine mapping: an allowed
// scope for the order-size policy, consistent axes, and at least one positive
// decimal ceiling.
func (l LimitOrderSize) Validate() error {
	if err := ValidateLimitScopeAndAxes(
		PolicyOrderSizeLimit, l.Scope, l.Account, l.Asset, "",
	); err != nil {
		return err
	}
	switch l.Scope {
	case ScopeUnderlyingAsset, ScopeAccountUnderlyingAsset:
		if l.MaxQuantity == "" {
			return invalidField(
				"/maxQuantity", "required",
				fmt.Errorf(
					"scope %q requires max_quantity: %w", l.Scope, ErrInvalid,
				),
			)
		}
		if l.MaxNotional != "" {
			return invalidField(
				"/maxNotional", "unsupported_value",
				fmt.Errorf(
					"scope %q must not carry max_notional: %w", l.Scope, ErrInvalid,
				),
			)
		}
	case ScopeSettlementAsset, ScopeAccountSettlementAsset:
		if l.MaxNotional == "" {
			return invalidField(
				"/maxNotional", "required",
				fmt.Errorf(
					"scope %q requires max_notional: %w", l.Scope, ErrInvalid,
				),
			)
		}
		if l.MaxQuantity != "" {
			return invalidField(
				"/maxQuantity", "unsupported_value",
				fmt.Errorf(
					"scope %q must not carry max_quantity: %w", l.Scope, ErrInvalid,
				),
			)
		}
	}
	if l.MaxQuantity == "" && l.MaxNotional == "" {
		return invalidField(
			"", "",
			fmt.Errorf(
				"order_size_limit requires at least max_quantity or max_notional: %w",
				ErrInvalid,
			),
		)
	}
	if l.MaxQuantity != "" {
		if err := validatePositiveDecimal(l.MaxQuantity); err != nil {
			return invalidField("/maxQuantity", "positive_decimal", fmt.Errorf("max_quantity: %w", err))
		}
	}
	if l.MaxNotional != "" {
		if err := validatePositiveDecimal(l.MaxNotional); err != nil {
			return invalidField("/maxNotional", "positive_decimal", fmt.Errorf("max_notional: %w", err))
		}
	}
	return nil
}

// Validate checks the SpotFunds self-computed P&L-bounds barrier against the
// Officer meta-policy contract.
func (l LimitSpotFundsPnlBounds) Validate() error {
	if err := ValidateLimitScopeAndAxes(
		PolicySpotFundsPnlBoundsKillSwitch, l.Scope, l.Account, "", l.AccountGroup,
	); err != nil {
		return err
	}
	if l.Currency == "" {
		return invalidField("/currency", "required", fmt.Errorf("currency is required: %w", ErrInvalid))
	}
	if l.LowerBound == "" && l.UpperBound == "" {
		return invalidField(
			"", "",
			fmt.Errorf(
				"spot_funds_pnl_bounds_kill_switch requires at least lower_bound or upper_bound: %w",
				ErrInvalid,
			),
		)
	}
	if err := validatePnlBounds(l.LowerBound, l.UpperBound); err != nil {
		return err
	}
	return nil
}

// ValidateLimitScopeAndAxes checks that scope is allowed for policy and that the
// account/asset/account-group axes are present exactly when the scope carries
// them.
func ValidateLimitScopeAndAxes(
	policy string, scope LimitScope, account AccountID, asset string, accountGroup string,
) error {
	allowed, ok := allowedScopes[policy]
	if !ok {
		return invalidField("/scope", "unsupported_value", fmt.Errorf("unknown policy %q: %w", policy, ErrInvalid))
	}
	scopeOK := false
	for _, s := range allowed {
		if s == scope {
			scopeOK = true
			break
		}
	}
	if !scopeOK {
		return invalidField(
			"/scope", "enum",
			fmt.Errorf(
				"scope %q not allowed for policy %q: %w", scope, policy, ErrInvalid,
			),
		)
	}

	needsAccount := scope.HasAccountAxis()
	needsAccountGroup := scope == ScopeAccountGroup
	needsAsset := scope == ScopeAsset || scope == ScopeAccountAsset ||
		scope == ScopeUnderlyingAsset || scope == ScopeSettlementAsset ||
		scope == ScopeAccountUnderlyingAsset ||
		scope == ScopeAccountSettlementAsset

	// Only the presence rule is Officer's: axis values are parsed and
	// format-checked downstream by the engine barrier build or store resolvers,
	// so no charset check here.
	if needsAccount {
		if account == "" {
			return invalidField(
				"/account", "required",
				fmt.Errorf(
					"scope %q requires an account: %w", scope, ErrInvalid,
				),
			)
		}
		if err := ValidateAccountID(account); err != nil {
			return err
		}
	} else if account != "" {
		return invalidField(
			"/account", "unsupported_value",
			fmt.Errorf(
				"scope %q must not have account: %w", scope, ErrInvalid,
			),
		)
	}
	if needsAccountGroup {
		if accountGroup == "" {
			return invalidField(
				"/accountGroup", "required",
				fmt.Errorf(
					"scope %q requires an account_group: %w", scope, ErrInvalid,
				),
			)
		}
	} else if accountGroup != "" {
		return invalidField(
			"/accountGroup", "unsupported_value",
			fmt.Errorf(
				"scope %q must not have account_group: %w", scope, ErrInvalid,
			),
		)
	}
	if needsAsset {
		if asset == "" {
			return invalidField(
				"/asset", "required",
				fmt.Errorf(
					"scope %q requires an asset: %w", scope, ErrInvalid,
				),
			)
		}
	} else if asset != "" {
		return invalidField(
			"/asset", "unsupported_value",
			fmt.Errorf(
				"scope %q must not have asset: %w", scope, ErrInvalid,
			),
		)
	}
	return nil
}

// validatePnlBounds checks that each set bound is a decimal the engine can
// hold, written as the engine parses it, and that the lower bound is not above
// the upper one. Both are parsed with parseEngineBound before they are
// compared, so the comparison never rescales by the exponent a bound is
// written with.
func validatePnlBounds(lowerBound, upperBound string) error {
	var lowerD, upperD decimal.Decimal
	var err error
	if lowerBound != "" {
		lowerD, err = parseEngineBound(lowerBound)
		if err != nil {
			return invalidField(
				"/lowerBound", "decimal", fmt.Errorf("lower_bound: %w", err),
			)
		}
	}
	if upperBound != "" {
		upperD, err = parseEngineBound(upperBound)
		if err != nil {
			return invalidField(
				"/upperBound", "decimal", fmt.Errorf("upper_bound: %w", err),
			)
		}
	}
	if lowerBound != "" && upperBound != "" && lowerD.GreaterThan(upperD) {
		return invalidField(
			"", "",
			fmt.Errorf(
				"lower_bound must be <= upper_bound: %w", ErrInvalid,
			),
		)
	}
	return nil
}

// parseEngineBound parses a P&L bound as ParseEngineDecimal does and also
// refuses, with an error wrapping ErrInvalid, a bound written in a way the
// engine does not parse. The engine reads a bound with rust_decimal's
// Decimal::from_str_exact (rust_decimal 1.42.1, parse_str_radix_10_exact in
// src/str.rs): an optional leading sign, then digits with at most one point,
// every digit kept as written, at most 28 of them after the point, and all of
// them, read as one integer, below 2^96. ParseEngineDecimal takes more: an
// exponent, a sign right after a leading point, and trailing fractional zeros
// past either limit, since it drops them before it checks the scale and the
// range. A stored bound the engine cannot parse fails the policy
// configuration, and undoing that rebuilds the engine, which resets every
// rate-limit window.
func parseEngineBound(value string) (decimal.Decimal, error) {
	parsed, err := ParseEngineDecimal(value)
	if err != nil {
		return decimal.Decimal{}, err
	}
	unsigned := value
	if strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		unsigned = value[1:]
	}
	whole, fraction, _ := strings.Cut(unsigned, ".")
	digits := whole + fraction
	if strings.ContainsFunc(digits, func(r rune) bool { return r < '0' || r > '9' }) {
		return decimal.Decimal{}, fmt.Errorf(
			"%q is not in plain notation, an optional leading sign and digits with at most one point: %w",
			value, ErrInvalid,
		)
	}
	if len(fraction) > engineDecimalMaxScale {
		return decimal.Decimal{}, fmt.Errorf(
			"%q has %d fractional digits as written, at most %d: %w",
			value, len(fraction), engineDecimalMaxScale, ErrInvalid,
		)
	}
	coefficient, ok := new(big.Int).SetString(digits, 10)
	if !ok || coefficient.BitLen() > engineDecimalMantissaBits {
		return decimal.Decimal{}, fmt.Errorf(
			"%q has digits that, read as one integer as written, exceed %d bits: %w",
			value, engineDecimalMantissaBits, ErrInvalid,
		)
	}
	return parsed, nil
}

// NoLooserThan returns nil when the rate barrier is at least as strict as ref,
// the barrier it would replace: MaxOrders not above ref's, over a Window the
// scope's order count allows. The engine counts every attempt, admitted or
// not. A broker or asset barrier counts in a fixed window that opens at the
// first attempt after the previous window closes, so a window of any other
// length, a longer one too, can admit an attempt ref rejects: Window must
// equal ref's. An account or account_asset barrier counts over a sliding
// window, so Window must not be shorter than ref's. Otherwise it returns an
// error wrapping ErrLoosening that names the reason, or one wrapping
// ErrInvalid for a scope the rate policy does not allow.
func (l LimitRate) NoLooserThan(ref LimitRate) error {
	switch l.Scope {
	case ScopeBroker, ScopeAsset:
		if l.Window != ref.Window {
			return fmt.Errorf(
				"window %s differs from the current %s on a fixed-window scope: %w",
				l.Window, ref.Window, ErrLoosening,
			)
		}
	case ScopeAccount, ScopeAccountAsset:
		if l.Window < ref.Window {
			return fmt.Errorf(
				"window %s below the current %s: %w", l.Window, ref.Window, ErrLoosening,
			)
		}
	default:
		return fmt.Errorf(
			"scope %q not allowed for policy %q: %w",
			l.Scope, PolicyRateLimit, ErrInvalid,
		)
	}
	if l.MaxOrders > ref.MaxOrders {
		return fmt.Errorf(
			"max_orders %d above the current %d: %w",
			l.MaxOrders, ref.MaxOrders, ErrLoosening,
		)
	}
	return nil
}

// NoLooserThan returns nil when the order-size barrier is at least as strict
// as ref, the barrier it would replace or override: it carries every cap ref
// carries, none above ref's. A cap ref lacks may take any value. Otherwise it
// returns an error wrapping ErrLoosening that names the looser cap.
func (l LimitOrderSize) NoLooserThan(ref LimitOrderSize) error {
	if err := boundNoLooserThan(
		"max_quantity", l.MaxQuantity, ref.MaxQuantity, false,
	); err != nil {
		return err
	}
	return boundNoLooserThan(
		"max_notional", l.MaxNotional, ref.MaxNotional, false,
	)
}

// NoLooserThan returns nil when the Spot Funds P&L-bounds barrier is at least
// as strict as ref, the barrier it would replace or take over from: it is in
// ref's currency, since bounds in another currency are not comparable, it
// carries every bound ref carries, its lower bound is not below ref's and its
// upper bound is not above ref's. A bound ref lacks may take any value.
// Otherwise it returns an error wrapping ErrLoosening that names what loosens.
func (l LimitSpotFundsPnlBounds) NoLooserThan(ref LimitSpotFundsPnlBounds) error {
	if l.Currency != ref.Currency {
		return fmt.Errorf(
			"currency %s differs from the current %s: %w",
			l.Currency, ref.Currency, ErrLoosening,
		)
	}
	if err := boundNoLooserThan(
		"lower_bound", l.LowerBound, ref.LowerBound, true,
	); err != nil {
		return err
	}
	return boundNoLooserThan(
		"upper_bound", l.UpperBound, ref.UpperBound, false,
	)
}

// InAccountCurrency returns nil when the adjustment r adjusts currency, the
// own currency of the account r applies to: the one asset whose balances a
// tighten-only caller may change, as a lower balance of it is a withdrawal.
// Lowering any other asset, a position, moves the quantity the engine
// realizes P&L against: an absolute that does not end flat keeps the average
// entry price, so a later buy realizes a P&L that never happened; a lower
// held reserved by an accepted sell clears the average, so the venue fill
// opens a short whose loss is never realized; and lowering a position with no
// average dodges the MissingCostBasis halt. An empty currency, an account
// with none of its own, refuses every asset: a group's or the default
// currency does not count. Otherwise it returns an error wrapping ErrLoosening
// that says what an agent may adjust.
func (r AdjustmentRequest) InAccountCurrency(currency string) error {
	if currency == "" {
		return fmt.Errorf(
			"%s; the account has none: %w", agentMayAdjust(currency), ErrLoosening,
		)
	}
	if r.Asset != currency {
		return fmt.Errorf(
			"%s; asset %s is not it: %w",
			agentMayAdjust(currency), r.Asset, ErrLoosening,
		)
	}
	return nil
}

// InExistingAccountCurrency returns nil when the adjustment r adjusts
// currency, the account's own (InAccountCurrency), and exists reports that the
// asset of that code is stored: adjusting an asset that does not exist creates
// it, and a tighten-only caller may not. Otherwise it returns an error wrapping
// ErrLoosening that says what an agent may adjust.
func (r AdjustmentRequest) InExistingAccountCurrency(
	currency string, exists bool,
) error {
	if err := r.InAccountCurrency(currency); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf(
			"%s; no such asset exists, and an agent may not create one: %w",
			agentMayAdjust(currency), ErrLoosening,
		)
	}
	return nil
}

// agentMayAdjust says what a tighten-only caller may adjust in an account
// whose own currency is currency, empty when it has none: the text every
// refusal of the asset or the money its adjustment changes begins with.
func agentMayAdjust(currency string) string {
	own := "own currency"
	if currency != "" {
		own = "currency " + currency
	}
	return "an agent may only lower the balance, held or incoming of the " +
		"account's " + own
}

// NoLooserThan returns nil when the adjustment r, made by a tighten-only
// caller, only lowers the money of the account it applies to: currency is the
// account's own currency, which r must adjust (InAccountCurrency), and current
// the stored balance of it, a missing one being the zero balance. Each of
// balance (available), held and incoming that r names may only go down: a
// delta must not lie above zero, and an absolute must not lie above the
// field's current value, an empty one being zero. A lower held never raises
// spendable funds, which count held only while it is negative, and incoming is
// funds still to come. The average entry price and the realized P&L are the
// operator's books: r may name either only with its current value, compared
// by value, and an empty current one is no value, so any given one differs. A
// realized P&L is refused whatever its value while current's P&L calculation
// is halted, as it would restart it, and so is a halt r would set. A bound only
// constrains the result, so it may take any value. Every value is parsed with
// ParseEngineDecimal, so one it does not accept is an error, never a pass.
// Otherwise it returns an error wrapping ErrLoosening that says what an agent
// may change.
func (r AdjustmentRequest) NoLooserThan(currency string, current Balance) error {
	if err := r.InAccountCurrency(currency); err != nil {
		return err
	}
	lowerOnly := agentMayAdjust(currency)
	for _, field := range []struct {
		name    string
		amount  *AdjustmentAmount
		current string
	}{
		{"balance", r.Balance, current.Available},
		{"held", r.Held, current.Held},
		{"incoming", r.Incoming, current.Incoming},
	} {
		if field.amount == nil {
			continue
		}
		value, err := ParseEngineDecimal(field.amount.Value)
		if err != nil {
			return fmt.Errorf("%s: %w", field.name, err)
		}
		switch field.amount.Mode {
		case AdjustmentModeDelta:
			if value.IsPositive() {
				return fmt.Errorf(
					"%s; %s delta %s lies above zero: %w",
					lowerOnly, field.name, field.amount.Value, ErrLoosening,
				)
			}
		case AdjustmentModeAbsolute:
			now := field.current
			if now == "" {
				now = "0"
			}
			if err := boundNoLooserThan(
				field.name, field.amount.Value, now, false,
			); err != nil {
				return fmt.Errorf("%s; %w", lowerOnly, err)
			}
		default:
			return fmt.Errorf(
				"%s mode %q is neither %q nor %q: %w",
				field.name, field.amount.Mode, AdjustmentModeAbsolute,
				AdjustmentModeDelta, ErrInvalid,
			)
		}
	}
	if r.RealizedPnlHaltReason != "" {
		return fmt.Errorf(
			"an agent may not halt the position's P&L calculation: %w",
			ErrLoosening,
		)
	}
	if r.RealizedPnl != "" && current.RealizedPnlHaltReason != "" {
		return fmt.Errorf(
			"an agent may not set the realized P&L of a position whose P&L "+
				"calculation is halted for %s, as it would restart it: %w",
			current.RealizedPnlHaltReason, ErrLoosening,
		)
	}
	books := "an agent may not change the average entry price or the realized P&L"
	for _, field := range []struct{ name, value, current string }{
		{"average entry price", r.AverageEntryPrice, current.AverageEntryPrice},
		{"realized P&L", r.RealizedPnl, current.RealizedPnl},
	} {
		if field.value == "" {
			continue
		}
		if field.current == "" {
			return fmt.Errorf(
				"%s; %s %s given while the current is unset: %w",
				books, field.name, field.value, ErrLoosening,
			)
		}
		// A value neither above nor below the current one is the current one.
		for _, floor := range []bool{false, true} {
			if err := boundNoLooserThan(
				field.name, field.value, field.current, floor,
			); err != nil {
				return fmt.Errorf("%s; %w", books, err)
			}
		}
	}
	return nil
}

// TradingRuntimeIDPrefix begins the external id of every record the trading
// runtime books itself: a venue fee's adjustment,
// trading:<connection>:fee:<fee id>, and an execution report,
// trading:<client order id>:<suffix>. The namespace is reserved for the
// runtime (AdjustmentIDOutsideTradingRuntime).
const TradingRuntimeIDPrefix = "trading:"

// AdjustmentIDOutsideTradingRuntime returns nil when id, the external id a
// tighten-only caller gives its adjustment, does not begin with
// TradingRuntimeIDPrefix, as the zero id, one the store generates, does not.
// The runtime books a venue fee as an adjustment under such an id and takes an
// adjustment already stored under it for that fee booked: after an accepted
// one the fee is never debited, and after a rejected one it is skipped, so an
// adjustment taking the id first would leave the fee undebited. Otherwise it
// returns an error wrapping ErrLoosening that says the namespace is reserved
// for the trading runtime.
func AdjustmentIDOutsideTradingRuntime(id ExternalID) error {
	if strings.HasPrefix(id.String(), TradingRuntimeIDPrefix) {
		return fmt.Errorf(
			"adjustment id %q: an id starting with %q is reserved for the "+
				"trading runtime: %w",
			id, TradingRuntimeIDPrefix, ErrLoosening,
		)
	}
	return nil
}

// boundNoLooserThan compares one optional exact decimal bound, value, with the
// reference's, ref, by value. A bound ref lacks may take any value. A bound ref
// carries must be present and must not lie below ref when floor is set, or
// above it otherwise; else it returns an error wrapping ErrLoosening. Both are
// parsed with ParseEngineDecimal, so the comparison's work is bounded by their
// length, and a value it does not accept is an error, never a pass.
func boundNoLooserThan(field, value, ref string, floor bool) error {
	if ref == "" {
		return nil
	}
	if value == "" {
		return fmt.Errorf(
			"%s unset while the current is %s: %w", field, ref, ErrLoosening,
		)
	}
	v, err := ParseEngineDecimal(value)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	r, err := ParseEngineDecimal(ref)
	if err != nil {
		return fmt.Errorf("the current %s: %w", field, err)
	}
	switch {
	case floor && v.LessThan(r):
		return fmt.Errorf(
			"%s %s below the current %s: %w", field, value, ref, ErrLoosening,
		)
	case !floor && v.GreaterThan(r):
		return fmt.Errorf(
			"%s %s above the current %s: %w", field, value, ref, ErrLoosening,
		)
	}
	return nil
}
