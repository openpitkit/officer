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
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.openpit.dev/officer/framework/domain"
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
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

	subs := make([]fwmarketdata.Subscription, alpacaSymbolLimit+1)
	for i := range subs {
		subs[i] = fwmarketdata.Subscription{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")}
	}
	if _, err := normalizeAlpacaSubscriptions(subs); err == nil {
		t.Fatal("normalizeAlpacaSubscriptions err = nil, want limit error")
	}
}

func TestWriteAlpacaSubscriptionShape(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeAlpacaSubscriptions(t, []fwmarketdata.Subscription{
		{External: "aapl", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
		{External: "msft", Base: testMarketDataAssetID("MSFT"), Quote: testMarketDataAssetID("USD")},
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

	subs := mustNormalizeAlpacaSubscriptions(t, []fwmarketdata.Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
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

	subs := mustNormalizeAlpacaSubscriptions(t, []fwmarketdata.Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
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

	subs := mustNormalizeAlpacaSubscriptions(t, []fwmarketdata.Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
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
	connector := newTestAlpacaConnector(t)
	connector.dial = func(context.Context, string) (alpacaConn, error) {
		mu.Lock()
		defer mu.Unlock()
		dials++
		if dials == 1 {
			return first, nil
		}
		return second, nil
	}
	connector.sleep = func(context.Context, time.Duration) error { return nil }
	connector.reconnectMin = time.Millisecond
	connector.reconnectMax = time.Millisecond

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

	subs := mustNormalizeAlpacaSubscriptions(t, []fwmarketdata.Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	conn := &fakeAlpacaConn{
		messages: [][]byte{[]byte(`[{"T":"error","code":401,"msg":"auth failed"}]`)},
	}
	var okReported bool
	connector := newTestAlpacaConnector(t)
	connector.dial = func(context.Context, string) (alpacaConn, error) {
		return conn, nil
	}
	connector.report = func(ok bool, _ string) {
		if ok {
			okReported = true
		}
	}

	_, err := connector.stream(context.Background(), subs, make(chan fwmarketdata.QuoteUpdate))
	if err == nil || !strings.Contains(err.Error(), "401: auth failed") {
		t.Fatalf("stream err = %v, want auth failure", err)
	}
	if okReported {
		t.Fatal("reportStatus(true) called before auth success")
	}
}

func TestAlpacaConnector_ReportsErrorFrame(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeAlpacaSubscriptions(t, []fwmarketdata.Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	conn := &fakeAlpacaConn{
		messages: [][]byte{
			[]byte(`[{"T":"success","msg":"authenticated"}]`),
			[]byte(`[{"T":"error","code":400,"msg":"invalid symbol"}]`),
		},
		err: context.Canceled,
	}
	var statuses []string
	connector := newTestAlpacaConnector(t)
	connector.dial = func(context.Context, string) (alpacaConn, error) {
		return conn, nil
	}
	connector.report = func(ok bool, errMsg string) {
		if !ok {
			statuses = append(statuses, errMsg)
		}
	}

	if _, err := connector.stream(context.Background(), subs, make(chan fwmarketdata.QuoteUpdate)); !errors.Is(err, context.Canceled) {
		t.Fatalf("stream err = %v, want context.Canceled", err)
	}
	if len(statuses) != 1 || statuses[0] != "alpaca: 400: invalid symbol" {
		t.Fatalf("statuses = %v", statuses)
	}
}

func TestAlpacaConnector_ResetsBackoffAfterRead(t *testing.T) {
	t.Parallel()

	subs := mustNormalizeAlpacaSubscriptions(t, []fwmarketdata.Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
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
	connector := newTestAlpacaConnector(t)
	connector.dial = func(context.Context, string) (alpacaConn, error) {
		mu.Lock()
		defer mu.Unlock()
		conn := conns[dials]
		dials++
		return conn, nil
	}
	connector.sleep = func(_ context.Context, delay time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		delays = append(delays, delay)
		return nil
	}
	connector.reconnectMin = time.Millisecond
	connector.reconnectMax = 4 * time.Millisecond

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

// TestAlpacaConnector_RedactsCredentialsInStatus feeds auth replies that echo
// a credential, raw or JSON-escaped, and expects the published status to keep
// the venue text with the credential redacted.
func TestAlpacaConnector_RedactsCredentialsInStatus(t *testing.T) {
	t.Parallel()

	const (
		apiKey    = "PKREDACTIONKEY"
		apiSecret = `redaction"secret<value>`
	)
	credentials, err := json.Marshal(map[string]string{"apiKey": apiKey, "apiSecret": apiSecret})
	if err != nil {
		t.Fatal(err)
	}
	quoted, err := json.Marshal(apiSecret)
	if err != nil {
		t.Fatal(err)
	}
	// The JSON spelling escapes the quote and the angle brackets.
	escapedSecret := string(quoted[1 : len(quoted)-1])
	subs := mustNormalizeAlpacaSubscriptions(t, []fwmarketdata.Subscription{
		{External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD")},
	})
	for _, tc := range []struct {
		name string
		msg  string
		want string
	}{
		{"api key", "unknown key " + apiKey, "stream error: alpaca auth: 402: unknown key [redacted]"},
		{"json-escaped api secret", `bad secret "` + escapedSecret + `"`, `stream error: alpaca auth: 402: bad secret "[redacted]"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reply, err := json.Marshal([]alpacaControlEvent{{Type: "error", Code: 402, Msg: tc.msg}})
			if err != nil {
				t.Fatal(err)
			}
			connector, err := NewAlpacaConnector(domain.MarketDataInstance{Credentials: string(credentials)})
			if err != nil {
				t.Fatalf("NewAlpacaConnector: %v", err)
			}
			connector.dial = func(context.Context, string) (alpacaConn, error) {
				return &fakeAlpacaConn{messages: [][]byte{reply}}, nil
			}
			// The first reconnect wait ends the run.
			connector.sleep = func(context.Context, time.Duration) error { return context.Canceled }
			var statuses []string
			connector.report = func(_ bool, errMsg string) {
				statuses = append(statuses, errMsg)
			}

			connector.run(context.Background(), subs, make(chan fwmarketdata.QuoteUpdate))
			if len(statuses) != 1 || statuses[0] != tc.want {
				t.Fatalf("statuses = %q, want [%q]", statuses, tc.want)
			}
		})
	}
}

// TestAlpacaDialRefusesRedirect pins that the connector's dial refuses a
// redirect, as the trading stream's does, so the auth frame cannot reach a
// host chosen by a redirect response.
func TestAlpacaDialRefusesRedirect(t *testing.T) {
	t.Parallel()

	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.CloseNow()
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := newTestAlpacaConnector(t).dial(ctx, server.URL)
	if err == nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		t.Error("stream dial followed a redirect")
	}
	if reached.Load() {
		t.Error("redirect target received the stream dial")
	}
}

// newTestAlpacaConnector builds a connector as production does, so its
// credentials (key "key", secret "secret") and its redactor are set.
func newTestAlpacaConnector(t *testing.T) *alpacaConnector {
	t.Helper()
	connector, err := NewAlpacaConnector(domain.MarketDataInstance{
		Credentials: `{"apiKey":"key","apiSecret":"secret"}`,
	})
	if err != nil {
		t.Fatalf("NewAlpacaConnector: %v", err)
	}
	return connector
}

func mustNormalizeAlpacaSubscriptions(
	t *testing.T, subs []fwmarketdata.Subscription,
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
	for i, typ := range conn.types {
		if typ != websocket.MessageText {
			t.Fatalf("write %d message type = %v, want text", i, typ)
		}
	}
	var auth struct {
		Action string `json:"action"`
		Key    string `json:"key"`
		Secret string `json:"secret"`
	}
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
	types     []websocket.MessageType
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

func (c *fakeAlpacaConn) Write(_ context.Context, typ websocket.MessageType, payload []byte) error {
	c.writes = append(c.writes, append([]byte(nil), payload...))
	c.types = append(c.types, typ)
	return nil
}

func (c *fakeAlpacaConn) Close(websocket.StatusCode, string) error {
	c.closeOnce.Do(func() {})
	return nil
}
