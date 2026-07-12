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

package node

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// ApplyAdjustment applies one spot-funds adjustment through the engine, which
// is the authority for the resulting holdings. An adjustment to an account or
// asset that does not exist yet auto-creates it before applying, so an operator
// can fund a fresh account or a fresh asset in one call. On accept it writes the
// recomputed balance snapshot and the accepted record; on reject it records the
// rejected adjustment and leaves balances unchanged. If the engine returns no
// account modification and the request carries no realized P&L, the call returns
// ErrNoChange without recording an adjustment or adjustment audit; a supplied
// realized P&L is still a requested change and is persisted as a
// realized-pnl-only adjustment.
func (n *localNode) ApplyAdjustment(
	ctx context.Context, key Key, externalID domain.ExternalID,
	req domain.AdjustmentRequest, caller domain.Caller,
) (domain.AccountAdjustmentRecord, error) {
	// Auto-create the account and asset and rebuild the engine before entering
	// the lane, so the live resolver knows them when RunAccountSynchronized
	// resolves the account and so the rebuild never stops a live lane's runtime.
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, key.Account, "adjustment", caller, req.Asset,
	); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	if err := n.ensureAdjustmentExternalIDUnused(ctx, externalID); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	accountID, err := n.accountDiagnosticID(ctx, key.Account)
	if err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	if req.RealizedPnl != "" && !adjustmentRequestEngineOnly(req) {
		// Keep realized-P&L snapshots out of the engine lane. The SQLite store
		// serializes its adjustment and settlement transactions on one
		// connection; settlement accumulates realized-P&L deltas inside its tx,
		// while this path records an operator snapshot. Re-entering the lane
		// here would add no ordering and risks the single-connection deadlock
		// that the store transaction boundary avoids.
		return n.recordRealizedPnlOnlyAdjustment(
			ctx, key, accountID, externalID, req, caller,
		)
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	defer done()

	var stored domain.AccountAdjustmentRecord
	if err := eng.RunAccountSynchronized(ctx, key.Account, func(lane engine.AccountLane) error {
		result, err := lane.ApplyAccountAdjustment(ctx, key.Account, req)
		if err != nil {
			return fmt.Errorf("apply adjustment: %w", err)
		}
		if adjustmentResultNoChange(result) {
			// Engine netted no change, but a supplied realized P&L is still a
			// requested change: persist it alone in this same lane/tx.
			if req.RealizedPnl == "" {
				return domain.ErrNoChange
			}
			var recErr error
			stored, recErr = n.recordRealizedPnlOnlyAdjustment(
				ctx, key, accountID, externalID, req, caller,
			)
			return recErr
		}

		// A non-zero externalID is the caller-supplied handle, carried verbatim onto
		// the record so the store uses it (else the store mints one on append).
		rec := domain.AccountAdjustmentRecord{
			ExternalID: externalID,
			Account:    key.Account,
			Source:     caller.Source,
			Principal:  caller.Principal,
			Request:    req,
			Accepted:   result.Accepted,
			Rejected:   result.Rejected,
			Asset:      req.Asset,
		}

		var balance *domain.Balance
		var deleteBalance *store.BalanceKey
		if result.Accepted != nil {
			var pnl realizedPnlPersistedOutcome
			var err error
			balance, deleteBalance, pnl, err =
				n.adjustedBalanceCommand(ctx, key, req, *result.Accepted)
			if err != nil {
				return err
			}
			// Record the realized P&L actually persisted, not the engine's own
			// cross-check absolute, so the outcome is the single source of truth.
			accepted := *result.Accepted
			accepted.RealizedPnlResult = pnl.Result
			accepted.RealizedPnlDelta = pnl.Delta
			rec.Accepted = &accepted
		}

		audit := n.auditEntry(caller, store.AuditEntry{
			Action:  domain.AuditActionAdjustment,
			Account: key.Account,
			Asset:   req.Asset,
			Detail:  adjustmentDetail(key.Account, req.Asset, result.Accepted != nil),
		})
		stored, err = n.realm.RecordAccountAdjustment(ctx, store.AccountAdjustmentPersistence{
			UpsertBalance: balance,
			DeleteBalance: deleteBalance,
			Adjustment:    rec,
			Audit:         audit,
		})
		if err != nil {
			return n.fatalPostEnginePersistence(
				"record account adjustment",
				accountID,
				fmt.Errorf("record adjustment: %w", err),
			)
		}
		return nil
	}); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	return stored, nil
}

