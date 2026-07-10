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
	"sort"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func (r *memoryRealm) ListPolicyRows(
	_ context.Context, filter store.PolicyListFilter,
) (store.PolicyListPage, error) {
	wantKind := func(kind store.PolicyKind) bool {
		return filter.Kind == nil || *filter.Kind == kind
	}
	out := make([]store.PolicyListRow, 0)
	if wantKind(store.PolicyKindRate) {
		for _, limit := range r.rateLimits {
			if !textMatches(filter.Account, string(limit.Account)) ||
				!textMatches(filter.Asset, limit.Asset) {
				continue
			}
			value := limit
			out = append(out, store.PolicyListRow{
				Kind:    store.PolicyKindRate,
				Scope:   limit.Scope,
				Account: limit.Account,
				Asset:   limit.Asset,
				Rate:    &value,
			})
		}
	}
	if wantKind(store.PolicyKindOrderSize) {
		for _, limit := range r.orderSizeLimits {
			if !textMatches(filter.Account, string(limit.Account)) ||
				!textMatches(filter.Asset, limit.Asset) {
				continue
			}
			value := limit
			out = append(out, store.PolicyListRow{
				Kind:      store.PolicyKindOrderSize,
				Scope:     limit.Scope,
				Account:   limit.Account,
				Asset:     limit.Asset,
				OrderSize: &value,
			})
		}
	}
	if wantKind(store.PolicyKindSpotFundsPnlBounds) {
		for _, limit := range r.spotFundsPnlBoundsLimits {
			if !textMatches(filter.Account, string(limit.Account)) {
				continue
			}
			value := limit
			out = append(out, store.PolicyListRow{
				Kind:               store.PolicyKindSpotFundsPnlBounds,
				Scope:              limit.Scope,
				Account:            limit.Account,
				AccountGroup:       limit.AccountGroup,
				AccountCurrency:    limit.AccountCurrency,
				SpotFundsPnlBounds: &value,
			})
		}
	}
	sortPolicyRows(out, filter.Sort)
	total := len(out)
	out = pageRows(out, filter.Page)
	return store.PolicyListPage{Rows: out, Total: total}, nil
}

// policyRowKey returns the deterministic composite sort key matching the
// connector's default ORDER BY (kind, scope, account, asset).

func sortPolicyRows(rows []store.PolicyListRow, spec store.SortSpec) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		cmp := 0
		switch spec.Column {
		case "account":
			cmp = compareStrings(string(left.Account), string(right.Account))
		case "accountCurrency":
			cmp = compareStrings(left.AccountCurrency, right.AccountCurrency)
		case "accountGroup":
			cmp = compareStrings(left.AccountGroup, right.AccountGroup)
		case "asset":
			cmp = compareStrings(left.Asset, right.Asset)
		case "initialPnl":
			cmp = compareDecimalText(policyInitialPnl(left), policyInitialPnl(right))
		case "lowerBound":
			cmp = compareDecimalText(policyLowerBound(left), policyLowerBound(right))
		case "maxNotional":
			cmp = compareDecimalText(policyMaxNotional(left), policyMaxNotional(right))
		case "maxOrders":
			cmp = compareDecimalText(policyMaxOrders(left), policyMaxOrders(right))
		case "maxQuantity":
			cmp = compareDecimalText(policyMaxQuantity(left), policyMaxQuantity(right))
		case "scope":
			cmp = compareStrings(left.Scope, right.Scope)
		case "upperBound":
			cmp = compareDecimalText(policyUpperBound(left), policyUpperBound(right))
		default:
			cmp = compareStrings(string(left.Kind), string(right.Kind))
		}
		if cmp == 0 {
			cmp = compareStrings(policyRowKey(left), policyRowKey(right))
		}
		return sortCompare(cmp, spec.Descending)
	})
}

func policyMaxOrders(row store.PolicyListRow) string {
	if row.Rate == nil {
		return ""
	}
	return fmt.Sprintf("%d", row.Rate.MaxOrders)
}

func policyMaxQuantity(row store.PolicyListRow) string {
	if row.OrderSize == nil {
		return ""
	}
	return row.OrderSize.MaxQuantity
}

