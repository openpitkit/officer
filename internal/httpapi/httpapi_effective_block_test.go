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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func newBlockTestRequest(method, path, body string) *http.Request {
	if body == "" {
		return httptest.NewRequest(method, path, http.NoBody)
	}
	return httptest.NewRequest(method, path, bytes.NewBufferString(body))
}

// blockedGroupService serves one blocked group with four accounts: one clear
// member, one member blocked on its own, one that belongs to no group, and one
// naming a group the dictionary does not have.
func blockedGroupService() *fakeService {
	return &fakeService{
		groups: []domain.AccountGroup{
			{Code: "desk", Blocked: true, BlockReason: "desk halt"},
			{Code: "clear-desk"},
		},
		accounts: []domain.Account{
			{Code: "member", GroupCode: "desk"},
			{
				Code: "own-block", GroupCode: "desk",
				Blocked: true, BlockReason: "own halt",
			},
			{Code: "loner"},
			{Code: "ghost", GroupCode: "vanished"},
		},
	}
}

func accountByCode(t *testing.T, list []any, code string) map[string]any {
	t.Helper()
	for _, entry := range list {
		account, ok := entry.(map[string]any)
		if ok && account["code"] == code {
			return account
		}
	}
	t.Fatalf("account %q missing from %v", code, list)
	return nil
}

// assertBlockFields checks the six effective-block fields of an account DTO.
func assertBlockFields(
	t *testing.T,
	account map[string]any,
	blocked bool,
	reason string,
	source string,
	accountBlocked bool,
	groupBlocked bool,
) {
	t.Helper()
	if account["blocked"] != blocked {
		t.Fatalf("blocked = %v, want %v (%v)", account["blocked"], blocked, account)
	}
	if account["blockReason"] != reason {
		t.Fatalf("blockReason = %v, want %q", account["blockReason"], reason)
	}
	if account["blockSource"] != source {
		t.Fatalf("blockSource = %v, want %q", account["blockSource"], source)
	}
	if account["accountBlocked"] != accountBlocked {
		t.Fatalf("accountBlocked = %v, want %v", account["accountBlocked"], accountBlocked)
	}
	if account["groupBlocked"] != groupBlocked {
		t.Fatalf("groupBlocked = %v, want %v", account["groupBlocked"], groupBlocked)
	}
}

