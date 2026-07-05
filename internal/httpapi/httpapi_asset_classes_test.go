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

// --- GET /api/v1/asset-classes (handleListAssetClasses) ---------------------

func TestListAssetClasses(t *testing.T) {
	svc := &fakeService{
		assetClassRows: []store.AssetClassListRow{
			{Class: domain.AssetClass{Code: "equity", Title: "Equity", Notes: "shares"}, AssetCount: 3},
			{Class: domain.AssetClass{Code: "fx", Title: "FX"}},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/asset-classes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	classes, ok := m["assetClasses"].([]any)
	if !ok || len(classes) != 2 {
		t.Fatalf("want 2 classes, got %v", m["assetClasses"])
	}
	if m["total"] != float64(2) {
		t.Fatalf("total = %v", m["total"])
	}
	first := classes[0].(map[string]any)
	for _, field := range []string{"code", "title", "notes", "assetCount"} {
		if _, ok := first[field]; !ok {
			t.Fatalf("class missing field %q", field)
		}
	}
	if first["code"] != "equity" || first["assetCount"] != float64(3) {
		t.Fatalf("unexpected first class: %v", first)
	}
	assertNoSurrogateID(t, first)
}

func TestListAssetClasses_PropagatesFilters(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/asset-classes?code=eq*&codeMatch=ends_with&notes=shares&notesMatch=contains&sort=assetCount&order=desc",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := svc.assetClassFilter.Code.Fragments; !slices.Equal(got, []string{"eq", ""}) {
		t.Fatalf("code fragments = %v", got)
	}
	if got := svc.assetClassFilter.Notes.Fragments; !slices.Equal(got, []string{"shares"}) {
		t.Fatalf("notes fragments = %v", got)
	}
	if svc.assetClassFilter.Sort.Column != "assetCount" || !svc.assetClassFilter.Sort.Descending {
		t.Fatalf("sort = %+v", svc.assetClassFilter.Sort)
	}
}

func TestListAssetClasses_BadSort(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/asset-classes?sort=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestListAssetClasses_ServiceError(t *testing.T) {
	svc := &fakeService{assetClassErr: fmt.Errorf("boom")}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/asset-classes", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}

// --- POST /api/v1/asset-classes (handleCreateAssetClass) --------------------

func TestCreateAssetClass(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"equity","title":"Equity","notes":"shares"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/asset-classes", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	c, ok := m["assetClass"].(map[string]any)
	if !ok {
		t.Fatalf("want assetClass object, got %v", m["assetClass"])
	}
	if c["code"] != "equity" || c["title"] != "Equity" || c["notes"] != "shares" {
		t.Fatalf("unexpected class: %v", c)
	}
	assertNoSurrogateID(t, c)
	if len(svc.assetClasses) != 1 || svc.assetClasses[0].Code != "equity" {
		t.Fatalf("expected class persisted, got %v", svc.assetClasses)
	}
}

func TestCreateAssetClass_InvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/asset-classes", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	if len(svc.assetClasses) != 0 {
		t.Fatalf("invalid JSON must not persist, got %v", svc.assetClasses)
	}
}

func TestCreateAssetClass_Conflict(t *testing.T) {
	svc := &fakeService{createAssetClassErr: domain.ErrAlreadyExists}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"equity"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/asset-classes", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
}

func TestCreateAssetClass_ValidationError(t *testing.T) {
	svc := &fakeService{createAssetClassErr: fmt.Errorf("bad: %w", domain.ErrInvalid)}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":""}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/asset-classes", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

// --- PUT /api/v1/asset-classes/{code} (handleUpdateAssetClass) --------------

func TestUpdateAssetClass_Rename(t *testing.T) {
	svc := &fakeService{assetClasses: []domain.AssetClass{{Code: "equity", Title: "Equity"}}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"stock","title":"Stock","notes":"renamed"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/asset-classes/equity", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	c, _ := m["assetClass"].(map[string]any)
	if c["code"] != "stock" || c["title"] != "Stock" || c["notes"] != "renamed" {
		t.Fatalf("unexpected updated class: %v", c)
	}
	if svc.assetClasses[0].Code != "stock" {
		t.Fatalf("class not renamed in fake, got %v", svc.assetClasses)
	}
}

func TestUpdateAssetClass_NotFound(t *testing.T) {
	svc := &fakeService{updateAssetClassErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"stock"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/asset-classes/ghost", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestUpdateAssetClass_InvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/asset-classes/equity", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

// --- DELETE /api/v1/asset-classes/{code} (handleDeleteAssetClass) -----------

func TestDeleteAssetClass(t *testing.T) {
	svc := &fakeService{assetClasses: []domain.AssetClass{{Code: "equity"}}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/asset-classes/equity?force=true", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if !svc.deleteAssetClassForce {
		t.Fatal("force query param not propagated")
	}
	if len(svc.assetClasses) != 0 {
		t.Fatalf("class not deleted, got %v", svc.assetClasses)
	}
}

func TestDeleteAssetClass_NotFound(t *testing.T) {
	svc := &fakeService{deleteAssetClassErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/asset-classes/ghost", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}
