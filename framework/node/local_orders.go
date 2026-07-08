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
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

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

type reservationAttestingRealm interface {
	ResolveOrderReservationWithAttestation(
		ctx context.Context,
		res domain.ReservationResolution,
		attest store.EventAttestor,
	) error
}

type orderSettlementAttestingRealm interface {
	RecordOrderSettlementWithAttestation(
		ctx context.Context,
		st domain.OrderSettlement,
		attest store.EventAttestor,
	) error
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

func resolveOrderReservationWithAttestation(
	ctx context.Context,
	realm store.RealmStore,
	res domain.ReservationResolution,
	attest store.EventAttestor,
) error {
	if attest == nil {
		return realm.ResolveOrderReservation(ctx, res)
	}
	attesting, ok := realm.(reservationAttestingRealm)
	if !ok {
		return fmt.Errorf(
			"store: reservation attestation unsupported: %w",
			domain.ErrNotImplemented,
		)
	}
	return attesting.ResolveOrderReservationWithAttestation(ctx, res, attest)
}

func recordOrderSettlementWithAttestation(
	ctx context.Context,
	realm store.RealmStore,
	st domain.OrderSettlement,
	attest store.EventAttestor,
) error {
	if attest == nil {
		return realm.RecordOrderSettlement(ctx, st)
	}
	attesting, ok := realm.(orderSettlementAttestingRealm)
	if !ok {
		return fmt.Errorf(
			"store: order settlement attestation unsupported: %w",
			domain.ErrNotImplemented,
		)
	}
	return attesting.RecordOrderSettlementWithAttestation(ctx, st, attest)
}

// SubmitOrder runs the pre-trade submit on the account lane and persists the
// order, submitted event, and engine outcome in one store transaction. On accept
// it records pre_trade_accepted and reservation_committed, persists the lock,
// balance outcomes, and committed status; on reject it records
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
	attest store.EventAttestor,
) (domain.Order, engine.OrderResult, error) {
	return n.submitOrder(ctx, key, o, caller, attest)
}

