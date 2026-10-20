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

package access

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// The paths the resolver matrix requests: one route of the v1 registry on
// both v1 surfaces, the MCP endpoint, and one public panel route.
const (
	apiProbe      = "/api/v1/probe"
	panelProbe    = "/app/api/v1/probe"
	mcpEndpoint   = "/mcp"
	publicSession = "/app/api/v1/session"
)

// wantChallenge is the WWW-Authenticate value a refused bearer request
// carries.
const wantChallenge = `Bearer realm="pit-officer"`

// surfaceRequest is one GET request of the resolver matrix.
type surfaceRequest struct {
	path string
	// authorization is the Authorization header, empty for none.
	authorization string
	// cookie is the session cookie value, empty for none.
	cookie string
}

// surfaceAnswer is the answer the matrix expects. An admitted request answers
// 200 with body; a refused one answers status with the error code, a message
// containing message, and the bearer challenge when challenge is set.
type surfaceAnswer struct {
	status    int
	body      string
	code      string
	message   string
	challenge bool
}

// TestResolverAdmitsOnlyEachSurfaceCredential drives the access resolver
// behind the framework router: open mode admits every surface without
// credentials; token mode admits the REST API only with the API token, MCP
// only with the agent token and the panel API only with a live session,
// refuses everything else with 401 or 403 wrong_token, and still serves a
// public panel route without credentials; and a mode switch changes the
// answer of the next request on the same router.
func TestResolverAdmitsOnlyEachSurfaceCredential(t *testing.T) {
	h := newHarness(t)
	router := newSurfaceRouter(t, h.service.ResolveCaller)
	operatorAnswer := surfaceAnswer{status: http.StatusOK, body: domain.PrincipalOperator}
	publicAnswer := surfaceAnswer{status: http.StatusOK, body: "public"}

	t.Run("open mode admits every surface without credentials", func(t *testing.T) {
		for _, path := range []string{apiProbe, panelProbe, mcpEndpoint} {
			expectAnswer(t, router, surfaceRequest{path: path}, operatorAnswer)
		}
		expectAnswer(t, router, surfaceRequest{path: publicSession}, publicAnswer)
	})

	session := h.enableToken(firstPassword)
	apiToken, err := h.service.Token(TokenAPI)
	if err != nil {
		t.Fatalf("Token(api): %v", err)
	}
	agentToken, err := h.service.Token(TokenAgent)
	if err != nil {
		t.Fatalf("Token(agent): %v", err)
	}
	secrets := []string{session, apiToken, agentToken}
	challenged := surfaceAnswer{
		status: http.StatusUnauthorized, code: "unauthorized", challenge: true,
	}
	unauthorized := surfaceAnswer{
		status: http.StatusUnauthorized, code: "unauthorized",
	}
	for _, tc := range []struct {
		name string
		req  surfaceRequest
		want surfaceAnswer
	}{
		{"REST API without a bearer token",
			surfaceRequest{path: apiProbe}, challenged},
		{"REST API with the API token",
			surfaceRequest{path: apiProbe, authorization: "Bearer " + apiToken},
			operatorAnswer},
		{"REST API with the scheme in lower case",
			surfaceRequest{path: apiProbe, authorization: "bearer " + apiToken},
			operatorAnswer},
		{"REST API with the agent token",
			surfaceRequest{path: apiProbe, authorization: "Bearer " + agentToken},
			surfaceAnswer{
				status: http.StatusForbidden, code: "wrong_token",
				message: "accepted only on /mcp",
			}},
		{"REST API with an unknown token",
			surfaceRequest{path: apiProbe, authorization: "Bearer pit_api_unknown"},
			challenged},
		{"REST API with two tokens",
			surfaceRequest{
				path:          apiProbe,
				authorization: "Bearer " + apiToken + " " + apiToken,
			},
			challenged},
		{"REST API with another scheme",
			surfaceRequest{path: apiProbe, authorization: "Basic " + apiToken},
			challenged},
		{"REST API with a session cookie alone",
			surfaceRequest{path: apiProbe, cookie: session}, challenged},
		{"MCP with the agent token",
			surfaceRequest{path: mcpEndpoint, authorization: "Bearer " + agentToken},
			operatorAnswer},
		{"MCP with the API token",
			surfaceRequest{path: mcpEndpoint, authorization: "Bearer " + apiToken},
			surfaceAnswer{
				status: http.StatusForbidden, code: "wrong_token",
				message: "accepted only on /api/v1",
			}},
		{"MCP without a bearer token",
			surfaceRequest{path: mcpEndpoint}, challenged},
		{"MCP with a session cookie alone",
			surfaceRequest{path: mcpEndpoint, cookie: session}, challenged},
		{"panel API with a live session",
			surfaceRequest{path: panelProbe, cookie: session}, operatorAnswer},
		{"panel API with the API token alone",
			surfaceRequest{path: panelProbe, authorization: "Bearer " + apiToken},
			unauthorized},
		{"panel API without credentials",
			surfaceRequest{path: panelProbe}, unauthorized},
		{"public panel route without credentials",
			surfaceRequest{path: publicSession}, publicAnswer},
	} {
		t.Run("token mode: "+tc.name, func(t *testing.T) {
			expectAnswer(t, router, tc.req, tc.want, secrets...)
		})
	}

	t.Run("token mode: panel API with an ended session", func(t *testing.T) {
		if err := h.service.SignOut(context.Background(), session); err != nil {
			t.Fatalf("SignOut: %v", err)
		}
		expectAnswer(t, router, surfaceRequest{path: panelProbe, cookie: session},
			unauthorized, secrets...)
	})

	t.Run("switching to open mode admits the next request", func(t *testing.T) {
		if err := h.service.SwitchToOpen(panelCtx(), firstPassword, true); err != nil {
			t.Fatalf("SwitchToOpen: %v", err)
		}
		expectAnswer(t, router, surfaceRequest{path: apiProbe}, operatorAnswer,
			secrets...)
	})
}

