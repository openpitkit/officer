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
	"sync"
	"time"

	"github.com/coder/websocket"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/okx"
	"go.openpit.dev/officer/internal/vendorhttp"
)

const (
	// Ping and pong deadlines stay below OKX's 30 s idle cut.
	okxDefaultPingInterval = 25 * time.Second
	okxDefaultPongTimeout  = 5 * time.Second
	okxReconnectMin        = 100 * time.Millisecond
	okxReconnectMax        = 5 * time.Second
	okxHandshakeTimeout    = 10 * time.Second
	okxFrameUnreadable     = "An OKX stream message could not be decoded."
	okxRefUnreadable       = "An OKX order update had no readable client order reference."
	okxOrderUnreadable     = "An OKX order update had an unreadable symbol, order id or state."
	okxFillUnreadable      = "An OKX fill update had unreadable quantities, " +
		"price, timestamp or commission."
	okxFillAssets     = "An OKX fill update's instrument assets could not be read."
	okxFillOverfilled = "An OKX fill update reported more filled than the order quantity."
)

var errOKXUnauthorized = errors.New("okx: stream authorization refused")

// SetStatusReporter installs the owner's reporter before Subscribe.
func (c *okxConnector) SetStatusReporter(reporter health.StatusReporter) {
	c.reportStatus = reporter
}

// SetDiagnosticReporter installs the owner's reporter before Subscribe/Lookup.
func (c *okxConnector) SetDiagnosticReporter(reporter health.DiagnosticReporter) {
	c.reportDiag = reporter
}

func (c *okxConnector) Subscribe(ctx context.Context) (<-chan fwtrading.Event, error) {
	return c.lifecycle.subscribe(ctx, "okx", okxReconnectMin, okxReconnectMax, errOKXUnauthorized,
		c.stream, c.reportStatus, c.reportDiag, c.redact.Replace)
}

func (c *okxConnector) Close() { c.lifecycle.close() }

type okxFrame struct {
	Event string `json:"event"`
	Code  string `json:"code"`
	Arg   struct {
		Channel  string `json:"channel"`
		InstType string `json:"instType"`
	} `json:"arg"`
	Data []json.RawMessage `json:"data"`
}

func okxAuthCode(code string) bool {
	switch code {
	case "60004", "60005", "60006", "60007", "60009", "60024", "60032":
		return true
	}
	return false
}

func okxStreamRefusal(operation, code string) error {
	readable := "unreadable code"
	if len(code) <= 8 && isDecimalID(code) {
		readable = "code " + code
	}
	err := fmt.Errorf("okx: stream %s: %s", operation, readable)
	if okxAuthCode(code) {
		err = fmt.Errorf("%w: %w", err, errOKXUnauthorized)
	}
	return err
}

