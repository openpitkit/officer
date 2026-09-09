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
	"errors"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.openpit.dev/officer/framework/domain"
)

// ExtraMount is a non-v1 route the host application mounts before the SPA
// fallback.
type ExtraMount struct {
	Method  string
	Pattern string
	Handler http.Handler
}

// LogSource is a source of recent service log lines.
type LogSource interface {
	Snapshot() []string
}

// RouterConfig configures NewRouter.
type RouterConfig struct {
	Routes      *RouteRegistry
	Authorizer  Authorizer
	SPA         fs.FS
	MCP         http.Handler
	MCPPath     string
	Logs        LogSource
	BodyLimit   func(*http.Request) int64
	ExtraMounts []ExtraMount
}

// NewRouter builds the framework HTTP handler.
func NewRouter(cfg RouterConfig) (http.Handler, error) {
	if cfg.Routes == nil {
		return nil, errors.New("httpapi: nil route registry")
	}
	if cfg.Authorizer == nil {
		return nil, errors.New("httpapi: nil authorizer")
	}
	if cfg.SPA == nil {
		return nil, errors.New("httpapi: nil SPA filesystem")
	}
	if cfg.BodyLimit == nil {
		return nil, errors.New("httpapi: nil body limit policy")
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)

	routes := cfg.Routes.Routes()
	mountV1(
		router,
		"/api/v1",
		routes,
		cfg.Authorizer,
		domain.SourceAPI,
		cfg.BodyLimit,
		newRuntimeRouteManifestHandler(routes, cfg.Authorizer),
	)
	mountV1(
		router,
		"/app/api/v1",
		routes,
		cfg.Authorizer,
		domain.SourcePanel,
		cfg.BodyLimit,
		nil,
	)

	if cfg.MCP != nil {
		mcpPath := cfg.MCPPath
		if mcpPath == "" {
			mcpPath = "/mcp"
		}
		router.Mount(mcpPath, cfg.MCP)
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
	source domain.Source,
	bodyLimit func(*http.Request) int64,
	manifest http.Handler,
) {
	router.Route(prefix, func(v1 chi.Router) {
		base := Chain{LimitBody(bodyLimit), StampSource(source)}
		if manifest != nil {
			register(
				v1,
				http.MethodGet,
				runtimeRouteManifestPath,
				base.Then(manifest),
			)
		}
		for _, route := range routes {
			chain := make(Chain, 0, len(base)+1)
			chain = append(chain, base...)
			chain = append(chain, AuthorizeMiddleware(authorizer, route.Permission))
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
