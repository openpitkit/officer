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

	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/coinbase"
)

const (
	coinbaseReconnectMin     = 250 * time.Millisecond
	coinbaseReconnectMax     = 30 * time.Second
	coinbaseSubscribeTimeout = 5 * time.Second
	coinbaseReadTimeout      = 4 * time.Second

	coinbaseFrameUnreadable              = "A stream message could not be decoded."
	coinbaseSequenceUnreadable           = "A stream message had no usable sequence number."
	coinbaseTimestampUnreadable          = "A stream message had no readable timestamp."
	coinbaseTimestampInvalid             = "A stream message had an invalid timestamp."
	coinbaseHeartbeatTimestampUnreadable = "A heartbeat message had no readable timestamp."
	coinbaseHeartbeatTimestampInvalid    = "A heartbeat message had an invalid timestamp."
	coinbaseHeartbeatUnreadable          = "A heartbeat message's events could not be decoded."
	coinbaseHeartbeatEmpty               = "A heartbeat message had no events."
	coinbaseHeartbeatEventUnreadable     = "A heartbeat event had no readable time or counter."
	coinbaseUserEventsUnreadable         = "A user message had no readable events."
	coinbaseOrdersAbsent                 = "A user event had no orders."
	coinbaseOrderIDAbsent                = "An order update had no usable venue order id."
	coinbaseUserEventUnknown             = "A user event had an unknown type."
)

var errCoinbaseUnauthorized = errors.New("coinbase: stream subscription refused")

func (c *coinbaseConnector) SetStatusReporter(report health.StatusReporter) { c.reportStatus = report }
func (c *coinbaseConnector) SetDiagnosticReporter(report health.DiagnosticReporter) {
	c.reportDiag = report
}
func (c *coinbaseConnector) Close() { c.lifecycle.close() }

func (c *coinbaseConnector) Subscribe(ctx context.Context) (<-chan fwtrading.Event, error) {
	return c.lifecycle.subscribe(ctx, "coinbase", coinbaseReconnectMin, coinbaseReconnectMax,
		errCoinbaseUnauthorized, c.stream, c.reportStatus, c.reportDiag, c.client.Scrub)
}

