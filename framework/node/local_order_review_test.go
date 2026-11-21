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
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"go.openpit.dev/openpit/pretrade"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

func newOrderReviewTestNode(t *testing.T, mode string) (*localNode, *verdictAfterReservationEngine, domain.Order) {
	t.Helper()
	base := newFakeEngine()
	n, _ := newTestNode(t, base)
	t.Cleanup(func() { _ = n.Close() })
	eng := &verdictAfterReservationEngine{
		fakeEngine: base, verdict: domain.VerdictReview, confirmedVerdict: domain.VerdictAccept,
		reviews: []domain.ReviewMark{{OrderReject: domain.OrderReject{Code: "order_size_limit"}, Mode: domain.RuleModeReviewWithoutHold}},
	}
	n.engine = eng
	order := domain.Order{
		Account: "review-account", ExternalID: "review-order", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "100",
	}
	var err error
	if mode == domain.SubmitModeHold {
		order, err = n.SubmitOrder(context.Background(), order, domain.MissingAccountCreate, testCaller)
	} else {
		order, _, err = n.SubmitImmediate(context.Background(), order, domain.MissingAccountCreate, testCaller)
	}
	if err != nil {
		t.Fatalf("submit review: %v", err)
	}
	return n, eng, order
}

func confirmReviewForTest(n *localNode, mode string, id domain.ExternalID, attest store.EventAttestor) (domain.Order, domain.Verdict, error) {
	if mode == domain.SubmitModeHold {
		order, result, err := n.ConfirmOrderReview(context.Background(), id, testCaller,
			func(domain.Order, engine.OrderResult) store.EventAttestor { return attest })
		return order, result.Verdict, err
	}
	order, result, err := n.ConfirmImmediateReview(context.Background(), id, testCaller,
		func(domain.Order, engine.ImmediateResult) store.EventAttestor { return attest })
	return order, result.Verdict, err
}

func reviewTestDetail(t *testing.T, n *localNode, id domain.ExternalID) domain.OrderDetail {
	t.Helper()
	detail, err := n.GetOrder(context.Background(), id)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	return detail
}

func reviewTestAudit(t *testing.T, n *localNode, action domain.AuditAction) []domain.AuditRow {
	t.Helper()
	rows, err := n.ListAuditFiltered(context.Background(), domain.AuditFilter{Actions: []domain.AuditAction{action}}, 100)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	return rows
}

