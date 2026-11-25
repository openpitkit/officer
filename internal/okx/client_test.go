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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/secret"
)

const credentialsJSON = `{"apiKey":"test-key-marker","apiSecret":"test-secret-marker",` +
	`"passphrase":"test-passphrase-marker","site":"www.okx.com"}`

var fixedTime = time.Date(2026, 11, 12, 14, 5, 6, 123456789, time.FixedZone("test", 7200))

func testClient(t *testing.T, baseURL string, httpClient *http.Client) *Client {
	t.Helper()
	credentials, err := ParseCredentials(credentialsJSON)
	if err != nil {
		t.Fatal("credential fixture was rejected")
	}
	_, redact, err := secret.CredentialRedactor(credentialsJSON)
	if err != nil {
		t.Fatal("redactor fixture was rejected")
	}
	client := NewClient(baseURL, credentials, redact, httpClient)
	client.now = func() time.Time { return fixedTime }
	return client
}

// This implementation is independent of the production signing helper.
func expectedSignature(payload string) string {
	mac := hmac.New(sha256.New, []byte("test-secret-marker"))
	_, _ = mac.Write([]byte(payload))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func assertNoCredentials(t *testing.T, err error, signature string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		for _, value := range []string{
			"test-key-marker", "test-secret-marker", "test-passphrase-marker",
			GlobalSite, signature} {
			if value != "" && strings.Contains(current.Error(), value) {
				t.Fatal("error chain exposed authentication material")
			}
		}
	}
}

func TestEndpoints(t *testing.T) {
	for _, tc := range []struct{ site, rest, live, demo string }{
		{"www.okx.com", "https://openapi.okx.com",
			"wss://ws.okx.com/ws/v5/private", "wss://wspap.okx.com/ws/v5/private"},
		{"my.okx.com", "https://eea.okx.com",
			"wss://wseea.okx.com/ws/v5/private", "wss://wseeapap.okx.com/ws/v5/private"},
		{"app.okx.com", "https://us.okx.com",
			"wss://wsus.okx.com/ws/v5/private", "wss://wsuspap.okx.com/ws/v5/private"},
	} {
		for _, demo := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", tc.site, demo), func(t *testing.T) {
				rest, private, err := Endpoints(tc.site, demo)
				expected := tc.live
				if demo {
					expected = tc.demo
				}
				if err != nil || rest != tc.rest || private != expected {
					t.Fatal("wrong site endpoints")
				}
			})
		}
	}
	for _, site := range []string{"", "unknown-site-marker"} {
		rest, private, err := Endpoints(site, false)
		if !errors.Is(err, domain.ErrInvalid) || domain.ValidationPointer(err) != "/site" ||
			rest != "" || private != "" || strings.Contains(err.Error(), "unknown-site-marker") {
			t.Fatal("unknown site was not safely rejected")
		}
	}
}

func TestInvalidSiteMessageSurvivesRedaction(t *testing.T) {
	for _, site := range []string{
		"okx.com", "OKX", "www.okx.co", "global", "EEA", "eea", "US", "us",
	} {
		t.Run(site, func(t *testing.T) {
			raw := strings.ReplaceAll(credentialsJSON, GlobalSite, site)
			_, redact, err := secret.CredentialRedactor(raw)
			if err != nil {
				t.Fatal("invalid redactor fixture")
			}
			credentials, err := ParseCredentials(raw)
			if !errors.Is(err, domain.ErrInvalid) ||
				domain.ValidationPointer(err) != "/site" ||
				domain.ValidationConstraint(err) != "invalid" ||
				credentials != (Credentials{}) {
				t.Fatal("invalid site lost its validation details")
			}
			message := err.Error()
			if redact.Replace(message) != message {
				t.Fatal("site validation text was changed by credential redaction")
			}
			for _, domain := range []string{"okx.com", "www.okx.co", GlobalSite, EEASite, USSite} {
				if strings.Contains(strings.ToLower(message), domain) {
					t.Fatal("site validation text contains a site domain")
				}
			}
			for _, region := range []string{"global", "EEA", "US"} {
				if strings.Contains(strings.ToLower(message), strings.ToLower(region)) {
					t.Fatal("site validation text contains a registration region")
				}
			}
		})
	}
}

