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
	"fmt"
	"log/slog"

	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
)

// RejectedImmediate materializes the rejected branch of an immediate chain.
func (e *openPitEngine) RejectedImmediate(
	_ domain.Order, rejects []reject.Reject,
) fwengine.ImmediateResult {
	return fwengine.ImmediateResult{
		Accepted: false,
		Rejects:  orderRejectsFrom(rejects),
	}
}

// PrepareImmediateReservation derives the report while the reservation can
// still be rolled back.
func (e *openPitEngine) PrepareImmediateReservation(
	o domain.Order, result asyncengine.OperationResult,
) (fwengine.ImmediatePreparation, error) {
	return e.prepareImmediate(
		o, result, nil, "reservation", "reservation committed",
	)
}

// PrepareImmediateDropCopy derives the report and captures a requested block
// while the drop-copy operation can still be rolled back.
func (e *openPitEngine) PrepareImmediateDropCopy(
	o domain.Order, result asyncengine.DropCopyResult,
) (fwengine.ImmediatePreparation, error) {
	block, err := result.AccountBlock()
	if err != nil {
		return fwengine.ImmediatePreparation{}, err
	}
	return e.prepareImmediate(
		o, result, block, "drop-copy", "drop-copy committed",
	)
}

func (e *openPitEngine) prepareImmediate(
	o domain.Order,
	result asyncengine.OperationResult,
	block *reject.AccountBlock,
	operationName string,
	reconciliationState string,
) (fwengine.ImmediatePreparation, error) {
	lock, err := result.Lock()
	if err != nil {
		return fwengine.ImmediatePreparation{}, fmt.Errorf(
			"engine: read %s lock: %w", operationName, err,
		)
	}
	lockBytes, settlement, err := capturePreTradeOutput(lock, o)
	if err != nil {
		return fwengine.ImmediatePreparation{}, err
	}
	tradePrice, err := immediateTradePrice(o, settlement)
	if err != nil {
		return fwengine.ImmediatePreparation{}, err
	}
	adjustments, err := result.AccountAdjustments()
	if err != nil {
		return fwengine.ImmediatePreparation{}, err
	}
	outcomes, err := balanceOutcomesFromList(adjustments, e.res)
	if err != nil {
		return fwengine.ImmediatePreparation{}, err
	}
	fillQuantity, err := immediateFillQuantity(o, outcomes)
	if err != nil {
		return fwengine.ImmediatePreparation{}, err
	}
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
	accountID, err := e.res.account(o.Account)
	if err != nil {
		return fwengine.ImmediatePreparation{}, err
	}
	report, err := executionReportFromAccount(
		reportInput, accountID, "0", e.res,
	)
	if err != nil {
		return fwengine.ImmediatePreparation{}, err
	}
	var blocks []domain.AccountBlock
	if block != nil {
		blocks = executionBlocksFrom([]reject.AccountBlock{*block}, o.Account)
	}
	return fwengine.ImmediatePreparation{
		ExecutionReport:     report,
		ReportInput:         reportInput,
		Outcomes:            outcomes,
		Blocks:              blocks,
		ReconciliationState: reconciliationState,
	}, nil
}

// SettleImmediate combines the already-applied report result with the
// pre-trade preparation without re-entering the async engine.
func (e *openPitEngine) SettleImmediate(
	o domain.Order,
	prepared fwengine.ImmediatePreparation,
	postTrade pretrade.PostTradeResult,
) (fwengine.ImmediateResult, error) {
	postTradeOutcomes, err := balanceOutcomesFromList(
		postTrade.AccountAdjustments, e.res,
	)
	if err != nil {
		return fwengine.ImmediateResult{}, immediateReconciliationError(
			o, prepared.ReconciliationState,
			"the settled outcome is unmappable", err,
		)
	}
	finalOutcomes, err := mergeBalanceOutcomes(
		prepared.Outcomes, postTradeOutcomes,
	)
	if err != nil {
		return fwengine.ImmediateResult{}, immediateReconciliationError(
			o, prepared.ReconciliationState,
			"the ordered outcomes cannot be combined", err,
		)
	}
	accountID, err := e.res.account(o.Account)
	if err != nil {
		return fwengine.ImmediateResult{}, immediateReconciliationError(
			o, prepared.ReconciliationState,
			"the account P&L outcome is unmappable", err,
		)
	}
	accountPnl, accountPnlHaltReason, err := spotFundsAccountPnlFromList(
		accountID, postTrade.AccountPnls,
	)
	if err != nil {
		return fwengine.ImmediateResult{}, immediateReconciliationError(
			o, prepared.ReconciliationState,
			"the account P&L outcome is unmappable", err,
		)
	}
	blocks := append(
		[]domain.AccountBlock(nil), prepared.Blocks...,
	)
	blocks = append(
		blocks,
		executionBlocksFrom(postTrade.AccountBlocks, o.Account)...,
	)
	persistence := executionReportPersistenceFrom(
		prepared.ReportInput,
		blocks,
		finalOutcomes,
		accountPnl,
		accountPnlHaltReason,
	)
	return fwengine.ImmediateResult{
		Accepted:             true,
		Persistence:          &persistence,
		ExecutionReport:      domain.ExecutionReportRequestFromInput(prepared.ReportInput),
		Lock:                 append([]byte(nil), prepared.ReportInput.Lock...),
		Blocks:               blocks,
		Outcomes:             finalOutcomes,
		AccountPnl:           accountPnl,
		AccountPnlHaltReason: accountPnlHaltReason,
		SettlementLockPrice:  prepared.ReportInput.LockPrice,
		FillQuantity:         prepared.ReportInput.FillQuantity,
		TradePrice:           prepared.ReportInput.FillPrice,
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
