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

// Group 1 of the SQLite store: the dictionary entities the rest of the schema
// references - assets, principals, account groups and accounts. Dictionaries are
// addressed by their immutable code; the surrogate key never crosses the
// interface boundary. Accounts and groups carry a connector-assigned engine id,
// returned (populated) on the create path so the engine layer can build its
// tree, and an account's group link is cleared (SET NULL), not cascaded, when its
// group is deleted.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"go.openpit.dev/officer/internal/domain"
)

// --- Assets -----------------------------------------------------------------

// CreateAsset persists a new asset dictionary row.
func (r *realmStore) CreateAsset(ctx context.Context, asset domain.Asset) error {
	_, err := r.db().ExecContext(
		ctx,
		`INSERT INTO assets (code, title, asset_class) VALUES (?, ?, ?)`,
		asset.Code, asset.Title, nullableString(asset.AssetClass),
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return fmt.Errorf("asset %q: %w", asset.Code, domain.ErrAlreadyExists)
		}
		return fmt.Errorf("store: create asset: %w", err)
	}
	return nil
}

// GetAsset returns the asset with the given code.
func (r *realmStore) GetAsset(
	ctx context.Context, code string,
) (domain.Asset, bool, error) {
	row := r.db().QueryRowContext(
		ctx, `SELECT code, title, asset_class FROM assets WHERE code = ?`, code,
	)
	asset, err := scanAssetRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Asset{}, false, nil
	}
	if err != nil {
		return domain.Asset{}, false, fmt.Errorf("store: get asset: %w", err)
	}
	return asset, true, nil
}

// ListAssets returns every asset, ordered by code.
func (r *realmStore) ListAssets(ctx context.Context) ([]domain.Asset, error) {
	rows, err := r.db().QueryContext(
		ctx, `SELECT code, title, asset_class FROM assets ORDER BY code`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list assets: %w", err)
	}
	defer func() { _ = rows.Close() }()

	assets := make([]domain.Asset, 0)
	for rows.Next() {
		asset, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate assets: %w", err)
	}
	return assets, nil
}

// UpdateAsset replaces the mutable fields of the identified asset.
func (r *realmStore) UpdateAsset(ctx context.Context, asset domain.Asset) error {
	res, err := r.db().ExecContext(
		ctx,
		`UPDATE assets SET title = ?, asset_class = ? WHERE code = ?`,
		asset.Title, nullableString(asset.AssetClass), asset.Code,
	)
	if err != nil {
		return fmt.Errorf("store: update asset: %w", err)
	}
	return notFoundIfNoRows(res, "asset", asset.Code)
}

// DeleteAsset removes the asset and, when forced, cascades its dependent rows.
func (r *realmStore) DeleteAsset(ctx context.Context, code string, force bool) error {
	tx, err := r.db().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin delete asset: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	assetID, err := resolveAssetID(ctx, tx, code)
	if err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			return fmt.Errorf("asset %q: %w", code, domain.ErrNotFound)
		}
		return err
	}
	if !force {
		deps, err := assetDependents(ctx, tx, assetID)
		if err != nil {
			return err
		}
		if len(deps) > 0 {
			return domain.NewHasDependentsError(deps)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, assetID)
	if err != nil {
		return fmt.Errorf("store: delete asset: %w", err)
	}
	if err := notFoundIfNoRows(res, "asset", code); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit delete asset: %w", err)
	}
	return nil
}

func scanAsset(rows *sql.Rows) (domain.Asset, error) {
	var asset domain.Asset
	var assetClass sql.NullString
	if err := rows.Scan(&asset.Code, &asset.Title, &assetClass); err != nil {
		return domain.Asset{}, fmt.Errorf("store: scan asset: %w", err)
	}
	asset.AssetClass = assetClass.String
	return asset, nil
}

func scanAssetRow(row *sql.Row) (domain.Asset, error) {
	var asset domain.Asset
	var assetClass sql.NullString
	if err := row.Scan(&asset.Code, &asset.Title, &assetClass); err != nil {
		return domain.Asset{}, err
	}
	asset.AssetClass = assetClass.String
	return asset, nil
}

// --- Principals -------------------------------------------------------------

// CreatePrincipal persists a new principal dictionary row.
func (r *realmStore) CreatePrincipal(
	ctx context.Context, principal domain.Principal,
) error {
	_, err := r.db().ExecContext(
		ctx,
		`INSERT INTO principals (code, title) VALUES (?, ?)`,
		principal.Code, principal.Title,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return fmt.Errorf("principal %q: %w", principal.Code, domain.ErrAlreadyExists)
		}
		return fmt.Errorf("store: create principal: %w", err)
	}
	return nil
}

