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
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/okx"
	"go.openpit.dev/officer/internal/polltest"
)

func okxWSWrite(t *testing.T, ctx context.Context, conn *websocket.Conn, frame any) bool {
	t.Helper()
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Error("encode WS fixture")
		return false
	}
	return conn.Write(ctx, websocket.MessageText, raw) == nil
}

func okxWSHandshake(
	t *testing.T, ctx context.Context, w http.ResponseWriter, r *http.Request,
) *websocket.Conn {
	t.Helper()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		t.Error("accept OKX WS")
		return nil
	}
	_, raw, err := conn.Read(ctx)
	if err != nil {
		if t.Context().Err() == nil {
			t.Error("OKX login request")
		}
		_ = conn.CloseNow()
		return nil
	}
	var login struct {
		Op   string
		Args []map[string]string
	}
	if json.Unmarshal(raw, &login) != nil || login.Op != "login" ||
		len(login.Args) != 1 || login.Args[0]["apiKey"] == "" ||
		login.Args[0]["passphrase"] == "" || login.Args[0]["sign"] == "" {
		t.Error("OKX login request")
		_ = conn.CloseNow()
		return nil
	}
	if !okxWSWrite(t, ctx, conn, map[string]string{"event": "login", "code": "0"}) {
		_ = conn.CloseNow()
		return nil
	}
	_, raw, err = conn.Read(ctx)
	if err != nil {
		if t.Context().Err() == nil {
			t.Error("OKX orders subscription")
		}
		_ = conn.CloseNow()
		return nil
	}
	if string(raw) != `{"op":"subscribe","args":[{"channel":"orders","instType":"SPOT"}]}` {
		t.Error("OKX orders subscription")
		_ = conn.CloseNow()
		return nil
	}
	if !okxWSWrite(t, ctx, conn, map[string]any{
		"event": "subscribe", "arg": map[string]string{"channel": "orders", "instType": "SPOT"},
	}) {
		_ = conn.CloseNow()
		return nil
	}
	return conn
}

func okxWSRow(state, amount, cum, tradeID string) map[string]string {
	return map[string]string{
		"clOrdId": okxTestHex, "ordId": "123", "instId": "BTC-USDT", "state": state,
		"tradeId": tradeID, "fillSz": amount, "fillPx": "2", "accFillSz": cum, "sz": "10",
		"fillTime": "1700000000000", "fillFee": "-0.01", "fillFeeCcy": "BTC"}
}

func okxWSData(rows ...map[string]string) map[string]any {
	return map[string]any{
		"arg": map[string]string{"channel": "orders", "instType": "SPOT"}, "data": rows,
	}
}

func TestOKXStreamEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/public/instruments" {
			okxReply(w, []okx.Instrument{okxTestInstrument()})
			return
		}
		conn := okxWSHandshake(t, ctx, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		_ = conn.Write(ctx, websocket.MessageText, []byte("pong"))
		foreign := okxWSRow("filled", "10", "10", "1")
		foreign["clOrdId"] = "external"
		empty := okxWSRow("filled", "10", "10", "1")
		empty["clOrdId"] = ""
		okxWSWrite(t, ctx, conn, okxWSData(foreign, empty, okxWSRow("live", "0", "0", ""),
			okxWSRow("partially_filled", "3", "3", "1"),
			okxWSRow("filled", "7", "10", "2"), okxWSRow("canceled", "0", "0", "")))
		_, _, _ = conn.Read(ctx)
	})
	c.symbols["BTC-USDT"] = okxTestInstrument()
	events, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatal("subscribe")
	}
	if nextEvent(t, events).Kind != fwtrading.EventConnected {
		t.Fatal("handshake must precede connected")
	}
	for i := 0; i < 2; i++ {
		event := nextEvent(t, events)
		if event.Kind != fwtrading.EventFill || event.Ref.ClientOrderID != okxTestCOID ||
			event.Ref.VenueOrderID != "123" || event.Ref.Symbol != "BTC-USDT" ||
			event.Fill.Final != (i == 1) || event.Fill.At.UnixMilli() != 1700000000000 ||
			event.Fill.Commission == nil || event.Fill.Commission.Amount != "0.01" ||
			event.Fill.CommissionLeg != fwtrading.CommissionLegBase {
			t.Fatal("stream fill mapping")
		}
		if (i == 0 && (event.Fill.Quantity != "3" || event.Fill.CumQuantity != "3" ||
			event.Fill.LeavesQuantity != "7")) ||
			(i == 1 && (event.Fill.Quantity != "7" || event.Fill.CumQuantity != "10" ||
				event.Fill.LeavesQuantity != "0")) {
			t.Fatal("stream fill quantities")
		}
	}
	if e := nextEvent(t, events); e.Kind != fwtrading.EventOrder ||
		e.Ref.ClientOrderID != okxTestCOID {
		t.Fatal("venue cancel must reconcile")
	}
}

