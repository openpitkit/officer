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
	"errors"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
	"go.openpit.dev/officer/internal/signing"
	"go.openpit.dev/officer/internal/store"
)

// fakeSigner is a backend.SigningService stand-in for the approval-flow tests.
// Sign records the payload it was handed and returns a deterministic token; the
// matching Verify returns the recorded payload re-bound against the expectation.
type fakeSigner struct {
	noESign      bool
	signed       []domain.ApprovalPayload
	verifyResult *signing.VerifyResult
	verifyErr    error
	noESignErr   error
	signErr      error
	setNoESign   []bool
}

func (s *fakeSigner) GenerateKey(context.Context) (domain.SigningKey, error) {
	return domain.SigningKey{KeyID: "key-1", Alg: signing.AlgEd25519, Active: true}, nil
}

func (s *fakeSigner) ImportKey(_ context.Context, _, _ string) (domain.SigningKey, error) {
	return domain.SigningKey{KeyID: "key-2", Alg: signing.AlgEd25519, Active: true}, nil
}

func (s *fakeSigner) ListKeys(context.Context) ([]domain.SigningKey, error) {
	return []domain.SigningKey{{KeyID: "key-1", Alg: signing.AlgEd25519, Active: true}}, nil
}

func (s *fakeSigner) ActivePublicKey(string) (string, error) { return "PUBLIC", nil }

func (s *fakeSigner) Fingerprint([]byte) string { return "fp" }

func (s *fakeSigner) Sign(payload domain.ApprovalPayload) (string, error) {
	if s.signErr != nil {
		return "", s.signErr
	}
	s.signed = append(s.signed, payload)
	return "signed-token", nil
}

func (s *fakeSigner) Verify(
	_ context.Context, _ string, _ signing.VerifyParams,
) (signing.VerifyResult, error) {
	if s.verifyErr != nil {
		return signing.VerifyResult{}, s.verifyErr
	}
	if s.verifyResult != nil {
		return *s.verifyResult, nil
	}
	return signing.VerifyResult{
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
	if tok.OrderID == 0 {
		t.Fatalf("token must carry the recorded order id")
	}
	if len(signer.signed) != 1 {
		t.Fatalf("Sign calls = %d, want 1", len(signer.signed))
	}
	p := signer.signed[0]
	if p.Mode != backend.SubmitModeHold || p.Verdict != "accept" {
		t.Fatalf("payload mode/verdict = %q/%q", p.Mode, p.Verdict)
	}
	if p.EstimatePrice != "100" || p.EstimateSource != domain.EstimateSourceLimit {
		t.Fatalf("payload estimate = %q/%q, want engine lock price", p.EstimatePrice, p.EstimateSource)
	}
	if p.OrderID != tok.OrderID {
		t.Fatalf("payload order id = %d, want %d", p.OrderID, tok.OrderID)
	}
	if p.Instrument != "AAPL/USD" || p.Quantity != "10" ||
		p.AmountKind != string(domain.OrderAmountKindQuantity) || p.LimitPrice != "100" {
		t.Fatalf("payload bound params = %+v", p)
	}
	if p.Nonce == "" || p.IssuedAt == "" || p.ExpiresAt == "" {
		t.Fatalf("payload lifecycle fields missing: %+v", p)
	}
	if fn.orders[tok.OrderID].Status != domain.OrderStatusAccepted {
		t.Fatalf("hold must leave the order accepted (funds held), got %q", fn.orders[tok.OrderID].Status)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionApprovalIssued) {
		t.Fatalf("hold must audit approval_issued: %+v", fn.auditCalls)
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
	if fn.orders[tok.OrderID].Status != domain.OrderStatusFilled {
		t.Fatalf("immediate must leave the order filled, got %q", fn.orders[tok.OrderID].Status)
	}
	if len(signer.signed) != 1 || signer.signed[0].Mode != backend.SubmitModeImmediate {
		t.Fatalf("immediate payload mode wrong: %+v", signer.signed)
	}
	if !hasAudit(fn.auditCalls, domain.AuditActionApprovalIssued) {
		t.Fatalf("immediate must audit approval_issued")
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
	if fn.orders[tok.OrderID].Status != domain.OrderStatusFilled {
		t.Fatalf("empty mode must default to immediate (filled), got %q", fn.orders[tok.OrderID].Status)
	}
}

func TestService_SubmitOrderTokenRejectIssuesNoToken(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	fn.holdResult = &engine.HoldResult{
		Accepted: false,
		Rejects:  []domain.OrderReject{{Code: "insufficient_funds", Reason: "no funds"}},
	}

	_, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeHold)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("reject must surface as ErrInvalid, got %v", err)
	}
	if len(signer.signed) != 0 {
		t.Fatalf("reject must issue no token")
	}
	if hasAudit(fn.auditCalls, domain.AuditActionApprovalIssued) {
		t.Fatalf("reject must not audit approval_issued")
	}
}

func TestService_SubmitOrderTokenESignOff(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{noESign: true}
	svc, _ := newTestServiceWithSigner(signer)

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
	env, err := signing.DecodeEnvelope(tok.Token)
	if err != nil {
		t.Fatalf("decode eSign-off envelope: %v", err)
	}
	if env.Alg != signing.AlgNone || env.Signature != "" {
		t.Fatalf("eSign-off envelope = %+v, want alg none and no signature", env)
	}
	if env.Approval.Instrument != "AAPL/USD" || env.Approval.Verdict != "accept" {
		t.Fatalf("eSign-off payload binding missing: %+v", env.Approval)
	}
}

func TestService_ConfirmExecutionCommits(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)

	order, err := svc.ConfirmExecution(context.Background(), tok.OrderID, tok.Token, false)
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
}

