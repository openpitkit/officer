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
	"log/slog"
	"net/http"

	"go.openpit.dev/officer/framework/domain"
	fwsigning "go.openpit.dev/officer/framework/signing"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	appsigning "go.openpit.dev/officer/internal/signing"
)

type orderPresentation struct {
	displayPrice string
	signed       bool
}

// orderPresentationByID re-reads presentation data after a successful
// mutation. A lookup failure cannot safely fail an already-applied mutation,
// so it is logged and the response falls back to empty presentation fields.
func orderPresentationByID(
	ctx context.Context, svc Service, id domain.ExternalID,
) orderPresentation {
	detail, err := svc.GetOrder(ctx, id.String())
	if err != nil {
		slog.Warn(
			"order presentation unavailable after mutation",
			"order", id,
			"error", err,
		)
		return orderPresentation{}
	}
	return orderPresentation{
		displayPrice: detail.DisplayPrice,
		signed:       detail.Signed(),
	}
}

// handleCheckOrder handles POST /api/v1/orders/check. It runs the engine
// pre-trade as a non-mutating dry-run and reports whether the order would pass.
// An engine reject is a successful call returning passed=false with the
// reasons, not an HTTP error; nothing is recorded and no state changes.
func handleCheckOrder(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Account     string `json:"account"`
			BaseAsset   string `json:"baseAsset"`
			QuoteAsset  string `json:"quoteAsset"`
			Side        string `json:"side"`
			AmountKind  string `json:"amountKind"`
			AmountValue string `json:"amountValue"`
			Price       string `json:"price"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		probe := domain.OrderProbe{
			Account:     domain.AccountID(req.Account),
			BaseAsset:   req.BaseAsset,
			QuoteAsset:  req.QuoteAsset,
			Side:        domain.OrderSide(req.Side),
			AmountKind:  domain.OrderAmountKind(req.AmountKind),
			AmountValue: req.AmountValue,
			Price:       req.Price,
		}
		out, err := svc.CheckOrder(r.Context(), probe)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"check": toCheckResultDTO(out)})
	}
}

// handleListOrders handles GET /api/v1/orders[?account=&source=&limit=].
func handleListOrders(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := orderListFilterFromQuery(r.URL.Query())
		if err != nil {
			httpx.WriteValidationErr(w, err)
			return
		}
		orders, err := svc.ListOrderRows(r.Context(), filter)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dtos := make([]orderDTO, 0, len(orders.Rows))
		for _, o := range orders.Rows {
			dtos = append(dtos, toOrderDTO(
				o.Order, o.DisplayPrice, o.Signed,
			))
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"orders": dtos,
			"total":  orders.Total,
		})
	}
}

// handleGetOrder handles GET /api/v1/orders/{id}. It returns the order,
// its 1:1 approval envelope (omitted when unsigned), its events, and its trades,
// all addressed by opaque external ids.
func handleGetOrder(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathOrderExternalID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		detail, err := svc.GetOrder(r.Context(), id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		events := make([]orderEventDTO, 0, len(detail.Events))
		for _, e := range detail.Events {
			events = append(events, toOrderEventDTO(e))
		}
		trades := make([]tradeDTO, 0, len(detail.Trades))
		for _, t := range detail.Trades {
			trades = append(trades, toTradeDTO(t))
		}
		// signed is the order-level rollup (any event carries a signed
		// attestation); per-event attestation metadata rides on each event DTO.
		body := map[string]any{
			"order": toOrderDTO(
				detail.Order, detail.DisplayPrice, detail.Signed(),
			),
			"events": events,
			"trades": trades,
		}
		httpx.WriteJSON(w, http.StatusOK, body)
	}
}

// handleGetOrderEventReproduction handles GET
// /api/v1/orders/{id}/events/{eventId}/reproduction. It returns the
// controller-facing reproduction bundle for one order-history event's
// attestation: byte-for-byte what a robot / AI agent received from the live APIs
// for the request that produced this event, assembled from persisted state
// through the SAME serializers the live endpoints use plus the same signing code
// (DecodeEnvelope / CanonicalBytes) so the panel can never drift from real API
// output. Nothing is re-issued or re-signed. Only public artifacts are exposed;
// private key material never appears.
//
// It shares the order-read auth seam: the request reaches here only through the
// same middleware/authorizer as GET /orders/{id}, and GetOrder enforces whatever
// account/tenant scoping order reads already apply, so a controller cannot
// reproduce an event they could not already read.
func handleGetOrderEventReproduction(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orderID, err := httpx.PathOrderExternalID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		eventID, err := httpx.PathOrderEventID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		// Reproduction-permission seam: a future controller/reproduction capability
		// check slots in here, gating this endpoint by capability without
		// restructuring. It is intentionally the single guard point; the RBAC system
		// itself is out of scope for this change.
		detail, err := svc.GetOrder(r.Context(), orderID)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		var event *domain.OrderEvent
		for i := range detail.Events {
			if detail.Events[i].ExternalID.String() == eventID {
				event = &detail.Events[i]
				break
			}
		}
		if event == nil {
			httpx.WriteErrMsg(w, http.StatusNotFound, "not_found",
				"order event not found")
			return
		}
		bundle, err := buildEventReproduction(r.Context(), svc, *event)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, bundle)
	}
}

// buildEventReproduction assembles the reproduction bundle for one event. It
// reuses the live serializers for byte-identity: toOrderEventDTO for the event,
// toEventAttestationDTO for the attestation metadata, and a reconstructed
// type-specific response DTO that reproduces the exact live API response (token
// verbatim). The bound request and the canonical signed bytes and base64
// signature are recovered from the persisted token with the same signing code
// that produced it (DecodeEnvelope + CanonicalBytes). The public key is resolved
// rotation-safe by the attestation's keyId. No re-issue or re-sign occurs.
func buildEventReproduction(
	ctx context.Context, svc Service, event domain.OrderEvent,
) (eventReproductionDTO, error) {
	bundle := eventReproductionDTO{
		Event: toOrderEventDTO(event),
		ESign: eventReproductionESignDTO{
			Signed: eventAttestationSigned(event.Attestation),
		},
	}
	noESign, err := svc.GetNoESign(ctx)
	if err != nil {
		return eventReproductionDTO{}, err
	}
	bundle.ESign.NoESign = noESign

	att := event.Attestation
	if att == nil {
		// Legacy or intermediate events can be unsigned. The attestation, request,
		// response, canonicalApproval and publicKey are null; reason documents the
		// absence so the client need not infer it.
		bundle.Reason = "event has no persisted attestation envelope"
		return bundle, nil
	}
	bundle.RequestType = string(att.RequestType)
	bundle.ESign.Alg = att.Alg
	bundle.Attestation = toEventAttestationDTO(att)

	// Decode the persisted token with the same signing code that built it and
	// expose the payload in its exact canonical signed form and the bound request.
	// This is byte-identical to what was signed; it is never re-serialized here.
	env, err := appsigning.DecodeEnvelope(att.Token)
	if err != nil {
		// A token that fails to decode is presentation-only degradation: the event
		// row and its attestation metadata stay authoritative, so the bundle still
		// returns with the canonical bytes, request, response and public key omitted.
		bundle.Reason = "persisted attestation token could not be decoded"
		return bundle, nil
	}
	bundle.Request = toEventReproductionRequestDTO(env.Approval)
	bundle.Response = toEventReproductionResponseDTO(event, att, env.Approval)

	canon, err := appsigning.CanonicalBytes(env.Approval)
	if err != nil {
		return eventReproductionDTO{}, err
	}
	canonStr := string(canon)
	bundle.CanonicalApproval = &canonStr
	bundle.Signature = env.Signature

	// Resolve the public key by the envelope's own keyId, not the active key, so a
	// token signed under a since-rotated key reproduces the exact key that signed
	// it. Under alg "none" there is no signing key or signature to reproduce.
	if env.Approval.Alg == fwsigning.AlgEd25519 && env.KeyID != "" {
		pub, err := svc.PublicKeyByID(ctx, env.KeyID, "pem-pkcs8")
		if err != nil {
			return eventReproductionDTO{}, err
		}
		bundle.PublicKey = &publicKeyMaterialDTO{
			KeyID:  env.KeyID,
			Alg:    env.Approval.Alg,
			Format: "pem-pkcs8",
			Key:    pub,
		}
	}
	return bundle, nil
}

// toEventReproductionRequestDTO reconstructs the request bound in an attestation
// payload for reproduction: the request type and its material params plus the
// recorded result section when the payload carries one. Every issued payload
// stamps its request type, so an empty one is an unsupported payload and yields
// no reconstructed request (nil), matching how the response facet is omitted for
// an unrecognized request type.
func toEventReproductionRequestDTO(p domain.ApprovalPayload) *eventReproductionRequestDTO {
	if p.RequestType == "" {
		return nil
	}
	dto := &eventReproductionRequestDTO{
		RequestType:     p.RequestType,
		OrderExternalID: p.OrderExternalID,
		EventExternalID: p.EventExternalID,
		Instrument:      p.Instrument,
		Side:            p.Side,
		Quantity:        p.Quantity,
		AmountKind:      p.AmountKind,
		OrderType:       p.OrderType,
		LimitPrice:      p.LimitPrice,
		PriceCurrency:   p.PriceCurrency,
		AccountID:       p.AccountID,
		Verdict:         p.Verdict,
		Rejects:         toOrderRejectDTOs(p.Rejects),
		ExecutionReport: toExecutionReportRequestDTO(p.ExecutionReport),
	}
	if p.Result != nil {
		blocks := make([]attestationBlockDTO, 0, len(p.Result.Blocks))
		for _, b := range p.Result.Blocks {
			blocks = append(blocks, attestationBlockDTO{
				Account: b.Account,
				Policy:  b.Policy,
				Code:    b.Code,
				Reason:  b.Reason,
				Details: b.Details,
			})
		}
		dto.Result = &attestationResultDTO{
			Outcome:        p.Result.Outcome,
			FillQuantity:   p.Result.FillQuantity,
			FillPrice:      p.Result.FillPrice,
			FillLockPrice:  p.Result.FillLockPrice,
			Commission:     toCommissionDTO(p.Result.Commission),
			LeavesQuantity: p.Result.LeavesQuantity,
			OrderStatus:    p.Result.OrderStatus,
			Blocks:         blocks,
		}
	}
	return dto
}

// toEventReproductionResponseDTO reconstructs the exact type-specific live API
// response the robot received for the attested request. The token is carried
// verbatim in the response's attestation-token field, matching the live handler.
// Only the facet matching the request type is populated.
func toEventReproductionResponseDTO(
	event domain.OrderEvent, att *domain.EventAttestation, p domain.ApprovalPayload,
) *eventReproductionResponseDTO {
	out := &eventReproductionResponseDTO{}
	switch att.RequestType {
	case domain.AttestationRequestSubmit:
		out.SubmitResponse = &approvalTokenDTO{
			Token:           att.Token,
			KeyID:           att.KeyID,
			OrderExternalID: event.Order.String(),
			Verdict:         p.Verdict,
			Reasons:         submitPayloadRejectReasons(p),
		}
	case domain.AttestationRequestExecutionReport:
		reportID := ""
		if event.Payload.ExecutionReport != nil {
			reportID = event.Payload.ExecutionReport.ExternalID.String()
		}
		out.ExecutionReport = &executionReportResponseDTO{
			ID:               reportID,
			Result:           executionResultFromPayload(p),
			AttestationToken: att.Token,
			AttestationKeyID: att.KeyID,
			Signed:           eventAttestationSigned(att),
		}
	case domain.AttestationRequestConfirm:
		out.Confirm = orderMutationResponseFromPayload(event, att, p)
	case domain.AttestationRequestCancel:
		out.Cancel = orderMutationResponseFromPayload(event, att, p)
	}
	return out
}

func submitPayloadRejectReasons(p domain.ApprovalPayload) []orderRejectDTO {
	if p.Verdict != "reject" {
		return nil
	}
	if len(p.Rejects) > 0 {
		return toOrderRejectDTOs(p.Rejects)
	}
	if p.RejectCode == "" && p.RejectScope == "" &&
		p.RejectPolicy == "" && p.RejectReason == "" &&
		p.RejectDetails == "" {
		return nil
	}
	return []orderRejectDTO{{
		Code:    p.RejectCode,
		Scope:   p.RejectScope,
		Policy:  p.RejectPolicy,
		Reason:  p.RejectReason,
		Details: p.RejectDetails,
	}}
}

// executionResultFromPayload reconstructs the execution result DTO from the
// signed payload's result section (blocks and per-asset balance outcomes are not
// re-derived; only the blocks bound in the payload are surfaced).
func executionResultFromPayload(p domain.ApprovalPayload) executionResultDTO {
	blocks := make([]executionBlockDTO, 0)
	if p.Result != nil {
		for _, b := range p.Result.Blocks {
			blocks = append(blocks, executionBlockDTO{
				Account: b.Account,
				Policy:  b.Policy,
				Code:    b.Code,
				Reason:  b.Reason,
				Details: b.Details,
			})
		}
	}
	return executionResultDTO{Blocks: blocks, Outcomes: make([]executionOutcomeDTO, 0)}
}

// orderMutationResponseFromPayload reconstructs the confirm/cancel response DTO
// from persisted state. The order is rebuilt from the payload's bound params and
// result status; the token is carried verbatim.
func orderMutationResponseFromPayload(
	event domain.OrderEvent, att *domain.EventAttestation, p domain.ApprovalPayload,
) *orderMutationResponseDTO {
	status := ""
	if p.Result != nil {
		status = p.Result.OrderStatus
	}
	return &orderMutationResponseDTO{
		Order: orderDTO{
			ID:                  event.Order.String(),
			Account:             p.AccountID,
			Side:                p.Side,
			AmountKind:          p.AmountKind,
			AmountValue:         p.Quantity,
			CommissionSubtotals: toCommissionDTOs(nil),
			Price:               p.LimitPrice,
			Status:              status,
			DisplayPrice:        "",
			Signed:              eventAttestationSigned(att),
		},
		AttestationToken: att.Token,
		AttestationKeyID: att.KeyID,
		Signed:           eventAttestationSigned(att),
	}
}

// handleApplyExecutionReport handles
// POST /api/v1/orders/{id}/execution-reports. The instrument, account, and side
// are taken from the parent order; every report carries a target status.
func handleApplyExecutionReport(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := httpx.PathOrderExternalID(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		var req struct {
			ID             string         `json:"id"`
			Quantity       string         `json:"quantity"`
			Price          string         `json:"price"`
			LeavesQuantity string         `json:"leavesQuantity"`
			LockPrice      string         `json:"lockPrice"`
			Commission     *commissionDTO `json:"commission"`
			Status         string         `json:"status"`
			Force          bool           `json:"force"`
		}
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		status := domain.OrderStatus(req.Status)
		var commission *domain.Commission
		if req.Commission != nil {
			hasAmount := req.Commission.Amount != ""
			hasCurrency := req.Commission.Currency != ""
			if hasAmount || hasCurrency {
				commission = &domain.Commission{
					Amount:   req.Commission.Amount,
					Currency: req.Commission.Currency,
				}
			}
		}
		in := domain.ExecutionReportInput{
			FillQuantity:   req.Quantity,
			FillPrice:      req.Price,
			LeavesQuantity: req.LeavesQuantity,
			LockPrice:      req.LockPrice,
			Commission:     commission,
			OrderStatus:    status,
			Force:          req.Force,
		}
		if req.ID != "" {
			reportID, err := domain.ParseExternalID(req.ID)
			if err != nil {
				httpx.WriteValidationProblem(w, err.Error(), "/id", "format")
				return
			}
			in.ExternalID = reportID
		}
		if _, err := domain.ExecutionReportRequiresEngine(in); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		orderID, err := domain.ParseExternalID(id)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		in.Order = orderID
		result, att, err := svc.ApplyExecutionReport(r.Context(), in)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, executionReportResponseDTO{
			ID:               result.ReportID.String(),
			Result:           toExecutionResultDTO(result),
			AttestationToken: att.Token,
			AttestationKeyID: att.KeyID,
			Signed:           att.Signed,
		})
	}
}

// handleListTrades handles GET /api/v1/trades.
