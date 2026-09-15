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
	"errors"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
)

func TestAttestationBlocksPreserveSDKPolicy(t *testing.T) {
	t.Parallel()
	const policy = domain.PolicySpotFundsPnlBoundsKillSwitch
	got := attestationBlocks([]domain.AccountBlock{{
		Account: "acc-1", Policy: policy, Code: "pnl_bound_breached",
	}})
	if len(got) != 1 || got[0].Policy != policy {
		t.Fatalf("attestation blocks = %+v, want SDK policy %s", got, policy)
	}
}

// reachRecorder is a node that holds no order: it records that a cancel got as
// far as reading the order, and fails with an error that is not ErrInvalid.
type reachRecorder struct {
	node.Node
	reached bool
}

func (r *reachRecorder) GetOrder(context.Context, domain.ExternalID) (domain.OrderDetail, error) {
	r.reached = true
	return domain.OrderDetail{}, errors.New("no order")
}

// TestCancelOrderValidatesReason covers the free-form cancel reason: it is
// rendered verbatim into the audit detail line, so a newline could forge a
// second record and must be refused before the cancel reaches a node.
func TestCancelOrderValidatesReason(t *testing.T) {
	t.Parallel()

	const orderID = "ord-1"
	recorder := &reachRecorder{}
	svc := &Service{node: recorder}
	ctx := systemCtx()

	_, _, err := svc.CancelOrder(ctx, orderID, "token", "",
		"typo\ncancel approval 00000000 order ord-2 reason=routine")
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CancelOrder with a newline reason = %v, want ErrInvalid", err)
	}
	if recorder.reached {
		t.Fatal("cancel reached the node with an unvalidated reason")
	}

	_, _, err = svc.CancelOrder(ctx, orderID, "token", "", "typo")
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CancelOrder with a plain reason = %v, want it accepted", err)
	}
	if !recorder.reached {
		t.Fatal("cancel with a plain reason did not reach the node")
	}
}

// shortcutRecorder is a node holding one stored order that records every
// write a shortcut could make: the confirm or cancel itself and the audit row
// behind it.
type shortcutRecorder struct {
	node.Node
	stored domain.OrderDetail
	writes []string
}

func (r *shortcutRecorder) GetOrder(context.Context, domain.ExternalID) (domain.OrderDetail, error) {
	return r.stored, nil
}

func (r *shortcutRecorder) ConfirmOrderWithAttestation(
	context.Context, domain.ExternalID, domain.Caller, store.EventAttestor,
) (domain.Order, error) {
	r.writes = append(r.writes, "confirm")
	return r.stored.Order, nil
}

func (r *shortcutRecorder) CancelOrderWithAttestation(
	context.Context, domain.ExternalID, string, domain.Caller, store.EventAttestor,
) (domain.Order, engine.ExecutionReportResult, error) {
	r.writes = append(r.writes, "cancel")
	return r.stored.Order, engine.ExecutionReportResult{}, nil
}

func (r *shortcutRecorder) AppendAudit(context.Context, store.AuditEntry, domain.Caller) error {
	r.writes = append(r.writes, "audit")
	return nil
}

// envelopeSigner verifies a token by decoding its envelope alone. The signature
// is the signing package's subject; the verdict gate behind Verify is this
// file's.
type envelopeSigner struct{ fwsigning.Service }

func (envelopeSigner) Verify(
	_ context.Context, token string, _ fwsigning.VerifyParams,
) (fwsigning.VerifyResult, error) {
	env, err := fwsigning.DecodeEnvelope(token)
	if err != nil {
		return fwsigning.VerifyResult{}, err
	}
	return fwsigning.VerifyResult{Payload: env.Approval}, nil
}

func (envelopeSigner) NoESign(context.Context) (bool, error) { return true, nil }

// TestImmediateTokenIsNoShortcut covers the token an immediate submit issues:
// it is the receipt of an order already submitted and settled in one chain, so
// confirm and cancel refuse it with ErrConflict on its very first presentation
// and write nothing - no order event, no audit row. This gate is the only thing
// between such a token and the shortcut paths.
func TestImmediateTokenIsNoShortcut(t *testing.T) {
	t.Parallel()

	order := domain.Order{
		ExternalID:  "AAAAAAAAAAAAAAAAAAAAAA",
		AmountValue: "10",
		Price:       "150.25",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Account:     "acct-1",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		Status:      domain.OrderStatusFilled,
	}
	const eventID domain.ExternalID = "BBBBBBBBBBBBBBBBBBBBBB"
	// The pre-trade accept verdict of an immediate submit, stamped the way
	// eventAttestor stamps it before signing.
	payload := buildApprovalPayload(
		order, SubmitModeImmediate, "approval-1", "150.25", time.Now().UTC(), "nonce-1")
	payload.RequestType = string(domain.AttestationRequestSubmit)
	payload.EventExternalID = eventID.String()
	payload.Alg = fwsigning.AlgNone
	token, err := fwsigning.BuildEnvelope(fwsigning.Envelope{
		Approval: payload, Alg: fwsigning.AlgNone,
	})
	if err != nil {
		t.Fatalf("BuildEnvelope: %v", err)
	}
	stored := domain.OrderDetail{
		Order: order,
		Events: []domain.OrderEvent{{
			ExternalID: eventID,
			Order:      order.ExternalID,
			Type:       domain.OrderEventPreTradeAccepted,
			Attestation: &domain.EventAttestation{
				Token:       token,
				Alg:         fwsigning.AlgNone,
				RequestType: domain.AttestationRequestSubmit,
				Mode:        SubmitModeImmediate,
			},
		}},
	}
	ctx := systemCtx()

	for _, tc := range []struct {
		name string
		call func(*Service) error
	}{
		{"confirm", func(svc *Service) error {
			_, _, err := svc.ConfirmExecution(ctx, order.ExternalID.String(), token)
			return err
		}},
		{"cancel", func(svc *Service) error {
			_, _, err := svc.CancelOrder(ctx, order.ExternalID.String(), token, "", "routine")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder := &shortcutRecorder{stored: stored}
			svc := &Service{node: recorder, signer: envelopeSigner{}}

			err := tc.call(svc)
			if !errors.Is(err, domain.ErrConflict) ||
				!strings.Contains(err.Error(), `mode "immediate"`) {
				t.Errorf("%s with an immediate token = %v, want ErrConflict on its mode", tc.name, err)
			}
			if len(recorder.writes) != 0 {
				t.Errorf("%s with an immediate token wrote %v, want nothing", tc.name, recorder.writes)
			}
		})
	}
}
