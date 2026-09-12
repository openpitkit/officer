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
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

func TestLocalNode_ExportBackupAudits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n, st := newTestNode(t, newFakeEngine())

	if _, err := n.ExportBackup(ctx, backup.Scope{All: true}, testCaller); err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if rows[0].Action != domain.AuditActionExportBackup {
		t.Fatalf("latest action = %q, want export_backup", rows[0].Action)
	}
	if rows[0].Actor != testCaller.Principal ||
		rows[0].Source != testCaller.Source {
		t.Fatalf("audit attribution = %+v, want %+v", rows[0], testCaller)
	}
}

func TestRestoreAddsAssets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, realm := newTestNode(t, newFakeEngine())
	if _, err := realm.CreateAsset(ctx, domain.Asset{Code: "existing"}); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	adds, err := restoreAddsAssets(
		ctx, []backup.Asset{{Code: "existing"}}, realm,
	)
	if err != nil {
		t.Fatalf("restoreAddsAssets existing: %v", err)
	}
	if adds {
		t.Fatal("restoreAddsAssets existing = true, want false")
	}

	adds, err = restoreAddsAssets(
		ctx, []backup.Asset{{Code: "inserted"}}, realm,
	)
	if err != nil {
		t.Fatalf("restoreAddsAssets inserted: %v", err)
	}
	if !adds {
		t.Fatal("restoreAddsAssets inserted = false, want true")
	}

	wantErr := errors.New("list assets")
	_, err = restoreAddsAssets(
		ctx,
		[]backup.Asset{{Code: "inserted"}},
		assetListErrorRealm{RealmStore: realm, err: wantErr},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("restoreAddsAssets error = %v, want %v", err, wantErr)
	}
}

// testArchive builds a portable archive carrying data, labelled with the default
// realm, for the restore tests.
func testArchive(scope backup.Scope, data backup.Data) backup.Archive {
	return backup.NewArchive(
		backupTestTime(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		scope,
		data,
		backup.CredentialFormPlaintext,
	)
}

type restoreCommitProbeRealm struct {
	store.RealmStore
	afterCommit func()
}

type assetListErrorRealm struct {
	store.RealmStore
	err error
}

func (r assetListErrorRealm) ListAssets(context.Context) ([]domain.Asset, error) {
	return nil, r.err
}

type assetClassificationProbeRealm struct {
	store.RealmStore
	classified     chan<- struct{}
	classifiedOnce sync.Once
	release        <-chan struct{}
}

func (r *assetClassificationProbeRealm) ListAssets(
	ctx context.Context,
) ([]domain.Asset, error) {
	assets, err := r.RealmStore.ListAssets(ctx)
	if err != nil || r.classified == nil {
		return assets, err
	}
	r.classifiedOnce.Do(func() { close(r.classified) })
	<-r.release
	return assets, nil
}

func (r *restoreCommitProbeRealm) ExportBackup(
	ctx context.Context, scope backup.Scope,
) (backup.Archive, error) {
	if err := ctx.Err(); err != nil {
		return backup.Archive{}, err
	}
	return r.RealmStore.ExportBackup(ctx, scope)
}

func (r *restoreCommitProbeRealm) RestoreBackup(
	ctx context.Context,
	archive backup.Archive,
	opts backup.RestoreOptions,
) (backup.RestoreSummary, error) {
	summary, err := r.RealmStore.RestoreBackup(ctx, archive, opts)
	if err == nil && r.afterCommit != nil {
		r.afterCommit()
	}
	return summary, err
}

type cancelAfterResetStore struct {
	store.Store
	cancel context.CancelFunc
}

func (s *cancelAfterResetStore) ForRealm(
	ctx context.Context, realm domain.RealmID,
) (store.RealmStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Store.ForRealm(ctx, realm)
}

func (s *cancelAfterResetStore) Reset(ctx context.Context) error {
	if err := s.Store.Reset(ctx); err != nil {
		return err
	}
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

func TestLocalNode_RestoreBackupPublishesInsertedAccountOnline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	oldEngine.enforceResolver = true
	n, st := newTestNode(t, oldEngine)
	oldSink := oldEngine.MarketDataSink()
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}
	archive := testArchive(
		backup.Scope{All: true},
		backup.Data{
			Assets:   []backup.Asset{{Code: "restored-asset"}},
			Accounts: []backup.Account{{Code: "restored"}},
			Balances: []backup.Balance{{
				Account: "restored", Asset: "restored-asset", Available: "1",
			}},
		},
	)

	summary, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeInsertMissing,
	}, testCaller)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired {
		t.Fatalf("RestartRequired = true, want online publication")
	}
	if !oldEngine.running || n.currentEngine() != oldEngine {
		t.Fatalf("restore replaced or stopped the live engine")
	}
	if builds != 0 {
		t.Fatalf("build calls = %d, want 0", builds)
	}
	if n.currentMarketDataSink() != oldSink {
		t.Fatalf("restore replaced market-data sink")
	}
	if _, ok := oldEngine.knownAccounts["restored"]; !ok {
		t.Fatalf("live resolver missing restored account: %+v", oldEngine.knownAccounts)
	}
	if _, ok := oldEngine.knownAssets["restored-asset"]; !ok {
		t.Fatalf("live resolver missing restored asset: %+v", oldEngine.knownAssets)
	}
	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if rows[0].Action != domain.AuditActionRestoreBackup {
		t.Fatalf("latest action = %q, want restore_backup", rows[0].Action)
	}
	if _, err := n.ApplyAdjustment(
		ctx,
		testKey("restored"),
		"",
		domain.AdjustmentRequest{
			Asset: "restored-asset",
			Balance: &domain.AdjustmentAmount{
				Mode:  domain.AdjustmentModeAbsolute,
				Value: "2",
			},
		},
		domain.MissingAccountReject,
		testCaller,
	); err != nil {
		t.Fatalf("ApplyAdjustment after restore: %v", err)
	}
}

