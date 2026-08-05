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
	"strings"

	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func handleGenerateSigningKey(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, err := svc.GenerateSigningKey(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"key": toSigningKeyDTO(key)})
	}
}

// handleImportSigningKey handles POST /api/v1/signing/keys/import. The body
// carries the raw key material and the format (pem-pkcs8 | openssh | raw-base64).
func handleImportSigningKey(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req signingKeyImportRequestDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if req.Key == "" {
			httpx.WriteValidationProblem(w, "key material is required", "/key", "required")
			return
		}
		if req.Format == "" {
			httpx.WriteValidationProblem(w, "format is required", "/format", "required")
			return
		}
		key, err := svc.ImportSigningKey(r.Context(), req.Key, req.Format)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"key": toSigningKeyDTO(key)})
	}
}

// handleListSigningKeys handles GET /api/v1/signing/keys. It returns all keys
// including inactive ones (connectors need public keys to verify in-flight
// tokens). Private material is never returned.
func handleListSigningKeys(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keys, err := svc.ListSigningKeys(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]signingKeyDTO, 0, len(keys))
		for _, k := range keys {
			dtos = append(dtos, toSigningKeyDTO(k))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"keys": dtos})
	}
}

// handleGetActivePublicKey handles GET /api/v1/signing/keys/active/public. The
// optional ?format= parameter selects the export format
// (pem-pkcs8 | openssh | raw-base64); the default is pem-pkcs8.
func handleGetActivePublicKey(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		format := signingKeyFormatOrDefault(r)
		if !validSigningKeyFormat(format) {
			httpx.WriteValidationProblem(
				w, "format must be pem-pkcs8, openssh, or raw-base64", "/format", "enum",
			)
			return
		}
		pub, err := svc.ActivePublicKey(format)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, publicKeyDTO{PublicKey: pub})
	}
}

// handleGetSigningKeyPublic handles GET /api/v1/signing/keys/{keyId}/public. It
// resolves the public key by id (rotation-safe: not the active key), so the
// reproduction panel's paste-to-verify can resolve the keyId embedded in any
// pasted token, including one signed under a since-rotated key. The optional
// ?format= parameter selects the export format (pem-pkcs8 | openssh |
// raw-base64); the default is pem-pkcs8. Only public material is ever returned;
// an unknown id is a 404.
func handleGetSigningKeyPublic(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keyID, err := httpx.PathSigningKeyID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		format := signingKeyFormatOrDefault(r)
		if !validSigningKeyFormat(format) {
			httpx.WriteValidationProblem(
				w, "format must be pem-pkcs8, openssh, or raw-base64", "/format", "enum",
			)
			return
		}
		pub, err := svc.PublicKeyByID(r.Context(), keyID, format)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, publicKeyMaterialDTO{
			KeyID:  keyID,
			Alg:    "ed25519",
			Format: format,
			Key:    pub,
		})
	}
}

// signingKeyFormatOrDefault reads the ?format= query parameter, defaulting to
// pem-pkcs8 when absent.
func signingKeyFormatOrDefault(r *http.Request) string {
	format := r.URL.Query().Get("format")
	if format == "" {
		return "pem-pkcs8"
	}
	return format
}

// validSigningKeyFormat reports whether format is a supported public-key export
// format.
func validSigningKeyFormat(format string) bool {
	switch format {
	case "pem-pkcs8", "openssh", "raw-base64":
		return true
	default:
		return false
	}
}

// handleGetSigningConfig handles GET /api/v1/signing/config. It returns the
// current signing configuration (the global eSign-off flag).
func handleGetSigningConfig(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		noESign, err := svc.GetNoESign(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, signingConfigDTO{NoESign: noESign})
	}
}

// handleSetSigningConfig handles PUT /api/v1/signing/config. The body carries
// the new eSign-off state.
func handleSetSigningConfig(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req signingConfigUpdateDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if req.NoESign == nil {
			httpx.WriteErrMsg(
				w, http.StatusBadRequest, "validation", "noESign is required",
			)
			return
		}
		if err := svc.SetNoESign(r.Context(), *req.NoESign); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, signingConfigDTO{NoESign: *req.NoESign})
	}
}

// --- approval token ---------------------------------------------------------

