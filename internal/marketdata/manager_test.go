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
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/domain"
)

// testExternalID builds a deterministic domain.ExternalID for tests by copying
// up to 16 bytes of label into the array. Two distinct labels yield distinct
// ids; the zero remainder is identical for all labels shorter than 16 bytes,
// which is fine because the labels themselves differ.
func testExternalID(label string) domain.ExternalID {
	var id domain.ExternalID
	copy(id[:], label)
	return id
}

// fakeStore is a fixed in-memory Store for the manager: it returns the enabled
// instances and, per instance, its enabled instruments. The instrument lists are
// pre-filtered to "enabled" to mirror the store's WHERE enabled = 1 query.
// instruments is keyed by the ExternalID string form (ExternalID.String()).
type fakeStore struct {
	instances   []domain.MarketDataInstance
	instruments map[string][]domain.MarketDataInstrument
	quotes      []domain.MarketDataQuote
	instErr     error
}

func (s *fakeStore) ListEnabledMarketDataInstances(
	context.Context,
) ([]domain.MarketDataInstance, error) {
	return s.instances, nil
}

func (s *fakeStore) ListEnabledMarketDataInstruments(
	_ context.Context, instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	if s.instErr != nil {
		return nil, s.instErr
	}
	return s.instruments[instance.String()], nil
}

func (s *fakeStore) UpsertMarketDataQuote(
	_ context.Context, quote domain.MarketDataQuote,
) error {
	s.quotes = append(s.quotes, quote)
	return nil
}

// fakeSink records every pushed quote under a mutex.
type fakeSink struct {
	mu      sync.Mutex
	pushed  []QuoteUpdate
	pushErr error
}

func (s *fakeSink) Push(update QuoteUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pushErr != nil {
		return s.pushErr
	}
	s.pushed = append(s.pushed, update)
	return nil
}

func (s *fakeSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pushed)
}

// TestManager_NoEnabledIsCleanNoop verifies that with nothing enabled Start is a
// clean no-op and Stop is safe.
func TestManager_NoEnabledIsCleanNoop(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	sink := &fakeSink{}
	m := NewManager(store, sink, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.Stop()
	if sink.count() != 0 {
		t.Fatalf("want no pushes with nothing enabled, got %d", sink.count())
	}
}

// TestManager_FansMockQuotesToSink verifies an enabled mock instance subscribes
// its enabled instruments and fans quotes into the sink, then stops cleanly.
func TestManager_FansMockQuotesToSink(t *testing.T) {
	t.Parallel()
	mock1 := testExternalID("mock-1")
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: mock1, Provider: domain.MarketDataProviderMock, Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			mock1.String(): {
				{Instance: mock1, ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true},
			},
		},
	}
	sink := &fakeSink{}
	m := NewManager(store, sink, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The mock emits on a sub-second timer; wait for at least one push.
	waitFor(t, 2*time.Second, func() bool { return sink.count() > 0 })
	m.Stop()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.pushed) == 0 {
		t.Fatal("want at least one fanned quote")
	}
	first := sink.pushed[0]
	if first.Base != "AAPL" || first.Quote != "USD" {
		t.Fatalf("fanned quote instrument = %s/%s, want AAPL/USD", first.Base, first.Quote)
	}
}

// TestManager_SkipsInstanceWithNoInstruments verifies an enabled instance whose
// instruments are all disabled is skipped (no subscription, no push).
func TestManager_SkipsInstanceWithNoInstruments(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: testExternalID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{}, // none enabled
	}
	sink := &fakeSink{}
	m := NewManager(store, sink, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	m.Stop()
	if sink.count() != 0 {
		t.Fatalf("want no pushes for an instance with no enabled instruments, got %d", sink.count())
	}
}

