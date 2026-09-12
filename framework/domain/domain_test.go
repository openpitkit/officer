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
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.openpit.dev/officer/framework/domain"
)

func TestExecutionReportRequestFromInputOmitsInternalSettlementContext(t *testing.T) {
	t.Parallel()

	request := domain.ExecutionReportRequestFromInput(domain.ExecutionReportInput{
		LockPrice: "100.25",
		Lock:      []byte{0x00, 0x7f, 0xff},
	})
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if bytes.Contains(encoded, []byte(`"lock":`)) {
		t.Fatalf("execution-report audit snapshot exposed opaque lock: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte(`"lockPrice":"100.25"`)) {
		t.Fatalf("execution-report audit snapshot lost display price: %s", encoded)
	}
}

// --- ValidateAccountID ---

func TestValidateAccountID(t *testing.T) {
	t.Parallel()

	// multibyte: 64 Cyrillic code points = 128 bytes - must be accepted.
	cyrillic64 := strings.Repeat("я", 64)
	if utf8.RuneCountInString(cyrillic64) != 64 {
		t.Fatal("test setup: expected 64 code points")
	}
	// 65 Cyrillic code points = 130 bytes - must be rejected.
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
		{"invalid UTF-8", "acc\xff"},
		// A dot segment survives encodeURIComponent and is then resolved away by
		// the URL parser, so the account would be unreachable at its own path.
		{"dot", "."},
		{"dot dot", ".."},
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

func TestValidatePrincipalID(t *testing.T) {
	t.Parallel()

	for _, id := range []string{
		"operator",
		strings.Repeat("x", 64),
		strings.Repeat("я", 64),
	} {
		if err := domain.ValidatePrincipalID(id); err != nil {
			t.Errorf("ValidatePrincipalID(%q): %v", id, err)
		}
	}

	for _, id := range []string{
		"",
		strings.Repeat("x", 65),
		" operator",
		"operator ",
		"operator\x00",
		"operator\xff",
	} {
		err := domain.ValidatePrincipalID(id)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("ValidatePrincipalID(%q) = %v, want ErrInvalid", id, err)
		}
	}
}

func TestValidatePnlHaltReason(t *testing.T) {
	t.Parallel()

	ok := []struct {
		name   string
		reason domain.PnlHaltReason
	}{
		{"empty", ""},
		{"missing fx", domain.PnlHaltReasonMissingFx},
		{"missing account currency", domain.PnlHaltReasonMissingAccountCurrency},
		{"missing initial pnl", domain.PnlHaltReasonMissingInitialPnl},
		{"missing cost basis", domain.PnlHaltReasonMissingCostBasis},
		{"arithmetic overflow", domain.PnlHaltReasonArithmeticOverflow},
	}
	for _, tc := range ok {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := domain.ValidatePnlHaltReason(tc.reason); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	bad := []struct {
		name   string
		reason domain.PnlHaltReason
	}{
		{"typo", "missing_fxx"},
		{"unmappable", "unknown"},
		{"whitespace", " "},
		{"padded known reason", " missing_fx"},
		{"wrong case", "MISSING_FX"},
		{"arbitrary text", "engine broke"},
	}
	for _, tc := range bad {
		t.Run("err/"+tc.name, func(t *testing.T) {
			t.Parallel()
			err := domain.ValidatePnlHaltReason(tc.reason)
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

	// multibyte: 256 CJK code points = 768 bytes - must be accepted.
	cjk256 := strings.Repeat("字", 256)
	if utf8.RuneCountInString(cjk256) != 256 {
		t.Fatal("test setup: expected 256 code points")
	}
	// 257 CJK code points = 771 bytes - must be rejected.
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
		{"invalid UTF-8", "Desk\xffAlpha"},
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

	// multibyte: 4096 Cyrillic code points = 8192 bytes - must be accepted.
	cyrillic4096 := strings.Repeat("я", 4096)
	if utf8.RuneCountInString(cyrillic4096) != 4096 {
		t.Fatal("test setup: expected 4096 code points")
	}
	// 4097 code points - must be rejected.
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
		{"invalid UTF-8", "note\xff"},
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

	// multibyte: 64 Cyrillic code points = 128 bytes - must be accepted.
	cyrillic64 := strings.Repeat("я", 64)
	if utf8.RuneCountInString(cyrillic64) != 64 {
		t.Fatal("test setup: expected 64 code points")
	}
	// 65 Cyrillic code points - must be rejected.
	cyrillic65 := strings.Repeat("я", 65)

	ok := []struct {
		name string
		id   string
	}{
		{"simple", "grp-1"},
		{"max length ascii", strings.Repeat("x", 64)},
		{"multibyte 64 code points", cyrillic64},
		// Only the bare sentinel is reserved; a hyphen anywhere else is ordinary.
		{"leading hyphen", "-grp"},
		{"double hyphen", "--"},
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
		{"invalid UTF-8", "grp\xff"},
		{"dot", "."},
		{"dot dot", ".."},
		// The default-group path sentinel: a real group named "-" would be
		// shadowed by /groups/-/... and could never be renamed or deleted.
		{"reserved sentinel", "-"},
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

// TestValidateAssetClassID_PathDotSegments pins the third dictionary code on
// the same rule: a dot segment is resolved away by the URL parser, so the class
// could never be addressed at /asset-classes/{code}.
func TestValidateAssetClassID_PathDotSegments(t *testing.T) {
	t.Parallel()

	if err := domain.ValidateAssetClassID("equity.fx"); err != nil {
		t.Fatalf("an interior dot must stay legal: %v", err)
	}
	for _, id := range []string{".", ".."} {
		if err := domain.ValidateAssetClassID(id); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("ValidateAssetClassID(%q) = %v, want ErrInvalid", id, err)
		}
	}
}

// TestValidateAsset_Printable covers the asset code specifically: it is the one
// dictionary string that crosses the FFI as a string view, so a control
// character must not pass the boundary. The existing whitespace rule already
// rejects "\n" and "\t", so the gap is the non-space control characters.
func TestValidateAsset_Printable(t *testing.T) {
	t.Parallel()

	if err := domain.ValidateAsset("USD"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bad := []struct {
		name  string
		asset string
	}{
		{"nul", "US\x00D"},
		{"bell", "USD\x07"},
		{"delete", "USD\x7f"},
		{"invalid UTF-8", "US\xffD"},
		// U+202E written as a rune constant: a literal right-to-left override in
		// the source would reorder this file for a human reader.
		{"bidi override", "US" + string(rune(0x202e)) + "D"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := domain.ValidateAsset(tc.asset); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("ValidateAsset(%q) = %v, want ErrInvalid", tc.asset, err)
			}
		})
	}
}

// TestValidateAsset_PathDotSegments pins the asset code on the same rule as the
// sibling dictionaries: a dot segment is resolved away by the URL parser, so the
// asset could never be addressed at /assets/{code}.
func TestValidateAsset_PathDotSegments(t *testing.T) {
	t.Parallel()

	if err := domain.ValidateAsset("BRK.A"); err != nil {
		t.Fatalf("an interior dot must stay legal: %v", err)
	}
	for _, asset := range []string{".", ".."} {
		if err := domain.ValidateAsset(asset); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("ValidateAsset(%q) = %v, want ErrInvalid", asset, err)
		}
	}
}

// TestValidateBlockReason covers the operator-supplied reason: free text is
// allowed, but a newline is not - the reason is rendered verbatim into the
// audit detail line, where it could otherwise forge a second record.
func TestValidateBlockReason(t *testing.T) {
	t.Parallel()

	ok := []struct {
		name   string
		reason string
	}{
		{"empty", ""},
		{"free text", "margin breach: desk 4, 09:15"},
		{"multibyte", "превышен лимит"},
		{"max length", strings.Repeat("x", 4096)},
	}
	for _, tc := range ok {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if err := domain.ValidateBlockReason(tc.reason); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	bad := []struct {
		name   string
		reason string
	}{
		{"over 4096", strings.Repeat("x", 4097)},
		{"newline", "risk\nblock account acc-2: forged"},
		{"carriage return", "risk\rforged"},
		{"nul", "risk\x00"},
		{"invalid UTF-8", "risk\xff"},
	}
	for _, tc := range bad {
		t.Run("err/"+tc.name, func(t *testing.T) {
			t.Parallel()
			err := domain.ValidateBlockReason(tc.reason)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("ValidateBlockReason(%q) = %v, want ErrInvalid", tc.reason, err)
			}
		})
	}
}

// TestNormalizeReason covers the write-side normalizer for machine-produced
// reasons: non-printable runes are dropped and legal text is returned unchanged.
// U+200B is the interesting case - it is format-class, not control-class, so the
// engine-boundary scrub keeps it while ValidateReason rejects it.
func TestNormalizeReason(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		reason string
		want   string
	}{
		{"empty", "", ""},
		{"clean text", "margin breach: desk 4, 09:15", "margin breach: desk 4, 09:15"},
		{"multibyte", "превышен лимит", "превышен лимит"},
		// Written as an escape: a literal U+200B is invisible in this source.
		{"zero width space", "kill\u200bswitch", "killswitch"},
		{"newline", "risk\nblock account acc-2: forged", "riskblock account acc-2: forged"},
		{"nul", "risk\x00", "risk"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := domain.NormalizeReason(tc.reason); got != tc.want {
				t.Fatalf("NormalizeReason(%q) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

// TestNormalizeReason_TruncatesByCodePoints pins the length bound at code
// points, not bytes: a multibyte reason must be cut on a rune boundary, or the
// persisted value would be invalid UTF-8 that no restore could accept.
func TestNormalizeReason_TruncatesByCodePoints(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, unit string }{
		{"ascii", "x"},
		{"multibyte", "ю"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := domain.NormalizeReason(strings.Repeat(tc.unit, 5000))
			if !utf8.ValidString(got) {
				t.Fatal("truncation split a rune")
			}
			if n := utf8.RuneCountInString(got); n != 4096 {
				t.Fatalf("code points = %d, want 4096", n)
			}
			if want := strings.Repeat(tc.unit, 4096); got != want {
				t.Fatal("truncation changed the retained prefix")
			}
		})
	}
}

// TestNormalizeReason_OutputPassesValidator is the property the engine-mirror
// write seam relies on: whatever the engine composes, the persisted form is one
// the restore path accepts, so the rollback archive stays restorable.
func TestNormalizeReason_OutputPassesValidator(t *testing.T) {
	t.Parallel()

	reasons := []string{
		"",
		"margin breach: desk 4",
		"kill\u200bswitch [code=pnl_bound_breached]",
		"risk\nforged",
		strings.Repeat("x", 5000),
		// Multibyte at the length boundary, both plain and interleaved with a
		// non-printable rune so truncation lands mid-way through the source.
		strings.Repeat("ю", 5000),
		strings.Repeat("ю\u200b", 5000),
	}
	for i, reason := range reasons {
		normalized := domain.NormalizeReason(reason)
		if err := domain.ValidateReason(normalized); err != nil {
			t.Fatalf("reason %d: ValidateReason after NormalizeReason: %v", i, err)
		}
		if err := domain.ValidateBlockReason(normalized); err != nil {
			t.Fatalf("reason %d: ValidateBlockReason after NormalizeReason: %v", i, err)
		}
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

func TestValidateEngineAssetID(t *testing.T) {
	t.Parallel()
	ok := []domain.EngineAssetID{
		domain.EngineAssetID(domain.EngineAssetIDMin),
		1,
		1234567890,
		domain.EngineAssetID(domain.EngineAssetIDMax),
	}
	for _, id := range ok {
		if err := domain.ValidateEngineAssetID(id); err != nil {
			t.Errorf("ValidateEngineAssetID(%d): unexpected error %v", id, err)
		}
	}
	bad := []domain.EngineAssetID{
		0,
		domain.EngineAssetID(domain.EngineAssetIDMax) + 1,
		domain.EngineAssetID(1 << 63),
	}
	for _, id := range bad {
		if err := domain.ValidateEngineAssetID(id); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("ValidateEngineAssetID(%d): expected ErrInvalid, got %v", id, err)
		}
	}
}

func TestAssetEngineIDIsNotSerialized(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(domain.Asset{
		Code:          "AAPL",
		Title:         "Apple Inc.",
		AssetClass:    "equity",
		EngineAssetID: domain.EngineAssetID(domain.EngineAssetIDMin),
	})
	if err != nil {
		t.Fatalf("Marshal Asset: %v", err)
	}
	const want = `{"Code":"AAPL","Title":"Apple Inc.","AssetClass":"equity"}`
	if string(payload) != want {
		t.Fatalf("Asset JSON = %s, want %s", payload, want)
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

func TestExecutionReportRequiresEngine(t *testing.T) {
	t.Parallel()

	valid := []struct {
		name           string
		in             domain.ExecutionReportInput
		requiresEngine bool
	}{
		{
			name: "workflow without settlement fields",
			in: domain.ExecutionReportInput{
				OrderStatus: domain.OrderStatusAccepted,
			},
		},
		{
			name: "workflow keeps oddly scaled leaves DB-only",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "1.23000",
				OrderStatus:    domain.OrderStatusCommitted,
			},
		},
		{
			name: "fill",
			in: domain.ExecutionReportInput{
				FillQuantity:   "1",
				FillPrice:      "100",
				LeavesQuantity: "0",
				OrderStatus:    domain.OrderStatusFilled,
			},
			requiresEngine: true,
		},
		{
			name: "workflow commission does not require leaves",
			in: domain.ExecutionReportInput{
				Commission: &domain.Commission{
					Amount:   "-1",
					Currency: "USD",
				},
				OrderStatus: domain.OrderStatusAccepted,
			},
			requiresEngine: true,
		},
		{
			name: "terminal without fill",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "2",
				LockPrice:      "-100",
				OrderStatus:    domain.OrderStatusCancelled,
			},
			requiresEngine: true,
		},
		{
			name: "terminal cancellation without leaves",
			in: domain.ExecutionReportInput{
				OrderStatus: domain.OrderStatusCancelled,
			},
			requiresEngine: true,
		},
		{
			name: "terminal rejected defers leaves to the node",
			in: domain.ExecutionReportInput{
				OrderStatus: domain.OrderStatusRejected,
			},
			requiresEngine: true,
		},
		{
			name: "terminal rolled back defers leaves to the node",
			in: domain.ExecutionReportInput{
				OrderStatus: domain.OrderStatusRolledBack,
			},
			requiresEngine: true,
		},
		{
			name: "workflow status with commission routes through engine",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "2",
				Commission: &domain.Commission{
					Amount:   "-0.12",
					Currency: "USD",
				},
				OrderStatus: domain.OrderStatusAccepted,
			},
			requiresEngine: true,
		},
		{
			name: "submitted status with commission routes through engine",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "2",
				Commission: &domain.Commission{
					Amount:   "-0.12",
					Currency: "USD",
				},
				OrderStatus: domain.OrderStatusSubmitted,
			},
			requiresEngine: true,
		},
		{
			name: "committed status with commission routes through engine",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "2",
				Commission: &domain.Commission{
					Amount:   "-0.12",
					Currency: "USD",
				},
				OrderStatus: domain.OrderStatusCommitted,
			},
			requiresEngine: true,
		},
		{
			name: "terminal without fill with commission",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "2",
				Commission: &domain.Commission{
					Amount:   "-0.12",
					Currency: "USD",
				},
				OrderStatus: domain.OrderStatusCancelled,
			},
			requiresEngine: true,
		},
		{
			name: "workflow status without leaves stays DB-only",
			in: domain.ExecutionReportInput{
				OrderStatus: domain.OrderStatusSubmitted,
			},
		},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := domain.ExecutionReportRequiresEngine(tc.in)
			if err != nil {
				t.Fatalf("ExecutionReportRequiresEngine: %v", err)
			}
			if got != tc.requiresEngine {
				t.Fatalf("requires engine = %t, want %t", got, tc.requiresEngine)
			}
		})
	}

	invalid := []struct {
		name string
		in   domain.ExecutionReportInput
	}{
		{
			name: "invalid status",
			in: domain.ExecutionReportInput{
				OrderStatus: domain.OrderStatus("unknown"),
			},
		},
		{
			name: "one-sided fill",
			in: domain.ExecutionReportInput{
				FillQuantity: "1",
				OrderStatus:  domain.OrderStatusFilled,
			},
		},
		{
			name: "fill status without fill",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "0",
				OrderStatus:    domain.OrderStatusPartiallyFilled,
			},
		},
		{
			name: "commission-only partially filled status",
			in: domain.ExecutionReportInput{
				Commission:  &domain.Commission{Amount: "1", Currency: "USD"},
				OrderStatus: domain.OrderStatusPartiallyFilled,
			},
		},
		{
			name: "commission-only filled status",
			in: domain.ExecutionReportInput{
				Commission:  &domain.Commission{Amount: "1", Currency: "USD"},
				OrderStatus: domain.OrderStatusFilled,
			},
		},
		{
			name: "workflow with fill",
			in: domain.ExecutionReportInput{
				FillQuantity:   "1",
				FillPrice:      "100",
				LeavesQuantity: "0",
				OrderStatus:    domain.OrderStatusSubmitted,
			},
		},
		{
			name: "terminal filled report with fill without leaves",
			in: domain.ExecutionReportInput{
				FillQuantity: "1",
				FillPrice:    "100",
				OrderStatus:  domain.OrderStatusFilled,
			},
		},
		{
			name: "partial fill without leaves",
			in: domain.ExecutionReportInput{
				FillQuantity: "1",
				FillPrice:    "100",
				OrderStatus:  domain.OrderStatusPartiallyFilled,
			},
		},
		{
			name: "cancelled report with fill",
			in: domain.ExecutionReportInput{
				FillQuantity:   "1",
				FillPrice:      "100",
				LeavesQuantity: "0",
				OrderStatus:    domain.OrderStatusCancelled,
			},
		},
		{
			name: "rejected report with fill",
			in: domain.ExecutionReportInput{
				FillQuantity:   "1",
				FillPrice:      "100",
				LeavesQuantity: "0",
				OrderStatus:    domain.OrderStatusRejected,
			},
		},
		{
			name: "rolled back report with fill",
			in: domain.ExecutionReportInput{
				FillQuantity:   "1",
				FillPrice:      "100",
				LeavesQuantity: "0",
				OrderStatus:    domain.OrderStatusRolledBack,
			},
		},
		{
			name: "workflow with lock price",
			in: domain.ExecutionReportInput{
				LockPrice:   "100",
				OrderStatus: domain.OrderStatusAccepted,
			},
		},
		{
			name: "workflow with opaque lock",
			in: domain.ExecutionReportInput{
				Lock:        []byte{1},
				OrderStatus: domain.OrderStatusCommitted,
			},
		},
		{
			name: "commission workflow with lock price",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "1",
				LockPrice:      "100",
				Commission: &domain.Commission{
					Amount:   "-1",
					Currency: "USD",
				},
				OrderStatus: domain.OrderStatusAccepted,
			},
		},
		{
			name: "terminal with one-sided commission",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "1",
				Commission: &domain.Commission{
					Amount: "-1",
				},
				OrderStatus: domain.OrderStatusCancelled,
			},
		},
		{
			name: "workflow with malformed leaves",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "not-a-number",
				OrderStatus:    domain.OrderStatusCommitted,
			},
		},
		{
			name: "workflow with negative leaves",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "-1",
				OrderStatus:    domain.OrderStatusCommitted,
			},
		},
		{
			name: "workflow with exponent leaves",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "1e3",
				OrderStatus:    domain.OrderStatusCommitted,
			},
		},
		{
			name: "workflow with padded leaves",
			in: domain.ExecutionReportInput{
				LeavesQuantity: " 1 ",
				OrderStatus:    domain.OrderStatusCommitted,
			},
		},
		{
			name: "terminal with padded lock price",
			in: domain.ExecutionReportInput{
				LockPrice:   " 100 ",
				OrderStatus: domain.OrderStatusCancelled,
			},
		},
		{
			name: "terminal with exponent lock price",
			in: domain.ExecutionReportInput{
				LockPrice:   "1e3",
				OrderStatus: domain.OrderStatusCancelled,
			},
		},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := domain.ExecutionReportRequiresEngine(tc.in); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("ExecutionReportRequiresEngine error = %v, want ErrInvalid", err)
			}
		})
	}

	_, err := domain.ExecutionReportRequiresEngine(domain.ExecutionReportInput{
		Commission:  &domain.Commission{Amount: "-1"},
		OrderStatus: domain.OrderStatusCancelled,
	})
	if !errors.Is(err, domain.ErrInvalid) ||
		!strings.Contains(err.Error(), "commission amount and currency") {
		t.Fatalf("one-sided commission error = %v, want commission error", err)
	}
}

