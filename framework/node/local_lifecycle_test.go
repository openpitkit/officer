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
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
)

type lifecycleMarketDataServiceOwner struct {
	once  sync.Once
	calls int
}

func (o *lifecycleMarketDataServiceOwner) Close() {
	o.once.Do(func() { o.calls++ })
}

type lifecycleMarketDataEngine struct {
	engine.Engine
	owner *lifecycleMarketDataServiceOwner
}

func (e lifecycleMarketDataEngine) CloseMarketDataService() {
	e.owner.Close()
}

func TestLocalNode_SharedMarketDataServiceClosesOnlyAtFinalShutdown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newMemoryStore("shared-market-data-lifecycle.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = st.Close()
		}
	})

	owner := &lifecycleMarketDataServiceOwner{}
	first := newFakeEngine()
	second := newFakeEngine()
	builds := 0
	build := func(snapshot engine.Snapshot) (engine.Engine, error) {
		var next *fakeEngine
		switch builds {
		case 0:
			next = first
		case 1:
			next = second
		default:
			t.Fatalf("unexpected build %d", builds+1)
		}
		builds++
		built, err := fakeBuild(next, new(engine.Snapshot))(snapshot)
		if err != nil {
			return nil, err
		}
		return lifecycleMarketDataEngine{Engine: built, owner: owner}, nil
	}

	nodeValue, _, err := NewLocalNode(ctx, domain.DefaultRealm, st, build, failOnFatal(t))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nodeValue.(*localNode)
	if err := n.realm.CreatePrincipal(
		ctx, domain.Principal{Code: testCaller.Principal},
	); err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := n.realm.CreateAsset(ctx, domain.Asset{Code: "AAPL"}); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	if err := n.DeleteAsset(ctx, "AAPL", true, testCaller); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	if first.running || !second.running {
		t.Fatalf("engine rebuild state: first=%v second=%v", first.running, second.running)
	}
	if owner.calls != 0 {
		t.Fatalf("service closes after intermediate replacement = %d, want 0", owner.calls)
	}

	if err := n.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed = true
	if second.running {
		t.Fatal("final engine is still running after node shutdown")
	}
	if owner.calls != 1 {
		t.Fatalf("service closes after final shutdown = %d, want 1", owner.calls)
	}
}

func TestNewLocalNode_SeedsBuildAndAudits(t *testing.T) {
	t.Parallel()
	st := newMemoryStore("node.db")
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}

	// Pre-seed the store: one asset, one account and one rate-limit barrier.
	createdAsset, err := realm.CreateAsset(ctx, domain.Asset{Code: "AAPL"})
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	if _, err := realm.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := realm.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second)); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	eng := newFakeEngine()
	var seed engine.Snapshot
	n, got, err := NewLocalNode(ctx, domain.DefaultRealm, st, fakeBuild(eng, &seed), failOnFatal(t))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	if got != eng {
		t.Fatalf("NewLocalNode returned a different engine handle")
	}

	// The build must be seeded from the store snapshot.
	if len(seed.Assets) != 1 || len(seed.Accounts) != 1 || len(seed.RateLimits) != 1 {
		t.Fatalf("build not seeded from store: %+v", seed)
	}
	if seed.Assets[0] != createdAsset {
		t.Fatalf("seeded wrong asset: %+v", seed.Assets)
	}
	if seed.Accounts[0].Code != "acc-1" {
		t.Fatalf("seeded wrong account: %+v", seed.Accounts)
	}

	rows, err := realm.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// The startup hydrate is system-initiated: SourceSystem with an empty actor
	// (system origin carries no principal dictionary code).
	if len(rows) != 1 ||
		rows[0].Action != domain.AuditActionHydrate ||
		rows[0].Actor != "" || rows[0].Source != domain.SourceSystem {
		t.Fatalf("want one system hydrate row, got %+v", rows)
	}

	if err := n.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestNewLocalNode_BuildFailure verifies a build failure is surfaced and no node
// is returned.
func TestNewLocalNode_BuildFailure(t *testing.T) {
	t.Parallel()
	st := newMemoryStore("node.db")
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	failBuild := func(engine.Snapshot) (engine.Engine, error) {
		return nil, errors.New("build boom")
	}
	if _, _, err := NewLocalNode(ctx, domain.DefaultRealm, st, failBuild, failOnFatal(t)); err == nil {
		t.Fatalf("NewLocalNode: want error on build failure")
	}

	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	// A failed build writes no audit row.
	rows, err := realm.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("want no audit row on build failure, got %+v", rows)
	}
}

// TestNewLocalNode_NilArgs verifies the constructor rejects nil dependencies.
func TestNewLocalNode_NilArgs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	build := func(engine.Snapshot) (engine.Engine, error) {
		return newFakeEngine(), nil
	}
	if _, _, err := NewLocalNode(ctx, domain.DefaultRealm, nil, build, failOnFatal(t)); err == nil {
		t.Fatalf("want error for nil store")
	}

	st := newMemoryStore("node.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, _, err := NewLocalNode(ctx, domain.DefaultRealm, st, nil, failOnFatal(t)); err == nil {
		t.Fatalf("want error for nil build func")
	}
	if _, _, err := NewLocalNode(ctx, domain.DefaultRealm, st, build, nil); err == nil ||
		!strings.Contains(err.Error(), "nil fatal shutdown hook") {
		t.Fatalf("NewLocalNode(nil fatal hook) = %v, want the missing hook named", err)
	}
}

func TestLocalNode_CheckOrderDelegatesToEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.checkResult = domain.CheckResult{Passed: true, WouldLockPrice: "100"}
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	probe := domain.OrderProbe{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
		AmountValue: "1", Price: "100",
	}
	out, err := n.CheckOrder(ctx, probe)
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if !out.Passed || out.WouldLockPrice != "100" {
		t.Fatalf("engine result not propagated: %+v", out)
	}
	if len(eng.checkProbes) != 1 || eng.checkProbes[0].Account != "acc-1" {
		t.Fatalf("probe must be forwarded to the engine once")
	}
}

// TestLocalNode_CheckOrderWritesNoAudit asserts the non-mutating check writes
// no audit row and is side-effect-free across repeated calls: the audit count
// is unchanged from before the first check, and the engine is only ever asked
// to dry-run (the node never calls SubmitOrder/commit for a check).
func TestLocalNode_CheckOrderWritesNoAudit(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.checkResult = domain.CheckResult{
		Passed:  false,
		Rejects: []domain.OrderReject{{Code: "rate_limit_exceeded", Scope: "account"}},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	before, err := st.ListAudit(ctx, 1000)
	if err != nil {
		t.Fatalf("ListAudit before: %v", err)
	}

	probe := domain.OrderProbe{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
	}
	for i := 0; i < 3; i++ {
		if _, err := n.CheckOrder(ctx, probe); err != nil {
			t.Fatalf("CheckOrder #%d: %v", i, err)
		}
	}

	after, err := st.ListAudit(ctx, 1000)
	if err != nil {
		t.Fatalf("ListAudit after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("check must write no audit row: before=%d after=%d", len(before), len(after))
	}
	if len(eng.submitCalls) != 0 || len(eng.execReportCalls) != 0 {
		t.Fatalf("check must not submit or settle: submit=%d exec=%d",
			len(eng.submitCalls), len(eng.execReportCalls))
	}
	if len(eng.checkProbes) != 3 {
		t.Fatalf("want 3 dry-run calls, got %d", len(eng.checkProbes))
	}
}
