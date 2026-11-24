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
	"go.openpit.dev/officer/internal/bybit"
)

func newBybitTest(t *testing.T, handler http.HandlerFunc) *bybitConnector {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := newBybitConnector(domain.TradingConnection{
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

func writeBybitResult(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	writeBinanceJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": result})
}

func TestBybitProvider(t *testing.T) {
	p := BybitProvider()
	if p.Type != domain.TradingProviderBybit || p.Title != "Bybit" {
		t.Fatal("Bybit provider identity differs")
	}
	if p.VenueAccounts {
		t.Fatal("Bybit must not advertise venue accounts")
	}
	if !p.VerifiesSymbols {
		t.Fatal("Bybit must advertise symbol verification")
	}
	if !p.SearchesSymbols {
		t.Fatal("Bybit must advertise symbol search")
	}
	if p.UnlinkedFees {
		t.Fatal("Bybit must not advertise unlinked fees")
	}
	if p.Build == nil {
		t.Fatal("Bybit provider must have a constructor")
	}
	connector, err := p.Build(fwtrading.BuildContext{Connection: domain.TradingConnection{
		Mode: domain.TradingModePaper, Credentials: binanceTestCredentials,
	}})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := connector.(*bybitConnector)
	if !ok || c == nil {
		t.Fatal("Bybit provider must build a Bybit connector")
	}
	defer c.Close()
}

func TestBybitConstruction(t *testing.T) {
	for _, tc := range []struct {
		mode         domain.TradingMode
		rest, stream string
	}{
		{domain.TradingModePaper, bybit.TestnetRESTURL, bybit.TestnetPrivateStreamURL},
		{domain.TradingModeLive, bybit.LiveRESTURL, bybit.LivePrivateStreamURL},
	} {
		connector, err := NewBybitConnector(domain.TradingConnection{
			Mode: tc.mode, Credentials: binanceTestCredentials,
		})
		if err != nil {
			t.Fatal(err)
		}
		c := connector.(*bybitConnector)
		defer c.Close()
		if c.restURL != tc.rest || c.streamURL != tc.stream || c.httpClient.Timeout != 10*time.Second {
			t.Fatal("incorrect mode endpoints or HTTP timeout")
		}
	}
	if _, err := NewBybitConnector(domain.TradingConnection{Mode: "invalid"}); err == nil {
		t.Fatal("unknown mode accepted")
	}
	for _, tc := range []struct{ raw, pointer string }{
		{`{`, "/credentials"}, {`{"apiSecret":"secret"}`, "/credentials/apiKey"},
		{`{"apiKey":"key"}`, "/credentials/apiSecret"},
	} {
		_, err := NewBybitConnector(domain.TradingConnection{Mode: domain.TradingModePaper, Credentials: tc.raw})
		if err == nil || domain.ValidationPointer(err) != tc.pointer {
			t.Fatalf("validation pointer = %q, want %q", domain.ValidationPointer(err), tc.pointer)
		}
	}
}

func TestBybitPreCheck(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fwtrading.Order)
		status string
		absent bool
		want   error
	}{
		{"valid", func(*fwtrading.Order) {}, "Trading", false, nil},
		{"route", func(o *fwtrading.Order) { o.Route = `{"unknown":1}` }, "", false, fwtrading.ErrRejected},
		{"amount", func(o *fwtrading.Order) { o.AmountKind = "notional" }, "", false, fwtrading.ErrUnsupported},
		{"side", func(o *fwtrading.Order) { o.Side = "unknown" }, "", false, fwtrading.ErrRejected},
		{"missing", func(*fwtrading.Order) {}, "Trading", true, fwtrading.ErrRejected},
		{"halted", func(*fwtrading.Order) {}, "Closed", false, fwtrading.ErrRejected},
		{"rule", func(o *fwtrading.Order) { o.Quantity = "4" }, "Trading", false, fwtrading.ErrRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != bybit.InstrumentsPath || r.URL.Query().Get("category") != "spot" || r.URL.Query().Get("symbol") != "BTCUSDT" {
					t.Error("incorrect instrument query")
				}
				rules := bybitTestRules()
				rules.Status = tc.status
				list := []bybit.InstrumentRules{rules}
				if tc.absent {
					list = []bybit.InstrumentRules{}
				}
				writeBybitResult(t, w, map[string]any{"list": list})
			})
			order := bybitTestOrder()
			tc.change(&order)
			assertBybitError(t, c.PreCheck(context.Background(), order), tc.want)
			wantCalls := int32(1)
			if tc.name == "route" || tc.name == "amount" || tc.name == "side" {
				wantCalls = 0
			}
			if calls.Load() != wantCalls {
				t.Fatal("incorrect precheck network calls")
			}
		})
	}
}

