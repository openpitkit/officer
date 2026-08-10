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
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
)

func newAssetDeleteTestNode(t *testing.T, eng *fakeEngine) *localNode {
	t.Helper()
	n, _ := newTestNode(t, eng)
	return n
}

func seedAssetPnlBound(t *testing.T, n *localNode, asset string) {
	t.Helper()
	if err := n.realm.PutSpotFundsPnlBoundsLimit(
		context.Background(),
		domain.LimitSpotFundsPnlBounds{
			Scope: domain.ScopeGlobal, Currency: asset, LowerBound: "-1",
		},
	); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}
}

func TestSnapshotWithoutAssetDropsCascadedRuntimeRows(t *testing.T) {
	t.Parallel()
	snapshot := snapshotWithoutAsset(engine.Snapshot{
		Accounts: []domain.Account{{Code: "retained", Currency: "USD"}},
		Groups:   []domain.AccountGroup{{Code: "desk", Currency: "USD"}},
		Balances: []domain.Balance{
			{Account: "account", Asset: "AAPL"},
			{Account: "account", Asset: "USD"},
		},
		RateLimits: []domain.LimitRate{
			{Scope: domain.ScopeAsset, Asset: "AAPL"},
			{Scope: domain.ScopeAsset, Asset: "USD"},
		},
		OrderSizeLimits: []domain.LimitOrderSize{
			{Scope: domain.ScopeAsset, Asset: "AAPL"},
			{Scope: domain.ScopeAsset, Asset: "USD"},
		},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			{Scope: domain.ScopeGlobal, Currency: "AAPL", LowerBound: "-1"},
			{Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-1"},
		},
	}, "AAPL")

	if len(snapshot.Accounts) != 1 || len(snapshot.Groups) != 1 ||
		len(snapshot.Balances) != 1 || snapshot.Balances[0].Asset != "USD" ||
		len(snapshot.RateLimits) != 1 || snapshot.RateLimits[0].Asset != "USD" ||
		len(snapshot.OrderSizeLimits) != 1 ||
		snapshot.OrderSizeLimits[0].Asset != "USD" ||
		len(snapshot.SpotFundsPnlBoundsLimits) != 1 ||
		snapshot.SpotFundsPnlBoundsLimits[0].Currency != "USD" {
		t.Fatalf("snapshot without asset = %+v", snapshot)
	}
}

