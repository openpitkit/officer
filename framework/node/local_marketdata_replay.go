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

package node

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
)

type marketDataReplayInstrumentKey struct {
	instance string
	external string
}

type marketDataReplayPairKey struct {
	base  string
	quote string
}

type marketDataReplayInstrument struct {
	instrument domain.MarketDataInstrument
	provider   string
}

type marketDataReplayCandidate struct {
	update     marketdata.QuoteUpdate
	receivedAt time.Time
	instance   string
	external   string
}

type marketDataTransitionSink struct {
	mu      sync.Mutex
	route   marketDataTransitionRoute
	current marketdata.Sink
	next    marketdata.Sink
	pending []marketDataTransitionOperation
}

type marketDataTransitionOperation struct {
	kind   marketDataTransitionOperationKind
	update marketdata.QuoteUpdate
	base   string
	quote  string
}

type marketDataTransitionOperationKind uint8

const (
	marketDataTransitionPush marketDataTransitionOperationKind = iota
	marketDataTransitionClear
)

type marketDataTransitionRoute uint8

const (
	marketDataTransitionBuffering marketDataTransitionRoute = iota
	marketDataTransitionCurrent
	marketDataTransitionNext
)

// Push keeps the live handle current while buffering the same provider order
// for the fresh handle. Replaying the buffer only at commit ensures an older
// persisted snapshot cannot overwrite a newer tick observed during replay.
func (s *marketDataTransitionSink) Push(update marketdata.QuoteUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.route {
	case marketDataTransitionCurrent:
		return s.current.Push(update)
	case marketDataTransitionNext:
		return s.next.Push(update)
	default:
		err := s.current.Push(update)
		s.pending = append(s.pending, marketDataTransitionOperation{
			kind: marketDataTransitionPush, update: update,
		})
		return err
	}
}

// Clear keeps a live manual clear ordered with provider pushes during an engine
// transition. While buffering it clears the current service immediately and
// records the same operation after persisted replay on the fresh service.
func (s *marketDataTransitionSink) Clear(base, quote string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.route {
	case marketDataTransitionCurrent:
		return clearMarketDataQuote(s.current, base, quote)
	case marketDataTransitionNext:
		return clearMarketDataQuote(s.next, base, quote)
	default:
		err := clearMarketDataQuote(s.current, base, quote)
		s.pending = append(s.pending, marketDataTransitionOperation{
			kind: marketDataTransitionClear, base: base, quote: quote,
		})
		return err
	}
}

func clearMarketDataQuote(sink marketdata.Sink, base, quote string) error {
	clearer, ok := sink.(marketdata.QuoteClearer)
	if !ok {
		return fmt.Errorf("market-data sink does not support quote clear")
	}
	return clearer.Clear(base, quote)
}

func (s *marketDataTransitionSink) useCurrent() {
	s.mu.Lock()
	s.route = marketDataTransitionCurrent
	s.pending = nil
	s.mu.Unlock()
}

func (n *localNode) beginMarketDataTransition(
	next engine.Engine,
) (*marketDataTransitionSink, error) {
	nextSink := next.MarketDataSink()
	if nextSink == nil {
		return nil, fmt.Errorf("rebuilt engine returned nil market-data sink")
	}
	n.engineMu.Lock()
	defer n.engineMu.Unlock()
	if n.engine == nil || n.engine.MarketDataSink() == nil {
		return nil, fmt.Errorf("current engine returned nil market-data sink")
	}
	transition := &marketDataTransitionSink{
		current: n.engine.MarketDataSink(),
		next:    nextSink,
	}
	n.marketDataTransition = transition
	return transition, nil
}

// commitMarketDataTransition serializes the buffered provider tail after the
// persisted replay, then changes both the captured transition route and the
// node's current engine while holding both locks. A provider push can therefore
// land either in the buffer/current engine or in the new engine, never in the
// replay-to-swap gap.
func (n *localNode) commitMarketDataTransition(
	transition *marketDataTransitionSink,
	next engine.Engine,
) (engine.Engine, error) {
	return n.commitMarketDataTransitionWithHook(transition, next, nil)
}

