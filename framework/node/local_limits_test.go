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
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

func preparePolicyLifecycleBuild(
	n *localNode,
) (*fakeEngine, *engine.Snapshot) {
	next := newFakeEngine()
	snapshot := new(engine.Snapshot)
	n.build = fakeBuild(next, snapshot)
	return next, snapshot
}

func TestLocalNode_PutRateLimitAppliesAndAudits(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	preparePolicyLifecycleBuild(n)
	ctx := context.Background()

	if _, err := n.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), domain.MissingAccountCreate, testCaller); err != nil {
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
	preparePolicyLifecycleBuild(n)
	ctx := context.Background()

	limit := rateLimit(domain.ScopeAsset, "", "GOLD", 100, time.Second)
	if _, err := n.PutRateLimit(ctx, limit, domain.MissingAccountCreate, testCaller); err != nil {
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

	if _, err := n.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), domain.MissingAccountCreate, testCaller); err == nil {
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

// TestLocalNode_FailedRateLimitKeepsExplicitlyCreatedAccount locks the retry
// contract: missingAccount=create persists the identity even when the barrier
// itself is reverted after an engine failure.
func TestLocalNode_FailedRateLimitKeepsExplicitlyCreatedAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.failConfigure = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	_, err := n.PutRateLimit(
		ctx,
		rateLimit(domain.ScopeAccount, "fresh", "", 100, time.Second),
		domain.MissingAccountCreate,
		testCaller,
	)
	if err == nil {
		t.Fatal("PutRateLimit: want error on engine failure")
	}

	assertAutoCreatedAccount(t, st, "fresh", "put rate limit")
	stored, listErr := st.ListRateLimits(ctx, "fresh")
	if listErr != nil {
		t.Fatalf("ListRateLimits: %v", listErr)
	}
	if len(stored) != 0 {
		t.Fatalf("stored rate limits = %+v, want reverted barrier", stored)
	}
}

