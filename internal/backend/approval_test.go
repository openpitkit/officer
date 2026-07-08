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

package backend_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/backend"
)

// fakeSigner is a framework signing seam stand-in for the approval-flow tests.
// Sign records the payload it was handed and returns a deterministic token; the
// matching Verify returns the recorded payload re-bound against the expectation.
type fakeSigner struct {
	noESign      bool
	signed       []domain.ApprovalPayload
	signToken    func(domain.ApprovalPayload) string
	verifyResult *fwsigning.VerifyResult
	verifyErr    error
	noESignErr   error
	signErr      error
	setNoESign   []bool
}

var _ fwsigning.Service = (*fakeSigner)(nil)

func (s *fakeSigner) GenerateKey(context.Context) (domain.SigningKey, error) {
	return domain.SigningKey{KeyID: "key-1", Alg: fwsigning.AlgEd25519, Active: true}, nil
}

func (s *fakeSigner) ImportKey(_ context.Context, _, _ string) (domain.SigningKey, error) {
	return domain.SigningKey{KeyID: "key-2", Alg: fwsigning.AlgEd25519, Active: true}, nil
}

func (s *fakeSigner) ListKeys(context.Context) ([]domain.SigningKey, error) {
	return []domain.SigningKey{{KeyID: "key-1", Alg: fwsigning.AlgEd25519, Active: true}}, nil
}

func (s *fakeSigner) ActivePublicKey(string) (string, error) { return "PUBLIC", nil }

func (s *fakeSigner) PublicKeyByID(_ context.Context, keyID, _ string) (string, error) {
	if keyID == "" {
		return "", fmt.Errorf("signing: empty keyId: %w", domain.ErrInvalid)
	}
	return "PUBLIC", nil
}

func (s *fakeSigner) Fingerprint([]byte) string { return "fp" }

func (s *fakeSigner) Sign(payload domain.ApprovalPayload) (string, error) {
	if s.signErr != nil {
		return "", s.signErr
	}
	s.signed = append(s.signed, payload)
	if s.signToken != nil {
		return s.signToken(payload), nil
	}
	return "signed-token", nil
}

func (s *fakeSigner) SignNone(payload domain.ApprovalPayload) (string, error) {
	payload.KeyID = ""
	payload.Alg = fwsigning.AlgNone
	env := approvalEnvelope{Approval: payload, Alg: fwsigning.AlgNone}
	raw, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (s *fakeSigner) Verify(
	_ context.Context, _ string, _ fwsigning.VerifyParams,
) (fwsigning.VerifyResult, error) {
	if s.verifyErr != nil {
		return fwsigning.VerifyResult{}, s.verifyErr
	}
	if s.verifyResult != nil {
		return *s.verifyResult, nil
	}
	return fwsigning.VerifyResult{
		Payload: domain.ApprovalPayload{
			ApprovalID:    "approval-1",
			ReservationID: "approval-1",
			Mode:          backend.SubmitModeHold,
		},
		Signed: true,
	}, nil
}

func (s *fakeSigner) NoESign(context.Context) (bool, error) {
	if s.noESignErr != nil {
		return false, s.noESignErr
	}
	return s.noESign, nil
}

func (s *fakeSigner) SetNoESign(_ context.Context, off bool) error {
	s.setNoESign = append(s.setNoESign, off)
	s.noESign = off
	return nil
}

type approvalEnvelope struct {
	Approval  domain.ApprovalPayload `json:"approval"`
	Signature string                 `json:"signature,omitempty"`
	KeyID     string                 `json:"keyId,omitempty"`
	Alg       string                 `json:"alg"`
}

func decodeApprovalEnvelope(token string) (approvalEnvelope, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return approvalEnvelope{}, err
	}
	var env approvalEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return approvalEnvelope{}, err
	}
	return env, nil
}

func sampleOrder() domain.Order {
	return domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "10",
		Price:       "100",
	}
}

func TestService_SubmitOrderTokenHoldIssuesSignedToken(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)

	tok, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeHold)
	if err != nil {
		t.Fatalf("SubmitOrderToken hold: %v", err)
	}
	if tok.Token != "signed-token" || !tok.Signed || tok.KeyID != "key-1" {
		t.Fatalf("token = %+v, want signed token with key-1", tok)
	}
	if tok.OrderExternalID == "" {
		t.Fatalf("token must carry the recorded order external id")
	}
	if _, err := domain.ParseExternalID(tok.OrderExternalID); err != nil {
		t.Fatalf("token order handle must be a valid external id: %v", err)
	}
	// The HTTP token is the hold-mode envelope; the submit also persists a separate
	// display envelope, so two payloads are signed.
	p := httpTokenPayload(t, signer, backend.SubmitModeHold)
	if p.Verdict != "accept" {
		t.Fatalf("payload verdict = %q, want accept", p.Verdict)
	}
	if p.EstimatePrice != "100" || p.EstimateSource != domain.EstimateSourceLimit {
		t.Fatalf("payload estimate = %q/%q, want engine lock price", p.EstimatePrice, p.EstimateSource)
	}
	if p.OrderExternalID != tok.OrderExternalID {
		t.Fatalf("payload order handle = %q, want %q", p.OrderExternalID, tok.OrderExternalID)
	}
	if p.Instrument != "AAPL/USD" || p.Quantity != "10" ||
		p.AmountKind != string(domain.OrderAmountKindQuantity) || p.LimitPrice != "100" {
		t.Fatalf("payload bound params = %+v", p)
	}
	if p.Nonce == "" || p.IssuedAt == "" {
		t.Fatalf("payload lifecycle fields missing: %+v", p)
	}
	if orderByToken(t, fn, tok).Status != domain.OrderStatusAccepted {
		t.Fatalf("hold must leave the order accepted (funds held), got %q",
			orderByToken(t, fn, tok).Status)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionApprovalIssued) {
		t.Fatalf("hold must audit approval_issued: %+v", fn.auditCalls)
	}
	// The accepted order carries persisted 1:1 attestations on its submitted and
	// verdict events, surfaced by GetOrder.
	wantEvents := eventIDsByType(t, svc, tok.OrderExternalID,
		domain.OrderEventSubmitted,
		domain.OrderEventPreTradeAccepted,
	)
	if !reflect.DeepEqual(fn.persistAttestationCalls, wantEvents) {
		t.Fatalf("hold attestations = %+v, want event ids %+v",
			fn.persistAttestationCalls, wantEvents)
	}
	detail := getOrderByToken(t, svc, tok)
	att := verdictAttestation(t, detail)
	if att == nil || att.Token == "" || att.KeyID != "key-1" ||
		att.Alg != fwsigning.AlgEd25519 {
		t.Fatalf("hold order must surface a signed verdict attestation, got %+v", att)
	}
}

