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
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

//go:embed migrations/*.sql
var migrationsFS embed.FS

type migration struct {
	name    string
	sql     string
	version int
}

// sqliteStore is the SQLite-backed Store. Safe for concurrent use: all
// mutations go through database/sql which pools a single connection.
type sqliteStore struct {
	db   *sql.DB
	path string

	mu        sync.Mutex
	reachable bool
}

// NewSQLiteStore opens (creating if absent) the SQLite database at path.
// The caller must run Migrate before any read or write. Close releases the
// underlying connection pool.
//
// The connection still opens the path as given; only the value Path() reports is
// resolved to an absolute filesystem path so the operator sees the full
// location. filepath.Abs returns an already-absolute path unchanged and is the
// identity on a path it cannot resolve only via the error branch, where the
// original string is kept.
func NewSQLiteStore(path string) (Store, error) {
	db, err := sql.Open(driverName, path)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite at %q: %w", path, err)
	}
	// Single open connection avoids "database is locked" under SQLite's
	// file locking.
	db.SetMaxOpenConns(1)

	displayPath := path
	if abs, absErr := filepath.Abs(path); absErr == nil {
		displayPath = abs
	}
	return &sqliteStore{db: db, path: displayPath}, nil
}

// Migrate brings the schema up to the version this build expects. Idempotent.
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
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
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

// Path returns the on-disk location of the database.
func (s *sqliteStore) Path() string { return s.path }

// ListAccounts returns every persisted account across all tenants.
func (s *sqliteStore) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT tenant, id, blocked, block_reason, group_id, notes
		 FROM accounts ORDER BY tenant, id`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	accounts := make([]domain.Account, 0)
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate accounts: %w", err)
	}
	return accounts, nil
}

// GetAccount returns the account identified by (tenant, id).
func (s *sqliteStore) GetAccount(
	ctx context.Context,
	tenant domain.TenantID,
	id domain.AccountID,
) (domain.Account, bool, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT tenant, id, blocked, block_reason, group_id, notes FROM accounts
		 WHERE tenant = ? AND id = ?`,
		tenant.String(), id.String(),
	)
	a, err := scanAccountRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Account{}, false, nil
	}
	if err != nil {
		return domain.Account{}, false, fmt.Errorf("store: get account: %w", err)
	}
	return a, true, nil
}

// CreateAccount persists a new account. Returns domain.ErrAlreadyExists when
// the (tenant, id) pair already exists.
func (s *sqliteStore) CreateAccount(ctx context.Context, account domain.Account) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO accounts (tenant, id, blocked, block_reason, group_id, notes)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		account.Tenant.String(),
		account.ID.String(),
		account.Blocked,
		account.BlockReason,
		account.GroupID,
		account.Notes,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return fmt.Errorf("account %q: %w", account.ID, domain.ErrAlreadyExists)
		}
		return fmt.Errorf("store: create account: %w", err)
	}
	return nil
}

// SetAccountBlocked updates blocked and block_reason for an account.
func (s *sqliteStore) SetAccountBlocked(
	ctx context.Context,
	tenant domain.TenantID,
	id domain.AccountID,
	blocked bool,
	reason string,
) error {
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE accounts SET blocked = ?, block_reason = ? WHERE tenant = ? AND id = ?`,
		blocked, reason, tenant.String(), id.String(),
	)
	if err != nil {
		return fmt.Errorf("store: set account blocked: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set account blocked rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("account %q: %w", id, domain.ErrNotFound)
	}
	return nil
}

// SetAccountGroup updates the group_id for an account.
func (s *sqliteStore) SetAccountGroup(
	ctx context.Context,
	tenant domain.TenantID,
	id domain.AccountID,
	groupID string,
) error {
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE accounts SET group_id = ? WHERE tenant = ? AND id = ?`,
		groupID, tenant.String(), id.String(),
	)
	if err != nil {
		return fmt.Errorf("store: set account group: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set account group rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("account %q: %w", id, domain.ErrNotFound)
	}
	return nil
}

// SetAccountNotes updates the notes for an account.
func (s *sqliteStore) SetAccountNotes(
	ctx context.Context,
	tenant domain.TenantID,
	id domain.AccountID,
	notes string,
) error {
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE accounts SET notes = ? WHERE tenant = ? AND id = ?`,
		notes, tenant.String(), id.String(),
	)
	if err != nil {
		return fmt.Errorf("store: set account notes: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set account notes rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("account %q: %w", id, domain.ErrNotFound)
	}
	return nil
}

// ListLimits returns barriers for all (or one) account, ordered by
// (policy, scope, account, asset).
func (s *sqliteStore) ListLimits(
	ctx context.Context,
	account domain.AccountID,
) ([]domain.Limit, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if account == "" {
		rows, err = s.db.QueryContext(
			ctx,
			`SELECT tenant, policy, scope, account, asset, kind, value FROM limits
			 ORDER BY tenant, policy, scope, account, asset, kind`,
		)
	} else {
		rows, err = s.db.QueryContext(
			ctx,
			`SELECT tenant, policy, scope, account, asset, kind, value FROM limits
			 WHERE tenant = ? AND account = ?
			 ORDER BY tenant, policy, scope, account, asset, kind`,
			domain.DefaultTenant.String(),
			account.String(),
		)
	}
	if err != nil {
		return nil, fmt.Errorf("store: list limits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return groupLimitRows(rows)
}

// ListPolicyLimits returns all barriers for the given policy.
func (s *sqliteStore) ListPolicyLimits(
	ctx context.Context,
	policy string,
) ([]domain.Limit, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT tenant, policy, scope, account, asset, kind, value FROM limits
		 WHERE policy = ?
		 ORDER BY tenant, policy, scope, account, asset, kind`,
		policy,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list policy limits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return groupLimitRows(rows)
}

// groupLimitRows groups flat kind rows into Limit barriers.
// Rows must be ordered by (tenant, policy, scope, account, asset, kind).
func groupLimitRows(rows *sql.Rows) ([]domain.Limit, error) {
	limits := make([]domain.Limit, 0)
	var cur *domain.Limit

	for rows.Next() {
		var tenant, policy, scope, account, asset, kind, value string
		if err := rows.Scan(&tenant, &policy, &scope, &account, &asset, &kind, &value); err != nil {
			return nil, fmt.Errorf("store: scan limit row: %w", err)
		}
		t := domain.LimitTarget{
			Tenant:  domain.TenantID(tenant),
			Policy:  policy,
			Scope:   scope,
			Account: domain.AccountID(account),
			Asset:   asset,
		}
		if cur == nil || cur.Target != t {
			limits = append(limits, domain.Limit{Target: t})
			cur = &limits[len(limits)-1]
		}
		cur.Values = append(cur.Values, domain.LimitValue{Kind: kind, Value: value})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate limit rows: %w", err)
	}
	return limits, nil
}