// TestValidationMetadata pins that an execution-report refusal naming one
// request member carries its JSON pointer and constraint.
func TestValidationMetadata(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		in         domain.ExecutionReportInput
		want       string
		constraint string
	}{
		{
			name:       "missing status",
			in:         domain.ExecutionReportInput{},
			want:       "/status",
			constraint: "required",
		},
		{
			name:       "unsupported status",
			in:         domain.ExecutionReportInput{OrderStatus: "nope"},
			want:       "/status",
			constraint: "format",
		},
		{
			name: "filled without quantity",
			in: domain.ExecutionReportInput{
				OrderStatus: domain.OrderStatusFilled,
			},
			want:       "/quantity",
			constraint: "required_for_status",
		},
		{
			name: "partially filled without quantity",
			in: domain.ExecutionReportInput{
				OrderStatus: domain.OrderStatusPartiallyFilled,
			},
			want:       "/quantity",
			constraint: "required_for_status",
		},
		{
			name: "one-sided commission",
			in: domain.ExecutionReportInput{
				Commission:  &domain.Commission{Amount: "-1"},
				OrderStatus: domain.OrderStatusCancelled,
			},
			want:       "/commission",
			constraint: "paired_fields",
		},
		{
			name: "empty commission pair",
			in: domain.ExecutionReportInput{
				Commission:  &domain.Commission{},
				OrderStatus: domain.OrderStatusCancelled,
			},
			want:       "/commission",
			constraint: "required",
		},
		{
			name: "fill without leaves",
			in: domain.ExecutionReportInput{
				FillQuantity: "1",
				FillPrice:    "100",
				OrderStatus:  domain.OrderStatusFilled,
			},
			want:       "/leavesQuantity",
			constraint: "required",
		},
		{
			name: "malformed leaves",
			in: domain.ExecutionReportInput{
				LeavesQuantity: "not-a-number",
				OrderStatus:    domain.OrderStatusCancelled,
			},
			want:       "/leavesQuantity",
			constraint: "format",
		},
		{
			name: "malformed lock price",
			in: domain.ExecutionReportInput{
				LockPrice:   "1e3",
				OrderStatus: domain.OrderStatusCancelled,
			},
			want:       "/lockPrice",
			constraint: "format",
		},
		{
			name: "report-wide refusal names no member",
			in: domain.ExecutionReportInput{
				FillQuantity: "1",
				OrderStatus:  domain.OrderStatusFilled,
			},
			want:       "",
			constraint: "execution_report",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := domain.ExecutionReportRequiresEngine(tc.in)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
			if got := domain.ValidationPointer(err); got != tc.want {
				t.Fatalf("pointer = %q, want %q (error %v)", got, tc.want, err)
			}
			if got := domain.ValidationConstraint(err); got != tc.constraint {
				t.Fatalf("constraint = %q, want %q (error %v)", got, tc.constraint, err)
			}
		})
	}
}

