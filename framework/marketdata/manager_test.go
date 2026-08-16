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
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

func testExternalID(label string) domain.ExternalID {
	return domain.ExternalID(label)
}

type fakeStore struct {
	mu                    sync.Mutex
	instances             []domain.MarketDataInstance
	instruments           map[string][]domain.MarketDataInstrument
	configuredInstances   []domain.MarketDataInstance
	configuredInstruments map[string][]domain.MarketDataInstrument
	instErr               error
	listEntered           chan struct{}
	listRelease           chan struct{}
}

func (s *fakeStore) ListMarketDataInstances(context.Context) ([]domain.MarketDataInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.MarketDataInstance(nil), s.configuredInstances...), nil
}

func (s *fakeStore) ListEnabledMarketDataInstances(context.Context) ([]domain.MarketDataInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.MarketDataInstance(nil), s.instances...), nil
}

func (s *fakeStore) ListEnabledMarketDataInstruments(
	_ context.Context, instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	s.mu.Lock()
	if s.instErr != nil {
		s.mu.Unlock()
		return nil, s.instErr
	}
	out := append(
		[]domain.MarketDataInstrument(nil), s.instruments[instance.String()]...,
	)
	entered, release := s.listEntered, s.listRelease
	s.listEntered = nil
	s.listRelease = nil
	s.mu.Unlock()
	if entered != nil {
		close(entered)
		<-release
	}
	return testMarketDataInstrumentsWithAssetIDs(out), nil
}

func (s *fakeStore) blockNextEnabledInstrumentList() (<-chan struct{}, chan<- struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listEntered = make(chan struct{})
	s.listRelease = make(chan struct{})
	return s.listEntered, s.listRelease
}

func (s *fakeStore) ListMarketDataInstruments(
	_ context.Context, instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.instErr != nil {
		return nil, s.instErr
	}
	return testMarketDataInstrumentsWithAssetIDs(
		s.configuredInstruments[instance.String()],
	), nil
}

func (s *fakeStore) setEnabledInstruments(
	instance domain.ExternalID, instruments []domain.MarketDataInstrument,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instruments[instance.String()] = testMarketDataInstrumentsWithAssetIDs(instruments)
}

type fakeSink struct {
	mu           sync.Mutex
	pushCalls    int
	pushed       []QuoteUpdate
	pushedOrder  []int
	cleared      []quoteInstrumentKey
	clearedOrder []int
	mutationSeq  int
	live         map[quoteInstrumentKey]QuoteUpdate
	clearCalls   int
	pushErr      error
	clearErr     error
	blockNext    bool
	blockEntered chan struct{}
	blockRelease chan struct{}
	clearEntered chan struct{}
	clearRelease chan struct{}
}

func (s *fakeSink) Push(update QuoteUpdate) error {
	s.mu.Lock()
	s.pushCalls++
	if s.blockNext {
		s.blockNext = false
		entered := s.blockEntered
		release := s.blockRelease
		s.mu.Unlock()
		close(entered)
		<-release
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	if s.pushErr != nil {
		return s.pushErr
	}
	s.mutationSeq++
	s.pushed = append(s.pushed, update)
	s.pushedOrder = append(s.pushedOrder, s.mutationSeq)
	if s.live == nil {
		s.live = make(map[quoteInstrumentKey]QuoteUpdate)
	}
	s.live[quoteInstrumentKey{base: update.Base, quote: update.Quote}] = update
	return nil
}

func (s *fakeSink) attemptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pushCalls
}

func (s *fakeSink) Clear(base, quote domain.EngineAssetID) error {
	s.mu.Lock()
	if s.clearEntered != nil {
		entered := s.clearEntered
		release := s.clearRelease
		s.clearEntered = nil
		s.clearRelease = nil
		s.mu.Unlock()
		close(entered)
		<-release
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	s.clearCalls++
	if s.clearErr != nil {
		return s.clearErr
	}
	pair := quoteInstrumentKey{base: base, quote: quote}
	s.mutationSeq++
	s.cleared = append(s.cleared, pair)
	s.clearedOrder = append(s.clearedOrder, s.mutationSeq)
	delete(s.live, pair)
	return nil
}

func (s *fakeSink) blockNextClear() (<-chan struct{}, chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearEntered = make(chan struct{})
	s.clearRelease = make(chan struct{})
	return s.clearEntered, s.clearRelease
}

func startManualReconciliationTestManager(
	t *testing.T, sink *fakeSink, refreshInterval time.Duration,
) (*Manager, *fakeStore, domain.ExternalID, domain.MarketDataInstrument) {
	t.Helper()
	instanceID := testExternalID("byo-reconciliation")
	instance := domain.MarketDataInstance{
		ExternalID: instanceID,
		Provider:   domain.MarketDataProviderBYO,
		Enabled:    true,
	}
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "Z/USD",
		BaseAsset: "Z", QuoteAsset: "USD", ManualPrice: "2", Enabled: true,
	}
	instrument = testMarketDataInstrumentWithAssetIDs(instrument)
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: domain.MarketDataProviderBYO,
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return newFakeConnector(), nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances:             []domain.MarketDataInstance{instance},
		configuredInstances:   []domain.MarketDataInstance{instance},
		instruments:           map[string][]domain.MarketDataInstrument{instanceID.String(): {instrument}},
		configuredInstruments: map[string][]domain.MarketDataInstrument{instanceID.String(): {instrument}},
	}
	manager := mustNewManager(t, registry, store, sink, nil)
	manager.manualRefreshInterval = refreshInterval
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(manager.Stop)
	waitFor(t, time.Second, func() bool { return sink.count() >= 2 })
	return manager, store, instanceID, instrument
}

func startBlockedSinkTransitionTestManager(
	t *testing.T, sink *fakeSink,
) (*Manager, *fakeStore, *fakeConnector, domain.MarketDataInstrument) {
	t.Helper()
	instanceID := testExternalID("byo-to-streaming")
	byoConnector := newFakeConnector()
	streamConnector := newConsumptionObservedFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: domain.MarketDataProviderBYO,
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return byoConnector, nil
		},
	}); err != nil {
		t.Fatalf("register BYO: %v", err)
	}
	if err := registry.Register(Provider{
		Type: domain.MarketDataProviderMock,
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return streamConnector, nil
		},
	}); err != nil {
		t.Fatalf("register streaming: %v", err)
	}
	instance := domain.MarketDataInstance{
		ExternalID: instanceID,
		Provider:   domain.MarketDataProviderBYO,
		Enabled:    true,
	}
	instrument := testMarketDataInstrumentWithAssetIDs(
		domain.MarketDataInstrument{
			Instance:       instanceID,
			ExternalSymbol: "Z/USD",
			BaseAsset:      "Z",
			QuoteAsset:     "USD",
			ManualPrice:    "2",
			Enabled:        true,
		},
	)
	store := &fakeStore{
		instances:           []domain.MarketDataInstance{instance},
		configuredInstances: []domain.MarketDataInstance{instance},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
		configuredInstruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
	}
	manager := mustNewManager(t, registry, store, sink, nil)
	manager.manualRefreshInterval = 0
	manager.stopGrace = blockedSinkStopGrace
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(manager.Stop)
	waitFor(t, time.Second, func() bool { return sink.count() == 2 })
	return manager, store, streamConnector, instrument
}

func (s *fakeSink) blockNextPush() (<-chan struct{}, chan<- struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockNext = true
	s.blockEntered = make(chan struct{})
	s.blockRelease = make(chan struct{})
	return s.blockEntered, s.blockRelease
}

func (s *fakeSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pushed)
}

func (s *fakeSink) snapshot() []QuoteUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]QuoteUpdate(nil), s.pushed...)
}

func (s *fakeSink) clearedSnapshot() []quoteInstrumentKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]quoteInstrumentKey(nil), s.cleared...)
}

func (s *fakeSink) mutationOrderSnapshot() (
	[]QuoteUpdate, []int, []quoteInstrumentKey, []int,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]QuoteUpdate(nil), s.pushed...),
		append([]int(nil), s.pushedOrder...),
		append([]quoteInstrumentKey(nil), s.cleared...),
		append([]int(nil), s.clearedOrder...)
}

func (s *fakeSink) clearCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clearCalls
}

func (s *fakeSink) liveQuote(pair quoteInstrumentKey) (QuoteUpdate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	update, ok := s.live[pair]
	return update, ok
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

func mustPush(t *testing.T, pushable Pushable, update QuoteUpdate) {
	t.Helper()
	if err := pushable.Push(context.Background(), update); err != nil {
		t.Fatalf("Push: %v", err)
	}
}

type fakeConnector struct {
	ch         chan QuoteUpdate
	done       chan struct{}
	consumed   chan QuoteUpdate
	mu         sync.Mutex
	wg         sync.WaitGroup
	deliveryWG sync.WaitGroup
	closed     bool
}

func newFakeConnector() *fakeConnector {
	return &fakeConnector{
		ch:   make(chan QuoteUpdate, 4),
		done: make(chan struct{}),
	}
}

func newConsumptionObservedFakeConnector() *fakeConnector {
	connector := newFakeConnector()
	connector.consumed = make(chan QuoteUpdate, cap(connector.ch))
	return connector
}

type reportingConnector struct {
	*fakeConnector
	statusReporter StatusReporter
}

func newReportingConnector() *reportingConnector {
	return &reportingConnector{fakeConnector: newFakeConnector()}
}

func (c *reportingConnector) SetStatusReporter(report StatusReporter) {
	c.statusReporter = report
}

func (c *fakeConnector) Subscribe(
	context.Context, []Subscription,
) (<-chan QuoteUpdate, error) {
	if c.consumed == nil {
		return c.ch, nil
	}
	delivered := make(chan QuoteUpdate)
	c.deliveryWG.Add(1)
	go func() {
		defer c.deliveryWG.Done()
		defer close(delivered)
		for {
			var update QuoteUpdate
			var ok bool
			select {
			case <-c.done:
				return
			case update, ok = <-c.ch:
				if !ok {
					return
				}
			}
			select {
			case <-c.done:
				return
			case delivered <- update:
			}
			select {
			case <-c.done:
				return
			case c.consumed <- update:
			}
		}
	}()
	return delivered, nil
}

func (c *fakeConnector) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	close(c.done)
	c.mu.Unlock()
	c.wg.Wait()
	close(c.ch)
	c.deliveryWG.Wait()
}