func TestLocalNode_RestoreBackupSettingsOnlyPublishesInsertedAssetOnline(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	oldEngine.enforceResolver = true
	n, _ := newTestNode(t, oldEngine)
	if _, err := n.CreateAccount(ctx, testAccount("restored"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	oldSink := oldEngine.MarketDataSink()
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}

	archive := testArchive(
		backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		backup.Data{Assets: []backup.Asset{{Code: "restored-asset"}}},
	)
	summary, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{
			backup.SectionGeneralSettings,
		}},
		Mode: backup.RestoreModeInsertMissing,
	}, testCaller)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired {
		t.Fatalf("RestartRequired = true, want online publication")
	}
	if !oldEngine.running || n.currentEngine() != oldEngine || builds != 0 {
		t.Fatalf("settings restore replaced the engine: builds=%d", builds)
	}
	if n.currentMarketDataSink() != oldSink {
		t.Fatal("settings restore replaced market-data sink")
	}
	if _, err := n.ApplyAdjustment(
		ctx,
		testKey("restored"),
		"",
		domain.AdjustmentRequest{
			Asset: "restored-asset",
			Balance: &domain.AdjustmentAmount{
				Mode:  domain.AdjustmentModeAbsolute,
				Value: "2",
			},
		},
		domain.MissingAccountReject,
		testCaller,
	); err != nil {
		t.Fatalf("ApplyAdjustment after settings restore: %v", err)
	}
}

func TestLocalNode_RestoreBackupSerializesAssetClassificationWithDelete(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	base := newMemoryStore("restore-asset-classification.db")
	t.Cleanup(func() { _ = base.Close() })
	var probe *assetClassificationProbeRealm
	st := newRealmWrapStore(base, func(realm store.RealmStore) store.RealmStore {
		probe = &assetClassificationProbeRealm{RealmStore: realm}
		return probe
	})
	eng := newFakeEngine()
	eng.enforceResolver = true
	n := newTestNodeWithStore(t, st, eng)
	asset, err := n.CreateAsset(ctx, domain.Asset{Code: "GOLD"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	classified := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseClassification := func() {
		releaseOnce.Do(func() { close(release) })
	}
	t.Cleanup(releaseClassification)
	probe.classified = classified
	probe.release = release

	type restoreResult struct {
		summary backup.RestoreSummary
		err     error
	}
	restored := make(chan restoreResult, 1)
	go func() {
		summary, _, err := n.RestoreBackup(
			ctx,
			testArchive(
				backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
				backup.Data{Assets: []backup.Asset{{Code: asset.Code}}},
			),
			backup.RestoreOptions{
				Scope: backup.Scope{Sections: []backup.Section{
					backup.SectionGeneralSettings,
				}},
				Mode: backup.RestoreModeInsertMissing,
			},
			testCaller,
		)
		restored <- restoreResult{summary: summary, err: err}
	}()
	<-classified

	// TryLock only observes ownership; it never serializes this test.
	if n.laneGate.TryLock() {
		n.laneGate.Unlock()
		t.Fatal("RestoreBackup asset classification did not hold the lane")
	}
	deleted := make(chan error, 1)
	go func() {
		deleted <- n.DeleteAsset(ctx, asset.Code, false, testCaller)
	}()
	releaseClassification()
	result := <-restored
	if result.err != nil {
		t.Fatalf("RestoreBackup: %v", result.err)
	}
	if result.summary.RestartRequired {
		t.Fatal("RestoreBackup required an engine restart")
	}
	if err := <-deleted; err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}

	_, stored, err := n.realm.GetAsset(ctx, asset.Code)
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	eng.resolverMu.RLock()
	_, resolved := eng.assetResolverIDs[asset.Code]
	eng.resolverMu.RUnlock()
	if stored != resolved {
		t.Fatalf("asset store presence=%v resolver presence=%v", stored, resolved)
	}
	if stored {
		t.Fatal("delete completed before restore despite serialized classification")
	}
}

func TestLocalNode_RestoreBackupAssetOnlyRuntimeDeltaPublishesOnline(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	oldEngine.enforceResolver = true
	n, _ := newTestNode(t, oldEngine)
	if _, err := n.CreateAccount(ctx, testAccount("restored"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	before, err := n.realm.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	before.Data.Assets = append(
		before.Data.Assets,
		backup.Asset{Code: "restored-asset"},
	)
	oldSink := oldEngine.MarketDataSink()
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}

	summary, _, err := n.RestoreBackup(
		ctx,
		testArchive(backup.Scope{All: true}, before.Data),
		backup.RestoreOptions{
			Scope: backup.Scope{All: true},
			Mode:  backup.RestoreModeInsertMissing,
		},
		testCaller,
	)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired {
		t.Fatalf("RestartRequired = true, want online publication")
	}
	if !oldEngine.running || n.currentEngine() != oldEngine || builds != 0 {
		t.Fatalf("asset-only runtime restore rebuilt the engine: builds=%d", builds)
	}
	if n.currentMarketDataSink() != oldSink {
		t.Fatal("asset-only runtime restore replaced market-data sink")
	}
	if _, err := n.ApplyAdjustment(
		ctx,
		testKey("restored"),
		"",
		domain.AdjustmentRequest{
			Asset: "restored-asset",
			Balance: &domain.AdjustmentAmount{
				Mode:  domain.AdjustmentModeAbsolute,
				Value: "2",
			},
		},
		domain.MissingAccountReject,
		testCaller,
	); err != nil {
		t.Fatalf("ApplyAdjustment after asset-only runtime restore: %v", err)
	}
}

func TestLocalNode_RestoreBackupDetachesReconciliationAfterCommit(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := newMemoryStore("restore-post-commit-cancel.db")
	t.Cleanup(func() { _ = base.Close() })
	var probe *restoreCommitProbeRealm
	st := newRealmWrapStore(base, func(realm store.RealmStore) store.RealmStore {
		probe = &restoreCommitProbeRealm{RealmStore: realm}
		return probe
	})
	eng := newFakeEngine()
	eng.enforceResolver = true
	n := newTestNodeWithStore(t, st, eng)
	probe.afterCommit = cancel
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	summary, _, err := n.RestoreBackup(
		ctx,
		testArchive(backup.Scope{All: true}, backup.Data{
			Accounts: []backup.Account{{Code: "restored"}},
		}),
		backup.RestoreOptions{
			Scope: backup.Scope{All: true}, Mode: backup.RestoreModeInsertMissing,
		},
		testCaller,
	)
	if err != nil {
		t.Fatalf("RestoreBackup after request cancellation: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("restore commit hook did not cancel the request context")
	}
	if fatalErr != nil {
		t.Fatalf("post-commit request cancellation triggered fatal path: %v", fatalErr)
	}
	if summary.RestartRequired {
		t.Fatal("insert-only restore unexpectedly rebuilt the engine")
	}
	if _, ok := eng.knownAccounts["restored"]; !ok {
		t.Fatalf("restored account was not published after cancellation: %+v", eng.knownAccounts)
	}
}

func TestLocalNode_RestoreBackupOverwriteUpdatesRuntimeOnline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, _ := newTestNode(t, eng)
	group, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk"}, testCaller)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	account, err := n.CreateAccount(ctx, testAccount("existing"), testCaller)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	initialRate := rateLimit(domain.ScopeBroker, "", "", 10, time.Second)
	prepared := newFakeEngine()
	prepared.enforceResolver = true
	n.build = fakeBuild(prepared, new(engine.Snapshot))
	if _, err := n.PutRateLimit(ctx, initialRate, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	eng = prepared
	eng.configureCalls = nil
	sinkBefore := eng.MarketDataSink()
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}
	archive := testArchive(backup.Scope{All: true}, backup.Data{
		Assets: []backup.Asset{{Code: "USD"}},
		Groups: []backup.AccountGroup{{
			Code: "desk", Blocked: true, BlockReason: "restored group block",
		}},
		Accounts: []backup.Account{
			{
				Code: "existing", GroupCode: "desk", Currency: "USD", Pnl: "7",
				Blocked: true, BlockReason: "restored account block",
				BlockPolicy:  "SpotFundsPolicy",
				BlockCode:    domain.RejectCodePnlKillSwitchTriggered,
				BlockDetails: "restored account block details",
			},
			{Code: "added", GroupCode: "desk", Pnl: "3"},
		},
		Balances: []backup.Balance{{
			Account: "existing", Asset: "USD", Available: "12",
			Held: "2", Incoming: "1", RealizedPnl: "4",
		}},
		RateLimits: []domain.LimitRate{
			rateLimit(domain.ScopeBroker, "", "", 20, 2*time.Second),
		},
	})

	summary, sink, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}, testCaller)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired || builds != 0 || n.currentEngine() != eng {
		t.Fatalf("overwrite rebuilt engine: summary=%+v builds=%d", summary, builds)
	}
	if sink != sinkBefore || n.currentMarketDataSink() != sinkBefore {
		t.Fatal("overwrite replaced market-data sink")
	}
	if eng.accountResolverIDs[account.Code] != account.EngineAccountID {
		t.Fatalf("existing account engine id changed: got %d want %d",
			eng.accountResolverIDs[account.Code], account.EngineAccountID)
	}
	if eng.groupResolverIDs[group.Code] != group.EngineGroupID {
		t.Fatalf("existing group engine id changed: got %d want %d",
			eng.groupResolverIDs[group.Code], group.EngineGroupID)
	}
	if _, ok := eng.knownAccounts["added"]; !ok {
		t.Fatalf("live resolver missing added account: %+v", eng.knownAccounts)
	}
	assertFakeAccountGroup(t, eng, "existing", "desk")
	assertFakeAccountPnl(t, eng, "existing", "USD", "7")
	assertFakeBalances(t, eng, "existing", "USD", "12", "2", "1")
	assertFakeTypedOrderBlock(t, eng, "existing", domain.AccountBlock{
		Policy: "SpotFundsPolicy", Code: domain.RejectCodePnlKillSwitchTriggered,
		Reason: "restored account block", Details: "restored account block details",
	})
	assertFakeOrderBlock(t, eng, "added", true, "restored group block")
	if len(eng.configureCalls) != 1 ||
		eng.configureCalls[0].policy != domain.PolicyRateLimit ||
		eng.configureCalls[0].limits.RateLimits[0].MaxOrders != 20 {
		t.Fatalf("restored policy configure calls = %+v", eng.configureCalls)
	}
}