// A member of a blocked group cannot trade, so the account list must report it
// blocked rather than showing the account's own clear flag.
func TestListAccountsReportsEffectiveBlock(t *testing.T) {
	svc := blockedGroupService()
	svc.accountRows = []store.AccountListRow{
		{Account: svc.accounts[0]},
		{Account: svc.accounts[1]},
		{Account: svc.accounts[2]},
		{Account: svc.accounts[3]},
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
	accounts, ok := bodyMap(t, rec.Result())["accounts"].([]any)
	if !ok || len(accounts) != 4 {
		t.Fatalf("want 4 accounts, got %v", accounts)
	}

	member := accountByCode(t, accounts, "member")
	assertBlockFields(t, member, true, "desk halt", "group", false, true)
	if member["groupBlockReason"] != "desk halt" {
		t.Fatalf("groupBlockReason = %v", member["groupBlockReason"])
	}
	if member["accountBlockReason"] != "" {
		t.Fatalf("accountBlockReason = %v, want empty", member["accountBlockReason"])
	}

	// The account's own block wins reason attribution, as it does in the engine.
	own := accountByCode(t, accounts, "own-block")
	assertBlockFields(t, own, true, "own halt", "account", true, true)
	if own["groupBlockReason"] != "desk halt" {
		t.Fatalf("groupBlockReason = %v", own["groupBlockReason"])
	}

	loner := accountByCode(t, accounts, "loner")
	assertBlockFields(t, loner, false, "", "none", false, false)

	// A group code with no row behind it must contribute no block: the missing
	// group resolves to the zero value, not to a blocked one.
	ghost := accountByCode(t, accounts, "ghost")
	assertBlockFields(t, ghost, false, "", "none", false, false)
}

// The single-account path resolves the named group on its own, so an account
// whose group was deleted must still read back as unblocked rather than fail.
func TestGetAccountWithUnknownGroupIsNotBlocked(t *testing.T) {
	svc := blockedGroupService()
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/accounts/ghost", nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	account, ok := bodyMap(t, rec.Result())["account"].(map[string]any)
	if !ok {
		t.Fatal("missing account")
	}
	assertBlockFields(t, account, false, "", "none", false, false)
}

func TestGetAccountReportsEffectiveBlock(t *testing.T) {
	svc := blockedGroupService()
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/accounts/member", nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	account, ok := bodyMap(t, rec.Result())["account"].(map[string]any)
	if !ok {
		t.Fatal("missing account")
	}
	assertBlockFields(t, account, true, "desk halt", "group", false, true)
}

// The per-account unblock writes only the account tier, so the response must
// keep reporting the group block instead of claiming the account can trade.
func TestUnblockAccountKeepsGroupBlockVisible(t *testing.T) {
	svc := blockedGroupService()
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/accounts/member/unblock?missingAccount=reject",
		http.NoBody,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	account, ok := bodyMap(t, rec.Result())["account"].(map[string]any)
	if !ok {
		t.Fatal("missing account")
	}
	assertBlockFields(t, account, true, "desk halt", "group", false, true)
}

func TestGetGroupMembersReportEffectiveBlock(t *testing.T) {
	svc := blockedGroupService()
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/groups/desk", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	accounts, ok := bodyMap(t, rec.Result())["accounts"].([]any)
	if !ok || len(accounts) == 0 {
		t.Fatalf("want group members, got %v", accounts)
	}
	assertBlockFields(
		t, accountByCode(t, accounts, "member"), true, "desk halt", "group",
		false, true,
	)
	assertBlockFields(
		t, accountByCode(t, accounts, "own-block"), true, "own halt", "account",
		true, true,
	)
}

// --- reserved default group -------------------------------------------------

// The default group is addressed as "-" and is not an operator-owned group, so
// every /groups/{code} operation aimed at it is categorically forbidden - the
// read included, since the sentinel means the same thing on every such route.
// The fake service would answer 404 for the literal code, so a 403 also proves
// the handler refused before reaching the control plane.
func TestReservedGroupOperationsAreForbidden(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"get", http.MethodGet, "/api/v1/groups/-", ""},
		{"update", http.MethodPut, "/api/v1/groups/-", `{"code":"x","title":"X"}`},
		{"delete", http.MethodDelete, "/api/v1/groups/-", ""},
		{
			"currency", http.MethodPut, "/api/v1/groups/-/currency",
			`{"currency":"USD"}`,
		},
		{"notes", http.MethodPut, "/api/v1/groups/-/notes", `{"notes":"n"}`},
		{"block", http.MethodPost, "/api/v1/groups/-/block", `{"reason":"halt"}`},
		{"unblock", http.MethodPost, "/api/v1/groups/-/unblock", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, newBlockTestRequest(tc.method, tc.path, tc.body))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("want 403, got %d (%s)", rec.Code, rec.Body.String())
			}
			body := bodyMap(t, rec.Result())
			errObj, ok := body["error"].(map[string]any)
			if !ok {
				t.Fatalf("missing error envelope: %v", body)
			}
			if errObj["code"] != "reserved_group" {
				t.Fatalf("error code = %v, want reserved_group", errObj["code"])
			}
		})
	}
}

// The sentinel refusal must not swallow the one route that legitimately
// addresses the default group: it sits a segment deeper and never reads the
// {code} parameter the guard inspects.
func TestReservedGroupGuardKeepsDefaultCurrencyRoute(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, newBlockTestRequest(
		http.MethodPut, "/api/v1/groups/-/default/currency",
		`{"currency":"USD"}`,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	group, ok := bodyMap(t, rec.Result())["group"].(map[string]any)
	if !ok || group["code"] != "" || group["currency"] != "USD" {
		t.Fatalf("default group response = %v", group)
	}
	if len(svc.groups) != 1 || svc.groups[0].Currency != "USD" {
		t.Fatalf("default currency not written: %v", svc.groups)
	}
}

// A reserved-group refusal raised by the engine seam carries the same typed
// error code as the path guard instead of leaking a generic failure.
func TestGroupBlockMapsEngineReservedGroupError(t *testing.T) {
	svc := &fakeService{
		groups:   []domain.AccountGroup{{Code: "desk"}},
		groupErr: domain.ErrReservedGroup,
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, newBlockTestRequest(
		http.MethodPost, "/api/v1/groups/desk/block", `{"reason":"halt"}`,
	))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d (%s)", rec.Code, rec.Body.String())
	}
	errObj, ok := bodyMap(t, rec.Result())["error"].(map[string]any)
	if !ok || errObj["code"] != "reserved_group" {
		t.Fatalf("error envelope = %v", errObj)
	}
}