func TestLocalNode_PutRateLimitConfiguresPolicyFromStore(t *testing.T) {
	t.Parallel()
	old := newFakeEngine()
	n, _ := newTestNode(t, old)
	next, snapshot := preparePolicyLifecycleBuild(n)
	ctx := context.Background()

	limit := rateLimit(domain.ScopeBroker, "", "", 100, time.Second)
	if _, err := n.PutRateLimit(ctx, limit, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	if old.running {
		t.Fatal("old engine still running after first policy barrier")
	}
	if !next.running {
		t.Fatal("prepared engine is not running")
	}
	if len(snapshot.RateLimits) != 1 || snapshot.RateLimits[0] != limit {
		t.Fatalf("prepared rate limits = %+v, want the new barrier", snapshot.RateLimits)
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
	sink, err := n.PutRateLimit(ctx, limit, domain.MissingAccountCreate, testCaller)
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

// TestLocalNode_CreateAccountPublishesIntoLiveEngine verifies a freshly created
// account is immediately routable without replacing the engine.
func TestLocalNode_CreateAccountPublishesIntoLiveEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}
	sink := n.CurrentMarketDataSink()

	if _, err := n.CreateAccount(ctx, testAccount("fresh"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := eng.AccountID("fresh"); err != nil {
		t.Fatalf("new account is not routable: %v", err)
	}
	if builds != 0 {
		t.Fatalf("build calls = %d, want none", builds)
	}
	if !eng.running || n.currentEngine() != eng || n.CurrentMarketDataSink() != sink {
		t.Fatal("CreateAccount replaced the live engine or market-data sink")
	}
}

// TestLocalNode_CreateGroupPublishesIntoLiveEngine verifies a freshly created
// group is immediately routable without replacing the engine.
func TestLocalNode_CreateGroupPublishesIntoLiveEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}
	sink := n.CurrentMarketDataSink()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "vips"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := eng.ResolveGroup("vips"); err != nil {
		t.Fatalf("new group is not routable: %v", err)
	}
	if builds != 0 {
		t.Fatalf("build calls = %d, want none", builds)
	}
	if !eng.running || n.currentEngine() != eng || n.CurrentMarketDataSink() != sink {
		t.Fatal("CreateGroup replaced the live engine or market-data sink")
	}
}

func TestLocalNode_PutRateLimitSamePolicyDifferentAccountsRetunesOnePolicy(t *testing.T) {
	t.Parallel()
	old := newFakeEngine()
	n, _ := newTestNode(t, old)
	ctx := context.Background()
	buildCalls := 0
	current := newFakeEngine()
	n.build = func(snap engine.Snapshot) (engine.Engine, error) {
		buildCalls++
		return fakeBuild(current, new(engine.Snapshot))(snap)
	}

	seedTestAccount(t, n.realm, "acc-1")
	seedTestAccount(t, n.realm, "acc-2")
	first := rateLimit(domain.ScopeAccount, "acc-1", "", 100, time.Second)
	second := rateLimit(domain.ScopeAccount, "acc-2", "", 10, time.Second)
	if _, err := n.PutRateLimit(ctx, first, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutRateLimit first: %v", err)
	}
	if _, err := n.PutRateLimit(ctx, second, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutRateLimit second: %v", err)
	}

	if buildCalls != 1 {
		t.Fatalf("build calls = %d, want one lifecycle build", buildCalls)
	}
	if len(current.configureCalls) != 1 {
		t.Fatalf("configure calls = %d, want one online retune", len(current.configureCalls))
	}
	last := current.configureCalls[0]
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
	old := newFakeEngine()
	n, st := newTestNode(t, old)
	current, _ := preparePolicyLifecycleBuild(n)
	ctx := context.Background()

	first := rateLimit(domain.ScopeBroker, "", "", 100, time.Second)
	if _, err := n.PutRateLimit(ctx, first, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutRateLimit first: %v", err)
	}

	current.failConfigure = true
	second := rateLimit(domain.ScopeBroker, "", "", 5, 2*time.Second)
	if _, err := n.PutRateLimit(ctx, second, domain.MissingAccountCreate, testCaller); err == nil {
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
	preparePolicyLifecycleBuild(n)
	ctx := context.Background()

	if _, err := n.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	preparePolicyLifecycleBuild(n)
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
	old := newFakeEngine()
	n, st := newTestNode(t, old)
	current, _ := preparePolicyLifecycleBuild(n)
	ctx := context.Background()

	if _, err := n.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

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
	if current.running {
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
	_, snapshot := preparePolicyLifecycleBuild(n)
	ctx := context.Background()

	// An account-asset scope barrier links a real account and asset; the store
	// enforces that both dictionary rows exist before the barrier is keyed to them.
	seedTestAccount(t, st, "acc-1")
	limit := rateLimit(domain.ScopeAccountAsset, "acc-1", "AAPL", 50, time.Second)
	if _, err := n.PutRateLimit(ctx, limit, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutRateLimit account-asset: %v", err)
	}
	if len(snapshot.RateLimits) != 1 || snapshot.RateLimits[0] != limit {
		t.Fatalf("prepared rate limits = %+v, want account-asset barrier", snapshot.RateLimits)
	}
}

// TestLocalNode_PutOrderSizeLimit verifies the typed order-size barrier is
// persisted, applied to the engine, and returned by the limits read model.
func TestLocalNode_PutOrderSizeLimit(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	_, snapshot := preparePolicyLifecycleBuild(n)
	ctx := context.Background()

	size := domain.LimitOrderSize{Scope: domain.ScopeBroker, MaxQuantity: "100"}
	if _, err := n.PutOrderSizeLimit(ctx, size, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutOrderSizeLimit: %v", err)
	}

	stored, err := st.ListOrderSizeLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListOrderSizeLimits: %v", err)
	}
	if len(stored) != 1 || stored[0] != size {
		t.Fatalf("order-size barriers = %+v, want the one barrier", stored)
	}
	if len(snapshot.OrderSizeLimits) != 1 || snapshot.OrderSizeLimits[0] != size {
		t.Fatalf("prepared order-size limits = %+v, want the new barrier", snapshot.OrderSizeLimits)
	}

	limits, err := n.ListLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListLimits: %v", err)
	}
	if len(limits.OrderSizeLimits) != 1 || len(limits.RateLimits) != 0 {
		t.Fatalf("listed limits = %+v, want one order-size barrier", limits)
	}
}

type liveConfigureProbeEngine struct {
	*fakeEngine
	onConfigure func()
}

func (e *liveConfigureProbeEngine) ConfigurePolicy(
	ctx context.Context, policy string, limits engine.LimitSet,
) (engine.PolicyConfigurationResult, error) {
	e.onConfigure()
	return e.fakeEngine.ConfigurePolicy(ctx, policy, limits)
}

func setLiveConfigureProbe(
	t *testing.T, n *localNode, eng *fakeEngine,
) {
	t.Helper()
	n.engineMu.Lock()
	n.engine = &liveConfigureProbeEngine{
		fakeEngine: eng,
		onConfigure: func() {
			if n.restarting.Load() {
				t.Error("live policy reconfiguration entered the engine-restart gate")
			}
		},
	}
	n.engineMu.Unlock()
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		t.Error("live policy reconfiguration rebuilt the engine")
		return nil, fmt.Errorf("unexpected engine rebuild")
	}
}

