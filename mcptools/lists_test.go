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

package mcptools

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// listSource serves the four list reads and the group read the account join
// needs, recording each filter it receives; any other Source call panics on
// the nil embedded interface.
type listSource struct {
	frameworkmcp.Source
	calls         int
	orderFilter   store.OrderListFilter
	tradeFilter   store.TradeListFilter
	accountFilter store.AccountListFilter
	groupFilter   store.GroupListFilter
	orders        backend.OrderListPage
	trades        store.TradeListPage
	accounts      store.AccountListPage
	groups        []domain.AccountGroup
	groupRows     store.GroupListPage
	err           error
}

func (s *listSource) ListOrderRows(
	_ context.Context, filter store.OrderListFilter,
) (backend.OrderListPage, error) {
	s.calls++
	s.orderFilter = filter
	return s.orders, s.err
}

func (s *listSource) ListTradeRows(
	_ context.Context, filter store.TradeListFilter,
) (store.TradeListPage, error) {
	s.calls++
	s.tradeFilter = filter
	return s.trades, s.err
}

func (s *listSource) ListAccountRows(
	_ context.Context, filter store.AccountListFilter,
) (store.AccountListPage, error) {
	s.calls++
	s.accountFilter = filter
	return s.accounts, s.err
}

func (s *listSource) ListGroups(context.Context) ([]domain.AccountGroup, error) {
	s.calls++
	return s.groups, s.err
}

func (s *listSource) ListGroupRows(
	_ context.Context, filter store.GroupListFilter,
) (store.GroupListPage, error) {
	s.calls++
	s.groupFilter = filter
	return s.groupRows, s.err
}

func ref[T any](v T) *T { return &v }

// listArgs are the arguments every list tool takes.
type listArgs struct {
	venue *venueInput
	sort  *sortInput
	limit int
	page  int
}

// listTool runs one list tool with the common arguments and reports the venue
// filter and page window that reached the core filter.
type listTool struct {
	name string
	run  func(t *testing.T, src *listSource, args listArgs) error
	got  func(src *listSource) (store.VenueFilter, store.PageSpec)
}

func listTools() []listTool {
	return []listTool{
		{
			name: "list_orders",
			run: func(t *testing.T, src *listSource, a listArgs) error {
				in := listOrdersInput{Venue: a.venue, Sort: a.sort, Limit: a.limit, Page: a.page}
				_, _, err := listOrdersHandler(src)(t.Context(), nil, in)
				return err
			},
			got: func(src *listSource) (store.VenueFilter, store.PageSpec) {
				return src.orderFilter.Venue, src.orderFilter.Page
			},
		},
		{
			name: "list_trades",
			run: func(t *testing.T, src *listSource, a listArgs) error {
				in := listTradesInput{Venue: a.venue, Sort: a.sort, Limit: a.limit, Page: a.page}
				_, _, err := listTradesHandler(src)(t.Context(), nil, in)
				return err
			},
			got: func(src *listSource) (store.VenueFilter, store.PageSpec) {
				return src.tradeFilter.Venue, src.tradeFilter.Page
			},
		},
		{
			name: "list_accounts",
			run: func(t *testing.T, src *listSource, a listArgs) error {
				in := listAccountsInput{Venue: a.venue, Sort: a.sort, Limit: a.limit, Page: a.page}
				_, _, err := listAccountsHandler(src)(t.Context(), nil, in)
				return err
			},
			got: func(src *listSource) (store.VenueFilter, store.PageSpec) {
				return src.accountFilter.Venue, src.accountFilter.Page
			},
		},
		{
			name: "list_groups",
			run: func(t *testing.T, src *listSource, a listArgs) error {
				in := listGroupsInput{Venue: a.venue, Sort: a.sort, Limit: a.limit, Page: a.page}
				_, _, err := listGroupsHandler(src)(t.Context(), nil, in)
				return err
			},
			got: func(src *listSource) (store.VenueFilter, store.PageSpec) {
				return src.groupFilter.Venue, src.groupFilter.Page
			},
		},
	}
}

// requireValidation fails the test unless err is a validation tool error
// naming want and the Source was never called.
func requireValidation(t *testing.T, src *listSource, err error, want string) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), "validation: ") || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want a validation error naming %q", err, want)
	}
	if src.calls != 0 {
		t.Fatalf("Source called %d time(s) after a rejected input", src.calls)
	}
}

