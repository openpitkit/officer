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
// addressed by their public code; the surrogate key never crosses the
// interface boundary. Accounts and groups carry a connector-assigned engine id,
// returned (populated) on the create path so the engine layer can build its
// tree, and an account's group link is cleared (SET NULL), not cascaded, when its
// group is deleted.

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

// --- Assets -----------------------------------------------------------------

// assetSelect is the shared projection for asset reads. The LEFT JOIN surfaces
// the class's code (NULL when the asset has no class) so the surrogate class id
// never leaves the store.
const assetSelect = `
SELECT a.code, a.title, c.code
FROM asset a
LEFT JOIN asset_class c ON c.id = a.class_id`

// CreateAsset persists a new asset dictionary row, resolving an optional class
// code to its surrogate id (an empty code leaves the link NULL).
func (r *realmStore) CreateAsset(ctx context.Context, asset domain.Asset) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	classID, err := optionalClassID(ctx, db, asset.AssetClass)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(
		ctx,
		`INSERT INTO asset (code, title, class_id) VALUES (?, ?, ?)`,
		asset.Code, asset.Title, classID,
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
	db, err := r.db()
	if err != nil {
		return domain.Asset{}, false, err
	}
	row := db.QueryRowContext(ctx, assetSelect+` WHERE a.code = ?`, code)
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
	page, err := r.ListAssetRows(ctx, fwstore.AssetListFilter{})
	if err != nil {
		return nil, err
	}
	return page.Rows, nil
}

// ListAssetRows returns assets in the requested list sort order with count.
func (r *realmStore) ListAssetRows(
	ctx context.Context, filter fwstore.AssetListFilter,
) (fwstore.AssetListPage, error) {
	clauses, args := assetListWhere(filter)
	query := assetSelect
	if len(clauses) > 0 {
		query += "\nWHERE " + strings.Join(clauses, " AND ")
	}

	countQuery := `SELECT COUNT(*) FROM asset a
LEFT JOIN asset_class c ON c.id = a.class_id`
	if len(clauses) > 0 {
		countQuery += "\nWHERE " + strings.Join(clauses, " AND ")
	}
	db, err := r.db()
	if err != nil {
		return fwstore.AssetListPage{}, err
	}
	var total int
	if err := db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return fwstore.AssetListPage{}, fmt.Errorf("store: count assets: %w", err)
	}

	queryArgs := append([]any{}, args...)
	query += assetListOrderBy(filter.Sort)
	if filter.Page.Limit > 0 {
		query += "\nLIMIT ? OFFSET ?"
		queryArgs = append(queryArgs, filter.Page.Limit, max(filter.Page.Offset, 0))
	}
	rows, err := db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return fwstore.AssetListPage{}, fmt.Errorf("store: list assets: %w", err)
	}
	defer func() { _ = rows.Close() }()

	assets := make([]domain.Asset, 0)
	for rows.Next() {
		asset, err := scanAsset(rows)
		if err != nil {
			return fwstore.AssetListPage{}, err
		}
		assets = append(assets, asset)
	}
	if err := rows.Err(); err != nil {
		return fwstore.AssetListPage{}, fmt.Errorf("store: iterate assets: %w", err)
	}
	return fwstore.AssetListPage{Rows: assets, Total: total}, nil
}

func assetListWhere(filter fwstore.AssetListFilter) ([]string, []any) {
	clauses := make([]string, 0)
	args := make([]any, 0)
	appendMatcherAny(&clauses, &args, []string{"a.code", "a.title"}, filter.Code)
	appendMatcher(&clauses, &args, "c.code", filter.Class)
	return clauses, args
}

func assetListOrderBy(sort fwstore.SortSpec) string {
	columns := map[string]string{
		"assetClass": "c.code",
		"code":       "a.code",
		"title":      "a.title",
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
	return "\nORDER BY " + column + " " + direction + ", a.code " + tieDirection
}

// UpdateAsset replaces the public code and mutable fields of the identified
// asset. Balances and other rows reference the asset by its surrogate id, so a
// code rename is safe, mirroring UpdateGroup. The optional class code is resolved
// to its surrogate id (an empty code clears the link).
func (r *realmStore) UpdateAsset(
	ctx context.Context, oldCode string, asset domain.Asset,
) (domain.Asset, error) {
	db, err := r.db()
	if err != nil {
		return domain.Asset{}, err
	}
	classID, err := optionalClassID(ctx, db, asset.AssetClass)
	if err != nil {
		return domain.Asset{}, err
	}
	res, err := db.ExecContext(
		ctx,
		`UPDATE asset SET code = ?, title = ?, class_id = ? WHERE code = ?`,
		asset.Code, asset.Title, classID, oldCode,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return domain.Asset{},
				fmt.Errorf("asset %q: %w", asset.Code, domain.ErrAlreadyExists)
		}
		return domain.Asset{}, fmt.Errorf("store: update asset: %w", err)
	}
	if err := notFoundIfNoRows(res, "asset", oldCode); err != nil {
		return domain.Asset{}, err
	}
	updated, ok, err := r.GetAsset(ctx, asset.Code)
	if err != nil {
		return domain.Asset{}, err
	}
	if !ok {
		return domain.Asset{},
			fmt.Errorf("asset %q: %w", asset.Code, domain.ErrNotFound)
	}
	return updated, nil
}

// DeleteAsset removes the asset and, when forced, cascades its dependent rows.
func (r *realmStore) DeleteAsset(ctx context.Context, code string, force bool) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
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
	currencyDeps, err := assetCurrencyDependents(ctx, tx, assetID)
	if err != nil {
		return err
	}
	if len(currencyDeps) > 0 {
		return domain.NewHasDependentsError(currencyDeps)
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
	res, err := tx.ExecContext(ctx, `DELETE FROM asset WHERE id = ?`, assetID)
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

// --- Asset classes ----------------------------------------------------------

// CreateAssetClass persists a new asset-class dictionary row.
func (r *realmStore) CreateAssetClass(
	ctx context.Context, class domain.AssetClass,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(
		ctx,
		`INSERT INTO asset_class (code, title, notes) VALUES (?, ?, ?)`,
		class.Code, class.Title, class.Notes,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return fmt.Errorf("asset class %q: %w", class.Code, domain.ErrAlreadyExists)
		}
		return fmt.Errorf("store: create asset class: %w", err)
	}
	return nil
}

