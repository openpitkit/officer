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
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
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
					ExternalID:  extID("bn-1"),
					Provider:    "finnhub",
					Label:       "Primary",
					Credentials: `{"token":"secret"}`,
					Enabled:     true,
				},
				State: "ok",
			},
		},
		FreshnessSeconds: 30,
		RestartRequired:  true,
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
	if inst["externalId"] != extID("bn-1").String() || inst["state"] != "ok" {
		t.Fatalf("unexpected instance: %v", inst)
	}
	if inst["credentials"] != "" {
		t.Fatalf("credentials = %q, want redacted empty string", inst["credentials"])
	}
	secrets, _ := inst["secrets"].(map[string]any)
	if secrets["token"] != true {
		t.Fatalf("secret state = %v, want token=true", secrets)
	}
	if md["freshnessSeconds"].(float64) != 30 {
		t.Fatalf("want freshnessSeconds=30, got %v", md["freshnessSeconds"])
	}
	if md["restartRequired"] != true {
		t.Fatalf("want restartRequired=true, got %v", md["restartRequired"])
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
	created := domain.MarketDataInstance{
		ExternalID: extID("md-created"),
		Provider:   "ib",
		Label:      "Backup",
		Enabled:    true,
	}
	svc := &fakeService{mdCreateResult: created}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"provider":"ib","label":"Backup","credentials":"{\"host\":\"127.0.0.1\",\"port\":7496}","enabled":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	instance, ok := m["instance"].(map[string]any)
	if !ok {
		t.Fatalf("want instance object, got %v", m["instance"])
	}
	if instance["externalId"] != created.ExternalID.String() {
		t.Fatalf("want created externalId, got %v", instance["externalId"])
	}
	// The public create request carries no instance id; the backend assigns it.
	want := `create:ib:Backup:{"host":"127.0.0.1","port":7496}:true`
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != want {
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
	body := bytes.NewBufferString(`{"provider":"binance"}`)
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

// TestCreateMarketDataInstance_SuppliedExternalID checks an optional supplied id
// is threaded onto the instance entity as-is.
func TestCreateMarketDataInstance_SuppliedExternalID(t *testing.T) {
	supplied := extID("md-supplied")
	svc := &fakeService{marketData: sampleMarketData()}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(fmt.Sprintf(
		`{"externalId":%q,"provider":"ib","label":"Backup"}`, supplied.String()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.mdCreateInstance.ExternalID != supplied {
		t.Errorf("supplied id not threaded: got %s, want %s",
			svc.mdCreateInstance.ExternalID, supplied)
	}
}

// TestCreateMarketDataInstance_AbsentExternalID checks omitting the id leaves the
// backend to generate one (a zero id is passed through).
func TestCreateMarketDataInstance_AbsentExternalID(t *testing.T) {
	svc := &fakeService{marketData: sampleMarketData()}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"provider":"ib","label":"Backup"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if !svc.mdCreateInstance.ExternalID.IsZero() {
		t.Errorf("absent id should pass a zero id to the backend, got %s",
			svc.mdCreateInstance.ExternalID)
	}
}

// TestCreateMarketDataInstance_DuplicateConflict checks a duplicate supplied id
// maps to 409 (the backend rejects with domain.ErrAlreadyExists).
func TestCreateMarketDataInstance_DuplicateConflict(t *testing.T) {
	svc := &fakeService{stateErr: domain.ErrAlreadyExists}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(fmt.Sprintf(
		`{"externalId":%q,"provider":"ib","label":"Backup"}`, extID("md-dup").String()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "conflict" {
		t.Errorf("want code=conflict, got %v", errObj["code"])
	}
}

// TestCreateMarketDataInstance_MalformedExternalID checks a malformed supplied id
// maps to 400 before any backend call.
func TestCreateMarketDataInstance_MalformedExternalID(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"externalId":"bad-id","provider":"ib","label":"Backup"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Errorf("want code=validation, got %v", errObj["code"])
	}
	if len(svc.mdCalls) != 0 {
		t.Errorf("malformed id must not reach service: %v", svc.mdCalls)
	}
}

// --- PUT /market-data/instances/{id}/settings -------------------------------

func TestUpdateMarketDataInstanceSettings_OK(t *testing.T) {
	svc := &fakeService{marketData: sampleMarketData()}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"label":"Primary IB","credentials":"{\"host\":\"127.0.0.1\",\"clientId\":7}"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/market-data/instances/ib-1/settings", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if _, ok := m["marketData"].(map[string]any); !ok {
		t.Fatalf("want marketData object, got %v", m["marketData"])
	}
	want := `settings:ib-1:Primary IB:{"host":"127.0.0.1","clientId":7}`
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != want {
		t.Fatalf("unexpected service calls: %v", svc.mdCalls)
	}
}

func TestUpdateMarketDataInstanceSettings_InvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/market-data/instances/ib-1/settings",
		bytes.NewBufferString(`{bad`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	if len(svc.mdCalls) != 0 {
		t.Fatalf("invalid JSON must not reach service: %v", svc.mdCalls)
	}
}

func TestUpdateMarketDataInstanceSettings_NotFound(t *testing.T) {
	svc := &fakeService{stateErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/market-data/instances/missing/settings",
		bytes.NewBufferString(`{"label":"Missing","credentials":"{}"}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

// --- PUT /market-data/instances/{id}/enabled --------------------------------

func TestSetMarketDataInstanceEnabled_Toggle(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		persistedEnabled bool
	}{
		{"enable", `{"enabled":true}`, true},
		{"disable response uses persisted enabled", `{"enabled":false}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status := sampleMarketData()
			status.Instances[0].Instance.Enabled = tc.persistedEnabled
			id := status.Instances[0].Instance.ExternalID.String()
			svc := &fakeService{marketData: status}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
				"/api/v1/market-data/instances/"+id+"/enabled",
				bytes.NewBufferString(tc.body)))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
			if bodyMap(t, rec.Result())["enabled"] != tc.persistedEnabled {
				t.Fatalf("want persisted enabled=%v body", tc.persistedEnabled)
			}
			want := fmt.Sprintf("instance:%s:%v", id, strings.Contains(tc.body, "true"))
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
	id := extID("bn-x").String()
	svc := &fakeService{
		stateErr: fmt.Errorf("instance %q: %w", id, domain.ErrNotFound),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/market-data/instances/"+id+"/enabled",
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
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != "delete-instance:bn-1:false" {
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
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != "delete-instance:bn/1:false" {
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
	// The upsert-instrument route parses the path id as the instance's opaque
	// external id, so the path carries a valid external-id wire form.
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/market-data/instances/"+extID("bn-1").String()+"/instruments", body))
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
	// A valid external-id path reaches the service so the ErrInvalid the backend
	// returns drives the 400, not the path-parse guard.
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/market-data/instances/"+extID("bn-1").String()+"/instruments", body))
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
			Supported: true, Exists: true, Details: "APPLE INC, Common Stock",
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
	if v["details"] != "APPLE INC, Common Stock" {
		t.Fatalf("want details, got %v", v["details"])
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

func TestVerifyMarketDataSymbol_Upstream(t *testing.T) {
	// A provider/transport failure wraps domain.ErrUpstream and must map to a
	// 502/upstream with a plain message, not a 500 "unhandled internal error",
	// so the operator gets an actionable reaction instead of a cryptic failure.
	svc := &fakeService{
		mdVerifyErr: fmt.Errorf(
			"%w: verify market-data symbol: finnhub quote: unexpected status 403",
			domain.ErrUpstream,
		),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"externalSymbol":"EUR.WA"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances/bn-1/verify-symbol", body))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "upstream" {
		t.Fatalf("want code=upstream, got %v", errObj["code"])
	}
	msg, _ := errObj["message"].(string)
	if strings.Contains(msg, "403") || strings.Contains(msg, "finnhub") {
		t.Fatalf("message leaks provider jargon: %q", msg)
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

func TestMarketDataSafeSettings_IBExposesContracts(t *testing.T) {
	// Per-instrument IB contract overrides carry no secrets, so contracts are
	// exposed verbatim through the safe settings. The user-facing contractDefaults
	// is no longer a setting and is never surfaced even if present in credentials.
	instance := domain.MarketDataInstance{
		Provider: domain.MarketDataProviderIB,
		Credentials: `{
			"host": "127.0.0.1",
			"clientId": 7,
			"contractDefaults": {"secType": "STK", "exchange": "SMART"},
			"contracts": {"AAPL": {"secType": "STK", "primaryExchange": "NASDAQ"}}
		}`,
	}
	settings := marketDataSafeSettings(instance)
	if _, ok := settings["contractDefaults"]; ok {
		t.Fatalf("contractDefaults present, want absent: %v", settings["contractDefaults"])
	}
	contracts, ok := settings["contracts"].(map[string]any)
	if !ok {
		t.Fatalf("contracts = %v", settings["contracts"])
	}
	aapl, ok := contracts["AAPL"].(map[string]any)
	if !ok || aapl["secType"] != "STK" || aapl["primaryExchange"] != "NASDAQ" {
		t.Fatalf("contracts[AAPL] = %v", contracts["AAPL"])
	}
}

func TestMarketDataSafeSettings_IBOmitsAbsentContracts(t *testing.T) {
	// With no contract overrides stored, the contracts key is absent
	// (copyObjectSetting only copies non-empty maps).
	instance := domain.MarketDataInstance{
		Provider:    domain.MarketDataProviderIB,
		Credentials: `{"host": "127.0.0.1", "clientId": 7}`,
	}
	settings := marketDataSafeSettings(instance)
	if _, ok := settings["contracts"]; ok {
		t.Fatalf("contracts present, want absent: %v", settings)
	}
}

// --- POST /market-data/instances/{id}/search-symbols ------------------------

func TestSearchMarketDataSymbols_OK(t *testing.T) {
	// Happy path: supported=true with one resolved contract; the secType/exchange/
	// currency criteria reach the service and the match round-trips through the
	// DTO with its contract specifics.
	svc := &fakeService{
		mdSearch: backend.MarketDataSymbolSearch{
			Supported: true,
			Matches: []backend.MarketDataSymbolMatch{{
				Symbol:       "BTC",
				Name:         "Bitcoin",
				SecType:      "CRYPTO",
				Exchange:     "PAXOS",
				Currency:     "USD",
				ConID:        "12345",
				LocalSymbol:  "BTC.USD",
				TradingClass: "BTC",
			}},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"query":"BTC","secType":"CRYPTO","exchange":"PAXOS","currency":"USD"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances/bn-1/search-symbols", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	out := bodyMap(t, rec.Result())
	if out["supported"] != true {
		t.Fatalf("want supported=true, got %v", out["supported"])
	}
	matches, _ := out["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("want 1 match, got %v", out["matches"])
	}
	match, _ := matches[0].(map[string]any)
	if match["symbol"] != "BTC" || match["secType"] != "CRYPTO" ||
		match["exchange"] != "PAXOS" || match["currency"] != "USD" ||
		match["localSymbol"] != "BTC.USD" {
		t.Fatalf("unexpected match: %v", match)
	}
	if svc.mdSearchInput.SecType != "CRYPTO" ||
		svc.mdSearchInput.Exchange != "PAXOS" ||
		svc.mdSearchInput.Currency != "USD" {
		t.Fatalf("decoded search input = %+v", svc.mdSearchInput)
	}
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != "search-symbols:bn-1/BTC" {
		t.Fatalf("unexpected service calls: %v", svc.mdCalls)
	}
}

func TestSearchMarketDataSymbols_InvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances/bn-1/search-symbols",
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

func TestSearchMarketDataSymbols_EmptyQuery(t *testing.T) {
	// An empty (whitespace-only) query is a validation error and never reaches
	// the service.
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"query":"   ","secType":"STK"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances/bn-1/search-symbols", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
	if len(svc.mdCalls) != 0 {
		t.Fatalf("empty query must not reach service: %v", svc.mdCalls)
	}
}

func TestSearchMarketDataSymbols_ServiceError(t *testing.T) {
	// A generic service error (e.g. TWS unreachable) maps to 500/internal.
	svc := &fakeService{mdSearchErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"query":"AAPL"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances/bn-1/search-symbols", body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

func TestSearchMarketDataSymbols_NotFound(t *testing.T) {
	// A wrapped ErrNotFound (unknown instance id) maps to 404/not_found.
	svc := &fakeService{
		mdSearchErr: fmt.Errorf("instance %q: %w", "bn-x", domain.ErrNotFound),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"query":"AAPL"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances/bn-x/search-symbols", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

func TestSearchMarketDataSymbols_InvalidStrike(t *testing.T) {
	// A malformed client-supplied strike is a pure input error: the boundary
	// rejects it as 400/validation and the request never reaches the service, so
	// it cannot be mislabelled as a 502 upstream/provider failure.
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"query":"AAPL","secType":"OPT","strike":"abc"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances/bn-1/search-symbols", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "strike") {
		t.Fatalf("message should name the offending field: %q", msg)
	}
	if len(svc.mdCalls) != 0 {
		t.Fatalf("invalid strike must not reach service: %v", svc.mdCalls)
	}
}

func TestSearchMarketDataSymbols_NonFiniteStrike(t *testing.T) {
	// A non-finite strike (e.g. "NaN", "Inf") is also a client input error and
	// must map to 400, never reaching the service or surfacing as upstream.
	for _, strike := range []string{"NaN", "Inf"} {
		svc := &fakeService{}
		r, err := newRouter(svc)
		if err != nil {
			t.Fatal(err)
		}
		body := bytes.NewBufferString(
			fmt.Sprintf(`{"query":"AAPL","secType":"OPT","strike":%q}`, strike))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
			"/api/v1/market-data/instances/bn-1/search-symbols", body))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("strike %q: want 400, got %d", strike, rec.Code)
		}
		errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
		if errObj["code"] != "validation" {
			t.Fatalf("strike %q: want code=validation, got %v", strike, errObj["code"])
		}
		if len(svc.mdCalls) != 0 {
			t.Fatalf("strike %q must not reach service: %v", strike, svc.mdCalls)
		}
	}
}

func TestSearchMarketDataSymbols_ValidStrike(t *testing.T) {
	// A well-formed decimal strike is a legitimate criterion: it passes the
	// boundary check, reaches the service, and round-trips on the search input.
	svc := &fakeService{
		mdSearch: backend.MarketDataSymbolSearch{Supported: true},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"query":"AAPL","secType":"OPT","right":"C","strike":"185.5"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/market-data/instances/bn-1/search-symbols", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.mdSearchInput.Strike != "185.5" {
		t.Fatalf("decoded strike = %q, want 185.5", svc.mdSearchInput.Strike)
	}
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != "search-symbols:bn-1/AAPL" {
		t.Fatalf("unexpected service calls: %v", svc.mdCalls)
	}
}
