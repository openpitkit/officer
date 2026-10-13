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
	"context"
	"errors"
	"fmt"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	fwsigning "go.openpit.dev/officer/framework/signing"
)

// executionReportsSource records the execution reports report_fill applies and
// answers them with result, att and the recorder's err.
type executionReportsSource struct {
	fakeSource
	sourceRecorder
	result engine.ExecutionReportResult
	att    frameworkmcp.Attestation
}

func (s *executionReportsSource) ApplyExecutionReport(_ context.Context, in domain.ExecutionReportInput) (engine.ExecutionReportResult, frameworkmcp.Attestation, error) {
	return s.result, s.att, s.record("report", in)
}

// reportsAttestedBy is a fixture answering every report with a recorded result
// that carries an account block and both legs of a fill, attested by att, as
// the shared tool drivers take it.
func reportsAttestedBy(att frameworkmcp.Attestation) recordedFixture {
	return func() (frameworkmcp.Source, *fakeSource, *sourceRecorder) {
		src := &executionReportsSource{
			result: engine.ExecutionReportResult{
				ReportID: "report-1",
				Blocks: []domain.AccountBlock{{
					Account: "desk-1", Policy: "spot_funds_pnl_bounds", Code: "pnl_bound_breached",
					Reason: "realized P&L below the lower bound", Details: "-12 < -10",
				}},
				Outcomes: []engine.BalanceOutcome{
					{Asset: "AAPL", Outcome: domain.AdjustmentOutcomeAccepted{
						BalanceDelta: "-1", BalanceResult: "2", HeldDelta: "-3", HeldResult: "4",
						IncomingDelta: "5", IncomingResult: "6", RealizedPnlDelta: "-12",
						RealizedPnlResult: "-13", AverageEntryPrice: "150",
					}},
					{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{
						BalanceDelta: "160", BalanceResult: "170", HeldDelta: "0", HeldResult: "7",
						IncomingDelta: "8", IncomingResult: "9", RealizedPnlDelta: "10",
						RealizedPnlResult: "11", RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
					}},
				},
			},
			att: att,
		}
		return src, &src.fakeSource, &src.sourceRecorder
	}
}

// signedReportAttestation is the attestation of a report on an ordinary order.
var signedReportAttestation = frameworkmcp.Attestation{
	Token: "signed-envelope", KeyID: "key-1", Signed: true,
}

// reportResultBody is the REST result body of every reportsAttestedBy report.
const reportResultBody = `{"blocks":[{"account":"desk-1","policy":"spot_funds_pnl_bounds",` +
	`"code":"pnl_bound_breached","reason":"realized P&L below the lower bound","details":"-12 < -10"}],` +
	`"outcomes":[{"asset":"AAPL","balanceDelta":"-1","balanceResult":"2","heldDelta":"-3",` +
	`"heldResult":"4","incomingDelta":"5","incomingResult":"6","realizedPnlDelta":"-12",` +
	`"realizedPnlResult":"-13","averageEntryPrice":"150"},{"asset":"USD","balanceDelta":"160",` +
	`"balanceResult":"170","heldDelta":"0","heldResult":"7","incomingDelta":"8","incomingResult":"9",` +
	`"realizedPnlDelta":"10","realizedPnlResult":"11","realizedPnlHaltReason":"missing_fx"}]}`

// reportFillInputs holds a valid report_fill input.
var reportFillInputs = map[string]map[string]any{
	"report_fill": {"order": "ord-1", "status": "filled", "quantity": "1", "price": "160", "leavesQuantity": "0"},
}

