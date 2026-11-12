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

package fixture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/internal/apikey"
	"go.openpit.dev/officer/internal/backoff"
	"go.openpit.dev/officer/internal/vendorhttp"
)

func reconnect() {
	for attempt := 0; ; attempt++ {
		// ruleid: shared-venue-stream-lifecycle
		time.Sleep(backoff.Delay(attempt, time.Second, time.Minute))
	}
}

func reconnectWithFixedDelay(ctx context.Context) {
	for {
		// ruleid: shared-venue-stream-lifecycle
		backoff.Sleep(ctx, 2*time.Second)
	}
}

func reconnectDelayFunction() {
	// ruleid: shared-venue-stream-lifecycle
	delay := backoff.Delay
	_ = delay
}

type retryPolicy struct{}

func (retryPolicy) Delay(attempt int) time.Duration {
	return time.Duration(attempt) * time.Second
}

func retryWithLocalPolicy() {
	retry := retryPolicy{}
	// ok: shared-venue-stream-lifecycle
	time.Sleep(retry.Delay(1))
}

func ParseCredentials(raw json.RawMessage) (string, string, error) {
	var fields map[string]string
	// ruleid: shared-vendor-credentials
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", "", err
	}
	// ruleid: shared-vendor-credentials
	key := fields["apiKey"]
	// ruleid: shared-vendor-credentials
	secret := fields["apiSecret"]
	return key, secret, nil
}

func ParseCredentials(raw string) (map[string]string, error) {
	var fields map[string]string
	// ruleid: shared-vendor-credentials
	decoder := json.NewDecoder(strings.NewReader(raw))
	err := decoder.Decode(&fields)
	return fields, err
}

func ParseCredentials(raw json.RawMessage) (apikey.Credentials, error) {
	// ok: shared-vendor-credentials
	return apikey.Parse(raw, "vendor")
}

func ParseCredentials(raw json.RawMessage) (Credentials, error) {
	// ok: shared-vendor-credentials
	parsed, err := apikey.ParseWithSecretField(raw, "vendor", "secretKey")
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{Key: parsed.APIKey, Secret: parsed.APISecret}, nil
}

func decodePayload(raw json.RawMessage) error {
	var fields map[string]string
	// ok: shared-vendor-credentials
	return json.Unmarshal(raw, &fields)
}

func NewVendorConnector(raw string) (string, string, error) {
	var fields map[string]string
	// ok: shared-vendor-credentials
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return "", "", err
	}
	// ruleid: shared-vendor-credentials
	key := fields["apiKey"]
	// ruleid: shared-vendor-credentials
	secret := fields["apiSecret"]
	return key, secret, nil
}

func inspectCredentialField(field string) {
	// ruleid: shared-vendor-credentials
	_ = field != "apiKey"
	switch field {
	// ruleid: shared-vendor-credentials
	case "apiSecret":
		return
	}
}

func inspectConstantCredentialFields(fields map[string]string, field string) {
	const apiSecretField = "apiSecret"
	const passphraseField = "passphrase"

	// ruleid: shared-vendor-credentials
	_ = fields[apiSecretField]
	// ruleid: shared-vendor-credentials
	_ = field != apiSecretField
	// ruleid: shared-vendor-credentials
	_ = fields[passphraseField]
	// ruleid: shared-vendor-credentials
	_ = field == passphraseField
}

func inspectCredentialParameters(fields map[string]string, apiSecret, passphrase string) {
	// ok: shared-vendor-credentials
	_ = fields[apiSecret]
	// ok: shared-vendor-credentials
	_ = fields[passphrase]
	// ok: shared-vendor-credentials
	_ = apiSecret != passphrase
}

func inspectNewCredentialFields(fields map[string]string, field string) {
	// ruleid: shared-vendor-credentials
	_ = fields["passphrase"]
	// ruleid: shared-vendor-credentials
	_ = fields["keyName"]
	// ruleid: shared-vendor-credentials
	_ = fields["privateKey"]
	// ruleid: shared-vendor-credentials
	_ = field == "passphrase"
	// ruleid: shared-vendor-credentials
	_ = field != "passphrase"
	// ruleid: shared-vendor-credentials
	_ = field == "keyName"
	// ruleid: shared-vendor-credentials
	_ = field != "keyName"
	// ruleid: shared-vendor-credentials
	_ = field == "privateKey"
	// ruleid: shared-vendor-credentials
	_ = field != "privateKey"
	switch field {
	// ruleid: shared-vendor-credentials
	case "passphrase":
		return
	// ruleid: shared-vendor-credentials
	case "keyName":
		return
	// ruleid: shared-vendor-credentials
	case "privateKey":
		return
	}
	// ok: shared-vendor-credentials
	_ = fields["address"]
	// ok: shared-vendor-credentials
	_ = field == "address"
}

