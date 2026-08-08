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
	"fmt"

	"go.openpit.dev/officer/framework/domain"
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

// CreateAsset persists a new asset dictionary row and audits the action.
func (n *localNode) CreateAsset(
	ctx context.Context, asset domain.Asset, caller domain.Caller,
) (domain.Asset, error) {
	if err := n.beginMutation(); err != nil {
		return domain.Asset{}, err
	}
	defer n.endMutation()

	if err := n.realm.CreateAsset(ctx, asset); err != nil {
		return domain.Asset{}, fmt.Errorf("create asset: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionCreateAsset,
		Asset:  asset.Code,
		Detail: fmt.Sprintf("create asset %s", asset.Code),
	}); err != nil {
		return domain.Asset{}, fmt.Errorf("audit create asset: %w", err)
	}
	return asset, nil
}

// UpdateAsset replaces the asset's public code and mutable fields and audits the
// action.
func (n *localNode) UpdateAsset(
	ctx context.Context, oldCode string, asset domain.Asset, caller domain.Caller,
) (domain.Asset, error) {
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

// updateAssetDetail renders one asset update. Both codes are named only when
// the update renames the asset; a same-code update has no arrow to render.
func updateAssetDetail(previous string, next string) string {
	if previous == next {
		return fmt.Sprintf("update asset %s", next)
	}
	return fmt.Sprintf("update asset %s -> %s", previous, next)
}

// DeleteAsset removes the asset, cascading its dependent rows when force is set,
// and audits the action. The asset is store-only, so there is no engine
// side-effect.
func (n *localNode) DeleteAsset(
	ctx context.Context, code string, force bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.DeleteAsset(ctx, code, force); err != nil {
		return fmt.Errorf("delete asset: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionDeleteAsset,
		Asset:  code,
		Detail: fmt.Sprintf("delete asset %s", code),
	}); err != nil {
		return fmt.Errorf("audit delete asset: %w", err)
	}
	return nil
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