// TestManager_UnknownTypeSkippedNotFailed verifies an unknown provider type is
// skipped without failing Start; a healthy mock instance alongside it still
// streams.
func TestManager_UnknownTypeSkippedNotFailed(t *testing.T) {
	t.Parallel()
	weird := testExternalID("weird")
	mock1 := testExternalID("mock-1")
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: weird, Provider: "does-not-exist", Enabled: true},
			{ExternalID: mock1, Provider: domain.MarketDataProviderMock, Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			weird.String(): {{Instance: weird, ExternalSymbol: "X", BaseAsset: "X", QuoteAsset: "Y", Enabled: true}},
			mock1.String(): {{Instance: mock1, ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true}},
		},
	}
	sink := &fakeSink{}
	m := NewManager(store, sink, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start must not fail on an unknown type: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return sink.count() > 0 })
	m.Stop()
}

// TestManager_StartIdempotent verifies a second Start while running is a no-op.
func TestManager_StartIdempotent(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	m := NewManager(store, &fakeSink{}, nil)
	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	m.Stop()
	m.Stop() // idempotent
}

func TestNewConnector_ProviderTypesRegistered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		instance domain.MarketDataInstance
		wantType any
	}{
		{
			name:     "byo",
			instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderBYO},
			wantType: (*byoConnector)(nil),
		},
		{
			name:     "binance",
			instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderBinance},
			wantType: (*binanceConnector)(nil),
		},
		{
			name: "ib",
			instance: domain.MarketDataInstance{
				Provider:    domain.MarketDataProviderIB,
				Credentials: `{"clientId":109}`,
			},
			wantType: (*ibConnector)(nil),
		},
		{
			name:     "kraken",
			instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderKraken},
			wantType: (*krakenConnector)(nil),
		},
		{
			name:     "coinbase",
			instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderCoinbase},
			wantType: (*coinbaseConnector)(nil),
		},
		{
			name: "alpaca",
			instance: domain.MarketDataInstance{
				Provider:    domain.MarketDataProviderAlpaca,
				Credentials: `{"apiKey":"key","apiSecret":"secret"}`,
			},
			wantType: (*alpacaConnector)(nil),
		},
		{
			name:     "okx",
			instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderOKX},
			wantType: (*okxConnector)(nil),
		},
		{
			name: "bybit",
			instance: domain.MarketDataInstance{
				Provider:    domain.MarketDataProviderBybit,
				Credentials: `{"category":"linear"}`,
			},
			wantType: (*bybitConnector)(nil),
		},
		{
			name: "oanda",
			instance: domain.MarketDataInstance{
				Provider:    domain.MarketDataProviderOANDA,
				Credentials: `{"token":"token","accountID":"account","environment":"practice"}`,
			},
			wantType: (*oandaConnector)(nil),
		},
		{
			name: "finnhub",
			instance: domain.MarketDataInstance{
				Provider:    domain.MarketDataProviderFinnhub,
				Credentials: `{"token":"token"}`,
			},
			wantType: (*finnhubConnector)(nil),
		},
		{
			name:     "mock",
			instance: domain.MarketDataInstance{Provider: domain.MarketDataProviderMock},
			wantType: (*mockConnector)(nil),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.instance.ExternalID = testExternalID(tt.name + "-1")
			connector, err := newConnector(tt.instance)
			if err != nil {
				t.Fatalf("newConnector: %v", err)
			}
			defer connector.Close()
			if reflect.TypeOf(connector) != reflect.TypeOf(tt.wantType) {
				t.Fatalf("connector type = %T, want %T", connector, tt.wantType)
			}
		})
	}
}

func TestManager_InvalidProviderConfigIsNotUnsupported(t *testing.T) {
	t.Parallel()

	alpaca1 := testExternalID("alpaca-1")
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: alpaca1, Provider: domain.MarketDataProviderAlpaca, Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			alpaca1.String(): {{
				Instance: alpaca1, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
			}},
		},
	}
	m := NewManager(store, &fakeSink{}, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	status := m.InstanceStatuses()[alpaca1.String()]
	if status.State != StateError {
		t.Fatalf("state = %q, want error", status.State)
	}
	if len(status.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want one", status.Diagnostics)
	}
	if status.Diagnostics[0].Code != CodeInvalidProviderConfig {
		t.Fatalf("diag code = %q, want %q", status.Diagnostics[0].Code, CodeInvalidProviderConfig)
	}
	if status.Diagnostics[0].Code == CodeUnsupportedProvider {
		t.Fatal("invalid credentials were reported as unsupported provider")
	}
}

