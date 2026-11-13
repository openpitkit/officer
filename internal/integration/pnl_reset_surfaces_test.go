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

package integration_test

import (
	"context"
	"errors"
	"github.com/go-chi/chi/v5"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/httpapi"
	"go.openpit.dev/officer/mcptools"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type resetBoundary struct {
	calls int
	reads int
}

func (b *resetBoundary) ResetAccountPnl(context.Context, domain.AccountID, string, domain.Caller) (domain.Account, error) {
	b.calls++
	return domain.Account{}, nil
}
func (b *resetBoundary) Read(context.Context) (backend.AccountPnlResetScheduleState, error) {
	b.reads++
	return backend.AccountPnlResetScheduleState{}, nil
}
func (b *resetBoundary) Save(context.Context, backend.AccountPnlResetScheduleInput, domain.Caller) (backend.AccountPnlResetScheduleState, error) {
	b.calls++
	return backend.AccountPnlResetScheduleState{}, nil
}
func (b *resetBoundary) Recover(context.Context, int64, string, domain.Caller) (backend.AccountPnlResetScheduleState, error) {
	b.calls++
	return backend.AccountPnlResetScheduleState{}, nil
}

func TestResetAgentDeniedAcrossRESTAndReadOnlyMCP(t *testing.T) {
	svc, _ := newTestService(t)
	boundary := &resetBoundary{}
	cap, err := backend.NewAccountPnlResetControl(svc, boundary, boundary, backend.EveryAccount{})
	if err != nil {
		t.Fatal(err)
	}
	agent := domain.Caller{Source: domain.SourceMCP, Principal: "agent"}
	ctx := auth.ContextWithCaller(testCtx(t), agent)
	routes := &httpx.RouteRegistry{}
	httpapi.RegisterAccountPnlResetRoutes(routes, svc, cap)
	router := chi.NewRouter()
	for _, route := range routes.Routes() {
		router.Method(route.Method, route.Pattern, route.Handler)
	}
	for _, test := range []struct{ method, path, body string }{
		{"POST", "/accounts/desk/realized-pnl/reset", ""},
		{"PUT", "/account-pnl-reset-schedule", `{"enabled":true,"time":"09:00","timezone":"UTC","termination":{"mode":"forever"},"revision":0}`},
		{"POST", "/account-pnl-reset-schedule/recover", `{"revision":1,"action":"retry"}`},
	} {
		req := httptest.NewRequestWithContext(ctx, test.method, test.path, strings.NewReader(test.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "loosening_refused") {
			t.Fatalf("agent REST %s=%d %s", test.path, rec.Code, rec.Body)
		}
	}
	reg := frameworkmcp.NewToolRegistry()
	for _, body := range []string{`{"time":"09:00","timezone":"UTC","termination":{"mode":"forever"},"revision":0}`, `{"enabled":true,"time":"09:00","timezone":"UTC","termination":{"mode":"forever"}}`} {
		req := httptest.NewRequestWithContext(systemCtx(), http.MethodPut, "/account-pnl-reset-schedule", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("missing explicit input=%d %s", rec.Code, rec.Body)
		}
	}
	mcptools.RegisterAccountPnlResetTools(reg, cap)
	src := enabledControlPlaneSource{frameworkmcp.SourceFor(svc)}
	server, err := frameworkmcp.Build(reg, src, nil, httpx.AllowAll{}, agent)
	if err != nil {
		t.Fatal(err)
	}
	serverSide, clientSide := sdkmcp.NewInMemoryTransports()
	serverCtx, cancel := context.WithCancel(context.Background())
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Run(serverCtx, serverSide) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "reset-test"}, nil)
	session, err := client.Connect(testCtx(t), clientSide, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() {
		cancel()
		if err := <-serverErr; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	}()
	listed, err := session.ListTools(testCtx(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 1 || listed.Tools[0].Name != "get_account_pnl_reset_schedule" {
		t.Fatalf("reset MCP capability must be read-only: %+v", listed.Tools)
	}
	result, err := session.CallTool(testCtx(t), &sdkmcp.CallToolParams{Name: "get_account_pnl_reset_schedule", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("read MCP=%+v %v", result, err)
	}
	if boundary.calls != 0 || boundary.reads != 1 {
		t.Fatalf("boundary writes=%d reads=%d", boundary.calls, boundary.reads)
	}
}
func TestResetScopeRefusesHiddenAccountAndRealmSchedule(t *testing.T) {
	scope := &recordingScope{account: "desk", hidden: []domain.AccountID{"desk"}}
	svc := newOfficerService(t, &fakeNode{}, nil, nil, backend.WithAccountScope(scope))
	boundary := &resetBoundary{}
	cap, err := backend.NewAccountPnlResetControl(svc, boundary, boundary, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cap.ResetAccountPnl(systemCtx(), "outside"); !errors.Is(err, errScopeRefused) {
		t.Fatalf("hidden account=%v", err)
	}
	// Schedule authority is an explicit dependency even when the service itself
	// exposes every account; it must not inherit that broader scope.
	unrestricted := newOfficerService(t, &fakeNode{}, nil, nil)
	cap, err = backend.NewAccountPnlResetControl(unrestricted, boundary, boundary, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cap.AccountPnlResetSchedule(systemCtx()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("restricted schedule read=%v", err)
	}
	if _, err := cap.PutAccountPnlResetSchedule(systemCtx(), backend.AccountPnlResetScheduleInput{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("restricted schedule write=%v", err)
	}
	if boundary.calls != 0 || boundary.reads != 0 {
		t.Fatalf("scope refusal reached boundary %+v", boundary)
	}
	if _, err := backend.NewAccountPnlResetControl(unrestricted, boundary, boundary, nil); err == nil {
		t.Fatal("missing explicit schedule authority was accepted")
	}
}