func TestListToolsVenue(t *testing.T) {
	for _, tool := range listTools() {
		for _, tc := range []struct {
			name  string
			venue *venueInput
			want  store.VenueFilter
		}{
			{"omitted", nil, store.VenueFilter{}},
			{"connection", &venueInput{Mode: "connection", Connection: "conn-1"},
				store.VenueFilter{Mode: store.VenueFilterConnection, ConnectionID: "conn-1"}},
			{"any", &venueInput{Mode: "any"}, store.VenueFilter{Mode: store.VenueFilterAny}},
			{"none", &venueInput{Mode: "none"}, store.VenueFilter{Mode: store.VenueFilterNone}},
		} {
			t.Run(tool.name+"/"+tc.name, func(t *testing.T) {
				src := &listSource{}
				if err := tool.run(t, src, listArgs{venue: tc.venue}); err != nil {
					t.Fatal(err)
				}
				if got, _ := tool.got(src); got != tc.want {
					t.Fatalf("venue filter = %+v, want %+v", got, tc.want)
				}
			})
		}
	}
}

func TestListToolsPaging(t *testing.T) {
	for _, tool := range listTools() {
		for _, tc := range []struct {
			name        string
			limit, page int
			want        store.PageSpec
		}{
			{"omitted", 0, 0, store.PageSpec{Limit: 50, Offset: 0}},
			{"limit-and-page", 10, 3, store.PageSpec{Limit: 10, Offset: 20}},
			{"capped", 1000, 0, store.PageSpec{Limit: 500, Offset: 0}},
		} {
			t.Run(tool.name+"/"+tc.name, func(t *testing.T) {
				src := &listSource{}
				if err := tool.run(t, src, listArgs{limit: tc.limit, page: tc.page}); err != nil {
					t.Fatal(err)
				}
				if _, got := tool.got(src); got != tc.want {
					t.Fatalf("page = %+v, want %+v", got, tc.want)
				}
			})
		}
	}
}

func TestListToolsRejectBeforeSource(t *testing.T) {
	for _, tool := range listTools() {
		for _, tc := range []struct {
			name string
			args listArgs
			want string
		}{
			{"negative-limit", listArgs{limit: -1}, "limit: must not be negative"},
			{"negative-page", listArgs{page: -1}, "page: must not be negative"},
			{"page-overflow", listArgs{limit: 10, page: math.MaxInt}, "page: too large"},
			{"unknown-sort-column", listArgs{sort: &sortInput{Column: "nope"}}, `sort: unknown column "nope"`},
			{"unknown-venue-mode", listArgs{venue: &venueInput{Mode: "some"}}, `unknown venue filter mode "some"`},
			{"connection-without-id", listArgs{venue: &venueInput{Mode: "connection"}}, "connection mode requires a connection id"},
			{"id-without-connection-mode", listArgs{venue: &venueInput{Mode: "any", Connection: "conn-1"}}, "connection id requires connection mode"},
		} {
			t.Run(tool.name+"/"+tc.name, func(t *testing.T) {
				src := &listSource{}
				requireValidation(t, src, tool.run(t, src, tc.args), tc.want)
			})
		}
	}
}

func TestListToolsSourceError(t *testing.T) {
	for _, tool := range listTools() {
		t.Run(tool.name, func(t *testing.T) {
			src := &listSource{err: fmt.Errorf("read rows: %w", domain.ErrNotFound)}
			err := tool.run(t, src, listArgs{})
			if err == nil || err.Error() != "not_found: read rows: not found" {
				t.Fatalf("error = %v, want not_found: read rows: not found", err)
			}
		})
	}
}

