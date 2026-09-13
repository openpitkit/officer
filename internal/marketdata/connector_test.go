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
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVerifySymbolFromSet(t *testing.T) {
	t.Parallel()

	known := map[string]struct{}{"BTCUSDT": {}, "ETH-USDT": {}}
	if got := verifySymbolFromSet(known, "BTCUSDT"); !got.Exists || got.Suggestion != "" {
		t.Fatalf("exact = %+v, want exists", got)
	}
	if got := verifySymbolFromSet(known, "eth-usdt"); got.Exists || got.Suggestion != "ETH-USDT" {
		t.Fatalf("folded = %+v, want suggestion ETH-USDT", got)
	}
	if got := verifySymbolFromSet(known, "DOGEUSDT"); got.Exists || got.Suggestion != "" {
		t.Fatalf("missing = %+v, want zero", got)
	}
	if got := verifySymbolFromSet(known, " "); got.Exists || got.Suggestion != "" {
		t.Fatalf("blank = %+v, want zero", got)
	}
}

func TestSearchSymbolsFromSetMatchesEncodedPairs(t *testing.T) {
	t.Parallel()

	known := map[string]struct{}{
		"BTCUSDT":  {},
		"BTC-USDT": {},
		"ETHUSDT":  {},
	}
	matches := searchSymbolsFromSet(
		known, fwmarketdata.SymbolSearchQuery{Query: "BTC/USDT"}, "SPOT",
	)
	if len(matches) < 2 {
		t.Fatalf("matches = %+v, want BTC variants", matches)
	}
	if matches[0].Symbol != "BTC-USDT" && matches[0].Symbol != "BTCUSDT" {
		t.Fatalf("first match = %+v, want BTC pair", matches[0])
	}
	for _, match := range matches {
		if match.SecType != "SPOT" {
			t.Fatalf("match secType = %q, want SPOT", match.SecType)
		}
		if !strings.Contains(normalizedSymbolSearchKey(match.Symbol), "BTCUSDT") {
			t.Fatalf("unexpected match for BTC/USDT: %+v", match)
		}
	}
}

func TestSearchSymbolsFromSetMatchesBaseOrSubstring(t *testing.T) {
	t.Parallel()

	known := map[string]struct{}{
		"EURUSD": {},
		"XBEUR":  {},
		"AAPL":   {},
	}
	matches := searchSymbolsFromSet(
		known, fwmarketdata.SymbolSearchQuery{Query: "EUR"}, "SPOT",
	)
	if len(matches) != 2 {
		t.Fatalf("matches = %+v, want EURUSD and XBEUR", matches)
	}
	if matches[0].Symbol != "EURUSD" {
		t.Fatalf("first match = %+v, want base/prefix match first", matches[0])
	}
}

func TestProviderUnknownSymbolDiag(t *testing.T) {
	t.Parallel()

	diag := providerUnknownSymbolDiag("Provider", "BAD")
	if diag.Code != CodeUnknownSymbol || diag.Kind != DiagKindConfig {
		t.Fatalf("diag = %+v", diag)
	}
	if diag.Instrument != "BAD" {
		t.Fatalf("instrument = %q, want BAD", diag.Instrument)
	}
	if len(diag.Actions) != 2 || diag.Actions[0].Type != ActionRemoveInstrument ||
		diag.Actions[0].Target != "BAD" || diag.Actions[1].Type != ActionOpenSymbols {
		t.Fatalf("actions = %+v", diag.Actions)
	}
}

func TestSubscriptionNormalizersRequireExternalSymbol(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		normalize func(fwmarketdata.Subscription) error
	}{
		{
			name: "alpaca",
			normalize: func(sub fwmarketdata.Subscription) error {
				_, err := normalizeAlpacaSubscriptions([]fwmarketdata.Subscription{sub})
				return err
			},
		},
		{
			name: "binance",
			normalize: func(sub fwmarketdata.Subscription) error {
				_, err := normalizeBinanceSubscriptions([]fwmarketdata.Subscription{sub})
				return err
			},
		},
		{
			name: "bybit",
			normalize: func(sub fwmarketdata.Subscription) error {
				_, err := normalizeBybitSubscriptions([]fwmarketdata.Subscription{sub})
				return err
			},
		},
		{
			name: "coinbase",
			normalize: func(sub fwmarketdata.Subscription) error {
				_, err := normalizeCoinbaseSubscriptions([]fwmarketdata.Subscription{sub})
				return err
			},
		},
		{
			name: "finnhub",
			normalize: func(sub fwmarketdata.Subscription) error {
				_, err := normalizeFinnhubSubscriptions([]fwmarketdata.Subscription{sub})
				return err
			},
		},
		{
			name: "ib",
			normalize: func(sub fwmarketdata.Subscription) error {
				_, err := normalizeIBSubscriptions(ibConfig{}, []fwmarketdata.Subscription{sub})
				return err
			},
		},
		{
			name: "kraken",
			normalize: func(sub fwmarketdata.Subscription) error {
				_, err := normalizeKrakenSubscriptions([]fwmarketdata.Subscription{sub})
				return err
			},
		},
		{
			name: "oanda",
			normalize: func(sub fwmarketdata.Subscription) error {
				_, err := normalizeOANDASubscriptions([]fwmarketdata.Subscription{sub})
				return err
			},
		},
		{
			name: "okx",
			normalize: func(sub fwmarketdata.Subscription) error {
				_, err := normalizeOKXSubscriptions([]fwmarketdata.Subscription{sub})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.normalize(fwmarketdata.Subscription{Base: 41, Quote: 42})
			if err == nil {
				t.Fatal("normalize error = nil, want missing external symbol")
			}
			if !strings.Contains(err.Error(), "external symbol is missing") {
				t.Fatalf("normalize error = %q, want missing external symbol", err)
			}
			if !strings.Contains(err.Error(), "41/42") {
				t.Fatalf("normalize error = %q, want decimal asset ids 41/42", err)
			}
		})
	}
}

