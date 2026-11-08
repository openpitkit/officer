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

package node

import (
	"context"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

type auditIdentityRealm struct {
	store.RealmStore
	entries []store.AuditEntry
}

func (realm *auditIdentityRealm) AppendAudit(ctx context.Context, entry store.AuditEntry) error {
	realm.entries = append(realm.entries, entry)
	return realm.RealmStore.AppendAudit(ctx, entry)
}

func (realm *auditIdentityRealm) AppendAuditBatch(ctx context.Context, entries []store.AuditEntry) error {
	realm.entries = append(realm.entries, entries...)
	return realm.RealmStore.AppendAuditBatch(ctx, entries)
}

func TestAuditIdentityCapturedBeforeRenameAndDelete(t *testing.T) {
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, original := newTestNode(t, eng)
	realm := &auditIdentityRealm{RealmStore: original}
	n.realm = realm
	ctx := context.Background()
	account, err := n.CreateAccount(ctx, domain.Account{Code: "before"}, testCaller)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SetAccountNotes(ctx, account.Code, "note", testCaller); err != nil {
		t.Fatal(err)
	}
	updated, err := n.UpdateAccount(ctx, account.Code, domain.Account{Code: "after"}, testCaller)
	if err != nil {
		t.Fatal(err)
	}
	n.build = func(snapshot engine.Snapshot) (engine.Engine, error) {
		next := newFakeEngine()
		return fakeBuild(next, new(engine.Snapshot))(snapshot)
	}
	if err := n.DeleteAccount(ctx, updated.Code, true, testCaller); err != nil {
		t.Fatal(err)
	}
	if account.EngineAccountID == 0 {
		t.Fatal("fixture has no identity")
	}
	seen := 0
	for _, entry := range realm.entries {
		if entry.Account == "" {
			continue
		}
		seen++
		if entry.AccountIdentity != account.EngineAccountID {
			t.Fatalf("%s under %s captured %d, want actual %d", entry.Action, entry.Account, entry.AccountIdentity, account.EngineAccountID)
		}
	}
	if seen != 5 {
		t.Fatalf("target audit records=%d, want create, notes, both rename filings, delete", seen)
	}
	if _, found, err := realm.GetAccount(ctx, updated.Code); err != nil || found {
		t.Fatalf("deleted target exists=%t err=%v", found, err)
	}
}
