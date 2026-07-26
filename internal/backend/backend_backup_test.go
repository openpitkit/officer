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
	"errors"
	"reflect"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/internal/backend"
)

func TestService_ExportBackupRoutesScopeCallerAndFilename(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	createdAt := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	fn.backupArchive = backup.NewArchive(
		createdAt,
		"test",
		backup.RealmLabel{Code: "default"},
		backup.Scope{All: true},
		backup.Data{},
	)
	scope := backup.Scope{
		Sections: []backup.Section{backup.SectionAccountsGroups},
	}
	caller := domain.Caller{
		Principal: "operator",
		Source:    domain.SourceAPI,
	}

	archive, filename, err := svc.ExportBackup(
		auth.ContextWithCaller(context.Background(), caller),
		scope,
	)
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	if archive.Manifest.Source != "test" {
		t.Fatalf("archive source = %q, want test", archive.Manifest.Source)
	}
	if filename != backup.Filename(createdAt) {
		t.Fatalf("filename = %q, want %q", filename, backup.Filename(createdAt))
	}
	if !reflect.DeepEqual(fn.backupScope, scope) {
		t.Fatalf("backup scope = %+v, want %+v", fn.backupScope, scope)
	}
	if fn.backupCaller != caller {
		t.Fatalf("backup caller = %+v, want %+v", fn.backupCaller, caller)
	}
}

func TestService_ExportBackupRouteError(t *testing.T) {
	t.Parallel()
	routeErr := errors.New("route failed")
	svc := backend.New(&fakeRouter{routeErr: routeErr}, nil, nil)

	if _, _, err := svc.ExportBackup(
		context.Background(),
		backup.Scope{All: true},
	); !errors.Is(err, routeErr) {
		t.Fatalf("ExportBackup error = %v, want route error", err)
	}
}

func TestService_ExportBackupNodeError(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	exportErr := errors.New("export failed")
	fn.backupErr = exportErr

	if _, _, err := svc.ExportBackup(
		context.Background(),
		backup.Scope{All: true},
	); !errors.Is(err, exportErr) {
		t.Fatalf("ExportBackup error = %v, want export error", err)
	}
}

func TestService_ResetDatabaseRoutesCallerAndRestartsMarketData(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.resetSink = sink
	caller := domain.Caller{
		Principal: "operator",
		Source:    domain.SourcePanel,
	}

	if err := svc.ResetDatabase(
		auth.ContextWithCaller(context.Background(), caller),
	); err != nil {
		t.Fatalf("ResetDatabase: %v", err)
	}
	if fn.resetCaller != caller {
		t.Fatalf("reset caller = %+v, want %+v", fn.resetCaller, caller)
	}
	if md.stops != 1 || md.restarts != 1 {
		t.Fatalf("market-data stops/restarts = %d/%d, want 1/1",
			md.stops, md.restarts)
	}
	if md.sink != sink {
		t.Fatalf("market-data sink = %T, want reset sink", md.sink)
	}
}

func TestService_ResetDatabaseRouteError(t *testing.T) {
	t.Parallel()
	routeErr := errors.New("route failed")
	svc := backend.New(&fakeRouter{routeErr: routeErr}, nil, nil)

	if err := svc.ResetDatabase(context.Background()); !errors.Is(err, routeErr) {
		t.Fatalf("ResetDatabase error = %v, want route error", err)
	}
}

func TestService_ResetDatabaseNodeError(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	resetErr := errors.New("reset failed")
	fn.resetErr = resetErr

	if err := svc.ResetDatabase(context.Background()); !errors.Is(err, resetErr) {
		t.Fatalf("ResetDatabase error = %v, want reset error", err)
	}
}

// TestService_RestartMarketDataReadoptsCurrentSink proves an explicit feed
// restart re-adopts the node's current sink. Reset and residual recovery may
// replace the engine's market-data service, so reusing a cached native sink can
// otherwise leave every push targeting the closed service.
func TestService_RestartMarketDataReadoptsCurrentSink(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	// Simulate the post-rebuild engine handing back a new sink.
	current := &backendTestSink{}
	fn.currentSink = current

	if err := svc.RestartMarketData(context.Background()); err != nil {
		t.Fatalf("RestartMarketData: %v", err)
	}
	if md.stops != 1 || md.restarts != 1 {
		t.Fatalf("market-data stops/restarts = %d/%d, want 1/1",
			md.stops, md.restarts)
	}
	if md.sink != current {
		t.Fatalf("market-data sink = %#v, want current engine sink", md.sink)
	}
}

// TestService_RestartMarketDataNoRuntimeIsNoop confirms a control-plane with no
// market-data runtime (e.g. MCP-only) treats restart as a clean no-op without
// routing to a node.
func TestService_RestartMarketDataNoRuntimeIsNoop(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService()
	if err := svc.RestartMarketData(context.Background()); err != nil {
		t.Fatalf("RestartMarketData with no runtime: %v", err)
	}
}