func TestLocalNode_OrderReviewAccept(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			n, eng, waiting := newOrderReviewTestNode(t, mode)
			if mode == domain.SubmitModeHold {
				eng.submitLock = []byte("engine-lock")
			}
			eng.submitOutcomes = []engine.BalanceOutcome{{Asset: "AAPL", Outcome: domain.AdjustmentOutcomeAccepted{
				IncomingDelta: "1", IncomingResult: "1", BalanceDelta: "0", BalanceResult: "0", HeldDelta: "0", HeldResult: "0",
			}}}
			attest := func(_ context.Context, event domain.OrderEvent) (domain.EventAttestation, bool, error) {
				return domain.EventAttestation{SignedEnvelope: "confirmed-" + string(event.Type)}, true, nil
			}
			order, verdict, err := confirmReviewForTest(n, mode, waiting.ExternalID, attest)
			if err != nil || verdict != domain.VerdictAccept {
				t.Fatalf("confirm = %+v, %q, %v", order, verdict, err)
			}
			detail := reviewTestDetail(t, n, waiting.ExternalID)
			wantStatus := domain.OrderStatusCommitted
			if mode == domain.SubmitModeImmediate {
				wantStatus = domain.OrderStatusFilled
			}
			if order.Status != wantStatus || detail.Review != nil || !reflect.DeepEqual(order, detail.Order) || !reflect.DeepEqual(order.Lock, eng.submitLock) {
				t.Fatalf("accepted order = %+v, review=%+v, want %s without review and engine lock", order, detail.Review, wantStatus)
			}
			// Compare with a fresh accepted submit using the identical fake outcomes.
			baselineEngine := newFakeEngine()
			baselineEngine.submitLock, baselineEngine.submitOutcomes = eng.submitLock, eng.submitOutcomes
			baseline, _ := newTestNode(t, baselineEngine)
			t.Cleanup(func() { _ = baseline.Close() })
			input := waiting
			var accepted domain.Order
			if mode == domain.SubmitModeHold {
				accepted, err = baseline.SubmitOrder(context.Background(), input, domain.MissingAccountCreate, testCaller)
			} else {
				accepted, _, err = baseline.SubmitImmediate(context.Background(), input, domain.MissingAccountCreate, testCaller)
			}
			if err != nil {
				t.Fatalf("baseline submit: %v", err)
			}
			if order.Leaves != accepted.Leaves || order.ReservedQuantity != accepted.ReservedQuantity || !reflect.DeepEqual(order.Lock, accepted.Lock) {
				t.Fatalf("accepted state leaves/reserved/lock = %s/%s/%q, baseline %s/%s/%q", order.Leaves, order.ReservedQuantity, order.Lock, accepted.Leaves, accepted.ReservedQuantity, accepted.Lock)
			}
			balances, err := n.ListBalances(context.Background(), waiting.Account, "")
			if err != nil {
				t.Fatal(err)
			}
			baselineBalances, err := baseline.ListBalances(context.Background(), waiting.Account, "")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(balances, baselineBalances) {
				t.Fatalf("accepted balances = %+v, baseline %+v", balances, baselineBalances)
			}
			baselineDetail := reviewTestDetail(t, baseline, waiting.ExternalID)
			if len(detail.Events)-2 != len(baselineDetail.Events)-1 || len(detail.Trades) != len(baselineDetail.Trades) {
				t.Fatalf("accepted events/trades = %+v/%+v, baseline %+v/%+v", detail.Events, detail.Trades, baselineDetail.Events, baselineDetail.Trades)
			}
			for i, event := range detail.Events[2:] {
				want := baselineDetail.Events[i+1]
				if event.Payload.ExecutionReport != nil && want.Payload.ExecutionReport != nil {
					gotReport, wantReport := *event.Payload.ExecutionReport, *want.Payload.ExecutionReport
					gotReport.ExternalID, wantReport.ExternalID = "", ""
					if !reflect.DeepEqual(gotReport, wantReport) {
						t.Fatalf("confirmed report = %+v, baseline %+v", gotReport, wantReport)
					}
					event.Payload.ExecutionReport, want.Payload.ExecutionReport = nil, nil
				}
				if event.Type != want.Type || !reflect.DeepEqual(event.Payload, want.Payload) || event.Source != testCaller.Source || event.Principal != testCaller.Principal || event.Attestation == nil || event.Attestation.SignedEnvelope != "confirmed-"+string(event.Type) {
					t.Fatalf("confirmed event = %+v, baseline %+v; want attestation and caller", event, want)
				}
			}
			rows := reviewTestAudit(t, n, domain.AuditActionConfirmOrderReview)
			if len(rows) != 1 || rows[0].Verdict != domain.VerdictAccept || rows[0].OrderID != waiting.ExternalID.String() || rows[0].Detail.Code != domain.AuditDetailConfirmOrderReviewAccepted {
				t.Fatalf("confirm audit = %+v, want accept", rows)
			}
			if mode == domain.SubmitModeImmediate && len(reviewTestAudit(t, n, domain.AuditActionExecutionReport)) != 1 {
				t.Fatal("immediate confirm missing execution report audit")
			}
			if len(eng.reviewFlags) != 2 || !eng.reviewFlags[1].Confirmed || len(eng.submitCalls) != 2 || eng.submitCalls[1].ExternalID != waiting.ExternalID {
				t.Fatalf("confirm inputs flags=%+v, calls=%+v", eng.reviewFlags, eng.submitCalls)
			}
		})
	}
}

