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
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	appsigning "go.openpit.dev/officer/signing"
)

// reportFillChain builds the Officer service over a local node running the
// OpenPit engine, signing with a freshly generated key with eSign on, and the
// tool Source over it, with the order tools switched on through the
// per-command switch. USD and AAPL exist, and desk-1 tracks its realized P&L
// in USD. It returns the active signing key id.
func reportFillChain(t *testing.T) (*backend.Service, frameworkmcp.Source, string) {
	t.Helper()
	ctx := systemCtx()
	target, realm := newLocalNode(t)
	signer, err := appsigning.New(realm)
	if err != nil {
		t.Fatalf("signing.New: %v", err)
	}
	key, err := signer.GenerateKey(ctx)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	service := newOfficerService(t, target, nil, signer)
	// A fresh realm starts under eSign-off.
	if err := service.SetNoESign(ctx, false); err != nil {
		t.Fatalf("switch eSign on: %v", err)
	}
	for _, code := range []string{"USD", "AAPL"} {
		if _, err := service.CreateAsset(ctx, domain.Asset{Code: code}); err != nil {
			t.Fatalf("create asset %s: %v", code, err)
		}
	}
	if _, err := service.CreateAccount(ctx, domain.Account{Code: "desk-1", Currency: "USD"}); err != nil {
		t.Fatalf("create account desk-1: %v", err)
	}
	for _, command := range []string{"submit_order", "submit_drop_copy_order", "report_fill"} {
		if err := service.SetMcpAccess(ctx, command, true); err != nil {
			t.Fatalf("switch on %s: %v", command, err)
		}
	}
	return service, frameworkmcp.SourceFor(service), key.KeyID
}

// submitDropCopy records a desk-1 drop-copy limit order on AAPL through
// submit_drop_copy_order and returns its id.
func submitDropCopy(t *testing.T, src frameworkmcp.Source, side, quantity, price string) string {
	t.Helper()
	res := callSubmitDropCopyOrder(t, src, submitDropCopyOrderInput{
		Account: "desk-1", BaseAsset: "AAPL", QuoteAsset: "USD", Side: side,
		AmountKind: "quantity", AmountValue: quantity, Price: price,
		MissingAccount: "create",
	})
	if res.IsError {
		t.Fatalf("submit_drop_copy_order: %s", textContent(res.Content))
	}
	return res.StructuredContent.OrderExternalID
}

// reportFill applies in through report_fill and returns the REST body it
// answered with.
func reportFill(t *testing.T, src frameworkmcp.Source, in map[string]any) httpx.ExecutionReportResponseDTO {
	t.Helper()
	res := callTool[httpx.ExecutionReportResponseDTO](t, src, "report_fill", in)
	if res.IsError {
		t.Fatalf("report_fill: %s", textContent(res.Content))
	}
	if len(res.Wire) == 0 {
		t.Fatalf("report_fill answered no structured body: %s", textContent(res.Content))
	}
	return res.StructuredContent
}

// reportOutcome returns the outcome of asset in a report's result.
func reportOutcome(t *testing.T, res httpx.ExecutionReportResponseDTO, asset string) httpx.ExecutionOutcomeDTO {
	t.Helper()
	for _, outcome := range res.Result.Outcomes {
		if outcome.Asset == asset {
			return outcome
		}
	}
	t.Fatalf("report %s has no %s outcome: %+v", res.ID, asset, res.Result.Outcomes)
	return httpx.ExecutionOutcomeDTO{}
}

// deskBalance reads desk-1's balance of asset through the service.
func deskBalance(t *testing.T, service *backend.Service, asset string) domain.Balance {
	t.Helper()
	balances, err := service.ListBalances(systemCtx(), "desk-1", asset)
	if err != nil {
		t.Fatalf("list desk-1 %s balance: %v", asset, err)
	}
	if len(balances) != 1 {
		t.Fatalf("desk-1 %s balances = %+v, want one", asset, balances)
	}
	return balances[0]
}

// reportedState is everything a report on an order of desk-1 can change: the
// order with its status, events and trades, and desk-1's balances.
type reportedState struct {
	order    domain.OrderDetail
	balances []domain.Balance
}

