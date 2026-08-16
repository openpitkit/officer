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

package backend_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/internal/backend"
)

type blockingStatusMarketDataRuntime struct {
	*fakeMarketDataRuntime
	statusEntered chan struct{}
	statusRelease chan struct{}
	stopCalled    chan struct{}
}

type managerSnapshotStore struct {
	node *fakeNode
}

func (s managerSnapshotStore) ListMarketDataInstances(
	ctx context.Context,
) ([]domain.MarketDataInstance, error) {
	return s.node.ListMarketDataInstances(ctx)
}

func (s managerSnapshotStore) ListEnabledMarketDataInstances(
	ctx context.Context,
) ([]domain.MarketDataInstance, error) {
	instances, err := s.node.ListMarketDataInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.MarketDataInstance, 0, len(instances))
	for _, instance := range instances {
		if instance.Enabled {
			out = append(out, instance)
		}
	}
	return out, nil
}

func (s managerSnapshotStore) ListMarketDataInstruments(
	ctx context.Context,
	instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	return s.node.ListMarketDataInstruments(ctx, instance)
}

func (s managerSnapshotStore) ListEnabledMarketDataInstruments(
	ctx context.Context,
	instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	instruments, err := s.node.ListMarketDataInstruments(ctx, instance)
	if err != nil {
		return nil, err
	}
	out := make([]domain.MarketDataInstrument, 0, len(instruments))
	for _, instrument := range instruments {
		if instrument.Enabled {
			out = append(out, instrument)
		}
	}
	return out, nil
}

type managerSnapshotConnector struct {
	updates chan marketdata.QuoteUpdate
}

func (c *managerSnapshotConnector) Subscribe(
	context.Context,
	[]marketdata.Subscription,
) (<-chan marketdata.QuoteUpdate, error) {
	return c.updates, nil
}

func (*managerSnapshotConnector) Close() {}

type managerSnapshotSink struct{}

func (managerSnapshotSink) Push(marketdata.QuoteUpdate) error { return nil }

func (r *blockingStatusMarketDataRuntime) InstanceStatuses() map[string]marketdata.InstanceRuntimeStatus {
	close(r.statusEntered)
	<-r.statusRelease
	return r.fakeMarketDataRuntime.InstanceStatuses()
}

func (r *blockingStatusMarketDataRuntime) Stop() {
	r.stopCalled <- struct{}{}
	r.fakeMarketDataRuntime.Stop()
}