func TestLocalNode_OrderReviewRejectThenAccept(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			n, eng, waiting := newOrderReviewTestNode(t, mode)
			eng.submitReject = &domain.OrderReject{Code: "position_limit", Scope: "account", Policy: "position", Reason: "limit", Details: "over"}
			order, verdict, err := confirmReviewForTest(n, mode, waiting.ExternalID, nil)
			if err != nil || verdict != domain.VerdictReject || order.Status != domain.OrderStatusAwaitingReview || order.Leaves != waiting.Leaves || order.ReservedQuantity != waiting.ReservedQuantity {
				t.Fatalf("rejected confirm = %+v, %q, %v; want waiting unchanged", order, verdict, err)
			}
			detail := reviewTestDetail(t, n, waiting.ExternalID)
			if detail.Review == nil || detail.Review.Mode != mode || len(detail.Events) != 3 || detail.Events[2].Type != domain.OrderEventPreTradeRejected || !reflect.DeepEqual(detail.Events[2].Payload.Rejects, []domain.OrderReject{*eng.submitReject}) || detail.Events[2].Payload.RejectCode != eng.submitReject.Code {
				t.Fatalf("rejected review history = %+v", detail)
			}
			rows := reviewTestAudit(t, n, domain.AuditActionConfirmOrderReview)
			if len(rows) != 1 || rows[0].Verdict != domain.VerdictReject || rows[0].RejectCode != eng.submitReject.Code || rows[0].Detail.Code != domain.AuditDetailConfirmOrderReviewRejected {
				t.Fatalf("reject audit = %+v", rows)
			}
			account, _, err := n.realm.GetAccount(context.Background(), waiting.Account)
			if err != nil || !account.Blocked || len(reviewTestAudit(t, n, domain.AuditActionBlock)) != 1 {
				t.Fatalf("latched block not mirrored: %+v, %v", account, err)
			}
			eng.submitReject = nil
			if _, verdict, err := confirmReviewForTest(n, mode, waiting.ExternalID, nil); err != nil || verdict != domain.VerdictAccept {
				t.Fatalf("retry = %q, %v", verdict, err)
			}
		})
	}
}

func TestLocalNode_OrderReviewUnexpectedReview(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			n, eng, waiting := newOrderReviewTestNode(t, mode)
			eng.confirmedVerdict = domain.VerdictReview
			before := reviewTestDetail(t, n, waiting.ExternalID)
			if _, _, err := confirmReviewForTest(n, mode, waiting.ExternalID, nil); err == nil {
				t.Fatal("review under confirmed succeeded, want explicit error")
			}
			if after := reviewTestDetail(t, n, waiting.ExternalID); !reflect.DeepEqual(before, after) || len(reviewTestAudit(t, n, domain.AuditActionConfirmOrderReview)) != 0 || len(eng.execReportLeaves) != 0 {
				t.Fatalf("review under confirmed wrote state: %+v", after)
			}
		})
	}
}

func TestLocalNode_OrderReviewCancel(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			n, eng, waiting := newOrderReviewTestNode(t, mode)
			beforeCalls, beforeFlags, beforePreTrade := len(eng.submitCalls), len(eng.reviewFlags), eng.preTradeCalls
			order, err := n.CancelOrderReview(context.Background(), waiting.ExternalID, testCaller,
				func(context.Context, domain.OrderEvent) (domain.EventAttestation, bool, error) {
					return domain.EventAttestation{SignedEnvelope: "cancel-envelope"}, true, nil
				})
			if err != nil || order.Status != domain.OrderStatusCancelled || order.Leaves != "0" {
				t.Fatalf("cancel = %+v, %v", order, err)
			}
			detail := reviewTestDetail(t, n, waiting.ExternalID)
			if detail.Review != nil || len(detail.Events) != 3 || detail.Events[2].Type != domain.OrderEventCancelled || detail.Events[2].Source != testCaller.Source || detail.Events[2].Principal != testCaller.Principal || detail.Events[2].Attestation == nil {
				t.Fatalf("cancel history = %+v", detail)
			}
			if len(eng.submitCalls) != beforeCalls || len(eng.reviewFlags) != beforeFlags || len(eng.execReportLeaves) != 0 || eng.preTradeCalls != beforePreTrade {
				t.Fatal("cancel called engine trading operations")
			}
			rows := reviewTestAudit(t, n, domain.AuditActionCancelOrderReview)
			if len(rows) != 1 || rows[0].OrderID != "" || rows[0].Verdict != "" || rows[0].RejectCode != "" || rows[0].Detail.Code != domain.AuditDetailCancelOrderReview || rows[0].Detail.Params["order"] != waiting.ExternalID.String() {
				t.Fatalf("cancel audit = %+v", rows)
			}
		})
	}
}

