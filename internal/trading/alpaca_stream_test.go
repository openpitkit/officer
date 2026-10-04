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
// Please see https://officer.openpit.dev and the OWNERS file for details.

package trading

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

func acceptAlpaca(t *testing.T, w http.ResponseWriter, r *http.Request) *websocket.Conn {
	t.Helper()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		t.Error("websocket accept failed")
		return nil
	}
	return conn
}

func readAuth(t *testing.T, ctx context.Context, conn *websocket.Conn) bool {
	t.Helper()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Error("auth frame missing")
		return false
	}
	var auth map[string]string
	if json.Unmarshal(raw, &auth) != nil || len(auth) != 3 || auth["action"] != "auth" || auth["key"] != "test-key-marker" || auth["secret"] != "test-secret-marker" {
		t.Error("incorrect auth frame")
		return false
	}
	return true
}

func sendFrame(t *testing.T, ctx context.Context, conn *websocket.Conn, kind websocket.MessageType, raw string) bool {
	t.Helper()
	if conn.Write(ctx, kind, []byte(raw)) != nil {
		t.Error("server frame write failed")
		return false
	}
	return true
}

func completeHandshake(t *testing.T, ctx context.Context, conn *websocket.Conn) bool {
	t.Helper()
	if !sendFrame(t, ctx, conn, websocket.MessageBinary, `{"stream":"authorization","data":{"status":"authorized","action":"authenticate"}}`) {
		return false
	}
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Error("listen frame missing")
		return false
	}
	var listen struct {
		Action string `json:"action"`
		Data   struct {
			Streams []string `json:"streams"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &listen) != nil || listen.Action != "listen" || len(listen.Data.Streams) != 1 || listen.Data.Streams[0] != "trade_updates" {
		t.Error("incorrect listen frame")
		return false
	}
	return sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"listening","data":{"streams":["trade_updates"]}}`)
}

func nextEvent(t *testing.T, events <-chan fwtrading.Event) fwtrading.Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("stream channel closed early")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("stream event timeout")
	}
	return fwtrading.Event{}
}

func subscribeAlpaca(t *testing.T, c *alpacaConnector) <-chan fwtrading.Event {
	t.Helper()
	events, err := c.Subscribe(context.Background())
	if err != nil {
		t.Fatal("Subscribe failed")
	}
	return events
}

func assertClosed(t *testing.T, c *alpacaConnector, events <-chan fwtrading.Event) {
	t.Helper()
	c.Close()
	c.Close()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("Close did not end channel")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not close channel")
	}
}

func updateFrame(event, qty, cum, price string) string {
	return fmt.Sprintf(`{"stream":"trade_updates","data":{"event":%q,"execution_id":"execution","order":{"id":"venue","client_order_id":"client","qty":"3.000000000000000001","filled_qty":%q},"timestamp":"2026-09-01T10:00:00.123456789Z","qty":%q,"price":%q}}`, event, cum, qty, price)
}

