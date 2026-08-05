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
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"testing/fstest"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// fakeSPA returns a minimal in-memory filesystem for the SPA option.
func fakeSPA() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html></html>")},
	}
}

func newRouter(svc Service) (http.Handler, error) {
	return httpx.NewRouter(httpx.RouterConfig{
		Routes:      NewRouteRegistry(svc, nil),
		Authorizer:  httpx.AllowAll{},
		SPA:         fakeSPA(),
		BodyLimit:   BodyLimitPolicy(),
		ExtraMounts: ExtraMounts(),
	})
}

func TestRouteRegistrySurfaceBaseline(t *testing.T) {
	routes := NewRouteRegistry(&fakeService{}, nil).Routes()
	got := make([]string, 0, len(routes))
	for _, route := range routes {
		got = append(got, route.Method+" "+route.Pattern)
	}
	want := []string{
		"GET /health",
		"GET /status",
		"GET /service",
		"GET /overview",
		"POST /backup/export",
		"POST /backup/restore",
		"POST /business-csv/export",
		"POST /database/reset",
		"GET /assets",
		"POST /assets",
		"PUT /assets/{code}",
		"DELETE /assets/{code}",
		"GET /asset-classes",
		"POST /asset-classes",
		"PUT /asset-classes/{code}",
		"DELETE /asset-classes/{code}",
		"GET /accounts",
		"POST /accounts",
		"GET /accounts/{code}",
		"PUT /accounts/{code}",
		"POST /accounts/{code}/block",
		"POST /accounts/{code}/unblock",
		"DELETE /accounts/{code}",
		"PUT /accounts/{code}/group",
		"PUT /accounts/{code}/currency",
		"PUT /accounts/{code}/notes",
		"GET /accounts/{code}/adjustments",
		"POST /accounts/{code}/adjustments",
		"PUT /accounts/{code}/balances/realized-pnl",
		"GET /groups",
		"POST /groups",
		"PUT /groups/-/default/currency",
		"GET /groups/{code}",
		"PUT /groups/{code}",
		"PUT /groups/{code}/currency",
		"PUT /groups/{code}/notes",
		"POST /groups/{code}/block",
		"POST /groups/{code}/unblock",
		"DELETE /groups/{code}",
		"GET /balances",
		"GET /adjustments",
		"POST /orders/check",
		"GET /orders",
		"GET /orders/{id}",
		"GET /orders/{id}/events/{eventId}/reproduction",
		"POST /orders/{id}/execution-reports",
		"GET /trades",
		"GET /limits",
		"PUT /limits/rate",
		"PUT /limits/order-size",
		"PUT /limits/spot-funds-pnl-bounds",
		"DELETE /limits",
		"GET /audit",
		"GET /audit/actions",
		"GET /mcp-access",
		"PUT /mcp-access/{command}",
		"GET /user-settings",
		"PUT /user-settings",
		"POST /signing/keys/generate",
		"POST /signing/keys/import",
		"GET /signing/keys",
		"GET /signing/keys/active/public",
		"GET /signing/keys/{keyId}/public",
		"GET /signing/config",
		"PUT /signing/config",
		"POST /orders/submit",
		"POST /orders/drop-copy/submit",
		"POST /orders/{id}/confirm",
		"POST /orders/{id}/cancel",
		"GET /market-data",
		"POST /market-data/restart",
		"POST /market-data/instances",
		"PUT /market-data/instances/{id}/enabled",
		"PUT /market-data/instances/{id}/settings",
		"DELETE /market-data/instances/{id}",
		"PUT /market-data/instances/{id}/instruments",
		"PUT /market-data/instances/{id}/instruments/enabled",
		"DELETE /market-data/instances/{id}/instruments",
		"POST /market-data/instances/{id}/verify-symbol",
		"POST /market-data/instances/{id}/search-symbols",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("route surface mismatch\ngot:  %v\nwant: %v", got, want)
	}
}

func TestServiceLifecycleRoutes(t *testing.T) {
	t.Parallel()
	routes := ServiceLifecycleRoutes(
		http.NotFoundHandler(),
		http.NotFoundHandler(),
	)
	want := []struct {
		id      string
		method  string
		pattern string
	}{
		{"service.restart.post", http.MethodPost, "/service/restart"},
		{"service.stop.post", http.MethodPost, "/service/stop"},
	}
	if len(routes) != len(want) {
		t.Fatalf("route count = %d, want %d", len(routes), len(want))
	}
	for i, route := range routes {
		if route.ID != want[i].id || route.Method != want[i].method ||
			route.Pattern != want[i].pattern {
			t.Fatalf("route %d = (%q, %q, %q), want (%q, %q, %q)",
				i, route.ID, route.Method, route.Pattern,
				want[i].id, want[i].method, want[i].pattern)
		}
	}
}

