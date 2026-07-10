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
// (limit_rate, limit_order_size, limit_spot_funds_pnl_bound).
// Each row is keyed by its natural composite; there is no external id and no
// surrogate id outward. Account, asset, account group and account currency are
// present only for policies and scopes that carry them; an empty code means the
// NULL axis (scope-wide). Domain Validate() enforces allowed scopes and
// consistent axis presence before persist; the store applies that rule by
// calling Validate() on every Put. Unknown dictionary codes on a non-NULL axis
// are an error wrapping domain.ErrInvalid.

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

// --- Unified policy list (flattens the three barrier tables) -----------------

// policyUnion projects the typed barrier tables onto one common shape so
// they can be sorted, filtered, and paged together. Columns absent for a given
// barrier kind are NULL; stable_id is the kind-prefixed surrogate id, unique
// across the union, used as the deterministic sort tiebreak. The dictionary
// codes come from the same LEFT JOINs the per-kind reads use.
const policyUnion = `
SELECT 'rate_limit' AS kind, lr.scope AS scope,
       a.code AS account_code, NULL AS account_group_code,
       ast.code AS asset_code, NULL AS account_currency_code,
       lr.max_orders AS max_orders, lr.window AS window,
       NULL AS max_quantity, NULL AS max_notional,
       NULL AS lower_bound, NULL AS upper_bound, NULL AS initial_pnl,
       'rate_limit:' || lr.id AS stable_id
FROM limit_rate lr
LEFT JOIN account a   ON a.id   = lr.account_id
LEFT JOIN asset   ast ON ast.id = lr.asset_id
UNION ALL
SELECT 'order_size_limit' AS kind, los.scope AS scope,
       a.code AS account_code, NULL AS account_group_code,
       ast.code AS asset_code, NULL AS account_currency_code,
       NULL AS max_orders, NULL AS window,
       los.max_quantity AS max_quantity, los.max_notional AS max_notional,
       NULL AS lower_bound, NULL AS upper_bound, NULL AS initial_pnl,
       'order_size_limit:' || los.id AS stable_id
FROM limit_order_size los
LEFT JOIN account a   ON a.id   = los.account_id
LEFT JOIN asset   ast ON ast.id = los.asset_id
UNION ALL
SELECT 'spot_funds_pnl_bounds_kill_switch' AS kind, lsfpb.scope AS scope,
       a.code AS account_code, g.code AS account_group_code,
       NULL AS asset_code, ac.code AS account_currency_code,
       NULL AS max_orders, NULL AS window,
       NULL AS max_quantity, NULL AS max_notional,
       lsfpb.lower_bound AS lower_bound, lsfpb.upper_bound AS upper_bound,
       lsfpb.initial_pnl AS initial_pnl,
       'spot_funds_pnl_bounds_kill_switch:' || lsfpb.id AS stable_id
FROM limit_spot_funds_pnl_bound lsfpb
LEFT JOIN account       a  ON a.id  = lsfpb.account_id
LEFT JOIN account_group g  ON g.id  = lsfpb.account_group_id
LEFT JOIN asset         ac ON ac.id = lsfpb.account_currency_asset_id`

