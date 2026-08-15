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

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

type marketDataReplaySink struct {
	updates    []marketdata.QuoteUpdate
	cleared    []marketDataReplayPairKey
	err        error
	clearErr   error
	failMark   string
	beforePush func(marketdata.QuoteUpdate)
}

func (s *marketDataReplaySink) Push(update marketdata.QuoteUpdate) error {
	if before := s.beforePush; before != nil {
		s.beforePush = nil
		before(update)
	}
	if s.err != nil && (s.failMark == "" || s.failMark == update.Mark) {
		return s.err
	}
	s.updates = append(s.updates, update)
	return nil
}

func (s *marketDataReplaySink) Clear(
	base, quote domain.EngineAssetID,
) error {
	if s.clearErr != nil {
		return s.clearErr
	}
	s.cleared = append(s.cleared, marketDataReplayPairKey{base: base, quote: quote})
	return nil
}

func assertStaleLastKnownReplay(
	t *testing.T,
	updates []marketdata.QuoteUpdate,
	asOf time.Time,
	base, quote domain.EngineAssetID,
) {
	t.Helper()
	if len(updates) != 2 ||
		updates[0].Base != base ||
		updates[0].Quote != quote ||
		updates[0].Mark != "2" || !updates[0].AsOf.Equal(asOf) ||
		updates[1].Base != quote ||
		updates[1].Quote != base ||
		updates[1].Mark != "0.5" || !updates[1].AsOf.Equal(asOf) {
		t.Fatalf(
			"replayed updates = %+v, want stale direct and inverse last-known quotes at %v",
			updates, asOf,
		)
	}
}

func seedReplayInstrument(
	t *testing.T,
	realm store.RealmStore,
	instance domain.ExternalID,
	external, base, quote, manualPrice string,
) domain.MarketDataInstrument {
	t.Helper()
	ctx := context.Background()
	for _, code := range []string{base, quote} {
		if _, err := realm.CreateAsset(ctx, domain.Asset{Code: code}); err != nil &&
			!errors.Is(err, domain.ErrAlreadyExists) {
			t.Fatalf("CreateAsset(%s): %v", code, err)
		}
	}
	instrument := domain.MarketDataInstrument{
		Instance:       instance,
		ExternalSymbol: external,
		BaseAsset:      base,
		QuoteAsset:     quote,
		ManualPrice:    manualPrice,
		Enabled:        true,
	}
	if err := realm.UpsertMarketDataInstrument(ctx, instrument); err != nil {
		t.Fatalf("UpsertMarketDataInstrument(%s): %v", external, err)
	}
	instruments, err := realm.ListMarketDataInstruments(ctx, instance)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments(%s): %v", instance, err)
	}
	for _, stored := range instruments {
		if stored.ExternalSymbol == external {
			return stored
		}
	}
	t.Fatalf("ListMarketDataInstruments(%s) omitted %q", instance, external)
	return domain.MarketDataInstrument{}
}

func seedReplayInstance(
	t *testing.T, realm store.RealmStore,
) domain.MarketDataInstance {
	return seedReplayInstanceForProvider(t, realm, domain.MarketDataProviderBYO)
}

func seedReplayInstanceForProvider(
	t *testing.T, realm store.RealmStore, provider string,
) domain.MarketDataInstance {
	t.Helper()
	instance, err := realm.CreateMarketDataInstance(
		context.Background(),
		domain.MarketDataInstance{
			Provider: provider,
			Label:    "Static FX",
			Enabled:  true,
		},
	)
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	return instance
}

