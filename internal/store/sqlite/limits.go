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

// Limits group of the SQLite store: one typed table per engine policy
// (limit_rate, limit_order_size, limit_spot_funds_pnl_bound). The Officer
// Spot Funds limit-mode meta-policy is a realm setting (settings.go), listed
// with the barriers by the unified policy list.
// Each row is keyed by its natural composite; there is no external id and no
// surrogate id outward. Account, asset, and account group are present only for
// policies and scopes that carry them; an empty code means the NULL axis
// (scope-wide). Domain Validate() enforces allowed scopes and
// consistent axis presence before persist; the store applies that rule by
// calling Validate() on every Put. Unknown dictionary codes on a non-NULL axis
// are an error wrapping domain.ErrInvalid.

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

// --- Unified policy list (the barrier tables and the meta-policy setting) ----

// policyUnion projects the typed barrier tables and the Officer meta-policy
// setting onto one common shape so they can be sorted, filtered, and paged
// together. Columns absent for a given kind are NULL; stable_id is the
// kind-prefixed surrogate id, unique across the union, used as the
// deterministic sort tiebreak. The dictionary codes come from the same LEFT
// JOINs the per-kind reads use. The Spot Funds limit-mode meta-policy is one
// global row with no axis, present only while its setting holds a value.
const policyUnion = `
SELECT 'rate_limit' AS kind, lr.scope AS scope,
       a.code AS account_code, NULL AS account_group_code,
       ast.code AS asset_code, NULL AS currency_code,
       lr.max_orders AS max_orders, lr.window AS window,
       NULL AS max_quantity, NULL AS max_notional,
       NULL AS max_position,
	       NULL AS lower_bound, NULL AS upper_bound,
       NULL AS mode,
       'rate_limit:' || lr.id AS stable_id
FROM limit_rate lr
LEFT JOIN account a   ON a.id   = lr.account_id
LEFT JOIN asset   ast ON ast.id = lr.asset_id
UNION ALL
SELECT 'order_size_limit' AS kind, los.scope AS scope,
       a.code AS account_code, NULL AS account_group_code,
       ast.code AS asset_code, NULL AS currency_code,
       NULL AS max_orders, NULL AS window,
       los.max_quantity AS max_quantity, los.max_notional AS max_notional,
       NULL AS max_position,
	       NULL AS lower_bound, NULL AS upper_bound,
       los.mode AS mode,
       'order_size_limit:' || los.id AS stable_id
FROM limit_order_size los
LEFT JOIN account a   ON a.id   = los.account_id
LEFT JOIN asset   ast ON ast.id = los.asset_id
UNION ALL
SELECT 'position_limit' AS kind, lp.scope AS scope,
       a.code AS account_code, NULL AS account_group_code,
       ast.code AS asset_code, NULL AS currency_code,
       NULL AS max_orders, NULL AS window,
       NULL AS max_quantity, NULL AS max_notional, lp.max_position AS max_position,
       NULL AS lower_bound, NULL AS upper_bound, NULL AS mode,
       'position_limit:' || lp.id AS stable_id
FROM limit_position lp
JOIN account a ON a.id = lp.account_id
JOIN asset ast ON ast.id = lp.asset_id
UNION ALL
SELECT CASE lsfpb.axis
         WHEN 'realized'   THEN 'spot_funds_pnl_bounds_kill_switch'
         WHEN 'unrealized' THEN 'spot_funds_unrealized_pnl_bounds_kill_switch'
         WHEN 'total'      THEN 'spot_funds_total_pnl_bounds_kill_switch'
       END AS kind,
       lsfpb.scope AS scope,
       a.code AS account_code, g.code AS account_group_code,
       NULL AS asset_code, c.code AS currency_code,
       NULL AS max_orders, NULL AS window,
       NULL AS max_quantity, NULL AS max_notional,
       NULL AS max_position,
	       lsfpb.lower_bound AS lower_bound, lsfpb.upper_bound AS upper_bound,
       NULL AS mode,
       'spot_funds_pnl_bounds_kill_switch:' || lsfpb.id AS stable_id
FROM limit_spot_funds_pnl_bound lsfpb
LEFT JOIN account       a  ON a.id  = lsfpb.account_id
LEFT JOIN account_group g  ON g.id  = lsfpb.account_group_id
JOIN asset               c  ON c.id  = lsfpb.currency_asset_id
UNION ALL
SELECT 'spot_funds_limit_mode' AS kind, 'global' AS scope,
       NULL AS account_code, NULL AS account_group_code,
       NULL AS asset_code, NULL AS currency_code,
       NULL AS max_orders, NULL AS window,
       NULL AS max_quantity, NULL AS max_notional,
       NULL AS max_position,
       NULL AS lower_bound, NULL AS upper_bound,
       COALESCE((SELECT value FROM setting WHERE key = '` + domain.SettingKeySpotFundsLimitMode + `'), 'enforce') AS mode,
       'spot_funds_limit_mode:global' AS stable_id
`

