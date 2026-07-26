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

// SubmitOrder runs the pre-trade submit on the account lane and persists the
// order, submitted event, and engine outcome in one store transaction. On accept
// it records pre_trade_accepted and committed, persists the lock, canonical
// leaves, balance outcomes, and committed status; on reject it records
// pre_trade_rejected and rejected status. An order for an account Officer does
// not know yet auto-creates it (like a fresh adjustment target), so submitting
// against an unknown account is processed rather than rejected as invalid.
func (n *localNode) SubmitOrder(
	ctx context.Context, key Key, o domain.Order, caller domain.Caller,
) (domain.Order, error) {
	order, _, err := n.submitOrder(ctx, key, o, caller, nil)
	return order, err
}

func (n *localNode) SubmitOrderWithAttestation(
	ctx context.Context,
	key Key,
	o domain.Order,
	caller domain.Caller,
	attestFor func(domain.Order, engine.OrderResult) store.EventAttestor,
) (domain.Order, engine.OrderResult, error) {
	return n.submitOrder(ctx, key, o, caller, attestFor)
}

func (n *localNode) submitOrder(
	ctx context.Context,
	key Key,
	o domain.Order,
	caller domain.Caller,
	attestFor func(domain.Order, engine.OrderResult) store.EventAttestor,
) (domain.Order, engine.OrderResult, error) {
	// Register the account and both order assets and rebuild pre-lane so the
	// resolver knows them before RunAccountSynchronized resolves the account.
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, key.Account, "submit order", caller, o.BaseAsset, o.QuoteAsset,
	); err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	accountID, err := n.accountDiagnosticID(ctx, key.Account)
	if err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	defer done()

	var order domain.Order
	var result engine.OrderResult
	var attest store.EventAttestor
	var attestSubmitted store.EventAttestor
	if attestFor != nil {
		attestSubmitted = func(
			ctx context.Context, event domain.OrderEvent,
		) (domain.EventAttestation, bool, error) {
			if attest == nil {
				return domain.EventAttestation{}, false, nil
			}
			return attest(ctx, event)
		}
	}
	engineApplied := false
	draft := submittedOrderDraft(key, o, caller)
	submitted := submittedOrderEvent(caller)
	if err := eng.RunAccountSynchronized(ctx, key.Account, func(lane engine.AccountLane) error {
		var err error
		order, err = recordOrderSubmissionWithAttestation(
			ctx,
			n.realm,
			draft,
			submitted,
			func(persisted domain.Order) (domain.OrderSettlement, error) {
				result, err = lane.SubmitOrder(ctx, persisted)
				if err != nil {
					return domain.OrderSettlement{}, fmt.Errorf("submit order: %w", err)
				}
				engineApplied = true
				if attestFor != nil {
					attest = attestFor(persisted, result)
				}
				if result.Accepted {
					return orderAcceptedSettlement(key, persisted, result, caller), nil
				}
				return orderRejectedSettlement(key, persisted, result.Rejects, caller), nil
			},
			attestSubmitted,
		)
		if err != nil {
			if engineApplied {
				return n.fatalPostEnginePersistence(
					"record order submission", accountID, err,
				)
			}
			return err
		}

		if err := n.audit(ctx, caller, store.AuditEntry{
			Action:  domain.AuditActionSubmitOrder,
			Account: key.Account,
			Detail:  submitOrderDetail(order, result.Accepted),
		}); err != nil {
			return n.fatalPostEnginePersistence(
				"audit submit order",
				accountID,
				fmt.Errorf("audit submit order: %w", err),
			)
		}
		return nil
	}); err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	return order, result, nil
}

func orderAcceptedSettlement(
	key Key, order domain.Order, result engine.OrderResult, caller domain.Caller,
) domain.OrderSettlement {
	return domain.OrderSettlement{
		Account:     key.Account,
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusCommitted,
		Leaves:      result.LeavesQuantity,
		Balances:    balanceSettlementsFrom(result.Outcomes),
		Events: []domain.OrderEvent{
			fillSettlementEvent(order.ExternalID, domain.OrderEventPreTradeAccepted, caller, domain.OrderEventPayload{}),
			fillSettlementEvent(order.ExternalID, domain.OrderEventCommitted, caller, domain.OrderEventPayload{}),
		},
		Lock:    result.Lock,
		SetLock: true,
	}
}

