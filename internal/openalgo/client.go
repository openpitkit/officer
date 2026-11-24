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

// Package openalgo holds OpenAlgo REST access, stream authentication and
// credentials shared by trading and market-data connectors.
package openalgo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/secret"
	"go.openpit.dev/officer/internal/apikey"
	"go.openpit.dev/officer/internal/vendorhttp"
)

// ErrInvalidResponse means the bridge answered, but its response is not one
// the client can read.
var ErrInvalidResponse = errors.New("invalid response")

// Credentials contains the non-secret bridge addresses and its API key.
type Credentials struct {
	URL, WSURL, APIKey string
}

// ParseCredentials validates the shared trading and market-data credentials.
// Refused addresses are never included in errors.
// Keys limited to numeric characters are refused to prevent numeric echoes.
func ParseCredentials(raw string) (Credentials, error) {
	key, settings, err := apikey.ParseKey(raw, "openalgo", "url", "wsUrl")
	if err != nil {
		return Credentials{}, err
	}
	if strings.Trim(key, "0123456789.+-eE") == "" {
		return Credentials{}, domain.NewValidationError("/apiKey", "format",
			"openalgo: apiKey must contain a non-numeric character")
	}
	for i, field := range []string{"url", "wsUrl"} {
		address, ok := secret.CredentialFreeURL(settings[i])
		schemes := "http or https"
		if i == 1 {
			schemes = "ws or wss"
		}
		if !ok || (i == 0 && address.Scheme != "http" && address.Scheme != "https") ||
			(i == 1 && address.Scheme != "ws" && address.Scheme != "wss") {
			return Credentials{}, domain.NewValidationError("/"+field, "format",
				"openalgo: "+field+" must be an absolute "+schemes+" URL without user info, query or fragment")
		}
	}
	return Credentials{URL: settings[0], WSURL: settings[1], APIKey: key}, nil
}

// APIError preserves the HTTP status without assigning connector semantics.
// Message contains credential-redacted text rendered from decoded venue values.
// EnvelopeDecoded reports whether the complete body decoded successfully with
// a non-empty string status identifying an OpenAlgo envelope.
// Mode is its redacted string mode, empty for an absent or non-string member;
// it makes no mode claim when EnvelopeDecoded is false.
type APIError struct {
	Status          int
	Message         string
	Mode            string
	EnvelopeDecoded bool
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("openalgo: HTTP %d", e.Status)
	}
	return fmt.Sprintf("openalgo: HTTP %d: %s", e.Status, e.Message)
}

// Decimal is exact decimal text from a JSON number or plain decimal string.
// Null and absent fields remain empty; interpretation belongs to the connector.
type Decimal string