func TestDeleteAccountReplaysPersistedFXAndSyntheticInverse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := newFakeEngine()
	n, realm := newTestNode(t, old)
	instance := seedReplayInstance(t, realm)
	instrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "Z\\USD", "Z", "USD", "2",
	)
	asOf := time.Date(2026, time.July, 18, 17, 10, 0, 0, time.UTC)
	if err := realm.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
		AsOf:           asOf,
		ReceivedAt:     asOf.Add(time.Second),
		Instance:       instance.ExternalID,
		ExternalSymbol: instrument.ExternalSymbol,
		BaseAsset:      instrument.BaseAsset,
		QuoteAsset:     instrument.QuoteAsset,
		Mark:           "2",
	}); err != nil {
		t.Fatalf("UpsertMarketDataQuote: %v", err)
	}
	if _, err := realm.CreateAccount(ctx, domain.Account{Code: "deleted"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	sink := &marketDataReplaySink{}
	next := newFakeEngine()
	next.sink = sink
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)

	if err := n.DeleteAccount(ctx, testKey("deleted"), true, testCaller); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if n.currentEngine() != next || !next.running || old.running {
		t.Fatalf("engine swap state: current=%p next running=%v old running=%v",
			n.currentEngine(), next.running, old.running)
	}
	want := []marketdata.QuoteUpdate{
		{Base: instrument.BaseAssetID, Quote: instrument.QuoteAssetID, Mark: "2"},
		{Base: instrument.QuoteAssetID, Quote: instrument.BaseAssetID, Mark: "0.5"},
	}
	if len(sink.updates) != len(want) {
		t.Fatalf("replayed updates = %+v, want %+v", sink.updates, want)
	}
	for i := range want {
		if sink.updates[i] != want[i] {
			t.Fatalf("replayed update %d = %+v, want %+v", i, sink.updates[i], want[i])
		}
	}
	if _, ok, err := realm.GetAccount(ctx, "deleted"); err != nil || ok {
		t.Fatalf("deleted account after rebuild: ok=%v err=%v, want absent", ok, err)
	}
	quotes, err := realm.ListMarketDataQuotes(ctx, instance.ExternalID)
	if err != nil || len(quotes) != 1 || quotes[0].Mark != "2" ||
		quotes[0].AsOf != asOf {
		t.Fatalf("persisted static FX after delete = %+v, err=%v; want original quote", quotes, err)
	}
	if len(snapshot.Accounts) != 0 {
		t.Fatalf("prepared delete snapshot = %+v, want account filtered", snapshot)
	}
}

func TestRebuildReplaysManualPriceWithoutPersistedQuote(t *testing.T) {
	t.Parallel()
	old := newFakeEngine()
	n, realm := newTestNode(t, old)
	instance := seedReplayInstance(t, realm)
	instrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "4",
	)

	sink := &marketDataReplaySink{}
	next := newFakeEngine()
	next.sink = sink
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)
	if err := n.rebuildEngineFromStore(context.Background()); err != nil {
		t.Fatalf("rebuildEngineFromStore: %v", err)
	}
	want := []marketdata.QuoteUpdate{
		{Base: instrument.BaseAssetID, Quote: instrument.QuoteAssetID, Mark: "4"},
		{Base: instrument.QuoteAssetID, Quote: instrument.BaseAssetID, Mark: "0.25"},
	}
	if len(sink.updates) != len(want) {
		t.Fatalf("replayed updates = %+v, want %+v", sink.updates, want)
	}
	for i := range want {
		if sink.updates[i] != want[i] {
			t.Fatalf("replayed update %d = %+v, want %+v", i, sink.updates[i], want[i])
		}
	}
}

func TestRebuildReplaysConfiguredBYOMarkInsteadOfPersistedSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := newFakeEngine()
	n, realm := newTestNode(t, old)
	instance := seedReplayInstance(t, realm)
	instrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "3",
	)
	if err := realm.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
		AsOf: time.Now().UTC().Add(-time.Hour), ReceivedAt: time.Now().UTC(),
		Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
		BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset, Mark: "2",
	}); err != nil {
		t.Fatalf("UpsertMarketDataQuote: %v", err)
	}

	sink := &marketDataReplaySink{}
	next := newFakeEngine()
	next.sink = sink
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		t.Fatalf("rebuildEngineFromStore: %v", err)
	}
	want := []marketdata.QuoteUpdate{
		{Base: instrument.BaseAssetID, Quote: instrument.QuoteAssetID, Mark: "3"},
		{Base: instrument.QuoteAssetID, Quote: instrument.BaseAssetID, Mark: "0.3333333333333333"},
	}
	if len(sink.updates) != len(want) {
		t.Fatalf("replayed updates = %+v, want %+v", sink.updates, want)
	}
	for i := range want {
		if sink.updates[i] != want[i] {
			t.Fatalf("replayed update %d = %+v, want %+v", i, sink.updates[i], want[i])
		}
	}
}

