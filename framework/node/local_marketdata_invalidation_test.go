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
// Please see https://officer.openpit.dev and the OWNERS file for details.

package node

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
)

type lifecycleQuoteRegistrySink struct {
	mu       sync.Mutex
	quotes   map[marketDataPairKey]marketdata.QuoteUpdate
	closed   bool
	closeRun int
}

func newLifecycleQuoteRegistrySink() *lifecycleQuoteRegistrySink {
	return &lifecycleQuoteRegistrySink{
		quotes: make(map[marketDataPairKey]marketdata.QuoteUpdate),
	}
}

func (s *lifecycleQuoteRegistrySink) Push(update marketdata.QuoteUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotes[marketDataPairKey{base: update.Base, quote: update.Quote}] = update
	return nil
}

func (s *lifecycleQuoteRegistrySink) Clear(
	base, quote domain.EngineAssetID,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.quotes, marketDataPairKey{base: base, quote: quote})
	return nil
}

func (s *lifecycleQuoteRegistrySink) hasQuote(
	base, quote domain.EngineAssetID,
) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.quotes[marketDataPairKey{base: base, quote: quote}]
	return ok
}

func (s *lifecycleQuoteRegistrySink) mark(
	base, quote domain.EngineAssetID,
) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	update, ok := s.quotes[marketDataPairKey{base: base, quote: quote}]
	return update.Mark, ok
}

func (s *lifecycleQuoteRegistrySink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.closeRun++
	clear(s.quotes)
}

func (s *lifecycleQuoteRegistrySink) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *lifecycleQuoteRegistrySink) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeRun
}

type lifecycleQuoteServiceEngine struct {
	*fakeEngine
	service *lifecycleQuoteRegistrySink
}

func (e *lifecycleQuoteServiceEngine) CloseMarketDataService() {
	e.service.close()
}

type rotatingLifecycleQuoteBuilder struct {
	service *lifecycleQuoteRegistrySink
	builds  int
}

func (b *rotatingLifecycleQuoteBuilder) build(
	snapshot engine.Snapshot,
) (engine.Engine, error) {
	if b.service == nil || b.service.isClosed() {
		b.service = newLifecycleQuoteRegistrySink()
	}
	b.builds++
	next := newFakeEngine()
	next.sink = b.service
	if _, err := fakeBuild(next, new(engine.Snapshot))(snapshot); err != nil {
		return nil, err
	}
	return &lifecycleQuoteServiceEngine{
		fakeEngine: next,
		service:    b.service,
	}, nil
}

func newRotatingLifecycleQuoteNode(
	t *testing.T,
) (*localNode, *rotatingLifecycleQuoteBuilder) {
	t.Helper()
	ctx := context.Background()
	st := newMemoryStore("rotating-market-data-service.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	builder := &rotatingLifecycleQuoteBuilder{}
	nn, _, err := NewLocalNode(ctx, st, builder.build)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n)
	return n, builder
}

func seedLifecycleMarketDataInstance(
	t *testing.T, n *localNode,
) domain.MarketDataInstance {
	t.Helper()
	instance, err := n.CreateMarketDataInstance(
		context.Background(),
		domain.MarketDataInstance{
			Provider: domain.MarketDataProviderBYO,
			Label:    "lifecycle quotes",
			Enabled:  true,
		},
		testCaller,
	)
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	return instance
}

func seedLifecycleMarketDataInstrument(
	t *testing.T,
	n *localNode,
	instance domain.ExternalID,
	externalSymbol, base, quote, manualPrice string,
	enabled bool,
) domain.MarketDataInstrument {
	t.Helper()
	ctx := context.Background()
	instrument := domain.MarketDataInstrument{
		Instance:       instance,
		ExternalSymbol: externalSymbol,
		BaseAsset:      base,
		QuoteAsset:     quote,
		ManualPrice:    manualPrice,
		Enabled:        enabled,
	}
	if err := n.UpsertMarketDataInstrument(ctx, instrument, testCaller); err != nil {
		t.Fatalf("UpsertMarketDataInstrument(%s): %v", externalSymbol, err)
	}
	instruments, err := n.realm.ListMarketDataInstruments(ctx, instance)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments(%s): %v", instance, err)
	}
	for _, stored := range instruments {
		if stored.ExternalSymbol == externalSymbol {
			return stored
		}
	}
	t.Fatalf("ListMarketDataInstruments(%s) omitted %q", instance, externalSymbol)
	return domain.MarketDataInstrument{}
}

