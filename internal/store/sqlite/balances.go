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

// Spot-funds balance group of the SQLite store. Balances are keyed by the
// natural composite primary key (account_id, asset_id); there is no surrogate
// external id and no external id outward. Account and asset are always
// addressed and surfaced by code. The realized_pnl column stores the latest
// engine-reported absolute. Both UpsertBalance and settlement persistence write
// caller-supplied snapshots directly without recalculating PnL.

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

// balanceFrom is the shared source block for balance reads. The JOINs surface
// the account and asset as their codes so the surrogate ids never leave the
// store, and reach the three tiers of the account's currency cascade (account,
// its group, the reserved default group) that denominate the row's realized
// P&L and average entry price. It is shared by the projection and the count so
// the two always filter over the same rows.
const balanceFrom = `
FROM balance b
JOIN account a   ON a.id  = b.account_id
LEFT JOIN account_group g ON g.id = a.group_id
JOIN asset   ast ON ast.id = b.asset_id
LEFT JOIN asset ac ON ac.id = a.currency_asset_id
LEFT JOIN asset gc ON gc.id = g.currency_asset_id
LEFT JOIN account_group dg ON dg.code = ''
LEFT JOIN asset dc ON dc.id = dg.currency_asset_id`

// balanceAccountCurrency resolves the account currency cascade in SQL, mirroring
// domain.ResolveCurrencyCascade: the first tier with a currency wins, and no
// tier set yields the empty string. Rows are compared on this expression rather
// than on the account tier alone, so an inherited currency denominates its rows
// exactly as a directly assigned one does.
const balanceAccountCurrency = `COALESCE(NULLIF(ac.code, ''), ` +
	`NULLIF(gc.code, ''), NULLIF(dc.code, ''), '')`

// balanceSelect is the shared projection for balance reads.
const balanceSelect = `
SELECT a.code, ast.code,
       b.available, b.held, b.incoming, b.realized_pnl,
       b.realized_pnl_halt_reason, b.average_entry_price, b.updated_at,
       ` + balanceAccountCurrency + balanceFrom

// UpsertBalance inserts or replaces the balance snapshot for the
// (account, asset) named by balance. The caller supplies all amount fields
// including realized_pnl as an absolute value (snapshot semantics). The
// settlement path (settleBalanceTx) persists the engine absolute in its own tx;
// it uses INSERT OR REPLACE directly, keeping the two paths consistent.
// AccountCurrency is a read-only projection and is ignored on writes.
func (r *realmStore) UpsertBalance(ctx context.Context, balance domain.Balance) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	accountID, err := resolveAccountID(ctx, db, balance.Account)
	if err != nil {
		return err
	}
	assetID, err := resolveAssetID(ctx, db, balance.Asset)
	if err != nil {
		return err
	}
	updatedAt := balance.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err = db.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO balance
		 (account_id, asset_id, available, held,
		  incoming, realized_pnl, realized_pnl_halt_reason, average_entry_price, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		accountID, assetID,
		settleOrZero(balance.Available),
		settleOrZero(balance.Held),
		settleOrZero(balance.Incoming),
		settleOrZero(balance.RealizedPnl),
		balance.RealizedPnlHaltReason,
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
	db, err := r.db()
	if err != nil {
		return domain.Balance{}, false, err
	}
	row := db.QueryRowContext(
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
	page, err := r.ListBalanceRows(ctx, fwstore.BalanceListFilter{
		Account: fwstore.ExactTextMatcher(account.String()),
		Asset:   fwstore.ExactTextMatcher(asset),
	})
	if err != nil {
		return nil, err
	}
	result := make([]domain.Balance, 0, len(page.Rows))
	for _, row := range page.Rows {
		result = append(result, row.Balance)
	}
	return result, nil
}

// ListAccountsWithOpenBalances returns accounts carrying a non-zero balance,
// cost-basis or P&L value. A halted P&L holds no trustworthy number, so neither
// the halt flag nor the number behind it counts an account as open: the engine
// leaves the prior P&L in place when it reports a halt without a value, and
// that retained number is historical, not authoritative. Quantities and cost
// basis are not P&L and keep counting regardless: a halted position still
// holds real units at a real cost basis, applied from the report's own inputs.
// The account outer join keeps accounts whose only value is the account-level
// P&L, which survives the deletion of every balance row.
func (r *realmStore) ListAccountsWithOpenBalances(
	ctx context.Context, accounts []domain.AccountID,
) ([]domain.AccountID, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	args := make([]any, 0, len(accounts))
	accountWhere := ""
	if len(accounts) > 0 {
		placeholders := make([]string, 0, len(accounts))
		for _, account := range accounts {
			placeholders = append(placeholders, "?")
			args = append(args, account.String())
		}
		accountWhere = " AND a.code IN (" + strings.Join(placeholders, ", ") + ")"
	}
	query := `SELECT DISTINCT a.code
FROM account a
LEFT JOIN balance b ON b.account_id = a.id
WHERE (
    (b.available <> '' AND b.available COLLATE DECIMAL <> '0') OR
    (b.held <> '' AND b.held COLLATE DECIMAL <> '0') OR
    (b.incoming <> '' AND b.incoming COLLATE DECIMAL <> '0') OR
    (b.average_entry_price <> '' AND b.average_entry_price COLLATE DECIMAL <> '0') OR
    (b.realized_pnl_halt_reason = '' AND
     b.realized_pnl <> '' AND b.realized_pnl COLLATE DECIMAL <> '0') OR
    (a.pnl_halt_reason = '' AND a.pnl <> '' AND a.pnl COLLATE DECIMAL <> '0')
)` + accountWhere + `
ORDER BY a.code`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list open balance accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]domain.AccountID, 0)
	for rows.Next() {
		var account domain.AccountID
		if err := rows.Scan(&account); err != nil {
			return nil, fmt.Errorf("store: scan open balance account: %w", err)
		}
		result = append(result, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate open balance accounts: %w", err)
	}
	return result, nil
}

