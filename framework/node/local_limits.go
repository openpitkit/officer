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
//
// missing decides what happens when an account-carrying scope names an account
// Officer does not know yet.
func (n *localNode) PutRateLimit(
	ctx context.Context, limit domain.LimitRate,
	missing domain.MissingAccountPolicy, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginLivePolicyConfiguration(); err != nil {
		return nil, err
	}
	defer n.endLivePolicyConfiguration()

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
	if err := n.ensureLimitAccount(
		ctx, limit.Scope, limit.Account, missing, "put rate limit", caller,
	); err != nil {
		return nil, err
	}
	if err := n.ensureLimitAsset(
		ctx, limit.Scope, limit.Asset, "rate limit", caller,
	); err != nil {
		return nil, err
	}

	snapshot, _, err := n.loadSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("load rate policy lifecycle snapshot: %w", err)
	}
	var sink marketdata.Sink
	if !hadPrev && len(snapshot.RateLimits) == 0 {
		snapshot.RateLimits = []domain.LimitRate{limit}
		durableCtx := context.WithoutCancel(ctx)
		sink, err = n.replaceEngineForPolicyLifecycle(
			ctx, domain.PolicyRateLimit, snapshot, func() error {
				return n.realm.PutRateLimit(durableCtx, limit)
			},
		)
		if err != nil {
			return sink, fmt.Errorf("install first rate limit: %w", err)
		}
	} else {
		if err := n.realm.PutRateLimit(ctx, limit); err != nil {
			return nil, fmt.Errorf("put rate limit: %w", err)
		}
		sink, err = n.applyPolicyChangeLocked(ctx, domain.PolicyRateLimit, func() error {
			return n.revertRateBarrier(context.WithoutCancel(ctx), target, prev, hadPrev)
		})
		if err != nil {
			return sink, fmt.Errorf("configure policy after limit: %w", err)
		}
	}
	ctx = context.WithoutCancel(ctx)
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetLimit,
		Account: target.Account,
		Detail:  setRateLimitDetail(limit),
	}); err != nil {
		return sink, n.fatalPostEngineAuditByCode(
			"audit rate limit", "policy", domain.PolicyRateLimit,
			fmt.Errorf("audit set limit: %w", err),
		)
	}
	return sink, nil
}

// PutOrderSizeLimit upserts the whole order-size barrier and reconfigures the
// order-size policy, mirroring PutRateLimit for the rate policy, including its
// handling of missing.
func (n *localNode) PutOrderSizeLimit(
	ctx context.Context, limit domain.LimitOrderSize,
	missing domain.MissingAccountPolicy, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginLivePolicyConfiguration(); err != nil {
		return nil, err
	}
	defer n.endLivePolicyConfiguration()

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
	if err := n.ensureLimitAccount(
		ctx, limit.Scope, limit.Account, missing, "put order-size limit", caller,
	); err != nil {
		return nil, err
	}
	if err := n.ensureLimitAsset(
		ctx, limit.Scope, limit.Asset, "order-size limit", caller,
	); err != nil {
		return nil, err
	}

	snapshot, _, err := n.loadSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("load order-size policy lifecycle snapshot: %w", err)
	}
	var sink marketdata.Sink
	if !hadPrev && len(snapshot.OrderSizeLimits) == 0 {
		snapshot.OrderSizeLimits = []domain.LimitOrderSize{limit}
		durableCtx := context.WithoutCancel(ctx)
		sink, err = n.replaceEngineForPolicyLifecycle(
			ctx, domain.PolicyOrderSizeLimit, snapshot, func() error {
				return n.realm.PutOrderSizeLimit(durableCtx, limit)
			},
		)
		if err != nil {
			return sink, fmt.Errorf("install first order-size limit: %w", err)
		}
	} else {
		if err := n.realm.PutOrderSizeLimit(ctx, limit); err != nil {
			return nil, fmt.Errorf("put order-size limit: %w", err)
		}
		sink, err = n.applyPolicyChangeLocked(
			ctx, domain.PolicyOrderSizeLimit, func() error {
				return n.revertOrderSizeBarrier(
					context.WithoutCancel(ctx), target, prev, hadPrev,
				)
			},
		)
		if err != nil {
			return sink, fmt.Errorf("configure policy after limit: %w", err)
		}
	}
	ctx = context.WithoutCancel(ctx)
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetLimit,
		Account: target.Account,
		Detail:  setOrderSizeLimitDetail(limit),
	}); err != nil {
		return sink, n.fatalPostEngineAuditByCode(
			"audit order-size limit", "policy", domain.PolicyOrderSizeLimit,
			fmt.Errorf("audit set limit: %w", err),
		)
	}
	return sink, nil
}

