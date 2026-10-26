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

package node

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// ListAssets returns every persisted asset.
func (n *localNode) ListAssets(ctx context.Context) ([]domain.Asset, error) {
	page, err := n.ListAssetRows(ctx, store.AssetListFilter{})
	if err != nil {
		return nil, err
	}
	return page.Rows, nil
}

// ListAssetRows returns persisted assets matching filter, with total count.
func (n *localNode) ListAssetRows(
	ctx context.Context, filter store.AssetListFilter,
) (store.AssetListPage, error) {
	page, err := n.realm.ListAssetRows(ctx, filter)
	if err != nil {
		return store.AssetListPage{}, fmt.Errorf("list asset rows: %w", err)
	}
	return page, nil
}

// CreateAsset persists and publishes a new asset dictionary row, then audits
// the action.
func (n *localNode) CreateAsset(
	ctx context.Context, asset domain.Asset, caller domain.Caller,
) (domain.Asset, error) {
	if err := n.beginLiveIdentityPublication(); err != nil {
		return domain.Asset{}, err
	}
	defer n.endLiveIdentityPublication()

	resolver := n.currentEngine()
	created, err := n.realm.CreateAsset(ctx, asset)
	if err != nil {
		return domain.Asset{}, fmt.Errorf("create asset: %w", err)
	}
	if err := resolver.AddAssetResolverEntry(created); err != nil {
		mutationCtx := context.WithoutCancel(ctx)
		if rollbackErr := n.realm.DeleteAsset(
			mutationCtx, created.Code, false,
		); rollbackErr != nil {
			cause := errors.Join(
				fmt.Errorf("publish asset resolver: %w", err),
				fmt.Errorf("rollback asset create: %w", rollbackErr),
			)
			return domain.Asset{}, internalPostCommitNodeMutationError(
				n.reconcileEngineAfterFailure(
					mutationCtx,
					"reconcile engine after asset creation failure",
					cause,
				).err,
			)
		}
		return domain.Asset{}, internalPostCommitNodeMutationError(
			fmt.Errorf("publish asset resolver: %w", err),
		)
	}
	if err := n.audit(context.WithoutCancel(ctx), caller, store.AuditEntry{
		Action: domain.AuditActionCreateAsset,
		Asset:  created.Code,
		Detail: domain.NewAuditDetail(
			domain.AuditDetailCreateAsset, domain.AuditDetailParams{"asset": created.Code},
		),
	}); err != nil {
		return domain.Asset{}, n.fatalPostEngineAuditByCode(
			"audit create asset", "asset", created.Code,
			fmt.Errorf("audit create asset: %w", err),
		)
	}
	return created, nil
}

// UpdateAsset replaces the asset's public code and mutable fields and
// audits the action. A code rename republishes the asset's resolver alias on
// the live engine rather than rebuilding it (see renameAsset) and can report
// ErrEngineRestarting while an engine rebuild is in progress. A tighten-only
// caller's code change is refused before anything changes while a barrier
// references the asset (see guardTightenOnlyAssetRelease); a title or
// asset-class change alone passes.
func (n *localNode) UpdateAsset(
	ctx context.Context, oldCode string, asset domain.Asset, caller domain.Caller,
) (domain.Asset, error) {
	if oldCode != asset.Code {
		return n.renameAsset(ctx, oldCode, asset, caller)
	}
	if err := n.beginMutation(); err != nil {
		return domain.Asset{}, err
	}
	defer n.endMutation()

	updated, err := n.realm.UpdateAsset(ctx, oldCode, asset)
	if err != nil {
		return domain.Asset{}, fmt.Errorf("update asset: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionUpdateAsset,
		Asset:  updated.Code,
		Detail: domain.NewAuditDetail(
			domain.AuditDetailUpdateAsset, domain.AuditDetailParams{"asset": updated.Code},
		),
	}); err != nil {
		return domain.Asset{}, fmt.Errorf("audit update asset: %w", err)
	}
	return updated, nil
}

