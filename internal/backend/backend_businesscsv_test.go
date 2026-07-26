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
	"fmt"
	"slices"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/store/sqlite"
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

// TestService_OrderFlowsRouteOnceFetchAtMostOnce locks the route-once invariant:
// every order-resolving flow must route exactly once per operation and fetch the
// stored order no more than the attestation-aware budget below. It instruments
// the fake router/node call counters so a regression to double routing fails
// here. Signing is additive: it re-reads the order to bind its verdict/resolution
// event, so submit fetches once (attest read-back) and confirm/cancel fetch twice
// (the token-binding read plus the attest read-back).
func TestService_OrderFlowsRouteOnceFetchAtMostOnce(t *testing.T) {
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
					context.Background(), sampleOrder(), backend.SubmitModeHold,
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
					context.Background(), sampleOrder(), backend.SubmitModeImmediate,
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
					context.Background(), tok.OrderExternalID, tok.Token,
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
					context.Background(), tok.OrderExternalID, tok.Token,
				); err != nil {
					t.Fatalf("first confirm: %v", err)
				}
				reset()
				order, att, err := svc.ConfirmExecution(
					context.Background(), tok.OrderExternalID, tok.Token,
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
					context.Background(), tok.OrderExternalID, tok.Token, "operator",
				); err != nil {
					t.Fatalf("cancel setup: %v", err)
				}
				reset()
				if _, _, err := svc.ConfirmExecution(
					context.Background(), tok.OrderExternalID, tok.Token,
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
					context.Background(), tok.OrderExternalID, tok.Token, "stale price",
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
					context.Background(), tok.OrderExternalID, tok.Token, "setup",
				); err != nil {
					t.Fatalf("cancel setup: %v", err)
				}
				reset()
				if _, _, err := svc.CancelOrder(
					context.Background(), tok.OrderExternalID, tok.Token, "too late",
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
			router := &fakeRouter{node: &fakeNode{orders: make(map[domain.ExternalID]domain.Order)}}
			svc := backend.New(router, nil, &fakeSigner{})
			fn := router.node
			reset := func() {
				router.routeCount.Store(0)
				fn.getOrderCount.Store(0)
			}

			wantFetches := tc.run(t, svc, fn, reset)

			if got := router.routeCount.Load(); got != 1 {
				t.Fatalf("Route calls = %d, want exactly 1", got)
			}
			if got := fn.getOrderCount.Load(); got > int64(wantFetches) {
				t.Fatalf("GetOrder calls = %d, want at most %d", got, wantFetches)
			}
		})
	}
}

func TestService_BusinessCSVImportStopKeepsPreviousRowsAndAudits(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.accounts = []domain.Account{{
		Code: "acc-existing",
	}}

	body := []byte(
		"code,title,group_code,notes,blocked,block_reason\n" +
			"acc-new,,desk-a,new note,false,\n" +
			"acc-existing,,desk-a,old note,false,\n" +
			"acc-after,,desk-a,after,false,\n",
	)
	result, err := svc.ImportBusinessCSV(context.Background(),
		backend.BusinessCSVImportRequest{
			Entity:         businesscsv.EntityAccounts,
			Delimiter:      businesscsv.DelimiterComma,
			Filename:       "accounts.csv",
			Payload:        body,
			ConflictPolicy: businesscsv.ConflictStop,
		})
	if err != nil {
		t.Fatalf("ImportBusinessCSV: %v", err)
	}
	if result.Counts.Applied != 1 || result.Counts.Conflicts != 1 ||
		!result.Counts.Stopped || len(fn.createCalls) != 1 ||
		fn.createCalls[0].Code != "acc-new" {
		t.Fatalf("result=%+v createCalls=%+v", result.Counts, fn.createCalls)
	}
	if len(fn.auditCalls) != 1 ||
		fn.auditCalls[0].Action != domain.AuditActionImportBusinessCSV ||
		!strings.Contains(fn.auditCalls[0].Detail, "policy=stop") {
		t.Fatalf("auditCalls = %+v", fn.auditCalls)
	}
}

