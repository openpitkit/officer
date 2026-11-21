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
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
)

func reviewViewDetail(t *testing.T) domain.OrderDetail {
	t.Helper()
	token, err := fwsigning.BuildEnvelope(fwsigning.Envelope{Approval: domain.ApprovalPayload{
		Verdict: domain.VerdictReview,
		Reviews: []domain.ReviewMark{{
			OrderReject: domain.OrderReject{Code: "size", Scope: "order", Policy: "size", Reason: "review size", Details: "limit"},
			Mode:        domain.RuleMode("review_without_hold"),
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return domain.OrderDetail{
		Order:  domain.Order{ExternalID: "review-order", Status: domain.OrderStatusAwaitingReview},
		Review: &domain.OrderReview{Mode: domain.SubmitModeHold},
		Events: []domain.OrderEvent{{
			At: time.Unix(100, 0).UTC(), Type: domain.OrderEventPreTradeReview,
			Attestation: &domain.EventAttestation{SignedEnvelope: token},
			// Marks belong to the envelope, not the event payload.
			Payload: domain.OrderEventPayload{Rejects: []domain.OrderReject{{Code: "not-a-mark"}}},
		}},
	}
}

func TestOrderReviewView(t *testing.T) {
	for _, trading := range []bool{false, true} {
		t.Run(fmt.Sprintf("trading=%t", trading), func(t *testing.T) {
			detail := reviewViewDetail(t)
			if trading {
				detail.Review.Destination = &domain.TradingDestination{
					Connection: "paper", VenueAccount: "desk", Route: `{"timeInForce":"day"}`,
				}
			}
			view, err := OrderReviewView(detail)
			if err != nil || view == nil {
				t.Fatalf("view=%+v err=%v", view, err)
			}
			if len(view.Marks) != 1 || view.Marks[0].Code != "size" || view.Marks[0].Mode != "review_without_hold" || view.LastConfirm != nil ||
				!reflect.DeepEqual(view.Destination, detail.Review.Destination) {
				t.Fatalf("review presentation=%+v", view)
			}
		})
	}
}

func TestOrderReviewViewLastConfirmRequestType(t *testing.T) {
	detail := reviewViewDetail(t)
	rejected := func(at int64, request domain.AttestationRequestType, code string) domain.OrderEvent {
		return domain.OrderEvent{
			At: time.Unix(at, 0).UTC(), Type: domain.OrderEventPreTradeRejected,
			Attestation: &domain.EventAttestation{RequestType: request},
			Payload:     domain.OrderEventPayload{Rejects: []domain.OrderReject{{Code: code, Reason: code}}},
		}
	}
	before := rejected(99, domain.AttestationRequestReviewConfirm, "before-review")
	first := rejected(101, domain.AttestationRequestReviewConfirm, "first")
	last := rejected(102, domain.AttestationRequestReviewConfirm, "last")
	wrongRequest := rejected(103, domain.AttestationRequestSubmit, "wrong-request")
	unattested := rejected(104, domain.AttestationRequestReviewConfirm, "unattested")
	unattested.Attestation = nil
	wrongEvent := rejected(105, domain.AttestationRequestReviewConfirm, "wrong-event")
	wrongEvent.Type = domain.OrderEventPreTradeAccepted
	for _, tc := range []struct {
		name   string
		events []domain.OrderEvent
		want   *domain.OrderEvent
	}{
		{"no-confirm", []domain.OrderEvent{detail.Events[0], wrongRequest, unattested, wrongEvent}, nil},
		{"confirm-before-review-in-history", []domain.OrderEvent{before, detail.Events[0]}, &before},
		{"latest-confirm", []domain.OrderEvent{before, detail.Events[0], first, wrongRequest, last, unattested, wrongEvent}, &last},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := detail
			d.Events = tc.events
			view, err := OrderReviewView(d)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil {
				if view.LastConfirm != nil {
					t.Fatalf("lastConfirm=%+v; want nil for unrelated rejection", view.LastConfirm)
				}
				return
			}
			want := &ReviewConfirmView{Verdict: domain.VerdictReject, Reasons: tc.want.Payload.Rejects, At: tc.want.At}
			if !reflect.DeepEqual(view.LastConfirm, want) {
				t.Fatalf("lastConfirm=%+v; want %+v", view.LastConfirm, want)
			}
		})
	}
}

func TestOrderReviewViewHistoryOrder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		times []int64
	}{
		{"equal-timestamps", []int64{100}},
		{"clock-step-back", []int64{99}},
		{"last-in-history", []int64{101, 102}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := reviewViewDetail(t)
			for i, at := range tc.times {
				detail.Events = append(detail.Events, domain.OrderEvent{
					At: time.Unix(at, 0).UTC(), Type: domain.OrderEventPreTradeRejected,
					Attestation: &domain.EventAttestation{RequestType: domain.AttestationRequestReviewConfirm},
					Payload: domain.OrderEventPayload{Rejects: []domain.OrderReject{{
						Code: fmt.Sprintf("reject-%d", i),
					}}},
				})
			}
			last := detail.Events[len(detail.Events)-1]
			if tc.name == "clock-step-back" {
				detail.Events[0], detail.Events[1] = detail.Events[1], detail.Events[0]
			}
			view, err := OrderReviewView(detail)
			if err != nil {
				t.Fatal(err)
			}
			want := &ReviewConfirmView{Verdict: domain.VerdictReject, Reasons: last.Payload.Rejects, At: last.At}
			if !reflect.DeepEqual(view.LastConfirm, want) {
				t.Fatalf("lastConfirm=%+v; want last rejection in history %+v", view.LastConfirm, want)
			}
		})
	}
}

