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
	"crypto/rand"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.openpit.dev/officer/internal/domain"

	// modernc.org/sqlite is a pure-Go SQLite driver registered under "sqlite".
	_ "modernc.org/sqlite"
)

const driverName = "sqlite"

// Compile-time assertions that the SQLite connector satisfies both seams.
var (
	_ Store      = (*sqliteStore)(nil)
	_ RealmStore = (*realmStore)(nil)
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type migration struct {
	name    string
	sql     string
	version int
}

// sqliteStore is the SQLite-backed Store. It is single-realm: it serves exactly
// one realm and rejects any other realm id. Safe for concurrent use: all
// mutations go through database/sql which pools a single connection.
type sqliteStore struct {
	db      *sql.DB
	dialect Dialect
	path    string
	realm   domain.RealmID

	mu        sync.Mutex
	reachable bool
	fatal     func(error)
}

// SQLiteOption customizes a SQLite store instance.
type SQLiteOption func(*sqliteStore)

// WithFatalShutdownHook wires the process-level fatal handler for unrecoverable
// business-CSV import store failures.
func WithFatalShutdownHook(hook func(error)) SQLiteOption {
	return func(s *sqliteStore) {
		if hook != nil {
			s.fatal = hook
		}
	}
}

// WithRealm binds the single-realm connector to a non-default realm id. When
// unset the connector serves domain.DefaultRealm.
func WithRealm(realm domain.RealmID) SQLiteOption {
	return func(s *sqliteStore) {
		if realm != "" {
			s.realm = realm
		}
	}
}

// NewSQLiteStore opens (creating if absent) the SQLite database at path. The
// caller must run Migrate before any read or write, then obtain a RealmStore
// from ForRealm. Close releases the underlying connection pool.
//
// The connection opens the path as given; only Path() resolves to an absolute
// filesystem path so the operator sees the full location. filepath.Abs returns
// an already-absolute path unchanged and keeps the original string on error.
func NewSQLiteStore(path string, opts ...SQLiteOption) (Store, error) {
	db, err := openSQLiteDB(path)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite at %q: %w", path, err)
	}

	displayPath := path
	if abs, absErr := filepath.Abs(path); absErr == nil {
		displayPath = abs
	}
	s := &sqliteStore{
		db:      db,
		dialect: sqliteDialect{},
		path:    displayPath,
		realm:   domain.DefaultRealm,
		fatal:   defaultFatalShutdown,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

func defaultFatalShutdown(err error) {
	log.Fatalf("fatal store error: %v", err)
}

// isExpectedDomainError reports whether err is a domain-level outcome the caller
// can map to an HTTP status (a 4xx), as opposed to an unexpected infrastructure
// failure (begin/commit/exec) that signals the store is no longer trustworthy.
func isExpectedDomainError(err error) bool {
	return errors.Is(err, domain.ErrInvalid) ||
		errors.Is(err, domain.ErrNotFound) ||
		errors.Is(err, domain.ErrAlreadyExists) ||
		errors.Is(err, domain.ErrHasDependents)
}

func openSQLiteDB(path string) (*sql.DB, error) {
	db, err := sql.Open(driverName, sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	// Single open connection avoids "database is locked" under SQLite's file
	// locking. The DSN turns foreign-key enforcement on for every connection.
	db.SetMaxOpenConns(1)
	return db, nil
}

func sqliteDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "_pragma=foreign_keys(1)"
}

// ForRealm returns the data-access handle bound to realm. The single-realm
// connector accepts only its own realm id and domain.DefaultRealm; any other id
// is rejected with an error wrapping domain.ErrInvalid. The bound realm's
// identity row is ensured so backup labelling and future placement have it.
func (s *sqliteStore) ForRealm(
	ctx context.Context, realm domain.RealmID,
) (RealmStore, error) {
	if realm == "" {
		realm = domain.DefaultRealm
	}
	if realm != s.realm {
		return nil, fmt.Errorf(
			"store: realm %q not served by this single-realm connector (serves %q): %w",
			realm, s.realm, domain.ErrInvalid,
		)
	}
	if err := s.ensureRealmRow(ctx); err != nil {
		return nil, err
	}
	return &realmStore{store: s}, nil
}

// ensureRealmRow inserts the single realm identity row when absent, assigning it
// an external id. Its code is the bound realm id.
func (s *sqliteStore) ensureRealmRow(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(
		ctx, `SELECT COUNT(*) FROM realm`,
	).Scan(&n); err != nil {
		return fmt.Errorf("store: count realm rows: %w", err)
	}
	if n > 0 {
		return nil
	}
	xid, err := newExternalID()
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO realm (external_id, code, title) VALUES (?, ?, '')`,
		xid.Bytes(), s.realm.String(),
	); err != nil {
		return fmt.Errorf("store: insert realm row: %w", err)
	}
	return nil
}

// Migrate brings the schema up to the version this build expects. Idempotent.
// The one canonical migration is rendered through the dialect before it runs, so
// the surrogate-PK, external-id and boolean tokens become backend-specific DDL.
func (s *sqliteStore) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	applied, err := s.appliedVersions(ctx)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqliteStore) applyMigration(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration %d: %w", m.version, err)
	}
	rendered := renderSchema(s.dialect, m.sql)
	if _, err := tx.ExecContext(ctx, rendered); err != nil {
		return errors.Join(
			fmt.Errorf("store: apply migration %d (%s): %w", m.version, m.name, err),
			tx.Rollback(),
		)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, nowStr(),
	); err != nil {
		return errors.Join(
			fmt.Errorf("store: record migration %d: %w", m.version, err),
			tx.Rollback(),
		)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration %d: %w", m.version, err)
	}
	return nil
}

func (s *sqliteStore) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("store: scan schema version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate schema versions: %w", err)
	}
	return applied, nil
}

// SchemaVersion returns the highest applied schema version, or zero.
func (s *sqliteStore) SchemaVersion(ctx context.Context) (int, error) {
	var version sql.NullInt64
	err := s.db.QueryRowContext(
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

// Ping verifies the database is reachable.
func (s *sqliteStore) Ping(ctx context.Context) error {
	err := s.db.PingContext(ctx)
	s.mu.Lock()
	s.reachable = err == nil
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	return nil
}

// Reset recreates the SQLite file and reapplies the bundled migration.
func (s *sqliteStore) Reset(ctx context.Context) error {
	path := s.path
	if err := s.Close(); err != nil {
		return fmt.Errorf("store: reset close sqlite: %w", err)
	}
	for _, candidate := range sqliteResetPaths(path) {
		if err := os.Remove(candidate); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("store: reset remove %q: %w", candidate, err)
		}
	}
	db, err := openSQLiteDB(path)
	if err != nil {
		return fmt.Errorf("store: reset open sqlite at %q: %w", path, err)
	}
	s.db = db
	if err := s.Migrate(ctx); err != nil {
		return fmt.Errorf("store: reset migrate: %w", err)
	}
	return nil
}

func sqliteResetPaths(path string) []string {
	return []string{path, path + "-wal", path + "-shm", path + "-journal"}
}

// Path returns the on-disk location of the database.
func (s *sqliteStore) Path() string { return s.path }

// Close releases the connection pool. Idempotent.
func (s *sqliteStore) Close() error {
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	if err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	return nil
}

// realmStore is the realm-scoped data-access handle the single-realm SQLite
// connector returns. It shares the connector's connection pool; all data access
// for the bound realm goes through it. A future multi-realm connector would bind
// each handle to its own schema or route here instead.
type realmStore struct {
	store *sqliteStore
}

// db returns the shared connection pool.
func (r *realmStore) db() *sql.DB { return r.store.db }

// --- Shared helpers reused by every table group -----------------------------

// schemaMigrationsDDL creates the migration bookkeeping table before the
// canonical migration runs.
const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`

// nowStr returns the current UTC time as RFC3339Nano.
func nowStr() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// newExternalID draws 16 crypto/rand bytes and wraps them in a domain external
// id. This is the single place machine-record external ids are generated; there
// is no per-insert existence check because 128 bits of randomness make a
// collision negligible. The connector fills this into the external_id BLOB
// column at insert time (the SQLite dialect has no server-side random default).
func newExternalID() (domain.ExternalID, error) {
	var raw [domain.ExternalIDByteLen]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return domain.ExternalID{}, fmt.Errorf("store: generate external id: %w", err)
	}
	return domain.ExternalIDFromBytes(raw[:])
}

// externalIDForInsert returns the external id to write for a user-created
// machine record. A caller-supplied (non-zero) id is used verbatim so the
// operator owns the handle; a zero id is freshly generated by the connector.
// Neither path probes the table first: a supplied id relies on the external_id
// UNIQUE constraint (mapped to domain.ErrAlreadyExists on violation), and a
// generated id needs no check because 128 bits of randomness make a collision
// negligible.
func externalIDForInsert(supplied domain.ExternalID) (domain.ExternalID, error) {
	if !supplied.IsZero() {
		return supplied, nil
	}
	return newExternalID()
}

// sqlQueryer is the read surface shared by *sql.DB and *sql.Tx.
type sqlQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// sqlExecer is the write surface shared by *sql.DB and *sql.Tx.
type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// resolveAssetID resolves an asset code to its surrogate id for an insert or a
// query. An unknown code is reported as an error wrapping domain.ErrInvalid so a
// dangling dictionary reference never silently inserts a bad foreign key.
func resolveAssetID(ctx context.Context, q sqlQueryer, code string) (int64, error) {
	return resolveDictID(ctx, q, "assets", "asset", code)
}

// resolveAccountID resolves an account code to its surrogate id.
func resolveAccountID(ctx context.Context, q sqlQueryer, code domain.AccountID) (int64, error) {
	return resolveDictID(ctx, q, "accounts", "account", code.String())
}

// resolveGroupID resolves a group code to its surrogate id.
func resolveGroupID(ctx context.Context, q sqlQueryer, code string) (int64, error) {
	return resolveDictID(ctx, q, "account_groups", "group", code)
}

// resolvePrincipalID resolves a principal code to its surrogate id.
func resolvePrincipalID(ctx context.Context, q sqlQueryer, code string) (int64, error) {
	return resolveDictID(ctx, q, "principals", "principal", code)
}

// resolveDictID resolves a dictionary code in table to its surrogate id. The
// label names the dictionary in the error. An unknown code wraps
// domain.ErrInvalid; the table names are internal constants, never user input.
func resolveDictID(
	ctx context.Context, q sqlQueryer, table, label, code string,
) (int64, error) {
	var id int64
	err := q.QueryRowContext(
		ctx, `SELECT id FROM `+table+` WHERE code = ?`, code,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("unknown %s %q: %w", label, code, domain.ErrInvalid)
	}
	if err != nil {
		return 0, fmt.Errorf("store: resolve %s %q: %w", label, code, err)
	}
	return id, nil
}

// resolveOptionalPrincipalID resolves an optional principal code: an empty code
// yields a NULL foreign key (sql.NullInt64{Valid:false}); a non-empty unknown
// code wraps domain.ErrInvalid.
func resolveOptionalPrincipalID(
	ctx context.Context, q sqlQueryer, code string,
) (sql.NullInt64, error) {
	if code == "" {
		return sql.NullInt64{}, nil
	}
	id, err := resolvePrincipalID(ctx, q, code)
	if err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: id, Valid: true}, nil
}

// nextEngineAccountID assigns the next collision-free engine account id inside a
// transaction. Engine-id invariant: the connector assigns engine ids
// monotonically as MAX(existing)+1 within the same write transaction that
// inserts the row, so the single-writer SQLite connection guarantees uniqueness
// without a separate sequence. The starting value is the minimum assignable id;
// the range is checked so the id always fits the engine constructor and the
// signed column.
func nextEngineAccountID(ctx context.Context, tx *sql.Tx) (domain.EngineAccountID, error) {
	var max sql.NullInt64
	if err := tx.QueryRowContext(
		ctx, `SELECT MAX(engine_account_id) FROM accounts`,
	).Scan(&max); err != nil {
		return 0, fmt.Errorf("store: read max engine account id: %w", err)
	}
	next := domain.EngineAccountIDMin
	if max.Valid {
		next = uint64(max.Int64) + 1
	}
	id := domain.EngineAccountID(next)
	if err := domain.ValidateEngineAccountID(id); err != nil {
		return 0, fmt.Errorf("store: assign engine account id: %w", err)
	}
	return id, nil
}

// nextEngineGroupID assigns the next collision-free engine group id inside a
// transaction. See nextEngineAccountID for the monotonic single-writer
// invariant.
func nextEngineGroupID(ctx context.Context, tx *sql.Tx) (domain.EngineGroupID, error) {
	var max sql.NullInt64
	if err := tx.QueryRowContext(
		ctx, `SELECT MAX(engine_group_id) FROM account_groups`,
	).Scan(&max); err != nil {
		return 0, fmt.Errorf("store: read max engine group id: %w", err)
	}
	next := uint64(domain.EngineGroupIDMin)
	if max.Valid {
		next = uint64(max.Int64) + 1
	}
	if next > uint64(domain.EngineGroupIDMax) {
		return 0, fmt.Errorf(
			"store: engine group id space exhausted: %w", domain.ErrInvalid,
		)
	}
	id := domain.EngineGroupID(next)
	if err := domain.ValidateEngineGroupID(id); err != nil {
		return 0, fmt.Errorf("store: assign engine group id: %w", err)
	}
	return id, nil
}

// isSQLiteUnique returns true when the error is a SQLite UNIQUE constraint
// violation. modernc.org/sqlite surfaces these as errors whose text contains
// "UNIQUE constraint failed".
func isSQLiteUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// isSQLiteUniqueOn returns true when err is a SQLite UNIQUE constraint violation
// on table.column. modernc.org/sqlite names the offending column in the error
// text ("UNIQUE constraint failed: <table>.<column>"), so a table with several
// UNIQUE columns can tell which one was hit and map each to a distinct error.
func isSQLiteUniqueOn(err error, table, column string) bool {
	return err != nil &&
		strings.Contains(
			err.Error(), "UNIQUE constraint failed: "+table+"."+column,
		)
}

// --- Migration loading ------------------------------------------------------

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read embedded migrations: %w", err)
	}

	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := parseMigrationVersion(entry.Name())
		if err != nil {
			return nil, err
		}
		body, err := fs.ReadFile(migrationsFS, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("store: read migration %q: %w", entry.Name(), err)
		}
		migrations = append(migrations, migration{
			name:    entry.Name(),
			sql:     string(body),
			version: version,
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})
	return migrations, nil
}

func parseMigrationVersion(name string) (int, error) {
	prefix, _, found := strings.Cut(name, "_")
	if !found {
		return 0, fmt.Errorf(
			"store: malformed migration name %q (want <version>_<name>.sql)", name,
		)
	}
	if prefix == "" {
		return 0, fmt.Errorf("store: empty migration version in %q", name)
	}
	version := 0
	for _, r := range prefix {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("store: non-numeric migration version in %q", name)
		}
		version = version*10 + int(r-'0')
	}
	return version, nil
}