// GetPrincipal returns the principal with the given code.
func (r *realmStore) GetPrincipal(
	ctx context.Context, code string,
) (domain.Principal, bool, error) {
	var principal domain.Principal
	err := r.db().QueryRowContext(
		ctx, `SELECT code, title FROM principals WHERE code = ?`, code,
	).Scan(&principal.Code, &principal.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Principal{}, false, nil
	}
	if err != nil {
		return domain.Principal{}, false, fmt.Errorf("store: get principal: %w", err)
	}
	return principal, true, nil
}

// ListPrincipals returns every principal, ordered by code.
func (r *realmStore) ListPrincipals(ctx context.Context) ([]domain.Principal, error) {
	rows, err := r.db().QueryContext(
		ctx, `SELECT code, title FROM principals ORDER BY code`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list principals: %w", err)
	}
	defer func() { _ = rows.Close() }()

	principals := make([]domain.Principal, 0)
	for rows.Next() {
		var principal domain.Principal
		if err := rows.Scan(&principal.Code, &principal.Title); err != nil {
			return nil, fmt.Errorf("store: scan principal: %w", err)
		}
		principals = append(principals, principal)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate principals: %w", err)
	}
	return principals, nil
}

// UpdatePrincipal replaces the mutable title of the identified principal.
func (r *realmStore) UpdatePrincipal(
	ctx context.Context, principal domain.Principal,
) error {
	res, err := r.db().ExecContext(
		ctx, `UPDATE principals SET title = ? WHERE code = ?`,
		principal.Title, principal.Code,
	)
	if err != nil {
		return fmt.Errorf("store: update principal: %w", err)
	}
	return notFoundIfNoRows(res, "principal", principal.Code)
}

// DeletePrincipal removes the principal; references to it are cleared.
func (r *realmStore) DeletePrincipal(ctx context.Context, code string) error {
	res, err := r.db().ExecContext(ctx, `DELETE FROM principals WHERE code = ?`, code)
	if err != nil {
		return fmt.Errorf("store: delete principal: %w", err)
	}
	return notFoundIfNoRows(res, "principal", code)
}

// --- Account groups ---------------------------------------------------------

