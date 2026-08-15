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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.openpit.dev/officer/framework/domain"
)

func TestParseFinnhubCredentials(t *testing.T) {
	t.Parallel()

	connector, err := NewFinnhubConnector(domain.MarketDataInstance{
		Credentials: `{"token":" token "}`,
	})
	if err != nil {
		t.Fatalf("NewFinnhubConnector: %v", err)
	}
	if connector.token != "token" {
		t.Fatalf("token = %q, want token", connector.token)
	}
}

func TestParseFinnhubCredentialsRejectsMissingToken(t *testing.T) {
	t.Parallel()

	if _, err := NewFinnhubConnector(domain.MarketDataInstance{
		Credentials: `{}`,
	}); err == nil {
		t.Fatal("NewFinnhubConnector err = nil, want error")
	}
}

func TestNormalizeFinnhubSubscriptionsLimit(t *testing.T) {
	t.Parallel()

	subs := make([]Subscription, finnhubSymbolLimit+1)
	for i := range subs {
		subs[i] = Subscription{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")}
	}
	if _, err := normalizeFinnhubSubscriptions(subs); err == nil {
		t.Fatal("normalizeFinnhubSubscriptions err = nil, want limit error")
	}
}

func TestFinnhubStreamURL(t *testing.T) {
	t.Parallel()

	got := finnhubStreamURL("key with space")
	if !strings.HasPrefix(got, finnhubWSBaseURL+"?") {
		t.Fatalf("stream URL = %q", got)
	}
	if !strings.Contains(got, "token=key+with+space") {
		t.Fatalf("stream URL = %q, want encoded token", got)
	}
}

func TestFinnhubVerifySymbolUsesSearchSymbol(t *testing.T) {
	t.Parallel()

	connector := &finnhubConnector{
		token: "token",
		now: func() time.Time {
			return time.Unix(1710000000, 0).UTC()
		},
		quote: func(_ context.Context, symbol, token string) (finnhubQuote, error) {
			if symbol != "AAPL" {
				t.Fatalf("quote symbol = %q, want AAPL", symbol)
			}
			if token != "token" {
				t.Fatalf("quote token = %q, want token", token)
			}
			return finnhubQuote{
				Current: json.RawMessage(`191.23`),
				Time:    1710000000,
			}, nil
		},
		search: func(_ context.Context, query, token string) ([]finnhubSearchResult, error) {
			if query != "AAPL" {
				t.Fatalf("query = %q, want AAPL", query)
			}
			if token != "token" {
				t.Fatalf("token = %q, want token", token)
			}
			return []finnhubSearchResult{
				{
					Description:   "APPLE INC",
					DisplaySymbol: "AAPL.SW",
					Symbol:        "AAPL.SW",
					Type:          "Common Stock",
				},
				{
					Description:   "APPLE INC",
					DisplaySymbol: "AAPL",
					Symbol:        "AAPL",
					Type:          "Common Stock",
				},
			}, nil
		},
	}

	got, err := connector.VerifySymbol(context.Background(), "AAPL")
	if err != nil {
		t.Fatalf("VerifySymbol: %v", err)
	}
	if !got.Exists || got.Suggestion != "" ||
		got.Details != "APPLE INC, Common Stock" {
		t.Fatalf("VerifySymbol = %+v, want exists with details", got)
	}
}

func TestFinnhubVerifySymbolRejectsStaleQuote(t *testing.T) {
	t.Parallel()

	connector := &finnhubConnector{
		token: "token",
		now: func() time.Time {
			return time.Unix(1710000000, 0).UTC().
				Add(finnhubSnapshotMaxAge + time.Second)
		},
		quote: func(context.Context, string, string) (finnhubQuote, error) {
			return finnhubQuote{
				Current: json.RawMessage(`191.23`),
				Time:    1710000000,
			}, nil
		},
		search: func(context.Context, string, string) ([]finnhubSearchResult, error) {
			return []finnhubSearchResult{
				{
					Description: "APPLE INC",
					Symbol:      "AAPL",
					Type:        "Common Stock",
				},
			}, nil
		},
	}

	got, err := connector.VerifySymbol(context.Background(), "AAPL")
	if err != nil {
		t.Fatalf("VerifySymbol: %v", err)
	}
	if got.Exists || got.Suggestion != "" ||
		got.Details != "APPLE INC, Common Stock; no fresh Finnhub quote" {
		t.Fatalf("VerifySymbol = %+v, want no fresh quote", got)
	}
}

func TestFinnhubVerifySymbolReportsUnquotableSymbol(t *testing.T) {
	t.Parallel()

	connector := &finnhubConnector{
		token: "token",
		now: func() time.Time {
			return time.Unix(1710000000, 0).UTC()
		},
		// A venue outside the API plan answers 403 on /quote.
		quote: func(context.Context, string, string) (finnhubQuote, error) {
			return finnhubQuote{}, errors.New(
				"finnhub quote: unexpected status 403",
			)
		},
		search: func(context.Context, string, string) ([]finnhubSearchResult, error) {
			return []finnhubSearchResult{
				{
					Description: "EURONET WORLDWIDE",
					Symbol:      "EUR.WA",
					Type:        "Common Stock",
				},
			}, nil
		},
	}

	got, err := connector.VerifySymbol(context.Background(), "EUR.WA")
	if err != nil {
		t.Fatalf("VerifySymbol err = %v, want nil despite quote 403", err)
	}
	// Recognized in the catalogue, but no accessible quote: a verdict, not an
	// error, so the operator sees a row reaction.
	if got.Exists || got.Suggestion != "" || got.Details == "" {
		t.Fatalf("VerifySymbol = %+v, want recognized-but-unquotable", got)
	}
}

func TestFinnhubVerifySymbolSuggestsCanonicalCase(t *testing.T) {
	t.Parallel()

	connector := &finnhubConnector{
		token: "token",
		search: func(_ context.Context, query, _ string) ([]finnhubSearchResult, error) {
			if query != "aapl" {
				t.Fatalf("query = %q, want aapl", query)
			}
			return []finnhubSearchResult{{Symbol: "AAPL"}}, nil
		},
	}

	got, err := connector.VerifySymbol(context.Background(), "aapl")
	if err != nil {
		t.Fatalf("VerifySymbol: %v", err)
	}
	if got.Exists || got.Suggestion != "AAPL" {
		t.Fatalf("VerifySymbol = %+v, want suggestion AAPL", got)
	}
}

func TestFinnhubSearchSymbolsMapsProviderSymbol(t *testing.T) {
	t.Parallel()

	connector := &finnhubConnector{
		token: "token",
		search: func(_ context.Context, query, _ string) ([]finnhubSearchResult, error) {
			if query != "AAPL" {
				t.Fatalf("query = %q, want AAPL", query)
			}
			return []finnhubSearchResult{
				{
					Description:   "APPLE INC",
					DisplaySymbol: "AAPL.SW",
					Symbol:        "AAPL.SW",
					Type:          "Common Stock",
				},
			}, nil
		},
	}

	got, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: " AAPL "},
	)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(got) != 1 || got[0].Symbol != "AAPL.SW" ||
		got[0].Name != "APPLE INC" || got[0].SecType != "Common Stock" {
		t.Fatalf("SearchSymbols = %+v", got)
	}
	if got[0].Exchange != "" {
		t.Fatalf("equity exchange = %q, want empty", got[0].Exchange)
	}
}

