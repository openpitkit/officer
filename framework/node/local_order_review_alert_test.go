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
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

type orderReviewObserverCall struct {
	step   string
	ctx    context.Context
	order  domain.Order
	mark   domain.ReviewMark
	caller domain.Caller
}

type orderReviewObserverFake struct {
	calls []orderReviewObserverCall
	err   error
}

func (f *orderReviewObserverFake) OrderAwaitingReview(
	ctx context.Context, order domain.Order, mark domain.ReviewMark, caller domain.Caller,
) error {
	f.calls = append(f.calls, orderReviewObserverCall{
		step: domain.OrderReviewAlertStepPublish, ctx: ctx,
		order: order, mark: mark, caller: caller,
	})
	return f.err
}

func (f *orderReviewObserverFake) OrderReviewDecided(
	ctx context.Context, order domain.Order, caller domain.Caller,
) error {
	f.calls = append(f.calls, orderReviewObserverCall{
		step: domain.OrderReviewAlertStepClose, ctx: ctx,
		order: order, caller: caller,
	})
	return f.err
}

func requireOrderReviewObserverCall(
	t *testing.T, observer *orderReviewObserverFake, step string, order domain.Order,
) {
	t.Helper()
	if len(observer.calls) != 1 {
		t.Fatalf("observer calls = %+v; want one %s", observer.calls, step)
	}
	call := observer.calls[0]
	if call.step != step || !reflect.DeepEqual(call.order, order) || !reflect.DeepEqual(call.caller, testCaller) {
		t.Fatalf("observer call = %+v; want %s, %+v, %+v", call, step, order, testCaller)
	}
	if call.ctx.Err() != nil {
		t.Fatalf("observer context cancelled: %v", call.ctx.Err())
	}
}

func submitObservedReviewOrder(
	n *localNode, ctx context.Context, mode string, order domain.Order,
) (domain.Order, []domain.ReviewMark, error) {
	if mode == domain.SubmitModeHold {
		got, result, err := n.SubmitOrderWithAttestation(ctx, order, domain.MissingAccountCreate, testCaller, nil, nil)
		return got, result.Reviews, err
	}
	got, result, err := n.SubmitImmediateWithAttestation(ctx, order, domain.MissingAccountCreate, testCaller, nil, nil)
	return got, result.Reviews, err
}

func TestPostCommitScopeFailuresAreInternal(t *testing.T) {
	ctx, finish := PostCommitNotifications(context.Background())
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"missing", context.Background()},
		{"finished", ctx},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := requirePostCommitScope(tc.ctx)
			if err == nil || errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("post-commit scope = %v, want internal error", err)
			}
		})
	}
}

func TestLocalNode_OrderReviewObserverSubmit(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, held := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/held=%t", mode, held), func(t *testing.T) {
				n, eng, input := newOrderReviewTestNode(t, mode, held)
				eng.reviews = append(eng.reviews, domain.ReviewMark{
					OrderReject: domain.OrderReject{Code: "second-mark"}, Mode: eng.reviews[0].Mode,
				})
				observer := &orderReviewObserverFake{}
				if err := n.InstallOrderReviewObserver(observer); err != nil {
					t.Fatal(err)
				}
				input.ExternalID += "-observed"
				order, marks, err := submitObservedReviewOrder(n, context.Background(), mode, input)
				if err != nil || order.Status != domain.OrderStatusAwaitingReview || len(marks) != 2 {
					t.Fatalf("submit = %+v, marks=%+v, %v", order, marks, err)
				}
				requireOrderReviewObserverCall(t, observer, domain.OrderReviewAlertStepPublish, order)
				if !reflect.DeepEqual(observer.calls[0].mark, marks[0]) {
					t.Fatalf("published mark = %+v; want %+v", observer.calls[0].mark, marks[0])
				}
			})
		}
	}
}

