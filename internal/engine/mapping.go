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

package engine

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pkg/optional"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/pretrade/policies"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/internal/domain"
)

// rateLimitAxes maps a rate-limit barrier set onto the public Configure axes:
// an optional broker barrier and per-asset/account/account-asset slices. The
// asset/account/account-asset slices are always non-nil so a Configure call
// replaces each axis wholesale (an empty slice clears the axis); barriers are
// added and removed at runtime and a surviving key keeps its live counter. A nil
// broker leaves the broker barrier unchanged: the SDK's Configure surface has no
// way to clear a rate-limit broker barrier in isolation, so the caller
// (configureRateLimitLocked) rejects a broker-barrier drop as not-implemented
// before reaching this point.
func rateLimitAxes(limits []domain.Limit) (
	*policies.RateLimitBrokerBarrier,
	[]policies.RateLimitAssetBarrier,
	[]policies.RateLimitAccountBarrier,
	[]policies.RateLimitAccountAssetBarrier,
	error,
) {
	var broker *policies.RateLimitBrokerBarrier
	assets := []policies.RateLimitAssetBarrier{}
	accounts := []policies.RateLimitAccountBarrier{}
	accountAssets := []policies.RateLimitAccountAssetBarrier{}

	for _, limit := range limits {
		rate, err := rateLimitFromValues(limit.Values)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		switch limit.Target.Scope {
		case domain.ScopeBroker:
			broker = &policies.RateLimitBrokerBarrier{Limit: rate}
		case domain.ScopeAsset:
			asset, err := newAsset(limit.Target.Asset)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			assets = append(assets, policies.RateLimitAssetBarrier{
				Limit:           rate,
				SettlementAsset: asset,
			})
		case domain.ScopeAccount:
			account, err := newAccountID(limit.Target.Account)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			accounts = append(accounts, policies.RateLimitAccountBarrier{
				Limit:     rate,
				AccountID: account,
			})
		case domain.ScopeAccountAsset:
			account, err := newAccountID(limit.Target.Account)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			asset, err := newAsset(limit.Target.Asset)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			accountAssets = append(accountAssets, policies.RateLimitAccountAssetBarrier{
				Limit:           rate,
				AccountID:       account,
				SettlementAsset: asset,
			})
		default:
			return nil, nil, nil, nil, fmt.Errorf(
				"engine: rate_limit unsupported scope %q", limit.Target.Scope)
		}
	}
	return broker, assets, accounts, accountAssets, nil
}

// orderSizeAxes maps an order-size barrier set onto the public Configure axes:
// an optional broker barrier and per-asset/account-asset slices. The slices are
// always non-nil so a Configure call replaces each axis wholesale (an empty
// slice clears the axis). A nil broker leaves the broker barrier unchanged: the
// SDK's Configure surface has no way to clear an order-size broker barrier in
// isolation. The caller (configureOrderSizeLocked) therefore rejects a
// broker-barrier drop as not-implemented before reaching this point, so this
// mapping never has to clear a broker barrier. The at-least-one-barrier rule is
// likewise enforced by the caller, which rejects an empty barrier set as
// not-implemented.
func orderSizeAxes(limits []domain.Limit) (
	*policies.OrderSizeBrokerBarrier,
	[]policies.OrderSizeAssetBarrier,
	[]policies.OrderSizeAccountAssetBarrier,
	error,
) {
	var broker *policies.OrderSizeBrokerBarrier
	assets := []policies.OrderSizeAssetBarrier{}
	accountAssets := []policies.OrderSizeAccountAssetBarrier{}

	for _, limit := range limits {
		size, err := orderSizeFromValues(limit.Values)
		if err != nil {
			return nil, nil, nil, err
		}
		switch limit.Target.Scope {
		case domain.ScopeBroker:
			broker = &policies.OrderSizeBrokerBarrier{Limit: size}
		case domain.ScopeAsset:
			asset, err := newAsset(limit.Target.Asset)
			if err != nil {
				return nil, nil, nil, err
			}
			assets = append(assets, policies.OrderSizeAssetBarrier{
				Limit:           size,
				SettlementAsset: asset,
			})
		case domain.ScopeAccountAsset:
			account, err := newAccountID(limit.Target.Account)
			if err != nil {
				return nil, nil, nil, err
			}
			asset, err := newAsset(limit.Target.Asset)
			if err != nil {
				return nil, nil, nil, err
			}
			accountAssets = append(accountAssets, policies.OrderSizeAccountAssetBarrier{
				Limit:           size,
				AccountID:       account,
				SettlementAsset: asset,
			})
		default:
			return nil, nil, nil, fmt.Errorf(
				"engine: order_size_limit unsupported scope %q", limit.Target.Scope)
		}
	}
	return broker, assets, accountAssets, nil
}

