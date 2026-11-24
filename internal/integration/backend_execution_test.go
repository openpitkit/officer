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

package integration_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	fwsigning "go.openpit.dev/officer/framework/signing"
)

func TestService_ExecutionReportVenueTradeIDReachesStoredTrade(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src, _ := reportFillChain(t)
	if _, err := service.ApplyAdjustment(ctx, "desk-1", domain.ExternalID(""),
		sampleAdjustmentRequest(), domain.MissingAccountReject); err != nil {
		t.Fatal(err)
	}
	for _, venueTradeID := range []string{"", "venue-trade-42"} {
		submitted := callSubmitOrder(t, src, submitOrderInput{
			Account: "desk-1", BaseAsset: "AAPL", QuoteAsset: "USD", Side: "buy",
			AmountKind: "quantity", AmountValue: "1", Price: "50", Mode: "hold",
			MissingAccount: "reject",
		})
		if submitted.IsError || submitted.StructuredContent.Verdict != "accept" {
			t.Fatalf("submit: %+v", submitted)
		}
		orderID := submitted.StructuredContent.OrderExternalID
		_, att, err := service.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
			Order: domain.ExternalID(orderID), VenueTradeID: venueTradeID,
			FillQuantity: "1", FillPrice: "50", LeavesQuantity: "0",
			OrderStatus: domain.OrderStatusFilled,
		})
		if err != nil || !att.Signed {
			t.Fatalf("report: att=%+v err=%v", att, err)
		}
		detail, err := service.GetOrder(ctx, orderID)
		if err != nil || len(detail.Trades) != 1 || detail.Trades[0].VenueTradeID != venueTradeID {
			t.Fatalf("stored trades = %+v, want venue trade id %q; err=%v", detail.Trades, venueTradeID, err)
		}
	}
}

func TestService_ExecutionReportVenueTradeIDLeavesSignedPayloadUnchanged(t *testing.T) {
	t.Parallel()
	var wantPayload []byte
	var wantEvent []byte
	for _, venueTradeID := range []string{"", "venue-trade-42"} {
		signer := &fakeSigner{}
		service, fn := newTestServiceWithSigner(t, signer)
		orderID := orderID(t, mustWorkflow(t, service, "1"))
		beforeSigned := len(signer.signed)
		beforeEvents := len(fn.orderEvents[orderID])
		_, _, err := service.ApplyExecutionReport(systemCtx(), domain.ExecutionReportInput{
			ExternalID: mdID("report-1"), Order: orderID, VenueTradeID: venueTradeID,
			FillQuantity: "1", FillPrice: "100", LeavesQuantity: "0",
			OrderStatus: domain.OrderStatusFilled,
		})
		if err != nil || len(signer.signed) != beforeSigned+1 || len(fn.execReports) != 1 {
			t.Fatalf("report: signed=%+v reports=%+v err=%v", signer.signed, fn.execReports, err)
		}
		if fn.execReports[0].VenueTradeID != venueTradeID {
			t.Fatalf("backend lost venue trade id: %+v", fn.execReports[0])
		}
		payload := signer.signed[beforeSigned]
		// Only per-issuance randomness differs between these identical reports.
		payload.ApprovalID, payload.Nonce, payload.IssuedAt, payload.EventExternalID = "", "", "", ""
		wire, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		event, err := json.Marshal(fn.orderEvents[orderID][beforeEvents].Payload)
		if err != nil {
			t.Fatal(err)
		}
		if venueTradeID == "" {
			wantPayload, wantEvent = wire, event
		} else if !bytes.Equal(wantPayload, wire) || !bytes.Equal(wantEvent, event) {
			t.Fatalf("venue trade id changed signed payload or event:\npayload=%s\nevent=%s", wire, event)
		}
	}
}

func TestService_ApplyExecutionReportTerminalRequiresForce(t *testing.T) {
	t.Parallel()
	svc, fn := newTestServiceWithSigner(t, &fakeSigner{})
	orderID := orderID(t, mustWorkflow(t, svc, "1"))
	if _, _, err := svc.CancelOrder(systemCtx(), orderID.String(), "1", "operator cancelled"); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	beforeReports := len(fn.execReports)
	report := domain.ExecutionReportInput{
		Order:        orderID,
		Account:      "acc-1",
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "1",
		FillPrice:    "100",
		OrderStatus:  domain.OrderStatusFilled,
	}

	_, _, err := svc.ApplyExecutionReport(systemCtx(), report)
	if !errors.Is(err, domain.ErrTerminalOrder) {
		t.Fatalf("ApplyExecutionReport terminal = %v, want terminal order", err)
	}
	if len(fn.execReports) != beforeReports {
		t.Fatalf("terminal report reached node: %+v", fn.execReports)
	}
}

