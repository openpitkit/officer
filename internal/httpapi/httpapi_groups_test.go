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
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// --- GET /api/v1/groups (handleListGroups) ----------------------------------

func TestListGroups(t *testing.T) {
	svc := &fakeService{
		groupRows: []store.GroupListRow{
			{
				Group:         domain.AccountGroup{Code: "grp-1", Notes: "team A"},
				AccountCount:  2,
				PositionCount: 3,
			},
			{
				Group: domain.AccountGroup{
					Code: "grp-2", Notes: "team B", Blocked: true, BlockReason: "risk",
				},
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/groups", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	groups, ok := m["groups"].([]any)
	if !ok || len(groups) != 2 {
		t.Fatalf("want 2 groups, got %v", m["groups"])
	}
	// Verify the wire shape carries the camelCase contract keys.
	first := groups[0].(map[string]any)
	for _, field := range []string{
		"code", "title", "notes", "blockReason", "blocked",
		"accountCount", "positionCount",
	} {
		if _, ok := first[field]; !ok {
			t.Fatalf("group missing field %q", field)
		}
	}
	if first["code"] != "grp-1" || first["blocked"] != false {
		t.Fatalf("unexpected first group: %v", first)
	}
	if first["accountCount"] != float64(2) || first["positionCount"] != float64(3) {
		t.Fatalf("unexpected counts: %v", first)
	}
	assertNoSurrogateID(t, first)
	second := groups[1].(map[string]any)
	if second["blocked"] != true || second["blockReason"] != "risk" {
		t.Fatalf("unexpected second group: %v", second)
	}
}

func TestListGroups_PropagatesFilters(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/groups?code=desk*&codeMatch=ends_with&status=active"+
			"&positionCountMode=less_than&positionCountMax=2"+
			"&accountCountMode=greater_than&accountCountMin=0"+
			"&notes=desk&notesMatch=contains&blockReason=halt&blockReasonMatch=contains",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := svc.groupFilter.Code.Fragments; !slices.Equal(got, []string{"desk", ""}) {
		t.Fatalf("code fragments = %v", got)
	}
	if svc.groupFilter.Code.AnchorStart || !svc.groupFilter.Code.AnchorEnd {
		t.Fatalf("code anchors = %+v, want end only", svc.groupFilter.Code)
	}
	if svc.groupFilter.Status != store.StatusFilterActive {
		t.Fatalf("status filter = %q", svc.groupFilter.Status)
	}
	if svc.groupFilter.Position.Max == nil || *svc.groupFilter.Position.Max != 2 ||
		!svc.groupFilter.Position.MaxExclusive {
		t.Fatalf("position filter = %+v", svc.groupFilter.Position)
	}
	if svc.groupFilter.Account.Min == nil || *svc.groupFilter.Account.Min != 0 ||
		!svc.groupFilter.Account.MinExclusive {
		t.Fatalf("account filter = %+v", svc.groupFilter.Account)
	}
	if got := svc.groupFilter.BlockReason.Fragments; !slices.Equal(got, []string{"halt"}) {
		t.Fatalf("block reason fragments = %v", got)
	}
}

func TestListGroups_AccountCountRange(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/groups?accountCountMode=between&accountCountMin=1&accountCountMax=5",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	got := svc.groupFilter.Account
	if got.Min == nil || *got.Min != 1 || got.Max == nil || *got.Max != 5 ||
		got.MinExclusive || got.MaxExclusive {
		t.Fatalf("account count range = %+v, want [1, 5]", got)
	}
}

func TestListGroups_TotalAndPaging(t *testing.T) {
	svc := &fakeService{
		groupRows: []store.GroupListRow{
			{Group: domain.AccountGroup{Code: "grp-1"}},
			{Group: domain.AccountGroup{Code: "grp-2"}},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/groups?sort=accountCount&order=desc&limit=5&offset=10",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if m["total"] != float64(2) {
		t.Fatalf("total = %v, want 2", m["total"])
	}
	if svc.groupFilter.Sort.Column != "accountCount" || !svc.groupFilter.Sort.Descending {
		t.Fatalf("sort spec = %+v", svc.groupFilter.Sort)
	}
	if svc.groupFilter.Page.Limit != 5 || svc.groupFilter.Page.Offset != 10 {
		t.Fatalf("page spec = %+v", svc.groupFilter.Page)
	}
}

func TestListGroups_BadSort(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/groups?sort=unknown", nil,
	))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestListGroups_BadLimit(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/groups?limit=-1", nil,
	))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestListGroups_Empty(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/groups", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	// A nil group slice must still serialize as an empty JSON array, never null.
	groups, ok := m["groups"].([]any)
	if !ok {
		t.Fatalf("want groups array, got %v", m["groups"])
	}
	if len(groups) != 0 {
		t.Fatalf("want 0 groups, got %d", len(groups))
	}
}

func TestListGroups_ServiceError(t *testing.T) {
	svc := &fakeService{groupErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/groups", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- POST /api/v1/groups (handleCreateGroup) --------------------------------

func TestCreateGroup(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"code":"grp-1","currency":"USD","notes":"desk one"}`,
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/groups", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	g, ok := m["group"].(map[string]any)
	if !ok {
		t.Fatalf("want group object, got %v", m["group"])
	}
	if g["code"] != "grp-1" || g["currency"] != "USD" || g["notes"] != "desk one" {
		t.Fatalf("unexpected group: %v", g)
	}
	assertNoSurrogateID(t, g)
	// CreateGroup appends to the seed set; one row must now exist.
	if len(svc.groups) != 1 ||
		svc.groups[0].Code != "grp-1" ||
		svc.groups[0].Currency != "USD" {
		t.Fatalf("expected group persisted, got %v", svc.groups)
	}
}

func TestCreateGroup_InvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/groups", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
	// A rejected body must never reach the service.
	if len(svc.groups) != 0 {
		t.Fatalf("invalid JSON must not persist, got %v", svc.groups)
	}
}

func TestCreateGroup_Conflict(t *testing.T) {
	svc := &fakeService{groupErr: domain.ErrAlreadyExists}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"grp-1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/groups", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "conflict" {
		t.Fatalf("want code=conflict, got %v", errObj["code"])
	}
}

func TestCreateGroup_ValidationError(t *testing.T) {
	// The backend rejects a malformed group id with ErrInvalid; the handler
	// maps the wrapped sentinel to 400 with the real message preserved.
	const msg = "group id must be non-empty"
	svc := &fakeService{
		groupErr: fmt.Errorf("%s: %w", msg, domain.ErrInvalid),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":""}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/groups", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
	if errObj["message"] != fmt.Sprintf("%s: %s", msg, domain.ErrInvalid) {
		t.Fatalf("want wrapped message, got %v", errObj["message"])
	}
}

func TestCreateGroup_ServiceError(t *testing.T) {
	svc := &fakeService{groupErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"grp-1"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/groups", body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- GET /api/v1/groups/{id} (handleGetGroup) -------------------------------

func TestGetGroup(t *testing.T) {
	svc := &fakeService{
		groups: []domain.AccountGroup{
			{Code: "grp-1", Notes: "desk one"},
		},
		// GetGroup returns the seeded accounts as the member list.
		accounts: []domain.Account{
			{Code: "acc-1"},
			{Code: "acc-2"},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/groups/grp-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	g, ok := m["group"].(map[string]any)
	if !ok {
		t.Fatalf("want group object, got %v", m["group"])
	}
	if g["code"] != "grp-1" || g["notes"] != "desk one" {
		t.Fatalf("unexpected group: %v", g)
	}
	assertNoSurrogateID(t, g)
	accounts, ok := m["accounts"].([]any)
	if !ok || len(accounts) != 2 {
		t.Fatalf("want 2 member accounts, got %v", m["accounts"])
	}
}

func TestGetGroup_NotFound(t *testing.T) {
	// No seeded groups: the fake's GetGroup lookup falls through to ErrNotFound.
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/groups/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

func TestGetGroup_URLEncodedID(t *testing.T) {
	// Verify percent-encoded characters in the group id are decoded before the
	// service lookup. %40 = '@'.
	svc := &fakeService{
		groups: []domain.AccountGroup{{Code: "grp@1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/groups/grp%401", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for URL-encoded id, got %d", rec.Code)
	}
}

func TestGetGroup_ServiceError(t *testing.T) {
	svc := &fakeService{groupErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/groups/grp-1", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

func TestSetGroupCurrency(t *testing.T) {
	svc := &fakeService{
		groups: []domain.AccountGroup{{Code: "grp-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"currency":"USD"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/groups/grp-1/currency", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	g, ok := m["group"].(map[string]any)
	if !ok {
		t.Fatalf("want group object, got %v", m["group"])
	}
	if g["code"] != "grp-1" || g["currency"] != "USD" {
		t.Fatalf("unexpected group currency response: %v", g)
	}
}

func TestSetGroupCurrency_ServiceInvalid(t *testing.T) {
	svc := &fakeService{
		groups:   []domain.AccountGroup{{Code: "grp-1"}},
		groupErr: domain.ErrInvalid,
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"currency":"NOPE"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/groups/grp-1/currency", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestSetDefaultGroupCurrency(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"currency":"USD"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/groups/-/default/currency", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	g, ok := m["group"].(map[string]any)
	if !ok {
		t.Fatalf("want group object, got %v", m["group"])
	}
	if g["code"] != "" || g["currency"] != "USD" {
		t.Fatalf("unexpected default group currency response: %v", g)
	}
}

func TestSetDefaultGroupCurrency_ServiceInvalid(t *testing.T) {
	svc := &fakeService{groupErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"currency":"NOPE"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/groups/-/default/currency", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestSetGroupCurrency_AllowsLiteralDefaultGroupCode(t *testing.T) {
	svc := &fakeService{
		groups: []domain.AccountGroup{
			{Code: "default"},
			{Code: "", Currency: "EUR"},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"currency":"USD"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/groups/default/currency", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	g, ok := m["group"].(map[string]any)
	if !ok {
		t.Fatalf("want group object, got %v", m["group"])
	}
	if g["code"] != "default" || g["currency"] != "USD" {
		t.Fatalf("literal default group response = %v", g)
	}
	if svc.groups[1].Currency != "EUR" {
		t.Fatalf("reserved default currency changed to %q", svc.groups[1].Currency)
	}
}

// --- PUT /api/v1/groups/{id}/notes (handleSetGroupNotes) --------------------

func TestSetGroupNotes(t *testing.T) {
	// The group must already exist: after the no-op setter, writeGroup re-reads
	// it via GetGroup and serializes the current server state.
	svc := &fakeService{
		groups: []domain.AccountGroup{{Code: "grp-1", Notes: "old"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"notes":"updated"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/groups/grp-1/notes", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	g, ok := m["group"].(map[string]any)
	if !ok || g["code"] != "grp-1" {
		t.Fatalf("want group object, got %v", m["group"])
	}
}

func TestSetGroupNotes_InvalidJSON(t *testing.T) {
	svc := &fakeService{
		groups: []domain.AccountGroup{{Code: "grp-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/groups/grp-1/notes", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestSetGroupNotes_NotFound(t *testing.T) {
	// SetGroupNotes succeeds (no groupErr), but writeGroup's re-read finds no
	// such group and surfaces 404.
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"notes":"x"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/groups/missing/notes", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

func TestSetGroupNotes_ServiceError(t *testing.T) {
	// groupErr makes the setter itself fail before writeGroup runs.
	svc := &fakeService{groupErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"notes":"x"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/api/v1/groups/grp-1/notes", body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- POST /api/v1/groups/{id}/block (handleBlockGroup) ----------------------

func TestBlockGroup(t *testing.T) {
	svc := &fakeService{
		groups: []domain.AccountGroup{{Code: "grp-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"reason":"compliance"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/groups/grp-1/block", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if _, ok := m["group"].(map[string]any); !ok {
		t.Fatalf("want group object, got %v", m["group"])
	}
}

func TestBlockGroup_InvalidJSON(t *testing.T) {
	svc := &fakeService{
		groups: []domain.AccountGroup{{Code: "grp-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/groups/grp-1/block", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestBlockGroup_NotFound(t *testing.T) {
	// The block setter succeeds, then writeGroup's re-read 404s on the absent id.
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"reason":"x"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/groups/missing/block", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestBlockGroup_ServiceError(t *testing.T) {
	svc := &fakeService{groupErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"reason":"x"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/groups/grp-1/block", body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- POST /api/v1/groups/{id}/unblock (handleUnblockGroup) ------------------

func TestUnblockGroup(t *testing.T) {
	// Unblock takes no body; writeGroup re-reads the seeded group.
	svc := &fakeService{
		groups: []domain.AccountGroup{
			{Code: "grp-1", Blocked: true, BlockReason: "risk"},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/groups/grp-1/unblock", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if _, ok := m["group"].(map[string]any); !ok {
		t.Fatalf("want group object, got %v", m["group"])
	}
}

func TestUnblockGroup_NotFound(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/groups/missing/unblock", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestUnblockGroup_ServiceError(t *testing.T) {
	svc := &fakeService{groupErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/groups/grp-1/unblock", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}

// --- DELETE /api/v1/groups/{id} (handleDeleteGroup) -------------------------

func TestDeleteGroup(t *testing.T) {
	svc := &fakeService{
		groups: []domain.AccountGroup{{Code: "grp-1"}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/groups/grp-1", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	// A 204 carries no body.
	if rec.Body.Len() != 0 {
		t.Fatalf("want empty body, got %q", rec.Body.String())
	}
}

func TestDeleteGroup_NotFound(t *testing.T) {
	svc := &fakeService{groupErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/groups/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("want code=not_found, got %v", errObj["code"])
	}
}

func TestDeleteGroup_ServiceError(t *testing.T) {
	svc := &fakeService{groupErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/v1/groups/grp-1", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("want code=internal, got %v", errObj["code"])
	}
}