func TestStreamEvents(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) || !completeHandshake(t, ctx, conn) {
			return
		}
		for _, raw := range []string{"{", "null", updateFrame("future_event", "1", "1", "1"), updateFrame("partial_fill", "1", "bad", "1"), `{"stream":"trade_updates","data":{"event":"fill","timestamp":"bad"}}`} {
			if !sendFrame(t, ctx, conn, websocket.MessageText, raw) {
				return
			}
		}
		if !sendFrame(t, ctx, conn, websocket.MessageBinary, updateFrame("partial_fill", "1.000000000000000001", "1.000000000000000001", "0.123456789123456789")) {
			return
		}
		if !sendFrame(t, ctx, conn, websocket.MessageText, updateFrame("fill", "2.000", "3.000000000000000002", "99999999999999.999999999")) {
			return
		}
		for _, event := range []string{"canceled", "expired", "rejected", "replaced"} {
			if !sendFrame(t, ctx, conn, websocket.MessageBinary, updateFrame(event, "", "", "")) {
				return
			}
		}
		<-release
	}))
	defer server.Close()
	defer close(release)
	c := testAlpacaConnector(t, server)
	events := subscribeAlpaca(t, c)
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("Connected missing after listen")
	}
	if event := nextEvent(t, events); event.Kind != fwtrading.EventOrder || event.Ref.VenueOrderID != "venue" {
		t.Fatal("invalid fill did not request reconciliation")
	}
	partial := nextEvent(t, events)
	if partial.Kind != fwtrading.EventFill || partial.Ref.ClientOrderID != "client" || partial.Ref.VenueOrderID != "venue" || partial.Fill.Quantity != "1.000000000000000001" || partial.Fill.Price != "0.123456789123456789" || partial.Fill.CumQuantity != "1.000000000000000001" || partial.Fill.LeavesQuantity != "2" || partial.Fill.Final || partial.Fill.At.Format(time.RFC3339Nano) != "2026-09-01T10:00:00.123456789Z" {
		t.Fatal("partial fill mapping lost exact fields")
	}
	final := nextEvent(t, events)
	if partial.Fill.Commission != nil || final.Fill.Commission != nil {
		t.Fatal("Alpaca stream attached a fee to a fill")
	}
	if final.Kind != fwtrading.EventFill || !final.Fill.Final || final.Fill.Quantity != "2.000" || final.Fill.Price != "99999999999999.999999999" || final.Fill.LeavesQuantity != "0" {
		t.Fatal("final fill leaves not floored at zero")
	}
	for range 4 {
		event := nextEvent(t, events)
		if event.Kind != fwtrading.EventOrder || event.Ref.VenueOrderID != "venue" || event.Ref.ClientOrderID != "client" {
			t.Fatal("terminal update did not request lookup")
		}
	}
	assertClosed(t, c, events)
}

func TestStreamReconnectBackoff(t *testing.T) {
	attempts := make(chan time.Time, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts <- time.Now()
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if readAuth(t, ctx, conn) {
			completeHandshake(t, ctx, conn)
		}
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	events := subscribeAlpaca(t, c)
	var previous time.Time
	for i := range 4 {
		if nextEvent(t, events).Kind != fwtrading.EventConnected {
			t.Fatal("listen acknowledgement did not connect")
		}
		var at time.Time
		select {
		case at = <-attempts:
		case <-time.After(time.Second):
			t.Fatal("dial timestamp missing")
		}
		if i > 0 {
			minimum := alpacaReconnectMin * time.Duration(1<<(i-1))
			if at.Sub(previous) < minimum-25*time.Millisecond {
				t.Fatalf("reconnect spacing did not grow: dial %d waited %v, want at least %v", i+1, at.Sub(previous), minimum)
			}
		}
		previous = at
	}
	assertClosed(t, c, events)
}

func TestStreamSlowConsumerReconnectBackoff(t *testing.T) {
	attempts := make(chan time.Time, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		at := time.Now()
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) || !completeHandshake(t, ctx, conn) {
			return
		}
		_ = conn.CloseNow()
		attempts <- at
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	events := subscribeAlpaca(t, c)
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("listen acknowledgement did not connect")
	}
	for range 2 {
		select {
		case <-attempts:
		case <-time.After(2 * time.Second):
			t.Fatal("server did not close after listen acknowledgement")
		}
	}
	<-time.After(alpacaReconnectMax + 50*time.Millisecond)
	releasedAt := time.Now()
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("blocked Connected missing")
	}
	select {
	case at := <-attempts:
		minimum := 2 * alpacaReconnectMin
		if elapsed := at.Sub(releasedAt); elapsed < minimum-25*time.Millisecond {
			t.Fatalf("slow consumer reset reconnect backoff: waited %v, want at least %v", elapsed, minimum)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect after releasing slow consumer missing")
	}
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("reconnect Connected missing")
	}
	assertClosed(t, c, events)
}

func TestStreamRejectsRedirect(t *testing.T) {
	var targetDials, authFrames atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetDials.Add(1)
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, raw, err := conn.Read(ctx)
		var frame struct {
			Action string `json:"action"`
		}
		if err == nil && json.Unmarshal(raw, &frame) == nil && frame.Action == "auth" {
			authFrames.Add(1)
		}
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stream", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	healthy, err := c.stream(ctx, make(chan fwtrading.Event, 1))
	if targetDials.Load() != 0 || authFrames.Load() != 0 {
		t.Fatal("redirect target received a dial or auth frame")
	}
	if err == nil || healthy {
		t.Fatal("redirect was not a failed connection")
	}
}

