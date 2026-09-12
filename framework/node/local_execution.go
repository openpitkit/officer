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
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

type executionReportChainState struct {
	ctx                  context.Context
	adapter              engine.AccountChainAdapter
	accountID            param.AccountID
	in                   domain.ExecutionReportInput
	request              *domain.ExecutionReportRequest
	order                domain.Order
	reservationRemainder string
	report               model.ExecutionReport
	result               engine.ExecutionReportResult
	err                  error
	engineApplied        bool
	persistenceCompleted bool
	overFilled           bool
	forcedTerminalBypass bool
}

func (n *localNode) accountChainTerminalError(
	operation string,
	account domain.AccountID,
	stateErr error,
	engineApplied bool,
	persistenceCompleted bool,
	outcome asyncengine.ChainOutcome,
) error {
	if !engineApplied || outcome.Err == nil || errors.Is(stateErr, domain.ErrNoChange) {
		return stateErr
	}
	switch stateErr.(type) {
	case internalPostCommitNodeMutationFailure,
		*internalPostCommitNodeMutationFailure:
		return stateErr
	}
	cause := chainRootCause(outcome.Err)
	if persistenceCompleted {
		return n.fatalPostCommitAudit(operation, account, cause)
	}
	return n.fatalPostEnginePersistence(operation, account, cause)
}

func accountChainRunError(operation string, stateErr, chainErr error) error {
	if stateErr != nil {
		if errors.Is(chainErr, asyncengine.ErrChainRetryUnsafe) &&
			!errors.Is(stateErr, asyncengine.ErrChainRetryUnsafe) {
			return errors.Join(stateErr, asyncengine.ErrChainRetryUnsafe)
		}
		return stateErr
	}
	if chainErr != nil {
		return fmt.Errorf("%s: %w", operation, chainErr)
	}
	return nil
}

