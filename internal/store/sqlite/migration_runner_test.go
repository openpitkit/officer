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
// Please see https://officer.openpit.dev and the OWNERS file for details.

package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/migration"
)

func TestSQLiteDialectBindVar(t *testing.T) {
	for _, position := range []int{1, 2, 99} {
		if got := (sqliteDialect{}).BindVar(position); got != "?" {
			t.Errorf(
				"sqliteDialect{}.BindVar(%d) = %q, want %q",
				position, got, "?",
			)
		}
	}
}

func TestMigrationRunner(t *testing.T) {
	t.Run("applies and records once", func(t *testing.T) {
		db := openMigrationTestDB(t)
		ctx := context.Background()
		source := migrationList{{
			Version: 1,
			Name:    "create_applied_migration.sql",
			SQL: "CREATE TABLE applied_migration " +
				"(id INTEGER PRIMARY KEY)",
		}}
		for range 2 {
			if err := migration.Apply(
				ctx, db, source, migration.Config{}, sqliteDialect{},
			); err != nil {
				t.Fatalf("Apply: %v", err)
			}
		}
		assertSQLiteTableCount(t, db, "applied_migration", 1)
		assertMigrationVersionCount(t, db, 1, 1)
	})

	t.Run("rolls back failed SQL", func(t *testing.T) {
		db := openMigrationTestDB(t)
		ctx := context.Background()
		err := migration.Apply(
			ctx, db, migrationList{{
				Version: 1,
				Name:    "rollback.sql",
				SQL: "CREATE TABLE rolled_back_migration " +
					"(id INTEGER PRIMARY KEY); " +
					"CREATE TABL invalid_statement (id INTEGER PRIMARY KEY)",
			}},
			migration.Config{}, sqliteDialect{},
		)
		if err == nil || !strings.Contains(
			err.Error(), "migration: apply migration 1 (rollback.sql)",
		) {
			t.Fatalf("Apply() error = %v, want migration context", err)
		}
		assertSQLiteTableCount(t, db, "rolled_back_migration", 0)
		assertMigrationVersionCount(t, db, 1, 0)
	})

	t.Run("executes quoted bookkeeping identifiers", func(t *testing.T) {
		tests := []struct {
			name           string
			table          string
			countRecordSQL string
		}{
			{
				name:           "space",
				table:          "migration history",
				countRecordSQL: `SELECT COUNT(*) FROM "migration history" WHERE version = ?`,
			},
			{
				name:           "embedded double quote",
				table:          `migration "history`,
				countRecordSQL: `SELECT COUNT(*) FROM "migration ""history" WHERE version = ?`,
			},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				db := openMigrationTestDB(t)
				ctx := context.Background()
				cfg := migration.Config{Table: test.table}
				source := migrationList{{
					Version: 7,
					Name:    "quoted_bookkeeping.sql",
					SQL: "CREATE TABLE quoted_bookkeeping_migration " +
						"(id INTEGER PRIMARY KEY)",
				}}

				if err := migration.Apply(
					ctx, db, source, cfg, sqliteDialect{},
				); err != nil {
					t.Fatalf("first Apply with table %q: %v", test.table, err)
				}
				assertSQLiteTableCount(t, db, "quoted_bookkeeping_migration", 1)

				var records int
				if err := db.QueryRowContext(
					ctx, test.countRecordSQL, 7,
				).Scan(&records); err != nil {
					t.Fatalf(
						"count records in bookkeeping table %q: %v",
						test.table, err,
					)
				}
				if records != 1 {
					t.Errorf(
						"bookkeeping table %q version 7 count = %d, want 1",
						test.table, records,
					)
				}

				if err := migration.Apply(
					ctx, db, source, cfg, sqliteDialect{},
				); err != nil {
					t.Fatalf("second Apply with table %q: %v", test.table, err)
				}
				version, err := migration.SchemaVersion(
					ctx, db, cfg,
				)
				if err != nil {
					t.Fatalf("SchemaVersion with table %q: %v", test.table, err)
				}
				if version != 7 {
					t.Errorf(
						"SchemaVersion with table %q = %d, want 7",
						test.table, version,
					)
				}
			})
		}
	})

	t.Run("uses attached schema", func(t *testing.T) {
		db := openMigrationTestDB(t)
		// ATTACH is connection-local, so pin the pool to the attached connection.
		db.SetMaxOpenConns(1)
		ctx := context.Background()
		if _, err := db.ExecContext(
			ctx,
			`ATTACH DATABASE ? AS "migration schema"`,
			filepath.Join(t.TempDir(), "attached.db"),
		); err != nil {
			t.Fatalf("attach migration schema: %v", err)
		}

		cfg := migration.Config{Schema: "migration schema"}
		if err := migration.Apply(
			ctx, db,
			migrationList{{
				Version: 9,
				Name:    "attached_schema.sql",
				SQL: "CREATE TABLE attached_schema_migration " +
					"(id INTEGER PRIMARY KEY)",
			}},
			cfg, sqliteDialect{},
		); err != nil {
			t.Fatalf("Apply with attached schema: %v", err)
		}

		var records int
		if err := db.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM "migration schema"."schema_migration" WHERE version = ?`,
			9,
		).Scan(&records); err != nil {
			t.Fatalf("count records in attached schema: %v", err)
		}
		if records != 1 {
			t.Errorf("attached schema version 9 count = %d, want 1", records)
		}
		assertSQLiteTableCount(t, db, "schema_migration", 0)

		version, err := migration.SchemaVersion(
			ctx, db, cfg,
		)
		if err != nil {
			t.Fatalf("SchemaVersion with attached schema: %v", err)
		}
		if version != 9 {
			t.Errorf("SchemaVersion with attached schema = %d, want 9", version)
		}
	})
}

func TestPending(t *testing.T) {
	ctx := context.Background()
	source := migrationList{
		{Version: 1, Name: "one.sql", SQL: "SELECT 1"},
		{Version: 2, Name: "two.sql", SQL: "SELECT 2"},
		{Version: 3, Name: "three.sql", SQL: "SELECT 3"},
	}

	t.Run("filters applied versions", func(t *testing.T) {
		for _, cfg := range []migration.Config{
			{},
			{Schema: "main", Table: "migration \"history"},
		} {
			t.Run(cfg.Schema+"/"+cfg.Table, func(t *testing.T) {
				db := openMigrationTestDB(t)
				if err := migration.Apply(
					ctx, db, source[:2], cfg, sqliteDialect{},
				); err != nil {
					t.Fatalf("Apply: %v", err)
				}
				pending, err := migration.Pending(ctx, db, source, cfg)
				if err != nil {
					t.Fatalf("Pending: %v", err)
				}
				if !reflect.DeepEqual(pending, []migration.Migration(source[2:])) {
					t.Errorf("Pending() = %v, want %v", pending, source[2:])
				}
				pending, err = migration.Pending(ctx, db, source[:2], cfg)
				if err != nil {
					t.Fatalf("Pending applied source: %v", err)
				}
				if len(pending) != 0 {
					t.Errorf("Pending applied source = %v, want none", pending)
				}
			})
		}
	})

	t.Run("sorts pending versions", func(t *testing.T) {
		db := openMigrationTestDB(t)
		cfg := migration.Config{}
		if err := migration.Apply(
			ctx, db, source[:1], cfg, sqliteDialect{},
		); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		unordered := migrationList{source[2], source[0], source[1]}
		pending, err := migration.Pending(ctx, db, unordered, cfg)
		if err != nil {
			t.Fatalf("Pending: %v", err)
		}
		if !reflect.DeepEqual(pending, []migration.Migration(source[1:])) {
			t.Errorf("Pending() = %v, want %v", pending, source[1:])
		}
	})

	t.Run("missing bookkeeping table", func(t *testing.T) {
		db := openMigrationTestDB(t)
		_, err := migration.Pending(ctx, db, source, migration.Config{})
		if err == nil || !strings.Contains(
			err.Error(), `migration: read "schema_migration":`,
		) {
			t.Errorf("Pending() error = %v, want bookkeeping read error", err)
		}
		assertSQLiteTableCount(t, db, "schema_migration", 0)
	})

	t.Run("read only connection", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "migration.db")
		db, err := openSQLiteDB(path)
		if err != nil {
			t.Fatalf("openSQLiteDB: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		cfg := migration.Config{}
		if err := migration.Apply(
			ctx, db, source[:2], cfg, sqliteDialect{},
		); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close writable database: %v", err)
		}

		readOnly, err := sql.Open(driverName, snapshotSourceURI(path))
		if err != nil {
			t.Fatalf("open read-only database: %v", err)
		}
		t.Cleanup(func() { _ = readOnly.Close() })
		pending, err := migration.Pending(ctx, readOnly, source, cfg)
		if err != nil {
			t.Fatalf("Pending read-only: %v", err)
		}
		if !reflect.DeepEqual(pending, []migration.Migration(source[2:])) {
			t.Errorf("Pending read-only = %v, want %v", pending, source[2:])
		}
	})
}

func openMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openSQLiteDB(filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatalf("openSQLiteDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func assertSQLiteTableCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	err := db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		table,
	).Scan(&got)
	if err != nil {
		t.Fatalf("count SQLite table %q: %v", table, err)
	}
	if got != want {
		t.Errorf("SQLite table %q count = %d, want %d", table, got, want)
	}
}

func assertMigrationVersionCount(
	t *testing.T, db *sql.DB, version int, want int,
) {
	t.Helper()
	var got int
	err := db.QueryRowContext(
		context.Background(),
		"SELECT COUNT(*) FROM schema_migration WHERE version = ?",
		version,
	).Scan(&got)
	if err != nil {
		t.Fatalf("count migration version %d: %v", version, err)
	}
	if got != want {
		t.Errorf("migration version %d count = %d, want %d", version, got, want)
	}
}