// GetAssetClass returns the asset class with the given code.
func (r *realmStore) GetAssetClass(
	ctx context.Context, code string,
) (domain.AssetClass, bool, error) {
	db, err := r.db()
	if err != nil {
		return domain.AssetClass{}, false, err
	}
	var class domain.AssetClass
	err = db.QueryRowContext(
		ctx, `SELECT code, title, notes FROM asset_class WHERE code = ?`, code,
	).Scan(&class.Code, &class.Title, &class.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AssetClass{}, false, nil
	}
	if err != nil {
		return domain.AssetClass{}, false, fmt.Errorf("store: get asset class: %w", err)
	}
	return class, true, nil
}

// ListAssetClasses returns every asset class, ordered by code.
func (r *realmStore) ListAssetClasses(
	ctx context.Context,
) ([]domain.AssetClass, error) {
	page, err := r.ListAssetClassRows(ctx, fwstore.AssetClassListFilter{})
	if err != nil {
		return nil, err
	}
	classes := make([]domain.AssetClass, 0, len(page.Rows))
	for _, row := range page.Rows {
		classes = append(classes, row.Class)
	}
	return classes, nil
}

// ListAssetClassRows returns asset classes matching filter, sorted and paged,
// with the count of assets whose asset_class equals each class code.
func (r *realmStore) ListAssetClassRows(
	ctx context.Context, filter fwstore.AssetClassListFilter,
) (fwstore.AssetClassListPage, error) {
	where, args := assetClassListWhere(filter)
	from := `
FROM asset_class c
LEFT JOIN asset a ON a.class_id = c.id` +
		where + `
GROUP BY c.id`

	countQuery := `SELECT COUNT(*) FROM (SELECT c.id` + from + `) AS filtered`
	db, err := r.db()
	if err != nil {
		return fwstore.AssetClassListPage{}, err
	}
	var total int
	if err := db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return fwstore.AssetClassListPage{}, fmt.Errorf("store: count asset class rows: %w", err)
	}

	queryArgs := append([]any{}, args...)
	query := `
SELECT c.code, c.title, c.notes, COUNT(a.id) AS asset_count` +
		from + assetClassListOrderBy(filter.Sort)
	if filter.Page.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		queryArgs = append(queryArgs, filter.Page.Limit, max(filter.Page.Offset, 0))
	}

	rows, err := db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return fwstore.AssetClassListPage{}, fmt.Errorf("store: list asset class rows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	classes := make([]fwstore.AssetClassListRow, 0)
	for rows.Next() {
		class, err := scanAssetClassListRow(rows)
		if err != nil {
			return fwstore.AssetClassListPage{}, err
		}
		classes = append(classes, class)
	}
	if err := rows.Err(); err != nil {
		return fwstore.AssetClassListPage{}, fmt.Errorf("store: iterate asset class rows: %w", err)
	}
	return fwstore.AssetClassListPage{Rows: classes, Total: total}, nil
}

// UpdateAssetClass replaces the public code, title and notes of the identified
// class. Assets link to it by the class_id foreign key, so a code rename needs
// no cascade, mirroring UpdateGroup.
func (r *realmStore) UpdateAssetClass(
	ctx context.Context, oldCode string, class domain.AssetClass,
) (domain.AssetClass, error) {
	db, err := r.db()
	if err != nil {
		return domain.AssetClass{}, err
	}
	res, err := db.ExecContext(
		ctx,
		`UPDATE asset_class SET code = ?, title = ?, notes = ? WHERE code = ?`,
		class.Code, class.Title, class.Notes, oldCode,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return domain.AssetClass{},
				fmt.Errorf("asset class %q: %w", class.Code, domain.ErrAlreadyExists)
		}
		return domain.AssetClass{}, fmt.Errorf("store: update asset class: %w", err)
	}
	if err := notFoundIfNoRows(res, "asset class", oldCode); err != nil {
		return domain.AssetClass{}, err
	}
	return class, nil
}