func TestListToolsRejectOwnValues(t *testing.T) {
	seventeenFragments := strings.Split(strings.Repeat("a", 17), "")
	tooManyRunes := strings.Repeat("x", store.TextMatcherMaxRunes+1)
	tooManyRunesErr := func(field string) string {
		return fmt.Sprintf(
			"%s: %d code points, at most %d", field, store.TextMatcherMaxRunes+1, store.TextMatcherMaxRunes,
		)
	}
	orders := func(in listOrdersInput) func(*testing.T, *listSource) error {
		return func(t *testing.T, src *listSource) error {
			_, _, err := listOrdersHandler(src)(t.Context(), nil, in)
			return err
		}
	}
	trades := func(in listTradesInput) func(*testing.T, *listSource) error {
		return func(t *testing.T, src *listSource) error {
			_, _, err := listTradesHandler(src)(t.Context(), nil, in)
			return err
		}
	}
	accounts := func(in listAccountsInput) func(*testing.T, *listSource) error {
		return func(t *testing.T, src *listSource) error {
			_, _, err := listAccountsHandler(src)(t.Context(), nil, in)
			return err
		}
	}
	groups := func(in listGroupsInput) func(*testing.T, *listSource) error {
		return func(t *testing.T, src *listSource) error {
			_, _, err := listGroupsHandler(src)(t.Context(), nil, in)
			return err
		}
	}
	for _, tc := range []struct {
		name string
		run  func(*testing.T, *listSource) error
		want string
	}{
		{"list_orders/non-decimal-amount", orders(listOrdersInput{Amount: &decimalRangeInput{Min: ref("ten")}}), "amount.min"},
		{"list_orders/inverted-price", orders(listOrdersInput{Price: &decimalRangeInput{Min: ref("10"), Max: ref("1")}}), "price: min 10 is greater than max 1"},
		{"list_orders/bad-side", orders(listOrdersInput{Side: "hold"}), `side: unknown value "hold"`},
		{"list_orders/unknown-status", orders(listOrdersInput{Status: []string{"filled", "open"}}), `status: unknown value "open"`},
		{"list_orders/malformed-at", orders(listOrdersInput{At: &timeRangeInput{Min: ref("yesterday")}}), `at.min: "yesterday" is not an RFC3339Nano time`},
		{"list_orders/inverted-at", orders(listOrdersInput{At: &timeRangeInput{Min: ref("2026-01-02T00:00:00Z"), Max: ref("2026-01-01T00:00:00Z")}}), "at: min 2026-01-02T00:00:00Z is after max"},
		{"list_trades/inverted-lock-price", trades(listTradesInput{LockPrice: &decimalRangeInput{Min: ref("5"), Max: ref("4")}}), "lockPrice: min 5 is greater than max 4"},
		{"list_trades/malformed-at", trades(listTradesInput{At: &timeRangeInput{Max: ref("2026-13-01T00:00:00Z")}}), "at.max: "},
		{"list_trades/bad-side", trades(listTradesInput{Side: "short"}), `side: unknown value "short"`},
		{"list_accounts/pnl-without-currency", accounts(listAccountsInput{Pnl: &pnlInput{Range: &decimalRangeInput{Min: ref("1")}}}), "pnl: currency is required"},
		{"list_accounts/include-unset-without-currency", accounts(listAccountsInput{IncludeUnsetPnl: true}), "pnl: currency is required"},
		{"list_accounts/unknown-status", accounts(listAccountsInput{Status: "frozen"}), `status: unknown value "frozen"`},
		{"list_groups/negative-account-count", groups(listGroupsInput{Account: &countRangeInput{Min: ref(-1)}}), "account.min: -1 is negative"},
		{"list_groups/inverted-position-count", groups(listGroupsInput{Position: &countRangeInput{Min: ref(3), Max: ref(2)}}), "position: min 3 is greater than max 2"},
		// The core list-filter bounds reach the tools through the same Validate.
		{"list_orders/amount-outside-engine-range", orders(listOrdersInput{Amount: &decimalRangeInput{Min: ref("1e2147483647")}}), `amount.min: "1e2147483647" is outside the engine decimal range`},
		{"list_trades/lock-price-outside-engine-range", trades(listTradesInput{LockPrice: &decimalRangeInput{Max: ref("1e2147483647")}}), `lockPrice.max: "1e2147483647" is outside the engine decimal range`},
		{"list_accounts/pnl-outside-engine-range", accounts(listAccountsInput{Pnl: &pnlInput{Currency: "USD", Range: &decimalRangeInput{Equal: ref("1e2147483647")}}}), `pnl.equal: "1e2147483647" is outside the engine decimal range`},
		{"list_orders/base-asset-fragments", orders(listOrdersInput{BaseAsset: &textMatchInput{Fragments: seventeenFragments}}), "baseAsset: 17 fragments, at most 16"},
		{"list_accounts/code-fragments", accounts(listAccountsInput{Code: &textMatchInput{Fragments: seventeenFragments}}), "code: 17 fragments, at most 16"},
		{"list_trades/account-runes", trades(listTradesInput{Account: &textMatchInput{Fragments: []string{tooManyRunes}}}), tooManyRunesErr("account")},
		{"list_groups/notes-runes", groups(listGroupsInput{Notes: &textMatchInput{Fragments: []string{tooManyRunes}}}), tooManyRunesErr("notes")},
		{"list_orders/repeated-status", orders(listOrdersInput{Status: []string{"filled", "cancelled", "filled"}}), `status: "filled" is repeated`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &listSource{}
			requireValidation(t, src, tc.run(t, src), tc.want)
		})
	}
}

