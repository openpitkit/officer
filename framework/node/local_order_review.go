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

package node

import (
	"context"
	"fmt"

	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

type orderReviewChainState struct {
	order                domain.Order
	mode                 string
	held                 bool
	account              domain.Account
	persistenceCompleted bool
	err                  error
}

// beginOrderReview is called inside the account chain, before pre-trade can
// consume rate units. It fills state, including the stored mode. Confirms
// check that mode; cancellation accepts either mode.
func (n *localNode) beginOrderReview(
	ctx context.Context, source domain.Order, state *orderReviewChainState,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	detail, err := n.realm.GetOrder(ctx, source.ExternalID)
	if err != nil {
		return fmt.Errorf("read order review: %w", err)
	}
	if err := domain.RequireOrderAwaitingReview(detail.Order.Status); err != nil {
		return err
	}
	if detail.Review == nil {
		return fmt.Errorf("order %q awaiting review has no review row", source.ExternalID)
	}
	if detail.Review.Held != state.held {
		return fmt.Errorf("order review held flag changed: %w", domain.ErrConflict)
	}
	if err := validatePersistedOrderChainInput(source, detail.Order); err != nil {
		return err
	}
	state.order, state.mode = detail.Order, detail.Review.Mode
	state.account, err = readOrderAccount(ctx, n.realm, detail.Order.Account)
	return err
}

func (n *localNode) recordReviewSettlement(
	ctx context.Context, state *orderReviewChainState,
	settlement domain.OrderSettlement, attest store.EventAttestor,
) error {
	if err := n.persistReviewSettlement(ctx, state, settlement, attest); err != nil {
		operation := "record order review decision"
		if state.persistenceCompleted {
			operation = "read order review decision"
		}
		return n.fatalPostEnginePersistence(operation, state.order.Account, err)
	}
	return nil
}

func (n *localNode) persistReviewSettlement(
	ctx context.Context, state *orderReviewChainState,
	settlement domain.OrderSettlement, attest store.EventAttestor,
) error {
	settlement.AllowedFrom = []domain.OrderStatus{domain.OrderStatusAwaitingReview}
	if _, err := recordOrderSettlementWithAttestation(ctx, n.realm, settlement, attest); err != nil {
		return err
	}
	state.persistenceCompleted = true
	detail, err := n.realm.GetOrder(ctx, state.order.ExternalID)
	if err != nil {
		return err
	}
	state.order = detail.Order
	return nil
}

func (n *localNode) beginAccountOrderReview(
	ctx context.Context, order domain.Order, state *orderReviewChainState,
	source param.AccountID,
) error {
	if err := n.beginOrderReview(ctx, order, state); err != nil {
		return err
	}
	return validateAccountAdministrativeSource(state.account, source)
}

func (n *localNode) beginForcedOrderReview(
	ctx context.Context, order domain.Order, state *orderReviewChainState,
	source param.AccountID, mode string,
) (engine.HeldReservation, error) {
	if err := n.beginAccountOrderReview(ctx, order, state, source); err != nil {
		return engine.HeldReservation{}, err
	}
	if !state.held {
		return engine.HeldReservation{}, fmt.Errorf("order review is not held: %w", domain.ErrConflict)
	}
	if state.mode != mode {
		return engine.HeldReservation{}, fmt.Errorf("order review mode %q does not match %q: %w", state.mode, mode, domain.ErrConflict)
	}
	account := state.account
	var group domain.AccountGroup
	if account.GroupCode != "" {
		var ok bool
		var err error
		group, ok, err = n.realm.GetGroup(ctx, account.GroupCode)
		if err != nil {
			return engine.HeldReservation{}, fmt.Errorf("read force account group %q: %w", account.GroupCode, err)
		}
		// Only ok == false means a dangling group code with no group block.
		if !ok {
			group = domain.AccountGroup{}
		}
	}
	if block := domain.ResolveAccountBlock(account, group); block.Blocked {
		return engine.HeldReservation{}, fmt.Errorf("account %q is blocked at %s tier: %w", account.Code, block.Source, domain.ErrConflict)
	}
	return storedHeldReservation(state.order)
}

func acceptedReviewSettlement(
	state *orderReviewChainState, result engine.OrderResult, caller domain.Caller,
) (domain.OrderSettlement, error) {
	settlement, err := orderAcceptedSettlement(state.order.Account, state.order, result, caller)
	if err != nil {
		return domain.OrderSettlement{}, err
	}
	if state.held {
		settlement.Leaves, settlement.ReservedQuantity = "", ""
		settlement.Balances, settlement.Lock, settlement.SetLock = nil, nil, false
	}
	settlement.ClearReview = true
	return settlement, nil
}

func reviewActionAudit(
	order domain.Order, identity domain.EngineAccountID,
	action domain.AuditAction, detail domain.AuditDetailCode,
) store.AuditEntry {
	return store.AuditEntry{
		Action: action, Account: order.Account, AccountIdentity: identity,
		Detail: submitOrderDetail(order, detail),
	}
}

func (n *localNode) recordReviewRejection(
	ctx context.Context, state *orderReviewChainState,
	result engine.OrderResult, caller domain.Caller, attest store.EventAttestor,
) error {
	settlement := domain.OrderSettlement{
		Account: state.order.Account, Order: state.order.ExternalID,
		OrderStatus: domain.OrderStatusAwaitingReview, Blocks: result.Blocks,
		Events: []domain.OrderEvent{fillSettlementEvent(
			state.order.ExternalID, domain.OrderEventPreTradeRejected, caller,
			orderRejectedPayload(result.Rejects),
		)},
	}
	if err := n.recordReviewSettlement(ctx, state, settlement, attest); err != nil {
		return err
	}
	return n.auditSubmittedOrder(ctx, state.order.Account, state.order, result,
		caller, state.account.EngineAccountID, domain.AuditActionConfirmOrderReview)
}

func (n *localNode) ConfirmOrderReview(
	ctx context.Context, order domain.ExternalID, caller domain.Caller,
	attestFor func(domain.Order, engine.OrderResult) store.EventAttestor,
) (resultOrder domain.Order, resultDecision engine.OrderResult, resultErr error) {
	ctx, finish := PostCommitNotifications(ctx)
	defer n.finishDecisionReviewNotifications(ctx, finish, &resultOrder, caller, &resultErr)
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, engine.OrderResult{}, fmt.Errorf("get order: %w", err)
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	defer done()
	held := detail.Review != nil && detail.Review.Held
	source, err := eng.OrderModel(detail.Order, engine.ReviewFlags{Confirmed: true, AlreadyHeld: held})
	if err != nil {
		return domain.Order{}, engine.OrderResult{}, fmt.Errorf("confirm order review: %w", err)
	}
	state := &orderReviewChainState{held: held}
	var result engine.OrderResult
	var settlement domain.OrderSettlement
	var attest store.EventAttestor
	var preTradeFinalized bool
	builder := asyncengine.Chain(source, func(context.Context) (*orderReviewChainState, error) {
		state.err = n.beginOrderReview(ctx, detail.Order, state)
		if state.err == nil && state.mode != domain.SubmitModeHold {
			state.err = fmt.Errorf("order review mode %q does not match %q: %w", state.mode, domain.SubmitModeHold, domain.ErrConflict)
		}
		return state, state.err
	})
	builder.ExecutePreTrade(asyncengine.PreTradeHooks[*orderReviewChainState]{
		OnRejected: func(_ context.Context, state *orderReviewChainState, rejects []reject.Reject) error {
			result, state.err = eng.RejectedOrder(state.order, rejects)
			if state.err != nil {
				return state.err
			}
			if len(result.Rejects) == 0 {
				state.err = fmt.Errorf("confirm order review: engine rejection has no reject reason")
				return state.err
			}
			result.Blocks = preTradeLatchedBlocks(state.order, state.account.Blocked, result.Rejects)
			if attestFor != nil {
				attest = attestFor(state.order, result)
			}
			state.err = n.recordReviewRejection(ctx, state, result, caller, attest)
			return state.err
		},
		OnReserved: func(_ context.Context, state *orderReviewChainState, reservation asyncengine.OperationResult) (asyncengine.Decision, error) {
			if state.held {
				var stored engine.HeldReservation
				stored, state.err = storedHeldReservation(state.order)
				if state.err == nil {
					result, state.err = eng.ReservedHeldOrder(state.order, reservation, stored)
				}
			} else {
				result, state.err = eng.ReservedOrder(state.order, reservation)
			}
			if state.err != nil {
				return asyncengine.DecisionRollback, state.err
			}
			if result.Verdict != domain.VerdictAccept {
				state.err = fmt.Errorf("confirm order review: engine returned %q under confirmed flag", result.Verdict)
				return asyncengine.DecisionRollback, state.err
			}
			settlement, state.err = acceptedReviewSettlement(state, result, caller)
			if state.err != nil {
				return asyncengine.DecisionRollback, state.err
			}
			if attestFor != nil {
				attest = attestFor(state.order, result)
			}
			return asyncengine.DecisionCommit, nil
		},
	})
	runner := builder.Then(func(_ context.Context, state *orderReviewChainState) error {
		preTradeFinalized = true
		state.err = n.recordReviewSettlement(ctx, state, settlement, attest)
		return state.err
	}).Then(func(_ context.Context, state *orderReviewChainState) error {
		state.err = n.auditSubmittedOrder(ctx, state.order.Account, state.order, result,
			caller, state.account.EngineAccountID, domain.AuditActionConfirmOrderReview)
		return state.err
	}).Finally(func(_ context.Context, state *orderReviewChainState, outcome asyncengine.ChainOutcome) error {
		state.err = n.accountChainTerminalError("confirm order review", state.order.Account,
			state.err, preTradeFinalized, state.persistenceCompleted, outcome)
		return nil
	})
	_, chainErr := runner.Run(ctx, eng.AsyncEngine()).Await(context.Background())
	if err := accountChainRunError("confirm order review", state.err, chainErr); err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	return state.order, result, nil
}

func (n *localNode) ConfirmImmediateReview(
	ctx context.Context, order domain.ExternalID, caller domain.Caller,
	attestFor func(domain.Order, engine.ImmediateResult) store.EventAttestor,
) (resultOrder domain.Order, resultDecision engine.ImmediateResult, resultErr error) {
	ctx, finish := PostCommitNotifications(ctx)
	defer n.finishDecisionReviewNotifications(ctx, finish, &resultOrder, caller, &resultErr)
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, fmt.Errorf("get order: %w", err)
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	defer done()
	held := detail.Review != nil && detail.Review.Held
	source, err := eng.OrderModel(detail.Order, engine.ReviewFlags{Confirmed: true, AlreadyHeld: held})
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, fmt.Errorf("confirm immediate review: %w", err)
	}
	state := &orderReviewChainState{held: held}
	var result engine.ImmediateResult
	var preparation engine.ImmediatePreparation
	var attest store.EventAttestor
	var preTradeFinalized bool
	builder := asyncengine.Chain(source, func(context.Context) (*orderReviewChainState, error) {
		state.err = n.beginOrderReview(ctx, detail.Order, state)
		if state.err == nil && state.mode != domain.SubmitModeImmediate {
			state.err = fmt.Errorf("order review mode %q does not match %q: %w", state.mode, domain.SubmitModeImmediate, domain.ErrConflict)
		}
		return state, state.err
	})
	builder.ExecutePreTrade(asyncengine.PreTradeHooks[*orderReviewChainState]{
		OnRejected: func(_ context.Context, state *orderReviewChainState, rejects []reject.Reject) error {
			result, state.err = eng.RejectedImmediate(state.order, rejects)
			if state.err != nil {
				return state.err
			}
			if len(result.Rejects) == 0 {
				state.err = fmt.Errorf("confirm immediate review: engine rejection has no reject reason")
				return state.err
			}
			result.Blocks = preTradeLatchedBlocks(state.order, state.account.Blocked, result.Rejects)
			if attestFor != nil {
				attest = attestFor(state.order, result)
			}
			state.err = n.recordReviewRejection(ctx, state, engine.OrderResult{
				Verdict: result.Verdict, Rejects: result.Rejects, Blocks: result.Blocks,
			}, caller, attest)
			return state.err
		},
		OnReserved: func(_ context.Context, state *orderReviewChainState, reservation asyncengine.OperationResult) (asyncengine.Decision, error) {
			if state.held {
				var stored engine.HeldReservation
				stored, state.err = storedHeldReservation(state.order)
				if state.err == nil {
					preparation, state.err = eng.PrepareImmediateHeldReservation(state.order, reservation, stored)
				}
			} else {
				preparation, state.err = eng.PrepareImmediateReservation(state.order, reservation)
			}
			if state.err != nil {
				return asyncengine.DecisionRollback, state.err
			}
			if preparation.Verdict != domain.VerdictAccept {
				state.err = fmt.Errorf("confirm immediate review: engine returned %q under confirmed flag", preparation.Verdict)
				return asyncengine.DecisionRollback, state.err
			}
			return asyncengine.DecisionCommit, nil
		},
	})
	builder.Then(func(_ context.Context, _ *orderReviewChainState) error {
		preTradeFinalized = true
		return nil
	})
	settled, err := n.runImmediateReviewReport(ctx, eng, builder, state, &preparation,
		caller, attestFor, &preTradeFinalized, "confirm immediate review",
		domain.AuditActionConfirmOrderReview)
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	if result.Verdict == domain.VerdictReject {
		return state.order, result, nil
	}
	return state.order, settled, nil
}

