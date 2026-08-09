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
	"log/slog"

	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
)

// SubmitImmediate runs the pre-trade pipeline and, on accept, settles a fill in
// the same call so the held amount nets to zero: the fill carries the request
// limit price (or the lock price for a market order) and the pre-trade lock the
// engine itself produced. Both branches finalize their engine-side state first - the
// reservation commit and the drop-copy commit - because the report settles
// against realized state.
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
	lockBytes, settlement, err := capturePreTradeOutput(lock, o)
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, err
	}
	tradePrice, err := immediateTradePrice(o, settlement)
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, err
	}
	// The engine has now applied the reservation and reports its base delta.
	// Read it before constructing the post-trade report so a volume order never
	// needs Officer to derive a base quantity from a price.
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
	fillQuantity, err := immediateFillQuantity(o, reservationOutcomes)
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, err
	}
	// The report goes straight back to the engine, so it carries the lock the
	// engine produced for this very reservation. A lock is an opaque engine
	// artifact; the LockPrice fallback would rebuild a single-entry
	// default-policy-group lock and drop every other group's leg.
	// The zero leaves is the request's own terms - an immediate order fills in
	// full - not a terminal zero Officer derived from an engine answer.
	reportInput := domain.ExecutionReportInput{
		BaseAsset:      o.BaseAsset,
		QuoteAsset:     o.QuoteAsset,
		FillQuantity:   fillQuantity,
		FillPrice:      tradePrice,
		LeavesQuantity: "0",
		LockPrice:      settlement,
		Lock:           lockBytes,
		Account:        o.Account,
		Side:           o.Side,
		Order:          o.ExternalID,
		OrderStatus:    domain.OrderStatusFilled,
	}

	report, err := executionReportFromAccount(reportInput, accountID, "0")
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
		reportInput,
		"reservation committed",
	)
}

type preparedImmediateDropCopy struct {
	report      model.ExecutionReport
	reportInput domain.ExecutionReportInput
	outcomes    []BalanceOutcome
	blocks      []reject.AccountBlock
}

// submitImmediateDropCopy mirrors the regular branch on the drop-copy rollback
// window: everything the settlement needs is derived while the applied
// mutations can still be compensated. The commit stays ahead of
// ApplyExecutionReport for the same reason the reservation commit does - a
// report settled against unrealized state would not net the held amount to
// zero - so a settlement failure past that point is still a reconciliation
// error, now the only one left on this path.
func (l accountLane) submitImmediateDropCopy(
	o domain.Order,
	order model.Order,
	accountID param.AccountID,
) (ImmediateResult, error) {
	operation, rejects, err := l.eng.ApplyDropCopy(order)
	if err != nil {
		return ImmediateResult{}, wrapPreTradeError(err)
	}
	if rejects != nil {
		return ImmediateResult{
			Accepted: false,
			Rejects:  orderRejectsFrom(rejects),
		}, nil
	}
	if operation == nil {
		return ImmediateResult{}, fmt.Errorf(
			"engine: drop-copy returned neither an operation nor rejects",
		)
	}
	prepared, err := finalizeDropCopy(
		operation,
		func() (preparedImmediateDropCopy, error) {
			lock, err := operation.Lock()
			if err != nil {
				return preparedImmediateDropCopy{}, fmt.Errorf(
					"engine: read drop-copy lock: %w", err,
				)
			}
			lockBytes, settlement, err := capturePreTradeOutput(lock, o)
			if err != nil {
				return preparedImmediateDropCopy{}, err
			}
			tradePrice, err := immediateTradePrice(o, settlement)
			if err != nil {
				return preparedImmediateDropCopy{}, err
			}
			// The applied drop-copy operation is the source of a volume order's
			// base quantity. Read its outcomes before building the fill report.
			adjustments, err := operation.AccountAdjustments()
			if err != nil {
				return preparedImmediateDropCopy{}, err
			}
			outcomes, err := balanceOutcomesFromList(adjustments)
			if err != nil {
				return preparedImmediateDropCopy{}, err
			}
			fillQuantity, err := immediateFillQuantity(o, outcomes)
			if err != nil {
				return preparedImmediateDropCopy{}, err
			}
			// Hand back the lock this operation produced, not a reconstruction
			// from one display price. The zero leaves is the request's own terms
			// - an immediate order fills in full - not a terminal zero Officer
			// derived from an engine answer.
			reportInput := domain.ExecutionReportInput{
				BaseAsset:      o.BaseAsset,
				QuoteAsset:     o.QuoteAsset,
				FillQuantity:   fillQuantity,
				FillPrice:      tradePrice,
				LeavesQuantity: "0",
				LockPrice:      settlement,
				Lock:           lockBytes,
				Account:        o.Account,
				Side:           o.Side,
				Order:          o.ExternalID,
				OrderStatus:    domain.OrderStatusFilled,
			}
			report, err := executionReportFromAccount(reportInput, accountID, "0")
			if err != nil {
				return preparedImmediateDropCopy{}, err
			}
			block, err := operation.AccountBlock()
			if err != nil {
				return preparedImmediateDropCopy{}, err
			}
			var blocks []reject.AccountBlock
			if block != nil {
				blocks = append(blocks, *block)
			}
			return preparedImmediateDropCopy{
				report:      report,
				reportInput: reportInput,
				outcomes:    outcomes,
				blocks:      blocks,
			}, nil
		},
	)
	if err != nil {
		return ImmediateResult{}, err
	}
	return l.settleImmediateApplied(
		o,
		accountID,
		prepared.report,
		prepared.outcomes,
		prepared.blocks,
		prepared.reportInput,
		"drop-copy committed",
	)
}

