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
		Routes:     &registry,
		Authorizer: AllowAll{},
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

func TestNewRouterCustomAuthorizer(t *testing.T) {
	var registry RouteRegistry
	registry.Register(Route{
		ID:         "open",
		Method:     http.MethodGet,
		Pattern:    "/open",
		Permission: "read",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	})
	registry.Register(Route{
		ID:         "denied",
		Method:     http.MethodGet,
		Pattern:    "/denied",
		Permission: "deny",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("blocked"))
		}),
	})
	router, err := NewRouter(RouterConfig{
		Routes:     &registry,
		Authorizer: denyPermission("deny"),
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

func TestNewRouterCustomMCPPath(t *testing.T) {
	t.Parallel()

	var registry RouteRegistry
	router, err := NewRouter(RouterConfig{
		Routes:     &registry,
		Authorizer: AllowAll{},
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

type denyPermission string

func (d denyPermission) Authorize(
	_ context.Context,
	_ domain.Caller,
	permission string,
) error {
	if permission == string(d) {
		return domain.ErrForbidden
	}
	return nil
}

var _ Authorizer = denyPermission("")
