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
	"bytes"
	"encoding/csv"
	"errors"
	"slices"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
)

func mdID(label string) domain.ExternalID {
	return domain.ExternalID(label)
}

func containsProviderType(providers []backend.MarketDataProvider, want string) bool {
	for _, provider := range providers {
		if provider.Type == want {
			return true
		}
	}
	return false
}

// TestService_OrderFlowsFetchAtMostOnce locks the fetch budget: every
// order-resolving flow must fetch the stored order no more than the
// attestation-aware budget below. Signing is additive: it re-reads the order to
// bind its verdict/resolution event, so submit fetches once (attest read-back)
// and confirm/cancel fetch twice (the token-binding read plus the attest
// read-back).
func TestService_OrderFlowsFetchAtMostOnce(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// run drives one operation. mustWorkflow and any other multi-step setup happen
		// before the measured operation; the case calls reset() to zero the counters
		// just before the operation under test so the assertion covers only it. It
		// returns the number of order fetches expected for the measured operation.
		run func(t *testing.T, svc *backend.Service, fn *fakeNode, reset func()) int
	}{
		{
			name: "submit workflow",
			run: func(t *testing.T, svc *backend.Service, _ *fakeNode, reset func()) int {
				t.Helper()
				reset()
				if _, err := svc.SubmitOrderToken(
					systemCtx(), sampleOrder(), backend.SubmitModeHold, domain.MissingAccountCreate,
				); err != nil {
					t.Fatalf("SubmitOrderToken workflow: %v", err)
				}
				// One fetch: the attest read-back that binds the verdict event.
				return 1
			},
		},
		{
			name: "submit immediate",
			run: func(t *testing.T, svc *backend.Service, _ *fakeNode, reset func()) int {
				t.Helper()
				reset()
				if _, err := svc.SubmitOrderToken(
					systemCtx(), sampleOrder(), backend.SubmitModeImmediate, domain.MissingAccountCreate,
				); err != nil {
					t.Fatalf("SubmitOrderToken immediate: %v", err)
				}
				// One fetch: the attest read-back that binds the verdict event.
				return 1
			},
		},
		{
			name: "confirm accepted",
			run: func(t *testing.T, svc *backend.Service, _ *fakeNode, reset func()) int {
				t.Helper()
				tok := mustWorkflow(t, svc)
				reset()
				if _, _, err := svc.ConfirmExecution(
					systemCtx(), tok.OrderExternalID, tok.Token,
				); err != nil {
					t.Fatalf("ConfirmExecution: %v", err)
				}
				// Two fetches: the token-binding read plus the attestation read-back
				// that binds the confirmed event.
				return 2
			},
		},
		{
			name: "confirm history idempotent",
			run: func(t *testing.T, svc *backend.Service, fn *fakeNode, reset func()) int {
				t.Helper()
				tok := mustWorkflow(t, svc)
				if _, _, err := svc.ConfirmExecution(
					systemCtx(), tok.OrderExternalID, tok.Token,
				); err != nil {
					t.Fatalf("first confirm: %v", err)
				}
				reset()
				order, att, err := svc.ConfirmExecution(
					systemCtx(), tok.OrderExternalID, tok.Token,
				)
				if err != nil {
					t.Fatalf("idempotent confirm: %v", err)
				}
				if order.Status != domain.OrderStatusCommitted || att.Token == "" {
					t.Fatalf("idempotent confirm = %+v att=%+v, want unchanged status with attestation",
						order, att)
				}
				// Two fetches: the token-binding read plus the confirmed-event
				// attestation read-back after the node returns without a new event.
				return 2
			},
		},
		{
			name: "confirm conflict",
			run: func(t *testing.T, svc *backend.Service, fn *fakeNode, reset func()) int {
				t.Helper()
				tok := mustWorkflow(t, svc)
				if _, _, err := svc.CancelOrder(
					systemCtx(), tok.OrderExternalID, tok.Token, "10", "operator",
				); err != nil {
					t.Fatalf("cancel setup: %v", err)
				}
				reset()
				if _, _, err := svc.ConfirmExecution(
					systemCtx(), tok.OrderExternalID, tok.Token,
				); !errors.Is(err, domain.ErrExecutionReportRequired) {
					t.Fatalf("confirm after cancel = %v, want explicit report", err)
				}
				// One fetch: the token-binding read happens before the node-level
				// terminal guard rejects without attestation.
				return 1
			},
		},
		{
			name: "cancel accepted",
			run: func(t *testing.T, svc *backend.Service, _ *fakeNode, reset func()) int {
				t.Helper()
				tok := mustWorkflow(t, svc)
				reset()
				if _, _, err := svc.CancelOrder(
					systemCtx(), tok.OrderExternalID, tok.Token, "10", "stale price",
				); err != nil {
					t.Fatalf("CancelOrder: %v", err)
				}
				// Two fetches: the token-binding read plus the attest read-back that
				// binds the cancelled event.
				return 2
			},
		},
		{
			name: "cancel conflict",
			run: func(t *testing.T, svc *backend.Service, fn *fakeNode, reset func()) int {
				t.Helper()
				tok := mustWorkflow(t, svc)
				if _, _, err := svc.CancelOrder(
					systemCtx(), tok.OrderExternalID, tok.Token, "10", "setup",
				); err != nil {
					t.Fatalf("cancel setup: %v", err)
				}
				reset()
				if _, _, err := svc.CancelOrder(
					systemCtx(), tok.OrderExternalID, tok.Token, "10", "too late",
				); !errors.Is(err, domain.ErrExecutionReportRequired) {
					t.Fatalf("second cancel = %v, want explicit report", err)
				}
				// One fetch: the token-binding read happens before the node-level
				// conflict rejects without attestation.
				return 1
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fn := &fakeNode{orders: make(map[domain.ExternalID]domain.Order)}
			svc := newOfficerService(t, fn, nil, &fakeSigner{})
			reset := func() {
				fn.getOrderCount.Store(0)
			}

			wantFetches := tc.run(t, svc, fn, reset)

			if got := fn.getOrderCount.Load(); got > int64(wantFetches) {
				t.Fatalf("GetOrder calls = %d, want at most %d", got, wantFetches)
			}
		})
	}
}

