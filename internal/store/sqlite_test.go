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

// Skeleton tests for the rebuilt SQLite store: schema migration and foreign-key
// enforcement, single-realm ForRealm enforcement, the shared external-id and
// engine-id helpers, and group-1 (assets, principals, account groups, accounts)
// round-trips by code. The table groups still stubbed are not covered here.

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"go.openpit.dev/officer/internal/domain"
)

// newTestStore opens a fresh migrated SQLite store in a temp file and returns it
// with its default realm handle.
func newTestStore(t *testing.T) (Store, RealmStore) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rs, err := s.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm(default): %v", err)
	}
	return s, rs
}

func TestMigrateAppliesSchemaAndForeignKeys(t *testing.T) {
	ctx := context.Background()
	s, rs := newTestStore(t)

	version, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema version = %d, want 1", version)
	}

	// Foreign-key enforcement must be ON: inserting an account whose group link
	// points at a non-existent group surrogate id must be rejected. We reach this
	// through the public path by referencing an unknown group code, which the
	// connector rejects as ErrInvalid before any insert; to prove the PRAGMA
	// itself, run a raw insert with an impossible group_id and expect a failure.
	sq, ok := s.(*sqliteStore)
	if !ok {
		t.Fatalf("store is not *sqliteStore")
	}
	_, err = sq.db.ExecContext(
		ctx,
		`INSERT INTO accounts (engine_account_id, code, group_id) VALUES (1, 'x', 999999)`,
	)
	if err == nil {
		t.Fatal("expected foreign-key violation inserting dangling group_id, got nil")
	}

	// The store handle is usable: an empty dictionary lists as a non-nil empty
	// slice.
	assets, err := rs.ListAssets(ctx)
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	if assets == nil {
		t.Fatal("ListAssets returned nil, want non-nil empty slice")
	}
	if len(assets) != 0 {
		t.Fatalf("ListAssets len = %d, want 0", len(assets))
	}
}

func TestForRealmSingleRealmEnforcement(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)

	// The empty realm id defaults to the served realm.
	if _, err := s.ForRealm(ctx, ""); err != nil {
		t.Fatalf("ForRealm(empty) = %v, want nil", err)
	}

	// A foreign realm id is rejected with ErrInvalid.
	_, err := s.ForRealm(ctx, domain.RealmID("other"))
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ForRealm(other) error = %v, want ErrInvalid", err)
	}
}

func TestForRealmNonDefaultBoundRealm(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	s, err := NewSQLiteStore(path, WithRealm(domain.RealmID("desk-a")))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := s.ForRealm(ctx, domain.RealmID("desk-a")); err != nil {
		t.Fatalf("ForRealm(desk-a) = %v, want nil", err)
	}
	// The default realm is now foreign to a connector bound to desk-a.
	if _, err := s.ForRealm(ctx, domain.DefaultRealm); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ForRealm(default) error = %v, want ErrInvalid", err)
	}
}

func TestExternalIDHelperRoundTrip(t *testing.T) {
	id, err := newExternalID()
	if err != nil {
		t.Fatalf("newExternalID: %v", err)
	}
	if id.IsZero() {
		t.Fatal("newExternalID returned the zero value")
	}
	s := id.String()
	if len(s) != domain.ExternalIDStringLen {
		t.Fatalf("external id string len = %d, want %d", len(s), domain.ExternalIDStringLen)
	}
	parsed, err := domain.ParseExternalID(s)
	if err != nil {
		t.Fatalf("ParseExternalID: %v", err)
	}
	if parsed != id {
		t.Fatalf("round-trip mismatch: %v != %v", parsed, id)
	}

	// Two draws differ with overwhelming probability.
	other, err := newExternalID()
	if err != nil {
		t.Fatalf("newExternalID (second): %v", err)
	}
	if other == id {
		t.Fatal("two external ids collided")
	}
}

func TestAssetRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	asset := domain.Asset{Code: "AAPL", Title: "Apple", AssetClass: "equity"}
	if err := rs.CreateAsset(ctx, asset); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	got, ok, err := rs.GetAsset(ctx, "AAPL")
	if err != nil || !ok {
		t.Fatalf("GetAsset: ok=%v err=%v", ok, err)
	}
	if got != asset {
		t.Fatalf("GetAsset = %+v, want %+v", got, asset)
	}

	// Duplicate code is ErrAlreadyExists.
	if err := rs.CreateAsset(ctx, asset); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateAsset(dup) error = %v, want ErrAlreadyExists", err)
	}

	// Update mutates title and class.
	asset.Title = "Apple Inc."
	asset.AssetClass = ""
	if err := rs.UpdateAsset(ctx, asset); err != nil {
		t.Fatalf("UpdateAsset: %v", err)
	}
	got, _, err = rs.GetAsset(ctx, "AAPL")
	if err != nil {
		t.Fatalf("GetAsset after update: %v", err)
	}
	if got.Title != "Apple Inc." || got.AssetClass != "" {
		t.Fatalf("UpdateAsset result = %+v", got)
	}

	// List returns the row.
	assets, err := rs.ListAssets(ctx)
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("ListAssets len = %d, want 1", len(assets))
	}

	// Delete removes it.
	if err := rs.DeleteAsset(ctx, "AAPL", true); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	if _, ok, _ := rs.GetAsset(ctx, "AAPL"); ok {
		t.Fatal("GetAsset after delete returned ok=true")
	}
	// Delete of a missing asset is ErrNotFound.
	if err := rs.DeleteAsset(ctx, "AAPL", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeleteAsset(missing) error = %v, want ErrNotFound", err)
	}
}

func TestPrincipalRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	p := domain.Principal{Code: "operator", Title: "Desk Operator"}
	if err := rs.CreatePrincipal(ctx, p); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	got, ok, err := rs.GetPrincipal(ctx, "operator")
	if err != nil || !ok {
		t.Fatalf("GetPrincipal: ok=%v err=%v", ok, err)
	}
	if got != p {
		t.Fatalf("GetPrincipal = %+v, want %+v", got, p)
	}
	if err := rs.CreatePrincipal(ctx, p); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreatePrincipal(dup) error = %v, want ErrAlreadyExists", err)
	}
	p.Title = "Senior Operator"
	if err := rs.UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}
	list, err := rs.ListPrincipals(ctx)
	if err != nil {
		t.Fatalf("ListPrincipals: %v", err)
	}
	if len(list) != 1 || list[0].Title != "Senior Operator" {
		t.Fatalf("ListPrincipals = %+v", list)
	}
	if err := rs.DeletePrincipal(ctx, "operator"); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	if err := rs.DeletePrincipal(ctx, "operator"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeletePrincipal(missing) error = %v, want ErrNotFound", err)
	}
}

func TestGroupRoundTripAndEngineID(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	g1, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "alpha", Title: "Alpha"})
	if err != nil {
		t.Fatalf("CreateGroup(alpha): %v", err)
	}
	if err := domain.ValidateEngineGroupID(g1.EngineGroupID); err != nil {
		t.Fatalf("assigned engine group id out of range: %v", err)
	}

	g2, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "beta"})
	if err != nil {
		t.Fatalf("CreateGroup(beta): %v", err)
	}
	if g1.EngineGroupID == g2.EngineGroupID {
		t.Fatalf("engine group ids collided: %d", g1.EngineGroupID)
	}

	// Duplicate code rejected.
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "alpha"}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateGroup(dup) error = %v, want ErrAlreadyExists", err)
	}

	got, ok, err := rs.GetGroup(ctx, "alpha")
	if err != nil || !ok {
		t.Fatalf("GetGroup: ok=%v err=%v", ok, err)
	}
	if got.EngineGroupID != g1.EngineGroupID {
		t.Fatalf("GetGroup engine id = %d, want %d", got.EngineGroupID, g1.EngineGroupID)
	}

	if err := rs.SetGroupNotes(ctx, "alpha", "vip desk"); err != nil {
		t.Fatalf("SetGroupNotes: %v", err)
	}
	if err := rs.SetGroupBlocked(ctx, "alpha", true, "risk review"); err != nil {
		t.Fatalf("SetGroupBlocked: %v", err)
	}
	got, _, _ = rs.GetGroup(ctx, "alpha")
	if got.Notes != "vip desk" || !got.Blocked || got.BlockReason != "risk review" {
		t.Fatalf("group after updates = %+v", got)
	}

	groups, err := rs.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("ListGroups len = %d, want 2", len(groups))
	}

	if err := rs.SetGroupNotes(ctx, "ghost", "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetGroupNotes(missing) error = %v, want ErrNotFound", err)
	}
}

