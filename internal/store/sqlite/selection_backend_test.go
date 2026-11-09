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

package sqlite

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	fwstore "go.openpit.dev/officer/framework/store"
)

type selectionScope struct {
	backend.EveryAccount
	restriction domain.AccountRestriction
}

func (s selectionScope) List(domain.Caller, domain.AccountID) (domain.AccountRestriction, error) {
	return s.restriction, nil
}

type selectionNode struct {
	node.Node
	realm fwstore.RealmStore
}

func (n selectionNode) ListTrades(ctx context.Context, account domain.AccountID, source domain.Source, limit int) ([]domain.Trade, error) {
	return n.realm.ListTrades(ctx, account, source, limit)
}

func (n selectionNode) ListAllTrades(ctx context.Context, account domain.AccountID, source domain.Source) ([]domain.Trade, error) {
	return n.realm.ListAllTrades(ctx, account, source)
}

func (n selectionNode) ListAllOrders(ctx context.Context, account domain.AccountID, source domain.Source) ([]domain.Order, error) {
	return n.realm.ListAllOrders(ctx, account, source)
}

func (n selectionNode) ListTradeRows(ctx context.Context, filter fwstore.TradeListFilter) (fwstore.TradeListPage, error) {
	return n.realm.ListTradeRows(ctx, filter)
}

func (n selectionNode) ListOrderRows(ctx context.Context, filter fwstore.OrderListFilter) (fwstore.OrderListPage, error) {
	return n.realm.ListOrderRows(ctx, filter)
}

func (n selectionNode) ListAccountRows(ctx context.Context, filter fwstore.AccountListFilter) (fwstore.AccountListPage, error) {
	return n.realm.ListAccountRows(ctx, filter)
}

func (n selectionNode) ListGroupRows(ctx context.Context, filter fwstore.GroupListFilter) (fwstore.GroupListPage, error) {
	return n.realm.ListGroupRows(ctx, filter)
}

func (n selectionNode) GetGroup(ctx context.Context, code string) (domain.AccountGroup, []domain.Account, bool, error) {
	group, found, err := n.realm.GetGroup(ctx, code)
	if err != nil || !found {
		return group, nil, found, err
	}
	page, err := n.realm.ListAccountRows(ctx, fwstore.AccountListFilter{GroupCode: &code})
	accounts := make([]domain.Account, 0, len(page.Rows))
	for _, row := range page.Rows {
		accounts = append(accounts, row.Account)
	}
	return group, accounts, found, err
}

