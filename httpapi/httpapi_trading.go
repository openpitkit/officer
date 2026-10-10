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
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func handleGetTrading(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := svc.GetTrading(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"trading": toTradingDTO(status)})
	}
}

func tradingObjectMember(w http.ResponseWriter, raw json.RawMessage, member, pointer string) (string, bool) {
	var object map[string]json.RawMessage
	if len(raw) == 0 {
		httpx.WriteValidationProblem(w, member+" is required", pointer, "required")
		return "", false
	}
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		httpx.WriteValidationProblem(w, member+" must be a JSON object", pointer, "type")
		return "", false
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		httpx.WriteValidationProblem(w, member+" must be a JSON object", pointer, "type")
		return "", false
	}
	return compact.String(), true
}

func handleCreateTradingConnection(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID          string             `json:"id"`
			Provider    string             `json:"provider"`
			Label       *string            `json:"label"`
			Mode        domain.TradingMode `json:"mode"`
			Credentials json.RawMessage    `json:"credentials"`
			Enabled     *bool              `json:"enabled"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if req.Label == nil || strings.TrimSpace(*req.Label) == "" {
			httpx.WriteValidationProblem(w, "label is required", "/label", "required")
			return
		}
		if req.Enabled == nil {
			httpx.WriteValidationProblem(w, "enabled is required", "/enabled", "required")
			return
		}
		if strings.TrimSpace(req.Provider) == "" {
			httpx.WriteValidationProblem(w, "provider is required", "/provider", "required")
			return
		}
		if req.Mode == "" {
			httpx.WriteValidationProblem(w, "mode is required", "/mode", "required")
			return
		}
		if !domain.TradingModeSupported(req.Mode) {
			httpx.WriteValidationProblem(w, "mode must be paper or live", "/mode", "enum")
			return
		}
		credentials, ok := tradingObjectMember(w, req.Credentials, "credentials", "/credentials")
		if !ok {
			return
		}
		c := domain.TradingConnection{Provider: req.Provider, Label: *req.Label, Mode: req.Mode, Credentials: credentials, Enabled: *req.Enabled}
		if req.ID != "" {
			id, err := domain.ParseExternalID(req.ID)
			if err != nil {
				httpx.WriteErr(w, err)
				return
			}
			c.ExternalID = id
		}
		created, err := svc.CreateTradingConnection(r.Context(), c)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		status, err := svc.GetTrading(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		for _, connection := range status.Connections {
			if connection.ExternalID == created.ExternalID {
				httpx.WriteJSON(w, http.StatusCreated, map[string]any{"connection": toTradingConnectionDTO(connection)})
				return
			}
		}
		httpx.WriteErr(w, errors.New("created trading connection is missing from the snapshot"))
	}
}

func handleUpdateTradingConnectionSettings(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var req struct {
			Label       json.RawMessage `json:"label"`
			Credentials json.RawMessage `json:"credentials"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		label := ""
		if len(req.Label) != 0 {
			if err := json.Unmarshal(req.Label, &label); err != nil {
				httpx.WriteValidationProblem(w, "label must be a string", "/label", "type")
				return
			}
			if strings.TrimSpace(label) == "" {
				httpx.WriteValidationProblem(w, "label must not be blank", "/label", "required")
				return
			}
		}
		credentials := ""
		if len(req.Credentials) != 0 {
			value, ok := tradingObjectMember(w, req.Credentials, "credentials", "/credentials")
			if !ok {
				return
			}
			credentials = value
		}
		if err := svc.UpdateTradingConnectionSettings(r.Context(), id, label, credentials); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		status, err := svc.GetTrading(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"trading": toTradingDTO(status)})
	}
}

func handleSetTradingConnectionEnabled(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if req.Enabled == nil {
			httpx.WriteValidationProblem(w, "enabled is required", "/enabled", "required")
			return
		}
		if err := svc.SetTradingConnectionEnabled(r.Context(), id, *req.Enabled); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"enabled": *req.Enabled})
	}
}

