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
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"go.openpit.dev/openpit/pretrade"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

func TestStoredHeldReservationQuantity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		quantity string
		wantErr  bool
	}{
		{"empty", "", true},
		{"zero", "0", true},
		{"decimal zero", "0.0", true},
		{"negative", "-1", true},
		{"invalid", "abc", true},
		{"positive", "1.5", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order := domain.Order{ReservedQuantity: tc.quantity, Lock: []byte("lock")}
			held, err := storedHeldReservation(order)
			if tc.wantErr {
				if err == nil || !strings.HasPrefix(err.Error(), "held order review has no reserved quantity") {
					t.Fatalf("storedHeldReservation error = %v, want quantity error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if held.Quantity != tc.quantity || !reflect.DeepEqual(held.Lock, order.Lock) {
				t.Fatalf("storedHeldReservation = %+v, want quantity %q and lock %q", held, tc.quantity, order.Lock)
			}
		})
	}
}

func newOrderReviewTestNode(t *testing.T, mode string, held ...bool) (*localNode, *verdictAfterReservationEngine, domain.Order) {
	t.Helper()
	base := newFakeEngine()
	n, _ := newTestNode(t, base)
	t.Cleanup(func() { _ = n.Close() })
	eng := &verdictAfterReservationEngine{
		fakeEngine: base, verdict: domain.VerdictReview, confirmedVerdict: domain.VerdictAccept,
		reviews: []domain.ReviewMark{{OrderReject: domain.OrderReject{Code: "order_size_limit"}, Mode: domain.RuleModeReviewWithoutHold}},
	}
	if len(held) > 0 && held[0] {
		eng.held = true
		eng.reviews[0].Mode = domain.RuleModeReviewWithHold
		base.submitLock = fakeStoredLock(t)
		base.submitSettlementLockPrice = "100"
		base.submitOutcomes = []engine.BalanceOutcome{
			{Asset: "AAPL", Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "1", IncomingResult: "1", BalanceResult: "0", HeldResult: "0"}},
			{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{HeldDelta: "100", HeldResult: "100", BalanceResult: "900", IncomingResult: "0"}},
		}
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
		var result engine.ImmediateResult
		order, result, err = n.SubmitImmediate(context.Background(), order, domain.MissingAccountCreate, testCaller)
		if eng.held && err == nil && (!reflect.DeepEqual(result.Lock, eng.submitLock) || result.SettlementLockPrice != "100" || result.ExecutionReport != nil) {
			t.Fatalf("held immediate result lost lock estimate or executed: %+v", result)
		}
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

func TestLocalNode_HeldReviewSubmitAndConfirm(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		t.Run(mode, func(t *testing.T) {
			n, eng, waiting := newOrderReviewTestNode(t, mode, true)
			ctx := context.Background()
			detail := reviewTestDetail(t, n, waiting.ExternalID)
			if waiting.Status != domain.OrderStatusAwaitingReview || detail.Review == nil || !detail.Review.Held || waiting.Leaves != "1" || waiting.ReservedQuantity != "1" || !reflect.DeepEqual(waiting.Lock, eng.submitLock) {
				t.Fatalf("held submit lost reservation: %+v, review=%+v", waiting, detail.Review)
			}
			if len(eng.execReportLeaves) != 0 || len(detail.Trades) != 0 || len(detail.Events) != 2 || detail.Events[1].Type != domain.OrderEventPreTradeReview {
				t.Fatalf("held submit executed or lost review event: %+v", detail)
			}
			rows := reviewTestAudit(t, n, domain.AuditActionSubmitOrder)
			if len(rows) != 1 || rows[0].Verdict != domain.VerdictReview || rows[0].RejectCode != eng.reviews[0].Code {
				t.Fatalf("held submit audit = %+v", rows)
			}
			before, err := n.ListBalances(ctx, waiting.Account, "")
			if err != nil || len(before) != 2 {
				t.Fatalf("held balances = %+v, %v", before, err)
			}
			for _, balance := range before {
				if balance.Asset == "USD" && (balance.Held != "100" || balance.Available != "900") {
					t.Fatalf("held USD balance = %+v", balance)
				}
			}
			if mode == domain.SubmitModeImmediate {
				eng.submitOutcomes = []engine.BalanceOutcome{
					{Asset: "AAPL", Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "-1", IncomingResult: "0", BalanceResult: "1", HeldResult: "0"}},
					{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{HeldDelta: "-100", HeldResult: "0", BalanceResult: "900", IncomingResult: "0"}},
				}
			}
			confirmed, verdict, err := confirmReviewForTest(n, mode, waiting.ExternalID, nil)
			if err != nil || verdict != domain.VerdictAccept {
				t.Fatalf("held confirm = %+v, %s, %v", confirmed, verdict, err)
			}
			if !reflect.DeepEqual(eng.reviewFlags[1], engine.ReviewFlags{Confirmed: true, AlreadyHeld: true}) || len(eng.heldReservations) != 1 || !reflect.DeepEqual(eng.heldReservations[0], engine.HeldReservation{Lock: waiting.Lock, Quantity: waiting.ReservedQuantity}) {
				t.Fatalf("held rerun inputs flags=%+v, held=%+v", eng.reviewFlags, eng.heldReservations)
			}
			if mode == domain.SubmitModeHold {
				after, err := n.ListBalances(ctx, waiting.Account, "")
				if err != nil || confirmed.Status != domain.OrderStatusCommitted || confirmed.Leaves != waiting.Leaves || confirmed.ReservedQuantity != waiting.ReservedQuantity || !reflect.DeepEqual(confirmed.Lock, waiting.Lock) || !reflect.DeepEqual(after, before) {
					t.Fatalf("venue confirm rewrote hold: %+v, balances=%+v, %v", confirmed, after, err)
				}
			} else if confirmed.Status != domain.OrderStatusFilled || confirmed.ReservedQuantity != "0" || len(eng.execReportLeaves) != 1 {
				t.Fatalf("immediate confirm did not fill: %+v, reports=%+v", confirmed, eng.execReportLeaves)
			}
			if reviewTestDetail(t, n, waiting.ExternalID).Review != nil {
				t.Fatal("held confirm kept review row")
			}
		})
	}
}

