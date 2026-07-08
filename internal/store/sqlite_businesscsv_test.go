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

// Store-level tests for the transactional business CSV import. Covers happy
// paths (groups, account, balance), code-resolution errors (unknown group
// code, unknown asset), conflict semantics (Exists flag drives create-vs-update
// so engine ids are preserved on update), and the atomicity guarantee (a
// bad-asset balance row rolls back the entire transaction).

package store

import (
	"context"
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

// --- Happy path: groups ------------------------------------------------------

func TestApplyBusinessCSVImport_CreatesGroup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, rs := newTestStore(t)
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset USD: %v", err)
	}

	in := BusinessCSVImport{
		Groups: []BusinessCSVImportGroup{
			{
				Group: domain.AccountGroup{
					Code: "desk-a", Title: "Desk A",
					Currency: "USD", Notes: "import note", Blocked: false,
				},
				Exists: false,
			},
		},
	}
	if err := rs.ApplyBusinessCSVImport(ctx, in); err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}

	g, ok, err := rs.GetGroup(ctx, "desk-a")
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if !ok {
		t.Fatal("group not found after import")
	}
	if g.Code != "desk-a" || g.Title != "Desk A" || g.Currency != "USD" ||
		g.Notes != "import note" {
		t.Errorf("group = %+v", g)
	}
	if g.EngineGroupID == 0 {
		t.Error("engine group id not assigned")
	}
}

func TestApplyBusinessCSVImport_UpdatesExistingGroup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, rs := newTestStore(t)
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "EUR"}); err != nil {
		t.Fatalf("CreateAsset EUR: %v", err)
	}

	// Create a group first via the normal path so it has an engine id.
	created, err := rs.CreateGroup(ctx, domain.AccountGroup{
		Code: "desk-b", Title: "Old Title", Notes: "old",
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	origEngineID := created.EngineGroupID

	in := BusinessCSVImport{
		Groups: []BusinessCSVImportGroup{
			{
				Group: domain.AccountGroup{
					Code: "desk-b", Title: "New Title",
					Currency: "EUR", Notes: "updated", Blocked: true,
					BlockReason: "hold",
				},
				Exists: true,
			},
		},
	}
	if err := rs.ApplyBusinessCSVImport(ctx, in); err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}

	g, ok, err := rs.GetGroup(ctx, "desk-b")
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if !ok {
		t.Fatal("group not found")
	}
	if g.Title != "New Title" || g.Currency != "EUR" ||
		g.Notes != "updated" || !g.Blocked || g.BlockReason != "hold" {
		t.Errorf("group = %+v", g)
	}
	// Engine id must be preserved on update.
	if g.EngineGroupID != origEngineID {
		t.Errorf("engine id changed: want %d, got %d", origEngineID, g.EngineGroupID)
	}
}

// --- Happy path: account ----------------------------------------------------

func TestApplyBusinessCSVImport_CreatesAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, rs := newTestStore(t)
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "JPY"}); err != nil {
		t.Fatalf("CreateAsset JPY: %v", err)
	}

	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "grp"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	in := BusinessCSVImport{
		Accounts: []BusinessCSVImportAccount{
			{
				Account: domain.Account{
					Code: "acc-1", GroupCode: "grp", Currency: "JPY",
					Notes: "note",
				},
				Exists: false,
			},
		},
	}
	if err := rs.ApplyBusinessCSVImport(ctx, in); err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}

	a, ok, err := rs.GetAccount(ctx, "acc-1")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if !ok {
		t.Fatal("account not found")
	}
	if a.Code != "acc-1" || a.GroupCode != "grp" || a.Currency != "JPY" ||
		a.Notes != "note" {
		t.Errorf("account = %+v", a)
	}
	if a.EngineAccountID == 0 {
		t.Error("engine account id not assigned")
	}
}

