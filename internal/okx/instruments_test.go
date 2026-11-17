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

package okx_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"go.openpit.dev/officer/internal/okx"
)

func TestFetchInstruments(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		body    string
		want    []okx.Instrument
		wantErr string
		wantIs  error
	}{
		{name: "catalogue", status: 200, body: `{"code":"0","data":[{"instId":" BTC-USDT ","baseCcy":"BTC","quoteCcy":"USDT","state":"live","minSz":"0.00001","lotSz":"0.000001","tickSz":"0.1"},{"instId":""}]}`,
			want: []okx.Instrument{{InstID: " BTC-USDT ", BaseCcy: "BTC", QuoteCcy: "USDT", State: "live", MinSz: "0.00001", LotSz: "0.000001", TickSz: "0.1"}, {}}},
		{name: "non_200", status: 429, body: `{`, wantErr: "okx instruments: unexpected status 429"},
		{name: "decode", status: 200, body: `{`, wantErr: "okx instruments: decode: unexpected EOF", wantIs: io.ErrUnexpectedEOF},
		{name: "code_with_msg", status: 200, body: `{"code":"50011","msg":"  rate limit  "}`, wantErr: "okx instruments: 50011: rate limit"},
		{name: "code_without_msg", status: 200, body: `{"code":"50011"}`, wantErr: "okx instruments: 50011: request failed"},
		{name: "code_blank_msg", status: 200, body: `{"code":"50011","msg":"  "}`, wantErr: "okx instruments: 50011: request failed"},
		{name: "absent_code", status: 200, body: `{"data":[{"instId":"ETH-USDT"}]}`, want: []okx.Instrument{{InstID: "ETH-USDT"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.RequestURI() != "/prefix/api/v5/public/instruments?instType=SPOT" {
					t.Errorf("request = %s %s; want GET /prefix/api/v5/public/instruments?instType=SPOT", r.Method, r.URL.RequestURI())
				}
				if r.Header.Get("X-Test-Client") != "caller" {
					t.Error("request did not use caller's transport")
				}
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, test.body)
			}))
			t.Cleanup(server.Close)
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				r.Header.Set("X-Test-Client", "caller")
				return server.Client().Transport.RoundTrip(r)
			})}
			got, err := okx.FetchInstruments(context.Background(), client, server.URL+"/prefix")
			if test.wantErr != "" {
				if err == nil || err.Error() != test.wantErr {
					t.Fatalf("error = %v; want %q", err, test.wantErr)
				}
			} else if err != nil {
				t.Fatalf("FetchInstruments: %v", err)
			}
			if test.wantIs != nil && !errors.Is(err, test.wantIs) {
				t.Fatalf("error = %v; want wrapped %v", err, test.wantIs)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("instruments = %#v; want %#v", got, test.want)
			}
			if calls != 1 {
				t.Fatalf("requests = %d; want 1", calls)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
