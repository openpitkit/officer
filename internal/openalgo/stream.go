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
	"fmt"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/internal/vendorhttp"
)

// WriteStreamFrame sends one JSON text frame. Errors retain their cause; the
// caller's stream lifecycle redacts text before display.
func WriteStreamFrame(ctx context.Context, conn vendorhttp.StreamWriter, frame any) error {
	if conn == nil {
		return errors.New("openalgo: stream writer is required")
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("openalgo: encode stream frame: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return fmt.Errorf("openalgo: write stream frame: %w", err)
	}
	return nil
}

// WriteStreamAuth sends the bridge's authentication frame. The key must be
// non-blank; it is carried only in the JSON frame. Errors retain their cause;
// the caller's stream lifecycle redacts text before display.
func WriteStreamAuth(ctx context.Context, conn vendorhttp.StreamWriter, apiKey string) error {
	if strings.TrimSpace(apiKey) == "" {
		return errors.New("openalgo: apiKey is required")
	}
	return WriteStreamFrame(ctx, conn, struct {
		Action string `json:"action"`
		APIKey string `json:"api_key"`
	}{"authenticate", apiKey})
}

// Authenticate sends credentials and validates one text authentication reply.
// The caller redacts transport errors before displaying them.
func Authenticate(ctx context.Context, conn *websocket.Conn, apiKey string, redactor *strings.Replacer) error {
	if err := WriteStreamAuth(ctx, conn, apiKey); err != nil {
		return err
	}
	payload, err := ReadTextReply(ctx, conn, "authentication")
	if err != nil {
		return err
	}
	return ParseStreamAuthReply(payload, redactor)
}

// ReadTextReply reads one text handshake reply for the named operation.
func ReadTextReply(ctx context.Context, conn *websocket.Conn, operation string) ([]byte, error) {
	typ, payload, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, fmt.Errorf("openalgo: expected text %s reply", operation)
	}
	return payload, nil
}

// StreamError reports a refused stream operation with redacted venue text.
type StreamError struct {
	Op, Code, Message string
}

func (e *StreamError) Error() string {
	return fmt.Sprintf("openalgo: stream %s failed (%s): %s", e.Op, e.Code, e.Message)
}

