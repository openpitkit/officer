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
	"encoding/json"
	"fmt"
	"net/http"

	"go.openpit.dev/officer/framework/alerts"
	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// AlertRoutes serves the same service on public REST and the panel mirror.
// The router resolves callers and authorizes each non-public registered route.
func AlertRoutes(s *alerts.Service) []httpx.Route {
	return []httpx.Route{
		{ID: "alerts.list", Method: http.MethodGet, Pattern: "/alerts", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(r.URL.Query()) != 0 {
				httpx.WriteValidationErr(w, fmt.Errorf("alerts: query parameters are not supported"))
				return
			}
			out, err := s.List(r.Context())
			if err != nil {
				httpx.WriteErr(w, err)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, out)
		})},
		{ID: "alerts.detail", Method: http.MethodGet, Pattern: "/alerts/{id}", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(r.URL.Query()) != 0 {
				httpx.WriteValidationErr(w, fmt.Errorf("alerts: query parameters are not supported"))
				return
			}
			out, err := s.Detail(r.Context(), r.PathValue("id"))
			if err != nil {
				httpx.WriteErr(w, err)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, out)
		})},
		{ID: "alerts.read.put", Method: http.MethodPut, Pattern: "/alerts/{id}/read", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Read *bool `json:"read"`
			}
			if !httpx.DecodeBody(w, r, &in) {
				return
			}
			if in.Read == nil {
				httpx.WriteErr(w, fmt.Errorf("alerts: read is required: %w", domain.ErrInvalid))
				return
			}
			if err := s.SetRead(r.Context(), r.PathValue("id"), *in.Read); err != nil {
				httpx.WriteErr(w, err)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, map[string]bool{"read": *in.Read})
		})},
		{ID: "alerts.action.post", Method: http.MethodPost, Pattern: "/alerts/{id}/actions/{action}", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Input json.RawMessage `json:"input"`
			}
			if !httpx.DecodeBody(w, r, &in) {
				return
			}
			out, err := s.Resolve(r.Context(), r.PathValue("id"), r.PathValue("action"), in.Input)
			if err != nil {
				httpx.WriteErr(w, err)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, out)
		})},
	}
}