func TestLocalNode_OrderReviewRefusals(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, scenario := range []string{"wrong mode", "after cancel", "after confirm"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				t.Parallel()
				n, eng, waiting := newOrderReviewTestNode(t, mode)
				if scenario == "after cancel" {
					if _, err := n.CancelOrderReview(context.Background(), waiting.ExternalID, testCaller, nil); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "after confirm" {
					if _, _, err := confirmReviewForTest(n, mode, waiting.ExternalID, nil); err != nil {
						t.Fatal(err)
					}
				}
				before := reviewTestDetail(t, n, waiting.ExternalID)
				beforeAudit, err := n.ListAudit(context.Background(), 100)
				if err != nil {
					t.Fatal(err)
				}
				calls := len(eng.submitCalls)
				preTradeCalls := eng.preTradeCalls
				confirmMode := mode
				if scenario == "wrong mode" {
					if mode == domain.SubmitModeHold {
						confirmMode = domain.SubmitModeImmediate
					} else {
						confirmMode = domain.SubmitModeHold
					}
				}
				if _, _, err := confirmReviewForTest(n, confirmMode, waiting.ExternalID, nil); !errors.Is(err, domain.ErrConflict) {
					t.Fatalf("confirm refusal = %v, want ErrConflict", err)
				}
				if scenario != "wrong mode" {
					if _, err := n.CancelOrderReview(context.Background(), waiting.ExternalID, testCaller, nil); !errors.Is(err, domain.ErrConflict) {
						t.Fatalf("cancel refusal = %v, want ErrConflict", err)
					}
				}
				afterAudit, err := n.ListAudit(context.Background(), 100)
				if err != nil {
					t.Fatal(err)
				}
				if after := reviewTestDetail(t, n, waiting.ExternalID); !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeAudit, afterAudit) || len(eng.submitCalls) != calls || eng.preTradeCalls != preTradeCalls {
					t.Fatalf("refusal changed state or called pre-trade: %+v", after)
				}
			})
		}
	}
}

func TestLocalNode_OrderReviewNonWaiting(t *testing.T) {
	t.Parallel()
	for _, status := range []domain.OrderStatus{
		domain.OrderStatusSubmitted, domain.OrderStatusAccepted, domain.OrderStatusRejected,
		domain.OrderStatusCommitted, domain.OrderStatusRolledBack, domain.OrderStatusFilled,
		domain.OrderStatusPartiallyFilled, domain.OrderStatusCancelled,
	} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			n, realm := newTestNode(t, eng)
			t.Cleanup(func() { _ = n.Close() })
			order, err := n.SubmitOrder(context.Background(), domain.Order{
				Account: "non-waiting", ExternalID: "non-waiting-order", BaseAsset: "AAPL", QuoteAsset: "USD",
				Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "100",
			}, domain.MissingAccountCreate, testCaller)
			if err != nil {
				t.Fatal(err)
			}
			if err := realm.UpdateOrderStatus(context.Background(), order.ExternalID, status); err != nil {
				t.Fatal(err)
			}
			before := reviewTestDetail(t, n, order.ExternalID)
			calls := eng.preTradeCalls
			for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
				if _, _, err := confirmReviewForTest(n, mode, order.ExternalID, nil); !errors.Is(err, domain.ErrConflict) {
					t.Fatalf("%s confirm on %s = %v, want ErrConflict", mode, status, err)
				}
			}
			if _, err := n.CancelOrderReview(context.Background(), order.ExternalID, testCaller, nil); !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("cancel on %s = %v, want ErrConflict", status, err)
			}
			if after := reviewTestDetail(t, n, order.ExternalID); !reflect.DeepEqual(before, after) || eng.preTradeCalls != calls || len(reviewTestAudit(t, n, domain.AuditActionConfirmOrderReview)) != 0 || len(reviewTestAudit(t, n, domain.AuditActionCancelOrderReview)) != 0 {
				t.Fatalf("non-waiting refusal wrote state or called pre-trade: %+v", after)
			}
		})
	}
}