func TestOKXStreamAuthorizationAndRedaction(t *testing.T) {
	for _, code := range []string{
		"60004", "60005", "60006", "60007", "60009", "60024", "60032", "63999",
	} {
		t.Run(code, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			signatures := make(chan string, 1)
			c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				_, raw, err := conn.Read(ctx)
				if err != nil {
					return
				}
				var login struct{ Args []struct{ Sign string } }
				if json.Unmarshal(raw, &login) == nil && len(login.Args) == 1 {
					signatures <- login.Args[0].Sign
				}
				okxWSWrite(t, ctx, conn, map[string]string{
					"event": "error", "code": code,
					"msg": "Login failed okx-key-marker okx-secret-marker " +
						"okx-pass-marker www.okx.com",
				})
				_, _, _ = conn.Read(ctx)
			})
			_, err := c.stream(ctx, make(chan fwtrading.Event, 1))
			if err == nil || errors.Is(err, errOKXUnauthorized) != (code != "63999") {
				t.Fatal("authorization classification")
			}
			signature := <-signatures
			for _, secret := range []string{
				"okx-key-marker", "okx-secret-marker", "okx-pass-marker", "www.okx.com", signature,
			} {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Fatal("login error leaked credential or signature")
				}
			}
		})
	}
}

func TestOKXStreamEchoedRequestsStayPrivate(t *testing.T) {
	for _, tc := range []struct{ stage, code string }{
		{"login", "60012"}, {"login", "60009"},
		{"subscribe error", "60018"}, {"subscribe reply", "60012"},
		{"stream", "60018"},
		{"subscribe error", "60009"}, {"stream", "60009"},
		{"login", "venue-code-marker"}, {"subscribe reply", "venue-code-marker"},
		{"stream", "venue-code-marker"},
		{"login", "123456789"}, {"subscribe reply", "123456789"},
		{"stream", "123456789"},
		{"login", ""}, {"subscribe error", ""}, {"stream", ""},
		{"login", "12345678"}, {"subscribe reply", "12345678"}, {"stream", "12345678"},
	} {
		for _, lifecycle := range []bool{false, true} {
			t.Run(tc.stage+"/"+tc.code+"/"+strconv.FormatBool(lifecycle), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				t.Cleanup(cancel)
				signatures := make(chan string, 1)
				c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/v5/public/instruments" {
						okxReply(w, []okx.Instrument{okxTestInstrument()})
						return
					}
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						return
					}
					defer func() { _ = conn.CloseNow() }()
					_, login, err := conn.Read(ctx)
					if err != nil {
						if t.Context().Err() == nil {
							t.Error("missing captured login")
						}
						return
					}
					var frame struct{ Args []struct{ Sign string } }
					if json.Unmarshal(login, &frame) != nil ||
						len(frame.Args) != 1 || frame.Args[0].Sign == "" {
						t.Error("missing captured login")
						return
					}
					signatures <- frame.Args[0].Sign
					failure := map[string]any{
						"event": "error", "code": tc.code, "msg": string(login),
					}
					if tc.stage != "login" {
						if !okxWSWrite(t, ctx, conn,
							map[string]string{"event": "login", "code": "0"}) {
							return
						}
						if _, _, err := conn.Read(ctx); err != nil {
							return
						}
					}
					if tc.stage == "subscribe reply" {
						failure["event"] = "subscribe"
						failure["arg"] = map[string]string{
							"channel": "orders", "instType": "SPOT",
						}
					}
					if tc.stage == "stream" {
						okxWSWrite(t, ctx, conn, map[string]any{
							"event": "subscribe",
							"arg":   map[string]string{"channel": "orders", "instType": "SPOT"},
						})
					}
					okxWSWrite(t, ctx, conn, failure)
					_, _, _ = conn.Read(ctx)
				})
				var message string
				if lifecycle {
					statuses := make(chan string, 2)
					c.SetStatusReporter(func(connected bool, text string) {
						if !connected {
							statuses <- text
						}
					})
					events, err := c.Subscribe(ctx)
					if err != nil {
						t.Fatal("subscribe failed")
					}
					go func() {
						for range events {
						}
					}()
					message = <-statuses // Stream publishes the failure status.
				} else {
					_, err := c.stream(ctx, make(chan fwtrading.Event, 1))
					if err == nil || errors.Is(err, errOKXUnauthorized) != okxAuthCode(tc.code) {
						t.Fatal("stream refusal classification changed")
					}
					message = err.Error()
				}
				var signature string
				select {
				case signature = <-signatures:
				default:
					t.Fatal("missing login signature capture")
				}
				operation := "error"
				switch tc.stage {
				case "login":
					operation = "login refused"
				case "subscribe error", "subscribe reply":
					operation = "subscription refused"
				}
				if !strings.Contains(message, "okx: stream "+operation+": ") {
					t.Fatal("refusal operation missing from error or status")
				}
				if tc.code == "" || tc.code == "venue-code-marker" || tc.code == "123456789" {
					if !strings.Contains(message, "unreadable code") ||
						(tc.code != "" && strings.Contains(message, tc.code)) {
						t.Fatal("unreadable refusal code escaped into error or status")
					}
				} else if !strings.Contains(message, "code "+tc.code) {
					t.Fatal("refusal code missing from error or status")
				}
				for _, value := range []string{
					"okx-key-marker", "okx-secret-marker", "okx-pass-marker", signature,
				} {
					if strings.Contains(message, value) {
						t.Fatal("request credentials escaped into error or status")
					}
				}
			})
		}
	}
}