func TestFinnhubSearchSymbolsParsesExchangeFromQualifiedSymbol(t *testing.T) {
	t.Parallel()

	connector := &finnhubConnector{
		token: "token",
		search: func(context.Context, string, string) ([]finnhubSearchResult, error) {
			return []finnhubSearchResult{
				{
					Description:   "Binance ETHUSDT",
					DisplaySymbol: "ETH/USDT",
					Symbol:        "BINANCE:ETHUSDT",
					Type:          "Crypto",
				},
				{
					Description:   "Ethan Allen Interiors Inc",
					DisplaySymbol: "ETH",
					Symbol:        "ETH",
					Type:          "Common Stock",
				},
			}, nil
		},
	}

	got, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: "ETH"},
	)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("SearchSymbols len = %d, want 2", len(got))
	}
	// Exchange-qualified crypto carries the venue parsed from the prefix.
	if got[0].Symbol != "BINANCE:ETHUSDT" || got[0].Exchange != "BINANCE" {
		t.Fatalf("crypto match = %+v, want exchange BINANCE", got[0])
	}
	// A bare equity ticker has no venue prefix and reports none.
	if got[1].Symbol != "ETH" || got[1].Exchange != "" {
		t.Fatalf("equity match = %+v, want empty exchange", got[1])
	}
}