func TestListOrdersFilter(t *testing.T) {
	src := &listSource{}
	in := listOrdersInput{
		Venue:      &venueInput{Mode: "connection", Connection: "conn-1"},
		Account:    "acc-1",
		Source:     "api",
		Side:       "sell",
		Status:     []string{"submitted", "partially_filled"},
		BaseAsset:  &textMatchInput{Fragments: []string{"B", "C"}, AnchorStart: true},
		QuoteAsset: &textMatchInput{Fragments: []string{"SD"}, AnchorEnd: true},
		Amount:     &decimalRangeInput{Min: ref("1"), Max: ref("10.5"), MinExclusive: true},
		Price:      &decimalRangeInput{Equal: ref("101.25"), NotEqual: ref("99"), MaxExclusive: true},
		At: &timeRangeInput{
			Min: ref("2026-01-02T05:04:05.123456789+02:00"), Max: ref("2026-01-03T00:00:00Z"),
			MinExclusive: true, MaxExclusive: true,
		},
		Sort:  &sortInput{Column: "price", Descending: true},
		Limit: 20,
		Page:  2,
	}
	if _, _, err := listOrdersHandler(src)(t.Context(), nil, in); err != nil {
		t.Fatal(err)
	}
	sell := domain.OrderSideSell
	want := store.OrderListFilter{
		Venue:      store.VenueFilter{Mode: store.VenueFilterConnection, ConnectionID: "conn-1"},
		Account:    "acc-1",
		Source:     domain.SourceAPI,
		Side:       &sell,
		Status:     []domain.OrderStatus{domain.OrderStatusSubmitted, domain.OrderStatusPartiallyFilled},
		BaseAsset:  store.TextMatcher{Fragments: []string{"B", "C"}, AnchorStart: true},
		QuoteAsset: store.TextMatcher{Fragments: []string{"SD"}, AnchorEnd: true},
		Amount:     store.DecimalRangeFilter{Min: ref("1"), Max: ref("10.5"), MinExclusive: true},
		Price:      store.DecimalRangeFilter{Equal: ref("101.25"), NotEqual: ref("99"), MaxExclusive: true},
		At: store.TimeRangeFilter{
			Min:          ref(time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)),
			Max:          ref(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)),
			MinExclusive: true, MaxExclusive: true,
		},
		Sort: store.SortSpec{Column: "price", Descending: true},
		Page: store.PageSpec{Limit: 20, Offset: 20},
	}
	if !reflect.DeepEqual(src.orderFilter, want) {
		t.Fatalf("order filter\n got: %+v\nwant: %+v", src.orderFilter, want)
	}
}

