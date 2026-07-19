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
	"log/slog"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

func testExternalID(label string) domain.ExternalID {
	return domain.ExternalID(label)
}

type fakeStore struct {
	mu          sync.Mutex
	instances   []domain.MarketDataInstance
	instruments map[string][]domain.MarketDataInstrument
	quotes      []domain.MarketDataQuote
	instErr     error
}

func (s *fakeStore) ListEnabledMarketDataInstances(context.Context) ([]domain.MarketDataInstance, error) {
	return s.instances, nil
}

func (s *fakeStore) ListEnabledMarketDataInstruments(
	_ context.Context, instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	if s.instErr != nil {
		return nil, s.instErr
	}
	return s.instruments[instance.String()], nil
}

func (s *fakeStore) UpsertMarketDataQuote(
	_ context.Context, quote domain.MarketDataQuote,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotes = append(s.quotes, quote)
	return nil
}

func (s *fakeStore) quoteCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.quotes)
}

func (s *fakeStore) quotesSnapshot() []domain.MarketDataQuote {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.MarketDataQuote(nil), s.quotes...)
}

type blockingQuoteStore struct {
	instances   []domain.MarketDataInstance
	instruments map[string][]domain.MarketDataInstrument
	entered     chan struct{}
	release     chan struct{}
}

func (s *blockingQuoteStore) ListEnabledMarketDataInstances(
	context.Context,
) ([]domain.MarketDataInstance, error) {
	return s.instances, nil
}

func (s *blockingQuoteStore) ListEnabledMarketDataInstruments(
	_ context.Context, instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	return s.instruments[instance.String()], nil
}

func (s *blockingQuoteStore) UpsertMarketDataQuote(
	ctx context.Context, _ domain.MarketDataQuote,
) error {
	close(s.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
		return nil
	}
}

type fakeSink struct {
	mu      sync.Mutex
	pushed  []QuoteUpdate
	pushErr error
}

func (s *fakeSink) Push(update QuoteUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pushErr != nil {
		return s.pushErr
	}
	s.pushed = append(s.pushed, update)
	return nil
}

func (s *fakeSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pushed)
}

func mustNewManager(
	t *testing.T,
	registry *Registry,
	store Store,
	sink Sink,
	logger *slog.Logger,
) *Manager {
	t.Helper()
	manager, err := NewManager(registry, store, sink, logger)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return manager
}

type fakeConnector struct {
	ch     chan QuoteUpdate
	closed bool
}

func newFakeConnector() *fakeConnector {
	return &fakeConnector{ch: make(chan QuoteUpdate, 4)}
}

func (c *fakeConnector) Subscribe(
	context.Context, []Subscription,
) (<-chan QuoteUpdate, error) {
	return c.ch, nil
}

func (c *fakeConnector) Close() {
	if c.closed {
		return
	}
	c.closed = true
	close(c.ch)
}

func (c *fakeConnector) Push(update QuoteUpdate) {
	c.ch <- update
}

func (c *fakeConnector) VerifySymbol(
	context.Context, string,
) (SymbolVerification, error) {
	return SymbolVerification{Exists: true}, nil
}

func (c *fakeConnector) SearchSymbols(
	context.Context, SymbolSearchQuery,
) ([]SymbolMatch, error) {
	return []SymbolMatch{{Symbol: "AAPL"}}, nil
}

type plainConnector struct {
	ch     chan QuoteUpdate
	closed bool
}

func newPlainConnector() *plainConnector {
	return &plainConnector{ch: make(chan QuoteUpdate, 4)}
}

func (c *plainConnector) Subscribe(
	context.Context, []Subscription,
) (<-chan QuoteUpdate, error) {
	return c.ch, nil
}

func (c *plainConnector) Close() {
	if c.closed {
		return
	}
	c.closed = true
	close(c.ch)
}

