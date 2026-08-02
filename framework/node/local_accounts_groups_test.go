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
	"slices"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

type failAccountBlockRevertRealm struct {
	store.RealmStore
	revertErr error
	calls     int
}

func (r *failAccountBlockRevertRealm) SetAccountBlocked(
	ctx context.Context,
	id domain.AccountID,
	blocked bool,
	reason string,
) error {
	r.calls++
	if r.calls == 2 {
		return r.revertErr
	}
	return r.RealmStore.SetAccountBlocked(ctx, id, blocked, reason)
}

func TestLocalNode_BlockUnblockAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("block: %v", err)
	}
	if len(eng.blockCalls) != 1 || eng.blockCalls[0].reason != "risk" {
		t.Fatalf("engine block not applied: %+v", eng.blockCalls)
	}

	account, _, err := n.GetAccountState(ctx, testKey(id))
	if err != nil {
		t.Fatalf("GetAccountState: %v", err)
	}
	if !account.Blocked || account.BlockReason != "risk" {
		t.Fatalf("account not blocked in store: %+v", account)
	}

	if err := n.SetAccountBlocked(ctx, testKey(id), false, "", domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	if len(eng.unblockCalls) != 1 {
		t.Fatalf("engine unblock not applied: %+v", eng.unblockCalls)
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// startup hydrate, create_account, block, unblock = 4 rows, newest first.
	if len(rows) != 4 || rows[0].Action != domain.AuditActionUnblock {
		t.Fatalf("unexpected audit trail: %+v", rows)
	}
}

// TestLocalNode_SetAccountBlockedUsesAccountLane proves the engine block/unblock
// runs through the account lane (so it serializes against fills and the
// execution-report kill-switch on the same account), not just under the global
// mutation lock.
func TestLocalNode_SetAccountBlockedUsesAccountLane(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("block: %v", err)
	}
	if len(eng.blockCalls) != 1 {
		t.Fatalf("engine block not applied: %+v", eng.blockCalls)
	}
	if len(eng.accountSyncCalls) == 0 || eng.accountSyncCalls[len(eng.accountSyncCalls)-1] != id {
		t.Fatalf("account sync calls = %+v, want last %s (block routed through lane)",
			eng.accountSyncCalls, id)
	}
}

// TestLocalNode_SetAccountGroupUsesGroupLane proves the engine group move runs
// on the target group's synchronized lane while the store write remains inside
// the account lane.
func TestLocalNode_SetAccountGroupUsesGroupLane(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := n.SetAccountGroup(ctx, testKey(id), "desk-a", domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("SetAccountGroup: %v", err)
	}
	if len(eng.registerGroupCalls) != 1 {
		t.Fatalf("register group calls = %+v, want one", eng.registerGroupCalls)
	}
	if len(eng.accountSyncCalls) == 0 || eng.accountSyncCalls[len(eng.accountSyncCalls)-1] != id {
		t.Fatalf("account sync calls = %+v, want last %s", eng.accountSyncCalls, id)
	}
	if len(eng.groupSyncCalls) == 0 || eng.groupSyncCalls[len(eng.groupSyncCalls)-1] != "desk-a" {
		t.Fatalf("group sync calls = %+v, want last desk-a", eng.groupSyncCalls)
	}
}

// TestLocalNode_SetAccountGroupAutoCreatesUnknownGroup proves the auto-create
// contract against a strict resolver: moving an account into a group with no
// account_groups record succeeds because the missing record is created and the
// engine rebuilt from the store before the lane, so the in-lane RegisterGroup
// resolves the now-known group. Against the old in-lane-create-no-rebuild
// implementation the account lane's RegisterGroup resolved the brand-new group
// before it was registered and rejected it with ErrInvalid, so the move failed.
func TestLocalNode_SetAccountGroupAutoCreatesUnknownGroup(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	// The strict resolver rejects any group it has not seen in a build snapshot.
	// "new-desk" has never been created, so it is absent from the resolver.
	eng.enforceResolver = true

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := n.SetAccountGroup(ctx, testKey(id), "new-desk", domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("SetAccountGroup into unknown group: %v", err)
	}

	if len(eng.registerGroupCalls) != 1 ||
		eng.registerGroupCalls[0].groupID != "new-desk" ||
		!slices.Equal(eng.registerGroupCalls[0].accounts, []domain.AccountID{id}) {
		t.Fatalf("register group calls = %+v, want one for new-desk/%s",
			eng.registerGroupCalls, id)
	}

	account, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: %v ok=%v", err, ok)
	}
	if account.GroupCode != "new-desk" {
		t.Fatalf("account group = %q, want new-desk", account.GroupCode)
	}
	group, ok, err := st.GetGroup(ctx, "new-desk")
	if err != nil || !ok {
		t.Fatalf("GetGroup: %v ok=%v", err, ok)
	}
	if group.Code != "new-desk" {
		t.Fatalf("group code = %q, want new-desk", group.Code)
	}
}

