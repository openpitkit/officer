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
	"errors"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
)

type nonClearingRestoreSink struct {
	updates []marketdata.QuoteUpdate
}

func (s *nonClearingRestoreSink) Push(update marketdata.QuoteUpdate) error {
	s.updates = append(s.updates, update)
	return nil
}

func restoreMarketDataArchive(
	instance domain.MarketDataInstance,
	instruments []domain.MarketDataInstrument,
	quotes []domain.MarketDataQuote,
) backup.Archive {
	assets := make(map[string]domain.Asset)
	for _, instrument := range instruments {
		assets[instrument.BaseAsset] = domain.Asset{Code: instrument.BaseAsset}
		assets[instrument.QuoteAsset] = domain.Asset{Code: instrument.QuoteAsset}
	}
	assetRows := make([]domain.Asset, 0, len(assets))
	for _, asset := range assets {
		assetRows = append(assetRows, asset)
	}
	return testArchive(backup.Scope{All: true}, backup.Data{
		Assets:                assetRows,
		MarketDataInstances:   []domain.MarketDataInstance{instance},
		MarketDataInstruments: instruments,
		MarketDataQuotes:      quotes,
	})
}

func TestRestoreMarketDataFreshToStaleClearsOnline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sink := &marketDataReplaySink{}
	eng := newFakeEngine()
	eng.sink = sink
	n, realm := newTestNode(t, eng)
	instance := seedReplayInstanceForProvider(t, realm, domain.MarketDataProviderMock)
	instrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "",
	)
	fresh := domain.MarketDataQuote{
		AsOf: time.Now().UTC(), ReceivedAt: time.Now().UTC(),
		Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
		BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset, Mark: "2",
	}
	if err := realm.UpsertMarketDataQuote(ctx, fresh); err != nil {
		t.Fatalf("UpsertMarketDataQuote: %v", err)
	}
	stale := fresh
	stale.AsOf = time.Now().UTC().Add(-marketdata.FreshnessTTL - time.Second)
	stale.ReceivedAt = stale.AsOf
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}

	summary, returnedSink, err := n.RestoreBackup(
		ctx,
		restoreMarketDataArchive(instance, []domain.MarketDataInstrument{instrument},
			[]domain.MarketDataQuote{stale}),
		backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite},
		testCaller,
	)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired || builds != 0 || n.currentEngine() != eng || returnedSink != sink {
		t.Fatalf("restore rebuilt engine: summary=%+v builds=%d sink=%T", summary, builds, returnedSink)
	}
	wantCleared := []marketDataReplayPairKey{
		{base: "EUR", quote: "USD"},
		{base: "USD", quote: "EUR"},
	}
	if !equalReplayPairs(sink.cleared, wantCleared) {
		t.Fatalf("cleared pairs = %+v, want %+v", sink.cleared, wantCleared)
	}
	if len(sink.updates) != 0 {
		t.Fatalf("stale quote replayed after clear: %+v", sink.updates)
	}
}

func TestRestoreMarketDataQuoteRemovalClearsOnline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sink := &marketDataReplaySink{}
	eng := newFakeEngine()
	eng.sink = sink
	n, realm := newTestNode(t, eng)
	instance := seedReplayInstanceForProvider(t, realm, domain.MarketDataProviderMock)
	instrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "",
	)
	if err := realm.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
		AsOf: time.Now().UTC(), ReceivedAt: time.Now().UTC(),
		Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
		BaseAsset: "EUR", QuoteAsset: "USD", Mark: "2",
	}); err != nil {
		t.Fatalf("UpsertMarketDataQuote: %v", err)
	}
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}

	summary, _, err := n.RestoreBackup(
		ctx,
		restoreMarketDataArchive(instance, []domain.MarketDataInstrument{instrument}, nil),
		backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll},
		testCaller,
	)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired || builds != 0 || n.currentEngine() != eng {
		t.Fatalf("quote removal rebuilt: summary=%+v builds=%d", summary, builds)
	}
	if len(sink.cleared) != 2 || len(sink.updates) != 0 {
		t.Fatalf("quote removal clear/replay = cleared %+v updates %+v", sink.cleared, sink.updates)
	}
}

