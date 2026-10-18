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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"go.openpit.dev/officer/framework/domain"
)

// serveGuarded serves req through guard in front of a handler that answers
// 204 and reports whether it ran.
func serveGuarded(guard Middleware, req *http.Request) (*httptest.ResponseRecorder, bool) {
	reached := false
	rec := httptest.NewRecorder()
	guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)
	return rec, reached
}

// errorCode decodes the code of an {"error":{"code","message"}} answer.
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct{ Code string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	return body.Error.Code
}

// assertRefused checks that a guard answered with status and code without
// running the handler behind it.
func assertRefused(
	t *testing.T, rec *httptest.ResponseRecorder, reached bool, status int, code string,
) {
	t.Helper()
	if reached {
		t.Fatal("the refused request reached the handler")
	}
	if rec.Code != status || errorCode(t, rec) != code {
		t.Fatalf("answer = %d %s, want %d %s", rec.Code, rec.Body, status, code)
	}
}

func TestAllowedHostsServesOnlyListedHosts(t *testing.T) {
	t.Parallel()

	guard, err := AllowedHosts([]string{
		"localhost", "127.0.0.1", "10.0.0.1", "::1", "fd00::1", "2001:0db8::1",
		"Officer.Example", "host.docker.internal", "officer", "my_host", "a1",
	})
	if err != nil {
		t.Fatalf("AllowedHosts: %v", err)
	}
	for _, tc := range []struct {
		name    string
		host    string
		allowed bool
	}{
		{"name", "localhost", true},
		{"name with port", "localhost:8787", true},
		{"IPv4 with port", "127.0.0.1:8787", true},
		{"other IPv4", "10.0.0.1", true},
		{"IPv6 with port", "[::1]:8787", true},
		{"IPv6 without port", "[::1]", true},
		{"other IPv6 with port", "[fd00::1]:8787", true},
		{"canonical IPv6 of a name listed non-canonically", "[2001:db8::1]:8787", true},
		{"non-canonical upper-case IPv6", "[2001:0DB8::1]", true},
		{"upper-case host", "LOCALHOST:8787", true},
		{"name listed in upper case", "officer.example", true},
		{"name of three labels", "host.docker.internal:8787", true},
		{"single-label name", "officer", true},
		{"name with underscore", "my_host:8787", true},
		{"name ending in a digit", "a1:8787", true},
		{"foreign host", "attacker.example", false},
		{"foreign host with port", "attacker.example:8787", false},
		{"subdomain of a listed name", "evil.localhost", false},
		{"trailing dot", "localhost.", false},
		{"empty host", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
			req.Host = tc.host
			rec, reached := serveGuarded(guard, req)
			if !tc.allowed {
				assertRefused(t, rec, reached, http.StatusForbidden, "host_not_allowed")
				return
			}
			if !reached || rec.Code != http.StatusNoContent {
				t.Fatalf("host %q = %d %s, want it served", tc.host, rec.Code, rec.Body)
			}
		})
	}
}

func TestAllowedHostsRejectsInvalidNames(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		names []string
	}{
		{"no names", nil},
		{"empty name", []string{"localhost", ""}},
		{"whitespace", []string{"local host"}},
		{"wildcard", []string{"*.example.com"}},
		{"path", []string{"example.com/api"}},
		{"bracketed IPv6", []string{"[::1]"}},
		{"name with port", []string{"localhost:8787"}},
		{"IPv4 with port", []string{"127.0.0.1:8787"}},
		{"IPv6 with zone", []string{"fe80::1%eth0"}},
		{"non-ASCII name", []string{"münchen.example"}},
		{"query character", []string{"officer.example?"}},
		{"empty label", []string{"a..b"}},
		{"leading dot", []string{".example.com"}},
		{"trailing dot", []string{"example.com."}},
		{"short IPv4", []string{"10.1"}},
		{"IPv4 with a hexadecimal part", []string{"0x7f.0.0.1"}},
		{"IPv4 as one decimal number", []string{"2130706433"}},
		{"IPv4 as one hexadecimal number", []string{"0x7f000001"}},
		{"IPv4 with leading zeros", []string{"127.000.000.001"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			guard, err := AllowedHosts(tc.names)
			if err == nil || guard != nil {
				t.Fatalf("AllowedHosts(%q) = guard %v, error %v; want an error", tc.names, guard != nil, err)
			}
			if !strings.HasPrefix(err.Error(), "httpapi: ") {
				t.Fatalf("AllowedHosts(%q) error = %q, want the httpapi prefix", tc.names, err)
			}
		})
	}
}

