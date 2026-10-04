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
// Please see https://officer.openpit.dev and the OWNERS file for details.

package backend

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
)

type paperSendTestNode struct {
	*tradingTestNode
	accepted            bool
	submitErr, venueErr error
	orderErr            error
	order               *domain.Order
	venue               *domain.VenueOrder
	missing             domain.MissingAccountPolicy
	events              []string
	holds, reads        int
	orderEvents         []domain.OrderEvent
	cancelErr           error
	cancels             int
	audits              []store.AuditEntry
}

func (n *paperSendTestNode) SubmitOrderWithAttestation(
	ctx context.Context, o domain.Order, missing domain.MissingAccountPolicy,
	_ domain.Caller,
	attestFor func(domain.Order, engine.OrderResult) store.EventAttestor,
) (domain.Order, engine.OrderResult, error) {
	n.holds++
	n.events = append(n.events, "hold")
	n.missing = missing
	if n.submitErr != nil {
		return domain.Order{}, engine.OrderResult{}, n.submitErr
	}
	if o.ExternalID.IsZero() {
		o.ExternalID = "generated-paper-order"
	}
	result := engine.OrderResult{Accepted: n.accepted, SettlementLockPrice: "100"}
	eventType := domain.OrderEventPreTradeAccepted
	o.Status = domain.OrderStatusCommitted
	if !n.accepted {
		eventType = domain.OrderEventPreTradeRejected
		o.Status = domain.OrderStatusRejected
		result.Rejects = []domain.OrderReject{{Code: "order_size_limit", Reason: "too large"}}
	}
	event := domain.OrderEvent{
		ExternalID: "paper-approval-event", Order: o.ExternalID, Type: eventType,
	}
	att, _, err := attestFor(o, result)(ctx, event)
	if err != nil {
		return domain.Order{}, engine.OrderResult{}, err
	}
	n.order = &o
	event.Attestation = &att
	n.orderEvents = append(n.orderEvents, event)
	return o, result, nil
}

func (n *paperSendTestNode) GetOrder(_ context.Context, id domain.ExternalID) (domain.OrderDetail, error) {
	if n.orderErr != nil {
		return domain.OrderDetail{}, n.orderErr
	}
	if n.order == nil || n.order.ExternalID != id {
		return domain.OrderDetail{}, domain.ErrNotFound
	}
	return domain.OrderDetail{Order: *n.order, Events: n.orderEvents}, nil
}

func (n *paperSendTestNode) CancelOrderWithAttestation(
	ctx context.Context, id domain.ExternalID, leaves string, caller domain.Caller,
	attest store.EventAttestor,
) (domain.Order, engine.ExecutionReportResult, error) {
	n.cancels++
	if ctx.Err() != nil || !reflect.DeepEqual(caller, auth.SystemCaller()) {
		return domain.Order{}, engine.ExecutionReportResult{}, errors.New("missing cancellation context")
	}
	if n.cancelErr != nil {
		return domain.Order{}, engine.ExecutionReportResult{}, n.cancelErr
	}
	_, _, err := attest(ctx, domain.OrderEvent{
		ExternalID: "cancel-event", Order: id, Type: domain.OrderEventCancelled,
		Payload: domain.OrderEventPayload{
			OrderStatus: string(domain.OrderStatusCancelled), LeavesQuantity: leaves,
			ExecutionReport: &domain.ExecutionReportRequest{Order: id, LeavesQuantity: leaves},
		},
	})
	if err != nil {
		return domain.Order{}, engine.ExecutionReportResult{}, err
	}
	n.order.Status, n.order.Leaves = domain.OrderStatusCancelled, leaves
	return *n.order, engine.ExecutionReportResult{}, nil
}

func (n *paperSendTestNode) GetVenueOrder(ctx context.Context, id domain.ExternalID) (domain.VenueOrder, bool, error) {
	n.reads++
	n.events = append(n.events, "read")
	if err := ctx.Err(); err != nil {
		return domain.VenueOrder{}, false, err
	}
	if n.venueErr != nil {
		return domain.VenueOrder{}, false, n.venueErr
	}
	if n.venue == nil || n.venue.Order != id {
		return domain.VenueOrder{}, false, nil
	}
	return *n.venue, true, nil
}

