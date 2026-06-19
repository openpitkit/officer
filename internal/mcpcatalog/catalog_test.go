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

package mcpcatalog

import "testing"

// TestCatalogueContract pins the exact command set and per-command risk flags.
func TestCatalogueContract(t *testing.T) {
	want := []Command{
		{Name: "health", Mutating: false, Protective: false, Implemented: true, DefaultEnabled: true},
		{Name: "get_account_state", Mutating: false, Protective: false, Implemented: true, DefaultEnabled: true},
		{Name: "get_limits", Mutating: false, Protective: false, Implemented: true, DefaultEnabled: true},
		{Name: "get_audit", Mutating: false, Protective: false, Implemented: true, DefaultEnabled: true},
		{Name: "check_order", Mutating: false, Protective: false, Implemented: true, DefaultEnabled: true},
		{Name: "set_limit", Mutating: true, Protective: true, Implemented: false, DefaultEnabled: false},
		{Name: "set_market_data_instrument", Mutating: true, Protective: true, Implemented: true, DefaultEnabled: false},
		{Name: "arm_killswitch", Mutating: true, Protective: true, Implemented: false, DefaultEnabled: false},
		{Name: "disarm_killswitch", Mutating: true, Protective: true, Implemented: false, DefaultEnabled: false},
		{Name: "submit_order", Mutating: true, Protective: true, Implemented: true, DefaultEnabled: false},
		{Name: "confirm_execution", Mutating: true, Protective: true, Implemented: true, DefaultEnabled: false},
		{Name: "cancel", Mutating: true, Protective: true, Implemented: true, DefaultEnabled: false},
		{Name: "get_next_token", Mutating: true, Protective: true, Implemented: false, DefaultEnabled: false},
		{Name: "report_fill", Mutating: true, Protective: true, Implemented: false, DefaultEnabled: false},
	}

	got := All()
	if len(got) != len(want) {
		t.Fatalf("catalogue size: want %d, got %d", len(want), len(got))
	}
	for i, w := range want {
		g := got[i]
		if g.Name != w.Name {
			t.Errorf("position %d: name want %q got %q", i, w.Name, g.Name)
		}
		if g.Mutating != w.Mutating || g.Protective != w.Protective ||
			g.Implemented != w.Implemented || g.DefaultEnabled != w.DefaultEnabled {
			t.Errorf("%s flags: want %+v got %+v", w.Name, w, g)
		}
		if g.Title == "" || g.AgentDescription == "" {
			t.Errorf("%s: title and agentDescription must be set", w.Name)
		}
	}
}

// TestEffectiveMerge checks that stored overrides win and unstored commands fall
// back to their catalogue default, and stale stored names are ignored.
func TestEffectiveMerge(t *testing.T) {
	stored := map[string]bool{
		"health":      false, // override a default-on command off
		"set_limit":   true,  // override a default-off command on
		"nonexistent": true,  // stale name, must be ignored
	}
	eff := Effective(stored)

	if len(eff) != len(All()) {
		t.Fatalf("effective size: want %d, got %d", len(All()), len(eff))
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
	if eff["arm_killswitch"] != false {
		t.Errorf("arm_killswitch should default off, got %v", eff["arm_killswitch"])
	}
	if _, ok := eff["nonexistent"]; ok {
		t.Errorf("stale stored name leaked into effective map")
	}
}

// TestEnabledForAndLookup checks single-command resolution and lookup.
func TestEnabledForAndLookup(t *testing.T) {
	if EnabledFor("check_order", nil) != true {
		t.Error("check_order should default enabled")
	}
	if EnabledFor("submit_order", nil) != false {
		t.Error("submit_order should default disabled")
	}
	if EnabledFor("submit_order", map[string]bool{"submit_order": true}) != true {
		t.Error("stored override should enable submit_order")
	}
	if EnabledFor("unknown", nil) != false {
		t.Error("unknown command should resolve to disabled")
	}
	if _, ok := Lookup("health"); !ok {
		t.Error("health must be in the catalogue")
	}
	if _, ok := Lookup("unknown"); ok {
		t.Error("unknown must not be in the catalogue")
	}
}