func (l accountLane) settleImmediateApplied(
	o domain.Order,
	accountID param.AccountID,
	report model.ExecutionReport,
	preTradeOutcomes []BalanceOutcome,
	preTradeBlocks []reject.AccountBlock,
	reportInput domain.ExecutionReportInput,
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
	executionBlocks := executionBlocksFrom(blocks, reportInput.Account)
	persistence := executionReportPersistenceFrom(
		reportInput,
		executionBlocks,
		finalOutcomes,
		accountPnl,
		accountPnlHaltReason,
	)
	return ImmediateResult{
		Accepted:             true,
		Persistence:          &persistence,
		ExecutionReport:      domain.ExecutionReportRequestFromInput(reportInput),
		Lock:                 append([]byte(nil), reportInput.Lock...),
		Blocks:               executionBlocks,
		Outcomes:             finalOutcomes,
		AccountPnl:           accountPnl,
		AccountPnlHaltReason: accountPnlHaltReason,
		SettlementLockPrice:  reportInput.LockPrice,
		FillQuantity:         reportInput.FillQuantity,
		TradePrice:           reportInput.FillPrice,
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

func immediateTradePrice(o domain.Order, settlementLockPrice string) (string, error) {
	if o.Price != "" {
		return o.Price, nil
	}
	if settlementLockPrice == "" {
		return "", fmt.Errorf(
			"engine: market immediate order has no lock price: %w",
			domain.ErrInvalid,
		)
	}
	return settlementLockPrice, nil
}

func orderExternalIDForError(id domain.ExternalID) string {
	if id.IsZero() {
		return "<unassigned>"
	}
	return id.String()
}

// capturePreTradeOutput reads a live lock once and serializes it for storage.
// Stored-lock consumers decode through LockSettlementPrice later.
func capturePreTradeOutput(
	lock pretrade.Lock, o domain.Order,
) ([]byte, string, error) {
	prices, err := lock.Prices()
	if err != nil {
		return nil, "", fmt.Errorf("engine: read lock prices: %w", err)
	}
	lockBytes, err := serializePreTradeLock(lock)
	if err != nil {
		return nil, "", err
	}
	settlement := settlementPrice(
		pricesToStrings(prices),
		orderExternalIDForError(o.ExternalID),
	)
	return lockBytes, settlement, nil
}

// settlementPrice returns the lock's single settlement price.
// The current Spot Funds engine contract emits exactly one lock price. Any other
// cardinality is an engine/adapter bug, but a non-empty result still degrades to
// the documented settlement leg at the end of the list.
func settlementPrice(lockPrices []string, orderIdentifier string) string {
	if len(lockPrices) > 1 {
		slog.Warn(
			"pre-trade lock carries unexpected price count",
			"count", len(lockPrices),
			"order", orderIdentifier,
		)
	}
	if len(lockPrices) > 0 {
		return lockPrices[len(lockPrices)-1]
	}
	return ""
}
