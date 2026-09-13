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
	"strings"

	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// --- trading ----------------------------------------------------------------

type orderSubmissionAttestingRealm interface {
	RecordOrderSubmissionWithAttestation(
		ctx context.Context,
		o domain.Order,
		submitted domain.OrderEvent,
		apply func(domain.Order) (domain.OrderSettlement, error),
		attest store.EventAttestor,
	) (domain.Order, error)
}

type orderSettlementAttestingRealm interface {
	RecordOrderSettlementWithAttestation(
		ctx context.Context,
		st domain.OrderSettlement,
		attest store.EventAttestor,
	) (domain.ExternalID, error)
}

func recordOrderSubmissionWithAttestation(
	ctx context.Context,
	realm store.RealmStore,
	o domain.Order,
	submitted domain.OrderEvent,
	apply func(domain.Order) (domain.OrderSettlement, error),
	attest store.EventAttestor,
) (domain.Order, error) {
	if attest == nil {
		return realm.RecordOrderSubmission(ctx, o, submitted, apply)
	}
	attesting, ok := realm.(orderSubmissionAttestingRealm)
	if !ok {
		return domain.Order{}, fmt.Errorf(
			"store: order submission attestation unsupported: %w",
			domain.ErrNotImplemented,
		)
	}
	return attesting.RecordOrderSubmissionWithAttestation(
		ctx, o, submitted, apply, attest)
}

func recordOrderSettlementWithAttestation(
	ctx context.Context,
	realm store.RealmStore,
	st domain.OrderSettlement,
	attest store.EventAttestor,
) (domain.ExternalID, error) {
	if attest == nil {
		return realm.RecordOrderSettlement(ctx, st)
	}
	attesting, ok := realm.(orderSettlementAttestingRealm)
	if !ok {
		return "", fmt.Errorf(
			"store: order settlement attestation unsupported: %w",
			domain.ErrNotImplemented,
		)
	}
	return attesting.RecordOrderSettlementWithAttestation(ctx, st, attest)
}

var errOrderSubmissionChainAborted = errors.New(
	"order submission chain aborted",
)

type orderSubmissionCompletion struct {
	order domain.Order
	err   error
}

// orderSubmissionBridge keeps RecordOrderSubmission's transaction open while
// the SDK chain runs. The store callback publishes the assigned draft and then
// waits for the settlement produced by the chain hook.
type orderSubmissionBridge struct {
	settlement chan domain.OrderSettlement
	abort      chan struct{}
	done       chan orderSubmissionCompletion
	completed  bool
}

func beginOrderSubmission(
	ctx context.Context,
	realm store.RealmStore,
	draft domain.Order,
	submitted domain.OrderEvent,
	attest store.EventAttestor,
) (*orderSubmissionBridge, domain.Order, error) {
	started := make(chan domain.Order, 1)
	bridge := &orderSubmissionBridge{
		settlement: make(chan domain.OrderSettlement, 1),
		abort:      make(chan struct{}),
		done:       make(chan orderSubmissionCompletion, 1),
	}
	go func() {
		order, err := recordOrderSubmissionWithAttestation(
			ctx,
			realm,
			draft,
			submitted,
			func(persisted domain.Order) (domain.OrderSettlement, error) {
				started <- persisted
				select {
				case settlement := <-bridge.settlement:
					return settlement, nil
				case <-bridge.abort:
					return domain.OrderSettlement{},
						errOrderSubmissionChainAborted
				}
			},
			attest,
		)
		bridge.done <- orderSubmissionCompletion{order: order, err: err}
	}()

	select {
	case order := <-started:
		return bridge, order, nil
	case completion := <-bridge.done:
		if completion.err == nil {
			completion.err = errors.New(
				"store: order submission apply callback was not called",
			)
		}
		return nil, domain.Order{}, completion.err
	}
}

func (b *orderSubmissionBridge) complete(
	settlement domain.OrderSettlement,
) (domain.Order, error) {
	if b == nil {
		return domain.Order{}, errors.New(
			"store: order submission transaction is unavailable",
		)
	}
	if b.completed {
		return domain.Order{}, errors.New(
			"store: order submission transaction already completed",
		)
	}
	b.completed = true
	b.settlement <- settlement
	completion := <-b.done
	return completion.order, completion.err
}

func (b *orderSubmissionBridge) abortAndWait() {
	if b == nil || b.completed {
		return
	}
	b.completed = true
	close(b.abort)
	<-b.done
}

func validatePersistedOrderChainInput(
	draft domain.Order, persisted domain.Order,
) error {
	var field string
	switch {
	case persisted.Account != draft.Account:
		field = "account"
	case persisted.BaseAsset != draft.BaseAsset:
		field = "base asset"
	case persisted.QuoteAsset != draft.QuoteAsset:
		field = "quote asset"
	case persisted.Side != draft.Side:
		field = "side"
	case persisted.AmountKind != draft.AmountKind:
		field = "amount kind"
	case persisted.AmountValue != draft.AmountValue:
		field = "amount value"
	case persisted.Price != draft.Price:
		field = "price"
	case persisted.DropCopy != draft.DropCopy:
		field = "drop-copy mode"
	default:
		return nil
	}
	return fmt.Errorf(
		"store: persisted order %s does not match chain input: %w",
		field,
		domain.ErrInvalid,
	)
}

type submitOrderChainState struct {
	ctx        context.Context
	bridge     *orderSubmissionBridge
	order      domain.Order
	result     engine.OrderResult
	settlement domain.OrderSettlement
	attest     store.EventAttestor
	err        error
}

type submitImmediateChainState struct {
	ctx               context.Context
	bridge            *orderSubmissionBridge
	order             domain.Order
	result            engine.ImmediateResult
	preparation       engine.ImmediatePreparation
	attest            store.EventAttestor
	err               error
	preTradeFinalized bool
	reportSettled     bool
}

