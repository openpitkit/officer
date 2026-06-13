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
	"time"
)

// FetchFunc fetches the current quotes for subs from a REST source. It returns
// one QuoteUpdate per instrument that has a quote (a missing instrument is
// simply omitted). A fetch error is reported by the poller and the tick is
// skipped; the last quote per instrument is retained.
type FetchFunc func(ctx context.Context, subs []Subscription) ([]QuoteUpdate, error)

// Poller drives a REST-only source on a fixed interval, coalescing each tick's
// fetched quotes and emitting only the ones that changed since the last tick.
// It is the shared base for adapters that have no streaming endpoint
// (later work builds on it); no real REST source ships yet.
//
// The poller keeps the last emitted quote per instrument so an unchanged value
// is not re-emitted. A non-nil errFn observes fetch errors (for logging) without
// stopping the loop. now defaults to time.Now and tick to a real ticker; both
// are injectable for deterministic tests.
type Poller struct {
	fetch    FetchFunc
	interval time.Duration
	errFn    func(error)
	now      func() time.Time

	last map[instrumentKey]QuoteUpdate
}

// instrumentKey identifies one instrument by its (base, quote) pair for the
// last-quote cache.
type instrumentKey struct {
	base  string
	quote string
}

// NewPoller builds a poller over fetch at interval. errFn, when non-nil, is
// called with each fetch error. interval must be positive; a non-positive value
// is treated as a single immediate fetch with no further ticks.
func NewPoller(fetch FetchFunc, interval time.Duration, errFn func(error)) *Poller {
	return &Poller{
		fetch:    fetch,
		interval: interval,
		errFn:    errFn,
		now:      time.Now,
		last:     make(map[instrumentKey]QuoteUpdate),
	}
}

// Run polls for subs until ctx is cancelled, sending changed quotes to out. It
// does one fetch immediately, then one per interval tick. out is owned by the
// caller and is not closed here. Run returns when ctx is done.
func (p *Poller) Run(ctx context.Context, subs []Subscription, out chan<- QuoteUpdate) {
	p.pollOnce(ctx, subs, out)
	if p.interval <= 0 {
		<-ctx.Done()
		return
	}

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollOnce(ctx, subs, out)
		}
	}
}

// pollOnce fetches one batch and emits the quotes that changed since the last
// tick. A fetch error is reported and the tick skipped. A send races ctx so a
// cancelled poller never blocks on a full channel.
func (p *Poller) pollOnce(ctx context.Context, subs []Subscription, out chan<- QuoteUpdate) {
	updates, err := p.fetch(ctx, subs)
	if err != nil {
		if p.errFn != nil {
			p.errFn(err)
		}
		return
	}
	for _, u := range updates {
		key := instrumentKey{base: u.Base, quote: u.Quote}
		if prev, ok := p.last[key]; ok && quoteUnchanged(prev, u) {
			continue
		}
		p.last[key] = u
		select {
		case <-ctx.Done():
			return
		case out <- u:
		}
	}
}

// quoteUnchanged reports whether b carries the same prices as a (timestamp
// aside): an identical mark/bid/ask is coalesced away to avoid re-emitting a
// quote that did not move.
func quoteUnchanged(a, b QuoteUpdate) bool {
	return a.Mark == b.Mark && a.Bid == b.Bid && a.Ask == b.Ask
}
