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

// These tests exercise the OpenPit market-data binding and so require the native
// runtime dylib at run time (set OPENPIT_RUNTIME_LIBRARY_PATH or build the
// workspace dylib first, as documented for the Go bindings). They build one real
// market-data service through the engine builder and push quotes into it via the
// sink, then read them back through the binding to assert the mapping.

package native

import (
	"errors"
	"testing"
	"time"

	"go.openpit.dev/openpit"
	bindmd "go.openpit.dev/openpit/marketdata"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pkg/optional"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
)

// newTestSink builds a real market-data service (through the engine builder, as
// buildEngine does) and wraps it in the sink. The service is closed via
// t.Cleanup.
func newTestSink(t *testing.T) (*marketDataSink, *bindmd.Service) {
	t.Helper()
	service, err := openpit.NewEngineBuilder().FullSync().MarketData(defaultQuoteTTL).Build()
	if err != nil {
		t.Fatalf("build market-data service: %v", err)
	}
	t.Cleanup(service.Close)
	return newMarketDataSink(service, testResolver()), service
}

// readMark resolves instrument and reads its mark price as a decimal string,
// failing the test if no quote is found.
func readMark(t *testing.T, service *bindmd.Service, instrument param.Instrument) string {
	t.Helper()
	id, ok := service.Resolve(instrument)
	if !ok {
		t.Fatalf("Resolve: instrument not registered")
	}
	quote, err := service.Get(
		id,
		param.NewAccountIDFromUint64(1),
		noGroupAccountInfo{},
		bindmd.QuoteResolutionAccountThenGroupThenDefault,
	)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	mark, ok := quote.Mark().Get()
	if !ok {
		t.Fatalf("quote has no mark")
	}
	return mark.String()
}

// TestMarketDataSink_PushRegistersAndPushes verifies the first Push registers the
// instrument and stores the quote with only the present fields.
func TestMarketDataSink_PushRegistersAndPushes(t *testing.T) {
	t.Parallel()
	sink, service := newTestSink(t)

	update := marketdata.QuoteUpdate{Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "100.5", Bid: "100", Ask: "101"}
	if err := sink.Push(update); err != nil {
		t.Fatalf("Push: %v", err)
	}

	aapl, err := testResolver().asset("AAPL")
	if err != nil {
		t.Fatalf("resolve AAPL: %v", err)
	}
	usd, err := testResolver().asset("USD")
	if err != nil {
		t.Fatalf("resolve USD: %v", err)
	}
	instrument := param.NewInstrument(aapl, usd)
	if got := readMark(t, service, instrument); got != "100.5" {
		t.Fatalf("mark = %q, want 100.5", got)
	}
}

func TestMarketDataSink_RenameKeepsLiveFeedFlowing(t *testing.T) {
	t.Parallel()
	res, err := newIDResolver(nil, nil, []domain.Asset{
		{Code: "asset-old", EngineAssetID: 41},
		{Code: "USD", EngineAssetID: 42},
	})
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	service, err := openpit.NewEngineBuilder().FullSync().MarketData(defaultQuoteTTL).Build()
	if err != nil {
		t.Fatalf("build market-data service: %v", err)
	}
	t.Cleanup(service.Close)
	sink := newMarketDataSink(service, res)

	if err := sink.Push(marketdata.QuoteUpdate{
		Base: 41, Quote: 42, Mark: "100",
	}); err != nil {
		t.Fatalf("Push before rename: %v", err)
	}
	if err := res.renameAssetResolverEntry("asset-old", domain.Asset{
		Code: "asset-new", EngineAssetID: 41,
	}); err != nil {
		t.Fatalf("rename asset resolver entry: %v", err)
	}

	if err := sink.Push(marketdata.QuoteUpdate{
		Base: 41, Quote: 42, Mark: "101",
	}); err != nil {
		t.Fatalf("Push after rename: %v", err)
	}
	if got := len(sink.ids); got != 1 {
		t.Fatalf("instrument cache entries = %d, want 1", got)
	}
	base, err := res.assetByID(41)
	if err != nil {
		t.Fatalf("resolve base asset id: %v", err)
	}
	quote, err := res.assetByID(42)
	if err != nil {
		t.Fatalf("resolve quote asset id: %v", err)
	}
	if got := readMark(t, service, param.NewInstrument(base, quote)); got != "101" {
		t.Fatalf("mark after rename = %q, want 101", got)
	}
}

