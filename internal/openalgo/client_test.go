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

package openalgo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/secret"
)

const testKey = "test-key-marker"

func testRedactor(t *testing.T) *strings.Replacer {
	t.Helper()
	_, redactor, err := secret.CredentialRedactor(`{"apiKey":"` + testKey + `"}`)
	if err != nil {
		t.Fatal("cannot create test redactor")
	}
	return redactor
}

func assertNoKey(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, testKey) {
		t.Fatal("credential leaked into display text")
	}
}

func testClient(t *testing.T, status int, payload string, name string, fields map[string]string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/"+name || r.Header.Get("Content-Type") != "application/json" {
			t.Error("unexpected request method, path or content type")
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery || strings.Contains(r.URL.String(), testKey) {
			t.Error("request URL carries authentication material")
		}
		for _, values := range r.Header {
			for _, value := range values {
				assertNoKey(t, value)
			}
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error("invalid JSON request body")
		}
		if body["apikey"] != testKey {
			t.Error("missing body API key")
		}
		delete(body, "apikey")
		if len(body) != len(fields) {
			t.Error("unexpected request body members")
		}
		for key, value := range fields {
			if body[key] != value {
				t.Error("request body member changed")
			}
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL+"///", testKey, server.Client(), testRedactor(t))
}

func TestParseCredentials(t *testing.T) {
	for _, addresses := range [][2]string{
		{"http://127.0.0.1:5000", "ws://127.0.0.1:8765"},
		{"https://bridge/base/", "wss://bridge/stream"},
	} {
		raw, _ := json.Marshal(map[string]string{"apiKey": " \t" + testKey + "\n ", "url": " " + addresses[0] + " ", "wsUrl": " " + addresses[1] + " "})
		got, err := ParseCredentials(string(raw))
		if err != nil || got != (Credentials{URL: addresses[0], WSURL: addresses[1], APIKey: testKey}) {
			t.Fatal("valid credentials refused or not trimmed")
		}
	}
	for _, field := range []string{"url", "wsUrl"} {
		for _, tc := range []struct{ name, value string }{
			{"scheme", "ftp://refused-host/refused-path"},
			{"userinfo", "http://refused-user:refused-password@refused-host/refused-path"},
			{"empty_userinfo", "http://@refused-host/refused-path"},
			{"query", "http://refused-host/refused-path?refused-token=refused-secret"},
			{"bare_query", "http://refused-host/refused-path?"},
			{"fragment", "http://refused-host/refused-path#refused-secret"},
			{"bare_fragment", "http://refused-host/refused-path#"},
			{"relative", "//refused-host/refused-path"},
			{"hostless", "http:/refused-path"},
			{"malformed", "http://%refused"},
			{"blank", " \t "},
			{"empty", ""},
		} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				value := tc.value
				if field == "wsUrl" && strings.HasPrefix(value, "http:") {
					value = "ws:" + strings.TrimPrefix(value, "http:")
				}
				fields := map[string]string{"apiKey": testKey, "url": "http://host", "wsUrl": "ws://host"}
				fields[field] = value
				raw, _ := json.Marshal(fields)
				_, err := ParseCredentials(string(raw))
				if !errors.Is(err, domain.ErrInvalid) || domain.ValidationPointer(err) != "/"+field {
					t.Fatal("invalid address did not report the field")
				}
				constraint := "format"
				if strings.TrimSpace(value) == "" {
					constraint = "required"
				}
				if domain.ValidationConstraint(err) != constraint {
					t.Fatal("unexpected address validation constraint")
				}
				if strings.Contains(err.Error(), "refused") {
					t.Fatal("refused address leaked into validation error")
				}
				assertNoKey(t, err.Error())
			})
		}
	}
	for _, raw := range []string{
		"", "null", "[]", `{}`, `{broken`,
		`{"apiKey":true,"url":"http://host","wsUrl":"ws://host"}`,
		`{"apiKey":" ","url":"http://host","wsUrl":"ws://host"}`,
		`{"apiKey":"test-key-marker","url":"http://host"}`,
		`{"apiKey":"test-key-marker","wsUrl":"ws://host"}`,
		`{"apiKey":"test-key-marker","url":"http://host","wsUrl":"ws://host","extra":"refused-extra"}`,
		`{"apiKey":"test-key-marker","url":"ws://host","wsUrl":"ws://host"}`,
		`{"apiKey":"test-key-marker","url":"http://host","wsUrl":"https://host"}`,
	} {
		_, err := ParseCredentials(raw)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("invalid credentials accepted")
		}
		assertNoKey(t, err.Error())
		if strings.Contains(err.Error(), "refused-extra") {
			t.Fatal("unknown value leaked")
		}
	}
}