// PutLimit upserts a barrier: removes kinds absent from the payload and
// inserts/replaces the rest, all in one transaction.
func (s *sqliteStore) PutLimit(ctx context.Context, limit domain.Limit) error {
	t := limit.Target
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin put_limit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Build the set of incoming kinds.
	newKinds := make([]string, 0, len(limit.Values))
	for _, v := range limit.Values {
		newKinds = append(newKinds, v.Kind)
	}

	// Delete kinds for this target that are not in the payload.
	if len(newKinds) > 0 {
		// Build NOT IN (?, ?, ...) clause.
		placeholders := strings.Repeat("?,", len(newKinds))
		placeholders = placeholders[:len(placeholders)-1]
		args := []any{t.Tenant.String(), t.Policy, t.Scope, t.Account.String(), t.Asset}
		for _, k := range newKinds {
			args = append(args, k)
		}
		q := fmt.Sprintf(
			`DELETE FROM limits WHERE tenant=? AND policy=? AND scope=? AND account=? AND asset=?`+
				` AND kind NOT IN (%s)`,
			placeholders,
		)
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("store: put_limit delete old kinds: %w", err)
		}
	} else {
		// No incoming kinds: remove all existing rows for the target.
		if _, err := tx.ExecContext(
			ctx,
			`DELETE FROM limits WHERE tenant=? AND policy=? AND scope=? AND account=? AND asset=?`,
			t.Tenant.String(), t.Policy, t.Scope, t.Account.String(), t.Asset,
		); err != nil {
			return fmt.Errorf("store: put_limit delete all: %w", err)
		}
	}

	// Insert or replace each value.
	for _, v := range limit.Values {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT OR REPLACE INTO limits (tenant, policy, scope, account, asset, kind, value)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			t.Tenant.String(), t.Policy, t.Scope, t.Account.String(), t.Asset, v.Kind, v.Value,
		); err != nil {
			return fmt.Errorf("store: put_limit upsert kind %q: %w", v.Kind, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit put_limit: %w", err)
	}
	return nil
}

// DeleteLimit removes all kind rows for the given target. Returns
// domain.ErrNotFound when no such barrier exists.
func (s *sqliteStore) DeleteLimit(ctx context.Context, target domain.LimitTarget) error {
	res, err := s.db.ExecContext(
		ctx,
		`DELETE FROM limits WHERE tenant=? AND policy=? AND scope=? AND account=? AND asset=?`,
		target.Tenant.String(), target.Policy, target.Scope,
		target.Account.String(), target.Asset,
	)
	if err != nil {
		return fmt.Errorf("store: delete limit: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete limit rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("limit %+v: %w", target, domain.ErrNotFound)
	}
	return nil
}

// AppendAudit persists a new append-only audit record.
func (s *sqliteStore) AppendAudit(ctx context.Context, entry AuditEntry) error {
	src := entry.Source
	if src == "" {
		// Entries written before Source was modelled default to system.
		src = domain.SourceSystem
	}
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO audit (at, actor, action, tenant, account, detail, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		nowStr(),
		entry.Actor,
		string(entry.Action),
		entry.Tenant.String(),
		entry.Account.String(),
		entry.Detail,
		string(src),
	)
	if err != nil {
		return fmt.Errorf("store: append audit: %w", err)
	}
	return nil
}

// ListAudit returns the most recent n audit rows, newest first.
func (s *sqliteStore) ListAudit(ctx context.Context, n int) ([]domain.AuditRow, error) {
	if n <= 0 {
		return make([]domain.AuditRow, 0), nil
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, at, actor, action, tenant, account, detail, source FROM audit
		 ORDER BY id DESC LIMIT ?`,
		n,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer func() { _ = rows.Close() }()

	audit := make([]domain.AuditRow, 0, n)
	for rows.Next() {
		var (
			id                                              int64
			at, actor, action, tenant, account, detail, src string
		)
		if err := rows.Scan(
			&id, &at, &actor, &action, &tenant, &account, &detail, &src,
		); err != nil {
			return nil, fmt.Errorf("store: scan audit row: %w", err)
		}
		parsedAt, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, fmt.Errorf("store: parse audit timestamp %q: %w", at, err)
		}
		audit = append(audit, domain.AuditRow{
			At:      parsedAt,
			Actor:   actor,
			Action:  domain.AuditAction(action),
			Tenant:  domain.TenantID(tenant),
			Account: domain.AccountID(account),
			Detail:  detail,
			Source:  domain.Source(src),
			ID:      id,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate audit rows: %w", err)
	}
	return audit, nil
}

// --- MCP access control -----------------------------------------------------

// ListMcpAccess returns the stored per-command MCP overrides keyed by command.
func (s *sqliteStore) ListMcpAccess(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT command, enabled FROM mcp_access`)
	if err != nil {
		return nil, fmt.Errorf("store: list mcp access: %w", err)
	}
	defer func() { _ = rows.Close() }()

	access := make(map[string]bool)
	for rows.Next() {
		var (
			command string
			enabled bool
		)
		if err := rows.Scan(&command, &enabled); err != nil {
			return nil, fmt.Errorf("store: scan mcp access row: %w", err)
		}
		access[command] = enabled
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate mcp access rows: %w", err)
	}
	return access, nil
}

// SetMcpAccess upserts the enabled state for one command.
func (s *sqliteStore) SetMcpAccess(
	ctx context.Context, command string, enabled bool,
) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO mcp_access (command, enabled) VALUES (?, ?)`,
		command, enabled,
	)
	if err != nil {
		return fmt.Errorf("store: set mcp access: %w", err)
	}
	return nil
}

// --- Market-data instances --------------------------------------------------

// CreateMarketDataInstance persists a new market-data instance.
func (s *sqliteStore) CreateMarketDataInstance(
	ctx context.Context, instance domain.MarketDataInstance,
) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO market_data_instances (id, type, label, credentials, enabled)
		 VALUES (?, ?, ?, ?, ?)`,
		instance.ID, instance.Type, instance.Label, instance.Credentials, instance.Enabled,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return fmt.Errorf("market-data instance %q label %q: %w",
				instance.ID, instance.Label, domain.ErrAlreadyExists)
		}
		return fmt.Errorf("store: create market-data instance: %w", err)
	}
	return nil
}

