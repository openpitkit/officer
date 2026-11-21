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

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/signing"
)

type backendReviewAlertObserver struct {
	failStep string
	cause    error
	steps    []string
	orders   []domain.Order
	callers  []domain.Caller
	marks    []domain.ReviewMark
	log      *[]string
}

func (o *backendReviewAlertObserver) OrderAwaitingReview(
	_ context.Context, order domain.Order, mark domain.ReviewMark, caller domain.Caller,
) error {
	o.steps = append(o.steps, domain.OrderReviewAlertStepPublish)
	o.orders = append(o.orders, order)
	o.callers = append(o.callers, caller)
	o.marks = append(o.marks, mark)
	if o.failStep == domain.OrderReviewAlertStepPublish {
		return o.cause
	}
	return nil
}

func (o *backendReviewAlertObserver) OrderReviewDecided(
	_ context.Context, order domain.Order, caller domain.Caller,
) error {
	if o.log != nil {
		*o.log = append(*o.log, "close")
	}
	o.steps = append(o.steps, domain.OrderReviewAlertStepClose)
	o.orders = append(o.orders, order)
	o.callers = append(o.callers, caller)
	if o.failStep == domain.OrderReviewAlertStepClose {
		return o.cause
	}
	return nil
}

func newBackendReviewAlertTest(
	t *testing.T, off bool, failStep string, options ...backend.Option,
) (*backend.Service, node.Node, domain.Order, *backendReviewAlertObserver) {
	t.Helper()
	n, realm, _, _, _ := newIsolationNode(t)
	order := seedHeldReviewNode(t, n, false)
	signer, err := signing.New(realm)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.GenerateKey(systemCtx()); err != nil {
		t.Fatal(err)
	}
	if err := signer.SetNoESign(systemCtx(), off); err != nil {
		t.Fatal(err)
	}
	svc, err := backend.New(n, nil, signer, engine.LockSettlementPrice, options...)
	if err != nil {
		t.Fatal(err)
	}
	observer := &backendReviewAlertObserver{
		failStep: failStep, cause: errors.New("review alert unavailable"),
	}
	host, ok := n.(node.OrderReviewObserverHost)
	if !ok {
		t.Fatal("local node does not expose the review observer host")
	}
	if err := host.InstallOrderReviewObserver(observer); err != nil {
		t.Fatal(err)
	}
	return svc, n, order, observer
}

func requireBackendReviewAlertFailure(t *testing.T, failure *backend.AlertFailure, step string, cause error) {
	t.Helper()
	if failure == nil || failure.Step != step || failure.Message != cause.Error() {
		t.Fatalf("alert failure = %+v; want %s, %q", failure, step, cause.Error())
	}
}

func TestService_OrderReviewAlertPublishFailure(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, off := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/esign-off=%t", mode, off), func(t *testing.T) {
				svc, n, input, observer := newBackendReviewAlertTest(t, off, domain.OrderReviewAlertStepPublish)
				approval, err := svc.SubmitOrder(systemCtx(), input, mode, domain.MissingAccountReject)
				if err != nil || approval.Verdict != domain.VerdictReview || approval.SignedApproval == "" || approval.Signed == off {
					t.Fatalf("submit = %+v, %v; want successful review approval", approval, err)
				}
				requireBackendReviewAlertFailure(t, approval.AlertFailure, domain.OrderReviewAlertStepPublish, observer.cause)
				detail, err := n.GetOrder(systemCtx(), input.ExternalID)
				if err != nil || detail.Order.Status != domain.OrderStatusAwaitingReview || detail.Review == nil || !detail.Review.Held {
					t.Fatalf("persisted review = %+v, %v", detail, err)
				}
				if !reflect.DeepEqual(observer.steps, []string{domain.OrderReviewAlertStepPublish}) {
					t.Fatalf("observer steps = %v; want [publish]", observer.steps)
				}
				// The store reads an order without commissions back as an empty slice.
				got, want := observer.orders[0], detail.Order
				if len(got.CommissionSubtotals) == 0 && len(want.CommissionSubtotals) == 0 {
					got.CommissionSubtotals, want.CommissionSubtotals = nil, nil
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("published order = %#v; want %#v", got, want)
				}
				if !reflect.DeepEqual(observer.callers[0], auth.SystemCaller()) {
					t.Fatalf("published caller = %+v", observer.callers[0])
				}
				if !reflect.DeepEqual(observer.marks[0], approval.Reviews[0]) {
					t.Fatalf("published mark = %+v; want %+v", observer.marks[0], approval.Reviews[0])
				}
			})
		}
	}
}

