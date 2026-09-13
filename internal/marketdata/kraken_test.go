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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
)

func TestNormalizeKrakenSubscriptionsPreservesAssetKeys(t *testing.T) {
	t.Parallel()

	subs, err := normalizeKrakenSubscriptions([]fwmarketdata.Subscription{
		{External: "xbt/usd", Base: testMarketDataAssetID("opaque-base"), Quote: testMarketDataAssetID("opaque-quote")},
		{External: "XDG/USD", Base: testMarketDataAssetID("another-base"), Quote: testMarketDataAssetID("another-quote")},
	})
	if err != nil {
		t.Fatalf("normalizeKrakenSubscriptions: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("len(subs) = %d, want 2", len(subs))
	}
	if subs[0].symbol != "BTC/USD" {
		t.Fatalf("first symbol = %q, want BTC/USD", subs[0].symbol)
	}
	if subs[0].Base != testMarketDataAssetID("opaque-base") || subs[0].Quote != testMarketDataAssetID("opaque-quote") {
		t.Fatalf("first asset key = %q/%q, want caller values", subs[0].Base, subs[0].Quote)
	}
	if subs[1].symbol != "DOGE/USD" {
		t.Fatalf("second symbol = %q, want DOGE/USD", subs[1].symbol)
	}
	if subs[1].Base != testMarketDataAssetID("another-base") || subs[1].Quote != testMarketDataAssetID("another-quote") {
		t.Fatalf("second asset key = %q/%q, want caller values", subs[1].Base, subs[1].Quote)
	}
}

func TestKrakenSubscribePayload(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeKrakenSubscriptions(t, []fwmarketdata.Subscription{
		{External: "ETH/USD", Base: testMarketDataAssetID("ETH"), Quote: testMarketDataAssetID("USD")},
		{External: "XBT/USD", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
	})

	payload, err := krakenSubscribePayload(subs)
	if err != nil {
		t.Fatalf("krakenSubscribePayload: %v", err)
	}
	var got krakenSubscribeRequest
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("subscribe payload: %v", err)
	}
	if got.Method != "subscribe" || got.Params.Channel != "ticker" ||
		got.Params.EventTrigger != "bbo" || !got.Params.Snapshot {
		t.Fatalf("payload = %+v", got)
	}
	if len(got.Params.Symbol) != 2 ||
		got.Params.Symbol[0] != "BTC/USD" ||
		got.Params.Symbol[1] != "ETH/USD" {
		t.Fatalf("symbols = %+v", got.Params.Symbol)
	}
}

func TestKrakenSymbolsFromAssetPairsUsesDisplayPairKeys(t *testing.T) {
	t.Parallel()

	symbols := krakenSymbolsFromAssetPairs(krakenAssetPairsResponse{
		Result: map[string]struct{}{
			"BTC/USD": {},
			"XDG/USD": {},
		},
	})
	if _, ok := symbols["BTC/USD"]; !ok {
		t.Fatalf("symbols = %+v, want BTC/USD", symbols)
	}
	if _, ok := symbols["DOGE/USD"]; !ok {
		t.Fatalf("symbols = %+v, want DOGE/USD", symbols)
	}
	if _, ok := symbols["XDG/USD"]; ok {
		t.Fatalf("symbols = %+v, did not want XDG/USD", symbols)
	}
}

func TestParseKrakenQuoteUpdates(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeKrakenSubscriptions(t, []fwmarketdata.Subscription{
		{External: "XBT/USD", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
	})
	payload := []byte(`{"channel":"ticker","type":"snapshot","data":[` +
		`{"symbol":"BTC/USD","bid":65000.01,"ask":65000.02,` +
		`"last":65000.10,"timestamp":"2026-06-20T09:04:31.742648Z"}]}`)

	updates, ok := parseKrakenQuoteUpdates(payload, subs)
	if !ok {
		t.Fatal("parseKrakenQuoteUpdates returned ok=false")
	}
	if len(updates) != 1 {
		t.Fatalf("len(updates) = %d, want 1", len(updates))
	}
	update := updates[0]
	if !update.AsOf.Equal(time.Date(
		2026, 6, 20, 9, 4, 31, 742648000, time.UTC,
	)) {
		t.Fatalf("AsOf = %s", update.AsOf)
	}
	if update.Base != testMarketDataAssetID("BTC") || update.Quote != testMarketDataAssetID("USD") {
		t.Fatalf("instrument = %d/%d", update.Base, update.Quote)
	}
	if update.Mark != "65000.10" ||
		update.Bid != "65000.01" ||
		update.Ask != "65000.02" {
		t.Fatalf("prices = %+v", update)
	}
}

func TestParseKrakenQuoteUpdatesIgnoresRoutineFrames(t *testing.T) {
	t.Parallel()

	if !krakenFrameIsRoutine([]byte(`{"channel":"heartbeat"}`)) {
		t.Fatal("heartbeat should be routine")
	}
	if !krakenFrameIsRoutine([]byte(
		`{"channel":"status","type":"update","data":[{"system":"online"}]}`,
	)) {
		t.Fatal("status should be routine")
	}
	if !krakenFrameIsRoutine([]byte(
		`{"method":"subscribe","success":true,"result":{"channel":"ticker"}}`,
	)) {
		t.Fatal("successful subscribe ack should be routine")
	}
}

func TestKrakenConnector_ReconnectsAndResubscribes(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeKrakenSubscriptions(t, []fwmarketdata.Subscription{
		{External: "XBT/USD", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
	})
	first := &fakeKrakenConn{
		messages: [][]byte{
			[]byte(`{"channel":"ticker","type":"snapshot","data":[` +
				`{"symbol":"BTC/USD","bid":1.1,"ask":1.2,"last":1.15,` +
				`"timestamp":"2026-06-20T09:00:00Z"}]}`),
		},
		err: errors.New("disconnect"),
	}
	second := &fakeKrakenConn{
		messages: [][]byte{
			[]byte(`{"channel":"ticker","type":"update","data":[` +
				`{"symbol":"BTC/USD","bid":2.1,"ask":2.2,"last":2.15,` +
				`"timestamp":"2026-06-20T09:00:01Z"}]}`),
		},
		err: context.Canceled,
	}

	var (
		mu    sync.Mutex
		dials int
	)
	connector := &krakenConnector{
		dial: func(context.Context, string) (krakenConn, error) {
			mu.Lock()
			defer mu.Unlock()
			dials++
			if dials == 1 {
				return first, nil
			}
			return second, nil
		},
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			known := make(map[string]struct{}, len(subs))
			for _, s := range subs {
				known[s.symbol] = struct{}{}
			}
			return known, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: 2 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan fwmarketdata.QuoteUpdate)
	done := make(chan struct{})
	go func() {
		defer close(done)
		connector.run(ctx, subs, ch)
		close(ch)
	}()

	got := make([]fwmarketdata.QuoteUpdate, 0, 2)
	for update := range ch {
		got = append(got, update)
	}
	<-done

	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Mark != "1.15" || got[1].Mark != "2.15" {
		t.Fatalf("updates = %+v", got)
	}
	if len(first.writes) != 1 || len(second.writes) != 1 {
		t.Fatalf("writes = %d/%d, want one subscribe per connection",
			len(first.writes), len(second.writes))
	}
}

func TestKrakenConnector_CloseStopsSubscription(t *testing.T) {
	t.Parallel()

	blocked := &fakeKrakenConn{blockRead: make(chan struct{})}
	connector := &krakenConnector{
		dial: func(context.Context, string) (krakenConn, error) {
			return blocked, nil
		},
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"BTC/USD": {}}, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
	}

	ch, err := connector.Subscribe(context.Background(), []fwmarketdata.Subscription{
		{External: "XBT/USD", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	connector.Close()
	connector.Close()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel should be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for connector shutdown")
	}
}

func TestKrakenConnector_VerifySymbolExists(t *testing.T) {
	t.Parallel()

	connector := &krakenConnector{
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"BTC/USD": {}, "ETH/USD": {}}, nil
		},
	}

	got, err := connector.VerifySymbol(context.Background(), "BTC/USD")
	if err != nil {
		t.Fatalf("VerifySymbol: %v", err)
	}
	if !got.Exists || got.Suggestion != "" {
		t.Fatalf("VerifySymbol = %+v, want Exists=true, no suggestion", got)
	}
}

func TestKrakenConnector_VerifySymbolCaseFoldSuggestion(t *testing.T) {
	t.Parallel()

	connector := &krakenConnector{
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"BTC/USD": {}}, nil
		},
	}

	got, err := connector.VerifySymbol(context.Background(), "btc/usd")
	if err != nil {
		t.Fatalf("VerifySymbol: %v", err)
	}
	if got.Exists {
		t.Fatalf("VerifySymbol = %+v, want Exists=false", got)
	}
	if got.Suggestion != "BTC/USD" {
		t.Fatalf("VerifySymbol suggestion = %q, want %q", got.Suggestion, "BTC/USD")
	}
}