func TestService_ApplyExecutionReportForceBypassesTerminalGuard(t *testing.T) {
	t.Parallel()
	svc, fn := newTestServiceWithSigner(t, &fakeSigner{})
	orderID := orderID(t, mustWorkflow(t, svc, "1"))
	if _, _, err := svc.CancelOrder(systemCtx(), orderID.String(), "1", "operator cancelled"); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	beforeReports := len(fn.execReports)
	report := domain.ExecutionReportInput{
		Order:        orderID,
		Account:      "acc-1",
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "1",
		FillPrice:    "100",
		Force:        true,
		OrderStatus:  domain.OrderStatusFilled,
	}

	if _, _, err := svc.ApplyExecutionReport(systemCtx(), report); err != nil {
		t.Fatalf("ApplyExecutionReport force: %v", err)
	}
	if len(fn.execReports) != beforeReports+1 || fn.execReports[beforeReports].Order != orderID {
		t.Fatalf("report not forwarded: %+v", fn.execReports)
	}
}

func TestService_ApplyExecutionReportRejectsMissingEventOrderStatus(t *testing.T) {
	t.Parallel()
	for _, eventType := range []domain.OrderEventType{
		domain.OrderEventFill, domain.OrderEventCommission,
	} {
		t.Run(string(eventType), func(t *testing.T) {
			signer := &fakeSigner{}
			svc, fn := newTestServiceWithSigner(t, signer)
			tok := mustWorkflow(t, svc)
			orderID := domain.ExternalID(tok.OrderExternalID)
			beforeOrder := fn.orders[orderID]
			beforeEvents := slices.Clone(fn.orderEvents[orderID])
			beforeSigned := len(signer.signed)
			fn.omitExecutionReportEventStatus = true
			report := domain.ExecutionReportInput{
				Order: orderID, OrderStatus: domain.OrderStatusFilled,
				LeavesQuantity: "0",
			}
			if eventType == domain.OrderEventFill {
				report.FillQuantity = "10"
				report.FillPrice = "100"
			} else {
				report.Commission = &domain.Commission{
					Amount: "-0.12", Currency: "USD",
				}
			}

			_, att, err := svc.ApplyExecutionReport(systemCtx(), report)
			if err == nil || !strings.Contains(err.Error(), "execution report event missing order status") {
				t.Errorf("ApplyExecutionReport error = %v, want missing event order status", err)
			}
			if att.SignedAttestation != "" || len(signer.signed) != beforeSigned {
				t.Error("execution report with missing event order status was signed")
			}
			if !reflect.DeepEqual(fn.orders[orderID], beforeOrder) {
				t.Errorf("order after failed report = %+v, want %+v", fn.orders[orderID], beforeOrder)
			}
			if !reflect.DeepEqual(fn.orderEvents[orderID], beforeEvents) {
				t.Errorf("history after failed report = %+v, want %+v", fn.orderEvents[orderID], beforeEvents)
			}
		})
	}
}

// TestService_ApplyExecutionReportAttestsFillEvent covers the report attestation
// path: with a configured signer the report signs an attestation over its result
// and stamps it onto the fill event, returning a signed token and persisting one
// attestation.
func TestService_ApplyExecutionReportAttestsFillEvent(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	orderID := orderID(t, mustWorkflow(t, svc, "1"))
	beforeAttestations := len(fn.persistAttestationCalls)
	beforeSigned := len(signer.signed)
	report := domain.ExecutionReportInput{
		Order:        orderID,
		Account:      "acc-1",
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "1",
		FillPrice:    "100",
		OrderStatus:  domain.OrderStatusFilled,
	}

	_, att, err := svc.ApplyExecutionReport(systemCtx(), report)
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if att.SignedAttestation == "" || !att.Signed {
		t.Fatalf("report must return a signed attestation, got %+v", att)
	}
	if len(fn.persistAttestationCalls) != beforeAttestations+1 {
		t.Fatalf("report must persist one attestation, calls=%+v", fn.persistAttestationCalls)
	}
	// The attestation binds the fill event: GetOrder surfaces it on that event.
	detail, err := svc.GetOrder(systemCtx(), orderID.String())
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	var fill *domain.OrderEvent
	for i := range detail.Events {
		if detail.Events[i].Type == domain.OrderEventFill {
			fill = &detail.Events[i]
		}
	}
	if fill == nil || fill.Attestation == nil || fill.Attestation.SignedEnvelope == "" ||
		fill.Attestation.Alg != fwsigning.AlgEd25519 {
		t.Fatalf("report must stamp a signed attestation on the fill event, got %+v", fill)
	}
	if len(signer.signed) != beforeSigned+1 || signer.signed[beforeSigned].Result == nil {
		t.Fatalf("signed report result = %+v", signer.signed)
	}
}

