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

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func TestListBalances_PropagatesFilters(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/balances?account=acc&groupCode=desk"+
			"&asset=USD&availableMode=between&availableMin=2.5&availableMax=10"+
			"&updatedAtMode=greater_than&updatedAfter=2026-01-02T03:04:05Z"+
			"&sort=available&order=desc&limit=10&offset=20",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := svc.balanceFilter.Account.Fragments; !slices.Equal(got, []string{"acc"}) {
		t.Fatalf("account fragments = %v", got)
	}
	if svc.balanceFilter.GroupCode == nil || *svc.balanceFilter.GroupCode != "desk" {
		t.Fatalf("group filter = %v", svc.balanceFilter.GroupCode)
	}
	if got := svc.balanceFilter.Asset.Fragments; !slices.Equal(got, []string{"USD"}) {
		t.Fatalf("asset fragments = %v", got)
	}
	if svc.balanceFilter.Available.Min == nil || svc.balanceFilter.Available.Max == nil {
		t.Fatalf("available range = %+v", svc.balanceFilter.Available)
	}
	if svc.balanceFilter.Available.MinExclusive || svc.balanceFilter.Available.MaxExclusive {
		t.Fatalf("available exclusivity = %+v", svc.balanceFilter.Available)
	}
	if svc.balanceFilter.UpdatedAt.Min == nil || !svc.balanceFilter.UpdatedAt.MinExclusive {
		t.Fatalf("updatedAt range = %+v", svc.balanceFilter.UpdatedAt)
	}
	if svc.balanceFilter.Sort.Column != "available" || !svc.balanceFilter.Sort.Descending {
		t.Fatalf("sort filter = %+v", svc.balanceFilter.Sort)
	}
	if svc.balanceFilter.Page.Limit != 10 || svc.balanceFilter.Page.Offset != 20 {
		t.Fatalf("page filter = %+v", svc.balanceFilter.Page)
	}
}

// The denominated thresholds reach the store paired with the currency they were
// given in, so the search never compares them against a row kept in another.
func TestListBalances_PropagatesDenominatedFilterCurrency(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/balances?realizedPnlMode=gt&realizedPnlMin=50"+
			"&realizedPnlCurrency=USD"+
			"&averageEntryPriceMode=lt&averageEntryPriceMax=200"+
			"&averageEntryPriceCurrency=EUR",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	pnl := svc.balanceFilter.RealizedPnl
	if pnl.Currency != "USD" {
		t.Fatalf("realized P&L currency = %q, want USD", pnl.Currency)
	}
	if pnl.Range.Min == nil || *pnl.Range.Min != "50" || !pnl.Range.MinExclusive {
		t.Fatalf("realized P&L range = %+v", pnl.Range)
	}
	avg := svc.balanceFilter.AverageEntryPrice
	if avg.Currency != "EUR" {
		t.Fatalf("avg entry price currency = %q, want EUR", avg.Currency)
	}
	if avg.Range.Max == nil || *avg.Range.Max != "200" || !avg.Range.MaxExclusive {
		t.Fatalf("avg entry price range = %+v", avg.Range)
	}
}

// A threshold on a per-row denominated column cannot be evaluated without the
// currency it was given in, and the request names no default to fall back on.
func TestListBalances_RejectsDenominatedFilterWithoutCurrency(t *testing.T) {
	for _, query := range []string{
		"realizedPnlMode=gt&realizedPnlMin=50",
		"averageEntryPriceMode=gt&averageEntryPriceMin=50",
	} {
		t.Run(query, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(
				http.MethodGet, "/api/v1/balances?"+query, nil,
			))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("want 422, got %d", rec.Code)
			}
			if !svc.balanceFilter.RealizedPnl.Empty() ||
				!svc.balanceFilter.AverageEntryPrice.Empty() {
				t.Fatalf("rejected filter reached the store: %+v", svc.balanceFilter)
			}
		})
	}
}

func TestListBalances_RejectsAverageEntryPriceSort(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/balances?sort=averageEntryPrice", nil,
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", rec.Code)
	}
}

func TestListBalances_RealizedPnlSortRequiresCurrency(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/balances?sort=realizedPnl", nil,
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q", got)
	}
	problem := bodyMap(t, rec.Result())
	if problem["type"] != "about:blank" || problem["title"] != "Unprocessable Content" ||
		problem["status"] != float64(http.StatusUnprocessableEntity) {
		t.Fatalf("problem = %+v", problem)
	}
	errorsExt, _ := problem["errors"].([]any)
	if len(errorsExt) != 1 {
		t.Fatalf("errors = %v", problem["errors"])
	}
	item, _ := errorsExt[0].(map[string]any)
	if item["code"] != "validation" || item["constraint"] != "required" ||
		item["pointer"] != "/realizedPnlCurrency" {
		t.Fatalf("validation item = %+v", item)
	}
}

func TestListBalances_PropagatesScopedRealizedPnlSort(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/balances?sort=realizedPnl&order=desc&realizedPnlCurrency=USD",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.balanceFilter.Sort.Column != "realizedPnl" ||
		!svc.balanceFilter.Sort.Descending ||
		svc.balanceFilter.RealizedPnl.Currency != "USD" {
		t.Fatalf("filter = %+v", svc.balanceFilter)
	}
}