func TestService_ListMarketDataBuildsStatus(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	md := &fakeMarketDataRuntime{snapshots: []marketdata.QuoteSnapshot{
		{
			MarketDataQuote: domain.MarketDataQuote{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "AAPL",
				Mark:           "100",
				AsOf:           now,
				ReceivedAt:     now,
			},
			BaseAssetID:  testMarketDataAssetID("AAPL"),
			QuoteAssetID: testMarketDataAssetID("USD"),
		},
	}}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
		{ExternalID: mdID("mock-2"), Provider: domain.MarketDataProviderMock, Enabled: false},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		mdID("mock-1").String(): {
			{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "AAPL",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
			{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "MSFT",
				BaseAsset:      "MSFT",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
		mdID("mock-2").String(): {
			{
				Instance:       mdID("mock-2"),
				ExternalSymbol: "TSLA",
				BaseAsset:      "TSLA",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
	}
	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	if status.FreshnessSeconds != int(backend.MarketDataFreshnessTTL.Seconds()) {
		t.Fatalf("freshness seconds mismatch: %+v", status)
	}
	for _, want := range []string{
		domain.MarketDataProviderIB,
		domain.MarketDataProviderBinance,
		domain.MarketDataProviderKraken,
		domain.MarketDataProviderCoinbase,
		domain.MarketDataProviderAlpaca,
		domain.MarketDataProviderOKX,
		domain.MarketDataProviderBybit,
		domain.MarketDataProviderOANDA,
		domain.MarketDataProviderFinnhub,
		domain.MarketDataProviderBYO,
		domain.MarketDataProviderMock,
	} {
		if !containsProviderType(status.Providers, want) {
			t.Fatalf("providers = %+v, want %s", status.Providers, want)
		}
	}
	if len(status.Instances) != 2 || len(status.Instances[0].Instruments) != 2 ||
		len(status.Instances[1].Instruments) != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}
	first := status.Instances[0].Instruments[0]
	second := status.Instances[0].Instruments[1]
	disabledSource := status.Instances[1].Instruments[0]
	if first.Quote == nil || first.Stale {
		t.Fatalf("fresh quoted instrument should not be stale: %+v", first)
	}
	if !second.Stale {
		t.Fatalf("enabled instrument without quote should be stale: %+v", second)
	}
	if disabledSource.Stale {
		t.Fatalf("disabled source instrument should not be stale: %+v", disabledSource)
	}
}

func TestService_ListMarketDataKeepsManagerSnapshotAfterEngineRebuild(t *testing.T) {
	t.Parallel()
	instanceID := mdID("manager-rebuild")
	fn := &fakeNode{
		mdInstances: []domain.MarketDataInstance{{
			ExternalID: instanceID,
			Provider:   domain.MarketDataProviderMock,
			Enabled:    true,
		}},
		mdInstruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance:       instanceID,
				ExternalSymbol: "AAPL",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Enabled:        true,
			}},
		},
		orders: make(map[domain.ExternalID]domain.Order),
	}
	connector := &managerSnapshotConnector{
		updates: make(chan marketdata.QuoteUpdate, 1),
	}
	registry := marketdata.NewRegistry()
	if err := registry.Register(marketdata.Provider{
		Type: domain.MarketDataProviderMock,
		Build: func(domain.MarketDataInstance) (marketdata.Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	manager, err := marketdata.NewManager(
		registry,
		managerSnapshotStore{node: fn},
		managerSnapshotSink{},
		nil,
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	liveSink := marketdata.Sink(managerSnapshotSink{})
	manager.UseSinkProvider(func() marketdata.Sink { return liveSink })
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Stop()

	now := time.Now().UTC()
	connector.updates <- marketdata.QuoteUpdate{
		Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"),
		Mark: "185", AsOf: now,
	}
	deadline := time.Now().Add(time.Second)
	for len(manager.QuoteSnapshots()) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshots := manager.QuoteSnapshots(); len(snapshots) != 1 {
		t.Fatalf("accepted quote snapshots = %+v, want one", snapshots)
	}

	liveSink = managerSnapshotSink{}
	svc := backend.New(&fakeRouter{node: fn}, manager, nil)
	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	got := status.Instances[0].Instruments[0]
	if got.Quote == nil || got.Quote.Mark != "185" || !got.Quote.AsOf.Equal(now) {
		t.Fatalf("quote after rebuild = %+v, want accepted manager snapshot", got.Quote)
	}
}

func TestService_ListMarketDataKeepsManagerSnapshotWhileManagerStopped(t *testing.T) {
	t.Parallel()
	instanceID := mdID("manager-stopped")
	fn := &fakeNode{
		mdInstances: []domain.MarketDataInstance{{
			ExternalID: instanceID,
			Provider:   domain.MarketDataProviderMock,
			Enabled:    true,
		}},
		mdInstruments: map[string][]domain.MarketDataInstrument{
			instanceID.String(): {{
				Instance:       instanceID,
				ExternalSymbol: "AAPL",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Enabled:        true,
			}},
		},
		orders: make(map[domain.ExternalID]domain.Order),
	}
	connector := &managerSnapshotConnector{
		updates: make(chan marketdata.QuoteUpdate, 1),
	}
	registry := marketdata.NewRegistry()
	if err := registry.Register(marketdata.Provider{
		Type: domain.MarketDataProviderMock,
		Build: func(domain.MarketDataInstance) (marketdata.Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	manager, err := marketdata.NewManager(
		registry,
		managerSnapshotStore{node: fn},
		managerSnapshotSink{},
		nil,
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(manager.Stop)

	now := time.Now().UTC()
	connector.updates <- marketdata.QuoteUpdate{
		Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"),
		Mark: "185", AsOf: now,
	}
	deadline := time.Now().Add(time.Second)
	for len(manager.QuoteSnapshots()) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshots := manager.QuoteSnapshots(); len(snapshots) != 1 {
		t.Fatalf("accepted quote snapshots = %+v, want one", snapshots)
	}

	manager.Stop()
	if applied := manager.AppliedConfig(); len(applied) != 0 {
		t.Fatalf("applied config after Stop = %+v, want empty", applied)
	}

	svc := backend.New(&fakeRouter{node: fn}, manager, nil)
	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	got := status.Instances[0].Instruments[0]
	if got.Quote == nil || got.Quote.Mark != "185" || !got.Quote.AsOf.Equal(now) {
		t.Fatalf("quote while manager stopped = %+v, want accepted manager snapshot", got.Quote)
	}
}

func TestService_ListMarketDataSerializesManagerLifecycle(t *testing.T) {
	md := &blockingStatusMarketDataRuntime{
		fakeMarketDataRuntime: &fakeMarketDataRuntime{},
		statusEntered:         make(chan struct{}),
		statusRelease:         make(chan struct{}),
		stopCalled:            make(chan struct{}, 1),
	}
	released := false
	t.Cleanup(func() {
		if !released {
			close(md.statusRelease)
		}
	})
	svc, _ := newTestServiceWithMarketDataRuntime(md)
	listDone := make(chan error, 1)
	go func() {
		_, err := svc.ListMarketData(context.Background())
		listDone <- err
	}()
	select {
	case <-md.statusEntered:
	case <-time.After(time.Second):
		t.Fatal("ListMarketData did not enter runtime status read")
	}

	restartDone := make(chan error, 1)
	go func() {
		restartDone <- svc.RestartMarketData(context.Background())
	}()
	select {
	case <-md.stopCalled:
		t.Fatal("RestartMarketData entered manager lifecycle during ListMarketData")
	case <-time.After(20 * time.Millisecond):
	}
	close(md.statusRelease)
	released = true
	if err := <-listDone; err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	select {
	case <-md.stopCalled:
	case <-time.After(time.Second):
		t.Fatal("RestartMarketData did not continue after ListMarketData")
	}
	if err := <-restartDone; err != nil {
		t.Fatalf("RestartMarketData: %v", err)
	}
}

func TestMarketDataFreshnessTTLContract(t *testing.T) {
	t.Parallel()

	if marketdata.FreshnessTTL != 70*time.Second {
		t.Fatalf("marketdata.FreshnessTTL = %s, want 70s", marketdata.FreshnessTTL)
	}
	if backend.MarketDataFreshnessTTL != marketdata.FreshnessTTL {
		t.Fatalf(
			"backend.MarketDataFreshnessTTL = %s, want %s",
			backend.MarketDataFreshnessTTL,
			marketdata.FreshnessTTL,
		)
	}
}

func TestService_ListMarketDataSurfacesAppliedSyntheticInverse(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{
		statuses: map[string]marketdata.InstanceRuntimeStatus{
			mdID("mock-1").String(): {State: marketdata.StateOK},
		},
		applied: map[string]marketdata.AppliedInstanceConfig{
			mdID("mock-1").String(): {
				Provider: domain.MarketDataProviderMock,
				Subscriptions: []marketdata.Subscription{{
					External: "EURUSD", Base: testMarketDataAssetID("EUR"), Quote: testMarketDataAssetID("USD"),
					SyntheticInverse: true,
				}},
			},
		},
	}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	now := time.Now().UTC()
	fn.mdInstances = []domain.MarketDataInstance{{
		ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true,
	}}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		mdID("mock-1").String(): {{
			Instance: mdID("mock-1"), ExternalSymbol: "EURUSD",
			BaseAsset: "EUR", QuoteAsset: "USD", Enabled: true,
		}},
	}
	md.snapshots = []marketdata.QuoteSnapshot{{
		MarketDataQuote: domain.MarketDataQuote{
			Instance: mdID("mock-1"), ExternalSymbol: "EURUSD",
			BaseAsset: "EUR", QuoteAsset: "USD",
			Mark: "2", Bid: "4", Ask: "8", AsOf: now, ReceivedAt: now,
		},
		BaseAssetID:  testMarketDataAssetID("EUR"),
		QuoteAssetID: testMarketDataAssetID("USD"),
	}}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	got := status.Instances[0].Instruments[0]
	if !got.SyntheticInverse || got.InverseQuote == nil {
		t.Fatalf("synthetic inverse status = %+v", got)
	}
	if got.InverseQuote.BaseAsset != "USD" || got.InverseQuote.QuoteAsset != "EUR" ||
		got.InverseQuote.Mark != "0.5" || got.InverseQuote.Bid != "0.125" ||
		got.InverseQuote.Ask != "0.25" {
		t.Fatalf("inverse quote = %+v", got.InverseQuote)
	}
	if status.RestartRequired {
		t.Fatal("RestartRequired = true for matching synthetic plan")
	}
}

func TestService_ListMarketDataSuppressesInverseForConfiguredDisabledReverse(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{
		statuses: map[string]marketdata.InstanceRuntimeStatus{
			mdID("mock-1").String(): {State: marketdata.StateOK},
		},
		applied: map[string]marketdata.AppliedInstanceConfig{
			mdID("mock-1").String(): {
				Provider: domain.MarketDataProviderMock,
				Subscriptions: []marketdata.Subscription{{
					External: "EURUSD", Base: testMarketDataAssetID("EUR"), Quote: testMarketDataAssetID("USD"),
				}},
			},
		},
	}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	now := time.Now().UTC()
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
		{ExternalID: mdID("dead-reverse"), Provider: domain.MarketDataProviderMock, Enabled: false},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		mdID("mock-1").String(): {{
			Instance: mdID("mock-1"), ExternalSymbol: "EURUSD",
			BaseAsset: "EUR", QuoteAsset: "USD", Enabled: true,
		}},
		mdID("dead-reverse").String(): {{
			Instance: mdID("dead-reverse"), ExternalSymbol: "USD/EUR",
			BaseAsset: "USD", QuoteAsset: "EUR", Enabled: false,
		}},
	}
	md.snapshots = []marketdata.QuoteSnapshot{{
		MarketDataQuote: domain.MarketDataQuote{
			Instance: mdID("mock-1"), ExternalSymbol: "EURUSD",
			BaseAsset: "EUR", QuoteAsset: "USD", Mark: "2", AsOf: now, ReceivedAt: now,
		},
		BaseAssetID:  testMarketDataAssetID("EUR"),
		QuoteAssetID: testMarketDataAssetID("USD"),
	}}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	got := status.Instances[0].Instruments[0]
	if got.SyntheticInverse || got.InverseQuote != nil {
		t.Fatalf("suppressed synthetic inverse status = %+v", got)
	}
	if status.RestartRequired {
		t.Fatal("RestartRequired = true for matching suppressed plan")
	}
}

func TestService_ListMarketDataFlagsStaleQuote(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	md := &fakeMarketDataRuntime{snapshots: []marketdata.QuoteSnapshot{
		{
			MarketDataQuote: domain.MarketDataQuote{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "AAPL",
				Mark:           "298.01",
				AsOf:           now.Add(-backend.MarketDataFreshnessTTL - time.Second),
				ReceivedAt:     now,
			},
			BaseAssetID:  testMarketDataAssetID("AAPL"),
			QuoteAssetID: testMarketDataAssetID("USD"),
		},
	}}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		mdID("mock-1").String(): {
			{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "AAPL",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
	}
	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	got := status.Instances[0].Instruments[0]
	if !got.Stale {
		t.Fatalf("instrument should be stale: %+v", got)
	}
	// Quote is kept even when stale so the last known price remains visible.
	if got.Quote == nil {
		t.Fatalf("stale quote must still be present: %+v", got)
	}
	if got.Quote.Mark != "298.01" {
		t.Fatalf("stale quote has unexpected mark: %+v", got.Quote)
	}
}

func TestService_ListMarketDataDetectsRestartRequired(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{
		statuses: map[string]marketdata.InstanceRuntimeStatus{
			mdID("mock-1").String(): {State: marketdata.StateOK},
		},
		applied: map[string]marketdata.AppliedInstanceConfig{
			mdID("mock-1").String(): {
				Provider: domain.MarketDataProviderMock,
				Subscriptions: []marketdata.Subscription{
					{
						External: "AAPL", Base: testMarketDataAssetID("AAPL"), Quote: testMarketDataAssetID("USD"),
						SyntheticInverse: true,
					},
				},
			},
		},
	}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		mdID("mock-1").String(): {
			{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "AAPL",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
			{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "MSFT",
				BaseAsset:      "MSFT",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	if !status.RestartRequired {
		t.Fatal("RestartRequired = false, want true for unapplied instrument")
	}
}

func TestService_ListMarketDataSurfacesUpdateInterval(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{
		statuses: map[string]marketdata.InstanceRuntimeStatus{
			mdID("mock-1").String(): {State: marketdata.StateOK},
		},
		intervals: map[string]time.Duration{
			mdID("mock-1").String() + "\x00AAPL": 12 * time.Second,
		},
	}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		mdID("mock-1").String(): {
			{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "AAPL",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
			{
				Instance:       mdID("mock-1"),
				ExternalSymbol: "MSFT",
				BaseAsset:      "MSFT",
				QuoteAsset:     "USD",
				Enabled:        true,
			},
		},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	instruments := status.Instances[0].Instruments
	withInterval := instruments[0]
	if withInterval.UpdateInterval == nil {
		t.Fatalf("AAPL interval should be known: %+v", withInterval)
	}
	if *withInterval.UpdateInterval != 12*time.Second {
		t.Fatalf("AAPL interval = %v, want 12s", *withInterval.UpdateInterval)
	}
	withoutInterval := instruments[1]
	if withoutInterval.UpdateInterval != nil {
		t.Fatalf("MSFT interval should be unknown: %+v", withoutInterval)
	}
}

func TestService_CreateMarketDataInstanceGeneratesExternalIDAndDefaultLabel(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	// With no supplied id the store mints one and returns it.
	created, err := svc.CreateMarketDataInstance(context.Background(), domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBinance,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if len(fn.mdInstances) != 1 {
		t.Fatalf("created instances = %d, want 1", len(fn.mdInstances))
	}
	got := fn.mdInstances[0]
	if got.ExternalID.IsZero() {
		t.Fatalf("instance external id is zero, want a generated id")
	}
	if created.ExternalID != got.ExternalID {
		t.Fatalf("returned external id = %q, want stored %q", created.ExternalID, got.ExternalID)
	}
	if got.Provider != domain.MarketDataProviderBinance || got.Label != "Binance" || !got.Enabled {
		t.Fatalf("created instance = %+v, want Binance default label and enabled", got)
	}
}

func TestService_CreateMarketDataInstanceHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	supplied := mdID("operator-supplied")
	created, err := svc.CreateMarketDataInstance(context.Background(), domain.MarketDataInstance{
		ExternalID: supplied,
		Provider:   domain.MarketDataProviderBinance,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if len(fn.mdInstances) != 1 {
		t.Fatalf("created instances = %d, want 1", len(fn.mdInstances))
	}
	if fn.mdInstances[0].ExternalID != supplied {
		t.Fatalf("stored external id = %q, want supplied %q", fn.mdInstances[0].ExternalID, supplied)
	}
	if created.ExternalID != supplied {
		t.Fatalf("returned external id = %q, want supplied %q", created.ExternalID, supplied)
	}
}

func TestService_CreateMarketDataInstancePassesCredentials(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	_, err := svc.CreateMarketDataInstance(context.Background(), domain.MarketDataInstance{
		Provider: domain.MarketDataProviderAlpaca,
		Credentials: `{
			"apiKey": "key",
			"apiSecret": "secret"
		}`,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if len(fn.mdInstances) != 1 {
		t.Fatalf("created instances = %d, want 1", len(fn.mdInstances))
	}
	if fn.mdInstances[0].Credentials == "" {
		t.Fatal("Credentials not persisted")
	}
}

func TestService_CreateMarketDataInstanceRejectsInvalidCredentials(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	_, err := svc.CreateMarketDataInstance(context.Background(), domain.MarketDataInstance{
		Provider: domain.MarketDataProviderAlpaca,
		Credentials: `{
			"apiKey": "key"
		}`,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateMarketDataInstance error = %v, want ErrInvalid", err)
	}
	if len(fn.mdInstances) != 0 {
		t.Fatalf("invalid create changed instances: %+v", fn.mdInstances)
	}
}

func TestService_CreateMarketDataInstanceRejectsDuplicateLabel(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("bn-1"), Provider: domain.MarketDataProviderBinance, Label: "Binance"},
	}

	_, err := svc.CreateMarketDataInstance(context.Background(), domain.MarketDataInstance{
		Provider: domain.MarketDataProviderMock,
		Label:    " binance ",
	})
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateMarketDataInstance error = %v, want ErrAlreadyExists", err)
	}
	if len(fn.mdInstances) != 1 {
		t.Fatalf("duplicate create changed instances: %+v", fn.mdInstances)
	}
}

func TestService_UpdateMarketDataInstanceSettingsMergesBlankSecret(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{
			ExternalID:  mdID("alpaca-1"),
			Provider:    domain.MarketDataProviderAlpaca,
			Label:       "Alpaca",
			Credentials: `{"apiKey":"old-key","apiSecret":"old-secret"}`,
		},
	}

	err := svc.UpdateMarketDataInstanceSettings(
		context.Background(),
		mdID("alpaca-1").String(),
		"Alpaca live",
		`{"apiKey":"new-key","apiSecret":""}`,
	)
	if err != nil {
		t.Fatalf("UpdateMarketDataInstanceSettings: %v", err)
	}
	got := fn.mdInstances[0]
	if got.Label != "Alpaca live" {
		t.Fatalf("Label = %q, want updated label", got.Label)
	}
	var credentials map[string]string
	if err := json.Unmarshal([]byte(got.Credentials), &credentials); err != nil {
		t.Fatalf("credentials JSON: %v", err)
	}
	if credentials["apiKey"] != "new-key" || credentials["apiSecret"] != "old-secret" {
		t.Fatalf("credentials = %+v, want new key and preserved secret", credentials)
	}
}

func TestService_UpdateMarketDataInstanceSettingsRejectsDuplicateLabel(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("a"), Provider: domain.MarketDataProviderBinance, Label: "Primary"},
		{ExternalID: mdID("b"), Provider: domain.MarketDataProviderBinance, Label: "Backup"},
	}

	err := svc.UpdateMarketDataInstanceSettings(
		context.Background(), mdID("b").String(), "primary", "",
	)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("UpdateMarketDataInstanceSettings error = %v, want ErrAlreadyExists", err)
	}
	if fn.mdInstances[1].Label != "Backup" {
		t.Fatalf("duplicate update changed instance: %+v", fn.mdInstances[1])
	}
}

func TestService_ListMarketDataManualPriceDoesNotRequireRestart(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{
		statuses: map[string]marketdata.InstanceRuntimeStatus{
			mdID("byo-1").String(): {State: marketdata.StateOK},
		},
		applied: map[string]marketdata.AppliedInstanceConfig{
			mdID("byo-1").String(): {
				Provider: domain.MarketDataProviderBYO,
				Subscriptions: []marketdata.Subscription{
					{
						External: "USDT/USD", Base: testMarketDataAssetID("USDT"), Quote: testMarketDataAssetID("USD"),
						SyntheticInverse: true,
					},
				},
			},
		},
	}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("byo-1"), Provider: domain.MarketDataProviderBYO, Enabled: true},
	}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		mdID("byo-1").String(): {
			{
				Instance:       mdID("byo-1"),
				ExternalSymbol: "USDT/USD",
				BaseAsset:      "USDT",
				QuoteAsset:     "USD",
				ManualPrice:    "0.9998",
				Enabled:        true,
			},
		},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	if status.RestartRequired {
		t.Fatal("RestartRequired = true, want false for manual price change")
	}
}

func TestService_ListMarketDataClearedManualHasNoSnapshot(t *testing.T) {
	t.Parallel()
	instanceID := mdID("byo-cleared")
	md := &fakeMarketDataRuntime{
		statuses: map[string]marketdata.InstanceRuntimeStatus{
			instanceID.String(): {State: marketdata.StateOK},
		},
		applied: map[string]marketdata.AppliedInstanceConfig{
			instanceID.String(): {
				Provider: domain.MarketDataProviderBYO,
				Subscriptions: []marketdata.Subscription{{
					External: "Z/USD", Base: testMarketDataAssetID("Z"), Quote: testMarketDataAssetID("USD"),
					SyntheticInverse: true,
				}},
			},
		},
	}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{{
		ExternalID: instanceID,
		Provider:   domain.MarketDataProviderBYO,
		Enabled:    true,
	}}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		instanceID.String(): {{
			Instance: instanceID, ExternalSymbol: "Z/USD",
			BaseAsset: "Z", QuoteAsset: "USD", ManualPrice: "", Enabled: true,
		}},
	}
	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	if status.RestartRequired {
		t.Fatal("RestartRequired = true for same-mapping manual clear")
	}
	got := status.Instances[0].Instruments[0]
	if got.Quote != nil || got.InverseQuote != nil || !got.Stale {
		t.Fatalf("cleared manual status = %+v, want no snapshot and stale", got)
	}
}

func TestService_ListMarketDataSurfacesVerifyCapability(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
		{ExternalID: mdID("bn-1"), Provider: domain.MarketDataProviderBinance, Enabled: false},
		{ExternalID: mdID("ib-1"), Provider: domain.MarketDataProviderIB, Enabled: false},
		{ExternalID: mdID("kraken-1"), Provider: domain.MarketDataProviderKraken, Enabled: false},
		{ExternalID: mdID("coinbase-1"), Provider: domain.MarketDataProviderCoinbase, Enabled: false},
		{ExternalID: mdID("okx-1"), Provider: domain.MarketDataProviderOKX, Enabled: false},
		{ExternalID: mdID("bybit-1"), Provider: domain.MarketDataProviderBybit, Enabled: false},
	}

	status, err := svc.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	byID := make(map[string]backend.MarketDataInstanceStatus, len(status.Instances))
	for _, instance := range status.Instances {
		byID[instance.Instance.ExternalID.String()] = instance
	}
	if byID[mdID("mock-1").String()].VerifiesSymbols {
		t.Fatalf("mock instance VerifiesSymbols = true, want false")
	}
	if byID[mdID("ib-1").String()].VerifiesSymbols {
		t.Fatalf("ib instance VerifiesSymbols = true, want false")
	}
	// Binance is verify-capable even though the instance is disabled: the flag is
	// provider-derived, not runtime-derived.
	if !byID[mdID("bn-1").String()].VerifiesSymbols {
		t.Fatalf("binance instance VerifiesSymbols = false, want true")
	}
	for _, label := range []string{"kraken-1", "coinbase-1", "okx-1", "bybit-1"} {
		if !byID[mdID(label).String()].VerifiesSymbols {
			t.Fatalf("%s VerifiesSymbols = false, want true", label)
		}
	}
}

func TestService_VerifyMarketDataSymbolUnsupported(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
	}

	got, err := svc.VerifyMarketDataSymbol(context.Background(), mdID("mock-1").String(), "AAPL")
	if err != nil {
		t.Fatalf("VerifyMarketDataSymbol: %v", err)
	}
	if got.Supported || got.Exists || got.Suggestion != "" {
		t.Fatalf("verification = %+v, want unsupported zero result", got)
	}
}

func TestService_VerifyMarketDataSymbolUnknownInstance(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService()

	_, err := svc.VerifyMarketDataSymbol(context.Background(), mdID("missing").String(), "AAPL")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("VerifyMarketDataSymbol(missing) err = %v, want ErrNotFound", err)
	}
}

func TestService_SearchMarketDataSymbolsUnsupported(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.mdInstances = []domain.MarketDataInstance{
		{ExternalID: mdID("mock-1"), Provider: domain.MarketDataProviderMock, Enabled: true},
	}

	got, err := svc.SearchMarketDataSymbols(
		context.Background(), mdID("mock-1").String(),
		backend.MarketDataSymbolSearchInput{Query: "AAPL"},
	)
	if err != nil {
		t.Fatalf("SearchMarketDataSymbols: %v", err)
	}
	if got.Supported || len(got.Matches) != 0 {
		t.Fatalf("search = %+v, want unsupported empty result", got)
	}
}

func TestService_SearchMarketDataSymbolsNotFound(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService()

	_, err := svc.SearchMarketDataSymbols(
		context.Background(), mdID("missing").String(),
		backend.MarketDataSymbolSearchInput{Query: "AAPL"},
	)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SearchMarketDataSymbols(missing) err = %v, want ErrNotFound", err)
	}
}

func sampleAdjustmentRequest() domain.AdjustmentRequest {
	return domain.AdjustmentRequest{
		Asset: "USD",
		Balance: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeDelta, Value: "100",
		},
	}
}

// TestService_ApplyAdjustmentHonorsSuppliedExternalID covers the user-create
// adjustment path: a caller-supplied external id is threaded onto the record and
// returned verbatim.
func TestService_ApplyAdjustmentHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	supplied := mdID("supplied-adj-id")
	rec, err := svc.ApplyAdjustment(
		context.Background(), "acc-1", supplied, sampleAdjustmentRequest(), domain.MissingAccountCreate)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if len(fn.adjustmentExternalIDs) != 1 || fn.adjustmentExternalIDs[0] != supplied {
		t.Fatalf("threaded ids = %+v, want [%q]", fn.adjustmentExternalIDs, supplied)
	}
	if rec.ExternalID != supplied {
		t.Fatalf("returned record id = %q, want supplied %q", rec.ExternalID, supplied)
	}
}

