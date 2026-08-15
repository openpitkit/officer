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

package native

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	bindmd "go.openpit.dev/openpit/marketdata"
	"go.openpit.dev/openpit/param"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
)

// instrumentKey identifies one instrument by its (base, quote) pair, the cache
// key for the first-sight register.
type instrumentKey struct {
	base  domain.EngineAssetID
	quote domain.EngineAssetID
}

// marketDataSink adapts the binding's market-data service to the connector
// framework's Sink. It registers each instrument on first sight, caches the
// returned id, and pushes quotes through the service. The first-sight
// register-then-cache must be atomic across draining goroutines, so it is
// guarded by mu.
type marketDataSink struct {
	service *bindmd.Service
	res     idResolver
	now     func() time.Time

	mu  sync.Mutex
	ids map[instrumentKey]bindmd.InstrumentID
	// publishMu serializes Officer writers across the TTL change and the quote
	// publish that must land under it.
	publishMu sync.Mutex
	// lifetimes holds the quote lifetime each instrument currently runs under, so
	// a publish knows whether the incoming one narrows or widens it. Read and
	// written only under publishMu; a missing entry means the service-wide
	// default the instrument was registered with.
	lifetimes map[instrumentKey]time.Duration
}

// newMarketDataSink wraps service into a Sink with an empty id cache.
func newMarketDataSink(service *bindmd.Service, res idResolver) *marketDataSink {
	return &marketDataSink{
		service:   service,
		res:       res,
		now:       time.Now,
		ids:       make(map[instrumentKey]bindmd.InstrumentID),
		lifetimes: make(map[instrumentKey]time.Duration),
	}
}

// Push registers the instrument on first sight and publishes the quote,
// replacing the stored snapshot. Only the present price fields are set
// (.WithMark/.WithBid/.WithAsk for non-empty decimal strings). Binding errors
// are wrapped as officer errors. The SDK market-data service is FullSync for a
// non-NoSync engine, so connector goroutines may push off the account lanes.
func (s *marketDataSink) Push(update marketdata.QuoteUpdate) error {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	instrument, err := instrumentFrom(update.Base, update.Quote, s.res)
	if err != nil {
		return err
	}

	id, err := s.resolveID(update.Base, update.Quote, instrument)
	if err != nil {
		return err
	}

	quote, err := quoteFrom(update)
	if err != nil {
		return err
	}
	now := s.now()
	if update.AsOf.After(now) {
		slog.Warn(
			"clamp future source quote timestamp",
			"base", update.Base,
			"quote", update.Quote,
			"as_of", update.AsOf,
			"now", now,
		)
	}
	ttl, sourced := quoteSourceTTL(update.AsOf, now)
	// A cleared override falls back to the service-wide default, which this
	// package builds from the same freshness constant.
	lifetime := MarketDataFreshnessTTL
	if sourced {
		lifetime = ttl
	}
	setLifetime := func() error {
		if !sourced {
			if err := s.service.ClearInstrumentTTL(id); err != nil {
				return fmt.Errorf(
					"engine: restore quote ttl %d/%d: %w", update.Base, update.Quote, err,
				)
			}
			return nil
		}
		if err := s.service.SetInstrumentTTL(id, bindmd.WithinTTL(ttl)); err != nil {
			return fmt.Errorf(
				"engine: set source quote ttl %d/%d: %w", update.Base, update.Quote, err,
			)
		}
		return nil
	}
	push := func() error {
		if err := s.service.Push(id, quote); err != nil {
			return fmt.Errorf(
				"engine: push quote %d/%d: %w", update.Base, update.Quote, err,
			)
		}
		return nil
	}

	// The SDK exposes the quote and its lifetime as separate calls, so one of
	// them is always visible before the other and neither order is safe alone:
	// TTL first lends the incoming lifetime to the outgoing quote, quote first
	// lends the outgoing lifetime to the incoming quote. Narrowing first and
	// widening last runs every window under min(outgoing, incoming), which can
	// only expire a quote early, never serve an aged one as fresh - and that is
	// also what a failed call leaves behind. The quote itself is never cleared:
	// SpotFunds may still use an expired snapshot as an FX fallback.
	key := instrumentKey{base: update.Base, quote: update.Quote}
	if lifetime <= s.appliedLifetime(key) {
		if err := setLifetime(); err != nil {
			return err
		}
		s.lifetimes[key] = lifetime
		return push()
	}
	if err := push(); err != nil {
		return err
	}
	if err := setLifetime(); err != nil {
		return err
	}
	s.lifetimes[key] = lifetime
	return nil
}

