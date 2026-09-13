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

// Package auth carries the request caller through the context. It is the
// authorization-ready seam: each surface resolves the caller server-side and
// stamps it here, and the control plane reads it back to attribute mutations.
// The source is never taken from a client-supplied header or body.
//
// The seam lives in its own package so every surface (httpapi, mcp) and the
// control plane (backend) can share it without importing one another.
package auth

import (
	"context"
	"net/http"

	"go.openpit.dev/officer/framework/domain"
)

// CallerResolver resolves the request identity without choosing its surface.
type CallerResolver func(*http.Request) (domain.Caller, error)

// callerContextKey is the unexported context key the caller is carried under. A
// package-private type keeps the key from colliding with other context values.
type callerContextKey struct{}

// systemCaller is the attribution used when no caller was stamped: a startup or
// internal system action. Surfaces stamp their own caller (see
// ContextWithCaller); an unstamped context is therefore system-initiated. It
// carries SourceSystem and NO principal: a system action has no actor, so its
// audit/record rows store a NULL actor reference (Source=system marks the
// channel). A literal "system" principal would reference a non-existent
// dictionary row and be rejected by the strict actor resolution.
var systemCaller = domain.Caller{Source: domain.SourceSystem}

// ContextWithCaller returns a copy of ctx carrying caller. Each surface
// resolves the caller server-side and stamps it here; the source is never read
// from a client-supplied header or body. Once authentication lands, the
// resolved principal (and role) ride the same value.
func ContextWithCaller(ctx context.Context, caller domain.Caller) context.Context {
	return context.WithValue(ctx, callerContextKey{}, caller)
}

// LookupCaller returns the caller stamped into ctx and whether one was stamped.
// Unlike CallerFromContext it does not default an unstamped ctx to the system.
func LookupCaller(ctx context.Context) (domain.Caller, bool) {
	c, ok := ctx.Value(callerContextKey{}).(domain.Caller)
	return c, ok
}

// CallerFromContext returns the caller stamped into ctx, or the system caller
// when none was set. Control-plane mutations attribute their audit/record rows
// to this caller, so an unstamped context is safely attributed to the system.
func CallerFromContext(ctx context.Context) domain.Caller {
	if c, ok := ctx.Value(callerContextKey{}).(domain.Caller); ok {
		return c
	}
	return systemCaller
}