func TestLocalNode_OrderReviewConcurrentDecisions(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, raceCancel := range []bool{false, true} {
			name := mode + "/double confirm"
			if raceCancel {
				name = mode + "/confirm cancel"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				n, eng, waiting := newOrderReviewTestNode(t, mode)
				start := make(chan struct{})
				results := make(chan error, 2)
				var ready sync.WaitGroup
				ready.Add(2)
				for i := 0; i < 2; i++ {
					go func(i int) {
						ready.Done()
						<-start
						if raceCancel && i == 1 {
							_, err := n.CancelOrderReview(context.Background(), waiting.ExternalID, testCaller, nil)
							results <- err
						} else {
							_, _, err := confirmReviewForTest(n, mode, waiting.ExternalID, nil)
							results <- err
						}
					}(i)
				}
				ready.Wait()
				close(start)
				var success, conflicts int
				for i := 0; i < 2; i++ {
					err := <-results
					if err == nil {
						success++
					} else if errors.Is(err, domain.ErrConflict) {
						conflicts++
					} else {
						t.Fatal(err)
					}
				}
				if success != 1 || conflicts != 1 || len(eng.submitCalls) > 2 || eng.preTradeCalls > 2 {
					t.Fatalf("concurrent decisions: success=%d conflicts=%d engine calls=%d", success, conflicts, len(eng.submitCalls))
				}
			})
		}
	}
}

func TestLocalNode_OrderReviewNilAttestor(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			n, _, waiting := newOrderReviewTestNode(t, mode)
			var err error
			if mode == domain.SubmitModeHold {
				_, _, err = n.ConfirmOrderReview(context.Background(), waiting.ExternalID, testCaller, nil)
			} else {
				_, _, err = n.ConfirmImmediateReview(context.Background(), waiting.ExternalID, testCaller, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range reviewTestDetail(t, n, waiting.ExternalID).Events {
				if event.Attestation != nil {
					t.Fatalf("nil attestor signed event: %+v", event)
				}
			}
		})
	}
}

func TestLocalNode_OrderReviewCancelFailures(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"store", "signer", "audit"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			n, eng, waiting := newOrderReviewTestNode(t, domain.SubmitModeHold)
			cause := errors.New("cancel review failure")
			var fatal error
			n.fatal = func(err error) { fatal = err }
			before := reviewTestDetail(t, n, waiting.ExternalID)
			original := n.realm
			var attest store.EventAttestor
			switch failure {
			case "store":
				n.realm = &failOrderSettlementRealm{RealmStore: original, err: cause}
			case "signer":
				attest = func(context.Context, domain.OrderEvent) (domain.EventAttestation, bool, error) {
					return domain.EventAttestation{}, false, cause
				}
			case "audit":
				n.realm = &failActionAuditRealm{RealmStore: original, action: domain.AuditActionCancelOrderReview, err: cause}
			}
			if _, err := n.CancelOrderReview(context.Background(), waiting.ExternalID, testCaller, attest); !errors.Is(err, cause) {
				t.Fatalf("cancel failure = %v, want cause", err)
			}
			n.realm = original
			after := reviewTestDetail(t, n, waiting.ExternalID)
			if failure == "audit" {
				if !errors.Is(fatal, cause) || !strings.Contains(fatal.Error(), "post-commit audit failure") || after.Review != nil || after.Order.Status != domain.OrderStatusCancelled {
					t.Fatalf("cancel audit failure = %v, state=%+v", fatal, after)
				}
			} else if fatal != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("cancel store/signing failure = %v, state=%+v", fatal, after)
			}
			if eng.preTradeCalls != 1 || len(eng.execReportLeaves) != 0 {
				t.Fatal("cancel failure called engine")
			}
		})
	}
}

type failImmediateReviewSettleEngine struct {
	*verdictAfterReservationEngine
	err error
}

func (e *failImmediateReviewSettleEngine) SettleImmediate(
	_ domain.Order, _ engine.ImmediatePreparation, _ pretrade.PostTradeResult,
) (engine.ImmediateResult, error) {
	return engine.ImmediateResult{}, e.err
}

