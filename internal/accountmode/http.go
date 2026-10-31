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

package accountmode

import (
	"net/http"

	"go.openpit.dev/officer/framework/backend"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// Routes returns the account-mode v1 routes over cp, the running composition's
// control plane, which runs scope: GET /account-mode answers Read with
// State.Live and PUT /account-mode applies a Request through ApplyDuringSetup
// under gate. A nil scope is a composition whose host replaced the service
// factory and runs a scope of its own, which no stored choice switches: PUT
// applies a Request through Apply, so the choice waits for the next start even
// during setup, and State.Live stays false.
func Routes(
	cp backend.ControlPlane, scope *Scope, gate SetupGate,
) []httpx.Route {
	return []httpx.Route{
		{
			ID:          "account-mode.get",
			Method:      http.MethodGet,
			Pattern:     "/account-mode",
			Handler:     handleGetAccountMode(cp, scope, gate),
			DuringSetup: true,
		},
		{
			ID:          "account-mode.put",
			Method:      http.MethodPut,
			Pattern:     "/account-mode",
			Handler:     handleSetAccountMode(cp, scope, gate),
			DuringSetup: true,
		},
	}
}

// stateDTO is the wire shape of State.
type stateDTO struct {
	Mode            *string     `json:"mode"`
	Account         string      `json:"account"`
	Features        featuresDTO `json:"features"`
	RestartRequired bool        `json:"restartRequired"`
	Live            bool        `json:"live"`
}

// featuresDTO is the wire shape of backend.AccountFeatures.
type featuresDTO struct {
	Accounts bool   `json:"accounts"`
	Account  string `json:"account"`
}

func toStateDTO(state State) stateDTO {
	return stateDTO{
		Mode:    state.Mode,
		Account: state.Account,
		Features: featuresDTO{
			Accounts: state.Features.Accounts(),
			Account:  string(state.Features.Account),
		},
		RestartRequired: state.RestartRequired,
		Live:            state.Live,
	}
}

// handleGetAccountMode handles GET /api/v1/account-mode: the stored mode
// choice, what the running process enforces, and whether a choice stored now
// takes effect at once.
func handleGetAccountMode(
	cp backend.ControlPlane, scope *Scope, gate SetupGate,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state, err := Read(r.Context(), cp)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		if state.Live, err = live(scope, gate); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, toStateDTO(state))
	}
}

// handleSetAccountMode handles PUT /api/v1/account-mode: it stores the mode
// choice, for the next start once setup is complete, and answers the stored
// state.
func handleSetAccountMode(
	cp backend.ControlPlane, scope *Scope, gate SetupGate,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		var (
			state State
			err   error
		)
		if scope == nil {
			state, err = Apply(r.Context(), cp, req)
		} else {
			state, err = ApplyDuringSetup(r.Context(), cp, scope, gate, req)
		}
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, toStateDTO(state))
	}
}
