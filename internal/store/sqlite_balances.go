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

// Spot-funds balances group of the SQLite store. Balances are keyed by the
// natural composite primary key (account_id, asset_id); there is no surrogate
// external id and no external id outward. Account and asset are always
// addressed and surfaced by code. The realized_pnl column is delta-accumulated
// by the settlement path (settleBalanceTx in sqlite_orders.go); UpsertBalance
// must preserve that semantic: it writes the caller-supplied value directly,
// not accumulate it. The settlement path owns accumulation; UpsertBalance is
// the snapshot path (business CSV import, position snapshot) where the caller
// has already computed the desired absolute value.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

// balanceSelect is the shared projection for balance reads. The JOINs surface
// the account and asset as their codes so the surrogate ids never leave the
// store.
const balanceSelect = `
SELECT a.code, ast.code,
       b.available, b.held, b.incoming, b.realized_pnl,
       b.average_entry_price, b.updated_at
FROM balances b
JOIN accounts a   ON a.id  = b.account_id
JOIN assets   ast ON ast.id = b.asset_id`

// UpsertBalance inserts or replaces the balance snapshot for the
// (account, asset) named by balance. The caller supplies all amount fields
// including realized_pnl as an absolute value (snapshot semantics). The
// settlement path (settleBalanceTx) handles delta accumulation in its own tx;
// it uses INSERT OR REPLACE directly, keeping the two paths consistent.
func (r *realmStore) UpsertBalance(ctx context.Context, balance domain.Balance) error {
	accountID, err := resolveAccountID(ctx, r.db(), balance.Account)
	if err != nil {
		return err
	}
	assetID, err := resolveAssetID(ctx, r.db(), balance.Asset)
	if err != nil {
		return err
	}
	updatedAt := balance.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err = r.db().ExecContext(
		ctx,
		`INSERT OR REPLACE INTO balances
		 (account_id, asset_id, available, held, incoming,
		  realized_pnl, average_entry_price, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		accountID, assetID,
		settleOrZero(balance.Available),
		settleOrZero(balance.Held),
		settleOrZero(balance.Incoming),
		settleOrZero(balance.RealizedPnl),
		balance.AverageEntryPrice,
		updatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("store: upsert balance: %w", err)
	}
	return nil
}

// GetBalance returns the balance for (account, asset). The bool is false when
// no row exists. An unknown account or asset code is not an error from this
// method — there simply is no row to return.
func (r *realmStore) GetBalance(
	ctx context.Context, account domain.AccountID, asset string,
) (domain.Balance, bool, error) {
	row := r.db().QueryRowContext(
		ctx,
		balanceSelect+` WHERE a.code = ? AND ast.code = ?`,
		account.String(), asset,
	)
	b, err := scanBalanceRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Balance{}, false, nil
	}
	if err != nil {
		return domain.Balance{}, false, fmt.Errorf("store: get balance: %w", err)
	}
	return b, true, nil
}

// ListBalances returns balance rows matching the non-empty filter fields. A
// non-empty account narrows to that account; a non-empty asset narrows to that
// asset; both empty returns every balance. Rows are ordered by account code
// then asset code for stable output.
func (r *realmStore) ListBalances(
	ctx context.Context, account domain.AccountID, asset string,
) ([]domain.Balance, error) {
	q := balanceSelect + ` WHERE 1 = 1`
	args := make([]any, 0, 2)
	if account != "" {
		q += ` AND a.code = ?`
		args = append(args, account.String())
	}
	if asset != "" {
		q += ` AND ast.code = ?`
		args = append(args, asset)
	}
	q += ` ORDER BY a.code, ast.code`

	rows, err := r.db().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list balances: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.Balance, 0)
	for rows.Next() {
		b, err := scanBalance(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate balances: %w", err)
	}
	return result, nil
}

// DeleteBalance removes the balance for (account, asset). Returns
// domain.ErrNotFound when the row is absent.
func (r *realmStore) DeleteBalance(
	ctx context.Context, account domain.AccountID, asset string,
) error {
	res, err := r.db().ExecContext(
		ctx,
		`DELETE FROM balances
		 WHERE account_id = (SELECT id FROM accounts WHERE code = ?)
		   AND asset_id   = (SELECT id FROM assets   WHERE code = ?)`,
		account.String(), asset,
	)
	if err != nil {
		return fmt.Errorf("store: delete balance: %w", err)
	}
	return notFoundIfNoRows(res, "balance", account.String()+"/"+asset)
}

// --- Balance scan helpers ----------------------------------------------------

func scanBalance(rows *sql.Rows) (domain.Balance, error) {
	var b domain.Balance
	if err := scanBalanceInto(rows.Scan, &b); err != nil {
		return domain.Balance{}, fmt.Errorf("store: scan balance: %w", err)
	}
	return b, nil
}

func scanBalanceRow(row *sql.Row) (domain.Balance, error) {
	var b domain.Balance
	if err := scanBalanceInto(row.Scan, &b); err != nil {
		return domain.Balance{}, err
	}
	return b, nil
}

// scanBalanceInto scans one balance projection into b. Amounts are exact text
// decimals; updated_at is RFC3339Nano UTC text.
func scanBalanceInto(scan func(...any) error, b *domain.Balance) error {
	var (
		accountCode, assetCode                string
		available, held, incoming             string
		realizedPnl, avgEntryPrice, updatedAt string
	)
	if err := scan(
		&accountCode, &assetCode,
		&available, &held, &incoming, &realizedPnl,
		&avgEntryPrice, &updatedAt,
	); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return fmt.Errorf("store: parse balance updated_at %q: %w", updatedAt, err)
	}
	b.Account = domain.AccountID(accountCode)
	b.Asset = assetCode
	b.Available = available
	b.Held = held
	b.Incoming = incoming
	b.RealizedPnl = realizedPnl
	b.AverageEntryPrice = avgEntryPrice
	b.UpdatedAt = t
	return nil
}
