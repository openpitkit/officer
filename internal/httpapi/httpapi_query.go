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

package httpapi

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func textMatcherFromQuery(q url.Values, valueKey, modeKey string) (
	store.TextMatcher, error,
) {
	value := q.Get(valueKey)
	if value == "" {
		return store.TextMatcher{}, nil
	}
	mode := q.Get(modeKey)
	matcher := store.TextMatcher{Fragments: strings.Split(value, "*")}
	switch mode {
	case "", "contains":
	case "starts_with":
		matcher.AnchorStart = true
	case "ends_with":
		matcher.AnchorEnd = true
	case "exact":
		matcher.AnchorStart = true
		matcher.AnchorEnd = true
	default:
		return store.TextMatcher{}, fmt.Errorf("invalid %s", modeKey)
	}
	return matcher, nil
}

func externalIDFromQuery(q url.Values) (domain.ExternalID, error) {
	value := q.Get("id")
	if value == "" {
		value = q.Get("externalId")
	}
	if value == "" {
		return domain.ExternalID(""), nil
	}
	return domain.ParseExternalID(value)
}

func statusFilterFromQuery(q url.Values) (store.StatusFilter, error) {
	switch q.Get("status") {
	case "", "all":
		return store.StatusFilterAll, nil
	case "active":
		return store.StatusFilterActive, nil
	case "blocked":
		return store.StatusFilterBlocked, nil
	default:
		return store.StatusFilterAll, fmt.Errorf("invalid status")
	}
}

func accountCountRangeFromQuery(q url.Values) (store.CountRangeFilter, error) {
	return countRangeFromQuery(
		q, "accountCountMode", "accountCountMin", "accountCountMax",
	)
}

func countRangeFilterFromQuery(q url.Values) (store.CountRangeFilter, error) {
	return countRangeFromQuery(q, "positionCountMode", "positionCountMin", "positionCountMax")
}

