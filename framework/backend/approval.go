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
	"log/slog"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
)

// Submit modes for an approval token. SubmitModeImmediate commits and settles
// at the lock price; SubmitModeHold is the workflow submit, whose later state
// changes arrive as execution reports.
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
	// Token is the base64url-encoded approval envelope.
	Token string
	// KeyID is the signing key id; empty under eSign-off.
	KeyID string
	// OrderExternalID is the opaque public handle of the recorded order the token
	// authorises. It is the order's external id, never a surrogate or engine id.
	OrderExternalID string
	// Verdict is the pre-trade decision carried by the signed envelope.
	Verdict string
	// Reasons carries the engine reject reasons when Verdict is "reject".
	Reasons []domain.OrderReject
	// Signed reports whether the envelope carries an Ed25519 signature.
	Signed bool
}

type eventPayloadBuilder func(domain.OrderEvent) (domain.ApprovalPayload, bool, error)

type signedEventCapture func(Attestation, domain.ApprovalPayload)

// signerOrErr returns the configured signer or an unconfigured error. The
// signing and approval surfaces require a signer; nil or unavailable signing
// means the feature was not wired.
func (s *Service) signerOrErr() (fwsigning.Service, error) {
	if fwsigning.IsUnavailable(s.signer) {
		return nil, fwsigning.ErrNotConfigured
	}
	return s.signer, nil
}

func missingAttestationError(requestType domain.AttestationRequestType) error {
	return fmt.Errorf("backend: %s attestation missing", requestType)
}

func persistedEventAttestation(
	ctx context.Context,
	n node.Node,
	order domain.ExternalID,
	eventType domain.OrderEventType,
) (Attestation, bool, error) {
	detail, err := n.GetOrder(ctx, order)
	if err != nil {
		return Attestation{}, false, err
	}
	for i := len(detail.Events) - 1; i >= 0; i-- {
		event := detail.Events[i]
		if event.Order != order || event.Type != eventType {
			continue
		}
		if event.Attestation == nil || event.Attestation.Token == "" {
			continue
		}
		return Attestation{
			Token:           event.Attestation.Token,
			KeyID:           event.Attestation.KeyID,
			EventExternalID: event.ExternalID.String(),
			Signed:          event.Attestation.Alg != fwsigning.AlgNone,
		}, true, nil
	}
	return Attestation{}, false, nil
}

func signingMode(
	ctx context.Context, signer fwsigning.Service,
) (bool, string, error) {
	off, err := signer.NoESign(ctx)
	if err != nil {
		return false, "", err
	}
	if off {
		return true, "", nil
	}
	keys, err := signer.ListKeys(ctx)
	if err != nil {
		return false, "", err
	}
	keyID := activeKeyID(keys)
	if keyID == "" {
		return false, "", fmt.Errorf("signing: no active key: %w", domain.ErrNotFound)
	}
	return false, keyID, nil
}