func TestParseCredentialsNumericKeys(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		valid     bool
	}{
		{"digits", "12345678901234567890", false},
		{"exponent", "1.5e-3", false},
		{"negative", "-12", false},
		{"numeric_alphabet", "+.eE-012", false},
		{"hex_letter", "1234567890a1234567890", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{
				"apiKey": tc.key, "url": "http://host", "wsUrl": "ws://host",
			})
			got, err := ParseCredentials(string(raw))
			if tc.valid {
				if err != nil || got.APIKey != tc.key {
					t.Fatal("hex key refused or changed")
				}
				return
			}
			if !errors.Is(err, domain.ErrInvalid) ||
				domain.ValidationPointer(err) != "/apiKey" ||
				domain.ValidationConstraint(err) != "format" {
				t.Fatal("numeric key did not report apiKey validation")
			}
			if !strings.Contains(err.Error(), "apiKey") ||
				strings.Contains(err.Error(), tc.key) {
				t.Fatal("numeric key error lost field or exposed its value")
			}
		})
	}
}

func TestDecimal(t *testing.T) {
	for _, tc := range []struct{ name, raw, want, kind string }{
		{"number", `12345678901234567890.00100`, "12345678901234567890.00100", ""},
		{"string", `"001.2300"`, "001.2300", ""},
		{"negative", `-0.0500`, "-0.0500", ""},
		{"number_exponent", `1.2300e+2`, "", "number"},
		{"positive_exponent", `1e2`, "", "number"},
		{"negative_exponent", `1E-2`, "", "number"},
		{"huge_exponent", `1e400`, "", "number"},
		{"null", `null`, "", ""},
		{"empty", `""`, "", "string"},
		{"invalid", `"test-key-marker"`, "", "string"},
		{"bool", `true`, "", "boolean"},
		{"object", `{}`, "", "object"},
		{"array", `[]`, "", "array"},
		{"spaced", `" 1.5 "`, "", "string"},
		{"exponent_string", `"1e3"`, "", "string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got Decimal
			err := json.Unmarshal([]byte(tc.raw), &got)
			if tc.kind == "" {
				if err != nil || string(got) != tc.want {
					t.Fatal("decimal text changed or valid decimal refused")
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.kind) {
					t.Fatal("invalid decimal did not name its type")
				}
				assertNoKey(t, err.Error())
			}
		})
	}
	var absent struct {
		Value Decimal `json:"value"`
	}
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil || absent.Value != "" {
		t.Fatal("absent decimal is not empty")
	}
}

