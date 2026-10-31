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
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/binance"
)

const binanceTestCredentials = `{"apiKey":"test-key","apiSecret":"test-secret"}`
const binanceTestOrderID = "9007199254740993"

func binanceTestSymbol() binance.Symbol {
	return binance.Symbol{
		Symbol: "ETHUSDT", BaseAsset: "ETH", QuoteAsset: "USDT",
		Status: "TRADING", IsSpotTradingAllowed: true,
		OrderTypes: []string{"LIMIT", "MARKET"},
	}
}

func binanceTestOrder() fwtrading.Order {
	return fwtrading.Order{
		ClientOrderID: "officer-client", Symbol: "ETHUSDT",
		Side:       domain.OrderSide("buy"),
		AmountKind: domain.OrderAmountKindQuantity,
		Quantity:   "2", LimitPrice: "10", Route: `{"timeInForce":"GTC"}`,
	}
}

func newBinanceTest(t *testing.T,
	handler http.HandlerFunc,
) *binanceConnector {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := newBinanceConnector(domain.TradingConnection{
		Credentials: binanceTestCredentials,
	}, server.URL, "ws"+strings.TrimPrefix(server.URL, "http")+"/stream")
	if err != nil {
		t.Fatal(err)
	}
	c.SetStatusReporter(func(bool, string) {})
	c.SetDiagnosticReporter(func(health.Diagnostic) {})
	t.Cleanup(c.Close)
	return c
}

func writeBinanceJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func writeBinanceSymbol(t *testing.T, w http.ResponseWriter,
	symbol binance.Symbol,
) {
	t.Helper()
	writeBinanceJSON(t, w, binance.ExchangeInfo{
		Symbols: []binance.Symbol{symbol},
	})
}

func TestBinanceConstruction(t *testing.T) {
	for _, tc := range []struct {
		mode     domain.TradingMode
		rest, ws string
	}{
		{domain.TradingModePaper,
			binance.TestnetRESTURL, binance.TestnetStreamURL},
		{domain.TradingModeLive, binance.LiveRESTURL, binance.LiveStreamURL},
	} {
		connector, err := NewBinanceConnector(domain.TradingConnection{
			Mode: tc.mode, Credentials: binanceTestCredentials,
		})
		if err != nil {
			t.Fatal(err)
		}
		c := connector.(*binanceConnector)
		defer c.Close()
		if c.restURL != tc.rest || c.streamURL != tc.ws ||
			c.httpClient.Timeout != 10*time.Second {
			t.Fatal("wrong mode endpoints or HTTP timeout")
		}
	}
	for _, tc := range []struct {
		mode        domain.TradingMode
		credentials string
		pointer     string
	}{
		{"", binanceTestCredentials, ""},
		{"other", binanceTestCredentials, ""},
		{domain.TradingModePaper, `{}`, "/credentials/apiKey"},
		{domain.TradingModePaper, `{"apiKey":"k"}`,
			"/credentials/apiSecret"},
		{domain.TradingModePaper, `{`, "/credentials"},
		{domain.TradingModePaper, `{"extra":"x"}`, "/credentials"},
	} {
		_, err := NewBinanceConnector(domain.TradingConnection{
			Mode: tc.mode, Credentials: tc.credentials,
		})
		if err == nil || domain.ValidationPointer(err) != tc.pointer {
			t.Fatalf("error = %v; want pointer %q", err, tc.pointer)
		}
	}
}

func TestBinanceProvider(t *testing.T) {
	p := BinanceProvider()
	if p.Type != domain.TradingProviderBinance || p.Title != "Binance" {
		t.Fatal("Binance provider identity differs")
	}
	if p.VenueAccounts {
		t.Fatal("Binance must not advertise venue accounts")
	}
	if !p.VerifiesSymbols {
		t.Fatal("Binance must advertise symbol verification")
	}
	if !p.SearchesSymbols {
		t.Fatal("Binance must advertise symbol search")
	}
	if p.UnlinkedFees {
		t.Fatal("Binance must not advertise unlinked fees")
	}
	if p.Build == nil {
		t.Fatal("Binance provider must have a constructor")
	}
	connector, err := p.Build(domain.TradingConnection{
		Mode: domain.TradingModePaper, Credentials: binanceTestCredentials,
	})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := connector.(*binanceConnector)
	if !ok || c == nil {
		t.Fatal("Binance provider must build a Binance connector")
	}
	defer c.Close()
}

