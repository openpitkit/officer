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

package node

import (
	"context"
	"errors"
	"fmt"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

// ListLimits returns the barriers that reference account, or all barriers when
// account is empty.
func (n *localNode) ListLimits(
	ctx context.Context, account domain.AccountID,
) (AccountLimits, error) {
	limits, err := n.listLimits(ctx, account)
	if err != nil {
		return AccountLimits{}, fmt.Errorf("list limits: %w", err)
	}
	return limits, nil
}

// ListPolicyRows returns the node's typed barriers flattened into one sorted,
// paged policy list.
func (n *localNode) ListPolicyRows(
	ctx context.Context, filter store.PolicyListFilter,
) (store.PolicyListPage, error) {
	page, err := n.realm.ListPolicyRows(ctx, filter)
	if err != nil {
		return store.PolicyListPage{}, fmt.Errorf("list policy rows: %w", err)
	}
	return page, nil
}

// listLimits reads the typed barrier tables narrowed to account (empty
// returns every barrier) and bundles them into the read-side AccountLimits.
func (n *localNode) listLimits(
	ctx context.Context, account domain.AccountID,
) (AccountLimits, error) {
	rate, err := n.realm.ListRateLimits(ctx, account)
	if err != nil {
		return AccountLimits{}, fmt.Errorf("list rate limits: %w", err)
	}
	orderSize, err := n.realm.ListOrderSizeLimits(ctx, account)
	if err != nil {
		return AccountLimits{}, fmt.Errorf("list order-size limits: %w", err)
	}
	spotFundsPnlBounds, err := n.realm.ListSpotFundsPnlBoundsLimits(ctx, account)
	if err != nil {
		return AccountLimits{}, fmt.Errorf("list spot funds pnl-bounds limits: %w", err)
	}
	return AccountLimits{
		RateLimits:               rate,
		OrderSizeLimits:          orderSize,
		SpotFundsPnlBoundsLimits: spotFundsPnlBounds,
	}, nil
}

// PutRateLimit upserts the whole rate-limit barrier in the store, reconfigures
// the rate policy from the persisted full barrier set, reverts the store on
// engine-apply failure, and audits the action. It returns a replacement
// market-data sink only when the policy change had to rebuild the engine.
func (n *localNode) PutRateLimit(
	ctx context.Context, limit domain.LimitRate, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginEngineRestart(); err != nil {
		return nil, err
	}
	defer n.endEngineRestart()

	target := LimitTarget{
		Policy:  domain.PolicyRateLimit,
		Scope:   limit.Scope,
		Account: limit.Account,
		Asset:   limit.Asset,
	}
	prev, hadPrev, err := n.readRateBarrier(ctx, target)
	if err != nil {
		return nil, err
	}

	if err := n.ensureLimitAsset(
		ctx, limit.Scope, limit.Asset, "rate limit", caller,
	); err != nil {
		return nil, err
	}
	if err := n.realm.PutRateLimit(ctx, limit); err != nil {
		return nil, fmt.Errorf("put rate limit: %w", err)
	}

	sink, applyErr := n.applyPolicyChangeLocked(ctx, domain.PolicyRateLimit)
	if applyErr != nil {
		n.revertRateBarrier(ctx, target, prev, hadPrev)
		return nil, fmt.Errorf("configure policy after limit: %w", applyErr)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetLimit,
		Account: target.Account,
		Detail:  setRateLimitDetail(limit),
	}); err != nil {
		return sink, fmt.Errorf("audit set limit: %w", err)
	}
	return sink, nil
}

// PutOrderSizeLimit upserts the whole order-size barrier and reconfigures the
// order-size policy, mirroring PutRateLimit for the rate policy.
func (n *localNode) PutOrderSizeLimit(
	ctx context.Context, limit domain.LimitOrderSize, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginEngineRestart(); err != nil {
		return nil, err
	}
	defer n.endEngineRestart()

	target := LimitTarget{
		Policy:  domain.PolicyOrderSizeLimit,
		Scope:   limit.Scope,
		Account: limit.Account,
		Asset:   limit.Asset,
	}
	prev, hadPrev, err := n.readOrderSizeBarrier(ctx, target)
	if err != nil {
		return nil, err
	}

	if err := n.ensureLimitAsset(
		ctx, limit.Scope, limit.Asset, "order-size limit", caller,
	); err != nil {
		return nil, err
	}
	if err := n.realm.PutOrderSizeLimit(ctx, limit); err != nil {
		return nil, fmt.Errorf("put order-size limit: %w", err)
	}

	sink, applyErr := n.applyPolicyChangeLocked(ctx, domain.PolicyOrderSizeLimit)
	if applyErr != nil {
		n.revertOrderSizeBarrier(ctx, target, prev, hadPrev)
		return nil, fmt.Errorf("configure policy after limit: %w", applyErr)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetLimit,
		Account: target.Account,
		Detail:  setOrderSizeLimitDetail(limit),
	}); err != nil {
		return sink, fmt.Errorf("audit set limit: %w", err)
	}
	return sink, nil
}

