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

// List tests for the mandatory account restriction: every list query applies
// it in SQL before the total and the page are taken, keeps rows with no
// account only where the table has them and IncludeUnowned asks for them, and
// leaves the caller's own filters narrowing on top.

package sqlite

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

// restrictA restricts a list to account a; restrictAUnowned also keeps the
// rows that belong to no account.
var (
	restrictA        = domain.AccountRestriction{Account: "a"}
	restrictAUnowned = domain.AccountRestriction{Account: "a", IncludeUnowned: true}
)

// newRestrictionStore opens a store holding accounts a and b, the assets AAPL
// and USD and the operator principal, the fixtures the restriction tests seed
// their rows against.
func newRestrictionStore(t *testing.T) (context.Context, fwstore.RealmStore) {
	t.Helper()
	ctx := context.Background()
	_, rs := newTestStore(t)
	for _, code := range []string{"AAPL", "USD"} {
		if _, err := rs.CreateAsset(ctx, domain.Asset{Code: code}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", code, err)
		}
	}
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	for _, code := range []domain.AccountID{"a", "b"} {
		if _, err := rs.CreateAccount(ctx, domain.Account{Code: code}); err != nil {
			t.Fatalf("CreateAccount(%s): %v", code, err)
		}
	}
	return ctx, rs
}

// assertListPage fails unless a list returned exactly want, in order, with
// the total wantTotal.
func assertListPage(t *testing.T, total int, got []string, wantTotal int, want []string) {
	t.Helper()
	if total != wantTotal || !slices.Equal(got, want) {
		t.Fatalf("page = total %d rows %q, want total %d rows %q", total, got, wantTotal, want)
	}
}

func TestListAccountRowsAccountRestriction(t *testing.T) {
	ctx, rs := newRestrictionStore(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "c"}); err != nil {
		t.Fatalf("CreateAccount(c): %v", err)
	}
	// An account is one row, so the restricted set holds a single row and the
	// page after it is empty while the total still counts it.
	for _, tc := range []struct {
		name      string
		filter    fwstore.AccountListFilter
		wantTotal int
		want      []string
	}{
		{
			name:      "zero restriction lists every account",
			wantTotal: 3,
			want:      []string{"a", "b", "c"},
		},
		{
			name:      "restricted to a",
			filter:    fwstore.AccountListFilter{Restriction: restrictA},
			wantTotal: 1,
			want:      []string{"a"},
		},
		{
			name: "page past the restricted set",
			filter: fwstore.AccountListFilter{
				Restriction: restrictA, Page: fwstore.PageSpec{Limit: 1, Offset: 1},
			},
			wantTotal: 1,
			want:      []string{},
		},
		{
			name:      "include unowned adds no account",
			filter:    fwstore.AccountListFilter{Restriction: restrictAUnowned},
			wantTotal: 1,
			want:      []string{"a"},
		},
		{
			name: "code filter narrows on top",
			filter: fwstore.AccountListFilter{
				Restriction: restrictA, Code: fwstore.ExactTextMatcher("b"),
			},
			wantTotal: 0,
			want:      []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := rs.ListAccountRows(ctx, tc.filter)
			if err != nil {
				t.Fatalf("ListAccountRows: %v", err)
			}
			got := make([]string, 0, len(page.Rows))
			for _, row := range page.Rows {
				got = append(got, row.Account.Code.String())
			}
			assertListPage(t, page.Total, got, tc.wantTotal, tc.want)
		})
	}
}