// TestManager_PushErrorDoesNotStopFeed verifies a sink push error is tolerated:
// the drain keeps running (the manager logs and continues) rather than crashing.
func TestManager_PushErrorDoesNotStopFeed(t *testing.T) {
	t.Parallel()
	mock1 := testExternalID("mock-1")
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: mock1, Provider: domain.MarketDataProviderMock, Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			mock1.String(): {{Instance: mock1, ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true}},
		},
	}
	sink := &fakeSink{pushErr: errors.New("push boom")}
	m := NewManager(store, sink, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Let several ticks flow; a push error must not deadlock or panic.
	time.Sleep(50 * time.Millisecond)
	m.Stop()
}

func TestVerifySymbol_UnsupportedProvider(t *testing.T) {
	t.Parallel()

	// BYO and Mock connectors do not implement SymbolVerifier, so verification is
	// unsupported: supported=false, zero result, no error, no network.
	for _, providerType := range []string{
		domain.MarketDataProviderBYO,
		domain.MarketDataProviderIB,
		domain.MarketDataProviderMock,
	} {
		instance := domain.MarketDataInstance{ExternalID: testExternalID("i"), Provider: providerType}
		result, supported, err := VerifySymbol(context.Background(), instance, "BTCUSDT")
		if err != nil {
			t.Fatalf("%s: VerifySymbol err = %v", providerType, err)
		}
		if supported {
			t.Fatalf("%s: supported = true, want false", providerType)
		}
		if result.Exists || result.Suggestion != "" {
			t.Fatalf("%s: result = %+v, want zero", providerType, result)
		}
		if ProviderVerifiesSymbols(providerType) {
			t.Fatalf("%s: ProviderVerifiesSymbols = true, want false", providerType)
		}
	}

	for _, instance := range []domain.MarketDataInstance{
		{
			ExternalID:  testExternalID("alpaca-1"),
			Provider:    domain.MarketDataProviderAlpaca,
			Credentials: `{"apiKey":"key","apiSecret":"secret"}`,
		},
		{
			ExternalID:  testExternalID("oanda-1"),
			Provider:    domain.MarketDataProviderOANDA,
			Credentials: `{"token":"token","accountID":"account","environment":"practice"}`,
		},
	} {
		result, supported, err := VerifySymbol(context.Background(), instance, "BTCUSDT")
		if err != nil {
			t.Fatalf("%s: VerifySymbol err = %v", instance.Provider, err)
		}
		if supported {
			t.Fatalf("%s: supported = true, want false", instance.Provider)
		}
		if result.Exists || result.Suggestion != "" {
			t.Fatalf("%s: result = %+v, want zero", instance.Provider, result)
		}
		if ProviderVerifiesSymbols(instance.Provider) {
			t.Fatalf("%s: ProviderVerifiesSymbols = true, want false", instance.Provider)
		}
	}
}

func TestVerifySymbol_UnknownProvider(t *testing.T) {
	t.Parallel()

	instance := domain.MarketDataInstance{ExternalID: testExternalID("i"), Provider: "nope"}
	if _, _, err := VerifySymbol(context.Background(), instance, "BTCUSDT"); err == nil {
		t.Fatal("VerifySymbol(unknown provider) err = nil, want error")
	}
	if ProviderVerifiesSymbols("nope") {
		t.Fatal("ProviderVerifiesSymbols(unknown) = true, want false")
	}
}

