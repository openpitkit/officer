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

package fixture

import (
	"context"
	"database/sql"
)

func rawTransaction(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	// ruleid: store-transaction-helper
	return db.BeginTx(ctx, nil)
}

type transactionStore struct {
	db *sql.DB
}

func nestedTransaction(ctx context.Context, s transactionStore) (*sql.Tx, error) {
	// ruleid: store-transaction-helper
	return s.db.BeginTx(ctx, nil)
}

func transactionMethodValue(db *sql.DB) {
	// ruleid: store-transaction-helper
	begin := db.BeginTx
	_ = begin
}

func (s transactionStore) beginTx(ctx context.Context, db *sql.DB, opts *sql.TxOptions) (*sql.Tx, error) {
	// ruleid: store-transaction-helper
	return db.BeginTx(ctx, opts)
}

func beginTx(ctx context.Context, db *sql.DB, opts *sql.TxOptions) (*sql.Tx, error) {
	// ok: store-transaction-helper
	return db.BeginTx(ctx, opts)
}

type realmStore struct{}

func (r *realmStore) twoPoolReads(ctx context.Context) {
	// ruleid: store-composite-read-snapshot
	db, _ := r.db()
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*)
}
`).Scan(); err != nil {
		return
	}
	db.QueryContext(ctx, "SELECT rows")
}

func (r *realmStore) twoQueryerReads(ctx context.Context, restricted bool) {
	// ruleid: store-composite-read-snapshot
	pool, _ := r.db()
	var reader sqlQueryer = pool
	if restricted {
		tx, _ := beginTx(ctx, pool, &sql.TxOptions{ReadOnly: true})
		reader = tx
	}
	reader.QueryRowContext(ctx, "SELECT COUNT(*)")
	reader.QueryContext(ctx, "SELECT rows")
}

func (r *realmStore) poolReadThenHelper(ctx context.Context) {
	// ruleid: store-composite-read-snapshot
	db, _ := r.db()
	db.QueryContext(ctx, "SELECT rows")
	readDetails(ctx, db)
}

func (r *realmStore) helperThenPoolRead(ctx context.Context) {
	// ruleid: store-composite-read-snapshot
	db, _ := r.db()
	var reader sqlQueryer = db
	readDetails(ctx, reader)
	db.QueryContext(ctx, "SELECT rows")
}

func (r *realmStore) callArgumentBeforePool(ctx context.Context, key string) {
	var db *sql.DB
	var err error
	// ruleid: store-composite-read-snapshot
	db, err = r.db()
	if err != nil {
		return
	}
	readDetails(ctx, normalizeKey(key), db)
	if err := db.QueryRowContext(ctx, "SELECT row").Scan(&key); err != nil {
		return
	}
}

func (r *realmStore) braceArgumentBeforePool(ctx context.Context, key string) {
	// ruleid: store-composite-read-snapshot
	db, _ := r.db()
	db.QueryContext(ctx, "SELECT rows")
	readDetails(ctx, []string{key}, db)
}

func (r *realmStore) queryTextAndComment(ctx context.Context) {
	// ok: store-composite-read-snapshot
	db, _ := r.db()
	// db.QueryRowContext(ctx, "SELECT row")
	db.QueryContext(ctx, `SELECT 'db.QueryContext(ctx, query)'
}
`)
}

func (r *realmStore) readsInTransaction(ctx context.Context) {
	// ok: store-composite-read-snapshot
	db, _ := r.db()
	tx, _ := beginTx(ctx, db, &sql.TxOptions{ReadOnly: true})
	tx.QueryRowContext(ctx, "SELECT COUNT(*)")
	tx.QueryContext(ctx, "SELECT rows")
	readDetails(ctx, tx)
}

func (r *realmStore) onePoolRead(ctx context.Context) {
	// ok: store-composite-read-snapshot
	db, _ := r.db()
	db.QueryContext(ctx, "SELECT rows")
}

func (r *realmStore) twoPoolHandoffs(ctx context.Context) {
	// ok: store-composite-read-snapshot
	db, _ := r.db()
	writeHeader(ctx, db)
	writeDetails(ctx, db)
}

func (r *realmStore) transactionAndOnePoolRead(ctx context.Context) {
	// todoruleid: store-composite-read-snapshot
	db, _ := r.db()
	tx, _ := beginTx(ctx, db, &sql.TxOptions{ReadOnly: true})
	tx.QueryContext(ctx, "SELECT rows")
	tx.Commit()
	db.QueryContext(ctx, "SELECT row")
}

func (r *realmStore) writeWithDiagnosticRead(ctx context.Context) {
	// ok: store-composite-read-snapshot
	db, _ := r.db()
	if _, err := db.ExecContext(ctx, "UPDATE rows"); err != nil {
		db.QueryRowContext(ctx, "SELECT diagnostic")
	}
}