func (n *localNode) runImmediateReviewReport(
	ctx context.Context, eng engine.Engine,
	builder *asyncengine.ChainBuilder[*orderReviewChainState],
	state *orderReviewChainState, preparation *engine.ImmediatePreparation,
	caller domain.Caller,
	attestFor func(domain.Order, engine.ImmediateResult) store.EventAttestor,
	preTradeFinalized *bool, operation string, action domain.AuditAction,
) (engine.ImmediateResult, error) {
	var result engine.ImmediateResult
	var attest store.EventAttestor
	var reportEntered, reportSettled bool
	builder.ApplyExecutionReport(asyncengine.ExecutionReportHooks[*orderReviewChainState]{
		Report: func(context.Context, *orderReviewChainState) (model.ExecutionReport, error) {
			reportEntered = true
			return preparation.ExecutionReport, nil
		},
		OnSettled: func(_ context.Context, state *orderReviewChainState, postTrade pretrade.PostTradeResult) error {
			result, state.err = eng.SettleImmediate(state.order, *preparation, postTrade)
			if state.err != nil {
				return state.err
			}
			reportSettled = true
			if attestFor != nil {
				attest = attestFor(state.order, result)
			}
			return nil
		},
	})
	runner := builder.Then(func(_ context.Context, state *orderReviewChainState) error {
		settlement, err := immediateAcceptedSettlement(state.order.Account, state.order, result, caller)
		if err != nil {
			state.err = n.fatalPostEnginePersistence("record immediate review decision", state.order.Account, err)
			return state.err
		}
		settlement.ClearReview = true
		state.err = n.recordReviewSettlement(ctx, state, settlement, attest)
		return state.err
	}).Then(func(_ context.Context, state *orderReviewChainState) error {
		state.err = n.auditImmediateSubmission(ctx, state.order.Account, state.order, result,
			caller, state.account.EngineAccountID, action)
		return state.err
	}).Finally(func(_ context.Context, state *orderReviewChainState, outcome asyncengine.ChainOutcome) error {
		if (*preTradeFinalized || reportEntered) && !reportSettled && outcome.Err != nil {
			outcome.Err = immediateReconciliationError(state.order, preparation.ReconciliationState,
				"the execution report failed and the fill is unsettled", chainRootCause(outcome.Err))
		}
		state.err = n.accountChainTerminalError(operation, state.order.Account,
			state.err, *preTradeFinalized || reportEntered, state.persistenceCompleted, outcome)
		return nil
	})
	_, chainErr := runner.Run(ctx, eng.AsyncEngine()).Await(context.Background())
	if err := accountChainRunError(operation, state.err, chainErr); err != nil {
		return engine.ImmediateResult{}, err
	}
	return result, nil
}

