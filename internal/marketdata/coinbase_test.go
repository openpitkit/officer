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
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestNormalizeCoinbaseSubscriptions(t *testing.T) {
	t.Parallel()

	subs, err := normalizeCoinbaseSubscriptions([]Subscription{
		{External: "btc-usd", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
		{External: "eth-usd", Base: testMarketDataAssetID("Eth"), Quote: testMarketDataAssetID("Usd")},
	})
	if err != nil {
		t.Fatalf("normalizeCoinbaseSubscriptions: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("len(subs) = %d, want 2", len(subs))
	}
	if subs[0].productID != "BTC-USD" || subs[1].productID != "ETH-USD" {
		t.Fatalf("subs = %+v", subs)
	}
}

func TestNormalizeCoinbaseSubscriptionsEmptySymbol(t *testing.T) {
	t.Parallel()

	if _, err := normalizeCoinbaseSubscriptions([]Subscription{{}}); err == nil {
		t.Fatal("normalizeCoinbaseSubscriptions error = nil, want error")
	}
}

func TestCoinbaseSubscribePayload(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeCoinbaseSubscriptions(t, []Subscription{
		{External: "BTC-USD", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
	})
	payload, err := coinbaseSubscribePayload(subs)
	if err != nil {
		t.Fatalf("coinbaseSubscribePayload: %v", err)
	}

	var got coinbaseSubscribeRequest
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got.Type != "subscribe" || len(got.Channels) != 1 ||
		got.Channels[0] != "ticker" || len(got.ProductIDs) != 1 ||
		got.ProductIDs[0] != "BTC-USD" {
		t.Fatalf("payload = %+v", got)
	}
}

func TestParseCoinbaseQuoteUpdate(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeCoinbaseSubscriptions(t, []Subscription{
		{External: "BTC-USD", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
	})
	payload := []byte(`{"type":"ticker","product_id":"BTC-USD","price":"65000.10","best_bid":"65000.01","best_ask":"65000.02","time":"2024-03-09T16:00:00.123456Z"}`)

	update, ok := parseCoinbaseQuoteUpdate(payload, subs)
	if !ok {
		t.Fatal("parseCoinbaseQuoteUpdate returned ok=false")
	}
	if !update.AsOf.Equal(time.Date(2024, 3, 9, 16, 0, 0, 123456000, time.UTC)) {
		t.Fatalf("AsOf = %s", update.AsOf)
	}
	if update.Base != testMarketDataAssetID("BTC") || update.Quote != testMarketDataAssetID("USD") {
		t.Fatalf("instrument = %d/%d", update.Base, update.Quote)
	}
	if update.Mark != "65000.10" || update.Bid != "65000.01" || update.Ask != "65000.02" {
		t.Fatalf("prices = %+v", update)
	}
}

func TestParseCoinbaseQuoteUpdateBidAskOnly(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeCoinbaseSubscriptions(t, []Subscription{
		{External: "BTC-USD", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
	})
	payload := []byte(`{"type":"ticker","product_id":"BTC-USD","best_bid":"65000.01","best_ask":"65000.02","time":"2024-03-09T16:00:00Z"}`)

	update, ok := parseCoinbaseQuoteUpdate(payload, subs)
	if !ok {
		t.Fatal("parseCoinbaseQuoteUpdate returned ok=false")
	}
	if update.Mark != "" || update.Bid != "65000.01" || update.Ask != "65000.02" {
		t.Fatalf("prices = %+v", update)
	}
}

func TestParseCoinbaseQuoteUpdateRejectsInvalidFrames(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeCoinbaseSubscriptions(t, []Subscription{
		{External: "BTC-USD", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
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
			payload: `{"type":"ticker","product_id":"BTC-USD","price":"65000.10","time":"bad"}`,
		},
		{
			name:    "empty object",
			payload: `{}`,
		},
		{
			name:    "empty prices",
			payload: `{"type":"ticker","product_id":"BTC-USD","time":"2024-03-09T16:00:00Z"}`,
		},
		{
			name:    "unsubscribed symbol",
			payload: `{"type":"ticker","product_id":"ETH-USD","price":"65000.10","time":"2024-03-09T16:00:00Z"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if update, ok := parseCoinbaseQuoteUpdate([]byte(tt.payload), subs); ok {
				t.Fatalf("parseCoinbaseQuoteUpdate = %+v, want ok=false", update)
			}
		})
	}
}

func TestCoinbaseConnector_ReconnectsAndResubscribes(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeCoinbaseSubscriptions(t, []Subscription{
		{External: "BTC-USD", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
	})
	first := &fakeCoinbaseConn{
		messages: [][]byte{
			[]byte(`{"type":"ticker","product_id":"BTC-USD","price":"1","best_bid":"0.9","best_ask":"1.1","time":"2024-03-09T16:00:00Z"}`),
		},
		err: errors.New("disconnect"),
	}
	second := &fakeCoinbaseConn{
		messages: [][]byte{
			[]byte(`{"type":"ticker","product_id":"BTC-USD","price":"2","best_bid":"1.9","best_ask":"2.1","time":"2024-03-09T16:00:01Z"}`),
		},
		err: context.Canceled,
	}

	var (
		mu    sync.Mutex
		dials int
		urls  []string
	)
	connector := &coinbaseConnector{
		dial: func(_ context.Context, url string) (coinbaseConn, error) {
			mu.Lock()
			defer mu.Unlock()
			dials++
			urls = append(urls, url)
			if dials == 1 {
				return first, nil
			}
			return second, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: 2 * time.Millisecond,
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
		t.Fatalf("updates = %+v, want marks 1 and 2", got)
	}
	if first.writeCount() != 1 || second.writeCount() != 1 {
		t.Fatalf("write counts = %d/%d, want 1/1", first.writeCount(), second.writeCount())
	}
	if len(urls) != 2 || urls[0] != coinbaseFeedURL || urls[1] != coinbaseFeedURL {
		t.Fatalf("urls = %+v", urls)
	}
}

func TestCoinbaseConnector_ReportsErrorFrame(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeCoinbaseSubscriptions(t, []Subscription{
		{External: "BAD-USD", Base: testMarketDataAssetID("BAD"), Quote: testMarketDataAssetID("USD")},
	})
	conn := &fakeCoinbaseConn{
		messages: [][]byte{
			[]byte(`{"type":"error","message":"Failed to subscribe","reason":"BAD-USD is invalid"}`),
		},
		err: context.Canceled,
	}
	var statuses []string
	connector := &coinbaseConnector{
		dial: func(context.Context, string) (coinbaseConn, error) {
			return conn, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		report: func(ok bool, errMsg string) {
			if !ok {
				statuses = append(statuses, errMsg)
			}
		},
	}

	if _, err := connector.stream(context.Background(), subs, make(chan QuoteUpdate)); !errors.Is(err, context.Canceled) {
		t.Fatalf("stream err = %v, want context.Canceled", err)
	}
	if len(statuses) != 1 || statuses[0] != "coinbase: Failed to subscribe: BAD-USD is invalid" {
		t.Fatalf("statuses = %v", statuses)
	}
}

func TestCoinbaseVerifySymbolUsesProductCatalogue(t *testing.T) {
	t.Parallel()

	connector := NewCoinbaseConnector()
	connector.fetchSymbols = func(context.Context) (map[string]struct{}, error) {
		return map[string]struct{}{"BTC-USD": {}}, nil
	}

	exact, err := connector.VerifySymbol(context.Background(), "BTC-USD")
	if err != nil {
		t.Fatalf("VerifySymbol exact: %v", err)
	}
	if !exact.Exists || exact.Suggestion != "" {
		t.Fatalf("exact = %+v, want exists", exact)
	}
	folded, err := connector.VerifySymbol(context.Background(), "btc-usd")
	if err != nil {
		t.Fatalf("VerifySymbol folded: %v", err)
	}
	if folded.Exists || folded.Suggestion != "BTC-USD" {
		t.Fatalf("folded = %+v, want suggestion BTC-USD", folded)
	}
}

func TestCoinbaseDiagnoseUnknownSymbol(t *testing.T) {
	t.Parallel()

	connector := NewCoinbaseConnector()
	connector.fetchSymbols = func(context.Context) (map[string]struct{}, error) {
		return map[string]struct{}{"BTC-USD": {}}, nil
	}
	connector.subs = mustNormalizeCoinbaseSubscriptions(t, []Subscription{
		{External: "BTC-USD", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
		{External: "BAD-USD", Base: testMarketDataAssetID("BAD"), Quote: testMarketDataAssetID("USD")},
	})

	findings, err := connector.Diagnose(context.Background())
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(findings) != 1 || findings[0].Code != CodeUnknownSymbol ||
		findings[0].Instrument != "BAD-USD" {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestCoinbaseConnector_ResetsBackoffAfterRead(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeCoinbaseSubscriptions(t, []Subscription{
		{External: "BTC-USD", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
	})
	conns := []*fakeCoinbaseConn{
		{
			messages: [][]byte{
				[]byte(`{"type":"ticker","product_id":"BTC-USD","price":"1","best_bid":"0.9","best_ask":"1.1","time":"2024-03-09T16:00:00Z"}`),
			},
			err: errors.New("disconnect 1"),
		},
		{
			messages: [][]byte{
				[]byte(`{"type":"ticker","product_id":"BTC-USD","price":"2","best_bid":"1.9","best_ask":"2.1","time":"2024-03-09T16:00:01Z"}`),
			},
			err: errors.New("disconnect 2"),
		},
		{
			messages: [][]byte{
				[]byte(`{"type":"ticker","product_id":"BTC-USD","price":"3","best_bid":"2.9","best_ask":"3.1","time":"2024-03-09T16:00:02Z"}`),
			},
			err: context.Canceled,
		},
	}

	var (
		mu     sync.Mutex
		dials  int
		delays []time.Duration
	)
	connector := &coinbaseConnector{
		dial: func(context.Context, string) (coinbaseConn, error) {
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

func TestCoinbaseConnector_CloseStopsSubscription(t *testing.T) {
	t.Parallel()

	conn := &fakeCoinbaseConn{blockRead: make(chan struct{})}
	connector := &coinbaseConnector{
		dial: func(context.Context, string) (coinbaseConn, error) {
			return conn, nil
		},
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"BTC-USD": {}}, nil
		},
		readTimeout: time.Hour,
		report:      func(bool, string) {},
		diagReport:  func(Diagnostic) {},
	}
	out, err := connector.Subscribe(context.Background(), []Subscription{
		{External: "BTC-USD", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USD")},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for conn.writeCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if conn.writeCount() == 0 {
		t.Fatal("subscribe frame was not written")
	}

	connector.Close()
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("subscription channel still open")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription channel did not close")
	}
}

func mustNormalizeCoinbaseSubscriptions(
	t *testing.T, subs []Subscription,
) []coinbaseSubscription {
	t.Helper()
	normalized, err := normalizeCoinbaseSubscriptions(subs)
	if err != nil {
		t.Fatalf("normalizeCoinbaseSubscriptions: %v", err)
	}
	return normalized
}

type fakeCoinbaseConn struct {
	mu        sync.Mutex
	messages  [][]byte
	writes    [][]byte
	blockRead chan struct{}
	err       error
	index     int
}

func (c *fakeCoinbaseConn) Read(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	if c.index < len(c.messages) {
		message := c.messages[c.index]
		c.index++
		c.mu.Unlock()
		return message, nil
	}
	err := c.err
	blockRead := c.blockRead
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if blockRead == nil {
		return nil, io.EOF
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-blockRead:
		return nil, io.EOF
	}
}

func (c *fakeCoinbaseConn) Write(_ context.Context, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), payload...))
	return nil
}

func (c *fakeCoinbaseConn) Close(websocket.StatusCode, string) error {
	return nil
}

func (c *fakeCoinbaseConn) writeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.writes)
}
