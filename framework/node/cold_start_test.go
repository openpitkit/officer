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
	"fmt"
	"reflect"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

type principalCheckingRealm struct {
	store.RealmStore
}

func (r *principalCheckingRealm) checkPrincipal(ctx context.Context, code string) error {
	if code == "" {
		return nil
	}
	if _, ok, err := r.GetPrincipal(ctx, code); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("unknown principal %q: %w", code, domain.ErrInvalid)
	}
	return nil
}

func (r *principalCheckingRealm) AppendAdjustment(
	ctx context.Context, rec domain.AccountAdjustmentRecord,
) (domain.AccountAdjustmentRecord, error) {
	if err := r.checkPrincipal(ctx, rec.Principal); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	return r.RealmStore.AppendAdjustment(ctx, rec)
}

func (r *principalCheckingRealm) CreateOrder(
	ctx context.Context, order domain.Order,
) (domain.Order, error) {
	if err := r.checkPrincipal(ctx, order.Principal); err != nil {
		return domain.Order{}, err
	}
	return r.RealmStore.CreateOrder(ctx, order)
}

func (r *principalCheckingRealm) RecordOrderSettlement(
	ctx context.Context, settlement domain.OrderSettlement,
) (domain.ExternalID, error) {
	for _, event := range settlement.Events {
		if err := r.checkPrincipal(ctx, event.Principal); err != nil {
			return "", err
		}
	}
	if settlement.Trade != nil {
		if err := r.checkPrincipal(ctx, settlement.Trade.Principal); err != nil {
			return "", err
		}
	}
	return r.RealmStore.RecordOrderSettlement(ctx, settlement)
}

func (r *principalCheckingRealm) AppendOrderEvent(
	ctx context.Context, event domain.OrderEvent,
) (domain.OrderEvent, error) {
	if err := r.checkPrincipal(ctx, event.Principal); err != nil {
		return domain.OrderEvent{}, err
	}
	return r.RealmStore.AppendOrderEvent(ctx, event)
}

func (r *principalCheckingRealm) CreateTrade(
	ctx context.Context, trade domain.Trade,
) (domain.Trade, error) {
	if err := r.checkPrincipal(ctx, trade.Principal); err != nil {
		return domain.Trade{}, err
	}
	return r.RealmStore.CreateTrade(ctx, trade)
}

func newColdStartTestNode(t *testing.T, eng *fakeEngine) (*localNode, store.RealmStore) {
	t.Helper()
	ctx := context.Background()
	real := newMemoryStore("cold-start.db")
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &principalCheckingRealm{RealmStore: r}
	})
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var seed engine.Snapshot
	n, _, err := NewLocalNode(ctx, domain.DefaultRealm, st, fakeBuild(eng, &seed), failOnFatal(t))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	return n.(*localNode), n.(*localNode).realm
}

func TestLocalNode_NewNodeSeedsOperatorPrincipal(t *testing.T) {
	t.Parallel()
	_, realm := newColdStartTestNode(t, newFakeEngine())
	ctx := context.Background()

	if _, ok, err := realm.GetPrincipal(ctx, testCaller.Principal); err != nil || !ok {
		t.Fatalf("GetPrincipal(%q) = ok %v, err %v; want present",
			testCaller.Principal, ok, err)
	}
}

func TestLocalNode_OperatorPrincipalSurvivesReset(t *testing.T) {
	t.Parallel()
	n, _ := newColdStartTestNode(t, newFakeEngine())
	ctx := context.Background()

	if _, err := n.ResetDatabase(ctx, testCaller); err != nil {
		t.Fatalf("ResetDatabase: %v", err)
	}
	if _, ok, err := n.realm.GetPrincipal(ctx, testCaller.Principal); err != nil || !ok {
		t.Fatalf("GetPrincipal(%q) after reset = ok %v, err %v; want present",
			testCaller.Principal, ok, err)
	}
}

