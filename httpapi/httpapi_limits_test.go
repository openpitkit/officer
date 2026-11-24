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
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/internal/store/sqlite"
)

type followingLimitWriteService struct {
	backend.ControlPlane
}

func (s followingLimitWriteService) PutOrderSizeLimit(
	ctx context.Context, limit domain.LimitOrderSize, write domain.LimitWrite,
	missing domain.MissingAccountPolicy,
) (domain.LimitOrderSize, error) {
	stored, err := s.ControlPlane.PutOrderSizeLimit(ctx, limit, write, missing)
	if err != nil {
		return domain.LimitOrderSize{}, err
	}
	limit.MaxQuantity = "5"
	_, err = s.ControlPlane.PutOrderSizeLimit(ctx, limit, domain.LimitWriteChange, missing)
	return stored, err
}

func TestLimitRESTWriteAnswersBeforeFollowingWriter(t *testing.T) {
	svc := newTestPolicyService(t)
	router, err := newRouter(followingLimitWriteService{ControlPlane: svc})
	if err != nil {
		t.Fatal(err)
	}
	rec := callTestPolicy(t, router, "/api/v1", http.MethodPost,
		"/limits/order-size?missingAccount=reject",
		`{"scope":"broker","maxQuantity":"10","mode":"review_without_hold"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	barrier, _ := bodyMap(t, rec.Result())["orderSizeLimit"].(map[string]any)
	if barrier["maxQuantity"] != "10" || barrier["mode"] != "review_without_hold" {
		t.Fatalf("write answered %+v, want its own review_without_hold cap 10", barrier)
	}
}

type orderSizeDeleteService struct {
	*fakeService
	realm store.RealmStore
}

func (s *orderSizeDeleteService) DeleteLimit(ctx context.Context, target node.LimitTarget) error {
	s.deleteLimitTarget = target
	key, err := target.Key()
	if err != nil {
		return err
	}
	return s.realm.DeleteLimit(ctx, key)
}

func TestDeleteOrderSizeLimitDefaultsToReject(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(t.TempDir()+"/order-size.db", domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	realm, err := db.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	review := domain.LimitOrderSize{Mode: domain.RuleModeReviewWithoutHold, Scope: domain.ScopeBroker, MaxQuantity: "20"}
	reject := review
	reject.Mode, reject.MaxQuantity = domain.RuleModeReject, "10"
	for _, limit := range []domain.LimitOrderSize{review, reject} {
		if err := realm.PutOrderSizeLimit(ctx, limit); err != nil {
			t.Fatal(err)
		}
	}
	svc := &orderSizeDeleteService{fakeService: &fakeService{}, realm: realm}
	router, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/limits?policy=order_size_limit&scope=broker", nil))
	if rec.Code != http.StatusNoContent || svc.deleteLimitTarget.Mode != domain.RuleModeReject {
		t.Fatalf("REST delete = %d %s target=%+v", rec.Code, rec.Body.String(), svc.deleteLimitTarget)
	}
	limits, err := realm.ListOrderSizeLimits(ctx, "")
	if err != nil || len(limits) != 1 || limits[0] != review {
		t.Fatalf("REST delete touched review barrier: %+v, %v", limits, err)
	}
}

func TestCountingLimitRESTModes(t *testing.T) {
	for _, policy := range []string{"rate", "position", "spot-funds-pnl-bounds", "spot-funds-unrealized-pnl-bounds", "spot-funds-total-pnl-bounds"} {
		for _, mode := range []string{"absent", "", "reject", "review_with_hold", "review_without_hold", "unknown"} {
			t.Run(policy+"/"+mode, func(t *testing.T) {
				svc := newTestPolicyService(t)
				router, err := newRouter(svc)
				if err != nil {
					t.Fatal(err)
				}
				body := map[string]any{"scope": "broker", "windowMs": 1000, "maxOrders": 10}
				field := "rateLimit"
				if policy == "position" {
					field = "positionLimit"
					body = map[string]any{"scope": "account_asset", "account": "acc-1", "asset": "AAPL", "maxPosition": "10"}
					if rec := callTestPolicy(t, router, "/api/v1", http.MethodPost, "/assets", `{"code":"AAPL"}`); rec.Code != http.StatusCreated {
						t.Fatalf("create asset: %d %s", rec.Code, rec.Body)
					}
					if rec := callTestPolicy(t, router, "/api/v1", http.MethodPost, "/accounts", `{"code":"acc-1"}`); rec.Code != http.StatusCreated {
						t.Fatalf("create account: %d %s", rec.Code, rec.Body)
					}
				}
				if strings.HasPrefix(policy, "spot-funds-") {
					field = "spotFundsPnlBoundsLimit"
					switch policy {
					case "spot-funds-unrealized-pnl-bounds":
						field = "spotFundsUnrealizedPnlBoundsLimit"
					case "spot-funds-total-pnl-bounds":
						field = "spotFundsTotalPnlBoundsLimit"
					}
					body = map[string]any{"scope": "global", "currency": "USD", "lowerBound": "-10"}
					if rec := callTestPolicy(t, router, "/api/v1", http.MethodPost, "/assets", `{"code":"USD"}`); rec.Code != http.StatusCreated {
						t.Fatalf("create currency: %d %s", rec.Code, rec.Body)
					}
				}
				if mode != "absent" {
					body["mode"] = mode
				}
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				rec := callTestPolicy(t, router, "/api/v1", http.MethodPost, "/limits/"+policy+"?missingAccount=reject", string(raw))
				if mode == "unknown" {
					if rec.Code != http.StatusUnprocessableEntity {
						t.Fatalf("unknown mode = %d: %s", rec.Code, rec.Body)
					}
					problem := bodyMap(t, rec.Result())["errors"].([]any)[0].(map[string]any)
					if problem["code"] != "validation" || problem["pointer"] != "/mode" {
						t.Fatalf("mode problem = %v", problem)
					}
					return
				}
				wantMode := mode
				if mode == "absent" || mode == "" {
					wantMode = "reject"
				}
				if rec.Code != http.StatusCreated {
					t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
				}
				if got := bodyMap(t, rec.Result())[field].(map[string]any)["mode"]; got != wantMode {
					t.Fatalf("mode = %v, want %s", got, wantMode)
				}
				duplicate := callTestPolicy(t, router, "/api/v1", http.MethodPost, "/limits/"+policy+"?missingAccount=reject", string(raw))
				if duplicate.Code != http.StatusConflict {
					t.Fatalf("duplicate POST = %d: %s", duplicate.Code, duplicate.Body)
				}
			})
		}
	}
}

func TestCountingLimitRESTWriteAnswersOwnMode(t *testing.T) {
	for _, tc := range []struct {
		path, field, body, threshold string
		reject, review               any
	}{
		{"rate", "rateLimit", `{"scope":"broker","windowMs":1000,"maxOrders":%v,"mode":%q}`, "maxOrders", float64(10), float64(5)},
		{"position", "positionLimit", `{"scope":"account_asset","account":"acc-1","asset":"USD","maxPosition":%q,"mode":%q}`, "maxPosition", "10", "5"},
		{"spot-funds-pnl-bounds", "spotFundsPnlBoundsLimit", `{"scope":"global","currency":"USD","lowerBound":%q,"mode":%q}`, "lowerBound", "-10", "-5"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			router, err := newRouter(newTestPolicyService(t))
			if err != nil {
				t.Fatal(err)
			}
			if rec := callTestPolicy(t, router, "/api/v1", http.MethodPost, "/assets", `{"code":"USD"}`); rec.Code != http.StatusCreated {
				t.Fatalf("create asset: %d %s", rec.Code, rec.Body)
			}
			if rec := callTestPolicy(t, router, "/api/v1", http.MethodPost, "/accounts", `{"code":"acc-1"}`); rec.Code != http.StatusCreated {
				t.Fatalf("create account: %d %s", rec.Code, rec.Body)
			}
			path := "/limits/" + tc.path + "?missingAccount=reject"
			for _, write := range []struct {
				method, mode string
				status       int
				threshold    any
			}{
				{http.MethodPost, "reject", http.StatusCreated, tc.reject},
				{http.MethodPost, "review_with_hold", http.StatusCreated, tc.review},
				{http.MethodPut, "review_with_hold", http.StatusOK, tc.review},
				{http.MethodPut, "reject", http.StatusOK, tc.reject},
			} {
				body := fmt.Sprintf(tc.body, write.threshold, write.mode)
				rec := callTestPolicy(t, router, "/api/v1", write.method, path, body)
				if rec.Code != write.status {
					t.Fatalf("%s %s = %d, want %d: %s", write.method, write.mode, rec.Code, write.status, rec.Body)
				}
				barrier, _ := bodyMap(t, rec.Result())[tc.field].(map[string]any)
				if barrier["mode"] != write.mode || barrier[tc.threshold] != write.threshold {
					t.Fatalf("%s %s returned %v, want %s=%v", write.method, write.mode, barrier, tc.threshold, write.threshold)
				}
			}
		})
	}
}

func TestCountingLimitRESTFilterAndDelete(t *testing.T) {
	for _, policy := range []string{domain.PolicyRateLimit, domain.PolicyPositionLimit,
		domain.PolicySpotFundsPnlBoundsKillSwitch, domain.PolicySpotFundsUnrealizedPnlBoundsKillSwitch,
		domain.PolicySpotFundsTotalPnlBoundsKillSwitch} {
		for _, queryMode := range []string{"", "reject", "review_with_hold", "review_without_hold", "unknown"} {
			t.Run(policy+"/"+queryMode, func(t *testing.T) {
				router, realm := newRealServiceRouter(t)
				if _, err := realm.CreateAsset(t.Context(), domain.Asset{Code: "AAPL"}); err != nil {
					t.Fatal(err)
				}
				if _, err := realm.CreateAccount(t.Context(), domain.Account{Code: "acc-1"}); err != nil {
					t.Fatal(err)
				}
				for _, mode := range []domain.RuleMode{domain.RuleModeReject, domain.RuleModeReviewWithHold, domain.RuleModeReviewWithoutHold} {
					var err error
					switch policy {
					case domain.PolicyRateLimit:
						err = realm.PutRateLimit(t.Context(), domain.LimitRate{Mode: mode, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL", Window: time.Second, MaxOrders: 10})
					case domain.PolicyPositionLimit:
						err = realm.PutPositionLimit(t.Context(), domain.LimitPosition{Mode: mode, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL", MaxPosition: "10"})
					default:
						axis, ok := domain.SpotFundsPnlAxisOf(policy)
						if !ok {
							t.Fatal("missing P&L axis")
						}
						err = realm.PutSpotFundsPnlBoundsLimit(t.Context(), domain.LimitSpotFundsPnlBounds{Mode: mode, Axis: axis, Scope: domain.ScopeGlobal, Currency: "AAPL", LowerBound: "-10"})
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				shortPolicy := "rate"
				if policy == domain.PolicyPositionLimit {
					shortPolicy = "position"
				} else if axis, ok := domain.SpotFundsPnlAxisOf(policy); ok {
					shortPolicy = "spot_funds_" + string(axis) + "_pnl_bounds"
					if axis == domain.SpotFundsPnlAxisRealized {
						shortPolicy = "spot_funds_pnl_bounds"
					}
				}
				if queryMode != "" && queryMode != "unknown" {
					rec := callTestPolicy(t, router, "/api/v1", http.MethodGet, "/limits?policy="+shortPolicy+"&mode="+queryMode, "")
					if rec.Code != http.StatusOK {
						t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
					}
					body := bodyMap(t, rec.Result())
					rows := body["policies"].([]any)
					if len(rows) != 1 || body["total"] != float64(1) || rows[0].(map[string]any)["mode"] != queryMode {
						t.Fatalf("mode-filtered list = %v", body)
					}
				}
				deleteRouter, err := newRouter(&orderSizeDeleteService{fakeService: &fakeService{}, realm: realm})
				if err != nil {
					t.Fatal(err)
				}
				query := "/limits?policy=" + policy + "&scope=account_asset&account=acc-1&asset=AAPL"
				if _, ok := domain.SpotFundsPnlAxisOf(policy); ok {
					query = "/limits?policy=" + policy + "&scope=global"
				}
				if queryMode != "" {
					query += "&mode=" + queryMode
				}
				rec := callTestPolicy(t, deleteRouter, "/api/v1", http.MethodDelete, query, "")
				wantMode := domain.RuleMode(queryMode)
				if wantMode == "" {
					wantMode = domain.RuleModeReject
				}
				if queryMode == "unknown" {
					if rec.Code != http.StatusUnprocessableEntity {
						t.Fatalf("invalid delete = %d: %s", rec.Code, rec.Body)
					}
				} else if rec.Code != http.StatusNoContent {
					t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body)
				}
				var remaining []domain.KeyedLimit
				switch policy {
				case domain.PolicyRateLimit:
					limits, err := realm.ListRateLimits(t.Context(), "")
					if err != nil {
						t.Fatal(err)
					}
					for _, limit := range limits {
						remaining = append(remaining, limit)
					}
				case domain.PolicyPositionLimit:
					limits, err := realm.ListPositionLimits(t.Context(), "")
					if err != nil {
						t.Fatal(err)
					}
					for _, limit := range limits {
						remaining = append(remaining, limit)
					}
				default:
					limits, err := realm.ListSpotFundsPnlBoundsLimits(t.Context(), "")
					if err != nil {
						t.Fatal(err)
					}
					for _, limit := range limits {
						remaining = append(remaining, limit)
					}
				}
				wantCount := 2
				if queryMode == "unknown" {
					wantCount = 3
				}
				if len(remaining) != wantCount {
					t.Fatalf("remaining = %v", remaining)
				}
				for _, limit := range remaining {
					key, err := limit.Key()
					if err != nil || key.Mode() == wantMode {
						t.Fatalf("remaining key = %v, %v", key, err)
					}
				}
			})
		}
	}
}

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
// forwards it verbatim, and answers the value returned by the source,
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
			svc := &fakeService{limitModeResult: domain.LimitSpotFundsLimitMode{
				Scope: domain.ScopeGlobal, Mode: domain.SpotFundsLimitModeEnforce,
			}}
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
			if svc.listLimitsCalls != 0 || svc.spotFundsLimitModeGetCalls != 0 {
				t.Fatalf("extra reads = (list %d, effective %d), want none",
					svc.listLimitsCalls, svc.spotFundsLimitModeGetCalls)
			}
			const wantBody = `{"spotFundsLimitMode":{"scope":"global","mode":"enforce"}}`
			if got := strings.TrimSpace(rec.Body.String()); got != wantBody {
				t.Fatalf("body = %s, want %s", got, wantBody)
			}
		})
	}
}

// TestSpotFundsLimitModeRESTReviewRoundTrip proves both review modes survive PUT and GET.
func TestSpotFundsLimitModeRESTReviewRoundTrip(t *testing.T) {
	router, _ := newRealServiceRouter(t)
	const path = "/limits/spot-funds-limit-mode"
	for _, mode := range []string{"review_with_hold", "review_without_hold"} {
		t.Run(mode, func(t *testing.T) {
			rec := callTestPolicy(t, router, "/api/v1", http.MethodPut, path, fmt.Sprintf(`{"scope":"global","mode":%q}`, mode))
			if rec.Code != http.StatusOK {
				t.Fatalf("PUT = %d: %s", rec.Code, rec.Body)
			}
			stored, _ := bodyMap(t, rec.Result())["spotFundsLimitMode"].(map[string]any)
			if stored["mode"] != mode || stored["scope"] != "global" {
				t.Fatalf("PUT returned %v, want global %s", stored, mode)
			}
			rec = callTestPolicy(t, router, "/api/v1", http.MethodGet, path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
			}
			state := bodyMap(t, rec.Result())
			if state["mode"] != mode || state["stored"] != true {
				t.Fatalf("GET returned %v, want stored %s", state, mode)
			}
		})
	}
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

// TestPutSpotFundsLimitModeReadBackError checks that a read-back failure after
// the write was stored answers an internal error.
func TestPutSpotFundsLimitModeReadBackError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"internal", domain.Internal(fmt.Errorf("limit mode write was stored but its read-back did not find it")), http.StatusInternalServerError, "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{putSpotFundsLimitModeErr: tc.err}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/spot-funds-limit-mode",
				strings.NewReader(`{"scope":"global","mode":"enforce"}`)))
			problem, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
			if rec.Code != tc.status || problem["code"] != tc.code ||
				svc.spotFundsLimitModePutCalls != 1 || svc.listLimitsCalls != 0 {
				t.Fatalf("status = %d, error = %v, put calls = %d, list calls = %d",
					rec.Code, problem, svc.spotFundsLimitModePutCalls, svc.listLimitsCalls)
			}
		})
	}
}

// TestDeleteSpotFundsLimitMode proves the permanent-policy refusal is mapped
// to a validation response by DELETE /limits.
func TestDeleteSpotFundsLimitMode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"permanent", fmt.Errorf("funds limit mode is permanent: %w", domain.ErrInvalid), http.StatusUnprocessableEntity},
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
					Mode:  domain.RuleModeReject,
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
				SpotFundsPnlBounds: &domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject,
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
	const want = `{"policies":[{"kind":"spot_funds_limit_mode","mode":null,"scope":"global","account":"",` +
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

func TestListLimits_PositionSort(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/limits?policy=position&sort=maxPosition&order=desc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.policyFilter.Kind == nil || *svc.policyFilter.Kind != store.PolicyKindPosition {
		t.Fatalf("kind filter = %v", svc.policyFilter.Kind)
	}
	if svc.policyFilter.Sort.Column != "maxPosition" || !svc.policyFilter.Sort.Descending {
		t.Fatalf("sort spec = %+v", svc.policyFilter.Sort)
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

func TestKeyedLimitRESTWrites(t *testing.T) {
	const (
		pnlFirst   = `{"scope":"global","currency":"USD","lowerBound":"-1000","upperBound":"5000"}`
		pnlChanged = `{"scope":"global","currency":"USD","lowerBound":"-2000","upperBound":"6000"}`
	)
	pnlFirstValues := map[string]any{"currency": "USD", "lowerBound": "-1000", "upperBound": "5000"}
	pnlChangedValues := map[string]any{"currency": "USD", "lowerBound": "-2000", "upperBound": "6000"}

	for _, tc := range []struct {
		path, policy, scope, resultField, valuesField string
		asset, query                                  string
		first, changed                                string
		firstValues, changedValues                    map[string]any
	}{
		{
			path: "rate", policy: "rate", scope: "broker",
			resultField: "rateLimit", valuesField: "rate",
			first:         `{"scope":"broker","windowMs":1000,"maxOrders":100}`,
			changed:       `{"scope":"broker","windowMs":2000,"maxOrders":200}`,
			firstValues:   map[string]any{"windowMs": float64(1000), "maxOrders": float64(100)},
			changedValues: map[string]any{"windowMs": float64(2000), "maxOrders": float64(200)},
		},
		{
			path: "order-size", policy: "order_size", scope: "account_underlying_asset",
			resultField: "orderSizeLimit", valuesField: "orderSize",
			asset: "AAPL", query: "?missingAccount=create",
			first:         `{"scope":"account_underlying_asset","account":"acc-1","asset":"AAPL","maxQuantity":"10"}`,
			changed:       `{"scope":"account_underlying_asset","account":"acc-1","asset":"AAPL","maxQuantity":"20"}`,
			firstValues:   map[string]any{"maxQuantity": "10", "maxNotional": ""},
			changedValues: map[string]any{"maxQuantity": "20", "maxNotional": ""},
		},
		{
			path: "position", policy: "position", scope: "account_asset",
			resultField: "positionLimit", valuesField: "position",
			asset: "AAPL", query: "?missingAccount=create",
			first:         `{"scope":"account_asset","account":"acc-1","asset":"AAPL","maxPosition":"10"}`,
			changed:       `{"scope":"account_asset","account":"acc-1","asset":"AAPL","maxPosition":"20"}`,
			firstValues:   map[string]any{"maxPosition": "10"},
			changedValues: map[string]any{"maxPosition": "20"},
		},
		{
			path: "spot-funds-pnl-bounds", policy: "spot_funds_pnl_bounds", scope: "global",
			resultField: "spotFundsPnlBoundsLimit", valuesField: "spotFundsPnlBounds",
			asset: "USD", first: pnlFirst, changed: pnlChanged,
			firstValues: pnlFirstValues, changedValues: pnlChangedValues,
		},
		{
			path: "spot-funds-unrealized-pnl-bounds", policy: "spot_funds_unrealized_pnl_bounds", scope: "global",
			resultField: "spotFundsUnrealizedPnlBoundsLimit", valuesField: "spotFundsPnlBounds",
			asset: "USD", first: pnlFirst, changed: pnlChanged,
			firstValues: pnlFirstValues, changedValues: pnlChangedValues,
		},
		{
			path: "spot-funds-total-pnl-bounds", policy: "spot_funds_total_pnl_bounds", scope: "global",
			resultField: "spotFundsTotalPnlBoundsLimit", valuesField: "spotFundsPnlBounds",
			asset: "USD", first: pnlFirst, changed: pnlChanged,
			firstValues: pnlFirstValues, changedValues: pnlChangedValues,
		},
	} {
		t.Run(tc.path, func(t *testing.T) {
			for _, create := range []struct {
				method string
				status int
			}{
				{http.MethodPost, http.StatusCreated},
				{http.MethodPut, http.StatusOK},
			} {
				t.Run(create.method, func(t *testing.T) {
					router, err := newRouter(newTestPolicyService(t))
					if err != nil {
						t.Fatal(err)
					}
					if tc.asset != "" {
						rec := callTestPolicy(t, router, "/api/v1", http.MethodPost, "/assets", `{"code":"`+tc.asset+`"}`)
						if rec.Code != http.StatusCreated {
							t.Fatalf("create %s asset = %d: %s", tc.asset, rec.Code, rec.Body)
						}
					}
					writePath := "/limits/" + tc.path + tc.query
					write := func(method, body string) *httptest.ResponseRecorder {
						return callTestPolicy(t, router, "/api/v1", method, writePath, body)
					}
					requireResult := func(rec *httptest.ResponseRecorder, status int, want map[string]any) {
						t.Helper()
						if rec.Code != status {
							t.Fatalf("write status = %d, want %d: %s", rec.Code, status, rec.Body)
						}
						result, _ := bodyMap(t, rec.Result())[tc.resultField].(map[string]any)
						if result["scope"] != tc.scope {
							t.Fatalf("%s = %v, want scope %s", tc.resultField, result, tc.scope)
						}
						for field, value := range want {
							if result[field] != value {
								t.Fatalf("%s.%s = %v, want %v", tc.resultField, field, result[field], value)
							}
						}
					}
					requireStored := func(want map[string]any) {
						t.Helper()
						rec := callTestPolicy(t, router, "/api/v1", http.MethodGet, "/limits?policy="+tc.policy, "")
						if rec.Code != http.StatusOK {
							t.Fatalf("GET /limits = %d: %s", rec.Code, rec.Body)
						}
						policies, _ := bodyMap(t, rec.Result())["policies"].([]any)
						if len(policies) != 1 {
							t.Fatalf("GET /limits policies = %v, want one rule", policies)
						}
						rule, _ := policies[0].(map[string]any)
						values, _ := rule["values"].(map[string]any)
						if rule["scope"] != tc.scope || !reflect.DeepEqual(values[tc.valuesField], want) {
							t.Fatalf("stored rule = %v, want scope %s with values %v", rule, tc.scope, want)
						}
					}

					requireResult(write(create.method, tc.first), create.status, tc.firstValues)
					requireStored(tc.firstValues)
					if create.method == http.MethodPost {
						rec := write(http.MethodPost, tc.changed)
						if rec.Code != http.StatusConflict {
							t.Errorf("repeated POST = %d, want 409: %s", rec.Code, rec.Body)
						} else {
							failure, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
							if failure["code"] != "limit_exists" {
								t.Errorf("repeated POST error = %v, want code limit_exists", failure)
							}
						}
						requireStored(tc.firstValues)
						requireResult(write(http.MethodPut, tc.changed), http.StatusOK, tc.changedValues)
						requireStored(tc.changedValues)
					}
				})
			}
		})
	}
}

func TestPutRateLimit(t *testing.T) {
	svc := &fakeService{rateLimitResult: domain.LimitRate{Mode: domain.RuleModeReject,
		Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
		Window: 2 * time.Second, MaxOrders: 101,
	}}
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
	// The handler forwards the typed barrier and answers the source result.
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
	svc := &fakeService{orderSizeLimitResult: domain.LimitOrderSize{
		Mode: domain.RuleModeReject, Scope: domain.ScopeAccountSettlementAsset,
		Account: "acc-1", Asset: "USD", MaxNotional: "50001",
	}}
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
	if svc.orderSizeLimitPut.Mode != domain.RuleModeReject ||
		svc.orderSizeLimitPut.Scope != domain.ScopeAccountSettlementAsset ||
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
	if osl["mode"] != "reject" || osl["scope"] != "account_settlement_asset" ||
		osl["maxQuantity"] != "" || osl["maxNotional"] != "50001" {
		t.Fatalf("unexpected persisted orderSizeLimit: %v", osl)
	}
}

func TestOrderSizeLimitModeDTO(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  domain.RuleMode
	}{
		{"", domain.RuleModeReject},
		{"reject", domain.RuleModeReject},
		{"review_without_hold", domain.RuleModeReviewWithoutHold},
		{"unknown", domain.RuleMode("unknown")},
	} {
		t.Run(tc.input, func(t *testing.T) {
			limit := httpx.FromOrderSizeLimitDTO(httpx.OrderSizeLimitDTO{Mode: tc.input})
			if limit.Mode != tc.want {
				t.Fatalf("decoded mode = %q, want %q", limit.Mode, tc.want)
			}
			if got := httpx.ToOrderSizeLimitDTO(limit).Mode; got != string(tc.want) {
				t.Fatalf("encoded mode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOrderSizeLimitModeRESTWrite(t *testing.T) {
	svc := newTestPolicyService(t)
	router, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	write := func(method, body string, status int, mode, quantity string) {
		t.Helper()
		rec := callTestPolicy(t, router, "/api/v1", method, "/limits/order-size", body)
		if rec.Code != status {
			t.Fatalf("%s order-size = %d, want %d: %s", method, rec.Code, status, rec.Body)
		}
		result := bodyMap(t, rec.Result())["orderSizeLimit"].(map[string]any)
		if result["mode"] != mode || result["maxQuantity"] != quantity {
			t.Fatalf("stored response = %v, want mode %s, quantity %s", result, mode, quantity)
		}
	}
	write(http.MethodPost, `{"scope":"broker","maxQuantity":"10"}`, http.StatusCreated, "reject", "10")
	const review = `{"scope":"broker","mode":"review_without_hold","maxQuantity":"20"}`
	write(http.MethodPost, review, http.StatusCreated, "review_without_hold", "20")
	rec := callTestPolicy(t, router, "/api/v1", http.MethodPost, "/limits/order-size", review)
	if rec.Code != http.StatusConflict || bodyMap(t, rec.Result())["error"].(map[string]any)["code"] != "limit_exists" {
		t.Fatalf("duplicate review POST = %d: %s", rec.Code, rec.Body)
	}
	write(http.MethodPut, `{"scope":"broker","mode":"review_without_hold","maxQuantity":"15"}`, http.StatusOK, "review_without_hold", "15")
	rec = callTestPolicy(t, router, "/api/v1", http.MethodGet, "/limits?policy=order_size", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list coexisting barriers = %d: %s", rec.Code, rec.Body)
	}
	limits := bodyMap(t, rec.Result())["policies"].([]any)
	if len(limits) != 2 {
		t.Fatalf("coexisting barriers = %v, want both modes", limits)
	}
	for _, value := range limits {
		limit := value.(map[string]any)
		want := "10"
		if limit["mode"] == "review_without_hold" {
			want = "15"
		} else if limit["mode"] != "reject" {
			t.Fatalf("unexpected barrier mode: %+v", limit)
		}
		quantity := limit["values"].(map[string]any)["orderSize"].(map[string]any)["maxQuantity"]
		if quantity != want {
			t.Fatalf("mode %s quantity = %v, want %s", limit["mode"], quantity, want)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		rec := callTestPolicy(t, router, "/api/v1", method, "/limits/order-size", `{"scope":"broker","mode":"unknown","maxQuantity":"5"}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s unknown mode = %d: %s", method, rec.Code, rec.Body)
		}
		problem := bodyMap(t, rec.Result())["errors"].([]any)[0].(map[string]any)
		if problem["code"] != "validation" || problem["pointer"] != "/mode" {
			t.Fatalf("unknown mode problem = %v", problem)
		}
	}
}

func TestListLimitsModeValidation(t *testing.T) {
	for _, query := range []string{
		"mode=review_without_hold",
		"policy=all&mode=review_without_hold",
		"policy=rate&mode=unknown",
		"policy=spot_funds_limit_mode&mode=reject",
		"policy=order_size&mode=unknown",
	} {
		t.Run(query, func(t *testing.T) {
			router, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := callTestPolicy(t, router, "/api/v1", http.MethodGet, "/limits?"+query, "")
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("GET /limits?%s = %d, want 422: %s", query, rec.Code, rec.Body)
			}
			problem := bodyMap(t, rec.Result())["errors"].([]any)[0].(map[string]any)
			if problem["code"] != "validation" || problem["pointer"] != "/mode" {
				t.Fatalf("mode filter problem = %v", problem)
			}
		})
	}
}

func TestListLimitsModeFilter(t *testing.T) {
	router, err := newRouter(newTestPolicyService(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, setup := range []struct{ path, body string }{
		{"/assets", `{"code":"USD"}`},
		{"/limits/order-size", `{"scope":"broker","maxQuantity":"10"}`},
		{"/limits/order-size", `{"scope":"broker","mode":"review_without_hold","maxQuantity":"20"}`},
		{"/limits/order-size", `{"scope":"underlying_asset","asset":"USD","mode":"review_without_hold","maxQuantity":"30"}`},
	} {
		rec := callTestPolicy(t, router, "/api/v1", http.MethodPost, setup.path, setup.body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("setup %s = %d: %s", setup.path, rec.Code, rec.Body)
		}
	}
	for _, tc := range []struct {
		query, mode string
		rows, total int
	}{
		{"policy=order_size&mode=review_without_hold", "review_without_hold", 2, 2},
		{"policy=order_size&mode=review_without_hold&limit=1", "review_without_hold", 1, 2},
		{"policy=order_size&mode=reject", "reject", 1, 1},
		{"policy=order_size&mode=", "", 3, 3},
	} {
		t.Run(tc.query, func(t *testing.T) {
			rec := callTestPolicy(t, router, "/api/v1", http.MethodGet, "/limits?"+tc.query, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("list = %d: %s", rec.Code, rec.Body)
			}
			body := bodyMap(t, rec.Result())
			rows := body["policies"].([]any)
			if len(rows) != tc.rows || body["total"] != float64(tc.total) {
				t.Fatalf("filtered list = %v, want %d rows, total %d", body, tc.rows, tc.total)
			}
			for _, value := range rows {
				row := value.(map[string]any)
				if row["kind"] != "order_size_limit" || (tc.mode != "" && row["mode"] != tc.mode) || row["mode"] == nil {
					t.Fatalf("mode-filtered row = %v, want mode %s", row, tc.mode)
				}
			}
		})
	}
}

func TestPutPositionLimit(t *testing.T) {
	for _, value := range []string{"0", "123.000000000000000001"} {
		t.Run(value, func(t *testing.T) {
			limit := domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "BTC", MaxPosition: value}
			stored := limit
			stored.MaxPosition = "321.000000000000000002"
			svc := &fakeService{positionLimitResult: stored}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			body := bytes.NewBufferString(`{"scope":"account_asset","account":"acc-1","asset":"BTC","maxPosition":"` + value + `"}`)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/position?missingAccount=reject", body))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body)
			}
			if svc.positionLimitPut != limit || svc.missingAccount != domain.MissingAccountReject {
				t.Fatalf("captured position limit = %+v, missing = %q", svc.positionLimitPut, svc.missingAccount)
			}
			m := bodyMap(t, rec.Result())
			position, ok := m["positionLimit"].(map[string]any)
			if !ok || len(m) != 1 || len(position) != 5 || position["mode"] != "reject" || position["scope"] != "account_asset" || position["account"] != "acc-1" || position["asset"] != "BTC" || position["maxPosition"] != stored.MaxPosition {
				t.Fatalf("unexpected persisted positionLimit: %v", m)
			}
		})
	}
}

func TestPutPositionLimit_ValidationError(t *testing.T) {
	for _, value := range []string{"-1", "not-a-decimal"} {
		t.Run(value, func(t *testing.T) {
			limit := domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "BTC", MaxPosition: value}
			svc := &fakeService{putLimErr: limit.Validate()}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			body := bytes.NewBufferString(`{"scope":"account_asset","account":"acc-1","asset":"BTC","maxPosition":"` + value + `"}`)
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/position?missingAccount=create", body))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("want 422, got %d body=%s", rec.Code, rec.Body)
			}
			m := bodyMap(t, rec.Result())
			problems, ok := m["errors"].([]any)
			if !ok || len(problems) == 0 {
				t.Fatalf("missing validation problems: %v", m)
			}
			if problem := problems[0].(map[string]any); problem["code"] != "validation" {
				t.Fatalf("unexpected validation problem: %v", problem)
			}
			if svc.positionLimitPut != limit || svc.listLimitsCalls != 0 {
				t.Fatalf("invalid put = %+v, read-back calls = %d", svc.positionLimitPut, svc.listLimitsCalls)
			}
		})
	}
}