func TestListTradesFilter(t *testing.T) {
	src := &listSource{}
	in := listTradesInput{
		Venue:      &venueInput{Mode: "connection", Connection: "conn-2"},
		Account:    &textMatchInput{Fragments: []string{"acc"}, AnchorStart: true, AnchorEnd: true},
		ID:         "trade-1",
		BaseAsset:  &textMatchInput{Fragments: []string{"BTC"}},
		QuoteAsset: &textMatchInput{Fragments: []string{"USD"}, AnchorEnd: true},
		Side:       "buy",
		Source:     "mcp",
		At:         &timeRangeInput{Min: ref("2026-01-02T00:00:00Z"), MaxExclusive: true},
		Quantity:   &decimalRangeInput{Min: ref("0.5")},
		Price:      &decimalRangeInput{Max: ref("200"), MaxExclusive: true},
		LockPrice:  &decimalRangeInput{Equal: ref("101.3")},
		Sort:       &sortInput{Column: "lockPrice"},
		Limit:      5,
		Page:       4,
	}
	if _, _, err := listTradesHandler(src)(t.Context(), nil, in); err != nil {
		t.Fatal(err)
	}
	buy := domain.OrderSideBuy
	want := store.TradeListFilter{
		Venue:      store.VenueFilter{Mode: store.VenueFilterConnection, ConnectionID: "conn-2"},
		Account:    store.TextMatcher{Fragments: []string{"acc"}, AnchorStart: true, AnchorEnd: true},
		ExternalID: "trade-1",
		BaseAsset:  store.TextMatcher{Fragments: []string{"BTC"}},
		QuoteAsset: store.TextMatcher{Fragments: []string{"USD"}, AnchorEnd: true},
		Side:       &buy,
		Source:     domain.SourceMCP,
		At:         store.TimeRangeFilter{Min: ref(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)), MaxExclusive: true},
		Quantity:   store.DecimalRangeFilter{Min: ref("0.5")},
		Price:      store.DecimalRangeFilter{Max: ref("200"), MaxExclusive: true},
		LockPrice:  store.DecimalRangeFilter{Equal: ref("101.3")},
		Sort:       store.SortSpec{Column: "lockPrice"},
		Page:       store.PageSpec{Limit: 5, Offset: 15},
	}
	if !reflect.DeepEqual(src.tradeFilter, want) {
		t.Fatalf("trade filter\n got: %+v\nwant: %+v", src.tradeFilter, want)
	}
}

func TestListAccountsFilter(t *testing.T) {
	src := &listSource{}
	in := listAccountsInput{
		Group:            ref("desk-a"),
		Code:             &textMatchInput{Fragments: []string{"acc"}, AnchorStart: true},
		BlockReason:      &textMatchInput{Fragments: []string{"halt"}, AnchorEnd: true},
		Status:           "blocked",
		Position:         &countRangeInput{Min: ref(1), Max: ref(9), Equal: ref(3), NotEqual: ref(4), MinExclusive: true, MaxExclusive: true},
		Pnl:              &pnlInput{Currency: "USD", Range: &decimalRangeInput{Min: ref("-5"), Max: ref("5")}},
		IncludeUnsetPnl:  true,
		IncludeHaltedPnl: true,
		Venue:            &venueInput{Mode: "connection", Connection: "conn-3"},
		Sort:             &sortInput{Column: "positionCount", Descending: true},
		Limit:            30,
		Page:             3,
	}
	if _, _, err := listAccountsHandler(src)(t.Context(), nil, in); err != nil {
		t.Fatal(err)
	}
	want := store.AccountListFilter{
		GroupCode:   ref("desk-a"),
		Code:        store.TextMatcher{Fragments: []string{"acc"}, AnchorStart: true},
		BlockReason: store.TextMatcher{Fragments: []string{"halt"}, AnchorEnd: true},
		Status:      store.StatusFilterBlocked,
		Position: store.CountRangeFilter{
			Min: ref(1), Max: ref(9), Equal: ref(3), NotEqual: ref(4), MinExclusive: true, MaxExclusive: true,
		},
		Pnl: store.DenominatedDecimalRangeFilter{
			Currency: "USD", Range: store.DecimalRangeFilter{Min: ref("-5"), Max: ref("5")},
		},
		IncludeUnsetPnl:  true,
		IncludeHaltedPnl: true,
		Venue:            store.VenueFilter{Mode: store.VenueFilterConnection, ConnectionID: "conn-3"},
		Sort:             store.SortSpec{Column: "positionCount", Descending: true},
		Page:             store.PageSpec{Limit: 30, Offset: 60},
	}
	if !reflect.DeepEqual(src.accountFilter, want) {
		t.Fatalf("account filter\n got: %+v\nwant: %+v", src.accountFilter, want)
	}
}

func TestListAccountsGroupNilVersusEmpty(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group *string
	}{
		{"omitted-any-group", nil},
		{"empty-no-group", ref("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &listSource{}
			if _, _, err := listAccountsHandler(src)(t.Context(), nil, listAccountsInput{Group: tc.group}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(src.accountFilter.GroupCode, tc.group) {
				t.Fatalf("group code = %v, want %v", src.accountFilter.GroupCode, tc.group)
			}
		})
	}
}

