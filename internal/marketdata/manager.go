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
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.openpit.dev/officer/internal/domain"
)

// Store is the read seam the manager needs: the enabled instances and, per
// instance, its enabled instruments. It is the subset of the officer Store the
// runtime consumes; the concrete sqlite store satisfies it.
type Store interface {
	// ListEnabledMarketDataInstances returns every enabled market-data instance.
	ListEnabledMarketDataInstances(ctx context.Context) ([]domain.MarketDataInstance, error)
	// ListEnabledMarketDataInstruments returns the enabled instruments of the
	// identified instance.
	ListEnabledMarketDataInstruments(
		ctx context.Context, instanceID string,
	) ([]domain.MarketDataInstrument, error)
	// UpsertMarketDataQuote records the latest quote snapshot for panel and
	// dashboard observability.
	UpsertMarketDataQuote(ctx context.Context, quote domain.MarketDataQuote) error
}

// Manager owns the runtime lifecycle of every enabled market-data connector. It
// reads the enabled config from the store, builds one connector per instance by
// type, subscribes it to that instance's enabled instruments, and drains each
// connector's channel into the sink. Symbol mapping (instrument rows ->
// Subscriptions) lives here; connectors tag emitted quotes with base/quote.
//
// Graceful degradation is the contract: nothing enabled is a clean no-op, and a
// single bad instance (unknown type, subscribe error) is logged and skipped
// without blocking the others or crashing the process.
type Manager struct {
	store  Store
	sink   Sink
	logger *slog.Logger

	mu         sync.Mutex
	started    bool
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	connectors []Connector
}

// NewManager builds a manager over the store, sink, and logger. logger may be
// nil, in which case a discarding default is used.
func NewManager(store Store, sink Sink, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &Manager{store: store, sink: sink, logger: logger}
}

// Start reads the enabled instances and brings up one connector per instance.
// It is safe to call once; a second Start while running is a no-op. An instance
// whose type is unknown, whose instruments cannot be read, or whose Subscribe
// fails is logged and skipped - it never fails Start or stops the other
// instances. With nothing enabled Start is a clean no-op.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return nil
	}

	instances, err := m.store.ListEnabledMarketDataInstances(ctx)
	if err != nil {
		return fmt.Errorf("marketdata: list enabled instances: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.started = true

	for _, instance := range instances {
		m.startInstanceLocked(runCtx, instance)
	}
	return nil
}

// startInstanceLocked brings up one instance: read its enabled instruments,
// build the connector, subscribe, and drain into the sink. Any failure for this
// instance is logged and the instance skipped. Callers must hold mu.
func (m *Manager) startInstanceLocked(ctx context.Context, instance domain.MarketDataInstance) {
	instruments, err := m.store.ListEnabledMarketDataInstruments(ctx, instance.ID)
	if err != nil {
		m.logger.Error("marketdata: read instruments, skipping instance",
			"instance", instance.ID, "err", err)
		return
	}
	if len(instruments) == 0 {
		m.logger.Info("marketdata: instance has no enabled instruments, skipping",
			"instance", instance.ID, "type", instance.Type)
		return
	}

	connector, err := newConnector(instance)
	if err != nil {
		m.logger.Warn("marketdata: unsupported instance, skipping",
			"instance", instance.ID, "type", instance.Type, "err", err)
		return
	}

	subs := subscriptionsFor(instruments)
	symbols := externalSymbolsFor(subs)
	ch, err := connector.Subscribe(ctx, subs)
	if err != nil {
		m.logger.Error("marketdata: subscribe failed, skipping instance",
			"instance", instance.ID, "type", instance.Type, "err", err)
		connector.Close()
		return
	}

	m.connectors = append(m.connectors, connector)
	m.wg.Add(1)
	go m.drain(instance.ID, symbols, ch)
}

// drain forwards every quote from ch into the sink, logging push errors without
// stopping (one bad quote must not tear down a feed). It ends when ch closes.
func (m *Manager) drain(
	instanceID string, symbols map[quoteInstrumentKey]string, ch <-chan QuoteUpdate,
) {
	defer m.wg.Done()
	for update := range ch {
		if err := m.store.UpsertMarketDataQuote(
			context.Background(), quoteSnapshot(instanceID, symbols, update),
		); err != nil {
			m.logger.Error("marketdata: persist quote failed",
				"instance", instanceID, "base", update.Base, "quote", update.Quote, "err", err)
		}
		if err := m.sink.Push(update); err != nil {
			m.logger.Error("marketdata: push quote failed",
				"instance", instanceID, "base", update.Base, "quote", update.Quote, "err", err)
		}
	}
}

func quoteSnapshot(
	instanceID string, symbols map[quoteInstrumentKey]string, update QuoteUpdate,
) domain.MarketDataQuote {
	receivedAt := time.Now().UTC()
	asOf := update.AsOf
	if asOf.IsZero() {
		asOf = receivedAt
	}
	return domain.MarketDataQuote{
		AsOf:           asOf.UTC(),
		ReceivedAt:     receivedAt,
		InstanceID:     instanceID,
		ExternalSymbol: symbols[quoteInstrumentKey{base: update.Base, quote: update.Quote}],
		BaseAsset:      update.Base,
		QuoteAsset:     update.Quote,
		Mark:           update.Mark,
		Bid:            update.Bid,
		Ask:            update.Ask,
	}
}

// Stop cancels the run context, closes every connector so its channel ends, and
// waits for the drain goroutines to finish. It is idempotent: a second Stop (or
// a Stop before Start) is a no-op.
func (m *Manager) Stop() {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return
	}
	m.started = false
	cancel := m.cancel
	connectors := m.connectors
	m.connectors = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	for _, connector := range connectors {
		connector.Close()
	}
	m.wg.Wait()
}

// subscriptionsFor maps enabled instrument rows to subscriptions: external
// symbol plus the instrument (base, quote) it resolves to.
func subscriptionsFor(instruments []domain.MarketDataInstrument) []Subscription {
	subs := make([]Subscription, 0, len(instruments))
	for _, instrument := range instruments {
		subs = append(subs, Subscription{
			External: instrument.ExternalSymbol,
			Base:     instrument.BaseAsset,
			Quote:    instrument.QuoteAsset,
		})
	}
	return subs
}

type quoteInstrumentKey struct {
	base  string
	quote string
}

func externalSymbolsFor(subs []Subscription) map[quoteInstrumentKey]string {
	symbols := make(map[quoteInstrumentKey]string, len(subs))
	for _, sub := range subs {
		symbols[quoteInstrumentKey{base: sub.Base, quote: sub.Quote}] = sub.External
	}
	return symbols
}

// newConnector constructs the connector for one instance by its type. The two
// known types are constructed directly; there is no generic provider registry
// (the external-author contract is deferred). An unknown type returns
// an error so the manager skips the instance.
func newConnector(instance domain.MarketDataInstance) (Connector, error) {
	switch instance.Type {
	case domain.MarketDataProviderBYO:
		return NewBYOConnector(0), nil
	case domain.MarketDataProviderBinance:
		return NewBinanceConnector(), nil
	case domain.MarketDataProviderMock:
		return NewMockConnector(0), nil
	default:
		return nil, fmt.Errorf("unknown market-data provider type %q", instance.Type)
	}
}

// discard is an io.Writer that drops everything, backing the manager's default
// no-op logger when the caller supplies none.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