func TestKrakenConnector_VerifySymbolLegacySuggestion(t *testing.T) {
	t.Parallel()

	connector := &krakenConnector{
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"BTC/USD": {}}, nil
		},
	}

	got, err := connector.VerifySymbol(context.Background(), "XBT/USD")
	if err != nil {
		t.Fatalf("VerifySymbol: %v", err)
	}
	if got.Exists {
		t.Fatalf("VerifySymbol = %+v, want Exists=false", got)
	}
	if got.Suggestion != "BTC/USD" {
		t.Fatalf("VerifySymbol suggestion = %q, want BTC/USD", got.Suggestion)
	}
}

func TestKrakenConnector_VerifySymbolFetchError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("AssetPairs down")
	connector := &krakenConnector{
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return nil, wantErr
		},
	}

	if _, err := connector.VerifySymbol(context.Background(), "XBT/USD"); !errors.Is(err, wantErr) {
		t.Fatalf("VerifySymbol error = %v, want %v", err, wantErr)
	}
}

func TestKrakenConnector_ValidateSymbolsDropsUnknown(t *testing.T) {
	t.Parallel()

	var diags []fwmarketdata.Diagnostic
	connector := &krakenConnector{
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"BTC/USD": {}, "DOGE/USD": {}}, nil
		},
		diagReport: func(diag fwmarketdata.Diagnostic) {
			diags = append(diags, diag)
		},
	}
	subs := mustNormalizeKrakenSubscriptions(t, []fwmarketdata.Subscription{
		{External: "BTC/USD", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
		{
			External: "DOGE/EUR",
			Base:     testMarketDataAssetID("opaque-base-key"),
			Quote:    testMarketDataAssetID("opaque-quote-key"),
		},
	})

	valid := connector.validateSymbols(context.Background(), subs)
	if len(valid) != 1 || valid[0].symbol != "BTC/USD" {
		t.Fatalf("valid = %+v, want only BTC/USD", valid)
	}
	if len(diags) != 1 || diags[0].Code != CodeUnknownSymbol {
		t.Fatalf("diags = %+v, want one unknown_symbol", diags)
	}
	remediation := diags[0].Remediation
	if !strings.Contains(remediation, "Did you mean: DOGE/USD?") {
		t.Fatalf("remediation = %q, want external symbol suggestion", remediation)
	}
	if strings.Contains(remediation, "opaque-base-key") ||
		strings.Contains(remediation, "opaque-quote-key") {
		t.Fatalf("remediation = %q, must not use asset keys", remediation)
	}
}