func (n *paperSendTestNode) AppendAudit(_ context.Context, entry store.AuditEntry, _ domain.Caller) error {
	n.audits = append(n.audits, entry)
	return nil
}

type paperSendTestSigner struct {
	fwsigning.Service
	payload     domain.ApprovalPayload
	holdPayload domain.ApprovalPayload
}

func (*paperSendTestSigner) NoESign(context.Context) (bool, error) { return false, nil }

func (*paperSendTestSigner) ListKeys(context.Context) ([]domain.SigningKey, error) {
	return []domain.SigningKey{{KeyID: "paper-key", Active: true}}, nil
}

func (s *paperSendTestSigner) Sign(payload domain.ApprovalPayload) (string, error) {
	s.payload = payload
	if payload.RequestType == string(domain.AttestationRequestSubmit) {
		s.holdPayload = payload
	}
	return "paper-approval-token", nil
}

func (s *paperSendTestSigner) Verify(context.Context, string, fwsigning.VerifyParams) (fwsigning.VerifyResult, error) {
	return fwsigning.VerifyResult{Payload: s.holdPayload, Signed: true}, nil
}

type paperSendTestRuntime struct {
	*tradingTestRuntime
	n                *paperSendTestNode
	preCheckErr      error
	preChecks        []domain.TradingDestination
	drafts           []domain.Order
	sends            []domain.TradingDestination
	orderIDs         []domain.ExternalID
	sendErr          error
	returned         domain.VenueOrder
	cancel           context.CancelFunc
	orderWhenSending *domain.Order
	cancelled        bool
}

func (r *paperSendTestRuntime) PreCheck(_ context.Context, o domain.Order, dest domain.TradingDestination) error {
	r.n.events = append(r.n.events, "precheck")
	r.preChecks = append(r.preChecks, dest)
	r.drafts = append(r.drafts, o)
	return r.preCheckErr
}

func (r *paperSendTestRuntime) Send(_ context.Context, id domain.ExternalID, dest domain.TradingDestination) (domain.VenueOrder, error) {
	r.n.events = append(r.n.events, "send")
	r.sends = append(r.sends, dest)
	r.orderIDs = append(r.orderIDs, id)
	r.orderWhenSending = r.n.order
	if r.cancel != nil {
		r.cancel()
	}
	if r.cancelled {
		r.n.order.Status, r.n.order.Leaves = domain.OrderStatusCancelled, "0"
	}
	return r.returned, r.sendErr
}

func newPaperSendTestService(t *testing.T) (*Service, *paperSendTestNode, *paperSendTestRuntime, *paperSendTestSigner) {
	t.Helper()
	s, base, runtime := newTradingTestService(t)
	n := &paperSendTestNode{tradingTestNode: base, accepted: true}
	n.connections = []domain.TradingConnection{tradingTestConnection()}
	n.access = []domain.TradingAccess{{Account: "acct-1", Connection: "connection-1", VenueAccount: "remote-1"}}
	rt := &paperSendTestRuntime{tradingTestRuntime: runtime, n: n}
	signer := &paperSendTestSigner{}
	s.node, s.trading, s.signer = n, rt, signer
	return s, n, rt, signer
}

func paperSendTestOrder() domain.Order {
	return domain.Order{Account: "acct-1", ExternalID: "draft-order", BaseAsset: "AAPL", QuoteAsset: "USD", AmountValue: "1", Price: "100"}
}

func assertPaperSendNotHeld(t *testing.T, s *Service, n *paperSendTestNode, rt *paperSendTestRuntime) {
	t.Helper()
	if n.holds != 0 || n.order != nil || len(rt.sends) != 0 || n.reads != 0 {
		t.Fatalf("failure took a hold or sent: holds=%d order=%v sends=%v reads=%d", n.holds, n.order, rt.sends, n.reads)
	}
	if _, err := s.GetOrder(systemCtx(), "draft-order"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("order exists after failure: %v", err)
	}
}

