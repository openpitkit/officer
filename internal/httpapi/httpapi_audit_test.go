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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func TestListAudit_PropagatesFilters(t *testing.T) {
	auditID := extID("audit-exact")
	svc := &fakeService{
		auditListPage: &store.AuditListPage{
			Total: 33,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/audit?id="+auditID.String()+
			"&account=acc&accountMatch=starts_with"+
			"&asset=AAPL&assetMatch=exact"+
			"&actor=operator&actorMatch=exact&source=panel"+
			"&actions=block,unblock&atMode=lte&atMax=2026-01-03T03:04:05Z"+
			"&limit=4&offset=8",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := svc.auditListFilter.Account.Fragments; !slices.Equal(got, []string{"acc"}) {
		t.Fatalf("account fragments = %v", got)
	}
	if svc.auditListFilter.ExternalID != auditID {
		t.Fatalf("external id = %s", svc.auditListFilter.ExternalID)
	}
	if !svc.auditListFilter.Account.AnchorStart || !svc.auditListFilter.Account.AnchorEnd {
		t.Fatalf("account matcher = %+v", svc.auditListFilter.Account)
	}
	if got := svc.auditListFilter.Asset.Fragments; !slices.Equal(got, []string{"AAPL"}) {
		t.Fatalf("asset fragments = %v", got)
	}
	if !svc.auditListFilter.Asset.AnchorStart || !svc.auditListFilter.Asset.AnchorEnd {
		t.Fatalf("asset matcher = %+v", svc.auditListFilter.Asset)
	}
	if got := svc.auditListFilter.Actor.Fragments; !slices.Equal(got, []string{"operator"}) {
		t.Fatalf("actor fragments = %v", got)
	}
	if !svc.auditListFilter.Actor.AnchorStart || !svc.auditListFilter.Actor.AnchorEnd {
		t.Fatalf("actor matcher = %+v", svc.auditListFilter.Actor)
	}
	if svc.auditListFilter.Source != domain.SourcePanel {
		t.Fatalf("source = %q", svc.auditListFilter.Source)
	}
	if !slices.Equal(svc.auditListFilter.Actions, []domain.AuditAction{
		domain.AuditActionBlock,
		domain.AuditActionUnblock,
	}) {
		t.Fatalf("actions = %v", svc.auditListFilter.Actions)
	}
	if svc.auditListFilter.At.Max == nil || svc.auditListFilter.At.MaxExclusive {
		t.Fatalf("at range = %+v", svc.auditListFilter.At)
	}
	if svc.auditListFilter.Page.Limit != 4 || svc.auditListFilter.Page.Offset != 8 {
		t.Fatalf("page = %+v", svc.auditListFilter.Page)
	}
	m := bodyMap(t, rec.Result())
	if m["total"] != float64(33) {
		t.Fatalf("envelope = %v", m)
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
	for _, field := range []string{"id", "at", "actor", "action", "account", "detail"} {
		if _, ok := e[field]; !ok {
			t.Fatalf("audit entry missing field %q", field)
		}
	}
	// The audit row is addressed by its opaque external id; no surrogate id leaks.
	if e["id"] != extID("audit-1").String() {
		t.Fatalf("want id=%s, got %v", extID("audit-1").String(), e["id"])
	}
	if _, leaked := e["externalId"]; leaked {
		t.Fatalf("audit entry leaked externalId: %v", e)
	}
	assertNoSurrogateID(t, e)
	if e["actor"] != "operator" {
		t.Fatalf("want actor=operator, got %v", e["actor"])
	}
}

func TestListAudit_FilterResolution(t *testing.T) {
	cases := []struct {
		name         string
		query        string
		wantActions  []domain.AuditAction
		wantCategory domain.AuditCategory
	}{
		{"default unfiltered", "/api/v1/audit", nil, ""},
		{"category control", "/api/v1/audit?category=control", nil, domain.AuditCategoryControl},
		{"category trading", "/api/v1/audit?category=trading", nil, domain.AuditCategoryTrading},
		{"category all", "/api/v1/audit?category=all", nil, ""},
		{"explicit actions", "/api/v1/audit?actions=block,submit_order",
			[]domain.AuditAction{domain.AuditActionBlock, domain.AuditActionSubmitOrder}, ""},
		{"explicit actions override category", "/api/v1/audit?category=control&actions=submit_order",
			[]domain.AuditAction{domain.AuditActionSubmitOrder}, ""},
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
			if !slices.Equal(svc.auditListFilter.Actions, tc.wantActions) {
				t.Fatalf("actions = %+v, want %+v", svc.auditListFilter.Actions, tc.wantActions)
			}
			if svc.auditListFilter.Category != tc.wantCategory {
				t.Fatalf("category = %q, want %q", svc.auditListFilter.Category, tc.wantCategory)
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

func TestListAudit_BadQueryParams(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"unknown_sort", "?sort=unknown"},
		{"invalid_order", "?sort=source&order=sideways"},
		{"invalid_category", "?category=bogus"},
		{"invalid_time", "?atMode=after&atMin=not-time"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(&fakeService{})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/audit"+tc.query, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d", rec.Code)
			}
			m := bodyMap(t, rec.Result())
			errObj, _ := m["error"].(map[string]any)
			if errObj["code"] != "validation" {
				t.Fatalf("want code=validation, got %v", errObj["code"])
			}
		})
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
	if !slices.Equal(
		tradingActions,
		[]string{"submit_order", "submit_drop_copy_order", "execution_report"},
	) {
		t.Fatalf(
			"trading group = %v, want [submit_order submit_drop_copy_order execution_report]",
			tradingActions,
		)
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
