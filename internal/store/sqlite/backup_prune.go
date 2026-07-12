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

// Backup group of the SQLite store: the pruning helpers restoreTx.prune uses to
// remove rows outside the archive scope under a replace-all restore, plus the
// small shared scanning and comparison utilities they and the restore helpers use.

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
)

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

// pruneAccountsGroups deletes the in-scope account the archive omits (their
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
			ctx, `DELETE FROM account WHERE code = ?`, p.a,
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
	groups, err := scanStrings(rt.tx.QueryContext(ctx, `SELECT code FROM account_group`))
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
			ctx, `DELETE FROM account_group WHERE code = ?`, code,
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
		 FROM account a
		 LEFT JOIN account_group g ON g.id = a.group_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: prune account groups scan: %w", err)
	}
	return scanCodePairs(rows)
}

// prunePositions deletes the in-scope balance the archive omits. The resolved
// positions scope decides which balance are in scope; an All/empty selector
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
		 FROM balance b
		 JOIN account a   ON a.id   = b.account_id
		 JOIN asset   ast ON ast.id = b.asset_id`,
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
			`DELETE FROM balance
			 WHERE account_id = (SELECT id FROM account WHERE code = ?)
			   AND asset_id   = (SELECT id FROM asset   WHERE code = ?)`,
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
	spotFundsKeep := make(map[string]bool, len(data.SpotFundsPnlBoundsLimits))
	for _, l := range data.SpotFundsPnlBoundsLimits {
		spotFundsKeep[spotFundsPnlBoundsLimitKey(
			l.Scope,
			l.Account,
			l.AccountGroup,
		)] = true
	}
	groupScope := make(map[string]bool, len(selector.Groups)+len(data.Accounts))
	for _, group := range selector.Groups {
		groupScope[group] = true
	}
	for _, account := range data.Accounts {
		if accounts.in(account.Code) && account.GroupCode != "" {
			groupScope[account.GroupCode] = true
		}
	}
	return rt.pruneSpotFundsPnlBoundsLimits(
		ctx,
		selector,
		accounts,
		groupScope,
		spotFundsKeep,
	)
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
		 LEFT JOIN account a   ON a.id   = t.account_id
		 LEFT JOIN asset   ast ON ast.id = t.asset_id`,
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

func (rt *restoreTx) pruneSpotFundsPnlBoundsLimits(
	ctx context.Context,
	selector backup.EntitySelector,
	accounts accountScope,
	groupScope map[string]bool,
	keep map[string]bool,
) error {
	rows, err := rt.tx.QueryContext(
		ctx,
		`SELECT t.id, t.scope, a.code, g.code
		 FROM limit_spot_funds_pnl_bound t
		 LEFT JOIN account       a  ON a.id  = t.account_id
		 LEFT JOIN account_group g  ON g.id  = t.account_group_id`,
	)
	if err != nil {
		return fmt.Errorf("store: prune limit_spot_funds_pnl_bound scan: %w", err)
	}
	type limitRow struct {
		id                    int64
		scope, account, group string
	}
	out := make([]limitRow, 0)
	for rows.Next() {
		var (
			id             int64
			scopeStr       string
			account, group sql.NullString
		)
		if err := rows.Scan(&id, &scopeStr, &account, &group); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: prune limit_spot_funds_pnl_bound row: %w", err)
		}
		out = append(out, limitRow{
			id:      id,
			scope:   scopeStr,
			account: account.String,
			group:   group.String,
		})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("store: prune limit_spot_funds_pnl_bound iterate: %w", err)
	}
	_ = rows.Close()
	for _, l := range out {
		if keep[spotFundsPnlBoundsLimitKey(
			l.scope,
			domain.AccountID(l.account),
			l.group,
		)] || !spotFundsPnlBoundsAxisInScope(
			selector,
			accounts,
			groupScope,
			l.account,
			l.group,
		) {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM limit_spot_funds_pnl_bound WHERE id = ?`, l.id,
		); err != nil {
			return fmt.Errorf(
				"store: prune limit_spot_funds_pnl_bound id %d: %w", l.id, err,
			)
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

func spotFundsPnlBoundsAxisInScope(
	selector backup.EntitySelector,
	accounts accountScope,
	groupScope map[string]bool,
	account string,
	group string,
) bool {
	if account != "" {
		return limitAxisInScope(selector, accounts, account)
	}
	if group != "" {
		if selector.All || selector.Empty() {
			return true
		}
		return groupScope[group]
	}
	return selector.All || selector.Empty()
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
		 FROM market_data_instrument mdi
		 JOIN market_data_instance i ON i.id = mdi.instance_id`,
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
			ctx, `DELETE FROM market_data_instrument WHERE id = ?`, instr.id,
		); err != nil {
			return fmt.Errorf("store: prune md instrument id %d: %w", instr.id, err)
		}
	}

	keepInst := make(map[string]bool, len(data.MarketDataInstances))
	for _, inst := range data.MarketDataInstances {
		keepInst[inst.ExternalID.String()] = true
	}
	instRows, err := rt.tx.QueryContext(
		ctx, `SELECT id, external_id FROM market_data_instance`,
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
			ctx, `DELETE FROM market_data_instance WHERE id = ?`, inst.id,
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
		 FROM market_data_quote q
		 JOIN market_data_instrument mdi ON mdi.id = q.instrument_id
		 JOIN market_data_instance i ON i.id = mdi.instance_id`,
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
			ctx, `DELETE FROM market_data_quote WHERE instrument_id = ?`, q.instrumentID,
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
	keyIDs, err := scanStrings(rt.tx.QueryContext(ctx, `SELECT key_id FROM signing_key`))
	if err != nil {
		return fmt.Errorf("store: prune signing keys: %w", err)
	}
	for _, keyID := range keyIDs {
		if keepKeys[keyID] {
			continue
		}
		referenced, err := rowExists(
			ctx, rt.tx, `SELECT 1 FROM event_attestation ea
			 JOIN signing_key sk ON sk.id = ea.signing_key_id
			 WHERE sk.key_id = ?`, keyID,
		)
		if err != nil {
			return err
		}
		if referenced {
			continue
		}
		if _, err := rt.tx.ExecContext(
			ctx, `DELETE FROM signing_key WHERE key_id = ?`, keyID,
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
		ctx, `SELECT user_id, setting_key FROM user_setting`,
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
			ctx, `DELETE FROM user_setting WHERE user_id = ? AND setting_key = ?`, p.a, p.b,
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
		ctx, accounts, "adjustment", keepAdj,
	); err != nil {
		return err
	}
	keepOrders := make(map[string]bool, len(data.Orders))
	for _, rec := range data.Orders {
		keepOrders[rec.Order.ExternalID.String()] = true
	}
	return rt.pruneByExternalID(ctx, accounts, "order_record", keepOrders)
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
		 JOIN account a ON a.id = t.account_id`,
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

func spotFundsPnlBoundsLimitKey(
	scope string,
	account domain.AccountID,
	group string,
) string {
	return scope + "\x00" + account.String() + "\x00" + group
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
