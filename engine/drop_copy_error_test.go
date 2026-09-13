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

package engine

import (
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

// A drop-copy order without a limit price is refused by the engine itself
// (SpotFunds cannot value a historical market order), so Officer never sees an
// applied operation and the seeded funds stay untouched.
func TestSubmitOrder_DropCopyMarketOrderReturnsPolicyReject(t *testing.T) {
	e := newTestEngine(t)
	order := testOrder()
	order.DropCopy = true
	order.Price = ""

	result, err := materializeOrderResult(e, order)
	if err != nil {
		t.Fatalf("SubmitOrder(drop-copy market): %v", err)
	}
	if result.Accepted || len(result.Rejects) != 1 {
		t.Fatalf(
			"drop-copy market result = %+v, want one policy reject", result,
		)
	}
	if result.Rejects[0].Code != "missing_required_field" {
		t.Fatalf(
			"drop-copy market reject = %+v, want missing_required_field",
			result.Rejects[0],
		)
	}
	assertWholeSeedAvailable(t, e)
}

// assertWholeSeedAvailable reads engine state instead of trusting the failed
// call: an ordinary order for the entire 1000-quote seed is accepted only when
// no drop-copy hold survived, and its outcome carries the balances the engine
// actually holds. A surviving 500 hold would reject it for insufficient funds.
func assertWholeSeedAvailable(t *testing.T, e *openPitEngine) {
	t.Helper()
	probe := testOrder()
	probe.AmountValue = "10" // 10 * 100 = the whole seeded quote balance
	result, err := materializeOrderResult(e, probe)
	if err != nil {
		t.Fatalf("SubmitOrder(balance probe): %v", err)
	}
	if !result.Accepted {
		t.Fatalf(
			"balance probe rejected: %+v; the drop-copy hold is still in place",
			result.Rejects,
		)
	}
	var quote *domain.AdjustmentOutcomeAccepted
	for i := range result.Outcomes {
		if result.Outcomes[i].Asset == testQuote {
			quote = &result.Outcomes[i].Outcome
			break
		}
	}
	if quote == nil {
		t.Fatalf("balance probe quote outcome missing: %+v", result.Outcomes)
	}
	if quote.BalanceResult != "0" || quote.HeldResult != "1000" {
		t.Fatalf(
			"quote outcome = %+v, want the whole 1000 seed held from 0 available",
			quote,
		)
	}
}
