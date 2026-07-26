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
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

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
	if rec.Code != http.StatusBadRequest {
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