// GetMarketDataInstance returns the instance identified by id.
func (s *sqliteStore) GetMarketDataInstance(
	ctx context.Context, id string,
) (domain.MarketDataInstance, bool, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, type, label, credentials, enabled FROM market_data_instances
		 WHERE id = ?`,
		id,
	)
	instance, err := scanMarketDataInstanceRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MarketDataInstance{}, false, nil
	}
	if err != nil {
		return domain.MarketDataInstance{}, false, fmt.Errorf("store: get market-data instance: %w", err)
	}
	return instance, true, nil
}

// ListMarketDataInstances returns every instance, ordered by id.
func (s *sqliteStore) ListMarketDataInstances(
	ctx context.Context,
) ([]domain.MarketDataInstance, error) {
	return s.queryMarketDataInstances(ctx,
		`SELECT id, type, label, credentials, enabled FROM market_data_instances
		 ORDER BY id`)
}

// ListEnabledMarketDataInstances returns the enabled instances, ordered by id.
func (s *sqliteStore) ListEnabledMarketDataInstances(
	ctx context.Context,
) ([]domain.MarketDataInstance, error) {
	return s.queryMarketDataInstances(ctx,
		`SELECT id, type, label, credentials, enabled FROM market_data_instances
		 WHERE enabled = 1 ORDER BY id`)
}

// queryMarketDataInstances runs query (no parameters) and scans the instance
// rows into a non-nil slice.
func (s *sqliteStore) queryMarketDataInstances(
	ctx context.Context, query string,
) ([]domain.MarketDataInstance, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("store: list market-data instances: %w", err)
	}
	defer func() { _ = rows.Close() }()

	instances := make([]domain.MarketDataInstance, 0)
	for rows.Next() {
		instance, err := scanMarketDataInstance(rows)
		if err != nil {
			return nil, err
		}
		instances = append(instances, instance)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate market-data instances: %w", err)
	}
	return instances, nil
}

// SetMarketDataInstanceEnabled toggles the enabled flag of an instance.
func (s *sqliteStore) SetMarketDataInstanceEnabled(
	ctx context.Context, id string, enabled bool,
) error {
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE market_data_instances SET enabled = ? WHERE id = ?`,
		enabled, id,
	)
	if err != nil {
		return fmt.Errorf("store: set market-data instance enabled: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set market-data instance enabled rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("market-data instance %q: %w", id, domain.ErrNotFound)
	}
	return nil
}

// UpdateMarketDataInstanceSettings replaces editable settings of an instance.
func (s *sqliteStore) UpdateMarketDataInstanceSettings(
	ctx context.Context, id, label, credentials string,
) error {
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE market_data_instances SET label = ?, credentials = ? WHERE id = ?`,
		label, credentials, id,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return fmt.Errorf("market-data instance %q label %q: %w",
				id, label, domain.ErrAlreadyExists)
		}
		return fmt.Errorf("store: update market-data instance settings: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update market-data instance settings rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("market-data instance %q: %w", id, domain.ErrNotFound)
	}
	return nil
}

// DeleteMarketDataInstance removes the instance and its instruments in one
// transaction. Instruments are deleted explicitly rather than relying on the
// ON DELETE CASCADE foreign key, because the modernc.org/sqlite driver leaves
// foreign-key enforcement off by default. Returns domain.ErrNotFound when no
// such instance exists.
func (s *sqliteStore) DeleteMarketDataInstance(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin delete market-data instance: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM market_data_quotes WHERE instance_id = ?`,
		id,
	); err != nil {
		return fmt.Errorf("store: delete market-data quotes: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM market_data_instruments WHERE instance_id = ?`,
		id,
	); err != nil {
		return fmt.Errorf("store: delete market-data instruments: %w", err)
	}

	res, err := tx.ExecContext(
		ctx,
		`DELETE FROM market_data_instances WHERE id = ?`,
		id,
	)
	if err != nil {
		return fmt.Errorf("store: delete market-data instance: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete market-data instance rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("market-data instance %q: %w", id, domain.ErrNotFound)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit delete market-data instance: %w", err)
	}
	return nil
}

// --- Market-data instruments ------------------------------------------------

// UpsertMarketDataInstrument inserts or replaces one instrument of an instance.
func (s *sqliteStore) UpsertMarketDataInstrument(
	ctx context.Context, instrument domain.MarketDataInstrument,
) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO market_data_instruments
		 (instance_id, external_symbol, base_asset, quote_asset, manual_price, enabled)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		instrument.InstanceID, instrument.ExternalSymbol,
		instrument.BaseAsset, instrument.QuoteAsset,
		instrument.ManualPrice, instrument.Enabled,
	)
	if err != nil {
		return fmt.Errorf("store: upsert market-data instrument: %w", err)
	}
	return nil
}

// ListMarketDataInstruments returns every instrument of the instance.
func (s *sqliteStore) ListMarketDataInstruments(
	ctx context.Context, instanceID string,
) ([]domain.MarketDataInstrument, error) {
	return s.queryMarketDataInstruments(ctx,
		`SELECT instance_id, external_symbol, base_asset, quote_asset, manual_price, enabled
		 FROM market_data_instruments WHERE instance_id = ? ORDER BY external_symbol`,
		instanceID)
}

// ListEnabledMarketDataInstruments returns the enabled instruments of the
// instance.
func (s *sqliteStore) ListEnabledMarketDataInstruments(
	ctx context.Context, instanceID string,
) ([]domain.MarketDataInstrument, error) {
	return s.queryMarketDataInstruments(ctx,
		`SELECT instance_id, external_symbol, base_asset, quote_asset, manual_price, enabled
		 FROM market_data_instruments WHERE instance_id = ? AND enabled = 1
		 ORDER BY external_symbol`,
		instanceID)
}

// queryMarketDataInstruments runs query with instanceID and scans the
// instrument rows into a non-nil slice.
func (s *sqliteStore) queryMarketDataInstruments(
	ctx context.Context, query, instanceID string,
) ([]domain.MarketDataInstrument, error) {
	rows, err := s.db.QueryContext(ctx, query, instanceID)
	if err != nil {
		return nil, fmt.Errorf("store: list market-data instruments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	instruments := make([]domain.MarketDataInstrument, 0)
	for rows.Next() {
		instrument, err := scanMarketDataInstrument(rows)
		if err != nil {
			return nil, err
		}
		instruments = append(instruments, instrument)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate market-data instruments: %w", err)
	}
	return instruments, nil
}

// SetMarketDataInstrumentEnabled toggles the enabled flag of one instrument.
func (s *sqliteStore) SetMarketDataInstrumentEnabled(
	ctx context.Context, instanceID, externalSymbol string, enabled bool,
) error {
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE market_data_instruments SET enabled = ?
		 WHERE instance_id = ? AND external_symbol = ?`,
		enabled, instanceID, externalSymbol,
	)
	if err != nil {
		return fmt.Errorf("store: set market-data instrument enabled: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set market-data instrument enabled rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("market-data instrument %q/%q: %w",
			instanceID, externalSymbol, domain.ErrNotFound)
	}
	return nil
}

// DeleteMarketDataInstrument removes one instrument of an instance.
func (s *sqliteStore) DeleteMarketDataInstrument(
	ctx context.Context, instanceID, externalSymbol string,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin delete market-data instrument: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM market_data_quotes
		 WHERE instance_id = ? AND external_symbol = ?`,
		instanceID, externalSymbol,
	); err != nil {
		return fmt.Errorf("store: delete market-data quote: %w", err)
	}

	res, err := tx.ExecContext(
		ctx,
		`DELETE FROM market_data_instruments
		 WHERE instance_id = ? AND external_symbol = ?`,
		instanceID, externalSymbol,
	)
	if err != nil {
		return fmt.Errorf("store: delete market-data instrument: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete market-data instrument rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("market-data instrument %q/%q: %w",
			instanceID, externalSymbol, domain.ErrNotFound)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit delete market-data instrument: %w", err)
	}
	return nil
}

// UpsertMarketDataQuote records the latest quote for a configured instrument.
func (s *sqliteStore) UpsertMarketDataQuote(
	ctx context.Context, quote domain.MarketDataQuote,
) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO market_data_quotes
		 (instance_id, external_symbol, base_asset, quote_asset,
		  mark, bid, ask, as_of, received_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		quote.InstanceID, quote.ExternalSymbol, quote.BaseAsset, quote.QuoteAsset,
		quote.Mark, quote.Bid, quote.Ask,
		quote.AsOf.UTC().Format(time.RFC3339Nano),
		quote.ReceivedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("store: upsert market-data quote: %w", err)
	}
	return nil
}

