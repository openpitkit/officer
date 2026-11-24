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

package backend

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
)

// ReviewView presents the recorded review of an order awaiting a decision.
// Marks are decoded for display only; decision authorization verifies the
// envelope.
type ReviewView struct {
	Held        bool
	Marks       []domain.ReviewMark
	LastConfirm *ReviewConfirmView
	Destination *domain.TradingDestination
}

// ReviewConfirmView presents the latest rejected review confirmation.
type ReviewConfirmView struct {
	Verdict domain.Verdict
	Reasons []domain.OrderReject
	At      time.Time
}

// OrderReviewView derives review presentation in the history order of GetOrder.
// It returns nil for orders not awaiting review and an internal error when a
// waiting order lacks its review row, unique review event or a decodable
// attestation with a review verdict and nonempty marks.
// Confirm rejects are causally after the unique review event even when a clock
// step back places them before it in history. Among rejects, GetOrder's history
// order (the store's (at, id) order) decides which is last.
// It does not verify the envelope and must not be used to authorize a decision.
func OrderReviewView(detail domain.OrderDetail) (*ReviewView, error) {
	if detail.Order.Status != domain.OrderStatusAwaitingReview {
		return nil, nil
	}
	if detail.Review == nil {
		return nil, fmt.Errorf("backend: awaiting-review order %s has no review row", detail.Order.ExternalID)
	}
	var review *domain.OrderEvent
	for i := range detail.Events {
		event := &detail.Events[i]
		if event.Type == domain.OrderEventPreTradeReview {
			if review != nil {
				return nil, fmt.Errorf("backend: awaiting-review order %s has multiple pre-trade review events", detail.Order.ExternalID)
			}
			review = event
		}
	}
	if review == nil {
		return nil, fmt.Errorf("backend: awaiting-review order %s has no pre-trade review event", detail.Order.ExternalID)
	}
	if review.Attestation == nil || review.Attestation.SignedEnvelope == "" {
		return nil, fmt.Errorf("backend: awaiting-review order %s has no review attestation", detail.Order.ExternalID)
	}
	env, err := fwsigning.DecodeEnvelope(review.Attestation.SignedEnvelope)
	if err != nil {
		return nil, fmt.Errorf("backend: awaiting-review order %s review envelope could not be decoded: %v", detail.Order.ExternalID, err)
	}
	if env.Approval.Verdict != domain.VerdictReview {
		return nil, fmt.Errorf("backend: awaiting-review order %s review envelope has verdict %q", detail.Order.ExternalID, env.Approval.Verdict)
	}
	if len(env.Approval.Reviews) == 0 {
		return nil, fmt.Errorf("backend: awaiting-review order %s review envelope has no review marks", detail.Order.ExternalID)
	}
	view := &ReviewView{Held: detail.Review.Held, Marks: env.Approval.Reviews, Destination: detail.Review.Destination}
	for _, event := range detail.Events {
		if event.Type != domain.OrderEventPreTradeRejected ||
			event.Attestation == nil || event.Attestation.RequestType != domain.AttestationRequestReviewConfirm {
			continue
		}
		view.LastConfirm = &ReviewConfirmView{
			Verdict: domain.VerdictReject, Reasons: event.Payload.Rejects, At: event.At,
		}
	}
	return view, nil
}

// ConfirmOrderReview re-runs pre-trade for an order awaiting operator review.
// It refuses MCP callers with ErrForbidden, orders not awaiting review with
// ErrConflict, and missing, unverifiable or inconsistent review approvals with
// ErrApprovalRequired. Trading orders require a runtime (ErrNotImplemented),
// their stored binding and venue account (ErrForbidden), and a successful venue
// pre-check (ErrInvalid for a venue refusal, ErrUpstream for an upstream
// failure).
// Other domain errors from the venue pre-check, including ErrConflict and
// ErrNotFound, are returned unchanged.
// It returns the order as the node recorded the decision and its signed (or
// eSign-off) decision. Send carries the send outcome only for a trading order
// accepted by the re-run; when Send.Status is "refused", the order has since
// been cancelled.
func (s *Service) ConfirmOrderReview(
	ctx context.Context, orderID string,
) (order domain.Order, result VenueSendResult, err error) {
	return s.decideOrderReview(ctx, orderID, false)
}

