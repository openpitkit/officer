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

package trading

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/shopspring/decimal"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

const (
	alpacaReconnectMin     = 100 * time.Millisecond
	alpacaReconnectMax     = 5 * time.Second
	alpacaHandshakeTimeout = 10 * time.Second
)

var errAlpacaUnauthorized = errors.New("alpaca: stream authorization refused")

func (c *alpacaConnector) Subscribe(ctx context.Context) (<-chan fwtrading.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("alpaca: connector is closed")
	}
	if c.done != nil {
		return nil, errors.New("alpaca: stream already subscribed")
	}
	ctx, c.cancel = context.WithCancel(ctx)
	c.done = make(chan struct{})
	out := make(chan fwtrading.Event)
	go func() {
		defer close(c.done)
		defer close(out)
		defer c.cancel()
		c.run(ctx, out)
	}()
	return out, nil
}

func (c *alpacaConnector) Close() {
	c.mu.Lock()
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	done := c.done
	c.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (c *alpacaConnector) run(ctx context.Context, out chan<- fwtrading.Event) {
	delay := alpacaReconnectMin
	for ctx.Err() == nil {
		healthy, err := c.stream(ctx, out)
		if ctx.Err() != nil {
			return
		}
		if healthy {
			delay = alpacaReconnectMin
		}
		if errors.Is(err, errAlpacaUnauthorized) {
			slog.Error("alpaca: stream authorization refused", "connection", c.externalID, "error", c.redactStreamMessage(err.Error()))
			delay = alpacaReconnectMax
		} else {
			slog.Warn("alpaca: stream failed, reconnecting", "connection", c.externalID, "error", c.redactStreamMessage(err.Error()))
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay *= 2
		if delay > alpacaReconnectMax {
			delay = alpacaReconnectMax
		}
	}
}

type alpacaFrame struct {
	Stream string          `json:"stream"`
	Action string          `json:"action"`
	Data   json.RawMessage `json:"data"`
}

func (c *alpacaConnector) readAlpacaFrame(ctx context.Context, conn *websocket.Conn) (alpacaFrame, error) {
	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			return alpacaFrame{}, err
		}
		var frame alpacaFrame
		if json.Unmarshal(payload, &frame) != nil || (frame.Stream == "" && frame.Action == "") {
			slog.Debug("alpaca: ignoring unparsable stream frame", "connection", c.externalID)
			continue
		}
		if frame.Action == "error" {
			var data struct {
				Message string `json:"error_message"`
			}
			if json.Unmarshal(frame.Data, &data) != nil || data.Message == "" {
				return alpacaFrame{}, errors.New("alpaca: stream server error without error_message")
			}
			return alpacaFrame{}, fmt.Errorf("alpaca: stream server error: %s", c.redactStreamMessage(data.Message))
		}
		return frame, nil
	}
}

func writeAlpacaFrame(ctx context.Context, conn *websocket.Conn, frame any) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}

func (c *alpacaConnector) stream(ctx context.Context, out chan<- fwtrading.Event) (healthy bool, err error) {
	handshakeCtx, cancel := context.WithTimeout(ctx, alpacaHandshakeTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(handshakeCtx, c.streamURL, &websocket.DialOptions{
		HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	})
	if err != nil {
		return false, err
	}
	defer func() {
		if err := conn.CloseNow(); err != nil {
			slog.Debug("alpaca: close stream connection", "connection", c.externalID, "error", err)
		}
	}()
	auth := struct {
		Action string `json:"action"`
		Key    string `json:"key"`
		Secret string `json:"secret"`
	}{"auth", c.credentials.APIKey, c.credentials.APISecret}
	if err := writeAlpacaFrame(handshakeCtx, conn, auth); err != nil {
		return false, err
	}
	for {
		frame, err := c.readAlpacaFrame(handshakeCtx, conn)
		if err != nil {
			return false, err
		}
		if frame.Stream != "authorization" {
			continue
		}
		var data struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(frame.Data, &data) != nil {
			slog.Debug("alpaca: ignoring unparsable authorization", "connection", c.externalID)
			continue
		}
		if data.Status == "unauthorized" {
			return false, errAlpacaUnauthorized
		}
		if data.Status == "authorized" {
			break
		}
	}
	listen := struct {
		Action string `json:"action"`
		Data   struct {
			Streams []string `json:"streams"`
		} `json:"data"`
	}{Action: "listen"}
	listen.Data.Streams = []string{"trade_updates"}
	if err := writeAlpacaFrame(handshakeCtx, conn, listen); err != nil {
		return false, err
	}
	for {
		frame, err := c.readAlpacaFrame(handshakeCtx, conn)
		if err != nil {
			return false, err
		}
		if frame.Stream != "listening" {
			continue
		}
		var data struct {
			Streams []string `json:"streams"`
		}
		if json.Unmarshal(frame.Data, &data) != nil {
			slog.Debug("alpaca: ignoring unparsable listen acknowledgement", "connection", c.externalID)
			continue
		}
		listening := false
		for _, stream := range data.Streams {
			if stream == "trade_updates" {
				listening = true
			}
		}
		if listening {
			break
		}
	}
	cancel()
	select {
	case <-ctx.Done():
		return healthy, ctx.Err()
	case out <- fwtrading.Event{Kind: fwtrading.EventConnected}:
	}
	connectedAt := time.Now()
	defer func() {
		healthy = healthy || time.Since(connectedAt) > alpacaReconnectMax
	}()
	for {
		frame, err := c.readAlpacaFrame(ctx, conn)
		if err != nil {
			return healthy, err
		}
		if frame.Stream != "trade_updates" {
			continue
		}
		event, ok := c.parseAlpacaTradeUpdate(frame.Data)
		if !ok {
			continue
		}
		select {
		case <-ctx.Done():
			return healthy, ctx.Err()
		case out <- event:
			healthy = true
		}
	}
}

func (c *alpacaConnector) redactStreamMessage(message string) string {
	for _, value := range []string{c.credentials.APISecret, c.credentials.APIKey} {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[redacted]")
		}
	}
	return message
}