func TestLocalNode_OrderReviewObserverNonReviewSubmit(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, verdict := range []domain.Verdict{domain.VerdictAccept, domain.VerdictReject} {
			t.Run(mode+"/"+string(verdict), func(t *testing.T) {
				n, eng, input := newOrderReviewTestNode(t, mode)
				n.engine = eng.fakeEngine
				if verdict == domain.VerdictReject {
					eng.submitReject = &domain.OrderReject{Code: "order_size_limit", Scope: "order"}
				}
				observer := &orderReviewObserverFake{}
				if err := n.InstallOrderReviewObserver(observer); err != nil {
					t.Fatal(err)
				}
				input.ExternalID += "-observed"
				order, marks, err := submitObservedReviewOrder(n, context.Background(), mode, input)
				status := domain.OrderStatusCommitted
				if mode == domain.SubmitModeImmediate {
					status = domain.OrderStatusFilled
				}
				if verdict == domain.VerdictReject {
					status = domain.OrderStatusRejected
				}
				if err != nil || order.Status != status || len(marks) != 0 || len(observer.calls) != 0 {
					t.Fatalf("submit = %+v, marks=%+v, calls=%+v, %v", order, marks, observer.calls, err)
				}
			})
		}
	}
}

func TestLocalNode_OrderReviewObserverDecisions(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, held := range []bool{false, true} {
			for _, action := range []string{"confirm", "reject", "force", "cancel"} {
				if action == "force" && !held {
					continue
				}
				t.Run(fmt.Sprintf("%s/held=%t/%s", mode, held, action), func(t *testing.T) {
					n, eng, waiting := newOrderReviewTestNode(t, mode, held)
					observer := &orderReviewObserverFake{}
					if err := n.InstallOrderReviewObserver(observer); err != nil {
						t.Fatal(err)
					}
					if held && mode == domain.SubmitModeImmediate {
						eng.submitOutcomes = []engine.BalanceOutcome{
							{Asset: "AAPL", Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "-1", IncomingResult: "0", BalanceResult: "1", HeldResult: "0"}},
							{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{HeldDelta: "-100", HeldResult: "0", BalanceResult: "900", IncomingResult: "0"}},
						}
					}
					var order domain.Order
					var err error
					switch action {
					case "reject":
						eng.submitReject = &domain.OrderReject{Code: "position_limit", Scope: "order"}
						fallthrough
					case "confirm":
						order, _, err = confirmReviewForTest(n, mode, waiting.ExternalID, nil)
					case "force":
						order, _, err = forceReviewForTest(n, mode, waiting.ExternalID,
							func(domain.Order) store.EventAttestor { return nil })
					case "cancel":
						if held {
							eng.execReportOutcomes = []engine.BalanceOutcome{
								{Asset: "AAPL", Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "-1", IncomingResult: "0", BalanceResult: "0", HeldResult: "0"}},
								{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{HeldDelta: "-100", HeldResult: "0", BalanceResult: "1000", IncomingResult: "0"}},
							}
						}
						order, err = n.CancelOrderReview(context.Background(), waiting.ExternalID, testCaller, nil)
					}
					if err != nil {
						t.Fatal(err)
					}
					if action == "reject" {
						if order.Status != domain.OrderStatusAwaitingReview || len(observer.calls) != 0 {
							t.Fatalf("reject = %+v, calls=%+v", order, observer.calls)
						}
						return
					}
					if order.Status == domain.OrderStatusAwaitingReview {
						t.Fatalf("%s left order waiting: %+v", action, order)
					}
					requireOrderReviewObserverCall(t, observer, domain.OrderReviewAlertStepClose, order)
				})
			}
		}
	}
}

func TestLocalNode_OrderReviewObserverInstall(t *testing.T) {
	n, _, _ := newOrderReviewTestNode(t, domain.SubmitModeHold)
	var host OrderReviewObserverHost = n
	if err := host.InstallOrderReviewObserver(nil); err == nil || errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("nil install = %v", err)
	}
	if err := host.InstallOrderReviewObserver(&orderReviewObserverFake{}); err != nil {
		t.Fatal(err)
	}
	if err := host.InstallOrderReviewObserver(&orderReviewObserverFake{}); err == nil || errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("second install = %v", err)
	}
}

