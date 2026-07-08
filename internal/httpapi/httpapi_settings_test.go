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
)

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