func TestService_SubmitOrderTokenImmediateSettles(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)

	tok, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeImmediate)
	if err != nil {
		t.Fatalf("SubmitOrderToken immediate: %v", err)
	}
	if orderByToken(t, fn, tok).Status != domain.OrderStatusFilled {
		t.Fatalf("immediate must leave the order filled, got %q",
			orderByToken(t, fn, tok).Status)
	}
	p := httpTokenPayload(t, signer, backend.SubmitModeImmediate)
	if p.Verdict != "accept" {
		t.Fatalf("immediate payload verdict = %q, want accept", p.Verdict)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionApprovalIssued) {
		t.Fatalf("immediate must audit approval_issued")
	}
	// The filled order carries persisted 1:1 attestations on every immediate
	// lifecycle event, surfaced by GetOrder.
	wantEvents := eventIDsByType(t, svc, tok.OrderExternalID,
		domain.OrderEventSubmitted,
		domain.OrderEventPreTradeAccepted,
		domain.OrderEventReservationCommitted,
		domain.OrderEventFill,
	)
	if !reflect.DeepEqual(fn.persistAttestationCalls, wantEvents) {
		t.Fatalf("immediate attestations = %+v, want event ids %+v",
			fn.persistAttestationCalls, wantEvents)
	}
	if att := verdictAttestation(t, getOrderByToken(t, svc, tok)); att == nil {
		t.Fatalf("immediate order must surface a signed verdict attestation")
	}
}

func TestService_SubmitOrderTokenDefaultsImmediate(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)

	tok, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), "")
	if err != nil {
		t.Fatalf("SubmitOrderToken default: %v", err)
	}
	if orderByToken(t, fn, tok).Status != domain.OrderStatusFilled {
		t.Fatalf("empty mode must default to immediate (filled), got %q",
			orderByToken(t, fn, tok).Status)
	}
}

// TestService_SubmitOrderTokenRejectPersistsVerdict covers the reject path: the
// token flow returns a successful signed reject verdict with reasons, records
// the rejected order, and persists its 1:1 reject attestation onto the
// pre_trade_rejected event.
func TestService_SubmitOrderTokenRejectPersistsVerdict(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	fn.holdResult = &engine.HoldResult{
		Accepted: false,
		Rejects:  []domain.OrderReject{{Code: "insufficient_funds", Reason: "no funds"}},
	}

	o := sampleOrder()
	o.ExternalID = mdID("reject-order-id")
	tok, err := svc.SubmitOrderToken(context.Background(), o, backend.SubmitModeHold)
	if err != nil {
		t.Fatalf("reject submit must be a successful decision: %v", err)
	}
	if tok.Verdict != "reject" || tok.Token == "" || len(tok.Reasons) != 1 {
		t.Fatalf("reject token = %+v, want signed reject with one reason", tok)
	}
	if len(signer.signed) != 2 || signer.signed[1].Verdict != "reject" {
		t.Fatalf("reject must sign the reject envelope: %+v", signer.signed)
	}
	if signer.signed[0].ApprovalID == "" ||
		signer.signed[0].ApprovalID != signer.signed[1].ApprovalID {
		t.Fatalf("submitted/reject approval ids = %q/%q, want same non-empty id",
			signer.signed[0].ApprovalID, signer.signed[1].ApprovalID)
	}
	if signer.signed[1].RejectCode != "insufficient_funds" {
		t.Fatalf("persisted reject envelope must carry the engine reject: %+v", signer.signed[1])
	}
	wantRejectEvents := eventIDsByType(t, svc, o.ExternalID.String(),
		domain.OrderEventSubmitted,
		domain.OrderEventPreTradeRejected,
	)
	if !reflect.DeepEqual(fn.persistAttestationCalls, wantRejectEvents) {
		t.Fatalf("reject attestations = %+v, want event ids %+v",
			fn.persistAttestationCalls, wantRejectEvents)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionApprovalIssued) {
		t.Fatalf("reject must audit approval_issued for the persisted verdict")
	}
	detail, err := svc.GetOrder(context.Background(), o.ExternalID.String())
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusRejected {
		t.Fatalf("order must be recorded rejected, got %q", detail.Order.Status)
	}
	att := verdictAttestation(t, detail)
	if att == nil || att.Token == "" {
		t.Fatalf("rejected order must surface a signed verdict attestation, got %+v", att)
	}
}