func TestLocalNode_PutSpotFundsPnlBoundsLimitConfiguresLiveEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	setLiveConfigureProbe(t, n, eng)

	limit := domain.LimitSpotFundsPnlBounds{
		Scope:      domain.ScopeGlobal,
		Currency:   "USD",
		LowerBound: "-100",
	}
	sink, err := n.PutSpotFundsPnlBoundsLimit(ctx, limit, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}
	if sink != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit sink = %T, want nil", sink)
	}

	stored, err := st.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", err)
	}
	if len(stored) != 1 || stored[0] != limit {
		t.Fatalf("stored SpotFunds P&L-bounds limits = %+v, want %+v", stored, limit)
	}
	if len(eng.configureCalls) != 1 ||
		eng.configureCalls[0].policy != domain.PolicySpotFundsPnlBoundsKillSwitch ||
		len(eng.configureCalls[0].limits.SpotFundsPnlBoundsLimits) != 1 ||
		eng.configureCalls[0].limits.SpotFundsPnlBoundsLimits[0] != limit {
		t.Fatalf("configure calls = %+v, want live SpotFunds update", eng.configureCalls)
	}

	rows, err := n.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 2 || rows[0].Action != domain.AuditActionSetLimit ||
		!strings.Contains(rows[0].Detail, "currency=USD") {
		t.Fatalf(
			"want newest set_limit with currency=USD over startup hydrate, got %+v",
			rows,
		)
	}
}

func TestLocalNode_PolicyConfigurationBlockPreservesFirstCause(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetAccountBlocked(
		ctx, "acc-1", true, "operator hold", domain.MissingAccountCreate, testCaller,
	); err != nil {
		t.Fatalf("SetAccountBlocked: %v", err)
	}
	eng.configureBlocks = []domain.AccountBlock{{
		Account: "acc-1", Policy: domain.PolicySpotFundsPnlBoundsKillSwitch,
		Code: "pnl_bound_breached", Reason: "later SDK cause",
	}}
	if _, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-100",
	}, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if !account.Blocked || account.BlockReason != "operator hold" {
		t.Fatalf("account block = (%v, %q), want original operator cause",
			account.Blocked, account.BlockReason)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionBlock}, Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("block audits = %+v, want only original first cause", rows)
	}
}

func TestLocalNode_SpotFundsLimitAuditFailureFatalsAfterConfigure(t *testing.T) {
	t.Parallel()
	auditErr := errors.New("set limit audit failed")
	st := newRealmWrapStore(newMemoryStore("limits.db"), func(r store.RealmStore) store.RealmStore {
		return &failActionAuditRealm{
			RealmStore: r, action: domain.AuditActionSetLimit, err: auditErr,
		}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, func(err error) {
		fatalErr = err
	})
	seedTestAccount(t, n.realm, "acc-1")

	_, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeAccount, Account: "acc-1",
		Currency:   "USD",
		LowerBound: "-100",
	}, domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, auditErr) {
		t.Fatalf("PutSpotFundsPnlBoundsLimit error = %v, want audit failure", err)
	}
	if fatalErr == nil || !strings.Contains(fatalErr.Error(), "audit spot funds pnl-bounds limit") {
		t.Fatalf("fatal error = %v, want post-engine limit audit failure", fatalErr)
	}
}

// rebuildProbe counts the engine (re)builds a node performs and captures the
// snapshot of the last one, so a test can prove what the node rebuilt from.
type rebuildProbe struct {
	builds int
	last   engine.Snapshot
}

