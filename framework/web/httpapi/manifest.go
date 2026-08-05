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

import "net/http"

const runtimeRouteManifestPath = "/route-manifest.json"

type runtimeRouteManifest struct {
	Routes []runtimeRouteManifestEntry `json:"routes"`
}

type runtimeRouteManifestEntry struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

func newRuntimeRouteManifestHandler(routes []Route) http.Handler {
	entries := make([]runtimeRouteManifestEntry, 0, len(routes))
	for _, route := range routes {
		entries = append(entries, runtimeRouteManifestEntry{
			Method: route.Method,
			Path:   route.Pattern,
		})
	}
	manifest := runtimeRouteManifest{Routes: entries}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, manifest)
	})
}