func TestNextWebsocketRead(t *testing.T) {
	t.Parallel()

	reads := make(chan websocketReadResult, 1)
	reads <- websocketReadResult{payload: []byte("payload")}
	payload, err := nextWebsocketRead(context.Background(), reads, time.Second)
	if err != nil {
		t.Fatalf("nextWebsocketRead: %v", err)
	}
	if string(payload) != "payload" {
		t.Fatalf("payload = %q, want payload", payload)
	}
}

func TestWebsocketReadLoopStopsOnError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("read failed")
	calls := 0
	reads := websocketReadLoop(context.Background(), func(context.Context) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("payload"), nil
		}
		return nil, wantErr
	})

	result := <-reads
	if string(result.payload) != "payload" || result.err != nil {
		t.Fatalf("first read = %+v, want payload", result)
	}
	result = <-reads
	if !errors.Is(result.err, wantErr) {
		t.Fatalf("second read err = %v, want %v", result.err, wantErr)
	}
	select {
	case _, ok := <-reads:
		if ok {
			t.Fatal("read loop kept running after error")
		}
	case <-time.After(time.Second):
		t.Fatal("read loop did not close after error")
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestWebsocketReadLoopStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var startOnce sync.Once
	reads := websocketReadLoop(ctx, func(ctx context.Context) ([]byte, error) {
		startOnce.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	})

	<-started
	cancel()
	select {
	case result, ok := <-reads:
		if ok {
			if !errors.Is(result.err, context.Canceled) {
				t.Fatalf("cancel result err = %v, want context.Canceled", result.err)
			}
			select {
			case _, ok := <-reads:
				if ok {
					t.Fatal("read loop kept running after cancel")
				}
			case <-time.After(time.Second):
				t.Fatal("read loop did not close after cancel result")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("read loop did not stop after cancel")
	}
}

func TestNextWebsocketReadAuxiliaryError(t *testing.T) {
	t.Parallel()

	reads := make(chan websocketReadResult)
	errs := make(chan error, 1)
	wantErr := errors.New("ping failed")
	errs <- wantErr
	if _, err := nextWebsocketRead(context.Background(), reads, time.Second, errs); !errors.Is(err, wantErr) {
		t.Fatalf("nextWebsocketRead err = %v, want %v", err, wantErr)
	}
}

func TestNextWebsocketReadContextDone(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := nextWebsocketRead(ctx, make(chan websocketReadResult), time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("nextWebsocketRead err = %v, want context.Canceled", err)
	}
}

func TestNextWebsocketReadClosedChannels(t *testing.T) {
	t.Parallel()

	reads := make(chan websocketReadResult)
	close(reads)
	if _, err := nextWebsocketRead(context.Background(), reads, time.Second); !errors.Is(err, io.EOF) {
		t.Fatalf("closed reads err = %v, want EOF", err)
	}

	errs := make(chan error)
	close(errs)
	if _, err := nextWebsocketRead(context.Background(), make(chan websocketReadResult), time.Second, errs); !errors.Is(err, io.EOF) {
		t.Fatalf("closed errs err = %v, want EOF", err)
	}
}

func TestNextWebsocketReadClosedReadsWithCancelledContext(t *testing.T) {
	t.Parallel()

	reads := make(chan websocketReadResult)
	close(reads)
	ctx := errOnlyContext{
		Context: context.Background(),
		err:     context.Canceled,
	}
	if _, err := nextWebsocketRead(ctx, reads, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed reads err = %v, want context.Canceled", err)
	}
}

func TestNextWebsocketReadTimeout(t *testing.T) {
	t.Parallel()

	_, err := nextWebsocketRead(context.Background(), make(chan websocketReadResult), time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "websocket read timeout after") {
		t.Fatalf("nextWebsocketRead timeout err = %v", err)
	}
}

func TestUnparsableTrackerReportsOnceAfterThreshold(t *testing.T) {
	t.Parallel()

	var tracker unparsableTracker
	var diagnostics []fwmarketdata.Diagnostic
	report := func(diag fwmarketdata.Diagnostic) { diagnostics = append(diagnostics, diag) }

	tracker.recordUnparsed(report)
	tracker.recordUnparsed(report)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics before threshold = %+v", diagnostics)
	}
	tracker.recordUnparsed(report)
	tracker.recordUnparsed(report)
	if len(diagnostics) != 1 || diagnostics[0].Code != CodeUnparsableData {
		t.Fatalf("diagnostics = %+v, want one unparsable_data", diagnostics)
	}
}

func TestUnparsableTrackerDoesNotReportAfterParsedFrame(t *testing.T) {
	t.Parallel()

	var tracker unparsableTracker
	var diagnostics []fwmarketdata.Diagnostic
	report := func(diag fwmarketdata.Diagnostic) { diagnostics = append(diagnostics, diag) }

	tracker.recordParsed()
	for i := 0; i < unparsableThreshold+1; i++ {
		tracker.recordUnparsed(report)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v, want none after parsed frame", diagnostics)
	}
}

type errOnlyContext struct {
	context.Context
	err error
}

func (c errOnlyContext) Done() <-chan struct{} {
	return nil
}

func (c errOnlyContext) Err() error {
	return c.err
}
