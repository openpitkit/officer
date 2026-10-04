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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fwtrading "go.openpit.dev/officer/framework/trading"
)

func TestFeesMapping(t *testing.T) {
	for _, tc := range []struct {
		name, kind, qty, net, currency, symbol, status string
		asset, amount, reason                          string
	}{
		{name: "USD fee", kind: "FEE", net: "-0.123456789012345678", currency: "USD", asset: "USD", amount: "-0.123456789012345678"},
		{name: "positive fee delta", kind: "FEE", net: "0.01", currency: "USD", asset: "USD", amount: "0.01"},
		{name: "FEE missing currency", kind: "FEE", net: "-1", reason: "requires currency"},
		{name: "FEE missing amount", kind: "FEE", currency: "USD", reason: "non-zero net_amount"},
		{name: "FEE zero", kind: "FEE", net: "-0.000", currency: "USD", reason: "non-zero net_amount"},
		{name: "FEE bad decimal", kind: "FEE", net: "garbage", currency: "USD", reason: "invalid net_amount"},
		{name: "FEE exponent", kind: "FEE", net: "1e-4", currency: "USD", reason: "invalid net_amount"},
		{name: "FEE whitespace", kind: "FEE", net: " -1", currency: "USD", reason: "invalid net_amount"},
		{name: "legacy crypto qty", kind: "CFEE", qty: "-0.000195", net: "0", symbol: "ETHUSD", asset: "ETH", amount: "-0.000195"},
		{name: "crypto empty money", kind: "CFEE", qty: "-0.000000000000000001", symbol: "ETH/USD", asset: "ETH", amount: "-0.000000000000000001"},
		{name: "crypto money", kind: "CFEE", qty: "0.00", net: "-0.25", currency: "USD", asset: "USD", amount: "-0.25"},
		{name: "crypto empty qty", kind: "CFEE", net: "-0.25", currency: "USD", asset: "USD", amount: "-0.25"},
		{name: "crypto both amounts", kind: "CFEE", qty: "-0.1", net: "-1", currency: "USD", symbol: "ETHUSD", reason: "exactly one"},
		{name: "crypto neither amount", kind: "CFEE", reason: "exactly one"},
		{name: "crypto both zero", kind: "CFEE", qty: "0", net: "0.000", reason: "exactly one"},
		{name: "crypto bad qty", kind: "CFEE", qty: "NaN", reason: "invalid qty"},
		{name: "crypto double sign", kind: "CFEE", qty: "--0", net: "-1", currency: "USD", reason: "invalid qty"},
		{name: "crypto bad unused qty", kind: "CFEE", qty: "bad", net: "-1", currency: "USD", reason: "invalid qty"},
		{name: "crypto bad unused money", kind: "CFEE", qty: "-1", net: "bad", symbol: "ETHUSD", reason: "invalid net_amount"},
		{name: "crypto missing currency", kind: "CFEE", net: "-1", reason: "requires currency"},
		{name: "crypto missing symbol", kind: "CFEE", qty: "-1", reason: "requires symbol"},
		{name: "crypto unknown asset", kind: "CFEE", qty: "-1", symbol: "UNKNOWN", reason: "resolve to BASE/QUOTE"},
		{name: "crypto asset without pair", kind: "CFEE", qty: "-1", symbol: "NOPAIR", reason: "resolve to BASE/QUOTE"},
		{name: "crypto malformed pair", kind: "CFEE", qty: "-1", symbol: "BADPAIR", reason: "malformed BASE/QUOTE"},
		{name: "correct", kind: "FEE", status: "correct", net: "-1", currency: "USD", reason: "status correct"},
		{name: "canceled", kind: "CFEE", status: "canceled", qty: "-1", symbol: "ETHUSD", reason: "status canceled"},
		{name: "unknown status", kind: "FEE", status: "pending", net: "-1", currency: "USD", reason: "status pending"},
		{name: "unknown type", kind: "OTHER", reason: "unsupported activity type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Date(2026, 9, 30, 21, 0, 0, 123, time.UTC)
			status := tc.status
			if status == "" {
				status = "executed"
			}
			activity := alpacaFeeActivity{ID: "activity-id", Type: tc.kind, Quantity: tc.qty,
				NetAmount: tc.net, Currency: tc.currency, Symbol: tc.symbol, Status: status,
				CreatedAt: at, Description: "fee test-key-marker test-secret-marker"}
			lookups := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v2/account/activities":
					_ = json.NewEncoder(w).Encode([]alpacaFeeActivity{activity})
				case strings.HasPrefix(r.URL.Path, "/v2/assets/"):
					lookups++
					symbol := strings.TrimPrefix(r.URL.Path, "/v2/assets/")
					if symbol != tc.symbol {
						t.Errorf("asset lookup symbol = %s, want %s", symbol, tc.symbol)
					}
					switch symbol {
					case "ETHUSD", "ETH/USD":
						_, _ = fmt.Fprint(w, `{"symbol":"ETH/USD","class":"crypto"}`)
					case "NOPAIR":
						_, _ = fmt.Fprint(w, `{"symbol":"ETHUSD"}`)
					case "BADPAIR":
						_, _ = fmt.Fprint(w, `{"symbol":"ETH/"}`)
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
			}))
			defer server.Close()
			c := testAlpacaConnector(t, server)
			fees, next, err := c.Fees(context.Background(), at.Add(-time.Hour), "")
			if err != nil || len(fees) != 1 || next != "" {
				t.Fatalf("fees=%v err=%v", fees, err)
			}
			fee := fees[0]
			if fee.ID != activity.ID || !fee.At.Equal(at) {
				t.Fatal("fee lost activity identity or creation time")
			}
			if strings.Contains(fee.Detail, "test-key-marker") || strings.Contains(fee.Detail, "test-secret-marker") {
				t.Fatal("fee detail leaked credentials")
			}
			if tc.reason != "" {
				if fee.Status != fwtrading.FeeNotApplicable || !strings.Contains(fee.Detail, tc.reason) || fee.Asset != "" || fee.Amount != "" {
					t.Fatalf("not-applicable fee = %+v, want reason %s", fee, tc.reason)
				}
			} else if fee.Status != fwtrading.FeeExecuted || fee.Asset != tc.asset || fee.Amount != tc.amount {
				t.Fatalf("fee = %+v, want exact (%s, %s)", fee, tc.asset, tc.amount)
			}
			if tc.name == "legacy crypto qty" && lookups != 1 {
				t.Fatal("legacy CFEE symbol was not resolved through asset discovery")
			}
		})
	}
}