func (c *okxConnector) stream(ctx context.Context, out chan<- fwtrading.Event) (bool, error) {
	report := venueStreamReporter(c.reportDiag)
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, okxHandshakeTimeout)
	defer cancelHandshake()
	conn, err := vendorhttp.DialStream(handshakeCtx, "okx", c.streamURL)
	if err != nil {
		return false, err
	}
	defer func() {
		if err := conn.CloseNow(); err != nil {
			slog.Debug("okx: close stream connection", "error", c.redact.Replace(err.Error()))
		}
	}()
	read := func(readCtx context.Context, pong func()) (okxFrame, error) {
		for {
			_, raw, err := conn.Read(readCtx)
			if err != nil {
				var closeErr websocket.CloseError
				if errors.As(err, &closeErr) {
					return okxFrame{}, fmt.Errorf(
						"okx: stream closed by venue: status %d", closeErr.Code)
				}
				return okxFrame{}, err
			}
			if string(raw) == "pong" {
				if pong != nil {
					pong()
				}
				continue
			}
			var frame okxFrame
			if json.Unmarshal(raw, &frame) != nil ||
				(frame.Event == "" && frame.Arg.Channel == "") {
				report(venueStreamUnparsable(health.DiagWarn,
					okxFrameUnreadable, venueStreamSkipped))
				continue
			}
			return frame, nil
		}
	}
	if err := c.client.WriteLogin(handshakeCtx, conn); err != nil {
		return false, err
	}
	for {
		frame, err := read(handshakeCtx, nil)
		if err != nil {
			return false, err
		}
		if (frame.Event == "login" && frame.Code != "0") || frame.Event == "error" {
			return false, okxStreamRefusal("login refused", frame.Code)
		}
		if frame.Event == "login" && frame.Code == "0" {
			break
		}
		if frame.Event == "notice" {
			return false, errors.New("okx: stream reconnect notice")
		}
	}
	if err := c.client.WriteSubscribe(handshakeCtx, conn); err != nil {
		return false, err
	}
	for {
		frame, err := read(handshakeCtx, nil)
		if err != nil {
			return false, err
		}
		if frame.Event == "error" ||
			(frame.Event == "subscribe" && frame.Code != "" && frame.Code != "0") {
			return false, okxStreamRefusal("subscription refused", frame.Code)
		}
		if frame.Event == "notice" {
			return false, errors.New("okx: stream reconnect notice")
		}
		if frame.Event == "subscribe" && frame.Arg.Channel == "orders" &&
			frame.Arg.InstType == "SPOT" {
			break
		}
	}
	if _, err := c.instruments(handshakeCtx); err != nil {
		return false, err
	}
	cancelHandshake()
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	session, err := connectStream(streamCtx, out, okxReconnectMax, c.reportStatus)
	if err != nil {
		return false, err
	}
	pingErrors := make(chan error, 1)
	writer := &okxPingWriter{conn: conn, errs: pingErrors, timeout: c.pongTimeout}
	defer writer.pong()
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		okx.PingLoop(streamCtx, writer, c.pingInterval, pingErrors)
	}()
	defer func() { cancel(); <-pingDone }()
	failure := make(chan error, 1)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case err := <-pingErrors:
			failure <- err
			cancel()
		case <-streamCtx.Done():
		}
	}()
	defer func() { cancel(); <-watchDone }()
	for {
		frame, err := read(streamCtx, writer.pong)
		if err != nil {
			select {
			case pingErr := <-failure:
				err = pingErr
			default:
			}
			return session.healthy(), err
		}
		if frame.Event == "notice" {
			return session.healthy(), errors.New("okx: stream reconnect notice")
		}
		if frame.Event == "error" {
			return session.healthy(), okxStreamRefusal("error", frame.Code)
		}
		if frame.Arg.Channel != "orders" {
			continue
		}
		for _, row := range frame.Data {
			events, problem := c.parseOKXOrder(streamCtx, row)
			if problem != nil {
				report(problem)
			}
			for _, event := range events {
				if err := session.deliver(event); err != nil {
					select {
					case pingErr := <-failure:
						err = pingErr
					default:
					}
					return session.healthy(), err
				}
			}
		}
	}
}

// The timer requires a text pong even when other order frames keep arriving.
type okxPingWriter struct {
	conn    *websocket.Conn
	errs    chan<- error
	mu      sync.Mutex
	timer   *time.Timer
	timeout time.Duration
}

func (w *okxPingWriter) Write(ctx context.Context, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	if err := w.conn.Write(writeCtx, websocket.MessageText, payload); err != nil {
		return err
	}
	w.timer = time.AfterFunc(w.timeout, func() {
		select {
		case w.errs <- errors.New("okx: stream pong timeout"):
		default:
		}
	})
	return nil
}

func (w *okxPingWriter) pong() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