func (c *fakeConnector) Push(ctx context.Context, update QuoteUpdate) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.wg.Add(1)
	c.mu.Unlock()
	defer c.wg.Done()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case c.ch <- update:
		return nil
	}
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

func (c *blockingPushConnector) Push(ctx context.Context, update QuoteUpdate) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.ch <- update:
		return nil
	}
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

func TestManagerStartAppliesConfiguredManualQuotesOutsideLock(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("byo-1")
	connector := newBlockingPushConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: domain.MarketDataProviderBYO,
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{
				ExternalID: instanceID,
				Provider:   domain.MarketDataProviderBYO,
				Enabled:    true,
			},
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

func TestManagerUseSinkPreservesLastAcceptedSnapshotAcrossEngineRebuild(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-rebuild")
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
		instances: []domain.MarketDataInstance{{
			ExternalID: instanceID, Provider: "mock", Enabled: true,
		}},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
			}},
		},
	}
	firstSink := &fakeSink{}
	manager := mustNewManager(t, registry, store, firstSink, nil)
	liveSink := Sink(firstSink)
	manager.UseSinkProvider(func() Sink { return liveSink })
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()

	mustPush(t, connector, QuoteUpdate{
		Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "185",
	})
	waitFor(t, time.Second, func() bool { return firstSink.count() == 1 })

	secondSink := &fakeSink{}
	liveSink = secondSink
	snapshots := manager.QuoteSnapshots()
	if len(snapshots) != 1 || snapshots[0].Mark != "185" {
		t.Fatalf("quote snapshots after rebuild = %+v, want last accepted mark 185", snapshots)
	}

	mustPush(t, connector, QuoteUpdate{
		Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "186",
	})
	waitFor(t, time.Second, func() bool { return secondSink.count() == 1 })
	snapshots = manager.QuoteSnapshots()
	if len(snapshots) != 1 || snapshots[0].Mark != "186" {
		t.Fatalf("quote snapshots after rebuilt sink push = %+v, want mark 186", snapshots)
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
	mustPush(t, connector, QuoteUpdate{Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "185"})
	waitFor(t, time.Second, func() bool { return sink.count() == 1 })
	manager.Stop()
	quotes := manager.QuoteSnapshots()
	if len(quotes) != 1 || quotes[0].ExternalSymbol != "AAPL" {
		t.Fatalf("accepted quote snapshots = %+v", quotes)
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
	instance := domain.MarketDataInstance{
		ExternalID: instanceID, Provider: "mock", Enabled: true,
	}
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "AAPL",
		BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}
	store := &fakeStore{
		instances:             []domain.MarketDataInstance{instance},
		configuredInstances:   []domain.MarketDataInstance{instance},
		instruments:           map[string][]domain.MarketDataInstrument{instanceID.String(): {instrument}},
		configuredInstruments: map[string][]domain.MarketDataInstrument{instanceID.String(): {instrument}},
	}
	handler := &capturingHandler{}
	sink := &fakeSink{pushErr: errors.New("push boom")}
	manager := mustNewManager(t, registry, store, sink, slog.New(handler))

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()
	mustPush(t, connector, QuoteUpdate{Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "185"})
	waitFor(t, time.Second, func() bool { return sink.attemptCount() >= 1 })
	if err := manager.acquireGate(
		context.Background(), manager.sinkApplyGate,
	); err != nil {
		t.Fatalf("acquire sink gate: %v", err)
	}
	manager.releaseGate(manager.sinkApplyGate)
	if got := sink.attemptCount(); got != 1 {
		t.Fatalf("push attempts after rejected direct quote = %d, want 1", got)
	}
	if got := manager.QuoteSnapshots(); len(got) != 0 {
		t.Fatalf("visible quotes after rejected pushes = %+v, want none", got)
	}
	sink.mu.Lock()
	sink.pushErr = nil
	sink.mu.Unlock()
	mustPush(t, connector, QuoteUpdate{Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "186"})
	waitFor(t, time.Second, func() bool {
		quotes := manager.QuoteSnapshots()
		return sink.count() == 2 && len(quotes) == 1 && quotes[0].Mark == "186"
	})

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
	mustPush(t, connectors[1], QuoteUpdate{Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "185"})
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
	manager.UseSinkProvider(func() Sink { return nil })
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("second Start with nil provider sink: %v", err)
	}
	manager.Stop()
	manager.Stop()
}

func TestManagerAppliesConfiguredManualPriceOnceAtStartup(t *testing.T) {
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
	if got := manager.QuoteSnapshots()[0]; got.ExternalSymbol != "USDT/USD" ||
		got.Mark != "1" {
		t.Fatalf("manual quote = %+v", got)
	}
}

func TestManagerManualRefreshTracksLiveUpdateAndClear(t *testing.T) {
	instanceID := testExternalID("byo-refresh")
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: domain.MarketDataProviderBYO,
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "Z/USD",
		BaseAsset: "Z", QuoteAsset: "USD", ManualPrice: "2", Enabled: true,
	}
	instrument = testMarketDataInstrumentWithAssetIDs(instrument)
	store := &fakeStore{
		instances: []domain.MarketDataInstance{{
			ExternalID: instanceID,
			Provider:   domain.MarketDataProviderBYO,
			Enabled:    true,
		}},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	manager.manualRefreshInterval = 10 * time.Millisecond
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()

	// Startup publishes once; subsequent ticks must keep the static mark fresh.
	waitFor(t, time.Second, func() bool { return sink.count() >= 3 })

	instrument.ManualPrice = "3"
	store.setEnabledInstruments(instanceID, []domain.MarketDataInstrument{instrument})
	waitFor(t, time.Second, func() bool {
		for _, update := range sink.snapshot() {
			if update.Mark == "3" {
				return true
			}
		}
		return false
	})

	instrument.ManualPrice = ""
	store.setEnabledInstruments(instanceID, []domain.MarketDataInstrument{instrument})
	// Allow a tick that already read the old value to drain, then require the
	// count to remain stable across several refresh intervals.
	time.Sleep(3 * manager.manualRefreshInterval)
	countAfterClear := sink.count()
	time.Sleep(5 * manager.manualRefreshInterval)
	if got := sink.count(); got != countAfterClear {
		t.Fatalf("quotes after manual clear = %d -> %d, want refresh stopped",
			countAfterClear, got)
	}
	if got := manager.QuoteSnapshots(); len(got) != 0 {
		t.Fatalf("quote snapshots after successful manual clear = %+v, want none", got)
	}
}

func TestManagerManualClearIsAfterOldHeartbeatAndClearsSynthetic(t *testing.T) {
	instanceID := testExternalID("byo-clear-order")
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: domain.MarketDataProviderBYO,
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	instance := domain.MarketDataInstance{
		ExternalID: instanceID,
		Provider:   domain.MarketDataProviderBYO,
		Enabled:    true,
	}
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "Z/USD",
		BaseAsset: "Z", QuoteAsset: "USD", ManualPrice: "2", Enabled: true,
	}
	instrument = testMarketDataInstrumentWithAssetIDs(instrument)
	store := &fakeStore{
		instances:           []domain.MarketDataInstance{instance},
		configuredInstances: []domain.MarketDataInstance{instance},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
		configuredInstruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()
	waitFor(t, time.Second, func() bool { return sink.count() == 2 })

	entered, release := sink.blockNextPush()
	mustPush(t, connector, manualQuote(instrument))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old heartbeat did not reach sink")
	}

	cleared := instrument
	cleared.ManualPrice = ""
	store.setEnabledInstruments(instanceID, []domain.MarketDataInstrument{cleared})
	clearResult := make(chan error, 1)
	go func() {
		clearResult <- manager.PushManual(context.Background(), instanceID.String(), cleared)
	}()
	close(release)
	select {
	case err := <-clearResult:
		if err != nil {
			t.Fatalf("PushManual(clear): %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("manual clear did not drain after old heartbeat")
	}

	// Queue another stale old tick before a second clear barrier. The desired
	// mark is already empty, so drain must drop the stale tick; the barrier proves
	// it was observed before the assertions below.
	mustPush(t, connector, manualQuote(instrument))
	if err := manager.PushManual(context.Background(), instanceID.String(), cleared); err != nil {
		t.Fatalf("second PushManual(clear): %v", err)
	}
	if got := sink.count(); got != 4 {
		t.Fatalf("pushed quotes after clear = %d, want direct+synthetic startup and pre-clear heartbeat", got)
	}
	wantCleared := []quoteInstrumentKey{
		{base: testMarketDataAssetID("Z"), quote: testMarketDataAssetID("USD")},
		{base: testMarketDataAssetID("USD"), quote: testMarketDataAssetID("Z")},
		{base: testMarketDataAssetID("Z"), quote: testMarketDataAssetID("USD")},
		{base: testMarketDataAssetID("USD"), quote: testMarketDataAssetID("Z")},
	}
	gotCleared := sink.clearedSnapshot()
	if len(gotCleared) != len(wantCleared) {
		t.Fatalf("cleared pairs = %+v, want %+v", gotCleared, wantCleared)
	}
	for i := range wantCleared {
		if gotCleared[i] != wantCleared[i] {
			t.Fatalf("cleared pair %d = %+v, want %+v", i, gotCleared[i], wantCleared[i])
		}
	}
}

func TestManagerManualClearInvalidatesAlreadyReadRefresh(t *testing.T) {
	instanceID := testExternalID("byo-clear-read-race")
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: domain.MarketDataProviderBYO,
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	instance := domain.MarketDataInstance{
		ExternalID: instanceID,
		Provider:   domain.MarketDataProviderBYO,
		Enabled:    true,
	}
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "Z/USD",
		BaseAsset: "Z", QuoteAsset: "USD", ManualPrice: "2", Enabled: true,
	}
	instrument = testMarketDataInstrumentWithAssetIDs(instrument)
	store := &fakeStore{
		instances:           []domain.MarketDataInstance{instance},
		configuredInstances: []domain.MarketDataInstance{instance},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
		configuredInstruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	manager.manualRefreshInterval = 0
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()
	waitFor(t, time.Second, func() bool { return sink.count() == 2 })

	read, release := store.blockNextEnabledInstrumentList()
	refreshDone := make(chan struct{})
	go func() {
		manager.refreshManualMarksOnce(context.Background())
		close(refreshDone)
	}()
	select {
	case <-read:
	case <-time.After(time.Second):
		t.Fatal("manual refresh did not read the old mark")
	}

	cleared := instrument
	cleared.ManualPrice = ""
	store.setEnabledInstruments(instanceID, []domain.MarketDataInstrument{cleared})
	if err := manager.PushManual(context.Background(), instanceID.String(), cleared); err != nil {
		t.Fatalf("PushManual(clear): %v", err)
	}
	close(release)
	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("stale refresh snapshot did not finish")
	}

	// A second clear is a FIFO barrier after the stale refresh attempt.
	if err := manager.PushManual(context.Background(), instanceID.String(), cleared); err != nil {
		t.Fatalf("second PushManual(clear): %v", err)
	}
	if got := sink.count(); got != 2 {
		t.Fatalf("stale already-read heartbeat reached sink: pushes=%d", got)
	}
	if got := len(sink.clearedSnapshot()); got != 4 {
		t.Fatalf("direct+synthetic clear calls = %d, want 4", got)
	}
}