// pnlBoundsAxes maps a P&L bounds barrier set onto the public Configure axes:
// broker and account-asset slices. Both slices are always non-nil so a
// Configure call replaces each axis wholesale (an empty slice clears the axis).
// The asset scope maps to a broker barrier carrying the settlement asset;
// account_asset maps to an account barrier-update.
//
// The account axis is the Update shape (PnlBoundsAccountAssetBarrierUpdate),
// which retunes bounds without touching the live accumulated P&L: the runtime
// configure path must not reset accumulators when only bounds change.
func pnlBoundsAxes(limits []domain.Limit) (
	[]policies.PnlBoundsBrokerBarrier,
	[]policies.PnlBoundsAccountAssetBarrierUpdate,
	error,
) {
	brokers := []policies.PnlBoundsBrokerBarrier{}
	accounts := []policies.PnlBoundsAccountAssetBarrierUpdate{}

	for _, limit := range limits {
		lower, upper, initial, err := pnlBoundsFromValues(limit.Values)
		if err != nil {
			return nil, nil, err
		}
		// The runtime Configure path applies the barrier-update shape, which
		// cannot reseed the live accumulated P&L. initial_pnl can only be honored
		// when the barrier is first created at the one-time engine build, so reject
		// it here rather than silently dropping it.
		if _, ok := initial.Get(); ok {
			return nil, nil, fmt.Errorf(
				"engine: pnl_bounds initial_pnl cannot be set on a runtime barrier "+
					"update; it is only applied when the barrier is first created: %w",
				domain.ErrInvalid)
		}
		switch limit.Target.Scope {
		case domain.ScopeAsset:
			asset, err := newAsset(limit.Target.Asset)
			if err != nil {
				return nil, nil, err
			}
			brokers = append(brokers, policies.PnlBoundsBrokerBarrier{
				SettlementAsset: asset,
				LowerBound:      lower,
				UpperBound:      upper,
			})
		case domain.ScopeAccountAsset:
			account, err := newAccountID(limit.Target.Account)
			if err != nil {
				return nil, nil, err
			}
			asset, err := newAsset(limit.Target.Asset)
			if err != nil {
				return nil, nil, err
			}
			accounts = append(accounts, policies.PnlBoundsAccountAssetBarrierUpdate{
				AccountID: account,
				Barrier: policies.PnlBoundsBrokerBarrier{
					SettlementAsset: asset,
					LowerBound:      lower,
					UpperBound:      upper,
				},
			})
		default:
			return nil, nil, fmt.Errorf(
				"engine: pnl_bounds_kill_switch unsupported scope %q", limit.Target.Scope)
		}
	}
	return brokers, accounts, nil
}

// rateLimitReady maps a rate-limit barrier set onto a ready builder. Each
// domain barrier carries both max_orders and window (domain validation
// guarantees it), distributed across the broker/asset/account/account-asset
// axes by scope.
func rateLimitReady(limits []domain.Limit) (*policies.RateLimitReadyBuilder, error) {
	builder := policies.BuildRateLimit()
	ready := builder.PolicyGroupID(0)

	var (
		brokers       []policies.RateLimitBrokerBarrier
		assets        []policies.RateLimitAssetBarrier
		accounts      []policies.RateLimitAccountBarrier
		accountAssets []policies.RateLimitAccountAssetBarrier
	)
	for _, limit := range limits {
		rate, err := rateLimitFromValues(limit.Values)
		if err != nil {
			return nil, err
		}
		switch limit.Target.Scope {
		case domain.ScopeBroker:
			brokers = append(brokers, policies.RateLimitBrokerBarrier{Limit: rate})
		case domain.ScopeAsset:
			asset, err := newAsset(limit.Target.Asset)
			if err != nil {
				return nil, err
			}
			assets = append(assets, policies.RateLimitAssetBarrier{
				Limit:           rate,
				SettlementAsset: asset,
			})
		case domain.ScopeAccount:
			account, err := newAccountID(limit.Target.Account)
			if err != nil {
				return nil, err
			}
			accounts = append(accounts, policies.RateLimitAccountBarrier{
				Limit:     rate,
				AccountID: account,
			})
		case domain.ScopeAccountAsset:
			account, err := newAccountID(limit.Target.Account)
			if err != nil {
				return nil, err
			}
			asset, err := newAsset(limit.Target.Asset)
			if err != nil {
				return nil, err
			}
			accountAssets = append(accountAssets, policies.RateLimitAccountAssetBarrier{
				Limit:           rate,
				AccountID:       account,
				SettlementAsset: asset,
			})
		default:
			return nil, fmt.Errorf("engine: rate_limit unsupported scope %q",
				limit.Target.Scope)
		}
	}

	if len(brokers) > 0 {
		ready = ready.BrokerBarrier(brokers[len(brokers)-1])
	}
	ready = ready.AssetBarriers(assets...).
		AccountBarriers(accounts...).
		AccountAssetBarriers(accountAssets...)
	return ready, nil
}

// orderSizeReady maps an order-size-limit barrier set onto a ready builder.
func orderSizeReady(limits []domain.Limit) (*policies.OrderSizeLimitReadyBuilder, error) {
	builder := policies.BuildOrderSizeLimit()
	ready := builder.PolicyGroupID(0)

	var (
		brokers       []policies.OrderSizeBrokerBarrier
		assets        []policies.OrderSizeAssetBarrier
		accountAssets []policies.OrderSizeAccountAssetBarrier
	)
	for _, limit := range limits {
		size, err := orderSizeFromValues(limit.Values)
		if err != nil {
			return nil, err
		}
		switch limit.Target.Scope {
		case domain.ScopeBroker:
			brokers = append(brokers, policies.OrderSizeBrokerBarrier{Limit: size})
		case domain.ScopeAsset:
			asset, err := newAsset(limit.Target.Asset)
			if err != nil {
				return nil, err
			}
			assets = append(assets, policies.OrderSizeAssetBarrier{
				Limit:           size,
				SettlementAsset: asset,
			})
		case domain.ScopeAccountAsset:
			account, err := newAccountID(limit.Target.Account)
			if err != nil {
				return nil, err
			}
			asset, err := newAsset(limit.Target.Asset)
			if err != nil {
				return nil, err
			}
			accountAssets = append(accountAssets, policies.OrderSizeAccountAssetBarrier{
				Limit:           size,
				AccountID:       account,
				SettlementAsset: asset,
			})
		default:
			return nil, fmt.Errorf("engine: order_size_limit unsupported scope %q",
				limit.Target.Scope)
		}
	}

	if len(brokers) > 0 {
		ready = ready.BrokerBarrier(brokers[len(brokers)-1])
	}
	ready = ready.AssetBarriers(assets...).AccountAssetBarriers(accountAssets...)
	return ready, nil
}

