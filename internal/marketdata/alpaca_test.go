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
	"go.openpit.dev/officer/framework/domain"
)

func TestParseAlpacaCredentials(t *testing.T) {
	t.Parallel()

	connector, err := NewAlpacaConnector(domain.MarketDataInstance{
		Credentials: `{"apiKey":" key ","apiSecret":" secret "}`,
	})
	if err != nil {
		t.Fatalf("NewAlpacaConnector: %v", err)
	}
	if connector.credentials.APIKey != "key" {
		t.Fatalf("APIKey = %q, want key", connector.credentials.APIKey)
	}
	if connector.credentials.APISecret != "secret" {
		t.Fatalf("APISecret = %q, want secret", connector.credentials.APISecret)
	}
}

func TestParseAlpacaCredentialsAliases(t *testing.T) {
	t.Parallel()

	connector, err := NewAlpacaConnector(domain.MarketDataInstance{
		Credentials: `{"key":"alias-key","secret":"alias-secret"}`,
	})
	if err != nil {
		t.Fatalf("NewAlpacaConnector: %v", err)
	}
	if connector.credentials.APIKey != "alias-key" {
		t.Fatalf("APIKey = %q, want alias-key", connector.credentials.APIKey)
	}
	if connector.credentials.APISecret != "alias-secret" {
		t.Fatalf("APISecret = %q, want alias-secret", connector.credentials.APISecret)
	}
}

func TestParseAlpacaCredentialsRejectsMissingSecret(t *testing.T) {
	t.Parallel()

	if _, err := NewAlpacaConnector(domain.MarketDataInstance{
		Credentials: `{"apiKey":"key"}`,
	}); err == nil {
		t.Fatal("NewAlpacaConnector err = nil, want error")
	}
}

func TestNormalizeAlpacaSubscriptionsLimit(t *testing.T) {
	t.Parallel()

	subs := make([]Subscription, alpacaSymbolLimit+1)
	for i := range subs {
		subs[i] = Subscription{External: "AAPL", Base: "AAPL", Quote: "USD"}
	}
	if _, err := normalizeAlpacaSubscriptions(subs); err == nil {
		t.Fatal("normalizeAlpacaSubscriptions err = nil, want limit error")
	}
}

func TestWriteAlpacaSubscriptionShape(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeAlpacaSubscriptions(t, []Subscription{
		{External: "aapl", Base: "AAPL", Quote: "USD"},
		{External: "msft", Base: "MSFT", Quote: "USD"},
	})
	conn := &fakeAlpacaConn{}
	if err := writeAlpacaSubscribe(context.Background(), conn, subs); err != nil {
		t.Fatalf("writeAlpacaSubscribe: %v", err)
	}

	var frame alpacaFrame
	if err := json.Unmarshal(conn.writes[0], &frame); err != nil {
		t.Fatalf("unmarshal subscribe: %v", err)
	}
	if frame.Action != "subscribe" {
		t.Fatalf("action = %q, want subscribe", frame.Action)
	}
	if !slices.Equal(frame.Quotes, []string{"AAPL", "MSFT"}) {
		t.Fatalf("quotes = %v", frame.Quotes)
	}
	if !slices.Equal(frame.Trades, []string{"AAPL", "MSFT"}) {
		t.Fatalf("trades = %v", frame.Trades)
	}
}

func TestParseAlpacaQuoteUpdates(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeAlpacaSubscriptions(t, []Subscription{
		{External: "AAPL", Base: "AAPL", Quote: "USD"},
	})
	payload := []byte(`[
		{"T":"q","S":"AAPL","bp":12345678901234567890.123456789,"ap":12345678901234567890.223456789,"t":"2026-06-20T10:11:12.123456789Z"},
		{"T":"t","S":"AAPL","p":12345678901234567890.323456789,"t":"2026-06-20T10:11:13Z"}
	]`)

	updates := parseAlpacaQuoteUpdates(payload, subs)
	if len(updates) != 2 {
		t.Fatalf("len(updates) = %d, want 2", len(updates))
	}
	if updates[0].Bid != "12345678901234567890.123456789" ||
		updates[0].Ask != "12345678901234567890.223456789" {
		t.Fatalf("quote update = %+v", updates[0])
	}
	if updates[1].Mark != "12345678901234567890.323456789" {
		t.Fatalf("trade update = %+v", updates[1])
	}
	if !updates[0].AsOf.Equal(time.Date(2026, 6, 20, 10, 11, 12, 123456789, time.UTC)) {
		t.Fatalf("AsOf = %s", updates[0].AsOf)
	}
}