func (c *alpacaConnector) parseAlpacaTradeUpdate(raw json.RawMessage) (fwtrading.Event, bool) {
	var header struct {
		Event *string         `json:"event"`
		Order json.RawMessage `json:"order"`
	}
	if json.Unmarshal(raw, &header) != nil || header.Event == nil {
		slog.Debug("alpaca: ignoring trade update with invalid header", "connection", c.externalID)
		return fwtrading.Event{}, false
	}
	terminal := false
	switch *header.Event {
	case "canceled", "expired", "rejected", "replaced":
		terminal = true
	case "fill", "partial_fill":
	default:
		return fwtrading.Event{}, false
	}
	var reference map[string]json.RawMessage
	if json.Unmarshal(header.Order, &reference) != nil {
		slog.Debug("alpaca: ignoring trade update without ord ref", "connection", c.externalID)
		return fwtrading.Event{}, false
	}
	var ref fwtrading.OrderRef
	if err := json.Unmarshal(reference["id"], &ref.VenueOrderID); err != nil {
		ref.VenueOrderID = ""
	}
	if err := json.Unmarshal(reference["client_order_id"], &ref.ClientOrderID); err != nil {
		ref.ClientOrderID = ""
	}
	if ref.VenueOrderID == "" && ref.ClientOrderID == "" {
		slog.Debug("alpaca: ignoring trade update without ord ref", "connection", c.externalID)
		return fwtrading.Event{}, false
	}
	event := fwtrading.Event{Kind: fwtrading.EventOrder, Ref: ref}
	if terminal {
		return event, true
	}
	var update struct {
		Order     alpacaOrder `json:"order"`
		Timestamp time.Time   `json:"timestamp"`
		Price     string      `json:"price"`
		Quantity  string      `json:"qty"`
	}
	if json.Unmarshal(raw, &update) != nil {
		slog.Debug("alpaca: reconciling unparsable trade update", "connection", c.externalID)
		return event, true
	}
	if update.Timestamp.IsZero() {
		slog.Debug("alpaca: reconciling fill without timestamp", "connection", c.externalID)
		return event, true
	}
	qty, err := parseAlpacaDecimal(update.Order.Quantity, "order.qty", true)
	cum, cumErr := parseAlpacaDecimal(update.Order.FilledQuantity, "order.filled_qty", false)
	_, fillErr := parseAlpacaDecimal(update.Quantity, "qty", true)
	_, priceErr := parseAlpacaDecimal(update.Price, "price", true)
	if err != nil || cumErr != nil || fillErr != nil || priceErr != nil {
		slog.Debug("alpaca: reconciling invalid fill decimals", "connection", c.externalID)
		return event, true
	}
	leaves := qty.Sub(cum)
	if leaves.IsNegative() {
		leaves = decimal.Zero
	}
	event.Kind = fwtrading.EventFill
	event.Fill = fwtrading.Fill{
		Quantity: update.Quantity, Price: update.Price,
		CumQuantity: update.Order.FilledQuantity, LeavesQuantity: leaves.String(),
		Final: *header.Event == "fill", At: update.Timestamp,
	}
	return event, true
}