func TestParseCredentials(t *testing.T) {
	got, err := ParseCredentials(strings.ReplaceAll(credentialsJSON, "www.okx.com", " my.okx.com "))
	if err != nil || got.Site != EEASite || got.APIKey != "test-key-marker" ||
		got.APISecret != "test-secret-marker" || got.Passphrase != "test-passphrase-marker" {
		t.Fatal("valid credentials were rejected or changed")
	}
	for _, name := range []string{"apiKey", "apiSecret", "passphrase", "site"} {
		for _, mode := range []string{"missing", "blank"} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				var fields map[string]string
				if json.Unmarshal([]byte(credentialsJSON), &fields) != nil {
					t.Fatal("invalid fixture")
				}
				delete(fields, name)
				if mode == "blank" {
					fields[name] = " \t\n "
				}
				raw, err := json.Marshal(fields)
				if err != nil {
					t.Fatal("could not encode fixture")
				}
				credentials, err := ParseCredentials(string(raw))
				if !errors.Is(err, domain.ErrInvalid) ||
					domain.ValidationPointer(err) != "/"+name ||
					domain.ValidationConstraint(err) != "required" ||
					credentials != (Credentials{}) {
					t.Fatal("missing credential field was not rejected")
				}
				assertNoCredentials(t, err, "")
			})
		}
	}
	for _, tc := range []struct{ name, raw, pointer string }{
		{"unknown", strings.TrimSuffix(credentialsJSON, "}") +
			`,"unknown-field-marker":"unknown-value-marker"}`, ""},
		{"bad_site", strings.ReplaceAll(credentialsJSON,
			"www.okx.com", "bad-site-marker"), "/site"},
		{"type", strings.ReplaceAll(credentialsJSON, `"www.okx.com"`, "42"), ""},
		{"invalid_json", `{"apiKey":"test-key-marker"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCredentials(tc.raw)
			if !errors.Is(err, domain.ErrInvalid) || domain.ValidationPointer(err) != tc.pointer {
				t.Fatal("invalid credentials were not rejected")
			}
			assertNoCredentials(t, err, "")
			for _, marker := range []string{
				"unknown-field-marker", "unknown-value-marker", "bad-site-marker",
			} {
				if strings.Contains(err.Error(), marker) {
					t.Fatal("validation echoed input")
				}
			}
		})
	}
}

func TestSignedHeaders(t *testing.T) {
	for _, withBody := range []bool{false, true} {
		t.Run(fmt.Sprintf("body_%t", withBody), func(t *testing.T) {
			path := "/api/v5/test?ccy=BTC%2FUSDT&limit=2"
			method, encodedBody := http.MethodGet, ""
			var body any
			if withBody {
				method = http.MethodPost
				body = struct {
					InstID string `json:"instId"`
					Size   string `json:"sz"`
				}{"BTC-USDT", "2"}
				encodedBody = `{"instId":"BTC-USDT","sz":"2"}`
			}
			server := httptest.NewServer(http.HandlerFunc(func(
				w http.ResponseWriter, r *http.Request,
			) {
				actualBody, err := io.ReadAll(r.Body)
				if err != nil || string(actualBody) != encodedBody ||
					r.Method != method || r.URL.RequestURI() != path {
					t.Error("wrong method, target or JSON body")
				}
				expected := map[string]string{
					"OK-ACCESS-KEY":        "test-key-marker",
					"OK-ACCESS-PASSPHRASE": "test-passphrase-marker",
					"OK-ACCESS-TIMESTAMP":  "2026-11-12T12:05:06.123Z",
					"OK-ACCESS-SIGN": expectedSignature(
						"2026-11-12T12:05:06.123Z" + method + path + encodedBody),
					"Content-Type": "application/json",
				}
				for name, value := range expected {
					if r.Header.Get(name) != value {
						t.Error("authentication or content header mismatch")
					}
				}
				_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"value":"decoded"}]}`)
			}))
			defer server.Close()
			client := testClient(t, server.URL, server.Client())
			var result []struct {
				Value string `json:"value"`
			}
			status, err := client.Signed(context.Background(),
				strings.ToLower(method), path, body, &result)
			if err != nil || status != 200 || len(result) != 1 || result[0].Value != "decoded" {
				t.Fatal("successful data envelope was not decoded")
			}
		})
	}
}