func publishLifecycleQuote(
	t *testing.T,
	sink *lifecycleQuoteRegistrySink,
	instrument domain.MarketDataInstrument,
	mark string,
) {
	t.Helper()
	update := marketdata.QuoteUpdate{
		AsOf: time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC),
		Base: instrument.BaseAssetID, Quote: instrument.QuoteAssetID, Mark: mark,
	}
	if err := sink.Push(update); err != nil {
		t.Fatalf("Push(%s): %v", instrument.ExternalSymbol, err)
	}
	inverse, ok := marketdata.InvertQuote(update)
	if !ok {
		t.Fatalf("InvertQuote(%s) = false", instrument.ExternalSymbol)
	}
	if err := sink.Push(inverse); err != nil {
		t.Fatalf("Push inverse(%s): %v", instrument.ExternalSymbol, err)
	}
}

func sharedLifecycleQuoteBuild(
	sink *lifecycleQuoteRegistrySink, builds *int,
) engine.BuildFunc {
	return func(snapshot engine.Snapshot) (engine.Engine, error) {
		*builds = *builds + 1
		next := newFakeEngine()
		next.sink = sink
		return fakeBuild(next, new(engine.Snapshot))(snapshot)
	}
}

func assertLifecycleQuoteAvailability(
	t *testing.T,
	sink *lifecycleQuoteRegistrySink,
	instrument domain.MarketDataInstrument,
	want bool,
) {
	t.Helper()
	for _, pair := range []marketDataPairKey{
		{base: instrument.BaseAssetID, quote: instrument.QuoteAssetID},
		{base: instrument.QuoteAssetID, quote: instrument.BaseAssetID},
	} {
		if got := sink.hasQuote(pair.base, pair.quote); got != want {
			t.Fatalf(
				"quote %d/%d available = %v, want %v",
				pair.base,
				pair.quote,
				got,
				want,
			)
		}
	}
}