func TestAccountRoundTripEngineIDAndGroupLink(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "alpha"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	a1, err := rs.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Title: "Account 1", GroupCode: "alpha", Notes: "n",
	})
	if err != nil {
		t.Fatalf("CreateAccount(acc-1): %v", err)
	}
	if err := domain.ValidateEngineAccountID(a1.EngineAccountID); err != nil {
		t.Fatalf("assigned engine account id out of range: %v", err)
	}

	a2, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"})
	if err != nil {
		t.Fatalf("CreateAccount(acc-2): %v", err)
	}
	if a1.EngineAccountID == a2.EngineAccountID {
		t.Fatalf("engine account ids collided: %d", a1.EngineAccountID)
	}

	// Read back surfaces the group code, not a surrogate id.
	got, ok, err := rs.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if got.GroupCode != "alpha" {
		t.Fatalf("GetAccount group code = %q, want alpha", got.GroupCode)
	}
	if got.EngineAccountID != a1.EngineAccountID {
		t.Fatalf("GetAccount engine id = %d, want %d", got.EngineAccountID, a1.EngineAccountID)
	}

	// acc-2 has no group.
	got2, _, _ := rs.GetAccount(ctx, "acc-2")
	if got2.GroupCode != "" {
		t.Fatalf("acc-2 group code = %q, want empty", got2.GroupCode)
	}

	// Duplicate account code rejected.
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateAccount(dup) error = %v, want ErrAlreadyExists", err)
	}

	// Unknown group code on create is ErrInvalid.
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-3", GroupCode: "ghost"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateAccount(unknown group) error = %v, want ErrInvalid", err)
	}

	// ListGroupAccounts returns only acc-1.
	members, err := rs.ListGroupAccounts(ctx, "alpha")
	if err != nil {
		t.Fatalf("ListGroupAccounts: %v", err)
	}
	if len(members) != 1 || members[0].Code != "acc-1" {
		t.Fatalf("ListGroupAccounts = %+v", members)
	}

	// SetAccountGroup to an unknown group is ErrInvalid; valid clear works.
	if err := rs.SetAccountGroup(ctx, "acc-2", "ghost"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SetAccountGroup(unknown) error = %v, want ErrInvalid", err)
	}
	if err := rs.SetAccountGroup(ctx, "acc-1", ""); err != nil {
		t.Fatalf("SetAccountGroup(clear): %v", err)
	}
	got, _, _ = rs.GetAccount(ctx, "acc-1")
	if got.GroupCode != "" {
		t.Fatalf("after clear, group code = %q, want empty", got.GroupCode)
	}

	// Block and notes round-trip.
	if err := rs.SetAccountBlocked(ctx, "acc-2", true, "kill"); err != nil {
		t.Fatalf("SetAccountBlocked: %v", err)
	}
	if err := rs.SetAccountNotes(ctx, "acc-2", "watch"); err != nil {
		t.Fatalf("SetAccountNotes: %v", err)
	}
	got2, _, _ = rs.GetAccount(ctx, "acc-2")
	if !got2.Blocked || got2.BlockReason != "kill" || got2.Notes != "watch" {
		t.Fatalf("acc-2 after updates = %+v", got2)
	}

	all, err := rs.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAccounts len = %d, want 2", len(all))
	}

	// Missing account on a setter is ErrNotFound.
	if err := rs.SetAccountNotes(ctx, "ghost", "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetAccountNotes(missing) error = %v, want ErrNotFound", err)
	}
}

func TestDeleteGroupClearsAccountLink(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "alpha"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1", GroupCode: "alpha"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Deleting the group must clear the member's link (ON DELETE SET NULL), not
	// cascade-delete the account.
	if err := rs.DeleteGroup(ctx, "alpha"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	got, ok, err := rs.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("account should survive group delete: ok=%v err=%v", ok, err)
	}
	if got.GroupCode != "" {
		t.Fatalf("account group code after group delete = %q, want empty", got.GroupCode)
	}

	if err := rs.DeleteGroup(ctx, "alpha"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeleteGroup(missing) error = %v, want ErrNotFound", err)
	}
}

func TestApplyBusinessCSVImport_EmptyBatchIsNoOp(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	// An empty import batch commits cleanly and produces no rows.
	if err := rs.ApplyBusinessCSVImport(ctx, BusinessCSVImport{}); err != nil {
		t.Fatalf("ApplyBusinessCSVImport(empty): %v", err)
	}
}