func TestLocalNode_HeldReviewRejectedConfirm(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		t.Run(mode, func(t *testing.T) {
			n, eng, waiting := newOrderReviewTestNode(t, mode, true)
			eng.submitReject = &domain.OrderReject{Code: "position_limit", Scope: "order"}
			before, err := n.ListBalances(context.Background(), waiting.Account, "")
			if err != nil {
				t.Fatal(err)
			}
			got, verdict, err := confirmReviewForTest(n, mode, waiting.ExternalID, nil)
			after, listErr := n.ListBalances(context.Background(), waiting.Account, "")
			detail := reviewTestDetail(t, n, waiting.ExternalID)
			if err != nil || listErr != nil || verdict != domain.VerdictReject || !reflect.DeepEqual(got, waiting) || !reflect.DeepEqual(before, after) || detail.Review == nil || !detail.Review.Held {
				t.Fatalf("rejected confirm changed hold: %+v, %s, %v, balances=%+v, %v", got, verdict, err, after, listErr)
			}
		})
	}
}

type changedHeldReviewRealm struct {
	store.RealmStore
	reads          int
	zero           bool
	empty          bool
	noLock         bool
	noReview       bool
	notWaiting     bool
	changedInput   bool
	accountBlocked bool
	groupBlocked   bool
	groupMissing   bool
	groupErr       error
}

func (r *changedHeldReviewRealm) GetGroup(ctx context.Context, code string) (domain.AccountGroup, bool, error) {
	if r.groupMissing || r.groupErr != nil {
		return domain.AccountGroup{}, false, r.groupErr
	}
	return r.RealmStore.GetGroup(ctx, code)
}

func (r *changedHeldReviewRealm) GetOrder(ctx context.Context, id domain.ExternalID) (domain.OrderDetail, error) {
	detail, err := r.RealmStore.GetOrder(ctx, id)
	r.reads++
	if err == nil && r.reads == 2 {
		switch {
		case r.accountBlocked:
			err = r.SetAccountBlocked(ctx, detail.Order.Account, true, "risk")
		case r.groupBlocked:
			_, err = r.CreateGroup(ctx, domain.AccountGroup{Code: "blocked-desk", Blocked: true, BlockReason: "risk"})
			if err == nil {
				err = r.SetAccountGroup(ctx, detail.Order.Account, "blocked-desk")
			}
		case r.notWaiting:
			detail.Order.Status = domain.OrderStatusCommitted
		case r.changedInput:
			detail.Order.Price = "101"
		case r.zero:
			detail.Order.ReservedQuantity = "0"
		case r.empty:
			detail.Order.ReservedQuantity = ""
		case r.noLock:
			detail.Order.Lock = nil
		case r.noReview:
			detail.Review = nil
		default:
			detail.Review.Held = !detail.Review.Held
		}
	}
	return detail, err
}