// ListMarketDataQuotes returns latest quotes, ordered by instance and symbol.
func (s *sqliteStore) ListMarketDataQuotes(
	ctx context.Context, instanceID string,
) ([]domain.MarketDataQuote, error) {
	query := `SELECT instance_id, external_symbol, base_asset, quote_asset,
		mark, bid, ask, as_of, received_at
		FROM market_data_quotes`
	args := []any{}
	if instanceID != "" {
		query += ` WHERE instance_id = ?`
		args = append(args, instanceID)
	}
	query += ` ORDER BY instance_id, external_symbol`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list market-data quotes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	quotes := make([]domain.MarketDataQuote, 0)
	for rows.Next() {
		quote, err := scanMarketDataQuote(rows)
		if err != nil {
			return nil, err
		}
		quotes = append(quotes, quote)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate market-data quotes: %w", err)
	}
	return quotes, nil
}

// scanMarketDataInstance scans one instance row from a *sql.Rows cursor.
// Expects columns: id, type, label, credentials, enabled.
func scanMarketDataInstance(rows *sql.Rows) (domain.MarketDataInstance, error) {
	var id, typ, label, credentials string
	var enabled bool
	if err := rows.Scan(&id, &typ, &label, &credentials, &enabled); err != nil {
		return domain.MarketDataInstance{}, fmt.Errorf("store: scan market-data instance: %w", err)
	}
	return domain.MarketDataInstance{
		ID:          id,
		Type:        typ,
		Label:       label,
		Credentials: credentials,
		Enabled:     enabled,
	}, nil
}

// scanMarketDataInstanceRow scans one instance from a *sql.Row (single-row
// query). Expects columns: id, type, label, credentials, enabled.
func scanMarketDataInstanceRow(row *sql.Row) (domain.MarketDataInstance, error) {
	var id, typ, label, credentials string
	var enabled bool
	if err := row.Scan(&id, &typ, &label, &credentials, &enabled); err != nil {
		return domain.MarketDataInstance{}, err
	}
	return domain.MarketDataInstance{
		ID:          id,
		Type:        typ,
		Label:       label,
		Credentials: credentials,
		Enabled:     enabled,
	}, nil
}

// scanMarketDataInstrument scans one instrument row from a *sql.Rows cursor.
// Expects columns: instance_id, external_symbol, base_asset, quote_asset,
// manual_price, enabled.
func scanMarketDataInstrument(rows *sql.Rows) (domain.MarketDataInstrument, error) {
	var instanceID, externalSymbol, baseAsset, quoteAsset, manualPrice string
	var enabled bool
	if err := rows.Scan(
		&instanceID, &externalSymbol, &baseAsset, &quoteAsset, &manualPrice, &enabled,
	); err != nil {
		return domain.MarketDataInstrument{}, fmt.Errorf("store: scan market-data instrument: %w", err)
	}
	return domain.MarketDataInstrument{
		InstanceID:     instanceID,
		ExternalSymbol: externalSymbol,
		BaseAsset:      baseAsset,
		QuoteAsset:     quoteAsset,
		ManualPrice:    manualPrice,
		Enabled:        enabled,
	}, nil
}

func scanMarketDataQuote(rows *sql.Rows) (domain.MarketDataQuote, error) {
	var quote domain.MarketDataQuote
	var asOf, receivedAt string
	if err := rows.Scan(
		&quote.InstanceID,
		&quote.ExternalSymbol,
		&quote.BaseAsset,
		&quote.QuoteAsset,
		&quote.Mark,
		&quote.Bid,
		&quote.Ask,
		&asOf,
		&receivedAt,
	); err != nil {
		return domain.MarketDataQuote{}, fmt.Errorf("store: scan market-data quote: %w", err)
	}
	parsedAsOf, err := time.Parse(time.RFC3339Nano, asOf)
	if err != nil {
		return domain.MarketDataQuote{}, fmt.Errorf("store: parse market-data quote as-of: %w", err)
	}
	parsedReceivedAt, err := time.Parse(time.RFC3339Nano, receivedAt)
	if err != nil {
		return domain.MarketDataQuote{}, fmt.Errorf("store: parse market-data quote received-at: %w", err)
	}
	quote.AsOf = parsedAsOf
	quote.ReceivedAt = parsedReceivedAt
	return quote, nil
}

// --- Account groups ---------------------------------------------------------

// CreateGroup persists a new account group.
func (s *sqliteStore) CreateGroup(
	ctx context.Context,
	group domain.AccountGroup,
) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO account_groups (tenant, id, notes, blocked, block_reason)
		 VALUES (?, ?, ?, ?, ?)`,
		group.Tenant.String(), group.ID, group.Notes, group.Blocked, group.BlockReason,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return fmt.Errorf("group %q: %w", group.ID, domain.ErrAlreadyExists)
		}
		return fmt.Errorf("store: create group: %w", err)
	}
	return nil
}

// GetGroup returns the group identified by (tenant, id).
func (s *sqliteStore) GetGroup(
	ctx context.Context,
	tenant domain.TenantID,
	id string,
) (domain.AccountGroup, bool, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT tenant, id, notes, blocked, block_reason FROM account_groups
		 WHERE tenant = ? AND id = ?`,
		tenant.String(), id,
	)
	g, err := scanGroupRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AccountGroup{}, false, nil
	}
	if err != nil {
		return domain.AccountGroup{}, false, fmt.Errorf("store: get group: %w", err)
	}
	return g, true, nil
}

// ListGroups returns all groups for a tenant.
func (s *sqliteStore) ListGroups(
	ctx context.Context,
	tenant domain.TenantID,
) ([]domain.AccountGroup, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT tenant, id, notes, blocked, block_reason FROM account_groups
		 WHERE tenant = ? ORDER BY id`,
		tenant.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("store: list groups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	groups := make([]domain.AccountGroup, 0)
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate groups: %w", err)
	}
	return groups, nil
}

// SetGroupNotes updates the notes for the identified group.
func (s *sqliteStore) SetGroupNotes(
	ctx context.Context,
	tenant domain.TenantID,
	id string,
	notes string,
) error {
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE account_groups SET notes = ? WHERE tenant = ? AND id = ?`,
		notes, tenant.String(), id,
	)
	if err != nil {
		return fmt.Errorf("store: set group notes: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set group notes rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("group %q: %w", id, domain.ErrNotFound)
	}
	return nil
}