// pnlBoundsReady maps a P&L bounds barrier set onto a ready builder. The asset
// scope maps to a broker barrier carrying the settlement asset; account_asset
// maps to an account barrier. This is the construction path: an account barrier
// is created fresh, so its InitialPnl seed is honored here (from the optional
// initial_pnl value, else zero). Domain validation guarantees initial_pnl only
// reaches the account-asset scope; the runtime Configure path cannot reseed and
// rejects it instead.
func pnlBoundsReady(limits []domain.Limit) (*policies.PnlBoundsKillswitchReadyBuilder, error) {
	builder := policies.BuildPnlBoundsKillswitch()
	ready := builder.PolicyGroupID(0)

	var (
		brokers  []policies.PnlBoundsBrokerBarrier
		accounts []policies.PnlBoundsAccountAssetBarrier
	)
	for _, limit := range limits {
		lower, upper, initial, err := pnlBoundsFromValues(limit.Values)
		if err != nil {
			return nil, err
		}
		switch limit.Target.Scope {
		case domain.ScopeAsset:
			asset, err := newAsset(limit.Target.Asset)
			if err != nil {
				return nil, err
			}
			brokers = append(brokers, policies.PnlBoundsBrokerBarrier{
				SettlementAsset: asset,
				LowerBound:      lower,
				UpperBound:      upper,
			})
		case domain.ScopeAccountAsset:
			account, err := newAccountID(limit.Target.Account)
			if err != nil {
				return nil, err
			}
			asset, err := newAsset(limit.Target.Asset)
			if err != nil {
				return nil, err
			}
			initialPnl := param.NewPnlZero()
			if seed, ok := initial.Get(); ok {
				initialPnl = seed
			}
			accounts = append(accounts, policies.PnlBoundsAccountAssetBarrier{
				Barrier: policies.PnlBoundsBrokerBarrier{
					SettlementAsset: asset,
					LowerBound:      lower,
					UpperBound:      upper,
				},
				AccountID:  account,
				InitialPnl: initialPnl,
			})
		default:
			return nil, fmt.Errorf("engine: pnl_bounds_kill_switch unsupported scope %q",
				limit.Target.Scope)
		}
	}

	ready = ready.BrokerBarriers(brokers...).AccountBarriers(accounts...)
	return ready, nil
}

// rateLimitFromValues extracts the rate limit from a barrier's values.
func rateLimitFromValues(values []domain.LimitValue) (policies.RateLimit, error) {
	var (
		maxOrders uint64
		window    time.Duration
		hasMax    bool
		hasWindow bool
	)
	for _, v := range values {
		switch v.Kind {
		case domain.KindMaxOrders:
			n, err := parseMaxOrders(v.Value)
			if err != nil {
				return policies.RateLimit{}, err
			}
			maxOrders, hasMax = n, true
		case domain.KindWindow:
			d, err := time.ParseDuration(v.Value)
			if err != nil {
				return policies.RateLimit{}, fmt.Errorf(
					"engine: rate_limit window %q: %w", v.Value, err)
			}
			window, hasWindow = d, true
		default:
			return policies.RateLimit{}, fmt.Errorf(
				"engine: rate_limit unexpected kind %q", v.Kind)
		}
	}
	if !hasMax || !hasWindow {
		return policies.RateLimit{}, fmt.Errorf(
			"engine: rate_limit requires max_orders and window")
	}
	return policies.RateLimit{MaxOrders: uint(maxOrders), Window: window}, nil
}

// orderSizeFromValues extracts the order-size limit from a barrier's values.
//
// The binding's OrderSizeLimit always carries both a quantity and a notional
// cap and rejects an order when it exceeds either (a strict ">"). The domain
// barrier may omit one dimension ("at least one required"), meaning that
// dimension must not constrain. The boundary adapter maps an omitted dimension
// to the largest representable value so it never triggers a reject; this is a
// transport-only sentinel and never participates in any calculation.
func orderSizeFromValues(values []domain.LimitValue) (policies.OrderSizeLimit, error) {
	maxQty, err := param.NewQuantityFromInt64(math.MaxInt64)
	if err != nil {
		return policies.OrderSizeLimit{}, fmt.Errorf(
			"engine: order_size_limit unbounded quantity: %w", err)
	}
	maxNotional, err := param.NewVolumeFromInt64(math.MaxInt64)
	if err != nil {
		return policies.OrderSizeLimit{}, fmt.Errorf(
			"engine: order_size_limit unbounded notional: %w", err)
	}
	limit := policies.OrderSizeLimit{MaxQuantity: maxQty, MaxNotional: maxNotional}

	for _, v := range values {
		switch v.Kind {
		case domain.KindMaxQuantity:
			q, err := param.NewQuantityFromString(v.Value)
			if err != nil {
				return policies.OrderSizeLimit{}, fmt.Errorf(
					"engine: order_size_limit max_quantity %q: %w", v.Value, err)
			}
			limit.MaxQuantity = q
		case domain.KindMaxNotional:
			n, err := param.NewVolumeFromString(v.Value)
			if err != nil {
				return policies.OrderSizeLimit{}, fmt.Errorf(
					"engine: order_size_limit max_notional %q: %w", v.Value, err)
			}
			limit.MaxNotional = n
		default:
			return policies.OrderSizeLimit{}, fmt.Errorf(
				"engine: order_size_limit unexpected kind %q", v.Kind)
		}
	}
	return limit, nil
}

