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
	"fmt"
	"net/http"
	"strings"

	"go.openpit.dev/officer/framework/auth"
)

// Access is what credentials a route requires once access modes are enforced.
// The route manifest publishes it for every mounted route; the router acts on
// it only by serving a public route without resolving a caller.
type Access string

const (
	// AccessPublic requires no credentials.
	AccessPublic Access = "public"
	// AccessAPI requires the REST surface credentials.
	AccessAPI Access = "api"
	// AccessAgent requires the agent surface credentials.
	AccessAgent Access = "agent"
	// AccessPanel requires the panel session.
	AccessPanel Access = "panel"
)

// AccessMode is the access state of the service. Within the framework only the
// route manifest reads it, from RouterConfig.AccessMode on every request, so a
// mode the application switches at runtime shows on the next request.
type AccessMode string

const (
	// AccessModeSetup is the state before first-run setup completes: the REST
	// and agent surfaces refuse every request, and the panel answers only its
	// DuringSetup routes.
	AccessModeSetup AccessMode = "setup"
	// AccessModeToken requires the API token on the REST surface, the agent
	// token on the agent surface and a password session on the panel.
	AccessModeToken AccessMode = "token"
	// AccessModeOpen requires no credentials; the browser guards still apply.
	AccessModeOpen AccessMode = "open"
)

const runtimeRouteManifestPath = "/route-manifest.json"

type runtimeRouteManifest struct {
	AccessMode AccessMode                  `json:"accessMode"`
	Routes     []runtimeRouteManifestEntry `json:"routes"`
}

// runtimeRouteManifestEntry is one mounted operation: its method, its full
// path and the access it requires.
type runtimeRouteManifestEntry struct {
	// routeID is the registry route the Authorizer filters the entry by; it is
	// empty on an entry every caller sees.
	routeID     string
	Method      string `json:"method"`
	Path        string `json:"path"`
	Access      Access `json:"access"`
	DuringSetup bool   `json:"duringSetup,omitempty"`
}

// mountedOperations lists, in manifest order, every operation NewRouter mounts:
// routes on both prefixes, panelRoutes, the MCP handler on mcpPath (empty when
// none is mounted), extraMounts and the SPA fallback. Like
// RouteRegistry.Register on an empty id, it panics on a public routes entry and
// on an extra mount without a valid access kind; it refuses two operations
// chi routes as one (see routingKey), a route id in both registries, and an
// extra mount or an MCP path under a v1 prefix (see v1PrefixOf).
func mountedOperations(
	routes, panelRoutes []Route,
	mcpPath string,
	extraMounts []ExtraMount,
) ([]runtimeRouteManifestEntry, error) {
	var operations []runtimeRouteManifestEntry
	routeIDs := make(map[string]bool, len(routes))
	for _, route := range routes {
		if route.Public {
			panic(fmt.Sprintf(
				"httpapi: route %q is public: public routes belong to PanelRoutes",
				route.ID,
			))
		}
		routeIDs[route.ID] = true
		operations = append(operations, runtimeRouteManifestEntry{
			routeID: route.ID,
			Method:  route.Method,
			Path:    apiPrefix + route.Pattern,
			Access:  AccessAPI,
		})
	}
	operations = append(operations, runtimeRouteManifestEntry{
		Method: http.MethodGet,
		Path:   apiPrefix + runtimeRouteManifestPath,
		Access: AccessAPI,
	})
	for _, route := range routes {
		operations = append(operations, runtimeRouteManifestEntry{
			routeID:     route.ID,
			Method:      route.Method,
			Path:        panelPrefix + route.Pattern,
			Access:      AccessPanel,
			DuringSetup: route.DuringSetup,
		})
	}
	for _, route := range panelRoutes {
		if routeIDs[route.ID] {
			return nil, fmt.Errorf(
				"httpapi: route id %q is in both Routes and PanelRoutes", route.ID,
			)
		}
		access, routeID := AccessPanel, route.ID
		if route.Public {
			// The router serves a public route without the Authorizer, so the
			// entry is listed for every caller.
			access, routeID = AccessPublic, ""
		}
		operations = append(operations, runtimeRouteManifestEntry{
			routeID:     routeID,
			Method:      route.Method,
			Path:        panelPrefix + route.Pattern,
			Access:      access,
			DuringSetup: route.DuringSetup,
		})
	}
	if mcpPath != "" {
		if prefix := v1PrefixOf(mcpPath); prefix != "" {
			return nil, fmt.Errorf("httpapi: MCP path %s is under %s", mcpPath, prefix)
		}
		for _, method := range mcpMethods {
			operations = append(operations, runtimeRouteManifestEntry{
				Method: method,
				Path:   mcpPath,
				Access: AccessAgent,
			})
		}
	}
	for _, mount := range extraMounts {
		if mount.Access != AccessPublic && mount.Access != AccessAgent {
			panic(fmt.Sprintf(
				"httpapi: extra mount %s %s has no valid access kind",
				mount.Method, mount.Pattern,
			))
		}
		if prefix := v1PrefixOf(mount.Pattern); prefix != "" {
			return nil, fmt.Errorf(
				"httpapi: extra mount %s %s is under %s",
				mount.Method, mount.Pattern, prefix,
			)
		}
		operations = append(operations, runtimeRouteManifestEntry{
			Method: mount.Method,
			Path:   mount.Pattern,
			Access: mount.Access,
		})
	}
	operations = append(operations, runtimeRouteManifestEntry{
		Method: http.MethodGet,
		Path:   "/*",
		Access: AccessPublic,
	})

	mounted := make(map[string]bool, len(operations))
	for _, operation := range operations {
		key := routingKey(operation.Method, operation.Path)
		if mounted[key] {
			return nil, fmt.Errorf(
				"httpapi: %s %s is mounted twice", operation.Method, operation.Path,
			)
		}
		mounted[key] = true
	}
	return operations, nil
}