// SetGroupBlocked updates blocked and block_reason for a group.
func (s *sqliteStore) SetGroupBlocked(
	ctx context.Context,
	tenant domain.TenantID,
	id string,
	blocked bool,
	reason string,
) error {
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE account_groups SET blocked = ?, block_reason = ?
		 WHERE tenant = ? AND id = ?`,
		blocked, reason, tenant.String(), id,
	)
	if err != nil {
		return fmt.Errorf("store: set group blocked: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set group blocked rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("group %q: %w", id, domain.ErrNotFound)
	}
	return nil
}

// DeleteGroup removes a group.
func (s *sqliteStore) DeleteGroup(
	ctx context.Context,
	tenant domain.TenantID,
	id string,
) error {
	res, err := s.db.ExecContext(
		ctx,
		`DELETE FROM account_groups WHERE tenant = ? AND id = ?`,
		tenant.String(), id,
	)
	if err != nil {
		return fmt.Errorf("store: delete group: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete group rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("group %q: %w", id, domain.ErrNotFound)
	}
	return nil
}

// ListGroupAccounts returns all accounts whose group_id matches groupID.
func (s *sqliteStore) ListGroupAccounts(
	ctx context.Context,
	tenant domain.TenantID,
	groupID string,
) ([]domain.Account, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT tenant, id, blocked, block_reason, group_id, notes FROM accounts
		 WHERE tenant = ? AND group_id = ? ORDER BY id`,
		tenant.String(), groupID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list group accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	accounts := make([]domain.Account, 0)
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate group accounts: %w", err)
	}
	return accounts, nil
}

func scanGroup(rows *sql.Rows) (domain.AccountGroup, error) {
	var tenant, id, notes, blockReason string
	var blocked bool
	if err := rows.Scan(&tenant, &id, &notes, &blocked, &blockReason); err != nil {
		return domain.AccountGroup{}, fmt.Errorf("store: scan group: %w", err)
	}
	return domain.AccountGroup{
		Tenant:      domain.TenantID(tenant),
		ID:          id,
		Notes:       notes,
		Blocked:     blocked,
		BlockReason: blockReason,
	}, nil
}

func scanGroupRow(row *sql.Row) (domain.AccountGroup, error) {
	var tenant, id, notes, blockReason string
	var blocked bool
	if err := row.Scan(&tenant, &id, &notes, &blocked, &blockReason); err != nil {
		return domain.AccountGroup{}, err
	}
	return domain.AccountGroup{
		Tenant:      domain.TenantID(tenant),
		ID:          id,
		Notes:       notes,
		Blocked:     blocked,
		BlockReason: blockReason,
	}, nil
}

// --- Spot-funds balances ----------------------------------------------------

// orZero returns "0" when s is empty, otherwise s. Used to coerce empty
// amount fields to a valid decimal before persisting so seeding never reads
// an empty string back from the store.
func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// UpsertBalance inserts or replaces a balance snapshot. Empty available/held/
// incoming strings are coerced to "0" so that a single-field adjustment on a
// fresh (account, asset) row never persists an unparseable empty decimal;
// average_entry_price is left as-is because "" is a valid "unset" sentinel.
func (s *sqliteStore) UpsertBalance(ctx context.Context, b domain.Balance) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT OR REPLACE INTO balances
		 (tenant, account, asset, available, held, incoming, realized_pnl,
		  average_entry_price, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.Tenant.String(),
		b.Account.String(),
		b.Asset,
		orZero(b.Available),
		orZero(b.Held),
		orZero(b.Incoming),
		orZero(b.RealizedPnl),
		b.AverageEntryPrice,
		b.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("store: upsert balance: %w", err)
	}
	return nil
}