func TestService_ConfirmExecutionIdempotent(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)

	if _, err := svc.ConfirmExecution(context.Background(), tok.OrderID, tok.Token, false); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	// The engine guards the double-commit panic by returning a conflict; the
	// backend treats a re-confirm of an already-committed order as success.
	fn.confirmErr = domain.ErrConflict
	order, err := svc.ConfirmExecution(context.Background(), tok.OrderID, tok.Token, false)
	if err != nil {
		t.Fatalf("second confirm must be idempotent success, got %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("idempotent confirm must report committed, got %q", order.Status)
	}
}

func TestService_ConfirmAfterCancelConflicts(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)

	if _, err := svc.CancelOrder(
		context.Background(), tok.OrderID, tok.Token, "operator", false,
	); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// The cancelled order is terminal, so the default safety gate rejects before
	// the engine commit path.
	fn.confirmErr = domain.ErrConflict
	_, err := svc.ConfirmExecution(context.Background(), tok.OrderID, tok.Token, false)
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

	if _, err := svc.CancelOrder(
		context.Background(), tok.OrderID, tok.Token, "operator", false,
	); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	order, err := svc.ConfirmExecution(context.Background(), tok.OrderID, tok.Token, true)
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

	if _, err := svc.ConfirmExecution(context.Background(), tok.OrderID, tok.Token, false); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	_, err := svc.CancelOrder(context.Background(), tok.OrderID, tok.Token, "too late", false)
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

	order := fn.orders[tok.OrderID]
	order.Status = domain.OrderStatusFilled
	fn.orders[tok.OrderID] = order

	_, err := svc.CancelOrder(context.Background(), tok.OrderID, tok.Token, "late", false)
	if !errors.Is(err, domain.ErrTerminalOrder) {
		t.Fatalf("cancel filled without force = %v, want terminal order", err)
	}
	if len(fn.cancelCalls) != 0 {
		t.Fatalf("cancel filled without force reached engine: %+v", fn.cancelCalls)
	}

	order, err = svc.CancelOrder(context.Background(), tok.OrderID, tok.Token, "late", true)
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

	order, err := svc.CancelOrder(context.Background(), tok.OrderID, tok.Token, "stale price", false)
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
}

func TestService_ConfirmRejectsBadToken(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{verifyErr: domain.ErrInvalid}
	svc, fn := newTestServiceWithSigner(signer)
	tok := mustHold(t, svc)
	fn.confirmCalls = nil

	_, err := svc.ConfirmExecution(context.Background(), tok.OrderID, tok.Token, false)
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
		verifyResult: &signing.VerifyResult{
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

	_, err = svc.ConfirmExecution(context.Background(), tok.OrderID, tok.Token, false)
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

// mustHold issues a hold token and fails the test on error.
func mustHold(t *testing.T, svc *backend.Service) backend.ApprovalToken {
	t.Helper()
	tok, err := svc.SubmitOrderToken(context.Background(), sampleOrder(), backend.SubmitModeHold)
	if err != nil {
		t.Fatalf("hold setup: %v", err)
	}
	if time.Until(tok.ExpiresAt) <= 0 {
		t.Fatalf("hold token must not be pre-expired: %v", tok.ExpiresAt)
	}
	return tok
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