func TestDeleteAssetForceRebuildsWithoutCascadedRuntimeRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := newFakeEngine()
	n := newAssetDeleteTestNode(t, old)
	const asset = "AAPL"

	if _, err := n.realm.CreateAccount(ctx, domain.Account{Code: "account"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.realm.UpsertBalance(ctx, domain.Balance{
		Account: "account", Asset: asset, Available: "1",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	if err := n.realm.PutRateLimit(ctx, domain.LimitRate{
		Scope: domain.ScopeAsset, Asset: asset, MaxOrders: 1, Window: time.Minute,
	}); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	if err := n.realm.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeAsset, Asset: asset, MaxQuantity: "1",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit: %v", err)
	}
	seedAssetPnlBound(t, n, asset)
	instance, err := n.realm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO,
		Label:    "manual",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if err := n.realm.UpsertMarketDataInstrument(ctx, domain.MarketDataInstrument{
		Instance:       instance.ExternalID,
		ExternalSymbol: "AAPLUSD",
		BaseAsset:      asset,
		QuoteAsset:     "USD",
		ManualPrice:    "1",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	const (
		survivingBaseAsset  = "MSFT"
		survivingQuoteAsset = "USD"
		survivingMark       = "400"
	)
	if err := n.realm.UpsertMarketDataInstrument(ctx, domain.MarketDataInstrument{
		Instance:       instance.ExternalID,
		ExternalSymbol: "MSFTUSD",
		BaseAsset:      survivingBaseAsset,
		QuoteAsset:     survivingQuoteAsset,
		ManualPrice:    survivingMark,
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertMarketDataInstrument(surviving): %v", err)
	}

	next := newFakeEngine()
	sink := &marketDataReplaySink{}
	next.sink = sink
	var rebuilt engine.Snapshot
	n.build = fakeBuild(next, &rebuilt)

	err = n.DeleteAsset(ctx, asset, false, testCaller)
	if !errors.Is(err, domain.ErrHasDependents) {
		t.Fatalf("DeleteAsset(no force) = %v, want ErrHasDependents", err)
	}
	if n.currentEngine() != old || !old.running {
		t.Fatalf("engine after rejected delete: current=%p old running=%v",
			n.currentEngine(), old.running)
	}

	if err := n.DeleteAsset(ctx, asset, true, testCaller); err != nil {
		t.Fatalf("DeleteAsset(force): %v", err)
	}
	if _, ok, getErr := n.realm.GetAsset(ctx, asset); getErr != nil || ok {
		t.Fatalf("asset after forced delete: ok=%v err=%v, want absent", ok, getErr)
	}
	if len(rebuilt.Balances) != 0 || len(rebuilt.RateLimits) != 0 ||
		len(rebuilt.OrderSizeLimits) != 0 ||
		len(rebuilt.SpotFundsPnlBoundsLimits) != 0 {
		t.Fatalf("rebuilt configuration retained deleted asset: %+v", rebuilt)
	}
	balances, balanceErr := n.realm.ListBalances(ctx, "", asset)
	if balanceErr != nil || len(balances) != 0 {
		t.Fatalf("balances after forced delete = %+v, err=%v", balances, balanceErr)
	}
	rateLimits, rateLimitErr := n.realm.ListRateLimits(ctx, "")
	if rateLimitErr != nil || len(rateLimits) != 0 {
		t.Fatalf("rate limits after forced delete = %+v, err=%v", rateLimits, rateLimitErr)
	}
	orderSizeLimits, orderSizeLimitErr := n.realm.ListOrderSizeLimits(ctx, "")
	if orderSizeLimitErr != nil || len(orderSizeLimits) != 0 {
		t.Fatalf(
			"order-size limits after forced delete = %+v, err=%v",
			orderSizeLimits,
			orderSizeLimitErr,
		)
	}
	pnlBounds, pnlBoundsErr := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
	if pnlBoundsErr != nil || len(pnlBounds) != 0 {
		t.Fatalf("P&L bounds after forced delete = %+v, err=%v", pnlBounds, pnlBoundsErr)
	}
	instruments, instrumentsErr := n.realm.ListMarketDataInstruments(ctx, instance.ExternalID)
	if instrumentsErr != nil || len(instruments) != 1 ||
		instruments[0].ExternalSymbol != "MSFTUSD" ||
		instruments[0].BaseAsset != survivingBaseAsset ||
		instruments[0].QuoteAsset != survivingQuoteAsset ||
		instruments[0].ManualPrice != survivingMark {
		t.Fatalf(
			"market-data instruments after forced delete = %+v, err=%v",
			instruments,
			instrumentsErr,
		)
	}
	survivingReplayed := false
	deletedAssetReplayed := false
	for _, update := range sink.updates {
		if update.Base == survivingBaseAsset && update.Quote == survivingQuoteAsset &&
			update.Mark == survivingMark {
			survivingReplayed = true
		}
		if update.Base == asset || update.Quote == asset {
			deletedAssetReplayed = true
		}
	}
	if !survivingReplayed {
		t.Fatalf("rebuilt engine did not replay surviving market data: %+v", sink.updates)
	}
	if deletedAssetReplayed {
		t.Fatalf("rebuilt engine replayed deleted asset market data: %+v", sink.updates)
	}
	if n.currentEngine() != next || !next.running || old.running {
		t.Fatalf("engine after forced delete: current=%p next running=%v old running=%v",
			n.currentEngine(), next.running, old.running)
	}
}

func TestDeleteAssetForceFailureKeepsStoreAndOldEngine(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		run    func(*testing.T, *localNode, *fakeEngine) (*fakeEngine, error)
		verify func(*testing.T, *localNode)
	}{
		{
			name: "build",
			run: func(_ *testing.T, n *localNode, _ *fakeEngine) (*fakeEngine, error) {
				buildErr := errors.New("asset delete build failed")
				n.build = func(engine.Snapshot) (engine.Engine, error) {
					return nil, buildErr
				}
				return nil, buildErr
			},
		},
		{
			name: "market-data transition",
			run: func(_ *testing.T, n *localNode, _ *fakeEngine) (*fakeEngine, error) {
				next := newFakeEngine()
				next.sink = nil
				n.build = fakeBuild(next, new(engine.Snapshot))
				return next, errors.New("nil market-data sink")
			},
		},
		{
			name: "durable store delete",
			run: func(t *testing.T, n *localNode, _ *fakeEngine) (*fakeEngine, error) {
				t.Helper()
				if _, err := n.realm.CreateAccount(context.Background(), domain.Account{
					Code: "account", Currency: "AAPL",
				}); err != nil {
					t.Fatalf("CreateAccount: %v", err)
				}
				next := newFakeEngine()
				n.build = fakeBuild(next, new(engine.Snapshot))
				return next, domain.ErrHasDependents
			},
			verify: func(t *testing.T, n *localNode) {
				t.Helper()
				account, ok, err := n.realm.GetAccount(context.Background(), "account")
				if err != nil || !ok || account.Currency != "AAPL" {
					t.Fatalf(
						"account after failed delete = %+v, ok=%v err=%v",
						account,
						ok,
						err,
					)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			old := newFakeEngine()
			n := newAssetDeleteTestNode(t, old)
			seedAssetPnlBound(t, n, "AAPL")

			next, want := test.run(t, n, old)
			err := n.DeleteAsset(ctx, "AAPL", true, testCaller)
			if err == nil || (!errors.Is(err, want) &&
				!strings.Contains(err.Error(), want.Error())) {
				t.Fatalf("DeleteAsset(force) = %v, want %v", err, want)
			}
			if _, ok, getErr := n.realm.GetAsset(ctx, "AAPL"); getErr != nil || !ok {
				t.Fatalf("asset after failed delete: ok=%v err=%v, want retained", ok, getErr)
			}
			limits, listErr := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
			if listErr != nil || len(limits) != 1 || limits[0].Currency != "AAPL" {
				t.Fatalf("SpotFunds P&L bounds after failed delete = %+v, err=%v", limits, listErr)
			}
			if n.currentEngine() != old || !old.running {
				t.Fatalf("engine after failed delete: current=%p old running=%v",
					n.currentEngine(), old.running)
			}
			if next != nil && next.running {
				t.Fatalf("new engine after failed delete still running: %p", next)
			}
			if test.verify != nil {
				test.verify(t, n)
			}
		})
	}
}

func TestAssetDeleteTransitionExcludesPendingMarketData(t *testing.T) {
	t.Parallel()
	oldSink := &marketDataReplaySink{}
	old := newFakeEngine()
	old.sink = oldSink
	n, _ := newTestNode(t, old)
	nextSink := &marketDataReplaySink{}
	next := newFakeEngine()
	next.sink = nextSink

	transition, err := n.beginMarketDataTransition(next)
	if err != nil {
		t.Fatalf("beginMarketDataTransition: %v", err)
	}
	transition.excludeAsset("AAPL")
	update := marketdata.QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "1"}
	if err := n.CurrentMarketDataSink().Push(update); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(oldSink.updates) != 1 || oldSink.updates[0] != update ||
		len(nextSink.updates) != 0 {
		t.Fatalf("buffered asset delete update: old=%+v next=%+v",
			oldSink.updates, nextSink.updates)
	}

	prev, err := n.commitMarketDataTransition(transition, next)
	if err != nil {
		t.Fatalf("commitMarketDataTransition: %v", err)
	}
	if prev != old || n.currentEngine() != next || len(nextSink.updates) != 0 {
		t.Fatalf("committed asset delete transition: prev=%p current=%p next=%+v",
			prev, n.currentEngine(), nextSink.updates)
	}
	prev.Stop()
}