func eventAttestor(
	signer fwsigning.Service,
	off bool,
	keyID string,
	requestType domain.AttestationRequestType,
	build eventPayloadBuilder,
	capture signedEventCapture,
) store.EventAttestor {
	return func(
		ctx context.Context, event domain.OrderEvent,
	) (domain.EventAttestation, bool, error) {
		payload, ok, err := build(event)
		if err != nil || !ok {
			return domain.EventAttestation{}, false, err
		}
		actualRequestType := requestType
		if payload.RequestType != "" {
			actualRequestType = domain.AttestationRequestType(payload.RequestType)
		}
		payload.RequestType = string(actualRequestType)
		payload.EventExternalID = event.ExternalID.String()
		var token string
		signed := false
		if off {
			payload.KeyID = ""
			payload.Alg = fwsigning.AlgNone
			token, err = signer.SignNone(payload)
		} else {
			payload.KeyID = keyID
			payload.Alg = fwsigning.AlgEd25519
			token, err = signer.Sign(payload)
			signed = true
		}
		if err != nil {
			return domain.EventAttestation{}, false, err
		}
		att := domain.EventAttestation{
			Token:       token,
			KeyID:       payload.KeyID,
			Alg:         payload.Alg,
			RequestType: requestType,
			Mode:        payload.Mode,
			IssuedAt:    payload.IssuedAt,
		}
		if capture != nil {
			capture(Attestation{
				Token:           token,
				KeyID:           payload.KeyID,
				EventExternalID: event.ExternalID.String(),
				Signed:          signed,
			}, payload)
		}
		att.RequestType = actualRequestType
		return att, true, nil
	}
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
// accept, issues a signed (or eSign-off) approval token. Mode "hold" uses the
// normal workflow submit: the engine commits the pre-trade effects and later
// execution reports settle or release them. Mode "immediate" also settles a
// fill at the engine lock price in the same call. A pre-trade reject is a
// successful signed decision and returns the reject reasons with the same
// envelope shape as an accept verdict. The post-commit approval_issued audit
// write is best-effort: because the operation is already committed, a failure
// is logged rather than returned. missing is the caller's required choice for
// an order naming
// an account that does not exist yet: register it with no group and no currency,
// or fail with domain.ErrAccountMissing.
func (s *Service) SubmitOrderToken(
	ctx context.Context,
	o domain.Order,
	mode string,
	missing domain.MissingAccountPolicy,
) (ApprovalToken, error) {
	if isDropCopyOrder(o) {
		return ApprovalToken{}, dropCopySigningError()
	}
	return s.submitOrderToken(ctx, o, mode, missing)
}

func (s *Service) submitOrderToken(
	ctx context.Context,
	o domain.Order,
	mode string,
	missing domain.MissingAccountPolicy,
) (ApprovalToken, error) {
	// Malformed identity is refused here, not only at a surface: every caller -
	// HTTP, MCP, or an embedder - reaches the engine lane through this seam, and
	// an account registered under an unaddressable code could never be read back.
	if err := domain.ValidateAccountID(o.Account); err != nil {
		return ApprovalToken{}, err
	}
	if err := validateMissingAccountPolicy(o.Account, missing); err != nil {
		return ApprovalToken{}, err
	}
	signer, err := s.signerOrErr()
	if err != nil {
		return ApprovalToken{}, err
	}
	off, keyID, err := signingMode(ctx, signer)
	if err != nil {
		return ApprovalToken{}, err
	}
	if mode == "" {
		return ApprovalToken{}, fmt.Errorf("backend: submit mode is required: %w", domain.ErrInvalid)
	}
	if mode != SubmitModeHold && mode != SubmitModeImmediate {
		return ApprovalToken{}, fmt.Errorf("backend: submit mode %q: %w", mode, domain.ErrInvalid)
	}

	// The account id was validated above, before the engine lane is entered. The
	// engine seam parses asset identifiers and enforces the trading rules.
	//
	// A caller-supplied order external id is used verbatim when valid: the order
	// is created exactly once below (submitOrder/submitImmediate record it), and
	// the returned OrderExternalID is that created id, so confirm/cancel resolve
	// the same order. Surface layers parse the wire string before this boundary;
	// a duplicate id is rejected by the store with domain.ErrAlreadyExists.

	n := s.node
	caller := auth.CallerFromContext(ctx)
	submitApprovalID, err := newNonce()
	if err != nil {
		return ApprovalToken{}, err
	}

	var (
		order    domain.Order
		rejects  []domain.OrderReject
		verdict  string
		issued   Attestation
		payload  domain.ApprovalPayload
		accepted bool
	)
	switch mode {
	case SubmitModeHold:
		var result engine.OrderResult
		attestFor := func(
			persisted domain.Order, submitted engine.OrderResult,
		) store.EventAttestor {
			return eventAttestor(
				signer, off, keyID, domain.AttestationRequestSubmit,
				func(event domain.OrderEvent) (domain.ApprovalPayload, bool, error) {
					eventOrder := orderForEvent(persisted, o.Account, event, caller)
					switch event.Type {
					case domain.OrderEventSubmitted:
						p, err := buildSubmittedPayload(
							eventOrder, SubmitModeHold, submitApprovalID)
						return p, err == nil, err
					case domain.OrderEventPreTradeAccepted:
						nonce, err := newNonce()
						if err != nil {
							return domain.ApprovalPayload{}, false, err
						}
						return buildApprovalPayload(
							eventOrder,
							SubmitModeHold,
							submitApprovalID,
							submitted.SettlementLockPrice,
							time.Now().UTC(),
							nonce,
						), true, nil
					case domain.OrderEventPreTradeRejected:
						nonce, err := newNonce()
						if err != nil {
							return domain.ApprovalPayload{}, false, err
						}
						return buildRejectApprovalPayload(
							eventOrder, SubmitModeHold, submitApprovalID,
							submitted.Rejects, time.Now().UTC(), nonce,
						), true, nil
					case domain.OrderEventCommitted:
						committed := eventOrder
						committed.Status = domain.OrderStatusCommitted
						p, err := s.buildLifecyclePayload(
							committed, domain.AttestationRequestSubmit,
							"committed", "", submitApprovalID,
						)
						return p, err == nil, err
					default:
						return domain.ApprovalPayload{}, false, nil
					}
				},
				func(att Attestation, p domain.ApprovalPayload) {
					if p.Result == nil && p.Verdict != "" {
						issued = att
						payload = p
					}
				},
			)
		}
		order, result, err = n.SubmitOrderWithAttestation(
			ctx, o, missing, caller, attestFor)
		if err != nil {
			if auditErr := s.auditApproval(
				ctx, n, o.Account, domain.AuditActionApprovalFailed,
				fmt.Sprintf("submit attestation failed: %v", err),
			); auditErr != nil {
				return ApprovalToken{}, errors.Join(err, auditErr)
			}
			return ApprovalToken{}, err
		}
		accepted = result.Accepted
		rejects = result.Rejects
	default:
		var result engine.ImmediateResult
		attestFor := func(
			persisted domain.Order, immediate engine.ImmediateResult,
		) store.EventAttestor {
			return eventAttestor(
				signer, off, keyID, domain.AttestationRequestSubmit,
				func(event domain.OrderEvent) (domain.ApprovalPayload, bool, error) {
					eventOrder := orderForEvent(persisted, o.Account, event, caller)
					switch event.Type {
					case domain.OrderEventSubmitted:
						p, err := buildSubmittedPayload(
							eventOrder, SubmitModeImmediate, submitApprovalID)
						return p, err == nil, err
					case domain.OrderEventPreTradeAccepted:
						nonce, err := newNonce()
						if err != nil {
							return domain.ApprovalPayload{}, false, err
						}
						return buildApprovalPayload(
							eventOrder,
							SubmitModeImmediate,
							submitApprovalID,
							immediate.SettlementLockPrice,
							time.Now().UTC(),
							nonce,
						), true, nil
					case domain.OrderEventPreTradeRejected:
						nonce, err := newNonce()
						if err != nil {
							return domain.ApprovalPayload{}, false, err
						}
						return buildRejectApprovalPayload(
							eventOrder, SubmitModeImmediate, submitApprovalID,
							immediate.Rejects, time.Now().UTC(), nonce), true, nil
					case domain.OrderEventCommitted:
						committed := eventOrder
						committed.Status = domain.OrderStatusCommitted
						p, err := s.buildLifecyclePayload(
							committed, domain.AttestationRequestSubmit,
							"committed", "", submitApprovalID)
						if err != nil {
							return domain.ApprovalPayload{}, false, err
						}
						p.Mode = SubmitModeImmediate
						return p, true, nil
					// A settling event must never travel unattested. The commission
					// variant is defensive: no immediate path populates a commission
					// today, but one that settled the account without a fill would
					// have to be signed exactly like a fill.
					case domain.OrderEventFill, domain.OrderEventCommission:
						p, err := s.buildImmediateSettlementPayload(
							eventOrder, event, immediate.Persistence,
						)
						if err != nil {
							return domain.ApprovalPayload{}, false, err
						}
						return p, true, nil
					default:
						return domain.ApprovalPayload{}, false, nil
					}
				},
				func(att Attestation, p domain.ApprovalPayload) {
					if p.Result == nil && p.Verdict != "" {
						issued = att
						payload = p
					}
				},
			)
		}
		order, result, err = n.SubmitImmediateWithAttestation(
			ctx, o, missing, caller, attestFor)
		if err != nil {
			if auditErr := s.auditApproval(
				ctx, n, o.Account, domain.AuditActionApprovalFailed,
				fmt.Sprintf("submit attestation failed: %v", err),
			); auditErr != nil {
				return ApprovalToken{}, errors.Join(err, auditErr)
			}
			return ApprovalToken{}, err
		}
		accepted = result.Accepted
		rejects = result.Rejects
	}

	if issued.Token == "" {
		return ApprovalToken{}, missingAttestationError(
			domain.AttestationRequestSubmit)
	}
	if accepted {
		verdict = "accept"
	} else {
		verdict = "reject"
	}
	if err := s.auditApproval(
		ctx, n, o.Account, domain.AuditActionApprovalIssued,
		fmt.Sprintf("issue attestation order %s event %s request=%s",
			order.ExternalID.String(), issued.EventExternalID,
			domain.AttestationRequestSubmit),
	); err != nil {
		slog.Error(
			"write post-commit approval audit",
			"action", domain.AuditActionApprovalIssued,
			"order", order.ExternalID,
			"event", issued.EventExternalID,
			"error", err,
		)
	}

	return ApprovalToken{
		Token:           issued.Token,
		KeyID:           issued.KeyID,
		OrderExternalID: order.ExternalID.String(),
		Verdict:         verdict,
		Reasons:         rejectReasons(payload, rejects),
		Signed:          issued.Signed,
	}, nil
}

// SubmitDropCopyOrder submits one non-enforcing pre-trade order without ever
// creating an approval token or lifecycle attestation. A caller-supplied
// external id is preserved; when omitted, the store assigns one. Duplicate ids
// follow the ordinary unique-store conflict path. missing must be an explicit
// domain.MissingAccountCreate, enforced by the shared domain validator.
func (s *Service) SubmitDropCopyOrder(
	ctx context.Context, o domain.Order, missing domain.MissingAccountPolicy,
) (domain.Order, error) {
	// See submitOrderToken: identity format is a seam check, not a surface check.
	if err := domain.ValidateAccountID(o.Account); err != nil {
		return domain.Order{}, err
	}
	if err := validateMissingAccountPolicy(o.Account, missing); err != nil {
		return domain.Order{}, err
	}
	if err := domain.ValidateDropCopyMissingAccountPolicy(missing); err != nil {
		return domain.Order{}, fmt.Errorf("backend: %w", err)
	}
	o.DropCopy = true
	caller := auth.CallerFromContext(ctx)

	n := s.node

	// A drop-copy submit must never be signed. Its pre-trade decision was not
	// enforced, so a submit attestation would falsely claim risk approval.
	return n.SubmitOrder(ctx, o, missing, caller)
}

func isDropCopyOrder(order domain.Order) bool {
	return order.DropCopy
}

func dropCopySigningError() error {
	// A drop-copy pre-trade decision was not enforced. Signing that decision or
	// a token shortcut would falsely claim that risk approved the order.
	return fmt.Errorf(
		"backend: drop-copy submit has no signing token; submit an explicit execution report: %w",
		domain.ErrInvalid,
	)
}

// ConfirmExecution verifies the submit token against the stored order and
// records an idempotent confirmation event. It never calls the engine and never
// changes the order status or balances. The node re-checks execution-report
// activity inside the account lane; after any such activity the shortcut is no
// longer allowed because Officer cannot safely infer the venue state. The
// post-commit approval_confirmed audit write is best-effort: because the
// operation is already committed, a failure is logged rather than returned.
func (s *Service) ConfirmExecution(
	ctx context.Context, orderID string, token string,
) (domain.Order, Attestation, error) {
	order, err := domain.ParseExternalID(orderID)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	n := s.node

	stored, err := n.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	if isDropCopyOrder(stored.Order) {
		return domain.Order{}, Attestation{}, dropCopySigningError()
	}
	signer, err := s.signerOrErr()
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	result, err := signer.Verify(ctx, token, verifyParamsFor(stored.Order))
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	if err := requireShortcutSubmitVerdict(
		stored, token, result.Payload, "confirmed",
	); err != nil {
		return domain.Order{}, Attestation{}, err
	}

	caller := auth.CallerFromContext(ctx)
	off, keyID, err := signingMode(ctx, signer)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	var att Attestation
	attest := eventAttestor(
		signer, off, keyID, domain.AttestationRequestConfirm,
		func(event domain.OrderEvent) (domain.ApprovalPayload, bool, error) {
			if event.Type != domain.OrderEventConfirmed {
				return domain.ApprovalPayload{}, false, nil
			}
			confirmedOrder := stored.Order
			confirmedOrder.Source = caller.Source
			confirmedOrder.Principal = caller.Principal
			p, err := s.buildLifecyclePayload(
				confirmedOrder, domain.AttestationRequestConfirm,
				"confirmed", "", result.Payload.ApprovalID)
			if err != nil {
				return domain.ApprovalPayload{}, false, err
			}
			return p, true, nil
		},
		func(got Attestation, _ domain.ApprovalPayload) {
			att = got
		},
	)
	confirmed, err := n.ConfirmOrderWithAttestation(
		ctx, order, caller, attest)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	if att.Token == "" {
		var ok bool
		att, ok, err = persistedEventAttestation(
			ctx, n, order, domain.OrderEventConfirmed)
		if err != nil {
			return domain.Order{}, Attestation{}, err
		}
		if !ok {
			return domain.Order{}, Attestation{}, missingAttestationError(
				domain.AttestationRequestConfirm)
		}
	}
	detail := fmt.Sprintf(
		"confirm approval %s order %s", result.Payload.ApprovalID, orderID)
	if err := s.auditApproval(
		ctx, n, confirmed.Account, domain.AuditActionApprovalConfirmed, detail,
	); err != nil {
		slog.Error(
			"write post-commit approval audit",
			"action", domain.AuditActionApprovalConfirmed,
			"order", orderID,
			"error", err,
		)
	}
	return confirmed, att, nil
}

// CancelOrder verifies the submit token, then forwards a caller-supplied
// terminal cancellation report for the untouched stored order. Officer stores
// and signs caller leaves verbatim, while forwarding that value to the engine.
// If any execution report was already recorded, the shortcut fails and the
// caller must provide an explicit report instead. The post-commit
// approval_cancelled audit write is best-effort: because the operation is
// already committed, a failure is logged rather than returned.
func (s *Service) CancelOrder(
	ctx context.Context, orderID string, token, leavesQuantity, reason string,
) (domain.Order, Attestation, error) {
	order, err := domain.ParseExternalID(orderID)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	// The reason is signed into the payload and rendered verbatim into the audit
	// detail line, where a control character could forge a second record.
	if err := domain.ValidateReason(reason); err != nil {
		return domain.Order{}, Attestation{}, err
	}
	n := s.node

	stored, err := n.GetOrder(ctx, order)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	if isDropCopyOrder(stored.Order) {
		return domain.Order{}, Attestation{}, dropCopySigningError()
	}
	signer, err := s.signerOrErr()
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	result, err := signer.Verify(ctx, token, verifyParamsFor(stored.Order))
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	if err := requireShortcutSubmitVerdict(
		stored, token, result.Payload, "cancelled",
	); err != nil {
		return domain.Order{}, Attestation{}, err
	}

	caller := auth.CallerFromContext(ctx)
	off, keyID, err := signingMode(ctx, signer)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	var att Attestation
	attest := eventAttestor(
		signer, off, keyID, domain.AttestationRequestCancel,
		func(event domain.OrderEvent) (domain.ApprovalPayload, bool, error) {
			if event.Type != domain.OrderEventCancelled ||
				event.Payload.ExecutionReport == nil {
				return domain.ApprovalPayload{}, false, nil
			}
			status := domain.OrderStatus(event.Payload.OrderStatus)
			if status == "" {
				status = domain.OrderStatusCancelled
			}
			persistence := engine.ExecutionReportPersistence{
				OrderStatus: status,
				Commission:  event.Payload.Commission,
				Leaves:      event.Payload.LeavesQuantity,
			}
			if event.Payload.RejectCode != "" || event.Payload.RejectReason != "" {
				persistence.Blocks = []domain.AccountBlock{{
					Account: stored.Order.Account,
					Policy:  event.Payload.RejectPolicy,
					Code:    event.Payload.RejectCode,
					Reason:  event.Payload.RejectReason,
					Details: event.Payload.RejectDetails,
				}}
			}
			cancelledOrder := stored.Order
			cancelledOrder.Status = status
			cancelledOrder.Source = caller.Source
			cancelledOrder.Principal = caller.Principal
			p, err := s.buildExecutionReportPayload(cancelledOrder, persistence)
			if err != nil {
				return domain.ApprovalPayload{}, false, err
			}
			p.Mode = SubmitModeHold
			p.ApprovalRef = result.Payload.ApprovalID
			p.PolicySummary = "order cancelled by shortcut"
			p.ExecutionReport = event.Payload.ExecutionReport
			p.Result.Outcome = "cancelled"
			p.RejectReason = reason
			return p, true, nil
		},
		func(got Attestation, p domain.ApprovalPayload) {
			if p.Result != nil && p.Result.OrderStatus == string(domain.OrderStatusCancelled) {
				att = got
			}
		},
	)
	cancelled, _, err := n.CancelOrderWithAttestation(
		ctx, order, leavesQuantity, caller, attest)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	if att.Token == "" {
		return domain.Order{}, Attestation{}, missingAttestationError(
			domain.AttestationRequestCancel)
	}
	detail := fmt.Sprintf(
		"cancel approval %s order %s reason=%s",
		result.Payload.ApprovalID, orderID, reason)
	if err := s.auditApproval(
		ctx, n, cancelled.Account, domain.AuditActionApprovalCancelled, detail,
	); err != nil {
		slog.Error(
			"write post-commit approval audit",
			"action", domain.AuditActionApprovalCancelled,
			"order", orderID,
			"error", err,
		)
	}
	return cancelled, att, nil
}

// --- helpers ----------------------------------------------------------------

func requireShortcutSubmitVerdict(
	stored domain.OrderDetail,
	token string,
	payload domain.ApprovalPayload,
	action string,
) error {
	if payload.Mode != SubmitModeHold {
		return fmt.Errorf(
			"backend: approval %s mode %q cannot be %s: %w",
			payload.ApprovalID, payload.Mode, action, domain.ErrConflict,
		)
	}
	if payload.Verdict != "accept" {
		return fmt.Errorf(
			"backend: approval %s verdict %q cannot be %s: %w",
			payload.ApprovalID, payload.Verdict, action, domain.ErrConflict,
		)
	}
	if payload.RequestType != string(domain.AttestationRequestSubmit) ||
		payload.Result != nil ||
		payload.ExecutionReport != nil ||
		payload.ApprovalRef != "" {
		return fmt.Errorf(
			"backend: approval %s is not an original submit verdict and cannot be %s: %w",
			payload.ApprovalID, action, domain.ErrConflict,
		)
	}
	eventID, err := domain.ParseExternalID(payload.EventExternalID)
	if err != nil {
		return fmt.Errorf(
			"backend: approval %s is not bound to a recorded pre-trade verdict and cannot be %s: %w",
			payload.ApprovalID, action, domain.ErrConflict,
		)
	}
	for _, event := range stored.Events {
		if event.ExternalID != eventID {
			continue
		}
		if event.Type == domain.OrderEventPreTradeAccepted &&
			event.Attestation != nil &&
			event.Attestation.Token == token {
			return nil
		}
		break
	}
	return fmt.Errorf(
		"backend: approval %s does not identify the recorded pre-trade accept verdict and cannot be %s: %w",
		payload.ApprovalID, action, domain.ErrConflict,
	)
}

// buildApprovalPayload assembles the canonical approval payload from the
// recorded order, the engine estimate, and the lifecycle timestamps. KeyID and
// Alg are set by the signer; the rest are bound here.
func buildApprovalPayload(
	order domain.Order, mode, approvalID, settlement string,
	issuedAt time.Time, nonce string,
) domain.ApprovalPayload {
	orderType := "market"
	if order.Price != "" {
		orderType = "limit"
	}
	return domain.ApprovalPayload{
		Version:         approvalPayloadVersion,
		ApprovalID:      approvalID,
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
		IssuedAt:        issuedAt.Format(time.RFC3339Nano),
		Nonce:           nonce,
		Principal:       order.Principal,
	}
}

// orderExternalID renders the order's opaque public handle for an approval
// payload, never a surrogate or engine id. A zero external id maps to the empty
// string so the canonical payload omits the order handle entirely.
func orderExternalID(order domain.Order) string {
	if order.ExternalID.IsZero() {
		return ""
	}
	return order.ExternalID.String()
}

func orderForEvent(
	order domain.Order,
	account domain.AccountID,
	event domain.OrderEvent,
	caller domain.Caller,
) domain.Order {
	order.ExternalID = event.Order
	order.Account = account
	order.Source = caller.Source
	order.Principal = caller.Principal
	return order
}

// buildRejectApprovalPayload assembles the canonical approval payload for a
// rejected pre-trade verdict. It binds the same order params as the accept path
// (so the envelope re-binds against the order it records), carries an empty
// estimate, binds the complete ordered engine reject list, and mirrors its first
// entry onto the legacy Reject* fields.
func buildRejectApprovalPayload(
	order domain.Order, mode, approvalID string, rejects []domain.OrderReject,
	issuedAt time.Time, nonce string,
) domain.ApprovalPayload {
	reject := firstReject(rejects)
	boundRejects := append([]domain.OrderReject(nil), rejects...)
	orderType := "market"
	if order.Price != "" {
		orderType = "limit"
	}
	return domain.ApprovalPayload{
		Version:         approvalPayloadVersion,
		ApprovalID:      approvalID,
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
		Nonce:           nonce,
		RejectCode:      reject.Code,
		RejectScope:     reject.Scope,
		RejectPolicy:    reject.Policy,
		RejectReason:    reject.Reason,
		RejectDetails:   reject.Details,
		Principal:       order.Principal,
		Rejects:         boundRejects,
	}
}

func buildSubmittedPayload(
	order domain.Order, mode, approvalID string,
) (domain.ApprovalPayload, error) {
	nonce, err := newNonce()
	if err != nil {
		return domain.ApprovalPayload{}, err
	}
	payload := baseAttestationPayload(
		order, mode, time.Now().UTC(), approvalID, nonce)
	payload.PolicySummary = "order submitted"
	return payload, nil
}

// baseAttestationPayload assembles the request-neutral core of an attestation
// payload: the bound order params, lifecycle timestamps, and a fresh
// approvalId/nonce. Callers stamp the verdict/result and request-specific fields.
// RequestType and EventExternalID are set later by attestEvent. It is the shared
// core the report/confirm/cancel builders extend, mirroring buildApprovalPayload.
func baseAttestationPayload(
	order domain.Order, mode string, issuedAt time.Time,
	approvalID, nonce string,
) domain.ApprovalPayload {
	orderType := "market"
	if order.Price != "" {
		orderType = "limit"
	}
	return domain.ApprovalPayload{
		Version:         approvalPayloadVersion,
		ApprovalID:      approvalID,
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
		Nonce:           nonce,
		Principal:       order.Principal,
	}
}

// buildImmediateSettlementPayload assembles the attestation payload for a
// settling event of an immediate submit.
func (s *Service) buildImmediateSettlementPayload(
	order domain.Order,
	event domain.OrderEvent,
	persistence *engine.ExecutionReportPersistence,
) (domain.ApprovalPayload, error) {
	if persistence == nil {
		return domain.ApprovalPayload{}, fmt.Errorf(
			"backend: immediate execution report persistence missing",
		)
	}
	settled := order
	settled.Status = persistence.OrderStatus
	if settled.Status == "" {
		settled.Status = domain.OrderStatusFilled
	}
	payload, err := s.buildExecutionReportEventPayload(settled, event, *persistence)
	if err != nil {
		return domain.ApprovalPayload{}, err
	}
	payload.RequestType = string(domain.AttestationRequestExecutionReport)
	return payload, nil
}

// buildExecutionReportPayload assembles the attestation payload for an execution
// report: the bound order params plus its recorded persistence section
// (optional fill and commission, leaves, target status, and any account block
// the engine recorded). Verdict is "accept" (the report was applied); a report
// that produced an account block carries the first block onto the reject fields
// too, mirroring the fill event.
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

	payload := baseAttestationPayload(
		order, SubmitModeImmediate, issuedAt, approvalID, nonce)
	payload.Verdict = "accept"
	payload.PolicySummary = "execution report applied"
	fillQuantity := ""
	fillPrice := ""
	fillLockPrice := ""
	commission := persistence.Commission
	if persistence.Trade != nil {
		fillQuantity = persistence.Trade.Quantity
		fillPrice = persistence.Trade.Price
		fillLockPrice = persistence.Trade.LockPrice
		if commission == nil {
			commission = persistence.Trade.Commission
		}
	}
	payload.Result = &domain.AttestationResult{
		Outcome:        "applied",
		FillQuantity:   fillQuantity,
		FillPrice:      fillPrice,
		FillLockPrice:  fillLockPrice,
		Commission:     commission,
		LeavesQuantity: persistence.Leaves,
		OrderStatus:    string(persistence.OrderStatus),
		Blocks:         attestationBlocks(persistence.Blocks),
	}
	if len(persistence.Blocks) > 0 {
		b := persistence.Blocks[0]
		payload.RejectCode = b.Code
		payload.RejectScope = "account"
		payload.RejectPolicy = b.Policy
		payload.RejectReason = b.Reason
		payload.RejectDetails = b.Details
	}
	return payload, nil
}