func (n *localNode) runExecutionReportChain(
	ctx context.Context,
	eng engine.Engine,
	source param.AccountID,
	state *executionReportChainState,
	begin func(context.Context) (*executionReportChainState, error),
	persist func(*executionReportChainState) error,
	operation string,
	account domain.AccountID,
) error {
	builder := asyncengine.Chain(source, begin)
	builder.ApplyExecutionReport(
		asyncengine.ExecutionReportHooks[*executionReportChainState]{
			Report: func(
				_ context.Context,
				state *executionReportChainState,
			) (model.ExecutionReport, error) {
				return state.report, nil
			},
			OnSettled: func(
				_ context.Context,
				state *executionReportChainState,
				postTrade pretrade.PostTradeResult,
			) error {
				state.engineApplied = true
				result, err := state.adapter.SettledExecutionReport(
					state.in, state.accountID, postTrade,
				)
				if err != nil {
					state.err = fmt.Errorf("%s: %w", operation, err)
					return state.err
				}
				state.result = result
				return nil
			},
		},
	)
	runner := builder.Then(func(
		_ context.Context, state *executionReportChainState,
	) error {
		state.err = persist(state)
		return state.err
	}).Finally(func(
		_ context.Context,
		state *executionReportChainState,
		outcome asyncengine.ChainOutcome,
	) error {
		state.err = n.accountChainTerminalError(
			operation,
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
	return accountChainRunError(operation, state.err, chainErr)
}

// ApplyExecutionReport serializes every report on its account pipeline.
// Reports carrying a fill or commission, and reports targeting a terminal
// status, settle through the engine; all other non-terminal reports only record
// workflow state.
// Engine settlement writes report events, an optional trade, per-asset
// balances, engine-block UPDATEs, and reflected status/leaves atomically.
func (n *localNode) ApplyExecutionReport(
	ctx context.Context, key Key, in domain.ExecutionReportInput, caller domain.Caller,
) (engine.ExecutionReportResult, error) {
	return n.applyExecutionReport(ctx, key, in, caller, nil)
}

func (n *localNode) ApplyExecutionReportWithAttestation(
	ctx context.Context,
	key Key,
	in domain.ExecutionReportInput,
	caller domain.Caller,
	attest store.EventAttestor,
) (engine.ExecutionReportResult, error) {
	return n.applyExecutionReport(ctx, key, in, caller, attest)
}

func (n *localNode) applyExecutionReport(
	ctx context.Context,
	key Key,
	in domain.ExecutionReportInput,
	caller domain.Caller,
	attest store.EventAttestor,
) (engine.ExecutionReportResult, error) {
	request := domain.ExecutionReportRequestFromInput(in)
	status := in.OrderStatus
	// The report is a fact from the venue; caller cancellation must not cancel it.
	ctx = context.WithoutCancel(ctx)
	requiresEngine, err := domain.ExecutionReportRequiresEngine(in)
	if err != nil {
		return engine.ExecutionReportResult{}, err
	}
	// Report ids are realm-wide while account chains only serialize one account.
	// Keep the availability check and persistence ordered across all chains.
	n.reportMu.Lock()
	defer n.reportMu.Unlock()
	if !requiresEngine {
		return n.recordWorkflowExecutionReport(ctx, in, caller, attest, request)
	}

	routeDetail, err := n.realm.GetOrder(ctx, in.Order)
	if err != nil {
		return engine.ExecutionReportResult{}, fmt.Errorf("get execution report order: %w", err)
	}
	account := routeDetail.Order.Account

	// Register the order account and assets and rebuild before resolving the
	// stable account-chain key.
	assets := []string{routeDetail.Order.BaseAsset, routeDetail.Order.QuoteAsset}
	if in.Commission != nil {
		assets = append(assets, in.Commission.Currency)
	}
	// The account comes from the stored order, not from the request, so there is
	// no missing-account choice to make: it is registered unconditionally.
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, account, domain.MissingAccountCreate, "execution report", caller,
		assets...,
	); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return engine.ExecutionReportResult{}, err
	}
	defer done()
	if err := n.requireExecutionReportIDAvailable(ctx, in.ExternalID); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	source, err := eng.AccountID(account)
	if err != nil {
		return engine.ExecutionReportResult{}, err
	}
	state := &executionReportChainState{
		ctx:       ctx,
		adapter:   eng,
		accountID: source,
	}
	begin := func(context.Context) (*executionReportChainState, error) {
		detail, err := n.realm.GetOrder(ctx, in.Order)
		if err != nil {
			state.err = fmt.Errorf("get execution report order: %w", err)
			return nil, state.err
		}
		if err := domain.RequireOrderModifiable(detail.Order.Status, in.Force); err != nil {
			state.err = err
			return nil, err
		}
		state.forcedTerminalBypass = in.Force &&
			domain.OrderStatusTerminal(detail.Order.Status)
		state.order = detail.Order
		state.in = in
		state.in.Account = detail.Order.Account
		state.in.BaseAsset = detail.Order.BaseAsset
		state.in.QuoteAsset = detail.Order.QuoteAsset
		state.in.Side = detail.Order.Side
		if len(state.in.Lock) == 0 && len(detail.Order.Lock) > 0 {
			state.in.Lock = detail.Order.Lock
		}
		reservationRemainder, err := executionReservedQuantity(state.in, detail)
		if err != nil {
			state.err = err
			return nil, err
		}
		state.reservationRemainder = reservationRemainder
		state.overFilled = executionReportOverFilled(state.in, detail)
		state.report, err = eng.ExecutionReportModel(
			state.in, state.reservationRemainder,
		)
		if err != nil {
			state.err = fmt.Errorf("apply execution report: %w", err)
			return nil, state.err
		}
		return state, nil
	}
	persist := func(state *executionReportChainState) error {
		if state.result.Persistence == nil {
			return fmt.Errorf("apply execution report returned no persistence write set: %w", domain.ErrInvalid)
		}
		persistence := stampExecutionReportPersistence(
			*state.result.Persistence, caller, request,
		)
		reservedQuantity, err := reservationAfterOutcomes(state.order, persistence.Balances)
		if err != nil {
			return n.fatalPostEnginePersistence("record execution reservation", account, err)
		}
		settlement := domain.OrderSettlement{
			ReservedQuantity:     reservedQuantity,
			Account:              state.in.Account,
			Order:                state.in.Order,
			ReportID:             &state.in.ExternalID,
			OrderStatus:          persistence.OrderStatus,
			AccountPnl:           persistence.AccountPnl,
			AccountPnlHaltReason: persistence.AccountPnlHaltReason,
			Leaves:               persistence.Leaves,
			Balances:             persistence.Balances,
			Events:               persistence.Events,
			Trade:                persistence.Trade,
			Blocks:               persistence.Blocks,
		}
		reportID, err := recordOrderSettlementWithAttestation(
			ctx, n.realm, settlement, attest,
		)
		if err != nil {
			return n.fatalPostEnginePersistence(
				"record execution report",
				account,
				fmt.Errorf("record execution report: %w", err),
			)
		}
		state.result.ReportID = reportID
		state.persistenceCompleted = true

		if err := n.mirrorEngineBlocksAudit(
			ctx, state.in.Order, state.result.Blocks,
		); err != nil {
			return n.fatalPostEnginePersistence(
				"audit execution report engine blocks", account, err,
			)
		}

		detailText := executionReportDetail(
			state.in,
			state.reservationRemainder,
			status,
			len(state.result.Blocks),
		)
		if state.overFilled {
			detailText += " overfill=true preReportReserveQty=" +
				state.order.ReservedQuantity
		}
		if state.forcedTerminalBypass {
			detailText += " forced=true"
		}
		if err := n.audit(ctx, caller, store.AuditEntry{
			Action:  domain.AuditActionExecutionReport,
			Account: state.in.Account,
			Detail:  detailText,
		}); err != nil {
			return n.fatalPostEnginePersistence(
				"audit execution report",
				account,
				fmt.Errorf("audit execution report: %w", err),
			)
		}
		return nil
	}
	if err := n.runExecutionReportChain(
		ctx,
		eng,
		source,
		state,
		begin,
		persist,
		"apply execution report",
		account,
	); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	return state.result, nil
}

// executionReservedQuantity supplies the SDK's reservation context after this
// report's trade. It uses the recorded engine reservation, never venue leaves.
// The engine still applies the complete trade and owns every ledger mutation.
func executionReservedQuantity(
	in domain.ExecutionReportInput, detail domain.OrderDetail,
) (string, error) {
	reserved, err := domain.ParseOpenQuantity(detail.Order.ReservedQuantity)
	if err != nil {
		return "", fmt.Errorf("stored order %s reservation: %w", detail.Order.ExternalID, err)
	}
	if in.FillQuantity != "" && in.FillPrice != "" {
		filled, err := domain.ParseOpenQuantity(in.FillQuantity)
		if err != nil {
			return "", fmt.Errorf("execution report fill quantity: %w: %w", err, domain.ErrInvalid)
		}
		reserved = reserved.Sub(filled)
		// An overfill exhausts this order's reservation. The full venue fill
		// still reaches the engine; its ledger deltas are never capped here.
		if reserved.IsNegative() {
			return "0", nil
		}
	}
	return reserved.String(), nil
}

// reservationAfterOutcomes advances only this order's reservation using the
// engine's base-asset deltas. A block with no movement retains the reserve.
func reservationAfterOutcomes(
	order domain.Order, balances []domain.BalanceSettlement,
) (string, error) {
	reserved, err := domain.ParseOpenQuantity(order.ReservedQuantity)
	if err != nil {
		return "", fmt.Errorf("stored order %s reservation: %w", order.ExternalID, err)
	}
	for _, balance := range balances {
		if balance.Asset != order.BaseAsset {
			continue
		}
		var value string
		switch order.Side {
		case domain.OrderSideBuy:
			value = balance.Outcome.IncomingDelta
		case domain.OrderSideSell:
			value = balance.Outcome.HeldDelta
		default:
			return "", fmt.Errorf("order %s has unsupported side %q", order.ExternalID, order.Side)
		}
		// Omitted delta means the engine did not move this reservation leg.
		if value == "" {
			continue
		}
		delta, err := decimal.NewFromString(value)
		if err != nil {
			return "", fmt.Errorf("order %s reservation delta: %w", order.ExternalID, err)
		}
		reserved = reserved.Add(delta)
	}
	// An overfill may leave negative engine holdings, which remain untouched.
	// There is no positive reservation left for this order to release again.
	if reserved.IsNegative() {
		return "0", nil
	}
	return reserved.String(), nil
}

// executionReportOverFilled reports that this report's own fill exceeds the
// engine reservation recorded for the order, on any fill status. Nothing is
// adjusted because of it - the caller's account of the fill stands and every
// quantity is forwarded and stored verbatim - but the audit row is the only
// place the disagreement can be seen by whoever reconciles the reservation. A
// value neither side can parse is no evidence of a mismatch, so it stays silent.
func executionReportOverFilled(
	in domain.ExecutionReportInput, detail domain.OrderDetail,
) bool {
	if in.FillQuantity == "" || detail.Order.ReservedQuantity == "" {
		return false
	}
	filled, err := domain.ParseOpenQuantity(in.FillQuantity)
	if err != nil {
		return false
	}
	recorded, err := domain.ParseOpenQuantity(detail.Order.ReservedQuantity)
	if err != nil {
		return false
	}
	return filled.GreaterThan(recorded)
}

type workflowExecutionReportChainState struct {
	ctx                  context.Context
	in                   domain.ExecutionReportInput
	settlement           domain.OrderSettlement
	err                  error
	forcedTerminalBypass bool
}

func (n *localNode) recordWorkflowExecutionReport(
	ctx context.Context,
	in domain.ExecutionReportInput,
	caller domain.Caller,
	attest store.EventAttestor,
	request *domain.ExecutionReportRequest,
) (engine.ExecutionReportResult, error) {
	routeDetail, err := n.realm.GetOrder(ctx, in.Order)
	if err != nil {
		return engine.ExecutionReportResult{}, fmt.Errorf("get execution report order: %w", err)
	}
	account := routeDetail.Order.Account
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, account, domain.MissingAccountCreate,
		"workflow execution report", caller,
	); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return engine.ExecutionReportResult{}, err
	}
	defer done()
	if err := n.requireExecutionReportIDAvailable(ctx, in.ExternalID); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	source, err := eng.AccountID(account)
	if err != nil {
		return engine.ExecutionReportResult{}, err
	}
	state := &workflowExecutionReportChainState{ctx: ctx, in: in}
	begin := func(context.Context) (*workflowExecutionReportChainState, error) {
		detail, err := n.realm.GetOrder(ctx, in.Order)
		if err != nil {
			state.err = fmt.Errorf("get execution report order: %w", err)
			return nil, state.err
		}
		if err := domain.RequireOrderModifiable(detail.Order.Status, in.Force); err != nil {
			state.err = err
			return nil, err
		}
		state.forcedTerminalBypass = in.Force &&
			domain.OrderStatusTerminal(detail.Order.Status)
		status := in.OrderStatus
		eventType, ok := domain.ExecutionReportStatusChangeEvent(status)
		if !ok {
			state.err = fmt.Errorf(
				"execution report status %q is not workflow-only: %w",
				status,
				domain.ErrInvalid,
			)
			return nil, state.err
		}
		state.in.Account = detail.Order.Account
		state.in.BaseAsset = detail.Order.BaseAsset
		state.in.QuoteAsset = detail.Order.QuoteAsset
		state.settlement = domain.OrderSettlement{
			Account:     state.in.Account,
			Order:       state.in.Order,
			ReportID:    &state.in.ExternalID,
			OrderStatus: status,
			Leaves:      state.in.LeavesQuantity,
			AllowedFrom: []domain.OrderStatus{detail.Order.Status},
			Events: []domain.OrderEvent{{
				Order:     state.in.Order,
				Type:      eventType,
				Source:    caller.Source,
				Principal: caller.Principal,
				Payload: domain.OrderEventPayload{
					LeavesQuantity:  request.LeavesQuantity,
					OrderStatus:     string(request.OrderStatus),
					ExecutionReport: request,
				},
			}},
		}
		return state, nil
	}
	builder := asyncengine.Chain(source, begin)
	runner := builder.Then(func(
		_ context.Context, state *workflowExecutionReportChainState,
	) error {
		reportID, err := recordOrderSettlementWithAttestation(
			ctx, n.realm, state.settlement, attest,
		)
		if err != nil {
			state.err = fmt.Errorf("record workflow execution report: %w", err)
			return state.err
		}
		state.in.ExternalID = reportID
		return nil
	}).Then(func(
		_ context.Context, state *workflowExecutionReportChainState,
	) error {
		detailText := executionReportDetail(
			state.in, "", state.in.OrderStatus, 0,
		)
		if state.forcedTerminalBypass {
			detailText += " forced=true"
		}
		if err := n.audit(ctx, caller, store.AuditEntry{
			Action:  domain.AuditActionExecutionReport,
			Account: state.in.Account,
			Detail:  detailText,
		}); err != nil {
			state.err = n.fatalPostCommitAudit(
				"audit workflow execution report",
				account,
				fmt.Errorf("audit workflow execution report: %w", err),
			)
			return state.err
		}
		return nil
	}).Finally(func(
		context.Context,
		*workflowExecutionReportChainState,
		asyncengine.ChainOutcome,
	) error {
		return nil
	})
	_, chainErr := runner.Run(
		ctx, eng.AsyncEngine(),
	).Await(context.Background())
	if state.err != nil {
		return engine.ExecutionReportResult{}, state.err
	}
	if chainErr != nil {
		return engine.ExecutionReportResult{}, chainErr
	}
	return engine.ExecutionReportResult{ReportID: state.in.ExternalID}, nil
}

