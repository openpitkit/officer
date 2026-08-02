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

	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
)

// SubmitImmediate runs the pre-trade pipeline and, on accept, settles a fill in
// the same call so the held amount nets to zero: the fill is priced at the
// captured settlement lock price and carries the pre-trade lock the engine
// itself produced. Regular pre-trade commits its reservation first; drop-copy
// arrives already applied.
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
	if o.DropCopy {
		return l.submitImmediateDropCopy(o, order, accountID)
	}

	reservation, rejects, err := l.eng.ExecutePreTrade(order)
	if err != nil {
		return ImmediateResult{}, wrapPreTradeError(err)
	}
	if rejects != nil {
		return ImmediateResult{Accepted: false, Rejects: orderRejectsFrom(rejects)}, nil
	}

	lock, err := reservation.Lock()
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, fmt.Errorf("engine: read reservation lock: %w", err)
	}
	lockBytes, settlement, source, err := capturePreTradeOutput(lock, o)
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
	// The report goes straight back to the engine, so it carries the lock the
	// engine produced for this very reservation. A lock is an opaque engine
	// artifact; the LockPrice fallback would rebuild a single-entry
	// default-policy-group lock and drop every other group's leg.
	report, err := executionReportFromAccount(domain.ExecutionReportInput{
		BaseAsset:      o.BaseAsset,
		QuoteAsset:     o.QuoteAsset,
		FillQuantity:   fillQuantity,
		FillPrice:      settlement,
		LeavesQuantity: "0",
		LockPrice:      settlement,
		Lock:           lockBytes,
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
	adjustments, err := reservation.AccountAdjustments()
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, err
	}
	reservationOutcomes, err := balanceOutcomesFromList(adjustments)
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
	return l.settleImmediateApplied(
		o,
		accountID,
		report,
		reservationOutcomes,
		nil,
		lockBytes,
		settlement,
		fillQuantity,
		source,
		"reservation committed",
	)
}

func (l accountLane) submitImmediateDropCopy(
	o domain.Order,
	order model.Order,
	accountID param.AccountID,
) (ImmediateResult, error) {
	result, rejects, err := l.eng.ApplyDropCopy(order)
	if err != nil {
		return ImmediateResult{}, wrapPreTradeError(err)
	}
	if rejects != nil {
		return ImmediateResult{
			Accepted: false,
			Rejects:  orderRejectsFrom(rejects),
		}, nil
	}
	// Guard the FFI contract: Close dereferences the result, so a nil one must
	// not reach the defer below.
	if result == nil {
		return ImmediateResult{}, fmt.Errorf(
			"engine: drop-copy returned neither a result nor rejects",
		)
	}
	defer result.Close()

	lock, err := result.Lock()
	if err != nil {
		return ImmediateResult{}, dropCopyReconciliationError(
			o, "read applied lock", err,
		)
	}
	lockBytes, settlement, source, err := capturePreTradeOutput(lock, o)
	if err != nil {
		return ImmediateResult{}, dropCopyReconciliationError(o, "materialize lock", err)
	}
	if reject, ok := volumeOrderSizingReject(o, settlement); ok {
		return ImmediateResult{}, dropCopyReconciliationError(
			o,
			"size volume order",
			fmt.Errorf("%s: %s", reject.Reason, reject.Details),
		)
	}
	fillQuantity, err := immediateFillQuantity(o, settlement)
	if err != nil {
		return ImmediateResult{}, dropCopyReconciliationError(
			o, "derive immediate fill quantity", err,
		)
	}
	// Same contract as the regular branch: hand back the lock this drop-copy
	// result produced, not the single-entry one the LockPrice fallback rebuilds.
	report, err := executionReportFromAccount(domain.ExecutionReportInput{
		BaseAsset:      o.BaseAsset,
		QuoteAsset:     o.QuoteAsset,
		FillQuantity:   fillQuantity,
		FillPrice:      settlement,
		LeavesQuantity: "0",
		LockPrice:      settlement,
		Lock:           lockBytes,
		Account:        o.Account,
		Side:           o.Side,
		Order:          o.ExternalID,
		OrderStatus:    domain.OrderStatusFilled,
	}, accountID)
	if err != nil {
		return ImmediateResult{}, dropCopyReconciliationError(
			o, "build immediate execution report", err,
		)
	}
	adjustments, err := result.AccountAdjustments()
	if err != nil {
		return ImmediateResult{}, dropCopyReconciliationError(
			o, "read applied balance outcomes", err,
		)
	}
	preTradeOutcomes, err := balanceOutcomesFromList(adjustments)
	if err != nil {
		return ImmediateResult{}, dropCopyReconciliationError(
			o, "map balance outcomes", err,
		)
	}
	block, err := result.AccountBlock()
	if err != nil {
		return ImmediateResult{}, dropCopyReconciliationError(
			o, "read applied account block", err,
		)
	}
	var blocks []reject.AccountBlock
	if block != nil {
		blocks = append(blocks, *block)
	}
	return l.settleImmediateApplied(
		o,
		accountID,
		report,
		preTradeOutcomes,
		blocks,
		lockBytes,
		settlement,
		fillQuantity,
		source,
		"drop-copy applied",
	)
}