// pnlBoundsFromValues extracts the optional lower and upper P&L bounds plus the
// optional initial P&L seed from a barrier's values. initial_pnl is only honored
// by the construction path (account-asset barrier); the runtime-update path must
// reject it separately, since the binding's barrier-update shape cannot reseed
// the live accumulator.
func pnlBoundsFromValues(
	values []domain.LimitValue,
) (lower, upper, initial optional.Option[param.Pnl], err error) {
	lower = optional.None[param.Pnl]()
	upper = optional.None[param.Pnl]()
	initial = optional.None[param.Pnl]()
	for _, v := range values {
		switch v.Kind {
		case domain.KindLowerBound:
			p, perr := param.NewPnlFromString(v.Value)
			if perr != nil {
				return lower, upper, initial, fmt.Errorf(
					"engine: pnl_bounds lower_bound %q: %w", v.Value, perr)
			}
			lower = optional.Some(p)
		case domain.KindUpperBound:
			p, perr := param.NewPnlFromString(v.Value)
			if perr != nil {
				return lower, upper, initial, fmt.Errorf(
					"engine: pnl_bounds upper_bound %q: %w", v.Value, perr)
			}
			upper = optional.Some(p)
		case domain.KindInitialPnl:
			p, perr := param.NewPnlFromString(v.Value)
			if perr != nil {
				return lower, upper, initial, fmt.Errorf(
					"engine: pnl_bounds initial_pnl %q: %w", v.Value, perr)
			}
			initial = optional.Some(p)
		default:
			return lower, upper, initial, fmt.Errorf(
				"engine: pnl_bounds unexpected kind %q", v.Kind)
		}
	}
	return lower, upper, initial, nil
}

// parseMaxOrders parses an integer max_orders string into a uint64. Domain
// validation already bounds it to (0, 1e9].
func parseMaxOrders(s string) (uint64, error) {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("engine: rate_limit max_orders %q: %w", s, err)
	}
	return n, nil
}

// newAccountID parses a caller-supplied domain account id into a
// param.AccountID. A bad format is caller input, so it wraps domain.ErrInvalid
// (HTTP 400); the internal seeding paths use param.NewAccountIDFromString
// directly so a corrupt stored id stays an internal error.
func newAccountID(id domain.AccountID) (param.AccountID, error) {
	account, err := param.NewAccountIDFromString(id.String())
	if err != nil {
		return param.AccountID{}, fmt.Errorf("engine: account %q: %w: %w", id, err, domain.ErrInvalid)
	}
	return account, nil
}

// newAsset parses a caller-supplied asset code into a param.Asset. A bad format
// is caller input, so it wraps domain.ErrInvalid (HTTP 400); the internal
// seeding paths use param.NewAsset directly so a corrupt stored asset stays an
// internal error.
func newAsset(code string) (param.Asset, error) {
	asset, err := param.NewAsset(code)
	if err != nil {
		return param.Asset{}, fmt.Errorf("engine: asset %q: %w: %w", code, err, domain.ErrInvalid)
	}
	return asset, nil
}

// newAccountGroupID hashes a domain group id string into a param.AccountGroupID.
// The binding hashes with FNV-1a; the same string always yields the same id, so
// the store's group-id strings map deterministically onto the engine's groups.
func newAccountGroupID(id string) (param.AccountGroupID, error) {
	group, err := param.NewAccountGroupIDFromString(id)
	if err != nil {
		return param.AccountGroupID{}, fmt.Errorf("engine: group %q: %w", id, err)
	}
	return group, nil
}

// --- account adjustment translation ----------------------------------------

// accountAdjustmentFromRequest maps one domain adjustment request onto a single
// model.AccountAdjustment: a balance operation on req.Asset with optional
// average-entry-price, per-field absolute|delta amounts, and optional bounds.
func accountAdjustmentFromRequest(
	req domain.AdjustmentRequest,
) (model.AccountAdjustment, error) {
	asset, err := newAsset(req.Asset)
	if err != nil {
		return model.AccountAdjustment{}, err
	}

	balanceOp := model.AccountAdjustmentBalanceOperationValues{
		Asset: optional.Some(asset),
	}
	if req.AverageEntryPrice != "" {
		price, perr := param.NewPriceFromString(req.AverageEntryPrice)
		if perr != nil {
			return model.AccountAdjustment{}, fmt.Errorf(
				"engine: adjustment average_entry_price %q: %w: %w",
				req.AverageEntryPrice, perr, domain.ErrInvalid)
		}
		balanceOp.AverageEntryPrice = optional.Some(price)
	}

	amount, err := adjustmentAmountValues(req)
	if err != nil {
		return model.AccountAdjustment{}, err
	}

	values := model.AccountAdjustmentValues{
		BalanceOperation: optional.Some(
			model.NewAccountAdjustmentBalanceOperationFromValues(balanceOp)),
		Amount: optional.Some(model.NewAccountAdjustmentAmountFromValues(amount)),
	}
	if bounds, ok, berr := adjustmentBoundsValues(req); berr != nil {
		return model.AccountAdjustment{}, berr
	} else if ok {
		values.Bounds = optional.Some(model.NewAccountAdjustmentBoundsFromValues(bounds))
	}

	// NewAccountAdjustmentFromValues never errors (the discriminated operation
	// makes a balance/position conflict unrepresentable), but thread it anyway.
	adjustment, err := model.NewAccountAdjustmentFromValues(values)
	if err != nil {
		return model.AccountAdjustment{}, fmt.Errorf("engine: build account adjustment: %w", err)
	}
	return adjustment, nil
}