// ParseStreamAuthReply accepts only an explicit successful auth reply. A
// refused authentication leaves connection lifecycle decisions to the caller.
func ParseStreamAuthReply(payload []byte, redactor *strings.Replacer) error {
	if redactor == nil {
		return errors.New("openalgo: credential redactor is required")
	}
	var reply struct {
		Type    string `json:"type"`
		Status  string `json:"status"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &reply); err != nil {
		return errors.New("openalgo: invalid stream authentication reply")
	}
	if reply.Status == "error" {
		return &StreamError{Op: "authentication", Code: redactor.Replace(reply.Code), Message: redactor.Replace(reply.Message)}
	}
	if reply.Type == "auth" && reply.Status == "success" {
		return nil
	}
	return errors.New("openalgo: unexpected stream authentication reply")
}

// WriteSubscribeOrders subscribes to account orders with a required request ID.
func WriteSubscribeOrders(ctx context.Context, conn vendorhttp.StreamWriter, requestID string) error {
	if strings.TrimSpace(requestID) == "" {
		return errors.New("openalgo: subscription request id is required")
	}
	return WriteStreamFrame(ctx, conn, struct {
		Action    string `json:"action"`
		RequestID string `json:"request_id"`
	}{"subscribe_orders", requestID})
}

// ParseSubscribeOrdersReply requires a successful, matching acknowledgement.
// Upstream error codes and messages are redacted before they leave the parser.
func ParseSubscribeOrdersReply(payload []byte, requestID string, redactor *strings.Replacer) error {
	if redactor == nil {
		return errors.New("openalgo: credential redactor is required")
	}
	var reply struct {
		Type      string `json:"type"`
		Status    string `json:"status"`
		RequestID string `json:"request_id"`
		Code      string `json:"code"`
		Message   string `json:"message"`
	}
	if json.Unmarshal(payload, &reply) != nil {
		return errors.New("openalgo: invalid stream subscription reply")
	}
	if reply.Status == "error" {
		return &StreamError{Op: "subscription", Code: redactor.Replace(reply.Code), Message: redactor.Replace(reply.Message)}
	}
	if reply.Type == "subscribe_orders" && reply.Status == "success" &&
		reply.RequestID == requestID {
		return nil
	}
	return errors.New("openalgo: unexpected stream subscription reply")
}

// Instrument identifies an exact exchange/symbol subscription pair.
type Instrument struct {
	Exchange string `json:"exchange"`
	Symbol   string `json:"symbol"`
}

// QuoteRefusal is a refused subscription with a redacted venue explanation.
type QuoteRefusal struct {
	Instrument
	Message string
}

// WriteSubscribeQuotes requests Quote-mode data for instruments.
func WriteSubscribeQuotes(ctx context.Context, conn vendorhttp.StreamWriter, requestID string, instruments []Instrument) error {
	if strings.TrimSpace(requestID) == "" {
		return errors.New("openalgo: subscription request id is required")
	}
	if len(instruments) == 0 {
		return errors.New("openalgo: at least one instrument is required")
	}
	return WriteStreamFrame(ctx, conn, struct {
		Action    string       `json:"action"`
		Mode      string       `json:"mode"`
		Symbols   []Instrument `json:"symbols"`
		RequestID string       `json:"request_id"`
	}{"subscribe", "Quote", instruments, requestID})
}

// ParseSubscribeQuotesReply requires one row for each requested instrument.
// A partial acknowledgement returns each refusal; success returns none.
// Error replies may omit request_id while this request alone is outstanding.
func ParseSubscribeQuotesReply(payload []byte, requestID string, requested []Instrument, redactor *strings.Replacer) ([]QuoteRefusal, error) {
	if redactor == nil {
		return nil, errors.New("openalgo: credential redactor is required")
	}
	var reply struct {
		Type          string `json:"type"`
		Status        string `json:"status"`
		RequestID     string `json:"request_id"`
		Code          string `json:"code"`
		Message       string `json:"message"`
		Subscriptions []struct {
			Instrument
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"subscriptions"`
	}
	unexpected := errors.New("openalgo: unexpected quote subscription reply")
	if json.Unmarshal(payload, &reply) != nil || requestID == "" {
		return nil, unexpected
	}
	if reply.Status == "error" && (reply.RequestID == "" || reply.RequestID == requestID) {
		return nil, &StreamError{Op: "subscription", Code: redactor.Replace(reply.Code), Message: redactor.Replace(reply.Message)}
	}
	if reply.RequestID != requestID || reply.Type != "subscribe" ||
		(reply.Status != "success" && reply.Status != "partial") ||
		len(reply.Subscriptions) != len(requested) {
		return nil, unexpected
	}
	var refused []QuoteRefusal
	pending := make(map[Instrument]bool, len(requested))
	for _, instrument := range requested {
		if pending[instrument] {
			return nil, unexpected
		}
		pending[instrument] = true
	}
	for _, sub := range reply.Subscriptions {
		if !pending[sub.Instrument] {
			return nil, unexpected
		}
		delete(pending, sub.Instrument)
		switch sub.Status {
		case "success":
		case "error":
			refused = append(refused, QuoteRefusal{sub.Instrument, redactor.Replace(sub.Message)})
		default:
			return nil, unexpected
		}
	}
	if len(pending) != 0 || (reply.Status == "partial") != (len(refused) > 0) {
		return nil, unexpected
	}
	return refused, nil
}

// MarketData contains the last traded price and the source observation time.
type MarketData struct {
	Instrument
	LTP       Decimal
	Timestamp time.Time
}

// ErrStreamPong identifies a successful application heartbeat reply.
var ErrStreamPong = errors.New("openalgo: stream pong")