func TestSignedEnvelopeErrors(t *testing.T) {
	for _, tc := range []struct {
		name                string
		status              int
		body, code, message string
		items               int
	}{
		{"item_refusal", 200,
			`{"code":"1","msg":"refused","data":[{"sCode":"51000",` +
				`"sMsg":"invalid order","ordId":"not-retained"}]}`, "1", "refused", 1},
		{"http_error", 403, `{"code":"50101","msg":"denied","data":[]}`, "50101", "denied", 0},
		{"http_error_zero_code", 503,
			`{"code":"0","msg":"unavailable","data":[]}`, "0", "unavailable", 0},
		{"non_json", 502,
			"test-key-marker test-secret-marker test-passphrase-marker <html>", "", "", 0},
		{"redirect", 302, "", "", "", 0},
		{"missing_code", 200, `{"msg":"missing code","data":[]}`, "", "missing code", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(
				w http.ResponseWriter, r *http.Request,
			) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client := testClient(t, server.URL, server.Client())
			status, err := client.Signed(context.Background(), "GET", "/api/v5/test", nil, nil)
			var apiErr *APIError
			if status != tc.status || !errors.As(err, &apiErr) || apiErr.Status != tc.status ||
				apiErr.Code != tc.code || apiErr.Message != tc.message ||
				len(apiErr.Items) != tc.items {
				t.Fatal("HTTP or OKX refusal lost structured error fields")
			}
			if tc.items != 0 && (apiErr.Items[0].Code != "51000" ||
				apiErr.Items[0].Message != "invalid order") {
				t.Fatal("per-item status was not retained")
			}
			if tc.name == "non_json" && err.Error() != "okx: HTTP 502" {
				t.Fatal("undecoded HTTP error included absent fields")
			}
			assertNoCredentials(t, err, "")
			if strings.Contains(err.Error(), "not-retained") ||
				strings.Contains(err.Error(), "<html>") {
				t.Fatal("error exposed response body beyond message fields")
			}
		})
	}
}

