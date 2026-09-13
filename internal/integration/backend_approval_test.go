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

package integration_test

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

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
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
	_ context.Context, token string, _ fwsigning.VerifyParams,
) (fwsigning.VerifyResult, error) {
	if s.verifyErr != nil {
		return fwsigning.VerifyResult{}, s.verifyErr
	}
	if s.verifyResult != nil {
		return *s.verifyResult, nil
	}
	if env, err := decodeApprovalEnvelope(token); err == nil {
		return fwsigning.VerifyResult{
			Payload: env.Approval,
			Signed:  env.Alg != fwsigning.AlgNone,
		}, nil
	}
	for i := len(s.signed) - 1; i >= 0; i-- {
		payload := s.signed[i]
		if payload.RequestType == string(domain.AttestationRequestSubmit) &&
			payload.Verdict != "" && payload.Result == nil {
			return fwsigning.VerifyResult{Payload: payload, Signed: true}, nil
		}
	}
	return fwsigning.VerifyResult{
		Payload: domain.ApprovalPayload{
			ApprovalID: "approval-1",
			Mode:       backend.SubmitModeHold,
			Verdict:    "accept",
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

func (*fakeSigner) Reload(context.Context) error {
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

func TestService_SubmitOrderTokenWorkflowIssuesSignedToken(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	ctx := auth.ContextWithCaller(context.Background(), domain.Caller{
		Source: domain.SourceAPI, Principal: "trader-accept",
	})

	tok, err := svc.SubmitOrderToken(
		ctx, sampleOrder(), backend.SubmitModeHold, domain.MissingAccountCreate,
	)
	if err != nil {
		t.Fatalf("SubmitOrderToken workflow: %v", err)
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
	// The HTTP token is the workflow-mode envelope; the submit also persists a separate
	// display envelope, so two payloads are signed.
	p := httpTokenPayload(t, signer, backend.SubmitModeHold)
	if p.Verdict != "accept" {
		t.Fatalf("payload verdict = %q, want accept", p.Verdict)
	}
	if p.EstimatePrice != "100" {
		t.Fatalf("payload estimate = %q, want engine lock price", p.EstimatePrice)
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
	if p.Principal != "trader-accept" {
		t.Fatalf("payload principal = %q, want trader-accept", p.Principal)
	}
	if orderByToken(t, fn, tok).Status != domain.OrderStatusCommitted {
		t.Fatalf("workflow submit must leave the order committed, got %q",
			orderByToken(t, fn, tok).Status)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionApprovalIssued) {
		t.Fatalf("workflow submit must audit approval_issued: %+v", fn.auditCalls)
	}
	// The accepted order carries persisted 1:1 attestations on its submitted and
	// verdict events, surfaced by GetOrder.
	wantEvents := eventIDsByType(t, svc, tok.OrderExternalID,
		domain.OrderEventSubmitted,
		domain.OrderEventPreTradeAccepted,
		domain.OrderEventCommitted,
	)
	if !reflect.DeepEqual(fn.persistAttestationCalls, wantEvents) {
		t.Fatalf("workflow attestations = %+v, want event ids %+v",
			fn.persistAttestationCalls, wantEvents)
	}
	detail := getOrderByToken(t, svc, tok)
	att := verdictAttestation(t, detail)
	if att == nil || att.Token == "" || att.KeyID != "key-1" ||
		att.Alg != fwsigning.AlgEd25519 {
		t.Fatalf("workflow order must surface a signed verdict attestation, got %+v", att)
	}
}

func TestService_SubmitOrderTokenImmediateSettles(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)

	tok, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeImmediate, domain.MissingAccountCreate)
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
	if p.Principal != "" {
		t.Fatalf("unstamped submit invented principal %q", p.Principal)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionApprovalIssued) {
		t.Fatalf("immediate must audit approval_issued")
	}
	// The filled order carries persisted 1:1 attestations on every immediate
	// lifecycle event, surfaced by GetOrder.
	wantEvents := eventIDsByType(t, svc, tok.OrderExternalID,
		domain.OrderEventSubmitted,
		domain.OrderEventPreTradeAccepted,
		domain.OrderEventCommitted,
		domain.OrderEventFill,
	)
	if !reflect.DeepEqual(fn.persistAttestationCalls, wantEvents) {
		t.Fatalf("immediate attestations = %+v, want event ids %+v",
			fn.persistAttestationCalls, wantEvents)
	}
	if att := verdictAttestation(t, getOrderByToken(t, svc, tok)); att == nil {
		t.Fatalf("immediate order must surface a signed verdict attestation")
	}
	detail := getOrderByToken(t, svc, tok)
	var fill domain.OrderEvent
	for _, event := range detail.Events {
		if event.Type == domain.OrderEventFill {
			fill = event
			break
		}
	}
	if fill.Payload.ExecutionReport == nil ||
		fill.Payload.ExecutionReport.ExternalID.IsZero() ||
		fill.Payload.LeavesQuantity != "0" ||
		fill.Payload.OrderStatus != string(domain.OrderStatusFilled) {
		t.Fatalf("immediate fill event = %+v, want complete execution report", fill)
	}
	var reportPayload *domain.ApprovalPayload
	for i := range signer.signed {
		p := &signer.signed[i]
		if p.RequestType == string(domain.AttestationRequestExecutionReport) {
			reportPayload = p
		}
	}
	if reportPayload == nil || reportPayload.ExecutionReport == nil ||
		reportPayload.ExecutionReport.ExternalID !=
			fill.Payload.ExecutionReport.ExternalID ||
		reportPayload.Result == nil ||
		reportPayload.Result.LeavesQuantity != "0" ||
		reportPayload.Result.OrderStatus != string(domain.OrderStatusFilled) {
		t.Fatalf("immediate signed report payload = %+v", reportPayload)
	}
}

func TestService_SubmitOrderTokenDefaultsImmediate(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)

	tok, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), "", domain.MissingAccountCreate)
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
	svc, fn := newTestServiceWithSigner(t, signer)
	fn.submitResult = &engine.OrderResult{
		Accepted: false,
		Rejects:  []domain.OrderReject{{Code: "insufficient_funds", Reason: "no funds"}},
	}

	o := sampleOrder()
	o.ExternalID = mdID("reject-order-id")
	tok, err := svc.SubmitOrderToken(context.Background(), o, backend.SubmitModeHold, domain.MissingAccountCreate)
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
	svc, fn := newTestServiceWithSigner(t, signer)

	tok, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeHold, domain.MissingAccountCreate)
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
		domain.OrderEventCommitted,
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

func TestService_ShortcutsESignOffPersistUnsignedEvents(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{noESign: true}
	svc, fn := newTestServiceWithSigner(t, signer)

	confirmTok := mustWorkflow(t, svc)
	confirmed, confirmAtt, err := svc.ConfirmExecution(
		context.Background(), confirmTok.OrderExternalID, confirmTok.Token)
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
		domain.OrderEventCommitted,
		domain.OrderEventConfirmed,
	)
	if !slices.Contains(fn.persistAttestationCalls, confirmEvents[3]) {
		t.Fatalf("confirm did not persist unsigned confirmation attestation: calls=%+v",
			fn.persistAttestationCalls)
	}

	cancelTok := mustWorkflow(t, svc)
	cancelled, cancelAtt, err := svc.CancelOrder(
		context.Background(), cancelTok.OrderExternalID, cancelTok.Token, "10", "operator")
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
		domain.OrderEventCommitted,
		domain.OrderEventCancelled,
	)
	if !slices.Contains(fn.persistAttestationCalls, cancelEvents[3]) {
		t.Fatalf("cancel did not persist unsigned cancellation attestation: calls=%+v",
			fn.persistAttestationCalls)
	}
}

