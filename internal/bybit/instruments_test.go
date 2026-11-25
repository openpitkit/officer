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

package bybit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestFetchInstrumentsIgnoresRules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"retCode":0,"result":{"list":[{"symbol":"BTCUSDT","baseCoin":"BTC","quoteCoin":"USDT","lotSizeFilter":[],"priceFilter":"x","status":"Trading"},{"symbol":"ETHUSDT","baseCoin":"ETH","quoteCoin":"USDT"}]}}`)
	}))
	defer server.Close()
	got, err := FetchInstruments(t.Context(), server.Client(), server.URL, "spot")
	if err != nil {
		t.Fatal(err)
	}
	want := []Instrument{
		{Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "Trading"},
		{Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("instruments = %#v, want %#v", got, want)
	}
}

func TestFetchInstrumentRules(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "GET" || r.URL.Path != InstrumentsPath || r.URL.Query().Get("category") != "spot" || r.URL.Query().Get("symbol") != "BTC+USDT" || len(r.URL.Query()) != 2 {
			t.Error("wrong instrument request")
		}
		_, _ = io.WriteString(w, `{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"ignored","list":[{"symbol":"BTC+USDT","baseCoin":"BTC","quoteCoin":"USDT","status":"Trading","lotSizeFilter":{"basePrecision":"0.00000100","quotePrecision":"0.00000001","minOrderQty":"0.000001","maxOrderQty":"17000","minOrderAmt":"5.00","maxOrderAmt":"1999999999.000","maxLimitOrderQty":"11900","maxMarketOrderQty":"8500"},"priceFilter":{"tickSize":"0.0100"}}]}}`)
	}))
	defer server.Close()
	instrument, found, err := FetchInstrument(t.Context(), server.Client(), server.URL, "spot", "BTC+USDT")
	if err != nil || !found || requests != 1 {
		t.Fatalf("lookup: found=%v requests=%d err=%v", found, requests, err)
	}
	want := InstrumentRules{
		Instrument: Instrument{Symbol: "BTC+USDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "Trading"},
		LotSizeFilter: LotSizeFilter{
			BasePrecision: "0.00000100", QuotePrecision: "0.00000001",
			MinOrderAmt: "5.00", MaxLimitOrderQty: "11900", MaxMarketOrderQty: "8500",
		},
		PriceFilter: PriceFilter{TickSize: "0.0100"},
	}
	if instrument != want {
		t.Errorf("instrument = %+v, want %+v", instrument, want)
	}
	var absent InstrumentRules
	if err := json.Unmarshal([]byte(`{"symbol":"BTCUSDT"}`), &absent); err != nil {
		t.Fatal(err)
	}
	if absent.Status != "" || absent.LotSizeFilter != (LotSizeFilter{}) || absent.PriceFilter != (PriceFilter{}) {
		t.Fatal("absent filters acquired defaults")
	}
}

func TestFetchInstrumentNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"retCode":0,"result":{"list":[]}}`)
	}))
	defer server.Close()
	rules, found, err := FetchInstrument(t.Context(), server.Client(), server.URL, "spot", "NOSUCHXYZUSDT")
	if !reflect.DeepEqual(rules, InstrumentRules{}) || found || err != nil {
		t.Fatalf("lookup: rules=%+v found=%v err=%v, want zero rules, false, nil", rules, found, err)
	}
	_, found, err = FetchInstrument(t.Context(), server.Client(), server.URL, "linear", "NOSUCHXYZUSDT")
	if err == nil || found || !strings.Contains(err.Error(), "expected exactly one matching symbol") {
		t.Fatalf("linear lookup: found=%v err=%v, want an error", found, err)
	}
}

func TestFetchInstrumentErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
		exact            bool
	}{
		{"mismatch", `{"retCode":0,"result":{"list":[{"symbol":"OTHER"}]}}`, "expected exactly one matching symbol", 200, false},
		{"multiple", `{"retCode":0,"result":{"list":[{"symbol":"BTCUSDT"},{"symbol":"BTCUSDT"}]}}`, "expected exactly one matching symbol", 200, false},
		{"venue refusal", `{"retCode":10001,"retMsg":"invalid symbol"}`, "10001: invalid symbol", 200, false},
		{"blank venue refusal", `{"retCode":10002,"retMsg":"   "}`, "bybit instruments: 10002: request failed", 200, true},
		{"HTTP refusal", `no`, "unexpected status 400", 400, false},
		{"missing code", `{"result":{}}`, "decode: retCode is required", 200, false},
		{"missing result", `{"retCode":0}`, "decode: result.list is required", 200, false},
		{"missing list", `{"retCode":0,"result":{}}`, "decode: result.list is required", 200, false},
		{"null list", `{"retCode":0,"result":{"list":null}}`, "decode: result.list is required", 200, false},
		{"bad JSON", `!`, "decode:", 200, false},
		{"bad rules", `{"retCode":0,"result":{"list":[{"symbol":"BTCUSDT","priceFilter":{"tickSize":0.01}}]}}`, "decode:", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			_, found, err := FetchInstrument(t.Context(), server.Client(), server.URL, "spot", "BTCUSDT")
			if err == nil || found || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("lookup: found=%v err=%v, want %q", found, err, tc.want)
			}
			if tc.exact && err.Error() != tc.want {
				t.Fatalf("lookup error: got %q, want %q", err, tc.want)
			}
		})
	}
	if _, _, err := FetchInstrument(t.Context(), nil, "https://venue.invalid", "spot", "BTCUSDT"); err == nil {
		t.Fatal("nil HTTP client accepted")
	}
}

