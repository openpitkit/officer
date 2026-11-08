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

package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/internal/store/sqlite"
	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// pnlBoundV1Fixture builds a database whose shared schema stops at 0001, so
// its P&L-bound table has the 0.1 shape without an axis, holding one global
// bound and one account bound.
func pnlBoundV1Fixture(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	st := preMigrationStore(t, path)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rs, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rs.CreateAsset(ctx, domain.Asset{Code: "USD", Title: "Dollar"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db := openRawDB(t, path)
	if _, err := db.Exec(`
		DROP TABLE limit_spot_funds_pnl_bound;
		CREATE TABLE limit_spot_funds_pnl_bound (
			id                INTEGER PRIMARY KEY,
			scope             TEXT NOT NULL,
			account_id        INTEGER REFERENCES account(id)       ON DELETE CASCADE,
			account_group_id  INTEGER REFERENCES account_group(id) ON DELETE CASCADE,
			currency_asset_id INTEGER NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
			lower_bound       TEXT,
			upper_bound       TEXT
		);
		CREATE INDEX idx_limit_spot_funds_pnl_bounds_account
			ON limit_spot_funds_pnl_bound (account_id);
		CREATE INDEX idx_limit_spot_funds_pnl_bounds_account_group
			ON limit_spot_funds_pnl_bound (account_group_id);
		CREATE INDEX idx_limit_spot_funds_pnl_bounds_currency_asset
			ON limit_spot_funds_pnl_bound (currency_asset_id);
		CREATE UNIQUE INDEX uq_limit_spot_funds_pnl_bounds
			ON limit_spot_funds_pnl_bound (
				scope, COALESCE(account_id, 0), COALESCE(account_group_id, 0)
			);
		INSERT INTO limit_spot_funds_pnl_bound (scope, currency_asset_id, lower_bound)
		VALUES ('global', (SELECT id FROM asset WHERE code = 'USD'), '-1000');
		INSERT INTO limit_spot_funds_pnl_bound (scope, currency_asset_id, upper_bound)
		VALUES ('account_group', (SELECT id FROM asset WHERE code = 'USD'), '500');
		DELETE FROM schema_migration WHERE version > 1;
	`); err != nil {
		t.Fatalf("build 0.1 P&L-bound fixture with raw SQL: %v", err)
	}
	return path
}

func openRawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func pnlBoundAxes(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT scope, axis FROM limit_spot_funds_pnl_bound`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	axes := make(map[string]string)
	for rows.Next() {
		var scope, axis string
		if err := rows.Scan(&scope, &axis); err != nil {
			t.Fatal(err)
		}
		axes[scope] = axis
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return axes
}

func TestPnlBoundAxisMigrationKeepsV1BoundsRealized(t *testing.T) {
	ctx := context.Background()
	path := pnlBoundV1Fixture(t)
	st := preMigrationStore(t, path, sqlite.WithPreMigrationCopy())
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// The pre-migration copy is taken before 0002 runs, so it keeps the 0.1 table.
	copies := preMigrationCopies(t, path)
	if len(copies) != 1 {
		t.Fatalf("published copies = %d, want 1 before 0002", len(copies))
	}
	copyDB := openRawDB(t, filepath.Join(filepath.Dir(path), copies[0].Name))
	var copyAxisColumns int
	if err := copyDB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('limit_spot_funds_pnl_bound')
		WHERE name = 'axis'`).Scan(&copyAxisColumns); err != nil {
		t.Fatal(err)
	}
	if copyAxisColumns != 0 {
		t.Error("pre-migration copy already has the axis column")
	}

	db := openRawDB(t, path)
	want := map[string]string{"global": "realized", "account_group": "realized"}
	got := pnlBoundAxes(t, db)
	if len(got) != len(want) || got["global"] != want["global"] ||
		got["account_group"] != want["account_group"] {
		t.Fatalf("migrated axes = %v, want %v", got, want)
	}

	rs, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	limits, err := rs.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range limits {
		if limit.Axis != domain.SpotFundsPnlAxisRealized {
			t.Errorf("migrated bound %+v axis = %q, want realized", limit, limit.Axis)
		}
	}

	// The axis is part of the address: an unrealized bound sits beside the
	// realized one, a second realized bound at the same address does not.
	if _, err := db.Exec(`INSERT INTO limit_spot_funds_pnl_bound
		(axis, scope, currency_asset_id, lower_bound)
		VALUES ('unrealized', 'global', (SELECT id FROM asset WHERE code = 'USD'), '-50')`); err != nil {
		t.Fatalf("unrealized bound beside realized: %v", err)
	}
	var sqliteErr *sqlitedriver.Error
	if _, err := db.Exec(`INSERT INTO limit_spot_funds_pnl_bound
		(axis, scope, currency_asset_id, lower_bound)
		VALUES ('realized', 'global', (SELECT id FROM asset WHERE code = 'USD'), '-50')`); !errors.As(err, &sqliteErr) || sqliteErr.Code() != sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		t.Fatalf("got %v, want SQLITE_CONSTRAINT_UNIQUE", err)
	}
	if _, err := db.Exec(`INSERT INTO limit_spot_funds_pnl_bound
		(axis, scope, currency_asset_id, lower_bound)
		VALUES ('position', 'global', (SELECT id FROM asset WHERE code = 'USD'), '-50')`); !errors.As(err, &sqliteErr) || sqliteErr.Code() != sqlite3.SQLITE_CONSTRAINT_CHECK {
		t.Fatalf("got %v, want SQLITE_CONSTRAINT_CHECK", err)
	}
}
