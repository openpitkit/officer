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

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/alerts"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/internal/store/sqlite"
)

func alertHTTPService(t *testing.T) *alerts.Service {
	t.Helper()
	st, err := sqlite.New(filepath.Join(t.TempDir(), "alerts.db"), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	realm, err := st.ForRealm(t.Context(), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	valid := func(raw json.RawMessage) error {
		if string(raw) != "{}" {
			return domain.ErrInvalid
		}
		return nil
	}
	svc, err := alerts.New(realm.(alerts.Store), []alerts.Type{{Name: "test", Validate: valid, Actions: map[string]alerts.Action{
		"accept": {Validate: valid, Resolve: func(context.Context, alerts.Event, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"accepted":true}`), nil
		}},
		"refuse": {Validate: valid, Resolve: func(context.Context, alerts.Event, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("refused")
		}},
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.ContextWithCaller(t.Context(), auth.SystemCaller())
	if err := svc.Publish(ctx, alerts.Event{ID: "event", Type: "test", At: time.Now(), Source: domain.SourceSystem, Payload: json.RawMessage("{}")}); err != nil {
		t.Fatal(err)
	}
	return svc
}

type alertDeny struct{}

func (alertDeny) Authorize(context.Context, domain.Caller, string) error { return domain.ErrForbidden }

func alertRouter(t *testing.T, s *alerts.Service, principal string, authorizer httpx.Authorizer) http.Handler {
	t.Helper()
	reg := &httpx.RouteRegistry{}
	for _, route := range AlertRoutes(s) {
		reg.Register(route)
	}
	guards, err := testGuards()
	if err != nil {
		t.Fatal(err)
	}
	router, err := httpx.NewRouter(httpx.RouterConfig{Surfaces: httpx.V1Surfaces(reg, nil), Authorizer: authorizer, BodyLimit: BodyLimitPolicy(),
		CallerResolver: func(*http.Request) (domain.Caller, error) { return domain.Caller{Principal: principal}, nil },
		Guards:         guards, AccessMode: func() httpx.AccessMode { return httpx.AccessModeOpen }})
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func alertRequest(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAlertRoutesShareTrustedPersonalState(t *testing.T) {
	s := alertHTTPService(t)
	alice := alertRouter(t, s, "alice", httpx.AllowAll{})
	bob := alertRouter(t, s, "bob", httpx.AllowAll{})
	for _, path := range []string{"/api/v1/alerts", "/app/api/v1/alerts"} {
		rec := alertRequest(alice, "GET", path, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"unread":1`) {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body)
		}
	}
	rec := alertRequest(alice, "PUT", "/app/api/v1/alerts/event/read", `{"read":true}`)
	if rec.Code != 200 {
		t.Fatalf("mark = %d %s", rec.Code, rec.Body)
	}
	rec = alertRequest(alice, "GET", "/api/v1/alerts/event", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"read":true`) {
		t.Fatalf("session state = %d %s", rec.Code, rec.Body)
	}
	rec = alertRequest(bob, "GET", "/api/v1/alerts/event", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"read":false`) {
		t.Fatalf("foreign personal state = %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{`{"read":false,"principal":"alice"}`, `{}`, `{"read":null}`, `{"read":"true"}`} {
		rec = alertRequest(bob, "PUT", "/api/v1/alerts/event/read", bad)
		if rec.Code < 400 || rec.Code >= 500 {
			t.Fatalf("accepted body %s: %d", bad, rec.Code)
		}
	}
	rec = alertRequest(bob, "GET", "/api/v1/alerts?principal=alice", "")
	if rec.Code != 422 {
		t.Fatalf("principal query = %d", rec.Code)
	}
	for _, action := range []string{"refuse", "unknown"} {
		rec = alertRequest(alice, "POST", "/app/api/v1/alerts/event/actions/"+action, `{"input":{}}`)
		if rec.Code < 400 {
			t.Fatalf("action %s accepted", action)
		}
		rec = alertRequest(alice, "GET", "/api/v1/alerts/event", "")
		if rec.Code != 200 {
			t.Fatal("failed action removed alert")
		}
	}
	denied := alertRouter(t, s, "alice", alertDeny{})
	rec = alertRequest(denied, "POST", "/api/v1/alerts/event/actions/accept", `{"input":{}}`)
	if rec.Code != 403 {
		t.Fatalf("authorizer bypass = %d", rec.Code)
	}
	missing := alertRouter(t, s, "", httpx.AllowAll{})
	rec = alertRequest(missing, "GET", "/api/v1/alerts", "")
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"code":"internal"`) {
		t.Fatalf("missing trusted principal = %d %s", rec.Code, rec.Body)
	}
	rec = alertRequest(alice, "POST", "/app/api/v1/alerts/event/actions/accept", `{"input":{}}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"actor":"alice"`) {
		t.Fatalf("resolve = %d %s", rec.Code, rec.Body)
	}
	rec = alertRequest(bob, "GET", "/api/v1/alerts/event", "")
	if rec.Code != 404 {
		t.Fatalf("resolved detail = %d", rec.Code)
	}
}
