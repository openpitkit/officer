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

// Package etoro provides authenticated REST access and stream control frames.
package etoro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/internal/apikey"
)

// RESTURL serves both demo and real accounts; trading paths select the account.
const RESTURL = "https://public-api.etoro.com"

// Credentials identifies one eToro user and application.
type Credentials struct{ APIKey, UserKey string }

// ParseCredentials accepts only a JSON object with apiKey and userKey, each
// non-blank after trimming. Validation pointers are relative to the credentials
// object; errors never include input values or unknown field names.
func ParseCredentials(raw string) (Credentials, error) {
	credentials, err := apikey.ParseWithSecretField(raw, "etoro", "userKey")
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{APIKey: credentials.APIKey, UserKey: credentials.APISecret}, nil
}

func (c Credentials) validate() error {
	if strings.TrimSpace(c.APIKey) == "" {
		return domain.NewValidationError("/apiKey", "required", "etoro: apiKey is required")
	}
	if strings.TrimSpace(c.UserKey) == "" {
		return domain.NewValidationError("/userKey", "required", "etoro: userKey is required")
	}
	return nil
}

// TradingPath joins a trading prefix (such as /api/v3/trading/execution) and
// a resource (such as /orders), inserting the demo segment for demo accounts.
func TradingPath(demo bool, prefix, resource string) string {
	if demo {
		return prefix + "/demo" + resource
	}
	return prefix + resource
}

// CloseOrderInfoPath selects the explicit account segment required by the
// close-order info endpoint, unlike other real-account trading resources.
func CloseOrderInfoPath(demo bool, orderID string) string {
	account := "real"
	if demo {
		account = "demo"
	}
	return "/api/v1/trading/info/" + account + "/close-orders/" + orderID
}

// APIError is an HTTP refusal. Code is ProblemDetails.type, errorCode, or
// error.code; Message is detail (or title), errorMessage, or error.message.
// Code and Message are redacted and stay empty for unknown body shapes.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	message := fmt.Sprintf("etoro: HTTP %d", e.Status)
	if e.Code != "" {
		message += " (code " + e.Code + ")"
	}
	if e.Message != "" {
		message += ": " + e.Message
	}
	return message
}

// Client owns account credentials and uses the supplied transport and credential
// redactor from secret.CredentialRedactor.
type Client struct {
	baseURL     string
	credentials Credentials
	httpClient  *http.Client
	redactor    *strings.Replacer
}

// NewClient binds REST requests to one host. Redirects are refused so auth
// headers cannot be forwarded to a host chosen by a response.
func NewClient(baseURL string, credentials Credentials, httpClient *http.Client, redactor *strings.Replacer) *Client {
	if httpClient != nil {
		copy := *httpClient
		copy.CheckRedirect = refuseRedirect
		httpClient = &copy
	}
	return &Client{strings.TrimRight(baseURL, "/"), credentials, httpClient, redactor}
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

func (c *Client) wrap(operation string, err error) error {
	return &requestError{c.redactor.Replace("etoro: " + operation + ": " + err.Error()), err}
}

// Request exchanges JSON for calls that do not create orders, generating a fresh
// request UUID. Order-creating calls must use RequestOrder instead.
func (c *Client) Request(ctx context.Context, method, path string, body, result any) (int, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return 0, errors.New("etoro: generate request id failed")
	}
	return c.RequestOrder(ctx, method, path, id.String(), body, result)
}

// RequestOrder exchanges JSON using exactly the caller's request id, which
// eToro echoes as referenceId. A blank id is an error, never a generated id.
// Non-2xx replies produce APIError; status is retained on response errors.
func (c *Client) RequestOrder(ctx context.Context, method, path, requestID string, body, result any) (int, error) {
	if c.httpClient == nil {
		return 0, errors.New("etoro: HTTP client is required")
	}
	if c.redactor == nil {
		return 0, errors.New("etoro: credential redactor is required")
	}
	if err := c.credentials.validate(); err != nil {
		return 0, err
	}
	if strings.TrimSpace(requestID) == "" {
		return 0, errors.New("etoro: request id is required")
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return 0, errors.New("etoro: host-relative path is required")
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
		return 0, errors.New("etoro: create request failed")
	}
	req.Header.Set("x-api-key", c.credentials.APIKey)
	req.Header.Set("x-user-key", c.credentials.UserKey)
	req.Header.Set("x-request-id", requestID)
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
			Type         *string `json:"type"`
			Title        *string `json:"title"`
			Detail       *string `json:"detail"`
			ErrorCode    *string `json:"errorCode"`
			ErrorMessage *string `json:"errorMessage"`
			Error        *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &fields) == nil {
			switch {
			case fields.Type != nil || fields.Title != nil || fields.Detail != nil:
				if fields.Type != nil {
					apiErr.Code = *fields.Type
				}
				if fields.Detail != nil {
					apiErr.Message = *fields.Detail
				} else if fields.Title != nil {
					apiErr.Message = *fields.Title
				}
			case fields.ErrorCode != nil || fields.ErrorMessage != nil:
				if fields.ErrorCode != nil {
					apiErr.Code = *fields.ErrorCode
				}
				if fields.ErrorMessage != nil {
					apiErr.Message = *fields.ErrorMessage
				}
			case fields.Error != nil:
				apiErr.Code, apiErr.Message = fields.Error.Code, fields.Error.Message
			}
		}
		apiErr.Code = c.redactor.Replace(apiErr.Code)
		apiErr.Message = c.redactor.Replace(apiErr.Message)
		return resp.StatusCode, apiErr
	}
	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return resp.StatusCode, c.wrap("decode response", err)
		}
	}
	return resp.StatusCode, nil
}
