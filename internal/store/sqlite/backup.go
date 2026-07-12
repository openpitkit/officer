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

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
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
	return r.exportBackup(ctx, scope, nil)
}

func (r *realmStore) exportBackup(
	ctx context.Context,
	scope backup.Scope,
	afterSnapshot func() error,
) (backup.Archive, error) {
	snapshot, cleanup, err := r.backupSnapshot(ctx)
	if err != nil {
		return backup.Archive{}, err
	}
	defer cleanup()
	if afterSnapshot != nil {
		if err := afterSnapshot(); err != nil {
			return backup.Archive{}, err
		}
	}
	label, err := snapshot.exportRealmLabel(ctx)
	if err != nil {
		return backup.Archive{}, err
	}
	data, err := snapshot.exportData(ctx)
	if err != nil {
		return backup.Archive{}, err
	}
	return backup.NewArchive(time.Now(), backupSource, label, scope, data), nil
}

func (r *realmStore) backupSnapshot(
	ctx context.Context,
) (*realmStore, func(), error) {
	db, err := r.db()
	if err != nil {
		return nil, nil, err
	}
	temp, err := os.CreateTemp("", "pit-officer-backup-*.db")
	if err != nil {
		return nil, nil, fmt.Errorf("store: create backup snapshot path: %w", err)
	}
	path := temp.Name()
	removeSnapshot := func() {
		for _, candidate := range sqliteResetPaths(path) {
			_ = os.Remove(candidate)
		}
	}
	if err := temp.Close(); err != nil {
		removeSnapshot()
		return nil, nil, fmt.Errorf("store: close backup snapshot path: %w", err)
	}
	if err := os.Remove(path); err != nil {
		removeSnapshot()
		return nil, nil, fmt.Errorf("store: prepare backup snapshot path: %w", err)
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		removeSnapshot()
		return nil, nil, fmt.Errorf("store: capture backup snapshot: %w", err)
	}

	raw, err := New(path, WithRealm(r.store.realm))
	if err != nil {
		removeSnapshot()
		return nil, nil, fmt.Errorf("store: open backup snapshot: %w", err)
	}
	snapshotStore, ok := raw.(*sqliteStore)
	if !ok {
		_ = raw.Close()
		removeSnapshot()
		return nil, nil, fmt.Errorf("store: open backup snapshot returned unexpected type")
	}
	cleanup := func() {
		_ = snapshotStore.Close()
		removeSnapshot()
	}
	return &realmStore{store: snapshotStore}, cleanup, nil
}

// exportRealmLabel reads the single realm identity row's portable label (code and
// title). The realm's external id is deliberately not carried: it is the realm
// row's machine identity in this schema and is re-established by the target
// connector on restore.
func (r *realmStore) exportRealmLabel(ctx context.Context) (backup.RealmLabel, error) {
	var label backup.RealmLabel
	db, err := r.db()
	if err != nil {
		return backup.RealmLabel{}, err
	}
	err = db.QueryRowContext(
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
	if data.AssetClasses, err = r.ListAssetClasses(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.Assets, err = r.ListAssets(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.Principals, err = r.ListPrincipals(ctx); err != nil {
		return backup.Data{}, err
	}
	if data.Groups, err = r.exportGroups(ctx); err != nil {
		return backup.Data{}, err
	}
	if group, ok, err := r.GetGroup(ctx, ""); err != nil {
		return backup.Data{}, err
	} else if ok {
		data.DefaultGroupCurrency = group.Currency
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
	if data.SpotFundsPnlBoundsLimits, err = r.ListSpotFundsPnlBoundsLimits(ctx, ""); err != nil {
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
	if data.MarketDataQuotes, err = r.ListMarketDataQuotes(ctx, domain.ExternalID("")); err != nil {
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

	db, err := r.db()
	if err != nil {
		return backup.RestoreSummary{}, err
	}
	dictionaries, err := r.dictionaries()
	if err != nil {
		return backup.RestoreSummary{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return backup.RestoreSummary{}, fmt.Errorf("store: begin restore: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	summary := backup.NewSummary()
	rt := &restoreTx{
		tx:           tx,
		dictionaries: dictionaries,
		mode:         mode,
		summary:      &summary,
	}

	if err := rt.run(ctx, scope, data); err != nil {
		return backup.RestoreSummary{}, err
	}
	if err := tx.Commit(); err != nil {
		return backup.RestoreSummary{}, fmt.Errorf("store: commit restore: %w", err)
	}

	// Drive the restart signal off the rows actually written, not the requested
	// scope. Normalize force-includes the accounts+groups (and market-data) parent
	// dictionaries whenever an account-addressed section is restored, so an
	// audit-only or activity-history-only request can still land a new runtime
	// dictionary row the live resolver has not seen; when it does, the engine must
	// rebuild. A force-included parent that inserted no runtime row leaves the
	// signal off, keeping an observational restore observational.
	summary.RestartRequired = rt.runtimeApplied
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
// per-section summary so the per-table insert helpers stay small. runtimeApplied
// records whether any genuinely runtime-affecting row (account, group, balance,
// limit, market-data instance/instrument or quote) was written, driving the
// engine-rebuild signal. It is tracked separately from the summary counts because
// the shared support dictionaries (asset classes, assets, principals) roll into
// the accounts+groups summary section but are not part of the engine snapshot, so
// they must not by themselves flip the restart signal.
type restoreTx struct {
	tx             *sql.Tx
	dictionaries   *enumDictionaries
	mode           backup.RestoreMode
	summary        *backup.RestoreSummary
	runtimeApplied bool
}

// applyRuntime records n runtime rows written to section: it bumps the summary
// count and, when section is a runtime section, raises the engine-rebuild signal.
func (rt *restoreTx) applyRuntime(section backup.Section, n int) {
	rt.summary.AddApplied(section, n)
	if n > 0 && backup.RuntimeSection(section) {
		rt.runtimeApplied = true
	}
}

// run inserts every included section in dictionary-first order. Assets and
// principal always travel (FilterData carries them unconditionally) so the
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
	if err := rt.restoreAssetClasses(ctx, data.AssetClasses); err != nil {
		return err
	}
	if err := rt.restoreAssets(ctx, data.Assets); err != nil {
		return err
	}
	if err := rt.restorePrincipals(ctx, data.Principals); err != nil {
		return err
	}
	if scope.Included(backup.SectionAccountsGroups) {
		if err := rt.restoreGroups(ctx, data.Groups, data.DefaultGroupCurrency); err != nil {
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
