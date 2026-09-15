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

package main

import (
	"net/http"

	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

const (
	hostRouteID      = "customhost.host.get"
	hostRoutePattern = "/example/host"

	replacedBaseRouteID = "health.get"
	hiddenBaseRouteID   = "status.get"
	removedBaseRouteID  = "accounts.list.get"
)

func composeCustomHostRoutes(registry *httpx.RouteRegistry) {
	registry.Register(httpx.Route{
		ID:      hostRouteID,
		Method:  http.MethodGet,
		Pattern: hostRoutePattern,
		Handler: http.HandlerFunc(hostRoute),
	})
	registry.Register(httpx.Route{
		ID:      replacedBaseRouteID,
		Method:  http.MethodGet,
		Pattern: "/health",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			httpx.WriteJSON(w, http.StatusOK, map[string]string{"route": "replaced"})
		}),
	})
	registry.Unregister(removedBaseRouteID)
}

func hostRoute(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"example": "ok"})
}
