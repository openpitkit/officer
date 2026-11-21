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
	"testing"
	"time"

	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

func seedHeldReviewNode(t *testing.T, n node.Node, mixed bool) domain.Order {
	t.Helper()
	ctx, caller := systemCtx(), auth.SystemCaller()
	deposit, err := n.ApplyAdjustment(ctx, "held-review-account", "held-review-deposit", domain.AdjustmentRequest{
		Type: domain.AdjustmentTypeDeposit, Asset: "USD",
		Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "500"},
	}, domain.MissingAccountCreate, caller)
	if err != nil || deposit.Accepted == nil {
		t.Fatalf("deposit = %+v, %v", deposit, err)
	}
	modes := []domain.RuleMode{domain.RuleModeReviewWithHold}
	if mixed {
		modes = append(modes, domain.RuleModeReviewWithoutHold)
	}
	for _, mode := range modes {
		if _, err := n.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
			Mode: mode, Scope: domain.ScopeBroker, MaxQuantity: "4",
		}, domain.LimitWriteAdd, domain.MissingAccountReject, caller); err != nil {
			t.Fatal(err)
		}
	}
	return domain.Order{
		ExternalID: "held-review-order", Account: "held-review-account", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "5", Price: "100",
	}
}

func heldReviewBalances(t *testing.T, n node.Node, available, held, incoming string) []domain.Balance {
	t.Helper()
	balances, err := n.ListBalances(systemCtx(), "held-review-account", "")
	if err != nil {
		t.Fatal(err)
	}
	var quoteFound, baseFound bool
	for _, balance := range balances {
		switch balance.Asset {
		case "USD":
			quoteFound = true
			if balance.Available != available || balance.Held != held {
				t.Fatalf("USD balance = %+v; want available=%s held=%s", balance, available, held)
			}
		case "AAPL":
			baseFound = true
			if balance.Incoming != incoming {
				t.Fatalf("AAPL incoming = %s; want %s", balance.Incoming, incoming)
			}
		}
	}
	if (!quoteFound && (available != "0" || held != "0")) || (incoming != "0" && !baseFound) {
		t.Fatalf("missing held balance: %+v", balances)
	}
	return balances
}

func TestLocalNodeHeldReviewLifecycle(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, volume := range []bool{false, true} {
			for _, restart := range []bool{false, true} {
				name := mode
				if volume {
					name += "/volume"
				} else {
					name += "/quantity"
				}
				if restart {
					name += "/restart"
				}
				t.Run(name, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "officer.db")
					n, _ := newLocalNodeAt(t, path, engine.NewOpenPitEngineBuildFunc())
					order := seedHeldReviewNode(t, n, false)
					if volume {
						order.AmountKind, order.AmountValue = domain.OrderAmountKindVolume, "500"
					}
					ctx, caller := systemCtx(), auth.SystemCaller()
					submitted, err := submitReviewOrder(ctx, n, mode, order, caller)
					if err != nil {
						t.Fatalf("held submit: %v", err)
					}
					detail, err := n.GetOrder(ctx, order.ExternalID)
					if err != nil || detail.Review == nil || !detail.Review.Held || submitted.Status != domain.OrderStatusAwaitingReview || submitted.ReservedQuantity != "5" || submitted.Leaves != "5" || len(submitted.Lock) == 0 || len(detail.Trades) != 0 {
						t.Fatalf("held submit = %+v, review=%+v, %v", submitted, detail.Review, err)
					}
					before := heldReviewBalances(t, n, "0", "500", "5")
					if restart {
						if err := n.Close(); err != nil {
							t.Fatal(err)
						}
						n, _ = newLocalNodeAt(t, path, engine.NewOpenPitEngineBuildFunc())
						after := heldReviewBalances(t, n, "0", "500", "5")
						if !reflect.DeepEqual(before, after) {
							t.Fatalf("restart balances = %+v; want %+v", after, before)
						}
					}
					var confirmed domain.Order
					if mode == domain.SubmitModeImmediate {
						confirmed, _, err = n.ConfirmImmediateReview(ctx, order.ExternalID, caller, nil)
					} else {
						confirmed, _, err = n.ConfirmOrderReview(ctx, order.ExternalID, caller, nil)
					}
					if err != nil {
						t.Fatalf("held confirm: %v", err)
					}
					if mode == domain.SubmitModeImmediate {
						if confirmed.Status != domain.OrderStatusFilled || confirmed.ReservedQuantity != "0" || confirmed.Leaves != "0" {
							t.Fatalf("held fill = %+v", confirmed)
						}
						heldReviewBalances(t, n, "0", "0", "0")
						detail, err := n.GetOrder(ctx, order.ExternalID)
						if err != nil || len(detail.Trades) != 1 || detail.Trades[0].Quantity != "5" || detail.Trades[0].LockPrice != "100" {
							t.Fatalf("held trade = %+v, %v", detail.Trades, err)
						}
					} else {
						after := heldReviewBalances(t, n, "0", "500", "5")
						if confirmed.Status != domain.OrderStatusCommitted || confirmed.ReservedQuantity != "5" || !reflect.DeepEqual(confirmed.Lock, submitted.Lock) || !reflect.DeepEqual(before, after) {
							t.Fatalf("venue confirm changed hold: %+v, balances=%+v", confirmed, after)
						}
					}
				})
			}
		}
	}
}

