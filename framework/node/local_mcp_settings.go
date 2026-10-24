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

package node

import (
	"context"
	"fmt"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// ListMcpAccess returns the stored per-command MCP overrides keyed by command.
// MCP access is a control-plane-wide setting with no engine side-effect, so
// this is a plain store read.
func (n *localNode) ListMcpAccess(ctx context.Context) (map[string]bool, error) {
	access, err := n.realm.ListMcpAccess(ctx)
	if err != nil {
		return nil, fmt.Errorf("list mcp access: %w", err)
	}
	return access, nil
}

// SetMcpAccess upserts the enabled state for one MCP command and audits the
// action. There is no engine side-effect: gating happens in the MCP surface,
// so the store is the sole authority and nothing is applied to or reverted
// from the engine.
func (n *localNode) SetMcpAccess(
	ctx context.Context, command string, enabled bool, caller domain.Caller,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.SetMcpAccess(ctx, command, enabled); err != nil {
		return fmt.Errorf("set mcp access: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetMcpAccess,
		Detail: setMcpAccessDetail(command, enabled),
	}); err != nil {
		return fmt.Errorf("audit set mcp access: %w", err)
	}
	return nil
}

// Setting returns the stored value of one realm-wide setting. Realm settings
// have no engine side-effect, so this is a plain store read.
func (n *localNode) Setting(ctx context.Context, key string) (string, bool, error) {
	if err := domain.ValidateSettingKey(key); err != nil {
		return "", false, err
	}
	value, ok, err := n.realm.GetSetting(ctx, key)
	if err != nil {
		return "", false, fmt.Errorf("get setting: %w", err)
	}
	return value, ok, nil
}

// SetSetting upserts one realm-wide setting and audits the action. There is no
// engine side-effect: the store is the sole authority and nothing is applied
// to or reverted from the engine. A key reserved for its owner's dedicated
// write path is refused before anything is written or audited.
func (n *localNode) SetSetting(
	ctx context.Context, key, value string, caller domain.Caller,
) error {
	if err := domain.ValidateSettingWriteKey(key); err != nil {
		return err
	}
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.SetSetting(ctx, key, value); err != nil {
		return fmt.Errorf("set setting: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetSetting,
		Detail: setSettingDetail(key, value),
	}); err != nil {
		return fmt.Errorf("audit set setting: %w", err)
	}
	return nil
}
