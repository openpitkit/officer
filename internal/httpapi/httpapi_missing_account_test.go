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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

// missingAccountRoute is one endpoint that names an account in the request and
// therefore requires the missingAccount query parameter.
type missingAccountRoute struct {
	name   string
	method string
	path   string
	body   string
	// ok is the status the endpoint returns once the parameter is supplied.
	ok int
}

// missingAccountRoutes enumerates every surface that takes the parameter. The
// bodies are the minimal valid payload each handler needs to reach the backend.
func missingAccountRoutes() []missingAccountRoute {
	return []missingAccountRoute{
		{
			name:   "limits.rate",
			method: http.MethodPut,
			path:   "/api/v1/limits/rate",
			body: `{"scope":"account","account":"acc-1",` +
				`"windowMs":1000,"maxOrders":100}`,
			ok: http.StatusOK,
		},
		{
			name:   "limits.order-size",
			method: http.MethodPut,
			path:   "/api/v1/limits/order-size",
			body: `{"scope":"account_asset","account":"acc-1",` +
				`"asset":"AAPL","maxQuantity":"10"}`,
			ok: http.StatusOK,
		},
		{
			name:   "limits.spot-funds-pnl-bounds",
			method: http.MethodPut,
			path:   "/api/v1/limits/spot-funds-pnl-bounds",
			body:   `{"scope":"account","account":"acc-1","lowerBound":"-100"}`,
			ok:     http.StatusOK,
		},
		{
			name:   "accounts.adjustments",
			method: http.MethodPost,
			path:   "/api/v1/accounts/acc-1/adjustments",
			body:   `{"asset":"USD","balance":{"mode":"absolute","value":"1"}}`,
			ok:     http.StatusCreated,
		},
		{
			name:   "accounts.balances.realized-pnl",
			method: http.MethodPut,
			path:   "/api/v1/accounts/acc-1/balances/realized-pnl",
			body:   `{"asset":"USD","realizedPnl":"1"}`,
			ok:     http.StatusOK,
		},
		{
			name:   "orders.submit",
			method: http.MethodPost,
			path:   "/api/v1/orders/submit",
			body: `{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD",` +
				`"side":"buy","amountKind":"quantity","amountValue":"1",` +
				`"price":"100"}`,
			ok: http.StatusCreated,
		},
		{
			name:   "orders.drop-copy.submit",
			method: http.MethodPost,
			path:   "/api/v1/orders/drop-copy/submit",
			body: `{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD",` +
				`"side":"buy","amountKind":"quantity","amountValue":"1",` +
				`"price":"100"}`,
			ok: http.StatusCreated,
		},
		{
			name:   "accounts.block",
			method: http.MethodPost,
			path:   "/api/v1/accounts/acc-1/block",
			body:   `{"reason":"risk"}`,
			ok:     http.StatusOK,
		},
		{
			name:   "accounts.unblock",
			method: http.MethodPost,
			path:   "/api/v1/accounts/acc-1/unblock",
			body:   `{}`,
			ok:     http.StatusOK,
		},
		{
			name:   "accounts.group",
			method: http.MethodPut,
			path:   "/api/v1/accounts/acc-1/group",
			body:   `{"group":"desk-a"}`,
			ok:     http.StatusOK,
		},
	}
}

// newMissingAccountService builds a fake service whose canned data satisfies
// every route in missingAccountRoutes. The limit fixtures mirror the request
// bodies because each Put* handler answers from a persisted read-back.
func newMissingAccountService() *fakeService {
	return &fakeService{
		accounts:   []domain.Account{{Code: "acc-1"}},
		adjustment: domain.AccountAdjustmentRecord{Account: "acc-1", Asset: "USD"},
		limits: node.AccountLimits{
			RateLimits: []domain.LimitRate{
				{
					Scope:     domain.ScopeAccount,
					Account:   "acc-1",
					Window:    time.Second,
					MaxOrders: 100,
				},
				{
					Scope:     domain.ScopeBroker,
					Window:    time.Second,
					MaxOrders: 100,
				},
			},
			OrderSizeLimits: []domain.LimitOrderSize{{
				Scope:       domain.ScopeAccountAsset,
				Account:     "acc-1",
				Asset:       "AAPL",
				MaxQuantity: "10",
			}},
			SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{{
				Scope:      domain.ScopeAccount,
				Account:    "acc-1",
				LowerBound: "-100",
			}},
		},
	}
}

func callMissingAccountRoute(
	t *testing.T, svc *fakeService, route missingAccountRoute, query string,
) *httptest.ResponseRecorder {
	t.Helper()
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	var body io.Reader = bytes.NewBufferString(route.body)
	r.ServeHTTP(rec, httptest.NewRequest(route.method, route.path+query, body))
	return rec
}

// TestMissingAccountParameterRequired covers the required-parameter boundary on
// every endpoint that names an account: an absent value is a 400 "validation",
// never a silent default.
func TestMissingAccountParameterRequired(t *testing.T) {
	for _, route := range missingAccountRoutes() {
		t.Run(route.name, func(t *testing.T) {
			rec := callMissingAccountRoute(
				t, newMissingAccountService(), route, "",
			)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
			}
			assertValidationCode(t, rec, "missingAccount is required")
		})
	}
}