func handleCheckTradingConnection(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		info, err := svc.CheckTradingConnection(r.Context(), id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"check": map[string]any{"venueIdentity": info.Identity, "venueStatus": info.Status}})
	}
}

func handleUpsertTradingInstrument(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var req struct {
			ExternalSymbol string `json:"externalSymbol"`
			BaseAsset      string `json:"baseAsset"`
			QuoteAsset     string `json:"quoteAsset"`
			Enabled        *bool  `json:"enabled"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.ExternalSymbol) == "" {
			httpx.WriteValidationProblem(w, "externalSymbol is required", "/externalSymbol", "required")
			return
		}
		if strings.TrimSpace(req.BaseAsset) == "" {
			httpx.WriteValidationProblem(w, "baseAsset is required", "/baseAsset", "required")
			return
		}
		if strings.TrimSpace(req.QuoteAsset) == "" {
			httpx.WriteValidationProblem(w, "quoteAsset is required", "/quoteAsset", "required")
			return
		}
		if req.Enabled == nil {
			httpx.WriteValidationProblem(w, "enabled is required", "/enabled", "required")
			return
		}
		connection, err := domain.ParseExternalID(id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		if err := svc.UpsertTradingInstrument(r.Context(), domain.TradingInstrument{Connection: connection, ExternalSymbol: req.ExternalSymbol, BaseAsset: req.BaseAsset, QuoteAsset: req.QuoteAsset, Enabled: *req.Enabled}); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		status, err := svc.GetTrading(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"trading": toTradingDTO(status)})
	}
}

func handleDeleteTradingInstrument(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		symbol := r.URL.Query().Get("externalSymbol")
		if strings.TrimSpace(symbol) == "" {
			httpx.WriteValidationProblem(w, "externalSymbol is required", "/externalSymbol", "required")
			return
		}
		if err := svc.DeleteTradingInstrument(r.Context(), id, symbol); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleVerifyTradingSymbol(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var req struct {
			ExternalSymbol string `json:"externalSymbol"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.ExternalSymbol) == "" {
			httpx.WriteValidationProblem(w, "externalSymbol is required", "/externalSymbol", "required")
			return
		}
		out, err := svc.VerifyTradingSymbol(r.Context(), id, req.ExternalSymbol)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		verification := struct {
			Supported bool   `json:"supported"`
			Exists    bool   `json:"exists"`
			Tradable  bool   `json:"tradable"`
			Details   string `json:"details,omitempty"`
		}{Supported: out.Supported, Exists: out.Exists, Tradable: out.Tradable, Details: out.Details}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"verification": verification})
	}
}

func handleSearchTradingSymbols(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var req struct {
			Query string `json:"query"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.Query) == "" {
			httpx.WriteValidationProblem(w, "query is required", "/query", "required")
			return
		}
		out, err := svc.SearchTradingSymbols(r.Context(), id, req.Query)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		type matchDTO struct {
			Symbol   string `json:"symbol"`
			Name     string `json:"name,omitempty"`
			Class    string `json:"class,omitempty"`
			Exchange string `json:"exchange,omitempty"`
			Tradable bool   `json:"tradable"`
		}
		matches := make([]matchDTO, 0, len(out.Matches))
		for _, m := range out.Matches {
			matches = append(matches, matchDTO{Symbol: m.Symbol, Name: m.Name, Class: m.Class, Exchange: m.Exchange, Tradable: m.Tradable})
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"supported": out.Supported, "matches": matches})
	}
}

func handlePutTradingAccess(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var req struct {
			Account      string `json:"account"`
			AccountGroup string `json:"accountGroup"`
			VenueAccount string `json:"venueAccount"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		// Both subjects reach the service, whose validation refuses them at
		// /accountGroup; a second mapping here would drift from it. When the
		// composition does not offer accounts, an omitted subject reaches the
		// service, whose account scope supplies the one account.
		if svc.AccountFeatures().Accounts() &&
			strings.TrimSpace(req.Account) == "" && strings.TrimSpace(req.AccountGroup) == "" {
			httpx.WriteValidationProblem(w, "account or accountGroup is required", "/account", "required")
			return
		}
		connection, err := domain.ParseExternalID(id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		if err := svc.PutTradingAccess(r.Context(), domain.TradingAccess{
			Account: domain.AccountID(req.Account), AccountGroup: req.AccountGroup,
			Connection: connection, VenueAccount: req.VenueAccount,
		}); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		status, err := svc.GetTrading(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"trading": toTradingDTO(status)})
	}
}