func TestManagerManualClearFailureRetriesStoredConfiguration(t *testing.T) {
	sink := &fakeSink{}
	manager, store, instanceID, instrument := startManualReconciliationTestManager(
		t, sink, 10*time.Millisecond,
	)

	cleared := instrument
	cleared.ManualPrice = ""
	sink.mu.Lock()
	sink.clearErr = errors.New("clear unavailable")
	sink.mu.Unlock()
	store.setEnabledInstruments(instanceID, []domain.MarketDataInstrument{cleared})
	err := manager.PushManual(context.Background(), instanceID.String(), cleared)
	if err == nil || !strings.Contains(err.Error(), "pending reconciliation") {
		t.Fatalf("PushManual(clear) error = %v, want pending reconciliation", err)
	}
	sink.mu.Lock()
	sink.clearErr = nil
	sink.mu.Unlock()

	waitFor(t, time.Second, func() bool {
		return len(sink.clearedSnapshot()) == 2
	})
	manager.mu.Lock()
	mark := manager.manualMarks[instanceID.String()][quoteInstrumentKey{
		base: instrument.BaseAssetID, quote: instrument.QuoteAssetID,
	}]
	manager.mu.Unlock()
	if mark != "" {
		t.Fatalf("observed manual mark = %q, want acknowledged clear", mark)
	}
	status := manager.InstanceStatuses()[instanceID.String()]
	foundPending := false
	for _, diag := range status.Diagnostics {
		if diag.Title == "Manual quote reconciliation pending" {
			foundPending = true
			break
		}
	}
	if !foundPending {
		t.Fatalf("diagnostics = %+v, want pending reconciliation", status.Diagnostics)
	}
}

func TestManagerManualClearTimeoutReleasesPublisherAndPreservesNewMark(t *testing.T) {
	sink := &fakeSink{}
	manager, store, instanceID, instrument := startManualReconciliationTestManager(
		t, sink, 0,
	)
	manager.stopGrace = 20 * time.Millisecond
	entered, release := sink.blockNextClear()
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})

	cleared := instrument
	cleared.ManualPrice = ""
	store.setEnabledInstruments(instanceID, []domain.MarketDataInstrument{cleared})
	started := time.Now()
	err := manager.PushManual(context.Background(), instanceID.String(), cleared)
	if err == nil || !strings.Contains(err.Error(), "pending reconciliation") {
		t.Fatalf("PushManual(clear) error = %v, want pending reconciliation", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("PushManual(clear) elapsed = %s, want bounded return", elapsed)
	}
	select {
	case <-entered:
	default:
		t.Fatal("manual clear did not reach the sink")
	}

	updated := instrument
	updated.ManualPrice = "3"
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := manager.PushManual(ctx, instanceID.String(), updated); err != nil {
		t.Fatalf("PushManual(new mark) while old clear is pending: %v", err)
	}
	close(release)
	released = true
	waitFor(t, time.Second, func() bool {
		for _, update := range sink.snapshot() {
			if update.Base == testMarketDataAssetID("Z") && update.Quote == testMarketDataAssetID("USD") && update.Mark == "3" {
				return true
			}
		}
		return false
	})
	manager.mu.Lock()
	mark := manager.manualMarks[instanceID.String()][quoteInstrumentKey{
		base: instrument.BaseAssetID, quote: instrument.QuoteAssetID,
	}]
	manager.mu.Unlock()
	if mark != "3" {
		t.Fatalf("observed manual mark = %q, want newer mark 3", mark)
	}
}

func TestManagerPushManualCancellationWhilePublisherIsBusy(t *testing.T) {
	sink := &fakeSink{}
	manager, _, instanceID, instrument := startManualReconciliationTestManager(
		t, sink, 0,
	)
	if err := manager.acquireGate(
		context.Background(), manager.manualPublisherGate,
	); err != nil {
		t.Fatalf("acquire publisher gate: %v", err)
	}
	defer manager.releaseGate(manager.manualPublisherGate)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	instrument.ManualPrice = "3"
	err := manager.PushManual(ctx, instanceID.String(), instrument)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PushManual error = %v, want context cancellation", err)
	}
}

func TestManagerPendingManualClearSurvivesRestart(t *testing.T) {
	sink := &fakeSink{}
	manager, store, instanceID, instrument := startManualReconciliationTestManager(
		t, sink, 0,
	)
	cleared := instrument
	cleared.ManualPrice = ""
	sink.mu.Lock()
	sink.clearErr = errors.New("clear unavailable")
	sink.mu.Unlock()
	store.setEnabledInstruments(instanceID, []domain.MarketDataInstrument{cleared})
	if err := manager.PushManual(
		context.Background(), instanceID.String(), cleared,
	); err == nil {
		t.Fatal("PushManual(clear) succeeded, want pending reconciliation")
	}
	sink.mu.Lock()
	sink.clearErr = nil
	sink.mu.Unlock()
	if err := manager.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		return len(sink.clearedSnapshot()) == 2
	})
}

func TestManagerRestartClearsEmptyManualDirectAndSyntheticPairs(t *testing.T) {
	sink := &fakeSink{}
	manager, store, instanceID, instrument := startManualReconciliationTestManager(
		t, sink, 0,
	)
	instrument.ManualPrice = ""
	store.setEnabledInstruments(
		instanceID, []domain.MarketDataInstrument{instrument},
	)
	if err := manager.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		return len(sink.clearedSnapshot()) == 2
	})
	want := []quoteInstrumentKey{
		{base: testMarketDataAssetID("Z"), quote: testMarketDataAssetID("USD")},
		{base: testMarketDataAssetID("USD"), quote: testMarketDataAssetID("Z")},
	}
	got := sink.clearedSnapshot()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cleared pair %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestManagerRetriesFailedStartupManualClear(t *testing.T) {
	sink := &fakeSink{}
	manager, store, instanceID, instrument := startManualReconciliationTestManager(
		t, sink, 10*time.Millisecond,
	)
	instrument.ManualPrice = ""
	store.setEnabledInstruments(
		instanceID, []domain.MarketDataInstrument{instrument},
	)
	sink.mu.Lock()
	sink.clearErr = errors.New("clear unavailable")
	sink.mu.Unlock()
	if err := manager.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	waitFor(t, time.Second, func() bool { return sink.clearCount() > 0 })
	sink.mu.Lock()
	sink.clearErr = nil
	sink.mu.Unlock()
	waitFor(t, time.Second, func() bool {
		return len(sink.clearedSnapshot()) == 2
	})
}

func TestManagerRetriesFailedSyntheticTopologyClear(t *testing.T) {
	sink := &fakeSink{}
	manager, store, instanceID, instrument := startManualReconciliationTestManager(
		t, sink, 10*time.Millisecond,
	)
	reverse := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "USD/Z",
		BaseAsset: "USD", QuoteAsset: "Z", Enabled: false,
	}
	store.mu.Lock()
	store.configuredInstruments[instanceID.String()] =
		[]domain.MarketDataInstrument{instrument, reverse}
	store.mu.Unlock()
	sink.mu.Lock()
	sink.clearErr = errors.New("clear unavailable")
	sink.mu.Unlock()
	if err := manager.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	waitFor(t, time.Second, func() bool { return sink.clearCount() > 0 })
	sink.mu.Lock()
	sink.clearErr = nil
	sink.mu.Unlock()
	waitFor(t, time.Second, func() bool {
		return len(sink.clearedSnapshot()) == 1
	})
	cleared := sink.clearedSnapshot()[0]
	if want := (quoteInstrumentKey{base: testMarketDataAssetID("USD"), quote: testMarketDataAssetID("Z")}); cleared != want {
		t.Fatalf("cleared pair = %+v, want synthetic %+v", cleared, want)
	}
	if update, ok := sink.liveQuote(quoteInstrumentKey{base: testMarketDataAssetID("Z"), quote: testMarketDataAssetID("USD")}); !ok || update.Mark != "2" {
		t.Fatalf("live direct quote = %+v/%v, want mark 2", update, ok)
	}
}

