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

// Audit-group tests: append/list with the external id surfaced (22-char handle,
// surrogate id never exposed), newest-first ordering, the account/source/action
// filters applied in the query, and that deleting an account or principal leaves
// immutable audit snapshots intact, so the compliance trail survives deletes.

package store

import (
	"context"
	"testing"

	"go.openpit.dev/officer/internal/domain"
)

// seedAuditFixtures creates an account and an actor principal for the audit tests.
func seedAuditFixtures(t *testing.T) (context.Context, RealmStore) {
	t.Helper()
	ctx := context.Background()
	_, rs := newTestStore(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	return ctx, rs
}

func TestAuditAppendListAndExternalID(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)

	if err := rs.AppendAudit(ctx, AuditEntry{
		Actor:   "operator",
		Action:  domain.AuditActionCreateAccount,
		Account: "acc-1",
		Detail:  "created acc-1",
		Source:  domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	// A system-initiated row with no account/actor.
	if err := rs.AppendAudit(ctx, AuditEntry{
		Action: domain.AuditActionHydrate,
		Detail: "boot",
	}); err != nil {
		t.Fatalf("AppendAudit(system): %v", err)
	}

	rows, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ListAudit len = %d, want 2", len(rows))
	}

	// Newest first: the system hydrate row was appended last.
	if rows[0].Action != domain.AuditActionHydrate {
		t.Fatalf("ListAudit[0].Action = %q, want hydrate (newest first)", rows[0].Action)
	}
	// External id is the 22-char public handle; the surrogate id is never surfaced
	// (AuditRow has no such field).
	for _, row := range rows {
		if row.ExternalID.IsZero() {
			t.Fatal("audit row external id is zero")
		}
		if len(row.ExternalID.String()) != domain.ExternalIDStringLen {
			t.Fatalf("external id string len = %d, want %d",
				len(row.ExternalID.String()), domain.ExternalIDStringLen)
		}
	}

	// The control-action row surfaces the account and actor codes; the system row
	// surfaces neither and defaults its source to system.
	control := rows[1]
	if control.Account != "acc-1" || control.Actor != "operator" {
		t.Fatalf("control row refs = (%q, %q), want (acc-1, operator)", control.Account, control.Actor)
	}
	if control.Source != domain.SourcePanel {
		t.Fatalf("control row source = %q, want panel", control.Source)
	}
	system := rows[0]
	if system.Account != "" || system.Actor != "" {
		t.Fatalf("system row refs = (%q, %q), want empty", system.Account, system.Actor)
	}
	if system.Source != domain.SourceSystem {
		t.Fatalf("system row source = %q, want system (default)", system.Source)
	}

	// A non-positive n returns an empty slice.
	empty, err := rs.ListAudit(ctx, 0)
	if err != nil {
		t.Fatalf("ListAudit(0): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("ListAudit(0) len = %d, want 0", len(empty))
	}
}

func TestAuditAppendUnknownRefIsSnapshot(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)

	if err := rs.AppendAudit(ctx, AuditEntry{
		Action:  domain.AuditActionBlock,
		Account: "ghost",
		Actor:   "ghost",
	}); err != nil {
		t.Fatalf("AppendAudit(unknown refs): %v", err)
	}
	rows, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 || rows[0].Account != "ghost" || rows[0].Actor != "ghost" {
		t.Fatalf("unknown refs snapshot = %+v", rows)
	}
}

// TestAuditTitleSnapshotPersistedVerbatim covers the title snapshot path: an
// entry that carries non-empty account/actor titles persists them as given, and
// ListAudit reads them back unchanged - no lookup is consulted.
func TestAuditTitleSnapshotPersistedVerbatim(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)

	if err := rs.AppendAudit(ctx, AuditEntry{
		Actor:        "operator",
		ActorTitle:   "Snapshot Operator",
		Action:       domain.AuditActionBlock,
		Account:      "acc-1",
		AccountTitle: "Snapshot Account",
		Source:       domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}

	rows, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListAudit len = %d, want 1", len(rows))
	}
	if rows[0].AccountTitle != "Snapshot Account" {
		t.Fatalf("account title = %q, want Snapshot Account", rows[0].AccountTitle)
	}
	if rows[0].ActorTitle != "Snapshot Operator" {
		t.Fatalf("actor title = %q, want Snapshot Operator", rows[0].ActorTitle)
	}
}