func TestLocalNode_RestoreBackupAccountDeletionStillRebuilds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := newFakeEngine()
	n, _ := newTestNode(t, old)
	if _, err := n.CreateAccount(ctx, testAccount("delete-me"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	next := newFakeEngine()
	builds := 0
	n.build = func(snap engine.Snapshot) (engine.Engine, error) {
		builds++
		return fakeBuild(next, new(engine.Snapshot))(snap)
	}

	summary, sink, err := n.RestoreBackup(
		ctx,
		testArchive(backup.Scope{All: true}, backup.Data{}),
		backup.RestoreOptions{
			Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
		},
		testCaller,
	)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if !summary.RestartRequired || builds != 1 {
		t.Fatalf("account-deleting restore summary=%+v builds=%d, want one rebuild",
			summary, builds)
	}
	if old.running || n.currentEngine() != next || sink != next.MarketDataSink() {
		t.Fatalf("account-deleting restore did not swap engine")
	}
}

func TestLocalNode_RestoreBackupReservesRestartBeforeCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := newMemoryStore("restore-restart-reservation.db")
	t.Cleanup(func() { _ = base.Close() })
	committed := make(chan struct{})
	release := make(chan struct{})
	var probe *restoreCommitProbeRealm
	st := newRealmWrapStore(base, func(realm store.RealmStore) store.RealmStore {
		probe = &restoreCommitProbeRealm{RealmStore: realm}
		return probe
	})
	old := newFakeEngine()
	n := newTestNodeWithStore(t, st, old)
	if _, err := n.CreateAccount(ctx, testAccount("delete-me"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	next := newFakeEngine()
	n.build = fakeBuild(next, new(engine.Snapshot))
	probe.afterCommit = func() {
		close(committed)
		<-release
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	type restoreResult struct {
		summary backup.RestoreSummary
		err     error
	}
	results := make(chan restoreResult, 1)
	go func() {
		summary, _, err := n.RestoreBackup(
			ctx,
			testArchive(backup.Scope{All: true}, backup.Data{}),
			backup.RestoreOptions{
				Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
			},
			testCaller,
		)
		results <- restoreResult{summary: summary, err: err}
	}()
	<-committed
	if !n.restarting.Load() {
		close(release)
		<-results
		t.Fatal("runtime restore did not reserve restart ownership before commit")
	}
	if err := n.beginEngineRestart(); !errors.Is(err, domain.ErrEngineRestarting) {
		close(release)
		<-results
		if err == nil {
			n.endEngineRestart()
		}
		t.Fatalf("concurrent engine restart error = %v, want ErrEngineRestarting", err)
	}
	close(release)
	result := <-results
	if result.err != nil {
		t.Fatalf("RestoreBackup: %v", result.err)
	}
	if !result.summary.RestartRequired || n.currentEngine() != next {
		t.Fatalf("restore result = %+v engine=%T, want reserved rebuild", result.summary, n.currentEngine())
	}
	if fatalErr != nil {
		t.Fatalf("concurrent restart attempt triggered fatal path: %v", fatalErr)
	}
}

func TestLocalNode_RestoreBackupPublishesDefaultCurrencyOnline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, _ := newTestNode(t, eng)
	sinkBefore := eng.MarketDataSink()
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}

	summary, sink, err := n.RestoreBackup(
		ctx,
		testArchive(backup.Scope{All: true}, backup.Data{
			Assets:               []backup.Asset{{Code: "EUR"}, {Code: "USD"}},
			Accounts:             []backup.Account{{Code: "probe"}},
			DefaultGroupCurrency: "EUR",
		}),
		backup.RestoreOptions{
			Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
		},
		testCaller,
	)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired || builds != 0 || n.currentEngine() != eng {
		t.Fatalf("default currency restore rebuilt: summary=%+v builds=%d", summary, builds)
	}
	if sink != sinkBefore {
		t.Fatalf("default currency restore replaced sink: %T", sink)
	}
	assertFakeEffectiveCurrency(t, eng, "probe", "EUR")
	if _, ok := eng.knownGroups[""]; !ok {
		t.Fatalf("default group resolver entry missing: %+v", eng.knownGroups)
	}
	if eng.groupResolverIDs[""] != 0 {
		t.Fatalf("default group resolver id = %d, want reserved 0",
			eng.groupResolverIDs[""])
	}
}

func TestLocalNode_RestoreBackupClearsDefaultCurrencyWithoutRemovingResolver(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, _ := newTestNode(t, eng)
	if _, _, err := n.RestoreBackup(
		ctx,
		testArchive(backup.Scope{All: true}, backup.Data{
			Assets:               []backup.Asset{{Code: "EUR"}},
			Accounts:             []backup.Account{{Code: "probe"}},
			DefaultGroupCurrency: "EUR",
		}),
		backup.RestoreOptions{
			Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
		},
		testCaller,
	); err != nil {
		t.Fatalf("seed RestoreBackup: %v", err)
	}

	summary, _, err := n.RestoreBackup(
		ctx,
		testArchive(backup.Scope{All: true}, backup.Data{
			Assets:   []backup.Asset{{Code: "EUR"}, {Code: "USD"}},
			Accounts: []backup.Account{{Code: "probe"}},
		}),
		backup.RestoreOptions{
			Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
		},
		testCaller,
	)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired {
		t.Fatal("default currency clear unexpectedly rebuilt the engine")
	}
	if _, ok := eng.knownGroups[""]; !ok {
		t.Fatalf("default group resolver entry was removed: %+v", eng.knownGroups)
	}
	assertFakeNoEffectiveCurrency(t, eng, "probe", "USD")
}

func TestMemoryRealmRestoreDefaultGroupSemantics(t *testing.T) {
	t.Parallel()
	realm := newMemoryStore("memory-default-restore.db").realm

	realm.restoreData(backup.Data{}, backup.RestoreModeOverwrite)
	if _, ok := realm.groups[""]; ok {
		t.Fatal("empty restore invented an absent default group")
	}

	realm.restoreData(
		backup.Data{DefaultGroupCurrency: "USD"},
		backup.RestoreModeOverwrite,
	)
	group, ok := realm.groups[""]
	if !ok || group.Currency != "USD" || group.EngineGroupID != 0 {
		t.Fatalf("non-empty default restore = %+v ok=%v, want USD with id 0", group, ok)
	}

	realm.restoreData(backup.Data{}, backup.RestoreModeOverwrite)
	group, ok = realm.groups[""]
	if !ok || group.Currency != "" || group.EngineGroupID != 0 {
		t.Fatalf("cleared default restore = %+v ok=%v, want retained row with id 0", group, ok)
	}
}

// TestLocalNode_RestoreBackupNonRuntimeScopePublishesForceIncludedGroup proves
// normalized force-included dictionaries take the exclusive live-publication
// gate even when the requested section itself is observational.
func TestLocalNode_RestoreBackupNonRuntimeScopePublishesForceIncludedGroup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	n, _ := newTestNode(t, oldEngine)

	oldEngine.enforceResolver = true
	oldSink := oldEngine.MarketDataSink()
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}

	// The archive carries a new group (and its member account) but the caller
	// requests ONLY the audit log: Normalize force-includes accounts+groups so the
	// group travels and restores, even though audit_log is observational.
	archive := testArchive(
		backup.Scope{Sections: []backup.Section{backup.SectionAuditLog}},
		backup.Data{
			Groups:   []backup.AccountGroup{{Code: "restored-desk"}},
			Accounts: []backup.Account{{Code: "restored-acc", GroupCode: "restored-desk"}},
		},
	)

	summary, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionAuditLog}},
		Mode:  backup.RestoreModeInsertMissing,
	}, testCaller)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if summary.RestartRequired {
		t.Fatalf("RestartRequired = true, want online force-included dictionary publication")
	}
	if !oldEngine.running || n.currentEngine() != oldEngine || builds != 0 {
		t.Fatalf("force-included restore replaced the engine: builds=%d", builds)
	}
	if n.currentMarketDataSink() != oldSink {
		t.Fatalf("force-included restore replaced market-data sink")
	}
	if _, ok := oldEngine.knownGroups["restored-desk"]; !ok {
		t.Fatalf("live resolver missing restored-desk: %+v", oldEngine.knownGroups)
	}

	// The store lists the group and, because the rebuild reconciled the resolver,
	// a group block into it succeeds instead of failing with ErrInvalid.
	if err := n.SetGroupBlocked(ctx, "restored-desk", true, "risk", testCaller); err != nil {
		t.Fatalf("SetGroupBlocked into restored group: %v", err)
	}
	group, ok, err := n.realm.GetGroup(ctx, "restored-desk")
	if err != nil || !ok || !group.Blocked {
		t.Fatalf("restored group after block = %+v ok=%v err=%v", group, ok, err)
	}
	assertFakeOrderBlock(t, oldEngine, "restored-acc", true, "risk")
}