func TestService_BusinessCSVPartialImportFailureAuditsFileAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, eng := newBusinessCSVRealService(t)

	body := []byte(
		"code,title,group_code,notes,blocked,block_reason\n" +
			"acc-good,,,first note,false,\n" +
			",,,bad note,false,\n",
	)
	_, err := svc.ImportBusinessCSV(ctx, backend.BusinessCSVImportRequest{
		Entity:         businesscsv.EntityAccounts,
		Delimiter:      businesscsv.DelimiterComma,
		Filename:       "accounts.csv",
		Payload:        body,
		ConflictPolicy: businesscsv.ConflictReplace,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ImportBusinessCSV error = %v, want invalid", err)
	}

	if _, ok, err := st.GetAccount(ctx, "acc-good"); err != nil || ok {
		t.Fatalf("GetAccount acc-good after failed import: %v ok=%v, want absent", err, ok)
	}

	operationAudits, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{
			domain.AuditActionCreateAccount,
			domain.AuditActionSetNotes,
			domain.AuditActionSetGroup,
			domain.AuditActionBlock,
			domain.AuditActionUnblock,
		},
		Account: "acc-good",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered operations: %v", err)
	}
	if len(operationAudits) != 0 {
		t.Fatalf("operation audits = %+v, want none after rollback", operationAudits)
	}
	if len(eng.adjustmentCalls) != 0 {
		t.Fatalf("engine adjustment calls = %d, want none", len(eng.adjustmentCalls))
	}

	importAudits, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionImportBusinessCSV},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered import: %v", err)
	}
	if len(importAudits) != 1 ||
		!strings.Contains(importAudits[0].Detail, "entity=accounts") ||
		!strings.Contains(importAudits[0].Detail, "delimiter=comma") ||
		!strings.Contains(importAudits[0].Detail, "file=accounts.csv") ||
		!strings.Contains(importAudits[0].Detail, "policy=replace") ||
		!strings.Contains(importAudits[0].Detail, "rows=2") ||
		!strings.Contains(importAudits[0].Detail, "applied=0") ||
		!strings.Contains(importAudits[0].Detail, "error=") {
		t.Fatalf("import audit rows = %+v", importAudits)
	}
}

func TestService_BusinessCSVImportRejectsInvalidTitle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newBusinessCSVRealService(t)
	longTitle := strings.Repeat("x", 257)

	body := []byte(
		"code,title,group_code,notes,blocked,block_reason\n" +
			"acc-bad," + longTitle + ",,,false,\n",
	)
	_, err := svc.ImportBusinessCSV(ctx, backend.BusinessCSVImportRequest{
		Entity:         businesscsv.EntityAccounts,
		Delimiter:      businesscsv.DelimiterComma,
		Filename:       "accounts.csv",
		Payload:        body,
		ConflictPolicy: businesscsv.ConflictReplace,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ImportBusinessCSV error = %v, want invalid", err)
	}
	if _, ok, err := st.GetAccount(ctx, "acc-bad"); err != nil || ok {
		t.Fatalf("GetAccount acc-bad after failed import: %v ok=%v, want absent", err, ok)
	}
}

