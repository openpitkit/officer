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
	"io"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestNormalizeBinanceSubscriptions(t *testing.T) {
	t.Parallel()

	subs, err := normalizeBinanceSubscriptions([]Subscription{
		{External: "btcusdt", Base: "BTC", Quote: "USDT"},
		{Base: "Eth", Quote: "Usd"},
	})
	if err != nil {
		t.Fatalf("normalizeBinanceSubscriptions: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("len(subs) = %d, want 2", len(subs))
	}
	if subs[0].symbol != "BTCUSDT" || subs[0].stream != "btcusdt@ticker" {
		t.Fatalf("first sub = %+v", subs[0])
	}
	if subs[1].symbol != "ETHUSD" || subs[1].stream != "ethusd@ticker" {
		t.Fatalf("second sub = %+v", subs[1])
	}
}

func TestParseBinanceQuoteUpdate(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeBinanceSubscriptions(t, []Subscription{
		{External: "BTCUSDT", Base: "BTC", Quote: "USDT"},
	})
	payload := []byte(`{"stream":"btcusdt@ticker","data":{"E":1710000000123,"s":"BTCUSDT","c":"65000.10","b":"65000.01","a":"65000.02"}}`)

	update, ok := parseBinanceQuoteUpdate(payload, subs)
	if !ok {
		t.Fatal("parseBinanceQuoteUpdate returned ok=false")
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

func TestBinanceConnector_ReconnectsAndResubscribes(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeBinanceSubscriptions(t, []Subscription{
		{External: "BTCUSDT", Base: "BTC", Quote: "USDT"},
	})
	first := &fakeBinanceConn{
		messages: [][]byte{
			[]byte(`{"stream":"btcusdt@ticker","data":{"E":1710000000123,"s":"BTCUSDT","c":"1","b":"0.9","a":"1.1"}}`),
		},
		err: errors.New("disconnect"),
	}
	second := &fakeBinanceConn{
		messages: [][]byte{
			[]byte(`{"stream":"btcusdt@ticker","data":{"E":1710000001123,"s":"BTCUSDT","c":"2","b":"1.9","a":"2.1"}}`),
		},
		err: context.Canceled,
	}

	var (
		mu    sync.Mutex
		dials int
	)
	connector := &binanceConnector{
		dial: func(context.Context, string) (binanceConn, error) {
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

	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Mark != "1" || got[1].Mark != "2" {
		t.Fatalf("updates = %+v", got)
	}
}

func TestBinanceConnector_ResetsBackoffAfterRead(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeBinanceSubscriptions(t, []Subscription{
		{External: "BTCUSDT", Base: "BTC", Quote: "USDT"},
	})
	conns := []*fakeBinanceConn{
		{
			messages: [][]byte{
				[]byte(`{"stream":"btcusdt@ticker","data":{"E":1710000000123,"s":"BTCUSDT","c":"1","b":"0.9","a":"1.1"}}`),
			},
			err: errors.New("disconnect 1"),
		},
		{
			messages: [][]byte{
				[]byte(`{"stream":"btcusdt@ticker","data":{"E":1710000001123,"s":"BTCUSDT","c":"2","b":"1.9","a":"2.1"}}`),
			},
			err: errors.New("disconnect 2"),
		},
		{
			messages: [][]byte{
				[]byte(`{"stream":"btcusdt@ticker","data":{"E":1710000002123,"s":"BTCUSDT","c":"3","b":"2.9","a":"3.1"}}`),
			},
			err: context.Canceled,
		},
	}

	var (
		mu     sync.Mutex
		dials  int
		delays []time.Duration
	)
	connector := &binanceConnector{
		dial: func(context.Context, string) (binanceConn, error) {
			mu.Lock()
			defer mu.Unlock()
			conn := conns[dials]
			dials++
			return conn, nil
		},
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"BTCUSDT": {}}, nil
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
			t.Fatalf("delay[%d] = %s, want %s", i, delay, time.Millisecond)
		}
	}
}

func TestBinanceConnector_AllInvalidSymbolsReportsStatusError(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeBinanceSubscriptions(t, []Subscription{
		{External: "NOPEUSDT", Base: "NOPE", Quote: "USDT"},
	})
	var (
		statusOK bool
		status   string
		diags    []Diagnostic
	)
	connector := &binanceConnector{
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"BTCUSDT": {}}, nil
		},
		report: func(ok bool, errMsg string) {
			statusOK = ok
			status = errMsg
		},
		diagReport: func(diag Diagnostic) {
			diags = append(diags, diag)
		},
	}

	connector.run(context.Background(), subs, make(chan QuoteUpdate))

	if statusOK {
		t.Fatal("status ok = true, want false")
	}
	if status != "binance: no valid symbols" {
		t.Fatalf("status = %q, want no valid symbols", status)
	}
	if len(diags) != 1 || diags[0].Code != CodeUnknownSymbol {
		t.Fatalf("diags = %+v, want one unknown-symbol diagnostic", diags)
	}
}

func TestBinanceConnector_CloseStopsSubscription(t *testing.T) {
	t.Parallel()

	blocked := &fakeBinanceConn{blockRead: make(chan struct{})}
	connector := &binanceConnector{
		dial: func(context.Context, string) (binanceConn, error) {
			return blocked, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
	}

	ch, err := connector.Subscribe(context.Background(), []Subscription{
		{External: "BTCUSDT", Base: "BTC", Quote: "USDT"},
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

func TestBinanceConnector_CloseBeforeSubscribeDoesNotBreakLaterClose(t *testing.T) {
	t.Parallel()

	blocked := &fakeBinanceConn{blockRead: make(chan struct{})}
	connector := &binanceConnector{
		dial: func(context.Context, string) (binanceConn, error) {
			return blocked, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
	}

	connector.Close()
	ch, err := connector.Subscribe(context.Background(), []Subscription{
		{External: "BTCUSDT", Base: "BTC", Quote: "USDT"},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	done := make(chan struct{})
	go func() {
		connector.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Close")
	}

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel should be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for connector shutdown")
	}
}

func TestBackoffCaps(t *testing.T) {
	t.Parallel()

	if got := backoff(1, time.Second, 8*time.Second); got != time.Second {
		t.Fatalf("backoff(1) = %s", got)
	}
	if got := backoff(4, time.Second, 8*time.Second); got != 8*time.Second {
		t.Fatalf("backoff(4) = %s", got)
	}
	if got := backoff(10, time.Second, 8*time.Second); got != 8*time.Second {
		t.Fatalf("backoff(10) = %s", got)
	}
}

func TestBinanceConnector_VerifySymbolExists(t *testing.T) {
	t.Parallel()

	connector := &binanceConnector{
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"BTCUSDT": {}, "ETHUSDT": {}}, nil
		},
	}

	got, err := connector.VerifySymbol(context.Background(), "ETHUSDT")
	if err != nil {
		t.Fatalf("VerifySymbol: %v", err)
	}
	if !got.Exists || got.Suggestion != "" {
		t.Fatalf("VerifySymbol = %+v, want Exists=true, no suggestion", got)
	}
}

func TestBinanceConnector_VerifySymbolNotFound(t *testing.T) {
	t.Parallel()

	connector := &binanceConnector{
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"BTCUSDT": {}}, nil
		},
	}

	got, err := connector.VerifySymbol(context.Background(), "DOGEUSDT")
	if err != nil {
		t.Fatalf("VerifySymbol: %v", err)
	}
	if got.Exists || got.Suggestion != "" {
		t.Fatalf("VerifySymbol = %+v, want Exists=false, no suggestion", got)
	}
}

func TestBinanceConnector_VerifySymbolCaseFoldSuggestion(t *testing.T) {
	t.Parallel()

	connector := &binanceConnector{
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"ETHUSDT": {}}, nil
		},
	}

	got, err := connector.VerifySymbol(context.Background(), "ethusdt")
	if err != nil {
		t.Fatalf("VerifySymbol: %v", err)
	}
	if got.Exists {
		t.Fatalf("VerifySymbol = %+v, want Exists=false", got)
	}
	if got.Suggestion != "ETHUSDT" {
		t.Fatalf("VerifySymbol suggestion = %q, want %q", got.Suggestion, "ETHUSDT")
	}
}

func TestBinanceConnector_VerifySymbolFetchError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("exchangeInfo down")
	connector := &binanceConnector{
		fetchSymbols: func(context.Context) (map[string]struct{}, error) {
			return nil, wantErr
		},
	}

	if _, err := connector.VerifySymbol(context.Background(), "BTCUSDT"); !errors.Is(err, wantErr) {
		t.Fatalf("VerifySymbol error = %v, want %v", err, wantErr)
	}
}

func mustNormalizeBinanceSubscriptions(
	t *testing.T, subs []Subscription,
) []binanceSubscription {
	t.Helper()
	normalized, err := normalizeBinanceSubscriptions(subs)
	if err != nil {
		t.Fatalf("normalizeBinanceSubscriptions: %v", err)
	}
	return normalized
}

type fakeBinanceConn struct {
	messages  [][]byte
	err       error
	blockRead chan struct{}
	index     int
	closeOnce sync.Once
}

func (c *fakeBinanceConn) Read(ctx context.Context) ([]byte, error) {
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

func (c *fakeBinanceConn) Close(websocket.StatusCode, string) error {
	c.closeOnce.Do(func() {
		if c.blockRead != nil {
			close(c.blockRead)
			c.blockRead = nil
		}
	})
	return nil
}
