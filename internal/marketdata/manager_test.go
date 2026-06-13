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

	"go.openpit.dev/officer/internal/domain"
)

// fakeStore is a fixed in-memory Store for the manager: it returns the enabled
// instances and, per instance, its enabled instruments. The instrument lists are
// pre-filtered to "enabled" to mirror the store's WHERE enabled = 1 query.
type fakeStore struct {
	instances   []domain.MarketDataInstance
	instruments map[string][]domain.MarketDataInstrument
	quotes      []domain.MarketDataQuote
	instErr     error
}

func (s *fakeStore) ListEnabledMarketDataInstances(
	context.Context,
) ([]domain.MarketDataInstance, error) {
	return s.instances, nil
}

func (s *fakeStore) ListEnabledMarketDataInstruments(
	_ context.Context, instanceID string,
) ([]domain.MarketDataInstrument, error) {
	if s.instErr != nil {
		return nil, s.instErr
	}
	return s.instruments[instanceID], nil
}

func (s *fakeStore) UpsertMarketDataQuote(
	_ context.Context, quote domain.MarketDataQuote,
) error {
	s.quotes = append(s.quotes, quote)
	return nil
}

// fakeSink records every pushed quote under a mutex.
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

// TestManager_NoEnabledIsCleanNoop verifies that with nothing enabled Start is a
// clean no-op and Stop is safe.
func TestManager_NoEnabledIsCleanNoop(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	sink := &fakeSink{}
	m := NewManager(store, sink, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.Stop()
	if sink.count() != 0 {
		t.Fatalf("want no pushes with nothing enabled, got %d", sink.count())
	}
}

// TestManager_FansMockQuotesToSink verifies an enabled mock instance subscribes
// its enabled instruments and fans quotes into the sink, then stops cleanly.
func TestManager_FansMockQuotesToSink(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ID: "mock-1", Type: domain.MarketDataProviderMock, Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			"mock-1": {
				{InstanceID: "mock-1", ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true},
			},
		},
	}
	sink := &fakeSink{}
	m := NewManager(store, sink, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The mock emits on a sub-second timer; wait for at least one push.
	waitFor(t, 2*time.Second, func() bool { return sink.count() > 0 })
	m.Stop()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.pushed) == 0 {
		t.Fatal("want at least one fanned quote")
	}
	first := sink.pushed[0]
	if first.Base != "AAPL" || first.Quote != "USD" {
		t.Fatalf("fanned quote instrument = %s/%s, want AAPL/USD", first.Base, first.Quote)
	}
}

// TestManager_SkipsInstanceWithNoInstruments verifies an enabled instance whose
// instruments are all disabled is skipped (no subscription, no push).
func TestManager_SkipsInstanceWithNoInstruments(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ID: "mock-1", Type: domain.MarketDataProviderMock, Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{}, // none enabled
	}
	sink := &fakeSink{}
	m := NewManager(store, sink, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	m.Stop()
	if sink.count() != 0 {
		t.Fatalf("want no pushes for an instance with no enabled instruments, got %d", sink.count())
	}
}

// TestManager_UnknownTypeSkippedNotFailed verifies an unknown provider type is
// skipped without failing Start; a healthy mock instance alongside it still
// streams.
func TestManager_UnknownTypeSkippedNotFailed(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ID: "weird", Type: "does-not-exist", Enabled: true},
			{ID: "mock-1", Type: domain.MarketDataProviderMock, Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			"weird":  {{InstanceID: "weird", ExternalSymbol: "X", BaseAsset: "X", QuoteAsset: "Y", Enabled: true}},
			"mock-1": {{InstanceID: "mock-1", ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true}},
		},
	}
	sink := &fakeSink{}
	m := NewManager(store, sink, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start must not fail on an unknown type: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return sink.count() > 0 })
	m.Stop()
}

// TestManager_StartIdempotent verifies a second Start while running is a no-op.
func TestManager_StartIdempotent(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	m := NewManager(store, &fakeSink{}, nil)
	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	m.Stop()
	m.Stop() // idempotent
}

func TestNewConnector_BinanceRegistered(t *testing.T) {
	t.Parallel()

	connector, err := newConnector(domain.MarketDataInstance{
		ID:   "binance-1",
		Type: domain.MarketDataProviderBinance,
	})
	if err != nil {
		t.Fatalf("newConnector: %v", err)
	}
	if _, ok := connector.(*binanceConnector); !ok {
		t.Fatalf("connector type = %T, want *binanceConnector", connector)
	}
}

// TestManager_PushErrorDoesNotStopFeed verifies a sink push error is tolerated:
// the drain keeps running (the manager logs and continues) rather than crashing.
func TestManager_PushErrorDoesNotStopFeed(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		instances: []domain.MarketDataInstance{
			{ID: "mock-1", Type: domain.MarketDataProviderMock, Enabled: true},
		},
		instruments: map[string][]domain.MarketDataInstrument{
			"mock-1": {{InstanceID: "mock-1", ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true}},
		},
	}
	sink := &fakeSink{pushErr: errors.New("push boom")}
	m := NewManager(store, sink, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Let several ticks flow; a push error must not deadlock or panic.
	time.Sleep(50 * time.Millisecond)
	m.Stop()
}

// waitFor polls cond until it returns true or the timeout elapses.
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