func TestService_SubmitOrderTokenESignOff(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{noESign: true}
	svc, fn := newTestServiceWithSigner(signer)

	tok, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeHold)
	if err != nil {
		t.Fatalf("SubmitOrderToken eSign-off: %v", err)
	}
	if tok.Signed {
		t.Fatalf("eSign-off token must be unsigned")
	}
	if tok.KeyID != "" {
		t.Fatalf("eSign-off token must carry no key id, got %q", tok.KeyID)
	}
	if len(signer.signed) != 0 {
		t.Fatalf("eSign-off must not sign")
	}
	// The token is a real eSign-off envelope: it decodes with alg "none" and no
	// signature, with the bound payload still present.
	env, err := decodeApprovalEnvelope(tok.Token)
	if err != nil {
		t.Fatalf("decode eSign-off envelope: %v", err)
	}
	if env.Alg != fwsigning.AlgNone || env.Signature != "" {
		t.Fatalf("eSign-off envelope = %+v, want alg none and no signature", env)
	}
	if env.Approval.Instrument != "AAPL/USD" || env.Approval.Verdict != "accept" {
		t.Fatalf("eSign-off payload binding missing: %+v", env.Approval)
	}
	// eSign-off still persists unsigned (alg "none") attestations on the
	// submitted and verdict events, so the panel order shows the unsigned badge.
	wantEvents := eventIDsByType(t, svc, tok.OrderExternalID,
		domain.OrderEventSubmitted,
		domain.OrderEventPreTradeAccepted,
	)
	if !reflect.DeepEqual(fn.persistAttestationCalls, wantEvents) {
		t.Fatalf("eSign-off attestations = %+v, want event ids %+v",
			fn.persistAttestationCalls, wantEvents)
	}
	detail := getOrderByToken(t, svc, tok)
	att := verdictAttestation(t, detail)
	if att == nil || att.Alg != fwsigning.AlgNone ||
		att.KeyID != "" || att.Token == "" {
		t.Fatalf("eSign-off order must surface an unsigned verdict attestation, got %+v", att)
	}
}

func TestService_ResolutionESignOffPersistsUnsignedLifecycleEvents(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{noESign: true}
	svc, fn := newTestServiceWithSigner(signer)

	confirmTok := mustHold(t, svc)
	confirmed, confirmAtt, err := svc.ConfirmExecution(
		context.Background(), confirmTok.OrderExternalID, confirmTok.Token, false)
	if err != nil {
		t.Fatalf("ConfirmExecution: %v", err)
	}
	if confirmed.Status != domain.OrderStatusCommitted ||
		confirmAtt.Signed ||
		confirmAtt.KeyID != "" ||
		confirmAtt.Token == "" {
		t.Fatalf("confirm eSign-off = order %+v att %+v", confirmed, confirmAtt)
	}
	confirmEvents := eventIDsByType(t, svc, confirmTok.OrderExternalID,
		domain.OrderEventSubmitted,
		domain.OrderEventPreTradeAccepted,
		domain.OrderEventReservationCommitted,
	)
	if !slices.Contains(fn.persistAttestationCalls, confirmEvents[2]) {
		t.Fatalf("confirm did not persist unsigned committed attestation: calls=%+v",
			fn.persistAttestationCalls)
	}

	cancelTok := mustHold(t, svc)
	cancelled, cancelAtt, err := svc.CancelOrder(
		context.Background(), cancelTok.OrderExternalID, cancelTok.Token, "operator", false)
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if cancelled.Status != domain.OrderStatusCancelled ||
		cancelAtt.Signed ||
		cancelAtt.KeyID != "" ||
		cancelAtt.Token == "" {
		t.Fatalf("cancel eSign-off = order %+v att %+v", cancelled, cancelAtt)
	}
	cancelEvents := eventIDsByType(t, svc, cancelTok.OrderExternalID,
		domain.OrderEventSubmitted,
		domain.OrderEventPreTradeAccepted,
		domain.OrderEventReservationRolledBack,
		domain.OrderEventCancelled,
	)
	if !slices.Contains(fn.persistAttestationCalls, cancelEvents[2]) ||
		!slices.Contains(fn.persistAttestationCalls, cancelEvents[3]) {
		t.Fatalf("cancel did not persist unsigned rollback/cancel attestations: calls=%+v",
			fn.persistAttestationCalls)
	}
}

func TestService_ConfirmExecutionCommits(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)

	before := len(fn.persistAttestationCalls)
	order, att, err := svc.ConfirmExecution(context.Background(), tok.OrderExternalID, tok.Token, false)
	if err != nil {
		t.Fatalf("ConfirmExecution: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("confirm must commit, got %q", order.Status)
	}
	if len(fn.confirmCalls) != 1 || fn.confirmCalls[0] != "approval-1" {
		t.Fatalf("confirm must resolve by approval id: %+v", fn.confirmCalls)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionApprovalConfirmed) {
		t.Fatalf("confirm must audit approval_confirmed")
	}
	// The confirm attests its reservation_committed event: a signed token is
	// returned and one more attestation is persisted.
	if att.Token == "" || !att.Signed {
		t.Fatalf("confirm must return a signed attestation, got %+v", att)
	}
	if len(fn.persistAttestationCalls) != before+1 {
		t.Fatalf("confirm must persist one attestation, calls=%+v", fn.persistAttestationCalls)
	}
}

func TestService_ConfirmExecutionIdempotent(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)

	first, firstAtt, err := svc.ConfirmExecution(
		context.Background(), tok.OrderExternalID, tok.Token, false)
	if err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	if first.Status != domain.OrderStatusCommitted || firstAtt.Token == "" {
		t.Fatalf("first confirm = %+v att=%+v, want committed with attestation",
			first, firstAtt)
	}
	fn.confirmErr = domain.ErrConflict
	second, secondAtt, err := svc.ConfirmExecution(
		context.Background(), tok.OrderExternalID, tok.Token, false)
	if err != nil {
		t.Fatalf("second confirm must be idempotent success, got %v", err)
	}
	if second.Status != domain.OrderStatusCommitted {
		t.Fatalf("idempotent confirm status = %q, want committed",
			second.Status)
	}
	if secondAtt != firstAtt {
		t.Fatalf("idempotent confirm attestation = %+v, want %+v",
			secondAtt, firstAtt)
	}
}

func TestService_ConfirmExecutionCommittedMissingAttestationFailsClosed(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)

	if _, _, err := svc.ConfirmExecution(
		context.Background(), tok.OrderExternalID, tok.Token, false); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	detail, err := svc.GetOrder(context.Background(), tok.OrderExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	for _, event := range detail.Events {
		if event.Type == domain.OrderEventReservationCommitted {
			delete(fn.attestations, event.ExternalID)
		}
	}

	_, _, err = svc.ConfirmExecution(
		context.Background(), tok.OrderExternalID, tok.Token, false)
	if err == nil {
		t.Fatal("idempotent confirm succeeded without persisted attestation")
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("idempotent confirm error = %v, want internal class", err)
	}
}