func TestAPIErrorText(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  APIError
		want string
	}{
		{"status", APIError{Status: 502}, "okx: HTTP 502"},
		{"code", APIError{Status: 403, Code: "50101"}, "okx: HTTP 403, code 50101"},
		{"message", APIError{Status: 403, Message: "denied"}, "okx: HTTP 403: denied"},
		{"item", APIError{Status: 200, Code: "1", Message: "refused",
			Items: []ItemError{{Code: "51000", Message: "invalid order"}}},
			"okx: HTTP 200, code 1: refused; code 51000: invalid order"},
		{"item_code", APIError{Status: 200, Items: []ItemError{{Code: "51000"}}},
			"okx: HTTP 200; code 51000"},
		{"item_message", APIError{Status: 200, Items: []ItemError{{Message: "invalid order"}}},
			"okx: HTTP 200; invalid order"},
		{"two_item_messages", APIError{Status: 200,
			Items: []ItemError{{Message: "invalid order"}, {Message: "denied"}}},
			"okx: HTTP 200; invalid order; denied"},
		{"empty_item", APIError{Status: 200, Items: []ItemError{{}}},
			"okx: HTTP 200"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Error() != tc.want {
				t.Fatal("API error text included absent parts or omitted present ones")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSignedRedaction(t *testing.T) {
	for _, mode := range []string{"envelope", "codes"} {
		t.Run(mode, func(t *testing.T) {
			var signature string
			httpClient := &http.Client{Transport: roundTripFunc(func(
				r *http.Request,
			) (*http.Response, error) {
				signature = r.Header.Get("OK-ACCESS-SIGN")
				echo := r.Header.Get("OK-ACCESS-KEY") + " " +
					r.Header.Get("OK-ACCESS-PASSPHRASE") + " test-secret-marker " +
					GlobalSite + " " + signature
				code, itemCode := "1", "51000"
				if mode == "codes" {
					code, itemCode = echo, echo
				}
				payload, err := json.Marshal(map[string]any{"code": code, "msg": echo,
					"data": []map[string]string{{"sCode": itemCode, "sMsg": echo}}})
				if err != nil {
					t.Fatal("could not encode error fixture")
				}
				return &http.Response{StatusCode: 200,
					Body:   io.NopCloser(strings.NewReader(string(payload))),
					Header: make(http.Header)}, nil
			})}
			client := testClient(t, "https://example.invalid", httpClient)
			_, err := client.Signed(context.Background(), "GET", "/api/v5/test", nil, nil)
			assertNoCredentials(t, err, signature)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || len(apiErr.Items) != 1 {
				t.Fatal("venue refusal lost structured fields")
			}
			for _, value := range []string{
				apiErr.Code, apiErr.Message, apiErr.Items[0].Code, apiErr.Items[0].Message,
			} {
				assertNoCredentials(t, errors.New(value), signature)
			}
		})
	}
}

func TestSignedCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := testClient(t, "https://example.invalid", &http.Client{})
	status, err := client.Signed(ctx, http.MethodGet, "/api/v5/test", nil, nil)
	var urlErr *url.Error
	if status != 0 || !errors.Is(err, context.Canceled) || !errors.As(err, &urlErr) {
		t.Fatal("cancelled request lost its cancellation cause or transport type")
	}
}

func TestSignedLocalErrorCauses(t *testing.T) {
	credentials, err := ParseCredentials(credentialsJSON)
	if err != nil {
		t.Fatal("credential fixture was rejected")
	}
	markers := []string{
		credentials.APIKey, credentials.APISecret, credentials.Passphrase, credentials.Site,
	}
	for _, tc := range []struct {
		name, payload, operation string
		status                   int
	}{
		{"transport", "", "request", 0},
		{"read", "", "read response", 200},
		{"envelope", `{"code":[]}`, "decode response", 200},
		{"data", `{"code":"0","data":42}`, "decode data", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New(strings.Join(markers, " "))
			httpClient := &http.Client{Transport: roundTripFunc(func(
				*http.Request,
			) (*http.Response, error) {
				if tc.name == "transport" {
					return nil, cause
				}
				body := io.NopCloser(strings.NewReader(tc.payload))
				if tc.name == "read" {
					body = errorBody{err: cause}
				}
				return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header)}, nil
			})}
			client := testClient(t, "https://example.invalid", httpClient)
			var result string
			status, err := client.Signed(context.Background(), "GET", "/api/v5/test", nil, &result)
			if status != tc.status || err == nil ||
				!strings.HasPrefix(err.Error(), "okx: "+tc.operation+": ") {
				t.Fatal("local error lost its status or operation")
			}
			if tc.name == "transport" || tc.name == "read" {
				if !errors.Is(err, cause) {
					t.Fatal("local error lost its original cause")
				}
				for _, marker := range markers {
					if strings.Contains(err.Error(), marker) {
						t.Fatal("local error exposed a credential")
					}
				}
			} else {
				var typeErr *json.UnmarshalTypeError
				if !errors.As(err, &typeErr) {
					t.Fatal("decode error lost its original type")
				}
			}
		})
	}
}

type errorBody struct{ err error }

func (b errorBody) Read([]byte) (int, error) { return 0, b.err }
func (b errorBody) Close() error             { return nil }

