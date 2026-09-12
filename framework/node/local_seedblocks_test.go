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
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// seedBlockEngine is a fakeEngine that also reports the blocks the engine
// latched while the handle was seeded, the way the native adapter does when a
// restored P&L is halted or already breaches its barrier.
type seedBlockEngine struct {
	*fakeEngine
	seedBlocks []domain.AccountBlock
}

func (e *seedBlockEngine) SeedAccountBlocks() []domain.AccountBlock {
	return e.seedBlocks
}

func newSeedBlockEngine() *seedBlockEngine {
	return &seedBlockEngine{fakeEngine: newFakeEngine()}
}

// seedBlockBuild adapts the fake build to hand back the seed-block-reporting
// wrapper, so every (re)build the node performs reports eng's current blocks.
func seedBlockBuild(eng *seedBlockEngine) engine.BuildFunc {
	var seed engine.Snapshot
	inner := fakeBuild(eng.fakeEngine, &seed)
	return func(snap engine.Snapshot) (engine.Engine, error) {
		if _, err := inner(snap); err != nil {
			return nil, err
		}
		return eng, nil
	}
}

func seedBlockOf(account domain.AccountID) domain.AccountBlock {
	return domain.AccountBlock{
		Account: account,
		Policy:  "SpotFundsPolicy",
		Code:    domain.RejectCodePnlKillSwitchTriggered,
		Reason:  "lower bound breached",
		Details: "realized pnl -50000, lower_bound -100",
	}
}

// newSeedBlockStore returns a migrated store already carrying the operator
// principal and every account in accounts, so a seed block harvested by the very
// first build has a row to land on.
func newSeedBlockStore(
	t *testing.T, accounts ...domain.AccountID,
) *memoryStore {
	t.Helper()
	ctx := context.Background()
	st := newMemoryStore("seedblocks.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	err = realm.CreatePrincipal(ctx, domain.Principal{Code: testCaller.Principal})
	if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	for _, account := range accounts {
		if _, err := realm.CreateAccount(ctx, testAccount(account)); err != nil {
			t.Fatalf("CreateAccount(%s): %v", account, err)
		}
	}
	return st
}

func assertBlockedByEngine(
	t *testing.T, realm store.RealmStore, account domain.AccountID,
) {
	t.Helper()
	ctx := context.Background()
	stored, ok, err := realm.GetAccount(ctx, account)
	if err != nil || !ok {
		t.Fatalf("GetAccount(%s): ok=%v err=%v", account, ok, err)
	}
	if !stored.Blocked {
		t.Fatalf("account %s reads tradable, want the engine's seed block mirrored",
			account)
	}
	if !strings.Contains(stored.BlockReason, "lower bound breached") {
		t.Fatalf("block reason = %q, want the engine cause", stored.BlockReason)
	}
	want := seedBlockOf(account)
	if stored.BlockPolicy != want.Policy || stored.BlockCode != want.Code ||
		stored.BlockDetails != want.Details {
		t.Fatalf("typed seed block = %+v, want %+v", stored, want)
	}

	rows, err := realm.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("block audits = %+v, want exactly one naming the cause", rows)
	}
	if rows[0].Source != domain.SourceSystem || rows[0].Actor != "" {
		t.Fatalf("block audit attribution = %+v, want system without actor", rows[0])
	}
}

// TestLocalNode_SeedAccountBlocksFromStartupBuildReachStore covers the restart
// case: the engine kill-switches an account while seeding its accumulated loss
// past the barrier, so the node must never hand out a store that still reads
// tradable.
func TestLocalNode_SeedAccountBlocksFromStartupBuildReachStore(t *testing.T) {
	t.Parallel()
	const account domain.AccountID = "acc-1"
	st := newSeedBlockStore(t, account)
	eng := newSeedBlockEngine()
	eng.seedBlocks = []domain.AccountBlock{seedBlockOf(account)}

	n, _, err := NewLocalNode(context.Background(), st, seedBlockBuild(eng))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	assertBlockedByEngine(t, n.(*localNode).realm, account)
}

// TestLocalNode_SeedAccountBlocksFromRebuildReachStore covers the same harvest
// on the rebuild path, which reseeds the same P&L into a fresh handle.
func TestLocalNode_SeedAccountBlocksFromRebuildReachStore(t *testing.T) {
	t.Parallel()
	const account domain.AccountID = "acc-1"
	st := newSeedBlockStore(t, account)
	eng := newSeedBlockEngine()
	ctx := context.Background()

	n, _, err := NewLocalNode(ctx, st, seedBlockBuild(eng))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	local := n.(*localNode)

	// The handle built next reports the block; any rebuild must mirror it.
	eng.seedBlocks = []domain.AccountBlock{seedBlockOf(account)}
	if err := local.rebuildEngineFromStore(ctx); err != nil {
		t.Fatalf("rebuildEngineFromStore: %v", err)
	}
	assertBlockedByEngine(t, local.realm, account)
}

// TestLocalNode_NoSeedAccountBlocksLeavesAccountsTradable is the boundary: a
// clean seeding path must not block anything.
func TestLocalNode_NoSeedAccountBlocksLeavesAccountsTradable(t *testing.T) {
	t.Parallel()
	const account domain.AccountID = "acc-1"
	st := newSeedBlockStore(t, account)
	eng := newSeedBlockEngine()

	n, _, err := NewLocalNode(context.Background(), st, seedBlockBuild(eng))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	stored, ok, err := n.(*localNode).realm.GetAccount(context.Background(), account)
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if stored.Blocked {
		t.Fatalf("account blocked without any engine seed block: %+v", stored)
	}
}

// TestLocalNode_SeedAccountBlockWithoutAccountFailsClosed pins the fail-closed
// path: the engine has blocked an account the store cannot name, so startup must
// fail rather than serve a store that disagrees with the engine.
func TestLocalNode_SeedAccountBlockWithoutAccountFailsClosed(t *testing.T) {
	t.Parallel()
	st := newSeedBlockStore(t)
	eng := newSeedBlockEngine()
	block := seedBlockOf("acc-1")
	block.Account = ""
	eng.seedBlocks = []domain.AccountBlock{block}

	var fatal error
	_, _, err := NewLocalNode(
		context.Background(), st, seedBlockBuild(eng),
		WithFatalShutdownHook(func(hookErr error) { fatal = hookErr }),
	)
	if err == nil {
		t.Fatal("NewLocalNode succeeded, want failure on an unattributable block")
	}
	if fatal == nil {
		t.Fatal("fatal hook not invoked for an unattributable engine block")
	}
}
