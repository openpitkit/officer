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
	"net/http"
	"net/http/httptest"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
)

func TestVerifyMarketDataSymbol_Unsupported(t *testing.T) {
	svc := &fakeService{
		mdVerify: backend.MarketDataSymbolVerification{Supported: false},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"externalSymbol":"AAPL"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/market-data/instances/byo-1/verify-symbol",
		body,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	v, ok := m["verification"].(map[string]any)
	if !ok {
		t.Fatalf("want verification object, got %v", m["verification"])
	}
	if v["supported"] != false {
		t.Fatalf("want supported=false, got %v", v["supported"])
	}
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != "verify-symbol:byo-1/AAPL" {
		t.Fatalf("unexpected service calls: %v", svc.mdCalls)
	}
}

func TestVerifyMarketDataSymbol_SuggestionFlows(t *testing.T) {
	svc := &fakeService{
		mdVerify: backend.MarketDataSymbolVerification{
			Supported: true, Exists: false, Suggestion: "ETHUSDT",
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"externalSymbol":"ethusdt"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/market-data/instances/bn-1/verify-symbol",
		body,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	v, _ := m["verification"].(map[string]any)
	if v["supported"] != true || v["exists"] != false {
		t.Fatalf("want supported=true exists=false, got %v", v)
	}
	if v["suggestion"] != "ETHUSDT" {
		t.Fatalf("want suggestion=ETHUSDT, got %v", v["suggestion"])
	}
}

// TestDeleteMarketDataInstance_HasDependents asserts the same 409 wire shape for
// the market-data instance delete endpoint.
func TestDeleteMarketDataInstance_HasDependents(t *testing.T) {
	svc := &fakeService{stateErr: domain.NewHasDependentsError([]domain.DependentCount{
		{Kind: "instruments", Count: 2},
	})}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/market-data/instances/inst-1", nil))
	assertHasDependents409(t, rec, "instruments", 2)
}