func TestService_ConfirmAfterCancelConflicts(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)

	if _, _, err := svc.CancelOrder(
		context.Background(), tok.OrderExternalID, tok.Token, "operator", false,
	); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// The cancelled order is terminal, so the default safety gate rejects before
	// the engine commit path.
	fn.confirmErr = domain.ErrConflict
	_, _, err := svc.ConfirmExecution(context.Background(), tok.OrderExternalID, tok.Token, false)
	if !errors.Is(err, domain.ErrTerminalOrder) {
		t.Fatalf("confirm after cancel must report terminal order, got %v", err)
	}
	if len(fn.confirmCalls) != 0 {
		t.Fatalf("confirm after cancel must not reach engine commit: %+v", fn.confirmCalls)
	}
}

func TestService_ConfirmAfterCancelForceReachesEngine(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)

	if _, _, err := svc.CancelOrder(
		context.Background(), tok.OrderExternalID, tok.Token, "operator", false,
	); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	order, _, err := svc.ConfirmExecution(context.Background(), tok.OrderExternalID, tok.Token, true)
	if err != nil {
		t.Fatalf("forced confirm after cancel: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("forced confirm status = %q, want committed", order.Status)
	}
	if len(fn.confirmCalls) != 1 || fn.confirmCalls[0] != "approval-1" {
		t.Fatalf("forced confirm did not reach engine: %+v", fn.confirmCalls)
	}
	if !hasAuditDetail(fn.auditCalls, domain.AuditActionApprovalConfirmed, "forced=true") {
		t.Fatalf("forced confirm must audit forced=true: %+v", fn.auditCalls)
	}
}

func TestService_CancelAfterConfirmConflictsBeforeRollback(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)

	if _, _, err := svc.ConfirmExecution(context.Background(), tok.OrderExternalID, tok.Token, false); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	_, _, err := svc.CancelOrder(context.Background(), tok.OrderExternalID, tok.Token, "too late", false)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cancel after confirm must conflict, got %v", err)
	}
	if len(fn.cancelCalls) != 0 {
		t.Fatalf("cancel after confirm must not reach engine rollback: %+v", fn.cancelCalls)
	}
}

func TestService_CancelFilledOrderRequiresForce(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)

	oid := orderID(t, tok)
	order := fn.orders[oid]
	order.Status = domain.OrderStatusFilled
	fn.orders[oid] = order

	_, _, err := svc.CancelOrder(context.Background(), tok.OrderExternalID, tok.Token, "late", false)
	if !errors.Is(err, domain.ErrTerminalOrder) {
		t.Fatalf("cancel filled without force = %v, want terminal order", err)
	}
	if len(fn.cancelCalls) != 0 {
		t.Fatalf("cancel filled without force reached engine: %+v", fn.cancelCalls)
	}

	order, _, err = svc.CancelOrder(context.Background(), tok.OrderExternalID, tok.Token, "late", true)
	if err != nil {
		t.Fatalf("forced cancel filled: %v", err)
	}
	if order.Status != domain.OrderStatusCancelled {
		t.Fatalf("forced cancel status = %q, want cancelled", order.Status)
	}
	if len(fn.cancelCalls) != 1 || fn.cancelCalls[0] != "approval-1" {
		t.Fatalf("forced cancel did not reach engine: %+v", fn.cancelCalls)
	}
	if !hasAuditDetail(fn.auditCalls, domain.AuditActionApprovalCancelled, "forced=true") {
		t.Fatalf("forced cancel must audit forced=true: %+v", fn.auditCalls)
	}
}

func TestService_CancelOrderRollsBack(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)

	before := len(fn.persistAttestationCalls)
	order, att, err := svc.CancelOrder(context.Background(), tok.OrderExternalID, tok.Token, "stale price", false)
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if order.Status != domain.OrderStatusCancelled {
		t.Fatalf("cancel must roll back, got %q", order.Status)
	}
	if len(fn.cancelCalls) != 1 || fn.cancelCalls[0] != "approval-1" {
		t.Fatalf("cancel must resolve by approval id: %+v", fn.cancelCalls)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionApprovalCancelled) {
		t.Fatalf("cancel must audit approval_cancelled")
	}
	// The cancel signs rollback and cancelled events; the returned token is bound
	// to the cancelled event.
	if att.Token == "" || !att.Signed {
		t.Fatalf("cancel must return a signed attestation, got %+v", att)
	}
	if len(fn.persistAttestationCalls) != before+2 {
		t.Fatalf("cancel must persist two attestations, calls=%+v", fn.persistAttestationCalls)
	}
	cancelEvents := eventIDsByType(t, svc, tok.OrderExternalID,
		domain.OrderEventReservationRolledBack,
		domain.OrderEventCancelled,
	)
	if att.EventExternalID != cancelEvents[1].String() {
		t.Fatalf("returned cancel event = %q, want %q",
			att.EventExternalID, cancelEvents[1].String())
	}
}

func TestService_CancelOrderNoopMissingAttestationFailsClosed(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)
	fn.cancelNoop = true

	_, _, err := svc.CancelOrder(
		context.Background(), tok.OrderExternalID, tok.Token, "already resolved", false)
	if err == nil {
		t.Fatal("cancel no-op succeeded without an attestation")
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("cancel no-op error = %v, want internal class", err)
	}
}

func TestService_ConfirmRejectsBadToken(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{verifyErr: domain.ErrInvalid}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)
	fn.confirmCalls = nil

	_, _, err := svc.ConfirmExecution(context.Background(), tok.OrderExternalID, tok.Token, false)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad token must reject before commit, got %v", err)
	}
	if len(fn.confirmCalls) != 0 {
		t.Fatalf("verification failure must not reach the engine commit")
	}
}

