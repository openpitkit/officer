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

package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/internal/openalgo"
)

const oaFakeKey = "fake-openalgo-key-marker"

type oaQuoteRequest struct {
	Action    string                `json:"action"`
	Mode      string                `json:"mode"`
	RequestID string                `json:"request_id"`
	Symbols   []openalgo.Instrument `json:"symbols"`
}

type oaStatus struct {
	ok      bool
	message string
}

func oaRead(t *testing.T, ctx context.Context, conn *websocket.Conn, into any) bool {
	t.Helper()
	typ, payload, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageText || json.Unmarshal(payload, into) != nil {
		t.Error("could not read OpenAlgo client request")
		return false
	}
	return true
}

func oaWrite(t *testing.T, ctx context.Context, conn *websocket.Conn, frame any) bool {
	t.Helper()
	payload, err := json.Marshal(frame)
	if err != nil || conn.Write(ctx, websocket.MessageText, payload) != nil {
		t.Error("could not write OpenAlgo test frame")
		return false
	}
	return true
}

func oaHandshake(t *testing.T, ctx context.Context, conn *websocket.Conn) (oaQuoteRequest, bool) {
	t.Helper()
	var auth struct {
		Action string `json:"action"`
		APIKey string `json:"api_key"`
	}
	if !oaRead(t, ctx, conn, &auth) {
		return oaQuoteRequest{}, false
	}
	if auth.Action != "authenticate" || auth.APIKey != oaFakeKey {
		t.Error("authentication wire shape changed")
		return oaQuoteRequest{}, false
	}
	if !oaWrite(t, ctx, conn, map[string]string{"type": "auth", "status": "success"}) {
		return oaQuoteRequest{}, false
	}
	var request oaQuoteRequest
	if !oaRead(t, ctx, conn, &request) {
		return request, false
	}
	if request.Action != "subscribe" || request.Mode != "Quote" || request.RequestID == "" {
		t.Error("subscription wire shape changed")
		return request, false
	}
	return request, true
}

func oaAck(t *testing.T, ctx context.Context, conn *websocket.Conn, request oaQuoteRequest) bool {
	t.Helper()
	rows := make([]map[string]string, 0, len(request.Symbols))
	for _, instrument := range request.Symbols {
		rows = append(rows, map[string]string{"exchange": instrument.Exchange, "symbol": instrument.Symbol, "status": "success"})
	}
	return oaWrite(t, ctx, conn, map[string]any{
		"type": "subscribe", "status": "success", "request_id": request.RequestID, "subscriptions": rows,
	})
}

func oaQuote(symbol, exchange, price string) string {
	return fmt.Sprintf(`{"type":"market_data","symbol":%q,"exchange":%q,"mode":2,"data":{"ltp":%s,"timestamp":1712572800123,"depth":{"buy":[]},"volume":10}}`, symbol, exchange, price)
}

func oaNewConnector(t *testing.T, restURL, wsURL string) fwmarketdata.Connector {
	t.Helper()
	credentials, err := json.Marshal(map[string]string{"url": restURL, "wsUrl": wsURL, "apiKey": oaFakeKey})
	if err != nil {
		t.Fatal("could not prepare fake credentials")
	}
	connector, err := NewOpenAlgoConnector(domain.MarketDataInstance{Credentials: string(credentials)})
	if err != nil {
		t.Fatal("could not construct OpenAlgo connector")
	}
	t.Cleanup(connector.Close)
	return connector
}

func oaServer(t *testing.T, handler func(context.Context, *websocket.Conn)) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error("could not accept test WebSocket")
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		handler(ctx, conn)
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func oaSubscribe(t *testing.T, connector fwmarketdata.Connector, subs []fwmarketdata.Subscription) <-chan fwmarketdata.QuoteUpdate {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	updates, err := connector.Subscribe(ctx, subs)
	if err != nil {
		t.Fatal("could not subscribe to test symbols")
	}
	return updates
}

func oaNextUpdate(t *testing.T, updates <-chan fwmarketdata.QuoteUpdate) fwmarketdata.QuoteUpdate {
	t.Helper()
	select {
	case update, ok := <-updates:
		if !ok {
			t.Fatal("quote channel closed before expected update")
		}
		return update
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for quote")
		return fwmarketdata.QuoteUpdate{}
	}
}

