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
	"strconv"
	"time"

	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/binance"
)

const (
	binanceReconnectMin     = 100 * time.Millisecond
	binanceReconnectMax     = 5 * time.Second
	binanceHandshakeTimeout = 10 * time.Second

	binanceFrameUnreadable     = "A stream message could not be decoded."
	binanceSubscribeUnreadable = "The stream subscription reply " +
		"could not be decoded."
	binanceHeaderUnreadable = "A trade update had no readable event type."
	binanceRefUnreadable    = "A trade update had no usable order reference."
	binanceSymbolUnreadable = "A trade update had an unreadable symbol."
	binanceVenueIDUnusable  = "A trade update had no usable venue order id."
	binanceFillUnreadable   = "A fill update could not be decoded."
	binanceFillUntimed      = "A fill update had no timestamp."
	binanceFillDecimals     = "A fill update had an unreadable quantity, " +
		"price or commission."
	binanceFillAssets     = "A fill update's symbol assets could not be read."
	binanceFillOverfilled = "A fill update reported more filled than " +
		"the order quantity."
)

var errBinanceUnauthorized = errors.New(
	"binance: stream authorization refused")

// SetStatusReporter installs the owner's reporter before Subscribe.
func (c *binanceConnector) SetStatusReporter(report health.StatusReporter) {
	c.reportStatus = report
}

// SetDiagnosticReporter installs the owner's reporter before Subscribe/Lookup.
func (c *binanceConnector) SetDiagnosticReporter(
	report health.DiagnosticReporter,
) {
	c.reportDiag = report
}

func (c *binanceConnector) Subscribe(
	ctx context.Context,
) (<-chan fwtrading.Event, error) {
	return c.lifecycle.subscribe(ctx, "binance",
		binanceReconnectMin, binanceReconnectMax, errBinanceUnauthorized,
		c.stream, c.reportStatus, c.reportDiag, c.redact.Replace)
}

func (c *binanceConnector) Close() {
	c.lifecycle.close()
}

type binanceFrame struct {
	ID             string          `json:"id"`
	Status         *int            `json:"status"`
	SubscriptionID json.Number     `json:"subscriptionId"`
	Event          json.RawMessage `json:"event"`
	Result         struct {
		SubscriptionID json.Number `json:"subscriptionId"`
	} `json:"result"`
	Error struct {
		Code    *int    `json:"code"`
		Message *string `json:"msg"`
	} `json:"error"`
}