// SetBalanceRealizedPnl writes the current realized-P&L control-plane value for
// one per-(account, asset) balance row through the adjustment history path.
func (n *localNode) SetBalanceRealizedPnl(
	ctx context.Context, key Key, asset string, realizedPnl string,
	caller domain.Caller,
) (domain.Balance, error) {
	rec, err := n.ApplyAdjustment(
		ctx,
		key,
		domain.ExternalID(""),
		domain.AdjustmentRequest{Asset: asset, RealizedPnl: realizedPnl},
		caller,
	)
	if err != nil {
		return domain.Balance{}, err
	}
	return n.balanceFromRealizedPnlRecord(ctx, key, rec, realizedPnl)
}

// ImportPositionSnapshot imports a persisted balance snapshot through the
// engine adjustment path, then stores the full snapshot. The engine has no
// setter for cumulative realized P&L, so the adjustment synchronizes
// available/held/incoming/average-entry-price while Officer persists the
// historical realized_pnl value from the snapshot.
func (n *localNode) ImportPositionSnapshot(
	ctx context.Context, key Key, externalID domain.ExternalID,
	snapshot domain.Balance, caller domain.Caller,
) (domain.AccountAdjustmentRecord, error) {
	// Register the account and asset and rebuild pre-lane (see ApplyAdjustment).
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, key.Account, "position snapshot import", caller, snapshot.Asset,
	); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	if err := n.ensureAdjustmentExternalIDUnused(ctx, externalID); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	accountID, err := n.accountDiagnosticID(ctx, key.Account)
	if err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	defer done()

	var stored domain.AccountAdjustmentRecord
	if err := eng.RunAccountSynchronized(ctx, key.Account, func(lane engine.AccountLane) error {
		req := domain.AdjustmentRequest{
			Asset:             snapshot.Asset,
			AverageEntryPrice: snapshot.AverageEntryPrice,
			Balance: &domain.AdjustmentAmount{
				Mode: domain.AdjustmentModeAbsolute, Value: snapshot.Available,
			},
			Held: &domain.AdjustmentAmount{
				Mode: domain.AdjustmentModeAbsolute, Value: snapshot.Held,
			},
			Incoming: &domain.AdjustmentAmount{
				Mode: domain.AdjustmentModeAbsolute, Value: snapshot.Incoming,
			},
		}
		result, err := lane.ApplyAccountAdjustment(ctx, key.Account, req)
		if err != nil {
			return fmt.Errorf("apply position snapshot adjustment: %w", err)
		}
		if adjustmentResultNoChange(result) {
			return domain.ErrNoChange
		}

		rec := domain.AccountAdjustmentRecord{
			ExternalID: externalID,
			Account:    key.Account,
			Source:     caller.Source,
			Principal:  caller.Principal,
			Request:    req,
			Accepted:   result.Accepted,
			Rejected:   result.Rejected,
			Asset:      snapshot.Asset,
		}

		var balance *domain.Balance
		var deleteBalance *store.BalanceKey
		if result.Accepted != nil {
			_, _, err := n.realm.GetBalance(ctx, key.Account, snapshot.Asset)
			if err != nil {
				return fmt.Errorf("read balance for position snapshot: %w", err)
			}
			balanceValue := snapshot
			balanceValue.Account = key.Account
			balance, deleteBalance = balanceSnapshotCommand(balanceValue)
		}

		auditSnapshot := snapshot
		auditSnapshot.Account = key.Account
		audit := n.auditEntry(caller, store.AuditEntry{
			Action:  domain.AuditActionAdjustment,
			Account: key.Account,
			Detail:  importPositionSnapshotDetail(auditSnapshot, result.Accepted != nil),
		})
		stored, err = n.realm.RecordAccountAdjustment(ctx, store.AccountAdjustmentPersistence{
			UpsertBalance: balance,
			DeleteBalance: deleteBalance,
			Adjustment:    rec,
			Audit:         audit,
		})
		if err != nil {
			return n.fatalPostEnginePersistence(
				"record position snapshot adjustment",
				accountID,
				fmt.Errorf("record position snapshot adjustment: %w", err),
			)
		}
		return nil
	}); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	return stored, nil
}

func adjustmentResultNoChange(result engine.AdjustmentResult) bool {
	return result.Accepted == nil && result.Rejected == nil
}