func TestInstrumentFrom_UsesDecimalEngineAssetIDs(t *testing.T) {
	t.Parallel()
	res, err := newIDResolver(nil, nil, []domain.Asset{
		{Code: "base-code", EngineAssetID: 41},
		{Code: "quote-code", EngineAssetID: 42},
	})
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	instrument, err := instrumentFrom(41, 42, res)
	if err != nil {
		t.Fatalf("instrumentFrom: %v", err)
	}
	if got := instrument.String(); got != "41/42" {
		t.Fatalf("engine instrument = %q, want decimal engine ids 41/42", got)
	}
}

// TestMarketDataSink_PushSecondQuoteSameInstrument verifies the cached id is
// reused on the second push (no ErrAlreadyRegistered surfaced) and the quote is
// replaced.
func TestMarketDataSink_PushSecondQuoteSameInstrument(t *testing.T) {
	t.Parallel()
	sink, service := newTestSink(t)

	if err := sink.Push(marketdata.QuoteUpdate{Base: testMarketDataAssetID("MSFT"), Quote: testMarketDataAssetID("USD"), Mark: "2000"}); err != nil {
		t.Fatalf("first Push: %v", err)
	}
	if err := sink.Push(marketdata.QuoteUpdate{Base: testMarketDataAssetID("MSFT"), Quote: testMarketDataAssetID("USD"), Mark: "2100"}); err != nil {
		t.Fatalf("second Push: %v", err)
	}

	msft, err := testResolver().asset("MSFT")
	if err != nil {
		t.Fatalf("resolve MSFT: %v", err)
	}
	usd, err := testResolver().asset("USD")
	if err != nil {
		t.Fatalf("resolve USD: %v", err)
	}
	if got := readMark(t, service, param.NewInstrument(msft, usd)); got != "2100" {
		t.Fatalf("mark = %q, want 2100 (replaced)", got)
	}
}

func TestMarketDataSink_SourceAgeExpiresOldQuoteImmediately(t *testing.T) {
	t.Parallel()
	sink, service := newTestSink(t)
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	sink.now = func() time.Time { return now }
	instrument, err := instrumentFrom(
		testMarketDataAssetID("AAPL"), testMarketDataAssetID("USD"), testResolver(),
	)
	if err != nil {
		t.Fatalf("instrumentFrom: %v", err)
	}

	if err := sink.Push(marketdata.QuoteUpdate{
		AsOf: now.Add(-MarketDataFreshnessTTL + time.Second),
		Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "100",
	}); err != nil {
		t.Fatalf("Push aged fresh quote: %v", err)
	}
	if got := readMark(t, service, instrument); got != "100" {
		t.Fatalf("aged fresh mark = %q, want 100", got)
	}

	if err := sink.Push(marketdata.QuoteUpdate{
		AsOf: now.Add(-MarketDataFreshnessTTL - time.Second),
		Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "101",
	}); err != nil {
		t.Fatalf("Push stale boundary quote: %v", err)
	}
	id, ok := service.Resolve(instrument)
	if !ok {
		t.Fatal("Resolve: instrument not registered")
	}
	quote, err := service.Get(
		id,
		param.NewAccountIDFromUint64(1),
		noGroupAccountInfo{},
		bindmd.QuoteResolutionAccountThenGroupThenDefault,
	)
	if !errors.Is(err, bindmd.ErrQuoteExpired) {
		t.Fatalf("Get stale boundary error = %v, want ErrQuoteExpired", err)
	}
	mark, ok := quote.Mark().Get()
	if !ok || mark.String() != "101" {
		t.Fatalf("expired quote mark = %v, %t, want retained 101", mark, ok)
	}

	if err := sink.Push(marketdata.QuoteUpdate{
		AsOf: now,
		Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "102",
	}); err != nil {
		t.Fatalf("Push fresh quote after stale: %v", err)
	}
	if got := readMark(t, service, instrument); got != "102" {
		t.Fatalf("fresh quote after stale mark = %q, want 102", got)
	}
}