const (
	blockedSinkStopGrace         = 50 * time.Millisecond
	blockedSinkObservationWindow = 100 * time.Millisecond
)

func TestManagerRestartWaitsForOldManualClearBeforeStreamingTick(t *testing.T) {
	sink := &fakeSink{}
	manager, store, streamConnector, instrument :=
		startBlockedSinkTransitionTestManager(t, sink)
	instanceID := instrument.Instance
	entered, release := sink.blockNextClear()
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})
	cleared := instrument
	cleared.ManualPrice = ""
	if err := manager.PushManual(
		context.Background(), instanceID.String(), cleared,
	); err == nil {
		t.Fatal("PushManual(clear) succeeded, want timeout")
	}
	select {
	case <-entered:
	default:
		t.Fatal("old manual clear did not enter sink")
	}
	streamingInstance := domain.MarketDataInstance{
		ExternalID: instanceID,
		Provider:   domain.MarketDataProviderMock,
		Enabled:    true,
	}
	streamingInstrument := instrument
	streamingInstrument.ManualPrice = ""
	store.mu.Lock()
	store.instances = []domain.MarketDataInstance{streamingInstance}
	store.configuredInstances = []domain.MarketDataInstance{streamingInstance}
	store.instruments[instanceID.String()] =
		[]domain.MarketDataInstrument{streamingInstrument}
	store.configuredInstruments[instanceID.String()] =
		[]domain.MarketDataInstrument{streamingInstrument}
	store.mu.Unlock()
	restarted := make(chan error, 1)
	go func() { restarted <- manager.Restart() }()
	select {
	case err := <-restarted:
		close(release)
		released = true
		t.Fatalf("Restart returned before old manual clear completed: %v", err)
	case <-time.After(blockedSinkObservationWindow):
	}
	close(release)
	released = true
	select {
	case err := <-restarted:
		if err != nil {
			t.Fatalf("Restart: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Restart did not finish after old manual clear")
	}
	mustPush(t, streamConnector, QuoteUpdate{
		Base: testMarketDataAssetID("Z"), Quote: testMarketDataAssetID("USD"), Mark: "9",
	})
	waitFor(t, time.Second, func() bool {
		update, ok := sink.liveQuote(quoteInstrumentKey{base: testMarketDataAssetID("Z"), quote: testMarketDataAssetID("USD")})
		return sink.clearCount() >= 4 && ok && update.Mark == "9"
	})
}

func TestManagerSinkBarrierOrdersQueuedStreamingQuoteAfterInheritedClear(
	t *testing.T,
) {
	const quoteMark = "9"
	sink := &fakeSink{}
	manager, store, streamConnector, instrument :=
		startBlockedSinkTransitionTestManager(t, sink)
	instanceID := instrument.Instance

	holdWindow := blockedSinkObservationWindow
	if holdWindow <= manager.stopGrace || holdWindow >= 3*manager.stopGrace {
		t.Fatalf(
			"blocked sink window %v must be between stop grace %v and %v",
			holdWindow, manager.stopGrace, 3*manager.stopGrace,
		)
	}
	entered, release := sink.blockNextClear()
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})
	clearedInstrument := instrument
	clearedInstrument.ManualPrice = ""
	clearResult := make(chan error, 1)
	go func() {
		clearResult <- manager.PushManual(
			context.Background(), instanceID.String(), clearedInstrument,
		)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old manual clear did not enter sink")
	}
	holdTimer := time.NewTimer(holdWindow)
	defer holdTimer.Stop()

	streamingInstance := domain.MarketDataInstance{
		ExternalID: instanceID,
		Provider:   domain.MarketDataProviderMock,
		Enabled:    true,
	}
	streamingInstrument := instrument
	streamingInstrument.ManualPrice = ""
	store.mu.Lock()
	store.instances = []domain.MarketDataInstance{streamingInstance}
	store.configuredInstances = []domain.MarketDataInstance{streamingInstance}
	store.instruments[instanceID.String()] =
		[]domain.MarketDataInstrument{streamingInstrument}
	store.configuredInstruments[instanceID.String()] =
		[]domain.MarketDataInstrument{streamingInstrument}
	store.mu.Unlock()

	restarted := make(chan error, 1)
	go func() { restarted <- manager.Restart() }()
	pushed := make(chan error, 1)
	go func() {
		pushed <- streamConnector.Push(context.Background(), QuoteUpdate{
			Base:  instrument.BaseAssetID,
			Quote: instrument.QuoteAssetID,
			Mark:  quoteMark,
		})
	}()
	select {
	case err := <-pushed:
		if err != nil {
			t.Fatalf("queue streaming quote: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("streaming quote was not queued while clear was blocked")
	}
	select {
	case err := <-clearResult:
		if err == nil {
			t.Fatal("PushManual(clear) succeeded, want timeout")
		}
	case <-time.After(time.Second):
		t.Fatal("PushManual(clear) did not time out while sink was blocked")
	}
	gateCtx, cancelGateWait := context.WithTimeout(context.Background(), time.Second)
	if err := manager.acquireGate(gateCtx, manager.manualPublisherGate); err != nil {
		cancelGateWait()
		t.Fatalf("hold startup manual publisher gate: %v", err)
	}
	cancelGateWait()
	publisherGateHeld := true
	t.Cleanup(func() {
		if publisherGateHeld {
			manager.releaseGate(manager.manualPublisherGate)
		}
	})
	if got := sink.count(); got != 2 {
		t.Fatalf("sink pushes while clear blocked = %d, want initial 2", got)
	}
	select {
	case err := <-restarted:
		close(release)
		released = true
		t.Fatalf("Restart returned while old clear was blocked: %v", err)
	case <-holdTimer.C:
	}
	close(release)
	released = true

	select {
	case err := <-restarted:
		if err != nil {
			t.Fatalf("Restart: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Restart did not finish after old clear")
	}
	select {
	case consumed := <-streamConnector.consumed:
		if consumed.Base != instrument.BaseAssetID ||
			consumed.Quote != instrument.QuoteAssetID ||
			consumed.Mark != quoteMark {
			t.Fatalf("consumed streaming quote = %+v", consumed)
		}
	case <-time.After(time.Second):
		t.Fatal("drain did not consume queued streaming quote")
	}
	if got := sink.count(); got != 2 {
		t.Fatalf("sink pushes after drain consumption = %d, want initial 2", got)
	}
	time.Sleep(holdWindow)
	if got := sink.count(); got != 2 {
		t.Fatalf("sink pushes while inherited clear was blocked = %d, want initial 2", got)
	}

	sink.mu.Lock()
	clearAttempts := sink.clearCalls
	sink.clearErr = errors.New("startup inherited clear unavailable")
	sink.mu.Unlock()
	manager.releaseGate(manager.manualPublisherGate)
	publisherGateHeld = false
	waitFor(t, time.Second, func() bool {
		return sink.clearCount() > clearAttempts
	})
	gateCtx, cancelGateWait = context.WithTimeout(context.Background(), time.Second)
	if err := manager.acquireGate(gateCtx, manager.manualPublisherGate); err != nil {
		cancelGateWait()
		t.Fatalf("wait for failed startup inherited clear: %v", err)
	}
	cancelGateWait()
	manager.releaseGate(manager.manualPublisherGate)
	time.Sleep(holdWindow)
	if got := sink.count(); got != 2 {
		t.Fatalf("sink pushes while inherited clear remained pending = %d, want initial 2", got)
	}
	sink.mu.Lock()
	sink.clearErr = nil
	sink.mu.Unlock()
	manager.refreshManualMarksOnce(context.Background())

	pair := quoteInstrumentKey{
		base: instrument.BaseAssetID, quote: instrument.QuoteAssetID,
	}
	waitFor(t, time.Second, func() bool {
		update, ok := sink.liveQuote(pair)
		return ok && update.Mark == quoteMark
	})

	pushes, pushOrder, clears, clearOrder := sink.mutationOrderSnapshot()
	quoteOrder := 0
	for i, update := range pushes {
		if update.Base == pair.base && update.Quote == pair.quote &&
			update.Mark == quoteMark {
			quoteOrder = pushOrder[i]
		}
	}
	lastClearOrder := 0
	matchingClears := 0
	for i, clearedPair := range clears {
		if clearedPair == pair {
			matchingClears++
			lastClearOrder = clearOrder[i]
		}
	}
	if matchingClears < 2 {
		t.Fatalf(
			"direct clear calls for inherited quote = %d, want old and startup clears",
			matchingClears,
		)
	}
	if quoteOrder == 0 || lastClearOrder >= quoteOrder {
		t.Fatalf(
			"sink mutation order last-clear=%d quote=%d, want clear before quote",
			lastClearOrder, quoteOrder,
		)
	}
}

func TestManagerRejectsOldGenerationClearBeforeSink(t *testing.T) {
	sink := &fakeSink{}
	manager, _, instanceID, instrument := startManualReconciliationTestManager(
		t, sink, 0,
	)
	pair := quoteInstrumentKey{base: instrument.BaseAssetID, quote: instrument.QuoteAssetID}
	manager.mu.Lock()
	state := manualClearState{
		generation: manager.manualGeneration,
		token:      manager.nextManualTokenLocked(),
		direct:     true,
		synthetic:  true,
	}
	manager.pendingManualClears[manualQuoteIdentity{
		instanceID: instanceID.String(), pair: pair,
	}] = state
	manager.manualGeneration++
	manager.mu.Unlock()
	if err := manager.applyManualClear(
		context.Background(), instanceID.String(), manualClearUpdate(pair, state),
	); err != nil {
		t.Fatalf("apply stale clear: %v", err)
	}
	if got := len(sink.clearedSnapshot()); got != 0 {
		t.Fatalf("stale generation cleared %d pairs, want none", got)
	}
}

func TestManagerStopTimeoutOrdersNewRunMarkAfterOldClear(t *testing.T) {
	sink := &fakeSink{}
	manager, store, instanceID, instrument := startManualReconciliationTestManager(
		t, sink, 0,
	)
	manager.stopGrace = blockedSinkStopGrace
	entered, release := sink.blockNextClear()
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})
	cleared := instrument
	cleared.ManualPrice = ""
	store.setEnabledInstruments(instanceID, []domain.MarketDataInstrument{cleared})
	if err := manager.PushManual(
		context.Background(), instanceID.String(), cleared,
	); err == nil {
		t.Fatal("PushManual(clear) succeeded, want timeout")
	}
	select {
	case <-entered:
	default:
		t.Fatal("old clear did not enter sink")
	}
	updated := instrument
	updated.ManualPrice = "3"
	store.setEnabledInstruments(instanceID, []domain.MarketDataInstrument{updated})
	restarted := make(chan error, 1)
	go func() { restarted <- manager.Restart() }()
	select {
	case err := <-restarted:
		close(release)
		released = true
		t.Fatalf("Restart returned before old clear completed: %v", err)
	case <-time.After(blockedSinkObservationWindow):
	}
	close(release)
	released = true
	select {
	case err := <-restarted:
		if err != nil {
			t.Fatalf("Restart: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Restart did not finish after old clear")
	}
	waitFor(t, time.Second, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		acknowledged := manager.manualAcknowledged[manualQuoteIdentity{
			instanceID: instanceID.String(),
			pair: quoteInstrumentKey{
				base: instrument.BaseAssetID, quote: instrument.QuoteAssetID,
			},
		}]
		return acknowledged.mark == "3"
	})
}