// renameAsset updates the persisted code and atomically publishes the new
// alias. The stable engine asset id keeps SDK state attached to the same ready
// asset while the operator-facing code changes.
func (n *localNode) renameAsset(
	ctx context.Context, oldCode string, asset domain.Asset, caller domain.Caller,
) (domain.Asset, error) {
	if err := n.beginLiveIdentityPublication(); err != nil {
		return domain.Asset{}, err
	}
	defer n.endLiveIdentityPublication()

	previous, ok, err := n.realm.GetAsset(ctx, oldCode)
	if err != nil {
		return domain.Asset{}, fmt.Errorf("read asset for rename: %w", err)
	}
	if !ok {
		return domain.Asset{}, fmt.Errorf("asset %q: %w", oldCode, domain.ErrNotFound)
	}
	if err := n.guardTightenOnlyAssetRelease(ctx, previous.Code, caller); err != nil {
		return domain.Asset{}, fmt.Errorf("rename asset %q: %w", oldCode, err)
	}
	resolver := n.currentEngine()

	durableCtx := context.WithoutCancel(ctx)
	updated, err := n.realm.UpdateAsset(durableCtx, oldCode, asset)
	if err != nil {
		return domain.Asset{}, fmt.Errorf("update asset: %w", err)
	}
	if err := resolver.RenameAssetResolverEntry(previous.Code, updated); err != nil {
		_, rollbackErr := n.realm.UpdateAsset(durableCtx, updated.Code, previous)
		if rollbackErr != nil {
			cause := errors.Join(
				fmt.Errorf("publish asset resolver rename: %w", err),
				fmt.Errorf("rollback asset rename: %w", rollbackErr),
			)
			return domain.Asset{}, internalPostCommitNodeMutationError(
				n.reconcileEngineAfterFailure(
					durableCtx,
					"reconcile engine after asset rename failure",
					cause,
				).err,
			)
		}
		return domain.Asset{}, internalPostCommitNodeMutationError(
			fmt.Errorf("publish asset resolver rename: %w", err),
		)
	}
	// A renamed asset is filed under both codes, and each record says which
	// filing it is; a same-code update has no transition to state.
	entries := []store.AuditEntry{{
		Action: domain.AuditActionUpdateAsset,
		Asset:  updated.Code,
		Detail: domain.NewAuditDetail(
			domain.AuditDetailUpdateAsset, domain.AuditDetailParams{"asset": updated.Code},
		),
	}}
	if oldCode != updated.Code {
		rename := domain.AuditDetailParams{"from": oldCode, "to": updated.Code}
		entries[0].Detail = domain.NewAuditDetail(
			domain.AuditDetailRenameAssetNewRecord, rename,
		)
		entries = append([]store.AuditEntry{{
			Action: domain.AuditActionUpdateAsset,
			Asset:  oldCode,
			Detail: domain.NewAuditDetail(
				domain.AuditDetailRenameAssetOldRecord, rename,
			),
		}}, entries...)
	}
	if err := n.auditBatch(durableCtx, caller, entries); err != nil {
		return domain.Asset{}, n.fatalPostEngineAuditByCode(
			"audit asset rename",
			"asset",
			updated.Code,
			fmt.Errorf("audit update asset: %w", err),
		)
	}
	return updated, nil
}