// DeleteAssetClass removes the class. Assets link to it by the class_id foreign
// key: without force, referencing assets are reported as dependents; either way
// the delete clears the link via ON DELETE SET NULL, mirroring DeleteGroup.
func (r *realmStore) DeleteAssetClass(
	ctx context.Context, code string, force bool,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin delete asset class: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	classID, err := resolveAssetClassID(ctx, tx, code)
	if err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			return fmt.Errorf("asset class %q: %w", code, domain.ErrNotFound)
		}
		return err
	}
	if !force {
		count, err := assetClassAssetCount(ctx, tx, classID)
		if err != nil {
			return err
		}
		if count > 0 {
			return domain.NewHasDependentsError([]domain.DependentCount{
				{Kind: "asset", Count: count},
			})
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM asset_class WHERE id = ?`, classID)
	if err != nil {
		return fmt.Errorf("store: delete asset class: %w", err)
	}
	if err := notFoundIfNoRows(res, "asset class", code); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit delete asset class: %w", err)
	}
	return nil
}

func assetClassAssetCount(
	ctx context.Context, q sqlQueryer, classID int64,
) (int, error) {
	var count int
	if err := q.QueryRowContext(
		ctx, `SELECT COUNT(*) FROM asset WHERE class_id = ?`, classID,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("store: count asset class assets: %w", err)
	}
	return count, nil
}

func scanAssetClassListRow(rows *sql.Rows) (fwstore.AssetClassListRow, error) {
	var (
		class      domain.AssetClass
		assetCount int
	)
	if err := rows.Scan(&class.Code, &class.Title, &class.Notes, &assetCount); err != nil {
		return fwstore.AssetClassListRow{}, fmt.Errorf("store: scan asset class row: %w", err)
	}
	return fwstore.AssetClassListRow{Class: class, AssetCount: assetCount}, nil
}

func assetClassListWhere(filter fwstore.AssetClassListFilter) (string, []any) {
	clauses := make([]string, 0)
	args := make([]any, 0)
	appendMatcherAny(&clauses, &args, []string{"c.code", "c.title"}, filter.Code)
	appendMatcher(&clauses, &args, "c.notes", filter.Notes)
	if len(clauses) == 0 {
		return "", args
	}
	return "\nWHERE " + strings.Join(clauses, " AND "), args
}

func assetClassListOrderBy(sort fwstore.SortSpec) string {
	columns := map[string]string{
		"assetCount": "asset_count",
		"code":       "c.code",
		"title":      "c.title",
	}
	column := columns[sort.Column]
	if column == "" {
		column = "c.code"
	}
	direction := "ASC"
	tieDirection := "ASC"
	if sort.Descending {
		direction = "DESC"
		tieDirection = "DESC"
	}
	return "\nORDER BY " + column + " " + direction + ", c.code " + tieDirection
}

// --- Principals -------------------------------------------------------------

// CreatePrincipal persists a new principal dictionary row.
func (r *realmStore) CreatePrincipal(
	ctx context.Context, principal domain.Principal,
) error {
	if err := domain.ValidatePrincipalID(principal.Code); err != nil {
		return err
	}
	if err := domain.ValidateTitle(principal.Title); err != nil {
		return err
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(
		ctx,
		`INSERT INTO principal (code, title) VALUES (?, ?)`,
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
	db, err := r.db()
	if err != nil {
		return domain.Principal{}, false, err
	}
	var principal domain.Principal
	err = db.QueryRowContext(
		ctx, `SELECT code, title FROM principal WHERE code = ?`, code,
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
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(
		ctx, `SELECT code, title FROM principal ORDER BY code`,
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
	if err := domain.ValidatePrincipalID(principal.Code); err != nil {
		return err
	}
	if err := domain.ValidateTitle(principal.Title); err != nil {
		return err
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
		ctx, `UPDATE principal SET title = ? WHERE code = ?`,
		principal.Title, principal.Code,
	)
	if err != nil {
		return fmt.Errorf("store: update principal: %w", err)
	}
	return notFoundIfNoRows(res, "principal", principal.Code)
}

// DeletePrincipal removes the principal; references to it are cleared.
func (r *realmStore) DeletePrincipal(ctx context.Context, code string) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `DELETE FROM principal WHERE code = ?`, code)
	if err != nil {
		return fmt.Errorf("store: delete principal: %w", err)
	}
	return notFoundIfNoRows(res, "principal", code)
}

// --- Account groups ---------------------------------------------------------

// CreateGroup persists a new account group and returns it with EngineGroupID
// populated from the inserted row's surrogate id, which is the id the engine runs
// the group on.
func (r *realmStore) CreateGroup(
	ctx context.Context, group domain.AccountGroup,
) (domain.AccountGroup, error) {
	if group.Code == "" {
		return domain.AccountGroup{},
			fmt.Errorf("group code is reserved for the default group: %w", domain.ErrInvalid)
	}
	db, err := r.db()
	if err != nil {
		return domain.AccountGroup{}, err
	}
	assetID, err := nullableAssetID(ctx, db, group.Currency)
	if err != nil {
		return domain.AccountGroup{}, err
	}
	res, err := db.ExecContext(
		ctx,
		`INSERT INTO account_group
		 (code, title, currency_asset_id, notes, blocked, block_reason)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		group.Code, group.Title, assetID, group.Notes, group.Blocked,
		group.BlockReason,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return domain.AccountGroup{},
				fmt.Errorf("group %q: %w", group.Code, domain.ErrAlreadyExists)
		}
		return domain.AccountGroup{}, fmt.Errorf("store: create group: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.AccountGroup{}, fmt.Errorf("store: create group id: %w", err)
	}
	group.EngineGroupID = domain.EngineGroupID(id)
	return group, nil
}

// GetGroup returns the group with the given code. The persisted default-group
// row is exposed with reserved EngineGroupID 0 rather than its SQLite row id.
func (r *realmStore) GetGroup(
	ctx context.Context, code string,
) (domain.AccountGroup, bool, error) {
	db, err := r.db()
	if err != nil {
		return domain.AccountGroup{}, false, err
	}
	row := db.QueryRowContext(
		ctx,
		`SELECT CASE WHEN g.code = '' THEN 0 ELSE g.id END,
		        g.code, g.title, ca.code, g.notes, g.blocked,
		        g.block_reason
		 FROM account_group g
		 LEFT JOIN asset ca ON ca.id = g.currency_asset_id
		 WHERE g.code = ?`,
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
	page, err := r.ListGroupRows(ctx, fwstore.GroupListFilter{})
	if err != nil {
		return nil, err
	}
	groups := make([]domain.AccountGroup, 0, len(page.Rows))
	for _, row := range page.Rows {
		if row.Group.Code == "" {
			continue
		}
		groups = append(groups, row.Group)
	}
	return groups, nil
}

// ListGroupRows returns groups matching filter, sorted and paged, plus the
// synthetic default group for accounts without a group.
//
// The default group is a single synthetic row that cannot share a SQL sort or
// offset window with the real groups (it has no code and no stable position
// among them). It is therefore handled separately: it is read once with the
// same filter visibility rules as the real groups and, when it qualifies,
// pinned first on the first page (offset zero). It is excluded from the real
// groups' ORDER BY, LIMIT/OFFSET window, and from Total, so paging the real
// groups stays deterministic regardless of the default group's presence.
func (r *realmStore) ListGroupRows(
	ctx context.Context, filter fwstore.GroupListFilter,
) (fwstore.GroupListPage, error) {
	where, args := groupListWhere(filter, "g")
	having, havingArgs := countHaving(
		"COUNT(DISTINCT a.id)", filter.Account, "COUNT(b.asset_id)", filter.Position,
	)
	from := `
FROM account_group g
LEFT JOIN asset ca ON ca.id = g.currency_asset_id
LEFT JOIN account a ON a.group_id = g.id
LEFT JOIN balance b ON b.account_id = a.id` +
		realGroupWhere(where) + `
GROUP BY g.id` + having

	countArgs := append(append([]any{}, args...), havingArgs...)
	countQuery := `SELECT COUNT(*) FROM (SELECT g.id` + from + `) AS filtered`
	db, err := r.db()
	if err != nil {
		return fwstore.GroupListPage{}, err
	}
	var total int
	if err := db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return fwstore.GroupListPage{}, fmt.Errorf("store: count group rows: %w", err)
	}

	// The default group is read first, before the real-group cursor is opened:
	// the SQLite connector serializes on a single connection, so a second query
	// issued while a result set is still open would deadlock on the pool.
	groups := make([]fwstore.GroupListRow, 0)
	if filter.Page.Offset <= 0 {
		defaultRow, ok, err := r.defaultGroupRow(ctx, filter)
		if err != nil {
			return fwstore.GroupListPage{}, err
		}
		if ok {
			groups = append(groups, defaultRow)
		}
	}

	queryArgs := append(append([]any{}, args...), havingArgs...)
	query := `
SELECT g.id, g.code, g.title, ca.code, g.notes, g.blocked, g.block_reason,
       COUNT(DISTINCT a.id) AS account_count,
       COUNT(b.asset_id) AS position_count` + from + groupListOrderBy(filter.Sort)
	if filter.Page.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		queryArgs = append(queryArgs, filter.Page.Limit, max(filter.Page.Offset, 0))
	}

	rows, err := db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return fwstore.GroupListPage{}, fmt.Errorf("store: list group rows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		group, err := scanGroupListRow(rows)
		if err != nil {
			return fwstore.GroupListPage{}, err
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return fwstore.GroupListPage{}, fmt.Errorf("store: iterate group rows: %w", err)
	}
	return fwstore.GroupListPage{Rows: groups, Total: total}, nil
}

