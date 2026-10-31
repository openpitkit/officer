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

package binance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/secret"
)

var testCredentials = Credentials{
	APIKey:    "test-key-marker",
	APISecret: "test-secret-marker",
}

func testClient(t *testing.T, baseURL string, credentials Credentials,
	httpClient *http.Client,
) *Client {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"apiKey": credentials.APIKey, "apiSecret": credentials.APISecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, redact, err := secret.CredentialRedactor(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(baseURL, credentials, redact, httpClient)
	c.now = func() time.Time { return time.UnixMilli(1499827319559) }
	return c
}

func assertNoCredentials(t *testing.T, message string) {
	t.Helper()
	for _, value := range []string{
		testCredentials.APIKey, testCredentials.APISecret,
	} {
		if strings.Contains(message, value) {
			t.Error("error text leaked a credential")
		}
	}
}

func TestSignPublishedVector(t *testing.T) {
	secret := "NhqPtmdSJYdKjVHjA7PZj4Mge3R5YNiP1e3UZjInClVN65XAbvqq" +
		"M6A7H5fATj0j"
	payload := "symbol=LTCBTC&side=BUY&type=LIMIT&timeInForce=GTC&" +
		"quantity=1&price=0.1&recvWindow=5000&timestamp=1499827319559"
	want := "c8db56825ae71d6d79447849e617115f4a920fa2acdcab2b053c4b2838bd6b71"
	if got := sign(payload, secret); got != want {
		t.Fatalf("published signature = %q, want %q", got, want)
	}
}

func TestParseCredentials(t *testing.T) {
	valid := `{"apiKey":"test-key-marker",` +
		`"apiSecret":"test-secret-marker"}`
	for _, raw := range []string{
		valid,
		`{"apiKey":" \t test-key-marker \n ",` +
			`"apiSecret":"\n test-secret-marker \t "}`,
	} {
		got, err := ParseCredentials(raw)
		if err != nil || got != testCredentials {
			t.Fatal("valid credentials were refused or not trimmed")
		}
	}
	for _, tc := range []struct {
		name, raw, pointer string
	}{
		{"empty", "", ""},
		{"null", "null", "/apiKey"},
		{"array", "[]", ""},
		{"empty object", `{}`, "/apiKey"},
		{"missing secret", `{"apiKey":"x"}`, "/apiSecret"},
		{"missing key", `{"apiSecret":"x"}`, "/apiKey"},
		{"blank key", `{"apiKey":" \t ","apiSecret":"x"}`, "/apiKey"},
		{"blank secret", `{"apiKey":"x","apiSecret":" "}`, "/apiSecret"},
		{"numeric secret", `{"apiKey":"x","apiSecret":1}`, ""},
		{"null key", `{"apiKey":null,"apiSecret":"x"}`, "/apiKey"},
		{"wrong case", `{"APIKey":"x","apiSecret":"x"}`, ""},
		{"snake case", `{"api_key":"x","api_secret":"x"}`, ""},
		{"trailing JSON", valid + " {}", ""},
		{"unknown string", valid[:len(valid)-1] +
			`,"unknown-marker":"test-secret-marker"}`, ""},
		{"unknown bool", valid[:len(valid)-1] +
			`,"test-secret-marker":true}`, ""},
		{"broken JSON", valid[:len(valid)-1], ""},
		{"broken padded", `{"apiKey":" test-key-marker ",` +
			`"apiSecret":" test-secret-marker "`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCredentials(tc.raw)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatal("invalid credentials did not return ErrInvalid")
			}
			if got := domain.ValidationPointer(err); got != tc.pointer {
				t.Fatalf("pointer = %q, want %q", got, tc.pointer)
			}
			if !strings.HasPrefix(err.Error(), "binance:") ||
				strings.Contains(err.Error(), "unknown-marker") {
				t.Error("validation error lacks prefix or leaks field name")
			}
			assertNoCredentials(t, err.Error())
		})
	}
}

