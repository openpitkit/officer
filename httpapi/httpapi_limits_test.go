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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func TestGetSpotFundsLimitMode(t *testing.T) {
	trackOnly := domain.SpotFundsLimitModeState{Mode: domain.SpotFundsLimitModeTrackOnly, Stored: true}
	for _, tc := range []struct {
		name     string
		state    domain.SpotFundsLimitModeState
		accounts []domain.AccountID
		want     string
	}{
		{"accounts", trackOnly, []domain.AccountID{"acc-1"},
			`{"mode":"track_only","stored":true,"accountsWithoutCurrency":["acc-1"]}`},
		{"nil accounts", trackOnly, nil,
			`{"mode":"track_only","stored":true,"accountsWithoutCurrency":[]}`},
		{"no row", domain.SpotFundsLimitModeState{Mode: domain.SpotFundsLimitModeEnforce}, nil,
			`{"mode":"enforce","stored":false,"accountsWithoutCurrency":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := tc.state
			state.AccountsWithoutCurrency = tc.accounts
			svc := &fakeService{spotFundsLimitModeState: state}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/limits/spot-funds-limit-mode", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tc.want {
				t.Fatalf("body = %s, want %s", got, tc.want)
			}
			if svc.spotFundsLimitModeGetCalls != 1 {
				t.Fatalf("get calls = %d, want 1", svc.spotFundsLimitModeGetCalls)
			}
		})
	}
}

// TestPutSpotFundsLimitMode proves the put takes the meta-policy body,
// forwards it verbatim, and answers the value read back under its own key,
// while a field the body does not carry, an axis among them, is refused before
// the service.
func TestPutSpotFundsLimitMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		wantStatus  int
		wantCalls   int
		wantPointer string
	}{
		{"track only", `{"scope":"global","mode":"track_only"}`, http.StatusOK, 1, ""},
		{"unknown field", `{"scope":"global","mode":"track_only","extra":true}`, http.StatusUnprocessableEntity, 0, "/extra"},
		{"account axis", `{"scope":"global","mode":"track_only","account":"acc-1"}`, http.StatusUnprocessableEntity, 0, "/account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/spot-funds-limit-mode", strings.NewReader(tc.body)))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if svc.spotFundsLimitModePutCalls != tc.wantCalls {
				t.Fatalf("put calls = %d, want %d", svc.spotFundsLimitModePutCalls, tc.wantCalls)
			}
			if tc.wantStatus == http.StatusUnprocessableEntity {
				m := bodyMap(t, rec.Result())
				errObj, _ := m["error"].(map[string]any)
				if errObj["code"] != "validation" || errObj["message"] != "unknown field in request body" {
					t.Fatalf("want unknown %s validation problem, got %+v", tc.wantPointer, errObj)
				}
				problems, _ := m["errors"].([]any)
				if len(problems) != 1 {
					t.Fatalf("problems = %v, want one", problems)
				}
				problem, _ := problems[0].(map[string]any)
				if problem["pointer"] != tc.wantPointer || problem["constraint"] != "unknown_field" {
					t.Fatalf("want unknown %s validation detail, got %+v", tc.wantPointer, problem)
				}
				if svc.listLimitsCalls != 0 {
					t.Fatalf("refused put read the stored row: list calls = %d", svc.listLimitsCalls)
				}
				return
			}
			want := domain.LimitSpotFundsLimitMode{
				Scope: domain.ScopeGlobal, Mode: domain.SpotFundsLimitModeTrackOnly,
			}
			if svc.spotFundsLimitModePut != want {
				t.Fatalf("put = %+v, want %+v", svc.spotFundsLimitModePut, want)
			}
			if svc.listLimitsCalls != 1 || svc.spotFundsLimitModeGetCalls != 0 {
				t.Fatalf("read-back = (list %d, effective %d), want the stored row read once",
					svc.listLimitsCalls, svc.spotFundsLimitModeGetCalls)
			}
			const wantBody = `{"spotFundsLimitMode":{"scope":"global","mode":"track_only"}}`
			if got := strings.TrimSpace(rec.Body.String()); got != wantBody {
				t.Fatalf("body = %s, want %s", got, wantBody)
			}
		})
	}
}

// TestPutSpotFundsLimitModeAnswersReadBack proves the put answers the stored
// row it reads back, not the body it was sent, and answers the absent-barrier
// status when no row is stored after the put.
func TestPutSpotFundsLimitModeAnswersReadBack(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stored     []domain.LimitSpotFundsLimitMode
		wantStatus int
		wantBody   string
	}{
		{
			"stored row", []domain.LimitSpotFundsLimitMode{{Scope: domain.ScopeGlobal, Mode: domain.SpotFundsLimitModeEnforce}},
			http.StatusOK, `{"spotFundsLimitMode":{"scope":"global","mode":"enforce"}}`,
		},
		{"no row", nil, http.StatusNotFound, `{"error":{"code":"not_found","message":"not found"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &readBackModeService{fakeService: &fakeService{}, stored: tc.stored}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/spot-funds-limit-mode",
				strings.NewReader(`{"scope":"global","mode":"track_only"}`)))
			if rec.Code != tc.wantStatus || strings.TrimSpace(rec.Body.String()) != tc.wantBody {
				t.Fatalf("status = %d, body = %s; want %d %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

// readBackModeService stores a put but reads back its own fixed rows, so an
// answer that echoes the body instead of the stored row is caught.
type readBackModeService struct {
	*fakeService
	stored []domain.LimitSpotFundsLimitMode
}

func (s *readBackModeService) ListLimits(context.Context, domain.AccountID) (node.AccountLimits, error) {
	return node.AccountLimits{SpotFundsLimitModes: s.stored}, nil
}

func TestSpotFundsLimitModeErrors(t *testing.T) {
	accountScope := domain.LimitSpotFundsLimitMode{
		Scope: domain.ScopeAccount, Mode: domain.SpotFundsLimitModeEnforce,
	}.Validate()
	for _, tc := range []struct {
		name           string
		method         string
		body           string
		err            error
		wantStatus     int
		wantCode       string
		wantPointer    string
		wantConstraint string
	}{
		{"required", http.MethodPut, `{"scope":"global","mode":""}`, domain.ValidateSpotFundsLimitMode(""), http.StatusUnprocessableEntity, "validation", "/mode", "required"},
		{"absent", http.MethodPut, `{"scope":"global"}`, domain.ValidateSpotFundsLimitMode(""), http.StatusUnprocessableEntity, "validation", "/mode", "required"},
		{"unsupported", http.MethodPut, `{"scope":"global","mode":"unknown"}`, domain.ValidateSpotFundsLimitMode("unknown"), http.StatusUnprocessableEntity, "validation", "/mode", "unsupported_value"},
		{"account scope", http.MethodPut, `{"scope":"account","mode":"enforce"}`, accountScope, http.StatusUnprocessableEntity, "validation", "/scope", "enum"},
		{"loosening", http.MethodPut, `{"scope":"global","mode":"track_only"}`, fmt.Errorf("mode change: %w", domain.ErrLoosening), http.StatusForbidden, "loosening_refused", "", ""},
		{"restarting", http.MethodPut, `{"scope":"global","mode":"track_only"}`, fmt.Errorf("mode change: %w", domain.ErrEngineRestarting), http.StatusServiceUnavailable, "engine_restarting", "", ""},
		{"read error", http.MethodGet, "", fmt.Errorf("mode read: %w", domain.ErrEngineRestarting), http.StatusServiceUnavailable, "engine_restarting", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{putSpotFundsLimitModeErr: tc.err, spotFundsLimitModeErr: tc.err}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(tc.method, "/api/v1/limits/spot-funds-limit-mode", strings.NewReader(tc.body)))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			m := bodyMap(t, rec.Result())
			if tc.wantPointer != "" {
				problems, _ := m["errors"].([]any)
				if len(problems) != 1 {
					t.Fatalf("problems = %v, want one", problems)
				}
				problem, _ := problems[0].(map[string]any)
				if problem["code"] != tc.wantCode || problem["pointer"] != tc.wantPointer || problem["constraint"] != tc.wantConstraint {
					t.Fatalf("problem = %v, want %s at %s (%s)", problem, tc.wantCode, tc.wantPointer, tc.wantConstraint)
				}
			} else {
				problem, _ := m["error"].(map[string]any)
				if problem["code"] != tc.wantCode {
					t.Fatalf("code = %v, want %s", problem["code"], tc.wantCode)
				}
			}
			if tc.method == http.MethodPut {
				var req httpx.SpotFundsLimitModeLimitDTO
				if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
					t.Fatal(err)
				}
				if svc.spotFundsLimitModePutCalls != 1 || svc.spotFundsLimitModePut != httpx.FromSpotFundsLimitModeLimitDTO(req) {
					t.Fatalf("service write = (%d, %+v), want (1, %+v)", svc.spotFundsLimitModePutCalls, svc.spotFundsLimitModePut, req)
				}
				if svc.listLimitsCalls != 0 {
					t.Fatalf("failed put read the stored row back: list calls = %d", svc.listLimitsCalls)
				}
			}
		})
	}
}

// TestPutSpotFundsLimitModeReadBackError proves a failed read-back after a
// stored put answers the read's error, not a stale body.
func TestPutSpotFundsLimitModeReadBackError(t *testing.T) {
	svc := &fakeService{listLimErr: fmt.Errorf("limits read: %w", domain.ErrEngineRestarting)}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/spot-funds-limit-mode",
		strings.NewReader(`{"scope":"global","mode":"enforce"}`)))
	if rec.Code != http.StatusServiceUnavailable || svc.spotFundsLimitModePutCalls != 1 {
		t.Fatalf("status = %d, put calls = %d; want 503 after one put", rec.Code, svc.spotFundsLimitModePutCalls)
	}
}

