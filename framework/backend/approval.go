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

package backend

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
)

// Submit modes for an approval token. SubmitModeImmediate is the default and
// mirrors the current submit behaviour (commit and settle at the lock price);
// SubmitModeHold keeps the reservation held for out-of-band approval.
const (
	SubmitModeImmediate = "immediate"
	SubmitModeHold      = "hold"
)

// approvalPayloadVersion is the canonical payload schema version.
const approvalPayloadVersion = 1

// ApprovalToken is the backend-facing result of issuing an approval token. The
// surface layers map it onto their wire DTO. Token is the base64url-encoded
// signed (or eSign-off) envelope; KeyID is the signing key id (empty under
// eSign-off); Signed reports whether the envelope carries a signature.
type ApprovalToken struct {
	// ExpiresAt is when the token (and, for a hold, the held reservation) expires.
	ExpiresAt time.Time
	// Token is the base64url-encoded approval envelope.
	Token string
	// KeyID is the signing key id; empty under eSign-off.
	KeyID string
	// OrderExternalID is the opaque public handle of the recorded order the token
	// authorises. It is the order's external id, never a surrogate or engine id.
	OrderExternalID string
	// Signed reports whether the envelope carries an Ed25519 signature.
	Signed bool
}

// signerOrErr returns the configured signer or an unconfigured error. The
// signing and approval surfaces require a signer; nil or unavailable signing
// means the feature was not wired.
func (s *Service) signerOrErr() (fwsigning.Service, error) {
	if fwsigning.IsUnavailable(s.signer) {
		return nil, fwsigning.ErrNotConfigured
	}
	return s.signer, nil
}

// --- Signing keys -----------------------------------------------------------

// GenerateSigningKey generates a fresh Ed25519 keypair, makes it the sole active
// key, audits the action, and returns it without the private half.
func (s *Service) GenerateSigningKey(ctx context.Context) (domain.SigningKey, error) {
	signer, err := s.signerOrErr()
	if err != nil {
		return domain.SigningKey{}, err
	}
	key, err := signer.GenerateKey(ctx)
	if err != nil {
		return domain.SigningKey{}, err
	}
	if err := s.auditSigning(ctx, domain.AuditActionGenerateSigningKey,
		fmt.Sprintf("generate signing key %s", key.KeyID)); err != nil {
		return domain.SigningKey{}, err
	}
	return key, nil
}

// ImportSigningKey imports an externally supplied Ed25519 private key in the
// given format (pem-pkcs8 | openssh | raw-base64), makes it the sole active key,
// audits the action, and returns it without the private half.
func (s *Service) ImportSigningKey(
	ctx context.Context, material, format string,
) (domain.SigningKey, error) {
	signer, err := s.signerOrErr()
	if err != nil {
		return domain.SigningKey{}, err
	}
	key, err := signer.ImportKey(ctx, material, format)
	if err != nil {
		return domain.SigningKey{}, err
	}
	if err := s.auditSigning(ctx, domain.AuditActionImportSigningKey,
		fmt.Sprintf("import signing key %s format=%s", key.KeyID, format)); err != nil {
		return domain.SigningKey{}, err
	}
	return key, nil
}

// ListSigningKeys returns every signing key newest-first, including inactive
// public keys (connectors verify in-flight tokens). Private material is never
// carried.
func (s *Service) ListSigningKeys(ctx context.Context) ([]domain.SigningKey, error) {
	signer, err := s.signerOrErr()
	if err != nil {
		return nil, err
	}
	return signer.ListKeys(ctx)
}

// ActivePublicKey exports the active public key in the given format
// (pem-pkcs8 | openssh | raw-base64). A missing active key reports
// domain.ErrNotFound.
func (s *Service) ActivePublicKey(format string) (string, error) {
	signer, err := s.signerOrErr()
	if err != nil {
		return "", err
	}
	return signer.ActivePublicKey(format)
}