func TestFeesPagination(t *testing.T) {
	since := time.Date(2026, 9, 30, 13, 4, 5, 123456789, time.FixedZone("offset", 3*60*60))
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/v2/account/activities" ||
			q.Get("activity_types") != "FEE,CFEE" || q.Get("after") != since.Format(time.RFC3339Nano) ||
			q.Get("direction") != "asc" || q.Get("page_size") != "100" {
			t.Error("fee query lost the activity filter, since, direction or page size")
		}
		requests++
		start, count := 0, 100
		if requests == 2 {
			if q.Get("page_token") != "fee-099" {
				t.Error("fee pagination lost last activity id")
			}
			start, count = 100, 2
		} else if requests != 1 || q.Get("page_token") != "" {
			t.Error("unexpected fee page")
		}
		page := make([]alpacaFeeActivity, count)
		for i := range page {
			page[i] = alpacaFeeActivity{ID: fmt.Sprintf("fee-%03d", start+i), Type: "FEE",
				Status: "executed", Currency: "USD", NetAmount: "-0.01", CreatedAt: since.Add(time.Duration(start+i+1) * time.Second)}
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	fees, next, err := c.Fees(context.Background(), since, "")
	if err != nil || len(fees) != 100 || requests != 1 || next != "fee-099" {
		t.Fatalf("one page: count=%d requests=%d next=%s err=%v", len(fees), requests, next, err)
	}
	last, next, err := c.Fees(context.Background(), since, next)
	fees = append(fees, last...)
	if err != nil || len(fees) != 102 || requests != 2 || next != "" {
		t.Fatalf("pagination: count=%d requests=%d err=%v", len(fees), requests, err)
	}
	for i, fee := range fees {
		if fee.ID != fmt.Sprintf("fee-%03d", i) || !fee.At.Equal(since.Add(time.Duration(i+1)*time.Second)) {
			t.Fatal("fee pagination changed oldest-first order")
		}
	}
}

func TestFeesRejectPageContainingCursor(t *testing.T) {
	for _, cursorIndex := range []int{99, 50} {
		t.Run(fmt.Sprintf("cursor-%03d", cursorIndex), func(t *testing.T) {
			since := time.Now()
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				wantToken := ""
				if requests == 2 {
					wantToken = fmt.Sprintf("fee-%03d", cursorIndex)
				}
				if requests > 2 || r.URL.Query().Get("page_token") != wantToken {
					t.Error("unexpected fee page token")
				}
				page := make([]alpacaFeeActivity, 100)
				for i := range page {
					page[i] = alpacaFeeActivity{ID: fmt.Sprintf("fee-%03d", i), Type: "FEE",
						Status: "executed", Currency: "USD", NetAmount: "-0.01", CreatedAt: since.Add(time.Duration(i+1) * time.Second)}
				}
				_ = json.NewEncoder(w).Encode(page)
			}))
			defer server.Close()
			c := testAlpacaConnector(t, server)
			fees, next, err := c.Fees(context.Background(), since, "")
			if err != nil || len(fees) != 100 || next != "fee-099" {
				t.Fatalf("first page: count=%d next=%s err=%v", len(fees), next, err)
			}
			after := fees[cursorIndex].ID
			fees, next, err = c.Fees(context.Background(), since, after)
			if err == nil || !strings.Contains(err.Error(), "pagination did not advance") || len(fees) != 0 || next != "" {
				t.Fatalf("repeated page: count=%d next=%s err=%v, want no fees and pagination did not advance", len(fees), next, err)
			}
		})
	}
}

