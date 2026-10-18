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
	"context"
	"errors"
	"fmt"
	"log/slog"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func (w *worker) intakeFees() {
	reader, ok := w.connector.(FeeReader)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(w.r.ctx, connectorTimeout)
	defer cancel()
	since, found, err := w.r.store.EarliestVenueOrderTime(ctx, w.connection.ExternalID)
	if err != nil {
		w.log(slog.LevelError, "", "read earliest venue order time for fees", err)
		return
	}
	if !found {
		return
	}
	account, accounts, err := w.r.store.GetTradingFeeAccounts(ctx, w.connection.ExternalID)
	if err != nil {
		w.log(slog.LevelError, "", "read connection fee accounts", err)
		return
	}
	accountReason := ""
	defaulted := false
	if account == "" {
		if len(accounts) == 1 {
			account = accounts[0]
			defaulted = true
		} else {
			accountReason = fmt.Sprintf("connection is used by %d accounts and has no fee account", len(accounts))
		}
	}
	for {
		if ctx.Err() != nil {
			return
		}
		fees, next, readErr := reader.Fees(ctx, since, w.feeCursor)
		for _, fee := range fees {
			if ctx.Err() != nil {
				return
			}
			if fee.ID == "" {
				w.logFee(fee, "missing fee id or invalid fee status", domain.ErrInvalid)
				w.reportFeeSkip(fee, "missing fee id")
				return
			}
			if !w.intakeFee(ctx, reader, fee, account, defaulted, accountReason) {
				return
			}
			w.feeCursor = fee.ID
		}
		if readErr != nil {
			w.log(slog.LevelError, "", "read venue fees", readErr)
			return
		}
		if next == "" {
			return
		}
		if len(fees) == 0 || next != w.feeCursor {
			w.log(slog.LevelError, "", "fee pagination did not advance to the last processed id", domain.ErrInvalid)
			return
		}
	}
}

func (w *worker) intakeFee(ctx context.Context, reader FeeReader, fee Fee, account domain.AccountID, defaulted bool, accountReason string) bool {
	if fee.Status == FeeNotApplicable {
		w.logFee(fee, fee.Detail, nil)
		return w.reportFeeSkip(fee, fee.Detail)
	}
	if fee.Status != FeeExecuted {
		w.logFee(fee, "missing fee id or invalid fee status", domain.ErrInvalid)
		return w.reportFeeSkip(fee, "invalid fee status")
	}
	id := domain.ExternalID(domain.TradingRuntimeIDPrefix + w.connection.ExternalID.String() + ":fee:" + fee.ID)
	page, err := w.r.store.ListAdjustmentRows(ctx, store.AdjustmentListFilter{
		ExternalID: id,
		Page:       store.PageSpec{Limit: 1},
	})
	if err != nil {
		w.logFee(fee, "check fee adjustment external id", err)
		return false
	}
	if len(page.Rows) > 0 {
		if page.Rows[0].Accepted != nil {
			return true
		}
		return w.reportFeeSkip(fee, "rejected by the core")
	}
	if account == "" {
		w.logFee(fee, accountReason, nil)
		return w.reportFeeSkip(fee, accountReason)
	}
	asset, final, err := w.feeAsset(ctx, reader, fee.Asset)
	if err != nil {
		w.logFee(fee, "resolve fee asset", err)
		if final {
			return w.reportFeeSkip(fee, "resolve fee asset: "+err.Error())
		}
		return final
	}
	if ctx.Err() != nil {
		return false
	}
	err = w.r.adjust(context.WithoutCancel(w.r.ctx), account, id, domain.AdjustmentRequest{
		Type:    domain.AdjustmentTypeVenueFee,
		Asset:   asset,
		Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: fee.Amount},
	})
	if errors.Is(err, ErrAdjustmentRejected) {
		w.logFee(fee, "rejected by the core: "+err.Error(), nil)
		return w.reportFeeSkip(fee, "rejected by the core")
	}
	if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		w.logFee(fee, "apply fee adjustment", err)
		return false
	}
	if err == nil && defaulted {
		w.r.logger.InfoContext(context.WithoutCancel(w.r.ctx),
			"venue fee applied to the only account that can send through the connection because the connection has no fee account",
			"connection", w.connection.ExternalID, "fee_id", fee.ID, "account", account)
	}
	return true
}

func (w *worker) reportFeeSkip(fee Fee, reason string) bool {
	w.r.mu.Lock()
	reported := w.r.feeSkips[w.connection.ExternalID]
	if _, found := reported[fee.ID]; found {
		w.r.mu.Unlock()
		return true
	}
	w.r.mu.Unlock()

	if err := w.r.feeSkip(context.WithoutCancel(w.r.ctx), w.connection.ExternalID, fee.ID, reason); err != nil {
		w.logFee(fee, "report skipped venue fee", err)
		return false
	}
	w.r.mu.Lock()
	if reported == nil {
		reported = make(map[string]struct{})
		w.r.feeSkips[w.connection.ExternalID] = reported
	}
	reported[fee.ID] = struct{}{}
	w.r.mu.Unlock()
	return true
}

func (w *worker) feeAsset(ctx context.Context, reader FeeReader, venueAsset string) (string, bool, error) {
	instruments, err := w.r.store.ListTradingInstruments(ctx, w.connection.ExternalID)
	if err != nil {
		return "", false, err
	}
	assets := make(map[string]struct{})
	for _, instrument := range instruments {
		base, quote, err := reader.InstrumentAssets(instrument.ExternalSymbol)
		if err != nil {
			return "", true, fmt.Errorf("instrument %s: %w", instrument.ExternalSymbol, err)
		}
		if base == venueAsset {
			assets[instrument.BaseAsset] = struct{}{}
		}
		if quote == venueAsset {
			assets[instrument.QuoteAsset] = struct{}{}
		}
	}
	if len(assets) != 1 {
		return "", true, fmt.Errorf("venue asset %s maps to %d Officer assets", venueAsset, len(assets))
	}
	for asset := range assets {
		return asset, true, nil
	}
	return "", true, domain.ErrInvalid
}

func (w *worker) logFee(fee Fee, detail string, err error) {
	w.r.logger.ErrorContext(context.WithoutCancel(w.r.ctx),
		fmt.Sprintf("fee %s not applied: %s", fee.ID, detail),
		"connection", w.connection.ExternalID, "fee_id", fee.ID,
		"detail", fee.Detail, "error", err)
}
