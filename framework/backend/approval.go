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
// commits and settles at the lock price. SubmitModeHold is the wire-compatible
// name for the normal workflow submit, whose later state changes arrive as
// execution reports.
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

type submitOrderAttestingNode interface {
	SubmitOrderWithAttestation(
		ctx context.Context,
		key node.Key,
		o domain.Order,
		caller domain.Caller,
		attestFor func(domain.Order, engine.OrderResult) store.EventAttestor,
	) (domain.Order, engine.OrderResult, error)
}

type submitImmediateAttestingNode interface {
	SubmitImmediateWithAttestation(
		ctx context.Context,
		key node.Key,
		o domain.Order,
		caller domain.Caller,
		attestFor func(domain.Order, engine.ImmediateResult) store.EventAttestor,
	) (domain.Order, engine.ImmediateResult, error)
}

type executionReportAttestingNode interface {
	ApplyExecutionReportWithAttestation(
		ctx context.Context,
		key node.Key,
		in domain.ExecutionReportInput,
		caller domain.Caller,
		attest store.EventAttestor,
	) (engine.ExecutionReportResult, error)
}

type confirmOrderAttestingNode interface {
	ConfirmOrderWithAttestation(
		ctx context.Context,
		order domain.ExternalID,
		caller domain.Caller,
		attest store.EventAttestor,
	) (domain.Order, error)
}

