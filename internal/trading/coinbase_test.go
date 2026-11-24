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
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/coinbase"
)

const coinbaseTestAPIKeyID = "organizations/test/apiKeys/secret-key-name"

func coinbaseTestConnection(t *testing.T) domain.TradingConnection {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"apiKeyId": coinbaseTestAPIKeyID,
		"apiKeySecret": string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))})
	if err != nil {
		t.Fatal(err)
	}
	return domain.TradingConnection{Mode: domain.TradingModeLive, Credentials: string(raw)}
}

func coinbaseTestConnector(t *testing.T, handler http.HandlerFunc) *coinbaseConnector {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := newCoinbaseConnector(coinbaseTestConnection(t), server.URL, "ws"+strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	c.SetDiagnosticReporter(func(health.Diagnostic) {})
	c.SetStatusReporter(func(bool, string) {})
	t.Cleanup(c.Close)
	return c
}

func coinbaseTestProduct() map[string]any {
	return map[string]any{"product_id": "BTC-USD", "product_type": "SPOT", "status": "online",
		"base_currency_id": "BTC", "quote_currency_id": "USD", "base_increment": "0.01",
		"base_min_size": "0.1", "base_max_size": "10", "quote_increment": "0.0001", "price_increment": "0.01", "quote_min_size": "1"}
}

func coinbaseTestOrder() fwtrading.Order {
	return fwtrading.Order{ClientOrderID: "officer-order", Symbol: "BTC-USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, Quantity: "1.00", LimitPrice: "10.00", Route: `{"timeInForce":"GTC"}`}
}

func TestCoinbaseConstruction(t *testing.T) {
	validPaper := coinbaseTestConnection(t)
	validPaper.Mode = domain.TradingModePaper
	if _, err := NewCoinbaseConnector(validPaper); err == nil || domain.ValidationPointer(err) != "/mode" {
		t.Fatalf("valid paper credentials must still be refused at /mode: %v", err)
	}
	for _, mode := range []domain.TradingMode{domain.TradingModePaper, "", "invalid"} {
		connection := domain.TradingConnection{Mode: mode, Credentials: "bad JSON"}
		_, err := NewCoinbaseConnector(connection)
		if err == nil || domain.ValidationPointer(err) != "/mode" {
			t.Fatalf("paper/non-live refusal must precede credentials: %v", err)
		}
	}
	connection := coinbaseTestConnection(t)
	connection.Credentials = `{"apiKeyId":"test","apiKeySecret":"bad"}`
	_, err := NewCoinbaseConnector(connection)
	if err == nil || domain.ValidationPointer(err) != "/credentials/apiKeySecret" {
		t.Fatalf("credential pointer: %v", err)
	}
	c, err := NewCoinbaseConnector(coinbaseTestConnection(t))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestCoinbaseRoutesAndSendPayload(t *testing.T) {
	for _, tc := range []struct {
		route, price, kind string
		valid              bool
	}{
		{`{}`, "", "market_market_ioc", true},
		{`{"timeInForce":"GTC"}`, "10.00", "limit_limit_gtc", true},
		{`{"timeInForce":"IOC"}`, "10.00", "sor_limit_ioc", true},
		{`{"timeInForce":"FOK"}`, "10.00", "limit_limit_fok", true},
		{`{}`, "10", "", false}, {`{"timeInForce":"GTC"}`, "", "", false},
		{`{"unknown":"GTC"}`, "10", "", false}, {`{"timeInForce":"gtc"}`, "10", "", false},
		{`{"timeInForce":null}`, "10", "", false}, {`null`, "", "", false}, {`[]`, "", "", false}, {`bad`, "", "", false},
	} {
		t.Run(tc.route+tc.price, func(t *testing.T) {
			calls := 0
			c := coinbaseTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/api/v3/brokerage/orders" {
					t.Errorf("wrong create endpoint: %s %s", r.Method, r.URL)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				config := map[string]any{"base_size": "1.00"}
				if tc.price != "" {
					config["limit_price"] = tc.price
				}
				if tc.kind == "limit_limit_gtc" {
					config["post_only"] = false
				}
				want := map[string]any{"client_order_id": "officer-order", "product_id": "BTC-USD", "side": "BUY", "order_configuration": map[string]any{tc.kind: config}}
				if !reflect.DeepEqual(body, want) {
					t.Errorf("exact create payload: got %#v want %#v", body, want)
				}
				writeJSON(t, w, map[string]any{"success": true, "success_response": map[string]string{"order_id": "venue-order", "client_order_id": "officer-order"}})
			})
			order := coinbaseTestOrder()
			order.Route, order.LimitPrice = tc.route, tc.price
			ack, err := c.Send(context.Background(), order)
			if tc.valid {
				if err != nil || ack.VenueOrderID != "venue-order" || calls != 1 {
					t.Fatalf("create: %+v %v calls=%d", ack, err, calls)
				}
			} else if !errors.Is(err, fwtrading.ErrRejected) || calls != 0 {
				t.Fatalf("route refusal: %v calls=%d", err, calls)
			}
		})
	}
	c := coinbaseTestConnector(t, func(http.ResponseWriter, *http.Request) { t.Error("unsupported order sent") })
	order := coinbaseTestOrder()
	order.AmountKind = "notional"
	if _, err := c.Send(context.Background(), order); !errors.Is(err, fwtrading.ErrUnsupported) {
		t.Fatal(err)
	}
	if err := c.PreCheck(context.Background(), order); !errors.Is(err, fwtrading.ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestCoinbaseSendOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		body          string
		rejected, ack bool
	}{
		{"ack", 200, `{"success":true,"success_response":{"order_id":"v","client_order_id":"officer-order"}}`, false, true},
		{"business", 200, `{"success":false,"error_response":{"new_order_failure_reason":"INSUFFICIENT_FUNDS","message":"denied","error_details":"balance"}}`, true, false},
		{"deprecated", 200, `{"success":false,"error_response":{"error":"INVALID_SIZE_PRECISION"}}`, true, false},
		{"duplicate", 200, `{"success":false,"error_response":{"new_order_failure_reason":"DUPLICATE_CLIENT_ORDER_ID"}}`, false, false},
		{"deprecated-duplicate", 200, `{"success":false,"error_response":{"error":"DUPLICATE_CLIENT_ORDER_ID"}}`, false, false},
		{"decoded-400", 400, `{"error":"INVALID_REQUEST","message":"denied"}`, true, false},
		{"duplicate-400", 400, `{"error":"DUPLICATE_CLIENT_ORDER_ID","message":"exists"}`, false, false},
		{"duplicate-message", 409, `{"error":"INVALID_REQUEST","message":"DUPLICATE_CLIENT_ORDER_ID"}`, false, false},
		{"timeout", 408, `{"error":"TIMEOUT","message":"timeout"}`, false, false},
		{"server", 503, `{"error":"UNAVAILABLE","message":"retry"}`, false, false},
		{"html-400", 400, `<html>proxy error</html>`, false, false},
		{"partial-400", 400, `{"error":"INVALID_REQUEST"}`, false, false},
		{"malformed", 200, `{`, false, false}, {"empty-body", 200, ``, false, false},
		{"missing-success", 200, `{}`, false, false},
		{"empty-id", 200, `{"success":true,"success_response":{"client_order_id":"officer-order"}}`, false, false},
		{"wrong-client", 200, `{"success":true,"success_response":{"order_id":"v","client_order_id":"other"}}`, false, false},
		{"transport", 0, ``, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := coinbaseTestConnector(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.status == 0 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			ack, err := c.Send(context.Background(), coinbaseTestOrder())
			if tc.ack {
				if err != nil || ack.VenueOrderID != "v" {
					t.Fatalf("ack=%+v err=%v", ack, err)
				}
				return
			}
			if err == nil || errors.Is(err, fwtrading.ErrRejected) != tc.rejected || errors.Is(err, fwtrading.ErrUnsupported) {
				t.Fatalf("wrong outcome class: %v", err)
			}
			if !tc.rejected && !strings.Contains(err.Error(), "outcome unknown") {
				t.Fatalf("missing uncertainty: %v", err)
			}
		})
	}
}

func TestCoinbasePreCheck(t *testing.T) {
	for _, tc := range []struct {
		name, field     string
		value           any
		quantity, price string
		upstream        bool
		valid           bool
	}{
		{name: "valid", valid: true}, {name: "type", field: "product_type", value: "FUTURE"}, {name: "status", field: "status", value: "offline"},
		{name: "trading-disabled", field: "trading_disabled", value: true}, {name: "disabled", field: "is_disabled", value: true},
		{name: "cancel-only", field: "cancel_only", value: true}, {name: "view-only", field: "view_only", value: true},
		{name: "post-only", field: "post_only", value: true}, {name: "limit-only-market", field: "limit_only", value: true, price: "market"},
		{name: "increment", quantity: "0.111"}, {name: "below-min", quantity: "0.09"}, {name: "above-max", quantity: "10.01"},
		{name: "zero-quantity", quantity: "0"}, {name: "bad-quantity", quantity: "NaN"},
		{name: "price-increment", price: "10.001"}, {name: "quote-minimum", quantity: "0.1", price: "1"},
		{name: "quote-aligned-price", price: "10.0001"},
		{name: "zero-price", price: "0"},
		{name: "bad-step", field: "base_increment", value: "bad", upstream: true},
		{name: "zero-step", field: "base_increment", value: "0", upstream: true},
		{name: "bad-min", field: "base_min_size", value: "bad", upstream: true},
		{name: "bad-max", field: "base_max_size", value: "bad", upstream: true},
		{name: "bad-price-step", field: "price_increment", value: "bad", upstream: true},
		{name: "zero-price-step", field: "price_increment", value: "0", upstream: true},
		{name: "negative-price-step", field: "price_increment", value: "-0.01", upstream: true},
		{name: "missing-price-step-limit", field: "price_increment", upstream: true},
		{name: "missing-price-step-market", field: "price_increment", price: "market", valid: true},
		{name: "bad-quote-min", field: "quote_min_size", value: "bad", upstream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := coinbaseTestProduct()
			if tc.field != "" {
				if tc.value == nil {
					delete(p, tc.field)
				} else {
					p[tc.field] = tc.value
				}
			}
			c := coinbaseTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v3/brokerage/products/BTC-USD" || r.URL.Query().Get("get_tradability_status") != "true" {
					t.Errorf("wrong product request: %s", r.URL)
				}
				writeJSON(t, w, p)
			})
			order := coinbaseTestOrder()
			if tc.quantity != "" {
				order.Quantity = tc.quantity
			}
			if tc.price != "" {
				order.LimitPrice = tc.price
			}
			if tc.price == "market" {
				order.LimitPrice, order.Route = "", "{}"
			}
			err := c.PreCheck(context.Background(), order)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || errors.Is(err, domain.ErrUpstream) != tc.upstream || errors.Is(err, fwtrading.ErrRejected) == tc.upstream {
				t.Fatalf("precheck class: %v", err)
			}
		})
	}
}

