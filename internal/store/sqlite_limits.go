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

// Limits group of the SQLite store: one typed table per policy
// (limit_rate, limit_order_size, limit_pnl_bounds). Each row is keyed by the
// natural composite (scope, account_id, asset_id); there is no external id and
// no surrogate id outward. Account and asset are present only for scopes that
// carry them; an empty code means the NULL axis (scope-wide). Domain Validate()
// enforces allowed scopes and consistent axis presence before persist; the store
// applies that rule by calling Validate() on every Put. Unknown account or asset
// codes on a non-NULL axis are an error wrapping domain.ErrInvalid.

package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.openpit.dev/officer/internal/domain"
)

// --- Rate-limit barriers (limit_rate) ----------------------------------------

// ListRateLimits returns every rate-limit barrier. When account is non-empty
// only barriers whose account_id matches are returned, ordered by scope then
// account code then asset code.
func (r *realmStore) ListRateLimits(
	ctx context.Context, account domain.AccountID,
) ([]domain.LimitRate, error) {
	q := `
SELECT lr.scope, a.code, ast.code, lr.max_orders, lr.window
FROM limit_rate lr
LEFT JOIN accounts a   ON a.id   = lr.account_id
LEFT JOIN assets   ast ON ast.id = lr.asset_id`
	args := make([]any, 0, 1)
	if account != "" {
		q += ` WHERE a.code = ?`
		args = append(args, account.String())
	}
	q += ` ORDER BY lr.scope, a.code, ast.code`

	rows, err := r.db().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list rate limits: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.LimitRate, 0)
	for rows.Next() {
		l, err := scanRateLimit(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate rate limits: %w", err)
	}
	return result, nil
}

// PutRateLimit upserts one rate-limit barrier keyed by (scope, account, asset).
// It calls domain.Validate() before persisting; validation failure returns an
// error wrapping domain.ErrInvalid. Unknown account/asset codes on non-NULL axes
// also return domain.ErrInvalid.
//
// SQLite treats NULL as distinct in UNIQUE constraints, so the standard
// ON CONFLICT clause does not fire when account_id or asset_id is NULL. The
// upsert is implemented as a DELETE-then-INSERT inside a transaction, which
// correctly handles all NULL combinations of the composite key.
func (r *realmStore) PutRateLimit(ctx context.Context, limit domain.LimitRate) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	accountID, assetID, err := resolveLimitAxes(ctx, r.db(), limit.Account, limit.Asset)
	if err != nil {
		return err
	}
	return putLimitRow(ctx, r.db(), "limit_rate",
		limit.Scope, accountID, assetID,
		func(ctx context.Context, exec sqlExecer) error {
			_, err := exec.ExecContext(
				ctx,
				`INSERT INTO limit_rate
				 (scope, account_id, asset_id, max_orders, window)
				 VALUES (?, ?, ?, ?, ?)`,
				limit.Scope, accountID, assetID,
				int64(limit.MaxOrders),
				limit.Window.String(),
			)
			return err
		},
	)
}

// DeleteRateLimit removes the rate-limit barrier with the given composite.
// Returns domain.ErrNotFound when absent.
func (r *realmStore) DeleteRateLimit(
	ctx context.Context, scope domain.LimitScope, account domain.AccountID, asset string,
) error {
	accountID, assetID, err := resolveLimitAxes(ctx, r.db(), account, asset)
	if err != nil {
		return err
	}
	res, err := r.db().ExecContext(
		ctx,
		`DELETE FROM limit_rate
		 WHERE scope = ? AND account_id IS ? AND asset_id IS ?`,
		scope, accountID, assetID,
	)
	if err != nil {
		return fmt.Errorf("store: delete rate limit: %w", err)
	}
	return notFoundIfNoRows(res, "rate limit", scope)
}

func scanRateLimit(rows *sql.Rows) (domain.LimitRate, error) {
	var (
		scope                  string
		accountCode, assetCode sql.NullString
		maxOrders              int64
		windowStr              string
	)
	if err := rows.Scan(&scope, &accountCode, &assetCode, &maxOrders, &windowStr); err != nil {
		return domain.LimitRate{}, fmt.Errorf("store: scan rate limit: %w", err)
	}
	window, err := time.ParseDuration(windowStr)
	if err != nil {
		return domain.LimitRate{}, fmt.Errorf(
			"store: parse rate limit window %q: %w", windowStr, err,
		)
	}
	return domain.LimitRate{
		Scope:     scope,
		Account:   domain.AccountID(accountCode.String),
		Asset:     assetCode.String,
		MaxOrders: uint64(maxOrders),
		Window:    window,
	}, nil
}

// --- Order-size barriers (limit_order_size) ----------------------------------