func outboundCredentials(values url.Values, key string) {
	// ok: shared-vendor-credentials
	_ = map[string]string{"apiKey": key}
	// ok: shared-vendor-credentials
	values.Set("apiKey", key)
}

type vendorConfig struct {
	// ok: shared-vendor-credentials
	APIKey string `json:"apiKey"`
	// ok: shared-vendor-credentials
	APISecret string `json:"apiSecret"`
}

func localRedirectPolicy(*http.Request, []*http.Request) error {
	// ruleid: shared-vendor-http-redirect
	return http.ErrUseLastResponse
}

func privateHTTPPolicies(client *http.Client) {
	ctx := context.Background()
	// ruleid: shared-vendor-stream-dial
	websocket.Dial(ctx, "wss://vendor.example", &websocket.DialOptions{HTTPClient: &http.Client{CheckRedirect: vendorhttp.RefuseRedirect}})
	// ruleid: shared-vendor-stream-dial
	websocket.Dial(ctx, "wss://vendor.example", &websocket.DialOptions{})
	// ruleid: shared-vendor-stream-dial
	websocket.Dial(ctx, "wss://vendor.example", nil)
	// ok: shared-vendor-stream-dial
	vendorhttp.DialStream(ctx, "vendor", "wss://vendor.example")
	// ruleid: shared-vendor-stream-dial
	type copiedStreamWriter interface {
		Write(context.Context, websocket.MessageType, []byte) error
	}
	// ruleid: shared-vendor-stream-dial
	type namedStreamWriter interface {
		Write(ctx context.Context, typ websocket.MessageType, payload []byte) error
	}
	// ok: shared-vendor-stream-dial
	type streamReaderWriter interface {
		Write(context.Context, websocket.MessageType, []byte) error
		Read(context.Context) (websocket.MessageType, []byte, error)
	}
	// ok: shared-vendor-stream-dial
	type sharedStreamWriter interface {
		vendorhttp.StreamWriter
	}
	// ruleid: shared-vendor-http-redirect
	client.CheckRedirect = localRedirectPolicy
	// ruleid: shared-vendor-http-redirect
	client.CheckRedirect = nil
	// ruleid: shared-vendor-http-redirect
	_ = &http.Client{CheckRedirect: localRedirectPolicy}
	// ruleid: shared-vendor-http-redirect
	_ = http.Client{CheckRedirect: nil}
	// ruleid: shared-vendor-http-redirect
	_ = http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	// ok: shared-vendor-http-redirect
	_ = &http.Client{CheckRedirect: vendorhttp.RefuseRedirect}
	// ruleid: shared-vendor-http-redirect
	client.CheckRedirect = vendorhttp.RefuseRedirect
	// ok: shared-vendor-http-redirect
	_ = vendorhttp.CopyClient(client)
}

func localCopyClient(client *http.Client) *http.Client {
	c := *client
	// ruleid: shared-vendor-http-redirect
	c.CheckRedirect = vendorhttp.RefuseRedirect
	return &c
}

func privateRequestWrap(redact *strings.Replacer, err error, operation string) error {
	// ruleid: shared-vendor-http-wrap
	return &privateRequestError{redact.Replace("vendor: " + operation + ": " + err.Error()), err}
}

func keyedPrivateRequestWrap(redact *strings.Replacer, err error) error {
	// ruleid: shared-vendor-http-wrap
	return &privateRequestError{message: redact.Replace("vendor: request: " + err.Error()), cause: err}
}

func constructorRequestWrap(redact *strings.Replacer, err error, operation string) error {
	// ruleid: shared-vendor-http-wrap
	return vendorhttp.NewRequestError(redact.Replace("kraken: "+operation+": "+err.Error()), err)
}

// ruleid: shared-vendor-http-error
type privateRequestError struct {
	message string
	cause   error
}

func (e *privateRequestError) Error() string { return e.message }
func (e *privateRequestError) Unwrap() error { return e.cause }

// ruleid: shared-vendor-http-error
type reversedRequestError struct {
	err  error
	text string
}

func (e reversedRequestError) Unwrap() error { return e.err }
func (e reversedRequestError) Error() string { return e.text }

func (e *trailingRequestError) Error() string { return e.message }
func (e trailingRequestError) Unwrap() error  { return e.cause }