func TestLocalNode_ResetDatabaseRotatesServiceBeforeAssetIDsRecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n, builder := newRotatingLifecycleQuoteNode(t)
	sink := builder.service
	instance := seedLifecycleMarketDataInstance(t, n)
	instrument := seedLifecycleMarketDataInstrument(
		t, n, instance.ExternalID, "EURUSD", "EUR", "USD", "", false,
	)
	publishLifecycleQuote(t, sink, instrument, "2")

	buildsBeforeReset := builder.builds
	replacementSink, err := n.ResetDatabase(ctx, testCaller)
	if err != nil {
		t.Fatalf("ResetDatabase: %v", err)
	}
	if builder.builds != buildsBeforeReset+1 {
		t.Fatalf(
			"reset builds = %d, want %d",
			builder.builds,
			buildsBeforeReset+1,
		)
	}
	if !sink.isClosed() || sink.closeCount() != 1 {
		t.Fatalf(
			"previous service closed=%v close count=%d, want true/1",
			sink.isClosed(),
			sink.closeCount(),
		)
	}
	replacement, ok := replacementSink.(*lifecycleQuoteRegistrySink)
	if !ok {
		t.Fatalf("replacement sink = %T, want lifecycle registry", replacementSink)
	}
	if replacement == sink {
		t.Fatal("reset reused the previous market-data service")
	}

	recycledBase, err := n.CreateAsset(ctx, domain.Asset{Code: "BTC"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAsset(BTC): %v", err)
	}
	recycledQuote, err := n.CreateAsset(ctx, domain.Asset{Code: "USDT"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAsset(USDT): %v", err)
	}
	recycledIDs := map[domain.EngineAssetID]struct{}{
		recycledBase.EngineAssetID:  {},
		recycledQuote.EngineAssetID: {},
	}
	if _, ok := recycledIDs[instrument.BaseAssetID]; !ok {
		t.Fatalf(
			"recycled ids = %d/%d, missing previous base id %d",
			recycledBase.EngineAssetID,
			recycledQuote.EngineAssetID,
			instrument.BaseAssetID,
		)
	}
	if _, ok := recycledIDs[instrument.QuoteAssetID]; !ok {
		t.Fatalf(
			"recycled ids = %d/%d, missing previous quote id %d",
			recycledBase.EngineAssetID,
			recycledQuote.EngineAssetID,
			instrument.QuoteAssetID,
		)
	}
	if replacement.hasQuote(recycledBase.EngineAssetID, recycledQuote.EngineAssetID) ||
		replacement.hasQuote(recycledQuote.EngineAssetID, recycledBase.EngineAssetID) {
		t.Fatal("recycled asset pair inherited the pre-reset quote")
	}
	if err := n.Close(); err != nil {
		t.Fatalf("Close after reset rotation: %v", err)
	}
	if sink.closeCount() != 1 || replacement.closeCount() != 1 {
		t.Fatalf(
			"service close counts before/after rotation = %d/%d, want 1/1",
			sink.closeCount(),
			replacement.closeCount(),
		)
	}
}

func TestLocalNode_ResetDatabaseRotatesQuoteRemovedFromConfiguration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n, builder := newRotatingLifecycleQuoteNode(t)
	sink := builder.service
	instance := seedLifecycleMarketDataInstance(t, n)
	instrument := seedLifecycleMarketDataInstrument(
		t, n, instance.ExternalID, "EURUSD", "EUR", "USD", "", true,
	)
	publishLifecycleQuote(t, sink, instrument, "2")

	if err := n.DeleteAsset(ctx, instrument.BaseAsset, true, testCaller); err != nil {
		t.Fatalf("DeleteAsset(%s): %v", instrument.BaseAsset, err)
	}
	if current := n.currentMarketDataSink(); current != sink {
		t.Fatalf("asset lifecycle sink = %T, want shared registry", current)
	}
	assertLifecycleQuoteAvailability(t, sink, instrument, true)
	instruments, err := n.realm.ListMarketDataInstruments(ctx, instance.ExternalID)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments: %v", err)
	}
	if len(instruments) != 0 {
		t.Fatalf("instruments after forced asset delete = %+v, want none", instruments)
	}

	buildsBeforeReset := builder.builds
	replacementSink, err := n.ResetDatabase(ctx, testCaller)
	if err != nil {
		t.Fatalf("ResetDatabase: %v", err)
	}
	if builder.builds != buildsBeforeReset+1 {
		t.Fatalf(
			"reset builds = %d, want %d",
			builder.builds,
			buildsBeforeReset+1,
		)
	}
	replacement, ok := replacementSink.(*lifecycleQuoteRegistrySink)
	if !ok {
		t.Fatalf("replacement sink = %T, want lifecycle registry", replacementSink)
	}
	if replacement == sink || !sink.isClosed() {
		t.Fatalf(
			"reset replacement=%p previous=%p closed=%v, want fresh service",
			replacement,
			sink,
			sink.isClosed(),
		)
	}

	recycledBase, err := n.CreateAsset(ctx, domain.Asset{Code: "BTC"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAsset(BTC): %v", err)
	}
	recycledQuote, err := n.CreateAsset(ctx, domain.Asset{Code: "USDT"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAsset(USDT): %v", err)
	}
	recycledIDs := map[domain.EngineAssetID]struct{}{
		recycledBase.EngineAssetID:  {},
		recycledQuote.EngineAssetID: {},
	}
	if _, ok := recycledIDs[instrument.BaseAssetID]; !ok {
		t.Fatalf(
			"recycled ids = %d/%d, missing previous base id %d",
			recycledBase.EngineAssetID,
			recycledQuote.EngineAssetID,
			instrument.BaseAssetID,
		)
	}
	if _, ok := recycledIDs[instrument.QuoteAssetID]; !ok {
		t.Fatalf(
			"recycled ids = %d/%d, missing previous quote id %d",
			recycledBase.EngineAssetID,
			recycledQuote.EngineAssetID,
			instrument.QuoteAssetID,
		)
	}
	if replacement.hasQuote(recycledBase.EngineAssetID, recycledQuote.EngineAssetID) ||
		replacement.hasQuote(recycledQuote.EngineAssetID, recycledBase.EngineAssetID) {
		t.Fatal("recycled asset pair inherited the orphaned pre-reset quote")
	}
}

func TestLocalNode_RestoreBackupClearsOnlyChangedManualInstrument(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sink := newLifecycleQuoteRegistrySink()
	initial := newFakeEngine()
	initial.sink = sink
	n, _ := newTestNode(t, initial)
	instance := seedLifecycleMarketDataInstance(t, n)
	changed := seedLifecycleMarketDataInstrument(
		t, n, instance.ExternalID, "EURUSD", "EUR", "USD", "2", true,
	)
	untouched := seedLifecycleMarketDataInstrument(
		t, n, instance.ExternalID, "GBPJPY", "GBP", "JPY", "200", true,
	)
	publishLifecycleQuote(t, sink, changed, "2")
	publishLifecycleQuote(t, sink, untouched, "200")

	archive, err := n.realm.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	for i := range archive.Data.MarketDataInstruments {
		if archive.Data.MarketDataInstruments[i].ExternalSymbol ==
			changed.ExternalSymbol {
			archive.Data.MarketDataInstruments[i].ManualPrice = "3"
		}
	}
	builds := 0
	n.build = sharedLifecycleQuoteBuild(sink, &builds)
	summary, _, err := n.RestoreBackup(
		ctx,
		testArchive(backup.Scope{All: true}, archive.Data),
		backup.RestoreOptions{
			Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
		},
		testCaller,
	)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired || builds != 0 {
		t.Fatalf("manual restore summary=%+v builds=%d, want online", summary, builds)
	}
	assertLifecycleQuoteAvailability(t, sink, changed, false)
	assertLifecycleQuoteAvailability(t, sink, untouched, true)
}

func TestLocalNode_RestoreBackupRebuildClearsRemovedInstrument(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sink := newLifecycleQuoteRegistrySink()
	initial := newFakeEngine()
	initial.sink = sink
	n, _ := newTestNode(t, initial)
	instance := seedLifecycleMarketDataInstance(t, n)
	removed := seedLifecycleMarketDataInstrument(
		t, n, instance.ExternalID, "EURUSD", "EUR", "USD", "", true,
	)
	untouched := seedLifecycleMarketDataInstrument(
		t, n, instance.ExternalID, "GBPJPY", "GBP", "JPY", "", true,
	)
	publishLifecycleQuote(t, sink, removed, "2")
	publishLifecycleQuote(t, sink, untouched, "200")
	if _, err := n.CreateAccount(ctx, testAccount("delete-me"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	archive, err := n.realm.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	accounts := archive.Data.Accounts[:0]
	for _, account := range archive.Data.Accounts {
		if account.Code != "delete-me" {
			accounts = append(accounts, account)
		}
	}
	archive.Data.Accounts = accounts
	instruments := archive.Data.MarketDataInstruments[:0]
	for _, instrument := range archive.Data.MarketDataInstruments {
		if instrument.ExternalSymbol != removed.ExternalSymbol {
			instruments = append(instruments, instrument)
		}
	}
	archive.Data.MarketDataInstruments = instruments

	builds := 0
	n.build = sharedLifecycleQuoteBuild(sink, &builds)
	summary, _, err := n.RestoreBackup(
		ctx,
		testArchive(backup.Scope{All: true}, archive.Data),
		backup.RestoreOptions{
			Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
		},
		testCaller,
	)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if !summary.RestartRequired || builds != 1 {
		t.Fatalf("rebuild restore summary=%+v builds=%d, want one rebuild", summary, builds)
	}
	assertLifecycleQuoteAvailability(t, sink, removed, false)
	assertLifecycleQuoteAvailability(t, sink, untouched, true)
}

func TestLocalNode_PolicyLifecycleRebuildPreservesQuotes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sink := newLifecycleQuoteRegistrySink()
	initial := newFakeEngine()
	initial.sink = sink
	n, _ := newTestNode(t, initial)
	instance := seedLifecycleMarketDataInstance(t, n)
	instrument := seedLifecycleMarketDataInstrument(
		t, n, instance.ExternalID, "EURUSD", "EUR", "USD", "", true,
	)
	publishLifecycleQuote(t, sink, instrument, "2")

	builds := 0
	n.build = sharedLifecycleQuoteBuild(sink, &builds)
	if _, err := n.PutRateLimit(
		ctx,
		rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
		domain.MissingAccountCreate,
		testCaller,
	); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	if builds != 1 {
		t.Fatalf("policy lifecycle builds = %d, want 1", builds)
	}
	assertLifecycleQuoteAvailability(t, sink, instrument, true)
}

func TestLocalNode_ResetDatabaseFailurePreservesQuotes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newMemoryStore("reset-failure-preserves-quotes.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sink := newLifecycleQuoteRegistrySink()
	initial := newFakeEngine()
	initial.sink = sink
	capable := &lifecycleQuoteServiceEngine{
		fakeEngine: initial,
		service:    sink,
	}
	var seed engine.Snapshot
	nn, _, err := NewLocalNode(ctx, st, func(snapshot engine.Snapshot) (engine.Engine, error) {
		_, buildErr := fakeBuild(initial, &seed)(snapshot)
		if buildErr != nil {
			return nil, buildErr
		}
		return capable, nil
	})
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n)
	instance := seedLifecycleMarketDataInstance(t, n)
	instrument := seedLifecycleMarketDataInstrument(
		t, n, instance.ExternalID, "EURUSD", "EUR", "USD", "", true,
	)
	publishLifecycleQuote(t, sink, instrument, "2")

	resetErr := errors.New("reset failed before replacement")
	st.resetErr = resetErr
	if _, err := n.ResetDatabase(ctx, testCaller); !errors.Is(err, resetErr) {
		t.Fatalf("ResetDatabase error = %v, want %v", err, resetErr)
	}
	if sink.isClosed() || sink.closeCount() != 0 {
		t.Fatalf(
			"service closed=%v close count=%d after failed reset, want false/0",
			sink.isClosed(),
			sink.closeCount(),
		)
	}
	assertLifecycleQuoteAvailability(t, sink, instrument, true)
	if mark, ok := sink.mark(
		instrument.BaseAssetID, instrument.QuoteAssetID,
	); !ok || mark != "2" {
		t.Fatalf("mark after failed reset = %q, %v, want 2, true", mark, ok)
	}
	if err := sink.Clear(instrument.BaseAssetID, instrument.QuoteAssetID); err != nil {
		t.Fatalf("Clear after failed reset: %v", err)
	}
	if sink.hasQuote(instrument.BaseAssetID, instrument.QuoteAssetID) {
		t.Fatal("quote remained after clear on service preserved by failed reset")
	}
	publishLifecycleQuote(t, sink, instrument, "3")
	assertLifecycleQuoteAvailability(t, sink, instrument, true)
	if mark, ok := sink.mark(
		instrument.BaseAssetID, instrument.QuoteAssetID,
	); !ok || mark != "3" {
		t.Fatalf("mark after republish = %q, %v, want 3, true", mark, ok)
	}
}

func TestLocalNode_RestoreBackupDisablingInstanceClearsQuotes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		rebuild bool
	}{
		{name: "online"},
		{name: "rebuild", rebuild: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			sink := newLifecycleQuoteRegistrySink()
			initial := newFakeEngine()
			initial.sink = sink
			n, _ := newTestNode(t, initial)
			instance := seedLifecycleMarketDataInstance(t, n)
			instrument := seedLifecycleMarketDataInstrument(
				t, n, instance.ExternalID, "EURUSD", "EUR", "USD", "2", true,
			)
			publishLifecycleQuote(t, sink, instrument, "2")
			if tc.rebuild {
				if _, err := n.CreateAccount(
					ctx, testAccount("delete-me"), testCaller,
				); err != nil {
					t.Fatalf("CreateAccount: %v", err)
				}
			}

			archive, err := n.realm.ExportBackup(ctx, backup.Scope{All: true})
			if err != nil {
				t.Fatalf("ExportBackup: %v", err)
			}
			foundInstance := false
			for i := range archive.Data.MarketDataInstances {
				if archive.Data.MarketDataInstances[i].ExternalID ==
					instance.ExternalID {
					archive.Data.MarketDataInstances[i].Enabled = false
					foundInstance = true
				}
			}
			if !foundInstance {
				t.Fatalf("backup omitted market-data instance %s", instance.ExternalID)
			}
			mode := backup.RestoreModeOverwrite
			if tc.rebuild {
				accounts := archive.Data.Accounts[:0]
				for _, account := range archive.Data.Accounts {
					if account.Code != "delete-me" {
						accounts = append(accounts, account)
					}
				}
				archive.Data.Accounts = accounts
				mode = backup.RestoreModeReplaceAll
			}

			builds := 0
			n.build = sharedLifecycleQuoteBuild(sink, &builds)
			summary, _, err := n.RestoreBackup(
				ctx,
				testArchive(backup.Scope{All: true}, archive.Data),
				backup.RestoreOptions{
					Scope: backup.Scope{All: true}, Mode: mode,
				},
				testCaller,
			)
			if err != nil {
				t.Fatalf("RestoreBackup: %v", err)
			}
			wantBuilds := 0
			if tc.rebuild {
				wantBuilds = 1
			}
			if summary.RestartRequired != tc.rebuild || builds != wantBuilds {
				t.Fatalf(
					"restore summary=%+v builds=%d, want rebuild=%v builds=%d",
					summary,
					builds,
					tc.rebuild,
					wantBuilds,
				)
			}
			assertLifecycleQuoteAvailability(t, sink, instrument, false)
		})
	}
}

