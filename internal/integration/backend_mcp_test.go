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

package integration_test

import (
	"context"
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

func TestService_AggregatesReads(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	fn.accounts = []domain.Account{{Code: "a"}, {Code: "b"}}
	fn.limits = node.AccountLimits{
		RateLimits: []domain.LimitRate{{Scope: domain.ScopeBroker}},
	}
	fn.audit = []domain.AuditRow{{ExternalID: domain.ExternalID("audit-1")}}
	ctx := context.Background()

	accounts, err := svc.ListAccounts(ctx)
	if err != nil || len(accounts) != 2 {
		t.Fatalf("ListAccounts: %v len=%d", err, len(accounts))
	}
	limits, err := svc.ListLimits(ctx, "")
	if err != nil || len(limits.RateLimits) != 1 {
		t.Fatalf("ListLimits: %v rate=%d", err, len(limits.RateLimits))
	}
	rows, err := svc.ListAudit(ctx, 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListAudit: %v len=%d", err, len(rows))
	}
}

func TestService_ListMcpAccessMergesDefaults(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	// Override one default-on command off and one default-off command on.
	fn.mcpAccess = map[string]bool{"health": false, "set_limit": true}
	ctx := context.Background()

	commands, err := svc.ListMcpAccess(ctx)
	if err != nil {
		t.Fatalf("ListMcpAccess: %v", err)
	}
	got := make(map[string]bool, len(commands))
	for _, c := range commands {
		got[c.Command.Name] = c.Enabled
	}
	if got["health"] != false {
		t.Errorf("health override not applied: %v", got["health"])
	}
	if got["set_limit"] != true {
		t.Errorf("set_limit override not applied: %v", got["set_limit"])
	}
	if got["get_limits"] != true {
		t.Errorf("get_limits should default on, got %v", got["get_limits"])
	}
}

func TestService_SetMcpAccessValidatesCommand(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	ctx := context.Background()

	if err := svc.SetMcpAccess(ctx, "not_a_command", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound for unknown command, got %v", err)
	}
	if len(fn.setMcpAccessCall) != 0 {
		t.Fatalf("unknown command must not reach the node")
	}

	if err := svc.SetMcpAccess(ctx, "health", false); err != nil {
		t.Fatalf("SetMcpAccess: %v", err)
	}
	if len(fn.setMcpAccessCall) != 1 || fn.setMcpAccessCall[0].command != "health" ||
		fn.setMcpAccessCall[0].enabled != false {
		t.Fatalf("valid set must route to node: %+v", fn.setMcpAccessCall)
	}
}

func TestService_CommandEnabledResolves(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	fn.mcpAccess = map[string]bool{"check_order": false}
	ctx := context.Background()

	if enabled, err := svc.CommandEnabled(ctx, "check_order"); err != nil || enabled {
		t.Fatalf("check_order override should disable: enabled=%v err=%v", enabled, err)
	}
	if enabled, err := svc.CommandEnabled(ctx, "health"); err != nil || !enabled {
		t.Fatalf("health should default enabled: enabled=%v err=%v", enabled, err)
	}
	if enabled, err := svc.CommandEnabled(ctx, "submit_order"); err != nil || enabled {
		t.Fatalf("submit_order should default disabled: enabled=%v err=%v", enabled, err)
	}
}