func TestOrderReviewViewInvalidApproval(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict domain.Verdict
		reviews []domain.ReviewMark
		message string
	}{
		{"accept", domain.VerdictAccept, []domain.ReviewMark{{OrderReject: domain.OrderReject{Code: "unexpected"}}}, "verdict"},
		{"reject", domain.VerdictReject, []domain.ReviewMark{{OrderReject: domain.OrderReject{Code: "unexpected"}}}, "verdict"},
		{"empty-marks", domain.VerdictReview, nil, "no review marks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := reviewViewDetail(t)
			token, err := fwsigning.BuildEnvelope(fwsigning.Envelope{Approval: domain.ApprovalPayload{
				Verdict: tc.verdict, Reviews: tc.reviews,
			}})
			if err != nil {
				t.Fatal(err)
			}
			detail.Events[0].Attestation.SignedEnvelope = token
			view, err := OrderReviewView(detail)
			if view != nil || err == nil || !strings.Contains(err.Error(), tc.message) || !strings.Contains(err.Error(), "review-order") || errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("view=%+v err=%v; want plain internal error containing %q and order id", view, err, tc.message)
			}
		})
	}
}

func TestOrderReviewViewMissingHistory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*domain.OrderDetail)
		message string
	}{
		{"row", func(d *domain.OrderDetail) { d.Review = nil }, "no review row"},
		{"event", func(d *domain.OrderDetail) { d.Events = nil }, "no pre-trade review event"},
		{"duplicate-review", func(d *domain.OrderDetail) { d.Events = append(d.Events, d.Events[0]) }, "multiple pre-trade review events"},
		{"attestation", func(d *domain.OrderDetail) { d.Events[0].Attestation = nil }, "no review attestation"},
		{"empty-envelope", func(d *domain.OrderDetail) { d.Events[0].Attestation.SignedEnvelope = "" }, "no review attestation"},
		{"envelope", func(d *domain.OrderDetail) { d.Events[0].Attestation.SignedEnvelope = "!bad" }, "could not be decoded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := reviewViewDetail(t)
			tc.change(&detail)
			view, err := OrderReviewView(detail)
			if view != nil || err == nil || !strings.Contains(err.Error(), tc.message) || !strings.Contains(err.Error(), "review-order") || errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("view=%+v err=%v; want plain internal error containing %q and order id", view, err, tc.message)
			}
		})
	}
	view, err := OrderReviewView(domain.OrderDetail{Order: domain.Order{Status: domain.OrderStatusCommitted}})
	if view != nil || err != nil {
		t.Fatalf("non-waiting view=%+v err=%v", view, err)
	}
}

func (n *paperSendTestNode) ConfirmOrderReview(
	ctx context.Context, id domain.ExternalID, _ domain.Caller,
	attestFor func(domain.Order, engine.OrderResult) store.EventAttestor,
) (domain.Order, engine.OrderResult, error) {
	return n.decideReview(ctx, id, attestFor, false)
}

