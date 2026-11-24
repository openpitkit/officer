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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/openalgo"
)

const openAlgoTestKey = "c5-openalgo-secret-marker"

type openAlgoTestReply struct {
	status int
	body   string
}

type openAlgoTestRequest struct {
	name   string
	fields map[string]string
}

type openAlgoTestVenue struct {
	c           *openAlgoConnector
	url         string
	mu          sync.Mutex
	requests    []openAlgoTestRequest
	diagnostics []health.Diagnostic
}

func openAlgoAssertSafe(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, openAlgoTestKey) {
		t.Fatal("OpenAlgo credential leaked into display text")
	}
}

func openAlgoAssertError(t *testing.T, err, sentinel error, text string) {
	t.Helper()
	if err == nil {
		if sentinel != nil || text != "" {
			t.Fatal("expected an OpenAlgo error")
		}
		return
	}
	openAlgoAssertSafe(t, err.Error())
	if sentinel == nil && text == "" {
		t.Fatalf("unexpected OpenAlgo error: %v", err)
	}
	if sentinel != nil && !errors.Is(err, sentinel) {
		t.Fatalf("error %v does not wrap %v", err, sentinel)
	}
	if text != "" && !strings.Contains(err.Error(), text) {
		t.Fatalf("error %v does not contain %q", err, text)
	}
}

func openAlgoTestCredentials(address string) string {
	return fmt.Sprintf(`{"url":%q,"wsUrl":"ws://127.0.0.1:8765","apiKey":%q}`, address, openAlgoTestKey)
}

func openAlgoTestOrder() fwtrading.Order {
	return fwtrading.Order{
		ClientOrderID: "officer-client-id", Symbol: "NSE:RELIANCE",
		BaseAsset: "RELIANCE", QuoteAsset: "INR", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, Quantity: "2.000",
		Route: `{"product":"CNC"}`,
	}
}

func openAlgoTestStatus(id, state string) string {
	return fmt.Sprintf(`{"status":"success","mode":"analyze","data":{"orderid":%q,"symbol":"RELIANCE","exchange":"NSE","action":"BUY","quantity":2,"order_status":%q}}`, id, state)
}

func newOpenAlgoTestVenue(t *testing.T, mode domain.TradingMode, overrides map[string]openAlgoTestReply) *openAlgoTestVenue {
	t.Helper()
	v := &openAlgoTestVenue{}
	analyze := mode == domain.TradingModePaper
	analyzerMode, sendMode := "live", ""
	if analyze {
		analyzerMode, sendMode = "analyze", "analyze"
	}
	replies := map[string]openAlgoTestReply{
		"ping":        {200, `{"status":"success","data":{"broker":"test-broker"}}`},
		"analyzer":    {200, fmt.Sprintf(`{"status":"success","data":{"analyze_mode":%t,"mode":%q}}`, analyze, analyzerMode)},
		"placeorder":  {200, fmt.Sprintf(`{"status":"success","orderid":"123","mode":%q}`, sendMode)},
		"orderstatus": {200, strings.Replace(openAlgoTestStatus("123", "complete"), `"mode":"analyze"`, fmt.Sprintf(`"mode":%q`, sendMode), 1)},
		"tradebook":   {200, fmt.Sprintf(`{"status":"success","mode":%q,"data":[{"orderid":"123","symbol":"RELIANCE","exchange":"NSE","action":"BUY","quantity":2,"average_price":100}]}`, sendMode)},
		"symbol":      {200, `{"status":"success","data":{"symbol":"RELIANCE","exchange":"NSE","name":"Reliance","instrumenttype":"EQ","lotsize":2,"tick_size":"0.05"}}`},
	}
	for name, reply := range overrides {
		replies[name] = reply
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/")
		var fields map[string]string
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" ||
			json.NewDecoder(r.Body).Decode(&fields) != nil {
			t.Error("invalid fake OpenAlgo request")
			w.WriteHeader(500)
			return
		}
		if fields["apikey"] != openAlgoTestKey {
			t.Error("fake OpenAlgo request has incorrect authentication")
		}
		v.mu.Lock()
		v.requests = append(v.requests, openAlgoTestRequest{name, fields})
		v.mu.Unlock()
		reply, exists := replies[name]
		if !exists && name == "search" {
			reply = openAlgoTestReply{200, fmt.Sprintf(`{"status":"success","data":[{"symbol":"RELIANCE","exchange":%q,"name":"Reliance","instrumenttype":"EQ"}]}`, fields["exchange"])}
			exists = true
		}
		if !exists {
			t.Error("unexpected fake OpenAlgo endpoint")
			w.WriteHeader(500)
			return
		}
		if reply.status == -1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error("hijack fake OpenAlgo connection failed")
				return
			}
			if err := conn.Close(); err != nil {
				t.Error("close fake OpenAlgo connection failed")
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		if _, err := io.WriteString(w, reply.body); err != nil {
			t.Error("write fake OpenAlgo response failed")
		}
	}))
	t.Cleanup(server.Close)
	v.url = server.URL
	var err error
	v.c, err = newOpenAlgoConnector(domain.TradingConnection{
		Mode: mode, Credentials: openAlgoTestCredentials(server.URL + "///"),
	})
	openAlgoAssertError(t, err, nil, "")
	v.c.SetDiagnosticReporter(func(diag health.Diagnostic) {
		encoded, err := json.Marshal(diag)
		if err != nil {
			t.Fatal("encode fake OpenAlgo diagnostic failed")
		}
		openAlgoAssertSafe(t, string(encoded))
		v.diagnostics = append(v.diagnostics, diag)
	})
	return v
}

func (v *openAlgoTestVenue) recorded() []openAlgoTestRequest {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]openAlgoTestRequest(nil), v.requests...)
}

func openAlgoAssertRequests(t *testing.T, v *openAlgoTestVenue, names ...string) []openAlgoTestRequest {
	t.Helper()
	requests := v.recorded()
	if len(requests) != len(names) {
		t.Fatalf("got %d requests, want %d", len(requests), len(names))
	}
	for i, name := range names {
		if requests[i].name != name {
			t.Fatalf("request %d: got %s, want %s", i, requests[i].name, name)
		}
	}
	return requests
}

func openAlgoAssertDiagnostic(t *testing.T, v *openAlgoTestVenue, code, kind, title, instrument string) health.Diagnostic {
	t.Helper()
	if len(v.diagnostics) != 1 {
		t.Fatalf("got %d diagnostics, want one", len(v.diagnostics))
	}
	diag := v.diagnostics[0]
	if diag.Level != health.DiagError || diag.Code != code || diag.Kind != kind ||
		diag.Title != title || diag.Instrument != instrument || diag.Detail == "" || diag.Remediation == "" {
		t.Fatal("OpenAlgo diagnostic contract changed")
	}
	return diag
}

