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

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// refuseReservedGroup answers a group operation aimed at the reserved default
// group and reports that it did. domain.ReservedGroupCode is the URL sentinel
// addressing it, as in PUT /groups/-/default/currency; the default group is
// stored under the empty code and is the terminal tier of the currency cascade
// every account resolves through, so it is not an operator-owned group and
// cannot be addressed as one. Every /groups/{code} operation refuses the
// sentinel, reads included: refusing it on only some routes would leave the
// rest reporting a validation error about a group id the client never sent.
// The request is well-formed, so the refusal is categorical (403) rather than
// a validation error.
func refuseReservedGroup(w http.ResponseWriter, code string) bool {
	if code != domain.ReservedGroupCode {
		return false
	}
	writeReservedGroupErr(w)
	return true
}

// writeReservedGroupErr writes the typed refusal used by both the path
// sentinel guard and the engine's own reserved-group error.
func writeReservedGroupErr(w http.ResponseWriter) {
	httpx.WriteErrMsg(
		w, http.StatusForbidden, "reserved_group",
		"the default account group is reserved: it is not an operator-owned "+
			"group and cannot be addressed as one",
	)
}

// refuseReservedGroupCode answers a request that would give a group the
// reserved sentinel as its literal code and reports that it did. The sentinel
// addresses the realm default group in every group route, so a group carrying
// it would be created but then unreachable. It is a schema-valid but forbidden
// value, hence a 422 validation problem rather than the categorical 403 the
// sentinel path itself returns.
func refuseReservedGroupCode(w http.ResponseWriter, code string) bool {
	if code != domain.ReservedGroupCode {
		return false
	}
	httpx.WriteValidationProblem(
		w,
		"group code "+domain.ReservedGroupCode+" is reserved for addressing the "+
			"realm default group",
		"/code",
		"reserved",
	)
	return true
}

// writeGroupErr maps a failed group mutation, reporting a reserved-group
// refusal under its own code instead of the generic forbidden envelope.
func writeGroupErr(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrReservedGroup) {
		writeReservedGroupErr(w)
		return
	}
	httpx.WriteErr(w, err)
}

func handleListGroups(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := groupListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteValidationErr(w, err)
			return
		}
		groups, err := svc.ListGroupRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]groupDTO, 0, len(groups.Rows))
		for _, row := range groups.Rows {
			dtos = append(dtos, toGroupRowDTO(row))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"groups": dtos,
			"total":  groups.Total,
		})
	}
}

// handleCreateGroup handles POST /api/v1/groups. The body carries the group's
// public code, an optional title, and optional notes; the engine assigns its
// internal group id, which is never exposed.
func handleCreateGroup(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code     string `json:"code"`
			Title    string `json:"title"`
			Currency string `json:"currency"`
			Notes    string `json:"notes"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if refuseReservedGroupCode(w, req.Code) {
			return
		}
		group := domain.AccountGroup{
			Code:     req.Code,
			Title:    req.Title,
			Currency: req.Currency,
			Notes:    req.Notes,
		}
		if _, err := svc.CreateGroup(r.Context(), group); err != nil {
			writeGroupErr(w, err)
			return
		}
		writeGroup(w, svc, r, req.Code, http.StatusCreated)
	}
}

// handleGetGroup handles GET /api/v1/groups/{code}.
func handleGetGroup(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		if refuseReservedGroup(w, code) {
			return
		}
		group, accounts, err := svc.GetGroup(r.Context(), code)
		if err != nil {
			writeGroupErr(w, err)
			return
		}
		dtos := make([]accountDTO, 0, len(accounts))
		for _, a := range accounts {
			// Every member resolves against this very group, so the block joins
			// without a second read.
			dtos = append(dtos, toAccountDTO(a, domain.ResolveAccountBlock(a, group)))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"group":    toGroupDTO(group),
			"accounts": dtos,
		})
	}
}

// handleUpdateGroup handles PUT /api/v1/groups/{code}. The body carries the
// replacement public code and title.
func handleUpdateGroup(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		if refuseReservedGroup(w, code) {
			return
		}
		var req struct {
			Code  string `json:"code"`
			Title string `json:"title"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if refuseReservedGroupCode(w, req.Code) {
			return
		}
		group, err := svc.UpdateGroup(r.Context(), code, domain.AccountGroup{
			Code:  req.Code,
			Title: req.Title,
		})
		if err != nil {
			writeGroupErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"group": toGroupDTO(group)})
	}
}

// handleSetGroupCurrency handles PUT /api/v1/groups/{code}/currency.
func handleSetGroupCurrency(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		if refuseReservedGroup(w, code) {
			return
		}
		var req struct {
			Currency string `json:"currency"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if err := svc.SetGroupCurrency(r.Context(), code, req.Currency); err != nil {
			writeGroupErr(w, err)
			return
		}
		writeGroup(w, svc, r, code, http.StatusOK)
	}
}

// handleSetDefaultGroupCurrency handles PUT /api/v1/groups/-/default/currency.
func handleSetDefaultGroupCurrency(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Currency string `json:"currency"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if err := svc.SetDefaultGroupCurrency(r.Context(), req.Currency); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"group": toGroupDTO(domain.AccountGroup{
				Code:     "",
				Currency: req.Currency,
			}),
		})
	}
}

// handleSetGroupNotes handles PUT /api/v1/groups/{code}/notes.
func handleSetGroupNotes(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		if refuseReservedGroup(w, code) {
			return
		}
		var req struct {
			Notes string `json:"notes"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if err := svc.SetGroupNotes(r.Context(), code, req.Notes); err != nil {
			writeGroupErr(w, err)
			return
		}
		writeGroup(w, svc, r, code, http.StatusOK)
	}
}

// handleBlockGroup handles POST /api/v1/groups/{code}/block.
func handleBlockGroup(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		if refuseReservedGroup(w, code) {
			return
		}
		var req struct {
			Reason string `json:"reason"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if err := svc.SetGroupBlocked(r.Context(), code, true, req.Reason); err != nil {
			writeGroupErr(w, err)
			return
		}
		writeGroup(w, svc, r, code, http.StatusOK)
	}
}

// handleUnblockGroup handles POST /api/v1/groups/{code}/unblock.
func handleUnblockGroup(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		if refuseReservedGroup(w, code) {
			return
		}
		if err := svc.SetGroupBlocked(r.Context(), code, false, ""); err != nil {
			writeGroupErr(w, err)
			return
		}
		writeGroup(w, svc, r, code, http.StatusOK)
	}
}

// handleDeleteGroup handles DELETE /api/v1/groups/{code}.
func handleDeleteGroup(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		if refuseReservedGroup(w, code) {
			return
		}
		if err := svc.DeleteGroup(r.Context(), code, forceQuery(r)); err != nil {
			writeGroupErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// writeGroup re-reads the group and writes it as {"group": {...}} with status,
// so group-mutating handlers return valid JSON reflecting real server state.
// The member accounts returned alongside the group are ignored here.
func writeGroup(w http.ResponseWriter, svc backend.ControlPlane, r *http.Request, code string, status int) {
	group, _, err := svc.GetGroup(r.Context(), code)
	if err != nil {
		writeGroupErr(w, err)
		return
	}
	httpx.WriteJSON(w, status, map[string]any{"group": toGroupDTO(group)})
}

// --- spot funds -------------------------------------------------------------

// handleListBalances handles GET /api/v1/balances[?account=&asset=].
