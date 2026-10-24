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

// Settings group of the SQLite store: realm-wide settings and per-command
// MCP access overrides. Both are plain key/value tables: setting is keyed by
// the setting key, mcp_access by the command name. The keys are hardcoded
// enums stored as TEXT (validated by the surfaces that own them), never
// dictionaries, so there is no surrogate id and no code resolution in this
// group.

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// --- Realm settings ---------------------------------------------------------

// GetSetting returns the value of the realm-wide setting stored under key. The
// bool is false when the setting is absent. The key is a hardcoded enum stored
// as TEXT, not a dictionary code.
func (r *realmStore) GetSetting(
	ctx context.Context, key string,
) (string, bool, error) {
	var value string
	db, err := r.db()
	if err != nil {
		return "", false, err
	}
	err = db.QueryRowContext(
		ctx, `SELECT value FROM setting WHERE key = ?`, key,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: get setting: %w", err)
	}
	return value, true, nil
}

// SetSetting upserts the value of the realm-wide setting stored under key.
func (r *realmStore) SetSetting(ctx context.Context, key, value string) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(
		ctx,
		`INSERT INTO setting (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	); err != nil {
		return fmt.Errorf("store: set setting: %w", err)
	}
	return nil
}

// --- MCP access control -----------------------------------------------------

// ListMcpAccess returns the stored per-command MCP enable/disable overrides,
// keyed by command name. An empty table returns a non-nil empty map.
func (r *realmStore) ListMcpAccess(ctx context.Context) (map[string]bool, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(
		ctx, `SELECT command, enabled FROM mcp_access`,
	)
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
			return nil, fmt.Errorf("store: scan mcp access: %w", err)
		}
		access[command] = enabled
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate mcp access: %w", err)
	}
	return access, nil
}

// SetMcpAccess upserts the enabled state for one command.
func (r *realmStore) SetMcpAccess(
	ctx context.Context, command string, enabled bool,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(
		ctx,
		`INSERT INTO mcp_access (command, enabled) VALUES (?, ?)
		 ON CONFLICT(command) DO UPDATE SET enabled = excluded.enabled`,
		command, enabled,
	); err != nil {
		return fmt.Errorf("store: set mcp access: %w", err)
	}
	return nil
}