func oaReporters(t *testing.T, connector fwmarketdata.Connector) (<-chan oaStatus, <-chan health.Diagnostic) {
	t.Helper()
	statuses := make(chan oaStatus, 32)
	diagnostics := make(chan health.Diagnostic, 32)
	connector.(interface{ SetStatusReporter(health.StatusReporter) }).SetStatusReporter(func(ok bool, message string) {
		if strings.Contains(message, oaFakeKey) {
			t.Error("status exposed a credential")
		}
		statuses <- oaStatus{ok, message}
	})
	connector.(interface {
		SetDiagnosticReporter(health.DiagnosticReporter)
	}).SetDiagnosticReporter(func(diag health.Diagnostic) {
		if strings.Contains(diag.Detail+diag.Instrument+diag.Title+diag.Code, oaFakeKey) {
			t.Error("diagnostic exposed a credential")
		}
		diagnostics <- diag
	})
	return statuses, diagnostics
}

func oaNextStatus(t *testing.T, statuses <-chan oaStatus) oaStatus {
	t.Helper()
	select {
	case status := <-statuses:
		return status
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stream status")
		return oaStatus{}
	}
}

func oaNextDiagnostic(t *testing.T, diagnostics <-chan health.Diagnostic) health.Diagnostic {
	t.Helper()
	select {
	case diag := <-diagnostics:
		return diag
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stream diagnostic")
		return health.Diagnostic{}
	}
}

func TestOpenAlgoQuotesAndDuplicateSubscriptions(t *testing.T) {
	wsURL := oaServer(t, func(ctx context.Context, conn *websocket.Conn) {
		request, ok := oaHandshake(t, ctx, conn)
		if !ok {
			return
		}
		if len(request.Symbols) != 2 || request.Symbols[0] != (openalgo.Instrument{Exchange: "NSE", Symbol: "RELIANCE"}) ||
			request.Symbols[1] != (openalgo.Instrument{Exchange: "BSE", Symbol: "RELIANCE"}) {
			t.Error("subscription did not deduplicate exact instrument pairs")
			return
		}
		if !oaAck(t, ctx, conn, request) {
			return
		}
		for _, exchange := range []string{"NSE", "BSE"} {
			if conn.Write(ctx, websocket.MessageText, []byte(oaQuote("RELIANCE", exchange, "1187.750000000000000001"))) != nil {
				t.Error("could not send exact-price quote")
				return
			}
		}
		_, _, _ = conn.Read(ctx)
	})
	connector := oaNewConnector(t, "http://localhost:5000", wsURL)
	statuses, _ := oaReporters(t, connector)
	subs := []fwmarketdata.Subscription{
		{External: "NSE:RELIANCE", Base: 1001, Quote: 2001},
		{External: "NSE:RELIANCE", Base: 1002, Quote: 2002},
		{External: "BSE:RELIANCE", Base: 1003, Quote: 2003},
	}
	updates := oaSubscribe(t, connector, subs)
	if !oaNextStatus(t, statuses).ok {
		t.Fatal("authenticated and subscribed stream did not report connected")
	}
	for _, sub := range subs {
		update := oaNextUpdate(t, updates)
		if update.Base != sub.Base || update.Quote != sub.Quote || update.Mark != "1187.750000000000000001" ||
			update.Bid != "" || update.Ask != "" || !update.AsOf.Equal(time.UnixMilli(1712572800123)) {
			t.Fatal("quote lost exact mark, observation time or subscription identity")
		}
	}
	references, ok := connector.(fwmarketdata.Referenceable).References()
	if !ok || references.DocsURL != "https://docs.openalgo.in/api-documentation/v1/websockets" ||
		references.SymbolsURL != "https://docs.openalgo.in/symbol-format" {
		t.Fatal("OpenAlgo references changed")
	}
	connector.Close()
	if _, open := <-updates; open {
		t.Fatal("Close did not close quote channel")
	}
	connector.Close()
}

func TestOpenAlgoSubscribeRejectsInvalidSymbols(t *testing.T) {
	connector := oaNewConnector(t, "http://localhost:5000", "ws://localhost:8765")
	for _, external := range []string{"RELIANCE", "NFO:RELIANCE", "nse:A", "NSE:", "NSE: A"} {
		if _, err := connector.Subscribe(context.Background(), []fwmarketdata.Subscription{{External: external}}); err == nil ||
			err.Error() != fmt.Sprintf("openalgo subscription external symbol %q is invalid: %s", external, openalgo.SymbolForm) {
			t.Fatal("unsupported symbol accepted or form explanation changed")
		}
	}
	if _, err := connector.Subscribe(context.Background(), nil); err == nil {
		t.Fatal("empty subscription accepted")
	}
	for _, credentials := range []string{"", `{}`, `{"apiKey":"fake-key"}`} {
		if _, err := NewOpenAlgoConnector(domain.MarketDataInstance{Credentials: credentials}); err == nil {
			t.Fatal("missing credentials accepted")
		}
	}
}