func readReportedState(t *testing.T, service *backend.Service, order string) reportedState {
	t.Helper()
	ctx := systemCtx()
	detail, err := service.GetOrder(ctx, order)
	if err != nil {
		t.Fatalf("read order %s: %v", order, err)
	}
	balances, err := service.ListBalances(ctx, "desk-1", "")
	if err != nil {
		t.Fatalf("list desk-1 balances: %v", err)
	}
	return reportedState{order: detail, balances: balances}
}

// requireUnattested fails the test unless a report answered as one on a
// drop-copy order: unsigned and with no attestation.
func requireUnattested(t *testing.T, res httpx.ExecutionReportResponseDTO) {
	t.Helper()
	if res.Signed || res.SignedAttestation != "" || res.AttestationKeyID != "" {
		t.Fatalf("drop-copy report %s attestation = %q key %q signed %v, want none",
			res.ID, res.SignedAttestation, res.AttestationKeyID, res.Signed)
	}
}

// TestReportFillDropCopyOnControlPlane covers fills on drop-copy orders
// through report_fill into the production engine with an active signing key:
// each report is unsigned and attests no event, each fill records its trade,
// and closing the AAPL position bought at 150 with a sale at 160 realizes
// (160 - 150) x 1 = 10 in desk-1's currency.
func TestReportFillDropCopyOnControlPlane(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src, _ := reportFillChain(t)

	buy := submitDropCopy(t, src, "buy", "1", "150")
	opened := reportFill(t, src, map[string]any{
		"order": buy, "status": "filled", "quantity": "1", "price": "150", "leavesQuantity": "0",
	})
	requireUnattested(t, opened)
	sell := submitDropCopy(t, src, "sell", "1", "160")
	closed := reportFill(t, src, map[string]any{
		"order": sell, "status": "filled", "quantity": "1", "price": "160", "leavesQuantity": "0",
	})
	requireUnattested(t, closed)

	aapl := reportOutcome(t, closed, "AAPL")
	if aapl.RealizedPnlDelta != "10" || aapl.RealizedPnlResult != "10" {
		t.Fatalf("closing AAPL realized P&L delta %q result %q, want 10 and 10",
			aapl.RealizedPnlDelta, aapl.RealizedPnlResult)
	}
	if got := deskBalance(t, service, "AAPL"); got.RealizedPnl != "10" || got.AccountCurrency != "USD" {
		t.Fatalf("desk-1 AAPL realized P&L = %q in %q, want 10 in USD", got.RealizedPnl, got.AccountCurrency)
	}
	for order, price := range map[string]string{buy: "150", sell: "160"} {
		detail, err := service.GetOrder(ctx, order)
		if err != nil {
			t.Fatalf("read order %s: %v", order, err)
		}
		if detail.Order.Status != domain.OrderStatusFilled {
			t.Fatalf("order %s status = %q, want filled", order, detail.Order.Status)
		}
		if len(detail.Trades) != 1 || detail.Trades[0].Quantity != "1" || detail.Trades[0].Price != price {
			t.Fatalf("order %s trades = %+v, want one of 1 at %s", order, detail.Trades, price)
		}
		for _, event := range detail.Events {
			if event.Attestation != nil {
				t.Fatalf("drop-copy order %s event %s is attested: %+v", order, event.Type, event.Attestation)
			}
		}
	}
}

// TestReportFillTerminalReleasesDropCopyReserveOnControlPlane covers a
// terminal report on a drop-copy order through report_fill: the buy of 2 at
// 150 holds its notional of 300 USD, a fill of 1 consumes its own 150, and
// the cancellation releases the remaining 150, back to the 0 held before the
// order.
func TestReportFillTerminalReleasesDropCopyReserveOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src, _ := reportFillChain(t)
	requireHeld := func(stage, want string) {
		t.Helper()
		if got := deskBalance(t, service, "USD").Held; got != want {
			t.Fatalf("desk-1 USD held %s = %q, want %s", stage, got, want)
		}
	}

	order := submitDropCopy(t, src, "buy", "2", "150")
	requireHeld("after the drop-copy buy", "300")
	reportFill(t, src, map[string]any{
		"order": order, "status": "partially_filled", "quantity": "1", "price": "150", "leavesQuantity": "1",
	})
	requireHeld("after the partial fill", "150")
	cancelled := reportFill(t, src, map[string]any{"order": order, "status": "cancelled"})
	requireUnattested(t, cancelled)
	if usd := reportOutcome(t, cancelled, "USD"); usd.HeldDelta != "-150" || usd.HeldResult != "0" {
		t.Fatalf("cancellation USD held delta %q result %q, want -150 and 0", usd.HeldDelta, usd.HeldResult)
	}
	requireHeld("after the cancellation", "0")
	if status := readReportedState(t, service, order).order.Order.Status; status != domain.OrderStatusCancelled {
		t.Fatalf("order status = %q, want cancelled", status)
	}
}