// TestResolverFailsClosedWithoutKnownSurface calls the resolver directly, past
// the router, which always stamps a known surface: in both modes a request
// without a surface, or with a surface other than the REST API, MCP and the
// panel API, resolves no caller and is an internal error - neither an
// unauthorized nor a wrong-token refusal - so WriteErr answers 500 internal.
func TestResolverFailsClosedWithoutKnownSurface(t *testing.T) {
	h := newHarness(t)
	expectInternal := func(mode string) {
		for _, tc := range []struct {
			name string
			ctx  context.Context
		}{
			{"no surface", context.Background()},
			{"system surface", httpx.ContextWithSurface(
				context.Background(), domain.SourceSystem,
			)},
		} {
			t.Run(mode+": "+tc.name, func(t *testing.T) {
				request := httptest.NewRequestWithContext(
					tc.ctx, http.MethodGet, "http://127.0.0.1"+apiProbe, nil,
				)
				caller, err := h.service.ResolveCaller(request)
				if err == nil || errors.Is(err, domain.ErrUnauthorized) ||
					errors.Is(err, domain.ErrWrongToken) {
					t.Fatalf("ResolveCaller error = %v, want an internal error", err)
				}
				if caller != (domain.Caller{}) {
					t.Fatalf("ResolveCaller caller = %+v, want none", caller)
				}
			})
		}
	}
	expectInternal("open mode")
	h.enableToken(firstPassword)
	expectInternal("token mode")
}

