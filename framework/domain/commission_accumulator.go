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

package domain

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// CommissionAccumulator totals recorded commissions by their own currencies.
// The zero value is ready to use. It never converts currencies or recomputes
// a commission from trade quantities or prices.
type CommissionAccumulator struct{ amounts map[string]decimal.Decimal }

// Add includes a complete recorded commission. An absent or one-sided record
// contributes nothing, as it does in the stored order's commission rollup.
func (a *CommissionAccumulator) Add(commission *Commission) error {
	if commission == nil || commission.Amount == "" || commission.Currency == "" {
		return nil
	}
	amount, err := decimal.NewFromString(commission.Amount)
	if err != nil {
		return fmt.Errorf("parse commission amount %q: %w", commission.Amount, err)
	}
	if a.amounts == nil {
		a.amounts = make(map[string]decimal.Decimal)
	}
	a.amounts[commission.Currency] = a.amounts[commission.Currency].Add(amount)
	return nil
}

// AddEvent includes only the canonical fee-only report event. A report with a
// fill is already represented by a trade; counting its event would count twice.
func (a *CommissionAccumulator) AddEvent(eventType OrderEventType, payload OrderEventPayload) error {
	report := payload.ExecutionReport
	if report == nil || report.Commission == nil || report.FillQuantity != "" || report.FillPrice != "" {
		return nil
	}
	canonical, ok := ExecutionReportStatusChangeEvent(report.OrderStatus)
	if !ok && (report.OrderStatus == OrderStatusFilled || report.OrderStatus == OrderStatusPartiallyFilled) {
		canonical, ok = OrderEventCommission, true
	}
	if !ok || eventType != canonical {
		return nil
	}
	return a.Add(report.Commission)
}

// Subtotals returns an independent currency-ordered result, including recorded
// zero totals and an empty slice when no complete commission was recorded.
func (a *CommissionAccumulator) Subtotals() []Commission {
	currencies := make([]string, 0, len(a.amounts))
	for currency := range a.amounts {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	result := make([]Commission, 0, len(currencies))
	for _, currency := range currencies {
		result = append(result, Commission{Currency: currency, Amount: a.amounts[currency].String()})
	}
	return result
}