// TestReportFillRepeatedIDOnControlPlane covers a repeat of a report under its
// id through report_fill: it is refused with conflict before the engine,
// leaves the order, its trades and desk-1's balances as they were, and the
// engine still holds only the remainder the first fill left, which the
// cancellation releases.
func TestReportFillRepeatedIDOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src, _ := reportFillChain(t)
	order := submitDropCopy(t, src, "buy", "2", "150")
	fill := map[string]any{
		"order": order, "id": "fill-1", "status": "partially_filled", "quantity": "1", "price": "150",
		"leavesQuantity": "1",
	}
	if first := reportFill(t, src, fill); first.ID != "fill-1" {
		t.Fatalf("report id = %q, want fill-1", first.ID)
	}
	before := readReportedState(t, service, order)

	res := callTool[map[string]any](t, src, "report_fill", fill)
	requireToolError(t, res.IsError)
	if got, want := textContent(res.Content), `conflict: execution report "fill-1": already exists`; got != want {
		t.Fatalf("repeat refusal = %q, want %q", got, want)
	}
	if after := readReportedState(t, service, order); !reflect.DeepEqual(after, before) {
		t.Fatalf("state after the refused repeat:\n%+v\nwant:\n%+v", after, before)
	}
	cancelled := reportFill(t, src, map[string]any{"order": order, "status": "cancelled"})
	if usd := reportOutcome(t, cancelled, "USD"); usd.HeldDelta != "-150" || usd.HeldResult != "0" {
		t.Fatalf("cancellation USD held delta %q result %q, want -150 and 0", usd.HeldDelta, usd.HeldResult)
	}
}

// TestReportFillOrdinaryOrderOnControlPlane covers a fill on an ordinary order
// through report_fill under each eSign mode: the report is attested, signed
// with the active key while eSign is on and unsigned under the eSign-off a
// fresh realm starts in, and its attestation is the one recorded on the
// order's fill event, which is the attestation REST answers with.
func TestReportFillOrdinaryOrderOnControlPlane(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src, keyID := reportFillChain(t)
	// The pre-trade check reserves each buy's 50 USD from desk-1's 100.
	if _, err := service.ApplyAdjustment(ctx, "desk-1", domain.ExternalID(""),
		sampleAdjustmentRequest(), domain.MissingAccountReject); err != nil {
		t.Fatalf("deposit into desk-1: %v", err)
	}
	for _, tc := range []struct {
		name   string
		off    bool
		keyID  string
		signed bool
	}{
		{"esign-off", true, "", false},
		{"esign-on", false, keyID, true},
	} {
		if err := service.SetNoESign(ctx, tc.off); err != nil {
			t.Fatalf("%s: set eSign-off %v: %v", tc.name, tc.off, err)
		}
		submitted := callSubmitOrder(t, src, submitOrderInput{
			Account: "desk-1", BaseAsset: "AAPL", QuoteAsset: "USD", Side: "buy",
			AmountKind: "quantity", AmountValue: "1", Price: "50", Mode: "hold",
			MissingAccount: "reject",
		})
		if submitted.IsError {
			t.Fatalf("%s: submit_order: %s", tc.name, textContent(submitted.Content))
		}
		if submitted.StructuredContent.Verdict != "accept" {
			t.Fatalf("%s: submit verdict = %q, want accept", tc.name, submitted.StructuredContent.Verdict)
		}
		order := submitted.StructuredContent.OrderExternalID

		res := reportFill(t, src, map[string]any{
			"order": order, "status": "filled", "quantity": "1", "price": "50", "leavesQuantity": "0",
		})
		if res.Signed != tc.signed || res.SignedAttestation == "" || res.AttestationKeyID != tc.keyID {
			t.Fatalf("%s: report attestation = %q key %q signed %v, want one under key %q signed %v",
				tc.name, res.SignedAttestation, res.AttestationKeyID, res.Signed, tc.keyID, tc.signed)
		}
		detail, err := service.GetOrder(ctx, order)
		if err != nil {
			t.Fatalf("%s: read order: %v", tc.name, err)
		}
		var recorded []*domain.EventAttestation
		for _, event := range detail.Events {
			if event.Type == domain.OrderEventFill {
				recorded = append(recorded, event.Attestation)
			}
		}
		if len(recorded) != 1 || recorded[0] == nil || recorded[0].SignedEnvelope != res.SignedAttestation ||
			recorded[0].KeyID != tc.keyID {
			t.Fatalf("%s: fill event attestations = %+v, want the reported one", tc.name, recorded)
		}
		if len(detail.Trades) != 1 || detail.Order.Status != domain.OrderStatusFilled {
			t.Fatalf("%s: order %s status %q trades %+v, want filled with one trade",
				tc.name, order, detail.Order.Status, detail.Trades)
		}
	}
}