func TestPutPositionLimitReadBackError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"missing stored barrier", domain.Internal(fmt.Errorf("limit write was stored but its read-back did not find it")), http.StatusInternalServerError},
		{"write target missing", domain.ErrNotFound, http.StatusNotFound},
		{"read failed", fmt.Errorf("read failed"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{putLimErr: tc.err}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			body := bytes.NewBufferString(`{"scope":"account_asset","account":"acc-1","asset":"BTC","maxPosition":"1"}`)
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/position?missingAccount=create", body))
			if rec.Code != tc.status {
				t.Fatalf("want %d, got %d body=%s", tc.status, rec.Code, rec.Body)
			}
		})
	}
}

func TestListLimitsPositionValues(t *testing.T) {
	for _, policy := range []string{"position", "all"} {
		t.Run(policy, func(t *testing.T) {
			limit := domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "BTC", MaxPosition: "123.000000000000000001"}
			svc := &fakeService{policyRows: []store.PolicyListRow{{Kind: store.PolicyKindPosition, Scope: limit.Scope, Account: limit.Account, Asset: limit.Asset, Position: &limit}}}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/limits?policy="+policy, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body)
			}
			if policy == "position" {
				if svc.policyFilter.Kind == nil || *svc.policyFilter.Kind != store.PolicyKindPosition {
					t.Fatalf("kind filter = %v, want position", svc.policyFilter.Kind)
				}
			} else if svc.policyFilter.Kind != nil {
				t.Fatalf("all kind filter = %v, want nil", svc.policyFilter.Kind)
			}
			m := bodyMap(t, rec.Result())
			policies, ok := m["policies"].([]any)
			if !ok || len(policies) != 1 || m["total"] != float64(1) {
				t.Fatalf("unexpected policy list: %v", m)
			}
			row := policies[0].(map[string]any)
			values := row["values"].(map[string]any)
			position, ok := values["position"].(map[string]any)
			if row["kind"] != "position_limit" || row["scope"] != "account_asset" || row["account"] != "acc-1" || row["asset"] != "BTC" || !ok || len(values) != 1 || position["maxPosition"] != limit.MaxPosition {
				t.Fatalf("unexpected position row: %v", row)
			}
		})
	}
}

