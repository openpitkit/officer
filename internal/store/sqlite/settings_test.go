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

// Settings-group tests: MCP access map round-trips keyed by command and the
// realm setting key/value round-trip. MCP access returns a non-nil empty map
// for an empty table.

package sqlite

import (
	"context"
	"testing"
)

func TestMcpAccessRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	// Empty table returns a non-nil empty map.
	access, err := rs.ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	if access == nil {
		t.Fatal("ListMcpAccess returned nil, want non-nil empty map")
	}
	if len(access) != 0 {
		t.Fatalf("ListMcpAccess len = %d, want 0", len(access))
	}

	if err := rs.SetMcpAccess(ctx, "submit_order", false); err != nil {
		t.Fatalf("SetMcpAccess(submit_order): %v", err)
	}
	if err := rs.SetMcpAccess(ctx, "list_accounts", true); err != nil {
		t.Fatalf("SetMcpAccess(list_accounts): %v", err)
	}

	access, err = rs.ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	if len(access) != 2 || access["submit_order"] != false || access["list_accounts"] != true {
		t.Fatalf("ListMcpAccess = %+v", access)
	}

	// Upsert flips the existing command's flag in place (no second row).
	if err := rs.SetMcpAccess(ctx, "submit_order", true); err != nil {
		t.Fatalf("SetMcpAccess(flip): %v", err)
	}
	access, _ = rs.ListMcpAccess(ctx)
	if len(access) != 2 || access["submit_order"] != true {
		t.Fatalf("ListMcpAccess after flip = %+v", access)
	}
}

func TestSettingRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	// The migration seeds no_esign=1, so fresh realms do not require signatures
	// until an operator configures a signing key and enables the mode.
	v, ok, err := rs.GetSetting(ctx, "no_esign")
	if err != nil || !ok {
		t.Fatalf("GetSetting(no_esign): ok=%v err=%v", ok, err)
	}
	if v != "1" {
		t.Fatalf("seeded no_esign = %q, want 1", v)
	}

	// Set then read back (upsert over the seeded row).
	if err := rs.SetSetting(ctx, "no_esign", "0"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	v, _, _ = rs.GetSetting(ctx, "no_esign")
	if v != "0" {
		t.Fatalf("no_esign after set = %q, want 0", v)
	}

	// An absent enum key reports ok=false, not an error.
	if _, ok, err := rs.GetSetting(ctx, "unset_key"); err != nil || ok {
		t.Fatalf("GetSetting(unset): ok=%v err=%v, want ok=false", ok, err)
	}

	// A fresh enum key inserts.
	if err := rs.SetSetting(ctx, "unset_key", "x"); err != nil {
		t.Fatalf("SetSetting(new): %v", err)
	}
	v, ok, _ = rs.GetSetting(ctx, "unset_key")
	if !ok || v != "x" {
		t.Fatalf("unset_key after set = %q ok=%v, want x true", v, ok)
	}
}