func (n *paperSendTestNode) ForceOrderReview(
	ctx context.Context, id domain.ExternalID, _ domain.Caller,
	attestFor func(domain.Order, engine.OrderResult) store.EventAttestor,
) (domain.Order, engine.OrderResult, error) {
	return n.decideReview(ctx, id, attestFor, true)
}

func (n *paperSendTestNode) decideReview(
	ctx context.Context, id domain.ExternalID,
	attestFor func(domain.Order, engine.OrderResult) store.EventAttestor, force bool,
) (domain.Order, engine.OrderResult, error) {
	if force {
		n.reviewForces++
		n.events = append(n.events, "force")
	} else {
		n.reviewConfirms++
		n.events = append(n.events, "decision")
	}
	if n.order == nil || n.order.ExternalID != id {
		return domain.Order{}, engine.OrderResult{}, domain.ErrNotFound
	}
	if err := domain.RequireOrderAwaitingReview(n.order.Status); err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	if force && (n.review == nil || !n.review.Held || n.review.Mode != domain.SubmitModeHold) {
		return domain.Order{}, engine.OrderResult{}, domain.ErrConflict
	}
	if n.submitErr != nil {
		return domain.Order{}, engine.OrderResult{}, n.submitErr
	}
	if force && n.forceVerdict != "" {
		return *n.order, engine.OrderResult{Verdict: n.forceVerdict}, nil
	}
	order := *n.order
	result := engine.OrderResult{Verdict: domain.VerdictAccept, SettlementLockPrice: "123.45"}
	if force {
		result.Lock = n.order.Lock
		result.SettlementLockPrice = string(n.order.Lock)
	}
	kinds := []domain.OrderEventType{domain.OrderEventPreTradeAccepted, domain.OrderEventCommitted}
	if n.accepted || force {
		order.Status, order.Leaves = domain.OrderStatusCommitted, order.AmountValue
	} else {
		result.Verdict = domain.VerdictReject
		result.Rejects = []domain.OrderReject{{Code: "order_size_limit", Reason: "too large"}}
		kinds = []domain.OrderEventType{domain.OrderEventPreTradeRejected}
	}
	attest := attestFor(*n.order, result)
	var events []domain.OrderEvent
	for i, kind := range kinds {
		event := domain.OrderEvent{ExternalID: domain.ExternalID(fmt.Sprintf("paper-confirm-event-%d", i)), Order: id, Type: kind}
		att, ok, err := attest(ctx, event)
		if err != nil {
			return domain.Order{}, engine.OrderResult{}, err
		}
		if !ok {
			return domain.Order{}, engine.OrderResult{}, errors.New("missing review decision attestation")
		}
		event.Attestation = &att
		events = append(events, event)
	}
	n.order = &order
	n.orderEvents = append(n.orderEvents, events...)
	if n.accepted || force {
		n.review = nil
	}
	return order, result, nil
}

func newTradingReviewTest(t *testing.T, held ...bool) (*Service, *paperSendTestNode, *paperSendTestRuntime, *paperSendTestSigner, domain.TradingDestination) {
	t.Helper()
	s, n, rt, signer := newPaperSendTestService(t)
	signer.reviewTokens = make(map[string]domain.ApprovalPayload)
	n.reviews = []domain.ReviewMark{{OrderReject: domain.OrderReject{Code: "order_size_limit", Reason: "review required"}, Mode: domain.RuleModeReviewWithoutHold}}
	if len(held) > 0 && held[0] {
		n.reviews[0].Mode = domain.RuleModeReviewWithHold
	}
	result, err := s.SendPaperOrder(systemCtx(), paperSendTestOrder(), SendDestination{Connection: "connection-1", Route: `{"market":"AAPL"}`})
	if err != nil || result.Approval.Verdict != domain.VerdictReview || result.Send != nil {
		t.Fatalf("prepare waiting trading order: result=%+v err=%v", result, err)
	}
	destination := *n.review.Destination
	n.events, rt.preChecks, rt.drafts = nil, nil, nil
	n.reads = 0
	return s, n, rt, signer, destination
}

