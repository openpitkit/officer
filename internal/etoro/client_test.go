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

package etoro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/uuid"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/secret"
)

var testCredentials = Credentials{APIKey: `api-"<& +/?-marker`, UserKey: `user-"<& +/?-marker`}

func TestCloseOrderInfoPath(t *testing.T) {
	for _, tc := range []struct {
		demo bool
		want string
	}{
		{true, "/api/v1/trading/info/demo/close-orders/9007199254740993"},
		{false, "/api/v1/trading/info/real/close-orders/9007199254740993"},
	} {
		if got := CloseOrderInfoPath(tc.demo, "9007199254740993"); got != tc.want {
			t.Fatalf("close-order info path = %q, want %q", got, tc.want)
		}
	}
}

func credentialJSON(t *testing.T, credentials Credentials) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"apiKey": credentials.APIKey, "userKey": credentials.UserKey})
	if err != nil {
		t.Fatal("encode test credentials failed")
	}
	return string(raw)
}

func credentialRedactor(t *testing.T) *strings.Replacer {
	t.Helper()
	_, replacer, err := secret.CredentialRedactor(credentialJSON(t, testCredentials))
	if err != nil {
		t.Fatal("create credential redactor failed")
	}
	return replacer
}

func credentialSpellings(t *testing.T) map[string]string {
	t.Helper()
	spellings := make(map[string]string)
	for name, value := range map[string]string{"api": testCredentials.APIKey, "user": testCredentials.UserKey} {
		spellings[name+"-raw"] = value
		spellings[name+"-query"] = url.QueryEscape(value)
		spellings[name+"-path"] = url.PathEscape(value)
		for nameHTML, escapeHTML := range map[string]bool{"json-html": true, "json": false} {
			var encoded bytes.Buffer
			encoder := json.NewEncoder(&encoded)
			encoder.SetEscapeHTML(escapeHTML)
			if err := encoder.Encode(value); err != nil {
				t.Fatal("encode test credential spelling failed")
			}
			spellings[name+"-"+nameHTML] = string(encoded.Bytes()[1 : encoded.Len()-2])
		}
	}
	return spellings
}

func assertNoCredentials(t *testing.T, text string) {
	t.Helper()
	for name, spelling := range credentialSpellings(t) {
		if strings.Contains(text, spelling) {
			t.Fatalf("credential leaked in %s spelling", name)
		}
	}
}

func TestParseCredentials(t *testing.T) {
	for name, credentials := range map[string]Credentials{
		"valid":   testCredentials,
		"trimmed": {APIKey: " \t" + testCredentials.APIKey + "\n ", UserKey: "\n " + testCredentials.UserKey + "\t "},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseCredentials(credentialJSON(t, credentials))
			if err != nil || got != testCredentials {
				t.Fatal("valid credentials were refused or not trimmed")
			}
		})
	}
	for _, tc := range []struct {
		name, raw, pointer, constraint string
	}{
		{"bad-json", credentialJSON(t, testCredentials)[:20], "", "type"},
		{"trailing-json", credentialJSON(t, testCredentials) + " {}", "", "type"},
		{"null", "null", "/apiKey", "required"},
		{"array", "[]", "", "type"},
		{"scalar", `"secret-input-marker"`, "", "type"},
		{"wrong-type", `{"apiKey":42,"userKey":"secret-input-marker"}`, "", "type"},
		{"unknown-field", strings.TrimSuffix(credentialJSON(t, testCredentials), "}") + `,"unknown-secret-field":"secret-input-marker"}`, "", "unknown_field"},
		{"blank-api", credentialJSON(t, Credentials{APIKey: " \t ", UserKey: testCredentials.UserKey}), "/apiKey", "required"},
		{"blank-user", credentialJSON(t, Credentials{APIKey: testCredentials.APIKey, UserKey: " \n "}), "/userKey", "required"},
		{"missing-api", `{"userKey":"secret-input-marker"}`, "/apiKey", "required"},
		{"missing-user", `{"apiKey":"secret-input-marker"}`, "/userKey", "required"},
		{"empty-object", `{}`, "/apiKey", "required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCredentials(tc.raw)
			if !errors.Is(err, domain.ErrInvalid) || got != (Credentials{}) {
				t.Fatal("invalid credentials must return empty credentials and ErrInvalid")
			}
			if domain.ValidationPointer(err) != tc.pointer || domain.ValidationConstraint(err) != tc.constraint {
				t.Fatal("wrong credential validation pointer or constraint")
			}
			assertNoCredentials(t, err.Error())
			if strings.Contains(err.Error(), "unknown-secret-field") || strings.Contains(err.Error(), "secret-input-marker") {
				t.Fatal("credential validation exposed an input value or unknown field name")
			}
		})
	}
}