func TestManagerStopTimeoutOrdersNewStreamingQuoteAfterOldPush(t *testing.T) {
	instanceID := testExternalID("streaming-stop-timeout")
	oldConnector := newReportingConnector()
	newConnector := newReportingConnector()
	registry := NewRegistry()
	builds := 0
	if err := registry.Register(Provider{
		Type: domain.MarketDataProviderMock,
		Build: func(domain.MarketDataInstance) (Connector, error) {
			builds++
			if builds == 1 {
				return oldConnector, nil
			}
			return newConnector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{{
			ExternalID: instanceID,
			Provider:   domain.MarketDataProviderMock,
			Enabled:    true,
		}},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance:       instanceID,
				ExternalSymbol: "Z/USD",
				BaseAsset:      "Z",
				QuoteAsset:     "USD",
				Enabled:        true,
			}},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	manager.stopGrace = blockedSinkStopGrace
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(manager.Stop)

	entered, release := sink.blockNextPush()
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})
	mustPush(t, oldConnector, QuoteUpdate{Base: testMarketDataAssetID("Z"), Quote: testMarketDataAssetID("USD"), Mark: "1"})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old streaming quote did not enter sink")
	}
	restarted := make(chan error, 1)
	go func() { restarted <- manager.Restart() }()
	select {
	case err := <-restarted:
		close(release)
		released = true
		t.Fatalf("Restart returned before old push completed: %v", err)
	case <-time.After(blockedSinkObservationWindow):
	}
	close(release)
	released = true
	select {
	case err := <-restarted:
		if err != nil {
			t.Fatalf("Restart: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Restart did not finish after old push")
	}
	if oldConnector.statusReporter == nil {
		t.Fatal("old connector did not receive status reporter")
	}
	oldConnector.statusReporter(false, "stale connection error")
	status := manager.InstanceStatuses()[instanceID.String()]
	if status.State != StateOK || status.Error != "" {
		t.Fatalf("old callback replaced new status: %+v", status)
	}
	for _, diag := range status.Diagnostics {
		if diag.Detail == "stale connection error" {
			t.Fatalf("old callback recorded stale diagnostic: %+v", diag)
		}
	}

	newEntered, newRelease := sink.blockNextPush()
	newReleased := false
	t.Cleanup(func() {
		if !newReleased {
			close(newRelease)
		}
	})
	mustPush(t, newConnector, QuoteUpdate{Base: testMarketDataAssetID("Z"), Quote: testMarketDataAssetID("USD"), Mark: "2"})
	select {
	case <-newEntered:
	case <-time.After(time.Second):
		t.Fatal("new streaming quote did not enter sink after old push")
	}
	quotes := manager.QuoteSnapshots()
	if len(quotes) != 1 || quotes[0].Mark != "1" {
		t.Fatalf("quote snapshots while new push is blocked = %+v, want old accepted mark 1", quotes)
	}

	close(newRelease)
	newReleased = true
	waitFor(t, time.Second, func() bool {
		update, ok := sink.liveQuote(quoteInstrumentKey{base: testMarketDataAssetID("Z"), quote: testMarketDataAssetID("USD")})
		quotes := manager.QuoteSnapshots()
		return ok && update.Mark == "2" && len(quotes) == 1 && quotes[0].Mark == "2"
	})
}

func TestManagerStopEndsManualRefresh(t *testing.T) {
	instanceID := testExternalID("byo-stop-refresh")
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: domain.MarketDataProviderBYO,
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{{
			ExternalID: instanceID,
			Provider:   domain.MarketDataProviderBYO,
			Enabled:    true,
		}},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "Z/USD",
				BaseAsset: "Z", QuoteAsset: "USD", ManualPrice: "2", Enabled: true,
			}},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	manager.manualRefreshInterval = 10 * time.Millisecond
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, time.Second, func() bool { return sink.count() >= 2 })
	manager.Stop()
	countAfterStop := sink.count()
	time.Sleep(5 * manager.manualRefreshInterval)
	if got := sink.count(); got != countAfterStop {
		t.Fatalf("quotes after Stop = %d -> %d, want heartbeat stopped",
			countAfterStop, got)
	}
}

func TestManagerDoesNotRefreshPushableStreamingProvider(t *testing.T) {
	instanceID := testExternalID("mock-pushable")
	connector := newFakeConnector()
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: domain.MarketDataProviderMock,
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{{
			ExternalID: instanceID,
			Provider:   domain.MarketDataProviderMock,
			Enabled:    true,
		}},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "EURUSD",
				BaseAsset: "EUR", QuoteAsset: "USD", ManualPrice: "2", Enabled: true,
			}},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	manager.manualRefreshInterval = 10 * time.Millisecond
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()
	time.Sleep(5 * manager.manualRefreshInterval)
	if got := sink.count(); got != 0 {
		t.Fatalf("streaming provider manual refresh count = %d, want zero", got)
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
	manual := testMarketDataInstrumentWithAssetIDs(domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "USDT/USD",
		BaseAsset: "USDT", QuoteAsset: "USD", ManualPrice: "1", Enabled: true,
	})
	if err := manager.PushManual(context.Background(), instanceID.String(), manual); err != nil {
		t.Fatalf("PushManual: %v", err)
	}
	waitFor(t, time.Second, func() bool { return sink.count() == 1 })
	manager.Stop()
	if got := manager.QuoteSnapshots()[0]; got.ExternalSymbol != "USDT/USD" ||
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

	if err := manager.PushManual(context.Background(), instanceID.String(), domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "AAPL",
		BaseAsset: "AAPL", QuoteAsset: "USD", ManualPrice: "185", Enabled: true,
	}); err != nil {
		t.Fatalf("PushManual: %v", err)
	}

	time.Sleep(20 * time.Millisecond)
	if sink.count() != 0 {
		t.Fatalf("sink count = %d, want no manual push", sink.count())
	}
	if got := len(manager.QuoteSnapshots()); got != 0 {
		t.Fatalf("visible quotes = %d, want none", got)
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

	if err := manager.PushManual(context.Background(), instanceID.String(), domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "Z/USD",
		BaseAsset: "Z", QuoteAsset: "USD", ManualPrice: "2", Enabled: true,
	}); err != nil {
		t.Fatalf("PushManual: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	if sink.count() != 0 {
		t.Fatalf("sink count = %d, want no push for an unapplied instrument", sink.count())
	}
	if got := len(manager.QuoteSnapshots()); got != 0 {
		t.Fatalf("visible quotes = %d, want none for an unapplied instrument", got)
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
	mustPush(t, connector, QuoteUpdate{Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "100"})
	waitFor(t, time.Second, func() bool { return len(manager.QuoteSnapshots()) == 1 })
	if _, ok := manager.QuoteUpdateInterval(instanceID.String(), "AAPL"); ok {
		t.Fatalf("interval known after first quote")
	}
	time.Sleep(5 * time.Millisecond)
	mustPush(t, connector, QuoteUpdate{Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "101"})
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
	connector.ch <- QuoteUpdate{Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"), Mark: "100"}
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

func TestManagerStartReconcilesThroughSinkProvider(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-reconcile-provider")
	instance := domain.MarketDataInstance{
		ExternalID: instanceID, Provider: "mock", Enabled: true,
	}
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "NEW/USD",
		BaseAsset: "NEW", QuoteAsset: "USD", Enabled: true,
	}
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
		instances:           []domain.MarketDataInstance{instance},
		configuredInstances: []domain.MarketDataInstance{instance},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
		configuredInstruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
	}
	staleSink := &fakeSink{
		clearErr: errors.New("stale resolver does not know NEW"),
	}
	liveSink := &fakeSink{}
	manager := mustNewManager(t, registry, store, staleSink, nil)
	manager.UseSinkProvider(func() Sink { return liveSink })

	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	mustPush(t, connector, QuoteUpdate{
		Base:  testMarketDataAssetID("NEW"),
		Quote: testMarketDataAssetID("USD"),
		Mark:  "42",
	})
	pair := quoteInstrumentKey{
		base:  testMarketDataAssetID("NEW"),
		quote: testMarketDataAssetID("USD"),
	}
	waitFor(t, time.Second, func() bool {
		_, live := liveSink.liveQuote(pair)
		return live && len(manager.QuoteSnapshots()) == 1
	})
	manager.Stop()

	store.setEnabledInstruments(instanceID, nil)
	store.mu.Lock()
	store.configuredInstruments[instanceID.String()] = nil
	store.mu.Unlock()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start after instrument removal: %v", err)
	}
	defer manager.Stop()

	if got := staleSink.clearCount(); got != 0 {
		t.Fatalf("stale sink clear calls = %d, want zero", got)
	}
	if got := liveSink.clearCount(); got != 2 {
		t.Fatalf("provider sink clear calls = %d, want direct and inverse clears", got)
	}
	if got := manager.QuoteSnapshots(); len(got) != 0 {
		t.Fatalf("quote snapshots after provider reconciliation = %+v, want none", got)
	}
}

