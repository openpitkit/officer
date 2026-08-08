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
	"net/http"

	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func handleListMcpAccess(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		commands, err := svc.ListMcpAccess(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]mcpCommandDTO, 0, len(commands))
		for _, c := range commands {
			dtos = append(dtos, toMcpCommandDTO(c))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"commands": dtos})
	}
}

// handleSetMcpAccess handles PUT /api/v1/mcp-access/{command}. The body carries
// the new enabled flag. An unknown command maps onto a 404 via the backend's
// domain.ErrNotFound. The confirmation-on-enable for protective commands is a UI
// concern handled elsewhere; the backend just persists.
func handleSetMcpAccess(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		command, err := httpx.PathCommand(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if req.Enabled == nil {
			httpx.WriteValidationProblem(w, "enabled is required", "/enabled", "required")
			return
		}
		if err := svc.SetMcpAccess(r.Context(), command, *req.Enabled); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		// Re-read the catalogue so the response reflects the persisted state and
		// the unchanged metadata of the toggled command.
		commands, err := svc.ListMcpAccess(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		for _, c := range commands {
			if c.Command.Name == command {
				httpx.WriteJSON(w, http.StatusOK, map[string]any{"command": toMcpCommandDTO(c)})
				return
			}
		}
		// The backend validated the command, so it must be present; treat its
		// absence as an internal inconsistency rather than a 404.
		httpx.WriteErrMsg(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// --- user settings ----------------------------------------------------------