func TestSendPaperOrderNamedDestination(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*paperSendTestNode)
		want   error
	}{
		{"live", func(n *paperSendTestNode) { n.connections[0].Mode = domain.TradingModeLive }, domain.ErrTradingModeMismatch},
		{"unbound", func(n *paperSendTestNode) { n.access[0].Account = "other-account" }, domain.ErrForbidden},
		{"unknown", func(n *paperSendTestNode) { n.connections = nil }, domain.ErrNotFound},
		{"unbound_live", func(n *paperSendTestNode) { n.access = nil; n.connections[0].Mode = domain.TradingModeLive }, domain.ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, n, rt, _ := newPaperSendTestService(t)
			tc.change(n)
			_, err := s.SendPaperOrder(systemCtx(), paperSendTestOrder(), SendDestination{Connection: "connection-1", Route: "{}"})
			if !errors.Is(err, tc.want) || len(rt.preChecks) != 0 {
				t.Fatalf("destination error = %v, prechecks = %v", err, rt.preChecks)
			}
			assertPaperSendNotHeld(t, s, n, rt)
		})
	}
}

func TestSendPaperOrderAutomaticDestination(t *testing.T) {
	for _, name := range []string{"single", "ambiguous", "disabled_and_live", "none"} {
		t.Run(name, func(t *testing.T) {
			s, n, rt, _ := newPaperSendTestService(t)
			live, disabled := tradingTestConnection(), tradingTestConnection()
			live.ExternalID, live.Label, live.Mode = "live", "Live", domain.TradingModeLive
			disabled.ExternalID, disabled.Label, disabled.Enabled = "disabled", "Disabled", false
			n.connections = append(n.connections, live, disabled)
			n.access = append(n.access, domain.TradingAccess{Account: "acct-1", Connection: "live"}, domain.TradingAccess{Account: "acct-1", Connection: "disabled"})
			if name == "ambiguous" {
				second := tradingTestConnection()
				second.ExternalID, second.Label = "second", "Another paper"
				n.connections = append(n.connections, second)
				n.access = append(n.access, domain.TradingAccess{Account: "acct-1", Connection: "second", VenueAccount: "remote-2"})
				rt.states = map[domain.ExternalID]trading.ConnectionState{"second": {Unavailable: "offline"}}
			}
			switch name {
			case "disabled_and_live":
				n.access = n.access[1:]
			case "none":
				n.access = nil
			}
			got, err := s.SendPaperOrder(systemCtx(), paperSendTestOrder(), SendDestination{Route: "{}"})
			switch name {
			case "single":
				want := domain.TradingDestination{Connection: "connection-1", VenueAccount: "remote-1", Route: "{}"}
				if err != nil || got.Send == nil || !reflect.DeepEqual(rt.preChecks, []domain.TradingDestination{want}) || !reflect.DeepEqual(rt.sends, []domain.TradingDestination{want}) {
					t.Fatalf("single destination = %+v, err=%v, prechecks=%v sends=%v", got, err, rt.preChecks, rt.sends)
				}
			case "ambiguous":
				var ambiguous *DestinationAmbiguousError
				if !errors.Is(err, domain.ErrDestinationAmbiguous) || !errors.As(err, &ambiguous) {
					t.Fatalf("ambiguity error = %v", err)
				}
				want, listErr := s.ListTradingDestinations(systemCtx(), "acct-1", domain.TradingModePaper)
				if listErr != nil {
					t.Fatal(listErr)
				}
				var enabled []TradingDestinationStatus
				for _, d := range want {
					if d.Enabled {
						enabled = append(enabled, d)
					}
				}
				if len(ambiguous.Candidates) != 2 || !reflect.DeepEqual(ambiguous.Candidates, enabled) || !strings.Contains(err.Error(), "name one") {
					t.Fatalf("candidates = %+v, want %+v", ambiguous.Candidates, enabled)
				}
				assertPaperSendNotHeld(t, s, n, rt)
			default:
				if !errors.Is(err, domain.ErrNoDestination) {
					t.Fatalf("no destination error = %v", err)
				}
				assertPaperSendNotHeld(t, s, n, rt)
			}
			if name != "single" && len(rt.preChecks) != 0 {
				t.Fatal("failed routing reached PreCheck")
			}
		})
	}
}

