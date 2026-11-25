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
	"time"

	"github.com/coder/websocket"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/bybit"
	"go.openpit.dev/officer/internal/vendorhttp"
)

const (
	bybitReconnectMin        = 100 * time.Millisecond
	bybitReconnectMax        = 5 * time.Second
	bybitHandshakeTimeout    = 10 * time.Second
	bybitPingInterval        = 20 * time.Second
	bybitFrameUnreadable     = "A stream message could not be decoded."
	bybitTradeUnreadable     = "A trade update could not be decoded."
	bybitSubscribeUnreadable = "The stream subscription reply could not be decoded."
	bybitAuthUnreadable      = "The stream authorization reply could not be decoded."
	bybitRefUnreadable       = "A trade update had no usable order reference."
	bybitSymbolUnreadable    = "A trade update had an unreadable symbol."
	bybitVenueIDUnusable     = "A trade update had no usable venue order id."
	bybitHeaderUnreadable    = "An order update had no readable order status."
	bybitFillUnreadable      = "A fill update could not be decoded."
	bybitFillAssets          = "A fill update's symbol assets could not be read."
	bybitFillExtraFees       = "A fill update returned unsupported extra fees."
)

var errBybitUnauthorized = errors.New("bybit: stream authorization refused")

func (c *bybitConnector) SetStatusReporter(report health.StatusReporter) {
	c.reportStatus = report
}

func (c *bybitConnector) SetDiagnosticReporter(report health.DiagnosticReporter) {
	c.reportDiag = report
}

func (c *bybitConnector) Subscribe(ctx context.Context) (<-chan fwtrading.Event, error) {
	return c.lifecycle.subscribe(ctx, "bybit", bybitReconnectMin,
		bybitReconnectMax, errBybitUnauthorized, c.stream,
		c.reportStatus, c.reportDiag, c.redact.Replace)
}

func (c *bybitConnector) Close() { c.lifecycle.close() }

type bybitFrame struct {
	Op      string          `json:"op"`
	Topic   string          `json:"topic"`
	Success *bool           `json:"success"`
	Message string          `json:"ret_msg"`
	Data    json.RawMessage `json:"data"`
}

func (c *bybitConnector) stream(ctx context.Context,
	out chan<- fwtrading.Event,
) (bool, error) {
	report := venueStreamReporter(c.reportDiag)
	handshakeCtx, cancel := context.WithTimeout(ctx, bybitHandshakeTimeout)
	defer cancel()
	conn, err := vendorhttp.DialStream(handshakeCtx, "bybit", c.streamURL)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := conn.CloseNow(); closeErr != nil {
			slog.Debug("bybit: close stream connection", "error",
				c.redact.Replace(closeErr.Error()))
		}
	}()
	read := func(readCtx context.Context) (bybitFrame, error) {
		for {
			_, payload, err := conn.Read(readCtx)
			if err != nil {
				return bybitFrame{}, err
			}
			var frame bybitFrame
			if json.Unmarshal(payload, &frame) != nil || (frame.Op == "" && frame.Topic == "") {
				report(venueStreamUnparsable(health.DiagWarn,
					bybitFrameUnreadable, venueStreamSkipped))
				continue
			}
			return frame, nil
		}
	}
	if err := bybit.WriteAuth(handshakeCtx, conn, c.credentials,
		time.Now().Add(bybitHandshakeTimeout)); err != nil {
		return false, err
	}
	for _, op := range []string{"auth", "subscribe"} {
		if op == "subscribe" {
			if err := bybit.WriteSubscribe(handshakeCtx, conn, "order.spot", "execution.spot"); err != nil {
				return false, err
			}
		}
		for {
			frame, err := read(handshakeCtx)
			if err != nil {
				return false, err
			}
			if frame.Op != op {
				continue
			}
			if frame.Success == nil {
				detail := bybitAuthUnreadable
				if op == "subscribe" {
					detail = bybitSubscribeUnreadable
				}
				report(venueStreamUnparsable(health.DiagWarn, detail, venueStreamSkipped))
				continue
			}
			if !*frame.Success {
				if op == "auth" {
					return false, errBybitUnauthorized
				}
				return false, fmt.Errorf("bybit: stream subscription: %s", c.redact.Replace(frame.Message))
			}
			break
		}
	}
	cancel()
	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	session, err := connectStream(sessionCtx, out, bybitReconnectMax, c.reportStatus)
	if err != nil {
		return false, err
	}
	pingErrors := make(chan error, 1)
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		bybit.PingLoop(sessionCtx, func(ctx context.Context, payload []byte) error {
			return conn.Write(ctx, websocket.MessageText, payload)
		}, bybitPingInterval, pingErrors)
		cancelSession()
	}()
	defer func() { cancelSession(); <-pingDone }()
	for {
		frame, err := read(sessionCtx)
		if err != nil {
			select {
			case pingErr := <-pingErrors:
				err = pingErr
			default:
			}
			return session.healthy(), err
		}
		if frame.Topic != "order.spot" && frame.Topic != "execution.spot" {
			continue
		}
		var items []json.RawMessage
		if json.Unmarshal(frame.Data, &items) != nil || items == nil {
			report(venueStreamUnparsable(health.DiagError,
				bybitTradeUnreadable, venueStreamDropped))
			continue
		}
		for _, item := range items {
			event, ok, problem := c.parseBybitUpdate(sessionCtx, frame.Topic, item)
			if problem != nil {
				report(problem)
			}
			if ok {
				if err := session.deliver(event); err != nil {
					return session.healthy(), err
				}
			}
		}
	}
}