// PutSpotFundsPnlBoundsLimit upserts the whole SpotFunds self-computed
// P&L-bounds barrier and reconfigures the SpotFunds policy. missing is handled
// as in PutRateLimit; only the account scope carries an account axis here.
func (n *localNode) PutSpotFundsPnlBoundsLimit(
	ctx context.Context, limit domain.LimitSpotFundsPnlBounds,
	missing domain.MissingAccountPolicy, caller domain.Caller,
) (marketdata.Sink, error) {
	if err := n.beginLivePolicyConfiguration(); err != nil {
		return nil, err
	}
	defer n.endLivePolicyConfiguration()

	target := LimitTarget{
		Policy:       domain.PolicySpotFundsPnlBoundsKillSwitch,
		Scope:        limit.Scope,
		Account:      limit.Account,
		AccountGroup: limit.AccountGroup,
	}
	prev, hadPrev, err := n.readSpotFundsPnlBoundsBarrier(ctx, target)
	if err != nil {
		return nil, err
	}
	if err := n.ensureLimitAccount(
		ctx, limit.Scope, limit.Account, missing,
		"put spot-funds P&L bounds limit", caller,
	); err != nil {
		return nil, err
	}

	if err := n.realm.PutSpotFundsPnlBoundsLimit(ctx, limit); err != nil {
		return nil, fmt.Errorf("put spot funds pnl-bounds limit: %w", err)
	}

	sink, applyErr := n.applyPolicyChangeLocked(
		ctx, domain.PolicySpotFundsPnlBoundsKillSwitch, func() error {
			return n.revertSpotFundsPnlBoundsBarrier(
				context.WithoutCancel(ctx), target, prev, hadPrev,
			)
		},
	)
	if applyErr != nil {
		return sink, fmt.Errorf("configure policy after limit: %w", applyErr)
	}
	ctx = context.WithoutCancel(ctx)

	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionSetLimit,
		Account: target.Account,
		Detail:  setSpotFundsPnlBoundsLimitDetail(limit),
	}); err != nil {
		return sink, n.fatalPostEngineAuditByCode(
			"audit spot funds pnl-bounds limit", "policy",
			domain.PolicySpotFundsPnlBoundsKillSwitch,
			fmt.Errorf("audit set limit: %w", err),
		)
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
	if err := n.beginLivePolicyConfiguration(); err != nil {
		return nil, err
	}
	defer n.endLivePolicyConfiguration()

	sink, prepared, err := n.deleteLastPolicyBarrierPrepared(ctx, target)
	if err != nil {
		return sink, fmt.Errorf("delete last policy barrier: %w", err)
	}
	if !prepared {
		revert, err := n.deleteBarrier(ctx, target)
		if err != nil {
			return nil, err
		}
		sink, err = n.applyPolicyChangeLocked(ctx, target.Policy, revert)
		if err != nil {
			return sink, fmt.Errorf("configure policy after delete limit: %w", err)
		}
	}
	ctx = context.WithoutCancel(ctx)
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionDeleteLimit,
		Account: target.Account,
		Detail:  deleteLimitDetail(target),
	}); err != nil {
		return sink, n.fatalPostEngineAuditByCode(
			"audit delete limit", "policy", target.Policy,
			fmt.Errorf("audit delete limit: %w", err),
		)
	}
	return sink, nil
}