func TestBybitSend(t *testing.T) {
	for _, limit := range []bool{false, true} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v5/order/create" {
					t.Error("incorrect order endpoint")
				}
				for _, h := range []string{"X-BAPI-API-KEY", "X-BAPI-TIMESTAMP", "X-BAPI-RECV-WINDOW", "X-BAPI-SIGN"} {
					if r.Header.Get(h) == "" {
						t.Errorf("missing signed header %s", h)
					}
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				want := `{"category":"spot","symbol":"BTCUSDT","side":"Buy","orderType":"Market","qty":"2.000","orderLinkId":"client-1","isLeverage":0,"orderFilter":"Order","marketUnit":"baseCoin"}`
				if limit {
					want = `{"category":"spot","symbol":"BTCUSDT","side":"Buy","orderType":"Limit","qty":"2.000","orderLinkId":"client-1","isLeverage":0,"orderFilter":"Order","price":"10","timeInForce":"GTC"}`
				}
				if string(body) != want {
					t.Fatalf("body = %s, want %s", body, want)
				}
				writeBybitResult(t, w, map[string]string{"orderId": "venue-1", "orderLinkId": "client-1"})
			})
			order := bybitTestOrder()
			if limit {
				order.LimitPrice, order.Route = "10", `{"timeInForce":"GTC"}`
			}
			ack, err := c.Send(context.Background(), order)
			if err != nil || ack.VenueOrderID != "venue-1" {
				t.Fatalf("ack = %+v, err = %v", ack, err)
			}
		})
	}
	for _, code := range []int{10005, 10000} {
		c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
			writeBinanceJSON(t, w, map[string]any{"retCode": code, "retMsg": "refused"})
		})
		_, err := c.Send(context.Background(), bybitTestOrder())
		if err == nil || errors.Is(err, fwtrading.ErrRejected) != (code == 10005) {
			t.Fatal("wrong send refusal classification")
		}
	}
	c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) { t.Error("refused order reached HTTP") })
	order := bybitTestOrder()
	order.AmountKind = "notional"
	_, err := c.Send(context.Background(), order)
	assertBybitError(t, err, fwtrading.ErrUnsupported)
}