func TestApplyBusinessCSVImport_UpdatesExistingAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, rs := newTestStore(t)
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "CHF"}); err != nil {
		t.Fatalf("CreateAsset CHF: %v", err)
	}

	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "grp"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	created, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2", GroupCode: "grp"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	origEngineID := created.EngineAccountID

	in := BusinessCSVImport{
		Accounts: []BusinessCSVImportAccount{
			{
				Account: domain.Account{
					Code:        "acc-2",
					GroupCode:   "grp",
					Currency:    "CHF",
					Notes:       "updated",
					Blocked:     true,
					BlockReason: "kyc",
				},
				Exists: true,
			},
		},
	}
	if err := rs.ApplyBusinessCSVImport(ctx, in); err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}

	a, ok, err := rs.GetAccount(ctx, "acc-2")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if !ok {
		t.Fatal("account not found")
	}
	if a.Currency != "CHF" || a.Notes != "updated" || !a.Blocked ||
		a.BlockReason != "kyc" {
		t.Errorf("account = %+v", a)
	}
	// Engine id must be preserved on update.
	if a.EngineAccountID != origEngineID {
		t.Errorf("engine id changed: want %d, got %d", origEngineID, a.EngineAccountID)
	}
}

// --- Happy path: balance ----------------------------------------------------

func TestApplyBusinessCSVImport_UpsertsBalance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, rs := newTestStore(t)

	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-3"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL"}); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	in := BusinessCSVImport{
		Balances: []domain.Balance{
			{
				Account:     "acc-3",
				Asset:       "AAPL",
				Available:   "100",
				Held:        "10",
				Incoming:    "0",
				RealizedPnl: "5",
			},
		},
	}
	if err := rs.ApplyBusinessCSVImport(ctx, in); err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}

	b, ok, err := rs.GetBalance(ctx, "acc-3", "AAPL")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if !ok {
		t.Fatal("balance not found")
	}
	if b.Available != "100" || b.Held != "10" || b.RealizedPnl != "5" {
		t.Errorf("balance = %+v", b)
	}
}

// --- Happy path: audit -------------------------------------------------------

func TestApplyBusinessCSVImport_AppendsAudit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, rs := newTestStore(t)

	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-4"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	in := BusinessCSVImport{
		Groups: []BusinessCSVImportGroup{
			{Group: domain.AccountGroup{Code: "grp-audit"}, Exists: false},
		},
		Audits: []AuditEntry{
			{
				Action:  domain.AuditActionImportBusinessCSV,
				Account: "acc-4",
				Detail:  "test import",
				Source:  domain.SourcePanel,
			},
		},
	}
	if err := rs.ApplyBusinessCSVImport(ctx, in); err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}

	rows, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.Action == domain.AuditActionImportBusinessCSV && row.Detail == "test import" {
			found = true
			break
		}
	}
	if !found {
		t.Error("audit row not found")
	}
}

// --- Happy path: adjustment -------------------------------------------------

// TestApplyBusinessCSVImport_PersistsAdjustments asserts the adjustment records
// the node produced from the imported position snapshots are persisted in the
// same transaction, each resolving its account/asset/principal by code and
// receiving a generated external id, with its accepted outcome round-tripped.
func TestApplyBusinessCSVImport_PersistsAdjustments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, rs := newTestStore(t)

	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-adj"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	in := BusinessCSVImport{
		Adjustments: []domain.AccountAdjustmentRecord{
			{
				Account: "acc-adj", Asset: "USD", Source: domain.SourcePanel,
				Request:  domain.AdjustmentRequest{Asset: "USD"},
				Accepted: &domain.AdjustmentOutcomeAccepted{BalanceDelta: "100", BalanceResult: "100"},
			},
		},
	}
	if err := rs.ApplyBusinessCSVImport(ctx, in); err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}

	adj, err := rs.ListAdjustments(ctx, "acc-adj", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(adj) != 1 {
		t.Fatalf("persisted adjustments = %d, want 1", len(adj))
	}
	rec := adj[0]
	if rec.Account != "acc-adj" || rec.Asset != "USD" || rec.Source != domain.SourcePanel {
		t.Fatalf("adjustment identity = %+v", rec)
	}
	if rec.ExternalID.IsZero() {
		t.Fatal("adjustment external id not generated")
	}
	if rec.Accepted == nil || rec.Rejected != nil {
		t.Fatalf("adjustment outcome = %+v, want accepted", rec)
	}
	if rec.Accepted.BalanceResult != "100" {
		t.Fatalf("adjustment outcome round-trip = %+v, want balance result 100", rec.Accepted)
	}
}