func (n *localNode) requireExecutionReportIDAvailable(
	ctx context.Context, id domain.ExternalID,
) error {
	if id.IsZero() {
		return nil
	}
	exists, err := n.realm.ExecutionReportExists(ctx, id)
	if err != nil {
		return fmt.Errorf("check execution report id %q: %w", id, err)
	}
	if exists {
		return fmt.Errorf("execution report %q: %w", id, domain.ErrAlreadyExists)
	}
	return nil
}

// PersistEventAttestation stamps the signed attestation envelope onto the
// order-history event named by its external id. It is write-once:
// store.PutEventAttestation sets the envelope only when none is present, so a
// retry never clobbers it. The event is already durable, so this mutation only
// adds the envelope; it never touches money or status.
func (n *localNode) PersistEventAttestation(
	ctx context.Context, _ Key, eventID domain.ExternalID, att domain.EventAttestation,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.PutEventAttestation(ctx, eventID, att); err != nil {
		return fmt.Errorf("persist event attestation: %w", err)
	}
	return nil
}

// GetOrder returns the order with its 1:1 signed approval, events and trades,
// addressed by the order's opaque external id.
func (n *localNode) GetOrder(
	ctx context.Context, order domain.ExternalID,
) (domain.OrderDetail, error) {
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.OrderDetail{}, fmt.Errorf("get order: %w", err)
	}
	return detail, nil
}

