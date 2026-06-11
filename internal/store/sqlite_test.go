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

package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/store"
)

// openStore opens a fresh temp-file SQLite store and migrates it. The caller
// does not need to call Close; it is registered with t.Cleanup.
func openStore(t *testing.T) store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// --- Migration ---

func TestMigration_Chain(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()

	v, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 1 {
		t.Fatalf("want schema version 1, got %d", v)
	}
}

func TestMigration_Idempotent(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()

	// Run Migrate a second time; should be a no-op.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	v, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 1 {
		t.Fatalf("want schema version 1, got %d", v)
	}
}

// --- Ping / Path ---

func TestPingAndPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ping.db")
	s, err := store.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if s.Path() != path {
		t.Fatalf("Path: want %q, got %q", path, s.Path())
	}
}

// --- Account CRUD ---

func TestAccount_Create_Get_List(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()

	acc := domain.Account{
		Tenant:      domain.DefaultTenant,
		ID:          "acc-1",
		Blocked:     false,
		BlockReason: "",
	}
	if err := s.CreateAccount(ctx, acc); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	got, ok, err := s.GetAccount(ctx, domain.DefaultTenant, "acc-1")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if !ok {
		t.Fatal("GetAccount: not found")
	}
	if got != acc {
		t.Fatalf("GetAccount: want %+v, got %+v", acc, got)
	}

	list, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(list) != 1 || list[0] != acc {
		t.Fatalf("ListAccounts: unexpected %+v", list)
	}
}

func TestAccount_GetNotFound(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()
	_, ok, err := s.GetAccount(ctx, domain.DefaultTenant, "missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected not found")
	}
}

func TestAccount_Create_AlreadyExists(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()
	acc := domain.Account{Tenant: domain.DefaultTenant, ID: "dup"}
	if err := s.CreateAccount(ctx, acc); err != nil {
		t.Fatalf("first CreateAccount: %v", err)
	}
	err := s.CreateAccount(ctx, acc)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestAccount_SetBlocked(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()
	acc := domain.Account{Tenant: domain.DefaultTenant, ID: "acc-block"}
	if err := s.CreateAccount(ctx, acc); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := s.SetAccountBlocked(ctx, domain.DefaultTenant, "acc-block", true, "manual"); err != nil {
		t.Fatalf("SetAccountBlocked: %v", err)
	}
	got, _, err := s.GetAccount(ctx, domain.DefaultTenant, "acc-block")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if !got.Blocked || got.BlockReason != "manual" {
		t.Fatalf("want blocked=true reason=manual, got %+v", got)
	}

	if err := s.SetAccountBlocked(ctx, domain.DefaultTenant, "acc-block", false, ""); err != nil {
		t.Fatalf("SetAccountBlocked unblock: %v", err)
	}
	got, _, err = s.GetAccount(ctx, domain.DefaultTenant, "acc-block")
	if err != nil {
		t.Fatalf("GetAccount after unblock: %v", err)
	}
	if got.Blocked || got.BlockReason != "" {
		t.Fatalf("want unblocked, got %+v", got)
	}
}

func TestAccount_SetBlocked_NotFound(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()
	err := s.SetAccountBlocked(ctx, domain.DefaultTenant, "ghost", true, "x")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestAccount_ListAccounts_BlockReason(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()
	a1 := domain.Account{Tenant: domain.DefaultTenant, ID: "a1"}
	a2 := domain.Account{Tenant: domain.DefaultTenant, ID: "a2"}
	if err := s.CreateAccount(ctx, a1); err != nil {
		t.Fatalf("CreateAccount a1: %v", err)
	}
	if err := s.CreateAccount(ctx, a2); err != nil {
		t.Fatalf("CreateAccount a2: %v", err)
	}
	if err := s.SetAccountBlocked(ctx, domain.DefaultTenant, "a2", true, "risk"); err != nil {
		t.Fatalf("SetAccountBlocked: %v", err)
	}

	list, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 accounts, got %d", len(list))
	}
	// ordered by tenant,id
	if list[0].ID != "a1" || list[0].Blocked {
		t.Fatalf("unexpected a1: %+v", list[0])
	}
	if list[1].ID != "a2" || !list[1].Blocked || list[1].BlockReason != "risk" {
		t.Fatalf("unexpected a2: %+v", list[1])
	}
}

// --- Limits ---

func makeLimit(policy, scope, account, asset string, vals ...domain.LimitValue) domain.Limit {
	return domain.Limit{
		Target: domain.LimitTarget{
			Tenant:  domain.DefaultTenant,
			Policy:  policy,
			Scope:   scope,
			Account: domain.AccountID(account),
			Asset:   asset,
		},
		Values: vals,
	}
}

func TestLimits_PutAndList(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()

	lim := makeLimit(
		domain.PolicyRateLimit, domain.ScopeBroker, "", "",
		domain.LimitValue{Kind: domain.KindMaxOrders, Value: "100"},
		domain.LimitValue{Kind: domain.KindWindow, Value: "1s"},
	)
	if err := s.PutLimit(ctx, lim); err != nil {
		t.Fatalf("PutLimit: %v", err)
	}

	got, err := s.ListLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListLimits: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 barrier, got %d", len(got))
	}
	if got[0].Target != lim.Target {
		t.Fatalf("target mismatch: %+v", got[0].Target)
	}
	if len(got[0].Values) != 2 {
		t.Fatalf("want 2 values, got %d", len(got[0].Values))
	}
}

