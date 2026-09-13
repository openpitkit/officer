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

package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

func TestToolRegistryReplaceAndUnregister(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(ToolDescriptor{
		Name:             "alpha",
		Title:            "Alpha",
		AgentDescription: "first",
		DefaultEnabled:   true,
	})
	reg.Register(ToolDescriptor{
		Name:             "beta",
		Title:            "Beta",
		AgentDescription: "second",
		Mutating:         true,
	})
	reg.Register(ToolDescriptor{
		Name:             "alpha",
		Title:            "Alpha replacement",
		AgentDescription: "replacement",
		Mutating:         true,
		Protective:       true,
		Implemented:      true,
	})

	got := reg.Descriptors()
	if len(got) != 2 {
		t.Fatalf("descriptor count: want 2 got %d", len(got))
	}
	if got[0].Name != "alpha" || got[0].Title != "Alpha replacement" {
		t.Fatalf("replace should preserve position and update descriptor: %+v", got)
	}
	if got[1].Name != "beta" {
		t.Fatalf("replace should not reorder later descriptors: %+v", got)
	}

	cat := reg.Catalog().All()
	if len(cat) != 2 || cat[0].Name != "alpha" || cat[0].Title != "Alpha replacement" {
		t.Fatalf("catalog should reflect replacement: %+v", cat)
	}

	reg.Unregister("alpha")
	got = reg.Descriptors()
	if len(got) != 1 || got[0].Name != "beta" {
		t.Fatalf("unregister should remove descriptor: %+v", got)
	}
	cat = reg.Catalog().All()
	if len(cat) != 1 || cat[0].Name != "beta" {
		t.Fatalf("catalog should reflect unregister: %+v", cat)
	}
}

func TestGuardAuthorizerAndPanelGates(t *testing.T) {
	ctx := context.Background()
	allowSrc := &guardSource{enabled: true}
	read := ToolDescriptor{Name: "read", DefaultEnabled: true}
	caller := domain.Caller{Principal: "alice"}
	handler := Guard(
		read,
		RegisterDeps{Source: allowSrc, Authorizer: allowAuthorizer{}, Caller: caller},
		func(context.Context, *sdkmcp.ServerSession, struct{}) (string, string, error) {
			return "ran", "ok", nil
		},
	)
	res, err := handler(ctx, nil, &sdkmcp.CallToolParamsFor[struct{}]{})
	if err != nil {
		t.Fatalf("allow handler returned error: %v", err)
	}
	if res.IsError || res.StructuredContent != "ok" {
		t.Fatalf("allow handler result mismatch: %+v", res)
	}

	deny := Guard(
		read,
		RegisterDeps{Source: allowSrc, Authorizer: denyAuthorizer{}, Caller: caller},
		func(context.Context, *sdkmcp.ServerSession, struct{}) (string, string, error) {
			t.Fatal("denied tool body must not run")
			return "", "", nil
		},
	)
	res, err = deny(ctx, nil, &sdkmcp.CallToolParamsFor[struct{}]{})
	if err != nil {
		t.Fatalf("deny handler returned transport error: %v", err)
	}
	if !res.IsError || !contentContains(res.Content, "permission denied") {
		t.Fatalf("deny should return permission error result: %+v", res)
	}

	missing := Guard(
		read,
		RegisterDeps{Source: allowSrc, Caller: caller},
		func(context.Context, *sdkmcp.ServerSession, struct{}) (string, string, error) {
			t.Fatal("tool without an authorizer must not run")
			return "", "", nil
		},
	)
	res, err = missing(ctx, nil, &sdkmcp.CallToolParamsFor[struct{}]{})
	if err != nil {
		t.Fatalf("missing authorizer returned transport error: %v", err)
	}
	if !res.IsError || !contentContains(res.Content, "permission denied") {
		t.Fatalf("missing authorizer should deny the tool: %+v", res)
	}

	disabled := Guard(
		read,
		RegisterDeps{
			Source: &guardSource{enabled: false}, Authorizer: allowAuthorizer{}, Caller: caller,
		},
		func(context.Context, *sdkmcp.ServerSession, struct{}) (string, string, error) {
			t.Fatal("disabled tool body must not run")
			return "", "", nil
		},
	)
	res, err = disabled(ctx, nil, &sdkmcp.CallToolParamsFor[struct{}]{})
	if err != nil {
		t.Fatalf("disabled handler returned transport error: %v", err)
	}
	if res.IsError || !contentContains(res.Content, "disabled in the Pit Officer panel") {
		t.Fatalf("disabled should return non-error panel notice: %+v", res)
	}
}

