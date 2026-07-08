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

// GetUserSetting returns the stored value for (userID, key). User settings are
// a plain store read with no engine side-effect.
func (n *localNode) GetUserSetting(
	ctx context.Context, userID, key string,
) (string, bool, error) {
	value, ok, err := n.realm.GetUserSetting(ctx, userID, key)
	if err != nil {
		return "", false, fmt.Errorf("get user setting: %w", err)
	}
	return value, ok, nil
}

// SetUserSetting upserts one per-user setting. There is no engine side-effect
// and personal UI preferences are not audited, so this is a guarded store write.
func (n *localNode) SetUserSetting(
	ctx context.Context, userID, key, value string,
) error {
	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.SetUserSetting(ctx, userID, key, value); err != nil {
		return fmt.Errorf("set user setting: %w", err)
	}
	return nil
}