func countRangeFromQuery(
	q url.Values, modeKey string, minKey string, maxKey string,
) (store.CountRangeFilter, error) {
	switch q.Get(modeKey) {
	case "", "all":
		return store.CountRangeFilter{}, nil
	case "eq", "equal", "equals", "exact":
		value, err := nonNegativeIntFromQuery(q, minKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		return store.CountRangeFilter{Equal: &value}, nil
	case "neq", "not_equal", "not_equals":
		value, err := nonNegativeIntFromQuery(q, minKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		return store.CountRangeFilter{NotEqual: &value}, nil
	case "gt", "greater_than":
		value, err := nonNegativeIntFromQuery(q, minKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		return store.CountRangeFilter{Min: &value, MinExclusive: true}, nil
	case "gte":
		value, err := nonNegativeIntFromQuery(q, minKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		return store.CountRangeFilter{Min: &value}, nil
	case "lt", "less_than":
		value, err := nonNegativeIntFromQuery(q, maxKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		return store.CountRangeFilter{Max: &value, MaxExclusive: true}, nil
	case "lte":
		value, err := nonNegativeIntFromQuery(q, maxKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		return store.CountRangeFilter{Max: &value}, nil
	case "between":
		minValue, err := nonNegativeIntFromQuery(q, minKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		maxValue, err := nonNegativeIntFromQuery(q, maxKey)
		if err != nil {
			return store.CountRangeFilter{}, err
		}
		if minValue > maxValue {
			return store.CountRangeFilter{}, fmt.Errorf("invalid %s range", modeKey)
		}
		return store.CountRangeFilter{Min: &minValue, Max: &maxValue}, nil
	default:
		return store.CountRangeFilter{}, fmt.Errorf("invalid %s", modeKey)
	}
}

func nonNegativeIntFromQuery(q url.Values, key string) (int, error) {
	raw := q.Get(key)
	if raw == "" {
		return 0, fmt.Errorf("missing %s", key)
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return value, nil
}

func pageSpecFromQuery(q url.Values) (store.PageSpec, error) {
	limit := listDefaultLimit
	if raw := q.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return store.PageSpec{}, fmt.Errorf("invalid limit")
		}
		if value > listCapREST {
			value = listCapREST
		}
		limit = value
	}
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return store.PageSpec{}, fmt.Errorf("invalid offset")
		}
		offset = value
	} else if raw := q.Get("page"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return store.PageSpec{}, fmt.Errorf("invalid page")
		}
		if value-1 > math.MaxInt/limit {
			return store.PageSpec{}, fmt.Errorf("invalid page")
		}
		offset = (value - 1) * limit
	}
	return store.PageSpec{Limit: limit, Offset: offset}, nil
}

func sortSpecFromQuery(q url.Values, allowed map[string]struct{}) (store.SortSpec, error) {
	column := q.Get("sort")
	order := q.Get("order")
	if column == "" {
		if order != "" && order != "asc" && order != "desc" {
			return store.SortSpec{}, fmt.Errorf("invalid order")
		}
		return store.SortSpec{}, nil
	}
	if _, ok := allowed[column]; !ok {
		return store.SortSpec{}, fmt.Errorf("invalid sort")
	}
	switch order {
	case "none":
		return store.SortSpec{}, nil
	case "", "asc":
		return store.SortSpec{Column: column}, nil
	case "desc":
		return store.SortSpec{Column: column, Descending: true}, nil
	default:
		return store.SortSpec{}, fmt.Errorf("invalid order")
	}
}

func decimalRangeFromQuery(
	q url.Values,
	modeKey string,
	minKey string,
	maxKey string,
) (store.DecimalRangeFilter, error) {
	switch q.Get(modeKey) {
	case "", "all":
		return store.DecimalRangeFilter{}, nil
	case "eq", "equal", "equals", "exact":
		value, _, err := decimalBoundFromQuery(q, minKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		return store.DecimalRangeFilter{Equal: &value}, nil
	case "neq", "not_equal", "not_equals":
		value, _, err := decimalBoundFromQuery(q, minKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		return store.DecimalRangeFilter{NotEqual: &value}, nil
	case "gt", "greater_than":
		value, _, err := decimalBoundFromQuery(q, minKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		return store.DecimalRangeFilter{Min: &value, MinExclusive: true}, nil
	case "gte":
		value, _, err := decimalBoundFromQuery(q, minKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		return store.DecimalRangeFilter{Min: &value}, nil
	case "lt", "less_than":
		value, _, err := decimalBoundFromQuery(q, maxKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		return store.DecimalRangeFilter{Max: &value, MaxExclusive: true}, nil
	case "lte":
		value, _, err := decimalBoundFromQuery(q, maxKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		return store.DecimalRangeFilter{Max: &value}, nil
	case "between":
		minValue, minDec, err := decimalBoundFromQuery(q, minKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		maxValue, maxDec, err := decimalBoundFromQuery(q, maxKey)
		if err != nil {
			return store.DecimalRangeFilter{}, err
		}
		if minDec.GreaterThan(maxDec) {
			return store.DecimalRangeFilter{}, fmt.Errorf("invalid %s range", modeKey)
		}
		return store.DecimalRangeFilter{Min: &minValue, Max: &maxValue}, nil
	default:
		return store.DecimalRangeFilter{}, fmt.Errorf("invalid %s", modeKey)
	}
}

// decimalBoundFromQuery reads a decimal range bound, validating it is a real
// decimal and returning the plain string for the filter (the store compares it
// numerically through the column's DECIMAL collation) alongside the parsed value
// for an order check.
func decimalBoundFromQuery(
	q url.Values, key string,
) (string, decimal.Decimal, error) {
	raw := q.Get(key)
	if raw == "" {
		return "", decimal.Decimal{}, fmt.Errorf("missing %s", key)
	}
	value, err := decimal.NewFromString(raw)
	if err != nil {
		return "", decimal.Decimal{}, fmt.Errorf("invalid %s", key)
	}
	return raw, value, nil
}

func timeRangeFromQuery(
	q url.Values,
	modeKey string,
	minKey string,
	maxKey string,
) (store.TimeRangeFilter, error) {
	switch q.Get(modeKey) {
	case "", "all":
		return store.TimeRangeFilter{}, nil
	case "gt", "after", "greater_than":
		value, err := timeFromQuery(q, minKey)
		if err != nil {
			return store.TimeRangeFilter{}, err
		}
		return store.TimeRangeFilter{Min: &value, MinExclusive: true}, nil
	case "gte", "on_or_after":
		value, err := timeFromQuery(q, minKey)
		if err != nil {
			return store.TimeRangeFilter{}, err
		}
		return store.TimeRangeFilter{Min: &value}, nil
	case "lt", "before", "less_than":
		value, err := timeFromQuery(q, maxKey)
		if err != nil {
			return store.TimeRangeFilter{}, err
		}
		return store.TimeRangeFilter{Max: &value, MaxExclusive: true}, nil
	case "lte", "on_or_before":
		value, err := timeFromQuery(q, maxKey)
		if err != nil {
			return store.TimeRangeFilter{}, err
		}
		return store.TimeRangeFilter{Max: &value}, nil
	case "between":
		minValue, err := timeFromQuery(q, minKey)
		if err != nil {
			return store.TimeRangeFilter{}, err
		}
		maxValue, err := timeFromQuery(q, maxKey)
		if err != nil {
			return store.TimeRangeFilter{}, err
		}
		if minValue.After(maxValue) {
			return store.TimeRangeFilter{}, fmt.Errorf("invalid %s range", modeKey)
		}
		return store.TimeRangeFilter{Min: &minValue, Max: &maxValue}, nil
	default:
		return store.TimeRangeFilter{}, fmt.Errorf("invalid %s", modeKey)
	}
}

func timeFromQuery(q url.Values, key string) (time.Time, error) {
	raw := q.Get(key)
	if raw == "" {
		return time.Time{}, fmt.Errorf("missing %s", key)
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s", key)
	}
	return value.UTC(), nil
}

var accountSortKeys = map[string]struct{}{
	"blockReason":   {},
	"code":          {},
	"group":         {},
	"positionCount": {},
	"status":        {},
	"title":         {},
}

var assetSortKeys = map[string]struct{}{
	"assetClass": {},
	"code":       {},
	"title":      {},
}

var orderSortKeys = map[string]struct{}{
	"account":     {},
	"amountValue": {},
	"at":          {},
	"baseAsset":   {},
	"price":       {},
	"quoteAsset":  {},
	"side":        {},
	"source":      {},
	"status":      {},
}

var groupSortKeys = map[string]struct{}{
	"accountCount":  {},
	"blockReason":   {},
	"code":          {},
	"notes":         {},
	"positionCount": {},
	"status":        {},
	"title":         {},
}

var assetClassSortKeys = map[string]struct{}{
	"assetCount": {},
	"code":       {},
	"title":      {},
}

var policySortKeys = map[string]struct{}{
	"account":     {},
	"asset":       {},
	"initialPnl":  {},
	"lowerBound":  {},
	"maxNotional": {},
	"maxOrders":   {},
	"maxQuantity": {},
	"policy":      {},
	"scope":       {},
	"upperBound":  {},
}

var balanceSortKeys = map[string]struct{}{
	"account":           {},
	"asset":             {},
	"available":         {},
	"averageEntryPrice": {},
	"held":              {},
	"incoming":          {},
	"realizedPnl":       {},
	"updatedAt":         {},
}

var adjustmentSortKeys = map[string]struct{}{
	"account":   {},
	"asset":     {},
	"at":        {},
	"principal": {},
	"source":    {},
	"status":    {},
}

var tradeSortKeys = map[string]struct{}{
	"account":    {},
	"at":         {},
	"baseAsset":  {},
	"lockPrice":  {},
	"price":      {},
	"quantity":   {},
	"quoteAsset": {},
	"side":       {},
	"source":     {},
}

func accountListFilterFromQuery(q url.Values) (store.AccountListFilter, error) {
	code, err := textMatcherFromQuery(q, "code", "codeMatch")
	if err != nil {
		return store.AccountListFilter{}, err
	}
	blockReason, err := textMatcherFromQuery(q, "blockReason", "blockReasonMatch")
	if err != nil {
		return store.AccountListFilter{}, err
	}
	status, err := statusFilterFromQuery(q)
	if err != nil {
		return store.AccountListFilter{}, err
	}
	position, err := countRangeFilterFromQuery(q)
	if err != nil {
		return store.AccountListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, accountSortKeys)
	if err != nil {
		return store.AccountListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.AccountListFilter{}, err
	}
	filter := store.AccountListFilter{
		Code:        code,
		BlockReason: blockReason,
		Status:      status,
		Position:    position,
		GroupCode:   nil,
		Sort:        sortSpec,
		Page:        page,
	}
	if values, ok := q["group"]; ok {
		group := ""
		if len(values) > 0 {
			group = values[0]
		}
		filter.GroupCode = &group
	}
	return filter, nil
}

func orderListFilterFromQuery(q url.Values) (store.OrderListFilter, error) {
	baseAsset := store.ExactTextMatcher(q.Get("baseAsset"))
	quoteAsset := store.ExactTextMatcher(q.Get("quoteAsset"))
	side, err := orderSideFromQuery(q)
	if err != nil {
		return store.OrderListFilter{}, err
	}
	status, err := orderStatusFromQuery(q)
	if err != nil {
		return store.OrderListFilter{}, err
	}
	amount, err := decimalRangeFromQuery(q, "amountMode", "amountMin", "amountMax")
	if err != nil {
		return store.OrderListFilter{}, err
	}
	price, err := decimalRangeFromQuery(q, "priceMode", "priceMin", "priceMax")
	if err != nil {
		return store.OrderListFilter{}, err
	}
	at, err := timeRangeFromQuery(q, "atMode", "atMin", "atMax")
	if err != nil {
		return store.OrderListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, orderSortKeys)
	if err != nil {
		return store.OrderListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.OrderListFilter{}, err
	}
	return store.OrderListFilter{
		Account:    domain.AccountID(q.Get("account")),
		Source:     domain.Source(q.Get("source")),
		Side:       side,
		Status:     status,
		BaseAsset:  baseAsset,
		QuoteAsset: quoteAsset,
		Amount:     amount,
		Price:      price,
		At:         at,
		Sort:       sortSpec,
		Page:       page,
	}, nil
}

func balanceListFilterFromQuery(q url.Values) (store.BalanceListFilter, error) {
	account := store.ExactTextMatcher(q.Get("account"))
	asset := store.ExactTextMatcher(q.Get("asset"))
	available, err := decimalRangeFromQuery(q, "availableMode", "availableMin", "availableMax")
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	held, err := decimalRangeFromQuery(q, "heldMode", "heldMin", "heldMax")
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	incoming, err := decimalRangeFromQuery(q, "incomingMode", "incomingMin", "incomingMax")
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	averageEntryPrice, err := decimalRangeFromQuery(
		q, "averageEntryPriceMode", "averageEntryPriceMin", "averageEntryPriceMax",
	)
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	realizedPnl, err := decimalRangeFromQuery(
		q, "realizedPnlMode", "realizedPnlMin", "realizedPnlMax",
	)
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	updatedAt, err := timeRangeFromQuery(q, "updatedAtMode", "updatedAfter", "updatedBefore")
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, balanceSortKeys)
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.BalanceListFilter{}, err
	}
	filter := store.BalanceListFilter{
		Account:           account,
		Asset:             asset,
		Available:         available,
		Held:              held,
		Incoming:          incoming,
		AverageEntryPrice: averageEntryPrice,
		RealizedPnl:       realizedPnl,
		UpdatedAt:         updatedAt,
		Sort:              sortSpec,
		Page:              page,
	}
	if values, ok := q["groupCode"]; ok {
		groupCode := ""
		if len(values) > 0 {
			groupCode = values[0]
		}
		filter.GroupCode = &groupCode
	}
	return filter, nil
}

func orderSideFromQuery(q url.Values) (*domain.OrderSide, error) {
	switch raw := q.Get("side"); raw {
	case "", "all":
		return nil, nil
	case string(domain.OrderSideBuy), string(domain.OrderSideSell):
		side := domain.OrderSide(raw)
		return &side, nil
	default:
		return nil, fmt.Errorf("invalid side")
	}
}

func sourceFromQuery(q url.Values) (domain.Source, error) {
	switch raw := q.Get("source"); raw {
	case "", "all":
		return "", nil
	default:
		return domain.Source(raw), nil
	}
}

func adjustmentStatusFromQuery(q url.Values) (*domain.AdjustmentStatus, error) {
	switch raw := q.Get("status"); raw {
	case "", "all":
		return nil, nil
	case string(domain.AdjustmentStatusAccepted), string(domain.AdjustmentStatusRejected):
		status := domain.AdjustmentStatus(raw)
		return &status, nil
	default:
		return nil, fmt.Errorf("invalid status")
	}
}

func orderStatusFromQuery(q url.Values) ([]domain.OrderStatus, error) {
	rawValues := q["status"]
	if len(rawValues) == 0 {
		return nil, nil
	}
	out := make([]domain.OrderStatus, 0, len(rawValues))
	for _, raw := range rawValues {
		for _, part := range strings.Split(raw, ",") {
			if part == "" || part == "all" {
				continue
			}
			status := domain.OrderStatus(part)
			if !validOrderStatus(status) {
				return nil, fmt.Errorf("invalid status")
			}
			out = append(out, status)
		}
	}
	return out, nil
}

func validOrderStatus(status domain.OrderStatus) bool {
	return domain.OrderStatusSupported(status)
}

func groupListFilterFromQuery(q url.Values) (store.GroupListFilter, error) {
	code, err := textMatcherFromQuery(q, "code", "codeMatch")
	if err != nil {
		return store.GroupListFilter{}, err
	}
	notes, err := textMatcherFromQuery(q, "notes", "notesMatch")
	if err != nil {
		return store.GroupListFilter{}, err
	}
	blockReason, err := textMatcherFromQuery(q, "blockReason", "blockReasonMatch")
	if err != nil {
		return store.GroupListFilter{}, err
	}
	status, err := statusFilterFromQuery(q)
	if err != nil {
		return store.GroupListFilter{}, err
	}
	position, err := countRangeFilterFromQuery(q)
	if err != nil {
		return store.GroupListFilter{}, err
	}
	account, err := accountCountRangeFromQuery(q)
	if err != nil {
		return store.GroupListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, groupSortKeys)
	if err != nil {
		return store.GroupListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.GroupListFilter{}, err
	}
	return store.GroupListFilter{
		Code:        code,
		Notes:       notes,
		BlockReason: blockReason,
		Status:      status,
		Position:    position,
		Account:     account,
		Sort:        sortSpec,
		Page:        page,
	}, nil
}

func assetClassListFilterFromQuery(q url.Values) (store.AssetClassListFilter, error) {
	code, err := textMatcherFromQuery(q, "code", "codeMatch")
	if err != nil {
		return store.AssetClassListFilter{}, err
	}
	notes, err := textMatcherFromQuery(q, "notes", "notesMatch")
	if err != nil {
		return store.AssetClassListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, assetClassSortKeys)
	if err != nil {
		return store.AssetClassListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.AssetClassListFilter{}, err
	}
	return store.AssetClassListFilter{
		Code:  code,
		Notes: notes,
		Sort:  sortSpec,
		Page:  page,
	}, nil
}

func assetListFilterFromQuery(q url.Values) (store.AssetListFilter, error) {
	code, err := textMatcherFromQuery(q, "code", "codeMatch")
	if err != nil {
		return store.AssetListFilter{}, err
	}
	class, err := textMatcherFromQuery(q, "class", "classMatch")
	if err != nil {
		return store.AssetListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, assetSortKeys)
	if err != nil {
		return store.AssetListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.AssetListFilter{}, err
	}
	return store.AssetListFilter{
		Code:  code,
		Class: class,
		Sort:  sortSpec,
		Page:  page,
	}, nil
}

// policyKindFromQuery maps the Limits UI policy selector to the store kind. The
// "all" value (and an absent param) leaves the kind unrestricted; any other
// value is rejected.
func policyKindFromQuery(q url.Values) (*store.PolicyKind, error) {
	switch q.Get("policy") {
	case "", "all":
		return nil, nil
	case "rate":
		kind := store.PolicyKindRate
		return &kind, nil
	case "order_size":
		kind := store.PolicyKindOrderSize
		return &kind, nil
	case "pnl_bounds":
		kind := store.PolicyKindPnlBounds
		return &kind, nil
	default:
		return nil, fmt.Errorf("invalid policy")
	}
}

func policyListFilterFromQuery(q url.Values) (store.PolicyListFilter, error) {
	account := store.ExactTextMatcher(q.Get("account"))
	asset := store.ExactTextMatcher(q.Get("asset"))
	kind, err := policyKindFromQuery(q)
	if err != nil {
		return store.PolicyListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, policySortKeys)
	if err != nil {
		return store.PolicyListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.PolicyListFilter{}, err
	}
	return store.PolicyListFilter{
		Account: account,
		Asset:   asset,
		Kind:    kind,
		Sort:    sortSpec,
		Page:    page,
	}, nil
}

func adjustmentListFilterFromQuery(q url.Values) (store.AdjustmentListFilter, error) {
	externalID, err := externalIDFromQuery(q)
	if err != nil {
		return store.AdjustmentListFilter{}, err
	}
	account := store.ExactTextMatcher(q.Get("account"))
	asset := store.ExactTextMatcher(q.Get("asset"))
	source, err := sourceFromQuery(q)
	if err != nil {
		return store.AdjustmentListFilter{}, err
	}
	status, err := adjustmentStatusFromQuery(q)
	if err != nil {
		return store.AdjustmentListFilter{}, err
	}
	at, err := timeRangeFromQuery(q, "atMode", "atMin", "atMax")
	if err != nil {
		return store.AdjustmentListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, adjustmentSortKeys)
	if err != nil {
		return store.AdjustmentListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.AdjustmentListFilter{}, err
	}
	return store.AdjustmentListFilter{
		Account:    account,
		Asset:      asset,
		ExternalID: externalID,
		Source:     source,
		Status:     status,
		At:         at,
		Sort:       sortSpec,
		Page:       page,
	}, nil
}

func tradeListFilterFromQuery(q url.Values) (store.TradeListFilter, error) {
	account := store.ExactTextMatcher(q.Get("account"))
	externalID, err := externalIDFromQuery(q)
	if err != nil {
		return store.TradeListFilter{}, err
	}
	baseAsset := store.ExactTextMatcher(q.Get("baseAsset"))
	quoteAsset := store.ExactTextMatcher(q.Get("quoteAsset"))
	side, err := orderSideFromQuery(q)
	if err != nil {
		return store.TradeListFilter{}, err
	}
	source, err := sourceFromQuery(q)
	if err != nil {
		return store.TradeListFilter{}, err
	}
	at, err := timeRangeFromQuery(q, "atMode", "atMin", "atMax")
	if err != nil {
		return store.TradeListFilter{}, err
	}
	quantity, err := decimalRangeFromQuery(q, "quantityMode", "quantityMin", "quantityMax")
	if err != nil {
		return store.TradeListFilter{}, err
	}
	price, err := decimalRangeFromQuery(q, "priceMode", "priceMin", "priceMax")
	if err != nil {
		return store.TradeListFilter{}, err
	}
	lockPrice, err := decimalRangeFromQuery(q, "lockPriceMode", "lockPriceMin", "lockPriceMax")
	if err != nil {
		return store.TradeListFilter{}, err
	}
	sortSpec, err := sortSpecFromQuery(q, tradeSortKeys)
	if err != nil {
		return store.TradeListFilter{}, err
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.TradeListFilter{}, err
	}
	return store.TradeListFilter{
		Account:    account,
		ExternalID: externalID,
		BaseAsset:  baseAsset,
		QuoteAsset: quoteAsset,
		Side:       side,
		Source:     source,
		At:         at,
		Quantity:   quantity,
		Price:      price,
		LockPrice:  lockPrice,
		Sort:       sortSpec,
		Page:       page,
	}, nil
}

func auditListFilterFromQuery(q url.Values) (store.AuditListFilter, error) {
	account := store.ExactTextMatcher(q.Get("account"))
	asset := store.ExactTextMatcher(q.Get("asset"))
	externalID, err := externalIDFromQuery(q)
	if err != nil {
		return store.AuditListFilter{}, err
	}
	actor, err := textMatcherFromQuery(q, "actor", "actorMatch")
	if err != nil {
		return store.AuditListFilter{}, err
	}
	source, err := sourceFromQuery(q)
	if err != nil {
		return store.AuditListFilter{}, err
	}
	actions, err := auditActionsFromQuery(q)
	if err != nil {
		return store.AuditListFilter{}, err
	}
	category, err := auditCategoryFromQuery(q, len(actions) > 0)
	if err != nil {
		return store.AuditListFilter{}, err
	}
	at, err := timeRangeFromQuery(q, "atMode", "atMin", "atMax")
	if err != nil {
		return store.AuditListFilter{}, err
	}
	if q.Get("sort") != "" || q.Get("order") != "" {
		return store.AuditListFilter{}, fmt.Errorf("audit sorting is not supported")
	}
	page, err := pageSpecFromQuery(q)
	if err != nil {
		return store.AuditListFilter{}, err
	}
	return store.AuditListFilter{
		Account:    account,
		Asset:      asset,
		ExternalID: externalID,
		Actor:      actor,
		Source:     source,
		Actions:    actions,
		Category:   category,
		At:         at,
		Page:       page,
	}, nil
}

// handleListAssets handles GET /api/v1/assets.