func coinbaseRESTOrder() map[string]any {
	return map[string]any{"order_id": "venue-order", "client_order_id": "officer-order", "product_id": "BTC-USD", "status": "OPEN", "filled_size": "0.5",
		"order_configuration": map[string]any{"limit_limit_gtc": map[string]string{"base_size": "1"}}}
}

func coinbaseRESTFill(id, size string) map[string]any {
	return map[string]any{"entry_id": id, "trade_id": "trade-" + id, "order_id": "venue-order", "trade_type": "FILL", "size_in_quote": false,
		"trade_time": "2026-11-12T10:00:00Z", "size": size, "price": "10", "commission": "0.01"}
}

type coinbaseLookupTest struct {
	order      map[string]any
	product    map[string]any
	fillPages  []map[string]any
	orderPages []map[string]any
	getStatus  int
}

func newCoinbaseLookupTest(t *testing.T) (*coinbaseConnector, *coinbaseLookupTest) {
	t.Helper()
	f := &coinbaseLookupTest{order: coinbaseRESTOrder(), product: coinbaseTestProduct(), getStatus: 200,
		fillPages: []map[string]any{{"fills": []any{coinbaseRESTFill("entry-a", "0.5")}, "cursor": ""}}}
	c := coinbaseTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("lookup method %s", r.Method)
		}
		page := 0
		if cursor := r.URL.Query().Get("cursor"); cursor != "" {
			var err error
			page, err = strconv.Atoi(cursor)
			if err != nil {
				t.Error(err)
				return
			}
		}
		switch r.URL.Path {
		case "/api/v3/brokerage/orders/historical/venue-order":
			if f.getStatus != 200 {
				w.WriteHeader(f.getStatus)
				writeJSON(t, w, map[string]string{"error": "NOT_FOUND", "message": "missing"})
				return
			}
			writeJSON(t, w, map[string]any{"order": f.order})
		case "/api/v3/brokerage/orders/historical/fills":
			if r.URL.Query().Get("order_ids") != "venue-order" {
				t.Error("fills not filtered by venue id")
			}
			if page >= len(f.fillPages) {
				t.Error("unexpected fill page")
				w.WriteHeader(500)
				return
			}
			writeJSON(t, w, f.fillPages[page])
		case "/api/v3/brokerage/orders/historical/batch":
			q := r.URL.Query()
			if q.Get("product_ids") != "BTC-USD" || q.Get("order_side") != "BUY" || q.Has("client_order_id") || q.Has("order_placement_source") {
				t.Errorf("order filters: %s", r.URL)
			}
			if page >= len(f.orderPages) {
				t.Error("unexpected order page")
				w.WriteHeader(500)
				return
			}
			writeJSON(t, w, f.orderPages[page])
		case "/api/v3/brokerage/products/BTC-USD":
			writeJSON(t, w, f.product)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(500)
		}
	})
	return c, f
}