func TestService_ConfirmImmediateTokenConflictsBeforeEngine(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{
		verifyResult: &fwsigning.VerifyResult{
			Payload: domain.ApprovalPayload{
				ApprovalID:    "approval-immediate",
				ReservationID: "approval-immediate",
				Mode:          backend.SubmitModeImmediate,
			},
			Signed: true,
		},
	}
	svc, fn := newTestServiceWithSigner(signer)
	tok, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeHold)
	if err != nil {
		t.Fatalf("hold setup: %v", err)
	}

	_, _, err = svc.ConfirmExecution(context.Background(), tok.OrderExternalID, tok.Token, false)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("confirm immediate token must conflict, got %v", err)
	}
	if len(fn.confirmCalls) != 0 {
		t.Fatalf("immediate token must not reach engine commit: %+v", fn.confirmCalls)
	}
}

func TestService_SubmitOrderTokenValidatesMode(t *testing.T) {
	t.Parallel()
	svc, _ := newTestServiceWithSigner(&fakeSigner{})
	if _, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), "weird"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown mode must be ErrInvalid, got %v", err)
	}
}

func TestService_SigningMethodsRequireSigner(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService() // nil signer
	ctx := context.Background()

	if _, err := svc.GenerateSigningKey(ctx); !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("nil signer GenerateSigningKey = %v, want ErrNotImplemented", err)
	}
	if _, err := svc.SubmitOrderToken(ctx, sampleOrder(), backend.SubmitModeHold); !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("nil signer SubmitOrderToken = %v, want ErrNotImplemented", err)
	}
}

func TestService_SetNoESignAudits(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)

	if err := svc.SetNoESign(context.Background(), true); err != nil {
		t.Fatalf("SetNoESign: %v", err)
	}
	if len(signer.setNoESign) != 1 || !signer.setNoESign[0] {
		t.Fatalf("SetNoESign not delegated: %+v", signer.setNoESign)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionSetSigningConfig) {
		t.Fatalf("SetNoESign must audit set_signing_config")
	}
}

func TestService_GenerateSigningKeyAudits(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)

	key, err := svc.GenerateSigningKey(context.Background())
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	if key.KeyID != "key-1" {
		t.Fatalf("key id = %q", key.KeyID)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionGenerateSigningKey) {
		t.Fatalf("generate must audit generate_signing_key")
	}
}

// orderID parses the token's order handle into the opaque external id the fake
// node keys its order map by.
func orderID(t *testing.T, tok backend.ApprovalToken) domain.ExternalID {
	t.Helper()
	id, err := domain.ParseExternalID(tok.OrderExternalID)
	if err != nil {
		t.Fatalf("token order handle %q: %v", tok.OrderExternalID, err)
	}
	return id
}

// orderByToken returns the fake node's recorded order for a token.
func orderByToken(t *testing.T, fn *fakeNode, tok backend.ApprovalToken) domain.Order {
	t.Helper()
	return fn.orders[orderID(t, tok)]
}

// getOrderByToken reads the order detail (with its 1:1 approval) for a token
// through the service GetOrder seam.
func getOrderByToken(
	t *testing.T, svc *backend.Service, tok backend.ApprovalToken,
) domain.OrderDetail {
	t.Helper()
	detail, err := svc.GetOrder(context.Background(), tok.OrderExternalID)
	if err != nil {
		t.Fatalf("GetOrder %q: %v", tok.OrderExternalID, err)
	}
	return detail
}

// verdictAttestation returns the first attested event's attestation on the
// detail, or nil when no verdict event carries one.
func verdictAttestation(t *testing.T, detail domain.OrderDetail) *domain.EventAttestation {
	t.Helper()
	for i := range detail.Events {
		switch detail.Events[i].Type {
		case domain.OrderEventPreTradeAccepted, domain.OrderEventPreTradeRejected:
			return detail.Events[i].Attestation
		}
	}
	return nil
}

func eventIDsByType(
	t *testing.T,
	svc *backend.Service,
	orderExternalID string,
	types ...domain.OrderEventType,
) []domain.ExternalID {
	t.Helper()
	detail, err := svc.GetOrder(context.Background(), orderExternalID)
	if err != nil {
		t.Fatalf("GetOrder %q: %v", orderExternalID, err)
	}
	eventsByType := make(map[domain.OrderEventType]domain.ExternalID, len(detail.Events))
	for i := range detail.Events {
		eventsByType[detail.Events[i].Type] = detail.Events[i].ExternalID
	}
	out := make([]domain.ExternalID, 0, len(types))
	for _, typ := range types {
		id, ok := eventsByType[typ]
		if !ok {
			t.Fatalf("event %s not found on order %q: %+v", typ, orderExternalID, detail.Events)
		}
		out = append(out, id)
	}
	return out
}

// httpTokenPayload returns the recorded signed payload for the HTTP approval
// token issued in the given submit mode. SubmitOrderToken signs two envelopes
// per accepted submit: the persisted display envelope (buildOrderEnvelope,
// always "immediate") and the returned HTTP token (buildApprovalPayload, the
// submit mode). For a hold submit the two are distinguishable by mode; the
// helper picks the one that matches the submit mode.
func httpTokenPayload(t *testing.T, signer *fakeSigner, mode string) domain.ApprovalPayload {
	t.Helper()
	for i := len(signer.signed) - 1; i >= 0; i-- {
		if signer.signed[i].Mode == mode &&
			signer.signed[i].RequestType == string(domain.AttestationRequestSubmit) &&
			signer.signed[i].Verdict != "" {
			return signer.signed[i]
		}
	}
	t.Fatalf("no signed payload with mode %q: %+v", mode, signer.signed)
	return domain.ApprovalPayload{}
}

// mustHold issues a hold token and fails the test on error.
func mustHold(t *testing.T, svc *backend.Service) backend.ApprovalToken {
	t.Helper()
	tok, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeHold)
	if err != nil {
		t.Fatalf("hold setup: %v", err)
	}
	return tok
}

