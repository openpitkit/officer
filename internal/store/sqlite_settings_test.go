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

// Settings-group tests: MCP access map get/set/list round-trips keyed by command,
// and per-user settings get/set/list round-trips keyed by the
// (user_id, setting_key) composite. Both surface non-nil empty results on an
// empty table.

package store

import (
	"context"
	"testing"

	"go.openpit.dev/officer/internal/domain"
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

func TestUserSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	// Empty table: list is non-nil empty, get is ok=false.
	settings, err := rs.ListUserSettings(ctx)
	if err != nil {
		t.Fatalf("ListUserSettings: %v", err)
	}
	if settings == nil {
		t.Fatal("ListUserSettings returned nil, want non-nil empty slice")
	}
	if len(settings) != 0 {
		t.Fatalf("ListUserSettings len = %d, want 0", len(settings))
	}
	if _, ok, err := rs.GetUserSetting(ctx, domain.DefaultUserID, domain.UserSettingWelcomeSeen); err != nil || ok {
		t.Fatalf("GetUserSetting(absent): ok=%v err=%v, want ok=false", ok, err)
	}

	// Set under the default user, read back.
	if err := rs.SetUserSetting(ctx, domain.DefaultUserID, domain.UserSettingWelcomeSeen, "1"); err != nil {
		t.Fatalf("SetUserSetting: %v", err)
	}
	v, ok, err := rs.GetUserSetting(ctx, domain.DefaultUserID, domain.UserSettingWelcomeSeen)
	if err != nil || !ok {
		t.Fatalf("GetUserSetting: ok=%v err=%v", ok, err)
	}
	if v != "1" {
		t.Fatalf("welcome_seen = %q, want 1", v)
	}

	// The composite key is (user_id, setting_key): a different user is a distinct
	// row, not an overwrite.
	if err := rs.SetUserSetting(ctx, "other-user", domain.UserSettingWelcomeSeen, "0"); err != nil {
		t.Fatalf("SetUserSetting(other): %v", err)
	}
	v, _, _ = rs.GetUserSetting(ctx, domain.DefaultUserID, domain.UserSettingWelcomeSeen)
	if v != "1" {
		t.Fatalf("default user welcome_seen after other-user write = %q, want 1", v)
	}

	// Upsert on the same composite key replaces in place.
	if err := rs.SetUserSetting(ctx, domain.DefaultUserID, domain.UserSettingWelcomeSeen, "0"); err != nil {
		t.Fatalf("SetUserSetting(replace): %v", err)
	}
	v, _, _ = rs.GetUserSetting(ctx, domain.DefaultUserID, domain.UserSettingWelcomeSeen)
	if v != "0" {
		t.Fatalf("welcome_seen after replace = %q, want 0", v)
	}

	// List returns both rows, ordered by (user_id, setting_key).
	settings, err = rs.ListUserSettings(ctx)
	if err != nil {
		t.Fatalf("ListUserSettings: %v", err)
	}
	if len(settings) != 2 {
		t.Fatalf("ListUserSettings len = %d, want 2", len(settings))
	}
	if settings[0].UserID != domain.DefaultUserID || settings[1].UserID != "other-user" {
		t.Fatalf("ListUserSettings order = %+v", settings)
	}
}