func TestListOrderRowsAccountRestriction(t *testing.T) {
	ctx, rs := newRestrictionStore(t)
	// Interleaved, so the restricted rows are not one contiguous id range.
	for _, seed := range []struct {
		account domain.AccountID
		amount  string
	}{{"a", "1"}, {"b", "10"}, {"a", "2"}, {"b", "20"}, {"a", "3"}} {
		order := sampleOrder()
		order.Account = seed.account
		order.AmountValue = seed.amount
		if _, err := rs.CreateOrder(ctx, order); err != nil {
			t.Fatalf("CreateOrder(%s %s): %v", seed.account, seed.amount, err)
		}
	}
	byAmount := fwstore.SortSpec{Column: "amountValue"}
	for _, tc := range []struct {
		name      string
		filter    fwstore.OrderListFilter
		wantTotal int
		want      []string
	}{
		{
			name:      "zero restriction lists every account",
			filter:    fwstore.OrderListFilter{Sort: byAmount},
			wantTotal: 5,
			want:      []string{"1", "2", "3", "10", "20"},
		},
		{
			name:      "restricted to a",
			filter:    fwstore.OrderListFilter{Restriction: restrictA, Sort: byAmount},
			wantTotal: 3,
			want:      []string{"1", "2", "3"},
		},
		{
			name: "page inside the restricted set",
			filter: fwstore.OrderListFilter{
				Restriction: restrictA, Sort: byAmount,
				Page: fwstore.PageSpec{Limit: 2, Offset: 1},
			},
			wantTotal: 3,
			want:      []string{"2", "3"},
		},
		{
			name:      "include unowned adds no order",
			filter:    fwstore.OrderListFilter{Restriction: restrictAUnowned, Sort: byAmount},
			wantTotal: 3,
			want:      []string{"1", "2", "3"},
		},
		{
			name: "account filter narrows on top",
			filter: fwstore.OrderListFilter{
				Restriction: restrictA, Account: "b", Sort: byAmount,
			},
			wantTotal: 0,
			want:      []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := rs.ListOrderRows(ctx, tc.filter)
			if err != nil {
				t.Fatalf("ListOrderRows: %v", err)
			}
			got := make([]string, 0, len(page.Rows))
			for _, row := range page.Rows {
				got = append(got, row.Order.AmountValue)
			}
			assertListPage(t, page.Total, got, tc.wantTotal, tc.want)
		})
	}
}

func TestListTradeRowsAccountRestriction(t *testing.T) {
	ctx, rs := newRestrictionStore(t)
	orders := make(map[domain.AccountID]domain.ExternalID)
	for _, account := range []domain.AccountID{"a", "b"} {
		order := sampleOrder()
		order.Account = account
		created, err := rs.CreateOrder(ctx, order)
		if err != nil {
			t.Fatalf("CreateOrder(%s): %v", account, err)
		}
		orders[account] = created.ExternalID
	}
	for _, seed := range []struct {
		account  domain.AccountID
		quantity string
	}{{"a", "1"}, {"b", "10"}, {"a", "2"}, {"b", "20"}, {"a", "3"}} {
		if _, err := rs.CreateTrade(ctx, domain.Trade{
			Order: orders[seed.account], Account: seed.account,
			BaseAsset: "AAPL", QuoteAsset: "USD", Principal: "operator",
			Source: domain.SourcePanel, Side: domain.OrderSideBuy,
			Quantity: seed.quantity, Price: "1", LockPrice: "1",
		}); err != nil {
			t.Fatalf("CreateTrade(%s %s): %v", seed.account, seed.quantity, err)
		}
	}
	byQuantity := fwstore.SortSpec{Column: "quantity"}
	for _, tc := range []struct {
		name      string
		filter    fwstore.TradeListFilter
		wantTotal int
		want      []string
	}{
		{
			name:      "zero restriction lists every account",
			filter:    fwstore.TradeListFilter{Sort: byQuantity},
			wantTotal: 5,
			want:      []string{"1", "2", "3", "10", "20"},
		},
		{
			name:      "restricted to a",
			filter:    fwstore.TradeListFilter{Restriction: restrictA, Sort: byQuantity},
			wantTotal: 3,
			want:      []string{"1", "2", "3"},
		},
		{
			name: "page inside the restricted set",
			filter: fwstore.TradeListFilter{
				Restriction: restrictA, Sort: byQuantity,
				Page: fwstore.PageSpec{Limit: 2, Offset: 1},
			},
			wantTotal: 3,
			want:      []string{"2", "3"},
		},
		{
			name:      "include unowned adds no trade",
			filter:    fwstore.TradeListFilter{Restriction: restrictAUnowned, Sort: byQuantity},
			wantTotal: 3,
			want:      []string{"1", "2", "3"},
		},
		{
			name: "account filter narrows on top",
			filter: fwstore.TradeListFilter{
				Restriction: restrictA, Account: fwstore.ExactTextMatcher("b"),
				Sort: byQuantity,
			},
			wantTotal: 0,
			want:      []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := rs.ListTradeRows(ctx, tc.filter)
			if err != nil {
				t.Fatalf("ListTradeRows: %v", err)
			}
			got := make([]string, 0, len(page.Rows))
			for _, row := range page.Rows {
				got = append(got, row.Trade.Quantity)
			}
			assertListPage(t, page.Total, got, tc.wantTotal, tc.want)
		})
	}
}

