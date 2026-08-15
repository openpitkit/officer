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

package marketdata

import (
	"context"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

func TestInvertQuoteUsesMarketConvention(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, time.July, 17, 10, 30, 0, 0, time.UTC)
	got, ok := InvertQuote(QuoteUpdate{
		AsOf: asOf, Base: testMarketDataAssetID("EUR"), Quote: testMarketDataAssetID("USD"), Mark: "2", Bid: "4", Ask: "8",
	})
	if !ok {
		t.Fatal("InvertQuote ok = false, want true")
	}
	want := QuoteUpdate{
		AsOf: asOf, Base: testMarketDataAssetID("USD"), Quote: testMarketDataAssetID("EUR"), Mark: "0.5", Bid: "0.125", Ask: "0.25",
	}
	if got != want {
		t.Fatalf("InvertQuote = %+v, want %+v", got, want)
	}
}

func TestInvertQuoteOmitsMissingZeroAndInvalidFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		update QuoteUpdate
		want   QuoteUpdate
		ok     bool
	}{
		{
			name:   "bid becomes ask",
			update: QuoteUpdate{Base: testMarketDataAssetID("A"), Quote: testMarketDataAssetID("B"), Bid: "5"},
			want:   QuoteUpdate{Base: testMarketDataAssetID("B"), Quote: testMarketDataAssetID("A"), Ask: "0.2"},
			ok:     true,
		},
		{
			name:   "ask becomes bid",
			update: QuoteUpdate{Base: testMarketDataAssetID("A"), Quote: testMarketDataAssetID("B"), Ask: "4"},
			want:   QuoteUpdate{Base: testMarketDataAssetID("B"), Quote: testMarketDataAssetID("A"), Bid: "0.25"},
			ok:     true,
		},
		{
			name:   "zero and invalid skipped beside valid field",
			update: QuoteUpdate{Base: testMarketDataAssetID("A"), Quote: testMarketDataAssetID("B"), Mark: "0", Bid: "bad", Ask: "10"},
			want:   QuoteUpdate{Base: testMarketDataAssetID("B"), Quote: testMarketDataAssetID("A"), Bid: "0.1"},
			ok:     true,
		},
		{
			name:   "no invertible fields",
			update: QuoteUpdate{Base: testMarketDataAssetID("A"), Quote: testMarketDataAssetID("B"), Mark: "0", Bid: "bad"},
			want:   QuoteUpdate{Base: testMarketDataAssetID("B"), Quote: testMarketDataAssetID("A")},
			ok:     false,
		},
		{
			name:   "negative prices are not invertible",
			update: QuoteUpdate{Base: testMarketDataAssetID("A"), Quote: testMarketDataAssetID("B"), Mark: "-2"},
			want:   QuoteUpdate{Base: testMarketDataAssetID("B"), Quote: testMarketDataAssetID("A")},
			ok:     false,
		},
		{
			name:   "empty quote",
			update: QuoteUpdate{Base: testMarketDataAssetID("A"), Quote: testMarketDataAssetID("B")},
			want:   QuoteUpdate{Base: testMarketDataAssetID("B"), Quote: testMarketDataAssetID("A")},
			ok:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := InvertQuote(tt.update)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("InvertQuote = (%+v, %v), want (%+v, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestInvertQuotePreservesHugePriceReciprocal(t *testing.T) {
	t.Parallel()
	got, ok := InvertQuote(QuoteUpdate{
		Base: testMarketDataAssetID("A"), Quote: testMarketDataAssetID("B"), Mark: "100000000000000000",
	})
	if !ok {
		t.Fatal("InvertQuote ok = false, want a representable reciprocal")
	}
	if got.Mark != "0.00000000000000001" {
		t.Fatalf("inverted mark = %q, want exact non-zero reciprocal", got.Mark)
	}
}

func TestSubscriptionsForSwapsAssetIDsForInversePair(t *testing.T) {
	t.Parallel()
	original := testMarketDataInstrumentWithAssetIDs(domain.MarketDataInstrument{
		ExternalSymbol: "EURUSD",
		BaseAsset:      "EUR",
		QuoteAsset:     "USD",
	})
	inverse := testMarketDataInstrumentWithAssetIDs(domain.MarketDataInstrument{
		ExternalSymbol: "USDEUR",
		BaseAsset:      original.QuoteAsset,
		QuoteAsset:     original.BaseAsset,
		BaseAssetID:    original.QuoteAssetID,
		QuoteAssetID:   original.BaseAssetID,
	})

	subs := subscriptionsFor([]domain.MarketDataInstrument{original, inverse}, nil)
	if len(subs) != 2 {
		t.Fatalf("subscription count = %d, want 2", len(subs))
	}
	if got := subs[1]; got.Base != subs[0].Quote ||
		got.Quote != subs[0].Base {
		t.Fatalf("inverse subscription = %+v, original = %+v", got, subs[0])
	}
}

func TestManagerDeliversOriginalAndSyntheticAndStoresOnlyOriginal(t *testing.T) {
	t.Parallel()
	instanceID := testExternalID("mock-1")
	instance := domain.MarketDataInstance{
		ExternalID: instanceID, Provider: "mock", Enabled: true,
	}
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "EURUSD",
		BaseAsset: "EUR", QuoteAsset: "USD", Enabled: true,
	}
	instrument = testMarketDataInstrumentWithAssetIDs(instrument)
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "mock",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances:             []domain.MarketDataInstance{instance},
		instruments:           map[string][]domain.MarketDataInstrument{instanceID.String(): {instrument}},
		configuredInstances:   []domain.MarketDataInstance{instance},
		configuredInstruments: map[string][]domain.MarketDataInstrument{instanceID.String(): {instrument}},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()

	mustPush(t, connector, QuoteUpdate{
		Base: testMarketDataAssetID("EUR"), Quote: testMarketDataAssetID("USD"), Mark: "2", Bid: "4", Ask: "8",
	})
	waitFor(t, time.Second, func() bool { return sink.count() == 2 })
	got := sink.snapshot()
	if got[0].Base != testMarketDataAssetID("EUR") || got[1].Base != testMarketDataAssetID("USD") ||
		got[1].Mark != "0.5" || got[1].Bid != "0.125" || got[1].Ask != "0.25" {
		t.Fatalf("sink updates = %+v", got)
	}

	mustPush(t, connector, QuoteUpdate{
		Base: testMarketDataAssetID("EUR"), Quote: testMarketDataAssetID("USD"), Mark: "100000000000000000",
	})
	waitFor(t, time.Second, func() bool { return sink.count() == 4 })
	got = sink.snapshot()
	if got[3].Mark != "0.00000000000000001" {
		t.Fatalf("huge-price synthetic mark = %q, want non-zero reciprocal", got[3].Mark)
	}
	if store.quoteCount() != 2 {
		t.Fatalf("stored quote count = %d, want originals only", store.quoteCount())
	}
	applied := manager.AppliedConfig()[instanceID.String()].Subscriptions
	if len(applied) != 1 || !applied[0].SyntheticInverse {
		t.Fatalf("applied subscriptions = %+v, want synthetic inverse", applied)
	}
}

func TestManagerConfiguredReverseSuppressesUntilRemovalAndRestart(t *testing.T) {
	instanceID := testExternalID("byo-1")
	reverseID := testExternalID("dead-reverse")
	instance := domain.MarketDataInstance{
		ExternalID: instanceID, Provider: "byo", Enabled: true,
	}
	reverseInstance := domain.MarketDataInstance{
		ExternalID: reverseID, Provider: "mock", Enabled: false,
	}
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "EURUSD",
		BaseAsset: "EUR", QuoteAsset: "USD", Enabled: true,
	}
	instrument = testMarketDataInstrumentWithAssetIDs(instrument)
	reverse := domain.MarketDataInstrument{
		Instance: reverseID, ExternalSymbol: "USD/EUR",
		BaseAsset: "USD", QuoteAsset: "EUR", Enabled: false,
	}
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "byo",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return newBlockingPushConnector(), nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances:           []domain.MarketDataInstance{instance},
		instruments:         map[string][]domain.MarketDataInstrument{instanceID.String(): {instrument}},
		configuredInstances: []domain.MarketDataInstance{instance, reverseInstance},
		configuredInstruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument}, reverseID.String(): {reverse},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()

	manual := instrument
	manual.ManualPrice = "2"
	if err := manager.PushManual(context.Background(), instanceID.String(), manual); err != nil {
		t.Fatalf("PushManual: %v", err)
	}
	waitFor(t, time.Second, func() bool { return sink.count() == 1 })
	if manager.AppliedConfig()[instanceID.String()].Subscriptions[0].SyntheticInverse {
		t.Fatal("synthetic inverse active with configured reverse pair")
	}

	delete(store.configuredInstruments, reverseID.String())
	if err := manager.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if err := manager.PushManual(context.Background(), instanceID.String(), manual); err != nil {
		t.Fatalf("PushManual after Restart: %v", err)
	}
	waitFor(t, time.Second, func() bool { return sink.count() == 3 })
	got := sink.snapshot()
	if got[1].Mark != "2" || got[2].Base != testMarketDataAssetID("USD") || got[2].Quote != testMarketDataAssetID("EUR") ||
		got[2].Mark != "0.5" {
		t.Fatalf("post-restart sink updates = %+v", got)
	}
	if !manager.AppliedConfig()[instanceID.String()].Subscriptions[0].SyntheticInverse {
		t.Fatal("synthetic inverse inactive after reverse removal and restart")
	}
}

