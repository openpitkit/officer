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
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/alpaca"
)

const tradingCredentials = `{"apiKey":"test-key-marker","apiSecret":"test-secret-marker"}`

func testAlpacaConnector(t *testing.T, server *httptest.Server) *alpacaConnector {
	t.Helper()
	c, err := newAlpacaConnector(domain.TradingConnection{ExternalID: "test-connection", Credentials: tradingCredentials}, server.URL, "ws"+strings.TrimPrefix(server.URL, "http")+"/stream")
	if err != nil {
		t.Fatal("connector construction failed")
	}
	t.Cleanup(c.Close)
	return c
}

func testOrder() fwtrading.Order {
	return fwtrading.Order{ClientOrderID: "officer-client", Symbol: "AAPL", Side: domain.OrderSide("buy"), AmountKind: domain.OrderAmountKindQuantity, Quantity: "10", LimitPrice: "1.10", Route: `{"timeInForce":"day"}`}
}

func TestProvidersAndEndpoints(t *testing.T) {
	providers := FirstPartyProviders()
	if len(providers) != 1 {
		t.Fatal("expected one provider")
	}
	p := providers[0]
	if p.Type != domain.TradingProviderAlpaca || p.Title != "Alpaca" || p.VenueAccounts || !p.VerifiesSymbols || p.Build == nil {
		t.Fatal("provider contract differs")
	}
	for _, tc := range []struct {
		mode         domain.TradingMode
		rest, stream string
	}{
		{domain.TradingModeTest, alpaca.PaperRESTURL, alpacaPaperStreamURL},
		{domain.TradingModeReal, alpaca.LiveRESTURL, alpacaLiveStreamURL},
	} {
		connector, err := p.Build(domain.TradingConnection{Mode: tc.mode, Credentials: tradingCredentials})
		if err != nil {
			t.Fatal("supported mode refused")
		}
		c := connector.(*alpacaConnector)
		defer c.Close()
		if c.streamURL != tc.stream {
			t.Fatal("wrong stream endpoint")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err = c.client.Asset(ctx, "AAPL")
		if err == nil || !strings.Contains(err.Error(), tc.rest+"/v2/assets/AAPL") {
			t.Fatal("wrong REST endpoint")
		}
	}
	if c, err := NewAlpacaConnector(domain.TradingConnection{Mode: domain.TradingModeTest, Credentials: "{}"}); err == nil || c != nil {
		t.Fatal("bad credentials must return an error and a nil connector")
	}
}

func TestInvariantNoDefaults(t *testing.T) {
	for _, mode := range []domain.TradingMode{"", "unknown"} {
		if c, err := NewAlpacaConnector(domain.TradingConnection{Mode: mode, Credentials: tradingCredentials}); err == nil || c != nil {
			t.Fatal("missing or unknown mode accepted")
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid route reached venue") }))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	for _, raw := range []string{"", "null", "[]", "{}", `{"timeInForce":""}`, `{"timeInForce":"DAY"}`,
		`{"timeInForce":"unknown"}`, `{"timeInForce":1}`, `{"timeInForce":"day","extra":true}`, `{"timeInForce":"day"} {}`,
		`{"TimeInForce":"day"}`, `{"timeInForce":null}`,
	} {
		order := testOrder()
		order.Route = raw
		if err := c.PreCheck(context.Background(), order); !errors.Is(err, fwtrading.ErrRejected) {
			t.Fatal("invalid route accepted by PreCheck")
		}
		if _, err := c.Send(context.Background(), order); !errors.Is(err, fwtrading.ErrRejected) {
			t.Fatal("invalid route accepted by Send")
		}
	}
}

func TestPreCheck(t *testing.T) {
	type rule struct {
		name    string
		change  func(*alpaca.Asset, *fwtrading.Order)
		want    error
		message string
	}
	rules := []rule{
		{"equity", func(a *alpaca.Asset, o *fwtrading.Order) {}, nil, ""},
		{"volume", func(a *alpaca.Asset, o *fwtrading.Order) { o.AmountKind = domain.OrderAmountKindVolume }, fwtrading.ErrUnsupported, "quantity"},
		{"inactive", func(a *alpaca.Asset, o *fwtrading.Order) { a.Status = "inactive" }, fwtrading.ErrRejected, "active"},
		{"not tradable", func(a *alpaca.Asset, o *fwtrading.Order) { a.Tradable = false }, fwtrading.ErrRejected, "tradable"},
		{"unsupported class", func(a *alpaca.Asset, o *fwtrading.Order) { a.Class = "option" }, fwtrading.ErrUnsupported, "class"},
		{"fractional allowed", func(a *alpaca.Asset, o *fwtrading.Order) { o.Quantity = "0.5" }, nil, ""},
		{"not fractionable", func(a *alpaca.Asset, o *fwtrading.Order) { o.Quantity = "0.5"; a.Fractionable = false }, fwtrading.ErrRejected, "fractionable"},
		{"fractional TIF", func(a *alpaca.Asset, o *fwtrading.Order) { o.Quantity = "0.5"; o.Route = `{"timeInForce":"gtc"}` }, fwtrading.ErrRejected, "day"},
		{"integer with trailing zeros", func(a *alpaca.Asset, o *fwtrading.Order) {
			o.Quantity = "10.000"
			a.Fractionable = false
			o.Route = `{"timeInForce":"gtc"}`
		}, nil, ""},
		{"above dollar precision", func(a *alpaca.Asset, o *fwtrading.Order) { o.LimitPrice = "1.001" }, fwtrading.ErrRejected, "2 decimal"},
		{"at dollar precision", func(a *alpaca.Asset, o *fwtrading.Order) { o.LimitPrice = "1.00" }, nil, ""},
		{"below dollar precision pass", func(a *alpaca.Asset, o *fwtrading.Order) { o.LimitPrice = "0.9999" }, nil, ""},
		{"below dollar precision fail", func(a *alpaca.Asset, o *fwtrading.Order) { o.LimitPrice = "0.99999" }, fwtrading.ErrRejected, "4 decimal"},
		{"price trailing zeros", func(a *alpaca.Asset, o *fwtrading.Order) { o.LimitPrice = "1.1000" }, nil, ""},
		{"market", func(a *alpaca.Asset, o *fwtrading.Order) { o.LimitPrice = "" }, nil, ""},
		{"invalid quantity", func(a *alpaca.Asset, o *fwtrading.Order) { o.Quantity = "bad" }, fwtrading.ErrRejected, "quantity"},
		{"zero quantity", func(a *alpaca.Asset, o *fwtrading.Order) { o.Quantity = "0" }, fwtrading.ErrRejected, "quantity"},
		{"negative quantity", func(a *alpaca.Asset, o *fwtrading.Order) { o.Quantity = "-1" }, fwtrading.ErrRejected, "quantity"},
		{"invalid price", func(a *alpaca.Asset, o *fwtrading.Order) { o.LimitPrice = "bad" }, fwtrading.ErrRejected, "limit price"},
		{"negative price", func(a *alpaca.Asset, o *fwtrading.Order) { o.LimitPrice = "-1" }, fwtrading.ErrRejected, "limit price"},
		{"crypto no optional rules", func(a *alpaca.Asset, o *fwtrading.Order) { a.Class = "crypto"; o.Route = `{"timeInForce":"gtc"}` }, nil, ""},
		{"crypto invalid TIF", func(a *alpaca.Asset, o *fwtrading.Order) { a.Class = "crypto" }, fwtrading.ErrRejected, "gtc or ioc"},
		{"crypto minimum pass", func(a *alpaca.Asset, o *fwtrading.Order) {
			a.Class = "crypto"
			a.MinOrderSize = "10"
			o.Route = `{"timeInForce":"gtc"}`
		}, nil, ""},
		{"crypto minimum fail", func(a *alpaca.Asset, o *fwtrading.Order) {
			a.Class = "crypto"
			a.MinOrderSize = "10.000000000000000001"
			o.Route = `{"timeInForce":"gtc"}`
		}, fwtrading.ErrRejected, "min_order_size"},
		{"crypto qty increment pass", func(a *alpaca.Asset, o *fwtrading.Order) {
			a.Class = "crypto"
			a.MinTradeIncrement = "0.1"
			o.Quantity = "10.1"
			o.Route = `{"timeInForce":"ioc"}`
		}, nil, ""},
		{"crypto qty increment fail", func(a *alpaca.Asset, o *fwtrading.Order) {
			a.Class = "crypto"
			a.MinTradeIncrement = "0.1"
			o.Quantity = "10.100000000000000001"
			o.Route = `{"timeInForce":"ioc"}`
		}, fwtrading.ErrRejected, "min_trade_increment"},
		{"crypto price increment pass", func(a *alpaca.Asset, o *fwtrading.Order) {
			a.Class = "crypto"
			a.PriceIncrement = "0.05"
			o.Route = `{"timeInForce":"gtc"}`
		}, nil, ""},
		{"crypto price increment fail", func(a *alpaca.Asset, o *fwtrading.Order) {
			a.Class = "crypto"
			a.PriceIncrement = "0.03"
			o.Route = `{"timeInForce":"gtc"}`
		}, fwtrading.ErrRejected, "price_increment"},
		{"bad venue minimum", func(a *alpaca.Asset, o *fwtrading.Order) {
			a.Class = "crypto"
			a.MinOrderSize = "bad"
			o.Route = `{"timeInForce":"gtc"}`
		}, nil, "min_order_size"},
		{"zero venue increment", func(a *alpaca.Asset, o *fwtrading.Order) {
			a.Class = "crypto"
			a.MinTradeIncrement = "0"
			o.Route = `{"timeInForce":"gtc"}`
		}, nil, "min_trade_increment"},
	}
	for _, tif := range []string{"day", "gtc", "opg", "cls", "ioc", "fok"} {
		rules = append(rules, rule{"equity TIF " + tif, func(a *alpaca.Asset, o *fwtrading.Order) { o.Route = fmt.Sprintf(`{"timeInForce":"%s"}`, tif) }, nil, ""})
	}
	for _, tc := range rules {
		t.Run(tc.name, func(t *testing.T) {
			asset := alpaca.Asset{Class: "us_equity", Status: "active", Tradable: true, Fractionable: true}
			order := testOrder()
			tc.change(&asset, &order)
			if asset.Class == "crypto" {
				asset.Symbol, order.Symbol = "BTC/USD", "BTC/USD"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(asset) }))
			defer server.Close()
			err := testAlpacaConnector(t, server).PreCheck(context.Background(), order)
			if tc.want == nil && tc.message != "" {
				if !errors.Is(err, domain.ErrUpstream) || errors.Is(err, fwtrading.ErrRejected) || errors.Is(err, fwtrading.ErrUnsupported) || !strings.Contains(err.Error(), tc.message) {
					t.Fatal("invalid venue metadata must be an upstream data error naming its field")
				}
			} else if tc.want == nil {
				if err != nil {
					t.Fatal("valid order refused")
				}
			} else if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.message) {
				t.Fatal("rule did not reject with its reason")
			}
		})
	}
	for _, status := range []int{404, 500} {
		t.Run(fmt.Sprintf("asset HTTP %d", status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			err := testAlpacaConnector(t, server).PreCheck(context.Background(), testOrder())
			if status == 404 && !errors.Is(err, fwtrading.ErrRejected) {
				t.Fatal("unknown asset accepted")
			}
			if status == 500 && (err == nil || errors.Is(err, fwtrading.ErrRejected)) {
				t.Fatal("asset outage must not be definitive refusal")
			}
		})
	}
}

