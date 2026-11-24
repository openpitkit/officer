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
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/secret"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/internal/store/sqlite"
	"go.openpit.dev/officer/signing"
)

func newBackendReviewTest(t *testing.T, mode string, off bool, held ...bool) (*backend.Service, *fakeNode, *signing.Service, backend.SignedApproval) {
	t.Helper()
	ctx := systemCtx()
	key, err := secret.ParseMasterKey("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	st, err := sqlite.New(filepath.Join(t.TempDir(), "signing.db"), domain.DefaultRealm, sqlite.WithMasterKey(key))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := signing.New(realm)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.GenerateKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := signer.SetNoESign(ctx, off); err != nil {
		t.Fatal(err)
	}
	svc, n := newTestServiceWithSigner(t, signer)
	marks := []domain.ReviewMark{{OrderReject: domain.OrderReject{Code: "order_size_limit", Reason: "review required"}, Mode: domain.RuleModeReviewWithoutHold}}
	n.submitResult = &engine.OrderResult{Verdict: domain.VerdictReview, Reviews: marks}
	n.immediateResult = &engine.ImmediateResult{Verdict: domain.VerdictReview, Reviews: marks}
	if len(held) > 0 && held[0] {
		marks[0].Mode = domain.RuleModeReviewWithHold
		n.submitResult.Held = true
		n.submitResult.Lock = []byte("123.45")
		n.submitResult.SettlementLockPrice = "123.45"
		n.immediateResult.Lock = []byte("123.45")
		n.immediateResult.SettlementLockPrice = "123.45"
	}
	approval, err := svc.SubmitOrder(ctx, sampleOrder(), mode, domain.MissingAccountCreate)
	if err != nil {
		t.Fatal(err)
	}
	n.submitResult = &engine.OrderResult{Verdict: domain.VerdictAccept, SettlementLockPrice: "234.56"}
	n.immediateResult = nil
	n.accountCalls = nil
	n.getOrderCount.Store(0)
	return svc, n, signer, approval
}

func verifiedReviewTestPayload(t *testing.T, signer *signing.Service, token string, order domain.Order) domain.ApprovalPayload {
	t.Helper()
	verified, err := signer.Verify(systemCtx(), token, fwsigning.VerifyParams{
		OrderExternalID: order.ExternalID.String(), Instrument: "AAPL/USD", Side: string(order.Side),
		Quantity: order.AmountValue, AmountKind: string(order.AmountKind), OrderType: "limit",
		LimitPrice: order.Price, PriceCurrency: "USD", AccountID: string(order.Account),
	})
	if err != nil {
		t.Fatalf("verify decision envelope: %v", err)
	}
	return verified.Payload
}

func replaceReviewTestPayload(t *testing.T, n *fakeNode, signer *signing.Service, id domain.ExternalID, kind domain.OrderEventType, mutate func(*domain.ApprovalPayload)) {
	t.Helper()
	for _, event := range n.orderEvents[id] {
		if event.Type != kind {
			continue
		}
		att := n.attestations[event.ExternalID]
		payload := verifiedReviewTestPayload(t, signer, att.SignedEnvelope, n.orders[id])
		mutate(&payload)
		token, err := signer.Sign(payload)
		if err != nil {
			t.Fatal(err)
		}
		att.SignedEnvelope = token
		n.attestations[event.ExternalID] = att
		return
	}
	t.Fatalf("missing event %s", kind)
}

func TestService_ConfirmOrderReviewDecision(t *testing.T) {
	t.Parallel()
	t.Run("unheld", func(t *testing.T) {
		testServiceOrderReviewDecision(t, false, false)
	})
	t.Run("held", func(t *testing.T) {
		testServiceOrderReviewDecision(t, true, false)
	})
}

func TestService_ForceOrderReviewDecision(t *testing.T) {
	t.Parallel()
	t.Run("held", func(t *testing.T) {
		testServiceOrderReviewDecision(t, true, true)
	})
}

