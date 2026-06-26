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
	"errors"
	"fmt"
	"time"

	"go.openpit.dev/officer/internal/auth"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/node"
	"go.openpit.dev/officer/internal/signing"
	"go.openpit.dev/officer/internal/store"
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
// signing and approval surfaces require a signer; a nil signer means the feature
// was not wired (defensive: production always injects one).
func (s *Service) signerOrErr() (SigningService, error) {
	if s.signer == nil {
		return nil, fmt.Errorf("backend: signing service not configured: %w", domain.ErrNotImplemented)
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
		payload.Alg = signing.AlgNone
		token, err = signing.SignNone(payload)
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

	_ = s.auditApproval(ctx, n, key, domain.AuditActionApprovalIssued,
		fmt.Sprintf("issue approval %s order %s mode=%s",
			approvalID, order.ExternalID.String(), mode))

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
// order, and enforces expiry. A second confirm of an already-committed order is
// an idempotent success; a confirm of a cancelled or expired reservation is a
// conflict. The confirmation is audited as approval_confirmed.
func (s *Service) ConfirmExecution(
	ctx context.Context, orderID string, token string, force bool,
) (domain.Order, error) {
	signer, err := s.signerOrErr()
	if err != nil {
		return domain.Order{}, err
	}
	order, err := domain.ParseExternalID(orderID)
	if err != nil {
		return domain.Order{}, err
	}
	n, err := s.router.Route(keyFor(""))
	if err != nil {
		return domain.Order{}, fmt.Errorf("backend: route confirm: %w", err)
	}

	stored, err := n.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, err
	}
	switch stored.Order.Status {
	case domain.OrderStatusCommitted:
		return stored.Order, nil
	case domain.OrderStatusAccepted:
	default:
		if domain.OrderStatusTerminal(stored.Order.Status) {
			if !force {
				return domain.Order{}, fmt.Errorf(
					"backend: order %s is in terminal status %q: %w",
					orderID, stored.Order.Status, domain.ErrTerminalOrder)
			}
			break
		}
		return domain.Order{}, fmt.Errorf(
			"backend: order %s status %q cannot confirm held approval: %w",
			orderID, stored.Order.Status, domain.ErrConflict)
	}
	result, err := signer.Verify(ctx, token, verifyParamsFor(stored.Order))
	if err != nil {
		return domain.Order{}, err
	}
	if result.Payload.Mode != SubmitModeHold {
		return domain.Order{}, fmt.Errorf(
			"backend: approval %s mode %q cannot be confirmed: %w",
			result.Payload.ApprovalID, result.Payload.Mode, domain.ErrConflict)
	}

	caller := auth.CallerFromContext(ctx)
	confirmed, err := n.ConfirmHeld(
		ctx, order, result.Payload.ApprovalID, caller, force)
	if err != nil {
		// The engine guards the double-commit panic by returning a conflict on an
		// already-resolved reservation. A confirm of an already-committed order is
		// an idempotent success; any other terminal state is a real conflict.
		if errors.Is(err, domain.ErrConflict) {
			if stored.Order.Status == domain.OrderStatusCommitted {
				return stored.Order, nil
			}
		}
		return domain.Order{}, err
	}
	detail := fmt.Sprintf(
		"confirm approval %s order %s", result.Payload.ApprovalID, orderID)
	if force && domain.OrderStatusTerminal(stored.Order.Status) {
		detail += " forced=true"
	}
	_ = s.auditApproval(
		ctx, n, keyFor(confirmed.Account), domain.AuditActionApprovalConfirmed, detail)
	return confirmed, nil
}

// CancelOrder verifies token against the stored order, then rolls back the held
// reservation it authorises, returning the held amount to available. Rollback is
// idempotent: cancelling an already-resolved reservation is a no-op on the
// engine side and still records the cancelled lifecycle. The cancellation is
// audited as approval_cancelled, with reason.
func (s *Service) CancelOrder(
	ctx context.Context, orderID string, token, reason string, force bool,
) (domain.Order, error) {
	signer, err := s.signerOrErr()
	if err != nil {
		return domain.Order{}, err
	}
	order, err := domain.ParseExternalID(orderID)
	if err != nil {
		return domain.Order{}, err
	}
	n, err := s.router.Route(keyFor(""))
	if err != nil {
		return domain.Order{}, fmt.Errorf("backend: route cancel: %w", err)
	}

	stored, err := n.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, err
	}
	if stored.Order.Status != domain.OrderStatusAccepted {
		if domain.OrderStatusTerminal(stored.Order.Status) {
			if !force {
				return domain.Order{}, fmt.Errorf(
					"backend: order %s is in terminal status %q: %w",
					orderID, stored.Order.Status, domain.ErrTerminalOrder)
			}
		} else {
			return domain.Order{}, fmt.Errorf(
				"backend: order %s status %q cannot cancel held approval: %w",
				orderID, stored.Order.Status, domain.ErrConflict)
		}
	}
	result, err := signer.Verify(ctx, token, verifyParamsFor(stored.Order))
	if err != nil {
		return domain.Order{}, err
	}
	if result.Payload.Mode != SubmitModeHold {
		return domain.Order{}, fmt.Errorf(
			"backend: approval %s mode %q cannot be cancelled: %w",
			result.Payload.ApprovalID, result.Payload.Mode, domain.ErrConflict)
	}

	caller := auth.CallerFromContext(ctx)
	cancelled, err := n.CancelHeld(
		ctx, order, result.Payload.ApprovalID, caller, force)
	if err != nil {
		return domain.Order{}, err
	}
	detail := fmt.Sprintf(
		"cancel approval %s order %s reason=%s",
		result.Payload.ApprovalID, orderID, reason)
	if force && domain.OrderStatusTerminal(stored.Order.Status) {
		detail += " forced=true"
	}
	_ = s.auditApproval(
		ctx, n, keyFor(cancelled.Account), domain.AuditActionApprovalCancelled, detail)
	return cancelled, nil
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

// verifyParamsFor builds the re-bind expectation from the stored order. The
// connector contract compares each bound param to the order it will execute; the
// backend re-binds against the persisted order the token authorises.
func verifyParamsFor(order domain.Order) signing.VerifyParams {
	orderType := "market"
	if order.Price != "" {
		orderType = "limit"
	}
	return signing.VerifyParams{
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
