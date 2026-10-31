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
	"go.openpit.dev/officer/internal/alpaca"
)

const (
	alpacaReconnectMin     = 100 * time.Millisecond
	alpacaReconnectMax     = 5 * time.Second
	alpacaHandshakeTimeout = 10 * time.Second
)

var errAlpacaUnauthorized = errors.New("alpaca: stream authorization refused")

// The stream's diagnostics carry a fixed detail per site and never embed
// frame content.
const (
	alpacaInconsistentTitle = "Inconsistent data on the venue stream"

	alpacaFrameUnreadable  = "A stream message could not be decoded."
	alpacaAuthUnreadable   = "The stream authorization reply could not be decoded."
	alpacaListenUnreadable = "The stream subscription reply could not be decoded."
	alpacaHeaderUnreadable = "A trade update had no readable event type."
	alpacaRefUnreadable    = "A trade update had no usable order reference."
	alpacaVenueIDUnusable  = "A trade update had no usable venue order id."
	alpacaFillUnreadable   = "A fill update could not be decoded."
	alpacaFillUntimed      = "A fill update had no timestamp."
	alpacaFillDecimals     = "A fill update had an unreadable quantity or price."
	alpacaFillOverfilled   = "A fill update reported more filled than the order quantity."
)

// SetStatusReporter installs the owner's stream status reporter. It must be
// called before Subscribe.
func (c *alpacaConnector) SetStatusReporter(report health.StatusReporter) {
	c.reportStatus = report
}

// SetDiagnosticReporter installs the owner's diagnostic reporter. It must be
// called before Subscribe or Lookup.
func (c *alpacaConnector) SetDiagnosticReporter(report health.DiagnosticReporter) {
	c.reportDiag = report
}

// Subscribe requires both reporters: the stream reports its state and the
// venue data it cannot read through them instead of logging.
func (c *alpacaConnector) Subscribe(ctx context.Context) (<-chan fwtrading.Event, error) {
	return c.lifecycle.subscribe(ctx, "alpaca", alpacaReconnectMin, alpacaReconnectMax,
		errAlpacaUnauthorized, c.stream, c.reportStatus, c.reportDiag, c.redactStreamMessage)
}

func (c *alpacaConnector) Close() {
	c.lifecycle.close()
}

type alpacaFrame struct {
	Stream string          `json:"stream"`
	Action string          `json:"action"`
	Data   json.RawMessage `json:"data"`
}