func orderRejectedSettlement(
	key Key, order domain.Order, rejects []domain.OrderReject, caller domain.Caller,
) domain.OrderSettlement {
	payload := domain.OrderEventPayload{}
	if len(rejects) > 0 {
		r := rejects[0]
		payload.RejectCode = r.Code
		payload.RejectScope = r.Scope
		payload.RejectPolicy = r.Policy
		payload.RejectReason = r.Reason
		payload.RejectDetails = r.Details
	}
	return domain.OrderSettlement{
		Account:     key.Account,
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusRejected,
		Events: []domain.OrderEvent{
			fillSettlementEvent(order.ExternalID, domain.OrderEventPreTradeRejected, caller, payload),
		},
	}
}

// SubmitImmediate records the order, runs the engine pre-trade and, on accept,
// commits and settles the fill in the same engine call at the captured lock
// price so the held amount nets to zero, then persists the filled lifecycle. On
// reject the order is recorded rejected. The backend audits the issued approval.
func (n *localNode) SubmitImmediate(
	ctx context.Context, key Key, o domain.Order, caller domain.Caller,
) (domain.Order, engine.ImmediateResult, error) {
	return n.submitImmediate(ctx, key, o, caller, nil)
}

func (n *localNode) SubmitImmediateWithAttestation(
	ctx context.Context,
	key Key,
	o domain.Order,
	caller domain.Caller,
	attestFor func(domain.Order, engine.ImmediateResult) store.EventAttestor,
) (domain.Order, engine.ImmediateResult, error) {
	return n.submitImmediate(ctx, key, o, caller, attestFor)
}

func (n *localNode) submitImmediate(
	ctx context.Context,
	key Key,
	o domain.Order,
	caller domain.Caller,
	attestFor func(domain.Order, engine.ImmediateResult) store.EventAttestor,
) (domain.Order, engine.ImmediateResult, error) {
	// Register the account and both order assets and rebuild pre-lane (see
	// SubmitOrder).
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, key.Account, "submit immediate", caller, o.BaseAsset, o.QuoteAsset,
	); err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	accountID, err := n.accountDiagnosticID(ctx, key.Account)
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	defer done()

	var order domain.Order
	var result engine.ImmediateResult
	var attest store.EventAttestor
	var attestSubmitted store.EventAttestor
	if attestFor != nil {
		attestSubmitted = func(ctx context.Context, event domain.OrderEvent) (domain.EventAttestation, bool, error) {
			if attest == nil {
				return domain.EventAttestation{}, false, nil
			}
			return attest(ctx, event)
		}
	}
	engineApplied := false
	draft := submittedOrderDraft(key, o, caller)
	submitted := submittedOrderEvent(caller)
	if err := eng.RunAccountSynchronized(ctx, key.Account, func(lane engine.AccountLane) error {
		var err error
		order, err = recordOrderSubmissionWithAttestation(
			ctx,
			n.realm,
			draft,
			submitted,
			func(persisted domain.Order) (domain.OrderSettlement, error) {
				result, err = lane.SubmitImmediate(ctx, persisted)
				if err != nil {
					return domain.OrderSettlement{}, fmt.Errorf("submit immediate: %w", err)
				}
				engineApplied = true
				if !result.Accepted {
					if attestFor != nil {
						attest = attestFor(persisted, result)
					}
					return orderRejectedSettlement(key, persisted, result.Rejects, caller), nil
				}
				if attestFor != nil {
					attest = attestFor(persisted, result)
				}
				return immediateAcceptedSettlement(
					key,
					persisted,
					result,
					caller,
				), nil
			},
			attestSubmitted,
		)
		if err != nil {
			if engineApplied {
				return n.fatalPostEnginePersistence(
					"record immediate submission", accountID, err,
				)
			}
			return err
		}
		if result.Accepted {
			if err := n.mirrorEngineBlocksAudit(ctx, order.ExternalID, result.Blocks); err != nil {
				return n.fatalPostEnginePersistence(
					"audit immediate engine blocks", accountID, err,
				)
			}
		}
		return nil
	}); err != nil {
		return domain.Order{}, engine.ImmediateResult{}, err
	}
	return order, result, nil
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

	var confirmed domain.Order
	if err := eng.RunAccountSynchronized(
		ctx, routeDetail.Order.Account, func(_ engine.AccountLane) error {
			detail, err := n.realm.GetOrder(ctx, order)
			if err != nil {
				return fmt.Errorf("get order: %w", err)
			}
			if err := domain.RequireOrderModifiable(
				detail.Order.Status, false,
			); err != nil {
				return err
			}
			events, err := n.realm.ListOrderEvents(ctx, order)
			if err != nil {
				return fmt.Errorf("list order events: %w", err)
			}
			if err := requireNoExecutionReportActivity(order, events); err != nil {
				return err
			}
			if orderEventExists(events, domain.OrderEventConfirmed) {
				confirmed = detail.Order
				return nil
			}
			if _, err := recordOrderSettlementWithAttestation(
				ctx,
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
				return fmt.Errorf("record order confirmation: %w", err)
			}
			confirmed = detail.Order
			return nil
		},
	); err != nil {
		return domain.Order{}, err
	}
	return confirmed, nil
}

