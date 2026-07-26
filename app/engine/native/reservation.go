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
	"fmt"

	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
)

// SubmitImmediate runs the pre-trade pipeline and, on accept, commits the
// reservation and settles a fill at the captured settlement lock price in the
// same call so the held amount nets to zero.
func (e *openPitEngine) SubmitImmediate(
	ctx context.Context, o domain.Order,
) (ImmediateResult, error) {
	var result ImmediateResult
	err := e.RunAccountSynchronized(ctx, o.Account, func(lane fwengine.AccountLane) error {
		var err error
		result, err = lane.SubmitImmediate(ctx, o)
		return err
	})
	return result, err
}

func (l accountLane) SubmitImmediate(
	ctx context.Context, o domain.Order,
) (ImmediateResult, error) {
	if err := ctx.Err(); err != nil {
		return ImmediateResult{}, fmt.Errorf("engine: submit immediate cancelled: %w", err)
	}

	accountID, err := l.routedAccountID(o.Account)
	if err != nil {
		return ImmediateResult{}, err
	}
	order, err := orderModelFromAccount(o, accountID)
	if err != nil {
		return ImmediateResult{}, err
	}

	reservation, rejects, err := l.eng.ExecutePreTrade(order)
	if err != nil {
		return ImmediateResult{}, fmt.Errorf("engine: execute pre-trade: %w", err)
	}
	if rejects != nil {
		return ImmediateResult{Accepted: false, Rejects: orderRejectsFrom(rejects)}, nil
	}

	lockBytes, settlement, source, err := captureReservation(reservation, o)
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, err
	}
	if reject, ok := volumeOrderSizingReject(o, settlement); ok {
		reservation.RollbackAndClose()
		return ImmediateResult{
			Accepted: false,
			Rejects:  []domain.OrderReject{reject},
		}, nil
	}

	fillQuantity, err := immediateFillQuantity(o, settlement)
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, err
	}
	report, err := executionReportFromAccount(domain.ExecutionReportInput{
		BaseAsset:      o.BaseAsset,
		QuoteAsset:     o.QuoteAsset,
		FillQuantity:   fillQuantity,
		FillPrice:      settlement,
		LeavesQuantity: "0",
		LockPrice:      settlement,
		Account:        o.Account,
		Side:           o.Side,
		Order:          o.ExternalID,
		OrderStatus:    domain.OrderStatusFilled,
	}, accountID)
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, err
	}

	// Persist the reservation outcomes before the post-trade outcomes. A final
	// execution report may omit an unchanged available balance after releasing a
	// reservation, so the persisted snapshot still needs the reservation's
	// available/held transition.
	reservationOutcomes, err := balanceOutcomesFromList(reservation.AccountAdjustments())
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, err
	}
	// Ordering rationale: the SDK reservation exposes only Commit/Rollback, and a
	// fill is settled by the engine-level ApplyExecutionReport, not by the
	// reservation. The execution report settles against the reservation's reserved
	// state once it is realized, so the commit must happen first - a report applied
	// against an unrealized reservation would not net the held amount to zero.
	// Settle-then-commit is therefore not expressible with this SDK.
	reservation.CommitAndClose()

	// CommitAndClose has already closed the reservation, so there is no
	// compensating rollback available if the post-trade settlement fails. A
	// failure here leaves the funds committed but the fill unsettled, which only an
	// operator can reconcile; surface that explicitly rather than as a bare wrapped
	// error so the caller does not retry blindly.
	postTrade, err := l.eng.ApplyExecutionReport(report)
	if err != nil {
		return ImmediateResult{}, fmt.Errorf(
			"engine: reservation committed but execution report failed for order %s "+
				"(account %s); funds are committed and the fill is unsettled - the "+
				"engine needs manual reconciliation: %w",
			orderExternalIDForError(o.ExternalID), o.Account, err,
		)
	}
	// The reservation is committed and the report is settled, so an unmappable
	// engine outcome cannot be undone here. Report it in the same terms as a
	// failed settlement above: the engine state stands and only an operator can
	// reconcile the missing snapshot.
	postTradeOutcomes, err := balanceOutcomesFromList(postTrade.AccountAdjustments)
	if err != nil {
		return ImmediateResult{}, fmt.Errorf(
			"engine: reservation committed and execution report settled for order %s "+
				"(account %s), but its outcome is unmappable - the persisted balance "+
				"snapshot needs manual reconciliation: %w",
			orderExternalIDForError(o.ExternalID), o.Account, err,
		)
	}
	finalOutcomes, err := mergeBalanceOutcomes(reservationOutcomes, postTradeOutcomes)
	if err != nil {
		return ImmediateResult{}, fmt.Errorf(
			"engine: reservation committed and execution report settled for order %s "+
				"(account %s), but its ordered outcomes cannot be combined - the "+
				"persisted balance snapshot needs manual reconciliation: %w",
			orderExternalIDForError(o.ExternalID), o.Account, err,
		)
	}
	accountPnl, accountPnlHaltReason, err := spotFundsAccountPnlFromList(
		accountID,
		postTrade.AccountPnls,
	)
	if err != nil {
		return ImmediateResult{}, fmt.Errorf(
			"engine: reservation committed and execution report settled for order %s "+
				"(account %s), but its account P&L outcome is unmappable - the "+
				"persisted P&L needs manual reconciliation: %w",
			orderExternalIDForError(o.ExternalID), o.Account, err,
		)
	}
	return ImmediateResult{
		Accepted:             true,
		Lock:                 lockBytes,
		Blocks:               executionBlocksFrom(postTrade.AccountBlocks, o.Account),
		Outcomes:             finalOutcomes,
		AccountPnl:           accountPnl,
		AccountPnlHaltReason: accountPnlHaltReason,
		SettlementLockPrice:  settlement,
		FillQuantity:         fillQuantity,
		EstimateSource:       source,
	}, nil
}

