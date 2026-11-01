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
	"reflect"
	"strings"
	"sync"
	"testing"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

func TestLocalNode_RestoreBackupSpotFundsLimitMode(t *testing.T) {
	t.Parallel()
	settings := backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}}
	for _, test := range []struct {
		name     string
		before   string
		restored string
		scope    backup.Scope
		mode     backup.RestoreMode
		calls    []bool
	}{
		{
			name: "settings track only to enforce", before: "track_only",
			restored: "enforce", scope: settings,
			mode: backup.RestoreModeOverwrite, calls: []bool{false},
		},
		{
			name: "settings absent to track only", restored: "track_only",
			scope: settings, mode: backup.RestoreModeOverwrite, calls: []bool{true},
		},
		{
			name: "replace prunes mode to enforce", before: "track_only",
			scope: settings, mode: backup.RestoreModeReplaceAll, calls: []bool{false},
		},
		{
			name: "unchanged track only", before: "track_only", restored: "track_only",
			scope: settings, mode: backup.RestoreModeOverwrite,
		},
		{
			name: "absent to explicit enforce", restored: "enforce",
			scope: settings, mode: backup.RestoreModeOverwrite,
		},
		{
			name: "runtime applies online", restored: "track_only",
			scope: backup.Scope{All: true}, mode: backup.RestoreModeOverwrite,
			calls: []bool{true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			st, realm := newSpotFundsLimitModeStore(t, test.before, test.before != "")
			eng := newFakeEngine()
			n := newTestNodeWithStore(t, st, eng, failOnFatal(t))
			n.build = func(engine.Snapshot) (engine.Engine, error) {
				t.Fatal("mode-only restore unexpectedly rebuilt the engine")
				return nil, nil
			}
			data := backup.Data{}
			if test.restored != "" {
				data.Settings = []backup.SettingEntry{{
					Key: domain.SettingKeySpotFundsLimitMode, Value: test.restored,
				}}
			}
			summary, sink, err := n.RestoreBackup(
				ctx, testArchive(test.scope, data),
				backup.RestoreOptions{Scope: test.scope, Mode: test.mode}, testCaller,
			)
			if err != nil {
				t.Fatalf("RestoreBackup: %v", err)
			}
			if summary.RestartRequired || n.currentEngine() != eng || sink != eng.MarketDataSink() {
				t.Fatal("mode-only restore replaced the engine or sink")
			}
			if !reflect.DeepEqual(eng.spotFundsTrackOnlyCalls, test.calls) {
				t.Fatalf("engine mode calls = %v, want %v", eng.spotFundsTrackOnlyCalls, test.calls)
			}
			requireStoredSpotFundsLimitMode(t, realm, test.restored, test.restored != "")
			want := domain.SpotFundsLimitMode(test.restored)
			if want == "" {
				want = domain.SpotFundsLimitModeEnforce
			}
			state, err := n.SpotFundsLimitMode(ctx)
			if err != nil || state.Mode != want {
				t.Fatalf("SpotFundsLimitMode = %q, %v; want %q, nil", state.Mode, err, want)
			}
		})
	}
}

func TestLocalNode_RestoreBackupSpotFundsLimitModeWaitsForActiveLane(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, realm := newSpotFundsLimitModeStore(t, "", false)
	eng := &restoreSpotFundsModeLaneEngine{
		fakeEngine: newFakeEngine(),
		calls:      make(chan bool, 1),
	}
	n := newTestNodeWithStore(t, st, eng.fakeEngine, failOnFatal(t))
	n.engineMu.Lock()
	n.engine = eng
	n.engineMu.Unlock()
	_, endLane, err := n.beginLane()
	if err != nil {
		t.Fatalf("beginLane: %v", err)
	}
	laneEnded := false
	defer func() {
		if !laneEnded {
			endLane()
		}
	}()

	scope := backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}}
	archive := testArchive(scope, backup.Data{Settings: []backup.SettingEntry{{
		Key: domain.SettingKeySpotFundsLimitMode, Value: "track_only",
	}}})
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
			Scope: scope, Mode: backup.RestoreModeOverwrite,
		}, testCaller)
		done <- err
	}()
	<-started
	assertLivePolicyBlocked(t, eng.calls, "spot funds limit mode applied before active lane left")
	assertLivePolicyBlocked(t, done, "settings restore returned before active lane left")
	if n.restarting.Load() {
		t.Fatal("settings restore reserved an engine restart")
	}

	endLane()
	laneEnded = true
	if err := waitLivePolicyResult(t, done, "settings restore did not finish after lane left"); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	select {
	case trackOnly := <-eng.calls:
		if !trackOnly {
			t.Fatal("restored mode did not enable track only")
		}
	default:
		t.Fatal("restored mode was not applied after active lane left")
	}
	requireStoredSpotFundsLimitMode(t, realm, "track_only", true)
}

