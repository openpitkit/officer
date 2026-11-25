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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/okx"
)

const okxTestCredentials = `{"apiKey":"okx-key-marker","apiSecret":"okx-secret-marker",` +
	`"passphrase":"okx-pass-marker","site":"www.okx.com"}`
const okxTestCOID = "AAECAwQFBgcICQoLDA0ODw"
const okxTestHex = "000102030405060708090a0b0c0d0e0f"

func newOKXTest(t testing.TB, handler http.HandlerFunc) *okxConnector {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := newOKXConnector(domain.TradingConnection{
		Mode: domain.TradingModePaper, Credentials: okxTestCredentials},
		server.URL, "ws"+strings.TrimPrefix(server.URL, "http")+"/stream")
	if err != nil {
		t.Fatal("construct OKX connector")
	}
	c.SetDiagnosticReporter(func(health.Diagnostic) {})
	c.SetStatusReporter(func(bool, string) {})
	t.Cleanup(c.Close)
	return c
}

func okxTestOrder() fwtrading.Order {
	return fwtrading.Order{ClientOrderID: okxTestCOID, Symbol: "BTC-USDT", Side: "buy",
		AmountKind: domain.OrderAmountKindQuantity, Quantity: "10", Route: "{}"}
}

func okxReply(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "msg": "", "data": data})
}

func okxTestInstrument() okx.Instrument {
	return okx.Instrument{
		InstID: "BTC-USDT", BaseCcy: "BTC", QuoteCcy: "USDT", State: "live",
		MinSz: "0.1", LotSz: "0.1", TickSz: "0.01",
	}
}

func okxTestLookup(state, filled string) okxOrder {
	return okxOrder{
		OrderID: "123", ClientID: okxTestHex, Symbol: "BTC-USDT",
		State: state, Quantity: "10", Filled: filled,
	}
}

func okxTestTrade(i int) okxTrade {
	return okxTrade{OrderID: "123", TradeID: strconv.Itoa(i), BillID: strconv.Itoa(i + 1000),
		Quantity: "0.01", Price: strconv.Itoa(i),
		At: strconv.FormatInt(1700000000000+int64(i), 10), Fee: "0", FeeCcy: ""}
}

func TestOKXProviderConstruction(t *testing.T) {
	providers := FirstPartyProviders()
	i := slices.IndexFunc(providers, func(p fwtrading.Provider) bool {
		return p.Type == domain.TradingProviderOKX
	})
	if i < 0 {
		t.Fatal("OKX provider is not registered")
	}
	p := providers[i]
	if p.Type != domain.TradingProviderOKX || p.Title != "OKX" || p.VenueAccounts || p.UnlinkedFees ||
		!p.VerifiesSymbols || !p.SearchesSymbols || p.Build == nil {
		t.Fatal("OKX provider inventory contract")
	}
	for _, site := range []string{okx.GlobalSite, okx.EEASite, okx.USSite} {
		for _, mode := range []domain.TradingMode{domain.TradingModePaper, domain.TradingModeLive} {
			credentials := strings.ReplaceAll(okxTestCredentials, okx.GlobalSite, site)
			connector, err := NewOKXConnector(domain.TradingConnection{
				Mode: mode, Credentials: credentials,
			})
			if err != nil {
				t.Fatal("valid construction failed")
			}
			c := connector.(*okxConnector)
			rest, stream, _ := okx.Endpoints(site, mode == domain.TradingModePaper)
			if c.restURL != rest || c.streamURL != stream ||
				c.httpClient.Timeout != okxHTTPTimeout || c.httpClient.CheckRedirect == nil {
				t.Fatal("mode, site or HTTP policy")
			}
			if c.pingInterval != 25*time.Second || c.pongTimeout != 5*time.Second {
				t.Fatal("stream heartbeat defaults")
			}
			c.Close()
		}
	}
	if _, err := NewOKXConnector(domain.TradingConnection{
		Mode: "invalid", Credentials: okxTestCredentials,
	}); err == nil {
		t.Fatal("bad mode accepted")
	}
	_, err := NewOKXConnector(domain.TradingConnection{
		Mode: domain.TradingModeLive, Credentials: "{}",
	})
	if err == nil || !strings.HasPrefix(domain.ValidationPointer(err), "/credentials/") {
		t.Fatal("credential validation pointer")
	}
}

