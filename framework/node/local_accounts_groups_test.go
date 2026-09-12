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

	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/param"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

type mismatchedGroupIdentityRealm struct {
	store.RealmStore
	group string
}

func (r *mismatchedGroupIdentityRealm) GetGroup(
	ctx context.Context,
	code string,
) (domain.AccountGroup, bool, error) {
	group, ok, err := r.RealmStore.GetGroup(ctx, code)
	if code == r.group && ok && err == nil {
		group.EngineGroupID = domain.EngineGroupID(424242)
	}
	return group, ok, err
}

func TestAdministrativeIdentityMismatchErrorsAreRedacted(t *testing.T) {
	t.Parallel()
	err := validateAccountAdministrativeSource(
		domain.Account{
			Code:            "account",
			EngineAccountID: domain.EngineAccountID(424242),
		},
		param.NewAccountIDFromUint64(313131),
	)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("validateAccountAdministrativeSource = %v, want ErrInvalid", err)
	}
	for _, handle := range []string{"424242", "313131"} {
		if strings.Contains(err.Error(), handle) {
			t.Fatalf("validation error %q discloses engine handle %s", err, handle)
		}
	}
}

func TestLocalNode_SetGroupBlockedRejectsStoredLiveIdentityMismatch(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateGroup(
		ctx,
		domain.AccountGroup{Code: "desk"},
		testCaller,
	); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	n.realm = &mismatchedGroupIdentityRealm{
		RealmStore: n.realm,
		group:      "desk",
	}
	err := n.SetGroupBlocked(ctx, "desk", true, "risk", testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SetGroupBlocked = %v, want ErrInvalid", err)
	}
	if strings.Contains(err.Error(), "424242") {
		t.Fatalf("SetGroupBlocked error discloses stored engine handle: %v", err)
	}
	group, ok, getErr := st.GetGroup(ctx, "desk")
	if getErr != nil || !ok || group.Blocked {
		t.Fatalf("stored group = %+v ok=%v err=%v, want unblocked", group, ok, getErr)
	}
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
	account, _, err := n.GetAccountState(ctx, testKey(id))
	if err != nil {
		t.Fatalf("GetAccountState: %v", err)
	}
	if !account.Blocked || account.BlockReason != "risk" {
		t.Fatalf("account not blocked in store: %+v", account)
	}
	assertFakeOrderBlock(t, eng, id, true, "risk")

	if err := n.SetAccountBlocked(ctx, testKey(id), false, "", domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	assertFakeOrderBlock(t, eng, id, false, "")
	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// startup hydrate, create_account, block, unblock = 4 rows, newest first.
	if len(rows) != 4 || rows[0].Action != domain.AuditActionUnblock {
		t.Fatalf("unexpected audit trail: %+v", rows)
	}
}

// TestLocalNode_SetAccountBlockedChecksCapturedContext proves Begin observes
// the caller context captured before submission and stops before the SDK call.
func TestLocalNode_SetAccountBlockedChecksCapturedContext(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err := n.SetAccountBlocked(
		cancelled, testKey(id), true, "risk", domain.MissingAccountCreate, testCaller,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SetAccountBlocked error = %v, want context cancellation", err)
	}
	account, ok, err := n.realm.GetAccount(ctx, id)
	if err != nil || !ok || account.Blocked {
		t.Fatalf("account after cancelled block = %+v ok=%v err=%v", account, ok, err)
	}
}

// TestLocalNode_SetAccountGroupPersistsMembership proves the membership step
// and its post-engine store hook complete as one successful operation.
func TestLocalNode_SetAccountGroupPersistsMembership(t *testing.T) {
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
	account, ok, err := n.realm.GetAccount(ctx, id)
	if err != nil || !ok || account.GroupCode != "desk-a" {
		t.Fatalf("account after membership change = %+v ok=%v err=%v", account, ok, err)
	}
	assertFakeAccountGroup(t, eng, id, "desk-a")
}

// TestLocalNode_SetAccountGroupAutoCreatesUnknownGroup proves the auto-create
// contract against a strict resolver: moving an account into a group with no
// account_groups record succeeds because the missing record and its stable
// resolver identity are published before the membership chain is assembled.
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
	assertFakeAccountGroup(t, eng, id, "new-desk")
}