func TestOpenAlgoQuoteHandshakeFailures(t *testing.T) {
	for _, name := range []string{"auth-refused", "auth-binary", "subscribe-error", "wrong-request-id", "subscribe-binary", "all-refused", "unrequested-refusal"} {
		t.Run(name, func(t *testing.T) {
			wsURL := oaServer(t, func(ctx context.Context, conn *websocket.Conn) {
				if strings.HasPrefix(name, "auth-") {
					var auth map[string]string
					if !oaRead(t, ctx, conn, &auth) {
						return
					}
					if name == "auth-binary" {
						if conn.Write(ctx, websocket.MessageBinary, []byte(`{"type":"auth","status":"success"}`)) != nil {
							t.Error("could not send binary auth")
						}
					} else {
						oaWrite(t, ctx, conn, map[string]string{"status": "error", "code": "AUTHENTICATION_ERROR", "message": oaFakeKey})
					}
					_, _, _ = conn.Read(ctx)
					return
				}
				request, ok := oaHandshake(t, ctx, conn)
				if !ok {
					return
				}
				switch name {
				case "subscribe-error":
					oaWrite(t, ctx, conn, map[string]string{"status": "error", "code": "BROKER_ERROR", "message": oaFakeKey, "request_id": request.RequestID})
				case "wrong-request-id":
					request.RequestID = "wrong"
					oaAck(t, ctx, conn, request)
				case "subscribe-binary":
					if conn.Write(ctx, websocket.MessageBinary, []byte(`{}`)) != nil {
						t.Error("could not send binary ack")
					}
				case "all-refused", "unrequested-refusal":
					symbol := "RELIANCE"
					if name == "unrequested-refusal" {
						symbol = "OTHER"
					}
					oaWrite(t, ctx, conn, map[string]any{"type": "subscribe", "status": "partial", "request_id": request.RequestID,
						"subscriptions": []map[string]string{{"exchange": "NSE", "symbol": symbol, "status": "error", "message": oaFakeKey}},
					})
				}
				_, _, _ = conn.Read(ctx)
			})
			connector := oaNewConnector(t, "http://localhost:5000", wsURL)
			statuses, diagnostics := oaReporters(t, connector)
			updates := oaSubscribe(t, connector, []fwmarketdata.Subscription{{External: "NSE:RELIANCE", Base: 1, Quote: 2}})
			status := oaNextStatus(t, statuses)
			if status.ok || status.message == "" {
				t.Fatal("failed handshake reported connected or lost its error")
			}
			switch name {
			case "unrequested-refusal":
				if !strings.Contains(status.message, "unexpected quote subscription reply") {
					t.Fatal("unrequested refusal did not report the parser error")
				}
			case "auth-refused", "subscribe-error":
				if !strings.Contains(status.message, "[redacted]") {
					t.Fatal("refusal message was not preserved with redaction")
				}
			case "all-refused":
				if !strings.Contains(status.message, "all quote subscriptions were refused") {
					t.Fatal("all-refused session did not fail explicitly")
				}
				diag := oaNextDiagnostic(t, diagnostics)
				if diag.Level != health.DiagError || diag.Code != health.CodeNoData ||
					diag.Kind != health.DiagKindProvider || diag.Instrument != "NSE:RELIANCE" ||
					diag.Title != "OpenAlgo refused quotes for this instrument" ||
					diag.Detail != "OpenAlgo refused the quote subscription, so Officer receives no quotes for this instrument until the stream reconnects or feeds restart. OpenAlgo: [redacted]" {
					t.Fatal("all-refused instrument did not reach operator")
				}
			}
			connector.Close()
			if _, open := <-updates; open {
				t.Fatal("failed handshake emitted a quote")
			}
		})
	}
}

