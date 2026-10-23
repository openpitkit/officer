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
	"net/http"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
)

// Authorizer decides whether caller may use a stable surface identifier.
// Caller.Source identifies the identifier namespace: SourceMCP uses MCP tool
// names, while SourcePanel and SourceAPI use REST route IDs.
// Route.DuringSetup reaches an Authorizer through DuringSetupFromContext.
type Authorizer interface {
	Authorize(ctx context.Context, caller domain.Caller, identifier string) error
}

// AllowAll authorizes every request.
type AllowAll struct{}

// Authorize allows every request.
func (AllowAll) Authorize(context.Context, domain.Caller, string) error { return nil }

// AuthorizeMiddleware gates a route through the configured Authorizer.
func AuthorizeMiddleware(a Authorizer, identifier string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if a == nil {
				WriteErr(w, domain.ErrForbidden)
				return
			}
			caller, err := auth.CallerFromContext(r.Context())
			if err != nil {
				WriteErr(w, err)
				return
			}
			if err := a.Authorize(r.Context(), caller, identifier); err != nil {
				WriteErr(w, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ResolveCaller resolves the request identity and stamps the server-owned
// source: into the request context as its surface before it calls resolver,
// which reads it with SurfaceFromContext, and as the Source of the caller it
// stores for the handler.
func ResolveCaller(resolver auth.CallerResolver, source domain.Source) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(ContextWithSurface(r.Context(), source))
			caller, err := resolver(r)
			if err != nil {
				WriteErr(w, err)
				return
			}
			caller.Source = source
			ctx := auth.ContextWithCaller(r.Context(), caller)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// surfaceContextKey is the unexported context key the request surface is
// carried under.
type surfaceContextKey struct{}

// ContextWithSurface returns a copy of ctx carrying surface, the surface a
// request arrived on. The router stamps it from the mount that serves the
// request, never from the request itself, before any caller is resolved, so a
// CallerResolver reads it to admit only the credentials of that surface.
func ContextWithSurface(ctx context.Context, surface domain.Source) context.Context {
	return context.WithValue(ctx, surfaceContextKey{}, surface)
}

// SurfaceFromContext returns the surface stamped into ctx, and false when none
// is.
func SurfaceFromContext(ctx context.Context) (domain.Source, bool) {
	surface, ok := ctx.Value(surfaceContextKey{}).(domain.Source)
	return surface, ok
}

// withSurface stamps surface into the request context of every request.
func withSurface(surface domain.Source) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(ContextWithSurface(r.Context(), surface)))
		})
	}
}

// duringSetupContextKey is the unexported context key the route's setup flag
// is carried under.
type duringSetupContextKey struct{}

// contextWithDuringSetup returns a copy of ctx carrying duringSetup. It stays
// unexported so only the router stamps the flag, from the route table; no code
// outside this package can mark a route as available during setup.
func contextWithDuringSetup(ctx context.Context, duringSetup bool) context.Context {
	return context.WithValue(ctx, duringSetupContextKey{}, duringSetup)
}

// DuringSetupFromContext returns the route's Route.DuringSetup flag, stamped
// on non-public routes of both v1 mounts before the caller resolver, so both
// the resolver and an Authorizer see it. It marks a panel mirror available
// before first-run setup completes; an Authorizer combines it with
// caller.Source, so the flag alone never opens the REST API. An absent stamp
// returns false for public routes, MCP tool calls and the route manifest's
// per-entry checks.
func DuringSetupFromContext(ctx context.Context) bool {
	duringSetup, _ := ctx.Value(duringSetupContextKey{}).(bool)
	return duringSetup
}

// withDuringSetup stamps Route.DuringSetup before the caller resolver and
// authorization on non-public routes on both REST mounts. The resolver and
// Authorizer enforce setup access; this middleware only carries the flag.
// MCP and the route manifest do not stamp it.
func withDuringSetup(duringSetup bool) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(contextWithDuringSetup(r.Context(), duringSetup)))
		})
	}
}

// LimitBody caps request bodies using the caller-provided policy.
func LimitBody(policy func(*http.Request) int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, policy(r))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimitPolicy returns defaultLimit except for exact-path overrides.
func BodyLimitPolicy(
	defaultLimit int64,
	overrides map[string]int64,
) func(*http.Request) int64 {
	return func(r *http.Request) int64 {
		if limit, ok := overrides[r.URL.Path]; ok {
			return limit
		}
		return defaultLimit
	}
}