// PutSpotFundsPnlBoundsLimit upserts the whole SpotFunds self-computed
// P&L-bounds barrier and reconfigures the SpotFunds policy.
func (n *localNode) PutSpotFundsPnlBoundsLimit(
	ctx context.Context, limit domain.LimitSpotFundsPnlBounds, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginEngineRestart(); err != nil {
		return nil, err
	}
	defer n.endEngineRestart()

	target := LimitTarget{
		Policy:          domain.PolicySpotFundsPnlBoundsKillSwitch,
		Scope:           limit.Scope,
		Account:         limit.Account,
		AccountGroup:    limit.AccountGroup,
		AccountCurrency: limit.AccountCurrency,
	}
	prev, hadPrev, err := n.readSpotFundsPnlBoundsBarrier(ctx, target)
	if err != nil {
		return nil, err
	}

	if _, err := n.ensureAutoCreatedAsset(
		ctx, limit.AccountCurrency, "spot funds pnl-bounds limit", caller,
	); err != nil {
		return nil, err
	}
	if err := n.realm.PutSpotFundsPnlBoundsLimit(ctx, limit); err != nil {
		return nil, fmt.Errorf("put spot funds pnl-bounds limit: %w", err)
	}

	sink, applyErr := n.applyPolicyChangeLocked(
		ctx, domain.PolicySpotFundsPnlBoundsKillSwitch,
	)
	if applyErr != nil {
		n.revertSpotFundsPnlBoundsBarrier(ctx, target, prev, hadPrev)
		return nil, fmt.Errorf("configure policy after limit: %w", applyErr)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetLimit,
		Account: target.Account,
		Detail:  setSpotFundsPnlBoundsLimitDetail(limit),
	}); err != nil {
		return sink, fmt.Errorf("audit set limit: %w", err)
	}
	return sink, nil
}

// DeleteLimit removes the barrier addressed by target from its typed table,
// reconfigures the named policy from the persisted full barrier set, reverts the
// store on engine-apply failure, and audits the action. It returns a replacement
// market-data sink only when the policy change had to rebuild the engine.
func (n *localNode) DeleteLimit(
	ctx context.Context, target LimitTarget, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginEngineRestart(); err != nil {
		return nil, err
	}
	defer n.endEngineRestart()

	revert, err := n.deleteBarrier(ctx, target)
	if err != nil {
		return nil, err
	}

	sink, applyErr := n.applyPolicyChangeLocked(ctx, target.Policy)
	if applyErr != nil {
		revert()
		return nil, fmt.Errorf("configure policy after delete limit: %w", applyErr)
	}

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionDeleteLimit,
		Account: target.Account,
		Detail:  deleteLimitDetail(target),
	}); err != nil {
		return sink, fmt.Errorf("audit delete limit: %w", err)
	}
	return sink, nil
}

// deleteBarrier reads the barrier currently stored at target (so a failed engine
// apply can be reverted), deletes it from its typed table, and returns a
// best-effort revert closure that re-puts the previous barrier when one existed.
func (n *localNode) deleteBarrier(
	ctx context.Context, target LimitTarget,
) (func(), error) {
	switch target.Policy {
	case domain.PolicyRateLimit:
		prev, hadPrev, err := n.readRateBarrier(ctx, target)
		if err != nil {
			return nil, err
		}
		if err := n.realm.DeleteRateLimit(ctx, target.Scope, target.Account, target.Asset); err != nil {
			return nil, fmt.Errorf("delete rate limit: %w", err)
		}
		return func() { n.revertRateBarrier(ctx, target, prev, hadPrev) }, nil
	case domain.PolicyOrderSizeLimit:
		prev, hadPrev, err := n.readOrderSizeBarrier(ctx, target)
		if err != nil {
			return nil, err
		}
		if err := n.realm.DeleteOrderSizeLimit(ctx, target.Scope, target.Account, target.Asset); err != nil {
			return nil, fmt.Errorf("delete order-size limit: %w", err)
		}
		return func() { n.revertOrderSizeBarrier(ctx, target, prev, hadPrev) }, nil
	case domain.PolicySpotFundsPnlBoundsKillSwitch:
		prev, hadPrev, err := n.readSpotFundsPnlBoundsBarrier(ctx, target)
		if err != nil {
			return nil, err
		}
		if err := n.realm.DeleteSpotFundsPnlBoundsLimit(
			ctx,
			target.Scope,
			target.Account,
			target.AccountGroup,
			target.AccountCurrency,
		); err != nil {
			return nil, fmt.Errorf("delete spot funds pnl-bounds limit: %w", err)
		}
		return func() {
			n.revertSpotFundsPnlBoundsBarrier(ctx, target, prev, hadPrev)
		}, nil
	default:
		return nil, fmt.Errorf("unknown policy %q: %w", target.Policy, domain.ErrInvalid)
	}
}