// SubmitOrder runs pre-trade and caller persistence in one account chain and
// one store transaction. On accept
// it records pre_trade_accepted and committed, persists the lock, engine opening
// leaves, balance outcomes, and committed status; on reject it records
// pre_trade_rejected and rejected status. missing decides whether an order for
// an account Officer does not know yet registers that account or is rejected.
func (n *localNode) SubmitOrder(
	ctx context.Context, o domain.Order,
	missing domain.MissingAccountPolicy, caller domain.Caller,
) (domain.Order, error) {
	order, _, err := n.submitOrder(ctx, o, missing, caller, nil)
	return order, err
}

func (n *localNode) SubmitOrderWithAttestation(
	ctx context.Context,
	o domain.Order,
	missing domain.MissingAccountPolicy,
	caller domain.Caller,
	attestFor func(domain.Order, engine.OrderResult) store.EventAttestor,
) (domain.Order, engine.OrderResult, error) {
	return n.submitOrder(ctx, o, missing, caller, attestFor)
}

func (n *localNode) submitOrder(
	ctx context.Context,
	o domain.Order,
	missing domain.MissingAccountPolicy,
	caller domain.Caller,
	attestFor func(domain.Order, engine.OrderResult) store.EventAttestor,
) (domain.Order, engine.OrderResult, error) {
	// Resolve the account and register both order assets before the chain source
	// is converted to its stable SDK identifiers.
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, o.Account, missing, "submit order", caller,
		o.BaseAsset, o.QuoteAsset,
	); err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	defer done()

	draft := submittedOrderDraft(o, caller)
	source, err := eng.OrderModel(draft)
	if err != nil {
		return domain.Order{}, engine.OrderResult{},
			fmt.Errorf("submit order: %w", err)
	}
	state := &submitOrderChainState{ctx: ctx}
	var attestSubmitted store.EventAttestor
	if attestFor != nil {
		attestSubmitted = func(
			ctx context.Context, event domain.OrderEvent,
		) (domain.EventAttestation, bool, error) {
			if state.attest == nil {
				return domain.EventAttestation{}, false, nil
			}
			return state.attest(ctx, event)
		}
	}
	submitted := submittedOrderEvent(caller)

	begin := func(context.Context) (*submitOrderChainState, error) {
		if ctxErr := state.ctx.Err(); ctxErr != nil {
			state.err = fmt.Errorf(
				"engine: submit order cancelled: %w", ctxErr,
			)
			return nil, state.err
		}
		bridge, persisted, beginErr := beginOrderSubmission(
			state.ctx, n.realm, draft, submitted, attestSubmitted,
		)
		if beginErr != nil {
			state.err = beginErr
			return nil, beginErr
		}
		if validateErr := validatePersistedOrderChainInput(
			draft, persisted,
		); validateErr != nil {
			bridge.abortAndWait()
			state.err = fmt.Errorf("submit order: %w", validateErr)
			return nil, state.err
		}
		state.bridge = bridge
		state.order = persisted
		return state, nil
	}
	builder := asyncengine.Chain(source, begin)
	onRejected := func(
		chainCtx context.Context,
		state *submitOrderChainState,
		rejects []reject.Reject,
	) error {
		state.result = eng.RejectedOrder(state.order, rejects)
		if len(state.result.Rejects) == 0 {
			state.err = fmt.Errorf("submit order: engine rejection has no reject reason")
			return state.err
		}
		if attestFor != nil {
			state.attest = attestFor(state.order, state.result)
		}
		settlement := orderRejectedSettlement(
			o.Account, state.order, state.result.Rejects, caller,
		)
		persisted, persistErr := state.bridge.complete(settlement)
		if persistErr != nil {
			state.err = n.fatalPostEnginePersistence(
				"record order submission", o.Account, persistErr,
			)
			return state.err
		}
		state.order = persisted
		state.err = n.auditSubmittedOrder(
			state.ctx, o.Account, state.order, state.result, caller,
		)
		return state.err
	}
	if draft.DropCopy {
		builder.ApplyDropCopy(asyncengine.DropCopyHooks[*submitOrderChainState]{
			OnRejected: onRejected,
			OnApplied: func(
				_ context.Context,
				state *submitOrderChainState,
				operation asyncengine.DropCopyResult,
			) (asyncengine.Decision, error) {
				result, resultErr := eng.AppliedDropCopyOrder(
					state.order, operation,
				)
				if resultErr != nil {
					state.err = fmt.Errorf("submit order: %w", resultErr)
					return asyncengine.DecisionRollback, state.err
				}
				return n.materializeReservedOrder(
					o.Account, caller, attestFor, state, result,
				)
			},
		})
	} else {
		builder.ExecutePreTrade(asyncengine.PreTradeHooks[*submitOrderChainState]{
			OnRejected: onRejected,
			OnReserved: func(
				_ context.Context,
				state *submitOrderChainState,
				reservation asyncengine.OperationResult,
			) (asyncengine.Decision, error) {
				result, resultErr := eng.ReservedOrder(
					state.order, reservation,
				)
				if resultErr != nil {
					state.err = fmt.Errorf("submit order: %w", resultErr)
					return asyncengine.DecisionRollback, state.err
				}
				return n.materializeReservedOrder(
					o.Account, caller, attestFor, state, result,
				)
			},
		})
	}
	runner := builder.Then(func(
		_ context.Context, state *submitOrderChainState,
	) error {
		persisted, persistErr := state.bridge.complete(state.settlement)
		if persistErr != nil {
			state.err = n.fatalPostEnginePersistence(
				"record order submission", o.Account, persistErr,
			)
			return state.err
		}
		state.order = persisted
		return nil
	}).Then(func(
		_ context.Context, state *submitOrderChainState,
	) error {
		state.err = n.auditSubmittedOrder(
			state.ctx, o.Account, state.order, state.result, caller,
		)
		return state.err
	}).Finally(func(
		_ context.Context,
		state *submitOrderChainState,
		_ asyncengine.ChainOutcome,
	) error {
		state.bridge.abortAndWait()
		return nil
	})
	_, chainErr := runner.Run(ctx, eng.AsyncEngine()).Await(context.Background())
	if runErr := accountChainRunError(
		"submit order", state.err, chainErr,
	); runErr != nil {
		return domain.Order{}, engine.OrderResult{}, runErr
	}
	return state.order, state.result, nil
}

