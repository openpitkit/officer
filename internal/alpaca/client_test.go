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

package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testCredentials = Credentials{APIKey: "test-key-marker", APISecret: "test-secret-marker"}

func assertNoCredentials(t *testing.T, message string) {
	t.Helper()
	if strings.Contains(message, testCredentials.APIKey) || strings.Contains(message, testCredentials.APISecret) {
		t.Fatal("credentials leaked into an error")
	}
}

func TestParseCredentials(t *testing.T) {
	valid := `{"apiKey":"test-key-marker","apiSecret":"test-secret-marker"}`
	got, err := ParseCredentials(valid)
	if err != nil || got != testCredentials {
		t.Fatal("valid credentials refused")
	}
	for _, raw := range []string{
		"", "null", "[]", `{}`, `{"apiKey":"x"}`,
		`{"apiSecret":"x"}`, `{"apiKey":"","apiSecret":"x"}`,
		`{"apiKey":"x","apiSecret":" "}`,
		`{"apiKey":"x","apiSecret":1}`,
		`{"APIKey":"x","apiSecret":"x"}`,
		`{"api_key":"x","api_secret":"x"}`,
		valid + " {}", `{"apiKey":"x","apiSecret":"x","test-secret-marker":true}`,
		`{"apiKey":"test-key-marker","apiSecret":"test-secret-marker"`,
	} {
		_, err := ParseCredentials(raw)
		if err == nil {
			t.Fatal("invalid credentials accepted")
		}
		assertNoCredentials(t, err.Error())
	}
}

func TestAsset(t *testing.T) {
	for _, status := range []int{200, 404, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("APCA-API-KEY-ID") != testCredentials.APIKey || r.Header.Get("APCA-API-SECRET-KEY") != testCredentials.APISecret {
					t.Error("auth headers missing")
				}
				if r.Method != http.MethodGet || r.URL.EscapedPath() != "/v2/assets/BTC%2FUSD" {
					t.Error("crypto asset path is not escaped")
				}
				w.WriteHeader(status)
				if status == 200 {
					_, _ = w.Write([]byte(`{"id":"asset","class":"crypto","exchange":"CRYPTO","symbol":"BTC/USD","name":"Bitcoin","status":"active","tradable":true,"fractionable":true,"min_order_size":"0.000000001","min_trade_increment":"0.000000001","price_increment":"0.01"}`))
				} else {
					_, _ = w.Write([]byte(`{"code":40410000,"message":"venue refused"}`))
				}
			}))
			defer server.Close()
			client := NewClient(server.URL, testCredentials, server.Client())
			asset, found, err := client.Asset(context.Background(), "BTC/USD")
			switch status {
			case 200:
				if err != nil || !found || asset.Symbol != "BTC/USD" || asset.MinOrderSize != "0.000000001" || asset.PriceIncrement != "0.01" || !asset.Tradable {
					t.Fatal("asset fields not preserved")
				}
			case 404:
				if err != nil || found {
					t.Fatal("404 must be not found")
				}
			case 500:
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Status != 500 || apiErr.Code != 40410000 {
					t.Fatal("5xx not surfaced as APIError")
				}
			}
		})
	}
}

type errorTransport struct{ err error }

func (rt errorTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, rt.err }

func TestRequestErrors(t *testing.T) {
	for _, body := range []string{"not JSON", `{"message":"missing code"}`, `{"code":"bad","message":"bad"}`, "null"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503); _, _ = w.Write([]byte(body)) }))
		_, err := NewClient(server.URL, testCredentials, server.Client()).Request(context.Background(), "GET", "/v2/assets/AAPL", nil, nil)
		server.Close()
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != 503 || apiErr.Code != 0 || apiErr.Message != "" {
			t.Fatal("unexpected body must yield status alone")
		}
	}
	cause := errors.New("network failed")
	client := NewClient("http://example.invalid", testCredentials, &http.Client{Transport: errorTransport{cause}})
	_, err := client.Request(context.Background(), "GET", "/v2/assets/AAPL", nil, nil)
	var apiErr *APIError
	if err == nil || !errors.Is(err, cause) || errors.As(err, &apiErr) {
		t.Fatal("transport failure must be wrapped, not APIError")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	_, err = NewClient(server.URL, testCredentials, server.Client()).Request(ctx, "GET", "/v2/assets/AAPL", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &apiErr) {
		t.Fatal("timeout must remain a transport error")
	}
}

func TestInvariantCredentialsNeverLeak(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": testCredentials.APISecret + " " + testCredentials.APIKey})
	}))
	defer server.Close()
	_, _, err := NewClient(server.URL, testCredentials, server.Client()).Asset(context.Background(), "AAPL")
	if err == nil {
		t.Fatal("expected refusal")
	}
	assertNoCredentials(t, err.Error())
	cause := errors.New(testCredentials.APISecret + " " + testCredentials.APIKey)
	client := NewClient("http://example.invalid", testCredentials, &http.Client{Transport: errorTransport{cause}})
	_, err = client.Request(context.Background(), "GET", "/v2/assets/AAPL", nil, nil)
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause lost")
	}
	assertNoCredentials(t, err.Error())
}

func TestTimeoutReadingErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.(http.Flusher).Flush()
		time.Sleep(50 * time.Millisecond)
	}))
	defer server.Close()
	client := NewClient(server.URL, testCredentials, &http.Client{Timeout: 20 * time.Millisecond})
	_, err := client.Request(context.Background(), "GET", "/v2/assets/AAPL", nil, nil)
	var apiErr *APIError
	if err == nil || errors.As(err, &apiErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("body timeout must not become APIError")
	}
}

func TestRequestRefusesRedirect(t *testing.T) {
	var reached bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer server.Close()
	_, err := NewClient(server.URL, testCredentials, server.Client()).Request(context.Background(), "GET", "/v2/assets/AAPL", nil, nil)
	var apiErr *APIError
	if reached || !errors.As(err, &apiErr) || apiErr.Status != 302 {
		t.Fatal("REST credentials followed a redirect")
	}
}

func TestInvariantSharedClientHasNoTradingDependency(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
			continue
		}
		source, err := parser.ParseFile(token.NewFileSet(), file.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range source.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(path, "go.openpit.dev/officer/framework/trading") || strings.HasPrefix(path, "go.openpit.dev/officer/internal/trading") {
				t.Fatal("shared client imports trading")
			}
		}
	}
}