// applyPolicyChangeLocked applies a just-persisted barrier change for policy
// to the engine via the runtime Configure surface. If the SDK cannot express
// the change dynamically, the persisted snapshot becomes the source of truth and
// the engine is rebuilt. Callers must hold the exclusive restart gate.
func (n *localNode) applyPolicyChangeLocked(
	ctx context.Context, policy string,
) (marketdata.Sink, error) {
	if err := n.reconfigurePolicy(ctx, policy); err != nil {
		if !errors.Is(err, domain.ErrNotImplemented) {
			return nil, err
		}
		if err := n.rebuildEngineFromStore(ctx); err != nil {
			return nil, err
		}
		return n.currentMarketDataSink(), nil
	}
	return nil, nil
}

// reconfigurePolicy re-reads the full typed barrier set for policy from the
// store and applies it to the engine via the runtime Configure surface. It
// returns the engine error verbatim so the caller can decide whether to revert
// or rebuild.
func (n *localNode) reconfigurePolicy(ctx context.Context, policy string) error {
	limits, err := n.policyLimitSet(ctx, policy)
	if err != nil {
		return err
	}
	return n.engine.ConfigurePolicy(ctx, policy, limits)
}

// policyLimitSet reads the full barrier set for one policy from its typed table
// and packs it into the engine.LimitSet the runtime Configure surface consumes.
// Only the slice matching policy is populated; ConfigurePolicy ignores the rest.
func (n *localNode) policyLimitSet(ctx context.Context, policy string) (engine.LimitSet, error) {
	switch policy {
	case domain.PolicyRateLimit:
		limits, err := n.realm.ListRateLimits(ctx, "")
		if err != nil {
			return engine.LimitSet{}, fmt.Errorf("read rate limits: %w", err)
		}
		return engine.LimitSet{RateLimits: limits}, nil
	case domain.PolicyOrderSizeLimit:
		limits, err := n.realm.ListOrderSizeLimits(ctx, "")
		if err != nil {
			return engine.LimitSet{}, fmt.Errorf("read order-size limits: %w", err)
		}
		return engine.LimitSet{OrderSizeLimits: limits}, nil
	case domain.PolicySpotFundsPnlBoundsKillSwitch:
		limits, err := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
		if err != nil {
			return engine.LimitSet{}, fmt.Errorf(
				"read spot funds pnl-bounds limits: %w", err,
			)
		}
		return engine.LimitSet{SpotFundsPnlBoundsLimits: limits}, nil
	default:
		return engine.LimitSet{}, fmt.Errorf("unknown policy %q: %w", policy, domain.ErrInvalid)
	}
}

// readRateBarrier returns the rate-limit barrier currently stored at target so a
// failed engine apply can be reverted. The bool is false when none exists.
func (n *localNode) readRateBarrier(
	ctx context.Context, target LimitTarget,
) (domain.LimitRate, bool, error) {
	limits, err := n.realm.ListRateLimits(ctx, "")
	if err != nil {
		return domain.LimitRate{}, false, fmt.Errorf("read rate barrier: %w", err)
	}
	for _, limit := range limits {
		if limit.Scope == target.Scope && limit.Account == target.Account && limit.Asset == target.Asset {
			return limit, true, nil
		}
	}
	return domain.LimitRate{}, false, nil
}

func (n *localNode) ensureLimitAsset(
	ctx context.Context,
	scope string,
	asset string,
	operation string,
	caller domain.Caller,
) error {
	switch scope {
	case domain.ScopeAsset, domain.ScopeAccountAsset:
		_, err := n.ensureAutoCreatedAsset(ctx, asset, operation, caller)
		return err
	default:
		return nil
	}
}