func forceReviewForTest(
	n *localNode, mode string, id domain.ExternalID,
	attestFor func(domain.Order) store.EventAttestor,
) (domain.Order, domain.Verdict, error) {
	if mode == domain.SubmitModeHold {
		order, result, err := n.ForceOrderReview(context.Background(), id, testCaller,
			func(order domain.Order, _ engine.OrderResult) store.EventAttestor {
				return attestFor(order)
			})
		return order, result.Verdict, err
	}
	order, result, err := n.ForceImmediateReview(context.Background(), id, testCaller,
		func(order domain.Order, _ engine.ImmediateResult) store.EventAttestor {
			return attestFor(order)
		})
	return order, result.Verdict, err
}

func TestLocalNode_HeldReviewForce(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		t.Run(mode, func(t *testing.T) {
			n, eng, waiting := newOrderReviewTestNode(t, mode, true)
			before, err := n.ListBalances(context.Background(), waiting.Account, "")
			if err != nil {
				t.Fatal(err)
			}
			if mode == domain.SubmitModeImmediate {
				eng.submitOutcomes = []engine.BalanceOutcome{
					{Asset: "AAPL", Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "-1", IncomingResult: "0", BalanceResult: "1", HeldResult: "0"}},
					{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{HeldDelta: "-100", HeldResult: "0", BalanceResult: "900", IncomingResult: "0"}},
				}
			}
			calls, models := eng.preTradeCalls, len(eng.reviewFlags)
			var builds int
			var attested []domain.OrderEvent
			forced, verdict, err := forceReviewForTest(n, mode, waiting.ExternalID,
				func(order domain.Order) store.EventAttestor {
					builds++
					if !reflect.DeepEqual(order, waiting) {
						t.Errorf("attestor order = %+v; want waiting order", order)
					}
					return func(_ context.Context, event domain.OrderEvent) (domain.EventAttestation, bool, error) {
						attested = append(attested, event)
						return domain.EventAttestation{SignedEnvelope: "force-envelope"}, true, nil
					}
				})
			if err != nil || verdict != domain.VerdictAccept {
				t.Fatalf("force = %+v, %s, %v", forced, verdict, err)
			}
			if eng.preTradeCalls != calls || len(eng.reviewFlags) != models || len(eng.heldReservations) != 1 || !reflect.DeepEqual(eng.heldReservations[0], engine.HeldReservation{Lock: waiting.Lock, Quantity: waiting.ReservedQuantity}) {
				t.Fatalf("force reran pre-trade or lost hold: calls=%d flags=%+v held=%+v", eng.preTradeCalls, eng.reviewFlags, eng.heldReservations)
			}
			detail := reviewTestDetail(t, n, waiting.ExternalID)
			if detail.Review != nil || builds != 1 || len(attested) < 2 || attested[0].Type != domain.OrderEventPreTradeAccepted || attested[1].Type != domain.OrderEventCommitted || detail.Events[2].Attestation == nil || detail.Events[2].Attestation.SignedEnvelope != "force-envelope" {
				t.Fatalf("force review or attestation: %+v, builds=%d events=%+v", detail, builds, attested)
			}
			if mode == domain.SubmitModeHold {
				after, err := n.ListBalances(context.Background(), waiting.Account, "")
				if err != nil || forced.Status != domain.OrderStatusCommitted || forced.Leaves != waiting.Leaves || forced.ReservedQuantity != waiting.ReservedQuantity || !reflect.DeepEqual(forced.Lock, waiting.Lock) || !reflect.DeepEqual(before, after) || len(eng.execReportLeaves) != 0 || len(detail.Trades) != 0 || len(attested) != 2 {
					t.Fatalf("force hold changed reservation: %+v, balances=%+v, %v", forced, after, err)
				}
			} else {
				if forced.Status != domain.OrderStatusFilled || forced.Leaves != "0" || forced.ReservedQuantity != "0" || !reflect.DeepEqual(forced.Lock, waiting.Lock) || len(eng.execReportLeaves) != 1 || len(detail.Trades) != 1 || detail.Trades[0].Quantity != waiting.ReservedQuantity || detail.Trades[0].Price != waiting.Price || len(reviewTestAudit(t, n, domain.AuditActionExecutionReport)) != 1 {
					t.Fatalf("force immediate did not fill held quantity: %+v, detail=%+v", forced, detail)
				}
				if len(eng.execReportModels) != 1 {
					t.Fatalf("force sent %d reports; want one", len(eng.execReportModels))
				}
				fill, ok := eng.execReportModels[0].Fill().Get()
				if !ok {
					t.Fatal("forced report has no fill")
				}
				trade, ok := fill.LastTrade().Get()
				if !ok || trade.Quantity().String() != waiting.ReservedQuantity || trade.Price().String() != waiting.Price {
					t.Fatal("forced report lost stored quantity or trade price")
				}
				lock, err := pretrade.NewLockFromMsgPack(waiting.Lock)
				if err != nil || !reflect.DeepEqual(fill.Lock(), lock.Bytes()) || !reflect.DeepEqual(eng.execReportLeaves, []string{"0"}) {
					t.Fatalf("forced report lost stored lock or zero leaves: %v", err)
				}
			}
			rows := reviewTestAudit(t, n, domain.AuditActionForceOrderReview)
			if len(rows) != 1 || rows[0].OrderID != "" || rows[0].Verdict != "" || rows[0].RejectCode != "" || rows[0].Detail.Code != domain.AuditDetailForceOrderReview || rows[0].Detail.Params["order"] != waiting.ExternalID.String() {
				t.Fatalf("force audit = %+v", rows)
			}
			_, _, err = forceReviewForTest(n, mode, waiting.ExternalID,
				func(domain.Order) store.EventAttestor { t.Error("second force built attestor"); return nil })
			if !errors.Is(err, domain.ErrConflict) || !reflect.DeepEqual(detail, reviewTestDetail(t, n, waiting.ExternalID)) || len(reviewTestAudit(t, n, domain.AuditActionForceOrderReview)) != 1 {
				t.Fatalf("second force changed state: %v", err)
			}
		})
	}
}