func TestParseAlpacaQuoteUpdatesStringPrices(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeAlpacaSubscriptions(t, []Subscription{
		{External: "AAPL", Base: "AAPL", Quote: "USD"},
	})
	payload := []byte(`[
		{"T":"q","S":"AAPL","bp":"191.01","ap":"191.03","t":"2026-06-20T10:11:12Z"},
		{"T":"t","S":"AAPL","p":"191.02","t":"2026-06-20T10:11:13Z"}
	]`)

	updates := parseAlpacaQuoteUpdates(payload, subs)
	if len(updates) != 2 {
		t.Fatalf("len(updates) = %d, want 2", len(updates))
	}
	if updates[0].Bid != "191.01" || updates[0].Ask != "191.03" {
		t.Fatalf("quote update = %+v", updates[0])
	}
	if updates[1].Mark != "191.02" {
		t.Fatalf("trade update = %+v", updates[1])
	}
}

func TestAlpacaConnector_ReconnectsAndResubscribes(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeAlpacaSubscriptions(t, []Subscription{
		{External: "AAPL", Base: "AAPL", Quote: "USD"},
	})
	first := &fakeAlpacaConn{
		messages: [][]byte{
			[]byte(`[{"T":"success","msg":"authenticated"}]`),
			[]byte(`[{"T":"t","S":"AAPL","p":1,"t":"2026-06-20T10:11:12Z"}]`),
		},
		err: errors.New("disconnect"),
	}
	second := &fakeAlpacaConn{
		messages: [][]byte{
			[]byte(`[{"T":"success","msg":"authenticated"}]`),
			[]byte(`[{"T":"t","S":"AAPL","p":2,"t":"2026-06-20T10:11:13Z"}]`),
		},
		err: context.Canceled,
	}

	var (
		mu    sync.Mutex
		dials int
	)
	connector := &alpacaConnector{
		dial: func(context.Context, string) (alpacaConn, error) {
			mu.Lock()
			defer mu.Unlock()
			dials++
			if dials == 1 {
				return first, nil
			}
			return second, nil
		},
		sleep:        func(context.Context, time.Duration) error { return nil },
		reconnectMin: time.Millisecond,
		reconnectMax: time.Millisecond,
		credentials: alpacaCredentials{
			APIKey:    "key",
			APISecret: "secret",
		},
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
	assertAlpacaAuthAndSubscribeWrites(t, first)
	assertAlpacaAuthAndSubscribeWrites(t, second)
}

func TestAlpacaConnector_AuthFailureDoesNotReportConnected(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeAlpacaSubscriptions(t, []Subscription{
		{External: "AAPL", Base: "AAPL", Quote: "USD"},
	})
	conn := &fakeAlpacaConn{
		messages: [][]byte{[]byte(`[{"T":"error","code":401,"msg":"auth failed"}]`)},
	}
	var okReported bool
	connector := &alpacaConnector{
		dial: func(context.Context, string) (alpacaConn, error) {
			return conn, nil
		},
		credentials: alpacaCredentials{
			APIKey:    "key",
			APISecret: "secret",
		},
		report: func(ok bool, _ string) {
			if ok {
				okReported = true
			}
		},
	}

	_, err := connector.stream(context.Background(), subs, make(chan QuoteUpdate))
	if err == nil || !strings.Contains(err.Error(), "401: auth failed") {
		t.Fatalf("stream err = %v, want auth failure", err)
	}
	if okReported {
		t.Fatal("reportStatus(true) called before auth success")
	}
}

func TestAlpacaConnector_ReportsErrorFrame(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeAlpacaSubscriptions(t, []Subscription{
		{External: "AAPL", Base: "AAPL", Quote: "USD"},
	})
	conn := &fakeAlpacaConn{
		messages: [][]byte{
			[]byte(`[{"T":"success","msg":"authenticated"}]`),
			[]byte(`[{"T":"error","code":400,"msg":"invalid symbol"}]`),
		},
		err: context.Canceled,
	}
	var statuses []string
	connector := &alpacaConnector{
		dial: func(context.Context, string) (alpacaConn, error) {
			return conn, nil
		},
		credentials: alpacaCredentials{
			APIKey:    "key",
			APISecret: "secret",
		},
		report: func(ok bool, errMsg string) {
			if !ok {
				statuses = append(statuses, errMsg)
			}
		},
	}

	if _, err := connector.stream(context.Background(), subs, make(chan QuoteUpdate)); !errors.Is(err, context.Canceled) {
		t.Fatalf("stream err = %v, want context.Canceled", err)
	}
	if len(statuses) != 1 || statuses[0] != "alpaca: 400: invalid symbol" {
		t.Fatalf("statuses = %v", statuses)
	}
}

func TestAlpacaConnector_ResetsBackoffAfterRead(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeAlpacaSubscriptions(t, []Subscription{
		{External: "AAPL", Base: "AAPL", Quote: "USD"},
	})
	conns := []*fakeAlpacaConn{
		{
			messages: [][]byte{
				[]byte(`[{"T":"success","msg":"authenticated"}]`),
				[]byte(`[{"T":"t","S":"AAPL","p":1,"t":"2026-06-20T10:11:12Z"}]`),
			},
			err: errors.New("disconnect 1"),
		},
		{
			messages: [][]byte{
				[]byte(`[{"T":"success","msg":"authenticated"}]`),
				[]byte(`[{"T":"t","S":"AAPL","p":2,"t":"2026-06-20T10:11:13Z"}]`),
			},
			err: errors.New("disconnect 2"),
		},
		{
			messages: [][]byte{
				[]byte(`[{"T":"success","msg":"authenticated"}]`),
				[]byte(`[{"T":"t","S":"AAPL","p":3,"t":"2026-06-20T10:11:14Z"}]`),
			},
			err: context.Canceled,
		},
	}

	var (
		mu     sync.Mutex
		dials  int
		delays []time.Duration
	)
	connector := &alpacaConnector{
		dial: func(context.Context, string) (alpacaConn, error) {
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
		credentials: alpacaCredentials{
			APIKey:    "key",
			APISecret: "secret",
		},
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

func mustNormalizeAlpacaSubscriptions(
	t *testing.T, subs []Subscription,
) []alpacaSubscription {
	t.Helper()
	normalized, err := normalizeAlpacaSubscriptions(subs)
	if err != nil {
		t.Fatalf("normalizeAlpacaSubscriptions: %v", err)
	}
	return normalized
}

func assertAlpacaAuthAndSubscribeWrites(t *testing.T, conn *fakeAlpacaConn) {
	t.Helper()
	if len(conn.writes) != 2 {
		t.Fatalf("len(writes) = %d, want 2", len(conn.writes))
	}
	var auth alpacaFrame
	if err := json.Unmarshal(conn.writes[0], &auth); err != nil {
		t.Fatalf("unmarshal auth: %v", err)
	}
	if auth.Action != "auth" || auth.Key != "key" || auth.Secret != "secret" {
		t.Fatalf("auth frame = %+v", auth)
	}
	var subscribe alpacaFrame
	if err := json.Unmarshal(conn.writes[1], &subscribe); err != nil {
		t.Fatalf("unmarshal subscribe: %v", err)
	}
	if subscribe.Action != "subscribe" || !slices.Equal(subscribe.Trades, []string{"AAPL"}) {
		t.Fatalf("subscribe frame = %+v", subscribe)
	}
}

type fakeAlpacaConn struct {
	messages  [][]byte
	writes    [][]byte
	err       error
	index     int
	closeOnce sync.Once
}

func (c *fakeAlpacaConn) Read(context.Context) ([]byte, error) {
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

func (c *fakeAlpacaConn) Write(_ context.Context, payload []byte) error {
	c.writes = append(c.writes, append([]byte(nil), payload...))
	return nil
}

func (c *fakeAlpacaConn) Close(websocket.StatusCode, string) error {
	c.closeOnce.Do(func() {})
	return nil
}
