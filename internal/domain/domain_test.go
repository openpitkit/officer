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

package domain_test

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"go.openpit.dev/officer/internal/domain"
)

// --- ValidateAccountID ---

func TestValidateAccountID(t *testing.T) {
	t.Parallel()

	ok := []struct {
		name string
		id   string
	}{
		{"simple", "acc-1"},
		{"single char", "a"},
		{"max length", strings.Repeat("x", 64)},
		{"numbers", "1234567890"},
		{"symbols", "acc_1.2/3"},
	}
	for _, tc := range ok {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := domain.ValidateAccountID(domain.AccountID(tc.id)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	bad := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"over 64", strings.Repeat("x", 65)},
		{"leading space", " acc"},
		{"trailing space", "acc "},
		{"non-printable", "acc\x01"},
	}
	for _, tc := range bad {
		t.Run("err/"+tc.name, func(t *testing.T) {
			t.Parallel()
			err := domain.ValidateAccountID(domain.AccountID(tc.id))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

// --- ValidateLimit helpers ---

func limit(policy, scope string, account, asset string, vals ...domain.LimitValue) domain.Limit {
	return domain.Limit{
		Target: domain.LimitTarget{
			Tenant:  domain.DefaultTenant,
			Policy:  policy,
			Scope:   scope,
			Account: domain.AccountID(account),
			Asset:   asset,
		},
		Values: vals,
	}
}

func kv(kind, value string) domain.LimitValue {
	return domain.LimitValue{Kind: kind, Value: value}
}

// --- rate_limit ---

func TestValidateLimit_RateLimit(t *testing.T) {
	t.Parallel()

	ok := []struct {
		name  string
		limit domain.Limit
	}{
		{
			"broker",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxOrders, "100"), kv(domain.KindWindow, "1s")),
		},
		{
			"asset",
			limit(domain.PolicyRateLimit, domain.ScopeAsset, "", "AAPL",
				kv(domain.KindMaxOrders, "1"), kv(domain.KindWindow, "24h")),
		},
		{
			"account",
			limit(domain.PolicyRateLimit, domain.ScopeAccount, "acc-1", "",
				kv(domain.KindMaxOrders, "1000000000"), kv(domain.KindWindow, "500ms")),
		},
		{
			"account_asset",
			limit(domain.PolicyRateLimit, domain.ScopeAccountAsset, "acc-1", "MSFT",
				kv(domain.KindMaxOrders, "50"), kv(domain.KindWindow, "1m")),
		},
	}
	for _, tc := range ok {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := domain.ValidateLimit(tc.limit); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	bad := []struct {
		name  string
		limit domain.Limit
	}{
		{
			"missing window",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxOrders, "10")),
		},
		{
			"missing max_orders",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
				kv(domain.KindWindow, "1s")),
		},
		{
			"max_orders zero",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxOrders, "0"), kv(domain.KindWindow, "1s")),
		},
		{
			"max_orders over 1e9",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxOrders, "1000000001"), kv(domain.KindWindow, "1s")),
		},
		{
			"max_orders negative",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxOrders, "-1"), kv(domain.KindWindow, "1s")),
		},
		{
			"max_orders fractional",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxOrders, "1.5"), kv(domain.KindWindow, "1s")),
		},
		{
			// Non-canonical integer encodings must be rejected at the domain
			// layer so they never reach the engine, which parses with
			// strconv.ParseUint (no exponent, no decimal point).
			"max_orders scientific notation",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxOrders, "1e3"), kv(domain.KindWindow, "1s")),
		},
		{
			"max_orders uppercase exponent",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxOrders, "1E2"), kv(domain.KindWindow, "1s")),
		},
		{
			"max_orders trailing zero decimal",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxOrders, "100.0"), kv(domain.KindWindow, "1s")),
		},
		{
			"window zero",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxOrders, "10"), kv(domain.KindWindow, "0s")),
		},
		{
			"window over 24h",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxOrders, "10"), kv(domain.KindWindow, "25h")),
		},
		{
			"window invalid",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxOrders, "10"), kv(domain.KindWindow, "bad")),
		},
		{
			"unknown kind",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxOrders, "10"), kv(domain.KindWindow, "1s"),
				kv("extra", "x")),
		},
		{
			"scope account_asset missing asset",
			limit(domain.PolicyRateLimit, domain.ScopeAccountAsset, "acc-1", "",
				kv(domain.KindMaxOrders, "10"), kv(domain.KindWindow, "1s")),
		},
		{
			"scope broker with account",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "acc-1", "",
				kv(domain.KindMaxOrders, "10"), kv(domain.KindWindow, "1s")),
		},
		{
			"scope asset missing asset",
			limit(domain.PolicyRateLimit, domain.ScopeAsset, "", "",
				kv(domain.KindMaxOrders, "10"), kv(domain.KindWindow, "1s")),
		},
		{
			"asset whitespace",
			limit(domain.PolicyRateLimit, domain.ScopeAsset, "", "B T C",
				kv(domain.KindMaxOrders, "10"), kv(domain.KindWindow, "1s")),
		},
		{
			"asset over 32",
			limit(domain.PolicyRateLimit, domain.ScopeAsset, "", strings.Repeat("X", 33),
				kv(domain.KindMaxOrders, "10"), kv(domain.KindWindow, "1s")),
		},
		{
			"no values",
			limit(domain.PolicyRateLimit, domain.ScopeBroker, "", ""),
		},
		{
			"duplicate kind",
			domain.Limit{
				Target: domain.LimitTarget{
					Tenant: domain.DefaultTenant,
					Policy: domain.PolicyRateLimit,
					Scope:  domain.ScopeBroker,
				},
				Values: []domain.LimitValue{
					kv(domain.KindMaxOrders, "10"),
					kv(domain.KindMaxOrders, "20"),
					kv(domain.KindWindow, "1s"),
				},
			},
		},
	}
	for _, tc := range bad {
		t.Run("err/"+tc.name, func(t *testing.T) {
			t.Parallel()
			err := domain.ValidateLimit(tc.limit)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

// --- order_size_limit ---

func TestValidateLimit_OrderSizeLimit(t *testing.T) {
	t.Parallel()

	ok := []struct {
		name  string
		limit domain.Limit
	}{
		{
			"broker max_quantity only",
			limit(domain.PolicyOrderSizeLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxQuantity, "100")),
		},
		{
			"broker max_notional only",
			limit(domain.PolicyOrderSizeLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxNotional, "50000.5")),
		},
		{
			"account_asset both",
			limit(domain.PolicyOrderSizeLimit, domain.ScopeAccountAsset, "acc-1", "AAPL",
				kv(domain.KindMaxQuantity, "1"), kv(domain.KindMaxNotional, "999")),
		},
		{
			"asset",
			limit(domain.PolicyOrderSizeLimit, domain.ScopeAsset, "", "MSFT",
				kv(domain.KindMaxQuantity, "0.5")),
		},
	}
	for _, tc := range ok {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := domain.ValidateLimit(tc.limit); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	bad := []struct {
		name  string
		limit domain.Limit
	}{
		{
			"neither kind",
			limit(domain.PolicyOrderSizeLimit, domain.ScopeBroker, "", ""),
		},
		{
			"max_quantity zero",
			limit(domain.PolicyOrderSizeLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxQuantity, "0")),
		},
		{
			"max_notional negative",
			limit(domain.PolicyOrderSizeLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxNotional, "-1")),
		},
		{
			"unknown kind",
			limit(domain.PolicyOrderSizeLimit, domain.ScopeBroker, "", "",
				kv(domain.KindMaxQuantity, "1"), kv("bogus", "x")),
		},
		{
			"scope account not allowed",
			limit(domain.PolicyOrderSizeLimit, domain.ScopeAccount, "acc-1", "",
				kv(domain.KindMaxQuantity, "1")),
		},
	}
	for _, tc := range bad {
		t.Run("err/"+tc.name, func(t *testing.T) {
			t.Parallel()
			err := domain.ValidateLimit(tc.limit)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

// --- pnl_bounds_kill_switch ---

func TestValidateLimit_PnlBounds(t *testing.T) {
	t.Parallel()

	ok := []struct {
		name  string
		limit domain.Limit
	}{
		{
			"asset lower only",
			limit(domain.PolicyPnlBoundsKillSwitch, domain.ScopeAsset, "", "AAPL",
				kv(domain.KindLowerBound, "-1000")),
		},
		{
			"asset upper only",
			limit(domain.PolicyPnlBoundsKillSwitch, domain.ScopeAsset, "", "AAPL",
				kv(domain.KindUpperBound, "5000")),
		},
		{
			"account_asset both equal",
			limit(domain.PolicyPnlBoundsKillSwitch, domain.ScopeAccountAsset, "acc-1", "MSFT",
				kv(domain.KindLowerBound, "0"), kv(domain.KindUpperBound, "0")),
		},
		{
			"account_asset both valid",
			limit(domain.PolicyPnlBoundsKillSwitch, domain.ScopeAccountAsset, "acc-1", "AAPL",
				kv(domain.KindLowerBound, "-500"), kv(domain.KindUpperBound, "1000")),
		},
	}
	for _, tc := range ok {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := domain.ValidateLimit(tc.limit); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	bad := []struct {
		name  string
		limit domain.Limit
	}{
		{
			"neither bound",
			limit(domain.PolicyPnlBoundsKillSwitch, domain.ScopeAsset, "", "AAPL"),
		},
		{
			"lower > upper",
			limit(domain.PolicyPnlBoundsKillSwitch, domain.ScopeAsset, "", "AAPL",
				kv(domain.KindLowerBound, "100"), kv(domain.KindUpperBound, "50")),
		},
		{
			"lower not decimal",
			limit(domain.PolicyPnlBoundsKillSwitch, domain.ScopeAsset, "", "AAPL",
				kv(domain.KindLowerBound, "abc")),
		},
		{
			"scope broker not allowed",
			limit(domain.PolicyPnlBoundsKillSwitch, domain.ScopeBroker, "", "",
				kv(domain.KindLowerBound, "-100")),
		},
		{
			"scope account not allowed",
			limit(domain.PolicyPnlBoundsKillSwitch, domain.ScopeAccount, "acc-1", "",
				kv(domain.KindLowerBound, "-100")),
		},
		{
			"unknown kind",
			limit(domain.PolicyPnlBoundsKillSwitch, domain.ScopeAsset, "", "AAPL",
				kv(domain.KindLowerBound, "-100"), kv("extra", "x")),
		},
	}
	for _, tc := range bad {
		t.Run("err/"+tc.name, func(t *testing.T) {
			t.Parallel()
			err := domain.ValidateLimit(tc.limit)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

// --- unknown policy ---

func TestValidateLimit_UnknownPolicy(t *testing.T) {
	t.Parallel()
	err := domain.ValidateLimit(limit("bogus_policy", domain.ScopeBroker, "", ""))
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

// --- Audit categories ---

func TestAuditAction_Category(t *testing.T) {
	t.Parallel()
	trading := map[domain.AuditAction]bool{
		domain.AuditActionSubmitOrder:     true,
		domain.AuditActionExecutionReport: true,
	}
	for _, action := range domain.AllAuditActions() {
		want := domain.AuditCategoryControl
		if trading[action] {
			want = domain.AuditCategoryTrading
		}
		if got := action.Category(); got != want {
			t.Errorf("Category(%q) = %q, want %q", action, got, want)
		}
	}
}

func TestAuditActionsByCategory_PartitionsCatalogue(t *testing.T) {
	t.Parallel()
	all := domain.AllAuditActions()
	control := domain.AuditActionsByCategory(domain.AuditCategoryControl)
	trading := domain.AuditActionsByCategory(domain.AuditCategoryTrading)

	if len(control)+len(trading) != len(all) {
		t.Fatalf("partition sizes %d+%d != %d", len(control), len(trading), len(all))
	}
	if len(trading) != 2 {
		t.Fatalf("trading category = %d actions, want 2: %+v", len(trading), trading)
	}
	seen := make(map[domain.AuditAction]int, len(all))
	for _, action := range append(append([]domain.AuditAction{}, control...), trading...) {
		seen[action]++
	}
	for _, action := range all {
		if seen[action] != 1 {
			t.Errorf("action %q appears %d times across categories, want 1", action, seen[action])
		}
	}
}

// TestAllAuditActionsCoversEveryConstant guards that AllAuditActions includes
// every AuditAction constant declared in domain.go. It scans the source file
// for lines of the form `AuditActionXxx AuditAction = "literal"` and verifies
// each literal appears in the AllAuditActions slice.
func TestAllAuditActionsCoversEveryConstant(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("domain.go")
	if err != nil {
		t.Fatalf("read domain.go: %v", err)
	}

	// Matches: AuditActionXxx  AuditAction = "literal"
	re := regexp.MustCompile(`AuditAction\w+\s+AuditAction\s*=\s*"([^"]+)"`)
	matches := re.FindAllSubmatch(src, -1)
	if len(matches) < 10 {
		t.Fatalf("regexp found only %d AuditAction constants - check the pattern", len(matches))
	}

	inSlice := make(map[string]struct{}, len(domain.AllAuditActions()))
	for _, action := range domain.AllAuditActions() {
		inSlice[string(action)] = struct{}{}
	}

	for _, m := range matches {
		literal := strings.TrimSpace(string(m[1]))
		if _, ok := inSlice[literal]; !ok {
			t.Errorf("audit action %q is declared but missing from AllAuditActions()", literal)
		}
	}
}