func TestOpenAlgoQuotePartialAcknowledgement(t *testing.T) {
	wsURL := oaServer(t, func(ctx context.Context, conn *websocket.Conn) {
		request, ok := oaHandshake(t, ctx, conn)
		if !ok {
			return
		}
		if !oaWrite(t, ctx, conn, map[string]any{"type": "subscribe", "status": "partial", "request_id": request.RequestID,
			"subscriptions": []map[string]string{
				{"exchange": "NSE", "symbol": "RELIANCE", "status": "success"},
				{"exchange": "BSE", "symbol": "BAD1", "status": "error", "message": "denied " + oaFakeKey},
				{"exchange": "NSE", "symbol": "BAD2", "status": "error", "message": "not found"},
				{"exchange": "NSE", "symbol": "BAD3", "status": "error", "message": ""},
			},
		}) {
			return
		}
		for _, frame := range []string{oaQuote("BAD1", "BSE", "999"), oaQuote("RELIANCE", "NSE", "1187.75")} {
			if conn.Write(ctx, websocket.MessageText, []byte(frame)) != nil {
				t.Error("could not send partial-session frame")
				return
			}
		}
		_, _, _ = conn.Read(ctx)
	})
	connector := oaNewConnector(t, "http://localhost:5000", wsURL)
	statuses, diagnostics := oaReporters(t, connector)
	updates := oaSubscribe(t, connector, []fwmarketdata.Subscription{
		{External: "NSE:RELIANCE", Base: 1, Quote: 2},
		{External: "BSE:BAD1", Base: 3, Quote: 2},
		{External: "NSE:BAD2", Base: 4, Quote: 2},
		{External: "NSE:BAD3", Base: 5, Quote: 2},
	})
	if !oaNextStatus(t, statuses).ok {
		t.Fatal("partial acknowledgement stopped accepted instruments")
	}
	update := oaNextUpdate(t, updates)
	if update.Base != 1 || update.Mark != "1187.75" {
		t.Fatal("refused instrument emitted an update")
	}
	const detail = "OpenAlgo refused the quote subscription, so Officer receives no quotes for this instrument until the stream reconnects or feeds restart."
	for _, tc := range []struct {
		instrument, detail string
	}{
		{"BSE:BAD1", detail + " OpenAlgo: denied [redacted]"},
		{"NSE:BAD2", detail + " OpenAlgo: not found"},
		{"NSE:BAD3", detail},
	} {
		diag := oaNextDiagnostic(t, diagnostics)
		if diag.Level != health.DiagError || diag.Code != health.CodeNoData ||
			diag.Kind != health.DiagKindProvider || diag.Instrument != tc.instrument ||
			diag.Title != "OpenAlgo refused quotes for this instrument" || diag.Detail != tc.detail {
			t.Errorf("partial refusal diagnostic for %s = %+v, want provider no-data with detail %q", tc.instrument, diag, tc.detail)
		}
		if diag.Remediation != "Check the symbol and the broker's market-data access in OpenAlgo, use Verify symbol on this instrument, then Restart feeds." ||
			len(diag.Actions) != 2 || diag.Actions[0].Type != health.ActionRestart || diag.Actions[1].Type != health.ActionOpenDocs {
			t.Errorf("partial refusal for %s lost its remediation or actions: %+v", tc.instrument, diag)
		}
	}
	if diag := oaNextDiagnostic(t, diagnostics); diag.Level != health.DiagWarn ||
		diag.Code != health.CodeInconsistentData || diag.Kind != health.DiagKindProvider || diag.Instrument != "BSE:BAD1" {
		t.Fatal("refused instrument push was not skipped with a warning")
	}
	select {
	case <-diagnostics:
		t.Fatal("refused instrument produced duplicate diagnostics")
	default:
	}
}

