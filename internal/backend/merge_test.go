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

package backend_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/backend"
)

// twoNodeRouter routes every key to the first node and exposes both nodes to the
// aggregating All() so a merge test drives the cross-node path.
type twoNodeRouter struct {
	nodes []node.Node
}

func (r *twoNodeRouter) Route(node.Key) (node.Node, error) { return r.nodes[0], nil }
func (r *twoNodeRouter) All() []node.Node                  { return r.nodes }

func orderRow(id byte, amount string) store.OrderListRow {
	xid := domain.ExternalID(fmt.Sprintf("order-%d", id))
	return store.OrderListRow{Order: domain.Order{ExternalID: xid, AmountValue: amount}}
}

// TestListOrderRowsMergesNodesInGlobalNumericOrder proves the cross-node merge
// yields a single globally ordered page (numeric, not lexical), sums the
// per-node totals, and asks each node for Offset+Limit rows before merging.
func TestListOrderRowsMergesNodesInGlobalNumericOrder(t *testing.T) {
	nodeA := &fakeNode{orderRowsPage: store.OrderListPage{
		Rows:  []store.OrderListRow{orderRow(1, "2"), orderRow(2, "100")},
		Total: 5,
	}}
	nodeB := &fakeNode{orderRowsPage: store.OrderListPage{
		Rows:  []store.OrderListRow{orderRow(3, "9"), orderRow(4, "100.5")},
		Total: 7,
	}}
	svc, newErr := backend.New(&twoNodeRouter{nodes: []node.Node{nodeA, nodeB}}, nil, nil)
	if newErr != nil {
		t.Fatalf("New service: %v", newErr)
	}

	page, err := svc.ListOrderRows(context.Background(), store.OrderListFilter{
		Sort: store.SortSpec{Column: "amountValue"},
		Page: store.PageSpec{Limit: 3, Offset: 1},
	})
	if err != nil {
		t.Fatalf("ListOrderRows: %v", err)
	}

	// Total is summed across nodes, independent of the page window.
	if page.Total != 12 {
		t.Fatalf("total = %d, want 12 (5+7)", page.Total)
	}
	// Global numeric order is 2, 9, 100, 100.5; Offset 1 Limit 3 keeps 9,100,100.5
	// (a lexical sort would place "100" before "2").
	got := make([]string, 0, len(page.Rows))
	for _, row := range page.Rows {
		got = append(got, row.Order.AmountValue)
	}
	if want := "9,100,100.5"; strings.Join(got, ",") != want {
		t.Fatalf("merged amounts = %v, want %v", got, want)
	}

	// Each node was asked for Offset+Limit (= 4) rows before the merge, so the
	// merge has enough candidates to satisfy the requested window.
	for name, seen := range map[string]store.PageSpec{
		"A": nodeA.lastOrderFilter.Page,
		"B": nodeB.lastOrderFilter.Page,
	} {
		if seen.Limit != 4 || seen.Offset != 0 {
			t.Fatalf("node %s fetched page = %+v, want {Limit:4 Offset:0}", name, seen)
		}
	}
}

func balanceRow(account domain.AccountID, asset, available string) store.BalanceListRow {
	return store.BalanceListRow{Balance: domain.Balance{
		Account: account, Asset: asset, Available: available,
	}}
}

// TestListBalanceRowsMergesNodesInGlobalNumericOrder is the balances analogue:
// global numeric order across nodes, summed total, per-node Offset+Limit fetch.
func TestListBalanceRowsMergesNodesInGlobalNumericOrder(t *testing.T) {
	nodeA := &fakeNode{balanceRowsPage: store.BalanceListPage{
		Rows:  []store.BalanceListRow{balanceRow("acc-1", "AAPL", "2"), balanceRow("acc-1", "USD", "100")},
		Total: 2,
	}}
	nodeB := &fakeNode{balanceRowsPage: store.BalanceListPage{
		Rows:  []store.BalanceListRow{balanceRow("acc-2", "AAPL", "9"), balanceRow("acc-2", "USD", "100.5")},
		Total: 3,
	}}
	svc, newErr := backend.New(&twoNodeRouter{nodes: []node.Node{nodeA, nodeB}}, nil, nil)
	if newErr != nil {
		t.Fatalf("New service: %v", newErr)
	}

	page, err := svc.ListBalanceRows(context.Background(), store.BalanceListFilter{
		Sort: store.SortSpec{Column: "available"},
		Page: store.PageSpec{Limit: 3, Offset: 1},
	})
	if err != nil {
		t.Fatalf("ListBalanceRows: %v", err)
	}
	if page.Total != 5 {
		t.Fatalf("total = %d, want 5 (2+3)", page.Total)
	}
	got := make([]string, 0, len(page.Rows))
	for _, row := range page.Rows {
		got = append(got, row.Balance.Available)
	}
	if want := "9,100,100.5"; strings.Join(got, ",") != want {
		t.Fatalf("merged available = %v, want %v", got, want)
	}
	for name, seen := range map[string]store.PageSpec{
		"A": nodeA.lastBalanceFilter.Page,
		"B": nodeB.lastBalanceFilter.Page,
	} {
		if seen.Limit != 4 || seen.Offset != 0 {
			t.Fatalf("node %s fetched page = %+v, want {Limit:4 Offset:0}", name, seen)
		}
	}
}