// volumeOrderSizingReject converts a policy pass without a usable volume
// conversion price into a normal order reject. Pricing is known only after the
// policy pipeline returns its lock, so rejecting here avoids a duplicate dry
// run while still rolling the reservation back before any state is committed.
func volumeOrderSizingReject(
	o domain.Order, settlementPrice string,
) (domain.OrderReject, bool) {
	if o.AmountKind != domain.OrderAmountKindVolume || settlementPrice != "" {
		return domain.OrderReject{}, false
	}
	return domain.OrderReject{
		Code:    rejectCodeName(reject.CodeOrderValueCalculationFailed),
		Scope:   "order",
		Reason:  "volume order requires a settlement price",
		Details: "no configured policy produced a settlement price",
	}, true
}

func orderExternalIDForError(id domain.ExternalID) string {
	if id.IsZero() {
		return "<unassigned>"
	}
	return id.String()
}

// captureReservation serializes a reservation's lock and derives its settlement
// estimate while the reservation is still open. The caller owns the resulting
// snapshot and may then commit or roll back the reservation.
func captureReservation(
	reservation *pretrade.Reservation, o domain.Order,
) ([]byte, string, string, error) {
	lockBytes, err := serializePreTradeLock(reservation)
	if err != nil {
		return nil, "", "", err
	}
	prices, err := reservation.Lock().Prices()
	if err != nil {
		return nil, "", "", fmt.Errorf("engine: read reservation lock: %w", err)
	}
	settlement, source := settlementEstimate(pricesToStrings(prices), o)
	return lockBytes, settlement, source, nil
}

// settlementEstimate derives the settlement-leg lock price and the estimate
// source for an order. The settlement leg is the last lock price (default-group
// records come first, the spot-funds settlement leg last); the source is "limit"
// when the order carried a limit price, else "market_mark".
func settlementEstimate(lockPrices []string, o domain.Order) (string, string) {
	settlement := ""
	if len(lockPrices) > 0 {
		settlement = lockPrices[len(lockPrices)-1]
	}
	source := domain.EstimateSourceMarketMark
	if o.Price != "" {
		source = domain.EstimateSourceLimit
	}
	return settlement, source
}
