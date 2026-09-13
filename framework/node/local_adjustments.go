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
	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/model"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

type adjustmentChainState struct {
	ctx                  context.Context
	adapter              engine.AccountChainAdapter
	account              domain.AccountID
	reqs                 []domain.AdjustmentRequest
	adjustments          []model.AccountAdjustment
	result               engine.AdjustmentResult
	stored               domain.AccountAdjustmentRecord
	balanceExists        bool
	err                  error
	engineApplied        bool
	persistenceCompleted bool
}

func (n *localNode) auditEntry(
	caller domain.Caller, entry store.AuditEntry,
) store.AuditEntry {
	entry.Actor = caller.Principal
	entry.Source = caller.Source
	return entry
}

func (n *localNode) ensureAdjustmentExternalIDUnused(
	ctx context.Context, externalID domain.ExternalID,
) error {
	if externalID.IsZero() {
		return nil
	}
	page, err := n.realm.ListAdjustmentRows(ctx, store.AdjustmentListFilter{
		ExternalID: externalID,
		Page:       store.PageSpec{Limit: 1},
	})
	if err != nil {
		return fmt.Errorf("check adjustment external id: %w", err)
	}
	if len(page.Rows) > 0 {
		return fmt.Errorf("adjustment %q: %w", externalID, domain.ErrAlreadyExists)
	}
	return nil
}

func snapshotAdjustmentRequest(snapshot domain.Balance) domain.AdjustmentRequest {
	available := snapshot.Available
	if available == "" {
		available = "0"
	}
	held := snapshot.Held
	if held == "" {
		held = "0"
	}
	incoming := snapshot.Incoming
	if incoming == "" {
		incoming = "0"
	}
	req := domain.AdjustmentRequest{
		Asset:                 snapshot.Asset,
		AverageEntryPrice:     snapshot.AverageEntryPrice,
		RealizedPnlHaltReason: snapshot.RealizedPnlHaltReason,
		Balance: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeAbsolute, Value: available,
		},
		Held: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeAbsolute, Value: held,
		},
		Incoming: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeAbsolute, Value: incoming,
		},
	}
	if snapshot.RealizedPnlHaltReason == "" {
		req.RealizedPnl = snapshot.RealizedPnl
	}
	return req
}

