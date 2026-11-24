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

// Package coinbase provides signed REST requests and WebSocket subscriptions.
package coinbase

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"go.openpit.dev/officer/internal/apikey"
	"go.openpit.dev/officer/internal/vendorhttp"
)

const (
	// LiveRESTURL is Coinbase's live Advanced Trade REST endpoint.
	LiveRESTURL = "https://api.coinbase.com"
	// LiveStreamURL is Coinbase's live user order WebSocket endpoint.
	LiveStreamURL = "wss://advanced-trade-ws-user.coinbase.com"

	maxResponseBytes = 8 << 20
)

// ErrUnparsableResponse means a successful HTTP response could not be decoded.
var ErrUnparsableResponse = errors.New("coinbase: unparsable response")

var jwtPattern = regexp.MustCompile(`eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)

// Credentials holds the CDP API key ID and ECDSA P-256 private key.
type Credentials = apikey.CDPCredentials

// ParseCredentials accepts only apiKeyId and apiKeySecret JSON string members.
// Both are trimmed and required. apiKeySecret must contain one unencrypted SEC1
// EC PRIVATE KEY PEM block on P-256, with line breaks or with the \n escapes
// the Coinbase key dialog shows. Validation pointers are relative to the
// credentials object; errors never echo values or unknown member names.
func ParseCredentials(raw string) (Credentials, error) {
	return apikey.ParseCDP(raw, "coinbase")
}

// APIError reports a non-2xx response with scrubbed Coinbase error fields.
// It never retains the raw response body.
type APIError struct {
	// Status is the HTTP response status.
	Status int
	// Code is Coinbase's string error identifier from the error member.
	Code string
	// Message is Coinbase's explanation of the failure.
	Message string
	// ErrorDetails is the additional error_details text, when supplied.
	ErrorDetails string
	// PreviewFailureReason is the preview_failure_reason, when supplied.
	PreviewFailureReason string
	// BodyDecoded reports whether error and message were both JSON strings.
	BodyDecoded bool
}

// Error returns the HTTP status and any scrubbed Coinbase error fields.
func (e *APIError) Error() string {
	parts := []string{fmt.Sprintf("coinbase: HTTP %d", e.Status)}
	for _, field := range []string{e.Code, e.Message, e.ErrorDetails, e.PreviewFailureReason} {
		if field != "" {
			parts = append(parts, field)
		}
	}
	return strings.Join(parts, ": ")
}

// Client signs each request independently. The supplied client is copied and
// redirects are refused; its transport and TLS policy are preserved.
type Client struct {
	baseURL     string
	credentials Credentials
	redact      *strings.Replacer
	httpClient  *http.Client
	now         func() time.Time
	nonce       io.Reader
}

// NewClient copies httpClient with redirects refused, preserving its transport
// and TLS policy. redact must come from the stored credentials via
// secret.CredentialRedactor; both it and httpClient are required by Do.
func NewClient(baseURL string, credentials Credentials,
	redact *strings.Replacer, httpClient *http.Client,
) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), credentials: credentials,
		redact: redact, httpClient: vendorhttp.CopyClient(httpClient),
		now: time.Now, nonce: rand.Reader,
	}
}

func (c *Client) wrap(operation string, err error) error {
	message := c.Scrub(err.Error())
	// Flatten the cause: an inner layer can expose a secret absent from Error().
	return vendorhttp.NewRequestError("coinbase: "+operation+": "+message,
		errors.New(message))
}

// Scrub removes connection credentials and per-request JWTs from text.
func (c *Client) Scrub(text string) string {
	return c.redact.Replace(jwtPattern.ReplaceAllString(text, "[redacted]"))
}

// Do sends a request with a fresh JWT whose uri excludes the query string.
// A 2xx response returns nil after decoding into out, when out is non-nil.
// An oversized 2xx body or a decode failure wraps ErrUnparsableResponse.
// Non-2xx responses return *APIError; an oversized body leaves only Status set.
// Other errors are transport, response-read or local failures.
// Callers read cancellation from their own context. Response reads are bounded.
func (c *Client) Do(ctx context.Context, method, path string,
	query url.Values, body, out any,
) (int, error) {
	if c.httpClient == nil {
		return 0, errors.New("coinbase: HTTP client is required")
	}
	if c.redact == nil {
		return 0, errors.New("coinbase: credential redactor is required")
	}
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return 0, c.wrap("encode request", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path,
		bytes.NewReader(encoded))
	if err != nil {
		return 0, c.wrap("create request", err)
	}
	req.URL.RawQuery = query.Encode()
	token, err := mintJWT(c.credentials, req.Method+" "+req.URL.Host+
		req.URL.EscapedPath(), c.now(), c.nonce)
	if err != nil {
		return 0, c.wrap("sign request", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, c.wrap("request", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("coinbase: close response body", "error",
				c.wrap("close response", err))
		}
	}()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, c.wrap("read response", err)
	}
	if len(payload) > maxResponseBytes {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return resp.StatusCode, &APIError{Status: resp.StatusCode}
		}
		return resp.StatusCode, fmt.Errorf("%w: response exceeds size limit",
			ErrUnparsableResponse)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode}
		var fields struct {
			Code                 *string `json:"error"`
			Message              *string `json:"message"`
			ErrorDetails         *string `json:"error_details"`
			PreviewFailureReason *string `json:"preview_failure_reason"`
		}
		if json.Unmarshal(payload, &fields) == nil {
			apiErr.BodyDecoded = fields.Code != nil && fields.Message != nil
			if fields.Code != nil {
				apiErr.Code = c.Scrub(*fields.Code)
			}
			if fields.Message != nil {
				apiErr.Message = c.Scrub(*fields.Message)
			}
			if fields.ErrorDetails != nil {
				apiErr.ErrorDetails = c.Scrub(*fields.ErrorDetails)
			}
			if fields.PreviewFailureReason != nil {
				apiErr.PreviewFailureReason = c.Scrub(*fields.PreviewFailureReason)
			}
		}
		return resp.StatusCode, apiErr
	}
	if out != nil {
		if err := json.Unmarshal(payload, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%w: %s", ErrUnparsableResponse,
				c.Scrub(err.Error()))
		}
	}
	return resp.StatusCode, nil
}