func coinbaseTestRef() fwtrading.OrderRef {
	return fwtrading.OrderRef{VenueOrderID: "venue-order", ClientOrderID: "officer-order", Symbol: "BTC-USD", Side: domain.OrderSideBuy}
}

func TestCoinbaseLookupStatuses(t *testing.T) {
	for _, status := range []string{"PENDING", "QUEUED", "OPEN", "EDIT_QUEUED", "CANCEL_QUEUED", "FILLED", "CANCELLED", "EXPIRED", "FAILED", "UNKNOWN_ORDER_STATUS", "future"} {
		t.Run(status, func(t *testing.T) {
			c, f := newCoinbaseLookupTest(t)
			f.order["status"] = status
			f.order["reject_reason"], f.order["reject_message"] = "HOLD_FAILURE", "denied "+coinbaseTestAPIKeyID
			diagnostics := []health.Diagnostic{}
			c.SetDiagnosticReporter(func(d health.Diagnostic) { diagnostics = append(diagnostics, d) })
			s, found, err := c.Lookup(context.Background(), coinbaseTestRef())
			if status == "UNKNOWN_ORDER_STATUS" || status == "future" {
				if !errors.Is(err, domain.ErrUpstream) || found || len(diagnostics) != 1 ||
					diagnostics[0].Code != health.CodeUnparsableData ||
					diagnostics[0].Detail != "An order lookup returned an order status Officer does not know." ||
					!strings.Contains(err.Error(), status) {
					t.Fatalf("unknown status: %+v %v %+v", s, err, diagnostics)
				}
				return
			}
			want := fwtrading.VenueStatusOpen
			if status == "FILLED" {
				want = fwtrading.VenueStatusFilled
			}
			if status == "CANCELLED" || status == "EXPIRED" || status == "FAILED" {
				want = fwtrading.VenueStatusCancelled
			}
			if err != nil || !found || s.Status != want || len(s.Fills) != 1 {
				t.Fatalf("snapshot=%+v found=%v err=%v", s, found, err)
			}
			if status == "FAILED" && (!strings.Contains(s.Reason, "HOLD_FAILURE") || strings.Contains(s.Reason, coinbaseTestAPIKeyID)) {
				t.Fatalf("reason: %s", s.Reason)
			}
			fill := s.Fills[0]
			if fill.VenueTradeID != "trade-entry-a" {
				t.Fatalf("VenueTradeID = %q, want trade_id trade-entry-a", fill.VenueTradeID)
			}
			if fill.CumQuantity != "0.5" || fill.LeavesQuantity != "0.5" || fill.Final || fill.CommissionLeg != fwtrading.CommissionLegQuote || fill.Commission.Currency != "USD" || fill.Commission.Amount != "0.01" {
				t.Fatalf("fill: %+v", fill)
			}
		})
	}
}