func (n *localNode) materializeReservedOrder(
	account domain.AccountID,
	caller domain.Caller,
	attestFor func(domain.Order, engine.OrderResult) store.EventAttestor,
	state *submitOrderChainState,
	result engine.OrderResult,
) (asyncengine.Decision, error) {
	state.result = result
	if attestFor != nil {
		state.attest = attestFor(state.order, state.result)
	}
	settlement, err := orderAcceptedSettlement(
		account, state.order, state.result, caller,
	)
	if err != nil {
		state.err = fmt.Errorf("submit order: materialize settlement: %w", err)
		return asyncengine.DecisionRollback, state.err
	}
	state.settlement = settlement
	return asyncengine.DecisionCommit, nil
}

func (n *localNode) auditSubmittedOrder(
	ctx context.Context,
	account domain.AccountID,
	order domain.Order,
	result engine.OrderResult,
	caller domain.Caller,
) error {
	if result.Accepted {
		if err := n.mirrorEngineBlocksAudit(
			ctx, order.ExternalID, result.Blocks,
		); err != nil {
			return n.fatalPostEnginePersistence(
				"audit pre-trade engine blocks", account, err,
			)
		}
	}
	return n.auditOrderDecision(
		ctx, account, order, result.Accepted, result.Rejects, caller,
	)
}

func (n *localNode) auditOrderDecision(
	ctx context.Context,
	account domain.AccountID,
	order domain.Order,
	accepted bool,
	rejects []domain.OrderReject,
	caller domain.Caller,
) error {
	action := domain.AuditActionSubmitOrder
	if order.DropCopy {
		action = domain.AuditActionSubmitDropCopy
	}
	verdict := "accept"
	rejectCode := ""
	if !accepted {
		if len(rejects) == 0 {
			return n.fatalPostEnginePersistence(
				"audit submit order",
				account,
				fmt.Errorf("audit submit order: engine rejection has no reject reason"),
			)
		}
		verdict = "reject"
		rejectCode = rejects[0].Code
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: action, Account: account,
		OrderID: order.ExternalID.String(), Verdict: verdict, RejectCode: rejectCode,
		Detail: submitOrderDetail(order, accepted),
	}); err != nil {
		return n.fatalPostEnginePersistence(
			"audit submit order",
			account,
			fmt.Errorf("audit submit order: %w", err),
		)
	}
	return nil
}

func orderAcceptedSettlement(
	account domain.AccountID, order domain.Order, result engine.OrderResult, caller domain.Caller,
) (domain.OrderSettlement, error) {
	openingLeaves, err := openingLeavesFromOutcomes(order, result.Outcomes)
	if err != nil {
		return domain.OrderSettlement{}, err
	}
	return domain.OrderSettlement{
		Account:          account,
		Order:            order.ExternalID,
		OrderStatus:      domain.OrderStatusCommitted,
		Leaves:           openingLeaves,
		ReservedQuantity: openingLeaves,
		Balances:         balanceSettlementsFrom(result.Outcomes),
		Blocks:           result.Blocks,
		Events: []domain.OrderEvent{
			fillSettlementEvent(order.ExternalID, domain.OrderEventPreTradeAccepted, caller, domain.OrderEventPayload{}),
			fillSettlementEvent(order.ExternalID, domain.OrderEventCommitted, caller, domain.OrderEventPayload{}),
		},
		Lock:    result.Lock,
		SetLock: true,
	}, nil
}

// openingLeavesFromOutcomes returns the accepted engine's applied base-asset
// delta. An engine answer without one records zero.
func openingLeavesFromOutcomes(
	order domain.Order, outcomes []engine.BalanceOutcome,
) (string, error) {
	openingLeaves := ""
	for _, outcome := range outcomes {
		if outcome.Asset != order.BaseAsset {
			continue
		}
		var candidate string
		switch order.Side {
		case domain.OrderSideBuy:
			candidate = outcome.Outcome.IncomingDelta
		case domain.OrderSideSell:
			candidate = outcome.Outcome.HeldDelta
		default:
			return "", fmt.Errorf(
				"engine accepted order with unsupported side %q",
				order.Side,
			)
		}
		if candidate == "" {
			continue
		}
		if openingLeaves != "" {
			return "", fmt.Errorf(
				"engine accepted order with several base-asset deltas for asset %q",
				order.BaseAsset,
			)
		}
		openingLeaves = candidate
	}
	if openingLeaves == "" {
		return "0", nil
	}
	return openingLeaves, nil
}

func orderRejectedSettlement(
	account domain.AccountID, order domain.Order, rejects []domain.OrderReject, caller domain.Caller,
) domain.OrderSettlement {
	payload := domain.OrderEventPayload{
		Rejects: append([]domain.OrderReject(nil), rejects...),
	}
	if len(rejects) > 0 {
		r := rejects[0]
		payload.RejectCode = r.Code
		payload.RejectScope = r.Scope
		payload.RejectPolicy = r.Policy
		payload.RejectReason = r.Reason
		payload.RejectDetails = r.Details
	}
	return domain.OrderSettlement{
		Account:     account,
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusRejected,
		// A pre-trade reject is the engine's answer that nothing was reserved,
		// recorded exactly as the accepted path records the engine's delta.
		// Leaving it empty would misrepresent that engine-authored zero as
		// unknown.
		Leaves:           "0",
		ReservedQuantity: "0",
		Events: []domain.OrderEvent{
			fillSettlementEvent(order.ExternalID, domain.OrderEventPreTradeRejected, caller, payload),
		},
	}
}