// adjustmentAmountValues maps the per-field balance/held/incoming amounts of a
// request onto the binding's amount values, each as an absolute or delta.
func adjustmentAmountValues(
	req domain.AdjustmentRequest,
) (model.AccountAdjustmentAmountValues, error) {
	var values model.AccountAdjustmentAmountValues
	if req.Balance != nil {
		amt, err := adjustmentAmountFrom(*req.Balance, "balance")
		if err != nil {
			return values, err
		}
		values.Balance = optional.Some(amt)
	}
	if req.Held != nil {
		amt, err := adjustmentAmountFrom(*req.Held, "held")
		if err != nil {
			return values, err
		}
		values.Held = optional.Some(amt)
	}
	if req.Incoming != nil {
		amt, err := adjustmentAmountFrom(*req.Incoming, "incoming")
		if err != nil {
			return values, err
		}
		values.Incoming = optional.Some(amt)
	}
	return values, nil
}

// adjustmentAmountFrom maps one domain amount field onto a param.AdjustmentAmount,
// honoring its absolute|delta mode.
func adjustmentAmountFrom(
	a domain.AdjustmentAmount, field string,
) (param.AdjustmentAmount, error) {
	size, err := param.NewPositionSizeFromString(a.Value)
	if err != nil {
		return param.AdjustmentAmount{}, fmt.Errorf(
			"engine: adjustment %s value %q: %w: %w", field, a.Value, err, domain.ErrInvalid)
	}
	switch a.Mode {
	case domain.AdjustmentModeAbsolute:
		return param.NewAbsoluteAdjustmentAmount(size), nil
	case domain.AdjustmentModeDelta:
		return param.NewDeltaAdjustmentAmount(size), nil
	default:
		return param.AdjustmentAmount{}, fmt.Errorf(
			"engine: adjustment %s unknown mode %q: %w", field, a.Mode, domain.ErrInvalid)
	}
}

// adjustmentBoundsValues maps the optional per-field bounds of a request onto
// the binding's bounds values. The bool is false when the request set no bound.
func adjustmentBoundsValues(
	req domain.AdjustmentRequest,
) (model.AccountAdjustmentBoundsValues, bool, error) {
	var values model.AccountAdjustmentBoundsValues
	hasBound := false
	set := func(b *domain.AdjustmentBounds, field string,
		lower, upper *optional.Option[param.PositionSize],
	) error {
		if b == nil {
			return nil
		}
		if b.Lower != "" {
			v, err := param.NewPositionSizeFromString(b.Lower)
			if err != nil {
				return fmt.Errorf(
					"engine: adjustment %s lower bound %q: %w: %w", field, b.Lower, err, domain.ErrInvalid)
			}
			*lower = optional.Some(v)
			hasBound = true
		}
		if b.Upper != "" {
			v, err := param.NewPositionSizeFromString(b.Upper)
			if err != nil {
				return fmt.Errorf(
					"engine: adjustment %s upper bound %q: %w: %w", field, b.Upper, err, domain.ErrInvalid)
			}
			*upper = optional.Some(v)
			hasBound = true
		}
		return nil
	}
	if err := set(req.BalanceBounds, "balance", &values.BalanceLower, &values.BalanceUpper); err != nil {
		return values, false, err
	}
	if err := set(req.HeldBounds, "held", &values.HeldLower, &values.HeldUpper); err != nil {
		return values, false, err
	}
	if err := set(req.IncomingBounds, "incoming", &values.IncomingLower, &values.IncomingUpper); err != nil {
		return values, false, err
	}
	return values, hasBound, nil
}

// outcomeAcceptedFromList extracts the accepted outcome for asset from a binding
// outcome list: the per-field delta and resulting absolute, including the
// settlement-asset realized P&L. A field absent in every outcome reports as the
// empty string (it was not adjusted). The spot-funds policy emits one outcome
// carrying the single adjusted asset's entry.
//
// Load-bearing invariant: the engine emits at most one account-adjustment
// outcome per asset. This function collapses per-asset entries by last-wins, and
// balanceOutcomesFromList emits one BalanceOutcome per outcome without merging,
// so both rely on that engine guarantee. Handling multiple outcomes for the same
// asset would require reworking the outcome model end to end (summing deltas and
// reconciling absolutes through the engine adapter and node persistence); that
// rework is intentionally deferred.
func outcomeAcceptedFromList(
	outcomes []accountadjustment.Outcome, asset string,
) domain.AdjustmentOutcomeAccepted {
	var result domain.AdjustmentOutcomeAccepted
	for _, outcome := range outcomes {
		entry := outcome.Entry
		if entry.Asset.String() != asset {
			continue
		}
		result = outcomeAcceptedFromEntry(entry)
	}
	return result
}

// balanceOutcomesFromList relies on the at-most-one-outcome-per-asset invariant
// documented on outcomeAcceptedFromList and performs no per-asset dedup.
func balanceOutcomesFromList(outcomes []accountadjustment.Outcome) []BalanceOutcome {
	result := make([]BalanceOutcome, 0, len(outcomes))
	for _, outcome := range outcomes {
		entry := outcome.Entry
		asset := entry.Asset.String()
		if asset == "" {
			continue
		}
		result = append(result, BalanceOutcome{
			Asset:   asset,
			Outcome: outcomeAcceptedFromEntry(entry),
		})
	}
	return result
}

func outcomeAcceptedFromEntry(
	entry accountadjustment.AccountOutcomeEntry,
) domain.AdjustmentOutcomeAccepted {
	var result domain.AdjustmentOutcomeAccepted
	if amt, ok := entry.Balance.Get(); ok {
		result.BalanceDelta = amt.Delta.String()
		result.BalanceResult = amt.Absolute.String()
	}
	if amt, ok := entry.Held.Get(); ok {
		result.HeldDelta = amt.Delta.String()
		result.HeldResult = amt.Absolute.String()
	}
	if amt, ok := entry.Incoming.Get(); ok {
		result.IncomingDelta = amt.Delta.String()
		result.IncomingResult = amt.Absolute.String()
	}
	if amt, ok := entry.RealizedPnl.Get(); ok {
		result.RealizedPnlDelta = amt.Delta.String()
		result.RealizedPnlResult = amt.Absolute.String()
	}
	return result
}