func TestStreamHealthyConnection(t *testing.T) {
	for _, condition := range []string{"trade update", "uptime"} {
		t.Run(condition, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn := acceptAlpaca(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				ctx, cancel := context.WithTimeout(context.Background(), 2*alpacaReconnectMax)
				defer cancel()
				if !readAuth(t, ctx, conn) || !completeHandshake(t, ctx, conn) {
					return
				}
				if condition == "trade update" {
					sendFrame(t, ctx, conn, websocket.MessageText, updateFrame("partial_fill", "1", "1", "1"))
				} else {
					timer := time.NewTimer(alpacaReconnectMax + 50*time.Millisecond)
					defer timer.Stop()
					<-timer.C
				}
			}))
			defer server.Close()
			c := testAlpacaConnector(t, server)
			ctx, cancel := context.WithTimeout(context.Background(), 2*alpacaReconnectMax)
			defer cancel()
			events := make(chan fwtrading.Event, 2)
			healthy, err := c.stream(ctx, events)
			if err == nil || !healthy {
				t.Fatal("healthy connection did not allow backoff reset")
			}
			if nextEvent(t, events).Kind != fwtrading.EventConnected {
				t.Fatal("Connected missing")
			}
			if condition == "trade update" && nextEvent(t, events).Kind != fwtrading.EventFill {
				t.Fatal("trade update missing")
			}
		})
	}
}

func TestStreamFailureLogs(t *testing.T) {
	var logs lockedLogBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) {
			return
		}
		if attempt == 1 {
			if !sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"authorization","data":{"status":[]}}`) {
				return
			}
		}
		if !completeHandshake(t, ctx, conn) {
			return
		}
		if attempt == 1 {
			for _, raw := range []string{"{", `{"stream":"trade_updates","data":[]}`, `{"stream":"trade_updates","data":{"event":42}}`, `{"stream":"trade_updates","data":{"event":"fill","order":{}}}`, updateFrame("fill", "bad", "1", "1"), `{"action":"error","data":{"error_message":"venue unavailable test-secret-marker test-key-marker"}}`} {
				if !sendFrame(t, ctx, conn, websocket.MessageText, raw) {
					return
				}
			}
			return
		}
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	events := subscribeAlpaca(t, c)
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("Connected missing")
	}
	if nextEvent(t, events).Kind != fwtrading.EventOrder {
		t.Fatal("invalid fill did not reconcile")
	}
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("reconnect missing")
	}
	assertClosed(t, c, events)
	text := logs.String()
	if !strings.Contains(text, "ignoring trade update with invalid header") || !strings.Contains(text, "ignoring trade update without ord ref") {
		t.Fatal("ignored updates were not logged")
	}
	if !strings.Contains(text, "level=WARN") || !strings.Contains(text, "venue unavailable") || !strings.Contains(text, "error=") {
		t.Fatal("stream failure lost warning level or venue cause")
	}
	if strings.Contains(text, "test-secret-marker") || strings.Contains(text, "test-key-marker") {
		t.Fatal("credentials leaked into stream log")
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if !strings.Contains(line, "connection=test-connection") {
			t.Fatal("stream log lacks connection external id")
		}
	}
}

func TestStreamBoundedDecimals(t *testing.T) {
	for _, field := range []string{"order.qty", "order.filled_qty", "qty", "price"} {
		for _, tc := range []struct{ name, value string }{
			{"exponent", "1e3"}, {"large exponent", "1e1000000000"},
			{"small exponent", "1e-2147483647"},
			{"overlong", strings.Repeat("1", alpacaDecimalMaxLength+1)},
			{"space", "1 "}, {"negative", "-1"}, {"malformed", "bad"},
		} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				var frame map[string]any
				if json.Unmarshal([]byte(updateFrame("fill", "1", "1", "1")), &frame) != nil {
					t.Fatal("invalid fixture")
				}
				data := frame["data"].(map[string]any)
				if strings.HasPrefix(field, "order.") {
					data["order"].(map[string]any)[strings.TrimPrefix(field, "order.")] = tc.value
				} else {
					data[field] = tc.value
				}
				raw, err := json.Marshal(frame)
				if err != nil {
					t.Fatal("cannot encode fixture")
				}
				assertStreamReconciles(t, string(raw))
			})
		}
	}
}

func TestStreamUnusableUpdateReconciles(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"fill timestamp", strings.ReplaceAll(updateFrame("fill", "1", "1", "1"), "2026-09-01T10:00:00.123456789Z", "bad")},
		{"terminal timestamp", `{"stream":"trade_updates","data":{"event":"canceled","order":{"id":"venue","client_order_id":"client"},"timestamp":"bad"}}`},
		{"wrong decimal type", `{"stream":"trade_updates","data":{"event":"fill","order":{"id":"venue","client_order_id":"client","qty":{}}}}`},
		{"missing timestamp", `{"stream":"trade_updates","data":{"event":"fill","order":{"id":"venue","client_order_id":"client","qty":"1","filled_qty":"1"},"qty":"1","price":"1"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) { assertStreamReconciles(t, tc.raw) })
	}
	for _, raw := range []string{`{}`, `{"event":"fill","timestamp":"bad"}`, `{"event":"canceled","order":{"id":"","client_order_id":""}}`} {
		if _, ok := (&alpacaConnector{}).parseAlpacaTradeUpdate(json.RawMessage(raw)); ok {
			t.Fatal("update without a usable ref was emitted")
		}
	}
}