// TestLocalNode_SetGroupBlockedRunsUnderLiveIdentityGate proves the group block
// keeps the live engine while fencing concurrent identity publications until
// the group-sourced chain finishes.
func TestLocalNode_SetGroupBlockedRunsUnderLiveIdentityGate(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	probe.entered = entered
	probe.release = release
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	errs := make(chan error, 1)
	go func() { errs <- n.SetGroupBlocked(ctx, "desk-a", true, "risk", testCaller) }()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("SetGroupBlocked did not reach its post-engine store hook")
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
	groupID, resolveErr := eng.ResolveGroup("new-desk")
	if resolveErr != nil {
		t.Fatalf("ResolveGroup(new-desk): %v", resolveErr)
	}
	if err := eng.administrativeDriver().Accounts().ReplaceGroupBlockReason(
		groupID, "risk",
	); err != nil {
		t.Fatalf("engine group block missing: %v", err)
	}
}

func TestLocalNode_SetGroupBlockedReplacesUnmirroredEngineReason(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateGroup(
		ctx, domain.AccountGroup{Code: "desk-a"}, testCaller,
	); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := n.CreateAccount(
		ctx,
		domain.Account{Code: "acc-1"},
		testCaller,
	); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetAccountGroup(
		ctx,
		testKey("acc-1"),
		"desk-a",
		domain.MissingAccountReject,
		testCaller,
	); err != nil {
		t.Fatalf("SetAccountGroup: %v", err)
	}
	groupID, err := eng.ResolveGroup("desk-a")
	if err != nil {
		t.Fatalf("ResolveGroup(desk-a): %v", err)
	}
	if err := eng.administrativeDriver().Accounts().BlockGroup(
		groupID, "unmirrored reason",
	); err != nil {
		t.Fatalf("seed engine group block: %v", err)
	}

	if err := n.SetGroupBlocked(
		ctx, "desk-a", true, "operator reason", testCaller,
	); err != nil {
		t.Fatalf("SetGroupBlocked: %v", err)
	}
	assertFakeOrderBlock(t, eng, "acc-1", true, "operator reason")
}