func testServiceOrderReviewDecision(t *testing.T, held, force bool) {
	t.Parallel()
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, off := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/eSignOff=%t", mode, off), func(t *testing.T) {
				t.Parallel()
				svc, n, signer, review := newBackendReviewTest(t, mode, off, held)
				id := domain.ExternalID(review.OrderExternalID)
				original := verifiedReviewTestPayload(t, signer, review.SignedApproval, n.orders[id])
				caller := domain.Caller{Source: domain.SourceAPI, Principal: "review-operator"}
				ctx := auth.ContextWithCaller(context.Background(), caller)
				audits := len(n.auditCalls)
				decide := svc.ConfirmOrderReview
				requestType := domain.AttestationRequestReviewConfirm
				if force {
					decide = svc.ForceOrderReview
					requestType = domain.AttestationRequestReviewForce
				}
				order, result, err := decide(ctx, review.OrderExternalID)
				if err != nil {
					t.Fatal(err)
				}
				wantStatus := domain.OrderStatusCommitted
				wantMethod := "ConfirmOrderReview"
				if mode == domain.SubmitModeImmediate {
					wantStatus, wantMethod = domain.OrderStatusFilled, "ConfirmImmediateReview"
				}
				if force {
					wantMethod = strings.Replace(wantMethod, "Confirm", "Force", 1)
				}
				if order.Status != wantStatus || !reflect.DeepEqual(order, n.orders[id]) || result.Send != nil || result.Approval.Verdict != domain.VerdictAccept || len(result.Approval.Reviews) != 0 || result.Approval.Signed == off {
					t.Fatalf("confirm order=%+v result=%+v", order, result)
				}
				if len(n.accountCalls) != 1 || !strings.HasPrefix(n.accountCalls[0], wantMethod+" ") || len(n.auditCalls) != audits || n.getOrderCount.Load() != 1 {
					t.Fatalf("calls=%v audits=%v reads=%d", n.accountCalls, n.auditCalls, n.getOrderCount.Load())
				}
				p := verifiedReviewTestPayload(t, signer, result.Approval.SignedApproval, order)
				if p.RequestType != string(requestType) || p.ApprovalRef != original.ApprovalID || p.ApprovalID == "" || p.ApprovalID == original.ApprovalID || p.Principal != caller.Principal || p.Mode != mode {
					t.Fatalf("confirm payload=%+v original=%+v", p, original)
				}
				wantPrice := "234.56"
				if held {
					wantPrice = "123.45"
				}
				if (mode == domain.SubmitModeHold || held && !force) && p.EstimatePrice != wantPrice {
					t.Fatalf("lock price=%q, want %s", p.EstimatePrice, wantPrice)
				}
				submitted, err := svc.SubmitOrder(ctx, sampleOrder(), mode, domain.MissingAccountCreate)
				if err != nil {
					t.Fatal(err)
				}
				submittedID := domain.ExternalID(submitted.OrderExternalID)
				var submittedResult *domain.AttestationResult
				for _, event := range n.orderEvents[submittedID] {
					if event.Type == domain.OrderEventCommitted {
						att := n.attestations[event.ExternalID]
						payload := verifiedReviewTestPayload(t, signer, att.SignedEnvelope, n.orders[submittedID])
						submittedResult = payload.Result
					}
				}
				if submittedResult == nil || submittedResult.LeavesQuantity != "" {
					t.Fatalf("submit committed result=%+v, want empty leaves", submittedResult)
				}
				committed := false
				for _, event := range n.orderEvents[id][2:] {
					att, ok := n.attestations[event.ExternalID]
					if !ok || att.SignedEnvelope == "" {
						t.Fatalf("missing attestation for %+v", event)
					}
					payload := verifiedReviewTestPayload(t, signer, att.SignedEnvelope, order)
					if payload.EventExternalID != event.ExternalID.String() {
						t.Fatalf("event binding=%+v", payload)
					}
					if event.Type == domain.OrderEventCommitted {
						committed = true
						if payload.RequestType != string(requestType) || payload.ApprovalRef != p.ApprovalID {
							t.Fatalf("committed payload=%+v", payload)
						}
						if payload.Result == nil || payload.Result.LeavesQuantity != submittedResult.LeavesQuantity {
							t.Fatalf("confirm committed result=%+v, want submit leaves %q", payload.Result, submittedResult.LeavesQuantity)
						}
					}
					if event.Type == domain.OrderEventFill && payload.RequestType != string(domain.AttestationRequestExecutionReport) {
						t.Fatalf("fill payload=%+v", payload)
					}
					if off && (payload.Alg != fwsigning.AlgNone || payload.KeyID != "") {
						t.Fatalf("unsigned payload=%+v", payload)
					}
				}
				if !committed {
					t.Fatal("confirm has no committed event")
				}
				if _, exists := n.orderReviews[id]; exists {
					t.Fatal("accepted review row was not removed")
				}
				if force {
					calls := len(n.accountCalls)
					if _, _, err := svc.ForceOrderReview(ctx, review.OrderExternalID); !errors.Is(err, domain.ErrConflict) || len(n.accountCalls) != calls {
						t.Fatalf("second force err=%v calls=%v", err, n.accountCalls)
					}
				}
			})
		}
	}
}