// routingKey is the identity chi routes an operation by: the method
// upper-cased, as chi.Mux.Method does, and the path with every parameter name
// erased and every non-empty regexp anchored as chi anchors it, since chi
// matches a parameter by its anchored regexp and position, never by its name.
// "{id}" and "{code}" are one route, and so are "{id:[0-9]+}" and
// "{code:^[0-9]+$}"; "{id:[0-9]+}" and "{id:[a-z]+}" are two.
func routingKey(method, path string) string {
	key := strings.ToUpper(method) + " "
	for {
		start := strings.IndexByte(path, '{')
		if start < 0 {
			return key + path
		}
		// The parameter ends where chi ends it: at the brace that closes the
		// opening one, counting the braces of its regexp. An unclosed parameter
		// is keyed as written; chi panics on it when it is mounted.
		end, depth := -1, 0
		for i := start; i < len(path) && end < 0; i++ {
			switch path[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i
				}
			}
		}
		if end < 0 {
			return key + path
		}
		key += path[:start+1]
		if colon := strings.IndexByte(path[start:end], ':'); colon >= 0 {
			// chi anchors a non-empty regexp at each end it is not anchored at.
			expr := path[start+colon+1 : end]
			if expr != "" && expr[0] != '^' {
				expr = "^" + expr
			}
			if expr != "" && expr[len(expr)-1] != '$' {
				expr += "$"
			}
			key += ":" + expr
		}
		key += "}"
		path = path[end+1:]
	}
}

// v1PrefixOf returns the v1 prefix path is or lies under, or "" when it is
// under neither. A route on the parent tree there can win over the subrouter
// mounted on the prefix and answer v1 paths outside the v1 chain.
func v1PrefixOf(path string) string {
	for _, prefix := range []string{apiPrefix, panelPrefix} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return prefix
		}
	}
	return ""
}

// newRuntimeRouteManifestHandler serves the manifest of operations in the
// mode accessMode returns when the request arrives: each non-public registry
// route the Authorizer allows the caller, and every other operation, public
// panel routes included, as it is. A mode that is not an AccessMode constant
// answers an internal error.
func newRuntimeRouteManifestHandler(
	accessMode func() AccessMode,
	operations []runtimeRouteManifestEntry,
	authorizer Authorizer,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, err := auth.CallerFromContext(r.Context())
		if err != nil {
			WriteErr(w, err)
			return
		}
		mode := accessMode()
		switch mode {
		case AccessModeSetup, AccessModeToken, AccessModeOpen:
		default:
			WriteErr(w, fmt.Errorf("httpapi: invalid access mode %q", mode))
			return
		}
		entries := make([]runtimeRouteManifestEntry, 0, len(operations))
		for _, operation := range operations {
			if operation.routeID != "" {
				if err := authorizer.Authorize(
					r.Context(), caller, operation.routeID,
				); err != nil {
					continue
				}
			}
			entries = append(entries, operation)
		}
		WriteJSON(w, http.StatusOK, runtimeRouteManifest{
			AccessMode: mode,
			Routes:     entries,
		})
	})
}