func submitReviewOrder(
	ctx context.Context,
	n node.Node,
	mode string,
	order domain.Order,
	caller domain.Caller,
) (domain.Order, error) {
	if mode == domain.SubmitModeImmediate {
		stored, _, err := n.SubmitImmediate(
			ctx, order, domain.MissingAccountReject, caller,
		)
		return stored, err
	}
	return n.SubmitOrder(ctx, order, domain.MissingAccountReject, caller)
}

func TestLocalNodeHeldReviewCancel(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, amountKind := range []domain.OrderAmountKind{domain.OrderAmountKindQuantity, domain.OrderAmountKindVolume} {
			for _, restart := range []bool{false, true} {
				name := mode + "/" + string(amountKind)
				if restart {
					name += "/restart"
				}
				t.Run(name, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "officer.db")
					n, _ := newLocalNodeAt(t, path, engine.NewOpenPitEngineBuildFunc())
					order := seedHeldReviewNode(t, n, false)
					if amountKind == domain.OrderAmountKindVolume {
						order.AmountKind, order.AmountValue = amountKind, "500"
					}
					ctx, caller := systemCtx(), auth.SystemCaller()
					if _, err := submitReviewOrder(ctx, n, mode, order, caller); err != nil {
						t.Fatalf("held submit: %v", err)
					}
					heldReviewBalances(t, n, "0", "500", "5")
					if restart {
						if err := n.Close(); err != nil {
							t.Fatal(err)
						}
						n, _ = newLocalNodeAt(t, path, engine.NewOpenPitEngineBuildFunc())
						heldReviewBalances(t, n, "0", "500", "5")
					}
					cancelled, err := n.CancelOrderReview(ctx, order.ExternalID, caller, nil)
					if err != nil {
						t.Fatalf("held cancel: %v", err)
					}
					heldReviewBalances(t, n, "500", "0", "0")
					if cancelled.Status != domain.OrderStatusCancelled || cancelled.Leaves != "0" || cancelled.ReservedQuantity != "0" {
						t.Fatalf("held cancel order = %+v", cancelled)
					}
					detail, err := n.GetOrder(ctx, order.ExternalID)
					if err != nil || detail.Review != nil || detail.Order.Status != domain.OrderStatusCancelled || detail.Order.Leaves != "0" || detail.Order.ReservedQuantity != "0" || len(detail.Trades) != 0 {
						t.Fatalf("held cancel persisted state = %+v, %v", detail, err)
					}
					order.ExternalID = "held-review-next-order"
					next, err := submitReviewOrder(ctx, n, mode, order, caller)
					if err != nil || next.Status != domain.OrderStatusAwaitingReview || next.ReservedQuantity != "5" {
						t.Fatalf("pre-trade after held cancel = %+v, %v", next, err)
					}
					heldReviewBalances(t, n, "0", "500", "5")
				})
			}
		}
	}
}

