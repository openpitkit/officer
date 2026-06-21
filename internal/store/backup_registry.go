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

import "go.openpit.dev/officer/internal/backup"

type backupTableCoverage struct {
	Table           string
	Columns         []string
	ConflictColumns []string
	Section         backup.Section
}

// Section coverage:
// accounts_groups -> accounts, account_groups
// positions -> balances
// risk_limits -> limits
// market_data_settings -> market_data_instances, market_data_instruments
// market_data_quotes -> market_data_quotes
// general_settings -> mcp_access
// user_settings -> user_settings
// activity_history -> orders, order_events, trades, adjustments
// audit_log -> audit
var backupCoverageRegistry = []backupTableCoverage{
	{
		Table: "accounts",
		Columns: []string{
			"tenant", "id", "blocked", "block_reason", "group_id", "notes",
		},
		ConflictColumns: []string{"tenant", "id"},
		Section:         backup.SectionAccountsGroups,
	},
	{
		Table: "account_groups",
		Columns: []string{
			"tenant", "id", "notes", "blocked", "block_reason",
		},
		ConflictColumns: []string{"tenant", "id"},
		Section:         backup.SectionAccountsGroups,
	},
	{
		Table: "limits",
		Columns: []string{
			"tenant", "policy", "scope", "account", "asset", "kind", "value",
		},
		ConflictColumns: []string{
			"tenant", "policy", "scope", "account", "asset", "kind",
		},
		Section: backup.SectionRiskLimits,
	},
	{
		Table: "balances",
		Columns: []string{
			"tenant", "account", "asset", "available", "held", "incoming",
			"realized_pnl", "average_entry_price", "updated_at",
		},
		ConflictColumns: []string{"tenant", "account", "asset"},
		Section:         backup.SectionPositions,
	},
	{
		Table: "adjustments",
		Columns: []string{
			"id", "tenant", "account", "at", "source", "principal", "asset",
			"status", "request", "outcome",
		},
		ConflictColumns: []string{"id"},
		Section:         backup.SectionActivityHistory,
	},
	{
		Table: "orders",
		Columns: []string{
			"id", "tenant", "account", "at", "source", "principal",
			"base_asset", "quote_asset", "side", "amount_kind", "amount_value",
			"price", "status", "lock_prices",
		},
		ConflictColumns: []string{"id"},
		Section:         backup.SectionActivityHistory,
	},
	{
		Table: "order_events",
		Columns: []string{
			"id", "order_id", "at", "type", "source", "principal", "payload",
		},
		ConflictColumns: []string{"id"},
		Section:         backup.SectionActivityHistory,
	},
	{
		Table: "trades",
		Columns: []string{
			"id", "order_id", "tenant", "account", "at", "source", "principal",
			"base_asset", "quote_asset", "side", "quantity", "price",
			"lock_price",
		},
		ConflictColumns: []string{"id"},
		Section:         backup.SectionActivityHistory,
	},
	{
		Table: "audit",
		Columns: []string{
			"id", "at", "actor", "action", "tenant", "account", "detail",
			"source",
		},
		ConflictColumns: []string{"id"},
		Section:         backup.SectionAuditLog,
	},
	{
		Table: "market_data_instances",
		Columns: []string{
			"id", "type", "label", "credentials", "enabled",
		},
		ConflictColumns: []string{"id"},
		Section:         backup.SectionMarketData,
	},
	{
		Table: "market_data_instruments",
		Columns: []string{
			"instance_id", "external_symbol", "base_asset", "quote_asset",
			"manual_price", "enabled",
		},
		ConflictColumns: []string{"instance_id", "external_symbol"},
		Section:         backup.SectionMarketData,
	},
	{
		Table: "market_data_quotes",
		Columns: []string{
			"instance_id", "external_symbol", "base_asset", "quote_asset",
			"mark", "bid", "ask", "as_of", "received_at",
		},
		ConflictColumns: []string{"instance_id", "external_symbol"},
		Section:         backup.SectionMarketDataQuotes,
	},
	{
		Table:           "mcp_access",
		Columns:         []string{"command", "enabled"},
		ConflictColumns: []string{"command"},
		Section:         backup.SectionGeneralSettings,
	},
	{
		Table:           "user_settings",
		Columns:         []string{"user_id", "setting_key", "setting_value"},
		ConflictColumns: []string{"user_id", "setting_key"},
		Section:         backup.SectionUserSettings,
	},
}

func backupRegistryTables() []backupTableCoverage {
	tables := make([]backupTableCoverage, 0, len(backupCoverageRegistry))
	for _, table := range backupCoverageRegistry {
		tables = append(tables, cloneBackupTable(table))
	}
	return tables
}

func mustBackupTable(table string) backupTableCoverage {
	for _, candidate := range backupCoverageRegistry {
		if candidate.Table == table {
			return cloneBackupTable(candidate)
		}
	}
	panic("store: missing backup registry table " + table)
}

func backupTableColumns(table string) []string {
	return mustBackupTable(table).Columns
}

func backupTableConflictColumns(table string) []string {
	return mustBackupTable(table).ConflictColumns
}

func backupTableSection(table string) backup.Section {
	return mustBackupTable(table).Section
}

func cloneBackupTable(table backupTableCoverage) backupTableCoverage {
	table.Columns = append([]string(nil), table.Columns...)
	table.ConflictColumns = append([]string(nil), table.ConflictColumns...)
	return table
}
