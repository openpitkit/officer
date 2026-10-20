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

// ExtraMount is a non-v1 route the host application mounts before the SPA
// fallback; NewRouter refuses one on /api/v1, /app/api/v1 or under either.
type ExtraMount struct {
	Method  string
	Pattern string
	Handler http.Handler
	// Access is what the mount requires. Extra mounts bypass ResolveCaller and
	// Authorize, so each declares it; only AccessPublic and AccessAgent are
	// valid, and NewRouter panics on any other.
	Access Access
}

// LogSource is a source of recent service log lines.
type LogSource interface {
	Snapshot() []string
}

// RouterConfig configures NewRouter.
type RouterConfig struct {
	Routes *RouteRegistry
	// PanelRoutes are routes served only on /app/api/v1, after the Routes
	// mirror and through the same chain; nil means none.
	PanelRoutes    *RouteRegistry
	Authorizer     Authorizer
	CallerResolver auth.CallerResolver
	SPA            fs.FS
	MCP            http.Handler
	MCPPath        string
	Logs           LogSource
	BodyLimit      func(*http.Request) int64
	ExtraMounts    []ExtraMount
	Guards         BrowserGuards
	// AccessMode reports the access state of the service. The router calls it
	// on every route manifest request, concurrently from request goroutines,
	// so it must be safe for concurrent use. A value that is not an AccessMode
	// constant answers an internal error; NewRouter refuses a nil one.
	AccessMode func() AccessMode
}

// NewRouter builds the framework HTTP handler. It panics on a public Routes
// route and on an extra mount without a valid access kind, and refuses an
// operation mounted twice, a route id in both Routes and PanelRoutes, and an
// extra mount or MCP path under a v1 prefix. Behind Guards, the MCP mount
// refuses, with domain.ErrOriginNotAllowed, a request a browser sent from
// another origin on every method, GET included. Every v1 route and the MCP
// mount carry their surface in the request context for SurfaceFromContext:
// domain.SourceAPI on /api/v1, domain.SourcePanel on /app/api/v1 and
// domain.SourceMCP on the MCP path.
func NewRouter(cfg RouterConfig) (http.Handler, error) {
	if cfg.Routes == nil {
		return nil, errors.New("httpapi: nil route registry")
	}
	if cfg.Authorizer == nil {
		return nil, errors.New("httpapi: nil authorizer")
	}
	if cfg.CallerResolver == nil {
		return nil, errors.New("httpapi: nil caller resolver")
	}
	if cfg.SPA == nil {
		return nil, errors.New("httpapi: nil SPA filesystem")
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
	if cfg.AccessMode == nil {
		return nil, errors.New("httpapi: nil access mode")
	}

	routes := cfg.Routes.Routes()
	panelRoutes := cfg.PanelRoutes.Routes()
	mcpPath := ""
	if cfg.MCP != nil {
		mcpPath = cfg.MCPPath
		if mcpPath == "" {
			mcpPath = "/mcp"
		}
	}
	operations, err := mountedOperations(
		routes, panelRoutes, mcpPath, cfg.ExtraMounts,
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

	mountV1(
		router,
		apiPrefix,
		routes,
		cfg.Authorizer,
		cfg.CallerResolver,
		domain.SourceAPI,
		cfg.BodyLimit,
		newRuntimeRouteManifestHandler(
			cfg.AccessMode, operations, cfg.Authorizer,
		),
	)
	mountV1(
		router,
		panelPrefix,
		slices.Concat(routes, panelRoutes),
		cfg.Authorizer,
		cfg.CallerResolver,
		domain.SourcePanel,
		cfg.BodyLimit,
		nil,
	)

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

	spa, err := newSPAHandler(cfg.SPA)
	if err != nil {
		return nil, err
	}
	router.NotFound(spa.ServeHTTP)

	return router, nil
}

func mountV1(
	router chi.Router,
	prefix string,
	routes []Route,
	authorizer Authorizer,
	callerResolver auth.CallerResolver,
	source domain.Source,
	bodyLimit func(*http.Request) int64,
	manifest http.Handler,
) {
	router.Route(prefix, func(v1 chi.Router) {
		limit := LimitBody(bodyLimit)
		base := Chain{limit, ResolveCaller(callerResolver, source)}
		// A public route carries no credentials: nothing resolves a caller for
		// it or authorizes it, so only the surface is stamped.
		public := Chain{limit, withSurface(source)}
		if manifest != nil {
			register(
				v1,
				http.MethodGet,
				runtimeRouteManifestPath,
				base.Then(manifest),
			)
		}
		for _, route := range routes {
			if route.Public {
				register(v1, route.Method, route.Pattern, public.Then(route.Handler))
				continue
			}
			chain := make(Chain, 0, len(base)+1)
			chain = append(chain, base...)
			chain = append(chain, AuthorizeMiddleware(authorizer, route.ID))
			register(v1, route.Method, route.Pattern, chain.Then(route.Handler))
		}
	})
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
