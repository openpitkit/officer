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
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/secret"
	"go.openpit.dev/officer/internal/logtest"
)

var signedCredentials = Credentials{APIKey: `test-key<&"/?`, APISecret: `test-secret>&"/+`}

func signedClient(t *testing.T, host string, httpClient *http.Client) *Client {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"apiKey": signedCredentials.APIKey, "apiSecret": signedCredentials.APISecret})
	if err != nil {
		t.Fatal(err)
	}
	_, redact, err := secret.CredentialRedactor(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(host, signedCredentials, redact, httpClient)
	c.now = func() time.Time { return time.UnixMilli(1700000000123) }
	return c
}

func independentHMAC(key, input string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = io.WriteString(mac, input)
	return hex.EncodeToString(mac.Sum(nil))
}

func assertClean(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, value := range []string{signedCredentials.APIKey, signedCredentials.APISecret} {
		encoded, _ := json.Marshal(value)
		for _, spelling := range []string{value, url.QueryEscape(value), url.PathEscape(value), string(encoded[1 : len(encoded)-1])} {
			if strings.Contains(err.Error(), spelling) {
				t.Errorf("error text leaks protected value")
			}
		}
	}
}

type countedBody struct{ calls *int }

func (b countedBody) MarshalJSON() ([]byte, error) {
	*b.calls++
	return []byte(fmt.Sprintf(`{"memo":"space + / & < >","sequence":%d}`, *b.calls)), nil
}

func TestSignedWireBytes(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			params := url.Values{"z": {"space + / & < >"}, "a": {"second", "first"}}
			original := params.Encode()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				payload := r.URL.RawQuery
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if r.Method != method || r.URL.Path != "/v5/test" {
					t.Error("wrong request target")
				}
				if method == http.MethodPost {
					payload = string(body)
					if r.Header.Get("Content-Type") != "application/json" || r.URL.RawQuery != "" {
						t.Error("wrong POST encoding")
					}
					if !strings.Contains(payload, `"sequence":1`) {
						t.Error("body was marshalled more than once")
					}
				} else if payload != original || len(body) != 0 {
					t.Error("GET query bytes changed")
				}
				if r.Header.Get("X-BAPI-API-KEY") != signedCredentials.APIKey || r.Header.Get("X-BAPI-TIMESTAMP") != "1700000000123" || r.Header.Get("X-BAPI-RECV-WINDOW") != "5000" {
					t.Error("wrong authentication headers")
				}
				input := r.Header.Get("X-BAPI-TIMESTAMP") + r.Header.Get("X-BAPI-API-KEY") + r.Header.Get("X-BAPI-RECV-WINDOW") + payload
				if r.Header.Get("X-BAPI-SIGN") != independentHMAC(signedCredentials.APISecret, input) {
					t.Error("signature does not match wire payload")
				}
				for key, values := range r.Header {
					if strings.Contains(key+strings.Join(values, ""), signedCredentials.APISecret) {
						t.Error("secret escaped HMAC key")
					}
				}
				if strings.Contains(r.URL.String()+string(body), signedCredentials.APISecret) {
					t.Error("secret escaped HMAC key")
				}
				_, _ = io.WriteString(w, `{"retCode":0,"retMsg":"OK","result":{"orderId":"42"}}`)
			}))
			defer server.Close()
			httpClient := server.Client()
			c := signedClient(t, server.URL+"///", httpClient)
			if httpClient.CheckRedirect != nil || c.httpClient == httpClient {
				t.Fatal("caller HTTP client was not copied")
			}
			var result struct {
				OrderID string `json:"orderId"`
			}
			var status int
			var err error
			if method == http.MethodGet {
				status, err = c.Get(t.Context(), "/v5/test", params, &result)
			} else {
				status, err = c.Post(t.Context(), "/v5/test", countedBody{&calls}, &result)
			}
			if err != nil || status != 200 || result.OrderID != "42" {
				t.Fatalf("request failed: status=%d err=%v result=%+v", status, err, result)
			}
			if params.Encode() != original || (method == http.MethodPost && calls != 1) {
				t.Error("input was modified or marshalled twice")
			}
		})
	}
}

func TestSignedEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, body            string
		status, code          int
		api, decoded, success bool
	}{
		{"success", `{"retCode":0,"result":{"orderId":"42"}}`, 200, 0, false, false, true},
		{"json refusal", `{"retCode":10001,"retMsg":"invalid"}`, 400, 10001, true, true, false},
		{"plain refusal", `not JSON`, 503, 0, true, false, false},
		{"partial refusal", `{"retCode":10001}`, 400, 10001, true, false, false},
		{"message only", `{"retMsg":"invalid"}`, 400, 0, true, false, false},
		{"missing code", `{"result":{"orderId":"42"}}`, 200, 0, false, false, false},
		{"null code", `{"retCode":null,"result":{}}`, 200, 0, false, false, false},
		{"bad code", `{"retCode":"0","result":{}}`, 200, 0, false, false, false},
		{"venue refusal", `{"retCode":10001,"retMsg":"invalid","result":{}}`, 200, 10001, true, true, false},
		{"venue refusal without message", `{"retCode":10001}`, 200, 10001, true, true, false},
		{"bad result", `{"retCode":0,"result":{"orderId":[]}}`, 200, 0, false, false, false},
		{"missing result", `{"retCode":0}`, 200, 0, false, false, false},
		{"malformed", `{"retCode":`, 200, 0, false, false, false},
		{"trailing JSON", `{"retCode":0,"result":{}} {}`, 200, 0, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			var result struct {
				OrderID string `json:"orderId"`
			}
			status, err := signedClient(t, server.URL, server.Client()).Get(t.Context(), "/test", nil, &result)
			if status != tc.status {
				t.Errorf("status=%d, want %d", status, tc.status)
			}
			if tc.success {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("unconfirmed response was accepted")
			}
			var apiErr *APIError
			if errors.As(err, &apiErr) != tc.api {
				t.Fatalf("wrong error type: %T", err)
			}
			if tc.api && (apiErr.RetCode != tc.code || apiErr.BodyDecoded != tc.decoded || apiErr.Status != tc.status) {
				t.Errorf("unexpected API error: %+v", apiErr)
			}
		})
	}
	for _, body := range []string{`{}`, `{"retCode":4,"retMsg":"no"}`, `{"retCode":null}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		_, err := signedClient(t, server.URL, server.Client()).Post(t.Context(), "/test", nil, nil)
		server.Close()
		if err == nil {
			t.Error("discarded result bypassed envelope validation")
		}
	}
}

func TestSignedRefusesRedirect(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = io.WriteString(w, `{"retCode":0}`) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	c := signedClient(t, server.URL, server.Client())
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			var err error
			if method == "GET" {
				_, err = c.Get(t.Context(), "/test", url.Values{"signed": {"payload"}}, nil)
			} else {
				_, err = c.Post(t.Context(), "/test", map[string]string{"signed": "payload"}, nil)
			}
			if hits.Load() != 0 {
				t.Fatal("redirect target was hit")
			}
			if err == nil {
				t.Fatal("redirect accepted")
			}
		})
	}
}

type echoDecoder string

func (d *echoDecoder) UnmarshalJSON([]byte) error { return errors.New(string(*d)) }

func TestSignedRedaction(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, failure := range []string{"transport", "api", "http-api", "read", "close", "decode"} {
			t.Run(method+"/"+failure, func(t *testing.T) {
				var logs bytes.Buffer
				logtest.SetDefault(t, slog.NewTextHandler(&logs, nil))
				var decoder echoDecoder
				var result any
				if failure == "decode" {
					result = &decoder
				}
				httpClient := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
					var spellings []string
					for _, value := range []string{signedCredentials.APIKey, signedCredentials.APISecret} {
						quoted, _ := json.Marshal(value)
						spellings = append(spellings, value, string(quoted[1:len(quoted)-1]), url.QueryEscape(value), url.PathEscape(value))
					}
					echo := strings.Join(spellings, " ")
					switch failure {
					case "transport":
						return nil, &url.Error{Op: "nested", URL: r.URL.String(), Err: errors.New(echo)}
					case "api", "http-api":
						body, _ := json.Marshal(map[string]any{"retCode": 10001, "retMsg": echo})
						status := http.StatusOK
						if failure == "http-api" {
							status = http.StatusBadRequest
						}
						return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
					case "decode":
						decoder = echoDecoder(echo)
						return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"retCode":0,"result":{}}`)), Header: make(http.Header)}, nil
					default:
						return &http.Response{StatusCode: 200, Body: &signedErrorBody{readError: failure == "read", text: echo}, Header: make(http.Header)}, nil
					}
				})}
				c := signedClient(t, "https://venue.invalid", httpClient)
				var err error
				if method == "GET" {
					_, err = c.Get(t.Context(), "/test", url.Values{"memo": {"sensitive + < >"}}, result)
				} else {
					_, err = c.Post(t.Context(), "/test", map[string]string{"memo": "sensitive + < >"}, result)
				}
				if failure == "close" {
					if err != nil {
						t.Fatal(err)
					}
					if logs.Len() == 0 {
						t.Fatal("missing close warning")
					}
				} else {
					assertClean(t, err)
				}
				if failure == "api" || failure == "http-api" {
					var apiErr *APIError
					if !errors.As(err, &apiErr) {
						t.Fatal("missing APIError")
					}
					assertClean(t, errors.New(apiErr.Message))
				}
				assertClean(t, errors.New(logs.String()))
			})
		}
	}
}