// TestValidationMetadataIgnoresOtherErrors pins that the accessors are safe on
// ordinary errors and nil.
func TestValidationMetadataIgnoresOtherErrors(t *testing.T) {
	t.Parallel()
	if got := domain.ValidationPointer(domain.ErrInvalid); got != "" {
		t.Fatalf("pointer = %q, want empty for a plain error", got)
	}
	if got := domain.ValidationPointer(nil); got != "" {
		t.Fatalf("pointer = %q, want empty for no error", got)
	}
	if got := domain.ValidationConstraint(domain.ErrInvalid); got != "" {
		t.Fatalf("constraint = %q, want empty for a plain error", got)
	}
	if got := domain.ValidationConstraint(nil); got != "" {
		t.Fatalf("constraint = %q, want empty for no error", got)
	}
}

// TestOrderStatusTerminal pins the single membership test that decides both the
// order's final lifecycle state and the release of its remaining reservation.
func TestOrderStatusTerminal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status domain.OrderStatus
		want   bool
	}{
		{status: domain.OrderStatusCancelled, want: true},
		{status: domain.OrderStatusRejected, want: true},
		{status: domain.OrderStatusRolledBack, want: true},
		{status: domain.OrderStatusFilled, want: true},
		{status: domain.OrderStatusSubmitted},
		{status: domain.OrderStatusAccepted},
		{status: domain.OrderStatusCommitted},
		{status: domain.OrderStatusPartiallyFilled},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()
			if got := domain.OrderStatusTerminal(tc.status); got != tc.want {
				t.Fatalf(
					"OrderStatusTerminal(%q) = %t, want %t",
					tc.status,
					got,
					tc.want,
				)
			}
		})
	}
}