func TestService_ConfirmExecutionRecordsHistoryOnly(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	tok := mustWorkflow(t, svc)

	before := len(fn.persistAttestationCalls)
	order, att, err := svc.ConfirmExecution(context.Background(), tok.OrderExternalID, tok.Token)
	if err != nil {
		t.Fatalf("ConfirmExecution: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("history-only confirm changed committed status to %q", order.Status)
	}
	if len(fn.confirmCalls) != 1 || fn.confirmCalls[0] != tok.OrderExternalID {
		t.Fatalf("confirm shortcut calls = %+v", fn.confirmCalls)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionApprovalConfirmed) {
		t.Fatalf("confirm must audit approval_confirmed")
	}
	// The confirm attests a history-only confirmed event.
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
	svc, _ := newTestServiceWithSigner(t, signer)
	tok := mustWorkflow(t, svc)

	first, firstAtt, err := svc.ConfirmExecution(
		context.Background(), tok.OrderExternalID, tok.Token)
	if err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	if first.Status != domain.OrderStatusCommitted || firstAtt.Token == "" {
		t.Fatalf("first confirm = %+v att=%+v, want unchanged status with attestation",
			first, firstAtt)
	}
	second, secondAtt, err := svc.ConfirmExecution(
		context.Background(), tok.OrderExternalID, tok.Token)
	if err != nil {
		t.Fatalf("second confirm must be idempotent success, got %v", err)
	}
	if second.Status != domain.OrderStatusCommitted {
		t.Fatalf("idempotent confirm changed status to %q",
			second.Status)
	}
	if secondAtt != firstAtt {
		t.Fatalf("idempotent confirm attestation = %+v, want %+v",
			secondAtt, firstAtt)
	}
}

func TestService_ConfirmExecutionMissingAttestationFailsClosed(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	tok := mustWorkflow(t, svc)

	if _, _, err := svc.ConfirmExecution(
		context.Background(), tok.OrderExternalID, tok.Token); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	detail, err := svc.GetOrder(context.Background(), tok.OrderExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	for _, event := range detail.Events {
		if event.Type == domain.OrderEventConfirmed {
			delete(fn.attestations, event.ExternalID)
		}
	}

	_, _, err = svc.ConfirmExecution(
		context.Background(), tok.OrderExternalID, tok.Token)
	if err == nil {
		t.Fatal("idempotent confirm succeeded without persisted attestation")
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("idempotent confirm error = %v, want internal class", err)
	}
}

func TestService_ConfirmAfterCancelRequiresExplicitReport(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	tok := mustWorkflow(t, svc)

	if _, _, err := svc.CancelOrder(
		context.Background(), tok.OrderExternalID, tok.Token, "10", "operator",
	); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	_, _, err := svc.ConfirmExecution(context.Background(), tok.OrderExternalID, tok.Token)
	if !errors.Is(err, domain.ErrExecutionReportRequired) {
		t.Fatalf("confirm after cancel = %v, want explicit report", err)
	}
	if len(fn.confirmCalls) != 0 {
		t.Fatalf("confirm after cancel must not append history: %+v", fn.confirmCalls)
	}
}

func TestService_ConfirmAfterWorkflowReportRequiresExplicitReport(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	tok := mustWorkflow(t, svc)

	if _, _, err := svc.ApplyExecutionReport(context.Background(), domain.ExecutionReportInput{
		Order:          orderID(t, tok),
		LeavesQuantity: "10",
		OrderStatus:    domain.OrderStatusAccepted,
	}); err != nil {
		t.Fatalf("workflow report: %v", err)
	}
	_, _, err := svc.ConfirmExecution(context.Background(), tok.OrderExternalID, tok.Token)
	if !errors.Is(err, domain.ErrExecutionReportRequired) {
		t.Fatalf("confirm after workflow report = %v, want explicit report", err)
	}
	if len(fn.confirmCalls) != 0 {
		t.Fatalf("confirm after report appended history: %+v", fn.confirmCalls)
	}
}

func TestService_CancelAfterConfirmUsesNormalReport(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	tok := mustWorkflow(t, svc)

	if _, _, err := svc.ConfirmExecution(context.Background(), tok.OrderExternalID, tok.Token); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	cancelled, _, err := svc.CancelOrder(
		context.Background(), tok.OrderExternalID, tok.Token, "10", "operator",
	)
	if err != nil {
		t.Fatalf("cancel after history-only confirm: %v", err)
	}
	if cancelled.Status != domain.OrderStatusCancelled || len(fn.cancelCalls) != 1 {
		t.Fatalf("cancel after confirm = %+v calls=%+v", cancelled, fn.cancelCalls)
	}
}

func TestService_CancelAfterFillRequiresExplicitReport(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	tok := mustWorkflow(t, svc)

	if _, _, err := svc.ApplyExecutionReport(context.Background(), domain.ExecutionReportInput{
		Order:          orderID(t, tok),
		FillQuantity:   "10",
		FillPrice:      "100",
		LeavesQuantity: "0",
		OrderStatus:    domain.OrderStatusFilled,
	}); err != nil {
		t.Fatalf("fill report: %v", err)
	}
	_, _, err := svc.CancelOrder(
		context.Background(), tok.OrderExternalID, tok.Token, "0", "late",
	)
	if !errors.Is(err, domain.ErrExecutionReportRequired) {
		t.Fatalf("cancel after fill = %v, want explicit report", err)
	}
	if len(fn.cancelCalls) != 0 {
		t.Fatalf("cancel after fill invoked shortcut: %+v", fn.cancelCalls)
	}
}

func TestService_CancelOrderForwardsCallerLeaves(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	tok := mustWorkflow(t, svc)
	submitPayload := httpTokenPayload(t, signer, backend.SubmitModeHold)

	before := len(fn.persistAttestationCalls)
	order, att, err := svc.CancelOrder(
		context.Background(), tok.OrderExternalID, tok.Token, "7.5", "stale price",
	)
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if order.Status != domain.OrderStatusCancelled {
		t.Fatalf("cancel status = %q, want cancelled", order.Status)
	}
	if len(fn.cancelCalls) != 1 || fn.cancelCalls[0] != tok.OrderExternalID {
		t.Fatalf("cancel shortcut calls = %+v", fn.cancelCalls)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionApprovalCancelled) {
		t.Fatalf("cancel must audit approval_cancelled")
	}
	if att.Token == "" || !att.Signed {
		t.Fatalf("cancel must return a signed attestation, got %+v", att)
	}
	if len(fn.persistAttestationCalls) != before+1 {
		t.Fatalf("cancel must persist one attestation, calls=%+v", fn.persistAttestationCalls)
	}
	cancelEvents := eventIDsByType(t, svc, tok.OrderExternalID,
		domain.OrderEventCancelled,
	)
	if att.EventExternalID != cancelEvents[0].String() {
		t.Fatalf("returned cancel event = %q, want %q",
			att.EventExternalID, cancelEvents[0].String())
	}
	if len(fn.execReports) != 1 ||
		fn.execReports[0].LeavesQuantity != "7.5" ||
		fn.execReports[0].OrderStatus != domain.OrderStatusCancelled {
		t.Fatalf("synthetic execution report = %+v", fn.execReports)
	}
	lastPayload := signer.signed[len(signer.signed)-1]
	if lastPayload.ExecutionReport == nil ||
		lastPayload.ExecutionReport.LeavesQuantity != "7.5" ||
		lastPayload.ApprovalRef != submitPayload.ApprovalID ||
		lastPayload.Result == nil || lastPayload.Result.Outcome != "cancelled" {
		t.Fatalf("cancel attestation payload = %+v", lastPayload)
	}
	canonical, err := json.Marshal(lastPayload)
	if err != nil {
		t.Fatalf("json.Marshal(cancel payload): %v", err)
	}
	if strings.Contains(string(canonical), `"lock":`) {
		t.Fatalf("cancel attestation exposed opaque order lock: %s", canonical)
	}
}

func TestService_CancelOrderNoopMissingAttestationFailsClosed(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	tok := mustWorkflow(t, svc)
	fn.cancelNoop = true

	_, _, err := svc.CancelOrder(
		context.Background(), tok.OrderExternalID, tok.Token, "10", "already resolved")
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
	svc, fn := newTestServiceWithSigner(t, signer)
	tok := mustWorkflow(t, svc)
	fn.confirmCalls = nil

	_, _, err := svc.ConfirmExecution(context.Background(), tok.OrderExternalID, tok.Token)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad token must reject before commit, got %v", err)
	}
	if len(fn.confirmCalls) != 0 {
		t.Fatalf("verification failure must not append confirmation history")
	}
}

func TestService_ConfirmImmediateTokenConflictsBeforeEngine(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{
		verifyResult: &fwsigning.VerifyResult{
			Payload: domain.ApprovalPayload{
				ApprovalID: "approval-immediate",
				Mode:       backend.SubmitModeImmediate,
				Verdict:    "accept",
			},
			Signed: true,
		},
	}
	svc, fn := newTestServiceWithSigner(t, signer)
	tok, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeHold, domain.MissingAccountCreate)
	if err != nil {
		t.Fatalf("workflow setup: %v", err)
	}

	_, _, err = svc.ConfirmExecution(context.Background(), tok.OrderExternalID, tok.Token)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("confirm immediate token must conflict, got %v", err)
	}
	if len(fn.confirmCalls) != 0 {
		t.Fatalf("immediate token appended confirmation history: %+v", fn.confirmCalls)
	}
}

