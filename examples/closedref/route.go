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
	privateRouteID         = "closedref.private.get"
	privateRoutePermission = "closedref.private.read"
	privateRoutePattern    = "/example/private"

	replacedOpenRouteID = "health.get"
	hiddenOpenRouteID   = "status.get"
	removedOpenRouteID  = "accounts.list.get"
)

func composeReferenceRoutes(registry *httpx.RouteRegistry) {
	registry.Register(httpx.Route{
		ID:      privateRouteID,
		Method:  http.MethodGet,
		Pattern: privateRoutePattern,
		Handler: http.HandlerFunc(privateRoute),
	})
	registry.Register(httpx.Route{
		ID:      replacedOpenRouteID,
		Method:  http.MethodGet,
		Pattern: "/health",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			httpx.WriteJSON(w, http.StatusOK, map[string]string{"route": "replaced"})
		}),
	})
	hideRoute(registry, hiddenOpenRouteID, hiddenRoutePermission)
	registry.Unregister(removedOpenRouteID)
}

func hideRoute(registry *httpx.RouteRegistry, id string, permission string) {
	for _, route := range registry.Routes() {
		if route.ID != id {
			continue
		}
		route.Permission = permission
		registry.Register(route)
		return
	}
}

func privateRoute(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"example": "ok"})
}