// buildLifecyclePayload assembles the attestation payload for a recorded
// lifecycle event. It binds the resulting order status and the order's own
// durable open quantity: a lifecycle event records the order's state at that
// moment, not an execution report, so report-side caller leaves has no place here.
// When approvalRef is present it links a workflow shortcut to the submit
// approval it acts on. reason, when present, is bound onto RejectReason.
func (s *Service) buildLifecyclePayload(
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

	payload := baseAttestationPayload(
		order, SubmitModeHold, issuedAt, approvalID, nonce)
	payload.Verdict = "accept"
	payload.ApprovalRef = approvalRef
	payload.PolicySummary = "order lifecycle recorded"
	if requestType == domain.AttestationRequestConfirm {
		payload.PolicySummary = "order confirmation recorded"
	}
	payload.Result = &domain.AttestationResult{
		Outcome:        outcome,
		LeavesQuantity: order.Leaves,
		OrderStatus:    string(order.Status),
		Blocks:         []domain.AttestationBlock{},
	}
	if reason != "" {
		payload.RejectReason = reason
	}
	return payload, nil
}

// attestationBlocks maps engine execution blocks onto the signed-payload block
// shape. It returns a non-nil (possibly empty) slice so the canonical bytes stay
// deterministic (the Result.Blocks field is always present when Result is).
func attestationBlocks(blocks []domain.AccountBlock) []domain.AttestationBlock {
	out := make([]domain.AttestationBlock, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, domain.AttestationBlock{
			Account: string(b.Account),
			Policy:  b.Policy,
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

// newNonce returns a fresh random 128-bit base64url identifier.
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

func firstReject(rejects []domain.OrderReject) domain.OrderReject {
	if len(rejects) == 0 {
		return domain.OrderReject{}
	}
	return rejects[0]
}

func rejectReasons(
	payload domain.ApprovalPayload, rejects []domain.OrderReject,
) []domain.OrderReject {
	if payload.Verdict != "reject" {
		return nil
	}
	if len(rejects) > 0 {
		out := make([]domain.OrderReject, len(rejects))
		copy(out, rejects)
		return out
	}
	if len(payload.Rejects) > 0 {
		out := make([]domain.OrderReject, len(payload.Rejects))
		copy(out, payload.Rejects)
		return out
	}
	return []domain.OrderReject{{
		Code:    payload.RejectCode,
		Scope:   payload.RejectScope,
		Policy:  payload.RejectPolicy,
		Reason:  payload.RejectReason,
		Details: payload.RejectDetails,
	}}
}

// auditSigning records a signing-key/config action via the group node, the same
// AppendAudit path used by MCP-access writes.
func (s *Service) auditSigning(ctx context.Context, action domain.AuditAction, detail string) error {
	n := s.node
	if err := n.AppendAudit(ctx, store.AuditEntry{Action: action, Detail: detail},
		auth.CallerFromContext(ctx)); err != nil {
		return fmt.Errorf("backend: audit %s: %w", action, err)
	}
	return nil
}

// auditApproval records an approval issue/confirm/cancel action on the given
// node, stamped with the account the token authorises.
func (s *Service) auditApproval(
	ctx context.Context,
	n node.Node,
	account domain.AccountID,
	action domain.AuditAction,
	detail string,
) error {
	if err := n.AppendAudit(ctx, store.AuditEntry{
		Action:  action,
		Account: account,
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
// whether the envelope carries a signature.
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