// outcomeRejectedFrom maps the first reject of a batch error onto the domain
// rejected outcome.
func outcomeRejectedFrom(batch reject.AccountAdjustmentBatchError) domain.AdjustmentOutcomeRejected {
	if len(batch.Rejects) == 0 {
		return domain.AdjustmentOutcomeRejected{
			Code:   rejectCodeName(0),
			Reason: "account adjustment rejected",
		}
	}
	return adjustmentRejectFrom(batch.Rejects[0])
}

// adjustmentRejectFrom maps one binding reject onto the domain rejected outcome.
func adjustmentRejectFrom(r reject.Reject) domain.AdjustmentOutcomeRejected {
	return domain.AdjustmentOutcomeRejected{
		Code:    rejectCodeName(r.Code),
		Scope:   rejectScopeName(r.Scope),
		Policy:  r.Policy,
		Reason:  sanitizeText(r.Reason),
		Details: sanitizeText(r.Details),
	}
}

// --- order translation ------------------------------------------------------

// orderModelFrom maps a domain order onto a model.Order operation view:
// instrument (base/quote), account, side, trade amount (quantity or volume), and
// the optional limit price (omitted for market orders).
func orderModelFrom(o domain.Order) (model.Order, error) {
	base, err := newAsset(o.BaseAsset)
	if err != nil {
		return model.Order{}, err
	}
	quote, err := newAsset(o.QuoteAsset)
	if err != nil {
		return model.Order{}, err
	}
	account, err := newAccountID(o.Account)
	if err != nil {
		return model.Order{}, err
	}
	side, err := orderSide(o.Side)
	if err != nil {
		return model.Order{}, err
	}
	amount, err := tradeAmountFrom(o.AmountKind, o.AmountValue)
	if err != nil {
		return model.Order{}, err
	}

	order := model.NewOrder()
	view := order.EnsureOperationView()
	view.SetInstrument(param.NewInstrument(base, quote))
	view.SetAccountID(account)
	view.SetSide(side)
	view.SetTradeAmount(amount)
	if o.Price != "" {
		price, perr := param.NewPriceFromString(o.Price)
		if perr != nil {
			return model.Order{}, fmt.Errorf(
				"engine: order price %q: %w: %w", o.Price, perr, domain.ErrInvalid)
		}
		view.SetPrice(price)
	}
	return order, nil
}

// orderSide maps a domain order side onto a param.Side.
func orderSide(side domain.OrderSide) (param.Side, error) {
	switch side {
	case domain.OrderSideBuy:
		return param.SideBuy, nil
	case domain.OrderSideSell:
		return param.SideSell, nil
	default:
		return 0, fmt.Errorf("engine: unknown order side %q: %w", side, domain.ErrInvalid)
	}
}

// tradeAmountFrom maps a domain amount kind and value onto a param.TradeAmount.
func tradeAmountFrom(kind domain.OrderAmountKind, value string) (param.TradeAmount, error) {
	switch kind {
	case domain.OrderAmountKindQuantity:
		q, err := param.NewQuantityFromString(value)
		if err != nil {
			return param.TradeAmount{}, fmt.Errorf(
				"engine: order quantity %q: %w: %w", value, err, domain.ErrInvalid)
		}
		return param.NewQuantityTradeAmount(q), nil
	case domain.OrderAmountKindVolume:
		v, err := param.NewVolumeFromString(value)
		if err != nil {
			return param.TradeAmount{}, fmt.Errorf(
				"engine: order volume %q: %w: %w", value, err, domain.ErrInvalid)
		}
		return param.NewVolumeTradeAmount(v), nil
	default:
		return param.TradeAmount{}, fmt.Errorf(
			"engine: unknown order amount kind %q: %w", kind, domain.ErrInvalid)
	}
}

// orderRejectsFrom maps a binding reject slice onto domain order rejects.
func orderRejectsFrom(rejects []reject.Reject) []domain.OrderReject {
	out := make([]domain.OrderReject, 0, len(rejects))
	for _, r := range rejects {
		out = append(out, domain.OrderReject{
			Code:    rejectCodeName(r.Code),
			Scope:   rejectScopeName(r.Scope),
			Policy:  r.Policy,
			Reason:  sanitizeText(r.Reason),
			Details: sanitizeText(r.Details),
		})
	}
	return out
}

// --- execution report translation -------------------------------------------

// executionReportFrom maps a domain execution-report input onto a
// model.ExecutionReport: the operation (instrument/account/side) and the fill
// (last trade price+quantity, and the lock reconstructed from the single
// reference price when present).
func executionReportFrom(in domain.ExecutionReportInput) (model.ExecutionReport, error) {
	base, err := newAsset(in.BaseAsset)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	quote, err := newAsset(in.QuoteAsset)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	account, err := newAccountID(in.Account)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	side, err := orderSide(in.Side)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	price, err := param.NewPriceFromString(in.FillPrice)
	if err != nil {
		return model.ExecutionReport{}, fmt.Errorf(
			"engine: fill price %q: %w: %w", in.FillPrice, err, domain.ErrInvalid)
	}
	quantity, err := param.NewQuantityFromString(in.FillQuantity)
	if err != nil {
		return model.ExecutionReport{}, fmt.Errorf(
			"engine: fill quantity %q: %w: %w", in.FillQuantity, err, domain.ErrInvalid)
	}

	lockBytes, err := lockBytesFromPrice(in.LockPrice)
	if err != nil {
		return model.ExecutionReport{}, err
	}

	report := model.NewExecutionReport()
	op := report.EnsureOperationView()
	op.SetInstrument(param.NewInstrument(base, quote))
	op.SetAccountID(account)
	op.SetSide(side)

	fill := report.EnsureFillView()
	fill.SetLastTrade(model.NewExecutionReportTrade(price, quantity))
	if lockBytes != nil {
		fill.SetLock(lockBytes)
	}
	return report, nil
}

