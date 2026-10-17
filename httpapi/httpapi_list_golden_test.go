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

package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// goldenFakeService populates every field of the account, group, order and
// trade row and detail DTOs that its type lets be non-zero, with two rows per
// list, so the golden bodies below pin the complete wire shape.
func goldenFakeService() *fakeService {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	groupA := domain.AccountGroup{
		Code:        "desk-a",
		Title:       "Desk A",
		Currency:    "EUR",
		Notes:       "desk a notes",
		BlockReason: "desk a halt",
		Blocked:     true,
	}
	groupB := domain.AccountGroup{
		Code:        "desk-b",
		Title:       "Desk B",
		Currency:    "CHF",
		Notes:       "desk b notes",
		BlockReason: "desk b halt",
		Blocked:     true,
	}
	// account1 is blocked only through its group, so its effective block comes
	// from the group tier; account2 is blocked on both tiers.
	account1 := domain.Account{
		Code:              "acc-1",
		Title:             "Account One",
		Pnl:               "125.50",
		PnlHaltReason:     domain.PnlHaltReasonMissingFx,
		Currency:          "USD",
		EffectiveCurrency: "USD",
		CurrencyOrigin:    "account",
		GroupCurrency:     "EUR",
		DefaultCurrency:   "GBP",
		GroupCode:         groupA.Code,
		Notes:             "account one notes",
	}
	account2 := domain.Account{
		Code:              "acc-2",
		Title:             "Account Two",
		Pnl:               "-7.25",
		PnlHaltReason:     domain.PnlHaltReasonMissingCostBasis,
		Currency:          "JPY",
		EffectiveCurrency: "JPY",
		CurrencyOrigin:    "account",
		GroupCurrency:     "CHF",
		DefaultCurrency:   "GBP",
		GroupCode:         groupB.Code,
		Notes:             "account two notes",
		BlockReason:       "manual stop",
		Blocked:           true,
	}
	order1 := domain.Order{
		At:          at,
		ExternalID:  extID("order-1"),
		AmountValue: "10",
		Leaves:      "4",
		Price:       "101.25",
		Principal:   "alice",
		BaseAsset:   "BTC",
		QuoteAsset:  "USD",
		CommissionSubtotals: []domain.Commission{
			{Amount: "0.75", Currency: "USD"},
			{Amount: "-0.01", Currency: "BNB"},
		},
		Account:    account1.Code,
		Source:     domain.SourceAPI,
		DropCopy:   true,
		Side:       domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity,
		Status:     domain.OrderStatusPartiallyFilled,
	}
	order2 := domain.Order{
		At:          at.Add(time.Minute),
		ExternalID:  extID("order-2"),
		AmountValue: "2500",
		Leaves:      "1",
		Price:       "3010.5",
		Principal:   "bob",
		BaseAsset:   "ETH",
		QuoteAsset:  "EUR",
		CommissionSubtotals: []domain.Commission{
			{Amount: "1.5", Currency: "EUR"},
			{Amount: "0.002", Currency: "ETH"},
		},
		Account:    account2.Code,
		Source:     domain.SourceMCP,
		DropCopy:   true,
		Side:       domain.OrderSideSell,
		AmountKind: domain.OrderAmountKindVolume,
		Status:     domain.OrderStatusSubmitted,
	}
	trade1 := domain.Trade{
		At:         at.Add(2 * time.Minute),
		ExternalID: extID("trade-1"),
		Order:      order1.ExternalID,
		Quantity:   "6",
		Price:      "101.2",
		LockPrice:  "101.3",
		Commission: &domain.Commission{Amount: "0.45", Currency: "USD"},
		Principal:  "alice",
		BaseAsset:  "BTC",
		QuoteAsset: "USD",
		Account:    account1.Code,
		Source:     domain.SourceAPI,
		Side:       domain.OrderSideBuy,
	}
	trade2 := domain.Trade{
		At:         at.Add(3 * time.Minute),
		ExternalID: extID("trade-2"),
		Order:      order1.ExternalID,
		Quantity:   "0.5",
		Price:      "101.4",
		LockPrice:  "101.35",
		Commission: &domain.Commission{Amount: "-0.01", Currency: "BNB"},
		Principal:  "carol",
		BaseAsset:  "BTC",
		QuoteAsset: "USD",
		Account:    account1.Code,
		Source:     domain.SourcePanel,
		Side:       domain.OrderSideSell,
	}
	return &fakeService{
		accounts: []domain.Account{account1, account2},
		accountRows: []store.AccountListRow{
			{Account: account1, PositionCount: 3, VenueAccess: true},
			{Account: account2, PositionCount: 5, VenueAccess: true},
		},
		groups: []domain.AccountGroup{groupA, groupB},
		groupRows: []store.GroupListRow{
			{Group: groupA, AccountCount: 1, PositionCount: 3, VenueAccess: true},
			{Group: groupB, AccountCount: 2, PositionCount: 7, VenueAccess: true},
		},
		orderPage: &backend.OrderListPage{
			Rows: []backend.OrderListRow{
				{
					Order:           order1,
					DisplayPrice:    "101.30",
					Signed:          true,
					VenueConnection: extID("venue-1"),
				},
				{
					Order:           order2,
					DisplayPrice:    "3010.75",
					Signed:          true,
					VenueConnection: extID("venue-2"),
				},
			},
			Total: 2,
		},
		tradePage: &store.TradeListPage{
			Rows: []store.TradeListRow{
				{Trade: trade1, VenueConnection: extID("venue-1")},
				{Trade: trade2, VenueConnection: extID("venue-2")},
			},
			Total: 2,
		},
		orderDetail: domain.OrderDetail{
			Order:        order1,
			DisplayPrice: "101.30",
			Events: []domain.OrderEvent{
				{
					At:         at,
					ExternalID: extID("event-1"),
					Order:      order1.ExternalID,
					Attestation: &domain.EventAttestation{
						SignedEnvelope: "signed-envelope",
						KeyID:          "key-1",
						Alg:            "ed25519",
						RequestType:    domain.AttestationRequestSubmit,
						Mode:           "hold",
						IssuedAt:       "2026-01-02T03:04:05Z",
					},
					Principal: "alice",
					Source:    domain.SourceAPI,
					Type:      domain.OrderEventSubmitted,
				},
			},
			Trades: []domain.Trade{trade1, trade2},
		},
	}
}

