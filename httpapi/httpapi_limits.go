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

// handleGetSpotFundsLimitMode handles GET /api/v1/limits/spot-funds-limit-mode.
func handleGetSpotFundsLimitMode(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state, err := svc.SpotFundsLimitMode(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, httpx.ToSpotFundsLimitModeDTO(state))
	}
}

// handlePutSpotFundsLimitMode handles PUT /api/v1/limits/spot-funds-limit-mode.
// The body is the Spot Funds limit-mode meta-policy, global only; the backend
// validates and sets it. It has no account axis, so there is no missingAccount
// query parameter.
func handlePutSpotFundsLimitMode(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req httpx.SpotFundsLimitModeLimitDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		limit := httpx.FromSpotFundsLimitModeLimitDTO(req)
		persisted, err := svc.PutSpotFundsLimitMode(r.Context(), limit)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		stored := httpx.ToSpotFundsLimitModeLimitDTO(persisted)
		httpx.WriteJSON(w, http.StatusOK, httpx.PutLimitResultDTO{
			SpotFundsLimitMode: &stored,
		})
	}
}

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

// handlePutRateLimit serves POST and PUT on /api/v1/limits/rate. The route
// supplies the write intent and success status. POST adds a barrier and returns
// 201, refusing an existing key. PUT creates or replaces a barrier and returns
// 200. The missingAccount query parameter is required when the scope names an
// account.
func handlePutRateLimit(svc backend.ControlPlane, write domain.LimitWrite, status int) http.HandlerFunc {
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
		persisted, err := svc.PutRateLimit(r.Context(), limit, write, missing)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		stored := httpx.ToRateLimitDTO(persisted)
		httpx.WriteJSON(w, status, httpx.PutLimitResultDTO{RateLimit: &stored})
	}
}

// handlePutOrderSizeLimit serves POST and PUT on /api/v1/limits/order-size.
// The route supplies the write intent and success status. POST adds a barrier
// and returns 201, refusing an existing (scope, account, asset, mode) key. PUT
// creates or replaces that key and returns 200. An omitted mode means reject;
// changing mode requires a delete and an add. The missingAccount query
// parameter is required when the scope names an account.
func handlePutOrderSizeLimit(svc backend.ControlPlane, write domain.LimitWrite, status int) http.HandlerFunc {
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
		persisted, err := svc.PutOrderSizeLimit(r.Context(), limit, write, missing)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		stored := httpx.ToOrderSizeLimitDTO(persisted)
		httpx.WriteJSON(w, status, httpx.PutLimitResultDTO{OrderSizeLimit: &stored})
	}
}

// handlePutPositionLimit serves POST and PUT on /api/v1/limits/position. The
// route supplies the write intent and success status. POST adds a barrier and
// returns 201, refusing an existing key. PUT creates or replaces a barrier and
// returns 200. The missingAccount query parameter is required when the scope
// names an account.
func handlePutPositionLimit(svc backend.ControlPlane, write domain.LimitWrite, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req httpx.PositionLimitDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		limit := httpx.FromPositionLimitDTO(req)
		missing, err := missingAccountQuery(r, limit.Account)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		persisted, err := svc.PutPositionLimit(r.Context(), limit, write, missing)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		stored := httpx.ToPositionLimitDTO(persisted)
		httpx.WriteJSON(w, status, httpx.PutLimitResultDTO{PositionLimit: &stored})
	}
}

// handlePutSpotFundsPnlBoundsLimit serves POST and PUT on
// /api/v1/limits/spot-funds-pnl-bounds,
// /api/v1/limits/spot-funds-unrealized-pnl-bounds and
// /api/v1/limits/spot-funds-total-pnl-bounds. The route supplies the P&L axis,
// write intent and success status. POST adds a barrier and returns 201, refusing
// an existing key. PUT creates or replaces a barrier and returns 200. The
// missingAccount query parameter is required when the scope names an account.
func handlePutSpotFundsPnlBoundsLimit(
	svc backend.ControlPlane, axis domain.SpotFundsPnlAxis, write domain.LimitWrite, status int,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req httpx.SpotFundsPnlBoundsLimitDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		limit := httpx.FromSpotFundsPnlBoundsLimitDTO(axis, req)
		missing, err := missingAccountQuery(r, limit.Account)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		persisted, err := svc.PutSpotFundsPnlBoundsLimit(
			r.Context(), limit, write, missing,
		)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		result, err := httpx.ToPutSpotFundsPnlBoundsLimitResultDTO(persisted)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, status, result)
	}
}

// handleDeleteLimit handles
// DELETE /api/v1/limits?policy=&scope=&account=&asset=&accountGroup=&mode=.
// The barrier is addressed by its policy-specific composite. For policies
// carrying a rule mode (domain.LimitPolicyHasMode), an omitted or empty mode
// addresses the reject barrier.
func handleDeleteLimit(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		target := node.LimitTarget{
			Policy:       q.Get("policy"),
			Mode:         domain.RuleMode(q.Get("mode")),
			Scope:        domain.LimitScope(q.Get("scope")),
			Account:      domain.AccountID(q.Get("account")),
			AccountGroup: q.Get("accountGroup"),
			Asset:        q.Get("asset"),
		}
		if domain.LimitPolicyHasMode(target.Policy) {
			target.Mode = httpx.RuleModeFromWire(q.Get("mode"))
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