func TestLocalNodeHeldReviewCancelAfterRejectedConfirm(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "officer.db")
			n, _ := newLocalNodeAt(t, path, engine.NewOpenPitEngineBuildFunc())
			order := seedHeldReviewNode(t, n, false)
			ctx, caller := systemCtx(), auth.SystemCaller()
			if _, err := n.PutRateLimit(ctx, domain.LimitRate{
				Scope: domain.ScopeAccount, Account: order.Account,
				MaxOrders: 1, Window: time.Hour,
			}, domain.LimitWriteAdd, domain.MissingAccountReject, caller); err != nil {
				t.Fatalf("put rate limit: %v", err)
			}
			waiting, err := submitReviewOrder(ctx, n, mode, order, caller)
			if err != nil || waiting.Status != domain.OrderStatusAwaitingReview || waiting.ReservedQuantity != "5" {
				t.Fatalf("held submit = %+v, %v", waiting, err)
			}
			heldReviewBalances(t, n, "0", "500", "5")
			var confirmed domain.Order
			var verdict domain.Verdict
			var rejects []domain.OrderReject
			if mode == domain.SubmitModeImmediate {
				stored, result, confirmErr := n.ConfirmImmediateReview(ctx, order.ExternalID, caller, nil)
				confirmed, verdict, rejects, err = stored, result.Verdict, result.Rejects, confirmErr
			} else {
				stored, result, confirmErr := n.ConfirmOrderReview(ctx, order.ExternalID, caller, nil)
				confirmed, verdict, rejects, err = stored, result.Verdict, result.Rejects, confirmErr
			}
			if err != nil || verdict != domain.VerdictReject || len(rejects) != 1 || rejects[0].Code != domain.RejectCodeRateLimitExceeded {
				t.Fatalf("held confirm = %+v, verdict=%s rejects=%+v, %v", confirmed, verdict, rejects, err)
			}
			detail, err := n.GetOrder(ctx, order.ExternalID)
			if err != nil || detail.Review == nil || !detail.Review.Held || detail.Order.Status != domain.OrderStatusAwaitingReview || detail.Order.ReservedQuantity != "5" || detail.Order.Leaves != "5" || !reflect.DeepEqual(detail.Order.Lock, waiting.Lock) || confirmed.Status != domain.OrderStatusAwaitingReview || confirmed.ReservedQuantity != "5" || len(detail.Trades) != 0 {
				t.Fatalf("rejected held confirm state = %+v, %v", detail, err)
			}
			heldReviewBalances(t, n, "0", "500", "5")
			cancelled, err := n.CancelOrderReview(ctx, order.ExternalID, caller, nil)
			if err != nil || cancelled.Status != domain.OrderStatusCancelled || cancelled.Leaves != "0" || cancelled.ReservedQuantity != "0" {
				t.Fatalf("held cancel after rejected confirm = %+v, %v", cancelled, err)
			}
			heldReviewBalances(t, n, "500", "0", "0")
			detail, err = n.GetOrder(ctx, order.ExternalID)
			if err != nil || detail.Review != nil || detail.Order.Status != domain.OrderStatusCancelled || detail.Order.Leaves != "0" || detail.Order.ReservedQuantity != "0" || len(detail.Trades) != 0 {
				t.Fatalf("held cancel persisted state = %+v, %v", detail, err)
			}
			var rejected bool
			for _, event := range detail.Events {
				if event.Type == domain.OrderEventPreTradeRejected {
					rejected = true
				}
			}
			if !rejected {
				t.Fatal("held cancel lost pre_trade_rejected event")
			}
			if _, err := n.DeleteLimit(ctx, node.LimitTarget{
				Policy: domain.PolicyRateLimit, Scope: domain.ScopeAccount,
				Account: order.Account,
			}, caller); err != nil {
				t.Fatalf("delete rate limit: %v", err)
			}
			order.ExternalID = "held-review-next-order"
			next, err := submitReviewOrder(ctx, n, mode, order, caller)
			if err != nil || next.Status != domain.OrderStatusAwaitingReview || next.ReservedQuantity != "5" {
				t.Fatalf("pre-trade after rejected confirm and cancel = %+v, %v", next, err)
			}
			heldReviewBalances(t, n, "0", "500", "5")
		})
	}
}