func TestLocalNode_RestoreBackupBuildFailureKeepsCommittedStoreAndFatals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	n, st := newTestNode(t, oldEngine)
	if _, err := n.CreateAccount(ctx, testAccount("keep"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	buildErr := errors.New("build failed")
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, buildErr
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }
	archive := testArchive(
		backup.Scope{All: true},
		backup.Data{Accounts: []backup.Account{{Code: "new"}}},
	)

	_, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller)
	if !errors.Is(err, buildErr) {
		t.Fatalf("RestoreBackup error = %v, want build failure", err)
	}
	if fatalErr == nil || !errors.Is(fatalErr, buildErr) {
		t.Fatalf("fatal error = %v, want build failure", fatalErr)
	}
	if n.currentEngine() != oldEngine || !oldEngine.running {
		t.Fatal("prepared rebuild failure replaced or stopped the old engine")
	}
	if _, ok, getErr := st.GetAccount(ctx, "keep"); getErr != nil || ok {
		t.Fatalf("keep account ok=%v err=%v, want removed by committed restore", ok, getErr)
	}
	if _, ok, getErr := st.GetAccount(ctx, "new"); getErr != nil || !ok {
		t.Fatalf("new account ok=%v err=%v, want committed", ok, getErr)
	}
}

func TestLocalNode_RestoreBackupPostSwapMirrorFailureDoesNotRollback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := newFakeEngine()
	var fatalErr error
	n, realm := newTestNode(t, old)
	n.fatal = func(err error) { fatalErr = err }
	if _, err := n.CreateAccount(ctx, testAccount("removed"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	next := &seedBlockEngine{
		fakeEngine: newFakeEngine(),
		seedBlocks: []domain.AccountBlock{seedBlockOf("missing")},
	}
	n.build = func(snapshot engine.Snapshot) (engine.Engine, error) {
		if _, err := fakeBuild(next.fakeEngine, new(engine.Snapshot))(snapshot); err != nil {
			return nil, err
		}
		return next, nil
	}
	archive := testArchive(backup.Scope{All: true}, backup.Data{})

	_, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller)
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want post-swap mirror failure")
	}
	if fatalErr == nil {
		t.Fatal("fatal error = nil after post-swap mirror failure")
	}
	if n.currentEngine() != next || old.running || !next.running {
		t.Fatalf(
			"engine state after post-swap failure: current=%p old=%v next=%v",
			n.currentEngine(), old.running, next.running,
		)
	}
	if _, ok, getErr := realm.GetAccount(ctx, "removed"); getErr != nil || ok {
		t.Fatalf(
			"removed account after committed restore: ok=%v err=%v, want absent",
			ok, getErr,
		)
	}
}

