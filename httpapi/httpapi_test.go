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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"testing/fstest"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/backup"
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

func newRouter(svc backend.ControlPlane) (http.Handler, error) {
	return newRouterOver(svc, nil, noSealReport)
}

// newRouterOver builds the router over svc with the log tail logs, nil for
// none, and the seal reports of sealReports.
func newRouterOver(
	svc backend.ControlPlane, logs httpx.LogSource, sealReports SealReportSource,
) (http.Handler, error) {
	guards, err := testGuards()
	if err != nil {
		return nil, err
	}
	return httpx.NewRouter(httpx.RouterConfig{
		Routes:         NewRouteRegistry(svc, logs, sealReports),
		Authorizer:     httpx.AllowAll{},
		CallerResolver: testCallerResolver,
		SPA:            fakeSPA(),
		BodyLimit:      BodyLimitPolicy(),
		ExtraMounts:    ExtraMounts(),
		Guards:         guards,
		AccessMode:     func() httpx.AccessMode { return httpx.AccessModeOpen },
	})
}

// testGuards returns the browser guards the handler tests serve behind: the
// real Host guard, allowing the example.com host httptest.NewRequest
// addresses, the real Origin guard, and no media-type guard.
func testGuards() (httpx.BrowserGuards, error) {
	host, err := httpx.AllowedHosts([]string{"example.com"})
	if err != nil {
		return httpx.BrowserGuards{}, err
	}
	return httpx.BrowserGuards{
		Host:   host,
		Origin: httpx.SameOrigin(),
		// Bodies here are untyped; guards_test.go and register_test.go test JSONBody.
		ContentType: func(next http.Handler) http.Handler { return next },
	}, nil
}

func testCallerResolver(*http.Request) (domain.Caller, error) {
	return domain.Caller{Principal: domain.PrincipalOperator}, nil
}

