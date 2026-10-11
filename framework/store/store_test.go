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

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

func ref[T any](value T) *T { return &value }

var (
	day1 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	day2 = day1.Add(24 * time.Hour)
)

// fragments returns a matcher of n one-rune fragments.
func fragments(n int) TextMatcher {
	return TextMatcher{Fragments: strings.Split(strings.Repeat("a", n), "")}
}

// runes returns a one-fragment matcher of n runes.
func runes(n int) TextMatcher {
	return TextMatcher{Fragments: []string{strings.Repeat("x", n)}}
}

// overRunes is one code point past TextMatcherMaxRunes.
const overRunes = TextMatcherMaxRunes + 1

// overRunesErr is the error Validate reads for field holding overRunes code
// points.
func overRunesErr(field string) string {
	return fmt.Sprintf(
		"%s: %d code points, at most %d: invalid", field, overRunes, TextMatcherMaxRunes,
	)
}

// requireValidation fails t unless err is nil for an empty want, or wraps
// domain.ErrInvalid and reads want otherwise.
func requireValidation(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
		return
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Validate() = %v, want an error wrapping domain.ErrInvalid", err)
	}
	if got := err.Error(); got != want {
		t.Fatalf("Validate() = %q, want %q", got, want)
	}
}

// TestTextMatcherMaxRunesCoversStoredText pins the cap against the domain,
// not against itself: a filter as long as the longest notes or block reason the
// domain stores must pass, so an exact lookup of a stored value is never refused.
func TestTextMatcherMaxRunesCoversStoredText(t *testing.T) {
	t.Parallel()
	for name, stored := range map[string]int{
		"notes":        domain.MaxNotesRunes,
		"block reason": domain.MaxReasonRunes,
	} {
		if TextMatcherMaxRunes < stored {
			t.Fatalf("TextMatcherMaxRunes = %d refuses a filter on a stored %s of %d code points",
				TextMatcherMaxRunes, name, stored)
		}
		matcher := TextMatcher{Fragments: []string{strings.Repeat("x", stored)}}
		if err := matcher.Validate("f"); err != nil {
			t.Fatalf("a filter of a stored %s's %d code points: Validate = %v, want nil", name, stored, err)
		}
	}
}

func TestTextMatcherValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		matcher TextMatcher
		want    string
	}{
		{name: "zero", matcher: TextMatcher{}},
		{name: "exact", matcher: ExactTextMatcher("acc-1")},
		{name: "most_fragments", matcher: fragments(TextMatcherMaxFragments)},
		{name: "most_runes", matcher: runes(TextMatcherMaxRunes)},
		{name: "most_runes_multibyte", matcher: TextMatcher{
			Fragments: []string{strings.Repeat("字", TextMatcherMaxRunes)},
		}},
		{name: "most_runes_across_fragments", matcher: TextMatcher{
			Fragments: []string{
				strings.Repeat("x", TextMatcherMaxRunes/2),
				strings.Repeat("字", TextMatcherMaxRunes-TextMatcherMaxRunes/2),
			},
			AnchorEnd: true,
		}},
		{
			name:    "too_many_fragments",
			matcher: fragments(17),
			want:    "code: 17 fragments, at most 16: invalid",
		},
		{
			name:    "too_many_empty_fragments",
			matcher: TextMatcher{Fragments: make([]string, 17), AnchorStart: true},
			want:    "code: 17 fragments, at most 16: invalid",
		},
		{
			name:    "too_many_runes",
			matcher: runes(overRunes),
			want:    overRunesErr("code"),
		},
		{
			name: "too_many_runes_across_fragments",
			matcher: TextMatcher{
				Fragments: []string{
					strings.Repeat("x", TextMatcherMaxRunes/2),
					strings.Repeat("字", overRunes-TextMatcherMaxRunes/2),
				},
			},
			want: overRunesErr("code"),
		},
		{
			name:    "fragments_reported_before_runes",
			matcher: TextMatcher{Fragments: append(fragments(16).Fragments, strings.Repeat("x", overRunes))},
			want:    "code: 17 fragments, at most 16: invalid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireValidation(t, tc.matcher.Validate("code"), tc.want)
		})
	}
}

func TestVenueFilterValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		filter VenueFilter
		want   string
	}{
		{name: "zero", filter: VenueFilter{}},
		{name: "any", filter: VenueFilter{Mode: VenueFilterAny}},
		{name: "none", filter: VenueFilter{Mode: VenueFilterNone}},
		{name: "connection", filter: VenueFilter{Mode: VenueFilterConnection, ConnectionID: "conn-1"}},
		{
			name:   "connection_without_id",
			filter: VenueFilter{Mode: VenueFilterConnection},
			want:   "store: venue connection mode requires a connection id: invalid",
		},
		{
			name:   "id_without_mode",
			filter: VenueFilter{ConnectionID: "conn-1"},
			want:   "store: venue connection id requires connection mode: invalid",
		},
		{
			name:   "any_with_id",
			filter: VenueFilter{Mode: VenueFilterAny, ConnectionID: "conn-1"},
			want:   "store: venue connection id requires connection mode: invalid",
		},
		{
			name:   "none_with_id",
			filter: VenueFilter{Mode: VenueFilterNone, ConnectionID: "conn-1"},
			want:   "store: venue connection id requires connection mode: invalid",
		},
		{
			name:   "unknown_mode",
			filter: VenueFilter{Mode: "bogus"},
			want:   `store: unknown venue filter mode "bogus": invalid`,
		},
		{
			name:   "unknown_mode_with_id_reports_id_first",
			filter: VenueFilter{Mode: "bogus", ConnectionID: "conn-1"},
			want:   "store: venue connection id requires connection mode: invalid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireValidation(t, tc.filter.Validate(), tc.want)
		})
	}
}

func TestCountRangeFilterValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		filter CountRangeFilter
		want   string
	}{
		{name: "zero", filter: CountRangeFilter{}},
		{name: "full", filter: CountRangeFilter{
			Min: ref(1), Max: ref(3), Equal: ref(2), NotEqual: ref(0),
			MinExclusive: true, MaxExclusive: true,
		}},
		{name: "zero_bounds", filter: CountRangeFilter{
			Min: ref(0), Max: ref(0), Equal: ref(0), NotEqual: ref(0),
		}},
		{name: "min_equals_max", filter: CountRangeFilter{Min: ref(2), Max: ref(2)}},
		{
			name:   "negative_min",
			filter: CountRangeFilter{Min: ref(-1)},
			want:   "position.min: -1 is negative: invalid",
		},
		{
			name:   "negative_max",
			filter: CountRangeFilter{Max: ref(-1)},
			want:   "position.max: -1 is negative: invalid",
		},
		{
			name:   "negative_equal",
			filter: CountRangeFilter{Equal: ref(-1)},
			want:   "position.equal: -1 is negative: invalid",
		},
		{
			name:   "negative_not_equal",
			filter: CountRangeFilter{NotEqual: ref(-1)},
			want:   "position.notEqual: -1 is negative: invalid",
		},
		{
			name:   "min_above_max",
			filter: CountRangeFilter{Min: ref(3), Max: ref(1)},
			want:   "position: min 3 is greater than max 1: invalid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireValidation(t, tc.filter.Validate("position"), tc.want)
		})
	}
}

func TestDecimalRangeFilterValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		filter DecimalRangeFilter
		want   string
	}{
		{name: "zero", filter: DecimalRangeFilter{}},
		{name: "full", filter: DecimalRangeFilter{
			Min: ref("1.5"), Max: ref("2"), Equal: ref("1.75"), NotEqual: ref("-3"),
			MinExclusive: true, MaxExclusive: true,
		}},
		{name: "min_equals_max_at_other_scale", filter: DecimalRangeFilter{
			Min: ref("1.10"), Max: ref("1.1"),
		}},
		{name: "compared_as_numbers", filter: DecimalRangeFilter{Min: ref("9"), Max: ref("10")}},
		{
			name:   "min_not_decimal",
			filter: DecimalRangeFilter{Min: ref("abc")},
			want:   `amount.min: "abc" is not a valid decimal: invalid`,
		},
		{
			name:   "max_not_decimal",
			filter: DecimalRangeFilter{Max: ref("1.2.3")},
			want:   `amount.max: "1.2.3" is not a valid decimal: invalid`,
		},
		{
			name:   "equal_not_decimal",
			filter: DecimalRangeFilter{Equal: ref("")},
			want:   `amount.equal: "" is not a valid decimal: invalid`,
		},
		{
			name:   "not_equal_not_decimal",
			filter: DecimalRangeFilter{NotEqual: ref("1,5")},
			want:   `amount.notEqual: "1,5" is not a valid decimal: invalid`,
		},
		{
			name:   "min_above_max",
			filter: DecimalRangeFilter{Min: ref("2"), Max: ref("1")},
			want:   "amount: min 2 is greater than max 1: invalid",
		},
		{
			name:   "min_above_max_as_numbers",
			filter: DecimalRangeFilter{Min: ref("10"), Max: ref("9")},
			want:   "amount: min 10 is greater than max 9: invalid",
		},
		{name: "engine_limits", filter: DecimalRangeFilter{
			Min: ref("-79228162514264337593543950335"), Max: ref("79228162514264337593543950335"),
			Equal: ref("0.0000000000000000000000000001"), NotEqual: ref("1e28"),
		}},
		{
			name:   "min_outside_engine_range",
			filter: DecimalRangeFilter{Min: ref("-79228162514264337593543950336")},
			want:   `amount.min: "-79228162514264337593543950336" is outside the engine decimal range: invalid`,
		},
		{
			name:   "max_outside_engine_range",
			filter: DecimalRangeFilter{Max: ref("79228162514264337593543950336")},
			want:   `amount.max: "79228162514264337593543950336" is outside the engine decimal range: invalid`,
		},
		{
			name:   "equal_beyond_engine_scale",
			filter: DecimalRangeFilter{Equal: ref("0.00000000000000000000000000001")},
			want:   `amount.equal: "0.00000000000000000000000000001" is outside the engine decimal range: invalid`,
		},
		{
			name:   "not_equal_beyond_engine_scale",
			filter: DecimalRangeFilter{NotEqual: ref("1e29")},
			want:   `amount.notEqual: "1e29" is outside the engine decimal range: invalid`,
		},
		// A zero is held as written to exponents from -(28 + 64) to 28.
		{name: "zeros_at_written_exponent_window", filter: DecimalRangeFilter{
			Min: ref("0e-92"), Max: ref("0e28"), Equal: ref("0." + strings.Repeat("0", 29)),
		}},
		{
			name:   "zero_below_written_exponent_window",
			filter: DecimalRangeFilter{Equal: ref("0e-93")},
			want:   `amount.equal: "0e-93" has written exponent -93, outside [-92, 28]: invalid`,
		},
		{
			name:   "zero_above_written_exponent_window",
			filter: DecimalRangeFilter{NotEqual: ref("0e29")},
			want:   `amount.notEqual: "0e29" has written exponent 29, outside [-92, 28]: invalid`,
		},
		{
			name:   "range_reported_before_order",
			filter: DecimalRangeFilter{Min: ref("2"), Max: ref("1e29")},
			want:   `amount.max: "1e29" is outside the engine decimal range: invalid`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireValidation(t, tc.filter.Validate("amount"), tc.want)
		})
	}
}

