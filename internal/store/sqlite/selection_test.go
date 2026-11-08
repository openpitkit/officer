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
// Please see https://officer.openpit.dev and the OWNERS file for details.

package sqlite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/logtest"
)

func selectionFixture(t *testing.T) (context.Context, fwstore.RealmStore, []domain.Account, []domain.AccountGroup) {
	t.Helper()
	ctx, rs := newRestrictionStore(t)
	var groups []domain.AccountGroup
	for _, code := range []string{"desk-a", "desk-b", "empty"} {
		group, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: code})
		if err != nil {
			t.Fatal(err)
		}
		groups = append(groups, group)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "c"}); err != nil {
		t.Fatal(err)
	}
	var accounts []domain.Account
	for i, code := range []domain.AccountID{"a", "b", "c"} {
		if err := rs.SetAccountGroup(ctx, code, groups[i/2].Code); err != nil {
			t.Fatal(err)
		}
		account, found, err := rs.GetAccount(ctx, code)
		if err != nil || !found {
			t.Fatalf("account %s: found=%t err=%v", code, found, err)
		}
		accounts = append(accounts, account)
	}
	return ctx, rs, accounts, groups
}

// This finite property enumerates every union over three accounts and two
// groups, intersected with every legacy exact-account restriction and page.
func TestSelectionUnionIntersectionBeforePageAndCount(t *testing.T) {
	ctx, rs, accounts, groups := selectionFixture(t)
	for mask := range 32 {
		selection := &domain.AccountSelection{}
		for i, account := range accounts {
			if mask&(1<<i) != 0 {
				selection.Accounts = append(selection.Accounts, account.EngineAccountID)
			}
		}
		for i, group := range groups[:2] {
			if mask&(1<<(i+3)) != 0 {
				selection.AccountGroups = append(selection.AccountGroups, group.EngineGroupID)
			}
		}
		for _, exact := range []domain.AccountID{"", "a", "b", "c"} {
			var want []string
			for i, account := range accounts {
				visible := mask&(1<<i) != 0 || mask&(1<<(i/2+3)) != 0
				if visible && (exact == "" || exact == account.Code) {
					want = append(want, account.Code.String())
				}
			}
			for offset := range 4 {
				page, err := rs.ListAccountRows(ctx, fwstore.AccountListFilter{
					Restriction: domain.AccountRestriction{Account: exact, Selection: selection},
					Page:        fwstore.PageSpec{Limit: 1, Offset: offset},
				})
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				for _, row := range page.Rows {
					got = append(got, row.Account.Code.String())
				}
				var window []string
				if offset < len(want) {
					window = want[offset : offset+1]
				}
				if page.Total != len(want) || !slices.Equal(got, window) {
					t.Fatalf("mask=%d exact=%s offset=%d got=%v total=%d want=%v total=%d", mask, exact, offset, got, page.Total, window, len(want))
				}
			}
		}
	}
}