// TestLocalNode_SetAccountBlockedAuditFailureFatals proves the account-block
// path joins the post-engine fail-stop: once the engine block and store write
// committed inside the account chain, a failing audit write routes to the fatal
// hook. The diagnostic must name the account CODE, never the
// engine surrogate (account_id), so operators reading the fatal log see the same
// identifier the audit row stores.
func TestLocalNode_SetAccountBlockedAuditFailureFatals(t *testing.T) {
	t.Parallel()
	auditCause := errors.New("account block audit failed")
	auditErr := errors.Join(domain.ErrNotFound, auditCause)
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

	err := n.SetAccountBlocked(
		ctx, testKey(id), true, "risk", domain.MissingAccountCreate, testCaller,
	)
	if errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetAccountBlocked error = %v, must hide domain sentinel", err)
	}
	if !errors.Is(err, auditCause) {
		t.Fatalf("SetAccountBlocked error = %v, want audit cause", err)
	}
	if fatalErr == nil ||
		!errors.Is(fatalErr, domain.ErrNotFound) ||
		!errors.Is(fatalErr, auditCause) {
		t.Fatalf(
			"fatal error = %v, want sentinel and non-domain audit cause",
			fatalErr,
		)
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

func TestFatalReconciliationReturnsIdempotentTerminalError(t *testing.T) {
	t.Parallel()
	reconciliationCause := errors.New("store reconciliation failed")
	raw := errors.Join(domain.ErrConflict, reconciliationCause)
	var fatalErr error
	n := &localNode{fatal: func(err error) { fatalErr = err }}

	err := n.fatalReconciliation("reconcile mutation", raw)
	if errors.Is(err, domain.ErrConflict) {
		t.Fatalf("returned error = %v, must hide domain sentinel", err)
	}
	if !errors.Is(err, reconciliationCause) {
		t.Fatalf("returned error = %v, want reconciliation cause", err)
	}
	if fatalErr == nil ||
		!errors.Is(fatalErr, domain.ErrConflict) ||
		!errors.Is(fatalErr, reconciliationCause) {
		t.Fatalf(
			"fatal error = %v, want sentinel and reconciliation cause",
			fatalErr,
		)
	}
	if rewrapped := internalPostCommitNodeMutationError(err); rewrapped != err {
		t.Fatalf("rewrapped error = %T, want unchanged %T", rewrapped, err)
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
	account, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: %v ok=%v", err, ok)
	}
	if account.GroupCode != "new-desk" {
		t.Fatalf("account group = %q, want new-desk", account.GroupCode)
	}
}

// newLaneProbeNode builds a node whose realm can hold an administrative store
// hook open while a test probes the SDK lane or the live-identity gate.
func newLaneProbeNode(
	t *testing.T, eng *fakeEngine,
) (*localNode, *laneProbeRealm) {
	t.Helper()
	probe := &laneProbeRealm{}
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

func assertAdministrativeStoreWriteSerialized(
	t *testing.T,
	eng *fakeEngine,
	probe *laneProbeRealm,
	account domain.AccountID,
	run func() error,
) {
	t.Helper()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	probe.entered = entered
	probe.release = release

	operation := make(chan error, 1)
	go func() { operation <- run() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("administrative operation did not reach its store hook")
	}

	source, err := eng.AccountID(account)
	if err != nil {
		t.Fatalf("AccountID: %v", err)
	}
	competingRan := make(chan struct{})
	competing := eng.AsyncEngine().Submit(
		context.Background(), source, func() error {
			close(competingRan)
			return nil
		},
	)
	select {
	case <-competingRan:
		t.Fatal("same-account task interleaved with administrative store hook")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	released = true
	if err := <-operation; err != nil {
		t.Fatalf("administrative operation: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := competing.Await(waitCtx); err != nil {
		t.Fatalf("competing same-account task: %v", err)
	}
}

// TestLocalNode_SetAccountBlockedStoreWriteInsideChain proves the caller's
// store write remains in the SDK account chain and serializes with the account.
func TestLocalNode_SetAccountBlockedStoreWriteInsideChain(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	assertAdministrativeStoreWriteSerialized(t, eng, probe, id, func() error {
		return n.SetAccountBlocked(
			ctx, testKey(id), true, "risk", domain.MissingAccountCreate, testCaller,
		)
	})
}

// TestLocalNode_SetAccountBlockedPostEngineStoreFailureFatals proves a store
// failure after the SDK mutation is retry-unsafe and fail-stops persistence.
func TestLocalNode_SetAccountBlockedPostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cause := errors.New("store write failed")
	probe.failErr = cause
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }
	err := n.SetAccountBlocked(
		ctx, testKey(id), true, "risk", domain.MissingAccountCreate, testCaller,
	)
	if !errors.Is(err, cause) || !errors.Is(err, asyncengine.ErrChainRetryUnsafe) {
		t.Fatalf("SetAccountBlocked error = %v, want cause and retry-unsafe marker", err)
	}
	if !errors.Is(fatalErr, cause) {
		t.Fatalf("fatal error = %v, want store cause", fatalErr)
	}
	assertFakeOrderBlock(t, eng, id, true, "risk")
	account, ok, getErr := n.realm.GetAccount(ctx, id)
	if getErr != nil || !ok || account.Blocked {
		t.Fatalf("stored account = %+v ok=%v err=%v, want unchanged", account, ok, getErr)
	}
}

// TestLocalNode_SetAccountGroupStoreWriteInsideAccountChain proves membership
// chains route through their first account rather than the synthetic group lane.
func TestLocalNode_SetAccountGroupStoreWriteInsideAccountChain(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	assertAdministrativeStoreWriteSerialized(t, eng, probe, id, func() error {
		return n.SetAccountGroup(
			ctx, testKey(id), "desk-a", domain.MissingAccountCreate, testCaller,
		)
	})
}

// TestLocalNode_SetAccountGroupPostEngineStoreFailureFatals proves membership
// persistence failure is classified after the successful SDK mutation.
func TestLocalNode_SetAccountGroupPostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cause := errors.New("store write failed")
	probe.failErr = cause
	reconcileErr := errors.New("post-engine persistence must not reconcile")
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, reconcileErr
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }
	err := n.SetAccountGroup(
		ctx, testKey(id), "desk-a", domain.MissingAccountCreate, testCaller,
	)
	if !errors.Is(err, cause) || !errors.Is(err, asyncengine.ErrChainRetryUnsafe) {
		t.Fatalf("SetAccountGroup error = %v, want cause and retry-unsafe marker", err)
	}
	if errors.Is(err, reconcileErr) {
		t.Fatalf("SetAccountGroup error = %v, must keep fatal persistence classification", err)
	}
	if !errors.Is(fatalErr, cause) {
		t.Fatalf("fatal error = %v, want store cause", fatalErr)
	}
	account, ok, getErr := n.realm.GetAccount(ctx, id)
	if getErr != nil || !ok || account.GroupCode != "" {
		t.Fatalf("stored account = %+v ok=%v err=%v, want no group", account, ok, getErr)
	}
	assertFakeAccountGroup(t, eng, id, "desk-a")
}