func TestSameOriginRefusesCrossOriginWrites(t *testing.T) {
	t.Parallel()

	guard := SameOrigin()
	for _, tc := range []struct {
		name    string
		method  string
		header  map[string]string
		allowed bool
	}{
		{"cross-site POST", http.MethodPost, map[string]string{
			"Sec-Fetch-Site": "cross-site",
		}, false},
		{"foreign Origin without Sec-Fetch-Site", http.MethodPost, map[string]string{
			"Origin": "http://attacker.example",
		}, false},
		{"same-origin POST", http.MethodPost, map[string]string{
			"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:8787",
		}, true},
		{"Origin matching Host without Sec-Fetch-Site", http.MethodPost, map[string]string{
			"Origin": "http://127.0.0.1:8787",
		}, true},
		{"POST without Origin and Sec-Fetch-Site", http.MethodPost, nil, true},
		{"cross-site GET", http.MethodGet, map[string]string{
			"Sec-Fetch-Site": "cross-site", "Origin": "http://attacker.example",
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, "http://127.0.0.1:8787/api/v1/database/reset", nil)
			for name, value := range tc.header {
				req.Header.Set(name, value)
			}
			rec, reached := serveGuarded(guard, req)
			if !tc.allowed {
				assertRefused(t, rec, reached, http.StatusForbidden, "origin_not_allowed")
				return
			}
			if !reached || rec.Code != http.StatusNoContent {
				t.Fatalf("%s = %d %s, want it served", tc.name, rec.Code, rec.Body)
			}
		})
	}
}

// readRecorder is a request body that records whether anything read it.
type readRecorder struct {
	io.Reader
	read bool
}

func (r *readRecorder) Read(p []byte) (int, error) {
	r.read = true
	return r.Reader.Read(p)
}

func TestJSONBodyRefusesUndeclaredBodies(t *testing.T) {
	t.Parallel()

	const body = `{"confirm":true}`
	guard := JSONBody()
	for _, tc := range []struct {
		name          string
		method        string
		contentTypes  []string
		body          string
		contentLength int64
		allowed       bool
	}{
		{"JSON", http.MethodPost, []string{"application/json"}, body, int64(len(body)), true},
		{"JSON with upper-case UTF-8 charset", http.MethodPut, []string{"application/json; charset=UTF-8"}, body, int64(len(body)), true},
		{"JSON with quoted UTF-8 charset", http.MethodPost, []string{`application/json; charset="utf-8"`}, body, int64(len(body)), true},
		{"text/plain", http.MethodPost, []string{"text/plain"}, body, int64(len(body)), false},
		{"no Content-Type", http.MethodPost, nil, body, int64(len(body)), false},
		{"UTF-16 charset", http.MethodPost, []string{"application/json; charset=utf-16"}, body, int64(len(body)), false},
		{"parameter other than charset", http.MethodPost, []string{"application/json; profile=strict"}, body, int64(len(body)), false},
		{"parameter beside charset", http.MethodPost, []string{"application/json; charset=utf-8; profile=strict"}, body, int64(len(body)), false},
		{"two Content-Type values", http.MethodPost, []string{"application/json", "application/json"}, body, int64(len(body)), false},
		{"unparsable parameter", http.MethodPatch, []string{"application/json; charset"}, body, int64(len(body)), false},
		{"malformed RFC 2231 parameter", http.MethodPost, []string{"application/json; profile*=bogus"}, body, int64(len(body)), false},
		{"RFC 2231 continuation without a start", http.MethodPost, []string{"application/json; profile*1=x"}, body, int64(len(body)), false},
		{"trailing semicolon", http.MethodPost, []string{"application/json;"}, body, int64(len(body)), false},
		{"repeated charset", http.MethodPost, []string{"application/json; charset=utf-8; charset=utf-8"}, body, int64(len(body)), false},
		{"RFC 2231 encoded charset", http.MethodPost, []string{"application/json; charset*=utf-8''utf-8"}, body, int64(len(body)), false},
		{"unknown length without JSON", http.MethodPost, []string{"text/plain"}, body, -1, false},
		{"empty POST without Content-Type", http.MethodPost, nil, "", 0, true},
		{"empty DELETE without Content-Type", http.MethodDelete, nil, "", 0, true},
		{"GET with a text body", http.MethodGet, []string{"text/plain"}, "hello", 5, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder := &readRecorder{Reader: strings.NewReader(tc.body)}
			req := httptest.NewRequest(tc.method, "/api/v1/database/reset", recorder)
			req.ContentLength = tc.contentLength
			for _, value := range tc.contentTypes {
				req.Header.Add("Content-Type", value)
			}
			rec, reached := serveGuarded(guard, req)
			if !tc.allowed {
				assertRefused(t, rec, reached, http.StatusUnsupportedMediaType, "unsupported_media_type")
				if recorder.read {
					t.Fatal("the refused request's body was read")
				}
				return
			}
			if !reached || rec.Code != http.StatusNoContent {
				t.Fatalf("%s = %d %s, want it served", tc.name, rec.Code, rec.Body)
			}
		})
	}
}

