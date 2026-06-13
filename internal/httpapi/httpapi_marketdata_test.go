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

	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/domain"
)

// sampleMarketData builds a non-empty MarketDataStatus snapshot so the happy
// paths can assert the wire envelope carries the providers, instances, and the
// freshness window.
func sampleMarketData() backend.MarketDataStatus {
	return backend.MarketDataStatus{
		Providers: []backend.MarketDataProvider{
			{Type: "binance", Title: "Binance"},
		},
		Instances: []backend.MarketDataInstanceStatus{
			{
				Instance: domain.MarketDataInstance{
					ID:      "bn-1",
					Type:    "binance",
					Label:   "Primary",
					Enabled: true,
				},
				State: "ok",
			},
		},
		FreshnessSeconds: 30,
	}
}

// --- GET /market-data -------------------------------------------------------

func TestListMarketData_OK(t *testing.T) {
	svc := &fakeService{marketData: sampleMarketData()}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/market-data", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	md, ok := m["marketData"].(map[string]any)
	if !ok {
		t.Fatalf("want marketData object, got %v", m["marketData"])
	}
	providers, _ := md["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("want 1 provider, got %v", md["providers"])
	}
	instances, _ := md["instances"].([]any)
	if len(instances) != 1 {
		t.Fatalf("want 1 instance, got %v", md["instances"])
	}
	inst := instances[0].(map[string]any)
	if inst["id"] != "bn-1" || inst["state"] != "ok" {
		t.Fatalf("unexpected instance: %v", inst)
	}
	if md["freshnessSeconds"].(float64) != 30 {
		t.Fatalf("want freshnessSeconds=30, got %v", md["freshnessSeconds"])
	}
}

