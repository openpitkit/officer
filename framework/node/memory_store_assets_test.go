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
		for code, group := range r.groups {
			if group.Currency == oldCode {
				group.Currency = asset.Code
				r.groups[code] = group
			}
		}
		for code, account := range r.accounts {
			if account.Currency == oldCode {
				account.Currency = asset.Code
				r.accounts[code] = account
			}
		}
		for key, balance := range r.balances {
			if balance.Asset != oldCode {
				continue
			}
			delete(r.balances, key)
			balance.Asset = asset.Code
			r.balances[balanceKey(balance.Account, balance.Asset)] = balance
		}
		for key, limit := range r.rateLimits {
			if limit.Asset != oldCode {
				continue
			}
			delete(r.rateLimits, key)
			limit.Asset = asset.Code
			r.rateLimits[limitKey(limit.Scope, limit.Account, limit.Asset)] = limit
		}
		for key, limit := range r.orderSizeLimits {
			if limit.Asset != oldCode {
				continue
			}
			delete(r.orderSizeLimits, key)
			limit.Asset = asset.Code
			r.orderSizeLimits[limitKey(limit.Scope, limit.Account, limit.Asset)] = limit
		}
		for key, limit := range r.spotFundsPnlBoundsLimits {
			if limit.Currency == oldCode {
				limit.Currency = asset.Code
				r.spotFundsPnlBoundsLimits[key] = limit
			}
		}
		for id, order := range r.orders {
			if order.BaseAsset == oldCode {
				order.BaseAsset = asset.Code
			}
			if order.QuoteAsset == oldCode {
				order.QuoteAsset = asset.Code
			}
			r.orders[id] = order
		}
		for i := range r.trades {
			if r.trades[i].BaseAsset == oldCode {
				r.trades[i].BaseAsset = asset.Code
			}
			if r.trades[i].QuoteAsset == oldCode {
				r.trades[i].QuoteAsset = asset.Code
			}
		}
		for key, instrument := range r.instruments {
			if instrument.BaseAsset == oldCode {
				instrument.BaseAsset = asset.Code
			}
			if instrument.QuoteAsset == oldCode {
				instrument.QuoteAsset = asset.Code
			}
			r.instruments[key] = instrument
		}
		for key, quote := range r.quotes {
			if quote.BaseAsset == oldCode {
				quote.BaseAsset = asset.Code
			}
			if quote.QuoteAsset == oldCode {
				quote.QuoteAsset = asset.Code
			}
			r.quotes[key] = quote
		}
	}
	r.assets[asset.Code] = asset
	return asset, nil
}

func (r *memoryRealm) DeleteAsset(_ context.Context, code string, force bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.assets[code]; !ok {
		return domain.ErrNotFound
	}
	if dependents := r.assetCurrencyDependents(code); len(dependents) > 0 {
		return domain.NewHasDependentsError(dependents)
	}
	if !force {
		if dependents := r.assetDependents(code); len(dependents) > 0 {
			return domain.NewHasDependentsError(dependents)
		}
	}
	r.cascadeDeleteAsset(code)
	return nil
}

func (r *memoryRealm) assetCurrencyDependents(code string) []domain.DependentCount {
	var accounts, groups int
	for _, account := range r.accounts {
		if account.Currency == code {
			accounts++
		}
	}
	for _, group := range r.groups {
		if group.Currency == code {
			groups++
		}
	}
	return memoryDependents(
		[]memoryDependentCount{
			{kind: "account_currency", count: accounts},
			{kind: "account_group_currency", count: groups},
		},
	)
}

