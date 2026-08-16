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

package backend

import (
	"context"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/node"
)

type marketDataListTestNode struct {
	node.Node
	instances   []domain.MarketDataInstance
	instruments map[string][]domain.MarketDataInstrument
}

func (n *marketDataListTestNode) Owns(node.Key) bool {
	return true
}

func (n *marketDataListTestNode) ListMarketDataInstances(
	context.Context,
) ([]domain.MarketDataInstance, error) {
	return append([]domain.MarketDataInstance(nil), n.instances...), nil
}

func (n *marketDataListTestNode) ListMarketDataInstruments(
	_ context.Context, instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	return append(
		[]domain.MarketDataInstrument(nil),
		n.instruments[instance.String()]...,
	), nil
}

type marketDataListTestRuntime struct {
	MarketDataRuntime
	snapshots     []marketdata.QuoteSnapshot
	appliedConfig map[string]marketdata.AppliedInstanceConfig
}

func (r *marketDataListTestRuntime) InstanceStatuses() map[string]marketdata.InstanceRuntimeStatus {
	return nil
}

func (r *marketDataListTestRuntime) AppliedConfig() map[string]marketdata.AppliedInstanceConfig {
	return r.appliedConfig
}

func (r *marketDataListTestRuntime) QuoteUpdateInterval(
	string, string,
) (time.Duration, bool) {
	return 0, false
}

func (r *marketDataListTestRuntime) QuoteSnapshots() []marketdata.QuoteSnapshot {
	return append([]marketdata.QuoteSnapshot(nil), r.snapshots...)
}

func listMarketDataTestInstrumentStatus(
	t *testing.T,
	instance domain.MarketDataInstance,
	instrument domain.MarketDataInstrument,
	snapshots []marketdata.QuoteSnapshot,
	appliedConfig marketdata.AppliedInstanceConfig,
) MarketDataInstrumentStatus {
	t.Helper()
	n := &marketDataListTestNode{
		instances: []domain.MarketDataInstance{instance},
		instruments: map[string][]domain.MarketDataInstrument{
			instance.ExternalID.String(): {instrument},
		},
	}
	router, err := node.NewLocalRouter(n)
	if err != nil {
		t.Fatalf("NewLocalRouter: %v", err)
	}
	service := &Service{
		router: router,
		md: &marketDataListTestRuntime{
			snapshots: snapshots,
			appliedConfig: map[string]marketdata.AppliedInstanceConfig{
				instance.ExternalID.String(): appliedConfig,
			},
		},
		registry: marketdata.NewRegistry(),
	}
	status, err := service.ListMarketData(context.Background())
	if err != nil {
		t.Fatalf("ListMarketData: %v", err)
	}
	if len(status.Instances) != 1 ||
		len(status.Instances[0].Instruments) != 1 {
		t.Fatalf("ListMarketData status = %+v, want one instrument", status)
	}
	return status.Instances[0].Instruments[0]
}

func TestMarketDataInstrumentStaleMatchesEngineTTLBoundary(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	instrument := domain.MarketDataInstrument{}

	for _, test := range []struct {
		name              string
		instanceEnabled   bool
		instrumentEnabled bool
		asOf              time.Time
		stale             bool
	}{
		{
			name:              "inside freshness window",
			instanceEnabled:   true,
			instrumentEnabled: true,
			asOf:              now.Add(-MarketDataFreshnessTTL + time.Nanosecond),
		},
		{
			name:              "at ttl",
			instanceEnabled:   true,
			instrumentEnabled: true,
			asOf:              now.Add(-MarketDataFreshnessTTL),
			stale:             true,
		},
		{
			name:              "past ttl",
			instanceEnabled:   true,
			instrumentEnabled: true,
			asOf:              now.Add(-MarketDataFreshnessTTL - time.Nanosecond),
			stale:             true,
		},
		{
			name:              "instrument disabled",
			instanceEnabled:   true,
			instrumentEnabled: false,
			asOf:              now.Add(-MarketDataFreshnessTTL),
		},
		{
			name:              "instance disabled",
			instanceEnabled:   false,
			instrumentEnabled: true,
			asOf:              now.Add(-MarketDataFreshnessTTL),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			quote := &domain.MarketDataQuote{AsOf: test.asOf}
			testInstrument := instrument
			testInstrument.Enabled = test.instrumentEnabled
			if got := marketDataInstrumentStale(
				test.instanceEnabled, testInstrument, quote, now,
			); got != test.stale {
				t.Fatalf("stale = %t, want %t", got, test.stale)
			}
		})
	}
}

func TestMarketDataInverseQuoteUsesCurrentInstrumentCodesAfterRename(t *testing.T) {
	t.Parallel()
	quote := &domain.MarketDataQuote{
		BaseAsset: "AAPL.OLD", QuoteAsset: "USD", Mark: "2",
	}
	instrument := domain.MarketDataInstrument{
		BaseAsset: "AAPL.NEW", QuoteAsset: "USD",
		BaseAssetID: 41, QuoteAssetID: 42,
	}
	inverted := marketDataInverseQuote(quote, instrument, true)
	if inverted == nil || inverted.BaseAsset != "USD" ||
		inverted.QuoteAsset != "AAPL.NEW" || inverted.Mark != "0.5" {
		t.Fatalf("panel inverse quote after rename = %+v", inverted)
	}
}