func TestLocalNode_RestoreBackupAuditFailureKeepsCommittedRuntimeAndFatals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()

	real := newMemoryStore("node.db")
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failRestoreAuditRealm{RealmStore: r}
	})

	nextEngine := newFakeEngine()
	buildCount := 0
	build := func(engine.Snapshot) (engine.Engine, error) {
		buildCount++
		if buildCount == 1 {
			return oldEngine, nil
		}
		return nextEngine, nil
	}
	nn, _, err := NewLocalNode(ctx, st, build)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n)
	if _, err := n.CreateAccount(ctx, testAccount("keep"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }
	archive := testArchive(
		backup.Scope{All: true},
		backup.Data{Accounts: []backup.Account{{Code: "new"}}},
	)

	_, _, err = n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller)
	if !errors.Is(err, errRestoreAuditFailed) {
		t.Fatalf("RestoreBackup error = %v, want audit failure", err)
	}
	if fatalErr == nil || !errors.Is(fatalErr, errRestoreAuditFailed) {
		t.Fatalf("fatal error = %v, want audit failure", fatalErr)
	}
	if n.currentEngine() != nextEngine || !nextEngine.running || oldEngine.running {
		t.Fatalf(
			"engine state after audit failure: current=%p old=%v next=%v",
			n.currentEngine(), oldEngine.running, nextEngine.running,
		)
	}
	if buildCount != 2 {
		t.Fatalf("build count = %d, want initial plus committed restore", buildCount)
	}
	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	if _, ok, getErr := realm.GetAccount(ctx, "keep"); getErr != nil || ok {
		t.Fatalf("keep account ok=%v err=%v, want removed", ok, getErr)
	}
	if _, ok, getErr := realm.GetAccount(ctx, "new"); getErr != nil || !ok {
		t.Fatalf("new account ok=%v err=%v, want committed", ok, getErr)
	}
}