func TestListBalanceRowsAccountRestriction(t *testing.T) {
	ctx, rs := newRestrictionStore(t)
	for _, code := range []string{"X1", "X2", "X3"} {
		if _, err := rs.CreateAsset(ctx, domain.Asset{Code: code}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", code, err)
		}
	}
	for _, seed := range []struct {
		account domain.AccountID
		asset   string
	}{{"a", "X1"}, {"b", "X1"}, {"a", "X2"}, {"b", "X2"}, {"a", "X3"}} {
		if err := rs.UpsertBalance(ctx, domain.Balance{
			Account: seed.account, Asset: seed.asset, Available: "1", RealizedPnl: "0",
		}); err != nil {
			t.Fatalf("UpsertBalance(%s %s): %v", seed.account, seed.asset, err)
		}
	}
	for _, tc := range []struct {
		name      string
		filter    fwstore.BalanceListFilter
		wantTotal int
		want      []string
	}{
		{
			name:      "zero restriction lists every account",
			wantTotal: 5,
			want:      []string{"a/X1", "a/X2", "a/X3", "b/X1", "b/X2"},
		},
		{
			name:      "restricted to a",
			filter:    fwstore.BalanceListFilter{Restriction: restrictA},
			wantTotal: 3,
			want:      []string{"a/X1", "a/X2", "a/X3"},
		},
		{
			name: "page inside the restricted set",
			filter: fwstore.BalanceListFilter{
				Restriction: restrictA, Page: fwstore.PageSpec{Limit: 2, Offset: 1},
			},
			wantTotal: 3,
			want:      []string{"a/X2", "a/X3"},
		},
		{
			name:      "include unowned adds no balance",
			filter:    fwstore.BalanceListFilter{Restriction: restrictAUnowned},
			wantTotal: 3,
			want:      []string{"a/X1", "a/X2", "a/X3"},
		},
		{
			name: "account filter narrows on top",
			filter: fwstore.BalanceListFilter{
				Restriction: restrictA, Account: fwstore.ExactTextMatcher("b"),
			},
			wantTotal: 0,
			want:      []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := rs.ListBalanceRows(ctx, tc.filter)
			if err != nil {
				t.Fatalf("ListBalanceRows: %v", err)
			}
			got := make([]string, 0, len(page.Rows))
			for _, row := range page.Rows {
				got = append(got, row.Balance.Account.String()+"/"+row.Balance.Asset)
			}
			assertListPage(t, page.Total, got, tc.wantTotal, tc.want)
		})
	}
}