// PublicKeyByID exports the public key bound to keyID in the given format
// (pem-pkcs8 | openssh | raw-base64). It resolves the key by id rather than by
// the active flag, so a reproduction of an order signed under a since-rotated
// key returns the exact public key that signed it. An unknown id reports
// domain.ErrNotFound.
func (s *Service) PublicKeyByID(ctx context.Context, keyID, format string) (string, error) {
	signer, err := s.signerOrErr()
	if err != nil {
		return "", err
	}
	return signer.PublicKeyByID(ctx, keyID, format)
}

// GetNoESign reports whether global eSign-off is enabled. With eSign-off on,
// approval tokens are emitted unsigned (alg "none").
func (s *Service) GetNoESign(ctx context.Context) (bool, error) {
	signer, err := s.signerOrErr()
	if err != nil {
		return false, err
	}
	return signer.NoESign(ctx)
}

// SetNoESign sets the global eSign-off flag and audits the action.
func (s *Service) SetNoESign(ctx context.Context, off bool) error {
	signer, err := s.signerOrErr()
	if err != nil {
		return err
	}
	if err := signer.SetNoESign(ctx, off); err != nil {
		return err
	}
	state := "enable"
	if off {
		state = "disable"
	}
	return s.auditSigning(ctx, domain.AuditActionSetSigningConfig,
		fmt.Sprintf("%s eSign", state))
}

// --- Submit / confirm / cancel ----------------------------------------------

// SubmitOrderToken runs the pre-trade pipeline for o in the given mode and, on
// accept, issues a signed (or eSign-off) approval token. mode "hold" keeps the
// reservation held until ConfirmExecution or CancelOrder resolves it; mode
// "immediate" commits and settles the fill at the engine lock price in the same
// call. A pre-trade reject returns the rejects as a validation error and issues
// no token. The issued token is audited as approval_issued.
func (s *Service) SubmitOrderToken(
	ctx context.Context, o domain.Order, mode string,
) (ApprovalToken, error) {
	signer, err := s.signerOrErr()
	if err != nil {
		return ApprovalToken{}, err
	}
	if mode == "" {
		mode = SubmitModeImmediate
	}
	if mode != SubmitModeHold && mode != SubmitModeImmediate {
		return ApprovalToken{}, fmt.Errorf("backend: submit mode %q: %w", mode, domain.ErrInvalid)
	}

	// Officer applies no boundary id/asset format checks; the engine seam parses
	// the account and assets and enforces the real trading rules.
	//
	// A caller-supplied order external id is used verbatim when valid: the order
	// is created exactly once below (submitHold/submitImmediate record it), and
	// the returned OrderExternalID is that created id, so confirm/cancel resolve
	// the same order. Surface layers parse the wire string before this boundary;
	// a duplicate id is rejected by the store with domain.ErrAlreadyExists.

	n, err := s.router.Route(keyFor(o.Account))
	if err != nil {
		return ApprovalToken{}, fmt.Errorf("backend: route submit token: %w", err)
	}
	caller := auth.CallerFromContext(ctx)
	key := keyFor(o.Account)
	payloadNonce, err := newNonce()
	if err != nil {
		return ApprovalToken{}, err
	}
	immediateApprovalID := ""
	if mode == SubmitModeImmediate {
		immediateApprovalID, err = newNonce()
		if err != nil {
			return ApprovalToken{}, err
		}
	}

	var (
		order          domain.Order
		approvalID     string
		settlement     string
		estimateSource string
		expiresAt      time.Time
		rejects        []domain.OrderReject
		accepted       bool
	)
	switch mode {
	case SubmitModeHold:
		var hold holdApproval
		order, hold, err = s.submitHold(ctx, n, key, o, caller)
		if err != nil {
			return ApprovalToken{}, err
		}
		accepted = order.Status != domain.OrderStatusRejected
		if accepted {
			approvalID = hold.approvalID
			settlement = hold.settlement
			estimateSource = hold.estimateSource
			expiresAt = hold.expiresAt
		} else {
			rejects = hold.rejects
		}
	default:
		order, settlement, estimateSource, accepted, rejects, err = s.submitImmediate(ctx, n, key, o, caller)
		if err != nil {
			return ApprovalToken{}, err
		}
		// Immediate has no held registry entry; use a fresh approval id and an
		// expiry equal to the token TTL from issue time.
		if accepted {
			approvalID = immediateApprovalID
		}
	}

	// Attest the recorded order's pre-trade verdict (accept or reject) onto its
	// verdict event, reusing the same attestSubmitVerdict path SubmitOrder uses.
	// The attestation is stamped write-once onto the pre_trade_accepted /
	// pre_trade_rejected event and surfaced by GetOrder as OrderEvent.Attestation,
	// so a panel order created through this token flow carries a signed verdict
	// event - the same attestation the like-the-API POST /orders path produces.
	// It is independent of the HTTP token below (the hold token the caller
	// confirms with) and best-effort: a build or persist failure records
	// attestation_failed and never fails the submit. This also stamps the
	// approval_issued audit for both verdicts, so no separate token-issue audit is
	// recorded below.
	s.attestSubmitVerdict(ctx, n, key, order)

	if !accepted {
		return ApprovalToken{}, rejectError(rejects)
	}

	issuedAt := time.Now().UTC()
	if expiresAt.IsZero() {
		expiresAt = issuedAt.Add(defaultTokenTTL)
	}

	payload := buildApprovalPayload(
		order, mode, approvalID, settlement, estimateSource, issuedAt, expiresAt, payloadNonce)

	off, err := signer.NoESign(ctx)
	if err != nil {
		return ApprovalToken{}, err
	}
	var token, keyID string
	signed := false
	if off {
		payload.Alg = fwsigning.AlgNone
		token, err = signer.SignNone(payload)
		if err != nil {
			return ApprovalToken{}, err
		}
	} else {
		token, err = signer.Sign(payload)
		if err != nil {
			return ApprovalToken{}, err
		}
		keys, kerr := signer.ListKeys(ctx)
		if kerr != nil {
			return ApprovalToken{}, kerr
		}
		keyID = activeKeyID(keys)
		signed = true
	}

	return ApprovalToken{
		ExpiresAt:       expiresAt,
		Token:           token,
		KeyID:           keyID,
		OrderExternalID: order.ExternalID.String(),
		Signed:          signed,
	}, nil
}