func TestPreCheckVenueDecimals(t *testing.T) {
	for _, field := range []string{"min_order_size", "min_trade_increment", "price_increment"} {
		for _, tc := range []struct{ name, value string }{
			{"exponent", "1e3"},
			{"large exponent", "1e1000000000"},
			{"small exponent", "1e-2147483647"},
			{"overlong", strings.Repeat("1", alpacaDecimalMaxLength+1)},
			{"space", " 1"}, {"negative", "-1"}, {"zero", "0"}, {"malformed", "bad"},
		} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				asset := alpaca.Asset{Symbol: "BTC/USD", Class: "crypto", Status: "active", Tradable: true}
				switch field {
				case "min_order_size":
					asset.MinOrderSize = tc.value
				case "min_trade_increment":
					asset.MinTradeIncrement = tc.value
				case "price_increment":
					asset.PriceIncrement = tc.value
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(asset)
				}))
				defer server.Close()
				order := testOrder()
				order.Symbol = "BTC/USD"
				order.Route = `{"timeInForce":"gtc"}`
				err := testAlpacaConnector(t, server).PreCheck(context.Background(), order)
				if !errors.Is(err, domain.ErrUpstream) || errors.Is(err, fwtrading.ErrRejected) || errors.Is(err, fwtrading.ErrUnsupported) || !strings.Contains(err.Error(), field) {
					t.Fatal("invalid venue decimal must be an upstream data error naming its field")
				}
			})
		}
	}
	t.Run("maximum length accepted", func(t *testing.T) {
		asset := alpaca.Asset{Symbol: "BTC/USD", Class: "crypto", Status: "active", Tradable: true,
			MinOrderSize:      "0." + strings.Repeat("0", alpacaDecimalMaxLength-3) + "1",
			MinTradeIncrement: "0.1", PriceIncrement: "0.01"}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(asset)
		}))
		defer server.Close()
		order := testOrder()
		order.Symbol = "BTC/USD"
		order.Route = `{"timeInForce":"gtc"}`
		if err := testAlpacaConnector(t, server).PreCheck(context.Background(), order); err != nil {
			t.Fatal("valid bounded venue decimals refused")
		}
	})
}

