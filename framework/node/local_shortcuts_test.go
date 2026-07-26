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

package node

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

type attestorFailingSettlementRealm struct {
	store.RealmStore
}

func (r *attestorFailingSettlementRealm) RecordOrderSettlementWithAttestation(
	ctx context.Context,
	settlement domain.OrderSettlement,
	attest store.EventAttestor,
) (domain.ExternalID, error) {
	if attest == nil {
		return r.RecordOrderSettlement(ctx, settlement)
	}
	for _, event := range settlement.Events {
		if _, _, err := attest(ctx, event); err != nil {
			return "", err
		}
	}
	return r.RecordOrderSettlement(ctx, settlement)
}

func submitShortcutOrder(
	t *testing.T, n *localNode, realm store.RealmStore, eng *fakeEngine,
) domain.Order {
	t.Helper()
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := realm.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "10000",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	eng.submitLock = []byte("stored-lock")
	eng.submitLeaves = "20"
	eng.submitOutcomes = []engine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceResult: "8000",
			HeldResult:    "2000",
		},
	}}
	order, err := n.SubmitOrder(ctx, testKey("acc-1"), domain.Order{
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	return order
}

func TestLocalNode_ConfirmOrderIsHistoryOnlyAndIdempotent(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, realm := newTestNode(t, eng)
	order := submitShortcutOrder(t, n, realm, eng)
	ctx := context.Background()

	confirmed, err := n.ConfirmOrder(ctx, order.ExternalID, testCaller)
	if err != nil {
		t.Fatalf("ConfirmOrder: %v", err)
	}
	if confirmed.Status != order.Status || confirmed.Leaves != order.Leaves ||
		!bytes.Equal(confirmed.Lock, order.Lock) {
		t.Fatalf("confirmed order mutated: got %+v, want lifecycle %+v", confirmed, order)
	}
	if len(eng.execReportCalls) != 0 {
		t.Fatalf("confirmation reached engine: %+v", eng.execReportCalls)
	}

	if _, err := n.ConfirmOrder(ctx, order.ExternalID, testCaller); err != nil {
		t.Fatalf("idempotent ConfirmOrder: %v", err)
	}
	events, err := realm.ListOrderEvents(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("ListOrderEvents: %v", err)
	}
	confirmedEvents := 0
	for _, event := range events {
		if event.Type == domain.OrderEventConfirmed {
			confirmedEvents++
		}
	}
	if confirmedEvents != 1 {
		t.Fatalf("confirmed events = %d, want 1: %+v", confirmedEvents, events)
	}
	balance, ok, err := realm.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance: %v ok=%v", err, ok)
	}
	if balance.Available != "8000" || balance.Held != "2000" {
		t.Fatalf("confirmation changed balance: %+v", balance)
	}
}

func TestLocalNode_CancelOrderUsesStoredLockAndLeaves(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, realm := newTestNode(t, eng)
	order := submitShortcutOrder(t, n, realm, eng)
	eng.execReportOutcomes = []engine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceResult: "10000",
			HeldResult:    "0",
		},
	}}
	ctx := context.Background()

	if _, err := n.ConfirmOrder(ctx, order.ExternalID, testCaller); err != nil {
		t.Fatalf("ConfirmOrder before cancel: %v", err)
	}
	cancelled, result, err := n.CancelOrder(ctx, order.ExternalID, testCaller)
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if cancelled.Status != domain.OrderStatusCancelled || cancelled.Leaves != "0" {
		t.Fatalf("cancelled order = %+v, want cancelled with zero leaves", cancelled)
	}
	if result.Persistence == nil {
		t.Fatal("CancelOrder result has no persistence")
	}
	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
	}
	input := eng.execReportCalls[0]
	if input.Order != order.ExternalID || input.OrderStatus != domain.OrderStatusCancelled ||
		input.LeavesQuantity != "20" || !bytes.Equal(input.Lock, []byte("stored-lock")) ||
		input.FillQuantity != "" || input.FillPrice != "" {
		t.Fatalf("synthetic cancellation input = %+v", input)
	}

	detail, err := realm.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	foundReport := false
	for _, event := range detail.Events {
		request := event.Payload.ExecutionReport
		if event.Type == domain.OrderEventCancelled && request != nil {
			foundReport = request.OrderStatus == domain.OrderStatusCancelled &&
				request.LeavesQuantity == "20"
		}
	}
	if !foundReport {
		t.Fatalf("cancel event lacks synthetic execution report: %+v", detail.Events)
	}
	balance, ok, err := realm.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance: %v ok=%v", err, ok)
	}
	if balance.Available != "10000" || balance.Held != "0" {
		t.Fatalf("cancel balance = %+v, want available=10000 held=0", balance)
	}
}