func TestLocalNode_OrderReviewImmediatePostCommitFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"execution report", "settle"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			n, eng, waiting := newOrderReviewTestNode(t, domain.SubmitModeImmediate)
			before := reviewTestDetail(t, n, waiting.ExternalID)
			beforeBalances, err := n.ListBalances(context.Background(), waiting.Account, "")
			if err != nil {
				t.Fatal(err)
			}
			cause := errors.New("immediate review settle failed")
			if failure == "execution report" {
				eng.failExecReport = true
			} else {
				n.engine = &failImmediateReviewSettleEngine{verdictAfterReservationEngine: eng, err: cause}
			}
			var fatals []error
			n.fatal = func(err error) { fatals = append(fatals, err) }
			_, _, err = n.ConfirmImmediateReview(context.Background(), waiting.ExternalID, testCaller, nil)
			if len(fatals) != 1 {
				t.Fatalf("fatal calls = %d, want exactly one; err=%v", len(fatals), err)
			}
			for _, got := range []error{err, fatals[0]} {
				if got == nil || !strings.Contains(got.Error(), "fill is unsettled") || !strings.Contains(got.Error(), "reservation committed") {
					t.Fatalf("post-commit failure = %v, want reconciliation cause", got)
				}
				if failure == "settle" && !strings.Contains(got.Error(), cause.Error()) {
					t.Fatalf("settle failure = %v, want reconciliation text with cause", got)
				}
			}
			if after := reviewTestDetail(t, n, waiting.ExternalID); !reflect.DeepEqual(before, after) || after.Order.Status != domain.OrderStatusAwaitingReview || after.Review == nil {
				t.Fatalf("post-commit failure changed waiting order: %+v", after)
			}
			afterBalances, err := n.ListBalances(context.Background(), waiting.Account, "")
			if err != nil || !reflect.DeepEqual(beforeBalances, afterBalances) {
				t.Fatalf("post-commit balances = %+v, err=%v; want %+v", afterBalances, err, beforeBalances)
			}
			if rows := reviewTestAudit(t, n, domain.AuditActionConfirmOrderReview); len(rows) != 0 {
				t.Fatalf("failed confirmation audit = %+v, want none", rows)
			}
		})
	}
}

type staleOrderReviewRealm struct {
	store.RealmStore
	reads int
	moved domain.OrderDetail
}

func (r *staleOrderReviewRealm) GetOrder(ctx context.Context, order domain.ExternalID) (domain.OrderDetail, error) {
	detail, err := r.RealmStore.GetOrder(ctx, order)
	if err != nil {
		return domain.OrderDetail{}, err
	}
	r.reads++
	if r.reads == 2 {
		// Return the waiting snapshot to the chain after the real store moved on.
		_, err = r.RecordOrderSettlement(ctx, domain.OrderSettlement{
			Account: detail.Order.Account, Order: order,
			AllowedFrom: []domain.OrderStatus{domain.OrderStatusAwaitingReview},
			OrderStatus: domain.OrderStatusCommitted, Leaves: detail.Order.AmountValue, ClearReview: true,
			Events: []domain.OrderEvent{fillSettlementEvent(order, domain.OrderEventCommitted, testCaller, domain.OrderEventPayload{})},
		})
		if err != nil {
			return domain.OrderDetail{}, err
		}
		r.moved, err = r.RealmStore.GetOrder(ctx, order)
	}
	return detail, err
}