func TestManagerStartRejectsNilSinkProviderResult(t *testing.T) {
	t.Parallel()

	staticSink := &fakeSink{}
	manager := mustNewManager(t, NewRegistry(), &fakeStore{}, staticSink, nil)
	manager.UseSinkProvider(func() Sink { return nil })

	if err := manager.Start(context.Background()); !errors.Is(err, ErrNilSink) {
		t.Fatalf("Start error = %v, want %v", err, ErrNilSink)
	}
}

func TestManagerStartClearsRemovedInstrumentAndSyntheticInverse(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-reconcile-remove")
	instance := domain.MarketDataInstance{
		ExternalID: instanceID, Provider: "mock", Enabled: true,
	}
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "AAPL",
		BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}
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
		instances:           []domain.MarketDataInstance{instance},
		configuredInstances: []domain.MarketDataInstance{instance},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
		configuredInstruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	mustPush(t, connector, QuoteUpdate{
		Base:  testMarketDataAssetID("AAPL"),
		Quote: testMarketDataAssetID("USD"),
		Mark:  "185",
	})
	waitFor(t, time.Second, func() bool {
		_, direct := sink.liveQuote(quoteInstrumentKey{
			base:  testMarketDataAssetID("AAPL"),
			quote: testMarketDataAssetID("USD"),
		})
		_, inverse := sink.liveQuote(quoteInstrumentKey{
			base:  testMarketDataAssetID("USD"),
			quote: testMarketDataAssetID("AAPL"),
		})
		return len(manager.QuoteSnapshots()) == 1 && direct && inverse
	})
	manager.Stop()

	store.setEnabledInstruments(instanceID, nil)
	store.mu.Lock()
	store.configuredInstruments[instanceID.String()] = nil
	store.mu.Unlock()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start after instrument removal: %v", err)
	}
	defer manager.Stop()
	if got := manager.QuoteSnapshots(); len(got) != 0 {
		t.Fatalf("quote snapshots after instrument removal = %+v, want none", got)
	}
	for _, pair := range []quoteInstrumentKey{
		{
			base:  testMarketDataAssetID("AAPL"),
			quote: testMarketDataAssetID("USD"),
		},
		{
			base:  testMarketDataAssetID("USD"),
			quote: testMarketDataAssetID("AAPL"),
		},
	} {
		if quote, ok := sink.liveQuote(pair); ok {
			t.Fatalf("live quote for cleared pair %+v = %+v", pair, quote)
		}
	}
}

func TestManagerStartPreservesConfiguredEnabledQuote(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-reconcile-keep")
	instance := domain.MarketDataInstance{
		ExternalID: instanceID, Provider: "mock", Enabled: true,
	}
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "AAPL",
		BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}
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
		instances:           []domain.MarketDataInstance{instance},
		configuredInstances: []domain.MarketDataInstance{instance},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
		configuredInstruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	mustPush(t, connectors[0], QuoteUpdate{
		Base:  testMarketDataAssetID("AAPL"),
		Quote: testMarketDataAssetID("USD"),
		Mark:  "185",
	})
	waitFor(t, time.Second, func() bool {
		return len(manager.QuoteSnapshots()) == 1
	})
	manager.Stop()

	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start after stop: %v", err)
	}
	defer manager.Stop()
	if got := manager.QuoteSnapshots(); len(got) != 1 || got[0].Mark != "185" {
		t.Fatalf("quote snapshots after reconciliation = %+v, want mark 185", got)
	}
	if got := sink.clearCount(); got != 0 {
		t.Fatalf("clear calls for configured quote = %d, want zero", got)
	}
}

func TestManagerStartBuildFailurePreservesConfiguredEnabledQuote(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-reconcile-build-failure")
	instance := domain.MarketDataInstance{
		ExternalID: instanceID, Provider: "mock", Enabled: true,
	}
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "AAPL",
		BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}
	connector := newFakeConnector()
	builds := 0
	buildErr := errors.New("provider unavailable")
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "mock",
		Build: func(domain.MarketDataInstance) (Connector, error) {
			builds++
			if builds == 1 {
				return connector, nil
			}
			return nil, buildErr
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances:           []domain.MarketDataInstance{instance},
		configuredInstances: []domain.MarketDataInstance{instance},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
		configuredInstruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	mustPush(t, connector, QuoteUpdate{
		Base:  testMarketDataAssetID("AAPL"),
		Quote: testMarketDataAssetID("USD"),
		Mark:  "185",
	})
	waitFor(t, time.Second, func() bool {
		return len(manager.QuoteSnapshots()) == 1
	})
	manager.Stop()

	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start with provider failure: %v", err)
	}
	defer manager.Stop()
	status := manager.InstanceStatuses()[instanceID.String()]
	if status.State != StateError || !strings.Contains(status.Error, buildErr.Error()) {
		t.Fatalf("status after provider failure = %+v", status)
	}
	if got := manager.QuoteSnapshots(); len(got) != 1 || got[0].Mark != "185" {
		t.Fatalf("quote snapshots after provider failure = %+v, want mark 185", got)
	}
	if got := sink.clearCount(); got != 0 {
		t.Fatalf("clear calls after provider failure = %d, want zero", got)
	}
}

func TestManagerStartReturnsQuoteReconciliationClearFailure(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-reconcile-clear-failure")
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
		instances: []domain.MarketDataInstance{{
			ExternalID: instanceID, Provider: "mock", Enabled: true,
		}},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance: instanceID, ExternalSymbol: "AAPL",
				BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
			}},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	mustPush(t, connector, QuoteUpdate{
		Base:  testMarketDataAssetID("AAPL"),
		Quote: testMarketDataAssetID("USD"),
		Mark:  "185",
	})
	waitFor(t, time.Second, func() bool {
		return len(manager.QuoteSnapshots()) == 1
	})
	manager.Stop()

	store.setEnabledInstruments(instanceID, nil)
	clearErr := fmt.Errorf("clearer row missing: %w", domain.ErrNotFound)
	sink.mu.Lock()
	sink.clearErr = clearErr
	sink.mu.Unlock()
	err := manager.Start(ctx)
	if !errors.Is(err, clearErr) {
		t.Fatalf("Start clear error = %v, want %v", err, clearErr)
	}
	if got := manager.QuoteSnapshots(); len(got) != 1 || got[0].Mark != "185" {
		t.Fatalf("quote snapshots after failed clear = %+v, want retained mark 185", got)
	}
}

func TestManagerStartDropsUnresolvableQuoteIdentity(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-reconcile-asset-gone")
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
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "AAPL",
		BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}
	store := &fakeStore{
		instances: []domain.MarketDataInstance{{
			ExternalID: instanceID, Provider: "mock", Enabled: true,
		}},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pair := quoteInstrumentKey{
		base:  testMarketDataAssetID("AAPL"),
		quote: testMarketDataAssetID("USD"),
	}
	mustPush(t, connector, QuoteUpdate{
		Base: pair.base, Quote: pair.quote, Mark: "185",
	})
	waitFor(t, time.Second, func() bool {
		return len(manager.QuoteSnapshots()) == 1
	})
	manager.Stop()

	store.setEnabledInstruments(instanceID, nil)
	sink.mu.Lock()
	sink.clearErr = fmt.Errorf("resolver asset is gone: %w", ErrUnknownAsset)
	sink.mu.Unlock()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start with retired asset id: %v", err)
	}
	if got := manager.QuoteSnapshots(); len(got) != 0 {
		t.Fatalf("quote snapshots after retired asset = %+v, want none", got)
	}
	if got := sink.clearCount(); got != 1 {
		t.Fatalf("clear attempts for retired asset = %d, want 1", got)
	}
	if _, ok := sink.liveQuote(pair); !ok {
		t.Fatal("unresolvable registry quote was cleared")
	}
	manager.Stop()

	if err := manager.Start(ctx); err != nil {
		t.Fatalf("second Start with retired asset id: %v", err)
	}
	defer manager.Stop()
	if got := sink.clearCount(); got != 1 {
		t.Fatalf("clear attempts after second Start = %d, want 1", got)
	}
}