func (c *alpacaConnector) readAlpacaFrame(ctx context.Context, conn *websocket.Conn, report func(*health.Diagnostic)) (alpacaFrame, error) {
	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			return alpacaFrame{}, err
		}
		var frame alpacaFrame
		if json.Unmarshal(payload, &frame) != nil || (frame.Stream == "" && frame.Action == "") {
			report(venueStreamUnparsable(health.DiagWarn, alpacaFrameUnreadable, venueStreamSkipped))
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

func (c *alpacaConnector) stream(ctx context.Context, out chan<- fwtrading.Event) (bool, error) {
	report := venueStreamReporter(c.reportDiag)
	handshakeCtx, cancel := context.WithTimeout(ctx, alpacaHandshakeTimeout)
	defer cancel()
	conn, err := alpaca.DialStream(handshakeCtx, c.streamURL)
	if err != nil {
		return false, err
	}
	defer func() {
		if err := conn.CloseNow(); err != nil {
			slog.Debug("alpaca: close stream connection", "connection", c.externalID, "error", err)
		}
	}()
	if err := alpaca.WriteStreamAuth(handshakeCtx, conn, c.credentials); err != nil {
		return false, err
	}
	for {
		frame, err := c.readAlpacaFrame(handshakeCtx, conn, report)
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
			report(venueStreamUnparsable(health.DiagWarn, alpacaAuthUnreadable, venueStreamSkipped))
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
	if err := alpaca.WriteStreamFrame(handshakeCtx, conn, listen); err != nil {
		return false, err
	}
	for {
		frame, err := c.readAlpacaFrame(handshakeCtx, conn, report)
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
			report(venueStreamUnparsable(health.DiagWarn, alpacaListenUnreadable, venueStreamSkipped))
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
	session, err := connectStream(ctx, out, alpacaReconnectMax, c.reportStatus)
	if err != nil {
		return false, err
	}
	for {
		frame, err := c.readAlpacaFrame(ctx, conn, report)
		if err != nil {
			return session.healthy(), err
		}
		if frame.Stream != "trade_updates" {
			continue
		}
		event, ok, problem := c.parseAlpacaTradeUpdate(frame.Data)
		if problem != nil {
			report(problem)
		}
		if !ok {
			continue
		}
		if err := session.deliver(event); err != nil {
			return session.healthy(), err
		}
	}
}

// redactStreamMessage redacts every spelling of every credential of the
// connection with the redactor the backend boundary applies too, so a
// credential inside a longer credential's escaped spelling leaves no fragment.
func (c *alpacaConnector) redactStreamMessage(message string) string {
	return c.redact.Replace(message)
}

// parseAlpacaTradeUpdate maps one trade update to the event to emit, if any,
// and returns the diagnostic of an update it could not map in full. An update
// without a readable event type or a usable client order id is dropped (no
// event). One without a usable venue order id, or a fill it could not fully
// read, is reconciled (an order event, never a fill) with an unparsable_data
// diagnostic. An overfill, a fill reporting more filled than the order
// quantity, is not booked: it yields an order event, never a fill, with an
// inconsistent_data diagnostic. An unknown event type is ignored, and a
// readable fill of a notional order is reconciled, without a diagnostic.
func (c *alpacaConnector) parseAlpacaTradeUpdate(raw json.RawMessage) (fwtrading.Event, bool, *health.Diagnostic) {
	var header struct {
		Event *string         `json:"event"`
		Order json.RawMessage `json:"order"`
	}
	if json.Unmarshal(raw, &header) != nil || header.Event == nil {
		return fwtrading.Event{}, false, venueStreamUnparsable(health.DiagError, alpacaHeaderUnreadable, venueStreamDropped)
	}
	terminal := false
	switch *header.Event {
	case "canceled", "expired", "rejected", "replaced":
		terminal = true
	case "fill", "partial_fill":
	default:
		return fwtrading.Event{}, false, nil
	}
	var reference map[string]json.RawMessage
	if json.Unmarshal(header.Order, &reference) != nil {
		return fwtrading.Event{}, false, venueStreamUnparsable(health.DiagError, alpacaRefUnreadable, venueStreamDropped)
	}
	// The runtime routes an event by its client order id alone, so there is
	// nothing to emit without one.
	clientID, ok := alpacaOrderID(reference["client_order_id"])
	if !ok {
		return fwtrading.Event{}, false, venueStreamUnparsable(health.DiagError, alpacaRefUnreadable, venueStreamDropped)
	}
	event := fwtrading.Event{Kind: fwtrading.EventOrder, Ref: fwtrading.OrderRef{ClientOrderID: clientID}}
	venueID, ok := alpacaOrderID(reference["id"])
	if !ok {
		return event, true, venueStreamUnparsable(health.DiagWarn, alpacaVenueIDUnusable, venueStreamReconciled)
	}
	event.Ref.VenueOrderID = venueID
	if terminal {
		return event, true, nil
	}
	var update struct {
		Order     alpacaOrder `json:"order"`
		Timestamp time.Time   `json:"timestamp"`
		Price     string      `json:"price"`
		Quantity  string      `json:"qty"`
	}
	if json.Unmarshal(raw, &update) != nil {
		return event, true, venueStreamUnparsable(health.DiagWarn, alpacaFillUnreadable, venueStreamReconciled)
	}
	if update.Timestamp.IsZero() {
		return event, true, venueStreamUnparsable(health.DiagWarn, alpacaFillUntimed, venueStreamReconciled)
	}
	qty, err := parseAlpacaDecimal(update.Order.Quantity, "order.qty", true)
	cum, cumErr := parseAlpacaDecimal(update.Order.FilledQuantity, "order.filled_qty", false)
	_, fillErr := parseAlpacaDecimal(update.Quantity, "qty", true)
	_, priceErr := parseAlpacaDecimal(update.Price, "price", true)
	// A notional (dollar-amount) order has no qty, absent or null, and states
	// its size as a notional instead, which is read in place of the qty; every
	// other field is read as for a quantity order. Officer sends quantity
	// orders only: the order was placed elsewhere on the account.
	var notional string
	isNotional := (reference["qty"] == nil || string(reference["qty"]) == "null") &&
		json.Unmarshal(reference["notional"], &notional) == nil
	if isNotional {
		_, err = parseAlpacaDecimal(notional, "order.notional", true)
	}
	if err != nil || cumErr != nil || fillErr != nil || priceErr != nil {
		return event, true, venueStreamUnparsable(health.DiagWarn, alpacaFillDecimals, venueStreamReconciled)
	}
	// Without the order quantity a notional order's fill cannot be mapped,
	// so it is reconciled; the shape is documented, not unreadable.
	if isNotional {
		return event, true, nil
	}
	leaves := qty.Sub(cum)
	if leaves.IsNegative() {
		return event, true, &health.Diagnostic{
			Level: health.DiagError, Code: health.CodeInconsistentData,
			Kind: health.DiagKindProvider, Title: alpacaInconsistentTitle,
			Detail: alpacaFillOverfilled, Remediation: venueStreamNotBooked,
		}
	}
	event.Kind = fwtrading.EventFill
	event.Fill = fwtrading.Fill{
		Quantity: update.Quantity, Price: update.Price,
		CumQuantity: update.Order.FilledQuantity, LeavesQuantity: leaves.String(),
		Final: *header.Event == "fill", At: update.Timestamp,
	}
	return event, true, nil
}

// alpacaOrderID reads one order reference field, usable only as a non-empty
// JSON string: absent, null, any other JSON type and "" are not.
func alpacaOrderID(raw json.RawMessage) (string, bool) {
	var id string
	if json.Unmarshal(raw, &id) != nil || id == "" {
		return "", false
	}
	return id, true
}