// TestAssetRenameNeedsNoRestart keeps asset codes out of restart identity.
func TestAssetRenameNeedsNoRestart(t *testing.T) {
	t.Parallel()
	const instanceID = "ib-primary"
	instance := domain.MarketDataInstance{
		ExternalID: domain.ExternalID(instanceID),
		Provider:   domain.MarketDataProviderIB,
		Enabled:    true,
	}
	before := []domain.MarketDataInstrument{{
		Instance:       instance.ExternalID,
		ExternalSymbol: "EURUSD",
		BaseAsset:      "EUR.OLD",
		QuoteAsset:     "USD.OLD",
		BaseAssetID:    41,
		QuoteAssetID:   42,
		Enabled:        true,
	}}
	renamed := []domain.MarketDataInstrument{{
		Instance:       instance.ExternalID,
		ExternalSymbol: "EURUSD",
		BaseAsset:      "EUR.NEW",
		QuoteAsset:     "USD.NEW",
		BaseAssetID:    41,
		QuoteAssetID:   42,
		Enabled:        true,
	}}
	current := map[string]marketdata.AppliedInstanceConfig{
		instanceID: marketDataAppliedConfig(instance, before, nil),
	}
	applied := map[string]marketdata.AppliedInstanceConfig{
		instanceID: marketDataAppliedConfig(instance, renamed, nil),
	}
	if marketDataRestartRequired(&marketdata.Manager{}, current, applied) {
		t.Fatal("market-data restart required after asset code rename")
	}
}

func TestListMarketDataDoesNotRelabelSnapshotAfterInstrumentRemap(t *testing.T) {
	t.Parallel()
	instanceID := domain.ExternalID("instance-remap")
	instance := domain.MarketDataInstance{
		ExternalID: instanceID,
		Provider:   "mock",
		Enabled:    true,
	}
	instrument := domain.MarketDataInstrument{
		Instance:       instanceID,
		ExternalSymbol: "BTCUSDT",
		BaseAsset:      "BTC",
		QuoteAsset:     "USD",
		BaseAssetID:    domain.EngineAssetID(1),
		QuoteAssetID:   domain.EngineAssetID(3),
		Enabled:        true,
	}
	oldSnapshot := marketdata.QuoteSnapshot{
		MarketDataQuote: domain.MarketDataQuote{
			AsOf: time.Date(
				2026, time.August, 24, 12, 0, 0, 0, time.UTC,
			),
			Instance:       instanceID,
			ExternalSymbol: "BTCUSDT",
			Mark:           "2",
		},
		BaseAssetID:  domain.EngineAssetID(1),
		QuoteAssetID: domain.EngineAssetID(2),
	}
	oldConfig := marketdata.AppliedInstanceConfig{
		Provider: "mock",
		Subscriptions: []marketdata.Subscription{{
			External: "BTCUSDT",
			Base:     domain.EngineAssetID(1),
			Quote:    domain.EngineAssetID(2),
		}},
	}

	got := listMarketDataTestInstrumentStatus(
		t, instance, instrument,
		[]marketdata.QuoteSnapshot{oldSnapshot}, oldConfig,
	)
	if got.Quote != nil {
		t.Fatalf("remapped instrument quote = %+v, want none", got.Quote)
	}
}

func TestListMarketDataHidesClearedEnabledBYOMarkWithSnapshot(t *testing.T) {
	t.Parallel()
	instanceID := domain.ExternalID("instance-byo-clear-enabled")
	instance := domain.MarketDataInstance{
		ExternalID: instanceID,
		Provider:   domain.MarketDataProviderBYO,
		Enabled:    true,
	}
	instrument := domain.MarketDataInstrument{
		Instance:       instanceID,
		ExternalSymbol: "AAPLUSD",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		BaseAssetID:    domain.EngineAssetID(1),
		QuoteAssetID:   domain.EngineAssetID(2),
		ManualPrice:    "",
		Enabled:        true,
	}
	retainedSnapshot := marketdata.QuoteSnapshot{
		MarketDataQuote: domain.MarketDataQuote{
			AsOf: time.Date(
				2026, time.August, 24, 12, 0, 0, 0, time.UTC,
			),
			Instance:       instanceID,
			ExternalSymbol: instrument.ExternalSymbol,
			Mark:           "2",
		},
		BaseAssetID:  instrument.BaseAssetID,
		QuoteAssetID: instrument.QuoteAssetID,
	}
	appliedConfig := marketdata.AppliedInstanceConfig{
		Provider: domain.MarketDataProviderBYO,
		Subscriptions: []marketdata.Subscription{{
			External: instrument.ExternalSymbol,
			Base:     instrument.BaseAssetID,
			Quote:    instrument.QuoteAssetID,
		}},
	}

	got := listMarketDataTestInstrumentStatus(
		t, instance, instrument,
		[]marketdata.QuoteSnapshot{retainedSnapshot}, appliedConfig,
	)
	if got.Quote != nil {
		t.Fatalf("cleared enabled BYO quote = %+v, want none", got.Quote)
	}
}