// TestParseOpenQuantity pins the source split: one syntax check serves caller
// input and Officer's own stored state, and only the caller-facing wrapper
// blames the caller with ErrInvalid.
func TestParseOpenQuantity(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"1e5", " 1 ", "-1", "abc", ""} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			if _, err := domain.ParseOpenQuantity(value); err == nil {
				t.Fatalf("ParseOpenQuantity(%q) succeeded", value)
			} else if errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("ParseOpenQuantity(%q) = %v, want no caller fault", value, err)
			}
			if err := domain.ValidateLeavesQuantity(value); !errors.Is(
				err, domain.ErrInvalid,
			) {
				t.Fatalf("ValidateLeavesQuantity(%q) = %v, want ErrInvalid", value, err)
			}
		})
	}
	parsed, err := domain.ParseOpenQuantity("1.50")
	if err != nil || parsed.String() != "1.5" {
		t.Fatalf("ParseOpenQuantity(1.50) = %v, %v", parsed, err)
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
		{"broker both", domain.LimitOrderSize{
			Scope: domain.ScopeBroker, MaxQuantity: "1", MaxNotional: "999"}},
		{"underlying asset", domain.LimitOrderSize{
			Scope: domain.ScopeUnderlyingAsset, Asset: "MSFT", MaxQuantity: "0.5"}},
		{"settlement asset", domain.LimitOrderSize{
			Scope: domain.ScopeSettlementAsset, Asset: "USD", MaxNotional: "500"}},
		{"account underlying asset", domain.LimitOrderSize{
			Scope: domain.ScopeAccountUnderlyingAsset, Account: "acc-1", Asset: "AAPL",
			MaxQuantity: "1"}},
		{"account settlement asset", domain.LimitOrderSize{
			Scope: domain.ScopeAccountSettlementAsset, Account: "acc-1", Asset: "USD",
			MaxNotional: "999"}},
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
		{"generic asset scope not allowed", domain.LimitOrderSize{
			Scope: domain.ScopeAsset, Asset: "AAPL", MaxQuantity: "1"}},
		{"underlying missing quantity", domain.LimitOrderSize{
			Scope: domain.ScopeUnderlyingAsset, Asset: "AAPL", MaxNotional: "10"}},
		{"underlying carries notional", domain.LimitOrderSize{
			Scope: domain.ScopeUnderlyingAsset, Asset: "AAPL",
			MaxQuantity: "1", MaxNotional: "10"}},
		{"settlement missing notional", domain.LimitOrderSize{
			Scope: domain.ScopeSettlementAsset, Asset: "USD", MaxQuantity: "1"}},
		{"settlement carries quantity", domain.LimitOrderSize{
			Scope: domain.ScopeSettlementAsset, Asset: "USD",
			MaxQuantity: "1", MaxNotional: "10"}},
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

func TestLimitOrderSize_EitherOrValidationUsesRequestRoot(t *testing.T) {
	t.Parallel()

	err := (domain.LimitOrderSize{Scope: domain.ScopeBroker}).Validate()
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("neither ceiling: error = %v, want ErrInvalid", err)
	}
	if got := domain.ValidationPointer(err); got != "" {
		t.Fatalf("neither ceiling: pointer = %q, want request root", got)
	}
	if got := domain.ValidationConstraint(err); got != "" {
		t.Fatalf("neither ceiling: constraint = %q, want empty", got)
	}

}