func TestService_ConfirmOrderReviewReject(t *testing.T) {
	t.Parallel()
	svc, n, signer, review := newBackendReviewTest(t, domain.SubmitModeHold, false)
	id := domain.ExternalID(review.OrderExternalID)
	original := verifiedReviewTestPayload(t, signer, review.SignedApproval, n.orders[id])
	n.submitResult = &engine.OrderResult{Verdict: domain.VerdictReject, Rejects: []domain.OrderReject{{Code: "order_size_limit", Reason: "too large"}}}
	order, result, err := svc.ConfirmOrderReview(systemCtx(), review.OrderExternalID)
	if err != nil || order.Status != domain.OrderStatusAwaitingReview || result.Send != nil || result.Approval.Verdict != domain.VerdictReject || len(result.Approval.Reasons) != 1 || len(result.Approval.Reviews) != 0 {
		t.Fatalf("reject order=%+v result=%+v err=%v", order, result, err)
	}
	p := verifiedReviewTestPayload(t, signer, result.Approval.SignedApproval, order)
	if p.RequestType != string(domain.AttestationRequestReviewConfirm) || p.ApprovalRef != original.ApprovalID || len(p.Rejects) != 1 || p.Rejects[0].Code != "order_size_limit" {
		t.Fatalf("reject payload=%+v", p)
	}
	if _, ok := n.orderReviews[id]; !ok {
		t.Fatal("reject removed review row")
	}
}

func TestService_CancelOrderReview(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, off := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/eSignOff=%t", mode, off), func(t *testing.T) {
				t.Parallel()
				svc, n, signer, review := newBackendReviewTest(t, mode, off)
				id := domain.ExternalID(review.OrderExternalID)
				original := verifiedReviewTestPayload(t, signer, review.SignedApproval, n.orders[id])
				caller := domain.Caller{Source: domain.SourceAPI, Principal: "review-operator"}
				audits := len(n.auditCalls)
				order, att, _, err := svc.CancelOrderReview(auth.ContextWithCaller(context.Background(), caller), review.OrderExternalID)
				if err != nil || order.Status != domain.OrderStatusCancelled || order.Leaves != "0" || att.Signed == off {
					t.Fatalf("cancel order=%+v att=%+v err=%v", order, att, err)
				}
				if len(n.accountCalls) != 1 || !strings.HasPrefix(n.accountCalls[0], "CancelOrderReview ") || len(n.auditCalls) != audits {
					t.Fatalf("cancel called engine path or backend audit: calls=%v audits=%v", n.accountCalls, n.auditCalls)
				}
				p := verifiedReviewTestPayload(t, signer, att.SignedAttestation, order)
				if p.RequestType != string(domain.AttestationRequestReviewCancel) || p.ApprovalRef != original.ApprovalID || p.Result == nil || p.Result.OrderStatus != "cancelled" || p.Result.Outcome != "cancelled" || p.Result.LeavesQuantity != "0" || p.Mode != mode || p.Principal != caller.Principal || p.RejectReason != "" {
					t.Fatalf("cancel payload=%+v", p)
				}
				if off && (p.Alg != fwsigning.AlgNone || p.KeyID != "") {
					t.Fatalf("unsigned cancel payload=%+v", p)
				}
				if _, ok := n.orderReviews[id]; ok {
					t.Fatal("cancel kept review row")
				}
			})
		}
	}
}

func TestService_ReviewConfirmedShortcuts(t *testing.T) {
	testServiceReviewShortcuts(t, false)
}

func TestService_ReviewForcedShortcuts(t *testing.T) {
	testServiceReviewShortcuts(t, true)
}

func testServiceReviewShortcuts(t *testing.T, force bool) {
	t.Parallel()
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancel), func(t *testing.T) {
			t.Parallel()
			svc, _, _, review := newBackendReviewTest(t, domain.SubmitModeHold, false, force)
			decide := svc.ConfirmOrderReview
			if force {
				decide = svc.ForceOrderReview
			}
			if _, _, err := decide(systemCtx(), review.OrderExternalID); err != nil {
				t.Fatal(err)
			}
			var err error
			if cancel {
				_, _, err = svc.CancelOrder(systemCtx(), review.OrderExternalID, "0", "operator cancelled")
			} else {
				_, _, err = svc.ConfirmExecution(systemCtx(), review.OrderExternalID)
			}
			if err != nil {
				t.Fatalf("shortcut after review confirm: %v", err)
			}
		})
	}
}

func TestService_ReviewConfirmedShortcutRefusals(t *testing.T) {
	testServiceReviewShortcutRefusals(t, false)
}

