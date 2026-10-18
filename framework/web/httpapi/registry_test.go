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
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"go.openpit.dev/officer/framework/domain"
)

func testCallerResolver(*http.Request) (domain.Caller, error) {
	return domain.Caller{Principal: domain.PrincipalOperator}, nil
}

// testGuards returns the real browser guards, with the Host guard allowing the
// example.com host httptest.NewRequest addresses.
func testGuards(t *testing.T) BrowserGuards {
	t.Helper()
	host, err := AllowedHosts([]string{"example.com"})
	if err != nil {
		t.Fatalf("AllowedHosts: %v", err)
	}
	return BrowserGuards{Host: host, Origin: SameOrigin(), ContentType: JSONBody()}
}

func TestRouteRegistryReplaceAndUnregister(t *testing.T) {
	var registry RouteRegistry
	registry.Register(Route{
		ID:      "first",
		Method:  http.MethodGet,
		Pattern: "/first",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("first"))
		}),
	})
	registry.Register(Route{
		ID:      "second",
		Method:  http.MethodGet,
		Pattern: "/second",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("second"))
		}),
	})
	registry.Register(Route{
		ID:      "first",
		Method:  http.MethodGet,
		Pattern: "/first",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("replaced"))
		}),
	})

	routes := registry.Routes()
	if len(routes) != 2 || routes[0].ID != "first" || routes[1].ID != "second" {
		t.Fatalf("routes order after replace = %#v", routes)
	}
	if !registry.Unregister("second") {
		t.Fatalf("Unregister returned false")
	}
	if registry.Unregister("second") {
		t.Fatalf("Unregister returned true for absent route")
	}

	router, err := NewRouter(RouterConfig{
		Routes:         &registry,
		Authorizer:     AllowAll{},
		CallerResolver: testCallerResolver,
		SPA: fstest.MapFS{
			"index.html": {Data: []byte("<html></html>")},
		},
		BodyLimit:  BodyLimitPolicy(1024, nil),
		Guards:     testGuards(t),
		AccessMode: AccessModeOpen,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/first", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "replaced" {
		t.Fatalf("/first = %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/second", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "<html></html>" {
		t.Fatalf("/second fallback = %d %q", rec.Code, rec.Body.String())
	}
}

// TestRuntimeRouteManifestSnapshotsMountedRoutes pins the manifest of a router
// with every kind of mount: the access mode, then each operation in mount order
// with its full path and access, duringSetup only on the panel entries that
// carry it. A route registered after NewRouter is not mounted and not listed.
func TestRuntimeRouteManifestSnapshotsMountedRoutes(t *testing.T) {
	var registry, panel RouteRegistry
	registry.Register(Route{
		ID:      "first",
		Method:  http.MethodGet,
		Pattern: "/first",
		Handler: http.NotFoundHandler(),
	})
	registry.Register(Route{
		ID:          "second",
		Method:      http.MethodPost,
		Pattern:     "/second/{id}",
		Handler:     http.NotFoundHandler(),
		DuringSetup: true,
	})
	panel.Register(Route{
		ID:      "session.post",
		Method:  http.MethodPost,
		Pattern: "/session",
		Handler: http.NotFoundHandler(),
		Public:  true,
	})
	panel.Register(Route{
		ID:          "setup.get",
		Method:      http.MethodGet,
		Pattern:     "/setup",
		Handler:     http.NotFoundHandler(),
		DuringSetup: true,
	})
	cfg := guardedRouterConfig(&registry, testGuards(t))
	cfg.PanelRoutes = &panel
	cfg.MCP = http.NotFoundHandler()
	cfg.ExtraMounts = []ExtraMount{
		{
			Method: http.MethodGet, Pattern: "/healthz",
			Handler: http.NotFoundHandler(), Access: AccessPublic,
		},
		{
			Method: http.MethodPost, Pattern: "/hook",
			Handler: http.NotFoundHandler(), Access: AccessAgent,
		},
	}
	cfg.AccessMode = AccessModeToken
	router, err := NewRouter(cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	registry.Register(Route{
		ID:      "late",
		Method:  http.MethodDelete,
		Pattern: "/late",
		Handler: http.NotFoundHandler(),
	})
	panel.Register(Route{
		ID:      "panel.late",
		Method:  http.MethodDelete,
		Pattern: "/panel-late",
		Handler: http.NotFoundHandler(),
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(
		rec,
		httptest.NewRequest(
			http.MethodGet,
			"/api/v1/route-manifest.json",
			nil,
		),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("manifest content type = %q", got)
	}
	want := `{"accessMode":"token","routes":[` +
		`{"method":"GET","path":"/api/v1/first","access":"api"},` +
		`{"method":"POST","path":"/api/v1/second/{id}","access":"api"},` +
		`{"method":"GET","path":"/api/v1/route-manifest.json","access":"api"},` +
		`{"method":"GET","path":"/app/api/v1/first","access":"panel"},` +
		`{"method":"POST","path":"/app/api/v1/second/{id}","access":"panel","duringSetup":true},` +
		`{"method":"POST","path":"/app/api/v1/session","access":"public"},` +
		`{"method":"GET","path":"/app/api/v1/setup","access":"panel","duringSetup":true},` +
		`{"method":"GET","path":"/mcp","access":"agent"},` +
		`{"method":"POST","path":"/mcp","access":"agent"},` +
		`{"method":"DELETE","path":"/mcp","access":"agent"},` +
		`{"method":"GET","path":"/healthz","access":"public"},` +
		`{"method":"POST","path":"/hook","access":"agent"},` +
		`{"method":"GET","path":"/*","access":"public"}]}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("manifest = %q, want %q", rec.Body.String(), want)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(
		rec,
		httptest.NewRequest(
			http.MethodPost,
			"/api/v1/route-manifest.json",
			nil,
		),
	)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST manifest status = %d, want 405", rec.Code)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/app/api/v1/route-manifest.json", nil,
	))
	if rec.Body.String() != "<html></html>" {
		t.Fatalf("panel manifest = %d %q, want the SPA shell", rec.Code, rec.Body.String())
	}
}

// TestRuntimeRouteManifestOmitsUnauthorizedRoutes checks that a registry route
// is listed, on either prefix and from PanelRoutes, only when the Authorizer
// allows the caller its id, while the manifest, MCP, extra mount and SPA
// entries are listed whatever the Authorizer answers.
func TestRuntimeRouteManifestOmitsUnauthorizedRoutes(t *testing.T) {
	var registry, panel RouteRegistry
	registry.Register(Route{
		ID: "allowed", Method: http.MethodGet, Pattern: "/allowed",
		Handler: http.NotFoundHandler(),
	})
	registry.Register(Route{
		ID: "denied", Method: http.MethodPost, Pattern: "/denied",
		Handler: http.NotFoundHandler(),
	})
	panel.Register(Route{
		ID: "panel.allowed", Method: http.MethodGet, Pattern: "/panel-allowed",
		Handler: http.NotFoundHandler(),
	})
	panel.Register(Route{
		ID: "panel.denied", Method: http.MethodPost, Pattern: "/panel-denied",
		Handler: http.NotFoundHandler(),
	})
	cfg := guardedRouterConfig(&registry, testGuards(t))
	cfg.PanelRoutes = &panel
	// Every other identifier is refused, the empty one included, so an entry
	// the manifest wrongly put to the Authorizer would drop out.
	cfg.Authorizer = allowIdentifiers{"allowed", "panel.allowed"}
	cfg.MCP = http.NotFoundHandler()
	cfg.ExtraMounts = []ExtraMount{{
		Method: http.MethodGet, Pattern: "/extra",
		Handler: http.NotFoundHandler(), Access: AccessPublic,
	}}
	router, err := NewRouter(cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/route-manifest.json", nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest status = %d, want 200", rec.Code)
	}
	want := `{"accessMode":"open","routes":[` +
		`{"method":"GET","path":"/api/v1/allowed","access":"api"},` +
		`{"method":"GET","path":"/api/v1/route-manifest.json","access":"api"},` +
		`{"method":"GET","path":"/app/api/v1/allowed","access":"panel"},` +
		`{"method":"GET","path":"/app/api/v1/panel-allowed","access":"panel"},` +
		`{"method":"GET","path":"/mcp","access":"agent"},` +
		`{"method":"POST","path":"/mcp","access":"agent"},` +
		`{"method":"DELETE","path":"/mcp","access":"agent"},` +
		`{"method":"GET","path":"/extra","access":"public"},` +
		`{"method":"GET","path":"/*","access":"public"}]}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("manifest = %q, want %q", rec.Body.String(), want)
	}
}

// TestPanelRoutesServeOnlyThePanelMirror checks that a PanelRoutes route
// answers on /app/api/v1 through the mirror's chain - the caller resolved for
// the panel and the route authorized by its id - and is not mounted on /api/v1.
func TestPanelRoutesServeOnlyThePanelMirror(t *testing.T) {
	served := 0
	var panel RouteRegistry
	panel.Register(Route{
		ID: "panel.only", Method: http.MethodGet, Pattern: "/panel-only",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			served++
			w.WriteHeader(http.StatusNoContent)
		}),
	})
	authorizer := &recordingAuthorizer{}
	cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
	cfg.PanelRoutes = &panel
	cfg.Authorizer = authorizer
	router, err := NewRouter(cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app/api/v1/panel-only", nil))
	if rec.Code != http.StatusNoContent || served != 1 {
		t.Fatalf("/app/api/v1/panel-only = %d, served %d times", rec.Code, served)
	}
	if len(authorizer.calls) != 1 || authorizer.calls[0].identifier != "panel.only" ||
		authorizer.calls[0].source != domain.SourcePanel {
		t.Fatalf("authorization calls = %+v, want the panel route ID", authorizer.calls)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/panel-only", nil))
	if served != 1 || rec.Body.String() != "<html></html>" {
		t.Fatalf("/api/v1/panel-only = %d %q, want the SPA shell", rec.Code, rec.Body.String())
	}
}

func TestNewRouterRequiresAccessMode(t *testing.T) {
	t.Parallel()

	for _, mode := range []AccessMode{AccessModeSetup, AccessModeToken, AccessModeOpen} {
		cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
		cfg.AccessMode = mode
		if _, err := NewRouter(cfg); err != nil {
			t.Fatalf("NewRouter(%q): %v", mode, err)
		}
	}
	for _, mode := range []AccessMode{"", "closed"} {
		cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
		cfg.AccessMode = mode
		_, err := NewRouter(cfg)
		want := fmt.Sprintf("httpapi: invalid access mode %q", mode)
		if err == nil || err.Error() != want {
			t.Fatalf("NewRouter(%q) error = %v, want %q", mode, err, want)
		}
	}
}

// TestNewRouterPanicsOnUndeclaredAccess checks that NewRouter refuses to
// register a public route outside PanelRoutes and an extra mount that does not
// declare public or agent access.
func TestNewRouterPanicsOnUndeclaredAccess(t *testing.T) {
	t.Parallel()

	extraMount := func(access Access) func(*RouterConfig) {
		return func(cfg *RouterConfig) {
			cfg.ExtraMounts = []ExtraMount{{
				Method: http.MethodGet, Pattern: "/extra",
				Handler: http.NotFoundHandler(), Access: access,
			}}
		}
	}
	for _, tc := range []struct {
		name  string
		setup func(*RouterConfig)
		want  string
	}{
		{
			name: "public route in Routes",
			setup: func(cfg *RouterConfig) {
				cfg.Routes.Register(Route{
					ID: "session.get", Method: http.MethodGet, Pattern: "/session",
					Handler: http.NotFoundHandler(), Public: true,
				})
			},
			want: `httpapi: route "session.get" is public: public routes belong to PanelRoutes`,
		},
		{
			name:  "extra mount without access",
			setup: extraMount(""),
			want:  "httpapi: extra mount GET /extra has no valid access kind",
		},
		{
			name:  "extra mount with API access",
			setup: extraMount(AccessAPI),
			want:  "httpapi: extra mount GET /extra has no valid access kind",
		},
		{
			name:  "extra mount with panel access",
			setup: extraMount(AccessPanel),
			want:  "httpapi: extra mount GET /extra has no valid access kind",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
			tc.setup(&cfg)
			defer func() {
				if got := recover(); got != tc.want {
					t.Fatalf("NewRouter panic = %v, want %q", got, tc.want)
				}
			}()
			_, _ = NewRouter(cfg)
		})
	}
}

// TestNewRouterRefusesDuplicateOperations checks that an operation mounted
// twice - whichever mounts collide, and however the parameter names, the
// regexp anchors or the method case differ - a route id in both registries,
// and an extra mount or MCP path under a v1 prefix fail NewRouter instead of
// one mount shadowing the other, while two regexps on one segment are two
// operations and mounts beside the v1 prefixes are accepted.
func TestNewRouterRefusesDuplicateOperations(t *testing.T) {
	t.Parallel()

	route := func(id, method, pattern string) Route {
		return Route{ID: id, Method: method, Pattern: pattern, Handler: http.NotFoundHandler()}
	}
	extraMount := func(method, pattern string) ExtraMount {
		return ExtraMount{
			Method: method, Pattern: pattern,
			Handler: http.NotFoundHandler(), Access: AccessPublic,
		}
	}
	for _, tc := range []struct {
		name        string
		routes      []Route
		panelRoutes []Route
		extraMounts []ExtraMount
		mcpPath     string
		want        string
	}{
		{
			name:        "panel route on a mirrored path",
			routes:      []Route{route("accounts.list", http.MethodGet, "/accounts")},
			panelRoutes: []Route{route("panel.accounts", http.MethodGet, "/accounts")},
			want:        "httpapi: GET /app/api/v1/accounts is mounted twice",
		},
		{
			name:        "extra mount on a v1 path",
			routes:      []Route{route("accounts.list", http.MethodGet, "/accounts")},
			extraMounts: []ExtraMount{extraMount(http.MethodGet, "/api/v1/accounts")},
			want:        "httpapi: extra mount GET /api/v1/accounts is under /api/v1",
		},
		{
			name:   "route on the manifest path",
			routes: []Route{route("manifest", http.MethodGet, "/route-manifest.json")},
			want:   "httpapi: GET /api/v1/route-manifest.json is mounted twice",
		},
		{
			name:        "extra mount on the MCP path",
			extraMounts: []ExtraMount{extraMount(http.MethodPost, "/mcp")},
			want:        "httpapi: POST /mcp is mounted twice",
		},
		{
			name:        "route id in both registries",
			routes:      []Route{route("accounts.list", http.MethodGet, "/accounts")},
			panelRoutes: []Route{route("accounts.list", http.MethodGet, "/panel-accounts")},
			want:        `httpapi: route id "accounts.list" is in both Routes and PanelRoutes`,
		},
		{
			name:        "panel route on a mirrored path under another parameter name",
			routes:      []Route{route("accounts.get", http.MethodGet, "/accounts/{code}")},
			panelRoutes: []Route{route("panel.account", http.MethodGet, "/accounts/{id}")},
			want:        "httpapi: GET /app/api/v1/accounts/{id} is mounted twice",
		},
		{
			name:        "extra mount on a v1 path under another parameter name",
			routes:      []Route{route("accounts.get", http.MethodGet, "/accounts/{code}")},
			extraMounts: []ExtraMount{extraMount(http.MethodGet, "/api/v1/accounts/{id}")},
			want:        "httpapi: extra mount GET /api/v1/accounts/{id} is under /api/v1",
		},
		{
			name:   "extra mount overlapping a v1 path under another key",
			routes: []Route{route("accounts.get", http.MethodGet, "/accounts/{code}")},
			extraMounts: []ExtraMount{
				extraMount(http.MethodGet, "/api/v1/accounts/{id:[0-9]+}"),
			},
			want: "httpapi: extra mount GET /api/v1/accounts/{id:[0-9]+} is under /api/v1",
		},
		{
			name:        "extra mount on a panel path",
			extraMounts: []ExtraMount{extraMount(http.MethodGet, "/app/api/v1/x")},
			want:        "httpapi: extra mount GET /app/api/v1/x is under /app/api/v1",
		},
		{
			name:        "extra mount on the v1 prefix",
			extraMounts: []ExtraMount{extraMount(http.MethodGet, "/api/v1")},
			want:        "httpapi: extra mount GET /api/v1 is under /api/v1",
		},
		{
			name:    "MCP path on a v1 path",
			mcpPath: "/api/v1/mcp",
			want:    "httpapi: MCP path /api/v1/mcp is under /api/v1",
		},
		{
			name: "extra mounts beside the v1 prefixes",
			extraMounts: []ExtraMount{
				extraMount(http.MethodGet, "/api/openapi.yaml"),
				extraMount(http.MethodGet, "/api/v1x"),
			},
		},
		{
			name: "one regexp under two parameter names",
			routes: []Route{
				route("accounts.get", http.MethodGet, "/accounts/{code:[0-9]+}"),
				route("accounts.by-id", http.MethodGet, "/accounts/{id:[0-9]+}"),
			},
			want: "httpapi: GET /api/v1/accounts/{id:[0-9]+} is mounted twice",
		},
		{
			name: "one regexp anchored and not",
			routes: []Route{
				route("accounts.get", http.MethodGet, "/accounts/{code:[0-9]+}"),
				route("accounts.by-id", http.MethodGet, "/accounts/{id:^[0-9]+$}"),
			},
			want: "httpapi: GET /api/v1/accounts/{id:^[0-9]+$} is mounted twice",
		},
		{
			name: "one regexp anchored at either end",
			routes: []Route{
				route("accounts.get", http.MethodGet, "/accounts/{code:^[0-9]+}"),
				route("accounts.by-id", http.MethodGet, "/accounts/{id:[0-9]+$}"),
			},
			want: "httpapi: GET /api/v1/accounts/{id:[0-9]+$} is mounted twice",
		},
		{
			name:        "panel route on a mirrored path in lower case",
			routes:      []Route{route("accounts.list", http.MethodGet, "/accounts")},
			panelRoutes: []Route{route("panel.accounts", "get", "/accounts")},
			want:        "httpapi: get /app/api/v1/accounts is mounted twice",
		},
		{
			name: "two regexps on one segment",
			routes: []Route{
				route("accounts.by-id", http.MethodGet, "/accounts/{id:[0-9]+}"),
				route("accounts.by-code", http.MethodGet, "/accounts/{id:[a-z]+}"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var registry, panel RouteRegistry
			for _, route := range tc.routes {
				registry.Register(route)
			}
			for _, route := range tc.panelRoutes {
				panel.Register(route)
			}
			cfg := guardedRouterConfig(&registry, testGuards(t))
			cfg.PanelRoutes = &panel
			cfg.MCP = http.NotFoundHandler()
			cfg.MCPPath = tc.mcpPath
			cfg.ExtraMounts = tc.extraMounts
			_, err := NewRouter(cfg)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("NewRouter error = %v, want none", err)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Fatalf("NewRouter error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNewRouterCustomAuthorizer(t *testing.T) {
	var registry RouteRegistry
	registry.Register(Route{
		ID:      "open",
		Method:  http.MethodGet,
		Pattern: "/open",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	})
	registry.Register(Route{
		ID:      "denied",
		Method:  http.MethodGet,
		Pattern: "/denied",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("blocked"))
		}),
	})
	router, err := NewRouter(RouterConfig{
		Routes:         &registry,
		Authorizer:     allowIdentifiers{"open"},
		CallerResolver: testCallerResolver,
		SPA: fstest.MapFS{
			"index.html": {Data: []byte("<html></html>")},
		},
		BodyLimit:  BodyLimitPolicy(1024, nil),
		Guards:     testGuards(t),
		AccessMode: AccessModeOpen,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/open", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("/open = %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/denied", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("/denied status = %d, want 403", rec.Code)
	}
}

func TestRouteAuthorizerReceivesRouteID(t *testing.T) {
	var registry RouteRegistry
	registry.Register(Route{
		ID:      "route.stable-id",
		Method:  http.MethodGet,
		Pattern: "/tracked",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	})
	authorizer := &recordingAuthorizer{}
	router, err := NewRouter(RouterConfig{
		Routes:     &registry,
		Authorizer: authorizer,
		CallerResolver: func(*http.Request) (domain.Caller, error) {
			return domain.Caller{Principal: "alice", Role: "admin"}, nil
		},
		SPA: fstest.MapFS{
			"index.html": {Data: []byte("<html></html>")},
		},
		BodyLimit:  BodyLimitPolicy(1024, nil),
		Guards:     testGuards(t),
		AccessMode: AccessModeOpen,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tracked", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("tracked route status = %d, want 204", rec.Code)
	}
	if len(authorizer.calls) != 1 {
		t.Fatalf("authorization calls = %+v, want one call", authorizer.calls)
	}
	call := authorizer.calls[0]
	if call.identifier != "route.stable-id" || call.source != domain.SourceAPI ||
		call.principal != "alice" || call.role != "admin" {
		t.Fatalf("authorization call = %+v, want API route ID", call)
	}
}

func TestResolverErrorRejectsRequestBeforeHandler(t *testing.T) {
	var registry RouteRegistry
	handlerCalled := false
	registry.Register(Route{
		ID: "blocked", Method: http.MethodPost, Pattern: "/blocked",
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			handlerCalled = true
		}),
	})
	router, err := NewRouter(RouterConfig{
		Routes: &registry, Authorizer: AllowAll{},
		CallerResolver: func(*http.Request) (domain.Caller, error) {
			return domain.Caller{}, domain.ErrForbidden
		},
		SPA:        fstest.MapFS{"index.html": {Data: []byte("<html></html>")}},
		BodyLimit:  BodyLimitPolicy(1024, nil),
		Guards:     testGuards(t),
		AccessMode: AccessModeOpen,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/blocked", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if handlerCalled {
		t.Fatal("handler ran after caller resolver rejected the request")
	}
}

func TestNewRouterCustomMCPPath(t *testing.T) {
	t.Parallel()

	var registry RouteRegistry
	served := 0
	router, err := NewRouter(RouterConfig{
		Routes:         &registry,
		Authorizer:     AllowAll{},
		CallerResolver: testCallerResolver,
		SPA: fstest.MapFS{
			"index.html": {Data: []byte("<html></html>")},
		},
		MCP: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			served++
			_, _ = w.Write([]byte("mcp"))
		}),
		MCPPath:    "/custom/mcp",
		BodyLimit:  BodyLimitPolicy(1024, nil),
		Guards:     testGuards(t),
		AccessMode: AccessModeOpen,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, "/custom/mcp", nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "mcp" {
			t.Fatalf("%s custom MCP = %d %q", method, rec.Code, rec.Body.String())
		}
	}
	// Only the exact path, and only the streamable-HTTP methods, reach MCP.
	for _, tc := range []struct{ method, target string }{
		{http.MethodGet, "/custom/mcp/ping"},
		{http.MethodPost, "/custom/mcp/ping"},
		{http.MethodPut, "/custom/mcp"},
		{http.MethodPatch, "/custom/mcp"},
	} {
		served = 0
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))
		if served != 0 {
			t.Fatalf("%s %s reached the MCP handler: %d %q",
				tc.method, tc.target, rec.Code, rec.Body.String())
		}
	}
}

// mcpBodyRead is what the MCP test handler observed of one request body: the
// bytes it managed to read and the read error, in the shape the MCP SDK sees
// when it drains the body with io.ReadAll.
type mcpBodyRead struct {
	body []byte
	err  error
}

// newMCPBodyLimitRouter builds a router whose MCP handler drains the request
// body like the MCP SDK does and reports each read on reads.
func newMCPBodyLimitRouter(
	t *testing.T, limit int64, reads chan<- mcpBodyRead,
) http.Handler {
	t.Helper()
	var registry RouteRegistry
	router, err := NewRouter(RouterConfig{
		Routes:         &registry,
		Authorizer:     AllowAll{},
		CallerResolver: testCallerResolver,
		SPA: fstest.MapFS{
			"index.html": {Data: []byte("<html></html>")},
		},
		MCP: http.StripPrefix(
			"/mcp",
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				reads <- mcpBodyRead{body: body, err: err}
				if err != nil {
					http.Error(w, "failed to read body", http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusOK)
			}),
		),
		MCPPath:    "/mcp",
		BodyLimit:  BodyLimitPolicy(limit, nil),
		Guards:     testGuards(t),
		AccessMode: AccessModeOpen,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return router
}

// TestNewRouterMCPOversizeBodyIsCutAtLimit checks that the MCP mount sits
// behind the same body limit as every route: the handler never reads past the
// limit, whether the client declares the length or streams it, and the read
// error answers 400 like an ordinary route's decode error.
func TestNewRouterMCPOversizeBodyIsCutAtLimit(t *testing.T) {
	t.Parallel()

	const limit = 64
	oversize := strings.Repeat("x", limit+1)
	tests := []struct {
		name string
		body func() io.Reader
		want int64
	}{
		{
			name: "declared length",
			body: func() io.Reader { return strings.NewReader(oversize) },
			want: limit + 1,
		},
		{
			// A non-bytes reader leaves Content-Length undeclared, as a
			// chunked transfer does, so only the read itself can stop the body.
			name: "undeclared length",
			body: func() io.Reader {
				return io.LimitReader(strings.NewReader(oversize), limit+1)
			},
			want: -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reads := make(chan mcpBodyRead, 1)
			router := newMCPBodyLimitRouter(t, limit, reads)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/mcp", tt.body())
			if req.ContentLength != tt.want {
				t.Fatalf("Content-Length = %d, want %d", req.ContentLength, tt.want)
			}
			// The media type passes the JSON guard, so the limit is what cuts.
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			var read mcpBodyRead
			select {
			case read = <-reads:
			default:
				t.Fatalf("MCP handler did not run; status = %d", rec.Code)
			}
			var tooLarge *http.MaxBytesError
			if !errors.As(read.err, &tooLarge) {
				t.Fatalf("MCP handler read error = %v, want *http.MaxBytesError", read.err)
			}
			if len(read.body) > limit {
				t.Fatalf("MCP handler read %d bytes, want at most %d", len(read.body), limit)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
		})
	}
}

// allowIdentifiers authorizes only the listed identifiers.
type allowIdentifiers []string

func (a allowIdentifiers) Authorize(
	_ context.Context,
	_ domain.Caller,
	identifier string,
) error {
	if slices.Contains(a, identifier) {
		return nil
	}
	return domain.ErrForbidden
}

var _ Authorizer = allowIdentifiers(nil)

type authorizationCall struct {
	source     domain.Source
	principal  string
	role       string
	identifier string
}

type recordingAuthorizer struct {
	calls []authorizationCall
}

func (a *recordingAuthorizer) Authorize(
	_ context.Context,
	caller domain.Caller,
	identifier string,
) error {
	a.calls = append(a.calls, authorizationCall{
		source:     caller.Source,
		principal:  caller.Principal,
		role:       caller.Role,
		identifier: identifier,
	})
	return nil
}