type restoreSpotFundsModeLaneEngine struct {
	*fakeEngine
	calls chan bool
}

func (e *restoreSpotFundsModeLaneEngine) SetSpotFundsTrackOnly(ctx context.Context, trackOnly bool) error {
	e.calls <- trackOnly
	return e.fakeEngine.SetSpotFundsTrackOnly(ctx, trackOnly)
}

func TestLocalNode_RestoreBackupSpotFundsLimitModeSerializesAssetClassificationWithDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, realm := newTestNode(t, eng)
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
	n.realm = &assetClassificationProbeRealm{
		RealmStore: realm, classified: classified, release: release,
	}
	scope := backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}}
	archive := testArchive(scope, backup.Data{Assets: []backup.Asset{{Code: asset.Code}}})
	restored := make(chan error, 1)
	go func() {
		_, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
			Scope: scope, Mode: backup.RestoreModeInsertMissing,
		}, testCaller)
		restored <- err
	}()
	waitLivePolicySignal(t, classified, "settings restore did not reach asset classification")
	deleted := make(chan error, 1)
	go func() {
		deleted <- n.DeleteAsset(ctx, asset.Code, false, testCaller)
	}()
	assertLivePolicyBlocked(t, deleted, "DeleteAsset passed the gate during restore asset classification")
	releaseClassification()
	if err := waitLivePolicyResult(t, restored, "settings restore did not finish after classification"); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if err := waitLivePolicyResult(t, deleted, "DeleteAsset did not finish after settings restore"); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}

	_, stored, err := realm.GetAsset(ctx, asset.Code)
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

func TestLocalNode_RestoreBackupSpotFundsLimitModeAssetAdditionReservesRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n, realm := newTestNode(t, newFakeEngine())
	restartingAtRestore := false
	probe := &restoreSpotFundsModeReadRealm{
		RealmStore: realm,
		onRestore:  func() { restartingAtRestore = n.restarting.Load() },
	}
	n.realm = probe
	scope := backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}}
	_, _, err := n.RestoreBackup(
		ctx, testArchive(scope, backup.Data{Assets: []backup.Asset{{Code: "GOLD"}}}),
		backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeInsertMissing}, testCaller,
	)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if probe.restores != 1 {
		t.Fatalf("store restore calls = %d, want 1", probe.restores)
	}
	if !restartingAtRestore {
		t.Error("settings restore adding an asset did not reserve restarting during store restore")
	}
	if n.restarting.Load() {
		t.Error("settings restore left restarting reserved after return")
	}
}