func TestLocalNode_HeldReviewForceGroupRead(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, scenario := range []string{"missing", "error"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				n, eng, waiting := newOrderReviewTestNode(t, mode, true)
				before := reviewTestDetail(t, n, waiting.ExternalID)
				cause := fmt.Errorf("group read failed: %w", domain.ErrNotFound)
				original := n.realm
				realm := &changedHeldReviewRealm{
					RealmStore: original, groupBlocked: true,
					groupMissing: scenario == "missing",
				}
				if scenario == "error" {
					realm.groupErr = cause
				}
				n.realm = realm
				calls, models := eng.preTradeCalls, len(eng.reviewFlags)
				var builds int
				forced, verdict, err := forceReviewForTest(n, mode, waiting.ExternalID,
					func(domain.Order) store.EventAttestor { builds++; return nil })
				n.realm = original
				after := reviewTestDetail(t, n, waiting.ExternalID)
				if scenario == "error" {
					if err == nil || !strings.Contains(err.Error(), "read force account group") || !errors.Is(err, cause) || errors.Is(err, domain.ErrConflict) {
						t.Fatalf("force group read error = %v; want wrapped read failure", err)
					}
					if builds != 0 || eng.preTradeCalls != calls || len(eng.reviewFlags) != models || len(eng.heldReservations) != 0 || len(eng.execReportLeaves) != 0 || !reflect.DeepEqual(before, after) || len(reviewTestAudit(t, n, domain.AuditActionForceOrderReview)) != 0 || len(reviewTestAudit(t, n, domain.AuditActionExecutionReport)) != 0 {
						t.Fatal("group read failure changed waiting order, audited or called engine/attestor")
					}
					return
				}
				wantStatus := domain.OrderStatusCommitted
				if mode == domain.SubmitModeImmediate {
					wantStatus = domain.OrderStatusFilled
				}
				if err != nil || verdict != domain.VerdictAccept || forced.Status != wantStatus || after.Review != nil || builds != 1 || eng.preTradeCalls != calls || len(eng.reviewFlags) != models || len(eng.heldReservations) != 1 || len(reviewTestAudit(t, n, domain.AuditActionForceOrderReview)) != 1 {
					t.Fatalf("force with missing group = %+v, %s, %v", forced, verdict, err)
				}
			})
		}
	}
}

func TestLocalNode_HeldReviewForceRequestCancelled(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		t.Run(mode, func(t *testing.T) {
			n, _, waiting := newOrderReviewTestNode(t, mode, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := n.realm
			n.realm = &cancelledOrderReviewRealm{
				RealmStore: original, cancel: cancel, allowReadBack: true,
			}
			var fatals []error
			n.fatal = func(err error) { fatals = append(fatals, err) }
			var order domain.Order
			var err error
			wantStatus := domain.OrderStatusCommitted
			if mode == domain.SubmitModeHold {
				order, _, err = n.ForceOrderReview(ctx, waiting.ExternalID, testCaller,
					nil)
			} else {
				wantStatus = domain.OrderStatusFilled
				order, _, err = n.ForceImmediateReview(ctx, waiting.ExternalID, testCaller,
					nil)
			}
			if err != nil || ctx.Err() != context.Canceled || order.Status != wantStatus || len(fatals) != 0 {
				t.Fatalf("cancelled force request: order=%+v err=%v ctx=%v fatals=%v", order, err, ctx.Err(), fatals)
			}
			n.realm = original
			detail := reviewTestDetail(t, n, waiting.ExternalID)
			if detail.Review != nil || !reflect.DeepEqual(order, detail.Order) || len(reviewTestAudit(t, n, domain.AuditActionForceOrderReview)) != 1 {
				t.Fatalf("cancelled force request lost settlement or audit: %+v", detail)
			}
		})
	}
}