// TestMissingAccountParameterUnknownValue covers an unrecognised value on every
// endpoint: a 400 "validation" rather than a fallback to either behaviour.
func TestMissingAccountParameterUnknownValue(t *testing.T) {
	for _, route := range missingAccountRoutes() {
		t.Run(route.name, func(t *testing.T) {
			rec := callMissingAccountRoute(
				t, newMissingAccountService(), route, "?missingAccount=maybe",
			)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
			}
			assertValidationCode(t, rec, "maybe")
		})
	}
}

// TestOrderSubmitRejectsEmptyAccountBeforeMissingAccountPolicy proves malformed
// identity wins over the secondary missing-account choice on both submit paths.
func TestOrderSubmitRejectsEmptyAccountBeforeMissingAccountPolicy(t *testing.T) {
	for _, route := range missingAccountRoutes() {
		if route.name != "orders.submit" &&
			route.name != "orders.drop-copy.submit" {
			continue
		}
		t.Run(route.name, func(t *testing.T) {
			route.body = strings.Replace(route.body, `"account":"acc-1"`, `"account":""`, 1)
			rec := callMissingAccountRoute(
				t, newMissingAccountService(), route, "?missingAccount=reject",
			)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
			}
			assertValidationCode(t, rec, "account id is empty")
		})
	}
}

// TestMissingAccountParameterThreaded covers the happy path: the chosen policy
// reaches the backend verbatim on every endpoint. Drop-copy takes only "create",
// so it is exercised with that value alone.
func TestMissingAccountParameterThreaded(t *testing.T) {
	for _, route := range missingAccountRoutes() {
		for _, policy := range []domain.MissingAccountPolicy{
			domain.MissingAccountCreate,
			domain.MissingAccountReject,
		} {
			if route.name == "orders.drop-copy.submit" &&
				policy == domain.MissingAccountReject {
				continue
			}
			t.Run(route.name+"/"+string(policy), func(t *testing.T) {
				svc := newMissingAccountService()
				rec := callMissingAccountRoute(
					t, svc, route, "?missingAccount="+string(policy),
				)
				if rec.Code != route.ok {
					t.Fatalf("status = %d, want %d (body %s)",
						rec.Code, route.ok, rec.Body)
				}
				if svc.missingAccount != policy {
					t.Fatalf("backend saw %q, want %q",
						svc.missingAccount, policy)
				}
			})
		}
	}
}

// TestMissingAccountRejectReportsAccountMissing covers the reject outcome: the
// backend's ErrAccountMissing becomes a 404 whose code is account_missing and
// whose envelope carries the offending account code as its own field.
func TestMissingAccountRejectReportsAccountMissing(t *testing.T) {
	for _, route := range missingAccountRoutes() {
		if route.name == "orders.drop-copy.submit" {
			continue
		}
		t.Run(route.name, func(t *testing.T) {
			svc := newMissingAccountService()
			missing := fmt.Errorf(
				"backend: %w", domain.NewAccountMissingError("acc-1"),
			)
			svc.stateErr = missing
			svc.putLimErr = missing
			svc.blockErr = missing
			svc.unblockErr = missing
			svc.signingErr = missing

			rec := callMissingAccountRoute(
				t, svc, route, "?missingAccount=reject",
			)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body)
			}
			errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
			if errObj["code"] != "account_missing" {
				t.Fatalf("code = %v, want account_missing", errObj["code"])
			}
			if errObj["account"] != "acc-1" {
				t.Fatalf("account field = %v, want acc-1", errObj["account"])
			}
		})
	}
}

// TestMissingAccountDropCopyRejectsRejectPolicy covers the drop-copy exception:
// the report describes an execution that already happened, so refusing it over
// an unknown account is a 400 "validation" and never reaches the backend.
func TestMissingAccountDropCopyRejectsRejectPolicy(t *testing.T) {
	var route missingAccountRoute
	for _, candidate := range missingAccountRoutes() {
		if candidate.name == "orders.drop-copy.submit" {
			route = candidate
		}
	}
	svc := newMissingAccountService()
	svc.signingErr = fmt.Errorf(
		"backend: drop-copy reports an execution that already happened and "+
			"cannot reject a missing account; use missingAccount=create: %w",
		domain.ErrInvalid,
	)

	rec := callMissingAccountRoute(t, svc, route, "?missingAccount=reject")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
	assertValidationCode(t, rec, "already happened")
}

// TestMissingAccountNotRequiredWithoutAccountAxis covers the scope boundary: a
// barrier that names no account needs no decision, and a value supplied anyway
// is ignored rather than rejected.
func TestMissingAccountNotRequiredWithoutAccountAxis(t *testing.T) {
	brokerRate := missingAccountRoute{
		method: http.MethodPut,
		path:   "/api/v1/limits/rate",
		body:   `{"scope":"broker","windowMs":1000,"maxOrders":100}`,
		ok:     http.StatusOK,
	}
	for _, query := range []string{"", "?missingAccount=reject"} {
		t.Run("query="+query, func(t *testing.T) {
			svc := newMissingAccountService()
			rec := callMissingAccountRoute(t, svc, brokerRate, query)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
			}
			if svc.missingAccount != "" {
				t.Fatalf("backend saw %q, want no choice for a broker barrier",
					svc.missingAccount)
			}
		})
	}
}

func assertValidationCode(
	t *testing.T, rec *httptest.ResponseRecorder, wantMessagePart string,
) {
	t.Helper()
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("code = %v, want validation", errObj["code"])
	}
	message, _ := errObj["message"].(string)
	if !strings.Contains(message, wantMessagePart) {
		t.Fatalf("message %q does not contain %q", message, wantMessagePart)
	}
}
