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
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func TestLocalNode_UpdateAccountAuditFilesRenameUnderBothCodes(t *testing.T) {
	t.Parallel()
	engine := newFakeEngine()
	engine.enforceResolver = true
	n, realm := newTestNode(t, engine)
	ctx := context.Background()

	created, err := n.CreateAccount(ctx, domain.Account{
		Code: "account-old", Title: "Before",
	}, testCaller)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	updated, err := n.UpdateAccount(
		ctx,
		testKey(created.Code),
		domain.Account{Code: "account-new", Title: "After"},
		testCaller,
	)
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}

	wants := []struct {
		code   domain.AccountID
		title  string
		detail string
	}{
		{
			code: "account-old", title: "Before",
			detail: "update account account-old -> account-new (record under old code)",
		},
		{
			code: "account-new", title: "After",
			detail: "update account account-old -> account-new (record under new code)",
		},
	}
	for _, want := range wants {
		rows := accountUpdateAuditRows(t, ctx, realm, want.code)
		if len(rows) != 1 || rows[0].Account != want.code ||
			rows[0].AccountTitle != want.title || rows[0].Detail != want.detail {
			t.Fatalf("update rows under %q = %+v", want.code, rows)
		}
	}

	if err := realm.DeleteAccount(ctx, updated.Code, true); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	for _, want := range wants {
		rows := accountUpdateAuditRows(t, ctx, realm, want.code)
		if len(rows) != 1 || rows[0].Detail != want.detail {
			t.Fatalf("surviving update rows under %q = %+v", want.code, rows)
		}
	}
}

func TestLocalNode_UpdateAccountTitleOnlyAuditsOnce(t *testing.T) {
	t.Parallel()
	engine := newFakeEngine()
	engine.enforceResolver = true
	n, realm := newTestNode(t, engine)
	ctx := context.Background()

	created, err := n.CreateAccount(ctx, domain.Account{
		Code: "account-a", Title: "Before",
	}, testCaller)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := n.UpdateAccount(
		ctx,
		testKey(created.Code),
		domain.Account{Code: created.Code, Title: "After"},
		testCaller,
	); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}

	rows := accountUpdateAuditRows(t, ctx, realm, created.Code)
	if len(rows) != 1 || rows[0].AccountTitle != "After" ||
		rows[0].Detail != "update account account-a" {
		t.Fatalf("title-only update rows = %+v", rows)
	}
}

func accountUpdateAuditRows(
	t *testing.T,
	ctx context.Context,
	realm store.RealmStore,
	code domain.AccountID,
) []domain.AuditRow {
	t.Helper()
	page, err := realm.ListAuditRows(ctx, store.AuditListFilter{
		Account: store.ExactTextMatcher(code.String()),
		Actions: []domain.AuditAction{domain.AuditActionUpdateAccount},
	})
	if err != nil {
		t.Fatalf("ListAuditRows account=%q: %v", code, err)
	}
	return page.Rows
}