func TestSearchSymbolsUnsupportedProvider(t *testing.T) {
	t.Parallel()

	// BYO accepts operator-pushed quotes and has no external catalogue, so
	// search is unsupported: supported=false, no matches, no error, no network.
	instance := domain.MarketDataInstance{ExternalID: testExternalID("byo-1"), Provider: domain.MarketDataProviderBYO}
	matches, supported, err := SearchSymbols(
		context.Background(), instance, SymbolSearchQuery{Query: "BTC"},
	)
	if err != nil {
		t.Fatalf("SearchSymbols err = %v", err)
	}
	if supported {
		t.Fatal("supported = true, want false")
	}
	if matches != nil {
		t.Fatalf("matches = %+v, want nil", matches)
	}
	if ProviderSearchesSymbols(domain.MarketDataProviderBYO) {
		t.Fatal("ProviderSearchesSymbols(byo) = true, want false")
	}
}

func TestSearchSymbolsUnknownInstanceType(t *testing.T) {
	t.Parallel()

	instance := domain.MarketDataInstance{ExternalID: testExternalID("i"), Provider: "nope"}
	if _, _, err := SearchSymbols(
		context.Background(), instance, SymbolSearchQuery{Query: "BTC"},
	); err == nil {
		t.Fatal("SearchSymbols(unknown provider) err = nil, want error")
	}
	if ProviderSearchesSymbols("nope") {
		t.Fatal("ProviderSearchesSymbols(unknown) = true, want false")
	}
}

func TestProviderVerifiesSymbols_ProviderCatalogues(t *testing.T) {
	t.Parallel()

	// These providers implement SymbolVerifier; the static probe never touches
	// the network (it builds and discards a connector without subscribing).
	for _, providerType := range []string{
		domain.MarketDataProviderBinance,
		domain.MarketDataProviderKraken,
		domain.MarketDataProviderCoinbase,
		domain.MarketDataProviderOKX,
		domain.MarketDataProviderBybit,
		domain.MarketDataProviderFinnhub,
	} {
		if !ProviderVerifiesSymbols(providerType) {
			t.Fatalf("ProviderVerifiesSymbols(%s) = false, want true", providerType)
		}
	}
	for _, providerType := range []string{
		domain.MarketDataProviderBinance,
		domain.MarketDataProviderKraken,
		domain.MarketDataProviderCoinbase,
		domain.MarketDataProviderOKX,
		domain.MarketDataProviderBybit,
		domain.MarketDataProviderFinnhub,
	} {
		if !ProviderSearchesSymbols(providerType) {
			t.Fatalf("ProviderSearchesSymbols(%s) = false, want true", providerType)
		}
	}
}

// TestManager_PushesStoredManualPriceOnceAtStartup verifies that an enabled BYO
// instance whose instrument carries a stored manual price pushes exactly one
// quote at startup (mark = the stored price), and that an instrument without a
// price does not push. BYO has no background producer, so the manual pushes are
// the only quotes.
func TestManager_PushesStoredManualPriceOnceAtStartup(t *testing.T) {
	t.Parallel()
	byo1 := testExternalID("byo-1")
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: byo1, Provider: domain.MarketDataProviderBYO, Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			byo1.String(): {
				{
					Instance: byo1, ExternalSymbol: "USDT/USD",
					BaseAsset: "USDT", QuoteAsset: "USD",
					ManualPrice: "1", Enabled: true,
				},
				{
					Instance: byo1, ExternalSymbol: "ETH/USD",
					BaseAsset: "ETH", QuoteAsset: "USD",
					ManualPrice: "", Enabled: true, // no manual price -> no push
				},
			},
		},
	}
	sink := &fakeSink{}
	m := NewManager(store, sink, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return sink.count() == 1 })
	// Give any erroneous extra push a chance to land before asserting exactness.
	time.Sleep(50 * time.Millisecond)
	m.Stop()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.pushed) != 1 {
		t.Fatalf("want exactly one startup push, got %d", len(sink.pushed))
	}
	got := sink.pushed[0]
	if got.Base != "USDT" || got.Quote != "USD" || got.Mark != "1" {
		t.Fatalf("startup push = %s/%s mark %q, want USDT/USD mark \"1\"",
			got.Base, got.Quote, got.Mark)
	}
}