func TestOpenAlgoConstructor(t *testing.T) {
	for _, tc := range []struct {
		name, credentials, pointer string
		mode                       domain.TradingMode
	}{
		{"bad_mode_before_credentials", "{", "", "bad-mode"},
		{"missing_key", `{"url":"http://localhost","wsUrl":"ws://localhost"}`, "/credentials/apiKey", domain.TradingModePaper},
		{"bad_url", `{"url":"invalid","wsUrl":"ws://localhost","apiKey":"` + openAlgoTestKey + `"}`, "/credentials/url", domain.TradingModeLive},
		{"bad_ws_url", `{"url":"http://localhost","wsUrl":"https://localhost","apiKey":"` + openAlgoTestKey + `"}`, "/credentials/wsUrl", domain.TradingModeLive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := newOpenAlgoConnector(domain.TradingConnection{Mode: tc.mode, Credentials: tc.credentials})
			openAlgoAssertError(t, err, nil, "openalgo")
			if c != nil || domain.ValidationPointer(err) != tc.pointer {
				t.Fatal("constructor did not preserve mode precedence or credential pointer")
			}
			if tc.pointer != "" && !errors.Is(err, domain.ErrInvalid) {
				t.Fatal("invalid credentials lost validation sentinel")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"status":"error","message":%q}`,
			"http://"+r.Host+" ws://127.0.0.1:8765 "+openAlgoTestKey)
	}))
	t.Cleanup(server.Close)
	c, err := newOpenAlgoConnector(domain.TradingConnection{
		Mode: domain.TradingModePaper, Credentials: openAlgoTestCredentials(server.URL),
	})
	openAlgoAssertError(t, err, nil, "")
	_, err = c.CheckAccount(context.Background())
	openAlgoAssertError(t, err, nil, "openalgo")
	if !strings.Contains(err.Error(), server.URL) || !strings.Contains(err.Error(), "ws://127.0.0.1:8765") {
		t.Fatal("connector redacted a non-secret address")
	}
}

func TestOpenAlgoCheckAccount(t *testing.T) {
	for _, mode := range []domain.TradingMode{domain.TradingModePaper, domain.TradingModeLive} {
		t.Run(string(mode), func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, mode, nil)
			info, err := v.c.CheckAccount(context.Background())
			openAlgoAssertError(t, err, nil, "")
			status := "live"
			if mode == domain.TradingModePaper {
				status = "analyze"
			}
			if info.Identity != "test-broker "+status+" "+v.url || info.Status != status {
				t.Fatal("wrong account identity or status")
			}
			openAlgoAssertRequests(t, v, "ping", "analyzer")
		})
	}
	for _, tc := range []struct {
		name, endpoint, body, message string
		mode                          domain.TradingMode
		status                        int
	}{
		{"paper_mismatch", "analyzer", `{"status":"success","data":{"analyze_mode":false,"mode":"live"}}`, "switch OpenAlgo to analyzer mode", domain.TradingModePaper, 200},
		{"live_mismatch", "analyzer", `{"status":"success","data":{"analyze_mode":true,"mode":"analyze"}}`, "switch OpenAlgo to live mode", domain.TradingModeLive, 200},
		{"empty_broker", "ping", `{"status":"success","data":{"broker":""}}`, "broker is required", domain.TradingModeLive, 200},
		{"blank_broker", "ping", `{"status":"success","data":{"broker":" "}}`, "broker is required", domain.TradingModeLive, 200},
		{"ping_error", "ping", `{"status":"error","message":"` + openAlgoTestKey + `"}`, "openalgo", domain.TradingModePaper, 503},
		{"analyzer_error", "analyzer", `{"status":"error","message":"` + openAlgoTestKey + `"}`, "openalgo", domain.TradingModePaper, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, tc.mode, map[string]openAlgoTestReply{tc.endpoint: {tc.status, tc.body}})
			info, err := v.c.CheckAccount(context.Background())
			openAlgoAssertError(t, err, nil, tc.message)
			if info != (fwtrading.AccountInfo{}) || errors.Is(err, fwtrading.ErrRejected) || errors.Is(err, fwtrading.ErrUnsupported) {
				t.Fatal("account error returned an identity or an order refusal")
			}
			if tc.endpoint == "ping" {
				openAlgoAssertRequests(t, v, "ping")
			} else {
				openAlgoAssertRequests(t, v, "ping", "analyzer")
			}
		})
	}
}

func TestOpenAlgoOrderRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, field, value, message string
		sentinel                    error
	}{
		{"route_empty", "route", "", "route requires product CNC or MIS", fwtrading.ErrRejected},
		{"route_invalid", "route", "{", "route requires product CNC or MIS", fwtrading.ErrRejected},
		{"route_null", "route", "null", "route requires product CNC or MIS", fwtrading.ErrRejected},
		{"route_array", "route", "[]", "route requires product CNC or MIS", fwtrading.ErrRejected},
		{"route_missing", "route", "{}", "route requires product CNC or MIS", fwtrading.ErrRejected},
		{"route_unknown", "route", `{"product":"CNC","other":"x"}`, "route requires product CNC or MIS", fwtrading.ErrRejected},
		{"product_wrong", "route", `{"product":"NRML"}`, "route requires product CNC or MIS", fwtrading.ErrRejected},
		{"product_number", "route", `{"product":1}`, "route requires product CNC or MIS", fwtrading.ErrRejected},
		{"product_null", "route", `{"product":null}`, "route requires product CNC or MIS", fwtrading.ErrRejected},
		{"product_blank", "route", `{"product":" CNC "}`, "route requires product CNC or MIS", fwtrading.ErrRejected},
		{"amount_kind", "amount", "quote", "quantity orders", fwtrading.ErrUnsupported},
		{"quote_asset", "quote", "USD", "INR", fwtrading.ErrUnsupported},
		{"symbol_unqualified", "symbol", "RELIANCE", "NSE:SYMBOL", fwtrading.ErrRejected},
		{"symbol_empty_exchange", "symbol", ":RELIANCE", "NSE:SYMBOL", fwtrading.ErrRejected},
		{"symbol_empty_symbol", "symbol", "NSE:", "NSE:SYMBOL", fwtrading.ErrRejected},
		{"symbol_spaced_exchange", "symbol", "NSE :RELIANCE", "NSE:SYMBOL", fwtrading.ErrRejected},
		{"symbol_spaced_symbol", "symbol", "NSE: RELIANCE", "NSE:SYMBOL", fwtrading.ErrRejected},
		{"symbol_trailing_space", "symbol", "NSE:RELIANCE ", "NSE:SYMBOL", fwtrading.ErrRejected},
		{"exchange_unsupported", "symbol", "NFO:RELIANCE", openalgo.SymbolForm, fwtrading.ErrRejected},
		{"exchange_mcx", "symbol", "MCX:GOLD", openalgo.SymbolForm, fwtrading.ErrRejected},
		{"side_unsupported", "side", "invalid", "buy or sell", fwtrading.ErrUnsupported},
		{"quantity_empty", "quantity", "", "quantity must be a positive", fwtrading.ErrRejected},
		{"quantity_invalid", "quantity", openAlgoTestKey, "quantity must be a positive", fwtrading.ErrRejected},
		{"quantity_zero", "quantity", "0", "quantity must be a positive", fwtrading.ErrRejected},
		{"quantity_negative", "quantity", "-2", "quantity must be a positive", fwtrading.ErrRejected},
		{"quantity_exponent", "quantity", "1e2", "positive plain decimal", fwtrading.ErrRejected},
		{"quantity_huge_exponent", "quantity", "1e2147483647", "positive plain decimal", fwtrading.ErrRejected},
		{"quantity_fraction", "quantity", "1.5", "positive whole number", fwtrading.ErrRejected},
		{"price_invalid", "price", openAlgoTestKey, "limit price must be a positive", fwtrading.ErrRejected},
		{"price_zero", "price", "0", "limit price must be a positive", fwtrading.ErrRejected},
		{"price_negative", "price", "-1", "limit price must be a positive", fwtrading.ErrRejected},
		{"price_exponent", "price", "1e-2147483647", "positive plain decimal", fwtrading.ErrRejected},
		{"price_small_exponent", "price", "1e2", "positive plain decimal", fwtrading.ErrRejected},
	} {
		for _, method := range []string{"precheck", "send"} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				v := newOpenAlgoTestVenue(t, domain.TradingModePaper, nil)
				order := openAlgoTestOrder()
				switch tc.field {
				case "route":
					order.Route = tc.value
				case "amount":
					order.AmountKind = domain.OrderAmountKind(tc.value)
				case "quote":
					order.QuoteAsset = tc.value
				case "symbol":
					order.Symbol = tc.value
				case "side":
					order.Side = domain.OrderSide(tc.value)
				case "quantity":
					order.Quantity = tc.value
				case "price":
					order.LimitPrice = tc.value
				}
				var err error
				if method == "precheck" {
					err = v.c.PreCheck(context.Background(), order)
				} else {
					var ack fwtrading.Ack
					ack, err = v.c.Send(context.Background(), order)
					if ack != (fwtrading.Ack{}) {
						t.Fatal("refused input returned an Ack")
					}
				}
				openAlgoAssertError(t, err, tc.sentinel, tc.message)
				other := fwtrading.ErrRejected
				if tc.sentinel == fwtrading.ErrRejected {
					other = fwtrading.ErrUnsupported
				}
				if errors.Is(err, other) || len(v.diagnostics) != 0 {
					t.Fatal("local refusal used another sentinel or reported a diagnostic")
				}
				openAlgoAssertRequests(t, v)
			})
		}
	}
}

func TestOpenAlgoPreCheck(t *testing.T) {
	for _, tc := range []struct {
		name, quantity, price, lot, tick, message string
		sentinel                                  error
	}{
		{"market", "2", "", "2", "0.05", "", nil},
		{"limit", "4", "100.10", "2", "0.05", "", nil},
		{"lot_multiple", "3", "", "2", "0.05", "lot size 2", fwtrading.ErrRejected},
		{"tick_multiple", "2", "100.11", "2", "0.05", "tick size 0.05", fwtrading.ErrRejected},
		{"lot_zero", "2", "", "0", "0.05", "venue lot size", nil},
		{"lot_negative", "2", "", "-2", "0.05", "venue lot size", nil},
		{"lot_null", "2", "", "", "0.05", "venue lot size", nil},
		{"tick_zero", "2", "", "2", "0", "venue tick size", nil},
		{"tick_negative", "2", "", "2", "-0.05", "venue tick size", nil},
		{"tick_null", "2", "", "2", "", "venue tick size", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lot, tick := "null", "null"
			if tc.lot != "" {
				lot = fmt.Sprintf("%q", tc.lot)
			}
			if tc.tick != "" {
				tick = fmt.Sprintf("%q", tc.tick)
			}
			body := fmt.Sprintf(`{"status":"success","data":{"lotsize":%s,"tick_size":%s}}`, lot, tick)
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, map[string]openAlgoTestReply{"symbol": {200, body}})
			order := openAlgoTestOrder()
			order.Quantity, order.LimitPrice = tc.quantity, tc.price
			err := v.c.PreCheck(context.Background(), order)
			openAlgoAssertError(t, err, tc.sentinel, tc.message)
			if tc.sentinel == nil && errors.Is(err, fwtrading.ErrRejected) {
				t.Fatal("venue metadata error incorrectly rejected the order")
			}
			requests := openAlgoAssertRequests(t, v, "analyzer", "symbol")
			if requests[1].fields["symbol"] != "RELIANCE" || requests[1].fields["exchange"] != "NSE" {
				t.Fatal("PreCheck queried the wrong symbol")
			}
		})
	}
	for _, tc := range []struct {
		name, endpoint, body, message string
		status                        int
		sentinel                      error
	}{
		{"unknown_symbol", "symbol", `{"status":"error","message":"` + openAlgoTestKey + `"}`, "does not know the symbol", 404, fwtrading.ErrRejected},
		{"symbol_error", "symbol", `{"status":"error","message":"` + openAlgoTestKey + `"}`, "openalgo", 500, nil},
		{"invalid_lot", "symbol", `{"status":"success","data":{"lotsize":"` + openAlgoTestKey + `","tick_size":1}}`, "decimal string", 200, nil},
		{"invalid_tick", "symbol", `{"status":"success","data":{"lotsize":1,"tick_size":"` + openAlgoTestKey + `"}}`, "decimal string", 200, nil},
		{"mismatch", "analyzer", `{"status":"success","data":{"analyze_mode":false,"mode":"live"}}`, "switch OpenAlgo to analyzer mode", 200, nil},
		{"analyzer_error", "analyzer", `{"status":"error","message":"` + openAlgoTestKey + `"}`, "openalgo", 503, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, map[string]openAlgoTestReply{tc.endpoint: {tc.status, tc.body}})
			err := v.c.PreCheck(context.Background(), openAlgoTestOrder())
			openAlgoAssertError(t, err, tc.sentinel, tc.message)
			if tc.sentinel == nil && (errors.Is(err, fwtrading.ErrRejected) || errors.Is(err, fwtrading.ErrUnsupported)) {
				t.Fatal("upstream PreCheck error became a definitive refusal")
			}
			if tc.endpoint == "analyzer" {
				openAlgoAssertRequests(t, v, "analyzer")
			} else {
				openAlgoAssertRequests(t, v, "analyzer", "symbol")
			}
		})
	}
}

func TestOpenAlgoSymbolFirstSeparator(t *testing.T) {
	v := newOpenAlgoTestVenue(t, domain.TradingModePaper, nil)
	order := openAlgoTestOrder()
	order.Symbol = "NSE:RELIANCE:SERIES"
	_, err := v.c.Send(context.Background(), order)
	openAlgoAssertError(t, err, nil, "")
	requests := openAlgoAssertRequests(t, v, "placeorder")
	if requests[0].fields["exchange"] != "NSE" || requests[0].fields["symbol"] != "RELIANCE:SERIES" {
		t.Fatal("external symbol was not split on its first separator")
	}
}

func TestOpenAlgoSendClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"400", 400, `{"status":"error","message":"` + openAlgoTestKey + `"}`},
		{"403", 403, `{"status":"error","message":"` + openAlgoTestKey + `"}`},
		{"404", 404, `{"status":"error","message":"` + openAlgoTestKey + `"}`},
		{"429", 429, `{"status":"error","message":"` + openAlgoTestKey + `"}`},
		{"408", 408, `{"status":"error","message":"` + openAlgoTestKey + `"}`},
		{"500", 500, `{"status":"error","message":"` + openAlgoTestKey + `"}`},
		{"502", 502, `{"status":"error","message":"` + openAlgoTestKey + `"}`},
		{"transport", -1, ""},
		{"error_envelope_200", 200, `{"status":"error","message":"` + openAlgoTestKey + `"}`},
		{"decode", 200, `{"status":"success","orderid":{}}`},
		{"empty_id", 200, `{"status":"success"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, map[string]openAlgoTestReply{"placeorder": {tc.status, tc.body}})
			ack, err := v.c.Send(context.Background(), openAlgoTestOrder())
			rejected := tc.status >= 400 && tc.status < 500 && tc.status != 408
			if rejected {
				openAlgoAssertError(t, err, fwtrading.ErrRejected, "")
			} else {
				openAlgoAssertError(t, err, nil, "openalgo: send outcome unknown:")
			}
			if errors.Is(err, fwtrading.ErrRejected) != rejected || errors.Is(err, fwtrading.ErrUnsupported) || ack != (fwtrading.Ack{}) {
				t.Fatal("incorrect send classification or unexpected Ack")
			}
			if len(v.diagnostics) != 0 {
				t.Fatal("generic send error reported an unexpected diagnostic")
			}
			openAlgoAssertRequests(t, v, "placeorder")
		})
	}
}

func TestOpenAlgoUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("closed fake OpenAlgo received a request")
	}))
	server.Close()
	c, err := newOpenAlgoConnector(domain.TradingConnection{
		Mode: domain.TradingModePaper, Credentials: openAlgoTestCredentials(server.URL),
	})
	openAlgoAssertError(t, err, nil, "")
	c.SetDiagnosticReporter(func(health.Diagnostic) {
		t.Error("unreachable OpenAlgo produced a diagnostic")
	})
	t.Run("send", func(t *testing.T) {
		ack, err := c.Send(context.Background(), openAlgoTestOrder())
		openAlgoAssertError(t, err, nil, "send outcome unknown")
		var opErr *net.OpError
		if !errors.As(err, &opErr) || opErr.Op != "dial" ||
			errors.Is(err, fwtrading.ErrRejected) ||
			errors.Is(err, fwtrading.ErrUnsupported) || ack != (fwtrading.Ack{}) {
			t.Fatal("unknown send outcome lost its dial cause or classification")
		}
	})
	t.Run("lookup", func(t *testing.T) {
		_, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{
			VenueOrderID: "123", Symbol: "NSE:RELIANCE",
		})
		openAlgoAssertError(t, err, nil, "openalgo")
		if !found || errors.Is(err, openalgo.ErrInvalidResponse) {
			t.Fatal("transport failure changed lookup semantics")
		}
	})
}