func adjustmentRequestEngineOnly(req domain.AdjustmentRequest) bool {
	return req.Balance != nil ||
		req.Held != nil ||
		req.Incoming != nil ||
		req.BalanceBounds != nil ||
		req.HeldBounds != nil ||
		req.IncomingBounds != nil ||
		req.AverageEntryPrice != ""
}

func (n *localNode) recordRealizedPnlOnlyAdjustment(
	ctx context.Context,
	key Key,
	accountID string,
	externalID domain.ExternalID,
	req domain.AdjustmentRequest,
	caller domain.Caller,
) (domain.AccountAdjustmentRecord, error) {
	// This path bypasses the engine, so the engine seam that rejects an empty or
	// malformed asset never runs; validate here before persisting a balance row
	// keyed by (account, asset).
	if err := domain.ValidateAsset(req.Asset); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	prev, _, err := n.realm.GetBalance(ctx, key.Account, req.Asset)
	if err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf(
			"read balance for realized pnl adjustment: %w", err)
	}
	// The realized P&L is persisted as an absolute; the delta is its signed change
	// from the previous stored value so the recorded outcome stays consistent.
	delta, err := subtractDecimals(req.RealizedPnl, prev.RealizedPnl)
	if err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	rec := domain.AccountAdjustmentRecord{
		ExternalID: externalID,
		Account:    key.Account,
		Source:     caller.Source,
		Principal:  caller.Principal,
		Request:    req,
		Accepted: &domain.AdjustmentOutcomeAccepted{
			RealizedPnlResult: req.RealizedPnl,
			RealizedPnlDelta:  delta,
		},
		Asset: req.Asset,
	}
	audit := n.auditEntry(caller, store.AuditEntry{
		Action:  domain.AuditActionAdjustment,
		Account: key.Account,
		Asset:   req.Asset,
		Detail:  balanceRealizedPnlDetail(key.Account, req.Asset, req.RealizedPnl),
	})
	stored, err := n.realm.RecordAccountAdjustment(ctx, store.AccountAdjustmentPersistence{
		RealizedPnl: realizedPnlPersistence(key, req),
		Adjustment:  rec,
		Audit:       audit,
	})
	if err != nil {
		return domain.AccountAdjustmentRecord{}, n.fatalPostEnginePersistence(
			"record realized pnl adjustment",
			accountID,
			fmt.Errorf("record realized pnl adjustment: %w", err),
		)
	}
	return stored, nil
}

func realizedPnlPersistence(
	key Key, req domain.AdjustmentRequest,
) *store.BalanceRealizedPnlPersistence {
	if req.RealizedPnl == "" {
		return nil
	}
	return &store.BalanceRealizedPnlPersistence{
		Account:     key.Account,
		Asset:       req.Asset,
		RealizedPnl: req.RealizedPnl,
	}
}

// realizedPnlPersistedOutcome carries the realized P&L an adjustment actually
// persists and its signed delta from the previous stored value, so the recorded
// outcome reflects what Officer stored rather than the engine's own cross-check
// absolute.
type realizedPnlPersistedOutcome struct {
	Result string
	Delta  string
}

func (n *localNode) adjustedBalanceCommand(
	ctx context.Context, key Key, req domain.AdjustmentRequest,
	outcome domain.AdjustmentOutcomeAccepted,
) (*domain.Balance, *store.BalanceKey, realizedPnlPersistedOutcome, error) {
	prev, _, err := n.realm.GetBalance(ctx, key.Account, req.Asset)
	if err != nil {
		return nil, nil, realizedPnlPersistedOutcome{},
			fmt.Errorf("read balance for adjustment: %w", err)
	}
	realizedPnl, err := domain.AddDecimals(prev.RealizedPnl, outcome.RealizedPnlDelta)
	if err != nil {
		return nil, nil, realizedPnlPersistedOutcome{},
			fmt.Errorf("accumulate realized pnl: %w", err)
	}
	// A supplied realized P&L overrides the accumulated value; recompute the
	// delta against the previous stored value so the persisted result and delta
	// stay mutually consistent.
	delta := outcome.RealizedPnlDelta
	if req.RealizedPnl != "" {
		realizedPnl = req.RealizedPnl
		delta, err = subtractDecimals(realizedPnl, prev.RealizedPnl)
		if err != nil {
			return nil, nil, realizedPnlPersistedOutcome{}, err
		}
	}
	balance := domain.Balance{
		Account:           key.Account,
		Asset:             req.Asset,
		Available:         pick(outcome.BalanceResult, prev.Available),
		Held:              pick(outcome.HeldResult, prev.Held),
		Incoming:          pick(outcome.IncomingResult, prev.Incoming),
		RealizedPnl:       realizedPnl,
		AverageEntryPrice: pick(req.AverageEntryPrice, prev.AverageEntryPrice),
	}
	upsert, deleteKey := balanceSnapshotCommand(balance)
	return upsert, deleteKey, realizedPnlPersistedOutcome{
		Result: realizedPnl,
		Delta:  delta,
	}, nil
}