type stuckCloseConnector struct {
	closeEntered chan struct{}
	releaseClose chan struct{}
	ch           chan QuoteUpdate
	closeOnce    sync.Once
}

func newStuckCloseConnector() *stuckCloseConnector {
	return &stuckCloseConnector{
		closeEntered: make(chan struct{}),
		releaseClose: make(chan struct{}),
		ch:           make(chan QuoteUpdate),
	}
}

func (c *stuckCloseConnector) Subscribe(
	context.Context, []Subscription,
) (<-chan QuoteUpdate, error) {
	return c.ch, nil
}

func (c *stuckCloseConnector) Close() {
	c.closeOnce.Do(func() {
		close(c.closeEntered)
		<-c.releaseClose
		close(c.ch)
	})
}

type blockingPushConnector struct {
	ch chan QuoteUpdate
}

func newBlockingPushConnector() *blockingPushConnector {
	return &blockingPushConnector{ch: make(chan QuoteUpdate)}
}

func (c *blockingPushConnector) Subscribe(
	context.Context, []Subscription,
) (<-chan QuoteUpdate, error) {
	return c.ch, nil
}

func (c *blockingPushConnector) Close() {
	close(c.ch)
}

func (c *blockingPushConnector) Push(update QuoteUpdate) {
	c.ch <- update
}

func TestManagerNoEnabledIsCleanNoop(t *testing.T) {
	t.Parallel()

	manager := mustNewManager(t, NewRegistry(), &fakeStore{}, &fakeSink{}, nil)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	manager.Stop()
	if got := manager.InstanceStatuses(); len(got) != 0 {
		t.Fatalf("statuses = %+v, want empty", got)
	}
}

func TestNewManagerRejectsNilSink(t *testing.T) {
	t.Parallel()

	_, err := NewManager(NewRegistry(), &fakeStore{}, nil, nil)
	if !errors.Is(err, ErrNilSink) {
		t.Fatalf("NewManager nil sink = %v, want ErrNilSink", err)
	}
}

func TestManagerStopTimesOutStuckConnectorClose(t *testing.T) {
	instanceID := testExternalID("mock-1")
	connector := newStuckCloseConnector()
	t.Cleanup(func() {
		select {
		case <-connector.releaseClose:
		default:
			close(connector.releaseClose)
		}
	})
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "mock",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "mock", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
			}},
		},
	}
	manager := mustNewManager(t, registry, store, &fakeSink{}, nil)
	manager.stopGrace = 10 * time.Millisecond
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan struct{})
	go func() {
		manager.Stop()
		close(done)
	}()

	select {
	case <-connector.closeEntered:
	case <-time.After(time.Second):
		t.Fatal("connector Close was not called")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop waited indefinitely for connector Close")
	}
}

func TestManagerStartReplaysManualQuotesOutsideLock(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-1")
	connector := newBlockingPushConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "mock",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "mock", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
				ManualPrice: "185",
			}},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()

	waitFor(t, time.Second, func() bool { return sink.count() == 1 })
}

func TestManagerUseSinkRejectsNil(t *testing.T) {
	t.Parallel()

	manager := mustNewManager(t, NewRegistry(), &fakeStore{}, &fakeSink{}, nil)
	if err := manager.UseSink(nil); !errors.Is(err, ErrNilSink) {
		t.Fatalf("UseSink nil = %v, want ErrNilSink", err)
	}
}

func TestManagerFansMockQuotesToSink(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-1")
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "mock",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "mock", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
			}},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	connector.Push(QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "185"})
	waitFor(t, time.Second, func() bool { return sink.count() == 1 })
	manager.Stop()
	quotes := store.quotesSnapshot()
	if len(quotes) != 1 || quotes[0].ExternalSymbol != "AAPL" {
		t.Fatalf("stored quotes = %+v", quotes)
	}
}