func (n *localNode) ForceOrderReview(
	ctx context.Context, order domain.ExternalID, caller domain.Caller,
	attestFor func(domain.Order, engine.OrderResult) store.EventAttestor,
) (resultOrder domain.Order, resultDecision engine.OrderResult, resultErr error) {
	ctx, finish := PostCommitNotifications(ctx)
	defer n.finishDecisionReviewNotifications(ctx, finish, &resultOrder, caller, &resultErr)
	ctx = context.WithoutCancel(ctx)
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, engine.OrderResult{}, fmt.Errorf("get order: %w", err)
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	defer done()
	source, err := eng.AccountID(detail.Order.Account)
	if err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	state := &orderReviewChainState{held: detail.Review != nil && detail.Review.Held}
	var result engine.OrderResult
	builder := asyncengine.Chain(source, func(context.Context) (*orderReviewChainState, error) {
		held, err := n.beginForcedOrderReview(ctx, detail.Order, state, source, domain.SubmitModeHold)
		if err != nil {
			state.err = err
			return state, err
		}
		result, state.err = eng.ForcedHeldOrder(state.order, held)
		return state, state.err
	})
	runner := builder.Then(func(_ context.Context, state *orderReviewChainState) error {
		settlement, err := acceptedReviewSettlement(state, result, caller)
		if err != nil {
			state.err = err
			return err
		}
		var attest store.EventAttestor
		if attestFor != nil {
			attest = attestFor(state.order, result)
		}
		state.err = n.persistReviewSettlement(ctx, state, settlement, attest)
		if state.err != nil && !state.persistenceCompleted {
			state.err = fmt.Errorf("record force order review: %w", state.err)
		}
		return state.err
	}).Then(func(_ context.Context, state *orderReviewChainState) error {
		if err := n.audit(ctx, caller, reviewActionAudit(state.order, state.account.EngineAccountID,
			domain.AuditActionForceOrderReview, domain.AuditDetailForceOrderReview)); err != nil {
			state.err = n.fatalPostCommitAudit("audit force order review", state.order.Account, err)
		}
		return state.err
	}).Finally(func(_ context.Context, state *orderReviewChainState, outcome asyncengine.ChainOutcome) error {
		state.err = n.accountChainTerminalError("force order review", state.order.Account,
			state.err, state.persistenceCompleted, state.persistenceCompleted, outcome)
		return nil
	})
	_, chainErr := runner.Run(ctx, eng.AsyncEngine()).Await(context.Background())
	if err := accountChainRunError("force order review", state.err, chainErr); err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	return state.order, result, nil
}

