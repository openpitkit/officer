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
	"fmt"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
)

func TestLocalNode_PutRateLimitAppliesAndAudits(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	if _, err := n.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	stored, err := st.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("want 1 stored barrier, got %d", len(stored))
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// Startup hydrate row plus the set_limit row, newest first.
	if len(rows) != 2 || rows[0].Action != domain.AuditActionSetLimit {
		t.Fatalf("want newest set_limit over startup hydrate, got %+v", rows)
	}
}

func TestLocalNode_PutAssetRateLimitAutoCreatesUnknownAsset(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	limit := rateLimit(domain.ScopeAsset, "", "GOLD", 100, time.Second)
	if _, err := n.PutRateLimit(ctx, limit, testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	asset, ok, err := st.GetAsset(ctx, "GOLD")
	if err != nil || !ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want auto-created", ok, err)
	}
	if asset.AssetClass != autoCreatedAssetClassCode {
		t.Fatalf("auto-created asset class = %q, want %q", asset.AssetClass,
			autoCreatedAssetClassCode)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAsset},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create asset): %v", err)
	}
	if len(rows) != 1 ||
		rows[0].Asset != "GOLD" ||
		!strings.Contains(rows[0].Detail, "auto-created asset GOLD by rate limit") {
		t.Fatalf("create-asset audit rows = %+v, want limit auto-create", rows)
	}
}

func TestLocalNode_PutRateLimitEngineFailureRevertsStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.failConfigure = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	if _, err := n.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), testCaller); err == nil {
		t.Fatalf("PutRateLimit: want error on engine failure")
	}

	stored, err := st.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("want store reverted to empty, got %d barriers", len(stored))
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// Only the startup hydrate row: the failed mutation audits nothing.
	if len(rows) != 1 || rows[0].Action != domain.AuditActionHydrate {
		t.Fatalf("want only the startup hydrate row, got %+v", rows)
	}
}

func TestLocalNode_PutRateLimitConfiguresPolicyFromStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	limit := rateLimit(domain.ScopeBroker, "", "", 100, time.Second)
	if _, err := n.PutRateLimit(ctx, limit, testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	if len(eng.configureCalls) != 1 {
		t.Fatalf("configure calls = %d, want 1", len(eng.configureCalls))
	}
	if eng.configureCalls[0].policy != domain.PolicyRateLimit {
		t.Fatalf("configured policy = %q, want rate_limit", eng.configureCalls[0].policy)
	}
	if len(eng.configureCalls[0].limits.RateLimits) != 1 {
		t.Fatalf("configured limits = %d, want 1", len(eng.configureCalls[0].limits.RateLimits))
	}
	if eng.configureCalls[0].limits.RateLimits[0] != limit {
		t.Fatalf("configured wrong barrier: %+v", eng.configureCalls[0].limits.RateLimits[0])
	}
}

func TestLocalNode_PutRateLimitNotImplementedRebuildsFromStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.configureErr = fmt.Errorf("engine: unregistered policy: %w",
		domain.ErrNotImplemented)
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	next := newFakeEngine()
	var rebuilt engine.Snapshot
	n.build = fakeBuild(next, &rebuilt)

	limit := rateLimit(domain.ScopeBroker, "", "", 100, time.Second)
	sink, err := n.PutRateLimit(ctx, limit, testCaller)
	if err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	if sink == nil {
		t.Fatal("PutRateLimit returned nil sink after rebuild")
	}
	if eng.running {
		t.Fatal("old engine still running after rebuild")
	}
	if !next.running {
		t.Fatal("replacement engine is not running after rebuild")
	}
	if len(rebuilt.RateLimits) != 1 || rebuilt.RateLimits[0] != limit {
		t.Fatalf("rebuilt limits = %+v, want the new barrier", rebuilt.RateLimits)
	}
}