func (n *localNode) submitOrder(
	ctx context.Context,
	key Key,
	o domain.Order,
	caller domain.Caller,
	attest store.EventAttestor,
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
				if result.Accepted {
					return orderAcceptedSettlement(key, persisted, result, caller), nil
				}
				return orderRejectedSettlement(key, persisted, result.Rejects, caller), nil
			},
			attest,
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
		Balances:    balanceSettlementsFrom(result.Outcomes),
		Events: []domain.OrderEvent{
			fillSettlementEvent(order.ExternalID, domain.OrderEventPreTradeAccepted, caller, domain.OrderEventPayload{}),
			fillSettlementEvent(order.ExternalID, domain.OrderEventReservationCommitted, caller, domain.OrderEventPayload{}),
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

// SubmitHold records the order, runs the engine pre-trade keeping the
// reservation held, and persists the accept/reject lifecycle. On accept the
// held amount stays reserved on engine storage; the order is left accepted and
// the result carries the approval id, lock, and settlement estimate. On reject
// the order is recorded rejected. The backend audits the issued approval.
func (n *localNode) SubmitHold(
	ctx context.Context, key Key, o domain.Order, caller domain.Caller,
) (domain.Order, engine.HoldResult, error) {
	return n.submitHold(ctx, key, o, caller, nil)
}

func (n *localNode) SubmitHoldWithAttestation(
	ctx context.Context,
	key Key,
	o domain.Order,
	caller domain.Caller,
	attestFor func(domain.Order, engine.HoldResult) store.EventAttestor,
) (domain.Order, engine.HoldResult, error) {
	return n.submitHold(ctx, key, o, caller, attestFor)
}

func (n *localNode) submitHold(
	ctx context.Context,
	key Key,
	o domain.Order,
	caller domain.Caller,
	attestFor func(domain.Order, engine.HoldResult) store.EventAttestor,
) (domain.Order, engine.HoldResult, error) {
	// Register the account and both order assets and rebuild pre-lane (see
	// SubmitOrder).
	if err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, key.Account, "submit hold", caller, o.BaseAsset, o.QuoteAsset,
	); err != nil {
		return domain.Order{}, engine.HoldResult{}, err
	}
	accountID, err := n.accountDiagnosticID(ctx, key.Account)
	if err != nil {
		return domain.Order{}, engine.HoldResult{}, err
	}
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, engine.HoldResult{}, err
	}
	defer done()

	var order domain.Order
	var result engine.HoldResult
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
				result, err = lane.ReserveHold(ctx, persisted)
				if err != nil {
					return domain.OrderSettlement{}, fmt.Errorf("reserve hold: %w", err)
				}
				if !result.Accepted {
					if attestFor != nil {
						attest = attestFor(persisted, result)
					}
					return orderRejectedSettlement(key, persisted, result.Rejects, caller), nil
				}
				if attestFor != nil {
					attest = attestFor(persisted, result)
				}
				return holdAcceptedSettlement(key, persisted, result, caller), nil
			},
			attestSubmitted,
		)
		return err
	}); err != nil {
		// The order transaction failed (it also carries the held-intent write). Every
		// hold-path engine effect is reversible: a reject held nothing, an accept
		// registered an in-memory hold. Roll a registered hold back off the lane -
		// RollbackHeld re-enters the account lane, so it must run after
		// RunAccountSynchronized returns - so the engine and the unwritten store agree,
		// then surface the error. Use an uncancellable context so a caller-cancelled
		// request still completes the rollback. Only a failed rollback leaves the
		// engine and store diverged, which is the unrecoverable fail-stop case.
		if result.Accepted && result.ApprovalID != "" {
			rbCtx := context.WithoutCancel(ctx)
			if rbErr := eng.RunAccountSynchronized(
				rbCtx, key.Account, func(lane engine.AccountLane) error {
					return lane.RollbackHeld(rbCtx, result.ApprovalID)
				},
			); rbErr != nil {
				return domain.Order{}, engine.HoldResult{}, n.fatalPostEnginePersistence(
					"rollback held after failed hold submission", accountID, rbErr,
				)
			}
		}
		return domain.Order{}, engine.HoldResult{}, err
	}
	return order, result, nil
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
				return immediateAcceptedSettlement(key, persisted, result, caller), nil
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

// ConfirmHeld commits the held reservation through the engine and records the
// committed lifecycle on the order. The backend audits the confirmation. The
// returned bool reports whether force actually bypassed a terminal-order guard.
func (n *localNode) ConfirmHeld(
	ctx context.Context, order domain.ExternalID,
	approvalID string, caller domain.Caller, force bool,
) (domain.Order, bool, error) {
	return n.confirmHeld(ctx, order, approvalID, caller, force, nil)
}

func (n *localNode) ConfirmHeldWithAttestation(
	ctx context.Context,
	order domain.ExternalID,
	approvalID string,
	caller domain.Caller,
	force bool,
	attest store.EventAttestor,
) (domain.Order, bool, error) {
	return n.confirmHeld(ctx, order, approvalID, caller, force, attest)
}

