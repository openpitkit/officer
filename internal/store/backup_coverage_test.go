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
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/backup"
	"go.openpit.dev/officer/internal/domain"
)

var backupCoverageSQLiteExcludedTables = map[string]string{
	"reservation_intents": "ephemeral approval state rebuilt from live runtime",
	"schema_migrations":   "migration version bookkeeping",
	"signing_config":      "signing configuration is excluded from portable backups",
	"signing_keys":        "private signing key material is excluded from portable backups",
	"sqlite_sequence":     "SQLite AUTOINCREMENT bookkeeping, filtered by sqlite_%",
}

// TestBackupRegistryCoversSQLiteTables introspects SQLite only. The backup
// registry is the portable coverage contract; a future non-SQLite backend must
// add its own schema-introspection guard against the same registry.
func TestBackupRegistryCoversSQLiteTables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openBackupCoverageStore(t)

	live := liveSQLiteTables(t, ctx, s.db)
	registered := backupRegistryTableSet()

	for table := range live {
		if registered[table] || backupCoverageSQLiteExcludedTables[table] != "" {
			continue
		}
		t.Errorf(
			"SQLite table %q is not covered by backup; add it to the "+
				"backup registry, or to the exclusion allowlist",
			table,
		)
	}
	for table := range registered {
		if !live[table] {
			t.Errorf(
				"backup registry table %q is absent from the migrated "+
					"SQLite schema",
				table,
			)
		}
	}
}

func TestBackupRegistryCoversSQLiteColumns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openBackupCoverageStore(t)

	for _, table := range backupRegistryTables() {
		liveColumns := liveSQLiteColumns(t, ctx, s.db, table.Table)
		missingFromRegistry := stringSetDifference(liveColumns, table.Columns)
		missingFromSQLite := stringSetDifference(table.Columns, liveColumns)
		if len(missingFromRegistry) > 0 || len(missingFromSQLite) > 0 {
			t.Errorf(
				"backup registry columns for table %q differ from live "+
					"SQLite schema: missing from registry=%v missing "+
					"from SQLite=%v",
				table.Table,
				missingFromRegistry,
				missingFromSQLite,
			)
		}
	}
}

func TestBackupRegistryCoversSections(t *testing.T) {
	t.Parallel()
	sections := make(map[backup.Section]bool, len(backup.AllSections))
	for _, section := range backup.AllSections {
		sections[section] = false
	}

	for _, table := range backupRegistryTables() {
		if _, ok := sections[table.Section]; !ok {
			t.Errorf(
				"backup registry table %q maps to unknown section %q",
				table.Table,
				table.Section,
			)
			continue
		}
		sections[table.Section] = true
	}
	for section, populated := range sections {
		if !populated {
			t.Errorf(
				"backup section %q has no registry table; add a table "+
					"to the registry or remove the section",
				section,
			)
		}
	}
}

func TestBackupRegistryRoundTripsEveryRegisteredColumn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := openBackupCoverageStore(t)
	seedBackupRoundTripRows(t, ctx, source.db)
	before := readBackupRegistryRows(t, ctx, source.db)

	archive, err := source.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	target := openBackupCoverageStore(t)
	if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true},
		Mode:  backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	after := readBackupRegistryRows(t, ctx, target.db)

	if !reflect.DeepEqual(after, before) {
		t.Fatalf("backup registry round-trip mismatch:\nbefore=%#v\nafter=%#v",
			before, after)
	}
}

func openBackupCoverageStore(t *testing.T) *sqliteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "coverage.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	sqlite, ok := s.(*sqliteStore)
	if !ok {
		t.Fatalf("NewSQLiteStore returned %T, want *sqliteStore", s)
	}
	return sqlite
}

func liveSQLiteTables(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT name
		FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]bool{}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan sqlite_master table: %v", err)
		}
		out[table] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master tables: %v", err)
	}
	return out
}

func liveSQLiteColumns(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	table string,
) []string {
	t.Helper()
	rows, err := db.QueryContext(
		ctx,
		"PRAGMA table_info("+quoteSQLiteIdentifier(table)+")",
	)
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	columns := make([]string, 0)
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	return columns
}

func backupRegistryTableSet() map[string]bool {
	out := make(map[string]bool, len(backupCoverageRegistry))
	for _, table := range backupCoverageRegistry {
		out[table.Table] = true
	}
	return out
}

