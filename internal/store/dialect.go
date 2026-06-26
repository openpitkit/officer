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

import "strings"

// Dialect renders the few backend-specific tokens of the one canonical schema.
//
// The schema text is shared verbatim across backends; only three declarations
// diverge per backend, so the seam is intentionally minimal. A future backend
// (for example PostgreSQL, which would map the external-id default to
// gen_random_uuid() and the boolean to BOOLEAN) slots in as a new Dialect
// without touching the schema text or the store logic. PostgreSQL is referenced
// here only in prose; it is not implemented.
type Dialect interface {
	// PrimaryKey renders the {{PK}} token: the surrogate-PK identity column
	// declaration, an internal integer key that never leaves the store.
	PrimaryKey() string
	// ExternalID renders the {{XID}} token: the 16-byte external-id column
	// declaration. SQLite has no server-side random default, so the column is a
	// plain blob and the connector fills 16 crypto/rand bytes at insert.
	ExternalID() string
	// Bool renders the {{BOOL}} token: the boolean column type.
	Bool() string
}

// Dialect tokens substituted in the canonical schema text. They are the only
// per-backend declarations of the one-schema-many-backends seam.
const (
	tokenPrimaryKey = "{{PK}}"
	tokenExternalID = "{{XID}}"
	tokenBool       = "{{BOOL}}"
)

// renderSchema substitutes the dialect tokens in the canonical schema text. It
// is the single place the shared schema becomes backend-specific DDL.
func renderSchema(d Dialect, schema string) string {
	return strings.NewReplacer(
		tokenPrimaryKey, d.PrimaryKey(),
		tokenExternalID, d.ExternalID(),
		tokenBool, d.Bool(),
	).Replace(schema)
}

// sqliteDialect renders the canonical schema tokens for SQLite
// (modernc.org/sqlite).
type sqliteDialect struct{}

// PrimaryKey renders the surrogate PK as an autoincrementing integer rowid. The
// key is sequential and internal; it never appears on the wire.
func (sqliteDialect) PrimaryKey() string {
	return "INTEGER PRIMARY KEY AUTOINCREMENT"
}

// ExternalID renders the external-id column as a non-null blob. SQLite has no
// server-side random default expression, so the connector fills 16 crypto/rand
// bytes at insert time; the value is never null in a persisted row.
func (sqliteDialect) ExternalID() string {
	return "BLOB NOT NULL"
}

// Bool renders booleans as INTEGER (0/1), SQLite's storage class for them.
func (sqliteDialect) Bool() string {
	return "INTEGER"
}
