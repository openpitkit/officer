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

package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/backup"
	"go.openpit.dev/officer/internal/businesscsv"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
	"go.openpit.dev/officer/internal/mcpcatalog"
	"go.openpit.dev/officer/internal/node"
	"go.openpit.dev/officer/internal/store"
)

// fakeService is a fake Service for handler tests.
type fakeService struct {
	accounts       []domain.Account
	limits         node.AccountLimits
	auditRows      []domain.AuditRow
	auditFilter    domain.AuditFilter
	groups         []domain.AccountGroup
	balances       []domain.Balance
	adjustments    []domain.AccountAdjustmentRecord
	orders         []domain.Order
	trades         []domain.Trade
	orderDetail    domain.OrderDetail
	adjustment     domain.AccountAdjustmentRecord
	submitOrder    domain.Order
	execReportIn   domain.ExecutionReportInput
	checkResult    domain.CheckResult
	overview       backend.Overview
	serviceInfo    backend.ServiceInfo
	backupArchive  backup.Archive
	backupFilename string
	backupSummary  backup.RestoreSummary
	backupErr      error
	csvExport      businesscsv.ExportFile
	csvExportReq   backend.BusinessCSVExportRequest
	csvPreview     backend.BusinessCSVImportPreview
	csvPreviewReq  backend.BusinessCSVImportRequest
	csvImport      backend.BusinessCSVImportResult
	csvImportReq   backend.BusinessCSVImportRequest
	csvErr         error
	restoreArchive backup.Archive
	restoreOptions backup.RestoreOptions
	resetCalled    bool
	resetErr       error
	marketData     backend.MarketDataStatus
	mdVerify       backend.MarketDataSymbolVerification
	mdVerifyErr    error
	mdSearch       backend.MarketDataSymbolSearch
	mdSearchInput  backend.MarketDataSymbolSearchInput
	mdSearchErr    error
	mdCreateResult domain.MarketDataInstance
	status         backend.Status
	statusErr      error
	welcomeSeen    bool
	createErr      error
	blockErr       error
	unblockErr     error
	stateErr       error
	listLimErr     error
	putLimErr      error
	delLimErr      error
	auditErr       error
	groupErr       error

	// Captured typed-limit puts and delete target, for round-trip assertions.
	rateLimitPut      domain.LimitRate
	orderSizeLimitPut domain.LimitOrderSize
	pnlBoundsLimitPut domain.LimitPnlBounds
	deleteLimitTarget node.LimitTarget
	// Captured market-data instance create input.
	mdCreateInstance domain.MarketDataInstance

	// Error fields for list handlers whose service methods otherwise return a
	// hardcoded nil; default nil so existing tests are unaffected.
	listAccountsErr error
	balancesErr     error
	ordersErr       error
	tradesErr       error
	allAdjErr       error

	mcpCommands  []backend.McpCommand
	mcpAccessErr error
	setMcpErr    error
	setMcpCalls  []setMcpCall
	mdCalls      []string

	// Signing / approval fields.
	signingKey            domain.SigningKey
	signingKeys           []domain.SigningKey
	activePublicKey       string
	activePublicKeyFormat string
	noESign               bool
	noESignSet            bool
	approvalToken         backend.ApprovalToken
	submitTokenMode       string
	confirmForce          bool
	cancelForce           bool
	signingErr            error

	// Captured caller-supplied external ids for the user-create surfaces. The
	// adjustment id is captured on ApplyAdjustment; the order id is captured on
	// SubmitOrderToken (which CREATES the order once, mirroring the backend).
	adjustmentExternalID domain.ExternalID
	submitOrderIn        domain.Order

	// orders submitted-then-resolved, keyed by their used external id, so a fake
	// SubmitOrderToken can create once and confirm/cancel resolve the same order.
	submittedOrders map[string]domain.Order
}

type setMcpCall struct {
	command string
	enabled bool
}