func TestAPIErrors(t *testing.T) {
	for _, status := range []int{200, 201, 301, 307, 400, 403, 404, 429, 500, 501, 502} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			client := testClient(t, status, `{"status":"error","message":"refused test-key-marker"}`, "ping", nil)
			_, err := client.Ping(context.Background())
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != status {
				t.Fatal("HTTP status or API error type lost")
			}
			if errors.Is(err, ErrInvalidResponse) {
				t.Fatal("API error was classified as an unreadable response")
			}
			assertNoKey(t, apiErr.Message)
			assertNoKey(t, err.Error())
			want := "refused [redacted]"
			if apiErr.Message != want {
				t.Fatal("unexpected redacted API message")
			}
			prefix := "openalgo: HTTP " + strconv.Itoa(status)
			if want != "" {
				prefix += ": " + want
			}
			if err.Error() != prefix {
				t.Fatal("unexpected API error format")
			}
		})
	}
	for _, tc := range []struct{ name, payload, want string }{
		{"object", `{"message": { "symbol": ["bad test-key-marker"] }}`, "symbol: bad [redacted]"},
		{"field_errors", `{"message":{"symbol":["required","invalid"],"quantity":["positive"]}}`, "quantity: positive; symbol: required, invalid"},
		{"unicode_value", `{"message":{"symbol":"bad \u0074est-key-marker"}}`, "symbol: bad [redacted]"},
		{"unicode_name", `{"message":{"\u0074est-key-marker":"bad"}}`, "[redacted]: bad"},
		{"unicode_nested", `{"message":[{"symbol":[["\u0074est-key-marker"],true,42,null]}]}`, "symbol: [redacted], true, 42, "},
		{"number", `{"message":42}`, "42"},
		{"boolean", `{"message":false}`, "false"},
		{"null", `{"message":null}`, ""},
		{"missing", `{"status":"error"}`, ""},
		{"non_json", `not JSON test-key-marker`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, 400, tc.payload, "ping", nil)
			_, err := client.Ping(context.Background())
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Message != tc.want {
				t.Fatal("decoded message was not rendered and redacted")
			}
			if errors.Is(err, ErrInvalidResponse) {
				t.Fatal("HTTP refusal was classified as an unreadable response")
			}
			assertNoKey(t, apiErr.Message)
			assertNoKey(t, err.Error())
			if strings.Contains(apiErr.Message, `\u0074est-key-marker`) ||
				strings.Contains(err.Error(), `\u0074est-key-marker`) {
				t.Fatal("escaped credential leaked into message")
			}
		})
	}
}

func TestAPIErrorModes(t *testing.T) {
	for _, tc := range []struct {
		name, payload, mode, message string
		decoded                      bool
	}{
		{"analyzer", `{"status":"error","message":"Order not found","mode":"analyze"}`, "analyze", "Order not found", true},
		{"absent", `{"status":"error","message":"Order not found"}`, "", "Order not found", true},
		{"redacted", `{"status":"error","message":"refused test-key-marker","mode":"\u0074est-key-marker"}`, "[redacted]", "refused [redacted]", true},
		{"undecoded", `not JSON test-key-marker`, "", "", false},
		{"empty_body", `{}`, "", "", false},
		{"null_body", `null`, "", "", false},
		{"message_only", `{"message":"x"}`, "", "x", false},
		{"empty_status", `{"status":""}`, "", "", false},
		{"mode_without_status", `{"mode":"analyze","message":"x"}`, "", "x", false},
		{"mode_with_empty_status", `{"status":"","mode":"analyze"}`, "", "", false},
		{"partial", `{"mode":"analyze","status":`, "", "", false},
		{"trailing", `{"status":"error","mode":"analyze"} extra`, "", "", false},
		{"number", `{"status":"error","message":"Order not found","mode":42}`, "", "Order not found", true},
		{"object", `{"status":"error","message":"Order not found","mode":{"key":"test-key-marker"}}`, "", "Order not found", true},
		{"null", `{"status":"error","message":"Order not found","mode":null}`, "", "Order not found", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, http.StatusNotFound, tc.payload, "orderstatus", map[string]string{"orderid": "123"})
			_, err := client.OrderStatus(context.Background(), "123")
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
				t.Fatal("HTTP status or API error type lost")
			}
			assertNoKey(t, apiErr.Mode)
			assertNoKey(t, apiErr.Message)
			assertNoKey(t, err.Error())
			if apiErr.Mode != tc.mode || apiErr.EnvelopeDecoded != tc.decoded || apiErr.Message != tc.message {
				t.Fatal("API error lost its envelope, mode or message")
			}
			if errors.Is(err, ErrInvalidResponse) {
				t.Fatal("HTTP refusal became an unreadable-response failure")
			}
		})
	}
}

func TestAPIMessageNumbers(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			for _, tc := range []struct{ name, message, want string }{
				{"large_integer", `{"id":[9007199254740993]}`, "id: 9007199254740993"},
				{"large_exponent", `1e400`, "1e400"},
				{"nested_exponent", `{"value":[1e400]}`, "value: 1e400"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					client := testClient(t, status, `{"status":"error","message":`+tc.message+`}`, "ping", nil)
					_, err := client.Ping(context.Background())
					var apiErr *APIError
					if !errors.As(err, &apiErr) || apiErr.Status != status || apiErr.Message != tc.want {
						t.Fatal("venue number text changed or API error lost")
					}
					if err.Error() != "openalgo: HTTP "+strconv.Itoa(status)+": "+tc.want {
						t.Fatal("venue number text changed in displayed error")
					}
				})
			}
		})
	}
}

