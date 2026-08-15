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

package native

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"

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

	result, err := e.SubmitOrder(context.Background(), order)
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

func appliedDropCopyOperation(
	t *testing.T,
	e *openPitEngine,
	o domain.Order,
) *pretrade.DropCopyOperation {
	t.Helper()
	accountID, err := e.res.account(o.Account)
	if err != nil {
		t.Fatalf("resolve drop-copy account: %v", err)
	}
	order, err := orderModelFromAccount(o, accountID, e.res)
	if err != nil {
		t.Fatalf("map drop-copy order: %v", err)
	}
	operation, rejects, err := e.eng.ApplyDropCopy(order)
	if err != nil {
		t.Fatalf("ApplyDropCopy: %v", err)
	}
	if len(rejects) != 0 {
		t.Fatalf("ApplyDropCopy rejects = %+v, want applied operation", rejects)
	}
	if operation == nil {
		t.Fatal("ApplyDropCopy returned no operation")
	}
	return operation
}

// dropCopyDerivationFailureOrder holds half of the seeded quote balance before
// the materialization callback fails.
func dropCopyDerivationFailureOrder() domain.Order {
	order := testOrder()
	order.DropCopy = true
	order.AmountValue = "5"
	return order
}

// assertOrdinaryDerivationError pins the failure as an ordinary error: with the
// rollback window in place nothing is applied by the time Officer gives up, so
// the caller may retry instead of reconciling engine state by hand.
func assertOrdinaryDerivationError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("materialization did not fail the drop-copy")
	}
	if !strings.Contains(err.Error(), "materialize applied drop-copy") {
		t.Fatalf("error = %v, want the materialization failure", err)
	}
	if strings.Contains(err.Error(), "manual reconciliation") {
		t.Fatalf(
			"error = %v, want an ordinary error, not a reconciliation verdict", err,
		)
	}
}

// assertWholeSeedAvailable reads engine state instead of trusting the failed
// call: an ordinary order for the entire 1000-quote seed is accepted only when
// no drop-copy hold survived, and its outcome carries the balances the engine
// actually holds. A surviving 500 hold would reject it for insufficient funds.
func assertWholeSeedAvailable(t *testing.T, e *openPitEngine) {
	t.Helper()
	probe := testOrder()
	probe.AmountValue = "10" // 10 * 100 = the whole seeded quote balance
	result, err := e.SubmitOrder(context.Background(), probe)
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

// TestFinalizeDropCopy_MaterializationFailureRollsBack proves the shared
// rollback window closes the old defect without a test-only production field.
func TestFinalizeDropCopy_MaterializationFailureRollsBack(t *testing.T) {
	e := newTestEngine(t)
	operation := appliedDropCopyOperation(
		t, e, dropCopyDerivationFailureOrder(),
	)
	result, err := finalizeDropCopy(operation, func() (OrderResult, error) {
		return OrderResult{}, errors.New("materialize applied drop-copy")
	})
	assertOrdinaryDerivationError(t, err)
	if result.Accepted {
		t.Fatalf("failed drop-copy result = %+v, want the zero value", result)
	}
	assertWholeSeedAvailable(t, e)
}

// TestFinalizeDropCopy_RollbackFailureSurfacesOnNextPreTrade pins the SDK's
// void-finalizer contract: Officer cannot observe the callback failure during
// Rollback, but the engine does not lose it and rejects the next call.
func TestFinalizeDropCopy_RollbackFailureSurfacesOnNextPreTrade(t *testing.T) {
	price, err := param.NewPriceFromString(testLimit)
	if err != nil {
		t.Fatalf("lock spy price: %v", err)
	}
	spy := &executionLockSpy{
		price:         price,
		rollbackPanic: "forced rollback failure",
	}
	e := newLockSpyTestEngine(t, spy)
	operation := appliedDropCopyOperation(
		t, e, dropCopyDerivationFailureOrder(),
	)
	_, err = finalizeDropCopy(operation, func() (OrderResult, error) {
		return OrderResult{}, errors.New("materialize applied drop-copy")
	})
	assertOrdinaryDerivationError(t, err)

	result, err := e.SubmitOrder(context.Background(), testOrder())
	if err != nil {
		t.Fatalf("SubmitOrder after failed rollback: %v", err)
	}
	if result.Accepted || len(result.Rejects) == 0 {
		t.Fatalf("result after failed rollback = %+v, want engine reject", result)
	}
	found := false
	for _, item := range result.Rejects {
		if item.Code == "system_unavailable" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("rejects = %+v, want system_unavailable", result.Rejects)
	}
}
