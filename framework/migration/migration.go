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

// Package migration defines a version-tracked database migration runner.
package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Migration is one ordered, version-tracked schema change.
type Migration struct {
	// Version is monotonic and unique within a source.
	Version int
	// Name is the human-readable identifier recorded in the bookkeeping table.
	Name string
	// SQL is the statement or statements to execute for this migration.
	SQL string
}

// MigrationSource yields a backend's ordered migration set.
type MigrationSource interface {
	// Migrations returns the migrations in ascending Version order.
	Migrations() ([]Migration, error)
}

// Config names the migration bookkeeping table.
type Config struct {
	// Table is the bookkeeping table name. Empty uses schema_migrations.
	Table string
}

const defaultTable = "schema_migrations"

// DefaultConfig returns the default schema_migrations bookkeeping layout.
func DefaultConfig() Config {
	return Config{Table: defaultTable}
}

// ParseVersion extracts the decimal version prefix from a migration filename.
func ParseVersion(name string) (int, error) {
	prefix, _, found := strings.Cut(name, "_")
	if !found {
		return 0, fmt.Errorf(
			"migration: malformed migration name %q (want <version>_<name>.sql)",
			name,
		)
	}
	if prefix == "" {
		return 0, fmt.Errorf("migration: empty migration version in %q", name)
	}
	version := 0
	for _, r := range prefix {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("migration: non-numeric migration version in %q", name)
		}
		version = version*10 + int(r-'0')
	}
	return version, nil
}

// Apply brings db to the latest migration version from source.
func Apply(
	ctx context.Context,
	db *sql.DB,
	source MigrationSource,
	cfg Config,
) error {
	table := tableName(cfg)
	if _, err := db.ExecContext(ctx, schemaMigrationsDDL(table)); err != nil {
		return fmt.Errorf("migration: create %s: %w", table, err)
	}

	migrations, err := source.Migrations()
	if err != nil {
		return err
	}
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	applied, err := appliedVersions(ctx, db, table)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		if err := applyMigration(ctx, db, table, m); err != nil {
			return err
		}
	}
	return nil
}

// SchemaVersion returns the highest applied migration version, or zero.
func SchemaVersion(ctx context.Context, db *sql.DB, cfg Config) (int, error) {
	table := tableName(cfg)
	var version sql.NullInt64
	err := db.QueryRowContext(
		ctx, `SELECT MAX(version) FROM `+table,
	).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("migration: read schema version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

func tableName(cfg Config) string {
	if cfg.Table == "" {
		return defaultTable
	}
	return cfg.Table
}

func schemaMigrationsDDL(table string) string {
	return `CREATE TABLE IF NOT EXISTS ` + table + ` (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`
}

func appliedVersions(
	ctx context.Context, db *sql.DB, table string,
) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM `+table)
	if err != nil {
		return nil, fmt.Errorf("migration: read %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("migration: scan schema version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migration: iterate schema versions: %w", err)
	}
	return applied, nil
}

func applyMigration(
	ctx context.Context, db *sql.DB, table string, m Migration,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration: begin migration %d: %w", m.Version, err)
	}
	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return errors.Join(
			fmt.Errorf(
				"migration: apply migration %d (%s): %w",
				m.Version, m.Name, err,
			),
			tx.Rollback(),
		)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO `+table+` (version, name, applied_at) VALUES (?, ?, ?)`,
		m.Version, m.Name, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return errors.Join(
			fmt.Errorf("migration: record migration %d: %w", m.Version, err),
			tx.Rollback(),
		)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration: commit migration %d: %w", m.Version, err)
	}
	return nil
}
