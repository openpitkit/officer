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

// Package mcpcatalog is the single source of truth for the Pit Officer MCP
// command surface and its operator-controlled enable/disable state. It is a
// neutral, dependency-free package so both the HTTP control-plane surface (via
// the backend) and the MCP adapter can describe and gate commands without an
// import cycle between them.
//
// The catalogue lists every command the MCP surface exposes - both the
// implemented read-only tools and the designed-but-not-yet-built mutating ones -
// so the operator panel can toggle a command's availability even before its
// handler lands. Each command's default enabled state is safe-by-default: the
// read-only tools default on, the protective mutating ones default off.
package mcpcatalog

// Command describes one MCP command in the catalogue: its identity, how an AI
// agent uses it, its risk posture, whether a handler is implemented yet, and the
// enabled state it resolves to when the operator has not stored an override.
type Command struct {
	// Name is the wire name of the MCP tool. For implemented commands it matches
	// the name the tool is registered under in the MCP surface.
	Name string
	// Title is the human-readable label shown in the operator panel.
	Title string
	// AgentDescription is a one-line description of what an AI agent does with
	// the command, written for the calling agent rather than the operator.
	AgentDescription string
	// Mutating reports whether the command changes control-plane or engine state.
	Mutating bool
	// Protective reports whether the command guards capital or risk posture, so
	// the panel can require explicit confirmation before enabling it.
	Protective bool
	// Implemented reports whether a handler backs the command yet. Designed but
	// unbuilt commands are catalogued so their enable/disable state persists
	// ahead of implementation.
	Implemented bool
	// DefaultEnabled is the enabled state used when no operator override is
	// stored for the command.
	DefaultEnabled bool
}

// All returns the ordered MCP command catalogue. The order is the surface's
// presentation order: implemented read-only commands first, then the designed
// mutating commands. The returned slice is a fresh copy the caller may mutate
// without affecting the catalogue.
func All() []Command {
	return []Command{
		{
			Name:             "health",
			Title:            "Health",
			AgentDescription: "Report Pit Officer deployment health (engine version/profile/liveness, store reachability).",
			Mutating:         false,
			Protective:       false,
			Implemented:      true,
			DefaultEnabled:   true,
		},
		{
			Name:             "get_account_state",
			Title:            "Get account state",
			AgentDescription: "Read an account row and its account-scoped risk barriers.",
			Mutating:         false,
			Protective:       false,
			Implemented:      true,
			DefaultEnabled:   true,
		},
		{
			Name:             "get_limits",
			Title:            "Get limits",
			AgentDescription: "List configured risk barriers, optionally filtered by account.",
			Mutating:         false,
			Protective:       false,
			Implemented:      true,
			DefaultEnabled:   true,
		},
		{
			Name:             "get_order",
			Title:            "Get order",
			AgentDescription: "Read one order by external id with its approval envelope and fills.",
			Mutating:         false,
			Protective:       false,
			Implemented:      true,
			DefaultEnabled:   true,
		},
		{
			Name:             "get_audit",
			Title:            "Get audit",
			AgentDescription: "Read recent control-plane audit-log entries.",
			Mutating:         false,
			Protective:       false,
			Implemented:      true,
			DefaultEnabled:   true,
		},
		{
			Name:             "check_order",
			Title:            "Check order",
			AgentDescription: "Run a non-mutating pre-trade dry-run for an order (pass/reject + reasons); changes no state.",
			Mutating:         false,
			Protective:       false,
			Implemented:      true,
			DefaultEnabled:   true,
		},
		{
			Name:             "set_limit",
			Title:            "Set limit",
			AgentDescription: "Create or change a risk-limit / policy barrier.",
			Mutating:         true,
			Protective:       true,
			Implemented:      false,
			DefaultEnabled:   false,
		},
		{
			Name:             "set_market_data_instrument",
			Title:            "Set market-data instrument",
			AgentDescription: "Enable or disable one configured market-data instrument.",
			Mutating:         true,
			Protective:       true,
			Implemented:      true,
			DefaultEnabled:   false,
		},
		{
			Name:             "arm_killswitch",
			Title:            "Arm kill-switch",
			AgentDescription: "Arm the P&L kill-switch for an account/asset.",
			Mutating:         true,
			Protective:       true,
			Implemented:      false,
			DefaultEnabled:   false,
		},
		{
			Name:             "disarm_killswitch",
			Title:            "Disarm kill-switch",
			AgentDescription: "Disarm the P&L kill-switch for an account/asset.",
			Mutating:         true,
			Protective:       true,
			Implemented:      false,
			DefaultEnabled:   false,
		},
		{
			Name:             "submit_order",
			Title:            "Submit order",
			AgentDescription: "Submit an order intent through pre-trade and obtain a signed approval token.",
			Mutating:         true,
			Protective:       true,
			Implemented:      true,
			DefaultEnabled:   false,
		},
		{
			Name:             "confirm_execution",
			Title:            "Confirm execution",
			AgentDescription: "Confirm execution (commit) of a previously approved order.",
			Mutating:         true,
			Protective:       true,
			Implemented:      true,
			DefaultEnabled:   false,
		},
		{
			Name:             "cancel",
			Title:            "Cancel",
			AgentDescription: "Cancel / revoke a pending approval token or reservation.",
			Mutating:         true,
			Protective:       true,
			Implemented:      true,
			DefaultEnabled:   false,
		},
		{
			Name:             "get_next_token",
			Title:            "Get next token",
			AgentDescription: "Fetch the next approval token for a trade from the registry.",
			Mutating:         true,
			Protective:       true,
			Implemented:      false,
			DefaultEnabled:   false,
		},
		{
			Name:             "report_fill",
			Title:            "Report fill",
			AgentDescription: "Report an execution fill to the engine (post-trade).",
			Mutating:         true,
			Protective:       true,
			Implemented:      false,
			DefaultEnabled:   false,
		},
	}
}

// Lookup returns the catalogue command with the given name and whether it
// exists. It is the validation seam for the toggle endpoint: a name absent from
// the catalogue is not a togglable command.
func Lookup(name string) (Command, bool) {
	for _, cmd := range All() {
		if cmd.Name == name {
			return cmd, true
		}
	}
	return Command{}, false
}

// Effective merges the stored enable/disable overrides over the catalogue
// defaults so every catalogue command resolves to a concrete bool. A command
// with a stored row takes the stored value; one without falls back to its
// DefaultEnabled. Stored rows for names no longer in the catalogue are ignored.
func Effective(stored map[string]bool) map[string]bool {
	out := make(map[string]bool, len(All()))
	for _, cmd := range All() {
		if enabled, ok := stored[cmd.Name]; ok {
			out[cmd.Name] = enabled
			continue
		}
		out[cmd.Name] = cmd.DefaultEnabled
	}
	return out
}

// EnabledFor resolves the effective enabled state of a single command from the
// stored overrides. A stored row wins; otherwise the catalogue default applies.
// A name absent from the catalogue resolves to false (an unknown command is not
// enabled).
func EnabledFor(name string, stored map[string]bool) bool {
	if enabled, ok := stored[name]; ok {
		return enabled
	}
	cmd, ok := Lookup(name)
	if !ok {
		return false
	}
	return cmd.DefaultEnabled
}
