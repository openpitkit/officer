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
	"time"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

// fillExecution is one venue execution in the order the chain numbers it.
type fillExecution struct {
	quantity, price decimal.Decimal
	at              time.Time
	commission      *domain.Commission
}

// fillChain numbers ordered executions into the runtime's fill chain.
// It returns false if their cumulative quantity exceeds requested.
// Only the last fill of a filled order is final.
func fillChain(executions []fillExecution, requested decimal.Decimal,
	status fwtrading.VenueStatus,
) ([]fwtrading.Fill, decimal.Decimal, bool) {
	var fills []fwtrading.Fill
	cumulative := decimal.Zero
	for i, execution := range executions {
		cumulative = cumulative.Add(execution.quantity)
		leaves := requested.Sub(cumulative)
		if leaves.IsNegative() {
			return nil, cumulative, false
		}
		fills = append(fills, fwtrading.Fill{
			Quantity: execution.quantity.String(), Price: execution.price.String(),
			CumQuantity: cumulative.String(), LeavesQuantity: leaves.String(),
			Final: i == len(executions)-1 && status == fwtrading.VenueStatusFilled,
			At:    execution.at, Commission: execution.commission,
		})
	}
	return fills, cumulative, true
}