func TestLocalNode_RestoreBackupRollsBackStoreOnAuditFailureWithoutRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()

	real := newMemoryStore("node.db")
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	realm0, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	if err := realm0.SetMcpAccess(ctx, "submit_order", true); err != nil {
		t.Fatalf("SetMcpAccess: %v", err)
	}
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failRestoreAuditRealm{RealmStore: r}
	})
	n := newTestNodeWithStore(t, st, oldEngine)
	if _, err := n.CreateAccount(ctx, testAccount("keep-runtime"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }
	archive := testArchive(
		backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		backup.Data{McpAccess: map[string]bool{"submit_order": false}},
	)

	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		// All means every section available in this archive, not every possible
		// section. The manifest carries only general settings, so this restore is
		// non-runtime and remains safely rollback-capable.
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller); err == nil {
		t.Fatal("RestoreBackup succeeded, want audit failure")
	}
	if fatalErr != nil {
		t.Fatalf("fatal error = %v, want non-runtime rollback", fatalErr)
	}
	if !oldEngine.running {
		t.Fatalf("engine stopped for non-runtime rollback")
	}
	if _, ok, err := realm0.GetAccount(ctx, "keep-runtime"); err != nil || !ok {
		t.Fatalf("runtime account after settings-only restore: ok=%v err=%v", ok, err)
	}
	access, err := realm0.ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	if access["submit_order"] != true {
		t.Fatalf("mcp access = %+v, want rollback to true", access)
	}
}

// failRestoreAuditDomainSentinelRealm fails the restore_backup audit append
// with a caller-supplied cause, so a test can drive rollbackStore's
// successful-rollback branch with a cause that wraps a domain sentinel.
type failRestoreAuditDomainSentinelRealm struct {
	store.RealmStore
	cause error
}

func (s *failRestoreAuditDomainSentinelRealm) AppendAudit(
	ctx context.Context, entry store.AuditEntry,
) error {
	if entry.Action == domain.AuditActionRestoreBackup {
		return s.cause
	}
	return s.RealmStore.AppendAudit(ctx, entry)
}

// TestLocalNode_RestoreBackupRollbackSuccessIsInternal covers the
// successful-rollback branch of rollbackStore: the non-runtime restore already
// committed before the final audit write failed, so even though the
// compensating restore-to-rollback-archive restores a consistent state, the
// failure is Officer's to own. The caller must not see it classified as the
// domain sentinel the underlying store failure carried, while the diagnostic
// cause must still be reachable for logs.
func TestLocalNode_RestoreBackupRollbackSuccessIsInternal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()

	real := newMemoryStore("node.db")
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	realm0, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	if err := realm0.SetMcpAccess(ctx, "submit_order", true); err != nil {
		t.Fatalf("SetMcpAccess: %v", err)
	}
	diagnosticCause := errors.New("audit backend rejected the restore row")
	auditErr := fmt.Errorf(
		"audit store constraint: %w: %w", domain.ErrAlreadyExists, diagnosticCause,
	)
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failRestoreAuditDomainSentinelRealm{RealmStore: r, cause: auditErr}
	})
	n := newTestNodeWithStore(t, st, oldEngine)
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }
	archive := testArchive(
		backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		backup.Data{McpAccess: map[string]bool{"submit_order": false}},
	)

	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		// All means every section available in this archive, not every possible
		// section. The manifest carries only general settings, so this restore is
		// non-runtime and remains safely rollback-capable.
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller); err == nil {
		t.Fatal("RestoreBackup succeeded, want audit failure")
	} else if errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf(
			"RestoreBackup error = %v, must not expose the domain sentinel after a "+
				"committed restore rollback",
			err,
		)
	} else if !errors.Is(err, diagnosticCause) {
		t.Fatalf("RestoreBackup error = %v, want diagnostic cause preserved", err)
	}
	if fatalErr != nil {
		t.Fatalf("fatal error = %v, want non-runtime rollback without a fatal", fatalErr)
	}
	access, err := realm0.ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	if access["submit_order"] != true {
		t.Fatalf("mcp access = %+v, want rollback to true", access)
	}
}

func TestLocalNode_RestoreBackupRollbackPreservesExactSelectors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	real := newMemoryStore("restore-selector-scope.db")
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })

	var probe *restoreScopeProbeRealm
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		probe = &restoreScopeProbeRealm{RealmStore: r}
		return probe
	})
	n := newTestNodeWithStore(t, st, newFakeEngine())
	scope := backup.Scope{
		Sections: []backup.Section{backup.SectionUserSettings},
		Accounts: backup.EntitySelector{
			Accounts: []string{"selected-account"},
			Groups:   []string{"selected-group"},
		},
		Positions: backup.EntitySelector{
			Accounts: []string{"selected-position-account"},
		},
	}
	archive := testArchive(scope, backup.Data{UserSettings: []domain.UserSetting{{
		UserID: "user", Key: "layout", Value: "restored",
	}}})

	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope,
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller); !errors.Is(err, errRestoreAuditFailed) {
		t.Fatalf("RestoreBackup error = %v, want audit failure", err)
	}
	if len(probe.exportScopes) != 1 {
		t.Fatalf("rollback export scopes = %+v, want one", probe.exportScopes)
	}
	if len(probe.restoreScopes) != 2 {
		t.Fatalf("restore scopes = %+v, want restore plus rollback", probe.restoreScopes)
	}
	want := scope.Normalize()
	if !reflect.DeepEqual(probe.exportScopes[0], want) {
		t.Fatalf("rollback export scope = %+v, want %+v", probe.exportScopes[0], want)
	}
	if !reflect.DeepEqual(probe.restoreScopes[1], want) {
		t.Fatalf("rollback restore scope = %+v, want %+v", probe.restoreScopes[1], want)
	}
}

func TestLocalNode_RestoreBackupJoinsRollbackRestoreFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	real := newMemoryStore("node.db")
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	realm0, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	if err := realm0.SetMcpAccess(ctx, "submit_order", true); err != nil {
		t.Fatalf("SetMcpAccess: %v", err)
	}
	rollbackErr := errors.New("rollback restore failed")
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failRollbackRestoreRealm{
			RealmStore:  &failRestoreAuditRealm{RealmStore: r},
			rollbackErr: rollbackErr,
		}
	})
	n := newTestNodeWithStore(t, st, newFakeEngine())
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }
	archive := testArchive(
		backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		backup.Data{McpAccess: map[string]bool{"submit_order": false}},
	)

	_, _, err = n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller)
	if err == nil {
		t.Fatal("RestoreBackup succeeded, want audit and rollback failure")
	}
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("RestoreBackup error = %v, want rollback restore error", err)
	}
	if !errors.Is(err, errRestoreAuditFailed) {
		t.Fatalf("RestoreBackup error = %v, want audit failure", err)
	}
	if fatalErr == nil || !errors.Is(fatalErr, rollbackErr) {
		t.Fatalf("fatal error = %v, want rollback restore failure", fatalErr)
	}
}

