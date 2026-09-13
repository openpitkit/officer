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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
)

func TestListLimits(t *testing.T) {
	svc := &fakeService{
		policyRows: []store.PolicyListRow{
			{
				Kind:  store.PolicyKindOrderSize,
				Scope: domain.ScopeBroker,
				OrderSize: &domain.LimitOrderSize{
					Scope: domain.ScopeBroker, MaxQuantity: "500",
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	// GET /limits returns the flat policy list plus a total.
	policies, ok := m["policies"].([]any)
	if !ok || len(policies) != 1 {
		t.Fatalf("want 1 policy, got %v", m["policies"])
	}
	if m["total"] != float64(1) {
		t.Fatalf("total = %v, want 1", m["total"])
	}
	policy := policies[0].(map[string]any)
	if policy["kind"] != domain.PolicyOrderSizeLimit || policy["scope"] != domain.ScopeBroker {
		t.Fatalf("unexpected policy: %v", policy)
	}
	values, ok := policy["values"].(map[string]any)
	if !ok {
		t.Fatalf("want values object, got %v", policy["values"])
	}
	orderSize, ok := values["orderSize"].(map[string]any)
	if !ok {
		t.Fatalf("want orderSize values, got %v", values)
	}
	if orderSize["maxQuantity"] != "500" {
		t.Fatalf("unexpected order-size values: %v", orderSize)
	}
}

func TestListLimitsSpotFundsPnlBoundsValues(t *testing.T) {
	svc := &fakeService{
		policyRows: []store.PolicyListRow{
			{
				Kind:         store.PolicyKindSpotFundsPnlBounds,
				Scope:        domain.ScopeAccountGroup,
				AccountGroup: "desk-a",
				SpotFundsPnlBounds: &domain.LimitSpotFundsPnlBounds{
					Scope:        domain.ScopeAccountGroup,
					AccountGroup: "desk-a",
					Currency:     "USD",
					LowerBound:   "-1000.50",
					UpperBound:   "5000.25",
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	policies, ok := m["policies"].([]any)
	if !ok || len(policies) != 1 {
		t.Fatalf("want 1 policy, got %v", m["policies"])
	}
	policy := policies[0].(map[string]any)
	values, ok := policy["values"].(map[string]any)
	if !ok {
		t.Fatalf("want values object, got %v", policy["values"])
	}
	if _, ok := values["pnlBounds"]; ok {
		t.Fatalf("spot-funds policy must not use values.pnlBounds: %v", values)
	}
	spotFunds, ok := values["spotFundsPnlBounds"].(map[string]any)
	if !ok {
		t.Fatalf("want spotFundsPnlBounds values, got %v", values)
	}
	if spotFunds["currency"] != "USD" ||
		spotFunds["lowerBound"] != "-1000.50" ||
		spotFunds["upperBound"] != "5000.25" {
		t.Fatalf("unexpected spot-funds values: %v", spotFunds)
	}
}

func TestListLimits_FilterAndPaging(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/limits?account=acc-1&accountGroup=desk-a&asset=AAPL"+
			"&scope=broker&policy=rate&sort=scope&order=desc&limit=20&offset=40",
		nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	// Account is matched exactly (anchored both ends, single fragment).
	if got := svc.policyFilter.Account.Fragments; len(got) != 1 || got[0] != "acc-1" {
		t.Fatalf("account fragments = %v", got)
	}
	if !svc.policyFilter.Account.AnchorStart || !svc.policyFilter.Account.AnchorEnd {
		t.Fatalf("account not exact-anchored: %+v", svc.policyFilter.Account)
	}
	if got := svc.policyFilter.AccountGroup.Fragments; len(got) != 1 || got[0] != "desk-a" {
		t.Fatalf("account group fragments = %v", got)
	}
	if !svc.policyFilter.AccountGroup.AnchorStart ||
		!svc.policyFilter.AccountGroup.AnchorEnd {
		t.Fatalf("account group not exact-anchored: %+v", svc.policyFilter.AccountGroup)
	}
	if got := svc.policyFilter.Asset.Fragments; len(got) != 1 || got[0] != "AAPL" {
		t.Fatalf("asset fragments = %v", got)
	}
	if !svc.policyFilter.Asset.AnchorStart || !svc.policyFilter.Asset.AnchorEnd {
		t.Fatalf("asset not exact-anchored: %+v", svc.policyFilter.Asset)
	}
	if svc.policyFilter.Kind == nil || *svc.policyFilter.Kind != store.PolicyKindRate {
		t.Fatalf("kind filter = %v", svc.policyFilter.Kind)
	}
	if svc.policyFilter.Scope != domain.ScopeBroker {
		t.Fatalf("scope filter = %q", svc.policyFilter.Scope)
	}
	if svc.policyFilter.Sort.Column != "scope" || !svc.policyFilter.Sort.Descending {
		t.Fatalf("sort spec = %+v", svc.policyFilter.Sort)
	}
	if svc.policyFilter.Page.Limit != 20 || svc.policyFilter.Page.Offset != 40 {
		t.Fatalf("page spec = %+v", svc.policyFilter.Page)
	}
}

func TestListLimits_BadSort(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/limits?sort=unknown", nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestListLimits_BadPolicy(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/limits?policy=invalid", nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestListLimits_BadScope(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/limits?scope=invalid", nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", rec.Code)
	}
}

func TestListLimits_BadLimit(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/limits?limit=-1", nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestPutRateLimit(t *testing.T) {
	svc := &fakeService{
		limits: node.AccountLimits{
			RateLimits: []domain.LimitRate{
				{
					Scope:     domain.ScopeAccountAsset,
					Account:   "acc-1",
					Asset:     "AAPL",
					Window:    2 * time.Second,
					MaxOrders: 101,
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"scope":"account_asset","account":"acc-1","asset":"AAPL",
		"windowMs":1000,"maxOrders":100
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/rate?missingAccount=create", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	// The handler captures the typed barrier and responds from persisted state.
	if svc.rateLimitPut.Scope != domain.ScopeAccountAsset ||
		svc.rateLimitPut.Account != "acc-1" ||
		svc.rateLimitPut.Asset != "AAPL" ||
		svc.rateLimitPut.Window != time.Second ||
		svc.rateLimitPut.MaxOrders != 100 {
		t.Fatalf("captured rate limit = %+v", svc.rateLimitPut)
	}
	m := bodyMap(t, rec.Result())
	rl, ok := m["rateLimit"].(map[string]any)
	if !ok {
		t.Fatalf("response missing rateLimit field: %v", m)
	}
	if rl["maxOrders"] != float64(101) || rl["windowMs"] != float64(2000) {
		t.Fatalf("unexpected persisted rateLimit: %v", rl)
	}
}

func TestPutOrderSizeLimit(t *testing.T) {
	svc := &fakeService{
		limits: node.AccountLimits{
			OrderSizeLimits: []domain.LimitOrderSize{
				{
					Scope:       domain.ScopeAccountSettlementAsset,
					Account:     "acc-1",
					Asset:       "USD",
					MaxNotional: "50001",
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"scope":"account_settlement_asset","account":"acc-1","asset":"USD",
		"maxQuantity":"","maxNotional":"50000"
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/order-size?missingAccount=create", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.orderSizeLimitPut.Scope != domain.ScopeAccountSettlementAsset ||
		svc.orderSizeLimitPut.Account != "acc-1" ||
		svc.orderSizeLimitPut.Asset != "USD" ||
		svc.orderSizeLimitPut.MaxQuantity != "" ||
		svc.orderSizeLimitPut.MaxNotional != "50000" {
		t.Fatalf("captured order-size limit = %+v", svc.orderSizeLimitPut)
	}
	m := bodyMap(t, rec.Result())
	osl, ok := m["orderSizeLimit"].(map[string]any)
	if !ok {
		t.Fatalf("response missing orderSizeLimit field: %v", m)
	}
	if osl["scope"] != "account_settlement_asset" ||
		osl["maxQuantity"] != "" || osl["maxNotional"] != "50001" {
		t.Fatalf("unexpected persisted orderSizeLimit: %v", osl)
	}
}

func TestPutSpotFundsPnlBoundsLimit(t *testing.T) {
	svc := &fakeService{
		limits: node.AccountLimits{
			SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
				{
					Scope:        domain.ScopeAccountGroup,
					AccountGroup: "desk-a",
					Currency:     "USD",
					LowerBound:   "-999",
					UpperBound:   "5001",
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"scope":"account_group","accountGroup":"desk-a",
		"currency":"USD",
		"lowerBound":"-1000","upperBound":"5000"
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(
		rec,
		httptest.NewRequest(
			http.MethodPut,
			"/api/v1/limits/spot-funds-pnl-bounds?missingAccount=create",
			body,
		),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.spotFundsPnlBoundsLimitPut.Scope != domain.ScopeAccountGroup ||
		svc.spotFundsPnlBoundsLimitPut.AccountGroup != "desk-a" ||
		svc.spotFundsPnlBoundsLimitPut.Currency != "USD" ||
		svc.spotFundsPnlBoundsLimitPut.LowerBound != "-1000" ||
		svc.spotFundsPnlBoundsLimitPut.UpperBound != "5000" {
		t.Fatalf(
			"captured spot funds pnl-bounds limit = %+v",
			svc.spotFundsPnlBoundsLimitPut,
		)
	}
	m := bodyMap(t, rec.Result())
	pbl, ok := m["spotFundsPnlBoundsLimit"].(map[string]any)
	if !ok {
		t.Fatalf("response missing spotFundsPnlBoundsLimit field: %v", m)
	}
	if pbl["accountGroup"] != "desk-a" ||
		pbl["currency"] != "USD" ||
		pbl["lowerBound"] != "-999" ||
		pbl["upperBound"] != "5001" {
		t.Fatalf("unexpected persisted spotFundsPnlBoundsLimit: %v", pbl)
	}
}

func TestPutSpotFundsPnlBoundsLimit_Account(t *testing.T) {
	svc := &fakeService{
		limits: node.AccountLimits{
			SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
				{
					Scope:      domain.ScopeAccount,
					Account:    "acc-1",
					Currency:   "EUR",
					LowerBound: "-999",
					UpperBound: "5001",
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"scope":"account","account":"acc-1",
		"currency":"EUR",
		"lowerBound":"-1000","upperBound":"5000"
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(
		rec,
		httptest.NewRequest(
			http.MethodPut,
			"/api/v1/limits/spot-funds-pnl-bounds?missingAccount=create",
			body,
		),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.spotFundsPnlBoundsLimitPut.Scope != domain.ScopeAccount ||
		svc.spotFundsPnlBoundsLimitPut.Account != "acc-1" ||
		svc.spotFundsPnlBoundsLimitPut.Currency != "EUR" {
		t.Fatalf(
			"captured spot funds pnl-bounds limit = %+v",
			svc.spotFundsPnlBoundsLimitPut,
		)
	}
	m := bodyMap(t, rec.Result())
	pbl, ok := m["spotFundsPnlBoundsLimit"].(map[string]any)
	if !ok {
		t.Fatalf("response missing spotFundsPnlBoundsLimit field: %v", m)
	}
	if pbl["account"] != "acc-1" ||
		pbl["currency"] != "EUR" ||
		pbl["lowerBound"] != "-999" ||
		pbl["upperBound"] != "5001" {
		t.Fatalf("unexpected persisted spotFundsPnlBoundsLimit: %v", pbl)
	}
}

func TestPutSpotFundsPnlBoundsLimit_ValidationError(t *testing.T) {
	svc := &fakeService{putLimErr: fmt.Errorf("bad scope: %w", domain.ErrInvalid)}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"scope":"broker","lowerBound":"-1000","upperBound":"5000"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPut,
		"/api/v1/limits/spot-funds-pnl-bounds?missingAccount=create",
		body,
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestPutSpotFundsPnlBoundsLimit_EmptyCurrencyValidationError(t *testing.T) {
	svc := &fakeService{putLimErr: fmt.Errorf("currency is required: %w", domain.ErrInvalid)}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"scope":"global","currency":"","lowerBound":"-1000"}`,
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPut,
		"/api/v1/limits/spot-funds-pnl-bounds?missingAccount=create",
		body,
	))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want validation status, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.spotFundsPnlBoundsLimitPut.Currency != "" {
		t.Fatalf("captured currency = %q, want empty", svc.spotFundsPnlBoundsLimitPut.Currency)
	}
}

func TestPutSpotFundsPnlBoundsLimit_NotImplemented(t *testing.T) {
	const msg = "engine: spot_funds add not supported yet"
	svc := &fakeService{
		putLimErr: fmt.Errorf("%s: %w", msg, domain.ErrNotImplemented),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"scope":"account","account":"acc-1","lowerBound":"-1000","upperBound":"5000"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPut,
		"/api/v1/limits/spot-funds-pnl-bounds?missingAccount=create",
		body,
	))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("want 501, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_implemented" {
		t.Fatalf("want code=not_implemented, got %v", errObj["code"])
	}
	if errObj["message"] != fmt.Sprintf("%s: %s", msg, domain.ErrNotImplemented) {
		t.Fatalf("want wrapped message, got %v", errObj["message"])
	}
}

func TestPutSpotFundsPnlBoundsLimit_EngineRestarting(t *testing.T) {
	const msg = "engine restart in progress; mutating requests are rejected until rebuild completes"
	svc := &fakeService{
		putLimErr: fmt.Errorf("%s: %w", msg, domain.ErrEngineRestarting),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"scope":"account","account":"acc-1","lowerBound":"-1000","upperBound":"5000"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPut,
		"/api/v1/limits/spot-funds-pnl-bounds?missingAccount=create",
		body,
	))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "engine_restarting" {
		t.Fatalf("want code=engine_restarting, got %v", errObj["code"])
	}
	if errObj["message"] != fmt.Sprintf("%s: %s", msg, domain.ErrEngineRestarting) {
		t.Fatalf("want wrapped message, got %v", errObj["message"])
	}
}

func TestPutRateLimit_ValidationError(t *testing.T) {
	svc := &fakeService{putLimErr: fmt.Errorf("bad scope: %w", domain.ErrInvalid)}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"scope":"broker","windowMs":1000,"maxOrders":100}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/rate?missingAccount=create", body))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestPutRateLimit_NotImplemented(t *testing.T) {
	// A wrapped domain.ErrNotImplemented (the engine's not-implemented stub
	// surfacing through the node and backend) maps to HTTP 501 with the wrapped
	// message, taking precedence over the generic 500 path.
	const msg = "engine: rate_limit add not supported yet"
	svc := &fakeService{
		putLimErr: fmt.Errorf("%s: %w", msg, domain.ErrNotImplemented),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"scope":"asset","asset":"AAPL","windowMs":1000,"maxOrders":100}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/rate?missingAccount=create", body))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("want 501, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_implemented" {
		t.Fatalf("want code=not_implemented, got %v", errObj["code"])
	}
	if errObj["message"] != fmt.Sprintf("%s: %s", msg, domain.ErrNotImplemented) {
		t.Fatalf("want wrapped message, got %v", errObj["message"])
	}
}

func TestPutRateLimit_EngineRestarting(t *testing.T) {
	const msg = "engine restart in progress; mutating requests are rejected until rebuild completes"
	svc := &fakeService{
		putLimErr: fmt.Errorf("%s: %w", msg, domain.ErrEngineRestarting),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"scope":"asset","asset":"AAPL","windowMs":1000,"maxOrders":100}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/rate?missingAccount=create", body))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "engine_restarting" {
		t.Fatalf("want code=engine_restarting, got %v", errObj["code"])
	}
	if errObj["message"] != fmt.Sprintf("%s: %s", msg, domain.ErrEngineRestarting) {
		t.Fatalf("want wrapped message, got %v", errObj["message"])
	}
}

func TestDeleteLimit(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/limits?policy=rate_limit&scope=account_asset&account=acc-1&asset=AAPL", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	// The delete target round-trips into the typed node.LimitTarget.
	if svc.deleteLimitTarget.Policy != domain.PolicyRateLimit ||
		svc.deleteLimitTarget.Scope != domain.ScopeAccountAsset ||
		svc.deleteLimitTarget.Account != "acc-1" ||
		svc.deleteLimitTarget.Asset != "AAPL" {
		t.Fatalf("captured delete target = %+v", svc.deleteLimitTarget)
	}
}

func TestDeleteSpotFundsPnlBoundsLimit(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/limits?policy=spot_funds_pnl_bounds_kill_switch"+
			"&scope=account_group&accountGroup=desk-a",
		nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if svc.deleteLimitTarget.Policy != domain.PolicySpotFundsPnlBoundsKillSwitch ||
		svc.deleteLimitTarget.Scope != domain.ScopeAccountGroup ||
		svc.deleteLimitTarget.AccountGroup != "desk-a" ||
		svc.deleteLimitTarget.Asset != "" {
		t.Fatalf("captured delete target = %+v", svc.deleteLimitTarget)
	}
}

func TestDeleteLimit_NotFound(t *testing.T) {
	svc := &fakeService{delLimErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/limits?policy=rate_limit&scope=broker", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestLimitDTO_JSONShape(t *testing.T) {
	// The per-policy limits view marshals into the supported typed barrier arrays
	// with the camelCase wire keys the contract requires.
	limits := node.AccountLimits{
		RateLimits: []domain.LimitRate{
			{Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
				Window: time.Second, MaxOrders: 100},
		},
		OrderSizeLimits: []domain.LimitOrderSize{
			{Scope: domain.ScopeBroker, MaxQuantity: "500", MaxNotional: "50000"},
		},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			{Scope: domain.ScopeAccount, Account: "acc-1",
				Currency: "USD", LowerBound: "-1000", UpperBound: "5000"},
		},
	}
	b, err := json.Marshal(toAccountLimitsDTO(limits))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	rates, _ := m["rateLimits"].([]any)
	if len(rates) != 1 {
		t.Fatalf("want 1 rate limit, got %v", m["rateLimits"])
	}
	rate := rates[0].(map[string]any)
	for _, key := range []string{"scope", "account", "asset", "windowMs", "maxOrders"} {
		if _, ok := rate[key]; !ok {
			t.Fatalf("rateLimitDTO missing JSON key %q", key)
		}
	}
	if rate["maxOrders"] != float64(100) || rate["windowMs"] != float64(1000) {
		t.Fatalf("unexpected rate limit: %v", rate)
	}
	sizes, _ := m["orderSizeLimits"].([]any)
	size := sizes[0].(map[string]any)
	for _, key := range []string{"scope", "account", "asset", "maxQuantity", "maxNotional"} {
		if _, ok := size[key]; !ok {
			t.Fatalf("orderSizeLimitDTO missing JSON key %q", key)
		}
	}
	spotFundsPnls, _ := m["spotFundsPnlBoundsLimits"].([]any)
	spotFundsPnl := spotFundsPnls[0].(map[string]any)
	for _, key := range []string{
		"scope",
		"account",
		"accountGroup",
		"currency",
		"lowerBound",
		"upperBound",
	} {
		if _, ok := spotFundsPnl[key]; !ok {
			t.Fatalf("spotFundsPnlBoundsLimitDTO missing JSON key %q", key)
		}
	}
	if spotFundsPnl["currency"] != "USD" {
		t.Fatalf("spotFundsPnlBoundsLimitDTO currency = %v, want USD", spotFundsPnl["currency"])
	}
}
