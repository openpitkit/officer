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

// Backup group of the SQLite store: the restore-side per-table insert helpers
// used by restoreTx.run to land one archive section into the bound realm.

package sqlite

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	fwsigning "go.openpit.dev/officer/framework/signing"
)

// --- Restore: dictionaries --------------------------------------------------

// An archive is caller-supplied data, not trusted state: it reaches the tables
// without passing the API code-validation seam, so every restored dictionary
// code is checked here against the same contract the live surfaces enforce.
// Otherwise a hand-crafted archive could plant a code carrying a NUL, a
// newline, or an unbounded length.

// restoreAssetClasses inserts (or updates) the asset-class dictionary before the
// assets, so an asset's class_id foreign key resolves on import.
func (rt *restoreTx) restoreAssetClasses(
	ctx context.Context, classes []domain.AssetClass,
) error {
	for _, c := range classes {
		if err := domain.ValidateAssetClassID(c.Code); err != nil {
			return fmt.Errorf("store: restore asset class %q: %w", c.Code, err)
		}
		if err := domain.ValidateTitle(c.Title); err != nil {
			return fmt.Errorf("store: restore asset class %q: %w", c.Code, err)
		}
		if err := domain.ValidateNotes(c.Notes); err != nil {
			return fmt.Errorf("store: restore asset class %q: %w", c.Code, err)
		}
		exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM asset_class WHERE code = ?`, c.Code)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionAccountsGroups, exists) {
			continue
		}
		if exists {
			if _, err := rt.tx.ExecContext(
				ctx, `UPDATE asset_class SET title = ?, notes = ? WHERE code = ?`,
				c.Title, c.Notes, c.Code,
			); err != nil {
				return fmt.Errorf("store: restore asset class %q: %w", c.Code, err)
			}
		} else if _, err := rt.tx.ExecContext(
			ctx, `INSERT INTO asset_class (code, title, notes) VALUES (?, ?, ?)`,
			c.Code, c.Title, c.Notes,
		); err != nil {
			return fmt.Errorf("store: restore asset class %q: %w", c.Code, err)
		}
		rt.summary.AddApplied(backup.SectionAccountsGroups, 1)
	}
	return nil
}

func (rt *restoreTx) restoreAssets(ctx context.Context, assets []backup.Asset) error {
	for _, a := range assets {
		if err := domain.ValidateAsset(a.Code); err != nil {
			return fmt.Errorf("store: restore asset %q: %w", a.Code, err)
		}
		if err := domain.ValidateTitle(a.Title); err != nil {
			return fmt.Errorf("store: restore asset %q: %w", a.Code, err)
		}
		if a.AssetClass != "" {
			if err := domain.ValidateAssetClassID(a.AssetClass); err != nil {
				return fmt.Errorf("store: restore asset %q: %w", a.Code, err)
			}
		}
		exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM asset WHERE code = ?`, a.Code)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionAccountsGroups, exists) {
			continue
		}
		classID, err := optionalClassID(ctx, rt.tx, a.AssetClass)
		if err != nil {
			return fmt.Errorf("store: restore asset %q: %w", a.Code, err)
		}
		if exists {
			if _, err := rt.tx.ExecContext(
				ctx,
				`UPDATE asset SET title = ?, class_id = ? WHERE code = ?`,
				a.Title, classID, a.Code,
			); err != nil {
				return fmt.Errorf("store: restore asset %q: %w", a.Code, err)
			}
		} else if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT INTO asset (code, title, class_id) VALUES (?, ?, ?)`,
			a.Code, a.Title, classID,
		); err != nil {
			return fmt.Errorf("store: restore asset %q: %w", a.Code, err)
		}
		// Assets are dictionary support rows for every section; their counts roll
		// into the account/groups section so the summary stays section-shaped.
		rt.summary.AddApplied(backup.SectionAccountsGroups, 1)
	}
	return nil
}

func (rt *restoreTx) restorePrincipals(ctx context.Context, principals []domain.Principal) error {
	for _, p := range principals {
		if err := domain.ValidatePrincipalID(p.Code); err != nil {
			return fmt.Errorf("store: restore principal %q: %w", p.Code, err)
		}
		if err := domain.ValidateTitle(p.Title); err != nil {
			return fmt.Errorf("store: restore principal %q: %w", p.Code, err)
		}
		exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM principal WHERE code = ?`, p.Code)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionAccountsGroups, exists) {
			continue
		}
		if exists {
			if _, err := rt.tx.ExecContext(
				ctx, `UPDATE principal SET title = ? WHERE code = ?`, p.Title, p.Code,
			); err != nil {
				return fmt.Errorf("store: restore principal %q: %w", p.Code, err)
			}
		} else if _, err := rt.tx.ExecContext(
			ctx, `INSERT INTO principal (code, title) VALUES (?, ?)`, p.Code, p.Title,
		); err != nil {
			return fmt.Errorf("store: restore principal %q: %w", p.Code, err)
		}
		rt.summary.AddApplied(backup.SectionAccountsGroups, 1)
	}
	return nil
}

