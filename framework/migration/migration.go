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
	// SQL is passed to one Exec; whether it may contain multiple statements is a
	// property of the caller's driver and connection configuration, not this
	// package. A body that executes without error is recorded as applied even if
	// it changes nothing, so the source owns whether the migration does work.
	SQL string
}

// MigrationSource yields a backend's ordered migration set.
type MigrationSource interface {
	// Migrations returns the migrations in ascending Version order.
	Migrations() ([]Migration, error)
}

// Binder renders bind placeholders. Positions are 1-based. Binders whose
// markers carry no number ignore position.
type Binder interface {
	BindVar(position int) string
}

// Config names the migration bookkeeping table.
type Config struct {
	// Schema qualifies the bookkeeping table. It is rendered as a quoted SQL
	// identifier and taken verbatim and case-sensitively; every character except
	// NUL is accepted and neutralised. Empty leaves the table unqualified.
	Schema string
	// Table is the bookkeeping table name. It is rendered as a quoted SQL
	// identifier and taken verbatim and case-sensitively; every character except
	// NUL is accepted and neutralised. A dot is part of the name; use Schema for
	// qualification. Empty uses schema_migration.
	Table string
}

const defaultTable = "schema_migration"

var (
	errInvalidIdentifier = errors.New("migration: invalid identifier")
	errMissingBinder     = errors.New("migration: missing binder")
	errEmptyMigrationSQL = errors.New("migration: empty SQL")
)

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

// Apply brings db to the latest migration version from source. binder renders
// bind placeholders for the version-record statement. It is required; a nil
// binder returns an explicit error and is not inferred from the driver.
func Apply(
	ctx context.Context,
	db *sql.DB,
	source MigrationSource,
	cfg Config,
	binder Binder,
) error {
	if binder == nil {
		return errMissingBinder
	}
	table, err := tableName(cfg)
	if err != nil {
		return err
	}

	migrations, err := source.Migrations()
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if strings.TrimSpace(migration.SQL) == "" {
			// Comment-only SQL is allowed because detecting work requires SQL-aware lexing.
			return fmt.Errorf(
				"%w: migration %d (%s)",
				errEmptyMigrationSQL, migration.Version, migration.Name,
			)
		}
	}
	if _, err := db.ExecContext(ctx, schemaMigrationsDDL(table)); err != nil {
		return fmt.Errorf("migration: create %s: %w", table, err)
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
		if err := applyMigration(ctx, db, table, m, binder); err != nil {
			return err
		}
	}
	return nil
}

// SchemaVersion returns the highest applied migration version, or zero.
func SchemaVersion(ctx context.Context, db *sql.DB, cfg Config) (int, error) {
	table, err := tableName(cfg)
	if err != nil {
		return 0, err
	}
	var version sql.NullInt64
	err = db.QueryRowContext(
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

func tableName(cfg Config) (string, error) {
	table := cfg.Table
	if table == "" {
		table = defaultTable
	}
	quotedTable, err := quoteIdentifier(table)
	if err != nil {
		return "", err
	}
	if cfg.Schema == "" {
		return quotedTable, nil
	}
	quotedSchema, err := quoteIdentifier(cfg.Schema)
	if err != nil {
		return "", err
	}
	return quotedSchema + "." + quotedTable, nil
}

func quoteIdentifier(name string) (string, error) {
	if strings.ContainsRune(name, '\x00') {
		return "", fmt.Errorf("%w %q: contains NUL byte", errInvalidIdentifier, name)
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`, nil
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
	ctx context.Context, db *sql.DB, table string, m Migration, binder Binder,
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
		versionRecordInsert(table, binder),
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

func versionRecordInsert(table string, binder Binder) string {
	return `INSERT INTO ` + table + ` (version, name, applied_at) VALUES (` +
		binder.BindVar(1) + `, ` + binder.BindVar(2) + `, ` +
		binder.BindVar(3) + `)`
}