// subtractDecimals returns base minus subtrahend as an exact decimal string. An
// empty operand is treated as zero. Returns ErrInvalid for a non-empty,
// non-decimal operand.
func subtractDecimals(base, subtrahend string) (string, error) {
	baseD := decimal.Zero
	subtrahendD := decimal.Zero
	var err error
	if base != "" {
		baseD, err = decimal.NewFromString(base)
		if err != nil {
			return "", fmt.Errorf("base %q is not a valid decimal: %w", base, domain.ErrInvalid)
		}
	}
	if subtrahend != "" {
		subtrahendD, err = decimal.NewFromString(subtrahend)
		if err != nil {
			return "", fmt.Errorf(
				"subtrahend %q is not a valid decimal: %w", subtrahend, domain.ErrInvalid)
		}
	}
	return baseD.Sub(subtrahendD).String(), nil
}

func (n *localNode) balanceFromRealizedPnlRecord(
	ctx context.Context,
	key Key,
	rec domain.AccountAdjustmentRecord,
	realizedPnl string,
) (domain.Balance, error) {
	if rec.Request.RealizedPnl != "" {
		realizedPnl = rec.Request.RealizedPnl
	}
	if rec.Request.Asset == "" {
		return domain.Balance{}, fmt.Errorf("realized pnl adjustment missing asset")
	}
	stored, ok, err := n.realm.GetBalance(ctx, key.Account, rec.Request.Asset)
	if err != nil {
		return domain.Balance{}, fmt.Errorf("read stored realized pnl balance: %w", err)
	}
	if ok {
		return stored, nil
	}
	return domain.Balance{
		Account:     key.Account,
		Asset:       rec.Request.Asset,
		RealizedPnl: realizedPnl,
	}, nil
}

func balanceSnapshotCommand(balance domain.Balance) (*domain.Balance, *store.BalanceKey) {
	if balanceIsEmpty(balance) {
		return nil, &store.BalanceKey{Account: balance.Account, Asset: balance.Asset}
	}
	return &balance, nil
}

func balanceIsEmpty(balance domain.Balance) bool {
	return decimalZeroOrEmpty(balance.Available) &&
		decimalZeroOrEmpty(balance.Held) &&
		decimalZeroOrEmpty(balance.Incoming) &&
		decimalZeroOrEmpty(balance.RealizedPnl) &&
		decimalZeroOrEmpty(balance.AverageEntryPrice)
}

func decimalZeroOrEmpty(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := decimal.NewFromString(value)
	return err == nil && parsed.IsZero()
}

// balanceSettlementsFrom maps the engine's per-asset outcomes onto the domain
// settlement carrier RecordOrderSettlement consumes. The store persists the
// engine-returned result fields and accumulates realized-P&L deltas inside the
// settlement tx. The engine emits at most one outcome per asset (see
// engine.BalanceOutcome), so the slice carries no duplicate-asset entries that
// would double-count realized P&L.
func balanceSettlementsFrom(outcomes []engine.BalanceOutcome) []domain.BalanceSettlement {
	if len(outcomes) == 0 {
		return nil
	}
	settlements := make([]domain.BalanceSettlement, 0, len(outcomes))
	for _, outcome := range outcomes {
		settlements = append(settlements, domain.BalanceSettlement{
			Asset:   outcome.Asset,
			Outcome: outcome.Outcome,
		})
	}
	return settlements
}

func accountBlockSettlementsFrom(
	order domain.ExternalID, blocks []domain.ExecutionAccountBlock,
) []domain.ExecutionAccountBlock {
	if len(blocks) == 0 {
		return nil
	}
	settlements := make([]domain.ExecutionAccountBlock, 0, len(blocks))
	for _, block := range blocks {
		block.Reason = engineBlockReason(order, block)
		settlements = append(settlements, block)
	}
	return settlements
}