// newSurfaceRouter builds the framework router over resolver with one probe
// route in the v1 registry, served on /api/v1 and /app/api/v1, one public
// panel route, and an MCP stand-in that, as framework/mcp.Handler does,
// resolves the caller itself before anything else and answers its error
// through WriteErr. An admitted probe answers with the caller's principal; the
// public route, which no resolver sees, answers "public".
func newSurfaceRouter(t *testing.T, resolver auth.CallerResolver) http.Handler {
	t.Helper()
	routes := &httpx.RouteRegistry{}
	routes.Register(httpx.Route{
		ID: "probe.get", Method: http.MethodGet, Pattern: "/probe",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller, err := auth.CallerFromContext(r.Context())
			if err != nil {
				httpx.WriteErr(w, err)
				return
			}
			_, _ = io.WriteString(w, caller.Principal)
		}),
	})
	panelRoutes := &httpx.RouteRegistry{}
	panelRoutes.Register(httpx.Route{
		ID: "session.get", Method: http.MethodGet, Pattern: "/session",
		Public: true,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "public")
		}),
	})
	mcp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, err := resolver(r)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		_, _ = io.WriteString(w, caller.Principal)
	})
	return newTestRouter(t, routes, panelRoutes, resolver, mcp)
}

// newTestRouter builds the framework router the package's tests drive, behind
// the browser guards of a loopback host: routes on /api/v1 and /app/api/v1,
// panelRoutes on /app/api/v1 only, resolver as the caller resolver, and mcp,
// nil for none, on the MCP endpoint.
func newTestRouter(
	t *testing.T,
	routes, panelRoutes *httpx.RouteRegistry,
	resolver auth.CallerResolver,
	mcp http.Handler,
) http.Handler {
	t.Helper()
	host, err := httpx.AllowedHosts([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("AllowedHosts: %v", err)
	}
	router, err := httpx.NewRouter(httpx.RouterConfig{
		Routes:         routes,
		PanelRoutes:    panelRoutes,
		Authorizer:     httpx.AllowAll{},
		CallerResolver: resolver,
		SPA:            fstest.MapFS{"index.html": {Data: []byte("<html></html>")}},
		MCP:            mcp,
		MCPPath:        mcpEndpoint,
		BodyLimit:      func(*http.Request) int64 { return 1 << 10 },
		Guards: httpx.BrowserGuards{
			Host:        host,
			Origin:      httpx.SameOrigin(),
			ContentType: httpx.JSONBody(),
		},
		// The tests request no route manifest, the only reader of the mode.
		AccessMode: func() httpx.AccessMode { return httpx.AccessModeOpen },
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return router
}

// expectAnswer serves req through router and checks the answer against want.
// It fails without printing the answer when the answer carries one of the
// secrets.
func expectAnswer(
	t *testing.T,
	router http.Handler,
	req surfaceRequest,
	want surfaceAnswer,
	secrets ...string,
) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+req.path, nil)
	if req.authorization != "" {
		request.Header.Set("Authorization", req.authorization)
	}
	if req.cookie != "" {
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: req.cookie})
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, request)
	body := rec.Body.String()
	challenge := rec.Header().Get("WWW-Authenticate")
	for _, secret := range secrets {
		if strings.Contains(body, secret) || strings.Contains(challenge, secret) {
			t.Fatalf("GET %s answered with a credential", req.path)
		}
	}
	expected := ""
	if want.challenge {
		expected = wantChallenge
	}
	if rec.Code != want.status || challenge != expected {
		t.Fatalf("GET %s = %d with WWW-Authenticate %q, want %d with %q: %s",
			req.path, rec.Code, challenge, want.status, expected, body)
	}
	if want.status == http.StatusOK {
		if body != want.body {
			t.Fatalf("GET %s body = %q, want %q", req.path, body, want.body)
		}
		return
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("GET %s: decode %q: %v", req.path, body, err)
	}
	if envelope.Error.Code != want.code ||
		!strings.Contains(envelope.Error.Message, want.message) {
		t.Fatalf("GET %s error = %s %q, want %s with %q", req.path,
			envelope.Error.Code, envelope.Error.Message, want.code, want.message)
	}
}