func (c *okxConnector) parseOKXOrder(
	ctx context.Context, raw json.RawMessage,
) ([]fwtrading.Event, *health.Diagnostic) {
	var fields map[string]json.RawMessage
	var clOrdID string
	if json.Unmarshal(raw, &fields) != nil {
		return nil, venueStreamUnparsable(health.DiagError, okxRefUnreadable, venueStreamDropped)
	}
	if fields["clOrdId"] == nil {
		return nil, nil
	}
	if json.Unmarshal(fields["clOrdId"], &clOrdID) != nil {
		return nil, venueStreamUnparsable(health.DiagError, okxRefUnreadable, venueStreamDropped)
	}
	clientID, own := okxOfficerID(clOrdID)
	if !own {
		return nil, nil
	}
	event := fwtrading.Event{
		Kind: fwtrading.EventOrder, Ref: fwtrading.OrderRef{ClientOrderID: clientID},
	}
	var state, tradeID, amount string
	for name, target := range map[string]*string{
		"ordId": &event.Ref.VenueOrderID, "instId": &event.Ref.Symbol, "state": &state,
	} {
		if json.Unmarshal(fields[name], target) != nil || *target == "" {
			return []fwtrading.Event{event}, venueStreamUnparsable(health.DiagWarn,
				okxOrderUnreadable, venueStreamReconciled)
		}
	}
	if !isDecimalID(event.Ref.VenueOrderID) {
		event.Ref.VenueOrderID = ""
		return []fwtrading.Event{event}, venueStreamUnparsable(health.DiagWarn,
			okxOrderUnreadable, venueStreamReconciled)
	}
	if fields["tradeId"] != nil && json.Unmarshal(fields["tradeId"], &tradeID) != nil {
		return []fwtrading.Event{event}, venueStreamUnparsable(health.DiagWarn,
			okxFillUnreadable, venueStreamReconciled)
	}
	if tradeID == "" {
		if state == "canceled" || state == "mmp_canceled" {
			return []fwtrading.Event{event}, nil
		}
		return nil, nil
	}
	if json.Unmarshal(fields["fillSz"], &amount) != nil {
		return []fwtrading.Event{event}, venueStreamUnparsable(health.DiagWarn,
			okxFillUnreadable, venueStreamReconciled)
	}
	size, sizeErr := parseVenueDecimal(amount, false)
	if sizeErr != nil {
		return []fwtrading.Event{event}, venueStreamUnparsable(health.DiagWarn,
			okxFillUnreadable, venueStreamReconciled)
	}
	if size.IsZero() {
		if state == "canceled" || state == "mmp_canceled" {
			return []fwtrading.Event{event}, nil
		}
		return nil, nil
	}
	var original, cumulative, price, timestamp, fee, currency string
	for name, target := range map[string]*string{
		"sz": &original, "accFillSz": &cumulative, "fillPx": &price, "fillTime": &timestamp,
		"fillFee": &fee, "fillFeeCcy": &currency,
	} {
		if json.Unmarshal(fields[name], target) != nil {
			return []fwtrading.Event{event}, venueStreamUnparsable(health.DiagWarn,
				okxFillUnreadable, venueStreamReconciled)
		}
	}
	qty, qtyErr := parseVenueDecimal(original, true)
	cum, cumErr := parseVenueDecimal(cumulative, true)
	_, priceErr := parseVenueDecimal(price, true)
	at, atErr := okxFillTime(timestamp)
	_, knownState := okxVenueStatus(state)
	if qtyErr != nil || cumErr != nil || priceErr != nil || atErr != nil ||
		!knownState || size.GreaterThan(cum) {
		return []fwtrading.Event{event}, venueStreamUnparsable(health.DiagWarn,
			okxFillUnreadable, venueStreamReconciled)
	}
	leaves := qty.Sub(cum)
	if leaves.IsNegative() {
		return []fwtrading.Event{event}, &health.Diagnostic{
			Level: health.DiagError, Code: health.CodeInconsistentData,
			Kind: health.DiagKindProvider, Title: "Inconsistent data on the venue stream",
			Detail: okxFillOverfilled, Remediation: venueStreamNotBooked}
	}
	commission, leg, err := c.commission(ctx, event.Ref.Symbol, fee, currency, false)
	if err != nil {
		return []fwtrading.Event{event}, venueStreamUnparsable(health.DiagWarn,
			okxFillAssets, venueStreamReconciled)
	}
	event.Kind = fwtrading.EventFill
	event.Fill = fwtrading.Fill{
		Quantity: amount, Price: price, CumQuantity: cumulative, LeavesQuantity: leaves.String(),
		Final: state == "filled", At: at, Commission: commission, CommissionLeg: leg}
	events := []fwtrading.Event{event}
	if state == "canceled" || state == "mmp_canceled" {
		events = append(events, fwtrading.Event{Kind: fwtrading.EventOrder, Ref: event.Ref})
	}
	return events, nil
}