func TestLimitSpotFundsPnlBounds_Validate(t *testing.T) {
	t.Parallel()

	ok := []struct {
		name  string
		limit domain.LimitSpotFundsPnlBounds
	}{
		{"global", domain.LimitSpotFundsPnlBounds{
			Scope:      domain.ScopeGlobal,
			Currency:   "USD",
			LowerBound: "-1000",
		}},
		{"account_group", domain.LimitSpotFundsPnlBounds{
			Scope:        domain.ScopeAccountGroup,
			AccountGroup: "desk-a",
			Currency:     "USD",
			UpperBound:   "500",
		}},
		{"account", domain.LimitSpotFundsPnlBounds{
			Scope:      domain.ScopeAccount,
			Account:    "acc-1",
			Currency:   "USD",
			LowerBound: "-100",
			UpperBound: "100",
		}},
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
		limit domain.LimitSpotFundsPnlBounds
	}{
		{"missing group", domain.LimitSpotFundsPnlBounds{
			Scope:      domain.ScopeAccountGroup,
			Currency:   "USD",
			LowerBound: "-1",
		}},
		{"account group on account rejected", domain.LimitSpotFundsPnlBounds{
			Scope:        domain.ScopeAccount,
			Account:      "acc-1",
			AccountGroup: "desk-a",
			Currency:     "USD",
			LowerBound:   "-1",
		}},
		{"neither bound", domain.LimitSpotFundsPnlBounds{
			Scope:    domain.ScopeGlobal,
			Currency: "USD",
		}},
		{"lower greater than upper", domain.LimitSpotFundsPnlBounds{
			Scope:      domain.ScopeGlobal,
			Currency:   "USD",
			LowerBound: "10",
			UpperBound: "1",
		}},
		{"global missing currency", domain.LimitSpotFundsPnlBounds{
			Scope:      domain.ScopeGlobal,
			LowerBound: "-1",
		}},
		{"account group missing currency", domain.LimitSpotFundsPnlBounds{
			Scope:        domain.ScopeAccountGroup,
			AccountGroup: "desk-a",
			LowerBound:   "-1",
		}},
		{"account missing currency", domain.LimitSpotFundsPnlBounds{
			Scope:      domain.ScopeAccount,
			Account:    "acc-1",
			LowerBound: "-1",
		}},
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

func TestLimitSpotFundsPnlBounds_EitherOrValidationUsesRequestRoot(t *testing.T) {
	t.Parallel()

	err := (domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeGlobal, Currency: "USD",
	}).Validate()
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("neither bound: error = %v, want ErrInvalid", err)
	}
	if got := domain.ValidationPointer(err); got != "" {
		t.Fatalf("neither bound: pointer = %q, want request root", got)
	}
	if got := domain.ValidationConstraint(err); got != "" {
		t.Fatalf("neither bound: constraint = %q, want empty", got)
	}

	err = (domain.LimitSpotFundsPnlBounds{
		Scope:      domain.ScopeGlobal,
		Currency:   "USD",
		LowerBound: "10",
		UpperBound: "1",
	}).Validate()
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("inverted bounds: error = %v, want ErrInvalid", err)
	}
	if got := domain.ValidationPointer(err); got != "" {
		t.Fatalf("inverted bounds: pointer = %q, want request root", got)
	}
	if got := domain.ValidationConstraint(err); got != "" {
		t.Fatalf("inverted bounds: constraint = %q, want empty", got)
	}
}