// TestReportFillSuccess covers report_fill's success: every argument reaches
// the Source verbatim, spaces included, a present commission as given even
// when empty, and the result is the REST body with the attestation the core
// returned: signed for an ordinary order, unsigned under eSign-off, and none
// for a drop-copy order.
func TestReportFillSuccess(t *testing.T) {
	t.Parallel()
	runToolSuccess(t, reportsAttestedBy(signedReportAttestation), []toolSuccessCase{
		{"report_fill", map[string]any{
			"order": " ord-1 ", "id": " rep-1 ", "status": " filled ", "quantity": " 1 ",
			"price": " 160 ", "leavesQuantity": " 0 ", "lockPrice": " 150 ",
			"commission": map[string]any{"amount": " 0.5 ", "currency": " EUR "},
		}, []string{"report"}, []any{domain.ExecutionReportInput{
			ExternalID: " rep-1 ", FillQuantity: " 1 ", FillPrice: " 160 ", LeavesQuantity: " 0 ",
			LockPrice: " 150 ", Commission: &domain.Commission{Amount: " 0.5 ", Currency: " EUR "},
			Order: " ord-1 ", OrderStatus: " filled ",
		}}, "execution report report-1 recorded for order  ord-1 :  filled ",
			`{"id":"report-1","result":` + reportResultBody + `,"signedAttestation":"signed-envelope",` +
				`"attestationKeyId":"key-1","signed":true}`},
	})
	runToolSuccess(t, reportsAttestedBy(frameworkmcp.Attestation{Token: "unsigned-envelope"}), []toolSuccessCase{
		{"report_fill", map[string]any{
			"order": "ord-1", "status": "cancelled", "commission": map[string]any{"amount": "", "currency": ""},
		}, []string{"report"}, []any{domain.ExecutionReportInput{
			Commission: &domain.Commission{}, Order: "ord-1", OrderStatus: "cancelled",
		}}, "execution report report-1 recorded for order ord-1: cancelled",
			`{"id":"report-1","result":` + reportResultBody + `,"signedAttestation":"unsigned-envelope",` +
				`"signed":false}`},
	})
	runToolSuccess(t, reportsAttestedBy(frameworkmcp.Attestation{}), []toolSuccessCase{
		{"report_fill", map[string]any{"order": "ord-1", "status": "committed"},
			[]string{"report"}, []any{domain.ExecutionReportInput{Order: "ord-1", OrderStatus: "committed"}},
			"execution report report-1 recorded for order ord-1: committed",
			`{"id":"report-1","result":` + reportResultBody + `,"signed":false}`},
	})
}

// TestReportFillOrderRequired covers an omitted order: the core's id parser
// refuses it as invalid input before the Source is called.
func TestReportFillOrderRequired(t *testing.T) {
	t.Parallel()
	src, _, recorder := reportsAttestedBy(signedReportAttestation)()
	res := callTool[map[string]any](t, src, "report_fill", map[string]any{"status": "cancelled"})
	requireToolError(t, res.IsError)
	if got, want := textContent(res.Content), "validation: external id must not be empty: invalid"; got != want {
		t.Fatalf("refusal = %q, want %q", got, want)
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("refused report reached the Source: %v", recorder.calls)
	}
}

// TestReportFillSourceRefusal covers each refusal the core gives an execution
// report: it reaches the agent under the code REST's shared mapping answers
// with the Source's message, and a signing failure of an ordinary order's
// report under the code REST answers for it.
func TestReportFillSourceRefusal(t *testing.T) {
	t.Parallel()
	runToolRefusals(t, reportsAttestedBy(signedReportAttestation), reportFillInputs, []toolRefusal{
		{"validation", domain.NewValidationError("/commission", "paired_fields",
			"commission amount and currency must be provided together"),
			"validation: commission amount and currency must be provided together"},
		{"not_found", fmt.Errorf("get order: order %q: %w", "ord-1", domain.ErrNotFound),
			`not_found: get order: order "ord-1": not found`},
		{"conflict", fmt.Errorf("execution report %q: %w", "rep-1", domain.ErrAlreadyExists),
			`conflict: execution report "rep-1": already exists`},
		{"terminal_order", domain.ErrTerminalOrder, "terminal_order: order in terminal status"},
		{"venue_order_managed", fmt.Errorf("order %s: %w", "ord-1", domain.ErrVenueOrderManaged),
			"venue_order_managed: the order is managed by its trading venue"},
		{"signer_not_configured", fwsigning.ErrNotConfigured,
			"not_implemented: signing: service not configured: not implemented"},
		{"no_active_key", fmt.Errorf("signing: no active key: %w", domain.ErrNotFound),
			"not_found: signing: no active key: not found"},
		{"attestation_missing", errors.New("backend: execution_report attestation missing"),
			"internal: internal error"},
	})
}

// TestReportFillDisabled covers report_fill with its command disabled: the
// agent receives the panel notice and the Source is not called.
func TestReportFillDisabled(t *testing.T) {
	t.Parallel()
	runToolsDisabled(t, reportsAttestedBy(signedReportAttestation), reportFillInputs)
}
