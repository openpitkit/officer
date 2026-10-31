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

package nonrelease

import (
	"net/http"

	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// Phase identifies a stage of the non-release database choice.
type Phase string

const (
	PhaseChoice    Phase = "choice"
	PhasePreparing Phase = "preparing"
	PhaseFailed    Phase = "failed"
	PhaseReady     Phase = "ready"
)

// Status reports build identity and database readiness. ReleaseDatabaseExists
// is meaningful during choice and failed; Error is present only on failure.
type Status struct {
	Release               bool   `json:"release"`
	Phase                 Phase  `json:"phase"`
	ReleaseDatabaseExists bool   `json:"releaseDatabaseExists"`
	Error                 string `json:"error,omitempty"`
}

// BuildRoute reports status without a session, including during setup.
// It belongs only in the panel route registry.
func BuildRoute(status func() Status) httpx.Route {
	return httpx.Route{
		ID: "build.get", Method: http.MethodGet, Pattern: "/build",
		Public: true, DuringSetup: true,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			httpx.WriteJSON(w, http.StatusOK, status())
		}),
	}
}

// DatabaseRoute accepts an explicit database choice without a session,
// including during setup. It belongs only in the gate's panel registry.
func DatabaseRoute(choose func(bool) (Status, error)) httpx.Route {
	return httpx.Route{
		ID: "build.database.post", Method: http.MethodPost, Pattern: "/build/database",
		Public: true, DuringSetup: true,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				FromRelease *bool `json:"fromRelease"`
			}
			if !httpx.DecodeBody(w, r, &req) {
				return
			}
			if req.FromRelease == nil {
				httpx.WriteValidationProblem(w, "fromRelease is required", "/fromRelease", "required")
				return
			}
			status, err := choose(*req.FromRelease)
			if err != nil {
				httpx.WriteErr(w, err)
				return
			}
			httpx.WriteJSON(w, http.StatusAccepted, status)
		}),
	}
}
