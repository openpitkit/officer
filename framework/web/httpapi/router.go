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
	"errors"
	"io/fs"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
)

// The v1 REST surface and its panel mirror.
const (
	apiPrefix   = "/api/v1"
	panelPrefix = "/app/api/v1"
)

// mcpMethods are the methods of the streamable-HTTP MCP transport, the only
// ones NewRouter routes to the MCP handler.
var mcpMethods = []string{http.MethodGet, http.MethodPost, http.MethodDelete}

// ExtraMount is a route the host application mounts before the SPA fallback;
// NewRouter refuses one at or under any configured surface prefix.
type ExtraMount struct {
	Method  string
	Pattern string
	Handler http.Handler
	// Access is what the mount requires. Extra mounts bypass ResolveCaller and
	// Authorize, so each declares it; only AccessPublic and AccessAgent are
	// valid, and NewRouter returns an error for any other.
	Access Access
}

// LogSource is a source of recent service log lines.
type LogSource interface {
	Snapshot() []string
}

// Surface is one REST prefix NewRouter mounts. Source is stamped in the request
// context and passed to the caller resolver; only SourceAPI and SourcePanel are
// valid. Public routes are valid only on a SourcePanel surface. Manifest mounts
// the runtime route manifest under this prefix.
type Surface struct {
	Prefix   string
	Source   domain.Source
	Routes   []Route
	Manifest bool
}

// V1Surfaces snapshots Officer's API routes and their panel mirror, followed by
// the panel-only routes. A nil panelRoutes adds none. A nil routes returns nil,
// which NewRouter refuses with "httpapi: nil route registry".
func V1Surfaces(routes, panelRoutes *RouteRegistry) []Surface {
	if routes == nil {
		return nil
	}
	return []Surface{
		{Prefix: apiPrefix, Source: domain.SourceAPI, Routes: routes.Routes(), Manifest: true},
		{Prefix: panelPrefix, Source: domain.SourcePanel, Routes: slices.Concat(routes.Routes(), panelRoutes.Routes())},
	}
}

// RouterConfig configures NewRouter.
type RouterConfig struct {
	Surfaces       []Surface
	Authorizer     Authorizer
	CallerResolver auth.CallerResolver
	// SPA serves unmatched paths outside the surfaces. Nil disables the SPA
	// and omits GET /* from the manifest. It cannot be set with NotFound.
	SPA fs.FS
	// NotFound handles unmatched paths at the root and inside every surface.
	// Nil uses the SPA at the root when set, otherwise the typed JSON 404;
	// inside surfaces nil always uses the typed JSON 404.
	NotFound http.Handler
	// MethodNotAllowed handles 405 responses after Allow is set to the
	// matched path's supported methods, sorted lexicographically. The hook
	// writes the status and body. Nil keeps chi's default 405 everywhere.
	MethodNotAllowed http.Handler
	MCP              http.Handler
	MCPPath          string
	Logs             LogSource
	BodyLimit        func(*http.Request) int64
	ExtraMounts      []ExtraMount
	Guards           BrowserGuards
	// AccessMode reports the access state of the service. The router calls it
	// on every route manifest request, concurrently from request goroutines,
	// so it must be safe for concurrent use. A value that is not an AccessMode
	// constant answers an internal error. It is required only when a surface
	// has Manifest set to true.
	AccessMode func() AccessMode
}