func TestRouteRegistrySurfaceBaseline(t *testing.T) {
	routes := NewRouteRegistry(&fakeService{}, nil, noSealReport).Routes()
	got := make([]string, 0, len(routes))
	for _, route := range routes {
		got = append(got, route.Method+" "+route.Pattern)
	}
	want := []string{
		"GET /health",
		"GET /status",
		"GET /service",
		"GET /credential-storage",
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
		"PUT /limits/spot-funds-unrealized-pnl-bounds",
		"PUT /limits/spot-funds-total-pnl-bounds",
		"DELETE /limits",
		"GET /audit",
		"GET /audit/actions",
		"GET /mcp-access",
		"PUT /mcp-access/{command}",
		"POST /signing/keys/generate",
		"POST /signing/keys/import",
		"GET /signing/keys",
		"GET /signing/keys/active/public",
		"GET /signing/keys/{keyId}/public",
		"GET /signing/config",
		"PUT /signing/config",
		"POST /orders/submit",
		"POST /orders/send-paper",
		"POST /orders/send-live",
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
		"GET /trading",
		"POST /trading/connections",
		"PUT /trading/connections/{id}/settings",
		"PUT /trading/connections/{id}/enabled",
		"POST /trading/connections/{id}/check",
		"PUT /trading/connections/{id}/instruments",
		"DELETE /trading/connections/{id}/instruments",
		"POST /trading/connections/{id}/verify-symbol",
		"POST /trading/connections/{id}/search-symbols",
		"PUT /trading/connections/{id}/access",
		"DELETE /trading/connections/{id}/access",
		"PUT /trading/connections/{id}/fee-account",
		"DELETE /trading/connections/{id}/fee-account",
		"GET /accounts/{code}/trading-destinations",
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
			Node: node.Health{
				Engine: engine.Health{Version: "v1", Running: true},
				Store:  store.StoreHealth{Reachable: true},
			},
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
	guards, err := testGuards()
	if err != nil {
		t.Fatal(err)
	}
	_, err = httpx.NewRouter(httpx.RouterConfig{
		Authorizer:     httpx.AllowAll{},
		CallerResolver: testCallerResolver,
		SPA:            fakeSPA(),
		BodyLimit:      BodyLimitPolicy(),
		Guards:         guards,
	})
	if err == nil || err.Error() != "httpapi: nil route registry" {
		t.Fatalf("NewRouter error = %v, want the nil route registry named", err)
	}
}

func TestNewRouter_MissingSPA(t *testing.T) {
	guards, err := testGuards()
	if err != nil {
		t.Fatal(err)
	}
	_, err = httpx.NewRouter(httpx.RouterConfig{
		Routes:         NewRouteRegistry(&fakeService{}, nil, noSealReport),
		Authorizer:     httpx.AllowAll{},
		CallerResolver: testCallerResolver,
		BodyLimit:      BodyLimitPolicy(),
		Guards:         guards,
	})
	if err == nil || err.Error() != "httpapi: nil SPA filesystem" {
		t.Fatalf("NewRouter error = %v, want the nil SPA named", err)
	}
}

func TestBackupRestoreNonReleaseArchive(t *testing.T) {
	original := backupRestoreReleaseBuild
	t.Cleanup(func() { backupRestoreReleaseBuild = original })
	for _, release := range []bool{true, false} {
		for _, marked := range []bool{true, false} {
			for _, malformedData := range []bool{false, true} {
				for _, shape := range []string{"inline", "JSON", "ZIP"} {
					name := fmt.Sprintf("release=%t/marked=%t/malformed=%t/%s", release, marked, malformedData, shape)
					t.Run(name, func(t *testing.T) {
						backupRestoreReleaseBuild = func() bool { return release }
						marker := ""
						if marked {
							marker = `,"nonRelease":true`
						}
						data := `{"tradingConnections":[{"enabled":true}]}`
						if malformedData {
							data = `"not an archive data object"`
						}
						payload := []byte(`{"manifest":{"source":"probe","sections":["trading"]` + marker + `},"credentialForm":"plaintext","data":` + data + `,"futureField":true}`)
						request := map[string]any{
							"scope": backup.Scope{All: true}, "mode": backup.RestoreModeOverwrite,
						}
						if shape == "inline" {
							request["archive"] = json.RawMessage(payload)
						} else {
							if shape == "ZIP" {
								payload = testZipPayload(t, map[string]string{"backup.json": string(payload)})
							}
							request["archiveFile"] = base64.StdEncoding.EncodeToString(payload)
							request["archiveFilename"] = "backup." + shape
						}
						body, err := json.Marshal(request)
						if err != nil {
							t.Fatal(err)
						}
						svc := &fakeService{backupSummary: backup.NewSummary()}
						router, err := newRouter(svc)
						if err != nil {
							t.Fatal(err)
						}
						rec := httptest.NewRecorder()
						router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
							"/api/v1/backup/restore", bytes.NewReader(body)))
						var response struct {
							Error  struct{ Code, Message string }
							Detail string
							Errors []struct{ Code, Pointer, Constraint string }
						}
						if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
							t.Fatal(err)
						}
						if release && marked {
							if rec.Code != 422 || response.Error.Code != "non_release_archive" || response.Error.Message != domain.ErrNonReleaseArchive.Error() {
								t.Errorf("release refusal = %d %s, want 422 non_release_archive with sentinel message", rec.Code, rec.Body)
							}
						} else if malformedData {
							if rec.Code != 422 || len(response.Errors) != 1 || response.Errors[0].Code != "validation" {
								t.Fatalf("malformed archive = %d %s, want 422 validation", rec.Code, rec.Body)
							}
							if shape == "inline" {
								if response.Detail != "request member has an invalid type" || response.Errors[0].Pointer != "/archive/data" {
									t.Errorf("inline type validation changed: %s", rec.Body)
								}
							} else if response.Detail != "invalid backup archive JSON in backup."+shape || response.Errors[0].Pointer != "/archiveFile" {
								t.Errorf("file validation changed: %s", rec.Body)
							}
						} else {
							if rec.Code != 200 || svc.restoreArchive.Manifest.NonRelease != marked ||
								len(svc.restoreArchive.Data.TradingConnections) != 1 || !svc.restoreArchive.Data.TradingConnections[0].Enabled {
								t.Fatalf("archive did not reach service unchanged: status=%d body=%s archive=%+v", rec.Code, rec.Body, svc.restoreArchive)
							}
						}
						wantCall := (!release || !marked) && !malformedData
						if called := svc.restoreOptions.Mode != ""; called != wantCall {
							t.Fatalf("service restore called=%t, want %t", called, wantCall)
						}
					})
				}
			}
		}
	}
}

func TestBackupRestoreNonReleaseDuplicateArchive(t *testing.T) {
	original := backupRestoreReleaseBuild
	backupRestoreReleaseBuild = func() bool { return true }
	t.Cleanup(func() { backupRestoreReleaseBuild = original })
	t.Run("marked archive followed by null", func(t *testing.T) {
		body := `{"archive":{"manifest":{"source":"probe","sections":["trading"],"nonRelease":true},"data":{}},"archive":null,"mode":"` + string(backup.RestoreModeOverwrite) + `","scope":{"all":true}}`
		svc := &fakeService{backupSummary: backup.NewSummary()}
		rec := httptest.NewRecorder()
		handleRestoreBackup(svc)(rec, httptest.NewRequest(http.MethodPost,
			"/backup/restore", bytes.NewBufferString(body)))
		var response struct{ Error struct{ Code string } }
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusUnprocessableEntity || response.Error.Code != "non_release_archive" {
			t.Errorf("duplicate archive refusal = %d %s, want 422 non_release_archive", rec.Code, rec.Body)
		}
		if svc.restoreOptions.Mode != "" {
			t.Error("marked duplicate archive reached restore service")
		}
	})
}

