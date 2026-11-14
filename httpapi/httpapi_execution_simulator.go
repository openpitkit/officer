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
	"net/http"
	"strings"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/framework/web/httpapi/dto"
)

func handleGetTestExecutionPolicy(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		state, err := svc.GetTestExecutionPolicy(r.Context(), id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, dto.ToTestExecutionPolicyStateDTO(domain.ExternalID(strings.TrimSpace(id)), state))
	}
}

func handleReplaceTestExecutionPolicy(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var request dto.TestExecutionPolicyDTO
		if !httpx.DecodeBody(w, r, &request) {
			return
		}
		policy, err := dto.FromTestExecutionPolicyDTO(request)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		state, err := svc.ReplaceTestExecutionPolicy(r.Context(), id, policy)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, dto.ToTestExecutionPolicyStateDTO(domain.ExternalID(strings.TrimSpace(id)), state))
	}
}