func TestService_ReviewForcedShortcutRefusals(t *testing.T) {
	testServiceReviewShortcutRefusals(t, true)
}

func testServiceReviewShortcutRefusals(t *testing.T, force bool) {
	t.Parallel()
	for _, fault := range []string{"wrong_reference", "empty_reference", "missing_review", "unverifiable_review", "mode_mismatch", "derived_accept"} {
		for _, cancel := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel=%t", fault, cancel), func(t *testing.T) {
				t.Parallel()
				svc, n, signer, review := newBackendReviewTest(t, domain.SubmitModeHold, false, force)
				id := domain.ExternalID(review.OrderExternalID)
				decide := svc.ConfirmOrderReview
				if force {
					decide = svc.ForceOrderReview
				}
				if _, _, err := decide(systemCtx(), review.OrderExternalID); err != nil {
					t.Fatal(err)
				}
				switch fault {
				case "wrong_reference", "empty_reference", "derived_accept":
					replaceReviewTestPayload(t, n, signer, id, domain.OrderEventPreTradeAccepted, func(p *domain.ApprovalPayload) {
						switch fault {
						case "derived_accept":
							p.Result = &domain.AttestationResult{}
						case "empty_reference":
							p.ApprovalRef = ""
						default:
							p.ApprovalRef = "another-approval"
						}
					})
				case "mode_mismatch":
					replaceReviewTestPayload(t, n, signer, id, domain.OrderEventPreTradeReview, func(p *domain.ApprovalPayload) { p.Mode = domain.SubmitModeImmediate })
				default:
					for _, event := range n.orderEvents[id] {
						if event.Type == domain.OrderEventPreTradeReview {
							if fault == "missing_review" {
								delete(n.attestations, event.ExternalID)
							} else {
								att := n.attestations[event.ExternalID]
								att.SignedEnvelope = "unverifiable"
								n.attestations[event.ExternalID] = att
							}
						}
					}
				}
				var err error
				if cancel {
					_, _, err = svc.CancelOrder(systemCtx(), review.OrderExternalID, "0", "cancel")
				} else {
					_, _, err = svc.ConfirmExecution(systemCtx(), review.OrderExternalID)
				}
				if !errors.Is(err, domain.ErrApprovalRequired) || len(n.confirmCalls) != 0 || len(n.cancelCalls) != 0 {
					t.Fatalf("shortcut refusal err=%v confirm=%v cancel=%v", err, n.confirmCalls, n.cancelCalls)
				}
			})
		}
	}
}

func TestService_OrderReviewRefusals(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"confirm", "cancel", "force"} {
		faults := []string{"mcp", "not_waiting", "missing_review_approval", "unverifiable", "mode_mismatch", "wrong_verdict", "wrong_request", "approval_reference", "result", "execution_report", "unknown_mode", "missing_row", "trading_immediate", "invalid_id"}
		if operation == "force" {
			faults = append(faults, "not_held")
		}
		for _, fault := range faults {
			t.Run(operation+"/"+fault, func(t *testing.T) {
				t.Parallel()
				svc, n, signer, review := newBackendReviewTest(t, domain.SubmitModeHold, false, operation == "force" && fault != "not_held")
				id := domain.ExternalID(review.OrderExternalID)
				ctx := systemCtx()
				want := domain.ErrApprovalRequired
				switch fault {
				case "not_held":
					want = domain.ErrConflict
				case "mcp":
					ctx = auth.ContextWithCaller(context.Background(), domain.Caller{Source: domain.SourceMCP, Principal: "agent"})
					want = domain.ErrForbidden
				case "not_waiting":
					o := n.orders[id]
					o.Status = domain.OrderStatusCommitted
					n.orders[id] = o
					want = domain.ErrConflict
				case "missing_row":
					delete(n.orderReviews, id)
					want = nil
				case "trading_immediate":
					n.orderReviews[id] = domain.OrderReview{Mode: domain.SubmitModeImmediate, Destination: &domain.TradingDestination{Connection: "connection"}}
					want = nil
				case "mode_mismatch":
					n.orderReviews[id] = domain.OrderReview{Mode: domain.SubmitModeImmediate}
				case "invalid_id":
					review.OrderExternalID = ""
					want = domain.ErrInvalid
				case "missing_review_approval", "unverifiable":
					for _, event := range n.orderEvents[id] {
						if event.Type == domain.OrderEventPreTradeReview {
							if fault == "missing_review_approval" {
								delete(n.attestations, event.ExternalID)
							} else {
								att := n.attestations[event.ExternalID]
								att.SignedEnvelope = "unverifiable"
								n.attestations[event.ExternalID] = att
							}
						}
					}
				default:
					replaceReviewTestPayload(t, n, signer, id, domain.OrderEventPreTradeReview, func(p *domain.ApprovalPayload) {
						switch fault {
						case "wrong_verdict":
							p.Verdict = domain.VerdictAccept
						case "wrong_request":
							p.RequestType = string(domain.AttestationRequestReviewConfirm)
						case "approval_reference":
							p.ApprovalRef = "another-approval"
						case "result":
							p.Result = &domain.AttestationResult{}
						case "execution_report":
							p.ExecutionReport = &domain.ExecutionReportRequest{}
						case "unknown_mode":
							p.Mode = "unknown"
						}
					})
				}
				before := n.orders[id]
				attestations := len(n.attestations)
				var err error
				switch operation {
				case "confirm":
					_, _, err = svc.ConfirmOrderReview(ctx, review.OrderExternalID)
				case "force":
					_, _, err = svc.ForceOrderReview(ctx, review.OrderExternalID)
				default:
					_, _, _, err = svc.CancelOrderReview(ctx, review.OrderExternalID)
				}
				if err == nil || (want != nil && !errors.Is(err, want)) || len(n.accountCalls) != 0 || len(n.attestations) != attestations || !reflect.DeepEqual(n.orders[id], before) {
					t.Fatalf("review refusal err=%v want=%v calls=%v order=%+v", err, want, n.accountCalls, n.orders[id])
				}
				if (fault == "mcp" || fault == "invalid_id") && n.getOrderCount.Load() != 0 {
					t.Fatalf("refused caller/input read order %d times", n.getOrderCount.Load())
				}
				if want == nil && (errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrApprovalRequired)) {
					t.Fatalf("internal inconsistency has domain sentinel: %v", err)
				}
			})
		}
	}
}

