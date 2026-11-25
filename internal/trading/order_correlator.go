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
	"slices"
	"sync"

	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

// orderCorrelationLimit bounds both FIFOs. 256 distinct orders cover an
// acknowledgement race burst without letting unrelated account traffic grow
// the queue indefinitely. Eviction is safe: an acknowledged order leaves the
// runtime's pending set, but the next EventConnected reconciliation resolves
// it from venue history.
const orderCorrelationLimit = 256

// orderCorrelator maps venue order IDs from stream pushes to client order IDs.
// Bounded FIFOs hold unmatched pushes and recently finished order IDs.
// A mapping installed by mapOrder lives until finishOrder, so the caller bounds
// orderIDs by finishing every order it maps.
// Wakes on mapped are coalesced: drain nextMappedUpdate until it returns false
// before blocking on mapped.
type orderCorrelator struct {
	ordersMu sync.Mutex
	orderIDs map[string]string
	finished []string
	held     []string
	// mapped signals that held updates may now have client order IDs.
	mapped chan struct{}
}

func newOrderCorrelator() *orderCorrelator {
	return &orderCorrelator{
		orderIDs: make(map[string]string), mapped: make(chan struct{}, 1),
	}
}

// mapOrder wakes the stream instead of publishing on the runtime's channel:
// Send and Lookup run on the same worker that consumes that channel.
func (c *orderCorrelator) mapOrder(venueID, clientID string) {
	c.ordersMu.Lock()
	c.orderIDs[venueID] = clientID
	c.ordersMu.Unlock()
	select {
	case c.mapped <- struct{}{}:
	default:
		// A pending wake already covers every mapping installed so far.
	}
}

// correlate returns event and ready for a mapped order that can be delivered.
// evicted reports whether this push evicted the oldest held ID. The caller must
// report an eviction with orderCorrelationEvicted and its venue's detail text.
func (c *orderCorrelator) correlate(venueID string) (event fwtrading.Event, ready, evicted bool) {
	c.ordersMu.Lock()
	if slices.Contains(c.finished, venueID) {
		c.ordersMu.Unlock()
		return fwtrading.Event{}, false, false
	}
	if clientID := c.orderIDs[venueID]; clientID != "" {
		c.ordersMu.Unlock()
		return fwtrading.Event{Kind: fwtrading.EventOrder,
			Ref: fwtrading.OrderRef{ClientOrderID: clientID, VenueOrderID: venueID}}, true, false
	}
	if slices.Contains(c.held, venueID) {
		c.ordersMu.Unlock()
		return fwtrading.Event{}, false, false
	}
	evicted = len(c.held) == orderCorrelationLimit
	if evicted {
		c.held = slices.Delete(c.held, 0, 1)
	}
	c.held = append(c.held, venueID)
	c.ordersMu.Unlock()
	return fwtrading.Event{}, false, evicted
}

// orderState reads mapping and completion atomically without changing them.
func (c *orderCorrelator) orderState(venueID string) (mapped, finished bool) {
	c.ordersMu.Lock()
	defer c.ordersMu.Unlock()
	return c.orderIDs[venueID] != "", slices.Contains(c.finished, venueID)
}

// nextMappedUpdate removes and returns a held update with a client order ID.
func (c *orderCorrelator) nextMappedUpdate() (fwtrading.Event, bool) {
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

// finishOrder must be called with the venue order ID once the venue reports a
// terminal status. It drops the mapping, records the ID in the bounded finished
// FIFO so later pushes for it are ignored, and removes any held push for it.
func (c *orderCorrelator) finishOrder(venueID string) {
	c.ordersMu.Lock()
	delete(c.orderIDs, venueID)
	if !slices.Contains(c.finished, venueID) {
		if len(c.finished) == orderCorrelationLimit {
			c.finished = slices.Delete(c.finished, 0, 1)
		}
		c.finished = append(c.finished, venueID)
	}
	if i := slices.Index(c.held, venueID); i >= 0 {
		c.held = slices.Delete(c.held, i, i+1)
	}
	c.ordersMu.Unlock()
}

// orderCorrelationEvicted builds the warning diagnostic for a held ID eviction.
// The detail describes the venue's affected stream updates.
func orderCorrelationEvicted(detail string) health.Diagnostic {
	return health.Diagnostic{
		Code: health.CodeInternalError, Kind: health.DiagKindProvider, Level: health.DiagWarn,
		Title: "The correlation queue for stream updates is full", Detail: detail,
		Remediation: "Updates for orders placed outside Officer can fill this queue. " +
			"An affected Officer order is resolved at the next stream reconnect or restart.",
	}
}