// ListOrders returns the most recent n orders for an account.
func (n *localNode) ListOrders(
	ctx context.Context, account domain.AccountID, source domain.Source, count int,
) ([]domain.Order, error) {
	orders, err := n.realm.ListOrders(ctx, account, source, count)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	return orders, nil
}

// ListOrderRows returns orders matching filter.
func (n *localNode) ListOrderRows(
	ctx context.Context, filter store.OrderListFilter,
) (store.OrderListPage, error) {
	orders, err := n.realm.ListOrderRows(ctx, filter)
	if err != nil {
		return store.OrderListPage{}, fmt.Errorf("list order rows: %w", err)
	}
	return orders, nil
}

// ListAllOrders returns every matching order, newest first.
func (n *localNode) ListAllOrders(
	ctx context.Context, account domain.AccountID, source domain.Source,
) ([]domain.Order, error) {
	orders, err := n.realm.ListAllOrders(ctx, account, source)
	if err != nil {
		return nil, fmt.Errorf("list all orders: %w", err)
	}
	return orders, nil
}

// CountOrders returns the total number of orders recorded in the realm.
func (n *localNode) CountOrders(ctx context.Context) (int, error) {
	count, err := n.realm.CountOrders(ctx)
	if err != nil {
		return 0, fmt.Errorf("count orders: %w", err)
	}
	return count, nil
}

