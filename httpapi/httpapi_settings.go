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

	"go.openpit.dev/officer/framework/backend"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

type userSettingsDTO struct {
	WelcomeSeen bool `json:"welcomeSeen"`
}

type userSettingsUpdateDTO struct {
	WelcomeSeen *bool `json:"welcomeSeen"`
}

// handleGetUserSettings handles GET /api/v1/user-settings, returning the current
// operator's UI preferences.
func handleGetUserSettings(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		seen, err := svc.WelcomeSeen(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, userSettingsDTO{WelcomeSeen: seen})
	}
}

// handleSetUserSettings handles PUT /api/v1/user-settings, persisting the
// operator's UI preferences and echoing the stored state.
func handleSetUserSettings(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req userSettingsUpdateDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if req.WelcomeSeen == nil {
			httpx.WriteValidationProblem(
				w, "welcomeSeen is required", "/welcomeSeen", "required",
			)
			return
		}
		if err := svc.SetWelcomeSeen(r.Context(), *req.WelcomeSeen); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, userSettingsDTO{WelcomeSeen: *req.WelcomeSeen})
	}
}

// --- market data ------------------------------------------------------------
