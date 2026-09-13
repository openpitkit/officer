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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"go.openpit.dev/officer/framework/domain"
)

func testCallerResolver(*http.Request) (domain.Caller, error) {
	return domain.Caller{Principal: domain.PrincipalOperator}, nil
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
		BodyLimit: BodyLimitPolicy(1024, nil),
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

func TestRuntimeRouteManifestSnapshotsMountedRoutes(t *testing.T) {
	var registry RouteRegistry
	registry.Register(Route{
		ID:      "first",
		Method:  http.MethodGet,
		Pattern: "/first",
		Handler: http.NotFoundHandler(),
	})
	registry.Register(Route{
		ID:      "second",
		Method:  http.MethodPost,
		Pattern: "/second/{id}",
		Handler: http.NotFoundHandler(),
	})
	router, err := NewRouter(RouterConfig{
		Routes:         &registry,
		Authorizer:     AllowAll{},
		CallerResolver: testCallerResolver,
		SPA: fstest.MapFS{
			"index.html": {Data: []byte("<html></html>")},
		},
		BodyLimit: BodyLimitPolicy(1024, nil),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	registry.Register(Route{
		ID:      "late",
		Method:  http.MethodDelete,
		Pattern: "/late",
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
	want := "{\"routes\":[{\"method\":\"GET\",\"path\":\"/first\"}," +
		"{\"method\":\"POST\",\"path\":\"/second/{id}\"}," +
		"{\"method\":\"GET\",\"path\":\"/route-manifest.json\"}]}\n"
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
}

func TestRuntimeRouteManifestOmitsUnauthorizedRoutes(t *testing.T) {
	var registry RouteRegistry
	registry.Register(Route{
		ID: "public", Method: http.MethodGet, Pattern: "/public",
		Handler: http.NotFoundHandler(),
	})
	registry.Register(Route{
		ID: "secret", Method: http.MethodPost, Pattern: "/secret",
		Handler: http.NotFoundHandler(),
	})
	router, err := NewRouter(RouterConfig{
		Routes: &registry, Authorizer: denyIdentifier("secret"),
		CallerResolver: testCallerResolver,
		SPA:            fstest.MapFS{"index.html": {Data: []byte("<html></html>")}},
		BodyLimit:      BodyLimitPolicy(1024, nil),
	})
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
	want := "{\"routes\":[{\"method\":\"GET\",\"path\":\"/public\"}," +
		"{\"method\":\"GET\",\"path\":\"/route-manifest.json\"}]}\n"
	if rec.Body.String() != want {
		t.Fatalf("manifest = %q, want %q", rec.Body.String(), want)
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
		Authorizer:     denyIdentifier("denied"),
		CallerResolver: testCallerResolver,
		SPA: fstest.MapFS{
			"index.html": {Data: []byte("<html></html>")},
		},
		BodyLimit: BodyLimitPolicy(1024, nil),
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
		BodyLimit: BodyLimitPolicy(1024, nil),
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
		SPA:       fstest.MapFS{"index.html": {Data: []byte("<html></html>")}},
		BodyLimit: BodyLimitPolicy(1024, nil),
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
	router, err := NewRouter(RouterConfig{
		Routes:         &registry,
		Authorizer:     AllowAll{},
		CallerResolver: testCallerResolver,
		SPA: fstest.MapFS{
			"index.html": {Data: []byte("<html></html>")},
		},
		MCP: http.StripPrefix(
			"/custom/mcp",
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/ping" {
					t.Fatalf("MCP path after StripPrefix = %q, want /ping", r.URL.Path)
				}
				_, _ = w.Write([]byte("mcp"))
			}),
		),
		MCPPath:   "/custom/mcp",
		BodyLimit: BodyLimitPolicy(1024, nil),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/custom/mcp/ping", nil),
	)
	if rec.Code != http.StatusOK || rec.Body.String() != "mcp" {
		t.Fatalf("custom MCP = %d %q", rec.Code, rec.Body.String())
	}
}

type denyIdentifier string

func (d denyIdentifier) Authorize(
	_ context.Context,
	_ domain.Caller,
	identifier string,
) error {
	if identifier == string(d) {
		return domain.ErrForbidden
	}
	return nil
}

var _ Authorizer = denyIdentifier("")

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
