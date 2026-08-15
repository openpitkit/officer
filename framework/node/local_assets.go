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

	// Deliberately checked before the store write: no point persisting an
	// asset the engine cannot publish. This also means a missing resolver
	// capability now surfaces as ErrNotImplemented ahead of the store's own
	// duplicate-code ErrAlreadyExists.
	resolver, err := requireDictionaryResolver(n.currentEngine())
	if err != nil {
		return domain.Asset{}, err
	}
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
		Detail: fmt.Sprintf("create asset %s", created.Code),
	}); err != nil {
		return domain.Asset{}, n.fatalPostEngineAuditByCode(
			"audit create asset", "asset", created.Code,
			fmt.Errorf("audit create asset: %w", err),
		)
	}
	return created, nil
}

// UpdateAsset replaces the asset's public code and mutable fields and
// audits the action. A code rename rebuilds the live engine from the
// renamed store snapshot and can report ErrEngineRestarting while another
// rebuild is in progress.
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
		Detail: updateAssetDetail(oldCode, updated.Code),
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
	// Same deliberate precedence as CreateAsset: checked ahead of the store
	// write, so a missing resolver capability surfaces as ErrNotImplemented
	// ahead of the store's own duplicate-code ErrAlreadyExists.
	resolver, err := requireDictionaryResolver(n.currentEngine())
	if err != nil {
		return domain.Asset{}, err
	}

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
	detail := updateAssetDetail(oldCode, updated.Code)
	entries := []store.AuditEntry{{
		Action: domain.AuditActionUpdateAsset,
		Asset:  updated.Code,
		Detail: detail,
	}}
	if oldCode != updated.Code {
		entries[0].Detail += " (record under new code)"
		entries = append([]store.AuditEntry{{
			Action: domain.AuditActionUpdateAsset,
			Asset:  oldCode,
			Detail: detail + " (record under old code)",
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

// updateAssetDetail renders one asset update. Both codes are named only when
// the update renames the asset; a same-code update has no arrow to render.
func updateAssetDetail(previous string, next string) string {
	if previous == next {
		return fmt.Sprintf("update asset %s", next)
	}
	return fmt.Sprintf("update asset %s -> %s", previous, next)
}

// DeleteAsset removes the asset and audits the action. A forced delete rebuilds
// the engine without the rows that the store cascades.
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
		resolver, err := requireDictionaryResolver(n.currentEngine())
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		durableCtx := context.WithoutCancel(ctx)
		if err := n.realm.DeleteAsset(durableCtx, code, false); err != nil {
			return fmt.Errorf("delete asset: %w", err)
		}
		auditDelete := func(detail string) error {
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
			failureDetail := fmt.Sprintf(
				"delete asset %s "+
					"(resolver removal failed)",
				code,
			)
			if auditErr := auditDelete(failureDetail); auditErr != nil {
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
			reconciledDetail := fmt.Sprintf(
				"delete asset %s "+
					"(resolver removal failed, reconciled)",
				code,
			)
			if auditErr := auditDelete(reconciledDetail); auditErr != nil {
				return internalPostCommitNodeMutationError(
					errors.Join(reconciliation.err, auditErr),
				)
			}
			return internalPostCommitNodeMutationError(reconciliation.err)
		}
		if auditErr := auditDelete(
			fmt.Sprintf("delete asset %s", code),
		); auditErr != nil {
			return n.fatalPostEngineAuditByCode(
				"audit delete asset", "asset", code, auditErr,
			)
		}
		return nil
	}

	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()

	asset, ok, err := n.realm.GetAsset(ctx, code)
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
	transition, err := n.beginMarketDataTransition(next)
	if err != nil {
		next.Stop()
		return fmt.Errorf("prepare asset delete market data: %w", err)
	}
	transition.excludeAsset(asset.EngineAssetID)
	if err := n.replayMarketDataWithoutAssetInto(ctx, next, code); err != nil {
		n.cancelMarketDataTransition(transition)
		next.Stop()
		return fmt.Errorf("replay market data for asset delete: %w", err)
	}

	durableCtx := context.WithoutCancel(ctx)
	prev, err := n.commitMarketDataTransitionWithHook(transition, next, func() error {
		if err := n.realm.DeleteAsset(durableCtx, code, true); err != nil {
			return fmt.Errorf("delete asset: %w", err)
		}
		return nil
	})
	if err != nil {
		n.cancelMarketDataTransition(transition)
		next.Stop()
		return fmt.Errorf("commit asset delete engine transition: %w", err)
	}
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
		Detail: fmt.Sprintf("delete asset %s", code),
	}); err != nil {
		return n.fatalPostEngineAuditByCode(
			"audit delete asset", "asset", code,
			fmt.Errorf("audit delete asset: %w", err),
		)
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
		func(row domain.LimitRate) bool { return row.Asset == asset },
	)
	snapshot.OrderSizeLimits = slices.DeleteFunc(
		snapshot.OrderSizeLimits,
		func(row domain.LimitOrderSize) bool { return row.Asset == asset },
	)
	snapshot.SpotFundsPnlBoundsLimits = slices.DeleteFunc(
		snapshot.SpotFundsPnlBoundsLimits,
		func(row domain.LimitSpotFundsPnlBounds) bool { return row.Currency == asset },
	)
	return snapshot
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
		Detail: fmt.Sprintf("create asset class %s", class.Code),
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
		Detail: fmt.Sprintf("update asset class %s -> %s", oldCode, updated.Code),
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
		Detail: fmt.Sprintf("delete asset class %s", code),
	}); err != nil {
		return fmt.Errorf("audit delete asset class: %w", err)
	}
	return nil
}