func newRebuildProbeNode(t *testing.T, eng *fakeEngine) (*localNode, *rebuildProbe) {
	t.Helper()
	ctx := context.Background()
	st := newMemoryStore("rebuild.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	probe := &rebuildProbe{}
	var seed engine.Snapshot
	inner := fakeBuild(eng, &seed)
	n, _, err := NewLocalNode(ctx, domain.DefaultRealm, st, func(snap engine.Snapshot) (engine.Engine, error) {
		probe.builds++
		probe.last = snap
		return inner(snap)
	}, failOnFatal(t))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	local := n.(*localNode)
	seedTestPrincipal(t, local)
	return local, probe
}

// TestLocalNode_FailedSpotFundsConfigureRebuildsFromRevertedStore pins the
// contract that a failed P&L-bounds configure is reconciled from the reverted
// store. The rebuild must therefore read a store that no longer carries the
// failed barrier.
func TestLocalNode_FailedSpotFundsConfigureRebuildsFromRevertedStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newRebuildProbeNode(t, eng)
	ctx := context.Background()

	const account domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(account), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	buildsBefore := probe.builds

	eng.failConfigure = true
	sink, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope:      domain.ScopeAccount,
		Account:    account,
		Currency:   "USD",
		LowerBound: "-100",
	}, domain.MissingAccountCreate, testCaller)
	if err == nil {
		t.Fatal("PutSpotFundsPnlBoundsLimit succeeded, want the engine failure")
	}
	if probe.builds != buildsBefore+1 {
		t.Fatalf("rebuilds = %d, want exactly one from the reverted store",
			probe.builds-buildsBefore)
	}
	if len(probe.last.SpotFundsPnlBoundsLimits) != 0 {
		t.Fatalf("rebuilt from %+v, want the store reverted before the rebuild read it",
			probe.last.SpotFundsPnlBoundsLimits)
	}
	if sink == nil {
		t.Fatal("sink = nil after the rebuild replaced the handle, want the new sink")
	}

	stored, err := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("stored barriers = %+v, want the failed barrier reverted", stored)
	}
}

func TestLocalNode_FailedSpotFundsConfigureRebuildFailureFatals(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newRebuildProbeNode(t, eng)
	ctx := context.Background()
	const account domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(account), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	configureErr := errors.New("partial configure failed")
	rebuildErr := errors.New("reconciliation rebuild failed")
	eng.configureErr = configureErr
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, rebuildErr
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	_, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope:      domain.ScopeAccount,
		Account:    account,
		Currency:   "USD",
		LowerBound: "-100",
	}, domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, configureErr) || !errors.Is(err, rebuildErr) {
		t.Fatalf("PutSpotFundsPnlBoundsLimit error = %v, want configure and rebuild failures", err)
	}
	if fatalErr == nil ||
		!strings.Contains(fatalErr.Error(), "rebuild after failed spot funds pnl-bounds configuration") {
		t.Fatalf("fatal error = %v, want failed reconciliation fail-stop", fatalErr)
	}
}

// TestLocalNode_NotImplementedSpotFundsConfigureDoesNotRebuild is the boundary
// to the case above: the engine's not-implemented stub is a pre-flight guard
// that rejects before mutating anything, so there is nothing to reconcile and
// the node must revert without churning the handle.
func TestLocalNode_NotImplementedSpotFundsConfigureDoesNotRebuild(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newRebuildProbeNode(t, eng)
	ctx := context.Background()
	buildsBefore := probe.builds

	eng.configureErr = fmt.Errorf("configure spot funds: %w", domain.ErrNotImplemented)
	sink, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope:      domain.ScopeGlobal,
		Currency:   "USD",
		LowerBound: "-100",
	}, domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("error = %v, want ErrNotImplemented", err)
	}
	if sink != nil {
		t.Fatalf("sink = %T, want nil without a rebuild", sink)
	}
	if probe.builds != buildsBefore {
		t.Fatalf("rebuilds = %d, want none for a pre-flight rejection",
			probe.builds-buildsBefore)
	}
}

// TestLocalNode_PolicyConfigurationBlocksPersistAndAudit verifies an accepted
// policy update mirrors every SDK-reported block into the account state before
// the caller receives success, and records the engine cause as a system audit.
func TestLocalNode_PolicyConfigurationBlocksPersistAndAudit(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.configureBlocks = []domain.AccountBlock{{
		Account: "acc-1",
		Policy:  "openpit.spot_funds",
		Code:    "missing_fx",
		Reason:  "account P&L halted",
		Details: "USD/EUR quote unavailable",
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const account domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(account), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope:      domain.ScopeGlobal,
		Currency:   "USD",
		LowerBound: "-100",
	}, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}

	stored, ok, err := st.GetAccount(ctx, account)
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	// The stored reason is the engine's own, verbatim; the policy, code and
	// details are carried by the audit line below.
	wantReason := "account P&L halted"
	if !stored.Blocked || stored.BlockReason != wantReason {
		t.Fatalf("stored account = %+v, want blocked reason %q", stored, wantReason)
	}

	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("block audits = %+v, want one", rows)
	}
	if rows[0].Source != domain.SourceSystem || rows[0].Actor != "" {
		t.Fatalf("block audit attribution = %+v, want system without actor", rows[0])
	}
	if !strings.Contains(rows[0].Detail, "policy openpit.spot_funds") ||
		!strings.Contains(rows[0].Detail, "code=missing_fx") ||
		!strings.Contains(rows[0].Detail, "USD/EUR quote unavailable") {
		t.Fatalf("block audit detail = %q, want policy, code and engine details", rows[0].Detail)
	}
}

