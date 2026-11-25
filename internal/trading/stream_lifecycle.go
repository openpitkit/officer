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
	"sync"
	"time"

	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/backoff"
)

// streamLifecycle owns one connector's stream loop. It is single-use: a
// connector cannot subscribe again after its first subscription or close.
type streamLifecycle struct {
	mu     sync.Mutex
	closed bool
	cancel context.CancelFunc
	done   chan struct{}
}

type streamRefusals struct {
	closed     string
	subscribed string
	reporters  string
}

// subscribe starts the loop through start with vendor-prefixed refusal texts
// and redacted status messages. It requires both reporters and a non-nil error
// whenever stream returns while ctx is live.
func (s *streamLifecycle) subscribe(
	ctx context.Context, vendor string,
	minDelay, maxDelay time.Duration, refusedAuth error,
	stream func(context.Context, chan<- fwtrading.Event) (bool, error),
	reportStatus health.StatusReporter, reportDiag health.DiagnosticReporter,
	redact func(string) string,
) (<-chan fwtrading.Event, error) {
	return s.start(ctx, streamRefusals{
		closed:     vendor + ": connector is closed",
		subscribed: vendor + ": stream already subscribed",
		reporters:  vendor + ": stream requires a status and a diagnostic reporter",
	}, minDelay, maxDelay, refusedAuth, stream, reportStatus, reportDiag, redact)
}

// start starts the loop and requires both reporters. reportDiag is checked
// here and used by stream. Whenever stream returns while ctx is live, it must
// return a non-nil error; statusMessage receives err.Error() for the status.
// A healthy session resets the backoff and clears the refused-authorization
// hold. An error matching refusedAuth holds maxDelay until a healthy session.
// A nil refusedAuth means no refused-authorization hold.
func (s *streamLifecycle) start(
	ctx context.Context, refusals streamRefusals,
	minDelay, maxDelay time.Duration, refusedAuth error,
	stream func(context.Context, chan<- fwtrading.Event) (bool, error),
	reportStatus health.StatusReporter, reportDiag health.DiagnosticReporter,
	statusMessage func(string) string,
) (<-chan fwtrading.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New(refusals.closed)
	}
	if s.done != nil {
		return nil, errors.New(refusals.subscribed)
	}
	if reportStatus == nil || reportDiag == nil {
		return nil, errors.New(refusals.reporters)
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.done = make(chan struct{})
	out := make(chan fwtrading.Event)
	go func() {
		// Close output before done so close(), which waits on done, sees it closed.
		defer close(s.done)
		defer close(out)
		defer s.cancel()
		attempt := 0
		refused := false
		for ctx.Err() == nil {
			healthy, err := stream(ctx, out)
			if ctx.Err() != nil {
				return
			}
			if healthy {
				attempt = 0
				refused = false
			}
			if errors.Is(err, refusedAuth) {
				refused = true
			}
			attempt++
			delay := backoff.Delay(attempt, minDelay, maxDelay)
			if refused {
				// A refused authorization holds the longest wait until a
				// healthy stream proves the credentials work again.
				delay = maxDelay
			}
			reportStatus(false, statusMessage(err.Error()))
			if err := backoff.Sleep(ctx, delay); err != nil {
				return
			}
		}
	}()
	return out, nil
}

// close cancels the stream context and waits for the loop and its output to
// close.
func (s *streamLifecycle) close() {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

// streamSession measures session health from the initial EventConnected
// delivery.
type streamSession struct {
	ctx         context.Context
	out         chan<- fwtrading.Event
	maxDelay    time.Duration
	connectedAt time.Time
	delivered   bool
}

// connectStream reports connected status before sending EventConnected and
// starts the session clock when that delivery completes. ctx controls delivery
// and may be a per-session context.
func connectStream(ctx context.Context, out chan<- fwtrading.Event,
	maxDelay time.Duration, reportStatus health.StatusReporter,
) (*streamSession, error) {
	reportStatus(true, "")
	session := &streamSession{ctx: ctx, out: out, maxDelay: maxDelay}
	if err := session.deliver(fwtrading.Event{Kind: fwtrading.EventConnected}); err != nil {
		return nil, err
	}
	session.connectedAt = time.Now()
	return session, nil
}

// correlateOrderUpdate fulfills orderCorrelator.correlate's eviction contract.
func correlateOrderUpdate(c *orderCorrelator, venueID string,
	report health.DiagnosticReporter, evictionDetail string,
) (fwtrading.Event, bool) {
	event, ready, evicted := c.correlate(venueID)
	if evicted {
		report(orderCorrelationEvicted(evictionDetail))
	}
	return event, ready
}

// runOrderStream reads frames and delivers correlated order updates.
// read runs on its own goroutine and must return when its context is cancelled.
// Every session exit, including Close, cancels and joins that goroutine.
// handle runs on the session goroutine. State it publishes before calling emit
// is in place before that ID is correlated and delivered, and before held
// updates are later drained. emit calls correlate and delivers a ready event
// before returning; handle must return emit's error immediately.
// correlate must report evictions by orderCorrelator.correlate's contract,
// normally by wrapping correlateOrderUpdate with the venue's detail.
// Any handle error ends the session.
// Drain all ready held updates before blocking: mapping wakes coalesce, so one
// signal can stand for several newly mapped orders.
func runOrderStream[Frame any](session *streamSession, c *orderCorrelator,
	correlate func(venueID string) (fwtrading.Event, bool),
	read func(context.Context) (Frame, error),
	handle func(frame Frame, emit func(venueID string) error) error,
) (bool, error) {
	// One reader lets a newly installed mapping wake the stream while the
	// socket is idle. Cancel and join it on every session exit, including Close.
	readCtx, stopRead := context.WithCancel(session.ctx)
	type result struct {
		frame Frame
		err   error
	}
	frames := make(chan result)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			frame, err := read(readCtx)
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
	emit := func(venueID string) error {
		if event, ready := correlate(venueID); ready {
			return session.deliver(event)
		}
		return nil
	}
	for {
		// Drain every ready held update before blocking: mapping wakeups
		// coalesce, so one signal can stand for several newly mapped orders.
		for event, ok := c.nextMappedUpdate(); ok; event, ok = c.nextMappedUpdate() {
			if err := session.deliver(event); err != nil {
				return session.healthy(), err
			}
		}
		select {
		case <-session.ctx.Done():
			return session.healthy(), session.ctx.Err()
		case <-c.mapped:
		case next := <-frames:
			if next.err != nil {
				return session.healthy(), next.err
			}
			if err := handle(next.frame, emit); err != nil {
				return session.healthy(), err
			}
		}
	}
}

// deliver selects on the ctx passed to connectStream. EventConnected neither
// marks the session healthy nor restarts its clock.
func (s *streamSession) deliver(event fwtrading.Event) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case s.out <- event:
		if event.Kind != fwtrading.EventConnected {
			s.delivered = true
		}
		return nil
	}
}

// healthy reports whether a non-EventConnected event was delivered or strictly
// more than maxDelay has passed since the initial EventConnected delivery.
func (s *streamSession) healthy() bool {
	return s.delivered || time.Since(s.connectedAt) > s.maxDelay
}
