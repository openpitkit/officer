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

package backend

import (
	"context"
	"fmt"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// ListAssets returns every asset in the realm.
func (s *Service) ListAssets(ctx context.Context) ([]domain.Asset, error) {
	page, err := s.ListAssetRows(ctx, store.AssetListFilter{})
	if err != nil {
		return nil, err
	}
	return page.Rows, nil
}

// ListAssetRows returns every asset in the realm in list sort order.
func (s *Service) ListAssetRows(
	ctx context.Context, filter store.AssetListFilter,
) (store.AssetListPage, error) {
	n, err := s.groupNode()
	if err != nil {
		return store.AssetListPage{}, err
	}
	page, err := n.ListAssetRows(ctx, filter)
	if err != nil {
		return store.AssetListPage{}, fmt.Errorf("backend: list assets: %w", err)
	}
	return page, nil
}

// CreateAsset validates the asset metadata and creates the asset.
func (s *Service) CreateAsset(
	ctx context.Context, asset domain.Asset,
) (domain.Asset, error) {
	if err := domain.ValidateAsset(asset.Code); err != nil {
		return domain.Asset{}, err
	}
	if err := domain.ValidateTitle(asset.Title); err != nil {
		return domain.Asset{}, err
	}
	if err := domain.ValidateTitle(asset.AssetClass); err != nil {
		return domain.Asset{}, err
	}
	n, err := s.groupNode()
	if err != nil {
		return domain.Asset{}, err
	}
	return n.CreateAsset(ctx, asset, auth.CallerFromContext(ctx))
}

// UpdateAsset validates the old and new asset metadata and updates the asset,
// renaming its public code when it differs.
func (s *Service) UpdateAsset(
	ctx context.Context, oldCode string, asset domain.Asset,
) (domain.Asset, error) {
	if err := domain.ValidateAsset(oldCode); err != nil {
		return domain.Asset{}, err
	}
	if err := domain.ValidateAsset(asset.Code); err != nil {
		return domain.Asset{}, err
	}
	if err := domain.ValidateTitle(asset.Title); err != nil {
		return domain.Asset{}, err
	}
	if err := domain.ValidateTitle(asset.AssetClass); err != nil {
		return domain.Asset{}, err
	}
	n, err := s.groupNode()
	if err != nil {
		return domain.Asset{}, err
	}
	return n.UpdateAsset(ctx, oldCode, asset, auth.CallerFromContext(ctx))
}

// --- Asset classes ---------------------------------------------------------

// ListAssetClasses returns every asset class in the realm.
func (s *Service) ListAssetClasses(ctx context.Context) ([]domain.AssetClass, error) {
	page, err := s.ListAssetClassRows(ctx, store.AssetClassListFilter{})
	if err != nil {
		return nil, err
	}
	classes := make([]domain.AssetClass, 0, len(page.Rows))
	for _, row := range page.Rows {
		classes = append(classes, row.Class)
	}
	return classes, nil
}

// ListAssetClassRows returns asset classes in the realm with list-only counts.
func (s *Service) ListAssetClassRows(
	ctx context.Context, filter store.AssetClassListFilter,
) (store.AssetClassListPage, error) {
	n, err := s.groupNode()
	if err != nil {
		return store.AssetClassListPage{}, err
	}
	page, err := n.ListAssetClassRows(ctx, filter)
	if err != nil {
		return store.AssetClassListPage{}, fmt.Errorf("backend: list asset class rows: %w", err)
	}
	return page, nil
}

// CreateAssetClass validates the class metadata and creates the class.
func (s *Service) CreateAssetClass(
	ctx context.Context, class domain.AssetClass,
) (domain.AssetClass, error) {
	if err := domain.ValidateAssetClassID(class.Code); err != nil {
		return domain.AssetClass{}, err
	}
	if err := domain.ValidateTitle(class.Title); err != nil {
		return domain.AssetClass{}, err
	}
	if err := domain.ValidateNotes(class.Notes); err != nil {
		return domain.AssetClass{}, err
	}
	n, err := s.groupNode()
	if err != nil {
		return domain.AssetClass{}, err
	}
	return n.CreateAssetClass(ctx, class, auth.CallerFromContext(ctx))
}

// UpdateAssetClass validates the old and new class metadata and updates the
// class, renaming its public code when it differs.
func (s *Service) UpdateAssetClass(
	ctx context.Context, oldCode string, class domain.AssetClass,
) (domain.AssetClass, error) {
	if err := domain.ValidateAssetClassID(oldCode); err != nil {
		return domain.AssetClass{}, err
	}
	if err := domain.ValidateAssetClassID(class.Code); err != nil {
		return domain.AssetClass{}, err
	}
	if err := domain.ValidateTitle(class.Title); err != nil {
		return domain.AssetClass{}, err
	}
	if err := domain.ValidateNotes(class.Notes); err != nil {
		return domain.AssetClass{}, err
	}
	n, err := s.groupNode()
	if err != nil {
		return domain.AssetClass{}, err
	}
	return n.UpdateAssetClass(ctx, oldCode, class, auth.CallerFromContext(ctx))
}

// DeleteAssetClass validates the code and removes the class, clearing the asset
// link when force is set.
func (s *Service) DeleteAssetClass(ctx context.Context, code string, force bool) error {
	if err := domain.ValidateAssetClassID(code); err != nil {
		return err
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.DeleteAssetClass(ctx, code, force, auth.CallerFromContext(ctx))
}

// DeleteAsset validates the code and removes the asset, cascading dependents
// when force is set.
func (s *Service) DeleteAsset(ctx context.Context, code string, force bool) error {
	if err := domain.ValidateAsset(code); err != nil {
		return err
	}
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()

	n, err := s.groupNode()
	if err != nil {
		return err
	}
	if err := n.DeleteAsset(ctx, code, force, auth.CallerFromContext(ctx)); err != nil {
		return err
	}
	// Both delete modes re-apply the market-data configuration. A forced delete
	// cascades the instruments that referenced the asset, so its subscriptions must
	// be rebuilt. A non-forced delete cannot reach an instrument at all: the store
	// refuses it while any market_data_instrument references the asset, so the
	// restart changes no subscription. It runs anyway so that feed lifecycle does
	// not depend on which delete mode the operator chose.
	return s.restartMarketDataAfterDeleteLocked()
}
