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

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func handleListLimits(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := policyListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteValidationErr(w, err)
			return
		}
		page, err := svc.ListPolicyRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]policyDTO, 0, len(page.Rows))
		for _, row := range page.Rows {
			dtos = append(dtos, toPolicyRowDTO(row))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"policies": dtos,
			"total":    page.Total,
		})
	}
}

// handlePutRateLimit handles
// PUT /api/v1/limits/rate[?missingAccount=create|reject]. The body is the typed
// rate-limit barrier; the backend validates scope/axes and upserts it. The query
// parameter is required when the scope names an account.
func handlePutRateLimit(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req httpx.RateLimitDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		limit, err := httpx.FromRateLimitDTO(req)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		missing, err := missingAccountQuery(r, limit.Account)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		if err := svc.PutRateLimit(r.Context(), limit, missing); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		persisted, err := httpx.PersistedRateLimit(r.Context(), svc, limit)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		stored := httpx.ToRateLimitDTO(persisted)
		httpx.WriteJSON(w, http.StatusOK, httpx.PutLimitResultDTO{RateLimit: &stored})
	}
}

// handlePutOrderSizeLimit handles
// PUT /api/v1/limits/order-size[?missingAccount=create|reject]. The body is the
// typed order-size barrier; the backend validates scope/axes and upserts it. The
// query parameter is required when the scope names an account.
func handlePutOrderSizeLimit(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req httpx.OrderSizeLimitDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		limit := httpx.FromOrderSizeLimitDTO(req)
		missing, err := missingAccountQuery(r, limit.Account)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		if err := svc.PutOrderSizeLimit(r.Context(), limit, missing); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		persisted, err := httpx.PersistedOrderSizeLimit(r.Context(), svc, limit)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		stored := httpx.ToOrderSizeLimitDTO(persisted)
		httpx.WriteJSON(w, http.StatusOK, httpx.PutLimitResultDTO{OrderSizeLimit: &stored})
	}
}

// handlePutSpotFundsPnlBoundsLimit handles
// PUT /api/v1/limits/spot-funds-pnl-bounds[?missingAccount=create|reject]. The
// body is the typed SpotFunds self-computed P&L-bounds barrier; the backend
// validates scope/axes and upserts it. The query parameter is required when the
// scope names an account.
func handlePutSpotFundsPnlBoundsLimit(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req httpx.SpotFundsPnlBoundsLimitDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		limit := httpx.FromSpotFundsPnlBoundsLimitDTO(req)
		missing, err := missingAccountQuery(r, limit.Account)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		if err := svc.PutSpotFundsPnlBoundsLimit(
			r.Context(), limit, missing,
		); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		persisted, err := httpx.PersistedSpotFundsPnlBoundsLimit(
			r.Context(), svc, limit,
		)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		stored := httpx.ToSpotFundsPnlBoundsLimitDTO(persisted)
		httpx.WriteJSON(w, http.StatusOK, httpx.PutLimitResultDTO{
			SpotFundsPnlBoundsLimit: &stored,
		})
	}
}

// handleDeleteLimit handles
// DELETE /api/v1/limits?policy=&scope=&account=&asset=&accountGroup=.
// The barrier is addressed by its policy-specific composite.
func handleDeleteLimit(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		target := node.LimitTarget{
			Policy:       q.Get("policy"),
			Scope:        domain.LimitScope(q.Get("scope")),
			Account:      domain.AccountID(q.Get("account")),
			AccountGroup: q.Get("accountGroup"),
			Asset:        q.Get("asset"),
		}
		if err := svc.DeleteLimit(r.Context(), target); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleListAudit handles
// GET /api/v1/audit[?account=&asset=&source=&actions=&category=&limit=100].
// account, asset, and source narrow the trail. An explicit ?actions=a,b include
// list wins; otherwise ?category selects control, trading, or all actions.
