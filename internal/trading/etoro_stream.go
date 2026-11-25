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
	"github.com/google/uuid"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/etoro"
	"go.openpit.dev/officer/internal/vendorhttp"
)

const (
	etoroReconnectMin     = 100 * time.Millisecond
	etoroReconnectMax     = 5 * time.Second
	etoroHandshakeTimeout = 10 * time.Second

	etoroPushUnreadable = "An eToro stream push could not be decoded."
	etoroPushUnknown    = "An eToro stream push had an unknown type."
	etoroHeldEvicted    = "An eToro stream update awaiting order correlation was evicted."
	etoroPushRefresh    = "Officer used only the order id from the push, when one was present, " +
		"to refresh that order from the venue's order history, and kept reading the stream. " +
		"This is a venue or Officer issue, not your configuration " +
		"- please report it if it repeats."
)

var errEtoroUnauthorized = errors.New("etoro: stream authorization refused")

// SetStatusReporter installs the owner's reporter before Subscribe.
func (c *etoroConnector) SetStatusReporter(report health.StatusReporter) {
	c.reportStatus = report
}

// SetDiagnosticReporter installs the owner's reporter before Subscribe or Lookup.
func (c *etoroConnector) SetDiagnosticReporter(report health.DiagnosticReporter) {
	c.reportDiag = report
}

func (c *etoroConnector) Subscribe(ctx context.Context) (<-chan fwtrading.Event, error) {
	return c.lifecycle.subscribe(ctx, "etoro", etoroReconnectMin, etoroReconnectMax,
		errEtoroUnauthorized, c.stream, c.reportStatus, c.reportDiag, c.redact.Replace)
}

func (c *etoroConnector) Close() {
	c.lifecycle.close()
}

func (c *etoroConnector) orderUpdate(venueID string) (fwtrading.Event, bool) {
	return correlateOrderUpdate(c.orderCorrelator, venueID, c.reportDiag, etoroHeldEvicted)
}

type etoroFrame struct {
	ID           string          `json:"id"`
	Operation    string          `json:"operation"`
	Success      *bool           `json:"success"`
	ErrorMessage string          `json:"errorMessage"`
	ErrorCode    string          `json:"errorCode"`
	Messages     json.RawMessage `json:"messages"`
}

func readEtoroFrame(ctx context.Context, conn *websocket.Conn) (etoroFrame, error) {
	for {
		kind, payload, err := conn.Read(ctx)
		if err != nil {
			return etoroFrame{}, err
		}
		// The AsyncAPI connection contract documents a periodic binary
		// heartbeat, with no application response or payload schema.
		if kind == websocket.MessageBinary {
			continue
		}
		var frame *etoroFrame
		if json.Unmarshal(payload, &frame) != nil || frame == nil {
			return etoroFrame{}, errors.New("etoro: invalid stream envelope")
		}
		return *frame, nil
	}
}

func (c *etoroConnector) streamAck(ctx context.Context, conn *websocket.Conn, id, operation string) error {
	frame, err := readEtoroFrame(ctx, conn)
	if err != nil {
		return err
	}
	if frame.ID != id || frame.Operation != operation || frame.Success == nil || frame.Messages != nil {
		return errors.New("etoro: stream acknowledgement mismatch")
	}
	if !*frame.Success {
		if operation == "Authenticate" && (frame.ErrorCode == "InvalidKey" || frame.ErrorCode == "Unauthorized") {
			return errEtoroUnauthorized
		}
		return fmt.Errorf("etoro: stream operation failed: %s: %s",
			c.redact.Replace(frame.ErrorCode), c.redact.Replace(frame.ErrorMessage))
	}
	return nil
}

func (c *etoroConnector) stream(ctx context.Context, out chan<- fwtrading.Event) (_ bool, err error) {
	defer func() {
		if err != nil && !errors.Is(err, errEtoroUnauthorized) {
			err = errors.New(c.redact.Replace(err.Error()))
		}
	}()
	handshakeCtx, cancel := context.WithTimeout(ctx, etoroHandshakeTimeout)
	defer cancel()
	conn, err := vendorhttp.DialStream(handshakeCtx, "etoro", c.streamURL)
	if err != nil {
		return false, err
	}
	defer func() {
		if err := conn.CloseNow(); err != nil {
			slog.Debug("etoro: close stream connection", "error", c.redact.Replace(err.Error()))
		}
	}()
	id := uuid.NewString()
	if err := etoro.WriteAuthenticate(handshakeCtx, conn, id, c.credentials); err != nil {
		return false, err
	}
	if err := c.streamAck(handshakeCtx, conn, id, "Authenticate"); err != nil {
		return false, err
	}
	topic := "private"
	if c.demo {
		topic = "private:demo"
	}
	id = uuid.NewString()
	if err := etoro.WriteSubscribe(handshakeCtx, conn, id, []string{topic}); err != nil {
		return false, err
	}
	if err := c.streamAck(handshakeCtx, conn, id, "Subscribe"); err != nil {
		return false, err
	}
	cancel()
	session, err := connectStream(ctx, out, etoroReconnectMax, c.reportStatus)
	if err != nil {
		return false, err
	}

	report := venueStreamReporter(c.reportDiag)
	return runOrderStream(session, c.orderCorrelator, c.orderUpdate,
		func(readCtx context.Context) (etoroFrame, error) {
			return readEtoroFrame(readCtx, conn)
		}, func(frame etoroFrame, emit func(venueID string) error) error {
			var messages []json.RawMessage
			if frame.Operation != "" || json.Unmarshal(frame.Messages, &messages) != nil || messages == nil {
				report(venueStreamUnparsable(health.DiagWarn, etoroPushUnreadable, venueStreamSkipped))
				return nil
			}
			for _, raw := range messages {
				var push struct {
					Topic   string `json:"topic"`
					Type    string `json:"type"`
					ID      string `json:"id"`
					Content string `json:"content"`
				}
				if json.Unmarshal(raw, &push) != nil || push.Topic != topic || push.ID == "" {
					report(venueStreamUnparsable(health.DiagWarn, etoroPushUnreadable, venueStreamSkipped))
					continue
				}
				known := true
				switch push.Type {
				case "Trading.Position.Opened", "Trading.Position.Closed":
					continue
				case "Trading.OrderForOpen.Update", "Trading.OrderForCloseMultiple.Update":
				default:
					report(venueStreamUnparsable(health.DiagWarn, etoroPushUnknown, etoroPushRefresh))
					known = false
				}
				// eToro demo probe P9: v1 single-position market-close updates
				// are assumed to arrive as Trading.OrderForCloseMultiple.Update;
				// an unknown type carrying an OrderID still triggers Lookup.
				var update struct {
					OrderID json.Number `json:"OrderID"`
				}
				if json.Unmarshal([]byte(push.Content), &update) != nil {
					if known {
						report(venueStreamUnparsable(health.DiagWarn, etoroPushUnreadable, venueStreamSkipped))
					}
					continue
				}
				orderID, err := etoroInteger(update.OrderID, "stream OrderID", true)
				if err != nil {
					if known {
						report(venueStreamUnparsable(health.DiagWarn, etoroPushUnreadable, venueStreamSkipped))
					}
					continue
				}
				// eToro demo probe P1: RequestGuid is not a reliable client
				// reference. Only REST establishes OrderID -> ClientOrderID;
				// stream status, quantity and fills never enter accounting.
				if err := emit(orderID.String()); err != nil {
					return err
				}
			}
			return nil
		})
}
