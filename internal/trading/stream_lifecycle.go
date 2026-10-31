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