func TestFetchInstrumentsPaging(t *testing.T) {
	for _, category := range []string{"spot", ""} {
		t.Run("category="+category, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != InstrumentsPath {
					t.Errorf("request = %s %s, want GET %s", r.Method, r.URL.Path, InstrumentsPath)
				}
				query := r.URL.Query()
				if !query.Has("category") || query.Get("category") != category {
					t.Errorf("category = %q, want %q (present)", query.Get("category"), category)
				}
				switch calls {
				case 1:
					if query.Has("cursor") {
						t.Errorf("first page cursor = %q, want absent", query.Get("cursor"))
					}
					_, _ = fmt.Fprint(w, `{"retCode":0,"result":{"list":[{"symbol":" BTCUSDT ","baseCoin":" BTC ","quoteCoin":" USDT "},{"symbol":"","baseCoin":"","quoteCoin":""}],"nextPageCursor":" next/+ page "}}`)
				case 2:
					if got := query.Get("cursor"); got != "next/+ page" {
						t.Errorf("second page cursor = %q, want %q", got, "next/+ page")
					}
					_, _ = fmt.Fprint(w, `{"retCode":0,"result":{"list":[{"symbol":"ETHUSDT","baseCoin":"ETH","quoteCoin":"USDT"}],"nextPageCursor":"last"}}`)
				case 3:
					if got := query.Get("cursor"); got != "last" {
						t.Errorf("last page cursor = %q, want %q", got, "last")
					}
					_, _ = fmt.Fprint(w, `{"retCode":0,"result":{"list":[],"nextPageCursor":"   "}}`)
				default:
					t.Errorf("unexpected request %d", calls)
					_, _ = fmt.Fprint(w, `{"retCode":0,"result":{}}`)
				}
			}))
			defer server.Close()
			got, err := FetchInstruments(context.Background(), server.Client(), server.URL, category)
			if err != nil {
				t.Fatalf("FetchInstruments: %v", err)
			}
			want := []Instrument{{Symbol: " BTCUSDT ", BaseCoin: " BTC ", QuoteCoin: " USDT "}, {Symbol: "", BaseCoin: "", QuoteCoin: ""}, {Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT"}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("instruments = %#v, want %#v", got, want)
			}
			if calls != 3 {
				t.Errorf("requests = %d, want 3", calls)
			}
		})
	}
}

func TestFetchInstrumentsErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		pages  []string
		want   string
	}{
		{"status", http.StatusServiceUnavailable, []string{"not JSON"}, "bybit instruments: unexpected status 503"},
		{"decode", http.StatusOK, []string{"{"}, "bybit instruments: decode: unexpected EOF"},
		{"retCode", http.StatusOK, []string{`{"retCode":10001,"retMsg":" invalid category "}`}, "bybit instruments: 10001: invalid category"},
		{"emptyRetMsg", http.StatusOK, []string{`{"retCode":10002,"retMsg":"   "}`}, "bybit instruments: 10002: request failed"},
		{"missingRetCode", http.StatusOK, []string{`{"result":{"list":[]}}`}, "bybit instruments: decode: retCode is required"},
		{"missingList", http.StatusOK, []string{`{"retCode":0,"result":{}}`}, "bybit instruments: decode: result.list is required"},
		{"nullList", http.StatusOK, []string{`{"retCode":0,"result":{"list":null}}`}, "bybit instruments: decode: result.list is required"},
		{"secondPageMissingList", http.StatusOK, []string{
			`{"retCode":0,"result":{"list":[{"symbol":"BTCUSDT"}],"nextPageCursor":"next"}}`,
			`{"retCode":0,"result":{}}`,
		}, "bybit instruments: decode: result.list is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls > len(tc.pages) {
					t.Errorf("unexpected request %d", calls)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.pages[calls-1])
			}))
			defer server.Close()
			got, err := FetchInstruments(context.Background(), server.Client(), server.URL, "spot")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if got != nil {
				t.Errorf("instruments on error = %#v, want nil", got)
			}
			if calls != len(tc.pages) {
				t.Errorf("requests = %d, want %d (no retry)", calls, len(tc.pages))
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFetchInstrumentsTransportError(t *testing.T) {
	sentinel := errors.New("transport failed")
	calls := 0
	client := &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, sentinel
		}),
	}
	const baseURL = "https://api.bybit.test"
	got, err := FetchInstruments(context.Background(), client, baseURL, "spot")
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want sentinel %v", err, sentinel)
	}
	wantErr := &url.Error{
		Op:  "Get",
		URL: baseURL + InstrumentsPath + "?category=spot",
		Err: sentinel,
	}
	if err.Error() != wantErr.Error() {
		t.Errorf("error = %q, want %q", err.Error(), wantErr.Error())
	}
	if got != nil {
		t.Errorf("instruments on error = %#v, want nil", got)
	}
	if calls != 1 {
		t.Errorf("round trips = %d, want 1 (no retry)", calls)
	}
}

func TestFetchInstrumentsInvalidHost(t *testing.T) {
	const baseURL = "://invalid"
	//nolint:staticcheck // SA1007: the invalid URL is the case under test.
	_, wantErr := url.Parse(baseURL + InstrumentsPath)
	if wantErr == nil {
		t.Fatal("url.Parse succeeded, want invalid host error")
	}
	_, err := FetchInstruments(context.Background(), &http.Client{}, baseURL, "spot")
	if err == nil || err.Error() != wantErr.Error() {
		t.Fatalf("error = %v, want %q", err, wantErr.Error())
	}
}
