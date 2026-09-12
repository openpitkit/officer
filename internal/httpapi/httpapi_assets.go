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

	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func handleListAssets(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := assetListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteValidationErr(w, err)
			return
		}
		page, err := svc.ListAssetRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]assetDTO, 0, len(page.Rows))
		for _, asset := range page.Rows {
			dtos = append(dtos, toAssetDTO(asset))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"assets": dtos,
			"total":  page.Total,
		})
	}
}

// handleCreateAsset handles POST /api/v1/assets. The body carries the asset's
// public code, optional title, and optional classification.
func handleCreateAsset(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code       string `json:"code"`
			Title      string `json:"title"`
			AssetClass string `json:"assetClass"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		asset := domain.Asset{
			Code:       req.Code,
			Title:      req.Title,
			AssetClass: req.AssetClass,
		}
		created, err := svc.CreateAsset(r.Context(), asset)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"asset": toAssetDTO(created)})
	}
}

// handleUpdateAsset handles PUT /api/v1/assets/{code}. The path code identifies
// the asset; the body carries the replacement public code, title and
// classification. A code rename rebuilds the live engine and can report
// ErrEngineRestarting while another rebuild is in progress.
func handleUpdateAsset(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var req struct {
			Code       string `json:"code"`
			Title      string `json:"title"`
			AssetClass string `json:"assetClass"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		updated, err := svc.UpdateAsset(r.Context(), code, domain.Asset{
			Code:       req.Code,
			Title:      req.Title,
			AssetClass: req.AssetClass,
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"asset": toAssetDTO(updated)})
	}
}

// handleListAssetClasses handles GET /api/v1/asset-classes.
func handleListAssetClasses(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := assetClassListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteValidationErr(w, err)
			return
		}
		page, err := svc.ListAssetClassRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]assetClassDTO, 0, len(page.Rows))
		for _, row := range page.Rows {
			dtos = append(dtos, toAssetClassRowDTO(row))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"assetClasses": dtos,
			"total":        page.Total,
		})
	}
}

// handleCreateAssetClass handles POST /api/v1/asset-classes. The body carries
// the class public code, an optional title, and optional notes.
func handleCreateAssetClass(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code  string `json:"code"`
			Title string `json:"title"`
			Notes string `json:"notes"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		created, err := svc.CreateAssetClass(r.Context(), domain.AssetClass{
			Code:  req.Code,
			Title: req.Title,
			Notes: req.Notes,
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"assetClass": toAssetClassDTO(created)})
	}
}

// handleUpdateAssetClass handles PUT /api/v1/asset-classes/{code}. The path code
// identifies the class; the body carries the replacement public code, title and
// notes, so a class can be renamed. A rename cascades the asset link.
func handleUpdateAssetClass(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var req struct {
			Code  string `json:"code"`
			Title string `json:"title"`
			Notes string `json:"notes"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		updated, err := svc.UpdateAssetClass(r.Context(), code, domain.AssetClass{
			Code:  req.Code,
			Title: req.Title,
			Notes: req.Notes,
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"assetClass": toAssetClassDTO(updated)})
	}
}

// handleDeleteAssetClass handles DELETE /api/v1/asset-classes/{code}.
func handleDeleteAssetClass(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		if err := svc.DeleteAssetClass(r.Context(), code, forceQuery(r)); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleDeleteAsset handles DELETE /api/v1/assets/{code}.
func handleDeleteAsset(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, err := httpx.PathGroupCode(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		if err := svc.DeleteAsset(r.Context(), code, forceQuery(r)); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleListAccounts handles GET /api/v1/accounts.