// TestLocalNode_CreateAccountRebuildsEngineWithNewAccount verifies a freshly
// created account is wired into the live engine. The resolver has no incremental
// account registration, so the node rebuilds from the store on create; without
// the rebuild the account would persist but stay unknown to the engine
// (adjustments, group moves, and orders would reject as "unknown account") until
// a restart.
func TestLocalNode_CreateAccountRebuildsEngineWithNewAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	next := newFakeEngine()
	var rebuilt engine.Snapshot
	n.build = fakeBuild(next, &rebuilt)

	if _, err := n.CreateAccount(ctx, testAccount("fresh"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if eng.running {
		t.Fatal("old engine still running after create rebuild")
	}
	if !next.running {
		t.Fatal("replacement engine is not running after create rebuild")
	}
	found := false
	for _, a := range rebuilt.Accounts {
		if a.Code == "fresh" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("rebuilt accounts = %+v, want the new account", rebuilt.Accounts)
	}
}

// TestLocalNode_CreateGroupRebuildsEngineWithNewGroup verifies a freshly created
// group is wired into the live engine for the same reason: a runtime-created
// group is unknown to the resolver until the engine is rebuilt from the store.
func TestLocalNode_CreateGroupRebuildsEngineWithNewGroup(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	next := newFakeEngine()
	var rebuilt engine.Snapshot
	n.build = fakeBuild(next, &rebuilt)

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "vips"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if eng.running {
		t.Fatal("old engine still running after create rebuild")
	}
	if !next.running {
		t.Fatal("replacement engine is not running after create rebuild")
	}
	found := false
	for _, g := range rebuilt.Groups {
		if g.Code == "vips" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("rebuilt groups = %+v, want the new group", rebuilt.Groups)
	}
}

func TestLocalNode_PutRateLimitSamePolicyDifferentAccountsRetunesOnePolicy(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	buildCalls := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		buildCalls++
		return newFakeEngine(), nil
	}

	seedTestAccount(t, n.realm, "acc-1")
	seedTestAccount(t, n.realm, "acc-2")
	first := rateLimit(domain.ScopeAccount, "acc-1", "", 100, time.Second)
	second := rateLimit(domain.ScopeAccount, "acc-2", "", 10, time.Second)
	if _, err := n.PutRateLimit(ctx, first, testCaller); err != nil {
		t.Fatalf("PutRateLimit first: %v", err)
	}
	if _, err := n.PutRateLimit(ctx, second, testCaller); err != nil {
		t.Fatalf("PutRateLimit second: %v", err)
	}

	if buildCalls != 0 {
		t.Fatalf("build calls = %d, want 0", buildCalls)
	}
	if len(eng.configureCalls) != 2 {
		t.Fatalf("configure calls = %d, want 2", len(eng.configureCalls))
	}
	last := eng.configureCalls[1]
	if last.policy != domain.PolicyRateLimit {
		t.Fatalf("configured policy = %q, want rate_limit", last.policy)
	}
	if len(last.limits.RateLimits) != 2 {
		t.Fatalf("configured barriers = %d, want 2", len(last.limits.RateLimits))
	}
	got := map[domain.AccountID]bool{}
	for _, limit := range last.limits.RateLimits {
		got[limit.Account] = true
	}
	if !got["acc-1"] || !got["acc-2"] {
		t.Fatalf("configured accounts = %+v, want acc-1 and acc-2", got)
	}
}

func TestLocalNode_PutRateLimitEngineFailureRestoresPrevious(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	first := rateLimit(domain.ScopeBroker, "", "", 100, time.Second)
	if _, err := n.PutRateLimit(ctx, first, testCaller); err != nil {
		t.Fatalf("PutRateLimit first: %v", err)
	}

	eng.failConfigure = true
	second := rateLimit(domain.ScopeBroker, "", "", 5, 2*time.Second)
	if _, err := n.PutRateLimit(ctx, second, testCaller); err == nil {
		t.Fatalf("PutRateLimit second: want error")
	}

	stored, err := st.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("want 1 barrier after revert, got %d", len(stored))
	}
	if stored[0].MaxOrders != 100 || stored[0].Window != time.Second {
		t.Fatalf("barrier not restored to previous: %+v", stored[0])
	}
}

func TestLocalNode_DeleteLimitAppliesAndAudits(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	if _, err := n.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	target := LimitTarget{Policy: domain.PolicyRateLimit, Scope: domain.ScopeBroker}
	if _, err := n.DeleteLimit(ctx, target, testCaller); err != nil {
		t.Fatalf("DeleteLimit: %v", err)
	}

	stored, err := st.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("want 0 barriers after delete, got %d", len(stored))
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// Startup hydrate, set_limit, delete_limit = 3 rows, newest first.
	if len(rows) != 3 || rows[0].Action != domain.AuditActionDeleteLimit {
		t.Fatalf("want newest row delete_limit, got %+v", rows)
	}
}

