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
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func TestListBalances_PropagatesFilters(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/balances?account=acc&groupCode=desk"+
			"&asset=USD&availableMode=between&availableMin=2.5&availableMax=10"+
			"&updatedAtMode=greater_than&updatedAfter=2026-01-02T03:04:05Z"+
			"&sort=available&order=desc&limit=10&offset=20",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := svc.balanceFilter.Account.Fragments; !slices.Equal(got, []string{"acc"}) {
		t.Fatalf("account fragments = %v", got)
	}
	if svc.balanceFilter.GroupCode == nil || *svc.balanceFilter.GroupCode != "desk" {
		t.Fatalf("group filter = %v", svc.balanceFilter.GroupCode)
	}
	if got := svc.balanceFilter.Asset.Fragments; !slices.Equal(got, []string{"USD"}) {
		t.Fatalf("asset fragments = %v", got)
	}
	if svc.balanceFilter.Available.Min == nil || svc.balanceFilter.Available.Max == nil {
		t.Fatalf("available range = %+v", svc.balanceFilter.Available)
	}
	if svc.balanceFilter.Available.MinExclusive || svc.balanceFilter.Available.MaxExclusive {
		t.Fatalf("available exclusivity = %+v", svc.balanceFilter.Available)
	}
	if svc.balanceFilter.UpdatedAt.Min == nil || !svc.balanceFilter.UpdatedAt.MinExclusive {
		t.Fatalf("updatedAt range = %+v", svc.balanceFilter.UpdatedAt)
	}
	if svc.balanceFilter.Sort.Column != "available" || !svc.balanceFilter.Sort.Descending {
		t.Fatalf("sort filter = %+v", svc.balanceFilter.Sort)
	}
	if svc.balanceFilter.Page.Limit != 10 || svc.balanceFilter.Page.Offset != 20 {
		t.Fatalf("page filter = %+v", svc.balanceFilter.Page)
	}
}

func TestListAdjustments_PropagatesFilters(t *testing.T) {
	status := domain.AdjustmentStatusRejected
	svc := &fakeService{
		adjustmentPage: &store.AdjustmentListPage{
			Total: 17,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/adjustments?id="+extID("adj-1").String()+
			"&account=acc-1&accountMatch=exact"+
			"&asset=US&assetMatch=starts_with&source=api&status=rejected"+
			"&atMode=gte&atMin=2026-01-02T03:04:05Z"+
			"&sort=status&order=asc&limit=9&offset=18",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.adjustmentFilter.ExternalID != extID("adj-1") {
		t.Fatalf("external id = %v", svc.adjustmentFilter.ExternalID)
	}
	if got := svc.adjustmentFilter.Account.Fragments; !slices.Equal(got, []string{"acc-1"}) {
		t.Fatalf("account fragments = %v", got)
	}
	if !svc.adjustmentFilter.Account.AnchorStart ||
		!svc.adjustmentFilter.Account.AnchorEnd {
		t.Fatalf("account matcher = %+v", svc.adjustmentFilter.Account)
	}
	if got := svc.adjustmentFilter.Asset.Fragments; !slices.Equal(got, []string{"US"}) {
		t.Fatalf("asset fragments = %v", got)
	}
	if !svc.adjustmentFilter.Asset.AnchorStart || !svc.adjustmentFilter.Asset.AnchorEnd {
		t.Fatalf("asset matcher = %+v", svc.adjustmentFilter.Asset)
	}
	if svc.adjustmentFilter.Source != domain.SourceAPI {
		t.Fatalf("source = %q", svc.adjustmentFilter.Source)
	}
	if svc.adjustmentFilter.Status == nil || *svc.adjustmentFilter.Status != status {
		t.Fatalf("status = %v", svc.adjustmentFilter.Status)
	}
	if svc.adjustmentFilter.At.Min == nil || svc.adjustmentFilter.At.MinExclusive {
		t.Fatalf("at range = %+v", svc.adjustmentFilter.At)
	}
	if svc.adjustmentFilter.Sort.Column != "status" ||
		svc.adjustmentFilter.Sort.Descending {
		t.Fatalf("sort = %+v", svc.adjustmentFilter.Sort)
	}
	if svc.adjustmentFilter.Page.Limit != 9 ||
		svc.adjustmentFilter.Page.Offset != 18 {
		t.Fatalf("page = %+v", svc.adjustmentFilter.Page)
	}
	m := bodyMap(t, rec.Result())
	if m["total"] != float64(17) {
		t.Fatalf("envelope = %v", m)
	}
}