// --- strict request bodies --------------------------------------------------

// A control-plane mutation must not acknowledge an intent it did not carry
// out: an unknown member is refused instead of silently dropped, and nothing
// is created or modified.
func TestAccountAndGroupMutationsRejectUnknownFields(t *testing.T) {
	t.Run("account create", func(t *testing.T) {
		svc := &fakeService{}
		r, err := newRouter(svc)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, newBlockTestRequest(
			http.MethodPost, "/api/v1/accounts",
			`{"code":"acc-1","title":"One","blocked":true}`,
		))
		assertUnknownFieldRejected(t, rec, "blocked")
		if len(svc.createdAccounts) != 0 {
			t.Fatalf("rejected request created accounts: %v", svc.createdAccounts)
		}
	})

	t.Run("account notes", func(t *testing.T) {
		svc := &fakeService{accounts: []domain.Account{{Code: "acc-1"}}}
		r, err := newRouter(svc)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, newBlockTestRequest(
			http.MethodPut, "/api/v1/accounts/acc-1/notes",
			`{"notes":"desk","blockReason":"halt"}`,
		))
		assertUnknownFieldRejected(t, rec, "blockReason")
		if len(svc.accountNotesWrites) != 0 {
			t.Fatalf("rejected request wrote notes: %v", svc.accountNotesWrites)
		}
	})

	t.Run("group create", func(t *testing.T) {
		svc := &fakeService{}
		r, err := newRouter(svc)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, newBlockTestRequest(
			http.MethodPost, "/api/v1/groups",
			`{"code":"desk","title":"Desk","blocked":true}`,
		))
		assertUnknownFieldRejected(t, rec, "blocked")
		if len(svc.groups) != 0 {
			t.Fatalf("rejected request created groups: %v", svc.groups)
		}
	})

	t.Run("group currency", func(t *testing.T) {
		svc := &fakeService{groups: []domain.AccountGroup{{Code: "desk"}}}
		r, err := newRouter(svc)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, newBlockTestRequest(
			http.MethodPut, "/api/v1/groups/desk/currency",
			`{"currency":"USD","notes":"x"}`,
		))
		assertUnknownFieldRejected(t, rec, "notes")
		if svc.groups[0].Currency != "" {
			t.Fatalf("rejected request set currency: %q", svc.groups[0].Currency)
		}
	})
}

// assertUnknownFieldRejected checks the typed 400 envelope naming the member.
func assertUnknownFieldRejected(
	t *testing.T, rec *httptest.ResponseRecorder, field string,
) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	errObj, ok := bodyMap(t, rec.Result())["error"].(map[string]any)
	if !ok || errObj["code"] != "validation" {
		t.Fatalf("error envelope = %v", errObj)
	}
	message, _ := errObj["message"].(string)
	if !strings.Contains(message, field) {
		t.Fatalf("message %q must name the rejected field %q", message, field)
	}
}

// The sentinel is not a legal group code: every group route addresses "-" as
// the realm default group, so a group carrying it would be created and then be
// unreachable for rename, delete, block, and unblock. It is refused as a
// malformed value on both the create and the rename path.
func TestReservedGroupCodeIsRejected(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"create", http.MethodPost, "/api/v1/groups", `{"code":"-","title":"Dash"}`},
		{
			"rename", http.MethodPut, "/api/v1/groups/desk",
			`{"code":"-","title":"Dash"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{groups: []domain.AccountGroup{{Code: "desk"}}}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, newBlockTestRequest(tc.method, tc.path, tc.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
			}
			errObj, ok := bodyMap(t, rec.Result())["error"].(map[string]any)
			if !ok || errObj["code"] != "validation" {
				t.Fatalf("error envelope = %v", errObj)
			}
			message, _ := errObj["message"].(string)
			if !strings.Contains(message, "reserved") {
				t.Fatalf("message %q must state that the code is reserved", message)
			}
		})
	}
}
