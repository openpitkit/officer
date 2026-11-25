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

package okx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestDemoHTTPClient(t *testing.T) {
	for _, demo := range []bool{false, true} {
		t.Run(strconv.FormatBool(demo), func(t *testing.T) {
			var public, signed atomic.Bool
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if demo {
						if r.Header.Get("x-simulated-trading") != "1" {
							t.Error("demo header missing")
						}
					} else if len(r.Header.Values("x-simulated-trading")) != 0 {
						t.Error("demo header on live request")
					}
					if r.URL.Path == "/public" {
						public.Store(true)
					} else {
						signed.Store(true)
					}
					_, _ = io.WriteString(w, `{"code":"0","data":[]}`)
				}))
			defer server.Close()
			original := server.Client()
			original.Timeout = time.Hour
			client := original
			if demo {
				client = DemoHTTPClient(original)
				if client == original || client.Timeout != original.Timeout ||
					client.CheckRedirect == nil {
					t.Fatal("demo wrapper must copy the client and refuse redirects")
				}
			}
			req, err := http.NewRequestWithContext(context.Background(),
				http.MethodGet, server.URL+"/public", nil)
			if err != nil {
				t.Fatal("create public request")
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal("public request failed")
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal("close response")
			}
			if req.Header.Get("x-simulated-trading") != "" {
				t.Fatal("wrapper mutated the original request")
			}
			if _, err := testClient(t, server.URL, client).Signed(
				context.Background(), http.MethodGet, "/signed", nil, nil,
			); err != nil {
				t.Fatal("signed request failed")
			}
			if !public.Load() || !signed.Load() {
				t.Fatal("both public and signed requests must be checked")
			}
		})
	}
}