func TestRebuildDoesNotReplayClearedBYOMark(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := newFakeEngine()
	n, realm := newTestNode(t, old)
	instance := seedReplayInstance(t, realm)
	instrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "",
	)
	if err := realm.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
		AsOf: time.Now().UTC(), ReceivedAt: time.Now().UTC(),
		Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
		BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset, Mark: "2",
	}); err != nil {
		t.Fatalf("UpsertMarketDataQuote: %v", err)
	}

	sink := &marketDataReplaySink{}
	next := newFakeEngine()
	next.sink = sink
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		t.Fatalf("rebuildEngineFromStore: %v", err)
	}
	if len(sink.updates) != 0 {
		t.Fatalf("replayed updates = %+v, want cleared BYO mark omitted", sink.updates)
	}
}

func TestRebuildReplaysStaleStreamingSnapshotAsLastKnown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := newFakeEngine()
	n, realm := newTestNode(t, old)
	instance := seedReplayInstanceForProvider(
		t, realm, domain.MarketDataProviderMock,
	)
	instrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "",
	)
	staleAt := time.Now().UTC().Add(-marketdata.FreshnessTTL - time.Second)
	if err := realm.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
		AsOf: staleAt, ReceivedAt: time.Now().UTC(),
		Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
		BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset, Mark: "2",
	}); err != nil {
		t.Fatalf("UpsertMarketDataQuote: %v", err)
	}

	sink := &marketDataReplaySink{}
	next := newFakeEngine()
	next.sink = sink
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		t.Fatalf("rebuildEngineFromStore: %v", err)
	}
	assertStaleLastKnownReplay(
		t, sink.updates, staleAt, instrument.BaseAssetID, instrument.QuoteAssetID,
	)
}

func TestRebuildReplaysFreshStreamingSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := newFakeEngine()
	n, realm := newTestNode(t, old)
	instance := seedReplayInstanceForProvider(
		t, realm, domain.MarketDataProviderMock,
	)
	instrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "",
	)
	freshAt := time.Now().UTC().Add(-time.Second)
	if err := realm.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
		AsOf: freshAt, ReceivedAt: time.Now().UTC(),
		Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
		BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset, Mark: "2",
	}); err != nil {
		t.Fatalf("UpsertMarketDataQuote: %v", err)
	}

	sink := &marketDataReplaySink{}
	next := newFakeEngine()
	next.sink = sink
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		t.Fatalf("rebuildEngineFromStore: %v", err)
	}
	want := []marketdata.QuoteUpdate{
		{AsOf: freshAt, Base: instrument.BaseAssetID, Quote: instrument.QuoteAssetID, Mark: "2"},
		{AsOf: freshAt, Base: instrument.QuoteAssetID, Quote: instrument.BaseAssetID, Mark: "0.5"},
	}
	if len(sink.updates) != len(want) {
		t.Fatalf("replayed updates = %+v, want %+v", sink.updates, want)
	}
	for i := range want {
		if sink.updates[i] != want[i] {
			t.Fatalf("replayed update %d = %+v, want %+v", i, sink.updates[i], want[i])
		}
	}
}

func TestRebuildDoesNotInferOverConfiguredReverseFeed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := newFakeEngine()
	n, realm := newTestNode(t, old)
	instance := seedReplayInstanceForProvider(
		t, realm, domain.MarketDataProviderMock,
	)
	forward := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "",
	)
	reverse := seedReplayInstrument(
		t, realm, instance.ExternalID, "USDEUR", "USD", "EUR", "",
	)
	asOf := time.Now().UTC().Add(-time.Second)
	for i, quote := range []domain.MarketDataQuote{
		{
			AsOf: asOf, ReceivedAt: asOf,
			Instance: instance.ExternalID, ExternalSymbol: forward.ExternalSymbol,
			BaseAsset: forward.BaseAsset, QuoteAsset: forward.QuoteAsset, Mark: "2",
		},
		{
			AsOf: asOf, ReceivedAt: asOf.Add(time.Second),
			Instance: instance.ExternalID, ExternalSymbol: reverse.ExternalSymbol,
			BaseAsset: reverse.BaseAsset, QuoteAsset: reverse.QuoteAsset, Mark: "0.4",
		},
	} {
		if err := realm.UpsertMarketDataQuote(ctx, quote); err != nil {
			t.Fatalf("UpsertMarketDataQuote(%d): %v", i, err)
		}
	}

	sink := &marketDataReplaySink{}
	next := newFakeEngine()
	next.sink = sink
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		t.Fatalf("rebuildEngineFromStore: %v", err)
	}
	if len(sink.updates) != 2 ||
		sink.updates[0].Base != forward.BaseAssetID || sink.updates[0].Mark != "2" ||
		sink.updates[1].Base != reverse.BaseAssetID || sink.updates[1].Mark != "0.4" {
		t.Fatalf("replayed updates = %+v, want only two explicit directions", sink.updates)
	}
}

