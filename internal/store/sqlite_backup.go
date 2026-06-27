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

// Backup group of the SQLite store: the realm-portable ExportBackup and
// RestoreBackup. An archive is one realm's portable form. Export reads the bound
// realm and emits portable rows - dictionaries by code (plus title), machine
// records by external id, every cross-row link by code or external id, never a
// surrogate or engine id. Restore lands an archive into the bound realm (the same
// path serves an isolated single-realm database and a shared multi-realm one,
// because the connector binds the schema): it inserts dictionaries first so
// foreign keys resolve, lets the connector ASSIGN fresh, collision-free surrogate
// and engine ids in the target while PRESERVING the archive's code and external id
// values, then inserts machine records whose cross-row links are resolved through
// those preserved identities. The whole restore runs in one transaction so a
// failure leaves the target realm untouched; a touched-runtime restore raises the
// engine-rebuild signal in the returned summary.

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
)

// backupSource labels the producing installation in the manifest. It is operator
// context only and carries no identity.
const backupSource = "pit-officer"

// ExportBackup reads the bound realm and emits a portable archive holding only
// the rows scope includes. It gathers the full realm into a portable Data, then
// hands it to backup.NewArchive, which narrows it to the scope. Every row is
// serialized by portable identity; no surrogate key or engine id is emitted.
func (r *realmStore) ExportBackup(
	ctx context.Context, scope backup.Scope,
) (backup.Archive, error) {
	label, err := r.exportRealmLabel(ctx)
	if err != nil {
		return backup.Archive{}, err
	}
	data, err := r.exportData(ctx)
	if err != nil {
		return backup.Archive{}, err
	}
	return backup.NewArchive(time.Now(), backupSource, label, scope, data), nil
}

// exportRealmLabel reads the single realm identity row's portable label (code and
// title). The realm's external id is deliberately not carried: it is the realm
// row's machine identity in this schema and is re-established by the target
// connector on restore.
func (r *realmStore) exportRealmLabel(ctx context.Context) (backup.RealmLabel, error) {
	var label backup.RealmLabel
	err := r.db().QueryRowContext(
		ctx, `SELECT code, title FROM realm LIMIT 1`,
	).Scan(&label.Code, &label.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return backup.RealmLabel{Code: r.store.realm.String()}, nil
	}
	if err != nil {
		return backup.RealmLabel{}, fmt.Errorf("store: export realm label: %w", err)
	}
	return label, nil
}