// ConfirmExecution verifies token against the stored order, then commits the
// held reservation it authorises. Verification recomputes the canonical bytes,
// checks the signature when signed, re-binds the bound params to the stored
// order, and enforces expiry. The conflict/terminal-order guard is not enforced
// here: it lives in node.ConfirmHeld, which re-reads the authoritative status
// inside the account lane (serialized with fills and reports). A second confirm
// of an already-committed order is an idempotent success there; a confirm of a
// cancelled or expired reservation is a conflict. The confirmation is audited as
// approval_confirmed.
func (s *Service) ConfirmExecution(
	ctx context.Context, orderID string, token string, force bool,
) (domain.Order, Attestation, error) {
	signer, err := s.signerOrErr()
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	order, err := domain.ParseExternalID(orderID)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	n, err := s.router.Route(keyFor(""))
	if err != nil {
		return domain.Order{}, Attestation{}, fmt.Errorf("backend: route confirm: %w", err)
	}

	stored, err := n.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	result, err := signer.Verify(ctx, token, verifyParamsFor(stored.Order))
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	if result.Payload.Mode != SubmitModeHold {
		return domain.Order{}, Attestation{}, fmt.Errorf(
			"backend: approval %s mode %q cannot be confirmed: %w",
			result.Payload.ApprovalID, result.Payload.Mode, domain.ErrConflict)
	}

	caller := auth.CallerFromContext(ctx)
	confirmed, forcedBypass, err := n.ConfirmHeld(
		ctx, order, result.Payload.ApprovalID, caller, force)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	key := keyFor(confirmed.Account)
	detail := fmt.Sprintf(
		"confirm approval %s order %s", result.Payload.ApprovalID, orderID)
	// forced=true is audited only when force actually bypassed a terminal-order
	// guard, matching the execution-report path; a force flag that changed nothing
	// is not recorded as a forced bypass.
	if forcedBypass {
		detail += " forced=true"
	}
	_ = s.auditApproval(ctx, n, key, domain.AuditActionApprovalConfirmed, detail)
	att := s.attestResolution(
		ctx, n, key, confirmed, domain.AttestationRequestConfirm,
		"committed", "", result.Payload.ApprovalID)
	return confirmed, att, nil
}