// deleteBarrier reads the barrier currently stored at target (so a failed engine
// apply can be reverted), deletes it from its typed table, and returns a
// best-effort revert closure that re-puts the previous barrier when one existed.
func (n *localNode) deleteBarrier(
	ctx context.Context, target LimitTarget,
) (func() error, error) {
	switch target.Policy {
	case domain.PolicyRateLimit:
		prev, hadPrev, err := n.readRateBarrier(ctx, target)
		if err != nil {
			return nil, err
		}
		if err := n.realm.DeleteRateLimit(ctx, target.Scope, target.Account, target.Asset); err != nil {
			return nil, fmt.Errorf("delete rate limit: %w", err)
		}
		return func() error {
			return n.revertRateBarrier(context.WithoutCancel(ctx), target, prev, hadPrev)
		}, nil
	case domain.PolicyOrderSizeLimit:
		prev, hadPrev, err := n.readOrderSizeBarrier(ctx, target)
		if err != nil {
			return nil, err
		}
		if err := n.realm.DeleteOrderSizeLimit(ctx, target.Scope, target.Account, target.Asset); err != nil {
			return nil, fmt.Errorf("delete order-size limit: %w", err)
		}
		return func() error {
			return n.revertOrderSizeBarrier(
				context.WithoutCancel(ctx), target, prev, hadPrev,
			)
		}, nil
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
		); err != nil {
			return nil, fmt.Errorf("delete spot funds pnl-bounds limit: %w", err)
		}
		return func() error {
			return n.revertSpotFundsPnlBoundsBarrier(
				context.WithoutCancel(ctx), target, prev, hadPrev,
			)
		}, nil
	default:
		return nil, fmt.Errorf("unknown policy %q: %w", target.Policy, domain.ErrInvalid)
	}
}

// replaceEngineForPolicyLifecycle builds the policy's next lifecycle shape and
// commits the durable barrier change as part of the engine transition.
func (n *localNode) replaceEngineForPolicyLifecycle(
	ctx context.Context,
	policy string,
	snapshot engine.Snapshot,
	commit func() error,
) (marketdata.Sink, error) {
	previousEngine := n.currentEngine()
	next, err := n.build(snapshot)
	if err != nil {
		return nil, fmt.Errorf("build engine for %s lifecycle: %w", policy, err)
	}
	if next == nil {
		return nil, fmt.Errorf("build engine for %s lifecycle returned nil", policy)
	}
	if next == previousEngine {
		return nil, fmt.Errorf("build engine for %s lifecycle returned current engine", policy)
	}
	transition, err := n.beginMarketDataTransition(next)
	if err != nil {
		next.Stop()
		return nil, fmt.Errorf("prepare %s lifecycle market data: %w", policy, err)
	}
	if err := n.replayMarketDataInto(ctx, next); err != nil {
		n.cancelMarketDataTransition(transition)
		next.Stop()
		return nil, fmt.Errorf("replay market data for %s lifecycle: %w", policy, err)
	}
	prev, err := n.commitMarketDataTransitionWithHook(transition, next, commit)
	if err != nil {
		n.cancelMarketDataTransition(transition)
		next.Stop()
		return nil, fmt.Errorf("commit %s lifecycle engine transition: %w", policy, err)
	}
	if prev != nil && prev != next {
		prev.Stop()
	}
	if err := n.mirrorSeedAccountBlocks(context.WithoutCancel(ctx), next); err != nil {
		return n.currentMarketDataSink(), n.fatalReconciliation(
			"mirror policy lifecycle seed blocks",
			fmt.Errorf("mirror %s lifecycle seed blocks: %w", policy, err),
		)
	}
	return n.currentMarketDataSink(), nil
}