func TestKrakenConnector_DiagnoseUnknownSymbol(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeKrakenSubscriptions(t, []fwmarketdata.Subscription{
		{
			External: "BTC/USDXX",
			Base:     testMarketDataAssetID("opaque-base-key"),
			Quote:    testMarketDataAssetID("opaque-quote-key"),
		},
	})
	connector := &krakenConnector{
		subs: subs,
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"BTC/USD": {}}, nil
		},
	}

	findings, err := connector.Diagnose(context.Background())
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(findings) != 1 || findings[0].Code != CodeUnknownSymbol {
		t.Fatalf("findings = %+v, want one unknown_symbol", findings)
	}
	remediation := findings[0].Remediation
	if !strings.Contains(remediation, "Did you mean: BTC/USD?") {
		t.Fatalf("remediation = %q, want external symbol suggestion", remediation)
	}
	if strings.Contains(remediation, "opaque-base-key") ||
		strings.Contains(remediation, "opaque-quote-key") {
		t.Fatalf("remediation = %q, must not use asset keys", remediation)
	}
}

func TestKrakenConnector_ReconnectBackoffResetsAfterData(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeKrakenSubscriptions(t, []fwmarketdata.Subscription{
		{External: "BTC/USD", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
	})
	conns := []*fakeKrakenConn{
		{err: errors.New("first")},
		{err: errors.New("second")},
		{
			messages: [][]byte{
				[]byte(`{"channel":"ticker","type":"snapshot","data":[` +
					`{"symbol":"BTC/USD","bid":1,"ask":1.1,"last":1.05,` +
					`"timestamp":"2026-06-20T09:00:00Z"}]}`),
			},
			err: errors.New("after data"),
		},
	}
	var (
		dials  int
		delays []time.Duration
	)
	connector := &krakenConnector{
		dial: func(context.Context, string) (krakenConn, error) {
			if dials >= len(conns) {
				return nil, context.Canceled
			}
			conn := conns[dials]
			dials++
			return conn, nil
		},
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"BTC/USD": {}}, nil
		},
		sleep: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			if len(delays) == 3 {
				return context.Canceled
			}
			return nil
		},
		reconnectMin: time.Millisecond,
		reconnectMax: 8 * time.Millisecond,
	}

	ch := make(chan fwmarketdata.QuoteUpdate, 1)
	connector.run(context.Background(), subs, ch)

	want := []time.Duration{
		time.Millisecond,
		2 * time.Millisecond,
		time.Millisecond,
	}
	if !slices.Equal(delays, want) {
		t.Fatalf("delays = %v, want %v", delays, want)
	}
}

func mustNormalizeKrakenSubscriptions(
	t *testing.T, subs []fwmarketdata.Subscription,
) []krakenSubscription {
	t.Helper()
	normalized, err := normalizeKrakenSubscriptions(subs)
	if err != nil {
		t.Fatalf("normalizeKrakenSubscriptions: %v", err)
	}
	return normalized
}

type fakeKrakenConn struct {
	messages  [][]byte
	writes    [][]byte
	err       error
	blockRead chan struct{}
	index     int
	closeOnce sync.Once
}

func (c *fakeKrakenConn) Read(ctx context.Context) ([]byte, error) {
	if c.blockRead != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.blockRead:
		}
	}
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

func (c *fakeKrakenConn) Write(_ context.Context, payload []byte) error {
	c.writes = append(c.writes, payload)
	return nil
}

func (c *fakeKrakenConn) Close(websocket.StatusCode, string) error {
	c.closeOnce.Do(func() {
		if c.blockRead != nil {
			close(c.blockRead)
			c.blockRead = nil
		}
	})
	return nil
}