func TestBybitLookup(t *testing.T) {
	created := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	for _, tc := range []struct {
		name                         string
		realtime, history            int
		zeroTime, wantFound, wantErr bool
	}{
		{"realtime", 1, 0, false, true, false},
		{"history", 0, 1, false, true, false},
		{"absent", 0, 0, false, false, false},
		{"missing-time", 0, 0, true, false, true},
		{"multiple-realtime", 2, 0, false, false, true},
		{"multiple-history", 0, 2, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				q := r.URL.Query()
				if q.Get("category") != "spot" || q.Get("symbol") != "BTCUSDT" || q.Get("orderLinkId") != "client-1" || q.Get("orderId") != "" {
					t.Error("incorrect lookup filter")
				}
				n := tc.realtime
				switch r.URL.Path {
				case "/v5/order/realtime":
				case "/v5/order/history":
					n = tc.history
					start := created.Add(-time.Minute)
					if q.Get("startTime") != strconv.FormatInt(start.UnixMilli(), 10) || q.Get("endTime") != strconv.FormatInt(start.Add(7*24*time.Hour).UnixMilli(), 10) {
						t.Error("history did not use Officer creation time and margin")
					}
				default:
					t.Error("zero-fill lookup made an unexpected call")
				}
				list := make([]bybitOrder, n)
				for i := range list {
					list[i] = bybitTestVenueOrder()
					list[i].CumExecQty = "0"
				}
				writeBybitResult(t, w, map[string]any{"list": list})
			})
			c.cacheSymbol(bybitTestRules().Instrument)
			ref := fwtrading.OrderRef{ClientOrderID: "client-1", VenueOrderID: "venue-1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy, CreatedAt: created}
			if tc.zeroTime {
				ref.CreatedAt = time.Time{}
			}
			snapshot, found, err := c.Lookup(context.Background(), ref)
			if found != tc.wantFound || (err != nil) != tc.wantErr {
				t.Fatalf("found=%v err=%v", found, err)
			}
			if tc.wantErr && !errors.Is(err, domain.ErrUpstream) {
				t.Fatalf("wrong error class: %v", err)
			}
			if found && (snapshot.VenueOrderID != "venue-1" || snapshot.Status != fwtrading.VenueStatusOpen || snapshot.FilledQuantity != "0" || len(snapshot.Fills) != 0) {
				t.Fatalf("snapshot=%+v", snapshot)
			}
			wantCalls := int32(1)
			if tc.realtime == 0 && !tc.zeroTime {
				wantCalls = 2
			}
			if calls.Load() != wantCalls {
				t.Fatalf("calls=%d want=%d", calls.Load(), wantCalls)
			}
		})
	}
	for _, field := range []string{"symbol", "side", "client", "venue", "missing-list", "nil-reporter", "empty-symbol", "empty-side", "unknown-side", "empty-reference"} {
		t.Run(field, func(t *testing.T) {
			c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
				record := bybitTestVenueOrder()
				record.CumExecQty = "0"
				switch field {
				case "symbol":
					record.Symbol = "ETHUSDT"
				case "side":
					record.Side = "Sell"
				case "client":
					record.OrderLinkID = "other"
				case "venue":
					record.OrderID = "other"
				}
				if field == "missing-list" {
					writeBybitResult(t, w, map[string]any{})
					return
				}
				writeBybitResult(t, w, map[string]any{"list": []bybitOrder{record}})
			})
			c.cacheSymbol(bybitTestRules().Instrument)
			ref := fwtrading.OrderRef{ClientOrderID: "client-1", VenueOrderID: "venue-1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy}
			switch field {
			case "nil-reporter":
				c.SetDiagnosticReporter(nil)
			case "empty-symbol":
				ref.Symbol = ""
			case "empty-side":
				ref.Side = ""
			case "unknown-side":
				ref.Side = domain.OrderSide("hold")
			case "empty-reference":
				ref.ClientOrderID, ref.VenueOrderID = "", ""
			}
			_, found, err := c.Lookup(context.Background(), ref)
			if err == nil || found {
				t.Fatal("unusable lookup accepted")
			}
			if (field == "empty-side" || field == "unknown-side") &&
				(err.Error() != "bybit: order side is required" || errors.Is(err, fwtrading.ErrRejected)) {
				t.Fatalf("wrong invalid-side error: %v", err)
			}
			if field == "side" && (!errors.Is(err, domain.ErrUpstream) ||
				err.Error() != fmt.Sprintf("bybit: lookup order reference mismatch: %v", domain.ErrUpstream)) {
				t.Fatalf("wrong side-mismatch error: %v", err)
			}
		})
	}
	t.Run("venue-id-only", func(t *testing.T) {
		c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("orderId") != "venue-1" || r.URL.Query().Get("orderLinkId") != "" {
				t.Error("venue id filter missing")
			}
			record := bybitTestVenueOrder()
			record.CumExecQty = "0"
			writeBybitResult(t, w, map[string]any{"list": []bybitOrder{record}})
		})
		c.cacheSymbol(bybitTestRules().Instrument)
		_, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{VenueOrderID: "venue-1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy})
		if err != nil || !found {
			t.Fatalf("found=%v err=%v", found, err)
		}
	})
	t.Run("cancelled-zero-fill-delisted", func(t *testing.T) {
		c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v5/order/realtime":
				record := bybitTestVenueOrder()
				record.OrderStatus, record.CumExecQty = "Cancelled", "0"
				writeBybitResult(t, w, map[string]any{"list": []bybitOrder{record}})
			case "/v5/market/instruments-info":
				writeBybitResult(t, w, map[string]any{"list": []bybit.InstrumentRules{}})
			default:
				t.Error("unexpected zero-fill lookup endpoint")
			}
		})
		snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{
			ClientOrderID: "client-1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy,
		})
		if err != nil || !found || snapshot.Status != fwtrading.VenueStatusCancelled ||
			snapshot.FilledQuantity != "0" || len(snapshot.Fills) != 0 {
			t.Fatalf("snapshot=%+v found=%v err=%v", snapshot, found, err)
		}
	})
}