func (f *fakeService) Status(_ context.Context) (backend.Status, error) {
	return f.status, f.statusErr
}
func (f *fakeService) ListAccounts(_ context.Context) ([]domain.Account, error) {
	return f.accounts, f.listAccountsErr
}
func (f *fakeService) ExportBackup(
	_ context.Context, _ backup.Scope,
) (backup.Archive, string, error) {
	return f.backupArchive, f.backupFilename, f.backupErr
}
func (f *fakeService) RestoreBackup(
	_ context.Context,
	archive backup.Archive,
	opts backup.RestoreOptions,
) (backup.RestoreSummary, error) {
	f.restoreArchive = archive
	f.restoreOptions = opts
	return f.backupSummary, f.backupErr
}
func (f *fakeService) ExportBusinessCSV(
	_ context.Context,
	req backend.BusinessCSVExportRequest,
) (businesscsv.ExportFile, error) {
	f.csvExportReq = req
	return f.csvExport, f.csvErr
}
func (f *fakeService) PreviewBusinessCSVImport(
	_ context.Context,
	req backend.BusinessCSVImportRequest,
) (backend.BusinessCSVImportPreview, error) {
	f.csvPreviewReq = req
	return f.csvPreview, f.csvErr
}
func (f *fakeService) ImportBusinessCSV(
	_ context.Context,
	req backend.BusinessCSVImportRequest,
) (backend.BusinessCSVImportResult, error) {
	f.csvImportReq = req
	return f.csvImport, f.csvErr
}
func (f *fakeService) ResetDatabase(_ context.Context) error {
	f.resetCalled = true
	return f.resetErr
}
func (f *fakeService) CreateAccount(_ context.Context, id domain.AccountID) (domain.Account, error) {
	if f.createErr != nil {
		return domain.Account{}, f.createErr
	}
	return domain.Account{Code: id}, nil
}
func (f *fakeService) GetAccountState(_ context.Context, id domain.AccountID) (domain.Account, node.AccountLimits, error) {
	if f.stateErr != nil {
		return domain.Account{}, node.AccountLimits{}, f.stateErr
	}
	for _, a := range f.accounts {
		if a.Code == id {
			return a, f.limits, nil
		}
	}
	return domain.Account{}, node.AccountLimits{}, domain.ErrNotFound
}
func (f *fakeService) BlockAccount(_ context.Context, _ domain.AccountID, _ string) error {
	return f.blockErr
}
func (f *fakeService) UnblockAccount(_ context.Context, _ domain.AccountID) error {
	return f.unblockErr
}
func (f *fakeService) DeleteAccount(
	_ context.Context, _ domain.AccountID, _ bool,
) error {
	return f.stateErr
}
func (f *fakeService) ListLimits(_ context.Context, _ domain.AccountID) (node.AccountLimits, error) {
	return f.limits, f.listLimErr
}
func (f *fakeService) PutRateLimit(_ context.Context, l domain.LimitRate) error {
	f.rateLimitPut = l
	return f.putLimErr
}
func (f *fakeService) PutOrderSizeLimit(_ context.Context, l domain.LimitOrderSize) error {
	f.orderSizeLimitPut = l
	return f.putLimErr
}
func (f *fakeService) PutPnlBoundsLimit(_ context.Context, l domain.LimitPnlBounds) error {
	f.pnlBoundsLimitPut = l
	return f.putLimErr
}
func (f *fakeService) DeleteLimit(_ context.Context, t node.LimitTarget) error {
	f.deleteLimitTarget = t
	return f.delLimErr
}
func (f *fakeService) ListAudit(_ context.Context, _ int) ([]domain.AuditRow, error) {
	return f.auditRows, f.auditErr
}
func (f *fakeService) ListAuditFiltered(
	_ context.Context, filter domain.AuditFilter, _ int,
) ([]domain.AuditRow, error) {
	f.auditFilter = filter
	return f.auditRows, f.auditErr
}
func (f *fakeService) ListMcpAccess(_ context.Context) ([]backend.McpCommand, error) {
	return f.mcpCommands, f.mcpAccessErr
}
func (f *fakeService) SetMcpAccess(_ context.Context, command string, enabled bool) error {
	if f.setMcpErr != nil {
		return f.setMcpErr
	}
	f.setMcpCalls = append(f.setMcpCalls, setMcpCall{command: command, enabled: enabled})
	return nil
}
func (f *fakeService) WelcomeSeen(_ context.Context) (bool, error) {
	return f.welcomeSeen, nil
}
func (f *fakeService) SetWelcomeSeen(_ context.Context, seen bool) error {
	f.welcomeSeen = seen
	return nil
}
func (f *fakeService) ListMarketData(_ context.Context) (backend.MarketDataStatus, error) {
	return f.marketData, f.stateErr
}
func (f *fakeService) CreateMarketDataInstance(
	_ context.Context, instance domain.MarketDataInstance,
) (domain.MarketDataInstance, error) {
	f.mdCreateInstance = instance
	f.mdCalls = append(f.mdCalls,
		fmt.Sprintf("create:%s:%s:%s:%v",
			instance.Provider, instance.Label, instance.Credentials, instance.Enabled))
	if !f.mdCreateResult.ExternalID.IsZero() {
		return f.mdCreateResult, f.stateErr
	}
	return instance, f.stateErr
}
func (f *fakeService) SetMarketDataInstanceEnabled(
	_ context.Context, id string, enabled bool,
) error {
	f.mdCalls = append(f.mdCalls, fmt.Sprintf("instance:%s:%v", id, enabled))
	return f.stateErr
}
func (f *fakeService) UpdateMarketDataInstanceSettings(
	_ context.Context, id, label, credentials string,
) error {
	f.mdCalls = append(f.mdCalls,
		fmt.Sprintf("settings:%s:%s:%s", id, label, credentials))
	return f.stateErr
}
func (f *fakeService) DeleteMarketDataInstance(
	_ context.Context, id string, force bool,
) error {
	f.mdCalls = append(f.mdCalls, fmt.Sprintf("delete-instance:%s:%v", id, force))
	return f.stateErr
}
func (f *fakeService) UpsertMarketDataInstrument(
	_ context.Context, instrument domain.MarketDataInstrument,
) error {
	f.mdCalls = append(f.mdCalls, "upsert-instrument:"+instrument.ExternalSymbol)
	return f.stateErr
}
func (f *fakeService) SetMarketDataInstrumentEnabled(
	_ context.Context, instanceID, externalSymbol string, enabled bool,
) error {
	f.mdCalls = append(f.mdCalls,
		fmt.Sprintf("instrument:%s/%s:%v", instanceID, externalSymbol, enabled))
	return f.stateErr
}
func (f *fakeService) DeleteMarketDataInstrument(
	_ context.Context, instanceID, externalSymbol string,
) error {
	f.mdCalls = append(f.mdCalls, "delete-instrument:"+instanceID+"/"+externalSymbol)
	return f.stateErr
}
func (f *fakeService) VerifyMarketDataSymbol(
	_ context.Context, id, externalSymbol string,
) (backend.MarketDataSymbolVerification, error) {
	f.mdCalls = append(f.mdCalls, "verify-symbol:"+id+"/"+externalSymbol)
	return f.mdVerify, f.mdVerifyErr
}
func (f *fakeService) SearchMarketDataSymbols(
	_ context.Context, id string, input backend.MarketDataSymbolSearchInput,
) (backend.MarketDataSymbolSearch, error) {
	f.mdCalls = append(f.mdCalls, "search-symbols:"+id+"/"+input.Query)
	f.mdSearchInput = input
	return f.mdSearch, f.mdSearchErr
}
func (f *fakeService) SetAccountGroup(_ context.Context, _ domain.AccountID, _ string) error {
	return f.stateErr
}
func (f *fakeService) SetAccountNotes(_ context.Context, _ domain.AccountID, _ string) error {
	return f.stateErr
}
func (f *fakeService) CreateGroup(
	_ context.Context, g domain.AccountGroup,
) (domain.AccountGroup, error) {
	if f.groupErr != nil {
		return domain.AccountGroup{}, f.groupErr
	}
	f.groups = append(f.groups, g)
	return g, nil
}
func (f *fakeService) ListGroups(_ context.Context) ([]domain.AccountGroup, error) {
	return f.groups, f.groupErr
}
func (f *fakeService) GetGroup(
	_ context.Context, code string,
) (domain.AccountGroup, []domain.Account, error) {
	if f.groupErr != nil {
		return domain.AccountGroup{}, nil, f.groupErr
	}
	for _, g := range f.groups {
		if g.Code == code {
			return g, f.accounts, nil
		}
	}
	return domain.AccountGroup{}, nil, domain.ErrNotFound
}
func (f *fakeService) SetGroupNotes(_ context.Context, _, _ string) error {
	return f.groupErr
}
func (f *fakeService) SetGroupBlocked(_ context.Context, _ string, _ bool, _ string) error {
	return f.groupErr
}
func (f *fakeService) DeleteGroup(_ context.Context, _ string) error {
	return f.groupErr
}
func (f *fakeService) ApplyAdjustment(
	_ context.Context, _ domain.AccountID, externalID domain.ExternalID,
	_ domain.AdjustmentRequest,
) (domain.AccountAdjustmentRecord, error) {
	f.adjustmentExternalID = externalID
	return f.adjustment, f.stateErr
}
func (f *fakeService) ListBalances(
	_ context.Context, _ domain.AccountID, _ string,
) ([]domain.Balance, error) {
	return f.balances, f.balancesErr
}
func (f *fakeService) ListAdjustments(
	_ context.Context, _ domain.AccountID, _ domain.Source, _ int,
) ([]domain.AccountAdjustmentRecord, error) {
	return f.adjustments, f.stateErr
}
func (f *fakeService) ListAllAdjustments(
	_ context.Context, _ domain.AccountID, _ domain.Source, _ int,
) ([]domain.AccountAdjustmentRecord, error) {
	return f.adjustments, f.allAdjErr
}
func (f *fakeService) SubmitOrder(_ context.Context, _ domain.Order) (domain.Order, error) {
	return f.submitOrder, f.stateErr
}
func (f *fakeService) CheckOrder(_ context.Context, _ domain.OrderProbe) (domain.CheckResult, error) {
	return f.checkResult, f.stateErr
}
func (f *fakeService) ApplyExecutionReport(
	_ context.Context, in domain.ExecutionReportInput,
) (engine.ExecutionReportResult, error) {
	f.execReportIn = in
	return engine.ExecutionReportResult{}, f.stateErr
}
func (f *fakeService) GetOrder(_ context.Context, _ string) (domain.OrderDetail, error) {
	return f.orderDetail, f.stateErr
}
func (f *fakeService) ListOrders(
	_ context.Context, _ domain.AccountID, _ domain.Source, _ int,
) ([]domain.Order, error) {
	return f.orders, f.ordersErr
}
func (f *fakeService) ListTrades(
	_ context.Context, _ domain.AccountID, _ domain.Source, _ int,
) ([]domain.Trade, error) {
	return f.trades, f.tradesErr
}
func (f *fakeService) Overview(_ context.Context, _ time.Time) (backend.Overview, error) {
	return f.overview, f.statusErr
}
func (f *fakeService) ServiceInfo(_ context.Context) (backend.ServiceInfo, error) {
	return f.serviceInfo, f.statusErr
}
func (f *fakeService) RestartMarketData(_ context.Context) error {
	return f.stateErr
}
func (f *fakeService) GenerateSigningKey(_ context.Context) (domain.SigningKey, error) {
	return f.signingKey, f.signingErr
}
func (f *fakeService) ImportSigningKey(_ context.Context, _, _ string) (domain.SigningKey, error) {
	return f.signingKey, f.signingErr
}
func (f *fakeService) ListSigningKeys(_ context.Context) ([]domain.SigningKey, error) {
	return f.signingKeys, f.signingErr
}
func (f *fakeService) ActivePublicKey(format string) (string, error) {
	f.activePublicKeyFormat = format
	return f.activePublicKey, f.signingErr
}
func (f *fakeService) GetNoESign(_ context.Context) (bool, error) {
	return f.noESign, f.signingErr
}
func (f *fakeService) SetNoESign(_ context.Context, off bool) error {
	f.noESignSet = off
	return f.signingErr
}