// An unrestricted list carries no threshold, so it needs no currency either.
func TestListBalances_AllowsNoCurrencyWithoutThreshold(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/balances?realizedPnlMode=all", nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestSetBalanceRealizedPnl(t *testing.T) {
	svc := &fakeService{balanceRealizedPnl: domain.Balance{
		Account:         "acc-1",
		Asset:           "USD",
		RealizedPnl:     "-12.50",
		AccountCurrency: "USD",
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"asset":"USD","realizedPnl":"-12.50"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPut,
		"/api/v1/accounts/acc-1/balances/realized-pnl?missingAccount=create",
		body,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.realizedPnlAsset != "USD" || svc.realizedPnlValue != "-12.50" {
		t.Fatalf(
			"captured realized pnl = asset %q value %q",
			svc.realizedPnlAsset,
			svc.realizedPnlValue,
		)
	}
	m := bodyMap(t, rec.Result())
	bal, ok := m["balance"].(map[string]any)
	if !ok {
		t.Fatalf("response missing balance: %v", m)
	}
	if bal["realizedPnl"] != "-12.50" {
		t.Fatalf("realizedPnl = %v, want -12.50", bal["realizedPnl"])
	}
	if bal["accountCurrency"] != "USD" {
		t.Fatalf("accountCurrency = %v, want USD", bal["accountCurrency"])
	}
}

func TestSetBalanceRealizedPnl_BadJSON(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPut,
		"/api/v1/accounts/acc-1/balances/realized-pnl?missingAccount=create",
		body,
	))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
	if errObj["message"] != "invalid JSON" {
		t.Fatalf("want invalid JSON message, got %v", errObj["message"])
	}
}

func TestSetBalanceRealizedPnl_LiteralPercentAccountID(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"asset":"USD","realizedPnl":"-12.50"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPut,
		"/api/v1/accounts/acc%25ZZ/balances/realized-pnl?missingAccount=create",
		body,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	response := bodyMap(t, rec.Result())
	balance, ok := response["balance"].(map[string]any)
	if !ok || balance["account"] != "acc%ZZ" {
		t.Fatalf("balance = %v, want literal account acc%%ZZ", response["balance"])
	}
}

func TestSetBalanceRealizedPnl_ServiceError(t *testing.T) {
	const msg = "bad realized pnl"
	svc := &fakeService{stateErr: fmt.Errorf("%s: %w", msg, domain.ErrInvalid)}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"asset":"USD","realizedPnl":"bad"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPut,
		"/api/v1/accounts/acc-1/balances/realized-pnl?missingAccount=create",
		body,
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
	if errObj["message"] != fmt.Sprintf("%s: %s", msg, domain.ErrInvalid) {
		t.Fatalf("want wrapped message, got %v", errObj["message"])
	}
}

func TestListAdjustments_PropagatesFilters(t *testing.T) {
	status := domain.AdjustmentStatusRejected
	svc := &fakeService{
		adjustmentPage: &store.AdjustmentListPage{
			Total: 17,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/adjustments?id="+extID("adj-1").String()+
			"&account=acc-1&accountMatch=exact"+
			"&asset=US&assetMatch=starts_with&source=api&status=rejected"+
			"&atMode=gte&atMin=2026-01-02T03:04:05Z"+
			"&sort=status&order=asc&limit=9&offset=18",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.adjustmentFilter.ExternalID != extID("adj-1") {
		t.Fatalf("external id = %v", svc.adjustmentFilter.ExternalID)
	}
	if got := svc.adjustmentFilter.Account.Fragments; !slices.Equal(got, []string{"acc-1"}) {
		t.Fatalf("account fragments = %v", got)
	}
	if !svc.adjustmentFilter.Account.AnchorStart ||
		!svc.adjustmentFilter.Account.AnchorEnd {
		t.Fatalf("account matcher = %+v", svc.adjustmentFilter.Account)
	}
	if got := svc.adjustmentFilter.Asset.Fragments; !slices.Equal(got, []string{"US"}) {
		t.Fatalf("asset fragments = %v", got)
	}
	if !svc.adjustmentFilter.Asset.AnchorStart || !svc.adjustmentFilter.Asset.AnchorEnd {
		t.Fatalf("asset matcher = %+v", svc.adjustmentFilter.Asset)
	}
	if svc.adjustmentFilter.Source != domain.SourceAPI {
		t.Fatalf("source = %q", svc.adjustmentFilter.Source)
	}
	if svc.adjustmentFilter.Status == nil || *svc.adjustmentFilter.Status != status {
		t.Fatalf("status = %v", svc.adjustmentFilter.Status)
	}
	if svc.adjustmentFilter.At.Min == nil || svc.adjustmentFilter.At.MinExclusive {
		t.Fatalf("at range = %+v", svc.adjustmentFilter.At)
	}
	if svc.adjustmentFilter.Sort.Column != "status" ||
		svc.adjustmentFilter.Sort.Descending {
		t.Fatalf("sort = %+v", svc.adjustmentFilter.Sort)
	}
	if svc.adjustmentFilter.Page.Limit != 9 ||
		svc.adjustmentFilter.Page.Offset != 18 {
		t.Fatalf("page = %+v", svc.adjustmentFilter.Page)
	}
	m := bodyMap(t, rec.Result())
	if m["total"] != float64(17) {
		t.Fatalf("envelope = %v", m)
	}
}
