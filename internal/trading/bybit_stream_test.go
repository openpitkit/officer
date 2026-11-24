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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/bybit"
)

func bybitReadRequest(t *testing.T, conn *websocket.Conn, op string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Error("missing WebSocket request")
		return false
	}
	var request struct {
		Op   string            `json:"op"`
		Args []json.RawMessage `json:"args"`
	}
	if json.Unmarshal(raw, &request) != nil || request.Op != op {
		t.Error("incorrect WebSocket operation")
		return false
	}
	if op == "auth" {
		var key, signature string
		var expires int64
		if len(request.Args) != 3 || json.Unmarshal(request.Args[0], &key) != nil || key == "" || json.Unmarshal(request.Args[1], &expires) != nil || expires <= time.Now().UnixMilli() || json.Unmarshal(request.Args[2], &signature) != nil || signature == "" {
			t.Error("invalid signed authentication")
			return false
		}
	} else {
		want := []json.RawMessage{json.RawMessage(`"order.spot"`), json.RawMessage(`"execution.spot"`)}
		if !reflect.DeepEqual(request.Args, want) {
			t.Error("incorrect private topics")
			return false
		}
	}
	return true
}

func bybitAccept(t *testing.T, w http.ResponseWriter, r *http.Request) *websocket.Conn {
	t.Helper()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		t.Error(err)
		return nil
	}
	if !bybitReadRequest(t, conn, "auth") {
		_ = conn.CloseNow()
		return nil
	}
	return conn
}

func bybitConfirm(t *testing.T, conn *websocket.Conn) bool {
	t.Helper()
	binanceWriteFrame(t, conn, map[string]any{"op": "auth", "success": true})
	if !bybitReadRequest(t, conn, "subscribe") {
		return false
	}
	binanceWriteFrame(t, conn, map[string]any{"op": "subscribe", "success": true})
	return true
}

func TestBybitStreamHandshake(t *testing.T) {
	for _, op := range []string{"auth", "subscribe"} {
		t.Run(op, func(t *testing.T) {
			c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
				conn := bybitAccept(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				if op == "subscribe" {
					binanceWriteFrame(t, conn, map[string]any{"op": "auth", "success": true})
					if !bybitReadRequest(t, conn, "subscribe") {
						return
					}
				}
				binanceWriteFrame(t, conn, map[string]any{"op": op, "success": false, "ret_msg": "refused"})
			})
			out := make(chan fwtrading.Event, 10)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := c.stream(ctx, out)
			if err == nil || errors.Is(err, errBybitUnauthorized) != (op == "auth") {
				t.Fatalf("wrong handshake error: %v", err)
			}
			if len(out) != 0 {
				t.Fatal("failed subscription announced a connection")
			}
			if op == "auth" {
				statuses := make(chan string, 1)
				c.SetStatusReporter(func(_ bool, text string) { statuses <- text })
				if _, err := c.Subscribe(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case text := <-statuses:
					if text != "bybit: stream authorization refused" {
						t.Fatalf("wrong authorization status: %q", text)
					}
				case <-ctx.Done():
					t.Fatal("authorization failure status missing")
				}
				c.Close()
			}
		})
	}
	t.Run("wait-for-subscribe", func(t *testing.T) {
		ready, release := make(chan struct{}), make(chan struct{})
		c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
			conn := bybitAccept(t, w, r)
			if conn == nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			binanceWriteFrame(t, conn, map[string]any{"op": "auth", "success": true})
			if !bybitReadRequest(t, conn, "subscribe") {
				return
			}
			close(ready)
			<-release
			binanceWriteFrame(t, conn, map[string]any{"op": "subscribe", "success": true})
			binanceWaitForClose(conn)
		})
		events, err := c.Subscribe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			select {
			case <-release:
			default:
				close(release)
			}
		}()
		select {
		case <-ready:
		case <-time.After(3 * time.Second):
			t.Fatal("subscription request missing")
		}
		select {
		case <-events:
			t.Fatal("connected before subscribe reply")
		case <-time.After(100 * time.Millisecond):
		}
		close(release)
		if e := binanceNextEvent(t, events); e.Kind != fwtrading.EventConnected {
			t.Fatal("connection event missing")
		}
		c.Close()
	})
	t.Run("reconnect", func(t *testing.T) {
		var attempts atomic.Int32
		c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
			conn := bybitAccept(t, w, r)
			if conn == nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			if !bybitConfirm(t, conn) {
				return
			}
			if attempts.Add(1) == 1 {
				return
			}
			binanceWaitForClose(conn)
		})
		events, err := c.Subscribe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if binanceNextEvent(t, events).Kind != fwtrading.EventConnected {
				t.Fatal("reconnection event missing")
			}
		}
		c.Close()
		if _, open := <-events; open {
			t.Fatal("stream not closed")
		}
	})
}

