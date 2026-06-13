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

	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/domain"
)

// --- GET /api/v1/service ----------------------------------------------------

func TestServiceInfo(t *testing.T) {
	svc := &fakeService{
		serviceInfo: backend.ServiceInfo{
			Name:               "Pit Officer",
			EngineVersion:      "v1.2.3",
			EngineBuildProfile: "release",
			Database: backend.ServiceDatabase{
				Path:      "/var/lib/officer.db",
				Reachable: true,
			},
			Release: true,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/service", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if m["name"] != "Pit Officer" {
		t.Fatalf("want name=Pit Officer, got %v", m["name"])
	}
	if m["engineVersion"] != "v1.2.3" {
		t.Fatalf("want engineVersion=v1.2.3, got %v", m["engineVersion"])
	}
	if m["engineBuildProfile"] != "release" {
		t.Fatalf("want engineBuildProfile=release, got %v", m["engineBuildProfile"])
	}
	if m["release"] != true {
		t.Fatalf("want release=true, got %v", m["release"])
	}
	db, ok := m["database"].(map[string]any)
	if !ok {
		t.Fatalf("want database object, got %v", m["database"])
	}
	if db["path"] != "/var/lib/officer.db" || db["reachable"] != true {
		t.Fatalf("unexpected database facet: %v", db)
	}
}

// TestServiceInfo_ServiceError checks the /service handler routes a service
// error through writeErr, yielding a generic 500/"internal" (statusErr is the
// injectable error for ServiceInfo).
func TestServiceInfo_ServiceError(t *testing.T) {
	r, _ := newRouter(&fakeService{statusErr: fmt.Errorf("boom")})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/service", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
	if errObj["message"] != "internal error" {
		t.Fatalf("want generic message, got %v", errObj["message"])
	}
}

// --- GET /api/v1/overview ---------------------------------------------------

func TestOverview(t *testing.T) {
	at := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	svc := &fakeService{
		overview: backend.Overview{
			Counts: backend.Counts{
				Accounts:    3,
				Groups:      1,
				Limits:      5,
				OrdersToday: 2,
				OrdersTotal: 9,
			},
			Activity: []backend.Activity{
				{
					At:      at,
					Source:  domain.SourceAPI,
					Kind:    backend.ActivityKindOrder,
					Ref:     "7",
					Summary: "order acc-1 buy AAPL",
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	counts, ok := m["counts"].(map[string]any)
	if !ok {
		t.Fatalf("want counts object, got %v", m["counts"])
	}
	// JSON numbers decode to float64.
	if counts["accounts"] != float64(3) || counts["ordersTotal"] != float64(9) {
		t.Fatalf("unexpected counts: %v", counts)
	}
	activity, ok := m["activity"].([]any)
	if !ok || len(activity) != 1 {
		t.Fatalf("want 1 activity entry, got %v", m["activity"])
	}
	a := activity[0].(map[string]any)
	for _, field := range []string{"at", "source", "kind", "ref", "summary"} {
		if _, ok := a[field]; !ok {
			t.Fatalf("activity missing field %q", field)
		}
	}
}

// TestOverview_Since exercises both the well-formed and the unparseable ?since=
// parameter. Either way the handler must answer 200: a usable RFC3339 value
// sets the "today" boundary, an unparseable one silently falls back to the
// server-local start of day. The fake ignores the boundary, so only the status
// code is asserted.
func TestOverview_Since(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "valid RFC3339", query: "?since=2026-06-11T00:00:00Z"},
		{name: "unparseable fallback", query: "?since=not-a-time"},
		{name: "empty value fallback", query: "?since="},
		{name: "absent", query: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/overview"+tc.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
		})
	}
}

// TestOverview_ServiceError checks the overview handler maps a service error
// through writeErr to a 500 (statusErr is the injectable error for Overview).
// Unlike /status this path does NOT degrade to 503; it is a genuine error.
func TestOverview_ServiceError(t *testing.T) {
	r, _ := newRouter(&fakeService{statusErr: fmt.Errorf("boom")})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- GET /api/v1/status (service-error 503 path) ----------------------------

// TestV1Status_ServiceUnavailable checks the special-cased /status path: on a
// Status error the handler does NOT go through writeErr. It returns 503 with a
// degraded statusDTO (healthy:false, empty nodes, no "error" object).
func TestV1Status_ServiceUnavailable(t *testing.T) {
	r, err := newRouter(&fakeService{statusErr: fmt.Errorf("store unreachable")})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if m["healthy"] != false {
		t.Fatalf("want healthy:false, got %v", m["healthy"])
	}
	nodes, ok := m["nodes"].([]any)
	if !ok || len(nodes) != 0 {
		t.Fatalf("want empty nodes array, got %v", m["nodes"])
	}
	// The degraded body must not carry the writeErr "error" envelope.
	if _, present := m["error"]; present {
		t.Fatalf("503 status body must not contain an error object: %v", m)
	}
}

// --- GET /api/v1/accounts (service-error path) ------------------------------

// TestListAccounts_ServiceError drives the harness-added listAccountsErr to the
// list handler's 500 branch. The happy path is covered by TestListAccounts.
func TestListAccounts_ServiceError(t *testing.T) {
	r, _ := newRouter(&fakeService{listAccountsErr: fmt.Errorf("boom")})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// TestListAccounts_Empty checks the empty case returns a non-null JSON array,
// not null, so the SPA can iterate it unconditionally.
func TestListAccounts_Empty(t *testing.T) {
	r, _ := newRouter(&fakeService{})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	accounts, ok := m["accounts"].([]any)
	if !ok {
		t.Fatalf("want accounts array, got %v", m["accounts"])
	}
	if len(accounts) != 0 {
		t.Fatalf("want empty array, got %v", accounts)
	}
}

// --- POST /api/v1/accounts/{id}/unblock (error/not-found paths) -------------

// TestUnblockAccount_NotFound checks an unblockErr that is domain.ErrNotFound
// maps to 404. The happy path is covered by TestUnblockAccount.
func TestUnblockAccount_NotFound(t *testing.T) {
	r, err := newRouter(&fakeService{unblockErr: domain.ErrNotFound})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-x/unblock", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

// TestUnblockAccount_ServiceError checks a non-sentinel unblockErr maps to a
// generic 500/"internal".
func TestUnblockAccount_ServiceError(t *testing.T) {
	r, _ := newRouter(&fakeService{unblockErr: fmt.Errorf("boom")})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/unblock", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- GET /api/v1/balances ---------------------------------------------------

func TestListBalances(t *testing.T) {
	updated := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	svc := &fakeService{
		balances: []domain.Balance{
			{
				UpdatedAt:         updated,
				Account:           "acc-1",
				Asset:             "USD",
				Available:         "1000",
				Held:              "50",
				Incoming:          "0",
				RealizedPnl:       "12",
				AverageEntryPrice: "",
				Tenant:            domain.DefaultTenant,
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/balances", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	balances, ok := m["balances"].([]any)
	if !ok || len(balances) != 1 {
		t.Fatalf("want 1 balance, got %v", m["balances"])
	}
	b := balances[0].(map[string]any)
	for _, field := range []string{
		"updatedAt", "account", "asset", "available", "held",
		"incoming", "realizedPnl", "averageEntryPrice",
	} {
		if _, ok := b[field]; !ok {
			t.Fatalf("balance missing field %q", field)
		}
	}
	if b["account"] != "acc-1" || b["asset"] != "USD" || b["available"] != "1000" {
		t.Fatalf("unexpected balance: %v", b)
	}
}

// TestListBalances_QueryParams checks the account/asset query parameters are
// accepted and the handler still answers 200. The fake ignores the filter
// arguments, so this asserts the parse-and-forward path, not the filtering.
func TestListBalances_QueryParams(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "account only", query: "?account=acc-1"},
		{name: "asset only", query: "?asset=USD"},
		{name: "account and asset", query: "?account=acc-1&asset=USD"},
		{name: "no filter", query: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/balances"+tc.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
			m := bodyMap(t, rec.Result())
			if _, ok := m["balances"].([]any); !ok {
				t.Fatalf("want balances array, got %v", m["balances"])
			}
		})
	}
}

// TestListBalances_ServiceError drives the harness-added balancesErr to the
// list handler's 500 branch.
func TestListBalances_ServiceError(t *testing.T) {
	r, _ := newRouter(&fakeService{balancesErr: fmt.Errorf("boom")})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/balances", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- PUT /api/v1/accounts/{id}/group ----------------------------------------

func TestSetAccountGroup(t *testing.T) {
	// writeAccount re-reads the account via GetAccountState, so the account
	// must be seeded or the success response would itself 404.
	svc := &fakeService{
		accounts: []domain.Account{{ID: "acc-1", Tenant: domain.DefaultTenant}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"group":"vip"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/group", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	acc, ok := m["account"].(map[string]any)
	if !ok || acc["id"] != "acc-1" {
		t.Fatalf("want account object for acc-1, got %v", m["account"])
	}
}

// TestSetAccountGroup_ClearMembership checks an empty group clears membership
// and still answers 200 (the handler documents the empty-group clear case).
func TestSetAccountGroup_ClearMembership(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{{ID: "acc-1", Tenant: domain.DefaultTenant}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"group":""}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/group", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestSetAccountGroup_InvalidJSON(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/group", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestSetAccountGroup_NotFound checks a stateErr of domain.ErrNotFound (e.g. an
// unknown account or target group) maps to 404. stateErr is the injectable
// error for SetAccountGroup.
func TestSetAccountGroup_NotFound(t *testing.T) {
	r, err := newRouter(&fakeService{stateErr: domain.ErrNotFound})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"group":"vip"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-x/group", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

// TestSetAccountGroup_ServiceError checks a non-sentinel stateErr maps to a
// generic 500/"internal".
func TestSetAccountGroup_ServiceError(t *testing.T) {
	r, _ := newRouter(&fakeService{stateErr: fmt.Errorf("boom")})
	body := bytes.NewBufferString(`{"group":"vip"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/group", body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- PUT /api/v1/accounts/{id}/notes ----------------------------------------

func TestSetAccountNotes(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{{ID: "acc-1", Tenant: domain.DefaultTenant}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"notes":"watch closely"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/notes", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	acc, ok := m["account"].(map[string]any)
	if !ok || acc["id"] != "acc-1" {
		t.Fatalf("want account object for acc-1, got %v", m["account"])
	}
}

// TestSetAccountNotes_Empty checks clearing notes (empty string) still answers
// 200.
func TestSetAccountNotes_Empty(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{{ID: "acc-1", Tenant: domain.DefaultTenant}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"notes":""}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/notes", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestSetAccountNotes_InvalidJSON(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/notes", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestSetAccountNotes_NotFound checks a stateErr of domain.ErrNotFound (unknown
// account) maps to 404.
func TestSetAccountNotes_NotFound(t *testing.T) {
	r, err := newRouter(&fakeService{stateErr: domain.ErrNotFound})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"notes":"x"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-x/notes", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

// TestSetAccountNotes_ServiceError checks a non-sentinel stateErr maps to a
// generic 500/"internal".
func TestSetAccountNotes_ServiceError(t *testing.T) {
	r, _ := newRouter(&fakeService{stateErr: fmt.Errorf("boom")})
	body := bytes.NewBufferString(`{"notes":"x"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/notes", body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}