func TestManager_PushErrorDoesNotStopFeed(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-1")
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "mock",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "mock", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
			}},
		},
	}
	handler := &capturingHandler{}
	sink := &fakeSink{pushErr: errors.New("push boom")}
	manager := mustNewManager(t, registry, store, sink, slog.New(handler))

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()
	connector.Push(QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "185"})
	connector.Push(QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "186"})
	waitFor(t, time.Second, func() bool { return store.quoteCount() == 2 })

	status := manager.InstanceStatuses()[instanceID.String()]
	var found bool
	for _, diag := range status.Diagnostics {
		if diag.Title == "Failed to push quote to engine" &&
			diag.Level == DiagWarn &&
			diag.Detail == "push boom" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %+v, want push warning", status.Diagnostics)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.records) != 1 ||
		handler.records[0].Level != slog.LevelWarn ||
		handler.records[0].Message != "Failed to push quote to engine" {
		t.Fatalf("records = %+v, want one push warning", handler.records)
	}
}

func TestManagerSkipsInstanceWithNoInstruments(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("empty-1")
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "fake",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			t.Fatal("provider must not build without instruments")
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "fake", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{instanceID.String(): nil},
	}
	manager := mustNewManager(t, registry, store, &fakeSink{}, nil)

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()
	status := manager.InstanceStatuses()[instanceID.String()]
	if status.State != StateError || len(status.Diagnostics) != 1 ||
		status.Diagnostics[0].Code != CodeNoEnabledInstruments {
		t.Fatalf("status = %+v", status)
	}
}

func TestManagerStopCancelsQuoteWriteContext(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-1")
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "mock",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &blockingQuoteStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "mock", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
			}},
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(store.release)
	manager := mustNewManager(t, registry, store, &fakeSink{}, nil)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()
	connector.Push(QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "185"})
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("quote write did not start")
	}

	stopped := make(chan struct{})
	go func() {
		manager.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel blocked quote write")
	}
}

func TestManagerStopClearsRuntimeSnapshots(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("empty-1")
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "fake", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{instanceID.String(): nil},
	}
	manager := mustNewManager(t, NewRegistry(), store, &fakeSink{}, nil)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(manager.InstanceStatuses()) == 0 || len(manager.AppliedConfig()) == 0 {
		t.Fatalf("runtime snapshots were not populated before Stop")
	}

	manager.Stop()

	if got := manager.InstanceStatuses(); len(got) != 0 {
		t.Fatalf("statuses after Stop = %+v, want empty", got)
	}
	if got := manager.AppliedConfig(); len(got) != 0 {
		t.Fatalf("applied config after Stop = %+v, want empty", got)
	}
}

func TestManagerStopPreservesRestartContext(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-restart")
	connectors := make([]*fakeConnector, 0, 2)
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "mock",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			connector := newFakeConnector()
			connectors = append(connectors, connector)
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "mock", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
			}},
		},
	}
	manager := mustNewManager(t, registry, store, &fakeSink{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(connectors) != 1 {
		t.Fatalf("connectors after Start = %d, want 1", len(connectors))
	}
	manager.Stop()

	sink := &fakeSink{}
	if err := manager.UseSink(sink); err != nil {
		t.Fatalf("UseSink: %v", err)
	}
	if err := manager.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if len(connectors) != 2 {
		t.Fatalf("connectors after Restart = %d, want 2", len(connectors))
	}
	if got := manager.AppliedConfig(); len(got) == 0 {
		t.Fatalf("applied config after Restart is empty")
	}
	connectors[1].Push(QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "185"})
	waitFor(t, time.Second, func() bool { return sink.count() == 1 })
	manager.Stop()
}

func TestManagerStartIdempotent(t *testing.T) {
	t.Parallel()

	manager := mustNewManager(t, NewRegistry(), &fakeStore{}, &fakeSink{}, nil)
	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	manager.Stop()
	manager.Stop()
}