func TestBinanceRouteAndOrderValidation(t *testing.T) {
	var calls atomic.Int32
	c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeBinanceSymbol(t, w, binanceTestSymbol())
	})
	for _, limit := range []bool{true, false} {
		for _, route := range []string{
			`{`, `null`, `[]`, `{"unknown":"GTC"}`,
			`{"timeInForce":"bad"}`, `{"timeInForce":1}`,
		} {
			order := binanceTestOrder()
			if !limit {
				order.LimitPrice = ""
			}
			order.Route = route
			before := calls.Load()
			err := c.PreCheck(context.Background(), order)
			if !errors.Is(err, fwtrading.ErrRejected) {
				t.Fatalf("route %q: %v", route, err)
			}
			if calls.Load() != before {
				t.Fatal("invalid route reached the venue")
			}
		}
	}
	for _, tc := range []struct {
		limit bool
		route string
		valid bool
	}{
		{true, `{}`, false},
		{true, `{"timeInForce":"GTC"}`, true},
		{true, `{"timeInForce":"IOC"}`, true},
		{true, `{"timeInForce":"FOK"}`, true},
		{false, `{}`, true},
		{false, `{"timeInForce":"GTC"}`, false},
		{false, `{"timeInForce":""}`, false},
	} {
		order := binanceTestOrder()
		order.Route = tc.route
		if !tc.limit {
			order.LimitPrice = ""
		}
		err := c.PreCheck(context.Background(), order)
		if (err == nil) != tc.valid {
			t.Fatalf("route %s limit=%t: %v", tc.route, tc.limit, err)
		}
	}
	for _, field := range []string{"quantity", "price"} {
		for _, value := range []string{
			"0", "-1", "1e3", "NaN", " 1", "+1", "1e-2147483647",
			strings.Repeat("1", binanceDecimalMaxLength+1),
		} {
			order := binanceTestOrder()
			if field == "quantity" {
				order.Quantity = value
			} else {
				order.LimitPrice = value
			}
			err := c.PreCheck(context.Background(), order)
			if !errors.Is(err, fwtrading.ErrRejected) {
				t.Fatalf("%s %q: %v", field, value, err)
			}
		}
	}
	order := binanceTestOrder()
	order.AmountKind = "notional"
	err := c.PreCheck(context.Background(), order)
	if !errors.Is(err, fwtrading.ErrUnsupported) {
		t.Fatalf("amount kind: %v", err)
	}
}

func TestBinancePreCheckCapabilities(t *testing.T) {
	for _, name := range []string{"missing", "halted", "not spot", "type"} {
		t.Run(name, func(t *testing.T) {
			symbol := binanceTestSymbol()
			switch name {
			case "halted":
				symbol.Status = "BREAK"
			case "not spot":
				symbol.IsSpotTradingAllowed = false
			case "type":
				symbol.OrderTypes = []string{"MARKET"}
			}
			c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
				if name == "missing" {
					w.WriteHeader(400)
					_, _ = fmt.Fprint(w, `{"code":-1121,"msg":"Invalid symbol"}`)
					return
				}
				writeBinanceSymbol(t, w, symbol)
			})
			err := c.PreCheck(context.Background(), binanceTestOrder())
			if !errors.Is(err, fwtrading.ErrRejected) {
				t.Fatal(err)
			}
		})
	}
}