// exportData gathers every portable row of the realm. The order of the fields
// mirrors the dictionary-first restore order; FilterData later trims to scope.
func (r *realmStore) exportData(ctx context.Context) (backup.Data, error) {
	var (
		data backup.Data
		err  error
	)
	if data.Assets, err = r.ListAssets(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.Principals, err = r.ListPrincipals(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.Groups, err = r.exportGroups(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.Accounts, err = r.exportAccounts(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.Balances, err = r.ListBalances(ctx, "", ""); err != nil {
		return backup.Data{}, err
	}
	if data.RateLimits, err = r.ListRateLimits(ctx, ""); err != nil {
		return backup.Data{}, err
	}
	if data.OrderSizeLimits, err = r.ListOrderSizeLimits(ctx, ""); err != nil {
		return backup.Data{}, err
	}
	if data.PnlBoundsLimits, err = r.ListPnlBoundsLimits(ctx, ""); err != nil {
		return backup.Data{}, err
	}
	if data.Adjustments, err = r.exportAdjustments(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.Orders, err = r.exportOrders(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.OrderEvents, err = r.exportOrderEvents(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.Trades, err = r.ListAllTrades(ctx, "", ""); err != nil {
		return backup.Data{}, err
	}
	if data.Audit, err = r.exportAudit(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.MarketDataInstances, err = r.ListMarketDataInstances(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.MarketDataInstruments, err = r.exportInstruments(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.MarketDataQuotes, err = r.ListMarketDataQuotes(ctx, domain.ExternalID{}); err != nil {
		return backup.Data{}, err
	}
	if data.SigningKeys, err = r.exportSigningKeys(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.SigningConfig, err = r.exportSigningConfig(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.McpAccess, err = r.ListMcpAccess(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.UserSettings, err = r.ListUserSettings(ctx); err != nil {
		return backup.Data{}, err
	}
	return data, nil
}

// exportGroups lists every group as a portable archive group (engine id dropped).
func (r *realmStore) exportGroups(ctx context.Context) ([]backup.AccountGroup, error) {
	groups, err := r.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]backup.AccountGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, backup.AccountGroup{
			Code:        g.Code,
			Title:       g.Title,
			Notes:       g.Notes,
			BlockReason: g.BlockReason,
			Blocked:     g.Blocked,
		})
	}
	return out, nil
}

// exportAccounts lists every account as a portable archive account (engine id
// dropped, group link kept by code).
func (r *realmStore) exportAccounts(ctx context.Context) ([]backup.Account, error) {
	accounts, err := r.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]backup.Account, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, backup.Account{
			Code:        a.Code.String(),
			Title:       a.Title,
			GroupCode:   a.GroupCode,
			Notes:       a.Notes,
			BlockReason: a.BlockReason,
			Blocked:     a.Blocked,
		})
	}
	return out, nil
}

// exportAdjustments scans the whole adjustments table, oldest first, so a backup
// captures the full history rather than a UI page. It uses the adjustments-group
// projection and scan helper directly to avoid a per-account, limit-bounded read.
func (r *realmStore) exportAdjustments(
	ctx context.Context,
) ([]domain.AccountAdjustmentRecord, error) {
	rows, err := r.db().QueryContext(
		ctx, adjustmentSelect+` ORDER BY adj.at ASC, adj.id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: export adjustments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]domain.AccountAdjustmentRecord, 0)
	for rows.Next() {
		rec, err := scanAdjustment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate adjustments for export: %w", err)
	}
	return out, nil
}

// exportOrders lists every order across all accounts (no UI limit) and folds in
// each order's optional 1:1 approval. Events and trades travel in their own
// sections, linked back by the order's external id.
func (r *realmStore) exportOrders(ctx context.Context) ([]backup.OrderRecord, error) {
	orders, err := r.ListAllOrders(ctx, "", "")
	if err != nil {
		return nil, err
	}
	out := make([]backup.OrderRecord, 0, len(orders))
	for _, o := range orders {
		rec := backup.OrderRecord{Order: o}
		if env, ok, aerr := r.getOrderApproval(ctx, o.ExternalID); aerr != nil {
			return nil, aerr
		} else if ok {
			env := env
			rec.Approval = &env
		}
		out = append(out, rec)
	}
	return out, nil
}

// exportOrderEvents lists every order's events, oldest first within each order,
// linked to the parent by the order's external id.
func (r *realmStore) exportOrderEvents(ctx context.Context) ([]domain.OrderEvent, error) {
	orders, err := r.ListAllOrders(ctx, "", "")
	if err != nil {
		return nil, err
	}
	out := make([]domain.OrderEvent, 0)
	for _, o := range orders {
		events, err := r.ListOrderEvents(ctx, o.ExternalID)
		if err != nil {
			return nil, err
		}
		out = append(out, events...)
	}
	return out, nil
}

// exportAudit scans the whole audit trail, oldest first, so the archive restores
// in the order rows were recorded. It uses the audit-group projection and scan
// helper directly to avoid a limit-bounded, newest-first UI read.
func (r *realmStore) exportAudit(ctx context.Context) ([]domain.AuditRow, error) {
	rows, err := r.db().QueryContext(
		ctx, auditSelect+` ORDER BY au.at ASC, au.id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: export audit: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]domain.AuditRow, 0)
	for rows.Next() {
		row, err := scanAuditRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate audit for export: %w", err)
	}
	return out, nil
}

// exportInstruments lists every instrument of every instance, linked back to its
// instance by the instance external id.
func (r *realmStore) exportInstruments(
	ctx context.Context,
) ([]domain.MarketDataInstrument, error) {
	instances, err := r.ListMarketDataInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.MarketDataInstrument, 0)
	for _, inst := range instances {
		instruments, err := r.ListMarketDataInstruments(ctx, inst.ExternalID)
		if err != nil {
			return nil, err
		}
		out = append(out, instruments...)
	}
	return out, nil
}

// exportSigningKeys reads every signing key WITH its private material so the keys
// round-trip; the listing API omits private keys, so this is a dedicated read.
func (r *realmStore) exportSigningKeys(ctx context.Context) ([]backup.SigningKey, error) {
	rows, err := r.db().QueryContext(
		ctx,
		`SELECT key_id, alg, private_key, public_key, created_at, active
		 FROM signing_keys ORDER BY created_at ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: export signing keys: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]backup.SigningKey, 0)
	for rows.Next() {
		var (
			key                   backup.SigningKey
			privateKey, publicKey []byte
			createdAt             string
		)
		if err := rows.Scan(
			&key.KeyID, &key.Alg, &privateKey, &publicKey, &createdAt, &key.Active,
		); err != nil {
			return nil, fmt.Errorf("store: scan signing key for export: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("store: parse signing key created_at %q: %w", createdAt, err)
		}
		key.PrivateKey = privateKey
		key.PublicKey = publicKey
		key.CreatedAt = parsed
		out = append(out, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate signing keys for export: %w", err)
	}
	return out, nil
}

// exportSigningConfig reads the whole signing-config table as portable entries.
func (r *realmStore) exportSigningConfig(
	ctx context.Context,
) ([]backup.SigningConfigEntry, error) {
	rows, err := r.db().QueryContext(
		ctx, `SELECT key, value FROM signing_config ORDER BY key`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: export signing config: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]backup.SigningConfigEntry, 0)
	for rows.Next() {
		var entry backup.SigningConfigEntry
		if err := rows.Scan(&entry.Key, &entry.Value); err != nil {
			return nil, fmt.Errorf("store: scan signing config: %w", err)
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate signing config: %w", err)
	}
	return out, nil
}

// --- Restore ----------------------------------------------------------------

// RestoreBackup imports archive into the bound realm under opts in a single
// transaction. Dictionaries land first (assigning fresh surrogate and engine ids
// while preserving code/external id), then the account-addressed facts whose
// cross-row links resolve through the freshly inserted dictionaries. The restore
// honors opts.Mode and the scope selectors and reports per-section counts; the
// returned summary's RestartRequired is the engine-rebuild signal for a
// touched-runtime restore. A failure rolls the whole transaction back, leaving
// the target realm untouched.
func (r *realmStore) RestoreBackup(
	ctx context.Context, archive backup.Archive, opts backup.RestoreOptions,
) (backup.RestoreSummary, error) {
	if archive.Manifest.FormatVersion != backup.FormatVersion {
		return backup.RestoreSummary{}, fmt.Errorf(
			"backup format version %d unsupported, want %d: %w",
			archive.Manifest.FormatVersion, backup.FormatVersion, domain.ErrInvalid)
	}
	mode, err := validateRestoreMode(opts.Mode)
	if err != nil {
		return backup.RestoreSummary{}, err
	}
	scope := opts.Scope.Normalize()
	// Re-narrow the archive to the restore scope so a caller may restore a subset
	// of a full archive; the archive already filtered to its own scope at export.
	data := backup.FilterData(archive.Data, scope)

	tx, err := r.db().BeginTx(ctx, nil)
	if err != nil {
		return backup.RestoreSummary{}, fmt.Errorf("store: begin restore: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	summary := backup.NewSummary()
	rt := &restoreTx{tx: tx, mode: mode, summary: &summary}

	if err := rt.run(ctx, scope, data); err != nil {
		return backup.RestoreSummary{}, err
	}
	if err := tx.Commit(); err != nil {
		return backup.RestoreSummary{}, fmt.Errorf("store: commit restore: %w", err)
	}

	// Evaluate the restart signal against the caller's requested scope, not the
	// force-included parent dictionaries Normalize adds for FK resolution: an
	// audit-only or activity-history-only restore must stay observational.
	summary.RestartRequired = backup.TouchesRuntime(opts.Scope)
	return summary, nil
}

// validateRestoreMode rejects an empty or unknown restore mode with ErrInvalid.
func validateRestoreMode(mode backup.RestoreMode) (backup.RestoreMode, error) {
	switch mode {
	case backup.RestoreModeReplaceAll,
		backup.RestoreModeOverwrite,
		backup.RestoreModeInsertMissing:
		return mode, nil
	default:
		return "", fmt.Errorf("store: unknown restore mode %q: %w", mode, domain.ErrInvalid)
	}
}

// restoreTx carries the in-flight restore transaction, the mode and the running
// per-section summary so the per-table insert helpers stay small.
type restoreTx struct {
	tx      *sql.Tx
	mode    backup.RestoreMode
	summary *backup.RestoreSummary
}

// run inserts every included section in dictionary-first order. Assets and
// principals always travel (FilterData carries them unconditionally) so the
// machine-record foreign keys resolve regardless of which sections the scope
// selected.
func (rt *restoreTx) run(ctx context.Context, scope backup.Scope, data backup.Data) error {
	// Replace-all makes each included section equal the archive: before any
	// insert, delete the in-scope rows the archive does not carry. Children are
	// removed first (or by FK cascade) so no delete orphans a row or violates a
	// constraint; the inserts that follow re-create the archive's rows. Accounts
	// and groups preserve engine ids when updated in place; orders are
	// INSERT OR REPLACE rows, so replacing one may assign a fresh rowid.
	if rt.mode == backup.RestoreModeReplaceAll {
		if err := rt.prune(ctx, scope, data); err != nil {
			return err
		}
	}
	if err := rt.restoreAssets(ctx, data.Assets); err != nil {
		return err
	}
	if err := rt.restorePrincipals(ctx, data.Principals); err != nil {
		return err
	}
	if scope.Included(backup.SectionAccountsGroups) {
		if err := rt.restoreGroups(ctx, data.Groups); err != nil {
			return err
		}
		if err := rt.restoreAccounts(ctx, data.Accounts); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionPositions) {
		if err := rt.restoreBalances(ctx, data.Balances); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionRiskLimits) {
		if err := rt.restoreLimits(ctx, data); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionMarketData) {
		if err := rt.restoreMarketData(ctx, data); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionMarketDataQuotes) {
		if err := rt.restoreQuotes(ctx, data.MarketDataQuotes); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionGeneralSettings) {
		if err := rt.restoreGeneralSettings(ctx, data); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionUserSettings) {
		if err := rt.restoreUserSettings(ctx, data.UserSettings); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionActivityHistory) {
		if err := rt.restoreActivity(ctx, data); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionAuditLog) {
		if err := rt.restoreAudit(ctx, data.Audit); err != nil {
			return err
		}
	}
	return nil
}

// --- Restore: dictionaries --------------------------------------------------

func (rt *restoreTx) restoreAssets(ctx context.Context, assets []domain.Asset) error {
	for _, a := range assets {
		exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM assets WHERE code = ?`, a.Code)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionAccountsGroups, exists) {
			continue
		}
		if exists {
			if _, err := rt.tx.ExecContext(
				ctx,
				`UPDATE assets SET title = ?, asset_class = ? WHERE code = ?`,
				a.Title, nullableString(a.AssetClass), a.Code,
			); err != nil {
				return fmt.Errorf("store: restore asset %q: %w", a.Code, err)
			}
		} else if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT INTO assets (code, title, asset_class) VALUES (?, ?, ?)`,
			a.Code, a.Title, nullableString(a.AssetClass),
		); err != nil {
			return fmt.Errorf("store: restore asset %q: %w", a.Code, err)
		}
		// Assets are dictionary support rows for every section; their counts roll
		// into the accounts/groups section so the summary stays section-shaped.
		rt.summary.AddApplied(backup.SectionAccountsGroups, 1)
	}
	return nil
}

func (rt *restoreTx) restorePrincipals(ctx context.Context, principals []domain.Principal) error {
	for _, p := range principals {
		exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM principals WHERE code = ?`, p.Code)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionAccountsGroups, exists) {
			continue
		}
		if exists {
			if _, err := rt.tx.ExecContext(
				ctx, `UPDATE principals SET title = ? WHERE code = ?`, p.Title, p.Code,
			); err != nil {
				return fmt.Errorf("store: restore principal %q: %w", p.Code, err)
			}
		} else if _, err := rt.tx.ExecContext(
			ctx, `INSERT INTO principals (code, title) VALUES (?, ?)`, p.Code, p.Title,
		); err != nil {
			return fmt.Errorf("store: restore principal %q: %w", p.Code, err)
		}
		rt.summary.AddApplied(backup.SectionAccountsGroups, 1)
	}
	return nil
}

// restoreGroups inserts the account groups, assigning a fresh engine group id per
// new row while preserving the code. On overwrite of an existing group the
// engine id is kept (re-assigning it would force an engine-tree churn that the
// rebuild already covers, and the code is the portable handle).
func (rt *restoreTx) restoreGroups(ctx context.Context, groups []backup.AccountGroup) error {
	for _, g := range groups {
		exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM account_groups WHERE code = ?`, g.Code)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionAccountsGroups, exists) {
			continue
		}
		if exists {
			if _, err := rt.tx.ExecContext(
				ctx,
				`UPDATE account_groups
				 SET title = ?, notes = ?, blocked = ?, block_reason = ?
				 WHERE code = ?`,
				g.Title, g.Notes, g.Blocked, g.BlockReason, g.Code,
			); err != nil {
				return fmt.Errorf("store: restore group %q: %w", g.Code, err)
			}
		} else {
			engineID, err := nextEngineGroupID(ctx, rt.tx)
			if err != nil {
				return err
			}
			if _, err := rt.tx.ExecContext(
				ctx,
				`INSERT INTO account_groups
				 (engine_group_id, code, title, notes, blocked, block_reason)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				int64(engineID.Uint32()), g.Code, g.Title, g.Notes, g.Blocked, g.BlockReason,
			); err != nil {
				return fmt.Errorf("store: restore group %q: %w", g.Code, err)
			}
		}
		rt.summary.AddApplied(backup.SectionAccountsGroups, 1)
	}
	return nil
}

