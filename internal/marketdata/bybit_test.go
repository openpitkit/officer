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
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
)

func TestNormalizeBybitSubscriptions(t *testing.T) {
	t.Parallel()

	subs, err := normalizeBybitSubscriptions([]fwmarketdata.Subscription{
		{External: "btcusdt", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USDT")},
		{External: "ethusd", Base: testMarketDataAssetID("Eth"), Quote: testMarketDataAssetID("Usd")},
	})
	if err != nil {
		t.Fatalf("normalizeBybitSubscriptions: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("len(subs) = %d, want 2", len(subs))
	}
	if subs[0].symbol != "BTCUSDT" || subs[0].topic != "tickers.BTCUSDT" {
		t.Fatalf("first sub = %+v", subs[0])
	}
	if subs[1].symbol != "ETHUSD" || subs[1].topic != "tickers.ETHUSD" {
		t.Fatalf("second sub = %+v", subs[1])
	}
}

func TestNormalizeBybitSubscriptionsEmptySymbol(t *testing.T) {
	t.Parallel()

	if _, err := normalizeBybitSubscriptions([]fwmarketdata.Subscription{{}}); err == nil {
		t.Fatal("normalizeBybitSubscriptions error = nil, want error")
	}
}

func TestBybitCredentialsCategory(t *testing.T) {
	t.Parallel()

	category, err := bybitCategoryFromCredentials(`{"category":"linear"}`)
	if err != nil {
		t.Fatalf("bybitCategoryFromCredentials: %v", err)
	}
	if category != "linear" {
		t.Fatalf("category = %q, want linear", category)
	}
	if _, err := bybitCategoryFromCredentials(`{"category":"wallet"}`); err == nil {
		t.Fatal("bybitCategoryFromCredentials invalid category error = nil")
	}
}

func TestBybitSubscribePayload(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeBybitSubscriptions(t, []fwmarketdata.Subscription{
		{External: "BTCUSDT", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USDT")},
	})
	payload, err := bybitSubscribePayload(subs)
	if err != nil {
		t.Fatalf("bybitSubscribePayload: %v", err)
	}

	var got bybitOpRequest
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got.Op != "subscribe" || len(got.Args) != 1 || got.Args[0] != "tickers.BTCUSDT" {
		t.Fatalf("payload = %+v", got)
	}
}

func TestParseBybitQuoteUpdate(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeBybitSubscriptions(t, []fwmarketdata.Subscription{
		{External: "BTCUSDT", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USDT")},
	})
	payload := []byte(`{"topic":"tickers.BTCUSDT","type":"snapshot","ts":1710000000123,"data":{"symbol":"BTCUSDT","lastPrice":"65000.10","bid1Price":"65000.01","ask1Price":"65000.02"}}`)

	state := make(map[string]bybitTickerSnapshot)
	update, ok := parseBybitQuoteUpdate(payload, subs, state)
	if !ok {
		t.Fatal("parseBybitQuoteUpdate returned ok=false")
	}
	if !update.AsOf.Equal(time.UnixMilli(1710000000123).UTC()) {
		t.Fatalf("AsOf = %s", update.AsOf)
	}
	if update.Base != testMarketDataAssetID("BTC") || update.Quote != testMarketDataAssetID("USDT") {
		t.Fatalf("instrument = %d/%d", update.Base, update.Quote)
	}
	if update.Mark != "65000.10" || update.Bid != "65000.01" || update.Ask != "65000.02" {
		t.Fatalf("prices = %+v", update)
	}
}

func TestParseBybitDeltaMergesSnapshot(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeBybitSubscriptions(t, []fwmarketdata.Subscription{
		{External: "BTCUSDT", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USDT")},
	})
	state := make(map[string]bybitTickerSnapshot)
	snapshot := []byte(`{"topic":"tickers.BTCUSDT","type":"snapshot","ts":1710000000000,"data":{"symbol":"BTCUSDT","lastPrice":"65000.10","bid1Price":"65000.01","ask1Price":"65000.02"}}`)
	if _, ok := parseBybitQuoteUpdate(snapshot, subs, state); !ok {
		t.Fatal("snapshot parse returned ok=false")
	}

	delta := []byte(`{"topic":"tickers.BTCUSDT","type":"delta","ts":1710000000123,"data":{"symbol":"BTCUSDT","bid1Price":"65001.00"}}`)
	update, ok := parseBybitQuoteUpdate(delta, subs, state)
	if !ok {
		t.Fatal("delta parse returned ok=false")
	}
	if update.Mark != "65000.10" || update.Bid != "65001.00" || update.Ask != "65000.02" {
		t.Fatalf("merged prices = %+v", update)
	}
}

func TestBybitConnector_ReportsControlError(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeBybitSubscriptions(t, []fwmarketdata.Subscription{
		{External: "BTCUSDT", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USDT")},
	})
	conn := &fakeBybitConn{
		messages: [][]byte{[]byte(`{"success":false,"op":"subscribe","ret_msg":"invalid symbol"}`)},
		err:      context.Canceled,
	}
	var statuses []string
	connector := &bybitConnector{
		dial: func(context.Context, string) (bybitConn, error) {
			return conn, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		category:     "spot",
		report: func(ok bool, errMsg string) {
			if !ok {
				statuses = append(statuses, errMsg)
			}
		},
	}

	if _, err := connector.stream(context.Background(), subs, make(chan fwmarketdata.QuoteUpdate)); !errors.Is(err, context.Canceled) {
		t.Fatalf("stream err = %v, want context.Canceled", err)
	}
	if len(statuses) != 1 || statuses[0] != "bybit: subscribe: invalid symbol" {
		t.Fatalf("statuses = %v", statuses)
	}
}

func TestBybitConnector_ControlFramesDoNotReportUnparsable(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeBybitSubscriptions(t, []fwmarketdata.Subscription{
		{External: "BTCUSDT", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USDT")},
	})
	conn := &fakeBybitConn{
		messages: [][]byte{
			[]byte(`pong`),
			[]byte(`{"op":"pong"}`),
			[]byte(`{"success":true,"op":"subscribe"}`),
			[]byte(`{"topic":"tickers.BTCUSDT","type":"snapshot","ts":1710000000000,"data":{"symbol":"BTCUSDT","lastPrice":"65000.10"}}`),
		},
		err: context.Canceled,
	}
	var diagnostics []fwmarketdata.Diagnostic
	connector := &bybitConnector{
		dial: func(context.Context, string) (bybitConn, error) {
			return conn, nil
		},
		pingInterval: time.Hour,
		readTimeout:  time.Hour,
		category:     "spot",
		report:       func(bool, string) {},
		diagReport: func(diag fwmarketdata.Diagnostic) {
			diagnostics = append(diagnostics, diag)
		},
	}

	out := make(chan fwmarketdata.QuoteUpdate, 1)
	delivered, err := connector.stream(context.Background(), subs, out)
	if !errors.Is(err, context.Canceled) || !delivered {
		t.Fatalf("stream = (%v, %v), want delivered context.Canceled", delivered, err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v, want none", diagnostics)
	}
	update := <-out
	if update.Mark != "65000.10" {
		t.Fatalf("update = %+v, want mark", update)
	}
}

func TestBybitConnector_CloseStopsSubscription(t *testing.T) {
	t.Parallel()

	conn := &fakeBybitConn{blockRead: make(chan struct{})}
	connector := &bybitConnector{
		dial: func(context.Context, string) (bybitConn, error) {
			return conn, nil
		},
		fetchSymbols: func(context.Context, string) (map[string]struct{}, error) {
			return map[string]struct{}{"BTCUSDT": {}}, nil
		},
		pingInterval: time.Hour,
		readTimeout:  time.Hour,
		category:     "spot",
		report:       func(bool, string) {},
		diagReport:   func(fwmarketdata.Diagnostic) {},
	}
	out, err := connector.Subscribe(context.Background(), []fwmarketdata.Subscription{
		{External: "BTCUSDT", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USDT")},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for !conn.hasSubscribe("tickers.BTCUSDT") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !conn.hasSubscribe("tickers.BTCUSDT") {
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

func TestBybitVerifySymbolUsesCategoryCatalogue(t *testing.T) {
	t.Parallel()

	connector, err := NewBybitConnector(`{"category":"linear"}`)
	if err != nil {
		t.Fatalf("NewBybitConnector: %v", err)
	}
	var gotCategory string
	connector.fetchSymbols = func(_ context.Context, category string) (map[string]struct{}, error) {
		gotCategory = category
		return map[string]struct{}{"BTCUSDT": {}}, nil
	}

	exact, err := connector.VerifySymbol(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatalf("VerifySymbol exact: %v", err)
	}
	if gotCategory != "linear" {
		t.Fatalf("category = %q, want linear", gotCategory)
	}
	if !exact.Exists || exact.Suggestion != "" {
		t.Fatalf("exact = %+v, want exists", exact)
	}
	folded, err := connector.VerifySymbol(context.Background(), "btcusdt")
	if err != nil {
		t.Fatalf("VerifySymbol folded: %v", err)
	}
	if folded.Exists || folded.Suggestion != "BTCUSDT" {
		t.Fatalf("folded = %+v, want suggestion BTCUSDT", folded)
	}
}

func TestBybitReferencesIncludeCategory(t *testing.T) {
	t.Parallel()

	defaultConnector, err := NewBybitConnector("")
	if err != nil {
		t.Fatalf("NewBybitConnector default: %v", err)
	}
	defaultRefs, ok := defaultConnector.References()
	if !ok {
		t.Fatal("References default ok=false")
	}
	if defaultRefs.SymbolsURL != bybitInstrumentsBaseURL+"?category=spot" {
		t.Fatalf("default symbols URL = %q", defaultRefs.SymbolsURL)
	}

	linearConnector, err := NewBybitConnector(`{"category":"linear"}`)
	if err != nil {
		t.Fatalf("NewBybitConnector linear: %v", err)
	}
	linearRefs, ok := linearConnector.References()
	if !ok {
		t.Fatal("References linear ok=false")
	}
	if linearRefs.SymbolsURL != bybitInstrumentsBaseURL+"?category=linear" {
		t.Fatalf("linear symbols URL = %q", linearRefs.SymbolsURL)
	}
}

func TestBybitDiagnoseUnknownSymbol(t *testing.T) {
	t.Parallel()

	connector, err := NewBybitConnector("")
	if err != nil {
		t.Fatalf("NewBybitConnector: %v", err)
	}
	connector.fetchSymbols = func(context.Context, string) (map[string]struct{}, error) {
		return map[string]struct{}{"BTCUSDT": {}}, nil
	}
	connector.subs = mustNormalizeBybitSubscriptions(t, []fwmarketdata.Subscription{
		{External: "BTCUSDT", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USDT")},
		{External: "BADUSDT", Base: testMarketDataAssetID("BAD"), Quote: testMarketDataAssetID("USDT")},
	})

	findings, err := connector.Diagnose(context.Background())
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(findings) != 1 || findings[0].Code != CodeUnknownSymbol ||
		findings[0].Instrument != "BADUSDT" {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestBybitConnector_PingsAndUsesCategoryURL(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeBybitSubscriptions(t, []fwmarketdata.Subscription{
		{External: "BTCUSDT", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USDT")},
	})
	conn := &fakeBybitConn{blockRead: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var gotURL string
	connector := &bybitConnector{
		dial: func(_ context.Context, url string) (bybitConn, error) {
			gotURL = url
			return conn, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		pingInterval: time.Millisecond,
		category:     "linear",
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		connector.run(ctx, subs, make(chan fwmarketdata.QuoteUpdate))
	}()

	if !conn.waitForPing(time.Second) {
		t.Fatal("timed out waiting for Bybit ping")
	}
	cancel()
	<-done

	if gotURL != bybitPublicFeedBaseURL+"linear" {
		t.Fatalf("url = %q, want %q", gotURL, bybitPublicFeedBaseURL+"linear")
	}
	if !conn.hasSubscribe("tickers.BTCUSDT") {
		t.Fatalf("writes = %+v, want subscribe for BTCUSDT", conn.writeStrings())
	}
}

func TestBybitConnector_ResetsBackoffAfterRead(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeBybitSubscriptions(t, []fwmarketdata.Subscription{
		{External: "BTCUSDT", Base: testMarketDataAssetID("BTC"), Quote: testMarketDataAssetID("USDT")},
	})
	snapshot := []byte(`{"topic":"tickers.BTCUSDT","type":"snapshot","ts":1710000000000,"data":{"symbol":"BTCUSDT","lastPrice":"65000","bid1Price":"65000","ask1Price":"65001"}}`)
	conns := []*fakeBybitConn{
		{messages: [][]byte{snapshot}, err: errors.New("disconnect 1")},
		{messages: [][]byte{snapshot}, err: errors.New("disconnect 2")},
		{messages: [][]byte{snapshot}, err: context.Canceled},
	}

	var (
		mu     sync.Mutex
		dials  int
		delays []time.Duration
	)
	connector := &bybitConnector{
		dial: func(context.Context, string) (bybitConn, error) {
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
		category:     "spot",
	}

	ch := make(chan fwmarketdata.QuoteUpdate)
	go func() {
		connector.run(context.Background(), subs, ch)
		close(ch)
	}()

	var got []fwmarketdata.QuoteUpdate
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

func mustNormalizeBybitSubscriptions(
	t *testing.T, subs []fwmarketdata.Subscription,
) []bybitSubscription {
	t.Helper()
	normalized, err := normalizeBybitSubscriptions(subs)
	if err != nil {
		t.Fatalf("normalizeBybitSubscriptions: %v", err)
	}
	return normalized
}

type fakeBybitConn struct {
	mu        sync.Mutex
	notify    chan struct{}
	messages  [][]byte
	writes    [][]byte
	err       error
	index     int
	blockRead chan struct{}
}

func (c *fakeBybitConn) Read(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	if c.index < len(c.messages) {
		message := c.messages[c.index]
		c.index++
		c.mu.Unlock()
		return message, nil
	}
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.blockRead:
		return nil, io.EOF
	}
}

func (c *fakeBybitConn) Write(_ context.Context, payload []byte) error {
	c.mu.Lock()
	c.writes = append(c.writes, append([]byte(nil), payload...))
	if c.notify != nil {
		close(c.notify)
		c.notify = nil
	}
	c.mu.Unlock()
	return nil
}

func (c *fakeBybitConn) Close(websocket.StatusCode, string) error {
	return nil
}

func (c *fakeBybitConn) waitForPing(timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		c.mu.Lock()
		for _, write := range c.writes {
			var req bybitOpRequest
			if err := json.Unmarshal(write, &req); err == nil && req.Op == "ping" {
				c.mu.Unlock()
				return true
			}
		}
		notify := make(chan struct{})
		c.notify = notify
		c.mu.Unlock()

		select {
		case <-deadline:
			return false
		case <-notify:
		}
	}
}

func (c *fakeBybitConn) hasSubscribe(topic string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, write := range c.writes {
		var req bybitOpRequest
		if err := json.Unmarshal(write, &req); err == nil &&
			req.Op == "subscribe" &&
			len(req.Args) == 1 &&
			req.Args[0] == topic {
			return true
		}
	}
	return false
}

func (c *fakeBybitConn) writeStrings() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	writes := make([]string, 0, len(c.writes))
	for _, write := range c.writes {
		writes = append(writes, string(write))
	}
	return writes
}