// guardedRouterConfig is a router config over registry behind guards, with
// every other dependency a no-op.
func guardedRouterConfig(registry *RouteRegistry, guards BrowserGuards) RouterConfig {
	return RouterConfig{
		Routes:         registry,
		Authorizer:     AllowAll{},
		CallerResolver: testCallerResolver,
		SPA:            fstest.MapFS{"index.html": {Data: []byte("<html></html>")}},
		BodyLimit:      BodyLimitPolicy(1024, nil),
		Guards:         guards,
		AccessMode:     AccessModeOpen,
	}
}

func TestNewRouterRequiresEveryGuard(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		remove func(*BrowserGuards)
		want   string
	}{
		{"host", func(g *BrowserGuards) { g.Host = nil }, "httpapi: nil host guard"},
		{"origin", func(g *BrowserGuards) { g.Origin = nil }, "httpapi: nil origin guard"},
		{"content type", func(g *BrowserGuards) { g.ContentType = nil }, "httpapi: nil content-type guard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			guards := testGuards(t)
			tc.remove(&guards)
			_, err := NewRouter(guardedRouterConfig(&RouteRegistry{}, guards))
			if err == nil || err.Error() != tc.want {
				t.Fatalf("NewRouter error = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestNewRouterGuardsEveryMount sends to each kind of mount NewRouter serves a
// read under a foreign Host, a cross-site write and a text/plain write: each
// is refused before the mount's own chain resolves a caller or runs a handler,
// while a same-origin read and JSON write under the allowed host pass the
// guards to the mount, which serves them - the SPA fallback answers the write
// with its 404.
func TestNewRouterGuardsEveryMount(t *testing.T) {
	t.Parallel()

	var reached []string
	resolved := 0
	mark := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = append(reached, name)
			w.WriteHeader(http.StatusOK)
		})
	}
	var registry RouteRegistry
	registry.Register(Route{
		ID: "probe", Method: http.MethodGet, Pattern: "/probe", Handler: mark("route"),
	})
	registry.Register(Route{
		ID: "probe.write", Method: http.MethodPost, Pattern: "/probe", Handler: mark("route"),
	})
	cfg := guardedRouterConfig(&registry, testGuards(t))
	cfg.CallerResolver = func(r *http.Request) (domain.Caller, error) {
		resolved++
		return testCallerResolver(r)
	}
	cfg.MCP = mark("mcp")
	cfg.MCPPath = "/mcp"
	cfg.ExtraMounts = []ExtraMount{
		{Method: http.MethodGet, Pattern: "/extra", Handler: mark("extra"), Access: AccessPublic},
		{Method: http.MethodPost, Pattern: "/extra", Handler: mark("extra"), Access: AccessPublic},
	}
	router, err := NewRouter(cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	for _, mount := range []struct {
		path  string
		write int
	}{
		{"/api/v1/probe", http.StatusOK},
		{"/app/api/v1/probe", http.StatusOK},
		{"/mcp", http.StatusOK},
		{"/extra", http.StatusOK},
		{"/spa-fallback", http.StatusNotFound},
	} {
		path := mount.path
		for _, tc := range []struct {
			name, method, target, contentType, fetchSite string
			status                                       int
			code                                         string
		}{
			{"foreign host", http.MethodGet, "http://attacker.example" + path,
				"application/json", "same-origin", http.StatusForbidden, "host_not_allowed"},
			{"cross-site write", http.MethodPost, path,
				"application/json", "cross-site", http.StatusForbidden, "origin_not_allowed"},
			{"text/plain write", http.MethodPost, path,
				"text/plain", "same-origin", http.StatusUnsupportedMediaType, "unsupported_media_type"},
			{"same-origin read", http.MethodGet, path,
				"application/json", "same-origin", http.StatusOK, ""},
			{"same-origin JSON write", http.MethodPost, path,
				"application/json", "same-origin", mount.write, ""},
		} {
			t.Run(path+" "+tc.name, func(t *testing.T) {
				reached, resolved = nil, 0
				req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(`{"confirm":true}`))
				req.Header.Set("Content-Type", tc.contentType)
				req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if tc.code == "" {
					if rec.Code != tc.status {
						t.Fatalf("answer = %d %s, want %d", rec.Code, rec.Body, tc.status)
					}
					return
				}
				assertRefused(t, rec, len(reached) != 0, tc.status, tc.code)
				if resolved != 0 {
					t.Fatal("the refused request resolved a caller")
				}
			})
		}
	}
}

// TestNewRouterAppliesGuardsInOrder sends a cross-site text/plain write under
// a foreign Host: the Host guard answers first, and under the allowed host the
// Origin guard answers before the media type is checked.
func TestNewRouterAppliesGuardsInOrder(t *testing.T) {
	t.Parallel()

	var registry RouteRegistry
	registry.Register(Route{
		ID: "probe", Method: http.MethodPost, Pattern: "/probe",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	})
	router, err := NewRouter(guardedRouterConfig(&registry, testGuards(t)))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	for _, tc := range []struct {
		name      string
		target    string
		crossSite bool
		status    int
		code      string
	}{
		{"foreign host", "http://attacker.example/api/v1/probe", true, http.StatusForbidden, "host_not_allowed"},
		{"cross-site", "/api/v1/probe", true, http.StatusForbidden, "origin_not_allowed"},
		{"text/plain", "/api/v1/probe", false, http.StatusUnsupportedMediaType, "unsupported_media_type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, tc.target, strings.NewReader(`{"confirm":true}`))
			req.Header.Set("Content-Type", "text/plain")
			if tc.crossSite {
				req.Header.Set("Sec-Fetch-Site", "cross-site")
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			assertRefused(t, rec, false, tc.status, tc.code)
		})
	}
}

// TestNewRouterChecksOriginOnEveryMCPMethod sends GET, POST and DELETE to the
// MCP mount: a browser request from another origin, by Origin or by
// Sec-Fetch-Site, is refused before the MCP handler runs, GET included, while a
// same-origin request by either header, a user's own navigation and a request
// without browser headers reach the handler with their own method. A GET of an
// extra mount and of the SPA from another site, as a navigation sends it, is
// still served: the check is the MCP mount's own.
func TestNewRouterChecksOriginOnEveryMCPMethod(t *testing.T) {
	t.Parallel()

	var mcpCalls []string
	cfg := guardedRouterConfig(&RouteRegistry{}, testGuards(t))
	cfg.MCP = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mcpCalls = append(mcpCalls, r.Method)
		w.WriteHeader(http.StatusOK)
	})
	cfg.MCPPath = "/mcp"
	cfg.ExtraMounts = []ExtraMount{{
		Method: http.MethodGet, Pattern: "/extra", Access: AccessPublic,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("extra"))
		}),
	}}
	router, err := NewRouter(cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		for _, tc := range []struct {
			name    string
			header  map[string]string
			allowed bool
		}{
			{"cross-site by Origin", map[string]string{"Origin": "http://attacker.example"}, false},
			{"cross-site by Sec-Fetch-Site", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
			{"same-origin by Origin", map[string]string{"Origin": "http://example.com"}, true},
			{"same-origin by Sec-Fetch-Site", map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
			{"user navigation", map[string]string{"Sec-Fetch-Site": "none"}, true},
			{"no browser headers", nil, true},
		} {
			t.Run(method+" "+tc.name, func(t *testing.T) {
				mcpCalls = nil
				req := httptest.NewRequest(method, "/mcp", nil)
				for name, value := range tc.header {
					req.Header.Set(name, value)
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if !tc.allowed {
					assertRefused(t, rec, len(mcpCalls) != 0, http.StatusForbidden, "origin_not_allowed")
					return
				}
				if rec.Code != http.StatusOK || len(mcpCalls) != 1 || mcpCalls[0] != method {
					t.Fatalf("answer = %d %s, MCP handler calls %q; want 200 and one %s call",
						rec.Code, rec.Body, mcpCalls, method)
				}
			})
		}
	}

	for _, tc := range []struct{ path, body string }{
		{"/extra", "extra"},
		{"/", "<html></html>"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != tc.body {
			t.Fatalf("cross-site GET %s = %d %q, want 200 %q", tc.path, rec.Code, rec.Body, tc.body)
		}
	}
}