func TestLimits_Upsert_Replace(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()

	lim := makeLimit(
		domain.PolicyRateLimit, domain.ScopeBroker, "", "",
		domain.LimitValue{Kind: domain.KindMaxOrders, Value: "100"},
		domain.LimitValue{Kind: domain.KindWindow, Value: "1s"},
	)
	if err := s.PutLimit(ctx, lim); err != nil {
		t.Fatalf("PutLimit initial: %v", err)
	}

	// Replace with updated window; max_orders stays.
	lim2 := makeLimit(
		domain.PolicyRateLimit, domain.ScopeBroker, "", "",
		domain.LimitValue{Kind: domain.KindMaxOrders, Value: "200"},
		domain.LimitValue{Kind: domain.KindWindow, Value: "5s"},
	)
	if err := s.PutLimit(ctx, lim2); err != nil {
		t.Fatalf("PutLimit replace: %v", err)
	}

	got, err := s.ListLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListLimits: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 barrier, got %d", len(got))
	}
	valMap := make(map[string]string)
	for _, v := range got[0].Values {
		valMap[v.Kind] = v.Value
	}
	if valMap[domain.KindMaxOrders] != "200" || valMap[domain.KindWindow] != "5s" {
		t.Fatalf("unexpected values: %+v", valMap)
	}
}

func TestLimits_Upsert_RemovesOldKinds(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()

	lim := makeLimit(
		domain.PolicyOrderSizeLimit, domain.ScopeBroker, "", "",
		domain.LimitValue{Kind: domain.KindMaxQuantity, Value: "10"},
		domain.LimitValue{Kind: domain.KindMaxNotional, Value: "500"},
	)
	if err := s.PutLimit(ctx, lim); err != nil {
		t.Fatalf("PutLimit: %v", err)
	}

	// Replace keeping only max_quantity; max_notional should be gone.
	lim2 := makeLimit(
		domain.PolicyOrderSizeLimit, domain.ScopeBroker, "", "",
		domain.LimitValue{Kind: domain.KindMaxQuantity, Value: "20"},
	)
	if err := s.PutLimit(ctx, lim2); err != nil {
		t.Fatalf("PutLimit replace: %v", err)
	}

	got, err := s.ListLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListLimits: %v", err)
	}
	if len(got) != 1 || len(got[0].Values) != 1 {
		t.Fatalf("want 1 barrier with 1 value, got %+v", got)
	}
	if got[0].Values[0].Kind != domain.KindMaxQuantity {
		t.Fatalf("unexpected kind: %v", got[0].Values[0].Kind)
	}
}

func TestLimits_Delete(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()

	lim := makeLimit(
		domain.PolicyRateLimit, domain.ScopeBroker, "", "",
		domain.LimitValue{Kind: domain.KindMaxOrders, Value: "5"},
		domain.LimitValue{Kind: domain.KindWindow, Value: "1s"},
	)
	if err := s.PutLimit(ctx, lim); err != nil {
		t.Fatalf("PutLimit: %v", err)
	}
	if err := s.DeleteLimit(ctx, lim.Target); err != nil {
		t.Fatalf("DeleteLimit: %v", err)
	}
	got, err := s.ListLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListLimits after delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 barriers, got %d", len(got))
	}
}

