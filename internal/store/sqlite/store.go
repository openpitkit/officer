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

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/migration"
	"go.openpit.dev/officer/framework/secret"
	fwstore "go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/store/schema"

	// modernc.org/sqlite is a pure-Go SQLite driver registered under "sqlite".
	_ "modernc.org/sqlite"
)

const driverName = "sqlite"

// Compile-time assertions that the SQLite connector satisfies both seams.
var (
	_ fwstore.Store      = (*sqliteStore)(nil)
	_ fwstore.RealmStore = (*realmStore)(nil)
)

// sqliteStore is the SQLite-backed Store. It is single-realm: it serves exactly
// one realm and rejects any other realm id. Safe for concurrent use: all
// mutations go through database/sql which pools a single connection.
type sqliteStore struct {
	db                  atomic.Pointer[sql.DB]
	enumCodes           atomic.Pointer[enumDictionaries]
	configuredMasterKey *secret.MasterKey
	sealer              atomic.Pointer[secret.MasterKey]
	dialect             sqliteDialect
	path                string
	realm               domain.RealmID
	databaseCreated     bool

	mu        sync.Mutex
	reachable bool
	staleDBs  []*sql.DB
}

// Option customizes a SQLite store instance.
type Option func(*sqliteStore)

// WithMasterKey records the master key considered by the migration-time secret
// state machine.
func WithMasterKey(key secret.MasterKey) Option {
	return func(s *sqliteStore) {
		keyCopy := key
		s.configuredMasterKey = &keyCopy
	}
}