// SubmitOrderToken mirrors the real backend: submit CREATES the order exactly
// once. It uses the caller-supplied external id when set, otherwise generates a
// deterministic one, records the created order keyed by that id, and returns an
// approval token whose OrderExternalID is the id actually used — so a later
// confirm/cancel resolves the same order. When signingErr is set it surfaces
// before any create, so duplicate/malformed-id rejection can be exercised.
func (f *fakeService) SubmitOrderToken(
	_ context.Context, o domain.Order, mode string,
) (backend.ApprovalToken, error) {
	f.submitTokenMode = mode
	f.submitOrderIn = o
	if f.signingErr != nil {
		return backend.ApprovalToken{}, f.signingErr
	}
	used := o.ExternalID
	if used.IsZero() {
		used = extID("generated-order")
	}
	o.ExternalID = used
	if f.submittedOrders == nil {
		f.submittedOrders = make(map[string]domain.Order)
	}
	f.submittedOrders[used.String()] = o
	tok := f.approvalToken
	tok.OrderExternalID = used.String()
	return tok, nil
}
func (f *fakeService) ConfirmExecution(
	_ context.Context, orderID string, _ string, force bool,
) (domain.Order, error) {
	f.confirmForce = force
	if f.signingErr != nil {
		return domain.Order{}, f.signingErr
	}
	if o, ok := f.submittedOrders[orderID]; ok {
		o.Status = domain.OrderStatusCommitted
		return o, nil
	}
	return f.submitOrder, nil
}
func (f *fakeService) CancelOrder(
	_ context.Context, orderID string, _, _ string, force bool,
) (domain.Order, error) {
	f.cancelForce = force
	if f.signingErr != nil {
		return domain.Order{}, f.signingErr
	}
	if o, ok := f.submittedOrders[orderID]; ok {
		o.Status = domain.OrderStatusRejected
		return o, nil
	}
	return f.submitOrder, nil
}

// fakeSPA returns a minimal in-memory filesystem for the SPA option.
func fakeSPA() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html></html>")},
	}
}

func newRouter(svc Service) (http.Handler, error) {
	return NewRouter(Options{Service: svc, SPA: fakeSPA()})
}

// bodyMap decodes a JSON response body into a map.
func bodyMap(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return m
}

// extID builds a deterministic ExternalID from a short seed for test fixtures.
// The wire form is the 22-char base64url of the 16 raw bytes.
func extID(seed string) domain.ExternalID {
	var b [16]byte
	copy(b[:], seed)
	id, err := domain.ExternalIDFromBytes(b[:])
	if err != nil {
		panic(err)
	}
	return id
}

// assertNoSurrogateID fails if a decoded response sub-map leaks any forbidden
// surrogate or engine identifier key. The new identity model addresses resources
// by their public handle (code / externalId) only; no numeric or engine id is
// ever serialized.
func assertNoSurrogateID(t *testing.T, obj map[string]any) {
	t.Helper()
	for _, k := range []string{"id", "orderId", "engineId", "engineAccountId", "engineGroupId"} {
		if _, ok := obj[k]; ok {
			t.Fatalf("response leaked forbidden key %q: %v", k, obj)
		}
	}
}

