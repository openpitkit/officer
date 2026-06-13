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
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeFetch is a deterministic FetchFunc whose responses are queued per call.
type fakeFetch struct {
	mu      sync.Mutex
	batches [][]QuoteUpdate
	err     error
	calls   int
}

func (f *fakeFetch) fetch(_ context.Context, _ []Subscription) ([]QuoteUpdate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if len(f.batches) == 0 {
		return nil, nil
	}
	batch := f.batches[0]
	if len(f.batches) > 1 {
		f.batches = f.batches[1:]
	}
	return batch, nil
}

var pollSubs = []Subscription{{External: "AAPL", Base: "AAPL", Quote: "USD"}}

// TestPoller_EmitsImmediatelyAndCoalesces verifies the immediate fetch emits and
// an unchanged second fetch is coalesced away (no re-emit).
func TestPoller_EmitsImmediatelyAndCoalesces(t *testing.T) {
	t.Parallel()
	q := QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "100"}
	f := &fakeFetch{batches: [][]QuoteUpdate{{q}, {q}}}

	// A short interval drives at least a second tick within the deadline.
	p := NewPoller(f.fetch, time.Millisecond, nil)
	out := make(chan QuoteUpdate, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { p.Run(ctx, pollSubs, out); close(done) }()

	select {
	case got := <-out:
		if got.Mark != "100" {
			t.Fatalf("first emit mark = %q, want 100", got.Mark)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the immediate emit")
	}

	// The unchanged quote must not be re-emitted across subsequent ticks.
	select {
	case got := <-out:
		t.Fatalf("unexpected re-emit of unchanged quote: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	<-done
}

// TestPoller_EmitsChangedQuote verifies a moved price after the first tick is
// emitted.
func TestPoller_EmitsChangedQuote(t *testing.T) {
	t.Parallel()
	first := QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "100"}
	second := QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "101"}
	f := &fakeFetch{batches: [][]QuoteUpdate{{first}, {second}}}

	p := NewPoller(f.fetch, time.Millisecond, nil)
	out := make(chan QuoteUpdate, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { p.Run(ctx, pollSubs, out); close(done) }()

	marks := map[string]bool{}
	deadline := time.After(time.Second)
	for len(marks) < 2 {
		select {
		case got := <-out:
			marks[got.Mark] = true
		case <-deadline:
			t.Fatalf("timed out; saw marks %v", marks)
		}
	}
	if !marks["100"] || !marks["101"] {
		t.Fatalf("want marks 100 and 101, got %v", marks)
	}
	cancel()
	<-done
}

// TestPoller_FetchErrorReportedAndSkipped verifies a fetch error is reported via
// errFn and emits nothing.
func TestPoller_FetchErrorReportedAndSkipped(t *testing.T) {
	t.Parallel()
	f := &fakeFetch{err: errors.New("boom")}

	var mu sync.Mutex
	var seen int
	errFn := func(error) {
		mu.Lock()
		seen++
		mu.Unlock()
	}

	p := NewPoller(f.fetch, time.Millisecond, errFn)
	out := make(chan QuoteUpdate, 1)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { p.Run(ctx, pollSubs, out); close(done) }()

	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	gotErrs := seen
	mu.Unlock()
	if gotErrs == 0 {
		t.Fatal("want at least one reported fetch error")
	}
	select {
	case got := <-out:
		t.Fatalf("want no emit on fetch error, got %+v", got)
	default:
	}
}