func TestOKXStreamCloseReasonStaysPrivate(t *testing.T) {
	for _, lifecycle := range []bool{false, true} {
		t.Run(strconv.FormatBool(lifecycle), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v5/public/instruments" {
					okxReply(w, []okx.Instrument{okxTestInstrument()})
					return
				}
				conn := okxWSHandshake(t, ctx, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				_ = conn.Close(websocket.StatusCode(4004), "venue-close-reason-marker")
			})
			var message string
			if lifecycle {
				statuses := make(chan string, 2)
				c.SetStatusReporter(func(connected bool, text string) {
					if !connected {
						statuses <- text
					}
				})
				events, err := c.Subscribe(ctx)
				if err != nil {
					t.Fatal("subscribe failed")
				}
				go func() {
					for range events {
					}
				}()
				message = <-statuses // Stream publishes the close status.
			} else {
				_, err := c.stream(ctx, make(chan fwtrading.Event, 1))
				if err == nil {
					t.Fatal("venue close did not end stream")
				}
				message = err.Error()
			}
			if !strings.Contains(message, "okx: stream closed by venue: status 4004") ||
				strings.Contains(message, "venue-close-reason-marker") {
				t.Fatal("close status missing or venue reason escaped into error or status")
			}
		})
	}
}

func TestOKXStreamReconnect(t *testing.T) {
	for _, reason := range []string{"notice", "close", "error"} {
		t.Run(reason, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			var sessions atomic.Int32
			c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v5/public/instruments" {
					okxReply(w, []okx.Instrument{okxTestInstrument()})
					return
				}
				conn := okxWSHandshake(t, ctx, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				if sessions.Add(1) == 1 {
					if reason == "close" {
						return
					}
					okxWSWrite(t, ctx, conn, map[string]string{
						"event": reason, "code": "64008",
						"msg": "okx-key-marker okx-secret-marker okx-pass-marker www.okx.com",
					})
				}
				_, _, _ = conn.Read(ctx)
			})
			var mu sync.Mutex
			var messages []string
			c.SetStatusReporter(func(_ bool, message string) {
				mu.Lock()
				messages = append(messages, message)
				mu.Unlock()
			})
			events, err := c.Subscribe(ctx)
			if err != nil {
				t.Fatal("subscribe")
			}
			for i := 0; i < 2; i++ {
				if nextEvent(t, events).Kind != fwtrading.EventConnected {
					t.Fatal("reconnect must announce reconciliation")
				}
			}
			mu.Lock()
			defer mu.Unlock()
			for _, message := range messages {
				for _, secret := range []string{
					"okx-key-marker", "okx-secret-marker", "okx-pass-marker", "www.okx.com",
				} {
					if strings.Contains(message, secret) {
						t.Fatal("stream status leaked credential")
					}
				}
			}
		})
	}
}

