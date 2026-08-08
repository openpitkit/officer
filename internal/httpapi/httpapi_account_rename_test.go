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
	"net/http"
	"net/http/httptest"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	appsigning "go.openpit.dev/officer/internal/signing"
	"go.openpit.dev/officer/internal/store/sqlite"
)

func TestUpdateAccountRenamesIdentityWithDependentsAndImmutableAudit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := sqlite.New(t.TempDir() + "/account-rename.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	for _, asset := range []string{"AAPL", "USD"} {
		if err := realm.CreateAsset(ctx, domain.Asset{Code: asset}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", asset, err)
		}
	}
	created, err := realm.CreateAccount(ctx, domain.Account{
		Code: "account-old", Title: "Before",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := realm.AppendAudit(ctx, store.AuditEntry{
		Action:  domain.AuditActionBlock,
		Account: created.Code,
		Detail:  "unrelated pre-rename audit row",
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	if err := realm.UpsertBalance(ctx, domain.Balance{
		Account: created.Code, Asset: "AAPL", Available: "2",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	order, err := realm.CreateOrder(ctx, domain.Order{
		Account:     created.Code,
		Source:      domain.SourceAPI,
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       "10",
		Status:      domain.OrderStatusSubmitted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	n, _, err := node.NewLocalNode(ctx, st, func(snapshot engine.Snapshot) (engine.Engine, error) {
		return newAccountRenameHTTPTestEngine(snapshot), nil
	})
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	router, err := node.NewLocalRouter(n)
	if err != nil {
		t.Fatalf("NewLocalRouter: %v", err)
	}
	signer, err := appsigning.New(realm)
	if err != nil {
		t.Fatalf("signing.New: %v", err)
	}
	svc := backend.New(router, nil, signer)
	handler, err := newRouter(svc)
	if err != nil {
		t.Fatalf("newRouter: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPut,
		"/api/v1/accounts/account-old",
		bytes.NewBufferString(`{"code":"account-new","title":"After"}`),
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	updated, ok, err := realm.GetAccount(ctx, "account-new")
	if err != nil || !ok {
		t.Fatalf("GetAccount(new) = %+v, %v, %v", updated, ok, err)
	}
	if updated.EngineAccountID != created.EngineAccountID {
		t.Fatalf("engine account id changed: %d -> %d", created.EngineAccountID, updated.EngineAccountID)
	}
	balance, ok, err := realm.GetBalance(ctx, updated.Code, "AAPL")
	if err != nil || !ok || balance.Available != "2" {
		t.Fatalf("renamed balance = %+v, %v, %v", balance, ok, err)
	}
	detail, err := realm.GetOrder(ctx, order.ExternalID)
	if err != nil || detail.Order.Account != updated.Code {
		t.Fatalf("renamed order = %+v, %v", detail.Order, err)
	}

	assertAccountRenameAudit(t, ctx, realm, "account-old", "Before")
	assertAccountRenameAudit(t, ctx, realm, "account-new", "After")

	auditRec := httptest.NewRecorder()
	handler.ServeHTTP(auditRec, httptest.NewRequest(
		http.MethodGet, "/api/v1/audit?account=account-new&limit=1000", nil,
	))
	if auditRec.Code != http.StatusOK {
		t.Fatalf("audit status = %d, want 200: %s", auditRec.Code, auditRec.Body.String())
	}
	auditBody := bodyMap(t, auditRec.Result())
	entries, _ := auditBody["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("new-code audit pulled old snapshots: %+v", entries)
	}
	entry, _ := entries[0].(map[string]any)
	if entry["account"] != "account-new" || entry["action"] != "update_account" {
		t.Fatalf("new-code audit entry = %+v", entry)
	}

	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, httptest.NewRequest(
		http.MethodDelete, "/api/v1/accounts/account-new?force=true", nil,
	))
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204: %s", deleteRec.Code, deleteRec.Body.String())
	}
	assertAccountRenameAudit(t, ctx, realm, "account-old", "Before")
	assertAccountRenameAudit(t, ctx, realm, "account-new", "After")
}

func assertAccountRenameAudit(
	t *testing.T,
	ctx context.Context,
	realm store.RealmStore,
	code domain.AccountID,
	title string,
) {
	t.Helper()
	detail := "update account account-old -> account-new (record under new code)"
	if code == "account-old" {
		detail = "update account account-old -> account-new (record under old code)"
	}
	page, err := realm.ListAuditRows(ctx, store.AuditListFilter{
		Account: store.ExactTextMatcher(code.String()),
		Actions: []domain.AuditAction{domain.AuditActionUpdateAccount},
	})
	if err != nil {
		t.Fatalf("ListAuditRows(%q): %v", code, err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Account != code ||
		page.Rows[0].AccountTitle != title ||
		page.Rows[0].Detail != detail {
		t.Fatalf("rename audit under %q = %+v", code, page.Rows)
	}
}

type accountRenameHTTPTestEngine struct {
	*realGateEngine
	accounts map[domain.AccountID]domain.EngineAccountID
}

func newAccountRenameHTTPTestEngine(snapshot engine.Snapshot) *accountRenameHTTPTestEngine {
	accounts := make(map[domain.AccountID]domain.EngineAccountID, len(snapshot.Accounts))
	for _, account := range snapshot.Accounts {
		accounts[account.Code] = account.EngineAccountID
	}
	return &accountRenameHTTPTestEngine{
		realGateEngine: &realGateEngine{running: true},
		accounts:       accounts,
	}
}

func (e *accountRenameHTTPTestEngine) AddAccountResolverEntry(account domain.Account) error {
	e.accounts[account.Code] = account.EngineAccountID
	return nil
}

func (e *accountRenameHTTPTestEngine) RenameAccountResolverEntry(
	oldCode domain.AccountID,
	account domain.Account,
) error {
	engineID, ok := e.accounts[oldCode]
	if !ok || engineID != account.EngineAccountID {
		return domain.ErrInvalid
	}
	delete(e.accounts, oldCode)
	e.accounts[account.Code] = engineID
	return nil
}

func (*accountRenameHTTPTestEngine) AddGroupResolverEntry(domain.AccountGroup) error {
	return nil
}

func (*accountRenameHTTPTestEngine) RenameGroupResolverEntry(
	string,
	domain.AccountGroup,
) error {
	return nil
}

func (*accountRenameHTTPTestEngine) RemoveGroupResolverEntry(domain.AccountGroup) error {
	return nil
}