func TestService_BusinessCSVPositionsRoundTripPreservesRealizedPnl(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sourceSvc, sourceStore, _ := newBusinessCSVRealService(t)
	if err := sourceStore.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset source: %v", err)
	}
	if _, err := sourceStore.CreateAccount(ctx, domain.Account{
		Code: "acc-1",
	}); err != nil {
		t.Fatalf("CreateAccount source: %v", err)
	}
	want := domain.Balance{
		Account:           "acc-1",
		Asset:             "USD",
		Available:         "100.25",
		Held:              "10.5",
		Incoming:          "2.75",
		RealizedPnl:       "7.125",
		AverageEntryPrice: "99.5",
	}
	if err := sourceStore.UpsertBalance(ctx, want); err != nil {
		t.Fatalf("UpsertBalance source: %v", err)
	}

	file, err := sourceSvc.ExportBusinessCSV(ctx, backend.BusinessCSVExportRequest{
		Entity:    businesscsv.EntityPositions,
		Delimiter: businesscsv.DelimiterSemicolon,
	})
	if err != nil {
		t.Fatalf("ExportBusinessCSV: %v", err)
	}
	if !strings.Contains(string(file.Body), "7.125") {
		t.Fatalf("exported body %q does not contain realized_pnl", file.Body)
	}

	targetSvc, targetStore, targetEngine := newBusinessCSVRealService(t)
	// Positions reference an existing account and asset by code; the relational
	// store enforces those foreign keys, so seed the dictionary rows before import.
	if err := targetStore.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset target: %v", err)
	}
	if _, err := targetStore.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount target: %v", err)
	}
	result, err := targetSvc.ImportBusinessCSV(ctx, backend.BusinessCSVImportRequest{
		Entity:         businesscsv.EntityPositions,
		Delimiter:      businesscsv.DelimiterSemicolon,
		Filename:       file.Name,
		Payload:        file.Body,
		ConflictPolicy: businesscsv.ConflictReplace,
	})
	if err != nil {
		t.Fatalf("ImportBusinessCSV: %v", err)
	}
	if result.Counts.Applied != 1 || result.Counts.Conflicts != 0 {
		t.Fatalf("import counts = %+v", result.Counts)
	}
	if len(targetEngine.adjustmentCalls) != 1 {
		t.Fatalf("engine adjustment calls = %d, want 1", len(targetEngine.adjustmentCalls))
	}
	got, ok, err := targetStore.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance target: %v ok=%v", err, ok)
	}
	if got.Available != want.Available || got.Held != want.Held ||
		got.Incoming != want.Incoming || got.RealizedPnl != want.RealizedPnl ||
		got.AverageEntryPrice != want.AverageEntryPrice {
		t.Fatalf("target balance = %+v, want %+v", got, want)
	}
	audits, err := targetStore.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionAdjustment},
		Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered adjustment: %v", err)
	}
	if len(audits) != 1 ||
		!strings.Contains(audits[0].Detail, "import position snapshot account acc-1 asset=USD") ||
		!strings.Contains(audits[0].Detail, "realized_pnl=7.125") {
		t.Fatalf("adjustment audit rows = %+v", audits)
	}
	importAudits, err := targetStore.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionImportBusinessCSV},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered import: %v", err)
	}
	if len(importAudits) != 1 || !strings.Contains(importAudits[0].Detail, "applied=1") {
		t.Fatalf("import audit rows = %+v", importAudits)
	}
}

func TestService_BusinessCSVGroupsRoundTripPreservesCurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sourceSvc, sourceStore, _ := newBusinessCSVRealService(t)
	if err := sourceStore.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset source USD: %v", err)
	}
	if _, err := sourceStore.CreateGroup(ctx, domain.AccountGroup{
		Code: "desk-us", Title: "US Desk", Currency: "USD",
	}); err != nil {
		t.Fatalf("CreateGroup source: %v", err)
	}

	file, err := sourceSvc.ExportBusinessCSV(ctx, backend.BusinessCSVExportRequest{
		Entity:    businesscsv.EntityAccountGroups,
		Delimiter: businesscsv.DelimiterComma,
	})
	if err != nil {
		t.Fatalf("ExportBusinessCSV: %v", err)
	}
	body := string(file.Body)
	if !strings.Contains(body, "currency") || !strings.Contains(body, "USD") {
		t.Fatalf("exported group CSV lost currency: %q", body)
	}

	targetSvc, targetStore, _ := newBusinessCSVRealService(t)
	if err := targetStore.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset target USD: %v", err)
	}
	result, err := targetSvc.ImportBusinessCSV(ctx, backend.BusinessCSVImportRequest{
		Entity:         businesscsv.EntityAccountGroups,
		Delimiter:      businesscsv.DelimiterComma,
		Filename:       file.Name,
		Payload:        file.Body,
		ConflictPolicy: businesscsv.ConflictReplace,
	})
	if err != nil {
		t.Fatalf("ImportBusinessCSV: %v", err)
	}
	if result.Counts.Applied != 1 || result.Counts.Conflicts != 0 {
		t.Fatalf("import counts = %+v", result.Counts)
	}
	group, ok, err := targetStore.GetGroup(ctx, "desk-us")
	if err != nil || !ok {
		t.Fatalf("GetGroup target: ok=%v err=%v", ok, err)
	}
	if group.Currency != "USD" {
		t.Fatalf("target group currency = %q, want USD", group.Currency)
	}
}

