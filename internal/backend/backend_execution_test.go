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
	"slices"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	fwsigning "go.openpit.dev/officer/framework/signing"
)

func TestService_ApplyExecutionReportTerminalRequiresForce(t *testing.T) {
	t.Parallel()
	svc, fn := newTestServiceWithSigner(&fakeSigner{})
	orderID := mdID("order-4")
	fn.orders[orderID] = domain.Order{
		ExternalID: orderID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Side:       domain.OrderSideBuy,
		Status:     domain.OrderStatusCancelled,
	}
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

	_, _, err := svc.ApplyExecutionReport(context.Background(), report)
	if !errors.Is(err, domain.ErrTerminalOrder) {
		t.Fatalf("ApplyExecutionReport terminal = %v, want terminal order", err)
	}
	if len(fn.execReports) != 0 {
		t.Fatalf("terminal report reached node: %+v", fn.execReports)
	}
}

func TestService_ApplyExecutionReportForceBypassesTerminalGuard(t *testing.T) {
	t.Parallel()
	svc, fn := newTestServiceWithSigner(&fakeSigner{})
	orderID := mdID("order-4")
	fn.orders[orderID] = domain.Order{
		ExternalID: orderID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Side:       domain.OrderSideBuy,
		Status:     domain.OrderStatusCancelled,
	}
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

	if _, _, err := svc.ApplyExecutionReport(context.Background(), report); err != nil {
		t.Fatalf("ApplyExecutionReport force: %v", err)
	}
	if len(fn.execReports) != 1 || fn.execReports[0].Order != orderID {
		t.Fatalf("report not forwarded: %+v", fn.execReports)
	}
}

// TestService_ApplyExecutionReportAttestsFillEvent covers the report attestation
// path: with a configured signer the report signs an attestation over its result
// and stamps it onto the fill event, returning a signed token and persisting one
// attestation.
func TestService_ApplyExecutionReportAttestsFillEvent(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	orderID := mdID("order-report")
	fn.orders[orderID] = domain.Order{
		ExternalID: orderID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Side:       domain.OrderSideBuy,
		Status:     domain.OrderStatusAccepted,
	}
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

	_, att, err := svc.ApplyExecutionReport(context.Background(), report)
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if att.Token == "" || !att.Signed {
		t.Fatalf("report must return a signed attestation, got %+v", att)
	}
	if len(fn.persistAttestationCalls) != 1 {
		t.Fatalf("report must persist one attestation, calls=%+v", fn.persistAttestationCalls)
	}
	// The attestation binds the fill event: GetOrder surfaces it on that event.
	detail, err := svc.GetOrder(context.Background(), orderID.String())
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	var fill *domain.OrderEvent
	for i := range detail.Events {
		if detail.Events[i].Type == domain.OrderEventFill {
			fill = &detail.Events[i]
		}
	}
	if fill == nil || fill.Attestation == nil || fill.Attestation.Token == "" ||
		fill.Attestation.Alg != fwsigning.AlgEd25519 {
		t.Fatalf("report must stamp a signed attestation on the fill event, got %+v", fill)
	}
}

func TestService_ApplyExecutionReportSigningFailureFailsClosed(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{signErr: errors.New("report signing down")}
	svc, fn := newTestServiceWithSigner(signer)
	orderID := mdID("order-report-signing")
	fn.orders[orderID] = domain.Order{
		ExternalID: orderID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Side:       domain.OrderSideBuy,
		Status:     domain.OrderStatusAccepted,
	}
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

	_, _, err := svc.ApplyExecutionReport(context.Background(), report)
	if !errors.Is(err, signer.signErr) {
		t.Fatalf("ApplyExecutionReport error = %v, want signing failure", err)
	}
	if len(fn.persistAttestationCalls) != 0 {
		t.Fatalf("signing failure must not persist attestation, calls=%+v",
			fn.persistAttestationCalls)
	}
	if len(fn.orderEvents[orderID]) != 0 {
		t.Fatalf("signing failure left fake events: %+v", fn.orderEvents[orderID])
	}
}

func TestService_ApplyExecutionReportSignsAllEventsAndReturnsFill(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	orderID := mdID("order-report-multi")
	fn.orders[orderID] = domain.Order{
		ExternalID: orderID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Side:       domain.OrderSideBuy,
		Status:     domain.OrderStatusAccepted,
	}
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

	_, att, err := svc.ApplyExecutionReport(context.Background(), report)
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	events := fn.orderEvents[orderID]
	if len(events) != 2 ||
		events[0].Type != domain.OrderEventFill ||
		events[1].Type != domain.OrderEventCancelled {
		t.Fatalf("events = %+v, want [fill cancelled]", events)
	}
	wantCalls := []domain.ExternalID{events[0].ExternalID, events[1].ExternalID}
	if !slices.Equal(fn.persistAttestationCalls, wantCalls) {
		t.Fatalf("attested events = %+v, want %+v",
			fn.persistAttestationCalls, wantCalls)
	}
	if att.EventExternalID != events[0].ExternalID.String() {
		t.Fatalf("returned attestation event = %q, want fill %q",
			att.EventExternalID, events[0].ExternalID.String())
	}
	if len(signer.signed) != 2 ||
		signer.signed[0].Result == nil ||
		signer.signed[0].Result.FillQuantity != "1" ||
		signer.signed[1].Result == nil ||
		signer.signed[1].Result.OrderStatus != string(domain.OrderStatusCancelled) {
		t.Fatalf("signed payloads = %+v, want fill then cancelled lifecycle", signer.signed)
	}
}

func TestService_ApplyExecutionReportMissingAttestationFailsClosed(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	svc, fn := newTestServiceWithSigner(signer)
	fn.execReportNoop = true
	orderID := mdID("order-report-noop")
	fn.orders[orderID] = domain.Order{
		ExternalID: orderID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Side:       domain.OrderSideBuy,
		Status:     domain.OrderStatusAccepted,
	}

	_, _, err := svc.ApplyExecutionReport(context.Background(), domain.ExecutionReportInput{
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
	if len(fn.orderEvents[orderID]) != 0 {
		t.Fatalf("missing signable event left fake events: %+v", fn.orderEvents[orderID])
	}
}

func TestService_ApplyExecutionReportStatusLifecycleReachesNode(t *testing.T) {
	t.Parallel()
	svc, fn := newTestServiceWithSigner(&fakeSigner{})
	orderID := mdID("order-4")
	fn.orders[orderID] = domain.Order{
		ExternalID: orderID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Side:       domain.OrderSideBuy,
		Status:     domain.OrderStatusAccepted,
	}
	report := domain.ExecutionReportInput{
		Order:       orderID,
		OrderStatus: domain.OrderStatusCommitted,
	}

	if _, _, err := svc.ApplyExecutionReport(context.Background(), report); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if len(fn.execReports) != 1 ||
		fn.execReports[0].OrderStatus != domain.OrderStatusCommitted {
		t.Fatalf("report not forwarded: %+v", fn.execReports)
	}
}

func TestService_ApplyExecutionReportRejectsInvalidStatusBeforeNode(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	orderID := mdID("order-5")
	fn.orders[orderID] = domain.Order{
		ExternalID: orderID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Side:       domain.OrderSideBuy,
		Status:     domain.OrderStatusSubmitted,
	}

	_, _, err := svc.ApplyExecutionReport(context.Background(), domain.ExecutionReportInput{
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