// TestDecimalRangeFilterValidateIsBoundedByLength proves a bound's exponent
// never sizes the work: each filter must be decided well inside the time
// budget, a zero at an extreme exponent included, which the range check
// refuses before the min-max comparison could rescale by it.
func TestDecimalRangeFilterValidateIsBoundedByLength(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		filter DecimalRangeFilter
		want   string
	}{
		{
			name:   "largest_exponent",
			filter: DecimalRangeFilter{Min: ref("1e2147483647")},
			want:   `amount.min: "1e2147483647" is outside the engine decimal range: invalid`,
		},
		{
			name:   "smallest_exponent",
			filter: DecimalRangeFilter{NotEqual: ref("1e-2147483648")},
			want:   `amount.notEqual: "1e-2147483648" is outside the engine decimal range: invalid`,
		},
		{
			name:   "zero_at_smallest_exponent",
			filter: DecimalRangeFilter{Min: ref("0e-2147483648")},
			want:   `amount.min: "0e-2147483648" has written exponent -2147483648, outside [-92, 28]: invalid`,
		},
		{
			name:   "max_zero_at_largest_exponent",
			filter: DecimalRangeFilter{Min: ref("1"), Max: ref("0e2147483647")},
			want:   `amount.max: "0e2147483647" has written exponent 2147483647, outside [-92, 28]: invalid`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			done := make(chan error, 1)
			go func() { done <- tc.filter.Validate("amount") }()
			select {
			case err := <-done:
				requireValidation(t, err, tc.want)
			case <-time.After(time.Second):
				t.Fatal("Validate() did not return within a second")
			}
		})
	}
}

func TestDenominatedDecimalRangeFilterValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		filter DenominatedDecimalRangeFilter
		want   string
	}{
		{name: "zero", filter: DenominatedDecimalRangeFilter{}},
		{name: "currency_only", filter: DenominatedDecimalRangeFilter{Currency: "USD"}},
		{name: "full", filter: DenominatedDecimalRangeFilter{
			Range:    DecimalRangeFilter{Min: ref("-5"), Max: ref("5")},
			Currency: "USD",
		}},
		{
			name:   "missing_currency",
			filter: DenominatedDecimalRangeFilter{Range: DecimalRangeFilter{Min: ref("1")}},
			want:   "pnl: currency is required to compare this value: invalid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireValidation(t, tc.filter.Validate("pnl"), tc.want)
		})
	}
}

func TestDenominatedDecimalRangeFilterValidateCurrencyIsSentinel(t *testing.T) {
	t.Parallel()
	err := DenominatedDecimalRangeFilter{Range: DecimalRangeFilter{Max: ref("1")}}.Validate("pnl")
	if !errors.Is(err, ErrCurrencyRequired) {
		t.Fatalf("Validate() = %v, want ErrCurrencyRequired", err)
	}
}

func TestTimeRangeFilterValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		filter TimeRangeFilter
		want   string
	}{
		{name: "zero", filter: TimeRangeFilter{}},
		{name: "full", filter: TimeRangeFilter{
			Min: ref(day1), Max: ref(day2), MinExclusive: true, MaxExclusive: true,
		}},
		{name: "min_only", filter: TimeRangeFilter{Min: ref(day2)}},
		{name: "min_equals_max", filter: TimeRangeFilter{Min: ref(day1), Max: ref(day1)}},
		{
			name:   "min_after_max",
			filter: TimeRangeFilter{Min: ref(day2), Max: ref(day1)},
			want:   "at: min 2026-01-02T00:00:00Z is after max 2026-01-01T00:00:00Z: invalid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireValidation(t, tc.filter.Validate("at"), tc.want)
		})
	}
}

func TestSortSpecValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		sort SortSpec
		want string
	}{
		{name: "zero", sort: SortSpec{}},
		{name: "descending_natural_order", sort: SortSpec{Descending: true}},
		{name: "known_column", sort: SortSpec{Column: "code", Descending: true}},
		{
			name: "unknown_column",
			sort: SortSpec{Column: "pnl"},
			want: `sort: unknown column "pnl": invalid`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireValidation(t, tc.sort.Validate(AccountSortKeys()), tc.want)
		})
	}
}

func TestPageSpecValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		page PageSpec
		want string
	}{
		{name: "zero", page: PageSpec{}},
		{name: "full", page: PageSpec{Limit: 50, Offset: 100}},
		{name: "negative_limit", page: PageSpec{Limit: -1}, want: "page.limit: -1 is negative: invalid"},
		{name: "negative_offset", page: PageSpec{Offset: -1}, want: "page.offset: -1 is negative: invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireValidation(t, tc.page.Validate(), tc.want)
		})
	}
}

func TestPageAt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		limit, page int
		wantPage    PageSpec
		want        string
	}{
		{name: "first_page", limit: 50, page: 1, wantPage: PageSpec{Limit: 50, Offset: 0}},
		{name: "third_page", limit: 10, page: 3, wantPage: PageSpec{Limit: 10, Offset: 20}},
		{name: "last_page_before_overflow", limit: 1, page: math.MaxInt, wantPage: PageSpec{Limit: 1, Offset: math.MaxInt - 1}},
		{name: "offset_overflow", limit: 10, page: math.MaxInt, want: "page: too large: invalid"},
		{name: "zero_limit", limit: 0, page: 1, want: "limit: 0 is not positive: invalid"},
		{name: "negative_limit", limit: -1, page: 1, want: "limit: -1 is not positive: invalid"},
		{name: "zero_page", limit: 10, page: 0, want: "page: 0 is not positive: invalid"},
		{name: "negative_page", limit: 10, page: -1, want: "page: -1 is not positive: invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := PageAt(tc.limit, tc.page)
			requireValidation(t, err, tc.want)
			if got != tc.wantPage {
				t.Fatalf("PageAt(%d, %d) = %+v, want %+v", tc.limit, tc.page, got, tc.wantPage)
			}
		})
	}
}