// ListOrderSizeLimits returns every order-size barrier. When account is
// non-empty only barriers whose account_id matches are returned.
func (r *realmStore) ListOrderSizeLimits(
	ctx context.Context, account domain.AccountID,
) ([]domain.LimitOrderSize, error) {
	q := `
SELECT los.scope, a.code, ast.code, los.max_quantity, los.max_notional
FROM limit_order_size los
LEFT JOIN accounts a   ON a.id   = los.account_id
LEFT JOIN assets   ast ON ast.id = los.asset_id`
	args := make([]any, 0, 1)
	if account != "" {
		q += ` WHERE a.code = ?`
		args = append(args, account.String())
	}
	q += ` ORDER BY los.scope, a.code, ast.code`

	rows, err := r.db().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list order size limits: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.LimitOrderSize, 0)
	for rows.Next() {
		l, err := scanOrderSizeLimit(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate order size limits: %w", err)
	}
	return result, nil
}

// PutOrderSizeLimit upserts one order-size barrier keyed by its composite.
// Uses the same DELETE-then-INSERT pattern as PutRateLimit to handle nullable
// axis columns correctly in SQLite's UNIQUE constraint.
func (r *realmStore) PutOrderSizeLimit(ctx context.Context, limit domain.LimitOrderSize) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	accountID, assetID, err := resolveLimitAxes(ctx, r.db(), limit.Account, limit.Asset)
	if err != nil {
		return err
	}
	return putLimitRow(ctx, r.db(), "limit_order_size",
		limit.Scope, accountID, assetID,
		func(ctx context.Context, exec sqlExecer) error {
			_, err := exec.ExecContext(
				ctx,
				`INSERT INTO limit_order_size
				 (scope, account_id, asset_id, max_quantity, max_notional)
				 VALUES (?, ?, ?, ?, ?)`,
				limit.Scope, accountID, assetID,
				nullableString(limit.MaxQuantity),
				nullableString(limit.MaxNotional),
			)
			return err
		},
	)
}

// DeleteOrderSizeLimit removes the order-size barrier with the given composite.
// Returns domain.ErrNotFound when absent.
func (r *realmStore) DeleteOrderSizeLimit(
	ctx context.Context, scope domain.LimitScope, account domain.AccountID, asset string,
) error {
	accountID, assetID, err := resolveLimitAxes(ctx, r.db(), account, asset)
	if err != nil {
		return err
	}
	res, err := r.db().ExecContext(
		ctx,
		`DELETE FROM limit_order_size
		 WHERE scope = ? AND account_id IS ? AND asset_id IS ?`,
		scope, accountID, assetID,
	)
	if err != nil {
		return fmt.Errorf("store: delete order size limit: %w", err)
	}
	return notFoundIfNoRows(res, "order size limit", scope)
}

func scanOrderSizeLimit(rows *sql.Rows) (domain.LimitOrderSize, error) {
	var (
		scope                    string
		accountCode, assetCode   sql.NullString
		maxQuantity, maxNotional sql.NullString
	)
	if err := rows.Scan(
		&scope, &accountCode, &assetCode, &maxQuantity, &maxNotional,
	); err != nil {
		return domain.LimitOrderSize{}, fmt.Errorf("store: scan order size limit: %w", err)
	}
	return domain.LimitOrderSize{
		Scope:       scope,
		Account:     domain.AccountID(accountCode.String),
		Asset:       assetCode.String,
		MaxQuantity: maxQuantity.String,
		MaxNotional: maxNotional.String,
	}, nil
}

// --- P&L-bounds barriers (limit_pnl_bounds) ----------------------------------

// ListPnlBoundsLimits returns every P&L-bounds barrier. When account is
// non-empty only barriers whose account_id matches are returned.
func (r *realmStore) ListPnlBoundsLimits(
	ctx context.Context, account domain.AccountID,
) ([]domain.LimitPnlBounds, error) {
	q := `
SELECT lpb.scope, a.code, ast.code, lpb.lower_bound, lpb.upper_bound, lpb.initial_pnl
FROM limit_pnl_bounds lpb
LEFT JOIN accounts a   ON a.id   = lpb.account_id
LEFT JOIN assets   ast ON ast.id = lpb.asset_id`
	args := make([]any, 0, 1)
	if account != "" {
		q += ` WHERE a.code = ?`
		args = append(args, account.String())
	}
	q += ` ORDER BY lpb.scope, a.code, ast.code`

	rows, err := r.db().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list pnl bounds limits: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.LimitPnlBounds, 0)
	for rows.Next() {
		l, err := scanPnlBoundsLimit(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate pnl bounds limits: %w", err)
	}
	return result, nil
}