// --- Audit categories ---

func TestAuditAction_Category(t *testing.T) {
	t.Parallel()
	trading := map[domain.AuditAction]bool{
		domain.AuditActionSubmitOrder:     true,
		domain.AuditActionSubmitDropCopy:  true,
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
	if len(trading) != 3 {
		t.Fatalf("trading category = %d actions, want 3: %+v", len(trading), trading)
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

func TestEveryDomainSentinelIsRegistered(t *testing.T) {
	t.Parallel()

	sentinels := map[string]error{
		"ErrAccountMissing":          domain.ErrAccountMissing,
		"ErrAlreadyExists":           domain.ErrAlreadyExists,
		"ErrConflict":                domain.ErrConflict,
		"ErrCurrencyValuedLimit":     domain.ErrCurrencyValuedLimit,
		"ErrEngineRestarting":        domain.ErrEngineRestarting,
		"ErrExecutionReportRequired": domain.ErrExecutionReportRequired,
		"ErrForbidden":               domain.ErrForbidden,
		"ErrHasDependents":           domain.ErrHasDependents,
		"ErrInvalid":                 domain.ErrInvalid,
		"ErrNoChange":                domain.ErrNoChange,
		"ErrNotFound":                domain.ErrNotFound,
		"ErrNotImplemented":          domain.ErrNotImplemented,
		"ErrReservedGroup":           domain.ErrReservedGroup,
		"ErrTerminalOrder":           domain.ErrTerminalOrder,
		"ErrTooLarge":                domain.ErrTooLarge,
		"ErrUpstream":                domain.ErrUpstream,
	}
	declared := exportedDomainErrorVariableNames(t)
	registered := make([]string, 0, len(sentinels))
	for name := range sentinels {
		registered = append(registered, name)
	}
	sort.Strings(registered)
	if strings.Join(declared, "\x00") != strings.Join(registered, "\x00") {
		t.Fatalf(
			"exported domain Err variables = %v, runtime sentinel table = %v",
			declared,
			registered,
		)
	}
	for _, name := range registered {
		if !domain.IsSentinel(sentinels[name]) {
			t.Errorf("domain.%s is not registered as a sentinel", name)
		}
	}
}

func exportedDomainErrorVariableNames(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read domain package directory: %v", err)
	}
	fileSet := token.NewFileSet()
	var sourceFiles []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() ||
			!strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(
			fileSet,
			name,
			nil,
			parser.SkipObjectResolution,
		)
		if err != nil {
			t.Fatalf("parse domain source %q: %v", name, err)
		}
		if file.Name.Name == "domain" {
			sourceFiles = append(sourceFiles, file)
		}
	}
	if len(sourceFiles) == 0 {
		t.Fatal("parse domain package: package domain is missing")
	}

	var names []string
	for _, file := range sourceFiles {
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				continue
			}
			for _, specification := range general.Specs {
				values, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range values.Names {
					if ast.IsExported(name.Name) &&
						strings.HasPrefix(name.Name, "Err") {
						names = append(names, name.Name)
					}
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// TestWithValidationPointerRelabelsWrappedError keeps validation details while
// replacing the pointer through an error wrapper.
func TestWithValidationPointerRelabelsWrappedError(t *testing.T) {
	t.Parallel()

	original := domain.ValidateAccountID("")
	wrapped := fmt.Errorf("validate create request: %w", original)
	got := domain.WithValidationPointer("/code", wrapped)
	if pointer := domain.ValidationPointer(got); pointer != "/code" {
		t.Errorf("pointer = %q, want %q", pointer, "/code")
	}
	if constraint := domain.ValidationConstraint(got); constraint != "required" {
		t.Errorf("constraint = %q, want %q", constraint, "required")
	}
	if !errors.Is(got, domain.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", got)
	}
	if got.Error() != wrapped.Error() {
		t.Errorf("message = %q, want %q", got, wrapped)
	}

	nonValidation := errors.New("not a validation error")
	if got := domain.WithValidationPointer("/code", nonValidation); got != nonValidation {
		t.Errorf("non-validation error = %v, want unchanged %v", got, nonValidation)
	}
}