func TestOpenAlgoSendModesAndFields(t *testing.T) {
	for _, tc := range []struct {
		name, echo string
		mode       domain.TradingMode
		mismatch   bool
	}{
		{"paper_matching", "analyze", domain.TradingModePaper, false},
		{"live_matching", "", domain.TradingModeLive, false},
		{"paper_live_echo", "", domain.TradingModePaper, true},
		{"live_analyze_echo", "analyze", domain.TradingModeLive, true},
		{"paper_unknown_echo", openAlgoTestKey, domain.TradingModePaper, true},
		{"live_unknown_echo", openAlgoTestKey, domain.TradingModeLive, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"status":"success","orderid":123,"mode":%q}`, tc.echo)
			v := newOpenAlgoTestVenue(t, tc.mode, map[string]openAlgoTestReply{"placeorder": {200, body}})
			order := openAlgoTestOrder()
			if tc.mode == domain.TradingModeLive {
				order.Symbol, order.Side, order.LimitPrice, order.Route = "BSE:RELIANCE", domain.OrderSideSell, "100.100", `{"product":"MIS"}`
			}
			ack, err := v.c.Send(context.Background(), order)
			openAlgoAssertError(t, err, nil, "")
			if ack.VenueOrderID != "123" {
				t.Fatal("send did not preserve the existing order's Ack")
			}
			if tc.mismatch {
				diag := openAlgoAssertDiagnostic(t, v, health.CodeInvalidProviderConfig, health.DiagKindConfig,
					"OpenAlgo order mode differs from the connection", "")
				consequence := "the order may be a sandbox order."
				if tc.mode == domain.TradingModePaper {
					consequence = "the order may be live at the broker."
				}
				if !strings.Contains(diag.Detail, consequence) || !strings.HasPrefix(diag.Remediation, "Officer recorded the order as sent.") {
					t.Fatal("mode mismatch diagnostic lost its consequence or recorded-send explanation")
				}
			} else if len(v.diagnostics) != 0 {
				t.Fatal("matching mode produced a diagnostic")
			}
			requests := openAlgoAssertRequests(t, v, "placeorder")
			want := map[string]string{
				"apikey": openAlgoTestKey, "strategy": "Pit Officer", "symbol": "RELIANCE",
				"exchange": "NSE", "action": "BUY", "pricetype": "MARKET", "product": "CNC", "quantity": "2",
			}
			if tc.mode == domain.TradingModeLive {
				want["exchange"], want["action"], want["pricetype"], want["product"], want["price"] = "BSE", "SELL", "LIMIT", "MIS", "100.1"
			}
			if !reflect.DeepEqual(requests[0].fields, want) {
				t.Fatal("OpenAlgo placeorder fields differ from the normalized order")
			}
		})
	}
}

func TestOpenAlgoSendSemiAuto(t *testing.T) {
	for _, id := range []string{"", `,"orderid":"123"`} {
		t.Run(fmt.Sprint(id != ""), func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, domain.TradingModeLive, map[string]openAlgoTestReply{
				"placeorder": {200, `{"status":"success","mode":"semi_auto","pending_order_id":17` + id + `}`},
			})
			ack, err := v.c.Send(context.Background(), openAlgoTestOrder())
			openAlgoAssertError(t, err, nil, "send outcome unknown:")
			if ack != (fwtrading.Ack{}) || errors.Is(err, fwtrading.ErrRejected) || errors.Is(err, fwtrading.ErrUnsupported) {
				t.Fatal("semi-auto order did not remain outcome unknown")
			}
			diag := openAlgoAssertDiagnostic(t, v, health.CodeInvalidProviderConfig, health.DiagKindConfig,
				"OpenAlgo queued the order for approval", "")
			if !strings.Contains(diag.Detail, "Action Center") || !strings.Contains(diag.Remediation, "switch OpenAlgo to auto mode") {
				t.Fatal("semi-auto diagnostic lost its explanation or remediation")
			}
			openAlgoAssertRequests(t, v, "placeorder")
		})
	}
}

func TestOpenAlgoNilReporter(t *testing.T) {
	for _, method := range []string{"send", "lookup"} {
		t.Run(method, func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, nil)
			v.c.SetDiagnosticReporter(nil)
			if method == "send" {
				order := openAlgoTestOrder()
				order.Route = "invalid"
				ack, err := v.c.Send(context.Background(), order)
				openAlgoAssertError(t, err, fwtrading.ErrUnsupported, "diagnostic reporter")
				if ack != (fwtrading.Ack{}) || errors.Is(err, fwtrading.ErrRejected) {
					t.Fatal("nil reporter was not refused before order validation")
				}
			} else {
				_, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{VenueOrderID: "123"})
				openAlgoAssertError(t, err, nil, "diagnostic reporter")
				if !found {
					t.Fatal("Lookup returned found=false")
				}
			}
			openAlgoAssertRequests(t, v)
		})
	}
}

func TestOpenAlgoLookup(t *testing.T) {
	for _, tc := range []struct {
		name, id, state, trades, message, title, code string
		sentinel                                      error
		wantStatus                                    fwtrading.VenueStatus
	}{
		{"complete", "123", "complete", "", "", "", "", nil, fwtrading.VenueStatusFilled},
		{"open", "123", "open", "", "", "", "", nil, fwtrading.VenueStatusOpen},
		{"cancelled", "123", "cancelled", "", "", "OpenAlgo fills of a closed order are unconfirmed", health.CodeInconsistentData, errOpenAlgoCancelledFillsUnconfirmed, ""},
		{"incomplete", "123", "complete", `{"status":"success","mode":"analyze","data":[]}`, "", "OpenAlgo fills of a complete order are missing", health.CodeInconsistentData, errOpenAlgoFillsIncomplete, ""},
		{"unknown_status", "123", openAlgoTestKey, "", "unknown order status", "Unknown order data from the venue", health.CodeUnparsableData, domain.ErrUpstream, ""},
		{"id_mismatch", "456", "complete", "", "order id mismatch", "Unknown order data from the venue", health.CodeUnparsableData, domain.ErrUpstream, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			overrides := map[string]openAlgoTestReply{"orderstatus": {200, openAlgoTestStatus(tc.id, tc.state)}}
			if tc.trades != "" {
				overrides["tradebook"] = openAlgoTestReply{200, tc.trades}
			}
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, overrides)
			ref := fwtrading.OrderRef{VenueOrderID: "123", Symbol: "NSE:RELIANCE"}
			snapshot, found, err := v.c.Lookup(context.Background(), ref)
			openAlgoAssertError(t, err, tc.sentinel, tc.message)
			if !found {
				t.Fatal("Lookup returned found=false")
			}
			if tc.wantStatus != "" {
				if snapshot.VenueOrderID != "123" || snapshot.Status != tc.wantStatus {
					t.Fatal("wrong lookup snapshot identity or status")
				}
				if tc.wantStatus == fwtrading.VenueStatusFilled {
					if snapshot.FilledQuantity != "2" || len(snapshot.Fills) != 1 || snapshot.Fills[0].Price != "100" {
						t.Fatal("complete lookup lost the exact tradebook fill")
					}
				} else if snapshot.FilledQuantity != "0" || len(snapshot.Fills) != 0 {
					t.Fatal("open lookup fabricated fills")
				}
			}
			if tc.title != "" {
				diag := openAlgoAssertDiagnostic(t, v, tc.code, health.DiagKindProvider, tc.title, ref.Symbol)
				if tc.name == "cancelled" &&
					(!strings.Contains(diag.Detail, "cannot confirm its filled quantity against the trade book") ||
						!strings.Contains(diag.Remediation, "filled quantity OpenAlgo's order stream reported")) {
					t.Fatal("closed-order diagnostic lost the stream confirmation boundary")
				}
				if tc.name == "id_mismatch" && diag.Detail != "OpenAlgo order lookup returned a different order id." {
					t.Fatal("order id mismatch lost its fixed diagnostic detail")
				}
				if tc.name == "unknown_status" && diag.Detail != "OpenAlgo order lookup returned order or trade data Officer cannot interpret." {
					t.Fatal("unparsable snapshot lost its fixed diagnostic detail")
				}
			} else if len(v.diagnostics) != 0 {
				t.Fatal("retry or successful lookup produced a diagnostic")
			}
			if tc.name == "id_mismatch" {
				openAlgoAssertRequests(t, v, "orderstatus")
			} else {
				openAlgoAssertRequests(t, v, "orderstatus", "tradebook")
			}
			if v.recorded()[0].fields["orderid"] != ref.VenueOrderID {
				t.Fatal("Lookup requested the wrong order id")
			}
		})
	}
}

func openAlgoAssertLookupRemediation(t *testing.T, remediation string, mode domain.TradingMode) {
	t.Helper()
	if mode == domain.TradingModePaper {
		if !strings.Contains(remediation, "OpenAlgo") ||
			strings.Contains(strings.ToLower(remediation), "broker") {
			t.Fatal("paper lookup remediation must point to OpenAlgo")
		}
	} else if !strings.Contains(remediation, "at the broker") {
		t.Fatal("live lookup remediation must point to the broker")
	}
}

func TestOpenAlgoLookupIncompleteFills(t *testing.T) {
	for _, mode := range []domain.TradingMode{domain.TradingModePaper, domain.TradingModeLive} {
		t.Run(string(mode), func(t *testing.T) {
			replyMode := ""
			if mode == domain.TradingModePaper {
				replyMode = "analyze"
			}
			for _, tc := range []struct {
				name, trades string
			}{
				{"empty", `[]`},
				{"short", `[{"orderid":"123","symbol":"RELIANCE","exchange":"NSE","action":"BUY","quantity":1,"average_price":100}]`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					v := newOpenAlgoTestVenue(t, mode, map[string]openAlgoTestReply{
						"tradebook": {200, fmt.Sprintf(`{"status":"success","mode":%q,"data":%s}`, replyMode, tc.trades)},
					})
					ref := fwtrading.OrderRef{VenueOrderID: "123", Symbol: "NSE:" + openAlgoTestKey}
					snapshot, found, err := v.c.Lookup(context.Background(), ref)
					openAlgoAssertError(t, err, errOpenAlgoFillsIncomplete, "")
					if !errors.Is(err, domain.ErrUpstream) {
						t.Fatal("incomplete fills did not return an upstream error")
					}
					if !found || !reflect.DeepEqual(snapshot, fwtrading.Snapshot{}) {
						t.Fatal("incomplete fills did not keep the order pending without a snapshot")
					}
					if mode == domain.TradingModePaper {
						diag := openAlgoAssertDiagnostic(t, v, health.CodeInconsistentData, health.DiagKindProvider,
							"OpenAlgo fills of a complete order are missing", v.c.redact.Replace(ref.Symbol))
						if diag.Detail != "OpenAlgo reports the order as complete, but its analyzer trade book does not show all of its fills; "+
							"the analyzer trade book covers the current session only." {
							t.Fatal("incomplete paper fills lost the session boundary detail")
						}
						if diag.Remediation != "Officer keeps the order pending and retries; it books the fills once the trade book shows them. "+
							"If this stays, the order's session has ended and OpenAlgo no longer shows its fills, so Officer cannot book them by itself." {
							t.Fatal("incomplete paper fills lost the pending order remediation")
						}
					} else if len(v.diagnostics) != 0 {
						t.Fatal("incomplete live fills produced a diagnostic")
					}
					openAlgoAssertRequests(t, v, "orderstatus", "tradebook")
				})
			}
		})
	}
}

func TestOpenAlgoLookupRemediations(t *testing.T) {
	for _, mode := range []domain.TradingMode{domain.TradingModePaper, domain.TradingModeLive} {
		t.Run(string(mode), func(t *testing.T) {
			status := openAlgoTestStatus("123", "cancelled")
			if mode == domain.TradingModeLive {
				status = strings.Replace(status, `,"mode":"analyze"`, "", 1)
			}
			notFound := `{"status":"error","message":"Order not found"}`
			if mode == domain.TradingModePaper {
				notFound = `{"status":"error","message":"Order not found","mode":"analyze"}`
			}
			for _, tc := range []struct {
				name, orderID, title string
				overrides            map[string]openAlgoTestReply
				sentinel             error
				paper, live          string
			}{
				{"unknown_outcome", "", "OpenAlgo order outcome unknown", nil, domain.ErrUpstream,
					"Officer keeps the order pending and cannot reconcile it by itself. With OpenAlgo in analyzer mode, find the order in its order book or Action Center.",
					"Officer keeps the order pending and cannot reconcile it by itself. Find the order in OpenAlgo's order book or Action Center and check it at the broker."},
				{"trading_day", "123", "OpenAlgo no longer lists the order", map[string]openAlgoTestReply{
					"orderstatus": {404, notFound},
				}, domain.ErrUpstream,
					"Officer keeps the order pending and cannot reconcile it by itself. Check that the connection's API key belongs to the OpenAlgo user that placed the order.",
					"Officer keeps the order pending and cannot reconcile it after its trading day. Check the order at the broker."},
				{"cancelled_fills", "123", "OpenAlgo fills of a closed order are unconfirmed", map[string]openAlgoTestReply{
					"orderstatus": {200, status},
				}, errOpenAlgoCancelledFillsUnconfirmed,
					"Officer keeps the order pending and retries; it books the fills once the trade book matches the filled quantity OpenAlgo's order stream reported. If this stays, compare Officer's fills with OpenAlgo's trade book in analyzer mode.",
					"Officer keeps the order pending and retries; it books the fills once the trade book matches the filled quantity OpenAlgo's order stream reported. If this stays, compare Officer's fills with the order's fills at the broker."},
			} {
				t.Run(tc.name, func(t *testing.T) {
					v := newOpenAlgoTestVenue(t, mode, tc.overrides)
					_, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{
						VenueOrderID: tc.orderID, ClientOrderID: "client", Symbol: "NSE:RELIANCE",
					})
					openAlgoAssertError(t, err, tc.sentinel, "")
					if !found {
						t.Fatal("Lookup returned found=false")
					}
					diag := openAlgoAssertDiagnostic(t, v, health.CodeInconsistentData,
						health.DiagKindProvider, tc.title, "NSE:RELIANCE")
					openAlgoAssertLookupRemediation(t, diag.Remediation, mode)
					want := tc.live
					if mode == domain.TradingModePaper {
						want = tc.paper
					}
					if diag.Remediation != want {
						t.Fatal("lookup remediation wording changed")
					}
					if tc.name == "trading_day" {
						detail := "OpenAlgo's order book does not list an order Officer sent; " +
							"OpenAlgo's order and trade books cover the current trading day only."
						if mode == domain.TradingModePaper {
							detail = "OpenAlgo's analyzer has no order with the id Officer recorded for it; " +
								"analyzer orders belong to the OpenAlgo user of the connection's API key."
						}
						if diag.Detail != detail {
							t.Fatal("missing-order diagnostic detail changed")
						}
					}
				})
			}
		})
	}
}

func TestOpenAlgoLookupModes(t *testing.T) {
	for _, tc := range []struct {
		name, modeMember, reportedMode string
		mode                           domain.TradingMode
		mismatch                       bool
	}{
		{"paper_matching", `,"mode":"analyze"`, "analyzer", domain.TradingModePaper, false},
		{"live_matching", "", "live", domain.TradingModeLive, false},
		{"paper_live", "", "live", domain.TradingModePaper, true},
		{"live_analyzer", `,"mode":"analyze"`, "analyzer", domain.TradingModeLive, true},
		{"paper_unknown", `,"mode":"` + openAlgoTestKey + `"`, `"[redacted]"`, domain.TradingModePaper, true},
		{"live_unknown", `,"mode":"` + openAlgoTestKey + `"`, `"[redacted]"`, domain.TradingModeLive, true},
	} {
		for _, endpoint := range []string{"orderstatus", "tradebook"} {
			t.Run(tc.name+"/"+endpoint, func(t *testing.T) {
				data := `{"orderid":"123","symbol":"RELIANCE","exchange":"NSE","action":"BUY","quantity":2,"order_status":"complete"}`
				if endpoint == "tradebook" {
					data = `[{"orderid":"123","symbol":"RELIANCE","exchange":"NSE","action":"BUY","quantity":2,"average_price":100}]`
				}
				body := `{"status":"success"` + tc.modeMember + `,"data":` + data + `}`
				v := newOpenAlgoTestVenue(t, tc.mode, map[string]openAlgoTestReply{endpoint: {200, body}})
				v.c.storeClosedQuantity("123", "2")
				v.c.mapOrder("123", "client")
				ref := fwtrading.OrderRef{VenueOrderID: "123", ClientOrderID: "client", Symbol: "NSE:" + openAlgoTestKey}
				snapshot, found, err := v.c.Lookup(context.Background(), ref)
				if !found {
					t.Fatal("Lookup returned found=false")
				}
				if tc.mismatch {
					openAlgoAssertError(t, err, domain.ErrUpstream, "mode differs")
					if !reflect.DeepEqual(snapshot, fwtrading.Snapshot{}) {
						t.Fatal("other-mode Lookup returned a snapshot")
					}
					diag := openAlgoAssertDiagnostic(t, v, health.CodeInvalidProviderConfig, health.DiagKindConfig,
						"OpenAlgo order mode differs from the connection", "NSE:[redacted]")
					wantDetail := fmt.Sprintf("OpenAlgo answered in %s mode on a %s connection.", tc.reportedMode, tc.mode)
					if diag.Detail != wantDetail ||
						!strings.Contains(diag.Remediation, "keeps the order pending and books nothing OpenAlgo reports in the other mode") ||
						!strings.Contains(diag.Remediation, "Set OpenAlgo's mode to match the connection") {
						t.Fatal("mode diagnostic lost its mode, connection or pending-order boundary")
					}
					if v.c.closedFills["123"] != "2" || len(v.c.closedIDs) != 1 || v.c.closedIDs[0] != "123" {
						t.Fatal("mode mismatch changed the closed confirmation store")
					}
					if event, ready, _ := v.c.correlate("123"); !ready || event.Ref.ClientOrderID != "client" {
						t.Fatal("mode mismatch changed the order correlation")
					}
				} else {
					openAlgoAssertError(t, err, nil, "")
					if snapshot.Status != fwtrading.VenueStatusFilled || snapshot.FilledQuantity != "2" || len(snapshot.Fills) != 1 {
						t.Fatal("matching-mode Lookup lost the confirmed fills")
					}
					if len(v.diagnostics) != 0 {
						t.Fatal("matching-mode Lookup reported a diagnostic")
					}
				}
				if tc.mismatch && endpoint == "orderstatus" {
					openAlgoAssertRequests(t, v, "orderstatus")
				} else {
					openAlgoAssertRequests(t, v, "orderstatus", "tradebook")
				}
			})
		}
	}
}

func TestOpenAlgoLookupNotFoundModes(t *testing.T) {
	for _, tc := range []struct {
		mode                          domain.TradingMode
		matching, other, reportedMode string
	}{
		{domain.TradingModePaper, `,"mode":"analyze"`, "", "live"},
		{domain.TradingModeLive, "", `,"mode":"analyze"`, "analyzer"},
	} {
		for _, reply := range []struct {
			name, body string
			mismatch   bool
			envelope   bool
		}{
			{"other_mode", `{"status":"error","message":"Order not found"` + tc.other + `}`, true, true},
			{"matching_mode", `{"status":"error","message":"Order not found"` + tc.matching + `}`, false, true},
			{"undecoded", `not JSON ` + openAlgoTestKey, false, false},
			{"message_only", `{"message":"x"}`, false, false},
			{"empty_object", `{}`, false, false},
			{"null_body", `null`, false, false},
			{"empty_status", `{"status":"","mode":"analyze"}`, false, false},
		} {
			t.Run(string(tc.mode)+"/"+reply.name, func(t *testing.T) {
				v := newOpenAlgoTestVenue(t, tc.mode, map[string]openAlgoTestReply{
					"orderstatus": {http.StatusNotFound, reply.body},
				})
				v.c.storeClosedQuantity("123", "2")
				v.c.mapOrder("123", "client")
				snapshot, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{
					VenueOrderID: "123", ClientOrderID: "client", Symbol: "NSE:RELIANCE",
				})
				if reply.envelope {
					openAlgoAssertError(t, err, domain.ErrUpstream, "")
				} else {
					openAlgoAssertError(t, err, nil, "openalgo")
					if errors.Is(err, domain.ErrUpstream) || len(v.diagnostics) != 0 {
						t.Fatal("non-envelope 404 became an OpenAlgo reconciliation diagnostic")
					}
					var apiErr *openalgo.APIError
					if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound ||
						apiErr.EnvelopeDecoded || apiErr.Mode != "" || err != apiErr {
						t.Fatal("non-envelope 404 did not preserve the API error unchanged")
					}
					if reply.name == "message_only" && apiErr.Message != "x" {
						t.Fatal("non-envelope 404 lost the decoded message")
					}
				}
				if !found || !reflect.DeepEqual(snapshot, fwtrading.Snapshot{}) {
					t.Fatal("missing-order Lookup must keep the order pending without a snapshot")
				}
				if reply.mismatch {
					wantErr := fmt.Errorf("openalgo: lookup reply mode differs from the connection: %w", domain.ErrUpstream)
					if err.Error() != wantErr.Error() {
						t.Fatal("other-mode 404 lost the lookup mode error")
					}
					diag := openAlgoAssertDiagnostic(t, v, health.CodeInvalidProviderConfig, health.DiagKindConfig,
						"OpenAlgo order mode differs from the connection", "NSE:RELIANCE")
					if diag.Detail != fmt.Sprintf("OpenAlgo answered in %s mode on a %s connection.", tc.reportedMode, tc.mode) ||
						diag.Remediation != "Officer keeps the order pending and books nothing OpenAlgo reports in the other mode. Set OpenAlgo's mode to match the connection." {
						t.Fatal("other-mode 404 lost its mode diagnostic")
					}
				} else if reply.envelope {
					var apiErr *openalgo.APIError
					if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
						t.Fatal("missing-order Lookup lost the API error")
					}
					diag := openAlgoAssertDiagnostic(t, v, health.CodeInconsistentData, health.DiagKindProvider,
						"OpenAlgo no longer lists the order", "NSE:RELIANCE")
					detail := "OpenAlgo's order book does not list an order Officer sent; " +
						"OpenAlgo's order and trade books cover the current trading day only."
					remediation := "Officer keeps the order pending and cannot reconcile it after its trading day. Check the order at the broker."
					if tc.mode == domain.TradingModePaper {
						detail = "OpenAlgo's analyzer has no order with the id Officer recorded for it; " +
							"analyzer orders belong to the OpenAlgo user of the connection's API key."
						remediation = "Officer keeps the order pending and cannot reconcile it by itself. Check that the connection's API key belongs to the OpenAlgo user that placed the order."
					}
					if diag.Detail != detail || diag.Remediation != remediation {
						t.Fatal("missing-order diagnostic wording changed")
					}
					openAlgoAssertLookupRemediation(t, diag.Remediation, tc.mode)
				}
				if v.c.closedFills["123"] != "2" || len(v.c.closedIDs) != 1 || v.c.closedIDs[0] != "123" {
					t.Fatal("404 changed the closed confirmation store")
				}
				if event, ready, _ := v.c.correlate("123"); !ready || event.Ref.ClientOrderID != "client" {
					t.Fatal("404 changed the order correlation")
				}
				openAlgoAssertRequests(t, v, "orderstatus")
			})
		}
	}
}

func TestOpenAlgoLookupConfirmedClosed(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   fwtrading.VenueStatus
	}{
		{"cancelled", fwtrading.VenueStatusCancelled},
		{"complete", fwtrading.VenueStatusFilled},
	} {
		t.Run(tc.status, func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper,
				map[string]openAlgoTestReply{"orderstatus": {200, openAlgoTestStatus("123", tc.status)}})
			v.c.mapOrder("123", "client")
			v.c.storeClosedQuantity("123", "2")
			snapshot, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{
				VenueOrderID: "123", ClientOrderID: "client",
			})
			openAlgoAssertError(t, err, nil, "")
			if !found || snapshot.Status != tc.want ||
				snapshot.FilledQuantity != "2" || len(snapshot.Fills) != 1 || snapshot.Fills[0].Price != "100" {
				t.Fatal("closed Lookup did not use its stream confirmation")
			}
			if len(v.diagnostics) != 0 || len(v.c.closedFills) != 0 || len(v.c.closedIDs) != 0 {
				t.Fatal("successful closed Lookup reported a diagnostic or kept confirmation")
			}
			if _, ready, _ := v.c.correlate("123"); ready || len(v.c.held) != 0 {
				t.Fatal("successful closed Lookup did not finish the order")
			}
		})
	}
}

func TestOpenAlgoProvider(t *testing.T) {
	p := OpenAlgoProvider()
	if p.Type != domain.TradingProviderOpenAlgo || p.Title != "OpenAlgo" ||
		p.VenueAccounts || !p.VerifiesSymbols || !p.SearchesSymbols || p.UnlinkedFees || p.Build == nil {
		t.Fatal("OpenAlgo provider capabilities changed")
	}
	connector, err := p.Build(fwtrading.BuildContext{Connection: domain.TradingConnection{
		Mode: domain.TradingModePaper, Credentials: openAlgoTestCredentials("http://127.0.0.1:5000"),
	}})
	openAlgoAssertError(t, err, nil, "")
	c, ok := connector.(*openAlgoConnector)
	if !ok {
		t.Fatal("OpenAlgo provider built another connector")
	}
	defer c.Close()
	if c.wsURL != "ws://127.0.0.1:8765" || c.orderCorrelator == nil || c.closedFills == nil {
		t.Fatal("OpenAlgo constructor did not initialize stream state")
	}
	connector, err = NewOpenAlgoConnector(domain.TradingConnection{Mode: domain.TradingModePaper, Credentials: "{}"})
	if err == nil || connector != nil {
		t.Fatal("OpenAlgo public constructor accepted missing credentials")
	}
	openAlgoAssertSafe(t, err.Error())
}

func TestOpenAlgoLookupPreservesConfirmationOnError(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint string
		reply          openAlgoTestReply
	}{
		{"status-error", "orderstatus", openAlgoTestReply{500, `{"status":"error","message":"retry"}`}},
		{"not-found", "orderstatus", openAlgoTestReply{404, `{"status":"error","message":"missing"}`}},
		{"invalid-status", "orderstatus", openAlgoTestReply{200, `{}`}},
		{"different-id", "orderstatus", openAlgoTestReply{200, openAlgoTestStatus("456", "complete")}},
		{"trade-error", "tradebook", openAlgoTestReply{500, `{"status":"error","message":"retry"}`}},
		{"invalid-trades", "tradebook", openAlgoTestReply{200, `{}`}},
		{"unknown-status", "orderstatus", openAlgoTestReply{200, openAlgoTestStatus("123", "unknown")}},
		{"incomplete", "tradebook", openAlgoTestReply{200, `{"status":"success","mode":"analyze","data":[]}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper,
				map[string]openAlgoTestReply{tc.endpoint: tc.reply})
			v.c.storeClosedQuantity("123", "2")
			v.c.mapOrder("123", "client")
			_, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{
				VenueOrderID: "123", ClientOrderID: "client",
			})
			if err == nil || !found {
				t.Fatal("invalid Lookup response did not keep the order pending")
			}
			openAlgoAssertSafe(t, err.Error())
			if v.c.closedFills["123"] != "2" || len(v.c.closedIDs) != 1 || v.c.closedIDs[0] != "123" {
				t.Fatal("failed Lookup changed the closed confirmation store")
			}
			if event, ready, _ := v.c.correlate("123"); !ready || event.Ref.ClientOrderID != "client" {
				t.Fatal("failed Lookup changed the order correlation")
			}
		})
	}
}