// bodyMap decodes a JSON response body into a map. Older behavioral tests use
// the former error-envelope view; expose that view from RFC 9457 fields while
// dedicated problem-detail tests assert the actual wire representation.
func bodyMap(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if _, exists := m["error"]; !exists && m["type"] == "about:blank" {
		message, _ := m["detail"].(string)
		m["error"] = map[string]any{
			"code": "validation", "message": message,
		}
	}
	return m
}

// extID builds a deterministic ExternalID from a short seed for test fixtures.
// The wire form is Officer's generated external id for the 16 raw bytes.
func extID(seed string) domain.ExternalID {
	var b [16]byte
	copy(b[:], seed)
	id, err := domain.GeneratedExternalIDFromBytes(b[:])
	if err != nil {
		panic(err)
	}
	return id
}

// assertNoSurrogateID fails if a decoded response sub-map leaks any forbidden
// surrogate or engine identifier key. Machine records may expose their public
// opaque handle as "id"; numeric and engine ids must never be serialized.
func assertNoSurrogateID(t *testing.T, obj map[string]any) {
	t.Helper()
	if id, ok := obj["id"]; ok {
		if _, ok := id.(string); !ok {
			t.Fatalf("response leaked non-public id %q: %v", id, obj)
		}
	}
	for _, k := range []string{"orderId", "engineId", "engineAccountId", "engineGroupId"} {
		if _, ok := obj[k]; ok {
			t.Fatalf("response leaked forbidden key %q: %v", k, obj)
		}
	}
}

func TestHealthz(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestV1Health(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if m["ok"] != true {
		t.Fatalf("want ok:true, got %v", m["ok"])
	}
}

func TestV1Status(t *testing.T) {
	svc := &fakeService{
		status: backend.Status{
			Nodes: []node.Health{{
				Engine: engine.Health{Version: "v1", Running: true},
				Store:  store.StoreHealth{Reachable: true},
			}},
			Healthy: true,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if m["healthy"] != true {
		t.Fatalf("want healthy:true, got %v", m["healthy"])
	}
}

// assertHasDependents409 checks the recorded response is the 409 has_dependents
// wire shape with a single dependent of the wanted kind/count.
func assertHasDependents409(t *testing.T, rec *httptest.ResponseRecorder, wantKind string, wantCount int) {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	body := bodyMap(t, rec.Result())
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object: %+v", body)
	}
	if errObj["code"] != "has_dependents" {
		t.Fatalf("error.code = %v, want has_dependents", errObj["code"])
	}
	deps, ok := errObj["dependents"].([]any)
	if !ok || len(deps) != 1 {
		t.Fatalf("dependents = %v, want one entry", errObj["dependents"])
	}
	dep, ok := deps[0].(map[string]any)
	if !ok {
		t.Fatalf("dependent[0] not an object: %v", deps[0])
	}
	if dep["kind"] != wantKind {
		t.Fatalf("dependent kind = %v, want %q", dep["kind"], wantKind)
	}
	// JSON numbers decode to float64.
	if count, _ := dep["count"].(float64); int(count) != wantCount {
		t.Fatalf("dependent count = %v, want %d", dep["count"], wantCount)
	}
}

func TestNewRouter_MissingService(t *testing.T) {
	_, err := httpx.NewRouter(httpx.RouterConfig{
		Authorizer: httpx.AllowAll{},
		SPA:        fakeSPA(),
		BodyLimit:  BodyLimitPolicy(),
	})
	if err == nil {
		t.Fatal("want error for nil route registry")
	}
}

func TestNewRouter_MissingSPA(t *testing.T) {
	_, err := httpx.NewRouter(httpx.RouterConfig{
		Routes:     NewRouteRegistry(&fakeService{}, nil),
		Authorizer: httpx.AllowAll{},
		BodyLimit:  BodyLimitPolicy(),
	})
	if err == nil {
		t.Fatal("want error for nil SPA")
	}
}

func TestRequestBodyLimitUsesBackupRestoreCap(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/api/v1/backup/restore",
		"/app/api/v1/backup/restore",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if got := BodyLimitPolicy()(req); got != maxBackupRestoreBody {
			t.Fatalf("request body limit for %s = %d, want %d", path, got, maxBackupRestoreBody)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/submit", nil)
	if got := BodyLimitPolicy()(req); got != maxRequestBody {
		t.Fatalf("default request body limit = %d, want %d", got, maxRequestBody)
	}
}
