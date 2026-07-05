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

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
)

// Authorizer decides whether caller may use permission.
type Authorizer interface {
	Authorize(ctx context.Context, caller domain.Caller, permission string) error
}

// AllowAll authorizes every request.
type AllowAll struct{}

// Authorize allows every request.
func (AllowAll) Authorize(context.Context, domain.Caller, string) error { return nil }

// AuthorizeMiddleware gates a route through the configured Authorizer.
func AuthorizeMiddleware(a Authorizer, permission string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if a == nil {
				WriteErr(w, domain.ErrForbidden)
				return
			}
			caller := auth.CallerFromContext(r.Context())
			if err := a.Authorize(r.Context(), caller, permission); err != nil {
				WriteErr(w, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// StampSource stamps the request caller with the surface source.
func StampSource(source domain.Source) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller := domain.Caller{Source: source, Principal: domain.PrincipalOperator}
			ctx := auth.ContextWithCaller(r.Context(), caller)
			next.ServeHTTP(w, r.WithContext(ctx))
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
