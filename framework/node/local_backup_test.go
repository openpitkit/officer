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

// testArchive builds a portable archive carrying data, labelled with the default
// realm, for the restore tests.
func testArchive(scope backup.Scope, data backup.Data) backup.Archive {
	return backup.NewArchive(
		backupTestTime(),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		scope,
		data,
	)
}

func TestLocalNode_RestoreBackupRebuildsEngineFromRestoredStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	n, st := newTestNode(t, oldEngine)
	nextEngine := newFakeEngine()
	var captured engine.Snapshot
	n.build = fakeBuild(nextEngine, &captured)
	archive := testArchive(
		backup.Scope{All: true},
		backup.Data{Accounts: []backup.Account{{Code: "restored"}}},
	)

	summary, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if !summary.RestartRequired {
		t.Fatalf("RestartRequired = false, want true")
	}
	if oldEngine.running {
		t.Fatalf("old engine still running after restore swap")
	}
	if !nextEngine.running {
		t.Fatalf("next engine not running after restore")
	}
	if len(captured.Accounts) != 1 || captured.Accounts[0].Code != "restored" {
		t.Fatalf("build snapshot accounts = %+v", captured.Accounts)
	}
	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if rows[0].Action != domain.AuditActionRestoreBackup {
		t.Fatalf("latest action = %q, want restore_backup", rows[0].Action)
	}
}

// TestLocalNode_RestoreBackupNonRuntimeScopeRebuildsForForceIncludedGroup proves
// a restore whose REQUESTED scope names only a non-runtime section (the audit
// log) still rebuilds the engine when the archive carries a group the local
// resolver has not seen. Normalize force-includes the accounts+groups dictionary
// so the archived group lands, and the store now raises RestartRequired from the
// rows actually written, so the node takes the exclusive restart gate and
// rebuilds. Against the old TouchesRuntime(opts.Scope) code the audit-only scope
// read as observational: no rebuild ran, the store held the new group but the
// live resolver did not, and the SetGroupBlocked below rejected the group it
// lists with ErrInvalid.
func TestLocalNode_RestoreBackupNonRuntimeScopeRebuildsForForceIncludedGroup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	n, _ := newTestNode(t, oldEngine)

	// A strict resolver rejects any group it has not seen in a build snapshot.
	// The rebuilt engine adopts the snapshot, so it learns the restored group.
	nextEngine := newFakeEngine()
	nextEngine.enforceResolver = true
	var captured engine.Snapshot
	n.build = fakeBuild(nextEngine, &captured)

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
	if !summary.RestartRequired {
		t.Fatalf("RestartRequired = false, want true for a force-included group write")
	}
	if oldEngine.running {
		t.Fatalf("old engine still running: rebuild did not swap the engine")
	}
	if _, ok := nextEngine.knownGroups["restored-desk"]; !ok {
		t.Fatalf("rebuilt resolver missing restored-desk: %+v", nextEngine.knownGroups)
	}

	// The store lists the group and, because the rebuild reconciled the resolver,
	// a group block into it succeeds instead of failing with ErrInvalid.
	if err := n.SetGroupBlocked(ctx, "restored-desk", true, "risk", testCaller); err != nil {
		t.Fatalf("SetGroupBlocked into restored group: %v", err)
	}
	if len(nextEngine.blockGroupCalls) != 1 ||
		nextEngine.blockGroupCalls[0].groupID != "restored-desk" {
		t.Fatalf("block group calls = %+v, want one for restored-desk",
			nextEngine.blockGroupCalls)
	}
}

func TestLocalNode_RestoreBackupRollsBackStoreOnRebuildFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldEngine := newFakeEngine()
	n, st := newTestNode(t, oldEngine)
	if _, err := n.CreateAccount(ctx, testAccount("keep"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, errors.New("build failed")
	}
	archive := testArchive(
		backup.Scope{All: true},
		backup.Data{Accounts: []backup.Account{{Code: "new"}}},
	)

	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller); err == nil {
		t.Fatal("RestoreBackup succeeded, want rebuild failure")
	}
	if !oldEngine.running {
		t.Fatalf("old engine stopped after failed rebuild")
	}
	// The pre-restore state is restored exactly: the rollback re-applies the
	// captured archive with replace-all, so the account that existed before the
	// failed restore is present and the account the failed forward restore
	// inserted is gone (replace-all deletes rows the rollback archive omits).
	if _, ok, err := st.GetAccount(ctx, "keep"); err != nil || !ok {
		t.Fatalf("keep account ok=%v err=%v, want present", ok, err)
	}
	if _, ok, err := st.GetAccount(ctx, "new"); err != nil || ok {
		t.Fatalf("new account ok=%v err=%v, want removed by rollback", ok, err)
	}
}

