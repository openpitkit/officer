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

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func TestLocalNode_SetSettingStoresAndAudits(t *testing.T) {
	t.Parallel()
	n, realm := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	before := auditRowCount(t, ctx, realm)

	if err := n.SetSetting(ctx, "example_key", "example-value", testCaller); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	value, ok, err := n.Setting(ctx, "example_key")
	if err != nil || !ok || value != "example-value" {
		t.Fatalf("Setting = %q, %v, %v; want %q, true, nil", value, ok, err, "example-value")
	}
	if after := auditRowCount(t, ctx, realm); after != before+1 {
		t.Fatalf("audit rows = %d, want %d (exactly one appended)", after, before+1)
	}
	rows, err := realm.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionSetSetting},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || rows[0].Detail != "set setting example_key=example-value" ||
		rows[0].Actor != testCaller.Principal || rows[0].Source != testCaller.Source {
		t.Fatalf("set_setting audit = %+v", rows)
	}
}

func TestLocalNode_SettingRejectsEmptyKey(t *testing.T) {
	t.Parallel()
	n, realm := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	before := auditRowCount(t, ctx, realm)

	err := n.SetSetting(ctx, "", "value", testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SetSetting empty key error = %v, want ErrInvalid", err)
	}
	if pointer, constraint := domain.ValidationPointer(err), domain.ValidationConstraint(err); pointer != "/key" || constraint != "required" {
		t.Fatalf("SetSetting empty key pointer, constraint = %q, %q; want %q, %q", pointer, constraint, "/key", "required")
	}
	if _, _, err := n.Setting(ctx, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Setting empty key error = %v, want ErrInvalid", err)
	}

	if _, ok, err := realm.GetSetting(ctx, ""); err != nil || ok {
		t.Fatalf("store row for empty key: ok = %v, err = %v; want none", ok, err)
	}
	if after := auditRowCount(t, ctx, realm); after != before {
		t.Fatalf("audit rows = %d, want %d (refused write audited)", after, before)
	}
}

func TestLocalNode_SetSettingRefusesSigningOwnedKey(t *testing.T) {
	t.Parallel()
	n, realm := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	beforeValue, beforeOK, err := realm.GetSetting(ctx, domain.SettingKeyNoESign)
	if err != nil {
		t.Fatalf("GetSetting before: %v", err)
	}
	refused := "0"
	if beforeOK && beforeValue == "0" {
		refused = "1"
	}

	err = n.SetSetting(ctx, domain.SettingKeyNoESign, refused, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SetSetting no_esign error = %v, want ErrInvalid", err)
	}
	if pointer, constraint := domain.ValidationPointer(err), domain.ValidationConstraint(err); pointer != "/key" || constraint != "reserved" {
		t.Fatalf("SetSetting no_esign pointer, constraint = %q, %q; want %q, %q", pointer, constraint, "/key", "reserved")
	}

	afterValue, afterOK, err := realm.GetSetting(ctx, domain.SettingKeyNoESign)
	if err != nil || afterValue != beforeValue || afterOK != beforeOK {
		t.Fatalf("no_esign after refused write = %q, %v, %v; want %q, %v, nil", afterValue, afterOK, err, beforeValue, beforeOK)
	}
	if rows := setSettingAuditRows(t, ctx, realm); len(rows) != 0 {
		t.Fatalf("set_setting audit after refused write = %+v, want none", rows)
	}
	// The reservation is write-side only: reads of the key stay allowed.
	if value, ok, err := n.Setting(ctx, domain.SettingKeyNoESign); err != nil || value != beforeValue || ok != beforeOK {
		t.Fatalf("Setting no_esign = %q, %v, %v; want %q, %v, nil", value, ok, err, beforeValue, beforeOK)
	}
}

func TestLocalNode_SetSettingRejectedWhileRestarting(t *testing.T) {
	t.Parallel()
	n, realm := newTestNode(t, newFakeEngine())
	ctx := context.Background()

	n.restarting.Store(true)
	err := n.SetSetting(ctx, "example_key", "example-value", testCaller)
	n.restarting.Store(false)
	if !errors.Is(err, domain.ErrEngineRestarting) {
		t.Fatalf("SetSetting while restarting error = %v, want ErrEngineRestarting", err)
	}
	if value, ok, err := realm.GetSetting(ctx, "example_key"); err != nil || ok {
		t.Fatalf("store row after rejected write = %q, %v, %v; want none", value, ok, err)
	}
	if rows := setSettingAuditRows(t, ctx, realm); len(rows) != 0 {
		t.Fatalf("set_setting audit after rejected write = %+v, want none", rows)
	}
}

func TestLocalNode_SetSettingStoreFailureNotAudited(t *testing.T) {
	t.Parallel()
	n, realm := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	n.realm = &failSetSettingRealm{RealmStore: realm}

	err := n.SetSetting(ctx, "example_key", "example-value", testCaller)
	if !errors.Is(err, errSetSettingStore) {
		t.Fatalf("SetSetting with failing store error = %v, want %v", err, errSetSettingStore)
	}
	if rows := setSettingAuditRows(t, ctx, realm); len(rows) != 0 {
		t.Fatalf("set_setting audit after failed store write = %+v, want none", rows)
	}
}

var errSetSettingStore = errors.New("set setting store failure")

// failSetSettingRealm fails every realm-setting write and passes everything
// else, including audit appends, through to the wrapped realm.
type failSetSettingRealm struct{ store.RealmStore }

func (r *failSetSettingRealm) SetSetting(context.Context, string, string) error {
	return errSetSettingStore
}

func setSettingAuditRows(t *testing.T, ctx context.Context, realm store.RealmStore) []domain.AuditRow {
	t.Helper()
	rows, err := realm.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionSetSetting},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	return rows
}

func auditRowCount(t *testing.T, ctx context.Context, realm store.RealmStore) int {
	t.Helper()
	rows, err := realm.ListAudit(ctx, 1000)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	return len(rows)
}