func TestOKXModeHeaders(t *testing.T) {
	for _, mode := range []domain.TradingMode{
		domain.TradingModePaper, domain.TradingModeLive,
	} {
		t.Run(string(mode), func(t *testing.T) {
			var signed, public atomic.Int32
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					want := ""
					if mode == domain.TradingModePaper {
						want = "1"
					}
					if r.Header.Get("x-simulated-trading") != want {
						t.Error("wrong mode header")
					}
					switch r.URL.Path {
					case "/api/v5/account/config":
						signed.Add(1)
						okxReply(w, []map[string]string{{"uid": "123"}})
					case "/api/v5/public/instruments":
						public.Add(1)
						okxReply(w, []okx.Instrument{okxTestInstrument()})
					default:
						t.Error("unexpected request")
					}
				}))
			defer server.Close()
			c, err := newOKXConnector(domain.TradingConnection{
				Mode: mode, Credentials: okxTestCredentials,
			}, server.URL, "")
			if err != nil {
				t.Fatal("construct connector")
			}
			defer c.Close()
			if _, err := c.CheckAccount(context.Background()); err != nil {
				t.Fatal("signed request failed")
			}
			if _, err := c.VerifySymbol(context.Background(), "BTC-USDT"); err != nil {
				t.Fatal("public request failed")
			}
			if signed.Load() != 1 || public.Load() != 1 {
				t.Fatal("both request surfaces must be checked")
			}
		})
	}
}

func TestOKXSendPayloads(t *testing.T) {
	for _, tc := range []struct{ tif, typ string }{
		{"", "market"}, {"GTC", "limit"}, {"IOC", "ioc"}, {"FOK", "fok"},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v5/trade/order" ||
					r.Header.Get("x-simulated-trading") != "1" {
					t.Error("order HTTP contract")
				}
				var body map[string]string
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("order JSON")
				}
				want := map[string]string{
					"instId": "BTC-USDT", "tdMode": "cash", "side": "buy",
					"clOrdId": okxTestHex, "sz": "10", "ordType": tc.typ,
				}
				if tc.tif == "" {
					want["tgtCcy"] = "base_ccy"
				} else {
					want["px"] = "2.01"
				}
				if !reflect.DeepEqual(body, want) {
					t.Error("order payload differs")
				}
				okxReply(w, []map[string]string{
					{"ordId": "123", "clOrdId": okxTestHex, "sCode": "0"},
				})
			})
			order := okxTestOrder()
			if tc.tif != "" {
				order.LimitPrice = "2.01"
				order.Route = fmt.Sprintf(`{"timeInForce":%q}`, tc.tif)
			}
			ack, err := c.Send(context.Background(), order)
			if err != nil || ack.VenueOrderID != "123" {
				t.Fatal("send acknowledgement")
			}
		})
	}
}

