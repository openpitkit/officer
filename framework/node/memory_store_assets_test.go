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
	"sort"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func (r *memoryRealm) CreateAsset(_ context.Context, asset domain.Asset) error {
	if _, ok := r.assets[asset.Code]; ok {
		return domain.ErrAlreadyExists
	}
	r.assets[asset.Code] = asset
	return nil
}

func (r *memoryRealm) GetAsset(_ context.Context, code string) (domain.Asset, bool, error) {
	asset, ok := r.assets[code]
	return asset, ok, nil
}

func (r *memoryRealm) ListAssets(context.Context) ([]domain.Asset, error) {
	page, err := r.ListAssetRows(context.Background(), store.AssetListFilter{})
	if err != nil {
		return nil, err
	}
	return page.Rows, nil
}

func (r *memoryRealm) ListAssetRows(
	_ context.Context, filter store.AssetListFilter,
) (store.AssetListPage, error) {
	out := make([]domain.Asset, 0, len(r.assets))
	for _, asset := range r.assets {
		codeMatches := textMatches(filter.Code, asset.Code) ||
			textMatches(filter.Code, asset.Title)
		if !codeMatches ||
			!textMatches(filter.Class, asset.AssetClass) {
			continue
		}
		out = append(out, asset)
	}
	sort.Slice(out, func(i, j int) bool {
		leftTie := out[i].Code < out[j].Code
		var less bool
		switch filter.Sort.Column {
		case "assetClass":
			less = out[i].AssetClass < out[j].AssetClass ||
				(out[i].AssetClass == out[j].AssetClass && leftTie)
		case "title":
			less = out[i].Title < out[j].Title ||
				(out[i].Title == out[j].Title && leftTie)
		default:
			less = leftTie
		}
		if filter.Sort.Descending {
			switch filter.Sort.Column {
			case "assetClass":
				return out[i].AssetClass > out[j].AssetClass ||
					(out[i].AssetClass == out[j].AssetClass && out[i].Code > out[j].Code)
			case "title":
				return out[i].Title > out[j].Title ||
					(out[i].Title == out[j].Title && out[i].Code > out[j].Code)
			default:
				return out[i].Code > out[j].Code
			}
		}
		return less
	})
	total := len(out)
	if filter.Page.Limit > 0 {
		start := min(max(filter.Page.Offset, 0), len(out))
		end := min(start+filter.Page.Limit, len(out))
		out = out[start:end]
	}
	return store.AssetListPage{Rows: out, Total: total}, nil
}

func (r *memoryRealm) UpdateAsset(
	_ context.Context, oldCode string, asset domain.Asset,
) (domain.Asset, error) {
	if _, ok := r.assets[oldCode]; !ok {
		return domain.Asset{}, domain.ErrNotFound
	}
	if oldCode != asset.Code {
		if _, ok := r.assets[asset.Code]; ok {
			return domain.Asset{}, domain.ErrAlreadyExists
		}
		delete(r.assets, oldCode)
	}
	r.assets[asset.Code] = asset
	return asset, nil
}

func (r *memoryRealm) DeleteAsset(_ context.Context, code string, _ bool) error {
	if _, ok := r.assets[code]; !ok {
		return domain.ErrNotFound
	}
	delete(r.assets, code)
	return nil
}

func (r *memoryRealm) CreateAssetClass(_ context.Context, class domain.AssetClass) error {
	if _, ok := r.assetClasses[class.Code]; ok {
		return domain.ErrAlreadyExists
	}
	r.assetClasses[class.Code] = class
	return nil
}

func (r *memoryRealm) GetAssetClass(
	_ context.Context, code string,
) (domain.AssetClass, bool, error) {
	class, ok := r.assetClasses[code]
	return class, ok, nil
}

func (r *memoryRealm) ListAssetClasses(context.Context) ([]domain.AssetClass, error) {
	out := make([]domain.AssetClass, 0, len(r.assetClasses))
	for _, class := range r.assetClasses {
		out = append(out, class)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (r *memoryRealm) ListAssetClassRows(
	ctx context.Context, filter store.AssetClassListFilter,
) (store.AssetClassListPage, error) {
	classes, err := r.ListAssetClasses(ctx)
	if err != nil {
		return store.AssetClassListPage{}, err
	}
	out := make([]store.AssetClassListRow, 0, len(classes))
	for _, class := range classes {
		row := store.AssetClassListRow{Class: class}
		for _, asset := range r.assets {
			if asset.AssetClass == class.Code {
				row.AssetCount++
			}
		}
		if !textMatchesAny(filter.Code, row.Class.Code, row.Class.Title) ||
			!textMatches(filter.Notes, row.Class.Notes) {
			continue
		}
		out = append(out, row)
	}
	sortAssetClassRows(out, filter.Sort)
	total := len(out)
	out = pageRows(out, filter.Page)
	return store.AssetClassListPage{Rows: out, Total: total}, nil
}

func (r *memoryRealm) UpdateAssetClass(
	_ context.Context, oldCode string, class domain.AssetClass,
) (domain.AssetClass, error) {
	if _, ok := r.assetClasses[oldCode]; !ok {
		return domain.AssetClass{}, domain.ErrNotFound
	}
	if oldCode != class.Code {
		if _, ok := r.assetClasses[class.Code]; ok {
			return domain.AssetClass{}, domain.ErrAlreadyExists
		}
		delete(r.assetClasses, oldCode)
		for code, asset := range r.assets {
			if asset.AssetClass == oldCode {
				asset.AssetClass = class.Code
				r.assets[code] = asset
			}
		}
	}
	r.assetClasses[class.Code] = class
	return class, nil
}

func (r *memoryRealm) DeleteAssetClass(_ context.Context, code string, force bool) error {
	if _, ok := r.assetClasses[code]; !ok {
		return domain.ErrNotFound
	}
	count := 0
	for _, asset := range r.assets {
		if asset.AssetClass == code {
			count++
		}
	}
	if count > 0 && !force {
		return domain.NewHasDependentsError([]domain.DependentCount{{Kind: "asset", Count: count}})
	}
	for c, asset := range r.assets {
		if asset.AssetClass == code {
			asset.AssetClass = ""
			r.assets[c] = asset
		}
	}
	delete(r.assetClasses, code)
	return nil
}

func sortAssetClassRows(rows []store.AssetClassListRow, spec store.SortSpec) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		cmp := 0
		switch spec.Column {
		case "assetCount":
			cmp = compareInts(left.AssetCount, right.AssetCount)
		case "title":
			cmp = compareStrings(left.Class.Title, right.Class.Title)
		default:
			cmp = compareStrings(left.Class.Code, right.Class.Code)
		}
		if cmp == 0 {
			cmp = compareStrings(left.Class.Code, right.Class.Code)
		}
		return sortCompare(cmp, spec.Descending)
	})
}
