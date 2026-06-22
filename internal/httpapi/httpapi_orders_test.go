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
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/domain"
)

// --- GET /api/v1/orders -----------------------------------------------------

func TestListOrders_Seeded(t *testing.T) {
	svc := &fakeService{
		orders: []domain.Order{
			{
				ID: 7, Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
				Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
				AmountValue: "1", Price: "100", Status: domain.OrderStatusCommitted,
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orders", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	orders, ok := m["orders"].([]any)
	if !ok || len(orders) != 1 {
		t.Fatalf("want 1 order, got %v", m["orders"])
	}
	o := orders[0].(map[string]any)
	// Decimal values are exact strings; the id is the order identifier.
	if o["account"] != "acc-1" || o["baseAsset"] != "AAPL" || o["price"] != "100" {
		t.Fatalf("order fields not on the wire: %v", o)
	}
	// lockPrices must serialise as an empty array, never null.
	if prices, ok := o["lockPrices"].([]any); !ok || len(prices) != 0 {
		t.Fatalf("want lockPrices=[], got %v", o["lockPrices"])
	}
}

func TestListOrders_Empty(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orders", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	// An empty list serialises as [], never null.
	orders, ok := m["orders"].([]any)
	if !ok || len(orders) != 0 {
		t.Fatalf("want orders=[], got %v", m["orders"])
	}
}

func TestListOrders_QueryFilters(t *testing.T) {
	// The account/source/limit query params are accepted on the happy path; the
	// fake ignores them but the handler must parse them without error.
	tests := []struct {
		name  string
		query string
	}{
		{"account", "?account=acc-1"},
		{"source", "?source=mcp"},
		{"limit", "?limit=10"},
		{"all", "?account=acc-1&source=rest&limit=5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/orders"+tc.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
		})
	}
}

func TestListOrders_BadLimit(t *testing.T) {
	// A non-integer or non-positive ?limit= is a 400 validation error.
	tests := []struct {
		name  string
		limit string
	}{
		{"non_numeric", "abc"},
		{"zero", "0"},
		{"negative", "-5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/orders?limit="+tc.limit, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d", rec.Code)
			}
			m := bodyMap(t, rec.Result())
			errObj, _ := m["error"].(map[string]any)
			if errObj["code"] != "validation" {
				t.Fatalf("want code=validation, got %v", errObj["code"])
			}
		})
	}
}

func TestListOrders_LimitCapping(t *testing.T) {
	// A limit beyond listCapREST is silently capped, not an error.
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/orders?limit=%d", listCapREST+999), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestListOrders_ServiceError(t *testing.T) {
	svc := &fakeService{ordersErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orders", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- GET /api/v1/orders/{id} ------------------------------------------------

func TestGetOrder_Found(t *testing.T) {
	ts := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ID: 9, Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
			Side: domain.OrderSideBuy, Status: domain.OrderStatusFilled,
		},
		Events: []domain.OrderEvent{
			{ID: 1, OrderID: 9, At: ts, Type: domain.OrderEventSubmitted},
		},
		Trades: []domain.Trade{
			{ID: 2, OrderID: 9, At: ts, Account: "acc-1", Quantity: "1", Price: "100"},
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orders/9", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if _, ok := m["order"].(map[string]any); !ok {
		t.Fatalf("want order object, got %v", m["order"])
	}
	events, ok := m["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("want 1 event, got %v", m["events"])
	}
	trades, ok := m["trades"].([]any)
	if !ok || len(trades) != 1 {
		t.Fatalf("want 1 trade, got %v", m["trades"])
	}
}

func TestGetOrder_EmptyChildLists(t *testing.T) {
	// An order with no events or trades serialises those as [], never null.
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{ID: 9, Account: "acc-1"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orders/9", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if events, ok := m["events"].([]any); !ok || len(events) != 0 {
		t.Fatalf("want events=[], got %v", m["events"])
	}
	if trades, ok := m["trades"].([]any); !ok || len(trades) != 0 {
		t.Fatalf("want trades=[], got %v", m["trades"])
	}
}

func TestGetOrder_BadID(t *testing.T) {
	// A non-integer order id fails pathInt64 with a 400 validation error.
	tests := []struct {
		name string
		id   string
	}{
		{"non_numeric", "abc"},
		{"float", "1.5"},
		{"empty_segment", "%20"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/orders/"+tc.id, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d", rec.Code)
			}
			m := bodyMap(t, rec.Result())
			errObj, _ := m["error"].(map[string]any)
			if errObj["code"] != "validation" {
				t.Fatalf("want code=validation, got %v", errObj["code"])
			}
		})
	}
}

