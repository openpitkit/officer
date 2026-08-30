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

package sqlite

// sqliteDialect renders the canonical schema tokens for SQLite
// (modernc.org/sqlite).
type sqliteDialect struct{}

// BindVar renders a SQLite bind placeholder.
func (sqliteDialect) BindVar(int) string {
	return "?"
}

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

// Decimal renders an exact-decimal column as TEXT with the DECIMAL collation
// registered by the connector. The collation makes <, >, BETWEEN and ORDER BY
// numeric and exact, and an index on the column inherits it, so range scans and
// sorts run through the index. The empty string sorts below every number.
func (sqliteDialect) Decimal() string {
	return "TEXT COLLATE DECIMAL"
}
