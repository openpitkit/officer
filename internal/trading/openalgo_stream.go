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
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/openalgo"
	"go.openpit.dev/officer/internal/vendorhttp"
)

const (
	openAlgoReconnectMin     = 100 * time.Millisecond
	openAlgoReconnectMax     = 5 * time.Second
	openAlgoHandshakeTimeout = 10 * time.Second
	openAlgoPushUnreadable   = "OpenAlgo sent an order update Officer cannot read."
	openAlgoPushUnknown      = "OpenAlgo sent an unknown order stream message."
	openAlgoHeldEvicted      = "OpenAlgo order updates arrived before their order acknowledgements; the oldest held update was dropped."
)

var errOpenAlgoUnauthorized = errors.New("openalgo: stream authorization refused")

func (c *openAlgoConnector) SetStatusReporter(report health.StatusReporter) {
	c.reportStatus = report
}

func (c *openAlgoConnector) Subscribe(ctx context.Context) (<-chan fwtrading.Event, error) {
	return c.lifecycle.subscribe(ctx, "openalgo", openAlgoReconnectMin, openAlgoReconnectMax,
		errOpenAlgoUnauthorized, c.stream, c.reportStatus, c.reportDiag, c.redact.Replace)
}

func (c *openAlgoConnector) Close() {
	c.lifecycle.close()
}

func (c *openAlgoConnector) orderUpdate(
	venueID string,
) (fwtrading.Event, bool) {
	return correlateOrderUpdate(
		c.orderCorrelator, venueID, c.reportDiag, openAlgoHeldEvicted,
	)
}

func (c *openAlgoConnector) storeClosedQuantity(id string, quantity openalgo.Decimal) {
	c.closedMu.Lock()
	defer c.closedMu.Unlock()
	if _, finished := c.orderState(id); finished {
		return
	}
	if _, exists := c.closedFills[id]; !exists {
		if len(c.closedIDs) == orderCorrelationLimit {
			// Evict the oldest unmapped entry first, and the oldest
			// overall only when every entry is mapped, so a mapped
			// order's confirmation outlives unrelated account traffic.
			// An own order is unmapped until Send's reply or a Lookup
			// maps it, so its entry can be evicted first until then.
			oldest := 0
			for i, storedID := range c.closedIDs {
				if mapped, _ := c.orderState(storedID); !mapped {
					oldest = i
					break
				}
			}
			delete(c.closedFills, c.closedIDs[oldest])
			c.closedIDs = slices.Delete(c.closedIDs, oldest, oldest+1)
		}
		c.closedIDs = append(c.closedIDs, id)
	}
	c.closedFills[id] = quantity
}

func (c *openAlgoConnector) forgetClosedQuantity(id string) {
	c.closedMu.Lock()
	defer c.closedMu.Unlock()
	delete(c.closedFills, id)
	if index := slices.Index(c.closedIDs, id); index >= 0 {
		c.closedIDs = slices.Delete(c.closedIDs, index, index+1)
	}
}

func (c *openAlgoConnector) stream(ctx context.Context, out chan<- fwtrading.Event) (_ bool, err error) {
	defer func() {
		if err != nil && !errors.Is(err, errOpenAlgoUnauthorized) {
			err = errors.New(c.redact.Replace(err.Error()))
		}
	}()
	handshakeCtx, cancel := context.WithTimeout(ctx, openAlgoHandshakeTimeout)
	defer cancel()
	conn, err := vendorhttp.DialStream(handshakeCtx, "openalgo", c.wsURL)
	if err != nil {
		return false, err
	}
	defer func() {
		if err := conn.CloseNow(); err != nil {
			slog.Debug("openalgo: close stream connection", "error", c.redact.Replace(err.Error()))
		}
	}()
	if err := openalgo.Authenticate(handshakeCtx, conn, c.apiKey, c.redact); err != nil {
		var streamErr *openalgo.StreamError
		if errors.As(err, &streamErr) && streamErr.Code == "AUTHENTICATION_ERROR" {
			return false, fmt.Errorf("%w: %w", errOpenAlgoUnauthorized, streamErr)
		}
		return false, err
	}
	id := uuid.NewString()
	if err := openalgo.WriteSubscribeOrders(handshakeCtx, conn, id); err != nil {
		return false, err
	}
	payload, err := openalgo.ReadTextReply(handshakeCtx, conn, "subscription")
	if err != nil {
		return false, err
	}
	if err := openalgo.ParseSubscribeOrdersReply(payload, id, c.redact); err != nil {
		return false, err
	}
	cancel()
	session, err := connectStream(ctx, out, openAlgoReconnectMax, c.reportStatus)
	if err != nil {
		return false, err
	}

	type frame struct {
		typ     websocket.MessageType
		payload []byte
	}
	report := venueStreamReporter(c.reportDiag)
	mode := "live"
	if c.mode == domain.TradingModePaper {
		mode = "analyze"
	}
	return runOrderStream(session, c.orderCorrelator, c.orderUpdate,
		func(readCtx context.Context) (frame, error) {
			typ, payload, err := conn.Read(readCtx)
			return frame{typ, payload}, err
		}, func(next frame, emit func(venueID string) error) error {
			if next.typ != websocket.MessageText {
				report(venueStreamUnparsable(health.DiagWarn, openAlgoPushUnreadable, venueStreamSkipped))
				return nil
			}
			update, err := openalgo.ParseOrderUpdate(next.payload, c.redact)
			if err != nil {
				var streamErr *openalgo.StreamError
				if errors.As(err, &streamErr) {
					return err
				}
				detail := openAlgoPushUnreadable
				if errors.Is(err, openalgo.ErrUnknownPush) {
					detail = openAlgoPushUnknown
				}
				report(venueStreamUnparsable(health.DiagWarn, detail, venueStreamSkipped))
				return nil
			}
			if update.Mode != mode {
				// Skip other-mode updates, including a D1-mapped order's.
				// A closed D1 order cannot get stream confirmation here;
				// its Lookup ends in D5 for the operator.
				return nil
			}
			id := string(update.OrderID)
			if slices.Contains(openAlgoClosedStatuses, update.Status) && update.FilledQuantity != "" {
				// Publish confirmation before the event can trigger Lookup.
				c.storeClosedQuantity(id, update.FilledQuantity)
			}
			return emit(id)
		})
}