// SubmitImmediate records the order, runs the engine pre-trade and, on accept,
// commits and settles the fill in the same engine call at the request trade
// price so the held amount nets to zero, then persists the filled lifecycle. On
// reject the order is recorded rejected. missing is handled as in SubmitOrder.
// The backend audits the issued approval.
func (n *localNode) SubmitImmediate(
	ctx context.Context, o domain.Order,
	missing domain.MissingAccountPolicy, caller domain.Caller,
) (domain.Order, engine.ImmediateResult, error) {
	return n.submitImmediate(ctx, o, missing, caller, nil)
}

func (n *localNode) SubmitImmediateWithAttestation(
	ctx context.Context,
	o domain.Order,
	missing domain.MissingAccountPolicy,
	caller domain.Caller,
	attestFor func(domain.Order, engine.ImmediateResult) store.EventAttestor,
) (domain.Order, engine.ImmediateResult, error) {
	return n.submitImmediate(ctx, o, missing, caller, attestFor)
}

func (n *localNode) submitImmediate(
	ctx context.Context,
	o domain.Order,
	missing domain.MissingAccountPolicy,
	caller domain.Caller,
	attestFor func(domain.Order, engine.ImmediateResult) store.EventAttestor,
) (domain.Order, engine.ImmediateResult, error) {
	// Resolve the account and register both order assets before converting the
	// chain source (see SubmitOrder).
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, o.Account, missing, "submit immediate", caller,
		o.BaseAsset, o.QuoteAsset,
	); err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	defer done()

	draft := submittedOrderDraft(o, caller)
	source, err := eng.OrderModel(draft)
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{},
			fmt.Errorf("submit immediate: %w", err)
	}
	state := &submitImmediateChainState{ctx: ctx}
	var attestSubmitted store.EventAttestor
	if attestFor != nil {
		attestSubmitted = func(ctx context.Context, event domain.OrderEvent) (domain.EventAttestation, bool, error) {
			if state.attest == nil {
				return domain.EventAttestation{}, false, nil
			}
			return state.attest(ctx, event)
		}
	}
	submitted := submittedOrderEvent(caller)

	begin := func(context.Context) (*submitImmediateChainState, error) {
		if ctxErr := state.ctx.Err(); ctxErr != nil {
			state.err = fmt.Errorf(
				"engine: submit immediate cancelled: %w", ctxErr,
			)
			return nil, state.err
		}
		bridge, persisted, beginErr := beginOrderSubmission(
			state.ctx, n.realm, draft, submitted, attestSubmitted,
		)
		if beginErr != nil {
			state.err = beginErr
			return nil, beginErr
		}
		if validateErr := validatePersistedOrderChainInput(
			draft, persisted,
		); validateErr != nil {
			bridge.abortAndWait()
			state.err = fmt.Errorf("submit immediate: %w", validateErr)
			return nil, state.err
		}
		state.bridge = bridge
		state.order = persisted
		return state, nil
	}
	builder := asyncengine.Chain(source, begin)
	onRejected := func(
		_ context.Context,
		state *submitImmediateChainState,
		rejects []reject.Reject,
	) error {
		state.result = eng.RejectedImmediate(state.order, rejects)
		if len(state.result.Rejects) == 0 {
			state.err = fmt.Errorf(
				"submit immediate: engine rejection has no reject reason",
			)
			return state.err
		}
		if attestFor != nil {
			state.attest = attestFor(state.order, state.result)
		}
		settlement := orderRejectedSettlement(
			o.Account, state.order, state.result.Rejects, caller,
		)
		persisted, persistErr := state.bridge.complete(settlement)
		if persistErr != nil {
			state.err = n.fatalPostEnginePersistence(
				"record immediate submission", o.Account, persistErr,
			)
			return state.err
		}
		state.order = persisted
		state.err = n.auditOrderDecision(
			state.ctx, o.Account, state.order, state.result.Accepted,
			state.result.Rejects, caller,
		)
		return state.err
	}
	if draft.DropCopy {
		builder.ApplyDropCopy(
			asyncengine.DropCopyHooks[*submitImmediateChainState]{
				OnRejected: onRejected,
				OnApplied: func(
					_ context.Context,
					state *submitImmediateChainState,
					operation asyncengine.DropCopyResult,
				) (asyncengine.Decision, error) {
					preparation, prepareErr := eng.PrepareImmediateDropCopy(
						state.order, operation,
					)
					if prepareErr != nil {
						state.err = fmt.Errorf(
							"submit immediate: %w", prepareErr,
						)
						return asyncengine.DecisionRollback, state.err
					}
					state.preparation = preparation
					return asyncengine.DecisionCommit, nil
				},
			},
		)
	} else {
		builder.ExecutePreTrade(
			asyncengine.PreTradeHooks[*submitImmediateChainState]{
				OnRejected: onRejected,
				OnReserved: func(
					_ context.Context,
					state *submitImmediateChainState,
					reservation asyncengine.OperationResult,
				) (asyncengine.Decision, error) {
					preparation, prepareErr := eng.PrepareImmediateReservation(
						state.order, reservation,
					)
					if prepareErr != nil {
						state.err = fmt.Errorf(
							"submit immediate: %w", prepareErr,
						)
						return asyncengine.DecisionRollback, state.err
					}
					state.preparation = preparation
					return asyncengine.DecisionCommit, nil
				},
			},
		)
	}
	builder.Then(func(
		_ context.Context, state *submitImmediateChainState,
	) error {
		state.preTradeFinalized = true
		return nil
	})
	builder.ApplyExecutionReport(
		asyncengine.ExecutionReportHooks[*submitImmediateChainState]{
			Report: func(
				_ context.Context, state *submitImmediateChainState,
			) (model.ExecutionReport, error) {
				return state.preparation.ExecutionReport, nil
			},
			OnSettled: func(
				_ context.Context,
				state *submitImmediateChainState,
				postTrade pretrade.PostTradeResult,
			) error {
				result, settleErr := eng.SettleImmediate(
					state.order, state.preparation, postTrade,
				)
				if settleErr != nil {
					state.err = settleErr
					return settleErr
				}
				state.result = result
				state.reportSettled = true
				if attestFor != nil {
					state.attest = attestFor(state.order, state.result)
				}
				return nil
			},
		},
	)
	runner := builder.Then(func(
		_ context.Context, state *submitImmediateChainState,
	) error {
		settlement, settlementErr := immediateAcceptedSettlement(
			o.Account, state.order, state.result, caller,
		)
		if settlementErr != nil {
			state.err = n.fatalPostEnginePersistence(
				"record immediate submission", o.Account, settlementErr,
			)
			return state.err
		}
		persisted, persistErr := state.bridge.complete(settlement)
		if persistErr != nil {
			state.err = n.fatalPostEnginePersistence(
				"record immediate submission", o.Account, persistErr,
			)
			return state.err
		}
		state.order = persisted
		return nil
	}).Then(func(
		_ context.Context, state *submitImmediateChainState,
	) error {
		state.err = n.auditImmediateSubmission(
			state.ctx, o.Account, state.order, state.result, caller,
		)
		return state.err
	}).Finally(func(
		_ context.Context,
		state *submitImmediateChainState,
		outcome asyncengine.ChainOutcome,
	) error {
		if state.err == nil && state.preTradeFinalized &&
			!state.reportSettled && outcome.Err != nil {
			state.err = immediateReconciliationError(
				state.order,
				state.preparation.ReconciliationState,
				"the execution report failed and the fill is unsettled",
				chainRootCause(outcome.Err),
			)
		}
		state.bridge.abortAndWait()
		return nil
	})
	_, chainErr := runner.Run(ctx, eng.AsyncEngine()).Await(context.Background())
	if runErr := accountChainRunError(
		"submit immediate", state.err, chainErr,
	); runErr != nil {
		return domain.Order{}, engine.ImmediateResult{}, runErr
	}
	return state.order, state.result, nil
}