func TestLocalNode_CancelOrderAttestorFailureFailsStop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	baseStore := newMemoryStore("node.db")
	if err := baseStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = baseStore.Close() })
	wrapped := newRealmWrapStore(
		baseStore,
		func(realm store.RealmStore) store.RealmStore {
			return &attestorFailingSettlementRealm{RealmStore: realm}
		},
	)
	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(
		t,
		wrapped,
		eng,
		WithFatalShutdownHook(func(err error) { fatalErr = err }),
	)
	order := submitShortcutOrder(t, n, n.realm, eng)
	eng.execReportOutcomes = []engine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceResult: "10000",
			HeldResult:    "0",
		},
	}}
	before, err := n.realm.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder before cancel: %v", err)
	}
	attestErr := errors.New("cancel attestation failed")
	attest := func(
		context.Context, domain.OrderEvent,
	) (domain.EventAttestation, bool, error) {
		return domain.EventAttestation{}, false, attestErr
	}

	if _, _, err := n.CancelOrderWithAttestation(
		ctx, order.ExternalID, testCaller, attest,
	); !errors.Is(err, attestErr) {
		t.Fatalf("CancelOrderWithAttestation error = %v, want attestor error", err)
	}
	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine calls = %+v, want one applied cancellation", eng.execReportCalls)
	}
	if !errors.Is(fatalErr, attestErr) {
		t.Fatalf("fatal error = %v, want wrapped attestor error", fatalErr)
	}
	after, err := n.realm.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder after failed cancel: %v", err)
	}
	if after.Order.Status != before.Order.Status ||
		after.Order.Leaves != before.Order.Leaves ||
		!bytes.Equal(after.Order.Lock, before.Order.Lock) ||
		len(after.Events) != len(before.Events) {
		t.Fatalf("failed cancel mutated store: before=%+v after=%+v", before, after)
	}
	balance, ok, err := n.realm.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance: %v ok=%v", err, ok)
	}
	if balance.Available != "8000" || balance.Held != "2000" {
		t.Fatalf("failed cancel persisted engine outcome: %+v", balance)
	}
}

func TestLocalNode_CancelOrderAfterEngineRebuildUsesStoredState(t *testing.T) {
	t.Parallel()
	initialEngine := newFakeEngine()
	n, realm := newTestNode(t, initialEngine)
	order := submitShortcutOrder(t, n, realm, initialEngine)
	ctx := context.Background()

	rebuiltEngine := newFakeEngine()
	rebuiltEngine.execReportOutcomes = []engine.BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceResult: "10000",
			HeldResult:    "0",
		},
	}}
	var snapshot engine.Snapshot
	n.build = fakeBuild(rebuiltEngine, &snapshot)
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		t.Fatalf("rebuildEngineFromStore: %v", err)
	}

	cancelled, _, err := n.CancelOrder(ctx, order.ExternalID, testCaller)
	if err != nil {
		t.Fatalf("CancelOrder after rebuild: %v", err)
	}
	if cancelled.Status != domain.OrderStatusCancelled || cancelled.Leaves != "0" {
		t.Fatalf("cancelled order = %+v", cancelled)
	}
	if len(initialEngine.execReportCalls) != 0 {
		t.Fatalf("stopped engine received cancellation: %+v", initialEngine.execReportCalls)
	}
	if len(rebuiltEngine.execReportCalls) != 1 {
		t.Fatalf("rebuilt engine calls = %+v", rebuiltEngine.execReportCalls)
	}
	input := rebuiltEngine.execReportCalls[0]
	if input.LeavesQuantity != "20" ||
		!bytes.Equal(input.Lock, []byte("stored-lock")) {
		t.Fatalf("restarted cancellation lost stored state: %+v", input)
	}
}

func TestLocalNode_ShortcutsRequireExplicitReportAfterActivity(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, realm := newTestNode(t, eng)
	order := submitShortcutOrder(t, n, realm, eng)
	ctx := context.Background()

	if _, err := n.ApplyExecutionReport(
		ctx,
		testKey("acc-1"),
		domain.ExecutionReportInput{
			Order:          order.ExternalID,
			LeavesQuantity: "19",
			OrderStatus:    domain.OrderStatusCommitted,
		},
		testCaller,
	); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if _, err := n.ConfirmOrder(ctx, order.ExternalID, testCaller); !errors.Is(err, domain.ErrExecutionReportRequired) {
		t.Fatalf("ConfirmOrder error = %v, want ErrExecutionReportRequired", err)
	}
	if _, _, err := n.CancelOrder(ctx, order.ExternalID, testCaller); !errors.Is(err, domain.ErrExecutionReportRequired) {
		t.Fatalf("CancelOrder error = %v, want ErrExecutionReportRequired", err)
	}
	if len(eng.execReportCalls) != 0 {
		t.Fatalf("guarded shortcut reached engine: %+v", eng.execReportCalls)
	}
}

func TestLocalNode_ShortcutsRejectTerminalOrderWithoutReportEvent(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, realm := newTestNode(t, eng)
	order := submitShortcutOrder(t, n, realm, eng)
	ctx := context.Background()

	if _, err := realm.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Account:     order.Account,
		Order:       order.ExternalID,
		OrderStatus: domain.OrderStatusCancelled,
		Leaves:      "0",
		AllowedFrom: []domain.OrderStatus{domain.OrderStatusCommitted},
		Events: []domain.OrderEvent{{
			Order:     order.ExternalID,
			Type:      domain.OrderEventCancelled,
			Source:    testCaller.Source,
			Principal: testCaller.Principal,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement: %v", err)
	}

	if _, err := n.ConfirmOrder(
		ctx, order.ExternalID, testCaller,
	); !errors.Is(err, domain.ErrTerminalOrder) {
		t.Fatalf("ConfirmOrder error = %v, want ErrTerminalOrder", err)
	}
	if _, _, err := n.CancelOrder(
		ctx, order.ExternalID, testCaller,
	); !errors.Is(err, domain.ErrTerminalOrder) {
		t.Fatalf("CancelOrder error = %v, want ErrTerminalOrder", err)
	}
	if len(eng.execReportCalls) != 0 {
		t.Fatalf("terminal shortcuts reached engine: %+v", eng.execReportCalls)
	}
}
