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

package sqlite

import (
	"strings"

	"github.com/shopspring/decimal"
	sqlite "modernc.org/sqlite"
)

// decimalCollationName is the SQLite collation that {{DECIMAL}} columns carry.
const decimalCollationName = "DECIMAL"

// init registers the DECIMAL collation on the driver so every connection opened
// afterwards (the connector opens its single pooled connection later) compares
// {{DECIMAL}} columns numerically. modernc.org/sqlite makes a registered
// collation available to all new connections; registering once at package init
// is therefore sufficient. A duplicate registration of the same name is the only
// expected failure and is harmless (the driver surfaces it as a plain error
// matched by text); any other failure is a programming error surfaced loudly.
func init() {
	if err := sqlite.RegisterCollationUtf8(
		decimalCollationName, compareDecimalStrings,
	); err != nil && !strings.Contains(err.Error(), "already registered") {
		panic(err)
	}
}

// compareDecimalStrings is the DECIMAL collation comparator. It returns -1, 0 or
// +1 for left vs right under a total order that is numeric for decimal values:
//
//   - the empty string is a sentinel (an unset price or average entry price) and
//     sorts below every number, so SQLite gathers unset rows at the low end;
//   - two parseable decimals compare by numeric value, so "100.50" and "100.5"
//     are equal and negatives order correctly;
//   - any other (non-decimal) text is pushed above all numbers and, against
//     another non-decimal, falls back to byte order, so the relation stays a
//     total order as the collation contract requires.
//
// The empty-string ordering here must match decimalStringCompare in the backend
// merge so single-node and multi-node listings agree.
func compareDecimalStrings(left, right string) int {
	leftEmpty, rightEmpty := left == "", right == ""
	switch {
	case leftEmpty && rightEmpty:
		return 0
	case leftEmpty:
		return -1
	case rightEmpty:
		return 1
	}
	leftDec, leftErr := decimal.NewFromString(left)
	rightDec, rightErr := decimal.NewFromString(right)
	switch {
	case leftErr == nil && rightErr == nil:
		return leftDec.Cmp(rightDec)
	case leftErr != nil && rightErr != nil:
		return strings.Compare(left, right)
	case leftErr != nil:
		return 1
	default:
		return -1
	}
}