// commitMarketDataTransitionWithHook flushes the provider tail, then runs hook
// immediately before the infallible engine/sink pointer swap. The hook runs
// while provider pushes are excluded by engineMu and transition.mu. Callers can
// therefore make one durable store mutation the commit point for a prepared
// engine: hook failure keeps the old route current, while hook success is
// followed only by in-memory assignments that cannot fail.
func (n *localNode) commitMarketDataTransitionWithHook(
	transition *marketDataTransitionSink,
	next engine.Engine,
	hook func() error,
) (engine.Engine, error) {
	n.engineMu.Lock()
	defer n.engineMu.Unlock()
	if n.marketDataTransition != transition {
		return nil, fmt.Errorf("market-data transition is no longer current")
	}

	transition.mu.Lock()
	defer transition.mu.Unlock()
	for _, operation := range transition.pending {
		if err := applyMarketDataTransitionOperation(
			transition.next, operation,
		); err != nil {
			transition.route = marketDataTransitionCurrent
			transition.pending = nil
			n.marketDataTransition = nil
			return nil, err
		}
	}
	transition.pending = nil
	if hook != nil {
		if err := hook(); err != nil {
			transition.route = marketDataTransitionCurrent
			n.marketDataTransition = nil
			return nil, fmt.Errorf("commit prepared engine store mutation: %w", err)
		}
	}
	transition.route = marketDataTransitionNext
	prev := n.engine
	n.engine = next
	n.marketDataTransition = nil
	return prev, nil
}

func applyMarketDataTransitionOperation(
	sink marketdata.Sink, operation marketDataTransitionOperation,
) error {
	switch operation.kind {
	case marketDataTransitionClear:
		if err := clearMarketDataQuote(sink, operation.base, operation.quote); err != nil {
			return fmt.Errorf(
				"flush buffered market-data clear %s/%s: %w",
				operation.base, operation.quote, err,
			)
		}
		return nil
	default:
		if err := sink.Push(operation.update); err != nil {
			return fmt.Errorf(
				"flush buffered market-data quote %s/%s: %w",
				operation.update.Base, operation.update.Quote, err,
			)
		}
		return nil
	}
}

func (n *localNode) cancelMarketDataTransition(transition *marketDataTransitionSink) {
	n.engineMu.Lock()
	defer n.engineMu.Unlock()
	if n.marketDataTransition == transition {
		transition.useCurrent()
		n.marketDataTransition = nil
	}
}