// CountActiveOrders returns orders in the working lifecycle set.
func (n *localNode) CountActiveOrders(ctx context.Context) (int, error) {
	count, err := n.realm.CountActiveOrders(ctx)
	if err != nil {
		return 0, fmt.Errorf("count active orders: %w", err)
	}
	return count, nil
}

// CountOrdersSince returns the number of orders in the realm at or after since.
func (n *localNode) CountOrdersSince(
	ctx context.Context, since time.Time,
) (int, error) {
	count, err := n.realm.CountOrdersSince(ctx, since)
	if err != nil {
		return 0, fmt.Errorf("count orders since: %w", err)
	}
	return count, nil
}

// ListOrderEvents returns all events for the identified order, oldest first.
func (n *localNode) ListOrderEvents(
	ctx context.Context, order domain.ExternalID,
) ([]domain.OrderEvent, error) {
	events, err := n.realm.ListOrderEvents(ctx, order)
	if err != nil {
		return nil, fmt.Errorf("list order events: %w", err)
	}
	return events, nil
}

// ListTrades returns the most recent n trades for an account.
func (n *localNode) ListTrades(
	ctx context.Context, account domain.AccountID, source domain.Source, count int,
) ([]domain.Trade, error) {
	trades, err := n.realm.ListTrades(ctx, account, source, count)
	if err != nil {
		return nil, fmt.Errorf("list trades: %w", err)
	}
	return trades, nil
}