// CreateGroup persists a new account group, assigning a collision-free engine
// group id inside the insert transaction, and returns it with EngineGroupID
// populated.
func (r *realmStore) CreateGroup(
	ctx context.Context, group domain.AccountGroup,
) (domain.AccountGroup, error) {
	tx, err := r.db().BeginTx(ctx, nil)
	if err != nil {
		return domain.AccountGroup{}, fmt.Errorf("store: begin create group: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	engineID, err := nextEngineGroupID(ctx, tx)
	if err != nil {
		return domain.AccountGroup{}, err
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO account_groups
		 (engine_group_id, code, title, notes, blocked, block_reason)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		int64(engineID.Uint32()), group.Code, group.Title,
		group.Notes, group.Blocked, group.BlockReason,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return domain.AccountGroup{},
				fmt.Errorf("group %q: %w", group.Code, domain.ErrAlreadyExists)
		}
		return domain.AccountGroup{}, fmt.Errorf("store: create group: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.AccountGroup{}, fmt.Errorf("store: commit create group: %w", err)
	}
	group.EngineGroupID = engineID
	return group, nil
}

// GetGroup returns the group with the given code.
func (r *realmStore) GetGroup(
	ctx context.Context, code string,
) (domain.AccountGroup, bool, error) {
	row := r.db().QueryRowContext(
		ctx,
		`SELECT engine_group_id, code, title, notes, blocked, block_reason
		 FROM account_groups WHERE code = ?`,
		code,
	)
	group, err := scanGroupRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AccountGroup{}, false, nil
	}
	if err != nil {
		return domain.AccountGroup{}, false, fmt.Errorf("store: get group: %w", err)
	}
	return group, true, nil
}

// ListGroups returns every group, ordered by code.
func (r *realmStore) ListGroups(ctx context.Context) ([]domain.AccountGroup, error) {
	rows, err := r.db().QueryContext(
		ctx,
		`SELECT engine_group_id, code, title, notes, blocked, block_reason
		 FROM account_groups ORDER BY code`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list groups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	groups := make([]domain.AccountGroup, 0)
	for rows.Next() {
		group, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate groups: %w", err)
	}
	return groups, nil
}

// SetGroupNotes replaces the notes of the identified group.
func (r *realmStore) SetGroupNotes(ctx context.Context, code, notes string) error {
	res, err := r.db().ExecContext(
		ctx, `UPDATE account_groups SET notes = ? WHERE code = ?`, notes, code,
	)
	if err != nil {
		return fmt.Errorf("store: set group notes: %w", err)
	}
	return notFoundIfNoRows(res, "group", code)
}

// SetGroupBlocked updates the blocked flag and block reason of the group.
func (r *realmStore) SetGroupBlocked(
	ctx context.Context, code string, blocked bool, reason string,
) error {
	res, err := r.db().ExecContext(
		ctx,
		`UPDATE account_groups SET blocked = ?, block_reason = ? WHERE code = ?`,
		blocked, reason, code,
	)
	if err != nil {
		return fmt.Errorf("store: set group blocked: %w", err)
	}
	return notFoundIfNoRows(res, "group", code)
}

// DeleteGroup removes the group; member accounts have their link cleared.
func (r *realmStore) DeleteGroup(ctx context.Context, code string) error {
	res, err := r.db().ExecContext(
		ctx, `DELETE FROM account_groups WHERE code = ?`, code,
	)
	if err != nil {
		return fmt.Errorf("store: delete group: %w", err)
	}
	return notFoundIfNoRows(res, "group", code)
}

// ListGroupAccounts returns every account whose group is code.
func (r *realmStore) ListGroupAccounts(
	ctx context.Context, code string,
) ([]domain.Account, error) {
	rows, err := r.db().QueryContext(
		ctx, accountSelect+` WHERE g.code = ? ORDER BY a.code`, code,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list group accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanAccounts(rows)
}

func scanGroup(rows *sql.Rows) (domain.AccountGroup, error) {
	var (
		group    domain.AccountGroup
		engineID int64
	)
	if err := rows.Scan(
		&engineID, &group.Code, &group.Title,
		&group.Notes, &group.Blocked, &group.BlockReason,
	); err != nil {
		return domain.AccountGroup{}, fmt.Errorf("store: scan group: %w", err)
	}
	group.EngineGroupID = domain.EngineGroupID(engineID)
	return group, nil
}

func scanGroupRow(row *sql.Row) (domain.AccountGroup, error) {
	var (
		group    domain.AccountGroup
		engineID int64
	)
	if err := row.Scan(
		&engineID, &group.Code, &group.Title,
		&group.Notes, &group.Blocked, &group.BlockReason,
	); err != nil {
		return domain.AccountGroup{}, err
	}
	group.EngineGroupID = domain.EngineGroupID(engineID)
	return group, nil
}

// --- Accounts ---------------------------------------------------------------

// accountSelect is the shared projection for account reads. The LEFT JOIN
// surfaces the group's code (NULL when the account is in no group) so the
// surrogate group id never leaves the store.
const accountSelect = `
SELECT a.engine_account_id, a.code, a.title, g.code,
       a.notes, a.blocked, a.block_reason
FROM accounts a
LEFT JOIN account_groups g ON g.id = a.group_id`

// CreateAccount persists a new account, assigning a collision-free engine
// account id inside the insert transaction, resolving an optional group code to
// its surrogate id, and returns the account with EngineAccountID populated.
func (r *realmStore) CreateAccount(
	ctx context.Context, account domain.Account,
) (domain.Account, error) {
	tx, err := r.db().BeginTx(ctx, nil)
	if err != nil {
		return domain.Account{}, fmt.Errorf("store: begin create account: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	groupID, err := optionalGroupID(ctx, tx, account.GroupCode)
	if err != nil {
		return domain.Account{}, err
	}
	engineID, err := nextEngineAccountID(ctx, tx)
	if err != nil {
		return domain.Account{}, err
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO accounts
		 (engine_account_id, code, title, group_id, notes, blocked, block_reason)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		int64(engineID.Uint64()), account.Code.String(), account.Title,
		groupID, account.Notes, account.Blocked, account.BlockReason,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return domain.Account{},
				fmt.Errorf("account %q: %w", account.Code, domain.ErrAlreadyExists)
		}
		return domain.Account{}, fmt.Errorf("store: create account: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Account{}, fmt.Errorf("store: commit create account: %w", err)
	}
	account.EngineAccountID = engineID
	return account, nil
}

// GetAccount returns the account with the given code.
func (r *realmStore) GetAccount(
	ctx context.Context, code domain.AccountID,
) (domain.Account, bool, error) {
	row := r.db().QueryRowContext(
		ctx, accountSelect+` WHERE a.code = ?`, code.String(),
	)
	account, err := scanAccountRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Account{}, false, nil
	}
	if err != nil {
		return domain.Account{}, false, fmt.Errorf("store: get account: %w", err)
	}
	return account, true, nil
}

// ListAccounts returns every account, ordered by code.
func (r *realmStore) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	rows, err := r.db().QueryContext(ctx, accountSelect+` ORDER BY a.code`)
	if err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanAccounts(rows)
}

// SetAccountBlocked updates the blocked flag and block reason of the account.
func (r *realmStore) SetAccountBlocked(
	ctx context.Context, code domain.AccountID, blocked bool, reason string,
) error {
	res, err := r.db().ExecContext(
		ctx,
		`UPDATE accounts SET blocked = ?, block_reason = ? WHERE code = ?`,
		blocked, reason, code.String(),
	)
	if err != nil {
		return fmt.Errorf("store: set account blocked: %w", err)
	}
	return notFoundIfNoRows(res, "account", code.String())
}

// SetAccountGroup sets the account's group link, resolving the group code; an
// empty group code clears it.
func (r *realmStore) SetAccountGroup(
	ctx context.Context, code domain.AccountID, groupCode string,
) error {
	groupID, err := optionalGroupID(ctx, r.db(), groupCode)
	if err != nil {
		return err
	}
	res, err := r.db().ExecContext(
		ctx, `UPDATE accounts SET group_id = ? WHERE code = ?`,
		groupID, code.String(),
	)
	if err != nil {
		return fmt.Errorf("store: set account group: %w", err)
	}
	return notFoundIfNoRows(res, "account", code.String())
}

// SetAccountNotes replaces the notes of the account.
func (r *realmStore) SetAccountNotes(
	ctx context.Context, code domain.AccountID, notes string,
) error {
	res, err := r.db().ExecContext(
		ctx, `UPDATE accounts SET notes = ? WHERE code = ?`, notes, code.String(),
	)
	if err != nil {
		return fmt.Errorf("store: set account notes: %w", err)
	}
	return notFoundIfNoRows(res, "account", code.String())
}

// DeleteAccount removes the account and, when forced, cascades its dependents.
func (r *realmStore) DeleteAccount(
	ctx context.Context, code domain.AccountID, force bool,
) error {
	tx, err := r.db().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin delete account: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	accountID, err := resolveAccountID(ctx, tx, code)
	if err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			return fmt.Errorf("account %q: %w", code, domain.ErrNotFound)
		}
		return err
	}
	if !force {
		deps, err := accountDependents(ctx, tx, accountID)
		if err != nil {
			return err
		}
		if len(deps) > 0 {
			return domain.NewHasDependentsError(deps)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, accountID)
	if err != nil {
		return fmt.Errorf("store: delete account: %w", err)
	}
	if err := notFoundIfNoRows(res, "account", code.String()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit delete account: %w", err)
	}
	return nil
}

func scanAccounts(rows *sql.Rows) ([]domain.Account, error) {
	accounts := make([]domain.Account, 0)
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate accounts: %w", err)
	}
	return accounts, nil
}

func scanAccount(rows *sql.Rows) (domain.Account, error) {
	var (
		account   domain.Account
		engineID  int64
		groupCode sql.NullString
	)
	if err := rows.Scan(
		&engineID, &account.Code, &account.Title, &groupCode,
		&account.Notes, &account.Blocked, &account.BlockReason,
	); err != nil {
		return domain.Account{}, fmt.Errorf("store: scan account: %w", err)
	}
	account.EngineAccountID = domain.EngineAccountID(engineID)
	account.GroupCode = groupCode.String
	return account, nil
}

func scanAccountRow(row *sql.Row) (domain.Account, error) {
	var (
		account   domain.Account
		engineID  int64
		groupCode sql.NullString
	)
	if err := row.Scan(
		&engineID, &account.Code, &account.Title, &groupCode,
		&account.Notes, &account.Blocked, &account.BlockReason,
	); err != nil {
		return domain.Account{}, err
	}
	account.EngineAccountID = domain.EngineAccountID(engineID)
	account.GroupCode = groupCode.String
	return account, nil
}

// --- Group-1 shared small helpers -------------------------------------------

// optionalGroupID resolves an optional group code to a nullable surrogate id: an
// empty code yields a NULL group link; a non-empty unknown code wraps
// domain.ErrInvalid.
func optionalGroupID(
	ctx context.Context, q sqlQueryer, code string,
) (sql.NullInt64, error) {
	if code == "" {
		return sql.NullInt64{}, nil
	}
	id, err := resolveGroupID(ctx, q, code)
	if err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: id, Valid: true}, nil
}