func TestAccountListFilterValidate(t *testing.T) {
	t.Parallel()
	const currencyRequired = "pnl: currency is required to compare this value: invalid"
	for _, tc := range []struct {
		name   string
		filter AccountListFilter
		want   string
	}{
		{name: "zero", filter: AccountListFilter{}},
		{name: "full", filter: AccountListFilter{
			GroupCode:   ref("desk"),
			Code:        ExactTextMatcher("acc-1"),
			BlockReason: ExactTextMatcher("halt"),
			Status:      StatusFilterBlocked,
			Position:    CountRangeFilter{Min: ref(1), Max: ref(3)},
			Pnl: DenominatedDecimalRangeFilter{
				Range:    DecimalRangeFilter{Min: ref("-10"), Max: ref("10")},
				Currency: "USD",
			},
			IncludeUnsetPnl:  true,
			IncludeHaltedPnl: true,
			Venue:            VenueFilter{Mode: VenueFilterConnection, ConnectionID: "conn-1"},
			Sort:             SortSpec{Column: "positionCount", Descending: true},
			Page:             PageSpec{Limit: 50, Offset: 100},
		}},
		{
			name: "pnl_range_without_currency",
			filter: AccountListFilter{
				Pnl: DenominatedDecimalRangeFilter{Range: DecimalRangeFilter{Min: ref("1")}},
			},
			want: currencyRequired,
		},
		{
			name:   "unset_pnl_without_currency",
			filter: AccountListFilter{IncludeUnsetPnl: true},
			want:   currencyRequired,
		},
		{
			name:   "halted_pnl_without_currency",
			filter: AccountListFilter{IncludeHaltedPnl: true},
			want:   currencyRequired,
		},
		{
			name:   "unknown_status",
			filter: AccountListFilter{Status: "paused"},
			want:   `status: unknown value "paused": invalid`,
		},
		{
			name:   "negative_position",
			filter: AccountListFilter{Position: CountRangeFilter{Min: ref(-1)}},
			want:   "position.min: -1 is negative: invalid",
		},
		{
			name: "pnl_bound_not_decimal",
			filter: AccountListFilter{Pnl: DenominatedDecimalRangeFilter{
				Range: DecimalRangeFilter{Max: ref("ten")}, Currency: "USD",
			}},
			want: `pnl.max: "ten" is not a valid decimal: invalid`,
		},
		{
			name: "pnl_min_above_max",
			filter: AccountListFilter{Pnl: DenominatedDecimalRangeFilter{
				Range: DecimalRangeFilter{Min: ref("2"), Max: ref("1")}, Currency: "USD",
			}},
			want: "pnl: min 2 is greater than max 1: invalid",
		},
		{
			name: "pnl_range_reported_before_status",
			filter: AccountListFilter{
				Status: "paused",
				Pnl: DenominatedDecimalRangeFilter{
					Range: DecimalRangeFilter{Min: ref("x")}, Currency: "USD",
				},
			},
			want: `pnl.min: "x" is not a valid decimal: invalid`,
		},
		{
			name:   "connection_without_id",
			filter: AccountListFilter{Venue: VenueFilter{Mode: VenueFilterConnection}},
			want:   "store: venue connection mode requires a connection id: invalid",
		},
		{
			name:   "group_sort_column",
			filter: AccountListFilter{Sort: SortSpec{Column: "accountCount"}},
			want:   `sort: unknown column "accountCount": invalid`,
		},
		{
			name:   "negative_offset",
			filter: AccountListFilter{Page: PageSpec{Offset: -1}},
			want:   "page.offset: -1 is negative: invalid",
		},
		{
			name: "currency_flag_reported_first",
			filter: AccountListFilter{
				Status:           "paused",
				Position:         CountRangeFilter{Min: ref(-1)},
				IncludeHaltedPnl: true,
				Venue:            VenueFilter{Mode: "bogus"},
				Sort:             SortSpec{Column: "pnl"},
				Page:             PageSpec{Limit: -1},
			},
			want: currencyRequired,
		},
		{
			name: "currency_range_reported_first",
			filter: AccountListFilter{
				Status: "paused",
				Pnl:    DenominatedDecimalRangeFilter{Range: DecimalRangeFilter{Min: ref("x")}},
				Page:   PageSpec{Limit: -1},
			},
			want: currencyRequired,
		},
		{
			name: "status_reported_before_page",
			filter: AccountListFilter{
				Status: "paused",
				Page:   PageSpec{Limit: -1},
			},
			want: `status: unknown value "paused": invalid`,
		},
		{name: "text_at_bounds", filter: AccountListFilter{
			Code: fragments(TextMatcherMaxFragments), BlockReason: runes(TextMatcherMaxRunes),
		}},
		{
			name:   "code_too_many_fragments",
			filter: AccountListFilter{Code: fragments(17)},
			want:   "code: 17 fragments, at most 16: invalid",
		},
		{
			name:   "block_reason_too_many_runes",
			filter: AccountListFilter{BlockReason: runes(overRunes)},
			want:   overRunesErr("blockReason"),
		},
		{
			name: "pnl_bound_outside_engine_range",
			filter: AccountListFilter{Pnl: DenominatedDecimalRangeFilter{
				Range: DecimalRangeFilter{Max: ref("1e2147483647")}, Currency: "USD",
			}},
			want: `pnl.max: "1e2147483647" is outside the engine decimal range: invalid`,
		},
		{
			name:   "currency_reported_before_text",
			filter: AccountListFilter{IncludeUnsetPnl: true, Code: fragments(17)},
			want:   currencyRequired,
		},
		{
			name:   "page_reported_before_text",
			filter: AccountListFilter{Page: PageSpec{Limit: -1}, BlockReason: runes(overRunes)},
			want:   "page.limit: -1 is negative: invalid",
		},
		{
			name:   "code_reported_before_block_reason",
			filter: AccountListFilter{Code: fragments(17), BlockReason: runes(overRunes)},
			want:   "code: 17 fragments, at most 16: invalid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireValidation(t, tc.filter.Validate(), tc.want)
		})
	}
}

func TestGroupListFilterValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		filter GroupListFilter
		want   string
	}{
		{name: "zero", filter: GroupListFilter{}},
		{name: "full", filter: GroupListFilter{
			Code:        ExactTextMatcher("desk"),
			Notes:       ExactTextMatcher("night"),
			BlockReason: ExactTextMatcher("halt"),
			Status:      StatusFilterActive,
			Position:    CountRangeFilter{Equal: ref(0)},
			Account:     CountRangeFilter{Min: ref(1), Max: ref(4)},
			Venue:       VenueFilter{Mode: VenueFilterAny},
			Sort:        SortSpec{Column: "accountCount"},
			Page:        PageSpec{Limit: 25, Offset: 25},
		}},
		{
			name:   "unknown_status",
			filter: GroupListFilter{Status: "paused"},
			want:   `status: unknown value "paused": invalid`,
		},
		{
			name:   "negative_position",
			filter: GroupListFilter{Position: CountRangeFilter{Max: ref(-1)}},
			want:   "position.max: -1 is negative: invalid",
		},
		{
			name:   "account_min_above_max",
			filter: GroupListFilter{Account: CountRangeFilter{Min: ref(5), Max: ref(2)}},
			want:   "account: min 5 is greater than max 2: invalid",
		},
		{
			name:   "unknown_venue_mode",
			filter: GroupListFilter{Venue: VenueFilter{Mode: "bogus"}},
			want:   `store: unknown venue filter mode "bogus": invalid`,
		},
		{
			name:   "account_sort_column",
			filter: GroupListFilter{Sort: SortSpec{Column: "group"}},
			want:   `sort: unknown column "group": invalid`,
		},
		{
			name:   "negative_limit",
			filter: GroupListFilter{Page: PageSpec{Limit: -1}},
			want:   "page.limit: -1 is negative: invalid",
		},
		{
			name: "status_reported_before_page",
			filter: GroupListFilter{
				Status: "paused",
				Page:   PageSpec{Limit: -1},
			},
			want: `status: unknown value "paused": invalid`,
		},
		{name: "text_at_bounds", filter: GroupListFilter{
			Code:        fragments(TextMatcherMaxFragments),
			Notes:       runes(TextMatcherMaxRunes),
			BlockReason: fragments(TextMatcherMaxFragments),
		}},
		{
			name:   "code_too_many_fragments",
			filter: GroupListFilter{Code: fragments(17)},
			want:   "code: 17 fragments, at most 16: invalid",
		},
		{
			name:   "notes_too_many_runes",
			filter: GroupListFilter{Notes: runes(overRunes)},
			want:   overRunesErr("notes"),
		},
		{
			name:   "block_reason_too_many_fragments",
			filter: GroupListFilter{BlockReason: fragments(17)},
			want:   "blockReason: 17 fragments, at most 16: invalid",
		},
		{
			name:   "page_reported_before_text",
			filter: GroupListFilter{Page: PageSpec{Offset: -1}, Code: fragments(17)},
			want:   "page.offset: -1 is negative: invalid",
		},
		{
			name:   "notes_reported_before_block_reason",
			filter: GroupListFilter{Notes: runes(overRunes), BlockReason: fragments(17)},
			want:   overRunesErr("notes"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireValidation(t, tc.filter.Validate(), tc.want)
		})
	}
}

func TestOrderListFilterValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		filter OrderListFilter
		want   string
	}{
		{name: "zero", filter: OrderListFilter{}},
		{name: "full", filter: OrderListFilter{
			Venue:      VenueFilter{Mode: VenueFilterNone},
			Account:    "acc-1",
			Source:     domain.SourceAPI,
			ExternalID: ExactTextMatcher("ord-1"),
			Side:       ref(domain.OrderSideSell),
			Status:     []domain.OrderStatus{domain.OrderStatusSubmitted, domain.OrderStatusFilled},
			BaseAsset:  ExactTextMatcher("BTC"),
			QuoteAsset: ExactTextMatcher("USD"),
			Amount:     DecimalRangeFilter{Min: ref("1"), Max: ref("10")},
			Price:      DecimalRangeFilter{Equal: ref("100.5")},
			At:         TimeRangeFilter{Min: ref(day1), Max: ref(day2)},
			Sort:       SortSpec{Column: "amountValue", Descending: true},
			Page:       PageSpec{Limit: 10},
		}},
		{
			name:   "id_without_connection_mode",
			filter: OrderListFilter{Venue: VenueFilter{ConnectionID: "conn-1"}},
			want:   "store: venue connection id requires connection mode: invalid",
		},
		{
			name:   "unknown_side",
			filter: OrderListFilter{Side: ref(domain.OrderSide("hold"))},
			want:   `side: unknown value "hold": invalid`,
		},
		{
			name: "unknown_status",
			filter: OrderListFilter{
				Status: []domain.OrderStatus{domain.OrderStatusFilled, "open"},
			},
			want: `status: unknown value "open": invalid`,
		},
		{
			name:   "amount_not_decimal",
			filter: OrderListFilter{Amount: DecimalRangeFilter{Min: ref("abc")}},
			want:   `amount.min: "abc" is not a valid decimal: invalid`,
		},
		{
			name:   "price_min_above_max",
			filter: OrderListFilter{Price: DecimalRangeFilter{Min: ref("3"), Max: ref("1")}},
			want:   "price: min 3 is greater than max 1: invalid",
		},
		{
			name:   "at_min_after_max",
			filter: OrderListFilter{At: TimeRangeFilter{Min: ref(day2), Max: ref(day1)}},
			want:   "at: min 2026-01-02T00:00:00Z is after max 2026-01-01T00:00:00Z: invalid",
		},
		{
			name:   "trade_sort_column",
			filter: OrderListFilter{Sort: SortSpec{Column: "quantity"}},
			want:   `sort: unknown column "quantity": invalid`,
		},
		{
			name:   "negative_offset",
			filter: OrderListFilter{Page: PageSpec{Offset: -5}},
			want:   "page.offset: -5 is negative: invalid",
		},
		{
			name: "venue_reported_before_page",
			filter: OrderListFilter{
				Venue: VenueFilter{Mode: VenueFilterConnection},
				Page:  PageSpec{Offset: -5},
			},
			want: "store: venue connection mode requires a connection id: invalid",
		},
		{name: "every_status_once", filter: OrderListFilter{Status: []domain.OrderStatus{
			domain.OrderStatusSubmitted, domain.OrderStatusAccepted, domain.OrderStatusRejected,
			domain.OrderStatusCommitted, domain.OrderStatusRolledBack, domain.OrderStatusFilled,
			domain.OrderStatusPartiallyFilled, domain.OrderStatusCancelled,
		}}},
		{
			name: "repeated_status",
			filter: OrderListFilter{Status: []domain.OrderStatus{
				domain.OrderStatusFilled, domain.OrderStatusSubmitted, domain.OrderStatusFilled,
			}},
			want: `status: "filled" is repeated: invalid`,
		},
		{
			name:   "repeat_reported_before_later_unknown",
			filter: OrderListFilter{Status: []domain.OrderStatus{domain.OrderStatusFilled, domain.OrderStatusFilled, "open"}},
			want:   `status: "filled" is repeated: invalid`,
		},
		{
			name:   "unknown_reported_before_later_repeat",
			filter: OrderListFilter{Status: []domain.OrderStatus{domain.OrderStatusFilled, "open", domain.OrderStatusFilled}},
			want:   `status: unknown value "open": invalid`,
		},
		{
			name: "long_repeated_list",
			filter: OrderListFilter{
				Status: slices.Repeat([]domain.OrderStatus{domain.OrderStatusCancelled}, 100000),
			},
			want: `status: "cancelled" is repeated: invalid`,
		},
		{name: "text_at_bounds", filter: OrderListFilter{
			BaseAsset: fragments(TextMatcherMaxFragments), QuoteAsset: runes(TextMatcherMaxRunes),
		}},
		{name: "external_id_not_bounded", filter: OrderListFilter{ExternalID: fragments(17)}},
		{
			name:   "base_asset_too_many_fragments",
			filter: OrderListFilter{BaseAsset: fragments(17)},
			want:   "baseAsset: 17 fragments, at most 16: invalid",
		},
		{
			name:   "quote_asset_too_many_runes",
			filter: OrderListFilter{QuoteAsset: runes(overRunes)},
			want:   overRunesErr("quoteAsset"),
		},
		{
			name:   "amount_outside_engine_range",
			filter: OrderListFilter{Amount: DecimalRangeFilter{Min: ref("1e2147483647")}},
			want:   `amount.min: "1e2147483647" is outside the engine decimal range: invalid`,
		},
		{
			name:   "price_beyond_engine_scale",
			filter: OrderListFilter{Price: DecimalRangeFilter{Equal: ref("0.00000000000000000000000000001")}},
			want:   `price.equal: "0.00000000000000000000000000001" is outside the engine decimal range: invalid`,
		},
		{
			name:   "page_reported_before_text",
			filter: OrderListFilter{Page: PageSpec{Limit: -1}, BaseAsset: fragments(17)},
			want:   "page.limit: -1 is negative: invalid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireValidation(t, tc.filter.Validate(), tc.want)
		})
	}
}

func TestTradeListFilterValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		filter TradeListFilter
		want   string
	}{
		{name: "zero", filter: TradeListFilter{}},
		{name: "full", filter: TradeListFilter{
			Venue:      VenueFilter{Mode: VenueFilterConnection, ConnectionID: "conn-1"},
			Account:    ExactTextMatcher("acc-1"),
			ExternalID: "trd-1",
			BaseAsset:  ExactTextMatcher("BTC"),
			QuoteAsset: ExactTextMatcher("USD"),
			Side:       ref(domain.OrderSideBuy),
			Source:     domain.SourceMCP,
			At:         TimeRangeFilter{Min: ref(day1)},
			Quantity:   DecimalRangeFilter{Min: ref("0.5")},
			Price:      DecimalRangeFilter{Max: ref("100")},
			LockPrice:  DecimalRangeFilter{NotEqual: ref("0")},
			Sort:       SortSpec{Column: "lockPrice"},
			Page:       PageSpec{Limit: 20, Offset: 40},
		}},
		{
			name:   "connection_without_id",
			filter: TradeListFilter{Venue: VenueFilter{Mode: VenueFilterConnection}},
			want:   "store: venue connection mode requires a connection id: invalid",
		},
		{
			name:   "unknown_side",
			filter: TradeListFilter{Side: ref(domain.OrderSide("short"))},
			want:   `side: unknown value "short": invalid`,
		},
		{
			name:   "at_min_after_max",
			filter: TradeListFilter{At: TimeRangeFilter{Min: ref(day2), Max: ref(day1)}},
			want:   "at: min 2026-01-02T00:00:00Z is after max 2026-01-01T00:00:00Z: invalid",
		},
		{
			name:   "quantity_not_decimal",
			filter: TradeListFilter{Quantity: DecimalRangeFilter{Max: ref("many")}},
			want:   `quantity.max: "many" is not a valid decimal: invalid`,
		},
		{
			name:   "price_not_decimal",
			filter: TradeListFilter{Price: DecimalRangeFilter{Equal: ref("n/a")}},
			want:   `price.equal: "n/a" is not a valid decimal: invalid`,
		},
		{
			name:   "lock_price_min_above_max",
			filter: TradeListFilter{LockPrice: DecimalRangeFilter{Min: ref("9"), Max: ref("1")}},
			want:   "lockPrice: min 9 is greater than max 1: invalid",
		},
		{
			name:   "order_sort_column",
			filter: TradeListFilter{Sort: SortSpec{Column: "amountValue"}},
			want:   `sort: unknown column "amountValue": invalid`,
		},
		{
			name:   "negative_limit",
			filter: TradeListFilter{Page: PageSpec{Limit: -2}},
			want:   "page.limit: -2 is negative: invalid",
		},
		{
			name: "venue_reported_before_page",
			filter: TradeListFilter{
				Venue: VenueFilter{Mode: VenueFilterConnection},
				Page:  PageSpec{Limit: -2},
			},
			want: "store: venue connection mode requires a connection id: invalid",
		},
		{name: "text_at_bounds", filter: TradeListFilter{
			Account:    runes(TextMatcherMaxRunes),
			BaseAsset:  fragments(TextMatcherMaxFragments),
			QuoteAsset: ExactTextMatcher(strings.Repeat("字", TextMatcherMaxRunes)),
		}},
		{
			name:   "account_too_many_runes",
			filter: TradeListFilter{Account: runes(overRunes)},
			want:   overRunesErr("account"),
		},
		{
			name:   "base_asset_too_many_fragments",
			filter: TradeListFilter{BaseAsset: fragments(17)},
			want:   "baseAsset: 17 fragments, at most 16: invalid",
		},
		{
			name:   "quote_asset_too_many_runes",
			filter: TradeListFilter{QuoteAsset: ExactTextMatcher(strings.Repeat("字", overRunes))},
			want:   overRunesErr("quoteAsset"),
		},
		{
			name:   "quantity_outside_engine_range",
			filter: TradeListFilter{Quantity: DecimalRangeFilter{Max: ref("1e2147483647")}},
			want:   `quantity.max: "1e2147483647" is outside the engine decimal range: invalid`,
		},
		{
			name:   "price_outside_engine_range",
			filter: TradeListFilter{Price: DecimalRangeFilter{Min: ref("-79228162514264337593543950336")}},
			want:   `price.min: "-79228162514264337593543950336" is outside the engine decimal range: invalid`,
		},
		{
			name:   "lock_price_outside_engine_range",
			filter: TradeListFilter{LockPrice: DecimalRangeFilter{NotEqual: ref("1e-2147483648")}},
			want:   `lockPrice.notEqual: "1e-2147483648" is outside the engine decimal range: invalid`,
		},
		{
			name:   "page_reported_before_text",
			filter: TradeListFilter{Page: PageSpec{Offset: -1}, QuoteAsset: runes(overRunes)},
			want:   "page.offset: -1 is negative: invalid",
		},
		{
			name:   "account_reported_before_assets",
			filter: TradeListFilter{Account: runes(overRunes), BaseAsset: fragments(17)},
			want:   overRunesErr("account"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireValidation(t, tc.filter.Validate(), tc.want)
		})
	}
}
