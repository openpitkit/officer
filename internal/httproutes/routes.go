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

// Package httproutes composes the open app's HTTP route registry.
package httproutes

import (
	"net/http"

	"go.openpit.dev/officer/framework/backend"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/internal/httpapi"
)

// Config is the open app HTTP composition output.
type Config struct {
	Routes      *httpx.RouteRegistry
	Authorizer  httpx.Authorizer
	BodyLimit   func(*http.Request) int64
	ExtraMounts []httpx.ExtraMount
}

// Build returns the open app's route registry and framework router options.
func Build(svc backend.ControlPlane, logs httpx.LogSource) Config {
	return Config{
		Routes:      httpapi.NewRouteRegistry(svc, logs),
		Authorizer:  httpx.AllowAll{},
		BodyLimit:   httpapi.BodyLimitPolicy(),
		ExtraMounts: httpapi.ExtraMounts(),
	}
}