func TestBybitLookupExecutions(t *testing.T) {
	created := time.Now().Add(-8 * 24 * time.Hour).Truncate(time.Millisecond)
	start := created.Add(-time.Minute).UnixMilli()
	var calls atomic.Int32
	c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v5/order/realtime" {
			record := bybitTestVenueOrder()
			record.CreatedTime = strconv.FormatInt(created.UnixMilli(), 10)
			record.OrderStatus = "Filled"
			record.CumExecQty = "3.000"
			writeBybitResult(t, w, map[string]any{"list": []bybitOrder{record}})
			return
		}
		if r.URL.Path != "/v5/execution/list" {
			t.Error("unexpected endpoint")
		}
		i := int(calls.Add(1)) - 1
		if i >= 4 {
			t.Error("excess execution pages")
			writeBybitResult(t, w, map[string]any{"list": []bybitExecution{}})
			return
		}
		q := r.URL.Query()
		from, _ := strconv.ParseInt(q.Get("startTime"), 10, 64)
		to, _ := strconv.ParseInt(q.Get("endTime"), 10, 64)
		window := i / 3
		wantStart := start
		if window == 1 {
			wantStart += bybitLookupWindow.Milliseconds() + 1
		}
		if from != wantStart || to < from || to-from > bybitLookupWindow.Milliseconds() {
			t.Error("incorrect execution window")
		}
		if window == 0 && to != start+bybitLookupWindow.Milliseconds() {
			t.Error("first window truncated")
		}
		if window == 1 && time.UnixMilli(to).Before(time.Now().Add(59*time.Second)) {
			t.Error("last window misses now plus margin")
		}
		wantCursor := []string{"", "cursor-1", "cursor-2", ""}[i]
		if q.Get("cursor") != wantCursor || q.Get("category") != "spot" || q.Get("symbol") != "BTCUSDT" || q.Get("orderId") != "venue-1" || q.Get("limit") != "100" {
			t.Error("incorrect execution query")
		}
		list := []bybitExecution{}
		if i != 1 {
			exec := bybitTestExecution()
			n := 1
			if i == 2 {
				n = 2
			}
			if i == 3 {
				n = 3
			}
			exec.ExecID = fmt.Sprintf("exec-%d", n)
			exec.LeavesQty = strconv.Itoa(3 - n)
			exec.ExecTime = strconv.FormatInt(created.Add(time.Duration(n)*time.Second).UnixMilli(), 10)
			list = append(list, exec)
		}
		cursor := ""
		if i == 0 {
			cursor = "cursor-1"
		}
		if i == 1 {
			cursor = "cursor-2"
		}
		writeBybitResult(t, w, map[string]any{"list": list, "nextPageCursor": cursor})
	})
	c.cacheSymbol(bybitTestRules().Instrument)
	snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "client-1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy})
	if err != nil || !found || calls.Load() != 4 || len(snapshot.Fills) != 3 {
		t.Fatalf("snapshot=%+v found=%v calls=%d err=%v", snapshot, found, calls.Load(), err)
	}
	for i, fill := range snapshot.Fills {
		exec := bybitTestExecution()
		exec.LeavesQty = strconv.Itoa(2 - i)
		exec.ExecTime = strconv.FormatInt(created.Add(time.Duration(i+1)*time.Second).UnixMilli(), 10)
		want, err := exec.fill("BTC", "USDT")
		if err != nil || !reflect.DeepEqual(fill, want.fill) {
			t.Fatalf("fill %d=%+v want=%+v", i, fill, want.fill)
		}
	}
}

func TestBybitLookupDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, detail string
		sentinel     error
	}{
		{"status", bybitLookupOrderStatus, errBybitOrderStatus},
		{"fees", bybitLookupExtraFees, errBybitExtraFees},
		{"time", "", nil}, {"pagination", "", nil}, {"quantity", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v5/order/realtime" {
					record := bybitTestVenueOrder()
					record.CreatedTime = strconv.FormatInt(time.Now().Add(-time.Hour).UnixMilli(), 10)
					switch tc.name {
					case "status":
						record.OrderStatus = "FutureStatus"
					case "time":
						record.CreatedTime = "+123"
					case "quantity":
						record.CumExecQty = "bad"
					}
					writeBybitResult(t, w, map[string]any{"list": []bybitOrder{record}})
					return
				}
				exec := bybitTestExecution()
				if tc.name == "fees" {
					exec.ExtraFees = json.RawMessage(`[{"feeCoin":"BTC","fee":"0.1"}]`)
				}
				cursor := ""
				if tc.name == "pagination" {
					cursor = "repeated"
				}
				writeBybitResult(t, w, map[string]any{"list": []bybitExecution{exec}, "nextPageCursor": cursor})
			})
			c.cacheSymbol(bybitTestRules().Instrument)
			var diags []health.Diagnostic
			c.SetDiagnosticReporter(func(d health.Diagnostic) { diags = append(diags, d) })
			_, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{ClientOrderID: "client-1", Symbol: "BTCUSDT", Side: domain.OrderSideBuy})
			if err == nil || found || !errors.Is(err, domain.ErrUpstream) {
				t.Fatalf("found=%v err=%v", found, err)
			}
			if tc.sentinel != nil && (!errors.Is(err, tc.sentinel) || len(diags) != 1 || !reflect.DeepEqual(diags[0], venueLookupUnparsable(tc.detail))) {
				t.Fatalf("sentinel or diagnostic missing: %v %+v", err, diags)
			}
			if tc.sentinel == nil && len(diags) != 0 {
				t.Fatal("unexpected diagnostic")
			}
		})
	}
}

func TestBybitAccount(t *testing.T) {
	for _, tc := range []struct {
		uta, read int
		spot      bool
		want      string
	}{
		{0, 0, true, "classic"}, {1, 0, true, "unified"}, {1, 1, true, "unified, read-only"},
		{0, 0, false, "classic, spot trading not permitted"}, {0, 1, false, "classic, read-only, spot trading not permitted"},
	} {
		c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v5/user/query-api" {
				t.Error("incorrect account endpoint")
			}
			spot := []string{}
			if tc.spot {
				spot = []string{"SpotTrade"}
			}
			writeBybitResult(t, w, map[string]any{"userID": json.Number("9007199254740993"), "uta": tc.uta, "readOnly": tc.read, "permissions": map[string]any{"Spot": spot}, "apiKey": map[string]any{"ignored": true}})
		})
		account, err := c.CheckAccount(context.Background())
		if err != nil || account.Identity != "9007199254740993" || account.Status != tc.want {
			t.Fatalf("account=%+v err=%v", account, err)
		}
	}
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"missing-userID", "userID", nil},
		{"zero-userID", "userID", json.Number("0")},
		{"fractional-userID", "userID", json.Number("1.5")},
		{"exponent-userID", "userID", json.Number("1e3")},
		{"string-userID", "userID", "123"},
		{"negative-userID", "userID", json.Number("-1")},
		{"missing-uta", "uta", nil},
		{"invalid-uta", "uta", 2},
		{"missing-readOnly", "readOnly", nil},
		{"invalid-readOnly", "readOnly", 2},
		{"missing-permissions", "permissions", nil},
		{"missing-Spot", "permissions", map[string]any{}},
		{"null-Spot", "permissions", map[string]any{"Spot": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := map[string]any{
				"userID": json.Number("9007199254740993"), "uta": 1,
				"readOnly": 0, "permissions": map[string]any{"Spot": []string{"SpotTrade"}},
			}
			if tc.value == nil {
				delete(response, tc.field)
			} else {
				response[tc.field] = tc.value
			}
			c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
				writeBybitResult(t, w, response)
			})
			_, err := c.CheckAccount(context.Background())
			if !errors.Is(err, domain.ErrUpstream) {
				t.Fatalf("bad account accepted or wrong class: %v", err)
			}
			if tc.field == "permissions" && err.Error() !=
				fmt.Sprintf("bybit: unreadable account status: %v", domain.ErrUpstream) {
				t.Fatalf("wrong permissions error: %v", err)
			}
		})
	}
}