func TestStreamTradeUpdateDiscriminatorAndReferences(t *testing.T) {
	type testCase struct {
		name, raw string
		want      fwtrading.OrderRef
	}
	cases := []testCase{
		{"unknown malformed timestamp", `{"event":"future_event","order":{"id":"venue","client_order_id":"client"},"timestamp":"bad"}`, fwtrading.OrderRef{}},
		{"unknown malformed decimals", `{"event":"future_event","order":{"id":"venue","client_order_id":"client","qty":{}},"qty":[],"price":false}`, fwtrading.OrderRef{}},
		{"unknown malformed order", `{"event":"future_event","order":7}`, fwtrading.OrderRef{}},
		{"malformed discriminator", `{"event":7,"order":{"id":"venue"},"timestamp":"bad"}`, fwtrading.OrderRef{}},
	}
	for _, event := range []string{"fill", "partial_fill", "canceled", "expired", "rejected", "replaced"} {
		for _, ref := range []struct {
			name, raw string
			want      fwtrading.OrderRef
		}{
			{"numeric venue id", `{"id":7,"client_order_id":"client"}`, fwtrading.OrderRef{ClientOrderID: "client"}},
			{"object client id", `{"id":"venue","client_order_id":{}}`, fwtrading.OrderRef{VenueOrderID: "venue"}},
			{"null venue id", `{"id":null,"client_order_id":"client"}`, fwtrading.OrderRef{ClientOrderID: "client"}},
			{"empty client id", `{"id":"venue","client_order_id":""}`, fwtrading.OrderRef{VenueOrderID: "venue"}},
			{"missing venue id", `{"client_order_id":"client"}`, fwtrading.OrderRef{ClientOrderID: "client"}},
			{"both ids unusable", `{"id":7,"client_order_id":{}}`, fwtrading.OrderRef{}},
		} {
			cases = append(cases, testCase{event + "/" + ref.name,
				fmt.Sprintf(`{"event":%q,"order":%s,"timestamp":"bad"}`, event, ref.raw), ref.want})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn := acceptAlpaca(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if !readAuth(t, ctx, conn) || !completeHandshake(t, ctx, conn) {
					return
				}
				if !sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"trade_updates","data":`+tc.raw+`}`) {
					return
				}
				if !sendFrame(t, ctx, conn, websocket.MessageBinary, updateFrame("partial_fill", "1", "1", "1")) {
					return
				}
				_, _, _ = conn.Read(ctx)
			}))
			defer server.Close()
			c := testAlpacaConnector(t, server)
			events := subscribeAlpaca(t, c)
			if nextEvent(t, events).Kind != fwtrading.EventConnected {
				t.Fatal("Connected missing")
			}
			event := nextEvent(t, events)
			if tc.want != (fwtrading.OrderRef{}) {
				if event.Kind != fwtrading.EventOrder || event.Ref != tc.want {
					t.Fatal("supported update did not reconcile with the usable order ref")
				}
				event = nextEvent(t, events)
			}
			if event.Kind != fwtrading.EventFill {
				t.Fatal("ignored update was emitted before the valid fill")
			}
			assertClosed(t, c, events)
		})
	}
}

func assertStreamReconciles(t *testing.T, raw string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) || !completeHandshake(t, ctx, conn) {
			return
		}
		if !sendFrame(t, ctx, conn, websocket.MessageText, raw) {
			return
		}
		if !sendFrame(t, ctx, conn, websocket.MessageBinary, updateFrame("partial_fill", "1", "1", "1")) {
			return
		}
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	events := subscribeAlpaca(t, c)
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("Connected missing")
	}
	event := nextEvent(t, events)
	if event.Kind != fwtrading.EventOrder || event.Ref.ClientOrderID != "client" || event.Ref.VenueOrderID != "venue" {
		t.Fatal("unusable update with known ref did not request reconciliation")
	}
	if nextEvent(t, events).Kind != fwtrading.EventFill {
		t.Fatal("valid update after invalid payload was lost")
	}
	assertClosed(t, c, events)
}

func TestStreamReconnect(t *testing.T) {
	for _, failure := range []string{"close", "server error", "handshake server error"} {
		t.Run(failure, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := attempts.Add(1)
				conn := acceptAlpaca(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if !readAuth(t, ctx, conn) {
					return
				}
				if attempt == 1 && failure == "handshake server error" {
					sendFrame(t, ctx, conn, websocket.MessageBinary, `{"action":"error","data":{"error_message":"test-secret-marker"}}`)
					return
				}
				if !completeHandshake(t, ctx, conn) {
					return
				}
				if attempt == 1 {
					if failure == "server error" {
						sendFrame(t, ctx, conn, websocket.MessageText, `{"action":"error","data":{"error_message":"test-secret-marker"}}`)
					}
					return
				}
				_, _, _ = conn.Read(ctx)
			}))
			defer server.Close()
			c := testAlpacaConnector(t, server)
			events := subscribeAlpaca(t, c)
			if nextEvent(t, events).Kind != fwtrading.EventConnected {
				t.Fatal("Connected missing")
			}
			if failure != "handshake server error" && nextEvent(t, events).Kind != fwtrading.EventConnected {
				t.Fatal("Connected missing after reconnect")
			}
			if attempts.Load() < 2 {
				t.Fatal("stream failed to reconnect")
			}
			assertClosed(t, c, events)
		})
	}
}

type lockedLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *lockedLogBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }

func TestStreamUnauthorized(t *testing.T) {
	var logs lockedLogBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	attempts := make(chan time.Time, 4)
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := count.Add(1)
		attempts <- time.Now()
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) {
			return
		}
		if attempt == 1 {
			sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"authorization","data":{"status":"unauthorized","action":"authenticate","extra":"test-secret-marker"}}`)
			return
		}
		if !completeHandshake(t, ctx, conn) {
			return
		}
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	events := subscribeAlpaca(t, c)
	var first, second time.Time
	select {
	case first = <-attempts:
	case <-time.After(time.Second):
		t.Fatal("initial connection missing")
	}
	select {
	case <-events:
		t.Fatal("unauthorized stream reported Connected")
	case <-time.After(alpacaReconnectMax / 2):
	}
	select {
	case second = <-attempts:
	case <-time.After(alpacaReconnectMax):
		t.Fatal("unauthorized stream not retried")
	}
	if second.Sub(first) < alpacaReconnectMax-50*time.Millisecond {
		t.Fatal("unauthorized retry did not use max backoff")
	}
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("authorized retry did not connect")
	}
	assertClosed(t, c, events)
	text := logs.String()
	if !strings.Contains(text, "level=ERROR") || !strings.Contains(text, "authorization refused") || !strings.Contains(text, "connection=test-connection") {
		t.Fatal("unauthorized was not logged at error level")
	}
	if strings.Contains(text, "test-secret-marker") || strings.Contains(text, "test-key-marker") {
		t.Fatal("credentials leaked into stream log")
	}
}

