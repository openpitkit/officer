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
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

type orderReviewChainState struct {
	order                domain.Order
	mode                 string
	accountBlocked       bool
	accountIdentity      domain.EngineAccountID
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
	if err := validatePersistedOrderChainInput(source, detail.Order); err != nil {
		return err
	}
	state.order, state.mode = detail.Order, detail.Review.Mode
	state.accountBlocked, state.accountIdentity, err = readAccountBlockState(ctx, n.realm, detail.Order.Account)
	return err
}

func (n *localNode) recordReviewSettlement(
	ctx context.Context, state *orderReviewChainState,
	settlement domain.OrderSettlement, attest store.EventAttestor,
) error {
	settlement.AllowedFrom = []domain.OrderStatus{domain.OrderStatusAwaitingReview}
	if _, err := recordOrderSettlementWithAttestation(ctx, n.realm, settlement, attest); err != nil {
		return n.fatalPostEnginePersistence("record order review decision", state.order.Account, err)
	}
	state.persistenceCompleted = true
	detail, err := n.realm.GetOrder(ctx, state.order.ExternalID)
	if err != nil {
		return n.fatalPostEnginePersistence("read order review decision", state.order.Account, err)
	}
	state.order = detail.Order
	return nil
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
		caller, state.accountIdentity, domain.AuditActionConfirmOrderReview)
}

func (n *localNode) ConfirmOrderReview(
	ctx context.Context, order domain.ExternalID, caller domain.Caller,
	attestFor func(domain.Order, engine.OrderResult) store.EventAttestor,
) (domain.Order, engine.OrderResult, error) {
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, engine.OrderResult{}, fmt.Errorf("get order: %w", err)
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	defer done()
	source, err := eng.OrderModel(detail.Order, engine.ReviewFlags{Confirmed: true})
	if err != nil {
		return domain.Order{}, engine.OrderResult{}, fmt.Errorf("confirm order review: %w", err)
	}
	state := &orderReviewChainState{}
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
			result.Blocks = preTradeLatchedBlocks(state.order, state.accountBlocked, result.Rejects)
			if attestFor != nil {
				attest = attestFor(state.order, result)
			}
			state.err = n.recordReviewRejection(ctx, state, result, caller, attest)
			return state.err
		},
		OnReserved: func(_ context.Context, state *orderReviewChainState, reservation asyncengine.OperationResult) (asyncengine.Decision, error) {
			result, state.err = eng.ReservedOrder(state.order, reservation)
			if state.err != nil {
				return asyncengine.DecisionRollback, state.err
			}
			if result.Verdict != domain.VerdictAccept {
				state.err = fmt.Errorf("confirm order review: engine returned %q under confirmed flag", result.Verdict)
				return asyncengine.DecisionRollback, state.err
			}
			settlement, state.err = orderAcceptedSettlement(state.order.Account, state.order, result, caller)
			if state.err != nil {
				return asyncengine.DecisionRollback, state.err
			}
			settlement.ClearReview = true
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
			caller, state.accountIdentity, domain.AuditActionConfirmOrderReview)
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
) (domain.Order, engine.ImmediateResult, error) {
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, fmt.Errorf("get order: %w", err)
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	defer done()
	source, err := eng.OrderModel(detail.Order, engine.ReviewFlags{Confirmed: true})
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, fmt.Errorf("confirm immediate review: %w", err)
	}
	state := &orderReviewChainState{}
	var result engine.ImmediateResult
	var preparation engine.ImmediatePreparation
	var attest store.EventAttestor
	var preTradeFinalized, reportSettled bool
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
			result.Blocks = preTradeLatchedBlocks(state.order, state.accountBlocked, result.Rejects)
			if attestFor != nil {
				attest = attestFor(state.order, result)
			}
			state.err = n.recordReviewRejection(ctx, state, engine.OrderResult{
				Verdict: result.Verdict, Rejects: result.Rejects, Blocks: result.Blocks,
			}, caller, attest)
			return state.err
		},
		OnReserved: func(_ context.Context, state *orderReviewChainState, reservation asyncengine.OperationResult) (asyncengine.Decision, error) {
			preparation, state.err = eng.PrepareImmediateReservation(state.order, reservation)
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
	builder.ApplyExecutionReport(asyncengine.ExecutionReportHooks[*orderReviewChainState]{
		Report: func(context.Context, *orderReviewChainState) (model.ExecutionReport, error) {
			return preparation.ExecutionReport, nil
		},
		OnSettled: func(_ context.Context, state *orderReviewChainState, postTrade pretrade.PostTradeResult) error {
			result, state.err = eng.SettleImmediate(state.order, preparation, postTrade)
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
			caller, state.accountIdentity, domain.AuditActionConfirmOrderReview)
		return state.err
	}).Finally(func(_ context.Context, state *orderReviewChainState, outcome asyncengine.ChainOutcome) error {
		if preTradeFinalized && !reportSettled && outcome.Err != nil {
			outcome.Err = immediateReconciliationError(state.order, preparation.ReconciliationState,
				"the execution report failed and the fill is unsettled", chainRootCause(outcome.Err))
		}
		state.err = n.accountChainTerminalError("confirm immediate review", state.order.Account,
			state.err, preTradeFinalized, state.persistenceCompleted, outcome)
		return nil
	})
	_, chainErr := runner.Run(ctx, eng.AsyncEngine()).Await(context.Background())
	if err := accountChainRunError("confirm immediate review", state.err, chainErr); err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	return state.order, result, nil
}

func (n *localNode) CancelOrderReview(
	ctx context.Context, order domain.ExternalID, caller domain.Caller, attest store.EventAttestor,
) (domain.Order, error) {
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
	state := &orderReviewChainState{}
	builder := asyncengine.Chain(source, func(context.Context) (*orderReviewChainState, error) {
		state.err = n.beginOrderReview(ctx, detail.Order, state)
		if state.err != nil {
			return nil, state.err
		}
		if err := validateAccountAdministrativeSource(domain.Account{
			Code: state.order.Account, EngineAccountID: state.accountIdentity,
		}, source); err != nil {
			state.err = err
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
		if err := n.audit(ctx, caller, store.AuditEntry{
			Action: domain.AuditActionCancelOrderReview, Account: state.order.Account,
			AccountIdentity: state.accountIdentity,
			Detail:          submitOrderDetail(state.order, domain.AuditDetailCancelOrderReview),
		}); err != nil {
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