func TestSignedRedirectRefused(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter, r *http.Request,
	) {
		followed.Add(1)
		_, _ = io.WriteString(w, `{"code":"0","data":[]}`)
	}))
	defer target.Close()
	for _, status := range []int{301, 302, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			source := httptest.NewServer(http.HandlerFunc(func(
				w http.ResponseWriter, r *http.Request,
			) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
			}))
			defer source.Close()
			var originalRedirects atomic.Int32
			original := &http.Client{Timeout: time.Hour, CheckRedirect: func(
				*http.Request, []*http.Request,
			) error {
				originalRedirects.Add(1)
				return nil
			}}
			client := testClient(t, source.URL, original)
			if client.httpClient == original || client.httpClient.Timeout != original.Timeout {
				t.Fatal("caller HTTP client was not copied")
			}
			got, err := client.Signed(context.Background(), "GET", "/api/v5/test", nil, nil)
			var apiErr *APIError
			if got != status || !errors.As(err, &apiErr) || followed.Load() != 0 ||
				originalRedirects.Load() != 0 {
				t.Fatal("redirect was followed or not reported")
			}
			if err := original.CheckRedirect(nil, nil); err != nil ||
				originalRedirects.Load() != 1 {
				t.Fatal("caller redirect policy was modified")
			}
		})
	}
}

func TestSignedRequiredInputs(t *testing.T) {
	credentials, err := ParseCredentials(credentialsJSON)
	if err != nil {
		t.Fatal("invalid credential fixture")
	}
	_, redact, err := secret.CredentialRedactor(credentialsJSON)
	if err != nil {
		t.Fatal("invalid redactor fixture")
	}
	for _, tc := range []struct{ name, message string }{
		{"http", "okx: HTTP client is required"},
		{"redactor", "okx: credential redactor is required"},
		{"base", "okx: REST base URL is required"},
		{"method", "okx: request method is required"},
		{"path", "okx: request path must be absolute within the REST host"},
		{"host_path", "okx: request path must be absolute within the REST host"},
		{"fragment", "okx: request path must contain only an encoded path and query"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripFunc(func(
				*http.Request,
			) (*http.Response, error) {
				t.Fatal("invalid input reached the transport")
				return nil, errors.New("unexpected transport call")
			})}
			baseURL, method, path := "https://example.invalid", "GET", "/api/v5/test"
			redactor := redact
			switch tc.name {
			case "http":
				httpClient = nil
			case "redactor":
				redactor = nil
			case "base":
				baseURL = ""
			case "method":
				method = ""
			case "path":
				path = ""
			case "host_path":
				path = "//example.invalid/test"
			case "fragment":
				path += "#test"
			}
			client := NewClient(baseURL, credentials, redactor, httpClient)
			status, err := client.Signed(context.Background(), method, path, nil, nil)
			if status != 0 || err == nil || err.Error() != tc.message {
				t.Fatal("guard returned the wrong status or error")
			}
		})
	}
}

func TestAccountUID(t *testing.T) {
	for _, tc := range []struct{ name, body, uid string }{
		{"exact", `{"code":"0","data":[{"uid":"00900719925474099312345",` +
			`"mainUid":"other","label":"ignored"}]}`, "00900719925474099312345"},
		{"missing", `{"code":"0","data":[{}]}`, ""},
		{"empty", `{"code":"0","data":[]}`, ""},
		{"null", `{"code":"0","data":null}`, ""},
		{"ambiguous", `{"code":"0","data":[{"uid":"1"},{"uid":"2"}]}`, ""},
		{"refused", `{"code":"50101","msg":"denied","data":[]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(
				w http.ResponseWriter, r *http.Request,
			) {
				if r.Method != "GET" || r.URL.RequestURI() != "/api/v5/account/config" ||
					r.Header.Get("OK-ACCESS-SIGN") == "" {
					t.Error("wrong or unsigned account request")
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			uid, err := testClient(t, server.URL, server.Client()).
				AccountUID(context.Background())
			if tc.uid == "" {
				if err == nil || uid != "" {
					t.Fatal("missing account uid was accepted")
				}
			} else if err != nil || uid != tc.uid {
				t.Fatal("exact uid string was lost")
			}
		})
	}
}