func TestManagerPushesStoredManualPriceOnceAtStartup(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("byo-1")
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "byo",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "byo", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {
				{
					Instance: instanceID, ExternalSymbol: "USDT/USD",
					BaseAsset: "USDT", QuoteAsset: "USD",
					ManualPrice: "1", Enabled: true,
				},
				{
					Instance: instanceID, ExternalSymbol: "ETH/USD",
					BaseAsset: "ETH", QuoteAsset: "USD",
					Enabled: true,
				},
			},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, time.Second, func() bool { return sink.count() == 1 })
	manager.Stop()
	if got := store.quotesSnapshot()[0]; got.ExternalSymbol != "USDT/USD" ||
		got.Mark != "1" {
		t.Fatalf("manual quote = %+v", got)
	}
}

func TestManagerPushManualAfterStartupPushesOnce(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("byo-1")
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "byo",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "byo", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "USDT/USD",
				BaseAsset: "USDT", QuoteAsset: "USD", Enabled: true,
			}},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	manager.PushManual(instanceID.String(), domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "USDT/USD",
		BaseAsset: "USDT", QuoteAsset: "USD", ManualPrice: "1", Enabled: true,
	})
	waitFor(t, time.Second, func() bool { return sink.count() == 1 })
	manager.Stop()
	if got := store.quotesSnapshot()[0]; got.ExternalSymbol != "USDT/USD" ||
		got.Mark != "1" {
		t.Fatalf("manual quote = %+v", got)
	}
}

func TestManager_PushManualNonByoUntouched(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("plain-1")
	connector := newPlainConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "plain",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "plain", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
			}},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()

	manager.PushManual(instanceID.String(), domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "AAPL",
		BaseAsset: "AAPL", QuoteAsset: "USD", ManualPrice: "185", Enabled: true,
	})

	time.Sleep(20 * time.Millisecond)
	if sink.count() != 0 {
		t.Fatalf("sink count = %d, want no manual push", sink.count())
	}
	if got := store.quoteCount(); got != 0 {
		t.Fatalf("stored quotes = %d, want none", got)
	}
}

func TestManagerPushManualSkipsUnappliedInstrument(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("byo-1")
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "byo",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "byo", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "USD/Z",
				BaseAsset: "USD", QuoteAsset: "Z", Enabled: true,
			}},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()

	manager.PushManual(instanceID.String(), domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "Z/USD",
		BaseAsset: "Z", QuoteAsset: "USD", ManualPrice: "2", Enabled: true,
	})
	time.Sleep(20 * time.Millisecond)

	if sink.count() != 0 {
		t.Fatalf("sink count = %d, want no push for an unapplied instrument", sink.count())
	}
	if got := store.quoteCount(); got != 0 {
		t.Fatalf("stored quotes = %d, want no quote for an unapplied instrument", got)
	}
}

func TestManagerQuoteUpdateIntervalUnknownThenGap(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-1")
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "mock",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "mock", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
			}},
		},
	}
	manager := mustNewManager(t, registry, store, &fakeSink{}, nil)
	if _, ok := manager.QuoteUpdateInterval(instanceID.String(), "AAPL"); ok {
		t.Fatalf("interval known before start")
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	connector.Push(QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "100"})
	waitFor(t, time.Second, func() bool { return store.quoteCount() == 1 })
	if _, ok := manager.QuoteUpdateInterval(instanceID.String(), "AAPL"); ok {
		t.Fatalf("interval known after first quote")
	}
	time.Sleep(5 * time.Millisecond)
	connector.Push(QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "101"})
	waitFor(t, time.Second, func() bool {
		interval, ok := manager.QuoteUpdateInterval(instanceID.String(), "AAPL")
		return ok && interval > 0
	})
	manager.Stop()
}

