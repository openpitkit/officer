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

// Package alpaca provides authenticated REST access and asset discovery, and
// opens authenticated streams.
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

	"go.openpit.dev/officer/framework/domain"
)

// PaperRESTURL and LiveRESTURL are the trading hosts of the two separate
// Alpaca environments, paper and live. DataURL is the market data host, which
// paper and live keys both read from.
const (
	PaperRESTURL = "https://paper-api.alpaca.markets"
	LiveRESTURL  = "https://api.alpaca.markets"
	DataURL      = "https://data.alpaca.markets"
)

// Credentials identifies one account in one environment.
type Credentials struct{ APIKey, APISecret string }

// ParseCredentials accepts only a JSON object with apiKey and apiSecret, each
// non-blank after trimming. Errors are domain validation errors whose JSON
// pointer is relative to the credentials object, and they never include input
// values or unknown field names.
func ParseCredentials(raw string) (Credentials, error) {
	var fields map[string]string
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return Credentials{}, domain.NewValidationError("", "type", "alpaca: invalid credentials JSON")
	}
	for field := range fields {
		if field != "apiKey" && field != "apiSecret" {
			return Credentials{}, domain.NewValidationError("", "unknown_field", "alpaca: unknown credentials field")
		}
	}
	apiKey := strings.TrimSpace(fields["apiKey"])
	if apiKey == "" {
		return Credentials{}, domain.NewValidationError("/apiKey", "required", "alpaca: apiKey is required")
	}
	apiSecret := strings.TrimSpace(fields["apiSecret"])
	if apiSecret == "" {
		return Credentials{}, domain.NewValidationError("/apiSecret", "required", "alpaca: apiSecret is required")
	}
	return Credentials{APIKey: apiKey, APISecret: apiSecret}, nil
}

// APIError is an HTTP refusal. Message is the body's message and Code its
// numeric code; the trading host sends both, the data host sends a message
// alone. Both stay empty for other body shapes.
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
	if !e.BodyDecoded {
		return fmt.Sprintf("alpaca: HTTP %d: %s", e.Status, e.Message)
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
		copy.CheckRedirect = refuseRedirect
		httpClient = &copy
	}
	return &Client{strings.TrimRight(baseURL, "/"), credentials, httpClient}
}

// refuseRedirect is the package's one redirect policy, for REST and streams:
// a redirect response is returned as it is and never followed.
func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

type requestError struct {
	message string
	cause   error
}

func (e *requestError) Error() string { return e.message }
func (e *requestError) Unwrap() error { return e.cause }

func (c *Client) redact(message string) string {
	values := []string{c.credentials.APIKey, c.credentials.APISecret}
	if len(values[0]) < len(values[1]) {
		values[0], values[1] = values[1], values[0]
	}
	replacements := make([]string, 0, 4)
	for _, value := range values {
		if value != "" {
			replacements = append(replacements, value, "[redacted]")
		}
	}
	return strings.NewReplacer(replacements...).Replace(message)
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
		if json.Unmarshal(payload, &fields) == nil && fields.Message != nil {
			apiErr.Message = c.redact(*fields.Message)
			if fields.Code != nil {
				apiErr.Code, apiErr.BodyDecoded = *fields.Code, true
			}
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

// Account is the venue account reached by the client's credentials.
type Account struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Account reads the account identity and status. A missing id is an error.
func (c *Client) Account(ctx context.Context) (Account, error) {
	var account Account
	_, err := c.Request(ctx, http.MethodGet, "/v2/account", nil, &account)
	if err != nil {
		return Account{}, err
	}
	if account.ID == "" {
		return Account{}, errors.New("alpaca: account id is missing")
	}
	return account, nil
}

// Assets reads active US equities and crypto pairs with separate /v2/assets
// requests for asset_class=us_equity and asset_class=crypto. A null list is
// an error.
func (c *Client) Assets(ctx context.Context) ([]Asset, error) {
	assets := make([]Asset, 0)
	for _, class := range []string{"us_equity", "crypto"} {
		var classAssets []Asset
		_, err := c.Request(ctx, http.MethodGet, "/v2/assets?status=active&asset_class="+class, nil, &classAssets)
		if err != nil {
			return nil, err
		}
		if classAssets == nil {
			return nil, errors.New("alpaca: assets list is missing")
		}
		assets = append(assets, classAssets...)
	}
	return assets, nil
}

// LatestTradeSymbols asks the data host for the latest trade of symbol on a
// stock feed and returns the symbols it answered for, spelled as the host
// spells them. The set is empty when the feed has no trade for symbol, or when
// the host refuses it with HTTP 400 as an invalid symbol. Any other refusal or
// failure is returned as Request produced it, and a reply without a trades
// object is an error.
func (c *Client) LatestTradeSymbols(ctx context.Context, symbol, feed string) (map[string]struct{}, error) {
	query := url.Values{"symbols": {symbol}, "feed": {feed}}
	var reply struct {
		// Trades is keyed by symbol; the trades themselves are not read.
		Trades map[string]struct{} `json:"trades"`
	}
	_, err := c.Request(ctx, http.MethodGet, "/v2/stocks/trades/latest?"+query.Encode(), nil, &reply)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest &&
		strings.Contains(apiErr.Message, "invalid symbol") {
		return map[string]struct{}{}, nil
	}
	if err != nil {
		return nil, err
	}
	if reply.Trades == nil {
		return nil, errors.New("alpaca: latest trades are missing")
	}
	return reply.Trades, nil
}