// restoreAccounts inserts the accounts, resolving the optional group link by
// group code (the group is already inserted) and assigning a fresh engine account
// id per new row while preserving the code.
func (rt *restoreTx) restoreAccounts(ctx context.Context, accounts []backup.Account) error {
	for _, a := range accounts {
		groupID, err := optionalGroupID(ctx, rt.tx, a.GroupCode)
		if err != nil {
			return err
		}
		exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM accounts WHERE code = ?`, a.Code)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionAccountsGroups, exists) {
			continue
		}
		if exists {
			if _, err := rt.tx.ExecContext(
				ctx,
				`UPDATE accounts
				 SET title = ?, group_id = ?, notes = ?, blocked = ?, block_reason = ?
				 WHERE code = ?`,
				a.Title, groupID, a.Notes, a.Blocked, a.BlockReason, a.Code,
			); err != nil {
				return fmt.Errorf("store: restore account %q: %w", a.Code, err)
			}
		} else {
			engineID, err := nextEngineAccountID(ctx, rt.tx)
			if err != nil {
				return err
			}
			if _, err := rt.tx.ExecContext(
				ctx,
				`INSERT INTO accounts
				 (engine_account_id, code, title, group_id, notes, blocked, block_reason)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				int64(engineID.Uint64()), a.Code, a.Title, groupID,
				a.Notes, a.Blocked, a.BlockReason,
			); err != nil {
				return fmt.Errorf("store: restore account %q: %w", a.Code, err)
			}
		}
		rt.summary.AddApplied(backup.SectionAccountsGroups, 1)
	}
	return nil
}

// --- Restore: positions -----------------------------------------------------

func (rt *restoreTx) restoreBalances(ctx context.Context, balances []domain.Balance) error {
	for _, b := range balances {
		accountID, err := resolveAccountID(ctx, rt.tx, b.Account)
		if err != nil {
			return err
		}
		assetID, err := resolveAssetID(ctx, rt.tx, b.Asset)
		if err != nil {
			return err
		}
		exists, err := rowExists(
			ctx, rt.tx,
			`SELECT 1 FROM balances WHERE account_id = ? AND asset_id = ?`,
			accountID, assetID,
		)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionPositions, exists) {
			continue
		}
		updatedAt := b.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = time.Now().UTC()
		}
		if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT OR REPLACE INTO balances
			 (account_id, asset_id, available, held, incoming, realized_pnl,
			  average_entry_price, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			accountID, assetID,
			settleOrZero(b.Available), settleOrZero(b.Held), settleOrZero(b.Incoming),
			settleOrZero(b.RealizedPnl), b.AverageEntryPrice,
			updatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("store: restore balance %q/%q: %w", b.Account, b.Asset, err)
		}
		rt.summary.AddApplied(backup.SectionPositions, 1)
	}
	return nil
}

// --- Restore: limits --------------------------------------------------------