func TestSendPaperOrderVenueAccount(t *testing.T) {
	for _, venueAccount := range []string{"", "remote-1", "other"} {
		t.Run("venue_"+venueAccount, func(t *testing.T) {
			s, n, rt, _ := newPaperSendTestService(t)
			_, err := s.SendPaperOrder(systemCtx(), paperSendTestOrder(), SendDestination{Connection: "connection-1", VenueAccount: venueAccount, Route: `{"timeInForce":"day"}`})
			if venueAccount == "other" {
				if !errors.Is(err, domain.ErrForbidden) || len(rt.preChecks) != 0 {
					t.Fatalf("venue override error = %v", err)
				}
				assertPaperSendNotHeld(t, s, n, rt)
				return
			}
			want := domain.TradingDestination{Connection: "connection-1", VenueAccount: "remote-1", Route: `{"timeInForce":"day"}`}
			if err != nil || !reflect.DeepEqual(rt.preChecks, []domain.TradingDestination{want}) || !reflect.DeepEqual(rt.sends, []domain.TradingDestination{want}) {
				t.Fatalf("bound venue account = %v, err=%v", rt.preChecks, err)
			}
		})
	}
}

func TestSendPaperOrderValidation(t *testing.T) {
	for _, tc := range []struct {
		name, account, route string
		unwired              bool
		want                 error
	}{
		{"account", "", "{}", false, domain.ErrInvalid},
		{"array", "acct-1", "[]", false, domain.ErrInvalid},
		{"null", "acct-1", "null", false, domain.ErrInvalid},
		{"string", "acct-1", `"route"`, false, domain.ErrInvalid},
		{"number", "acct-1", "1", false, domain.ErrInvalid},
		{"missing_route", "acct-1", "", false, domain.ErrInvalid},
		{"malformed", "acct-1", "{", false, domain.ErrInvalid},
		{"whitespace", "acct-1", " {} ", false, domain.ErrInvalid},
		{"unwired", "acct-1", "{}", true, domain.ErrNotImplemented},
		{"invalid_before_unwired", "acct-1", "[]", true, domain.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, n, rt, _ := newPaperSendTestService(t)
			if tc.unwired {
				s.trading = nil
			}
			o := paperSendTestOrder()
			o.Account = domain.AccountID(tc.account)
			_, err := s.SendPaperOrder(systemCtx(), o, SendDestination{Route: tc.route})
			if !errors.Is(err, tc.want) || len(rt.preChecks) != 0 {
				t.Fatalf("validation = %v, prechecks = %v", err, rt.preChecks)
			}
			assertPaperSendNotHeld(t, s, n, rt)
		})
	}
}

func TestSendPaperOrderPreCheck(t *testing.T) {
	domainError := fmt.Errorf("worker unavailable: %w", domain.ErrConflict)
	joinedError := fmt.Errorf("worker: %w", errors.Join(errors.New("offline"), domain.ErrUpstream))
	for _, tc := range []struct {
		name        string
		cause, want error
		passthrough bool
	}{
		{"venue_refusal", fmt.Errorf("invalid price increment: %w", trading.ErrRejected), domain.ErrInvalid, false},
		{"domain", domainError, domain.ErrConflict, true},
		{"joined_domain", joinedError, domain.ErrUpstream, true},
		{"upstream", errors.New("connection lost"), domain.ErrUpstream, false},
		{"unsupported", fmt.Errorf("invalid amount: %w", trading.ErrUnsupported), domain.ErrInvalid, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, n, rt, _ := newPaperSendTestService(t)
			rt.preCheckErr = tc.cause
			_, err := s.SendPaperOrder(systemCtx(), paperSendTestOrder(), SendDestination{Route: "{}"})
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.cause.Error()) || (tc.passthrough && err != tc.cause) {
				t.Fatalf("precheck error = %v", err)
			}
			if !reflect.DeepEqual(rt.drafts, []domain.Order{paperSendTestOrder()}) {
				t.Fatalf("precheck draft = %v", rt.drafts)
			}
			assertPaperSendNotHeld(t, s, n, rt)
		})
	}
}

