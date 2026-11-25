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

package trading

import (
	"fmt"
	"slices"
	"sort"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/openalgo"
)

// errOpenAlgoFillsIncomplete keeps the order pending and retried until
// the tradebook catches up with the complete order.
var errOpenAlgoFillsIncomplete = fmt.Errorf(
	"openalgo: complete order fills are incomplete: %w", domain.ErrUpstream)

// errOpenAlgoCancelledFillsUnconfirmed keeps the order pending and retried.
// Only a row sum equal to a known terminal total clears it. The caller raises
// every occurrence to the operator: unknown totals or mismatches may never
// clear.
var errOpenAlgoCancelledFillsUnconfirmed = fmt.Errorf(
	"openalgo: cancelled order fills are unconfirmed: %w", domain.ErrUpstream)

var openAlgoClosedStatuses = []string{"cancelled", "rejected", "expired"}

// openAlgoSnapshot reports fills only at a final status.
// Complete totals must match the order quantity.
// Cancelled/rejected/expired need terminalFilled >= 0 equal to the row sum.
// Canonical price/quantity order replaces execution
// order because OpenAlgo's broker row order is unreliable. The sum of
// quantity * price is the same in either order.
func openAlgoSnapshot(status openalgo.OrderStatus, trades []openalgo.Trade,
	terminalFilled openalgo.Decimal,
) (fwtrading.Snapshot, error) {
	quantity, err := decimal.NewFromString(string(status.Quantity))
	if err != nil || !quantity.IsPositive() {
		return fwtrading.Snapshot{}, fmt.Errorf(
			"openalgo: invalid order quantity: %w", domain.ErrUpstream)
	}
	if status.OrderID == "" {
		return fwtrading.Snapshot{}, fmt.Errorf(
			"openalgo: missing order id: %w", domain.ErrUpstream)
	}
	snapshot := fwtrading.Snapshot{
		VenueOrderID: string(status.OrderID), FilledQuantity: "0",
	}
	switch {
	case status.Status == "open" || status.Status == "pending" || status.Status == "trigger pending":
		snapshot.Status = fwtrading.VenueStatusOpen
		return snapshot, nil
	case status.Status == "complete":
		snapshot.Status = fwtrading.VenueStatusFilled
	case slices.Contains(openAlgoClosedStatuses, status.Status):
		snapshot.Status = fwtrading.VenueStatusCancelled
		snapshot.Reason = status.Status
	default:
		return fwtrading.Snapshot{}, fmt.Errorf(
			"openalgo: unknown order status: %w", domain.ErrUpstream)
	}
	var executions []fillExecution
	for _, trade := range trades {
		if trade.OrderID != status.OrderID {
			continue
		}
		if trade.Symbol != status.Symbol || trade.Exchange != status.Exchange ||
			trade.Action != status.Action {
			return fwtrading.Snapshot{}, fmt.Errorf(
				"openalgo: trade identity does not match order: %w", domain.ErrUpstream)
		}
		qty, qtyErr := decimal.NewFromString(string(trade.Quantity))
		if qtyErr != nil || !qty.IsPositive() {
			return fwtrading.Snapshot{}, fmt.Errorf(
				"openalgo: invalid trade quantity: %w", domain.ErrUpstream)
		}
		price, priceErr := decimal.NewFromString(string(trade.AveragePrice))
		if priceErr != nil || !price.IsPositive() {
			return fwtrading.Snapshot{}, fmt.Errorf(
				"openalgo: invalid trade average price: %w", domain.ErrUpstream)
		}
		executions = append(executions, fillExecution{quantity: qty, price: price})
	}
	sort.Slice(executions, func(i, j int) bool {
		if cmp := executions[i].price.Cmp(executions[j].price); cmp != 0 {
			return cmp < 0
		}
		return executions[i].quantity.LessThan(executions[j].quantity)
	})
	fills, cumulative, ok := fillChain(executions, quantity, snapshot.Status)
	if !ok {
		return fwtrading.Snapshot{}, fmt.Errorf(
			"openalgo: trades exceed order quantity: %w", domain.ErrUpstream)
	}
	snapshot.Fills = fills
	if snapshot.Status == fwtrading.VenueStatusFilled {
		if !cumulative.Equal(quantity) {
			return fwtrading.Snapshot{}, errOpenAlgoFillsIncomplete
		}
	} else if terminalFilled == "" {
		return fwtrading.Snapshot{}, errOpenAlgoCancelledFillsUnconfirmed
	} else {
		confirmed, err := decimal.NewFromString(string(terminalFilled))
		if err != nil || confirmed.IsNegative() {
			return fwtrading.Snapshot{}, fmt.Errorf(
				"openalgo: invalid terminal filled quantity: %w", domain.ErrUpstream)
		}
		if !confirmed.Equal(cumulative) {
			return fwtrading.Snapshot{}, errOpenAlgoCancelledFillsUnconfirmed
		}
	}
	snapshot.FilledQuantity = cumulative.String()
	return snapshot, nil
}