func TestManagerReconciliationWarningReportsAttemptedInversePair(t *testing.T) {
	t.Parallel()

	pair := quoteInstrumentKey{
		base:  testMarketDataAssetID("AAPL"),
		quote: testMarketDataAssetID("USD"),
	}
	staleIdentity := quoteSnapshotIdentity{
		instanceID: "stale-instance",
		external:   "AAPL/USD",
		pair:       pair,
	}
	allowed := map[quoteSnapshotIdentity]struct{}{
		{
			instanceID: "configured-instance",
			external:   "AAPLUSD",
			pair:       pair,
		}: {},
	}
	handler := &capturingHandler{}
	manager := &Manager{
		logger:       slog.New(handler),
		latestQuotes: map[quoteSnapshotIdentity]domain.MarketDataQuote{staleIdentity: {}},
	}
	sink := &fakeSink{clearErr: fmt.Errorf("asset gone: %w", ErrUnknownAsset)}
	if err := manager.reconcileQuoteRegistry(
		sink, allowed, map[quoteInstrumentKey]struct{}{},
	); err != nil {
		t.Fatalf("reconcileQuoteRegistry: %v", err)
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.records) != 1 {
		t.Fatalf("warning records = %d, want 1", len(handler.records))
	}
	record := handler.records[0]
	attributes := make(map[string]slog.Value)
	record.Attrs(func(attribute slog.Attr) bool {
		attributes[attribute.Key] = attribute.Value
		return true
	})
	if got := attributes["instance_id"].String(); got != staleIdentity.instanceID {
		t.Fatalf("instance_id = %q, want %q", got, staleIdentity.instanceID)
	}
	if got := attributes["external_symbol"].String(); got != staleIdentity.external {
		t.Fatalf("external_symbol = %q, want %q", got, staleIdentity.external)
	}
	if got := fmt.Sprint(attributes["base_asset_id"].Any()); got != fmt.Sprint(pair.quote) {
		t.Fatalf("base_asset_id = %s, want attempted inverse %d", got, pair.quote)
	}
	if got := fmt.Sprint(attributes["quote_asset_id"].Any()); got != fmt.Sprint(pair.base) {
		t.Fatalf("quote_asset_id = %s, want attempted inverse %d", got, pair.base)
	}
	if !attributes["inverse"].Bool() {
		t.Fatal("inverse = false, want true")
	}
}

func TestManagerUnknownManualAssetDoesNotBlockStreamingFeed(t *testing.T) {
	t.Parallel()

	byoID := testExternalID("byo-asset-gone")
	streamingID := testExternalID("mock-after-byo-asset-gone")
	byoInstance := domain.MarketDataInstance{
		ExternalID: byoID,
		Provider:   domain.MarketDataProviderBYO,
		Enabled:    true,
	}
	streamingInstance := domain.MarketDataInstance{
		ExternalID: streamingID,
		Provider:   domain.MarketDataProviderMock,
		Enabled:    true,
	}
	byoInstrument := testMarketDataInstrumentWithAssetIDs(
		domain.MarketDataInstrument{
			Instance:       byoID,
			ExternalSymbol: "AAPL/USD",
			BaseAsset:      "AAPL",
			QuoteAsset:     "USD",
			ManualPrice:    "185",
			Enabled:        true,
		},
	)
	streamingInstrument := testMarketDataInstrumentWithAssetIDs(
		domain.MarketDataInstrument{
			Instance:       streamingID,
			ExternalSymbol: "MSFT/USD",
			BaseAsset:      "MSFT",
			QuoteAsset:     "USD",
			Enabled:        true,
		},
	)
	streamingConnectors := make([]*fakeConnector, 0, 2)
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: domain.MarketDataProviderBYO,
		Build: func(domain.MarketDataInstance) (Connector, error) {
			return newFakeConnector(), nil
		},
	}); err != nil {
		t.Fatalf("register BYO: %v", err)
	}
	if err := registry.Register(Provider{
		Type: domain.MarketDataProviderMock,
		Build: func(domain.MarketDataInstance) (Connector, error) {
			connector := newFakeConnector()
			streamingConnectors = append(streamingConnectors, connector)
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register streaming: %v", err)
	}
	instances := []domain.MarketDataInstance{byoInstance, streamingInstance}
	store := &fakeStore{
		instances:           instances,
		configuredInstances: instances,
		instruments: map[string][]domain.MarketDataInstrument{
			byoID.String():       {byoInstrument},
			streamingID.String(): {streamingInstrument},
		},
		configuredInstruments: map[string][]domain.MarketDataInstrument{
			byoID.String():       {byoInstrument},
			streamingID.String(): {streamingInstrument},
		},
	}
	sink := &fakeSink{pushErr: errors.New("manual push unavailable")}
	manager := mustNewManager(t, registry, store, sink, nil)
	manager.manualRefreshInterval = 0
	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		return sink.attemptCount() == 1
	})
	if got := manager.QuoteSnapshots(); len(got) != 0 {
		t.Fatalf("quote snapshots after failed manual push = %+v, want none", got)
	}
	manager.Stop()

	store.setEnabledInstruments(byoID, nil)
	store.mu.Lock()
	store.configuredInstruments[byoID.String()] = nil
	store.mu.Unlock()
	sink.mu.Lock()
	sink.pushErr = nil
	sink.clearErr = fmt.Errorf("resolver asset is gone: %w", ErrUnknownAsset)
	sink.mu.Unlock()

	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start after deleting BYO asset: %v", err)
	}
	defer manager.Stop()
	var retired Diagnostic
	waitFor(t, time.Second, func() bool {
		status := manager.InstanceStatuses()[byoID.String()]
		for _, diag := range status.Diagnostics {
			if diag.Title == "Manual quote identity retired" {
				retired = diag
				return true
			}
		}
		return false
	})
	if retired.Level != DiagWarn || retired.Code != CodeInternalError ||
		retired.Kind != DiagKindConfig ||
		retired.Instrument != byoInstrument.ExternalSymbol ||
		!strings.Contains(retired.Detail, "a stale quote may remain") {
		t.Fatalf("retired manual quote diagnostic = %+v", retired)
	}
	if got := manager.QuoteSnapshots(); len(got) != 0 {
		t.Fatalf("quote snapshots after deleting BYO asset = %+v, want none", got)
	}
	before := sink.count()
	streamingPair := quoteInstrumentKey{
		base:  streamingInstrument.BaseAssetID,
		quote: streamingInstrument.QuoteAssetID,
	}
	mustPush(t, streamingConnectors[len(streamingConnectors)-1], QuoteUpdate{
		Base: streamingPair.base, Quote: streamingPair.quote, Mark: "410",
	})
	waitFor(t, time.Second, func() bool {
		update, ok := sink.liveQuote(streamingPair)
		return ok && update.Mark == "410" && sink.count() > before
	})
}

func TestManagerStartPreservesQuoteNamedByAnotherEnabledIdentity(t *testing.T) {
	t.Parallel()

	firstID := testExternalID("mock-reconcile-shared-first")
	secondID := testExternalID("mock-reconcile-shared-second")
	instances := []domain.MarketDataInstance{
		{ExternalID: firstID, Provider: "mock", Enabled: true},
		{ExternalID: secondID, Provider: "mock", Enabled: true},
	}
	first := domain.MarketDataInstrument{
		Instance: firstID, ExternalSymbol: "AAPL-1",
		BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}
	second := domain.MarketDataInstrument{
		Instance: secondID, ExternalSymbol: "AAPL-2",
		BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}
	connectors := make(map[string]*fakeConnector)
	registry := NewRegistry()
	if err := registry.Register(Provider{
		Type: "mock",
		Build: func(instance domain.MarketDataInstance) (Connector, error) {
			connector := newFakeConnector()
			if _, ok := connectors[instance.ExternalID.String()]; !ok {
				connectors[instance.ExternalID.String()] = connector
			}
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	store := &fakeStore{
		instances:           instances,
		configuredInstances: instances,
		instruments: map[string][]domain.MarketDataInstrument{
			firstID.String():  {first},
			secondID.String(): {second},
		},
		configuredInstruments: map[string][]domain.MarketDataInstrument{
			firstID.String():  {first},
			secondID.String(): {second},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pair := quoteInstrumentKey{
		base:  testMarketDataAssetID("AAPL"),
		quote: testMarketDataAssetID("USD"),
	}
	inverse := quoteInstrumentKey{base: pair.quote, quote: pair.base}
	mustPush(t, connectors[firstID.String()], QuoteUpdate{
		Base: pair.base, Quote: pair.quote, Mark: "184",
	})
	waitFor(t, time.Second, func() bool {
		return len(manager.QuoteSnapshots()) == 1
	})
	mustPush(t, connectors[secondID.String()], QuoteUpdate{
		Base: pair.base, Quote: pair.quote, Mark: "185",
	})
	waitFor(t, time.Second, func() bool {
		return len(manager.QuoteSnapshots()) == 2
	})
	manager.Stop()

	store.setEnabledInstruments(firstID, nil)
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start after disabling first instrument: %v", err)
	}
	defer manager.Stop()
	update, ok := sink.liveQuote(pair)
	if !ok || update.Mark != "185" {
		t.Fatalf("shared live quote = %+v/%v, want retained mark 185", update, ok)
	}
	if _, ok := sink.liveQuote(inverse); !ok {
		t.Fatal("shared synthetic inverse quote was cleared")
	}
	snapshots := manager.QuoteSnapshots()
	if len(snapshots) != 1 || snapshots[0].Instance != secondID {
		t.Fatalf("shared-pair snapshots = %+v, want second identity", snapshots)
	}
	for _, cleared := range sink.clearedSnapshot() {
		if cleared == pair || cleared == inverse {
			t.Fatalf("allowed shared pair was cleared: %+v", cleared)
		}
	}
}

func TestManagerStartClearsDisabledDirectionAndPreservesConfiguredReverseQuote(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-reconcile-directed-disabled")
	instance := domain.MarketDataInstance{
		ExternalID: instanceID, Provider: "mock", Enabled: true,
	}
	forward := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "EURUSD",
		BaseAsset: "EUR", QuoteAsset: "USD", Enabled: true,
	}
	reverse := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "USDEUR",
		BaseAsset: "USD", QuoteAsset: "EUR", Enabled: true,
	}
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
		instances:           []domain.MarketDataInstance{instance},
		configuredInstances: []domain.MarketDataInstance{instance},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {forward, reverse},
		},
		configuredInstruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {forward, reverse},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	forwardPair := quoteInstrumentKey{
		base:  testMarketDataAssetID("EUR"),
		quote: testMarketDataAssetID("USD"),
	}
	reversePair := quoteInstrumentKey{
		base: forwardPair.quote, quote: forwardPair.base,
	}
	mustPush(t, connectors[0], QuoteUpdate{
		Base: forwardPair.base, Quote: forwardPair.quote, Mark: "1.1",
	})
	waitFor(t, time.Second, func() bool {
		return len(manager.QuoteSnapshots()) == 1
	})
	mustPush(t, connectors[0], QuoteUpdate{
		Base: reversePair.base, Quote: reversePair.quote, Mark: "0.9",
	})
	waitFor(t, time.Second, func() bool {
		return len(manager.QuoteSnapshots()) == 2
	})
	manager.Stop()

	store.setEnabledInstruments(
		instanceID, []domain.MarketDataInstrument{reverse},
	)
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start after disabling forward instrument: %v", err)
	}
	defer manager.Stop()
	update, ok := sink.liveQuote(reversePair)
	if !ok || update.Mark != "0.9" {
		t.Fatalf("reverse live quote = %+v/%v, want retained mark 0.9", update, ok)
	}
	if _, ok := sink.liveQuote(forwardPair); ok {
		t.Fatal("disabled forward quote remained live")
	}
	for _, cleared := range sink.clearedSnapshot() {
		if cleared == reversePair {
			t.Fatalf("configured reverse pair was cleared: %+v", cleared)
		}
	}
}

