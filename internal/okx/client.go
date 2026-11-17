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
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/internal/apikey"
	"go.openpit.dev/officer/internal/vendorhttp"
)

const (
	// GlobalSite is the OKX registration site for global accounts.
	GlobalSite = "www.okx.com"
	// EEASite is the OKX registration site for EEA accounts.
	EEASite = "my.okx.com"
	// USSite is the OKX registration site for US and Australian accounts.
	USSite = "app.okx.com"
)

// Endpoints selects REST and private stream addresses for a registration site.
// Demo trading uses the same REST address with an extra request header.
// Sources: https://www.okx.com/docs-v5/en/, https://my.okx.com/docs-v5/en/, https://app.okx.com/docs-v5/en/.
func Endpoints(site string, demo bool) (rest, private string, err error) {
	switch site {
	case GlobalSite:
		rest, private = "https://openapi.okx.com", "wss://ws.okx.com/ws/v5/private"
		if demo {
			private = "wss://wspap.okx.com/ws/v5/private"
		}
	case EEASite:
		rest, private = "https://eea.okx.com", "wss://wseea.okx.com/ws/v5/private"
		if demo {
			private = "wss://wseeapap.okx.com/ws/v5/private"
		}
	case USSite:
		rest, private = "https://us.okx.com", "wss://wsus.okx.com/ws/v5/private"
		if demo {
			private = "wss://wsuspap.okx.com/ws/v5/private"
		}
	default:
		return "", "", domain.NewValidationError("/site", "invalid",
			"okx: site is not a registration site")
	}
	return rest, private, nil
}

// Credentials keeps the required registration site separate from the secrets.
type Credentials struct {
	apikey.PassphraseCredentials
	Site string
}

// ParseCredentials accepts only apiKey, apiSecret, passphrase and site.
// Each member is required and non-blank after trimming; site is the account's
// OKX registration site and has no default. Validation pointers are relative
// to the credentials object. Errors never echo member values.
func ParseCredentials(raw string) (Credentials, error) {
	credentials, fields, err := apikey.ParseWithPassphrase(raw, "okx", "site")
	if err != nil {
		return Credentials{}, err
	}
	site := fields[0]
	if _, _, err := Endpoints(site, false); err != nil {
		return Credentials{}, err
	}
	return Credentials{PassphraseCredentials: credentials, Site: site}, nil
}

// ItemError contains an order row's status, without retaining its other data.
type ItemError struct {
	Code    string `json:"sCode"`
	Message string `json:"sMsg"`
}

// APIError carries only the HTTP status and redacted OKX error fields.
type APIError struct {
	Status  int
	Code    string
	Message string
	Items   []ItemError
}

func (e *APIError) Error() string {
	message := fmt.Sprintf("okx: HTTP %d", e.Status)
	if e.Code != "" {
		message += ", code " + e.Code
	}
	if e.Message != "" {
		message += ": " + e.Message
	}
	for _, item := range e.Items {
		if item.Code == "" && item.Message == "" {
			continue
		}
		message += "; "
		if item.Code != "" {
			message += "code " + item.Code
		}
		if item.Message != "" {
			if item.Code != "" {
				message += ": "
			}
			message += item.Message
		}
	}
	return message
}

// Client signs requests for one connection.
// The caller owns response data types.
type Client struct {
	baseURL     string
	credentials Credentials
	redact      *strings.Replacer
	httpClient  *http.Client
	now         func() time.Time
}

// NewClient copies the supplied HTTP client and refuses redirects. A missing
// client or connection redactor is reported when a request is attempted.
// For demo trading, wrap the HTTP client with DemoHTTPClient before calling.
func NewClient(baseURL string, credentials Credentials,
	redact *strings.Replacer, httpClient *http.Client,
) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), credentials: credentials,
		redact: redact, httpClient: vendorhttp.CopyClient(httpClient),
		now: time.Now,
	}
}

func sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func (c *Client) redactSigned(value, signature string) string {
	if signature != "" {
		value = strings.ReplaceAll(value, signature, "[redacted]")
	}
	return c.redact.Replace(value)
}

// Signed sends JSON using the exact path and query in the signature. A nil body
// sends no body. result receives the envelope's data, not the whole envelope.
// DemoHTTPClient supplies the demo header through the HTTP transport.
func (c *Client) Signed(ctx context.Context, method, path string, body, result any) (int, error) {
	if c.httpClient == nil {
		return 0, errors.New("okx: HTTP client is required")
	}
	if c.redact == nil {
		return 0, errors.New("okx: credential redactor is required")
	}
	if c.baseURL == "" {
		return 0, errors.New("okx: REST base URL is required")
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return 0, errors.New("okx: request path must be absolute within the REST host")
	}
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return 0, vendorhttp.Wrap(c.redact, "okx", "encode request", err)
		}
	}
	method = strings.ToUpper(method)
	if method == "" {
		return 0, errors.New("okx: request method is required")
	}
	timestamp := c.now().UTC().Format("2006-01-02T15:04:05.000Z")
	signature := sign(timestamp+method+path+string(encoded), c.credentials.APISecret)
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return 0, vendorhttp.Wrap(c.redact, "okx", "create request", err)
	}
	if req.URL.RequestURI() != path || req.URL.Fragment != "" {
		return 0, errors.New("okx: request path must contain only an encoded path and query")
	}
	req.Header.Set("OK-ACCESS-KEY", c.credentials.APIKey)
	req.Header.Set("OK-ACCESS-SIGN", signature)
	req.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
	req.Header.Set("OK-ACCESS-PASSPHRASE", c.credentials.Passphrase)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, vendorhttp.Wrap(c.redact, "okx", "request", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("okx: close response body", "error",
				vendorhttp.Wrap(c.redact, "okx", "close response", err))
		}
	}()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, vendorhttp.Wrap(c.redact, "okx", "read response", err)
	}
	var envelope struct {
		Code    string          `json:"code"`
		Message string          `json:"msg"`
		Data    json.RawMessage `json:"data"`
	}
	decodeErr := json.Unmarshal(payload, &envelope)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 ||
		(decodeErr == nil && envelope.Code != "0") {
		apiErr := &APIError{Status: resp.StatusCode}
		if decodeErr == nil {
			apiErr.Code = c.redactSigned(envelope.Code, signature)
			apiErr.Message = c.redactSigned(envelope.Message, signature)
			var rows []json.RawMessage
			if json.Unmarshal(envelope.Data, &rows) == nil {
				for _, row := range rows {
					var item ItemError
					if json.Unmarshal(row, &item) == nil &&
						(item.Code != "" || item.Message != "") {
						item.Code = c.redactSigned(item.Code, signature)
						item.Message = c.redactSigned(item.Message, signature)
						apiErr.Items = append(apiErr.Items, item)
					}
				}
			}
		}
		return resp.StatusCode, apiErr
	}
	if decodeErr != nil {
		return resp.StatusCode, vendorhttp.Wrap(c.redact, "okx", "decode response", decodeErr)
	}
	if result != nil {
		if err := json.Unmarshal(envelope.Data, result); err != nil {
			return resp.StatusCode, vendorhttp.Wrap(c.redact, "okx", "decode data", err)
		}
	}
	return resp.StatusCode, nil
}

// AccountUID retrieves only the account's exact string identifier.
func (c *Client) AccountUID(ctx context.Context) (string, error) {
	var rows []struct {
		UID string `json:"uid"`
	}
	if _, err := c.Signed(ctx, http.MethodGet, "/api/v5/account/config", nil, &rows); err != nil {
		return "", err
	}
	if len(rows) != 1 || rows[0].UID == "" {
		return "", errors.New("okx: account uid is missing or ambiguous")
	}
	return rows[0].UID, nil
}