func TestRestoreMarketDataExplicitReverseSuppressesOldSyntheticOnline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sink := &marketDataReplaySink{}
	eng := newFakeEngine()
	eng.sink = sink
	n, realm := newTestNode(t, eng)
	instance := seedReplayInstance(t, realm)
	forward := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "2",
	)
	reverse := domain.MarketDataInstrument{
		Instance: instance.ExternalID, ExternalSymbol: "USDEUR",
		BaseAsset: "USD", QuoteAsset: "EUR", Enabled: true,
	}
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}

	summary, _, err := n.RestoreBackup(
		ctx,
		restoreMarketDataArchive(instance,
			[]domain.MarketDataInstrument{forward, reverse}, nil),
		backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite},
		testCaller,
	)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired || builds != 0 || n.currentEngine() != eng {
		t.Fatalf("topology restore rebuilt: summary=%+v builds=%d", summary, builds)
	}
	if len(sink.cleared) != 2 {
		t.Fatalf("topology clear pairs = %+v, want both directions", sink.cleared)
	}
	if len(sink.updates) != 1 || sink.updates[0].Base != "EUR" ||
		sink.updates[0].Quote != "USD" || sink.updates[0].Mark != "2" {
		t.Fatalf("topology replay = %+v, want explicit forward only", sink.updates)
	}
}

func TestRestoreMarketDataClearFailureKeepsCommittedStoreAndFatals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clearErr := errors.New("clear failed")
	sink := &marketDataReplaySink{clearErr: clearErr}
	eng := newFakeEngine()
	eng.sink = sink
	n, realm := newTestNode(t, eng)
	instance := seedReplayInstanceForProvider(t, realm, domain.MarketDataProviderMock)
	instrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "",
	)
	fresh := domain.MarketDataQuote{
		AsOf: time.Now().UTC(), ReceivedAt: time.Now().UTC(),
		Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
		BaseAsset: "EUR", QuoteAsset: "USD", Mark: "2",
	}
	if err := realm.UpsertMarketDataQuote(ctx, fresh); err != nil {
		t.Fatalf("UpsertMarketDataQuote: %v", err)
	}
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }
	stale := fresh
	stale.AsOf = time.Now().UTC().Add(-marketdata.FreshnessTTL - time.Second)

	_, _, err := n.RestoreBackup(
		ctx,
		restoreMarketDataArchive(instance, []domain.MarketDataInstrument{instrument},
			[]domain.MarketDataQuote{stale}),
		backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite},
		testCaller,
	)
	if !errors.Is(err, clearErr) {
		t.Fatalf("RestoreBackup error = %v, want clear failure", err)
	}
	if fatalErr == nil || !errors.Is(fatalErr, clearErr) {
		t.Fatalf("fatal error = %v, want clear failure", fatalErr)
	}
	if n.currentEngine() != eng || !eng.running || builds != 0 {
		t.Fatalf(
			"engine state after clear failure: current=%p running=%v builds=%d",
			n.currentEngine(), eng.running, builds,
		)
	}
	quotes, listErr := realm.ListMarketDataQuotes(ctx, instance.ExternalID)
	if listErr != nil || len(quotes) != 1 || !quotes[0].AsOf.Equal(stale.AsOf) {
		t.Fatalf(
			"quotes after committed restore = %+v err=%v, want restored stale quote",
			quotes, listErr,
		)
	}
}

func TestRestoreMarketDataWithoutClearCapabilityFallsBackToRebuild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := newFakeEngine()
	old.sink = &nonClearingRestoreSink{}
	n, realm := newTestNode(t, old)
	instance := seedReplayInstanceForProvider(t, realm, domain.MarketDataProviderMock)
	instrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "",
	)
	next := newFakeEngine()
	builds := 0
	n.build = func(snapshot engine.Snapshot) (engine.Engine, error) {
		builds++
		return fakeBuild(next, new(engine.Snapshot))(snapshot)
	}

	changed := instrument
	changed.ManualPrice = "2"
	summary, sink, err := n.RestoreBackup(
		ctx,
		restoreMarketDataArchive(instance, []domain.MarketDataInstrument{changed}, nil),
		backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite},
		testCaller,
	)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if !summary.RestartRequired || builds != 1 || n.currentEngine() != next ||
		sink != next.MarketDataSink() {
		t.Fatalf("fallback rebuild: summary=%+v builds=%d current=%p sink=%T",
			summary, builds, n.currentEngine(), sink)
	}
}

func equalReplayPairs(left, right []marketDataReplayPairKey) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