func (r *memoryRealm) assetDependents(code string) []domain.DependentCount {
	var balances, adjustments, rateLimits, orderSizeLimits, pnlBounds int
	var orders, trades, instruments int
	for _, balance := range r.balances {
		if balance.Asset == code {
			balances++
		}
	}
	for _, adjustment := range r.adjustments {
		if adjustment.Asset == code {
			adjustments++
		}
	}
	for _, limit := range r.rateLimits {
		if limit.Asset == code {
			rateLimits++
		}
	}
	for _, limit := range r.orderSizeLimits {
		if limit.Asset == code {
			orderSizeLimits++
		}
	}
	for _, limit := range r.spotFundsPnlBoundsLimits {
		if limit.Currency == code {
			pnlBounds++
		}
	}
	for _, order := range r.orders {
		if order.BaseAsset == code || order.QuoteAsset == code {
			orders++
		}
	}
	for _, trade := range r.trades {
		if trade.BaseAsset == code || trade.QuoteAsset == code {
			trades++
		}
	}
	for _, instrument := range r.instruments {
		if instrument.BaseAsset == code || instrument.QuoteAsset == code {
			instruments++
		}
	}
	return memoryDependents(
		[]memoryDependentCount{
			{kind: "balance", count: balances},
			{kind: "adjustment", count: adjustments},
			{kind: "limit_rate", count: rateLimits},
			{kind: "limit_order_size", count: orderSizeLimits},
			{kind: "limit_spot_funds_pnl_bound", count: pnlBounds},
			{kind: "order_record", count: orders},
			{kind: "trade", count: trades},
			{kind: "market_data_instrument", count: instruments},
		},
	)
}

type memoryDependentCount struct {
	kind  string
	count int
}

func memoryDependents(counts []memoryDependentCount) []domain.DependentCount {
	dependents := make([]domain.DependentCount, 0, len(counts))
	for _, dependent := range counts {
		if dependent.count > 0 {
			dependents = append(dependents, domain.DependentCount{
				Kind: dependent.kind, Count: dependent.count,
			})
		}
	}
	return dependents
}

func (r *memoryRealm) cascadeDeleteAsset(code string) {
	delete(r.assets, code)
	for key, balance := range r.balances {
		if balance.Asset == code {
			delete(r.balances, key)
		}
	}
	for key, limit := range r.rateLimits {
		if limit.Asset == code {
			delete(r.rateLimits, key)
		}
	}
	for key, limit := range r.orderSizeLimits {
		if limit.Asset == code {
			delete(r.orderSizeLimits, key)
		}
	}
	for key, limit := range r.spotFundsPnlBoundsLimits {
		if limit.Currency == code {
			delete(r.spotFundsPnlBoundsLimits, key)
		}
	}
	r.adjustments = deleteMemoryAssetAdjustments(r.adjustments, code)

	deletedOrders := make(map[domain.ExternalID]struct{})
	for id, order := range r.orders {
		if order.BaseAsset == code || order.QuoteAsset == code {
			delete(r.orders, id)
			deletedOrders[id] = struct{}{}
		}
	}
	r.trades = deleteMemoryAssetTrades(r.trades, code, deletedOrders)
	retainedEvents := r.events[:0]
	for _, event := range r.events {
		if _, deleted := deletedOrders[event.Order]; deleted {
			delete(r.attestations, event.ExternalID)
			continue
		}
		retainedEvents = append(retainedEvents, event)
	}
	r.events = retainedEvents
	for reportID, orderID := range r.reports {
		if _, deleted := deletedOrders[orderID]; deleted {
			delete(r.reports, reportID)
		}
	}

	for key, instrument := range r.instruments {
		if instrument.BaseAsset == code || instrument.QuoteAsset == code {
			delete(r.instruments, key)
			delete(r.quotes, key)
		}
	}
}

func deleteMemoryAssetAdjustments(
	adjustments []domain.AccountAdjustmentRecord, asset string,
) []domain.AccountAdjustmentRecord {
	retained := adjustments[:0]
	for _, adjustment := range adjustments {
		if adjustment.Asset != asset {
			retained = append(retained, adjustment)
		}
	}
	return retained
}

func deleteMemoryAssetTrades(
	trades []domain.Trade,
	asset string,
	deletedOrders map[domain.ExternalID]struct{},
) []domain.Trade {
	retained := trades[:0]
	for _, trade := range trades {
		_, deletedOrder := deletedOrders[trade.Order]
		if trade.BaseAsset != asset && trade.QuoteAsset != asset && !deletedOrder {
			retained = append(retained, trade)
		}
	}
	return retained
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
