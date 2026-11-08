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

// Package binance provides exchange information, signed REST requests and
// WebSocket API user data subscriptions.
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
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.openpit.dev/officer/internal/apikey"
)

const (
	// TestnetRESTURL is Binance's spot testnet REST endpoint.
	TestnetRESTURL = "https://testnet.binance.vision"
	// LiveRESTURL is Binance's live spot REST endpoint.
	LiveRESTURL = "https://api.binance.com"
	// TestnetStreamURL is Binance's spot testnet WebSocket API endpoint.
	TestnetStreamURL = "wss://ws-api.testnet.binance.vision/ws-api/v3"
	// LiveStreamURL is Binance's live spot WebSocket API endpoint.
	LiveStreamURL = "wss://ws-api.binance.com:443/ws-api/v3"
)

// Credentials holds the HMAC key pair for a Binance connection.
type Credentials = apikey.Credentials

// ParseCredentials accepts only apiKey and apiSecret JSON string members.
// Both values are trimmed and required. Validation pointers are relative to
// the credentials object; errors never echo values or unknown member names.
func ParseCredentials(raw string) (Credentials, error) {
	return apikey.Parse(raw, "binance")
}

// APIError reports an HTTP refusal without retaining the response body.
type APIError struct {
	Status  int
	Code    int
	Message string
	// BodyDecoded reports whether both code and msg were decoded.
	BodyDecoded bool
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("binance: HTTP %d", e.Status)
	}
	if !e.BodyDecoded {
		return fmt.Sprintf("binance: HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("binance: HTTP %d (code %d): %s",
		e.Status, e.Code, e.Message)
}

// Client sends signed requests without following redirects.
type Client struct {
	baseURL     string
	credentials Credentials
	redact      *strings.Replacer
	httpClient  *http.Client
	now         func() time.Time
}

// NewClient copies httpClient and refuses redirects on the copy. Both
// httpClient and the connection's secret.CredentialRedactor replacer are
// required by Signed; neither has a default.
func NewClient(baseURL string, credentials Credentials,
	redact *strings.Replacer, httpClient *http.Client,
) *Client {
	if httpClient != nil {
		copy := *httpClient
		copy.CheckRedirect = refuseRedirect
		httpClient = &copy
	}
	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		credentials: credentials,
		redact:      redact,
		httpClient:  httpClient,
		now:         time.Now,
	}
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

type requestError struct {
	message string
	cause   error
}

func (e *requestError) Error() string { return e.message }
func (e *requestError) Unwrap() error { return e.cause }

func (c *Client) wrap(operation string, err error,
	signedRedactor *strings.Replacer,
) error {
	inner := err
	var urlErr *url.Error
	for errors.As(inner, &urlErr) {
		inner = urlErr.Err
	}
	raw := inner.Error()
	message := signedRedactor.Replace(raw)
	causeText := c.redact.Replace(message)
	if causeText != raw {
		inner = errors.New(causeText)
	}
	return &requestError{
		message: c.redact.Replace("binance: " + operation + ": " + message),
		cause:   inner,
	}
}

// Signed sends all parameters as an HMAC-SHA256 signed query, with an empty
// body and an API key header. It copies params, adds a millisecond timestamp
// and leaves recvWindow to the venue. The status is returned for responses,
// including refusals and decode failures. Error text redacts credentials,
// the signed query and its signature; wrapped causes omit every *url.Error
// layer while preserving errors for Is/As when their text needs no redaction.
// No error in the returned chain renders the signed query, the signature or
// a credential.
func (c *Client) Signed(ctx context.Context, method, path string,
	params url.Values, result any,
) (int, error) {
	if c.httpClient == nil {
		return 0, errors.New("binance: HTTP client is required")
	}
	if c.redact == nil {
		return 0, errors.New("binance: credential redactor is required")
	}
	query := make(url.Values, len(params)+1)
	for name, values := range params {
		query[name] = append([]string(nil), values...)
	}
	query.Set("timestamp", strconv.FormatInt(c.now().UnixMilli(), 10))
	payload := query.Encode()
	signature := sign(payload, c.credentials.APISecret)
	signedQuery := payload + "&signature=" + signature
	signedRedactor := strings.NewReplacer(
		signedQuery, "[redacted]", signature, "[redacted]",
	)
	endpoint := c.baseURL + path + "?" + signedQuery
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return 0, c.wrap("create request", err, signedRedactor)
	}
	req.Header.Set("X-MBX-APIKEY", c.credentials.APIKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, c.wrap("request", err, signedRedactor)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("binance: close response body", "error", c.wrap("close response", err, signedRedactor))
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp.StatusCode, c.wrap("read error response", err,
				signedRedactor)
		}
		apiErr := &APIError{Status: resp.StatusCode}
		var fields struct {
			Code    *int    `json:"code"`
			Message *string `json:"msg"`
		}
		if json.Unmarshal(payload, &fields) == nil {
			if fields.Message != nil {
				message := signedRedactor.Replace(*fields.Message)
				apiErr.Message = c.redact.Replace(message)
			}
			if fields.Code != nil {
				apiErr.Code = *fields.Code
				apiErr.BodyDecoded = fields.Message != nil
			}
		}
		return resp.StatusCode, apiErr
	}
	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return resp.StatusCode, c.wrap("decode response", err,
				signedRedactor)
		}
	}
	return resp.StatusCode, nil
}

// Account is the identity and trading permission of a Binance spot account.
// UID retains the exact decimal digits; balances are not read.
type Account struct {
	UID         json.Number `json:"uid"`
	AccountType string      `json:"accountType"`
	CanTrade    bool        `json:"canTrade"`
}

// Account reads the signed spot account identity and trading permission.
func (c *Client) Account(ctx context.Context) (Account, error) {
	var account Account
	params := url.Values{"omitZeroBalances": {"true"}}
	_, err := c.Signed(ctx, http.MethodGet, "/api/v3/account",
		params, &account)
	if err != nil {
		return Account{}, err
	}
	if account.UID == "" {
		return Account{}, errors.New("binance: account uid is missing")
	}
	return account, nil
}
