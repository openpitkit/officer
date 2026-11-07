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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestRouterWithoutSPAAnswersTypedNotFound(t *testing.T) {
	cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
	cfg.SPA = nil
	router, err := NewRouter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
		t.Fatalf("unknown root path = %d %s, want typed not_found", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("unknown root path content type = %q, want JSON", got)
	}
}

func TestRouterWithoutSPAOmitsManifestFallback(t *testing.T) {
	cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
	cfg.SPA = nil
	router, err := NewRouter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/route-manifest.json", nil))
	var manifest runtimeRouteManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest status = %d, want 200", rec.Code)
	}
	want := []runtimeRouteManifestEntry{{
		Method: http.MethodGet, Path: "/api/v1/route-manifest.json", Access: AccessAPI,
	}}
	if !slices.Equal(manifest.Routes, want) {
		t.Errorf("manifest routes = %+v, want %+v without GET /*", manifest.Routes, want)
	}
}

func TestRouterNotFoundHook(t *testing.T) {
	for _, path := range []string{"/missing", "/ext/v1/missing"} {
		t.Run(path, func(t *testing.T) {
			cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
			cfg.SPA = nil
			cfg.Surfaces = []Surface{{Prefix: "/ext/v1", Source: domain.SourceAPI}}
			calls := 0
			cfg.NotFound = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != path {
					t.Errorf("hook path = %q, want %q", r.URL.Path, path)
				}
				w.WriteHeader(http.StatusTeapot)
				_, _ = w.Write([]byte("host not found"))
			})
			router, err := NewRouter(cfg)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if calls != 1 || rec.Code != http.StatusTeapot || rec.Body.String() != "host not found" {
				t.Errorf("NotFound calls = %d, response = %d %q, want 1, 418 host not found", calls, rec.Code, rec.Body)
			}
		})
	}
}

func TestRouterRefusesSPAWithNotFound(t *testing.T) {
	cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
	cfg.NotFound = http.NotFoundHandler()
	if _, err := NewRouter(cfg); err == nil {
		t.Fatal("NewRouter accepted both SPA and NotFound")
	}
}

func TestRouterMethodNotAllowedHook(t *testing.T) {
	for _, tc := range []struct {
		name    string
		method  string
		path    string
		methods []string
	}{
		{"root extra mount", http.MethodPut, "/extra", []string{http.MethodGet}},
		{"exact prefix", http.MethodPut, "/ext/v1", []string{http.MethodGet}},
		{"prefix trailing slash", http.MethodPut, "/ext/v1/", []string{http.MethodGet}},
		{"surface", http.MethodPut, "/ext/v1/item", []string{http.MethodGet}},
		{"parameter", http.MethodPut, "/ext/v1/things/42", []string{http.MethodGet}},
		{"several methods", http.MethodPut, "/ext/v1/multi", []string{http.MethodGet, http.MethodPatch, http.MethodPost}},
		{"unknown method several methods", "PROPFIND", "/ext/v1/multi", []string{http.MethodGet, http.MethodPatch, http.MethodPost}},
		{"unknown method exact prefix", "PROPFIND", "/ext/v1", []string{http.MethodGet}},
		{"unknown method prefix trailing slash", "PROPFIND", "/ext/v1/", []string{http.MethodGet}},
		{"unknown method root extra mount", "PROPFIND", "/extra", []string{http.MethodGet}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
			cfg.Surfaces = []Surface{{
				Prefix: "/ext/v1", Source: domain.SourceAPI,
				Routes: []Route{
					{ID: "index", Method: http.MethodGet, Pattern: "/", Handler: http.NotFoundHandler()},
					{ID: "other", Method: http.MethodPost, Pattern: "/other", Handler: http.NotFoundHandler()},
					{ID: "item", Method: http.MethodGet, Pattern: "/item", Handler: http.NotFoundHandler()},
					{ID: "thing", Method: http.MethodGet, Pattern: "/things/{id}", Handler: http.NotFoundHandler()},
					{ID: "multi.read", Method: http.MethodGet, Pattern: "/multi", Handler: http.NotFoundHandler()},
					{ID: "multi.patch", Method: http.MethodPatch, Pattern: "/multi", Handler: http.NotFoundHandler()},
					{ID: "multi.post", Method: http.MethodPost, Pattern: "/multi", Handler: http.NotFoundHandler()},
					{ID: "unrelated", Method: http.MethodDelete, Pattern: "/unrelated", Handler: http.NotFoundHandler()},
				},
			}}
			cfg.ExtraMounts = []ExtraMount{{
				Method: http.MethodGet, Pattern: "/extra", Handler: http.NotFoundHandler(), Access: AccessPublic,
			}, {
				Method: http.MethodOptions, Pattern: "/root-only", Handler: http.NotFoundHandler(), Access: AccessPublic,
			}}
			calls := 0
			cfg.MethodNotAllowed = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if got, want := w.Header().Get("Allow"), strings.Join(tc.methods, ", "); got != want {
					t.Errorf("hook Allow = %q, want %q", got, want)
				}
				if r.URL.Path != tc.path {
					t.Errorf("hook path = %q, want %q", r.URL.Path, tc.path)
				}
				w.WriteHeader(http.StatusMethodNotAllowed)
				_, _ = w.Write([]byte("host method not allowed"))
			})
			router, err := NewRouter(cfg)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			if calls != 1 || rec.Code != http.StatusMethodNotAllowed || rec.Body.String() != "host method not allowed" {
				t.Errorf("MethodNotAllowed calls = %d, response = %d %q, want 1, 405 host method not allowed", calls, rec.Code, rec.Body)
			}
		})
	}
}