func selectionService(t *testing.T, realm fwstore.RealmStore, restriction domain.AccountRestriction) *backend.Service {
	t.Helper()
	service, err := backend.New(selectionNode{realm: realm}, nil, nil,
		func([]byte, domain.Order) (string, error) { return "", errors.New("no lock in this fixture") },
		backend.WithAccountScope(selectionScope{restriction: restriction}))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func seedSelectionOrders(t *testing.T, ctx context.Context, realm fwstore.RealmStore, accounts []domain.Account) {
	t.Helper()
	for i, account := range accounts {
		order := sampleOrder()
		order.Account = account.Code
		created, err := realm.CreateOrder(ctx, order)
		if err != nil {
			t.Fatal(err)
		}
		trade, err := realm.CreateTrade(ctx, domain.Trade{
			Order: created.ExternalID, Account: account.Code, BaseAsset: "AAPL", QuoteAsset: "USD",
			Source: domain.SourcePanel, Side: domain.OrderSideBuy, Quantity: "1", Price: "150.25",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := realm.(*realmStore).rawDB().ExecContext(ctx,
			`UPDATE trade SET at = ? WHERE external_id = ?`,
			timeStr(time.Date(2026, 1, 2, i, 0, 0, 0, time.UTC)), trade.ExternalID.Bytes()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSelectionLegacyLists(t *testing.T) {
	ctx, realm, accounts, groups := selectionFixture(t)
	ctx = auth.ContextWithCaller(ctx, domain.Caller{Source: domain.SourcePanel})
	seedSelectionOrders(t, ctx, realm, accounts)
	for _, tc := range []struct {
		name        string
		restriction domain.AccountRestriction
		visible     []domain.AccountID
	}{
		{"unrestricted", domain.AccountRestriction{}, []domain.AccountID{"a", "b", "c"}},
		{"unowned only", domain.AccountRestriction{IncludeUnowned: true}, []domain.AccountID{"a", "b", "c"}},
		{"account only", domain.AccountRestriction{Account: "a"}, []domain.AccountID{"a"}},
		{"selected account", domain.AccountRestriction{Selection: &domain.AccountSelection{Accounts: []domain.EngineAccountID{accounts[0].EngineAccountID}}}, []domain.AccountID{"a"}},
		{"selected group", domain.AccountRestriction{Selection: &domain.AccountSelection{AccountGroups: []domain.EngineGroupID{groups[0].EngineGroupID}}}, []domain.AccountID{"a", "b"}},
		{"empty selection", domain.AccountRestriction{Selection: &domain.AccountSelection{}}, nil},
		{"intersection", domain.AccountRestriction{Account: "b", Selection: &domain.AccountSelection{Accounts: []domain.EngineAccountID{accounts[0].EngineAccountID}}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := selectionService(t, realm, tc.restriction)
			for _, requested := range []domain.AccountID{"", "a", "b", "c"} {
				t.Run("account="+requested.String(), func(t *testing.T) {
					var want []domain.AccountID
					for _, code := range tc.visible {
						if requested == "" || code == requested {
							want = append(want, code)
						}
					}
					for _, source := range []domain.Source{"", domain.SourcePanel, domain.SourceMCP} {
						expected := want
						if source == domain.SourceMCP {
							expected = nil
						}
						orders, err := service.ListAllOrders(ctx, requested, source)
						var got []domain.AccountID
						for _, order := range orders {
							got = append(got, order.Account)
						}
						slices.Sort(got)
						if err != nil || !slices.Equal(got, expected) {
							t.Errorf("ListAllOrders(%q, %q) = %v, %v; want %v", requested, source, got, err, expected)
						}
						trades, err := service.ListAllTrades(ctx, requested, source)
						got = nil
						for _, trade := range trades {
							got = append(got, trade.Account)
						}
						slices.Sort(got)
						if err != nil || !slices.Equal(got, expected) {
							t.Errorf("ListAllTrades(%q, %q) = %v, %v; want %v", requested, source, got, err, expected)
						}
						trades, err = service.ListTrades(ctx, requested, source, 100)
						got = nil
						for _, trade := range trades {
							got = append(got, trade.Account)
						}
						slices.Sort(got)
						if err != nil || !slices.Equal(got, expected) {
							t.Errorf("ListTrades(%q, %q) = %v, %v; want %v", requested, source, got, err, expected)
						}
					}
				})
			}
		})
	}
}

func TestSelectionTradesBeforeLimit(t *testing.T) {
	ctx, realm, accounts, _ := selectionFixture(t)
	ctx = auth.ContextWithCaller(ctx, domain.Caller{Source: domain.SourcePanel})
	seedSelectionOrders(t, ctx, realm, accounts)
	service := selectionService(t, realm, domain.AccountRestriction{Selection: &domain.AccountSelection{
		Accounts: []domain.EngineAccountID{accounts[0].EngineAccountID},
	}})
	for _, limit := range []int{-1, 0, 1, 100} {
		trades, err := service.ListTrades(ctx, "", "", limit)
		want := 1
		if limit <= 0 {
			want = 0
		}
		if err != nil || len(trades) != want {
			t.Fatalf("ListTrades(limit=%d) = %v, %v; want %d rows", limit, trades, err, want)
		}
		if want > 0 && trades[0].Account != "a" {
			t.Fatalf("ListTrades(limit=%d) account = %q; want a", limit, trades[0].Account)
		}
	}
}

func TestSelectionGetGroupMembers(t *testing.T) {
	ctx, realm, accounts, groups := selectionFixture(t)
	ctx = auth.ContextWithCaller(ctx, domain.Caller{Source: domain.SourcePanel})
	alias, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: "alias", Title: groups[0].Code})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		restriction domain.AccountRestriction
		code        string
		want        []domain.AccountID
		absent      bool
	}{
		{"account member", domain.AccountRestriction{Selection: &domain.AccountSelection{Accounts: []domain.EngineAccountID{accounts[0].EngineAccountID}}}, groups[0].Code, []domain.AccountID{"a"}, false},
		{"group members", domain.AccountRestriction{Selection: &domain.AccountSelection{AccountGroups: []domain.EngineGroupID{groups[0].EngineGroupID}}}, groups[0].Code, []domain.AccountID{"a", "b"}, false},
		{"assigned empty group", domain.AccountRestriction{Selection: &domain.AccountSelection{AccountGroups: []domain.EngineGroupID{groups[2].EngineGroupID}}}, groups[2].Code, nil, false},
		{"invisible group", domain.AccountRestriction{Selection: &domain.AccountSelection{Accounts: []domain.EngineAccountID{accounts[0].EngineAccountID}}}, groups[1].Code, nil, true},
		{"empty selection", domain.AccountRestriction{Selection: &domain.AccountSelection{}}, groups[0].Code, nil, true},
		{"title is not code", domain.AccountRestriction{Selection: &domain.AccountSelection{AccountGroups: []domain.EngineGroupID{alias.EngineGroupID}}}, groups[0].Code, nil, true},
		{"intersection", domain.AccountRestriction{Account: "b", Selection: &domain.AccountSelection{Accounts: []domain.EngineAccountID{accounts[0].EngineAccountID}}}, groups[0].Code, nil, true},
		{"account only", domain.AccountRestriction{Account: "a"}, groups[0].Code, []domain.AccountID{"a"}, false},
		{"unrestricted", domain.AccountRestriction{}, groups[0].Code, []domain.AccountID{"a", "b"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := selectionService(t, realm, tc.restriction)
			group, members, err := service.GetGroup(ctx, tc.code)
			if tc.absent {
				if !errors.Is(err, domain.ErrNotFound) || group.Code != "" || len(members) != 0 {
					t.Fatalf("GetGroup = %v, %v, %v; want not found without members", group, members, err)
				}
				return
			}
			var got []domain.AccountID
			for _, member := range members {
				got = append(got, member.Code)
			}
			slices.Sort(got)
			if err != nil || group.Code != tc.code || !slices.Equal(got, tc.want) {
				t.Fatalf("GetGroup(%q) = %v, %v, %v; want %v", tc.code, group, got, err, tc.want)
			}
		})
	}
}

func TestSelectionOrderCounts(t *testing.T) {
	ctx, realm, accounts, groups := selectionFixture(t)
	seedSelectionOrders(t, ctx, realm, accounts)
	for _, tc := range []struct {
		name        string
		restriction domain.AccountRestriction
		want        int
	}{
		{"selected account", domain.AccountRestriction{Selection: &domain.AccountSelection{Accounts: []domain.EngineAccountID{accounts[0].EngineAccountID}}}, 1},
		{"selected group", domain.AccountRestriction{Selection: &domain.AccountSelection{AccountGroups: []domain.EngineGroupID{groups[0].EngineGroupID}}}, 2},
		{"empty selection", domain.AccountRestriction{Selection: &domain.AccountSelection{}}, 0},
		{"intersection", domain.AccountRestriction{Account: "b", Selection: &domain.AccountSelection{Accounts: []domain.EngineAccountID{accounts[0].EngineAccountID}}}, 0},
		{"unrestricted", domain.AccountRestriction{}, 3},
		{"unowned only", domain.AccountRestriction{IncludeUnowned: true}, 3},
		{"account only", domain.AccountRestriction{Account: "a"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total, err := realm.CountOrders(ctx, tc.restriction)
			if err != nil || total != tc.want {
				t.Errorf("CountOrders = %d, %v; want %d", total, err, tc.want)
			}
			active, err := realm.CountActiveOrders(ctx, tc.restriction)
			if err != nil || active != tc.want {
				t.Errorf("CountActiveOrders = %d, %v; want %d", active, err, tc.want)
			}
			since, err := realm.CountOrdersSince(ctx, time.Time{}, tc.restriction)
			if err != nil || since != tc.want {
				t.Errorf("CountOrdersSince = %d, %v; want %d", since, err, tc.want)
			}
			page, err := realm.ListOrderRows(ctx, fwstore.OrderListFilter{Restriction: tc.restriction, Page: fwstore.PageSpec{Limit: 1, Offset: 1}})
			rows := 0
			if tc.want > 1 {
				rows = 1
			}
			if err != nil || page.Total != tc.want || len(page.Rows) != rows {
				t.Errorf("ListOrderRows = total %d, rows %d, %v; want total %d, rows %d", page.Total, len(page.Rows), err, tc.want, rows)
			}
		})
	}
}