// defaultGroupRow reads the synthetic default group (code empty) holding the
// accounts with no group, applying the same filter visibility rules as the real
// groups. The bool is false when the filter excludes it.
func (r *realmStore) defaultGroupRow(
	ctx context.Context, filter fwstore.GroupListFilter,
) (fwstore.GroupListRow, bool, error) {
	where, args := groupListWhere(filter, "dg")
	having, havingArgs := countHaving(
		"COUNT(DISTINCT a.id)", filter.Account, "COUNT(b.asset_id)", filter.Position,
	)
	args = append(args, havingArgs...)
	query := `
SELECT dg.id, dg.code, dg.title, dca.code, dg.notes, dg.blocked,
       dg.block_reason,
       COUNT(DISTINCT a.id) AS account_count,
       COUNT(b.asset_id) AS position_count
FROM (
    SELECT 0 AS id, '' AS code, '' AS title, '' AS notes,
           0 AS blocked, '' AS block_reason
) dg
LEFT JOIN account_group persisted_default ON persisted_default.code = ''
LEFT JOIN asset dca ON dca.id = persisted_default.currency_asset_id
LEFT JOIN account a ON a.group_id IS NULL
LEFT JOIN balance b ON b.account_id = a.id` +
		where + `
GROUP BY dg.code` + having

	db, err := r.db()
	if err != nil {
		return fwstore.GroupListRow{}, false, err
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fwstore.GroupListRow{}, false, fmt.Errorf("store: default group row: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return fwstore.GroupListRow{}, false, fmt.Errorf("store: default group row: %w", err)
		}
		return fwstore.GroupListRow{}, false, nil
	}
	row, err := scanGroupListRow(rows)
	if err != nil {
		return fwstore.GroupListRow{}, false, err
	}
	return row, true, nil
}

// SetGroupNotes replaces the notes of the identified group.
func (r *realmStore) SetGroupNotes(ctx context.Context, code, notes string) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
		ctx, `UPDATE account_group SET notes = ? WHERE code = ?`, notes, code,
	)
	if err != nil {
		return fmt.Errorf("store: set group notes: %w", err)
	}
	return notFoundIfNoRows(res, "group", code)
}

// SetGroupCurrency sets or clears the currency of a group. The empty group code
// is the reserved default group tier and is materialized as the empty-code row.
func (r *realmStore) SetGroupCurrency(
	ctx context.Context, code string, currency string,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	assetID, err := nullableAssetID(ctx, db, currency)
	if err != nil {
		return err
	}
	if code == "" {
		_, err := db.ExecContext(
			ctx,
			`INSERT INTO account_group (code, currency_asset_id)
			 VALUES ('', ?)
			 ON CONFLICT(code) DO UPDATE SET currency_asset_id = excluded.currency_asset_id`,
			assetID,
		)
		if err != nil {
			return fmt.Errorf("store: set default group currency: %w", err)
		}
		return nil
	}
	res, err := db.ExecContext(
		ctx, `UPDATE account_group SET currency_asset_id = ? WHERE code = ?`,
		assetID, code,
	)
	if err != nil {
		return fmt.Errorf("store: set group currency: %w", err)
	}
	return notFoundIfNoRows(res, "group", code)
}

// UpdateGroup replaces the public code and title of the identified group.
func (r *realmStore) UpdateGroup(
	ctx context.Context, oldCode string, group domain.AccountGroup,
) (domain.AccountGroup, error) {
	if oldCode == "" || group.Code == "" {
		return domain.AccountGroup{},
			fmt.Errorf("group code is reserved for the default group: %w", domain.ErrInvalid)
	}
	db, err := r.db()
	if err != nil {
		return domain.AccountGroup{}, err
	}
	res, err := db.ExecContext(
		ctx,
		`UPDATE account_group SET code = ?, title = ? WHERE code = ?`,
		group.Code, group.Title, oldCode,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return domain.AccountGroup{},
				fmt.Errorf("group %q: %w", group.Code, domain.ErrAlreadyExists)
		}
		return domain.AccountGroup{}, fmt.Errorf("store: update group: %w", err)
	}
	if err := notFoundIfNoRows(res, "group", oldCode); err != nil {
		return domain.AccountGroup{}, err
	}
	updated, ok, err := r.GetGroup(ctx, group.Code)
	if err != nil {
		return domain.AccountGroup{}, err
	}
	if !ok {
		return domain.AccountGroup{},
			fmt.Errorf("group %q: %w", group.Code, domain.ErrNotFound)
	}
	return updated, nil
}

// SetGroupBlocked updates the blocked flag and block reason of the group.
func (r *realmStore) SetGroupBlocked(
	ctx context.Context, code string, blocked bool, reason string,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
		ctx,
		`UPDATE account_group SET blocked = ?, block_reason = ? WHERE code = ?`,
		blocked, reason, code,
	)
	if err != nil {
		return fmt.Errorf("store: set group blocked: %w", err)
	}
	return notFoundIfNoRows(res, "group", code)
}

