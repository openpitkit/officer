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
	"go.openpit.dev/officer/internal/backoff"
)

const (
	alpacaReconnectMin     = 100 * time.Millisecond
	alpacaReconnectMax     = 5 * time.Second
	alpacaHandshakeTimeout = 10 * time.Second
)

var errAlpacaUnauthorized = errors.New("alpaca: stream authorization refused")

// The stream's diagnostics carry a fixed detail per site and never embed
// frame content; the unparsable_data ones share one title.
const (
	alpacaUnparsableTitle   = "Unreadable data on the venue stream"
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

	alpacaSkipped = "Officer skipped the message and kept reading the " +
		"stream. This is a venue or Officer issue, not your configuration " +
		"- please report it if it repeats."
	alpacaDropped = "Officer dropped the update; a fill it carried is not " +
		"reconciled until the next stream reconnect. This is a venue or " +
		"Officer issue, not your configuration - please report it."
	alpacaReconciled = "Officer reconciles the order from the venue's " +
		"order and fill history instead. This is a venue or Officer issue, " +
		"not your configuration - please report it if it repeats."
	alpacaNotBooked = "Officer did not book the update. An order Officer " +
		"still has open is reconciled from the venue's order and fill " +
		"history; an excess on an order Officer has already finished is " +
		"not booked anywhere. Compare the account's position at the venue " +
		"with Officer's. This is a venue issue, not your configuration."
)

// alpacaUnparsable builds the unparsable_data diagnostic of one stream site.
func alpacaUnparsable(level, detail, remediation string) *health.Diagnostic {
	return &health.Diagnostic{
		Level: level, Code: health.CodeUnparsableData,
		Kind: health.DiagKindProvider, Title: alpacaUnparsableTitle,
		Detail: detail, Remediation: remediation,
	}
}

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
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("alpaca: connector is closed")
	}
	if c.done != nil {
		return nil, errors.New("alpaca: stream already subscribed")
	}
	if c.reportStatus == nil || c.reportDiag == nil {
		return nil, errors.New("alpaca: stream requires a status and a diagnostic reporter")
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
	attempt := 0
	refused := false
	for ctx.Err() == nil {
		healthy, err := c.stream(ctx, out)
		if ctx.Err() != nil {
			return
		}
		if healthy {
			attempt = 0
			refused = false
		}
		if errors.Is(err, errAlpacaUnauthorized) {
			refused = true
		}
		attempt++
		delay := backoff.Delay(attempt, alpacaReconnectMin, alpacaReconnectMax)
		if refused {
			// A refused authorization holds the longest wait until a
			// healthy stream proves the credentials work again.
			delay = alpacaReconnectMax
		}
		c.reportStatus(false, c.redactStreamMessage(err.Error()))
		if err := backoff.Sleep(ctx, delay); err != nil {
			return
		}
	}
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
			report(alpacaUnparsable(health.DiagWarn, alpacaFrameUnreadable, alpacaSkipped))
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

func (c *alpacaConnector) stream(ctx context.Context, out chan<- fwtrading.Event) (healthy bool, err error) {
	// An unparsable_data site reports once per connection, so a venue
	// sending malformed frames cannot turn every frame into a report; a
	// site's detail is fixed and identifies it. An inconsistent_data
	// diagnostic reports every time: each one is a fill the venue reports
	// beyond the order quantity, and one on an order Officer has already
	// finished leaves no other trace.
	reported := make(map[string]bool)
	report := func(diag *health.Diagnostic) {
		if diag.Code == health.CodeUnparsableData {
			if reported[diag.Detail] {
				return
			}
			reported[diag.Detail] = true
		}
		c.reportDiag(*diag)
	}
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
			report(alpacaUnparsable(health.DiagWarn, alpacaAuthUnreadable, alpacaSkipped))
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
			report(alpacaUnparsable(health.DiagWarn, alpacaListenUnreadable, alpacaSkipped))
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
	c.reportStatus(true, "")
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
		frame, err := c.readAlpacaFrame(ctx, conn, report)
		if err != nil {
			return healthy, err
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
		select {
		case <-ctx.Done():
			return healthy, ctx.Err()
		case out <- event:
			healthy = true
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
		return fwtrading.Event{}, false, alpacaUnparsable(health.DiagError, alpacaHeaderUnreadable, alpacaDropped)
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
		return fwtrading.Event{}, false, alpacaUnparsable(health.DiagError, alpacaRefUnreadable, alpacaDropped)
	}
	// The runtime routes an event by its client order id alone, so there is
	// nothing to emit without one.
	clientID, ok := alpacaOrderID(reference["client_order_id"])
	if !ok {
		return fwtrading.Event{}, false, alpacaUnparsable(health.DiagError, alpacaRefUnreadable, alpacaDropped)
	}
	event := fwtrading.Event{Kind: fwtrading.EventOrder, Ref: fwtrading.OrderRef{ClientOrderID: clientID}}
	venueID, ok := alpacaOrderID(reference["id"])
	if !ok {
		return event, true, alpacaUnparsable(health.DiagWarn, alpacaVenueIDUnusable, alpacaReconciled)
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
		return event, true, alpacaUnparsable(health.DiagWarn, alpacaFillUnreadable, alpacaReconciled)
	}
	if update.Timestamp.IsZero() {
		return event, true, alpacaUnparsable(health.DiagWarn, alpacaFillUntimed, alpacaReconciled)
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
		return event, true, alpacaUnparsable(health.DiagWarn, alpacaFillDecimals, alpacaReconciled)
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
			Detail: alpacaFillOverfilled, Remediation: alpacaNotBooked,
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