func TestRegistryVerifyAndSearchSymbols(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type:            "catalogue",
		VerifiesSymbols: true,
		SearchesSymbols: true,
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return newFakeConnector(), nil
		},
	}); err != nil {
		t.Fatalf("register catalogue: %v", err)
	}
	if err := registry.Register(Provider{
		Type: "plain",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return newPlainConnector(), nil
		},
	}); err != nil {
		t.Fatalf("register plain: %v", err)
	}

	result, supported, err := registry.VerifySymbol(
		context.Background(),
		domain.MarketDataInstance{Provider: "catalogue"},
		"AAPL",
	)
	if err != nil || !supported || !result.Exists {
		t.Fatalf("VerifySymbol = %+v/%v/%v", result, supported, err)
	}
	matches, supported, err := registry.SearchSymbols(
		context.Background(),
		domain.MarketDataInstance{Provider: "catalogue"},
		SymbolSearchQuery{Query: "AAP"},
	)
	if err != nil || !supported || len(matches) != 1 || matches[0].Symbol != "AAPL" {
		t.Fatalf("SearchSymbols = %+v/%v/%v", matches, supported, err)
	}
	if _, supported, err := registry.VerifySymbol(
		context.Background(),
		domain.MarketDataInstance{Provider: "plain"},
		"AAPL",
	); err != nil || supported {
		t.Fatalf("plain VerifySymbol supported=%v err=%v", supported, err)
	}
	if _, _, err := registry.VerifySymbol(
		context.Background(),
		domain.MarketDataInstance{Provider: "missing"},
		"AAPL",
	); err == nil {
		t.Fatalf("VerifySymbol missing provider err = nil")
	}
	matches, supported, err = registry.SearchSymbols(
		context.Background(),
		domain.MarketDataInstance{Provider: "plain"},
		SymbolSearchQuery{Query: "AAP"},
	)
	if err != nil || supported || matches != nil {
		t.Fatalf("plain SearchSymbols = %+v/%v/%v", matches, supported, err)
	}
	if _, _, err := registry.SearchSymbols(
		context.Background(),
		domain.MarketDataInstance{Provider: "missing"},
		SymbolSearchQuery{Query: "AAP"},
	); err == nil {
		t.Fatalf("SearchSymbols missing provider err = nil")
	}
}

func TestRegistryRegisterReplaceRemove(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	if err := registry.Register(Provider{Type: "a", Title: "A"}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if err := registry.Register(Provider{Type: "b", Title: "B"}); err != nil {
		t.Fatalf("register b: %v", err)
	}
	if err := registry.Register(Provider{Type: "a", Title: "A2"}); err != nil {
		t.Fatalf("replace a: %v", err)
	}

	list := registry.List()
	if len(list) != 2 || list[0].Type != "a" || list[0].Title != "A2" ||
		list[1].Type != "b" {
		t.Fatalf("list after replace = %+v", list)
	}
	if !registry.Unregister("a") || registry.Known("a") {
		t.Fatalf("unregister did not remove provider")
	}
	if registry.Unregister("a") {
		t.Fatalf("second unregister reported present")
	}
	if err := registry.Register(Provider{}); err == nil {
		t.Fatalf("empty provider type accepted")
	}
}

func TestManagerBuildsThroughRegistry(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("inst-1")
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type:            "fake",
		Title:           "Fake",
		VerifiesSymbols: true,
		SearchesSymbols: true,
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "fake", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
			}},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()
	connector.ch <- QuoteUpdate{Base: "AAPL", Quote: "USD", Mark: "100"}
	waitFor(t, time.Second, func() bool { return sink.count() == 1 })

	status := manager.InstanceStatuses()[instanceID.String()]
	if status.State != StateOK || !status.VerifiesSymbols || !status.SearchesSymbols {
		t.Fatalf("status = %+v", status)
	}
}

func TestManagerInvalidProviderConfigIsNotUnsupported(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("bad credentials")
	instanceID := testExternalID("inst-1")
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type:  "fake",
		Title: "Fake",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return nil, wantErr
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "fake", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
			}},
		},
	}
	manager := mustNewManager(t, registry, store, &fakeSink{}, nil)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()

	status := manager.InstanceStatuses()[instanceID.String()]
	if status.State != StateError {
		t.Fatalf("state = %q, want error", status.State)
	}
	if len(status.Diagnostics) != 1 ||
		status.Diagnostics[0].Code != CodeInvalidProviderConfig {
		t.Fatalf("diagnostics = %+v", status.Diagnostics)
	}
}

