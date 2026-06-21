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

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.openpit.dev/officer/internal/backup"
	"go.openpit.dev/officer/internal/domain"
)

type backupQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ExportBackup returns a portable domain archive independent of SQLite.
func (s *sqliteStore) ExportBackup(
	ctx context.Context,
	scope backup.Scope,
) (backup.Archive, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return backup.Archive{}, fmt.Errorf("store: begin export backup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	version, err := schemaVersion(ctx, tx)
	if err != nil {
		return backup.Archive{}, err
	}
	data, err := exportBackupData(ctx, tx)
	if err != nil {
		return backup.Archive{}, err
	}
	archive := backup.NewArchive(
		time.Now().UTC(),
		"sqlite:"+s.Path(),
		version,
		scope,
		data,
	)
	if err := tx.Commit(); err != nil {
		return backup.Archive{}, fmt.Errorf("store: commit export backup: %w", err)
	}
	return archive, nil
}

// RestoreBackup imports a portable domain archive into SQLite.
func (s *sqliteStore) RestoreBackup(
	ctx context.Context,
	archive backup.Archive,
	opts backup.RestoreOptions,
) (backup.RestoreSummary, error) {
	if err := validateRestoreMode(opts.Mode); err != nil {
		return backup.RestoreSummary{}, err
	}
	archive, err := backup.MigrateArchive(archive)
	if err != nil {
		return backup.RestoreSummary{}, err
	}
	currentSchema, err := s.SchemaVersion(ctx)
	if err != nil {
		return backup.RestoreSummary{}, err
	}
	// Keep restores strict until archive data migrations exist for DB deltas.
	if archive.Manifest.SchemaVersion != currentSchema {
		return backup.RestoreSummary{}, fmt.Errorf(
			"backup schema version %d does not match database schema version %d: %w",
			archive.Manifest.SchemaVersion, currentSchema, domain.ErrInvalid,
		)
	}
	opts.Scope = opts.Scope.Normalize()
	data := backup.FilterData(archive.Data, opts.Scope)
	summary := backup.NewSummary()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return backup.RestoreSummary{},
			fmt.Errorf("store: begin restore backup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if opts.Mode == backup.RestoreModeReplaceAll {
		if err := deleteRestoreSections(ctx, tx, opts.Scope); err != nil {
			return backup.RestoreSummary{}, err
		}
	}

	if err := restoreBackupData(ctx, tx, data, opts.Mode, summary); err != nil {
		return backup.RestoreSummary{}, err
	}
	if err := resetAutoSequences(ctx, tx, opts.Mode, opts.Scope, data); err != nil {
		return backup.RestoreSummary{}, err
	}
	if err := tx.Commit(); err != nil {
		return backup.RestoreSummary{},
			fmt.Errorf("store: commit restore backup: %w", err)
	}
	summary.RestartRequired = backup.TouchesRuntime(opts.Scope)
	return summary, nil
}

func validateRestoreMode(mode backup.RestoreMode) error {
	switch mode {
	case backup.RestoreModeReplaceAll,
		backup.RestoreModeOverwrite,
		backup.RestoreModeInsertMissing:
		return nil
	default:
		return fmt.Errorf("store: unknown restore mode %q: %w",
			mode, domain.ErrInvalid)
	}
}

func exportBackupData(ctx context.Context, q backupQueryer) (backup.Data, error) {
	var data backup.Data
	var err error
	if data.Accounts, err = listBackupAccounts(ctx, q); err != nil {
		return backup.Data{}, err
	}
	if data.Groups, err = listBackupGroups(ctx, q); err != nil {
		return backup.Data{}, err
	}
	if data.Limits, err = listBackupLimits(ctx, q); err != nil {
		return backup.Data{}, err
	}
	if data.Balances, err = listBackupBalances(ctx, q); err != nil {
		return backup.Data{}, err
	}
	if data.Adjustments, err = listAllAdjustments(ctx, q); err != nil {
		return backup.Data{}, err
	}
	if data.Orders, err = listAllOrders(ctx, q); err != nil {
		return backup.Data{}, err
	}
	if data.OrderEvents, err = listAllOrderEvents(ctx, q); err != nil {
		return backup.Data{}, err
	}
	if data.Trades, err = listAllTrades(ctx, q); err != nil {
		return backup.Data{}, err
	}
	if data.Audit, err = listAllAudit(ctx, q); err != nil {
		return backup.Data{}, err
	}
	if data.McpAccess, err = listBackupMcpAccess(ctx, q); err != nil {
		return backup.Data{}, err
	}
	if data.UserSettings, err = listBackupUserSettings(ctx, q); err != nil {
		return backup.Data{}, err
	}
	if data.MarketDataInstances, err = listBackupMarketDataInstances(ctx, q); err != nil {
		return backup.Data{}, err
	}
	if data.MarketDataInstruments, err = listAllMarketDataInstruments(ctx, q); err != nil {
		return backup.Data{}, err
	}
	if data.MarketDataQuotes, err = listBackupMarketDataQuotes(ctx, q); err != nil {
		return backup.Data{}, err
	}
	return data, nil
}

func schemaVersion(ctx context.Context, q backupQueryer) (int, error) {
	var version sql.NullInt64
	err := q.QueryRowContext(
		ctx, `SELECT MAX(version) FROM schema_migrations`,
	).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

func backupSelectQuery(table string, orderBy string) string {
	query := "SELECT " + strings.Join(backupTableColumns(table), ", ") +
		" FROM " + table
	if orderBy == "" {
		return query
	}
	return query + " ORDER BY " + orderBy
}

func listBackupAccounts(
	ctx context.Context, q backupQueryer,
) ([]domain.Account, error) {
	rows, err := q.QueryContext(ctx,
		backupSelectQuery("accounts", "tenant, id"))
	if err != nil {
		return nil, fmt.Errorf("store: list accounts for backup: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.Account, 0)
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate backup accounts: %w", err)
	}
	return result, nil
}

func listBackupGroups(
	ctx context.Context, q backupQueryer,
) ([]domain.AccountGroup, error) {
	rows, err := q.QueryContext(ctx,
		backupSelectQuery("account_groups", "tenant, id"))
	if err != nil {
		return nil, fmt.Errorf("store: list groups for backup: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.AccountGroup, 0)
	for rows.Next() {
		group, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate backup groups: %w", err)
	}
	return result, nil
}

func listBackupLimits(ctx context.Context, q backupQueryer) ([]domain.Limit, error) {
	rows, err := q.QueryContext(ctx,
		backupSelectQuery("limits",
			"tenant, policy, scope, account, asset, kind"))
	if err != nil {
		return nil, fmt.Errorf("store: list limits for backup: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return groupLimitRows(rows)
}

func listBackupBalances(
	ctx context.Context, q backupQueryer,
) ([]domain.Balance, error) {
	rows, err := q.QueryContext(ctx,
		backupSelectQuery("balances", "tenant, account, asset"))
	if err != nil {
		return nil, fmt.Errorf("store: list balances for backup: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.Balance, 0)
	for rows.Next() {
		var (
			ten, acc, ast, avail, held, incoming, realizedPnl, avgPx, updatedAt string
		)
		if err := rows.Scan(
			&ten, &acc, &ast, &avail, &held, &incoming, &realizedPnl, &avgPx, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("store: scan backup balance: %w", err)
		}
		t, err := time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("store: parse backup balance updated_at %q: %w",
				updatedAt, err)
		}
		result = append(result, domain.Balance{
			Tenant:            domain.TenantID(ten),
			Account:           domain.AccountID(acc),
			Asset:             ast,
			Available:         avail,
			Held:              held,
			Incoming:          incoming,
			RealizedPnl:       realizedPnl,
			AverageEntryPrice: avgPx,
			UpdatedAt:         t,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate backup balances: %w", err)
	}
	return result, nil
}

func listAllAdjustments(
	ctx context.Context, q backupQueryer,
) ([]domain.AccountAdjustmentRecord, error) {
	rows, err := q.QueryContext(ctx,
		backupSelectQuery("adjustments", "id ASC"))
	if err != nil {
		return nil, fmt.Errorf("store: list all adjustments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.AccountAdjustmentRecord, 0)
	for rows.Next() {
		rec, err := scanAdjustment(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate all adjustments: %w", err)
	}
	return result, nil
}

func listAllOrders(ctx context.Context, q backupQueryer) ([]domain.Order, error) {
	rows, err := q.QueryContext(ctx,
		backupSelectQuery("orders", "id ASC"))
	if err != nil {
		return nil, fmt.Errorf("store: list all orders: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.Order, 0)
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, order)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate all orders: %w", err)
	}
	return result, nil
}

func listAllOrderEvents(
	ctx context.Context, q backupQueryer,
) ([]domain.OrderEvent, error) {
	rows, err := q.QueryContext(ctx,
		backupSelectQuery("order_events", "id ASC"))
	if err != nil {
		return nil, fmt.Errorf("store: list all order events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.OrderEvent, 0)
	for rows.Next() {
		event, err := scanOrderEvent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate all order events: %w", err)
	}
	return result, nil
}

func listAllTrades(ctx context.Context, q backupQueryer) ([]domain.Trade, error) {
	rows, err := q.QueryContext(ctx,
		backupSelectQuery("trades", "id ASC"))
	if err != nil {
		return nil, fmt.Errorf("store: list all trades: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.Trade, 0)
	for rows.Next() {
		trade, err := scanTrade(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, trade)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate all trades: %w", err)
	}
	return result, nil
}

func listAllAudit(ctx context.Context, q backupQueryer) ([]domain.AuditRow, error) {
	rows, err := q.QueryContext(ctx,
		backupSelectQuery("audit", "id ASC"))
	if err != nil {
		return nil, fmt.Errorf("store: list all audit: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.AuditRow, 0)
	for rows.Next() {
		row, err := scanAuditRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate all audit: %w", err)
	}
	return result, nil
}

func listBackupMcpAccess(
	ctx context.Context, q backupQueryer,
) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, backupSelectQuery("mcp_access", ""))
	if err != nil {
		return nil, fmt.Errorf("store: list mcp access for backup: %w", err)
	}
	defer func() { _ = rows.Close() }()

	access := make(map[string]bool)
	for rows.Next() {
		var (
			command string
			enabled bool
		)
		if err := rows.Scan(&command, &enabled); err != nil {
			return nil, fmt.Errorf("store: scan backup mcp access row: %w", err)
		}
		access[command] = enabled
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate backup mcp access rows: %w", err)
	}
	return access, nil
}

func listBackupUserSettings(
	ctx context.Context, q backupQueryer,
) ([]domain.UserSetting, error) {
	rows, err := q.QueryContext(ctx, backupSelectQuery("user_settings", "user_id, setting_key"))
	if err != nil {
		return nil, fmt.Errorf("store: list user settings for backup: %w", err)
	}
	defer func() { _ = rows.Close() }()

	settings := make([]domain.UserSetting, 0)
	for rows.Next() {
		var setting domain.UserSetting
		if err := rows.Scan(&setting.UserID, &setting.Key, &setting.Value); err != nil {
			return nil, fmt.Errorf("store: scan backup user setting row: %w", err)
		}
		settings = append(settings, setting)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate backup user setting rows: %w", err)
	}
	return settings, nil
}

func listBackupMarketDataInstances(
	ctx context.Context, q backupQueryer,
) ([]domain.MarketDataInstance, error) {
	rows, err := q.QueryContext(ctx,
		backupSelectQuery("market_data_instances", "id"))
	if err != nil {
		return nil, fmt.Errorf("store: list market-data instances for backup: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.MarketDataInstance, 0)
	for rows.Next() {
		instance, err := scanMarketDataInstance(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, instance)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate backup market-data instances: %w", err)
	}
	return result, nil
}

func listAllMarketDataInstruments(
	ctx context.Context, q backupQueryer,
) ([]domain.MarketDataInstrument, error) {
	rows, err := q.QueryContext(ctx,
		backupSelectQuery("market_data_instruments",
			"instance_id, external_symbol"))
	if err != nil {
		return nil, fmt.Errorf("store: list all market-data instruments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.MarketDataInstrument, 0)
	for rows.Next() {
		instrument, err := scanMarketDataInstrument(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, instrument)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"store: iterate all market-data instruments: %w", err,
		)
	}
	return result, nil
}

func listBackupMarketDataQuotes(
	ctx context.Context, q backupQueryer,
) ([]domain.MarketDataQuote, error) {
	rows, err := q.QueryContext(ctx,
		backupSelectQuery("market_data_quotes",
			"instance_id, external_symbol"))
	if err != nil {
		return nil, fmt.Errorf("store: list market-data quotes for backup: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.MarketDataQuote, 0)
	for rows.Next() {
		quote, err := scanMarketDataQuote(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, quote)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate backup market-data quotes: %w", err)
	}
	return result, nil
}

func scanAuditRow(rows *sql.Rows) (domain.AuditRow, error) {
	var (
		id                                              int64
		at, actor, action, tenant, account, detail, src string
	)
	if err := rows.Scan(
		&id, &at, &actor, &action, &tenant, &account, &detail, &src,
	); err != nil {
		return domain.AuditRow{}, fmt.Errorf("store: scan audit row: %w", err)
	}
	parsedAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return domain.AuditRow{},
			fmt.Errorf("store: parse audit timestamp %q: %w", at, err)
	}
	return domain.AuditRow{
		At:      parsedAt,
		Actor:   actor,
		Action:  domain.AuditAction(action),
		Tenant:  domain.TenantID(tenant),
		Account: domain.AccountID(account),
		Detail:  detail,
		Source:  domain.Source(src),
		ID:      id,
	}, nil
}

func deleteRestoreSections(
	ctx context.Context,
	tx *sql.Tx,
	scope backup.Scope,
) error {
	if scope.Included(backup.SectionActivityHistory) {
		if err := deleteActivity(ctx, tx, scope.Accounts); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionAuditLog) {
		if err := deleteAccountRows(ctx, tx, "audit", scope.Accounts); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionMarketDataQuotes) ||
		scope.Included(backup.SectionMarketData) {
		if err := deleteAll(ctx, tx, "market_data_quotes"); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionMarketData) {
		for _, table := range []string{
			"market_data_instruments", "market_data_instances",
		} {
			if err := deleteAll(ctx, tx, table); err != nil {
				return err
			}
		}
	}
	if scope.Included(backup.SectionPositions) {
		if err := deleteAccountRows(ctx, tx, "balances", scope.Positions); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionRiskLimits) {
		if err := deleteAccountRows(ctx, tx, "limits", scope.Accounts); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionAccountsGroups) {
		if err := deleteAccountsGroups(ctx, tx, scope.Accounts); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionGeneralSettings) {
		if err := deleteAll(ctx, tx, "mcp_access"); err != nil {
			return err
		}
	}
	if scope.Included(backup.SectionUserSettings) {
		if err := deleteAll(ctx, tx, "user_settings"); err != nil {
			return err
		}
	}
	return nil
}

func deleteAccountsGroups(
	ctx context.Context,
	tx *sql.Tx,
	selector backup.EntitySelector,
) error {
	selector = selector.Normalize()
	if selector.All || selector.Empty() {
		for _, table := range []string{"accounts", "account_groups"} {
			if err := deleteAll(ctx, tx, table); err != nil {
				return err
			}
		}
		return nil
	}
	clauses := make([]string, 0, 2)
	args := make([]any, 0, len(selector.Accounts)+len(selector.Groups))
	if len(selector.Accounts) > 0 {
		clauses = append(clauses, "id IN ("+placeholders(len(selector.Accounts))+")")
		for _, account := range selector.Accounts {
			args = append(args, account)
		}
	}
	if len(selector.Groups) > 0 {
		clauses = append(clauses,
			"group_id IN ("+placeholders(len(selector.Groups))+")")
		for _, group := range selector.Groups {
			args = append(args, group)
		}
	}
	if err := deleteWhere(ctx, tx, "accounts",
		strings.Join(clauses, " OR "), args...); err != nil {
		return err
	}
	if len(selector.Groups) == 0 {
		return nil
	}
	args = args[:0]
	for _, group := range selector.Groups {
		args = append(args, group)
	}
	return deleteWhere(ctx, tx, "account_groups",
		"id IN ("+placeholders(len(selector.Groups))+")", args...)
}

func deleteActivity(
	ctx context.Context,
	tx *sql.Tx,
	selector backup.EntitySelector,
) error {
	predicate, args, all := accountPredicate(selector, "account")
	if all {
		for _, table := range []string{
			"order_events", "trades", "orders", "adjustments",
		} {
			if err := deleteAll(ctx, tx, table); err != nil {
				return err
			}
		}
		return nil
	}
	orderPredicate := "order_id IN (SELECT id FROM orders WHERE " + predicate + ")"
	if err := deleteWhere(ctx, tx, "order_events", orderPredicate, args...); err != nil {
		return err
	}
	tradeArgs := make([]any, 0, len(args)*2)
	tradeArgs = append(tradeArgs, args...)
	tradeArgs = append(tradeArgs, args...)
	if err := deleteWhere(ctx, tx, "trades",
		"("+predicate+") OR "+orderPredicate, tradeArgs...); err != nil {
		return err
	}
	if err := deleteWhere(ctx, tx, "orders", predicate, args...); err != nil {
		return err
	}
	return deleteWhere(ctx, tx, "adjustments", predicate, args...)
}

func deleteAccountRows(
	ctx context.Context,
	tx *sql.Tx,
	table string,
	selector backup.EntitySelector,
) error {
	predicate, args, all := accountPredicate(selector, "account")
	if all {
		return deleteAll(ctx, tx, table)
	}
	return deleteWhere(ctx, tx, table, predicate, args...)
}

func deleteAll(ctx context.Context, tx *sql.Tx, table string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
		return fmt.Errorf("store: delete %s for restore: %w", table, err)
	}
	return nil
}

func deleteWhere(
	ctx context.Context,
	tx *sql.Tx,
	table string,
	predicate string,
	args ...any,
) error {
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM "+table+" WHERE "+predicate, args...); err != nil {
		return fmt.Errorf("store: delete %s for restore: %w", table, err)
	}
	return nil
}

func accountPredicate(
	selector backup.EntitySelector,
	column string,
) (string, []any, bool) {
	selector = selector.Normalize()
	if selector.All || selector.Empty() {
		return "", nil, true
	}
	clauses := make([]string, 0, 2)
	args := make([]any, 0, len(selector.Accounts)+len(selector.Groups))
	if len(selector.Accounts) > 0 {
		clauses = append(clauses, column+" IN ("+
			placeholders(len(selector.Accounts))+")")
		for _, account := range selector.Accounts {
			args = append(args, account)
		}
	}
	if len(selector.Groups) > 0 {
		clauses = append(clauses, column+
			" IN (SELECT id FROM accounts WHERE group_id IN ("+
			placeholders(len(selector.Groups))+"))")
		for _, group := range selector.Groups {
			args = append(args, group)
		}
	}
	return strings.Join(clauses, " OR "), args, false
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func restoreBackupData(
	ctx context.Context,
	tx *sql.Tx,
	data backup.Data,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	if err := restoreGroups(ctx, tx, data.Groups, mode, summary); err != nil {
		return err
	}
	if err := restoreAccounts(ctx, tx, data.Accounts, mode, summary); err != nil {
		return err
	}
	if err := restoreLimits(ctx, tx, data.Limits, mode, summary); err != nil {
		return err
	}
	if err := restoreBalances(ctx, tx, data.Balances, mode, summary); err != nil {
		return err
	}
	if err := restoreMarketDataInstances(
		ctx, tx, data.MarketDataInstances, mode, summary,
	); err != nil {
		return err
	}
	if err := restoreMarketDataInstruments(
		ctx, tx, data.MarketDataInstruments, mode, summary,
	); err != nil {
		return err
	}
	if err := restoreMarketDataQuotes(
		ctx, tx, data.MarketDataQuotes, mode, summary,
	); err != nil {
		return err
	}
	if err := restoreMcpAccess(ctx, tx, data.McpAccess, mode, summary); err != nil {
		return err
	}
	if err := restoreUserSettings(
		ctx, tx, data.UserSettings, mode, summary,
	); err != nil {
		return err
	}
	if err := restoreAdjustments(
		ctx, tx, data.Adjustments, mode, summary,
	); err != nil {
		return err
	}
	if err := restoreOrders(ctx, tx, data.Orders, mode, summary); err != nil {
		return err
	}
	if err := restoreOrderEvents(
		ctx, tx, data.OrderEvents, mode, summary,
	); err != nil {
		return err
	}
	if err := restoreTrades(ctx, tx, data.Trades, mode, summary); err != nil {
		return err
	}
	if err := restoreAudit(ctx, tx, data.Audit, mode, summary); err != nil {
		return err
	}
	return nil
}

func insertVerb(mode backup.RestoreMode) (string, error) {
	switch mode {
	case backup.RestoreModeReplaceAll:
		return "INSERT OR REPLACE", nil
	case backup.RestoreModeOverwrite:
		return "INSERT", nil
	case backup.RestoreModeInsertMissing:
		return "INSERT OR IGNORE", nil
	default:
		return "", fmt.Errorf("store: unknown restore mode %q: %w",
			mode, domain.ErrInvalid)
	}
}

func restoreInsertQuery(
	mode backup.RestoreMode,
	table string,
	columns []string,
	conflictColumns []string,
) (string, error) {
	verb, err := insertVerb(mode)
	if err != nil {
		return "", err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(columns)), ", ")
	query := fmt.Sprintf(
		"%s INTO %s (%s) VALUES (%s)",
		verb, table, strings.Join(columns, ", "), placeholders,
	)
	if mode != backup.RestoreModeOverwrite {
		return query, nil
	}

	conflictSet := make(map[string]struct{}, len(conflictColumns))
	for _, column := range conflictColumns {
		conflictSet[column] = struct{}{}
	}
	updates := make([]string, 0, len(columns)-len(conflictColumns))
	for _, column := range columns {
		if _, ok := conflictSet[column]; ok {
			continue
		}
		updates = append(updates, column+" = excluded."+column)
	}
	if len(updates) == 0 {
		// Defensive for future key-only registry entries; current backup tables
		// all have at least one non-key column to update.
		return query + " ON CONFLICT(" +
			strings.Join(conflictColumns, ", ") + ") DO NOTHING", nil
	}
	return query + " ON CONFLICT(" + strings.Join(conflictColumns, ", ") +
		") DO UPDATE SET " + strings.Join(updates, ", "), nil
}

func restoreInsertQueryForTable(
	mode backup.RestoreMode,
	table string,
) (string, error) {
	return restoreInsertQuery(
		mode,
		table,
		backupTableColumns(table),
		backupTableConflictColumns(table),
	)
}

func restoreGroups(
	ctx context.Context,
	tx *sql.Tx,
	groups []domain.AccountGroup,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	const table = "account_groups"
	query, err := restoreInsertQueryForTable(mode, table)
	if err != nil {
		return err
	}
	for _, group := range groups {
		n, err := execCount(ctx, tx,
			query,
			group.Tenant.String(), group.ID, group.Notes,
			group.Blocked, group.BlockReason,
		)
		if err != nil {
			return fmt.Errorf("store: restore group %q: %w", group.ID, err)
		}
		addRestoreCount(summary, backupTableSection(table), n)
	}
	return nil
}

func restoreAccounts(
	ctx context.Context,
	tx *sql.Tx,
	accounts []domain.Account,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	const table = "accounts"
	query, err := restoreInsertQueryForTable(mode, table)
	if err != nil {
		return err
	}
	for _, account := range accounts {
		n, err := execCount(ctx, tx,
			query,
			account.Tenant.String(), account.ID.String(), account.Blocked,
			account.BlockReason, account.GroupID, account.Notes,
		)
		if err != nil {
			return fmt.Errorf("store: restore account %q: %w", account.ID, err)
		}
		addRestoreCount(summary, backupTableSection(table), n)
	}
	return nil
}

func restoreLimits(
	ctx context.Context,
	tx *sql.Tx,
	limits []domain.Limit,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	const table = "limits"
	query, err := restoreInsertQueryForTable(mode, table)
	if err != nil {
		return err
	}
	for _, limit := range limits {
		if mode == backup.RestoreModeInsertMissing {
			exists, err := limitExists(ctx, tx, limit.Target)
			if err != nil {
				return err
			}
			if exists {
				summary.AddSkipped(backupTableSection(table), len(limit.Values))
				continue
			}
		}
		for _, value := range limit.Values {
			n, err := execCount(ctx, tx,
				query,
				limit.Target.Tenant.String(), limit.Target.Policy,
				limit.Target.Scope, limit.Target.Account.String(),
				limit.Target.Asset, value.Kind, value.Value,
			)
			if err != nil {
				return fmt.Errorf("store: restore limit: %w", err)
			}
			addRestoreCount(summary, backupTableSection(table), n)
		}
	}
	return nil
}

func restoreBalances(
	ctx context.Context,
	tx *sql.Tx,
	balances []domain.Balance,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	const table = "balances"
	query, err := restoreInsertQueryForTable(mode, table)
	if err != nil {
		return err
	}
	for _, balance := range balances {
		n, err := execCount(ctx, tx,
			query,
			balance.Tenant.String(), balance.Account.String(), balance.Asset,
			orZero(balance.Available), orZero(balance.Held),
			orZero(balance.Incoming), orZero(balance.RealizedPnl),
			balance.AverageEntryPrice,
			balance.UpdatedAt.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("store: restore balance: %w", err)
		}
		addRestoreCount(summary, backupTableSection(table), n)
	}
	return nil
}

func restoreMarketDataInstances(
	ctx context.Context,
	tx *sql.Tx,
	instances []domain.MarketDataInstance,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	const table = "market_data_instances"
	query, err := restoreInsertQueryForTable(mode, table)
	if err != nil {
		return err
	}
	for _, instance := range instances {
		n, err := execCount(ctx, tx,
			query,
			instance.ID, instance.Type, instance.Label,
			instance.Credentials, instance.Enabled,
		)
		if err != nil {
			return fmt.Errorf(
				"store: restore market-data instance %q: %w", instance.ID, err,
			)
		}
		addRestoreCount(summary, backupTableSection(table), n)
	}
	return nil
}

func restoreMarketDataInstruments(
	ctx context.Context,
	tx *sql.Tx,
	instruments []domain.MarketDataInstrument,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	const table = "market_data_instruments"
	query, err := restoreInsertQueryForTable(mode, table)
	if err != nil {
		return err
	}
	for _, instrument := range instruments {
		n, err := execCount(ctx, tx,
			query,
			instrument.InstanceID, instrument.ExternalSymbol,
			instrument.BaseAsset, instrument.QuoteAsset,
			instrument.ManualPrice, instrument.Enabled,
		)
		if err != nil {
			return fmt.Errorf("store: restore market-data instrument: %w", err)
		}
		addRestoreCount(summary, backupTableSection(table), n)
	}
	return nil
}

func restoreMarketDataQuotes(
	ctx context.Context,
	tx *sql.Tx,
	quotes []domain.MarketDataQuote,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	const table = "market_data_quotes"
	query, err := restoreInsertQueryForTable(mode, table)
	if err != nil {
		return err
	}
	section := backupTableSection(table)
	for _, quote := range quotes {
		exists, err := marketDataInstrumentExists(ctx, tx,
			quote.InstanceID, quote.ExternalSymbol)
		if err != nil {
			return err
		}
		if !exists {
			summary.AddSkipped(section, 1)
			continue
		}
		n, err := execCount(ctx, tx,
			query,
			quote.InstanceID, quote.ExternalSymbol,
			quote.BaseAsset, quote.QuoteAsset,
			quote.Mark, quote.Bid, quote.Ask,
			quote.AsOf.UTC().Format(time.RFC3339Nano),
			quote.ReceivedAt.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("store: restore market-data quote: %w", err)
		}
		addRestoreCount(summary, section, n)
	}
	return nil
}

func restoreMcpAccess(
	ctx context.Context,
	tx *sql.Tx,
	access map[string]bool,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	const table = "mcp_access"
	query, err := restoreInsertQueryForTable(mode, table)
	if err != nil {
		return err
	}
	commands := make([]string, 0, len(access))
	for command := range access {
		commands = append(commands, command)
	}
	sort.Strings(commands)
	for _, command := range commands {
		n, err := execCount(ctx, tx,
			query,
			command, access[command],
		)
		if err != nil {
			return fmt.Errorf("store: restore mcp access %q: %w", command, err)
		}
		addRestoreCount(summary, backupTableSection(table), n)
	}
	return nil
}

func restoreUserSettings(
	ctx context.Context,
	tx *sql.Tx,
	settings []domain.UserSetting,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	const table = "user_settings"
	query, err := restoreInsertQueryForTable(mode, table)
	if err != nil {
		return err
	}
	for _, setting := range settings {
		n, err := execCount(ctx, tx,
			query,
			setting.UserID, setting.Key, setting.Value,
		)
		if err != nil {
			return fmt.Errorf("store: restore user setting %q/%q: %w",
				setting.UserID, setting.Key, err)
		}
		addRestoreCount(summary, backupTableSection(table), n)
	}
	return nil
}

func restoreAdjustments(
	ctx context.Context,
	tx *sql.Tx,
	adjustments []domain.AccountAdjustmentRecord,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	const table = "adjustments"
	query, err := restoreInsertQueryForTable(mode, table)
	if err != nil {
		return err
	}
	for _, adjustment := range adjustments {
		status, outcome, err := adjustmentOutcomeJSON(adjustment)
		if err != nil {
			return err
		}
		req, err := json.Marshal(adjustment.Request)
		if err != nil {
			return fmt.Errorf("store: marshal adjustment request: %w", err)
		}
		n, err := execCount(ctx, tx,
			query,
			adjustment.ID, adjustment.Tenant.String(),
			adjustment.Account.String(),
			adjustment.At.UTC().Format(time.RFC3339Nano),
			string(adjustment.Source), adjustment.Principal,
			adjustment.Request.Asset, string(status), string(req), string(outcome),
		)
		if err != nil {
			return fmt.Errorf("store: restore adjustment %d: %w",
				adjustment.ID, err)
		}
		addRestoreCount(summary, backupTableSection(table), n)
	}
	return nil
}

func adjustmentOutcomeJSON(
	rec domain.AccountAdjustmentRecord,
) (domain.AdjustmentStatus, []byte, error) {
	if rec.Accepted != nil {
		body, err := json.Marshal(rec.Accepted)
		return domain.AdjustmentStatusAccepted, body, err
	}
	if rec.Rejected != nil {
		body, err := json.Marshal(rec.Rejected)
		return domain.AdjustmentStatusRejected, body, err
	}
	return "", nil, fmt.Errorf(
		"store: adjustment %d has no outcome: %w", rec.ID, domain.ErrInvalid,
	)
}

func restoreOrders(
	ctx context.Context,
	tx *sql.Tx,
	orders []domain.Order,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	const table = "orders"
	query, err := restoreInsertQueryForTable(mode, table)
	if err != nil {
		return err
	}
	for _, order := range orders {
		lockPrices, err := json.Marshal(order.LockPrices)
		if err != nil {
			return fmt.Errorf("store: marshal order lock prices: %w", err)
		}
		n, err := execCount(ctx, tx,
			query,
			order.ID, order.Tenant.String(), order.Account.String(),
			order.At.UTC().Format(time.RFC3339Nano),
			string(order.Source), order.Principal,
			order.BaseAsset, order.QuoteAsset, string(order.Side),
			string(order.AmountKind), order.AmountValue,
			order.Price, string(order.Status), string(lockPrices),
		)
		if err != nil {
			return fmt.Errorf("store: restore order %d: %w", order.ID, err)
		}
		addRestoreCount(summary, backupTableSection(table), n)
	}
	return nil
}

func restoreOrderEvents(
	ctx context.Context,
	tx *sql.Tx,
	events []domain.OrderEvent,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	const table = "order_events"
	query, err := restoreInsertQueryForTable(mode, table)
	if err != nil {
		return err
	}
	for _, event := range events {
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			return fmt.Errorf("store: marshal event payload: %w", err)
		}
		n, err := execCount(ctx, tx,
			query,
			event.ID, event.OrderID,
			event.At.UTC().Format(time.RFC3339Nano),
			string(event.Type), string(event.Source), event.Principal,
			string(payload),
		)
		if err != nil {
			return fmt.Errorf("store: restore order event %d: %w",
				event.ID, err)
		}
		addRestoreCount(summary, backupTableSection(table), n)
	}
	return nil
}

func restoreTrades(
	ctx context.Context,
	tx *sql.Tx,
	trades []domain.Trade,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	const table = "trades"
	query, err := restoreInsertQueryForTable(mode, table)
	if err != nil {
		return err
	}
	for _, trade := range trades {
		n, err := execCount(ctx, tx,
			query,
			trade.ID, trade.OrderID, trade.Tenant.String(),
			trade.Account.String(),
			trade.At.UTC().Format(time.RFC3339Nano),
			string(trade.Source), trade.Principal,
			trade.BaseAsset, trade.QuoteAsset, string(trade.Side),
			trade.Quantity, trade.Price, trade.LockPrice,
		)
		if err != nil {
			return fmt.Errorf("store: restore trade %d: %w", trade.ID, err)
		}
		addRestoreCount(summary, backupTableSection(table), n)
	}
	return nil
}

func restoreAudit(
	ctx context.Context,
	tx *sql.Tx,
	rows []domain.AuditRow,
	mode backup.RestoreMode,
	summary backup.RestoreSummary,
) error {
	const table = "audit"
	query, err := restoreInsertQueryForTable(mode, table)
	if err != nil {
		return err
	}
	for _, row := range rows {
		n, err := execCount(ctx, tx,
			query,
			row.ID, row.At.UTC().Format(time.RFC3339Nano),
			row.Actor, string(row.Action), row.Tenant.String(),
			row.Account.String(), row.Detail, string(row.Source),
		)
		if err != nil {
			return fmt.Errorf("store: restore audit row %d: %w", row.ID, err)
		}
		addRestoreCount(summary, backupTableSection(table), n)
	}
	return nil
}

func execCount(
	ctx context.Context,
	tx *sql.Tx,
	query string,
	args ...any,
) (int, error) {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, nil
	}
	if n > math.MaxInt {
		return math.MaxInt, nil
	}
	return int(n), nil
}

func addRestoreCount(
	summary backup.RestoreSummary,
	section backup.Section,
	rows int,
) {
	if rows == 0 {
		summary.AddSkipped(section, 1)
		return
	}
	summary.AddApplied(section, rows)
}

func limitExists(
	ctx context.Context,
	tx *sql.Tx,
	target domain.LimitTarget,
) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM limits
		 WHERE tenant = ? AND policy = ? AND scope = ?
		   AND account = ? AND asset = ?`,
		target.Tenant.String(), target.Policy, target.Scope,
		target.Account.String(), target.Asset,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("store: check existing limit: %w", err)
	}
	return count > 0, nil
}

func marketDataInstrumentExists(
	ctx context.Context,
	tx *sql.Tx,
	instanceID string,
	externalSymbol string,
) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM market_data_instruments
		WHERE instance_id = ? AND external_symbol = ?`,
		instanceID, externalSymbol,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf(
			"store: check existing market-data instrument: %w", err,
		)
	}
	return count > 0, nil
}

func resetAutoSequences(
	ctx context.Context,
	tx *sql.Tx,
	mode backup.RestoreMode,
	scope backup.Scope,
	data backup.Data,
) error {
	for _, table := range restoreSequenceTables(mode, scope, data) {
		if err := resetAutoSequence(ctx, tx, table); err != nil {
			return err
		}
	}
	return nil
}

func restoreSequenceTables(
	mode backup.RestoreMode,
	scope backup.Scope,
	data backup.Data,
) []string {
	include := map[string]bool{}
	if mode == backup.RestoreModeReplaceAll {
		if scope.Included(backup.SectionActivityHistory) {
			include["adjustments"] = true
			include["orders"] = true
			include["order_events"] = true
			include["trades"] = true
		}
		if scope.Included(backup.SectionAuditLog) {
			include["audit"] = true
		}
	}
	if len(data.Adjustments) > 0 {
		include["adjustments"] = true
	}
	if len(data.Orders) > 0 {
		include["orders"] = true
	}
	if len(data.OrderEvents) > 0 {
		include["order_events"] = true
	}
	if len(data.Trades) > 0 {
		include["trades"] = true
	}
	if len(data.Audit) > 0 {
		include["audit"] = true
	}
	tables := []string{"audit", "adjustments", "orders", "order_events", "trades"}
	out := tables[:0]
	for _, table := range tables {
		if include[table] {
			out = append(out, table)
		}
	}
	return out
}

func resetAutoSequence(ctx context.Context, tx *sql.Tx, table string) error {
	var seq int64
	err := tx.QueryRowContext(
		ctx, "SELECT COALESCE(MAX(id), 0) FROM "+table,
	).Scan(&seq)
	if err != nil {
		return fmt.Errorf("store: read %s sequence: %w", table, err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO sqlite_sequence (name, seq) VALUES (?, ?)`,
		table, seq,
	)
	if err != nil {
		return fmt.Errorf("store: reset %s sequence: %w", table, err)
	}
	return nil
}
