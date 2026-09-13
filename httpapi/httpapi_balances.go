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
	"errors"
	"net/http"

	backend "go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func handleListBalances(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := balanceListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteValidationErr(w, err)
			return
		}
		balances, err := svc.ListBalanceRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]balanceDTO, 0, len(balances.Rows))
		for _, b := range balances.Rows {
			dtos = append(dtos, toBalanceDTO(b.Balance))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"balances": dtos,
			"total":    balances.Total,
		})
	}
}

// handleSetBalanceRealizedPnl handles
// PUT /api/v1/accounts/{id}/balances/realized-pnl?missingAccount=create|reject.
func handleSetBalanceRealizedPnl(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		missing, err := missingAccountQuery(r, id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		var req balanceRealizedPnlRequestDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		balance, err := svc.SetBalanceRealizedPnl(
			r.Context(),
			id,
			req.Asset,
			req.RealizedPnl,
			missing,
		)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"balance": toBalanceDTO(balance),
		})
	}
}

// handleApplyAdjustment handles
// POST /api/v1/accounts/{id}/adjustments?missingAccount=create|reject. A policy
// reject is a successful call: the rejected record is returned in the body.
func handleApplyAdjustment(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		missing, err := missingAccountQuery(r, id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		var req adjustmentRequestDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		// A caller-supplied external id is optional. When present, the backend
		// uses it verbatim and rejects a duplicate with 409. When absent the
		// backend generates one and returns it on the record.
		var externalID domain.ExternalID
		suppliedID := req.ID
		if suppliedID != "" {
			externalID, err = domain.ParseExternalID(suppliedID)
			if err != nil {
				httpx.WriteErr(w, err)
				return
			}
		}
		record, err := svc.ApplyAdjustment(
			r.Context(), id, externalID, fromAdjustmentRequestDTO(req), missing)
		if err != nil {
			if errors.Is(err, domain.ErrNoChange) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"adjustment": toAdjustmentDTO(record)})
	}
}

// handleListAccountAdjustments handles
// GET /api/v1/accounts/{id}/adjustments[?source=&limit=].
func handleListAccountAdjustments(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		n, err := httpx.LimitParam(r, listDefaultLimit, listCapREST)
		if err != nil {
			httpx.WriteValidationErr(w, err)
			return
		}
		recs, err := svc.ListAdjustments(r.Context(), id,
			domain.Source(r.URL.Query().Get("source")), n)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"adjustments": toAdjustmentDTOs(recs)})
	}
}

// handleListAdjustments handles GET /api/v1/adjustments.
func handleListAdjustments(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		filter, err := adjustmentListFilterFromQuery(q)
		if err != nil {
			httpx.WriteValidationErr(w, err)
			return
		}
		page, err := svc.ListAdjustmentRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"adjustments": toAdjustmentDTOs(page.Rows),
			"total":       page.Total,
		})
	}
}

func toAdjustmentDTOs(recs []domain.AccountAdjustmentRecord) []adjustmentDTO {
	dtos := make([]adjustmentDTO, 0, len(recs))
	for _, rec := range recs {
		dtos = append(dtos, toAdjustmentDTO(rec))
	}
	return dtos
}