func TestBinancePreCheckFilters(t *testing.T) {
	for _, tc := range []struct {
		name, filter, qty, price string
		rejected, upstream       bool
	}{
		{"price below", `{"filterType":"PRICE_FILTER","minPrice":"2",` +
			`"maxPrice":"4","tickSize":"0.5"}`, "2", "1.5", true, false},
		{"price min", `{"filterType":"PRICE_FILTER","minPrice":"2",` +
			`"maxPrice":"4","tickSize":"0.5"}`, "2", "2", false, false},
		{"price max", `{"filterType":"PRICE_FILTER","minPrice":"2",` +
			`"maxPrice":"4","tickSize":"0.5"}`, "2", "4", false, false},
		{"price above", `{"filterType":"PRICE_FILTER","minPrice":"2",` +
			`"maxPrice":"4","tickSize":"0.5"}`, "2", "4.5", true, false},
		{"price tick", `{"filterType":"PRICE_FILTER","minPrice":"2.1",` +
			`"maxPrice":"4","tickSize":"0.5"}`, "2", "2.6", false, false},
		{"price off tick", `{"filterType":"PRICE_FILTER","minPrice":"2.1",` +
			`"maxPrice":"4","tickSize":"0.5"}`, "2", "2.5", true, false},
		{"disabled price", `{"filterType":"PRICE_FILTER","minPrice":"0",` +
			`"maxPrice":"0","tickSize":"0"}`, "2", "999", false, false},
		{"lot below", `{"filterType":"LOT_SIZE","minQty":"2.1",` +
			`"maxQty":"4.1","stepSize":"0.5"}`, "1.6", "10", true, false},
		{"lot min", `{"filterType":"LOT_SIZE","minQty":"2.1",` +
			`"maxQty":"4.1","stepSize":"0.5"}`, "2.1", "10", false, false},
		{"lot max", `{"filterType":"LOT_SIZE","minQty":"2.1",` +
			`"maxQty":"4.1","stepSize":"0.5"}`, "4.1", "10", false, false},
		{"lot above", `{"filterType":"LOT_SIZE","minQty":"2.1",` +
			`"maxQty":"4.1","stepSize":"0.5"}`, "4.6", "10", true, false},
		{"lot step", `{"filterType":"LOT_SIZE","minQty":"2.1",` +
			`"maxQty":"4.1","stepSize":"0.5"}`, "2.6", "10", false, false},
		{"lot off step", `{"filterType":"LOT_SIZE","minQty":"2.1",` +
			`"maxQty":"4.1","stepSize":"0.5"}`, "2.5", "10", true, false},
		{"lot no max", `{"filterType":"LOT_SIZE","minQty":"0",` +
			`"maxQty":"0","stepSize":"0"}`, "100", "10", false, false},
		{"market below", `{"filterType":"MARKET_LOT_SIZE","minQty":"2",` +
			`"maxQty":"4","stepSize":"0.5"}`, "1.5", "", true, false},
		{"market min", `{"filterType":"MARKET_LOT_SIZE","minQty":"2",` +
			`"maxQty":"4","stepSize":"0.5"}`, "2", "", false, false},
		{"market max", `{"filterType":"MARKET_LOT_SIZE","minQty":"2",` +
			`"maxQty":"4","stepSize":"0.5"}`, "4", "", false, false},
		{"market above", `{"filterType":"MARKET_LOT_SIZE","minQty":"2",` +
			`"maxQty":"4","stepSize":"0.5"}`, "4.5", "", true, false},
		{"market off step", `{"filterType":"MARKET_LOT_SIZE","minQty":"2",` +
			`"maxQty":"4","stepSize":"0.5"}`, "2.1", "", true, false},
		{"market disabled", `{"filterType":"MARKET_LOT_SIZE","minQty":"2",` +
			`"maxQty":"4","stepSize":"0"}`, "100", "", false, false},
		{"market ignores lot", `{"filterType":"LOT_SIZE",` +
			`"minQty":"bad"}`, "2", "", false, false},
		{"notional below", `{"filterType":"NOTIONAL","minNotional":"20",` +
			`"maxNotional":"40"}`, "1", "10", true, false},
		{"notional min", `{"filterType":"NOTIONAL","minNotional":"20",` +
			`"maxNotional":"40"}`, "2", "10", false, false},
		{"notional max", `{"filterType":"NOTIONAL","minNotional":"20",` +
			`"maxNotional":"40"}`, "4", "10", false, false},
		{"notional above", `{"filterType":"NOTIONAL","minNotional":"20",` +
			`"maxNotional":"40"}`, "5", "10", true, false},
		{"notional no max", `{"filterType":"NOTIONAL",` +
			`"minNotional":"20"}`, "10", "10", false, false},
		{"notional zero max", `{"filterType":"NOTIONAL",` +
			`"minNotional":"20","maxNotional":"0"}`, "10", "10", false, false},
		{"minimum below", `{"filterType":"MIN_NOTIONAL",` +
			`"minNotional":"20","applyToMarket":true}`, "1", "10", true, false},
		{"minimum equal", `{"filterType":"MIN_NOTIONAL",` +
			`"minNotional":"20","applyToMarket":true}`, "2", "10", false, false},
		{"market notional", `{"filterType":"NOTIONAL",` +
			`"minNotional":"bad"}`, "1", "", false, false},
		{"market minimum", `{"filterType":"MIN_NOTIONAL",` +
			`"minNotional":"bad"}`, "1", "", false, false},
		{"unknown", `{"filterType":"FUTURE_RULE","minQty":{}}`,
			"2", "10", false, false},
		{"bad decimal", `{"filterType":"LOT_SIZE","minQty":"bad",` +
			`"maxQty":"4","stepSize":"1"}`, "2", "10", false, true},
		{"bad type", `{"filterType":"PRICE_FILTER","minPrice":true}`,
			"2", "10", false, true},
		{"bad max", `{"filterType":"NOTIONAL","minNotional":"1",` +
			`"maxNotional":"bad"}`, "2", "10", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			symbol := binanceTestSymbol()
			symbol.Filters = []json.RawMessage{json.RawMessage(tc.filter)}
			c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
				writeBinanceSymbol(t, w, symbol)
			})
			order := binanceTestOrder()
			order.Quantity, order.LimitPrice = tc.qty, tc.price
			if tc.price == "" {
				order.Route = `{}`
			}
			err := c.PreCheck(context.Background(), order)
			if errors.Is(err, fwtrading.ErrRejected) != tc.rejected ||
				errors.Is(err, domain.ErrUpstream) != tc.upstream ||
				(err == nil) != (!tc.rejected && !tc.upstream) {
				t.Fatalf("rejected=%t upstream=%t: %v",
					tc.rejected, tc.upstream, err)
			}
		})
	}
}