// handleSubmitOrderToken handles
// POST /api/v1/orders/submit?missingAccount=create|reject. The body carries
// the order fields, an optional caller-supplied external id, and the submit mode
// (hold | immediate). The hold wire value is retained for compatibility and
// selects the workflow path that waits for execution reports. Submit CREATES
// the order exactly once: it runs pre-trade in the given mode and records the
// order, then issues a signed
// approval token on accept. The returned id is the one actually used
// (the supplied one when valid, else a generated one), so confirm/cancel resolve
// the same order. There is no separate persisting create before submit.
func handleSubmitOrderToken(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req submitOrderTokenRequestDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		mode := req.Mode
		if mode == "" {
			mode = "immediate"
		}
		if mode != "hold" && mode != "immediate" {
			httpx.WriteValidationProblem(
				w, "mode must be hold or immediate", "/mode", "enum",
			)
			return
		}
		order, err := submitOrderFromDTO(req.orderFields())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		missing, err := missingAccountQuery(r, order.Account)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		tok, err := svc.SubmitOrderToken(r.Context(), order, mode, missing)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, approvalTokenDTO{
			Token:           tok.Token,
			KeyID:           tok.KeyID,
			OrderExternalID: tok.OrderExternalID,
			Verdict:         tok.Verdict,
			Reasons:         toOrderRejectDTOs(tok.Reasons),
		})
	}
}

// handleSubmitDropCopyOrder handles the distinct unsigned drop-copy submit at
// POST /api/v1/orders/drop-copy/submit?missingAccount=create. Its external id is
// optional; when omitted, the store assigns one. Duplicate ids follow the
// ordinary unique-store conflict path; no approval token is produced. Only
// missingAccount=create is accepted here (see the backend).
func handleSubmitDropCopyOrder(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req submitDropCopyOrderRequestDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		order, err := submitOrderFromDTO(req.orderFields())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		missing, err := missingAccountQuery(r, order.Account)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		created, err := svc.SubmitDropCopyOrder(r.Context(), order, missing)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, dropCopyOrderResponseDTO{
			OrderExternalID: created.ExternalID.String(),
			Status:          string(created.Status),
		})
	}
}

func submitOrderFromDTO(req submitOrderFieldsDTO) (domain.Order, error) {
	order := domain.Order{
		Account:     domain.AccountID(req.Account),
		BaseAsset:   req.BaseAsset,
		QuoteAsset:  req.QuoteAsset,
		Side:        domain.OrderSide(req.Side),
		AmountKind:  domain.OrderAmountKind(req.AmountKind),
		AmountValue: req.AmountValue,
		Price:       req.Price,
	}
	// The backend seam validates the id too, for callers that never pass here.
	// The surface repeats it so a malformed account is reported ahead of the
	// secondary missingAccount parameter error.
	if err := domain.ValidateAccountID(order.Account); err != nil {
		return domain.Order{}, err
	}
	if req.ID == "" {
		return order, nil
	}
	id, err := domain.ParseExternalID(req.ID)
	if err != nil {
		return domain.Order{}, err
	}
	order.ExternalID = id
	return order, nil
}

// handleConfirmExecution handles POST /api/v1/orders/{id}/confirm. The body
// carries the approval token used to record the order confirmation shortcut.
func handleConfirmExecution(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orderID, err := httpx.PathOrderExternalID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req confirmExecutionRequestDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if req.Token == "" {
			httpx.WriteValidationProblem(w, "token is required", "/token", "required")
			return
		}
		order, att, err := svc.ConfirmExecution(r.Context(), orderID, req.Token)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		presentation := orderPresentationByID(
			r.Context(), svc, order.ExternalID,
		)
		httpx.WriteJSON(w, http.StatusOK, orderMutationResponseDTO{
			Order: toOrderDTO(
				order, presentation.displayPrice, presentation.signed,
			),
			AttestationToken: att.Token,
			AttestationKeyID: att.KeyID,
			Signed:           att.Signed,
		})
	}
}

// handleCancelOrder handles POST /api/v1/orders/{id}/cancel. The body carries
// the approval token, the caller-supplied leaves the engine-settled
// cancellation requires, and an optional reason for the shortcut.
func handleCancelOrder(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orderID, err := httpx.PathOrderExternalID(r)
		if err != nil {
			httpx.WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		var req cancelOrderRequestDTO
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		if req.Token == "" {
			httpx.WriteValidationProblem(w, "token is required", "/token", "required")
			return
		}
		leavesQuantity := strings.TrimSpace(req.LeavesQuantity)
		if leavesQuantity == "" {
			httpx.WriteErrMsg(
				w, http.StatusBadRequest, "validation", "leavesQuantity is required",
			)
			return
		}
		order, att, err := svc.CancelOrder(
			r.Context(), orderID, req.Token, leavesQuantity, req.Reason)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		presentation := orderPresentationByID(
			r.Context(), svc, order.ExternalID,
		)
		httpx.WriteJSON(w, http.StatusOK, orderMutationResponseDTO{
			Order: toOrderDTO(
				order, presentation.displayPrice, presentation.signed,
			),
			AttestationToken: att.Token,
			AttestationKeyID: att.KeyID,
			Signed:           att.Signed,
		})
	}
}

// --- dashboard / service ----------------------------------------------------

// handleOverview handles GET /api/v1/overview. The optional ?since= query
// parameter (RFC3339) sets the "today" boundary for the orders-today tally;
// when absent or unparseable it falls back to the server-local start of day.
