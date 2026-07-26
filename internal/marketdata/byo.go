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
// Please see https://openpit.dev and the OWNERS file for details.

package marketdata

import (
	"context"
	"sync"
)

// byoConnector is the bring-your-own connector: the customer pushes their own
// quotes through Push, which forwards them onto the subscription channel. It
// pulls nothing from any source - it is a manual ingress, a first-class provider
// in the framework.
//
// Close must never close the channel out from under an in-flight Push (that
// would panic). The wg counts in-flight pushes: Close marks the connector closed
// (so no new push starts), waits for the in-flight ones to drain, and only then
// closes the channel. A push that observes the closed flag returns without
// touching the channel.
type byoConnector struct {
	ch     chan QuoteUpdate
	done   chan struct{}
	closed bool

	mu sync.Mutex
	wg sync.WaitGroup
}

// NewBYOConnector builds a BYO connector. buffer sizes the channel so a burst of
// pushes is not lost when the draining consumer is momentarily behind; a
// non-positive buffer is clamped to a small default.
func NewBYOConnector(buffer int) *byoConnector {
	if buffer <= 0 {
		buffer = 1
	}
	return &byoConnector{
		ch:   make(chan QuoteUpdate, buffer),
		done: make(chan struct{}),
	}
}

// Subscribe returns the channel customer pushes are forwarded onto. The
// subscription set is accepted for interface symmetry but not used: the customer
// is responsible for pushing only quotes for instruments they enabled.
func (c *byoConnector) Subscribe(
	_ context.Context, _ []Subscription,
) (<-chan QuoteUpdate, error) {
	return c.ch, nil
}

// Push forwards one quote onto the subscription channel. After Close it is a
// no-op, so a late push never panics. The send races done and ctx so a closed
// connector or unavailable consumer never blocks the producer indefinitely.
// The in-flight count registered under the lock guarantees Close cannot close
// the channel mid-send.
func (c *byoConnector) Push(ctx context.Context, update QuoteUpdate) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.wg.Add(1)
	c.mu.Unlock()
	defer c.wg.Done()

	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case c.ch <- update:
		return nil
	}
}

// Close stops accepting pushes, waits for any in-flight push to finish, and
// closes the subscription channel so the draining consumer ends. It is
// idempotent.
func (c *byoConnector) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	close(c.done)
	c.mu.Unlock()

	// Wait outside the lock so an in-flight Push that races done can complete.
	c.wg.Wait()
	close(c.ch)
}