func TestSendPaperOrderPreTrade(t *testing.T) {
	for _, name := range []string{"accepted", "rejected", "submit_error"} {
		t.Run(name, func(t *testing.T) {
			s, n, rt, signer := newPaperSendTestService(t)
			n.accepted = name != "rejected"
			if name == "submit_error" {
				n.submitErr = fmt.Errorf("hold failed: %w", domain.ErrEngineRestarting)
			}
			o := paperSendTestOrder()
			o.ExternalID = ""
			got, err := s.SendPaperOrder(systemCtx(), o, SendDestination{Route: "{}"})
			if n.holds != 1 || n.missing != domain.MissingAccountReject {
				t.Fatalf("hold = %d missing=%s", n.holds, n.missing)
			}
			if name == "submit_error" {
				if err != n.submitErr || got.Send != nil || len(rt.sends) != 0 || n.order != nil {
					t.Fatalf("submit error result=%+v err=%v", got, err)
				}
				return
			}
			verdict := "accept"
			var reasons []domain.OrderReject
			if name == "rejected" {
				verdict = "reject"
				reasons = []domain.OrderReject{{Code: "order_size_limit", Reason: "too large"}}
			}
			want := ApprovalToken{Token: "paper-approval-token", KeyID: "paper-key", OrderExternalID: "generated-paper-order", Verdict: verdict, Reasons: reasons, Signed: true}
			if err != nil || !reflect.DeepEqual(got.Token, want) || signer.payload.Mode != SubmitModeHold {
				t.Fatalf("approval = %+v, err=%v mode=%s", got.Token, err, signer.payload.Mode)
			}
			if name == "rejected" {
				if got.Send != nil || len(rt.sends) != 0 || n.reads != 0 {
					t.Fatal("pre-trade reject sent or read a venue order")
				}
				return
			}
			if got.Send == nil || rt.orderWhenSending == nil || rt.orderWhenSending.Status != domain.OrderStatusCommitted || !reflect.DeepEqual(rt.orderIDs, []domain.ExternalID{"generated-paper-order"}) || !reflect.DeepEqual(n.events, []string{"precheck", "hold", "send", "read"}) {
				t.Fatalf("send did not follow the committed hold: result=%+v order=%v ids=%v events=%v", got, rt.orderWhenSending, rt.orderIDs, n.events)
			}
		})
	}
}

func TestSendPaperOrderSend(t *testing.T) {
	for _, tc := range []struct {
		name             string
		cause            error
		want             string
		zeroLink, cancel bool
		cancelled        bool
	}{
		{"acknowledged", nil, "acknowledged", false, false, false},
		{"acknowledged_zero_link", nil, "acknowledged", true, false, false},
		{"rejected", fmt.Errorf("venue refused: %w", trading.ErrRejected), "refused", false, false, true},
		{"unsupported", fmt.Errorf("venue unsupported: %w", trading.ErrUnsupported), "refused", true, false, true},
		{"rejected_cancellation_pending", fmt.Errorf("venue refused: %w", trading.ErrRejected), "unknown", false, false, false},
		{"unsupported_cancellation_pending", fmt.Errorf("venue unsupported: %w", trading.ErrUnsupported), "unknown", true, false, false},
		{"stopped_after_handoff", fmt.Errorf("runtime stopped: %w", domain.ErrConflict), "unknown", true, false, false},
		{"upstream", fmt.Errorf("timeout: %w", domain.ErrUpstream), "unknown", true, false, false},
		{"caller_cancelled", context.Canceled, "unknown", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, n, rt, _ := newPaperSendTestService(t)
			n.venue = &domain.VenueOrder{Order: "draft-order", Connection: "connection-1", VenueAccount: "remote-1", Route: "{}", ClientOrderID: "stored-client", SendAttempted: true, VenueOrderID: "stored-venue"}
			rt.sendErr = tc.cause
			rt.cancelled = tc.cancelled
			if !tc.zeroLink {
				rt.returned = domain.VenueOrder{Order: "draft-order", VenueOrderID: "stale-runtime-link"}
			}
			ctx, cancel := context.WithCancel(systemCtx())
			defer cancel()
			if tc.cancel {
				rt.cancel = cancel
			}
			got, err := s.SendPaperOrder(ctx, paperSendTestOrder(), SendDestination{Route: "{}"})
			if err != nil || got.Token.OrderExternalID != "draft-order" || got.Token.Token != "paper-approval-token" || got.Send == nil || got.Send.Status != tc.want || !reflect.DeepEqual(got.Send.Order, n.venue) || n.reads != 1 || n.cancels != 0 {
				t.Fatalf("send result=%+v send=%+v err=%v reads=%d", got, got.Send, err, n.reads)
			}
			message := ""
			if tc.cause != nil {
				message = tc.cause.Error()
			}
			if strings.HasSuffix(tc.name, "_cancellation_pending") {
				message += "\nvenue refused; cancellation is not recorded yet; runtime retries it"
				if n.order.Status == domain.OrderStatusCancelled {
					t.Fatal("pending cancellation was recorded")
				}
			} else if tc.cancelled && n.order.Status != domain.OrderStatusCancelled {
				t.Fatal("refused requires a recorded cancellation")
			}
			if got.Send.Error != message {
				t.Fatalf("send error = %q, want %q", got.Send.Error, message)
			}
		})
	}
}