func TestLocalNode_RestoreBackupSpotFundsLimitModeAssetAdditionRejectsReservedRestart(t *testing.T) {
	t.Parallel()
	for _, reserveBeforeCall := range []bool{true, false} {
		name := "during classification"
		if reserveBeforeCall {
			name = "before call"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			n, realm := newTestNode(t, newFakeEngine())
			probe := &restoreSpotFundsModeReadRealm{RealmStore: realm}
			n.realm = probe
			classified := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseClassification := func() {
				releaseOnce.Do(func() { close(release) })
			}
			t.Cleanup(releaseClassification)
			if reserveBeforeCall {
				n.restarting.Store(true)
			} else {
				n.realm = &assetClassificationProbeRealm{
					RealmStore: probe, classified: classified, release: release,
				}
			}
			defer n.restarting.Store(false)
			scope := backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}}
			restored := make(chan error, 1)
			go func() {
				_, _, err := n.RestoreBackup(
					ctx, testArchive(scope, backup.Data{Assets: []backup.Asset{{Code: "GOLD"}}}),
					backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeInsertMissing}, testCaller,
				)
				restored <- err
			}()
			if !reserveBeforeCall {
				waitLivePolicySignal(t, classified, "settings restore did not reach asset classification")
				// The initial gate rejects an earlier reservation. Reserve here to
				// exercise the failed in-place upgrade while both gates are held.
				n.restarting.Store(true)
				releaseClassification()
			}
			err := waitLivePolicyResult(t, restored, "settings restore did not reject reserved restart")
			if !errors.Is(err, domain.ErrEngineRestarting) {
				t.Fatalf("RestoreBackup error = %v, want ErrEngineRestarting", err)
			}
			if probe.restores != 0 {
				t.Fatalf("store restore calls = %d, want none after restart rejection", probe.restores)
			}
			if n.laneGate.TryLock() {
				n.laneGate.Unlock()
			} else {
				t.Error("settings restore leaked laneGate after restart rejection")
			}
			if n.mutate.TryLock() {
				n.mutate.Unlock()
			} else {
				t.Error("settings restore leaked mutate after restart rejection")
			}
		})
	}
}

func TestLocalNode_RestoreBackupSpotFundsLimitModeRebuild(t *testing.T) {
	t.Parallel()
	for _, restored := range []string{"enforce", "track_only"} {
		t.Run(restored, func(t *testing.T) {
			ctx := context.Background()
			before := "enforce"
			if restored == "enforce" {
				before = "track_only"
			}
			st, realm := newSpotFundsLimitModeStore(t, before, true)
			old := newFakeEngine()
			n := newTestNodeWithStore(t, st, old, failOnFatal(t))
			if _, err := n.CreateAccount(ctx, testAccount("delete-me"), testCaller); err != nil {
				t.Fatalf("CreateAccount: %v", err)
			}
			next := newFakeEngine()
			var snapshot engine.Snapshot
			builds := 0
			n.build = func(snap engine.Snapshot) (engine.Engine, error) {
				builds++
				return fakeBuild(next, &snapshot)(snap)
			}
			scope := backup.Scope{All: true}
			summary, sink, err := n.RestoreBackup(
				ctx, testArchive(scope, backup.Data{Settings: []backup.SettingEntry{{
					Key: domain.SettingKeySpotFundsLimitMode, Value: restored,
				}}}),
				backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeReplaceAll},
				testCaller,
			)
			if err != nil {
				t.Fatalf("RestoreBackup: %v", err)
			}
			if !summary.RestartRequired || builds != 1 || old.running || n.currentEngine() != next || sink != next.MarketDataSink() {
				t.Fatalf("account-deleting restore summary=%+v builds=%d, want one engine swap", summary, builds)
			}
			if len(old.spotFundsTrackOnlyCalls) != 0 || len(next.spotFundsTrackOnlyCalls) != 0 {
				t.Fatalf("rebuild made online mode calls: old=%v next=%v, want none", old.spotFundsTrackOnlyCalls, next.spotFundsTrackOnlyCalls)
			}
			if snapshot.SpotFundsTrackOnly != (restored == "track_only") {
				t.Fatalf("rebuild snapshot track only = %v, want mode %q", snapshot.SpotFundsTrackOnly, restored)
			}
			requireStoredSpotFundsLimitMode(t, realm, restored, true)
		})
	}
}