func TestListBalanceRowsRejectsDenominatedRangeWithoutCurrency(t *testing.T) {
	nodeA := &fakeNode{}
	svc, newErr := backend.New(&twoNodeRouter{nodes: []node.Node{nodeA}}, nil, nil)
	if newErr != nil {
		t.Fatalf("New service: %v", newErr)
	}
	min := "50"
	_, err := svc.ListBalanceRows(context.Background(), store.BalanceListFilter{
		RealizedPnl: store.DenominatedDecimalRangeFilter{
			Range: store.DecimalRangeFilter{Min: &min},
		},
	})
	if !errors.Is(err, store.ErrCurrencyRequired) {
		t.Fatalf("ListBalanceRows invalid filter = %v, want ErrCurrencyRequired", err)
	}
	if !nodeA.lastBalanceFilter.RealizedPnl.Empty() {
		t.Fatalf("node received invalid filter: %+v", nodeA.lastBalanceFilter)
	}
}

func ratePolicyRow(account domain.AccountID) store.PolicyListRow {
	return store.PolicyListRow{
		Kind:    store.PolicyKindRate,
		Scope:   domain.ScopeAccount,
		Account: account,
		Rate:    &domain.LimitRate{Scope: domain.ScopeAccount, Account: account},
	}
}

func ratePolicyRowWithAsset(
	account domain.AccountID, asset string,
) store.PolicyListRow {
	return store.PolicyListRow{
		Kind:    store.PolicyKindRate,
		Scope:   domain.ScopeAccountAsset,
		Account: account,
		Asset:   asset,
		Rate: &domain.LimitRate{
			Scope: domain.ScopeAccountAsset, Account: account, Asset: asset,
		},
	}
}

func orderSizePolicyRow(account domain.AccountID, asset string) store.PolicyListRow {
	return store.PolicyListRow{
		Kind:    store.PolicyKindOrderSize,
		Scope:   domain.ScopeAccountUnderlyingAsset,
		Account: account,
		Asset:   asset,
		OrderSize: &domain.LimitOrderSize{
			Scope:   domain.ScopeAccountUnderlyingAsset,
			Account: account, Asset: asset, MaxQuantity: "1",
		},
	}
}

func spotFundsPnlBoundsPolicyRow(
	account domain.AccountID, currency string,
) store.PolicyListRow {
	return store.PolicyListRow{
		Kind:    store.PolicyKindSpotFundsPnlBounds,
		Scope:   domain.ScopeAccount,
		Account: account,
		SpotFundsPnlBounds: &domain.LimitSpotFundsPnlBounds{
			Scope: domain.ScopeAccount, Account: account, Currency: currency,
		},
	}
}

// TestListPolicyRowsMergesNodesInGlobalOrder proves the cross-shard policy merge
// flattens heterogeneous kinds from two nodes into one globally ordered page,
// sums the per-node totals, and asks each node for Offset+Limit rows.
func TestListPolicyRowsMergesNodesInGlobalOrder(t *testing.T) {
	// Each node returns its own rows pre-ordered by account; the global merge by
	// account must interleave the two nodes.
	nodeA := &fakeNode{policyRowsPage: store.PolicyListPage{
		Rows: []store.PolicyListRow{
			ratePolicyRow("acc-1"),
			orderSizePolicyRow("acc-3", "AAPL"),
		},
		Total: 4,
	}}
	nodeB := &fakeNode{policyRowsPage: store.PolicyListPage{
		Rows: []store.PolicyListRow{
			ratePolicyRow("acc-2"),
			orderSizePolicyRow("acc-4", "MSFT"),
		},
		Total: 6,
	}}
	svc, newErr := backend.New(&twoNodeRouter{nodes: []node.Node{nodeA, nodeB}}, nil, nil)
	if newErr != nil {
		t.Fatalf("New service: %v", newErr)
	}

	page, err := svc.ListPolicyRows(context.Background(), store.PolicyListFilter{
		Sort: store.SortSpec{Column: "account"},
		Page: store.PageSpec{Limit: 3, Offset: 1},
	})
	if err != nil {
		t.Fatalf("ListPolicyRows: %v", err)
	}

	// Total is summed across nodes, independent of the page window.
	if page.Total != 10 {
		t.Fatalf("total = %d, want 10 (4+6)", page.Total)
	}
	// Global account order is acc-1..acc-4; Offset 1 Limit 3 keeps acc-2..acc-4.
	got := make([]string, 0, len(page.Rows))
	for _, row := range page.Rows {
		got = append(got, string(row.Account))
	}
	if want := "acc-2,acc-3,acc-4"; strings.Join(got, ",") != want {
		t.Fatalf("merged accounts = %v, want %v", got, want)
	}
	// The mixed kinds survive the merge: acc-3 and acc-4 keep their order-size
	// payloads, acc-2 keeps its rate payload.
	if page.Rows[0].Rate == nil || page.Rows[1].OrderSize == nil || page.Rows[2].OrderSize == nil {
		t.Fatalf("merged kinds lost their typed payloads: %+v", page.Rows)
	}

	// Each node was asked for Offset+Limit (= 4) rows before the merge.
	for name, seen := range map[string]store.PageSpec{
		"A": nodeA.lastPolicyFilter.Page,
		"B": nodeB.lastPolicyFilter.Page,
	} {
		if seen.Limit != 4 || seen.Offset != 0 {
			t.Fatalf("node %s fetched page = %+v, want {Limit:4 Offset:0}", name, seen)
		}
	}
}