func (rt *restoreTx) restoreLimits(ctx context.Context, data backup.Data) error {
	for _, l := range data.RateLimits {
		accountID, assetID, err := resolveLimitAxes(ctx, rt.tx, l.Account, l.Asset)
		if err != nil {
			return err
		}
		applied, err := rt.putLimit(ctx, "limit_rate", l.Scope, accountID, assetID,
			`INSERT INTO limit_rate (scope, account_id, asset_id, max_orders, window)
			 VALUES (?, ?, ?, ?, ?)`,
			[]any{l.Scope, accountID, assetID, int64(l.MaxOrders), l.Window.String()},
		)
		if err != nil {
			return err
		}
		rt.summary.AddApplied(backup.SectionRiskLimits, applied)
	}
	for _, l := range data.OrderSizeLimits {
		accountID, assetID, err := resolveLimitAxes(ctx, rt.tx, l.Account, l.Asset)
		if err != nil {
			return err
		}
		applied, err := rt.putLimit(ctx, "limit_order_size", l.Scope, accountID, assetID,
			`INSERT INTO limit_order_size (scope, account_id, asset_id, max_quantity, max_notional)
			 VALUES (?, ?, ?, ?, ?)`,
			[]any{l.Scope, accountID, assetID,
				nullableString(l.MaxQuantity), nullableString(l.MaxNotional)},
		)
		if err != nil {
			return err
		}
		rt.summary.AddApplied(backup.SectionRiskLimits, applied)
	}
	for _, l := range data.PnlBoundsLimits {
		accountID, assetID, err := resolveLimitAxes(ctx, rt.tx, l.Account, l.Asset)
		if err != nil {
			return err
		}
		applied, err := rt.putLimit(ctx, "limit_pnl_bounds", l.Scope, accountID, assetID,
			`INSERT INTO limit_pnl_bounds
			 (scope, account_id, asset_id, lower_bound, upper_bound, initial_pnl)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{l.Scope, accountID, assetID,
				nullableString(l.LowerBound), nullableString(l.UpperBound),
				nullableString(l.InitialPnl)},
		)
		if err != nil {
			return err
		}
		rt.summary.AddApplied(backup.SectionRiskLimits, applied)
	}
	return nil
}

// putLimit applies one limit row under the restore mode. A limit is keyed by its
// (scope, account, asset) composite; insert-missing skips an existing composite,
// overwrite and replace-all delete-then-insert it (delete-then-insert is required
// because SQLite treats NULL axes as distinct in the UNIQUE constraint). Returns
// the number of rows applied (0 when skipped).
func (rt *restoreTx) putLimit(
	ctx context.Context,
	table, scope string,
	accountID, assetID sql.NullInt64,
	insertSQL string,
	insertArgs []any,
) (int, error) {
	exists, err := rowExists(
		ctx, rt.tx,
		`SELECT 1 FROM `+table+` WHERE scope = ? AND account_id IS ? AND asset_id IS ?`,
		scope, accountID, assetID,
	)
	if err != nil {
		return 0, err
	}
	if rt.skip(backup.SectionRiskLimits, exists) {
		return 0, nil
	}
	if _, err := rt.tx.ExecContext(
		ctx,
		`DELETE FROM `+table+` WHERE scope = ? AND account_id IS ? AND asset_id IS ?`,
		scope, accountID, assetID,
	); err != nil {
		return 0, fmt.Errorf("store: restore delete %s: %w", table, err)
	}
	if _, err := rt.tx.ExecContext(ctx, insertSQL, insertArgs...); err != nil {
		return 0, fmt.Errorf("store: restore insert %s: %w", table, err)
	}
	return 1, nil
}

// --- Restore: market data ---------------------------------------------------

func (rt *restoreTx) restoreMarketData(ctx context.Context, data backup.Data) error {
	for _, inst := range data.MarketDataInstances {
		exists, err := rowExists(
			ctx, rt.tx,
			`SELECT 1 FROM market_data_instances WHERE external_id = ?`,
			inst.ExternalID.Bytes(),
		)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionMarketData, exists) {
			continue
		}
		if exists {
			if _, err := rt.tx.ExecContext(
				ctx,
				`UPDATE market_data_instances
				 SET provider = ?, label = ?, credentials = ?, enabled = ?
				 WHERE external_id = ?`,
				inst.Provider, inst.Label, inst.Credentials, inst.Enabled,
				inst.ExternalID.Bytes(),
			); err != nil {
				return fmt.Errorf("store: restore md instance %q: %w", inst.ExternalID, err)
			}
		} else if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT INTO market_data_instances
			 (external_id, provider, label, credentials, enabled)
			 VALUES (?, ?, ?, ?, ?)`,
			inst.ExternalID.Bytes(), inst.Provider, inst.Label,
			inst.Credentials, inst.Enabled,
		); err != nil {
			return fmt.Errorf("store: restore md instance %q: %w", inst.ExternalID, err)
		}
		rt.summary.AddApplied(backup.SectionMarketData, 1)
	}

	// Instruments resolve their instance by the preserved external id and their
	// assets by code; they upsert on (instance, external_symbol).
	for _, instr := range data.MarketDataInstruments {
		instanceID, err := resolveInstanceID(ctx, rt.tx, instr.Instance)
		if err != nil {
			return err
		}
		baseID, err := resolveAssetID(ctx, rt.tx, instr.BaseAsset)
		if err != nil {
			return err
		}
		quoteID, err := resolveAssetID(ctx, rt.tx, instr.QuoteAsset)
		if err != nil {
			return err
		}
		exists, err := rowExists(
			ctx, rt.tx,
			`SELECT 1 FROM market_data_instruments WHERE instance_id = ? AND external_symbol = ?`,
			instanceID, instr.ExternalSymbol,
		)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionMarketData, exists) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT INTO market_data_instruments
			 (instance_id, external_symbol, base_asset_id, quote_asset_id, enabled, manual_price)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(instance_id, external_symbol) DO UPDATE SET
			   base_asset_id = excluded.base_asset_id,
			   quote_asset_id = excluded.quote_asset_id,
			   enabled = excluded.enabled,
			   manual_price = excluded.manual_price`,
			instanceID, instr.ExternalSymbol, baseID, quoteID,
			instr.Enabled, instr.ManualPrice,
		); err != nil {
			return fmt.Errorf("store: restore md instrument %q: %w", instr.ExternalSymbol, err)
		}
		rt.summary.AddApplied(backup.SectionMarketData, 1)
	}
	return nil
}

func (rt *restoreTx) restoreQuotes(ctx context.Context, quotes []domain.MarketDataQuote) error {
	for _, q := range quotes {
		var instrumentID int64
		err := rt.tx.QueryRowContext(
			ctx,
			`SELECT mdi.id
			 FROM market_data_instruments mdi
			 JOIN market_data_instances i ON i.id = mdi.instance_id
			 WHERE i.external_id = ? AND mdi.external_symbol = ?`,
			q.Instance.Bytes(), q.ExternalSymbol,
		).Scan(&instrumentID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf(
				"store: restore quote instrument %q/%q: %w",
				q.Instance, q.ExternalSymbol, domain.ErrNotFound,
			)
		}
		if err != nil {
			return fmt.Errorf("store: restore quote resolve instrument: %w", err)
		}
		exists, err := rowExists(
			ctx, rt.tx,
			`SELECT 1 FROM market_data_quotes WHERE instrument_id = ?`, instrumentID,
		)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionMarketDataQuotes, exists) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT OR REPLACE INTO market_data_quotes
			 (instrument_id, mark, bid, ask, as_of, received_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			instrumentID, q.Mark, q.Bid, q.Ask,
			q.AsOf.UTC().Format(time.RFC3339Nano),
			q.ReceivedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("store: restore quote %q: %w", q.ExternalSymbol, err)
		}
		rt.summary.AddApplied(backup.SectionMarketDataQuotes, 1)
	}
	return nil
}

// --- Restore: settings ------------------------------------------------------

// restoreGeneralSettings restores the MCP access overrides, the signing config
// and the signing keys. Signing keys are dictionary support rows referenced by
// order approvals through key_id, so they must land before activity history.
func (rt *restoreTx) restoreGeneralSettings(ctx context.Context, data backup.Data) error {
	for command, enabled := range data.McpAccess {
		exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM mcp_access WHERE command = ?`, command)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionGeneralSettings, exists) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT INTO mcp_access (command, enabled) VALUES (?, ?)
			 ON CONFLICT(command) DO UPDATE SET enabled = excluded.enabled`,
			command, enabled,
		); err != nil {
			return fmt.Errorf("store: restore mcp access %q: %w", command, err)
		}
		rt.summary.AddApplied(backup.SectionGeneralSettings, 1)
	}
	for _, entry := range data.SigningConfig {
		exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM signing_config WHERE key = ?`, entry.Key)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionGeneralSettings, exists) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT INTO signing_config (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			entry.Key, entry.Value,
		); err != nil {
			return fmt.Errorf("store: restore signing config %q: %w", entry.Key, err)
		}
		rt.summary.AddApplied(backup.SectionGeneralSettings, 1)
	}
	for _, key := range data.SigningKeys {
		exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM signing_keys WHERE key_id = ?`, key.KeyID)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionGeneralSettings, exists) {
			continue
		}
		createdAt := key.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT INTO signing_keys
			 (key_id, alg, private_key, public_key, created_at, active)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(key_id) DO UPDATE SET
			   alg = excluded.alg, private_key = excluded.private_key,
			   public_key = excluded.public_key, created_at = excluded.created_at,
			   active = excluded.active`,
			key.KeyID, key.Alg, key.PrivateKey, key.PublicKey,
			createdAt.UTC().Format(time.RFC3339Nano), key.Active,
		); err != nil {
			return fmt.Errorf("store: restore signing key %q: %w", key.KeyID, err)
		}
		rt.summary.AddApplied(backup.SectionGeneralSettings, 1)
	}
	return nil
}