// nullableString maps an empty string to a NULL column value and a non-empty
// string to itself. It backs the optional asset_class column.
func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// notFoundIfNoRows turns a zero-rows-affected result into domain.ErrNotFound for
// an addressed update or delete, naming the entity and its code.
func notFoundIfNoRows(res sql.Result, entity, code string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: %s rows affected: %w", entity, err)
	}
	if n == 0 {
		return fmt.Errorf("%s %q: %w", entity, code, domain.ErrNotFound)
	}
	return nil
}

func accountDependents(
	ctx context.Context, q sqlQueryer, accountID int64,
) ([]domain.DependentCount, error) {
	checks := []dependentQuery{
		{"balances", `SELECT COUNT(*) FROM balances WHERE account_id = ?`},
		{"orders", `SELECT COUNT(*) FROM orders WHERE account_id = ?`},
		{"order_events", `SELECT COUNT(*) FROM order_events ev
		 JOIN orders o ON o.id = ev.order_id WHERE o.account_id = ?`},
		{"trades", `SELECT COUNT(*) FROM trades WHERE account_id = ?`},
		{"order_approvals", `SELECT COUNT(*) FROM order_approvals ap
		 JOIN orders o ON o.id = ap.order_id WHERE o.account_id = ?`},
		{"adjustments", `SELECT COUNT(*) FROM adjustments WHERE account_id = ?`},
		{"limit_rate", `SELECT COUNT(*) FROM limit_rate WHERE account_id = ?`},
		{"limit_order_size", `SELECT COUNT(*) FROM limit_order_size WHERE account_id = ?`},
		{"limit_pnl_bounds", `SELECT COUNT(*) FROM limit_pnl_bounds WHERE account_id = ?`},
		{"reservation_intents", `SELECT COUNT(*) FROM reservation_intents WHERE account_id = ?`},
	}
	return collectDependents(ctx, q, checks, accountID)
}

