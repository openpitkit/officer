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
	return newMarketDataSink(service), service
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

	update := marketdata.QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "100.5", Bid: "100", Ask: "101"}
	if err := sink.Push(update); err != nil {
		t.Fatalf("Push: %v", err)
	}

	aapl, _ := param.NewAsset("AAPL")
	usd, _ := param.NewAsset("USD")
	instrument := param.NewInstrument(aapl, usd)
	if got := readMark(t, service, instrument); got != "100.5" {
		t.Fatalf("mark = %q, want 100.5", got)
	}
}

// TestMarketDataSink_PushSecondQuoteSameInstrument verifies the cached id is
// reused on the second push (no ErrAlreadyRegistered surfaced) and the quote is
// replaced.
func TestMarketDataSink_PushSecondQuoteSameInstrument(t *testing.T) {
	t.Parallel()
	sink, service := newTestSink(t)

	if err := sink.Push(marketdata.QuoteUpdate{Base: "MSFT", Quote: "USD", Mark: "2000"}); err != nil {
		t.Fatalf("first Push: %v", err)
	}
	if err := sink.Push(marketdata.QuoteUpdate{Base: "MSFT", Quote: "USD", Mark: "2100"}); err != nil {
		t.Fatalf("second Push: %v", err)
	}

	msft, _ := param.NewAsset("MSFT")
	usd, _ := param.NewAsset("USD")
	if got := readMark(t, service, param.NewInstrument(msft, usd)); got != "2100" {
		t.Fatalf("mark = %q, want 2100 (replaced)", got)
	}
}

func TestMarketDataSink_SourceFreshnessPreservesExpiredQuote(t *testing.T) {
	t.Parallel()
	sink, service := newTestSink(t)
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	sink.now = func() time.Time { return now }
	instrument, err := instrumentFrom("AAPL", "USD")
	if err != nil {
		t.Fatalf("instrumentFrom: %v", err)
	}

	if err := sink.Push(marketdata.QuoteUpdate{
		AsOf: now.Add(-MarketDataFreshnessTTL + time.Second),
		Base: "AAPL", Quote: "USD", Mark: "100",
	}); err != nil {
		t.Fatalf("Push aged fresh quote: %v", err)
	}
	if got := readMark(t, service, instrument); got != "100" {
		t.Fatalf("aged fresh mark = %q, want 100", got)
	}

	if err := sink.Push(marketdata.QuoteUpdate{
		AsOf: now.Add(-MarketDataFreshnessTTL),
		Base: "AAPL", Quote: "USD", Mark: "101",
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
		Base: "AAPL", Quote: "USD", Mark: "102",
	}); err != nil {
		t.Fatalf("Push fresh quote after stale: %v", err)
	}
	if got := readMark(t, service, instrument); got != "102" {
		t.Fatalf("fresh quote after stale mark = %q, want 102", got)
	}
}

func TestQuoteSourceTTL(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		asOf      time.Time
		want      time.Duration
		wantBound bool
	}{
		{name: "missing source time uses default"},
		{name: "future source time uses default", asOf: now.Add(time.Second)},
		{
			name: "fresh source keeps only its remaining lifetime",
			asOf: now.Add(-MarketDataFreshnessTTL + time.Second),
			want: time.Second, wantBound: true,
		},
		{
			name:      "source boundary is expired",
			asOf:      now.Add(-MarketDataFreshnessTTL),
			wantBound: true,
		},
		{
			name:      "older source is expired",
			asOf:      now.Add(-MarketDataFreshnessTTL - time.Second),
			wantBound: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, bound := quoteSourceTTL(test.asOf, now)
			if got != test.want || bound != test.wantBound {
				t.Fatalf(
					"quoteSourceTTL() = (%s, %t), want (%s, %t)",
					got, bound, test.want, test.wantBound,
				)
			}
		})
	}
}

func TestMarketDataSink_ClearRemovesStoredQuote(t *testing.T) {
	t.Parallel()
	sink, service := newTestSink(t)

	if err := sink.Push(marketdata.QuoteUpdate{
		Base: "EUR", Quote: "USD", Mark: "2",
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := sink.Clear("EUR", "USD"); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	eur, _ := param.NewAsset("EUR")
	usd, _ := param.NewAsset("USD")
	id, ok := service.Resolve(param.NewInstrument(eur, usd))
	if !ok {
		t.Fatal("Resolve: cleared instrument was unregistered")
	}
	_, err := service.Get(
		id,
		param.NewAccountIDFromUint64(1),
		noGroupAccountInfo{},
		bindmd.QuoteResolutionAccountThenGroupThenDefault,
	)
	if !errors.Is(err, bindmd.ErrQuoteUnavailable) {
		t.Fatalf("Get after Clear error = %v, want ErrQuoteUnavailable", err)
	}
}

func TestMarketDataSink_ClearUnknownInstrumentIsNoop(t *testing.T) {
	t.Parallel()
	sink, _ := newTestSink(t)
	if err := sink.Clear("EUR", "USD"); err != nil {
		t.Fatalf("Clear unknown instrument: %v", err)
	}
}

// TestMarketDataSink_InvalidAssetErrors verifies a malformed asset name surfaces
// as an error rather than a push.
func TestMarketDataSink_InvalidAssetErrors(t *testing.T) {
	t.Parallel()
	sink, _ := newTestSink(t)

	if err := sink.Push(marketdata.QuoteUpdate{Base: "", Quote: "USD", Mark: "1"}); err == nil {
		t.Fatal("Push with empty base asset: want error")
	}
}

// TestMarketDataSink_InvalidPriceErrors verifies a malformed decimal price
// surfaces as an error.
func TestMarketDataSink_InvalidPriceErrors(t *testing.T) {
	t.Parallel()
	sink, _ := newTestSink(t)

	if err := sink.Push(marketdata.QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "not-a-number"}); err == nil {
		t.Fatal("Push with malformed mark price: want error")
	}
}

// noGroupAccountInfo is a minimal bindmd.AccountInfo whose reading account has no
// group, so quote reads fall through to the default bucket.
type noGroupAccountInfo struct{}

func (noGroupAccountInfo) AccountGroup() optional.Option[param.AccountGroupID] {
	return optional.None[param.AccountGroupID]()
}