func TestFeesCacheCanonicalSymbols(t *testing.T) {
	lookups := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/account/activities" {
			_ = json.NewEncoder(w).Encode([]alpacaFeeActivity{
				{ID: "eth-first", Type: "CFEE", Status: "executed", Symbol: "ETHUSD", Quantity: "-0.01"},
				{ID: "btc", Type: "CFEE", Status: "executed", Symbol: "BTCUSD", Quantity: "-0.02"},
				{ID: "eth-last", Type: "CFEE", Status: "executed", Symbol: "ETHUSD", Quantity: "-0.03"},
				{ID: "unknown-first", Type: "CFEE", Status: "executed", Symbol: "UNKNOWN", Quantity: "-1"},
				{ID: "unknown-last", Type: "CFEE", Status: "executed", Symbol: "UNKNOWN", Quantity: "-1"},
			})
			return
		}
		symbol := strings.TrimPrefix(r.URL.Path, "/v2/assets/")
		lookups[symbol]++
		if symbol == "UNKNOWN" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"symbol": strings.TrimSuffix(symbol, "USD") + "/USD", "class": "crypto"})
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	for i := 0; i < 2; i++ {
		fees, next, err := c.Fees(context.Background(), time.Now(), "")
		if err != nil || next != "" || len(fees) != 5 || fees[0].Asset != "ETH" || fees[1].Asset != "BTC" || fees[2].Asset != "ETH" ||
			fees[3].Status != fwtrading.FeeNotApplicable || fees[4].Status != fwtrading.FeeNotApplicable {
			t.Fatalf("cached canonical fees = %v, next=%s err=%v", fees, next, err)
		}
	}
	if lookups["ETHUSD"] != 1 || lookups["BTCUSD"] != 1 || lookups["UNKNOWN"] != 1 {
		t.Fatalf("CFEE symbol lookups = %v, want one per distinct symbol for connector lifetime", lookups)
	}
}

func TestFeesReturnProgressBeforeAssetFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/account/activities" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode([]alpacaFeeActivity{
			{ID: "first", Type: "FEE", Status: "executed", Currency: "USD", NetAmount: "-1"},
			{ID: "retry", Type: "CFEE", Status: "executed", Symbol: "ETHUSD", Quantity: "-1"},
			{ID: "later", Type: "FEE", Status: "executed", Currency: "USD", NetAmount: "-1"},
		})
	}))
	defer server.Close()
	fees, next, err := testAlpacaConnector(t, server).Fees(context.Background(), time.Now(), "")
	if err == nil || len(fees) != 1 || fees[0].ID != "first" || next != "" {
		t.Fatalf("asset failure lost prior progress or skipped the failed fee: fees=%v next=%s err=%v", fees, next, err)
	}
}

func TestFeesReadErrors(t *testing.T) {
	for _, failure := range []string{"activities", "asset lookup", "pagination"} {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if failure == "activities" || strings.HasPrefix(r.URL.Path, "/v2/assets/") {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = fmt.Fprint(w, `{"code":500,"message":"test-secret-marker test-key-marker"}`)
					return
				}
				page := []alpacaFeeActivity{{ID: "fee", Type: "CFEE", Status: "executed", Quantity: "-1", Symbol: "ETHUSD"}}
				if failure == "pagination" {
					page = make([]alpacaFeeActivity, 100)
					for i := range page {
						page[i] = alpacaFeeActivity{ID: "same-id", Type: "FEE", Status: "executed", Currency: "USD", NetAmount: "-1"}
					}
				}
				_ = json.NewEncoder(w).Encode(page)
			}))
			defer server.Close()
			after := ""
			if failure == "pagination" {
				after = "same-id"
			}
			fees, _, err := testAlpacaConnector(t, server).Fees(context.Background(), time.Now(), after)
			if err == nil || len(fees) != 0 {
				t.Fatal("fee read error was hidden or partial fees returned")
			}
			if strings.Contains(err.Error(), "test-secret-marker") || strings.Contains(err.Error(), "test-key-marker") {
				t.Fatal("fee read error leaked credentials")
			}
		})
	}
}

func TestInstrumentAssets(t *testing.T) {
	c := &alpacaConnector{}
	for _, tc := range []struct{ symbol, base, quote string }{
		{"AAPL", "AAPL", "USD"}, {"BRK.B", "BRK.B", "USD"},
		{"BTC/USD", "BTC", "USD"}, {"ETH/BTC", "ETH", "BTC"},
	} {
		base, quote, err := c.InstrumentAssets(tc.symbol)
		if err != nil || base != tc.base || quote != tc.quote {
			t.Fatalf("InstrumentAssets(%s) = (%s,%s,%v)", tc.symbol, base, quote, err)
		}
	}
	for _, symbol := range []string{"", "/", "BTC/", "/USD", "BTC//USD", "BTC/USD/ETH", " BTC/USD", "BTC/ USD", "AAPL\n"} {
		if _, _, err := c.InstrumentAssets(symbol); err == nil {
			t.Fatalf("malformed instrument %q accepted", symbol)
		}
	}
}

func TestFeesStaySeparateFromFills(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/account/activities":
			_, _ = fmt.Fprint(w, `[{"id":"fee","activity_type":"FEE","status":"executed","currency":"USD","net_amount":"-1"}]`)
		case "/v2/orders/venue":
			_, _ = fmt.Fprint(w, `{"id":"venue","status":"filled","filled_qty":"1"}`)
		case "/v2/account/activities/FILL":
			_, _ = fmt.Fprint(w, `[{"id":"fill","type":"fill","qty":"1","price":"2","cum_qty":"1","leaves_qty":"0","transaction_time":"2026-09-30T12:00:00Z"}]`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c := testAlpacaConnector(t, server)
	fees, _, err := c.Fees(context.Background(), time.Now(), "")
	if err != nil || len(fees) != 1 || fees[0].Amount != "-1" {
		t.Fatalf("fee read failed: %v", err)
	}
	snapshot, found, err := c.Lookup(context.Background(), fwtrading.OrderRef{VenueOrderID: "venue"})
	if err != nil || !found || len(snapshot.Fills) != 1 {
		t.Fatalf("fill read failed: %v", err)
	}
	if snapshot.Fills[0].Commission != nil {
		t.Fatal("Alpaca fee was computed or attached to a fill")
	}
}