func (n *localNode) auditImmediateSubmission(
	ctx context.Context,
	account domain.AccountID,
	order domain.Order,
	result engine.ImmediateResult,
	caller domain.Caller,
) error {
	if err := n.mirrorEngineBlocksAudit(
		ctx, order.ExternalID, result.Blocks,
	); err != nil {
		return n.fatalPostEnginePersistence(
			"audit immediate engine blocks", account, err,
		)
	}
	if result.ExecutionReport == nil {
		return n.fatalPostEnginePersistence(
			"audit immediate execution report",
			account,
			fmt.Errorf("immediate execution report missing"),
		)
	}
	// The adapter-built report request carries the reservation remainder sent
	// to the SDK.
	reported := executionReportInputFromRequest(*result.ExecutionReport)
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action:  domain.AuditActionExecutionReport,
		Account: account,
		Detail: executionReportDetail(
			reported,
			reported.LeavesQuantity,
			domain.OrderStatusFilled,
			len(result.Blocks),
		),
	}); err != nil {
		return n.fatalPostEnginePersistence(
			"audit immediate execution report",
			account,
			fmt.Errorf("audit immediate execution report: %w", err),
		)
	}
	return n.auditOrderDecision(
		ctx, account, order, result.Accepted, result.Rejects, caller,
	)
}

// immediateReconciliationError reports state the engine already applied and the
// caller must reconcile by hand. The cause is rendered, not wrapped: causes
// carrying domain.ErrInvalid would answer 400 "fix your input and retry", which
// contradicts the message and hides an internal failure.
func immediateReconciliationError(
	o domain.Order, state string, action string, err error,
) error {
	orderID := o.ExternalID.String()
	if o.ExternalID.IsZero() {
		orderID = "<unassigned>"
	}
	return fmt.Errorf(
		"engine: %s for order %s (account %s), but %s; engine state needs "+
			"manual reconciliation - do not retry blindly: %v",
		state, orderID, o.Account, action, err,
	)
}

func chainRootCause(err error) error {
	if err == nil {
		return nil
	}
	if err == asyncengine.ErrChainRetryUnsafe {
		return nil
	}
	type joined interface {
		Unwrap() []error
	}
	if unwrapped, ok := err.(joined); ok {
		causes := make([]error, 0, len(unwrapped.Unwrap()))
		for _, child := range unwrapped.Unwrap() {
			if cause := chainRootCause(child); cause != nil {
				causes = append(causes, cause)
			}
		}
		return errors.Join(causes...)
	}
	if unwrapped := errors.Unwrap(err); unwrapped != nil &&
		strings.HasPrefix(err.Error(), "async chain ") {
		return chainRootCause(unwrapped)
	}
	return err
}

// ConfirmOrder records a history-only confirmation for an untouched workflow
// order. It never calls the engine or changes the order, lock, or balances.
func (n *localNode) ConfirmOrder(
	ctx context.Context, order domain.ExternalID, caller domain.Caller,
) (domain.Order, error) {
	return n.confirmOrder(ctx, order, caller, nil)
}