// TestService_SubmitOrderApprovalReadBack covers the C2 read-back path: a signed
// submit stamps the attestation onto the order's verdict event, and GetOrder
// returns it via OrderEvent.Attestation addressed by the event's external id -
// never a surrogate or engine id. The payload also carries the order's external
// id.
func TestService_SubmitOrderApprovalReadBack(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{
		signToken: func(p domain.ApprovalPayload) string {
			return "token-" + p.EventExternalID
		},
	}
	svc, fn := newTestServiceWithSigner(signer)
	ctx := context.Background()

	submitted, err := svc.SubmitOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	order := submitted.Order
	if order.ExternalID.IsZero() {
		t.Fatalf("submitted order must carry an external id")
	}
	if submitted.Token.Token == "" ||
		submitted.Token.KeyID != "key-1" ||
		submitted.Token.OrderExternalID != order.ExternalID.String() ||
		submitted.Token.Verdict != "accept" ||
		len(submitted.Token.Reasons) != 0 {
		t.Fatalf("inline submit token = %+v, want signed accept verdict", submitted.Token)
	}
	// The signer recorded every lifecycle payload; the returned verdict payload
	// must bind the order's external id, not any integer/surrogate handle.
	if len(signer.signed) != 3 {
		t.Fatalf("Sign calls = %d, want 3", len(signer.signed))
	}
	verdictPayload := httpTokenPayload(t, signer, backend.SubmitModeImmediate)
	if verdictPayload.OrderExternalID != order.ExternalID.String() {
		t.Fatalf("payload order handle = %q, want %q",
			verdictPayload.OrderExternalID, order.ExternalID.String())
	}
	// The attestation persists against the verdict event's external id.
	wantEvents := eventIDsByType(t, svc, order.ExternalID.String(),
		domain.OrderEventSubmitted,
		domain.OrderEventPreTradeAccepted,
		domain.OrderEventReservationCommitted,
	)
	if !reflect.DeepEqual(fn.persistAttestationCalls, wantEvents) {
		t.Fatalf("attestations = %+v, want event ids %+v",
			fn.persistAttestationCalls, wantEvents)
	}
	wantVerdictToken := "token-" + wantEvents[1].String()
	if submitted.Token.Token != wantVerdictToken {
		t.Fatalf("inline submit token = %q, want pre-trade verdict token %q",
			submitted.Token.Token, wantVerdictToken)
	}
	if gotCommitToken := "token-" + wantEvents[2].String(); submitted.Token.Token == gotCommitToken {
		t.Fatalf("inline submit token returned committed attestation %q", gotCommitToken)
	}

	// Read the order back by its external-id handle: the verdict event carries its
	// 1:1 signed attestation.
	detail, err := svc.GetOrder(ctx, order.ExternalID.String())
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	att := verdictAttestation(t, detail)
	if att == nil {
		t.Fatalf("order detail must surface the signed verdict attestation")
	}
	if att.Token == "" || att.KeyID != "key-1" {
		t.Fatalf("read-back attestation = %+v, want signed envelope", att)
	}
}

func TestService_SubmitOrderApprovalReadBackESignOff(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{noESign: true}
	svc, fn := newTestServiceWithSigner(signer)
	ctx := context.Background()

	submitted, err := svc.SubmitOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	order := submitted.Order
	if order.ExternalID.IsZero() {
		t.Fatalf("submitted order must carry an external id")
	}
	if submitted.Token.Token == "" ||
		submitted.Token.KeyID != "" ||
		submitted.Token.Verdict != "accept" ||
		submitted.Token.Signed {
		t.Fatalf("inline eSign-off submit token = %+v, want alg none verdict", submitted.Token)
	}
	if len(signer.signed) != 0 {
		t.Fatalf("eSign-off must not sign")
	}
	wantEvents := eventIDsByType(t, svc, order.ExternalID.String(),
		domain.OrderEventSubmitted,
		domain.OrderEventPreTradeAccepted,
		domain.OrderEventReservationCommitted,
	)
	if !reflect.DeepEqual(fn.persistAttestationCalls, wantEvents) {
		t.Fatalf("attestations = %+v, want event ids %+v",
			fn.persistAttestationCalls, wantEvents)
	}

	detail, err := svc.GetOrder(ctx, order.ExternalID.String())
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	att := verdictAttestation(t, detail)
	if att == nil {
		t.Fatalf("order detail must surface the unsigned verdict attestation")
	}
	if att.Alg != fwsigning.AlgNone || att.KeyID != "" {
		t.Fatalf("read-back attestation = %+v, want unsigned envelope", att)
	}
	if att.Token == "" {
		t.Fatalf("unsigned attestation token must be bound")
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionApprovalIssued) {
		t.Fatalf("eSign-off approval must audit approval_issued: %+v", fn.auditCalls)
	}
	if hasAudit(fn.auditCalls, domain.AuditActionApprovalFailed) {
		t.Fatalf("eSign-off approval must not audit approval_failed: %+v", fn.auditCalls)
	}
}

func TestService_SubmitOrderApprovalPersistFailureAudited(t *testing.T) {
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	fn.persistAttestationErr = errors.New("approval store down")

	_, err := svc.SubmitOrder(context.Background(), sampleOrder())
	if !errors.Is(err, fn.persistAttestationErr) {
		t.Fatalf("SubmitOrder error = %v, want attestation persist error", err)
	}
	if len(fn.persistAttestationCalls) != 0 {
		t.Fatalf("persist attestation calls = %d, want rollback to 0", len(fn.persistAttestationCalls))
	}
	if hasAudit(fn.auditCalls, domain.AuditActionApprovalIssued) {
		t.Fatalf("failed approval must not audit approval_issued: %+v", fn.auditCalls)
	}
	if !hasAuditDetail(fn.auditCalls, domain.AuditActionApprovalFailed, "approval store down") {
		t.Fatalf("failed approval must audit approval_failed: %+v", fn.auditCalls)
	}
}