func TestGuardGatePolicy(t *testing.T) {
	ctx := context.Background()
	readRan := false
	read := Guard(
		ToolDescriptor{Name: "read"},
		RegisterDeps{
			Source:     &guardSource{err: errors.New("store down")},
			Authorizer: allowAuthorizer{},
			Caller:     domain.Caller{Principal: "alice"},
		},
		func(context.Context, *sdkmcp.ServerSession, struct{}) (string, string, error) {
			readRan = true
			return "read", "ok", nil
		},
	)
	res, err := read(ctx, nil, &sdkmcp.CallToolParamsFor[struct{}]{})
	if err != nil {
		t.Fatalf("read handler returned error: %v", err)
	}
	if !readRan || res.IsError {
		t.Fatalf("read gate should fail open and run: ran=%v res=%+v", readRan, res)
	}

	mutatingRan := false
	mutating := Guard(
		ToolDescriptor{Name: "write", Mutating: true},
		RegisterDeps{
			Source:     &guardSource{err: errors.New("store down")},
			Authorizer: allowAuthorizer{},
			Caller:     domain.Caller{Principal: "alice"},
		},
		func(context.Context, *sdkmcp.ServerSession, struct{}) (string, string, error) {
			mutatingRan = true
			return "write", "ok", nil
		},
	)
	res, err = mutating(ctx, nil, &sdkmcp.CallToolParamsFor[struct{}]{})
	if err != nil {
		t.Fatalf("mutating handler returned transport error: %v", err)
	}
	if mutatingRan {
		t.Fatal("mutating gate must fail closed and skip body")
	}
	if !res.IsError || !contentContains(res.Content, "command access check failed; command not executed") {
		t.Fatalf("mutating gate should return fail-closed tool error: %+v", res)
	}
}

func TestBuildRejectsNilAuthorizer(t *testing.T) {
	_, err := Build(NewToolRegistry(), &guardSource{}, nil, nil, domain.Caller{})
	if err == nil {
		t.Fatal("Build accepted a nil authorizer")
	}
	if !strings.Contains(err.Error(), "nil authorizer") {
		t.Fatalf("Build error = %v, want the missing authorizer named", err)
	}
}

func TestBuildRunsToolAsItsCaller(t *testing.T) {
	src := &callerSource{}
	caller := domain.Caller{Source: domain.SourcePanel, Principal: "build-caller", Role: "ops"}
	server, err := Build(callerRegistry(), src, nil, allowAuthorizer{}, caller)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Run(ctx, serverTransport) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport)
	if err != nil {
		cancel()
		t.Fatalf("client connect: %v", err)
	}
	if _, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "caller"}); err != nil {
		cancel()
		t.Fatalf("call tool: %v", err)
	}
	_ = session.Close()
	cancel()
	if err := <-serverErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("server run: %v", err)
	}

	want := caller
	want.Source = domain.SourceMCP
	if got := src.onlyCaller(t); got != want {
		t.Fatalf("source caller = %+v, want %+v", got, want)
	}
}

func TestHandlerSessionRunsAsOpeningCaller(t *testing.T) {
	src := &callerSource{}
	h := newCallerHTTPHandler(t, src)
	httpServer := httptest.NewServer(h)
	defer httpServer.Close()

	transport := &principalTransport{principal: "session-owner"}
	session := connectHTTPClient(t, httpServer.URL, transport)
	defer func() { _ = session.Close() }()
	if _, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "caller"}); err != nil {
		t.Fatalf("call tool: %v", err)
	}

	want := domain.Caller{Source: domain.SourceMCP, Principal: "session-owner"}
	if got := src.onlyCaller(t); got != want {
		t.Fatalf("source caller = %+v, want %+v", got, want)
	}
}

func TestHandlerRejectsSessionReuseByDifferentCaller(t *testing.T) {
	src := &callerSource{}
	h := newCallerHTTPHandler(t, src)
	httpServer := httptest.NewServer(h)
	defer httpServer.Close()

	transport := &principalTransport{principal: "session-owner"}
	session := connectHTTPClient(t, httpServer.URL, transport)
	transport.setPrincipal("intruder")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "caller"}); err == nil {
		t.Fatal("tool call under a different caller succeeded")
	}
	if got := src.callerCount(); got != 0 {
		t.Fatalf("tool ran %d times, want 0", got)
	}
	transport.setPrincipal("session-owner")
	_ = session.Close()
}

