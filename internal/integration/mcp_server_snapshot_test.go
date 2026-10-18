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
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerToolSnapshot(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(systemCtx(), 5*time.Second)
	defer cancel()
	session, closeSession := officerSession(t, ctx, &fakeSource{})
	defer closeSession()

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
			name: "list_orders",
			description: listToolDescription(
				"Return one page of orders; each row is the order row of the REST order list.",
				"account (exact account code), source, side (buy | sell), status (order "+
					"statuses), baseAsset and quoteAsset (text match), amount and price "+
					"(decimal range), at (time range)",
				"account, amountValue, at, baseAsset, price, quoteAsset, side, source, status",
			),
		},
		{
			name: "list_trades",
			description: listToolDescription(
				"Return one page of trades; each row is the trade row of the REST trade list.",
				"account, baseAsset and quoteAsset (text match), id (exact trade id), side "+
					"(buy | sell), source, at (time range), quantity, price and lockPrice "+
					"(decimal range)",
				"account, at, baseAsset, lockPrice, price, quantity, quoteAsset, side, source",
			),
		},
		{
			name: "list_accounts",
			description: listToolDescription(
				"Return one page of accounts; each row is the account row of the REST "+
					"account list, its block joined with its group's.",
				"group (exact group code; an empty string selects accounts in no group), "+
					"code (code or title) and blockReason (text match), status (active | "+
					"blocked), position (open-position count range), pnl (P&L bounds in "+
					"pnl.currency) with includeUnsetPnl and includeHaltedPnl",
				"blockReason, code, group, positionCount, status, title, venueAccess",
			),
		},
		{
			name: "list_groups",
			description: listToolDescription(
				"Return one page of account groups; each row is the group row of the REST "+
					"group list. The first page also carries the default group when it "+
					"matches the filters, outside the limit and the total.",
				"code (code or title), notes and blockReason (text match), status (active "+
					"| blocked), position (open-position count range), account "+
					"(member-account count range)",
				"accountCount, blockReason, code, notes, positionCount, status, title, venueAccess",
			),
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
			name: "create_account",
			description: "Create an account with a code, an optional title and an " +
				"optional realized P&L currency. Without its own currency the account " +
				"inherits its group's, then the default group's; with no tier set it " +
				"tracks no realized P&L. Mutates Officer control-plane config; " +
				"protected and disabled by default.",
		},
		{
			name: "update_account",
			description: "Change an account's code and title; the title replaces the " +
				"stored one and an empty title clears it. Mutates Officer control-plane " +
				"config; protected and disabled by default.",
		},
		{
			name: "delete_account",
			description: "Delete an account. An account with dependent rows is refused " +
				"with has_dependents unless force deletes them with it; a trading " +
				"connection's fee account is refused with conflict, even with force. " +
				"Force-deleting removes the account's venue-fee adjustments, so fees it " +
				"received may be booked again to the connection's current fee account " +
				"on the next restart. Mutates Officer control-plane config; protected " +
				"and disabled by default.",
		},
		{
			name: "set_account_currency",
			description: "Set or clear an account's own realized P&L currency; an " +
				"empty currency clears it, so the account inherits its group's, then " +
				"the default group's, and tracks no realized P&L when no tier is set. " +
				"A change of the account's effective currency is refused with " +
				"currency_change_blocked while the account holds any non-zero " +
				"balance, position, cost-basis or P&L value, a P&L halt or an active " +
				"order, or while a Spot Funds P&L bound applies to it. Mutates Officer " +
				"control-plane config; protected and disabled by default.",
		},
		{
			name: "apply_adjustment",
			description: "Apply an adjustment to an account's balances of one asset " +
				"through the engine and return the recorded adjustment. Each of " +
				"balance (available), held and incoming is set to a value (absolute) " +
				"or changed by one (delta), given as exact decimal strings. A bound " +
				"constrains the value the adjustment results in, not the change: a " +
				"result outside it records the adjustment as rejected with a " +
				"structured reason and changes no balance, a policy outcome rather " +
				"than an error. An adjustment that changes nothing, such as one " +
				"naming no field, is not recorded and returns no adjustment. Mutates " +
				"account balances; protected and disabled by default.",
		},
		{
			name: "create_asset",
			description: "Create an asset with a code, an optional title and the " +
				"optional code of an existing asset class. Mutates Officer " +
				"control-plane config; protected and disabled by default.",
		},
		{
			name: "update_asset",
			description: "Change an asset's code, title and asset class; the title " +
				"and the asset class replace the stored ones, and an omitted or empty " +
				"one clears it. Mutates Officer control-plane config; protected and " +
				"disabled by default.",
		},
		{
			name: "delete_asset",
			description: "Delete an asset. An asset with dependent rows is refused " +
				"with has_dependents unless force deletes them with it, the market-data " +
				"and trading instruments that reference the asset included; an asset an " +
				"account or group holds as its currency is refused with has_dependents, " +
				"even with force. Mutates Officer control-plane config; protected and " +
				"disabled by default.",
		},
		{
			name: "create_group",
			description: "Create an account group with a code, an optional title, an " +
				"optional realized P&L currency and optional notes. A member account " +
				"without its own currency inherits the group's, then the default " +
				"group's; without a currency the group tier stays unset. The code - is " +
				"reserved for the default group and refused with validation. Mutates " +
				"Officer control-plane config; protected and disabled by default.",
		},
		{
			name: "update_group",
			description: "Change an account group's code and title; the title " +
				"replaces the stored one and an empty title clears it. The default " +
				"group cannot be changed, and the code - is reserved for it and refused " +
				"with validation. Mutates Officer control-plane config; protected and " +
				"disabled by default.",
		},
		{
			name: "delete_group",
			description: "Delete an account group. Its member accounts leave the group " +
				"and keep their trading and compliance history, and a block the group " +
				"carries no longer applies to them; the delete is refused with " +
				"currency_change_blocked, even with force, when leaving would " +
				"change the effective currency of a member carrying protected state. A " +
				"group with a group-scoped Spot Funds P&L bound is refused with " +
				"has_dependents unless force deletes the bound with it. The group's " +
				"trading access grants are deleted with it; its former members keep " +
				"only their own. The default group cannot be deleted. Mutates Officer " +
				"control-plane config; protected and disabled by default.",
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
		{
			name: "report_fill",
			description: "Apply an execution report - a fill, a commission or a " +
				"lifecycle status - to an order returned by submit_order or " +
				"submit_drop_copy_order; the instrument, account and side come from " +
				"the order. A report on an ordinary order is signed and attested " +
				"fail-closed and returns its attestation, unsigned under eSign-off; a " +
				"report on a drop-copy order stays unsigned and returns no " +
				"attestation, because drop-copy never enforced a pre-trade verdict. " +
				"filled and partially_filled carry a fill; rejected, rolled_back, " +
				"filled and cancelled are terminal and release the order's remaining " +
				"reservation. A report on a terminal order is refused with " +
				"terminal_order, on a venue-linked order with venue_order_managed, " +
				"and a repeated id with conflict. Mutates engine state; protected " +
				"and disabled by default.",
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
		}{name: "get_credential_storage", description: "Report whether stored credentials are sealed under the master key and, when they are not, whether trading connections or market-data sources hold keys in plain text. Read-only - credentials are never exposed."},
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
		}{name: "grant_trading_access", description: "Grant an Officer account access to a trading connection, or an account group where the server offers account groups. A group grant needs the connection's fee account on providers with unlinked fees. Mutates Officer control-plane config; protected and disabled by default."},
		struct {
			name        string
			description string
		}{name: "revoke_trading_access", description: "Revoke the access of an Officer account to a trading connection, or of an account group where the server offers account groups. Mutates Officer control-plane config; protected and disabled by default."},
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
	t.Run("output-schemas-have-no-opaque-objects", func(t *testing.T) {
		for _, tool := range list.Tools {
			if paths := opaqueObjects(t, "", tool.OutputSchema); len(paths) > 0 {
				t.Errorf("%s: output schema has opaque objects at %v", tool.Name, paths)
			}
		}
	})
	t.Run("list-orders-time-as-string", func(t *testing.T) {
		tool, ok := gotByName["list_orders"]
		if !ok {
			t.Fatal("list_orders not advertised")
		}
		orders := tool.OutputSchema.Properties["orders"]
		if orders == nil || orders.Items == nil {
			t.Fatalf("%s output schema has no orders items", tool.Name)
		}
		at := orders.Items.Properties["at"]
		if at == nil {
			t.Fatalf("%s output schema has no orders[].at", tool.Name)
		}
		if at.Type != "string" || at.Format != "date-time" {
			raw, _ := json.Marshal(at)
			t.Fatalf("%s orders[].at schema = %s, want a date-time string", tool.Name, raw)
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
		"report_fill", "set_market_data_instrument",
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
			if name == "report_fill" && schemaName == "input" {
				var shape struct {
					Properties map[string]json.RawMessage `json:"properties"`
				}
				if err := json.Unmarshal(raw, &shape); err != nil {
					t.Fatalf("decode report_fill input schema: %v", err)
				}
				if _, ok := shape.Properties["force"]; ok {
					t.Fatalf("report_fill input schema offers force: %s", wire)
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

// opaqueObjects returns the paths under s of every object schema with no
// properties that admits no additional property either: what the inference
// gives a struct whose fields are all unexported, such as time.Time, and a
// schema no marshalled value of it can match.
func opaqueObjects(t *testing.T, path string, s *jsonschema.Schema) []string {
	t.Helper()
	if s == nil {
		return nil
	}
	var found []string
	isObject := s.Type == "object" || slices.Contains(s.Types, "object")
	if isObject && len(s.Properties) == 0 && isFalseSchema(t, s.AdditionalProperties) {
		found = append(found, path)
	}
	for _, name := range slices.Sorted(maps.Keys(s.Properties)) {
		found = append(found, opaqueObjects(t, path+"."+name, s.Properties[name])...)
	}
	found = append(found, opaqueObjects(t, path+"[]", s.Items)...)
	if !isFalseSchema(t, s.AdditionalProperties) {
		found = append(found, opaqueObjects(t, path+"{}", s.AdditionalProperties)...)
	}
	return found
}

// isFalseSchema reports whether s is the schema that admits nothing, as it
// arrives over the wire.
func isFalseSchema(t *testing.T, s *jsonschema.Schema) bool {
	t.Helper()
	if s == nil {
		return false
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	return string(raw) == `{"not":{}}`
}

// listToolDescription spells out a list tool description: the tool's own
// lead, filters and sort columns around the text the four list tools share.
func listToolDescription(lead, filters, sortColumns string) string {
	return lead + " Read-only - no secrets, no order-flow control. Filters: " +
		filters + "; venue is the trading-connection filter: mode any (through " +
		"at least one trading connection), none (through no connection) or " +
		"connection (through the connection whose id is in connection). Sort " +
		"columns: " + sortColumns + ". Paging: limit rows per page (default 50, " +
		"capped at 500) and a 1-based page; total counts the matching rows " +
		"before paging. An invalid value fails with a validation error."
}
