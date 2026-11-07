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

package store

import "testing"

// TestCompareDecimalStringsNumericOrder checks the DECIMAL collation comparator
// orders values numerically (including negatives and arbitrary precision), so an
// index on a {{DECIMAL}} column scans and sorts in true numeric order.
func TestCompareDecimalStringsNumericOrder(t *testing.T) {
	// The empty sentinel is first: it sorts below every number.
	ordered := []string{
		"",
		"-1000000000000000000000000000000000.0001",
		"-10",
		"-2.5",
		"-2",
		"-0.1",
		"0",
		"0.000000001",
		"2",
		"2.5",
		"10",
		"263.1578947368421052631578947368421053",
		"1000000000000000000000000000000000.0001",
	}
	for i := 1; i < len(ordered); i++ {
		if got := CompareDecimalStrings(ordered[i-1], ordered[i]); got != -1 {
			t.Fatalf("CompareDecimalStrings(%q, %q) = %d, want -1",
				ordered[i-1], ordered[i], got)
		}
		if got := CompareDecimalStrings(ordered[i], ordered[i-1]); got != 1 {
			t.Fatalf("CompareDecimalStrings(%q, %q) = %d, want 1",
				ordered[i], ordered[i-1], got)
		}
	}
}

// TestCompareDecimalStringsTrailingZeros checks that decimals differing only in
// trailing zeros compare equal, so the collation matches numeric equality.
func TestCompareDecimalStringsTrailingZeros(t *testing.T) {
	cases := [][2]string{
		{"100.50", "100.5"},
		{"100.500", "100.5"},
		{"0", "0.0"},
		{"-3.50", "-3.5"},
	}
	for _, c := range cases {
		if got := CompareDecimalStrings(c[0], c[1]); got != 0 {
			t.Fatalf("CompareDecimalStrings(%q, %q) = %d, want 0", c[0], c[1], got)
		}
	}
}

// TestCompareDecimalStringsNumericForms checks that accepted decimal forms
// compare equal to their ordinary decimal representations.
func TestCompareDecimalStringsNumericForms(t *testing.T) {
	for _, c := range [][2]string{
		{"1e3", "1000"},
		{"+5", "5"},
		{".5", "0.5"},
	} {
		if got := CompareDecimalStrings(c[0], c[1]); got != 0 {
			t.Fatalf("CompareDecimalStrings(%q, %q) = %d, want 0", c[0], c[1], got)
		}
		if got := CompareDecimalStrings(c[1], c[0]); got != 0 {
			t.Fatalf("CompareDecimalStrings(%q, %q) = %d, want 0", c[1], c[0], got)
		}
	}
}

// TestCompareDecimalStringsNonDecimal checks that rejected decimal texts sort
// above every number and compare with one another in byte order.
func TestCompareDecimalStringsNonDecimal(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"!", "abc", "xyz", "字", "NaN", "Inf", " 1", "1 ", "1,5",
	} {
		for _, number := range []string{
			"", "-1000000000000000000000000000000000.0001", "-0.1",
			"0", "0.000000001", "1000000000000000000000000000000000.0001",
		} {
			if got := CompareDecimalStrings(text, number); got != 1 {
				t.Fatalf("CompareDecimalStrings(%q, %q) = %d, want 1",
					text, number, got)
			}
			if got := CompareDecimalStrings(number, text); got != -1 {
				t.Fatalf("CompareDecimalStrings(%q, %q) = %d, want -1",
					number, text, got)
			}
		}
	}
	for _, tc := range []struct {
		left, right string
		want        int
	}{
		{left: "abc", right: "xyz", want: -1},
		{left: "xyz", right: "abc", want: 1},
		{left: "abc", right: "abc", want: 0},
		{left: "xyz", right: "xyz", want: 0},
		{left: "!", right: "!", want: 0},
		{left: "字", right: "字", want: 0},
		{left: "Z", right: "abc", want: -1},
		{left: "字", right: "xyz", want: 1},
	} {
		if got := CompareDecimalStrings(tc.left, tc.right); got != tc.want {
			t.Fatalf("CompareDecimalStrings(%q, %q) = %d, want %d",
				tc.left, tc.right, got, tc.want)
		}
	}
}

// TestCompareDecimalStringsEmptySentinel pins the empty-string ordering: empty
// equals empty and sorts below every number.
func TestCompareDecimalStringsEmptySentinel(t *testing.T) {
	if got := CompareDecimalStrings("", ""); got != 0 {
		t.Fatalf("CompareDecimalStrings(\"\", \"\") = %d, want 0", got)
	}
	for _, number := range []string{"-1000", "-0.0001", "0", "0.0001", "1000"} {
		if got := CompareDecimalStrings("", number); got != -1 {
			t.Fatalf("CompareDecimalStrings(\"\", %q) = %d, want -1", number, got)
		}
		if got := CompareDecimalStrings(number, ""); got != 1 {
			t.Fatalf("CompareDecimalStrings(%q, \"\") = %d, want 1", number, got)
		}
	}
}