// ForceOrderReview commits or immediately fills a held waiting order without a
// pre-trade call, then sends a trading order to its stored venue destination.
// It refuses MCP callers with ErrForbidden, non-waiting or non-held orders with
// ErrConflict, and missing, unverifiable or inconsistent review approvals with
// ErrApprovalRequired. Trading orders require a runtime (ErrNotImplemented),
// their stored binding and venue account (ErrForbidden), and a successful venue
// pre-check (ErrInvalid for venue refusal, ErrUpstream for upstream failure).
// Other domain errors from the venue pre-check are returned unchanged.
// The node refuses an account blocked by its own flag or group, a changed hold,
// or a mode mismatch with ErrConflict inside the account lane before any write;
// changed chain input returns ErrInvalid. Node errors pass through unchanged
// and prevent sending. Missing stored settlement data, an unknown mode, an
// unexpected verdict or a missing decision attestation is an explicit error.
// It returns the recorded order and its signed (or eSign-off) accept decision.
// Send carries the trading outcome; "refused" means the order was cancelled.
func (s *Service) ForceOrderReview(
	ctx context.Context, orderID string,
) (domain.Order, VenueSendResult, error) {
	return s.decideOrderReview(ctx, orderID, true)
}

func (s *Service) decideOrderReview(
	ctx context.Context, orderID string, force bool,
) (order domain.Order, result VenueSendResult, err error) {
	ctx, finish := node.PostCommitNotifications(ctx)
	var redact *strings.Replacer
	defer func() {
		err = redactTradingSend(redact, result.Send, err)
	}()
	defer func() {
		result.Approval.AlertFailure, err = finishOrderReviewNotifications(err, finish())
	}()
	action, operation := "confirmed", "confirm"
	requestType := domain.AttestationRequestReviewConfirm
	decideHold := s.node.ConfirmOrderReview
	decideImmediate := s.node.ConfirmImmediateReview
	if force {
		action, operation = "forced", "force"
		requestType = domain.AttestationRequestReviewForce
		decideHold = s.node.ForceOrderReview
		decideImmediate = s.node.ForceImmediateReview
	}
	stored, review, signer, caller, err := s.orderReviewForDecision(ctx, orderID, action)
	if err != nil {
		return domain.Order{}, VenueSendResult{}, err
	}
	if force && !stored.Review.Held {
		return domain.Order{}, VenueSendResult{}, fmt.Errorf("backend: order review is not held: %w", domain.ErrConflict)
	}
	off, keyID, err := signingMode(ctx, signer)
	if err != nil {
		return domain.Order{}, VenueSendResult{}, err
	}
	destination := stored.Review.Destination
	if destination != nil {
		if err := s.requireTradingRuntime(); err != nil {
			return domain.Order{}, VenueSendResult{}, err
		}
		var venueAccount string
		_, venueAccount, redact, err = s.resolveTradingConnection(ctx, stored.Order.Account, destination.Connection)
		if err != nil {
			return domain.Order{}, VenueSendResult{}, err
		}
		if venueAccount != destination.VenueAccount {
			return domain.Order{}, VenueSendResult{}, fmt.Errorf("trading venue account differs from the binding: %w", domain.ErrForbidden)
		}
		if err := s.preCheckTradingOrder(ctx, stored.Order, *destination); err != nil {
			return domain.Order{}, VenueSendResult{}, err
		}
	}
	approvalID, err := newNonce()
	if err != nil {
		return domain.Order{}, VenueSendResult{}, err
	}
	var issued Attestation
	var payload domain.ApprovalPayload
	attestFor := func(persisted domain.Order, decision decisionPayloadInputs) store.EventAttestor {
		// Decision payloads use the submitted draft's leaves, not the waiting
		// order's stored leaves.
		persisted.Leaves = ""
		decision.mode = stored.Review.Mode
		decision.approvalID = approvalID
		decision.approvalRef = review.ApprovalID
		return eventAttestor(
			signer, off, keyID, requestType,
			func(event domain.OrderEvent) (domain.ApprovalPayload, bool, error) {
				eventOrder := orderForEvent(persisted, stored.Order.Account, event, caller)
				return s.buildDecisionEventPayload(eventOrder, event, decision)
			},
			captureDecisionEvent(&issued, &payload),
		)
	}
	var verdict domain.Verdict
	var rejects []domain.OrderReject
	switch stored.Review.Mode {
	case domain.SubmitModeHold:
		var decision engine.OrderResult
		order, decision, err = decideHold(ctx, stored.Order.ExternalID, caller,
			func(persisted domain.Order, submitted engine.OrderResult) store.EventAttestor {
				return attestFor(persisted, decisionPayloadInputs{
					verdict: submitted.Verdict, rejects: submitted.Rejects, reviews: submitted.Reviews,
					settlementLockPrice: submitted.SettlementLockPrice,
				})
			})
		verdict, rejects = decision.Verdict, decision.Rejects
	case domain.SubmitModeImmediate:
		var decision engine.ImmediateResult
		order, decision, err = decideImmediate(ctx, stored.Order.ExternalID, caller,
			func(persisted domain.Order, immediate engine.ImmediateResult) store.EventAttestor {
				return attestFor(persisted, decisionPayloadInputs{
					verdict: immediate.Verdict, rejects: immediate.Rejects, reviews: immediate.Reviews,
					settlementLockPrice: immediate.SettlementLockPrice, persistence: immediate.Persistence,
				})
			})
		verdict, rejects = decision.Verdict, decision.Rejects
	default:
		return domain.Order{}, VenueSendResult{}, fmt.Errorf("backend: order review mode %q is invalid", stored.Review.Mode)
	}
	if err != nil {
		return domain.Order{}, VenueSendResult{}, err
	}
	if verdict != domain.VerdictAccept && (force || verdict != domain.VerdictReject) {
		return domain.Order{}, VenueSendResult{}, fmt.Errorf("backend: %s order review verdict %q is invalid", operation, verdict)
	}
	if issued.SignedAttestation == "" {
		return domain.Order{}, VenueSendResult{}, missingAttestationError(requestType)
	}
	result.Approval = SignedApproval{
		SignedApproval: issued.SignedAttestation, KeyID: issued.KeyID,
		OrderExternalID: order.ExternalID.String(), Verdict: verdict,
		Reasons: rejectReasons(payload, rejects), Signed: issued.Signed,
	}
	if destination != nil && verdict == domain.VerdictAccept {
		result.Send = s.sendTradingOrder(ctx, order.ExternalID, *destination, redact)
	}
	return order, result, nil
}

