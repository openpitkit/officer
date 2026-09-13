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
	"context"
	"net/http"
	"time"

	backend "go.openpit.dev/officer/framework/backend"
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
		var req rateLimitDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		limit := domain.LimitRate{
			Scope:     req.Scope,
			Account:   domain.AccountID(req.Account),
			Asset:     req.Asset,
			Window:    time.Duration(req.WindowMs) * time.Millisecond,
			MaxOrders: req.MaxOrders,
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
		persisted, err := persistedRateLimit(r.Context(), svc, limit)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"rateLimit": toRateLimitDTO(persisted)})
	}
}

// handlePutOrderSizeLimit handles
// PUT /api/v1/limits/order-size[?missingAccount=create|reject]. The body is the
// typed order-size barrier; the backend validates scope/axes and upserts it. The
// query parameter is required when the scope names an account.
func handlePutOrderSizeLimit(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req orderSizeLimitDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		limit := domain.LimitOrderSize{
			Scope:       req.Scope,
			Account:     domain.AccountID(req.Account),
			Asset:       req.Asset,
			MaxQuantity: req.MaxQuantity,
			MaxNotional: req.MaxNotional,
		}
		missing, err := missingAccountQuery(r, limit.Account)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		if err := svc.PutOrderSizeLimit(r.Context(), limit, missing); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		persisted, err := persistedOrderSizeLimit(r.Context(), svc, limit)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"orderSizeLimit": toOrderSizeLimitDTO(persisted)})
	}
}

// handlePutSpotFundsPnlBoundsLimit handles
// PUT /api/v1/limits/spot-funds-pnl-bounds[?missingAccount=create|reject]. The
// body is the typed SpotFunds self-computed P&L-bounds barrier; the backend
// validates scope/axes and upserts it. The query parameter is required when the
// scope names an account.
func handlePutSpotFundsPnlBoundsLimit(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req spotFundsPnlBoundsLimitDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		limit := domain.LimitSpotFundsPnlBounds{
			Scope:        req.Scope,
			Account:      domain.AccountID(req.Account),
			AccountGroup: req.AccountGroup,
			Currency:     req.Currency,
			LowerBound:   req.LowerBound,
			UpperBound:   req.UpperBound,
		}
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
		persisted, err := persistedSpotFundsPnlBoundsLimit(
			r.Context(), svc, limit,
		)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"spotFundsPnlBoundsLimit": toSpotFundsPnlBoundsLimitDTO(persisted),
		})
	}
}

func persistedRateLimit(
	ctx context.Context, svc backend.ControlPlane, target domain.LimitRate,
) (domain.LimitRate, error) {
	limits, err := svc.ListLimits(ctx, target.Account)
	if err != nil {
		return domain.LimitRate{}, err
	}
	for _, limit := range limits.RateLimits {
		if sameLimitAddress(limit.Scope, limit.Account, limit.Asset,
			target.Scope, target.Account, target.Asset) {
			return limit, nil
		}
	}
	return domain.LimitRate{}, domain.ErrNotFound
}

func persistedOrderSizeLimit(
	ctx context.Context, svc backend.ControlPlane, target domain.LimitOrderSize,
) (domain.LimitOrderSize, error) {
	limits, err := svc.ListLimits(ctx, target.Account)
	if err != nil {
		return domain.LimitOrderSize{}, err
	}
	for _, limit := range limits.OrderSizeLimits {
		if sameLimitAddress(limit.Scope, limit.Account, limit.Asset,
			target.Scope, target.Account, target.Asset) {
			return limit, nil
		}
	}
	return domain.LimitOrderSize{}, domain.ErrNotFound
}

func persistedSpotFundsPnlBoundsLimit(
	ctx context.Context, svc backend.ControlPlane, target domain.LimitSpotFundsPnlBounds,
) (domain.LimitSpotFundsPnlBounds, error) {
	limits, err := svc.ListLimits(ctx, target.Account)
	if err != nil {
		return domain.LimitSpotFundsPnlBounds{}, err
	}
	for _, limit := range limits.SpotFundsPnlBoundsLimits {
		if sameSpotFundsPnlBoundsAddress(limit, target) {
			return limit, nil
		}
	}
	return domain.LimitSpotFundsPnlBounds{}, domain.ErrNotFound
}

func sameLimitAddress(
	leftScope string, leftAccount domain.AccountID, leftAsset string,
	rightScope string, rightAccount domain.AccountID, rightAsset string,
) bool {
	return leftScope == rightScope && leftAccount == rightAccount && leftAsset == rightAsset
}

func sameSpotFundsPnlBoundsAddress(
	left domain.LimitSpotFundsPnlBounds,
	right domain.LimitSpotFundsPnlBounds,
) bool {
	return left.Scope == right.Scope &&
		left.Account == right.Account &&
		left.AccountGroup == right.AccountGroup
}

// handleDeleteLimit handles
// DELETE /api/v1/limits?policy=&scope=&account=&asset=&accountGroup=.
// The barrier is addressed by its policy-specific composite.
func handleDeleteLimit(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		target := node.LimitTarget{
			Policy:       q.Get("policy"),
			Scope:        q.Get("scope"),
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
