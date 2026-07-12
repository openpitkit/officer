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
	"slices"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
)

func TestListAccounts(t *testing.T) {
	svc := &fakeService{
		accountRows: []store.AccountListRow{
			{
				Account:       domain.Account{Code: "acc-1", Title: "Account One", Pnl: "12.5"},
				PositionCount: 2,
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	accounts, ok := m["accounts"].([]any)
	if !ok || len(accounts) != 1 {
		t.Fatalf("want 1 account, got %v", m["accounts"])
	}
	a := accounts[0].(map[string]any)
	// Accounts are addressed by their public code; no surrogate id leaks.
	if a["code"] != "acc-1" {
		t.Fatalf("want code=acc-1, got %v", a["code"])
	}
	if a["title"] != "Account One" {
		t.Fatalf("want title=Account One, got %v", a["title"])
	}
	if a["pnl"] != "12.5" {
		t.Fatalf("want pnl=12.5, got %v", a["pnl"])
	}
	assertNoSurrogateID(t, a)
	if _, ok := a["blocked"]; !ok {
		t.Fatal("missing blocked field")
	}
	if _, ok := a["blockReason"]; !ok {
		t.Fatal("missing blockReason field")
	}
	if a["positionCount"] != float64(2) {
		t.Fatalf("positionCount = %v, want 2", a["positionCount"])
	}
}

func TestListAccounts_PropagatesFilters(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/accounts?code=acc*alpha&codeMatch=starts_with"+
			"&status=blocked&positionCountMode=greater_than&positionCountMin=1"+
			"&blockReason=risk&blockReasonMatch=contains&group="+
			"&sort=positionCount&order=desc&limit=25&offset=50",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.accountFilter.GroupCode == nil || *svc.accountFilter.GroupCode != "" {
		t.Fatalf("group filter = %v, want empty group pointer", svc.accountFilter.GroupCode)
	}
	if got := svc.accountFilter.Code.Fragments; !slices.Equal(got, []string{"acc", "alpha"}) {
		t.Fatalf("code fragments = %v", got)
	}
	if !svc.accountFilter.Code.AnchorStart || svc.accountFilter.Code.AnchorEnd {
		t.Fatalf("code anchors = %+v, want start only", svc.accountFilter.Code)
	}
	if svc.accountFilter.Status != store.StatusFilterBlocked {
		t.Fatalf("status filter = %q", svc.accountFilter.Status)
	}
	if svc.accountFilter.Position.Min == nil || *svc.accountFilter.Position.Min != 1 ||
		!svc.accountFilter.Position.MinExclusive {
		t.Fatalf("position filter = %+v", svc.accountFilter.Position)
	}
	if got := svc.accountFilter.BlockReason.Fragments; !slices.Equal(got, []string{"risk"}) {
		t.Fatalf("block reason fragments = %v", got)
	}
	if svc.accountFilter.Sort.Column != "positionCount" ||
		!svc.accountFilter.Sort.Descending {
		t.Fatalf("sort filter = %+v", svc.accountFilter.Sort)
	}
	if svc.accountFilter.Page.Limit != 25 || svc.accountFilter.Page.Offset != 50 {
		t.Fatalf("page filter = %+v", svc.accountFilter.Page)
	}
}

func TestListAccounts_RejectsNotesSort(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/accounts?sort=notes",
		nil,
	))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestCreateAccount(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"code":"acc-1","title":"Account One","currency":"USD"}`,
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	acc, ok := m["account"].(map[string]any)
	if !ok {
		t.Fatalf("want account object, got %v", m["account"])
	}
	if acc["code"] != "acc-1" {
		t.Fatalf("want code=acc-1, got %v", acc["code"])
	}
	if acc["title"] != "Account One" {
		t.Fatalf("want title=Account One, got %v", acc["title"])
	}
	if acc["currency"] != "USD" {
		t.Fatalf("want currency=USD, got %v", acc["currency"])
	}
	assertNoSurrogateID(t, acc)
}

func TestCreateAccount_ValidationError(t *testing.T) {
	r, err := newRouter(&fakeService{createErr: domain.ErrInvalid})
	if err != nil {
		t.Fatal(err)
	}
	// Empty code fails ValidateAccountID
	body := bytes.NewBufferString(`{"code":""}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestCreateAccount_Conflict(t *testing.T) {
	svc := &fakeService{createErr: domain.ErrAlreadyExists}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"acc-1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "conflict" {
		t.Fatalf("want code=conflict, got %v", errObj["code"])
	}
}

func TestGetAccount(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{
			{Code: "acc-1", Title: "Account One"},
		},
		limits: node.AccountLimits{
			RateLimits: []domain.LimitRate{
				{
					Scope:     domain.ScopeAccount,
					Account:   "acc-1",
					Window:    time.Second,
					MaxOrders: 100,
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/acc-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	acc, ok := m["account"].(map[string]any)
	if !ok {
		t.Fatal("missing account field")
	}
	if acc["code"] != "acc-1" {
		t.Fatalf("want code=acc-1, got %v", acc["code"])
	}
	assertNoSurrogateID(t, acc)
	// The per-policy limits view carries the three typed barrier arrays.
	limits, ok := m["limits"].(map[string]any)
	if !ok {
		t.Fatalf("want limits object, got %v", m["limits"])
	}
	rates, _ := limits["rateLimits"].([]any)
	if len(rates) != 1 {
		t.Fatalf("want 1 rate limit, got %v", limits["rateLimits"])
	}
	rate := rates[0].(map[string]any)
	if rate["scope"] != domain.ScopeAccount || rate["account"] != "acc-1" ||
		rate["maxOrders"] != float64(100) || rate["windowMs"] != float64(1000) {
		t.Fatalf("unexpected rate limit: %v", rate)
	}
}

func TestGetAccount_NotFound(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

func TestGetAccount_URLEncodedID(t *testing.T) {
	// Verify that percent-encoded characters in the account id are decoded
	// before reaching the service. %40 = '@'.
	svc := &fakeService{
		accounts: []domain.Account{
			{Code: "acc@1"},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	// acc%401 decodes to acc@1
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/acc%401", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for URL-encoded id, got %d", rec.Code)
	}
}

func TestSetAccountCurrency(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{{Code: "acc-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"currency":"USD"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/currency", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	acc, ok := m["account"].(map[string]any)
	if !ok {
		t.Fatalf("want account object, got %v", m["account"])
	}
	if acc["code"] != "acc-1" || acc["currency"] != "USD" {
		t.Fatalf("unexpected account currency response: %v", acc)
	}
}

func TestSetAccountCurrency_ServiceInvalid(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{{Code: "acc-1"}},
		stateErr: fmt.Errorf("apply account currency: %w", domain.ErrInvalid),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"currency":"NOPE"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/accounts/acc-1/currency", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestBlockAccount(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{{Code: "acc-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"reason":"compliance"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/block", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestBlockAccount_NotFound(t *testing.T) {
	svc := &fakeService{blockErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"reason":"x"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-x/block", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestUnblockAccount(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{{Code: "acc-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/unblock", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

// TestDeleteAccount_HasDependents asserts the 409 has_dependents wire shape: a
// HasDependentsError from the service maps to HTTP 409 with body
// error.code == "has_dependents" and a dependents array of {kind, count}.
func TestDeleteAccount_HasDependents(t *testing.T) {
	svc := &fakeService{stateErr: domain.NewHasDependentsError([]domain.DependentCount{
		{Kind: "orders", Count: 3},
	})}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/accounts/acc-1", nil))
	assertHasDependents409(t, rec, "orders", 3)
}