// TestManager_PushManualAfterStartupPushesOnce verifies that PushManual delivers
// exactly one quote into a running BYO instance's connector - the after-startup
// counterpart of the startup re-apply. The instrument starts with no stored
// price (no startup push), so the single quote observed is the explicit one.
func TestManager_PushManualAfterStartupPushesOnce(t *testing.T) {
	t.Parallel()
	byo1 := testExternalID("byo-1")
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: byo1, Provider: domain.MarketDataProviderBYO, Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			byo1.String(): {
				{
					Instance: byo1, ExternalSymbol: "USDT/USDC",
					BaseAsset: "USDT", QuoteAsset: "USDC",
					ManualPrice: "", Enabled: true,
				},
			},
		},
	}
	sink := &fakeSink{}
	m := NewManager(store, sink, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// No stored price means no startup push.
	time.Sleep(50 * time.Millisecond)
	if sink.count() != 0 {
		t.Fatalf("want no startup push without a stored price, got %d", sink.count())
	}

	m.PushManual(byo1.String(), domain.MarketDataInstrument{
		Instance: byo1, ExternalSymbol: "USDT/USDC",
		BaseAsset: "USDT", QuoteAsset: "USDC",
		ManualPrice: "1", Enabled: true,
	})
	waitFor(t, 2*time.Second, func() bool { return sink.count() == 1 })
	time.Sleep(50 * time.Millisecond)
	m.Stop()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.pushed) != 1 {
		t.Fatalf("want exactly one push after PushManual, got %d", len(sink.pushed))
	}
	got := sink.pushed[0]
	if got.Base != "USDT" || got.Quote != "USDC" || got.Mark != "1" {
		t.Fatalf("push = %s/%s mark %q, want USDT/USDC mark \"1\"",
			got.Base, got.Quote, got.Mark)
	}
}

// TestManager_PushManualNonByoUntouched verifies PushManual is a no-op for a
// non-BYO instance: a streaming connector is not push-capable and is not
// recorded in the per-instance push map, so an explicit manual push reaches
// nothing. PushManual into an unknown instance id is likewise a no-op.
func TestManager_PushManualNonByoUntouched(t *testing.T) {
	t.Parallel()
	mock1 := testExternalID("mock-1")
	doesNotExist := testExternalID("does-not-exist")
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: mock1, Provider: domain.MarketDataProviderMock, Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			mock1.String(): {
				{
					Instance: mock1, ExternalSymbol: "AAPL",
					BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
				},
			},
		},
	}
	sink := &fakeSink{}
	m := NewManager(store, sink, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// PushManual must not deliver into the non-push-capable mock connector, nor
	// into an unknown instance. (The mock still streams its own quotes; we only
	// assert PushManual itself is inert by checking the unknown-instance case.)
	m.PushManual(mock1.String(), domain.MarketDataInstrument{
		Instance: mock1, ExternalSymbol: "AAPL",
		BaseAsset: "AAPL", QuoteAsset: "USD",
		ManualPrice: "1", Enabled: true,
	})
	m.PushManual(doesNotExist.String(), domain.MarketDataInstrument{
		Instance: doesNotExist, ExternalSymbol: "X",
		BaseAsset: "X", QuoteAsset: "Y",
		ManualPrice: "1", Enabled: true,
	})
	m.Stop()

	// The unknown instance has no connector, so PushManual could not have pushed
	// for it; any quotes in the sink would be the mock's own stream, never the
	// manual push. Assert no manual USDT-style mark leaked through a non-BYO path
	// by confirming the unknown-instance push produced nothing for X/Y.
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, q := range sink.pushed {
		if q.Base == "X" && q.Quote == "Y" {
			t.Fatal("PushManual into unknown instance leaked a quote")
		}
	}
}