func TestLocalNode_OrderReviewAllowedFrom(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate, "cancel"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			mode := operation
			if operation == "cancel" {
				mode = domain.SubmitModeHold
			}
			n, _, waiting := newOrderReviewTestNode(t, mode)
			original := n.realm
			wrapper := &staleOrderReviewRealm{RealmStore: original}
			n.realm = wrapper
			var fatals []error
			n.fatal = func(err error) { fatals = append(fatals, err) }
			var err error
			if operation == "cancel" {
				_, err = n.CancelOrderReview(context.Background(), waiting.ExternalID, testCaller, nil)
				if !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "record order review cancellation") {
					t.Fatalf("cancel guard error = %v, want settlement conflict", err)
				}
				if len(fatals) != 0 {
					t.Fatalf("cancel guard called fatal: %v", fatals)
				}
			} else {
				_, _, err = confirmReviewForTest(n, mode, waiting.ExternalID, nil)
				if len(fatals) != 1 {
					t.Fatalf("confirm guard fatal calls = %d, want one; err=%v", len(fatals), err)
				}
			}
			if err == nil {
				t.Fatal("settlement accepted stale awaiting_review state, want refusal")
			}
			n.realm = original
			if after := reviewTestDetail(t, n, waiting.ExternalID); wrapper.moved.Order.Status != domain.OrderStatusCommitted || !reflect.DeepEqual(wrapper.moved, after) {
				t.Fatalf("refused settlement changed store: before=%+v, after=%+v", wrapper.moved, after)
			}
			if rows := reviewTestAudit(t, n, domain.AuditActionConfirmOrderReview); len(rows) != 0 {
				t.Fatalf("refused confirmation audit = %+v", rows)
			}
			if rows := reviewTestAudit(t, n, domain.AuditActionCancelOrderReview); len(rows) != 0 {
				t.Fatalf("refused cancellation audit = %+v", rows)
			}
		})
	}
}

type cancelledOrderReviewRealm struct {
	store.RealmStore
	cancel    context.CancelFunc
	committed bool
}

func (r *cancelledOrderReviewRealm) RecordOrderSettlement(ctx context.Context, settlement domain.OrderSettlement) (domain.ExternalID, error) {
	id, err := r.RealmStore.RecordOrderSettlement(ctx, settlement)
	if err == nil {
		r.committed = true
		r.cancel()
	}
	return id, err
}

func (r *cancelledOrderReviewRealm) GetOrder(ctx context.Context, order domain.ExternalID) (domain.OrderDetail, error) {
	if r.committed {
		return domain.OrderDetail{}, errors.New("unexpected read after review cancellation")
	}
	return r.RealmStore.GetOrder(ctx, order)
}

func (r *cancelledOrderReviewRealm) AppendAudit(ctx context.Context, entry store.AuditEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.RealmStore.AppendAudit(ctx, entry)
}

func TestLocalNode_OrderReviewCancelRequestCancelled(t *testing.T) {
	t.Parallel()
	n, _, waiting := newOrderReviewTestNode(t, domain.SubmitModeHold)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := n.realm
	n.realm = &cancelledOrderReviewRealm{RealmStore: original, cancel: cancel}
	var fatals []error
	n.fatal = func(err error) { fatals = append(fatals, err) }
	order, err := n.CancelOrderReview(ctx, waiting.ExternalID, testCaller, nil)
	if err != nil || ctx.Err() != context.Canceled || order.Status != domain.OrderStatusCancelled || order.Leaves != "0" {
		t.Fatalf("cancelled request result = %+v, err=%v, ctx=%v", order, err, ctx.Err())
	}
	if len(fatals) != 0 {
		t.Fatalf("cancelled request called fatal: %v", fatals)
	}
	n.realm = original
	if detail := reviewTestDetail(t, n, waiting.ExternalID); detail.Review != nil || !reflect.DeepEqual(order, detail.Order) {
		t.Fatalf("cancelled request store = %+v, returned=%+v", detail, order)
	}
	if rows := reviewTestAudit(t, n, domain.AuditActionCancelOrderReview); len(rows) != 1 {
		t.Fatalf("cancelled request audit = %+v, want one row", rows)
	}
}

type panicOrderReviewRealm struct {
	store.RealmStore
	audit bool
}

func (r *panicOrderReviewRealm) RecordOrderSettlement(ctx context.Context, settlement domain.OrderSettlement) (domain.ExternalID, error) {
	if !r.audit {
		panic("review settlement panic")
	}
	return r.RealmStore.RecordOrderSettlement(ctx, settlement)
}

func (r *panicOrderReviewRealm) AppendAudit(ctx context.Context, entry store.AuditEntry) error {
	if r.audit && entry.Action == domain.AuditActionConfirmOrderReview {
		panic("review audit panic")
	}
	return r.RealmStore.AppendAudit(ctx, entry)
}