// TestService_ApplyAdjustmentGeneratesExternalIDWhenAbsent covers the absent-id
// path: a zero id is forwarded so the store mints one.
func TestService_ApplyAdjustmentGeneratesExternalIDWhenAbsent(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()

	rec, err := svc.ApplyAdjustment(
		context.Background(), "acc-1", domain.ExternalID(""), sampleAdjustmentRequest(), domain.MissingAccountCreate)
	if err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	if len(fn.adjustmentExternalIDs) != 1 || !fn.adjustmentExternalIDs[0].IsZero() {
		t.Fatalf("threaded ids = %+v, want one zero id", fn.adjustmentExternalIDs)
	}
	if rec.ExternalID.IsZero() {
		t.Fatalf("returned record id is zero, want a generated id")
	}
}

// TestService_ApplyAdjustmentDuplicateSuppliedIDConflicts covers the conflict
// propagation: a duplicate supplied id surfaces domain.ErrAlreadyExists
// unchanged from the store.
func TestService_ApplyAdjustmentDuplicateSuppliedIDConflicts(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.adjustmentErr = fmt.Errorf("append adjustment: %w", domain.ErrAlreadyExists)

	_, err := svc.ApplyAdjustment(
		context.Background(), "acc-1", mdID("dup-adj-id"), sampleAdjustmentRequest(), domain.MissingAccountCreate)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate supplied id error = %v, want ErrAlreadyExists", err)
	}
}

// mdID derives a deterministic, distinct external id from a short label so a
// test can address a market-data instance by a stable handle. Market-data
// instances are dictionary rows addressed by their opaque external id.