// DeleteAsset removes the asset and audits the action. A forced delete rebuilds
// the engine without the rows that the store cascades. A tighten-only caller's
// delete without force is refused before anything changes while a barrier
// references the asset (see guardTightenOnlyAssetRelease), and its forced
// delete always, whatever the asset carries: force deletes every dependent row
// of the asset - balances with their realized P&L, a negative held amount the
// spendable funds subtract included, orders, trades and adjustments - and an
// order naming the code recreates the asset with no balance, so a debt or a
// position the funds and P&L checks measure would be gone.
func (n *localNode) DeleteAsset(
	ctx context.Context, code string, force bool, caller domain.Caller,
) error {
	if !force {
		if err := n.beginLiveIdentityPublication(); err != nil {
			return err
		}
		defer n.endLiveIdentityPublication()

		asset, ok, err := n.realm.GetAsset(ctx, code)
		if err != nil {
			return fmt.Errorf("read asset for delete: %w", err)
		}
		if !ok {
			return fmt.Errorf("asset %q: %w", code, domain.ErrNotFound)
		}
		if err := n.guardTightenOnlyAssetRelease(ctx, asset.Code, caller); err != nil {
			return fmt.Errorf("delete asset %q: %w", code, err)
		}
		resolver := n.currentEngine()
		if err := ctx.Err(); err != nil {
			return err
		}
		durableCtx := context.WithoutCancel(ctx)
		if err := n.realm.DeleteAsset(durableCtx, code, false); err != nil {
			return fmt.Errorf("delete asset: %w", err)
		}
		auditDelete := func(detailCode domain.AuditDetailCode) error {
			detail := domain.NewAuditDetail(detailCode, domain.AuditDetailParams{"asset": code})
			if err := n.audit(durableCtx, caller, store.AuditEntry{
				Action: domain.AuditActionDeleteAsset,
				Asset:  code,
				Detail: detail,
			}); err != nil {
				return fmt.Errorf("audit delete asset: %w", err)
			}
			return nil
		}

		if err := resolver.RemoveAssetResolverEntry(asset); err != nil {
			cause := fmt.Errorf(
				"asset %q delete is durable, but resolver removal failed: %w",
				code,
				err,
			)
			if auditErr := auditDelete(
				domain.AuditDetailDeleteAssetResolverFailed,
			); auditErr != nil {
				resultErr := errors.Join(cause, auditErr)
				if rebuildErr := n.rebuildEngineFromStore(durableCtx); rebuildErr != nil {
					resultErr = errors.Join(
						resultErr,
						fmt.Errorf(
							"rebuild engine from current store: %w",
							rebuildErr,
						),
					)
				}
				return n.fatalPostEngineAuditByCode(
					"audit delete asset", "asset", code, resultErr,
				)
			}
			reconciliation := n.reconcileEngineAfterFailure(
				durableCtx,
				"reconcile engine after asset delete failure",
				cause,
			)
			if !reconciliation.reconciled {
				return internalPostCommitNodeMutationError(reconciliation.err)
			}
			reconciliation.err = fmt.Errorf(
				"engine resolver was reconciled after asset delete failure: %w",
				reconciliation.err,
			)
			// Failure of the first write is fatal because it would leave the
			// committed delete unrecorded. This corrective write only records
			// successful reconciliation, so its failure is returned.
			if auditErr := auditDelete(
				domain.AuditDetailDeleteAssetResolverReconciled,
			); auditErr != nil {
				return internalPostCommitNodeMutationError(
					errors.Join(reconciliation.err, auditErr),
				)
			}
			return internalPostCommitNodeMutationError(reconciliation.err)
		}
		if auditErr := auditDelete(domain.AuditDetailDeleteAsset); auditErr != nil {
			return n.fatalPostEngineAuditByCode(
				"audit delete asset", "asset", code, auditErr,
			)
		}
		return nil
	}

	if caller.TightenOnly() {
		return fmt.Errorf(
			"delete asset %q: force would delete the asset's dependent rows with "+
				"it - balances, orders, trades, adjustments and barriers among them: %w",
			code, domain.ErrLoosening,
		)
	}
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()

	_, ok, err := n.realm.GetAsset(ctx, code)
	if err != nil {
		return fmt.Errorf("read asset for delete: %w", err)
	}
	if !ok {
		return fmt.Errorf("asset %q: %w", code, domain.ErrNotFound)
	}

	snapshot, _, err := n.loadSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("load snapshot for asset delete: %w", err)
	}
	if dependents := snapshotAssetCurrencyDependents(snapshot, code); len(dependents) > 0 {
		return domain.NewHasDependentsError(dependents)
	}
	snapshot = snapshotWithoutAsset(snapshot, code)
	previousEngine := n.currentEngine()
	next, err := n.build(snapshot)
	if err != nil {
		return fmt.Errorf("build engine for asset delete: %w", err)
	}
	if next == nil {
		return fmt.Errorf("build engine for asset delete returned nil")
	}
	if next == previousEngine {
		return fmt.Errorf("build engine for asset delete returned current engine")
	}
	durableCtx := context.WithoutCancel(ctx)
	if err := n.realm.DeleteAsset(durableCtx, code, true); err != nil {
		next.Stop()
		return fmt.Errorf("delete asset: %w", err)
	}
	prev := n.swapEngine(next)
	if prev != nil && prev != next {
		prev.Stop()
	}
	if err := n.mirrorSeedAccountBlocks(durableCtx, next); err != nil {
		return n.fatalPostEngineAuditByCode(
			"mirror asset delete seed blocks", "asset", code,
			fmt.Errorf("mirror asset delete seed blocks: %w", err),
		)
	}
	if err := n.audit(durableCtx, caller, store.AuditEntry{
		Action: domain.AuditActionDeleteAsset,
		Asset:  code,
		Detail: domain.NewAuditDetail(
			domain.AuditDetailDeleteAsset, domain.AuditDetailParams{"asset": code},
		),
	}); err != nil {
		return n.fatalPostEngineAuditByCode(
			"audit delete asset", "asset", code,
			fmt.Errorf("audit delete asset: %w", err),
		)
	}
	return nil
}