func TestPutSpotFundsPnlBoundsLimit(t *testing.T) {
	svc := &fakeService{pnlBoundsLimitResult: domain.LimitSpotFundsPnlBounds{
		Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized,
		Scope: domain.ScopeAccountGroup, AccountGroup: "desk-a",
		Currency: "USD", LowerBound: "-999", UpperBound: "5001",
	}}
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
	svc := &fakeService{pnlBoundsLimitResult: domain.LimitSpotFundsPnlBounds{
		Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized,
		Scope: domain.ScopeAccount, Account: "acc-1",
		Currency: "EUR", LowerBound: "-999", UpperBound: "5001",
	}}
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

func TestDeletePositionLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"deleted", nil, http.StatusNoContent},
		{"absent", domain.ErrNotFound, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{delLimErr: tc.err}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/limits?policy=position_limit&scope=account_asset&account=acc-1&asset=BTC", nil))
			if rec.Code != tc.status {
				t.Fatalf("want %d, got %d body=%s", tc.status, rec.Code, rec.Body)
			}
			want := node.LimitTarget{Mode: domain.RuleModeReject, Policy: domain.PolicyPositionLimit, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "BTC"}
			if svc.deleteLimitTarget != want {
				t.Fatalf("delete target = %+v, want %+v", svc.deleteLimitTarget, want)
			}
		})
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
			{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
				Window: time.Second, MaxOrders: 100},
		},
		OrderSizeLimits: []domain.LimitOrderSize{
			{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, MaxQuantity: "500", MaxNotional: "50000"},
		},
		PositionLimits: []domain.LimitPosition{{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "BTC", MaxPosition: "1.000000000000000001"}},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			{Mode: domain.RuleModeReject, Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeAccount, Account: "acc-1",
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
	for _, key := range []string{"mode", "scope", "account", "asset", "maxQuantity", "maxNotional"} {
		if _, ok := size[key]; !ok {
			t.Fatalf("OrderSizeLimitDTO missing JSON key %q", key)
		}
	}
	if size["mode"] != "reject" {
		t.Fatalf("order-size mode = %v, want reject", size["mode"])
	}
	positions, ok := m["positionLimits"].([]any)
	if !ok || len(positions) != 1 {
		t.Fatalf("want 1 position limit, got %v", m["positionLimits"])
	}
	position := positions[0].(map[string]any)
	if len(position) != 5 || position["mode"] != "reject" || position["scope"] != "account_asset" || position["account"] != "acc-1" || position["asset"] != "BTC" || position["maxPosition"] != "1.000000000000000001" {
		t.Fatalf("unexpected position limit: %v", position)
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