func assetDependents(
	ctx context.Context, q sqlQueryer, assetID int64,
) ([]domain.DependentCount, error) {
	checks := []dependentQuery{
		{"balances", `SELECT COUNT(*) FROM balances WHERE asset_id = ?`},
		{"adjustments", `SELECT COUNT(*) FROM adjustments WHERE asset_id = ?`},
		{"limit_rate", `SELECT COUNT(*) FROM limit_rate WHERE asset_id = ?`},
		{"limit_order_size", `SELECT COUNT(*) FROM limit_order_size WHERE asset_id = ?`},
		{"limit_pnl_bounds", `SELECT COUNT(*) FROM limit_pnl_bounds WHERE asset_id = ?`},
		{"orders", `SELECT COUNT(*) FROM orders
		 WHERE base_asset_id = ? OR quote_asset_id = ?`},
		{"trades", `SELECT COUNT(*) FROM trades
		 WHERE base_asset_id = ? OR quote_asset_id = ?`},
		{"market_data_instruments", `SELECT COUNT(*) FROM market_data_instruments
		 WHERE base_asset_id = ? OR quote_asset_id = ?`},
	}
	return collectDependents(ctx, q, checks, assetID)
}

type dependentQuery struct {
	kind  string
	query string
}

func collectDependents(
	ctx context.Context, q sqlQueryer, checks []dependentQuery, id int64,
) ([]domain.DependentCount, error) {
	dependents := make([]domain.DependentCount, 0)
	for _, check := range checks {
		count, err := countDependent(ctx, q, check.query, id)
		if err != nil {
			return nil, err
		}
		if count > 0 {
			dependents = append(dependents, domain.DependentCount{
				Kind:  check.kind,
				Count: count,
			})
		}
	}
	return dependents, nil
}

func countDependent(ctx context.Context, q sqlQueryer, query string, id int64) (int, error) {
	args := []any{id}
	if strings.Count(query, "?") == 2 {
		args = append(args, id)
	}
	var count int
	if err := q.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("store: count delete dependents: %w", err)
	}
	return count, nil
}