func TestSessionBindingHandlerRejectsUnknownSession(t *testing.T) {
	sdkRan := false
	h := &sessionBindingHandler{
		sdk: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			sdkRan = true
		}),
		callers: make(map[string]domain.Caller),
	}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Mcp-Session-Id", "unknown")
	rec := httptest.NewRecorder()

	h.serveHTTP(rec, req, domain.Caller{Principal: "caller"})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want WriteErr JSON response", got)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"code":"not_found"`) {
		t.Fatalf("body = %q, want WriteErr not_found envelope", body)
	}
	if sdkRan {
		t.Fatal("SDK handled a request for an unbound session")
	}
}

func TestSessionResponseWriterFlushesAfterBinding(t *testing.T) {
	caller := domain.Caller{Principal: "caller"}
	h := &sessionBindingHandler{callers: make(map[string]domain.Caller)}
	underlying := &flushResponseWriter{header: make(http.Header)}
	underlying.beforeFlush = func() {
		h.mu.Lock()
		bound, exists := h.callers["opened"]
		h.mu.Unlock()
		if !exists || bound != caller {
			t.Fatalf("binding at Flush = (%+v, %v), want (%+v, true)", bound, exists, caller)
		}
	}
	h.sdk = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("session response writer does not implement http.Flusher")
		}
		w.Header().Set("Mcp-Session-Id", "opened")
		flusher.Flush()
	})

	h.serveHTTP(underlying, httptest.NewRequest(http.MethodPost, "/", nil), caller)

	if !underlying.flushed {
		t.Fatal("Flush did not reach the underlying response writer")
	}
	if underlying.status != http.StatusOK {
		t.Fatalf("status = %d, want Flush to write %d first", underlying.status, http.StatusOK)
	}
}

func TestHandlerRejectsNilRegistry(t *testing.T) {
	_, err := Handler(
		nil, &guardSource{}, nil, allowAuthorizer{},
		func(*http.Request) (domain.Caller, error) { return domain.Caller{}, nil },
	)
	if err == nil {
		t.Fatal("Handler accepted a nil registry")
	}
	if !strings.Contains(err.Error(), "nil registry") {
		t.Fatalf("Handler error = %v, want the missing registry named", err)
	}
}

func TestHandlerResolverErrorNeverReachesSDK(t *testing.T) {
	h, err := Handler(
		callerRegistry(), &callerSource{}, nil, allowAuthorizer{},
		func(*http.Request) (domain.Caller, error) {
			return domain.Caller{}, domain.ErrForbidden
		},
	)
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not MCP JSON"))
	req.Header.Set("Accept", "invalid")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; SDK handled the request", rec.Code, http.StatusForbidden)
	}
	if strings.Contains(rec.Body.String(), "Accept must contain") {
		t.Fatalf("SDK handled the rejected request: %q", rec.Body.String())
	}
}

func TestCatalogCopy(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(ToolDescriptor{Name: "alpha", Title: "Alpha"})
	cat := reg.Catalog()

	got := cat.All()
	got[0].Title = "mutated"
	if again := cat.All(); len(again) != 1 || again[0].Title != "Alpha" {
		t.Fatalf("catalog All must return a copy: %+v", again)
	}
}

type allowAuthorizer struct{}

func (allowAuthorizer) Authorize(context.Context, domain.Caller, string) error {
	return nil
}

type denyAuthorizer struct{}

func (denyAuthorizer) Authorize(context.Context, domain.Caller, string) error {
	return domain.ErrForbidden
}

type guardSource struct {
	err     error
	enabled bool
}

type callerSource struct {
	guardSource
	mu      sync.Mutex
	callers []domain.Caller
}

func (s *callerSource) CommandEnabled(ctx context.Context, _ string) (bool, error) {
	s.mu.Lock()
	s.callers = append(s.callers, auth.CallerFromContext(ctx))
	s.mu.Unlock()
	return true, nil
}

func (s *callerSource) onlyCaller(t *testing.T) domain.Caller {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.callers) != 1 {
		t.Fatalf("source observed %d callers, want 1", len(s.callers))
	}
	return s.callers[0]
}

func (s *callerSource) callerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.callers)
}