// GetBalance returns the balance for (tenant, account, asset).
func (s *sqliteStore) GetBalance(
	ctx context.Context,
	tenant domain.TenantID,
	account domain.AccountID,
	asset string,
) (domain.Balance, bool, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT tenant, account, asset, available, held, incoming, realized_pnl,
		        average_entry_price, updated_at
		 FROM balances WHERE tenant = ? AND account = ? AND asset = ?`,
		tenant.String(), account.String(), asset,
	)
	b, err := scanBalanceRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Balance{}, false, nil
	}
	if err != nil {
		return domain.Balance{}, false, fmt.Errorf("store: get balance: %w", err)
	}
	return b, true, nil
}

// ListBalances returns balance rows filtered by the non-empty fields.
func (s *sqliteStore) ListBalances(
	ctx context.Context,
	tenant domain.TenantID,
	account domain.AccountID,
	asset string,
) ([]domain.Balance, error) {
	q := `SELECT tenant, account, asset, available, held, incoming, realized_pnl,
	             average_entry_price, updated_at
	      FROM balances WHERE tenant = ?`
	args := []any{tenant.String()}
	if account != "" {
		q += ` AND account = ?`
		args = append(args, account.String())
	}
	if asset != "" {
		q += ` AND asset = ?`
		args = append(args, asset)
	}
	q += ` ORDER BY account, asset`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list balances: %w", err)
	}
	defer func() { _ = rows.Close() }()

	balances := make([]domain.Balance, 0)
	for rows.Next() {
		var (
			ten, acc, ast, avail, held, incoming, realizedPnl, avgPx, updatedAt string
		)
		if err := rows.Scan(
			&ten, &acc, &ast, &avail, &held, &incoming, &realizedPnl, &avgPx, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("store: scan balance: %w", err)
		}
		t, err := time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("store: parse balance updated_at %q: %w", updatedAt, err)
		}
		balances = append(balances, domain.Balance{
			Tenant:            domain.TenantID(ten),
			Account:           domain.AccountID(acc),
			Asset:             ast,
			Available:         avail,
			Held:              held,
			Incoming:          incoming,
			RealizedPnl:       realizedPnl,
			AverageEntryPrice: avgPx,
			UpdatedAt:         t,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate balances: %w", err)
	}
	return balances, nil
}

// DeleteBalance removes a balance row.
func (s *sqliteStore) DeleteBalance(
	ctx context.Context,
	tenant domain.TenantID,
	account domain.AccountID,
	asset string,
) error {
	res, err := s.db.ExecContext(
		ctx,
		`DELETE FROM balances WHERE tenant = ? AND account = ? AND asset = ?`,
		tenant.String(), account.String(), asset,
	)
	if err != nil {
		return fmt.Errorf("store: delete balance: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete balance rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("balance (%s, %s, %s): %w", tenant, account, asset, domain.ErrNotFound)
	}
	return nil
}

func scanBalanceRow(row *sql.Row) (domain.Balance, error) {
	var ten, acc, ast, avail, held, incoming, realizedPnl, avgPx, updatedAt string
	if err := row.Scan(
		&ten, &acc, &ast, &avail, &held, &incoming, &realizedPnl, &avgPx, &updatedAt,
	); err != nil {
		return domain.Balance{}, err
	}
	t, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return domain.Balance{}, fmt.Errorf("parse updated_at %q: %w", updatedAt, err)
	}
	return domain.Balance{
		Tenant:            domain.TenantID(ten),
		Account:           domain.AccountID(acc),
		Asset:             ast,
		Available:         avail,
		Held:              held,
		Incoming:          incoming,
		RealizedPnl:       realizedPnl,
		AverageEntryPrice: avgPx,
		UpdatedAt:         t,
	}, nil
}

// --- Account adjustments ----------------------------------------------------

// AppendAdjustment records an adjustment outcome. The store assigns ID and at.
func (s *sqliteStore) AppendAdjustment(
	ctx context.Context,
	rec domain.AccountAdjustmentRecord,
) (domain.AccountAdjustmentRecord, error) {
	reqJSON, err := json.Marshal(rec.Request)
	if err != nil {
		return rec, fmt.Errorf("store: marshal adjustment request: %w", err)
	}

	var status domain.AdjustmentStatus
	var outcomeJSON []byte
	switch {
	case rec.Accepted != nil:
		status = domain.AdjustmentStatusAccepted
		outcomeJSON, err = json.Marshal(rec.Accepted)
	case rec.Rejected != nil:
		status = domain.AdjustmentStatusRejected
		outcomeJSON, err = json.Marshal(rec.Rejected)
	default:
		return rec, fmt.Errorf("store: adjustment has neither accepted nor rejected outcome: %w",
			domain.ErrInvalid)
	}
	if err != nil {
		return rec, fmt.Errorf("store: marshal adjustment outcome: %w", err)
	}

	at := nowStr()
	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO adjustments
		 (tenant, account, at, source, principal, asset, status, request, outcome)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.Tenant.String(),
		rec.Account.String(),
		at,
		string(rec.Source),
		rec.Principal,
		rec.Request.Asset,
		string(status),
		string(reqJSON),
		string(outcomeJSON),
	)
	if err != nil {
		return rec, fmt.Errorf("store: append adjustment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return rec, fmt.Errorf("store: adjustment last insert id: %w", err)
	}
	parsedAt, _ := time.Parse(time.RFC3339Nano, at)
	rec.ID = id
	rec.At = parsedAt
	return rec, nil
}

// ListAdjustments returns the most recent n adjustments for an account.
func (s *sqliteStore) ListAdjustments(
	ctx context.Context,
	tenant domain.TenantID,
	account domain.AccountID,
	source domain.Source,
	n int,
) ([]domain.AccountAdjustmentRecord, error) {
	if n <= 0 {
		return make([]domain.AccountAdjustmentRecord, 0), nil
	}
	q := `SELECT id, tenant, account, at, source, principal, asset, status, request, outcome
	      FROM adjustments WHERE tenant = ?`
	args := []any{tenant.String()}
	if account != "" {
		q += ` AND account = ?`
		args = append(args, account.String())
	}
	if source != "" {
		q += ` AND source = ?`
		args = append(args, string(source))
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, n)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list adjustments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.AccountAdjustmentRecord, 0, n)
	for rows.Next() {
		rec, err := scanAdjustment(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate adjustments: %w", err)
	}
	return result, nil
}

func scanAdjustment(rows *sql.Rows) (domain.AccountAdjustmentRecord, error) {
	var (
		id                                                        int64
		ten, acc, at, src, principal, asset, status, req, outcome string
	)
	if err := rows.Scan(
		&id, &ten, &acc, &at, &src, &principal, &asset, &status, &req, &outcome,
	); err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf("store: scan adjustment: %w", err)
	}
	parsedAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return domain.AccountAdjustmentRecord{},
			fmt.Errorf("store: parse adjustment at %q: %w", at, err)
	}
	var request domain.AdjustmentRequest
	if err := json.Unmarshal([]byte(req), &request); err != nil {
		return domain.AccountAdjustmentRecord{},
			fmt.Errorf("store: unmarshal adjustment request: %w", err)
	}
	rec := domain.AccountAdjustmentRecord{
		ID:        id,
		Tenant:    domain.TenantID(ten),
		Account:   domain.AccountID(acc),
		At:        parsedAt,
		Source:    domain.Source(src),
		Principal: principal,
		Request:   request,
	}
	switch domain.AdjustmentStatus(status) {
	case domain.AdjustmentStatusAccepted:
		var a domain.AdjustmentOutcomeAccepted
		if err := json.Unmarshal([]byte(outcome), &a); err != nil {
			return rec, fmt.Errorf("store: unmarshal accepted outcome: %w", err)
		}
		rec.Accepted = &a
	case domain.AdjustmentStatusRejected:
		var r domain.AdjustmentOutcomeRejected
		if err := json.Unmarshal([]byte(outcome), &r); err != nil {
			return rec, fmt.Errorf("store: unmarshal rejected outcome: %w", err)
		}
		rec.Rejected = &r
	}
	return rec, nil
}

// --- Orders -----------------------------------------------------------------

// CreateOrder inserts a new order record; the store assigns ID.
func (s *sqliteStore) CreateOrder(
	ctx context.Context,
	o domain.Order,
) (domain.Order, error) {
	lockJSON, err := json.Marshal(o.LockPrices)
	if err != nil {
		return o, fmt.Errorf("store: marshal lock_prices: %w", err)
	}
	at := nowStr()
	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO orders
		 (tenant, account, at, source, principal,
		  base_asset, quote_asset, side, amount_kind, amount_value,
		  price, status, lock_prices)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.Tenant.String(), o.Account.String(), at,
		string(o.Source), o.Principal,
		o.BaseAsset, o.QuoteAsset,
		string(o.Side), string(o.AmountKind), o.AmountValue,
		o.Price, string(o.Status), string(lockJSON),
	)
	if err != nil {
		return o, fmt.Errorf("store: create order: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return o, fmt.Errorf("store: order last insert id: %w", err)
	}
	parsedAt, _ := time.Parse(time.RFC3339Nano, at)
	o.ID = id
	o.At = parsedAt
	return o, nil
}

// UpdateOrderStatus updates the status field for an order.
func (s *sqliteStore) UpdateOrderStatus(
	ctx context.Context,
	tenant domain.TenantID,
	id int64,
	status domain.OrderStatus,
) error {
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE orders SET status = ? WHERE tenant = ? AND id = ?`,
		string(status), tenant.String(), id,
	)
	if err != nil {
		return fmt.Errorf("store: update order status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update order status rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("order %d: %w", id, domain.ErrNotFound)
	}
	return nil
}