func TestService_BusinessCSVAccountsRoundTripPreservesCurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sourceSvc, sourceStore, _ := newBusinessCSVRealService(t)
	if err := sourceStore.CreateAsset(ctx, domain.Asset{Code: "JPY"}); err != nil {
		t.Fatalf("CreateAsset source JPY: %v", err)
	}
	if _, err := sourceStore.CreateGroup(ctx, domain.AccountGroup{Code: "desk-jp"}); err != nil {
		t.Fatalf("CreateGroup source: %v", err)
	}
	if _, err := sourceStore.CreateAccount(ctx, domain.Account{
		Code: "acc-jpy", GroupCode: "desk-jp", Currency: "JPY",
	}); err != nil {
		t.Fatalf("CreateAccount source: %v", err)
	}

	file, err := sourceSvc.ExportBusinessCSV(ctx, backend.BusinessCSVExportRequest{
		Entity:    businesscsv.EntityAccounts,
		Delimiter: businesscsv.DelimiterComma,
	})
	if err != nil {
		t.Fatalf("ExportBusinessCSV: %v", err)
	}
	body := string(file.Body)
	if !strings.Contains(body, "currency") || !strings.Contains(body, "JPY") {
		t.Fatalf("exported account CSV lost currency: %q", body)
	}

	targetSvc, targetStore, _ := newBusinessCSVRealService(t)
	if err := targetStore.CreateAsset(ctx, domain.Asset{Code: "JPY"}); err != nil {
		t.Fatalf("CreateAsset target JPY: %v", err)
	}
	if _, err := targetStore.CreateGroup(ctx, domain.AccountGroup{Code: "desk-jp"}); err != nil {
		t.Fatalf("CreateGroup target: %v", err)
	}
	result, err := targetSvc.ImportBusinessCSV(ctx, backend.BusinessCSVImportRequest{
		Entity:         businesscsv.EntityAccounts,
		Delimiter:      businesscsv.DelimiterComma,
		Filename:       file.Name,
		Payload:        file.Body,
		ConflictPolicy: businesscsv.ConflictReplace,
	})
	if err != nil {
		t.Fatalf("ImportBusinessCSV: %v", err)
	}
	if result.Counts.Applied != 1 || result.Counts.Conflicts != 0 {
		t.Fatalf("import counts = %+v", result.Counts)
	}
	account, ok, err := targetStore.GetAccount(ctx, "acc-jpy")
	if err != nil || !ok {
		t.Fatalf("GetAccount target: ok=%v err=%v", ok, err)
	}
	if account.Currency != "JPY" ||
		account.EffectiveCurrency != "JPY" ||
		account.CurrencyOrigin != domain.CurrencyOriginAccount {
		t.Fatalf("target account currency = %+v", account)
	}
}

func TestService_BusinessCSVPreviewRejectsMissingCurrencyAsset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, targetStore, _ := newBusinessCSVRealService(t)
	if _, err := targetStore.CreateGroup(ctx, domain.AccountGroup{Code: "desk-jp"}); err != nil {
		t.Fatalf("CreateGroup target: %v", err)
	}

	body := []byte(
		"code,title,group_code,currency,notes,blocked,block_reason\n" +
			"acc-jpy,,desk-jp,JPY,,false,\n",
	)
	_, err := svc.PreviewBusinessCSVImport(ctx, backend.BusinessCSVImportRequest{
		Entity:    businesscsv.EntityAccounts,
		Delimiter: businesscsv.DelimiterComma,
		Filename:  "accounts.csv",
		Payload:   body,
	})
	if !errors.Is(err, domain.ErrInvalid) ||
		!strings.Contains(err.Error(), "row 2 JPY") {
		t.Fatalf("PreviewBusinessCSVImport error = %v, want missing JPY invalid", err)
	}
}