func TestOpenAlgoLookupMissingID(t *testing.T) {
	for _, tc := range []struct{ name, symbol, instrument string }{
		{"empty", "", ""},
		{"symbol", "NSE:RELIANCE", "NSE:RELIANCE"},
		{"redacted", "NSE:" + openAlgoTestKey, "NSE:[redacted]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, nil)
			_, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "client", Symbol: tc.symbol})
			openAlgoAssertError(t, err, domain.ErrUpstream, "venue order id")
			if !found {
				t.Fatal("Lookup returned found=false")
			}
			diag := openAlgoAssertDiagnostic(t, v, health.CodeInconsistentData, health.DiagKindProvider,
				"OpenAlgo order outcome unknown", tc.instrument)
			if !strings.Contains(diag.Detail, "no client order id") || !strings.Contains(diag.Remediation, "cannot reconcile it by itself") {
				t.Fatal("missing-id diagnostic lost its reconciliation boundary")
			}
			openAlgoAssertRequests(t, v)
		})
	}
}

func TestOpenAlgoLookupAPIErrors(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint string
		status         int
	}{
		{"not_found", "orderstatus", 404},
		{"status_error", "orderstatus", 500},
		{"trades_error", "tradebook", 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := ""
			if tc.status == http.StatusNotFound {
				mode = `,"mode":"analyze"`
			}
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, map[string]openAlgoTestReply{
				tc.endpoint: {tc.status, `{"status":"error","message":"` + openAlgoTestKey + `"` + mode + `}`},
			})
			_, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{VenueOrderID: "123", Symbol: "NSE:RELIANCE"})
			openAlgoAssertError(t, err, nil, "openalgo")
			if !found {
				t.Fatal("Lookup returned found=false")
			}
			var apiErr *openalgo.APIError
			if !errors.As(err, &apiErr) || apiErr.Status != tc.status {
				t.Fatal("Lookup lost the API error")
			}
			if errors.Is(err, openalgo.ErrInvalidResponse) {
				t.Fatal("API error became an unreadable-response failure")
			}
			if tc.status == 404 {
				if !errors.Is(err, domain.ErrUpstream) {
					t.Fatal("missing venue order lost ErrUpstream")
				}
				diag := openAlgoAssertDiagnostic(t, v, health.CodeInconsistentData, health.DiagKindProvider,
					"OpenAlgo no longer lists the order", "NSE:RELIANCE")
				if diag.Detail != "OpenAlgo's analyzer has no order with the id Officer recorded for it; "+
					"analyzer orders belong to the OpenAlgo user of the connection's API key." ||
					diag.Remediation != "Officer keeps the order pending and cannot reconcile it by itself. Check that the connection's API key belongs to the OpenAlgo user that placed the order." {
					t.Fatal("missing paper order diagnostic wording changed")
				}
				openAlgoAssertLookupRemediation(t, diag.Remediation, domain.TradingModePaper)
			} else if len(v.diagnostics) != 0 {
				t.Fatal("generic API error produced a reconciliation diagnostic")
			}
			if tc.endpoint == "orderstatus" {
				openAlgoAssertRequests(t, v, "orderstatus")
			} else {
				openAlgoAssertRequests(t, v, "orderstatus", "tradebook")
			}
		})
	}
}