func (rt *restoreTx) restoreUserSettings(ctx context.Context, settings []domain.UserSetting) error {
	for _, s := range settings {
		exists, err := rowExists(
			ctx, rt.tx,
			`SELECT 1 FROM user_settings WHERE user_id = ? AND setting_key = ?`,
			s.UserID, s.Key,
		)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionUserSettings, exists) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT INTO user_settings (user_id, setting_key, setting_value)
			 VALUES (?, ?, ?)
			 ON CONFLICT(user_id, setting_key) DO UPDATE SET
			   setting_value = excluded.setting_value`,
			s.UserID, s.Key, s.Value,
		); err != nil {
			return fmt.Errorf("store: restore user setting %q/%q: %w", s.UserID, s.Key, err)
		}
		rt.summary.AddApplied(backup.SectionUserSettings, 1)
	}
	return nil
}

// --- Restore: activity history ----------------------------------------------

// restoreActivity restores adjustments, orders (with approvals), order events and
// trades, each preserving its archived external id while resolving cross-row
// links through the dictionaries inserted earlier. Orders land before their
// events and trades so the order surrogate id those rows reference exists.
func (rt *restoreTx) restoreActivity(ctx context.Context, data backup.Data) error {
	for _, adj := range data.Adjustments {
		if err := rt.restoreAdjustment(ctx, adj); err != nil {
			return err
		}
	}
	for _, rec := range data.Orders {
		if err := rt.restoreOrder(ctx, rec); err != nil {
			return err
		}
	}
	for _, ev := range data.OrderEvents {
		if err := rt.restoreOrderEvent(ctx, ev); err != nil {
			return err
		}
	}
	for _, t := range data.Trades {
		if err := rt.restoreTrade(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

func (rt *restoreTx) restoreAdjustment(
	ctx context.Context, rec domain.AccountAdjustmentRecord,
) error {
	accountID, err := resolveAccountID(ctx, rt.tx, rec.Account)
	if err != nil {
		return err
	}
	assetID, err := resolveAssetID(ctx, rt.tx, rec.Asset)
	if err != nil {
		return err
	}
	principalID, err := resolveOptionalPrincipalID(ctx, rt.tx, rec.Principal)
	if err != nil {
		return err
	}
	exists, err := rowExists(
		ctx, rt.tx, `SELECT 1 FROM adjustments WHERE external_id = ?`, rec.ExternalID.Bytes(),
	)
	if err != nil {
		return err
	}
	if rt.skipMachine(backup.SectionActivityHistory, exists) {
		return nil
	}
	reqJSON, err := json.Marshal(rec.Request)
	if err != nil {
		return fmt.Errorf("store: restore adjustment request: %w", err)
	}
	outcomeJSON, err := marshalAdjustmentOutcome(rec)
	if err != nil {
		return err
	}
	if _, err := rt.tx.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO adjustments
		 (external_id, account_id, asset_id, principal_id, at, source, status, request, outcome)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ExternalID.Bytes(), accountID, assetID, principalID,
		atOrNow(rec.At), string(rec.Source), string(adjustmentStatus(rec)),
		string(reqJSON), string(outcomeJSON),
	); err != nil {
		return fmt.Errorf("store: restore adjustment %q: %w", rec.ExternalID, err)
	}
	rt.summary.AddApplied(backup.SectionActivityHistory, 1)
	return nil
}

func (rt *restoreTx) restoreOrder(ctx context.Context, rec backup.OrderRecord) error {
	o := rec.Order
	accountID, err := resolveAccountID(ctx, rt.tx, o.Account)
	if err != nil {
		return err
	}
	baseID, err := resolveAssetID(ctx, rt.tx, o.BaseAsset)
	if err != nil {
		return err
	}
	quoteID, err := resolveAssetID(ctx, rt.tx, o.QuoteAsset)
	if err != nil {
		return err
	}
	principalID, err := resolveOptionalPrincipalID(ctx, rt.tx, o.Principal)
	if err != nil {
		return err
	}
	exists, err := rowExists(
		ctx, rt.tx, `SELECT 1 FROM orders WHERE external_id = ?`, o.ExternalID.Bytes(),
	)
	if err != nil {
		return err
	}
	if rt.skipMachine(backup.SectionActivityHistory, exists) {
		return nil
	}
	if exists && rt.mode == backup.RestoreModeOverwrite {
		if _, err := rt.tx.ExecContext(
			ctx,
			`UPDATE orders
			 SET account_id = ?, base_asset_id = ?, quote_asset_id = ?, principal_id = ?,
			     at = ?, source = ?, side = ?, amount_kind = ?, amount_value = ?,
			     price = ?, status = ?, lock = ?
			 WHERE external_id = ?`,
			accountID, baseID, quoteID, principalID, atOrNow(o.At), string(o.Source),
			string(o.Side), string(o.AmountKind), o.AmountValue, o.Price,
			string(o.Status), nullableBlob(o.Lock), o.ExternalID.Bytes(),
		); err != nil {
			return fmt.Errorf("store: restore order %q: %w", o.ExternalID, err)
		}
	} else if _, err := rt.tx.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO orders
		 (external_id, account_id, base_asset_id, quote_asset_id, principal_id,
		  at, source, side, amount_kind, amount_value, price, status, lock)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ExternalID.Bytes(), accountID, baseID, quoteID, principalID,
		atOrNow(o.At), string(o.Source), string(o.Side), string(o.AmountKind),
		o.AmountValue, o.Price, string(o.Status), nullableBlob(o.Lock),
	); err != nil {
		return fmt.Errorf("store: restore order %q: %w", o.ExternalID, err)
	}
	rt.summary.AddApplied(backup.SectionActivityHistory, 1)

	if rec.Approval != nil {
		orderID, err := lookupOrderID(ctx, rt.tx, o.ExternalID)
		if err != nil {
			return err
		}
		env := rec.Approval
		signingKeyID, err := resolveSigningKeyID(ctx, rt.tx, env.KeyID)
		if err != nil {
			return err
		}
		if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT INTO order_approvals
			 (order_id, token, signing_key_id, alg, mode, issued_at, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(order_id) DO UPDATE SET
			   token = excluded.token,
			   signing_key_id = excluded.signing_key_id,
			   alg = excluded.alg,
			   mode = excluded.mode,
			   issued_at = excluded.issued_at,
			   expires_at = excluded.expires_at`,
			orderID, env.Token, signingKeyID, env.Alg, env.Mode, env.IssuedAt, env.ExpiresAt,
		); err != nil {
			return fmt.Errorf("store: restore order approval %q: %w", o.ExternalID, err)
		}
	}
	return nil
}

func (rt *restoreTx) restoreOrderEvent(ctx context.Context, ev domain.OrderEvent) error {
	orderID, err := lookupOrderID(ctx, rt.tx, ev.Order)
	if err != nil {
		return err
	}
	principalID, err := resolveOptionalPrincipalID(ctx, rt.tx, ev.Principal)
	if err != nil {
		return err
	}
	exists, err := rowExists(
		ctx, rt.tx, `SELECT 1 FROM order_events WHERE external_id = ?`, ev.ExternalID.Bytes(),
	)
	if err != nil {
		return err
	}
	if rt.skipMachine(backup.SectionActivityHistory, exists) {
		return nil
	}
	payloadJSON, err := json.Marshal(ev.Payload)
	if err != nil {
		return fmt.Errorf("store: restore event payload: %w", err)
	}
	if _, err := rt.tx.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO order_events
		 (external_id, order_id, principal_id, at, type, source, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ev.ExternalID.Bytes(), orderID, principalID, atOrNow(ev.At),
		string(ev.Type), string(ev.Source), string(payloadJSON),
	); err != nil {
		return fmt.Errorf("store: restore order event %q: %w", ev.ExternalID, err)
	}
	rt.summary.AddApplied(backup.SectionActivityHistory, 1)
	return nil
}

func (rt *restoreTx) restoreTrade(ctx context.Context, t domain.Trade) error {
	orderID, err := lookupOrderID(ctx, rt.tx, t.Order)
	if err != nil {
		return err
	}
	accountID, err := resolveAccountID(ctx, rt.tx, t.Account)
	if err != nil {
		return err
	}
	baseID, err := resolveAssetID(ctx, rt.tx, t.BaseAsset)
	if err != nil {
		return err
	}
	quoteID, err := resolveAssetID(ctx, rt.tx, t.QuoteAsset)
	if err != nil {
		return err
	}
	principalID, err := resolveOptionalPrincipalID(ctx, rt.tx, t.Principal)
	if err != nil {
		return err
	}
	exists, err := rowExists(
		ctx, rt.tx, `SELECT 1 FROM trades WHERE external_id = ?`, t.ExternalID.Bytes(),
	)
	if err != nil {
		return err
	}
	if rt.skipMachine(backup.SectionActivityHistory, exists) {
		return nil
	}
	if _, err := rt.tx.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO trades
		 (external_id, order_id, account_id, base_asset_id, quote_asset_id,
		  principal_id, at, source, side, quantity, price, lock_price)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ExternalID.Bytes(), orderID, accountID, baseID, quoteID, principalID,
		atOrNow(t.At), string(t.Source), string(t.Side), t.Quantity, t.Price, t.LockPrice,
	); err != nil {
		return fmt.Errorf("store: restore trade %q: %w", t.ExternalID, err)
	}
	rt.summary.AddApplied(backup.SectionActivityHistory, 1)
	return nil
}

// --- Restore: audit ---------------------------------------------------------

func (rt *restoreTx) restoreAudit(ctx context.Context, rows []domain.AuditRow) error {
	for _, row := range rows {
		exists, err := rowExists(
			ctx, rt.tx, `SELECT 1 FROM audit WHERE external_id = ?`, row.ExternalID.Bytes(),
		)
		if err != nil {
			return err
		}
		if rt.skipMachine(backup.SectionAuditLog, exists) {
			continue
		}
		source := row.Source
		if source == "" {
			source = domain.SourceSystem
		}
		if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT OR REPLACE INTO audit
			 (external_id, account_code, account_title, actor_code, actor_title,
			  at, action, source, detail)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.ExternalID.Bytes(), row.Account.String(), row.AccountTitle,
			row.Actor, row.ActorTitle, atOrNow(row.At),
			string(row.Action), string(source), row.Detail,
		); err != nil {
			return fmt.Errorf("store: restore audit %q: %w", row.ExternalID, err)
		}
		rt.summary.AddApplied(backup.SectionAuditLog, 1)
	}
	return nil
}

// --- Restore: replace-all prune ---------------------------------------------