func TestBinanceSend(t *testing.T) {
	for _, limit := range []bool{false, true} {
		for _, side := range []domain.OrderSide{"buy", "sell"} {
			order := binanceTestOrder()
			order.Side = side
			if !limit {
				order.Route, order.LimitPrice = `{}`, ""
			}
			var calls atomic.Int32
			c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				q := r.URL.Query()
				if r.Method != "POST" || r.URL.Path != "/api/v3/order" ||
					q.Get("symbol") != order.Symbol ||
					q.Get("side") != strings.ToUpper(string(side)) ||
					q.Get("quantity") != order.Quantity ||
					q.Get("newClientOrderId") != order.ClientOrderID ||
					q.Get("newOrderRespType") != "ACK" ||
					q.Get("signature") == "" || q.Get("timestamp") == "" ||
					r.Header.Get("X-MBX-APIKEY") != "test-key" {
					t.Error("incorrect signed order request")
				}
				if limit {
					if q.Get("type") != "LIMIT" || q.Get("price") != "10" ||
						q.Get("timeInForce") != "GTC" {
						t.Error("incorrect limit fields")
					}
				} else if q.Get("type") != "MARKET" || q.Has("price") ||
					q.Has("timeInForce") {
					t.Error("incorrect market fields")
				}
				_, _ = fmt.Fprintf(w, `{"orderId":%s,"clientOrderId":%q}`,
					binanceTestOrderID, order.ClientOrderID)
			})
			ack, err := c.Send(context.Background(), order)
			if err != nil || ack.VenueOrderID != binanceTestOrderID ||
				calls.Load() != 1 {
				t.Fatalf("ack=%+v calls=%d: %v", ack, calls.Load(), err)
			}
		}
	}
}

func TestBinanceSendOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		rejected   bool
	}{
		{"filter", `{"code":-1013,"msg":"Filter failure"}`, 400, true},
		{"balance", `{"code":-2010,"msg":"Insufficient balance"}`, 400, true},
		{"auth", `{"code":-2015,"msg":"Invalid key"}`, 401, true},
		{"throttle", `{"code":-1003,"msg":"Too many requests"}`, 429, true},
		{"timeout code", `{"code":-1007,"msg":"Timeout"}`, 400, false},
		{"timeout code 500", `{"code":-1007,"msg":"Timeout"}`, 500, false},
		{"unknown execution", `{"code":-1006,"msg":"Unexpected response"}`,
			400, false},
		{"unknown execution 500", `{"code":-1006,"msg":"Unexpected response"}`,
			500, false},
		{"duplicate", `{"code":-2010,"msg":"Duplicate order sent."}`,
			400, false},
		{"unknown body", `<html>failure</html>`, 400, false},
		{"missing code", `{"msg":"Filter failure"}`, 400, false},
		{"missing message", `{"code":-1013}`, 400, false},
		{"408", `{"code":-1013,"msg":"Filter failure"}`, 408, false},
		{"500", `{"code":-1013,"msg":"Filter failure"}`, 500, false},
		{"missing id", `{"clientOrderId":"officer-client"}`, 200, false},
		{"bad id", `{"orderId":1.5,"clientOrderId":"officer-client"}`,
			200, false},
		{"mismatched client", `{"orderId":1,"clientOrderId":"other"}`,
			200, false},
		{"bad ack", `{`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			})
			ack, err := c.Send(context.Background(), binanceTestOrder())
			if err == nil || ack.VenueOrderID != "" ||
				errors.Is(err, fwtrading.ErrRejected) != tc.rejected ||
				calls.Load() != 1 {
				t.Fatalf("rejected=%t calls=%d ack=%+v: %v",
					tc.rejected, calls.Load(), ack, err)
			}
		})
	}
	t.Run("transport", func(t *testing.T) {
		c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
		})
		_, err := c.Send(context.Background(), binanceTestOrder())
		if err == nil || errors.Is(err, fwtrading.ErrRejected) {
			t.Fatalf("transport must leave outcome unknown: %v", err)
		}
	})
}

func binanceLookupOrder(status, qty, filled string) binanceOrder {
	return binanceOrder{
		ID: json.Number(binanceTestOrderID), ClientOrderID: "officer-client",
		Status: status, Quantity: qty, Filled: filled,
	}
}

