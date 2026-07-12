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
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func nodePageForMerge(page store.PageSpec) store.PageSpec {
	if page.Limit <= 0 {
		return store.PageSpec{}
	}
	return store.PageSpec{Limit: max(page.Offset, 0) + page.Limit}
}

func pageAccountRows(
	rows []store.AccountListRow, page store.PageSpec,
) []store.AccountListRow {
	if page.Limit <= 0 {
		return rows
	}
	start := min(max(page.Offset, 0), len(rows))
	end := min(start+page.Limit, len(rows))
	return rows[start:end]
}

func pageBalanceRows(
	rows []store.BalanceListRow, page store.PageSpec,
) []store.BalanceListRow {
	if page.Limit <= 0 {
		return rows
	}
	start := min(max(page.Offset, 0), len(rows))
	end := min(start+page.Limit, len(rows))
	return rows[start:end]
}

func pageOrderRows(rows []store.OrderListRow, page store.PageSpec) []store.OrderListRow {
	if page.Limit <= 0 {
		return rows
	}
	start := min(max(page.Offset, 0), len(rows))
	end := min(start+page.Limit, len(rows))
	return rows[start:end]
}

func pageAdjustmentRows(
	rows []domain.AccountAdjustmentRecord, page store.PageSpec,
) []domain.AccountAdjustmentRecord {
	if page.Limit <= 0 {
		return rows
	}
	start := min(max(page.Offset, 0), len(rows))
	end := min(start+page.Limit, len(rows))
	return rows[start:end]
}

func pageAuditRows(rows []domain.AuditRow, page store.PageSpec) []domain.AuditRow {
	if page.Limit <= 0 {
		return rows
	}
	start := min(max(page.Offset, 0), len(rows))
	end := min(start+page.Limit, len(rows))
	return rows[start:end]
}

func pageTradeRows(rows []domain.Trade, page store.PageSpec) []domain.Trade {
	if page.Limit <= 0 {
		return rows
	}
	start := min(max(page.Offset, 0), len(rows))
	end := min(start+page.Limit, len(rows))
	return rows[start:end]
}