func TestListAdjustmentRowsAccountRestriction(t *testing.T) {
	ctx, rs := newRestrictionStore(t)
	for _, seed := range []struct {
		account domain.AccountID
		id      domain.ExternalID
	}{{"a", "adj-a1"}, {"b", "adj-b1"}, {"a", "adj-a2"}, {"b", "adj-b2"}, {"a", "adj-a3"}} {
		rec := sampleAdjustment()
		rec.Account = seed.account
		rec.ExternalID = seed.id
		if _, err := rs.AppendAdjustment(ctx, rec); err != nil {
			t.Fatalf("AppendAdjustment(%s): %v", seed.id, err)
		}
	}
	// Ordered by account, then by append order.
	byAccount := fwstore.SortSpec{Column: "account"}
	for _, tc := range []struct {
		name      string
		filter    fwstore.AdjustmentListFilter
		wantTotal int
		want      []string
	}{
		{
			name:      "zero restriction lists every account",
			filter:    fwstore.AdjustmentListFilter{Sort: byAccount},
			wantTotal: 5,
			want:      []string{"adj-a1", "adj-a2", "adj-a3", "adj-b1", "adj-b2"},
		},
		{
			name:      "restricted to a",
			filter:    fwstore.AdjustmentListFilter{Restriction: restrictA, Sort: byAccount},
			wantTotal: 3,
			want:      []string{"adj-a1", "adj-a2", "adj-a3"},
		},
		{
			name: "page inside the restricted set",
			filter: fwstore.AdjustmentListFilter{
				Restriction: restrictA, Sort: byAccount,
				Page: fwstore.PageSpec{Limit: 2, Offset: 1},
			},
			wantTotal: 3,
			want:      []string{"adj-a2", "adj-a3"},
		},
		{
			name: "include unowned adds no adjustment",
			filter: fwstore.AdjustmentListFilter{
				Restriction: restrictAUnowned, Sort: byAccount,
			},
			wantTotal: 3,
			want:      []string{"adj-a1", "adj-a2", "adj-a3"},
		},
		{
			name: "account filter narrows on top",
			filter: fwstore.AdjustmentListFilter{
				Restriction: restrictA, Account: fwstore.ExactTextMatcher("b"),
				Sort: byAccount,
			},
			wantTotal: 0,
			want:      []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := rs.ListAdjustmentRows(ctx, tc.filter)
			if err != nil {
				t.Fatalf("ListAdjustmentRows: %v", err)
			}
			got := make([]string, 0, len(page.Rows))
			for _, row := range page.Rows {
				got = append(got, row.ExternalID.String())
			}
			assertListPage(t, page.Total, got, tc.wantTotal, tc.want)
		})
	}
}