func TestLocalNode_SetGroupBlockedPersistsAfterEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newLaneProbeNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := n.SetGroupBlocked(ctx, "desk-a", true, "risk", testCaller); err != nil {
		t.Fatalf("SetGroupBlocked: %v", err)
	}
	group, ok, err := n.realm.GetGroup(ctx, "desk-a")
	if err != nil || !ok || !group.Blocked {
		t.Fatalf("stored group = %+v ok=%v err=%v, want blocked", group, ok, err)
	}
	groupID, resolveErr := eng.ResolveGroup("desk-a")
	if resolveErr != nil {
		t.Fatalf("ResolveGroup(desk-a): %v", resolveErr)
	}
	if err := eng.administrativeDriver().Accounts().ReplaceGroupBlockReason(
		groupID, "risk",
	); err != nil {
		t.Fatalf("engine group block missing: %v", err)
	}
}

func TestLocalNode_SetGroupBlockedPostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	cause := errors.New("store write failed")
	probe.failErr = cause
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }
	err := n.SetGroupBlocked(ctx, "desk-a", true, "risk", testCaller)
	if !errors.Is(err, cause) || !errors.Is(err, asyncengine.ErrChainRetryUnsafe) {
		t.Fatalf("SetGroupBlocked error = %v, want cause and retry-unsafe marker", err)
	}
	if !errors.Is(fatalErr, cause) {
		t.Fatalf("fatal error = %v, want store cause", fatalErr)
	}
	groupID, resolveErr := eng.ResolveGroup("desk-a")
	if resolveErr != nil {
		t.Fatalf("ResolveGroup(desk-a): %v", resolveErr)
	}
	if err := eng.administrativeDriver().Accounts().ReplaceGroupBlockReason(
		groupID, "risk",
	); err != nil {
		t.Fatalf("engine group block missing: %v", err)
	}
}

func TestLocalNode_ReblockOperatorAccountReplacesReasonAndAudits(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	for _, reason := range []string{"first operator reason", "replacement operator reason"} {
		if err := n.SetAccountBlocked(
			ctx, testKey(id), true, reason, domain.MissingAccountCreate, testCaller,
		); err != nil {
			t.Fatalf("SetAccountBlocked(%q): %v", reason, err)
		}
	}

	assertFakeOrderBlock(t, eng, id, true, "replacement operator reason")
	account, _, err := n.GetAccountState(ctx, testKey(id))
	if err != nil {
		t.Fatalf("GetAccountState: %v", err)
	}
	if account.BlockReason != "replacement operator reason" || account.BlockCode != "" {
		t.Fatalf("stored replacement = %+v", account)
	}
	rows, err := st.ListAudit(ctx, 20)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	details := make([]string, 0, len(rows))
	for _, row := range rows {
		details = append(details, row.Detail)
	}
	for _, want := range []string{
		"block account acc-1: first operator reason",
		"block account acc-1: replacement operator reason",
	} {
		if !slices.Contains(details, want) {
			t.Fatalf("audit details = %v, want %q", details, want)
		}
	}
}

func TestLocalNode_ReblockTypedAccountConflictsWithoutAudit(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const id domain.AccountID = "acc-1"
	typed := domain.AccountBlock{
		Account: id,
		Policy:  "SpotFundsPolicy",
		Code:    domain.RejectCodePnlKillSwitchTriggered,
		Reason:  "persisted typed reason",
		Details: "persisted typed details",
	}
	account := testAccount(id)
	account.Blocked = true
	account.BlockReason = typed.Reason
	account.BlockPolicy = typed.Policy
	account.BlockCode = typed.Code
	account.BlockDetails = typed.Details
	if _, err := n.CreateAccount(ctx, account, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	before, err := st.ListAudit(ctx, 20)
	if err != nil {
		t.Fatalf("ListAudit(before): %v", err)
	}
	err = n.SetAccountBlocked(
		ctx, testKey(id), true, "operator override",
		domain.MissingAccountCreate, testCaller,
	)
	if !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), typed.Code) {
		t.Fatalf("SetAccountBlocked = %v, want typed-cause ErrConflict", err)
	}
	after, err := st.ListAudit(ctx, 20)
	if err != nil {
		t.Fatalf("ListAudit(after): %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("audit rows grew from %d to %d after refused re-block", len(before), len(after))
	}
	assertFakeTypedOrderBlock(t, eng, id, typed)
	stored, _, err := n.GetAccountState(ctx, testKey(id))
	if err != nil {
		t.Fatalf("GetAccountState: %v", err)
	}
	if stored.BlockReason != typed.Reason || stored.BlockPolicy != typed.Policy ||
		stored.BlockCode != typed.Code || stored.BlockDetails != typed.Details {
		t.Fatalf("typed cause changed after refused re-block: %+v", stored)
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

// testOrder records a committed buy order so an execution report has a parent
// order row to attach its event and trade to.