func TestListGroupsFilter(t *testing.T) {
	src := &listSource{}
	in := listGroupsInput{
		Code:        &textMatchInput{Fragments: []string{"desk"}, AnchorStart: true},
		Notes:       &textMatchInput{Fragments: []string{"night"}},
		BlockReason: &textMatchInput{Fragments: []string{"halt"}, AnchorEnd: true},
		Status:      "active",
		Position:    &countRangeInput{Max: ref(10)},
		Account:     &countRangeInput{Min: ref(1), MinExclusive: true},
		Venue:       &venueInput{Mode: "connection", Connection: "conn-4"},
		Sort:        &sortInput{Column: "accountCount", Descending: true},
		Limit:       7,
		Page:        2,
	}
	if _, _, err := listGroupsHandler(src)(t.Context(), nil, in); err != nil {
		t.Fatal(err)
	}
	want := store.GroupListFilter{
		Code:        store.TextMatcher{Fragments: []string{"desk"}, AnchorStart: true},
		Notes:       store.TextMatcher{Fragments: []string{"night"}},
		BlockReason: store.TextMatcher{Fragments: []string{"halt"}, AnchorEnd: true},
		Status:      store.StatusFilterActive,
		Position:    store.CountRangeFilter{Max: ref(10)},
		Account:     store.CountRangeFilter{Min: ref(1), MinExclusive: true},
		Venue:       store.VenueFilter{Mode: store.VenueFilterConnection, ConnectionID: "conn-4"},
		Sort:        store.SortSpec{Column: "accountCount", Descending: true},
		Page:        store.PageSpec{Limit: 7, Offset: 7},
	}
	if !reflect.DeepEqual(src.groupFilter, want) {
		t.Fatalf("group filter\n got: %+v\nwant: %+v", src.groupFilter, want)
	}
}

func TestListOrdersRows(t *testing.T) {
	rows := []backend.OrderListRow{
		{
			Order: domain.Order{
				ExternalID: "order-1", Account: "acc-1", BaseAsset: "BTC", QuoteAsset: "USD",
				Side: domain.OrderSideBuy, AmountValue: "1", Status: domain.OrderStatusSubmitted,
				Source: domain.SourceAPI, At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			},
			DisplayPrice: "101.30", Signed: true, VenueConnection: "conn-1",
		},
		{Order: domain.Order{ExternalID: "order-2", Account: "acc-2", Side: domain.OrderSideSell}},
	}
	src := &listSource{orders: backend.OrderListPage{Rows: rows, Total: 7}}
	text, out, err := listOrdersHandler(src)(t.Context(), nil, listOrdersInput{})
	if err != nil {
		t.Fatal(err)
	}
	want := listOrdersOutput{
		Orders: []httpx.OrderDTO{httpx.ToOrderRowDTO(rows[0]), httpx.ToOrderRowDTO(rows[1])},
		Total:  7,
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("output\n got: %+v\nwant: %+v", out, want)
	}
	if text != "2 of 7 order(s)" {
		t.Fatalf("summary = %q", text)
	}
	_, empty, err := listOrdersHandler(&listSource{})(t.Context(), nil, listOrdersInput{})
	if err != nil || empty.Orders == nil {
		t.Fatalf("empty page = %+v, %v; want a non-nil empty slice", empty, err)
	}
}

func TestListTradesRows(t *testing.T) {
	rows := []store.TradeListRow{
		{
			Trade: domain.Trade{
				ExternalID: "trade-1", Order: "order-1", Account: "acc-1", Side: domain.OrderSideBuy,
				Quantity: "0.5", Price: "101.2", LockPrice: "101.3",
				Commission: &domain.Commission{Amount: "0.45", Currency: "USD"},
			},
			VenueConnection: "conn-1",
		},
	}
	src := &listSource{trades: store.TradeListPage{Rows: rows, Total: 3}}
	text, out, err := listTradesHandler(src)(t.Context(), nil, listTradesInput{})
	if err != nil {
		t.Fatal(err)
	}
	want := listTradesOutput{Trades: []httpx.TradeDTO{httpx.ToTradeRowDTO(rows[0])}, Total: 3}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("output\n got: %+v\nwant: %+v", out, want)
	}
	if text != "1 of 3 trade(s)" {
		t.Fatalf("summary = %q", text)
	}
	_, empty, err := listTradesHandler(&listSource{})(t.Context(), nil, listTradesInput{})
	if err != nil || empty.Trades == nil {
		t.Fatalf("empty page = %+v, %v; want a non-nil empty slice", empty, err)
	}
}