func TestListPolicyRowsAccountRestriction(t *testing.T) {
	ctx, rs := newRestrictionStore(t)
	// Account a is a member of desk; ops is b's group, which a is not in.
	for code, member := range map[string]domain.AccountID{"desk": "a", "ops": "b"} {
		if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: code}); err != nil {
			t.Fatalf("CreateGroup(%s): %v", code, err)
		}
		if err := rs.SetAccountGroup(ctx, member, code); err != nil {
			t.Fatalf("SetAccountGroup(%s, %s): %v", member, code, err)
		}
	}
	for _, limit := range []domain.LimitRate{
		{Scope: domain.ScopeAccount, Account: "a", MaxOrders: 1, Window: time.Minute},
		{Scope: domain.ScopeAccount, Account: "b", MaxOrders: 2, Window: time.Minute},
		{Scope: domain.ScopeBroker, MaxOrders: 3, Window: time.Minute},
	} {
		if err := rs.PutRateLimit(ctx, limit); err != nil {
			t.Fatalf("PutRateLimit(%s %s): %v", limit.Scope, limit.Account, err)
		}
	}
	if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeAccountUnderlyingAsset, Account: "a", Asset: "AAPL",
		MaxQuantity: "500",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit(a): %v", err)
	}
	for _, limit := range []domain.LimitSpotFundsPnlBounds{
		{Scope: domain.ScopeAccount, Account: "a", Currency: "USD", LowerBound: "-1"},
		{Scope: domain.ScopeAccountGroup, AccountGroup: "desk", Currency: "USD", LowerBound: "-2"},
		{Scope: domain.ScopeAccountGroup, AccountGroup: "ops", Currency: "USD", LowerBound: "-4"},
		{Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-3"},
	} {
		if err := rs.PutSpotFundsPnlBoundsLimit(ctx, limit); err != nil {
			t.Fatalf("PutSpotFundsPnlBoundsLimit(%s): %v", limit.Scope, err)
		}
	}
	// Keys are kind|scope|account|group in the default order: by kind, then
	// scope, account and group.
	const (
		sizeA       = "order_size_limit|account_underlying_asset|a|"
		rateA       = "rate_limit|account|a|"
		rateB       = "rate_limit|account|b|"
		rateBroker  = "rate_limit|broker||"
		boundsA     = "spot_funds_pnl_bounds_kill_switch|account|a|"
		boundsDesk  = "spot_funds_pnl_bounds_kill_switch|account_group||desk"
		boundsOps   = "spot_funds_pnl_bounds_kill_switch|account_group||ops"
		boundsGlobe = "spot_funds_pnl_bounds_kill_switch|global||"
	)
	for _, tc := range []struct {
		name      string
		filter    fwstore.PolicyListFilter
		wantTotal int
		want      []string
	}{
		{
			name:      "zero restriction lists every barrier",
			wantTotal: 8,
			want: []string{
				sizeA, rateA, rateB, rateBroker, boundsA, boundsDesk, boundsOps, boundsGlobe,
			},
		},
		{
			name:      "restricted to a",
			filter:    fwstore.PolicyListFilter{Restriction: restrictA},
			wantTotal: 3,
			want:      []string{sizeA, rateA, boundsA},
		},
		{
			name: "page inside the restricted set",
			filter: fwstore.PolicyListFilter{
				Restriction: restrictA, Page: fwstore.PageSpec{Limit: 2, Offset: 1},
			},
			wantTotal: 3,
			want:      []string{rateA, boundsA},
		},
		{
			name:      "include unowned adds account-less barriers except other groups'",
			filter:    fwstore.PolicyListFilter{Restriction: restrictAUnowned},
			wantTotal: 6,
			want:      []string{sizeA, rateA, rateBroker, boundsA, boundsDesk, boundsGlobe},
		},
		{
			name: "page inside the set with unowned barriers",
			filter: fwstore.PolicyListFilter{
				Restriction: restrictAUnowned, Page: fwstore.PageSpec{Limit: 2, Offset: 2},
			},
			wantTotal: 6,
			want:      []string{rateBroker, boundsA},
		},
		{
			name: "account filter narrows on top",
			filter: fwstore.PolicyListFilter{
				Restriction: restrictAUnowned, Account: fwstore.ExactTextMatcher("b"),
			},
			wantTotal: 0,
			want:      []string{},
		},
		{
			name: "group filter narrows on top",
			filter: fwstore.PolicyListFilter{
				Restriction: restrictAUnowned, AccountGroup: fwstore.ExactTextMatcher("desk"),
			},
			wantTotal: 1,
			want:      []string{boundsDesk},
		},
		{
			name: "barrier of a group a is not in stays out",
			filter: fwstore.PolicyListFilter{
				Restriction: restrictAUnowned, AccountGroup: fwstore.ExactTextMatcher("ops"),
			},
			wantTotal: 0,
			want:      []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := rs.ListPolicyRows(ctx, tc.filter)
			if err != nil {
				t.Fatalf("ListPolicyRows: %v", err)
			}
			got := make([]string, 0, len(page.Rows))
			for _, row := range page.Rows {
				got = append(got, string(row.Kind)+"|"+string(row.Scope)+"|"+
					row.Account.String()+"|"+row.AccountGroup)
			}
			assertListPage(t, page.Total, got, tc.wantTotal, tc.want)
		})
	}
}