func TestService_ShortcutsRejectNonAcceptVerdict(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{
		verifyResult: &fwsigning.VerifyResult{
			Payload: domain.ApprovalPayload{
				ApprovalID: "approval-rejected",
				Mode:       backend.SubmitModeHold,
				Verdict:    "reject",
			},
			Signed: true,
		},
	}
	svc, fn := newTestServiceWithSigner(t, signer)
	tok, err := svc.SubmitOrderToken(
		context.Background(), sampleOrder(), backend.SubmitModeHold, domain.MissingAccountCreate,
	)
	if err != nil {
		t.Fatalf("workflow setup: %v", err)
	}
	if _, _, err := svc.ConfirmExecution(
		context.Background(), tok.OrderExternalID, tok.Token,
	); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("confirm reject verdict = %v, want conflict", err)
	}
	if _, _, err := svc.CancelOrder(
		context.Background(), tok.OrderExternalID, tok.Token, "10", "operator",
	); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cancel reject verdict = %v, want conflict", err)
	}
	if len(fn.confirmCalls) != 0 || len(fn.cancelCalls) != 0 {
		t.Fatalf("rejected verdict reached shortcuts: confirm=%+v cancel=%+v",
			fn.confirmCalls, fn.cancelCalls)
	}
}

func TestService_ShortcutsRejectCommittedEventAttestation(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	tok := mustWorkflow(t, svc)
	detail := getOrderByToken(t, svc, tok)

	var committedEvent domain.OrderEvent
	for _, event := range detail.Events {
		if event.Type == domain.OrderEventCommitted {
			committedEvent = event
			break
		}
	}
	if committedEvent.ExternalID.IsZero() || committedEvent.Attestation == nil {
		t.Fatalf("committed event attestation missing: %+v", detail.Events)
	}
	var committedPayload domain.ApprovalPayload
	for _, payload := range signer.signed {
		if payload.EventExternalID == committedEvent.ExternalID.String() {
			committedPayload = payload
			break
		}
	}
	if committedPayload.Result == nil || committedPayload.ApprovalRef == "" {
		t.Fatalf("committed payload is not derived lifecycle evidence: %+v", committedPayload)
	}
	signer.verifyResult = &fwsigning.VerifyResult{
		Payload: committedPayload,
		Signed:  true,
	}
	derivedToken := committedEvent.Attestation.Token

	if _, _, err := svc.ConfirmExecution(
		context.Background(), tok.OrderExternalID, derivedToken,
	); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("confirm with committed attestation = %v, want conflict", err)
	}
	if _, _, err := svc.CancelOrder(
		context.Background(), tok.OrderExternalID, derivedToken, "", "operator",
	); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cancel with committed attestation = %v, want conflict", err)
	}
	if len(fn.confirmCalls) != 0 || len(fn.cancelCalls) != 0 {
		t.Fatalf(
			"derived attestation reached shortcuts: confirm=%+v cancel=%+v",
			fn.confirmCalls,
			fn.cancelCalls,
		)
	}
}