func (n *paperSendTestNode) CancelOrderReview(
	ctx context.Context, id domain.ExternalID, _ domain.Caller,
	attest store.EventAttestor,
) (domain.Order, error) {
	n.cancels++
	n.events = append(n.events, "review_cancel")
	order := *n.order
	order.Status, order.Leaves = domain.OrderStatusCancelled, "0"
	event := domain.OrderEvent{
		ExternalID: "paper-review-cancel-event", Order: id,
		Type: domain.OrderEventCancelled,
	}
	att, ok, err := attest(ctx, event)
	if err != nil {
		return domain.Order{}, err
	}
	if !ok {
		return domain.Order{}, errors.New("missing review cancel attestation")
	}
	event.Attestation = &att
	n.orderEvents = append(n.orderEvents, event)
	n.order, n.review = &order, nil
	return order, nil
}

func TestOrderReviewHeldCrossCheck(t *testing.T) {
	for _, action := range []string{"confirm", "cancel", "force"} {
		for _, fault := range []string{"row_held", "row_not_held", "empty_mode", "reject_mode", "unknown_mode"} {
			t.Run(action+"/"+fault, func(t *testing.T) {
				s, n, rt, signer, _ := newTradingReviewTest(t, fault == "row_not_held")
				switch fault {
				case "row_held", "row_not_held":
					n.review.Held = !n.review.Held
				default:
					mode := domain.RuleMode("")
					switch fault {
					case "reject_mode":
						mode = domain.RuleModeReject
					case "unknown_mode":
						mode = "unknown"
					}
					for token, payload := range signer.reviewTokens {
						payload.Reviews = []domain.ReviewMark{{OrderReject: domain.OrderReject{Code: "test_rule"}, Mode: mode}}
						signer.reviewTokens[token] = payload
					}
				}
				before, review := *n.order, *n.review
				tokens, events := len(signer.reviewTokens), len(n.orderEvents)
				var err error
				switch action {
				case "confirm":
					_, _, err = s.ConfirmOrderReview(systemCtx(), before.ExternalID.String())
				case "cancel":
					_, _, _, err = s.CancelOrderReview(systemCtx(), before.ExternalID.String())
				case "force":
					_, _, err = s.ForceOrderReview(systemCtx(), before.ExternalID.String())
				}
				if !errors.Is(err, domain.ErrApprovalRequired) {
					t.Fatalf("held cross-check err=%v, want ErrApprovalRequired", err)
				}
				if n.reviewConfirms+n.reviewForces+n.cancels != 0 || len(n.events) != 0 || len(rt.preChecks) != 0 || len(rt.sends) != 0 || len(signer.reviewTokens) != tokens || len(n.orderEvents) != events {
					t.Fatalf("decision reached a side effect: calls=%v confirms=%d forces=%d cancels=%d prechecks=%v sends=%v tokens=%d events=%d", n.events, n.reviewConfirms, n.reviewForces, n.cancels, rt.preChecks, rt.sends, len(signer.reviewTokens), len(n.orderEvents))
				}
				if !reflect.DeepEqual(*n.order, before) || n.order.Status != domain.OrderStatusAwaitingReview || !reflect.DeepEqual(n.review, &review) {
					t.Fatalf("waiting state changed: order=%+v review=%+v", n.order, n.review)
				}
			})
		}
	}
}

func TestConfirmOrderReviewTradingAccept(t *testing.T) {
	testOrderReviewTradingAccept(t, false)
}

func TestForceOrderReviewTradingAccept(t *testing.T) {
	testOrderReviewTradingAccept(t, true)
}

func testOrderReviewTradingAccept(t *testing.T, force bool) {
	s, n, rt, _, destination := newTradingReviewTest(t, force)
	n.venue = &domain.VenueOrder{Order: n.order.ExternalID, Connection: destination.Connection, VenueAccount: destination.VenueAccount}
	decide := s.ConfirmOrderReview
	decisionEvent := "decision"
	if force {
		decide, decisionEvent = s.ForceOrderReview, "force"
	}
	order, result, err := decide(systemCtx(), n.order.ExternalID.String())
	if err != nil || order.Status != domain.OrderStatusCommitted || result.Approval.Verdict != domain.VerdictAccept || result.Send == nil || result.Send.Status != "acknowledged" || result.Send.Order == nil {
		t.Fatalf("confirm order=%+v result=%+v err=%v", order, result, err)
	}
	wantConfirms, wantForces := 1, 0
	if force {
		wantConfirms, wantForces = 0, 1
	}
	if !reflect.DeepEqual(n.events, []string{"precheck", decisionEvent, "send", "read"}) || n.reviewConfirms != wantConfirms || n.reviewForces != wantForces || n.holds != 1 {
		t.Fatalf("confirm call sequence=%v decisions=%d submits=%d", n.events, n.reviewConfirms, n.holds)
	}
	if !reflect.DeepEqual(rt.preChecks, []domain.TradingDestination{destination}) || !reflect.DeepEqual(rt.sends, []domain.TradingDestination{destination}) || len(rt.drafts) != 1 || rt.drafts[0].Status != domain.OrderStatusAwaitingReview || rt.orderIDs[0] != order.ExternalID {
		t.Fatalf("destination/draft changed: prechecks=%+v sends=%+v drafts=%+v ids=%v", rt.preChecks, rt.sends, rt.drafts, rt.orderIDs)
	}
}