func TestOpenAlgoQuoteSkippedFrames(t *testing.T) {
	const unparsableDetail = "The source is sending data but Officer could not decode it (likely a format mismatch)."
	for _, tc := range []struct {
		name, payload, detail, code, instrument string
		typ                                     websocket.MessageType
	}{
		{"missing-ltp", `{"type":"market_data","exchange":"NSE","symbol":"RELIANCE","data":{"timestamp":1712572800123}}`, unparsableDetail, health.CodeUnparsableData, "", websocket.MessageText},
		{"zero-ltp", oaQuote("RELIANCE", "NSE", "0"), unparsableDetail, health.CodeUnparsableData, "", websocket.MessageText},
		{"missing-timestamp", `{"type":"market_data","exchange":"NSE","symbol":"RELIANCE","data":{"ltp":999}}`, unparsableDetail, health.CodeUnparsableData, "", websocket.MessageText},
		{"missing-instrument", `{"type":"market_data","data":{"ltp":1}}`, unparsableDetail, health.CodeUnparsableData, "", websocket.MessageText},
		{"unknown-type", `{"type":"other"}`, unparsableDetail, health.CodeUnparsableData, "", websocket.MessageText},
		{"binary", oaQuote("RELIANCE", "NSE", "999"), unparsableDetail, health.CodeUnparsableData, "", websocket.MessageBinary},
		{"unsubscribed", oaQuote("OTHER", "NSE", "999"), "OpenAlgo's quote stream sent market data for an instrument this source did not subscribe to; Officer ignored it.", health.CodeInconsistentData, "NSE:OTHER", websocket.MessageText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wsURL := oaServer(t, func(ctx context.Context, conn *websocket.Conn) {
				request, ok := oaHandshake(t, ctx, conn)
				if !ok || !oaAck(t, ctx, conn, request) {
					return
				}
				count := unparsableThreshold + 1
				if tc.code == health.CodeInconsistentData {
					count = 1
				}
				for range count {
					if conn.Write(ctx, tc.typ, []byte(tc.payload)) != nil {
						t.Error("could not send skipped frame")
						return
					}
				}
				if conn.Write(ctx, websocket.MessageText, []byte(oaQuote("RELIANCE", "NSE", "1187.75"))) != nil {
					t.Error("could not send skipped-frame sequence")
					return
				}
				_, _, _ = conn.Read(ctx)
			})
			connector := oaNewConnector(t, "http://localhost:5000", wsURL)
			_, diagnostics := oaReporters(t, connector)
			updates := oaSubscribe(t, connector, []fwmarketdata.Subscription{{External: "NSE:RELIANCE", Base: 1, Quote: 2}})
			if oaNextUpdate(t, updates).Mark != "1187.75" {
				t.Fatal("invalid frame emitted an update before the barrier quote")
			}
			diag := oaNextDiagnostic(t, diagnostics)
			level := health.DiagError
			if tc.code == health.CodeInconsistentData {
				level = health.DiagWarn
			}
			if diag.Level != level || diag.Code != tc.code || diag.Kind != health.DiagKindProvider ||
				diag.Instrument != tc.instrument || diag.Detail != tc.detail {
				t.Fatal("skipped frame lost its diagnostic")
			}
			select {
			case <-diagnostics:
				t.Fatal("repeated skipped frames produced duplicate diagnostics")
			default:
			}
		})
	}
}

func TestOpenAlgoQuoteStreamError(t *testing.T) {
	wsURL := oaServer(t, func(ctx context.Context, conn *websocket.Conn) {
		request, ok := oaHandshake(t, ctx, conn)
		if !ok || !oaAck(t, ctx, conn, request) {
			return
		}
		oaWrite(t, ctx, conn, map[string]string{"status": "error", "code": "BROKER_ERROR", "message": oaFakeKey})
		_, _, _ = conn.Read(ctx)
	})
	connector := oaNewConnector(t, "http://localhost:5000", wsURL)
	statuses, _ := oaReporters(t, connector)
	updates := oaSubscribe(t, connector, []fwmarketdata.Subscription{{External: "NSE:RELIANCE"}})
	if !oaNextStatus(t, statuses).ok {
		t.Fatal("stream did not connect before error")
	}
	status := oaNextStatus(t, statuses)
	if status.ok || !strings.Contains(status.message, "BROKER_ERROR") || !strings.Contains(status.message, "[redacted]") {
		t.Fatal("stream error did not terminate session with a redacted status")
	}
	connector.Close()
	if _, open := <-updates; open {
		t.Fatal("error frame emitted a quote")
	}
}

func TestOpenAlgoQuoteReconnectResubscribes(t *testing.T) {
	var connections atomic.Int32
	requests := make(chan oaQuoteRequest, 4)
	wsURL := oaServer(t, func(ctx context.Context, conn *websocket.Conn) {
		attempt := connections.Add(1)
		request, ok := oaHandshake(t, ctx, conn)
		if !ok || !oaAck(t, ctx, conn, request) {
			return
		}
		requests <- request
		if conn.Write(ctx, websocket.MessageText, []byte(oaQuote("RELIANCE", "NSE", fmt.Sprint(attempt)))) != nil {
			t.Error("could not send reconnect quote")
			return
		}
		if attempt > 1 {
			_, _, _ = conn.Read(ctx)
		}
	})
	connector := oaNewConnector(t, "http://localhost:5000", wsURL)
	statuses, _ := oaReporters(t, connector)
	updates := oaSubscribe(t, connector, []fwmarketdata.Subscription{{External: "NSE:RELIANCE", Base: 11, Quote: 22}})
	for _, mark := range []string{"1", "2"} {
		update := oaNextUpdate(t, updates)
		if update.Mark != mark || update.Base != 11 || update.Quote != 22 {
			t.Fatal("reconnected stream lost subscription or quote")
		}
	}
	first, second := <-requests, <-requests
	if first.RequestID == second.RequestID || len(second.Symbols) != 1 || second.Symbols[0] != first.Symbols[0] {
		t.Fatal("reconnect did not resubscribe with a fresh request id")
	}
	if !oaNextStatus(t, statuses).ok || oaNextStatus(t, statuses).ok || !oaNextStatus(t, statuses).ok {
		t.Fatal("reconnect status sequence changed")
	}
}