func TestBackupRestoreNonReleaseFileOverridesInlineArchive(t *testing.T) {
	original := backupRestoreReleaseBuild
	backupRestoreReleaseBuild = func() bool { return true }
	t.Cleanup(func() { backupRestoreReleaseBuild = original })
	for _, markedFile := range []bool{false, true} {
		t.Run(fmt.Sprintf("markedFile=%t", markedFile), func(t *testing.T) {
			inline := backup.Archive{Manifest: backup.Manifest{
				Source: "inline", Sections: []backup.Section{backup.SectionTrading},
				NonRelease: !markedFile,
			}}
			fileArchive := backup.Archive{Manifest: backup.Manifest{
				Source: "file", Sections: []backup.Section{backup.SectionTrading},
				NonRelease: markedFile,
			}}
			payload, err := json.Marshal(fileArchive)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(map[string]any{
				"archive": inline, "archiveFile": base64.StdEncoding.EncodeToString(payload),
				"archiveFilename": "backup.json",
				"scope":           backup.Scope{All: true}, "mode": backup.RestoreModeOverwrite,
			})
			if err != nil {
				t.Fatal(err)
			}
			svc := &fakeService{backupSummary: backup.NewSummary()}
			rec := httptest.NewRecorder()
			handleRestoreBackup(svc)(rec, httptest.NewRequest(http.MethodPost,
				"/backup/restore", bytes.NewReader(body)))
			if markedFile {
				var response struct{ Error struct{ Code string } }
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if rec.Code != http.StatusUnprocessableEntity || response.Error.Code != "non_release_archive" {
					t.Fatalf("marked file refusal = %d %s, want 422 non_release_archive", rec.Code, rec.Body)
				}
				if svc.restoreOptions.Mode != "" {
					t.Fatal("marked file reached restore service")
				}
				return
			}
			if rec.Code != http.StatusOK || svc.restoreOptions.Mode != backup.RestoreModeOverwrite ||
				svc.restoreArchive.Manifest.Source != "file" || svc.restoreArchive.Manifest.NonRelease {
				t.Fatalf("unmarked file did not override marked inline archive: status=%d body=%s archive=%+v",
					rec.Code, rec.Body, svc.restoreArchive)
			}
		})
	}
}

func TestBackupRestoreArchiveTypeErrorPrecedesModeAndScope(t *testing.T) {
	original := backupRestoreReleaseBuild
	backupRestoreReleaseBuild = func() bool { return true }
	t.Cleanup(func() { backupRestoreReleaseBuild = original })
	for _, tc := range []struct{ name, body string }{
		{
			name: "unknown mode",
			body: `{"archive":{"manifest":{"source":"s","sections":["trading"]},"data":"x"},"mode":"bogus","scope":{"all":true}}`,
		},
		{
			name: "invalid scope",
			body: `{"archive":{"manifest":{"source":"s","sections":["trading"]},"data":"x"},"mode":"overwrite","scope":{}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			rec := httptest.NewRecorder()
			handleRestoreBackup(svc)(rec, httptest.NewRequest(http.MethodPost,
				"/backup/restore", bytes.NewBufferString(tc.body)))
			var response struct {
				Detail string
				Errors []struct{ Code, Pointer, Constraint string }
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusUnprocessableEntity || response.Detail != "request member has an invalid type" ||
				len(response.Errors) != 1 || response.Errors[0].Code != "validation" ||
				response.Errors[0].Pointer != "/archive/data" || response.Errors[0].Constraint != "type" {
				t.Fatalf("first validation = %d %s, want 422 invalid type at /archive/data", rec.Code, rec.Body)
			}
			if svc.restoreOptions.Mode != "" {
				t.Fatal("malformed archive reached restore service")
			}
		})
	}
}

func TestBackupRestoreUnreadableNonReleaseMarker(t *testing.T) {
	original := backupRestoreReleaseBuild
	backupRestoreReleaseBuild = func() bool { return true }
	t.Cleanup(func() { backupRestoreReleaseBuild = original })
	for _, shape := range []string{"inline", "JSON", "ZIP"} {
		t.Run(shape, func(t *testing.T) {
			payload := []byte(`{"manifest":{"nonRelease":"true","sections":["trading"]},"data":{}}`)
			request := map[string]any{"scope": backup.Scope{All: true}, "mode": backup.RestoreModeOverwrite}
			if shape == "inline" {
				request["archive"] = json.RawMessage(payload)
			} else {
				if shape == "ZIP" {
					payload = testZipPayload(t, map[string]string{"backup.json": string(payload)})
				}
				request["archiveFile"] = base64.StdEncoding.EncodeToString(payload)
			}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			svc := &fakeService{}
			rec := httptest.NewRecorder()
			handleRestoreBackup(svc)(rec, httptest.NewRequest(http.MethodPost,
				"/backup/restore", bytes.NewReader(body)))
			var response struct {
				Detail string
				Errors []struct{ Code, Pointer string }
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 422 || len(response.Errors) != 1 || response.Errors[0].Code != "validation" || svc.restoreOptions.Mode != "" {
				t.Fatalf("unreadable marker did not reach normal validation: %d %s", rec.Code, rec.Body)
			}
			if shape == "inline" {
				if response.Detail != "request member has an invalid type" || response.Errors[0].Pointer != "/archive/manifest/nonRelease" {
					t.Fatalf("inline marker validation changed: %s", rec.Body)
				}
			} else if response.Detail != "invalid backup archive JSON" || response.Errors[0].Pointer != "/archiveFile" {
				t.Fatalf("file marker validation changed: %s", rec.Body)
			}
		})
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