func TestListPolicyRowsMergesNodesByEffectiveAsset(t *testing.T) {
	rateCAD := ratePolicyRowWithAsset("rate-cad", "CAD")
	pnlUSD := spotFundsPnlBoundsPolicyRow("pnl-usd", "USD")
	orderZAR := orderSizePolicyRow("order-zar", "ZAR")

	for _, tc := range []struct {
		name        string
		descending  bool
		nodeARows   []store.PolicyListRow
		nodeAPrefix []store.PolicyListRow
		want        string
	}{
		{
			name:        "ascending",
			nodeARows:   []store.PolicyListRow{rateCAD, pnlUSD},
			nodeAPrefix: []store.PolicyListRow{rateCAD},
			want:        "rate-cad,pnl-usd,order-zar",
		},
		{
			name:        "descending",
			descending:  true,
			nodeARows:   []store.PolicyListRow{pnlUSD, rateCAD},
			nodeAPrefix: []store.PolicyListRow{pnlUSD},
			want:        "order-zar,pnl-usd,rate-cad",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodeA := &fakeNode{policyRowsPage: store.PolicyListPage{
				Rows: tc.nodeARows, Total: len(tc.nodeARows),
			}}
			nodeB := &fakeNode{policyRowsPage: store.PolicyListPage{
				Rows: []store.PolicyListRow{orderZAR}, Total: 1,
			}}
			svc, newErr := backend.New(
				&twoNodeRouter{nodes: []node.Node{nodeA, nodeB}}, nil, nil,
			)
			if newErr != nil {
				t.Fatalf("New service: %v", newErr)
			}
			filter := store.PolicyListFilter{
				Sort: store.SortSpec{Column: "asset", Descending: tc.descending},
			}

			unpaged, err := svc.ListPolicyRows(context.Background(), filter)
			if err != nil {
				t.Fatalf("ListPolicyRows unpaged: %v", err)
			}
			if unpaged.Total != 3 {
				t.Fatalf("unpaged total = %d, want 3", unpaged.Total)
			}
			if got := policyRowAccounts(unpaged.Rows); got != tc.want {
				t.Fatalf("unpaged order = %q, want %q", got, tc.want)
			}
			if unpaged.Rows[1].Asset != "" ||
				unpaged.Rows[1].SpotFundsPnlBounds == nil ||
				unpaged.Rows[1].SpotFundsPnlBounds.Currency != "USD" {
				t.Fatalf(
					"P&L barrier wire asset = %+v, want empty Asset and USD currency",
					unpaged.Rows[1],
				)
			}

			nodeA.policyRowsPage.Rows = tc.nodeAPrefix
			paged, err := svc.ListPolicyRows(
				context.Background(),
				store.PolicyListFilter{
					Sort: store.SortSpec{Column: "asset", Descending: tc.descending},
					Page: store.PageSpec{Limit: 1},
				},
			)
			if err != nil {
				t.Fatalf("ListPolicyRows paged: %v", err)
			}
			got := policyRowAccounts(paged.Rows)
			want := policyRowAccounts(unpaged.Rows[:1])
			if got != want {
				t.Fatalf("paged order = %q, want unpaged prefix %q", got, want)
			}
			seen := nodeA.lastPolicyFilter.Page
			if seen != (store.PageSpec{Limit: 1}) {
				t.Fatalf("node A page = %+v, want {Limit:1 Offset:0}", seen)
			}
		})
	}
}

func policyRowAccounts(rows []store.PolicyListRow) string {
	accounts := make([]string, 0, len(rows))
	for _, row := range rows {
		accounts = append(accounts, row.Account.String())
	}
	return strings.Join(accounts, ",")
}