// SetOrderLockPrices replaces the lock_prices column of an order with the JSON
// encoding of prices. Mirrors UpdateOrderStatus: addresses the row by
// (tenant, id) and maps a missing row onto domain.ErrNotFound.
func (s *sqliteStore) SetOrderLockPrices(
	ctx context.Context,
	tenant domain.TenantID,
	id int64,
	prices []string,
) error {
	lockJSON, err := json.Marshal(prices)
	if err != nil {
		return fmt.Errorf("store: marshal lock_prices: %w", err)
	}
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE orders SET lock_prices = ? WHERE tenant = ? AND id = ?`,
		string(lockJSON), tenant.String(), id,
	)
	if err != nil {
		return fmt.Errorf("store: update order lock prices: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update order lock prices rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("order %d: %w", id, domain.ErrNotFound)
	}
	return nil
}

// GetOrder returns the order with its events and trades.
func (s *sqliteStore) GetOrder(
	ctx context.Context,
	tenant domain.TenantID,
	id int64,
) (domain.OrderDetail, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, tenant, account, at, source, principal,
		        base_asset, quote_asset, side, amount_kind, amount_value,
		        price, status, lock_prices
		 FROM orders WHERE tenant = ? AND id = ?`,
		tenant.String(), id,
	)
	o, err := scanOrderRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OrderDetail{}, fmt.Errorf("order %d: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return domain.OrderDetail{}, fmt.Errorf("store: get order: %w", err)
	}

	events, err := s.ListOrderEvents(ctx, tenant, id)
	if err != nil {
		return domain.OrderDetail{}, err
	}
	trades, err := s.listTradesByOrder(ctx, id)
	if err != nil {
		return domain.OrderDetail{}, err
	}
	return domain.OrderDetail{Order: o, Events: events, Trades: trades}, nil
}

// ListOrders returns the most recent n orders for an account.
func (s *sqliteStore) ListOrders(
	ctx context.Context,
	tenant domain.TenantID,
	account domain.AccountID,
	source domain.Source,
	n int,
) ([]domain.Order, error) {
	if n <= 0 {
		return make([]domain.Order, 0), nil
	}
	q := `SELECT id, tenant, account, at, source, principal,
	             base_asset, quote_asset, side, amount_kind, amount_value,
	             price, status, lock_prices
	      FROM orders WHERE tenant = ?`
	args := []any{tenant.String()}
	if account != "" {
		q += ` AND account = ?`
		args = append(args, account.String())
	}
	if source != "" {
		q += ` AND source = ?`
		args = append(args, string(source))
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, n)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list orders: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.Order, 0, n)
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate orders: %w", err)
	}
	return result, nil
}

// CountOrders returns the total number of orders recorded for the tenant.
func (s *sqliteStore) CountOrders(
	ctx context.Context, tenant domain.TenantID,
) (int, error) {
	var n int
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM orders WHERE tenant = ?`,
		tenant.String(),
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count orders: %w", err)
	}
	return n, nil
}

// CountOrdersSince returns the number of orders for the tenant whose `at`
// timestamp is at or after since. `at` is stored as RFC3339Nano UTC text, so the
// boundary is formatted the same way for a lexicographic comparison.
func (s *sqliteStore) CountOrdersSince(
	ctx context.Context, tenant domain.TenantID, since time.Time,
) (int, error) {
	var n int
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM orders WHERE tenant = ? AND at >= ?`,
		tenant.String(), since.UTC().Format(time.RFC3339Nano),
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count orders since: %w", err)
	}
	return n, nil
}

func scanOrder(rows *sql.Rows) (domain.Order, error) {
	var (
		id                                                          int64
		ten, acc, at, src, principal                                string
		baseAsset, quoteAsset, side, amtKind, amtVal, price, status string
		lockJSON                                                    string
	)
	if err := rows.Scan(
		&id, &ten, &acc, &at, &src, &principal,
		&baseAsset, &quoteAsset, &side, &amtKind, &amtVal,
		&price, &status, &lockJSON,
	); err != nil {
		return domain.Order{}, fmt.Errorf("store: scan order: %w", err)
	}
	return buildOrder(id, ten, acc, at, src, principal,
		baseAsset, quoteAsset, side, amtKind, amtVal, price, status, lockJSON)
}

func scanOrderRow(row *sql.Row) (domain.Order, error) {
	var (
		id                                                          int64
		ten, acc, at, src, principal                                string
		baseAsset, quoteAsset, side, amtKind, amtVal, price, status string
		lockJSON                                                    string
	)
	if err := row.Scan(
		&id, &ten, &acc, &at, &src, &principal,
		&baseAsset, &quoteAsset, &side, &amtKind, &amtVal,
		&price, &status, &lockJSON,
	); err != nil {
		return domain.Order{}, err
	}
	return buildOrder(id, ten, acc, at, src, principal,
		baseAsset, quoteAsset, side, amtKind, amtVal, price, status, lockJSON)
}

func buildOrder(
	id int64,
	ten, acc, at, src, principal,
	baseAsset, quoteAsset, side, amtKind, amtVal, price, status, lockJSON string,
) (domain.Order, error) {
	parsedAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return domain.Order{}, fmt.Errorf("store: parse order at %q: %w", at, err)
	}
	var lockPrices []string
	if err := json.Unmarshal([]byte(lockJSON), &lockPrices); err != nil {
		return domain.Order{}, fmt.Errorf("store: unmarshal lock_prices: %w", err)
	}
	return domain.Order{
		ID:          id,
		Tenant:      domain.TenantID(ten),
		Account:     domain.AccountID(acc),
		At:          parsedAt,
		Source:      domain.Source(src),
		Principal:   principal,
		BaseAsset:   baseAsset,
		QuoteAsset:  quoteAsset,
		Side:        domain.OrderSide(side),
		AmountKind:  domain.OrderAmountKind(amtKind),
		AmountValue: amtVal,
		Price:       price,
		Status:      domain.OrderStatus(status),
		LockPrices:  lockPrices,
	}, nil
}

// --- Order events -----------------------------------------------------------

// AppendOrderEvent adds an event to an order's event stream.
func (s *sqliteStore) AppendOrderEvent(
	ctx context.Context,
	ev domain.OrderEvent,
) (domain.OrderEvent, error) {
	payloadJSON, err := json.Marshal(ev.Payload)
	if err != nil {
		return ev, fmt.Errorf("store: marshal event payload: %w", err)
	}
	at := nowStr()
	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO order_events (order_id, at, type, source, principal, payload)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		ev.OrderID, at,
		string(ev.Type), string(ev.Source), ev.Principal,
		string(payloadJSON),
	)
	if err != nil {
		return ev, fmt.Errorf("store: append order event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ev, fmt.Errorf("store: event last insert id: %w", err)
	}
	parsedAt, _ := time.Parse(time.RFC3339Nano, at)
	ev.ID = id
	ev.At = parsedAt
	return ev, nil
}