// NewRouter builds the framework HTTP handler, mounting Surfaces in list order.
// It requires at least one surface with a non-empty, slash-prefixed literal
// prefix without a trailing slash or any '{', '}' or '*'. Prefixes must not be
// equal or nested, and sources must be SourceAPI or SourcePanel. At most one
// surface may mount the manifest; that surface requires a non-nil AccessMode.
// It returns an error for a public route outside a SourcePanel surface or an
// extra mount without a valid access kind. It refuses empty route IDs, duplicate
// route IDs within a surface, duplicate operations, and extra mounts or MCP
// paths at or under any surface prefix. No surfaces returns
// "httpapi: nil route registry".
// Behind Guards, the MCP mount refuses, with domain.ErrOriginNotAllowed, a
// request a browser sent from another origin on every method, GET included.
// Every surface route carries its
// configured Source in the request context for SurfaceFromContext; the MCP
// mount carries domain.SourceMCP.
// SPA is optional: without it, unmatched paths answer the typed JSON 404.
// NotFound overrides that response at the root and inside every surface;
// setting both SPA and NotFound returns an error. MethodNotAllowed, when set,
// receives Allow computed from the mounted chi tree and writes the response.
// A nil MethodNotAllowed preserves chi's default 405.
func NewRouter(cfg RouterConfig) (http.Handler, error) {
	if len(cfg.Surfaces) == 0 {
		return nil, errors.New("httpapi: nil route registry")
	}
	cfg.Surfaces = slices.Clone(cfg.Surfaces)
	if cfg.Authorizer == nil {
		return nil, errors.New("httpapi: nil authorizer")
	}
	if cfg.CallerResolver == nil {
		return nil, errors.New("httpapi: nil caller resolver")
	}
	if cfg.SPA != nil && cfg.NotFound != nil {
		return nil, errors.New("httpapi: SPA and NotFound cannot both be set")
	}
	if cfg.BodyLimit == nil {
		return nil, errors.New("httpapi: nil body limit policy")
	}
	if cfg.Guards.Host == nil {
		return nil, errors.New("httpapi: nil host guard")
	}
	if cfg.Guards.Origin == nil {
		return nil, errors.New("httpapi: nil origin guard")
	}
	if cfg.Guards.ContentType == nil {
		return nil, errors.New("httpapi: nil content-type guard")
	}
	for _, surface := range cfg.Surfaces {
		if surface.Manifest && cfg.AccessMode == nil {
			return nil, errors.New("httpapi: nil access mode")
		}
	}

	mcpPath := ""
	if cfg.MCP != nil {
		mcpPath = cfg.MCPPath
		if mcpPath == "" {
			mcpPath = "/mcp"
		}
	}
	operations, err := mountedOperations(
		cfg.Surfaces, mcpPath, cfg.ExtraMounts, cfg.SPA != nil,
	)
	if err != nil {
		return nil, err
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)
	router.Use(cfg.Guards.Host)
	router.Use(cfg.Guards.Origin)
	router.Use(cfg.Guards.ContentType)
	notFound := cfg.NotFound
	if notFound == nil {
		notFound = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			WriteErr(w, domain.ErrNotFound)
		})
	}

	surfaceAllows := make(map[string]func(string) string, len(cfg.Surfaces))
	for _, surface := range cfg.Surfaces {
		var manifest http.Handler
		if surface.Manifest {
			manifest = newRuntimeRouteManifestHandler(cfg.AccessMode, operations, cfg.Authorizer)
		}
		surfaceAllows[surface.Prefix] = mountSurface(router, surface, cfg.Authorizer, cfg.CallerResolver, cfg.BodyLimit, manifest, notFound, cfg.MethodNotAllowed)
	}

	if cfg.MCP != nil {
		// The MCP handler resolves the caller itself, so the surface is stamped
		// before it.
		mcp := Chain{
			sameOriginOnEveryMethod(),
			LimitBody(cfg.BodyLimit),
			withSurface(domain.SourceMCP),
		}.Then(cfg.MCP)
		for _, method := range mcpMethods {
			register(router, method, mcpPath, mcp)
		}
	}
	for _, mount := range cfg.ExtraMounts {
		register(router, mount.Method, mount.Pattern, mount.Handler)
	}

	if cfg.SPA != nil {
		spa, err := newSPAHandler(cfg.SPA)
		if err != nil {
			return nil, err
		}
		notFound = spa
	}
	router.NotFound(notFound.ServeHTTP)
	if cfg.MethodNotAllowed != nil {
		rootAllow := allowedMethods(router)
		router.MethodNotAllowed(methodNotAllowed(func(path string) string {
			if prefix := surfacePrefixOf(path, cfg.Surfaces); prefix != "" {
				path = strings.TrimPrefix(path, prefix)
				if path == "" {
					path = "/"
				}
				return surfaceAllows[prefix](path)
			}
			return rootAllow(path)
		}, cfg.MethodNotAllowed))
	}

	return router, nil
}

func mountSurface(
	router chi.Router,
	surface Surface,
	authorizer Authorizer,
	callerResolver auth.CallerResolver,
	bodyLimit func(*http.Request) int64,
	manifest http.Handler,
	notFound http.Handler,
	methodNotAllowedHook http.Handler,
) func(string) string {
	var allow func(string) string
	router.Route(surface.Prefix, func(v1 chi.Router) {
		v1.NotFound(notFound.ServeHTTP)
		limit := LimitBody(bodyLimit)
		base := Chain{limit, ResolveCaller(callerResolver, surface.Source)}
		// A public route carries no credentials: nothing resolves a caller for
		// it or authorizes it, so only the surface is stamped.
		public := Chain{limit, withSurface(surface.Source)}
		if manifest != nil {
			register(
				v1,
				http.MethodGet,
				runtimeRouteManifestPath,
				base.Then(manifest),
			)
		}
		for _, route := range surface.Routes {
			if route.Public {
				register(v1, route.Method, route.Pattern, public.Then(route.Handler))
				continue
			}
			chain := slices.Concat(Chain{withDuringSetup(route.DuringSetup)}, base,
				Chain{AuthorizeMiddleware(authorizer, route.ID)})
			register(v1, route.Method, route.Pattern, chain.Then(route.Handler))
		}
		if methodNotAllowedHook != nil {
			allow = allowedMethods(v1)
			v1.MethodNotAllowed(methodNotAllowed(allow, methodNotAllowedHook))
		}
	})
	return allow
}

func allowedMethods(router chi.Routes) func(string) string {
	var routedMethods []string
	for _, route := range router.Routes() {
		for method := range route.Handlers {
			if method != "*" && !slices.Contains(routedMethods, method) {
				routedMethods = append(routedMethods, method)
			}
		}
	}
	slices.Sort(routedMethods)

	return func(path string) string {
		var methods []string
		for _, method := range routedMethods {
			if router.Match(chi.NewRouteContext(), method, path) {
				methods = append(methods, method)
			}
		}
		return strings.Join(methods, ", ")
	}
}

func methodNotAllowed(allow func(string) string, hook http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := chi.RouteContext(r.Context()).RoutePath
		if path == "" {
			path = r.URL.Path
			if r.URL.RawPath != "" {
				path = r.URL.RawPath
			}
		}
		w.Header().Set("Allow", allow(path))
		hook.ServeHTTP(w, r)
	}
}

func register(r chi.Router, method, pattern string, handler http.Handler) {
	switch method {
	case http.MethodGet:
		r.Get(pattern, handler.ServeHTTP)
	case http.MethodPost:
		r.Post(pattern, handler.ServeHTTP)
	case http.MethodPut:
		r.Put(pattern, handler.ServeHTTP)
	case http.MethodDelete:
		r.Delete(pattern, handler.ServeHTTP)
	case http.MethodPatch:
		r.Patch(pattern, handler.ServeHTTP)
	default:
		r.Method(method, pattern, handler)
	}
}
