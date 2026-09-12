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
	"errors"
	"net/http"

	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// groupBlocks reads the realm's groups so an account list can report the
// effective block. The engine rejects every order from a member of a blocked
// group, while the account row carries only the account's own latched flag, so
// the group tier is joined in on every account read path. Only the list path
// needs the whole dictionary; accountBlock resolves a single account.
func groupBlocks(
	ctx context.Context, svc Service,
) (domain.GroupBlockIndex, error) {
	groups, err := svc.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	return domain.NewGroupBlockIndex(groups), nil
}

func handleListAccounts(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := accountListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteValidationErr(w, err)
			return
		}
		accounts, err := svc.ListAccountRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		blocks, err := groupBlocks(r.Context(), svc)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]accountDTO, 0, len(accounts.Rows))
		for _, row := range accounts.Rows {
			dtos = append(dtos, toAccountRowDTO(row, blocks.Resolve(row.Account)))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"accounts": dtos,
			"total":    accounts.Total,
		})
	}
}

// handleCreateAccount handles POST /api/v1/accounts. The body carries the
// account's public code and optional title; the engine assigns its internal id,
// which is never exposed.
func handleCreateAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code     string `json:"code"`
			Title    string `json:"title"`
			Currency string `json:"currency"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		account, err := svc.CreateAccount(r.Context(), domain.Account{
			Code:     domain.AccountID(req.Code),
			Title:    req.Title,
			Currency: req.Currency,
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeAccountBody(w, r, svc, account, http.StatusCreated)
	}
}

// handleGetAccount handles GET /api/v1/accounts/{id}.
func handleGetAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		account, limits, err := svc.GetAccountState(r.Context(), id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		block, err := accountBlock(r.Context(), svc, account)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"account": toAccountDTO(account, block),
			"limits":  toAccountLimitsDTO(limits),
		})
	}
}

// handleUpdateAccount handles PUT /api/v1/accounts/{id}. The body carries the
// replacement public code and title.
func handleUpdateAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var req struct {
			Code  string `json:"code"`
			Title string `json:"title"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		account, err := svc.UpdateAccount(r.Context(), id, domain.Account{
			Code:  domain.AccountID(req.Code),
			Title: req.Title,
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeAccountBody(w, r, svc, account, http.StatusOK)
	}
}

// handleBlockAccount handles
// POST /api/v1/accounts/{id}/block?missingAccount=create|reject.
func handleBlockAccount(svc Service) http.HandlerFunc {
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
		var req struct {
			Reason string `json:"reason"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if err := svc.BlockAccount(r.Context(), id, req.Reason, missing); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeAccount(w, svc, r, id)
	}
}

// handleUnblockAccount handles
// POST /api/v1/accounts/{id}/unblock?missingAccount=create|reject.
func handleUnblockAccount(svc Service) http.HandlerFunc {
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
		if err := svc.UnblockAccount(r.Context(), id, missing); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeAccount(w, svc, r, id)
	}
}

// handleDeleteAccount handles DELETE /api/v1/accounts/{id}.
func handleDeleteAccount(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		if err := svc.DeleteAccount(r.Context(), id, forceQuery(r)); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// writeAccount re-reads the account and writes it as {"account": {...}} with a
// 200. Account-mutating handlers (block/unblock, group, notes) use it so the
// response body is valid JSON reflecting the real persisted state, not an empty
// 200 the SPA would fail to parse. The limits returned alongside are ignored.
func writeAccount(w http.ResponseWriter, svc Service, r *http.Request, id domain.AccountID) {
	account, _, err := svc.GetAccountState(r.Context(), id)
	if err != nil {
		httpx.WriteErr(w, err)
		return
	}
	writeAccountBody(w, r, svc, account, http.StatusOK)
}

// writeAccountBody writes {"account": {...}} with status, joining the group
// tier of the block onto the account first.
func writeAccountBody(
	w http.ResponseWriter,
	r *http.Request,
	svc Service,
	account domain.Account,
	status int,
) {
	block, err := accountBlock(r.Context(), svc, account)
	if err != nil {
		httpx.WriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, status, map[string]any{"account": toAccountDTO(account, block)})
}

// accountBlock resolves one account's effective block. An account in no group
// needs no group read: nothing but its own flag can block it. One member reads
// its own group only - the whole dictionary is a list-path cost, not a
// single-account one.
func accountBlock(
	ctx context.Context, svc Service, account domain.Account,
) (domain.AccountBlockState, error) {
	if account.GroupCode == "" {
		return domain.ResolveAccountBlock(account, domain.AccountGroup{}), nil
	}
	group, _, err := svc.GetGroup(ctx, account.GroupCode)
	if err != nil {
		// A dangling group code contributes no block, as in GroupBlockIndex.
		if !errors.Is(err, domain.ErrNotFound) {
			return domain.AccountBlockState{}, err
		}
		group = domain.AccountGroup{}
	}
	return domain.ResolveAccountBlock(account, group), nil
}

// handleSetAccountGroup handles
// PUT /api/v1/accounts/{id}/group?missingAccount=create|reject. An empty group
// clears membership.
func handleSetAccountGroup(svc Service) http.HandlerFunc {
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
		var req struct {
			Group string `json:"group"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if err := svc.SetAccountGroup(r.Context(), id, req.Group, missing); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeAccount(w, svc, r, id)
	}
}

// handleSetAccountCurrency handles PUT /api/v1/accounts/{id}/currency.
func handleSetAccountCurrency(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var req struct {
			Currency string `json:"currency"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if err := svc.SetAccountCurrency(r.Context(), id, req.Currency); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeAccount(w, svc, r, id)
	}
}

// handleSetAccountNotes handles PUT /api/v1/accounts/{id}/notes.
func handleSetAccountNotes(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var req struct {
			Notes string `json:"notes"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if err := svc.SetAccountNotes(r.Context(), id, req.Notes); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		writeAccount(w, svc, r, id)
	}
}

// handleListLimits handles GET /api/v1/limits. It returns the three typed
// barrier tables flattened into one sorted, paged policy list. The optional
// account, policy (kind), sort/order, and limit/offset query params narrow and
// order the result; barriers reference accounts by code, never a surrogate id.