// ListPolicyRows returns the typed barrier tables and the meta-policy setting
// flattened into one sorted, paged list with the pre-paging total. It applies filters in SQL and
// reconstructs each row's typed value from the projected columns.
func (r *realmStore) ListPolicyRows(
	ctx context.Context, filter fwstore.PolicyListFilter,
) (value fwstore.PolicyListPage, resultErr error) {
	where, args, err := policyListWhere(filter)
	if err != nil {
		return fwstore.PolicyListPage{}, err
	}
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
		       asset_code, currency_code,
	       max_orders, window, max_quantity, max_notional, max_position,
	       lower_bound, upper_bound, mode` + from + policyListOrderBy(filter.Sort)
	if filter.Page.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		queryArgs = append(queryArgs, filter.Page.Limit, max(filter.Page.Offset, 0))
	}

	rows, err := db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return fwstore.PolicyListPage{}, fmt.Errorf("store: list policy rows: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()

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

func policyListWhere(filter fwstore.PolicyListFilter) (string, []any, error) {
	clauses := make([]string, 0, 5)
	args := make([]any, 0, 5)
	if filter.Restriction.Selection != nil {
		appendPolicySelection(&clauses, &args, filter.Restriction)
	} else {
		appendAccountRestriction(
			&clauses, &args, "account_code", "account_code IS NULL", filter.Restriction,
		)
		// An account-group barrier is kept only when the restricted account is a
		// member of its group. The domain forbids a group on an account-axis
		// barrier, so this narrows the unowned rows alone.
		if filter.Restriction.Restricted() {
			clauses = append(clauses, `(account_group_code IS NULL OR account_group_code IN (
			SELECT g.code FROM account m JOIN account_group g ON g.id = m.group_id
			WHERE m.code = ?))`)
			args = append(args, filter.Restriction.Account.String())
		}
	}
	appendMatcher(&clauses, &args, "account_code", filter.Account)
	appendMatcher(&clauses, &args, "account_group_code", filter.AccountGroup)
	appendMatcher(&clauses, &args, "COALESCE(asset_code, currency_code)", filter.Asset)
	if filter.Scope != "" {
		switch filter.Scope {
		case domain.ScopeBroker, domain.ScopeGlobal, domain.ScopeAsset,
			domain.ScopeAccount, domain.ScopeAccountGroup, domain.ScopeAccountAsset,
			domain.ScopeUnderlyingAsset, domain.ScopeSettlementAsset,
			domain.ScopeAccountUnderlyingAsset,
			domain.ScopeAccountSettlementAsset:
		default:
			return "", nil, fmt.Errorf(
				"invalid policy scope %q: %w", filter.Scope, domain.ErrInvalid,
			)
		}
		clauses = append(clauses, "scope = ?")
		args = append(args, filter.Scope)
	}
	if filter.Kind != nil {
		clauses = append(clauses, "kind = ?")
		args = append(args, string(*filter.Kind))
	}
	if filter.Mode != "" {
		clauses = append(clauses, "kind = 'order_size_limit' AND mode = ?")
		args = append(args, filter.Mode)
	}
	if len(clauses) == 0 {
		return "", args, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

func policyListOrderBy(sort fwstore.SortSpec) string {
	columns := map[string]string{
		"account":      "account_code",
		"accountGroup": "account_group_code",
		"asset":        "COALESCE(asset_code, currency_code)",
		"lowerBound":   "lower_bound COLLATE DECIMAL",
		"maxNotional":  "max_notional COLLATE DECIMAL",
		"maxOrders":    "max_orders",
		"maxQuantity":  "max_quantity COLLATE DECIMAL",
		"maxPosition":  "max_position COLLATE DECIMAL",
		"policy":       "kind",
		"scope":        "scope",
		"upperBound":   "upper_bound COLLATE DECIMAL",
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
	// The (kind, scope, account, asset, mode) composite is unique across the union, so
	// it is a deterministic tiebreak that does not depend on the per-table
	// surrogate id. stable_id stays the final tiebreak for the degenerate case
	// where the composite repeats.
	tie := "kind " + tieDirection + ", scope " + tieDirection +
		", account_code " + tieDirection +
		", account_group_code " + tieDirection +
		", COALESCE(asset_code, currency_code) " + tieDirection +
		", mode " + tieDirection +
		", stable_id " + tieDirection
	return " ORDER BY " + column + " " + direction + ", " + tie
}

func scanPolicyRow(rows *sql.Rows) (fwstore.PolicyListRow, error) {
	var (
		kind                          string
		scope                         domain.LimitScope
		accountCode, accountGroupCode sql.NullString
		assetCode, currencyCode       sql.NullString
		maxOrders                     sql.NullInt64
		windowStr                     sql.NullString
		maxQuantity                   sql.NullString
		maxNotional                   sql.NullString
		maxPosition                   sql.NullString
		lowerBound, upperBound        sql.NullString
		mode                          sql.NullString
	)
	if err := rows.Scan(
		&kind, &scope, &accountCode, &accountGroupCode,
		&assetCode, &currencyCode,
		&maxOrders, &windowStr, &maxQuantity, &maxNotional, &maxPosition,
		&lowerBound, &upperBound, &mode,
	); err != nil {
		return fwstore.PolicyListRow{}, fmt.Errorf("store: scan policy row: %w", err)
	}
	account := domain.AccountID(accountCode.String)
	row := fwstore.PolicyListRow{
		Kind:         fwstore.PolicyKind(kind),
		Scope:        scope,
		Account:      account,
		AccountGroup: accountGroupCode.String,
		Asset:        assetCode.String,
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
			Mode:        domain.RuleMode(mode.String),
			Scope:       scope,
			Account:     account,
			Asset:       assetCode.String,
			MaxQuantity: maxQuantity.String,
			MaxNotional: maxNotional.String,
		}
	case fwstore.PolicyKindPosition:
		row.Position = &domain.LimitPosition{
			Scope: scope, Account: account, Asset: assetCode.String,
			MaxPosition: maxPosition.String,
		}
	case fwstore.PolicyKindSpotFundsPnlBounds,
		fwstore.PolicyKindSpotFundsUnrealizedPnlBounds,
		fwstore.PolicyKindSpotFundsTotalPnlBounds:
		axis, ok := domain.SpotFundsPnlAxisOf(kind)
		if !ok {
			return fwstore.PolicyListRow{}, fmt.Errorf(
				"store: policy kind %q has no spot funds pnl axis: %w",
				kind, domain.ErrInvalid,
			)
		}
		row.SpotFundsPnlBounds = &domain.LimitSpotFundsPnlBounds{
			Axis:         axis,
			Scope:        scope,
			Account:      account,
			AccountGroup: accountGroupCode.String,
			Currency:     currencyCode.String,
			LowerBound:   lowerBound.String,
			UpperBound:   upperBound.String,
		}
	case fwstore.PolicyKindSpotFundsLimitMode:
		parsed, err := domain.ParseSpotFundsLimitMode(mode.String)
		if err != nil {
			// An unknown stored value is corruption, not caller input: discard
			// the parse error's validation chain.
			return fwstore.PolicyListRow{}, fmt.Errorf(
				"store: setting %q holds invalid stored value %q",
				domain.SettingKeySpotFundsLimitMode, mode.String,
			)
		}
		row.SpotFundsLimitMode = &domain.LimitSpotFundsLimitMode{
			Scope: scope,
			Mode:  parsed,
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
) (value []domain.LimitRate, resultErr error) {
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
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()

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
	key, err := limit.Key()
	if err != nil {
		return err
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	ids, err := resolveLimitKeyIDs(ctx, db, key)
	if err != nil {
		return err
	}
	predicate, err := limitKeyWhere(key, ids)
	if err != nil {
		return err
	}
	accountID, assetID := ids.account, ids.asset
	return putLimitRow(ctx, db, predicate,
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

// DeleteLimit removes the barrier addressed by key from its policy's table.
// Returns domain.ErrNotFound when absent.
func (r *realmStore) DeleteLimit(ctx context.Context, key domain.LimitKey) error {
	// Reject keys without a barrier table before even opening the database.
	if _, err := limitTableForPolicy(key.Policy()); err != nil {
		return err
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	ids, err := resolveLimitKeyIDs(ctx, db, key)
	if err != nil {
		return err
	}
	predicate, err := limitKeyWhere(key, ids)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx,
		`DELETE FROM `+predicate.table+` WHERE `+predicate.where,
		predicate.args...,
	)
	if err != nil {
		return fmt.Errorf("store: delete %s: %w", predicate.noun, err)
	}
	return notFoundIfNoRows(res, predicate.noun, predicate.subject)
}

func scanRateLimit(rows *sql.Rows) (domain.LimitRate, error) {
	var (
		scope                  domain.LimitScope
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
) (value []domain.LimitOrderSize, resultErr error) {
	q := `
SELECT los.scope, a.code, ast.code, los.max_quantity, los.max_notional, los.mode
FROM limit_order_size los
LEFT JOIN account a   ON a.id   = los.account_id
LEFT JOIN asset   ast ON ast.id = los.asset_id`
	args := make([]any, 0, 1)
	if account != "" {
		q += ` WHERE a.code = ?`
		args = append(args, account.String())
	}
	q += ` ORDER BY los.scope, a.code, ast.code, los.mode`

	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list order size limits: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()

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

// PutOrderSizeLimit upserts one barrier keyed by (scope, account, asset, mode).
// Uses the same DELETE-then-INSERT pattern as PutRateLimit to handle nullable
// axis columns correctly in SQLite's UNIQUE constraint.
func (r *realmStore) PutOrderSizeLimit(ctx context.Context, limit domain.LimitOrderSize) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	key, err := limit.Key()
	if err != nil {
		return err
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	ids, err := resolveLimitKeyIDs(ctx, db, key)
	if err != nil {
		return err
	}
	predicate, err := limitKeyWhere(key, ids)
	if err != nil {
		return err
	}
	accountID, assetID := ids.account, ids.asset
	return putLimitRow(ctx, db, predicate,
		func(ctx context.Context, exec sqlExecer) error {
			_, err := exec.ExecContext(
				ctx,
				`INSERT INTO limit_order_size
				 (scope, account_id, asset_id, max_quantity, max_notional, mode)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				limit.Scope, accountID, assetID,
				nullableString(limit.MaxQuantity),
				nullableString(limit.MaxNotional),
				limit.Mode,
			)
			return err
		},
	)
}