func TestSendPaperOrderNotSent(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		cause                        error
		cancelFails, callerCancelled bool
	}{
		{"not_sent", domain.ErrConflict, false, false},
		{"recheck_refusal", trading.ErrRejected, false, false},
		{"cancel_failure", domain.ErrUpstream, true, false},
		{"cancel_failure_refusal", trading.ErrRejected, true, false},
		{"caller_cancelled", context.Canceled, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, n, rt, signer := newPaperSendTestService(t)
			rt.sendErr = fmt.Errorf("%w: send refused: %w", trading.ErrNotSent, tc.cause)
			if tc.cancelFails {
				n.cancelErr = errors.New("cancel failed")
			}
			ctx, cancel := context.WithCancel(systemCtx())
			defer cancel()
			if tc.callerCancelled {
				rt.cancel = cancel
			}
			got, err := s.SendPaperOrder(ctx, paperSendTestOrder(), SendDestination{Route: "{}"})
			if err != nil || got.Token.OrderExternalID != "draft-order" || got.Token.Token != "paper-approval-token" || got.Send == nil || n.cancels != 1 {
				t.Fatalf("not sent result=%+v send=%+v err=%v cancels=%d", got, got.Send, err, n.cancels)
			}
			if tc.cancelFails {
				if got.Send.Status != "unknown" || got.Send.Error != errors.Join(rt.sendErr, n.cancelErr).Error() || n.order.Status == domain.OrderStatusCancelled {
					t.Fatalf("failed cancel: send=%+v order=%+v", got.Send, n.order)
				}
			} else if got.Send.Status != "refused" || got.Send.Error != rt.sendErr.Error() || n.order.Status != domain.OrderStatusCancelled || n.order.Leaves != "0" || signer.payload.RejectReason != rt.sendErr.Error() {
				t.Fatalf("cancelled not sent: send=%+v order=%+v reason=%q", got.Send, n.order, signer.payload.RejectReason)
			}
		})
	}
}

func TestSendPaperOrderNotSentReason(t *testing.T) {
	const reasonLimit = 4096
	prefix := trading.ErrNotSent.Error() + ": "
	for _, tc := range []struct {
		name       string
		cause      error
		wantError  string
		wantReason string
	}{
		{
			name: "newline",
			cause: errors.Join(
				errors.New("venue test-key-value"),
				errors.New("refused\t\r\x00\u200b test-secret-value"),
			),
			wantError:  prefix + "venue [redacted]\nrefused\t\r\x00\u200b [redacted]",
			wantReason: prefix + "venue [redacted]refused [redacted]",
		},
		{
			name:       "credential_split_by_newline",
			cause:      errors.New("venue test-secret-\nvalue"),
			wantError:  prefix + "venue test-secret-\nvalue",
			wantReason: prefix + "venue [redacted]",
		},
		{
			name:       "oversized",
			cause:      errors.New(strings.Repeat("界", reasonLimit+1)),
			wantError:  prefix + strings.Repeat("界", reasonLimit+1),
			wantReason: prefix + strings.Repeat("界", reasonLimit-utf8.RuneCountInString(prefix)),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, n, rt, signer := newPaperSendTestService(t)
			rt.sendErr = fmt.Errorf("%w: %w", trading.ErrNotSent, tc.cause)
			got, err := s.SendPaperOrder(systemCtx(), paperSendTestOrder(), SendDestination{Route: "{}"})
			if err != nil || got.Send == nil {
				t.Fatalf("not sent: err=%v hasSend=%t", err, got.Send != nil)
			}
			if got.Send.Status != "refused" || n.cancels != 1 {
				t.Fatalf("not sent: status=%s cancels=%d", got.Send.Status, n.cancels)
			}
			if n.order.Status != domain.OrderStatusCancelled || n.order.Leaves != "0" {
				t.Fatalf("unsent order retained reservation: %+v", n.order)
			}
			if got.Send.Error != tc.wantError {
				t.Fatalf("full redacted error changed: got %d runes want %d", utf8.RuneCountInString(got.Send.Error), utf8.RuneCountInString(tc.wantError))
			}
			reason := signer.payload.RejectReason
			if strings.Contains(reason, "test-key-value") || strings.Contains(reason, "test-secret-value") {
				t.Fatal("stored cancellation reason contains a credential")
			}
			if err := domain.ValidateReason(reason); err != nil {
				t.Fatalf("invalid stored cancellation reason: %v", err)
			}
			if reason != tc.wantReason {
				t.Fatalf("stored reason differs: got %d runes want %d", utf8.RuneCountInString(reason), utf8.RuneCountInString(tc.wantReason))
			}
		})
	}
}