func TestSelectionGroupsKeepAssignedEmptyAndCountVisibleMembers(t *testing.T) {
	ctx, rs, accounts, groups := selectionFixture(t)
	page, err := rs.ListGroupRows(ctx, fwstore.GroupListFilter{Restriction: domain.AccountRestriction{Selection: &domain.AccountSelection{
		Accounts:      []domain.EngineAccountID{accounts[0].EngineAccountID},
		AccountGroups: []domain.EngineGroupID{groups[2].EngineGroupID},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Rows) != 2 || page.Rows[0].Group.Code != "desk-a" || page.Rows[0].AccountCount != 1 || page.Rows[1].Group.Code != "empty" || page.Rows[1].AccountCount != 0 {
		t.Fatalf("groups=%+v", page)
	}
}

func TestSelectionCannotFollowRecreatedAccountOrGroupCode(t *testing.T) {
	ctx, rs, accounts, groups := selectionFixture(t)
	selection := &domain.AccountSelection{Accounts: []domain.EngineAccountID{accounts[2].EngineAccountID}, AccountGroups: []domain.EngineGroupID{groups[2].EngineGroupID}}
	if err := rs.DeleteAccount(ctx, "c", true); err != nil {
		t.Fatal(err)
	}
	recreated, err := rs.CreateAccount(ctx, domain.Account{Code: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if recreated.EngineAccountID == accounts[2].EngineAccountID {
		t.Fatal("reused account identity")
	}
	if err := rs.DeleteGroup(ctx, "empty", true); err != nil {
		t.Fatal(err)
	}
	group, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	if group.EngineGroupID == groups[2].EngineGroupID {
		t.Fatal("reused group identity")
	}
	if err := rs.SetAccountGroup(ctx, "c", "empty"); err != nil {
		t.Fatal(err)
	}
	page, err := rs.ListAccountRows(ctx, fwstore.AccountListFilter{Restriction: domain.AccountRestriction{Selection: selection}})
	if err != nil || page.Total != 0 || len(page.Rows) != 0 {
		t.Fatalf("recreated targets acquired visibility: %+v err=%v", page, err)
	}
}

func selectionAudit(t *testing.T, ctx context.Context, rs fwstore.RealmStore, entry fwstore.AuditEntry) {
	t.Helper()
	entry.Source = domain.SourceSystem
	entry.Action = domain.AuditActionBlock
	if err := rs.AppendAudit(ctx, entry); err != nil {
		t.Fatal(err)
	}
}

func TestSelectionAuditUsesCurrentMembershipAndActualGroupIdentity(t *testing.T) {
	ctx, rs, accounts, groups := selectionFixture(t)
	selectionAudit(t, ctx, rs, fwstore.AuditEntry{Account: "a", AccountIdentity: accounts[0].EngineAccountID, GroupIdentity: groups[0].EngineGroupID})
	selectionAudit(t, ctx, rs, fwstore.AuditEntry{Group: groups[0].Code, GroupIdentity: groups[0].EngineGroupID})
	selectionAudit(t, ctx, rs, fwstore.AuditEntry{})
	filter := fwstore.AuditListFilter{Restriction: domain.AccountRestriction{Selection: &domain.AccountSelection{AccountGroups: []domain.EngineGroupID{groups[0].EngineGroupID}}}, Page: fwstore.PageSpec{Limit: 1}}
	page, err := rs.ListAuditRows(ctx, filter)
	if err != nil || page.Total != 2 || len(page.Rows) != 1 || page.Rows[0].Group != groups[0].Code {
		t.Fatalf("initial audit=%+v err=%v", page, err)
	}
	if err := rs.SetAccountGroup(ctx, "a", groups[1].Code); err != nil {
		t.Fatal(err)
	}
	page, err = rs.ListAuditRows(ctx, filter)
	if err != nil || page.Total != 1 || len(page.Rows) != 1 || page.Rows[0].Group != groups[0].Code {
		t.Fatalf("current membership audit=%+v err=%v", page, err)
	}
	filter.Restriction.IncludeUnowned = true
	page, err = rs.ListAuditRows(ctx, filter)
	if err != nil || page.Total != 2 {
		t.Fatalf("explicit unowned audit=%+v err=%v", page, err)
	}
	if err := rs.DeleteGroup(ctx, groups[0].Code, true); err != nil {
		t.Fatal(err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: groups[0].Code}); err != nil {
		t.Fatal(err)
	}
	filter.Restriction.IncludeUnowned = false
	page, err = rs.ListAuditRows(ctx, filter)
	if err != nil || page.Total != 0 {
		t.Fatalf("recreated group audit=%+v err=%v", page, err)
	}
}

func TestSelectionAuditUnavailableFailsBeforeCountAndPage(t *testing.T) {
	ctx, rs, accounts, _ := selectionFixture(t)
	selectionAudit(t, ctx, rs, fwstore.AuditEntry{Account: "a"})
	selectionAudit(t, ctx, rs, fwstore.AuditEntry{Account: "b", AccountIdentity: accounts[1].EngineAccountID})
	for _, offset := range []int{0, 1, 100} {
		page, err := rs.ListAuditRows(ctx, fwstore.AuditListFilter{Restriction: domain.AccountRestriction{Selection: &domain.AccountSelection{Accounts: []domain.EngineAccountID{accounts[1].EngineAccountID}}, IncludeUnowned: true}, Page: fwstore.PageSpec{Limit: 1, Offset: offset}})
		if !errors.Is(err, domain.ErrAuditIdentityUnavailable) || page.Total != 0 || len(page.Rows) != 0 {
			t.Fatalf("offset=%d got=%+v err=%v", offset, page, err)
		}
	}
	page, err := rs.ListAuditRows(ctx, fwstore.AuditListFilter{})
	if err != nil || page.Total != 2 {
		t.Fatalf("legacy unrestricted read=%+v err=%v", page, err)
	}
}

func TestSelectionRestoreNeverTransfersSourceNumericIdentity(t *testing.T) {
	ctx, src, accounts, groups := selectionFixture(t)
	selectionAudit(t, ctx, src, fwstore.AuditEntry{Account: "a", AccountIdentity: accounts[0].EngineAccountID})
	selectionAudit(t, ctx, src, fwstore.AuditEntry{Group: groups[0].Code, GroupIdentity: groups[0].EngineGroupID, Detail: domain.NewAuditDetail(domain.AuditDetailCreateGroup, domain.AuditDetailParams{"group": "private audit payload"})})
	archive, err := src.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatal(err)
	}
	_, dst := newRealmStore(t, domain.DefaultRealm)
	// The receiver's first object intentionally has the source target's numeric
	// ID, but a different code. Later allocations also differ from the source.
	collision, err := dst.CreateAccount(ctx, domain.Account{Code: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if collision.EngineAccountID != accounts[0].EngineAccountID {
		t.Fatalf("collision fixture: %d != %d", collision.EngineAccountID, accounts[0].EngineAccountID)
	}
	groupCollision, err := dst.CreateGroup(ctx, domain.AccountGroup{Code: "other-desk"})
	if err != nil {
		t.Fatal(err)
	}
	if groupCollision.EngineGroupID != groups[0].EngineGroupID {
		t.Fatal("group numeric collision fixture failed")
	}
	for i := range 4 {
		if _, err := dst.CreateAccount(ctx, domain.Account{Code: domain.AccountID(fmt.Sprintf("padding-%d", i))}); err != nil {
			t.Fatal(err)
		}
		if _, err := dst.CreateGroup(ctx, domain.AccountGroup{Code: fmt.Sprintf("padding-desk-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	var logs bytes.Buffer
	logtest.SetDefault(t, slog.NewTextHandler(&logs, nil))
	if _, err := dst.RestoreBackup(ctx, archive, backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "restored audit target identities are unavailable") || strings.Contains(logs.String(), "private audit payload") || strings.Contains(logs.String(), "desk-a") {
		t.Fatalf("restore diagnostic missing or contains payload: %s", logs.String())
	}
	restored, found, err := dst.GetAccount(ctx, "a")
	if err != nil || !found || restored.EngineAccountID == accounts[0].EngineAccountID {
		t.Fatalf("receiver allocation=%+v err=%v", restored, err)
	}
	for _, id := range []domain.EngineAccountID{collision.EngineAccountID, restored.EngineAccountID} {
		page, err := dst.ListAuditRows(ctx, fwstore.AuditListFilter{Restriction: domain.AccountRestriction{Selection: &domain.AccountSelection{Accounts: []domain.EngineAccountID{id}}}})
		if !errors.Is(err, domain.ErrAuditIdentityUnavailable) || len(page.Rows) != 0 {
			t.Fatalf("receiver=%d audit=%+v err=%v", id, page, err)
		}
	}
	restoredGroup, found, err := dst.GetGroup(ctx, groups[0].Code)
	if err != nil || !found || restoredGroup.EngineGroupID == groups[0].EngineGroupID {
		t.Fatalf("receiver group allocation=%+v err=%v", restoredGroup, err)
	}
	for _, id := range []domain.EngineGroupID{groupCollision.EngineGroupID, restoredGroup.EngineGroupID} {
		page, err := dst.ListAuditRows(ctx, fwstore.AuditListFilter{Restriction: domain.AccountRestriction{Selection: &domain.AccountSelection{AccountGroups: []domain.EngineGroupID{id}}}})
		if !errors.Is(err, domain.ErrAuditIdentityUnavailable) || len(page.Rows) != 0 {
			t.Fatalf("receiver group=%d audit=%+v err=%v", id, page, err)
		}
	}
	page, err := dst.ListAuditRows(ctx, fwstore.AuditListFilter{})
	if err != nil || page.Total != 2 || !slices.ContainsFunc(page.Rows, func(row domain.AuditRow) bool { return row.Account == "a" }) || !slices.ContainsFunc(page.Rows, func(row domain.AuditRow) bool {
		return row.Group == groups[0].Code && row.Detail.Params["group"] == "private audit payload"
	}) {
		t.Fatalf("restored history=%+v err=%v", page, err)
	}
}
