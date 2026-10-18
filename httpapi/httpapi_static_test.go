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
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// spaIndexBody is the body fakeSPA serves at index.html. The SPA fallback must
// reproduce it byte-for-byte, so tests compare against this constant.
const spaIndexBody = "<html></html>"

// TestServeOpenAPISpec covers GET /api/openapi.yaml: 200, the application/yaml
// content type, MIME sniffing off, and a non-empty body (the embedded spec).
func TestServeOpenAPISpec(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	ct := rec.Result().Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/yaml") {
		t.Fatalf("want application/yaml content type, got %q", ct)
	}
	if got := rec.Result().Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("want X-Content-Type-Options nosniff, got %q", got)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("want non-empty spec body")
	}
	// The embedded spec is OpenAPI YAML; sanity-check a known marker.
	if !strings.Contains(rec.Body.String(), "openapi") {
		t.Fatal("body does not look like an OpenAPI spec")
	}
	body := rec.Body.String()
	limitScopeQuery := `        - name: scope
          in: query
          description: Filter by exact policy scope.
          schema:
            $ref: "#/components/schemas/LimitScope"`
	if !strings.Contains(body, limitScopeQuery) {
		t.Fatal("GET /limits scope filter does not reference LimitScope")
	}
	adjustmentTypeQuery := `        - name: type
          in: query
          description: >
            Filter by adjustment type (see ` + "`AdjustmentType`" + `). Absent, empty or
            ` + "`all`" + ` means no restriction. Any other value returns a 422
            validation error.
          schema:
            anyOf:
              - $ref: "#/components/schemas/AdjustmentType"
              - type: string
                enum: [all]
            default: all`
	if !strings.Contains(body, adjustmentTypeQuery) {
		t.Fatal("GET /adjustments type filter does not reference AdjustmentType")
	}
	limitScopeDefinition := "    LimitScope:\n      type: string"
	limitScopeEnum := `      enum: [broker, global, asset, account, account_group, account_asset,
        underlying_asset, settlement_asset, account_underlying_asset,
        account_settlement_asset]`
	if !strings.Contains(body, limitScopeDefinition) ||
		!strings.Contains(body, limitScopeEnum) {
		t.Fatal("LimitScope does not include the order-size scopes")
	}
	if !strings.Contains(body, "realizedPnlResult:\n          type: object") ||
		!strings.Contains(body, "required: [delta, result]") {
		t.Fatal("AdjustmentAccepted realizedPnlResult is not documented as an object")
	}
	if !strings.Contains(body, "application/problem+json:") ||
		!strings.Contains(body, "required: [type, title, status, detail, errors]") ||
		strings.Contains(
			body,
			"\"400\":\n          $ref: \"#/components/responses/ValidationError\"",
		) {
		t.Fatal("validation responses do not document the RFC 9457 400/422 split")
	}
	if !strings.Contains(
		body,
		"enum: [account, asset, available, held, incoming, realizedPnl, updatedAt]",
	) {
		t.Fatal("balance realizedPnl sort is missing from OpenAPI")
	}
}

// TestServeOpenAPISpec_WrongMethod covers the route's method restriction: only
// GET is registered, so a non-GET request is rejected by chi (405), not served
// the spec and not swallowed by the SPA fallback.
func TestServeOpenAPISpec_WrongMethod(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/openapi.yaml", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}

// TestServeSwaggerUI covers GET /docs: 200, the text/html content type, the
// exact content security policy, which admits only same-origin scripts, styles
// and requests, and a page that loads the stylesheet, the bundle and then the
// initializer from /docs, with no inline script or style element and no
// external URL. The initializer wires Swagger UI to the spec route.
func TestServeSwaggerUI(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	ct := rec.Result().Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("want text/html content type, got %q", ct)
	}
	const wantPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; " +
		"img-src 'self' data:; connect-src 'self'; font-src 'self'; " +
		"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	if policy := rec.Result().Header.Get("Content-Security-Policy"); policy != wantPolicy {
		t.Fatalf("content security policy mismatch\ngot:  %q\nwant: %q",
			policy, wantPolicy)
	}
	body := rec.Body.String()
	for _, tag := range regexp.MustCompile(`(?i)<script\b[^>]*>`).FindAllString(body, -1) {
		if !regexp.MustCompile(`(?i)\ssrc\s*=`).MatchString(tag) {
			t.Fatalf("page has an inline script: %s", tag)
		}
	}
	if regexp.MustCompile(`(?i)<style\b`).MatchString(body) {
		t.Fatal("page has an inline style element")
	}
	external := regexp.MustCompile(`(?i)\b(?:src|href)\s*=\s*["']?\s*(?:https?:)?//`)
	if found := external.FindString(body); found != "" {
		t.Fatalf("page loads an external URL: %s", found)
	}
	css := strings.Index(body, `href="/docs/swagger-ui.css"`)
	bundle := strings.Index(body, `src="/docs/swagger-ui-bundle.js"`)
	initializer := strings.Index(body, `src="/docs/swagger-initializer.js"`)
	if css < 0 || bundle < 0 || initializer < bundle {
		t.Fatalf("page does not load the stylesheet, the bundle and then the "+
			"initializer from /docs:\n%s", body)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/docs/swagger-initializer.js", nil,
	))
	if !strings.Contains(rec.Body.String(), `url: "/api/openapi.yaml"`) {
		t.Fatal("Swagger UI initializer does not reference the spec URL")
	}
}