func TestListMarketData_ServiceError(t *testing.T) {
	// ListMarketData returns stateErr; a generic error maps to 500/internal.
	svc := &fakeService{stateErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/market-data", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- POST /market-data/restart ----------------------------------------------

func TestRestartMarketData_OK(t *testing.T) {
	svc := &fakeService{marketData: sampleMarketData()}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/restart", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	// The refreshed snapshot is returned in the same envelope as the other
	// market-data mutations.
	m := bodyMap(t, rec.Result())
	if _, ok := m["marketData"].(map[string]any); !ok {
		t.Fatalf("want marketData object, got %v", m["marketData"])
	}
}

func TestRestartMarketData_ServiceError(t *testing.T) {
	// RestartMarketData returns stateErr; the refresh ListMarketData is never
	// reached and the error maps to 500/internal.
	svc := &fakeService{stateErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/restart", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

// --- POST /market-data/instances --------------------------------------------

func TestCreateMarketDataInstance_Created(t *testing.T) {
	svc := &fakeService{marketData: sampleMarketData()}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"id":"bn-2","type":"binance","label":"Backup","enabled":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if _, ok := m["marketData"].(map[string]any); !ok {
		t.Fatalf("want marketData object, got %v", m["marketData"])
	}
	// The instance id off the decoded body reaches the service verbatim.
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != "create:bn-2" {
		t.Fatalf("unexpected service calls: %v", svc.mdCalls)
	}
}

func TestCreateMarketDataInstance_InvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
	if len(svc.mdCalls) != 0 {
		t.Fatalf("invalid JSON must not reach service: %v", svc.mdCalls)
	}
}

func TestCreateMarketDataInstance_ServiceError(t *testing.T) {
	// The backend rejects a malformed instance with ErrInvalid -> 400.
	svc := &fakeService{stateErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"id":"bn-2","type":"binance"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// --- PUT /market-data/instances/{id}/enabled --------------------------------

func TestSetMarketDataInstanceEnabled_Toggle(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		enabled bool
	}{
		{"enable", `{"enabled":true}`, true},
		{"disable", `{"enabled":false}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
				"/api/v1/market-data/instances/bn-1/enabled",
				bytes.NewBufferString(tc.body)))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
			if bodyMap(t, rec.Result())["enabled"] != tc.enabled {
				t.Fatalf("want enabled=%v body", tc.enabled)
			}
			want := fmt.Sprintf("instance:bn-1:%v", tc.enabled)
			if len(svc.mdCalls) != 1 || svc.mdCalls[0] != want {
				t.Fatalf("unexpected service calls: %v", svc.mdCalls)
			}
		})
	}
}

func TestSetMarketDataInstanceEnabled_InvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/market-data/instances/bn-1/enabled",
		bytes.NewBufferString(`{bad`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	if len(svc.mdCalls) != 0 {
		t.Fatalf("invalid JSON must not reach service: %v", svc.mdCalls)
	}
}

func TestSetMarketDataInstanceEnabled_NotFound(t *testing.T) {
	// A wrapped ErrNotFound from the service maps to 404/not_found.
	svc := &fakeService{
		stateErr: fmt.Errorf("instance %q: %w", "bn-x", domain.ErrNotFound),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/market-data/instances/bn-x/enabled",
		bytes.NewBufferString(`{"enabled":true}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

// --- DELETE /market-data/instances/{id} -------------------------------------

func TestDeleteMarketDataInstance_NoContent(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/market-data/instances/bn-1", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != "delete-instance:bn-1" {
		t.Fatalf("unexpected service calls: %v", svc.mdCalls)
	}
}

func TestDeleteMarketDataInstance_URLEncodedID(t *testing.T) {
	// The {id} path parameter is URL-decoded before reaching the service;
	// %2F decodes to '/'.
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/market-data/instances/bn%2F1", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != "delete-instance:bn/1" {
		t.Fatalf("unexpected service calls: %v", svc.mdCalls)
	}
}

func TestDeleteMarketDataInstance_NotFound(t *testing.T) {
	svc := &fakeService{
		stateErr: fmt.Errorf("instance %q: %w", "bn-x", domain.ErrNotFound),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/market-data/instances/bn-x", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

// --- PUT /market-data/instances/{id}/instruments ----------------------------

func TestUpsertMarketDataInstrument_OK(t *testing.T) {
	svc := &fakeService{marketData: sampleMarketData()}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"externalSymbol":"BTCUSDT","baseAsset":"BTC","quoteAsset":"USDT","enabled":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/market-data/instances/bn-1/instruments", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if _, ok := m["marketData"].(map[string]any); !ok {
		t.Fatalf("want marketData object, got %v", m["marketData"])
	}
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != "upsert-instrument:BTCUSDT" {
		t.Fatalf("unexpected service calls: %v", svc.mdCalls)
	}
}

func TestUpsertMarketDataInstrument_InvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/market-data/instances/bn-1/instruments",
		bytes.NewBufferString(`{bad`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	if len(svc.mdCalls) != 0 {
		t.Fatalf("invalid JSON must not reach service: %v", svc.mdCalls)
	}
}

func TestUpsertMarketDataInstrument_ServiceError(t *testing.T) {
	// A malformed instrument the backend rejects with ErrInvalid -> 400.
	svc := &fakeService{stateErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"externalSymbol":"BTCUSDT"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/market-data/instances/bn-1/instruments", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// --- PUT /market-data/instances/{id}/instruments/enabled --------------------

func TestSetMarketDataInstrumentEnabled_Toggle(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		enabled bool
	}{
		{"enable", `{"externalSymbol":"BTCUSDT","enabled":true}`, true},
		{"disable", `{"externalSymbol":"BTCUSDT","enabled":false}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
				"/api/v1/market-data/instances/bn-1/instruments/enabled",
				bytes.NewBufferString(tc.body)))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
			if bodyMap(t, rec.Result())["enabled"] != tc.enabled {
				t.Fatalf("want enabled=%v body", tc.enabled)
			}
			want := fmt.Sprintf("instrument:bn-1/BTCUSDT:%v", tc.enabled)
			if len(svc.mdCalls) != 1 || svc.mdCalls[0] != want {
				t.Fatalf("unexpected service calls: %v", svc.mdCalls)
			}
		})
	}
}

func TestSetMarketDataInstrumentEnabled_InvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/market-data/instances/bn-1/instruments/enabled",
		bytes.NewBufferString(`{bad`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	if len(svc.mdCalls) != 0 {
		t.Fatalf("invalid JSON must not reach service: %v", svc.mdCalls)
	}
}

func TestSetMarketDataInstrumentEnabled_NotFound(t *testing.T) {
	svc := &fakeService{
		stateErr: fmt.Errorf("instrument %q: %w", "BTCUSDT", domain.ErrNotFound),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/market-data/instances/bn-1/instruments/enabled",
		bytes.NewBufferString(`{"externalSymbol":"BTCUSDT","enabled":true}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

// --- DELETE /market-data/instances/{id}/instruments -------------------------

func TestDeleteMarketDataInstrument_NoContent(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/market-data/instances/bn-1/instruments?externalSymbol=BTCUSDT",
		nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	// The external symbol comes off the query string; the id from the path.
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != "delete-instrument:bn-1/BTCUSDT" {
		t.Fatalf("unexpected service calls: %v", svc.mdCalls)
	}
}

func TestDeleteMarketDataInstrument_MissingSymbol(t *testing.T) {
	// A missing externalSymbol query param is passed through as empty; the
	// handler does not pre-validate it, so the service decides. With a nil
	// stateErr the call succeeds with 204 and an empty symbol tag.
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/market-data/instances/bn-1/instruments", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != "delete-instrument:bn-1/" {
		t.Fatalf("unexpected service calls: %v", svc.mdCalls)
	}
}

func TestDeleteMarketDataInstrument_NotFound(t *testing.T) {
	svc := &fakeService{
		stateErr: fmt.Errorf("instrument %q: %w", "BTCUSDT", domain.ErrNotFound),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/market-data/instances/bn-1/instruments?externalSymbol=BTCUSDT",
		nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

// --- POST /market-data/instances/{id}/verify-symbol -------------------------

func TestVerifyMarketDataSymbol_Exists(t *testing.T) {
	// Happy path with the symbol present: supported=true, exists=true, and no
	// suggestion (omitempty drops the field).
	svc := &fakeService{
		mdVerify: backend.MarketDataSymbolVerification{
			Supported: true, Exists: true,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"externalSymbol":"BTCUSDT"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances/bn-1/verify-symbol", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	v, _ := bodyMap(t, rec.Result())["verification"].(map[string]any)
	if v["supported"] != true || v["exists"] != true {
		t.Fatalf("want supported=true exists=true, got %v", v)
	}
	if _, ok := v["suggestion"]; ok {
		t.Fatalf("want suggestion omitted, got %v", v["suggestion"])
	}
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != "verify-symbol:bn-1/BTCUSDT" {
		t.Fatalf("unexpected service calls: %v", svc.mdCalls)
	}
}

func TestVerifyMarketDataSymbol_InvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances/bn-1/verify-symbol",
		bytes.NewBufferString(`{bad`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
	if len(svc.mdCalls) != 0 {
		t.Fatalf("invalid JSON must not reach service: %v", svc.mdCalls)
	}
}

func TestVerifyMarketDataSymbol_ServiceError(t *testing.T) {
	// VerifyMarketDataSymbol returns mdVerifyErr; a generic error maps to
	// 500/internal (a provider that cannot verify returns supported=false, not
	// an error, so a real error here is a genuine failure).
	svc := &fakeService{mdVerifyErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"externalSymbol":"BTCUSDT"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances/bn-1/verify-symbol", body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

func TestVerifyMarketDataSymbol_NotFound(t *testing.T) {
	// A wrapped ErrNotFound (e.g. unknown instance id) maps to 404/not_found.
	svc := &fakeService{
		mdVerifyErr: fmt.Errorf("instance %q: %w", "bn-x", domain.ErrNotFound),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"externalSymbol":"BTCUSDT"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances/bn-x/verify-symbol", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}