func TestLocalNode_OrderReviewPostCommitPanic(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, step := range []string{"settlement", "audit"} {
			t.Run(mode+"/"+step, func(t *testing.T) {
				t.Parallel()
				n, _, waiting := newOrderReviewTestNode(t, mode)
				before := reviewTestDetail(t, n, waiting.ExternalID)
				original := n.realm
				n.realm = &panicOrderReviewRealm{RealmStore: original, audit: step == "audit"}
				var fatals []error
				n.fatal = func(err error) { fatals = append(fatals, err) }
				_, _, err := confirmReviewForTest(n, mode, waiting.ExternalID, nil)
				if len(fatals) != 1 {
					t.Fatalf("fatal calls = %d, want exactly one; err=%v", len(fatals), err)
				}
				cause := "review " + step + " panic"
				if err == nil || !strings.Contains(err.Error(), cause) || !strings.Contains(fatals[0].Error(), cause) {
					t.Fatalf("panic error=%v fatal=%v, want %q", err, fatals[0], cause)
				}
				n.realm = original
				after := reviewTestDetail(t, n, waiting.ExternalID)
				if step == "settlement" {
					if !reflect.DeepEqual(before, after) || after.Order.Status != domain.OrderStatusAwaitingReview || after.Review == nil {
						t.Fatalf("settlement panic changed waiting order: %+v", after)
					}
					if !strings.Contains(fatals[0].Error(), "post-engine persistence failure") {
						t.Fatalf("settlement panic fatal = %v, want persistence failure", fatals[0])
					}
				} else {
					if after.Review != nil || after.Order.Status == domain.OrderStatusAwaitingReview {
						t.Fatalf("audit panic lost accepted settlement: %+v", after)
					}
					if !strings.Contains(fatals[0].Error(), "post-commit audit failure") {
						t.Fatalf("audit panic fatal = %v, want audit failure", fatals[0])
					}
				}
				if rows := reviewTestAudit(t, n, domain.AuditActionConfirmOrderReview); len(rows) != 0 {
					t.Fatalf("panic confirmation audit = %+v, want none", rows)
				}
			})
		}
	}
}

func TestLocalNode_OrderReviewFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, failure := range []string{"store accept", "store reject", "audit accept", "audit reject", "signer accept", "signer reject"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				t.Parallel()
				n, eng, waiting := newOrderReviewTestNode(t, mode)
				cause := errors.New("review decision failure")
				var fatals []error
				n.fatal = func(err error) { fatals = append(fatals, err) }
				original := n.realm
				if failure == "store accept" || failure == "store reject" {
					n.realm = &failOrderSettlementRealm{RealmStore: original, err: cause}
				}
				if failure == "audit accept" || failure == "audit reject" {
					n.realm = &failActionAuditRealm{RealmStore: original, action: domain.AuditActionConfirmOrderReview, err: cause}
				}
				if failure == "store reject" || failure == "audit reject" || failure == "signer reject" {
					eng.submitReject = &domain.OrderReject{Code: "rejected"}
				}
				var attest store.EventAttestor
				if failure == "signer accept" || failure == "signer reject" {
					attest = func(context.Context, domain.OrderEvent) (domain.EventAttestation, bool, error) {
						return domain.EventAttestation{}, false, cause
					}
				}
				_, _, err := confirmReviewForTest(n, mode, waiting.ExternalID, attest)
				if len(fatals) != 1 || !errors.Is(err, cause) || !errors.Is(fatals[0], cause) {
					t.Fatalf("failure=%v fatals=%v, want one fatal with wrapped cause", err, fatals)
				}
				n.realm = original
				detail := reviewTestDetail(t, n, waiting.ExternalID)
				if failure == "audit accept" {
					if detail.Review != nil {
						t.Fatal("audit failure lost successful settlement")
					}
				} else if detail.Review == nil || detail.Order.Status != domain.OrderStatusAwaitingReview {
					t.Fatalf("failed persistence changed waiting state: %+v", detail)
				}
			})
		}
	}
}
