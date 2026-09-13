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

	fwstore "go.openpit.dev/officer/framework/store"
)

// appendDecimalRangeFilter binds the user's plain decimal bounds. The column
// carries the DECIMAL collation, so the comparison operators evaluate
// numerically and exactly without an encoded sort key.
func appendDecimalRangeFilter(
	clauses *[]string,
	args *[]any,
	column string,
	filter fwstore.DecimalRangeFilter,
) {
	if filter.Empty() {
		return
	}
	if filter.Equal != nil {
		*clauses = append(*clauses, column+" = ?")
		*args = append(*args, *filter.Equal)
	}
	if filter.NotEqual != nil {
		*clauses = append(*clauses, column+" <> ?")
		*args = append(*args, *filter.NotEqual)
	}
	if filter.Min != nil {
		op := " >= ?"
		if filter.MinExclusive {
			op = " > ?"
		}
		*clauses = append(*clauses, column+op)
		*args = append(*args, *filter.Min)
	}
	if filter.Max != nil {
		op := " <= ?"
		if filter.MaxExclusive {
			op = " < ?"
		}
		*clauses = append(*clauses, column+op)
		*args = append(*args, *filter.Max)
	}
}

// appendDenominatedDecimalRangeFilter binds bounds that are only meaningful in
// one denomination. currencyExpr resolves a row's denomination; admitting the
// numeric predicate only alongside currencyExpr = the filter's currency keeps
// the comparison within a single denomination by construction, so a row
// denominated in another asset is excluded rather than compared against a
// threshold that does not apply to it.
func appendDenominatedDecimalRangeFilter(
	clauses *[]string,
	args *[]any,
	column string,
	currencyExpr string,
	filter fwstore.DenominatedDecimalRangeFilter,
) {
	if filter.Empty() {
		return
	}
	*clauses = append(*clauses, currencyExpr+" = ?")
	*args = append(*args, filter.Currency)
	appendDecimalRangeFilter(clauses, args, column, filter.Range)
}

// appendPnlRangeFilter compares only meaningful amounts in the requested
// currency. Explicit category flags admit non-numeric rows without assigning
// them an amount or a denomination.
func appendPnlRangeFilter(
	clauses *[]string, args *[]any,
	column, haltColumn, currencyExpr string,
	filter fwstore.DenominatedDecimalRangeFilter,
	includeUnset, includeHalted bool,
) {
	if filter.Empty() && !includeUnset && !includeHalted {
		return
	}
	numeric := []string{
		column + " IS NOT NULL", column + " <> ''", currencyExpr + " = ?",
	}
	*args = append(*args, filter.Currency)
	appendDecimalRangeFilter(&numeric, args, column, filter.Range)
	categories := []string{"(" + strings.Join(numeric, " AND ") + ")"}
	if includeUnset {
		categories = append(categories,
			"(("+column+" IS NULL OR "+column+" = '') AND "+haltColumn+" = '')")
	}
	if includeHalted {
		categories = append(categories, "("+haltColumn+" <> '')")
	}
	*clauses = append(*clauses, "("+strings.Join(categories, " OR ")+")")
}

func appendTimeRangeFilter(
	clauses *[]string,
	args *[]any,
	column string,
	filter fwstore.TimeRangeFilter,
) {
	if filter.Empty() {
		return
	}
	if filter.Min != nil {
		op := " >= ?"
		if filter.MinExclusive {
			op = " > ?"
		}
		*clauses = append(*clauses, column+op)
		*args = append(*args, timeStr(*filter.Min))
	}
	if filter.Max != nil {
		op := " <= ?"
		if filter.MaxExclusive {
			op = " < ?"
		}
		*clauses = append(*clauses, column+op)
		*args = append(*args, timeStr(*filter.Max))
	}
}