// revertRateBarrier restores the rate-limit barrier at target to its previous
// state: re-put when it existed before, delete when it did not.
func (n *localNode) revertRateBarrier(
	ctx context.Context, target LimitTarget, prev domain.LimitRate, hadPrev bool,
) {
	if hadPrev {
		// Best-effort revert; the caller already surfaces the primary error.
		_ = n.realm.PutRateLimit(ctx, prev)
		return
	}
	// Best-effort revert; the caller already surfaces the primary error.
	_ = n.realm.DeleteRateLimit(ctx, target.Scope, target.Account, target.Asset)
}

// readOrderSizeBarrier mirrors readRateBarrier for the order-size table.
func (n *localNode) readOrderSizeBarrier(
	ctx context.Context, target LimitTarget,
) (domain.LimitOrderSize, bool, error) {
	limits, err := n.realm.ListOrderSizeLimits(ctx, "")
	if err != nil {
		return domain.LimitOrderSize{}, false, fmt.Errorf("read order-size barrier: %w", err)
	}
	for _, limit := range limits {
		if limit.Scope == target.Scope && limit.Account == target.Account && limit.Asset == target.Asset {
			return limit, true, nil
		}
	}
	return domain.LimitOrderSize{}, false, nil
}

// revertOrderSizeBarrier mirrors revertRateBarrier for the order-size table.
func (n *localNode) revertOrderSizeBarrier(
	ctx context.Context, target LimitTarget, prev domain.LimitOrderSize, hadPrev bool,
) {
	if hadPrev {
		_ = n.realm.PutOrderSizeLimit(ctx, prev)
		return
	}
	_ = n.realm.DeleteOrderSizeLimit(ctx, target.Scope, target.Account, target.Asset)
}

// readSpotFundsPnlBoundsBarrier mirrors readRateBarrier for the SpotFunds
// P&L-bounds table.
func (n *localNode) readSpotFundsPnlBoundsBarrier(
	ctx context.Context, target LimitTarget,
) (domain.LimitSpotFundsPnlBounds, bool, error) {
	limits, err := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		return domain.LimitSpotFundsPnlBounds{}, false,
			fmt.Errorf("read spot funds pnl-bounds barrier: %w", err)
	}
	for _, limit := range limits {
		if limit.Scope == target.Scope &&
			limit.Account == target.Account &&
			limit.AccountGroup == target.AccountGroup &&
			limit.AccountCurrency == target.AccountCurrency {
			return limit, true, nil
		}
	}
	return domain.LimitSpotFundsPnlBounds{}, false, nil
}

// revertSpotFundsPnlBoundsBarrier mirrors revertRateBarrier for the SpotFunds
// P&L-bounds table.
func (n *localNode) revertSpotFundsPnlBoundsBarrier(
	ctx context.Context,
	target LimitTarget,
	prev domain.LimitSpotFundsPnlBounds,
	hadPrev bool,
) {
	if hadPrev {
		_ = n.realm.PutSpotFundsPnlBoundsLimit(ctx, prev)
		return
	}
	_ = n.realm.DeleteSpotFundsPnlBoundsLimit(
		ctx,
		target.Scope,
		target.Account,
		target.AccountGroup,
		target.AccountCurrency,
	)
}

// ListAudit returns the most recent n audit rows, newest first.
func (n *localNode) ListAudit(
	ctx context.Context, count int,
) ([]domain.AuditRow, error) {
	rows, err := n.realm.ListAudit(ctx, count)
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	return rows, nil
}

// ListAuditFiltered returns the most recent count audit rows matching the
// filter, newest first.
func (n *localNode) ListAuditFiltered(
	ctx context.Context, filter domain.AuditFilter, count int,
) ([]domain.AuditRow, error) {
	rows, err := n.realm.ListAuditFiltered(ctx, filter, count)
	if err != nil {
		return nil, fmt.Errorf("list audit filtered: %w", err)
	}
	return rows, nil
}

// ListAuditRows returns audit rows matching filter.
func (n *localNode) ListAuditRows(
	ctx context.Context, filter store.AuditListFilter,
) (store.AuditListPage, error) {
	page, err := n.realm.ListAuditRows(ctx, filter)
	if err != nil {
		return store.AuditListPage{}, fmt.Errorf("list audit rows: %w", err)
	}
	return page, nil
}