func (n *localNode) deleteLastPolicyBarrierPrepared(
	ctx context.Context,
	target LimitTarget,
) (marketdata.Sink, bool, error) {
	durableCtx := context.WithoutCancel(ctx)
	switch target.Policy {
	case domain.PolicyRateLimit:
		prev, hadPrev, err := n.readRateBarrier(ctx, target)
		if err != nil {
			return nil, false, err
		}
		limits, err := n.realm.ListRateLimits(ctx, "")
		if err != nil {
			return nil, false, fmt.Errorf("list rate limits for lifecycle delete: %w", err)
		}
		if !hadPrev || len(limits) != 1 {
			return nil, false, nil
		}
		snapshot, _, err := n.loadSnapshot(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("load rate policy lifecycle snapshot: %w", err)
		}
		snapshot.RateLimits = nil
		sink, err := n.replaceEngineForPolicyLifecycle(
			ctx, target.Policy, snapshot, func() error {
				return n.realm.DeleteRateLimit(
					durableCtx, prev.Scope, prev.Account, prev.Asset,
				)
			},
		)
		return sink, true, err
	case domain.PolicyOrderSizeLimit:
		prev, hadPrev, err := n.readOrderSizeBarrier(ctx, target)
		if err != nil {
			return nil, false, err
		}
		limits, err := n.realm.ListOrderSizeLimits(ctx, "")
		if err != nil {
			return nil, false, fmt.Errorf(
				"list order-size limits for lifecycle delete: %w", err,
			)
		}
		if !hadPrev || len(limits) != 1 {
			return nil, false, nil
		}
		snapshot, _, err := n.loadSnapshot(ctx)
		if err != nil {
			return nil, false, fmt.Errorf(
				"load order-size policy lifecycle snapshot: %w", err,
			)
		}
		snapshot.OrderSizeLimits = nil
		sink, err := n.replaceEngineForPolicyLifecycle(
			ctx, target.Policy, snapshot, func() error {
				return n.realm.DeleteOrderSizeLimit(
					durableCtx, prev.Scope, prev.Account, prev.Asset,
				)
			},
		)
		return sink, true, err
	default:
		return nil, false, nil
	}
}

// applyPolicyChangeLocked applies a just-persisted barrier change for policy to
// the engine via the runtime Configure surface. revert restores the barrier the
// caller just wrote and is invoked here, before any recovery reads the store.
//
// The engine's not-implemented stubs are pre-flight guards: they reject before
// any engine mutation, so nothing has to be reconciled. Rate and order-size
// callers keep the barrier and rebuild from the persisted snapshot, making the
// store the desired truth; SpotFunds P&L-bounds callers revert instead, because
// they never rebuild for a change the surface cannot express.
//
// Any other failure may have landed part-way. A SpotFunds P&L-bounds configure
// is expressed by the SDK runtime surface, so the node reverts the just-written
// barrier and rebuilds from the reverted store to make the live handle match
// durable state. The rebuild replaces the handle, so its sink is returned
// alongside the error for the caller to re-adopt.
func (n *localNode) applyPolicyChangeLocked(
	ctx context.Context, policy string, revert func() error,
) (marketdata.Sink, error) {
	result, err := n.reconfigurePolicy(ctx, policy)
	if err == nil {
		if err := n.mirrorPolicyConfigurationBlocks(
			ctx, policy, result.AccountBlocks,
		); err != nil {
			return nil, err
		}
		return nil, nil
	}
	spotFunds := policy == domain.PolicySpotFundsPnlBoundsKillSwitch
	durableCtx := context.WithoutCancel(ctx)
	revertPolicy := func() error {
		if revertErr := revert(); revertErr != nil {
			return n.fatalPostEngineAuditByCode(
				"revert policy configuration", "policy", policy,
				fmt.Errorf("revert policy configuration: %w", revertErr),
			)
		}
		return nil
	}

	if errors.Is(err, domain.ErrNotImplemented) {
		if spotFunds {
			if revertErr := revertPolicy(); revertErr != nil {
				return nil, revertErr
			}
			return nil, err
		}
		if rebuildErr := n.rebuildEngineFromStore(durableCtx); rebuildErr != nil {
			return nil, n.fatalReconciliation(
				"reconcile unexpected policy lifecycle gap",
				errors.Join(
					err,
					fmt.Errorf("rebuild %s policy after online lifecycle gap: %w", policy, rebuildErr),
				),
			)
		}
		return n.currentMarketDataSink(), nil
	}

	if revertErr := revertPolicy(); revertErr != nil {
		return nil, revertErr
	}
	if !spotFunds {
		return nil, err
	}
	if rebuildErr := n.rebuildEngineFromStore(durableCtx); rebuildErr != nil {
		reconcileErr := errors.Join(err, fmt.Errorf(
			"rebuild engine after failed spot funds pnl-bounds configure: %w",
			rebuildErr,
		))
		return nil, n.fatalPostEngineAuditByCode(
			"rebuild after failed spot funds pnl-bounds configuration",
			"policy",
			policy,
			reconcileErr,
		)
	}
	return n.currentMarketDataSink(), err
}