// guardTightenOnlyAssetRelease refuses a tighten-only caller's release of the
// asset code names - its delete without force or a change of its code - while
// a barrier references the asset: a rate-limit or order-size barrier on it, or
// a Spot Funds P&L bound denominated in it. A release frees the code, so an
// asset recreated under it - an order auto-creates the assets it names - comes
// back without the barrier. A forced delete is refused outright (see
// DeleteAsset). It returns nil for a caller that is not tighten-only.
func (n *localNode) guardTightenOnlyAssetRelease(
	ctx context.Context, code string, caller domain.Caller,
) error {
	if !caller.TightenOnly() {
		return nil
	}
	limits, err := n.listLimits(ctx, "")
	if err != nil {
		return err
	}
	for _, limit := range limits.RateLimits {
		if isAssetRateLimit(limit, code) {
			return fmt.Errorf(
				"a rate-limit barrier references the asset: %w", domain.ErrLoosening,
			)
		}
	}
	for _, limit := range limits.OrderSizeLimits {
		if isAssetOrderSizeLimit(limit, code) {
			return fmt.Errorf(
				"an order-size barrier references the asset: %w", domain.ErrLoosening,
			)
		}
	}
	for _, limit := range limits.SpotFundsPnlBoundsLimits {
		if isAssetSpotFundsPnlBoundsLimit(limit, code) {
			return fmt.Errorf(
				"a Spot Funds P&L bound is denominated in the asset: %w",
				domain.ErrLoosening,
			)
		}
	}
	return nil
}

func snapshotWithoutAsset(snapshot engine.Snapshot, asset string) engine.Snapshot {
	snapshot.Assets = slices.DeleteFunc(snapshot.Assets, func(row domain.Asset) bool {
		return row.Code == asset
	})
	snapshot.Balances = slices.DeleteFunc(snapshot.Balances, func(row domain.Balance) bool {
		return row.Asset == asset
	})
	snapshot.RateLimits = slices.DeleteFunc(
		snapshot.RateLimits,
		func(row domain.LimitRate) bool { return isAssetRateLimit(row, asset) },
	)
	snapshot.OrderSizeLimits = slices.DeleteFunc(
		snapshot.OrderSizeLimits,
		func(row domain.LimitOrderSize) bool {
			return isAssetOrderSizeLimit(row, asset)
		},
	)
	snapshot.SpotFundsPnlBoundsLimits = slices.DeleteFunc(
		snapshot.SpotFundsPnlBoundsLimits,
		func(row domain.LimitSpotFundsPnlBounds) bool {
			return isAssetSpotFundsPnlBoundsLimit(row, asset)
		},
	)
	return snapshot
}

// isAssetRateLimit reports whether limit is a rate-limit barrier on asset.
func isAssetRateLimit(limit domain.LimitRate, asset string) bool {
	return limit.Asset == asset
}

// isAssetOrderSizeLimit reports whether limit is an order-size barrier on
// asset.
func isAssetOrderSizeLimit(limit domain.LimitOrderSize, asset string) bool {
	return limit.Asset == asset
}

// isAssetSpotFundsPnlBoundsLimit reports whether limit is a Spot Funds P&L
// bound denominated in asset.
func isAssetSpotFundsPnlBoundsLimit(
	limit domain.LimitSpotFundsPnlBounds, asset string,
) bool {
	return limit.Currency == asset
}

