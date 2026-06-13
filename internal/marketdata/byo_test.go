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

// TestBYOConnector_PushForwards verifies a customer push reaches the
// subscription channel.
func TestBYOConnector_PushForwards(t *testing.T) {
	t.Parallel()
	c := NewBYOConnector(4)
	defer c.Close()

	ch, err := c.Subscribe(context.Background(), nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	want := QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "100"}
	c.Push(want)

	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for forwarded push")
	}
}

// TestBYOConnector_PushAfterCloseIsNoop verifies a push after Close does not
// panic and the channel is closed.
func TestBYOConnector_PushAfterCloseIsNoop(t *testing.T) {
	t.Parallel()
	c := NewBYOConnector(1)
	ch, err := c.Subscribe(context.Background(), nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	c.Close()
	// Must not panic on a closed channel.
	c.Push(QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "1"})

	// Draining a closed channel ends.
	for range ch { //nolint:revive // intentional drain
	}
}

// TestBYOConnector_CloseIdempotent verifies Close twice is safe.
func TestBYOConnector_CloseIdempotent(t *testing.T) {
	t.Parallel()
	c := NewBYOConnector(1)
	if _, err := c.Subscribe(context.Background(), nil); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	c.Close()
	c.Close()
}
