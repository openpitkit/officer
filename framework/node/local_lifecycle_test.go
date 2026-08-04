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
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
)

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

	// Pre-seed the store: one account and one rate-limit barrier.
	if _, err := realm.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := realm.PutRateLimit(ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second)); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	eng := newFakeEngine()
	var seed engine.Snapshot
	n, got, err := NewLocalNode(ctx, st, fakeBuild(eng, &seed))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	if got != eng {
		t.Fatalf("NewLocalNode returned a different engine handle")
	}

	// The build must be seeded from the store snapshot.
	if len(seed.Accounts) != 1 || len(seed.RateLimits) != 1 {
		t.Fatalf("build not seeded from store: %+v", seed)
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
	if _, _, err := NewLocalNode(ctx, st, failBuild); err == nil {
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
	if _, _, err := NewLocalNode(ctx, nil, build); err == nil {
		t.Fatalf("want error for nil store")
	}

	st := newMemoryStore("node.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, _, err := NewLocalNode(ctx, st, nil); err == nil {
		t.Fatalf("want error for nil build func")
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
	out, err := n.CheckOrder(ctx, testKey("acc-1"), probe)
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if !out.Passed || out.WouldLockPrice != "100" {
		t.Fatalf("engine result not propagated: %+v", out)
	}
	if len(eng.checkProbes) != 1 || eng.checkProbes[0].Account != "acc-1" {
		t.Fatalf("probe must be forwarded to the engine once")
	}
	if eng.operationOutsideSync {
		t.Fatal("check order ran outside the account sync lane")
	}
	if len(eng.accountSyncCalls) == 0 ||
		eng.accountSyncCalls[len(eng.accountSyncCalls)-1] != "acc-1" {
		t.Fatalf("account sync calls = %+v, want last acc-1", eng.accountSyncCalls)
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
		if _, err := n.CheckOrder(ctx, testKey("acc-1"), probe); err != nil {
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