func TestService_SubmitOrderTokenValidatesMode(t *testing.T) {
	t.Parallel()
	svc, _ := newTestServiceWithSigner(t, &fakeSigner{})
	if _, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), "weird", domain.MissingAccountCreate); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown mode must be ErrInvalid, got %v", err)
	}
}

func TestService_SigningMethodsRequireSigner(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t) // nil signer
	ctx := context.Background()

	if _, err := svc.GenerateSigningKey(ctx); !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("nil signer GenerateSigningKey = %v, want ErrNotImplemented", err)
	}
	if _, err := svc.SubmitOrderToken(ctx, sampleOrder(), backend.SubmitModeHold, domain.MissingAccountCreate); !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("nil signer SubmitOrderToken = %v, want ErrNotImplemented", err)
	}
}

func TestService_SetNoESignAudits(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)

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
	svc, fn := newTestServiceWithSigner(t, signer)

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

// httpTokenPayload returns the signed pre-trade verdict payload for the given
// submit mode. A submit records multiple attested lifecycle events; the returned
// approval token is the one that carries the verdict and no result section.
func httpTokenPayload(t *testing.T, signer *fakeSigner, mode string) domain.ApprovalPayload {
	t.Helper()
	for i := len(signer.signed) - 1; i >= 0; i-- {
		if signer.signed[i].Mode == mode &&
			signer.signed[i].RequestType == string(domain.AttestationRequestSubmit) &&
			signer.signed[i].Verdict != "" && signer.signed[i].Result == nil {
			return signer.signed[i]
		}
	}
	t.Fatalf("no signed payload with mode %q: %+v", mode, signer.signed)
	return domain.ApprovalPayload{}
}