func TestRebuildReplayFailureKeepsOldEngineAndStopsNewEngine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := newFakeEngine()
	n, realm := newTestNode(t, old)
	instance := seedReplayInstance(t, realm)
	instrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "2",
	)
	if err := realm.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
		AsOf:           time.Now().UTC(),
		ReceivedAt:     time.Now().UTC(),
		Instance:       instance.ExternalID,
		ExternalSymbol: instrument.ExternalSymbol,
		BaseAsset:      instrument.BaseAsset,
		QuoteAsset:     instrument.QuoteAsset,
		Mark:           "2",
	}); err != nil {
		t.Fatalf("UpsertMarketDataQuote: %v", err)
	}
	if _, err := realm.CreateAccount(ctx, domain.Account{Code: "deleted"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	replayErr := errors.New("replay failed")
	next := newFakeEngine()
	next.sink = &marketDataReplaySink{err: replayErr}
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)
	err := n.DeleteAccount(ctx, testKey("deleted"), false, testCaller)
	if !errors.Is(err, replayErr) {
		t.Fatalf("DeleteAccount error = %v, want replay failure", err)
	}
	if n.currentEngine() != old || !old.running || next.running {
		t.Fatalf("engine state after replay failure: current=%p old running=%v next running=%v",
			n.currentEngine(), old.running, next.running)
	}
	if n.CurrentMarketDataSink() != old.MarketDataSink() {
		t.Fatal("market-data sink changed after failed replay")
	}
	if _, ok, getErr := realm.GetAccount(ctx, "deleted"); getErr != nil || !ok {
		t.Fatalf("account after failed replay: ok=%v err=%v, want retained", ok, getErr)
	}
}

func TestMarketDataTransitionBuffersThenFlushesProviderUpdate(t *testing.T) {
	t.Parallel()
	oldSink := &marketDataReplaySink{}
	old := newFakeEngine()
	old.sink = oldSink
	n, _ := newTestNode(t, old)
	nextSink := &marketDataReplaySink{}
	next := newFakeEngine()
	next.sink = nextSink

	transition, err := n.beginMarketDataTransition(next)
	if err != nil {
		t.Fatalf("beginMarketDataTransition: %v", err)
	}
	update := marketdata.QuoteUpdate{Base: testMarketDataAssetID("EUR"), Quote: testMarketDataAssetID("USD"), Mark: "2"}
	if err := n.CurrentMarketDataSink().Push(update); err != nil {
		t.Fatalf("transition Push: %v", err)
	}
	if len(oldSink.updates) != 1 || oldSink.updates[0] != update ||
		len(nextSink.updates) != 0 {
		t.Fatalf("buffering updates: old=%+v next=%+v", oldSink.updates, nextSink.updates)
	}
	prev, err := n.commitMarketDataTransition(transition, next)
	if err != nil {
		t.Fatalf("commitMarketDataTransition: %v", err)
	}
	if prev != old || n.currentEngine() != next ||
		len(nextSink.updates) != 1 || nextSink.updates[0] != update {
		t.Fatalf("committed updates: prev=%p current=%p next=%+v",
			prev, n.currentEngine(), nextSink.updates)
	}
	prev.Stop()
	if n.CurrentMarketDataSink() != nextSink {
		t.Fatal("committed transition did not install the new engine sink")
	}
}