func TestService_SubmitOrderMissingAttestationFailsClosedInternal(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	fn.skipAttestNewEvents = true

	_, err := svc.SubmitOrder(context.Background(), sampleOrder())
	if err == nil {
		t.Fatal("SubmitOrder succeeded without an attestation")
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SubmitOrder error = %v, want internal class", err)
	}
}

func TestService_SubmitOrderRiskRejectReturnsSignedDecision(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{
		signToken: func(p domain.ApprovalPayload) string {
			return "token-" + p.EventExternalID
		},
	}
	svc, fn := newTestServiceWithSigner(signer)
	rejects := []domain.OrderReject{
		{
			Code:    "max_order_size",
			Scope:   "order",
			Policy:  "order-size",
			Reason:  "order too large",
			Details: "qty=100",
		},
		{
			Code:    "notional_limit",
			Scope:   "account",
			Policy:  "notional",
			Reason:  "notional too large",
			Details: "notional=10000",
		},
	}
	fn.submitResult = &engine.OrderResult{Accepted: false, Rejects: rejects}

	submitted, err := svc.SubmitOrder(context.Background(), sampleOrder())
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if submitted.Order.Status != domain.OrderStatusRejected {
		t.Fatalf("order status = %q, want rejected", submitted.Order.Status)
	}
	if submitted.Token.Token == "" ||
		submitted.Token.KeyID != "key-1" ||
		submitted.Token.OrderExternalID != submitted.Order.ExternalID.String() ||
		submitted.Token.Verdict != "reject" ||
		!submitted.Token.Signed {
		t.Fatalf("inline submit token = %+v, want signed reject verdict", submitted.Token)
	}
	if !reflect.DeepEqual(submitted.Token.Reasons, rejects) {
		t.Fatalf("reject reasons = %+v, want %+v", submitted.Token.Reasons, rejects)
	}
	if len(signer.signed) != 2 || signer.signed[1].Verdict != "reject" {
		t.Fatalf("signed payloads = %+v, want one reject verdict", signer.signed)
	}
	wantEvents := eventIDsByType(t, svc, submitted.Order.ExternalID.String(),
		domain.OrderEventSubmitted,
		domain.OrderEventPreTradeRejected,
	)
	if want := "token-" + wantEvents[1].String(); submitted.Token.Token != want {
		t.Fatalf("inline reject token = %q, want pre-trade reject token %q",
			submitted.Token.Token, want)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionApprovalIssued) {
		t.Fatalf("reject decision must audit approval_issued: %+v", fn.auditCalls)
	}
}

func TestService_SubmitOrderTokenSigningFailureFailsClosed(t *testing.T) {
	signer := &fakeSigner{signErr: errors.New("signing down")}
	svc, fn := newTestServiceWithSigner(signer)

	_, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeHold)
	if !errors.Is(err, signer.signErr) {
		t.Fatalf("SubmitOrderToken error = %v, want signing failure", err)
	}
	if hasAudit(fn.auditCalls, domain.AuditActionApprovalIssued) {
		t.Fatalf("failed signing must not audit approval_issued: %+v", fn.auditCalls)
	}
	if len(fn.orders) != 0 || len(fn.orderEvents) != 0 || len(fn.attestations) != 0 {
		t.Fatalf("failed signing left fake durable state: orders=%+v events=%+v atts=%+v",
			fn.orders, fn.orderEvents, fn.attestations)
	}
}

func TestService_ConfirmExecutionSigningFailureFailsClosed(t *testing.T) {
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)
	beforeOrder := fn.orders[domain.ExternalID(tok.OrderExternalID)]
	beforeEvents := slices.Clone(fn.orderEvents[domain.ExternalID(tok.OrderExternalID)])
	signer.signErr = errors.New("confirm signing down")

	_, _, err := svc.ConfirmExecution(
		context.Background(), tok.OrderExternalID, tok.Token, false)
	if !errors.Is(err, signer.signErr) {
		t.Fatalf("ConfirmExecution error = %v, want signing failure", err)
	}
	afterOrder := fn.orders[domain.ExternalID(tok.OrderExternalID)]
	if afterOrder.Status != beforeOrder.Status {
		t.Fatalf("order status after failed confirm = %q, want %q",
			afterOrder.Status, beforeOrder.Status)
	}
	if !reflect.DeepEqual(fn.orderEvents[domain.ExternalID(tok.OrderExternalID)], beforeEvents) {
		t.Fatalf("events after failed confirm = %+v, want %+v",
			fn.orderEvents[domain.ExternalID(tok.OrderExternalID)], beforeEvents)
	}
}

func TestService_CancelOrderSigningFailureFailsClosed(t *testing.T) {
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)
	beforeOrder := fn.orders[domain.ExternalID(tok.OrderExternalID)]
	beforeEvents := slices.Clone(fn.orderEvents[domain.ExternalID(tok.OrderExternalID)])
	signer.signErr = errors.New("cancel signing down")

	_, _, err := svc.CancelOrder(
		context.Background(), tok.OrderExternalID, tok.Token, "operator", false)
	if !errors.Is(err, signer.signErr) {
		t.Fatalf("CancelOrder error = %v, want signing failure", err)
	}
	afterOrder := fn.orders[domain.ExternalID(tok.OrderExternalID)]
	if afterOrder.Status != beforeOrder.Status {
		t.Fatalf("order status after failed cancel = %q, want %q",
			afterOrder.Status, beforeOrder.Status)
	}
	if !reflect.DeepEqual(fn.orderEvents[domain.ExternalID(tok.OrderExternalID)], beforeEvents) {
		t.Fatalf("events after failed cancel = %+v, want %+v",
			fn.orderEvents[domain.ExternalID(tok.OrderExternalID)], beforeEvents)
	}
}