type signedErrorBody struct {
	readError bool
	text      string
	sent      bool
}

func (b *signedErrorBody) Read(p []byte) (int, error) {
	if b.readError {
		return 0, errors.New(b.text)
	}
	if b.sent {
		return 0, io.EOF
	}
	b.sent = true
	return copy(p, `{"retCode":0}`), io.EOF
}
func (b *signedErrorBody) Close() error { return errors.New(b.text) }

func TestSignedClosedServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c := signedClient(t, server.URL, server.Client())
	server.Close()
	payload := url.Values{"private": {"signed-payload"}}
	_, err := c.Get(t.Context(), "/test", payload, nil)
	assertClean(t, err)
	_, err = c.Post(t.Context(), "/test", map[string]string{"private": "signed-payload"}, nil)
	assertClean(t, err)
}

func TestSignedSecretOnlyHMACKey(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				body := ""
				if r.Body != nil {
					encoded, _ := io.ReadAll(r.Body)
					body = string(encoded)
				}
				wire := r.URL.String() + body
				for key, values := range r.Header {
					wire += key + strings.Join(values, "")
				}
				encoded, _ := json.Marshal(signedCredentials.APISecret)
				for _, spelling := range []string{signedCredentials.APISecret, url.QueryEscape(signedCredentials.APISecret), string(encoded[1 : len(encoded)-1])} {
					if strings.Contains(wire, spelling) {
						t.Error("secret escaped HMAC key")
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"retCode":0}`)), Header: make(http.Header)}, nil
			})}
			c := signedClient(t, "https://venue.invalid", client)
			var err error
			if method == "GET" {
				_, err = c.Get(t.Context(), "/test", nil, nil)
			} else {
				_, err = c.Post(t.Context(), "/test", map[string]string{"symbol": "BTCUSDT"}, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSignedRequiresDependencies(t *testing.T) {
	for _, missing := range []string{"client", "redactor"} {
		for _, method := range []string{"GET", "POST"} {
			t.Run(missing+"/"+method, func(t *testing.T) {
				defer func() {
					if recover() != nil {
						t.Error("request panicked instead of returning dependency error")
					}
				}()
				c := signedClient(t, "https://venue.invalid", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					t.Error("request sent without dependency")
					return nil, errors.New("unexpected request")
				})})
				if missing == "client" {
					c.httpClient = nil
				} else {
					c.redact = nil
				}
				var status int
				var err error
				if method == "GET" {
					status, err = c.Get(t.Context(), "/test", nil, nil)
				} else {
					status, err = c.Post(t.Context(), "/test", nil, nil)
				}
				if status != 0 || err == nil || !strings.Contains(err.Error(), "is required") {
					t.Errorf("%s/%s: expected explicit dependency error", method, missing)
				}
			})
		}
	}
}

func TestBybitCredentialsAndAPIError(t *testing.T) {
	credentials, err := ParseCredentials(`{"apiKey":" key ","apiSecret":" secret "}`)
	if err != nil || !reflect.DeepEqual(credentials, Credentials{APIKey: "key", APISecret: "secret"}) {
		t.Fatalf("parse: %+v %v", credentials, err)
	}
	if _, err := ParseCredentials(`{}`); err == nil || !strings.Contains(err.Error(), "bybit") {
		t.Fatal("missing credentials accepted")
	}
	for _, tc := range []struct {
		err  APIError
		want string
	}{
		{APIError{Status: 503}, "bybit: HTTP 503"},
		{APIError{Status: 400, Message: "invalid"}, "bybit: HTTP 400: invalid"},
		{APIError{Status: 200, RetCode: 10001, BodyDecoded: true}, "bybit: HTTP 200 (retCode 10001)"},
		{APIError{Status: 200, RetCode: 10001, Message: "invalid", BodyDecoded: true}, "bybit: HTTP 200 (retCode 10001): invalid"},
	} {
		if tc.err.Error() != tc.want {
			t.Errorf("error=%q, want %q", tc.err.Error(), tc.want)
		}
	}
}