func (c *coinbaseConnector) stream(ctx context.Context, out chan<- fwtrading.Event) (bool, error) {
	report := venueStreamReporter(c.reportDiag)
	unreadable := func(detail string) error {
		report(venueStreamUnparsable(health.DiagError, detail, venueStreamReconciled))
		return errors.New(detail)
	}
	subscribeCtx, cancel := context.WithTimeout(ctx, coinbaseSubscribeTimeout)
	defer cancel()
	conn, err := coinbase.DialStream(subscribeCtx, c.streamURL)
	if err != nil {
		return false, err
	}
	defer func() {
		if err := conn.CloseNow(); err != nil {
			slog.Debug("coinbase: close stream", "error", c.client.Scrub(err.Error()))
		}
	}()
	for _, channel := range []string{"user", "heartbeats"} {
		if err := coinbase.WriteSubscribe(subscribeCtx, conn, channel, c.credentials); err != nil {
			return false, err
		}
	}
	var session *streamSession
	healthy := func() bool { return session != nil && session.healthy() }
	var previous int64
	sequenced := false
readLoop:
	for {
		readParent := ctx
		if session == nil {
			readParent = subscribeCtx
		}
		readCtx, readCancel := context.WithTimeout(readParent, coinbaseReadTimeout)
		_, payload, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			return healthy(), errors.New(c.client.Scrub(err.Error()))
		}
		var frame struct {
			Type      string          `json:"type"`
			Message   json.RawMessage `json:"message"`
			Channel   string          `json:"channel"`
			Timestamp json.RawMessage `json:"timestamp"`
			Sequence  *int64          `json:"sequence_num"`
			Events    json.RawMessage `json:"events"`
		}
		if json.Unmarshal(payload, &frame) != nil {
			return healthy(), unreadable(coinbaseFrameUnreadable)
		}
		if frame.Type == "error" {
			message := "message is absent"
			if len(frame.Message) > 0 {
				message = "message is not a JSON string"
				var text string
				if frame.Message[0] == '"' && json.Unmarshal(frame.Message, &text) == nil {
					message = text
				}
			}
			message = c.client.Scrub(message)
			detail := fmt.Sprintf("Coinbase refused the subscription: %q", message)
			report(&health.Diagnostic{Level: health.DiagError, Kind: health.DiagKindProvider,
				Code: health.CodeInvalidProviderConfig, Title: "Coinbase stream subscription refused",
				Detail: detail, Remediation: "Check the Coinbase API key permissions and account access. Officer retries the subscription."})
			return healthy(), fmt.Errorf("%w: %q", errCoinbaseUnauthorized, message)
		}
		if frame.Sequence == nil || *frame.Sequence < 0 {
			return healthy(), unreadable(coinbaseSequenceUnreadable)
		}
		// Sequence numbers cover the connection, including unknown channels.
		// A gap means a dropped message; only REST reconcile restores state.
		if sequenced && *frame.Sequence != previous+1 {
			return healthy(), errors.New("coinbase: stream sequence gap or out-of-order message; REST reconciliation required")
		}
		previous, sequenced = *frame.Sequence, true
		if frame.Channel != "user" && frame.Channel != "heartbeats" {
			continue
		}
		var timestamp string
		var timestampProblem string
		if json.Unmarshal(frame.Timestamp, &timestamp) != nil {
			timestampProblem = coinbaseTimestampUnreadable
		} else if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
			timestampProblem = coinbaseTimestampInvalid
		}
		if timestampProblem != "" {
			if frame.Channel == "heartbeats" {
				switch timestampProblem {
				case coinbaseTimestampUnreadable:
					timestampProblem = coinbaseHeartbeatTimestampUnreadable
				case coinbaseTimestampInvalid:
					timestampProblem = coinbaseHeartbeatTimestampInvalid
				}
				report(venueStreamUnparsable(health.DiagWarn, timestampProblem, venueStreamSkipped))
				continue
			}
			return healthy(), unreadable(timestampProblem)
		}
		if frame.Channel == "heartbeats" {
			var events []json.RawMessage
			if len(frame.Events) > 0 && frame.Events[0] == '{' {
				events = []json.RawMessage{frame.Events}
			} else if json.Unmarshal(frame.Events, &events) != nil {
				report(venueStreamUnparsable(health.DiagWarn, coinbaseHeartbeatUnreadable, venueStreamSkipped))
				continue
			}
			if len(events) == 0 {
				report(venueStreamUnparsable(health.DiagWarn, coinbaseHeartbeatEmpty, venueStreamSkipped))
				continue
			}
			for _, raw := range events {
				var heartbeat struct {
					Time    string          `json:"current_time"`
					Counter json.RawMessage `json:"heartbeat_counter"`
				}
				if json.Unmarshal(raw, &heartbeat) != nil || heartbeat.Time == "" || len(heartbeat.Counter) == 0 || string(heartbeat.Counter) == "null" {
					report(venueStreamUnparsable(health.DiagWarn, coinbaseHeartbeatEventUnreadable, venueStreamSkipped))
					continue readLoop
				}
			}
			continue
		}
		var events []struct {
			Type   string `json:"type"`
			Orders *[]struct {
				ID       string `json:"order_id"`
				ClientID string `json:"client_order_id"`
			} `json:"orders"`
		}
		if json.Unmarshal(frame.Events, &events) != nil || len(events) == 0 {
			return healthy(), unreadable(coinbaseUserEventsUnreadable)
		}
		for _, event := range events {
			if event.Orders == nil {
				return healthy(), unreadable(coinbaseOrdersAbsent)
			}
			switch event.Type {
			case "snapshot":
				if session == nil {
					session, err = connectStream(ctx, out, coinbaseReconnectMax, c.reportStatus)
					if err != nil {
						return false, err
					}
					cancel()
				}
			case "update":
				if session == nil {
					continue
				}
				for _, order := range *event.Orders {
					if order.ClientID == "" {
						continue
					}
					if order.ID == "" {
						return healthy(), unreadable(coinbaseOrderIDAbsent)
					}
					if err := session.deliver(fwtrading.Event{Kind: fwtrading.EventOrder,
						Ref: fwtrading.OrderRef{ClientOrderID: order.ClientID, VenueOrderID: order.ID}}); err != nil {
						return healthy(), err
					}
				}
			default:
				return healthy(), unreadable(coinbaseUserEventUnknown)
			}
		}
	}
}