// ListBalanceRows returns balances matching filter, with total count before
// paging.
func (r *realmStore) ListBalanceRows(
	ctx context.Context, filter fwstore.BalanceListFilter,
) (fwstore.BalanceListPage, error) {
	if err := filter.Validate(); err != nil {
		return fwstore.BalanceListPage{}, err
	}
	where, args := balanceListWhere(filter)
	db, err := r.db()
	if err != nil {
		return fwstore.BalanceListPage{}, err
	}
	countQuery := `SELECT COUNT(*)` + balanceFrom + where
	var total int
	if err := db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return fwstore.BalanceListPage{}, fmt.Errorf("store: count balance rows: %w", err)
	}

	queryArgs := append([]any{}, args...)
	query := balanceSelect + where + balanceListOrderBy(filter.Sort)
	if filter.Page.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		queryArgs = append(queryArgs, filter.Page.Limit, max(filter.Page.Offset, 0))
	}
	rows, err := db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return fwstore.BalanceListPage{}, fmt.Errorf("store: list balance rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]fwstore.BalanceListRow, 0)
	for rows.Next() {
		b, err := scanBalance(rows)
		if err != nil {
			return fwstore.BalanceListPage{}, err
		}
		result = append(result, fwstore.BalanceListRow{Balance: b})
	}
	if err := rows.Err(); err != nil {
		return fwstore.BalanceListPage{}, fmt.Errorf("store: iterate balance rows: %w", err)
	}
	return fwstore.BalanceListPage{Rows: result, Total: total}, nil
}

func balanceListWhere(filter fwstore.BalanceListFilter) (string, []any) {
	clauses := make([]string, 0)
	args := make([]any, 0)
	appendMatcher(&clauses, &args, "a.code", filter.Account)
	if filter.GroupCode != nil {
		if *filter.GroupCode == "" {
			clauses = append(clauses, "a.group_id IS NULL")
		} else {
			clauses = append(clauses, "g.code = ?")
			args = append(args, *filter.GroupCode)
		}
	}
	appendMatcher(&clauses, &args, "ast.code", filter.Asset)
	appendDecimalRangeFilter(&clauses, &args, "b.available", filter.Available)
	appendDecimalRangeFilter(&clauses, &args, "b.held", filter.Held)
	appendDecimalRangeFilter(&clauses, &args, "b.incoming", filter.Incoming)
	appendDenominatedDecimalRangeFilter(
		&clauses, &args, "b.average_entry_price", balanceAccountCurrency,
		filter.AverageEntryPrice,
	)
	appendDenominatedDecimalRangeFilter(
		&clauses, &args, "b.realized_pnl", balanceAccountCurrency,
		filter.RealizedPnl,
	)
	appendTimeRangeFilter(&clauses, &args, "b.updated_at", filter.UpdatedAt)
	if len(clauses) == 0 {
		return "", args
	}
	return "\nWHERE " + strings.Join(clauses, " AND "), args
}

func balanceListOrderBy(sort fwstore.SortSpec) string {
	columns := map[string]string{
		"account":   "a.code",
		"asset":     "ast.code",
		"available": "b.available",
		"held":      "b.held",
		"incoming":  "b.incoming",
		"updatedAt": "b.updated_at",
	}
	column := columns[sort.Column]
	if column == "" {
		column = "a.code"
	}
	direction := "ASC"
	tieDirection := "ASC"
	if sort.Descending {
		direction = "DESC"
		tieDirection = "DESC"
	}
	return "\nORDER BY " + column + " " + direction +
		", a.code " + tieDirection + ", ast.code " + tieDirection
}

// DeleteBalance removes the balance for (account, asset). Returns
// domain.ErrNotFound when the row is absent.
func (r *realmStore) DeleteBalance(
	ctx context.Context, account domain.AccountID, asset string,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
		ctx,
		`DELETE FROM balance
		 WHERE account_id = (SELECT id FROM account WHERE code = ?)
		   AND asset_id   = (SELECT id FROM asset   WHERE code = ?)`,
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
// decimals; updated_at is RFC3339Nano UTC text. The three currency tiers are
// resolved into the single effective account currency that denominates the
// row's realized P&L and average entry price.
func scanBalanceInto(scan func(...any) error, b *domain.Balance) error {
	var (
		accountCode, assetCode                                       string
		available, held, incoming                                    string
		realizedPnl, realizedPnlHaltReason, avgEntryPrice, updatedAt string
		accountCurrency                                              string
	)
	if err := scan(
		&accountCode, &assetCode,
		&available, &held, &incoming, &realizedPnl, &realizedPnlHaltReason,
		&avgEntryPrice, &updatedAt,
		&accountCurrency,
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
	b.RealizedPnlHaltReason = domain.PnlHaltReason(realizedPnlHaltReason)
	b.AverageEntryPrice = avgEntryPrice
	b.AccountCurrency = accountCurrency
	b.UpdatedAt = t
	return nil
}