// seedRestrictionAudit appends audit rows of accounts a and b and rows
// recorded with no account, interleaved; each row's detail names it. Newest
// first they read: none-2, a-3, b-2, a-2, none-1, b-1, a-1.
func seedRestrictionAudit(t *testing.T, ctx context.Context, rs fwstore.RealmStore) {
	t.Helper()
	for _, entry := range []fwstore.AuditEntry{
		{Action: domain.AuditActionBlock, Account: "a", Detail: "a-1"},
		{Action: domain.AuditActionBlock, Account: "b", Detail: "b-1"},
		{Action: domain.AuditActionCreateAsset, Asset: "AAPL", Detail: "none-1"},
		{Action: domain.AuditActionUnblock, Account: "a", Detail: "a-2"},
		{Action: domain.AuditActionUnblock, Account: "b", Detail: "b-2"},
		{Action: domain.AuditActionBlock, Account: "a", Detail: "a-3"},
		{Action: domain.AuditActionUpdateAsset, Asset: "AAPL", Detail: "none-2"},
	} {
		entry.Source = domain.SourcePanel
		if err := rs.AppendAudit(ctx, entry); err != nil {
			t.Fatalf("AppendAudit(%s): %v", entry.Detail, err)
		}
	}
}

func auditDetails(rows []domain.AuditRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Detail)
	}
	return out
}

func TestListAuditRowsAccountRestriction(t *testing.T) {
	ctx, rs := newRestrictionStore(t)
	seedRestrictionAudit(t, ctx, rs)
	for _, tc := range []struct {
		name      string
		filter    fwstore.AuditListFilter
		wantTotal int
		want      []string
	}{
		{
			name:      "zero restriction lists every row",
			wantTotal: 7,
			want:      []string{"none-2", "a-3", "b-2", "a-2", "none-1", "b-1", "a-1"},
		},
		{
			name:      "restricted to a",
			filter:    fwstore.AuditListFilter{Restriction: restrictA},
			wantTotal: 3,
			want:      []string{"a-3", "a-2", "a-1"},
		},
		{
			name: "page inside the restricted set",
			filter: fwstore.AuditListFilter{
				Restriction: restrictA, Page: fwstore.PageSpec{Limit: 2, Offset: 1},
			},
			wantTotal: 3,
			want:      []string{"a-2", "a-1"},
		},
		{
			name:      "include unowned adds the rows with no account",
			filter:    fwstore.AuditListFilter{Restriction: restrictAUnowned},
			wantTotal: 5,
			want:      []string{"none-2", "a-3", "a-2", "none-1", "a-1"},
		},
		{
			name: "page inside the set with unowned rows",
			filter: fwstore.AuditListFilter{
				Restriction: restrictAUnowned, Page: fwstore.PageSpec{Limit: 2, Offset: 1},
			},
			wantTotal: 5,
			want:      []string{"a-3", "a-2"},
		},
		{
			name: "account filter narrows on top",
			filter: fwstore.AuditListFilter{
				Restriction: restrictAUnowned, Account: fwstore.ExactTextMatcher("b"),
			},
			wantTotal: 0,
			want:      []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := rs.ListAuditRows(ctx, tc.filter)
			if err != nil {
				t.Fatalf("ListAuditRows: %v", err)
			}
			assertListPage(t, page.Total, auditDetails(page.Rows), tc.wantTotal, tc.want)
		})
	}
}