func TestOKXStreamRowDiagnostics(t *testing.T) {
	c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	c.symbols["BTC-USDT"] = okxTestInstrument()
	for _, tc := range []struct{ name, field, value, level, detail string }{
		{"unknown ref", "clOrdId", "", "", ""},
		{"bad time", "fillTime", "bad", health.DiagWarn, okxFillUnreadable},
		{"bad decimal", "fillPx", "NaN", health.DiagWarn, okxFillUnreadable},
		{"overfilled", "accFillSz", "11", health.DiagError, okxFillOverfilled},
		{"bad venue id", "ordId", "wrong", health.DiagWarn, okxOrderUnreadable},
		{"bad commission", "fillFee", "invalid", health.DiagWarn, okxFillAssets},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := okxWSRow("partially_filled", "3", "3", "1")
			row[tc.field] = tc.value
			raw, _ := json.Marshal(row)
			events, diag := c.parseOKXOrder(context.Background(), raw)
			if tc.level == "" {
				if len(events) != 0 || diag != nil {
					t.Fatal("foreign order must be silent")
				}
				return
			}
			if len(events) != 1 || events[0].Kind != fwtrading.EventOrder ||
				diag == nil || diag.Level != tc.level || diag.Detail != tc.detail {
				t.Fatal("known order must reconcile with diagnostic")
			}
		})
	}
	events, diag := c.parseOKXOrder(context.Background(), json.RawMessage("42"))
	if len(events) != 0 || diag == nil || diag.Level != health.DiagError {
		t.Fatal("unknown reference must be dropped with error")
	}
}

func TestOKXStreamPongTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	pingSeen := make(chan struct{}, 1)
	c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/public/instruments" {
			okxReply(w, []okx.Instrument{okxTestInstrument()})
			return
		}
		conn := okxWSHandshake(t, ctx, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if string(raw) == "ping" {
				select {
				case pingSeen <- struct{}{}:
				default:
				}
				okxWSWrite(t, ctx, conn, okxWSData(okxWSRow("live", "0", "0", "")))
			}
		}
	})
	out := make(chan fwtrading.Event, 1)
	c.pingInterval = 150 * time.Millisecond
	c.pongTimeout = 100 * time.Millisecond
	_, err := c.stream(ctx, out)
	if err == nil || !strings.Contains(err.Error(), "pong timeout") {
		t.Fatal("missing pong did not break session")
	}
	select {
	case <-pingSeen:
	default:
		t.Fatal("PingLoop did not send text ping")
	}
}

func TestOKXStreamBlockedDeliveryPongTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/public/instruments" {
			okxReply(w, []okx.Instrument{okxTestInstrument()})
			return
		}
		conn := okxWSHandshake(t, ctx, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		okxWSWrite(t, ctx, conn, okxWSData(okxWSRow("canceled", "0", "0", "")))
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	})
	c.pingInterval = 150 * time.Millisecond
	c.pongTimeout = 100 * time.Millisecond
	// The connected event fits; the order delivery has no consumer.
	_, err := c.stream(ctx, make(chan fwtrading.Event, 1))
	if err == nil || !strings.Contains(err.Error(), "pong timeout") {
		t.Fatal("blocked delivery hid the pong failure")
	}
}

func TestOKXStreamCancelWithFill(t *testing.T) {
	for _, state := range []string{"canceled", "mmp_canceled"} {
		for _, withFill := range []bool{false, true} {
			t.Run(state+"/"+strconv.FormatBool(withFill), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				t.Cleanup(cancel)
				c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/v5/public/instruments" {
						okxReply(w, []okx.Instrument{okxTestInstrument()})
						return
					}
					conn := okxWSHandshake(t, ctx, w, r)
					if conn == nil {
						return
					}
					defer func() { _ = conn.CloseNow() }()
					row := okxWSRow(state, "0", "0", "")
					if withFill {
						row = okxWSRow(state, "3", "3", "1")
					}
					okxWSWrite(t, ctx, conn, okxWSData(row))
					okxWSWrite(t, ctx, conn, map[string]string{"event": "notice"})
				})
				out := make(chan fwtrading.Event, 4)
				_, err := c.stream(ctx, out)
				if err == nil || !strings.Contains(err.Error(), "reconnect notice") {
					t.Fatal("cancel stream did not reach its end")
				}
				if nextEvent(t, out).Kind != fwtrading.EventConnected {
					t.Fatal("missing connected event")
				}
				var ref fwtrading.OrderRef
				if withFill {
					event := nextEvent(t, out)
					if event.Kind != fwtrading.EventFill || event.Fill.Final {
						t.Fatal("cancel fill must precede reconciliation and stay non-final")
					}
					ref = event.Ref
				}
				event := nextEvent(t, out)
				if event.Kind != fwtrading.EventOrder || event.Ref.ClientOrderID != okxTestCOID ||
					(withFill && event.Ref != ref) || len(out) != 0 {
					t.Fatal("cancel must emit exactly one reconciliation with the same ref")
				}
			})
		}
	}
}