func TestManagerStartClearsDeletedDirectionWhenReverseBecomesSynthetic(t *testing.T) {
	t.Parallel()

	instanceID := testExternalID("mock-reconcile-directed")
	instance := domain.MarketDataInstance{
		ExternalID: instanceID, Provider: "mock", Enabled: true,
	}
	forward := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "EURUSD",
		BaseAsset: "EUR", QuoteAsset: "USD", Enabled: true,
	}
	reverse := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "USDEUR",
		BaseAsset: "USD", QuoteAsset: "EUR", Enabled: true,
	}
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
		instances:           []domain.MarketDataInstance{instance},
		configuredInstances: []domain.MarketDataInstance{instance},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {forward, reverse},
		},
		configuredInstruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {forward, reverse},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	forwardPair := quoteInstrumentKey{
		base:  testMarketDataAssetID("EUR"),
		quote: testMarketDataAssetID("USD"),
	}
	reversePair := quoteInstrumentKey{
		base: forwardPair.quote, quote: forwardPair.base,
	}
	mustPush(t, connectors[0], QuoteUpdate{
		Base: forwardPair.base, Quote: forwardPair.quote, Mark: "1.1",
	})
	waitFor(t, time.Second, func() bool {
		return len(manager.QuoteSnapshots()) == 1
	})
	mustPush(t, connectors[0], QuoteUpdate{
		Base: reversePair.base, Quote: reversePair.quote, Mark: "0.9",
	})
	waitFor(t, time.Second, func() bool {
		return len(manager.QuoteSnapshots()) == 2
	})
	manager.Stop()

	store.setEnabledInstruments(
		instanceID, []domain.MarketDataInstrument{reverse},
	)
	store.mu.Lock()
	store.configuredInstruments[instanceID.String()] =
		[]domain.MarketDataInstrument{reverse}
	store.mu.Unlock()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start after deleting forward instrument: %v", err)
	}
	defer manager.Stop()
	update, ok := sink.liveQuote(reversePair)
	if !ok || update.Mark != "0.9" {
		t.Fatalf("reverse live quote = %+v/%v, want retained mark 0.9", update, ok)
	}
	if _, ok := sink.liveQuote(forwardPair); ok {
		t.Fatal("deleted forward quote remained live")
	}
	for _, cleared := range sink.clearedSnapshot() {
		if cleared == reversePair {
			t.Fatalf("configured reverse pair was cleared: %+v", cleared)
		}
	}
}

func TestManagerStartWaitsForSinkApplyGateBeforeReconciliation(t *testing.T) {
	instanceID := testExternalID("mock-reconcile-late-push")
	instance := domain.MarketDataInstance{
		ExternalID: instanceID, Provider: "mock", Enabled: true,
	}
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "AAPL",
		BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}
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
		instances:           []domain.MarketDataInstance{instance},
		configuredInstances: []domain.MarketDataInstance{instance},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
		configuredInstruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	manager.stopGrace = 200 * time.Millisecond
	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(manager.Stop)
	pair := quoteInstrumentKey{
		base:  testMarketDataAssetID("AAPL"),
		quote: testMarketDataAssetID("USD"),
	}
	mustPush(t, connectors[0], QuoteUpdate{
		Base: pair.base, Quote: pair.quote, Mark: "185",
	})
	waitFor(t, time.Second, func() bool {
		return len(manager.QuoteSnapshots()) == 1
	})
	manager.Stop()
	store.setEnabledInstruments(instanceID, nil)
	store.mu.Lock()
	store.configuredInstruments[instanceID.String()] = nil
	store.mu.Unlock()

	if err := manager.acquireGate(ctx, manager.sinkApplyGate); err != nil {
		t.Fatalf("take sink apply gate: %v", err)
	}
	releaseAfter := 5 * manager.stopGrace / 4
	released := make(chan struct{})
	time.AfterFunc(releaseAfter, func() {
		manager.releaseGate(manager.sinkApplyGate)
		close(released)
	})
	startedAt := time.Now()
	err := manager.Start(ctx)
	elapsed := time.Since(startedAt)
	<-released
	if err != nil {
		t.Fatalf("Start after sink apply gate release: %v", err)
	}
	if elapsed <= manager.stopGrace || elapsed >= 2*manager.stopGrace {
		t.Fatalf(
			"Start gate wait took %v, want between %v and %v",
			elapsed, manager.stopGrace, 2*manager.stopGrace,
		)
	}
	if got := manager.QuoteSnapshots(); len(got) != 0 {
		t.Fatalf("quote snapshots after reconciliation = %+v", got)
	}
	if _, ok := sink.liveQuote(pair); ok {
		t.Fatal("direct quote remained live after reconciliation")
	}
	if _, ok := sink.liveQuote(quoteInstrumentKey{
		base: pair.quote, quote: pair.base,
	}); ok {
		t.Fatal("inverse quote remained live after reconciliation")
	}
}

func TestManagerStartTimesOutWaitingForSinkApplyGate(t *testing.T) {
	instanceID := testExternalID("mock-reconcile-stuck-push")
	instance := domain.MarketDataInstance{
		ExternalID: instanceID, Provider: "mock", Enabled: true,
	}
	instrument := domain.MarketDataInstrument{
		Instance: instanceID, ExternalSymbol: "AAPL",
		BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}
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
		instances:           []domain.MarketDataInstance{instance},
		configuredInstances: []domain.MarketDataInstance{instance},
		instruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
		configuredInstruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {instrument},
		},
	}
	sink := &fakeSink{}
	manager := mustNewManager(t, registry, store, sink, nil)
	manager.stopGrace = 400 * time.Millisecond
	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(manager.Stop)
	pair := quoteInstrumentKey{
		base:  testMarketDataAssetID("AAPL"),
		quote: testMarketDataAssetID("USD"),
	}
	mustPush(t, connectors[0], QuoteUpdate{
		Base: pair.base, Quote: pair.quote, Mark: "185",
	})
	waitFor(t, time.Second, func() bool {
		return len(manager.QuoteSnapshots()) == 1
	})
	manager.Stop()
	store.setEnabledInstruments(instanceID, nil)
	store.mu.Lock()
	store.configuredInstruments[instanceID.String()] = nil
	store.mu.Unlock()

	if err := manager.acquireGate(ctx, manager.sinkApplyGate); err != nil {
		t.Fatalf("take sink apply gate: %v", err)
	}
	startedAt := time.Now()
	err := manager.Start(ctx)
	elapsed := time.Since(startedAt)
	manager.releaseGate(manager.sinkApplyGate)
	if err == nil {
		t.Fatal("Start succeeded while the sink apply gate was held")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start gate error = %v, want deadline exceeded", err)
	}
	if !strings.Contains(
		err.Error(), "restart blocked by an in-flight engine sink operation",
	) {
		t.Fatalf("Start gate error = %v, want engine sink operation cause", err)
	}
	minimumWait := 3 * manager.stopGrace / 2
	maximumWait := 5 * manager.stopGrace / 2
	if elapsed <= minimumWait || elapsed >= maximumWait {
		t.Fatalf(
			"Start gate timeout took %v, want between %v and %v",
			elapsed, minimumWait, maximumWait,
		)
	}

	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start after sink apply gate release: %v", err)
	}
	if got := manager.QuoteSnapshots(); len(got) != 0 {
		t.Fatalf("quote snapshots after reconciliation = %+v", got)
	}
	if _, ok := sink.liveQuote(pair); ok {
		t.Fatal("direct quote remained live after reconciliation")
	}
	inverse := quoteInstrumentKey{base: pair.quote, quote: pair.base}
	if _, ok := sink.liveQuote(inverse); ok {
		t.Fatal("inverse quote remained live after reconciliation")
	}
}