func forceReviewOrder(
	ctx context.Context, n node.Node, mode string, order domain.ExternalID,
	caller domain.Caller,
) (domain.Order, error) {
	if mode == domain.SubmitModeImmediate {
		forced, _, err := n.ForceImmediateReview(ctx, order, caller, nil)
		return forced, err
	}
	forced, _, err := n.ForceOrderReview(ctx, order, caller, nil)
	return forced, err
}

func TestLocalNodeHeldReviewForce(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, volume := range []bool{false, true} {
			for _, restart := range []bool{false, true} {
				name := fmt.Sprintf("%s/volume=%t/restart=%t", mode, volume, restart)
				t.Run(name, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "officer.db")
					n, _ := newLocalNodeAt(t, path, engine.NewOpenPitEngineBuildFunc())
					order := seedHeldReviewNode(t, n, false)
					if volume {
						order.AmountKind, order.AmountValue = domain.OrderAmountKindVolume, "500"
					}
					ctx, caller := systemCtx(), auth.SystemCaller()
					if _, err := n.PutRateLimit(ctx, domain.LimitRate{
						Scope: domain.ScopeAccount, Account: order.Account,
						MaxOrders: 1, Window: time.Hour,
					}, domain.LimitWriteAdd, domain.MissingAccountReject, caller); err != nil {
						t.Fatal(err)
					}
					waiting, err := submitReviewOrder(ctx, n, mode, order, caller)
					if err != nil || waiting.Status != domain.OrderStatusAwaitingReview || waiting.ReservedQuantity != "5" {
						t.Fatalf("held submit = %+v, %v", waiting, err)
					}
					before := heldReviewBalances(t, n, "0", "500", "5")
					if restart {
						if err := n.Close(); err != nil {
							t.Fatal(err)
						}
						n, _ = newLocalNodeAt(t, path, engine.NewOpenPitEngineBuildFunc())
						if after := heldReviewBalances(t, n, "0", "500", "5"); !reflect.DeepEqual(before, after) {
							t.Fatalf("restart changed hold: %+v", after)
						}
					}
					forced, err := forceReviewOrder(ctx, n, mode, order.ExternalID, caller)
					if err != nil {
						t.Fatalf("force despite exhausted pre-trade rate limit: %v", err)
					}
					detail, err := n.GetOrder(ctx, order.ExternalID)
					if err != nil || detail.Review != nil || !reflect.DeepEqual(forced.Lock, waiting.Lock) {
						t.Fatalf("force lost lock or kept review: %+v, %v", detail, err)
					}
					var after []domain.Balance
					if mode == domain.SubmitModeHold {
						after = heldReviewBalances(t, n, "0", "500", "5")
						if forced.Status != domain.OrderStatusCommitted || forced.ReservedQuantity != "5" || forced.Leaves != "5" || len(detail.Trades) != 0 || !reflect.DeepEqual(before, after) {
							t.Fatalf("force changed hold: %+v, trades=%+v balances=%+v", forced, detail.Trades, after)
						}
					} else {
						after = heldReviewBalances(t, n, "0", "0", "0")
						if forced.Status != domain.OrderStatusFilled || forced.ReservedQuantity != "0" || forced.Leaves != "0" || len(detail.Trades) != 1 || detail.Trades[0].Quantity != "5" || detail.Trades[0].Price != order.Price || detail.Trades[0].LockPrice != "100" {
							t.Fatalf("forced fill = %+v, trades=%+v", forced, detail.Trades)
						}
						baseline, _ := newLocalNodeAt(t, filepath.Join(t.TempDir(), "baseline.db"), engine.NewOpenPitEngineBuildFunc())
						baselineOrder := seedHeldReviewNode(t, baseline, false)
						baselineOrder.AmountKind, baselineOrder.AmountValue = order.AmountKind, order.AmountValue
						if _, err := submitReviewOrder(ctx, baseline, mode, baselineOrder, caller); err != nil {
							t.Fatal(err)
						}
						if _, _, err := baseline.ConfirmImmediateReview(ctx, baselineOrder.ExternalID, caller, nil); err != nil {
							t.Fatal(err)
						}
						confirmed := heldReviewBalances(t, baseline, "0", "0", "0")
						if len(after) != len(confirmed) {
							t.Fatalf("force balances=%+v; confirm=%+v", after, confirmed)
						}
						for i, actual := range after {
							expected := confirmed[i]
							actual.UpdatedAt, expected.UpdatedAt = time.Time{}, time.Time{}
							if !reflect.DeepEqual(actual, expected) {
								t.Fatalf("force balance=%+v; confirm=%+v", actual, expected)
							}
						}
					}
					_, err = forceReviewOrder(ctx, n, mode, order.ExternalID, caller)
					if !errors.Is(err, domain.ErrConflict) {
						t.Fatalf("second force error = %v; want conflict", err)
					}
					again, getErr := n.GetOrder(ctx, order.ExternalID)
					balances, listErr := n.ListBalances(ctx, order.Account, "")
					if getErr != nil || listErr != nil || !reflect.DeepEqual(detail, again) || !reflect.DeepEqual(after, balances) {
						t.Fatalf("second force changed state: %+v, %v, %v", again, getErr, listErr)
					}
				})
			}
		}
	}
}

