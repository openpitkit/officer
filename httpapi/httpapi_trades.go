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

	backend "go.openpit.dev/officer/framework/backend"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func handleListTrades(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		filter, err := tradeListFilterFromQuery(q)
		if err != nil {
			httpx.WriteValidationErr(w, err)
			return
		}
		page, err := svc.ListTradeRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]tradeDTO, 0, len(page.Rows))
		for _, t := range page.Rows {
			dtos = append(dtos, toTradeDTO(t))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"trades": dtos,
			"total":  page.Total,
		})
	}
}

// --- signing keys -----------------------------------------------------------

// handleGenerateSigningKey handles POST /api/v1/signing/keys/generate. It
// generates a fresh Ed25519 keypair, makes it the sole active signing key, and
// returns it without private material.
