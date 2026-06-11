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
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
	"go.openpit.dev/officer/internal/node"
	"go.openpit.dev/officer/internal/store"
)

// fakeService is a fake Service for handler tests.
type fakeService struct {
	accounts    []domain.Account
	limits      []domain.Limit
	auditRows   []domain.AuditRow
	groups      []domain.AccountGroup
	balances    []domain.Balance
	adjustments []domain.AccountAdjustmentRecord
	orders      []domain.Order
	trades      []domain.Trade
	orderDetail domain.OrderDetail
	adjustment  domain.AccountAdjustmentRecord
	submitOrder domain.Order
	overview    backend.Overview
	serviceInfo backend.ServiceInfo
	status      backend.Status
	statusErr   error
	createErr   error
	blockErr    error
	unblockErr  error
	stateErr    error
	listLimErr  error
	putLimErr   error
	delLimErr   error
	auditErr    error
	groupErr    error
}

func (f *fakeService) Status(_ context.Context) (backend.Status, error) {
	return f.status, f.statusErr
}
func (f *fakeService) ListAccounts(_ context.Context) ([]domain.Account, error) {
	return f.accounts, nil
}
func (f *fakeService) CreateAccount(_ context.Context, id domain.AccountID) (domain.Account, error) {
	if f.createErr != nil {
		return domain.Account{}, f.createErr
	}
	return domain.Account{ID: id, Tenant: domain.DefaultTenant}, nil
}
func (f *fakeService) GetAccountState(_ context.Context, id domain.AccountID) (domain.Account, []domain.Limit, error) {
	if f.stateErr != nil {
		return domain.Account{}, nil, f.stateErr
	}
	for _, a := range f.accounts {
		if a.ID == id {
			return a, f.limits, nil
		}
	}
	return domain.Account{}, nil, domain.ErrNotFound
}
func (f *fakeService) BlockAccount(_ context.Context, _ domain.AccountID, _ string) error {
	return f.blockErr
}
func (f *fakeService) UnblockAccount(_ context.Context, _ domain.AccountID) error {
	return f.unblockErr
}
func (f *fakeService) ListLimits(_ context.Context, _ domain.AccountID) ([]domain.Limit, error) {
	return f.limits, f.listLimErr
}
func (f *fakeService) PutLimit(_ context.Context, _ domain.Limit) error {
	return f.putLimErr
}
func (f *fakeService) DeleteLimit(_ context.Context, _ domain.LimitTarget) error {
	return f.delLimErr
}
func (f *fakeService) ListAudit(_ context.Context, _ int) ([]domain.AuditRow, error) {
	return f.auditRows, f.auditErr
}
func (f *fakeService) ListAuditFiltered(
	_ context.Context, _ domain.AccountID, _ domain.Source, _ int,
) ([]domain.AuditRow, error) {
	return f.auditRows, f.auditErr
}
func (f *fakeService) SetAccountGroup(_ context.Context, _ domain.AccountID, _ string) error {
	return f.stateErr
}
func (f *fakeService) SetAccountNotes(_ context.Context, _ domain.AccountID, _ string) error {
	return f.stateErr
}
func (f *fakeService) CreateGroup(_ context.Context, g domain.AccountGroup) error {
	if f.groupErr != nil {
		return f.groupErr
	}
	f.groups = append(f.groups, g)
	return nil
}
func (f *fakeService) ListGroups(_ context.Context) ([]domain.AccountGroup, error) {
	return f.groups, f.groupErr
}
func (f *fakeService) GetGroup(
	_ context.Context, id string,
) (domain.AccountGroup, []domain.Account, error) {
	if f.groupErr != nil {
		return domain.AccountGroup{}, nil, f.groupErr
	}
	for _, g := range f.groups {
		if g.ID == id {
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
	_ context.Context, _ domain.AccountID, _ domain.AdjustmentRequest,
) (domain.AccountAdjustmentRecord, error) {
	return f.adjustment, f.stateErr
}
func (f *fakeService) ListBalances(
	_ context.Context, _ domain.AccountID, _ string,
) ([]domain.Balance, error) {
	return f.balances, nil
}
func (f *fakeService) ListAdjustments(
	_ context.Context, _ domain.AccountID, _ domain.Source, _ int,
) ([]domain.AccountAdjustmentRecord, error) {
	return f.adjustments, f.stateErr
}
func (f *fakeService) ListAllAdjustments(
	_ context.Context, _ domain.AccountID, _ domain.Source, _ int,
) ([]domain.AccountAdjustmentRecord, error) {
	return f.adjustments, nil
}
func (f *fakeService) SubmitOrder(_ context.Context, _ domain.Order) (domain.Order, error) {
	return f.submitOrder, f.stateErr
}
func (f *fakeService) ApplyExecutionReport(
	_ context.Context, _ domain.ExecutionReportInput,
) (engine.ExecutionReportResult, error) {
	return engine.ExecutionReportResult{}, f.stateErr
}
func (f *fakeService) GetOrder(_ context.Context, _ int64) (domain.OrderDetail, error) {
	return f.orderDetail, f.stateErr
}
func (f *fakeService) ListOrders(
	_ context.Context, _ domain.AccountID, _ domain.Source, _ int,
) ([]domain.Order, error) {
	return f.orders, nil
}
func (f *fakeService) ListTrades(
	_ context.Context, _ domain.AccountID, _ domain.Source, _ int,
) ([]domain.Trade, error) {
	return f.trades, nil
}
func (f *fakeService) Overview(_ context.Context) (backend.Overview, error) {
	return f.overview, f.statusErr
}
func (f *fakeService) ServiceInfo(_ context.Context) (backend.ServiceInfo, error) {
	return f.serviceInfo, f.statusErr
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

func TestListAccounts(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{
			{ID: "acc-1", Tenant: domain.DefaultTenant, Blocked: false},
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
	if a["id"] != "acc-1" {
		t.Fatalf("want id=acc-1, got %v", a["id"])
	}
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
	body := bytes.NewBufferString(`{"id":"acc-1"}`)
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
	if acc["id"] != "acc-1" {
		t.Fatalf("want id=acc-1, got %v", acc["id"])
	}
}

func TestCreateAccount_ValidationError(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	// Empty id fails ValidateAccountID
	body := bytes.NewBufferString(`{"id":""}`)
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
	body := bytes.NewBufferString(`{"id":"acc-1"}`)
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

func TestGetAccount(t *testing.T) {
	svc := &fakeService{
		accounts: []domain.Account{
			{ID: "acc-1", Tenant: domain.DefaultTenant},
		},
		limits: []domain.Limit{
			{
				Target: domain.LimitTarget{
					Policy:  domain.PolicyRateLimit,
					Scope:   domain.ScopeAccount,
					Account: "acc-1",
					Tenant:  domain.DefaultTenant,
				},
				Values: []domain.LimitValue{
					{Kind: domain.KindMaxOrders, Value: "100"},
					{Kind: domain.KindWindow, Value: "1s"},
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
	if _, ok := m["account"]; !ok {
		t.Fatal("missing account field")
	}
	limits, _ := m["limits"].([]any)
	if len(limits) != 1 {
		t.Fatalf("want 1 limit, got %d", len(limits))
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
			{ID: "acc@1", Tenant: domain.DefaultTenant},
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
		accounts: []domain.Account{{ID: "acc-1", Tenant: domain.DefaultTenant}},
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
		accounts: []domain.Account{{ID: "acc-1", Tenant: domain.DefaultTenant}},
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
		limits: []domain.Limit{
			{
				Target: domain.LimitTarget{
					Policy: domain.PolicyOrderSizeLimit,
					Scope:  domain.ScopeBroker,
					Tenant: domain.DefaultTenant,
				},
				Values: []domain.LimitValue{
					{Kind: domain.KindMaxQuantity, Value: "500"},
				},
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
	limits, _ := m["limits"].([]any)
	if len(limits) != 1 {
		t.Fatalf("want 1 limit, got %v", m["limits"])
	}
	// Verify the limit shape has the camelCase keys contract requires.
	l := limits[0].(map[string]any)
	for _, field := range []string{"policy", "scope", "account", "asset", "values"} {
		if _, ok := l[field]; !ok {
			t.Fatalf("limit missing field %q", field)
		}
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

func TestPutLimit(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"policy":"rate_limit","scope":"account_asset",
		"account":"acc-1","asset":"BTC",
		"values":{"max_orders":"100","window":"1s"}
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if _, ok := m["limit"]; !ok {
		t.Fatal("response missing limit field")
	}
}

func TestPutLimit_ValidationError(t *testing.T) {
	svc := &fakeService{putLimErr: fmt.Errorf("bad scope: %w", domain.ErrInvalid)}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{
		"policy":"rate_limit","scope":"broker",
		"values":{"max_orders":"100","window":"1s"}
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestPutLimit_NotImplemented(t *testing.T) {
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
	body := bytes.NewBufferString(`{
		"policy":"rate_limit","scope":"asset","asset":"BTC",
		"values":{"max_orders":"100","window":"1s"}
	}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/limits", body))
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

func TestDeleteLimit(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/limits?policy=rate_limit&scope=broker", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
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

func TestListAudit(t *testing.T) {
	ts := time.Date(2026, 6, 11, 10, 0, 0, 1, time.UTC)
	svc := &fakeService{
		auditRows: []domain.AuditRow{
			{
				ID:      12,
				At:      ts,
				Actor:   "operator",
				Action:  domain.AuditActionSetLimit,
				Account: "acc-1",
				Detail:  "set limit rate_limit account=acc-1",
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
	for _, field := range []string{"id", "at", "actor", "action", "account", "detail"} {
		if _, ok := e[field]; !ok {
			t.Fatalf("audit entry missing field %q", field)
		}
	}
	if e["actor"] != "operator" {
		t.Fatalf("want actor=operator, got %v", e["actor"])
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

func TestLimitDTO_JSONShape(t *testing.T) {
	l := domain.Limit{
		Target: domain.LimitTarget{
			Policy:  domain.PolicyRateLimit,
			Scope:   domain.ScopeAccountAsset,
			Account: "acc-1",
			Asset:   "BTC",
			Tenant:  domain.DefaultTenant,
		},
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "100"},
			{Kind: domain.KindWindow, Value: "1s"},
		},
	}
	dto := toLimitDTO(l)
	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"policy", "scope", "account", "asset", "values"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("limitDTO missing JSON key %q", key)
		}
	}
	vals, _ := m["values"].(map[string]any)
	if vals["max_orders"] != "100" || vals["window"] != "1s" {
		t.Fatalf("unexpected values: %v", vals)
	}
}

func TestAuditDTO_JSONShape(t *testing.T) {
	ts := time.Date(2026, 6, 11, 10, 0, 0, 1, time.UTC)
	row := domain.AuditRow{
		ID: 12, At: ts, Actor: "operator",
		Action: domain.AuditActionSetLimit, Account: "acc-1",
		Detail: "set limit rate_limit asset=BTC max_orders=100 window=1s",
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