func TestLocalNode_RestoreBackupPreservesNonBYOQuoteOnManualPriceChange(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	sink := newLifecycleQuoteRegistrySink()
	initial := newFakeEngine()
	initial.sink = sink
	n, _ := newTestNode(t, initial)
	instance, err := n.CreateMarketDataInstance(
		ctx,
		domain.MarketDataInstance{
			Provider: domain.MarketDataProviderBinance,
			Label:    "streaming lifecycle quotes",
			Enabled:  true,
		},
		testCaller,
	)
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	instrument := seedLifecycleMarketDataInstrument(
		t, n, instance.ExternalID, "EURUSD", "EUR", "USD", "2", true,
	)
	publishLifecycleQuote(t, sink, instrument, "2")

	archive, err := n.realm.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	for i := range archive.Data.MarketDataInstruments {
		if archive.Data.MarketDataInstruments[i].ExternalSymbol ==
			instrument.ExternalSymbol {
			archive.Data.MarketDataInstruments[i].ManualPrice = "3"
		}
	}
	builds := 0
	n.build = sharedLifecycleQuoteBuild(sink, &builds)
	summary, _, err := n.RestoreBackup(
		ctx,
		testArchive(backup.Scope{All: true}, archive.Data),
		backup.RestoreOptions{
			Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
		},
		testCaller,
	)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired || builds != 0 {
		t.Fatalf("manual restore summary=%+v builds=%d, want online", summary, builds)
	}
	assertLifecycleQuoteAvailability(t, sink, instrument, true)
}