func TestGetOrder_NotFound(t *testing.T) {
	svc := &fakeService{stateErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orders/404", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

func TestGetOrder_ServiceError(t *testing.T) {
	svc := &fakeService{stateErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orders/9", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

// --- GET /api/v1/trades -----------------------------------------------------

func TestListTrades_Seeded(t *testing.T) {
	svc := &fakeService{
		trades: []domain.Trade{
			{
				ID: 2, OrderID: 9, Account: "acc-1", BaseAsset: "AAPL",
				QuoteAsset: "USD", Side: domain.OrderSideBuy, Quantity: "1",
				Price: "100", LockPrice: "100",
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/trades", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	trades, ok := m["trades"].([]any)
	if !ok || len(trades) != 1 {
		t.Fatalf("want 1 trade, got %v", m["trades"])
	}
	tr := trades[0].(map[string]any)
	if tr["account"] != "acc-1" || tr["quantity"] != "1" || tr["price"] != "100" {
		t.Fatalf("trade fields not on the wire: %v", tr)
	}
}

func TestListTrades_Empty(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/trades", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if trades, ok := m["trades"].([]any); !ok || len(trades) != 0 {
		t.Fatalf("want trades=[], got %v", m["trades"])
	}
}

func TestListTrades_QueryFilters(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"account", "?account=acc-1"},
		{"source", "?source=mcp"},
		{"limit", "?limit=10"},
		{"all", "?account=acc-1&source=rest&limit=5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/trades"+tc.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
		})
	}
}

func TestListTrades_BadLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit string
	}{
		{"non_numeric", "abc"},
		{"zero", "0"},
		{"negative", "-5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/trades?limit="+tc.limit, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d", rec.Code)
			}
		})
	}
}

func TestListTrades_ServiceError(t *testing.T) {
	svc := &fakeService{tradesErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/trades", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- GET /api/v1/adjustments (global) ---------------------------------------

func TestListAdjustments_Seeded(t *testing.T) {
	svc := &fakeService{
		adjustments: []domain.AccountAdjustmentRecord{
			{
				ID: 3, Tenant: domain.DefaultTenant, Account: "acc-1",
				Request: domain.AdjustmentRequest{Asset: "USD"},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/adjustments", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	adjustments, ok := m["adjustments"].([]any)
	if !ok || len(adjustments) != 1 {
		t.Fatalf("want 1 adjustment, got %v", m["adjustments"])
	}
	a := adjustments[0].(map[string]any)
	if a["account"] != "acc-1" {
		t.Fatalf("want account=acc-1, got %v", a["account"])
	}
	// An accepted-by-default record reports status=accepted.
	if a["status"] != string(domain.AdjustmentStatusAccepted) {
		t.Fatalf("want status=accepted, got %v", a["status"])
	}
}

func TestListAdjustments_Empty(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/adjustments", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if adjustments, ok := m["adjustments"].([]any); !ok || len(adjustments) != 0 {
		t.Fatalf("want adjustments=[], got %v", m["adjustments"])
	}
}

func TestListAdjustments_QueryFilters(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"account", "?account=acc-1"},
		{"source", "?source=mcp"},
		{"limit", "?limit=10"},
		{"all", "?account=acc-1&source=rest&limit=5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/adjustments"+tc.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
		})
	}
}

func TestListAdjustments_BadLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit string
	}{
		{"non_numeric", "abc"},
		{"zero", "0"},
		{"negative", "-5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/adjustments?limit="+tc.limit, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d", rec.Code)
			}
		})
	}
}