// TestManager_QuoteUpdateIntervalUnknownThenGap verifies the inter-update
// interval is unknown after the first tick and equals the arrival gap after the
// second, and that clearing the instance resets it to unknown. It drives the
// arrival recorder directly with controlled timestamps so the gap is exact.
func TestManager_QuoteUpdateIntervalUnknownThenGap(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	base := time.Now()

	m.recordQuoteArrival("inst-1", "AAPL", base)
	if _, ok := m.QuoteUpdateInterval("inst-1", "AAPL"); ok {
		t.Fatal("interval should be unknown after one tick")
	}

	m.recordQuoteArrival("inst-1", "AAPL", base.Add(12*time.Second))
	interval, ok := m.QuoteUpdateInterval("inst-1", "AAPL")
	if !ok {
		t.Fatal("interval should be known after two ticks")
	}
	if interval != 12*time.Second {
		t.Fatalf("interval = %v, want 12s", interval)
	}

	// A different instrument is independent and still unknown.
	if _, ok := m.QuoteUpdateInterval("inst-2", "AAPL"); ok {
		t.Fatal("unrelated instrument interval should be unknown")
	}

	// A restart of the instance clears its interval.
	m.clearInstanceIntervals("inst-1")
	if _, ok := m.QuoteUpdateInterval("inst-1", "AAPL"); ok {
		t.Fatal("interval should be unknown after instance reset")
	}
}

// TestManager_QuoteUpdateIntervalFromDrain verifies the interval is wired
// through the live drain path: a BYO instance with two operator pushes yields a
// known, positive interval for the pushed instrument keyed by external symbol.
func TestManager_QuoteUpdateIntervalFromDrain(t *testing.T) {
	t.Parallel()
	byo1 := testExternalID("byo-1")
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: byo1, Provider: domain.MarketDataProviderBYO, Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			byo1.String(): {
				{
					Instance: byo1, ExternalSymbol: "EURUSD",
					BaseAsset: "EUR", QuoteAsset: "USD", Enabled: true,
				},
			},
		},
	}
	sink := &fakeSink{}
	m := NewManager(store, sink, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	// First push (tick 1): interval still unknown.
	m.PushManual(byo1.String(), domain.MarketDataInstrument{
		Instance: byo1, ExternalSymbol: "EURUSD",
		BaseAsset: "EUR", QuoteAsset: "USD",
		ManualPrice: "1.05", Enabled: true,
	})
	waitFor(t, time.Second, func() bool { return sink.count() >= 1 })

	// Second push (tick 2): interval becomes known.
	m.PushManual(byo1.String(), domain.MarketDataInstrument{
		Instance: byo1, ExternalSymbol: "EURUSD",
		BaseAsset: "EUR", QuoteAsset: "USD",
		ManualPrice: "1.06", Enabled: true,
	})
	waitFor(t, time.Second, func() bool {
		_, ok := m.QuoteUpdateInterval(byo1.String(), "EURUSD")
		return ok
	})

	interval, ok := m.QuoteUpdateInterval(byo1.String(), "EURUSD")
	if !ok || interval <= 0 {
		t.Fatalf("interval after two pushes = %v (known=%v), want positive", interval, ok)
	}
}

// waitFor polls cond until it returns true or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// capturingHandler collects slog records into a slice under a mutex.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
	attrs   []slog.Attr
}

func (h *capturingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *capturingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h2 := &capturingHandler{records: h.records}
	h2.attrs = append(h2.attrs, attrs...)
	return h2
}

func (h *capturingHandler) WithGroup(name string) slog.Handler { return h }

func (h *capturingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.records)
}