func TestRouterMethodNotAllowedKeepsSurfacesCopy(t *testing.T) {
	cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
	surfaces := []Surface{{
		Prefix: "/ext/v1", Source: domain.SourceAPI,
		Routes: []Route{
			{ID: "item", Method: http.MethodGet, Pattern: "/item", Handler: http.NotFoundHandler()},
			{ID: "other", Method: http.MethodPost, Pattern: "/other", Handler: http.NotFoundHandler()},
		},
	}}
	cfg.Surfaces = surfaces
	cfg.MethodNotAllowed = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	router, err := NewRouter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	surfaces[0].Prefix = "/other"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PROPFIND", "/ext/v1/item", nil)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Errorf("Allow = %q, want %q", got, http.MethodGet)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("PROPFIND", "/other/x", nil)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("/other/x status = %d, want 405", rec.Code)
	}
}

func TestRouterDefaultMethodNotAllowed(t *testing.T) {
	registry := &RouteRegistry{}
	registry.Register(Route{ID: "item", Method: http.MethodGet, Pattern: "/item", Handler: http.NotFoundHandler()})
	registry.Register(Route{ID: "multi.read", Method: http.MethodGet, Pattern: "/multi", Handler: http.NotFoundHandler()})
	registry.Register(Route{ID: "multi.post", Method: http.MethodPost, Pattern: "/multi", Handler: http.NotFoundHandler()})
	cfg := guardedRouterConfig(registry, testGuards(t))
	cfg.ExtraMounts = []ExtraMount{{
		Method: http.MethodGet, Pattern: "/extra", Handler: http.NotFoundHandler(), Access: AccessPublic,
	}}
	router, err := NewRouter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path    string
		methods []string
	}{
		{"/extra", []string{http.MethodGet}},
		{"/api/v1/item", []string{http.MethodGet}},
		{"/app/api/v1/item", []string{http.MethodGet}},
		{"/api/v1/multi", []string{http.MethodGet, http.MethodPost}},
		{"/app/api/v1/multi", []string{http.MethodGet, http.MethodPost}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, tc.path, nil)
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("default response = %d, want 405", rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("default body = %q, want empty", rec.Body.String())
			}
			methods := rec.Header().Values("Allow")
			slices.Sort(methods)
			if !slices.Equal(methods, tc.methods) {
				t.Errorf("default Allow values = %q, want %q", methods, tc.methods)
			}
		})
	}
}