func (n *localNode) confirmHeld(
	ctx context.Context,
	order domain.ExternalID,
	approvalID string,
	caller domain.Caller,
	force bool,
	attest store.EventAttestor,
) (domain.Order, bool, error) {
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, false, err
	}
	defer done()

	// Outer fetch is for routing only (the account keys the lane); the inner fetch
	// inside the lane re-reads the authoritative status under serialization with
	// fills and reports, closing the TOCTOU window on the terminal-order guard.
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, false, fmt.Errorf("get order: %w", err)
	}
	accountID, err := n.accountDiagnosticID(ctx, detail.Order.Account)
	if err != nil {
		return domain.Order{}, false, err
	}
	var confirmed domain.Order
	var forcedBypass bool
	if err := eng.RunAccountSynchronized(ctx, detail.Order.Account, func(lane engine.AccountLane) error {
		detail, err := n.realm.GetOrder(ctx, order)
		if err != nil {
			return fmt.Errorf("get order: %w", err)
		}
		if err := domain.RequireOrderModifiable(detail.Order.Status, force); err != nil {
			return fmt.Errorf(
				"order %s is in terminal status %q: %w",
				order, detail.Order.Status, err)
		}
		forcedBypass = force && domain.OrderStatusTerminal(detail.Order.Status)
		if detail.Order.Status == domain.OrderStatusCommitted {
			confirmed = detail.Order
			return nil
		}

		if err := lane.CommitHeld(ctx, approvalID); err != nil {
			if !errors.Is(err, domain.ErrNotFound) {
				return fmt.Errorf("commit held: %w", err)
			}
			// The native handle is gone (post-restart): fall back to the persisted
			// intent. The fallback verifies the intent then drives the same atomic
			// resolve below.
			if err := n.commitHeldIntentFallback(ctx, approvalID); err != nil {
				return err
			}
		}
		// One atomic store transaction: intent flip + order status advance + the
		// reservation_committed event. Without force, AllowedFrom={accepted} is
		// TOCTOU-safe inside the same account lane as fills and reports. Force drops
		// the store guard because it means straight to the engine with no Officer
		// checks.
		allowedFrom := []domain.OrderStatus{domain.OrderStatusAccepted}
		if force {
			allowedFrom = nil
		}
		if err := resolveOrderReservationWithAttestation(ctx, n.realm, domain.ReservationResolution{
			ApprovalID:  approvalID,
			IntentState: domain.ReservationIntentStateCommitted,
			OrderStatus: domain.OrderStatusCommitted,
			AllowedFrom: allowedFrom,
			Events: []domain.OrderEvent{{
				Order:     order,
				Type:      domain.OrderEventReservationCommitted,
				Source:    caller.Source,
				Principal: caller.Principal,
			}},
			Order: order,
		}, attest); err != nil {
			wrapped := fmt.Errorf("resolve confirm: %w", err)
			return n.fatalPostEnginePersistence(
				"resolve held confirmation", accountID, wrapped,
			)
		}
		detail, err = n.realm.GetOrder(ctx, order)
		if err != nil {
			return fmt.Errorf("get order: %w", err)
		}
		confirmed = detail.Order
		return nil
	}); err != nil {
		return domain.Order{}, false, err
	}
	return confirmed, forcedBypass, nil
}

// CancelHeld rolls back the held reservation through the engine and records the
// cancelled lifecycle on the order. The backend audits the cancellation. The
// returned bool reports whether force actually bypassed a terminal-order guard.
func (n *localNode) CancelHeld(
	ctx context.Context, order domain.ExternalID,
	approvalID string, caller domain.Caller, force bool,
) (domain.Order, bool, error) {
	return n.cancelHeld(ctx, order, approvalID, caller, force, nil)
}

func (n *localNode) CancelHeldWithAttestation(
	ctx context.Context,
	order domain.ExternalID,
	approvalID string,
	caller domain.Caller,
	force bool,
	attest store.EventAttestor,
) (domain.Order, bool, error) {
	return n.cancelHeld(ctx, order, approvalID, caller, force, attest)
}