func TestFinnhubSearchSymbolsSurfacesCryptoCatalogue(t *testing.T) {
	t.Parallel()

	connector := &finnhubConnector{
		token: "token",
		// Finnhub's /search only ever returns equities for "ETH".
		search: func(context.Context, string, string) ([]finnhubSearchResult, error) {
			return []finnhubSearchResult{
				{
					Description:   "Ethan Allen Interiors Inc",
					DisplaySymbol: "ETH",
					Symbol:        "ETH",
					Type:          "ETP",
				},
			}, nil
		},
		cryptoSymbols: func(
			_ context.Context, exchange, _ string,
		) ([]finnhubCryptoSymbol, error) {
			if exchange != "BINANCE" {
				return nil, nil
			}
			return []finnhubCryptoSymbol{
				{
					Description:   "Binance ETH/USDT",
					DisplaySymbol: "ETH/USDT",
					Symbol:        "BINANCE:ETHUSDT",
				},
				{
					Description:   "Binance BTC/USDT",
					DisplaySymbol: "BTC/USDT",
					Symbol:        "BINANCE:BTCUSDT",
				},
			}, nil
		},
	}

	got, err := connector.SearchSymbols(
		context.Background(), SymbolSearchQuery{Query: "ETH"},
	)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("SearchSymbols len = %d, want >= 2", len(got))
	}
	// The crypto pair for the queried base surfaces first, venue-qualified.
	if got[0].Symbol != "BINANCE:ETHUSDT" ||
		got[0].Exchange != "BINANCE" || got[0].SecType != "Crypto" {
		t.Fatalf("first match = %+v, want BINANCE:ETHUSDT crypto", got[0])
	}
	// A non-matching crypto pair (BTC/USDT) is filtered out.
	for _, m := range got {
		if m.Symbol == "BINANCE:BTCUSDT" {
			t.Fatalf("unexpected non-matching crypto symbol: %+v", got)
		}
	}
	// The equity ticker is still present, after the crypto match.
	if last := got[len(got)-1]; last.Symbol != "ETH" {
		t.Fatalf("last match = %+v, want equity ETH", last)
	}
}

func TestFinnhubCryptoMatchRank(t *testing.T) {
	t.Parallel()

	sym := finnhubCryptoSymbol{
		DisplaySymbol: "ETH/USDT",
		Symbol:        "BINANCE:ETHUSDT",
	}
	for _, q := range []string{
		"ETH", "eth", "ETHUSDT", "ETH/USDT", "BINANCE:ETHUSDT",
	} {
		if _, ok := finnhubCryptoMatchRank(sym, q); !ok {
			t.Fatalf("query %q did not match ETH/USDT", q)
		}
	}
	for _, q := range []string{"BTC", "SOL", "AAPL", ""} {
		if _, ok := finnhubCryptoMatchRank(sym, q); ok {
			t.Fatalf("query %q unexpectedly matched ETH/USDT", q)
		}
	}
}

func TestQuoteUpdateFromFinnhubQuote(t *testing.T) {
	t.Parallel()

	sub := mustNormalizeFinnhubSubscriptions(t, []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})[0]

	got, ok := quoteUpdateFromFinnhubQuote(finnhubQuote{
		Current: json.RawMessage(`191.23`),
		Time:    1710000000,
	}, sub)
	if !ok {
		t.Fatal("quoteUpdateFromFinnhubQuote ok = false, want true")
	}
	if got.Mark != "191.23" || got.Base != testMarketDataAssetID("AAPL") || got.Quote != testMarketDataAssetID("USD") {
		t.Fatalf("QuoteUpdate = %+v", got)
	}
	if !got.AsOf.Equal(time.Unix(1710000000, 0).UTC()) {
		t.Fatalf("AsOf = %s", got.AsOf)
	}
}

func TestQuoteUpdateFromFinnhubQuoteRejectsMissingPrice(t *testing.T) {
	t.Parallel()

	sub := mustNormalizeFinnhubSubscriptions(t, []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})[0]
	if got, ok := quoteUpdateFromFinnhubQuote(finnhubQuote{
		Current: json.RawMessage(`0`),
		Time:    1710000000,
	}, sub); ok {
		t.Fatalf("quoteUpdateFromFinnhubQuote = %+v, want rejected", got)
	}
}