// fillSettlementEvent builds one lifecycle event stamped with the caller for a
// fill/settlement tx. The store assigns the id and timestamp on append.
func fillSettlementEvent(
	order domain.ExternalID, typ domain.OrderEventType, caller domain.Caller, payload domain.OrderEventPayload,
) domain.OrderEvent {
	return domain.OrderEvent{
		Order:     order,
		Type:      typ,
		Source:    caller.Source,
		Principal: caller.Principal,
		Payload:   payload,
	}
}

func stampExecutionReportPersistence(
	persistence engine.ExecutionReportPersistence,
	caller domain.Caller,
	request *domain.ExecutionReportRequest,
) engine.ExecutionReportPersistence {
	persistence.Commission = request.Commission
	for i := range persistence.Events {
		persistence.Events[i].Source = caller.Source
		persistence.Events[i].Principal = caller.Principal
		persistence.Events[i].Payload.ExecutionReport = request
		persistence.Events[i].Payload.FillQuantity = request.FillQuantity
		persistence.Events[i].Payload.FillPrice = request.FillPrice
		persistence.Events[i].Payload.FillLockPrice = request.LockPrice
		persistence.Events[i].Payload.LeavesQuantity = request.LeavesQuantity
		persistence.Events[i].Payload.OrderStatus = string(request.OrderStatus)
		persistence.Events[i].Payload.Commission = request.Commission
	}
	if persistence.Trade != nil {
		persistence.Trade.Source = caller.Source
		persistence.Trade.Principal = caller.Principal
	}
	return persistence
}

func accountBlockPayload(blocks []domain.ExecutionAccountBlock) domain.OrderEventPayload {
	if len(blocks) == 0 {
		return domain.OrderEventPayload{}
	}
	// The engine currently emits at most one account block per fill. Keep the
	// event payload singular and raw: account state stores engineBlockReason
	// with order context, while this event preserves the engine block contract.
	block := blocks[0]
	return domain.OrderEventPayload{
		RejectCode:    block.Code,
		RejectScope:   "account",
		RejectReason:  block.Reason,
		RejectDetails: block.Details,
	}
}

// pick returns next when it is non-empty, otherwise prev. It carries an
// unchanged balance field forward when the outcome reported no value for it.
func pick(next, prev string) string {
	if next != "" {
		return next
	}
	return prev
}

// ListBalances returns the balance rows filtered by the non-empty account and
// asset.
func (n *localNode) ListBalances(
	ctx context.Context, account domain.AccountID, asset string,
) ([]domain.Balance, error) {
	balances, err := n.realm.ListBalances(ctx, account, asset)
	if err != nil {
		return nil, fmt.Errorf("list balances: %w", err)
	}
	return balances, nil
}

// ListBalanceRows returns the balance rows filtered by the typed list filter.
func (n *localNode) ListBalanceRows(
	ctx context.Context, filter store.BalanceListFilter,
) (store.BalanceListPage, error) {
	balances, err := n.realm.ListBalanceRows(ctx, filter)
	if err != nil {
		return store.BalanceListPage{}, fmt.Errorf("list balance rows: %w", err)
	}
	return balances, nil
}

// GetBalance returns the balance for (account, asset).
func (n *localNode) GetBalance(
	ctx context.Context, account domain.AccountID, asset string,
) (domain.Balance, bool, error) {
	balance, ok, err := n.realm.GetBalance(ctx, account, asset)
	if err != nil {
		return domain.Balance{}, false, fmt.Errorf("get balance: %w", err)
	}
	return balance, ok, nil
}

// ListAdjustments returns the most recent n adjustments for an account.
func (n *localNode) ListAdjustments(
	ctx context.Context, account domain.AccountID, source domain.Source, count int,
) ([]domain.AccountAdjustmentRecord, error) {
	records, err := n.realm.ListAdjustments(ctx, account, source, count)
	if err != nil {
		return nil, fmt.Errorf("list adjustments: %w", err)
	}
	return records, nil
}

// ListAdjustmentRows returns adjustments matching filter.
func (n *localNode) ListAdjustmentRows(
	ctx context.Context, filter store.AdjustmentListFilter,
) (store.AdjustmentListPage, error) {
	page, err := n.realm.ListAdjustmentRows(ctx, filter)
	if err != nil {
		return store.AdjustmentListPage{}, fmt.Errorf("list adjustment rows: %w", err)
	}
	return page, nil
}
