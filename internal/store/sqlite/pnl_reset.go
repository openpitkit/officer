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
	"go.openpit.dev/officer/framework/scheduler"
	fwstore "go.openpit.dev/officer/framework/store"
)

var _ fwstore.AccountPnlResetStore = (*realmStore)(nil)

func (r *realmStore) exportPnlResetReceipts(ctx context.Context) (result []backup.AccountPnlResetReceipt, err error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT r.run_id, a.code FROM account_pnl_reset_receipt r JOIN account a ON a.id = r.account_id ORDER BY r.run_id, a.code`)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var receipt backup.AccountPnlResetReceipt
		if err := rows.Scan(&receipt.RunID, &receipt.Account); err != nil {
			return nil, err
		}
		result = append(result, receipt)
	}
	return result, rows.Err()
}

func (rt *restoreTx) restorePnlResetReceipts(ctx context.Context, receipts []backup.AccountPnlResetReceipt) error {
	for _, receipt := range receipts {
		if receipt.RunID == "" {
			return fmt.Errorf("store: reset receipt requires runId: %w", domain.ErrInvalid)
		}
		accountID, err := resolveAccountID(ctx, rt.tx, receipt.Account)
		if err != nil {
			return fmt.Errorf("store: reset receipt requires accounts_groups account %q: %v: %w", receipt.Account, err, domain.ErrInvalid)
		}
		if _, err := rt.tx.ExecContext(ctx, `INSERT INTO account_pnl_reset_receipt(run_id, account_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, receipt.RunID, accountID); err != nil {
			return err
		}
	}
	return nil
}

// SchedulerStore explicitly advertises this connector's scheduler capability
// to standalone composition without changing the shared Store interface.
func (s *sqliteStore) SchedulerStore(ctx context.Context, realm domain.RealmID) (scheduler.Store, error) {
	return NewSchedulerStore(ctx, s, realm)
}

// AccountPnlResetRecorded answers from committed account-owned receipts.
func (r *realmStore) AccountPnlResetRecorded(ctx context.Context, account domain.EngineAccountID, runID string) (bool, error) {
	if runID == "" || account == 0 {
		return false, domain.ErrInvalid
	}
	db, err := r.db()
	if err != nil {
		return false, err
	}
	var found bool
	err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_pnl_reset_receipt
	 WHERE account_id = ? AND run_id = ?)`, account, runID).Scan(&found)
	return found, err
}

// RecordAccountPnlReset commits zero and its idempotence/audit evidence in one
// transaction. A post-engine failure is returned to the node's fail-stop path.
func (r *realmStore) RecordAccountPnlReset(ctx context.Context, account domain.Account, runID string, entry fwstore.AuditEntry) (err error) {
	if entry.Action != domain.AuditActionResetAccountPnl || entry.Account != account.Code || entry.AccountIdentity != account.EngineAccountID {
		return fmt.Errorf("reset audit does not identify the account: %w", domain.ErrInvalid)
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	dictionaries, err := r.dictionaries()
	if err != nil {
		return err
	}
	tx, err := beginTx(ctx, db, nil)
	if err != nil {
		return err
	}
	defer rollbackTransaction(ctx, &err, tx)
	if err := setAccountPnl(ctx, tx, account.Code, "0", ""); err != nil {
		return err
	}
	if err := appendAudit(ctx, tx, dictionaries, entry); err != nil {
		return err
	}
	if runID != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_pnl_reset_receipt(run_id, account_id) VALUES (?, ?)`, runID, account.EngineAccountID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