// immediateExecutionReport builds the synthetic fill that settles an immediate
// order at the captured settlement lock price. The fill quantity is the order's
// base quantity so the reservation's held amount nets to zero: a quantity order
// fills its quantity directly; a volume order's base quantity is the volume
// divided by the settlement price. The fill price and the lock are both the
// settlement price, so the post-trade settlement realizes exactly what the
// reservation held. Settle is final (the immediate fill closes the order).
func immediateExecutionReport(o domain.Order, settlementPrice string) (model.ExecutionReport, error) {
	quantity, err := immediateFillQuantity(o, settlementPrice)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	return executionReportFrom(domain.ExecutionReportInput{
		BaseAsset:    o.BaseAsset,
		QuoteAsset:   o.QuoteAsset,
		FillQuantity: quantity,
		FillPrice:    settlementPrice,
		LockPrice:    settlementPrice,
		Account:      o.Account,
		Side:         o.Side,
		OrderID:      o.ID,
		Final:        true,
	})
}

// immediateFillQuantity resolves the base quantity an immediate fill settles.
// For a quantity order it is the order amount; for a volume order it is the
// volume converted to base quantity at the settlement price (Volume divided by
// price). An empty settlement price on a volume order is an error: a fill cannot
// be sized without a price.
func immediateFillQuantity(o domain.Order, settlementPrice string) (string, error) {
	switch o.AmountKind {
	case domain.OrderAmountKindQuantity:
		return o.AmountValue, nil
	case domain.OrderAmountKindVolume:
		if settlementPrice == "" {
			return "", fmt.Errorf(
				"engine: cannot size volume fill without a settlement price: %w",
				domain.ErrInvalid)
		}
		volume, err := param.NewVolumeFromString(o.AmountValue)
		if err != nil {
			return "", fmt.Errorf(
				"engine: order volume %q: %w: %w", o.AmountValue, err, domain.ErrInvalid)
		}
		price, err := param.NewPriceFromString(settlementPrice)
		if err != nil {
			return "", fmt.Errorf(
				"engine: settlement price %q: %w: %w", settlementPrice, err, domain.ErrInvalid)
		}
		quantity, err := volume.CalculateQuantity(price)
		if err != nil {
			return "", fmt.Errorf("engine: size volume fill: %w", err)
		}
		return quantity.String(), nil
	default:
		return "", fmt.Errorf(
			"engine: unknown order amount kind %q: %w", o.AmountKind, domain.ErrInvalid)
	}
}

// lockBytesFromPrice reconstructs a default-group pre-trade lock carrying one
// reference price, returning its raw bytes for an execution-report fill. An
// empty price yields a nil slice (no lock attached).
func lockBytesFromPrice(lockPrice string) ([]byte, error) {
	if lockPrice == "" {
		return nil, nil
	}
	price, err := param.NewPriceFromString(lockPrice)
	if err != nil {
		return nil, fmt.Errorf("engine: lock price %q: %w: %w", lockPrice, err, domain.ErrInvalid)
	}
	lock, err := pretrade.NewLockFromEntries([]pretrade.Entry{
		{PolicyGroupID: model.DefaultPolicyGroupID, Price: price},
	})
	if err != nil {
		return nil, fmt.Errorf("engine: build fill lock: %w", err)
	}
	return lock.Bytes(), nil
}

// executionBlocksFrom maps the engine-recorded account blocks of a post-trade
// result onto domain blocks, stamping each with account (the binding's block
// record does not name the account).
func executionBlocksFrom(
	blocks []reject.AccountBlock, account domain.AccountID,
) []domain.ExecutionAccountBlock {
	out := make([]domain.ExecutionAccountBlock, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, domain.ExecutionAccountBlock{
			Account: account,
			Code:    rejectCodeName(b.Code),
			Reason:  sanitizeText(b.Reason),
			Details: sanitizeText(b.Details),
		})
	}
	return out
}

// accountBlockFrom maps a dry-run report's would-be account block onto a domain
// block, stamping it with account (the binding's block record does not name the
// account). It is the single-block counterpart of executionBlocksFrom, used by
// the pre-trade dry-run.
//
// The new core signals a would-be account block in two ways, and this maps both
// onto the single WouldBlock field:
//   - block (report.AccountBlock()) is non-nil when an account-scope reject
//     would have latched a fresh block during this dry-run (e.g. a kill-switch
//     policy tripping on the probe); it is preferred.
//   - when no fresh block would latch but the report carries an account-scope
//     reject (the probe account is already kill-switched, so the core re-emits
//     the standing block as an account-scope reject rather than latching a new
//     one), the would-block is derived from that reject.
//
// Returns nil when neither signal is present. This is faithful delegation: it
// reads only what the core emitted and adds no officer-side validation.
func accountBlockFrom(
	block *reject.AccountBlock, rejects []reject.Reject, account domain.AccountID,
) *domain.ExecutionAccountBlock {
	if block != nil {
		return &domain.ExecutionAccountBlock{
			Account: account,
			Code:    rejectCodeName(block.Code),
			Reason:  sanitizeText(block.Reason),
			Details: sanitizeText(block.Details),
		}
	}
	for _, r := range rejects {
		if r.Scope != reject.ScopeAccount {
			continue
		}
		return &domain.ExecutionAccountBlock{
			Account: account,
			Code:    rejectCodeName(r.Code),
			Reason:  sanitizeText(r.Reason),
			Details: sanitizeText(r.Details),
		}
	}
	return nil
}