// mustWorkflow issues a workflow token and fails the test on error.
func mustWorkflow(t *testing.T, svc *backend.Service) backend.ApprovalToken {
	t.Helper()
	tok, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeHold, domain.MissingAccountCreate)
	if err != nil {
		t.Fatalf("workflow setup: %v", err)
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
	svc, fn := newTestServiceWithSigner(t, signer)
	ctx := context.Background()

	token, err := svc.SubmitOrderToken(ctx, sampleOrder(), backend.SubmitModeHold, domain.MissingAccountCreate)
	if err != nil {
		t.Fatalf("SubmitOrderToken: %v", err)
	}
	detail, err := svc.GetOrder(ctx, token.OrderExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	order := detail.Order
	if order.ExternalID.IsZero() {
		t.Fatalf("submitted order must carry an external id")
	}
	if token.Token == "" ||
		token.KeyID != "key-1" ||
		token.OrderExternalID != order.ExternalID.String() ||
		token.Verdict != "accept" ||
		len(token.Reasons) != 0 {
		t.Fatalf("inline submit token = %+v, want signed accept verdict", token)
	}
	// The signer recorded every lifecycle payload; the returned verdict payload
	// must bind the order's external id, not any integer/surrogate handle.
	if len(signer.signed) != 3 {
		t.Fatalf("Sign calls = %d, want 3", len(signer.signed))
	}
	verdictPayload := httpTokenPayload(t, signer, backend.SubmitModeHold)
	if verdictPayload.OrderExternalID != order.ExternalID.String() {
		t.Fatalf("payload order handle = %q, want %q",
			verdictPayload.OrderExternalID, order.ExternalID.String())
	}
	// The attestation persists against the verdict event's external id.
	wantEvents := eventIDsByType(t, svc, order.ExternalID.String(),
		domain.OrderEventSubmitted,
		domain.OrderEventPreTradeAccepted,
		domain.OrderEventCommitted,
	)
	if !reflect.DeepEqual(fn.persistAttestationCalls, wantEvents) {
		t.Fatalf("attestations = %+v, want event ids %+v",
			fn.persistAttestationCalls, wantEvents)
	}
	wantVerdictToken := "token-" + wantEvents[1].String()
	if token.Token != wantVerdictToken {
		t.Fatalf("inline submit token = %q, want pre-trade verdict token %q",
			token.Token, wantVerdictToken)
	}
	if gotCommitToken := "token-" + wantEvents[2].String(); token.Token == gotCommitToken {
		t.Fatalf("inline submit token returned committed attestation %q", gotCommitToken)
	}

	// Read the order back by its external-id handle: the verdict event carries its
	// 1:1 signed attestation.
	detail, err = svc.GetOrder(ctx, order.ExternalID.String())
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
	svc, fn := newTestServiceWithSigner(t, signer)
	ctx := context.Background()

	token, err := svc.SubmitOrderToken(ctx, sampleOrder(), backend.SubmitModeHold, domain.MissingAccountCreate)
	if err != nil {
		t.Fatalf("SubmitOrderToken: %v", err)
	}
	detail, err := svc.GetOrder(ctx, token.OrderExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	order := detail.Order
	if order.ExternalID.IsZero() {
		t.Fatalf("submitted order must carry an external id")
	}
	if token.Token == "" ||
		token.KeyID != "" ||
		token.Verdict != "accept" ||
		token.Signed {
		t.Fatalf("inline eSign-off submit token = %+v, want alg none verdict", token)
	}
	if len(signer.signed) != 0 {
		t.Fatalf("eSign-off must not sign")
	}
	wantEvents := eventIDsByType(t, svc, order.ExternalID.String(),
		domain.OrderEventSubmitted,
		domain.OrderEventPreTradeAccepted,
		domain.OrderEventCommitted,
	)
	if !reflect.DeepEqual(fn.persistAttestationCalls, wantEvents) {
		t.Fatalf("attestations = %+v, want event ids %+v",
			fn.persistAttestationCalls, wantEvents)
	}

	detail, err = svc.GetOrder(ctx, order.ExternalID.String())
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

func TestService_SubmitOrderTokenApprovalPersistFailureAudited(t *testing.T) {
	for _, mode := range []string{
		backend.SubmitModeHold,
		backend.SubmitModeImmediate,
	} {
		t.Run(mode, func(t *testing.T) {
			signer := &fakeSigner{}
			svc, fn := newTestServiceWithSigner(t, signer)
			fn.persistAttestationErr = errors.New("approval store down")

			_, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), mode, domain.MissingAccountCreate)
			if !errors.Is(err, fn.persistAttestationErr) {
				t.Fatalf("SubmitOrderToken error = %v, want persistence failure", err)
			}
			if hasAudit(fn.auditCalls, domain.AuditActionApprovalIssued) {
				t.Fatalf("failed approval audited approval_issued: %+v", fn.auditCalls)
			}
			if !hasAuditDetail(
				fn.auditCalls,
				domain.AuditActionApprovalFailed,
				"approval store down",
			) {
				t.Fatalf("failed approval did not audit approval_failed: %+v", fn.auditCalls)
			}
		})
	}
}

func TestService_SubmitOrderTokenReturnsTokenWhenIssuedAuditFails(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	fn.auditErr = errors.New("audit store down")

	tok, err := svc.SubmitOrderToken(
		context.Background(), sampleOrder(), backend.SubmitModeHold,
		domain.MissingAccountCreate,
	)
	if err != nil {
		t.Fatalf("SubmitOrderToken post-commit audit failure: %v", err)
	}
	if tok.Token == "" {
		t.Fatal("SubmitOrderToken returned an empty token after successful commit")
	}
	if orderByToken(t, fn, tok).Status != domain.OrderStatusCommitted {
		t.Fatalf("successful submit did not keep its committed order: %+v", tok)
	}
	attempts := 0
	for _, entry := range fn.auditCalls {
		if entry.Action == domain.AuditActionApprovalIssued {
			attempts++
		}
	}
	if attempts != 1 {
		t.Fatalf("approval_issued audit attempts = %d, want exactly one failed attempt", attempts)
	}
}

func TestService_SubmitOrderMissingAttestationFailsClosedInternal(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	fn.skipAttestNewEvents = true

	_, err := svc.SubmitOrderToken(
		context.Background(), sampleOrder(), backend.SubmitModeHold, domain.MissingAccountCreate,
	)
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
	svc, fn := newTestServiceWithSigner(t, signer)
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
	ctx := auth.ContextWithCaller(context.Background(), domain.Caller{
		Source: domain.SourceAPI, Principal: "trader-reject",
	})

	token, err := svc.SubmitOrderToken(
		ctx, sampleOrder(), backend.SubmitModeHold, domain.MissingAccountCreate,
	)
	if err != nil {
		t.Fatalf("SubmitOrderToken: %v", err)
	}
	detail, err := svc.GetOrder(context.Background(), token.OrderExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusRejected {
		t.Fatalf("order status = %q, want rejected", detail.Order.Status)
	}
	if token.Token == "" ||
		token.KeyID != "key-1" ||
		token.OrderExternalID != detail.Order.ExternalID.String() ||
		token.Verdict != "reject" ||
		!token.Signed {
		t.Fatalf("inline submit token = %+v, want signed reject verdict", token)
	}
	if !reflect.DeepEqual(token.Reasons, rejects) {
		t.Fatalf("reject reasons = %+v, want %+v", token.Reasons, rejects)
	}
	if len(signer.signed) != 2 || signer.signed[1].Verdict != "reject" {
		t.Fatalf("signed payloads = %+v, want one reject verdict", signer.signed)
	}
	payload := signer.signed[1]
	first := rejects[0]
	if payload.Principal != "trader-reject" ||
		payload.RejectCode != first.Code ||
		payload.RejectScope != first.Scope ||
		payload.RejectPolicy != first.Policy ||
		payload.RejectReason != first.Reason ||
		payload.RejectDetails != first.Details ||
		!reflect.DeepEqual(payload.Rejects, rejects) {
		t.Fatalf("signed reject payload = %+v, want principal and exact first reject %+v", payload, first)
	}
	wantEvents := eventIDsByType(t, svc, detail.Order.ExternalID.String(),
		domain.OrderEventSubmitted,
		domain.OrderEventPreTradeRejected,
	)
	if want := "token-" + wantEvents[1].String(); token.Token != want {
		t.Fatalf("inline reject token = %q, want pre-trade reject token %q",
			token.Token, want)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionApprovalIssued) {
		t.Fatalf("reject decision must audit approval_issued: %+v", fn.auditCalls)
	}
}

func TestService_SubmitOrderTokenSigningFailureFailsClosed(t *testing.T) {
	signer := &fakeSigner{signErr: errors.New("signing down")}
	svc, fn := newTestServiceWithSigner(t, signer)

	_, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeHold, domain.MissingAccountCreate)
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
	svc, fn := newTestServiceWithSigner(t, signer)
	tok := mustWorkflow(t, svc)
	beforeOrder := fn.orders[domain.ExternalID(tok.OrderExternalID)]
	beforeEvents := slices.Clone(fn.orderEvents[domain.ExternalID(tok.OrderExternalID)])
	signer.signErr = errors.New("confirm signing down")

	_, _, err := svc.ConfirmExecution(
		context.Background(), tok.OrderExternalID, tok.Token)
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
	svc, fn := newTestServiceWithSigner(t, signer)
	tok := mustWorkflow(t, svc)
	beforeOrder := fn.orders[domain.ExternalID(tok.OrderExternalID)]
	beforeEvents := slices.Clone(fn.orderEvents[domain.ExternalID(tok.OrderExternalID)])
	signer.signErr = errors.New("cancel signing down")

	_, _, err := svc.CancelOrder(
		context.Background(), tok.OrderExternalID, tok.Token, "10", "operator")
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
// history-only confirm records against the same order.
func TestService_SubmitOrderTokenHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)

	supplied := mdID("supplied-order-id")
	o := sampleOrder()
	o.ExternalID = supplied

	tok, err := svc.SubmitOrderToken(context.Background(), o, backend.SubmitModeHold, domain.MissingAccountCreate)
	if err != nil {
		t.Fatalf("SubmitOrderToken workflow: %v", err)
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

	// Confirm records history against the very order the token created.
	confirmed, _, err := svc.ConfirmExecution(
		context.Background(), tok.OrderExternalID, tok.Token)
	if err != nil {
		t.Fatalf("ConfirmExecution: %v", err)
	}
	if confirmed.ExternalID != supplied {
		t.Fatalf("confirmed order id = %q, want supplied %q",
			confirmed.ExternalID, supplied)
	}
	if confirmed.Status != domain.OrderStatusCommitted {
		t.Fatalf("confirm changed supplied order status to %q", confirmed.Status)
	}
}

// TestService_SubmitOrderTokenGeneratesExternalIDWhenAbsent covers the absent-id
// path: with no supplied id the store mints one and the token returns it.
func TestService_SubmitOrderTokenGeneratesExternalIDWhenAbsent(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)

	tok, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeHold, domain.MissingAccountCreate)
	if err != nil {
		t.Fatalf("SubmitOrderToken workflow: %v", err)
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
	svc, fn := newTestServiceWithSigner(t, signer)
	fn.submitErr = fmt.Errorf("create order: %w", domain.ErrAlreadyExists)

	o := sampleOrder()
	o.ExternalID = mdID("dup-order-id")
	_, err := svc.SubmitOrderToken(context.Background(), o, backend.SubmitModeHold, domain.MissingAccountCreate)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate supplied id error = %v, want ErrAlreadyExists", err)
	}
}

