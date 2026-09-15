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
	"fmt"
	"net/http"

	"go.openpit.dev/officer/framework/domain"
)

// CallerResolver resolves the request identity without choosing its surface.
type CallerResolver func(*http.Request) (domain.Caller, error)

// callerContextKey is the unexported context key the caller is carried under. A
// package-private type keeps the key from colliding with other context values.
type callerContextKey struct{}

// SystemCaller is the attribution of an action the service takes on its own
// behalf: SourceSystem and no principal, so its audit and record rows store a
// NULL actor reference while the source marks the channel. Every internal entry
// point stamps it explicitly with ContextWithCaller; nothing infers it from an
// unstamped context.
func SystemCaller() domain.Caller {
	return domain.Caller{Source: domain.SourceSystem}
}

// ContextWithCaller returns a copy of ctx carrying caller. Each surface
// resolves the caller server-side and stamps it here; the source is never read
// from a client-supplied header or body. Once authentication lands, the
// resolved principal (and role) ride the same value.
func ContextWithCaller(ctx context.Context, caller domain.Caller) context.Context {
	return context.WithValue(ctx, callerContextKey{}, caller)
}

// CallerFromContext returns the caller stamped into ctx. An unstamped context
// is an error: control-plane mutations attribute their audit and record rows to
// the caller, and a surface that forgot to stamp one must fail rather than
// write rows under a guessed identity.
func CallerFromContext(ctx context.Context) (domain.Caller, error) {
	c, ok := ctx.Value(callerContextKey{}).(domain.Caller)
	if !ok {
		return domain.Caller{}, fmt.Errorf("auth: no caller in context: %w", domain.ErrInvalid)
	}
	return c, nil
}
