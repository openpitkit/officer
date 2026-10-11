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

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func TestListOrdersAndTrades_VenueFilter(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  store.VenueFilter
	}{
		{name: "absent"},
		{name: "empty", query: "?venue="},
		{name: "all", query: "?venue=all"},
		{name: "any", query: "?venue=any", want: store.VenueFilter{Mode: store.VenueFilterAny}},
		{name: "none", query: "?venue=none", want: store.VenueFilter{Mode: store.VenueFilterNone}},
		{
			name: "connection", query: "?venue=connection:conn-1",
			want: store.VenueFilter{Mode: store.VenueFilterConnection, ConnectionID: domain.ExternalID("conn-1")},
		},
		{
			name: "opaque_connection", query: "?venue=" + url.QueryEscape("connection:connection:any:none / α"),
			want: store.VenueFilter{Mode: store.VenueFilterConnection, ConnectionID: domain.ExternalID("connection:any:none / α")},
		},
	}
	for _, endpoint := range []string{"orders", "trades"} {
		for _, tc := range tests {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				svc := &fakeService{}
				r, err := newRouter(svc)
				if err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
					"/api/v1/"+endpoint+tc.query, nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("want 200, got %d", rec.Code)
				}
				got, calls := svc.orderFilter.Venue, svc.orderListCalls
				if endpoint == "trades" {
					got, calls = svc.tradeFilter.Venue, svc.tradeListCalls
				}
				if got != tc.want {
					t.Fatalf("venue filter = %+v, want %+v", got, tc.want)
				}
				if calls != 1 {
					t.Fatalf("list calls = %d, want 1", calls)
				}
			})
		}
	}
}

func TestListOrdersAndTrades_InvalidVenue(t *testing.T) {
	for _, endpoint := range []string{"orders", "trades"} {
		for _, value := range []string{"bogus", "connection:", "connection"} {
			t.Run(endpoint+"/"+value, func(t *testing.T) {
				svc := &fakeService{}
				r, err := newRouter(svc)
				if err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
					"/api/v1/"+endpoint+"?venue="+url.QueryEscape(value), nil))
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("want 422, got %d", rec.Code)
				}
				m := bodyMap(t, rec.Result())
				errObj, _ := m["error"].(map[string]any)
				if errObj["code"] != "validation" {
					t.Fatalf("want code=validation, got %v", errObj["code"])
				}
				if svc.orderListCalls != 0 || svc.tradeListCalls != 0 {
					t.Fatalf("list calls = %d/%d, want 0/0", svc.orderListCalls, svc.tradeListCalls)
				}
			})
		}
	}
}

func TestListOrdersAndTrades_VenueConnectionJSON(t *testing.T) {
	connection := domain.ExternalID("connection:any:none / α")
	order := domain.Order{
		ExternalID: extID("order-venue"), Account: "acc-1", BaseAsset: "AAPL",
		QuoteAsset: "USD", Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
		AmountValue: "1", Price: "100", Status: domain.OrderStatusAccepted,
	}
	trade := domain.Trade{
		ExternalID: extID("trade-venue"), Order: order.ExternalID, Account: order.Account,
		BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		Quantity: "1", Price: "100", LockPrice: "101.25",
	}
	for _, endpoint := range []string{"orders", "trades"} {
		t.Run(endpoint, func(t *testing.T) {
			svc := &fakeService{
				orderPage: &backend.OrderListPage{
					Rows: []backend.OrderListRow{
						{Order: order, DisplayPrice: "101.25", Signed: true, VenueConnection: connection},
						{Order: order, DisplayPrice: "101.25", Signed: true},
					}, Total: 2,
				},
				tradePage: &store.TradeListPage{
					Rows: []store.TradeListRow{
						{Trade: trade, VenueConnection: connection}, {Trade: trade},
					}, Total: 2,
				},
			}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/"+endpoint, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
			m := bodyMap(t, rec.Result())
			rows, ok := m[endpoint].([]any)
			if !ok || len(rows) != 2 || m["total"] != float64(2) {
				t.Fatalf("want 2 rows and total=2, got %v", m)
			}
			linked := rows[0].(map[string]any)
			if linked["venueConnection"] != connection.String() {
				t.Fatalf("venueConnection = %v, want %q", linked["venueConnection"], connection)
			}
			unlinked := rows[1].(map[string]any)
			if _, present := unlinked["venueConnection"]; present {
				t.Fatalf("unlinked row carries venueConnection: %v", unlinked)
			}
			var dto any = httpx.ToOrderDTO(order, "101.25", true)
			if endpoint == "trades" {
				dto = httpx.ToTradeDTO(trade)
			}
			data, err := json.Marshal(dto)
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(unlinked, want) {
				t.Fatalf("unlinked row = %v, want unchanged DTO %v", unlinked, want)
			}
			want["venueConnection"] = connection.String()
			if !reflect.DeepEqual(linked, want) {
				t.Fatalf("linked row = %v, want flat list row %v", linked, want)
			}
		})
	}
}