func (n *localNode) ConfirmOrderWithAttestation(
	ctx context.Context,
	order domain.ExternalID,
	caller domain.Caller,
	attest store.EventAttestor,
) (domain.Order, error) {
	return n.confirmOrder(ctx, order, caller, attest)
}

func (n *localNode) confirmOrder(
	ctx context.Context,
	order domain.ExternalID,
	caller domain.Caller,
	attest store.EventAttestor,
) (domain.Order, error) {
	routeDetail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, fmt.Errorf("get order: %w", err)
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, err
	}
	defer done()

	type confirmationChainState struct {
		ctx       context.Context
		account   domain.Account
		confirmed domain.Order
		err       error
	}
	account, found, err := n.realm.GetAccount(ctx, routeDetail.Order.Account)
	if err != nil {
		return domain.Order{}, fmt.Errorf("read account for confirmation: %w", err)
	}
	if !found {
		return domain.Order{}, fmt.Errorf(
			"account %q: %w", routeDetail.Order.Account, domain.ErrNotFound,
		)
	}
	source, err := eng.AccountID(routeDetail.Order.Account)
	if err != nil {
		return domain.Order{}, err
	}
	if err := validateAccountAdministrativeSource(account, source); err != nil {
		return domain.Order{}, err
	}
	state := &confirmationChainState{ctx: ctx, account: account}
	begin := func(context.Context) (*confirmationChainState, error) {
		if ctxErr := state.ctx.Err(); ctxErr != nil {
			state.err = ctxErr
			return nil, state.err
		}
		current, found, readErr := n.realm.GetAccount(
			state.ctx, state.account.Code,
		)
		if readErr != nil {
			state.err = fmt.Errorf("read account for confirmation: %w", readErr)
			return nil, state.err
		}
		if !found {
			state.err = fmt.Errorf(
				"account %q: %w", state.account.Code, domain.ErrNotFound,
			)
			return nil, state.err
		}
		if validateErr := validateAccountAdministrativeSource(
			current, source,
		); validateErr != nil {
			state.err = fmt.Errorf(
				"read account for confirmation: %w", validateErr,
			)
			return nil, state.err
		}
		return state, nil
	}
	builder := asyncengine.Chain(source, begin)
	runner := builder.Then(func(
		_ context.Context, state *confirmationChainState,
	) error {
		detail, err := n.realm.GetOrder(state.ctx, order)
		if err != nil {
			state.err = fmt.Errorf("get order: %w", err)
			return state.err
		}
		if detail.Order.Account != state.account.Code {
			state.err = fmt.Errorf(
				"order %q account changed from %q to %q: %w",
				order,
				state.account.Code,
				detail.Order.Account,
				domain.ErrInvalid,
			)
			return state.err
		}
		if err := domain.RequireOrderModifiable(
			detail.Order.Status, false,
		); err != nil {
			state.err = err
			return err
		}
		events, err := n.realm.ListOrderEvents(state.ctx, order)
		if err != nil {
			state.err = fmt.Errorf("list order events: %w", err)
			return state.err
		}
		if err := requireNoExecutionReportActivity(order, events); err != nil {
			state.err = err
			return err
		}
		if orderEventExists(events, domain.OrderEventConfirmed) {
			state.confirmed = detail.Order
			return nil
		}
		if _, err := recordOrderSettlementWithAttestation(
			state.ctx,
			n.realm,
			domain.OrderSettlement{
				Account:     detail.Order.Account,
				Order:       order,
				OrderStatus: detail.Order.Status,
				AllowedFrom: []domain.OrderStatus{detail.Order.Status},
				Events: []domain.OrderEvent{{
					Order:     order,
					Type:      domain.OrderEventConfirmed,
					Source:    caller.Source,
					Principal: caller.Principal,
				}},
			},
			attest,
		); err != nil {
			state.err = fmt.Errorf("record order confirmation: %w", err)
			return state.err
		}
		state.confirmed = detail.Order
		return nil
	}).Finally(func(
		_ context.Context,
		state *confirmationChainState,
		outcome asyncengine.ChainOutcome,
	) error {
		_, state.err = runtimeChainTerminalError(state.err, false, outcome)
		return nil
	})
	_, chainErr := runner.Run(
		ctx, eng.AsyncEngine(),
	).Await(context.Background())
	if runErr := accountChainRunError(
		"confirm order", state.err, chainErr,
	); runErr != nil {
		return domain.Order{}, runErr
	}
	return state.confirmed, nil
}

// CancelOrder forwards a terminal execution report for an untouched workflow
// order. Caller leaves update history when supplied; the engine receives the
// order's own recorded reservation remainder. The stored engine lock is
// restored.
func (n *localNode) CancelOrder(
	ctx context.Context,
	order domain.ExternalID,
	leavesQuantity string,
	caller domain.Caller,
) (domain.Order, engine.ExecutionReportResult, error) {
	return n.cancelOrder(ctx, order, leavesQuantity, caller, nil)
}

func (n *localNode) CancelOrderWithAttestation(
	ctx context.Context,
	order domain.ExternalID,
	leavesQuantity string,
	caller domain.Caller,
	attest store.EventAttestor,
) (domain.Order, engine.ExecutionReportResult, error) {
	return n.cancelOrder(ctx, order, leavesQuantity, caller, attest)
}