var plainDecimal = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?$`)

// Identifier preserves a string or unsigned JSON integer without rounding.
// Null and absent identifiers are empty. Empty or blank strings and other JSON
// kinds are refused.
type Identifier string

// UnmarshalJSON accepts exact identifier text without exposing invalid values.
func (id *Identifier) UnmarshalJSON(payload []byte) error {
	payload = bytes.TrimSpace(payload)
	kind := jsonKind(payload)
	if kind == "null" {
		*id = ""
		return nil
	}
	if kind == "string" {
		var text string
		if err := json.Unmarshal(payload, &text); err != nil {
			return errors.New("openalgo: invalid identifier string")
		}
		if strings.TrimSpace(text) == "" {
			return errors.New("openalgo: invalid identifier empty string")
		}
		*id = Identifier(text)
		return nil
	}
	text := string(payload)
	if kind != "number" ||
		strings.Trim(text, "0123456789") != "" || !json.Valid(payload) {
		return fmt.Errorf("openalgo: invalid identifier %s", kind)
	}
	*id = Identifier(text)
	return nil
}

// UnmarshalJSON preserves the text without a floating-point conversion.
func (d *Decimal) UnmarshalJSON(payload []byte) error {
	payload = bytes.TrimSpace(payload)
	kind := jsonKind(payload)
	if kind == "null" {
		*d = ""
		return nil
	}
	text := string(payload)
	if kind == "string" {
		if err := json.Unmarshal(payload, &text); err != nil {
			return errors.New("openalgo: invalid decimal string")
		}
	}
	valid := kind == "string" && plainDecimal.MatchString(text)
	if kind == "number" {
		valid = json.Valid(payload) && plainDecimal.MatchString(text)
	}
	if !valid {
		return fmt.Errorf("openalgo: invalid decimal %s", kind)
	}
	*d = Decimal(text)
	return nil
}

func jsonKind(payload []byte) string {
	if len(payload) == 0 {
		return "empty input"
	}
	if bytes.Equal(payload, []byte("null")) {
		return "null"
	}
	switch payload[0] {
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case '{':
		return "object"
	case '[':
		return "array"
	default:
		return "number"
	}
}

// Client sends authenticated POST requests to one user-hosted bridge.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	redactor   *strings.Replacer
}

// NewClient copies the HTTP client and refuses redirects. A nil client or
// redactor is reported when making a request.
func NewClient(baseURL, apiKey string, httpClient *http.Client, redactor *strings.Replacer) *Client {
	return &Client{strings.TrimRight(baseURL, "/"), apiKey,
		vendorhttp.CopyClient(httpClient), redactor}
}

func (c *Client) request(ctx context.Context, name string, fields map[string]string, result any) error {
	if c.httpClient == nil {
		return errors.New("openalgo: HTTP client is required")
	}
	if c.redactor == nil {
		return errors.New("openalgo: credential redactor is required")
	}
	body := make(map[string]string, len(fields)+1)
	for key, value := range fields {
		body[key] = value
	}
	body["apikey"] = c.apiKey
	encoded, err := json.Marshal(body)
	if err != nil {
		return vendorhttp.Wrap(c.redactor, "openalgo", "encode request", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/"+name, bytes.NewReader(encoded))
	if err != nil {
		return vendorhttp.Wrap(c.redactor, "openalgo", "create request", err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return vendorhttp.Wrap(c.redactor, "openalgo", "request", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			slog.Warn("openalgo: close response body", "error",
				vendorhttp.Wrap(c.redactor, "openalgo", "close response", err))
		}
	}()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return vendorhttp.Wrap(c.redactor, "openalgo", "read response", err)
	}
	var envelope struct {
		Status  string `json:"status"`
		Message any    `json:"message"`
		Mode    any    `json:"mode"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	decodeErr := decoder.Decode(&envelope)
	if decodeErr == nil {
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			decodeErr = errors.New("unexpected data after response")
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 ||
		(decodeErr == nil && envelope.Status != "success") {
		apiErr := &APIError{Status: response.StatusCode}
		if decodeErr == nil {
			apiErr.Message = c.redactor.Replace(messageText(envelope.Message))
			if envelope.Status != "" {
				apiErr.EnvelopeDecoded = true
				mode, _ := envelope.Mode.(string)
				apiErr.Mode = c.redactor.Replace(mode)
			}
		}
		return apiErr
	}
	if decodeErr == nil {
		decodeErr = json.Unmarshal(payload, result)
	}
	if decodeErr != nil {
		message := "openalgo: decode response: " +
			strings.TrimPrefix(decodeErr.Error(), "openalgo: ")
		return vendorhttp.NewRequestError(c.redactor.Replace(message),
			fmt.Errorf("%w: %w", ErrInvalidResponse, decodeErr))
	}
	return nil
}