func TestLocalNode_RestoreBackupRollsBackStoreAndEngineOnAuditFailure(t *testing.T) {
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
	rollbackEngine := newFakeEngine()
	var buildCount int
	var rollbackSnapshot engine.Snapshot
	build := func(snap engine.Snapshot) (engine.Engine, error) {
		buildCount++
		switch buildCount {
		case 1:
			return oldEngine, nil
		case 2:
			return nextEngine, nil
		default:
			rollbackSnapshot = snap
			return rollbackEngine, nil
		}
	}
	nn, _, err := NewLocalNode(ctx, st, build)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n.realm)
	if _, err := n.CreateAccount(ctx, testAccount("keep"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	archive := testArchive(
		backup.Scope{All: true},
		backup.Data{Accounts: []backup.Account{{Code: "new"}}},
	)

	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller); err == nil {
		t.Fatal("RestoreBackup succeeded, want audit failure")
	}
	if nextEngine.running {
		t.Fatalf("restored engine still running after rollback")
	}
	if !rollbackEngine.running {
		t.Fatalf("rollback engine not running")
	}
	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	if _, ok, err := realm.GetAccount(ctx, "keep"); err != nil || !ok {
		t.Fatalf("keep account ok=%v err=%v, want present", ok, err)
	}
	// The replace-all rollback deletes the account the failed forward restore
	// inserted: it is not in the captured pre-restore archive.
	if _, ok, err := realm.GetAccount(ctx, "new"); err != nil || ok {
		t.Fatalf("new account ok=%v err=%v, want removed by rollback", ok, err)
	}
	// The rollback engine is rebuilt from the restored pre-restore snapshot, which
	// carries the account that existed before the failed restore.
	if len(rollbackSnapshot.Accounts) == 0 {
		t.Fatalf("rollback snapshot accounts = %+v, want the pre-restore account", rollbackSnapshot.Accounts)
	}
	found := false
	for _, a := range rollbackSnapshot.Accounts {
		if a.Code == "keep" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rollback snapshot accounts = %+v, want to include keep", rollbackSnapshot.Accounts)
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
	archive := testArchive(
		backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		backup.Data{McpAccess: map[string]bool{"submit_order": false}},
	)

	if _, _, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}},
		Mode:  backup.RestoreModeReplaceAll,
	}, testCaller); err == nil {
		t.Fatal("RestoreBackup succeeded, want audit failure")
	}
	if !oldEngine.running {
		t.Fatalf("engine stopped for non-runtime rollback")
	}
	access, err := realm0.ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	if access["submit_order"] != true {
		t.Fatalf("mcp access = %+v, want rollback to true", access)
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
	seedTestPrincipal(t, n.realm)
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
	// Three builds: the initial seed, the rebuild CreateAccount triggers so the
	// new account enters the resolver, and the rebuild ResetDatabase performs.
	if builds != 3 {
		t.Fatalf("build calls = %d, want 3", builds)
	}
	if oldEngine.running {
		t.Fatalf("old engine still running after reset")
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

	// SetAccountBlocked on a missing account must also not carry "node: ".
	err = n.SetAccountBlocked(ctx, testKey("no-such-account"), true, "test", testCaller)
	if err == nil {
		t.Fatal("want error for missing account on block, got nil")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	msg = err.Error()
	if len(msg) >= 6 && msg[:6] == "node: " {
		t.Fatalf("error message must not start with \"node: \", got: %s", msg)
	}
}

// TestLocalNode_MissingAccountAdminNotFoundWithResolver guards the pre-lane
// existence check in SetAccountBlocked and SetAccountGroup. The fake engine runs
// with enforceResolver=true, so its RunAccountSynchronized rejects an unknown
// account with domain.ErrInvalid before the closure runs, mirroring the real
// adapter. The only way these methods can still surface domain.ErrNotFound is
// the pre-lane realm existence check: remove it and the missing account would
// fall through to the resolver, regressing 404 (ErrNotFound) to 400 (ErrInvalid).
func TestLocalNode_MissingAccountAdminNotFoundWithResolver(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	// SetAccountBlocked on a missing account must be ErrNotFound (404), not the
	// resolver's ErrInvalid (400).
	err := n.SetAccountBlocked(ctx, testKey("no-such-account"), true, "risk", testCaller)
	if err == nil {
		t.Fatal("SetAccountBlocked: want error for missing account, got nil")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetAccountBlocked: want ErrNotFound, got %v", err)
	}

	// SetAccountGroup on a missing account must likewise be ErrNotFound (404).
	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	err = n.SetAccountGroup(ctx, testKey("no-such-account"), "desk-a", testCaller)
	if err == nil {
		t.Fatal("SetAccountGroup: want error for missing account, got nil")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetAccountGroup: want ErrNotFound, got %v", err)
	}
}