func (l accountLane) settleImmediateApplied(
	o domain.Order,
	accountID param.AccountID,
	report model.ExecutionReport,
	preTradeOutcomes []BalanceOutcome,
	preTradeBlocks []reject.AccountBlock,
	lockBytes []byte,
	settlement string,
	fillQuantity string,
	source string,
	state string,
) (ImmediateResult, error) {
	postTrade, err := l.eng.ApplyExecutionReport(report)
	if err != nil {
		return ImmediateResult{}, immediateReconciliationError(
			o, state, "the execution report failed and the fill is unsettled", err,
		)
	}
	postTradeOutcomes, err := balanceOutcomesFromList(postTrade.AccountAdjustments)
	if err != nil {
		return ImmediateResult{}, immediateReconciliationError(
			o, state, "the settled outcome is unmappable", err,
		)
	}
	finalOutcomes, err := mergeBalanceOutcomes(preTradeOutcomes, postTradeOutcomes)
	if err != nil {
		return ImmediateResult{}, immediateReconciliationError(
			o, state, "the ordered outcomes cannot be combined", err,
		)
	}
	accountPnl, accountPnlHaltReason, err := spotFundsAccountPnlFromList(
		accountID,
		postTrade.AccountPnls,
	)
	if err != nil {
		return ImmediateResult{}, immediateReconciliationError(
			o, state, "the account P&L outcome is unmappable", err,
		)
	}
	// Build a fresh slice: appending to the caller's slice could write into its
	// backing array.
	blocks := make(
		[]reject.AccountBlock, 0, len(preTradeBlocks)+len(postTrade.AccountBlocks),
	)
	blocks = append(blocks, preTradeBlocks...)
	blocks = append(blocks, postTrade.AccountBlocks...)
	return ImmediateResult{
		Accepted:             true,
		Lock:                 lockBytes,
		Blocks:               executionBlocksFrom(blocks, o.Account),
		Outcomes:             finalOutcomes,
		AccountPnl:           accountPnl,
		AccountPnlHaltReason: accountPnlHaltReason,
		SettlementLockPrice:  settlement,
		FillQuantity:         fillQuantity,
		EstimateSource:       source,
	}, nil
}

// immediateReconciliationError reports state the engine already applied and the
// caller must reconcile by hand. The cause is rendered, not wrapped: causes
// carrying domain.ErrInvalid would answer 400 "fix your input and retry", which
// contradicts the message and hides an internal failure.
func immediateReconciliationError(
	o domain.Order, state string, action string, err error,
) error {
	return fmt.Errorf(
		"engine: %s for order %s (account %s), but %s; engine state needs "+
			"manual reconciliation - do not retry blindly: %v",
		state, orderExternalIDForError(o.ExternalID), o.Account, action, err,
	)
}

// volumeOrderSizingReject converts a policy pass without a usable volume
// conversion price into a normal order reject for regular pre-trade. Pricing
// is known only after the policy pipeline returns its lock, so rejecting here
// avoids a duplicate dry run while the reservation can still be rolled back.
// Drop-copy callers convert the same condition into a reconciliation error
// because its one-shot state has already been applied.
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

// capturePreTradeOutput serializes a pre-trade output's lock and derives its
// settlement estimate for either a live reservation or an already-applied
// drop-copy result.
func capturePreTradeOutput(
	lock pretrade.Lock, o domain.Order,
) ([]byte, string, string, error) {
	lockBytes, err := serializePreTradeLock(lock)
	if err != nil {
		return nil, "", "", err
	}
	prices, err := lock.Prices()
	if err != nil {
		return nil, "", "", fmt.Errorf("engine: read pre-trade lock: %w", err)
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