func TestRebuildBuffersManualClearAfterReplayAndCommitsIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldSink := &marketDataReplaySink{}
	old := newFakeEngine()
	old.sink = oldSink
	n, realm := newTestNode(t, old)
	instance := seedReplayInstance(t, realm)
	zInstrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "Z/USD", "Z", "USD", "",
	)
	seedReplayInstrument(
		t, realm, instance.ExternalID, "EUR/USD", "EUR", "USD", "4",
	)

	nextSink := &marketDataReplaySink{}
	next := newFakeEngine()
	next.sink = nextSink
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)
	nextSink.beforePush = func(marketdata.QuoteUpdate) {
		clearer, ok := n.CurrentMarketDataSink().(marketdata.QuoteClearer)
		if !ok {
			t.Fatal("transition sink does not expose QuoteClearer")
		}
		if err := clearer.Clear(zInstrument.BaseAssetID, zInstrument.QuoteAssetID); err != nil {
			t.Fatalf("Clear direct during transition: %v", err)
		}
		if err := clearer.Clear(zInstrument.QuoteAssetID, zInstrument.BaseAssetID); err != nil {
			t.Fatalf("Clear synthetic during transition: %v", err)
		}
	}

	if err := n.rebuildEngineFromStore(ctx); err != nil {
		t.Fatalf("rebuildEngineFromStore: %v", err)
	}
	wantCleared := []marketDataReplayPairKey{
		{base: zInstrument.BaseAssetID, quote: zInstrument.QuoteAssetID},
		{base: zInstrument.QuoteAssetID, quote: zInstrument.BaseAssetID},
	}
	if !equalReplayPairs(oldSink.cleared, wantCleared) {
		t.Fatalf("old sink clears = %+v, want %+v", oldSink.cleared, wantCleared)
	}
	if !equalReplayPairs(nextSink.cleared, wantCleared) {
		t.Fatalf("new sink clears = %+v, want %+v", nextSink.cleared, wantCleared)
	}
	if n.currentEngine() != next || !next.running || old.running {
		t.Fatalf("engine swap state: current=%p next=%v old=%v",
			n.currentEngine(), next.running, old.running)
	}
}

func TestRebuildBufferedManualClearFailureKeepsClearedOldEngine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldSink := &marketDataReplaySink{}
	old := newFakeEngine()
	old.sink = oldSink
	n, realm := newTestNode(t, old)
	instance := seedReplayInstance(t, realm)
	zInstrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "Z/USD", "Z", "USD", "",
	)
	seedReplayInstrument(
		t, realm, instance.ExternalID, "EUR/USD", "EUR", "USD", "4",
	)

	clearErr := errors.New("new sink clear failed")
	nextSink := &marketDataReplaySink{clearErr: clearErr}
	next := newFakeEngine()
	next.sink = nextSink
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)
	nextSink.beforePush = func(marketdata.QuoteUpdate) {
		clearer, ok := n.CurrentMarketDataSink().(marketdata.QuoteClearer)
		if !ok {
			t.Fatal("transition sink does not expose QuoteClearer")
		}
		if err := clearer.Clear(zInstrument.BaseAssetID, zInstrument.QuoteAssetID); err != nil {
			t.Fatalf("Clear direct during transition: %v", err)
		}
		if err := clearer.Clear(zInstrument.QuoteAssetID, zInstrument.BaseAssetID); err != nil {
			t.Fatalf("Clear synthetic during transition: %v", err)
		}
	}

	err := n.rebuildEngineFromStore(ctx)
	if !errors.Is(err, clearErr) {
		t.Fatalf("rebuildEngineFromStore error = %v, want clear failure", err)
	}
	wantCleared := []marketDataReplayPairKey{
		{base: zInstrument.BaseAssetID, quote: zInstrument.QuoteAssetID},
		{base: zInstrument.QuoteAssetID, quote: zInstrument.BaseAssetID},
	}
	if !equalReplayPairs(oldSink.cleared, wantCleared) {
		t.Fatalf("old sink clears = %+v, want %+v", oldSink.cleared, wantCleared)
	}
	if n.currentEngine() != old || !old.running || next.running {
		t.Fatalf("engine after failed clear flush: current=%p old=%v next=%v",
			n.currentEngine(), old.running, next.running)
	}
	if n.CurrentMarketDataSink() != oldSink {
		t.Fatal("failed rebuild did not restore old sink route")
	}
}