func TestLimits_Delete_NotFound(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()
	err := s.DeleteLimit(ctx, domain.LimitTarget{
		Tenant: domain.DefaultTenant,
		Policy: domain.PolicyRateLimit,
		Scope:  domain.ScopeBroker,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestLimits_GroupingOrder(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()

	// Insert three barriers in non-alphabetical order.
	barriers := []domain.Limit{
		makeLimit(domain.PolicyPnlBoundsKillSwitch, domain.ScopeAsset, "", "ETH",
			domain.LimitValue{Kind: domain.KindUpperBound, Value: "1000"}),
		makeLimit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
			domain.LimitValue{Kind: domain.KindMaxOrders, Value: "10"},
			domain.LimitValue{Kind: domain.KindWindow, Value: "1s"}),
		makeLimit(domain.PolicyOrderSizeLimit, domain.ScopeAsset, "", "BTC",
			domain.LimitValue{Kind: domain.KindMaxQuantity, Value: "5"}),
	}
	for _, b := range barriers {
		if err := s.PutLimit(ctx, b); err != nil {
			t.Fatalf("PutLimit: %v", err)
		}
	}

	got, err := s.ListLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListLimits: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 barriers, got %d", len(got))
	}
	// Expected order: order_size_limit/asset/BTC,
	// pnl_bounds_kill_switch/asset/ETH, rate_limit/broker
	if got[0].Target.Policy != domain.PolicyOrderSizeLimit {
		t.Errorf("pos 0: want order_size_limit, got %s", got[0].Target.Policy)
	}
	if got[1].Target.Policy != domain.PolicyPnlBoundsKillSwitch {
		t.Errorf("pos 1: want pnl_bounds_kill_switch, got %s", got[1].Target.Policy)
	}
	if got[2].Target.Policy != domain.PolicyRateLimit {
		t.Errorf("pos 2: want rate_limit, got %s", got[2].Target.Policy)
	}
}

func TestLimits_ListByAccount(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()

	broker := makeLimit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
		domain.LimitValue{Kind: domain.KindMaxOrders, Value: "10"},
		domain.LimitValue{Kind: domain.KindWindow, Value: "1s"})
	acc1 := makeLimit(domain.PolicyRateLimit, domain.ScopeAccount, "acc-1", "",
		domain.LimitValue{Kind: domain.KindMaxOrders, Value: "5"},
		domain.LimitValue{Kind: domain.KindWindow, Value: "1s"})
	acc2 := makeLimit(domain.PolicyRateLimit, domain.ScopeAccount, "acc-2", "",
		domain.LimitValue{Kind: domain.KindMaxOrders, Value: "3"},
		domain.LimitValue{Kind: domain.KindWindow, Value: "1s"})

	for _, b := range []domain.Limit{broker, acc1, acc2} {
		if err := s.PutLimit(ctx, b); err != nil {
			t.Fatalf("PutLimit: %v", err)
		}
	}

	got, err := s.ListLimits(ctx, "acc-1")
	if err != nil {
		t.Fatalf("ListLimits acc-1: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 barrier for acc-1, got %d", len(got))
	}
	if got[0].Target.Account != "acc-1" {
		t.Fatalf("unexpected account: %v", got[0].Target.Account)
	}
}

func TestLimits_ListPolicy(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()

	rl := makeLimit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
		domain.LimitValue{Kind: domain.KindMaxOrders, Value: "10"},
		domain.LimitValue{Kind: domain.KindWindow, Value: "1s"})
	osl := makeLimit(domain.PolicyOrderSizeLimit, domain.ScopeAsset, "", "BTC",
		domain.LimitValue{Kind: domain.KindMaxQuantity, Value: "1"})

	for _, b := range []domain.Limit{rl, osl} {
		if err := s.PutLimit(ctx, b); err != nil {
			t.Fatalf("PutLimit: %v", err)
		}
	}

	got, err := s.ListPolicyLimits(ctx, domain.PolicyRateLimit)
	if err != nil {
		t.Fatalf("ListPolicyLimits: %v", err)
	}
	if len(got) != 1 || got[0].Target.Policy != domain.PolicyRateLimit {
		t.Fatalf("unexpected: %+v", got)
	}
}

// --- Audit ---

func TestAudit_AppendAndList(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()

	before := time.Now().Add(-time.Second)
	entry := store.AuditEntry{
		Actor:   "operator",
		Action:  domain.AuditActionSetLimit,
		Tenant:  domain.DefaultTenant,
		Account: "acc-1",
		Detail:  "set limit rate_limit broker max_orders=10 window=1s",
	}
	if err := s.AppendAudit(ctx, entry); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	after := time.Now().Add(time.Second)

	rows, err := s.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.Actor != "operator" {
		t.Errorf("actor: want operator, got %q", r.Actor)
	}
	if r.Action != domain.AuditActionSetLimit {
		t.Errorf("action: want set_limit, got %q", r.Action)
	}
	if r.Account != "acc-1" {
		t.Errorf("account: want acc-1, got %q", r.Account)
	}
	if r.At.Before(before) || r.At.After(after) {
		t.Errorf("at: %v not in expected range", r.At)
	}
	if r.ID <= 0 {
		t.Errorf("id: want positive, got %d", r.ID)
	}
}

func TestAudit_ListNewestFirst(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()

	for i := range 5 {
		entry := store.AuditEntry{
			Actor:  "system",
			Action: domain.AuditActionHydrate,
			Detail: "row " + string(rune('0'+i)),
		}
		if err := s.AppendAudit(ctx, entry); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}

	rows, err := s.ListAudit(ctx, 3)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	// Newest first: id should be descending.
	if rows[0].ID <= rows[1].ID || rows[1].ID <= rows[2].ID {
		t.Errorf("rows not ordered newest-first: ids %d %d %d",
			rows[0].ID, rows[1].ID, rows[2].ID)
	}
}

func TestAudit_NonPositiveN(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	ctx := context.Background()
	_ = s.AppendAudit(ctx, store.AuditEntry{Actor: "x", Action: domain.AuditActionHydrate})
	rows, err := s.ListAudit(ctx, 0)
	if err != nil {
		t.Fatalf("ListAudit(0): %v", err)
	}
	if rows == nil {
		t.Fatal("want non-nil slice for n=0")
	}
	if len(rows) != 0 {
		t.Fatalf("want 0 rows, got %d", len(rows))
	}
}

// --- Close idempotent ---

func TestClose_Idempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "close.db")
	s, err := store.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

