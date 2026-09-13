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

package backend

import (
	"context"
	"fmt"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/mcp/catalog"
)

// Command describes one MCP command in the catalogue.
type Command = catalog.CatalogCommand

// McpCommand is one MCP command's catalogue metadata paired with its resolved
// effective enabled state, for the operator panel. It is the surface-facing
// view the HTTP layer maps onto its wire DTO.
type McpCommand struct {
	// Command is the catalogue entry (identity, agent description, risk flags,
	// implemented flag, default enabled state).
	Command Command
	// Enabled is the resolved effective state: the stored override when present,
	// otherwise the catalogue default.
	Enabled bool
}

// MarketDataFreshnessTTL is the quote freshness threshold surfaced to the panel.
// A quote older than this is flagged as stale (Stale == true in the status) but
// is still returned so the last known price remains visible. Configurable
// freshness is not yet implemented; it is the single market-data freshness
// window shared by the framework market-data seam.
const MarketDataFreshnessTTL = marketdata.FreshnessTTL

// ListMcpAccess returns the full MCP command catalogue, each entry paired with
// its effective enabled state. The stored per-command overrides are read from
// the group node (MCP access is a control-plane-wide setting, not
// account-scoped) and merged over the catalogue defaults so every command
// resolves to a bool.
func (s *Service) ListMcpAccess(ctx context.Context) ([]McpCommand, error) {
	n := s.node
	stored, err := n.ListMcpAccess(ctx)
	if err != nil {
		return nil, fmt.Errorf("backend: list mcp access: %w", err)
	}
	catalogue := s.catalog()
	effective := catalogue.Effective(stored)
	commands := catalogue.All()
	out := make([]McpCommand, 0, len(commands))
	for _, cmd := range commands {
		enabled := effective[cmd.Name]
		out = append(out, McpCommand{Command: cmd, Enabled: enabled})
	}
	return out, nil
}

// CommandEnabled resolves the effective enabled state of one MCP command: the
// stored override when present, otherwise the catalogue default. An unknown
// command resolves to false. It is the read the MCP surface consults to gate a
// tool call.
func (s *Service) CommandEnabled(ctx context.Context, command string) (bool, error) {
	n := s.node
	stored, err := n.ListMcpAccess(ctx)
	if err != nil {
		return false, fmt.Errorf("backend: read mcp access: %w", err)
	}
	return s.catalog().EnabledFor(command, stored), nil
}

// SetMcpAccess validates the command against the catalogue and upserts its
// enabled state on the group node. An unknown command is rejected with
// domain.ErrNotFound so the surface can map it onto a 404.
func (s *Service) SetMcpAccess(
	ctx context.Context, command string, enabled bool,
) error {
	if !s.hasCommand(command) {
		return fmt.Errorf("mcp command %q: %w", command, domain.ErrNotFound)
	}
	return s.node.SetMcpAccess(ctx, command, enabled, auth.CallerFromContext(ctx))
}

func (s *Service) hasCommand(command string) bool {
	_, ok := s.catalog().Lookup(command)
	return ok
}

func (s *Service) catalog() catalog.Catalog {
	if s.commands == nil {
		return catalog.New(nil)
	}
	return s.commands.Catalog()
}

type staticCatalogProvider struct {
	catalog catalog.Catalog
}

func (p staticCatalogProvider) Catalog() catalog.Catalog {
	return p.catalog
}

// --- User settings ----------------------------------------------------------

// WelcomeSeen reports whether the operator has dismissed the first-run welcome
// dialog with "don't show again". Until then the dialog is shown on every load.
func (s *Service) WelcomeSeen(ctx context.Context) (bool, error) {
	n := s.node
	value, ok, err := n.GetUserSetting(
		ctx, domain.DefaultUserID, domain.UserSettingWelcomeSeen,
	)
	if err != nil {
		return false, fmt.Errorf("backend: read welcome seen: %w", err)
	}
	return ok && value == "1", nil
}

// SetWelcomeSeen records (or clears) the operator's "don't show again" choice
// for the first-run welcome dialog.
func (s *Service) SetWelcomeSeen(ctx context.Context, seen bool) error {
	n := s.node
	value := ""
	if seen {
		value = "1"
	}
	if err := n.SetUserSetting(
		ctx, domain.DefaultUserID, domain.UserSettingWelcomeSeen, value,
	); err != nil {
		return fmt.Errorf("backend: set welcome seen: %w", err)
	}
	return nil
}