// CancelOrder verifies token against the stored order, then rolls back the held
// reservation it authorises, returning the held amount to available. The
// conflict/terminal-order guard is not enforced here: it lives in
// node.CancelHeld, which re-reads the authoritative status inside the account
// lane (serialized with fills and reports). Rollback is idempotent: cancelling an
// already-resolved reservation is a no-op on the engine side and still records
// the cancelled lifecycle. The cancellation is audited as approval_cancelled,
// with reason.
func (s *Service) CancelOrder(
	ctx context.Context, orderID string, token, reason string, force bool,
) (domain.Order, Attestation, error) {
	signer, err := s.signerOrErr()
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	order, err := domain.ParseExternalID(orderID)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	n, err := s.router.Route(keyFor(""))
	if err != nil {
		return domain.Order{}, Attestation{}, fmt.Errorf("backend: route cancel: %w", err)
	}

	stored, err := n.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	result, err := signer.Verify(ctx, token, verifyParamsFor(stored.Order))
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	if result.Payload.Mode != SubmitModeHold {
		return domain.Order{}, Attestation{}, fmt.Errorf(
			"backend: approval %s mode %q cannot be cancelled: %w",
			result.Payload.ApprovalID, result.Payload.Mode, domain.ErrConflict)
	}

	caller := auth.CallerFromContext(ctx)
	cancelled, forcedBypass, err := n.CancelHeld(
		ctx, order, result.Payload.ApprovalID, caller, force)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	key := keyFor(cancelled.Account)
	detail := fmt.Sprintf(
		"cancel approval %s order %s reason=%s",
		result.Payload.ApprovalID, orderID, reason)
	// forced=true is audited only when force actually bypassed a terminal-order
	// guard, matching the execution-report path.
	if forcedBypass {
		detail += " forced=true"
	}
	_ = s.auditApproval(ctx, n, key, domain.AuditActionApprovalCancelled, detail)
	att := s.attestResolution(
		ctx, n, key, cancelled, domain.AttestationRequestCancel,
		"rolled_back", reason, result.Payload.ApprovalID)
	return cancelled, att, nil
}

// defaultTokenTTL is how long an issued approval token stays valid. For a hold
// it equals the engine hold TTL (the engine returns the authoritative expiry);
// for an immediate token it bounds the confirmation window the connector honours.
const defaultTokenTTL = 120 * time.Second

// holdApproval bundles the engine hold-result fields the submit flow surfaces.
type holdApproval struct {
	expiresAt      time.Time
	approvalID     string
	settlement     string
	estimateSource string
	rejects        []domain.OrderReject
}

// submitHold records the order, runs the engine hold through the node, and
// returns the recorded order plus the hold approval fields. On reject the
// approval fields are zero and rejects carries the engine rejects.
func (s *Service) submitHold(
	ctx context.Context, n node.Node, key node.Key, o domain.Order, caller domain.Caller,
) (domain.Order, holdApproval, error) {
	order, result, err := n.SubmitHold(ctx, key, o, caller)
	if err != nil {
		return domain.Order{}, holdApproval{}, fmt.Errorf("backend: submit hold: %w", err)
	}
	if !result.Accepted {
		return order, holdApproval{rejects: result.Rejects}, nil
	}
	return order, holdApproval{
		expiresAt:      result.ExpiresAt,
		approvalID:     result.ApprovalID,
		settlement:     result.SettlementLockPrice,
		estimateSource: result.EstimateSource,
	}, nil
}

// submitImmediate records the order, runs the engine immediate settlement
// through the node, and returns the recorded order plus the settlement fields.
func (s *Service) submitImmediate(
	ctx context.Context, n node.Node, key node.Key, o domain.Order, caller domain.Caller,
) (domain.Order, string, string, bool, []domain.OrderReject, error) {
	order, result, err := n.SubmitImmediate(ctx, key, o, caller)
	if err != nil {
		return domain.Order{}, "", "", false, nil, fmt.Errorf("backend: submit immediate: %w", err)
	}
	if !result.Accepted {
		return order, "", "", false, result.Rejects, nil
	}
	return order, result.SettlementLockPrice, result.EstimateSource, true, nil, nil
}