// CancelOrder synthesizes a terminal execution report for an untouched
// workflow order. The normal engine settlement path releases its stored lock.
func (n *localNode) CancelOrder(
	ctx context.Context, order domain.ExternalID, caller domain.Caller,
) (domain.Order, engine.ExecutionReportResult, error) {
	return n.cancelOrder(ctx, order, caller, nil)
}

func (n *localNode) CancelOrderWithAttestation(
	ctx context.Context,
	order domain.ExternalID,
	caller domain.Caller,
	attest store.EventAttestor,
) (domain.Order, engine.ExecutionReportResult, error) {
	return n.cancelOrder(ctx, order, caller, attest)
}

func (n *localNode) cancelOrder(
	ctx context.Context,
	order domain.ExternalID,
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
	accountID, err := n.accountDiagnosticID(ctx, routeDetail.Order.Account)
	if err != nil {
		return domain.Order{}, engine.ExecutionReportResult{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.ExecutionReportResult{}, err
	}
	defer done()

	var cancelled domain.Order
	var result engine.ExecutionReportResult
	if err := eng.RunAccountSynchronized(
		ctx, routeDetail.Order.Account, func(lane engine.AccountLane) error {
			detail, err := n.realm.GetOrder(ctx, order)
			if err != nil {
				return fmt.Errorf("get order: %w", err)
			}
			if err := domain.RequireOrderModifiable(
				detail.Order.Status, false,
			); err != nil {
				return err
			}
			events, err := n.realm.ListOrderEvents(ctx, order)
			if err != nil {
				return fmt.Errorf("list order events: %w", err)
			}
			if err := requireNoExecutionReportActivity(order, events); err != nil {
				return err
			}

			in := domain.ExecutionReportInput{
				Order:          order,
				Account:        detail.Order.Account,
				BaseAsset:      detail.Order.BaseAsset,
				QuoteAsset:     detail.Order.QuoteAsset,
				Side:           detail.Order.Side,
				LeavesQuantity: detail.Order.Leaves,
				Lock:           append([]byte(nil), detail.Order.Lock...),
				OrderStatus:    domain.OrderStatusCancelled,
			}
			if _, err := domain.ExecutionReportRequiresEngine(in); err != nil {
				return fmt.Errorf("build cancellation execution report: %w", err)
			}
			request := domain.ExecutionReportRequestFromInput(in)
			result, err = lane.ApplyExecutionReport(ctx, in)
			if err != nil {
				return fmt.Errorf("apply cancellation execution report: %w", err)
			}
			if result.Persistence == nil {
				return fmt.Errorf(
					"apply cancellation execution report returned no persistence write set: %w",
					domain.ErrInvalid,
				)
			}
			persistence := stampExecutionReportPersistence(
				*result.Persistence, caller, request,
			)
			settlement := domain.OrderSettlement{
				Account:     in.Account,
				Order:       order,
				OrderStatus: persistence.OrderStatus,
				Leaves:      persistence.Leaves,
				Balances:    persistence.Balances,
				Events:      persistence.Events,
				Trade:       persistence.Trade,
				Blocks: accountBlockSettlementsFrom(
					order, persistence.Blocks,
				),
			}
			if _, err := recordOrderSettlementWithAttestation(
				ctx, n.realm, settlement, attest,
			); err != nil {
				return n.fatalPostEnginePersistence(
					"record cancellation execution report",
					accountID,
					fmt.Errorf("record cancellation execution report: %w", err),
				)
			}
			if err := n.mirrorEngineBlocksAudit(ctx, order, result.Blocks); err != nil {
				return n.fatalPostEnginePersistence(
					"audit cancellation execution report engine blocks",
					accountID,
					err,
				)
			}
			if err := n.audit(ctx, caller, store.AuditEntry{
				Action:  domain.AuditActionExecutionReport,
				Account: in.Account,
				Detail: executionReportDetail(
					in, domain.OrderStatusCancelled, len(result.Blocks),
				),
			}); err != nil {
				return n.fatalPostEnginePersistence(
					"audit cancellation execution report",
					accountID,
					fmt.Errorf("audit cancellation execution report: %w", err),
				)
			}
			cancelled = detail.Order
			cancelled.Status = persistence.OrderStatus
			if persistence.Leaves != "" {
				cancelled.Leaves = persistence.Leaves
			}
			return nil
		},
	); err != nil {
		return domain.Order{}, engine.ExecutionReportResult{}, err
	}
	return cancelled, result, nil
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

func submittedOrderDraft(key Key, o domain.Order, caller domain.Caller) domain.Order {
	o.Account = key.Account
	o.Source = caller.Source
	o.Principal = caller.Principal
	o.Status = domain.OrderStatusSubmitted
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
	key Key,
	order domain.Order,
	result engine.ImmediateResult,
	caller domain.Caller,
) domain.OrderSettlement {
	fillPayload := accountBlockPayload(result.Blocks)
	fillPayload.FillQuantity = result.FillQuantity
	fillPayload.FillPrice = result.SettlementLockPrice
	fillPayload.FillLockPrice = result.SettlementLockPrice
	return domain.OrderSettlement{
		Account:              key.Account,
		Order:                order.ExternalID,
		OrderStatus:          domain.OrderStatusFilled,
		AccountPnl:           result.AccountPnl,
		AccountPnlHaltReason: result.AccountPnlHaltReason,
		AllowedFrom:          domain.OrderStatusesEligibleForFill(),
		Balances:             balanceSettlementsFrom(result.Outcomes),
		Events: []domain.OrderEvent{
			fillSettlementEvent(order.ExternalID, domain.OrderEventPreTradeAccepted, caller, domain.OrderEventPayload{}),
			fillSettlementEvent(order.ExternalID, domain.OrderEventCommitted, caller, domain.OrderEventPayload{}),
			fillSettlementEvent(order.ExternalID, domain.OrderEventFill, caller, fillPayload),
		},
		Trade: &domain.Trade{
			Order:      order.ExternalID,
			Account:    key.Account,
			Source:     caller.Source,
			Principal:  caller.Principal,
			BaseAsset:  order.BaseAsset,
			QuoteAsset: order.QuoteAsset,
			Side:       order.Side,
			Quantity:   result.FillQuantity,
			Price:      result.SettlementLockPrice,
			LockPrice:  result.SettlementLockPrice,
		},
		Blocks:  accountBlockSettlementsFrom(order.ExternalID, result.Blocks),
		Lock:    result.Lock,
		SetLock: true,
	}
}

// ensureAutoCreatedAssets creates the base and quote assets when missing and
// reports whether either was newly created. It is a pure store helper: it never
// touches the engine because assets are Officer dictionary state.
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
	if err := n.realm.CreateAssetClass(ctx, domain.AssetClass{
		Code:  autoCreatedAssetClassCode,
		Title: autoCreatedAssetClassTitle,
		Notes: autoCreatedAssetClassNotes,
	}); err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		return false, fmt.Errorf("create auto-created asset class: %w", err)
	}
	if err := n.realm.CreateAsset(ctx, domain.Asset{
		Code:       code,
		AssetClass: autoCreatedAssetClassCode,
	}); err != nil {
		if errors.Is(err, domain.ErrAlreadyExists) {
			return false, nil
		}
		return false, fmt.Errorf("create auto-created asset %s: %w", code, err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionCreateAsset,
		Asset:  code,
		Detail: fmt.Sprintf("auto-created asset %s by %s", code, operation),
	}); err != nil {
		return false, fmt.Errorf("audit auto-created asset: %w", err)
	}
	return true, nil
}