// TestService_SubmitOrderTokenHonorsSuppliedExternalID covers the create-once
// id-honoring contract: a caller-supplied order external id is created exactly
// once and the issued token (and the signed payload) carry that same id, so the
// confirm that follows resolves the same order.
func TestService_SubmitOrderTokenHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)

	supplied := mdID("supplied-order-id")
	o := sampleOrder()
	o.ExternalID = supplied

	tok, err := svc.SubmitOrderToken(context.Background(), o, backend.SubmitModeHold)
	if err != nil {
		t.Fatalf("SubmitOrderToken hold: %v", err)
	}
	if tok.OrderExternalID != supplied.String() {
		t.Fatalf("token order id = %q, want supplied %q",
			tok.OrderExternalID, supplied.String())
	}
	// Exactly one order row was created under the supplied id.
	if len(fn.orders) != 1 {
		t.Fatalf("orders created = %d, want exactly 1", len(fn.orders))
	}
	if _, ok := fn.orders[supplied]; !ok {
		t.Fatalf("no order recorded under supplied id %q", supplied)
	}
	if signer.signed[0].OrderExternalID != supplied.String() {
		t.Fatalf("payload order id = %q, want supplied %q",
			signer.signed[0].OrderExternalID, supplied.String())
	}

	// Confirm resolves the very order the token created.
	confirmed, _, err := svc.ConfirmExecution(
		context.Background(), tok.OrderExternalID, tok.Token, false)
	if err != nil {
		t.Fatalf("ConfirmExecution: %v", err)
	}
	if confirmed.ExternalID != supplied {
		t.Fatalf("confirmed order id = %q, want supplied %q",
			confirmed.ExternalID, supplied)
	}
	if confirmed.Status != domain.OrderStatusCommitted {
		t.Fatalf("confirm must commit the supplied order, got %q", confirmed.Status)
	}
}

// TestService_SubmitOrderTokenGeneratesExternalIDWhenAbsent covers the absent-id
// path: with no supplied id the store mints one and the token returns it.
func TestService_SubmitOrderTokenGeneratesExternalIDWhenAbsent(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)

	tok, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeHold)
	if err != nil {
		t.Fatalf("SubmitOrderToken hold: %v", err)
	}
	if tok.OrderExternalID == "" {
		t.Fatalf("token must carry a generated order id")
	}
	id, err := domain.ParseExternalID(tok.OrderExternalID)
	if err != nil {
		t.Fatalf("generated order id not canonical: %v", err)
	}
	if len(fn.orders) != 1 {
		t.Fatalf("orders created = %d, want exactly 1", len(fn.orders))
	}
	if _, ok := fn.orders[id]; !ok {
		t.Fatalf("generated order id not recorded")
	}
}

// TestService_SubmitOrderTokenDuplicateSuppliedIDConflicts covers the conflict
// path: a duplicate supplied id surfaces domain.ErrAlreadyExists unchanged from
// the store, never rewrapped into a generic error.
func TestService_SubmitOrderTokenDuplicateSuppliedIDConflicts(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	fn.submitErr = fmt.Errorf("create order: %w", domain.ErrAlreadyExists)

	o := sampleOrder()
	o.ExternalID = mdID("dup-order-id")
	_, err := svc.SubmitOrderToken(context.Background(), o, backend.SubmitModeHold)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate supplied id error = %v, want ErrAlreadyExists", err)
	}
}

// TestService_SubmitOrderHonorsSuppliedExternalID covers the like-the-API submit
// path (POST /orders): a supplied id is created once and returned verbatim.
func TestService_SubmitOrderHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)

	supplied := mdID("supplied-submit-id")
	o := sampleOrder()
	o.ExternalID = supplied
	submitted, err := svc.SubmitOrder(context.Background(), o)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	order := submitted.Order
	if order.ExternalID != supplied {
		t.Fatalf("returned order id = %q, want supplied %q", order.ExternalID, supplied)
	}
	if submitted.Token.OrderExternalID != supplied.String() ||
		submitted.Token.Verdict != "accept" {
		t.Fatalf("submit token = %+v, want supplied id accept verdict", submitted.Token)
	}
	if len(fn.orders) != 1 {
		t.Fatalf("orders created = %d, want exactly 1", len(fn.orders))
	}
}

// TestService_SubmitOrderDuplicateSuppliedIDConflicts covers the POST /orders
// conflict propagation.
func TestService_SubmitOrderDuplicateSuppliedIDConflicts(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	fn.submitErr = fmt.Errorf("create order: %w", domain.ErrAlreadyExists)

	o := sampleOrder()
	o.ExternalID = mdID("dup-submit-id")
	_, err := svc.SubmitOrder(context.Background(), o)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate supplied id error = %v, want ErrAlreadyExists", err)
	}
}

// TestService_GetOrderAddressedByExternalID locks order addressing by the opaque
// external-id string handle, rejecting a malformed handle before routing.
func TestService_GetOrderAddressedByExternalID(t *testing.T) {
	t.Parallel()
	svc, _ := newTestServiceWithSigner(&fakeSigner{})
	ctx := context.Background()

	tok := mustHold(t, svc)
	detail, err := svc.GetOrder(ctx, tok.OrderExternalID)
	if err != nil {
		t.Fatalf("GetOrder by external id: %v", err)
	}
	if detail.Order.ExternalID != orderID(t, tok) {
		t.Fatalf("GetOrder returned %q, want %q",
			detail.Order.ExternalID, orderID(t, tok))
	}

	if _, err := svc.GetOrder(ctx, "not-an-external-id"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown order handle = %v, want ErrNotFound", err)
	}
}

// hasAudit reports whether the recorded audit entries include one with action.
func hasAudit(entries []store.AuditEntry, action domain.AuditAction) bool {
	for _, e := range entries {
		if e.Action == action {
			return true
		}
	}
	return false
}

func hasAuditDetail(
	entries []store.AuditEntry, action domain.AuditAction, detail string,
) bool {
	for _, e := range entries {
		if e.Action == action && strings.Contains(e.Detail, detail) {
			return true
		}
	}
	return false
}