func TestLocalNode_HeldReviewForceRefusals(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, scenario := range []string{"not held", "not waiting", "changed held", "mode", "zero", "empty", "lock", "row", "input", "account blocked", "group blocked"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				storedMode := mode
				if scenario == "mode" {
					storedMode = domain.SubmitModeImmediate
					if mode == domain.SubmitModeImmediate {
						storedMode = domain.SubmitModeHold
					}
				}
				n, eng, waiting := newOrderReviewTestNode(t, storedMode, scenario != "not held")
				before := reviewTestDetail(t, n, waiting.ExternalID)
				balances, err := n.ListBalances(context.Background(), waiting.Account, "")
				if err != nil {
					t.Fatal(err)
				}
				original := n.realm
				if scenario != "not held" && scenario != "mode" {
					n.realm = &changedHeldReviewRealm{
						RealmStore: original, zero: scenario == "zero", empty: scenario == "empty",
						accountBlocked: scenario == "account blocked", groupBlocked: scenario == "group blocked",
						noLock: scenario == "lock", noReview: scenario == "row",
						notWaiting: scenario == "not waiting", changedInput: scenario == "input",
					}
				}
				calls, models := eng.preTradeCalls, len(eng.reviewFlags)
				_, _, err = forceReviewForTest(n, mode, waiting.ExternalID,
					func(domain.Order) store.EventAttestor { t.Error("refused force built attestor"); return nil })
				n.realm = original
				want := map[string]string{"zero": "no reserved quantity", "empty": "no reserved quantity", "lock": "no lock", "row": "no review row", "input": "does not match chain input"}[scenario]
				if err == nil || (want != "" && !strings.Contains(err.Error(), want)) || (want == "" && !errors.Is(err, domain.ErrConflict)) {
					t.Fatalf("force %s error = %v; want explicit refusal %q", scenario, err, want)
				}
				if strings.HasSuffix(scenario, " blocked") {
					tier := strings.TrimSuffix(scenario, " blocked")
					if !strings.Contains(err.Error(), string(waiting.Account)) || !strings.Contains(err.Error(), tier+" tier") {
						t.Fatalf("force block error lacks account or tier: %v", err)
					}
				}
				afterBalances, listErr := n.ListBalances(context.Background(), waiting.Account, "")
				if listErr != nil || eng.preTradeCalls != calls || len(eng.reviewFlags) != models || len(eng.heldReservations) != 0 || len(eng.execReportLeaves) != 0 || !reflect.DeepEqual(before, reviewTestDetail(t, n, waiting.ExternalID)) || !reflect.DeepEqual(balances, afterBalances) || len(reviewTestAudit(t, n, domain.AuditActionForceOrderReview)) != 0 || len(reviewTestAudit(t, n, domain.AuditActionExecutionReport)) != 0 {
					t.Fatal("refused force changed state, audited or called engine")
				}
			})
		}
	}
}

func TestLocalNode_HeldReviewForceConcurrentDecisions(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, other := range []string{"force", "confirm", "cancel"} {
			t.Run(mode+"/"+other, func(t *testing.T) {
				n, _, waiting := newOrderReviewTestNode(t, mode, true)
				start := make(chan struct{})
				results := make(chan error, 2)
				for i := 0; i < 2; i++ {
					go func(i int) {
						<-start
						var err error
						if i == 1 && other == "cancel" {
							_, err = n.CancelOrderReview(context.Background(), waiting.ExternalID, testCaller, nil)
						} else if i == 1 && other == "confirm" {
							_, _, err = confirmReviewForTest(n, mode, waiting.ExternalID, nil)
						} else {
							_, _, err = forceReviewForTest(n, mode, waiting.ExternalID,
								func(domain.Order) store.EventAttestor { return nil })
						}
						results <- err
					}(i)
				}
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
				if success != 1 || conflicts != 1 {
					t.Fatalf("concurrent force/%s: successes=%d conflicts=%d", other, success, conflicts)
				}
				audits := len(reviewTestAudit(t, n, domain.AuditActionForceOrderReview)) +
					len(reviewTestAudit(t, n, domain.AuditActionConfirmOrderReview)) +
					len(reviewTestAudit(t, n, domain.AuditActionCancelOrderReview))
				if audits != 1 || reviewTestDetail(t, n, waiting.ExternalID).Review != nil {
					t.Fatalf("concurrent force duplicated settlement: audits=%d", audits)
				}
			})
		}
	}
}