func TestListAccountsRows(t *testing.T) {
	rows := []store.AccountListRow{
		{Account: domain.Account{Code: "acc-1", GroupCode: "desk-a"}, PositionCount: 2, VenueAccess: true},
		{Account: domain.Account{Code: "acc-2"}, PositionCount: 1},
	}
	src := &listSource{
		accounts: store.AccountListPage{Rows: rows, Total: 9},
		groups:   []domain.AccountGroup{{Code: "desk-a", Blocked: true, BlockReason: "desk halt"}},
	}
	text, out, err := listAccountsHandler(src)(t.Context(), nil, listAccountsInput{})
	if err != nil {
		t.Fatal(err)
	}
	groupBlock := domain.AccountBlockState{
		Reason: "desk halt", GroupReason: "desk halt", Source: domain.AccountBlockSourceGroup,
		Blocked: true, GroupBlocked: true,
	}
	noBlock := domain.AccountBlockState{Source: domain.AccountBlockSourceNone}
	want := listAccountsOutput{
		Accounts: []httpx.AccountDTO{
			httpx.ToAccountRowDTO(rows[0], groupBlock),
			httpx.ToAccountRowDTO(rows[1], noBlock),
		},
		Total: 9,
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("output\n got: %+v\nwant: %+v", out, want)
	}
	if !out.Accounts[0].Blocked || out.Accounts[0].BlockSource != "group" {
		t.Fatalf("member of a blocked group reported %+v, want blocked by the group", out.Accounts[0])
	}
	if text != "2 of 9 account(s)" {
		t.Fatalf("summary = %q", text)
	}
	_, empty, err := listAccountsHandler(&listSource{})(t.Context(), nil, listAccountsInput{})
	if err != nil || empty.Accounts == nil {
		t.Fatalf("empty page = %+v, %v; want a non-nil empty slice", empty, err)
	}
}

func TestListGroupsRows(t *testing.T) {
	// On the first page the core pins the synthetic default group first,
	// outside the limit and the total; the tool keeps it as returned.
	rows := []store.GroupListRow{
		{Group: domain.AccountGroup{Code: "", Title: "Default"}, AccountCount: 4},
		{Group: domain.AccountGroup{Code: "desk-a", Blocked: true, BlockReason: "desk halt"}, AccountCount: 1, PositionCount: 3, VenueAccess: true},
	}
	src := &listSource{groupRows: store.GroupListPage{Rows: rows, Total: 1}}
	text, out, err := listGroupsHandler(src)(t.Context(), nil, listGroupsInput{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := listGroupsOutput{
		Groups: []httpx.GroupDTO{httpx.ToGroupRowDTO(rows[0]), httpx.ToGroupRowDTO(rows[1])},
		Total:  1,
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("output\n got: %+v\nwant: %+v", out, want)
	}
	if text != "1 of 1 group(s), plus the default group" {
		t.Fatalf("summary = %q", text)
	}
	_, empty, err := listGroupsHandler(&listSource{})(t.Context(), nil, listGroupsInput{})
	if err != nil || empty.Groups == nil {
		t.Fatalf("empty page = %+v, %v; want a non-nil empty slice", empty, err)
	}
}

func TestListGroupsSummary(t *testing.T) {
	realRows := func(codes ...string) []store.GroupListRow {
		rows := make([]store.GroupListRow, 0, len(codes))
		for _, code := range codes {
			rows = append(rows, store.GroupListRow{Group: domain.AccountGroup{Code: code}})
		}
		return rows
	}
	defaultRow := store.GroupListRow{Group: domain.AccountGroup{Code: "", Title: "Default"}}
	tests := []struct {
		name string
		page store.GroupListPage
		want string
	}{
		{
			name: "first page with the default group",
			page: store.GroupListPage{
				Rows:  append([]store.GroupListRow{defaultRow}, realRows("a", "b", "c")...),
				Total: 3,
			},
			want: "3 of 3 group(s), plus the default group",
		},
		{
			name: "page without the default group",
			page: store.GroupListPage{Rows: realRows("d", "e"), Total: 5},
			want: "2 of 5 group(s)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &listSource{groupRows: tt.page}
			text, _, err := listGroupsHandler(src)(t.Context(), nil, listGroupsInput{})
			if err != nil {
				t.Fatal(err)
			}
			if text != tt.want {
				t.Fatalf("summary = %q, want %q", text, tt.want)
			}
		})
	}
}