func TestConfirmOrderReviewTradingReject(t *testing.T) {
	s, n, rt, _, _ := newTradingReviewTest(t)
	n.accepted = false
	order, result, err := s.ConfirmOrderReview(systemCtx(), n.order.ExternalID.String())
	if err != nil || order.Status != domain.OrderStatusAwaitingReview || result.Approval.Verdict != domain.VerdictReject || result.Send != nil || len(rt.sends) != 0 || n.review == nil {
		t.Fatalf("reject order=%+v result=%+v err=%v sends=%v", order, result, err, rt.sends)
	}
	if !reflect.DeepEqual(n.events, []string{"precheck", "decision"}) {
		t.Fatalf("reject sequence=%v", n.events)
	}
}

func TestConfirmOrderReviewTradingRefusals(t *testing.T) {
	testOrderReviewTradingRefusals(t, false)
}

func TestForceOrderReviewTradingRefusals(t *testing.T) {
	testOrderReviewTradingRefusals(t, true)
}

func TestForceOrderReviewTradingUnexpectedVerdict(t *testing.T) {
	for _, verdict := range []domain.Verdict{domain.VerdictReject, domain.VerdictReview, "unknown"} {
		t.Run(string(verdict), func(t *testing.T) {
			s, n, rt, _, _ := newTradingReviewTest(t, true)
			n.forceVerdict = verdict
			_, _, err := s.ForceOrderReview(systemCtx(), n.order.ExternalID.String())
			want := fmt.Sprintf("force order review verdict %q is invalid", verdict)
			if err == nil || !strings.Contains(err.Error(), want) || n.reviewForces != 1 || len(rt.sends) != 0 {
				t.Fatalf("unexpected verdict err=%v calls=%d sends=%v", err, n.reviewForces, rt.sends)
			}
		})
	}
}

func testOrderReviewTradingRefusals(t *testing.T, force bool) {
	faults := []string{"precheck", "binding_removed", "venue_account_changed", "empty_stored_account", "empty_binding_account", "runtime_missing", "not_waiting"}
	if force {
		faults = append(faults, "not_held", "node_conflict")
	}
	for _, fault := range faults {
		t.Run(fault, func(t *testing.T) {
			s, n, rt, signer, _ := newTradingReviewTest(t, force && fault != "not_held")
			want := domain.ErrForbidden
			wantCalls := 0
			switch fault {
			case "not_held":
				want = domain.ErrConflict
			case "node_conflict":
				n.submitErr = fmt.Errorf("account blocked: %w", domain.ErrConflict)
				want, wantCalls = domain.ErrConflict, 1
			case "precheck":
				rt.preCheckErr = fmt.Errorf("venue refused: %w", trading.ErrRejected)
				want = domain.ErrInvalid
			case "binding_removed":
				n.access = nil
			case "venue_account_changed":
				n.access[0].VenueAccount = "another-account"
			case "empty_stored_account":
				n.review.Destination.VenueAccount = ""
			case "empty_binding_account":
				n.access[0].VenueAccount = ""
			case "runtime_missing":
				s.trading = nil
				want = domain.ErrNotImplemented
			case "not_waiting":
				n.order.Status = domain.OrderStatusCommitted
				want = domain.ErrConflict
			}
			before := *n.order
			tokens := len(signer.reviewTokens)
			decide := s.ConfirmOrderReview
			if force {
				decide = s.ForceOrderReview
			}
			_, _, err := decide(systemCtx(), n.order.ExternalID.String())
			if !errors.Is(err, want) || n.reviewConfirms+n.reviewForces != wantCalls || len(rt.sends) != 0 || !reflect.DeepEqual(*n.order, before) || n.review == nil || len(signer.reviewTokens) != tokens {
				t.Fatalf("refusal err=%v want=%v confirms=%d forces=%d sends=%v order=%+v", err, want, n.reviewConfirms, n.reviewForces, rt.sends, n.order)
			}
			if strings.Contains(fault, "account") && !strings.Contains(err.Error(), "trading venue account differs from the binding") {
				t.Fatalf("venue mismatch error=%v", err)
			}
			if (fault == "not_waiting" || fault == "not_held") && len(rt.preChecks) != 0 {
				t.Fatal("non-waiting order reached venue")
			}
		})
	}
}