func TestCustomSurfaceChain(t *testing.T) {
	for _, source := range []domain.Source{domain.SourceAPI, domain.SourcePanel} {
		t.Run(string(source), func(t *testing.T) {
			cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
			authorizer := &recordingAuthorizer{}
			cfg.Authorizer = authorizer
			var events []string
			cfg.BodyLimit = func(*http.Request) int64 {
				events = append(events, "limit")
				return 1024
			}
			cfg.CallerResolver = func(r *http.Request) (domain.Caller, error) {
				events = append(events, "resolve")
				if got, ok := SurfaceFromContext(r.Context()); !ok || got != source {
					t.Errorf("resolver surface = %q, present %t, want %q", got, ok, source)
				}
				if !DuringSetupFromContext(r.Context()) {
					t.Error("resolver DuringSetup = false, want true")
				}
				return domain.Caller{Principal: "alice"}, nil
			}
			cfg.Surfaces = []Surface{{
				Prefix: "/ext/v1", Source: source,
				Routes: []Route{{
					ID: "ext.read", Method: http.MethodGet, Pattern: "/item", DuringSetup: true,
					Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						events = append(events, "handler")
						if got, ok := SurfaceFromContext(r.Context()); !ok || got != source {
							t.Errorf("handler surface = %q, present %t, want %q", got, ok, source)
						}
						if len(authorizer.calls) != 1 {
							t.Errorf("authorization calls before handler = %d, want 1", len(authorizer.calls))
						}
						w.WriteHeader(http.StatusNoContent)
					}),
				}},
			}}
			router, err := NewRouter(cfg)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ext/v1/item", nil))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("custom route = %d %s, want 204", rec.Code, rec.Body)
			}
			if !slices.Equal(events, []string{"limit", "resolve", "handler"}) {
				t.Errorf("chain = %v, want [limit resolve handler]", events)
			}
			want := []authorizationCall{{
				source: source, principal: "alice", identifier: "ext.read", duringSetup: true,
			}}
			if !slices.Equal(authorizer.calls, want) {
				t.Errorf("authorization = %+v, want %+v", authorizer.calls, want)
			}
		})
	}
}

func TestCustomSurfaceUnknownPath(t *testing.T) {
	cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
	cfg.Surfaces = []Surface{{Prefix: "/ext/v1", Source: domain.SourceAPI}}
	router, err := NewRouter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ext/v1/missing", nil))
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
		t.Fatalf("unknown custom path = %d %s, want typed not_found", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("unknown custom path content type = %q, want JSON", got)
	}
}

func TestSurfaceWithoutManifestAllowsNilAccessMode(t *testing.T) {
	cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
	cfg.Surfaces = []Surface{{Prefix: "/ext/v1", Source: domain.SourceAPI}}
	cfg.AccessMode = nil
	if _, err := NewRouter(cfg); err != nil {
		t.Fatalf("NewRouter without a manifest: %v", err)
	}
}