func TestOKXSendInputRefusals(t *testing.T) {
	var calls atomic.Int32
	c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, tc := range []struct {
		name string
		edit func(*fwtrading.Order)
		want error
	}{
		{"route", func(o *fwtrading.Order) { o.Route = "null" }, fwtrading.ErrRejected},
		{"unknown route", func(o *fwtrading.Order) {
			o.Route = `{"unknown":"x"}`
		}, fwtrading.ErrRejected},
		{"market TIF", func(o *fwtrading.Order) {
			o.Route = `{"timeInForce":"GTC"}`
		}, fwtrading.ErrRejected},
		{"limit TIF", func(o *fwtrading.Order) {
			o.LimitPrice = "2"
			o.Route = `{"timeInForce":"post_only"}`
		}, fwtrading.ErrRejected},
		{"side", func(o *fwtrading.Order) { o.Side = "BUY" }, fwtrading.ErrRejected},
		{"amount", func(o *fwtrading.Order) {
			o.AmountKind = domain.OrderAmountKindVolume
		}, fwtrading.ErrUnsupported},
		{"COID", func(o *fwtrading.Order) { o.ClientOrderID = "invalid" }, fwtrading.ErrRejected},
		{"short COID", func(o *fwtrading.Order) {
			o.ClientOrderID = base64.RawURLEncoding.EncodeToString([]byte{1})
		}, fwtrading.ErrRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := okxTestOrder()
			tc.edit(&o)
			_, err := c.Send(context.Background(), o)
			if !errors.Is(err, tc.want) {
				t.Fatal("wrong local refusal")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("local refusal reached venue")
	}
}

func TestOKXSendOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		rejected bool
	}{
		{"item refusal", 200, `{"code":"0","data":[{"sCode":"51008"}]}`, true},
		{"envelope refusal", 200, `{"code":"1","data":[{"sCode":"51008","sMsg":"balance"}]}`, true},
		{"4xx refusal", 400, `{"code":"51000","msg":"parameter"}`, true},
		{"top only 200", 200, `{"code":"51008"}`, false},
		{"undecoded", 400, `bad`, false},
		{"unreadable code", 400, `{"code":"bad"}`, false},
		{"unreadable item code", 200, `{"code":"1","data":[{"sCode":"bad"}]}`, false},
		{"408", 408, `{"code":"51000"}`, false},
		{"5xx", 500, `{"code":"51000"}`, false},
		{"bad ack", 200,
			`{"code":"0","data":[{"sCode":"0","ordId":"not-decimal","clOrdId":"wrong"}]}`, false},
		{"wrong echo", 200,
			`{"code":"0","data":[{"sCode":"0","ordId":"123","clOrdId":"wrong"}]}`, false},
		{"missing scode", 200,
			`{"code":"0","data":[{"ordId":"123",` +
				`"clOrdId":"000102030405060708090a0b0c0d0e0f"}]}`, false},
	}
	for _, code := range []string{
		"50001", "50004", "50011", "50013", "50026", "50040",
		"50042", "50071", "51011", "51016", "51149",
	} {
		tests = append(tests, struct {
			name     string
			status   int
			body     string
			rejected bool
		}{"item " + code, 200, fmt.Sprintf(`{"code":"1","data":[{"sCode":%q}]}`, code), false})
		tests = append(tests, struct {
			name     string
			status   int
			body     string
			rejected bool
		}{"4xx " + code, 400, fmt.Sprintf(`{"code":%q}`, code), false})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			})
			_, err := c.Send(context.Background(), okxTestOrder())
			if err == nil || errors.Is(err, fwtrading.ErrRejected) != tc.rejected ||
				(!tc.rejected && !strings.Contains(err.Error(), "send outcome unknown")) {
				t.Fatal("wrong outcome classification")
			}
		})
	}
}