// --- helpers ----------------------------------------------------------------

// buildApprovalPayload assembles the canonical approval payload from the
// recorded order, the engine estimate, and the lifecycle timestamps. KeyID and
// Alg are set by the signer; the rest are bound here.
func buildApprovalPayload(
	order domain.Order, mode, approvalID, settlement, estimateSource string,
	issuedAt, expiresAt time.Time, nonce string,
) domain.ApprovalPayload {
	orderType := "market"
	if order.Price != "" {
		orderType = "limit"
	}
	return domain.ApprovalPayload{
		Version:         approvalPayloadVersion,
		ApprovalID:      approvalID,
		ReservationID:   approvalID,
		Mode:            mode,
		OrderExternalID: orderExternalID(order),
		Instrument:      order.BaseAsset + "/" + order.QuoteAsset,
		Side:            string(order.Side),
		Quantity:        order.AmountValue,
		AmountKind:      string(order.AmountKind),
		OrderType:       orderType,
		LimitPrice:      order.Price,
		PriceCurrency:   order.QuoteAsset,
		AccountID:       string(order.Account),
		Verdict:         "accept",
		PolicySummary:   "accepted",
		EstimatePrice:   settlement,
		EstimateSource:  estimateSource,
		IssuedAt:        issuedAt.Format(time.RFC3339Nano),
		ExpiresAt:       expiresAt.Format(time.RFC3339Nano),
		Nonce:           nonce,
	}
}

// orderExternalID renders the order's opaque public handle for an approval
// payload, never a surrogate or engine id. A zero external id (an in-memory-only
// held reservation signed before an order row exists) maps to the empty string so
// the canonical payload omits the order handle entirely.
func orderExternalID(order domain.Order) string {
	if order.ExternalID.IsZero() {
		return ""
	}
	return order.ExternalID.String()
}

// buildRejectApprovalPayload assembles the canonical approval payload for a
// rejected pre-trade verdict. It binds the same order params as the accept path
// (so the envelope re-binds against the order it records), carries an empty
// estimate, and stamps the first engine reject onto the Reject* fields.
func buildRejectApprovalPayload(
	order domain.Order, mode, approvalID string, reject domain.OrderReject,
	issuedAt, expiresAt time.Time, nonce string,
) domain.ApprovalPayload {
	orderType := "market"
	if order.Price != "" {
		orderType = "limit"
	}
	return domain.ApprovalPayload{
		Version:         approvalPayloadVersion,
		ApprovalID:      approvalID,
		ReservationID:   approvalID,
		Mode:            mode,
		OrderExternalID: orderExternalID(order),
		Instrument:      order.BaseAsset + "/" + order.QuoteAsset,
		Side:            string(order.Side),
		Quantity:        order.AmountValue,
		AmountKind:      string(order.AmountKind),
		OrderType:       orderType,
		LimitPrice:      order.Price,
		PriceCurrency:   order.QuoteAsset,
		AccountID:       string(order.Account),
		Verdict:         "reject",
		PolicySummary:   "rejected",
		EstimatePrice:   "",
		IssuedAt:        issuedAt.Format(time.RFC3339Nano),
		ExpiresAt:       expiresAt.Format(time.RFC3339Nano),
		Nonce:           nonce,
		RejectCode:      reject.Code,
		RejectScope:     reject.Scope,
		RejectPolicy:    reject.Policy,
		RejectReason:    reject.Reason,
	}
}

