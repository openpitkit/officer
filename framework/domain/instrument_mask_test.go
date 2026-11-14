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

package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var instrumentMaskAssetCases = []struct {
	name, mask, code string
	want             bool
}{
	{"exact", "USD", "USD", true},
	{"exact rejects suffix", "USD", "USDT", false},
	{"exact rejects prefix", "USD", "XUSD", false},
	{"exact rejects empty", "USD", "", false},
	{"star empty", "*", "", true},
	{"star any", "*", "BTC", true},
	{"adjacent stars empty", "**", "", true},
	{"adjacent stars any", "***", "USD", true},
	{"trailing star empty", "USD*", "USD", true},
	{"trailing star run", "USD*", "USDT", true},
	{"trailing star rejects prefix", "USD*", "XUSD", false},
	{"leading star empty", "*USD", "USD", true},
	{"leading star run", "*USD", "XUSD", true},
	{"leading star full match", "*USD", "USDT", false},
	{"surrounding stars empty", "*USD*", "USD", true},
	{"surrounding stars run", "*USD*", "XUSDT", true},
	{"surrounding stars absent", "*USD*", "EUR", false},
	{"middle star empty", "B*C", "BC", true},
	{"middle star run", "B*C", "BTC", true},
	{"middle star full match", "B*C", "BTCX", false},
	{"overlap rejected", "AB*BC", "ABC", false},
	{"overlap separated", "AB*BC", "ABBC", true},
	{"many stars empty", "A*B*C*D", "ABCD", true},
	{"many stars run", "A*B*C*D", "AXBYCZD", true},
	{"middle order", "A*B*C*D", "ACBD", false},
	{"middle missing", "A*B*C*D", "ABD", false},
	{"middle cannot reuse prefix", "AB*B*C", "ABC", false},
	{"middle cannot reuse suffix", "A*B*BC", "ABC", false},
	{"middle cannot overlap", "A*BB*BC*D", "ABBCD", false},
	{"leftmost middle", "A*B*C*D", "ABBCBD", true},
	{"adjacent middle stars", "A**B***C", "AXBYC", true},
	{"adjacent middle stars empty", "A**B***C", "ABC", true},
	{"literal dot", "A.B", "A.B", true},
	{"dot not wildcard", "A.B", "AXB", false},
	{"literal question", "A?", "A?", true},
	{"question not wildcard", "A?", "AB", false},
	{"literal bracket", "A[B", "A[B", true},
	{"bracket not class", "A[B]", "AB", false},
	{"literal backslash", `A\B`, `A\B`, true},
	{"backslash not escape", `A\*`, `A\XYZ`, true},
	{"backslash required", `A\*`, "AXYZ", false},
	{"literal plus", "A+B", "A+B", true},
	{"plus not repetition", "A+B", "AAB", false},
	{"literal slash", "A/B", "A/B", true},
	{"slash required", "A/B", "AB", false},
	{"star crosses slash", "A*B", "A/X/B", true},
	{"star in candidate", "X*Y", "X*Y", true},
	{"trailing star in candidate", "X*", "X*Y", true},
	{"star always wildcard", "X*Y", "XABY", true},
	{"case sensitive", "usd", "USD", false},
	{"same lowercase", "usd", "usd", true},
	{"no candidate trimming", "USD", " USD", false},
	{"unvalidated whitespace", "*", " USD\n", true},
	{"replacement not invalid byte", "�", "\xff", false},
	{"star invalid byte", "*", "\xff", true},
	{"prefix invalid byte", "A*", "A\xff", true},
	{"unicode exact", "é", "é", true},
	{"no unicode normalization", "é", "e\u0301", false},
}

func TestInstrumentMaskMatches(t *testing.T) {
	t.Parallel()
	for _, test := range instrumentMaskAssetCases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, side := range []string{"base", "quote"} {
				t.Run(side, func(t *testing.T) {
					baseMask, quoteMask := test.mask, "*"
					base, quote := test.code, "USD"
					if side == "quote" {
						baseMask, quoteMask = quoteMask, baseMask
						base, quote = quote, base
					}
					mask, err := ParseInstrumentMask(baseMask, quoteMask)
					if err != nil {
						t.Fatal(err)
					}
					if got := mask.Matches(base, quote); got != test.want {
						t.Fatalf("Matches(%q, %q) = %t, want %t", base, quote, got, test.want)
					}
				})
			}
		})
	}
}

func TestInstrumentMaskTicketExamples(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		baseMask, quoteMask string
		want                [5]bool
	}{
		{"*", "USD", [5]bool{true, false, false, true, false}},
		{"BTC", "*USD*", [5]bool{true, true, true, false, false}},
	} {
		t.Run(test.baseMask+"/"+test.quoteMask, func(t *testing.T) {
			t.Parallel()
			mask, err := ParseInstrumentMask(test.baseMask, test.quoteMask)
			if err != nil {
				t.Fatal(err)
			}
			for i, pair := range [][2]string{
				{"BTC", "USD"}, {"BTC", "USDT"}, {"BTC", "USDC"},
				{"ETH", "USD"}, {"BTC", "EUR"},
			} {
				if got := mask.Matches(pair[0], pair[1]); got != test.want[i] {
					t.Errorf("Matches(%q, %q) = %t, want %t", pair[0], pair[1], got, test.want[i])
				}
			}
		})
	}
}