// restoreGroups inserts the account groups, preserving the code. A new group
// runs on its surrogate id (the engine group id). On overwrite of an existing
// group the surrogate id is kept (the code is the portable handle).
func (rt *restoreTx) restoreGroups(
	ctx context.Context,
	groups []backup.AccountGroup,
	defaultCurrency string,
) error {
	// The archive never carries the reserved default group (code empty); its
	// currency travels separately through restoreDefaultGroupCurrency, so the
	// non-empty group-code contract applies to every row here.
	for _, g := range groups {
		if err := domain.ValidateGroupID(g.Code); err != nil {
			return fmt.Errorf("store: restore group %q: %w", g.Code, err)
		}
		if err := domain.ValidateTitle(g.Title); err != nil {
			return fmt.Errorf("store: restore group %q: %w", g.Code, err)
		}
		if err := domain.ValidateNotes(g.Notes); err != nil {
			return fmt.Errorf("store: restore group %q: %w", g.Code, err)
		}
		if err := domain.ValidateBlockReason(g.BlockReason); err != nil {
			return fmt.Errorf("store: restore group %q: %w", g.Code, err)
		}
		exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM account_group WHERE code = ?`, g.Code)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionAccountsGroups, exists) {
			continue
		}
		currencyID, err := nullableAssetID(ctx, rt.tx, g.Currency)
		if err != nil {
			return fmt.Errorf("store: restore group %q: %w", g.Code, err)
		}
		if exists {
			if _, err := rt.tx.ExecContext(
				ctx,
				`UPDATE account_group
				 SET title = ?, currency_asset_id = ?, notes = ?, blocked = ?,
				     block_reason = ?
				 WHERE code = ?`,
				g.Title, currencyID, g.Notes, g.Blocked, g.BlockReason,
				g.Code,
			); err != nil {
				return fmt.Errorf("store: restore group %q: %w", g.Code, err)
			}
		} else if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT INTO account_group
			 (code, title, currency_asset_id, notes, blocked, block_reason)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			g.Code, g.Title, currencyID, g.Notes, g.Blocked,
			g.BlockReason,
		); err != nil {
			return fmt.Errorf("store: restore group %q: %w", g.Code, err)
		}
		rt.applyRuntime(backup.SectionAccountsGroups, 1)
	}
	if err := rt.restoreDefaultGroupCurrency(ctx, defaultCurrency); err != nil {
		return err
	}
	return nil
}

func (rt *restoreTx) restoreDefaultGroupCurrency(
	ctx context.Context,
	currency string,
) error {
	exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM account_group WHERE code = ''`)
	if err != nil {
		return err
	}
	if rt.skip(backup.SectionAccountsGroups, exists) {
		return nil
	}
	if !exists && currency == "" {
		return nil
	}
	currencyID, err := nullableAssetID(ctx, rt.tx, currency)
	if err != nil {
		return fmt.Errorf("store: restore default group currency: %w", err)
	}
	if exists {
		if _, err := rt.tx.ExecContext(
			ctx,
			`UPDATE account_group SET currency_asset_id = ? WHERE code = ''`,
			currencyID,
		); err != nil {
			return fmt.Errorf("store: restore default group currency: %w", err)
		}
	} else if _, err := rt.tx.ExecContext(
		ctx,
		`INSERT INTO account_group (code, currency_asset_id) VALUES ('', ?)`,
		currencyID,
	); err != nil {
		return fmt.Errorf("store: restore default group currency: %w", err)
	}
	rt.applyRuntime(backup.SectionAccountsGroups, 1)
	return nil
}