func TestMarketDataSink_ClearRemovesStoredQuote(t *testing.T) {
	t.Parallel()
	sink, service := newTestSink(t)

	if err := sink.Push(marketdata.QuoteUpdate{
		Base: testMarketDataAssetID("EUR"), Quote: testMarketDataAssetID("USD"), Mark: "2",
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := sink.Clear(testMarketDataAssetID("EUR"), testMarketDataAssetID("USD")); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	eur, err := testResolver().asset("EUR")
	if err != nil {
		t.Fatalf("resolve EUR: %v", err)
	}
	usd, err := testResolver().asset("USD")
	if err != nil {
		t.Fatalf("resolve USD: %v", err)
	}
	id, ok := service.Resolve(param.NewInstrument(eur, usd))
	if !ok {
		t.Fatal("Resolve: cleared instrument was unregistered")
	}
	_, err = service.Get(
		id,
		param.NewAccountIDFromUint64(1),
		noGroupAccountInfo{},
		bindmd.QuoteResolutionAccountThenGroupThenDefault,
	)
	if !errors.Is(err, bindmd.ErrQuoteUnavailable) {
		t.Fatalf("Get after Clear error = %v, want ErrQuoteUnavailable", err)
	}
}

func TestOpenPitBuildFunc_RebuildPreservesPublishedQuoteWithoutStore(t *testing.T) {
	t.Parallel()
	build := NewOpenPitEngineBuildFunc("")
	snapshot := Snapshot{Assets: testAssets()}

	firstEngine, err := build(snapshot)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	first := firstEngine.(*openPitEngine)
	current := first
	t.Cleanup(func() {
		current.Stop()
		current.CloseMarketDataService()
	})

	if err := first.sink.Push(marketdata.QuoteUpdate{
		Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "185",
	}); err != nil {
		t.Fatalf("Push before rebuild: %v", err)
	}

	secondEngine, err := build(snapshot)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	second := secondEngine.(*openPitEngine)
	current = second
	first.Stop()

	instrument, err := instrumentFrom(
		testMarketDataAssetID("AAPL"), testMarketDataAssetID("USD"), testResolver(),
	)
	if err != nil {
		t.Fatalf("instrumentFrom: %v", err)
	}
	if got := readMark(t, second.marketDataService.service, instrument); got != "185" {
		t.Fatalf("mark after rebuild = %q, want 185", got)
	}
}

func TestOpenPitBuildFunc_RebuildUsesPublishedQuoteForMarketOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		asOf     time.Time
		wantPass bool
	}{
		{name: "fresh", wantPass: true},
		{
			name: "expired",
			asOf: time.Now().Add(-MarketDataFreshnessTTL - time.Minute),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			build := NewOpenPitEngineBuildFunc("")
			firstSnapshot := Snapshot{
				Assets:   testAssets(),
				Accounts: []domain.Account{account("acc-1")},
				Balances: []domain.Balance{
					fundedBalance("acc-1", "USD", "1000000"),
					fundedBalance("acc-1", "AAPL", "1000000"),
				},
			}

			firstEngine, err := build(firstSnapshot)
			if err != nil {
				t.Fatalf("first build: %v", err)
			}
			serviceCloser, ok := firstEngine.(interface {
				CloseMarketDataService()
			})
			if !ok {
				firstEngine.Stop()
				t.Fatal("first engine cannot close shared market-data service")
			}
			current := firstEngine
			t.Cleanup(func() {
				current.Stop()
				serviceCloser.CloseMarketDataService()
			})

			if err := firstEngine.MarketDataSink().Push(marketdata.QuoteUpdate{
				AsOf: test.asOf,
				Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "185",
			}); err != nil {
				t.Fatalf("Push before rebuild: %v", err)
			}

			secondSnapshot := firstSnapshot
			secondSnapshot.Accounts = append(
				append([]domain.Account(nil), firstSnapshot.Accounts...),
				account("acc-2"),
			)
			secondEngine, err := build(secondSnapshot)
			if err != nil {
				t.Fatalf("second build: %v", err)
			}
			current = secondEngine
			firstEngine.Stop()

			result, err := materializeCheckedOrder(
				secondEngine,
				checkProbe("acc-1", domain.OrderSideBuy, "1", ""),
			)
			if err != nil {
				t.Fatalf("CheckOrder after rebuild: %v", err)
			}
			if test.wantPass {
				if !result.Passed || result.WouldLockPrice == "" {
					t.Fatalf(
						"fresh market order after rebuild = %+v, want priced pass",
						result,
					)
				}
				return
			}
			if result.Passed || !hasRejectCode(
				result.Rejects, "mark_price_unavailable",
			) {
				t.Fatalf(
					"expired market order after rebuild = %+v, want mark_price_unavailable reject",
					result,
				)
			}
		})
	}
}

func TestOpenPitBuildFunc_SharedServiceClosesOnlyAtFinalShutdown(t *testing.T) {
	t.Parallel()
	build := NewOpenPitEngineBuildFunc("")
	snapshot := Snapshot{Assets: testAssets()}

	firstEngine, err := build(snapshot)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	first := firstEngine.(*openPitEngine)
	secondEngine, err := build(snapshot)
	if err != nil {
		first.Stop()
		first.CloseMarketDataService()
		t.Fatalf("second build: %v", err)
	}
	second := secondEngine.(*openPitEngine)
	first.Stop()

	update := marketdata.QuoteUpdate{
		Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "186",
	}
	if err := second.sink.Push(update); err != nil {
		second.Stop()
		second.CloseMarketDataService()
		t.Fatalf("Push after intermediate Stop: %v", err)
	}

	second.Stop()
	second.CloseMarketDataService()
	second.CloseMarketDataService()
	if err := second.sink.Push(update); !errors.Is(err, bindmd.ErrServiceClosed) {
		t.Fatalf("Push after final shutdown = %v, want ErrServiceClosed", err)
	}
}