// prune deletes, for every section the scope includes, the in-scope rows the
// archive does not carry, so a replace-all restore leaves each included section
// exactly equal to the archive. It runs before any insert. Deletes follow the
// FK ownership chain: deleting an account or asset cascades its balances,
// adjustments, orders (and their events, trades and approval); deleting a group
// clears the account links (SET NULL); deleting a market-data instance cascades
// its instruments and quotes. Account-addressed sections honour the scope's
// account/position selectors; the dictionaries assets and principals are never
// pruned (they are shared support rows every section's foreign keys resolve
// against, carried in full rather than as a section).
func (rt *restoreTx) prune(ctx context.Context, scope backup.Scope, data backup.Data) error {
	// Resolve selector groups against the archive data, matching FilterData.
	// A live target account reassigned into or out of a selected group must not
	// change which archive-source rows a scoped restore treats as in scope.
	accountScope := archiveScopeAccounts(scope.Accounts, data.Accounts)
	positionScope := archiveScopeAccounts(scope.Positions, data.Accounts)
	if scope.Included(backup.SectionAccountsGroups) {
		if err := rt.pruneAccountsGroups(ctx, scope, accountScope, data); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionPositions) {
		if err := rt.prunePositions(ctx, positionScope, data.Balances); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionRiskLimits) {
		if err := rt.pruneLimits(ctx, scope.Accounts, accountScope, data); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionMarketData) {
		if err := rt.pruneMarketData(ctx, data); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionMarketDataQuotes) {
		if err := rt.pruneQuotes(ctx, data.MarketDataQuotes); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionUserSettings) {
		if err := rt.pruneUserSettings(ctx, data.UserSettings); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionActivityHistory) {
		if err := rt.pruneActivity(ctx, accountScope, data); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionGeneralSettings) {
		if err := rt.pruneGeneralSettings(ctx, data); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionAuditLog) {
		if err := rt.pruneAudit(ctx, scope.Accounts, accountScope, data.Audit); err != nil {
			return err
		}
	}
	return nil
}

// accountScope is the resolved set of in-scope account codes for one selector.
// matchesAll is true when the selector imposes no narrowing (All or empty), in
// which case codes is unused and every account-addressed row is in scope.
type accountScope struct {
	codes      map[string]bool
	matchesAll bool
}

// in reports whether an account code is in the resolved scope.
func (s accountScope) in(account string) bool {
	return s.matchesAll || s.codes[account]
}

// archiveScopeAccounts resolves account selectors with archive-source group
// membership. Direct account codes remain explicit, even if the archive omits
// that row, while group selectors expand only through archive account rows.
func archiveScopeAccounts(
	selector backup.EntitySelector, accounts []backup.Account,
) accountScope {
	if selector.All || selector.Empty() {
		return accountScope{matchesAll: true}
	}
	codes := make(map[string]bool, len(selector.Accounts)+len(accounts))
	for _, code := range selector.Accounts {
		codes[code] = true
	}
	for _, account := range accounts {
		for _, group := range selector.Groups {
			if account.GroupCode == group {
				codes[account.Code] = true
				break
			}
		}
	}
	return accountScope{codes: codes}
}

// pruneAccountsGroups deletes the in-scope accounts the archive omits (their
// balances/adjustments/orders cascade) and then the in-scope groups it omits
// (account links clear via SET NULL). The resolved account scope decides which
// existing rows are in scope; an All/empty selector prunes the whole realm. A
// group is in scope when the selector names it directly or it owns an in-scope
// account, so a restored account's group link always survives.
func (rt *restoreTx) pruneAccountsGroups(
	ctx context.Context, scope backup.Scope, accounts accountScope, data backup.Data,
) error {
	keepAccounts := make(map[string]bool, len(data.Accounts))
	for _, a := range data.Accounts {
		keepAccounts[a.Code] = true
	}
	accountGroups, err := rt.accountCodeGroups(ctx)
	if err != nil {
		return err
	}
	inScopeGroups := make(map[string]bool)
	for _, a := range data.Accounts {
		if accounts.in(a.Code) && a.GroupCode != "" {
			inScopeGroups[a.GroupCode] = true
		}
	}
	for _, p := range accountGroups {
		if !accounts.in(p.a) {
			continue
		}
		if keepAccounts[p.a] {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM accounts WHERE code = ?`, p.a,
		); err != nil {
			return fmt.Errorf("store: prune account %q: %w", p.a, err)
		}
	}

	keepGroups := make(map[string]bool, len(data.Groups))
	for _, g := range data.Groups {
		keepGroups[g.Code] = true
	}
	for _, code := range scope.Accounts.Groups {
		inScopeGroups[code] = true
	}
	groups, err := scanStrings(rt.tx.QueryContext(ctx, `SELECT code FROM account_groups`))
	if err != nil {
		return fmt.Errorf("store: prune groups scan: %w", err)
	}
	for _, code := range groups {
		if keepGroups[code] {
			continue
		}
		if !accounts.matchesAll && !inScopeGroups[code] {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM account_groups WHERE code = ?`, code,
		); err != nil {
			return fmt.Errorf("store: prune group %q: %w", code, err)
		}
	}
	return nil
}

// accountCodeGroups reads every account's code with its group code (empty when
// the account has no group), for scope resolution during a prune.
func (rt *restoreTx) accountCodeGroups(ctx context.Context) ([]codePair, error) {
	rows, err := rt.tx.QueryContext(
		ctx,
		`SELECT a.code, COALESCE(g.code, '')
		 FROM accounts a
		 LEFT JOIN account_groups g ON g.id = a.group_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: prune account groups scan: %w", err)
	}
	return scanCodePairs(rows)
}

// prunePositions deletes the in-scope balances the archive omits. The resolved
// positions scope decides which balances are in scope; an All/empty selector
// prunes the whole realm.
func (rt *restoreTx) prunePositions(
	ctx context.Context, positions accountScope, balances []domain.Balance,
) error {
	keep := make(map[string]bool, len(balances))
	for _, b := range balances {
		keep[b.Account.String()+"\x00"+b.Asset] = true
	}
	rows, err := rt.tx.QueryContext(
		ctx,
		`SELECT a.code, ast.code
		 FROM balances b
		 JOIN accounts a   ON a.id   = b.account_id
		 JOIN assets   ast ON ast.id = b.asset_id`,
	)
	if err != nil {
		return fmt.Errorf("store: prune positions scan: %w", err)
	}
	pairs, err := scanCodePairs(rows)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		if keep[p.a+"\x00"+p.b] || !positions.in(p.a) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx,
			`DELETE FROM balances
			 WHERE account_id = (SELECT id FROM accounts WHERE code = ?)
			   AND asset_id   = (SELECT id FROM assets   WHERE code = ?)`,
			p.a, p.b,
		); err != nil {
			return fmt.Errorf("store: prune balance %q/%q: %w", p.a, p.b, err)
		}
	}
	return nil
}

// pruneLimits deletes the in-scope limit barriers the archive omits across the
// three typed limit tables. A barrier is keyed by (scope, account, asset); a
// barrier with no account axis is realm-wide and pruned only when the account
// selector imposes no narrowing.
func (rt *restoreTx) pruneLimits(
	ctx context.Context, selector backup.EntitySelector, accounts accountScope, data backup.Data,
) error {
	rateKeep := make(map[string]bool, len(data.RateLimits))
	for _, l := range data.RateLimits {
		rateKeep[limitKey(l.Scope, l.Account, l.Asset)] = true
	}
	if err := rt.pruneLimitTable(ctx, selector, accounts, "limit_rate", rateKeep); err != nil {
		return err
	}
	sizeKeep := make(map[string]bool, len(data.OrderSizeLimits))
	for _, l := range data.OrderSizeLimits {
		sizeKeep[limitKey(l.Scope, l.Account, l.Asset)] = true
	}
	if err := rt.pruneLimitTable(ctx, selector, accounts, "limit_order_size", sizeKeep); err != nil {
		return err
	}
	pnlKeep := make(map[string]bool, len(data.PnlBoundsLimits))
	for _, l := range data.PnlBoundsLimits {
		pnlKeep[limitKey(l.Scope, l.Account, l.Asset)] = true
	}
	return rt.pruneLimitTable(ctx, selector, accounts, "limit_pnl_bounds", pnlKeep)
}

// pruneLimitTable deletes the in-scope rows of one limit table whose
// (scope, account, asset) key the archive does not carry. The account/asset
// axes are read back as codes (NULL = empty) to rebuild the portable key. A
// realm-wide barrier (empty account) is in scope only when the selector imposes
// no narrowing, matching the export filter; an account-bound barrier is in
// scope when its account is.
func (rt *restoreTx) pruneLimitTable(
	ctx context.Context, selector backup.EntitySelector, accounts accountScope,
	table string, keep map[string]bool,
) error {
	rows, err := rt.tx.QueryContext(
		ctx,
		`SELECT t.id, t.scope, a.code, ast.code
		 FROM `+table+` t
		 LEFT JOIN accounts a   ON a.id   = t.account_id
		 LEFT JOIN assets   ast ON ast.id = t.asset_id`,
	)
	if err != nil {
		return fmt.Errorf("store: prune %s scan: %w", table, err)
	}
	type limitRow struct {
		id            int64
		scope, a, ast string
	}
	out := make([]limitRow, 0)
	for rows.Next() {
		var (
			id         int64
			scopeStr   string
			acc, asset sql.NullString
		)
		if err := rows.Scan(&id, &scopeStr, &acc, &asset); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: prune %s row: %w", table, err)
		}
		out = append(out, limitRow{id: id, scope: scopeStr, a: acc.String, ast: asset.String})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("store: prune %s iterate: %w", table, err)
	}
	_ = rows.Close()
	for _, l := range out {
		if keep[limitKey(l.scope, domain.AccountID(l.a), l.ast)] ||
			!limitAxisInScope(selector, accounts, l.a) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM `+table+` WHERE id = ?`, l.id,
		); err != nil {
			return fmt.Errorf("store: prune %s id %d: %w", table, l.id, err)
		}
	}
	return nil
}