func TestSendPaperOrderNotSentAfterClaimCleanup(t *testing.T) {
	for _, stage := range []string{"claim_report", "post_claim_eligibility"} {
		t.Run(stage, func(t *testing.T) {
			s, n, rt, signer := newPaperSendTestService(t)
			rt.returned = domain.VenueOrder{
				Order: "draft-order", Connection: "connection-1", ClientOrderID: "deleted-claim",
			}
			rt.sendErr = fmt.Errorf("%w: %s failed: %w", trading.ErrNotSent, stage, domain.ErrConflict)
			got, err := s.SendPaperOrder(systemCtx(), paperSendTestOrder(), SendDestination{Route: "{}"})
			if err != nil || got.Send == nil || got.Send.Status != "refused" || n.cancels != 1 {
				t.Fatalf("cleaned claim: send=%+v err=%v cancels=%d", got.Send, err, n.cancels)
			}
			if n.order.Status != domain.OrderStatusCancelled || n.order.Leaves != "0" || n.venue != nil {
				t.Fatalf("cleaned claim retained held order: order=%+v venue=%+v", n.order, n.venue)
			}
			if got.Send.Error != rt.sendErr.Error() || signer.payload.RejectReason != got.Send.Error {
				t.Fatalf("cleaned claim reason=%q send=%+v", signer.payload.RejectReason, got.Send)
			}
		})
	}
}

func TestSendPaperOrderPreCheckRedacts(t *testing.T) {
	for _, named := range []bool{false, true} {
		for _, sentinel := range []error{trading.ErrRejected, trading.ErrUnsupported, domain.ErrConflict, domain.ErrUpstream, errors.New("offline")} {
			t.Run(fmt.Sprintf("named=%t/%v", named, sentinel), func(t *testing.T) {
				s, n, rt, _ := newPaperSendTestService(t)
				cause := &fs.PathError{Op: "precheck", Path: "test-key-value/test-secret-value", Err: sentinel}
				rt.preCheckErr = cause
				dest := SendDestination{Route: "{}"}
				if named {
					dest.Connection = "connection-1"
				}
				_, err := s.SendPaperOrder(systemCtx(), paperSendTestOrder(), dest)
				var typed *fs.PathError
				if !errors.Is(err, cause) || !errors.Is(err, sentinel) || !errors.As(err, &typed) || typed != cause || strings.Contains(err.Error(), "test-key-value") || strings.Contains(err.Error(), "test-secret-value") || !strings.Contains(err.Error(), "[redacted]") {
					t.Fatalf("precheck redaction/classification = %v", err)
				}
				assertPaperSendNotHeld(t, s, n, rt)
			})
		}
	}
}

