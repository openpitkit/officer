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
	"encoding/json"
	"net/http"

	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func handleListGroups(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := groupListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
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
func handleCreateGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code     string `json:"code"`
			Title    string `json:"title"`
			Currency string `json:"currency"`
			Notes    string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		group := domain.AccountGroup{
			Code:     req.Code,
			Title:    req.Title,
			Currency: req.Currency,
			Notes:    req.Notes,
		}
		if _, err := svc.CreateGroup(r.Context(), group); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeGroup(w, svc, r, req.Code, http.StatusCreated)
	}
}

// handleGetGroup handles GET /api/v1/groups/{code}.
func handleGetGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		group, accounts, err := svc.GetGroup(r.Context(), code)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]accountDTO, 0, len(accounts))
		for _, a := range accounts {
			dtos = append(dtos, toAccountDTO(a))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"group":    toGroupDTO(group),
			"accounts": dtos,
		})
	}
}

// handleUpdateGroup handles PUT /api/v1/groups/{code}. The body carries the
// replacement public code and title.
func handleUpdateGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Code  string `json:"code"`
			Title string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		group, err := svc.UpdateGroup(r.Context(), code, domain.AccountGroup{
			Code:  req.Code,
			Title: req.Title,
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"group": toGroupDTO(group)})
	}
}

// handleSetGroupCurrency handles PUT /api/v1/groups/{code}/currency.
func handleSetGroupCurrency(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Currency string `json:"currency"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetGroupCurrency(r.Context(), code, req.Currency); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeGroup(w, svc, r, code, http.StatusOK)
	}
}

// handleSetDefaultGroupCurrency handles PUT /api/v1/groups/-/default/currency.
func handleSetDefaultGroupCurrency(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Currency string `json:"currency"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
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
func handleSetGroupNotes(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Notes string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetGroupNotes(r.Context(), code, req.Notes); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeGroup(w, svc, r, code, http.StatusOK)
	}
}

// handleBlockGroup handles POST /api/v1/groups/{code}/block.
func handleBlockGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Reason string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetGroupBlocked(r.Context(), code, true, req.Reason); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeGroup(w, svc, r, code, http.StatusOK)
	}
}

// handleUnblockGroup handles POST /api/v1/groups/{code}/unblock.
func handleUnblockGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.SetGroupBlocked(r.Context(), code, false, ""); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeGroup(w, svc, r, code, http.StatusOK)
	}
}

// handleDeleteGroup handles DELETE /api/v1/groups/{code}.
func handleDeleteGroup(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.DeleteGroup(r.Context(), code); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// writeGroup re-reads the group and writes it as {"group": {...}} with status,
// so group-mutating handlers return valid JSON reflecting real server state.
// The member accounts returned alongside the group are ignored here.
func writeGroup(w http.ResponseWriter, svc Service, r *http.Request, code string, status int) {
	group, _, err := svc.GetGroup(r.Context(), code)
	if err != nil {
		httpx.WriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, status, map[string]any{"group": toGroupDTO(group)})
}

// --- spot funds -------------------------------------------------------------

// handleListBalances handles GET /api/v1/balances[?account=&asset=].