// CancelOrderReview cancels an order awaiting operator review. A held review
// uses a cancel execution report to release the hold through the engine; a
// review without hold makes no engine call. It never calls the trading venue.
// It refuses MCP callers with ErrForbidden and orders not awaiting review with
// ErrConflict. Missing, unverifiable or inconsistent review approvals return
// ErrApprovalRequired. It returns the cancelled order with zero leaves and the
// signed (or eSign-off) review_cancel attestation.
func (s *Service) CancelOrderReview(
	ctx context.Context, orderID string,
) (resultOrder domain.Order, resultAtt Attestation, alertFailure *AlertFailure, resultErr error) {
	ctx, finish := node.PostCommitNotifications(ctx)
	defer func() {
		alertFailure, resultErr = finishOrderReviewNotifications(resultErr, finish())
	}()
	stored, review, signer, caller, err := s.orderReviewForDecision(ctx, orderID, "cancelled")
	if err != nil {
		return domain.Order{}, Attestation{}, nil, err
	}
	off, keyID, err := signingMode(ctx, signer)
	if err != nil {
		return domain.Order{}, Attestation{}, nil, err
	}
	var att Attestation
	attest := eventAttestor(
		signer, off, keyID, domain.AttestationRequestReviewCancel,
		func(event domain.OrderEvent) (domain.ApprovalPayload, bool, error) {
			if event.Type != domain.OrderEventCancelled {
				return domain.ApprovalPayload{}, false, nil
			}
			cancelled := orderForEvent(stored.Order, stored.Order.Account, event, caller)
			cancelled.Status, cancelled.Leaves = domain.OrderStatusCancelled, "0"
			payload, err := s.buildLifecyclePayload(
				cancelled, stored.Review.Mode, domain.AttestationRequestReviewCancel,
				"cancelled", "", review.ApprovalID,
			)
			return payload, err == nil, err
		},
		func(got Attestation, _ domain.ApprovalPayload) { att = got },
	)
	order, err := s.node.CancelOrderReview(ctx, stored.Order.ExternalID, caller, attest)
	if err != nil {
		return domain.Order{}, Attestation{}, nil, err
	}
	if att.SignedAttestation == "" {
		return domain.Order{}, Attestation{}, nil, missingAttestationError(domain.AttestationRequestReviewCancel)
	}
	return order, att, nil, nil
}