func (n *localNode) cancelHeld(
	ctx context.Context,
	order domain.ExternalID,
	approvalID string,
	caller domain.Caller,
	force bool,
	attest store.EventAttestor,
) (domain.Order, bool, error) {
	eng, done, err := n.beginLane()
	if err != nil {
		return domain.Order{}, false, err
	}
	defer done()

	// Outer fetch is for routing only; the inner fetch inside the lane re-reads
	// the authoritative status under serialization with fills and reports, closing
	// the TOCTOU window on the terminal-order guard.
	detail, err := n.realm.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, false, fmt.Errorf("get order: %w", err)
	}
	accountID, err := n.accountDiagnosticID(ctx, detail.Order.Account)
	if err != nil {
		return domain.Order{}, false, err
	}
	var cancelled domain.Order
	var forcedBypass bool
	if err := eng.RunAccountSynchronized(ctx, detail.Order.Account, func(lane engine.AccountLane) error {
		detail, err := n.realm.GetOrder(ctx, order)
		if err != nil {
			return fmt.Errorf("get order: %w", err)
		}
		if err := domain.RequireOrderModifiable(detail.Order.Status, force); err != nil {
			return fmt.Errorf(
				"order %s is in terminal status %q: %w",
				order, detail.Order.Status, err)
		}
		forcedBypass = force && domain.OrderStatusTerminal(detail.Order.Status)

		if err := lane.RollbackHeld(ctx, approvalID); err != nil {
			if !errors.Is(err, domain.ErrNotFound) {
				return fmt.Errorf("rollback held: %w", err)
			}
			// Native handle gone (post-restart): release the held balance effects
			// through the engine from the persisted intent. The intent flip + order
			// status + events are still done by the atomic resolve below.
			if err := n.rollbackHeldIntentFallback(ctx, lane, approvalID, accountID); err != nil {
				return err
			}
		}
		// One atomic store transaction: intent flip + cancelled status +
		// reservation_rolled_back and cancelled events. Without force,
		// AllowedFrom={accepted} is TOCTOU-safe inside the same account lane as fills
		// and reports. Force drops the store guard because it means straight to the
		// engine with no Officer checks.
		allowedFrom := []domain.OrderStatus{domain.OrderStatusAccepted}
		if force {
			allowedFrom = nil
		}
		if err := resolveOrderReservationWithAttestation(ctx, n.realm, domain.ReservationResolution{
			ApprovalID:  approvalID,
			IntentState: domain.ReservationIntentStateRolledBack,
			OrderStatus: domain.OrderStatusCancelled,
			AllowedFrom: allowedFrom,
			Events: []domain.OrderEvent{
				{
					Order:     order,
					Type:      domain.OrderEventReservationRolledBack,
					Source:    caller.Source,
					Principal: caller.Principal,
				},
				{
					Order:     order,
					Type:      domain.OrderEventCancelled,
					Source:    caller.Source,
					Principal: caller.Principal,
				},
			},
			Order: order,
		}, attest); err != nil {
			wrapped := fmt.Errorf("resolve cancel: %w", err)
			return n.fatalPostEnginePersistence(
				"resolve held cancellation", accountID, wrapped,
			)
		}
		detail, err = n.realm.GetOrder(ctx, order)
		if err != nil {
			return fmt.Errorf("get order: %w", err)
		}
		cancelled = detail.Order
		return nil
	}); err != nil {
		return domain.Order{}, false, err
	}
	return cancelled, forcedBypass, nil
}

// ReconcileOrphans reports persisted held reservation intents after boot. It
// runs once after the engine is built; held balance effects remain durable in
// the store and are not rolled back here.
func (n *localNode) ReconcileOrphans(ctx context.Context) (int, error) {
	eng, done, err := n.beginLane()
	if err != nil {
		return 0, err
	}
	defer done()
	count, err := eng.ReconcileOrphans(ctx)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// commitHeldIntentFallback handles a confirm whose native handle is gone (post
// restart): it verifies the persisted held intent still exists. The intent flip
// itself is left to the caller's atomic ResolveOrderReservation, so the fallback
// confirm has the same all-or-nothing guarantee and status guard as the normal
// path.
//
// If the intent was already resolved before the restart, openReservationIntent
// returns ErrNotFound. GetReservationIntent is then used to distinguish an
// already-resolved (terminal) intent from one that never existed: the former
// returns ErrConflict (409) and the latter keeps ErrNotFound (404).
func (n *localNode) commitHeldIntentFallback(ctx context.Context, approvalID string) error {
	if _, err := n.openReservationIntent(ctx, approvalID); err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("commit held after engine restart: %w", err)
		}
		// Not in the open set - check whether it exists in a terminal state.
		swept, found, lookupErr := n.realm.GetReservationIntent(ctx, approvalID)
		if lookupErr != nil {
			return fmt.Errorf("commit held after engine restart: %w", lookupErr)
		}
		if found && isTerminalIntentState(swept.State) {
			return fmt.Errorf("confirm reservation %q: %w", approvalID, domain.ErrConflict)
		}
		return fmt.Errorf("commit held after engine restart: %w", err)
	}
	return nil
}