// replayMarketDataInto seeds a freshly built engine before it can replace the
// live handle. Only enabled instances and instruments are replayed. Synthetic
// inverses follow the manager's configured-pair plan: any configured reverse
// direction, even one currently disabled, owns that direction and suppresses
// an inferred inverse.
func (n *localNode) replayMarketDataInto(
	ctx context.Context, next engine.Engine,
) error {
	instances, err := n.realm.ListMarketDataInstances(ctx)
	if err != nil {
		return fmt.Errorf("list market-data instances: %w", err)
	}

	configuredPairs := make(map[marketDataReplayPairKey]struct{})
	active := make(map[marketDataReplayInstrumentKey]marketDataReplayInstrument)
	for _, instance := range instances {
		instruments, err := n.realm.ListMarketDataInstruments(ctx, instance.ExternalID)
		if err != nil {
			return fmt.Errorf(
				"list market-data instruments for %s: %w",
				instance.ExternalID, err,
			)
		}
		for _, instrument := range instruments {
			configuredPairs[marketDataReplayPairKey{
				base: instrument.BaseAsset, quote: instrument.QuoteAsset,
			}] = struct{}{}
			if !instance.Enabled || !instrument.Enabled {
				continue
			}
			active[marketDataReplayInstrumentKey{
				instance: instance.ExternalID.String(),
				external: instrument.ExternalSymbol,
			}] = marketDataReplayInstrument{
				instrument: instrument,
				provider:   instance.Provider,
			}
		}
	}

	quotes, err := n.realm.ListMarketDataQuotes(ctx, domain.ExternalID(""))
	if err != nil {
		return fmt.Errorf("list market-data quotes: %w", err)
	}
	latest := make(map[marketDataReplayInstrumentKey]domain.MarketDataQuote, len(quotes))
	for _, quote := range quotes {
		latest[marketDataReplayInstrumentKey{
			instance: quote.Instance.String(), external: quote.ExternalSymbol,
		}] = quote
	}

	candidates := make([]marketDataReplayCandidate, 0, len(active))
	now := time.Now()
	for key, applied := range active {
		quote, ok := latest[key]
		instrument := applied.instrument
		// BYO is static configuration, not a historical feed. An empty configured
		// mark explicitly clears it, so a persisted snapshot must never resurrect
		// the prior value. A non-empty mark is authoritative even when persistence
		// has not caught up with the latest live edit.
		if applied.provider == domain.MarketDataProviderBYO {
			if instrument.ManualPrice == "" {
				continue
			}
			candidates = append(candidates, marketDataReplayCandidate{
				update: marketdata.QuoteUpdate{
					Base: instrument.BaseAsset, Quote: instrument.QuoteAsset,
					Mark: instrument.ManualPrice,
				},
				instance: key.instance,
				external: key.external,
			})
			continue
		}
		// The native SDK starts its own freshness window at Push and currently
		// ignores QuoteUpdate.AsOf. Never turn an already stale persisted streaming
		// snapshot into a fresh engine quote during a rebuild.
		if !ok || now.Sub(quote.AsOf) > marketdata.FreshnessTTL {
			continue
		}
		candidates = append(candidates, marketDataReplayCandidate{
			update: marketdata.QuoteUpdate{
				AsOf:  quote.AsOf,
				Base:  quote.BaseAsset,
				Quote: quote.QuoteAsset,
				Mark:  quote.Mark,
				Bid:   quote.Bid,
				Ask:   quote.Ask,
			},
			receivedAt: quote.ReceivedAt,
			instance:   key.instance,
			external:   key.external,
		})
	}

	// Restore the cross-provider last-write-wins shape as closely as persisted
	// state permits. Manual fallbacks have no receipt timestamp and go first;
	// stable identity fields make ties deterministic.
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if !left.receivedAt.Equal(right.receivedAt) {
			return left.receivedAt.Before(right.receivedAt)
		}
		if left.instance != right.instance {
			return left.instance < right.instance
		}
		return left.external < right.external
	})

	sink := next.MarketDataSink()
	if sink == nil {
		return fmt.Errorf("rebuilt engine returned nil market-data sink")
	}
	for _, candidate := range candidates {
		if err := sink.Push(candidate.update); err != nil {
			return fmt.Errorf(
				"push market-data quote %s/%s from %s/%s: %w",
				candidate.update.Base, candidate.update.Quote,
				candidate.instance, candidate.external, err,
			)
		}
		pair := marketDataReplayPairKey{
			base: candidate.update.Base, quote: candidate.update.Quote,
		}
		if _, reverseConfigured := configuredPairs[marketDataReplayPairKey{
			base: pair.quote, quote: pair.base,
		}]; reverseConfigured {
			continue
		}
		if inverted, ok := marketdata.InvertQuote(candidate.update); ok {
			if err := sink.Push(inverted); err != nil {
				return fmt.Errorf(
					"push synthetic market-data quote %s/%s from %s/%s: %w",
					inverted.Base, inverted.Quote,
					candidate.instance, candidate.external, err,
				)
			}
		}
	}
	return nil
}