// baseAttestationPayload assembles the request-neutral core of an attestation
// payload: the bound order params, lifecycle timestamps, and a fresh
// approvalId/nonce. Callers stamp the verdict/result and request-specific fields.
// RequestType and EventExternalID are set later by attestEvent. It is the shared
// core the report/confirm/cancel builders extend, mirroring buildApprovalPayload.
func baseAttestationPayload(
	order domain.Order, mode string, issuedAt, expiresAt time.Time,
	approvalID, nonce string,
) domain.ApprovalPayload {
	orderType := "market"
	if order.Price != "" {
		orderType = "limit"
	}
	return domain.ApprovalPayload{
		Version:         approvalPayloadVersion,
		ApprovalID:      approvalID,
		ReservationID:   approvalID,
		Mode:            mode,
		OrderExternalID: orderExternalID(order),
		Instrument:      order.BaseAsset + "/" + order.QuoteAsset,
		Side:            string(order.Side),
		Quantity:        order.AmountValue,
		AmountKind:      string(order.AmountKind),
		OrderType:       orderType,
		LimitPrice:      order.Price,
		PriceCurrency:   order.QuoteAsset,
		AccountID:       string(order.Account),
		IssuedAt:        issuedAt.Format(time.RFC3339Nano),
		ExpiresAt:       expiresAt.Format(time.RFC3339Nano),
		Nonce:           nonce,
		Principal:       order.Principal,
	}
}

// buildExecutionReportPayload assembles the attestation payload for an execution
// report: the bound order params plus the engine persistence section (settled fill,
// leaves, target status, and any account block the engine recorded). Verdict is
// "accept" (the report was applied); a report that produced an account block
// carries the first block onto the reject fields too, mirroring the fill event.
func (s *Service) buildExecutionReportPayload(
	order domain.Order,
	persistence engine.ExecutionReportPersistence,
) (domain.ApprovalPayload, error) {
	approvalID, err := newNonce()
	if err != nil {
		return domain.ApprovalPayload{}, err
	}
	nonce, err := newNonce()
	if err != nil {
		return domain.ApprovalPayload{}, err
	}
	issuedAt := time.Now().UTC()
	expiresAt := issuedAt.Add(defaultTokenTTL)

	payload := baseAttestationPayload(
		order, SubmitModeImmediate, issuedAt, expiresAt, approvalID, nonce)
	payload.Verdict = "accept"
	payload.PolicySummary = "execution report applied"
	fillQuantity := ""
	fillPrice := ""
	fillLockPrice := ""
	if persistence.Trade != nil {
		fillQuantity = persistence.Trade.Quantity
		fillPrice = persistence.Trade.Price
		fillLockPrice = persistence.Trade.LockPrice
	}
	payload.Result = &domain.AttestationResult{
		Outcome:        "applied",
		FillQuantity:   fillQuantity,
		FillPrice:      fillPrice,
		FillLockPrice:  fillLockPrice,
		LeavesQuantity: persistence.Leaves,
		OrderStatus:    string(persistence.OrderStatus),
		Blocks:         attestationBlocks(persistence.Blocks),
	}
	if len(persistence.Blocks) > 0 {
		b := persistence.Blocks[0]
		payload.RejectCode = b.Code
		payload.RejectScope = "account"
		payload.RejectReason = b.Reason
	}
	return payload, nil
}

// buildResolutionPayload assembles the attestation payload for a held-reservation
// resolution (confirm commit or cancel rollback): the bound order params plus the
// engine result section carrying the coarse outcome, the resulting status, and
// the reservation approval id. reason, when present, is bound onto RejectReason.
func (s *Service) buildResolutionPayload(
	order domain.Order,
	requestType domain.AttestationRequestType,
	outcome, reason, approvalRef string,
) (domain.ApprovalPayload, error) {
	approvalID, err := newNonce()
	if err != nil {
		return domain.ApprovalPayload{}, err
	}
	nonce, err := newNonce()
	if err != nil {
		return domain.ApprovalPayload{}, err
	}
	issuedAt := time.Now().UTC()
	expiresAt := issuedAt.Add(defaultTokenTTL)

	payload := baseAttestationPayload(
		order, SubmitModeHold, issuedAt, expiresAt, approvalID, nonce)
	payload.Verdict = "accept"
	payload.ReservationID = approvalRef
	summary := "reservation committed"
	if requestType == domain.AttestationRequestCancel {
		summary = "reservation rolled back"
	}
	payload.PolicySummary = summary
	payload.Result = &domain.AttestationResult{
		Outcome:     outcome,
		OrderStatus: string(order.Status),
		Blocks:      []domain.AttestationBlock{},
	}
	if reason != "" {
		payload.RejectReason = reason
	}
	return payload, nil
}

