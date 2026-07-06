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
	"bytes"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.openpit.dev/officer/framework/domain"
)

// --- ValidateAccountID ---

func TestValidateAccountID(t *testing.T) {
	t.Parallel()

	// multibyte: 64 Cyrillic code points = 128 bytes — must be accepted.
	cyrillic64 := strings.Repeat("я", 64)
	if utf8.RuneCountInString(cyrillic64) != 64 {
		t.Fatal("test setup: expected 64 code points")
	}
	// 65 Cyrillic code points = 130 bytes — must be rejected.
	cyrillic65 := strings.Repeat("я", 65)

	ok := []struct {
		name string
		id   string
	}{
		{"simple", "acc-1"},
		{"single char", "a"},
		{"max length", strings.Repeat("x", 64)},
		{"numbers", "1234567890"},
		{"symbols", "acc_1.2/3"},
		{"multibyte 64 code points", cyrillic64},
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
		{"multibyte 65 code points", cyrillic65},
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

func TestValidateTitle(t *testing.T) {
	t.Parallel()

	// multibyte: 256 CJK code points = 768 bytes — must be accepted.
	cjk256 := strings.Repeat("字", 256)
	if utf8.RuneCountInString(cjk256) != 256 {
		t.Fatal("test setup: expected 256 code points")
	}
	// 257 CJK code points = 771 bytes — must be rejected.
	cjk257 := strings.Repeat("字", 257)

	ok := []struct {
		name  string
		title string
	}{
		{"empty", ""},
		{"display", "Desk Alpha"},
		{"max length ascii", strings.Repeat("x", 256)},
		{"multibyte 256 code points", cjk256},
	}
	for _, tc := range ok {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := domain.ValidateTitle(tc.title); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	bad := []struct {
		name  string
		title string
	}{
		{"over 256 ascii", strings.Repeat("x", 257)},
		{"multibyte 257 code points", cjk257},
		{"non-printable", "Desk\x01Alpha"},
	}
	for _, tc := range bad {
		t.Run("err/"+tc.name, func(t *testing.T) {
			t.Parallel()
			err := domain.ValidateTitle(tc.title)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

// --- ValidateNotes ---

func TestValidateNotes(t *testing.T) {
	t.Parallel()

	// multibyte: 4096 Cyrillic code points = 8192 bytes — must be accepted.
	cyrillic4096 := strings.Repeat("я", 4096)
	if utf8.RuneCountInString(cyrillic4096) != 4096 {
		t.Fatal("test setup: expected 4096 code points")
	}
	// 4097 code points — must be rejected.
	cyrillic4097 := strings.Repeat("я", 4097)

	ok := []struct {
		name  string
		notes string
	}{
		{"empty", ""},
		{"ascii max", strings.Repeat("x", 4096)},
		{"multibyte 4096 code points", cyrillic4096},
	}
	for _, tc := range ok {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := domain.ValidateNotes(tc.notes); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	bad := []struct {
		name  string
		notes string
	}{
		{"over 4096 ascii", strings.Repeat("x", 4097)},
		{"multibyte 4097 code points", cyrillic4097},
	}
	for _, tc := range bad {
		t.Run("err/"+tc.name, func(t *testing.T) {
			t.Parallel()
			err := domain.ValidateNotes(tc.notes)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

// --- ValidateGroupID ---

func TestValidateGroupID(t *testing.T) {
	t.Parallel()

	// multibyte: 64 Cyrillic code points = 128 bytes — must be accepted.
	cyrillic64 := strings.Repeat("я", 64)
	if utf8.RuneCountInString(cyrillic64) != 64 {
		t.Fatal("test setup: expected 64 code points")
	}
	// 65 Cyrillic code points — must be rejected.
	cyrillic65 := strings.Repeat("я", 65)

	ok := []struct {
		name string
		id   string
	}{
		{"simple", "grp-1"},
		{"max length ascii", strings.Repeat("x", 64)},
		{"multibyte 64 code points", cyrillic64},
	}
	for _, tc := range ok {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := domain.ValidateGroupID(tc.id); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	bad := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"over 64 ascii", strings.Repeat("x", 65)},
		{"multibyte 65 code points", cyrillic65},
		{"leading space", " grp"},
		{"trailing space", "grp "},
		{"non-printable", "grp\x01"},
	}
	for _, tc := range bad {
		t.Run("err/"+tc.name, func(t *testing.T) {
			t.Parallel()
			err := domain.ValidateGroupID(tc.id)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

// --- ExternalID codec ---

// TestGeneratedExternalIDFromBytes checks that every distinct 16-byte random
// value encodes to the generated 22-char wire form and parses back as that
// opaque string.
func TestExternalID_RoundTrip(t *testing.T) {
	t.Parallel()

	cases := [][]byte{
		bytes.Repeat([]byte{0x00}, domain.ExternalIDByteLen),
		bytes.Repeat([]byte{0xff}, domain.ExternalIDByteLen),
		{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		{0xde, 0xad, 0xbe, 0xef, 0x00, 0x11, 0x22, 0x33,
			0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb},
	}
	for _, raw := range cases {
		id, err := domain.GeneratedExternalIDFromBytes(raw)
		if err != nil {
			t.Fatalf("GeneratedExternalIDFromBytes(%x): %v", raw, err)
		}
		s := id.String()
		if len(s) != domain.ExternalIDStringLen {
			t.Fatalf("encoded %q has len %d, want %d", s, len(s), domain.ExternalIDStringLen)
		}
		back, err := domain.ParseExternalID(s)
		if err != nil {
			t.Fatalf("ParseExternalID(%q): %v", s, err)
		}
		if back != id {
			t.Fatalf("round-trip mismatch: %x -> %q -> %q", raw, s, back.String())
		}
	}
}

// TestExternalID_Zero checks the unset zero value reports IsZero and that the
// generated non-zero value does not.
func TestExternalID_Zero(t *testing.T) {
	t.Parallel()
	var zero domain.ExternalID
	if !zero.IsZero() {
		t.Fatal("zero value must report IsZero")
	}
	nonzero, err := domain.GeneratedExternalIDFromBytes(
		[]byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	if err != nil {
		t.Fatalf("GeneratedExternalIDFromBytes: %v", err)
	}
	if nonzero.IsZero() {
		t.Fatal("non-zero value must not report IsZero")
	}
}

func TestGeneratedExternalIDFromBytes_WrongLength(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 1, 15, 17, 32} {
		_, err := domain.GeneratedExternalIDFromBytes(make([]byte, n))
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("len %d: expected ErrInvalid, got %v", n, err)
		}
	}
}

func TestExternalIDFromBytes_Empty(t *testing.T) {
	t.Parallel()
	if _, err := domain.ExternalIDFromBytes(nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ExternalIDFromBytes(nil): expected ErrInvalid, got %v", err)
	}
}

func TestParseExternalID_AcceptsOpaqueCallerStrings(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"order-1",
		"short",
		strings.Repeat("A", 23),
		"standard/alphabet+plus",
		"contains spaces",
	} {
		id, err := domain.ParseExternalID(s)
		if err != nil {
			t.Fatalf("ParseExternalID(%q): %v", s, err)
		}
		if id.String() != s {
			t.Fatalf("ParseExternalID(%q) = %q", s, id.String())
		}
		if err := domain.ValidateExternalID(s); err != nil {
			t.Fatalf("ValidateExternalID(%q): %v", s, err)
		}
	}
}

func TestParseExternalID_Empty(t *testing.T) {
	t.Parallel()
	if _, err := domain.ParseExternalID(""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ParseExternalID(empty): expected ErrInvalid, got %v", err)
	}
	if err := domain.ValidateExternalID(""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ValidateExternalID(empty): expected ErrInvalid, got %v", err)
	}
}

// --- Engine id bounds ---

func TestValidateEngineAccountID(t *testing.T) {
	t.Parallel()
	ok := []domain.EngineAccountID{
		domain.EngineAccountID(domain.EngineAccountIDMin),
		1,
		1234567890,
		domain.EngineAccountID(domain.EngineAccountIDMax),
	}
	for _, id := range ok {
		if err := domain.ValidateEngineAccountID(id); err != nil {
			t.Errorf("ValidateEngineAccountID(%d): unexpected error %v", id, err)
		}
	}
	bad := []domain.EngineAccountID{
		0,
		domain.EngineAccountID(domain.EngineAccountIDMax) + 1,
		domain.EngineAccountID(1 << 63),
	}
	for _, id := range bad {
		if err := domain.ValidateEngineAccountID(id); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("ValidateEngineAccountID(%d): expected ErrInvalid, got %v", id, err)
		}
	}
}

func TestValidateEngineGroupID(t *testing.T) {
	t.Parallel()
	ok := []domain.EngineGroupID{
		domain.EngineGroupID(domain.EngineGroupIDMin),
		1,
		65535,
		domain.EngineGroupID(domain.EngineGroupIDMax),
	}
	for _, id := range ok {
		if err := domain.ValidateEngineGroupID(id); err != nil {
			t.Errorf("ValidateEngineGroupID(%d): unexpected error %v", id, err)
		}
	}
	if err := domain.ValidateEngineGroupID(0); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("ValidateEngineGroupID(0): expected ErrInvalid, got %v", err)
	}
}

// --- LimitRate ---

func TestLimitRate_Validate(t *testing.T) {
	t.Parallel()

	ok := []struct {
		name  string
		limit domain.LimitRate
	}{
		{"broker", domain.LimitRate{
			Scope: domain.ScopeBroker, MaxOrders: 100, Window: time.Second}},
		{"asset", domain.LimitRate{
			Scope: domain.ScopeAsset, Asset: "AAPL", MaxOrders: 1, Window: 24 * time.Hour}},
		{"account", domain.LimitRate{
			Scope: domain.ScopeAccount, Account: "acc-1",
			MaxOrders: 1_000_000_000, Window: 500 * time.Millisecond}},
		{"account_asset", domain.LimitRate{
			Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "MSFT",
			MaxOrders: 50, Window: time.Minute}},
		// Officer enforces axis presence only; the asset/account format is parsed
		// and rejected downstream by the engine barrier build, so an interior-space
		// or over-length asset passes Officer's scope/axes check.
		{"asset whitespace", domain.LimitRate{
			Scope: domain.ScopeAsset, Asset: "B T C",
			MaxOrders: 10, Window: time.Second}},
		{"asset over 32", domain.LimitRate{
			Scope: domain.ScopeAsset, Asset: strings.Repeat("X", 33),
			MaxOrders: 10, Window: time.Second}},
	}
	for _, tc := range ok {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.limit.Validate(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	bad := []struct {
		name  string
		limit domain.LimitRate
	}{
		{"max_orders zero", domain.LimitRate{
			Scope: domain.ScopeBroker, MaxOrders: 0, Window: time.Second}},
		{"max_orders over 1e9", domain.LimitRate{
			Scope: domain.ScopeBroker, MaxOrders: 1_000_000_001, Window: time.Second}},
		{"window zero", domain.LimitRate{
			Scope: domain.ScopeBroker, MaxOrders: 10, Window: 0}},
		{"window negative", domain.LimitRate{
			Scope: domain.ScopeBroker, MaxOrders: 10, Window: -time.Second}},
		{"window over 24h", domain.LimitRate{
			Scope: domain.ScopeBroker, MaxOrders: 10, Window: 25 * time.Hour}},
		{"scope account_asset missing asset", domain.LimitRate{
			Scope: domain.ScopeAccountAsset, Account: "acc-1",
			MaxOrders: 10, Window: time.Second}},
		{"scope broker with account", domain.LimitRate{
			Scope: domain.ScopeBroker, Account: "acc-1",
			MaxOrders: 10, Window: time.Second}},
		{"scope asset missing asset", domain.LimitRate{
			Scope: domain.ScopeAsset, MaxOrders: 10, Window: time.Second}},
		{"account missing for account scope", domain.LimitRate{
			Scope: domain.ScopeAccount, MaxOrders: 10, Window: time.Second}},
	}
	for _, tc := range bad {
		t.Run("err/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.limit.Validate(); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

// --- LimitOrderSize ---

func TestLimitOrderSize_Validate(t *testing.T) {
	t.Parallel()

	ok := []struct {
		name  string
		limit domain.LimitOrderSize
	}{
		{"broker max_quantity only", domain.LimitOrderSize{
			Scope: domain.ScopeBroker, MaxQuantity: "100"}},
		{"broker max_notional only", domain.LimitOrderSize{
			Scope: domain.ScopeBroker, MaxNotional: "50000.5"}},
		{"account_asset both", domain.LimitOrderSize{
			Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
			MaxQuantity: "1", MaxNotional: "999"}},
		{"asset", domain.LimitOrderSize{
			Scope: domain.ScopeAsset, Asset: "MSFT", MaxQuantity: "0.5"}},
	}
	for _, tc := range ok {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.limit.Validate(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	bad := []struct {
		name  string
		limit domain.LimitOrderSize
	}{
		{"neither kind", domain.LimitOrderSize{Scope: domain.ScopeBroker}},
		{"max_quantity zero", domain.LimitOrderSize{
			Scope: domain.ScopeBroker, MaxQuantity: "0"}},
		{"max_notional negative", domain.LimitOrderSize{
			Scope: domain.ScopeBroker, MaxNotional: "-1"}},
		{"scope account not allowed", domain.LimitOrderSize{
			Scope: domain.ScopeAccount, Account: "acc-1", MaxQuantity: "1"}},
	}
	for _, tc := range bad {
		t.Run("err/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.limit.Validate(); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

// --- LimitPnlBounds ---

func TestLimitPnlBounds_Validate(t *testing.T) {
	t.Parallel()

	ok := []struct {
		name  string
		limit domain.LimitPnlBounds
	}{
		{"asset lower only", domain.LimitPnlBounds{
			Scope: domain.ScopeAsset, Asset: "AAPL", LowerBound: "-1000"}},
		{"asset upper only", domain.LimitPnlBounds{
			Scope: domain.ScopeAsset, Asset: "AAPL", UpperBound: "5000"}},
		{"account_asset both equal", domain.LimitPnlBounds{
			Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "MSFT",
			LowerBound: "0", UpperBound: "0"}},
		{"account_asset both valid", domain.LimitPnlBounds{
			Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
			LowerBound: "-500", UpperBound: "1000"}},
		{"account_asset initial_pnl", domain.LimitPnlBounds{
			Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
			LowerBound: "-500", InitialPnl: "10"}},
	}
	for _, tc := range ok {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.limit.Validate(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	bad := []struct {
		name  string
		limit domain.LimitPnlBounds
	}{
		{"neither bound", domain.LimitPnlBounds{
			Scope: domain.ScopeAsset, Asset: "AAPL"}},
		{"lower > upper", domain.LimitPnlBounds{
			Scope: domain.ScopeAsset, Asset: "AAPL",
			LowerBound: "100", UpperBound: "50"}},
		{"lower not decimal", domain.LimitPnlBounds{
			Scope: domain.ScopeAsset, Asset: "AAPL", LowerBound: "abc"}},
		{"scope broker not allowed", domain.LimitPnlBounds{
			Scope: domain.ScopeBroker, LowerBound: "-100"}},
		{"scope account not allowed", domain.LimitPnlBounds{
			Scope: domain.ScopeAccount, Account: "acc-1", LowerBound: "-100"}},
		{"initial_pnl on asset scope", domain.LimitPnlBounds{
			Scope: domain.ScopeAsset, Asset: "AAPL",
			LowerBound: "-100", InitialPnl: "10"}},
		{"initial_pnl not decimal", domain.LimitPnlBounds{
			Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
			LowerBound: "-100", InitialPnl: "abc"}},
	}
	for _, tc := range bad {
		t.Run("err/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.limit.Validate(); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
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

// --- ValidateMarketDataMark ---

func TestValidateMarketDataMark(t *testing.T) {
	t.Parallel()

	// The mark maps to the engine Quote mark, a signed Option<Price>, so sign and
	// zero are accepted; only decimal syntax is enforced here. An empty string
	// means "no manual price".
	ok := []string{"", "0", "-1.5", "-0", "42", "0.0001", "100.25"}
	for _, mark := range ok {
		t.Run("ok/"+mark, func(t *testing.T) {
			t.Parallel()
			if err := domain.ValidateMarketDataMark(mark); err != nil {
				t.Fatalf("mark %q: unexpected error: %v", mark, err)
			}
		})
	}

	bad := []string{"abc", "NaN", "Inf", "1.2.3", "--1"}
	for _, mark := range bad {
		t.Run("err/"+mark, func(t *testing.T) {
			t.Parallel()
			if err := domain.ValidateMarketDataMark(mark); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("mark %q: expected ErrInvalid, got %v", mark, err)
			}
		})
	}
}
