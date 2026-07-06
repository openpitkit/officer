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
	"fmt"
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
) error {
	for _, event := range settlement.Events {
		if err := r.checkPrincipal(ctx, event.Principal); err != nil {
			return err
		}
	}
	if settlement.Trade != nil {
		if err := r.checkPrincipal(ctx, settlement.Trade.Principal); err != nil {
			return err
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
	n, _, err := NewLocalNode(ctx, st, fakeBuild(eng, &seed))
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
	n, realm := newColdStartTestNode(t, newFakeEngine())
	ctx := context.Background()

	if _, err := n.CreateAccount(ctx, testAccount("cold"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := realm.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset(USD): %v", err)
	}
	rec, err := n.ApplyAdjustment(ctx, testKey("cold"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset: "USD",
			Balance: &domain.AdjustmentAmount{
				Mode:  domain.AdjustmentModeAbsolute,
				Value: "1000",
			},
		}, testCaller)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if rec.Accepted == nil || rec.Rejected != nil {
		t.Fatalf("adjustment outcome = %+v, want accepted", rec)
	}
}

func TestLocalNode_ColdStartCreateAccountThenTrade(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
		BalanceResult: "10000",
	}
	n, realm := newColdStartTestNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateAccount(ctx, testAccount("cold"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	for _, asset := range []string{"USD", "AAPL"} {
		if err := realm.CreateAsset(ctx, domain.Asset{Code: asset}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", asset, err)
		}
	}
	if _, err := n.ApplyAdjustment(ctx, testKey("cold"), domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset: "USD",
			Balance: &domain.AdjustmentAmount{
				Mode:  domain.AdjustmentModeAbsolute,
				Value: "10000",
			},
		}, testCaller); err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if _, err := n.SubmitOrder(ctx, testKey("cold"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "10",
		Price:       "100",
	}, testCaller); err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if _, result, err := n.SubmitHold(ctx, testKey("cold"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "5",
		Price:       "100",
	}, testCaller); err != nil {
		t.Fatalf("SubmitHold: %v", err)
	} else if !result.Accepted {
		t.Fatalf("SubmitHold accepted = false, result = %+v", result)
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
	if err := n.SetAccountGroup(ctx, testKey("cold"), "vips", testCaller); err != nil {
		t.Fatalf("SetAccountGroup: %v", err)
	}
}