func TestSigned(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			payload, signature, found := strings.Cut(
				r.URL.RawQuery, "&signature=")
			mac := hmac.New(sha256.New, []byte(testCredentials.APISecret))
			_, _ = mac.Write([]byte(payload))
			if !found || signature != hex.EncodeToString(mac.Sum(nil)) {
				t.Error("request signature does not match the raw query")
			}
			want := "note=a%2Bb+%26%2F&symbol=LTCBTC&" +
				"tag=one&tag=two&timestamp=1499827319559"
			if payload != want {
				t.Errorf("signed payload = %q, want %q", payload, want)
			}
			if r.Header.Get("X-MBX-APIKEY") != testCredentials.APIKey {
				t.Error("API key header is missing or incorrect")
			}
			if r.URL.Path != "/api/v3/order" || r.Method != "POST" {
				t.Error("request method or path is incorrect")
			}
			body, err := io.ReadAll(r.Body)
			if err != nil || len(body) != 0 {
				t.Error("POST body is not empty")
			}
			_, _ = io.WriteString(w, `{"ok":true}`)
		}))
	defer server.Close()
	c := testClient(t, server.URL+"/", testCredentials, server.Client())
	params := url.Values{
		"note": {"a+b &/"}, "symbol": {"LTCBTC"},
		"tag": {"one", "two"}, "timestamp": {"old", "older"},
	}
	wantParams := url.Values{
		"note": {"a+b &/"}, "symbol": {"LTCBTC"},
		"tag": {"one", "two"}, "timestamp": {"old", "older"},
	}
	var result struct{ OK bool }
	status, err := c.Signed(context.Background(), http.MethodPost,
		"/api/v3/order", params, &result)
	if err != nil || status != http.StatusOK || !result.OK {
		t.Fatalf("Signed status = %d, error = %v", status, err)
	}
	if !reflect.DeepEqual(params, wantParams) {
		t.Error("Signed mutated the caller's parameters")
	}
}

func TestSignedRefusesRedirect(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			reached.Store(true)
			_, _ = io.WriteString(w, `{}`)
		}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		}))
	defer server.Close()
	var originalPolicyCalled atomic.Bool
	httpClient := server.Client()
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		originalPolicyCalled.Store(true)
		return nil
	}
	c := testClient(t, server.URL, testCredentials, httpClient)
	status, err := c.Signed(context.Background(), "GET", "/", nil, nil)
	var apiErr *APIError
	if status != http.StatusTemporaryRedirect || !errors.As(err, &apiErr) {
		t.Errorf("redirect status = %d, error = %v", status, err)
	}
	if reached.Load() || originalPolicyCalled.Load() {
		t.Error("redirect target or caller's redirect policy was reached")
	}
	if err := httpClient.CheckRedirect(nil, nil); err != nil ||
		!originalPolicyCalled.Load() {
		t.Error("NewClient changed the caller's redirect policy")
	}
}

func TestSignedMalformedRedirectRedactsQuery(t *testing.T) {
	for _, code := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
	} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Location",
						"https://example.invalid/%zz?"+r.URL.RawQuery)
					w.WriteHeader(code)
				}))
			defer server.Close()
			var reached atomic.Bool
			var query, signature string
			httpClient := server.Client()
			transport := httpClient.Transport
			httpClient.Transport = transportFunc(
				func(r *http.Request) (*http.Response, error) {
					if r.URL.Host == "example.invalid" {
						reached.Store(true)
						return nil, errors.New("redirect target reached")
					}
					query = r.URL.RawQuery
					signature = r.URL.Query().Get("signature")
					return transport.RoundTrip(r)
				})
			c := testClient(t, server.URL, testCredentials, httpClient)
			status, err := c.Signed(context.Background(), "GET", "/",
				url.Values{
					"apiKey":    {testCredentials.APIKey},
					"apiSecret": {testCredentials.APISecret},
				}, nil)
			if status != 0 || err == nil {
				t.Fatal("malformed redirect did not return a transport error")
			}
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				assertNoCredentials(t, cause.Error())
				for _, value := range []string{query, signature} {
					if value == "" || strings.Contains(cause.Error(), value) {
						t.Error("error chain leaked signed query or signature")
					}
				}
			}
			if reached.Load() {
				t.Error("malformed redirect target was reached")
			}
		})
	}
}

func TestSignedAPIErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, message, text string
		code                      int
		decoded                   bool
	}{
		{"refusal", `{"code":-2015,"msg":"denied"}`, "denied",
			"binance: HTTP 401 (code -2015): denied", -2015, true},
		{"missing msg", `{"code":-2015}`, "",
			"binance: HTTP 401", -2015, false},
		{"missing code", `{"msg":"denied"}`, "denied",
			"binance: HTTP 401: denied", 0, false},
		{"zero code", `{"code":0,"msg":"denied"}`, "denied",
			"binance: HTTP 401 (code 0): denied", 0, true},
		{"empty msg", `{"code":-1,"msg":""}`, "",
			"binance: HTTP 401", -1, true},
		{"null msg", `{"code":-1,"msg":null}`, "",
			"binance: HTTP 401", -1, false},
		{"not JSON", "test-key-marker test-secret-marker", "",
			"binance: HTTP 401", 0, false},
		{"wrong type", `{"code":"bad","msg":"test-key-marker"}`, "",
			"binance: HTTP 401", 0, false},
		{"echo", `{"code":-1,"msg":"test-key-marker test-secret-marker"}`,
			"[redacted] [redacted]",
			"binance: HTTP 401 (code -1): [redacted] [redacted]", -1, true},
		{"signed echo", `{"code":-1,"msg":"{query} {signature}"}`,
			"[redacted] [redacted]",
			"binance: HTTP 401 (code -1): [redacted] [redacted]", -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					body := strings.NewReplacer(
						"{query}", r.URL.RawQuery,
						"{signature}", r.URL.Query().Get("signature"),
					).Replace(tc.body)
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(w, body)
				}))
			defer server.Close()
			c := testClient(t, server.URL, testCredentials, server.Client())
			status, err := c.Signed(context.Background(), "GET", "/", nil, nil)
			var apiErr *APIError
			if status != http.StatusUnauthorized || !errors.As(err, &apiErr) {
				t.Fatalf("refusal status = %d, error = %v", status, err)
			}
			if apiErr.Status != status || apiErr.Code != tc.code ||
				apiErr.Message != tc.message || apiErr.BodyDecoded != tc.decoded {
				t.Error("APIError fields do not match the refusal")
			}
			if err.Error() != tc.text {
				t.Error("APIError text does not match its decoded fields")
			}
			assertNoCredentials(t, err.Error())
		})
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

type failingJSON struct{ err error }

func (r *failingJSON) UnmarshalJSON([]byte) error { return r.err }

func TestSignedErrorsRedactCause(t *testing.T) {
	for _, tc := range []struct {
		name, operation string
		status          int
	}{
		{"transport", "request", 0},
		{"nested transport", "request", 0},
		{"decode", "decode response", http.StatusOK},
		{"read refusal", "read error response", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := fmt.Errorf("%s %s: %w", testCredentials.APIKey,
				testCredentials.APISecret, context.Canceled)
			var endpoint, query, signature string
			httpClient := &http.Client{Transport: transportFunc(
				func(r *http.Request) (*http.Response, error) {
					endpoint, query = r.URL.String(), r.URL.RawQuery
					signature = r.URL.Query().Get("signature")
					if tc.status == 0 {
						requestErr := fmt.Errorf("%s %s: %w", query,
							signature, cause)
						if tc.name == "nested transport" {
							return nil, &url.Error{
								Op: "Get", URL: endpoint, Err: requestErr,
							}
						}
						return nil, requestErr
					}
					var body io.Reader = strings.NewReader(`{}`)
					if tc.status != http.StatusOK {
						body = failingReader{cause}
					}
					return &http.Response{
						StatusCode: tc.status, Header: make(http.Header),
						Body: io.NopCloser(body), Request: r,
					}, nil
				})}
			c := testClient(t, "https://example.invalid", testCredentials,
				httpClient)
			status, err := c.Signed(context.Background(), "GET", "/",
				nil, &failingJSON{cause})
			if status != tc.status || err == nil {
				t.Fatal("error lost its HTTP status or was not returned")
			}
			if errors.Is(err, cause) || errors.Is(err, context.Canceled) {
				t.Error("error retained a cause containing credentials")
			}
			if !strings.HasPrefix(err.Error(), "binance: "+tc.operation+":") {
				t.Error("error lacks its operation prefix")
			}
			if tc.status == 0 {
				var urlErr *url.Error
				if errors.As(err, &urlErr) {
					t.Error("transport error retained a url.Error")
				}
			}
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				assertNoCredentials(t, cause.Error())
				for _, value := range []string{
					endpoint, query, signature, "signature=", "timestamp=",
				} {
					if value == "" || strings.Contains(cause.Error(), value) {
						t.Error("error chain leaked signed URL material")
					}
				}
			}
		})
	}
}