func sortAccountRows(rows []store.AccountListRow, spec store.SortSpec) {
	desc := spec.Descending
	column := spec.Column
	if column == "" {
		column = "code"
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		cmp := 0
		switch column {
		case "blockReason":
			cmp = strings.Compare(left.Account.BlockReason, right.Account.BlockReason)
		case "group":
			cmp = strings.Compare(left.Account.GroupCode, right.Account.GroupCode)
		case "positionCount":
			cmp = left.PositionCount - right.PositionCount
		case "status":
			cmp = boolCompare(left.Account.Blocked, right.Account.Blocked)
		case "title":
			cmp = strings.Compare(left.Account.Title, right.Account.Title)
		case "code":
			cmp = strings.Compare(left.Account.Code.String(), right.Account.Code.String())
		default:
			cmp = strings.Compare(left.Account.Code.String(), right.Account.Code.String())
		}
		if cmp == 0 {
			cmp = strings.Compare(left.Account.Code.String(), right.Account.Code.String())
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
}

func sortBalanceRows(rows []store.BalanceListRow, spec store.SortSpec) {
	desc := spec.Descending
	column := spec.Column
	if column == "" {
		column = "account"
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i].Balance, rows[j].Balance
		cmp := 0
		switch column {
		case "asset":
			cmp = strings.Compare(left.Asset, right.Asset)
		case "available":
			cmp = decimalStringCompare(left.Available, right.Available)
		case "held":
			cmp = decimalStringCompare(left.Held, right.Held)
		case "incoming":
			cmp = decimalStringCompare(left.Incoming, right.Incoming)
		case "averageEntryPrice":
			cmp = decimalStringCompare(left.AverageEntryPrice, right.AverageEntryPrice)
		case "realizedPnl":
			cmp = decimalStringCompare(left.RealizedPnl, right.RealizedPnl)
		case "updatedAt":
			cmp = timeCompare(left.UpdatedAt, right.UpdatedAt)
		case "account":
			cmp = strings.Compare(left.Account.String(), right.Account.String())
		default:
			cmp = strings.Compare(left.Account.String(), right.Account.String())
		}
		if cmp == 0 {
			cmp = strings.Compare(left.Account.String(), right.Account.String())
		}
		if cmp == 0 {
			cmp = strings.Compare(left.Asset, right.Asset)
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
}

func sortOrderRows(rows []store.OrderListRow, spec store.SortSpec) {
	desc := spec.Descending
	column := spec.Column
	if column == "" {
		column = "at"
		desc = true
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i].Order, rows[j].Order
		cmp := 0
		switch column {
		case "account":
			cmp = strings.Compare(left.Account.String(), right.Account.String())
		case "amountValue":
			cmp = decimalStringCompare(left.AmountValue, right.AmountValue)
		case "baseAsset":
			cmp = strings.Compare(left.BaseAsset, right.BaseAsset)
		case "price":
			cmp = decimalStringCompare(left.Price, right.Price)
		case "quoteAsset":
			cmp = strings.Compare(left.QuoteAsset, right.QuoteAsset)
		case "side":
			cmp = strings.Compare(string(left.Side), string(right.Side))
		case "source":
			cmp = strings.Compare(string(left.Source), string(right.Source))
		case "status":
			cmp = strings.Compare(string(left.Status), string(right.Status))
		case "at":
			cmp = timeCompare(left.At, right.At)
		default:
			cmp = timeCompare(left.At, right.At)
		}
		if cmp == 0 {
			cmp = strings.Compare(left.ExternalID.String(), right.ExternalID.String())
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
}

func pagePolicyRows(rows []store.PolicyListRow, page store.PageSpec) []store.PolicyListRow {
	if page.Limit <= 0 {
		return rows
	}
	start := min(max(page.Offset, 0), len(rows))
	end := min(start+page.Limit, len(rows))
	return rows[start:end]
}

// sortPolicyRows orders the merged policy rows under the same total order as the
// connector's ORDER BY: the selected column first, then the barrier composite as
// the deterministic tiebreak. The composite is unique across the union, so
// single-node and cross-shard listings agree.
func sortPolicyRows(rows []store.PolicyListRow, spec store.SortSpec) {
	desc := spec.Descending
	column := spec.Column
	if column == "" {
		column = "policy"
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		cmp := 0
		switch column {
		case "scope":
			cmp = strings.Compare(left.Scope, right.Scope)
		case "account":
			cmp = strings.Compare(left.Account.String(), right.Account.String())
		case "asset":
			cmp = strings.Compare(left.Asset, right.Asset)
		case "accountGroup":
			cmp = strings.Compare(left.AccountGroup, right.AccountGroup)
		case "initialPnl":
			cmp = decimalStringCompare(policyInitialPnl(left), policyInitialPnl(right))
		case "lowerBound":
			cmp = decimalStringCompare(policyLowerBound(left), policyLowerBound(right))
		case "maxNotional":
			cmp = decimalStringCompare(policyMaxNotional(left), policyMaxNotional(right))
		case "maxOrders":
			cmp = decimalStringCompare(policyMaxOrders(left), policyMaxOrders(right))
		case "maxQuantity":
			cmp = decimalStringCompare(policyMaxQuantity(left), policyMaxQuantity(right))
		case "policy":
			cmp = strings.Compare(string(left.Kind), string(right.Kind))
		case "upperBound":
			cmp = decimalStringCompare(policyUpperBound(left), policyUpperBound(right))
		default:
			cmp = strings.Compare(string(left.Kind), string(right.Kind))
		}
		if cmp == 0 {
			cmp = policyCompositeCompare(left, right)
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
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
	switch {
	case row.SpotFundsPnlBounds != nil:
		return row.SpotFundsPnlBounds.LowerBound
	default:
		return ""
	}
}

func policyUpperBound(row store.PolicyListRow) string {
	switch {
	case row.SpotFundsPnlBounds != nil:
		return row.SpotFundsPnlBounds.UpperBound
	default:
		return ""
	}
}

func policyInitialPnl(row store.PolicyListRow) string {
	if row.SpotFundsPnlBounds != nil {
		return row.SpotFundsPnlBounds.InitialPnl
	}
	return ""
}

// policyCompositeCompare orders two policy rows by their unique composite, the
// tiebreak that mirrors the connector's ORDER BY.
func policyCompositeCompare(left, right store.PolicyListRow) int {
	if cmp := strings.Compare(string(left.Kind), string(right.Kind)); cmp != 0 {
		return cmp
	}
	if cmp := strings.Compare(left.Scope, right.Scope); cmp != 0 {
		return cmp
	}
	if cmp := strings.Compare(left.Account.String(), right.Account.String()); cmp != 0 {
		return cmp
	}
	if cmp := strings.Compare(left.AccountGroup, right.AccountGroup); cmp != 0 {
		return cmp
	}
	if cmp := strings.Compare(left.Asset, right.Asset); cmp != 0 {
		return cmp
	}
	return 0
}

func boolCompare(left, right bool) int {
	switch {
	case left == right:
		return 0
	case left:
		return 1
	default:
		return -1
	}
}

func timeCompare(left, right time.Time) int {
	switch {
	case left.Before(right):
		return -1
	case left.After(right):
		return 1
	default:
		return 0
	}
}

// decimalStringCompare orders two decimal strings for the cross-node merge under
// the same total order as the connector's DECIMAL collation, so single-node and
// multi-node listings agree: the empty string (an unset price or average entry
// price) sorts below every number, two parseable decimals compare numerically,
// and any other text is pushed above numbers with a byte-order tie-break.
func decimalStringCompare(left, right string) int {
	leftEmpty, rightEmpty := left == "", right == ""
	switch {
	case leftEmpty && rightEmpty:
		return 0
	case leftEmpty:
		return -1
	case rightEmpty:
		return 1
	}
	leftD, leftErr := decimal.NewFromString(left)
	rightD, rightErr := decimal.NewFromString(right)
	switch {
	case leftErr == nil && rightErr == nil:
		return leftD.Cmp(rightD)
	case leftErr != nil && rightErr != nil:
		return strings.Compare(left, right)
	case leftErr != nil:
		return 1
	default:
		return -1
	}
}