func TestOpenAlgoMarketDataSymbolLookup(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["apikey"] != oaFakeKey || r.Method != http.MethodPost {
			t.Error("symbol lookup request shape changed")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/symbol":
			switch body["symbol"] {
			case "MISSING":
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"status":"error","message":"absent"}`))
			case "FAIL":
				w.WriteHeader(http.StatusBadGateway)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": oaFakeKey})
			default:
				if body["exchange"] != "NSE" || body["symbol"] != "RELIANCE" {
					t.Error("verification changed exchange or symbol")
				}
				_, _ = w.Write([]byte(`{"status":"success","data":{"symbol":"RELIANCE","exchange":"NSE","name":"Reliance","lotsize":1,"tick_size":"0.05"}}`))
			}
		case "/api/v1/search":
			exchange := body["exchange"]
			if exchange != "NSE" && exchange != "BSE" {
				t.Error("search escaped NSE/BSE scope")
			}
			if body["query"] == "FAIL" {
				w.WriteHeader(http.StatusBadGateway)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": oaFakeKey})
				return
			}
			if body["query"] == "WRONG" {
				exchange = "NFO"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": []map[string]string{{
				"symbol": "RELIANCE", "exchange": exchange, "name": "Reliance", "instrumenttype": "EQ",
			}}})
		default:
			t.Error("unexpected symbol lookup path")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	connector := oaNewConnector(t, server.URL, "ws://localhost:8765")
	verifier := connector.(fwmarketdata.SymbolVerifier)
	searcher := connector.(fwmarketdata.SymbolSearcher)
	ctx := context.Background()
	for _, external := range []string{"bad", "NFO:A"} {
		got, err := verifier.VerifySymbol(ctx, external)
		if err != nil || got.Exists || got.Details != openalgo.SymbolForm {
			t.Fatal("invalid symbol verification changed")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid symbols reached REST")
	}
	got, err := verifier.VerifySymbol(ctx, "NSE:RELIANCE")
	if err != nil || !got.Exists || got.Details != "Reliance; lot size 1; tick size 0.05" {
		t.Fatal("verification mapping changed")
	}
	got, err = verifier.VerifySymbol(ctx, "BSE:MISSING")
	if err != nil || got.Exists || got.Details != "" {
		t.Fatal("missing symbol did not map to absent")
	}
	if _, err = verifier.VerifySymbol(ctx, "NSE:FAIL"); err == nil || strings.Contains(err.Error(), oaFakeKey) {
		t.Fatal("verification failure was hidden or exposed a credential")
	}
	for _, tc := range []struct {
		query string
		count int
	}{
		{"RELI", 2}, {"NSE:RELI", 1}, {"BSE: RELI ", 1}, {" ", 0}, {"NSE: ", 0},
	} {
		before := calls.Load()
		matches, err := searcher.SearchSymbols(ctx, fwmarketdata.SymbolSearchQuery{Query: tc.query})
		if err != nil || len(matches) != tc.count || calls.Load()-before != int32(tc.count) {
			t.Fatal("search exchange selection changed")
		}
		for _, match := range matches {
			if match.Symbol != match.Exchange+":RELIANCE" || match.Name != "Reliance" || match.SecType != "EQ" {
				t.Fatal("search result mapping changed")
			}
		}
	}
	for _, query := range []string{"FAIL", "WRONG"} {
		if _, err := searcher.SearchSymbols(ctx, fwmarketdata.SymbolSearchQuery{Query: query}); err == nil || strings.Contains(err.Error(), oaFakeKey) {
			t.Fatal("search failure was hidden or exposed a credential")
		}
	}
}

func TestOpenAlgoQuoteIncompleteAcknowledgement(t *testing.T) {
	for _, kind := range []string{"missing", "extra", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			wsURL := oaServer(t, func(ctx context.Context, conn *websocket.Conn) {
				request, ok := oaHandshake(t, ctx, conn)
				if !ok {
					return
				}
				row := func(instrument openalgo.Instrument) map[string]string {
					return map[string]string{"exchange": instrument.Exchange, "symbol": instrument.Symbol, "status": "success"}
				}
				rows := []map[string]string{row(request.Symbols[0]), row(request.Symbols[1])}
				switch kind {
				case "missing":
					rows = rows[:1]
				case "extra":
					rows = append(rows, row(openalgo.Instrument{Exchange: "NSE", Symbol: "EXTRA"}))
				case "duplicate":
					rows[1] = row(request.Symbols[0])
				}
				if !oaWrite(t, ctx, conn, map[string]any{"type": "subscribe", "status": "success", "request_id": request.RequestID, "subscriptions": rows}) {
					return
				}
				_ = conn.Write(ctx, websocket.MessageText, []byte(oaQuote("RELIANCE", "NSE", "1187.75")))
			})
			connector := oaNewConnector(t, "http://localhost:5000", wsURL).(*openAlgoConnector)
			connector.sleep = func(ctx context.Context, _ time.Duration) error {
				<-ctx.Done()
				return ctx.Err()
			}
			statuses, _ := oaReporters(t, connector)
			updates := oaSubscribe(t, connector, []fwmarketdata.Subscription{{External: "NSE:RELIANCE"}, {External: "BSE:OTHER"}})
			status := oaNextStatus(t, statuses)
			if status.ok || !strings.Contains(status.message, "unexpected quote subscription reply") {
				t.Fatal("incomplete subscription acknowledgement was accepted")
			}
			select {
			case <-updates:
				t.Fatal("incomplete acknowledgement delivered a quote")
			default:
			}
		})
	}
}

func TestOpenAlgoQuoteLiveness(t *testing.T) {
	for _, answer := range []bool{true, false} {
		name := "stopped-pongs"
		if answer {
			name = "silent-market"
		}
		t.Run(name, func(t *testing.T) {
			var connections, pings atomic.Int32
			wsURL := oaServer(t, func(ctx context.Context, conn *websocket.Conn) {
				connections.Add(1)
				request, ok := oaHandshake(t, ctx, conn)
				if !ok || !oaAck(t, ctx, conn, request) {
					return
				}
				for {
					typ, payload, err := conn.Read(ctx)
					if err != nil {
						return
					}
					var ping struct {
						Action string `json:"action"`
					}
					if typ != websocket.MessageText || json.Unmarshal(payload, &ping) != nil || ping.Action != "ping" {
						t.Error("unexpected application heartbeat")
						return
					}
					n := pings.Add(1)
					if answer || n <= 3 {
						if conn.Write(ctx, websocket.MessageText, []byte(`{"type":"pong","status":"success","server_timestamp":1}`)) != nil {
							return
						}
					}
				}
			})
			connector := oaNewConnector(t, "http://localhost:5000", wsURL).(*openAlgoConnector)
			connector.pingInterval = 10 * time.Millisecond
			connector.readTimeout = 100 * time.Millisecond
			connector.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
			statuses, diagnostics := oaReporters(t, connector)
			updates := oaSubscribe(t, connector, []fwmarketdata.Subscription{{External: "NSE:RELIANCE"}})
			if !oaNextStatus(t, statuses).ok {
				t.Fatal("session did not connect")
			}
			if answer {
				timer := time.NewTimer(4 * connector.readTimeout)
				defer timer.Stop()
				select {
				case <-statuses:
					t.Fatal("healthy silent session changed status")
				case <-diagnostics:
					t.Fatal("pong generated a diagnostic")
				case <-updates:
					t.Fatal("pong generated a quote or closed the channel")
				case <-timer.C:
				}
				if connections.Load() != 1 || pings.Load() < 3 {
					t.Fatal("silent session reconnected or stopped sending pings")
				}
			} else {
				status := oaNextStatus(t, statuses)
				if status.ok || !strings.Contains(status.message, "websocket read timeout after") {
					t.Fatal("stopped pongs did not end the session with an idle timeout")
				}
				if !oaNextStatus(t, statuses).ok || connections.Load() < 2 {
					t.Fatal("session did not reconnect after stopped pongs")
				}
			}
			connector.Close()
			select {
			case <-diagnostics:
				t.Fatal("heartbeat produced a skipped-frame diagnostic")
			default:
			}
		})
	}
}

func TestOpenAlgoDiagnose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if r.URL.Path != "/api/v1/symbol" || json.NewDecoder(r.Body).Decode(&body) != nil || body["apikey"] != oaFakeKey {
			t.Error("unexpected diagnostic lookup")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch body["symbol"] {
		case "MISSING":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":"error","message":"absent"}`))
		case "FAIL":
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": oaFakeKey})
		case "RELIANCE":
			_, _ = w.Write([]byte(`{"status":"success","data":{"symbol":"RELIANCE","exchange":"NSE","name":"Reliance","lotsize":1,"tick_size":"0.05"}}`))
		default:
			t.Error("diagnostic lookup changed the symbol")
		}
	}))
	defer server.Close()
	wsURL := oaServer(t, func(ctx context.Context, conn *websocket.Conn) {
		request, ok := oaHandshake(t, ctx, conn)
		if ok && oaAck(t, ctx, conn, request) {
			_, _, _ = conn.Read(ctx)
		}
	})
	for _, tc := range []struct {
		symbol   string
		findings int
		fails    bool
	}{
		{"RELIANCE", 0, false}, {"MISSING", 1, false}, {"FAIL", 0, true},
	} {
		t.Run(tc.symbol, func(t *testing.T) {
			connector := oaNewConnector(t, server.URL, wsURL)
			if !connector.(fwmarketdata.SilenceTolerant).ToleratesSilence() {
				t.Fatal("closed market silence was not tolerated")
			}
			statuses, _ := oaReporters(t, connector)
			external := "NSE:" + tc.symbol
			oaSubscribe(t, connector, []fwmarketdata.Subscription{{External: external}})
			if !oaNextStatus(t, statuses).ok {
				t.Fatal("diagnostic session did not connect")
			}
			findings, err := connector.(fwmarketdata.Diagnosable).Diagnose(context.Background())
			if (err != nil) != tc.fails || len(findings) != tc.findings {
				t.Fatal("symbol diagnostic acceptance changed")
			}
			if err != nil && strings.Contains(err.Error(), oaFakeKey) {
				t.Fatal("diagnostic lookup error exposed a credential")
			}
			if tc.findings != 0 && (findings[0].Code != health.CodeUnknownSymbol || findings[0].Instrument != external) {
				t.Fatal("unknown symbol diagnostic lost its instrument")
			}
		})
	}
}

