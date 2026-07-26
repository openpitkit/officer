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

package tools

import (
	"reflect"
	"testing"

	"go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/mcp/catalog"
)

func openCatalog() catalog.Catalog {
	reg := mcp.NewToolRegistry()
	RegisterTools(reg, nil)
	return reg.Catalog()
}

// TestCatalogParity pins the exact command set and per-command metadata.
func TestCatalogParity(t *testing.T) {
	want := []catalog.CatalogCommand{
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
			AgentDescription: "Read one order by id with its approval envelope and fills.",
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
			AgentDescription: "Record confirmation history for an untouched workflow order.",
			Mutating:         true,
			Protective:       true,
			Implemented:      true,
			DefaultEnabled:   false,
		},
		{
			Name:             "cancel",
			Title:            "Cancel",
			AgentDescription: "Cancel an untouched workflow order with its approval token.",
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

	if got := openCatalog().All(); !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog parity mismatch\nwant: %#v\ngot:  %#v", want, got)
	}
}

func TestImplementedDescriptorOrder(t *testing.T) {
	reg := mcp.NewToolRegistry()
	RegisterTools(reg, nil)
	var got []string
	for _, d := range reg.Descriptors() {
		if d.Register != nil {
			got = append(got, d.Name)
		}
	}
	want := []string{
		"health",
		"get_account_state",
		"get_limits",
		"get_order",
		"get_audit",
		"check_order",
		"set_market_data_instrument",
		"submit_order",
		"confirm_execution",
		"cancel",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("implemented tool order mismatch\nwant: %#v\ngot:  %#v", want, got)
	}
}

func TestEffectiveMerge(t *testing.T) {
	cat := openCatalog()
	stored := map[string]bool{
		"health":      false,
		"set_limit":   true,
		"nonexistent": true,
	}
	eff := cat.Effective(stored)

	if len(eff) != len(cat.All()) {
		t.Fatalf("effective size: want %d, got %d", len(cat.All()), len(eff))
	}
	if eff["health"] != false {
		t.Errorf("health override not applied: %v", eff["health"])
	}
	if eff["set_limit"] != true {
		t.Errorf("set_limit override not applied: %v", eff["set_limit"])
	}
	if eff["get_limits"] != true {
		t.Errorf("get_limits should default on, got %v", eff["get_limits"])
	}
	if _, ok := eff["nonexistent"]; ok {
		t.Errorf("stale stored name leaked into effective map")
	}
}

func TestEnabledForAndLookup(t *testing.T) {
	cat := openCatalog()
	if cat.EnabledFor("check_order", nil) != true {
		t.Error("check_order should default enabled")
	}
	if cat.EnabledFor("submit_order", nil) != false {
		t.Error("submit_order should default disabled")
	}
	if cat.EnabledFor("submit_order", map[string]bool{"submit_order": true}) != true {
		t.Error("stored override should enable submit_order")
	}
	if cat.EnabledFor("unknown", nil) != false {
		t.Error("unknown command should resolve to disabled")
	}
	if _, ok := cat.Lookup("health"); !ok {
		t.Error("health must be in the catalogue")
	}
	if _, ok := cat.Lookup("unknown"); ok {
		t.Error("unknown must not be in the catalogue")
	}
}
