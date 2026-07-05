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
		if got := compareDecimalStrings(ordered[i-1], ordered[i]); got != -1 {
			t.Fatalf("compareDecimalStrings(%q, %q) = %d, want -1",
				ordered[i-1], ordered[i], got)
		}
		if got := compareDecimalStrings(ordered[i], ordered[i-1]); got != 1 {
			t.Fatalf("compareDecimalStrings(%q, %q) = %d, want 1",
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
		if got := compareDecimalStrings(c[0], c[1]); got != 0 {
			t.Fatalf("compareDecimalStrings(%q, %q) = %d, want 0", c[0], c[1], got)
		}
	}
}

// TestCompareDecimalStringsEmptySentinel pins the empty-string ordering: empty
// equals empty and sorts below every number.
func TestCompareDecimalStringsEmptySentinel(t *testing.T) {
	if got := compareDecimalStrings("", ""); got != 0 {
		t.Fatalf("compareDecimalStrings(\"\", \"\") = %d, want 0", got)
	}
	for _, number := range []string{"-1000", "-0.0001", "0", "0.0001", "1000"} {
		if got := compareDecimalStrings("", number); got != -1 {
			t.Fatalf("compareDecimalStrings(\"\", %q) = %d, want -1", number, got)
		}
		if got := compareDecimalStrings(number, ""); got != 1 {
			t.Fatalf("compareDecimalStrings(%q, \"\") = %d, want 1", number, got)
		}
	}
}