func TestOKXStreamCatalogue(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(strconv.FormatBool(fail), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			var requests atomic.Int32
			c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v5/public/instruments" {
					requests.Add(1)
					if fail {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					okxReply(w, []okx.Instrument{okxTestInstrument()})
					return
				}
				conn := okxWSHandshake(t, ctx, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				known := okxWSRow("partially_filled", "3", "3", "1")
				unknown := okxWSRow("partially_filled", "3", "3", "2")
				unknown["instId"] = "MISSING-USDT"
				if fail {
					_, _, _ = conn.Read(ctx)
					return
				}
				okxWSWrite(t, ctx, conn, okxWSData(known, unknown))
				okxWSWrite(t, ctx, conn, map[string]string{"event": "notice"})
			})
			var diagnostics []health.Diagnostic
			c.SetDiagnosticReporter(func(d health.Diagnostic) {
				diagnostics = append(diagnostics, d)
			})
			out := make(chan fwtrading.Event, 4)
			_, err := c.stream(ctx, out)
			if requests.Load() != 1 || err == nil {
				t.Fatal("session must load its catalogue exactly once")
			}
			if fail {
				if !strings.Contains(err.Error(), "catalogue") || len(out) != 0 {
					t.Fatal("catalogue failure must end the session before connected")
				}
				return
			}
			if !strings.Contains(err.Error(), "reconnect notice") {
				t.Fatal("order reader did not reach the notice")
			}
			nextEvent(t, out)
			if nextEvent(t, out).Kind != fwtrading.EventFill ||
				nextEvent(t, out).Kind != fwtrading.EventOrder ||
				len(diagnostics) != 1 || diagnostics[0].Detail != okxFillAssets ||
				diagnostics[0].Level != health.DiagWarn ||
				diagnostics[0].Remediation != venueStreamReconciled {
				t.Fatal("cached assets or missing-assets reconciliation changed")
			}
		})
	}
}