func TestBybitStreamEvents(t *testing.T) {
	exec := bybitTestExecution()
	bad := exec
	bad.ExecQty = "unreadable"
	fees := exec
	fees.ExtraFees = json.RawMessage(`[{"fee":"1"}]`)
	foreign := exec
	foreign.OrderLinkID = ""
	order := func(status string) map[string]any {
		return map[string]any{"orderLinkId": "client-1", "orderId": "venue-1", "symbol": "BTCUSDT", "orderStatus": status}
	}
	c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
		conn := bybitAccept(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if !bybitConfirm(t, conn) {
			return
		}
		binanceWriteFrame(t, conn, map[string]any{"op": "ping", "success": true, "ret_msg": "pong"})
		binanceWriteFrame(t, conn, map[string]any{"topic": "order.spot", "data": []any{order("New"), order("PartiallyFilled"), order("Filled"), order("Cancelled"), order("Rejected"), order("PartiallyFilledCanceled"), order("FutureStatus")}})
		binanceWriteFrame(t, conn, map[string]any{"topic": "execution.spot", "data": []any{foreign, exec, bad, fees, exec}})
		binanceWaitForClose(conn)
	})
	c.cacheSymbol(bybitTestRules().Instrument)
	diags := make(chan health.Diagnostic, 10)
	c.SetDiagnosticReporter(func(d health.Diagnostic) { diags <- d })
	events, err := c.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantFill, err := exec.fill("BTC", "USDT")
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []fwtrading.EventKind{fwtrading.EventConnected, fwtrading.EventOrder, fwtrading.EventOrder, fwtrading.EventOrder, fwtrading.EventOrder, fwtrading.EventFill, fwtrading.EventOrder, fwtrading.EventOrder, fwtrading.EventFill}
	for _, kind := range wantKinds {
		event := binanceNextEvent(t, events)
		if event.Kind != kind {
			t.Fatalf("kind=%v want=%v", event.Kind, kind)
		}
		if kind == fwtrading.EventConnected {
			continue
		}
		if event.Ref != (fwtrading.OrderRef{ClientOrderID: "client-1", VenueOrderID: "venue-1", Symbol: "BTCUSDT"}) {
			t.Fatalf("ref=%+v", event.Ref)
		}
		if kind == fwtrading.EventFill && !reflect.DeepEqual(event.Fill, wantFill.fill) {
			t.Fatalf("fill=%+v want=%+v", event.Fill, wantFill.fill)
		}
	}
	for _, detail := range []string{bybitFillUnreadable, bybitFillExtraFees} {
		select {
		case d := <-diags:
			if !reflect.DeepEqual(d, *venueStreamUnparsable(health.DiagWarn, detail, venueStreamReconciled)) {
				t.Fatalf("diagnostic=%+v", d)
			}
		case <-time.After(time.Second):
			t.Fatal("fill diagnostic missing")
		}
	}
	select {
	case e := <-events:
		t.Fatalf("unexpected event %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
	c.Close()
	if len(diags) != 0 {
		t.Fatal("foreign order produced a diagnostic")
	}
}

func TestBybitStreamDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, raw, detail string
		deliver           bool
	}{
		{"missing-client", `{"orderId":"venue-1"}`, bybitRefUnreadable, false},
		{"typed-client", `{"orderLinkId":42}`, bybitRefUnreadable, false},
		{"null-client", `{"orderLinkId":null}`, bybitRefUnreadable, false},
		{"venue", `{"orderLinkId":"client-1","orderId":42,"symbol":"BTCUSDT"}`, bybitVenueIDUnusable, true},
		{"symbol", `{"orderLinkId":"client-1","orderId":"venue-1","symbol":42}`, bybitSymbolUnreadable, true},
		{"fill", `{"orderLinkId":"client-1","orderId":"venue-1","symbol":"BTCUSDT","execQty":42}`, bybitFillUnreadable, true},
		{"coins", `{"orderLinkId":"client-1","orderId":"venue-1","symbol":"MISSING"}`, bybitFillAssets, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
				writeBybitResult(t, w, map[string]any{"list": []bybit.InstrumentRules{}})
			})
			c.cacheSymbol(bybitTestRules().Instrument)
			event, ok, d := c.parseBybitUpdate(context.Background(), "execution.spot", json.RawMessage(tc.raw))
			if ok != tc.deliver || d == nil || d.Detail != tc.detail {
				t.Fatalf("ok=%v diagnostic=%+v", ok, d)
			}
			if ok && (event.Kind != fwtrading.EventOrder || event.Ref.ClientOrderID != "client-1" || d.Remediation != venueStreamReconciled) {
				t.Fatalf("reconciliation missing: %+v %+v", event, d)
			}
		})
	}
	t.Run("frames-once", func(t *testing.T) {
		c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
			conn := bybitAccept(t, w, r)
			if conn == nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			if !bybitConfirm(t, conn) {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			for _, raw := range []string{
				`{`, `{}`, `{`,
			} {
				if err := conn.Write(ctx, websocket.MessageText, []byte(raw)); err != nil {
					t.Error(err)
					return
				}
			}
			binanceWriteFrame(t, conn, map[string]any{"topic": "order.spot", "data": []any{map[string]any{"orderLinkId": "client-1", "orderId": "venue-1", "symbol": "BTCUSDT", "orderStatus": "Cancelled"}}})
			binanceWaitForClose(conn)
		})
		diags := make(chan health.Diagnostic, 10)
		c.SetDiagnosticReporter(func(d health.Diagnostic) { diags <- d })
		events, err := c.Subscribe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if binanceNextEvent(t, events).Kind != fwtrading.EventConnected || binanceNextEvent(t, events).Kind != fwtrading.EventOrder {
			t.Fatal("bad frame prevented subsequent order")
		}
		c.Close()
		if len(diags) != 1 {
			t.Fatalf("diagnostic count=%d want=1", len(diags))
		}
		if d := <-diags; d.Detail != bybitFrameUnreadable {
			t.Fatalf("diagnostic=%+v", d)
		}
	})
	for _, tc := range []struct {
		name, raw string
	}{
		{"data-object", `{"topic":"order.spot","data":{}}`},
		{"data-number", `{"topic":"execution.spot","data":42}`},
		{"data-null", `{"topic":"order.spot","data":null}`},
		{"data-absent", `{"topic":"execution.spot"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
				conn := bybitAccept(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				if !bybitConfirm(t, conn) {
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if err := conn.Write(ctx, websocket.MessageText, []byte(tc.raw)); err != nil {
					t.Error(err)
					return
				}
				binanceWriteFrame(t, conn, map[string]any{"topic": "order.spot", "data": []any{map[string]any{"orderLinkId": "client-1", "orderId": "venue-1", "symbol": "BTCUSDT", "orderStatus": "Cancelled"}}})
				binanceWaitForClose(conn)
			})
			diags := make(chan health.Diagnostic, 10)
			c.SetDiagnosticReporter(func(d health.Diagnostic) { diags <- d })
			events, err := c.Subscribe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if binanceNextEvent(t, events).Kind != fwtrading.EventConnected || binanceNextEvent(t, events).Kind != fwtrading.EventOrder {
				t.Fatal("bad frame prevented subsequent order")
			}
			c.Close()
			if len(diags) != 1 {
				t.Fatalf("diagnostic count=%d want=1", len(diags))
			}
			if d := <-diags; d.Level != health.DiagError ||
				d.Detail != bybitTradeUnreadable || d.Remediation != venueStreamDropped {
				t.Fatalf("dropped trade diagnostic=%+v", d)
			}
		})
	}
}

func TestBybitStreamPing(t *testing.T) {
	ping := make(chan bool, 1)
	c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
		conn := bybitAccept(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if !bybitConfirm(t, conn) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 23*time.Second)
		defer cancel()
		kind, raw, err := conn.Read(ctx)
		ping <- err == nil && kind == websocket.MessageText && string(raw) == `{"op":"ping"}`
		if err == nil {
			binanceWaitForClose(conn)
		}
	})
	events, err := c.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if binanceNextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("not connected")
	}
	select {
	case ok := <-ping:
		if !ok {
			t.Fatal("missing text ping")
		}
	case <-time.After(24 * time.Second):
		t.Fatal("ping timeout")
	}
	c.Close()
}

type bybitErrorTransport struct{ message string }

func (tr bybitErrorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New(tr.message)
}

func FuzzBybitStreamUpdate(f *testing.F) {
	for _, raw := range []string{
		`null`, `{}`, `[]`, `{"orderLinkId":""}`,
		`{"orderLinkId":"client-1","orderId":"venue-1","symbol":"BTCUSDT","orderStatus":"Cancelled"}`,
	} {
		f.Add(raw, true)
		f.Add(raw, false)
	}
	encoded, err := json.Marshal(bybitTestExecution())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(string(encoded), false)
	c, err := newBybitConnector(domain.TradingConnection{
		Credentials: binanceTestCredentials,
	}, "http://unused", "ws://unused")
	if err != nil {
		f.Fatal(err)
	}
	c.cacheSymbol(bybitTestRules().Instrument)
	c.httpClient = &http.Client{Transport: bybitErrorTransport{"unknown symbol"}}
	f.Fuzz(func(t *testing.T, raw string, order bool) {
		topic := "execution.spot"
		if order {
			topic = "order.spot"
		}
		event, emitted, diag := c.parseBybitUpdate(context.Background(), topic,
			json.RawMessage(raw))
		if emitted && event.Ref.ClientOrderID == "" {
			t.Fatal("emitted an event without Officer identity")
		}
		if emitted && diag != nil && event.Kind != fwtrading.EventOrder {
			t.Fatal("unreadable Officer update bypassed reconciliation")
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &fields) == nil &&
			string(fields["orderLinkId"]) == `""` && (emitted || diag != nil) {
			t.Fatal("foreign order produced an event or diagnostic")
		}
	})
}

func TestBybitRedaction(t *testing.T) {
	logs := captureStreamLogs(t)
	const key = `visible-key"\&`
	const secret = key + "-visible-secret"
	encoded, _ := json.Marshal(secret)
	variants := []string{key, secret, string(encoded[1 : len(encoded)-1]), url.QueryEscape(secret), url.PathEscape(secret)}
	message := strings.Join(variants, " ")
	credentials, _ := json.Marshal(map[string]string{"apiKey": key, "apiSecret": secret})
	assertSafe := func(t *testing.T, text string) {
		t.Helper()
		for _, v := range variants {
			if strings.Contains(text, v) {
				t.Fatal("credential appeared in error, diagnostic or log")
			}
		}
	}
	for _, mode := range []string{"rest", "transport", "auth", "subscribe", "read", "dial"} {
		t.Run(mode, func(t *testing.T) {
			c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/stream" {
					writeBinanceJSON(t, w, map[string]any{"retCode": 10003, "retMsg": message})
					return
				}
				conn := bybitAccept(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				if mode == "read" {
					_ = conn.Close(websocket.StatusPolicyViolation, key+" "+secret)
					return
				}
				op := "auth"
				if mode == "subscribe" {
					binanceWriteFrame(t, conn, map[string]any{"op": "auth", "success": true})
					if !bybitReadRequest(t, conn, "subscribe") {
						return
					}
					op = "subscribe"
				}
				binanceWriteFrame(t, conn, map[string]any{"op": op, "success": false, "ret_msg": message})
			})
			var err error
			c, err = newBybitConnector(domain.TradingConnection{
				Credentials: string(credentials),
			}, c.restURL, c.streamURL)
			if err != nil {
				t.Fatal("credential setup failed")
			}
			defer c.Close()
			messages := make(chan string, 20)
			c.SetStatusReporter(func(_ bool, s string) { messages <- s })
			c.SetDiagnosticReporter(func(d health.Diagnostic) { messages <- fmt.Sprint(d) })
			if mode == "transport" {
				c.httpClient = &http.Client{Transport: bybitErrorTransport{message}}
				c.client = bybit.NewClient(c.restURL, c.credentials, c.redact, c.httpClient)
			}
			if mode == "rest" || mode == "transport" {
				_, err := c.Send(context.Background(), bybitTestOrder())
				if err == nil {
					t.Fatal("send unexpectedly succeeded")
				}
				assertSafe(t, err.Error())
				_, err = c.CheckAccount(context.Background())
				if err == nil {
					t.Fatal("account unexpectedly succeeded")
				}
				assertSafe(t, err.Error())
				_, _, err = c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "client-1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy})
				if err == nil {
					t.Fatal("lookup unexpectedly succeeded")
				}
				assertSafe(t, err.Error())
				_, err = c.VerifySymbol(context.Background(), "BTCUSDT")
				if err == nil {
					t.Fatal("symbol unexpectedly succeeded")
				}
				assertSafe(t, err.Error())
				_, err = c.SearchSymbols(context.Background(), "BTC")
				if err == nil {
					t.Fatal("catalogue unexpectedly succeeded")
				}
				assertSafe(t, err.Error())
				return
			}
			if mode == "dial" {
				c.streamURL = "http://%" + message
			}
			if _, err = c.Subscribe(context.Background()); err != nil {
				t.Fatal("subscribe setup failed")
			}
			select {
			case text := <-messages:
				assertSafe(t, text)
				if mode == "dial" && (!strings.HasPrefix(text, "bybit: dial stream: ") ||
					strings.Count(text, "bybit:") != 1) {
					t.Fatal("dial failure must have exactly one vendor prefix")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("failure status missing")
			}
			c.Close()
			close(messages)
			for text := range messages {
				assertSafe(t, text)
			}
		})
	}
	assertSafe(t, logs.String())
}