func TestListOrders_PropagatesFilters(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/orders?account=acc-1&source=panel&side=buy&status=accepted,filled"+
			"&baseAsset=A&quoteAsset=USD"+
			"&amountMode=greater_than&amountMin=2.5&priceMode=less_than&priceMax=10"+
			"&atMode=between&atMin=2026-01-02T03:04:05Z&atMax=2026-01-03T03:04:05Z"+
			"&sort=amountValue&order=asc&limit=5&offset=15",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.orderFilter.Account != "acc-1" || svc.orderFilter.Source != domain.SourcePanel {
		t.Fatalf("account/source = %q/%q", svc.orderFilter.Account, svc.orderFilter.Source)
	}
	if svc.orderFilter.Side == nil || *svc.orderFilter.Side != domain.OrderSideBuy {
		t.Fatalf("side filter = %v", svc.orderFilter.Side)
	}
	if !slices.Equal(svc.orderFilter.Status, []domain.OrderStatus{
		domain.OrderStatusAccepted,
		domain.OrderStatusFilled,
	}) {
		t.Fatalf("status filter = %v", svc.orderFilter.Status)
	}
	if got := svc.orderFilter.BaseAsset.Fragments; !slices.Equal(got, []string{"A"}) {
		t.Fatalf("base fragments = %v", got)
	}
	if got := svc.orderFilter.QuoteAsset.Fragments; !slices.Equal(got, []string{"USD"}) {
		t.Fatalf("quote fragments = %v", got)
	}
	if svc.orderFilter.Amount.Min == nil || !svc.orderFilter.Amount.MinExclusive {
		t.Fatalf("amount range = %+v", svc.orderFilter.Amount)
	}
	if svc.orderFilter.Price.Max == nil || !svc.orderFilter.Price.MaxExclusive {
		t.Fatalf("price range = %+v", svc.orderFilter.Price)
	}
	if svc.orderFilter.At.Min == nil || svc.orderFilter.At.Max == nil {
		t.Fatalf("at range = %+v", svc.orderFilter.At)
	}
	if svc.orderFilter.Sort.Column != "amountValue" || svc.orderFilter.Sort.Descending {
		t.Fatalf("sort filter = %+v", svc.orderFilter.Sort)
	}
	if svc.orderFilter.Page.Limit != 5 || svc.orderFilter.Page.Offset != 15 {
		t.Fatalf("page filter = %+v", svc.orderFilter.Page)
	}
}

func TestListOrders_PageParamComputesOffset(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/orders?limit=25&page=3", nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.orderFilter.Page.Limit != 25 || svc.orderFilter.Page.Offset != 50 {
		t.Fatalf("page filter = %+v", svc.orderFilter.Page)
	}
}

func TestListOrders_OffsetParamWinsOverPage(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/orders?limit=25&page=3&offset=7", nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.orderFilter.Page.Limit != 25 || svc.orderFilter.Page.Offset != 7 {
		t.Fatalf("page filter = %+v", svc.orderFilter.Page)
	}
}