// reconfigurePolicy re-reads the full typed barrier set for policy from the
// store and applies it to the engine via the runtime Configure surface. It
// returns the engine error verbatim so the caller can decide whether to revert
// or rebuild.
func (n *localNode) reconfigurePolicy(
	ctx context.Context, policy string,
) (engine.PolicyConfigurationResult, error) {
	limits, err := n.policyLimitSet(ctx, policy)
	if err != nil {
		return engine.PolicyConfigurationResult{}, err
	}
	return n.currentEngine().ConfigurePolicy(ctx, policy, limits)
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

// ensureLimitAccount applies the request's missing-account choice to a barrier
// whose scope carries the account axis. Scopes without that axis (broker,
// global, asset, account_group) name no account and need no decision.
//
// The Put* callers hold beginLivePolicyConfiguration, which locks the same
// (laneGate, mutate) pair as beginLiveIdentityPublication and therefore already
// provides the account-lane exclusion ensureAutoCreatedAccount requires.
// ensureAccount is used directly for that reason: the self-acquiring
// ensureAccountAndAssetsRegisteredExclusive would deadlock on the held gate.
//
// Creating the account here publishes its resolver entry immediately, so the
// barrier resolves to a real engine id and is enforced from the same call. The
// explicit create is an independent identity mutation: a later barrier store or
// engine failure reverts the barrier, but deliberately keeps the account (and
// any auto-created asset) registered for an idempotent retry.
func (n *localNode) ensureLimitAccount(
	ctx context.Context,
	scope domain.LimitScope,
	account domain.AccountID,
	missing domain.MissingAccountPolicy,
	operation string,
	caller domain.Caller,
) error {
	switch scope {
	case domain.ScopeAccount, domain.ScopeAccountAsset:
		return n.ensureAccount(ctx, account, missing, operation, caller)
	default:
		return nil
	}
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
) error {
	if hadPrev {
		if err := n.realm.PutRateLimit(ctx, prev); err != nil {
			return fmt.Errorf("restore rate-limit barrier: %w", err)
		}
		return nil
	}
	if err := n.realm.DeleteRateLimit(ctx, target.Scope, target.Account, target.Asset); err != nil {
		return fmt.Errorf("remove rate-limit barrier: %w", err)
	}
	return nil
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
) error {
	if hadPrev {
		if err := n.realm.PutOrderSizeLimit(ctx, prev); err != nil {
			return fmt.Errorf("restore order-size barrier: %w", err)
		}
		return nil
	}
	if err := n.realm.DeleteOrderSizeLimit(ctx, target.Scope, target.Account, target.Asset); err != nil {
		return fmt.Errorf("remove order-size barrier: %w", err)
	}
	return nil
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
			limit.AccountGroup == target.AccountGroup {
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
) error {
	if hadPrev {
		if err := n.realm.PutSpotFundsPnlBoundsLimit(ctx, prev); err != nil {
			return fmt.Errorf("restore spot funds pnl-bounds barrier: %w", err)
		}
		return nil
	}
	if err := n.realm.DeleteSpotFundsPnlBoundsLimit(
		ctx,
		target.Scope,
		target.Account,
		target.AccountGroup,
	); err != nil {
		return fmt.Errorf("remove spot funds pnl-bounds barrier: %w", err)
	}
	return nil
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
