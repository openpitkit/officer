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
	"strconv"
	"sync"
	"time"
)

// defaultMockInterval is the emit period the mock connector uses when none is
// configured. Short so a demo shows movement quickly.
const defaultMockInterval = time.Second

// mockConnector emits synthetic quotes for each subscription on a fixed timer.
// It is for demos and tests: the price walks deterministically per instrument
// (a fixed base plus a tick counter), so a test with a known interval and tick
// count can assert exact values.
type mockConnector struct {
	interval time.Duration
	now      func() time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// NewMockConnector builds a mock connector emitting every interval. A
// non-positive interval falls back to defaultMockInterval.
func NewMockConnector(interval time.Duration) *mockConnector {
	if interval <= 0 {
		interval = defaultMockInterval
	}
	return &mockConnector{interval: interval, now: time.Now}
}

// Subscribe starts a goroutine that emits one synthetic quote per subscription
// each interval and returns the channel they arrive on. The channel is closed
// when ctx is cancelled or Close is called. Each instrument's mark walks by 1
// per tick from a base of 100, with bid/ask one unit either side, so the output
// is deterministic for a given tick count.
func (c *mockConnector) Subscribe(
	ctx context.Context, subs []Subscription,
) (<-chan QuoteUpdate, error) {
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	out := make(chan QuoteUpdate)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(out)

		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()

		tick := 0
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				tick++
				if !c.emitTick(runCtx, subs, tick, out) {
					return
				}
			}
		}
	}()
	return out, nil
}

// emitTick sends one quote per subscription for tick. It returns false when the
// context is cancelled mid-batch so the caller stops the loop.
func (c *mockConnector) emitTick(
	ctx context.Context, subs []Subscription, tick int, out chan<- QuoteUpdate,
) bool {
	asOf := c.now()
	for _, sub := range subs {
		mark := 100 + tick
		update := QuoteUpdate{
			AsOf:  asOf,
			Base:  sub.Base,
			Quote: sub.Quote,
			Mark:  strconv.Itoa(mark),
			Bid:   strconv.Itoa(mark - 1),
			Ask:   strconv.Itoa(mark + 1),
		}
		select {
		case <-ctx.Done():
			return false
		case out <- update:
		}
	}
	return true
}

// Close stops emitting and waits for the goroutine to finish so the channel is
// closed before Close returns. It is idempotent.
func (c *mockConnector) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
	c.wg.Wait()
}