// DeleteGroup removes the group; member accounts have their link cleared.
func (r *realmStore) DeleteGroup(ctx context.Context, code string) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
		ctx, `DELETE FROM account_group WHERE code = ?`, code,
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
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(
		ctx, accountSelect+` WHERE g.code = ? ORDER BY a.code`, code,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list group accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanAccounts(rows)
}

func scanGroupListRow(rows *sql.Rows) (fwstore.GroupListRow, error) {
	var (
		group         domain.AccountGroup
		engineID      int64
		currency      sql.NullString
		accountCount  int
		positionCount int
	)
	if err := rows.Scan(
		&engineID, &group.Code, &group.Title, &currency,
		&group.Notes, &group.Blocked, &group.BlockReason,
		&accountCount, &positionCount,
	); err != nil {
		return fwstore.GroupListRow{}, fmt.Errorf("store: scan group row: %w", err)
	}
	group.EngineGroupID = domain.EngineGroupID(engineID)
	group.Currency = currency.String
	return fwstore.GroupListRow{
		Group:         group,
		AccountCount:  accountCount,
		PositionCount: positionCount,
	}, nil
}

func scanGroupRow(row *sql.Row) (domain.AccountGroup, error) {
	var (
		group    domain.AccountGroup
		engineID int64
	)
	var currency sql.NullString
	if err := row.Scan(
		&engineID, &group.Code, &group.Title, &currency,
		&group.Notes, &group.Blocked, &group.BlockReason,
	); err != nil {
		return domain.AccountGroup{}, err
	}
	group.EngineGroupID = domain.EngineGroupID(engineID)
	group.Currency = currency.String
	return group, nil
}

// --- Accounts ---------------------------------------------------------------

// accountSelect is the shared projection for account reads. The LEFT JOIN
// surfaces the group's code (NULL when the account is in no group) so the
// surrogate group id never leaves the store.
const accountSelect = `
SELECT a.id, a.code, a.title, a.pnl, a.pnl_halt_reason, ac.code, g.code, gc.code, dc.code,
       a.notes, a.blocked, a.block_reason
FROM account a
LEFT JOIN asset ac ON ac.id = a.currency_asset_id
LEFT JOIN account_group g ON g.id = a.group_id
LEFT JOIN asset gc ON gc.id = g.currency_asset_id
LEFT JOIN account_group dg ON dg.code = ''
LEFT JOIN asset dc ON dc.id = dg.currency_asset_id`