func (n *localNode) cancelOrder(
	ctx context.Context,
	order domain.ExternalID,
	leavesQuantity string,
	caller domain.Caller,
	attest store.EventAttestor,
) (domain.Order, engine.ExecutionReportResult, error) {
	ctx = context.WithoutCancel(ctx)
	routeDetail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, engine.ExecutionReportResult{}, fmt.Errorf(
			"get order: %w", err,
		)
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.ExecutionReportResult{}, err
	}
	defer done()
	source, err := eng.AccountID(routeDetail.Order.Account)
	if err != nil {
		return domain.Order{}, engine.ExecutionReportResult{}, err
	}
	state := &executionReportChainState{
		ctx:       ctx,
		adapter:   eng,
		accountID: source,
	}
	begin := func(context.Context) (*executionReportChainState, error) {
		detail, err := n.realm.GetOrder(ctx, order)
		if err != nil {
			state.err = fmt.Errorf("get order: %w", err)
			return nil, state.err
		}
		if err := domain.RequireOrderModifiable(
			detail.Order.Status, false,
		); err != nil {
			state.err = err
			return nil, err
		}
		events, err := n.realm.ListOrderEvents(ctx, order)
		if err != nil {
			state.err = fmt.Errorf("list order events: %w", err)
			return nil, state.err
		}
		if err := requireNoExecutionReportActivity(order, events); err != nil {
			state.err = err
			return nil, err
		}
		state.order = detail.Order
		state.in = domain.ExecutionReportInput{
			Order:          order,
			Account:        detail.Order.Account,
			BaseAsset:      detail.Order.BaseAsset,
			QuoteAsset:     detail.Order.QuoteAsset,
			Side:           detail.Order.Side,
			LeavesQuantity: leavesQuantity,
			Lock:           append([]byte(nil), detail.Order.Lock...),
			OrderStatus:    domain.OrderStatusCancelled,
		}
		if _, err := domain.ExecutionReportRequiresEngine(state.in); err != nil {
			state.err = fmt.Errorf(
				"build cancellation execution report: %w", err,
			)
			return nil, state.err
		}
		state.reservationRemainder, err = executionReservedQuantity(state.in, detail)
		if err != nil {
			state.err = fmt.Errorf(
				"build cancellation reservation remainder: %w", err,
			)
			return nil, state.err
		}
		state.request = domain.ExecutionReportRequestFromInput(state.in)
		state.report, err = eng.ExecutionReportModel(
			state.in, state.reservationRemainder,
		)
		if err != nil {
			state.err = fmt.Errorf(
				"apply cancellation execution report: %w", err,
			)
			return nil, state.err
		}
		return state, nil
	}
	persist := func(state *executionReportChainState) error {
		if state.result.Persistence == nil {
			return fmt.Errorf(
				"apply cancellation execution report returned no persistence write set",
			)
		}
		persistence := stampExecutionReportPersistence(
			*state.result.Persistence, caller, state.request,
		)
		reservedQuantity, err := reservationAfterOutcomes(state.order, persistence.Balances)
		if err != nil {
			return n.fatalPostEnginePersistence("record cancellation reservation", routeDetail.Order.Account, err)
		}
		settlement := domain.OrderSettlement{
			ReservedQuantity: reservedQuantity,
			Account:          state.in.Account,
			Order:            order,
			OrderStatus:      persistence.OrderStatus,
			Leaves:           persistence.Leaves,
			Balances:         persistence.Balances,
			Events:           persistence.Events,
			Trade:            persistence.Trade,
			Blocks:           persistence.Blocks,
		}
		if _, err := recordOrderSettlementWithAttestation(
			ctx, n.realm, settlement, attest,
		); err != nil {
			return n.fatalPostEnginePersistence(
				"record cancellation execution report",
				routeDetail.Order.Account,
				fmt.Errorf("record cancellation execution report: %w", err),
			)
		}
		state.persistenceCompleted = true
		if err := n.mirrorEngineBlocksAudit(
			ctx, order, state.result.Blocks,
		); err != nil {
			return n.fatalPostEnginePersistence(
				"audit cancellation execution report engine blocks",
				routeDetail.Order.Account,
				err,
			)
		}
		detailText := executionReportDetail(
			state.in,
			state.reservationRemainder,
			domain.OrderStatusCancelled,
			len(state.result.Blocks),
		)
		if err := n.audit(ctx, caller, store.AuditEntry{
			Action:  domain.AuditActionExecutionReport,
			Account: state.in.Account,
			Detail:  detailText,
		}); err != nil {
			return n.fatalPostEnginePersistence(
				"audit cancellation execution report",
				routeDetail.Order.Account,
				fmt.Errorf("audit cancellation execution report: %w", err),
			)
		}
		state.order.Status = persistence.OrderStatus
		state.order.ReservedQuantity = reservedQuantity
		if persistence.Leaves != "" {
			state.order.Leaves = persistence.Leaves
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
		"apply cancellation execution report",
		routeDetail.Order.Account,
	); err != nil {
		return domain.Order{}, engine.ExecutionReportResult{}, err
	}
	return state.order, state.result, nil
}

func requireNoExecutionReportActivity(
	order domain.ExternalID, events []domain.OrderEvent,
) error {
	for _, event := range events {
		if event.Payload.ExecutionReport != nil {
			return fmt.Errorf("order %s: %w", order, domain.ErrExecutionReportRequired)
		}
	}
	return nil
}

func orderEventExists(events []domain.OrderEvent, eventType domain.OrderEventType) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func submittedOrderDraft(o domain.Order, caller domain.Caller) domain.Order {
	o.Source = caller.Source
	o.Principal = caller.Principal
	o.Status = domain.OrderStatusSubmitted
	o.ReservedQuantity = "0"
	return o
}

func submittedOrderEvent(caller domain.Caller) domain.OrderEvent {
	return fillSettlementEvent(
		domain.ExternalID(""),
		domain.OrderEventSubmitted,
		caller,
		domain.OrderEventPayload{},
	)
}

