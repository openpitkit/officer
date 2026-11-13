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
	"go.openpit.dev/officer/framework/backend"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"net/http"
)

// RegisterAccountPnlResetRoutes adds only an explicitly installed capability.
// The standard shared route table remains usable by hosts without this feature.
func RegisterAccountPnlResetRoutes(reg *httpx.RouteRegistry, svc backend.ControlPlane, resets backend.AccountPnlResetControl) {
	register(reg, "accounts.realized-pnl.reset.post", http.MethodPost, "/accounts/{code}/realized-pnl/reset", func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		account, err := resets.ResetAccountPnl(r.Context(), code)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeAccountBody(w, r, svc, account, http.StatusOK)
	})
	register(reg, "account-pnl-reset-schedule.get", http.MethodGet, "/account-pnl-reset-schedule", func(w http.ResponseWriter, r *http.Request) {
		state, err := resets.AccountPnlResetSchedule(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, state)
	})
	register(reg, "account-pnl-reset-schedule.put", http.MethodPut, "/account-pnl-reset-schedule", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			backend.AccountPnlResetScheduleInput
			Enabled  *bool  `json:"enabled"`
			Revision *int64 `json:"revision"`
		}
		if !httpx.DecodeBody(w, r, &input) {
			return
		}
		if input.Enabled == nil || input.Revision == nil {
			httpx.WriteValidationProblem(w, "enabled and revision are required", "", "required")
			return
		}
		input.AccountPnlResetScheduleInput.Enabled = *input.Enabled
		input.AccountPnlResetScheduleInput.Revision = *input.Revision
		state, err := resets.PutAccountPnlResetSchedule(r.Context(), input.AccountPnlResetScheduleInput)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, state)
	})
	register(reg, "account-pnl-reset-schedule.recover.post", http.MethodPost, "/account-pnl-reset-schedule/recover", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Revision *int64 `json:"revision"`
			Action   string `json:"action"`
		}
		if !httpx.DecodeBody(w, r, &input) {
			return
		}
		if input.Revision == nil {
			httpx.WriteValidationProblem(w, "revision is required", "/revision", "required")
			return
		}
		state, err := resets.RecoverAccountPnlResetSchedule(r.Context(), *input.Revision, input.Action)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, state)
	})
}