// limitAxisInScope reports whether a limit barrier's account axis is in scope. A
// realm-wide barrier (empty account) is in scope only when the selector imposes
// no narrowing; an account-bound barrier is in scope when its account is.
func limitAxisInScope(selector backup.EntitySelector, accounts accountScope, account string) bool {
	if account == "" {
		return selector.All || selector.Empty()
	}
	return accounts.in(account)
}

// pruneMarketData deletes the instruments and instances the archive omits.
// Instruments are deleted first by (instance external id, external symbol);
// then instances by external id (their remaining instruments and quotes
// cascade). Market data is not account-addressed, so the whole realm is in
// scope.
func (rt *restoreTx) pruneMarketData(ctx context.Context, data backup.Data) error {
	keepInstr := make(map[string]bool, len(data.MarketDataInstruments))
	for _, instr := range data.MarketDataInstruments {
		keepInstr[instr.Instance.String()+"\x00"+instr.ExternalSymbol] = true
	}
	instrRows, err := rt.tx.QueryContext(
		ctx,
		`SELECT mdi.id, i.external_id, mdi.external_symbol
		 FROM market_data_instruments mdi
		 JOIN market_data_instances i ON i.id = mdi.instance_id`,
	)
	if err != nil {
		return fmt.Errorf("store: prune md instruments scan: %w", err)
	}
	type instrRow struct {
		id  int64
		key string
	}
	instrs := make([]instrRow, 0)
	for instrRows.Next() {
		var (
			id     int64
			extID  []byte
			symbol string
		)
		if err := instrRows.Scan(&id, &extID, &symbol); err != nil {
			_ = instrRows.Close()
			return fmt.Errorf("store: prune md instrument row: %w", err)
		}
		xid, err := domain.ExternalIDFromBytes(extID)
		if err != nil {
			_ = instrRows.Close()
			return fmt.Errorf("store: prune md instrument external id: %w", err)
		}
		instrs = append(instrs, instrRow{id: id, key: xid.String() + "\x00" + symbol})
	}
	if err := instrRows.Err(); err != nil {
		_ = instrRows.Close()
		return fmt.Errorf("store: prune md instruments iterate: %w", err)
	}
	_ = instrRows.Close()
	for _, instr := range instrs {
		if keepInstr[instr.key] {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM market_data_instruments WHERE id = ?`, instr.id,
		); err != nil {
			return fmt.Errorf("store: prune md instrument id %d: %w", instr.id, err)
		}
	}

	keepInst := make(map[string]bool, len(data.MarketDataInstances))
	for _, inst := range data.MarketDataInstances {
		keepInst[inst.ExternalID.String()] = true
	}
	instRows, err := rt.tx.QueryContext(
		ctx, `SELECT id, external_id FROM market_data_instances`,
	)
	if err != nil {
		return fmt.Errorf("store: prune md instances scan: %w", err)
	}
	type instRow struct {
		id  int64
		key string
	}
	insts := make([]instRow, 0)
	for instRows.Next() {
		var (
			id    int64
			extID []byte
		)
		if err := instRows.Scan(&id, &extID); err != nil {
			_ = instRows.Close()
			return fmt.Errorf("store: prune md instance row: %w", err)
		}
		xid, err := domain.ExternalIDFromBytes(extID)
		if err != nil {
			_ = instRows.Close()
			return fmt.Errorf("store: prune md instance external id: %w", err)
		}
		insts = append(insts, instRow{id: id, key: xid.String()})
	}
	if err := instRows.Err(); err != nil {
		_ = instRows.Close()
		return fmt.Errorf("store: prune md instances iterate: %w", err)
	}
	_ = instRows.Close()
	for _, inst := range insts {
		if keepInst[inst.key] {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM market_data_instances WHERE id = ?`, inst.id,
		); err != nil {
			return fmt.Errorf("store: prune md instance id %d: %w", inst.id, err)
		}
	}
	return nil
}

// pruneQuotes deletes the quotes whose instrument (by instance external id and
// external symbol) the archive omits.
func (rt *restoreTx) pruneQuotes(ctx context.Context, quotes []domain.MarketDataQuote) error {
	keep := make(map[string]bool, len(quotes))
	for _, q := range quotes {
		keep[q.Instance.String()+"\x00"+q.ExternalSymbol] = true
	}
	rows, err := rt.tx.QueryContext(
		ctx,
		`SELECT q.instrument_id, i.external_id, mdi.external_symbol
		 FROM market_data_quotes q
		 JOIN market_data_instruments mdi ON mdi.id = q.instrument_id
		 JOIN market_data_instances i ON i.id = mdi.instance_id`,
	)
	if err != nil {
		return fmt.Errorf("store: prune quotes scan: %w", err)
	}
	type quoteRow struct {
		instrumentID int64
		key          string
	}
	out := make([]quoteRow, 0)
	for rows.Next() {
		var (
			instrumentID int64
			extID        []byte
			symbol       string
		)
		if err := rows.Scan(&instrumentID, &extID, &symbol); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: prune quote row: %w", err)
		}
		xid, err := domain.ExternalIDFromBytes(extID)
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: prune quote external id: %w", err)
		}
		out = append(out, quoteRow{instrumentID: instrumentID, key: xid.String() + "\x00" + symbol})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("store: prune quotes iterate: %w", err)
	}
	_ = rows.Close()
	for _, q := range out {
		if keep[q.key] {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM market_data_quotes WHERE instrument_id = ?`, q.instrumentID,
		); err != nil {
			return fmt.Errorf("store: prune quote %d: %w", q.instrumentID, err)
		}
	}
	return nil
}

// pruneGeneralSettings deletes the MCP access overrides, signing-config entries
// and signing keys the archive omits. Signing keys referenced by a surviving
// order approval are kept (the RESTRICT foreign key forbids dropping a key in
// use); such a key is still in the archive whenever the order it signs is.
func (rt *restoreTx) pruneGeneralSettings(ctx context.Context, data backup.Data) error {
	keepMcp := make(map[string]bool, len(data.McpAccess))
	for command := range data.McpAccess {
		keepMcp[command] = true
	}
	commands, err := scanStrings(rt.tx.QueryContext(ctx, `SELECT command FROM mcp_access`))
	if err != nil {
		return fmt.Errorf("store: prune mcp access: %w", err)
	}
	for _, command := range commands {
		if keepMcp[command] {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM mcp_access WHERE command = ?`, command,
		); err != nil {
			return fmt.Errorf("store: prune mcp access %q: %w", command, err)
		}
	}

	keepConfig := make(map[string]bool, len(data.SigningConfig))
	for _, entry := range data.SigningConfig {
		keepConfig[entry.Key] = true
	}
	keys, err := scanStrings(rt.tx.QueryContext(ctx, `SELECT key FROM signing_config`))
	if err != nil {
		return fmt.Errorf("store: prune signing config: %w", err)
	}
	for _, key := range keys {
		if keepConfig[key] {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM signing_config WHERE key = ?`, key,
		); err != nil {
			return fmt.Errorf("store: prune signing config %q: %w", key, err)
		}
	}

	keepKeys := make(map[string]bool, len(data.SigningKeys))
	for _, key := range data.SigningKeys {
		keepKeys[key.KeyID] = true
	}
	keyIDs, err := scanStrings(rt.tx.QueryContext(ctx, `SELECT key_id FROM signing_keys`))
	if err != nil {
		return fmt.Errorf("store: prune signing keys: %w", err)
	}
	for _, keyID := range keyIDs {
		if keepKeys[keyID] {
			continue
		}
		referenced, err := rowExists(
			ctx, rt.tx, `SELECT 1 FROM order_approvals ap
			 JOIN signing_keys sk ON sk.id = ap.signing_key_id
			 WHERE sk.key_id = ?`, keyID,
		)
		if err != nil {
			return err
		}
		if referenced {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM signing_keys WHERE key_id = ?`, keyID,
		); err != nil {
			return fmt.Errorf("store: prune signing key %q: %w", keyID, err)
		}
	}
	return nil
}