func TestLocalNode_AdjustmentUnderOperatorCallerOnColdStore(t *testing.T) {
	t.Parallel()
	n, _ := newColdStartTestNode(t, newFakeEngine())
	ctx := context.Background()

	if _, err := n.CreateAccount(ctx, testAccount("cold"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := n.CreateAsset(ctx, domain.Asset{Code: "USD"}, testCaller); err != nil {
		t.Fatalf("CreateAsset(USD): %v", err)
	}
	rec, err := n.ApplyAdjustment(ctx, "cold", domain.ExternalID(""),
		domain.AdjustmentRequest{
			Type:  domain.AdjustmentTypeDeposit,
			Asset: "USD",
			Balance: &domain.AdjustmentAmount{
				Mode:  domain.AdjustmentModeAbsolute,
				Value: "1000",
			},
		}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted == nil || rec.Rejected != nil {
		t.Fatalf("adjustment outcome = %+v, want accepted", rec)
	}
}

func TestLocalNode_ReviewSurvivesRestart(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{domain.SubmitModeHold, domain.SubmitModeImmediate} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			base := newFakeEngine()
			n, realm := newColdStartTestNode(t, base)
			seedTestPrincipal(t, n)
			n.engine = &verdictAfterReservationEngine{
				fakeEngine: base, verdict: domain.VerdictReview,
				reviews: []domain.ReviewMark{{OrderReject: domain.OrderReject{Code: "order_size_limit"}, Mode: domain.RuleModeReviewWithoutHold}},
			}
			if _, err := n.CreateAccount(ctx, testAccount("cold-review"), testCaller); err != nil {
				t.Fatal(err)
			}
			if err := realm.UpsertBalance(ctx, domain.Balance{Account: "cold-review", Asset: "USD", Available: "10000", Held: "0", Incoming: "0"}); err != nil {
				t.Fatal(err)
			}
			before, err := realm.ListBalances(ctx, "", "")
			if err != nil {
				t.Fatal(err)
			}
			order := domain.Order{
				ExternalID: "cold-review-order", Account: "cold-review", BaseAsset: "AAPL", QuoteAsset: "USD",
				Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "100",
			}
			if mode == domain.SubmitModeHold {
				_, err = n.SubmitOrder(ctx, order, domain.MissingAccountReject, testCaller)
			} else {
				_, _, err = n.SubmitImmediate(ctx, order, domain.MissingAccountReject, testCaller)
			}
			if err != nil {
				t.Fatalf("submit review: %v", err)
			}
			if err := n.Close(); err != nil {
				t.Fatal(err)
			}
			var seed engine.Snapshot
			restartedEngine := &verdictAfterReservationEngine{
				fakeEngine: newFakeEngine(), verdict: domain.VerdictReview,
				confirmedVerdict: domain.VerdictAccept,
			}
			build := func(snapshot engine.Snapshot) (engine.Engine, error) {
				if _, err := fakeBuild(restartedEngine.fakeEngine, &seed)(snapshot); err != nil {
					return nil, err
				}
				return restartedEngine, nil
			}
			restarted, _, err := NewLocalNode(ctx, domain.DefaultRealm, n.db, build, failOnFatal(t))
			if err != nil {
				t.Fatalf("restart: %v", err)
			}
			t.Cleanup(func() { _ = restarted.Close() })
			detail, err := restarted.GetOrder(ctx, order.ExternalID)
			if err != nil || detail.Order.Status != domain.OrderStatusAwaitingReview || detail.Review == nil || detail.Review.Mode != mode || detail.Review.Destination != nil {
				t.Fatalf("restarted review = %+v, err=%v", detail, err)
			}
			if !reflect.DeepEqual(seed.Balances, before) {
				t.Fatalf("restart seed balances = %+v, want pre-submit %+v", seed.Balances, before)
			}
			confirmed, verdict, err := confirmReviewForTest(restarted.(*localNode), mode, order.ExternalID, nil)
			wantStatus := domain.OrderStatusCommitted
			if mode == domain.SubmitModeImmediate {
				wantStatus = domain.OrderStatusFilled
			}
			if err != nil || verdict != domain.VerdictAccept || confirmed.Status != wantStatus {
				t.Fatalf("confirm after restart = %+v, %q, %v", confirmed, verdict, err)
			}
			if len(restartedEngine.reviewFlags) != 1 || !restartedEngine.reviewFlags[0].Confirmed {
				t.Fatalf("restart confirmation flags = %+v, want confirmed", restartedEngine.reviewFlags)
			}
			if detail := reviewTestDetail(t, restarted.(*localNode), order.ExternalID); detail.Review != nil {
				t.Fatalf("confirmed restart still has review: %+v", detail.Review)
			}
		})
	}
}

func TestLocalNode_ColdStartCreateAccountThenTrade(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceResult: "10000",
	}
	n, _ := newColdStartTestNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateAccount(ctx, testAccount("cold"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	for _, asset := range []string{"USD", "AAPL"} {
		if _, err := n.CreateAsset(ctx, domain.Asset{Code: asset}, testCaller); err != nil {
			t.Fatalf("CreateAsset(%s): %v", asset, err)
		}
	}
	if _, err := n.ApplyAdjustment(ctx, "cold", domain.ExternalID(""),
		domain.AdjustmentRequest{
			Type:  domain.AdjustmentTypeDeposit,
			Asset: "USD",
			Balance: &domain.AdjustmentAmount{
				Mode:  domain.AdjustmentModeAbsolute,
				Value: "10000",
			},
		}, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if _, err := n.SubmitOrder(ctx, domain.Order{
		Account:     "cold",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "10",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order, err := n.SubmitOrder(ctx, domain.Order{
		Account:     "cold",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "5",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("second SubmitOrder: %v", err)
	} else if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("second SubmitOrder status = %q, want committed", order.Status)
	}
}

func TestLocalNode_ColdStartCreateGroupThenAssign(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "vips"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := n.CreateAccount(ctx, testAccount("cold"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetAccountGroup(ctx, "cold", "vips", domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("SetAccountGroup: %v", err)
	}
}