// attestationBlocks maps engine execution blocks onto the signed-payload block
// shape. It returns a non-nil (possibly empty) slice so the canonical bytes stay
// deterministic (the Result.Blocks field is always present when Result is).
func attestationBlocks(blocks []domain.ExecutionAccountBlock) []domain.AttestationBlock {
	out := make([]domain.AttestationBlock, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, domain.AttestationBlock{
			Account: string(b.Account),
			Code:    b.Code,
			Reason:  b.Reason,
			Details: b.Details,
		})
	}
	return out
}

// verifyParamsFor builds the re-bind expectation from the stored order. The
// connector contract compares each bound param to the order it will execute; the
// backend re-binds against the persisted order the token authorises.
func verifyParamsFor(order domain.Order) fwsigning.VerifyParams {
	orderType := "market"
	if order.Price != "" {
		orderType = "limit"
	}
	return fwsigning.VerifyParams{
		OrderExternalID: orderExternalID(order),
		Instrument:      order.BaseAsset + "/" + order.QuoteAsset,
		Side:            string(order.Side),
		Quantity:        order.AmountValue,
		AmountKind:      string(order.AmountKind),
		OrderType:       orderType,
		LimitPrice:      order.Price,
		PriceCurrency:   order.QuoteAsset,
		AccountID:       string(order.Account),
	}
}