// ListAllTrades returns every matching trade, newest first.
func (n *localNode) ListAllTrades(
	ctx context.Context, account domain.AccountID, source domain.Source,
) ([]domain.Trade, error) {
	trades, err := n.realm.ListAllTrades(ctx, account, source)
	if err != nil {
		return nil, fmt.Errorf("list all trades: %w", err)
	}
	return trades, nil
}

// ListTradeRows returns trades matching filter.
func (n *localNode) ListTradeRows(
	ctx context.Context, filter store.TradeListFilter,
) (store.TradeListPage, error) {
	page, err := n.realm.ListTradeRows(ctx, filter)
	if err != nil {
		return store.TradeListPage{}, fmt.Errorf("list trade rows: %w", err)
	}
	return page, nil
}

// CheckOrder delegates the non-mutating pre-trade dry-run to the engine. The
// check mutates no state and writes no audit row, but it still runs through the
// account lane so dry-runs cannot observe account state out of order with fills.
func (n *localNode) CheckOrder(
	ctx context.Context, _ Key, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	eng, done := n.beginLaneRead()
	defer done()
	source, err := eng.CheckOrderModel(probe)
	if err != nil {
		return domain.CheckResult{}, err
	}
	type checkOrderState struct {
		ctx    context.Context
		result domain.CheckResult
		err    error
	}
	state := &checkOrderState{ctx: ctx}
	chain := asyncengine.Chain(
		source,
		func(context.Context) (*checkOrderState, error) {
			if ctxErr := state.ctx.Err(); ctxErr != nil {
				state.err = fmt.Errorf(
					"engine: check order cancelled: %w", ctxErr,
				)
				return nil, state.err
			}
			return state, nil
		},
	).CheckOrder(func(
		_ context.Context,
		state *checkOrderState,
		checked asyncengine.OrderCheckResult,
	) error {
		state.result, state.err = eng.CheckedOrder(probe, checked)
		return state.err
	})
	_, chainErr := chain.Run(ctx, eng.AsyncEngine()).Await(context.Background())
	if state.err != nil {
		return domain.CheckResult{}, state.err
	}
	if chainErr != nil {
		return domain.CheckResult{}, chainErr
	}
	return state.result, nil
}

// Close stops the engine and closes the store. It is idempotent: the engine's
// Stop and the store's Close are both idempotent.
func (n *localNode) Close() error {
	n.mutate.Lock()
	defer n.mutate.Unlock()
	n.engineMu.Lock()
	stopFinalEngine(n.engine)
	n.engineMu.Unlock()
	if err := n.db.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	return nil
}
