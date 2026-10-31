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

package binance_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/internal/binance"
)

func TestFetchSymbol(t *testing.T) {
	const symbol = "ETH/USDT &+"
	for _, tc := range []struct {
		name, body string
		status     int
		found, bad bool
	}{
		{"found", `{"symbols":[{"symbol":"ETH/USDT &+",` +
			`"baseAsset":"ETH","quoteAsset":"USDT"}]}`, 200, true, false},
		{"other success", `{"symbols":[{"symbol":"ETH/USDT &+"}]}`,
			201, true, false},
		{"absent", `{"code":-1121,"msg":"Invalid symbol"}`, 400, false, false},
		{"other 400", `{"code":-1003,"msg":"Too many requests"}`,
			400, false, true},
		{"invalid at 500", `{"code":-1121,"msg":"Invalid symbol"}`,
			500, false, true},
		{"undecoded 400", `{`, 400, false, true},
		{"malformed", `{`, 200, false, true},
		{"empty", `{"symbols":[]}`, 200, false, true},
		{"wrong symbol", `{"symbols":[{"symbol":"OTHER"}]}`,
			200, false, true},
		{"multiple", `{"symbols":[{"symbol":"ETH/USDT &+"},` +
			`{"symbol":"ETH/USDT &+"}]}`, 200, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" || r.URL.Path != binance.ExchangeInfoPath ||
						r.URL.Query().Get("symbol") != symbol ||
						len(r.URL.Query()) != 1 {
						t.Errorf("wrong symbol request: %s", r.URL.RequestURI())
					}
					w.WriteHeader(tc.status)
					_, _ = fmt.Fprint(w, tc.body)
				}))
			defer server.Close()
			got, found, err := binance.FetchSymbol(context.Background(),
				server.Client(), server.URL, symbol)
			if (err != nil) != tc.bad || found != tc.found {
				t.Fatalf("found=%t symbol=%+v: %v", found, got, err)
			}
			if found && got.Symbol != symbol {
				t.Fatalf("wrong symbol: %+v", got)
			}
		})
	}
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, found, err := binance.FetchSymbol(ctx, &http.Client{},
			"http://127.0.0.1:1", symbol)
		if err == nil || found || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled request: found=%t err=%v", found, err)
		}
	})
}