func TestRequestHeadersAndJSON(t *testing.T) {
	const orderID = "ED72693C-1545-4FA1-8A10-ACA7CF5419A6"
	ids := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != testCredentials.APIKey {
			t.Error("x-api-key header missing or changed")
		}
		if r.Header.Get("x-user-key") != testCredentials.UserKey {
			t.Error("x-user-key header missing or changed")
		}
		id := r.Header.Get("x-request-id")
		ids <- id
		if r.Method == http.MethodPost {
			if r.URL.RequestURI() != "/api/v3/trading/execution/demo/orders?mode=test" || id != orderID {
				t.Error("order path or caller request id changed")
			}
			payload, _ := io.ReadAll(r.Body)
			if string(payload) != `{"amount":10}` || r.Header.Get("Content-Type") != "application/json" {
				t.Error("request JSON or content type changed")
			}
		} else if r.URL.RequestURI() != "/api/v1/trading/info/demo/portfolio?detail=all" {
			t.Error("host-relative path or query changed")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"referenceId":"accepted"}`))
	}))
	defer server.Close()
	client := NewClient(server.URL+"/", testCredentials, server.Client(), credentialRedactor(t))
	var result struct {
		ReferenceID string `json:"referenceId"`
	}
	status, err := client.RequestOrder(context.Background(), http.MethodPost, TradingPath(true, "/api/v3/trading/execution", "/orders?mode=test"), orderID, map[string]int{"amount": 10}, &result)
	if err != nil || status != http.StatusCreated || result.ReferenceID != "accepted" {
		t.Fatal("successful JSON response or status lost")
	}
	if <-ids != orderID {
		t.Fatal("caller request id was overwritten")
	}
	for range 2 {
		status, err = client.Request(context.Background(), http.MethodGet, "/api/v1/trading/info/demo/portfolio?detail=all", nil, nil)
		if err != nil || status != http.StatusCreated {
			t.Fatal("ordinary request failed")
		}
	}
	first, second := <-ids, <-ids
	for _, id := range []string{first, second} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.Version() != 4 || parsed == uuid.Nil {
			t.Fatal("ordinary request id is not a random UUID")
		}
	}
	if first == second {
		t.Fatal("ordinary requests reused a request id")
	}
}

func TestRequestRefusesRedirect(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var hits atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Header.Get("x-api-key") != "" || r.Header.Get("x-user-key") != "" {
					t.Error("auth headers reached redirect target")
				}
			}))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, status)
			}))
			defer source.Close()
			supplied := source.Client()
			var originalPolicyCalls atomic.Int32
			supplied.CheckRedirect = func(*http.Request, []*http.Request) error {
				originalPolicyCalls.Add(1)
				return nil
			}
			client := NewClient(source.URL, testCredentials, supplied, credentialRedactor(t))
			gotStatus, err := client.Request(context.Background(), http.MethodGet, "/redirect", nil, nil)
			var apiErr *APIError
			if gotStatus != status || !errors.As(err, &apiErr) || apiErr.Status != status || hits.Load() != 0 || originalPolicyCalls.Load() != 0 {
				t.Fatal("REST redirect was followed or redirect status lost")
			}
			_ = supplied.CheckRedirect(nil, nil)
			if originalPolicyCalls.Load() != 1 {
				t.Fatal("constructor modified the supplied HTTP client")
			}
		})
	}
}

func TestAPIError(t *testing.T) {
	spellings := credentialSpellings(t)
	var echoed []string
	for _, spelling := range spellings {
		echoed = append(echoed, spelling)
	}
	text := strings.Join(echoed, " | ")
	quoted, _ := json.Marshal(text)
	for _, tc := range []struct {
		name, body, code, message string
		status                    int
	}{
		{"problem", `{"type":` + string(quoted) + `,"title":"refused","status":400,"detail":` + string(quoted) + `,"instance":"ignored"}`, text, text, 400},
		{"problem-title", `{"type":"about:blank","title":` + string(quoted) + `,"status":403}`, "about:blank", text, 403},
		{"gateway", `{"errorCode":` + string(quoted) + `,"errorMessage":` + string(quoted) + `}`, text, text, 429},
		{"standard", `{"success":false,"error":{"code":` + string(quoted) + `,"message":` + string(quoted) + `,"field":"ignored"},"timestamp":"2026-10-27T00:00:00Z","requestId":"ignored"}`, text, text, 404},
		{"unknown", `{"reason":` + string(quoted) + `}`, "", "", 500},
		{"invalid-json", string(quoted) + `{`, "", "", 502},
		{"null", `null`, "", "", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewClient(server.URL, testCredentials, server.Client(), credentialRedactor(t))
			status, err := client.Request(context.Background(), http.MethodGet, "/refused", nil, nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || status != tc.status || apiErr.Status != tc.status {
				t.Fatal("HTTP refusal must preserve status in APIError")
			}
			assertNoCredentials(t, apiErr.Code)
			assertNoCredentials(t, apiErr.Message)
			assertNoCredentials(t, err.Error())
			redacted := strings.Repeat("[redacted] | ", len(spellings)-1) + "[redacted]"
			wantCode, wantMessage := tc.code, tc.message
			if tc.code == text {
				wantCode = redacted
			}
			if tc.message == text {
				wantMessage = redacted
			}
			if apiErr.Code != wantCode || apiErr.Message != wantMessage {
				t.Fatal("API error code or message not decoded from documented shape")
			}
			wantError := "etoro: HTTP " + strconv.Itoa(tc.status)
			if wantCode != "" {
				wantError += " (code " + wantCode + ")"
			}
			if wantMessage != "" {
				wantError += ": " + wantMessage
			}
			if err.Error() != wantError {
				t.Fatal("APIError text lost status, code or message")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failingJSON string

func (f failingJSON) MarshalJSON() ([]byte, error) { return nil, errors.New(string(f)) }
func (f *failingJSON) UnmarshalJSON([]byte) error  { return errors.New(string(*f)) }

type failingBody string

func (f failingBody) Read([]byte) (int, error) { return 0, errors.New(string(f)) }
func (f failingBody) Close() error             { return nil }

func TestRequestTimeout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"transport", 0},
		{"error response body", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			baseURL := "https://example.test"
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Body:       io.NopCloser(iotest.ErrReader(context.DeadlineExceeded)),
				}, nil
			})}
			if tc.status == 0 {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					<-r.Context().Done()
				}))
				defer server.Close()
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
				baseURL = server.URL
				httpClient = server.Client()
			}
			client := NewClient(baseURL, testCredentials, httpClient, credentialRedactor(t))
			status, err := client.Request(ctx, http.MethodGet, "/request", nil, nil)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("timeout must preserve context.DeadlineExceeded")
			}
			var apiErr *APIError
			if errors.As(err, &apiErr) {
				t.Fatal("timeout must not become APIError")
			}
			if status != tc.status {
				t.Fatalf("timeout HTTP status = %d, want %d", status, tc.status)
			}
			assertNoCredentials(t, err.Error())
		})
	}
}

func TestRequestErrorsRedacted(t *testing.T) {
	for name, spelling := range credentialSpellings(t) {
		t.Run(name, func(t *testing.T) {
			for _, operation := range []string{"transport", "encode request", "create request", "read error response", "decode response"} {
				t.Run(operation, func(t *testing.T) {
					path := "/request"
					var body, result any
					httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						if operation == "transport" {
							return nil, errors.New(spelling)
						}
						if operation == "read error response" {
							return &http.Response{StatusCode: 400, Body: failingBody(spelling), Header: make(http.Header)}, nil
						}
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
					})}
					if operation == "encode request" {
						body = failingJSON(spelling)
					}
					if operation == "create request" {
						path = "/" + spelling + "\x00"
					}
					if operation == "decode response" {
						value := failingJSON(spelling)
						result = &value
					}
					client := NewClient("https://bound.example", testCredentials, httpClient, credentialRedactor(t))
					status, err := client.Request(context.Background(), http.MethodGet, path, body, result)
					if err == nil {
						t.Fatal("request failure was swallowed")
					}
					assertNoCredentials(t, err.Error())
					if strings.Contains(err.Error(), spelling) || operation != "create request" && !strings.Contains(err.Error(), "[redacted]") {
						t.Fatal("request error did not redact the credential spelling")
					}
					if operation == "create request" && err.Error() != "etoro: create request failed" {
						t.Fatal("request construction exposed parser text")
					}
					if operation == "read error response" && status != 400 || operation == "decode response" && status != 200 {
						t.Fatal("response failure lost HTTP status")
					}
				})
			}
		})
	}
}

func TestRequestRequiredInputs(t *testing.T) {
	for _, name := range []string{"http-client", "redactor", "api-key", "user-key", "order-id", "absolute-path", "network-path"} {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				hits.Add(1)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
			})}
			credentials, redactor := testCredentials, credentialRedactor(t)
			path, id, want := "/orders", "caller-id", ""
			switch name {
			case "http-client":
				httpClient, want = nil, "HTTP client is required"
			case "redactor":
				redactor, want = nil, "credential redactor is required"
			case "api-key":
				credentials.APIKey, want = " \t", "apiKey is required"
			case "user-key":
				credentials.UserKey, want = " \n", "userKey is required"
			case "order-id":
				id, want = " \t", "request id is required"
			case "absolute-path":
				path, want = "https://other.example/orders", "host-relative path is required"
			case "network-path":
				path, want = "//other.example/orders", "host-relative path is required"
			}
			client := NewClient("https://bound.example", credentials, httpClient, redactor)
			status, err := client.RequestOrder(context.Background(), http.MethodPost, path, id, nil, nil)
			if err == nil || status != 0 || !strings.Contains(err.Error(), want) || hits.Load() != 0 {
				t.Fatal("missing or invalid input did not fail explicitly before transport")
			}
			assertNoCredentials(t, err.Error())
		})
	}
}

func TestTradingPath(t *testing.T) {
	for _, tc := range []struct{ prefix, resource, real, demo string }{
		{"/api/v3/trading/execution", "/orders", "/api/v3/trading/execution/orders", "/api/v3/trading/execution/demo/orders"},
		{"/api/v2/trading/info", "/orders:lookup?referenceId=x", "/api/v2/trading/info/orders:lookup?referenceId=x", "/api/v2/trading/info/demo/orders:lookup?referenceId=x"},
		{"/api/v1/trading/execution", "/market-close-orders/positions/123", "/api/v1/trading/execution/market-close-orders/positions/123", "/api/v1/trading/execution/demo/market-close-orders/positions/123"},
		{"/api/v1/trading/info", "/portfolio", "/api/v1/trading/info/portfolio", "/api/v1/trading/info/demo/portfolio"},
	} {
		if TradingPath(false, tc.prefix, tc.resource) != tc.real {
			t.Fatal("real trading path changed")
		}
		if TradingPath(true, tc.prefix, tc.resource) != tc.demo {
			t.Fatal("demo trading segment missing or misplaced")
		}
	}
}