func messageText(value any) string {
	switch value := value.(type) {
	case nil:
		return ""
	case string:
		return value
	case map[string]any:
		names := make([]string, 0, len(value))
		for name := range value {
			names = append(names, name)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, name := range names {
			parts = append(parts, name+": "+messageText(value[name]))
		}
		return strings.Join(parts, "; ")
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			parts = append(parts, messageText(item))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(value)
	}
}

func (c *Client) redact(fields ...*string) {
	for _, field := range fields {
		*field = c.redactor.Replace(*field)
	}
}

// Ping requires an authenticated broker session and returns its broker name.
func (c *Client) Ping(ctx context.Context) (string, error) {
	var reply struct {
		Data struct {
			Broker string `json:"broker"`
		} `json:"data"`
	}
	if err := c.request(ctx, "ping", nil, &reply); err != nil {
		return "", err
	}
	if strings.TrimSpace(reply.Data.Broker) == "" {
		return "", fmt.Errorf("openalgo: ping broker is required: %w", ErrInvalidResponse)
	}
	return c.redactor.Replace(reply.Data.Broker), nil
}

// Analyzer refuses missing or inconsistent execution mode information.
func (c *Client) Analyzer(ctx context.Context) (analyze bool, err error) {
	var reply struct {
		Data struct {
			AnalyzeMode *bool  `json:"analyze_mode"`
			Mode        string `json:"mode"`
		} `json:"data"`
	}
	if err := c.request(ctx, "analyzer", nil, &reply); err != nil {
		return false, err
	}
	if reply.Data.AnalyzeMode == nil {
		return false, fmt.Errorf("openalgo: analyzer analyze_mode is required: %w", ErrInvalidResponse)
	}
	expected := "live"
	if *reply.Data.AnalyzeMode {
		expected = "analyze"
	}
	if reply.Data.Mode != expected {
		return false, fmt.Errorf("openalgo: analyzer mode is missing or inconsistent: %w", ErrInvalidResponse)
	}
	return *reply.Data.AnalyzeMode, nil
}

// PlaceOrderRequest carries OpenAlgo's string-valued order parameters.
// Price is omitted when empty.
type PlaceOrderRequest struct {
	Strategy, Symbol, Action, Exchange, PriceType, Product, Quantity, Price string
}

// PlaceOrderResult preserves live, analyzer and Semi-Auto replies without
// deciding whether an order was sent. PendingOrderID is unsigned integer text.
type PlaceOrderResult struct {
	OrderID        Identifier `json:"orderid"`
	Mode           string     `json:"mode"`
	PendingOrderID Identifier `json:"pending_order_id"`
	Message        string     `json:"message"`
}

// PlaceOrder returns the bridge's result without interpreting its mode.
func (c *Client) PlaceOrder(ctx context.Context, order PlaceOrderRequest) (PlaceOrderResult, error) {
	fields := map[string]string{
		"strategy": order.Strategy, "symbol": order.Symbol, "action": order.Action,
		"exchange": order.Exchange, "pricetype": order.PriceType,
		"product": order.Product, "quantity": order.Quantity,
	}
	if order.Price != "" {
		fields["price"] = order.Price
	}
	var result PlaceOrderResult
	if err := c.request(ctx, "placeorder", fields, &result); err != nil {
		return PlaceOrderResult{}, err
	}
	pending := string(result.PendingOrderID)
	if strings.Trim(pending, "0123456789") != "" {
		return PlaceOrderResult{}, fmt.Errorf("openalgo: pending_order_id must be an unsigned integer: %w", ErrInvalidResponse)
	}
	c.redact((*string)(&result.OrderID), &result.Mode,
		(*string)(&result.PendingOrderID), &result.Message)
	return result, nil
}

// OrderStatus holds the order fields consumed by Officer.
type OrderStatus struct {
	Mode         string     `json:"-"`
	OrderID      Identifier `json:"orderid"`
	Symbol       string     `json:"symbol"`
	Exchange     string     `json:"exchange"`
	Action       string     `json:"action"`
	Quantity     Decimal    `json:"quantity"`
	Price        Decimal    `json:"price"`
	AveragePrice Decimal    `json:"average_price"`
	Status       string     `json:"order_status"`
}

// OrderStatus returns one order without interpreting broker-specific states.
func (c *Client) OrderStatus(ctx context.Context, orderID string) (OrderStatus, error) {
	var reply struct {
		Mode string       `json:"mode"`
		Data *OrderStatus `json:"data"`
	}
	if err := c.request(ctx, "orderstatus", map[string]string{"orderid": orderID}, &reply); err != nil {
		return OrderStatus{}, err
	}
	if reply.Data == nil {
		return OrderStatus{}, fmt.Errorf("openalgo: orderstatus data is required: %w", ErrInvalidResponse)
	}
	result := *reply.Data
	result.Mode = reply.Mode
	c.redact(&result.Mode, (*string)(&result.OrderID), &result.Symbol, &result.Exchange, &result.Action, &result.Status)
	return result, nil
}

// Trade is one tradebook row. Several rows may refer to the same order.
type Trade struct {
	OrderID      Identifier `json:"orderid"`
	Symbol       string     `json:"symbol"`
	Exchange     string     `json:"exchange"`
	Action       string     `json:"action"`
	Quantity     Decimal    `json:"quantity"`
	AveragePrice Decimal    `json:"average_price"`
}

// TradeBookResult keeps the reply's top-level mode alongside its trade rows.
type TradeBookResult struct {
	Mode   string
	Trades []Trade
}

// TradeBook preserves all rows in the broker's order without aggregating.
func (c *Client) TradeBook(ctx context.Context) (TradeBookResult, error) {
	var reply struct {
		Mode string   `json:"mode"`
		Data []*Trade `json:"data"`
	}
	if err := c.request(ctx, "tradebook", nil, &reply); err != nil {
		return TradeBookResult{}, err
	}
	if reply.Data == nil {
		return TradeBookResult{}, fmt.Errorf("openalgo: tradebook data is required: %w", ErrInvalidResponse)
	}
	trades := make([]Trade, len(reply.Data))
	for i, row := range reply.Data {
		if row == nil {
			return TradeBookResult{}, fmt.Errorf("openalgo: tradebook data contains null row: %w", ErrInvalidResponse)
		}
		c.redact((*string)(&row.OrderID), &row.Symbol, &row.Exchange, &row.Action)
		trades[i] = *row
	}
	c.redact(&reply.Mode)
	return TradeBookResult{Mode: reply.Mode, Trades: trades}, nil
}

// SymbolInfo holds instrument metadata without converting its decimals.
type SymbolInfo struct {
	Symbol         string  `json:"symbol"`
	Name           string  `json:"name"`
	Exchange       string  `json:"exchange"`
	InstrumentType string  `json:"instrumenttype"`
	LotSize        Decimal `json:"lotsize"`
	TickSize       Decimal `json:"tick_size"`
}

// Symbol fetches the bridge's metadata for an exchange-qualified symbol.
func (c *Client) Symbol(ctx context.Context, symbol, exchange string) (SymbolInfo, error) {
	var reply struct {
		Data *SymbolInfo `json:"data"`
	}
	if err := c.request(ctx, "symbol", map[string]string{"symbol": symbol, "exchange": exchange}, &reply); err != nil {
		return SymbolInfo{}, err
	}
	if reply.Data == nil {
		return SymbolInfo{}, fmt.Errorf("openalgo: symbol data is required: %w", ErrInvalidResponse)
	}
	row := reply.Data
	c.redact(&row.Symbol, &row.Name, &row.Exchange, &row.InstrumentType)
	return *row, nil
}

// Search returns symbol metadata in the bridge's response order.
func (c *Client) Search(ctx context.Context, query, exchange string) ([]SymbolInfo, error) {
	var reply struct {
		Data []*SymbolInfo `json:"data"`
	}
	if err := c.request(ctx, "search", map[string]string{"query": query, "exchange": exchange}, &reply); err != nil {
		return nil, err
	}
	if reply.Data == nil {
		return nil, fmt.Errorf("openalgo: search data is required: %w", ErrInvalidResponse)
	}
	symbols := make([]SymbolInfo, len(reply.Data))
	for i, row := range reply.Data {
		if row == nil {
			return nil, fmt.Errorf("openalgo: search data contains null row: %w", ErrInvalidResponse)
		}
		c.redact(&row.Symbol, &row.Name, &row.Exchange, &row.InstrumentType)
		symbols[i] = *row
	}
	return symbols, nil
}
