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

package nonrelease

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func TestBuildRoute(t *testing.T) {
	status := Status{Phase: PhaseChoice, ReleaseDatabaseExists: true}
	route := BuildRoute(func() Status { return status })
	if route.Method != http.MethodGet || route.Pattern != "/build" ||
		!route.Public || !route.DuringSetup {
		t.Fatalf("build route = %+v", route)
	}
	for _, want := range []string{
		`{"release":false,"phase":"choice","releaseDatabaseExists":true}`,
		`{"release":false,"phase":"failed","releaseDatabaseExists":true,"error":"copy failed"}`,
		`{"release":true,"phase":"ready","releaseDatabaseExists":false}`,
	} {
		status = Status{}
		if err := json.Unmarshal([]byte(want), &status); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		route.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/build", nil))
		var gotJSON, wantJSON map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &gotJSON); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(want), &wantJSON); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK || !reflect.DeepEqual(gotJSON, wantJSON) {
			t.Fatalf("GET build = %d %s, want 200 %s", rec.Code, rec.Body, want)
		}
	}
}

func TestDatabaseRoute(t *testing.T) {
	for _, fromRelease := range []bool{true, false} {
		t.Run(fmt.Sprint(fromRelease), func(t *testing.T) {
			calls := 0
			route := DatabaseRoute(func(got bool) (Status, error) {
				calls++
				if got != fromRelease {
					t.Fatalf("choice = %v, want %v", got, fromRelease)
				}
				return Status{Phase: PhasePreparing}, nil
			})
			if route.Method != http.MethodPost || route.Pattern != "/build/database" ||
				!route.Public || !route.DuringSetup {
				t.Fatalf("database route = %+v", route)
			}
			rec := httptest.NewRecorder()
			route.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/build/database",
				strings.NewReader(fmt.Sprintf(`{"fromRelease":%t}`, fromRelease))))
			var status Status
			if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || rec.Code != http.StatusAccepted || status.Phase != PhasePreparing {
				t.Fatalf("choice = %d calls, %d %s, want one call and 202 preparing", calls, rec.Code, rec.Body)
			}
		})
	}
}

func TestDatabaseRouteValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body, pointer, constraint string
		status                          int
	}{
		{"absent", `{}`, "/fromRelease", "required", http.StatusUnprocessableEntity},
		{"null", `{"fromRelease":null}`, "/fromRelease", "required", http.StatusUnprocessableEntity},
		{"unknown", `{"fromRelease":false,"other":true}`, "/other", "", http.StatusUnprocessableEntity},
		{"wrong type", `{"fromRelease":"false"}`, "/fromRelease", "", http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := DatabaseRoute(func(bool) (Status, error) {
				t.Fatal("invalid body reached choose")
				return Status{}, nil
			})
			rec := httptest.NewRecorder()
			route.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/build/database",
				strings.NewReader(tc.body)))
			var problem httpx.ProblemDetails
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tc.status || len(problem.Errors) != 1 ||
				problem.Errors[0].Pointer != tc.pointer ||
				(tc.constraint != "" && problem.Errors[0].Constraint != tc.constraint) {
				t.Fatalf("invalid body = %d %s, want %d problem at %s (%s)",
					rec.Code, rec.Body, tc.status, tc.pointer, tc.constraint)
			}
		})
	}
}

func TestDatabaseRouteChooseError(t *testing.T) {
	err := fmt.Errorf("%w: already preparing", domain.ErrConflict)
	route := DatabaseRoute(func(bool) (Status, error) { return Status{}, err })
	rec := httptest.NewRecorder()
	route.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/build/database",
		strings.NewReader(`{"fromRelease":false}`)))
	expected := httptest.NewRecorder()
	httpx.WriteErr(expected, err)
	if rec.Code != http.StatusConflict || rec.Body.String() != expected.Body.String() {
		t.Fatalf("choose error = %d %s, want WriteErr: %d %s",
			rec.Code, rec.Body, expected.Code, expected.Body)
	}
}