func TestSignedUsesSharedRedactor(t *testing.T) {
	credentials := Credentials{APIKey: `key+"<&/marker`,
		APISecret: `key+"<&/marker-secret`}
	var spellings []string
	for _, value := range []string{credentials.APIKey, credentials.APISecret} {
		quoted, _ := json.Marshal(value)
		spellings = append(spellings, value, url.QueryEscape(value),
			url.PathEscape(value), string(quoted[1:len(quoted)-1]))
	}
	echo := strings.Join(spellings, " | ")
	body, _ := json.Marshal(map[string]any{"code": -1, "msg": echo})
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write(body)
		}))
	defer server.Close()
	c := testClient(t, server.URL, credentials, server.Client())
	_, err := c.Signed(context.Background(), "GET", "/", nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatal("expected an APIError")
	}
	for _, spelling := range spellings {
		if strings.Contains(err.Error(), spelling) ||
			strings.Contains(apiErr.Message, spelling) {
			t.Error("shared redactor missed a credential spelling")
		}
	}
	if strings.Contains(apiErr.Message, "-secret") {
		t.Error("overlapping credentials were not redacted longest first")
	}
	c.httpClient.Transport = transportFunc(
		func(*http.Request) (*http.Response, error) {
			return nil, errors.New(echo)
		})
	_, err = c.Signed(context.Background(), "GET", "/", nil, nil)
	if err == nil {
		t.Fatal("transport error was lost")
	}
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		for _, spelling := range spellings {
			if strings.Contains(cause.Error(), spelling) {
				t.Error("transport error chain leaked a credential spelling")
			}
		}
	}
}

func TestSignedContextAndDecodeErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"uid":`)
		}))
	defer server.Close()
	c := testClient(t, server.URL, testCredentials, server.Client())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Signed(ctx, "GET", "/", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("canceled request lost context.Canceled")
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	_, err = c.Signed(ctx, "GET", "/", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired request lost context.DeadlineExceeded")
	}
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Error("expired request lost its net.Error timeout")
	}
	var account Account
	status, err := c.Signed(context.Background(), "GET", "/", nil, &account)
	if status != http.StatusOK || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("JSON decode error lost its status or cause")
	}
	if !strings.HasPrefix(err.Error(), "binance: decode response:") {
		t.Error("decode error lacks its operation prefix")
	}
	assertNoCredentials(t, err.Error())
	_, err = c.Signed(context.Background(), "\n", "/", nil, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "binance: create request:") {
		t.Fatal("invalid request did not return an operation error")
	}
}

func TestSignedRequiresDependencies(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		client        *Client
	}{
		{"http client", "binance: HTTP client is required",
			NewClient(LiveRESTURL, testCredentials, strings.NewReplacer(), nil)},
		{"redactor", "binance: credential redactor is required",
			NewClient(LiveRESTURL, testCredentials, nil, &http.Client{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, err := tc.client.Signed(context.Background(),
				"GET", "/", nil, nil)
			if status != 0 || err == nil || err.Error() != tc.message {
				t.Error("missing dependency did not fail explicitly")
			}
		})
	}
}

func TestAccount(t *testing.T) {
	for _, tc := range []struct {
		name, body, uid string
		status          int
	}{
		{"exact uid", `{"uid":9007199254740993,"accountType":"SPOT",` +
			`"canTrade":true,"balances":[{"free":"ignored"}]}`,
			"9007199254740993", http.StatusOK},
		{"missing uid", `{"accountType":"SPOT","canTrade":true}`, "",
			http.StatusOK},
		{"null uid", `{"uid":null}`, "", http.StatusOK},
		{"refusal", `{"code":-2015,"msg":"denied"}`, "",
			http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" || r.URL.Path != "/api/v3/account" ||
						r.URL.Query().Get("omitZeroBalances") != "true" ||
						r.URL.Query().Get("signature") == "" {
						t.Error("account request is not signed with required params")
					}
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}))
			defer server.Close()
			c := testClient(t, server.URL, testCredentials, server.Client())
			account, err := c.Account(context.Background())
			if tc.status != http.StatusOK {
				var apiErr *APIError
				if !errors.As(err, &apiErr) {
					t.Error("Account lost the APIError")
				}
				return
			}
			if tc.uid == "" {
				if err == nil || err.Error() != "binance: account uid is missing" {
					t.Error("missing uid was not rejected")
				}
				return
			}
			uid := account.UID
			if err != nil || json.Number.String(uid) != tc.uid ||
				account.AccountType != "SPOT" || !account.CanTrade {
				t.Errorf("account = %+v, error = %v", account, err)
			}
		})
	}
}