// restoreAccounts inserts the account, resolving the optional group link by
// group code (the group is already inserted) and preserving the code. A new
// account runs on its surrogate id (the engine account id).
func (rt *restoreTx) restoreAccounts(ctx context.Context, accounts []backup.Account) error {
	for _, a := range accounts {
		if err := domain.ValidateAccountID(domain.AccountID(a.Code)); err != nil {
			return fmt.Errorf("store: restore account %q: %w", a.Code, err)
		}
		if err := domain.ValidateTitle(a.Title); err != nil {
			return fmt.Errorf("store: restore account %q: %w", a.Code, err)
		}
		if err := domain.ValidateNotes(a.Notes); err != nil {
			return fmt.Errorf("store: restore account %q: %w", a.Code, err)
		}
		if err := domain.ValidateBlockReason(a.BlockReason); err != nil {
			return fmt.Errorf("store: restore account %q: %w", a.Code, err)
		}
		if a.GroupCode != "" {
			if err := domain.ValidateGroupID(a.GroupCode); err != nil {
				return fmt.Errorf("store: restore account %q: %w", a.Code, err)
			}
		}
		if a.Pnl != "" {
			if _, err := domain.AddDecimals("", a.Pnl); err != nil {
				return fmt.Errorf("store: restore account %q pnl %q: %w", a.Code, a.Pnl, err)
			}
		}
		if err := domain.ValidatePnlHaltReason(a.PnlHaltReason); err != nil {
			return fmt.Errorf("store: restore account %q: %w", a.Code, err)
		}
		// Restore stores what the archive carried: an absent P&L stays absent
		// instead of becoming a zero the source never had.
		var storedPnl any
		if a.Pnl != "" && a.PnlHaltReason == "" {
			storedPnl = a.Pnl
		}
		groupID, err := optionalGroupID(ctx, rt.tx, a.GroupCode)
		if err != nil {
			return err
		}
		currencyID, err := nullableAssetID(ctx, rt.tx, a.Currency)
		if err != nil {
			return fmt.Errorf("store: restore account %q: %w", a.Code, err)
		}
		exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM account WHERE code = ?`, a.Code)
		if err != nil {
			return err
		}
		if rt.skip(backup.SectionAccountsGroups, exists) {
			continue
		}
		if exists {
			if _, err := rt.tx.ExecContext(
				ctx,
				`UPDATE account
				 SET title = ?, group_id = ?, currency_asset_id = ?, pnl = ?, pnl_halt_reason = ?,
				     notes = ?, blocked = ?, block_reason = ?
				 WHERE code = ?`,
				a.Title, groupID, currencyID, storedPnl, a.PnlHaltReason, a.Notes, a.Blocked,
				a.BlockReason, a.Code,
			); err != nil {
				return fmt.Errorf("store: restore account %q: %w", a.Code, err)
			}
		} else if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT INTO account
			 (code, title, group_id, currency_asset_id, pnl, pnl_halt_reason, notes, blocked, block_reason)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.Code, a.Title, groupID, currencyID, storedPnl, a.PnlHaltReason, a.Notes, a.Blocked,
			a.BlockReason,
		); err != nil {
			return fmt.Errorf("store: restore account %q: %w", a.Code, err)
		}
		rt.applyRuntime(backup.SectionAccountsGroups, 1)
	}
	return nil
}

// --- Restore: positions -----------------------------------------------------

func (rt *restoreTx) restoreBalances(ctx context.Context, balances []backup.Balance) error {
	for _, b := range balances {
		if err := domain.ValidatePnlHaltReason(b.RealizedPnlHaltReason); err != nil {
			return fmt.Errorf("store: restore balance %q/%q: %w", b.Account, b.Asset, err)
		}
		for _, field := range []struct {
			name  string
			value string
		}{
			{name: "available", value: b.Available},
			{name: "held", value: b.Held},
			{name: "incoming", value: b.Incoming},
			{name: "realized_pnl", value: b.RealizedPnl},
			{name: "average_entry_price", value: b.AverageEntryPrice},
		} {
			if field.value == "" {
				continue
			}
			if err := domain.ValidateDecimal(field.value); err != nil {
				return fmt.Errorf(
					"store: restore balance %q/%q %s: %w",
					b.Account, b.Asset, field.name, err,
				)
			}
		}
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
			`SELECT 1 FROM balance WHERE account_id = ? AND asset_id = ?`,
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
		available := settleOrZero(b.Available)
		held := settleOrZero(b.Held)
		incoming := settleOrZero(b.Incoming)
		realizedPnl := nullablePnl(b.RealizedPnl, b.RealizedPnlHaltReason)
		if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT OR REPLACE INTO balance
			 (account_id, asset_id, available, held,
			  incoming, realized_pnl, realized_pnl_halt_reason, average_entry_price, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			accountID, assetID,
			available, held, incoming, realizedPnl, b.RealizedPnlHaltReason,
			b.AverageEntryPrice,
			updatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("store: restore balance %q/%q: %w", b.Account, b.Asset, err)
		}
		rt.applyRuntime(backup.SectionPositions, 1)
	}
	return nil
}

// --- Restore: limits --------------------------------------------------------

func (rt *restoreTx) restoreLimits(ctx context.Context, data backup.Data) error {
	for _, l := range data.RateLimits {
		if err := l.Validate(); err != nil {
			return fmt.Errorf(
				"store: restore rate_limit scope %q account %q asset %q: %w",
				l.Scope,
				l.Account,
				l.Asset,
				err,
			)
		}
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
		rt.applyRuntime(backup.SectionRiskLimits, applied)
	}
	for _, l := range data.OrderSizeLimits {
		if err := l.Validate(); err != nil {
			return fmt.Errorf(
				"store: restore order_size_limit scope %q account %q asset %q: %w",
				l.Scope,
				l.Account,
				l.Asset,
				err,
			)
		}
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
		rt.applyRuntime(backup.SectionRiskLimits, applied)
	}
	for _, l := range data.SpotFundsPnlBoundsLimits {
		if err := l.Validate(); err != nil {
			return fmt.Errorf(
				"store: restore spot_funds_pnl_bounds_kill_switch scope %q "+
					"account %q account group %q currency %q: %w",
				l.Scope,
				l.Account,
				l.AccountGroup,
				l.Currency,
				err,
			)
		}
		accountID, groupID, err := resolveSpotFundsPnlBoundsAxes(
			ctx,
			rt.tx,
			l.Account,
			l.AccountGroup,
		)
		if err != nil {
			return err
		}
		currencyID, err := resolveAssetID(ctx, rt.tx, l.Currency)
		if err != nil {
			return err
		}
		applied, err := rt.putSpotFundsPnlBoundsLimit(
			ctx,
			l.Scope,
			accountID,
			groupID,
			`INSERT INTO limit_spot_funds_pnl_bound
				 (scope, account_id, account_group_id, currency_asset_id, lower_bound, upper_bound)
				 VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{
				l.Scope, accountID, groupID, currencyID,
				nullableString(l.LowerBound), nullableString(l.UpperBound),
			},
		)
		if err != nil {
			return err
		}
		rt.applyRuntime(backup.SectionRiskLimits, applied)
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

func (rt *restoreTx) putSpotFundsPnlBoundsLimit(
	ctx context.Context,
	scope string,
	accountID, groupID sql.NullInt64,
	insertSQL string,
	insertArgs []any,
) (int, error) {
	exists, err := rowExists(
		ctx, rt.tx,
		`SELECT 1 FROM limit_spot_funds_pnl_bound
		 WHERE scope = ?
		   AND account_id IS ?
		   AND account_group_id IS ?`,
		scope, accountID, groupID,
	)
	if err != nil {
		return 0, err
	}
	if rt.skip(backup.SectionRiskLimits, exists) {
		return 0, nil
	}
	if _, err := rt.tx.ExecContext(
		ctx,
		`DELETE FROM limit_spot_funds_pnl_bound
		 WHERE scope = ?
		   AND account_id IS ?
		   AND account_group_id IS ?`,
		scope, accountID, groupID,
	); err != nil {
		return 0, fmt.Errorf("store: restore delete limit_spot_funds_pnl_bound: %w", err)
	}
	if _, err := rt.tx.ExecContext(ctx, insertSQL, insertArgs...); err != nil {
		return 0, fmt.Errorf("store: restore insert limit_spot_funds_pnl_bound: %w", err)
	}
	return 1, nil
}

// --- Restore: market data ---------------------------------------------------

func (rt *restoreTx) restoreMarketData(ctx context.Context, data backup.Data) error {
	for _, inst := range data.MarketDataInstances {
		if inst.ExternalID.IsZero() {
			return fmt.Errorf(
				"store: restore %s market-data instance %q external id: %w",
				backup.SectionMarketData,
				inst.Label,
				domain.ErrInvalid,
			)
		}
		if err := domain.ValidateMarketDataInstance(inst); err != nil {
			return fmt.Errorf(
				"store: restore market-data instance %q: %w",
				inst.ExternalID,
				err,
			)
		}
		exists, err := rowExists(
			ctx, rt.tx,
			`SELECT 1 FROM market_data_instance WHERE external_id = ?`,
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
				`UPDATE market_data_instance
				 SET provider = ?, label = ?, credentials = ?, enabled = ?
				 WHERE external_id = ?`,
				inst.Provider, inst.Label, inst.Credentials, inst.Enabled,
				inst.ExternalID.Bytes(),
			); err != nil {
				return fmt.Errorf(
					"store: restore market-data instance %q: %w",
					inst.ExternalID,
					err,
				)
			}
		} else if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT INTO market_data_instance
			 (external_id, provider, label, credentials, enabled)
			 VALUES (?, ?, ?, ?, ?)`,
			inst.ExternalID.Bytes(), inst.Provider, inst.Label,
			inst.Credentials, inst.Enabled,
		); err != nil {
			return fmt.Errorf(
				"store: restore market-data instance %q: %w",
				inst.ExternalID,
				err,
			)
		}
		rt.applyRuntime(backup.SectionMarketData, 1)
	}

	// Instruments resolve their instance by the preserved external id and their
	// asset by code; they upsert on (instance, external_symbol).
	for _, instr := range data.MarketDataInstruments {
		if err := domain.ValidateMarketDataInstrument(domain.MarketDataInstrument{
			Instance:       instr.Instance,
			ExternalSymbol: instr.ExternalSymbol,
			BaseAsset:      instr.BaseAsset,
			QuoteAsset:     instr.QuoteAsset,
			ManualPrice:    instr.ManualPrice,
			Enabled:        instr.Enabled,
		}); err != nil {
			return fmt.Errorf(
				"store: restore market-data instrument %q/%q: %w",
				instr.Instance,
				instr.ExternalSymbol,
				err,
			)
		}
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
			`SELECT 1 FROM market_data_instrument WHERE instance_id = ? AND external_symbol = ?`,
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
			`INSERT INTO market_data_instrument
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
			return fmt.Errorf(
				"store: restore market-data instrument %q/%q: %w",
				instr.Instance,
				instr.ExternalSymbol,
				err,
			)
		}
		rt.applyRuntime(backup.SectionMarketData, 1)
	}
	return nil
}