func TestPreCheckRequiresCanonicalCryptoSymbol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(alpaca.Asset{Symbol: "BTC/USD", Class: "crypto", Status: "active", Tradable: true})
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	order := testOrder()
	order.Route = `{"timeInForce":"gtc"}`
	order.Symbol = "BTCUSD"
	if err := c.PreCheck(context.Background(), order); !errors.Is(err, fwtrading.ErrUnsupported) || !strings.Contains(err.Error(), "BTC/USD") {
		t.Fatalf("legacy crypto symbol must be refused with canonical symbol to configure: %v", err)
	}
	order.Symbol = "BTC/USD"
	if err := c.PreCheck(context.Background(), order); err != nil {
		t.Fatalf("canonical crypto symbol refused: %v", err)
	}
}

func TestSend(t *testing.T) {
	for _, tc := range []struct {
		name              string
		status            int
		body              string
		rejected, success bool
	}{
		{"success", 200, `{"id":"venue-id"}`, false, true},
		{"forbidden", 403, `{"code":40310000,"message":"insufficient buying power"}`, true, false},
		{"validation same code", 422, `{"code":40010001,"message":"qty must be positive"}`, true, false},
		{"duplicate", 422, `{"code":40010001,"message":"client_order_id must be unique"}`, false, false},
		{"duplicate alternate wording", 422, `{"code":40010001,"message":"client_order_id already exists"}`, false, false},
		{"client id other code", 422, `{"code":42210000,"message":"invalid CLIENT_ORDER_ID"}`, false, false},
		{"unrecognised body", 422, `{"error":"validation failed"}`, false, false},
		{"malformed body", 422, `{`, false, false},
		{"missing code", 422, `{"message":"qty must be positive"}`, false, false},
		{"missing message", 422, `{"code":40010001}`, false, false},
		{"wrong code type", 422, `{"code":"40010001","message":"qty must be positive"}`, false, false},
		{"null message", 422, `{"code":40010001,"message":null}`, false, false},
		{"decoded empty fields", 422, `{"code":0,"message":""}`, true, false},
		{"request timeout", 408, `{"code":408,"message":"timeout"}`, false, false},
		{"rate limit", 429, `{"code":429,"message":"rate limit"}`, true, false},
		{"server", 503, `{"code":503,"message":"unavailable"}`, false, false},
		{"missing id", 200, `{}`, false, false},
		{"malformed success", 200, `{"id":`, false, false},
		{"unexpected success", 201, `{"id":"venue-id"}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v2/orders" {
					t.Error("Send did an asset lookup or used wrong endpoint")
				}
				if r.Header.Get("APCA-API-KEY-ID") != "test-key-marker" || r.Header.Get("APCA-API-SECRET-KEY") != "test-secret-marker" {
					t.Error("missing auth")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["type"] != "limit" || body["time_in_force"] != "day" || body["qty"] != "10" || body["limit_price"] != "1.10" || body["client_order_id"] != "officer-client" || body["symbol"] != "AAPL" || body["side"] != "buy" {
					t.Error("wrong order JSON")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			ack, err := testAlpacaConnector(t, server).Send(context.Background(), testOrder())
			if tc.success {
				if err != nil || ack.VenueOrderID != "venue-id" {
					t.Fatal("ack missing")
				}
			} else if err == nil || errors.Is(err, fwtrading.ErrRejected) != tc.rejected {
				t.Fatal("wrong send outcome classification")
			}
			var venueError struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal([]byte(tc.body), &venueError)
			if tc.rejected && !strings.Contains(err.Error(), venueError.Message) {
				t.Fatal("venue reason lost")
			}
		})
	}
}

func TestInvariantUnknownOutcomeIsNotRejected(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		slow   bool
	}{
		{422, `{"code":40010001,"message":"client_order_id must be unique"}`, false},
		{408, "{}", false}, {500, "{}", false}, {200, "{}", false}, {200, `{"id":"late"}`, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.slow {
				time.Sleep(50 * time.Millisecond)
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := testAlpacaConnector(t, server).Send(ctx, testOrder())
		cancel()
		server.Close()
		if err == nil || errors.Is(err, fwtrading.ErrRejected) {
			t.Fatal("uncertain outcome marked rejected")
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	c := testAlpacaConnector(t, server)
	server.Close()
	_, err := c.Send(context.Background(), testOrder())
	if err == nil || errors.Is(err, fwtrading.ErrRejected) {
		t.Fatal("network failure marked rejected")
	}
}

func TestInvariantExactDecimals(t *testing.T) {
	order := testOrder()
	order.Quantity = "9007199254740993.000000000000000001"
	order.LimitPrice = "0.1000"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("cannot decode order")
			return
		}
		var qty, price string
		if json.Unmarshal(body["qty"], &qty) != nil || json.Unmarshal(body["limit_price"], &price) != nil || qty != order.Quantity || price != order.LimitPrice {
			t.Error("decimal strings changed on send")
		}
		_, _ = w.Write([]byte(`{"id":"venue"}`))
	}))
	defer server.Close()
	if _, err := testAlpacaConnector(t, server).Send(context.Background(), order); err != nil {
		t.Fatal("exact order refused")
	}
}

func TestSendMarket(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["type"] != "market" || body["limit_price"] != nil {
			t.Error("wrong market JSON")
		}
		_, _ = w.Write([]byte(`{"id":"market"}`))
	}))
	defer server.Close()
	order := testOrder()
	order.LimitPrice = ""
	if _, err := testAlpacaConnector(t, server).Send(context.Background(), order); err != nil {
		t.Fatal("market order failed")
	}
}

func TestLookup(t *testing.T) {
	for _, byVenue := range []bool{true, false} {
		t.Run(fmt.Sprint(byVenue), func(t *testing.T) {
			pages := 0
			at := time.Date(2026, 9, 1, 10, 0, 0, 123, time.UTC)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/v2/orders") {
					if byVenue {
						if r.URL.Path != "/v2/orders/venue" {
							t.Error("wrong venue lookup")
						}
					} else if r.URL.Path != "/v2/orders:by_client_order_id" || r.URL.Query().Get("client_order_id") != "client + id" {
						t.Error("wrong client lookup")
					}
					_, _ = w.Write([]byte(`{"id":"venue","status":"filled","filled_qty":"100.000000000000000001"}`))
					return
				}
				if r.URL.Path != "/v2/account/activities/FILL" || r.URL.Query().Get("order_id") != "venue" || r.URL.Query().Get("direction") != "asc" || r.URL.Query().Get("page_size") != "100" {
					t.Error("wrong FILL query")
				}
				page := make([]alpacaActivity, 100)
				if pages > 0 {
					if r.URL.Query().Get("page_token") != "99" {
						t.Error("wrong page token")
					}
					page = page[:1]
				} else if r.URL.Query().Get("page_token") != "" {
					t.Error("first page has token")
				}
				for i := range page {
					page[i] = alpacaActivity{ID: fmt.Sprint(i), Type: "partial_fill", Quantity: "1.000", Price: "0.123456789123456789", CumQuantity: fmt.Sprint(i + 1), LeavesQuantity: fmt.Sprint(100 - i), At: at}
				}
				if pages > 0 {
					page[0].Type = "fill"
					page[0].CumQuantity = "100.000000000000000001"
					page[0].LeavesQuantity = "0"
					page[0].Quantity = "0.000000000000000001"
				}
				pages++
				_ = json.NewEncoder(w).Encode(page)
			}))
			defer server.Close()
			ref := fwtrading.OrderRef{ClientOrderID: "client + id"}
			if byVenue {
				ref.VenueOrderID = "venue"
			}
			snapshot, found, err := testAlpacaConnector(t, server).Lookup(context.Background(), ref)
			if err != nil || !found || pages != 2 || len(snapshot.Fills) != 101 || snapshot.Status != fwtrading.VenueStatusFilled || snapshot.FilledQuantity != "100.000000000000000001" {
				t.Fatal("lookup pagination or snapshot failed")
			}
			if snapshot.Fills[0].CumQuantity != "1" || snapshot.Fills[99].CumQuantity != "100" || snapshot.Fills[0].Quantity != "1.000" || snapshot.Fills[0].Price != "0.123456789123456789" || !snapshot.Fills[0].At.Equal(at) || snapshot.Fills[0].Final || !snapshot.Fills[100].Final || snapshot.Fills[100].LeavesQuantity != "0" {
				t.Fatal("venue fill fields or order changed")
			}
		})
	}
	for _, status := range []string{"new", "partially_filled", "filled", "done_for_day", "canceled", "expired", "replaced", "pending_cancel", "pending_replace", "accepted", "pending_new", "accepted_for_bidding", "stopped", "rejected", "suspended", "calculated", "held", "future_status"} {
		t.Run(status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/v2/orders") {
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "venue", "status": status, "filled_qty": "0"})
				} else {
					_, _ = w.Write([]byte("[]"))
				}
			}))
			defer server.Close()
			snapshot, found, err := testAlpacaConnector(t, server).Lookup(context.Background(), fwtrading.OrderRef{VenueOrderID: "venue"})
			want := fwtrading.VenueStatusOpen
			switch status {
			case "filled":
				want = fwtrading.VenueStatusFilled
			case "canceled", "expired", "rejected":
				want = fwtrading.VenueStatusCancelled
			case "replaced":
				want = fwtrading.VenueStatusReplaced
			}
			if err != nil || !found || snapshot.Status != want {
				t.Fatal("wrong status mapping")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer server.Close()
	if _, found, err := testAlpacaConnector(t, server).Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "unknown"}); err != nil || found {
		t.Fatal("404 must be not found")
	}
}

func TestVerifySymbol(t *testing.T) {
	for _, tc := range []struct {
		name, status           string
		tradable, fractionable bool
		code                   int
	}{
		{"fractionable", "active", true, true, 200}, {"inactive", "inactive", true, false, 200}, {"not tradable", "active", false, false, 200}, {"unknown", "", false, false, 404}, {"outage", "", false, false, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				_ = json.NewEncoder(w).Encode(alpaca.Asset{Class: "us_equity", Exchange: "NASDAQ", Name: "Apple", Status: tc.status, Tradable: tc.tradable, Fractionable: tc.fractionable})
			}))
			defer server.Close()
			result, err := testAlpacaConnector(t, server).VerifySymbol(context.Background(), "AAPL")
			if tc.code == 500 {
				if err == nil {
					t.Fatal("outage ignored")
				}
				return
			}
			if err != nil || result.Exists != (tc.code == 200) || result.Tradable != (tc.status == "active" && tc.tradable) {
				t.Fatal("wrong verification")
			}
			if tc.code == 200 {
				want := "us_equity NASDAQ Apple"
				if tc.fractionable {
					want += ", fractionable"
				}
				if result.Details != want {
					t.Fatal("wrong details")
				}
			}
		})
	}
}