func TestLocalNode_RestoreBackupSpotFundsLimitModeApplyFailure(t *testing.T) {
	t.Parallel()
	for _, runtimeScope := range []bool{false, true} {
		scope := backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}}
		name := "settings rollback"
		if runtimeScope {
			scope = backup.Scope{All: true}
			name = "runtime fatal"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st, realm := newSpotFundsLimitModeStore(t, "track_only", true)
			eng := newFakeEngine()
			n := newTestNodeWithStore(t, st, eng, failOnFatal(t))
			applyErr := errors.New("mode apply failed")
			eng.spotFundsTrackOnlyErr = applyErr
			var fatalErr error
			n.fatal = func(err error) { fatalErr = err }
			_, _, err := n.RestoreBackup(
				ctx, testArchive(scope, backup.Data{Settings: []backup.SettingEntry{{
					Key: domain.SettingKeySpotFundsLimitMode, Value: "enforce",
				}}}),
				backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeOverwrite}, testCaller,
			)
			if !errors.Is(err, applyErr) {
				t.Fatalf("RestoreBackup error = %v, want mode apply failure", err)
			}
			if runtimeScope {
				if !errors.Is(fatalErr, applyErr) || !strings.Contains(fatalErr.Error(), "spot funds limit mode") {
					t.Fatalf("fatal error = %v, want mode apply failure with mode context", fatalErr)
				}
				requireStoredSpotFundsLimitMode(t, realm, "enforce", true)
			} else {
				if fatalErr != nil {
					t.Fatalf("fatal error = %v, want settings-only rollback", fatalErr)
				}
				requireStoredSpotFundsLimitMode(t, realm, "track_only", true)
			}
		})
	}
}

type restoreSpotFundsModeReadRealm struct {
	store.RealmStore
	readErr   error
	failRead  int
	reads     int
	restores  int
	onRestore func()
}

func (r *restoreSpotFundsModeReadRealm) GetSetting(ctx context.Context, key string) (string, bool, error) {
	if key == domain.SettingKeySpotFundsLimitMode {
		r.reads++
		if r.reads == r.failRead {
			return "", false, r.readErr
		}
	}
	return r.RealmStore.GetSetting(ctx, key)
}

func (r *restoreSpotFundsModeReadRealm) RestoreBackup(
	ctx context.Context, archive backup.Archive, opts backup.RestoreOptions,
) (backup.RestoreSummary, error) {
	r.restores++
	if r.onRestore != nil {
		r.onRestore()
	}
	return r.RealmStore.RestoreBackup(ctx, archive, opts)
}

func TestLocalNode_RestoreBackupSpotFundsLimitModeReadFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		failRead int
		runtime  bool
	}{
		{name: "before settings restore", failRead: 1},
		{name: "before runtime restore", failRead: 1, runtime: true},
		{name: "after settings restore", failRead: 2},
		{name: "after runtime restore", failRead: 2, runtime: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			eng := newFakeEngine()
			n, realm := newTestNode(t, eng)
			readErr := errors.New("mode read failed")
			probe := &restoreSpotFundsModeReadRealm{
				RealmStore: realm, readErr: readErr, failRead: test.failRead,
			}
			n.realm = probe
			var fatalErr error
			n.fatal = func(err error) { fatalErr = err }
			scope := backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}}
			if test.runtime {
				scope = backup.Scope{All: true}
			}
			_, _, err := n.RestoreBackup(
				ctx, testArchive(scope, backup.Data{Settings: []backup.SettingEntry{{
					Key: domain.SettingKeySpotFundsLimitMode, Value: "track_only",
				}}}),
				backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeOverwrite}, testCaller,
			)
			if !errors.Is(err, readErr) {
				t.Fatalf("RestoreBackup error = %v, want mode read failure", err)
			}
			if test.failRead == 2 && test.runtime {
				if !errors.Is(fatalErr, readErr) {
					t.Fatalf("fatal error = %v, want committed mode read failure", fatalErr)
				}
				requireStoredSpotFundsLimitMode(t, realm, "track_only", true)
			} else {
				if fatalErr != nil {
					t.Fatalf("fatal error = %v, want no fatal reconciliation", fatalErr)
				}
				requireStoredSpotFundsLimitMode(t, realm, "", false)
			}
			if test.failRead == 1 && probe.restores != 0 {
				t.Fatalf("store restore calls = %d, want none after pre-read failure", probe.restores)
			}
			if len(eng.spotFundsTrackOnlyCalls) != 0 {
				t.Fatalf("engine mode calls = %v, want none after read failure", eng.spotFundsTrackOnlyCalls)
			}
		})
	}
}