func TestService_ApplyExecutionReportAttestsNoTradeCommission(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, _ := newTestServiceWithSigner(t, signer)
	orderID := orderID(t, mustWorkflow(t, svc, "2"))
	beforeSigned := len(signer.signed)
	report := domain.ExecutionReportInput{
		Order:          orderID,
		LeavesQuantity: "2",
		Commission: &domain.Commission{
			Amount:   "-0.12",
			Currency: "USD",
		},
		OrderStatus: domain.OrderStatusCancelled,
	}

	result, _, err := svc.ApplyExecutionReport(systemCtx(), report)
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if result.Persistence == nil || result.Persistence.Trade != nil {
		t.Fatalf("terminal report persistence = %+v, want no trade", result.Persistence)
	}
	if len(signer.signed) != beforeSigned+1 || signer.signed[beforeSigned].Result == nil {
		t.Fatalf("signed report result = %+v", signer.signed)
	}
	commission := signer.signed[beforeSigned].Result.Commission
	if commission == nil || commission.Amount != "-0.12" ||
		commission.Currency != "USD" {
		t.Fatalf("signed report commission = %+v, want -0.12/USD", commission)
	}
}

func TestService_ApplyExecutionReportSigningFailureFailsClosed(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	orderID := orderID(t, mustWorkflow(t, svc, "1"))
	beforeAttestations := len(fn.persistAttestationCalls)
	beforeEvents := slices.Clone(fn.orderEvents[orderID])
	signer.signErr = errors.New("report signing down")
	report := domain.ExecutionReportInput{
		Order:        orderID,
		Account:      "acc-1",
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "1",
		FillPrice:    "100",
		OrderStatus:  domain.OrderStatusFilled,
	}

	_, _, err := svc.ApplyExecutionReport(systemCtx(), report)
	if !errors.Is(err, signer.signErr) {
		t.Fatalf("ApplyExecutionReport error = %v, want signing failure", err)
	}
	if len(fn.persistAttestationCalls) != beforeAttestations {
		t.Fatalf("signing failure must not persist attestation, calls=%+v",
			fn.persistAttestationCalls)
	}
	if !reflect.DeepEqual(fn.orderEvents[orderID], beforeEvents) {
		t.Fatalf("signing failure left fake events: %+v", fn.orderEvents[orderID])
	}
}

func TestService_ApplyExecutionReportSignsAllEventsAndReturnsFill(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	orderID := orderID(t, mustWorkflow(t, svc, "1"))
	beforeAttestations := len(fn.persistAttestationCalls)
	beforeEvents := len(fn.orderEvents[orderID])
	beforeSigned := len(signer.signed)
	report := domain.ExecutionReportInput{
		Order:          orderID,
		Account:        "acc-1",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		FillQuantity:   "1",
		FillPrice:      "100",
		LockPrice:      "99",
		LeavesQuantity: "0",
		OrderStatus:    domain.OrderStatusCancelled,
	}

	_, att, err := svc.ApplyExecutionReport(systemCtx(), report)
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	events := fn.orderEvents[orderID][beforeEvents:]
	if len(events) != 2 ||
		events[0].Type != domain.OrderEventFill ||
		events[1].Type != domain.OrderEventCancelled {
		t.Fatalf("events = %+v, want [fill cancelled]", events)
	}
	wantCalls := []domain.ExternalID{events[0].ExternalID, events[1].ExternalID}
	if !slices.Equal(fn.persistAttestationCalls[beforeAttestations:], wantCalls) {
		t.Fatalf("attested events = %+v, want %+v",
			fn.persistAttestationCalls, wantCalls)
	}
	if att.EventExternalID != events[0].ExternalID.String() {
		t.Fatalf("returned attestation event = %q, want fill %q",
			att.EventExternalID, events[0].ExternalID.String())
	}
	signed := signer.signed[beforeSigned:]
	if len(signed) != 2 ||
		signed[0].Result == nil ||
		signed[0].Result.FillQuantity != "1" ||
		signed[1].Result == nil ||
		signed[1].Result.OrderStatus != string(domain.OrderStatusCancelled) {
		t.Fatalf("signed payloads = %+v, want fill then cancelled lifecycle", signed)
	}
}

