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
	"strings"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func handleListMarketData(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"marketData": toMarketDataDTO(status),
		})
	}
}

// handleRestartMarketData handles POST /api/v1/market-data/restart. It
// re-applies the market-data configuration by restarting the connector manager,
// then returns the refreshed snapshot in the same envelope as the other
// market-data mutations.
func handleRestartMarketData(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := svc.RestartMarketData(r.Context()); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"marketData": toMarketDataDTO(status),
		})
	}
}

func handleCreateMarketDataInstance(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req marketDataCreateInstanceRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		instance := domain.MarketDataInstance{
			Provider:    req.Provider,
			Label:       req.Label,
			Credentials: req.Credentials,
			Enabled:     req.Enabled,
		}
		// A caller-supplied external id is optional. When present, the backend
		// uses it verbatim and rejects a duplicate with 409. When absent the
		// backend generates one and returns it on the instance.
		suppliedID := req.ID
		if suppliedID == "" {
			suppliedID = req.ExternalID
		}
		if suppliedID != "" {
			id, err := domain.ParseExternalID(suppliedID)
			if err != nil {
				httpx.WriteErr(w, err)
				return
			}
			instance.ExternalID = id
		}
		created, err := svc.CreateMarketDataInstance(r.Context(), instance)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{
			"instance": toMarketDataInstanceDTO(backend.MarketDataInstanceStatus{
				Instance: created,
			}),
		})
	}
}

func handleUpdateMarketDataInstanceSettings(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req marketDataUpdateInstanceSettingsRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.UpdateMarketDataInstanceSettings(
			r.Context(), id, req.Label, req.Credentials,
		); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"marketData": toMarketDataDTO(status),
		})
	}
}

func handleSetMarketDataInstanceEnabled(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		instanceID, err := domain.ParseExternalID(id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetMarketDataInstanceEnabled(r.Context(), id, req.Enabled); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		for _, instance := range status.Instances {
			if instance.Instance.ExternalID == instanceID {
				httpx.WriteJSON(w, http.StatusOK, map[string]any{
					"enabled": instance.Instance.Enabled,
				})
				return
			}
		}
		httpx.WriteErr(w, domain.ErrNotFound)
	}
}

func handleDeleteMarketDataInstance(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		if err := svc.DeleteMarketDataInstance(r.Context(), id, forceQuery(r)); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func forceQuery(r *http.Request) bool {
	return r.URL.Query().Get("force") == "true"
}

func handleUpsertMarketDataInstrument(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req marketDataInstrumentDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		instanceID, err := domain.ParseExternalID(id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		instrument := domain.MarketDataInstrument{
			Instance:       instanceID,
			ExternalSymbol: req.ExternalSymbol,
			BaseAsset:      req.BaseAsset,
			QuoteAsset:     req.QuoteAsset,
			ManualPrice:    req.ManualPrice,
			Enabled:        req.Enabled,
		}
		if err := svc.UpsertMarketDataInstrument(r.Context(), instrument); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		status, err := svc.ListMarketData(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"marketData": toMarketDataDTO(status),
		})
	}
}

func handleSetMarketDataInstrumentEnabled(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			ExternalSymbol string `json:"externalSymbol"`
			Enabled        bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if err := svc.SetMarketDataInstrumentEnabled(
			r.Context(), id, req.ExternalSymbol, req.Enabled,
		); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"enabled": req.Enabled})
	}
}

func handleDeleteMarketDataInstrument(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		symbol := r.URL.Query().Get("externalSymbol")
		if err := svc.DeleteMarketDataInstrument(r.Context(), id, symbol); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleVerifyMarketDataSymbol handles POST
// /api/v1/market-data/instances/{id}/verify-symbol. It runs a stateless,
// non-mutating check of whether the external symbol exists on the instance's
// provider; live feeds are untouched. A provider that cannot verify symbols is a
// successful call returning supported=false, not an HTTP error.
func handleVerifyMarketDataSymbol(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			ExternalSymbol string `json:"externalSymbol"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		out, err := svc.VerifyMarketDataSymbol(r.Context(), id, req.ExternalSymbol)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"verification": toMarketDataSymbolVerificationDTO(out),
		})
	}
}

// handleSearchMarketDataSymbols handles POST
// /api/v1/market-data/instances/{id}/search-symbols. It runs a stateless,
// non-mutating search of the instance's provider catalogue; live feeds are
// untouched. An empty query (after trimming) is a validation error and never
// reaches the service. A provider that cannot search symbols is a successful
// call returning supported=false, not an HTTP error.
func handleSearchMarketDataSymbols(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req struct {
			Query                        string `json:"query"`
			SecType                      string `json:"secType"`
			Exchange                     string `json:"exchange"`
			Currency                     string `json:"currency"`
			LastTradeDateOrContractMonth string `json:"lastTradeDateOrContractMonth"`
			Right                        string `json:"right"`
			Strike                       string `json:"strike"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "invalid JSON")
			return
		}
		if strings.TrimSpace(req.Query) == "" {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", "query is required")
			return
		}
		// The strike is an optional, caller-supplied decimal criterion. Validate it
		// here so a malformed value (e.g. "abc") is a 400 from the boundary, not a
		// false upstream 502 from the connector's deep decimal parse.
		if err := domain.ValidateMarketDataStrike(strings.TrimSpace(req.Strike)); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		out, err := svc.SearchMarketDataSymbols(r.Context(), id, backend.MarketDataSymbolSearchInput{
			Query:                        req.Query,
			SecType:                      req.SecType,
			Exchange:                     req.Exchange,
			Currency:                     req.Currency,
			LastTradeDateOrContractMonth: req.LastTradeDateOrContractMonth,
			Right:                        req.Right,
			Strike:                       req.Strike,
		})
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"supported": out.Supported,
			"matches":   toMarketDataSymbolMatchDTOs(out.Matches),
		})
	}
}

// --- groups -----------------------------------------------------------------

// handleListGroups handles GET /api/v1/groups.
