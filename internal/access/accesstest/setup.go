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
// Please see https://officer.openpit.dev and the OWNERS file for details.

// Package accesstest completes first-run setup through the panel HTTP surface.
package accesstest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.openpit.dev/officer/internal/access"
	"go.openpit.dev/officer/internal/accountmode"
	"go.openpit.dev/officer/internal/apitest"
)

// CompleteOpen chooses accounts and encryption, then completes setup in open
// mode. A sealed database already has its encryption decision.
func CompleteOpen(t testing.TB, handler http.Handler, accounts accountmode.Request, sealed bool) {
	t.Helper()
	decide(t, handler, accounts, sealed)
	request(t, handler, http.MethodPost, "/setup/complete",
		`{"access":"open","acknowledged":true}`, http.StatusNoContent)
}

// CompleteToken completes setup in token mode and returns the issued session
// cookie. It chooses accounts and declines encryption on an unsealed database.
func CompleteToken(
	t testing.TB, handler http.Handler, accounts accountmode.Request, password string,
) *http.Cookie {
	t.Helper()
	decide(t, handler, accounts, false)
	body, err := json.Marshal(map[string]string{"access": "token", "password": password})
	if err != nil {
		t.Fatal(err)
	}
	rec := request(t, handler, http.MethodPost, "/setup/complete", string(body), http.StatusNoContent)
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == access.SessionCookie && cookie.Value != "" {
			return cookie
		}
	}
	t.Fatal("POST /setup/complete issued no session cookie")
	return nil
}

func decide(t testing.TB, handler http.Handler, accounts accountmode.Request, sealed bool) {
	t.Helper()
	body, err := json.Marshal(accounts)
	if err != nil {
		t.Fatal(err)
	}
	request(t, handler, http.MethodPut, "/account-mode", string(body), http.StatusOK)
	if !sealed {
		request(t, handler, http.MethodPut, "/setup/encryption",
			`{"encryption":"none","acknowledged":true}`, http.StatusNoContent)
	}
}

func request(
	t testing.TB, handler http.Handler, method, path, body string, wantStatus int,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://127.0.0.1/app/api/v1"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	apitest.ExpectStatus(t, method+" /app/api/v1"+path, rec, wantStatus)
	if wantStatus == http.StatusNoContent && rec.Body.Len() != 0 {
		t.Fatalf("%s /app/api/v1%s returned a body with status 204", method, path)
	}
	return rec
}