func TestLocalNodeHeldReviewForceBlocked(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		for _, tier := range []string{"account", "group"} {
			t.Run(mode+"/"+tier, func(t *testing.T) {
				n, _ := newLocalNodeAt(t, filepath.Join(t.TempDir(), "officer.db"), engine.NewOpenPitEngineBuildFunc())
				order := seedHeldReviewNode(t, n, false)
				ctx, caller := systemCtx(), auth.SystemCaller()
				if tier == "group" {
					if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "review-desk"}, caller); err != nil {
						t.Fatal(err)
					}
					if err := n.SetAccountGroup(ctx, order.Account, "review-desk", domain.MissingAccountReject, caller); err != nil {
						t.Fatal(err)
					}
				}
				waiting, err := submitReviewOrder(ctx, n, mode, order, caller)
				if err != nil || waiting.Status != domain.OrderStatusAwaitingReview {
					t.Fatalf("held submit = %+v, %v", waiting, err)
				}
				before := heldReviewBalances(t, n, "0", "500", "5")
				setBlocked := func(blocked bool) error {
					if tier == "group" {
						return n.SetGroupBlocked(ctx, "review-desk", blocked, "risk", caller)
					}
					return n.SetAccountBlocked(ctx, order.Account, blocked, "risk", domain.MissingAccountReject, caller)
				}
				if err := setBlocked(true); err != nil {
					t.Fatal(err)
				}
				detail, err := n.GetOrder(ctx, order.ExternalID)
				if err != nil || detail.Review == nil || !detail.Review.Held {
					t.Fatalf("held review = %+v, %v", detail, err)
				}
				if _, err := forceReviewOrder(ctx, n, mode, order.ExternalID, caller); !errors.Is(err, domain.ErrConflict) {
					t.Fatalf("force blocked %s error = %v; want conflict", tier, err)
				}
				after, err := n.GetOrder(ctx, order.ExternalID)
				balances := heldReviewBalances(t, n, "0", "500", "5")
				if err != nil || !reflect.DeepEqual(detail, after) || !reflect.DeepEqual(before, balances) {
					t.Fatalf("blocked force changed order or hold: %+v, %v", after, err)
				}
				if err := setBlocked(false); err != nil {
					t.Fatal(err)
				}
				forced, err := forceReviewOrder(ctx, n, mode, order.ExternalID, caller)
				if err != nil {
					t.Fatalf("force after unblock: %v", err)
				}
				after, err = n.GetOrder(ctx, order.ExternalID)
				if err != nil || after.Review != nil || !reflect.DeepEqual(forced.Lock, waiting.Lock) {
					t.Fatalf("force after unblock lost lock or kept review: %+v, %v", after, err)
				}
				if mode == domain.SubmitModeHold {
					balances = heldReviewBalances(t, n, "0", "500", "5")
					if forced.Status != domain.OrderStatusCommitted || forced.ReservedQuantity != "5" || forced.Leaves != "5" || len(after.Trades) != 0 || !reflect.DeepEqual(before, balances) {
						t.Fatalf("force after unblock changed hold: %+v", after)
					}
				} else {
					heldReviewBalances(t, n, "0", "0", "0")
					if forced.Status != domain.OrderStatusFilled || forced.ReservedQuantity != "0" || forced.Leaves != "0" || len(after.Trades) != 1 || after.Trades[0].Quantity != "5" || after.Trades[0].Price != order.Price || after.Trades[0].LockPrice != "100" {
						t.Fatalf("force after unblock fill = %+v", after)
					}
				}
			})
		}
	}
}