// TestListAuditFilteredAccountRestriction covers the count-bounded audit
// listing: n bounds the restricted set, not a window taken before it.
func TestListAuditFilteredAccountRestriction(t *testing.T) {
	ctx, rs := newRestrictionStore(t)
	seedRestrictionAudit(t, ctx, rs)
	for _, tc := range []struct {
		name   string
		filter domain.AuditFilter
		n      int
		want   []string
	}{
		{
			name: "zero restriction lists every row",
			n:    10,
			want: []string{"none-2", "a-3", "b-2", "a-2", "none-1", "b-1", "a-1"},
		},
		{
			name:   "restricted to a",
			filter: domain.AuditFilter{Restriction: restrictA},
			n:      10,
			want:   []string{"a-3", "a-2", "a-1"},
		},
		{
			name:   "count bounds the restricted set",
			filter: domain.AuditFilter{Restriction: restrictA},
			n:      2,
			want:   []string{"a-3", "a-2"},
		},
		{
			name:   "include unowned adds the rows with no account",
			filter: domain.AuditFilter{Restriction: restrictAUnowned},
			n:      10,
			want:   []string{"none-2", "a-3", "a-2", "none-1", "a-1"},
		},
		{
			name:   "count bounds the set with unowned rows",
			filter: domain.AuditFilter{Restriction: restrictAUnowned},
			n:      3,
			want:   []string{"none-2", "a-3", "a-2"},
		},
		{
			name:   "account filter narrows on top",
			filter: domain.AuditFilter{Restriction: restrictAUnowned, Account: "b"},
			n:      10,
			want:   []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := rs.ListAuditFiltered(ctx, tc.filter, tc.n)
			if err != nil {
				t.Fatalf("ListAuditFiltered: %v", err)
			}
			if got := auditDetails(rows); !slices.Equal(got, tc.want) {
				t.Fatalf("rows = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestListGroupRowsAccountRestriction covers the group list under the account
// restriction: only the groups the restricted account is a member of remain,
// their account and position counts count that member alone, also where the
// count filters read them, and the synthetic default group is omitted, so a
// member of no group lists nothing.
func TestListGroupRowsAccountRestriction(t *testing.T) {
	ctx, rs := newRestrictionStore(t)
	for _, code := range []string{"desk", "empty", "ops"} {
		if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: code}); err != nil {
			t.Fatalf("CreateGroup(%s): %v", code, err)
		}
	}
	for _, account := range []domain.Account{{Code: "c", GroupCode: "desk"}, {Code: "d"}} {
		if _, err := rs.CreateAccount(ctx, account); err != nil {
			t.Fatalf("CreateAccount(%s): %v", account.Code, err)
		}
	}
	for account, group := range map[domain.AccountID]string{"a": "desk", "b": "ops"} {
		if err := rs.SetAccountGroup(ctx, account, group); err != nil {
			t.Fatalf("SetAccountGroup(%s): %v", account, err)
		}
	}
	// a holds two positions, every other account one.
	for _, position := range []struct {
		account domain.AccountID
		asset   string
	}{{"a", "AAPL"}, {"a", "USD"}, {"b", "AAPL"}, {"c", "AAPL"}, {"d", "AAPL"}} {
		if err := rs.UpsertBalance(ctx, domain.Balance{
			Account: position.account, Asset: position.asset, UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("UpsertBalance(%s %s): %v", position.account, position.asset, err)
		}
	}
	two := 2
	// Rows read code:accounts:positions; the default group's code is empty.
	for _, tc := range []struct {
		name      string
		filter    fwstore.GroupListFilter
		wantTotal int
		want      []string
	}{
		{
			name:      "zero restriction lists every group and the default group",
			wantTotal: 3,
			want:      []string{":1:1", "desk:2:3", "empty:0:0", "ops:1:1"},
		},
		{
			name:      "restricted to a lists its group counting a alone",
			filter:    fwstore.GroupListFilter{Restriction: restrictA},
			wantTotal: 1,
			want:      []string{"desk:1:2"},
		},
		{
			name:      "include unowned adds no group",
			filter:    fwstore.GroupListFilter{Restriction: restrictAUnowned},
			wantTotal: 1,
			want:      []string{"desk:1:2"},
		},
		{
			name: "member of no group lists nothing",
			filter: fwstore.GroupListFilter{
				Restriction: domain.AccountRestriction{Account: "d"},
			},
			wantTotal: 0,
			want:      []string{},
		},
		{
			name: "account count filter reads the restricted count",
			filter: fwstore.GroupListFilter{
				Restriction: restrictA, Account: fwstore.CountRangeFilter{Equal: &two},
			},
			wantTotal: 0,
			want:      []string{},
		},
		{
			name: "page past the restricted set",
			filter: fwstore.GroupListFilter{
				Restriction: restrictA, Page: fwstore.PageSpec{Limit: 1, Offset: 1},
			},
			wantTotal: 1,
			want:      []string{},
		},
		{
			name: "code filter narrows on top",
			filter: fwstore.GroupListFilter{
				Restriction: restrictA, Code: fwstore.ExactTextMatcher("ops"),
			},
			wantTotal: 0,
			want:      []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := rs.ListGroupRows(ctx, tc.filter)
			if err != nil {
				t.Fatalf("ListGroupRows: %v", err)
			}
			got := make([]string, 0, len(page.Rows))
			for _, row := range page.Rows {
				got = append(got, fmt.Sprintf(
					"%s:%d:%d", row.Group.Code, row.AccountCount, row.PositionCount,
				))
			}
			assertListPage(t, page.Total, got, tc.wantTotal, tc.want)
		})
	}
}

// TestCountOrdersAccountRestriction covers the overview order counts under the
// account restriction: the zero restriction counts every order as before, and
// a restriction counts only the restricted account's orders, in the total, the
// working set and the orders since a boundary alike.
func TestCountOrdersAccountRestriction(t *testing.T) {
	ctx, rs := newRestrictionStore(t)
	boundary := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	before, after := boundary.Add(-time.Hour), boundary.Add(time.Hour)
	for _, seed := range []struct {
		account domain.AccountID
		status  domain.OrderStatus
		at      time.Time
	}{
		{"a", domain.OrderStatusAccepted, before},
		{"b", domain.OrderStatusAccepted, before},
		{"a", domain.OrderStatusFilled, before},
		{"b", domain.OrderStatusRejected, before},
		{"a", domain.OrderStatusAccepted, after},
		{"b", domain.OrderStatusSubmitted, after},
		{"b", domain.OrderStatusCancelled, after},
	} {
		order := sampleOrder()
		order.Account = seed.account
		order.Status = seed.status
		created, err := rs.CreateOrder(ctx, order)
		if err != nil {
			t.Fatalf("CreateOrder(%s %s): %v", seed.account, seed.status, err)
		}
		if _, err := rs.(*realmStore).rawDB().ExecContext(
			ctx, `UPDATE order_record SET at = ? WHERE external_id = ?`,
			timeStr(seed.at), created.ExternalID.Bytes(),
		); err != nil {
			t.Fatalf("set order time: %v", err)
		}
	}
	for _, tc := range []struct {
		name                 string
		restriction          domain.AccountRestriction
		total, active, since int
	}{
		{name: "zero restriction counts every order", total: 7, active: 4, since: 3},
		{
			name: "restricted to a", restriction: restrictA,
			total: 3, active: 2, since: 1,
		},
		{
			name: "include unowned adds no order", restriction: restrictAUnowned,
			total: 3, active: 2, since: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total, err := rs.CountOrders(ctx, tc.restriction)
			if err != nil || total != tc.total {
				t.Fatalf("CountOrders = %d, %v; want %d", total, err, tc.total)
			}
			active, err := rs.CountActiveOrders(ctx, tc.restriction)
			if err != nil || active != tc.active {
				t.Fatalf("CountActiveOrders = %d, %v; want %d", active, err, tc.active)
			}
			since, err := rs.CountOrdersSince(ctx, boundary, tc.restriction)
			if err != nil || since != tc.since {
				t.Fatalf("CountOrdersSince = %d, %v; want %d", since, err, tc.since)
			}
		})
	}
}