func TestFinnhubSnapshotStale(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	if finnhubSnapshotStale(now.Add(-finnhubSnapshotMaxAge), now) {
		t.Fatal("snapshot at max age is stale, want accepted")
	}
	if !finnhubSnapshotStale(now.Add(-finnhubSnapshotMaxAge-time.Second), now) {
		t.Fatal("old snapshot is fresh, want stale")
	}
}

func TestFinnhubSnapshotMaxAgeTracksFreshnessTTL(t *testing.T) {
	t.Parallel()

	if finnhubSnapshotMaxAge != FreshnessTTL {
		t.Fatalf(
			"finnhubSnapshotMaxAge = %s, want %s",
			finnhubSnapshotMaxAge,
			FreshnessTTL,
		)
	}
	if finnhubSnapshotMaxAge != 70*time.Second {
		t.Fatalf("finnhubSnapshotMaxAge = %s, want 70s", finnhubSnapshotMaxAge)
	}
}

func TestFinnhubReferencesPointToGeneralAPIDocs(t *testing.T) {
	t.Parallel()

	refs, ok := (&finnhubConnector{}).References()
	if !ok {
		t.Fatal("References ok = false, want true")
	}
	if refs.SymbolsURL != "https://finnhub.io/docs/api" {
		t.Fatalf("SymbolsURL = %q, want general API docs", refs.SymbolsURL)
	}
}

func TestFinnhubDiagnoseStaysSilentForKnownSymbol(t *testing.T) {
	t.Parallel()

	connector := &finnhubConnector{
		token: "token",
		subs: mustNormalizeFinnhubSubscriptions(t, []Subscription{
			{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
		}),
		search: func(_ context.Context, query, _ string) ([]finnhubSearchResult, error) {
			if query != "AAPL" {
				t.Fatalf("query = %q, want AAPL", query)
			}
			return []finnhubSearchResult{{Symbol: "AAPL"}}, nil
		},
	}

	findings, err := connector.Diagnose(context.Background())
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	// A recognized symbol that is merely silent must not raise a recurring
	// diagnostic; staleness/no-trades is checked on demand via verify.
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none for a known symbol", findings)
	}
}

func TestFinnhubDiagnoseReportsUnknownSymbol(t *testing.T) {
	t.Parallel()

	connector := &finnhubConnector{
		token: "token",
		subs: mustNormalizeFinnhubSubscriptions(t, []Subscription{
			{External: "BAD", Base: testMarketDataAssetID("BAD"), Quote: testMarketDataAssetID("USD")},
		}),
		search: func(context.Context, string, string) ([]finnhubSearchResult, error) {
			return nil, nil
		},
	}

	findings, err := connector.Diagnose(context.Background())
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(findings) != 1 || findings[0].Code != CodeUnknownSymbol ||
		findings[0].Instrument != "BAD" {
		t.Fatalf("findings = %+v, want unknown symbol", findings)
	}
}

func TestFinnhubSubscribeEmitsInitialQuoteSnapshot(t *testing.T) {
	t.Parallel()

	connector := &finnhubConnector{
		dial: func(context.Context, string) (finnhubConn, error) {
			return nil, context.Canceled
		},
		sleep: func(context.Context, time.Duration) error { return context.Canceled },
		quote: func(_ context.Context, symbol, token string) (finnhubQuote, error) {
			if symbol != "AAPL" {
				t.Fatalf("symbol = %q, want AAPL", symbol)
			}
			if token != "token" {
				t.Fatalf("token = %q, want token", token)
			}
			return finnhubQuote{
				Current: json.RawMessage(`191.23`),
				Time:    1710000000,
			}, nil
		},
		now: func() time.Time {
			return time.Unix(1710000000, 0).UTC()
		},
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		token:        "token",
	}

	ch, err := connector.Subscribe(context.Background(), []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer connector.Close()

	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatal("updates closed before initial snapshot")
		}
		if got.Mark != "191.23" || got.Base != testMarketDataAssetID("AAPL") || got.Quote != testMarketDataAssetID("USD") {
			t.Fatalf("snapshot = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial snapshot")
	}
}

