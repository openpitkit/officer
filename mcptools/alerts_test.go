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

package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/alerts"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/internal/store/sqlite"
)

type alertToolSource struct {
	frameworkmcp.Source
	enabled bool
}

func (s alertToolSource) CommandEnabled(context.Context, string) (bool, error) { return s.enabled, nil }
func (alertToolSource) AccountFeatures() backend.AccountFeatures               { return backend.AccountFeatures{} }

func TestAlertToolsUseRegisteredServiceAndGuards(t *testing.T) {
	st, err := sqlite.New(filepath.Join(t.TempDir(), "alerts.db"), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	realm, err := st.ForRealm(t.Context(), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	validate := func(raw json.RawMessage) error {
		if string(raw) != "{}" {
			return domain.ErrInvalid
		}
		return nil
	}
	s, err := alerts.New(realm.(alerts.Store), []alerts.Type{{Name: "test", Validate: validate, Actions: map[string]alerts.Action{
		"accept": {Validate: validate, Resolve: func(context.Context, alerts.Event, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"accepted":true}`), nil
		}},
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Publish(auth.ContextWithCaller(t.Context(), auth.SystemCaller()), alerts.Event{ID: "event", Type: "test", At: time.Now(), Source: domain.SourceSystem, Payload: json.RawMessage("{}")}); err != nil {
		t.Fatal(err)
	}
	reg := frameworkmcp.NewToolRegistry()
	RegisterAlertTools(reg, s)
	caller := domain.Caller{Source: domain.SourceMCP, Principal: "agent"}
	for _, enabled := range []bool{false, true} {
		server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "alerts-test", Version: "1"}, nil)
		for _, d := range reg.Descriptors() {
			d.Register(server, frameworkmcp.RegisterDeps{Source: alertToolSource{enabled: enabled}, Authorizer: httpx.AllowAll{}, Caller: caller})
		}
		ctx, cancel := context.WithCancel(t.Context())
		serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
		done := make(chan error, 1)
		go func() { done <- server.Run(ctx, serverTransport) }()
		client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil)
		session, err := client.Connect(ctx, clientTransport, nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		call := func(name string, input map[string]any) *sdkmcp.CallToolResult {
			t.Helper()
			out, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: input})
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
		marked := call("set_alert_read", map[string]any{"id": "event", "read": true})
		if marked.IsError == enabled {
			t.Fatalf("command gate enabled=%v: %+v", enabled, marked)
		}
		if enabled {
			listed := call("list_alerts", map[string]any{})
			encoded, _ := json.Marshal(listed.StructuredContent)
			if listed.IsError || !strings.Contains(string(encoded), `"unread":0`) {
				t.Fatalf("list = %s %+v", encoded, listed)
			}
			foreign, foreignErr := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "set_alert_read", Arguments: map[string]any{"id": "event", "read": false, "principal": "alice"}})
			if foreignErr == nil && !foreign.IsError {
				t.Fatal("client principal accepted")
			}
			bad := call("resolve_alert", map[string]any{"id": "event", "action": "unknown", "input": map[string]any{}})
			if !bad.IsError {
				t.Fatal("unknown action accepted")
			}
			resolved := call("resolve_alert", map[string]any{"id": "event", "action": "accept", "input": map[string]any{}})
			encoded, _ = json.Marshal(resolved.StructuredContent)
			if resolved.IsError || !strings.Contains(string(encoded), `"actor":"agent"`) {
				t.Fatalf("resolution = %s %+v", encoded, resolved)
			}
		}
		cancel()
		runErr := <-done
		_ = session.Close()
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Fatal(runErr)
		}
	}
}