func TestManagerUnknownProviderUsesUnsupportedDiagnostic(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("inst-1")
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ExternalID: instanceID, Provider: "missing", Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
			}},
		},
	}
	manager := mustNewManager(t, NewRegistry(), store, &fakeSink{}, nil)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()

	status := manager.InstanceStatuses()[instanceID.String()]
	if len(status.Diagnostics) != 1 ||
		status.Diagnostics[0].Code != CodeUnsupportedProvider {
		t.Fatalf("diagnostics = %+v", status.Diagnostics)
	}
}

type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *capturingHandler) WithGroup(string) slog.Handler { return h }

func TestRecordDiagLockedDedupedDiagNoLog(t *testing.T) {
	t.Parallel()
	h := &capturingHandler{}
	manager := &Manager{
		logger:   slog.New(h),
		statuses: map[string]InstanceRuntimeStatus{"inst-1": {}},
	}
	diag := Diagnostic{
		Level:  DiagWarn,
		Code:   CodeNoData,
		Kind:   DiagKindProvider,
		Title:  "No market data",
		Detail: "no quotes arriving",
	}

	manager.recordDiagLocked("inst-1", diag)
	manager.recordDiagLocked("inst-1", diag)

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.records) != 1 {
		t.Fatalf("records = %d, want one", len(h.records))
	}
}

func TestRecordDiagLocked_LevelMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		diag Diagnostic
		want slog.Level
	}{
		{
			name: "routine",
			diag: Diagnostic{
				Routine: true,
				Level:   DiagInfo,
				Code:    CodeNoData,
				Title:   "routine diagnostic",
			},
			want: slog.LevelDebug,
		},
		{
			name: "info",
			diag: Diagnostic{Level: DiagInfo, Code: CodeNoData, Title: "info diagnostic"},
			want: slog.LevelInfo,
		},
		{
			name: "warn",
			diag: Diagnostic{Level: DiagWarn, Code: CodeNoData, Title: "warn diagnostic"},
			want: slog.LevelWarn,
		},
		{
			name: "error",
			diag: Diagnostic{Level: DiagError, Code: CodeNoData, Title: "error diagnostic"},
			want: slog.LevelError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &capturingHandler{}
			manager := &Manager{
				logger:   slog.New(h),
				statuses: map[string]InstanceRuntimeStatus{"inst-1": {}},
			}

			manager.recordDiagLocked("inst-1", tc.diag)

			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.records) != 1 {
				t.Fatalf("records = %d, want one", len(h.records))
			}
			if h.records[0].Level != tc.want {
				t.Fatalf("level = %v, want %v", h.records[0].Level, tc.want)
			}
		})
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestManagerSinkProviderResolvesLiveSink(t *testing.T) {
	static := &fakeSink{}
	manager := mustNewManager(t, NewRegistry(), &fakeStore{}, static, nil)

	// Without a provider, currentSink is the static sink.
	if got := manager.currentSink(); got != Sink(static) {
		t.Fatalf("currentSink without provider = %v, want static", got)
	}

	// A provider takes precedence and is resolved live on every call: swapping
	// the sink it returns is reflected immediately with no restart, mirroring an
	// engine rebuild that replaces the sink.
	live := Sink(static)
	manager.UseSinkProvider(func() Sink { return live })
	if got := manager.currentSink(); got != Sink(static) {
		t.Fatalf("currentSink via provider = %v, want static", got)
	}
	next := &fakeSink{}
	live = next
	if got := manager.currentSink(); got != Sink(next) {
		t.Fatalf("currentSink after swap = %v, want next", got)
	}
}