func TestLocalNode_DeleteLastLimitRebuildsFromStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	if _, err := n.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	eng.configureErr = fmt.Errorf("engine: empty policy settings: %w",
		domain.ErrNotImplemented)
	next := newFakeEngine()
	var rebuilt engine.Snapshot
	n.build = fakeBuild(next, &rebuilt)
	target := LimitTarget{Policy: domain.PolicyRateLimit, Scope: domain.ScopeBroker}
	sink, err := n.DeleteLimit(ctx, target, testCaller)
	if err != nil {
		t.Fatalf("DeleteLimit: %v", err)
	}
	if sink == nil {
		t.Fatal("DeleteLimit returned nil sink after rebuild")
	}
	if eng.running {
		t.Fatal("old engine still running after rebuild")
	}
	if !next.running {
		t.Fatal("replacement engine is not running after rebuild")
	}
	if len(rebuilt.RateLimits) != 0 {
		t.Fatalf("rebuilt limits = %+v, want no barriers", rebuilt.RateLimits)
	}
	stored, err := st.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("stored limits = %+v, want none", stored)
	}
}

func TestLocalNode_PutAccountAssetRateLimit(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	// An account-asset scope barrier links a real account and asset; the store
	// enforces that both dictionary rows exist before the barrier is keyed to them.
	seedTestAccount(t, st, "acc-1")
	limit := rateLimit(domain.ScopeAccountAsset, "acc-1", "AAPL", 50, time.Second)
	if _, err := n.PutRateLimit(ctx, limit, testCaller); err != nil {
		t.Fatalf("PutRateLimit account-asset: %v", err)
	}
	if len(eng.configureCalls) != 1 {
		t.Fatalf("configure calls = %d, want 1", len(eng.configureCalls))
	}
}

// TestLocalNode_PutOrderSizeAndPnlBoundsLimits verifies the order-size and
// P&L-bounds put paths persist their typed barrier and configure the matching
// policy from the store, exercising the typed limit surface beyond rate limits.
func TestLocalNode_PutOrderSizeAndPnlBoundsLimits(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	size := domain.LimitOrderSize{Scope: domain.ScopeBroker, MaxQuantity: "100"}
	if _, err := n.PutOrderSizeLimit(ctx, size, testCaller); err != nil {
		t.Fatalf("PutOrderSizeLimit: %v", err)
	}
	pnl := domain.LimitPnlBounds{Scope: domain.ScopeAsset, Asset: "USD", LowerBound: "-1000"}
	if _, err := n.PutPnlBoundsLimit(ctx, pnl, testCaller); err != nil {
		t.Fatalf("PutPnlBoundsLimit: %v", err)
	}

	storedSize, err := st.ListOrderSizeLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListOrderSizeLimits: %v", err)
	}
	if len(storedSize) != 1 || storedSize[0] != size {
		t.Fatalf("order-size barriers = %+v, want the one barrier", storedSize)
	}
	storedPnl, err := st.ListPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListPnlBoundsLimits: %v", err)
	}
	if len(storedPnl) != 1 || storedPnl[0] != pnl {
		t.Fatalf("pnl-bounds barriers = %+v, want the one barrier", storedPnl)
	}

	// Two configure calls, one per policy, each carrying only its own slice.
	if len(eng.configureCalls) != 2 {
		t.Fatalf("configure calls = %d, want 2", len(eng.configureCalls))
	}
	if eng.configureCalls[0].policy != domain.PolicyOrderSizeLimit ||
		len(eng.configureCalls[0].limits.OrderSizeLimits) != 1 {
		t.Fatalf("first configure = %+v, want order-size policy", eng.configureCalls[0])
	}
	if eng.configureCalls[1].policy != domain.PolicyPnlBoundsKillSwitch ||
		len(eng.configureCalls[1].limits.PnlBoundsLimits) != 1 {
		t.Fatalf("second configure = %+v, want pnl-bounds policy", eng.configureCalls[1])
	}

	// ListLimits bundles the matching barriers per policy into AccountLimits.
	limits, err := n.ListLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListLimits: %v", err)
	}
	if len(limits.OrderSizeLimits) != 1 || len(limits.PnlBoundsLimits) != 1 ||
		len(limits.RateLimits) != 0 {
		t.Fatalf("listed limits = %+v, want one order-size and one pnl-bounds", limits)
	}
}