func TestOpenAlgoCloseWithUnreadUpdate(t *testing.T) {
	wsURL := oaServer(t, func(ctx context.Context, conn *websocket.Conn) {
		request, ok := oaHandshake(t, ctx, conn)
		if !ok || !oaAck(t, ctx, conn, request) {
			return
		}
		if conn.Write(ctx, websocket.MessageText, []byte(oaQuote("RELIANCE", "NSE", "1187.75"))) != nil {
			t.Error("could not send unread update")
			return
		}
		_, _, _ = conn.Read(ctx)
	})
	connector := oaNewConnector(t, "http://localhost:5000", wsURL)
	updates := oaSubscribe(t, connector, []fwmarketdata.Subscription{
		{External: "NSE:RELIANCE", Base: 1, Quote: 2},
		{External: "NSE:RELIANCE", Base: 3, Quote: 4},
	})
	oaNextUpdate(t, updates)
	closed := make(chan struct{})
	go func() {
		connector.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close blocked on an unread update")
	}
	if _, ok := <-updates; ok {
		t.Fatal("Close left an update or an open channel")
	}
}

func TestOpenAlgoReconnectBackoffResetsAfterDelivery(t *testing.T) {
	var connections atomic.Int32
	wsURL := oaServer(t, func(ctx context.Context, conn *websocket.Conn) {
		n := connections.Add(1)
		request, ok := oaHandshake(t, ctx, conn)
		if !ok || !oaAck(t, ctx, conn, request) {
			return
		}
		if n == 3 && conn.Write(ctx, websocket.MessageText, []byte(oaQuote("RELIANCE", "NSE", "1187.75"))) != nil {
			t.Error("could not send delivering-session quote")
		}
	})
	connector := oaNewConnector(t, "http://localhost:5000", wsURL).(*openAlgoConnector)
	delays := make(chan time.Duration)
	resume := make(chan struct{})
	connector.sleep = func(ctx context.Context, delay time.Duration) error {
		select {
		case delays <- delay:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-resume:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	updates := oaSubscribe(t, connector, []fwmarketdata.Subscription{{External: "NSE:RELIANCE"}})
	for i, expected := range []time.Duration{openAlgoReconnectMin, 2 * openAlgoReconnectMin, openAlgoReconnectMin} {
		if i == 2 && oaNextUpdate(t, updates).Mark != "1187.75" {
			t.Fatal("delivering session did not publish the quote")
		}
		select {
		case got := <-delays:
			if got != expected {
				t.Fatalf("retry %d delay = %s, want %s", i+1, got, expected)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("session did not schedule a reconnect")
		}
		if i < 2 {
			resume <- struct{}{}
		}
	}
}