func TestCoinbaseLookupMoneyRules(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*coinbaseLookupTest, fwtrading.OrderRef) fwtrading.OrderRef
	}{
		{"sum-mismatch", "fill history sum differs from filled_size", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			f.order["filled_size"] = "0.6"
			return r
		}},
		{"order-overfilled", "unreadable filled quantity", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			f.order["filled_size"] = "2"
			return r
		}},
		{"order-and-fills-overfilled", "unreadable filled quantity", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			f.order["filled_size"] = "2"
			f.fillPages[0]["fills"] = []any{coinbaseRESTFill("entry-a", "2")}
			return r
		}},
		{"negative-filled", "unreadable filled quantity", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			f.order["filled_size"] = "-0.5"
			return r
		}},
		{"bad-filled", "unreadable filled quantity", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			f.order["filled_size"] = "bad"
			return r
		}},
		{"bad-quantity", "unreadable order quantity", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			f.order["order_configuration"] = map[string]any{"market_market_ioc": map[string]string{"base_size": "bad"}}
			return r
		}},
		{"zero-quantity", "unreadable order quantity", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			f.order["order_configuration"] = map[string]any{"market_market_ioc": map[string]string{"base_size": "0"}}
			return r
		}},
		{"missing-configuration", "unreadable order configuration", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			delete(f.order, "order_configuration")
			return r
		}},
		{"client-mismatch", "order identity does not match the reference", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			f.order["client_order_id"] = "other"
			return r
		}},
		{"product-mismatch", "order identity does not match the reference", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			f.order["product_id"] = "other"
			return r
		}},
		{"id-mismatch", "order identity does not match the reference", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			f.order["order_id"] = "other"
			return r
		}},
		{"missing-quote", "fill product has no quote currency", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			f.product["quote_currency_id"] = ""
			return r
		}},
		{"fill-overfilled", "fills exceed order quantity", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			f.fillPages[0]["fills"] = []any{coinbaseRESTFill("entry-a", "2")}
			return r
		}},
		{"conflicting-duplicate", "conflicting duplicate fill entry_id", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			conflict := coinbaseRESTFill("entry-a", "0.5")
			conflict["price"] = "11"
			f.fillPages[0]["fills"] = []any{coinbaseRESTFill("entry-a", "0.5"), conflict}
			return r
		}},
		{"non-advancing-cursor", "fill cursor did not advance", func(f *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			f.fillPages[0]["cursor"] = "1"
			f.fillPages = append(f.fillPages, map[string]any{"fills": []any{}, "cursor": "1"})
			return r
		}},
		{"empty-symbol", "client order lookup requires symbol, side and client order id", func(_ *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			r.VenueOrderID, r.Symbol = "", ""
			return r
		}},
		{"empty-side", "client order lookup requires symbol, side and client order id", func(_ *coinbaseLookupTest, r fwtrading.OrderRef) fwtrading.OrderRef {
			r.VenueOrderID, r.Side = "", ""
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, f := newCoinbaseLookupTest(t)
			ref := tc.change(f, coinbaseTestRef())
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			s, found, err := c.Lookup(ctx, ref)
			if err == nil || !strings.Contains(err.Error(), tc.want) || found || len(s.Fills) != 0 {
				t.Fatalf("want %q; snapshot=%+v found=%v err=%v", tc.want, s, found, err)
			}
		})
	}
	for _, tc := range []struct {
		name, field, want string
		value             any
	}{
		{"reversal", "trade_type", "unsupported trade_type", "REVERSAL"},
		{"correction", "trade_type", "unsupported trade_type", "CORRECTION"},
		{"synthetic", "trade_type", "unsupported trade_type", "SYNTHETIC"},
		{"unknown-type", "trade_type", "unsupported trade_type", "future"},
		{"quote-size", "size_in_quote", "unusable fill identity or size_in_quote", true},
		{"empty-id", "entry_id", "unusable fill identity or size_in_quote", ""},
		{"missing-trade-id", "trade_id", "unusable fill identity or size_in_quote", nil},
		{"wrong-order", "order_id", "unusable fill identity or size_in_quote", "other"},
		{"bad-time", "trade_time", "unreadable fill trade_time", "bad"},
		{"zero-size", "size", "unreadable fill decimals", "0"},
		{"negative-size", "size", "unreadable fill decimals", "-1"},
		{"bad-size", "size", "unreadable fill decimals", "bad"},
		{"zero-price", "price", "unreadable fill decimals", "0"},
		{"bad-price", "price", "unreadable fill decimals", "bad"},
		{"negative-fee", "commission", "unreadable fill decimals", "-0.01"},
		{"bad-fee", "commission", "unreadable fill decimals", "bad"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, f := newCoinbaseLookupTest(t)
			fill := coinbaseRESTFill("entry-a", "0.5")
			if tc.value == nil {
				delete(fill, tc.field)
			} else {
				fill[tc.field] = tc.value
			}
			f.fillPages[0]["fills"] = []any{fill}
			s, found, err := c.Lookup(context.Background(), coinbaseTestRef())
			if !errors.Is(err, domain.ErrUpstream) || !strings.Contains(err.Error(), tc.want) || found || len(s.Fills) != 0 {
				t.Fatalf("want %q; snapshot=%+v err=%v", tc.want, s, err)
			}
		})
	}
}