func binanceTestTrade(id int, qty, asset string) binanceTrade {
	tradeID := int64(id)
	return binanceTrade{
		ID:       &tradeID,
		OrderID:  json.Number(binanceTestOrderID),
		Quantity: qty, Price: "10.00", Commission: "0.01",
		CommissionAsset: asset, At: 1700000000000 + int64(id),
	}
}

func TestBinanceLookupStatusesAndReferences(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   fwtrading.VenueStatus
	}{
		{"NEW", fwtrading.VenueStatusOpen},
		{"PENDING_NEW", fwtrading.VenueStatusOpen},
		{"PARTIALLY_FILLED", fwtrading.VenueStatusOpen},
		{"PENDING_CANCEL", fwtrading.VenueStatusOpen},
		{"FILLED", fwtrading.VenueStatusFilled},
		{"CANCELED", fwtrading.VenueStatusCancelled},
		{"REJECTED", fwtrading.VenueStatusCancelled},
		{"EXPIRED", fwtrading.VenueStatusCancelled},
		{"EXPIRED_IN_MATCH", fwtrading.VenueStatusCancelled},
		{"future test-key", ""},
	} {
		t.Run(tc.status, func(t *testing.T) {
			for _, byID := range []bool{false, true} {
				c := newBinanceTest(t,
					func(w http.ResponseWriter, r *http.Request) {
						q := r.URL.Query()
						if q.Get("symbol") != "ETHUSDT" {
							t.Error("missing lookup symbol")
						}
						if r.URL.Path == "/api/v3/order" {
							if byID {
								if q.Get("orderId") != binanceTestOrderID ||
									q.Get("origClientOrderId") != "officer-client" {
									t.Error("lookup missing venue or original client id")
								}
							} else if q.Get("origClientOrderId") !=
								"officer-client" || q.Has("orderId") {
								t.Error("lookup missing original client id")
							}
							writeBinanceJSON(t, w,
								binanceLookupOrder(tc.status, "2", "0"))
							return
						}
						writeBinanceJSON(t, w, []binanceTrade{})
					})
				var diagnostics []health.Diagnostic
				c.SetDiagnosticReporter(func(d health.Diagnostic) {
					diagnostics = append(diagnostics, d)
				})
				ref := fwtrading.OrderRef{
					Symbol: "ETHUSDT", ClientOrderID: "officer-client",
				}
				if byID {
					ref.VenueOrderID = binanceTestOrderID
				}
				snapshot, found, err := c.Lookup(context.Background(), ref)
				if tc.want == "" {
					if !errors.Is(err, domain.ErrUpstream) || found ||
						len(diagnostics) != 1 ||
						strings.Contains(err.Error(), "test-key") ||
						diagnostics[0].Code != health.CodeUnparsableData {
						t.Fatalf("unknown status: %v %+v", err, diagnostics)
					}
				} else if err != nil || !found || snapshot.Status != tc.want ||
					snapshot.VenueOrderID != binanceTestOrderID ||
					snapshot.FilledQuantity != "0" {
					t.Fatalf("snapshot=%+v found=%t: %v", snapshot, found, err)
				}
			}
		})
	}
}

func TestBinanceLookupFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		notFound   bool
	}{
		{"absent", `{"code":-2013,"msg":"Order does not exist"}`, 400, true},
		{"other", `{"code":-2015,"msg":"Invalid key"}`, 400, false},
		{"wrong status", `{"code":-2013,"msg":"Unavailable"}`, 500, false},
		{"bad body", `{`, 200, false},
		{"missing id", `{}`, 200, false},
		{"bad qty", `{"orderId":1,"status":"NEW","origQty":"bad",` +
			`"executedQty":"0"}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			})
			_, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{
				Symbol: "ETHUSDT", ClientOrderID: "officer-client",
			})
			if found || (err == nil) != tc.notFound {
				t.Fatalf("found=%t: %v", found, err)
			}
		})
	}
	c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid lookup reached venue")
	})
	for _, ref := range []fwtrading.OrderRef{
		{ClientOrderID: "officer-client"}, {Symbol: "ETHUSDT"},
	} {
		_, _, err := c.Lookup(context.Background(), ref)
		if err == nil {
			t.Fatal("missing reference must be an error")
		}
	}
	c.SetDiagnosticReporter(nil)
	if _, _, err := c.Lookup(context.Background(), fwtrading.OrderRef{
		Symbol: "ETHUSDT", ClientOrderID: "officer-client",
	}); err == nil {
		t.Fatal("lookup requires diagnostics")
	}
}

func TestBinanceLookupTrades(t *testing.T) {
	for _, paging := range []bool{false, true} {
		for _, stuck := range []bool{false, true} {
			if stuck && !paging {
				continue
			}
			t.Run(fmt.Sprintf("paging=%t stuck=%t", paging, stuck),
				func(t *testing.T) {
					count := 3
					if paging {
						count = 1001
					}
					var pages, symbolCalls atomic.Int32
					c := newBinanceTest(t,
						func(w http.ResponseWriter, r *http.Request) {
							switch r.URL.Path {
							case binance.ExchangeInfoPath:
								symbolCalls.Add(1)
								writeBinanceSymbol(t, w, binanceTestSymbol())
							case "/api/v3/order":
								writeBinanceJSON(t, w, binanceLookupOrder(
									"FILLED", strconv.Itoa(count), strconv.Itoa(count)))
							case "/api/v3/myTrades":
								pages.Add(1)
								q := r.URL.Query()
								if q.Get("orderId") != binanceTestOrderID ||
									q.Get("limit") != "1000" {
									t.Error("incorrect trade query")
								}
								first, last := 1, count
								if paging {
									last = 1000
									if q.Get("fromId") == "" {
										first, last = 2, count
									} else if q.Get("fromId") != "0" {
										if q.Get("fromId") != "1001" {
											t.Error("incorrect trade cursor")
										}
										if !stuck {
											first, last = 1001, 1001
										}
									}
								}
								var trades []binanceTrade
								for id := last; id >= first; id-- {
									asset := []string{"BNB", "ETH", "USDT"}[id%3]
									trades = append(trades,
										binanceTestTrade(id, "1", asset))
								}
								writeBinanceJSON(t, w, trades)
							default:
								t.Error("unexpected endpoint")
							}
						})
					snapshot, found, err := c.Lookup(context.Background(),
						fwtrading.OrderRef{Symbol: "ETHUSDT",
							ClientOrderID: "officer-client"})
					if stuck {
						if !errors.Is(err, domain.ErrUpstream) || found ||
							len(snapshot.Fills) != 0 || pages.Load() != 2 {
							t.Fatalf("stuck pagination: %+v %v", snapshot, err)
						}
						return
					}
					if err != nil || !found || len(snapshot.Fills) != count {
						t.Fatalf("fills=%d found=%t: %v",
							len(snapshot.Fills), found, err)
					}
					for i, fill := range snapshot.Fills {
						id := i + 1
						leg := []fwtrading.CommissionLeg{
							"", fwtrading.CommissionLegBase,
							fwtrading.CommissionLegQuote,
						}[id%3]
						if fill.CumQuantity != strconv.Itoa(id) ||
							fill.LeavesQuantity != strconv.Itoa(count-id) ||
							fill.Final != (id == count) || fill.Quantity != "1" ||
							fill.Price != "10.00" || fill.CommissionLeg != leg ||
							fill.Commission == nil ||
							fill.Commission.Amount != "0.01" ||
							!fill.At.Equal(time.UnixMilli(1700000000000+int64(id))) {
							t.Fatalf("trade %d mapped incorrectly: %+v", id, fill)
						}
					}
					if symbolCalls.Load() != 1 {
						t.Fatal("symbol assets were not cached")
					}
				})
		}
	}
}

func TestBinanceLookupUnusableTrades(t *testing.T) {
	for _, field := range []string{
		"orderId", "id", "qty", "price", "commission", "asset", "time",
		"overfill", "symbol assets",
	} {
		t.Run(field, func(t *testing.T) {
			trade := binanceTestTrade(1, "1", "ETH")
			switch field {
			case "orderId":
				trade.OrderID = "2"
			case "id":
				*trade.ID = -1
			case "qty":
				trade.Quantity = "1e3"
			case "price":
				trade.Price = "bad"
			case "commission":
				trade.Commission = "bad"
			case "asset":
				trade.CommissionAsset = ""
			case "time":
				trade.At = 0
			case "overfill":
				trade.Quantity = "3"
			}
			c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v3/order":
					writeBinanceJSON(t, w, binanceLookupOrder("FILLED", "2", "2"))
				case "/api/v3/myTrades":
					writeBinanceJSON(t, w, []binanceTrade{trade})
				case binance.ExchangeInfoPath:
					symbol := binanceTestSymbol()
					if field == "symbol assets" {
						symbol.BaseAsset = ""
					}
					writeBinanceSymbol(t, w, symbol)
				}
			})
			snapshot, found, err := c.Lookup(context.Background(),
				fwtrading.OrderRef{Symbol: "ETHUSDT", ClientOrderID: "client"})
			if !errors.Is(err, domain.ErrUpstream) || found ||
				!reflect.DeepEqual(snapshot, fwtrading.Snapshot{}) {
				t.Fatalf("unusable trade returned snapshot=%+v: %v", snapshot, err)
			}
		})
	}
}

func TestBinanceSymbolCacheRecovery(t *testing.T) {
	for _, path := range []string{"fetchSymbol", "SearchSymbols"} {
		for _, missing := range []string{"base", "quote"} {
			t.Run(path+"/"+missing, func(t *testing.T) {
				var fetches atomic.Int32
				c := newBinanceTest(t,
					func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case binance.ExchangeInfoPath:
							symbol := binanceTestSymbol()
							if fetches.Add(1) == 1 {
								if missing == "base" {
									symbol.BaseAsset = ""
								} else {
									symbol.QuoteAsset = ""
								}
							}
							writeBinanceSymbol(t, w, symbol)
						case "/api/v3/order":
							writeBinanceJSON(t, w,
								binanceLookupOrder("FILLED", "1", "1"))
						case "/api/v3/myTrades":
							writeBinanceJSON(t, w, []binanceTrade{
								binanceTestTrade(1, "1", "ETH"),
							})
						default:
							t.Error("unexpected endpoint")
						}
					})
				ctx := context.Background()
				ref := fwtrading.OrderRef{
					Symbol: "ETHUSDT", ClientOrderID: "officer-client",
				}
				if path == "SearchSymbols" {
					if _, err := c.SearchSymbols(ctx, "ETH"); err != nil {
						t.Fatal(err)
					}
				} else {
					_, found, err := c.Lookup(ctx, ref)
					if !errors.Is(err, domain.ErrUpstream) || found {
						t.Fatalf("incomplete metadata: found=%t: %v", found, err)
					}
				}
				snapshot, found, err := c.Lookup(ctx, ref)
				if err != nil || !found || len(snapshot.Fills) != 1 {
					t.Fatalf("metadata recovery: found=%t fills=%d: %v",
						found, len(snapshot.Fills), err)
				}
				fill := snapshot.Fills[0]
				if fill.Commission == nil ||
					fill.CommissionLeg != fwtrading.CommissionLegBase ||
					fetches.Load() != 2 {
					t.Fatalf("recovered fill=%+v fetches=%d",
						fill, fetches.Load())
				}
			})
		}
	}
}

func TestBinanceLookupTradeIDs(t *testing.T) {
	for _, tc := range []struct {
		id   string
		fail bool
	}{
		{"0", false},
		{"9223372036854775807", false},
		{"-1", true},
		{"1.5", true},
		{"9223372036854775808", true},
		{"null", true},
		{"missing", true},
	} {
		t.Run(tc.id, func(t *testing.T) {
			c := newBinanceTest(t,
				func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/v3/order":
						writeBinanceJSON(t, w,
							binanceLookupOrder("FILLED", "1", "1"))
					case "/api/v3/myTrades":
						trade := map[string]any{
							"id":      json.Number(tc.id),
							"orderId": json.Number(binanceTestOrderID),
							"qty":     "1", "price": "10", "commission": "0",
							"commissionAsset": "", "time": 1700000000000,
						}
						switch tc.id {
						case "missing":
							delete(trade, "id")
						case "null":
							trade["id"] = nil
						}
						writeBinanceJSON(t, w, []map[string]any{trade})
					default:
						t.Error("unexpected endpoint")
					}
				})
			snapshot, found, err := c.Lookup(context.Background(),
				fwtrading.OrderRef{Symbol: "ETHUSDT", ClientOrderID: "client"})
			if tc.fail {
				if !errors.Is(err, domain.ErrUpstream) || found ||
					!reflect.DeepEqual(snapshot, fwtrading.Snapshot{}) {
					t.Fatalf("invalid trade id returned snapshot=%+v: %v",
						snapshot, err)
				}
			} else if err != nil || !found || len(snapshot.Fills) != 1 {
				t.Fatalf("valid trade id: found=%t fills=%d: %v",
					found, len(snapshot.Fills), err)
			}
		})
	}
}

func TestBinanceTradeCursorLimit(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(fmt.Sprintf("exhausted=%t", exhausted), func(t *testing.T) {
			var pages atomic.Int32
			c := newBinanceTest(t,
				func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/v3/order":
						writeBinanceJSON(t, w,
							binanceLookupOrder("FILLED", "1001", "1001"))
					case "/api/v3/myTrades":
						page := pages.Add(1)
						q := r.URL.Query()
						var trades []map[string]any
						if page == 1 {
							for i := range 1000 {
								id := strconv.Itoa(i)
								if i == 999 {
									id = "9223372036854775806"
									if exhausted {
										id = "9223372036854775807"
									}
								}
								trades = append(trades, map[string]any{
									"id":      json.Number(id),
									"orderId": json.Number(binanceTestOrderID),
									"qty":     "1", "price": "10",
									"commission": "0", "commissionAsset": "",
									"time": 1700000000000 + i,
								})
							}
						} else if !exhausted {
							if q.Get("fromId") != "9223372036854775807" {
								t.Errorf("cursor lost digits: %s", q.Get("fromId"))
							}
							trades = []map[string]any{{
								"id":      json.Number("9223372036854775807"),
								"orderId": json.Number(binanceTestOrderID),
								"qty":     "1", "price": "10", "commission": "0",
								"commissionAsset": "", "time": 1700000001000,
							}}
						}
						writeBinanceJSON(t, w, trades)
					default:
						t.Error("unexpected endpoint")
					}
				})
			snapshot, found, err := c.Lookup(context.Background(),
				fwtrading.OrderRef{Symbol: "ETHUSDT", ClientOrderID: "client"})
			if exhausted {
				if !errors.Is(err, domain.ErrUpstream) || found ||
					len(snapshot.Fills) != 0 || pages.Load() != 1 {
					t.Fatalf("exhausted cursor: found=%t fills=%d pages=%d: %v",
						found, len(snapshot.Fills), pages.Load(), err)
				}
			} else if err != nil || !found || len(snapshot.Fills) != 1001 ||
				pages.Load() != 2 {
				t.Fatalf("large cursor: found=%t fills=%d pages=%d: %v",
					found, len(snapshot.Fills), pages.Load(), err)
			} else {
				for i, fill := range snapshot.Fills {
					if fill.CumQuantity != strconv.Itoa(i+1) ||
						!fill.At.Equal(time.UnixMilli(1700000000000+int64(i))) {
						t.Fatalf("trade order lost precision: %+v", fill)
					}
				}
			}
		})
	}
}

func TestBinanceAccountAndSymbols(t *testing.T) {
	for _, canTrade := range []bool{false, true} {
		var requests atomic.Int32
		c := newBinanceTest(t, func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if r.URL.Path == "/api/v3/account" {
				writeBinanceJSON(t, w, binance.Account{
					UID: "9007199254740993", AccountType: "SPOT", CanTrade: canTrade,
				})
				return
			}
			if r.URL.Query().Get("symbol") == "UNKNOWN" {
				w.WriteHeader(400)
				_, _ = fmt.Fprint(w, `{"code":-1121,"msg":"Invalid symbol"}`)
				return
			}
			symbol := binanceTestSymbol()
			if r.URL.Query().Has("symbol") {
				writeBinanceSymbol(t, w, symbol)
				return
			}
			other := symbol
			other.Symbol, other.BaseAsset = "ETHUSDC", "WETH"
			other.QuoteAsset, other.Status = "USDC", "BREAK"
			noSpot := symbol
			noSpot.Symbol, noSpot.IsSpotTradingAllowed = "ETHBTC", false
			writeBinanceJSON(t, w, binance.ExchangeInfo{
				Symbols: []binance.Symbol{other, noSpot, symbol},
			})
		})
		matches, err := c.SearchSymbols(context.Background(), " \t")
		if err != nil || matches == nil || len(matches) != 0 ||
			requests.Load() != 0 {
			t.Fatal("empty query must return non-nil empty without network")
		}
		account, err := c.CheckAccount(context.Background())
		status := "SPOT"
		if !canTrade {
			status += ", trading disabled"
		}
		if err != nil || account.Identity != "9007199254740993" ||
			account.Status != status {
			t.Fatalf("account=%+v: %v", account, err)
		}
		verification, err := c.VerifySymbol(context.Background(), "ETHUSDT")
		if err != nil || !verification.Exists || !verification.Tradable ||
			verification.Details != "ETH/USDT TRADING" {
			t.Fatalf("verification=%+v: %v", verification, err)
		}
		verification, err = c.VerifySymbol(context.Background(), "UNKNOWN")
		if err != nil || verification.Exists {
			t.Fatalf("absent verification=%+v: %v", verification, err)
		}
		matches, err = c.SearchSymbols(context.Background(), "eth")
		if err != nil || len(matches) != 2 ||
			matches[0].Symbol != "ETHUSDC" || matches[0].Tradable ||
			matches[0].BaseAsset != "WETH" || matches[0].QuoteAsset != "USDC" ||
			matches[0].Name != "WETH/USDC" || matches[0].Class != "crypto" ||
			matches[0].Exchange != "Binance" || !matches[1].Tradable {
			t.Fatalf("matches=%+v: %v", matches, err)
		}
		matches, err = c.SearchSymbols(context.Background(), "ETH/USDT")
		if err != nil || len(matches) == 0 || matches[0].Symbol != "ETHUSDT" {
			t.Fatalf("pair ranking=%+v: %v", matches, err)
		}
	}
}