func TestOKXLookupStates(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  fwtrading.VenueStatus
	}{
		{"live", fwtrading.VenueStatusOpen}, {"partially_filled", fwtrading.VenueStatusOpen},
		{"filled", fwtrading.VenueStatusFilled}, {"canceled", fwtrading.VenueStatusCancelled},
		{"mmp_canceled", fwtrading.VenueStatusCancelled}, {"new-state", ""},
	} {
		t.Run(tc.state, func(t *testing.T) {
			c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v5/trade/order" {
					if r.URL.Query().Get("ordId") != "123" ||
						r.URL.Query().Get("clOrdId") != okxTestHex ||
						r.URL.Query().Get("instId") != "BTC-USDT" {
						t.Error("lookup must send both identifiers and symbol")
					}
					okxReply(w, []okxOrder{okxTestLookup(tc.state, "0")})
					return
				}
				okxReply(w, []okxTrade{})
			})
			var diagnostics []health.Diagnostic
			c.SetDiagnosticReporter(func(d health.Diagnostic) {
				diagnostics = append(diagnostics, d)
			})
			snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{
				Symbol: "BTC-USDT", VenueOrderID: "123", ClientOrderID: okxTestCOID,
			})
			if tc.want == "" {
				if !errors.Is(err, domain.ErrUpstream) || found || len(diagnostics) != 1 ||
					diagnostics[0].Code != health.CodeUnparsableData {
					t.Fatal("unknown state contract")
				}
				if !reflect.DeepEqual(diagnostics[0],
					venueLookupUnparsable(okxLookupStatusUnknown)) {
					t.Fatal("lookup diagnostic must use the shared remediation")
				}
				return
			}
			if err != nil || !found || snapshot.Status != tc.want || len(diagnostics) != 0 {
				t.Fatal("lookup state mapping")
			}
		})
	}
	c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"code":"51603","msg":"not found"}`)
	})
	if _, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{
		Symbol: "BTC-USDT", ClientOrderID: okxTestCOID,
	}); found || err != nil {
		t.Fatal("not found contract")
	}
	for _, ref := range []fwtrading.OrderRef{{ClientOrderID: okxTestCOID}, {Symbol: "BTC-USDT"}} {
		if _, _, err := c.Lookup(context.Background(), ref); err == nil {
			t.Fatal("missing reference accepted")
		}
	}
}

func TestOKXLookupFillPages(t *testing.T) {
	c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/trade/order" {
			order := okxTestLookup("filled", "2")
			order.Quantity = "2"
			okxReply(w, []okxOrder{order})
			return
		}
		q := r.URL.Query()
		if q.Get("instType") != "SPOT" || q.Get("ordId") != "123" ||
			q.Get("instId") != "BTC-USDT" || q.Get("limit") != "100" {
			t.Error("fill history request filters")
		}
		var page []okxTrade
		switch r.URL.Path {
		case "/api/v5/trade/fills":
			switch q.Get("after") {
			case "":
				for i := 200; i > 100; i-- {
					page = append(page, okxTestTrade(i))
				}
			case "1101":
				page = []okxTrade{okxTestTrade(100)}
			default:
				t.Error("recent cursor")
			}
		case "/api/v5/trade/fills-history":
			switch q.Get("after") {
			case "":
				for i := 150; i > 50; i-- {
					page = append(page, okxTestTrade(i))
				}
			case "1051":
				for i := 50; i > 0; i-- {
					page = append(page, okxTestTrade(i))
				}
			default:
				t.Error("historical cursor")
			}
		default:
			t.Error("unexpected path")
		}
		okxReply(w, page)
	})
	s, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{
		Symbol: "BTC-USDT", ClientOrderID: okxTestCOID,
	})
	if err != nil || !found || len(s.Fills) != 200 {
		t.Fatal("complete deduplicated history")
	}
	for i, fill := range s.Fills {
		want := fmt.Sprintf("%d.%02d", (i+1)/100, (i+1)%100)
		cum, _ := parsePlainDecimal(fill.CumQuantity, false)
		expected, _ := parsePlainDecimal(want, false)
		leaves, _ := parsePlainDecimal(fill.LeavesQuantity, false)
		if !cum.Equal(expected) || !leaves.Add(cum).Equal(decimal.NewFromInt(2)) ||
			fill.Price != strconv.Itoa(i+1) || fill.Final != (i == 199) {
			t.Fatal("ordered cumulative fill chain")
		}
	}
}

func TestOKXLookupRejectsPartialHistory(t *testing.T) {
	for _, tc := range []struct{ fault, fragment string }{
		{"sum", "incomplete fill history"},
		{"foreign", "inconsistent fill identity"},
		{"cursor", "repeated fill cursor"},
		{"decimal", "unreadable fill"},
		{"time", "unreadable fill time"},
		{"conflicting", "conflicting duplicate fill"},
		{"clOrdId not ours", "inconsistent lookup identity"},
		{"clOrdId mismatch", "inconsistent lookup identity"},
		{"ordId mismatch", "inconsistent lookup identity"},
		{"instId mismatch", "inconsistent lookup identity"},
	} {
		t.Run(tc.fault, func(t *testing.T) {
			order := okxTestLookup("canceled", "0.01")
			trade := okxTestTrade(1)
			page := []okxTrade{trade}
			switch tc.fault {
			case "sum":
				order.Filled = "0.02"
			case "foreign":
				page[0].OrderID = "999"
			case "decimal":
				page[0].Quantity = "1e2"
			case "time":
				page[0].At = "bad"
			case "conflicting":
				page = append(page, trade)
				page[1].Price = "3"
			case "clOrdId not ours":
				order.ClientID = ""
			case "clOrdId mismatch":
				order.ClientID = strings.Repeat("1", 32)
			case "ordId mismatch":
				order.OrderID = "999"
				page[0].OrderID = order.OrderID
			case "instId mismatch":
				order.Symbol = "ETH-USDT"
			case "cursor":
				page = make([]okxTrade, 100)
				for i := range page {
					page[i] = trade
				}
			}
			c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v5/trade/order" {
					okxReply(w, []okxOrder{order})
					return
				}
				okxReply(w, page)
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ref := fwtrading.OrderRef{
				Symbol: "BTC-USDT", ClientOrderID: okxTestCOID, VenueOrderID: "123",
			}
			if tc.fault == "clOrdId not ours" {
				ref.ClientOrderID = ""
			}
			s, found, err := c.Lookup(ctx, ref)
			if !errors.Is(err, domain.ErrUpstream) || found ||
				!reflect.DeepEqual(s, fwtrading.Snapshot{}) {
				t.Fatal("partial or inconsistent snapshot escaped")
			}
			if !strings.Contains(err.Error(), tc.fragment) {
				t.Fatalf("wrong fault reported for %s", tc.fault)
			}
		})
	}
}

func TestOKXLookupSkipsUnneededFillRequests(t *testing.T) {
	for _, filled := range []string{"0", "0.01"} {
		t.Run(filled, func(t *testing.T) {
			var recent, history atomic.Int32
			c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v5/trade/order":
					okxReply(w, []okxOrder{okxTestLookup("canceled", filled)})
				case "/api/v5/trade/fills":
					recent.Add(1)
					okxReply(w, []okxTrade{okxTestTrade(1)})
				case "/api/v5/trade/fills-history":
					history.Add(1)
					okxReply(w, []okxTrade{okxTestTrade(1)})
				default:
					t.Error("unexpected lookup request")
				}
			})
			snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{
				Symbol: "BTC-USDT", ClientOrderID: okxTestCOID,
			})
			want := int32(0)
			if filled != "0" {
				want = 1
			}
			if err != nil || !found || len(snapshot.Fills) != int(want) ||
				recent.Load() != want || history.Load() != 0 {
				t.Fatal("lookup requested unnecessary fill history or lost a fill")
			}
		})
	}
}

func TestOKXCommission(t *testing.T) {
	c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
		okxReply(w, []okx.Instrument{okxTestInstrument()})
	})
	for _, tc := range []struct {
		fee, currency, amount string
		leg                   fwtrading.CommissionLeg
	}{
		{"-0.001", "BTC", "0.001", fwtrading.CommissionLegBase},
		{"0.002", "USDT", "-0.002", fwtrading.CommissionLegQuote},
		{"-0.3", "OTHER", "0.3", ""}, {"0", "", "", ""},
	} {
		commission, leg, err := c.commission(
			context.Background(), "BTC-USDT", tc.fee, tc.currency, true)
		if err != nil || leg != tc.leg {
			t.Fatal("commission conversion")
		}
		if tc.amount == "" {
			if commission != nil {
				t.Fatal("zero fee must be nil")
			}
			continue
		}
		if commission == nil || commission.Amount != tc.amount ||
			commission.Currency != tc.currency {
			t.Fatal("fee sign or currency")
		}
	}
}

func TestOKXLookupCommissionAndFinal(t *testing.T) {
	for _, state := range []string{"filled", "canceled"} {
		t.Run(state, func(t *testing.T) {
			c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v5/trade/order" {
					okxReply(w, []okxOrder{okxTestLookup(state, "10")})
					return
				}
				if r.URL.Path == "/api/v5/public/instruments" {
					okxReply(w, []okx.Instrument{okxTestInstrument()})
					return
				}
				a, b := okxTestTrade(1), okxTestTrade(2)
				a.Quantity, a.Fee, a.FeeCcy = "3", "-0.01", "BTC"
				b.Quantity, b.Fee, b.FeeCcy = "7", "0.02", "USDT"
				okxReply(w, []okxTrade{b, a})
			})
			s, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{
				Symbol: "BTC-USDT", VenueOrderID: "123",
			})
			if err != nil || !found || len(s.Fills) != 2 {
				t.Fatal("commission lookup")
			}
			a, b := s.Fills[0], s.Fills[1]
			if a.Commission == nil || a.Commission.Amount != "0.01" ||
				a.CommissionLeg != fwtrading.CommissionLegBase ||
				b.Commission == nil || b.Commission.Amount != "-0.02" ||
				b.CommissionLeg != fwtrading.CommissionLegQuote ||
				a.Final || b.Final != (state == "filled") {
				t.Fatal("snapshot commission or final marker")
			}
		})
	}
}

func TestOKXAccountSymbolsPreCheck(t *testing.T) {
	instrument := okxTestInstrument()
	c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/account/config" {
			okxReply(w, []map[string]string{{"uid": "12345"}})
			return
		}
		if r.Header.Get("x-simulated-trading") != "1" || r.Header.Get("OK-ACCESS-KEY") != "" {
			t.Error("public demo catalogue header")
		}
		okxReply(w, []okx.Instrument{instrument})
	})
	info, err := c.CheckAccount(context.Background())
	if err != nil || info.Identity != "12345" {
		t.Fatal("account uid")
	}
	v, err := c.VerifySymbol(context.Background(), "BTC-USDT")
	if err != nil || !v.Exists || !v.Tradable {
		t.Fatal("verify")
	}
	matches, err := c.SearchSymbols(context.Background(), "BTC")
	if err != nil || len(matches) != 1 || matches[0].BaseAsset != "BTC" ||
		matches[0].QuoteAsset != "USDT" {
		t.Fatal("search assets")
	}
	for _, tc := range []struct {
		name       string
		qty, price string
		bad        bool
	}{
		{"valid", "0.2", "2.01", false}, {"minimum", "0.01", "", true},
		{"lot", "0.15", "", true}, {"tick", "1", "2.001", true},
		{"negative", "-1", "", true}, {"zero price", "1", "0", true},
		{"exponent", "1e2", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := okxTestOrder()
			o.Quantity = tc.qty
			o.LimitPrice = tc.price
			if tc.price != "" {
				o.Route = `{"timeInForce":"GTC"}`
			}
			err := c.PreCheck(context.Background(), o)
			if (err != nil) != tc.bad {
				t.Fatal("precheck rule")
			}
		})
	}
	instrument.State = "suspend"
	err = c.PreCheck(context.Background(), okxTestOrder())
	if !errors.Is(err, fwtrading.ErrRejected) {
		t.Fatal("suspended instrument accepted")
	}
	instrument.State = "live"
	instrument.LotSz = "0"
	err = c.PreCheck(context.Background(), okxTestOrder())
	if !errors.Is(err, domain.ErrUpstream) {
		t.Fatal("invalid venue rule accepted")
	}
}

func TestOKXRedaction(t *testing.T) {
	var signature string
	c := newOKXTest(t, func(w http.ResponseWriter, r *http.Request) {
		signature = r.Header.Get("OK-ACCESS-SIGN")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "51000", "msg": "Order refused " + r.Header.Get("OK-ACCESS-KEY") +
				" " + r.Header.Get("OK-ACCESS-PASSPHRASE") + " " + signature + " okx-secret-marker",
		})
	})
	_, err := c.Send(context.Background(), okxTestOrder())
	if !errors.Is(err, fwtrading.ErrRejected) {
		t.Fatal("refusal classification")
	}
	message := c.redact.Replace(err.Error())
	for _, secret := range []string{
		"okx-key-marker", "okx-secret-marker", "okx-pass-marker", signature, okx.GlobalSite,
	} {
		if secret != "" && strings.Contains(message, secret) {
			t.Fatal("credential or signature leaked")
		}
	}
	if !strings.Contains(message, "Order refused") || !strings.Contains(message, "okx:") {
		t.Fatal("global-site refusal is illegible")
	}
}

func FuzzOKXClientIDRoundTrip(f *testing.F) {
	f.Add([]byte("0123456789abcdef"))
	f.Add([]byte{0, 255})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		coid := base64.RawURLEncoding.EncodeToString(raw)
		id, err := okxClientID(coid)
		if len(raw) != 16 {
			if !errors.Is(err, fwtrading.ErrRejected) {
				t.Fatal("invalid byte length accepted")
			}
			return
		}
		back, own := okxOfficerID(id)
		if err != nil || len(id) != 32 || id != strings.ToLower(id) || !own || back != coid {
			t.Fatal("client id round trip")
		}
	})
}

func FuzzOKXFillChain(f *testing.F) {
	f.Add([]byte{2, 1, 3, 2})
	f.Add([]byte{255, 0, 1})
	var handler atomic.Value
	c := newOKXTest(f, func(w http.ResponseWriter, r *http.Request) {
		handler.Load().(http.HandlerFunc)(w, r)
	})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) < 2 || len(raw) > 20 {
			return
		}
		// Executions arrive in different orders and overlap across endpoints.
		sum := 0
		trades := make([]okxTrade, len(raw))
		for i, b := range raw {
			n := int(b%5) + 1
			sum += n
			trades[i] = okxTestTrade(i + 1)
			trades[i].Quantity = strconv.Itoa(n)
			trades[i].At = "1700000000000"
		}
		drop := 1 + int(raw[0])%(len(raw)-1)
		start := int(raw[len(raw)-1]) % len(raw)
		var historySeen atomic.Bool
		handler.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v5/trade/order" {
				o := okxTestLookup("filled", strconv.Itoa(sum))
				o.Quantity = strconv.Itoa(sum)
				okxReply(w, []okxOrder{o})
				return
			}
			page := append([]okxTrade(nil), trades...)
			switch r.URL.Path {
			case "/api/v5/trade/fills":
				page = page[:len(trades)-drop]
				for i := range page {
					page[i] = trades[(start+len(trades)-i)%len(trades)]
				}
			case "/api/v5/trade/fills-history":
				historySeen.Store(true)
			}
			okxReply(w, page)
		}))
		s, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{
			Symbol: "BTC-USDT", ClientOrderID: okxTestCOID,
		})
		if err != nil || !found || len(s.Fills) != len(raw) || !historySeen.Load() {
			t.Fatalf("fill chain coverage: found=%t fills=%d history=%t: %s",
				found, len(s.Fills), historySeen.Load(), c.redact.Replace(fmt.Sprint(err)))
		}
		cum := 0
		for i, fill := range s.Fills {
			cum += int(raw[i]%5) + 1
			if fill.CumQuantity != strconv.Itoa(cum) ||
				fill.LeavesQuantity != strconv.Itoa(sum-cum) ||
				fill.Price != strconv.Itoa(i+1) || fill.Final != (i == len(raw)-1) {
				t.Fatal("fill chain invariant")
			}
		}
	})
}