func TestCoinbaseLookupPagesAndProof(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		pages      []map[string]any
	}{
		{"empty-order-cursor", "order cursor did not advance", []map[string]any{
			{"orders": []any{}, "has_next": true, "cursor": ""},
		}},
		{"repeated-order-cursor", "order cursor did not advance", []map[string]any{
			{"orders": []any{}, "has_next": true, "cursor": "1"},
			{"orders": []any{}, "has_next": true, "cursor": "1"},
		}},
		{"missing-orders", "incomplete order list", []map[string]any{
			{"has_next": false},
		}},
		{"missing-has-next", "incomplete order list", []map[string]any{
			{"orders": []any{}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan struct{}, len(tc.pages)+1)
			c := coinbaseTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v3/brokerage/orders/historical/batch" {
					t.Errorf("unexpected request %s", r.URL)
				}
				select {
				case requests <- struct{}{}:
				default:
					t.Error("too many order page requests")
				}
				page := len(requests) - 1
				if page >= len(tc.pages) {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				writeJSON(t, w, tc.pages[page])
			})
			ref := coinbaseTestRef()
			ref.VenueOrderID = ""
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, found, err := c.Lookup(ctx, ref)
			if !errors.Is(err, domain.ErrUpstream) || found || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q and found=false; found=%v err=%v", tc.want, found, err)
			}
			if got := len(requests); got != len(tc.pages) {
				t.Fatalf("order page requests=%d, want %d", got, len(tc.pages))
			}
		})
	}
	t.Run("identical-replay", func(t *testing.T) {
		c, f := newCoinbaseLookupTest(t)
		f.fillPages[0]["cursor"] = "1"
		f.fillPages = append(f.fillPages, map[string]any{"fills": []any{coinbaseRESTFill("entry-a", "0.5")}, "cursor": ""})
		s, found, err := c.Lookup(context.Background(), coinbaseTestRef())
		if err != nil || !found || len(s.Fills) != 1 {
			t.Fatalf("replay: %+v %v", s, err)
		}
	})
	for _, count := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("client-matches-%d", count), func(t *testing.T) {
			c, f := newCoinbaseLookupTest(t)
			first := []any{}
			second := []any{}
			if count > 0 {
				first = append(first, f.order)
			}
			if count > 1 {
				second = append(second, f.order)
			}
			other := coinbaseRESTOrder()
			other["client_order_id"] = "not-ours"
			first = append(first, other)
			f.orderPages = []map[string]any{{"orders": first, "has_next": true, "cursor": "1"}, {"orders": second, "has_next": false, "cursor": ""}}
			ref := coinbaseTestRef()
			ref.VenueOrderID = ""
			s, found, err := c.Lookup(context.Background(), ref)
			if count == 2 {
				if err == nil || found {
					t.Fatalf("ambiguous match accepted: %+v %v", s, err)
				}
				return
			}
			if err != nil || found != (count == 1) {
				t.Fatalf("client lookup: %+v found=%v err=%v", s, found, err)
			}
		})
	}
	for _, source := range []string{"fills", "orders"} {
		t.Run("proof-"+source, func(t *testing.T) {
			c, f := newCoinbaseLookupTest(t)
			ref := coinbaseTestRef()
			if source == "fills" {
				f.order["filled_size"] = "0"
				f.fillPages[0] = map[string]any{"proof_token_required": true, "fills": []any{}}
			} else {
				ref.VenueOrderID = ""
				f.orderPages = []map[string]any{{"proof_token_required": true, "orders": []any{}, "has_next": false}}
			}
			var ds []health.Diagnostic
			c.SetDiagnosticReporter(func(d health.Diagnostic) { ds = append(ds, d) })
			s, found, err := c.Lookup(context.Background(), ref)
			if err == nil || found || len(s.Fills) != 0 || !strings.Contains(err.Error(), "strong customer authentication (proof token) to list "+source) {
				t.Fatalf("SCA must retain pending: %+v found=%v err=%v", s, found, err)
			}
			if len(ds) != 1 || ds[0].Code != health.CodeInvalidProviderConfig || ds[0].Level != health.DiagError || ds[0].Kind != health.DiagKindProvider || !strings.Contains(ds[0].Remediation, "pending") || !strings.Contains(ds[0].Remediation, "SCA settings") {
				t.Fatalf("SCA diagnostic: %+v", ds)
			}
		})
	}
	t.Run("decoded-404", func(t *testing.T) {
		c, f := newCoinbaseLookupTest(t)
		f.getStatus = 404
		_, found, err := c.Lookup(context.Background(), coinbaseTestRef())
		if err != nil || found {
			t.Fatalf("not found: %v %v", found, err)
		}
	})
	t.Run("server-failure", func(t *testing.T) {
		c, f := newCoinbaseLookupTest(t)
		f.getStatus = 500
		_, found, err := c.Lookup(context.Background(), coinbaseTestRef())
		if err == nil || found {
			t.Fatalf("failure became absence: %v %v", found, err)
		}
	})
	t.Run("requires-reporter", func(t *testing.T) {
		c, _ := newCoinbaseLookupTest(t)
		c.SetDiagnosticReporter(nil)
		_, _, err := c.Lookup(context.Background(), coinbaseTestRef())
		if err == nil {
			t.Fatal("lookup without reporter accepted")
		}
	})
}