// TestService_RestartMarketDataRouteError surfaces a routing failure so the
// restart trigger cannot silently skip re-adopting the sink.
func TestService_RestartMarketDataRouteError(t *testing.T) {
	t.Parallel()
	routeErr := errors.New("route failed")
	md := &fakeMarketDataRuntime{}
	svc := backend.New(&fakeRouter{routeErr: routeErr}, md, nil)
	if err := svc.RestartMarketData(context.Background()); !errors.Is(err, routeErr) {
		t.Fatalf("RestartMarketData error = %v, want route error", err)
	}
}

func TestService_RestoreBackupGeneralSettingsDoesNotStopMarketData(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, _ := newTestServiceWithMarketDataRuntime(md)
	_, err := svc.RestoreBackup(context.Background(), restoreArchive(
		backup.SectionGeneralSettings,
	), backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if md.stops != 0 || md.restarts != 0 || md.sink != nil {
		t.Fatalf("market-data touched for non-runtime restore: %+v", md)
	}
}

func TestService_RestoreBackupAccountOnlyKeepsMarketDataRunning(t *testing.T) {
	t.Parallel()
	originalSink := &backendTestSink{}
	md := &fakeMarketDataRuntime{sink: originalSink}
	svc, _ := newTestServiceWithMarketDataRuntime(md)
	_, err := svc.RestoreBackup(context.Background(), restoreArchive(
		backup.SectionAccountsGroups,
	), backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionAccountsGroups}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if md.stops != 0 || md.restarts != 0 || md.sink != originalSink {
		t.Fatalf("market-data lifecycle stops=%d restarts=%d sink=%#v",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_RestoreBackupResidualRebuildKeepsMarketDataRunning(t *testing.T) {
	t.Parallel()
	originalSink := &backendTestSink{}
	md := &fakeMarketDataRuntime{sink: originalSink}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.restoreSink = &backendTestSink{}
	fn.restoreSummary = backup.RestoreSummary{
		Applied:         map[backup.Section]int{},
		Skipped:         map[backup.Section]int{},
		RestartRequired: true,
	}
	_, err := svc.RestoreBackup(context.Background(), restoreArchive(
		backup.SectionPositions,
	), backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionPositions}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if md.stops != 0 || md.restarts != 0 || md.sink != originalSink {
		t.Fatalf("market-data lifecycle stops=%d restarts=%d sink=%#v",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_RestoreBackupMarketDataTopologyRestartsOnce(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	_, err := svc.RestoreBackup(context.Background(), marketDataTopologyArchive(),
		backup.RestoreOptions{
			Scope: backup.Scope{Sections: []backup.Section{backup.SectionMarketData}},
			Mode:  backup.RestoreModeOverwrite,
		})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if md.stops != 1 || md.restarts != 1 || md.sink != sink {
		t.Fatalf("market-data lifecycle stops=%d restarts=%d sink=%#v",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_RestoreBackupQuotesOnlyKeepsUnchangedFeedsRunning(t *testing.T) {
	t.Parallel()
	instance := restoredMarketDataInstance()
	instrument := restoredMarketDataInstrument(instance.ExternalID)
	originalSink := &backendTestSink{}
	md := &fakeMarketDataRuntime{sink: originalSink}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{instance}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		instance.ExternalID.String(): {instrument},
	}
	archive := backup.Archive{
		Manifest: backup.Manifest{Sections: []backup.Section{
			backup.SectionMarketData,
			backup.SectionMarketDataQuotes,
		}},
		Data: backup.Data{
			MarketDataInstances:   []domain.MarketDataInstance{instance},
			MarketDataInstruments: []domain.MarketDataInstrument{instrument},
			MarketDataQuotes: []domain.MarketDataQuote{{
				Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
				BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset,
				Mark: "2",
			}},
		},
	}
	_, err := svc.RestoreBackup(context.Background(), archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionMarketDataQuotes}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if md.stops != 0 || md.restarts != 0 || md.sink != originalSink {
		t.Fatalf("market-data lifecycle stops=%d restarts=%d sink=%#v",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_RestoreBackupManualPriceReconcilesOnline(t *testing.T) {
	t.Parallel()
	instance := restoredMarketDataInstance()
	currentInstrument := restoredMarketDataInstrument(instance.ExternalID)
	restoredInstrument := currentInstrument
	restoredInstrument.ManualPrice = ""
	md := &fakeMarketDataRuntime{sink: &backendTestSink{}}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{instance}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		instance.ExternalID.String(): {currentInstrument},
	}
	archive := backup.Archive{
		Manifest: backup.Manifest{Sections: []backup.Section{backup.SectionMarketData}},
		Data: backup.Data{
			MarketDataInstances:   []domain.MarketDataInstance{instance},
			MarketDataInstruments: []domain.MarketDataInstrument{restoredInstrument},
		},
	}
	_, err := svc.RestoreBackup(context.Background(), archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionMarketData}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if md.stops != 0 || md.restarts != 0 {
		t.Fatalf("market-data lifecycle stops=%d restarts=%d",
			md.stops, md.restarts)
	}
	if len(md.pushed) != 1 || md.pushed[0] != restoredInstrument {
		t.Fatalf("manual updates = %+v, want cleared restored instrument", md.pushed)
	}
}

func TestService_RestoreBackupMarketDataTopologyErrorRestartsReturnedSink(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	fn.restoreErr = errors.New("restore failed")
	_, err := svc.RestoreBackup(context.Background(), marketDataTopologyArchive(),
		backup.RestoreOptions{
			Scope: backup.Scope{Sections: []backup.Section{backup.SectionMarketData}},
			Mode:  backup.RestoreModeOverwrite,
		})
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want error")
	}
	if md.stops != 1 || md.restarts != 1 || md.sink != sink {
		t.Fatalf("market-data recovery stops=%d restarts=%d sink=%#v",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_RestoreBackupMarketDataUseSinkErrorStillRestarts(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{useSinkErr: errors.New("use sink failed")}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.restoreSink = &backendTestSink{}
	_, err := svc.RestoreBackup(context.Background(), marketDataTopologyArchive(),
		backup.RestoreOptions{
			Scope: backup.Scope{Sections: []backup.Section{backup.SectionMarketData}},
			Mode:  backup.RestoreModeOverwrite,
		})
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want UseSink error")
	}
	if md.stops != 1 || md.restarts != 1 {
		t.Fatalf("market-data recovery stops=%d restarts=%d",
			md.stops, md.restarts)
	}
}

func TestService_RestoreBackupMarketDataRestartErrorIsReturned(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{restartErr: errors.New("restart failed")}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	fn.restoreSummary = backup.RestoreSummary{
		Applied:         map[backup.Section]int{backup.SectionMarketData: 1},
		Skipped:         map[backup.Section]int{},
		RestartRequired: true,
	}
	summary, err := svc.RestoreBackup(context.Background(), marketDataTopologyArchive(),
		backup.RestoreOptions{
			Scope: backup.Scope{Sections: []backup.Section{backup.SectionMarketData}},
			Mode:  backup.RestoreModeOverwrite,
		})
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want Restart error")
	}
	if summary.Applied[backup.SectionMarketData] != 1 || !summary.RestartRequired {
		t.Fatalf("RestoreBackup summary = %+v, want committed restore summary", summary)
	}
	if md.stops != 1 || md.restarts != 1 || md.sink != sink {
		t.Fatalf("market-data recovery stops=%d restarts=%d sink=%#v",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_RestoreBackupManualReconciliationErrorReturnsSummary(t *testing.T) {
	t.Parallel()
	instance := restoredMarketDataInstance()
	currentInstrument := restoredMarketDataInstrument(instance.ExternalID)
	restoredInstrument := currentInstrument
	restoredInstrument.ManualPrice = ""
	md := &fakeMarketDataRuntime{
		sink:    &backendTestSink{},
		pushErr: errors.New("manual reconciliation failed"),
	}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.mdInstances = []domain.MarketDataInstance{instance}
	fn.mdInstruments = map[string][]domain.MarketDataInstrument{
		instance.ExternalID.String(): {currentInstrument},
	}
	fn.restoreSummary = backup.RestoreSummary{
		Applied: map[backup.Section]int{backup.SectionMarketData: 1},
		Skipped: map[backup.Section]int{},
	}
	archive := backup.Archive{
		Manifest: backup.Manifest{Sections: []backup.Section{backup.SectionMarketData}},
		Data: backup.Data{
			MarketDataInstances:   []domain.MarketDataInstance{instance},
			MarketDataInstruments: []domain.MarketDataInstrument{restoredInstrument},
		},
	}
	summary, err := svc.RestoreBackup(context.Background(), archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionMarketData}},
		Mode:  backup.RestoreModeOverwrite,
	})
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want manual reconciliation error")
	}
	if summary.Applied[backup.SectionMarketData] != 1 {
		t.Fatalf("RestoreBackup summary = %+v, want committed restore summary", summary)
	}
}

func restoreArchive(sections ...backup.Section) backup.Archive {
	return backup.Archive{Manifest: backup.Manifest{Sections: sections}}
}

func marketDataTopologyArchive() backup.Archive {
	return backup.Archive{
		Manifest: backup.Manifest{Sections: []backup.Section{backup.SectionMarketData}},
		Data: backup.Data{
			MarketDataInstances: []domain.MarketDataInstance{restoredMarketDataInstance()},
		},
	}
}

func restoredMarketDataInstance() domain.MarketDataInstance {
	return domain.MarketDataInstance{
		ExternalID: domain.ExternalID("restored-byo"),
		Provider:   domain.MarketDataProviderBYO,
		Label:      "Restored BYO",
		Enabled:    true,
	}
}

func restoredMarketDataInstrument(instance domain.ExternalID) domain.MarketDataInstrument {
	return domain.MarketDataInstrument{
		Instance:       instance,
		ExternalSymbol: "Z\\USD",
		BaseAsset:      "Z",
		QuoteAsset:     "USD",
		ManualPrice:    "2",
		Enabled:        true,
	}
}