// TestListAndDetailResponseBytes pins the exact response bytes of the account,
// group, order and trade list reads and of the account, group and order detail
// reads, so a refactor beneath them is provably byte-identical on the wire. The
// account-list error cases pin the order of its two reads (rows, then groups,
// even for an empty page) and that both errors reach the response unwrapped.
func TestListAndDetailResponseBytes(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		fake   func() *fakeService
		status int
		want   string
	}{
		{
			name:   "account list",
			path:   "/api/v1/accounts",
			fake:   goldenFakeService,
			status: http.StatusOK,
			want:   `{"accounts":[{"code":"acc-1","title":"Account One","pnl":"125.50","pnlHaltReason":"missing_fx","group":"desk-a","currency":"USD","effectiveCurrency":"USD","currencyOrigin":"account","currencyCascade":{"account":"USD","group":"EUR","default":"GBP"},"notes":"account one notes","positionCount":3,"venueAccess":true,"blockReason":"desk a halt","blockSource":"group","accountBlockReason":"","groupBlockReason":"desk a halt","blocked":true,"accountBlocked":false,"groupBlocked":true},{"code":"acc-2","title":"Account Two","pnl":"-7.25","pnlHaltReason":"missing_cost_basis","group":"desk-b","currency":"JPY","effectiveCurrency":"JPY","currencyOrigin":"account","currencyCascade":{"account":"JPY","group":"CHF","default":"GBP"},"notes":"account two notes","positionCount":5,"venueAccess":true,"blockReason":"manual stop","blockSource":"account","accountBlockReason":"manual stop","groupBlockReason":"desk b halt","blocked":true,"accountBlocked":true,"groupBlocked":true}],"total":2}` + "\n",
		},
		{
			name:   "group list",
			path:   "/api/v1/groups",
			fake:   goldenFakeService,
			status: http.StatusOK,
			want:   `{"groups":[{"code":"desk-a","title":"Desk A","currency":"EUR","notes":"desk a notes","accountCount":1,"positionCount":3,"venueAccess":true,"blockReason":"desk a halt","blocked":true},{"code":"desk-b","title":"Desk B","currency":"CHF","notes":"desk b notes","accountCount":2,"positionCount":7,"venueAccess":true,"blockReason":"desk b halt","blocked":true}],"total":2}` + "\n",
		},
		{
			name:   "order list",
			path:   "/api/v1/orders",
			fake:   goldenFakeService,
			status: http.StatusOK,
			want:   `{"orders":[{"at":"2026-01-02T03:04:05Z","id":"b3JkZXItMQAAAAAAAAAAAA","account":"acc-1","principal":"alice","baseAsset":"BTC","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"10","commissionSubtotals":[{"amount":"0.75","currency":"USD"},{"amount":"-0.01","currency":"BNB"}],"leavesQuantity":"4","price":"101.25","status":"partially_filled","source":"api","displayPrice":"101.30","dropCopy":true,"signed":true,"venueConnection":"dmVudWUtMQAAAAAAAAAAAA"},{"at":"2026-01-02T03:05:05Z","id":"b3JkZXItMgAAAAAAAAAAAA","account":"acc-2","principal":"bob","baseAsset":"ETH","quoteAsset":"EUR","side":"sell","amountKind":"volume","amountValue":"2500","commissionSubtotals":[{"amount":"1.5","currency":"EUR"},{"amount":"0.002","currency":"ETH"}],"leavesQuantity":"1","price":"3010.5","status":"submitted","source":"mcp","displayPrice":"3010.75","dropCopy":true,"signed":true,"venueConnection":"dmVudWUtMgAAAAAAAAAAAA"}],"total":2}` + "\n",
		},
		{
			name:   "trade list",
			path:   "/api/v1/trades",
			fake:   goldenFakeService,
			status: http.StatusOK,
			want:   `{"total":2,"trades":[{"at":"2026-01-02T03:06:05Z","id":"dHJhZGUtMQAAAAAAAAAAAA","order":"b3JkZXItMQAAAAAAAAAAAA","account":"acc-1","principal":"alice","baseAsset":"BTC","quoteAsset":"USD","side":"buy","quantity":"6","price":"101.2","lockPrice":"101.3","commission":{"amount":"0.45","currency":"USD"},"source":"api","venueConnection":"dmVudWUtMQAAAAAAAAAAAA"},{"at":"2026-01-02T03:07:05Z","id":"dHJhZGUtMgAAAAAAAAAAAA","order":"b3JkZXItMQAAAAAAAAAAAA","account":"acc-1","principal":"carol","baseAsset":"BTC","quoteAsset":"USD","side":"sell","quantity":"0.5","price":"101.4","lockPrice":"101.35","commission":{"amount":"-0.01","currency":"BNB"},"source":"panel","venueConnection":"dmVudWUtMgAAAAAAAAAAAA"}]}` + "\n",
		},
		{
			name:   "account detail",
			path:   "/api/v1/accounts/acc-1",
			fake:   goldenFakeService,
			status: http.StatusOK,
			want:   `{"account":{"code":"acc-1","title":"Account One","pnl":"125.50","pnlHaltReason":"missing_fx","group":"desk-a","currency":"USD","effectiveCurrency":"USD","currencyOrigin":"account","currencyCascade":{"account":"USD","group":"EUR","default":"GBP"},"notes":"account one notes","positionCount":0,"blockReason":"desk a halt","blockSource":"group","accountBlockReason":"","groupBlockReason":"desk a halt","blocked":true,"accountBlocked":false,"groupBlocked":true},"limits":{"rateLimits":[],"orderSizeLimits":[],"spotFundsPnlBoundsLimits":[]}}` + "\n",
		},
		{
			name:   "group detail",
			path:   "/api/v1/groups/desk-a",
			fake:   goldenFakeService,
			status: http.StatusOK,
			want:   `{"accounts":[{"code":"acc-1","title":"Account One","pnl":"125.50","pnlHaltReason":"missing_fx","group":"desk-a","currency":"USD","effectiveCurrency":"USD","currencyOrigin":"account","currencyCascade":{"account":"USD","group":"EUR","default":"GBP"},"notes":"account one notes","positionCount":0,"blockReason":"desk a halt","blockSource":"group","accountBlockReason":"","groupBlockReason":"desk a halt","blocked":true,"accountBlocked":false,"groupBlocked":true},{"code":"acc-2","title":"Account Two","pnl":"-7.25","pnlHaltReason":"missing_cost_basis","group":"desk-b","currency":"JPY","effectiveCurrency":"JPY","currencyOrigin":"account","currencyCascade":{"account":"JPY","group":"CHF","default":"GBP"},"notes":"account two notes","positionCount":0,"blockReason":"manual stop","blockSource":"account","accountBlockReason":"manual stop","groupBlockReason":"desk a halt","blocked":true,"accountBlocked":true,"groupBlocked":true}],"group":{"code":"desk-a","title":"Desk A","currency":"EUR","notes":"desk a notes","accountCount":0,"positionCount":0,"blockReason":"desk a halt","blocked":true}}` + "\n",
		},
		{
			name:   "order detail",
			path:   "/api/v1/orders/" + extID("order-1").String(),
			fake:   goldenFakeService,
			status: http.StatusOK,
			want:   `{"events":[{"at":"2026-01-02T03:04:05Z","id":"ZXZlbnQtMQAAAAAAAAAAAA","order":"b3JkZXItMQAAAAAAAAAAAA","type":"submitted","source":"api","alg":"ed25519","principal":"alice","signed":true}],"order":{"at":"2026-01-02T03:04:05Z","id":"b3JkZXItMQAAAAAAAAAAAA","account":"acc-1","principal":"alice","baseAsset":"BTC","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"10","commissionSubtotals":[{"amount":"0.75","currency":"USD"},{"amount":"-0.01","currency":"BNB"}],"leavesQuantity":"4","price":"101.25","status":"partially_filled","source":"api","displayPrice":"101.30","dropCopy":true,"signed":true},"trades":[{"at":"2026-01-02T03:06:05Z","id":"dHJhZGUtMQAAAAAAAAAAAA","order":"b3JkZXItMQAAAAAAAAAAAA","account":"acc-1","principal":"alice","baseAsset":"BTC","quoteAsset":"USD","side":"buy","quantity":"6","price":"101.2","lockPrice":"101.3","commission":{"amount":"0.45","currency":"USD"},"source":"api"},{"at":"2026-01-02T03:07:05Z","id":"dHJhZGUtMgAAAAAAAAAAAA","order":"b3JkZXItMQAAAAAAAAAAAA","account":"acc-1","principal":"carol","baseAsset":"BTC","quoteAsset":"USD","side":"sell","quantity":"0.5","price":"101.4","lockPrice":"101.35","commission":{"amount":"-0.01","currency":"BNB"},"source":"panel"}]}` + "\n",
		},
		{
			name: "account list group read failure",
			path: "/api/v1/accounts",
			fake: func() *fakeService {
				f := goldenFakeService()
				f.groupErr = fmt.Errorf("boom")
				return f
			},
			status: http.StatusInternalServerError,
			want:   `{"error":{"code":"internal","message":"internal error"}}` + "\n",
		},
		{
			name: "account list group read invalid",
			path: "/api/v1/accounts",
			fake: func() *fakeService {
				f := goldenFakeService()
				f.groupErr = fmt.Errorf("group read rejected: %w", domain.ErrInvalid)
				return f
			},
			status: http.StatusUnprocessableEntity,
			want:   `{"type":"about:blank","title":"Unprocessable Content","status":422,"detail":"group read rejected: invalid","errors":[{"code":"validation","pointer":""}]}` + "\n",
		},
		{
			name: "account list rows read invalid",
			path: "/api/v1/accounts",
			fake: func() *fakeService {
				f := goldenFakeService()
				f.listAccountsErr = fmt.Errorf("account page rejected: %w", domain.ErrInvalid)
				return f
			},
			status: http.StatusUnprocessableEntity,
			want:   `{"type":"about:blank","title":"Unprocessable Content","status":422,"detail":"account page rejected: invalid","errors":[{"code":"validation","pointer":""}]}` + "\n",
		},
		{
			name: "account list rows read before group read",
			path: "/api/v1/accounts",
			fake: func() *fakeService {
				f := goldenFakeService()
				f.listAccountsErr = fmt.Errorf("account page rejected: %w", domain.ErrInvalid)
				f.groupErr = fmt.Errorf("boom")
				return f
			},
			status: http.StatusUnprocessableEntity,
			want:   `{"type":"about:blank","title":"Unprocessable Content","status":422,"detail":"account page rejected: invalid","errors":[{"code":"validation","pointer":""}]}` + "\n",
		},
		{
			name: "account list empty page still reads groups",
			path: "/api/v1/accounts",
			fake: func() *fakeService {
				return &fakeService{groupErr: fmt.Errorf("boom")}
			},
			status: http.StatusInternalServerError,
			want:   `{"error":{"code":"internal","message":"internal error"}}` + "\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(tc.fake())
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.status {
				t.Fatalf("want %d, got %d: %s", tc.status, rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); got != tc.want {
				t.Fatalf("body mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}