func TestConfirmOrderReviewTradingEmptyVenueAccount(t *testing.T) {
	s, n, _, _, _ := newTradingReviewTest(t)
	n.access[0].VenueAccount = ""
	n.review.Destination.VenueAccount = ""
	_, result, err := s.ConfirmOrderReview(systemCtx(), n.order.ExternalID.String())
	if err != nil || result.Send == nil || result.Send.Status != "acknowledged" {
		t.Fatalf("matching empty venue accounts: result=%+v err=%v", result, err)
	}
}

func TestConfirmOrderReviewTradingNotSent(t *testing.T) {
	testOrderReviewTradingNotSent(t, false)
}

func TestForceOrderReviewTradingNotSent(t *testing.T) {
	testOrderReviewTradingNotSent(t, true)
}

func testOrderReviewTradingNotSent(t *testing.T, force bool) {
	s, n, rt, signer, _ := newTradingReviewTest(t, force)
	rt.sendErr = fmt.Errorf("%w: unavailable: %w", trading.ErrNotSent, domain.ErrUpstream)
	decide := s.ConfirmOrderReview
	if force {
		decide = s.ForceOrderReview
	}
	order, result, err := decide(systemCtx(), n.order.ExternalID.String())
	if err != nil || order.Status != domain.OrderStatusCommitted || result.Send == nil || result.Send.Status != "refused" || n.cancels != 1 || n.order.Status != domain.OrderStatusCancelled || n.order.Leaves != "0" {
		t.Fatalf("not sent order=%+v result=%+v err=%v cancels=%d stored=%+v", order, result, err, n.cancels, n.order)
	}
	if signer.payload.RequestType != string(domain.AttestationRequestCancel) || signer.payload.RejectReason != result.Send.Error {
		t.Fatalf("not-sent cancellation payload=%+v send=%+v", signer.payload, result.Send)
	}
}

func TestConfirmOrderReviewTradingRedacts(t *testing.T) {
	for _, stage := range []string{"precheck", "send", "not_sent", "cancel_failure", "venue_read"} {
		t.Run(stage, func(t *testing.T) {
			s, n, rt, signer, _ := newTradingReviewTest(t)
			secretError := errors.New("upstream test-key-value test-secret-value")
			want := "unknown"
			switch stage {
			case "precheck":
				rt.preCheckErr = secretError
			case "not_sent", "cancel_failure":
				rt.sendErr = fmt.Errorf("%w: %w", trading.ErrNotSent, secretError)
				want = "refused"
				if stage == "cancel_failure" {
					n.cancelErr = secretError
					want = "unknown"
				}
			case "venue_read":
				n.venueErr = secretError
			default:
				rt.sendErr = secretError
			}
			_, result, err := s.ConfirmOrderReview(systemCtx(), n.order.ExternalID.String())
			text := fmt.Sprintf("%v %+v %+v %+v", err, result.Send, signer.payload, n.audits)
			if strings.Contains(text, "test-key-value") || strings.Contains(text, "test-secret-value") || !strings.Contains(text, "[redacted]") {
				t.Fatalf("credentials leaked: %s", text)
			}
			if stage == "precheck" {
				if !errors.Is(err, domain.ErrUpstream) || n.reviewConfirms != 0 || n.order.Status != domain.OrderStatusAwaitingReview {
					t.Fatalf("precheck err=%v decisions=%d order=%+v", err, n.reviewConfirms, n.order)
				}
			} else if err != nil || result.Send == nil || result.Send.Status != want {
				t.Fatalf("redacted send=%+v err=%v", result.Send, err)
			}
		})
	}
}
