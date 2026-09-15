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
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// fakeLogs is a static LogSource returning a fixed set of lines.
type fakeLogs struct {
	lines []string
}

func (f *fakeLogs) Snapshot() []string {
	out := make([]string, len(f.lines))
	copy(out, f.lines)
	return out
}

// newRouterWithLogs builds the router with the log tail wired in, so the
// /service/logs routes are registered.
func newRouterWithLogs(svc backend.ControlPlane, logs httpx.LogSource) (http.Handler, error) {
	return httpx.NewRouter(httpx.RouterConfig{
		Routes:         NewRouteRegistry(svc, logs),
		Authorizer:     httpx.AllowAll{},
		CallerResolver: testCallerResolver,
		SPA:            fakeSPA(),
		BodyLimit:      BodyLimitPolicy(),
		ExtraMounts:    ExtraMounts(),
	})
}

// TestServiceLogs covers GET /api/v1/service/logs: 200, JSON with the lines
// oldest-first and the matching count.
func TestServiceLogs(t *testing.T) {
	logs := &fakeLogs{lines: []string{"line-1", "line-2", "line-3"}}
	r, err := newRouterWithLogs(&fakeService{}, logs)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/service/logs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	ct := rec.Result().Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("want application/json content type, got %q", ct)
	}
	m := bodyMap(t, rec.Result())
	if got, ok := m["count"].(float64); !ok || int(got) != 3 {
		t.Fatalf("want count=3, got %v", m["count"])
	}
	raw, ok := m["lines"].([]any)
	if !ok {
		t.Fatalf("want lines array, got %v", m["lines"])
	}
	want := []string{"line-1", "line-2", "line-3"}
	if len(raw) != len(want) {
		t.Fatalf("want %d lines, got %d (%v)", len(want), len(raw), raw)
	}
	for i := range want {
		if raw[i] != want[i] {
			t.Fatalf("line %d: want %q, got %v", i, want[i], raw[i])
		}
	}
}

// TestServiceLogs_Empty covers an empty buffer: 200 with an empty (non-null)
// lines array and count 0.
func TestServiceLogs_Empty(t *testing.T) {
	r, err := newRouterWithLogs(&fakeService{}, &fakeLogs{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/service/logs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	// The lines field must serialize as [] not null, so the body carries the
	// empty array literal.
	if body := rec.Body.String(); !strings.Contains(body, `"lines":[]`) {
		t.Fatalf("want empty lines array in body, got %q", body)
	}
	m := bodyMap(t, rec.Result())
	if got, ok := m["count"].(float64); !ok || int(got) != 0 {
		t.Fatalf("want count=0, got %v", m["count"])
	}
}

// TestServiceLogsDownload covers GET /api/v1/service/logs/download: the
// text/plain content type, the attachment disposition with the fixed filename,
// and the body as the lines joined by newlines.
func TestServiceLogsDownload(t *testing.T) {
	logs := &fakeLogs{lines: []string{"alpha", "beta", "gamma"}}
	r, err := newRouterWithLogs(&fakeService{}, logs)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/service/logs/download", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	ct := rec.Result().Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("want text/plain content type, got %q", ct)
	}
	cd := rec.Result().Header.Get("Content-Disposition")
	if cd != `attachment; filename="pit-officer.log"` {
		t.Fatalf("unexpected content disposition: %q", cd)
	}
	if body := rec.Body.String(); body != "alpha\nbeta\ngamma\n" {
		t.Fatalf("unexpected body: %q", body)
	}
}

// TestServiceLogsDownload_Empty covers the download with an empty buffer: a
// 200 with an empty body and the same attachment headers.
func TestServiceLogsDownload_Empty(t *testing.T) {
	r, err := newRouterWithLogs(&fakeService{}, &fakeLogs{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/v1/service/logs/download", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != "" {
		t.Fatalf("want empty body, got %q", body)
	}
}

// TestServiceLogsRoutesAbsentWhenNil confirms the log routes are not registered
// when Options.Logs is nil: an unmatched path under the v1 mount falls through
// to the SPA shell (200 text/html), exactly like any other unknown deep link,
// rather than returning the JSON log body. Both the /api/v1 and /app/api/v1
// mounts share mountV1, so checking one proves the gating.
func TestServiceLogsRoutesAbsentWhenNil(t *testing.T) {
	r, err := newRouter(&fakeService{}) // no Logs option
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/service/logs",
		"/api/v1/service/logs/download",
		"/app/api/v1/service/logs",
		"/app/api/v1/service/logs/download",
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: want 200 (SPA fallback), got %d", path, rec.Code)
		}
		ct := rec.Result().Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("%s: want text/html (SPA fallback), got %q", path, ct)
		}
		if rec.Body.String() != spaIndexBody {
			t.Fatalf("%s: want SPA shell, got %q", path, rec.Body.String())
		}
	}
}

// TestServiceLogsBothMounts confirms the log routes are present on both the
// /api/v1 and /app/api/v1 mounts when the source is supplied.
func TestServiceLogsBothMounts(t *testing.T) {
	logs := &fakeLogs{lines: []string{"x"}}
	r, err := newRouterWithLogs(&fakeService{}, logs)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/service/logs",
		"/app/api/v1/service/logs",
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d", path, rec.Code)
		}
		ct := rec.Result().Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("%s: want application/json, got %q", path, ct)
		}
	}
}