func TestOKXStreamMissedFillsReconciledOnce(t *testing.T) {
	f := newTradingE2E(t, false)
	var mu sync.Mutex
	var coid string
	var current *websocket.Conn
	var broken bool
	var lookups, posts int
	connected := make(chan *websocket.Conn, 4)
	replayed := make(chan struct{}, 1)
	venue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			conn := okxWSHandshake(t, f.ctx, w, r)
			if conn == nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			mu.Lock()
			current = conn
			recovering := broken
			id := coid
			mu.Unlock()
			connected <- conn
			select {
			case f.streams <- struct{}{}:
			case <-f.ctx.Done():
				return
			}
			if recovering {
				for i, fill := range []struct{ qty, cum string }{
					{"2", "2"}, {"3", "5"}, {"1", "6"},
				} {
					row := okxWSRow("partially_filled", fill.qty, fill.cum, strconv.Itoa(i+1))
					row["clOrdId"] = id
					row["instId"] = "AAPL-USD"
					row["fillFee"] = "0"
					row["fillFeeCcy"] = ""
					okxWSWrite(t, f.ctx, conn, okxWSData(row))
				}
				replayed <- struct{}{}
			}
			_, _, _ = conn.Read(f.ctx)
			return
		}
		switch r.URL.Path {
		case "/api/v5/account/config":
			okxReply(w, []map[string]string{{"uid": "12345"}})
		case "/api/v5/public/instruments":
			instrument := okxTestInstrument()
			instrument.InstID = "AAPL-USD"
			instrument.BaseCcy = "AAPL"
			instrument.QuoteCcy = "USD"
			okxReply(w, []okx.Instrument{instrument})
		case "/api/v5/trade/order":
			mu.Lock()
			defer mu.Unlock()
			if r.Method == http.MethodPost {
				var body map[string]string
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("send JSON")
					return
				}
				posts++
				coid = body["clOrdId"]
				okxReply(w, []map[string]string{{"ordId": "123", "clOrdId": coid, "sCode": "0"}})
				return
			}
			lookups++
			state, filled := "partially_filled", "2"
			if broken {
				state, filled = "canceled", "6"
			}
			o := okxTestLookup(state, filled)
			o.ClientID = coid
			o.Symbol = "AAPL-USD"
			okxReply(w, []okxOrder{o})
		case "/api/v5/trade/fills", "/api/v5/trade/fills-history":
			mu.Lock()
			recovering := broken
			mu.Unlock()
			trades := []okxTrade{okxTestTrade(1)}
			trades[0].Quantity = "2"
			trades[0].Price = "2"
			if recovering {
				for i, q := range []string{"3", "1"} {
					trade := okxTestTrade(i + 2)
					trade.Quantity = q
					trade.Price = "2"
					trades = append(trades, trade)
				}
			}
			okxReply(w, trades)
		default:
			t.Error("unexpected OKX recovery request")
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(venue.Close)
	tradingE2EMust(t, f.app.Close())
	provider := OKXProvider()
	provider.Build = func(ctx fwtrading.BuildContext) (fwtrading.Connector, error) {
		return newOKXConnector(ctx.Connection, venue.URL,
			"ws"+strings.TrimPrefix(venue.URL, "http")+"/stream")
	}
	tradingE2EMust(t, f.builder.RegisterTradingProvider(provider))
	f.start()
	created := tradingE2EDecode[tradingE2ECreated](t,
		f.rest(http.MethodPost, "/trading/connections", map[string]any{
			"provider": "okx", "label": "OKX demo", "mode": "paper", "enabled": true,
			"credentials": json.RawMessage(okxTestCredentials),
		}, http.StatusCreated)).Connection
	id := created.ID
	path := "/trading/connections/" + id
	f.rest(http.MethodPut, path+"/instruments", map[string]any{
		"externalSymbol": "AAPL-USD", "baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true,
	}, http.StatusOK)
	f.rest(http.MethodPut, path+"/access", map[string]any{"account": "desk"}, http.StatusOK)
	f.rest(http.MethodPost, "/accounts/desk/adjustments?missingAccount=reject", map[string]any{
		"type": "deposit", "asset": "USD", "averageEntryPrice": "1", "realizedPnl": "0",
		"balance": map[string]any{"mode": "absolute", "value": "1000"},
	}, http.StatusCreated)
	<-connected // First connection.
	input := tradingE2ESendInput(id)
	input["destination"] = map[string]any{
		"connection": id, "route": map[string]any{"timeInForce": "GTC"},
	}
	sent := tradingE2EDecode[struct{ ID string }](t,
		f.sendPaper("REST", input, http.StatusCreated, ""))
	mu.Lock()
	conn, idHex := current, coid
	mu.Unlock()
	row := okxWSRow("partially_filled", "2", "2", "1")
	row["clOrdId"] = idHex
	row["instId"] = "AAPL-USD"
	row["fillFee"] = "0"
	row["fillFeeCcy"] = ""
	if !okxWSWrite(t, f.ctx, conn, okxWSData(row)) {
		t.Fatal("first fill write")
	}
	polltest.Until(func() bool {
		d, e := f.realm.GetOrder(f.ctx, domain.ExternalID(sent.ID))
		tradingE2EMust(t, e)
		return len(d.Trades) == 1
	}) // first OKX fill
	mu.Lock()
	broken = true
	mu.Unlock()
	if !okxWSWrite(t, f.ctx, conn, map[string]string{"event": "notice", "code": "64008"}) {
		t.Fatal("break notice")
	}
	<-connected // Reconnect.
	<-replayed  // Replay.
	polltest.Until(func() bool {
		d, e := f.realm.GetOrder(f.ctx, domain.ExternalID(sent.ID))
		tradingE2EMust(t, e)
		return len(d.Trades) == 3 && d.Order.Status == domain.OrderStatusCancelled
	}) // OKX recovered fills and cancellation
	f.close()
	mu.Lock()
	gotPosts, gotLookups := posts, lookups
	mu.Unlock()
	if gotPosts != 1 || gotLookups == 0 {
		t.Fatal("recovery must lookup without resending")
	}
	// After restart, duplicates must not change the three booked trades.
	f.start()
	<-connected // Connection after restart.
	detail, err := f.realm.GetOrder(f.ctx, domain.ExternalID(sent.ID))
	tradingE2EMust(t, err)
	if len(detail.Trades) != 3 {
		t.Fatal("replayed fills were booked twice")
	}
	total := decimal.Zero
	for _, trade := range detail.Trades {
		q, e := decimal.NewFromString(trade.Quantity)
		tradingE2EMust(t, e)
		total = total.Add(q)
	}
	if total.String() != "6" {
		t.Fatal("missed fills were lost or double counted")
	}
	f.close()
}