// CreateAccount persists a new account, resolving an optional group code to its
// surrogate id, and returns the account with EngineAccountID populated from the
// inserted row's surrogate id, which is the id the engine runs the account on.
func (r *realmStore) CreateAccount(
	ctx context.Context, account domain.Account,
) (domain.Account, error) {
	db, err := r.db()
	if err != nil {
		return domain.Account{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Account{}, fmt.Errorf("store: begin create account: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	groupID, err := optionalGroupID(ctx, tx, account.GroupCode)
	if err != nil {
		return domain.Account{}, err
	}
	currencyID, err := nullableAssetID(ctx, tx, account.Currency)
	if err != nil {
		return domain.Account{}, err
	}
	pnl := account.Pnl
	if pnl == "" {
		pnl = "0"
	}
	if _, err := domain.AddDecimals("", pnl); err != nil {
		return domain.Account{}, fmt.Errorf("account %q pnl %q: %w", account.Code, pnl, err)
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO account
		 (code, title, group_id, currency_asset_id, pnl, pnl_halt_reason, notes, blocked, block_reason)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		account.Code.String(), account.Title,
		groupID, currencyID, pnl, account.PnlHaltReason, account.Notes, account.Blocked, account.BlockReason,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return domain.Account{},
				fmt.Errorf("account %q: %w", account.Code, domain.ErrAlreadyExists)
		}
		return domain.Account{}, fmt.Errorf("store: create account: %w", err)
	}
	created, err := scanAccountRow(tx.QueryRowContext(
		ctx, accountSelect+` WHERE a.code = ?`, account.Code.String(),
	))
	if err != nil {
		return domain.Account{}, fmt.Errorf("store: read created account: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Account{}, fmt.Errorf("store: commit create account: %w", err)
	}
	return created, nil
}

// GetAccount returns the account with the given code.
func (r *realmStore) GetAccount(
	ctx context.Context, code domain.AccountID,
) (domain.Account, bool, error) {
	db, err := r.db()
	if err != nil {
		return domain.Account{}, false, err
	}
	row := db.QueryRowContext(
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
	page, err := r.ListAccountRows(ctx, fwstore.AccountListFilter{})
	if err != nil {
		return nil, err
	}
	accounts := make([]domain.Account, 0, len(page.Rows))
	for _, row := range page.Rows {
		accounts = append(accounts, row.Account)
	}
	return accounts, nil
}

// ListAccountRows returns accounts matching filter, ordered by code.
func (r *realmStore) ListAccountRows(
	ctx context.Context, filter fwstore.AccountListFilter,
) (fwstore.AccountListPage, error) {
	where, args := accountListWhere(filter)
	having, havingArgs := countHaving(
		"", fwstore.CountRangeFilter{}, "COUNT(b.asset_id)", filter.Position,
	)
	args = append(args, havingArgs...)
	from := `
FROM account a
LEFT JOIN asset ac ON ac.id = a.currency_asset_id
LEFT JOIN account_group g ON g.id = a.group_id
LEFT JOIN asset gc ON gc.id = g.currency_asset_id
LEFT JOIN account_group dg ON dg.code = ''
LEFT JOIN asset dc ON dc.id = dg.currency_asset_id
LEFT JOIN balance b ON b.account_id = a.id` +
		where + `
GROUP BY a.id` + having
	countQuery := `SELECT COUNT(*) FROM (SELECT a.id` + from + `) AS filtered`
	db, err := r.db()
	if err != nil {
		return fwstore.AccountListPage{}, err
	}
	var total int
	if err := db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return fwstore.AccountListPage{}, fmt.Errorf("store: count account rows: %w", err)
	}
	queryArgs := append([]any{}, args...)
	query := `
SELECT a.id, a.code, a.title, a.pnl, a.pnl_halt_reason, ac.code, g.code, gc.code, dc.code,
       a.notes, a.blocked, a.block_reason,
       COUNT(b.asset_id) AS position_count
` + from + accountListOrderBy(filter.Sort)
	if filter.Page.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		queryArgs = append(queryArgs, filter.Page.Limit, max(filter.Page.Offset, 0))
	}

	rows, err := db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return fwstore.AccountListPage{}, fmt.Errorf("store: list account rows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result, err := scanAccountListRows(rows)
	if err != nil {
		return fwstore.AccountListPage{}, err
	}
	return fwstore.AccountListPage{Rows: result, Total: total}, nil
}

// SetAccountBlocked updates the blocked flag and block reason of the account.
func (r *realmStore) SetAccountBlocked(
	ctx context.Context, code domain.AccountID, blocked bool, reason string,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
		ctx,
		`UPDATE account SET blocked = ?, block_reason = ? WHERE code = ?`,
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
	db, err := r.db()
	if err != nil {
		return err
	}
	groupID, err := optionalGroupID(ctx, db, groupCode)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
		ctx, `UPDATE account SET group_id = ? WHERE code = ?`,
		groupID, code.String(),
	)
	if err != nil {
		return fmt.Errorf("store: set account group: %w", err)
	}
	return notFoundIfNoRows(res, "account", code.String())
}

// SetAccountCurrency sets or clears the account-level currency asset.
func (r *realmStore) SetAccountCurrency(
	ctx context.Context, code domain.AccountID, currency string,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	assetID, err := nullableAssetID(ctx, db, currency)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
		ctx, `UPDATE account SET currency_asset_id = ? WHERE code = ?`,
		assetID, code.String(),
	)
	if err != nil {
		return fmt.Errorf("store: set account currency: %w", err)
	}
	return notFoundIfNoRows(res, "account", code.String())
}

// SetAccountPnl replaces the account P&L snapshot and its halt reason together.
// Both columns are written in one statement: the value and the halt are one
// engine-owned fact, and writing only one of them would publish a number the
// halt says is untrustworthy, or a halt over a stale value.
func (r *realmStore) SetAccountPnl(
	ctx context.Context,
	code domain.AccountID,
	pnl string,
	haltReason domain.PnlHaltReason,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	if pnl != "" {
		if _, err := domain.AddDecimals("", pnl); err != nil {
			return fmt.Errorf("store: account pnl %q: %w", code, err)
		}
	}
	if err := domain.ValidatePnlHaltReason(haltReason); err != nil {
		return fmt.Errorf("store: account pnl halt %q: %w", code, err)
	}
	res, err := db.ExecContext(
		ctx,
		`UPDATE account SET pnl = ?, pnl_halt_reason = ? WHERE code = ?`,
		pnl, haltReason, code.String(),
	)
	if err != nil {
		return fmt.Errorf("store: set account pnl: %w", err)
	}
	return notFoundIfNoRows(res, "account", code.String())
}

// SetAccountNotes replaces the notes of the account.
func (r *realmStore) SetAccountNotes(
	ctx context.Context, code domain.AccountID, notes string,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	res, err := db.ExecContext(
		ctx, `UPDATE account SET notes = ? WHERE code = ?`, notes, code.String(),
	)
	if err != nil {
		return fmt.Errorf("store: set account notes: %w", err)
	}
	return notFoundIfNoRows(res, "account", code.String())
}

// UpdateAccount replaces the public code and title of the identified account.
func (r *realmStore) UpdateAccount(
	ctx context.Context, oldCode domain.AccountID, account domain.Account,
) (domain.Account, error) {
	db, err := r.db()
	if err != nil {
		return domain.Account{}, err
	}
	res, err := db.ExecContext(
		ctx,
		`UPDATE account SET code = ?, title = ? WHERE code = ?`,
		account.Code.String(), account.Title, oldCode.String(),
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return domain.Account{},
				fmt.Errorf("account %q: %w", account.Code, domain.ErrAlreadyExists)
		}
		return domain.Account{}, fmt.Errorf("store: update account: %w", err)
	}
	if err := notFoundIfNoRows(res, "account", oldCode.String()); err != nil {
		return domain.Account{}, err
	}
	updated, ok, err := r.GetAccount(ctx, account.Code)
	if err != nil {
		return domain.Account{}, err
	}
	if !ok {
		return domain.Account{},
			fmt.Errorf("account %q: %w", account.Code, domain.ErrNotFound)
	}
	return updated, nil
}