// TestLocalNode_PolicyConfigurationBlockReasonIsRestorable pins the mirror seam
// for an engine-produced reason. The engine-boundary scrub only strips control
// characters, so a format-class rune such as U+200B survives into this write and
// the restore path rejects it. Refusing the write would drop a real kill-switch
// record, so the reason is normalized and the realm stays restorable - including
// from Officer's own rollback archive.
func TestLocalNode_PolicyConfigurationBlockReasonIsRestorable(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.configureBlocks = []domain.AccountBlock{{
		Account: "acc-1",
		Policy:  "openpit.spot_funds",
		Code:    "missing_fx",
		Reason:  "account P&L\u200bhalted",
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	const account domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(account), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope:      domain.ScopeGlobal,
		Currency:   "USD",
		LowerBound: "-100",
	}, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}

	stored, ok, err := st.GetAccount(ctx, account)
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if !stored.Blocked {
		t.Fatal("mirrored policy configuration block did not block the account")
	}
	wantReason := "account P&Lhalted"
	if stored.BlockReason != wantReason {
		t.Fatalf("block reason = %q, want %q", stored.BlockReason, wantReason)
	}
	if err := domain.ValidateBlockReason(stored.BlockReason); err != nil {
		t.Fatalf("persisted block reason is not restorable: %v", err)
	}
}

func TestLocalNode_DeleteLastSpotFundsPnlBoundsLimitConfiguresLiveEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	limit := domain.LimitSpotFundsPnlBounds{
		Scope:      domain.ScopeGlobal,
		Currency:   "USD",
		LowerBound: "-100",
	}
	if _, err := n.PutSpotFundsPnlBoundsLimit(ctx, limit, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}
	setLiveConfigureProbe(t, n, eng)

	target := LimitTarget{
		Policy: domain.PolicySpotFundsPnlBoundsKillSwitch,
		Scope:  limit.Scope,
	}
	sink, err := n.DeleteLimit(ctx, target, testCaller)
	if err != nil {
		t.Fatalf("DeleteLimit: %v", err)
	}
	if sink != nil {
		t.Fatalf("DeleteLimit sink = %T, want nil", sink)
	}

	stored, err := st.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("stored SpotFunds P&L-bounds limits = %+v, want none", stored)
	}
	if len(eng.configureCalls) != 2 ||
		eng.configureCalls[1].policy != domain.PolicySpotFundsPnlBoundsKillSwitch ||
		len(eng.configureCalls[1].limits.SpotFundsPnlBoundsLimits) != 0 {
		t.Fatalf("configure calls = %+v, want empty live SpotFunds update", eng.configureCalls)
	}

	rows, err := n.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 3 || rows[0].Action != domain.AuditActionDeleteLimit {
		t.Fatalf("want newest delete_limit over set_limit and hydrate, got %+v", rows)
	}
}

func TestLocalNode_DeleteSpotFundsPnlBoundsLimitNotImplementedRevertsWithoutRebuild(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	limit := domain.LimitSpotFundsPnlBounds{
		Scope:      domain.ScopeGlobal,
		Currency:   "USD",
		LowerBound: "-100",
	}
	if _, err := n.PutSpotFundsPnlBoundsLimit(ctx, limit, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}
	setLiveConfigureProbe(t, n, eng)
	eng.configureErr = fmt.Errorf("configure spot funds: %w", domain.ErrNotImplemented)

	target := LimitTarget{
		Policy: domain.PolicySpotFundsPnlBoundsKillSwitch,
		Scope:  limit.Scope,
	}
	sink, err := n.DeleteLimit(ctx, target, testCaller)
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("DeleteLimit error = %v, want ErrNotImplemented", err)
	}
	if sink != nil {
		t.Fatalf("DeleteLimit sink = %T, want nil", sink)
	}

	stored, err := st.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", err)
	}
	if len(stored) != 1 || stored[0] != limit {
		t.Fatalf("stored SpotFunds P&L-bounds limits = %+v, want %+v", stored, limit)
	}

	rows, err := n.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 2 || rows[0].Action != domain.AuditActionSetLimit {
		t.Fatalf("want no delete audit after rejected reconfiguration, got %+v", rows)
	}
}
