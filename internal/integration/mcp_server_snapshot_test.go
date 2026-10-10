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

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func TestServerToolSnapshot(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(systemCtx(), 5*time.Second)
	defer cancel()

	server, err := frameworkmcp.Build(
		officerToolRegistry(), &fakeSource{}, nil, httpx.AllowAll{}, operatorCaller(),
	)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Run(ctx, serverTransport)
	}()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() {
		_ = session.Close()
		cancel()
		if err := <-serverErr; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("server run: %v", err)
		}
	}()

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := []struct {
		name        string
		description string
	}{
		{
			name: "health",
			description: "Report Pit Officer deployment health: per-node " +
				"engine version, build profile and liveness, and store reachability, " +
				"schema version and path. Read-only status only - exposes no secrets " +
				"and no order-flow control.",
		},
		{
			name: "get_account_state",
			description: "Return account row and its account-scoped risk barriers. " +
				"Read-only - no secrets, no order-flow control.",
		},
		{
			name: "get_limits",
			description: "Return risk barriers and P&L-bound currencies, optionally " +
				"filtered by account. Read-only - no secrets, no order-flow control.",
		},
		{
			name: "get_order",
			description: "Return one order addressed by its id: its " +
				"status, its 1:1 signed approval (when issued), and its fills with " +
				"their display prices. Read-only - no secrets, no order-flow control. " +
				"The opaque pre-trade lock is never exposed; only backend-derived " +
				"display prices are.",
		},
		{
			name: "get_audit",
			description: "Return recent control-plane audit entries (default 50, " +
				"max 500). Read-only - no secrets, no order-flow control.",
		},
		{
			name: "check_order",
			description: "Run a non-mutating pre-trade dry-run for an order: report " +
				"whether it would pass the engine's risk checks plus the reasons " +
				"(would-be lock prices on pass, structured rejects and any would-be " +
				"account block on reject). Submits nothing and changes no state - no " +
				"order is placed, no funds are reserved.",
		},
		{
			name: "set_market_data_instrument",
			description: "Enable or disable one configured market-data instrument. " +
				"Mutates Officer control-plane config only; protected and disabled " +
				"by default.",
		},
		{
			name: "submit_order",
			description: "Submit an order intent through pre-trade and obtain an " +
				"approval envelope. Mutates engine state and records the pre-trade " +
				"lock; protected and disabled by default.",
		},
		{
			name: "submit_drop_copy_order",
			description: "Submit a drop-copy order through the normal policy pipeline " +
				"while ignoring policy rejects and existing account or group kill-switch " +
				"blocks. Policies retain their normal state changes. The id is optional; " +
				"when omitted, the store assigns it. No lifecycle event is signed. " +
				"Mutates engine state; protected and disabled by default.",
		},
		{
			name: "confirm_execution",
			description: "Record confirmation history for a workflow order from the " +
				"signed hold approval Officer recorded at submit. The shortcut is " +
				"rejected after execution-report activity; protected and disabled by " +
				"default.",
		},
		{
			name: "cancel",
			description: "Cancel an untouched workflow order from the signed hold " +
				"approval Officer recorded at submit. Optional caller-reported " +
				"leavesQuantity is recorded verbatim when supplied; the SDK receives " +
				"the order's own recorded reservation remainder. After execution-report " +
				"activity, submit an explicit report. " +
				"Protected and disabled by default.",
		},
	}
	want = append(want,
		struct {
			name        string
			description string
		}{name: "send_paper_order", description: "Check, hold and send an order to a paper venue. Returns the approval and " +
			"acknowledged, refused or unknown send outcome. Mutating, protected and " +
			"disabled by default. send_live_order comes with the live-trading token."},
		struct {
			name        string
			description string
		}{name: "get_trading", description: "Read trading providers and connections. Read-only - credentials are never exposed."},
		struct {
			name        string
			description string
		}{name: "create_trading_connection", description: "Create a trading connection with write-only credentials. Mutates Officer control-plane config; protected and disabled by default."},
		struct {
			name        string
			description string
		}{name: "update_trading_connection", description: "Update a trading connection label or write-only credentials. Mutates Officer control-plane config; protected and disabled by default."},
		struct {
			name        string
			description string
		}{name: "set_trading_connection_enabled", description: "Enable or disable a trading connection. Mutates Officer control-plane config; protected and disabled by default."},
		struct {
			name        string
			description string
		}{name: "check_trading_connection", description: "Check the venue identity and status of a trading connection. Read-only - credentials are never exposed."},
		struct {
			name        string
			description string
		}{name: "set_trading_instrument", description: "Set a trading instrument mapping. Mutates Officer control-plane config; protected and disabled by default."},
		struct {
			name        string
			description string
		}{name: "delete_trading_instrument", description: "Delete a trading instrument mapping. Mutates Officer control-plane config; protected and disabled by default."},
		struct {
			name        string
			description string
		}{name: "verify_trading_symbol", description: "Verify whether a venue symbol exists and is tradable. Read-only - credentials are never exposed."},
		struct {
			name        string
			description string
		}{name: "search_trading_symbols", description: "Search venue symbols for a trading connection. Read-only - credentials are never exposed."},
		struct {
			name        string
			description string
		}{name: "grant_trading_access", description: "Grant an Officer account or an account group access to a trading connection. A group grant needs the connection's fee account on providers with unlinked fees. Mutates Officer control-plane config; protected and disabled by default."},
		struct {
			name        string
			description string
		}{name: "revoke_trading_access", description: "Revoke the access of an Officer account or an account group to a trading connection. Mutates Officer control-plane config; protected and disabled by default."},
		struct {
			name        string
			description string
		}{name: "set_trading_fee_account", description: "Set the Officer account receiving unlinked venue fees. Mutates Officer control-plane config; protected and disabled by default."},
		struct {
			name        string
			description string
		}{name: "clear_trading_fee_account", description: "Clear the Officer account receiving unlinked venue fees. Mutates Officer control-plane config; protected and disabled by default."},
		struct {
			name        string
			description string
		}{name: "list_trading_destinations", description: "List the trading destinations available to an Officer account. Read-only - credentials are never exposed."},
	)
	if len(list.Tools) != len(want) {
		t.Fatalf("tool count: want %d got %d", len(want), len(list.Tools))
	}
	gotByName := make(map[string]*sdkmcp.Tool, len(list.Tools))
	for _, got := range list.Tools {
		gotByName[got.Name] = got
	}
	t.Run("paper-send-schema", func(t *testing.T) {
		type schema struct {
			Type        any                `json:"type"`
			Description string             `json:"description"`
			Required    []string           `json:"required"`
			Properties  map[string]*schema `json:"properties"`
		}
		raw, err := json.Marshal(gotByName["send_paper_order"].InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var input schema
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		dest := input.Properties["destination"]
		if dest == nil || dest.Type != "object" || dest.Properties["route"] == nil || dest.Properties["route"].Type != "object" {
			t.Fatal("send destination.route must advertise an object")
		}
		for field, property := range dest.Properties {
			if property.Description == "" {
				t.Errorf("send destination.%s has no description", field)
			}
		}
		for _, name := range input.Required {
			if name == "id" || name == "price" {
				t.Fatalf("send requires optional %s", name)
			}
		}
		if len(dest.Required) != 1 || dest.Required[0] != "route" {
			t.Fatal("send must require route only in destination")
		}
		if _, ok := gotByName["send_live_order"]; ok {
			t.Fatal("live send must wait for the live-trading token")
		}
	})
	t.Run("venue-route-schema", func(t *testing.T) {
		type schema struct {
			Type       any                `json:"type"`
			Properties map[string]*schema `json:"properties"`
		}
		raw, err := json.Marshal(gotByName["get_order"].OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var output schema
		if err := json.Unmarshal(raw, &output); err != nil {
			t.Fatal(err)
		}
		venue := output.Properties["venueOrder"]
		if venue == nil || venue.Properties["route"] == nil || venue.Properties["route"].Type != "object" {
			t.Fatalf("get_order venueOrder.route must advertise an object: %s", raw)
		}
	})
	t.Run("trading-input-descriptions", func(t *testing.T) {
		for name, tool := range gotByName {
			if !strings.Contains(name, "trading") && name != "send_paper_order" {
				continue
			}
			raw, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var input struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				t.Fatal(err)
			}
			for field, property := range input.Properties {
				if property.Description == "" {
					t.Errorf("%s.%s has no input description", name, field)
				}
				if field == "credentials" && (!strings.Contains(property.Description, "rite-only") || !strings.Contains(property.Description, "{apiKey, apiSecret}")) {
					t.Errorf("%s credentials must describe write-only Alpaca credentials", name)
				}
				if name == "update_trading_connection" && (field == "label" || field == "credentials") && !strings.Contains(property.Description, "omitted keeps the stored value") {
					t.Errorf("%s.%s must describe omitted settings", name, field)
				}
				if field == "mode" && (!strings.Contains(property.Description, "paper") || !strings.Contains(property.Description, "live")) {
					t.Errorf("%s.mode must describe paper and live", name)
				}
				if name == "list_trading_destinations" && field == "mode" && !strings.Contains(property.Description, "omitted lists every mode") {
					t.Error("destination mode must describe listing every mode")
				}
				if field == "id" && !strings.Contains(property.Description, "store assign") {
					t.Errorf("%s.id must describe store assignment", name)
				}
				if strings.HasSuffix(name, "_trading_access") && (field == "account" || field == "accountGroup") &&
					!strings.Contains(property.Description, "exactly one of account or accountGroup") {
					t.Errorf("%s.%s must describe the one-subject rule", name, field)
				}
			}
		}
	})
	for _, w := range want {
		got, ok := gotByName[w.name]
		if !ok {
			t.Fatalf("missing tool %q in tools/list: %+v", w.name, list.Tools)
		}
		if got.Description != w.description {
			t.Fatalf("tool %s description mismatch\nwant: %q\ngot:  %q",
				w.name, w.description, got.Description)
		}
		if got.InputSchema == nil || got.OutputSchema == nil {
			t.Fatalf("tool %s must advertise input and output schemas", w.name)
		}
	}
	for _, name := range []string{
		"get_order", "submit_order", "send_paper_order", "submit_drop_copy_order", "confirm_execution", "cancel",
		"set_market_data_instrument",
	} {
		tool := gotByName[name]
		for schemaName, schema := range map[string]any{
			"input": tool.InputSchema, "output": tool.OutputSchema,
		} {
			raw, err := json.Marshal(schema)
			if err != nil {
				t.Fatalf("marshal %s %s schema: %v", name, schemaName, err)
			}
			wire := string(raw)
			if name == "submit_drop_copy_order" && schemaName == "input" {
				var shape struct {
					Required []string `json:"required"`
				}
				if err := json.Unmarshal(raw, &shape); err != nil {
					t.Fatalf("decode drop-copy input schema: %v", err)
				}
				for _, required := range shape.Required {
					if required == "id" {
						t.Fatalf("drop-copy input schema requires optional id: %s", wire)
					}
				}
			}
			if name == "cancel" && schemaName == "input" {
				var shape struct {
					Properties map[string]json.RawMessage `json:"properties"`
					Required   []string                   `json:"required"`
				}
				if err := json.Unmarshal(raw, &shape); err != nil {
					t.Fatalf("decode cancel input schema: %v", err)
				}
				if _, ok := shape.Properties["leavesQuantity"]; !ok {
					t.Fatalf("cancel input schema has no leavesQuantity: %s", wire)
				}
				if _, ok := shape.Properties["token"]; ok {
					t.Fatalf("cancel input schema still has token: %s", wire)
				}
				for _, required := range shape.Required {
					if required == "leavesQuantity" {
						t.Fatalf("cancel input schema requires optional leavesQuantity: %s", wire)
					}
				}
			}
			if name == "confirm_execution" && schemaName == "input" {
				var shape struct {
					Properties map[string]json.RawMessage `json:"properties"`
				}
				if err := json.Unmarshal(raw, &shape); err != nil {
					t.Fatalf("decode confirm input schema: %v", err)
				}
				if _, ok := shape.Properties["token"]; ok {
					t.Fatalf("confirm input schema still has token: %s", wire)
				}
			}
			if !strings.Contains(wire, `"id"`) {
				t.Errorf("%s %s schema has no id: %s", name, schemaName, wire)
			}
			for _, alias := range []string{
				"externalId", "orderExternalId", "instanceExternalId",
			} {
				if strings.Contains(wire, alias) {
					t.Errorf("%s %s schema exposes %s: %s", name, schemaName, alias, wire)
				}
			}
		}
	}
}