func TestService_ApplyExecutionReportMissingAttestationFailsClosed(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(t, signer)
	fn.execReportNoop = true
	orderID := orderID(t, mustWorkflow(t, svc, "1"))
	beforeEvents := slices.Clone(fn.orderEvents[orderID])

	_, _, err := svc.ApplyExecutionReport(systemCtx(), domain.ExecutionReportInput{
		Order:          orderID,
		Account:        "acc-1",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Side:           domain.OrderSideBuy,
		OrderStatus:    domain.OrderStatusCancelled,
		LeavesQuantity: "0",
	})
	if err == nil {
		t.Fatal("ApplyExecutionReport succeeded without an attestation")
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyExecutionReport error = %v, want ErrInvalid", err)
	}
	if !reflect.DeepEqual(fn.orderEvents[orderID], beforeEvents) {
		t.Fatalf("missing signable event left fake events: %+v", fn.orderEvents[orderID])
	}
}

func TestService_ApplyExecutionReportInvalidSubmitAttestationFailsClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		invalidMode string
		wantError   string
	}{
		{name: "missing", wantError: "backend: submit attestation missing"},
		{name: "unknown-mode", invalidMode: "unknown", wantError: `backend: invalid submit attestation mode "unknown"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := systemCtx()
			signer := &fakeSigner{}
			svc, fn := newTestServiceWithSigner(t, signer)
			var id domain.ExternalID
			if tc.invalidMode == "" {
				order := sampleOrder()
				order.ExternalID = mdID("order-missing-submit")
				order.AmountValue = "1"
				order.Status = domain.OrderStatusAccepted
				id = order.ExternalID
				fn.orders[id] = order
			} else {
				id = orderID(t, mustWorkflow(t, svc, "1"))
				for _, event := range fn.orderEvents[id] {
					attestation := fn.attestations[event.ExternalID]
					if attestation.RequestType == domain.AttestationRequestSubmit {
						attestation.Mode = tc.invalidMode
						fn.attestations[event.ExternalID] = attestation
						break
					}
				}
			}
			before, err := fn.GetOrder(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			beforeReports := len(fn.execReports)
			beforeSigned := len(signer.signed)
			beforeAttestations := len(fn.persistAttestationCalls)

			_, _, err = svc.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
				Order:          id,
				Account:        "acc-1",
				BaseAsset:      "AAPL",
				QuoteAsset:     "USD",
				Side:           domain.OrderSideBuy,
				FillQuantity:   "1",
				FillPrice:      "100",
				LeavesQuantity: "0",
				OrderStatus:    domain.OrderStatusFilled,
			})
			if err == nil || err.Error() != tc.wantError {
				t.Errorf("ApplyExecutionReport error = %v, want %q", err, tc.wantError)
			}
			if len(fn.execReports) != beforeReports {
				t.Errorf("report reached node: %+v", fn.execReports)
			}
			if len(signer.signed) != beforeSigned {
				t.Errorf("report was signed: %+v", signer.signed[beforeSigned:])
			}
			if len(fn.persistAttestationCalls) != beforeAttestations {
				t.Errorf("report attestation was persisted: %+v", fn.persistAttestationCalls)
			}
			after, err := fn.GetOrder(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Errorf("report changed stored order: got %+v, want %+v", after, before)
			}
		})
	}
}

func TestService_ApplyExecutionReportStatusLifecycleReachesNode(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	svc, fn := newTestServiceWithSigner(t, &fakeSigner{})
	orderID := orderID(t, mustWorkflow(t, svc, "1"))
	report := domain.ExecutionReportInput{
		Order:          orderID,
		OrderStatus:    domain.OrderStatusCancelled,
		LeavesQuantity: "0",
	}
	before, err := fn.GetOrder(ctx, orderID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if before.Order.Status == report.OrderStatus {
		t.Fatalf("stored status %q must differ from requested status", before.Order.Status)
	}

	if _, _, err := svc.ApplyExecutionReport(ctx, report); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if len(fn.execReports) != 1 || fn.execReports[0].OrderStatus != report.OrderStatus {
		t.Fatalf("forwarded reports = %+v, want one with status %q", fn.execReports, report.OrderStatus)
	}
}

func TestService_ApplyExecutionReportRejectsInvalidStatusBeforeNode(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	orderID := mdID("order-5")
	fn.orders[orderID] = domain.Order{
		ExternalID: orderID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Side:       domain.OrderSideBuy,
		Status:     domain.OrderStatusSubmitted,
	}

	_, _, err := svc.ApplyExecutionReport(systemCtx(), domain.ExecutionReportInput{
		Order:        orderID,
		Account:      "acc-1",
		BaseAsset:    "AAPL",
		QuoteAsset:   "USD",
		Side:         domain.OrderSideBuy,
		FillQuantity: "1",
		FillPrice:    "100",
		Force:        true,
		OrderStatus:  domain.OrderStatus("bogus"),
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyExecutionReport invalid status = %v, want invalid", err)
	}
	if len(fn.execReports) != 0 {
		t.Fatalf("invalid status reached node: %+v", fn.execReports)
	}
}
