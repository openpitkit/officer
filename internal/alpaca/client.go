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

// Package alpaca provides authenticated REST access and asset discovery.
package alpaca

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// PaperRESTURL and LiveRESTURL identify the separate Alpaca environments.
const (
	PaperRESTURL = "https://paper-api.alpaca.markets"
	LiveRESTURL  = "https://api.alpaca.markets"
)

// Credentials identifies one account in one environment.
type Credentials struct{ APIKey, APISecret string }

// ParseCredentials accepts only a JSON object with apiKey and apiSecret.
// Errors never include input values or unknown field names.
func ParseCredentials(raw string) (Credentials, error) {
	var fields map[string]string
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return Credentials{}, errors.New("alpaca: invalid credentials JSON")
	}
	for field := range fields {
		if field != "apiKey" && field != "apiSecret" {
			return Credentials{}, errors.New("alpaca: unknown credentials field")
		}
	}
	if strings.TrimSpace(fields["apiKey"]) == "" || strings.TrimSpace(fields["apiSecret"]) == "" {
		return Credentials{}, errors.New("alpaca: apiKey and apiSecret are required")
	}
	return Credentials{APIKey: fields["apiKey"], APISecret: fields["apiSecret"]}, nil
}

// APIError is an HTTP refusal. Code and Message are empty for other body shapes.
type APIError struct {
	Status  int
	Code    int
	Message string
	// BodyDecoded reports whether both code and message were decoded.
	BodyDecoded bool
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("alpaca: HTTP %d", e.Status)
	}
	return fmt.Sprintf("alpaca: HTTP %d (code %d): %s", e.Status, e.Code, e.Message)
}

// Client owns account credentials and uses the supplied HTTP transport.
type Client struct {
	baseURL     string
	credentials Credentials
	httpClient  *http.Client
}

// NewClient binds REST requests to one host. Redirects are refused so auth
// headers cannot be forwarded to a host chosen by a response.
func NewClient(baseURL string, credentials Credentials, httpClient *http.Client) *Client {
	if httpClient != nil {
		copy := *httpClient
		copy.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		httpClient = &copy
	}
	return &Client{strings.TrimRight(baseURL, "/"), credentials, httpClient}
}

type requestError struct {
	message string
	cause   error
}

func (e *requestError) Error() string { return e.message }
func (e *requestError) Unwrap() error { return e.cause }

func (c *Client) redact(message string) string {
	for _, value := range []string{c.credentials.APISecret, c.credentials.APIKey} {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[redacted]")
		}
	}
	return message
}

func (c *Client) wrap(operation string, err error) error {
	return &requestError{c.redact("alpaca: " + operation + ": " + err.Error()), err}
}

// Request exchanges JSON at a host-relative path (including its query).
// It returns the HTTP status even if response decoding fails. Non-2xx replies
// produce APIError; transport errors retain their cause without exposing keys.
func (c *Client) Request(ctx context.Context, method, path string, body, result any) (int, error) {
	if c.httpClient == nil {
		return 0, errors.New("alpaca: HTTP client is required")
	}
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return 0, c.wrap("encode request", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return 0, c.wrap("create request", err)
	}
	req.Header.Set("APCA-API-KEY-ID", c.credentials.APIKey)
	req.Header.Set("APCA-API-SECRET-KEY", c.credentials.APISecret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, c.wrap("request", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp.StatusCode, c.wrap("read error response", err)
		}
		apiErr := &APIError{Status: resp.StatusCode}
		var fields struct {
			Code    *int    `json:"code"`
			Message *string `json:"message"`
		}
		if json.Unmarshal(payload, &fields) == nil && fields.Code != nil && fields.Message != nil {
			apiErr.Code, apiErr.Message = *fields.Code, c.redact(*fields.Message)
			apiErr.BodyDecoded = true
		}
		return resp.StatusCode, apiErr
	}
	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return resp.StatusCode, c.wrap("decode response", err)
		}
	}
	return resp.StatusCode, nil
}

// Asset is Alpaca's asset metadata; venue decimal rules remain exact strings.
type Asset struct {
	ID                string `json:"id"`
	Class             string `json:"class"`
	Exchange          string `json:"exchange"`
	Symbol            string `json:"symbol"`
	Name              string `json:"name"`
	Status            string `json:"status"`
	Tradable          bool   `json:"tradable"`
	Fractionable      bool   `json:"fractionable"`
	MinOrderSize      string `json:"min_order_size"`
	MinTradeIncrement string `json:"min_trade_increment"`
	PriceIncrement    string `json:"price_increment"`
}

// Asset reports found=false for unknown symbols, including crypto pairs.
func (c *Client) Asset(ctx context.Context, symbol string) (Asset, bool, error) {
	var asset Asset
	_, err := c.Request(ctx, http.MethodGet, "/v2/assets/"+url.PathEscape(symbol), nil, &asset)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return Asset{}, false, nil
	}
	if err != nil {
		return Asset{}, false, err
	}
	return asset, true, nil
}