func TestFetchExchangeInfo(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		body     string
		want     binance.ExchangeInfo
		wantErr  string
		checkErr func(*testing.T, error)
	}{
		{
			name:   "catalogue",
			status: http.StatusOK,
			body: `{
				"timezone":"UTC", "serverTime":1499827319559,
				"symbols":[
					{
						"symbol":"BTCUSDT", "status":"TRADING",
						"baseAsset":"BTC", "quoteAsset":"USDT",
						"isSpotTradingAllowed":true,
						"orderTypes":["LIMIT","MARKET","LIMIT_MAKER"],
						"filters":[
							{"filterType":"PRICE_FILTER","tickSize":"0.01000000"},
							{"filterType":"LOT_SIZE",` +
				`"minQty":"0.00001000","stepSize":"0.00001000"}
						]
					},
					{
						"symbol":"ETHUSDT", "status":"BREAK",
						"baseAsset":"ETH", "quoteAsset":"USDT",
						"isSpotTradingAllowed":false,
						"orderTypes":["LIMIT","MARKET"],
						"filters":[` +
				`{"filterType":"NOTIONAL","minNotional":"5.00000000",` +
				`"applyMinToMarket":true}]
					}
				]
			}`,
			want: binance.ExchangeInfo{Symbols: []binance.Symbol{
				{
					Symbol: "BTCUSDT", Status: "TRADING",
					BaseAsset: "BTC", QuoteAsset: "USDT",
					IsSpotTradingAllowed: true,
					OrderTypes: []string{
						"LIMIT", "MARKET", "LIMIT_MAKER",
					},
					Filters: []json.RawMessage{
						json.RawMessage(
							`{"filterType":"PRICE_FILTER",` +
								`"tickSize":"0.01000000"}`,
						),
						json.RawMessage(
							`{"filterType":"LOT_SIZE","minQty":"0.00001000",` +
								`"stepSize":"0.00001000"}`,
						),
					},
				},
				{
					Symbol: "ETHUSDT", Status: "BREAK",
					BaseAsset: "ETH", QuoteAsset: "USDT",
					IsSpotTradingAllowed: false,
					OrderTypes:           []string{"LIMIT", "MARKET"},
					Filters: []json.RawMessage{
						json.RawMessage(
							`{"filterType":"NOTIONAL","minNotional":"5.00000000",` +
								`"applyMinToMarket":true}`,
						),
					},
				},
			}},
		},
		{
			name:    "non_200",
			status:  http.StatusTooManyRequests,
			body:    `{"code":-1003,"msg":"Too many requests"}`,
			wantErr: "binance exchangeInfo: unexpected status 429",
		},
		{
			name:    "malformed_json",
			status:  http.StatusOK,
			body:    `{`,
			wantErr: "binance exchangeInfo: decode: ",
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				if !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("error = %v; want io.ErrUnexpectedEOF", err)
				}
			},
		},
		{
			name:   "spot_allowed_type_change",
			status: http.StatusOK,
			body: `{"symbols":[{
				"symbol":"ETHUSDT","isSpotTradingAllowed":"true"
			}]}`,
			wantErr: "binance exchangeInfo: decode: ",
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				var typeErr *json.UnmarshalTypeError
				if !errors.As(err, &typeErr) {
					t.Fatalf("error = %v; want *json.UnmarshalTypeError", err)
				}
				if typeErr.Value != "string" || typeErr.Type == nil ||
					typeErr.Type.Kind() != reflect.Bool ||
					!strings.HasSuffix(typeErr.Field, "isSpotTradingAllowed") {
					t.Fatalf("UnmarshalTypeError = %#v; want string, bool, "+
						"field ending in isSpotTradingAllowed", typeErr)
				}
			},
		},
		{
			name:   "absent_trading_fields",
			status: http.StatusOK,
			body:   `{"symbols":[{"symbol":"ETHUSDT"}]}`,
			want: binance.ExchangeInfo{Symbols: []binance.Symbol{{
				Symbol: "ETHUSDT",
			}}},
		},
		{
			name:   "null_trading_fields",
			status: http.StatusOK,
			body: `{"symbols":[{"symbol":"ETHUSDT","status":null,
				"isSpotTradingAllowed":null,"orderTypes":null}]}`,
			want: binance.ExchangeInfo{Symbols: []binance.Symbol{{
				Symbol: "ETHUSDT",
			}}},
		},
		{
			name:   "unusual_filter",
			status: http.StatusOK,
			body: `{"symbols":[{"symbol":"DOGEUSDT","filters":[
				42,{"filterType":"NEW_FILTER","minQty":{"units":"lots"}}
			]}]}`,
			want: binance.ExchangeInfo{Symbols: []binance.Symbol{{
				Symbol: "DOGEUSDT",
				Filters: []json.RawMessage{
					json.RawMessage(`42`),
					json.RawMessage(
						`{"filterType":"NEW_FILTER",` +
							`"minQty":{"units":"lots"}}`,
					),
				},
			}}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(
				w http.ResponseWriter, r *http.Request,
			) {
				if r.Method != http.MethodGet ||
					r.URL.RequestURI() != "/api/v3/exchangeInfo" {
					t.Errorf(
						"request = %s %s; want GET /api/v3/exchangeInfo",
						r.Method, r.URL.RequestURI(),
					)
				}
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, test.body)
			}))
			t.Cleanup(server.Close)

			got, err := binance.FetchExchangeInfo(
				context.Background(), server.Client(), server.URL,
			)
			if test.wantErr != "" {
				if err == nil {
					t.Fatalf("FetchExchangeInfo error = nil; want %q", test.wantErr)
				}
				if test.checkErr != nil {
					if !strings.HasPrefix(err.Error(), test.wantErr) {
						t.Fatalf("error = %v; want prefix %q", err, test.wantErr)
					}
					test.checkErr(t, err)
				} else if err.Error() != test.wantErr {
					t.Fatalf("error = %v; want %q", err, test.wantErr)
				}
			} else if err != nil {
				t.Fatalf("FetchExchangeInfo error = %v; want nil", err)
			}
			if len(got.Symbols) != len(test.want.Symbols) {
				t.Fatalf(
					"symbol count = %d; want %d",
					len(got.Symbols), len(test.want.Symbols),
				)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("FetchExchangeInfo = %#v; want %#v", got, test.want)
			}
		})
	}
}