func TestBybitSymbols(t *testing.T) {
	c := newBybitTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != bybit.InstrumentsPath || r.URL.Query().Get("category") != "spot" {
			t.Error("incorrect catalogue endpoint")
		}
		rules := bybitTestRules()
		if symbol := r.URL.Query().Get("symbol"); symbol != "" {
			list := []bybit.InstrumentRules{}
			if symbol == "BTCUSDT" {
				list = append(list, rules)
			}
			if symbol == "BTCUSDC" {
				rules.Symbol, rules.QuoteCoin, rules.Status = "BTCUSDC", "USDC", "Closed"
				list = append(list, rules)
			}
			writeBybitResult(t, w, map[string]any{"list": list})
			return
		}
		writeBybitResult(t, w, map[string]any{"list": []bybit.Instrument{
			{Symbol: "BTCUSDC", BaseCoin: "BTC", QuoteCoin: "USDC", Status: "Closed"},
			rules.Instrument,
		}})
	})
	for _, symbol := range []string{"BTCUSDT", "BTCUSDC", "MISSING"} {
		v, err := c.VerifySymbol(context.Background(), symbol)
		if err != nil || v.Exists != (symbol != "MISSING") || v.Tradable != (symbol == "BTCUSDT") {
			t.Fatalf("verification=%+v err=%v", v, err)
		}
		if v.Exists && v.Details != map[string]string{"BTCUSDT": "BTC/USDT Trading", "BTCUSDC": "BTC/USDC Closed"}[symbol] {
			t.Fatal("incorrect details")
		}
	}
	matches, err := c.SearchSymbols(context.Background(), "BTCUSDT")
	if err != nil || len(matches) != 1 || matches[0] != (fwtrading.SymbolMatch{Symbol: "BTCUSDT", Name: "BTC/USDT", Exchange: "Bybit", Class: "crypto", BaseAsset: "BTC", QuoteAsset: "USDT", Tradable: true}) {
		t.Fatalf("matches=%+v err=%v", matches, err)
	}
	matches, err = c.SearchSymbols(context.Background(), "BTC")
	if err != nil || len(matches) != 2 || matches[0].Symbol != "BTCUSDC" || matches[0].Tradable || matches[1].Symbol != "BTCUSDT" {
		t.Fatalf("ranking=%+v err=%v", matches, err)
	}
	for _, query := range []string{"", "   ", "NO-SUCH-SYMBOL"} {
		matches, err = c.SearchSymbols(context.Background(), query)
		if err != nil || len(matches) != 0 {
			t.Fatal("empty or absent search returned results")
		}
	}
	base, quote, err := c.coins(context.Background(), "BTCUSDC")
	if err != nil || base != "BTC" || quote != "USDC" {
		t.Fatal("catalogue coins were not cached")
	}
	_, _, err = c.coins(context.Background(), "MISSING")
	if !errors.Is(err, domain.ErrUpstream) {
		t.Fatal("unknown coins accepted")
	}
}