// TestServeSwaggerUIAssets covers the files served under /docs - the ones the
// page loads and the vendored code's license and notices: each answers 200 with
// its content type, MIME sniffing off, and the file's bytes as the source tree
// holds them.
func TestServeSwaggerUIAssets(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, contentType, file string }{
		{"/docs/swagger-ui.css", "text/css; charset=utf-8", "swaggerui/swagger-ui.css"},
		{"/docs/swagger-ui-bundle.js", "text/javascript; charset=utf-8", "swaggerui/swagger-ui-bundle.js"},
		{"/docs/swagger-initializer.js", "text/javascript; charset=utf-8", "swagger-initializer.js"},
		{"/docs/swagger-ui-bundle.js.LICENSE.txt", "text/plain; charset=utf-8", "swaggerui/swagger-ui-bundle.js.LICENSE.txt"},
		{"/docs/LICENSE", "text/plain; charset=utf-8", "swaggerui/LICENSE"},
		{"/docs/NOTICE", "text/plain; charset=utf-8", "swaggerui/NOTICE"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			want, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
			if ct := rec.Result().Header.Get("Content-Type"); ct != tc.contentType {
				t.Fatalf("want content type %q, got %q", tc.contentType, ct)
			}
			if got := rec.Result().Header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("want X-Content-Type-Options nosniff, got %q", got)
			}
			if !bytes.Equal(rec.Body.Bytes(), want) {
				t.Fatalf("body is not %s: %d bytes, want %d",
					tc.file, rec.Body.Len(), len(want))
			}
		})
	}
}

// TestServeSwaggerUI_WrongMethod confirms /docs is GET-only: a non-GET request
// yields 405, not the page and not the SPA fallback.
func TestServeSwaggerUI_WrongMethod(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/docs", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}

// TestSPAFallback_UnknownRoutes is table-driven over paths that match no
// explicit route. The NotFound handler is the SPA, so each returns 200 with the
// index.html shell and a text/html content type, letting the client router
// resolve the deep link.
func TestSPAFallback_UnknownRoutes(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"root", "/"},
		{"client route", "/accounts"},
		{"nested client route", "/groups/group-1/limits"},
		{"unknown top level", "/totally-unknown"},
		{"path traversal stays inside dist", "/../../etc/passwd"},
	}
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
			ct := rec.Result().Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("want text/html content type, got %q", ct)
			}
			if rec.Body.String() != spaIndexBody {
				t.Fatalf("want SPA shell %q, got %q", spaIndexBody, rec.Body.String())
			}
		})
	}
}

// TestSPAFallback_ExistingAssetReachesFileServer confirms the SPA hands a
// request that names a real file to its embedded file server rather than always
// returning the shell. The only file in fakeSPA is index.html, which the Go
// file server canonicalizes (/index.html -> ./), so the observable proof that
// the asset branch ran - not the 200 shell fallback - is the 301 redirect.
func TestSPAFallback_ExistingAssetReachesFileServer(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.html", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("want 301 (file server canonicalization), got %d", rec.Code)
	}
	if loc := rec.Result().Header.Get("Location"); loc != "./" {
		t.Fatalf("want redirect to ./, got %q", loc)
	}
}

// TestSPAFallback_UnknownAPIPath documents that an unknown path under the
// /api/v1 mount is NOT a JSON 404: chi inherits the router's NotFound handler
// into inline subrouters, so an unmatched /api/v1/* path falls through to the
// SPA shell (200 text/html), exactly like any other deep link. The explicit
// v1 routes still match first; only genuinely unknown paths reach the fallback.
func TestSPAFallback_UnknownAPIPath(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/does-not-exist", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (SPA fallback), got %d", rec.Code)
	}
	ct := rec.Result().Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("want text/html content type, got %q", ct)
	}
	if rec.Body.String() != spaIndexBody {
		t.Fatalf("want SPA shell %q, got %q", spaIndexBody, rec.Body.String())
	}
}

// TestSPAFallback_KnownAPIWrongMethod confirms the SPA does not swallow method
// mismatches on real v1 routes: a non-GET to GET-only /api/v1/health returns
// 405 from chi rather than the SPA shell.
func TestSPAFallback_KnownAPIWrongMethod(t *testing.T) {
	r, err := newRouter(&fakeService{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}
