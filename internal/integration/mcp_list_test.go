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

package integration_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// listRowsSource serves one fixed page from each of the four list reads,
// counting the reads, over fakeSource, whose list stubs return zero pages.
type listRowsSource struct {
	*fakeSource
	reads    int
	orders   backend.OrderListPage
	trades   store.TradeListPage
	accounts store.AccountListPage
	groups   store.GroupListPage
}

func (s *listRowsSource) ListOrderRows(
	context.Context, store.OrderListFilter,
) (backend.OrderListPage, error) {
	s.reads++
	return s.orders, nil
}

func (s *listRowsSource) ListTradeRows(
	context.Context, store.TradeListFilter,
) (store.TradeListPage, error) {
	s.reads++
	return s.trades, nil
}

func (s *listRowsSource) ListAccountRows(
	context.Context, store.AccountListFilter,
) (store.AccountListPage, error) {
	s.reads++
	return s.accounts, nil
}

func (s *listRowsSource) ListGroupRows(
	context.Context, store.GroupListFilter,
) (store.GroupListPage, error) {
	s.reads++
	return s.groups, nil
}

func listRowsFixture() *listRowsSource {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return &listRowsSource{
		fakeSource: &fakeSource{},
		orders: backend.OrderListPage{Rows: []backend.OrderListRow{{
			Order: domain.Order{
				At: at, ExternalID: "order-1", Account: "acc-1", BaseAsset: "BTC",
				QuoteAsset: "USD", Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
				AmountValue: "1", Status: domain.OrderStatusSubmitted, Source: domain.SourceMCP,
			},
			DisplayPrice: "101.30", Signed: true, VenueConnection: "conn-1",
		}}, Total: 4},
		trades: store.TradeListPage{Rows: []store.TradeListRow{{
			Trade: domain.Trade{
				At: at, ExternalID: "trade-1", Order: "order-1", Account: "acc-1",
				BaseAsset: "BTC", QuoteAsset: "USD", Side: domain.OrderSideBuy,
				Quantity: "1", Price: "101.2", LockPrice: "101.3", Source: domain.SourceMCP,
			},
			VenueConnection: "conn-1",
		}}, Total: 2},
		accounts: store.AccountListPage{Rows: []store.AccountListRow{{
			Account: domain.Account{Code: "acc-1", Title: "Account One"}, PositionCount: 2, VenueAccess: true,
		}}, Total: 3},
		groups: store.GroupListPage{Rows: []store.GroupListRow{{
			Group: domain.AccountGroup{Code: "desk-a", Title: "Desk A"}, AccountCount: 1,
		}}, Total: 1},
	}
}

// jsonValue decodes wire JSON into a generic value, so two bodies compare by
// their keys and values whatever their key order.
func jsonValue(t *testing.T, wire []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(wire, &v); err != nil {
		t.Fatalf("decode %s: %v", wire, err)
	}
	return v
}

func TestListToolsOverMCP(t *testing.T) {
	src := listRowsFixture()
	noBlock := domain.AccountBlockState{Source: domain.AccountBlockSourceNone}
	for _, tc := range []struct {
		tool string
		rest map[string]any
	}{
		{"list_orders", map[string]any{
			"orders": []httpx.OrderDTO{httpx.ToOrderRowDTO(src.orders.Rows[0])}, "total": 4,
		}},
		{"list_trades", map[string]any{
			"trades": []httpx.TradeDTO{httpx.ToTradeRowDTO(src.trades.Rows[0])}, "total": 2,
		}},
		{"list_accounts", map[string]any{
			"accounts": []httpx.AccountDTO{httpx.ToAccountRowDTO(src.accounts.Rows[0], noBlock)}, "total": 3,
		}},
		{"list_groups", map[string]any{
			"groups": []httpx.GroupDTO{httpx.ToGroupRowDTO(src.groups.Rows[0])}, "total": 1,
		}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			res := callTool[map[string]any](t, src, tc.tool, map[string]any{})
			requireNotToolError(t, res.IsError)
			want, err := json.Marshal(tc.rest)
			if err != nil {
				t.Fatal(err)
			}
			// The structured content is the REST list body: the same list key,
			// the same row keys and values, and the same total.
			if !reflect.DeepEqual(jsonValue(t, res.Wire), jsonValue(t, want)) {
				t.Fatalf("%s structured content\n got: %s\nwant: %s", tc.tool, res.Wire, want)
			}
		})
	}

	t.Run("unknown-sort-column", func(t *testing.T) {
		src := listRowsFixture()
		res := callTool[map[string]any](t, src, "list_orders", map[string]any{
			"sort": map[string]any{"column": "nope"},
		})
		requireToolError(t, res.IsError)
		if text := textContent(res.Content); !strings.HasPrefix(text, "validation: ") {
			t.Fatalf("tool error = %q, want a validation error", text)
		}
		if src.reads != 0 {
			t.Fatalf("Source read %d time(s) after a rejected sort column", src.reads)
		}
	})
}

func TestListToolsSessionContract(t *testing.T) {
	src := listRowsFixture()
	ctx, cancel := context.WithTimeout(systemCtx(), 5*time.Second)
	defer cancel()
	session, closeSession := officerSession(t, ctx, src)
	defer closeSession()

	t.Run("tools-list", func(t *testing.T) {
		list, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		listed := make(map[string]bool, len(list.Tools))
		for _, tool := range list.Tools {
			listed[tool.Name] = true
		}
		for _, name := range []string{"list_orders", "list_trades", "list_accounts", "list_groups"} {
			if !listed[name] {
				t.Errorf("tools/list has no %s", name)
			}
		}
	})

	t.Run("limit-as-string", func(t *testing.T) {
		// A limit sent as the JSON string "10" never reaches the tool, so it
		// does not pass through toolError: the go-sdk (v0.2.0) decodes the
		// arguments into the typed tool input before the input schema is
		// even applied, the decode fails, and the call fails as a JSON-RPC
		// protocol error - CallTool returns a non-nil error such as
		// `calling "tools/call": unmarshaling: json: cannot unmarshal string
		// into Go struct field listOrdersInput.limit of type int` - not as a
		// tool result with IsError.
		res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
			Name:      "list_orders",
			Arguments: map[string]any{"limit": "10"},
		})
		if err == nil {
			t.Fatalf("string limit was accepted: %+v", res)
		}
		if !strings.Contains(err.Error(), "cannot unmarshal string") || !strings.Contains(err.Error(), "limit") {
			t.Fatalf("string limit error = %v, want the SDK's argument decoding error", err)
		}
		if src.reads != 0 {
			t.Fatalf("Source read %d time(s) for a string limit", src.reads)
		}
	})
}