func TestInstrumentMaskSeparateSides(t *testing.T) {
	t.Parallel()
	mask, err := ParseInstrumentMask("BTC", "USD")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		base, quote string
		want        bool
	}{
		{"BTC", "USD", true},
		{"USD", "BTC", false},
	} {
		if got := mask.Matches(test.base, test.quote); got != test.want {
			t.Errorf("Matches(%q, %q) = %t, want %t", test.base, test.quote, got, test.want)
		}
	}
}

func TestInstrumentMaskAccessors(t *testing.T) {
	t.Parallel()
	for _, test := range [][2]string{
		{"b**Tc/", "*uSd*"}, {"*", "*"}, {"BTC", "BTC"},
		{strings.Repeat("A", 32), strings.Repeat("é", 16)},
	} {
		mask, err := ParseInstrumentMask(test[0], test[1])
		if err != nil {
			t.Fatal(err)
		}
		if mask.BaseAsset() != test[0] || mask.QuoteAsset() != test[1] {
			t.Errorf("accessors = (%q, %q), want (%q, %q)", mask.BaseAsset(), mask.QuoteAsset(), test[0], test[1])
		}
	}
}

func TestInstrumentMaskZero(t *testing.T) {
	t.Parallel()
	for _, pair := range [][2]string{{"", ""}, {"BTC", "USD"}, {"*", "*"}} {
		if (InstrumentMask{}).Matches(pair[0], pair[1]) {
			t.Errorf("zero mask Matches(%q, %q) = true, want false", pair[0], pair[1])
		}
	}
}

func TestParseInstrumentMaskErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, value, constraint string
	}{
		{"empty", "", "required"},
		{"leading space", " BTC", "format"},
		{"trailing space", "BTC ", "format"},
		{"inner space", "B TC", "format"},
		{"dot", ".", "format"},
		{"double dot", "..", "format"},
		{"invalid UTF-8", "\xff", "format"},
		{"nonprintable", "A\x00", "format"},
		{"33 bytes", strings.Repeat("A", 33), "format"},
		{"multibyte over limit", strings.Repeat("é", 17), "format"},
	} {
		for _, side := range []string{"base", "quote"} {
			t.Run(side+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				base, quote := test.value, "USD"
				if side == "quote" {
					base, quote = "BTC", test.value
				}
				mask, err := ParseInstrumentMask(base, quote)
				pointer := "/" + side + "Asset"
				assertInstrumentMaskError(t, err, pointer, test.constraint)
				if mask != (InstrumentMask{}) {
					t.Errorf("refused mask = %#v, want zero value", mask)
				}
				wantMessage := side + " asset mask: " + ValidateAsset(test.value).Error()
				if err.Error() != wantMessage {
					t.Errorf("error = %q, want %q", err.Error(), wantMessage)
				}
				wrapped := fmt.Errorf("wrap: %w", err)
				assertInstrumentMaskError(t, wrapped, pointer, test.constraint)
				prefixed := WithValidationPointer(
					"/allowedInstruments/0"+ValidationPointer(wrapped), wrapped,
				)
				assertInstrumentMaskError(t, prefixed, "/allowedInstruments/0"+pointer, test.constraint)
			})
		}
	}
	for _, pair := range [][2]string{{"", ""}, {".", ""}, {"", "."}} {
		_, err := ParseInstrumentMask(pair[0], pair[1])
		constraint := "format"
		if pair[0] == "" {
			constraint = "required"
		}
		assertInstrumentMaskError(t, err, "/baseAsset", constraint)
	}
}

func assertInstrumentMaskError(t *testing.T, err error, pointer, constraint string) {
	t.Helper()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("errors.Is(err, ErrInvalid) = false: %v", err)
	}
	if got := ValidationPointer(err); got != pointer {
		t.Errorf("ValidationPointer = %q, want %q", got, pointer)
	}
	if got := ValidationConstraint(err); got != constraint {
		t.Errorf("ValidationConstraint = %q, want %q", got, constraint)
	}
}

func FuzzInstrumentMaskMatches(f *testing.F) {
	for _, test := range instrumentMaskAssetCases {
		f.Add(test.mask, test.code, "*", "USD")
		f.Add("*", "BTC", test.mask, test.code)
	}
	for _, pair := range [][2]string{
		{"BTC", "USD"}, {"BTC", "USDT"}, {"BTC", "USDC"},
		{"ETH", "USD"}, {"BTC", "EUR"},
	} {
		f.Add("*", pair[0], "USD", pair[1])
		f.Add("BTC", pair[0], "*USD*", pair[1])
	}
	f.Add("A****B", "AXB", "USD", "USD")
	f.Add("A*", "A\x00\n/\xff", "*", "\xff")
	f.Add("A*", "A\x00\n/", "*", "USD")
	f.Add("", "BTC", "", "USD")
	f.Add("\xff", "BTC", "*", "USD")
	f.Fuzz(func(t *testing.T, baseMask, base, quoteMask, quote string) {
		mask, err := ParseInstrumentMask(baseMask, quoteMask)
		if err != nil {
			t.Skip()
		}
		got := mask.Matches(base, quote)
		if !utf8.ValidString(base) || !utf8.ValidString(quote) {
			return
		}
		oracle := func(text, code string) bool {
			fragments := strings.Split(text, "*")
			for i := range fragments {
				fragments[i] = regexp.QuoteMeta(fragments[i])
			}
			return regexp.MustCompile("(?s)^" + strings.Join(fragments, ".*") + "$").MatchString(code)
		}
		want := oracle(baseMask, base) && oracle(quoteMask, quote)
		if got != want {
			t.Fatalf("mask (%q, %q) Matches(%q, %q) = %t, oracle %t", baseMask, quoteMask, base, quote, got, want)
		}
	})
}
