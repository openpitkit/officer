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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
)

func TestListMcpAccess(t *testing.T) {
	svc := &fakeService{
		mcpCommands: []backend.McpCommand{
			{Command: backend.Command{
				Name: "health", Title: "Health", AgentDescription: "desc",
				Implemented: true, DefaultEnabled: true,
			}, Enabled: false},
			{Command: backend.Command{
				Name: "set_limit", Title: "Set limit", AgentDescription: "desc",
				Mutating: true, Protective: true,
			}, Enabled: true},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/mcp-access", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	commands, ok := m["commands"].([]any)
	if !ok || len(commands) != 2 {
		t.Fatalf("want 2 commands, got %v", m["commands"])
	}
	first := commands[0].(map[string]any)
	if first["name"] != "health" || first["enabled"] != false || first["implemented"] != true {
		t.Fatalf("unexpected first command: %v", first)
	}
}

func TestSetMcpAccess_Persists(t *testing.T) {
	svc := &fakeService{
		mcpCommands: []backend.McpCommand{
			{Command: backend.Command{Name: "health", Title: "Health", AgentDescription: "d"}, Enabled: false},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"enabled":false}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/mcp-access/health", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if len(svc.setMcpCalls) != 1 || svc.setMcpCalls[0].command != "health" ||
		svc.setMcpCalls[0].enabled != false {
		t.Fatalf("expected persisted toggle, got %+v", svc.setMcpCalls)
	}
	m := bodyMap(t, rec.Result())
	cmd, ok := m["command"].(map[string]any)
	if !ok || cmd["name"] != "health" {
		t.Fatalf("expected command in body, got %v", m)
	}
}

func TestSetMcpAccess_UnknownCommandNotFound(t *testing.T) {
	svc := &fakeService{setMcpErr: fmt.Errorf("mcp command %q: %w", "nope", domain.ErrNotFound)}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"enabled":true}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/mcp-access/nope", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestSetMcpAccess_InvalidJSON(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{bad`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/mcp-access/health", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	if len(svc.setMcpCalls) != 0 {
		t.Fatalf("invalid JSON must not persist")
	}
}

func TestSetMcpAccess_RejectsUnknownAndMissingEnabled(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"unknown member", `{"enable":true}`},
		{"missing member", `{}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(
				http.MethodPut, "/api/v1/mcp-access/health",
				bytes.NewBufferString(tc.body),
			))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("want 400, got %d", rec.Code)
			}
			if len(svc.setMcpCalls) != 0 {
				t.Fatalf("invalid request persisted: %+v", svc.setMcpCalls)
			}
		})
	}
}