// ApplyAdjustment applies one spot-funds adjustment through the engine, which
// is the authority for the resulting holdings. missing decides whether an
// adjustment naming an unknown account registers it or is rejected; an asset
// that does not exist yet is always auto-created, so an operator can fund a
// fresh asset in one call. On accept it writes the recomputed balance snapshot
// and the accepted record; on reject it records the rejected adjustment and
// leaves balances unchanged. The engine outcome is authoritative: when the
// engine reports neither an accepted nor a rejected outcome the call returns
// ErrNoChange and records no adjustment, balance, or audit, even when the
// request carried a realized P&L.
func (n *localNode) ApplyAdjustment(
	ctx context.Context, account domain.AccountID, externalID domain.ExternalID,
	req domain.AdjustmentRequest, missing domain.MissingAccountPolicy,
	caller domain.Caller,
) (domain.AccountAdjustmentRecord, error) {
	if err := domain.ValidateAsset(req.Asset); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	// Resolve the account and auto-create the asset before entering the chain,
	// publishing a new account's stable id through the live resolver first.
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, account, missing, "adjustment", caller, req.Asset,
	); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	if err := n.ensureAdjustmentExternalIDUnused(ctx, externalID); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	defer done()
	source, err := eng.AccountID(account)
	if err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	state := &adjustmentChainState{
		ctx:           ctx,
		adapter:       eng,
		account:       account,
		reqs:          []domain.AdjustmentRequest{req},
		balanceExists: true,
	}
	begin := func(context.Context) (*adjustmentChainState, error) {
		if ctxErr := state.ctx.Err(); ctxErr != nil {
			state.err = fmt.Errorf("engine: adjustment cancelled: %w", ctxErr)
			return nil, state.err
		}
		requiresExistingBalance := adjustmentRequiresExistingBalance(req)
		if requiresExistingBalance {
			_, exists, err := n.realm.GetBalance(
				ctx, account, req.Asset,
			)
			if err != nil {
				state.err = fmt.Errorf(
					"read balance before average adjustment: %w", err,
				)
				return nil, state.err
			}
			state.balanceExists = exists
		}
		adjustments, adjustmentErr := eng.AccountAdjustmentModels(state.reqs)
		if adjustmentErr != nil {
			state.err = fmt.Errorf("apply adjustment: %w", adjustmentErr)
			return nil, state.err
		}
		state.adjustments = adjustments
		return state, nil
	}
	builder := asyncengine.Chain(source, begin)
	builder.ApplyAccountAdjustment(
		asyncengine.AccountAdjustmentHooks[*adjustmentChainState]{
			Adjustments: func(
				_ context.Context,
				state *adjustmentChainState,
			) ([]model.AccountAdjustment, error) {
				return state.adjustments, nil
			},
			OnAdjusted: func(
				_ context.Context,
				state *adjustmentChainState,
				batch accountadjustment.BatchResult,
			) error {
				state.engineApplied = true
				results, batchReject, err :=
					state.adapter.AppliedAccountAdjustmentBatch(
						state.account, state.reqs, batch,
					)
				if err != nil {
					state.err = fmt.Errorf("apply adjustment: %w", err)
					return state.err
				}
				if batchReject != nil {
					state.result = engine.AdjustmentResult{Rejected: batchReject}
					return nil
				}
				if len(results) == 0 {
					return nil
				}
				if len(results) != 1 {
					state.err = fmt.Errorf(
						"apply adjustment: engine: adjustment returned %d outcomes",
						len(results),
					)
					return state.err
				}
				state.result = results[0]
				return nil
			},
		},
	)
	runner := builder.Then(func(
		_ context.Context, state *adjustmentChainState,
	) error {
		if err := n.mirrorAdjustmentAccountBlocks(
			ctx, state.result.AccountBlocks,
		); err != nil {
			state.err = err
			return err
		}
		if adjustmentRequiresExistingBalance(req) &&
			!state.balanceExists && state.result.Accepted != nil {
			state.err = domain.ErrNoChange
			return state.err
		}
		if adjustmentResultNoChange(state.result) {
			state.err = domain.ErrNoChange
			return state.err
		}

		// A non-zero externalID is the caller-supplied handle, carried verbatim onto
		// the record so the store uses it (else the store mints one on append).
		rec := domain.AccountAdjustmentRecord{
			ExternalID: externalID,
			Account:    account,
			Source:     caller.Source,
			Principal:  caller.Principal,
			Request:    req,
			Accepted:   state.result.Accepted,
			Rejected:   state.result.Rejected,
			Asset:      req.Asset,
		}

		var balance *domain.Balance
		var deleteBalance *store.BalanceKey
		if state.result.Accepted != nil {
			adjustedBalance, deleteKey, balanceErr :=
				n.adjustedBalanceCommand(ctx, account, req.Asset, *state.result.Accepted)
			if balanceErr != nil {
				state.err = balanceErr
				return balanceErr
			}
			balance = adjustedBalance
			deleteBalance = deleteKey
		}

		audit := n.auditEntry(caller, store.AuditEntry{
			Action:  domain.AuditActionAdjustment,
			Account: account,
			Asset:   req.Asset,
			Detail: adjustmentDetail(
				account, req.Asset, state.result.Accepted != nil,
			),
		})
		stored, recordErr := n.realm.RecordAccountAdjustment(
			ctx, store.AccountAdjustmentPersistence{
				UpsertBalance: balance,
				DeleteBalance: deleteBalance,
				Adjustment:    rec,
				Audit:         audit,
			})
		if recordErr != nil {
			state.err = n.fatalPostEnginePersistence(
				"record account adjustment",
				account,
				fmt.Errorf("record adjustment: %w", recordErr),
			)
			return state.err
		}
		state.stored = stored
		state.persistenceCompleted = true
		return nil
	}).Finally(func(
		_ context.Context,
		state *adjustmentChainState,
		outcome asyncengine.ChainOutcome,
	) error {
		state.err = n.accountChainTerminalError(
			"apply adjustment",
			account,
			state.err,
			state.engineApplied,
			state.persistenceCompleted,
			outcome,
		)
		return nil
	})
	_, chainErr := runner.Run(
		ctx, eng.AsyncEngine(),
	).Await(context.Background())
	if runErr := accountChainRunError(
		"apply adjustment", state.err, chainErr,
	); runErr != nil {
		return domain.AccountAdjustmentRecord{}, runErr
	}
	return state.stored, nil
}

// SetBalanceRealizedPnl writes the current realized-P&L control-plane value for
// one per-(account, asset) balance row through the adjustment history path.
// missing is handled as in ApplyAdjustment, which this runs through.
func (n *localNode) SetBalanceRealizedPnl(
	ctx context.Context, account domain.AccountID, asset string, realizedPnl string,
	missing domain.MissingAccountPolicy, caller domain.Caller,
) (domain.Balance, error) {
	rec, err := n.ApplyAdjustment(
		ctx,
		account,
		domain.ExternalID(""),
		domain.AdjustmentRequest{Asset: asset, RealizedPnl: realizedPnl},
		missing,
		caller,
	)
	if err != nil {
		return domain.Balance{}, err
	}
	return n.balanceFromRealizedPnlRecord(ctx, account, rec, realizedPnl)
}

func adjustmentResultNoChange(result engine.AdjustmentResult) bool {
	return result.Accepted == nil && result.Rejected == nil
}

// mirrorAdjustmentAccountBlocks records the kill-switch blocks the engine
// latched while committing an adjustment batch, so the store agrees with the
// engine within this same operation: an account the engine already refuses to
// trade must never read as tradable while Officer waits for a later fill or
// other post-trade event to rediscover the block.
//
// The blocks are mirrored before the no-change check, because a force-set that
// only trips the kill-switch reports no accepted outcome yet still blocks. They
// are batch-level and repeated on every result of a batch; the sink dedups by
// account. Its failures are fatal by contract and are propagated unchanged.
func (n *localNode) mirrorAdjustmentAccountBlocks(
	ctx context.Context, blocks []domain.AccountBlock,
) error {
	if len(blocks) == 0 {
		return nil
	}
	return n.mirrorPolicyConfigurationBlocks(
		ctx, domain.PolicySpotFundsPnlBoundsKillSwitch, blocks,
	)
}