func TestLocalNode_HeldReviewForceAllowedFrom(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, failure := range []string{"stale", "write"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				n, _, waiting := newOrderReviewTestNode(t, mode, true)
				before := reviewTestDetail(t, n, waiting.ExternalID)
				original := n.realm
				wrapper := &staleOrderReviewRealm{RealmStore: original}
				n.realm = wrapper
				if failure == "write" {
					n.realm = &failOrderSettlementRealm{RealmStore: original, err: errors.New("force write failed")}
				}
				var fatals []error
				n.fatal = func(err error) { fatals = append(fatals, err) }
				_, _, err := forceReviewForTest(n, mode, waiting.ExternalID,
					func(domain.Order) store.EventAttestor { return nil })
				n.realm = original
				wantFatals := 0
				if mode == domain.SubmitModeImmediate {
					wantFatals = 1
				}
				if mode == domain.SubmitModeHold && (err == nil || !strings.Contains(err.Error(), "record force order review:")) {
					t.Fatalf("force write error lacks operation context: %v", err)
				}
				want := before
				if failure == "stale" {
					want = wrapper.moved
				}
				if err == nil || len(fatals) != wantFatals || !reflect.DeepEqual(want, reviewTestDetail(t, n, waiting.ExternalID)) || len(reviewTestAudit(t, n, domain.AuditActionForceOrderReview)) != 0 || len(reviewTestAudit(t, n, domain.AuditActionExecutionReport)) != 0 {
					t.Fatalf("force settlement failure changed state or fatal boundary: err=%v fatals=%v", err, fatals)
				}
			})
		}
	}
}

func TestLocalNode_HeldReviewForceImmediateFailure(t *testing.T) {
	for _, failure := range []string{"prepare", "report", "settle"} {
		t.Run(failure, func(t *testing.T) {
			n, eng, waiting := newOrderReviewTestNode(t, domain.SubmitModeImmediate, true)
			before := reviewTestDetail(t, n, waiting.ExternalID)
			cause := errors.New("forced immediate settle failed")
			if failure == "report" {
				eng.failExecReport = true
			} else {
				n.engine = &failImmediateReviewSettleEngine{
					verdictAfterReservationEngine: eng, err: cause, prepare: failure == "prepare",
				}
			}
			var fatals []error
			n.fatal = func(err error) { fatals = append(fatals, err) }
			_, _, err := n.ForceImmediateReview(context.Background(), waiting.ExternalID, testCaller,
				func(domain.Order, engine.ImmediateResult) store.EventAttestor {
					t.Error("failed force built attestor")
					return nil
				})
			if err == nil {
				t.Fatal("failed force succeeded")
			}
			if failure != "prepare" {
				if len(fatals) != 1 || !strings.Contains(err.Error(), "fill is unsettled") || (failure == "settle" && !strings.Contains(err.Error(), cause.Error())) {
					t.Fatalf("entered report failure must be fatal and unsettled: err=%v fatals=%v", err, fatals)
				}
			} else if len(fatals) != 0 || !errors.Is(err, cause) || strings.Contains(err.Error(), "fill is unsettled") || len(eng.execReportLeaves) != 0 {
				t.Fatalf("prepare failure must leave a retryable hold: err=%v fatals=%v", err, fatals)
			}
			if !reflect.DeepEqual(before, reviewTestDetail(t, n, waiting.ExternalID)) || len(reviewTestAudit(t, n, domain.AuditActionForceOrderReview)) != 0 || len(reviewTestAudit(t, n, domain.AuditActionExecutionReport)) != 0 {
				t.Fatal("failed force changed waiting order or audit")
			}
		})
	}
}