// TestApplyBusinessCSVImport_UnknownAdjustmentAccount asserts an adjustment for
// an unknown account fails the whole import with ErrInvalid.
func TestApplyBusinessCSVImport_UnknownAdjustmentAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, rs := newTestStore(t)

	if err := rs.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	in := BusinessCSVImport{
		Adjustments: []domain.AccountAdjustmentRecord{
			{Account: "ghost", Asset: "USD", Source: domain.SourcePanel},
		},
	}
	err := rs.ApplyBusinessCSVImport(ctx, in)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyBusinessCSVImport error = %v, want ErrInvalid", err)
	}
}

// --- Unknown code errors -----------------------------------------------------

func TestApplyBusinessCSVImport_UnknownGroupCode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, rs := newTestStore(t)

	in := BusinessCSVImport{
		Accounts: []BusinessCSVImportAccount{
			{
				Account: domain.Account{Code: "acc-5", GroupCode: "nonexistent"},
				Exists:  false,
			},
		},
	}
	err := rs.ApplyBusinessCSVImport(ctx, in)
	if err == nil {
		t.Fatal("want error for unknown group code")
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("want ErrInvalid, got %v", err)
	}
}

func TestApplyBusinessCSVImport_UnknownAssetCode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, rs := newTestStore(t)

	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-6"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	in := BusinessCSVImport{
		Balances: []domain.Balance{
			{Account: "acc-6", Asset: "GHOST", Available: "1"},
		},
	}
	err := rs.ApplyBusinessCSVImport(ctx, in)
	if err == nil {
		t.Fatal("want error for unknown asset code")
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("want ErrInvalid, got %v", err)
	}
}

func TestApplyBusinessCSVImport_UnknownAccountCodeInBalance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, rs := newTestStore(t)

	if err := rs.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	in := BusinessCSVImport{
		Balances: []domain.Balance{
			{Account: "ghost-account", Asset: "USD", Available: "1"},
		},
	}
	err := rs.ApplyBusinessCSVImport(ctx, in)
	if err == nil {
		t.Fatal("want error for unknown account code")
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("want ErrInvalid, got %v", err)
	}
}

// --- Atomicity ---------------------------------------------------------------

func TestApplyBusinessCSVImport_Atomicity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, rs := newTestStore(t)

	// A batch with one valid group and one balance row with an unknown asset.
	// The group must NOT be persisted because the balance row fails the tx.
	in := BusinessCSVImport{
		Groups: []BusinessCSVImportGroup{
			{Group: domain.AccountGroup{Code: "atomic-grp"}, Exists: false},
		},
		Balances: []domain.Balance{
			{Account: "nobody", Asset: "NOASSET", Available: "1"},
		},
	}
	err := rs.ApplyBusinessCSVImport(ctx, in)
	if err == nil {
		t.Fatal("want error to trigger rollback")
	}

	_, ok, err := rs.GetGroup(ctx, "atomic-grp")
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if ok {
		t.Error("group must not be visible after rollback")
	}
}

// --- Engine id assignment across multiple new groups/account ----------------

func TestApplyBusinessCSVImport_MultipleNewGroupsGetDistinctEngineIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, rs := newTestStore(t)

	in := BusinessCSVImport{
		Groups: []BusinessCSVImportGroup{
			{Group: domain.AccountGroup{Code: "g1"}, Exists: false},
			{Group: domain.AccountGroup{Code: "g2"}, Exists: false},
			{Group: domain.AccountGroup{Code: "g3"}, Exists: false},
		},
	}
	if err := rs.ApplyBusinessCSVImport(ctx, in); err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}

	ids := make(map[domain.EngineGroupID]bool)
	for _, code := range []string{"g1", "g2", "g3"} {
		g, ok, err := rs.GetGroup(ctx, code)
		if err != nil || !ok {
			t.Fatalf("GetGroup %q: ok=%v err=%v", code, ok, err)
		}
		if g.EngineGroupID == 0 {
			t.Errorf("group %q has zero engine id", code)
		}
		if ids[g.EngineGroupID] {
			t.Errorf("duplicate engine group id %d for %q", g.EngineGroupID, code)
		}
		ids[g.EngineGroupID] = true
	}
}