func (s *Service) orderReviewForDecision(
	ctx context.Context, orderID, action string,
) (domain.OrderDetail, domain.ApprovalPayload, fwsigning.Service, domain.Caller, error) {
	id, err := domain.ParseExternalID(orderID)
	if err != nil {
		return domain.OrderDetail{}, domain.ApprovalPayload{}, nil, domain.Caller{}, err
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return domain.OrderDetail{}, domain.ApprovalPayload{}, nil, caller, err
	}
	if caller.TightenOnly() {
		return domain.OrderDetail{}, domain.ApprovalPayload{}, nil, caller, fmt.Errorf("backend: MCP caller cannot decide an order review: %w", domain.ErrForbidden)
	}
	stored, err := s.node.GetOrder(ctx, id)
	if err != nil {
		return domain.OrderDetail{}, domain.ApprovalPayload{}, nil, caller, err
	}
	if err := s.scope.Visible(caller, stored.Order.Account); err != nil {
		return domain.OrderDetail{}, domain.ApprovalPayload{}, nil, caller, err
	}
	if err := domain.RequireOrderAwaitingReview(stored.Order.Status); err != nil {
		return domain.OrderDetail{}, domain.ApprovalPayload{}, nil, caller, err
	}
	if stored.Review == nil {
		return domain.OrderDetail{}, domain.ApprovalPayload{}, nil, caller, fmt.Errorf("backend: awaiting-review order %s has no review row", id)
	}
	if stored.Review.Destination != nil && stored.Review.Mode != domain.SubmitModeHold {
		return domain.OrderDetail{}, domain.ApprovalPayload{}, nil, caller, fmt.Errorf("backend: trading order %s has review mode %q instead of hold", id, stored.Review.Mode)
	}
	payload, signer, err := s.verifyReviewApproval(ctx, stored, action)
	if err != nil {
		return domain.OrderDetail{}, domain.ApprovalPayload{}, nil, caller, err
	}
	if payload.Mode != stored.Review.Mode {
		return domain.OrderDetail{}, domain.ApprovalPayload{}, nil, caller, fmt.Errorf("backend: recorded approval %s mode %q differs from review mode %q and cannot be %s: %w", payload.ApprovalID, payload.Mode, stored.Review.Mode, action, domain.ErrApprovalRequired)
	}
	held, err := domain.ReviewMarksHeld(payload.Reviews)
	if err != nil {
		return domain.OrderDetail{}, domain.ApprovalPayload{}, nil, caller, fmt.Errorf("backend: recorded approval %s has invalid review marks for review held %t and cannot be %s: %v: %w", payload.ApprovalID, stored.Review.Held, action, err, domain.ErrApprovalRequired)
	}
	if held != stored.Review.Held {
		return domain.OrderDetail{}, domain.ApprovalPayload{}, nil, caller, fmt.Errorf("backend: recorded approval %s held %t differs from review held %t and cannot be %s: %w", payload.ApprovalID, held, stored.Review.Held, action, domain.ErrApprovalRequired)
	}
	return stored, payload, signer, caller, nil
}

func (s *Service) verifyReviewApproval(
	ctx context.Context, stored domain.OrderDetail, action string,
) (domain.ApprovalPayload, fwsigning.Service, error) {
	payload, signer, err := s.verifyRecordedApproval(ctx, stored, domain.OrderEventPreTradeReview, "pre-trade review", action)
	if err != nil {
		return domain.ApprovalPayload{}, nil, err
	}
	if payload.Verdict != domain.VerdictReview {
		return domain.ApprovalPayload{}, nil, fmt.Errorf("backend: recorded approval %s verdict %q cannot be %s: %w", payload.ApprovalID, payload.Verdict, action, domain.ErrApprovalRequired)
	}
	if !isOriginalSubmitVerdict(payload) {
		return domain.ApprovalPayload{}, nil, fmt.Errorf("backend: recorded approval %s is not an original submit verdict and cannot be %s: %w", payload.ApprovalID, action, domain.ErrApprovalRequired)
	}
	if payload.Mode != domain.SubmitModeHold && payload.Mode != domain.SubmitModeImmediate {
		return domain.ApprovalPayload{}, nil, fmt.Errorf("backend: recorded approval %s mode %q cannot be %s: %w", payload.ApprovalID, payload.Mode, action, domain.ErrApprovalRequired)
	}
	return payload, signer, nil
}