func TestService_OrderReviewMissingAttestationFailsClosed(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"confirm", "cancel", "force"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			svc, n, _, review := newBackendReviewTest(t, domain.SubmitModeHold, false, operation == "force")
			n.skipAttestNewEvents = true
			var err error
			request := domain.AttestationRequestReviewConfirm
			switch operation {
			case "confirm":
				_, _, err = svc.ConfirmOrderReview(systemCtx(), review.OrderExternalID)
			case "force":
				request = domain.AttestationRequestReviewForce
				_, _, err = svc.ForceOrderReview(systemCtx(), review.OrderExternalID)
			default:
				request = domain.AttestationRequestReviewCancel
				_, _, _, err = svc.CancelOrderReview(systemCtx(), review.OrderExternalID)
			}
			if err == nil || !strings.Contains(err.Error(), string(request)) {
				t.Fatalf("missing attestation err=%v, want %s failure", err, request)
			}
		})
	}
}

func TestService_CancelTradingOrderReviewWithoutRuntime(t *testing.T) {
	t.Parallel()
	svc, n, _, review := newBackendReviewTest(t, domain.SubmitModeHold, false)
	id := domain.ExternalID(review.OrderExternalID)
	n.orderReviews[id] = domain.OrderReview{Mode: domain.SubmitModeHold, Destination: &domain.TradingDestination{
		Connection: "removed-connection", VenueAccount: "old-account", Route: "{}",
	}}
	order, _, _, err := svc.CancelOrderReview(systemCtx(), review.OrderExternalID)
	if err != nil || order.Status != domain.OrderStatusCancelled {
		t.Fatalf("cancel waiting trading order without a runtime: order=%+v err=%v", order, err)
	}
	if len(n.accountCalls) != 1 || !strings.HasPrefix(n.accountCalls[0], "CancelOrderReview ") {
		t.Fatalf("cancel called engine path: %v", n.accountCalls)
	}
}

func TestService_OrderReviewNodeError(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"confirm", "cancel", "force"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			svc, n, _, review := newBackendReviewTest(t, domain.SubmitModeHold, false, operation == "force")
			failure := errors.New("node decision failed")
			n.confirmErr, n.cancelErr = failure, failure
			audits := len(n.auditCalls)
			var err error
			switch operation {
			case "confirm":
				_, _, err = svc.ConfirmOrderReview(systemCtx(), review.OrderExternalID)
			case "force":
				_, _, err = svc.ForceOrderReview(systemCtx(), review.OrderExternalID)
			default:
				_, _, _, err = svc.CancelOrderReview(systemCtx(), review.OrderExternalID)
			}
			if err != failure || len(n.auditCalls) != audits {
				t.Fatalf("node error=%v, want unchanged %v; audits=%v", err, failure, n.auditCalls)
			}
		})
	}
}