func (n *localNode) ForceImmediateReview(
	ctx context.Context, order domain.ExternalID, caller domain.Caller,
	attestFor func(domain.Order, engine.ImmediateResult) store.EventAttestor,
) (resultOrder domain.Order, resultDecision engine.ImmediateResult, resultErr error) {
	ctx, finish := PostCommitNotifications(ctx)
	defer n.finishDecisionReviewNotifications(ctx, finish, &resultOrder, caller, &resultErr)
	ctx = context.WithoutCancel(ctx)
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, fmt.Errorf("get order: %w", err)
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	defer done()
	source, err := eng.AccountID(detail.Order.Account)
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	state := &orderReviewChainState{held: detail.Review != nil && detail.Review.Held}
	var preparation engine.ImmediatePreparation
	builder := asyncengine.Chain(source, func(context.Context) (*orderReviewChainState, error) {
		held, err := n.beginForcedOrderReview(ctx, detail.Order, state, source, domain.SubmitModeImmediate)
		if err != nil {
			state.err = err
			return state, err
		}
		preparation, state.err = eng.PrepareForcedHeldImmediate(state.order, held)
		return state, state.err
	})
	preTradeFinalized := false
	result, err := n.runImmediateReviewReport(ctx, eng, builder, state, &preparation,
		caller, attestFor, &preTradeFinalized, "force immediate review",
		domain.AuditActionForceOrderReview)
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	return state.order, result, nil
}