func stringSetDifference(left []string, right []string) []string {
	rightSet := make(map[string]bool, len(right))
	for _, value := range right {
		rightSet[value] = true
	}
	out := make([]string, 0)
	for _, value := range left {
		if !rightSet[value] {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func seedBackupRoundTripRows(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) {
	t.Helper()
	values := backupRoundTripValues(t)
	for _, table := range backupRegistryTables() {
		tableValues := values[table.Table]
		args := make([]any, 0, len(table.Columns))
		for _, column := range table.Columns {
			value, ok := tableValues[column]
			if !ok {
				t.Fatalf("round-trip fixture missing %s.%s", table.Table, column)
			}
			args = append(args, value)
		}
		query := fmt.Sprintf(
			"INSERT INTO %s (%s) VALUES (%s)",
			quoteSQLiteIdentifier(table.Table),
			quoteSQLiteIdentifierList(table.Columns),
			strings.TrimSuffix(strings.Repeat("?, ", len(table.Columns)), ", "),
		)
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("insert round-trip row into %s: %v", table.Table, err)
		}
	}
}

func backupRoundTripValues(t *testing.T) map[string]map[string]string {
	t.Helper()
	at1 := fixedBackupCoverageTime(1)
	at2 := fixedBackupCoverageTime(2)
	at3 := fixedBackupCoverageTime(3)
	at4 := fixedBackupCoverageTime(4)
	at5 := fixedBackupCoverageTime(5)
	req := mustBackupCoverageJSON(t, domain.AdjustmentRequest{
		Asset:             "EUR",
		AverageEntryPrice: "1.111",
		Balance: &domain.AdjustmentAmount{
			Mode:  domain.AdjustmentModeAbsolute,
			Value: "2.222",
		},
		Held: &domain.AdjustmentAmount{
			Mode:  domain.AdjustmentModeDelta,
			Value: "3.333",
		},
	})
	outcome := mustBackupCoverageJSON(t, domain.AdjustmentOutcomeAccepted{
		BalanceDelta:      "4.444",
		BalanceResult:     "5.555",
		HeldDelta:         "6.666",
		HeldResult:        "7.777",
		IncomingDelta:     "8.888",
		IncomingResult:    "9.999",
		RealizedPnlDelta:  "10.101",
		RealizedPnlResult: "11.111",
	})
	lockPrices := mustBackupCoverageJSON(t, []string{"12.121", "13.131"})
	payload := mustBackupCoverageJSON(t, domain.OrderEventPayload{
		RejectCode:    "sentinel_reject_code",
		RejectScope:   "account",
		RejectPolicy:  "pnl_bounds_kill_switch",
		RejectReason:  "sentinel reject reason",
		RejectDetails: "sentinel reject details",
	})

	// The fixture inserts raw SQL so every registry column is exercised, but
	// JSON payloads still come from domain structs to stay schema-canonical.
	return map[string]map[string]string{
		"account_groups": {
			"tenant":       "tenant-rt",
			"id":           "group-rt",
			"notes":        "group-notes-rt",
			"blocked":      "1",
			"block_reason": "group-block-reason-rt",
		},
		"accounts": {
			"tenant":       "tenant-rt",
			"id":           "account-rt",
			"blocked":      "1",
			"block_reason": "account-block-reason-rt",
			"group_id":     "group-rt",
			"notes":        "account-notes-rt",
		},
		"limits": {
			"tenant":  "tenant-rt",
			"policy":  "rate_limit",
			"scope":   "account_asset",
			"account": "account-rt",
			"asset":   "USD",
			"kind":    "max_orders",
			"value":   "101",
		},
		"balances": {
			"tenant":              "tenant-rt",
			"account":             "account-rt",
			"asset":               "USD",
			"available":           "201.201",
			"held":                "202.202",
			"incoming":            "203.203",
			"realized_pnl":        "204.204",
			"average_entry_price": "205.205",
			"updated_at":          at1,
		},
		"adjustments": {
			"id":        "301",
			"tenant":    "tenant-rt",
			"account":   "account-rt",
			"at":        at2,
			"source":    "api",
			"principal": "adjustment-principal-rt",
			// The indexed asset column mirrors AdjustmentRequest.Asset.
			"asset":   "EUR",
			"status":  "accepted",
			"request": req,
			"outcome": outcome,
		},
		"orders": {
			"id":           "401",
			"tenant":       "tenant-rt",
			"account":      "account-rt",
			"at":           at3,
			"source":       "mcp",
			"principal":    "order-principal-rt",
			"base_asset":   "AAPL",
			"quote_asset":  "USD",
			"side":         "buy",
			"amount_kind":  "quantity",
			"amount_value": "14.141",
			"price":        "15.151",
			"status":       "submitted",
			"lock_prices":  lockPrices,
		},
		"order_events": {
			"id":        "501",
			"order_id":  "401",
			"at":        at4,
			"type":      "pre_trade_rejected",
			"source":    "panel",
			"principal": "event-principal-rt",
			"payload":   payload,
		},
		"trades": {
			"id":          "601",
			"order_id":    "401",
			"tenant":      "tenant-rt",
			"account":     "account-rt",
			"at":          at5,
			"source":      "api",
			"principal":   "trade-principal-rt",
			"base_asset":  "AAPL",
			"quote_asset": "USD",
			"side":        "sell",
			"quantity":    "16.161",
			"price":       "17.171",
			"lock_price":  "18.181",
		},
		"audit": {
			"id":      "701",
			"at":      fixedBackupCoverageTime(6),
			"actor":   "audit-actor-rt",
			"action":  string(domain.AuditActionSetLimit),
			"tenant":  "tenant-rt",
			"account": "account-rt",
			"detail":  "audit-detail-rt",
			"source":  "system",
		},
		"market_data_instances": {
			"id":          "market-data-instance-rt",
			"type":        domain.MarketDataProviderBYO,
			"label":       "Market data instance RT",
			"credentials": `{"token":"credentials-rt"}`,
			"enabled":     "1",
		},
		"market_data_instruments": {
			"instance_id":     "market-data-instance-rt",
			"external_symbol": "AAPLUSD",
			"base_asset":      "AAPL",
			"quote_asset":     "USD",
			"manual_price":    "601.601",
			"enabled":         "1",
		},
		"market_data_quotes": {
			"instance_id":     "market-data-instance-rt",
			"external_symbol": "AAPLUSD",
			"base_asset":      "AAPL",
			"quote_asset":     "USD",
			"mark":            "701.701",
			"bid":             "702.702",
			"ask":             "703.703",
			"as_of":           fixedBackupCoverageTime(7),
			"received_at":     fixedBackupCoverageTime(8),
		},
		"mcp_access": {
			"command": "submit_order",
			"enabled": "1",
		},
		"user_settings": {
			"user_id":       "default",
			"setting_key":   "welcome_seen",
			"setting_value": "1",
		},
	}
}

func fixedBackupCoverageTime(offset int) string {
	return time.Date(
		2026,
		time.June,
		23,
		12,
		0,
		offset,
		123000000,
		time.UTC,
	).Format(time.RFC3339Nano)
}

func mustBackupCoverageJSON(t *testing.T, value any) string {
	t.Helper()
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal backup coverage fixture: %v", err)
	}
	return string(out)
}

func readBackupRegistryRows(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) map[string][][]string {
	t.Helper()
	out := make(map[string][][]string, len(backupCoverageRegistry))
	for _, table := range backupRegistryTables() {
		out[table.Table] = readBackupRegistryTableRows(t, ctx, db, table)
	}
	return out
}

func readBackupRegistryTableRows(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	table backupTableCoverage,
) [][]string {
	t.Helper()
	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		"SELECT %s FROM %s ORDER BY %s",
		castSQLiteColumnsAsText(table.Columns),
		quoteSQLiteIdentifier(table.Table),
		quoteSQLiteIdentifierList(table.ConflictColumns),
	))
	if err != nil {
		t.Fatalf("select round-trip rows from %s: %v", table.Table, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([][]string, 0)
	for rows.Next() {
		values := make([]string, len(table.Columns))
		scan := make([]any, len(values))
		for i := range values {
			scan[i] = &values[i]
		}
		if err := rows.Scan(scan...); err != nil {
			t.Fatalf("scan round-trip row from %s: %v", table.Table, err)
		}
		out = append(out, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate round-trip rows from %s: %v", table.Table, err)
	}
	return out
}

func castSQLiteColumnsAsText(columns []string) string {
	parts := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted := quoteSQLiteIdentifier(column)
		parts = append(parts, "CAST("+quoted+" AS TEXT)")
	}
	return strings.Join(parts, ", ")
}

func quoteSQLiteIdentifierList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, quoteSQLiteIdentifier(value))
	}
	return strings.Join(quoted, ", ")
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