func (n *localNode) rollbackHeldIntentFallback(
	ctx context.Context, lane engine.AccountLane, approvalID string, accountID string,
) error {
	intent, err := n.openReservationIntent(ctx, approvalID)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("rollback held after engine restart: %w", err)
		}
		// Not in the open set - check whether it exists in a terminal state.
		swept, found, lookupErr := n.realm.GetReservationIntent(ctx, approvalID)
		if lookupErr != nil {
			return fmt.Errorf("rollback held after engine restart: %w", lookupErr)
		}
		if found && isTerminalIntentState(swept.State) {
			return fmt.Errorf("cancel reservation %q: %w", approvalID, domain.ErrConflict)
		}
		return fmt.Errorf("rollback held after engine restart: %w", err)
	}
	_, outcomes, err := decodeReservationIntentPayload(intent.ParamsJSON)
	if err != nil {
		return fmt.Errorf("decode held reservation payload: %w", err)
	}
	if len(outcomes) == 0 {
		return fmt.Errorf(
			"held reservation %q has no persisted balance outcomes to release: %w",
			approvalID, domain.ErrConflict)
	}
	key := Key{Account: intent.Account}
	for _, outcome := range outcomes {
		if err := n.releaseHeldBalance(ctx, lane, key, accountID, outcome); err != nil {
			return err
		}
	}
	// The intent flip and order status/events are left to the caller's atomic
	// ResolveOrderReservation. The balance release above stays outside that tx: it
	// is an engine adjustment with its own outcome and its own persistence, and it
	// is idempotent once the native handle is gone.
	return nil
}

// isTerminalIntentState reports whether s is a terminal reservation state
// (committed or rolled_back).
func isTerminalIntentState(s domain.ReservationIntentState) bool {
	return s == domain.ReservationIntentStateCommitted ||
		s == domain.ReservationIntentStateRolledBack
}

func (n *localNode) openReservationIntent(
	ctx context.Context, approvalID string,
) (domain.ReservationIntent, error) {
	intents, err := n.realm.ListOpenReservationIntents(ctx)
	if err != nil {
		return domain.ReservationIntent{}, fmt.Errorf("list open reservation intents: %w", err)
	}
	for _, intent := range intents {
		if intent.ApprovalID == approvalID {
			return intent, nil
		}
	}
	return domain.ReservationIntent{}, fmt.Errorf(
		"reservation %q: %w", approvalID, domain.ErrNotFound)
}

type reservationIntentPayload struct {
	Order    domain.Order            `json:"order"`
	Outcomes []engine.BalanceOutcome `json:"outcomes,omitempty"`
}

func decodeReservationIntentPayload(raw string) (
	domain.Order, []engine.BalanceOutcome, error,
) {
	var payload reservationIntentPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return domain.Order{}, nil, fmt.Errorf(
			"malformed reservation intent payload: %w", domain.ErrInvalid)
	}
	if len(payload.Outcomes) == 0 || payload.Order.ExternalID.IsZero() ||
		payload.Order.Account == "" {
		return domain.Order{}, nil, fmt.Errorf(
			"malformed reservation intent payload: %w", domain.ErrInvalid)
	}
	return payload.Order, payload.Outcomes, nil
}