func scanOrderSizeLimit(rows *sql.Rows) (domain.LimitOrderSize, error) {
	var (
		scope                    domain.LimitScope
		accountCode, assetCode   sql.NullString
		maxQuantity, maxNotional sql.NullString
		mode                     domain.RuleMode
	)
	if err := rows.Scan(
		&scope, &accountCode, &assetCode, &maxQuantity, &maxNotional, &mode,
	); err != nil {
		return domain.LimitOrderSize{}, fmt.Errorf("store: scan order size limit: %w", err)
	}
	return domain.LimitOrderSize{
		Mode:        mode,
		Scope:       scope,
		Account:     domain.AccountID(accountCode.String),
		Asset:       assetCode.String,
		MaxQuantity: maxQuantity.String,
		MaxNotional: maxNotional.String,
	}, nil
}

// ListPositionLimits returns the position ceilings, optionally narrowed to an account.
func (r *realmStore) ListPositionLimits(
	ctx context.Context, account domain.AccountID,
) (value []domain.LimitPosition, resultErr error) {
	q := `SELECT lp.scope, a.code, ast.code, lp.max_position
FROM limit_position lp
JOIN account a ON a.id = lp.account_id
JOIN asset ast ON ast.id = lp.asset_id`
	args := make([]any, 0, 1)
	if account != "" {
		q += ` WHERE a.code = ?`
		args = append(args, account.String())
	}
	q += ` ORDER BY a.code, ast.code`
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list position limits: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	result := make([]domain.LimitPosition, 0)
	for rows.Next() {
		var limit domain.LimitPosition
		if err := rows.Scan(&limit.Scope, &limit.Account, &limit.Asset, &limit.MaxPosition); err != nil {
			return nil, fmt.Errorf("store: scan position limit: %w", err)
		}
		result = append(result, limit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate position limits: %w", err)
	}
	return result, nil
}

// PutPositionLimit replaces the ceiling for the account and asset atomically.
func (r *realmStore) PutPositionLimit(ctx context.Context, limit domain.LimitPosition) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	key, err := limit.Key()
	if err != nil {
		return err
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	ids, err := resolveLimitKeyIDs(ctx, db, key)
	if err != nil {
		return err
	}
	predicate, err := limitKeyWhere(key, ids)
	if err != nil {
		return err
	}
	accountID, assetID := ids.account, ids.asset
	return putLimitRow(ctx, db, predicate,
		func(ctx context.Context, exec sqlExecer) error {
			_, err := exec.ExecContext(
				ctx,
				`INSERT INTO limit_position
				 (scope, account_id, asset_id, max_position)
				 VALUES (?, ?, ?, ?)`,
				limit.Scope, accountID, assetID, limit.MaxPosition,
			)
			return err
		},
	)
}

// --- SpotFunds P&L-bounds barriers (limit_spot_funds_pnl_bound) -------------

// ListSpotFundsPnlBoundsLimits returns every SpotFunds self-computed
// P&L-bounds barrier of every axis. When account is non-empty only
// account-scoped barriers whose account_id matches are returned.
func (r *realmStore) ListSpotFundsPnlBoundsLimits(
	ctx context.Context, account domain.AccountID,
) (value []domain.LimitSpotFundsPnlBounds, resultErr error) {
	q := `
	SELECT lsfpb.axis, lsfpb.scope, a.code, g.code, c.code,
	       lsfpb.lower_bound, lsfpb.upper_bound
FROM limit_spot_funds_pnl_bound lsfpb
LEFT JOIN account       a  ON a.id  = lsfpb.account_id
LEFT JOIN account_group g  ON g.id  = lsfpb.account_group_id
JOIN asset               c  ON c.id  = lsfpb.currency_asset_id`
	args := make([]any, 0, 1)
	if account != "" {
		q += ` WHERE a.code = ?`
		args = append(args, account.String())
	}
	q += ` ORDER BY lsfpb.axis, lsfpb.scope, a.code, g.code`

	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list spot funds pnl bounds limits: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()

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
	key, err := limit.Key()
	if err != nil {
		return err
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	ids, err := resolveLimitKeyIDs(ctx, db, key)
	if err != nil {
		return err
	}
	currencyID, err := resolveAssetID(ctx, db, limit.Currency)
	if err != nil {
		return err
	}
	predicate, err := limitKeyWhere(key, ids)
	if err != nil {
		return err
	}
	accountID, groupID := ids.account, ids.group
	return putLimitRow(
		ctx, db, predicate,
		func(ctx context.Context, exec sqlExecer) error {
			_, err := exec.ExecContext(
				ctx,
				`INSERT INTO limit_spot_funds_pnl_bound
					 (axis, scope, account_id, account_group_id, currency_asset_id, lower_bound, upper_bound)
					 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				limit.Axis, limit.Scope, accountID, groupID, currencyID,
				nullableString(limit.LowerBound),
				nullableString(limit.UpperBound),
			)
			return err
		},
	)
}

func scanSpotFundsPnlBoundsLimit(
	rows *sql.Rows,
) (domain.LimitSpotFundsPnlBounds, error) {
	var (
		axis                          domain.SpotFundsPnlAxis
		scope                         domain.LimitScope
		accountCode, accountGroupCode sql.NullString
		currencyCode                  string
		lowerBound, upperBound        sql.NullString
	)
	if err := rows.Scan(
		&axis, &scope, &accountCode, &accountGroupCode,
		&currencyCode,
		&lowerBound, &upperBound,
	); err != nil {
		return domain.LimitSpotFundsPnlBounds{}, fmt.Errorf(
			"store: scan spot funds pnl bounds limit: %w", err,
		)
	}
	return domain.LimitSpotFundsPnlBounds{
		Axis:         axis,
		Scope:        scope,
		Account:      domain.AccountID(accountCode.String),
		AccountGroup: accountGroupCode.String,
		Currency:     currencyCode,
		LowerBound:   lowerBound.String,
		UpperBound:   upperBound.String,
	}, nil
}

// --- Limits-group shared helpers --------------------------------------------

type limitKeyIDs struct {
	account, asset, group sql.NullInt64
}

type limitKeyPredicate struct {
	table, where, noun, subject string
	args                        []any
}

type limitTable struct {
	name, noun string
	hasMode    bool
	hasPnlAxis bool
}

func limitTableForPolicy(policy string) (limitTable, error) {
	switch policy {
	case domain.PolicyRateLimit:
		return limitTable{name: "limit_rate", noun: "rate limit"}, nil
	case domain.PolicyOrderSizeLimit:
		return limitTable{name: "limit_order_size", noun: "order size limit", hasMode: true}, nil
	case domain.PolicyPositionLimit:
		return limitTable{name: "limit_position", noun: "position limit"}, nil
	case domain.PolicySpotFundsPnlBoundsKillSwitch,
		domain.PolicySpotFundsUnrealizedPnlBoundsKillSwitch,
		domain.PolicySpotFundsTotalPnlBoundsKillSwitch:
		return limitTable{name: "limit_spot_funds_pnl_bound", noun: "spot funds pnl bounds limit", hasPnlAxis: true}, nil
	}
	return limitTable{}, fmt.Errorf("store: policy %q has no limit barrier table: %w", policy, domain.ErrInvalid)
}

// limitKeyWhere chooses the barrier table and its complete key predicate.
// Keys without a barrier table are invalid, including the zero key.
func limitKeyWhere(key domain.LimitKey, ids limitKeyIDs) (limitKeyPredicate, error) {
	table, err := limitTableForPolicy(key.Policy())
	if err != nil {
		return limitKeyPredicate{}, err
	}
	predicate := limitKeyPredicate{
		table: table.name, noun: table.noun, subject: string(key.Scope()),
	}
	if table.hasPnlAxis {
		axis, ok := domain.SpotFundsPnlAxisOf(key.Policy())
		if !ok {
			return limitKeyPredicate{}, fmt.Errorf("store: policy %q has no spot funds pnl axis: %w", key.Policy(), domain.ErrInvalid)
		}
		predicate.where = `axis = ? AND scope = ? AND account_id IS ? AND account_group_id IS ?`
		predicate.args = []any{axis, key.Scope(), ids.account, ids.group}
		return predicate, nil
	}
	predicate.where = `scope = ? AND account_id IS ? AND asset_id IS ?`
	predicate.args = []any{key.Scope(), ids.account, ids.asset}
	if table.hasMode {
		predicate.where += ` AND mode = ?`
		predicate.args = append(predicate.args, key.Mode())
	}
	if key.Policy() == domain.PolicyPositionLimit {
		predicate.subject = key.Account().String() + ":" + key.Asset()
	}
	return predicate, nil
}

// putLimitRow upserts one limit row in table using DELETE-then-INSERT inside a
// transaction. This is required because SQLite treats NULL as distinct in UNIQUE
// constraints: two rows with the same (scope, NULL, NULL) are considered
// different by the ON CONFLICT clause, so the standard upsert idiom silently
// inserts a duplicate. The delete-first approach is safe under SQLite's
// single-writer serialisation. The complete key predicate comes from
// limitKeyWhere. insertFn executes the INSERT through the provided execer after
// the prior row (if any) is deleted.
func putLimitRow(
	ctx context.Context,
	db *sql.DB,
	predicate limitKeyPredicate,
	insertFn func(context.Context, sqlExecer) error,
) (err error) {
	table := predicate.table
	tx, err := beginTx(ctx, db, nil)
	if err != nil {
		return fmt.Errorf("store: begin put limit %s: %w", table, err)
	}
	defer rollbackTransaction(ctx, &err, tx)

	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM `+table+` WHERE `+predicate.where,
		predicate.args...,
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

// resolveLimitKeyIDs resolves the key's non-empty codes to nullable surrogate
// ids. A non-empty unknown code wraps domain.ErrInvalid.
func resolveLimitKeyIDs(
	ctx context.Context, q sqlQueryer, key domain.LimitKey,
) (limitKeyIDs, error) {
	accountID, groupID, err := resolveSubjectIDs(ctx, q, key.Account(), key.AccountGroup())
	if err != nil {
		return limitKeyIDs{}, err
	}
	ids := limitKeyIDs{account: accountID, group: groupID}
	if key.Asset() != "" {
		id, err := resolveAssetID(ctx, q, key.Asset())
		if err != nil {
			return limitKeyIDs{}, err
		}
		ids.asset = sql.NullInt64{Int64: id, Valid: true}
	}
	return ids, nil
}

func resolveSubjectIDs(
	ctx context.Context,
	q sqlQueryer,
	account domain.AccountID,
	accountGroup string,
) (sql.NullInt64, sql.NullInt64, error) {
	var accountID, groupID sql.NullInt64
	if account != "" {
		id, err := resolveAccountID(ctx, q, account)
		if err != nil {
			return sql.NullInt64{}, sql.NullInt64{}, err
		}
		accountID = sql.NullInt64{Int64: id, Valid: true}
	}
	if accountGroup != "" {
		id, err := resolveGroupID(ctx, q, accountGroup)
		if err != nil {
			return sql.NullInt64{}, sql.NullInt64{}, err
		}
		groupID = sql.NullInt64{Int64: id, Valid: true}
	}
	return accountID, groupID, nil
}