// New opens (creating if absent) the SQLite database at path and binds the
// single-realm connector to realm. An empty or invalid realm is rejected with
// an error wrapping domain.ErrInvalid before the database is opened. The caller
// must run Migrate before any read or write, then obtain the RealmStore from
// ForRealm with the same realm. Close releases the underlying connection pool.
//
// The connection opens the path as given; only Path() resolves to an absolute
// filesystem path so the operator sees the full location. filepath.Abs returns
// an already-absolute path unchanged and keeps the original string on error.
func New(
	path string, realm domain.RealmID, opts ...Option,
) (fwstore.Store, error) {
	if err := domain.ValidateRealmID(realm); err != nil {
		return nil, fmt.Errorf("store: bind realm: %w", err)
	}
	filesystemPath, _, _ := strings.Cut(path, "?")
	_, statErr := os.Stat(filesystemPath)
	databaseCreated := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !databaseCreated {
		return nil, fmt.Errorf("store: stat sqlite at %q: %w", path, statErr)
	}
	db, err := openSQLiteDB(path)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite at %q: %w", path, err)
	}

	displayPath := path
	if abs, absErr := filepath.Abs(path); absErr == nil {
		displayPath = abs
	}
	s := &sqliteStore{
		dialect:         sqliteDialect{},
		path:            displayPath,
		realm:           realm,
		databaseCreated: databaseCreated,
	}
	s.db.Store(db)
	for _, opt := range opts {
		opt(s)
	}
	if dictionaries, err := loadEnumDictionaries(context.Background(), db); err == nil {
		s.enumCodes.Store(dictionaries)
	} else if !isSQLiteMissingTable(err) {
		_ = db.Close()
		return nil, fmt.Errorf("store: load enum dictionaries: %w", err)
	}
	return s, nil
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
// connector accepts only its bound realm id; any other id, the empty one
// included, is rejected with an error wrapping domain.ErrInvalid. The bound
// realm's identity row is ensured so backup labelling and future placement have
// it.
func (s *sqliteStore) ForRealm(
	ctx context.Context, realm domain.RealmID,
) (fwstore.RealmStore, error) {
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
// an external id. Its code is the bound realm id. A present row whose code is
// another realm means the database was created for that realm: it is rejected
// with an error wrapping domain.ErrInvalid naming both codes rather than served
// under the bound one. Migrate runs the same check before it writes anything
// else, so a foreign database is refused before its secrets are resealed.
func (s *sqliteStore) ensureRealmRow(ctx context.Context) error {
	db := s.currentDB()
	if db == nil {
		return fmt.Errorf("store: sqlite is closed")
	}
	return s.ensureRealmRowDB(ctx, db)
}

func (s *sqliteStore) ensureRealmRowDB(ctx context.Context, db *sql.DB) error {
	var code string
	err := db.QueryRowContext(ctx, `SELECT code FROM realm`).Scan(&code)
	if err == nil {
		if code != s.realm.String() {
			return fmt.Errorf(
				"store: database at %q is bound to realm %q, not %q: %w",
				s.path, code, s.realm, domain.ErrInvalid,
			)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: read realm row: %w", err)
	}
	xid, err := newExternalID()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(
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
	db := s.currentDB()
	if db == nil {
		return fmt.Errorf("store: sqlite is closed")
	}
	return s.migrateDB(ctx, db, s.databaseCreated)
}

func (s *sqliteStore) migrateDB(
	ctx context.Context, db *sql.DB, databaseCreated bool,
) error {
	if err := migration.Apply(
		ctx,
		db,
		schema.NewEmbeddedMigrationSource(s.dialect),
		migration.Config{},
		s.dialect,
	); err != nil {
		return err
	}
	if err := s.ensureRealmRowDB(ctx, db); err != nil {
		return err
	}
	dictionaries, err := seedEnumDictionaries(ctx, db)
	if err != nil {
		return err
	}
	if err := s.configureSealing(ctx, db, databaseCreated); err != nil {
		return err
	}
	s.enumCodes.Store(dictionaries)
	return nil
}

// SchemaVersion returns the highest applied schema version, or zero.
func (s *sqliteStore) SchemaVersion(ctx context.Context) (int, error) {
	db := s.currentDB()
	if db == nil {
		return 0, fmt.Errorf("store: sqlite is closed")
	}
	return migration.SchemaVersion(ctx, db, migration.Config{})
}

// Ping verifies the database is reachable.
func (s *sqliteStore) Ping(ctx context.Context) error {
	db := s.currentDB()
	if db == nil {
		return fmt.Errorf("store: sqlite is closed")
	}
	err := db.PingContext(ctx)
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
	oldDB := s.currentDB()
	if oldDB == nil {
		return fmt.Errorf("store: reset sqlite: database is closed")
	}

	tmp, err := os.CreateTemp(
		filepath.Dir(path), filepath.Base(path)+".reset-*.db",
	)
	if err != nil {
		return fmt.Errorf("store: reset create temporary sqlite: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: reset close temporary sqlite file: %w", err)
	}
	if err := os.Remove(tmpPath); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: reset remove temporary sqlite %q: %w", tmpPath, err)
	}

	tmpDB, err := openSQLiteDB(tmpPath)
	if err != nil {
		return fmt.Errorf("store: reset open temporary sqlite at %q: %w", tmpPath, err)
	}
	if err := s.migrateDB(ctx, tmpDB, true); err != nil {
		_ = tmpDB.Close()
		return errors.Join(
			fmt.Errorf("store: reset migrate: %w", err),
			cleanupSQLiteResetTemp(tmpPath),
		)
	}
	if err := tmpDB.Close(); err != nil {
		return errors.Join(
			fmt.Errorf("store: reset close migrated temporary sqlite: %w", err),
			cleanupSQLiteResetTemp(tmpPath),
		)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return errors.Join(
			fmt.Errorf("store: reset replace sqlite at %q: %w", path, err),
			cleanupSQLiteResetTemp(tmpPath),
		)
	}
	db, err := openSQLiteDB(path)
	if err != nil {
		return fmt.Errorf("store: reset reopen sqlite at %q: %w", path, err)
	}
	s.db.Store(db)
	s.mu.Lock()
	s.staleDBs = append(s.staleDBs, oldDB)
	s.mu.Unlock()
	return nil
}

func sqliteResetPaths(path string) []string {
	return []string{path, path + "-wal", path + "-shm", path + "-journal"}
}

func cleanupSQLiteResetTemp(path string) error {
	var result error
	for _, candidate := range sqliteResetPaths(path) {
		if err := os.Remove(candidate); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			result = errors.Join(
				result,
				fmt.Errorf("store: remove reset temporary sqlite %q: %w", candidate, err),
			)
		}
	}
	return result
}

// rollbackTransaction returns a rollback failure alongside the operation result.
func rollbackTransaction(result *error, tx *sql.Tx) {
	err := tx.Rollback()
	if err == nil || errors.Is(err, sql.ErrTxDone) {
		return
	}
	rollbackErr := fmt.Errorf("store: rollback transaction: %w", err)
	if *result == nil {
		*result = rollbackErr
		return
	}
	*result = errors.Join(*result, rollbackErr)
}

// Path returns the on-disk location of the database.
func (s *sqliteStore) Path() string { return s.path }

// Close releases the connection pool. Idempotent.
func (s *sqliteStore) Close() error {
	current := s.db.Swap(nil)
	s.mu.Lock()
	stale := s.staleDBs
	s.staleDBs = nil
	s.mu.Unlock()

	var err error
	if current != nil {
		err = errors.Join(err, current.Close())
	}
	for _, db := range stale {
		if db != nil && db != current {
			err = errors.Join(err, db.Close())
		}
	}
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

func (s *sqliteStore) currentDB() *sql.DB { return s.db.Load() }

// db returns the shared connection pool, or the closed-store error once Close
// has swapped the pool out. Every realm data method resolves the pool through
// here so a shutdown/Close race fails gracefully with the same error the
// lifecycle methods return instead of dereferencing a nil pool.
func (r *realmStore) db() (*sql.DB, error) {
	db := r.store.currentDB()
	if db == nil {
		return nil, fmt.Errorf("store: sqlite is closed")
	}
	return db, nil
}

func (r *realmStore) dictionaries() (*enumDictionaries, error) {
	return r.store.enumDictionaries()
}

// --- Shared helpers reused by every table group -----------------------------

// storedTimeLayout is the fixed-width form of every stored timestamp: UTC with
// nine fractional digits, so the text order of a timestamp column is
// chronological. time.RFC3339Nano trims trailing zeros, which puts
// 10:00:00.12345Z after 10:00:00.123456Z in text. The parse sites keep
// time.RFC3339Nano, which accepts this width.
const storedTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// timeStr formats t for a stored timestamp column or a comparison against one.
func timeStr(t time.Time) string {
	return t.UTC().Format(storedTimeLayout)
}

// nowStr returns the current time formatted for a stored timestamp column.
func nowStr() string {
	return timeStr(time.Now())
}

// newExternalID draws 16 crypto/rand bytes and encodes them as a domain external
// id. This is the single place machine-record external ids are generated; there
// is no per-insert existence check because 128 bits of randomness make a
// collision negligible. The connector fills this into the external_id column at
// insert time (the SQLite dialect has no server-side random default).
func newExternalID() (domain.ExternalID, error) {
	id, err := domain.NewExternalID()
	if err != nil {
		return domain.ExternalID(""), fmt.Errorf("store: generate external id: %w", err)
	}
	return id, nil
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

// sqlScanner is the scan surface shared by *sql.Row and *sql.Rows.
type sqlScanner interface {
	Scan(...any) error
}

// sqlExecer is the write surface shared by *sql.DB and *sql.Tx.
type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type sqlReadWriter interface {
	sqlQueryer
	sqlExecer
}

// resolveAssetID resolves an asset code to its surrogate id for an insert or a
// query. An unknown code is reported as an error wrapping domain.ErrInvalid so a
// dangling dictionary reference never silently inserts a bad foreign key.
func resolveAssetID(ctx context.Context, q sqlQueryer, code string) (int64, error) {
	return resolveDictID(ctx, q, "asset", "asset", code)
}

// resolveAccountID resolves an account code to its surrogate id.
func resolveAccountID(ctx context.Context, q sqlQueryer, code domain.AccountID) (int64, error) {
	return resolveDictID(ctx, q, "account", "account", code.String())
}

// resolveGroupID resolves a group code to its surrogate id.
func resolveGroupID(ctx context.Context, q sqlQueryer, code string) (int64, error) {
	return resolveDictID(ctx, q, "account_group", "group", code)
}

// resolveAssetClassID resolves an asset-class code to its surrogate id.
func resolveAssetClassID(ctx context.Context, q sqlQueryer, code string) (int64, error) {
	return resolveDictID(ctx, q, "asset_class", "asset class", code)
}

// resolvePrincipalID resolves a principal code to its surrogate id.
func resolvePrincipalID(ctx context.Context, q sqlQueryer, code string) (int64, error) {
	return resolveDictID(ctx, q, "principal", "principal", code)
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

const (
	sourceKindTable             = schema.SourceKindTable
	orderEventTypeTable         = schema.OrderEventTypeTable
	orderSideTable              = schema.OrderSideTable
	orderAmountKindTable        = schema.OrderAmountKindTable
	orderStatusTable            = schema.OrderStatusTable
	adjustmentStatusTable       = schema.AdjustmentStatusTable
	auditActionTable            = schema.AuditActionTable
	attestationAlgTable         = schema.AttestationAlgTable
	attestationRequestTypeTable = schema.AttestationRequestTypeTable
	attestationModeTable        = schema.AttestationModeTable
)

type enumDictionaries struct {
	codeToID map[string]map[string]int64
	idToCode map[string]map[int64]string
}

func seedEnumDictionaries(
	ctx context.Context, db *sql.DB,
) (seeded *enumDictionaries, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin enum dictionary seed: %w", err)
	}
	defer rollbackTransaction(&err, tx)

	for _, dictionary := range schema.EnumDictionarySeeds() {
		for _, seed := range dictionary.Codes {
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO `+dictionary.Table+` (id, code) VALUES (?, ?)
				 ON CONFLICT(code) DO NOTHING`,
				seed.ID,
				seed.Code,
			); err != nil {
				return nil, fmt.Errorf(
					"store: seed %s %q: %w", dictionary.Table, seed.Code, err,
				)
			}
		}
	}

	result, err := loadEnumDictionaries(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: commit enum dictionary seed: %w", err)
	}
	return result, nil
}

func loadEnumDictionaries(
	ctx context.Context,
	q sqlQueryer,
) (*enumDictionaries, error) {
	seeds := schema.EnumDictionarySeeds()
	result := &enumDictionaries{
		codeToID: make(map[string]map[string]int64, len(seeds)),
		idToCode: make(map[string]map[int64]string, len(seeds)),
	}
	for _, dictionary := range seeds {
		rows, err := q.QueryContext(
			ctx, `SELECT id, code FROM `+dictionary.Table,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"store: load %s dictionary: %w", dictionary.Table, err,
			)
		}
		codes := make(map[string]int64, len(dictionary.Codes))
		ids := make(map[int64]string, len(dictionary.Codes))
		for rows.Next() {
			var id int64
			var code string
			if err := rows.Scan(&id, &code); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf(
					"store: scan %s dictionary: %w", dictionary.Table, err,
				)
			}
			codes[code] = id
			ids[id] = code
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf(
				"store: iterate %s dictionary: %w", dictionary.Table, err,
			)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf(
				"store: close %s dictionary: %w", dictionary.Table, err,
			)
		}
		for _, seed := range dictionary.Codes {
			id, ok := codes[seed.Code]
			if !ok || id != seed.ID {
				return nil, fmt.Errorf(
					"store: %s dictionary code %q has id %d, want %d",
					dictionary.Table,
					seed.Code,
					id,
					seed.ID,
				)
			}
		}
		result.codeToID[dictionary.Table] = codes
		result.idToCode[dictionary.Table] = ids
	}
	return result, nil
}

func (s *sqliteStore) enumDictionaries() (*enumDictionaries, error) {
	dictionaries := s.enumCodes.Load()
	if dictionaries == nil {
		return nil, fmt.Errorf("store: enum dictionaries are unavailable")
	}
	return dictionaries, nil
}

func (d *enumDictionaries) id(table, label, code string) (int64, error) {
	id, ok := d.codeToID[table][code]
	if !ok {
		return 0, fmt.Errorf("unknown %s %q: %w", label, code, domain.ErrInvalid)
	}
	return id, nil
}

func (d *enumDictionaries) code(table, label string, id int64) (string, error) {
	code, ok := d.idToCode[table][id]
	if !ok {
		return "", fmt.Errorf("store: unknown stored %s id %d", label, id)
	}
	return code, nil
}

func storedSource(source domain.Source) domain.Source {
	if source == "" {
		return domain.SourceSystem
	}
	return source
}

func isSQLiteMissingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
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