func TestCoinbaseUnparsableResponseDiagnostics(t *testing.T) {
	for _, operation := range []string{"account", "get-order", "list-orders", "list-fills"} {
		t.Run(operation, func(t *testing.T) {
			c := coinbaseTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
				if operation == "list-fills" && r.URL.Path == "/api/v3/brokerage/orders/historical/venue-order" {
					writeJSON(t, w, map[string]any{"order": coinbaseRESTOrder()})
					return
				}
				wantPath := map[string]string{
					"account":     "/api/v3/brokerage/key_permissions",
					"get-order":   "/api/v3/brokerage/orders/historical/venue-order",
					"list-orders": "/api/v3/brokerage/orders/historical/batch",
					"list-fills":  "/api/v3/brokerage/orders/historical/fills",
				}[operation]
				if r.URL.Path != wantPath {
					t.Errorf("unexpected request %s, want %s", r.URL.Path, wantPath)
				}
				_, _ = w.Write([]byte(`{"unreadable":`))
			})
			var diagnostics []health.Diagnostic
			c.SetDiagnosticReporter(func(d health.Diagnostic) { diagnostics = append(diagnostics, d) })
			var err error
			if operation == "account" {
				_, err = c.CheckAccount(context.Background())
			} else {
				ref := coinbaseTestRef()
				if operation == "list-orders" {
					ref.VenueOrderID = ""
				}
				var found bool
				_, found, err = c.Lookup(context.Background(), ref)
				if found {
					t.Fatal("unreadable lookup returned found=true")
				}
			}
			if !errors.Is(err, coinbase.ErrUnparsableResponse) {
				t.Fatalf("want wrapped unreadable response error, got %v", err)
			}
			if operation == "account" {
				if len(diagnostics) != 0 {
					t.Fatalf("account emitted lookup diagnostic: %+v", diagnostics)
				}
				return
			}
			if len(diagnostics) != 1 || diagnostics[0].Code != health.CodeUnparsableData ||
				diagnostics[0].Detail != "An order lookup returned an unreadable Coinbase response." {
				t.Fatalf("lookup diagnostic: %+v", diagnostics)
			}
		})
	}
}

