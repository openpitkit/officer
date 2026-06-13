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
	"testing"
	"time"
)

// TestMockConnector_EmitsDeterministicQuotes verifies the mock walks the mark by
// 1 per tick from a base of 100 with bid/ask one unit either side, tagged with
// the subscription's base/quote.
func TestMockConnector_EmitsDeterministicQuotes(t *testing.T) {
	t.Parallel()
	c := NewMockConnector(time.Millisecond)
	defer c.Close()

	subs := []Subscription{{External: "AAPL", Base: "AAPL", Quote: "USD"}}
	ch, err := c.Subscribe(context.Background(), subs)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	select {
	case got := <-ch:
		if got.Base != "AAPL" || got.Quote != "USD" {
			t.Fatalf("instrument tag = %s/%s, want AAPL/USD", got.Base, got.Quote)
		}
		// First tick: mark 101, bid 100, ask 102.
		if got.Mark != "101" || got.Bid != "100" || got.Ask != "102" {
			t.Fatalf("first tick = mark %q bid %q ask %q, want 101/100/102",
				got.Mark, got.Bid, got.Ask)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the first mock quote")
	}
}

// TestMockConnector_CloseStopsAndClosesChannel verifies Close stops emission and
// closes the channel, and is idempotent.
func TestMockConnector_CloseStopsAndClosesChannel(t *testing.T) {
	t.Parallel()
	c := NewMockConnector(time.Millisecond)
	subs := []Subscription{{External: "AAPL", Base: "AAPL", Quote: "USD"}}
	ch, err := c.Subscribe(context.Background(), subs)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	c.Close()
	c.Close() // idempotent

	// The channel is closed; draining ends.
	for range ch { //nolint:revive // intentional drain
	}
}