func TestOpenPitBuildFunc_ReplacesClosedSharedService(t *testing.T) {
	t.Parallel()
	build := NewOpenPitEngineBuildFunc("")
	snapshot := Snapshot{Assets: testAssets()}
	var engines []*openPitEngine
	t.Cleanup(func() {
		for _, eng := range engines {
			eng.Stop()
		}
		for _, eng := range engines {
			eng.CloseMarketDataService()
		}
	})

	firstEngine, err := build(snapshot)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	first := firstEngine.(*openPitEngine)
	engines = append(engines, first)
	firstService := first.marketDataService
	first.CloseMarketDataService()
	if !firstService.isClosed() {
		t.Fatal("first service did not report closed")
	}

	secondEngine, err := build(snapshot)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	second := secondEngine.(*openPitEngine)
	engines = append(engines, second)
	if second.marketDataService == firstService {
		t.Fatal("second build reused the closed service")
	}
	if second.marketDataService.isClosed() {
		t.Fatal("replacement service reports closed")
	}

	thirdEngine, err := build(snapshot)
	if err != nil {
		t.Fatalf("third build: %v", err)
	}
	third := thirdEngine.(*openPitEngine)
	engines = append(engines, third)
	if third.marketDataService != second.marketDataService {
		t.Fatal("builds after rotation did not share the replacement service")
	}
}

func TestOpenPitBuildFunc_FailedRebuildKeepsSharedServiceLive(t *testing.T) {
	t.Parallel()
	build := NewOpenPitEngineBuildFunc("")
	snapshot := Snapshot{Assets: testAssets()}

	firstEngine, err := build(snapshot)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	first := firstEngine.(*openPitEngine)
	t.Cleanup(func() {
		first.Stop()
		first.CloseMarketDataService()
	})
	if err := first.sink.Push(marketdata.QuoteUpdate{
		Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "185",
	}); err != nil {
		t.Fatalf("Push before failed rebuild: %v", err)
	}

	invalid := snapshot
	invalid.RateLimits = []domain.LimitRate{
		rateLimit(domain.ScopeAccount, "missing", "", 1, time.Second),
	}
	if _, err := build(invalid); err == nil {
		t.Fatal("failed rebuild succeeded")
	}

	instrument, err := instrumentFrom(
		testMarketDataAssetID("AAPL"), testMarketDataAssetID("USD"), testResolver(),
	)
	if err != nil {
		t.Fatalf("instrumentFrom: %v", err)
	}
	if got := readMark(t, first.marketDataService.service, instrument); got != "185" {
		t.Fatalf("mark after failed rebuild = %q, want 185", got)
	}
}

func TestMarketDataSink_ClearUnknownInstrumentIsNoop(t *testing.T) {
	t.Parallel()
	sink, _ := newTestSink(t)
	if err := sink.Clear(testMarketDataAssetID("EUR"), testMarketDataAssetID("USD")); err != nil {
		t.Fatalf("Clear unknown instrument: %v", err)
	}
}

// TestMarketDataSink_InvalidAssetErrors verifies a malformed asset name surfaces
// as an error rather than a push.
func TestMarketDataSink_InvalidAssetErrors(t *testing.T) {
	t.Parallel()
	sink, _ := newTestSink(t)

	if err := sink.Push(marketdata.QuoteUpdate{Base: 0, Quote: testMarketDataAssetID("USD"), Mark: "1"}); err == nil {
		t.Fatal("Push with empty base asset: want error")
	}
}

// TestMarketDataSink_InvalidPriceErrors verifies a malformed decimal price
// surfaces as an error.
func TestMarketDataSink_InvalidPriceErrors(t *testing.T) {
	t.Parallel()
	sink, _ := newTestSink(t)

	if err := sink.Push(marketdata.QuoteUpdate{Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "not-a-number"}); err == nil {
		t.Fatal("Push with malformed mark price: want error")
	}
}

// noGroupAccountInfo is a minimal bindmd.AccountInfo whose reading account has no
// group, so quote reads fall through to the default bucket.
type noGroupAccountInfo struct{}

func (noGroupAccountInfo) AccountGroup() optional.Option[param.AccountGroupID] {
	return optional.None[param.AccountGroupID]()
}
