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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.openpit.dev/officer/internal/apikey"
	"go.openpit.dev/officer/internal/vendorhttp"
)

// TestnetRESTURL is Bybit Global's testnet REST endpoint.
const TestnetRESTURL = "https://api-testnet.bybit.com"

// EUTestnetRESTURL is Bybit EU's testnet REST endpoint.
const EUTestnetRESTURL = "https://api-testnet.bybit.eu"

const recvWindow = "5000"

// Credentials holds an API key and secret; neither value has a default.
type Credentials = apikey.Credentials

// ParseCredentials parses the stored Bybit credential object and its site.
func ParseCredentials(raw string) (Credentials, string, error) {
	return apikey.ParseWithOptionalField(raw, "bybit", "site")
}

// APIError reports an HTTP or venue refusal without retaining its body.
type APIError struct {
	Status  int
	RetCode int
	Message string
	// BodyDecoded reports a decoded venue refusal. For non-2xx responses,
	// both retCode and retMsg must be present.
	BodyDecoded bool
}

func (e *APIError) Error() string {
	if e.BodyDecoded {
		message := fmt.Sprintf("bybit: HTTP %d (retCode %d)", e.Status, e.RetCode)
		if e.Message != "" {
			message += ": " + e.Message
		}
		return message
	}
	if e.Message == "" {
		return fmt.Sprintf("bybit: HTTP %d", e.Status)
	}
	return fmt.Sprintf("bybit: HTTP %d: %s", e.Status, e.Message)
}

// Client sends signed REST requests using the caller's host and credentials.
type Client struct {
	baseURL     string
	credentials Credentials
	redact      *strings.Replacer
	httpClient  *http.Client
	now         func() time.Time
}

// NewClient copies the HTTP client and refuses redirects. A nil HTTP client
// or credential redactor causes requests to fail before any network access.
func NewClient(baseURL string, credentials Credentials, redact *strings.Replacer, httpClient *http.Client) *Client {
	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		credentials: credentials,
		redact:      redact,
		httpClient:  vendorhttp.CopyClient(httpClient),
		now:         time.Now,
	}
}

// Get signs and sends the exact encoded query without modifying params.
func (c *Client) Get(ctx context.Context, path string, params url.Values, result any) (int, error) {
	return c.request(ctx, http.MethodGet, path, params.Encode(), result)
}

// Post marshals body once, then signs and sends those exact JSON bytes.
func (c *Client) Post(ctx context.Context, path string, body any, result any) (int, error) {
	if err := c.validate(); err != nil {
		return 0, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, vendorhttp.Wrap(c.redact, "bybit", "encode request", err)
	}
	return c.request(ctx, http.MethodPost, path, string(payload), result)
}

func (c *Client) validate() error {
	if c.httpClient == nil {
		return errors.New("bybit: HTTP client is required")
	}
	if c.redact == nil {
		return errors.New("bybit: credential redactor is required")
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, path, payload string, result any) (int, error) {
	if err := c.validate(); err != nil {
		return 0, err
	}
	timestamp := strconv.FormatInt(c.now().UnixMilli(), 10)
	signature := vendorhttp.SignHex(timestamp+c.credentials.APIKey+recvWindow+payload, c.credentials.APISecret)
	endpoint := c.baseURL + path
	var body io.Reader
	if method == http.MethodGet {
		endpoint += "?" + payload
	} else {
		body = bytes.NewBufferString(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, vendorhttp.Wrap(c.redact, "bybit", "create request", err)
	}
	req.Header.Set("X-BAPI-API-KEY", c.credentials.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN", signature)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, vendorhttp.Wrap(c.redact, "bybit", "request", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("bybit: close response body", "error", vendorhttp.Wrap(c.redact, "bybit", "close response", err))
		}
	}()
	encoded, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, vendorhttp.Wrap(c.redact, "bybit", "read response", err)
	}
	var envelope struct {
		RetCode *int            `json:"retCode"`
		RetMsg  *string         `json:"retMsg"`
		Result  json.RawMessage `json:"result"`
	}
	decodeErr := json.Unmarshal(encoded, &envelope)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || (decodeErr == nil && envelope.RetCode != nil && *envelope.RetCode != 0) {
		apiErr := &APIError{Status: resp.StatusCode}
		if decodeErr == nil {
			if envelope.RetMsg != nil {
				apiErr.Message = c.redact.Replace(*envelope.RetMsg)
			}
			if envelope.RetCode != nil {
				apiErr.RetCode = *envelope.RetCode
				apiErr.BodyDecoded = envelope.RetMsg != nil || (resp.StatusCode >= 200 && resp.StatusCode < 300)
			}
		}
		return resp.StatusCode, apiErr
	}
	if decodeErr != nil {
		return resp.StatusCode, vendorhttp.Wrap(c.redact, "bybit", "decode response", decodeErr)
	}
	if envelope.RetCode == nil {
		return resp.StatusCode, vendorhttp.Wrap(c.redact, "bybit", "decode response", errors.New("retCode is required"))
	}
	if result != nil {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return resp.StatusCode, vendorhttp.Wrap(c.redact, "bybit", "decode result", err)
		}
	}
	return resp.StatusCode, nil
}