// TestDeleteSpotFundsLimitMode proves DELETE /limits removes the meta-policy
// row like any barrier: 204 on success, and the absent-barrier status when no
// mode is stored.
func TestDeleteSpotFundsLimitMode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"stored", nil, http.StatusNoContent},
		{"absent", fmt.Errorf("setting %q: %w", domain.SettingKeySpotFundsLimitMode, domain.ErrNotFound), http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{delLimErr: tc.err}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
				"/api/v1/limits?policy=spot_funds_limit_mode&scope=global", nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			want := node.LimitTarget{Policy: domain.PolicySpotFundsLimitMode, Scope: domain.ScopeGlobal}
			if svc.deleteLimitTarget != want {
				t.Fatalf("delete target = %+v, want %+v", svc.deleteLimitTarget, want)
			}
		})
	}
}

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
	if policy["kind"] != domain.PolicyOrderSizeLimit || policy["scope"] != string(domain.ScopeBroker) {
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
					Axis:         domain.SpotFundsPnlAxisRealized,
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

// TestListLimitsSpotFundsLimitModeRow proves the meta-policy row is listed as
// one global row with no axis and its mode under values.spotFundsLimitMode,
// and that ?policy=spot_funds_limit_mode selects its kind.
func TestListLimitsSpotFundsLimitModeRow(t *testing.T) {
	svc := &fakeService{
		policyRows: []store.PolicyListRow{{
			Kind:  store.PolicyKindSpotFundsLimitMode,
			Scope: domain.ScopeGlobal,
			SpotFundsLimitMode: &domain.LimitSpotFundsLimitMode{
				Scope: domain.ScopeGlobal, Mode: domain.SpotFundsLimitModeTrackOnly,
			},
		}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/limits?policy=spot_funds_limit_mode", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	if svc.policyFilter.Kind == nil || *svc.policyFilter.Kind != store.PolicyKindSpotFundsLimitMode {
		t.Fatalf("filter kind = %v, want spot_funds_limit_mode", svc.policyFilter.Kind)
	}
	const want = `{"policies":[{"kind":"spot_funds_limit_mode","scope":"global","account":"",` +
		`"accountGroup":"","asset":"","values":{"spotFundsLimitMode":{"mode":"track_only"}}}],"total":1}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body = %s, want %s", got, want)
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
					Axis:         domain.SpotFundsPnlAxisRealized,
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
					Axis:       domain.SpotFundsPnlAxisRealized,
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

// TestPutSpotFundsPnlBoundsLimit_HugeExponentValidationError puts, through
// the real backend and node, a lower bound written with an exponent that,
// compared with the upper bound as written, would build 10^2000000000: it is
// answered within a second as a validation error on /lowerBound before the
// bounds are compared, and nothing is stored.
func TestPutSpotFundsPnlBoundsLimit_HugeExponentValidationError(t *testing.T) {
	requireLowerBoundRefused(t, "-1e-2000000000")
}

// TestPutSpotFundsPnlBoundsLimit_ExponentNotationValidationError puts, through
// the real backend and node, a lower bound of -100 written in exponent
// notation, which the engine's exact parser does not take: it is answered as
// a validation error on /lowerBound and nothing is stored, where a stored
// bound would fail the engine configuration and undoing that would rebuild the
// engine.
func TestPutSpotFundsPnlBoundsLimit_ExponentNotationValidationError(t *testing.T) {
	requireLowerBoundRefused(t, "-1e2")
}

// requireLowerBoundRefused puts, through the real backend and node, a global
// P&L-bounds barrier with lowerBound under an upper bound of 0, and fails t
// unless the answer comes within a second as a single decimal validation
// error at /lowerBound with nothing stored, so a bound whose check grows with
// its exponent fails the test instead of stalling the suite.
func requireLowerBoundRefused(t *testing.T, lowerBound string) {
	t.Helper()
	r, realm := newRealServiceRouter(t)
	body, err := json.Marshal(map[string]string{
		"scope": "global", "currency": "USD", "lowerBound": lowerBound, "upperBound": "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(
		http.MethodPut, "/api/v1/limits/spot-funds-pnl-bounds", bytes.NewReader(body),
	)
	rec := serveWithinASecond(t, r, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d body=%s", rec.Code, rec.Body.String())
	}
	problems, _ := bodyMap(t, rec.Result())["errors"].([]any)
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want one", problems)
	}
	problem, _ := problems[0].(map[string]any)
	if problem["code"] != "validation" || problem["pointer"] != "/lowerBound" ||
		problem["constraint"] != "decimal" {
		t.Fatalf("problem = %v, want a decimal validation at /lowerBound", problem)
	}
	stored, err := realm.ListSpotFundsPnlBoundsLimits(t.Context(), "")
	if err != nil || len(stored) != 0 {
		t.Fatalf("stored bounds = %+v err=%v, want none", stored, err)
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

// TestPutRateLimit_OverflowingWindowValidationError puts, through the real
// backend and node, a broker rate barrier whose windowMs does not fit
// time.Duration in nanoseconds, above or below, and would wrap to exactly 60s:
// it is answered as a range validation error on /windowMs and nothing is
// stored.
func TestPutRateLimit_OverflowingWindowValidationError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		windowMs string
	}{
		{name: "above", windowMs: "288230376151771744"},  // 2^58 + 60000
		{name: "below", windowMs: "-288230376151651744"}, // 60000 - 2^58
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, realm := newRealServiceRouter(t)
			body := bytes.NewBufferString(
				`{"scope":"broker","windowMs":` + tc.windowMs + `,"maxOrders":100}`,
			)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/rate", body))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("want 422, got %d body=%s", rec.Code, rec.Body.String())
			}
			problems, _ := bodyMap(t, rec.Result())["errors"].([]any)
			if len(problems) != 1 {
				t.Fatalf("problems = %v, want one", problems)
			}
			problem, _ := problems[0].(map[string]any)
			if problem["code"] != "validation" || problem["pointer"] != "/windowMs" ||
				problem["constraint"] != "range" {
				t.Fatalf("problem = %v, want a range validation at /windowMs", problem)
			}
			stored, err := realm.ListRateLimits(t.Context(), "")
			if err != nil || len(stored) != 0 {
				t.Fatalf("stored rate limits = %+v err=%v, want none", stored, err)
			}
		})
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
			{Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeAccount, Account: "acc-1",
				Currency: "USD", LowerBound: "-1000", UpperBound: "5000"},
		},
		SpotFundsLimitModes: []domain.LimitSpotFundsLimitMode{
			{Scope: domain.ScopeGlobal, Mode: domain.SpotFundsLimitModeTrackOnly},
		},
	}
	b, err := json.Marshal(httpx.ToAccountLimitsDTO(limits))
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
			t.Fatalf("RateLimitDTO missing JSON key %q", key)
		}
	}
	if rate["maxOrders"] != float64(100) || rate["windowMs"] != float64(1000) {
		t.Fatalf("unexpected rate limit: %v", rate)
	}
	sizes, _ := m["orderSizeLimits"].([]any)
	size := sizes[0].(map[string]any)
	for _, key := range []string{"scope", "account", "asset", "maxQuantity", "maxNotional"} {
		if _, ok := size[key]; !ok {
			t.Fatalf("OrderSizeLimitDTO missing JSON key %q", key)
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
			t.Fatalf("SpotFundsPnlBoundsLimitDTO missing JSON key %q", key)
		}
	}
	if spotFundsPnl["currency"] != "USD" {
		t.Fatalf("SpotFundsPnlBoundsLimitDTO currency = %v, want USD", spotFundsPnl["currency"])
	}
	modes, _ := m["spotFundsLimitModes"].([]any)
	if len(modes) != 1 {
		t.Fatalf("want 1 spot funds limit mode, got %v", m["spotFundsLimitModes"])
	}
	if mode := modes[0].(map[string]any); mode["scope"] != "global" || mode["mode"] != "track_only" {
		t.Fatalf("SpotFundsLimitModeLimitDTO = %v, want global track_only", mode)
	}
}