func TestService_BusinessCSVImportMissingCurrencyAssetAuditsAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, targetStore, _ := newBusinessCSVRealService(t)

	body := []byte(
		"code,title,currency,notes,blocked,block_reason\n" +
			"desk-us,US Desk,USD,,false,\n",
	)
	_, err := svc.ImportBusinessCSV(ctx, backend.BusinessCSVImportRequest{
		Entity:         businesscsv.EntityAccountGroups,
		Delimiter:      businesscsv.DelimiterComma,
		Filename:       "account_groups.csv",
		Payload:        body,
		ConflictPolicy: businesscsv.ConflictReplace,
	})
	if !errors.Is(err, domain.ErrInvalid) ||
		!strings.Contains(err.Error(), "row 2 USD") {
		t.Fatalf("ImportBusinessCSV error = %v, want missing USD invalid", err)
	}
	if _, ok, err := targetStore.GetGroup(ctx, "desk-us"); err != nil || ok {
		t.Fatalf("GetGroup desk-us after failed import: %v ok=%v, want absent", err, ok)
	}
	audits, err := targetStore.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionImportBusinessCSV},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered import: %v", err)
	}
	if len(audits) != 1 ||
		!strings.Contains(audits[0].Detail, "entity=account_groups") ||
		!strings.Contains(audits[0].Detail, "rows=1") ||
		!strings.Contains(audits[0].Detail, "error=") {
		t.Fatalf("import audit rows = %+v", audits)
	}
}