// TestLocalNode_SetGroupBlockedRunsUnderLiveIdentityGate proves the group block
// keeps the live engine while fencing concurrent identity publications until
// the async-engine group lane finishes.
func TestLocalNode_SetGroupBlockedRunsUnderLiveIdentityGate(t *testing.T) {
	t.Parallel()
	entered := make(chan string, 1)
	release := make(chan struct{})
	eng := newFakeEngine()
	eng.blockGroupEntered = entered
	eng.blockGroupRelease = release
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	errs := make(chan error, 1)
	go func() { errs <- n.SetGroupBlocked(ctx, "desk-a", true, "risk", testCaller) }()

	select {
	case got := <-entered:
		if got != "desk-a" {
			t.Fatalf("entered group = %s, want desk-a", got)
		}
	case <-time.After(time.Second):
		t.Fatal("SetGroupBlocked did not reach the engine block")
	}

	created := make(chan error, 1)
	go func() {
		_, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-b"}, testCaller)
		created <- err
	}()
	select {
	case err := <-created:
		t.Fatalf("concurrent CreateGroup completed before group block: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	if err := <-errs; err != nil {
		t.Fatalf("SetGroupBlocked: %v", err)
	}
	if err := <-created; err != nil {
		t.Fatalf("concurrent CreateGroup after group block: %v", err)
	}
	if len(eng.blockGroupCalls) != 1 {
		t.Fatalf("block group calls = %+v, want one", eng.blockGroupCalls)
	}
	if !slices.Equal(eng.groupSyncCalls, []string{"desk-a"}) {
		t.Fatalf("group sync calls = %+v, want desk-a", eng.groupSyncCalls)
	}
}

// TestLocalNode_SetGroupBlockedAutoCreatesUnknownGroup proves the auto-create
// contract against a strict resolver: a group with no account_groups record is
// unknown to the engine resolver, yet SetGroupBlocked succeeds because the
// missing record is created and the engine rebuilt from the store before the
// block runs. Against the old in-lane implementation the group-synchronized
// call resolved the group before the callback and rejected the unknown group
// with ErrInvalid, so the store record was never created and the block failed.
func TestLocalNode_SetGroupBlockedAutoCreatesUnknownGroup(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	// The strict resolver rejects any group it has not seen in a build snapshot.
	// "new-desk" has never been created, so it is absent from the resolver.
	eng.enforceResolver = true

	if err := n.SetGroupBlocked(ctx, "new-desk", true, "risk", testCaller); err != nil {
		t.Fatalf("SetGroupBlocked on unknown group: %v", err)
	}

	group, ok, err := st.GetGroup(ctx, "new-desk")
	if err != nil || !ok {
		t.Fatalf("GetGroup: %v ok=%v", err, ok)
	}
	if !group.Blocked {
		t.Fatalf("store not blocked after auto-create block")
	}
	if len(eng.blockGroupCalls) != 1 || eng.blockGroupCalls[0].groupID != "new-desk" {
		t.Fatalf("block group calls = %+v, want one for new-desk", eng.blockGroupCalls)
	}
}

// TestLocalNode_SetAccountBlockedAuditFailureFatals proves the account-block
// path joins the post-engine fail-stop: once the engine block and the store
// blocked-state write committed inside the account lane, a failing audit write
// routes to the fatal hook. The diagnostic must name the account CODE, never the
// engine surrogate (account_id), so operators reading the fatal log see the same
// identifier the audit row stores.
func TestLocalNode_SetAccountBlockedAuditFailureFatals(t *testing.T) {
	t.Parallel()
	auditErr := errors.New("account block audit failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failActionAuditRealm{
			RealmStore: r, action: domain.AuditActionBlock, err: auditErr,
		}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, auditErr) {
		t.Fatalf("SetAccountBlocked error = %v, want audit failure", err)
	}
	// Engine block applied before the audit write failed.
	if len(eng.blockCalls) != 1 {
		t.Fatalf("engine block calls = %+v, want one", eng.blockCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine account-block audit failure")
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="audit account block"`) ||
		!strings.Contains(msg, "account=acc-1") ||
		!strings.Contains(msg, "account block audit failed") {
		t.Fatalf("fatal error = %q, want operation, account code, and cause", msg)
	}
	if strings.Contains(msg, "account_id=") {
		t.Fatalf("fatal error = %q, must not leak the engine surrogate", msg)
	}
}

// TestLocalNode_SetAccountGroupAuditFailureFatals proves the account set-group
// path joins the post-engine fail-stop with the same code-not-surrogate
// diagnostic: the engine group move and the store link write committed inside
// the lane, so a failing audit write routes to the fatal hook naming the account
// code.
func TestLocalNode_SetAccountGroupAuditFailureFatals(t *testing.T) {
	t.Parallel()
	auditErr := errors.New("set group audit failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failActionAuditRealm{
			RealmStore: r, action: domain.AuditActionSetGroup, err: auditErr,
		}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	err := n.SetAccountGroup(ctx, testKey(id), "desk-a", domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, auditErr) {
		t.Fatalf("SetAccountGroup error = %v, want audit failure", err)
	}
	// Engine group move applied before the audit write failed.
	if len(eng.registerGroupCalls) != 1 {
		t.Fatalf("register group calls = %+v, want one", eng.registerGroupCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine set-group audit failure")
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="audit set account group"`) ||
		!strings.Contains(msg, "account=acc-1") ||
		!strings.Contains(msg, "set group audit failed") {
		t.Fatalf("fatal error = %q, want operation, account code, and cause", msg)
	}
	if strings.Contains(msg, "account_id=") {
		t.Fatalf("fatal error = %q, must not leak the engine surrogate", msg)
	}
}

// TestLocalNode_SetGroupBlockedAuditFailureFatals proves the group-block path
// joins the post-engine fail-stop. A group has no account, so the diagnostic
// names the group CODE. The engine group block and the store blocked-state write
// committed under the restart gate, so a failing audit write routes to the fatal
// hook.
func TestLocalNode_SetGroupBlockedAuditFailureFatals(t *testing.T) {
	t.Parallel()
	auditErr := errors.New("group block audit failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failActionAuditRealm{
			RealmStore: r, action: domain.AuditActionBlockGroup, err: auditErr,
		}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	err := n.SetGroupBlocked(ctx, "desk-a", true, "risk", testCaller)
	if !errors.Is(err, auditErr) {
		t.Fatalf("SetGroupBlocked error = %v, want audit failure", err)
	}
	// Engine group block applied before the audit write failed.
	if len(eng.blockGroupCalls) != 1 || eng.blockGroupCalls[0].groupID != "desk-a" {
		t.Fatalf("block group calls = %+v, want one for desk-a", eng.blockGroupCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine group-block audit failure")
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="audit group block"`) ||
		!strings.Contains(msg, "group=desk-a") ||
		!strings.Contains(msg, "group block audit failed") {
		t.Fatalf("fatal error = %q, want operation, group code, and cause", msg)
	}
	if strings.Contains(msg, "account_id=") || strings.Contains(msg, "account=") {
		t.Fatalf("fatal error = %q, must not leak a surrogate or account id", msg)
	}
}

// TestLocalNode_SetGroupNotesAutoCreatesRegisteredGroup proves that setting
// notes on a brand-new group registers it in the engine, not just the store:
// after SetGroupNotes auto-creates "new-desk" a later SetAccountGroup moving an
// account into it succeeds because the strict resolver knows the group.
// SetGroupNotes rebuilt the engine from the store when it created the record.
// Against the old no-rebuild SetGroupNotes the store row existed but the live
// resolver never learned the group, so the in-lane RegisterGroup rejected it
// with ErrInvalid and the account move failed.
func TestLocalNode_SetGroupNotesAutoCreatesRegisteredGroup(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	// The strict resolver rejects any group it has not seen in a build snapshot.
	// "new-desk" has never been created, so it is absent from the resolver.
	eng.enforceResolver = true

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := n.SetGroupNotes(ctx, "new-desk", "vip desk", testCaller); err != nil {
		t.Fatalf("SetGroupNotes on unknown group: %v", err)
	}

	group, ok, err := st.GetGroup(ctx, "new-desk")
	if err != nil || !ok {
		t.Fatalf("GetGroup: %v ok=%v", err, ok)
	}
	if group.Notes != "vip desk" {
		t.Fatalf("group notes = %q, want vip desk", group.Notes)
	}

	// The group is now engine-registered, so moving an account into it succeeds
	// against the strict resolver.
	if err := n.SetAccountGroup(ctx, testKey(id), "new-desk", domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("SetAccountGroup into notes-created group: %v", err)
	}
	if len(eng.registerGroupCalls) != 1 ||
		eng.registerGroupCalls[0].groupID != "new-desk" ||
		!slices.Equal(eng.registerGroupCalls[0].accounts, []domain.AccountID{id}) {
		t.Fatalf("register group calls = %+v, want one for new-desk/%s",
			eng.registerGroupCalls, id)
	}

	account, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: %v ok=%v", err, ok)
	}
	if account.GroupCode != "new-desk" {
		t.Fatalf("account group = %q, want new-desk", account.GroupCode)
	}
}

func TestLocalNode_BlockEngineFailureRevertsStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	eng.failBlock = true
	next := newFakeEngine()
	n.build = fakeBuild(next, new(engine.Snapshot))
	if err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", domain.MissingAccountCreate, testCaller); err == nil {
		t.Fatalf("block: want error on engine failure")
	}

	account, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: %v ok=%v", err, ok)
	}
	if account.Blocked {
		t.Fatalf("store not reverted: account still blocked")
	}
}

func TestLocalNode_BlockRevertFailureAndRebuildFailureIsFatal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	real := newMemoryStore("account-block-revert.db")
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	revertErr := errors.New("account block store revert failed")
	st := newRealmWrapStore(real, func(realm store.RealmStore) store.RealmStore {
		return &failAccountBlockRevertRealm{
			RealmStore: realm,
			revertErr:  revertErr,
		}
	})
	eng := newFakeEngine()
	n := newTestNodeWithStore(t, st, eng)
	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	eng.failBlock = true
	rebuildErr := errors.New("account block reconciliation rebuild failed")
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, rebuildErr
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, revertErr) || !errors.Is(err, rebuildErr) {
		t.Fatalf("SetAccountBlocked error = %v, want revert and rebuild failures", err)
	}
	if fatalErr == nil || !errors.Is(fatalErr, rebuildErr) {
		t.Fatalf("fatal error = %v, want rebuild failure", fatalErr)
	}
}

// newLaneProbeNode builds a node whose realm is a laneProbeRealm around a real
// temp SQLite store, so a test can assert block/group store writes execute
// inside the engine lane closure.
func newLaneProbeNode(
	t *testing.T, eng *fakeEngine,
) (*localNode, *laneProbeRealm) {
	t.Helper()
	probe := &laneProbeRealm{eng: eng}
	st := newRealmWrapStore(newMemoryStore("lane-probe.db"), func(inner store.RealmStore) store.RealmStore {
		probe.RealmStore = inner
		return probe
	})
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	n := newTestNodeWithStore(t, st, eng)
	return n, probe
}

// TestLocalNode_SetAccountBlockedStoreWriteInsideLane proves the store write is
// serialized inside the account lane closure, so a concurrent same-account admin
// op cannot interleave the store write with the engine block.
func TestLocalNode_SetAccountBlockedStoreWriteInsideLane(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("block: %v", err)
	}
	if len(probe.inLane) != 1 || !probe.inLane[0] {
		t.Fatalf("store write in-lane flags = %+v, want one true (write inside lane)", probe.inLane)
	}
}

// TestLocalNode_SetAccountBlockedStoreFailureInLaneLeavesEngineUntouched proves a
// store-write failure inside the lane returns before the engine block runs, so
// the engine is never mutated when the store write cannot commit.
func TestLocalNode_SetAccountBlockedStoreFailureInLaneLeavesEngineUntouched(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	probe.failErr = errors.New("store write failed")
	if err := n.SetAccountBlocked(ctx, testKey(id), true, "risk", domain.MissingAccountCreate, testCaller); err == nil {
		t.Fatalf("block: want error on store write failure")
	}
	if len(probe.inLane) != 1 || !probe.inLane[0] {
		t.Fatalf("store write in-lane flags = %+v, want one true", probe.inLane)
	}
	if len(eng.blockCalls) != 0 {
		t.Fatalf("engine block calls = %+v, want none on store failure", eng.blockCalls)
	}
	if len(eng.accountSyncCalls) == 0 || eng.accountSyncCalls[len(eng.accountSyncCalls)-1] != id {
		t.Fatalf("account sync calls = %+v, want last %s (write attempted inside lane)",
			eng.accountSyncCalls, id)
	}
}

// TestLocalNode_SetAccountGroupStoreWriteInsideLane proves the group-move store
// write is serialized inside the account lane closure.
func TestLocalNode_SetAccountGroupStoreWriteInsideLane(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetAccountGroup(ctx, testKey(id), "desk-a", domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("SetAccountGroup: %v", err)
	}
	if len(probe.inLane) != 1 || !probe.inLane[0] {
		t.Fatalf("store write in-lane flags = %+v, want one true (write inside lane)", probe.inLane)
	}
}

// TestLocalNode_SetAccountGroupStoreFailureInLaneLeavesEngineUntouched proves a
// store-write failure inside the account lane returns before the engine move
// runs.
func TestLocalNode_SetAccountGroupStoreFailureInLaneLeavesEngineUntouched(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	probe.failErr = errors.New("store write failed")
	if err := n.SetAccountGroup(ctx, testKey(id), "desk-a", domain.MissingAccountCreate, testCaller); err == nil {
		t.Fatalf("SetAccountGroup: want error on store write failure")
	}
	if len(probe.inLane) != 1 || !probe.inLane[0] {
		t.Fatalf("store write in-lane flags = %+v, want one true", probe.inLane)
	}
	if len(eng.registerGroupCalls) != 0 || len(eng.unregisterGroupCalls) != 0 {
		t.Fatalf("engine group moves = register %+v unregister %+v, want none on store failure",
			eng.registerGroupCalls, eng.unregisterGroupCalls)
	}
}

// TestLocalNode_SetGroupBlockedStoreWriteUnderGate proves the group-block store
// write runs under the exclusive restart gate, then the engine block follows on
// the group lane.
func TestLocalNode_SetGroupBlockedStoreWriteUnderGate(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := n.SetGroupBlocked(ctx, "desk-a", true, "risk", testCaller); err != nil {
		t.Fatalf("SetGroupBlocked: %v", err)
	}
	if len(probe.inLane) != 1 || probe.inLane[0] {
		t.Fatalf("store write in-lane flags = %+v, want one false (write under restart gate)", probe.inLane)
	}
	if len(eng.blockGroupCalls) != 1 {
		t.Fatalf("engine block group calls = %+v, want one after store write", eng.blockGroupCalls)
	}
	if !slices.Equal(eng.groupSyncCalls, []string{"desk-a"}) {
		t.Fatalf("group sync calls = %+v, want desk-a", eng.groupSyncCalls)
	}
}

// TestLocalNode_SetGroupBlockedStoreFailureLeavesEngineUntouched proves a
// store-write failure under the gate returns before the engine block runs, so
// the engine is never mutated when the store write cannot commit.
func TestLocalNode_SetGroupBlockedStoreFailureLeavesEngineUntouched(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	probe.failErr = errors.New("store write failed")
	if err := n.SetGroupBlocked(ctx, "desk-a", true, "risk", testCaller); err == nil {
		t.Fatalf("SetGroupBlocked: want error on store write failure")
	}
	if len(probe.inLane) != 1 || probe.inLane[0] {
		t.Fatalf("store write in-lane flags = %+v, want one false (write under restart gate)", probe.inLane)
	}
	if len(eng.blockGroupCalls) != 0 {
		t.Fatalf("engine block group calls = %+v, want none on store failure", eng.blockGroupCalls)
	}
}

// TestLocalNode_BlockAccountAuditRecordsReason proves the operator's reason
// reaches the audit trail and survives there. account.block_reason is
// overwritten in place and cleared on unblock, so after block-unblock-block the
// first reason exists nowhere but the trail - which is the property the trail
// exists to provide.
func TestLocalNode_BlockAccountAuditRecordsReason(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	block := func(reason string) {
		t.Helper()
		if err := n.SetAccountBlocked(
			ctx, testKey(id), true, reason, domain.MissingAccountCreate, testCaller,
		); err != nil {
			t.Fatalf("block %q: %v", reason, err)
		}
	}
	block("margin breach")
	if err := n.SetAccountBlocked(
		ctx, testKey(id), false, "", domain.MissingAccountCreate, testCaller,
	); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	block("suspected fraud")

	account, _, err := n.GetAccountState(ctx, testKey(id))
	if err != nil {
		t.Fatalf("GetAccountState: %v", err)
	}
	if account.BlockReason != "suspected fraud" {
		t.Fatalf("account block reason = %q, want the latest reason", account.BlockReason)
	}

	rows, err := st.ListAudit(ctx, 20)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	details := make([]string, 0, len(rows))
	for _, row := range rows {
		details = append(details, row.Detail)
	}
	if !slices.Contains(details, "block account acc-1: margin breach") {
		t.Fatalf("first block reason lost from the trail: %v", details)
	}
	if !slices.Contains(details, "block account acc-1: suspected fraud") {
		t.Fatalf("second block reason missing from the trail: %v", details)
	}
	if !slices.Contains(details, "unblock account acc-1") {
		t.Fatalf("reasonless unblock must keep the bare form: %v", details)
	}
}

// TestLocalNode_BlockGroupAuditRecordsReasonAndGroup proves the group block
// records both the operator's reason and the structured group handle. The
// structured field matters because group rows carry no account: a reader
// selecting them by a substring of Detail would match any group whose code is
// a substring of another's, and a crafted code can forge the match outright.
func TestLocalNode_BlockGroupAuditRecordsReasonAndGroup(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()

	for _, code := range []string{"a", "desk-a"} {
		if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: code}, testCaller); err != nil {
			t.Fatalf("CreateGroup %q: %v", code, err)
		}
	}
	if err := n.SetGroupBlocked(ctx, "desk-a", true, "desk over limit", testCaller); err != nil {
		t.Fatalf("SetGroupBlocked: %v", err)
	}
	if err := n.SetGroupBlocked(ctx, "desk-a", false, "", testCaller); err != nil {
		t.Fatalf("SetGroupBlocked unblock: %v", err)
	}

	page, err := st.ListAuditRows(ctx, store.AuditListFilter{
		Group: store.ExactTextMatcher("desk-a"),
	})
	if err != nil {
		t.Fatalf("ListAuditRows: %v", err)
	}
	wantDetails := []string{
		"unblock group desk-a",
		"block group desk-a: desk over limit",
		"create group desk-a",
	}
	got := make([]string, 0, len(page.Rows))
	for _, row := range page.Rows {
		if row.Group != "desk-a" {
			t.Fatalf("row %+v leaked into the desk-a filter", row)
		}
		got = append(got, row.Detail)
	}
	if !slices.Equal(got, wantDetails) {
		t.Fatalf("desk-a audit rows = %v, want %v", got, wantDetails)
	}

	// The one-character group "a" is a substring of "desk-a"; the structured
	// filter must not confuse the two the way a detail substring match would.
	page, err = st.ListAuditRows(ctx, store.AuditListFilter{
		Group: store.ExactTextMatcher("a"),
	})
	if err != nil {
		t.Fatalf("ListAuditRows for group a: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Detail != "create group a" {
		t.Fatalf("group a audit rows = %+v, want only its own create row", page.Rows)
	}
}

// auditGroupRows returns the rows of one action filed under a group code. The
// structured group column is the reader's only identity handle on a group, so
// the tests below select through it instead of matching detail text.
func auditGroupRows(
	t *testing.T, ctx context.Context, st store.RealmStore,
	code string, action domain.AuditAction,
) []domain.AuditRow {
	t.Helper()
	page, err := st.ListAuditRows(ctx, store.AuditListFilter{
		Group:   store.ExactTextMatcher(code),
		Actions: []domain.AuditAction{action},
	})
	if err != nil {
		t.Fatalf("ListAuditRows group=%q: %v", code, err)
	}
	return page.Rows
}

// auditActionRows returns every row of one action whatever group it is filed
// under, so a test can count the rows an event produced and see the ones filed
// under no group at all.
func auditActionRows(
	t *testing.T, ctx context.Context, st store.RealmStore, action domain.AuditAction,
) []domain.AuditRow {
	t.Helper()
	page, err := st.ListAuditRows(ctx, store.AuditListFilter{
		Actions: []domain.AuditAction{action},
	})
	if err != nil {
		t.Fatalf("ListAuditRows action=%q: %v", action, err)
	}
	return page.Rows
}

// TestLocalNode_UpdateGroupAuditFilesRenameUnderBothCodes proves a rename is
// filed under both codes and names both. Filed under the new code only, the old
// code's history ends with nothing saying where the group went; filed under the
// old one only, the new code starts with no history at all.
func TestLocalNode_UpdateGroupAuditFilesRenameUnderBothCodes(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()

	if _, err := n.CreateGroup(
		ctx, domain.AccountGroup{Code: "desk-old"}, testCaller,
	); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := n.UpdateGroup(ctx, "desk-old", domain.AccountGroup{
		Code:  "desk-new",
		Title: "renamed",
	}, testCaller); err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}

	const wantDetail = "update group desk-old -> desk-new"
	for _, code := range []string{"desk-old", "desk-new"} {
		rows := auditGroupRows(t, ctx, st, code, domain.AuditActionUpdateGroup)
		if len(rows) != 1 || rows[0].Detail != wantDetail {
			t.Fatalf("update rows under %q = %+v, want one %q", code, rows, wantDetail)
		}
	}
	if rows := auditActionRows(
		t, ctx, st, domain.AuditActionUpdateGroup,
	); len(rows) != 2 {
		t.Fatalf("update rows = %+v, want exactly one per code", rows)
	}
}

// TestLocalNode_UpdateGroupTitleOnlyAuditsOnce proves a title-only update stays
// a single row: there is no second code to file it under, and a duplicate would
// read as two separate updates.
func TestLocalNode_UpdateGroupTitleOnlyAuditsOnce(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()

	if _, err := n.CreateGroup(
		ctx, domain.AccountGroup{Code: "desk-a"}, testCaller,
	); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := n.UpdateGroup(ctx, "desk-a", domain.AccountGroup{
		Code:  "desk-a",
		Title: "renamed",
	}, testCaller); err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}

	rows := auditActionRows(t, ctx, st, domain.AuditActionUpdateGroup)
	if len(rows) != 1 || rows[0].Group != "desk-a" ||
		rows[0].Detail != "update group desk-a" {
		t.Fatalf("update rows = %+v, want one row filed under desk-a", rows)
	}
}

// TestLocalNode_SetAccountGroupAuditFilesMoveUnderBothGroups proves a membership
// move is filed under the origin and the destination. Membership is the
// operationally decisive group event: a group that loses a member must show that
// loss in its own rows, not only in the rows of the group that gained it.
func TestLocalNode_SetAccountGroupAuditFilesMoveUnderBothGroups(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()

	for _, code := range []string{"desk-a", "desk-b"} {
		if _, err := n.CreateGroup(
			ctx, domain.AccountGroup{Code: code}, testCaller,
		); err != nil {
			t.Fatalf("CreateGroup %q: %v", code, err)
		}
	}
	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code:      id,
		Title:     "desk account",
		GroupCode: "desk-a",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := n.SetAccountGroup(
		ctx, testKey(id), "desk-b", domain.MissingAccountCreate, testCaller,
	); err != nil {
		t.Fatalf("SetAccountGroup: %v", err)
	}

	const wantDetail = "set group account acc-1 from=desk-a to=desk-b"
	for _, code := range []string{"desk-a", "desk-b"} {
		rows := auditGroupRows(t, ctx, st, code, domain.AuditActionSetGroup)
		if len(rows) != 1 || rows[0].Detail != wantDetail {
			t.Fatalf("set-group rows under %q = %+v, want one %q", code, rows, wantDetail)
		}
		if rows[0].Account != id || rows[0].AccountTitle != "desk account" {
			t.Fatalf("set-group row under %q = %+v, want the account handle kept", code, rows[0])
		}
	}
	if rows := auditActionRows(
		t, ctx, st, domain.AuditActionSetGroup,
	); len(rows) != 2 {
		t.Fatalf("set-group rows = %+v, want exactly one per group", rows)
	}
}

// TestLocalNode_SetAccountGroupAuditFilesOpenEndedMoveOnce proves joining from
// no group and leaving to no group each leave one row, filed under the group
// that is actually named. The empty code is the reserved default group; a row
// filed under it would make every ungrouped membership change collect there.
func TestLocalNode_SetAccountGroupAuditFilesOpenEndedMoveOnce(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()

	if _, err := n.CreateGroup(
		ctx, domain.AccountGroup{Code: "desk-a"}, testCaller,
	); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := n.SetAccountGroup(
		ctx, testKey(id), "desk-a", domain.MissingAccountCreate, testCaller,
	); err != nil {
		t.Fatalf("SetAccountGroup join: %v", err)
	}
	rows := auditActionRows(t, ctx, st, domain.AuditActionSetGroup)
	wantJoin := "set group account acc-1 from=<none> to=desk-a"
	if len(rows) != 1 || rows[0].Group != "desk-a" || rows[0].Detail != wantJoin {
		t.Fatalf("join rows = %+v, want one desk-a row %q", rows, wantJoin)
	}

	if err := n.SetAccountGroup(
		ctx, testKey(id), "", domain.MissingAccountCreate, testCaller,
	); err != nil {
		t.Fatalf("SetAccountGroup leave: %v", err)
	}
	rows = auditActionRows(t, ctx, st, domain.AuditActionSetGroup)
	wantLeave := "set group account acc-1 from=desk-a to=<none>"
	// Newest first: the leave precedes the join in the listing.
	if len(rows) != 2 || rows[0].Group != "desk-a" || rows[0].Detail != wantLeave {
		t.Fatalf("leave rows = %+v, want one more desk-a row %q", rows, wantLeave)
	}
	for _, row := range rows {
		if row.Group == "" {
			t.Fatalf("row %+v was filed under the reserved default group", row)
		}
	}
	if got := auditGroupRows(
		t, ctx, st, "desk-a", domain.AuditActionSetGroup,
	); len(got) != 2 {
		t.Fatalf("desk-a set-group rows = %+v, want both the join and the leave", got)
	}
}

// TestLocalNode_BusinessCSVImportAuditRecordsBlockReasons proves the CSV import
// path records the same reasons as the interactive block: the import is the one
// route that can block many accounts and groups at once, so a trail that names
// none of the reasons is the least useful exactly where it matters most.
func TestLocalNode_BusinessCSVImportAuditRecordsBlockReasons(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Groups: []store.BusinessCSVImportGroup{{
			Group: domain.AccountGroup{
				Code:        "desk-a",
				Blocked:     true,
				BlockReason: "desk suspended",
			},
		}},
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{
				Code:        "acc-1",
				Blocked:     true,
				BlockReason: "imported block",
			},
		}},
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}

	rows, err := st.ListAudit(ctx, 50)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	details := make([]string, 0, len(rows))
	for _, row := range rows {
		details = append(details, row.Detail)
	}
	for _, want := range []string{
		"block account acc-1: imported block",
		"block group desk-a: desk suspended",
	} {
		if !slices.Contains(details, want) {
			t.Fatalf("audit details %v missing %q", details, want)
		}
	}
	for _, row := range rows {
		if row.Action == domain.AuditActionBlockGroup && row.Group != "desk-a" {
			t.Fatalf("imported group block row %+v carries no structured group", row)
		}
	}
}

// testOrder records a committed buy order so an execution report has a parent
// order row to attach its event and trade to.