// TestReportFillIncompleteCommissionOnControlPlane covers a report whose
// commission lacks a member through report_fill: the core refuses it as
// invalid input, an all-empty commission included, and nothing is recorded.
func TestReportFillIncompleteCommissionOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src, _ := reportFillChain(t)
	order := submitDropCopy(t, src, "buy", "1", "150")
	before := readReportedState(t, service, order)
	for _, tc := range []struct {
		name       string
		commission map[string]any
		want       string
	}{
		{"amount-only", map[string]any{"amount": "0.5"},
			"validation: commission amount and currency must be provided together"},
		{"all-empty", map[string]any{"amount": "", "currency": ""},
			"validation: commission amount and currency are required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := callTool[map[string]any](t, src, "report_fill", map[string]any{
				"order": order, "status": "filled", "quantity": "1", "price": "150", "leavesQuantity": "0",
				"commission": tc.commission,
			})
			requireToolError(t, res.IsError)
			if got := textContent(res.Content); got != tc.want {
				t.Fatalf("refusal = %q, want %q", got, tc.want)
			}
			if after := readReportedState(t, service, order); !reflect.DeepEqual(after, before) {
				t.Fatalf("state after the refusal:\n%+v\nwant:\n%+v", after, before)
			}
		})
	}
}

// TestReportFillCommandSwitchOnControlPlane covers the operator's per-command
// switch over report_fill: switched off, the call answers the panel notice and
// records nothing; switched on, the same report is recorded.
func TestReportFillCommandSwitchOnControlPlane(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src, _ := reportFillChain(t)
	order := submitDropCopy(t, src, "buy", "1", "150")
	fill := map[string]any{
		"order": order, "status": "filled", "quantity": "1", "price": "150", "leavesQuantity": "0",
	}
	if err := service.SetMcpAccess(ctx, "report_fill", false); err != nil {
		t.Fatalf("switch off report_fill: %v", err)
	}
	before := readReportedState(t, service, order)
	res := callTool[map[string]any](t, src, "report_fill", fill)
	requireNotToolError(t, res.IsError)
	notice := fmt.Sprintf("Command %q is disabled in the Pit Officer panel", "report_fill")
	if text := textContent(res.Content); !strings.Contains(text, notice) {
		t.Fatalf("switched-off report_fill = %q, want the disabled notice", text)
	}
	if after := readReportedState(t, service, order); !reflect.DeepEqual(after, before) {
		t.Fatalf("state after the switched-off call:\n%+v\nwant:\n%+v", after, before)
	}

	if err := service.SetMcpAccess(ctx, "report_fill", true); err != nil {
		t.Fatalf("switch on report_fill: %v", err)
	}
	reportFill(t, src, fill)
	if status := readReportedState(t, service, order).order.Order.Status; status != domain.OrderStatusFilled {
		t.Fatalf("order status after the switched-on call = %q, want filled", status)
	}
}