func TestService_BusinessCSVExportAuditsAndDoesNotReuseBackupAction(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	fn.accounts = []domain.Account{{
		Code:      "acc-1",
		GroupCode: "desk-a",
	}}

	file, err := svc.ExportBusinessCSV(context.Background(),
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

func newBusinessCSVRealService(
	t *testing.T,
) (*backend.Service, store.RealmStore, *businessCSVRoundTripEngine) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.New(t.TempDir() + "/business-csv.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// All data access hangs off the realm handle; the single-realm SQLite store
	// serves domain.DefaultRealm.
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	eng := &businessCSVRoundTripEngine{running: true}
	n, _, err := node.NewLocalNode(ctx, st, func(snap engine.Snapshot) (engine.Engine, error) {
		// Mirror the real adapter and the framework/node fakeEngine: seed the
		// resolver from the snapshot, then let live dictionary calls publish later
		// account and group changes without rebuilding the engine.
		eng.knownAccounts = map[domain.AccountID]struct{}{}
		for _, account := range snap.Accounts {
			eng.knownAccounts[account.Code] = struct{}{}
		}
		eng.knownGroups = map[string]struct{}{}
		for _, group := range snap.Groups {
			eng.knownGroups[group.Code] = struct{}{}
		}
		return eng, nil
	})
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	router, err := node.NewLocalRouter(n)
	if err != nil {
		t.Fatalf("NewLocalRouter: %v", err)
	}
	return backend.New(router, nil, nil), realm, eng
}

type businessCSVRoundTripEngine struct {
	running              bool
	enforceResolver      bool
	knownAccounts        map[domain.AccountID]struct{}
	knownGroups          map[string]struct{}
	adjustmentCalls      []domain.AdjustmentRequest
	adjustmentBatchCalls [][]domain.AdjustmentRequest
}

// resolveAccount mirrors the real adapter's pre-lane account resolution and the
// framework/node fakeEngine: the engine resolver knows only the accounts it was
// seeded with from a build/rebuild snapshot. Enforcement is opt-in (like
// fakeEngine.enforceResolver) so a test that legitimately seeds an account
// directly in the store, without the rebuild production would perform, still
// resolves; the resolve step itself always runs before the lane callback.
func (e *businessCSVRoundTripEngine) resolveAccount(account domain.AccountID) error {
	if !e.enforceResolver {
		return nil
	}
	if _, ok := e.knownAccounts[account]; !ok {
		return fmt.Errorf("engine: unknown account %q: %w", account, domain.ErrInvalid)
	}
	return nil
}

func (e *businessCSVRoundTripEngine) AddAccountResolverEntry(account domain.Account) error {
	if e.knownAccounts == nil {
		e.knownAccounts = map[domain.AccountID]struct{}{}
	}
	if _, exists := e.knownAccounts[account.Code]; exists {
		return fmt.Errorf("engine: duplicate account %q: %w", account.Code, domain.ErrInvalid)
	}
	e.knownAccounts[account.Code] = struct{}{}
	return nil
}

func (e *businessCSVRoundTripEngine) RenameAccountResolverEntry(
	oldCode domain.AccountID, account domain.Account,
) error {
	if _, exists := e.knownAccounts[oldCode]; !exists {
		return fmt.Errorf("engine: unknown account %q: %w", oldCode, domain.ErrInvalid)
	}
	if oldCode != account.Code {
		if _, exists := e.knownAccounts[account.Code]; exists {
			return fmt.Errorf("engine: duplicate account %q: %w", account.Code, domain.ErrInvalid)
		}
	}
	delete(e.knownAccounts, oldCode)
	e.knownAccounts[account.Code] = struct{}{}
	return nil
}

func (e *businessCSVRoundTripEngine) AddGroupResolverEntry(group domain.AccountGroup) error {
	if e.knownGroups == nil {
		e.knownGroups = map[string]struct{}{}
	}
	if _, exists := e.knownGroups[group.Code]; exists {
		return fmt.Errorf("engine: duplicate group %q: %w", group.Code, domain.ErrInvalid)
	}
	e.knownGroups[group.Code] = struct{}{}
	return nil
}

func (e *businessCSVRoundTripEngine) RenameGroupResolverEntry(
	oldCode string, group domain.AccountGroup,
) error {
	if _, exists := e.knownGroups[oldCode]; !exists {
		return fmt.Errorf("engine: unknown group %q: %w", oldCode, domain.ErrInvalid)
	}
	if oldCode != group.Code {
		if _, exists := e.knownGroups[group.Code]; exists {
			return fmt.Errorf("engine: duplicate group %q: %w", group.Code, domain.ErrInvalid)
		}
	}
	delete(e.knownGroups, oldCode)
	e.knownGroups[group.Code] = struct{}{}
	return nil
}

func (e *businessCSVRoundTripEngine) RemoveGroupResolverEntry(group domain.AccountGroup) error {
	if _, exists := e.knownGroups[group.Code]; !exists {
		return fmt.Errorf("engine: unknown group %q: %w", group.Code, domain.ErrInvalid)
	}
	delete(e.knownGroups, group.Code)
	return nil
}

// TestBusinessCSVRoundTripEngine_RunAccountSynchronizedResolvesBeforeCallback
// guards the fake's account-lane seam: an unknown account must reject before the
// lane callback runs, and a known account must resolve and run it. This proves
// the resolve-before-callback fix is not a permissive no-op.
func TestBusinessCSVRoundTripEngine_RunAccountSynchronizedResolvesBeforeCallback(t *testing.T) {
	t.Parallel()
	eng := &businessCSVRoundTripEngine{
		running:         true,
		enforceResolver: true,
		knownAccounts:   map[domain.AccountID]struct{}{"acc-known": {}},
	}

	ran := false
	err := eng.RunAccountSynchronized(context.Background(), "acc-missing",
		func(engine.AccountLane) error {
			ran = true
			return nil
		})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown account error = %v, want ErrInvalid before callback", err)
	}
	if ran {
		t.Fatal("callback ran for an unresolved account; the lane seam is not gated")
	}

	ran = false
	if err := eng.RunAccountSynchronized(context.Background(), "acc-known",
		func(engine.AccountLane) error {
			ran = true
			return nil
		}); err != nil {
		t.Fatalf("known account RunAccountSynchronized: %v", err)
	}
	if !ran {
		t.Fatal("callback did not run for a resolved account")
	}
}

func (e *businessCSVRoundTripEngine) Version() string      { return "fake" }
func (e *businessCSVRoundTripEngine) BuildProfile() string { return "test" }
func (e *businessCSVRoundTripEngine) Running() bool        { return e.running }
func (e *businessCSVRoundTripEngine) ConfigurePolicy(
	context.Context, string, engine.LimitSet,
) (engine.PolicyConfigurationResult, error) {
	return engine.PolicyConfigurationResult{}, nil
}
func (e *businessCSVRoundTripEngine) BlockAccount(context.Context, domain.AccountID, string) error {
	return nil
}
func (e *businessCSVRoundTripEngine) UnblockAccount(context.Context, domain.AccountID) error {
	return nil
}
func (e *businessCSVRoundTripEngine) ApplyAccountAdjustment(
	ctx context.Context, account domain.AccountID, req domain.AdjustmentRequest,
) (engine.AdjustmentResult, error) {
	results, batchReject, err := e.ApplyAccountAdjustmentBatch(ctx, account,
		[]domain.AdjustmentRequest{req})
	if err != nil {
		return engine.AdjustmentResult{}, err
	}
	if batchReject != nil {
		return engine.AdjustmentResult{Rejected: batchReject}, nil
	}
	return results[0], nil
}
func (e *businessCSVRoundTripEngine) ApplyAccountAdjustmentBatch(
	_ context.Context, _ domain.AccountID, reqs []domain.AdjustmentRequest,
) ([]engine.AdjustmentResult, *engine.AdjustmentBatchReject, error) {
	e.adjustmentBatchCalls = append(e.adjustmentBatchCalls,
		append([]domain.AdjustmentRequest(nil), reqs...))
	results := make([]engine.AdjustmentResult, 0, len(reqs))
	for _, req := range reqs {
		e.adjustmentCalls = append(e.adjustmentCalls, req)
		accepted := &domain.AdjustmentOutcomeAccepted{}
		if req.Balance != nil {
			accepted.BalanceResult = req.Balance.Value
		}
		if req.Held != nil {
			accepted.HeldResult = req.Held.Value
		}
		if req.Incoming != nil {
			accepted.IncomingResult = req.Incoming.Value
		}
		results = append(results, engine.AdjustmentResult{Accepted: accepted})
	}
	return results, nil, nil
}
func (e *businessCSVRoundTripEngine) SubmitOrder(
	context.Context, domain.Order,
) (engine.OrderResult, error) {
	return engine.OrderResult{Accepted: true}, nil
}
func (e *businessCSVRoundTripEngine) SetAccountCurrency(
	context.Context, domain.AccountID, string,
) error {
	return nil
}
func (e *businessCSVRoundTripEngine) ClearAccountCurrency(
	context.Context, domain.AccountID,
) error {
	return nil
}
func (e *businessCSVRoundTripEngine) SetAccountPnl(
	context.Context, domain.AccountID, string,
) ([]domain.AccountBlock, error) {
	return nil, nil
}

func (e *businessCSVRoundTripEngine) SetAccountPnlState(
	ctx context.Context,
	id domain.AccountID,
	pnl string,
	haltReason domain.PnlHaltReason,
) ([]domain.AccountBlock, error) {
	if (pnl == "") == (haltReason == "") {
		return nil, domain.ErrInvalid
	}
	if err := domain.ValidatePnlHaltReason(haltReason); err != nil {
		return nil, err
	}
	if haltReason != "" {
		return nil, nil
	}
	return e.SetAccountPnl(ctx, id, pnl)
}
func (e *businessCSVRoundTripEngine) SubmitImmediate(
	context.Context, domain.Order,
) (engine.ImmediateResult, error) {
	return engine.ImmediateResult{Accepted: true}, nil
}
func (e *businessCSVRoundTripEngine) RunAccountSynchronized(
	_ context.Context, account domain.AccountID, fn func(engine.AccountLane) error,
) error {
	// Mirror the real adapter (openPitEngine.RunAccountSynchronized) and the
	// framework/node fakeEngine: resolve the account before entering the lane, so a
	// brand-new account rejects here and its callback never runs unless a pre-lane
	// rebuild has already registered it. Resolving before fn is what exercises the
	// account-lane seam.
	if err := e.resolveAccount(account); err != nil {
		return err
	}
	return fn(e)
}
func (e *businessCSVRoundTripEngine) RunGroupSynchronized(
	_ context.Context, _ string, fn func(engine.GroupLane) error,
) error {
	return fn(e)
}
func (e *businessCSVRoundTripEngine) ApplyExecutionReport(
	context.Context, domain.ExecutionReportInput,
) (engine.ExecutionReportResult, error) {
	return engine.ExecutionReportResult{}, nil
}
func (e *businessCSVRoundTripEngine) RegisterGroup(
	context.Context, []domain.AccountID, string,
) error {
	return nil
}
func (e *businessCSVRoundTripEngine) UnregisterGroup(
	context.Context, []domain.AccountID, string,
) error {
	return nil
}
func (e *businessCSVRoundTripEngine) BlockGroup(context.Context, string, string) error {
	return nil
}
func (e *businessCSVRoundTripEngine) UnblockGroup(context.Context, string) error {
	return nil
}

func (e *businessCSVRoundTripEngine) SetGroupCurrency(
	context.Context, string, string,
) error {
	return nil
}

func (e *businessCSVRoundTripEngine) ClearGroupCurrency(
	context.Context, string,
) error {
	return nil
}
func (e *businessCSVRoundTripEngine) CheckOrder(
	context.Context, domain.OrderProbe,
) (domain.CheckResult, error) {
	return domain.CheckResult{}, nil
}
func (e *businessCSVRoundTripEngine) MarketDataSink() marketdata.Sink {
	return &backendTestSink{}
}
func (e *businessCSVRoundTripEngine) Stop() { e.running = false }

func TestService_BusinessCSVExportAccountGroupFilterPresence(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
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
			file, err := svc.ExportBusinessCSV(context.Background(),
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

// A pnl_halt_reason the engine cannot map would fail the engine rebuild on the
// next start, and CSV import performs no rebuild that would catch it - so the
// import boundary must reject it before it reaches the store.
func TestService_BusinessCSVImportRejectsUnmappablePnlHaltReason(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newBusinessCSVRealService(t)

	body := []byte(
		"code,title,group_code,currency,pnl,pnl_halt_reason,notes,blocked,block_reason\n" +
			"acc-bad,,,,0,missing_fxx,,false,\n",
	)
	_, err := svc.ImportBusinessCSV(ctx, backend.BusinessCSVImportRequest{
		Entity:         businesscsv.EntityAccounts,
		Delimiter:      businesscsv.DelimiterComma,
		Filename:       "accounts.csv",
		Payload:        body,
		ConflictPolicy: businesscsv.ConflictReplace,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ImportBusinessCSV error = %v, want invalid", err)
	}
	if _, ok, err := st.GetAccount(ctx, "acc-bad"); err != nil || ok {
		t.Fatalf("GetAccount acc-bad after failed import: %v ok=%v, want absent", err, ok)
	}
}

// A known reason must still import, so the guard does not reject valid engine
// halts round-tripped through CSV.
func TestService_BusinessCSVImportAcceptsKnownPnlHaltReason(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newBusinessCSVRealService(t)

	body := []byte(
		"code,title,group_code,currency,pnl,pnl_halt_reason,notes,blocked,block_reason\n" +
			"acc-ok,,,,0,missing_fx,,false,\n",
	)
	if _, err := svc.ImportBusinessCSV(ctx, backend.BusinessCSVImportRequest{
		Entity:         businesscsv.EntityAccounts,
		Delimiter:      businesscsv.DelimiterComma,
		Filename:       "accounts.csv",
		Payload:        body,
		ConflictPolicy: businesscsv.ConflictReplace,
	}); err != nil {
		t.Fatalf("ImportBusinessCSV error = %v, want nil", err)
	}
	account, ok, err := st.GetAccount(ctx, "acc-ok")
	if err != nil || !ok {
		t.Fatalf("GetAccount acc-ok: %v ok=%v, want present", err, ok)
	}
	if account.PnlHaltReason != domain.PnlHaltReasonMissingFx {
		t.Fatalf("PnlHaltReason = %q, want %q", account.PnlHaltReason, domain.PnlHaltReasonMissingFx)
	}
}