func TestLocalNode_RestoreBackupSpotFundsLimitModeAuditFailure(t *testing.T) {
	t.Parallel()
	for _, runtimeScope := range []bool{false, true} {
		name := "settings rollback"
		scope := backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}}
		if runtimeScope {
			name = "runtime fatal"
			scope = backup.Scope{All: true}
		}
		t.Run(name, func(t *testing.T) {
			st, realm := newSpotFundsLimitModeStore(t, "", false)
			eng := newFakeEngine()
			n := newTestNodeWithStore(t, st, eng, failOnFatal(t))
			n.realm = &failRestoreAuditRealm{RealmStore: realm}
			var fatalErr error
			n.fatal = func(err error) { fatalErr = err }
			_, _, err := n.RestoreBackup(
				context.Background(), testArchive(scope, backup.Data{Settings: []backup.SettingEntry{{
					Key: domain.SettingKeySpotFundsLimitMode, Value: "track_only",
				}}}),
				backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeOverwrite}, testCaller,
			)
			if !errors.Is(err, errRestoreAuditFailed) {
				t.Fatalf("RestoreBackup error = %v, want audit failure", err)
			}
			wantCalls := []bool{true, false}
			if runtimeScope {
				if !errors.Is(fatalErr, errRestoreAuditFailed) {
					t.Fatalf("fatal error = %v, want committed audit failure", fatalErr)
				}
				requireStoredSpotFundsLimitMode(t, realm, "track_only", true)
				wantCalls = []bool{true}
			} else {
				if fatalErr != nil {
					t.Fatalf("fatal error = %v, want settings-only rollback", fatalErr)
				}
				requireStoredSpotFundsLimitMode(t, realm, "", false)
			}
			if !reflect.DeepEqual(eng.spotFundsTrackOnlyCalls, wantCalls) {
				t.Fatalf("engine mode calls after audit failure = %v, want %v", eng.spotFundsTrackOnlyCalls, wantCalls)
			}
		})
	}
}

type restoreSpotFundsModeAuditRealm struct {
	*failRestoreAuditRealm
	engine      *fakeEngine
	rollbackErr error
}

func (r *restoreSpotFundsModeAuditRealm) AppendAudit(ctx context.Context, entry store.AuditEntry) error {
	if entry.Action == domain.AuditActionRestoreBackup {
		r.engine.spotFundsTrackOnlyErr = r.rollbackErr
	}
	return r.failRestoreAuditRealm.AppendAudit(ctx, entry)
}

func TestLocalNode_RestoreBackupSpotFundsLimitModeRollbackFailureFatals(t *testing.T) {
	t.Parallel()
	st, realm := newSpotFundsLimitModeStore(t, "", false)
	eng := newFakeEngine()
	n := newTestNodeWithStore(t, st, eng, failOnFatal(t))
	rollbackErr := errors.New("mode rollback failed")
	n.realm = &restoreSpotFundsModeAuditRealm{
		failRestoreAuditRealm: &failRestoreAuditRealm{RealmStore: realm},
		engine:                eng,
		rollbackErr:           rollbackErr,
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }
	scope := backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}}
	_, _, err := n.RestoreBackup(
		context.Background(), testArchive(scope, backup.Data{Settings: []backup.SettingEntry{{
			Key: domain.SettingKeySpotFundsLimitMode, Value: "track_only",
		}}}),
		backup.RestoreOptions{Scope: scope, Mode: backup.RestoreModeOverwrite}, testCaller,
	)
	if !errors.Is(err, errRestoreAuditFailed) || !errors.Is(err, rollbackErr) {
		t.Fatalf("RestoreBackup error = %v, want audit and mode rollback failures", err)
	}
	if !errors.Is(fatalErr, rollbackErr) {
		t.Fatalf("fatal error = %v, want mode rollback failure", fatalErr)
	}
	requireStoredSpotFundsLimitMode(t, realm, "track_only", true)
	if !reflect.DeepEqual(eng.spotFundsTrackOnlyCalls, []bool{true}) {
		t.Fatalf("engine mode calls = %v, want [true] before fatal rollback", eng.spotFundsTrackOnlyCalls)
	}
}