func TestLocalNode_HeldReviewRefusals(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, scenario := range []string{"zero", "mismatch"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				n, eng, waiting := newOrderReviewTestNode(t, mode, true)
				before := reviewTestDetail(t, n, waiting.ExternalID)
				rows := reviewTestAudit(t, n, domain.AuditActionConfirmOrderReview)
				original := n.realm
				n.realm = &changedHeldReviewRealm{RealmStore: original, zero: scenario == "zero"}
				_, _, err := confirmReviewForTest(n, mode, waiting.ExternalID, nil)
				n.realm = original
				if err == nil || (scenario != "zero" && !errors.Is(err, domain.ErrConflict)) || (scenario == "zero" && !strings.Contains(err.Error(), "no reserved quantity")) {
					t.Fatalf("held %s error = %v", scenario, err)
				}
				if len(eng.heldReservations) != 0 || !reflect.DeepEqual(before, reviewTestDetail(t, n, waiting.ExternalID)) || !reflect.DeepEqual(rows, reviewTestAudit(t, n, domain.AuditActionConfirmOrderReview)) {
					t.Fatal("held refusal changed state or called adapter")
				}
			})
		}
	}
}

func TestLocalNode_HeldReviewCancel(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		t.Run(mode, func(t *testing.T) {
			n, eng, waiting := newOrderReviewTestNode(t, mode, true)
			eng.execReportOutcomes = []engine.BalanceOutcome{
				{Asset: "AAPL", Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "-1", IncomingResult: "0", BalanceResult: "0", HeldResult: "0"}},
				{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{HeldDelta: "-100", HeldResult: "0", BalanceResult: "1000", IncomingResult: "0"}},
			}
			preTradeCalls := eng.preTradeCalls
			var attested []domain.OrderEvent
			cancelled, err := n.CancelOrderReview(context.Background(), waiting.ExternalID, testCaller,
				func(_ context.Context, event domain.OrderEvent) (domain.EventAttestation, bool, error) {
					attested = append(attested, event)
					return domain.EventAttestation{SignedEnvelope: "held-cancel-envelope"}, true, nil
				})
			if err != nil {
				t.Fatalf("held cancel: %v", err)
			}
			if cancelled.Status != domain.OrderStatusCancelled || cancelled.Leaves != "0" || cancelled.ReservedQuantity != "0" {
				t.Fatalf("held cancel order = %+v", cancelled)
			}
			if eng.preTradeCalls != preTradeCalls || len(eng.execReportCalls) != 1 || !reflect.DeepEqual(eng.execReportRemainders, []string{waiting.ReservedQuantity}) {
				t.Fatalf("held cancel engine calls: pre-trade=%d reports=%+v remainders=%v", eng.preTradeCalls, eng.execReportCalls, eng.execReportRemainders)
			}
			report := eng.execReportCalls[0]
			if report.OrderStatus != domain.OrderStatusCancelled || report.LeavesQuantity != "0" || !reflect.DeepEqual(report.Lock, waiting.Lock) {
				t.Fatalf("held cancel report = %+v", report)
			}
			detail := reviewTestDetail(t, n, waiting.ExternalID)
			if detail.Review != nil || detail.Order.Status != domain.OrderStatusCancelled || detail.Order.Leaves != "0" || detail.Order.ReservedQuantity != "0" || len(detail.Trades) != 0 {
				t.Fatalf("held cancel persisted state = %+v", detail)
			}
			if len(attested) != 1 || attested[0].Type != domain.OrderEventCancelled || attested[0].Payload.LeavesQuantity != "0" {
				t.Fatalf("held cancel attested events = %+v", attested)
			}
			if len(detail.Events) != 3 || detail.Events[2].Type != domain.OrderEventCancelled || detail.Events[2].Attestation == nil || detail.Events[2].Attestation.SignedEnvelope != "held-cancel-envelope" {
				t.Fatalf("held cancel history = %+v", detail.Events)
			}
			rows := reviewTestAudit(t, n, domain.AuditActionCancelOrderReview)
			if len(rows) != 1 || rows[0].Detail.Code != domain.AuditDetailCancelOrderReview || rows[0].Detail.Params["order"] != waiting.ExternalID.String() || len(reviewTestAudit(t, n, domain.AuditActionExecutionReport)) != 0 {
				t.Fatalf("held cancel audit = %+v", rows)
			}
		})
	}
}