func TestLocalNode_ResetDatabaseRebuildFailureIsFatal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newMemoryStore("reset-rebuild-failure.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	old := newFakeEngine()
	n := newTestNodeWithStore(t, st, old)
	if _, err := n.CreateAccount(ctx, testAccount("reset-me"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	rebuildErr := errors.New("reset rebuild failed")
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, rebuildErr
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	_, err := n.ResetDatabase(ctx, testCaller)
	if !errors.Is(err, rebuildErr) {
		t.Fatalf("ResetDatabase error = %v, want rebuild failure", err)
	}
	if fatalErr == nil || !errors.Is(fatalErr, rebuildErr) {
		t.Fatalf("fatal error = %v, want rebuild failure", fatalErr)
	}
}

func TestLocalNode_ResetDatabaseDetachesReconciliationAfterCommit(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := newMemoryStore("reset-post-commit-cancel.db")
	t.Cleanup(func() { _ = base.Close() })
	st := &cancelAfterResetStore{Store: base}
	old := newFakeEngine()
	next := newFakeEngine()
	builds := 0
	nn, _, err := NewLocalNode(
		context.Background(), st,
		func(engine.Snapshot) (engine.Engine, error) {
			builds++
			if builds == 1 {
				return old, nil
			}
			return next, nil
		},
	)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n)
	st.cancel = cancel
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	if _, err := n.ResetDatabase(ctx, testCaller); err != nil {
		t.Fatalf("ResetDatabase after request cancellation: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("reset commit hook did not cancel the request context")
	}
	if fatalErr != nil {
		t.Fatalf("post-commit request cancellation triggered fatal path: %v", fatalErr)
	}
	if builds != 2 || n.currentEngine() != next || old.running {
		t.Fatalf(
			"reset reconciliation builds=%d engine=%T old_running=%v, want replacement",
			builds, n.currentEngine(), old.running,
		)
	}
}

func TestLocalNode_ResetDatabaseRecreatesStoreAndAudits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newMemoryStore("reset.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	oldEngine := newFakeEngine()
	nextEngine := newFakeEngine()
	builds := 0
	build := func(engine.Snapshot) (engine.Engine, error) {
		builds++
		if builds == 1 {
			return oldEngine, nil
		}
		return nextEngine, nil
	}
	nn, _, err := NewLocalNode(ctx, st, build)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n)
	if _, err := n.CreateAccount(ctx, testAccount("reset-me"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	sink, err := n.ResetDatabase(ctx, testCaller)
	if err != nil {
		t.Fatalf("ResetDatabase: %v", err)
	}
	if sink == nil {
		t.Fatalf("ResetDatabase returned nil sink")
	}
	// CreateAccount publishes into the live resolver. Only ResetDatabase replaces
	// the engine after the initial seed.
	if builds != 2 {
		t.Fatalf("build calls = %d, want 2", builds)
	}
	if oldEngine.running {
		t.Fatalf("old engine still running after reset")
	}
	if oldEngine.marketDataServiceCloseCalls != 1 {
		t.Fatalf(
			"old engine service closes = %d, want 1",
			oldEngine.marketDataServiceCloseCalls,
		)
	}
	if !nextEngine.running {
		t.Fatalf("next engine is not running after reset")
	}
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	accounts, err := realm.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts after reset = %+v, want none", accounts)
	}
	rows, err := realm.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 || rows[0].Action != domain.AuditActionResetDatabase {
		t.Fatalf("audit after reset = %+v, want reset row only", rows)
	}
	if rows[0].Actor != testCaller.Principal || rows[0].Source != testCaller.Source {
		t.Fatalf("audit attribution = %+v, want actor %s and source %s",
			rows[0], testCaller.Principal, testCaller.Source)
	}
}

type engineWithoutMarketDataServiceCloser struct {
	engine.Engine
}

func TestLocalNode_ResetDatabaseRequiresMarketDataServiceCloser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newMemoryStore("reset-missing-market-data-rotation.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	inner := newFakeEngine()
	var seed engine.Snapshot
	nn, _, err := NewLocalNode(ctx, st, func(snapshot engine.Snapshot) (engine.Engine, error) {
		built, buildErr := fakeBuild(inner, &seed)(snapshot)
		if buildErr != nil {
			return nil, buildErr
		}
		return engineWithoutMarketDataServiceCloser{Engine: built}, nil
	})
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n)
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	_, err = n.ResetDatabase(ctx, testCaller)
	var reconciliation internalPostCommitNodeMutationFailure
	if !errors.As(err, &reconciliation) {
		t.Fatalf("ResetDatabase error = %T %v, want reconciliation error", err, err)
	}
	const missingCapability = "missing CloseMarketDataService"
	if !strings.Contains(err.Error(), missingCapability) {
		t.Fatalf("ResetDatabase error = %q, want %q", err, missingCapability)
	}
	if fatalErr == nil || !strings.Contains(fatalErr.Error(), missingCapability) {
		t.Fatalf("fatal error = %v, want %q", fatalErr, missingCapability)
	}
}

func TestLocalNode_ConcurrentRestoreSwapAndReads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n, _ := newTestNode(t, newFakeEngine())
	probe := domain.OrderProbe{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := n.CheckOrder(ctx, testKey("acc-1"), probe); err != nil {
				errs <- err
				return
			}
			if _, err := n.Health(ctx); err != nil {
				errs <- err
				return
			}
			_ = n.EngineVersion()
		}
	}()
	for i := 0; i < 25; i++ {
		n.build = func(engine.Snapshot) (engine.Engine, error) {
			return newFakeEngine(), nil
		}
		archive := backup.NewArchive(
			backupTestTime().Add(time.Duration(i)*time.Second),
			"test",
			backup.RealmLabel{Code: string(domain.DefaultRealm)},
			backup.Scope{All: true},
			backup.Data{Accounts: []backup.Account{{
				Code: fmt.Sprintf("acc-%d", i),
			}}},
			backup.CredentialFormPlaintext,
		)
		if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
			Scope: backup.Scope{All: true},
			Mode:  backup.RestoreModeReplaceAll,
		}, testCaller); err != nil {
			close(stop)
			t.Fatalf("RestoreBackup #%d: %v", i, err)
		}
	}
	close(stop)
	<-done
	select {
	case err := <-errs:
		t.Fatalf("concurrent read error: %v", err)
	default:
	}
}