func TestRebuildFlushesNewerTickAfterOlderPersistedReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldSink := &marketDataReplaySink{}
	old := newFakeEngine()
	old.sink = oldSink
	n, realm := newTestNode(t, old)
	instance, err := realm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderMock,
		Label:    "Streaming FX",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	instrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "",
	)
	asOf := time.Now().UTC().Add(-time.Second)
	oldQuote := domain.MarketDataQuote{
		AsOf: asOf, ReceivedAt: asOf,
		Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
		BaseAsset: "EUR", QuoteAsset: "USD", Mark: "2",
	}
	if err := realm.UpsertMarketDataQuote(ctx, oldQuote); err != nil {
		t.Fatalf("UpsertMarketDataQuote(old): %v", err)
	}

	nextSink := &marketDataReplaySink{}
	next := newFakeEngine()
	next.sink = nextSink
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)
	newer := marketdata.QuoteUpdate{
		AsOf: asOf.Add(time.Second), Base: instrument.BaseAssetID, Quote: instrument.QuoteAssetID, Mark: "3",
	}
	nextSink.beforePush = func(replayed marketdata.QuoteUpdate) {
		if replayed.Mark != "2" {
			t.Fatalf("first replayed quote = %+v, want old persisted quote", replayed)
		}
		if err := realm.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
			AsOf: newer.AsOf, ReceivedAt: newer.AsOf,
			Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
			BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset, Mark: newer.Mark,
		}); err != nil {
			t.Fatalf("UpsertMarketDataQuote(newer): %v", err)
		}
		if err := n.CurrentMarketDataSink().Push(newer); err != nil {
			t.Fatalf("Push(newer): %v", err)
		}
		inverted, ok := marketdata.InvertQuote(newer)
		if !ok {
			t.Fatal("InvertQuote(newer) = false")
		}
		if err := n.CurrentMarketDataSink().Push(inverted); err != nil {
			t.Fatalf("Push(inverted newer): %v", err)
		}
	}

	if err := n.rebuildEngineFromStore(ctx); err != nil {
		t.Fatalf("rebuildEngineFromStore: %v", err)
	}
	if len(nextSink.updates) != 4 ||
		nextSink.updates[0].Mark != "2" || nextSink.updates[0].Base != instrument.BaseAssetID ||
		nextSink.updates[1].Mark != "0.5" || nextSink.updates[1].Base != instrument.QuoteAssetID ||
		nextSink.updates[2] != newer ||
		nextSink.updates[3].Base != instrument.QuoteAssetID || nextSink.updates[3].Mark == "0.5" {
		t.Fatalf("new sink updates = %+v, want old replay followed by newer tick", nextSink.updates)
	}
	if n.currentEngine() != next || !next.running || old.running {
		t.Fatalf("engine swap state: current=%p next running=%v old running=%v",
			n.currentEngine(), next.running, old.running)
	}
}