func (n *localNode) releaseHeldBalance(
	ctx context.Context,
	lane engine.AccountLane,
	key Key,
	accountID string,
	held engine.BalanceOutcome,
) error {
	req, err := releaseHeldRequest(held)
	if err != nil {
		return err
	}
	if req.Balance == nil && req.Held == nil && req.Incoming == nil {
		return nil
	}
	result, err := lane.ApplyAccountAdjustment(ctx, key.Account, req)
	if err != nil {
		return fmt.Errorf("release held through engine: %w", err)
	}
	if result.Rejected != nil {
		return fmt.Errorf(
			"release held rejected by engine: %s: %w",
			result.Rejected.Reason, domain.ErrConflict)
	}
	if result.Accepted == nil {
		return fmt.Errorf("release held produced no accepted outcome: %w", domain.ErrConflict)
	}
	if err := n.persistAdjustedBalance(ctx, key, req, *result.Accepted); err != nil {
		return n.fatalPostEnginePersistence("release held balance", accountID, err)
	}
	return nil
}

func releaseHeldRequest(held engine.BalanceOutcome) (domain.AdjustmentRequest, error) {
	req := domain.AdjustmentRequest{Asset: held.Asset}
	var err error
	if req.Balance, err = inverseAdjustmentAmount(held.Outcome.BalanceDelta); err != nil {
		return domain.AdjustmentRequest{}, fmt.Errorf("release held available: %w", err)
	}
	if req.Held, err = inverseAdjustmentAmount(held.Outcome.HeldDelta); err != nil {
		return domain.AdjustmentRequest{}, fmt.Errorf("release held amount: %w", err)
	}
	if req.Incoming, err = inverseAdjustmentAmount(held.Outcome.IncomingDelta); err != nil {
		return domain.AdjustmentRequest{}, fmt.Errorf("release held incoming: %w", err)
	}
	return req, nil
}

func inverseAdjustmentAmount(delta string) (*domain.AdjustmentAmount, error) {
	if delta == "" {
		return nil, nil
	}
	parsed, err := decimal.NewFromString(delta)
	if err != nil {
		return nil, fmt.Errorf("delta %q is not a valid decimal: %w", delta, domain.ErrInvalid)
	}
	return &domain.AdjustmentAmount{
		Mode:  domain.AdjustmentModeDelta,
		Value: parsed.Neg().String(),
	}, nil
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

func holdAcceptedSettlement(
	key Key, order domain.Order, result engine.HoldResult, caller domain.Caller,
) domain.OrderSettlement {
	// Persist the held reservation intent in the same transaction as the order so
	// the two are atomic. The engine registered the hold in-memory and handed us
	// the durable record; writing it here (on the order tx) avoids the nested
	// pooled-connection write that would self-deadlock under SetMaxOpenConns(1).
	intent := result.Intent
	return domain.OrderSettlement{
		Account:     key.Account,
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusAccepted,
		Balances:    balanceSettlementsFrom(result.Outcomes),
		Events: []domain.OrderEvent{
			fillSettlementEvent(order.ExternalID, domain.OrderEventPreTradeAccepted, caller, domain.OrderEventPayload{}),
		},
		Lock:                    result.Lock,
		SetLock:                 true,
		ReservationIntentUpsert: &intent,
	}
}

func immediateAcceptedSettlement(
	key Key, order domain.Order, result engine.ImmediateResult, caller domain.Caller,
) domain.OrderSettlement {
	fillPayload := accountBlockPayload(result.Blocks)
	fillPayload.FillQuantity = result.FillQuantity
	fillPayload.FillPrice = result.SettlementLockPrice
	fillPayload.FillLockPrice = result.SettlementLockPrice
	return domain.OrderSettlement{
		Account:     key.Account,
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusFilled,
		AllowedFrom: domain.OrderStatusesEligibleForFill(),
		Balances:    balanceSettlementsFrom(result.Outcomes),
		Events: []domain.OrderEvent{
			fillSettlementEvent(order.ExternalID, domain.OrderEventPreTradeAccepted, caller, domain.OrderEventPayload{}),
			fillSettlementEvent(order.ExternalID, domain.OrderEventReservationCommitted, caller, domain.OrderEventPayload{}),
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
// rebuilds the engine, so the caller decides when the rebuild is safe (pre-lane
// for account-scoped operations).
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