// ListOrderEvents returns all events for an order, oldest first.
func (s *sqliteStore) ListOrderEvents(
	ctx context.Context,
	_ domain.TenantID, // tenant is enforced via the order FK lookup
	orderID int64,
) ([]domain.OrderEvent, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, order_id, at, type, source, principal, payload
		 FROM order_events WHERE order_id = ? ORDER BY id ASC`,
		orderID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list order events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.OrderEvent, 0)
	for rows.Next() {
		ev, err := scanOrderEvent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate order events: %w", err)
	}
	return result, nil
}

func scanOrderEvent(rows *sql.Rows) (domain.OrderEvent, error) {
	var (
		id, orderID                         int64
		at, typ, src, principal, payloadStr string
	)
	if err := rows.Scan(
		&id, &orderID, &at, &typ, &src, &principal, &payloadStr,
	); err != nil {
		return domain.OrderEvent{}, fmt.Errorf("store: scan order event: %w", err)
	}
	parsedAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return domain.OrderEvent{}, fmt.Errorf("store: parse event at %q: %w", at, err)
	}
	var payload domain.OrderEventPayload
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		return domain.OrderEvent{}, fmt.Errorf("store: unmarshal event payload: %w", err)
	}
	return domain.OrderEvent{
		ID:        id,
		OrderID:   orderID,
		At:        parsedAt,
		Type:      domain.OrderEventType(typ),
		Source:    domain.Source(src),
		Principal: principal,
		Payload:   payload,
	}, nil
}

// --- Trades -----------------------------------------------------------------

// CreateTrade records a fill as a standalone trade row.
func (s *sqliteStore) CreateTrade(
	ctx context.Context,
	t domain.Trade,
) (domain.Trade, error) {
	at := nowStr()
	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO trades
		 (order_id, tenant, account, at, source, principal,
		  base_asset, quote_asset, side, quantity, price, lock_price)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.OrderID,
		t.Tenant.String(), t.Account.String(),
		at,
		string(t.Source), t.Principal,
		t.BaseAsset, t.QuoteAsset,
		string(t.Side), t.Quantity, t.Price, t.LockPrice,
	)
	if err != nil {
		return t, fmt.Errorf("store: create trade: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return t, fmt.Errorf("store: trade last insert id: %w", err)
	}
	parsedAt, _ := time.Parse(time.RFC3339Nano, at)
	t.ID = id
	t.At = parsedAt
	return t, nil
}

// ListTrades returns the most recent n trades for an account.
func (s *sqliteStore) ListTrades(
	ctx context.Context,
	tenant domain.TenantID,
	account domain.AccountID,
	source domain.Source,
	n int,
) ([]domain.Trade, error) {
	if n <= 0 {
		return make([]domain.Trade, 0), nil
	}
	q := `SELECT id, order_id, tenant, account, at, source, principal,
	             base_asset, quote_asset, side, quantity, price, lock_price
	      FROM trades WHERE tenant = ?`
	args := []any{tenant.String()}
	if account != "" {
		q += ` AND account = ?`
		args = append(args, account.String())
	}
	if source != "" {
		q += ` AND source = ?`
		args = append(args, string(source))
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, n)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list trades: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.Trade, 0, n)
	for rows.Next() {
		tr, err := scanTrade(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, tr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate trades: %w", err)
	}
	return result, nil
}

// listTradesByOrder returns all trades for a given order_id, oldest first.
func (s *sqliteStore) listTradesByOrder(
	ctx context.Context,
	orderID int64,
) ([]domain.Trade, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, order_id, tenant, account, at, source, principal,
		        base_asset, quote_asset, side, quantity, price, lock_price
		 FROM trades WHERE order_id = ? ORDER BY id ASC`,
		orderID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list trades by order: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.Trade, 0)
	for rows.Next() {
		tr, err := scanTrade(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, tr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate trades by order: %w", err)
	}
	return result, nil
}

func scanTrade(rows *sql.Rows) (domain.Trade, error) {
	var (
		id, orderID                                             int64
		ten, acc, at, src, principal                            string
		baseAsset, quoteAsset, side, quantity, price, lockPrice string
	)
	if err := rows.Scan(
		&id, &orderID, &ten, &acc, &at, &src, &principal,
		&baseAsset, &quoteAsset, &side, &quantity, &price, &lockPrice,
	); err != nil {
		return domain.Trade{}, fmt.Errorf("store: scan trade: %w", err)
	}
	parsedAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return domain.Trade{}, fmt.Errorf("store: parse trade at %q: %w", at, err)
	}
	return domain.Trade{
		ID:         id,
		OrderID:    orderID,
		Tenant:     domain.TenantID(ten),
		Account:    domain.AccountID(acc),
		At:         parsedAt,
		Source:     domain.Source(src),
		Principal:  principal,
		BaseAsset:  baseAsset,
		QuoteAsset: quoteAsset,
		Side:       domain.OrderSide(side),
		Quantity:   quantity,
		Price:      price,
		LockPrice:  lockPrice,
	}, nil
}

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

// schemaMigrationsDDL creates the migration bookkeeping table before data
// migrations run.
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
	version := 0
	for _, r := range prefix {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("store: non-numeric migration version in %q", name)
		}
		version = version*10 + int(r-'0')
	}
	if prefix == "" {
		return 0, fmt.Errorf("store: empty migration version in %q", name)
	}
	return version, nil
}

// scanAccount scans one account row from a *sql.Rows cursor.
// Expects columns: tenant, id, blocked, block_reason, group_id, notes.
func scanAccount(rows *sql.Rows) (domain.Account, error) {
	var tenant, id, blockReason, groupID, notes string
	var blocked bool
	if err := rows.Scan(&tenant, &id, &blocked, &blockReason, &groupID, &notes); err != nil {
		return domain.Account{}, fmt.Errorf("store: scan account: %w", err)
	}
	return domain.Account{
		Tenant:      domain.TenantID(tenant),
		ID:          domain.AccountID(id),
		Blocked:     blocked,
		BlockReason: blockReason,
		GroupID:     groupID,
		Notes:       notes,
	}, nil
}

// scanAccountRow scans one account from a *sql.Row (single-row query).
// Expects columns: tenant, id, blocked, block_reason, group_id, notes.
func scanAccountRow(row *sql.Row) (domain.Account, error) {
	var tenant, id, blockReason, groupID, notes string
	var blocked bool
	if err := row.Scan(&tenant, &id, &blocked, &blockReason, &groupID, &notes); err != nil {
		return domain.Account{}, err
	}
	return domain.Account{
		Tenant:      domain.TenantID(tenant),
		ID:          domain.AccountID(id),
		Blocked:     blocked,
		BlockReason: blockReason,
		GroupID:     groupID,
		Notes:       notes,
	}, nil
}

// isSQLiteUnique returns true when the error is a SQLite UNIQUE constraint
// violation. modernc.org/sqlite surfaces these as errors whose text contains
// "UNIQUE constraint failed".
func isSQLiteUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
