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
	"slices"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/etoro"
)

const (
	etoroReconnectMin     = 100 * time.Millisecond
	etoroReconnectMax     = 5 * time.Second
	etoroHandshakeTimeout = 10 * time.Second

	// 256 distinct orders cover an acknowledgement race burst without letting
	// unrelated account traffic grow the queue indefinitely. Eviction is safe:
	// an acknowledged order leaves the runtime's pending set, but the next
	// EventConnected reconciliation resolves it from REST history.
	etoroHeldLimit = 256

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

// mapOrder wakes the stream instead of publishing on the runtime's channel:
// Send and Lookup run on the same worker that consumes that channel.
func (c *etoroConnector) mapOrder(venueID, clientID string) {
	c.ordersMu.Lock()
	c.orderIDs[venueID] = clientID
	c.ordersMu.Unlock()
	select {
	case c.mapped <- struct{}{}:
	default:
		// A pending wake already covers every mapping installed so far.
	}
}

func (c *etoroConnector) orderUpdate(venueID string) (fwtrading.Event, bool) {
	c.ordersMu.Lock()
	if slices.Contains(c.finished, venueID) {
		c.ordersMu.Unlock()
		return fwtrading.Event{}, false
	}
	if clientID := c.orderIDs[venueID]; clientID != "" {
		c.ordersMu.Unlock()
		return fwtrading.Event{Kind: fwtrading.EventOrder,
			Ref: fwtrading.OrderRef{ClientOrderID: clientID, VenueOrderID: venueID}}, true
	}
	if slices.Contains(c.held, venueID) {
		c.ordersMu.Unlock()
		return fwtrading.Event{}, false
	}
	evicted := len(c.held) == etoroHeldLimit
	if evicted {
		c.held = slices.Delete(c.held, 0, 1)
	}
	c.held = append(c.held, venueID)
	c.ordersMu.Unlock()
	if evicted {
		c.reportDiag(health.Diagnostic{
			Code: health.CodeInternalError, Kind: health.DiagKindProvider, Level: health.DiagWarn,
			Title: "The correlation queue for stream updates is full", Detail: etoroHeldEvicted,
			Remediation: "Updates for orders placed outside Officer can fill this queue. " +
				"An affected Officer order is resolved at the next stream reconnect or restart.",
		})
	}
	return fwtrading.Event{}, false
}

func (c *etoroConnector) nextMappedUpdate() (fwtrading.Event, bool) {
	c.ordersMu.Lock()
	defer c.ordersMu.Unlock()
	for i, venueID := range c.held {
		if clientID := c.orderIDs[venueID]; clientID != "" {
			c.held = slices.Delete(c.held, i, i+1)
			return fwtrading.Event{Kind: fwtrading.EventOrder,
				Ref: fwtrading.OrderRef{ClientOrderID: clientID, VenueOrderID: venueID}}, true
		}
	}
	return fwtrading.Event{}, false
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
	conn, err := etoro.DialStream(handshakeCtx, c.streamURL)
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

	// One reader lets a newly installed mapping wake the stream while the
	// socket is idle. Cancel and join it on every session exit, including Close.
	readCtx, stopRead := context.WithCancel(ctx)
	type result struct {
		frame etoroFrame
		err   error
	}
	frames := make(chan result)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			frame, err := readEtoroFrame(readCtx, conn)
			select {
			case frames <- result{frame, err}:
			case <-readCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { stopRead(); <-done }()
	report := venueStreamReporter(c.reportDiag)
	for {
		for event, ok := c.nextMappedUpdate(); ok; event, ok = c.nextMappedUpdate() {
			if err := session.deliver(event); err != nil {
				return session.healthy(), err
			}
		}
		select {
		case <-ctx.Done():
			return session.healthy(), ctx.Err()
		case <-c.mapped:
		case next := <-frames:
			if next.err != nil {
				return session.healthy(), next.err
			}
			var messages []json.RawMessage
			if next.frame.Operation != "" || json.Unmarshal(next.frame.Messages, &messages) != nil || messages == nil {
				report(venueStreamUnparsable(health.DiagWarn, etoroPushUnreadable, venueStreamSkipped))
				continue
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
				// eToro demo probe P9: v1 single-position market-close updates are assumed to arrive as Trading.OrderForCloseMultiple.Update; an unknown type carrying an OrderID still triggers Lookup.
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
				if event, ok := c.orderUpdate(orderID.String()); ok {
					if err := session.deliver(event); err != nil {
						return session.healthy(), err
					}
				}
			}
		}
	}
}