func TestSendPaperOrderSendRedacts(t *testing.T) {
	for _, named := range []bool{false, true} {
		for _, stage := range []string{"send", "unavailable", "cancel_failure", "venue_read"} {
			t.Run(fmt.Sprintf("named=%t/%s", named, stage), func(t *testing.T) {
				s, n, rt, signer := newPaperSendTestService(t)
				rt.sendErr = errors.New("send test-key-value test-secret-value")
				want := "unknown"
				if stage == "unavailable" || stage == "cancel_failure" {
					rt.sendErr = fmt.Errorf("%w: unavailable test-key-value test-secret-value: %w", trading.ErrNotSent, domain.ErrUpstream)
					want = "refused"
				}
				if stage == "cancel_failure" {
					n.cancelErr = errors.New("cancel test-key-value test-secret-value")
					want = "unknown"
				}
				if stage == "venue_read" {
					n.venueErr = errors.New("read test-key-value test-secret-value")
				}
				dest := SendDestination{Route: "{}"}
				if named {
					dest.Connection = "connection-1"
				}
				got, err := s.SendPaperOrder(systemCtx(), paperSendTestOrder(), dest)
				if err != nil || got.Send == nil || got.Send.Status != want {
					t.Fatalf("redacted send=%+v err=%v", got.Send, err)
				}
				text := fmt.Sprintf("%+v %+v %+v", got.Send, signer.payload, n.audits)
				if strings.Contains(text, "test-key-value") || strings.Contains(text, "test-secret-value") || !strings.Contains(got.Send.Error, "[redacted]") {
					t.Fatalf("credential leaked in returned/stored text: %s", text)
				}
				if stage == "unavailable" && (n.cancels != 1 || signer.payload.RejectReason != got.Send.Error) {
					t.Fatalf("cancel reason=%q send=%+v", signer.payload.RejectReason, got.Send)
				}
			})
		}
	}
}

func TestSendPaperOrderRefusalReadErrors(t *testing.T) {
	for _, tc := range []struct {
		name               string
		orderErr, venueErr error
		wantError          string
	}{
		{
			name:      "cancellation_pending",
			wantError: "venue refused; cancellation is not recorded yet; runtime retries it",
		},
		{
			name:      "order_read_error",
			orderErr:  errors.New("order read unavailable"),
			wantError: "venue refused; cancellation could not be confirmed\norder read unavailable",
		},
		{
			name:      "cancellation_pending_venue_read_error",
			venueErr:  errors.New("venue read unavailable"),
			wantError: "venue refused; cancellation is not recorded yet; runtime retries it\ntrading: read venue order: venue read unavailable",
		},
		{
			name:      "order_and_venue_read_errors",
			orderErr:  errors.New("order read unavailable"),
			venueErr:  errors.New("venue read unavailable"),
			wantError: "venue refused; cancellation could not be confirmed\norder read unavailable\ntrading: read venue order: venue read unavailable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, n, rt, _ := newPaperSendTestService(t)
			n.orderErr, n.venueErr = tc.orderErr, tc.venueErr
			rt.sendErr = fmt.Errorf("venue refused: %w", trading.ErrRejected)
			rt.cancelled = tc.orderErr != nil
			got, err := s.SendPaperOrder(systemCtx(), paperSendTestOrder(), SendDestination{Route: "{}"})
			if err != nil || got.Send == nil || got.Send.Status != "unknown" || got.Send.Order != nil || n.reads != 1 || n.cancels != 0 {
				t.Fatalf("refusal result=%+v send=%+v err=%v reads=%d cancels=%d", got, got.Send, err, n.reads, n.cancels)
			}
			want := rt.sendErr.Error() + "\n" + tc.wantError
			if got.Send.Error != want {
				t.Fatalf("send error = %q, want %q", got.Send.Error, want)
			}
		})
	}
}

func TestSendPaperOrderVenueRead(t *testing.T) {
	for _, name := range []string{"no_link", "read_error"} {
		t.Run(name, func(t *testing.T) {
			s, n, rt, _ := newPaperSendTestService(t)
			rt.returned = domain.VenueOrder{Order: "draft-order", VenueOrderID: "not-stored"}
			if name == "read_error" {
				n.venueErr = errors.New("store unavailable")
			}
			got, err := s.SendPaperOrder(systemCtx(), paperSendTestOrder(), SendDestination{Route: "{}"})
			if err != nil || got.Send == nil || got.Token.OrderExternalID != "draft-order" || got.Send.Order != nil || n.reads != 1 {
				t.Fatalf("venue read result=%+v send=%+v err=%v", got, got.Send, err)
			}
			if name == "read_error" {
				if got.Send.Status != "unknown" || !strings.Contains(got.Send.Error, "store unavailable") {
					t.Fatalf("lost read error: %+v", got.Send)
				}
			} else if got.Send.Status != "acknowledged" || got.Send.Error != "" {
				t.Fatalf("no link outcome: %+v", got.Send)
			}
		})
	}
}