func (n *localNode) CancelOrderReview(
	ctx context.Context, order domain.ExternalID, caller domain.Caller, attest store.EventAttestor,
) (resultOrder domain.Order, resultErr error) {
	ctx, finish := PostCommitNotifications(ctx)
	defer n.finishDecisionReviewNotifications(ctx, finish, &resultOrder, caller, &resultErr)
	ctx = context.WithoutCancel(ctx)
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, fmt.Errorf("get order: %w", err)
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, err
	}
	defer done()
	source, err := eng.AccountID(detail.Order.Account)
	if err != nil {
		return domain.Order{}, err
	}
	state := &orderReviewChainState{held: detail.Review != nil && detail.Review.Held}
	if state.held {
		operation := cancelReportOperation{
			name:        "order review cancellation",
			allowedFrom: []domain.OrderStatus{domain.OrderStatusAwaitingReview},
			clearReview: true,
			prepare: func() (domain.Order, error) {
				if err := n.beginAccountOrderReview(ctx, detail.Order, state, source); err != nil {
					return domain.Order{}, err
				}
				if _, err := storedHeldReservation(state.order); err != nil {
					return domain.Order{}, err
				}
				return state.order, nil
			},
			audit: func(report *executionReportChainState) store.AuditEntry {
				return reviewActionAudit(report.order, state.account.EngineAccountID,
					domain.AuditActionCancelOrderReview, domain.AuditDetailCancelOrderReview)
			},
		}
		cancelled, _, err := n.cancelOrderReport(ctx, eng, source, detail.Order.Account,
			order, "0", caller, attest, operation)
		return cancelled, err
	}
	builder := asyncengine.Chain(source, func(context.Context) (*orderReviewChainState, error) {
		state.err = n.beginAccountOrderReview(ctx, detail.Order, state, source)
		if state.err != nil {
			return nil, state.err
		}
		return state, nil
	})
	runner := builder.Then(func(_ context.Context, state *orderReviewChainState) error {
		settlement := domain.OrderSettlement{
			Account: state.order.Account, Order: order,
			AllowedFrom: []domain.OrderStatus{domain.OrderStatusAwaitingReview},
			OrderStatus: domain.OrderStatusCancelled, Leaves: "0", ClearReview: true,
			Events: []domain.OrderEvent{fillSettlementEvent(order, domain.OrderEventCancelled, caller, domain.OrderEventPayload{})},
		}
		if _, err := recordOrderSettlementWithAttestation(ctx, n.realm, settlement, attest); err != nil {
			state.err = fmt.Errorf("record order review cancellation: %w", err)
			return state.err
		}
		state.order.Status = domain.OrderStatusCancelled
		state.order.Leaves = "0"
		if err := n.audit(ctx, caller, reviewActionAudit(state.order, state.account.EngineAccountID,
			domain.AuditActionCancelOrderReview, domain.AuditDetailCancelOrderReview)); err != nil {
			state.err = n.fatalPostCommitAudit("audit order review cancellation", state.order.Account, err)
		}
		return state.err
	})
	_, chainErr := runner.Run(ctx, eng.AsyncEngine()).Await(context.Background())
	if err := accountChainRunError("cancel order review", state.err, chainErr); err != nil {
		return domain.Order{}, err
	}
	return state.order, nil
}

func storedHeldReservation(order domain.Order) (engine.HeldReservation, error) {
	if err := validateHeldQuantity(order.ReservedQuantity); err != nil {
		return engine.HeldReservation{}, err
	}
	if len(order.Lock) == 0 {
		return engine.HeldReservation{}, fmt.Errorf("held order review has no lock")
	}
	return engine.HeldReservation{Lock: order.Lock, Quantity: order.ReservedQuantity}, nil
}

func validateHeldQuantity(value string) error {
	quantity, err := domain.ParseOpenQuantity(value)
	if err != nil {
		return fmt.Errorf("held order review has no reserved quantity: %w", err)
	}
	if quantity.IsZero() {
		return fmt.Errorf("held order review has no reserved quantity")
	}
	return nil
}