func TestHealthz(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestV1Health(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if m["ok"] != true {
		t.Fatalf("want ok:true, got %v", m["ok"])
	}
}

func TestV1Status(t *testing.T) {
	svc := &fakeService{
		status: backend.Status{
			Nodes: []node.Health{{
				Engine: engine.Health{Version: "v1", Running: true},
				Store:  store.StoreHealth{Reachable: true},
			}},
			Healthy: true,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if m["healthy"] != true {
		t.Fatalf("want healthy:true, got %v", m["healthy"])
	}
}

func TestBusinessCSVExport(t *testing.T) {
	svc := &fakeService{csvExport: businesscsv.ExportFile{
		Name:        "pit-officer-accounts-20260625T100000Z.csv",
		ContentType: "text/csv; charset=utf-8",
		Body:        []byte("account_id,group_id\nacc-1,desk-a\n"),
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"entity":"accounts",
		"delimiter":"pipe",
		"zip":true,
		"filters":{"groupCode":"desk-a","account":"acc-1","asset":"AAPL","source":"api"}
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/export", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got !=
		`attachment; filename="pit-officer-accounts-20260625T100000Z.csv"` {
		t.Fatalf("content disposition = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if svc.csvExportReq.Entity != businesscsv.EntityAccounts ||
		svc.csvExportReq.Delimiter != businesscsv.DelimiterPipe ||
		!svc.csvExportReq.Zip ||
		svc.csvExportReq.Filter.GroupCode != "desk-a" ||
		!svc.csvExportReq.Filter.GroupCodeSet ||
		svc.csvExportReq.Filter.Account != "acc-1" ||
		svc.csvExportReq.Filter.Asset != "AAPL" ||
		svc.csvExportReq.Filter.Source != domain.SourceAPI {
		t.Fatalf("csvExportReq = %+v", svc.csvExportReq)
	}
}

func TestBusinessCSVExportOmittedGroupFilter(t *testing.T) {
	svc := &fakeService{csvExport: businesscsv.ExportFile{
		Name:        "pit-officer-accounts-20260625T100000Z.csv",
		ContentType: "text/csv; charset=utf-8",
		Body:        []byte("account_id,group_id\n"),
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"entity":"accounts",
		"delimiter":"comma",
		"filters":{"account":"acc-1"}
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/export", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.csvExportReq.Filter.GroupCodeSet ||
		svc.csvExportReq.Filter.GroupCode != "" {
		t.Fatalf("csvExportReq = %+v", svc.csvExportReq)
	}
}

func TestBusinessCSVExportExplicitEmptyGroupFilter(t *testing.T) {
	svc := &fakeService{csvExport: businesscsv.ExportFile{
		Name:        "pit-officer-accounts-20260625T100000Z.csv",
		ContentType: "text/csv; charset=utf-8",
		Body:        []byte("account_id,group_id\n"),
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"entity":"accounts",
		"delimiter":"comma",
		"filters":{"groupCode":""}
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/export", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !svc.csvExportReq.Filter.GroupCodeSet ||
		svc.csvExportReq.Filter.GroupCode != "" {
		t.Fatalf("csvExportReq = %+v", svc.csvExportReq)
	}
}

func TestBusinessCSVImport(t *testing.T) {
	svc := &fakeService{csvImport: backend.BusinessCSVImportResult{
		Counts: businesscsv.ImportCounts{Rows: 1, Applied: 1},
		File:   businesscsv.ImportFile{Name: "accounts.csv", Type: "csv"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString([]byte(
		"account_id,group_id,notes,blocked,block_reason\nacc-1,,note,false,\n",
	))
	body := bytes.NewBufferString(fmt.Sprintf(`{
		"entity":"accounts",
		"delimiter":"comma",
		"filename":"accounts.csv",
		"payloadBase64":%q,
		"conflictPolicy":"replace"
	}`, payload))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/import", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.csvImportReq.Entity != businesscsv.EntityAccounts ||
		svc.csvImportReq.Delimiter != businesscsv.DelimiterComma ||
		svc.csvImportReq.Filename != "accounts.csv" ||
		svc.csvImportReq.ConflictPolicy != businesscsv.ConflictReplace ||
		!bytes.Contains(svc.csvImportReq.Payload, []byte("acc-1")) {
		t.Fatalf("csvImportReq = %+v", svc.csvImportReq)
	}
}

func TestDecodeBusinessCSVPayloadBase64SizeGuard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		encoded  string
		maxBytes int
		wantErr  bool
	}{
		{
			name:     "pre-decode too large",
			encoded:  "AAAAAAAAA",
			maxBytes: 4,
			wantErr:  true,
		},
		{
			name:     "post-decode too large",
			encoded:  base64.StdEncoding.EncodeToString([]byte("12345")),
			maxBytes: 4,
			wantErr:  true,
		},
		{
			name:     "at limit",
			encoded:  base64.StdEncoding.EncodeToString([]byte("1234")),
			maxBytes: 4,
			wantErr:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := decodeBusinessCSVPayloadBase64(tc.encoded, tc.maxBytes)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrTooLarge) {
					t.Fatalf("error = %v, want too large", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeBusinessCSVPayloadBase64: %v", err)
			}
			if string(payload) != "1234" {
				t.Fatalf("payload = %q, want 1234", payload)
			}
		})
	}
}

func TestWriteErrMapsTooLargeTo413(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()

	writeErr(rec, businesscsv.NewTooLargeError(false))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "too_large" ||
		body.Error.Message == "" {
		t.Fatalf("error body = %+v", body.Error)
	}
}

func TestRequestBodyLimitUsesImportEnvelopeCap(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(
		http.MethodPost, "/api/v1/business-csv/import/preview", nil,
	)
	if got := requestBodyLimit(req); got != maxImportBody {
		t.Fatalf("requestBodyLimit import = %d, want %d", got, maxImportBody)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/backup/restore", nil)
	if got := requestBodyLimit(req); got != maxBackupRestoreBody {
		t.Fatalf("requestBodyLimit backup = %d, want %d", got, maxBackupRestoreBody)
	}
}

func TestListAccounts(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{
			{Code: "acc-1", Title: "Account One", Blocked: false},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	accounts, ok := m["accounts"].([]any)
	if !ok || len(accounts) != 1 {
		t.Fatalf("want 1 account, got %v", m["accounts"])
	}
	a := accounts[0].(map[string]any)
	// Accounts are addressed by their public code; no surrogate id leaks.
	if a["code"] != "acc-1" {
		t.Fatalf("want code=acc-1, got %v", a["code"])
	}
	if a["title"] != "Account One" {
		t.Fatalf("want title=Account One, got %v", a["title"])
	}
	assertNoSurrogateID(t, a)
	if _, ok := a["blocked"]; !ok {
		t.Fatal("missing blocked field")
	}
	if _, ok := a["blockReason"]; !ok {
		t.Fatal("missing blockReason field")
	}
}

func TestCreateAccount(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"acc-1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	acc, ok := m["account"].(map[string]any)
	if !ok {
		t.Fatalf("want account object, got %v", m["account"])
	}
	if acc["code"] != "acc-1" {
		t.Fatalf("want code=acc-1, got %v", acc["code"])
	}
	assertNoSurrogateID(t, acc)
}

func TestCreateAccount_ValidationError(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	// Empty code fails ValidateAccountID
	body := bytes.NewBufferString(`{"code":""}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestCreateAccount_Conflict(t *testing.T) {
	svc := &fakeService{createErr: domain.ErrAlreadyExists}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"acc-1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "conflict" {
		t.Fatalf("want code=conflict, got %v", errObj["code"])
	}
}

func TestVerifyMarketDataSymbol_Unsupported(t *testing.T) {
	svc := &fakeService{
		mdVerify: backend.MarketDataSymbolVerification{Supported: false},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"externalSymbol":"AAPL"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/market-data/instances/byo-1/verify-symbol",
		body,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	v, ok := m["verification"].(map[string]any)
	if !ok {
		t.Fatalf("want verification object, got %v", m["verification"])
	}
	if v["supported"] != false {
		t.Fatalf("want supported=false, got %v", v["supported"])
	}
	if len(svc.mdCalls) != 1 || svc.mdCalls[0] != "verify-symbol:byo-1/AAPL" {
		t.Fatalf("unexpected service calls: %v", svc.mdCalls)
	}
}

func TestVerifyMarketDataSymbol_SuggestionFlows(t *testing.T) {
	svc := &fakeService{
		mdVerify: backend.MarketDataSymbolVerification{
			Supported: true, Exists: false, Suggestion: "ETHUSDT",
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"externalSymbol":"ethusdt"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/market-data/instances/bn-1/verify-symbol",
		body,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	v, _ := m["verification"].(map[string]any)
	if v["supported"] != true || v["exists"] != false {
		t.Fatalf("want supported=true exists=false, got %v", v)
	}
	if v["suggestion"] != "ETHUSDT" {
		t.Fatalf("want suggestion=ETHUSDT, got %v", v["suggestion"])
	}
}

func TestGetAccount(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{
			{Code: "acc-1", Title: "Account One"},
		},
		limits: node.AccountLimits{
			RateLimits: []domain.LimitRate{
				{
					Scope:     domain.ScopeAccount,
					Account:   "acc-1",
					Window:    time.Second,
					MaxOrders: 100,
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/acc-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	acc, ok := m["account"].(map[string]any)
	if !ok {
		t.Fatal("missing account field")
	}
	if acc["code"] != "acc-1" {
		t.Fatalf("want code=acc-1, got %v", acc["code"])
	}
	assertNoSurrogateID(t, acc)
	// The per-policy limits view carries the three typed barrier arrays.
	limits, ok := m["limits"].(map[string]any)
	if !ok {
		t.Fatalf("want limits object, got %v", m["limits"])
	}
	rates, _ := limits["rateLimits"].([]any)
	if len(rates) != 1 {
		t.Fatalf("want 1 rate limit, got %v", limits["rateLimits"])
	}
	rate := rates[0].(map[string]any)
	if rate["scope"] != domain.ScopeAccount || rate["account"] != "acc-1" ||
		rate["maxOrders"] != float64(100) || rate["windowMs"] != float64(1000) {
		t.Fatalf("unexpected rate limit: %v", rate)
	}
}

func TestGetAccount_NotFound(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

func TestGetAccount_URLEncodedID(t *testing.T) {
	// Verify that percent-encoded characters in the account id are decoded
	// before reaching the service. %40 = '@'.
	svc := &fakeService{
		accounts: []domain.Account{
			{Code: "acc@1"},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	// acc%401 decodes to acc@1
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/acc%401", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for URL-encoded id, got %d", rec.Code)
	}
}

func TestBlockAccount(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{{Code: "acc-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"reason":"compliance"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/block", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestBlockAccount_NotFound(t *testing.T) {
	svc := &fakeService{blockErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"reason":"x"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-x/block", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestUnblockAccount(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{{Code: "acc-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/accounts/acc-1/unblock", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestListLimits(t *testing.T) {
	svc := &fakeService{
		limits: node.AccountLimits{
			OrderSizeLimits: []domain.LimitOrderSize{
				{Scope: domain.ScopeBroker, MaxQuantity: "500"},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/limits", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	// GET /limits returns the three-array per-policy object, never a flat list.
	limits, ok := m["limits"].(map[string]any)
	if !ok {
		t.Fatalf("want limits object, got %v", m["limits"])
	}
	for _, field := range []string{"rateLimits", "orderSizeLimits", "pnlBoundsLimits"} {
		if _, ok := limits[field].([]any); !ok {
			t.Fatalf("limits missing array field %q: %v", field, limits)
		}
	}
	sizes, _ := limits["orderSizeLimits"].([]any)
	if len(sizes) != 1 {
		t.Fatalf("want 1 order-size limit, got %v", limits["orderSizeLimits"])
	}
	size := sizes[0].(map[string]any)
	if size["scope"] != domain.ScopeBroker || size["maxQuantity"] != "500" {
		t.Fatalf("unexpected order-size limit: %v", size)
	}
}

func TestListLimits_AccountFilter(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/limits?account=acc-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestPutRateLimit(t *testing.T) {
	svc := &fakeService{
		limits: node.AccountLimits{
			RateLimits: []domain.LimitRate{
				{
					Scope:     domain.ScopeAccountAsset,
					Account:   "acc-1",
					Asset:     "AAPL",
					Window:    2 * time.Second,
					MaxOrders: 101,
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"scope":"account_asset","account":"acc-1","asset":"AAPL",
		"windowMs":1000,"maxOrders":100
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/rate", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	// The handler captures the typed barrier and responds from persisted state.
	if svc.rateLimitPut.Scope != domain.ScopeAccountAsset ||
		svc.rateLimitPut.Account != "acc-1" ||
		svc.rateLimitPut.Asset != "AAPL" ||
		svc.rateLimitPut.Window != time.Second ||
		svc.rateLimitPut.MaxOrders != 100 {
		t.Fatalf("captured rate limit = %+v", svc.rateLimitPut)
	}
	m := bodyMap(t, rec.Result())
	rl, ok := m["rateLimit"].(map[string]any)
	if !ok {
		t.Fatalf("response missing rateLimit field: %v", m)
	}
	if rl["maxOrders"] != float64(101) || rl["windowMs"] != float64(2000) {
		t.Fatalf("unexpected persisted rateLimit: %v", rl)
	}
}

func TestPutOrderSizeLimit(t *testing.T) {
	svc := &fakeService{
		limits: node.AccountLimits{
			OrderSizeLimits: []domain.LimitOrderSize{
				{
					Scope:       domain.ScopeAccountAsset,
					Account:     "acc-1",
					Asset:       "AAPL",
					MaxQuantity: "501",
					MaxNotional: "50001",
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"scope":"account_asset","account":"acc-1","asset":"AAPL",
		"maxQuantity":"500","maxNotional":"50000"
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/order-size", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.orderSizeLimitPut.Scope != domain.ScopeAccountAsset ||
		svc.orderSizeLimitPut.Account != "acc-1" ||
		svc.orderSizeLimitPut.Asset != "AAPL" ||
		svc.orderSizeLimitPut.MaxQuantity != "500" ||
		svc.orderSizeLimitPut.MaxNotional != "50000" {
		t.Fatalf("captured order-size limit = %+v", svc.orderSizeLimitPut)
	}
	m := bodyMap(t, rec.Result())
	osl, ok := m["orderSizeLimit"].(map[string]any)
	if !ok {
		t.Fatalf("response missing orderSizeLimit field: %v", m)
	}
	if osl["maxQuantity"] != "501" || osl["maxNotional"] != "50001" {
		t.Fatalf("unexpected persisted orderSizeLimit: %v", osl)
	}
}

func TestPutPnlBoundsLimit(t *testing.T) {
	svc := &fakeService{
		limits: node.AccountLimits{
			PnlBoundsLimits: []domain.LimitPnlBounds{
				{
					Scope:      domain.ScopeAccountAsset,
					Account:    "acc-1",
					Asset:      "AAPL",
					LowerBound: "-999",
					UpperBound: "5001",
					InitialPnl: "1",
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"scope":"account_asset","account":"acc-1","asset":"AAPL",
		"lowerBound":"-1000","upperBound":"5000","initialPnl":"0"
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/pnl-bounds", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.pnlBoundsLimitPut.Scope != domain.ScopeAccountAsset ||
		svc.pnlBoundsLimitPut.Account != "acc-1" ||
		svc.pnlBoundsLimitPut.Asset != "AAPL" ||
		svc.pnlBoundsLimitPut.LowerBound != "-1000" ||
		svc.pnlBoundsLimitPut.UpperBound != "5000" ||
		svc.pnlBoundsLimitPut.InitialPnl != "0" {
		t.Fatalf("captured pnl-bounds limit = %+v", svc.pnlBoundsLimitPut)
	}
	m := bodyMap(t, rec.Result())
	pbl, ok := m["pnlBoundsLimit"].(map[string]any)
	if !ok {
		t.Fatalf("response missing pnlBoundsLimit field: %v", m)
	}
	if pbl["lowerBound"] != "-999" || pbl["upperBound"] != "5001" ||
		pbl["initialPnl"] != "1" {
		t.Fatalf("unexpected persisted pnlBoundsLimit: %v", pbl)
	}
}

func TestPutRateLimit_ValidationError(t *testing.T) {
	svc := &fakeService{putLimErr: fmt.Errorf("bad scope: %w", domain.ErrInvalid)}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"scope":"broker","windowMs":1000,"maxOrders":100}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/rate", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestPutRateLimit_NotImplemented(t *testing.T) {
	// A wrapped domain.ErrNotImplemented (the engine's not-implemented stub
	// surfacing through the node and backend) maps to HTTP 501 with the wrapped
	// message, taking precedence over the generic 500 path.
	const msg = "engine: rate_limit add not supported yet"
	svc := &fakeService{
		putLimErr: fmt.Errorf("%s: %w", msg, domain.ErrNotImplemented),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"scope":"asset","asset":"AAPL","windowMs":1000,"maxOrders":100}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/rate", body))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("want 501, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_implemented" {
		t.Fatalf("want code=not_implemented, got %v", errObj["code"])
	}
	if errObj["message"] != fmt.Sprintf("%s: %s", msg, domain.ErrNotImplemented) {
		t.Fatalf("want wrapped message, got %v", errObj["message"])
	}
}

func TestPutRateLimit_EngineRestarting(t *testing.T) {
	const msg = "engine restart in progress; mutating requests are rejected until rebuild completes"
	svc := &fakeService{
		putLimErr: fmt.Errorf("%s: %w", msg, domain.ErrEngineRestarting),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"scope":"asset","asset":"AAPL","windowMs":1000,"maxOrders":100}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits/rate", body))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "engine_restarting" {
		t.Fatalf("want code=engine_restarting, got %v", errObj["code"])
	}
	if errObj["message"] != fmt.Sprintf("%s: %s", msg, domain.ErrEngineRestarting) {
		t.Fatalf("want wrapped message, got %v", errObj["message"])
	}
}

func TestDeleteLimit(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/limits?policy=rate_limit&scope=account_asset&account=acc-1&asset=AAPL", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	// The delete target round-trips into the typed node.LimitTarget.
	if svc.deleteLimitTarget.Policy != domain.PolicyRateLimit ||
		svc.deleteLimitTarget.Scope != domain.ScopeAccountAsset ||
		svc.deleteLimitTarget.Account != "acc-1" ||
		svc.deleteLimitTarget.Asset != "AAPL" {
		t.Fatalf("captured delete target = %+v", svc.deleteLimitTarget)
	}
}

func TestDeleteLimit_NotFound(t *testing.T) {
	svc := &fakeService{delLimErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/limits?policy=rate_limit&scope=broker", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

// TestDeleteAccount_HasDependents asserts the 409 has_dependents wire shape: a
// HasDependentsError from the service maps to HTTP 409 with body
// error.code == "has_dependents" and a dependents array of {kind, count}.
func TestDeleteAccount_HasDependents(t *testing.T) {
	svc := &fakeService{stateErr: domain.NewHasDependentsError([]domain.DependentCount{
		{Kind: "orders", Count: 3},
	})}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/accounts/acc-1", nil))
	assertHasDependents409(t, rec, "orders", 3)
}

// TestDeleteMarketDataInstance_HasDependents asserts the same 409 wire shape for
// the market-data instance delete endpoint.
func TestDeleteMarketDataInstance_HasDependents(t *testing.T) {
	svc := &fakeService{stateErr: domain.NewHasDependentsError([]domain.DependentCount{
		{Kind: "instruments", Count: 2},
	})}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/market-data/instances/inst-1", nil))
	assertHasDependents409(t, rec, "instruments", 2)
}

// assertHasDependents409 checks the recorded response is the 409 has_dependents
// wire shape with a single dependent of the wanted kind/count.
func assertHasDependents409(t *testing.T, rec *httptest.ResponseRecorder, wantKind string, wantCount int) {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	body := bodyMap(t, rec.Result())
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object: %+v", body)
	}
	if errObj["code"] != "has_dependents" {
		t.Fatalf("error.code = %v, want has_dependents", errObj["code"])
	}
	deps, ok := errObj["dependents"].([]any)
	if !ok || len(deps) != 1 {
		t.Fatalf("dependents = %v, want one entry", errObj["dependents"])
	}
	dep, ok := deps[0].(map[string]any)
	if !ok {
		t.Fatalf("dependent[0] not an object: %v", deps[0])
	}
	if dep["kind"] != wantKind {
		t.Fatalf("dependent kind = %v, want %q", dep["kind"], wantKind)
	}
	// JSON numbers decode to float64.
	if count, _ := dep["count"].(float64); int(count) != wantCount {
		t.Fatalf("dependent count = %v, want %d", dep["count"], wantCount)
	}
}

func TestListAudit(t *testing.T) {
	ts := time.Date(2026, 6, 11, 10, 0, 0, 1, time.UTC)
	svc := &fakeService{
		auditRows: []domain.AuditRow{
			{
				ExternalID: extID("audit-1"),
				At:         ts,
				Actor:      "operator",
				Action:     domain.AuditActionSetLimit,
				Account:    "acc-1",
				Detail:     "set limit rate_limit account=acc-1",
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	entries, _ := m["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %v", m["entries"])
	}
	e := entries[0].(map[string]any)
	for _, field := range []string{"externalId", "at", "actor", "action", "account", "detail"} {
		if _, ok := e[field]; !ok {
			t.Fatalf("audit entry missing field %q", field)
		}
	}
	// The audit row is addressed by its opaque external id; no surrogate id leaks.
	if e["externalId"] != extID("audit-1").String() {
		t.Fatalf("want externalId=%s, got %v", extID("audit-1").String(), e["externalId"])
	}
	assertNoSurrogateID(t, e)
	if e["actor"] != "operator" {
		t.Fatalf("want actor=operator, got %v", e["actor"])
	}
}

func TestListAudit_FilterResolution(t *testing.T) {
	tradingActions := domain.AuditActionsByCategory(domain.AuditCategoryTrading)
	controlActions := domain.AuditActionsByCategory(domain.AuditCategoryControl)
	cases := []struct {
		name  string
		query string
		want  []domain.AuditAction
	}{
		{"default hides trading", "/api/v1/audit", controlActions},
		{"category control", "/api/v1/audit?category=control", controlActions},
		{"category trading", "/api/v1/audit?category=trading", tradingActions},
		{"category all", "/api/v1/audit?category=all", nil},
		{"explicit actions", "/api/v1/audit?actions=block,submit_order",
			[]domain.AuditAction{domain.AuditActionBlock, domain.AuditActionSubmitOrder}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
			if !slices.Equal(svc.auditFilter.Actions, tc.want) {
				t.Fatalf("actions = %+v, want %+v", svc.auditFilter.Actions, tc.want)
			}
		})
	}
}

func TestListAudit_UnknownActionRejected(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit?actions=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for unknown action, got %d", rec.Code)
	}
}

func TestListAudit_LimitCapping(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	// Requesting more than auditCapREST should be silently capped (not error).
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/audit?limit=%d", auditCapREST+999), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestListAudit_InvalidLimit(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/audit?limit=abc", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestListAuditActions(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit/actions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	groups, ok := m["groups"].([]any)
	if !ok || len(groups) != 2 {
		t.Fatalf("want 2 groups, got %v", m["groups"])
	}

	// auditGroup decodes one group entry into its category and ordered actions.
	auditGroup := func(v any) (string, []string) {
		g := v.(map[string]any)
		category, _ := g["category"].(string)
		raw, _ := g["actions"].([]any)
		actions := make([]string, 0, len(raw))
		for _, a := range raw {
			actions = append(actions, a.(string))
		}
		return category, actions
	}

	controlCat, controlActions := auditGroup(groups[0])
	tradingCat, tradingActions := auditGroup(groups[1])
	if controlCat != string(domain.AuditCategoryControl) {
		t.Fatalf("groups[0].category = %q, want control", controlCat)
	}
	if tradingCat != string(domain.AuditCategoryTrading) {
		t.Fatalf("groups[1].category = %q, want trading", tradingCat)
	}

	wantControl := auditActionStrings(
		domain.AuditActionsByCategory(domain.AuditCategoryControl))
	wantTrading := auditActionStrings(
		domain.AuditActionsByCategory(domain.AuditCategoryTrading))
	if !slices.Equal(controlActions, wantControl) {
		t.Fatalf("control actions = %v, want %v", controlActions, wantControl)
	}
	if !slices.Equal(tradingActions, wantTrading) {
		t.Fatalf("trading actions = %v, want %v", tradingActions, wantTrading)
	}

	// The trading group is exactly the high-volume order/execution stream.
	if !slices.Equal(tradingActions, []string{"submit_order", "execution_report"}) {
		t.Fatalf("trading group = %v, want [submit_order execution_report]", tradingActions)
	}

	// The concatenation of all groups equals the full canonical catalogue in
	// order; this guards against future drift between the grouped endpoint and
	// domain.AllAuditActions.
	got := append(append([]string{}, controlActions...), tradingActions...)
	want := auditActionStrings(domain.AllAuditActions())
	if !slices.Equal(got, want) {
		t.Fatalf("concatenated actions = %v, want %v", got, want)
	}
}

func TestLimitDTO_JSONShape(t *testing.T) {
	// The per-policy limits view marshals into the three typed barrier arrays
	// with the camelCase wire keys the contract requires.
	limits := node.AccountLimits{
		RateLimits: []domain.LimitRate{
			{Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
				Window: time.Second, MaxOrders: 100},
		},
		OrderSizeLimits: []domain.LimitOrderSize{
			{Scope: domain.ScopeBroker, MaxQuantity: "500", MaxNotional: "50000"},
		},
		PnlBoundsLimits: []domain.LimitPnlBounds{
			{Scope: domain.ScopeAsset, Asset: "AAPL",
				LowerBound: "-1000", UpperBound: "5000", InitialPnl: "0"},
		},
	}
	b, err := json.Marshal(toAccountLimitsDTO(limits))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	rates, _ := m["rateLimits"].([]any)
	if len(rates) != 1 {
		t.Fatalf("want 1 rate limit, got %v", m["rateLimits"])
	}
	rate := rates[0].(map[string]any)
	for _, key := range []string{"scope", "account", "asset", "windowMs", "maxOrders"} {
		if _, ok := rate[key]; !ok {
			t.Fatalf("rateLimitDTO missing JSON key %q", key)
		}
	}
	if rate["maxOrders"] != float64(100) || rate["windowMs"] != float64(1000) {
		t.Fatalf("unexpected rate limit: %v", rate)
	}
	sizes, _ := m["orderSizeLimits"].([]any)
	size := sizes[0].(map[string]any)
	for _, key := range []string{"scope", "account", "asset", "maxQuantity", "maxNotional"} {
		if _, ok := size[key]; !ok {
			t.Fatalf("orderSizeLimitDTO missing JSON key %q", key)
		}
	}
	pnls, _ := m["pnlBoundsLimits"].([]any)
	pnl := pnls[0].(map[string]any)
	for _, key := range []string{"scope", "account", "asset", "lowerBound", "upperBound", "initialPnl"} {
		if _, ok := pnl[key]; !ok {
			t.Fatalf("pnlBoundsLimitDTO missing JSON key %q", key)
		}
	}
}

func TestAuditDTO_JSONShape(t *testing.T) {
	ts := time.Date(2026, 6, 11, 10, 0, 0, 1, time.UTC)
	row := domain.AuditRow{
		ExternalID: extID("audit-1"), At: ts, Actor: "operator",
		Action: domain.AuditActionSetLimit, Account: "acc-1",
		Detail: "set limit rate_limit asset=AAPL max_orders=100 window=1s",
	}
	b, err := json.Marshal(toAuditDTO(row))
	if err != nil {
		t.Fatal(err)
	}
	// Verify the at field is RFC3339Nano format.
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	atStr, _ := m["at"].(string)
	// time.Time marshals as RFC3339Nano; verify it parses back to the
	// same instant.
	got, err := time.Parse(time.RFC3339Nano, atStr)
	if err != nil {
		t.Fatalf("at field not RFC3339Nano: %v", atStr)
	}
	if !got.Equal(ts) {
		t.Fatalf("at round-trip mismatch: want %v, got %v", ts, got)
	}
}

func TestNewRouter_MissingService(t *testing.T) {
	_, err := NewRouter(Options{SPA: fakeSPA()})
	if err == nil {
		t.Fatal("want error for nil service")
	}
}

func TestNewRouter_MissingSPA(t *testing.T) {
	_, err := NewRouter(Options{Service: &fakeService{}})
	if err == nil {
		t.Fatal("want error for nil SPA")
	}
}

func TestCheckOrder_Pass(t *testing.T) {
	svc := &fakeService{checkResult: domain.CheckResult{
		Passed: true, WouldLockPrices: []string{"100"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1","price":"100"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/check", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	check, ok := m["check"].(map[string]any)
	if !ok {
		t.Fatalf("want check object, got %v", m["check"])
	}
	if check["passed"] != true {
		t.Fatalf("want passed=true, got %v", check["passed"])
	}
	prices, ok := check["wouldDisplayPrices"].([]any)
	if !ok || len(prices) != 1 || prices[0] != "100" {
		t.Fatalf("want wouldDisplayPrices=[100], got %v", check["wouldDisplayPrices"])
	}
	if check["wouldBlock"] != nil {
		t.Fatalf("want wouldBlock=null, got %v", check["wouldBlock"])
	}
}

func TestCheckOrder_Reject(t *testing.T) {
	svc := &fakeService{checkResult: domain.CheckResult{
		Passed: false,
		Rejects: []domain.OrderReject{
			{Code: "insufficient_funds", Scope: "account", Policy: "spot_funds", Reason: "no funds"},
		},
		WouldBlock: &domain.ExecutionAccountBlock{Account: "acc-1", Code: "account_blocked"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/check", body))
	// An engine reject is a successful 200, not an HTTP error.
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	check, _ := m["check"].(map[string]any)
	if check["passed"] != false {
		t.Fatalf("want passed=false, got %v", check["passed"])
	}
	rejects, ok := check["rejects"].([]any)
	if !ok || len(rejects) != 1 {
		t.Fatalf("want 1 reject, got %v", check["rejects"])
	}
	rej, _ := rejects[0].(map[string]any)
	if rej["code"] != "insufficient_funds" || rej["scope"] != "account" {
		t.Fatalf("reject fields not on the wire: %v", rej)
	}
	block, ok := check["wouldBlock"].(map[string]any)
	if !ok || block["account"] != "acc-1" || block["code"] != "account_blocked" {
		t.Fatalf("wouldBlock not on the wire: %v", check["wouldBlock"])
	}
}

func TestCheckOrder_InvalidJSON(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/check", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestCheckOrder_ValidationError(t *testing.T) {
	// The backend rejects a malformed account/asset with ErrInvalid; the handler
	// maps it to 400, mirroring submit.
	svc := &fakeService{stateErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"bad asset","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders/check", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestSubmitOrder_Created checks the submit path returns 201: the order row is
// persisted on every success path (even an engine reject), so the resource-
// creating POST is a 201 Created carrying the order.
func TestSubmitOrder_Created(t *testing.T) {
	svc := &fakeService{submitOrder: domain.Order{
		ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
		QuoteAsset: "USD", Side: domain.OrderSideBuy,
		Status: domain.OrderStatusCommitted,
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1","price":"100"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if _, ok := m["order"].(map[string]any); !ok {
		t.Fatalf("want order object, got %v", m["order"])
	}
}

// TestSubmitOrder_ValidationError checks malformed order input (a bad enum,
// decimal, or asset the engine mapper rejects with domain.ErrInvalid) surfaces
// as 400, not the 500 default. The fake stands in for the engine mapper raising
// ErrInvalid for, e.g., an unknown amount kind or a non-decimal amount.
func TestSubmitOrder_ValidationError(t *testing.T) {
	svc := &fakeService{stateErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"base","amountValue":"1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// TestApplyAdjustment_Created checks the adjustment path returns 201: the
// adjustment record is appended on every success path (accept or reject), so the
// resource-creating POST is a 201 Created carrying the record.
func TestApplyAdjustment_Created(t *testing.T) {
	svc := &fakeService{adjustment: domain.AccountAdjustmentRecord{
		ExternalID: extID("adj-1"), Account: "acc-1",
		Request: domain.AdjustmentRequest{Asset: "USD"},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"asset":"USD","balance":{"mode":"delta","value":"100"}}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/accounts/acc-1/adjustments", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	adj, ok := m["adjustment"].(map[string]any)
	if !ok {
		t.Fatalf("want adjustment object, got %v", m["adjustment"])
	}
	if adj["externalId"] != extID("adj-1").String() {
		t.Fatalf("want externalId=%s, got %v", extID("adj-1").String(), adj["externalId"])
	}
	assertNoSurrogateID(t, adj)
}

// TestApplyExecutionReport_Created checks the execution-report path returns 201:
// the trade row is created on the success path, so the resource-creating POST is
// a 201 Created carrying the result.
func TestApplyExecutionReport_Created(t *testing.T) {
	svc := &fakeService{orderDetail: domain.OrderDetail{
		Order: domain.Order{
			ExternalID: extID("order-1"), Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Side: domain.OrderSideBuy,
		},
	}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"quantity":"1","price":"100","force":true,"final":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/orders/"+extID("order-1").String()+"/execution-reports", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if _, ok := m["result"].(map[string]any); !ok {
		t.Fatalf("want result object, got %v", m["result"])
	}
	if !svc.execReportIn.Force {
		t.Fatal("force was not forwarded to ApplyExecutionReport")
	}
}

func TestListMcpAccess(t *testing.T) {
	svc := &fakeService{
		mcpCommands: []backend.McpCommand{
			{Command: mcpcatalog.Command{
				Name: "health", Title: "Health", AgentDescription: "desc",
				Implemented: true, DefaultEnabled: true,
			}, Enabled: false},
			{Command: mcpcatalog.Command{
				Name: "set_limit", Title: "Set limit", AgentDescription: "desc",
				Mutating: true, Protective: true,
			}, Enabled: true},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/mcp-access", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	commands, ok := m["commands"].([]any)
	if !ok || len(commands) != 2 {
		t.Fatalf("want 2 commands, got %v", m["commands"])
	}
	first := commands[0].(map[string]any)
	if first["name"] != "health" || first["enabled"] != false || first["implemented"] != true {
		t.Fatalf("unexpected first command: %v", first)
	}
}

func TestSetMcpAccess_Persists(t *testing.T) {
	svc := &fakeService{
		mcpCommands: []backend.McpCommand{
			{Command: mcpcatalog.Command{Name: "health", Title: "Health", AgentDescription: "d"}, Enabled: false},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"enabled":false}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/mcp-access/health", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if len(svc.setMcpCalls) != 1 || svc.setMcpCalls[0].command != "health" ||
		svc.setMcpCalls[0].enabled != false {
		t.Fatalf("expected persisted toggle, got %+v", svc.setMcpCalls)
	}
	m := bodyMap(t, rec.Result())
	cmd, ok := m["command"].(map[string]any)
	if !ok || cmd["name"] != "health" {
		t.Fatalf("expected command in body, got %v", m)
	}
}

func TestSetMcpAccess_UnknownCommandNotFound(t *testing.T) {
	svc := &fakeService{setMcpErr: fmt.Errorf("mcp command %q: %w", "nope", domain.ErrNotFound)}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"enabled":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/mcp-access/nope", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestSetMcpAccess_InvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/mcp-access/health", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	if len(svc.setMcpCalls) != 0 {
		t.Fatalf("invalid JSON must not persist")
	}
}

func TestUserSettings_GetAndPut(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}

	// Fresh store reports the welcome dialog as not yet dismissed.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/user-settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET want 200, got %d", rec.Code)
	}
	if m := bodyMap(t, rec.Result()); m["welcomeSeen"] != false {
		t.Fatalf("GET welcomeSeen = %v, want false", m["welcomeSeen"])
	}

	// Persisting the choice round-trips through the service.
	body := bytes.NewBufferString(`{"welcomeSeen":true}`)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/user-settings", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT want 200, got %d", rec.Code)
	}
	if !svc.welcomeSeen {
		t.Fatalf("PUT did not persist welcomeSeen")
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/user-settings", nil))
	if m := bodyMap(t, rec.Result()); m["welcomeSeen"] != true {
		t.Fatalf("GET after PUT welcomeSeen = %v, want true", m["welcomeSeen"])
	}
}

func TestUserSettings_PutInvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/user-settings", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}