func immediateAcceptedSettlement(
	account domain.AccountID,
	order domain.Order,
	result engine.ImmediateResult,
	caller domain.Caller,
) (domain.OrderSettlement, error) {
	if result.Persistence == nil || result.ExecutionReport == nil {
		return domain.OrderSettlement{}, fmt.Errorf(
			"immediate execution report persistence is incomplete",
		)
	}
	persistence := stampExecutionReportPersistence(
		*result.Persistence, caller, result.ExecutionReport,
	)
	events := make([]domain.OrderEvent, 0, len(persistence.Events)+2)
	events = append(events,
		fillSettlementEvent(
			order.ExternalID,
			domain.OrderEventPreTradeAccepted,
			caller,
			domain.OrderEventPayload{},
		),
		fillSettlementEvent(
			order.ExternalID,
			domain.OrderEventCommitted,
			caller,
			domain.OrderEventPayload{},
		),
	)
	events = append(events, persistence.Events...)
	reservedQuantity, err := reservationAfterOutcomes(order, persistence.Balances)
	if err != nil {
		return domain.OrderSettlement{}, err
	}
	return domain.OrderSettlement{
		ReservedQuantity:     reservedQuantity,
		Account:              account,
		Order:                order.ExternalID,
		ReportID:             &result.ExecutionReport.ExternalID,
		OrderStatus:          persistence.OrderStatus,
		Leaves:               persistence.Leaves,
		AccountPnl:           persistence.AccountPnl,
		AccountPnlHaltReason: persistence.AccountPnlHaltReason,
		AllowedFrom:          domain.OrderStatusesEligibleForFill(),
		Balances:             persistence.Balances,
		Events:               events,
		Trade:                persistence.Trade,
		Blocks:               persistence.Blocks,
		Lock:                 result.Lock,
		SetLock:              true,
	}, nil
}

func executionReportInputFromRequest(
	request domain.ExecutionReportRequest,
) domain.ExecutionReportInput {
	return domain.ExecutionReportInput{
		ExternalID:     request.ExternalID,
		BaseAsset:      request.BaseAsset,
		QuoteAsset:     request.QuoteAsset,
		FillQuantity:   request.FillQuantity,
		FillPrice:      request.FillPrice,
		LeavesQuantity: request.LeavesQuantity,
		LockPrice:      request.LockPrice,
		Commission:     request.Commission,
		Order:          request.Order,
		Account:        request.Account,
		Side:           request.Side,
		OrderStatus:    request.OrderStatus,
		Force:          request.Force,
	}
}

// ensureAutoCreatedAssets creates and publishes missing base and quote assets,
// then reports whether either was newly created.
func (n *localNode) ensureAutoCreatedAssets(
	ctx context.Context, baseAsset string, quoteAsset string,
	operation string, caller domain.Caller,
) (bool, error) {
	created := false
	for _, code := range []string{baseAsset, quoteAsset} {
		if code == "" {
			continue
		}
		assetCreated, err := n.ensureAutoCreatedAsset(ctx, code, operation, caller)
		if err != nil {
			return false, err
		}
		created = created || assetCreated
	}
	return created, nil
}

func (n *localNode) ensureAutoCreatedAsset(
	ctx context.Context, code string, operation string, caller domain.Caller,
) (bool, error) {
	if _, ok, err := n.realm.GetAsset(ctx, code); err != nil {
		return false, fmt.Errorf("read asset for %s: %w", operation, err)
	} else if ok {
		return false, nil
	}
	if err := domain.ValidateAsset(code); err != nil {
		return false, err
	}
	resolver := n.currentEngine()
	if err := n.realm.CreateAssetClass(ctx, domain.AssetClass{
		Code:  autoCreatedAssetClassCode,
		Title: autoCreatedAssetClassTitle,
		Notes: autoCreatedAssetClassNotes,
	}); err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		return false, fmt.Errorf("create auto-created asset class: %w", err)
	}
	asset, err := n.realm.CreateAsset(ctx, domain.Asset{
		Code:       code,
		AssetClass: autoCreatedAssetClassCode,
	})
	if err != nil {
		if errors.Is(err, domain.ErrAlreadyExists) {
			return false, nil
		}
		return false, fmt.Errorf("create auto-created asset %s: %w", code, err)
	}
	if err := resolver.AddAssetResolverEntry(asset); err != nil {
		return n.rollbackAutoCreatedAssetPublication(
			ctx,
			asset,
			fmt.Errorf("publish auto-created asset for %s: %w", operation, err),
		)
	}
	if err := n.audit(context.WithoutCancel(ctx), caller, store.AuditEntry{
		Action: domain.AuditActionCreateAsset,
		Asset:  code,
		Detail: fmt.Sprintf("auto-created asset %s by %s", code, operation),
	}); err != nil {
		return false, n.fatalPostEngineAuditByCode(
			"audit auto-created asset", "asset", asset.Code,
			fmt.Errorf("audit auto-created asset: %w", err),
		)
	}
	return true, nil
}

func (n *localNode) rollbackAutoCreatedAssetPublication(
	ctx context.Context,
	asset domain.Asset,
	cause error,
) (bool, error) {
	durableCtx := context.WithoutCancel(ctx)
	if err := n.realm.DeleteAsset(durableCtx, asset.Code, false); err == nil {
		// The auto-create store write already committed before publication failed,
		// so this is a post-commit failure even though the rollback restored a
		// consistent state: ErrAlreadyExists from the resolver describes an internal
		// store/engine desync, not something the caller did wrong.
		return false, internalPostCommitNodeMutationError(cause)
	} else {
		reconcileErr := n.rebuildEngineFromStore(durableCtx)
		combined := errors.Join(
			cause,
			fmt.Errorf("rollback auto-created asset %q: %w", asset.Code, err),
			reconcileErr,
		)
		return false, n.fatalPostEngineAuditByCode(
			"rollback auto-created asset publication",
			"asset",
			asset.Code,
			combined,
		)
	}
}