func TestCoinbaseRequestErrorClass(t *testing.T) {
	for _, mode := range []string{"transport", "api", "decode"} {
		t.Run(mode, func(t *testing.T) {
			c := coinbaseTestConnector(t, func(w http.ResponseWriter, _ *http.Request) {
				if mode == "api" {
					w.WriteHeader(http.StatusForbidden)
					writeJSON(t, w, map[string]string{"error": "FORBIDDEN", "message": "denied"})
				} else {
					_, _ = w.Write([]byte(`{`))
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "transport" {
				cancel()
			}
			var response any
			_, clientErr := c.client.Do(ctx, http.MethodGet, "/api/v3/brokerage/key_permissions", nil, nil, &response)
			_, err := c.CheckAccount(ctx)
			if err == nil || clientErr == nil || reflect.TypeOf(err) != reflect.TypeOf(clientErr) || err.Error() != clientErr.Error() {
				t.Fatalf("connector changed client error: %T %v; client: %T %v", err, err, clientErr, clientErr)
			}
		})
	}
}

func TestCoinbaseAccountSymbolAndRedaction(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		c := coinbaseTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v3/brokerage/key_permissions" {
				t.Error(r.URL)
			}
			writeJSON(t, w, map[string]any{"portfolio_uuid": "portfolio", "portfolio_type": "DEFAULT", "can_trade": enabled})
		})
		info, err := c.CheckAccount(context.Background())
		want := "DEFAULT"
		if !enabled {
			want += ", trading disabled"
		}
		if err != nil || info.Identity != "portfolio" || info.Status != want {
			t.Fatalf("account: %+v %v", info, err)
		}
	}
	c := coinbaseTestConnector(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"portfolio_uuid": ""})
	})
	if _, err := c.CheckAccount(context.Background()); !errors.Is(err, domain.ErrUpstream) {
		t.Fatal(err)
	}
	for _, status := range []int{200, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := coinbaseTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("get_tradability_status") != "true" {
					t.Error(r.URL)
				}
				w.WriteHeader(status)
				if status == 200 {
					writeJSON(t, w, coinbaseTestProduct())
				} else {
					writeJSON(t, w, map[string]string{"error": "NOT_FOUND", "message": "not found"})
				}
			})
			v, err := c.VerifySymbol(context.Background(), "BTC-USD")
			if status == 500 {
				if err == nil || v.Exists {
					t.Fatal("venue failure became absence")
				}
			} else if err != nil || v.Exists != (status == 200) || v.Tradable != (status == 200) {
				t.Fatalf("verify: %+v %v", v, err)
			}
			if status == 200 && v.Details != "BTC/USD online" {
				t.Fatalf("verify details: %q", v.Details)
			}
			if status == 404 && v.Details != "" {
				t.Fatalf("absent product details: %q", v.Details)
			}
			if status != 200 {
				err := c.PreCheck(context.Background(), coinbaseTestOrder())
				if err == nil || errors.Is(err, fwtrading.ErrRejected) != (status == 404) {
					t.Fatalf("product rejection: %v", err)
				}
			}
		})
	}
	t.Run("venue-text", func(t *testing.T) {
		c := coinbaseTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			writeJSON(t, w, map[string]any{"success": false, "error_response": map[string]string{"new_order_failure_reason": "INVALID_REQUEST", "message": coinbaseTestAPIKeyID + " " + token}})
		})
		_, err := c.Send(context.Background(), coinbaseTestOrder())
		if err == nil || strings.Contains(err.Error(), coinbaseTestAPIKeyID) || strings.Contains(err.Error(), "eyJ") {
			t.Fatalf("secret in error: %v", err)
		}
	})
}

