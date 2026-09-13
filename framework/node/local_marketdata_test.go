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
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

func TestUpsertMarketDataInstrumentKeepsEngineAndSink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newMemoryStore("node.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	buildCalls := 0
	var fake *fakeEngine
	wantSink := &marketDataTestSink{}
	build := func(engine.Snapshot) (engine.Engine, error) {
		buildCalls++
		if buildCalls > 1 {
			return nil, fmt.Errorf("unexpected market-data engine rebuild")
		}
		fake = newFakeEngine()
		return &marketDataTestEngine{
			Engine: fake,
			sink:   wantSink,
		}, nil
	}
	nodeValue, _, err := NewLocalNode(ctx, domain.DefaultRealm, st, build, failOnFatal(t))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nodeValue.(*localNode)
	seedTestPrincipal(t, n)

	instance, err := n.realm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBinance,
		Label:    "Binance",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	instrument := domain.MarketDataInstrument{
		Instance:       instance.ExternalID,
		ExternalSymbol: "BTCUSDT",
		BaseAsset:      "BTC",
		QuoteAsset:     "USDT",
		Enabled:        true,
	}

	before := n.CurrentMarketDataSink()
	if err := n.UpsertMarketDataInstrument(ctx, instrument, testCaller); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	after := n.CurrentMarketDataSink()
	if buildCalls != 1 {
		t.Fatalf("build calls = %d, want initial build only", buildCalls)
	}
	if before != after || after != wantSink {
		t.Fatalf("market-data sink changed: before=%p after=%p want=%p",
			before, after, wantSink)
	}

	for _, code := range []string{"BTC", "USDT"} {
		asset, ok, err := n.realm.GetAsset(ctx, code)
		if err != nil || !ok {
			t.Fatalf("GetAsset(%s) = ok %v, err %v; want created asset",
				code, ok, err)
		}
		if asset.AssetClass != autoCreatedAssetClassCode {
			t.Fatalf("asset %s class = %q, want %q", code, asset.AssetClass,
				autoCreatedAssetClassCode)
		}
		if got := fake.assetResolverIDs[code]; got != asset.EngineAssetID {
			t.Fatalf("live resolver id for %s = %d, want %d", code, got, asset.EngineAssetID)
		}
		if code == instrument.BaseAsset {
			instrument.BaseAssetID = asset.EngineAssetID
		} else {
			instrument.QuoteAssetID = asset.EngineAssetID
		}
	}
	instruments, err := n.realm.ListMarketDataInstruments(ctx, instance.ExternalID)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments: %v", err)
	}
	if len(instruments) != 1 || instruments[0] != instrument {
		t.Fatalf("instruments = %+v, want %+v", instruments, instrument)
	}
	assertMarketDataAssetAudits(t, ctx, n.realm, "BTC", "USDT")
	rows, err := n.realm.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionSetMarketData},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(set market data): %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "BTCUSDT") {
		t.Fatalf("set-market-data audit rows = %+v, want BTCUSDT upsert", rows)
	}
}

func TestUpsertMarketDataInstrumentRejectedByRestartWritesNothing(t *testing.T) {
	t.Parallel()
	n, realm := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	instance, err := realm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBinance,
		Label:    "Binance",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	n.restarting.Store(true)
	t.Cleanup(func() { n.restarting.Store(false) })
	err = n.UpsertMarketDataInstrument(ctx, domain.MarketDataInstrument{
		Instance:       instance.ExternalID,
		ExternalSymbol: "BTCUSDT",
		BaseAsset:      "BTC",
		QuoteAsset:     "USDT",
		Enabled:        true,
	}, testCaller)
	if !errors.Is(err, domain.ErrEngineRestarting) {
		t.Fatalf("UpsertMarketDataInstrument error = %v, want ErrEngineRestarting", err)
	}

	for _, code := range []string{"BTC", "USDT"} {
		if _, ok, err := realm.GetAsset(ctx, code); err != nil || ok {
			t.Fatalf("GetAsset(%s) = ok %v, err %v; want no partial asset",
				code, ok, err)
		}
	}
	instruments, err := realm.ListMarketDataInstruments(ctx, instance.ExternalID)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments: %v", err)
	}
	if len(instruments) != 0 {
		t.Fatalf("instruments = %+v, want no partial instrument", instruments)
	}
	rows, err := realm.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{
			domain.AuditActionCreateAsset,
			domain.AuditActionSetMarketData,
		},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("mutation audit rows = %+v, want none", rows)
	}
}

func assertMarketDataAssetAudits(
	t *testing.T, ctx context.Context, realm store.RealmStore, codes ...string,
) {
	t.Helper()
	rows, err := realm.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAsset},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create asset): %v", err)
	}
	if len(rows) != len(codes) {
		t.Fatalf("create-asset audit rows = %+v, want %v", rows, codes)
	}
	for _, code := range codes {
		found := false
		for _, row := range rows {
			if row.Asset == code && strings.Contains(
				row.Detail, "by market-data instrument upsert",
			) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("create-asset audit rows = %+v, want %s detail", rows, code)
		}
	}
}

type marketDataTestEngine struct {
	engine.Engine
	sink marketdata.Sink
}

func (e *marketDataTestEngine) MarketDataSink() marketdata.Sink { return e.sink }

type marketDataTestSink struct{}

func (*marketDataTestSink) Push(marketdata.QuoteUpdate) error { return nil }