// ruleid: shared-vendor-http-error
type trailingRequestError struct {
	message string
	cause   error
}

func (e betweenRequestError) Error() string { return e.message }

// ruleid: shared-vendor-http-error
type betweenRequestError struct {
	cause   error
	message string
}

func (e *betweenRequestError) Unwrap() error { return e.cause }

func (e *betweenReversedError) Unwrap() error { return e.cause }

// ruleid: shared-vendor-http-error
type betweenReversedError struct {
	message string
	cause   error
}

func (e *betweenReversedError) Error() string { return e.message }

// ok: shared-vendor-http-error
type ExportedRequestError struct {
	message string
	cause   error
}

func (e *ExportedRequestError) Error() string { return e.message }
func (e *ExportedRequestError) Unwrap() error { return e.cause }

// ok: shared-vendor-http-error
type noUnwrapError struct {
	message string
	cause   error
}

func (e *noUnwrapError) Error() string { return e.message }

func binanceWrap(redact, signedRedactor *strings.Replacer, operation string, err error) error {
	inner := err
	var urlErr *url.Error
	for errors.As(inner, &urlErr) {
		inner = urlErr.Err
	}
	raw := inner.Error()
	message := signedRedactor.Replace(raw)
	causeText := redact.Replace(message)
	if causeText != raw {
		inner = errors.New(causeText)
	}
	// ok: shared-vendor-http-wrap
	return vendorhttp.NewRequestError(redact.Replace("binance: "+operation+": "+message), inner)
}

func redactedText(redact *strings.Replacer, err error) (string, error) {
	// ok: shared-vendor-http-wrap
	message := redact.Replace(err.Error())
	// ok: shared-vendor-http-wrap
	return message, errors.New(redact.Replace(err.Error()))
}

func etoroStreamErrors(redact *strings.Replacer, err error) {
	// ok: shared-vendor-http-wrap
	err = errors.New(redact.Replace(err.Error()))
	// ok: shared-vendor-http-wrap
	slog.Debug("etoro: close stream connection", "error", redact.Replace(err.Error()))
}

func binanceSymbolLookupError(redact *strings.Replacer, err error) (string, bool, error) {
	// ok: shared-vendor-http-wrap
	return "", false, fmt.Errorf("binance: symbol lookup: %s: %w",
		redact.Replace(err.Error()), domain.ErrUpstream)
}

func binanceCatalogueError(redact *strings.Replacer, err error) ([]string, error) {
	// ok: shared-vendor-http-wrap
	return nil, fmt.Errorf("binance: catalogue: %s: %w",
		redact.Replace(err.Error()), domain.ErrUpstream)
}

// ok: shared-vendor-http-error
type RetryAfterError struct {
	Err   error
	After time.Duration
}

func (e RetryAfterError) Error() string {
	return fmt.Sprintf("%v; retry after %v", e.Err, e.After)
}
func (e RetryAfterError) Unwrap() error { return e.Err }

// ok: shared-vendor-http-error
type setupRequiredError struct{ host string }

func (e setupRequiredError) Error() string {
	return fmt.Sprintf("Pit Officer is not set up yet. Open http://%s "+
		"in a browser on this computer and finish the first-run setup.", e.host)
}

// ok: shared-vendor-http-error
type additionalFieldError struct {
	message string
	cause   error
	status  int
}

func (e *additionalFieldError) Error() string { return e.message }
func (e *additionalFieldError) Unwrap() error { return e.cause }

// ok: shared-vendor-http-error
type formattedError struct {
	message string
	cause   error
}

func (e *formattedError) Error() string { return fmt.Sprintf("wrapped: %s", e.message) }
func (e *formattedError) Unwrap() error { return e.cause }

func localRequestErrorShadows() {
	{
		// ok: shared-vendor-http-error
		type privateRequestError struct {
			text string
			err  error
		}
		_ = privateRequestError{}
	}
	{
		// ok: shared-vendor-http-error
		type privateRequestError struct {
			message string
			cause   error
		}
		_ = privateRequestError{}
	}
}

func (e *privateRequestError) localShadows() {
	{
		// ok: shared-vendor-http-error
		type privateRequestError struct {
			text string
			err  error
		}
		_ = privateRequestError{}
	}
	{
		// ok: shared-vendor-http-error
		type privateRequestError struct {
			message string
			cause   error
		}
		_ = privateRequestError{}
	}
}

var localRequestErrorLiteral = func() {
	// ok: shared-vendor-http-error
	type privateRequestError struct {
		message string
		cause   error
	}
	_ = privateRequestError{}
}