func TestResponseTrailingData(t *testing.T) {
	for _, tc := range []struct{ name, status string }{
		{"success", "success"},
		{"error", "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, suffix := range []string{` {}`, ` test-key-marker`, "\v", "\f", "\u00a0"} {
				client := testClient(t, 200,
					`{"status":"`+tc.status+`","data":{"broker":"zerodha"}}`+suffix, "ping", nil)
				_, err := client.Ping(context.Background())
				if err == nil || !strings.Contains(err.Error(), "openalgo: decode response:") {
					t.Fatal("trailing data did not cause a response decode error")
				}
				if !errors.Is(err, ErrInvalidResponse) {
					t.Fatal("envelope decode failure lost ErrInvalidResponse")
				}
				assertNoKey(t, err.Error())
			}
		})
	}
	client := testClient(t, 200, "{\"status\":\"success\",\"data\":{\"broker\":\"zerodha\"}} \n\t", "ping", nil)
	if broker, err := client.Ping(context.Background()); err != nil || broker != "zerodha" {
		t.Fatal("trailing whitespace refused")
	}
}

func TestPing(t *testing.T) {
	client := testClient(t, 200, `{"status":"success","data":{"message":"pong","broker":"zerodha"}}`, "ping", nil)
	if broker, err := client.Ping(context.Background()); err != nil || broker != "zerodha" {
		t.Fatal("ping broker not decoded")
	}
	for _, payload := range []string{`{"status":"success","data":{}}`, `{"status":"success","data":{"broker":" "}}`, `invalid test-key-marker`, `{"status":"success","data":{"broker":42}}`} {
		client := testClient(t, 200, payload, "ping", nil)
		_, err := client.Ping(context.Background())
		if err == nil {
			t.Fatal("invalid ping accepted")
		}
		if !errors.Is(err, ErrInvalidResponse) {
			t.Fatal("invalid ping lost ErrInvalidResponse")
		}
		assertNoKey(t, err.Error())
	}
	client = testClient(t, 200, `{"status":"success","data":{"broker":"test-key-marker"}}`, "ping", nil)
	if broker, err := client.Ping(context.Background()); err != nil || broker != "[redacted]" {
		t.Fatal("broker display text not redacted")
	}
}