func policyMaxNotional(row store.PolicyListRow) string {
	if row.OrderSize == nil {
		return ""
	}
	return row.OrderSize.MaxNotional
}

func policyLowerBound(row store.PolicyListRow) string {
	if row.SpotFundsPnlBounds != nil {
		return row.SpotFundsPnlBounds.LowerBound
	}
	return ""
}

func policyUpperBound(row store.PolicyListRow) string {
	if row.SpotFundsPnlBounds != nil {
		return row.SpotFundsPnlBounds.UpperBound
	}
	return ""
}

func policyInitialPnl(row store.PolicyListRow) string {
	if row.SpotFundsPnlBounds != nil {
		return row.SpotFundsPnlBounds.InitialPnl
	}
	return ""
}

func (r *memoryRealm) ListRateLimits(
	_ context.Context, account domain.AccountID,
) ([]domain.LimitRate, error) {
	var out []domain.LimitRate
	for _, limit := range r.rateLimits {
		if account == "" || limit.Account == account {
			out = append(out, limit)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return limitKey(out[i].Scope, out[i].Account, out[i].Asset) <
			limitKey(out[j].Scope, out[j].Account, out[j].Asset)
	})
	return out, nil
}

func (r *memoryRealm) PutRateLimit(_ context.Context, limit domain.LimitRate) error {
	r.rateLimits[limitKey(limit.Scope, limit.Account, limit.Asset)] = limit
	return nil
}

func (r *memoryRealm) DeleteRateLimit(
	_ context.Context, scope domain.LimitScope, account domain.AccountID, asset string,
) error {
	key := limitKey(scope, account, asset)
	if _, ok := r.rateLimits[key]; !ok {
		return domain.ErrNotFound
	}
	delete(r.rateLimits, key)
	return nil
}

func (r *memoryRealm) ListOrderSizeLimits(
	_ context.Context, account domain.AccountID,
) ([]domain.LimitOrderSize, error) {
	var out []domain.LimitOrderSize
	for _, limit := range r.orderSizeLimits {
		if account == "" || limit.Account == account {
			out = append(out, limit)
		}
	}
	return out, nil
}

func (r *memoryRealm) PutOrderSizeLimit(
	_ context.Context, limit domain.LimitOrderSize,
) error {
	r.orderSizeLimits[limitKey(limit.Scope, limit.Account, limit.Asset)] = limit
	return nil
}

func (r *memoryRealm) DeleteOrderSizeLimit(
	_ context.Context, scope domain.LimitScope, account domain.AccountID, asset string,
) error {
	key := limitKey(scope, account, asset)
	if _, ok := r.orderSizeLimits[key]; !ok {
		return domain.ErrNotFound
	}
	delete(r.orderSizeLimits, key)
	return nil
}

func (r *memoryRealm) ListSpotFundsPnlBoundsLimits(
	_ context.Context, account domain.AccountID,
) ([]domain.LimitSpotFundsPnlBounds, error) {
	var out []domain.LimitSpotFundsPnlBounds
	for _, limit := range r.spotFundsPnlBoundsLimits {
		if account == "" || limit.Account == account {
			out = append(out, limit)
		}
	}
	return out, nil
}

func (r *memoryRealm) PutSpotFundsPnlBoundsLimit(
	_ context.Context, limit domain.LimitSpotFundsPnlBounds,
) error {
	r.spotFundsPnlBoundsLimits[spotFundsPnlBoundsLimitKey(
		limit.Scope,
		limit.Account,
		limit.AccountGroup,
		limit.AccountCurrency,
	)] = limit
	return nil
}

func (r *memoryRealm) DeleteSpotFundsPnlBoundsLimit(
	_ context.Context,
	scope domain.LimitScope,
	account domain.AccountID,
	accountGroup string,
	accountCurrency string,
) error {
	key := spotFundsPnlBoundsLimitKey(scope, account, accountGroup, accountCurrency)
	if _, ok := r.spotFundsPnlBoundsLimits[key]; !ok {
		return domain.ErrNotFound
	}
	delete(r.spotFundsPnlBoundsLimits, key)
	return nil
}
