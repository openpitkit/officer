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
				ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
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
	// Decimal values are exact strings; the external id is the order handle.
	if o["account"] != "acc-1" || o["baseAsset"] != "AAPL" || o["price"] != "100" {
		t.Fatalf("order fields not on the wire: %v", o)
	}
	if o["externalId"] != extID("order-1").String() {
		t.Fatalf("want externalId=%s, got %v", extID("order-1").String(), o["externalId"])
	}
	assertNoSurrogateID(t, o)
	// displayPrices must serialise as an empty array, never null.
	if prices, ok := o["displayPrices"].([]any); !ok || len(prices) != 0 {
		t.Fatalf("want displayPrices=[], got %v", o["displayPrices"])
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
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
			Side: domain.OrderSideBuy, Status: domain.OrderStatusFilled,
		},
		Events: []domain.OrderEvent{
			{ExternalID: extID("event-1"), Order: extID("order-1"), At: ts, Type: domain.OrderEventSubmitted},
		},
		Trades: []domain.Trade{
			{ExternalID: extID("trade-1"), Order: extID("order-1"), At: ts, Account: "acc-1", Quantity: "1", Price: "100"},
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+extID("order-1").String(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	order, ok := m["order"].(map[string]any)
	if !ok {
		t.Fatalf("want order object, got %v", m["order"])
	}
	assertNoSurrogateID(t, order)
	if order["externalId"] != extID("order-1").String() {
		t.Fatalf("want externalId=%s, got %v", extID("order-1").String(), order["externalId"])
	}
	events, ok := m["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("want 1 event, got %v", m["events"])
	}
	trades, ok := m["trades"].([]any)
	if !ok || len(trades) != 1 {
		t.Fatalf("want 1 trade, got %v", m["trades"])
	}
	// An unsigned order (Approval == nil) omits the approval envelope entirely.
	if _, present := m["approval"]; present {
		t.Fatalf("unsigned order must not carry an approval key: %v", m["approval"])
	}
}

// TestGetOrder_ApprovalReadBack covers the 1:1 approval read-back: a signed
// order surfaces its envelope under the "approval" key with the full metadata,
// while an unsigned order omits the key. The envelope is addressed only by the
// signing key id; no surrogate or engine id appears.
func TestGetOrder_ApprovalReadBack(t *testing.T) {
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1",
			Status: domain.OrderStatusAccepted,
		},
		Approval: &domain.OrderApproval{
			Token:     "eyJhbHQ...",
			KeyID:     "key-1",
			Alg:       "ed25519",
			Mode:      "immediate",
			IssuedAt:  "2026-06-11T10:00:00Z",
			ExpiresAt: "2026-06-11T10:02:00Z",
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/orders/"+extID("order-1").String(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	approval, ok := m["approval"].(map[string]any)
	if !ok {
		t.Fatalf("want approval envelope, got %v", m["approval"])
	}
	for _, field := range []string{
		"token", "keyId", "alg", "mode", "issuedAt", "expiresAt", "signed",
	} {
		if _, ok := approval[field]; !ok {
			t.Fatalf("approval envelope missing field %q", field)
		}
	}
	if approval["keyId"] != "key-1" || approval["alg"] != "ed25519" {
		t.Fatalf("unexpected approval envelope: %v", approval)
	}
	// An ed25519 envelope reports signed=true.
	if approval["signed"] != true {
		t.Fatalf("want signed=true, got %v", approval["signed"])
	}
	assertNoSurrogateID(t, approval)
}

func TestGetOrder_EmptyChildLists(t *testing.T) {
	// An order with no events or trades serialises those as [], never null.
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{ExternalID: extID("order-1"), Account: "acc-1"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+extID("order-1").String(), nil))
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
	// The order id is now an opaque string; a malformed value reaches the backend,
	// which reports it as not found. (The old non-integer-path 400 no longer applies.)
	svc := &fakeService{stateErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orders/not-a-real-id", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

func TestGetOrder_NotFound(t *testing.T) {
	svc := &fakeService{stateErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+extID("missing").String(), nil))
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
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+extID("order-1").String(), nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

// --- GET /api/v1/trades -----------------------------------------------------

func TestListTrades_Seeded(t *testing.T) {
	svc := &fakeService{
		trades: []domain.Trade{
			{
				ExternalID: extID("trade-1"), Order: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
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
	if tr["externalId"] != extID("trade-1").String() {
		t.Fatalf("want externalId, got %v", tr["externalId"])
	}
	assertNoSurrogateID(t, tr)
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
				ExternalID: extID("adj-1"), Account: "acc-1",
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
	assertNoSurrogateID(t, a)
	if a["externalId"] != extID("adj-1").String() {
		t.Fatalf("want externalId, got %v", a["externalId"])
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
				ExternalID: extID("adj-1"), Account: "acc-1",
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
	assertNoSurrogateID(t, adjustments[0].(map[string]any))
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

// TestApplyAdjustment_SuppliedExternalID checks an optional caller-supplied id is
// threaded onto the backend create call as-is.
func TestApplyAdjustment_SuppliedExternalID(t *testing.T) {
	supplied := extID("adj-supplied")
	svc := &fakeService{adjustment: domain.AccountAdjustmentRecord{
		ExternalID: supplied, Account: "acc-1",
		Request: domain.AdjustmentRequest{Asset: "USD"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(fmt.Sprintf(
		`{"externalId":%q,"asset":"USD","balance":{"mode":"delta","value":"100"}}`,
		supplied.String()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/adjustments", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.adjustmentExternalID != supplied {
		t.Errorf("supplied id not threaded: got %s, want %s",
			svc.adjustmentExternalID, supplied)
	}
	m := bodyMap(t, rec.Result())
	adj, _ := m["adjustment"].(map[string]any)
	if adj["externalId"] != supplied.String() {
		t.Errorf("want externalId=%s, got %v", supplied.String(), adj["externalId"])
	}
	assertNoSurrogateID(t, adj)
}

// TestApplyAdjustment_AbsentExternalID checks omitting the id leaves the backend
// to generate one (a zero id is passed through).
func TestApplyAdjustment_AbsentExternalID(t *testing.T) {
	svc := &fakeService{adjustment: domain.AccountAdjustmentRecord{
		ExternalID: extID("adj-generated"), Account: "acc-1",
		Request: domain.AdjustmentRequest{Asset: "USD"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"asset":"USD","balance":{"mode":"delta","value":"100"}}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/adjustments", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if !svc.adjustmentExternalID.IsZero() {
		t.Errorf("absent id should pass a zero id to the backend, got %s",
			svc.adjustmentExternalID)
	}
}

// TestApplyAdjustment_DuplicateConflict checks a duplicate supplied id maps to
// 409 (the backend rejects with domain.ErrAlreadyExists).
func TestApplyAdjustment_DuplicateConflict(t *testing.T) {
	svc := &fakeService{stateErr: domain.ErrAlreadyExists}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(fmt.Sprintf(
		`{"externalId":%q,"asset":"USD","balance":{"mode":"delta","value":"100"}}`,
		extID("adj-dup").String()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/adjustments", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "conflict" {
		t.Errorf("want code=conflict, got %v", errObj["code"])
	}
}

// TestApplyAdjustment_MalformedExternalID checks a malformed supplied id maps to
// 400 before any backend call.
func TestApplyAdjustment_MalformedExternalID(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"externalId":"not-valid","asset":"USD","balance":{"mode":"delta","value":"100"}}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/adjustments", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Errorf("want code=validation, got %v", errObj["code"])
	}
}

// --- POST /api/v1/orders/{id}/execution-reports (error/edge only) -----------

func TestApplyExecutionReport_BadID(t *testing.T) {
	// The order id is now an opaque string; the GetOrder lookup for an unknown id
	// surfaces 404. (The old non-integer-path 400 no longer applies.)
	svc := &fakeService{stateErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"quantity":"1","price":"100","final":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/unknown-id/execution-reports", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
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
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
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
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
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
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
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
		"/api/v1/orders/"+extID("missing").String()+"/execution-reports", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}