// PutPnlBoundsLimit upserts one P&L-bounds barrier keyed by its composite.
// Uses the same DELETE-then-INSERT pattern as PutRateLimit to handle nullable
// axis columns correctly in SQLite's UNIQUE constraint.
func (r *realmStore) PutPnlBoundsLimit(ctx context.Context, limit domain.LimitPnlBounds) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	accountID, assetID, err := resolveLimitAxes(ctx, r.db(), limit.Account, limit.Asset)
	if err != nil {
		return err
	}
	return putLimitRow(ctx, r.db(), "limit_pnl_bounds",
		limit.Scope, accountID, assetID,
		func(ctx context.Context, exec sqlExecer) error {
			_, err := exec.ExecContext(
				ctx,
				`INSERT INTO limit_pnl_bounds
				 (scope, account_id, asset_id, lower_bound, upper_bound, initial_pnl)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				limit.Scope, accountID, assetID,
				nullableString(limit.LowerBound),
				nullableString(limit.UpperBound),
				nullableString(limit.InitialPnl),
			)
			return err
		},
	)
}

// DeletePnlBoundsLimit removes the P&L-bounds barrier with the given composite.
// Returns domain.ErrNotFound when absent.
func (r *realmStore) DeletePnlBoundsLimit(
	ctx context.Context, scope domain.LimitScope, account domain.AccountID, asset string,
) error {
	accountID, assetID, err := resolveLimitAxes(ctx, r.db(), account, asset)
	if err != nil {
		return err
	}
	res, err := r.db().ExecContext(
		ctx,
		`DELETE FROM limit_pnl_bounds
		 WHERE scope = ? AND account_id IS ? AND asset_id IS ?`,
		scope, accountID, assetID,
	)
	if err != nil {
		return fmt.Errorf("store: delete pnl bounds limit: %w", err)
	}
	return notFoundIfNoRows(res, "pnl bounds limit", scope)
}

func scanPnlBoundsLimit(rows *sql.Rows) (domain.LimitPnlBounds, error) {
	var (
		scope                              string
		accountCode, assetCode             sql.NullString
		lowerBound, upperBound, initialPnl sql.NullString
	)
	if err := rows.Scan(
		&scope, &accountCode, &assetCode,
		&lowerBound, &upperBound, &initialPnl,
	); err != nil {
		return domain.LimitPnlBounds{}, fmt.Errorf("store: scan pnl bounds limit: %w", err)
	}
	return domain.LimitPnlBounds{
		Scope:      scope,
		Account:    domain.AccountID(accountCode.String),
		Asset:      assetCode.String,
		LowerBound: lowerBound.String,
		UpperBound: upperBound.String,
		InitialPnl: initialPnl.String,
	}, nil
}

// --- Limits-group shared helpers --------------------------------------------

// putLimitRow upserts one limit row in table using DELETE-then-INSERT inside a
// transaction. This is required because SQLite treats NULL as distinct in UNIQUE
// constraints: two rows with the same (scope, NULL, NULL) are considered
// different by the ON CONFLICT clause, so the standard upsert idiom silently
// inserts a duplicate. The delete-first approach is safe under SQLite's
// single-writer serialisation; it correctly handles all NULL combinations of
// the (scope, account_id, asset_id) composite key. insertFn executes the INSERT
// through the provided execer after the prior row (if any) is deleted.
func putLimitRow(
	ctx context.Context,
	db *sql.DB,
	table string,
	scope string,
	accountID, assetID sql.NullInt64,
	insertFn func(context.Context, sqlExecer) error,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin put limit %s: %w", table, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM `+table+`
		 WHERE scope = ? AND account_id IS ? AND asset_id IS ?`,
		scope, accountID, assetID,
	); err != nil {
		return fmt.Errorf("store: delete old limit %s: %w", table, err)
	}
	if err := insertFn(ctx, tx); err != nil {
		return fmt.Errorf("store: insert limit %s: %w", table, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit put limit %s: %w", table, err)
	}
	return nil
}

// resolveLimitAxes resolves account and asset codes to their nullable surrogate
// ids for a limit row insert or delete. An empty code maps to a NULL axis (the
// scope-wide case); a non-empty unknown code wraps domain.ErrInvalid.
func resolveLimitAxes(
	ctx context.Context, q sqlQueryer,
	account domain.AccountID, asset string,
) (sql.NullInt64, sql.NullInt64, error) {
	var accountID, assetID sql.NullInt64
	if account != "" {
		id, err := resolveAccountID(ctx, q, account)
		if err != nil {
			return sql.NullInt64{}, sql.NullInt64{}, err
		}
		accountID = sql.NullInt64{Int64: id, Valid: true}
	}
	if asset != "" {
		id, err := resolveAssetID(ctx, q, asset)
		if err != nil {
			return sql.NullInt64{}, sql.NullInt64{}, err
		}
		assetID = sql.NullInt64{Int64: id, Valid: true}
	}
	return accountID, assetID, nil
}