// TestService_SubmitOrderTokenHonorsSuppliedID covers the live submit path: a
// supplied id is created once and returned verbatim.
func TestService_SubmitOrderTokenHonorsSuppliedID(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)

	supplied := mdID("supplied-submit-id")
	o := sampleOrder()
	o.ExternalID = supplied
	token, err := svc.SubmitOrderToken(
		context.Background(), o, backend.SubmitModeHold, domain.MissingAccountCreate,
	)
	if err != nil {
		t.Fatalf("SubmitOrderToken: %v", err)
	}
	detail, err := svc.GetOrder(context.Background(), token.OrderExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	order := detail.Order
	if order.ExternalID != supplied {
		t.Fatalf("returned order id = %q, want supplied %q", order.ExternalID, supplied)
	}
	if token.OrderExternalID != supplied.String() || token.Verdict != "accept" {
		t.Fatalf("submit token = %+v, want supplied id accept verdict", token)
	}
	if len(fn.orders) != 1 {
		t.Fatalf("orders created = %d, want exactly 1", len(fn.orders))
	}
}

// TestService_GetOrderAddressedByExternalID locks order addressing by the opaque
// external-id string handle, rejecting a malformed handle before routing.
func TestService_GetOrderAddressedByExternalID(t *testing.T) {
	t.Parallel()
	svc, _ := newTestServiceWithSigner(t, &fakeSigner{})
	ctx := context.Background()

	tok := mustWorkflow(t, svc)
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
	for _, entry := range entries {
		if entry.Action == action && strings.Contains(entry.Detail, detail) {
			return true
		}
	}
	return false
}