func TestListAdjustments_ServiceError(t *testing.T) {
	// The global list uses ListAllAdjustments -> allAdjErr (not stateErr).
	svc := &fakeService{allAdjErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/adjustments", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- GET /api/v1/accounts/{id}/adjustments (account-scoped) -----------------

func TestListAccountAdjustments_Seeded(t *testing.T) {
	svc := &fakeService{
		adjustments: []domain.AccountAdjustmentRecord{
			{
				ID: 3, Tenant: domain.DefaultTenant, Account: "acc-1",
				Request: domain.AdjustmentRequest{Asset: "USD"},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/accounts/acc-1/adjustments", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	adjustments, ok := m["adjustments"].([]any)
	if !ok || len(adjustments) != 1 {
		t.Fatalf("want 1 adjustment, got %v", m["adjustments"])
	}
}

func TestListAccountAdjustments_Empty(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/accounts/acc-1/adjustments", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if adjustments, ok := m["adjustments"].([]any); !ok || len(adjustments) != 0 {
		t.Fatalf("want adjustments=[], got %v", m["adjustments"])
	}
}

func TestListAccountAdjustments_QueryFilters(t *testing.T) {
	// The source and limit query params are accepted; account comes from the path.
	tests := []struct {
		name  string
		query string
	}{
		{"source", "?source=mcp"},
		{"limit", "?limit=10"},
		{"both", "?source=rest&limit=5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/accounts/acc-1/adjustments"+tc.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
		})
	}
}

func TestListAccountAdjustments_BadLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit string
	}{
		{"non_numeric", "abc"},
		{"zero", "0"},
		{"negative", "-5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/accounts/acc-1/adjustments?limit="+tc.limit, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d", rec.Code)
			}
		})
	}
}

func TestListAccountAdjustments_ServiceError(t *testing.T) {
	// The account-scoped list uses ListAdjustments -> stateErr.
	svc := &fakeService{stateErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/accounts/acc-1/adjustments", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- POST /api/v1/accounts/{id}/adjustments (error/edge only) ---------------

func TestApplyAdjustment_InvalidJSON(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/adjustments", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestApplyAdjustment_ValidationError(t *testing.T) {
	// The backend rejects a malformed request with ErrInvalid; the handler maps
	// it to 400.
	svc := &fakeService{stateErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"asset":"USD","balance":{"mode":"delta","value":"x"}}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/adjustments", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestApplyAdjustment_ServiceError(t *testing.T) {
	svc := &fakeService{stateErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"asset":"USD","balance":{"mode":"delta","value":"100"}}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/adjustments", body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- POST /api/v1/orders/{id}/execution-reports (error/edge only) -----------

func TestApplyExecutionReport_BadID(t *testing.T) {
	// A non-integer order id fails pathInt64 before the body is read.
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"quantity":"1","price":"100","final":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/abc/execution-reports", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestApplyExecutionReport_InvalidJSON(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/9/execution-reports", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestApplyExecutionReport_ServiceError(t *testing.T) {
	// stateErr drives the GetOrder lookup (and ApplyExecutionReport) to fail; a
	// generic error maps to 500.
	svc := &fakeService{stateErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"quantity":"1","price":"100","final":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/9/execution-reports", body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

func TestApplyExecutionReport_TerminalOrder(t *testing.T) {
	svc := &fakeService{stateErr: domain.ErrTerminalOrder}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"quantity":"1","price":"100","final":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/9/execution-reports", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "terminal_order" {
		t.Fatalf("want code=terminal_order, got %v", errObj["code"])
	}
	if errObj["message"] != domain.ErrTerminalOrder.Error() {
		t.Fatalf("want terminal message, got %v", errObj["message"])
	}
}

func TestApplyExecutionReport_NotFound(t *testing.T) {
	// A missing parent order surfaces as 404 from the GetOrder lookup.
	svc := &fakeService{stateErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"quantity":"1","price":"100","final":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/404/execution-reports", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}