// pruneUserSettings deletes the per-user UI settings the archive omits.
func (rt *restoreTx) pruneUserSettings(ctx context.Context, settings []domain.UserSetting) error {
	keep := make(map[string]bool, len(settings))
	for _, s := range settings {
		keep[s.UserID+"\x00"+s.Key] = true
	}
	rows, err := rt.tx.QueryContext(
		ctx, `SELECT user_id, setting_key FROM user_settings`,
	)
	if err != nil {
		return fmt.Errorf("store: prune user settings scan: %w", err)
	}
	pairs, err := scanCodePairs(rows)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		if keep[p.a+"\x00"+p.b] {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM user_settings WHERE user_id = ? AND setting_key = ?`, p.a, p.b,
		); err != nil {
			return fmt.Errorf("store: prune user setting %q/%q: %w", p.a, p.b, err)
		}
	}
	return nil
}

// pruneActivity deletes the in-scope adjustments and orders the archive omits.
// Deleting an order cascades its events, trades and approval; order events and
// trades therefore need no separate prune. The account selector decides scope.
func (rt *restoreTx) pruneActivity(
	ctx context.Context, accounts accountScope, data backup.Data,
) error {
	keepAdj := make(map[string]bool, len(data.Adjustments))
	for _, adj := range data.Adjustments {
		keepAdj[adj.ExternalID.String()] = true
	}
	if err := rt.pruneByExternalID(
		ctx, accounts, "adjustments", keepAdj,
	); err != nil {
		return err
	}
	keepOrders := make(map[string]bool, len(data.Orders))
	for _, rec := range data.Orders {
		keepOrders[rec.Order.ExternalID.String()] = true
	}
	return rt.pruneByExternalID(ctx, accounts, "orders", keepOrders)
}

// pruneByExternalID deletes the in-scope rows of an account-addressed,
// external-id-keyed table whose external id the archive omits. The row's
// account is read back as a code so the scope's account selector can decide
// whether it is in scope.
func (rt *restoreTx) pruneByExternalID(
	ctx context.Context, accounts accountScope, table string, keep map[string]bool,
) error {
	rows, err := rt.tx.QueryContext(
		ctx,
		`SELECT t.external_id, a.code
		 FROM `+table+` t
		 JOIN accounts a ON a.id = t.account_id`,
	)
	if err != nil {
		return fmt.Errorf("store: prune %s scan: %w", table, err)
	}
	type idRow struct {
		extID   []byte
		account string
	}
	out := make([]idRow, 0)
	for rows.Next() {
		var (
			extID   []byte
			account string
		)
		if err := rows.Scan(&extID, &account); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: prune %s row: %w", table, err)
		}
		stored := make([]byte, len(extID))
		copy(stored, extID)
		out = append(out, idRow{extID: stored, account: account})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("store: prune %s iterate: %w", table, err)
	}
	_ = rows.Close()
	for _, r := range out {
		xid, err := domain.ExternalIDFromBytes(r.extID)
		if err != nil {
			return fmt.Errorf("store: prune %s external id: %w", table, err)
		}
		if keep[xid.String()] || !accounts.in(r.account) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM `+table+` WHERE external_id = ?`, r.extID,
		); err != nil {
			return fmt.Errorf("store: prune %s %q: %w", table, xid, err)
		}
	}
	return nil
}

// pruneAudit deletes the in-scope audit rows the archive omits. An audit row
// with no account snapshot is realm-wide and pruned only when the account
// selector imposes no narrowing.
func (rt *restoreTx) pruneAudit(
	ctx context.Context, selector backup.EntitySelector, accounts accountScope,
	audit []domain.AuditRow,
) error {
	keep := make(map[string]bool, len(audit))
	for _, row := range audit {
		keep[row.ExternalID.String()] = true
	}
	rows, err := rt.tx.QueryContext(
		ctx,
		`SELECT external_id, account_code FROM audit`,
	)
	if err != nil {
		return fmt.Errorf("store: prune audit scan: %w", err)
	}
	type auditRow struct {
		extID   []byte
		account string
	}
	out := make([]auditRow, 0)
	for rows.Next() {
		var (
			extID   []byte
			account sql.NullString
		)
		if err := rows.Scan(&extID, &account); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: prune audit row: %w", err)
		}
		stored := make([]byte, len(extID))
		copy(stored, extID)
		out = append(out, auditRow{extID: stored, account: account.String})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("store: prune audit iterate: %w", err)
	}
	_ = rows.Close()
	for _, r := range out {
		xid, err := domain.ExternalIDFromBytes(r.extID)
		if err != nil {
			return fmt.Errorf("store: prune audit external id: %w", err)
		}
		if keep[xid.String()] || !auditRowInScope(selector, accounts, r.account) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM audit WHERE external_id = ?`, r.extID,
		); err != nil {
			return fmt.Errorf("store: prune audit %q: %w", xid, err)
		}
	}
	return nil
}

// --- Replace-all prune scope helpers ----------------------------------------

// codePair is a two-code row read back during a prune scan.
type codePair struct{ a, b string }

// scanCodePairs scans a two-text-column result into code pairs and closes it.
func scanCodePairs(rows *sql.Rows) ([]codePair, error) {
	defer func() { _ = rows.Close() }()
	out := make([]codePair, 0)
	for rows.Next() {
		var p codePair
		if err := rows.Scan(&p.a, &p.b); err != nil {
			return nil, fmt.Errorf("store: prune scan pair: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: prune iterate pairs: %w", err)
	}
	return out, nil
}

// scanStrings scans a single-text-column result into a slice and closes it.
func scanStrings(rows *sql.Rows, err error) ([]string, error) {
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]string, 0)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("store: prune scan string: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: prune iterate strings: %w", err)
	}
	return out, nil
}

// limitKey builds the portable (scope, account, asset) key a limit barrier is
// addressed by; empty account/asset codes denote an absent axis.
func limitKey(scope string, account domain.AccountID, asset string) string {
	return scope + "\x00" + account.String() + "\x00" + asset
}

// auditRowInScope reports whether an audit row is in scope. A row with no
// account is realm-wide and in scope only when the selector imposes no
// narrowing; an account-bound row is in scope when its account is.
func auditRowInScope(selector backup.EntitySelector, accounts accountScope, account string) bool {
	if account == "" {
		return selector.All || selector.Empty()
	}
	return accounts.in(account)
}

// --- Restore helpers --------------------------------------------------------

// skip reports whether a dictionary/settings row at the given section should be
// skipped under the restore mode: insert-missing skips an existing row, and it
// records the skip in the summary. Overwrite and replace-all never skip (they
// update or re-create in place). Under replace-all the rows the archive does not
// carry are already gone: the prune pass deleted them before any insert ran, so
// the upsert each writer performs leaves the section exactly equal the archive.
func (rt *restoreTx) skip(section backup.Section, exists bool) bool {
	if exists && rt.mode == backup.RestoreModeInsertMissing {
		rt.summary.AddSkipped(section, 1)
		return true
	}
	return false
}

// skipMachine is skip for machine records (orders, trades, events, adjustments,
// audit) keyed by external id. The behaviour matches skip; it is a separate name
// only to document that the identity is the external id rather than a code.
func (rt *restoreTx) skipMachine(section backup.Section, exists bool) bool {
	return rt.skip(section, exists)
}

// rowExists reports whether the single-column existence query returns any row.
func rowExists(ctx context.Context, q sqlQueryer, query string, args ...any) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: restore existence check: %w", err)
	}
	return true, nil
}

// atOrNow formats t as RFC3339Nano UTC text, substituting the current time when t
// is the zero value so a NOT NULL at/issued_at column is never written empty.
func atOrNow(t time.Time) string {
	if t.IsZero() {
		return nowStr()
	}
	return t.UTC().Format(time.RFC3339Nano)
}