func TestRebuildBufferedFlushFailureKeepsOldEngineCurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldSink := &marketDataReplaySink{}
	old := newFakeEngine()
	old.sink = oldSink
	n, realm := newTestNode(t, old)
	instance, err := realm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderMock,
		Label:    "Streaming FX",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	instrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "",
	)
	asOf := time.Now().UTC().Add(-time.Second)
	if err := realm.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
		AsOf: asOf, ReceivedAt: asOf,
		Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
		BaseAsset: "EUR", QuoteAsset: "USD", Mark: "2",
	}); err != nil {
		t.Fatalf("UpsertMarketDataQuote(old): %v", err)
	}

	flushErr := errors.New("buffer flush failed")
	nextSink := &marketDataReplaySink{err: flushErr, failMark: "3"}
	next := newFakeEngine()
	next.sink = nextSink
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)
	newer := marketdata.QuoteUpdate{
		AsOf: asOf.Add(time.Second), Base: instrument.BaseAssetID, Quote: instrument.QuoteAssetID, Mark: "3",
	}
	nextSink.beforePush = func(marketdata.QuoteUpdate) {
		if err := realm.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
			AsOf: newer.AsOf, ReceivedAt: newer.AsOf,
			Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
			BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset, Mark: newer.Mark,
		}); err != nil {
			t.Fatalf("UpsertMarketDataQuote(newer): %v", err)
		}
		if err := n.CurrentMarketDataSink().Push(newer); err != nil {
			t.Fatalf("Push(newer): %v", err)
		}
		inverted, ok := marketdata.InvertQuote(newer)
		if !ok {
			t.Fatal("InvertQuote(newer) = false")
		}
		if err := n.CurrentMarketDataSink().Push(inverted); err != nil {
			t.Fatalf("Push(inverted newer): %v", err)
		}
	}

	err = n.rebuildEngineFromStore(ctx)
	if !errors.Is(err, flushErr) {
		t.Fatalf("rebuildEngineFromStore error = %v, want flush error", err)
	}
	if n.currentEngine() != old || !old.running || next.running {
		t.Fatalf("engine state after flush failure: current=%p old running=%v next running=%v",
			n.currentEngine(), old.running, next.running)
	}
	if n.CurrentMarketDataSink() != oldSink {
		t.Fatal("flush failure did not keep the old sink current")
	}
	if len(oldSink.updates) != 2 || oldSink.updates[0] != newer ||
		oldSink.updates[1].Base != instrument.QuoteAssetID || oldSink.updates[1].Mark == "0.5" {
		t.Fatalf("old sink updates = %+v, want newer source and inverse", oldSink.updates)
	}
	quotes, listErr := realm.ListMarketDataQuotes(ctx, instance.ExternalID)
	if listErr != nil || len(quotes) != 1 || quotes[0].Mark != "3" {
		t.Fatalf("persisted quotes = %+v, err=%v; want newer quote", quotes, listErr)
	}
}

func TestDeleteAccountBufferedFlushFailureDoesNotCommitStoreDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldSink := &marketDataReplaySink{}
	old := newFakeEngine()
	old.sink = oldSink
	n, realm := newTestNode(t, old)
	instance, err := realm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderMock,
		Label:    "Streaming FX",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	instrument := seedReplayInstrument(
		t, realm, instance.ExternalID, "EURUSD", "EUR", "USD", "",
	)
	asOf := time.Now().UTC().Add(-time.Second)
	if err := realm.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
		AsOf: asOf, ReceivedAt: asOf,
		Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
		BaseAsset: "EUR", QuoteAsset: "USD", Mark: "2",
	}); err != nil {
		t.Fatalf("UpsertMarketDataQuote(old): %v", err)
	}
	if _, err := realm.CreateAccount(ctx, domain.Account{Code: "retained"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	flushErr := errors.New("delete transition flush failed")
	nextSink := &marketDataReplaySink{err: flushErr, failMark: "3"}
	next := newFakeEngine()
	next.sink = nextSink
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)
	newer := marketdata.QuoteUpdate{
		AsOf: asOf.Add(time.Second), Base: instrument.BaseAssetID, Quote: instrument.QuoteAssetID, Mark: "3",
	}
	nextSink.beforePush = func(marketdata.QuoteUpdate) {
		if err := realm.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
			AsOf: newer.AsOf, ReceivedAt: newer.AsOf,
			Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
			BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset, Mark: newer.Mark,
		}); err != nil {
			t.Fatalf("UpsertMarketDataQuote(newer): %v", err)
		}
		if err := n.CurrentMarketDataSink().Push(newer); err != nil {
			t.Fatalf("Push(newer): %v", err)
		}
	}

	err = n.DeleteAccount(ctx, testKey("retained"), false, testCaller)
	if !errors.Is(err, flushErr) {
		t.Fatalf("DeleteAccount error = %v, want flush error", err)
	}
	if _, ok, getErr := realm.GetAccount(ctx, "retained"); getErr != nil || !ok {
		t.Fatalf("account after failed transition: ok=%v err=%v, want retained", ok, getErr)
	}
	if n.currentEngine() != old || !old.running || next.running {
		t.Fatalf("engine after failed transition: current=%p old running=%v next running=%v",
			n.currentEngine(), old.running, next.running)
	}
	quotes, listErr := realm.ListMarketDataQuotes(ctx, instance.ExternalID)
	if listErr != nil || len(quotes) != 1 || quotes[0].Mark != "3" {
		t.Fatalf("persisted quotes = %+v, err=%v; want concurrent newer quote", quotes, listErr)
	}
}