func TestOpenAlgoLookupInvalidResponse(t *testing.T) {
	for _, tc := range []struct{ name, endpoint, body string }{
		{"status_exponent", "orderstatus", `{"status":"success","data":{"orderid":"123","quantity":1e2}}`},
		{"status_blank_id", "orderstatus", `{"status":"success","data":{"orderid":" "}}`},
		{"status_null", "orderstatus", `{"status":"success","data":null}`},
		{"trade_exponent", "tradebook", `{"status":"success","data":[{"orderid":"manual","quantity":1e2}]}`},
		{"trade_blank_id", "tradebook", `{"status":"success","data":[{"orderid":" "}]}`},
		{"trade_null_row", "tradebook", `{"status":"success","data":[null]}`},
		{"trade_null_data", "tradebook", `{"status":"success","data":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, map[string]openAlgoTestReply{
				tc.endpoint: {200, tc.body},
			})
			_, found, err := v.c.Lookup(context.Background(), fwtrading.OrderRef{
				VenueOrderID: "123", Symbol: "NSE:RELIANCE",
			})
			openAlgoAssertError(t, err, openalgo.ErrInvalidResponse, "")
			if !found {
				t.Fatal("unreadable response returned found=false")
			}
			diag := openAlgoAssertDiagnostic(t, v, health.CodeUnparsableData,
				health.DiagKindProvider, "Unknown order data from the venue", "NSE:RELIANCE")
			detail := "OpenAlgo returned an order status Officer cannot read."
			if tc.endpoint == "tradebook" {
				detail = "OpenAlgo returned a trade book Officer cannot read."
				openAlgoAssertRequests(t, v, "orderstatus", "tradebook")
			} else {
				openAlgoAssertRequests(t, v, "orderstatus")
			}
			if diag.Detail != detail {
				t.Fatal("unreadable-response diagnostic did not use the fixed detail")
			}
		})
	}
}

func TestOpenAlgoVerifySymbol(t *testing.T) {
	for _, external := range []string{"", "RELIANCE", ":RELIANCE", "NSE:", " NSE:RELIANCE", "NSE:RELIANCE ", "NFO:RELIANCE"} {
		t.Run(external, func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, nil)
			result, err := v.c.VerifySymbol(context.Background(), external)
			openAlgoAssertError(t, err, nil, "")
			if result.Exists || result.Tradable || result.Details != openalgo.SymbolForm {
				t.Fatal("invalid external symbol did not explain the expected form")
			}
			openAlgoAssertRequests(t, v)
		})
	}
	for _, exchange := range []string{"NSE", "BSE"} {
		t.Run(exchange, func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, domain.TradingModeLive, nil)
			result, err := v.c.VerifySymbol(context.Background(), exchange+":RELIANCE")
			openAlgoAssertError(t, err, nil, "")
			if !result.Exists || !result.Tradable || result.Details != "Reliance; lot size 2; tick size 0.05" {
				t.Fatal("known symbol lost its master-contract metadata")
			}
			requests := openAlgoAssertRequests(t, v, "symbol")
			if requests[0].fields["exchange"] != exchange || requests[0].fields["symbol"] != "RELIANCE" {
				t.Fatal("VerifySymbol queried the wrong symbol")
			}
		})
	}
	for _, status := range []int{404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, map[string]openAlgoTestReply{
				"symbol": {status, `{"status":"error","message":"` + openAlgoTestKey + `"}`},
			})
			result, err := v.c.VerifySymbol(context.Background(), "NSE:RELIANCE")
			if status == 404 {
				openAlgoAssertError(t, err, nil, "")
			} else {
				openAlgoAssertError(t, err, nil, "openalgo")
			}
			if result.Exists || result.Tradable {
				t.Fatal("failed verification claims the symbol exists")
			}
			openAlgoAssertRequests(t, v, "symbol")
		})
	}
}

func TestOpenAlgoSearchSymbols(t *testing.T) {
	for _, tc := range []struct {
		name, query, sentQuery string
		exchanges              []string
	}{
		{"empty", "", "", nil},
		{"blank", " \t\n", "", nil},
		{"nse_empty", "NSE:", "", nil},
		{"bse_blank", "BSE: \t", "", nil},
		{"both", " RELI ", "RELI", []string{"NSE", "BSE"}},
		{"nse_prefix", "NSE:RELI", "RELI", []string{"NSE"}},
		{"nse_spaced", "NSE: RELI", "RELI", []string{"NSE"}},
		{"bse_prefix", "BSE:RELI", "RELI", []string{"BSE"}},
		{"other_prefix", "NFO:RELI", "NFO:RELI", []string{"NSE", "BSE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, nil)
			matches, err := v.c.SearchSymbols(context.Background(), tc.query)
			openAlgoAssertError(t, err, nil, "")
			if matches == nil || len(matches) != len(tc.exchanges) {
				t.Fatal("search did not return the expected non-nil matches")
			}
			requests := v.recorded()
			if len(requests) != len(tc.exchanges) {
				t.Fatal("search contacted an unexpected number of exchanges")
			}
			for i, exchange := range tc.exchanges {
				if requests[i].name != "search" || requests[i].fields["exchange"] != exchange || requests[i].fields["query"] != tc.sentQuery {
					t.Fatal("wrong search exchange order or query")
				}
				want := fwtrading.SymbolMatch{Symbol: exchange + ":RELIANCE", Name: "Reliance", Class: "EQ", Exchange: exchange, Tradable: true}
				if matches[i] != want {
					t.Fatal("search changed a result's metadata or exchange order")
				}
			}
		})
	}
	for _, tc := range []struct {
		name, body, message string
		status              int
	}{
		{"exchange_mismatch", `{"status":"success","data":[{"symbol":"RELIANCE","exchange":"BSE"}]}`, "exchange differs", 200},
		{"api_error", `{"status":"error","message":"` + openAlgoTestKey + `"}`, "openalgo", 500},
		{"no_matches", `{"status":"success","data":[]}`, "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newOpenAlgoTestVenue(t, domain.TradingModePaper, map[string]openAlgoTestReply{"search": {tc.status, tc.body}})
			matches, err := v.c.SearchSymbols(context.Background(), "NSE:RELI")
			openAlgoAssertError(t, err, nil, tc.message)
			if tc.message == "" {
				if matches == nil || len(matches) != 0 {
					t.Fatal("empty search result must be a non-nil slice")
				}
			} else if matches != nil {
				t.Fatal("search error leaked partial matches")
			}
			openAlgoAssertRequests(t, v, "search")
		})
	}
}

func TestOpenAlgoSearchSecondExchangeFailure(t *testing.T) {
	var mu sync.Mutex
	var exchanges []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var fields map[string]string
		if json.NewDecoder(r.Body).Decode(&fields) != nil {
			t.Error("invalid search request")
			return
		}
		mu.Lock()
		exchanges = append(exchanges, fields["exchange"])
		mu.Unlock()
		if fields["exchange"] == "BSE" {
			w.WriteHeader(500)
			_, _ = io.WriteString(w, `{"status":"error","message":"`+openAlgoTestKey+`"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"success","data":[{"symbol":"RELIANCE","exchange":"NSE"}]}`)
	}))
	t.Cleanup(server.Close)
	c, err := newOpenAlgoConnector(domain.TradingConnection{
		Mode: domain.TradingModeLive, Credentials: openAlgoTestCredentials(server.URL),
	})
	openAlgoAssertError(t, err, nil, "")
	matches, err := c.SearchSymbols(context.Background(), "RELI")
	openAlgoAssertError(t, err, nil, "openalgo")
	if matches != nil {
		t.Fatal("second exchange failure returned incomplete search results")
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(exchanges, []string{"NSE", "BSE"}) {
		t.Fatal("unprefixed search did not reach both exchanges in order")
	}
}