func snapshotAssetCurrencyDependents(
	snapshot engine.Snapshot,
	asset string,
) []domain.DependentCount {
	accountCount := 0
	for _, account := range snapshot.Accounts {
		if account.Currency == asset {
			accountCount++
		}
	}
	groupCount := 0
	for _, group := range snapshot.Groups {
		if group.Currency == asset {
			groupCount++
		}
	}

	dependents := make([]domain.DependentCount, 0, 2)
	if accountCount > 0 {
		dependents = append(dependents, domain.DependentCount{
			Kind:  "account_currency",
			Count: accountCount,
		})
	}
	if groupCount > 0 {
		dependents = append(dependents, domain.DependentCount{
			Kind:  "account_group_currency",
			Count: groupCount,
		})
	}
	return dependents
}

// ListAssetClasses returns every persisted asset class.
func (n *localNode) ListAssetClasses(
	ctx context.Context,
) ([]domain.AssetClass, error) {
	classes, err := n.realm.ListAssetClasses(ctx)
	if err != nil {
		return nil, fmt.Errorf("list asset classes: %w", err)
	}
	return classes, nil
}

// ListAssetClassRows returns persisted asset classes matching filter.
func (n *localNode) ListAssetClassRows(
	ctx context.Context, filter store.AssetClassListFilter,
) (store.AssetClassListPage, error) {
	page, err := n.realm.ListAssetClassRows(ctx, filter)
	if err != nil {
		return store.AssetClassListPage{}, fmt.Errorf("list asset class rows: %w", err)
	}
	return page, nil
}

// CreateAssetClass persists a new asset-class dictionary row and audits the
// action. The class is store-only, so there is no engine side-effect.
func (n *localNode) CreateAssetClass(
	ctx context.Context, class domain.AssetClass, caller domain.Caller,
) (domain.AssetClass, error) {
	if err := n.beginMutation(); err != nil {
		return domain.AssetClass{}, err
	}
	defer n.endMutation()

	if err := n.realm.CreateAssetClass(ctx, class); err != nil {
		return domain.AssetClass{}, fmt.Errorf("create asset class: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionCreateAssetClass,
		Detail: domain.NewAuditDetail(
			domain.AuditDetailCreateAssetClass, domain.AuditDetailParams{"class": class.Code},
		),
	}); err != nil {
		return domain.AssetClass{}, fmt.Errorf("audit create asset class: %w", err)
	}
	return class, nil
}

// UpdateAssetClass replaces the class's public code, title and notes and audits
// the action. The store cascades the asset link on a code rename; the class is
// store-only, so there is no engine side-effect.
func (n *localNode) UpdateAssetClass(
	ctx context.Context, oldCode string, class domain.AssetClass, caller domain.Caller,
) (domain.AssetClass, error) {
	if err := n.beginMutation(); err != nil {
		return domain.AssetClass{}, err
	}
	defer n.endMutation()

	updated, err := n.realm.UpdateAssetClass(ctx, oldCode, class)
	if err != nil {
		return domain.AssetClass{}, fmt.Errorf("update asset class: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionUpdateAssetClass,
		Detail: domain.NewAuditDetail(
			domain.AuditDetailUpdateAssetClass,
			domain.AuditDetailParams{"from": oldCode, "to": updated.Code},
		),
	}); err != nil {
		return domain.AssetClass{}, fmt.Errorf("audit update asset class: %w", err)
	}
	return updated, nil
}

// DeleteAssetClass removes the class, clearing the asset link when force is set,
// and audits the action. The class is store-only, so there is no engine
// side-effect.
func (n *localNode) DeleteAssetClass(
	ctx context.Context, code string, force bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.DeleteAssetClass(ctx, code, force); err != nil {
		return fmt.Errorf("delete asset class: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionDeleteAssetClass,
		Detail: domain.NewAuditDetail(
			domain.AuditDetailDeleteAssetClass, domain.AuditDetailParams{"class": code},
		),
	}); err != nil {
		return fmt.Errorf("audit delete asset class: %w", err)
	}
	return nil
}
