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
	"context"
	"errors"
	"fmt"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
	"testing"
)

func TestResetReceiptAtomicAndInsertMissingAccountOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, source := newRealmStore(t, domain.DefaultRealm)
	account, err := source.CreateAccount(ctx, domain.Account{Code: "desk", Pnl: "12"})
	if err != nil {
		t.Fatal(err)
	}
	journal := source.(fwstore.AccountPnlResetStore)
	entry := fwstore.AuditEntry{Action: domain.AuditActionResetAccountPnl, Account: account.Code, AccountIdentity: account.EngineAccountID, Source: domain.SourceSystem}
	entry.Detail = domain.AuditDetail{Code: "invalid_reset_detail"}
	if err := journal.RecordAccountPnlReset(ctx, account, "run", entry); err == nil {
		t.Fatal("invalid audit accepted")
	}
	retained, _, err := source.GetAccount(ctx, "desk")
	if err != nil || retained.Pnl != "12" {
		t.Fatalf("failed transaction changed P&L %+v %v", retained, err)
	}
	if recorded, err := journal.AccountPnlResetRecorded(ctx, account.EngineAccountID, "run"); err != nil || recorded {
		t.Fatalf("failed transaction receipt=%v %v", recorded, err)
	}
	entry.Detail = domain.NewAuditDetail(domain.AuditDetailResetAccountPnl, domain.AuditDetailParams{"account": "desk", "previousPnl": "12"})
	if err := journal.RecordAccountPnlReset(ctx, account, "run", entry); err != nil {
		t.Fatal(err)
	}
	scope := backup.Scope{Sections: []backup.Section{backup.SectionAccountsGroups}}
	archive, err := source.ExportBackup(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	_, target := newRealmStore(t, domain.DefaultRealm)
	existing, err := target.CreateAccount(ctx, domain.Account{Code: "desk", Pnl: "5"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: backup.RestoreModeInsertMissing, Scope: scope}); err != nil {
		t.Fatal(err)
	}
	receipt, err := target.(fwstore.AccountPnlResetStore).AccountPnlResetRecorded(ctx, existing.EngineAccountID, "run")
	if err != nil || receipt {
		t.Fatalf("skipped account inherited foreign receipt %v %v", receipt, err)
	}
	if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: backup.RestoreModeOverwrite, Scope: scope}); err != nil {
		t.Fatal(err)
	}
	receipt, err = target.(fwstore.AccountPnlResetStore).AccountPnlResetRecorded(ctx, existing.EngineAccountID, "run")
	if err != nil || !receipt {
		t.Fatalf("restored account lost receipt %v %v", receipt, err)
	}
	archive.Data.AccountPnlResetReceipts[0].Account = "missing"
	if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: backup.RestoreModeOverwrite, Scope: scope}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing receipt account=%v", err)
	}
}

func TestResetReceiptRestoreModeAndSelectionMatrix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, mode := range []backup.RestoreMode{backup.RestoreModeReplaceAll, backup.RestoreModeOverwrite, backup.RestoreModeInsertMissing} {
		for _, selected := range []string{"a", "new", "all"} {
			t.Run(fmt.Sprintf("%s/%s", mode, selected), func(t *testing.T) {
				t.Parallel()
				_, source := newRealmStore(t, domain.DefaultRealm)
				_, target := newRealmStore(t, domain.DefaultRealm)
				record := func(st fwstore.RealmStore, code domain.AccountID, run string, pnl string) {
					t.Helper()
					account, err := st.CreateAccount(ctx, domain.Account{Code: code, Pnl: pnl})
					if err != nil {
						t.Fatal(err)
					}
					entry := fwstore.AuditEntry{Action: domain.AuditActionResetAccountPnl, Account: code, AccountIdentity: account.EngineAccountID, Source: domain.SourceSystem,
						Detail: domain.NewAuditDetail(domain.AuditDetailResetAccountPnl, domain.AuditDetailParams{"account": code.String(), "previousPnl": pnl})}
					if err := st.(fwstore.AccountPnlResetStore).RecordAccountPnlReset(ctx, account, run, entry); err != nil {
						t.Fatal(err)
					}
					if err := st.SetAccountPnl(ctx, code, pnl, ""); err != nil {
						t.Fatal(err)
					}
				}
				for _, code := range []domain.AccountID{"a", "new"} {
					record(source, code, "source", "12")
				}
				for _, code := range []domain.AccountID{"a", "other"} {
					record(target, code, "receiver", "5")
				}
				scope := backup.Scope{Sections: []backup.Section{backup.SectionAccountsGroups}}
				archive, err := source.ExportBackup(ctx, scope)
				if err != nil {
					t.Fatal(err)
				}
				if selected != "all" {
					scope.Accounts = backup.EntitySelector{Accounts: []string{selected}}
				}
				if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{Mode: mode, Scope: scope}); err != nil {
					t.Fatal(err)
				}
				for _, code := range []domain.AccountID{"a", "new", "other"} {
					applied := selected == "all" || selected == code.String()
					sourceAccount := code != "other"
					wasPresent := code != "new"
					wantPresent := wasPresent || (sourceAccount && applied)
					if mode == backup.RestoreModeReplaceAll && code == "other" && selected == "all" {
						wantPresent = false
					}
					account, exists, err := target.GetAccount(ctx, code)
					if err != nil || exists != wantPresent {
						t.Fatalf("account %s presence=%v want %v: %v", code, exists, wantPresent, err)
					}
					if !exists {
						continue
					}
					imported := sourceAccount && applied && (mode != backup.RestoreModeInsertMissing || !wasPresent)
					wantPnl := "5"
					if imported {
						wantPnl = "12"
					}
					if account.Pnl != wantPnl {
						t.Fatalf("account %s snapshot=%s want %s", code, account.Pnl, wantPnl)
					}
					journal := target.(fwstore.AccountPnlResetStore)
					for _, run := range []string{"source", "receiver"} {
						got, err := journal.AccountPnlResetRecorded(ctx, account.EngineAccountID, run)
						want := (run == "source" && imported) || (run == "receiver" && !imported && wasPresent)
						if err != nil || got != want {
							t.Fatalf("account %s receipt %s=%v want %v: %v", code, run, got, want, err)
						}
					}
				}
			})
		}
	}
}