// ListPolicyRows returns the typed barrier tables flattened into one sorted,
// paged list with the pre-paging total. It applies filters in SQL and
// reconstructs each row's typed value from the projected columns.
func (r *realmStore) ListPolicyRows(
	ctx context.Context, filter fwstore.PolicyListFilter,
) (fwstore.PolicyListPage, error) {
	where, args := policyListWhere(filter)
	from := ` FROM (` + policyUnion + `) AS policies` + where

	db, err := r.db()
	if err != nil {
		return fwstore.PolicyListPage{}, err
	}

	countQuery := `SELECT COUNT(*)` + from
	var total int
	if err := db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return fwstore.PolicyListPage{}, fmt.Errorf("store: count policy rows: %w", err)
	}

	queryArgs := append([]any{}, args...)
	query := `SELECT kind, scope, account_code, account_group_code,
       asset_code, account_currency_code,
       max_orders, window, max_quantity, max_notional,
       lower_bound, upper_bound, initial_pnl` + from + policyListOrderBy(filter.Sort)
	if filter.Page.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		queryArgs = append(queryArgs, filter.Page.Limit, max(filter.Page.Offset, 0))
	}

	rows, err := db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return fwstore.PolicyListPage{}, fmt.Errorf("store: list policy rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]fwstore.PolicyListRow, 0)
	for rows.Next() {
		row, err := scanPolicyRow(rows)
		if err != nil {
			return fwstore.PolicyListPage{}, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return fwstore.PolicyListPage{}, fmt.Errorf("store: iterate policy rows: %w", err)
	}
	return fwstore.PolicyListPage{Rows: result, Total: total}, nil
}

func policyListWhere(filter fwstore.PolicyListFilter) (string, []any) {
	clauses := make([]string, 0, 5)
	args := make([]any, 0, 5)
	appendMatcher(&clauses, &args, "account_code", filter.Account)
	appendMatcher(&clauses, &args, "account_group_code", filter.AccountGroup)
	appendMatcher(&clauses, &args, "asset_code", filter.Asset)
	appendMatcher(&clauses, &args, "account_currency_code", filter.AccountCurrency)
	if filter.Kind != nil {
		clauses = append(clauses, "kind = ?")
		args = append(args, string(*filter.Kind))
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func policyListOrderBy(sort fwstore.SortSpec) string {
	columns := map[string]string{
		"account":         "account_code",
		"accountCurrency": "account_currency_code",
		"accountGroup":    "account_group_code",
		"asset":           "asset_code",
		"initialPnl":      "initial_pnl COLLATE DECIMAL",
		"lowerBound":      "lower_bound COLLATE DECIMAL",
		"maxNotional":     "max_notional COLLATE DECIMAL",
		"maxOrders":       "max_orders",
		"maxQuantity":     "max_quantity COLLATE DECIMAL",
		"policy":          "kind",
		"scope":           "scope",
		"upperBound":      "upper_bound COLLATE DECIMAL",
	}
	column := columns[sort.Column]
	if column == "" {
		column = "kind"
	}
	direction := "ASC"
	tieDirection := "ASC"
	if sort.Descending {
		direction = "DESC"
		tieDirection = "DESC"
	}
	// The (kind, scope, account, asset) composite is unique across the union, so
	// it is a deterministic tiebreak the cross-shard merge can reproduce without
	// the per-table surrogate id. stable_id stays the final tiebreak for the
	// degenerate case where the composite repeats across shards.
	tie := "kind " + tieDirection + ", scope " + tieDirection +
		", account_code " + tieDirection +
		", account_group_code " + tieDirection +
		", asset_code " + tieDirection +
		", account_currency_code " + tieDirection +
		", stable_id " + tieDirection
	return " ORDER BY " + column + " " + direction + ", " + tie
}

func scanPolicyRow(rows *sql.Rows) (fwstore.PolicyListRow, error) {
	var (
		kind                               string
		scope                              string
		accountCode, accountGroupCode      sql.NullString
		assetCode, accountCurrencyCode     sql.NullString
		maxOrders                          sql.NullInt64
		windowStr                          sql.NullString
		maxQuantity                        sql.NullString
		maxNotional                        sql.NullString
		lowerBound, upperBound, initialPnl sql.NullString
	)
	if err := rows.Scan(
		&kind, &scope, &accountCode, &accountGroupCode,
		&assetCode, &accountCurrencyCode,
		&maxOrders, &windowStr, &maxQuantity, &maxNotional,
		&lowerBound, &upperBound, &initialPnl,
	); err != nil {
		return fwstore.PolicyListRow{}, fmt.Errorf("store: scan policy row: %w", err)
	}
	account := domain.AccountID(accountCode.String)
	row := fwstore.PolicyListRow{
		Kind:            fwstore.PolicyKind(kind),
		Scope:           scope,
		Account:         account,
		AccountGroup:    accountGroupCode.String,
		Asset:           assetCode.String,
		AccountCurrency: accountCurrencyCode.String,
	}
	switch row.Kind {
	case fwstore.PolicyKindRate:
		window, err := time.ParseDuration(windowStr.String)
		if err != nil {
			return fwstore.PolicyListRow{}, fmt.Errorf(
				"store: parse rate limit window %q: %w", windowStr.String, err,
			)
		}
		row.Rate = &domain.LimitRate{
			Scope:     scope,
			Account:   account,
			Asset:     assetCode.String,
			Window:    window,
			MaxOrders: uint64(maxOrders.Int64),
		}
	case fwstore.PolicyKindOrderSize:
		row.OrderSize = &domain.LimitOrderSize{
			Scope:       scope,
			Account:     account,
			Asset:       assetCode.String,
			MaxQuantity: maxQuantity.String,
			MaxNotional: maxNotional.String,
		}
	case fwstore.PolicyKindSpotFundsPnlBounds:
		row.SpotFundsPnlBounds = &domain.LimitSpotFundsPnlBounds{
			Scope:           scope,
			Account:         account,
			AccountGroup:    accountGroupCode.String,
			AccountCurrency: accountCurrencyCode.String,
			LowerBound:      lowerBound.String,
			UpperBound:      upperBound.String,
			InitialPnl:      initialPnl.String,
		}
	default:
		return fwstore.PolicyListRow{}, fmt.Errorf(
			"store: unknown policy kind %q: %w", kind, domain.ErrInvalid,
		)
	}
	return row, nil
}

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
LEFT JOIN account a   ON a.id   = lr.account_id
LEFT JOIN asset   ast ON ast.id = lr.asset_id`
	args := make([]any, 0, 1)
	if account != "" {
		q += ` WHERE a.code = ?`
		args = append(args, account.String())
	}
	q += ` ORDER BY lr.scope, a.code, ast.code`

	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, q, args...)
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
	db, err := r.db()
	if err != nil {
		return err
	}
	accountID, assetID, err := resolveLimitAxes(ctx, db, limit.Account, limit.Asset)
	if err != nil {
		return err
	}
	return putLimitRow(ctx, db, "limit_rate",
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
	db, err := r.db()
	if err != nil {
		return err
	}
	accountID, assetID, err := resolveLimitAxes(ctx, db, account, asset)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
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
LEFT JOIN account a   ON a.id   = los.account_id
LEFT JOIN asset   ast ON ast.id = los.asset_id`
	args := make([]any, 0, 1)
	if account != "" {
		q += ` WHERE a.code = ?`
		args = append(args, account.String())
	}
	q += ` ORDER BY los.scope, a.code, ast.code`

	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, q, args...)
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
	db, err := r.db()
	if err != nil {
		return err
	}
	accountID, assetID, err := resolveLimitAxes(ctx, db, limit.Account, limit.Asset)
	if err != nil {
		return err
	}
	return putLimitRow(ctx, db, "limit_order_size",
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
	db, err := r.db()
	if err != nil {
		return err
	}
	accountID, assetID, err := resolveLimitAxes(ctx, db, account, asset)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
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

// --- SpotFunds P&L-bounds barriers (limit_spot_funds_pnl_bound) -------------

// ListSpotFundsPnlBoundsLimits returns every SpotFunds self-computed
// P&L-bounds barrier. When account is non-empty only account-scoped barriers
// whose account_id matches are returned.
func (r *realmStore) ListSpotFundsPnlBoundsLimits(
	ctx context.Context, account domain.AccountID,
) ([]domain.LimitSpotFundsPnlBounds, error) {
	q := `
SELECT lsfpb.scope, a.code, g.code, ac.code,
       lsfpb.lower_bound, lsfpb.upper_bound, lsfpb.initial_pnl
FROM limit_spot_funds_pnl_bound lsfpb
LEFT JOIN account       a  ON a.id  = lsfpb.account_id
LEFT JOIN account_group g  ON g.id  = lsfpb.account_group_id
LEFT JOIN asset         ac ON ac.id = lsfpb.account_currency_asset_id`
	args := make([]any, 0, 1)
	if account != "" {
		q += ` WHERE a.code = ?`
		args = append(args, account.String())
	}
	q += ` ORDER BY lsfpb.scope, a.code, g.code, ac.code`

	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list spot funds pnl bounds limits: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.LimitSpotFundsPnlBounds, 0)
	for rows.Next() {
		l, err := scanSpotFundsPnlBoundsLimit(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"store: iterate spot funds pnl bounds limits: %w", err,
		)
	}
	return result, nil
}

// PutSpotFundsPnlBoundsLimit upserts one SpotFunds self-computed P&L-bounds
// barrier keyed by its composite.
func (r *realmStore) PutSpotFundsPnlBoundsLimit(
	ctx context.Context, limit domain.LimitSpotFundsPnlBounds,
) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	accountID, groupID, accountCurrencyID, err := resolveSpotFundsPnlBoundsAxes(
		ctx, db, limit.Account, limit.AccountGroup, limit.AccountCurrency,
	)
	if err != nil {
		return err
	}
	return putSpotFundsPnlBoundsRow(
		ctx, db, limit.Scope, accountID, groupID, accountCurrencyID,
		func(ctx context.Context, exec sqlExecer) error {
			_, err := exec.ExecContext(
				ctx,
				`INSERT INTO limit_spot_funds_pnl_bound
				 (scope, account_id, account_group_id, account_currency_asset_id,
				  lower_bound, upper_bound, initial_pnl)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				limit.Scope, accountID, groupID, accountCurrencyID,
				nullableString(limit.LowerBound),
				nullableString(limit.UpperBound),
				nullableString(limit.InitialPnl),
			)
			return err
		},
	)
}

// DeleteSpotFundsPnlBoundsLimit removes the SpotFunds self-computed P&L-bounds
// barrier with the given composite. Returns domain.ErrNotFound when absent.
func (r *realmStore) DeleteSpotFundsPnlBoundsLimit(
	ctx context.Context,
	scope domain.LimitScope,
	account domain.AccountID,
	accountGroup string,
	accountCurrency string,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	accountID, groupID, accountCurrencyID, err := resolveSpotFundsPnlBoundsAxes(
		ctx, db, account, accountGroup, accountCurrency,
	)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
		ctx,
		`DELETE FROM limit_spot_funds_pnl_bound
		 WHERE scope = ?
		   AND account_id IS ?
		   AND account_group_id IS ?
		   AND account_currency_asset_id = ?`,
		scope, accountID, groupID, accountCurrencyID,
	)
	if err != nil {
		return fmt.Errorf("store: delete spot funds pnl bounds limit: %w", err)
	}
	return notFoundIfNoRows(res, "spot funds pnl bounds limit", scope)
}

func scanSpotFundsPnlBoundsLimit(
	rows *sql.Rows,
) (domain.LimitSpotFundsPnlBounds, error) {
	var (
		scope                         string
		accountCode, accountGroupCode sql.NullString
		accountCurrencyCode           sql.NullString
		lowerBound, upperBound        sql.NullString
		initialPnl                    sql.NullString
	)
	if err := rows.Scan(
		&scope, &accountCode, &accountGroupCode, &accountCurrencyCode,
		&lowerBound, &upperBound, &initialPnl,
	); err != nil {
		return domain.LimitSpotFundsPnlBounds{}, fmt.Errorf(
			"store: scan spot funds pnl bounds limit: %w", err,
		)
	}
	return domain.LimitSpotFundsPnlBounds{
		Scope:           scope,
		Account:         domain.AccountID(accountCode.String),
		AccountGroup:    accountGroupCode.String,
		AccountCurrency: accountCurrencyCode.String,
		LowerBound:      lowerBound.String,
		UpperBound:      upperBound.String,
		InitialPnl:      initialPnl.String,
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

func putSpotFundsPnlBoundsRow(
	ctx context.Context,
	db *sql.DB,
	scope string,
	accountID, groupID sql.NullInt64,
	accountCurrencyID int64,
	insertFn func(context.Context, sqlExecer) error,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin put spot funds pnl bounds limit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM limit_spot_funds_pnl_bound
		 WHERE scope = ?
		   AND account_id IS ?
		   AND account_group_id IS ?
		   AND account_currency_asset_id = ?`,
		scope, accountID, groupID, accountCurrencyID,
	); err != nil {
		return fmt.Errorf("store: delete old spot funds pnl bounds limit: %w", err)
	}
	if err := insertFn(ctx, tx); err != nil {
		return fmt.Errorf("store: insert spot funds pnl bounds limit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit put spot funds pnl bounds limit: %w", err)
	}
	return nil
}

func resolveSpotFundsPnlBoundsAxes(
	ctx context.Context,
	q sqlQueryer,
	account domain.AccountID,
	accountGroup string,
	accountCurrency string,
) (sql.NullInt64, sql.NullInt64, int64, error) {
	var accountID, groupID sql.NullInt64
	if account != "" {
		id, err := resolveAccountID(ctx, q, account)
		if err != nil {
			return sql.NullInt64{}, sql.NullInt64{}, 0, err
		}
		accountID = sql.NullInt64{Int64: id, Valid: true}
	}
	if accountGroup != "" {
		id, err := resolveGroupID(ctx, q, accountGroup)
		if err != nil {
			return sql.NullInt64{}, sql.NullInt64{}, 0, err
		}
		groupID = sql.NullInt64{Int64: id, Valid: true}
	}
	accountCurrencyID, err := resolveAssetID(ctx, q, accountCurrency)
	if err != nil {
		return sql.NullInt64{}, sql.NullInt64{}, 0, err
	}
	return accountID, groupID, accountCurrencyID, nil
}