func (n *localNode) adjustedBalanceCommand(
	ctx context.Context, account domain.AccountID, asset string,
	outcome domain.AdjustmentOutcomeAccepted,
) (*domain.Balance, *store.BalanceKey, error) {
	balance, err := n.adjustedBalance(ctx, account, asset, outcome)
	if err != nil {
		return nil, nil, err
	}
	upsert, deleteKey := balanceSnapshotCommand(balance)
	return upsert, deleteKey, nil
}

func (n *localNode) adjustedBalance(
	ctx context.Context, account domain.AccountID, asset string,
	outcome domain.AdjustmentOutcomeAccepted,
) (domain.Balance, error) {
	prev, _, err := n.realm.GetBalance(ctx, account, asset)
	if err != nil {
		return domain.Balance{}, fmt.Errorf("read balance for adjustment: %w", err)
	}
	realizedPnl := pick(outcome.RealizedPnlResult, prev.RealizedPnl)
	balance := domain.Balance{
		Account:     account,
		Asset:       asset,
		Available:   pick(outcome.BalanceResult, prev.Available),
		Held:        pick(outcome.HeldResult, prev.Held),
		Incoming:    pick(outcome.IncomingResult, prev.Incoming),
		RealizedPnl: realizedPnl,
		RealizedPnlHaltReason: pickPnlHaltReason(
			outcome.RealizedPnlHaltReason,
			prev.RealizedPnlHaltReason,
			outcome.RealizedPnlResult != "",
		),
		AverageEntryPrice: pick(outcome.AverageEntryPrice, prev.AverageEntryPrice),
	}
	return balance, nil
}

func (n *localNode) balanceFromRealizedPnlRecord(
	ctx context.Context,
	code domain.AccountID,
	rec domain.AccountAdjustmentRecord,
	realizedPnl string,
) (domain.Balance, error) {
	if rec.Accepted != nil && rec.Accepted.RealizedPnlResult != "" {
		realizedPnl = rec.Accepted.RealizedPnlResult
	} else if rec.Request.RealizedPnl != "" {
		realizedPnl = rec.Request.RealizedPnl
	}
	if rec.Request.Asset == "" {
		return domain.Balance{}, fmt.Errorf("realized pnl adjustment missing asset")
	}
	stored, ok, err := n.realm.GetBalance(ctx, code, rec.Request.Asset)
	if err != nil {
		return domain.Balance{}, fmt.Errorf("read stored realized pnl balance: %w", err)
	}
	if ok {
		return stored, nil
	}
	account, ok, err := n.realm.GetAccount(ctx, code)
	if err != nil {
		return domain.Balance{}, fmt.Errorf("read account for realized pnl balance: %w", err)
	}
	if !ok {
		return domain.Balance{}, fmt.Errorf("account %q: %w", code, domain.ErrNotFound)
	}
	return domain.Balance{
		Account:         code,
		Asset:           rec.Request.Asset,
		RealizedPnl:     realizedPnl,
		AccountCurrency: account.EffectiveCurrency,
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
		balance.RealizedPnlHaltReason == "" &&
		decimalZeroOrEmpty(balance.AverageEntryPrice)
}

func pickPnlHaltReason(
	next, prev domain.PnlHaltReason, successfulPnlResult bool,
) domain.PnlHaltReason {
	if next != "" {
		return next
	}
	if successfulPnlResult {
		return ""
	}
	return prev
}

func decimalZeroOrEmpty(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := decimal.NewFromString(value)
	return err == nil && parsed.IsZero()
}

// adjustmentRequiresExistingBalance reports adjustments that only change the
// average entry price without creating any economic position state.
func adjustmentRequiresExistingBalance(req domain.AdjustmentRequest) bool {
	if req.AverageEntryPrice == "" || req.RealizedPnl != "" || req.RealizedPnlHaltReason != "" {
		return false
	}
	for _, amount := range []*domain.AdjustmentAmount{req.Balance, req.Held, req.Incoming} {
		if amount == nil {
			continue
		}
		if amount.Mode != domain.AdjustmentModeAbsolute && amount.Mode != domain.AdjustmentModeDelta {
			return false
		}
		value, err := decimal.NewFromString(amount.Value)
		if err != nil || !value.IsZero() {
			return false
		}
	}
	return true
}

// balanceSettlementsFrom maps engine per-asset outcomes onto the settlement
// carrier, including position P&L results and P&L-calculation halt reasons.
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
	// event payload singular and raw: it preserves the engine block contract, the
	// same fields the account row stores verbatim.
	block := blocks[0]
	return domain.OrderEventPayload{
		RejectCode:    block.Code,
		RejectScope:   "account",
		RejectPolicy:  block.Policy,
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
	if err := filter.Validate(); err != nil {
		return store.BalanceListPage{}, err
	}
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