func TestAnalyzer(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		want       bool
		valid      bool
	}{
		{"live", `{"analyze_mode":false,"mode":"live"}`, false, true},
		{"analyze", `{"analyze_mode":true,"mode":"analyze"}`, true, true},
		{"missing_flag", `{"mode":"live"}`, false, false},
		{"null_flag", `{"analyze_mode":null,"mode":"live"}`, false, false},
		{"missing_mode", `{"analyze_mode":false}`, false, false},
		{"wrong_live", `{"analyze_mode":false,"mode":"analyze"}`, false, false},
		{"wrong_analyze", `{"analyze_mode":true,"mode":"live"}`, false, false},
		{"unknown", `{"analyze_mode":true,"mode":"test-key-marker"}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, 200, `{"status":"success","data":`+tc.data+`}`, "analyzer", nil)
			got, err := client.Analyzer(context.Background())
			if (err == nil) != tc.valid || got != tc.want {
				t.Fatal("analyzer mode accepted inconsistently or changed")
			}
			if err != nil {
				assertNoKey(t, err.Error())
				if !errors.Is(err, ErrInvalidResponse) {
					t.Fatal("invalid analyzer response lost ErrInvalidResponse")
				}
			}
		})
	}
}

func TestPlaceOrder(t *testing.T) {
	for _, tc := range []struct {
		name, price, payload string
		want                 PlaceOrderResult
	}{
		{"market", "", `{"status":"success","orderid":"order-1"}`, PlaceOrderResult{OrderID: "order-1"}},
		{"limit", "120.0500", `{"status":"success","orderid":"order-2","mode":"analyze"}`, PlaceOrderResult{OrderID: "order-2", Mode: "analyze"}},
		{"semi_auto", "", `{"status":"success","mode":"semi_auto","pending_order_id":12345678901234567890,"message":"queued test-key-marker"}`, PlaceOrderResult{Mode: "semi_auto", PendingOrderID: "12345678901234567890", Message: "queued [redacted]"}},
		{"uninterpreted", "", `{"status":"success","mode":"broker_extra"}`, PlaceOrderResult{Mode: "broker_extra"}},
		{"redacted", "", `{"status":"success","orderid":"order-test-key-marker","mode":"mode-test-key-marker","message":"message-test-key-marker"}`, PlaceOrderResult{OrderID: "order-[redacted]", Mode: "mode-[redacted]", Message: "message-[redacted]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order := PlaceOrderRequest{"officer", "RELIANCE", "BUY", "NSE", "MARKET", "CNC", "2", tc.price}
			fields := map[string]string{"strategy": order.Strategy, "symbol": order.Symbol, "action": order.Action, "exchange": order.Exchange, "pricetype": order.PriceType, "product": order.Product, "quantity": order.Quantity}
			if tc.price != "" {
				order.PriceType = "LIMIT"
				fields["pricetype"] = "LIMIT"
				fields["price"] = tc.price
			}
			client := testClient(t, 200, tc.payload, "placeorder", fields)
			got, err := client.PlaceOrder(context.Background(), order)
			if err != nil || got != tc.want {
				t.Fatal("order result interpreted or request/result changed")
			}
		})
	}
}

func TestOrderIdentifiers(t *testing.T) {
	for _, operation := range []struct {
		name, prefix, suffix string
		fields               map[string]string
		call                 func(*Client) (Identifier, error)
	}{
		{
			"placeorder", `{"status":"success"`, `}`,
			map[string]string{"strategy": "", "symbol": "", "action": "",
				"exchange": "", "pricetype": "", "product": "", "quantity": ""},
			func(c *Client) (Identifier, error) {
				result, err := c.PlaceOrder(context.Background(), PlaceOrderRequest{})
				return result.OrderID, err
			},
		},
		{
			"orderstatus", `{"status":"success","data":{"quantity":1`, `}}`,
			map[string]string{"orderid": "requested"},
			func(c *Client) (Identifier, error) {
				result, err := c.OrderStatus(context.Background(), "requested")
				return result.OrderID, err
			},
		},
		{
			"tradebook", `{"status":"success","data":[{"quantity":1`, `}]}`, nil,
			func(c *Client) (Identifier, error) {
				result, err := c.TradeBook(context.Background())
				if err != nil {
					return "", err
				}
				if len(result.Trades) != 1 {
					t.Fatal("trade row lost")
				}
				return result.Trades[0].OrderID, nil
			},
		},
	} {
		t.Run(operation.name, func(t *testing.T) {
			for _, tc := range []struct{ name, raw, want, kind string }{
				{"number", `12345678901234567890`, "12345678901234567890", ""},
				{"zero", `0`, "0", ""},
				{"string", `"0012345678901234567890"`, "0012345678901234567890", ""},
				{"empty_string", `""`, "", "empty string"},
				{"blank_string", `" "`, "", "empty string"},
				{"blank_unicode", `"\t\u00a0"`, "", "empty string"},
				{"null", `null`, "", ""},
				{"absent", ``, "", ""},
				{"fractional", `1.25`, "", "number"},
				{"exponent", `1e2`, "", "number"},
				{"negative", `-12`, "", "number"},
				{"boolean", `true`, "", "boolean"},
				{"object", `{"value":"test-key-marker"}`, "", "object"},
				{"array", `["test-key-marker"]`, "", "array"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					member := ""
					if tc.raw != "" {
						member = `,"orderid":` + tc.raw
					}
					client := testClient(t, 200, operation.prefix+member+operation.suffix,
						operation.name, operation.fields)
					got, err := operation.call(client)
					if tc.kind == "" {
						if err != nil || string(got) != tc.want {
							t.Fatal("identifier text changed or valid identifier refused")
						}
						return
					}
					if err == nil || !strings.Contains(err.Error(), "identifier "+tc.kind) {
						t.Fatal("invalid identifier did not report its JSON kind")
					}
					assertNoKey(t, err.Error())
					if strings.Contains(err.Error(), tc.raw) {
						t.Fatal("invalid identifier value exposed")
					}
				})
			}
		})
	}
}

func TestPendingOrderIdentifier(t *testing.T) {
	fields := map[string]string{"strategy": "", "symbol": "", "action": "",
		"exchange": "", "pricetype": "", "product": "", "quantity": ""}
	for _, tc := range []struct{ name, raw, want, errorText string }{
		{"number", `12345678901234567890`, "12345678901234567890", ""},
		{"string", `"00123"`, "00123", ""},
		{"positive_number", `12`, "12", ""},
		{"positive_string", `"12"`, "12", ""},
		{"negative_number", `-12`, "", "identifier number"},
		{"negative_string", `"-12"`, "", "pending_order_id must be an unsigned integer"},
		{"empty_string", `""`, "", "identifier empty string"},
		{"null", `null`, "", ""},
		{"absent", ``, "", ""},
		{"invalid_string", `"refused-pending-id"`, "", "pending_order_id must be an unsigned integer"},
		{"fractional_string", `"1.25"`, "", "pending_order_id must be an unsigned integer"},
		{"fractional", `1.25`, "", "identifier number"},
		{"boolean", `true`, "", "identifier boolean"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			member := ""
			if tc.raw != "" {
				member = `,"pending_order_id":` + tc.raw
			}
			client := testClient(t, 200, `{"status":"success"`+member+`}`,
				"placeorder", fields)
			got, err := client.PlaceOrder(context.Background(), PlaceOrderRequest{})
			if tc.errorText == "" {
				if err != nil || string(got.PendingOrderID) != tc.want {
					t.Fatal("pending identifier changed or refused")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.errorText) {
				t.Fatal("invalid pending identifier did not report field or kind")
			}
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatal("invalid pending identifier lost ErrInvalidResponse")
			}
			assertNoKey(t, err.Error())
			if strings.Contains(err.Error(), tc.raw) ||
				(tc.raw != `""` && strings.Contains(err.Error(), strings.Trim(tc.raw, `"`))) {
				t.Fatal("invalid pending identifier value exposed")
			}
		})
	}
}