func TestLocalNode_OrderReviewObserverFailureKeepsCommittedState(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, action := range []string{"submit", "confirm", "cancel"} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				n, _, input := newOrderReviewTestNode(t, mode)
				cause := errors.New("observer unavailable")
				observer := &orderReviewObserverFake{err: cause}
				if err := n.InstallOrderReviewObserver(observer); err != nil {
					t.Fatal(err)
				}
				step, status := domain.OrderReviewAlertStepClose, domain.OrderStatusCommitted
				var order domain.Order
				var err error
				switch action {
				case "submit":
					step, status = domain.OrderReviewAlertStepPublish, domain.OrderStatusAwaitingReview
					input.ExternalID += "-observed"
					order, _, err = submitObservedReviewOrder(n, context.Background(), mode, input)
				case "confirm":
					if mode == domain.SubmitModeImmediate {
						status = domain.OrderStatusFilled
					}
					order, _, err = confirmReviewForTest(n, mode, input.ExternalID, nil)
				case "cancel":
					status = domain.OrderStatusCancelled
					order, err = n.CancelOrderReview(context.Background(), input.ExternalID, testCaller, nil)
				}
				var alertErr *domain.OrderReviewAlertError
				if !errors.As(err, &alertErr) || alertErr.Step != step || !errors.Is(err, cause) {
					t.Fatalf("notification error = %v; want %s with cause", err, step)
				}
				stored := reviewTestDetail(t, n, input.ExternalID).Order
				if order.Status != status || !reflect.DeepEqual(order, stored) {
					t.Fatalf("returned order = %+v, stored=%+v; want %s", order, stored, status)
				}
				requireOrderReviewObserverCall(t, observer, step, order)
			})
		}
	}
}

func TestLocalNode_OrderReviewObserverOuterScope(t *testing.T) {
	n, _, input := newOrderReviewTestNode(t, domain.SubmitModeHold)
	observer := &orderReviewObserverFake{}
	if err := n.InstallOrderReviewObserver(observer); err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, finish := PostCommitNotifications(base)
	input.ExternalID += "-observed"
	order, _, err := submitObservedReviewOrder(n, ctx, domain.SubmitModeHold, input)
	if err != nil || len(observer.calls) != 0 {
		t.Fatalf("submit inside outer scope = %v, calls=%+v", err, observer.calls)
	}
	cancel()
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	requireOrderReviewObserverCall(t, observer, domain.OrderReviewAlertStepPublish, order)
	if scope, _ := observer.calls[0].ctx.Value(postCommitKey{}).(*postCommitScope); scope != nil {
		t.Fatal("observer retained the completed post-commit scope")
	}
}

func TestLocalNode_OrderReviewObserverMissingMark(t *testing.T) {
	for _, status := range []domain.OrderStatus{
		domain.OrderStatusAwaitingReview, domain.OrderStatusCommitted,
	} {
		t.Run(string(status), func(t *testing.T) {
			observer := &orderReviewObserverFake{}
			n := &localNode{orderReviewObserver: observer}
			order := domain.Order{Status: status}
			var reviews []domain.ReviewMark
			var resultErr error
			finished := false
			finish := func() error {
				finished = true
				return nil
			}
			n.finishSubmitReviewNotifications(context.Background(), finish, &order, &reviews, testCaller, &resultErr)
			if status == domain.OrderStatusAwaitingReview {
				if resultErr == nil || errors.Is(resultErr, domain.ErrInvalid) || !strings.Contains(resultErr.Error(), "no review mark") {
					t.Fatalf("missing mark = %v; want explicit internal error", resultErr)
				}
			} else if resultErr != nil {
				t.Fatalf("non-review order = %v; want no mark validation", resultErr)
			}
			if !finished || len(observer.calls) != 0 {
				t.Fatalf("finished=%t, calls=%+v; want finish without publish", finished, observer.calls)
			}
		})
	}
}
