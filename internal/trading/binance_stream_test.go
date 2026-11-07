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
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
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
	"go.openpit.dev/officer/internal/binance"
)

func binanceWriteFrame(t *testing.T, conn *websocket.Conn, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Error(err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Error(err)
	}
}

func binanceAccept(t *testing.T, w http.ResponseWriter,
	r *http.Request,
) (*websocket.Conn, string) {
	t.Helper()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		t.Error(err)
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Error(err)
		_ = conn.CloseNow()
		return nil, ""
	}
	var request struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params struct {
			APIKey    string `json:"apiKey"`
			Timestamp int64  `json:"timestamp"`
			Signature string `json:"signature"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &request) != nil || request.ID == "" ||
		request.Method != "userDataStream.subscribe.signature" ||
		request.Params.APIKey == "" || request.Params.Timestamp <= 0 ||
		request.Params.Signature == "" {
		t.Error("invalid signed subscription request")
	}
	return conn, request.ID
}

func binanceConfirm(t *testing.T, conn *websocket.Conn, id string) {
	t.Helper()
	binanceWriteFrame(t, conn, map[string]any{
		"id": id, "status": 200,
		"result": map[string]int{"subscriptionId": 7},
	})
}

func binanceWaitForClose(conn *websocket.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _, _ = conn.Read(ctx)
}

func binanceWriteEvent(t *testing.T, conn *websocket.Conn, event any) {
	t.Helper()
	binanceWriteFrame(t, conn, map[string]any{
		"subscriptionId": 7, "event": event,
	})
}

func binanceExecution(trade binanceTrade,
	original, cumulative string, final bool,
) map[string]any {
	status := "PARTIALLY_FILLED"
	if final {
		status = "FILLED"
	}
	return map[string]any{
		"e": "executionReport", "x": "TRADE", "X": status,
		"E": trade.At, "t": trade.ID, "Q": "0", "Z": "999.00",
		"c": "officer-client", "C": "", "i": trade.OrderID, "s": "ETHUSDT",
		"q": original, "l": trade.Quantity, "L": trade.Price, "z": cumulative,
		"T": trade.At, "n": trade.Commission, "N": trade.CommissionAsset,
	}
}

func binanceNextEvent(t *testing.T,
	events <-chan fwtrading.Event,
) fwtrading.Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("stream closed before event")
		}
		return event
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for stream event")
		return fwtrading.Event{}
	}
}

func binanceSubscribe(t *testing.T,
	c *binanceConnector,
) <-chan fwtrading.Event {
	t.Helper()
	events, err := c.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	event := binanceNextEvent(t, events)
	if event.Kind != fwtrading.EventConnected {
		t.Fatal("first event must confirm connection")
	}
	return events
}

type binanceTestReports struct {
	mu          sync.Mutex
	diagnostics []health.Diagnostic
	messages    []string
	statuses    chan bool
}

func binanceRecordReports(c *binanceConnector) *binanceTestReports {
	reports := &binanceTestReports{statuses: make(chan bool, 100)}
	c.SetStatusReporter(func(connected bool, message string) {
		reports.mu.Lock()
		reports.messages = append(reports.messages, message)
		reports.mu.Unlock()
		reports.statuses <- connected
	})
	c.SetDiagnosticReporter(func(d health.Diagnostic) {
		reports.mu.Lock()
		defer reports.mu.Unlock()
		reports.diagnostics = append(reports.diagnostics, d)
	})
	return reports
}

func TestBinanceStreamExecutions(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
		kind        fwtrading.EventKind
		detail      string
		code        string
	}{
		{"partial", "z", "1", fwtrading.EventFill, "", ""},
		{"final", "X", "FILLED", fwtrading.EventFill, "", ""},
		{"cancel", "x", "CANCELED", fwtrading.EventOrder, "", ""},
		{"rejected", "x", "REJECTED", fwtrading.EventOrder, "", ""},
		{"expired", "x", "EXPIRED", fwtrading.EventOrder, "", ""},
		{"prevention", "x", "TRADE_PREVENTION", fwtrading.EventOrder, "", ""},
		{"replaced", "x", "REPLACED", fwtrading.EventOrder, "", ""},
		{"new", "x", "NEW", "", "", ""},
		{"unknown", "x", "FUTURE", "", "", ""},
		{"other event", "e", "balanceUpdate", "", "", ""},
		{"overfill", "z", "3", fwtrading.EventOrder,
			binanceFillOverfilled, health.CodeInconsistentData},
		{"quantity", "l", "bad", fwtrading.EventOrder,
			binanceFillDecimals, health.CodeUnparsableData},
		{"price", "L", "1e3", fwtrading.EventOrder,
			binanceFillDecimals, health.CodeUnparsableData},
		{"original", "q", "0", fwtrading.EventOrder,
			binanceFillDecimals, health.CodeUnparsableData},
		{"cumulative", "z", "-1", fwtrading.EventOrder,
			binanceFillDecimals, health.CodeUnparsableData},
		{"commission", "n", "bad", fwtrading.EventOrder,
			binanceFillDecimals, health.CodeUnparsableData},
		{"untimed", "T", nil, fwtrading.EventOrder,
			binanceFillUntimed, health.CodeUnparsableData},
		{"time type", "T", "bad", fwtrading.EventOrder,
			binanceFillUnreadable, health.CodeUnparsableData},
		{"no client", "c", "", "",
			binanceRefUnreadable, health.CodeUnparsableData},
		{"client type", "c", 7, "",
			binanceRefUnreadable, health.CodeUnparsableData},
		{"cancel reference", "C", 7, "",
			binanceRefUnreadable, health.CodeUnparsableData},
		{"symbol type", "s", 7, fwtrading.EventOrder,
			binanceSymbolUnreadable, health.CodeUnparsableData},
		{"no venue id", "i", nil, fwtrading.EventOrder,
			binanceVenueIDUnusable, health.CodeUnparsableData},
		{"fractional id", "i", json.Number("1.5"), fwtrading.EventOrder,
			binanceVenueIDUnusable, health.CodeUnparsableData},
		{"no execution", "x", nil, "",
			binanceHeaderUnreadable, health.CodeUnparsableData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trade := binanceTestTrade(1, "1", "ETH")
			raw := binanceExecution(trade, "2", "1", false)
			raw[tc.field] = tc.value
			if tc.name == "cancel" {
				raw["c"], raw["C"] = "cancel-request", "officer-client"
			}
			if tc.name == "cancel reference" {
				raw["x"] = "CANCELED"
			}
			c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/stream" {
					writeBinanceSymbol(t, w, binanceTestSymbol())
					return
				}
				conn, id := binanceAccept(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				binanceConfirm(t, conn, id)
				binanceWriteEvent(t, conn, raw)
				binanceWriteEvent(t, conn, map[string]any{
					"e": "executionReport", "x": "EXPIRED", "c": "sentinel", "i": 9,
				})
				binanceWaitForClose(conn)
			})
			reports := binanceRecordReports(c)
			events := binanceSubscribe(t, c)
			event := binanceNextEvent(t, events)
			if tc.kind == "" {
				if event.Ref.ClientOrderID != "sentinel" {
					t.Fatalf("ignored update emitted %+v", event)
				}
			} else {
				if event.Kind != tc.kind ||
					event.Ref.ClientOrderID != "officer-client" {
					t.Fatalf("event=%+v; want %s with original client id",
						event, tc.kind)
				}
				if tc.detail != binanceVenueIDUnusable &&
					event.Ref.VenueOrderID != binanceTestOrderID {
					t.Fatalf("venue id lost digits: %+v", event.Ref)
				}
				if event.Kind == fwtrading.EventFill {
					if event.Fill.Quantity != "1" || event.Fill.Price != "10.00" ||
						event.Fill.CumQuantity != "1" ||
						event.Fill.LeavesQuantity != "1" ||
						event.Fill.CommissionLeg != fwtrading.CommissionLegBase ||
						event.Fill.Commission == nil ||
						event.Fill.Commission.Currency != "ETH" ||
						event.Fill.Final != (tc.name == "final") {
						t.Fatalf("incorrect fill: %+v", event.Fill)
					}
				}
				_ = binanceNextEvent(t, events)
			}
			reports.mu.Lock()
			defer reports.mu.Unlock()
			if tc.detail == "" {
				if len(reports.diagnostics) != 0 {
					t.Fatalf("unexpected diagnostics: %+v", reports.diagnostics)
				}
			} else if len(reports.diagnostics) != 1 ||
				reports.diagnostics[0].Detail != tc.detail ||
				reports.diagnostics[0].Code != tc.code {
				t.Fatalf("diagnostics=%+v", reports.diagnostics)
			}
		})
	}
}

func TestBinanceStreamCommissionRecovery(t *testing.T) {
	for _, name := range []string{
		"fetch fails", "zero null", "zero empty", "zero named",
	} {
		t.Run(name, func(t *testing.T) {
			var fetches atomic.Int32
			c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/stream" {
					fetches.Add(1)
					w.WriteHeader(503)
					return
				}
				conn, id := binanceAccept(t, w, r)
				if conn == nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				binanceConfirm(t, conn, id)
				raw := binanceExecution(binanceTestTrade(1, "2", "ETH"),
					"2", "2", true)
				if name != "fetch fails" {
					raw["n"], raw["N"] = "0.00000000", ""
					switch name {
					case "zero null":
						raw["N"] = nil
					case "zero named":
						raw["N"] = "ETH"
					}
				}
				binanceWriteEvent(t, conn, raw)
				binanceWaitForClose(conn)
			})
			event := binanceNextEvent(t, binanceSubscribe(t, c))
			if name == "fetch fails" {
				if event.Kind != fwtrading.EventOrder || fetches.Load() != 1 {
					t.Fatalf("failed asset lookup must reconcile: %+v", event)
				}
			} else if event.Kind != fwtrading.EventFill ||
				event.Fill.Commission != nil || event.Fill.CommissionLeg != "" ||
				fetches.Load() != 0 {
				t.Fatalf("zero commission=%+v", event)
			}
		})
	}
}

func TestBinanceStreamHandshakeAndReconnect(t *testing.T) {
	var connections atomic.Int32
	ids := make(chan string, 2)
	c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
		conn, id := binanceAccept(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ids <- id
		attempt := connections.Add(1)
		binanceWriteFrame(t, conn, map[string]any{
			"id": id, "status": 200, "result": map[string]string{},
		})
		binanceWriteFrame(t, conn, map[string]any{
			"id": "unrelated", "status": 200,
			"result": map[string]int{"subscriptionId": 99},
		})
		binanceConfirm(t, conn, id)
		if attempt == 1 {
			binanceWriteEvent(t, conn, map[string]string{
				"e": "eventStreamTerminated",
			})
			return
		}
		binanceWaitForClose(conn)
	})
	reports := binanceRecordReports(c)
	events := binanceSubscribe(t, c)
	event := binanceNextEvent(t, events)
	if event.Kind != fwtrading.EventConnected {
		t.Fatalf("reconnect did not emit connected: %+v", event)
	}
	if first, second := <-ids, <-ids; first == second {
		t.Fatal("reconnect reused subscription request id")
	}
	c.Close()
	reports.mu.Lock()
	defer reports.mu.Unlock()
	if len(reports.diagnostics) != 2 {
		t.Fatalf("unreadable subscription must report per connection: %+v",
			reports.diagnostics)
	}
	for _, diag := range reports.diagnostics {
		if diag.Detail != binanceSubscribeUnreadable {
			t.Fatalf("wrong handshake diagnostic: %+v", diag)
		}
	}
}

func TestBinanceStreamVenueReconnect(t *testing.T) {
	for _, reason := range []string{"serverShutdown", "close frame"} {
		t.Run(reason, func(t *testing.T) {
			var connections atomic.Int32
			c := newBinanceTest(t,
				func(w http.ResponseWriter, r *http.Request) {
					conn, id := binanceAccept(t, w, r)
					if conn == nil {
						return
					}
					defer func() { _ = conn.CloseNow() }()
					attempt := connections.Add(1)
					binanceConfirm(t, conn, id)
					if attempt == 1 {
						if reason == "close frame" {
							if err := conn.Close(websocket.StatusGoingAway, "restart"); err != nil {
								t.Error(err)
							}
							return
						}
						binanceWriteFrame(t, conn, map[string]any{
							"event": map[string]any{
								"e": "serverShutdown", "E": 1700000000000,
							},
						})
					}
					binanceWaitForClose(conn)
				})
			reports := binanceRecordReports(c)
			events := binanceSubscribe(t, c)
			event := binanceNextEvent(t, events)
			if event.Kind != fwtrading.EventConnected {
				t.Fatalf("venue reconnect did not emit connected: %+v", event)
			}
			c.Close()
			reports.mu.Lock()
			defer reports.mu.Unlock()
			if len(reports.diagnostics) != 0 {
				t.Fatalf("venue reconnect reported unreadable data: %+v",
					reports.diagnostics)
			}
			disconnected := false
			for _, message := range reports.messages {
				if strings.Contains(message, "binance: close stream connection") {
					t.Fatalf("cleanup error leaked into status: %s", message)
				}
				if message != "" {
					disconnected = true
				}
			}
			if !disconnected || connections.Load() != 2 {
				t.Fatalf("reconnect statuses=%v connections=%d",
					reports.messages, connections.Load())
			}
		})
	}
}

func TestBinanceStreamReferenceDiagnostics(t *testing.T) {
	c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
		conn, id := binanceAccept(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		binanceConfirm(t, conn, id)
		raw := binanceExecution(binanceTestTrade(1, "1", "ETH"), "2", "1", false)
		raw["s"] = 7
		binanceWriteEvent(t, conn, raw)
		raw["s"], raw["c"] = "ETHUSDT", ""
		binanceWriteEvent(t, conn, raw)
		raw["x"], raw["c"] = "EXPIRED", "sentinel"
		binanceWriteEvent(t, conn, raw)
		binanceWaitForClose(conn)
	})
	reports := binanceRecordReports(c)
	events := binanceSubscribe(t, c)
	first := binanceNextEvent(t, events)
	last := binanceNextEvent(t, events)
	if first.Kind != fwtrading.EventOrder ||
		last.Ref.ClientOrderID != "sentinel" {
		t.Fatalf("unexpected reference events: %+v %+v", first, last)
	}
	c.Close()
	reports.mu.Lock()
	defer reports.mu.Unlock()
	if len(reports.diagnostics) != 2 {
		t.Fatalf("symbol and missing client id must both report: %+v",
			reports.diagnostics)
	}
	symbol, client := reports.diagnostics[0], reports.diagnostics[1]
	if symbol.Detail == client.Detail || symbol.Level != health.DiagWarn ||
		symbol.Remediation != venueStreamReconciled ||
		client.Level != health.DiagError || client.Remediation != venueStreamDropped {
		t.Fatalf("reference diagnostic sites collided: %+v %+v", symbol, client)
	}
}

func TestBinanceStreamDiagnosticsOnce(t *testing.T) {
	c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
		conn, id := binanceAccept(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		binanceConfirm(t, conn, id)
		for range 2 {
			binanceWriteFrame(t, conn, "test-key test-secret")
			bad := binanceExecution(binanceTestTrade(1, "1", "ETH"),
				"2", "3", false)
			binanceWriteEvent(t, conn, bad)
			bad["l"], bad["z"] = "test-secret", "1"
			binanceWriteEvent(t, conn, bad)
		}
		binanceWaitForClose(conn)
	})
	reports := binanceRecordReports(c)
	events := binanceSubscribe(t, c)
	for range 4 {
		if event := binanceNextEvent(t, events); event.Kind != fwtrading.EventOrder {
			t.Fatalf("bad data must reconcile: %+v", event)
		}
	}
	c.Close()
	reports.mu.Lock()
	defer reports.mu.Unlock()
	if len(reports.diagnostics) != 4 {
		t.Fatalf("want two sites once plus every overfill: %+v",
			reports.diagnostics)
	}
	for _, diag := range reports.diagnostics {
		if strings.Contains(fmt.Sprint(diag), "test-secret") ||
			strings.Contains(fmt.Sprint(diag), "test-key") {
			t.Fatal("diagnostic leaked frame content")
		}
	}
}

func TestBinanceStreamUnauthorizedHoldsMax(t *testing.T) {
	attempts := make(chan time.Time, 3)
	var count atomic.Int32
	c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
		conn, id := binanceAccept(t, w, r)
		if conn == nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		attempts <- time.Now()
		if count.Add(1) == 1 {
			binanceWriteFrame(t, conn, map[string]any{
				"id": id, "status": 400,
				"error": map[string]any{"code": -2015, "msg": "Invalid key"},
			})
			return
		}
		if count.Load() == 2 {
			return
		}
		binanceConfirm(t, conn, id)
		binanceWaitForClose(conn)
	})
	events, err := c.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var times []time.Time
	for range 3 {
		select {
		case at := <-attempts:
			times = append(times, at)
		case <-time.After(7 * time.Second):
			t.Fatal("reconnect did not resume")
		}
	}
	for i := 1; i < len(times); i++ {
		if times[i].Sub(times[i-1]) < binanceReconnectMax-100*time.Millisecond {
			t.Fatalf("refused credentials did not hold longest wait: %v",
				times[i].Sub(times[i-1]))
		}
	}
	event := binanceNextEvent(t, events)
	if event.Kind != fwtrading.EventConnected {
		t.Fatal("stream did not recover after refusal")
	}
}

func TestBinanceStreamLifecycle(t *testing.T) {
	closed := newBinanceTest(t,
		func(w http.ResponseWriter, r *http.Request) {
			t.Error("closed connector reached the venue")
		})
	closed.Close()
	if _, err := closed.Subscribe(context.Background()); err == nil {
		t.Fatal("connector closed before its first subscription was accepted")
	}
	c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err == nil {
			defer func() { _ = conn.CloseNow() }()
			binanceWaitForClose(conn)
		}
	})
	c.SetDiagnosticReporter(nil)
	if _, err := c.Subscribe(context.Background()); err == nil {
		t.Fatal("missing diagnostic reporter accepted")
	}
	c.SetDiagnosticReporter(func(health.Diagnostic) {})
	c.SetStatusReporter(nil)
	if _, err := c.Subscribe(context.Background()); err == nil {
		t.Fatal("missing status reporter accepted")
	}
	c.SetStatusReporter(func(bool, string) {})
	ctx, cancel := context.WithCancel(context.Background())
	events, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Subscribe(ctx); err == nil {
		t.Fatal("double subscription accepted")
	}
	cancel()
	c.Close()
	if _, open := <-events; open {
		t.Fatal("cancelled handshake did not close stream")
	}
	c.Close()
	if _, err := c.Subscribe(context.Background()); err == nil {
		t.Fatal("closed connector accepted subscription")
	}
}

func assertBinanceFillParity(t *testing.T, stream, lookup fwtrading.Fill) {
	t.Helper()
	streamCum, err := decimal.NewFromString(stream.CumQuantity)
	if err != nil {
		t.Fatal(err)
	}
	lookupCum, err := decimal.NewFromString(lookup.CumQuantity)
	if err != nil || !streamCum.Equal(lookupCum) {
		t.Fatalf("cumulative differs: stream=%s lookup=%s",
			stream.CumQuantity, lookup.CumQuantity)
	}
	stream.CumQuantity = lookup.CumQuantity
	if !reflect.DeepEqual(stream, lookup) {
		t.Fatalf("stream=%+v commission=%+v; lookup=%+v commission=%+v",
			stream, stream.Commission, lookup, lookup.Commission)
	}
}

func TestBinanceFillParityRandomized(t *testing.T) {
	const seed = int64(309)
	random := rand.New(rand.NewSource(seed))
	for sequence := range 40 {
		t.Run(fmt.Sprintf("seed=%d/sequence=%d", seed, sequence),
			func(t *testing.T) {
				count := 2 + random.Intn(12)
				trades := make([]binanceTrade, count)
				streamAssets := make([]any, count)
				cumulative := make([]string, count)
				total := decimal.Zero
				for i := range trades {
					qty := decimal.New(int64(1+random.Intn(10000)), -4)
					asset := []string{"ETH", "USDT", "BNB", ""}[random.Intn(4)]
					trade := binanceTestTrade(10+i*7, qty.String(), asset)
					trade.Price = decimal.New(int64(1+random.Intn(999999)), -3).
						String()
					trade.Commission = decimal.New(int64(random.Intn(100)), -6).
						String()
					streamAssets[i] = asset
					if asset == "" || i < 2 {
						trade.Commission = "0.00000000"
						if i%2 == 0 {
							trade.CommissionAsset = "ETH"
							streamAssets[i] = nil
						} else {
							trade.CommissionAsset = ""
							streamAssets[i] = "USDT"
						}
					}
					trades[i] = trade
					total = total.Add(qty)
					cumulative[i] = total.StringFixed(6)
				}
				c := newBinanceTest(t,
					func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case binance.ExchangeInfoPath:
							writeBinanceSymbol(t, w, binanceTestSymbol())
						case "/api/v3/order":
							writeBinanceJSON(t, w, binanceLookupOrder(
								"FILLED", total.String(), total.String()))
						case "/api/v3/myTrades":
							reversed := append([]binanceTrade(nil), trades...)
							for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
								reversed[i], reversed[j] = reversed[j], reversed[i]
							}
							writeBinanceJSON(t, w, reversed)
						case "/stream":
							conn, id := binanceAccept(t, w, r)
							if conn == nil {
								return
							}
							defer func() { _ = conn.CloseNow() }()
							binanceConfirm(t, conn, id)
							for i, trade := range trades {
								raw := binanceExecution(trade, total.String(),
									cumulative[i], i == count-1)
								raw["N"] = streamAssets[i]
								binanceWriteEvent(t, conn, raw)
							}
							binanceWaitForClose(conn)
						}
					})
				events := binanceSubscribe(t, c)
				fills := make([]fwtrading.Fill, 0, count)
				for range count {
					event := binanceNextEvent(t, events)
					if event.Kind != fwtrading.EventFill {
						t.Fatalf("expected fill: %+v", event)
					}
					fills = append(fills, event.Fill)
				}
				// A new connector has an empty asset cache, as after a restart.
				restarted, err := newBinanceConnector(domain.TradingConnection{
					Credentials: binanceTestCredentials,
				}, c.restURL, c.streamURL)
				if err != nil {
					t.Fatal(err)
				}
				defer restarted.Close()
				restarted.SetDiagnosticReporter(func(health.Diagnostic) {})
				for range 2 {
					snapshot, found, err := restarted.Lookup(context.Background(),
						fwtrading.OrderRef{Symbol: "ETHUSDT",
							ClientOrderID: "officer-client"})
					if err != nil || !found || len(snapshot.Fills) != count {
						t.Fatalf("lookup count=%d: %v", len(snapshot.Fills), err)
					}
					for i := range fills {
						assertBinanceFillParity(t, fills[i], snapshot.Fills[i])
					}
				}
				c.Close()
			})
	}
}

func TestBinanceRedaction(t *testing.T) {
	const key = `overlap"\&`
	const secret = key + "longer"
	credentials, err := json.Marshal(map[string]string{
		"apiKey": key, "apiSecret": secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(secret)
	variants := []string{key, secret, string(encoded[1 : len(encoded)-1]),
		url.QueryEscape(secret), url.PathEscape(secret)}
	message := strings.Join(variants, " ")
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/stream" {
				w.WriteHeader(400)
				writeBinanceJSON(t, w, map[string]any{
					"code": -1013, "msg": message,
				})
				return
			}
			conn, id := binanceAccept(t, w, r)
			if conn == nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			binanceWriteFrame(t, conn, map[string]any{
				"id": id, "status": 400,
				"error": map[string]any{"code": -1000, "msg": message},
			})
		}))
	defer server.Close()
	c, err := newBinanceConnector(domain.TradingConnection{
		Credentials: string(credentials),
	}, server.URL, "ws"+strings.TrimPrefix(server.URL, "http")+"/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	reports := binanceRecordReports(c)
	_, sendErr := c.Send(context.Background(), binanceTestOrder())
	if !errors.Is(sendErr, fwtrading.ErrRejected) {
		t.Fatalf("Send: got %v, want fwtrading.ErrRejected", sendErr)
	}
	_, err = c.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-reports.statuses:
	case <-time.After(3 * time.Second):
		t.Fatal("missing refused subscription status")
	}
	c.Close()
	reports.mu.Lock()
	defer reports.mu.Unlock()
	text := sendErr.Error() + fmt.Sprint(reports.messages, reports.diagnostics)
	for _, variant := range variants {
		if strings.Contains(text, variant) {
			t.Fatal("credentials appeared in error, status or diagnostic")
		}
	}
}