// newNonce returns a single-use 128-bit base64url nonce.
func newNonce() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("backend: generate approval nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// activeKeyID returns the active key id from a key list, or empty when none.
func activeKeyID(keys []domain.SigningKey) string {
	for _, k := range keys {
		if k.Active {
			return k.KeyID
		}
	}
	return ""
}

// rejectError wraps the first pre-trade reject as a validation error so the
// surface maps it onto a 4xx. A reject is a successful pre-trade run that issues
// no token.
func rejectError(rejects []domain.OrderReject) error {
	if len(rejects) == 0 {
		return fmt.Errorf("backend: order rejected: %w", domain.ErrInvalid)
	}
	r := rejects[0]
	return fmt.Errorf("backend: order rejected: %s %s: %w", r.Code, r.Reason, domain.ErrInvalid)
}

// auditSigning records a signing-key/config action via the group node, the same
// AppendAudit path used by MCP-access writes.
func (s *Service) auditSigning(ctx context.Context, action domain.AuditAction, detail string) error {
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	if err := n.AppendAudit(ctx, store.AuditEntry{Action: action, Detail: detail},
		auth.CallerFromContext(ctx)); err != nil {
		return fmt.Errorf("backend: audit %s: %w", action, err)
	}
	return nil
}

// auditApproval records an approval issue/confirm/cancel action on the given
// node, stamped with the account the token authorises.
func (s *Service) auditApproval(
	ctx context.Context, n node.Node, key node.Key, action domain.AuditAction, detail string,
) error {
	if err := n.AppendAudit(ctx, store.AuditEntry{
		Action:  action,
		Account: key.Account,
		Detail:  detail,
	}, auth.CallerFromContext(ctx)); err != nil {
		return fmt.Errorf("backend: audit %s: %w", action, err)
	}
	return nil
}

// --- Event attestation (shared across submit / report / confirm / cancel) ---

// Attestation is the backend-facing result of stamping a signed attestation onto
// an order-history event. Surface layers return Token to the caller so a robot
// receives its proof. Token is the base64url-encoded signed (or eSign-off)
// envelope; KeyID is the signing key id (empty under eSign-off); Signed reports
// whether the envelope carries a signature. It is the zero value (Token empty)
// when attestation was skipped or failed best-effort.
type Attestation struct {
	// Token is the base64url-encoded attestation envelope; empty when none.
	Token string
	// KeyID is the signing key id; empty under eSign-off or when none.
	KeyID string
	// EventExternalID is the opaque handle of the attested order-history event.
	EventExternalID string
	// Signed reports whether the envelope carries an Ed25519 signature.
	Signed bool
}

// attestEvent signs payload, binds it to the newest event of eventType on the
// order, and stamps the attestation onto that event write-once. It re-reads the
// order to resolve the event id and its request-type, signs (or SignNone under
// eSign-off), persists via the node, and audits approval_issued. It returns the
// resulting Attestation. It errors only on signing/persist failures; callers on
// the best-effort paths swallow the error and record attestation_failed.
func (s *Service) attestEvent(
	ctx context.Context,
	n node.Node,
	key node.Key,
	order domain.ExternalID,
	eventType domain.OrderEventType,
	requestType domain.AttestationRequestType,
	payload domain.ApprovalPayload,
) (Attestation, error) {
	signer, err := s.signerOrErr()
	if err != nil {
		return Attestation{}, err
	}
	event, err := s.latestEventOfType(ctx, n, order, eventType)
	if err != nil {
		return Attestation{}, err
	}
	payload.RequestType = string(requestType)
	payload.EventExternalID = event.ExternalID.String()

	off, err := signer.NoESign(ctx)
	if err != nil {
		return Attestation{}, err
	}
	var token, keyID string
	signed := false
	if off {
		payload.Alg = fwsigning.AlgNone
		token, err = signer.SignNone(payload)
		if err != nil {
			return Attestation{}, err
		}
	} else {
		token, err = signer.Sign(payload)
		if err != nil {
			return Attestation{}, err
		}
		keys, kerr := signer.ListKeys(ctx)
		if kerr != nil {
			return Attestation{}, kerr
		}
		keyID = activeKeyID(keys)
		payload.Alg = fwsigning.AlgEd25519
		signed = true
	}
	att := domain.EventAttestation{
		Token:       token,
		KeyID:       keyID,
		Alg:         payload.Alg,
		RequestType: requestType,
		Mode:        payload.Mode,
		IssuedAt:    payload.IssuedAt,
		ExpiresAt:   payload.ExpiresAt,
	}
	if err := n.PersistEventAttestation(ctx, key, event.ExternalID, att); err != nil {
		return Attestation{}, err
	}
	_ = s.auditApproval(ctx, n, key, domain.AuditActionApprovalIssued,
		fmt.Sprintf("issue attestation order %s event %s request=%s",
			order.String(), event.ExternalID.String(), requestType))
	return Attestation{
		Token:           token,
		KeyID:           keyID,
		EventExternalID: event.ExternalID.String(),
		Signed:          signed,
	}, nil
}

// latestEventOfType returns the newest event of eventType for the order. Events
// come back oldest-first; the last match is the one the just-completed request
// produced. A missing event is a real error: the request-produced event must
// exist for a 1:1 attestation.
func (s *Service) latestEventOfType(
	ctx context.Context, n node.Node, order domain.ExternalID, eventType domain.OrderEventType,
) (domain.OrderEvent, error) {
	detail, err := n.GetOrder(ctx, order)
	if err != nil {
		return domain.OrderEvent{}, err
	}
	var found *domain.OrderEvent
	for i := range detail.Events {
		if detail.Events[i].Type == eventType {
			found = &detail.Events[i]
		}
	}
	if found == nil {
		return domain.OrderEvent{}, fmt.Errorf(
			"backend: no %s event on order %s to attest: %w",
			eventType, order.String(), domain.ErrNotFound)
	}
	return *found, nil
}

// attestResolution signs an attestation over a held-reservation resolution
// (confirm commit or cancel rollback) and stamps it onto the resolution's
// terminal event (reservation_committed for confirm, cancelled for cancel). It
// is best-effort: on failure it records attestation_failed and returns a zero
// Attestation.
func (s *Service) attestResolution(
	ctx context.Context,
	n node.Node,
	key node.Key,
	order domain.Order,
	requestType domain.AttestationRequestType,
	outcome, reason, approvalRef string,
) Attestation {
	payload, err := s.buildResolutionPayload(order, requestType, outcome, reason, approvalRef)
	if err != nil {
		s.reportAttestationFailure(ctx, n, key, order, requestType, err)
		return Attestation{}
	}
	eventType := domain.OrderEventReservationCommitted
	if requestType == domain.AttestationRequestCancel {
		eventType = domain.OrderEventCancelled
	}
	att, err := s.attestEvent(ctx, n, key, order.ExternalID, eventType, requestType, payload)
	if err != nil {
		s.reportAttestationFailure(ctx, n, key, order, requestType, err)
		return Attestation{}
	}
	return att
}