// --- Restore: settings ------------------------------------------------------

// restoreGeneralSettings restores the MCP access overrides, the signing config
// and the signing keys. Signing keys are dictionary support rows referenced by
// order approvals through key_id, so they must land before activity history.
func (rt *restoreTx) restoreGeneralSettings(ctx context.Context, data backup.Data) error {
	if err := rt.validateRestoredSigningKeys(ctx, data.SigningKeys); err != nil {
		return err
	}
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
		exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM signing_key WHERE key_id = ?`, key.KeyID)
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
			`INSERT INTO signing_key
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

func (rt *restoreTx) validateRestoredSigningKeys(
	ctx context.Context, keys []backup.SigningKey,
) error {
	for _, key := range keys {
		if err := validateRestoredSigningKey(key); err != nil {
			return fmt.Errorf(
				"store: restore %s signing key %q: %w",
				backup.SectionGeneralSettings,
				key.KeyID,
				err,
			)
		}
	}

	rows, err := rt.tx.QueryContext(ctx, `SELECT key_id, active FROM signing_key`)
	if err != nil {
		return fmt.Errorf(
			"store: restore %s signing keys: %w",
			backup.SectionGeneralSettings,
			err,
		)
	}
	defer func() { _ = rows.Close() }()

	activeByID := make(map[string]bool)
	for rows.Next() {
		var keyID string
		var active bool
		if err := rows.Scan(&keyID, &active); err != nil {
			return fmt.Errorf(
				"store: restore %s signing keys: %w",
				backup.SectionGeneralSettings,
				err,
			)
		}
		activeByID[keyID] = active
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf(
			"store: restore %s signing keys: %w",
			backup.SectionGeneralSettings,
			err,
		)
	}

	for _, key := range keys {
		if _, exists := activeByID[key.KeyID]; exists && rt.mode == backup.RestoreModeInsertMissing {
			continue
		}
		activeByID[key.KeyID] = key.Active
	}
	var activeIDs []string
	for keyID, active := range activeByID {
		if active {
			activeIDs = append(activeIDs, keyID)
		}
	}
	if len(activeIDs) > 1 {
		sort.Strings(activeIDs)
		return fmt.Errorf(
			"store: restore %s signing keys %q: more than one active key: %w",
			backup.SectionGeneralSettings,
			activeIDs,
			domain.ErrInvalid,
		)
	}
	return nil
}

func validateRestoredSigningKey(key backup.SigningKey) error {
	if key.Alg != fwsigning.AlgEd25519 {
		return fmt.Errorf("algorithm %q: %w", key.Alg, domain.ErrInvalid)
	}
	if len(key.PrivateKey) != ed25519.SeedSize {
		return fmt.Errorf(
			"private key seed length %d, want %d: %w",
			len(key.PrivateKey),
			ed25519.SeedSize,
			domain.ErrInvalid,
		)
	}
	if len(key.PublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf(
			"public key length %d, want %d: %w",
			len(key.PublicKey),
			ed25519.PublicKeySize,
			domain.ErrInvalid,
		)
	}
	privateKey := ed25519.NewKeyFromSeed(key.PrivateKey)
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || !bytes.Equal(publicKey, key.PublicKey) {
		return fmt.Errorf("public key does not match private key seed: %w", domain.ErrInvalid)
	}
	return nil
}

func (rt *restoreTx) restoreUserSettings(ctx context.Context, settings []domain.UserSetting) error {
	for _, s := range settings {
		exists, err := rowExists(
			ctx, rt.tx,
			`SELECT 1 FROM user_setting WHERE user_id = ? AND setting_key = ?`,
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
			`INSERT INTO user_setting (user_id, setting_key, setting_value)
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

// restoreActivity restores adjustments, orders (with approvals), order events,
// execution reports and trades while resolving portable links. Orders land
// before events, reports land after their events, and report-event links land
// after both parent rows exist.
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
	for _, report := range data.ExecutionReports {
		if err := rt.restoreExecutionReport(ctx, report); err != nil {
			return err
		}
	}
	for _, link := range data.ExecutionReportEvents {
		if err := rt.restoreExecutionReportEvent(ctx, link); err != nil {
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
		ctx, rt.tx, `SELECT 1 FROM adjustment WHERE external_id = ?`, rec.ExternalID.Bytes(),
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
	sourceID, err := rt.dictionaries.id(sourceKindTable, "source", string(storedSource(rec.Source)))
	if err != nil {
		return fmt.Errorf(
			"store: restore %s adjustment %q source: %w",
			backup.SectionActivityHistory,
			rec.ExternalID,
			err,
		)
	}
	statusID, err := rt.dictionaries.id(
		adjustmentStatusTable, "adjustment status", string(adjustmentStatus(rec)),
	)
	if err != nil {
		return fmt.Errorf(
			"store: restore %s adjustment %q status: %w",
			backup.SectionActivityHistory,
			rec.ExternalID,
			err,
		)
	}
	if _, err := rt.tx.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO adjustment
		 (external_id, account_id, asset_id, principal_id, at, source_id, status_id, request, outcome)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ExternalID.Bytes(), accountID, assetID, principalID,
		atOrNow(rec.At), sourceID, statusID,
		string(reqJSON), string(outcomeJSON),
	); err != nil {
		return fmt.Errorf("store: restore adjustment %q: %w", rec.ExternalID, err)
	}
	rt.summary.AddApplied(backup.SectionActivityHistory, 1)
	return nil
}

func (rt *restoreTx) restoreOrder(ctx context.Context, rec backup.OrderRecord) error {
	o := rec.Order
	if o.Leaves != "" {
		if _, err := domain.ParseOpenQuantity(o.Leaves); err != nil {
			return fmt.Errorf(
				"store: restore %s order %q leaves: %w",
				backup.SectionActivityHistory,
				o.ExternalID,
				err,
			)
		}
	}
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
		ctx, rt.tx, `SELECT 1 FROM order_record WHERE external_id = ?`, o.ExternalID.Bytes(),
	)
	if err != nil {
		return err
	}
	if rt.skipMachine(backup.SectionActivityHistory, exists) {
		return nil
	}
	sourceID, err := rt.dictionaries.id(sourceKindTable, "source", string(storedSource(o.Source)))
	if err != nil {
		return fmt.Errorf(
			"store: restore %s order %q source: %w",
			backup.SectionActivityHistory,
			o.ExternalID,
			err,
		)
	}
	sideID, err := rt.dictionaries.id(orderSideTable, "order side", string(o.Side))
	if err != nil {
		return fmt.Errorf(
			"store: restore %s order %q side: %w",
			backup.SectionActivityHistory,
			o.ExternalID,
			err,
		)
	}
	amountKindID, err := rt.dictionaries.id(
		orderAmountKindTable, "order amount kind", string(o.AmountKind),
	)
	if err != nil {
		return fmt.Errorf(
			"store: restore %s order %q amount kind: %w",
			backup.SectionActivityHistory,
			o.ExternalID,
			err,
		)
	}
	statusID, err := rt.dictionaries.id(orderStatusTable, "order status", string(o.Status))
	if err != nil {
		return fmt.Errorf(
			"store: restore %s order %q status: %w",
			backup.SectionActivityHistory,
			o.ExternalID,
			err,
		)
	}
	if exists && rt.mode == backup.RestoreModeOverwrite {
		if _, err := rt.tx.ExecContext(
			ctx,
			`UPDATE order_record
			 SET account_id = ?, base_asset_id = ?, quote_asset_id = ?, principal_id = ?,
			     at = ?, source_id = ?, side_id = ?, amount_kind_id = ?, amount_value = ?,
			     leaves_quantity = ?, price = ?,
			     status_id = ?, drop_copy = ?, lock = ?
			 WHERE external_id = ?`,
			accountID, baseID, quoteID, principalID, atOrNow(o.At), sourceID,
			sideID, amountKindID, o.AmountValue,
			o.Leaves, o.Price,
			statusID, o.DropCopy, nullableBlob(o.Lock), o.ExternalID.Bytes(),
		); err != nil {
			return fmt.Errorf("store: restore order %q: %w", o.ExternalID, err)
		}
	} else if _, err := rt.tx.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO order_record
		 (external_id, account_id, base_asset_id, quote_asset_id, principal_id,
		  at, source_id, side_id, amount_kind_id, amount_value,
		  leaves_quantity, price, status_id, drop_copy, lock)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ExternalID.Bytes(), accountID, baseID, quoteID, principalID,
		atOrNow(o.At), sourceID, sideID, amountKindID,
		o.AmountValue, o.Leaves, o.Price,
		statusID, o.DropCopy, nullableBlob(o.Lock),
	); err != nil {
		return fmt.Errorf("store: restore order %q: %w", o.ExternalID, err)
	}
	rt.summary.AddApplied(backup.SectionActivityHistory, 1)
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
		ctx, rt.tx, `SELECT 1 FROM order_event WHERE external_id = ?`, ev.ExternalID.Bytes(),
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
	typeID, err := rt.dictionaries.id(orderEventTypeTable, "order event type", string(ev.Type))
	if err != nil {
		return fmt.Errorf(
			"store: restore %s order event %q type: %w",
			backup.SectionActivityHistory,
			ev.ExternalID,
			err,
		)
	}
	sourceID, err := rt.dictionaries.id(sourceKindTable, "source", string(storedSource(ev.Source)))
	if err != nil {
		return fmt.Errorf(
			"store: restore %s order event %q source: %w",
			backup.SectionActivityHistory,
			ev.ExternalID,
			err,
		)
	}
	if exists && rt.mode == backup.RestoreModeOverwrite {
		if _, err := rt.tx.ExecContext(
			ctx,
			`UPDATE order_event
			 SET order_id = ?, principal_id = ?, at = ?, type_id = ?, source_id = ?, payload = ?
			 WHERE external_id = ?`,
			orderID, principalID, atOrNow(ev.At), typeID, sourceID,
			string(payloadJSON), ev.ExternalID.Bytes(),
		); err != nil {
			return fmt.Errorf("store: restore order event %q: %w", ev.ExternalID, err)
		}
	} else if _, err := rt.tx.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO order_event
		 (external_id, order_id, principal_id, at, type_id, source_id, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ev.ExternalID.Bytes(), orderID, principalID, atOrNow(ev.At),
		typeID, sourceID, string(payloadJSON),
	); err != nil {
		return fmt.Errorf("store: restore order event %q: %w", ev.ExternalID, err)
	}
	if err := rt.restoreEventAttestation(ctx, ev); err != nil {
		return err
	}
	rt.summary.AddApplied(backup.SectionActivityHistory, 1)
	return nil
}

func (rt *restoreTx) restoreExecutionReport(
	ctx context.Context,
	report backup.ExecutionReportRecord,
) error {
	orderID, err := lookupOrderID(ctx, rt.tx, report.Order)
	if err != nil {
		return err
	}
	exists, err := rowExists(
		ctx,
		rt.tx,
		`SELECT 1 FROM execution_report WHERE external_id = ?`,
		report.ExternalID.Bytes(),
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
			`UPDATE execution_report SET order_id = ?, at = ? WHERE external_id = ?`,
			orderID,
			atOrNow(report.At),
			report.ExternalID.Bytes(),
		); err != nil {
			return fmt.Errorf(
				"store: restore execution report %q: %w", report.ExternalID, err,
			)
		}
		// A portable report id may collide with a target report attached to a
		// different order. Keep target-only links only when they still satisfy the
		// report/event same-order invariant after the overwrite.
		if _, err := rt.tx.ExecContext(
			ctx,
			`DELETE FROM execution_report_event
			 WHERE report_id = (
			   SELECT id FROM execution_report WHERE external_id = ?
			 )
			 AND event_id IN (
			   SELECT id FROM order_event WHERE order_id <> ?
			 )`,
			report.ExternalID.Bytes(),
			orderID,
		); err != nil {
			return fmt.Errorf(
				"store: reconcile execution report %q links: %w",
				report.ExternalID,
				err,
			)
		}
	} else if _, err := rt.tx.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO execution_report (external_id, order_id, at) VALUES (?, ?, ?)`,
		report.ExternalID.Bytes(),
		orderID,
		atOrNow(report.At),
	); err != nil {
		return fmt.Errorf(
			"store: restore execution report %q: %w", report.ExternalID, err,
		)
	}
	rt.summary.AddApplied(backup.SectionActivityHistory, 1)
	return nil
}

func (rt *restoreTx) restoreExecutionReportEvent(
	ctx context.Context,
	link backup.ExecutionReportEventLink,
) error {
	var reportRowID, reportOrderID int64
	err := rt.tx.QueryRowContext(
		ctx,
		`SELECT id, order_id FROM execution_report WHERE external_id = ?`,
		link.Report.Bytes(),
	).Scan(&reportRowID, &reportOrderID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("execution report %q: %w", link.Report, domain.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("store: resolve execution report %q: %w", link.Report, err)
	}
	eventRowID, err := lookupOrderEventID(ctx, rt.tx, link.Event)
	if err != nil {
		return err
	}
	var eventOrderID int64
	if err := rt.tx.QueryRowContext(
		ctx,
		`SELECT order_id FROM order_event WHERE id = ?`,
		eventRowID,
	).Scan(&eventOrderID); err != nil {
		return fmt.Errorf("store: resolve order for event %q: %w", link.Event, err)
	}
	if reportOrderID != eventOrderID {
		return fmt.Errorf(
			"execution report %q and event %q belong to different orders: %w",
			link.Report,
			link.Event,
			domain.ErrInvalid,
		)
	}
	exists, err := rowExists(
		ctx,
		rt.tx,
		`SELECT 1 FROM execution_report_event WHERE event_id = ?`,
		eventRowID,
	)
	if err != nil {
		return err
	}
	if rt.skipMachine(backup.SectionActivityHistory, exists) {
		return nil
	}
	if _, err := rt.tx.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO execution_report_event (report_id, event_id) VALUES (?, ?)`,
		reportRowID,
		eventRowID,
	); err != nil {
		return fmt.Errorf(
			"store: restore execution report %q event %q: %w",
			link.Report,
			link.Event,
			err,
		)
	}
	rt.summary.AddApplied(backup.SectionActivityHistory, 1)
	return nil
}

// restoreEventAttestation writes the event's 1:1 attestation when present. The
// event's surrogate id is resolved after the event insert (INSERT OR REPLACE may
// have re-keyed it), then the attestation is upserted onto it, mirroring the
// former order-approval restore.
func (rt *restoreTx) restoreEventAttestation(
	ctx context.Context, ev domain.OrderEvent,
) error {
	if ev.Attestation == nil {
		return nil
	}
	eventID, err := lookupOrderEventID(ctx, rt.tx, ev.ExternalID)
	if err != nil {
		return err
	}
	att := ev.Attestation
	var signingKeyID sql.NullInt64
	if att.KeyID != "" {
		id, err := resolveSigningKeyID(ctx, rt.tx, att.KeyID)
		if err != nil {
			return err
		}
		signingKeyID = sql.NullInt64{Int64: id, Valid: true}
	}
	algID, err := rt.dictionaries.id(attestationAlgTable, "attestation algorithm", att.Alg)
	if err != nil {
		return fmt.Errorf(
			"store: restore %s event attestation %q algorithm: %w",
			backup.SectionActivityHistory,
			ev.ExternalID,
			err,
		)
	}
	requestTypeID, err := rt.dictionaries.id(
		attestationRequestTypeTable, "attestation request type", string(att.RequestType),
	)
	if err != nil {
		return fmt.Errorf(
			"store: restore %s event attestation %q request type: %w",
			backup.SectionActivityHistory,
			ev.ExternalID,
			err,
		)
	}
	modeID, err := rt.dictionaries.id(attestationModeTable, "attestation mode", att.Mode)
	if err != nil {
		return fmt.Errorf(
			"store: restore %s event attestation %q mode: %w",
			backup.SectionActivityHistory,
			ev.ExternalID,
			err,
		)
	}
	if _, err := rt.tx.ExecContext(
		ctx,
		`INSERT INTO event_attestation
		 (event_id, token, signing_key_id, alg_id, request_type_id, mode_id, issued_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(event_id) DO UPDATE SET
		   token = excluded.token,
		   signing_key_id = excluded.signing_key_id,
		   alg_id = excluded.alg_id,
		   request_type_id = excluded.request_type_id,
		   mode_id = excluded.mode_id,
		   issued_at = excluded.issued_at`,
		eventID, att.Token, signingKeyID, algID, requestTypeID, modeID, att.IssuedAt,
	); err != nil {
		return fmt.Errorf("store: restore event attestation %q: %w", ev.ExternalID, err)
	}
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
		ctx, rt.tx, `SELECT 1 FROM trade WHERE external_id = ?`, t.ExternalID.Bytes(),
	)
	if err != nil {
		return err
	}
	if rt.skipMachine(backup.SectionActivityHistory, exists) {
		return nil
	}
	// Validate the commission before persisting: an archived one-sided or
	// malformed commission is failed here rather than deferred to a later read,
	// where a single bad row breaks the CommissionSubtotals rollup.
	if err := validateCommission(t.Commission); err != nil {
		return fmt.Errorf("store: restore trade %q: %w", t.ExternalID, err)
	}
	sourceID, err := rt.dictionaries.id(sourceKindTable, "source", string(storedSource(t.Source)))
	if err != nil {
		return fmt.Errorf(
			"store: restore %s trade %q source: %w",
			backup.SectionActivityHistory,
			t.ExternalID,
			err,
		)
	}
	sideID, err := rt.dictionaries.id(orderSideTable, "order side", string(t.Side))
	if err != nil {
		return fmt.Errorf(
			"store: restore %s trade %q side: %w",
			backup.SectionActivityHistory,
			t.ExternalID,
			err,
		)
	}
	if _, err := rt.tx.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO trade
		 (external_id, order_id, account_id, base_asset_id, quote_asset_id,
		  principal_id, at, source_id, side_id, quantity, price, lock_price,
		  commission_amount, commission_currency)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ExternalID.Bytes(), orderID, accountID, baseID, quoteID, principalID,
		atOrNow(t.At), sourceID, sideID, t.Quantity, t.Price, t.LockPrice,
		commissionAmount(t.Commission), commissionCurrency(t.Commission),
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
		actionID, err := rt.dictionaries.id(auditActionTable, "audit action", string(row.Action))
		if err != nil {
			return fmt.Errorf(
				"store: restore %s audit %q action: %w",
				backup.SectionAuditLog,
				row.ExternalID,
				err,
			)
		}
		sourceID, err := rt.dictionaries.id(sourceKindTable, "source", string(source))
		if err != nil {
			return fmt.Errorf(
				"store: restore %s audit %q source: %w",
				backup.SectionAuditLog,
				row.ExternalID,
				err,
			)
		}
		if _, err := rt.tx.ExecContext(
			ctx,
			`INSERT OR REPLACE INTO audit
			 (external_id, account_code, account_title, asset_code,
			  group_code, actor_code, actor_title, at, action_id, source_id, detail)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.ExternalID.Bytes(), row.Account.String(),
			row.AccountTitle, row.Asset, row.Group, row.Actor, row.ActorTitle,
			atOrNow(row.At), actionID, sourceID, row.Detail,
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
// account/position selectors; the dictionaries asset and principal are never
// pruned (they are shared support rows every section's foreign keys resolve
// against, carried in full rather than as a section).