// ParseMarketData reads exact positive LTP text and an epoch-ms timestamp.
// Measurement errors retain the instrument; higher-mode fields are ignored.
// A successful heartbeat returns ErrStreamPong.
func ParseMarketData(payload []byte, redactor *strings.Replacer) (MarketData, error) {
	if redactor == nil {
		return MarketData{}, errors.New("openalgo: credential redactor is required")
	}
	var frame struct {
		Type    string `json:"type"`
		Status  string `json:"status"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	unreadable := errors.New("openalgo: unreadable market data")
	if json.Unmarshal(payload, &frame) != nil {
		return MarketData{}, unreadable
	}
	if frame.Status == "error" {
		return MarketData{}, &StreamError{Op: "subscription", Code: redactor.Replace(frame.Code), Message: redactor.Replace(frame.Message)}
	}
	if frame.Type == "pong" && frame.Status == "success" {
		return MarketData{}, ErrStreamPong
	}
	if frame.Type != "market_data" {
		return MarketData{}, ErrUnknownPush
	}
	var quote struct {
		Instrument
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(payload, &quote) != nil ||
		strings.TrimSpace(quote.Exchange) == "" || strings.TrimSpace(quote.Symbol) == "" {
		return MarketData{}, unreadable
	}
	result := MarketData{Instrument: quote.Instrument}
	var data struct {
		LTP       json.RawMessage `json:"ltp"`
		Timestamp json.RawMessage `json:"timestamp"`
	}
	if len(quote.Data) != 0 && json.Unmarshal(quote.Data, &data) != nil {
		return result, errors.New("openalgo: unreadable market data: invalid data")
	}
	if len(data.LTP) == 0 || string(data.LTP) == "null" {
		return result, errors.New("openalgo: unreadable market data: missing ltp")
	}
	if json.Unmarshal(data.LTP, &result.LTP) != nil {
		return result, errors.New("openalgo: unreadable market data: invalid ltp")
	}
	price, err := decimal.NewFromString(string(result.LTP))
	if err != nil {
		return result, errors.New("openalgo: unreadable market data: invalid ltp")
	}
	if !price.IsPositive() {
		return result, errors.New("openalgo: unreadable market data: non-positive ltp")
	}
	if len(data.Timestamp) == 0 || string(data.Timestamp) == "null" {
		return result, errors.New("openalgo: unreadable market data: missing timestamp")
	}
	var timestamp int64
	if json.Unmarshal(data.Timestamp, &timestamp) != nil {
		return result, errors.New("openalgo: unreadable market data: invalid timestamp")
	}
	result.Timestamp = time.UnixMilli(timestamp)
	return result, nil
}

// OrderUpdate identifies an order to reconcile through REST.
type OrderUpdate struct {
	Mode    string     `json:"mode"`
	OrderID Identifier `json:"orderid"`
	Status  string     `json:"order_status"`
	// FilledQuantity is 0 when the broker adapter reported no quantity.
	FilledQuantity Decimal `json:"filled_quantity"`
}

// ErrUnknownPush identifies a frame outside the order-update protocol.
var ErrUnknownPush = errors.New("openalgo: unknown stream push")

// ParseOrderUpdate reads account order pushes without exposing payload values.
// An absent filled quantity stays empty and cannot confirm closed-order fills.
func ParseOrderUpdate(payload []byte, redactor *strings.Replacer) (OrderUpdate, error) {
	if redactor == nil {
		return OrderUpdate{}, errors.New("openalgo: credential redactor is required")
	}
	var frame struct {
		Type    string `json:"type"`
		Status  string `json:"status"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	unreadable := errors.New("openalgo: unreadable order push")
	if json.Unmarshal(payload, &frame) != nil {
		return OrderUpdate{}, unreadable
	}
	if frame.Status == "error" {
		return OrderUpdate{}, &StreamError{Op: "subscription", Code: redactor.Replace(frame.Code), Message: redactor.Replace(frame.Message)}
	}
	if frame.Type != "order_update" {
		return OrderUpdate{}, ErrUnknownPush
	}
	var update OrderUpdate
	if json.Unmarshal(payload, &update) != nil ||
		(update.Mode != "analyze" && update.Mode != "live") ||
		update.OrderID == "" || strings.TrimSpace(update.Status) == "" {
		return OrderUpdate{}, unreadable
	}
	return update, nil
}