func (c *bybitConnector) parseBybitUpdate(ctx context.Context,
	topic string, raw json.RawMessage,
) (fwtrading.Event, bool, *health.Diagnostic) {
	var fields map[string]json.RawMessage
	var clientID string
	if json.Unmarshal(raw, &fields) != nil || fields["orderLinkId"] == nil ||
		string(fields["orderLinkId"]) == "null" ||
		json.Unmarshal(fields["orderLinkId"], &clientID) != nil {
		return fwtrading.Event{}, false, venueStreamUnparsable(health.DiagError,
			bybitRefUnreadable, venueStreamDropped)
	}
	if clientID == "" {
		// An empty orderLinkId marks a foreign order, not an Officer order.
		return fwtrading.Event{}, false, nil
	}
	event := fwtrading.Event{Kind: fwtrading.EventOrder,
		Ref: fwtrading.OrderRef{ClientOrderID: clientID}}
	venueErr := json.Unmarshal(fields["orderId"], &event.Ref.VenueOrderID)
	symbolErr := json.Unmarshal(fields["symbol"], &event.Ref.Symbol)
	if venueErr != nil || event.Ref.VenueOrderID == "" {
		return event, true, venueStreamUnparsable(health.DiagWarn,
			bybitVenueIDUnusable, venueStreamReconciled)
	}
	if symbolErr != nil || event.Ref.Symbol == "" {
		return event, true, venueStreamUnparsable(health.DiagWarn,
			bybitSymbolUnreadable, venueStreamReconciled)
	}
	if topic == "order.spot" {
		var status string
		if json.Unmarshal(fields["orderStatus"], &status) != nil || status == "" {
			return event, true, venueStreamUnparsable(health.DiagWarn,
				bybitHeaderUnreadable, venueStreamReconciled)
		}
		switch status {
		case "New", "PartiallyFilled", "Filled":
			return fwtrading.Event{}, false, nil
		default:
			return event, true, nil
		}
	}
	var execution bybitExecution
	if json.Unmarshal(raw, &execution) != nil {
		return event, true, venueStreamUnparsable(health.DiagWarn,
			bybitFillUnreadable, venueStreamReconciled)
	}
	base, quote, err := c.coins(ctx, execution.Symbol)
	if err != nil {
		return event, true, venueStreamUnparsable(health.DiagWarn,
			bybitFillAssets, venueStreamReconciled)
	}
	fill, err := execution.fill(base, quote)
	if err != nil {
		detail := bybitFillUnreadable
		if errors.Is(err, errBybitExtraFees) {
			detail = bybitFillExtraFees
		}
		return event, true, venueStreamUnparsable(health.DiagWarn, detail, venueStreamReconciled)
	}
	event.Kind, event.Fill = fwtrading.EventFill, fill.fill
	return event, true, nil
}