func TestRequiredResponseData(t *testing.T) {
	for _, operation := range []struct {
		name   string
		fields map[string]string
		call   func(*Client) error
	}{
		{"orderstatus", map[string]string{"orderid": "requested"}, func(c *Client) error {
			_, err := c.OrderStatus(context.Background(), "requested")
			return err
		}},
		{"tradebook", nil, func(c *Client) error {
			_, err := c.TradeBook(context.Background())
			return err
		}},
		{"symbol", map[string]string{"symbol": "RELIANCE", "exchange": "NSE"}, func(c *Client) error {
			_, err := c.Symbol(context.Background(), "RELIANCE", "NSE")
			return err
		}},
		{"search", map[string]string{"query": "RELI", "exchange": "NSE"}, func(c *Client) error {
			_, err := c.Search(context.Background(), "RELI", "NSE")
			return err
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			for _, tc := range []struct{ name, payload string }{
				{"absent", `{"status":"success"}`},
				{"null", `{"status":"success","data":null}`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					client := testClient(t, 200, tc.payload, operation.name, operation.fields)
					if err := operation.call(client); !errors.Is(err, ErrInvalidResponse) ||
						!strings.Contains(err.Error(), "openalgo: "+operation.name+" data is required") {
						t.Fatal("missing data did not report the operation")
					}
				})
			}
		})
	}
	t.Run("empty_tradebook", func(t *testing.T) {
		client := testClient(t, 200, `{"status":"success","data":[]}`, "tradebook", nil)
		rows, err := client.TradeBook(context.Background())
		if err != nil || rows.Trades == nil || len(rows.Trades) != 0 {
			t.Fatal("empty tradebook refused or changed")
		}
	})
	t.Run("empty_search", func(t *testing.T) {
		client := testClient(t, 200, `{"status":"success","data":[]}`, "search",
			map[string]string{"query": "RELI", "exchange": "NSE"})
		rows, err := client.Search(context.Background(), "RELI", "NSE")
		if err != nil || rows == nil || len(rows) != 0 {
			t.Fatal("empty search refused or changed")
		}
	})
}