func TestLocalNode_HeldReviewCancelRefusals(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, scenario := range []string{"zero", "empty", "mismatch", "lock", "row"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				n, eng, waiting := newOrderReviewTestNode(t, mode, true)
				before := reviewTestDetail(t, n, waiting.ExternalID)
				original := n.realm
				n.realm = &changedHeldReviewRealm{
					RealmStore: original, zero: scenario == "zero", empty: scenario == "empty",
					noLock: scenario == "lock", noReview: scenario == "row",
				}
				_, err := n.CancelOrderReview(context.Background(), waiting.ExternalID, testCaller, nil)
				n.realm = original
				want := map[string]string{
					"zero": "no reserved quantity", "empty": "no reserved quantity",
					"mismatch": "held flag changed", "lock": "no lock", "row": "no review row",
				}[scenario]
				if err == nil || !strings.Contains(err.Error(), want) || (scenario == "mismatch" && !errors.Is(err, domain.ErrConflict)) {
					t.Fatalf("held cancel %s error = %v", scenario, err)
				}
				if len(eng.execReportRemainders) != 0 || len(eng.execReportCalls) != 0 || !reflect.DeepEqual(before, reviewTestDetail(t, n, waiting.ExternalID)) {
					t.Fatal("held cancel refusal changed state or called engine")
				}
			})
		}
	}
}

func TestLocalNode_HeldReviewPublicGuards(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, operation := range []string{"cancel", "confirm", "report"} {
			t.Run(mode+"/"+operation, func(t *testing.T) {
				n, eng, waiting := newOrderReviewTestNode(t, mode, true)
				ctx := context.Background()
				before := reviewTestDetail(t, n, waiting.ExternalID)
				balances, err := n.ListBalances(ctx, waiting.Account, "")
				if err != nil {
					t.Fatal(err)
				}
				calls := eng.preTradeCalls
				switch operation {
				case "cancel":
					_, _, err = n.CancelOrder(ctx, waiting.ExternalID, "0", testCaller)
				case "confirm":
					_, err = n.ConfirmOrder(ctx, waiting.ExternalID, testCaller)
				case "report":
					_, err = n.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
						Order: waiting.ExternalID, Account: waiting.Account,
						BaseAsset: waiting.BaseAsset, QuoteAsset: waiting.QuoteAsset,
						Side: waiting.Side, OrderStatus: domain.OrderStatusCancelled, LeavesQuantity: "0",
					}, testCaller)
				}
				if !errors.Is(err, domain.ErrConflict) {
					t.Fatalf("public %s error = %v; want conflict", operation, err)
				}
				afterBalances, err := n.ListBalances(ctx, waiting.Account, "")
				if err != nil {
					t.Fatal(err)
				}
				if eng.preTradeCalls != calls || len(eng.execReportRemainders) != 0 || len(eng.execReportCalls) != 0 || !reflect.DeepEqual(before, reviewTestDetail(t, n, waiting.ExternalID)) || !reflect.DeepEqual(balances, afterBalances) {
					t.Fatal("public refusal changed held order or balances, or called engine")
				}
			})
		}
	}
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
	err     error
	prepare bool
}

func (e *failImmediateReviewSettleEngine) PrepareForcedHeldImmediate(
	order domain.Order, held engine.HeldReservation,
) (engine.ImmediatePreparation, error) {
	if e.prepare {
		return engine.ImmediatePreparation{}, e.err
	}
	return e.verdictAfterReservationEngine.PrepareForcedHeldImmediate(order, held)
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
	cancel        context.CancelFunc
	committed     bool
	allowReadBack bool
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
	if r.committed && !r.allowReadBack {
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
	if r.audit && (entry.Action == domain.AuditActionConfirmOrderReview || entry.Action == domain.AuditActionForceOrderReview) {
		panic("review audit panic")
	}
	return r.RealmStore.AppendAudit(ctx, entry)
}

func TestLocalNode_OrderReviewPostCommitPanic(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate, "force"} {
		for _, step := range []string{"settlement", "audit"} {
			if mode == "force" && step == "settlement" {
				continue
			}
			t.Run(mode+"/"+step, func(t *testing.T) {
				t.Parallel()
				submitMode := mode
				if mode == "force" {
					submitMode = domain.SubmitModeHold
				}
				n, _, waiting := newOrderReviewTestNode(t, submitMode, mode == "force")
				before := reviewTestDetail(t, n, waiting.ExternalID)
				original := n.realm
				n.realm = &panicOrderReviewRealm{RealmStore: original, audit: step == "audit"}
				var fatals []error
				n.fatal = func(err error) { fatals = append(fatals, err) }
				var err error
				if mode == "force" {
					_, _, err = n.ForceOrderReview(context.Background(), waiting.ExternalID, testCaller, nil)
				} else {
					_, _, err = confirmReviewForTest(n, mode, waiting.ExternalID, nil)
				}
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
				if rows := reviewTestAudit(t, n, domain.AuditActionForceOrderReview); len(rows) != 0 {
					t.Fatalf("panic force audit = %+v, want none", rows)
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
