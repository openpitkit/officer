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

// Settings group of the SQLite store: the per-command MCP access overrides and
// the per-user UI settings. Both are plain key/value tables: mcp_access is keyed
// by the command name, user_settings by the (user_id, setting_key) composite.
// The keys are hardcoded enums stored as TEXT (validated by the surfaces that own
// them), never dictionaries, so there is no surrogate id and no code resolution
// in this group.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.openpit.dev/officer/framework/domain"
)

// --- MCP access control -----------------------------------------------------

// ListMcpAccess returns the stored per-command MCP enable/disable overrides,
// keyed by command name. An empty table returns a non-nil empty map.
func (r *realmStore) ListMcpAccess(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db().QueryContext(
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
	if _, err := r.db().ExecContext(
		ctx,
		`INSERT INTO mcp_access (command, enabled) VALUES (?, ?)
		 ON CONFLICT(command) DO UPDATE SET enabled = excluded.enabled`,
		command, enabled,
	); err != nil {
		return fmt.Errorf("store: set mcp access: %w", err)
	}
	return nil
}

// --- User settings ----------------------------------------------------------

// GetUserSetting returns the stored value for (userID, key). The bool is false
// when the row is absent; callers then apply their own default. The key is a
// hardcoded enum stored as TEXT, not a dictionary code.
func (r *realmStore) GetUserSetting(
	ctx context.Context, userID, key string,
) (string, bool, error) {
	var value string
	err := r.db().QueryRowContext(
		ctx,
		`SELECT setting_value FROM user_settings
		 WHERE user_id = ? AND setting_key = ?`,
		userID, key,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: get user setting: %w", err)
	}
	return value, true, nil
}

// SetUserSetting upserts one per-user key-value setting keyed by the
// (user_id, setting_key) composite primary key.
func (r *realmStore) SetUserSetting(
	ctx context.Context, userID, key, value string,
) error {
	if _, err := r.db().ExecContext(
		ctx,
		`INSERT INTO user_settings (user_id, setting_key, setting_value)
		 VALUES (?, ?, ?)
		 ON CONFLICT(user_id, setting_key) DO UPDATE SET
		   setting_value = excluded.setting_value`,
		userID, key, value,
	); err != nil {
		return fmt.Errorf("store: set user setting: %w", err)
	}
	return nil
}

// ListUserSettings returns every persisted user setting, ordered by
// (user_id, setting_key) for stable backups. An empty table returns a non-nil
// empty slice.
func (r *realmStore) ListUserSettings(
	ctx context.Context,
) ([]domain.UserSetting, error) {
	rows, err := r.db().QueryContext(
		ctx,
		`SELECT user_id, setting_key, setting_value FROM user_settings
		 ORDER BY user_id, setting_key`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list user settings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	settings := make([]domain.UserSetting, 0)
	for rows.Next() {
		var s domain.UserSetting
		if err := rows.Scan(&s.UserID, &s.Key, &s.Value); err != nil {
			return nil, fmt.Errorf("store: scan user setting: %w", err)
		}
		settings = append(settings, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate user settings: %w", err)
	}
	return settings, nil
}