func TestLocalNodeHeldReviewForceWithoutHold(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		t.Run(mode, func(t *testing.T) {
			n, _ := newLocalNodeAt(t, filepath.Join(t.TempDir(), "officer.db"), engine.NewOpenPitEngineBuildFunc())
			order := seedHeldReviewNode(t, n, true)
			ctx, caller := systemCtx(), auth.SystemCaller()
			if _, err := submitReviewOrder(ctx, n, mode, order, caller); err != nil {
				t.Fatal(err)
			}
			before, err := n.GetOrder(ctx, order.ExternalID)
			if err != nil || before.Review == nil || before.Review.Held {
				t.Fatalf("without-hold review = %+v, %v", before, err)
			}
			balances := heldReviewBalances(t, n, "500", "0", "0")
			_, err = forceReviewOrder(ctx, n, mode, order.ExternalID, caller)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("force without hold = %v; want conflict", err)
			}
			after, err := n.GetOrder(ctx, order.ExternalID)
			if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(balances, heldReviewBalances(t, n, "500", "0", "0")) {
				t.Fatalf("force without hold changed state: %+v, %v", after, err)
			}
		})
	}
}

func TestLocalNodeHeldReviewMixed(t *testing.T) {
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		t.Run(mode, func(t *testing.T) {
			n, _ := newLocalNodeAt(t, filepath.Join(t.TempDir(), "officer.db"), engine.NewOpenPitEngineBuildFunc())
			order := seedHeldReviewNode(t, n, true)
			ctx, caller := systemCtx(), auth.SystemCaller()
			_, err := submitReviewOrder(ctx, n, mode, order, caller)
			if err != nil {
				t.Fatal(err)
			}
			detail, err := n.GetOrder(ctx, order.ExternalID)
			if err != nil || detail.Review == nil || detail.Review.Held || detail.Order.Status != domain.OrderStatusAwaitingReview || detail.Order.ReservedQuantity != "0" || len(detail.Order.Lock) != 0 {
				t.Fatalf("mixed review = %+v, %v", detail, err)
			}
			heldReviewBalances(t, n, "500", "0", "0")
		})
	}
}