func TestNullResponseRows(t *testing.T) {
	for _, operation := range []struct {
		name   string
		fields map[string]string
		call   func(*Client) error
	}{
		{"tradebook", nil, func(c *Client) error {
			_, err := c.TradeBook(context.Background())
			return err
		}},
		{"search", map[string]string{"query": "RELI", "exchange": "NSE"}, func(c *Client) error {
			_, err := c.Search(context.Background(), "RELI", "NSE")
			return err
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			client := testClient(t, 200, `{"status":"success","data":[null]}`, operation.name, operation.fields)
			if err := operation.call(client); !errors.Is(err, ErrInvalidResponse) ||
				!strings.Contains(err.Error(), "openalgo: "+operation.name+" data contains null row") {
				t.Fatal("null row did not report its operation")
			}
		})
	}
}

func TestOrderStatusTradeBookAndSymbols(t *testing.T) {
	ctx := context.Background()
	client := testClient(t, 200, `{"status":"success","data":{"orderid":"order-test-key-marker","symbol":"RELIANCE-test-key-marker","exchange":"NSE-test-key-marker","action":"BUY-test-key-marker","quantity":"002","price":120.0500,"average_price":"120.0400","order_status":"expired-test-key-marker","ignored":true}}`, "orderstatus", map[string]string{"orderid": "order-1"})
	order, err := client.OrderStatus(ctx, "order-1")
	if err != nil || order != (OrderStatus{"", "order-[redacted]", "RELIANCE-[redacted]", "NSE-[redacted]", "BUY-[redacted]", "002", "120.0500", "120.0400", "expired-[redacted]"}) {
		t.Fatal("order status fields or decimals changed")
	}
	client = testClient(t, 200, `{"status":"success","data":[{"orderid":"o-test-key-marker","symbol":"RELIANCE-test-key-marker","exchange":"NSE-test-key-marker","action":"SELL-test-key-marker","quantity":2,"average_price":100.00},{"orderid":"o-test-key-marker","symbol":"RELIANCE-test-key-marker","exchange":"NSE-test-key-marker","action":"SELL-test-key-marker","quantity":1,"average_price":101.00}]}`, "tradebook", nil)
	trades, err := client.TradeBook(ctx)
	wantTrades := []Trade{{"o-[redacted]", "RELIANCE-[redacted]", "NSE-[redacted]", "SELL-[redacted]", "2", "100.00"}, {"o-[redacted]", "RELIANCE-[redacted]", "NSE-[redacted]", "SELL-[redacted]", "1", "101.00"}}
	if err != nil || !reflect.DeepEqual(trades.Trades, wantTrades) {
		t.Fatal("tradebook rows aggregated, reordered or changed")
	}
	symbolJSON := `{"symbol":"RELIANCE-test-key-marker","name":"Reliance test-key-marker","exchange":"NSE-test-key-marker","instrumenttype":"EQ-test-key-marker","lotsize":1,"tick_size":"0.0500","ignored":true}`
	wantSymbol := SymbolInfo{"RELIANCE-[redacted]", "Reliance [redacted]", "NSE-[redacted]", "EQ-[redacted]", "1", "0.0500"}
	client = testClient(t, 200, `{"status":"success","data":`+symbolJSON+`}`, "symbol", map[string]string{"symbol": "RELIANCE", "exchange": "NSE"})
	symbol, err := client.Symbol(ctx, "RELIANCE", "NSE")
	if err != nil || symbol != wantSymbol {
		t.Fatal("symbol metadata changed or venue name leaked")
	}
	client = testClient(t, 200, `{"status":"success","data":[`+symbolJSON+`]}`, "search", map[string]string{"query": "RELI", "exchange": "NSE"})
	symbols, err := client.Search(ctx, "RELI", "NSE")
	if err != nil || !reflect.DeepEqual(symbols, []SymbolInfo{wantSymbol}) {
		t.Fatal("search metadata changed")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOrderStatusAndTradeBookModes(t *testing.T) {
	for _, tc := range []struct{ name, member, want string }{
		{"absent", "", ""},
		{"analyzer", `,"mode":"analyze"`, "analyze"},
		{"redacted", `,"mode":"test-key-marker"`, "[redacted]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("orderstatus", func(t *testing.T) {
				body := `{"status":"success"` + tc.member + `,"data":{"orderid":"123","mode":"ignored"}}`
				client := testClient(t, 200, body, "orderstatus", map[string]string{"orderid": "123"})
				result, err := client.OrderStatus(context.Background(), "123")
				if err != nil || result.Mode != tc.want {
					t.Fatal("orderstatus did not preserve and redact the top-level mode")
				}
				assertNoKey(t, result.Mode)
			})
			t.Run("tradebook", func(t *testing.T) {
				body := `{"status":"success"` + tc.member + `,"data":[{"orderid":"123","mode":"ignored"}]}`
				client := testClient(t, 200, body, "tradebook", nil)
				result, err := client.TradeBook(context.Background())
				if err != nil || result.Mode != tc.want || len(result.Trades) != 1 {
					t.Fatal("tradebook did not preserve and redact the top-level mode")
				}
				assertNoKey(t, result.Mode)
			})
		})
	}
}