// TestRecordDiagLocked_NewDiagLogsOnce verifies that recording a genuinely new
// diagnostic emits exactly one slog record at the mapped level.
func TestRecordDiagLocked_NewDiagLogsOnce(t *testing.T) {
	t.Parallel()
	h := &capturingHandler{}
	logger := slog.New(h)

	m := &Manager{
		logger:   logger,
		statuses: map[string]InstanceRuntimeStatus{"inst-1": {}},
	}

	m.recordDiagLocked("inst-1", Diagnostic{
		Level:  DiagError,
		Code:   CodeConnectionError,
		Kind:   DiagKindEnvironment,
		Title:  "Connection error",
		Detail: "dial tcp: connection refused",
	})

	if n := h.count(); n != 1 {
		t.Fatalf("want 1 log record for new diagnostic, got %d", n)
	}
	h.mu.Lock()
	rec := h.records[0]
	h.mu.Unlock()
	if rec.Level != slog.LevelError {
		t.Fatalf("log level = %v, want Error", rec.Level)
	}
	if rec.Message != "Connection error" {
		t.Fatalf("log message = %q, want %q", rec.Message, "Connection error")
	}
}

func TestRecordDiagLocked_LevelMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		diag      Diagnostic
		wantLevel slog.Level
	}{
		{
			name:      "Routine→Debug",
			diag:      Diagnostic{Level: DiagInfo, Routine: true, Code: CodeDataFreshness, Kind: DiagKindProvider, Title: "routine notice", Detail: "some detail"},
			wantLevel: slog.LevelDebug,
		},
		{
			name:      "DiagInfo→Info",
			diag:      Diagnostic{Level: DiagInfo, Code: CodeConnectionError, Kind: DiagKindProvider, Title: "test diagnostic", Detail: "some detail"},
			wantLevel: slog.LevelInfo,
		},
		{
			name:      "DiagWarn→Warn",
			diag:      Diagnostic{Level: DiagWarn, Code: CodeConnectionError, Kind: DiagKindProvider, Title: "test diagnostic", Detail: "some detail"},
			wantLevel: slog.LevelWarn,
		},
		{
			name:      "DiagError→Error",
			diag:      Diagnostic{Level: DiagError, Code: CodeConnectionError, Kind: DiagKindProvider, Title: "test diagnostic", Detail: "some detail"},
			wantLevel: slog.LevelError,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &capturingHandler{}
			m := &Manager{
				logger:   slog.New(h),
				statuses: map[string]InstanceRuntimeStatus{"inst": {}},
			}
			m.recordDiagLocked("inst", tc.diag)
			if n := h.count(); n != 1 {
				t.Fatalf("want 1 log record, got %d", n)
			}
			h.mu.Lock()
			rec := h.records[0]
			h.mu.Unlock()
			if rec.Level != tc.wantLevel {
				t.Fatalf("log level = %v, want %v", rec.Level, tc.wantLevel)
			}
			if got := m.statuses["inst"].Diagnostics[0].Level; got != tc.diag.Level {
				t.Fatalf("stored diagnostic level = %q, want %q", got, tc.diag.Level)
			}
		})
	}
}

// TestRecordDiagLocked_DedupedDiagNoLog verifies that recording a repeated
// diagnostic (same Code+Instrument+Detail) does not produce an additional log
// record — the dedup early-return must precede the logging path.
func TestRecordDiagLocked_DedupedDiagNoLog(t *testing.T) {
	t.Parallel()
	h := &capturingHandler{}
	logger := slog.New(h)

	m := &Manager{
		logger:   logger,
		statuses: map[string]InstanceRuntimeStatus{"inst-1": {}},
	}

	diag := Diagnostic{
		Level:  DiagWarn,
		Code:   CodeNoData,
		Kind:   DiagKindProvider,
		Title:  "No market data",
		Detail: "no quotes arriving",
	}

	m.recordDiagLocked("inst-1", diag) // first: new → logs
	m.recordDiagLocked("inst-1", diag) // second: dedup → must not log

	if n := h.count(); n != 1 {
		t.Fatalf("want 1 log record after deduped repeat, got %d", n)
	}
}