// DeleteAccount removes the account and, when forced, cascades its dependents.
func (r *realmStore) DeleteAccount(
	ctx context.Context, code domain.AccountID, force bool,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
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
	res, err := tx.ExecContext(ctx, `DELETE FROM account WHERE id = ?`, accountID)
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

func scanAccountListRows(rows *sql.Rows) ([]fwstore.AccountListRow, error) {
	accounts := make([]fwstore.AccountListRow, 0)
	for rows.Next() {
		account, err := scanAccountListRow(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate account rows: %w", err)
	}
	return accounts, nil
}

func scanAccountListRow(rows *sql.Rows) (fwstore.AccountListRow, error) {
	var (
		account         domain.Account
		engineID        int64
		currency        sql.NullString
		groupCode       sql.NullString
		groupCurrency   sql.NullString
		defaultCurrency sql.NullString
		positionCount   int
	)
	if err := rows.Scan(
		&engineID, &account.Code, &account.Title, &account.Pnl, &account.PnlHaltReason, &currency,
		&groupCode, &groupCurrency, &defaultCurrency,
		&account.Notes, &account.Blocked, &account.BlockReason,
		&positionCount,
	); err != nil {
		return fwstore.AccountListRow{}, fmt.Errorf("store: scan account row: %w", err)
	}
	account.EngineAccountID = domain.EngineAccountID(engineID)
	account.GroupCode = groupCode.String
	setAccountCurrencyFields(&account, currency, groupCurrency, defaultCurrency)
	return fwstore.AccountListRow{Account: account, PositionCount: positionCount}, nil
}

func scanAccount(rows *sql.Rows) (domain.Account, error) {
	var (
		account         domain.Account
		engineID        int64
		currency        sql.NullString
		groupCode       sql.NullString
		groupCurrency   sql.NullString
		defaultCurrency sql.NullString
	)
	if err := rows.Scan(
		&engineID, &account.Code, &account.Title, &account.Pnl, &account.PnlHaltReason, &currency,
		&groupCode, &groupCurrency, &defaultCurrency,
		&account.Notes, &account.Blocked, &account.BlockReason,
	); err != nil {
		return domain.Account{}, fmt.Errorf("store: scan account: %w", err)
	}
	account.EngineAccountID = domain.EngineAccountID(engineID)
	account.GroupCode = groupCode.String
	setAccountCurrencyFields(&account, currency, groupCurrency, defaultCurrency)
	return account, nil
}

// accountEffectiveBlockedExpr is an account's effective kill-switch state: its
// own latched block OR the block of the group it belongs to. The engine rejects
// every order from a member of a blocked group, so the account list's status
// axis resolves the same join instead of reading a.blocked alone. It relies on
// the group LEFT JOIN present in every account query.
const accountEffectiveBlockedExpr = `(CASE
    WHEN a.blocked = 1 OR COALESCE(g.blocked, 0) = 1 THEN 1 ELSE 0 END)`

// accountEffectiveBlockReasonExpr is the reason behind that effective state,
// under the same attribution the domain applies: the account's own block wins,
// otherwise the group's reason answers. The account DTO publishes this reason,
// so filtering or sorting on a.block_reason alone would hide a group-blocked
// account from a search for the very text its own row displays.
const accountEffectiveBlockReasonExpr = `(CASE
    WHEN a.blocked = 1 THEN a.block_reason
    WHEN COALESCE(g.blocked, 0) = 1 THEN COALESCE(g.block_reason, '')
    ELSE '' END)`

func accountListWhere(filter fwstore.AccountListFilter) (string, []any) {
	clauses := make([]string, 0)
	args := make([]any, 0)
	appendMatcherAny(&clauses, &args, []string{"a.code", "a.title"}, filter.Code)
	appendMatcher(&clauses, &args, accountEffectiveBlockReasonExpr, filter.BlockReason)
	appendStatusFilter(&clauses, filter.Status, accountEffectiveBlockedExpr)
	if filter.GroupCode != nil {
		if *filter.GroupCode == "" {
			clauses = append(clauses, "a.group_id IS NULL")
		} else {
			clauses = append(clauses, "g.code = ?")
			args = append(args, *filter.GroupCode)
		}
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "\nWHERE " + strings.Join(clauses, " AND "), args
}

func accountListOrderBy(sort fwstore.SortSpec) string {
	columns := map[string]string{
		// Both block axes order by the same effective state the filters and the
		// account DTO report, so a sorted page cannot disagree with its badges.
		"blockReason":   accountEffectiveBlockReasonExpr,
		"code":          "a.code",
		"group":         "g.code",
		"positionCount": "position_count",
		"status":        accountEffectiveBlockedExpr,
		"title":         "a.title",
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
	return "\nORDER BY " + column + " " + direction + ", a.code " + tieDirection
}

func groupListOrderBy(sort fwstore.SortSpec) string {
	columns := map[string]string{
		"accountCount":  "account_count",
		"blockReason":   "g.block_reason",
		"code":          "g.code",
		"notes":         "g.notes",
		"positionCount": "position_count",
		"status":        "g.blocked",
		"title":         "g.title",
	}
	column := columns[sort.Column]
	if column == "" {
		column = "g.code"
	}
	direction := "ASC"
	tieDirection := "ASC"
	if sort.Descending {
		direction = "DESC"
		tieDirection = "DESC"
	}
	return "\nORDER BY " + column + " " + direction + ", g.code " + tieDirection
}

func groupListWhere(filter fwstore.GroupListFilter, tableAlias string) (string, []any) {
	clauses := make([]string, 0)
	args := make([]any, 0)
	appendMatcherAny(
		&clauses, &args,
		[]string{tableAlias + ".code", tableAlias + ".title"}, filter.Code,
	)
	appendMatcher(&clauses, &args, tableAlias+".notes", filter.Notes)
	appendMatcher(&clauses, &args, tableAlias+".block_reason", filter.BlockReason)
	appendStatusFilter(&clauses, filter.Status, tableAlias+".blocked")
	if len(clauses) == 0 {
		return "", args
	}
	return "\nWHERE " + strings.Join(clauses, " AND "), args
}

func realGroupWhere(where string) string {
	if where == "" {
		return "\nWHERE g.code <> ''"
	}
	return where + " AND g.code <> ''"
}

func appendMatcher(
	clauses *[]string, args *[]any, column string, matcher fwstore.TextMatcher,
) {
	pattern, ok := likePattern(matcher)
	if !ok {
		return
	}
	*clauses = append(*clauses, column+" LIKE ? ESCAPE '\\'")
	*args = append(*args, pattern)
}

// appendMatcherAny adds one OR'd LIKE clause across columns, so a single search
// term matches any of them (e.g. an account/group code or its title).
func appendMatcherAny(
	clauses *[]string, args *[]any, columns []string, matcher fwstore.TextMatcher,
) {
	pattern, ok := likePattern(matcher)
	if !ok {
		return
	}
	ors := make([]string, 0, len(columns))
	for _, column := range columns {
		ors = append(ors, column+" LIKE ? ESCAPE '\\'")
		*args = append(*args, pattern)
	}
	*clauses = append(*clauses, "("+strings.Join(ors, " OR ")+")")
}

func appendStatusFilter(
	clauses *[]string, status fwstore.StatusFilter, column string,
) {
	switch status {
	case fwstore.StatusFilterActive:
		*clauses = append(*clauses, column+" = 0")
	case fwstore.StatusFilterBlocked:
		*clauses = append(*clauses, column+" = 1")
	}
}

func countHaving(
	accountExpr string,
	accountFilter fwstore.CountRangeFilter,
	positionExpr string,
	positionFilter fwstore.CountRangeFilter,
) (string, []any) {
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 2)
	appendCountRangeFilter(&clauses, &args, accountExpr, accountFilter)
	appendCountRangeFilter(&clauses, &args, positionExpr, positionFilter)
	if len(clauses) == 0 {
		return "", nil
	}
	return "\nHAVING " + strings.Join(clauses, " AND "), args
}

func appendCountRangeFilter(
	clauses *[]string,
	args *[]any,
	expr string,
	filter fwstore.CountRangeFilter,
) {
	if expr == "" || filter.Empty() {
		return
	}
	if filter.Equal != nil {
		*clauses = append(*clauses, expr+" = ?")
		*args = append(*args, *filter.Equal)
	}
	if filter.NotEqual != nil {
		*clauses = append(*clauses, expr+" <> ?")
		*args = append(*args, *filter.NotEqual)
	}
	if filter.Min != nil {
		if filter.MinExclusive {
			*clauses = append(*clauses, expr+" > ?")
		} else {
			*clauses = append(*clauses, expr+" >= ?")
		}
		*args = append(*args, *filter.Min)
	}
	if filter.Max != nil {
		if filter.MaxExclusive {
			*clauses = append(*clauses, expr+" < ?")
		} else {
			*clauses = append(*clauses, expr+" <= ?")
		}
		*args = append(*args, *filter.Max)
	}
}

func likePattern(matcher fwstore.TextMatcher) (string, bool) {
	fragments := make([]string, 0, len(matcher.Fragments))
	for _, fragment := range matcher.Fragments {
		if fragment != "" {
			fragments = append(fragments, escapeLike(fragment))
		}
	}
	if len(fragments) == 0 {
		return "", false
	}
	var b strings.Builder
	if !matcher.AnchorStart {
		b.WriteByte('%')
	}
	for i, fragment := range fragments {
		if i > 0 {
			b.WriteByte('%')
		}
		b.WriteString(fragment)
	}
	if !matcher.AnchorEnd {
		b.WriteByte('%')
	}
	return b.String(), true
}

func escapeLike(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch r {
		case '\\', '%', '_':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func scanAccountRow(row *sql.Row) (domain.Account, error) {
	var (
		account         domain.Account
		engineID        int64
		currency        sql.NullString
		groupCode       sql.NullString
		groupCurrency   sql.NullString
		defaultCurrency sql.NullString
	)
	if err := row.Scan(
		&engineID, &account.Code, &account.Title, &account.Pnl, &account.PnlHaltReason, &currency,
		&groupCode, &groupCurrency, &defaultCurrency,
		&account.Notes, &account.Blocked, &account.BlockReason,
	); err != nil {
		return domain.Account{}, err
	}
	account.EngineAccountID = domain.EngineAccountID(engineID)
	account.GroupCode = groupCode.String
	setAccountCurrencyFields(&account, currency, groupCurrency, defaultCurrency)
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
// string to itself. It backs optional text columns.
func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// optionalClassID resolves an optional asset-class code to a nullable surrogate
// id: an empty code yields a NULL class link; a non-empty unknown code wraps
// domain.ErrInvalid. It mirrors optionalGroupID.
func optionalClassID(
	ctx context.Context, q sqlQueryer, code string,
) (sql.NullInt64, error) {
	if code == "" {
		return sql.NullInt64{}, nil
	}
	id, err := resolveAssetClassID(ctx, q, code)
	if err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: id, Valid: true}, nil
}

func nullableAssetID(
	ctx context.Context, q sqlQueryer, code string,
) (sql.NullInt64, error) {
	if code == "" {
		return sql.NullInt64{}, nil
	}
	id, err := resolveAssetID(ctx, q, code)
	if err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: id, Valid: true}, nil
}

func setAccountCurrencyFields(
	account *domain.Account,
	currency sql.NullString,
	groupCurrency sql.NullString,
	defaultCurrency sql.NullString,
) {
	account.Currency = currency.String
	account.GroupCurrency = groupCurrency.String
	account.DefaultCurrency = defaultCurrency.String
	account.EffectiveCurrency, account.CurrencyOrigin =
		domain.ResolveCurrencyCascade(
			account.Currency,
			account.GroupCurrency,
			account.DefaultCurrency,
		)
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
		{"balance", `SELECT COUNT(*) FROM balance WHERE account_id = ?`},
		{"order_record", `SELECT COUNT(*) FROM order_record WHERE account_id = ?`},
		{"order_event", `SELECT COUNT(*) FROM order_event ev
		 JOIN order_record o ON o.id = ev.order_id WHERE o.account_id = ?`},
		{"trade", `SELECT COUNT(*) FROM trade WHERE account_id = ?`},
		{"event_attestation", `SELECT COUNT(*) FROM event_attestation ea
		 JOIN order_event ev ON ev.id = ea.event_id
		 JOIN order_record o ON o.id = ev.order_id WHERE o.account_id = ?`},
		{"adjustment", `SELECT COUNT(*) FROM adjustment WHERE account_id = ?`},
		{"limit_rate", `SELECT COUNT(*) FROM limit_rate WHERE account_id = ?`},
		{"limit_order_size", `SELECT COUNT(*) FROM limit_order_size WHERE account_id = ?`},
		{"limit_spot_funds_pnl_bound", `SELECT COUNT(*)
		 FROM limit_spot_funds_pnl_bound WHERE account_id = ?`},
	}
	return collectDependents(ctx, q, checks, accountID)
}

func assetDependents(
	ctx context.Context, q sqlQueryer, assetID int64,
) ([]domain.DependentCount, error) {
	checks := []dependentQuery{
		{"balance", `SELECT COUNT(*) FROM balance WHERE asset_id = ?`},
		{"adjustment", `SELECT COUNT(*) FROM adjustment WHERE asset_id = ?`},
		{"limit_rate", `SELECT COUNT(*) FROM limit_rate WHERE asset_id = ?`},
		{"limit_order_size", `SELECT COUNT(*) FROM limit_order_size WHERE asset_id = ?`},
		{"order_record", `SELECT COUNT(*) FROM order_record
		 WHERE base_asset_id = ? OR quote_asset_id = ?`},
		{"trade", `SELECT COUNT(*) FROM trade
		 WHERE base_asset_id = ? OR quote_asset_id = ?`},
		{"market_data_instrument", `SELECT COUNT(*) FROM market_data_instrument
		 WHERE base_asset_id = ? OR quote_asset_id = ?`},
	}
	return collectDependents(ctx, q, checks, assetID)
}

func assetCurrencyDependents(
	ctx context.Context, q sqlQueryer, assetID int64,
) ([]domain.DependentCount, error) {
	checks := []dependentQuery{
		{"account_currency", `SELECT COUNT(*) FROM account WHERE currency_asset_id = ?`},
		{"account_group_currency", `SELECT COUNT(*) FROM account_group WHERE currency_asset_id = ?`},
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