func TestService_BusinessCSVExportAuditsAndDoesNotReuseBackupAction(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	fn.accounts = []domain.Account{{
		Code:      "acc-1",
		GroupCode: "desk-a",
	}}

	file, err := svc.ExportBusinessCSV(systemCtx(),
		backend.BusinessCSVExportRequest{
			Entity:    businesscsv.EntityAccounts,
			Delimiter: businesscsv.DelimiterPipe,
			Filter: businesscsv.ExportFilter{
				GroupCode:    "desk-a",
				GroupCodeSet: true,
			},
		})
	if err != nil {
		t.Fatalf("ExportBusinessCSV: %v", err)
	}
	if !strings.Contains(string(file.Body), "acc-1||desk-a") {
		t.Fatalf("body = %q", file.Body)
	}
	if len(fn.auditCalls) != 1 ||
		fn.auditCalls[0].Action != domain.AuditActionExportBusinessCSV ||
		strings.Contains(string(fn.auditCalls[0].Action), "backup") {
		t.Fatalf("auditCalls = %+v", fn.auditCalls)
	}
}

func TestService_BusinessCSVExportAccountGroupFilterPresence(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	fn.accounts = []domain.Account{
		{Code: "acc-none"},
		{Code: "acc-desk-a", GroupCode: "desk-a"},
		{Code: "acc-desk-b", GroupCode: "desk-b"},
	}

	cases := []struct {
		name       string
		filter     businesscsv.ExportFilter
		want       []string
		wantDetail string
	}{
		{
			name: "omitted group exports all",
			want: []string{"acc-none", "acc-desk-a", "acc-desk-b"},
		},
		{
			name:       "explicit empty group exports no-group bucket",
			filter:     businesscsv.ExportFilter{GroupCodeSet: true},
			want:       []string{"acc-none"},
			wantDetail: "filters=group=<none>",
		},
		{
			name: "explicit group exports matching group",
			filter: businesscsv.ExportFilter{
				GroupCode: "desk-a", GroupCodeSet: true,
			},
			want:       []string{"acc-desk-a"},
			wantDetail: "filters=group=desk-a",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fn.auditCalls = nil
			file, err := svc.ExportBusinessCSV(systemCtx(),
				backend.BusinessCSVExportRequest{
					Entity:    businesscsv.EntityAccounts,
					Delimiter: businesscsv.DelimiterComma,
					Filter:    tc.filter,
				})
			if err != nil {
				t.Fatalf("ExportBusinessCSV: %v", err)
			}
			body := string(file.Body)
			for _, id := range tc.want {
				if !strings.Contains(body, id) {
					t.Fatalf("body %q missing %s", body, id)
				}
			}
			for _, account := range fn.accounts {
				if slices.Contains(tc.want, string(account.Code)) {
					continue
				}
				if strings.Contains(body, string(account.Code)) {
					t.Fatalf("body %q unexpectedly contains %s", body, account.Code)
				}
			}
			if tc.wantDetail != "" && (len(fn.auditCalls) != 1 ||
				!strings.Contains(fn.auditCalls[0].Detail, tc.wantDetail)) {
				t.Fatalf("auditCalls = %+v, want detail %q", fn.auditCalls, tc.wantDetail)
			}
		})
	}
}

func TestService_BusinessCSVOrderExportKeepsUnreadableLockRow(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	fn.allOrders = []domain.Order{{
		ExternalID:  "order-corrupt-lock",
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Principal:   "operator",
		Source:      domain.SourcePanel,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       "152",
		Leaves:      "1",
		Status:      domain.OrderStatusAccepted,
		Lock:        []byte{0x01, 0x02, 0x03},
	}}

	file, err := svc.ExportBusinessCSV(systemCtx(),
		backend.BusinessCSVExportRequest{
			Entity:    businesscsv.EntityOrders,
			Delimiter: businesscsv.DelimiterComma,
		})
	if err != nil {
		t.Fatalf("ExportBusinessCSV(orders): %v", err)
	}
	rows, err := csv.NewReader(bytes.NewReader(file.Body)).ReadAll()
	if err != nil {
		t.Fatalf("read orders CSV: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("orders CSV rows = %d, want header and corrupt-lock order", len(rows))
	}
	column := make(map[string]int, len(rows[0]))
	for index, name := range rows[0] {
		column[name] = index
	}
	if rows[1][column["id"]] != "order-corrupt-lock" ||
		rows[1][column["lock_price"]] != "" {
		t.Fatalf("corrupt-lock row = %v, want retained with empty lock_price", rows[1])
	}
}
