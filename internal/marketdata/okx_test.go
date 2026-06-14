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

func TestNormalizeOKXSubscriptions(t *testing.T) {
	t.Parallel()

	subs, err := normalizeOKXSubscriptions([]Subscription{
		{External: "btc-usdt", Base: "BTC", Quote: "USDT"},
		{Base: "Eth", Quote: "Usd"},
	})
	if err != nil {
		t.Fatalf("normalizeOKXSubscriptions: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("len(subs) = %d, want 2", len(subs))
	}
	if subs[0].instID != "BTC-USDT" || subs[1].instID != "ETH-USD" {
		t.Fatalf("subs = %+v", subs)
	}
}

func TestNormalizeOKXSubscriptionsEmptySymbol(t *testing.T) {
	t.Parallel()

	if _, err := normalizeOKXSubscriptions([]Subscription{{}}); err == nil {
		t.Fatal("normalizeOKXSubscriptions error = nil, want error")
	}
}

func TestOKXSubscribePayload(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOKXSubscriptions(t, []Subscription{
		{External: "BTC-USDT", Base: "BTC", Quote: "USDT"},
	})
	payload, err := okxSubscribePayload(subs)
	if err != nil {
		t.Fatalf("okxSubscribePayload: %v", err)
	}

	var got okxSubscribeRequest
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got.Op != "subscribe" || len(got.Args) != 1 ||
		got.Args[0].Channel != "tickers" || got.Args[0].InstID != "BTC-USDT" {
		t.Fatalf("payload = %+v", got)
	}
}

func TestParseOKXQuoteUpdate(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOKXSubscriptions(t, []Subscription{
		{External: "BTC-USDT", Base: "BTC", Quote: "USDT"},
	})
	payload := []byte(`{"arg":{"channel":"tickers","instId":"BTC-USDT"},"data":[{"instId":"BTC-USDT","last":"65000.10","bidPx":"65000.01","askPx":"65000.02","ts":"1710000000123"}]}`)

	update, ok := parseOKXQuoteUpdate(payload, subs)
	if !ok {
		t.Fatal("parseOKXQuoteUpdate returned ok=false")
	}
	if !update.AsOf.Equal(time.UnixMilli(1710000000123).UTC()) {
		t.Fatalf("AsOf = %s", update.AsOf)
	}
	if update.Base != "BTC" || update.Quote != "USDT" {
		t.Fatalf("instrument = %s/%s", update.Base, update.Quote)
	}
	if update.Mark != "65000.10" || update.Bid != "65000.01" || update.Ask != "65000.02" {
		t.Fatalf("prices = %+v", update)
	}
}

func TestParseOKXQuoteUpdateRejectsInvalidFrames(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOKXSubscriptions(t, []Subscription{
		{External: "BTC-USDT", Base: "BTC", Quote: "USDT"},
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
			payload: `{"arg":{"channel":"tickers","instId":"BTC-USDT"},"data":[{"instId":"BTC-USDT","last":"65000.10","ts":"bad"}]}`,
		},
		{
			name:    "empty data",
			payload: `{"arg":{"channel":"tickers","instId":"BTC-USDT"},"data":[]}`,
		},
		{
			name:    "empty prices",
			payload: `{"arg":{"channel":"tickers","instId":"BTC-USDT"},"data":[{"instId":"BTC-USDT","ts":"1710000000123"}]}`,
		},
		{
			name:    "unsubscribed symbol",
			payload: `{"arg":{"channel":"tickers","instId":"ETH-USDT"},"data":[{"instId":"ETH-USDT","last":"65000.10","ts":"1710000000123"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if update, ok := parseOKXQuoteUpdate([]byte(tt.payload), subs); ok {
				t.Fatalf("parseOKXQuoteUpdate = %+v, want ok=false", update)
			}
		})
	}
}

func TestOKXConnector_PingsAndUsesPublicURL(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOKXSubscriptions(t, []Subscription{
		{External: "BTC-USDT", Base: "BTC", Quote: "USDT"},
	})
	conn := &fakeOKXConn{blockRead: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var gotURL string
	connector := &okxConnector{
		dial: func(_ context.Context, url string) (okxConn, error) {
			gotURL = url
			return conn, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		pingInterval: time.Millisecond,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		connector.run(ctx, subs, make(chan QuoteUpdate))
	}()

	if !conn.waitForWrite("ping", time.Second) {
		t.Fatal("timed out waiting for OKX ping")
	}
	cancel()
	<-done

	if gotURL != okxPublicFeedURL {
		t.Fatalf("url = %q, want %q", gotURL, okxPublicFeedURL)
	}
	if !conn.hasSubscribe("BTC-USDT") {
		t.Fatalf("writes = %+v, want subscribe for BTC-USDT", conn.writeStrings())
	}
}

func TestOKXConnector_ReportsErrorEvent(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOKXSubscriptions(t, []Subscription{
		{External: "BAD-USDT", Base: "BAD", Quote: "USDT"},
	})
	conn := &fakeOKXConn{
		messages: [][]byte{
			[]byte(`{"event":"error","code":"60018","msg":"Invalid request"}`),
		},
		err: context.Canceled,
	}
	var statuses []string
	connector := &okxConnector{
		dial: func(context.Context, string) (okxConn, error) {
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
	if len(statuses) != 1 || statuses[0] != "okx: 60018: Invalid request" {
		t.Fatalf("statuses = %v", statuses)
	}
}

func TestOKXConnector_SubscribeAckDoesNotReportUnparsable(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOKXSubscriptions(t, []Subscription{
		{External: "BTC-USDT", Base: "BTC", Quote: "USDT"},
	})
	conn := &fakeOKXConn{
		messages: [][]byte{
			[]byte(`{"event":"subscribe","arg":{"channel":"tickers","instId":"BTC-USDT"}}`),
			[]byte(`{"event":"subscribe","arg":{"channel":"tickers","instId":"ETH-USDT"}}`),
			[]byte(`{"event":"subscribe","arg":{"channel":"tickers","instId":"SOL-USDT"}}`),
		},
		err: context.Canceled,
	}
	var diagnostics []Diagnostic
	connector := &okxConnector{
		dial: func(context.Context, string) (okxConn, error) {
			return conn, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		diagReport: func(diag Diagnostic) {
			diagnostics = append(diagnostics, diag)
		},
	}

	if _, err := connector.stream(context.Background(), subs, make(chan QuoteUpdate)); !errors.Is(err, context.Canceled) {
		t.Fatalf("stream err = %v, want context.Canceled", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v, want none for subscribe acks", diagnostics)
	}
}

func TestOKXVerifySymbolUsesInstrumentCatalogue(t *testing.T) {
	t.Parallel()

	connector := NewOKXConnector()
	connector.fetchSymbols = func(context.Context) (map[string]struct{}, error) {
		return map[string]struct{}{"BTC-USDT": {}}, nil
	}

	exact, err := connector.VerifySymbol(context.Background(), "BTC-USDT")
	if err != nil {
		t.Fatalf("VerifySymbol exact: %v", err)
	}
	if !exact.Exists || exact.Suggestion != "" {
		t.Fatalf("exact = %+v, want exists", exact)
	}
	folded, err := connector.VerifySymbol(context.Background(), "btc-usdt")
	if err != nil {
		t.Fatalf("VerifySymbol folded: %v", err)
	}
	if folded.Exists || folded.Suggestion != "BTC-USDT" {
		t.Fatalf("folded = %+v, want suggestion BTC-USDT", folded)
	}
}

func TestOKXDiagnoseUnknownSymbol(t *testing.T) {
	t.Parallel()

	connector := NewOKXConnector()
	connector.fetchSymbols = func(context.Context) (map[string]struct{}, error) {
		return map[string]struct{}{"BTC-USDT": {}}, nil
	}
	connector.subs = mustNormalizeOKXSubscriptions(t, []Subscription{
		{External: "BTC-USDT", Base: "BTC", Quote: "USDT"},
		{External: "BAD-USDT", Base: "BAD", Quote: "USDT"},
	})

	findings, err := connector.Diagnose(context.Background())
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(findings) != 1 || findings[0].Code != CodeUnknownSymbol ||
		findings[0].Instrument != "BAD/USDT" {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestOKXConnector_ResetsBackoffAfterRead(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeOKXSubscriptions(t, []Subscription{
		{External: "BTC-USDT", Base: "BTC", Quote: "USDT"},
	})
	conns := []*fakeOKXConn{
		{
			blockRead: make(chan struct{}),
			messages: [][]byte{
				[]byte(`{"arg":{"channel":"tickers","instId":"BTC-USDT"},"data":[{"instId":"BTC-USDT","last":"1","bidPx":"0.9","askPx":"1.1","ts":"1710000000123"}]}`),
			},
			err: errors.New("disconnect 1"),
		},
		{
			blockRead: make(chan struct{}),
			messages: [][]byte{
				[]byte(`{"arg":{"channel":"tickers","instId":"BTC-USDT"},"data":[{"instId":"BTC-USDT","last":"2","bidPx":"1.9","askPx":"2.1","ts":"1710000001123"}]}`),
			},
			err: errors.New("disconnect 2"),
		},
		{
			blockRead: make(chan struct{}),
			messages: [][]byte{
				[]byte(`{"arg":{"channel":"tickers","instId":"BTC-USDT"},"data":[{"instId":"BTC-USDT","last":"3","bidPx":"2.9","askPx":"3.1","ts":"1710000002123"}]}`),
			},
			err: context.Canceled,
		},
	}

	var (
		mu     sync.Mutex
		dials  int
		delays []time.Duration
	)
	connector := &okxConnector{
		dial: func(context.Context, string) (okxConn, error) {
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

func mustNormalizeOKXSubscriptions(
	t *testing.T, subs []Subscription,
) []okxSubscription {
	t.Helper()
	normalized, err := normalizeOKXSubscriptions(subs)
	if err != nil {
		t.Fatalf("normalizeOKXSubscriptions: %v", err)
	}
	return normalized
}

type fakeOKXConn struct {
	mu        sync.Mutex
	notify    chan struct{}
	messages  [][]byte
	writes    [][]byte
	err       error
	index     int
	blockRead chan struct{}
}

func (c *fakeOKXConn) Read(ctx context.Context) ([]byte, error) {
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

func (c *fakeOKXConn) Write(_ context.Context, payload []byte) error {
	c.mu.Lock()
	c.writes = append(c.writes, append([]byte(nil), payload...))
	if c.notify != nil {
		close(c.notify)
		c.notify = nil
	}
	c.mu.Unlock()
	return nil
}

func (c *fakeOKXConn) Close(websocket.StatusCode, string) error {
	return nil
}

func (c *fakeOKXConn) waitForWrite(want string, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		c.mu.Lock()
		for _, write := range c.writes {
			if string(write) == want {
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

func (c *fakeOKXConn) hasSubscribe(instID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, write := range c.writes {
		var req okxSubscribeRequest
		if err := json.Unmarshal(write, &req); err == nil &&
			req.Op == "subscribe" &&
			len(req.Args) == 1 &&
			req.Args[0].InstID == instID {
			return true
		}
	}
	return false
}

func (c *fakeOKXConn) writeStrings() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	writes := make([]string, 0, len(c.writes))
	for _, write := range c.writes {
		writes = append(writes, string(write))
	}
	return writes
}

func TestOKXConnector_CloseStopsSubscription(t *testing.T) {
	t.Parallel()

	conn := &fakeOKXConn{blockRead: make(chan struct{})}
	connector := &okxConnector{
		dial: func(context.Context, string) (okxConn, error) {
			return conn, nil
		},
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"BTC-USDT": {}}, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		pingInterval: time.Hour,
		readTimeout:  time.Hour,
		report:       func(bool, string) {},
		diagReport:   func(Diagnostic) {},
	}
	out, err := connector.Subscribe(context.Background(), []Subscription{
		{External: "BTC-USDT", Base: "BTC", Quote: "USDT"},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for !conn.hasSubscribe("BTC-USDT") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !conn.hasSubscribe("BTC-USDT") {
		t.Fatal("subscribe frame was not written")
	}

	connector.Close()
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