func TestStreamWaitsForListenAck(t *testing.T) {
	listening := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) {
			return
		}
		if !sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"authorization","data":{"status":"authorized"}}`) {
			return
		}
		if _, _, err := conn.Read(ctx); err != nil {
			t.Error("listen missing")
			return
		}
		sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"listening","data":{"streams":[]}}`)
		close(listening)
		<-release
		sendFrame(t, ctx, conn, websocket.MessageText, `{"stream":"listening","data":{"streams":["trade_updates"]}}`)
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	events := subscribeAlpaca(t, c)
	select {
	case <-listening:
	case <-time.After(time.Second):
		t.Fatal("listen request missing")
	}
	select {
	case <-events:
		t.Fatal("Connected before trade_updates listen ack")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("listen ack did not connect")
	}
	assertClosed(t, c, events)
}

func TestStreamSilenceAndCancellation(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		conn := acceptAlpaca(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), alpacaHandshakeTimeout+3*time.Second)
		defer cancel()
		if !readAuth(t, ctx, conn) || !completeHandshake(t, ctx, conn) {
			return
		}
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatal("Subscribe failed")
	}
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("Connected missing")
	}
	select {
	case <-events:
		t.Fatal("silent stream reconnected or ended")
	case <-time.After(alpacaHandshakeTimeout + 100*time.Millisecond):
	}
	if attempts.Load() != 1 {
		t.Fatal("silent stream redialed")
	}
	cancel()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("cancellation produced an event")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close stream")
	}
	c.Close()
}

func TestStreamLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	events := subscribeAlpaca(t, c)
	if _, err := c.Subscribe(context.Background()); err == nil {
		t.Fatal("second subscription accepted")
	}
	assertClosed(t, c, events)
	if _, err := c.Subscribe(context.Background()); err == nil {
		t.Fatal("subscription after Close accepted")
	}
	c2 := testAlpacaConnector(t, server)
	c2.Close()
	c2.Close()
	if _, err := c2.Subscribe(context.Background()); err == nil {
		t.Fatal("Close before Subscribe ignored")
	}
}

func FuzzAlpacaTradeUpdate(f *testing.F) {
	for _, value := range []string{"1", "0", "-1", "1e1000000000", "1e-2147483647", strings.Repeat("1", alpacaDecimalMaxLength+1)} {
		var frame alpacaFrame
		_ = json.Unmarshal([]byte(updateFrame("fill", value, value, value)), &frame)
		f.Add(string(frame.Data))
	}
	for _, seed := range []string{`{}`, `{"event":"future_event"}`,
		`{"event":"fill","order":{"qty":"1.000000000000000001","filled_qty":"1.000000000000000002"},"qty":"1","price":"1"}`,
		`{"event":"partial_fill","order":{"qty":"10","filled_qty":"1"},"qty":"1","price":"1"}`,
		`{"event":"fill","order":{"qty":"bad","filled_qty":"1"}}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		c := &alpacaConnector{}
		event, ok := c.parseAlpacaTradeUpdate(json.RawMessage(raw))
		if !ok || event.Kind != fwtrading.EventFill {
			return
		}
		leaves, err := decimal.NewFromString(event.Fill.LeavesQuantity)
		if err != nil || leaves.IsNegative() {
			t.Fatal("fill leaves must be an exact non-negative decimal")
		}
		for _, value := range []string{event.Fill.Quantity, event.Fill.Price, event.Fill.CumQuantity} {
			if len(value) > alpacaDecimalMaxLength {
				t.Fatal("unbounded venue decimal reached a fill")
			}
			if _, err := domain.ParseOpenQuantity(value); err != nil {
				t.Fatal("non-plain venue decimal reached a fill")
			}
		}
	})
}
