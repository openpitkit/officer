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
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestListAssets(t *testing.T) {
	r, err := newRouter(&fakeService{
		assets: []domain.Asset{
			{Code: "AAPL", Title: "Apple Inc.", AssetClass: "equity"},
			{Code: "USD", Title: "US Dollar", AssetClass: "cash"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	assets, ok := m["assets"].([]any)
	if !ok || len(assets) != 2 {
		t.Fatalf("want 2 assets, got %v", m["assets"])
	}
	if m["total"] != float64(2) {
		t.Fatalf("total = %v, want 2", m["total"])
	}
	first, _ := assets[0].(map[string]any)
	if first["code"] != "AAPL" || first["title"] != "Apple Inc." ||
		first["assetClass"] != "equity" {
		t.Fatalf("unexpected first asset: %v", first)
	}
}

func TestListAssets_PropagatesSort(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/assets?sort=assetClass&order=desc",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.assetFilter.Sort.Column != "assetClass" || !svc.assetFilter.Sort.Descending {
		t.Fatalf("sort = %+v", svc.assetFilter.Sort)
	}
}

func TestListAssets_PropagatesFilters(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/assets?code=apple&codeMatch=contains&class=equity&classMatch=exact&limit=5&offset=10",
		nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if len(svc.assetFilter.Code.Fragments) != 1 ||
		svc.assetFilter.Code.Fragments[0] != "apple" {
		t.Fatalf("code matcher = %+v", svc.assetFilter.Code)
	}
	if len(svc.assetFilter.Class.Fragments) != 1 ||
		svc.assetFilter.Class.Fragments[0] != "equity" ||
		!svc.assetFilter.Class.AnchorStart ||
		!svc.assetFilter.Class.AnchorEnd {
		t.Fatalf("class matcher = %+v", svc.assetFilter.Class)
	}
	if svc.assetFilter.Page.Limit != 5 || svc.assetFilter.Page.Offset != 10 {
		t.Fatalf("page = %+v", svc.assetFilter.Page)
	}
}

func TestListAssets_BadSort(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assets?sort=bogus", nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestCreateAsset(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(
		`{"code":"AAPL","title":"Apple Inc.","assetClass":"equity"}`,
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/assets", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	asset, ok := m["asset"].(map[string]any)
	if !ok {
		t.Fatalf("want asset object, got %v", m["asset"])
	}
	if asset["code"] != "AAPL" || asset["title"] != "Apple Inc." ||
		asset["assetClass"] != "equity" {
		t.Fatalf("unexpected asset: %v", asset)
	}
	assertNoSurrogateID(t, asset)
}

func TestCreateAsset_ValidationError(t *testing.T) {
	svc := &fakeService{createAssetErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":""}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/assets", body))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestCreateAsset_Conflict(t *testing.T) {
	svc := &fakeService{createAssetErr: domain.ErrAlreadyExists}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"AAPL"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/assets", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "conflict" {
		t.Fatalf("want code=conflict, got %v", errObj["code"])
	}
}

func TestUpdateAsset(t *testing.T) {
	r, err := newRouter(&fakeService{
		assets: []domain.Asset{{Code: "AAPL", Title: "Apple Inc.", AssetClass: "equity"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"AAPL","title":"Apple","assetClass":"stock"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assets/AAPL", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	asset, ok := m["asset"].(map[string]any)
	if !ok {
		t.Fatalf("want asset object, got %v", m["asset"])
	}
	if asset["code"] != "AAPL" || asset["title"] != "Apple" ||
		asset["assetClass"] != "stock" {
		t.Fatalf("unexpected asset: %v", asset)
	}
}

// TestUpdateAsset_Rename covers the code-edit path: the path code identifies the
// asset and the body carries a new public code, mirroring the group rename.
func TestUpdateAsset_Rename(t *testing.T) {
	svc := &fakeService{
		assets: []domain.Asset{{Code: "AAPL", Title: "Apple Inc."}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"code":"AAPL.US","title":"Apple Inc."}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assets/AAPL", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	asset, _ := m["asset"].(map[string]any)
	if asset["code"] != "AAPL.US" {
		t.Fatalf("unexpected renamed asset: %v", asset)
	}
	if svc.assets[0].Code != "AAPL.US" {
		t.Fatalf("asset not renamed in fake, got %v", svc.assets)
	}
}

func TestUpdateAsset_NotFound(t *testing.T) {
	svc := &fakeService{updateAssetErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"title":"Apple"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assets/AAPL", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestUpdateAsset_ValidationError(t *testing.T) {
	svc := &fakeService{updateAssetErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"title":"Apple"}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assets/AAPL", body))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}

func TestDeleteAsset(t *testing.T) {
	svc := &fakeService{
		assets: []domain.Asset{{Code: "AAPL", Title: "Apple Inc."}},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodDelete, "/api/v1/assets/AAPL?force=true", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if !svc.deleteAssetForce {
		t.Fatal("want force flag propagated to service")
	}
	if len(svc.assets) != 0 {
		t.Fatalf("want asset removed, got %v", svc.assets)
	}
}

func TestDeleteAsset_NotFound(t *testing.T) {
	svc := &fakeService{deleteAssetErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/assets/AAPL", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestDeleteAsset_ValidationError(t *testing.T) {
	svc := &fakeService{deleteAssetErr: domain.ErrInvalid}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/assets/AAPL", nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Fatalf("want code=validation, got %v", errObj["code"])
	}
}