// appliedLifetime returns the quote lifetime the instrument currently runs
// under. An instrument this sink has not retuned yet runs under the
// service-wide default, which is the full freshness window. Callers hold
// publishMu.
func (s *marketDataSink) appliedLifetime(key instrumentKey) time.Duration {
	if applied, ok := s.lifetimes[key]; ok {
		return applied
	}
	return MarketDataFreshnessTTL
}

// quoteSourceTTL maps the connector's source timestamp onto the remaining SDK
// lifetime. The SDK retains an expired quote in ErrQuoteExpired, which lets
// SpotFunds accounting use the last-known FX while market-order pricing treats
// the same quote as unavailable. A zero timestamp keeps the service default.
func quoteSourceTTL(asOf, now time.Time) (time.Duration, bool) {
	if asOf.IsZero() {
		return 0, false
	}
	if asOf.After(now) {
		return MarketDataFreshnessTTL, true
	}
	age := now.Sub(asOf)
	if age >= MarketDataFreshnessTTL {
		return 0, true
	}
	return MarketDataFreshnessTTL - age, true
}

// Clear removes the live quote for one instrument without unregistering its
// stable service id. An instrument this sink has never observed is a no-op.
func (s *marketDataSink) Clear(base, quote domain.EngineAssetID) error {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	instrument, err := instrumentFrom(base, quote, s.res)
	if err != nil {
		return err
	}

	key := instrumentKey{base: base, quote: quote}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.ids[key]
	if !ok {
		id, ok = s.service.Resolve(instrument)
		if !ok {
			return nil
		}
		s.ids[key] = id
	}
	s.service.Clear(id)
	return nil
}

// resolveID returns the cached instrument id, registering it on first sight. A
// concurrent first-sight register that loses the race surfaces as
// ErrAlreadyRegistered, which is resolved to the existing id. The whole
// lookup-register-cache runs under mu so two goroutines never both register.
func (s *marketDataSink) resolveID(
	base, quote domain.EngineAssetID, instrument param.Instrument,
) (bindmd.InstrumentID, error) {
	key := instrumentKey{base: base, quote: quote}

	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.ids[key]; ok {
		return id, nil
	}

	id, err := s.service.Register(instrument)
	if errors.Is(err, bindmd.ErrAlreadyRegistered) {
		resolved, ok := s.service.Resolve(instrument)
		if !ok {
			return bindmd.InstrumentID{}, fmt.Errorf(
				"engine: instrument %d/%d already registered but unresolvable", base, quote)
		}
		id = resolved
	} else if err != nil {
		return bindmd.InstrumentID{}, fmt.Errorf(
			"engine: register instrument %d/%d: %w", base, quote, err)
	}
	s.ids[key] = id
	return id, nil
}

// instrumentFrom builds a binding instrument from stable asset identifiers.
func instrumentFrom(
	base, quote domain.EngineAssetID, res idResolver,
) (param.Instrument, error) {
	baseAsset, err := res.assetByID(base)
	if err != nil {
		return param.Instrument{}, fmt.Errorf("engine: base asset %d: %w", base, err)
	}
	quoteAsset, err := res.assetByID(quote)
	if err != nil {
		return param.Instrument{}, fmt.Errorf("engine: quote asset %d: %w", quote, err)
	}
	return param.NewInstrument(baseAsset, quoteAsset), nil
}

// quoteFrom builds a binding quote from an update, setting only the present
// price fields. An empty decimal string means the field is absent and is left
// unset.
func quoteFrom(update marketdata.QuoteUpdate) (bindmd.Quote, error) {
	quote := bindmd.NewQuote()
	if update.Mark != "" {
		mark, err := param.NewPriceFromString(update.Mark)
		if err != nil {
			return bindmd.Quote{}, fmt.Errorf("engine: mark price %q: %w", update.Mark, err)
		}
		quote = quote.WithMark(mark)
	}
	if update.Bid != "" {
		bid, err := param.NewPriceFromString(update.Bid)
		if err != nil {
			return bindmd.Quote{}, fmt.Errorf("engine: bid price %q: %w", update.Bid, err)
		}
		quote = quote.WithBid(bid)
	}
	if update.Ask != "" {
		ask, err := param.NewPriceFromString(update.Ask)
		if err != nil {
			return bindmd.Quote{}, fmt.Errorf("engine: ask price %q: %w", update.Ask, err)
		}
		quote = quote.WithAsk(ask)
	}
	return quote, nil
}