func TestListOrders_BadPage(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/orders?page=0", nil,
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestListTrades_PropagatesFilters(t *testing.T) {
	tradeID := extID("trade-exact")
	svc := &fakeService{
		tradePage: &store.TradeListPage{
			Total: 42,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/trades?id="+tradeID.String()+
			"&account=acc*"+
			"&baseAsset=AAPL&quoteAsset=US"+
			"&side=buy&source=panel&atMode=between"+
			"&atMin=2026-01-02T03:04:05Z&atMax=2026-01-03T03:04:05Z"+
			"&quantityMode=gte&quantityMin=2.5&priceMode=lt&priceMax=150.75"+
			"&lockPriceMode=neq&lockPriceMin=149.5"+
			"&sort=quantity&order=desc&limit=7&offset=14",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := svc.tradeFilter.Account.Fragments; !slices.Equal(got, []string{"acc*"}) {
		t.Fatalf("account fragments = %v", got)
	}
	if svc.tradeFilter.ExternalID != tradeID {
		t.Fatalf("external id = %s", svc.tradeFilter.ExternalID)
	}
	if !svc.tradeFilter.Account.AnchorStart || !svc.tradeFilter.Account.AnchorEnd {
		t.Fatalf("account matcher = %+v", svc.tradeFilter.Account)
	}
	if got := svc.tradeFilter.BaseAsset.Fragments; !slices.Equal(got, []string{"AAPL"}) {
		t.Fatalf("base fragments = %v", got)
	}
	if !svc.tradeFilter.BaseAsset.AnchorStart || !svc.tradeFilter.BaseAsset.AnchorEnd {
		t.Fatalf("base matcher = %+v", svc.tradeFilter.BaseAsset)
	}
	if got := svc.tradeFilter.QuoteAsset.Fragments; !slices.Equal(got, []string{"US"}) {
		t.Fatalf("quote fragments = %v", got)
	}
	if svc.tradeFilter.Side == nil || *svc.tradeFilter.Side != domain.OrderSideBuy {
		t.Fatalf("side = %v", svc.tradeFilter.Side)
	}
	if svc.tradeFilter.Source != domain.SourcePanel {
		t.Fatalf("source = %q", svc.tradeFilter.Source)
	}
	if svc.tradeFilter.At.Min == nil || svc.tradeFilter.At.Max == nil {
		t.Fatalf("at range = %+v", svc.tradeFilter.At)
	}
	if svc.tradeFilter.Quantity.Min == nil || *svc.tradeFilter.Quantity.Min != "2.5" ||
		svc.tradeFilter.Quantity.MinExclusive {
		t.Fatalf("quantity range = %+v", svc.tradeFilter.Quantity)
	}
	if svc.tradeFilter.Price.Max == nil || *svc.tradeFilter.Price.Max != "150.75" ||
		!svc.tradeFilter.Price.MaxExclusive {
		t.Fatalf("price range = %+v", svc.tradeFilter.Price)
	}
	if svc.tradeFilter.LockPrice.NotEqual == nil ||
		*svc.tradeFilter.LockPrice.NotEqual != "149.5" {
		t.Fatalf("lockPrice range = %+v", svc.tradeFilter.LockPrice)
	}
	if svc.tradeFilter.Sort.Column != "quantity" || !svc.tradeFilter.Sort.Descending {
		t.Fatalf("sort = %+v", svc.tradeFilter.Sort)
	}
	if svc.tradeFilter.Page.Limit != 7 || svc.tradeFilter.Page.Offset != 14 {
		t.Fatalf("page = %+v", svc.tradeFilter.Page)
	}
	m := bodyMap(t, rec.Result())
	if m["total"] != float64(42) {
		t.Fatalf("envelope = %v", m)
	}
}

func TestListTrades_IgnoresLegacyExternalIDFilter(t *testing.T) {
	svc := &fakeService{
		tradePage: &store.TradeListPage{},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/trades?externalId="+extID("legacy-trade").String(),
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if !svc.tradeFilter.ExternalID.IsZero() {
		t.Fatalf("legacy externalId filter was accepted: %s", svc.tradeFilter.ExternalID)
	}
}