// --- reject code/scope naming -----------------------------------------------

// sanitizeText coerces a binding-supplied reject/block string to valid,
// printable UTF-8: invalid byte sequences are dropped and control characters
// (including the C1 range) are stripped, preserving normal text and spaces. It
// is applied defensively at the officer boundary to every reject/block
// Reason/Details the engine surfaces.
//
// Root cause is upstream: the SDK Go binding returns garbage (non-UTF-8) reason/
// details for an account-blocked (Engine-scope) dry-run reject, so the control
// plane scrubs them here rather than render mojibake. An empty result is fine
// when the field was entirely garbage.
func sanitizeText(s string) string {
	if s == "" {
		return ""
	}
	valid := strings.ToValidUTF8(s, "")
	cleaned := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, valid))
	if looksLikeShortGarbledText(cleaned) {
		return ""
	}
	return cleaned
}

func looksLikeShortGarbledText(s string) bool {
	if s == "" {
		return false
	}
	allDigits := true
	count := 0
	for _, r := range s {
		count++
		if unicode.IsLetter(r) {
			return false
		}
		if !unicode.IsDigit(r) {
			allDigits = false
		}
	}
	return count <= 4 && !allDigits
}

// rejectCodeName maps a binding reject code onto a stable lower-snake string for
// the control plane's persisted records. Unknown codes fall back to a numeric
// "code_<n>" form so a new SDK code never loses information.
func rejectCodeName(code reject.Code) string {
	if name, ok := rejectCodeNames[code]; ok {
		return name
	}
	return fmt.Sprintf("code_%d", uint32(code))
}

// rejectScopeName maps a binding reject scope onto a stable string.
func rejectScopeName(scope reject.Scope) string {
	switch scope {
	case reject.ScopeOrder:
		return "order"
	case reject.ScopeAccount:
		return "account"
	default:
		return fmt.Sprintf("scope_%d", uint8(scope))
	}
}

// rejectCodeNames is the stable name table for the binding's predefined reject
// codes. It is the persisted vocabulary, decoupled from the numeric enum values
// so a code reorder in the SDK cannot silently change stored strings.
var rejectCodeNames = map[reject.Code]string{
	reject.CodeMissingRequiredField:            "missing_required_field",
	reject.CodeInvalidFieldFormat:              "invalid_field_format",
	reject.CodeInvalidFieldValue:               "invalid_field_value",
	reject.CodeUnsupportedOrderType:            "unsupported_order_type",
	reject.CodeUnsupportedTimeInForce:          "unsupported_time_in_force",
	reject.CodeUnsupportedOrderAttribute:       "unsupported_order_attribute",
	reject.CodeDuplicateClientOrderID:          "duplicate_client_order_id",
	reject.CodeTooLateToEnter:                  "too_late_to_enter",
	reject.CodeExchangeClosed:                  "exchange_closed",
	reject.CodeUnknownInstrument:               "unknown_instrument",
	reject.CodeUnknownAccount:                  "unknown_account",
	reject.CodeUnknownVenue:                    "unknown_venue",
	reject.CodeUnknownClearingAccount:          "unknown_clearing_account",
	reject.CodeUnknownCollateralAsset:          "unknown_collateral_asset",
	reject.CodeInsufficientFunds:               "insufficient_funds",
	reject.CodeInsufficientMargin:              "insufficient_margin",
	reject.CodeInsufficientPosition:            "insufficient_position",
	reject.CodeCreditLimitExceeded:             "credit_limit_exceeded",
	reject.CodeRiskLimitExceeded:               "risk_limit_exceeded",
	reject.CodeOrderExceedsLimit:               "order_exceeds_limit",
	reject.CodeOrderQtyExceedsLimit:            "order_qty_exceeds_limit",
	reject.CodeOrderNotionalExceedsLimit:       "order_notional_exceeds_limit",
	reject.CodePositionLimitExceeded:           "position_limit_exceeded",
	reject.CodeConcentrationLimitExceeded:      "concentration_limit_exceeded",
	reject.CodeLeverageLimitExceeded:           "leverage_limit_exceeded",
	reject.CodeRateLimitExceeded:               "rate_limit_exceeded",
	reject.CodePnlKillSwitchTriggered:          "pnl_kill_switch_triggered",
	reject.CodeAccountBlocked:                  "account_blocked",
	reject.CodeAccountNotAuthorized:            "account_not_authorized",
	reject.CodeComplianceRestriction:           "compliance_restriction",
	reject.CodeInstrumentRestricted:            "instrument_restricted",
	reject.CodeJurisdictionRestriction:         "jurisdiction_restriction",
	reject.CodeWashTradePrevention:             "wash_trade_prevention",
	reject.CodeSelfMatchPrevention:             "self_match_prevention",
	reject.CodeShortSaleRestriction:            "short_sale_restriction",
	reject.CodeRiskConfigurationMissing:        "risk_configuration_missing",
	reject.CodeReferenceDataUnavailable:        "reference_data_unavailable",
	reject.CodeOrderValueCalculationFailed:     "order_value_calculation_failed",
	reject.CodeSystemUnavailable:               "system_unavailable",
	reject.CodeMarkPriceUnavailable:            "mark_price_unavailable",
	reject.CodeAccountAdjustmentBoundsExceeded: "account_adjustment_bounds_exceeded",
	reject.CodeCustom:                          "custom",
	reject.CodeOther:                           "other",
}