func TestSurfaceValidation(t *testing.T) {
	api := func(prefix string) Surface { return Surface{Prefix: prefix, Source: domain.SourceAPI} }
	for _, tc := range []struct {
		name      string
		surfaces  []Surface
		mcpPath   string
		extraPath string
		want      string
		panic     bool
	}{
		{name: "no surfaces", want: "httpapi: nil route registry"},
		{name: "empty prefix", surfaces: []Surface{api("")}, want: `httpapi: invalid surface prefix ""`},
		{name: "no leading slash", surfaces: []Surface{api("ext/v1")}, want: `httpapi: invalid surface prefix "ext/v1"`},
		{name: "trailing slash", surfaces: []Surface{api("/ext/v1/")}, want: `httpapi: invalid surface prefix "/ext/v1/"`},
		{name: "root prefix", surfaces: []Surface{api("/")}, want: `httpapi: invalid surface prefix "/"`},
		{name: "parameter prefix with extra mount", surfaces: []Surface{api("/{tenant}/api/v1")}, extraPath: "/acme/api/v1/accounts", want: `httpapi: invalid surface prefix "/{tenant}/api/v1"`},
		{name: "opening brace", surfaces: []Surface{api("/ext/{")}, want: `httpapi: invalid surface prefix "/ext/{"`},
		{name: "closing brace", surfaces: []Surface{api("/ext/}")}, want: `httpapi: invalid surface prefix "/ext/}"`},
		{name: "wildcard prefix", surfaces: []Surface{api("/x/*")}, want: `httpapi: invalid surface prefix "/x/*"`},
		{name: "equivalent parameter prefixes", surfaces: []Surface{api("/{a}/v1"), api("/{b}/v1")}, want: `httpapi: invalid surface prefix "/{a}/v1"`},
		{name: "equal prefixes", surfaces: []Surface{api("/ext/v1"), api("/ext/v1")}, want: `httpapi: surface prefixes "/ext/v1" and "/ext/v1" overlap`},
		{name: "nested prefix", surfaces: []Surface{api("/ext/v1"), api("/ext/v1/child")}, want: `httpapi: surface prefixes "/ext/v1" and "/ext/v1/child" overlap`},
		{name: "nested prefix reversed", surfaces: []Surface{api("/ext/v1/child"), api("/ext/v1")}, want: `httpapi: surface prefixes "/ext/v1/child" and "/ext/v1" overlap`},
		{name: "missing source", surfaces: []Surface{{Prefix: "/ext/v1"}}, want: `httpapi: surface "/ext/v1" has invalid source ""`},
		{name: "MCP source", surfaces: []Surface{{Prefix: "/ext/v1", Source: domain.SourceMCP}}, want: `httpapi: surface "/ext/v1" has invalid source "mcp"`},
		{name: "unknown source", surfaces: []Surface{{Prefix: "/ext/v1", Source: "unknown"}}, want: `httpapi: surface "/ext/v1" has invalid source "unknown"`},
		{name: "two manifests", surfaces: []Surface{
			{Prefix: "/ext/v1", Source: domain.SourceAPI, Manifest: true},
			{Prefix: "/panel", Source: domain.SourcePanel, Manifest: true},
		}, want: "httpapi: more than one surface mounts the manifest"},
		{name: "empty route ID", surfaces: []Surface{{Prefix: "/ext/v1", Source: domain.SourceAPI, Routes: []Route{
			{Method: http.MethodGet, Pattern: "/item", Handler: http.NotFoundHandler()},
		}}}, want: `httpapi: route id is empty in surface "/ext/v1"`},
		{name: "duplicate route ID", surfaces: []Surface{{Prefix: "/ext/v1", Source: domain.SourceAPI, Routes: []Route{
			{ID: "ext.read", Method: http.MethodGet, Pattern: "/one", Handler: http.NotFoundHandler()},
			{ID: "ext.read", Method: http.MethodGet, Pattern: "/two", Handler: http.NotFoundHandler()},
		}}}, want: `httpapi: route id "ext.read" appears twice in surface "/ext/v1"`},
		{name: "public API route", surfaces: []Surface{{Prefix: "/ext/v1", Source: domain.SourceAPI, Routes: []Route{
			{ID: "ext.public", Method: http.MethodGet, Pattern: "/public", Handler: http.NotFoundHandler(), Public: true},
		}}}, want: `httpapi: route "ext.public" is public on non-panel surface "/ext/v1"`, panic: true},
		{name: "extra at prefix", surfaces: []Surface{api("/ext/v1")}, extraPath: "/ext/v1", want: "httpapi: extra mount GET /ext/v1 is under /ext/v1"},
		{name: "extra under prefix", surfaces: []Surface{api("/ext/v1")}, extraPath: "/ext/v1/extra", want: "httpapi: extra mount GET /ext/v1/extra is under /ext/v1"},
		{name: "MCP at prefix", surfaces: []Surface{api("/ext/v1")}, mcpPath: "/ext/v1", want: "httpapi: MCP path /ext/v1 is under /ext/v1"},
		{name: "MCP under prefix", surfaces: []Surface{api("/ext/v1")}, mcpPath: "/ext/v1/mcp", want: "httpapi: MCP path /ext/v1/mcp is under /ext/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
			cfg.Surfaces = tc.surfaces
			if tc.mcpPath != "" {
				cfg.MCP, cfg.MCPPath = http.NotFoundHandler(), tc.mcpPath
			}
			if tc.extraPath != "" {
				cfg.ExtraMounts = []ExtraMount{{Method: http.MethodGet, Pattern: tc.extraPath, Handler: http.NotFoundHandler(), Access: AccessPublic}}
			}
			defer func() {
				got := recover()
				if tc.panic && got != tc.want {
					t.Errorf("NewRouter panic = %v, want %q", got, tc.want)
				}
				if !tc.panic && got != nil {
					t.Errorf("NewRouter panicked: %v, want error %q", got, tc.want)
				}
			}()
			_, err := NewRouter(cfg)
			if !tc.panic && (err == nil || err.Error() != tc.want) {
				t.Errorf("NewRouter error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCustomSurfaceManifest(t *testing.T) {
	cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
	cfg.Surfaces = []Surface{
		{Prefix: "/ext/v1", Source: domain.SourceAPI, Routes: []Route{
			{ID: "shared", Method: http.MethodGet, Pattern: "/item", Handler: http.NotFoundHandler(), DuringSetup: true},
		}},
		{Prefix: "/ext/v1x", Source: domain.SourcePanel, Manifest: true, Routes: []Route{
			{ID: "shared", Method: http.MethodGet, Pattern: "/item", Handler: http.NotFoundHandler(), DuringSetup: true},
			{ID: "public", Method: http.MethodGet, Pattern: "/public", Handler: http.NotFoundHandler(), Public: true, DuringSetup: true},
		}},
	}
	cfg.Authorizer = allowIdentifiers{"shared"}
	cfg.MCP = http.NotFoundHandler()
	cfg.ExtraMounts = []ExtraMount{{Method: http.MethodGet, Pattern: "/extra", Handler: http.NotFoundHandler(), Access: AccessPublic}}
	router, err := NewRouter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ext/v1x/route-manifest.json", nil))
	want := `{"accessMode":"open","routes":[` +
		`{"method":"GET","path":"/ext/v1/item","access":"api"},` +
		`{"method":"GET","path":"/ext/v1x/item","access":"panel","duringSetup":true},` +
		`{"method":"GET","path":"/ext/v1x/public","access":"public","duringSetup":true},` +
		`{"method":"GET","path":"/ext/v1x/route-manifest.json","access":"panel"},` +
		`{"method":"GET","path":"/mcp","access":"agent"},` +
		`{"method":"POST","path":"/mcp","access":"agent"},` +
		`{"method":"DELETE","path":"/mcp","access":"agent"},` +
		`{"method":"GET","path":"/extra","access":"public"},` +
		`{"method":"GET","path":"/*","access":"public"}]}` + "\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Errorf("custom manifest = %d %s, want 200 %s", rec.Code, rec.Body, want)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ext/v1/route-manifest.json", nil))
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
		t.Errorf("disabled manifest = %d %s, want not_found", rec.Code, rec.Body)
	}
}

func TestV1SurfacesRejectsNilRoutes(t *testing.T) {
	cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
	cfg.Surfaces = V1Surfaces(nil, &RouteRegistry{})
	if _, err := NewRouter(cfg); err == nil || err.Error() != "httpapi: nil route registry" {
		t.Errorf("NewRouter with nil V1 routes = %v, want nil route registry", err)
	}
}