func TestTransportAndRequiredInputs(t *testing.T) {
	cause := errors.New("transport echoed " + testKey)
	client := NewClient("http://host", testKey, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, cause })}, testRedactor(t))
	_, err := client.Ping(context.Background())
	if err == nil || !errors.Is(err, cause) || errors.Is(err, ErrInvalidResponse) {
		t.Fatal("transport cause lost")
	}
	assertNoKey(t, err.Error())
	for _, c := range []*Client{
		NewClient("http://host", testKey, nil, testRedactor(t)),
		NewClient("http://host", testKey, &http.Client{}, nil),
		NewClient(":bad", testKey, &http.Client{}, testRedactor(t)),
	} {
		_, err := c.Ping(context.Background())
		if err == nil || errors.Is(err, ErrInvalidResponse) {
			t.Fatal("invalid request prerequisites accepted")
		}
		assertNoKey(t, err.Error())
	}
}

func TestInvalidResponseCause(t *testing.T) {
	client := testClient(t, 200, `{"status":"success","data":{"broker":42}}`, "ping", nil)
	_, err := client.Ping(context.Background())
	var typeErr *json.UnmarshalTypeError
	if !errors.Is(err, ErrInvalidResponse) || !errors.As(err, &typeErr) {
		t.Fatal("result decode failure lost its sentinel or cause")
	}
	assertNoKey(t, err.Error())
	if strings.Count(err.Error(), "openalgo:") != 1 {
		t.Fatal("invalid response has doubled vendor prefixes")
	}
	for _, payload := range []string{
		`{"status":"success","data":[{"orderid":" "}]}`,
		`{"status":"success","data":[{"quantity":1e2}]}`,
	} {
		client := testClient(t, 200, payload, "tradebook", nil)
		_, err := client.TradeBook(context.Background())
		if !errors.Is(err, ErrInvalidResponse) || strings.Count(err.Error(), "openalgo:") != 1 {
			t.Fatal("custom decode failure lost its sentinel or doubled the prefix")
		}
		assertNoKey(t, err.Error())
	}
}

func TestResponseFailuresWithoutInvalidResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = io.WriteString(w, `{"status":"success"}`)
	}))
	t.Cleanup(server.Close)
	client := NewClient(server.URL, testKey, server.Client(), testRedactor(t))
	t.Run("read", func(t *testing.T) {
		_, err := client.Ping(context.Background())
		if err == nil || errors.Is(err, ErrInvalidResponse) || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatal("read failure lost its cause or became an invalid response")
		}
		assertNoKey(t, err.Error())
	})
	server.Close()
	t.Run("dial", func(t *testing.T) {
		_, err := client.Ping(context.Background())
		if err == nil || errors.Is(err, ErrInvalidResponse) {
			t.Fatal("dial failure became an invalid response")
		}
		assertNoKey(t, err.Error())
	})
}

func TestHTTPRedirectRefused(t *testing.T) {
	var reached atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	original := server.Client()
	client := NewClient(server.URL, testKey, original, testRedactor(t))
	_, err := client.Ping(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTemporaryRedirect || reached.Load() != 0 {
		t.Fatal("REST redirect followed or status lost")
	}
	if original.CheckRedirect != nil {
		t.Fatal("caller's HTTP client was modified")
	}
}