func TestCoinbaseSearchSymbols(t *testing.T) {
	for _, cursor := range []string{"ok", "empty", "repeat", "no-pagination"} {
		t.Run(cursor, func(t *testing.T) {
			calls := 0
			c := coinbaseTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				q := r.URL.Query()
				if q.Get("product_type") != "SPOT" || q.Get("get_tradability_status") != "true" {
					t.Error(r.URL)
				}
				p := coinbaseTestProduct()
				next := "next"
				if q.Get("cursor") != "" {
					p["product_id"], p["quote_currency_id"] = "BTC-EUR", "EUR"
					if cursor != "repeat" {
						next = ""
					}
				}
				if cursor == "empty" {
					next = ""
				}
				response := map[string]any{"products": []any{p}}
				if cursor != "no-pagination" {
					response["pagination"] = map[string]any{"has_next": next != "" || cursor == "empty", "next_cursor": next}
				}
				writeJSON(t, w, response)
			})
			empty, err := c.SearchSymbols(context.Background(), "  ")
			if err != nil || empty == nil || len(empty) != 0 || calls != 0 {
				t.Fatal("empty query contacted venue")
			}
			matches, err := c.SearchSymbols(context.Background(), "btc")
			if cursor == "empty" || cursor == "repeat" {
				if !errors.Is(err, domain.ErrUpstream) {
					t.Fatalf("invalid cursor accepted: %v", err)
				}
				return
			}
			want := 2
			if cursor == "no-pagination" {
				want = 1
			}
			if err != nil || len(matches) != want {
				t.Fatalf("matches=%+v err=%v", matches, err)
			}
			for _, m := range matches {
				if m.BaseAsset != "BTC" || m.QuoteAsset == "" || !m.Tradable || m.Class != "crypto" || m.Exchange != "Coinbase" {
					t.Fatalf("match: %+v", m)
				}
			}
		})
	}
}

func TestCoinbaseRandomizedFillExactlyOnce(t *testing.T) {
	for _, seed := range []int64{1, 7, 29, 61, 109, 997} {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			c, f := newCoinbaseLookupTest(t)
			var canonical []any
			var want []fwtrading.Fill
			cum := decimal.Zero
			count := 3 + rng.Intn(15)
			for i := 0; i < count; i++ {
				qty := decimal.NewFromInt(int64(1 + rng.Intn(9))).Shift(-3)
				price := decimal.NewFromInt(int64(100 + rng.Intn(200))).Shift(-1)
				fee := decimal.NewFromInt(int64(rng.Intn(10))).Shift(-4)
				fill := coinbaseRESTFill(fmt.Sprintf("entry-%03d", i), qty.String())
				at := time.Date(2026, 11, 12, 10, 0, i/2, 0, time.UTC)
				fill["trade_time"], fill["price"], fill["commission"] = at.Format(time.RFC3339), price.String(), fee.String()
				canonical = append(canonical, fill)
				cum = cum.Add(qty)
				want = append(want, fwtrading.Fill{VenueTradeID: fmt.Sprintf("trade-entry-%03d", i), Quantity: qty.String(), Price: price.String(), CumQuantity: cum.String(), LeavesQuantity: decimal.NewFromInt(1).Sub(cum).String(), At: at, Commission: &domain.Commission{Amount: fee.String(), Currency: "USD"}, CommissionLeg: fwtrading.CommissionLegQuote})
			}
			f.order["filled_size"] = cum.String()
			for split := 0; split < 20; split++ {
				replayed := append([]any(nil), canonical...)
				for range rng.Intn(30) {
					replayed = append(replayed, canonical[rng.Intn(count)])
				}
				rng.Shuffle(len(replayed), func(i, j int) { replayed[i], replayed[j] = replayed[j], replayed[i] })
				f.fillPages = nil
				for len(replayed) > 0 {
					n := 1 + rng.Intn(len(replayed))
					f.fillPages = append(f.fillPages, map[string]any{"fills": append([]any(nil), replayed[:n]...), "cursor": strconv.Itoa(len(f.fillPages) + 1)})
					replayed = replayed[n:]
				}
				f.fillPages[len(f.fillPages)-1]["cursor"] = ""
				s, found, err := c.Lookup(context.Background(), coinbaseTestRef())
				if err != nil || !found || !reflect.DeepEqual(s.Fills, want) {
					t.Fatalf("split=%d fill conservation/order: got=%+v want=%+v err=%v", split, s.Fills, want, err)
				}
				conflict := map[string]any{}
				chosen := canonical[rng.Intn(count)].(map[string]any)
				for k, v := range chosen {
					conflict[k] = v
				}
				conflict["commission"] = "999"
				f.fillPages[0]["fills"] = append(f.fillPages[0]["fills"].([]any), chosen, conflict)
				if _, found, err := c.Lookup(context.Background(), coinbaseTestRef()); err == nil || found {
					t.Fatalf("split=%d conflicting replay booked", split)
				}
			}
		})
	}
}