// TestAuditTitleBackfilledFromLiveRows covers the lookupTitle safety-net: an
// entry with empty titles but codes matching live accounts/principals rows
// backfills both titles from those rows at write time.
func TestAuditTitleBackfilledFromLiveRows(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Title: "Live Account",
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := rs.CreatePrincipal(ctx, domain.Principal{
		Code: "operator", Title: "Live Operator",
	}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	if err := rs.AppendAudit(ctx, AuditEntry{
		Actor:   "operator",
		Action:  domain.AuditActionBlock,
		Account: "acc-1",
		Source:  domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}

	rows, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListAudit len = %d, want 1", len(rows))
	}
	if rows[0].AccountTitle != "Live Account" {
		t.Fatalf("account title = %q, want Live Account (backfilled)", rows[0].AccountTitle)
	}
	if rows[0].ActorTitle != "Live Operator" {
		t.Fatalf("actor title = %q, want Live Operator (backfilled)", rows[0].ActorTitle)
	}
}

func TestAuditFiltered(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
		t.Fatalf("CreateAccount(acc-2): %v", err)
	}

	mustAppend := func(e AuditEntry) {
		t.Helper()
		if err := rs.AppendAudit(ctx, e); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}
	mustAppend(AuditEntry{Action: domain.AuditActionBlock, Account: "acc-1", Source: domain.SourcePanel})
	mustAppend(AuditEntry{Action: domain.AuditActionUnblock, Account: "acc-1", Source: domain.SourceAPI})
	mustAppend(AuditEntry{Action: domain.AuditActionBlock, Account: "acc-2", Source: domain.SourcePanel})
	mustAppend(AuditEntry{Action: domain.AuditActionSubmitOrder, Account: "acc-2", Source: domain.SourceMCP})

	// Account filter narrows to acc-1's two rows.
	rows, err := rs.ListAuditFiltered(ctx, domain.AuditFilter{Account: "acc-1"}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(account): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("account filter len = %d, want 2", len(rows))
	}

	// Source filter narrows to the two panel rows.
	rows, err = rs.ListAuditFiltered(ctx, domain.AuditFilter{Source: domain.SourcePanel}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(source): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("source filter len = %d, want 2", len(rows))
	}

	// Action filter narrows to the two block rows.
	rows, err = rs.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(actions): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("action filter len = %d, want 2", len(rows))
	}

	// Combined account+action filter narrows to acc-1's single block row.
	rows, err = rs.ListAuditFiltered(ctx, domain.AuditFilter{
		Account: "acc-1",
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(combined): %v", err)
	}
	if len(rows) != 1 || rows[0].Action != domain.AuditActionBlock || rows[0].Account != "acc-1" {
		t.Fatalf("combined filter = %+v", rows)
	}

	// The limit bounds the filtered set, not a pre-filter window: 1 of the 2 block
	// rows, newest first (acc-2's block was appended after acc-1's).
	rows, err = rs.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 1)
	if err != nil {
		t.Fatalf("ListAuditFiltered(limit): %v", err)
	}
	if len(rows) != 1 || rows[0].Account != "acc-2" {
		t.Fatalf("limit-bounded filter = %+v, want one acc-2 block row", rows)
	}
}

// TestAuditTrailPreservedOnAccountDelete is the central §6 invariant: deleting an
// account must NOT cascade-delete its audit rows; the rows survive with the
// account reference cleared (ON DELETE SET NULL), and ListAudit still returns
// them. The same holds for a deleted actor principal.
func TestAuditTrailPreservedOnAccountDelete(t *testing.T) {
	ctx, rs := seedAuditFixtures(t)

	if err := rs.AppendAudit(ctx, AuditEntry{
		Actor:   "operator",
		Action:  domain.AuditActionBlock,
		Account: "acc-1",
		Detail:  "blocked then deleted",
		Source:  domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}

	// Sanity: the row carries both refs before any delete.
	before, _ := rs.ListAudit(ctx, 10)
	if len(before) != 1 || before[0].Account != "acc-1" || before[0].Actor != "operator" {
		t.Fatalf("pre-delete row = %+v", before)
	}
	originalXID := before[0].ExternalID

	// Delete the account: its audit row must survive with the account snapshot.
	deleteAccountRaw(t, ctx, rs, "acc-1")
	after, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit after account delete: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("audit row count after account delete = %d, want 1 (trail preserved)", len(after))
	}
	if after[0].ExternalID != originalXID {
		t.Fatalf("audit row external id changed after delete: %v != %v", after[0].ExternalID, originalXID)
	}
	if after[0].Account != "acc-1" {
		t.Fatalf("account after delete = %q, want snapshot acc-1", after[0].Account)
	}
	// The actor is still present (only the account was deleted).
	if after[0].Actor != "operator" {
		t.Fatalf("actor after account delete = %q, want operator", after[0].Actor)
	}
	// The action and detail are untouched — the compliance content survives.
	if after[0].Action != domain.AuditActionBlock || after[0].Detail != "blocked then deleted" {
		t.Fatalf("audit content after delete = %+v", after[0])
	}

	// Now delete the actor principal: the row must still survive with the actor
	// snapshot intact.
	if err := rs.DeletePrincipal(ctx, "operator"); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	final, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit after principal delete: %v", err)
	}
	if len(final) != 1 {
		t.Fatalf("audit row count after principal delete = %d, want 1 (trail preserved)", len(final))
	}
	if final[0].Actor != "operator" {
		t.Fatalf("actor after principal delete = %q, want snapshot operator", final[0].Actor)
	}
	if final[0].Action != domain.AuditActionBlock {
		t.Fatalf("audit action after principal delete = %q, want block", final[0].Action)
	}
}

// deleteAccountRaw deletes an account by code with direct SQL through the shared
// connection so the test isolates audit snapshot behavior from store delete
// policy.
func deleteAccountRaw(
	t *testing.T, ctx context.Context, rs RealmStore, code domain.AccountID,
) {
	t.Helper()
	r := rs.(*realmStore)
	res, err := r.db().ExecContext(
		ctx, `DELETE FROM accounts WHERE code = ?`, code.String(),
	)
	if err != nil {
		t.Fatalf("delete account %q: %v", code, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("delete account %q rows: %v", code, err)
	}
	if n == 0 {
		t.Fatalf("delete account %q affected no rows", code)
	}
}