func TestManagerDoesNotEmitEmptySyntheticQuote(t *testing.T) {
	t.Parallel()
	instanceID := testExternalID("mock-1")
	instance := domain.MarketDataInstance{
		ExternalID: instanceID, Provider: "mock", Enabled: true,
	}
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "A/B",
		BaseAsset: "A", QuoteAsset: "B", Enabled: true,
	}
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "mock",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances:             []domain.MarketDataInstance{instance},
		instruments:           map[string][]domain.MarketDataInstrument{instanceID.String(): {instrument}},
		configuredInstances:   []domain.MarketDataInstance{instance},
		configuredInstruments: map[string][]domain.MarketDataInstrument{instanceID.String(): {instrument}},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()

	mustPush(t, connector, QuoteUpdate{Base: testMarketDataAssetID("A"), Quote: testMarketDataAssetID("B")})
	mustPush(t, connector, QuoteUpdate{Base: testMarketDataAssetID("A"), Quote: testMarketDataAssetID("B"), Mark: "0", Bid: "bad"})
	waitFor(t, time.Second, func() bool { return sink.count() == 2 })
	if got := sink.snapshot(); got[0].Base != testMarketDataAssetID("A") || got[1].Base != testMarketDataAssetID("A") {
		t.Fatalf("sink updates = %+v, want originals only", got)
	}
}