func (c *binanceConnector) stream(ctx context.Context,
	out chan<- fwtrading.Event,
) (bool, error) {
	report := venueStreamReporter(c.reportDiag)
	handshakeCtx, cancel := context.WithTimeout(ctx, binanceHandshakeTimeout)
	defer cancel()
	conn, err := binance.DialStream(handshakeCtx, c.streamURL)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := conn.CloseNow(); closeErr != nil {
			slog.Debug("binance: close stream connection",
				"error", closeErr)
		}
	}()
	now := time.Now()
	id := strconv.FormatInt(now.UnixNano(), 10)
	if err := binance.WriteSubscribe(handshakeCtx, conn,
		c.credentials, id, now); err != nil {
		return false, err
	}
	read := func(readCtx context.Context) (binanceFrame, error) {
		for {
			_, payload, err := conn.Read(readCtx)
			if err != nil {
				return binanceFrame{}, err
			}
			var frame binanceFrame
			if json.Unmarshal(payload, &frame) != nil ||
				(frame.ID == "" && frame.Event == nil) {
				report(venueStreamUnparsable(health.DiagWarn,
					binanceFrameUnreadable, venueStreamSkipped))
				continue
			}
			return frame, nil
		}
	}
	var subscriptionID json.Number
	for {
		frame, err := read(handshakeCtx)
		if err != nil {
			return false, err
		}
		if frame.ID != id {
			continue
		}
		if frame.Status != nil {
			if *frame.Status == 200 && binanceID(frame.Result.SubscriptionID) {
				subscriptionID = frame.Result.SubscriptionID
				break
			}
			if *frame.Status != 200 && frame.Error.Code != nil &&
				frame.Error.Message != nil {
				code := *frame.Error.Code
				if code == -2014 || code == -2015 ||
					*frame.Status == 401 || *frame.Status == 403 {
					return false, errBinanceUnauthorized
				}
				return false, fmt.Errorf("binance: stream subscription: %d: %s",
					code, c.redact.Replace(*frame.Error.Message))
			}
		}
		report(venueStreamUnparsable(health.DiagWarn,
			binanceSubscribeUnreadable, venueStreamSkipped))
	}
	cancel()
	session, err := connectStream(ctx, out, binanceReconnectMax, c.reportStatus)
	if err != nil {
		return false, err
	}
	for {
		frame, err := read(ctx)
		if err != nil {
			return session.healthy(), err
		}
		if frame.Event == nil {
			continue
		}
		var header map[string]json.RawMessage
		var eventType string
		if json.Unmarshal(frame.Event, &header) != nil ||
			json.Unmarshal(header["e"], &eventType) != nil || eventType == "" {
			report(venueStreamUnparsable(health.DiagError,
				binanceHeaderUnreadable, venueStreamDropped))
			continue
		}
		if eventType == "serverShutdown" {
			return session.healthy(), errors.New("binance: server shutdown")
		}
		if !binanceID(frame.SubscriptionID) {
			report(venueStreamUnparsable(health.DiagWarn,
				binanceFrameUnreadable, venueStreamSkipped))
			continue
		}
		if frame.SubscriptionID != subscriptionID {
			continue
		}
		if eventType == "eventStreamTerminated" {
			return session.healthy(), errors.New("binance: event stream terminated")
		}
		if eventType != "executionReport" {
			continue
		}
		event, ok, problem := c.parseBinanceExecution(ctx, frame.Event)
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

func (c *binanceConnector) parseBinanceExecution(ctx context.Context,
	raw json.RawMessage,
) (fwtrading.Event, bool, *health.Diagnostic) {
	var fields map[string]json.RawMessage
	var execution string
	if json.Unmarshal(raw, &fields) != nil ||
		json.Unmarshal(fields["x"], &execution) != nil || execution == "" {
		return fwtrading.Event{}, false, venueStreamUnparsable(health.DiagError,
			binanceHeaderUnreadable, venueStreamDropped)
	}
	switch execution {
	case "CANCELED", "REJECTED", "EXPIRED", "TRADE_PREVENTION", "REPLACED":
	case "TRADE":
	default:
		return fwtrading.Event{}, false, nil
	}
	var clientID, originalID string
	if execution == "CANCELED" && fields["C"] != nil &&
		json.Unmarshal(fields["C"], &originalID) != nil {
		return fwtrading.Event{}, false, venueStreamUnparsable(health.DiagError,
			binanceRefUnreadable, venueStreamDropped)
	}
	if originalID != "" {
		clientID = originalID
	} else if json.Unmarshal(fields["c"], &clientID) != nil {
		return fwtrading.Event{}, false, venueStreamUnparsable(health.DiagError,
			binanceRefUnreadable, venueStreamDropped)
	}
	if clientID == "" {
		return fwtrading.Event{}, false, venueStreamUnparsable(health.DiagError,
			binanceRefUnreadable, venueStreamDropped)
	}
	event := fwtrading.Event{Kind: fwtrading.EventOrder,
		Ref: fwtrading.OrderRef{ClientOrderID: clientID}}
	var venueID json.Number
	if json.Unmarshal(fields["i"], &venueID) != nil || !binanceID(venueID) {
		return event, true, venueStreamUnparsable(health.DiagWarn,
			binanceVenueIDUnusable, venueStreamReconciled)
	}
	event.Ref.VenueOrderID = venueID.String()
	if fields["s"] != nil &&
		json.Unmarshal(fields["s"], &event.Ref.Symbol) != nil {
		return event, true, venueStreamUnparsable(health.DiagWarn,
			binanceSymbolUnreadable, venueStreamReconciled)
	}
	if execution != "TRADE" {
		return event, true, nil
	}
	var update struct {
		Quantity, Price, Cumulative, Original, Status, Commission string
		At                                                        *int64
		Asset                                                     *string
	}
	// Binance distinguishes keys by case; struct decoding also matches case
	// insensitively and would confuse X/x, T/t, q/Q and z/Z.
	for key, target := range map[string]any{
		"l": &update.Quantity, "L": &update.Price,
		"z": &update.Cumulative, "q": &update.Original,
		"X": &update.Status, "T": &update.At,
		"n": &update.Commission, "N": &update.Asset,
	} {
		if json.Unmarshal(fields[key], target) != nil {
			return event, true, venueStreamUnparsable(health.DiagWarn,
				binanceFillUnreadable, venueStreamReconciled)
		}
	}
	if update.At == nil || *update.At <= 0 {
		return event, true, venueStreamUnparsable(health.DiagWarn,
			binanceFillUntimed, venueStreamReconciled)
	}
	qty, qtyErr := parseBinanceDecimal(update.Original, true)
	cum, cumErr := parseBinanceDecimal(update.Cumulative, false)
	_, amountErr := parseBinanceDecimal(update.Quantity, true)
	_, priceErr := parseBinanceDecimal(update.Price, true)
	_, commissionErr := parseBinanceDecimal(update.Commission, false)
	if qtyErr != nil || cumErr != nil || amountErr != nil ||
		priceErr != nil || commissionErr != nil {
		return event, true, venueStreamUnparsable(health.DiagWarn,
			binanceFillDecimals, venueStreamReconciled)
	}
	leaves := qty.Sub(cum)
	if leaves.IsNegative() {
		return event, true, &health.Diagnostic{
			Level: health.DiagError, Code: health.CodeInconsistentData,
			Kind:   health.DiagKindProvider,
			Title:  "Inconsistent data on the venue stream",
			Detail: binanceFillOverfilled, Remediation: venueStreamNotBooked,
		}
	}
	var asset string
	if update.Asset != nil {
		asset = *update.Asset
	}
	commission, leg, err := c.commission(ctx, event.Ref.Symbol,
		update.Commission, asset)
	if err != nil {
		return event, true, venueStreamUnparsable(health.DiagWarn,
			binanceFillAssets, venueStreamReconciled)
	}
	event.Kind = fwtrading.EventFill
	event.Fill = fwtrading.Fill{
		Quantity: update.Quantity, Price: update.Price,
		CumQuantity: update.Cumulative, LeavesQuantity: leaves.String(),
		Final: update.Status == "FILLED", At: time.UnixMilli(*update.At).UTC(),
		Commission: commission, CommissionLeg: leg,
	}
	return event, true, nil
}