func backupTestTime() time.Time {
	return time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
}

func TestLocalNode_ErrorMessagesNoNodePrefix(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	// GetAccountState on a missing account must surface a not-found error whose
	// message does not start with "node: ".
	_, _, err := n.GetAccountState(ctx, testKey("no-such-account"))
	if err == nil {
		t.Fatal("want error for missing account, got nil")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	msg := err.Error()
	if len(msg) >= 6 && msg[:6] == "node: " {
		t.Fatalf("error message must not start with \"node: \", got: %s", msg)
	}

	// A rejecting SetAccountBlocked on a missing account must also not carry
	// "node: ".
	err = n.SetAccountBlocked(
		ctx, testKey("no-such-account"), true, "test",
		domain.MissingAccountReject, testCaller,
	)
	if err == nil {
		t.Fatal("want error for missing account on block, got nil")
	}
	if !errors.Is(err, domain.ErrAccountMissing) {
		t.Fatalf("want ErrAccountMissing, got %v", err)
	}
	msg = err.Error()
	if len(msg) >= 6 && msg[:6] == "node: " {
		t.Fatalf("error message must not start with \"node: \", got: %s", msg)
	}
}

// TestLocalNode_MissingAccountAdminRejectsWithResolver guards the pre-lane
// missing-account resolution in SetAccountBlocked and SetAccountGroup. The fake
// engine runs with enforceResolver=true, so its account-chain resolver rejects
// an unknown account with domain.ErrInvalid before the closure runs, mirroring
// the real adapter. The only way a rejecting request can still surface
// domain.ErrAccountMissing is the pre-lane realm check: remove it and the
// missing account would fall through to the resolver, regressing 404
// (ErrAccountMissing) to 400 (ErrInvalid).
func TestLocalNode_MissingAccountAdminRejectsWithResolver(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	// SetAccountBlocked on a missing account must be ErrAccountMissing (404),
	// not the resolver's ErrInvalid (400).
	err := n.SetAccountBlocked(
		ctx, testKey("no-such-account"), true, "risk",
		domain.MissingAccountReject, testCaller,
	)
	if err == nil {
		t.Fatal("SetAccountBlocked: want error for missing account, got nil")
	}
	if !errors.Is(err, domain.ErrAccountMissing) {
		t.Fatalf("SetAccountBlocked: want ErrAccountMissing, got %v", err)
	}

	// SetAccountGroup on a missing account must likewise be ErrAccountMissing.
	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	err = n.SetAccountGroup(
		ctx, testKey("no-such-account"), "desk-a",
		domain.MissingAccountReject, testCaller,
	)
	if err == nil {
		t.Fatal("SetAccountGroup: want error for missing account, got nil")
	}
	if !errors.Is(err, domain.ErrAccountMissing) {
		t.Fatalf("SetAccountGroup: want ErrAccountMissing, got %v", err)
	}
}

func restoredOrder(id domain.ExternalID, account domain.AccountID) domain.Order {
	return domain.Order{
		ExternalID:       id,
		Account:          account,
		BaseAsset:        "AAPL",
		QuoteAsset:       "USD",
		Side:             domain.OrderSideBuy,
		AmountKind:       domain.OrderAmountKindQuantity,
		AmountValue:      "2",
		Price:            "400",
		Leaves:           "2",
		ReservedQuantity: "2",
		Source:           domain.SourceAPI,
		Status:           domain.OrderStatusCommitted,
	}
}

func TestLocalNode_TerminalCancellationUsesRestoredLeaves(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)

	const account domain.AccountID = "acc-restored"
	orderID := externalID(t, "0123456789abcdefghjkmnpqrs")
	order := restoredOrder(orderID, account)
	if _, _, err := n.RestoreBackup(ctx, testArchive(backup.Scope{All: true}, backup.Data{
		Accounts: []backup.Account{{Code: string(account), Currency: "USD"}},
		Orders:   []backup.OrderRecord{{Order: order}},
	}), backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeOverwrite,
	}, testCaller); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if _, err := st.GetOrder(ctx, orderID); err != nil {
		t.Fatalf("GetOrder after restore: %v", err)
	}

	const leaves = "2"
	if _, err := n.ApplyExecutionReport(ctx, testKey(account), domain.ExecutionReportInput{
		Order:          orderID,
		LeavesQuantity: leaves,
		OrderStatus:    domain.OrderStatusCancelled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}

	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
	}
	if got := eng.execReportLeaves[0]; got != leaves {
		t.Fatalf("selected leaves = %q, want restored leaves %q", got, leaves)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionExecutionReport},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %+v, want one terminal settlement", rows)
	}
}

func TestLocalNode_TerminalCancellationUsesStoredZeroOverReportedLeaves(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)

	const account domain.AccountID = "acc-1"
	seedTestAccount(t, st, account)
	order, err := st.CreateOrder(ctx, domain.Order{
		Account:          account,
		Source:           domain.SourceAPI,
		BaseAsset:        "AAPL",
		QuoteAsset:       "USD",
		Side:             domain.OrderSideBuy,
		AmountKind:       domain.OrderAmountKindQuantity,
		AmountValue:      "2",
		Leaves:           "0",
		ReservedQuantity: "0",
		Price:            "400",
		Status:           domain.OrderStatusCommitted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	if _, err := n.ApplyExecutionReport(ctx, testKey(account), domain.ExecutionReportInput{
		Order:          order.ExternalID,
		LeavesQuantity: "2",
		OrderStatus:    domain.OrderStatusCancelled,
	}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}

	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
	}
	if got := eng.execReportLeaves[0]; got != "0" {
		t.Fatalf("selected leaves = %q, want stored leaves 0", got)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionExecutionReport},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %+v, want one terminal settlement", rows)
	}
}