func TestService_OrderReviewAlertCloseFailure(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, action := range []string{"confirm", "force", "cancel"} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				svc, n, input, observer := newBackendReviewAlertTest(t, false, domain.OrderReviewAlertStepClose)
				approval, err := svc.SubmitOrder(systemCtx(), input, mode, domain.MissingAccountReject)
				if err != nil || approval.Verdict != domain.VerdictReview || approval.AlertFailure != nil {
					t.Fatalf("prepare review = %+v, %v", approval, err)
				}
				var order domain.Order
				var failure *backend.AlertFailure
				status := domain.OrderStatusCommitted
				if mode == domain.SubmitModeImmediate {
					status = domain.OrderStatusFilled
				}
				if action == "cancel" {
					status = domain.OrderStatusCancelled
					var att backend.Attestation
					order, att, failure, err = svc.CancelOrderReview(systemCtx(), approval.OrderExternalID)
					if att.SignedAttestation == "" {
						t.Fatal("cancel lost its attestation")
					}
				} else {
					decide := svc.ConfirmOrderReview
					if action == "force" {
						decide = svc.ForceOrderReview
					}
					var result backend.VenueSendResult
					order, result, err = decide(systemCtx(), approval.OrderExternalID)
					failure = result.Approval.AlertFailure
					if result.Approval.Verdict != domain.VerdictAccept || result.Approval.SignedApproval == "" || result.Send != nil {
						t.Fatalf("decision = %+v; want accepted non-trading result", result)
					}
				}
				if err != nil || order.Status != status {
					t.Fatalf("decision order = %+v, %v; want %s", order, err, status)
				}
				requireBackendReviewAlertFailure(t, failure, domain.OrderReviewAlertStepClose, observer.cause)
				detail, err := n.GetOrder(systemCtx(), input.ExternalID)
				if err != nil || detail.Review != nil || !reflect.DeepEqual(detail.Order, order) {
					t.Fatalf("persisted decision = %+v, %v; want %+v", detail, err, order)
				}
				if !reflect.DeepEqual(observer.steps, []string{domain.OrderReviewAlertStepPublish, domain.OrderReviewAlertStepClose}) || !reflect.DeepEqual(observer.orders[1], order) || !reflect.DeepEqual(observer.callers[1], auth.SystemCaller()) {
					t.Fatalf("close observer = %+v", observer)
				}
			})
		}
	}
}

type backendReviewAlertRuntime struct {
	backend.TradingRuntime
	log *[]string
}

func (*backendReviewAlertRuntime) PreCheck(
	context.Context, domain.Order, domain.TradingDestination,
) error {
	return nil
}

func (r *backendReviewAlertRuntime) Send(
	context.Context, domain.ExternalID, domain.TradingDestination,
) (domain.VenueOrder, error) {
	*r.log = append(*r.log, "send")
	return domain.VenueOrder{}, nil
}

func backendReviewAlertPaperDestination(
	t *testing.T, n node.Node, account domain.AccountID,
) backend.SendDestination {
	t.Helper()
	ctx := systemCtx()
	connections, err := n.ListTradingConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, connection := range connections {
		if connection.Mode != domain.TradingModePaper {
			continue
		}
		if err := n.PutTradingAccess(ctx, domain.TradingAccess{
			Account: account, Connection: connection.ExternalID, VenueAccount: "review-venue-account",
		}, false, auth.SystemCaller()); err != nil {
			t.Fatal(err)
		}
		return backend.SendDestination{Connection: connection.ExternalID, VenueAccount: "review-venue-account", Route: "{}"}
	}
	t.Fatal("isolation fixture has no paper connection")
	return backend.SendDestination{}
}

func TestService_OrderReviewAlertCloseAfterTradingSend(t *testing.T) {
	for _, action := range []string{"confirm", "force"} {
		t.Run(action, func(t *testing.T) {
			var log []string
			runtime := &backendReviewAlertRuntime{log: &log}
			svc, n, input, observer := newBackendReviewAlertTest(t, false,
				domain.OrderReviewAlertStepClose, backend.WithTradingRuntime(runtime))
			observer.log = &log
			dest := backendReviewAlertPaperDestination(t, n, input.Account)
			submitted, err := svc.SendPaperOrder(systemCtx(), input, dest)
			if err != nil || submitted.Approval.Verdict != domain.VerdictReview ||
				submitted.Approval.AlertFailure != nil || submitted.Send != nil || len(log) != 0 {
				t.Fatalf("prepare trading review = %+v, %v, log=%v", submitted, err, log)
			}
			decide := svc.ConfirmOrderReview
			if action == "force" {
				decide = svc.ForceOrderReview
			}
			order, result, err := decide(systemCtx(), submitted.Approval.OrderExternalID)
			if err != nil || order.Status != domain.OrderStatusCommitted ||
				result.Approval.Verdict != domain.VerdictAccept ||
				result.Send == nil || result.Send.Status != "acknowledged" {
				t.Fatalf("trading decision = %+v, %+v, %v", order, result, err)
			}
			if !reflect.DeepEqual(log, []string{"send", "close"}) {
				t.Fatalf("operation log = %v; want [send close]", log)
			}
			requireBackendReviewAlertFailure(t, result.Approval.AlertFailure,
				domain.OrderReviewAlertStepClose, observer.cause)
		})
	}
}

func TestService_SendPaperOrderReviewAlertPublishFailure(t *testing.T) {
	var log []string
	runtime := &backendReviewAlertRuntime{log: &log}
	svc, n, input, observer := newBackendReviewAlertTest(t, false,
		domain.OrderReviewAlertStepPublish, backend.WithTradingRuntime(runtime))
	dest := backendReviewAlertPaperDestination(t, n, input.Account)
	result, err := svc.SendPaperOrder(systemCtx(), input, dest)
	if err != nil || result.Approval.Verdict != domain.VerdictReview ||
		result.Approval.SignedApproval == "" || result.Send != nil || len(log) != 0 {
		t.Fatalf("send paper review = %+v, %v, log=%v", result, err, log)
	}
	requireBackendReviewAlertFailure(t, result.Approval.AlertFailure,
		domain.OrderReviewAlertStepPublish, observer.cause)
	detail, err := n.GetOrder(systemCtx(), input.ExternalID)
	if err != nil || detail.Order.Status != domain.OrderStatusAwaitingReview || detail.Review == nil ||
		detail.Review.Destination == nil || detail.Review.Destination.Connection != dest.Connection {
		t.Fatalf("persisted trading review = %+v, %v", detail, err)
	}
	if !reflect.DeepEqual(observer.steps, []string{domain.OrderReviewAlertStepPublish}) {
		t.Fatalf("observer steps = %v; want [publish]", observer.steps)
	}
}