type cancelOrderAttestingNode interface {
	CancelOrderWithAttestation(
		ctx context.Context,
		order domain.ExternalID,
		caller domain.Caller,
		attest store.EventAttestor,
	) (domain.Order, engine.ExecutionReportResult, error)
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
// envelope shape as an accept verdict. The issued token is audited as
// approval_issued.
func (s *Service) SubmitOrderToken(
	ctx context.Context, o domain.Order, mode string,
) (ApprovalToken, error) {
	signer, err := s.signerOrErr()
	if err != nil {
		return ApprovalToken{}, err
	}
	off, keyID, err := signingMode(ctx, signer)
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
	// is created exactly once below (submitOrder/submitImmediate record it), and
	// the returned OrderExternalID is that created id, so confirm/cancel resolve
	// the same order. Surface layers parse the wire string before this boundary;
	// a duplicate id is rejected by the store with domain.ErrAlreadyExists.

	n, err := s.router.Route(keyFor(o.Account))
	if err != nil {
		return ApprovalToken{}, fmt.Errorf("backend: route submit token: %w", err)
	}
	caller := auth.CallerFromContext(ctx)
	key := keyFor(o.Account)
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
		attesting, ok := n.(submitOrderAttestingNode)
		if !ok {
			return ApprovalToken{}, fmt.Errorf(
				"backend: workflow submit attestation unsupported: %w",
				domain.ErrNotImplemented,
			)
		}
		var result engine.OrderResult
		attestFor := func(
			persisted domain.Order, submitted engine.OrderResult,
		) store.EventAttestor {
			return eventAttestor(
				signer, off, keyID, domain.AttestationRequestSubmit,
				func(event domain.OrderEvent) (domain.ApprovalPayload, bool, error) {
					eventOrder := orderForEvent(persisted, key, event, caller)
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
							submitted.EstimateSource,
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
							firstReject(submitted.Rejects), time.Now().UTC(), nonce,
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
		order, result, err = attesting.SubmitOrderWithAttestation(
			ctx, key, o, caller, attestFor)
		if err != nil {
			return ApprovalToken{}, err
		}
		accepted = result.Accepted
		rejects = result.Rejects
	default:
		attesting, ok := n.(submitImmediateAttestingNode)
		if !ok {
			return ApprovalToken{}, fmt.Errorf(
				"backend: submit immediate attestation unsupported: %w",
				domain.ErrNotImplemented,
			)
		}
		var result engine.ImmediateResult
		attestFor := func(
			persisted domain.Order, immediate engine.ImmediateResult,
		) store.EventAttestor {
			return eventAttestor(
				signer, off, keyID, domain.AttestationRequestSubmit,
				func(event domain.OrderEvent) (domain.ApprovalPayload, bool, error) {
					eventOrder := orderForEvent(persisted, key, event, caller)
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
							immediate.EstimateSource,
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
							firstReject(immediate.Rejects), time.Now().UTC(), nonce), true, nil
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
					case domain.OrderEventFill:
						filled := eventOrder
						filled.Status = domain.OrderStatusFilled
						persistence := engine.ExecutionReportPersistence{
							OrderStatus: domain.OrderStatusFilled,
							Trade: &domain.Trade{
								Order:      eventOrder.ExternalID,
								Account:    key.Account,
								Source:     caller.Source,
								Principal:  caller.Principal,
								BaseAsset:  eventOrder.BaseAsset,
								QuoteAsset: eventOrder.QuoteAsset,
								Side:       eventOrder.Side,
								Quantity:   immediate.FillQuantity,
								Price:      immediate.SettlementLockPrice,
								LockPrice:  immediate.SettlementLockPrice,
							},
							Blocks: immediate.Blocks,
						}
						p, err := s.buildExecutionReportPayload(filled, persistence)
						if err != nil {
							return domain.ApprovalPayload{}, false, err
						}
						p.RequestType = string(domain.AttestationRequestExecutionReport)
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
		order, result, err = attesting.SubmitImmediateWithAttestation(
			ctx, key, o, caller, attestFor)
		if err != nil {
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
	_ = s.auditApproval(ctx, n, key, domain.AuditActionApprovalIssued,
		fmt.Sprintf("issue attestation order %s event %s request=%s",
			order.ExternalID.String(), issued.EventExternalID,
			domain.AttestationRequestSubmit))

	return ApprovalToken{
		Token:           issued.Token,
		KeyID:           issued.KeyID,
		OrderExternalID: order.ExternalID.String(),
		Verdict:         verdict,
		Reasons:         rejectReasons(payload, rejects),
		Signed:          issued.Signed,
	}, nil
}

// ConfirmExecution verifies the submit token against the stored order and
// records an idempotent confirmation event. It never calls the engine and never
// changes the order status or balances. The node re-checks execution-report
// activity inside the account lane; after any such activity the shortcut is no
// longer allowed because Officer cannot safely infer the venue state.
func (s *Service) ConfirmExecution(
	ctx context.Context, orderID string, token string,
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
	attesting, ok := n.(confirmOrderAttestingNode)
	if !ok {
		return domain.Order{}, Attestation{}, fmt.Errorf(
			"backend: confirm attestation unsupported: %w",
			domain.ErrNotImplemented,
		)
	}
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
	confirmed, err := attesting.ConfirmOrderWithAttestation(
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
	key := keyFor(confirmed.Account)
	detail := fmt.Sprintf(
		"confirm approval %s order %s", result.Payload.ApprovalID, orderID)
	_ = s.auditApproval(ctx, n, key, domain.AuditActionApprovalConfirmed, detail)
	return confirmed, att, nil
}

// CancelOrder verifies the submit token, then asks the node to synthesize a
// terminal cancellation execution report from the untouched stored order. The
// normal engine path releases the order's remaining pre-trade effects. If any
// execution report was already recorded, the shortcut fails and the caller must
// provide an explicit report instead.
func (s *Service) CancelOrder(
	ctx context.Context, orderID string, token, reason string,
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
	attesting, ok := n.(cancelOrderAttestingNode)
	if !ok {
		return domain.Order{}, Attestation{}, fmt.Errorf(
			"backend: cancel attestation unsupported: %w",
			domain.ErrNotImplemented,
		)
	}
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
				Leaves: domain.ExecutionReportPersistedLeavesFor(
					status, event.Payload.LeavesQuantity,
				),
			}
			if event.Payload.RejectCode != "" || event.Payload.RejectReason != "" {
				persistence.Blocks = []domain.ExecutionAccountBlock{{
					Account: stored.Order.Account,
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
	cancelled, _, err := attesting.CancelOrderWithAttestation(
		ctx, order, caller, attest)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	if att.Token == "" {
		return domain.Order{}, Attestation{}, missingAttestationError(
			domain.AttestationRequestCancel)
	}
	key := keyFor(cancelled.Account)
	detail := fmt.Sprintf(
		"cancel approval %s order %s reason=%s",
		result.Payload.ApprovalID, orderID, reason)
	_ = s.auditApproval(ctx, n, key, domain.AuditActionApprovalCancelled, detail)
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
	order domain.Order, mode, approvalID, settlement, estimateSource string,
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
		EstimateSource:  estimateSource,
		IssuedAt:        issuedAt.Format(time.RFC3339Nano),
		Nonce:           nonce,
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
	key node.Key,
	event domain.OrderEvent,
	caller domain.Caller,
) domain.Order {
	order.ExternalID = event.Order
	order.Account = key.Account
	order.Source = caller.Source
	order.Principal = caller.Principal
	return order
}

// buildRejectApprovalPayload assembles the canonical approval payload for a
// rejected pre-trade verdict. It binds the same order params as the accept path
// (so the envelope re-binds against the order it records), carries an empty
// estimate, and stamps the first engine reject onto the Reject* fields.
func buildRejectApprovalPayload(
	order domain.Order, mode, approvalID string, reject domain.OrderReject,
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
		Verdict:         "reject",
		PolicySummary:   "rejected",
		EstimatePrice:   "",
		IssuedAt:        issuedAt.Format(time.RFC3339Nano),
		Nonce:           nonce,
		RejectCode:      reject.Code,
		RejectScope:     reject.Scope,
		RejectPolicy:    reject.Policy,
		RejectReason:    reject.Reason,
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
		payload.RejectReason = b.Reason
	}
	return payload, nil
}

// buildLifecyclePayload assembles the attestation payload for a recorded
// lifecycle event. It binds the resulting order status and, when approvalRef is
// present, links a workflow shortcut to the submit approval it acts on. reason,
// when present, is bound onto RejectReason.
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
	return []domain.OrderReject{{
		Code:   payload.RejectCode,
		Scope:  payload.RejectScope,
		Policy: payload.RejectPolicy,
		Reason: payload.RejectReason,
	}}
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