func TestFinnhubSubscribeSkipsStaleInitialQuoteSnapshot(t *testing.T) {
	t.Parallel()

	staleAt := time.Date(2026, 6, 18, 20, 0, 0, 0, time.UTC)
	connector := &finnhubConnector{
		dial: func(context.Context, string) (finnhubConn, error) {
			return nil, context.Canceled
		},
		sleep: func(context.Context, time.Duration) error { return context.Canceled },
		quote: func(context.Context, string, string) (finnhubQuote, error) {
			return finnhubQuote{
				Current: json.RawMessage(`298.01`),
				Time:    staleAt.Unix(),
			}, nil
		},
		now: func() time.Time {
			return staleAt.Add(finnhubSnapshotMaxAge + time.Second)
		},
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		token:        "token",
	}

	ch, err := connector.Subscribe(context.Background(), []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer connector.Close()

	if got, ok := <-ch; ok {
		t.Fatalf("snapshot = %+v, want no stale update", got)
	}
}

func TestWriteFinnhubSubscribeShape(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeFinnhubSubscriptions(t, []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
		{External: "BINANCE:BTCUSDT", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USDT")},
	})
	conn := &fakeFinnhubConn{}
	if err := writeFinnhubSubscribe(context.Background(), conn, subs); err != nil {
		t.Fatalf("writeFinnhubSubscribe: %v", err)
	}

	gotSymbols := make([]string, 0, len(conn.writes))
	for _, payload := range conn.writes {
		var frame finnhubSubscribeFrame
		if err := json.Unmarshal(payload, &frame); err != nil {
			t.Fatalf("unmarshal subscribe: %v", err)
		}
		if frame.Type != "subscribe" {
			t.Fatalf("type = %q, want subscribe", frame.Type)
		}
		gotSymbols = append(gotSymbols, frame.Symbol)
	}
	if !slices.Equal(gotSymbols, []string{"AAPL", "BINANCE:BTCUSDT"}) {
		t.Fatalf("symbols = %v", gotSymbols)
	}
}

func TestParseFinnhubQuoteUpdates(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeFinnhubSubscriptions(t, []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	payload := []byte(`{"type":"trade","data":[{"s":"AAPL","p":12345678901234567890.123456789,"t":1710000000123}]}`)

	updates := parseFinnhubQuoteUpdates(payload, subs)
	if len(updates) != 1 {
		t.Fatalf("len(updates) = %d, want 1", len(updates))
	}
	if updates[0].Mark != "12345678901234567890.123456789" {
		t.Fatalf("Mark = %q", updates[0].Mark)
	}
	if !updates[0].AsOf.Equal(time.UnixMilli(1710000000123).UTC()) {
		t.Fatalf("AsOf = %s", updates[0].AsOf)
	}
}

func TestParseFinnhubFrameIgnoresPing(t *testing.T) {
	t.Parallel()

	frame := parseFinnhubFrame([]byte(`{"type":"ping"}`), nil)
	if !frame.ignored || len(frame.updates) != 0 ||
		frame.unparsedReason != "" || frame.statusError != "" {
		t.Fatalf("frame = %+v, want ignored ping", frame)
	}
}

func TestParseFinnhubQuoteUpdatesRejectsInvalidFrames(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeFinnhubSubscriptions(t, []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "malformed json",
			payload: `{bad`,
		},
		{
			name:    "bad time",
			payload: `{"type":"trade","data":[{"s":"AAPL","p":191.02,"t":0}]}`,
		},
		{
			name:    "empty data",
			payload: `{"type":"trade","data":[]}`,
		},
		{
			name:    "empty price",
			payload: `{"type":"trade","data":[{"s":"AAPL","t":1710000000123}]}`,
		},
		{
			name:    "unsubscribed symbol",
			payload: `{"type":"trade","data":[{"s":"MSFT","p":191.02,"t":1710000000123}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if updates := parseFinnhubQuoteUpdates([]byte(tt.payload), subs); len(updates) != 0 {
				t.Fatalf("updates = %+v, want none", updates)
			}
		})
	}
}

func TestFinnhubConnector_PingDoesNotReportUnparsable(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeFinnhubSubscriptions(t, []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	conn := &fakeFinnhubConn{
		messages: [][]byte{
			[]byte(`{"type":"ping"}`),
			[]byte(`{"type":"ping"}`),
			[]byte(`{"type":"ping"}`),
		},
		err: context.Canceled,
	}
	var diagnostics []Diagnostic
	connector := &finnhubConnector{
		dial: func(context.Context, string) (finnhubConn, error) {
			return conn, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		token:        "token",
		diagReport: func(diag Diagnostic) {
			diagnostics = append(diagnostics, diag)
		},
	}

	_, err := connector.stream(context.Background(), subs, make(chan QuoteUpdate))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stream err = %v, want context.Canceled", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v, want none for ping frames", diagnostics)
	}
}

func TestFinnhubConnector_UnparsableDiagnosticOmitsDebugSample(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeFinnhubSubscriptions(t, []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	conn := &fakeFinnhubConn{
		messages: [][]byte{
			[]byte(`{"type":"mystery","payload":1}`),
			[]byte(`{"type":"mystery","payload":2}`),
			[]byte(`{"type":"mystery","payload":3}`),
		},
		err: context.Canceled,
	}
	var diagnostics []Diagnostic
	connector := &finnhubConnector{
		dial: func(context.Context, string) (finnhubConn, error) {
			return conn, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		token:        "token",
		diagReport: func(diag Diagnostic) {
			diagnostics = append(diagnostics, diag)
		},
	}

	_, err := connector.stream(context.Background(), subs, make(chan QuoteUpdate))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stream err = %v, want context.Canceled", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want one debug diagnostic", diagnostics)
	}
	diag := diagnostics[0]
	if diag.Code != CodeUnparsableData ||
		strings.Contains(diag.Detail, "TEMP DEBUG") ||
		strings.Contains(diag.Detail, "payload_sample") ||
		strings.Contains(diag.Detail, `"payload":3`) {
		t.Fatalf("diag = %+v, want generic unparsable diagnostic", diag)
	}
}

func TestFinnhubConnector_RedactsTokenInDialError(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeFinnhubSubscriptions(t, []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	const token = "secret-token"
	var statuses []string
	connector := &finnhubConnector{
		dial: func(_ context.Context, streamURL string) (finnhubConn, error) {
			return nil, &url.Error{Op: "dial", URL: streamURL, Err: errors.New("boom")}
		},
		sleep:        func(context.Context, time.Duration) error { return context.Canceled },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		token:        token,
		report: func(ok bool, errMsg string) {
			if !ok {
				statuses = append(statuses, errMsg)
			}
		},
	}

	connector.run(context.Background(), subs, make(chan QuoteUpdate))
	if len(statuses) != 1 {
		t.Fatalf("statuses = %v, want one error", statuses)
	}
	if strings.Contains(statuses[0], token) {
		t.Fatalf("status leaked token: %q", statuses[0])
	}
	if !strings.Contains(statuses[0], "token=REDACTED") {
		t.Fatalf("status = %q, want redacted token marker", statuses[0])
	}
}

func TestFinnhubConnector_ReconnectsAndResubscribes(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeFinnhubSubscriptions(t, []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	first := &fakeFinnhubConn{
		messages: [][]byte{[]byte(`{"type":"trade","data":[{"s":"AAPL","p":1,"t":1710000000123}]}`)},
		err:      errors.New("disconnect"),
	}
	second := &fakeFinnhubConn{
		messages: [][]byte{[]byte(`{"type":"trade","data":[{"s":"AAPL","p":2,"t":1710000001123}]}`)},
		err:      context.Canceled,
	}

	var (
		mu    sync.Mutex
		dials int
		urls  []string
	)
	connector := &finnhubConnector{
		dial: func(_ context.Context, streamURL string) (finnhubConn, error) {
			mu.Lock()
			defer mu.Unlock()
			dials++
			urls = append(urls, streamURL)
			if dials == 1 {
				return first, nil
			}
			return second, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		token:        "token",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan QuoteUpdate)
	done := make(chan struct{})
	go func() {
		defer close(done)
		connector.run(ctx, subs, ch)
		close(ch)
	}()

	got := make([]QuoteUpdate, 0, 2)
	for update := range ch {
		got = append(got, update)
		if len(got) == 2 {
			cancel()
		}
	}
	<-done

	if len(got) != 2 || got[0].Mark != "1" || got[1].Mark != "2" {
		t.Fatalf("updates = %+v", got)
	}
	if !slices.Equal(urls, []string{finnhubStreamURL("token"), finnhubStreamURL("token")}) {
		t.Fatalf("urls = %v", urls)
	}
	assertFinnhubSubscribeWrites(t, first)
	assertFinnhubSubscribeWrites(t, second)
}

func TestFinnhubConnector_ResetsBackoffAfterRead(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeFinnhubSubscriptions(t, []Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	trade := []byte(`{"type":"trade","data":[{"s":"AAPL","p":191.23,"t":1710000000123}]}`)
	conns := []*fakeFinnhubConn{
		{messages: [][]byte{trade}, err: errors.New("disconnect 1")},
		{messages: [][]byte{trade}, err: errors.New("disconnect 2")},
		{messages: [][]byte{trade}, err: context.Canceled},
	}

	var (
		mu     sync.Mutex
		dials  int
		delays []time.Duration
	)
	connector := &finnhubConnector{
		dial: func(context.Context, string) (finnhubConn, error) {
			mu.Lock()
			defer mu.Unlock()
			conn := conns[dials]
			dials++
			return conn, nil
		},
		sleep: func(_ context.Context, delay time.Duration) error {
			mu.Lock()
			defer mu.Unlock()
			delays = append(delays, delay)
			return nil
		},
		reconnectMin: time.Millisecond,
		reconnectMax: 4 * time.Millisecond,
		token:        "token",
	}

	ch := make(chan QuoteUpdate)
	go func() {
		connector.run(context.Background(), subs, ch)
		close(ch)
	}()

	var got []QuoteUpdate
	for update := range ch {
		got = append(got, update)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	if len(delays) != 2 {
		t.Fatalf("delays = %v, want two reconnect sleeps", delays)
	}
	for i, delay := range delays {
		if delay != time.Millisecond {
			t.Fatalf("delay[%d] = %s, want %s (backoff should reset after delivery)", i, delay, time.Millisecond)
		}
	}
}

func mustNormalizeFinnhubSubscriptions(
	t *testing.T, subs []Subscription,
) []finnhubSubscription {
	t.Helper()
	normalized, err := normalizeFinnhubSubscriptions(subs)
	if err != nil {
		t.Fatalf("normalizeFinnhubSubscriptions: %v", err)
	}
	return normalized
}

func assertFinnhubSubscribeWrites(t *testing.T, conn *fakeFinnhubConn) {
	t.Helper()
	if len(conn.writes) != 1 {
		t.Fatalf("len(writes) = %d, want 1", len(conn.writes))
	}
	var frame finnhubSubscribeFrame
	if err := json.Unmarshal(conn.writes[0], &frame); err != nil {
		t.Fatalf("unmarshal subscribe: %v", err)
	}
	if frame.Type != "subscribe" || frame.Symbol != "AAPL" {
		t.Fatalf("subscribe frame = %+v", frame)
	}
}

type fakeFinnhubConn struct {
	messages  [][]byte
	writes    [][]byte
	err       error
	index     int
	closeOnce sync.Once
}

func (c *fakeFinnhubConn) Read(context.Context) ([]byte, error) {
	if c.index >= len(c.messages) {
		if c.err != nil {
			return nil, c.err
		}
		return nil, io.EOF
	}
	message := c.messages[c.index]
	c.index++
	return message, nil
}

func (c *fakeFinnhubConn) Write(_ context.Context, payload []byte) error {
	c.writes = append(c.writes, append([]byte(nil), payload...))
	return nil
}

func (c *fakeFinnhubConn) Close(websocket.StatusCode, string) error {
	c.closeOnce.Do(func() {})
	return nil
}

func TestFinnhubRESTHelpersRedactTokenInTransportErrors(t *testing.T) {
	originalClient := finnhubHTTPClient
	const token = "secret-token"
	finnhubHTTPClient = &http.Client{
		Transport: finnhubRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, &url.Error{
				Op:  "Get",
				URL: req.URL.String(),
				Err: &url.Error{
					Op:  "dial",
					URL: req.URL.String(),
					Err: errors.New("boom"),
				},
			}
		}),
	}
	t.Cleanup(func() { finnhubHTTPClient = originalClient })

	check := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s error = nil, want transport error", name)
		}
		if strings.Contains(err.Error(), token) {
			t.Fatalf("%s leaked token: %v", name, err)
		}
		if !strings.Contains(err.Error(), "token=REDACTED") {
			t.Fatalf("%s error = %v, want token redacted", name, err)
		}
	}

	_, err := fetchFinnhubCryptoSymbols(context.Background(), "TEST", token)
	check("crypto symbols", err)
	_, err = fetchFinnhubQuote(context.Background(), "AAPL", token)
	check("quote", err)
	_, err = fetchFinnhubSearchResults(context.Background(), "AAPL", token)
	check("search", err)
}

type finnhubRoundTripFunc func(*http.Request) (*http.Response, error)

func (f finnhubRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