func handleRemoveTradingAccess(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		connection, err := domain.ParseExternalID(id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		query := r.URL.Query()
		account, accountGroup := query.Get("account"), query.Get("accountGroup")
		// Both subjects reach the service, whose validation refuses them at
		// /accountGroup; a second mapping here would drift from it. When the
		// composition does not offer accounts, an omitted subject reaches the
		// service, whose account scope supplies the one account.
		if svc.AccountFeatures().Accounts() &&
			strings.TrimSpace(account) == "" && strings.TrimSpace(accountGroup) == "" {
			httpx.WriteValidationProblem(w, "account or accountGroup is required", "/account", "required")
			return
		}
		if err := svc.RemoveTradingAccess(r.Context(), connection, domain.AccountID(account), accountGroup); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleSetTradingFeeAccount(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var req struct {
			Account string `json:"account"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		features := svc.AccountFeatures()
		if features.Accounts() && strings.TrimSpace(req.Account) == "" {
			httpx.WriteValidationProblem(w, "account is required", "/account", "required")
			return
		}
		// The service reads an empty fee account as a clear, so an omitted one
		// is named here as the composition's one account.
		account := features.AccountFor(domain.AccountID(req.Account))
		connection, err := domain.ParseExternalID(id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		if err := svc.SetTradingFeeAccount(r.Context(), connection, account); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		status, err := svc.GetTrading(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"trading": toTradingDTO(status)})
	}
}

func handleDeleteTradingFeeAccount(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		connection, err := domain.ParseExternalID(id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		if err := svc.SetTradingFeeAccount(r.Context(), connection, ""); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleListTradingDestinations(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, err := httpx.PathAccountID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		mode := domain.TradingMode(r.URL.Query().Get("mode"))
		if mode != "" && !domain.TradingModeSupported(mode) {
			httpx.WriteValidationProblem(w, "mode must be paper or live", "/mode", "enum")
			return
		}
		destinations, err := svc.ListTradingDestinations(r.Context(), account, mode)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"destinations": httpx.ToTradingDestinationDTOs(destinations)})
	}
}

func handleSendPaperOrder(svc backend.ControlPlane) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req sendPaperOrderRequestDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		route, ok := tradingObjectMember(w, req.Destination.Route,
			"destination route", "/destination/route")
		if !ok {
			return
		}
		order, err := submitOrderFromDTO(req.orderFields(), svc.AccountFeatures())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dest := backend.SendDestination{
			VenueAccount: req.Destination.VenueAccount, Route: route,
		}
		if req.Destination.Connection != "" {
			dest.Connection, err = domain.ParseExternalID(req.Destination.Connection)
			if err != nil {
				httpx.WriteErr(w, err)
				return
			}
		}
		result, err := svc.SendPaperOrder(r.Context(), order, dest)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		tok := result.Token
		out := sendPaperOrderResponseDTO{signedApprovalDTO: signedApprovalDTO{
			SignedApproval: tok.Token, KeyID: tok.KeyID,
			OrderExternalID: tok.OrderExternalID, Verdict: tok.Verdict,
			Reasons: toOrderRejectDTOs(tok.Reasons),
		}}
		if result.Send != nil {
			out.VenueSend = &venueSendDTO{Status: result.Send.Status,
				Error: result.Send.Error}
			if result.Send.Order != nil {
				link, err := toVenueOrderDTO(*result.Send.Order)
				if err != nil {
					httpx.WriteErr(w, err)
					return
				}
				out.VenueSend.VenueOrder = &link
			}
		}
		httpx.WriteJSON(w, http.StatusCreated, out)
	}
}