func callerRegistry() *ToolRegistry {
	reg := NewToolRegistry()
	d := ToolDescriptor{Name: "caller", DefaultEnabled: true}
	d.Register = func(server *sdkmcp.Server, deps RegisterDeps) {
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: d.Name}, Guard(
			d,
			deps,
			func(context.Context, *sdkmcp.ServerSession, struct{}) (string, string, error) {
				return "ok", "ok", nil
			},
		))
	}
	reg.Register(d)
	return reg
}

func newCallerHTTPHandler(t *testing.T, src Source) http.Handler {
	t.Helper()
	h, err := Handler(
		callerRegistry(), src, nil, allowAuthorizer{},
		func(r *http.Request) (domain.Caller, error) {
			return domain.Caller{Principal: r.Header.Get("X-Test-Principal")}, nil
		},
	)
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}
	return h
}

type principalTransport struct {
	mu        sync.Mutex
	principal string
}

type flushResponseWriter struct {
	header      http.Header
	status      int
	flushed     bool
	beforeFlush func()
}

func (w *flushResponseWriter) Header() http.Header {
	return w.header
}

func (w *flushResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *flushResponseWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *flushResponseWriter) Flush() {
	if w.beforeFlush != nil {
		w.beforeFlush()
	}
	w.flushed = true
}

func (t *principalTransport) setPrincipal(principal string) {
	t.mu.Lock()
	t.principal = principal
	t.mu.Unlock()
}

func (t *principalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	principal := t.principal
	t.mu.Unlock()
	req = req.Clone(req.Context())
	req.Header.Set("X-Test-Principal", principal)
	return http.DefaultTransport.RoundTrip(req)
}

func connectHTTPClient(
	t *testing.T,
	url string,
	transport http.RoundTripper,
) *sdkmcp.ClientSession {
	t.Helper()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(
		context.Background(),
		sdkmcp.NewStreamableClientTransport(url, &sdkmcp.StreamableClientTransportOptions{
			HTTPClient: &http.Client{Transport: transport},
		}),
	)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	return session
}

func (s *guardSource) Status(context.Context) (Status, error) {
	return Status{}, nil
}

func (s *guardSource) GetAccountState(context.Context, domain.AccountID) (
	domain.Account,
	node.AccountLimits,
	error,
) {
	return domain.Account{}, node.AccountLimits{}, nil
}

func (s *guardSource) ListGroups(context.Context) (
	[]domain.AccountGroup,
	error,
) {
	return nil, nil
}

func (s *guardSource) ListLimits(context.Context, domain.AccountID) (
	node.AccountLimits,
	error,
) {
	return node.AccountLimits{}, nil
}

func (s *guardSource) ListAudit(context.Context, int) ([]domain.AuditRow, error) {
	return nil, nil
}

func (s *guardSource) ListAuditFiltered(
	context.Context,
	domain.AuditFilter,
	int,
) ([]domain.AuditRow, error) {
	return nil, nil
}

func (s *guardSource) CheckOrder(context.Context, domain.OrderProbe) (domain.CheckResult, error) {
	return domain.CheckResult{}, nil
}

func (s *guardSource) GetOrder(context.Context, string) (domain.OrderDetail, error) {
	return domain.OrderDetail{}, nil
}

func (s *guardSource) SetMarketDataInstrumentEnabled(
	context.Context,
	string,
	string,
	bool,
) error {
	return nil
}

func (s *guardSource) CommandEnabled(context.Context, string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.enabled, nil
}

func (s *guardSource) SubmitOrderToken(
	context.Context,
	domain.Order,
	string,
	domain.MissingAccountPolicy,
) (SubmitOrderTokenResult, error) {
	return SubmitOrderTokenResult{}, nil
}

func (s *guardSource) SubmitDropCopyOrder(
	context.Context, domain.Order, domain.MissingAccountPolicy,
) (SubmitDropCopyOrderResult, error) {
	return SubmitDropCopyOrderResult{}, nil
}

func (s *guardSource) ConfirmExecution(
	context.Context,
	string,
	string,
) (domain.Order, Attestation, error) {
	return domain.Order{}, Attestation{}, nil
}

func (s *guardSource) CancelOrder(
	context.Context,
	string,
	string,
	string,
	string,
) (domain.Order, Attestation, error) {
	return domain.Order{}, Attestation{}, nil
}

func contentContains(content []sdkmcp.Content, needle string) bool {
	for _, c := range content {
		if text, ok := c.(*sdkmcp.TextContent); ok && strings.Contains(text.Text, needle) {
			return true
		}
	}
	return false
}
