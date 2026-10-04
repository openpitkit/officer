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
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/trading"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

type tradingSource struct {
	paperSendResult backend.VenueSendResult
	fakeSource
	status       backend.TradingStatus
	info         trading.AccountInfo
	verification backend.TradingSymbolVerification
	search       backend.TradingSymbolSearch
	destinations []backend.TradingDestinationStatus
	calls        []string
	arguments    []any
	err          error
	snapshotErr  error
}

func (s *tradingSource) record(operation string, args ...any) error {
	s.calls = append(s.calls, operation)
	s.arguments = args
	return s.err
}

func (s *tradingSource) SendPaperOrder(_ context.Context, o domain.Order, dest backend.SendDestination) (backend.VenueSendResult, error) {
	return s.paperSendResult, s.record("send-paper", o, dest)
}
func (s *tradingSource) GetTrading(context.Context) (backend.TradingStatus, error) {
	s.calls = append(s.calls, "get")
	return s.status, s.snapshotErr
}
func (s *tradingSource) CreateTradingConnection(_ context.Context, c domain.TradingConnection) (domain.TradingConnection, error) {
	err := s.record("create", c)
	c.ExternalID = domain.ExternalID("conn")
	return c, err
}
func (s *tradingSource) UpdateTradingConnectionSettings(_ context.Context, id, label, credentials string) error {
	return s.record("update", id, label, credentials)
}
func (s *tradingSource) SetTradingConnectionEnabled(_ context.Context, id string, enabled bool) error {
	return s.record("enable", id, enabled)
}
func (s *tradingSource) CheckTradingConnection(_ context.Context, id string) (trading.AccountInfo, error) {
	return s.info, s.record("check", id)
}
func (s *tradingSource) UpsertTradingInstrument(_ context.Context, i domain.TradingInstrument) error {
	return s.record("instrument", i)
}
func (s *tradingSource) DeleteTradingInstrument(_ context.Context, id, symbol string) error {
	return s.record("delete", id, symbol)
}
func (s *tradingSource) VerifyTradingSymbol(_ context.Context, id, symbol string) (backend.TradingSymbolVerification, error) {
	return s.verification, s.record("verify", id, symbol)
}
func (s *tradingSource) SearchTradingSymbols(_ context.Context, id, query string) (backend.TradingSymbolSearch, error) {
	return s.search, s.record("search", id, query)
}
func (s *tradingSource) PutTradingAccess(_ context.Context, access domain.TradingAccess) error {
	return s.record("grant", access)
}
func (s *tradingSource) RemoveTradingAccess(_ context.Context, account domain.AccountID, connection domain.ExternalID) error {
	return s.record("revoke", account, connection)
}
func (s *tradingSource) SetTradingFeeAccount(_ context.Context, connection domain.ExternalID, account domain.AccountID) error {
	return s.record("fee", connection, account)
}
func (s *tradingSource) ListTradingDestinations(_ context.Context, account domain.AccountID, mode domain.TradingMode) ([]backend.TradingDestinationStatus, error) {
	return s.destinations, s.record("destinations", account, mode)
}

func tradingFixture() *tradingSource {
	return &tradingSource{
		status: backend.TradingStatus{
			Providers: []backend.TradingProvider{{Type: "alpaca", Title: "Alpaca", VerifiesSymbols: true, SearchesSymbols: true, UnlinkedFees: true}},
			Connections: []backend.TradingConnectionStatus{{
				ExternalID: domain.ExternalID("conn"), Provider: "alpaca", Label: "Paper",
				Mode: domain.TradingModePaper, Enabled: true, VenueIdentity: "venue",
				State: "running", Secrets: map[string]bool{"key": true},
			}},
		},
		info:         trading.AccountInfo{Identity: "venue", Status: "ACTIVE"},
		verification: backend.TradingSymbolVerification{Supported: true, Exists: true, Tradable: true},
		search:       backend.TradingSymbolSearch{Supported: true, Matches: []trading.SymbolMatch{{Symbol: "AAPL", Tradable: true}}},
		destinations: []backend.TradingDestinationStatus{{
			Connection: domain.ExternalID("conn"), Label: "Paper", Provider: "alpaca",
			Mode: domain.TradingModePaper, Enabled: true, State: "running",
		}},
	}
}

func assertTradingWire(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var actual, expected any
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("JSON shape mismatch: got %s; want %s", got, want)
	}
}

func TestTradingToolsSuccess(t *testing.T) {
	const connection = `{"id":"conn","provider":"alpaca","label":"Paper","mode":"paper","enabled":true,"venueIdentity":"venue","feeAccount":"","state":"running","secrets":{"key":true},"instruments":[],"access":[]}`
	cases := []struct {
		name  string
		input map[string]any
		want  string
		calls []string
		args  []any
	}{
		{"get_trading", map[string]any{}, `{"providers":[{"type":"alpaca","title":"Alpaca","venueAccounts":false,"verifiesSymbols":true,"searchesSymbols":true,"unlinkedFees":true}],"connections":[` + connection + `],"credentialsSealed":false}`, []string{"get"}, nil},
		{"create_trading_connection", map[string]any{"provider": "alpaca", "label": "Paper", "mode": "paper", "credentials": map[string]any{"z": "secret-value", "a": "first-value"}, "enabled": true}, `{"connection":` + connection + `}`, []string{"create", "get"}, []any{domain.TradingConnection{Provider: "alpaca", Label: "Paper", Mode: domain.TradingModePaper, Credentials: `{"a":"first-value","z":"secret-value"}`, Enabled: true}}},
		{"update_trading_connection", map[string]any{"connection": "conn", "label": "Paper", "credentials": map[string]any{"z": "secret-value", "a": "first-value"}}, `{"connection":` + connection + `}`, []string{"update", "get"}, []any{"conn", "Paper", `{"a":"first-value","z":"secret-value"}`}},
		{"set_trading_connection_enabled", map[string]any{"connection": "conn", "enabled": false}, `{"connection":"conn","enabled":false}`, []string{"enable"}, []any{"conn", false}},
		{"check_trading_connection", map[string]any{"connection": "conn"}, `{"venueIdentity":"venue","venueStatus":"ACTIVE"}`, []string{"check"}, []any{"conn"}},
		{"set_trading_instrument", map[string]any{"connection": "conn", "externalSymbol": "AAPL", "baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true}, `{"connection":"conn","externalSymbol":"AAPL","baseAsset":"AAPL","quoteAsset":"USD","enabled":true}`, []string{"instrument"}, []any{domain.TradingInstrument{Connection: domain.ExternalID("conn"), ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true}}},
		{"delete_trading_instrument", map[string]any{"connection": "conn", "externalSymbol": "AAPL"}, `{"connection":"conn","externalSymbol":"AAPL"}`, []string{"delete"}, []any{"conn", "AAPL"}},
		{"verify_trading_symbol", map[string]any{"connection": "conn", "externalSymbol": "AAPL"}, `{"supported":true,"exists":true,"tradable":true}`, []string{"verify"}, []any{"conn", "AAPL"}},
		{"search_trading_symbols", map[string]any{"connection": "conn", "query": "AAPL"}, `{"supported":true,"matches":[{"symbol":"AAPL","tradable":true}]}`, []string{"search"}, []any{"conn", "AAPL"}},
		{"grant_trading_access", map[string]any{"connection": "conn", "account": "acc", "venueAccount": "remote"}, `{"connection":"conn","account":"acc","venueAccount":"remote"}`, []string{"grant"}, []any{domain.TradingAccess{Connection: domain.ExternalID("conn"), Account: domain.AccountID("acc"), VenueAccount: "remote"}}},
		{"revoke_trading_access", map[string]any{"connection": "conn", "account": "acc"}, `{"connection":"conn","account":"acc"}`, []string{"revoke"}, []any{domain.AccountID("acc"), domain.ExternalID("conn")}},
		{"set_trading_fee_account", map[string]any{"connection": "conn", "account": "acc"}, `{"connection":"conn","account":"acc"}`, []string{"fee"}, []any{domain.ExternalID("conn"), domain.AccountID("acc")}},
		{"clear_trading_fee_account", map[string]any{"connection": "conn"}, `{"connection":"conn"}`, []string{"fee"}, []any{domain.ExternalID("conn"), domain.AccountID("")}},
		{"list_trading_destinations", map[string]any{"account": "acc", "mode": "paper"}, `{"destinations":[{"connection":"conn","label":"Paper","provider":"alpaca","mode":"paper","enabled":true,"venueAccount":"","state":"running"}]}`, []string{"destinations"}, []any{domain.AccountID("acc"), domain.TradingModePaper}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tradingFixture()
			res := callTool[map[string]any](t, src, tc.name, tc.input)
			requireNotToolError(t, res.IsError)
			assertTradingWire(t, res.Wire, tc.want)
			if !reflect.DeepEqual(src.calls, tc.calls) || !reflect.DeepEqual(src.arguments, tc.args) {
				t.Fatalf("delegation mismatch: calls %v args %#v", src.calls, src.arguments)
			}
		})
	}
}

func TestTradingOptionalAndEmptyShapes(t *testing.T) {
	t.Run("get-empty", func(t *testing.T) {
		res := callTool[map[string]any](t, &tradingSource{}, "get_trading", map[string]any{})
		requireNotToolError(t, res.IsError)
		assertTradingWire(t, res.Wire, `{"providers":[],"connections":[],"credentialsSealed":false}`)
	})
	t.Run("populated-connection", func(t *testing.T) {
		src := tradingFixture()
		c := &src.status.Connections[0]
		c.State = "unavailable"
		c.Unavailable = "offline"
		c.FeeAccount = domain.AccountID("fees")
		c.Instruments = []domain.TradingInstrument{{ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true}}
		c.Access = []domain.TradingAccess{{Account: domain.AccountID("acc"), VenueAccount: "remote"}}
		src.status.CredentialsSealed = true
		res := callTool[map[string]any](t, src, "get_trading", map[string]any{})
		requireNotToolError(t, res.IsError)
		assertTradingWire(t, res.Wire, `{"providers":[{"type":"alpaca","title":"Alpaca","venueAccounts":false,"verifiesSymbols":true,"searchesSymbols":true,"unlinkedFees":true}],"connections":[{"id":"conn","provider":"alpaca","label":"Paper","mode":"paper","enabled":true,"venueIdentity":"venue","feeAccount":"fees","state":"unavailable","error":"offline","secrets":{"key":true},"instruments":[{"externalSymbol":"AAPL","baseAsset":"AAPL","quoteAsset":"USD","enabled":true}],"access":[{"account":"acc","venueAccount":"remote"}]}],"credentialsSealed":true}`)
	})
	t.Run("verification-details", func(t *testing.T) {
		src := tradingFixture()
		src.verification.Details = "equity"
		res := callTool[map[string]any](t, src, "verify_trading_symbol", map[string]any{"connection": "conn", "externalSymbol": "AAPL"})
		requireNotToolError(t, res.IsError)
		assertTradingWire(t, res.Wire, `{"supported":true,"exists":true,"tradable":true,"details":"equity"}`)
	})
	t.Run("search-details", func(t *testing.T) {
		src := tradingFixture()
		src.search.Matches[0] = trading.SymbolMatch{Symbol: "AAPL", Name: "Apple", Class: "equity", Exchange: "NASDAQ", Tradable: true}
		res := callTool[map[string]any](t, src, "search_trading_symbols", map[string]any{"connection": "conn", "query": "AAPL"})
		requireNotToolError(t, res.IsError)
		assertTradingWire(t, res.Wire, `{"supported":true,"matches":[{"symbol":"AAPL","name":"Apple","class":"equity","exchange":"NASDAQ","tradable":true}]}`)
	})
	t.Run("search-empty", func(t *testing.T) {
		src := tradingFixture()
		src.search = backend.TradingSymbolSearch{}
		res := callTool[map[string]any](t, src, "search_trading_symbols", map[string]any{"connection": "conn", "query": "none"})
		requireNotToolError(t, res.IsError)
		assertTradingWire(t, res.Wire, `{"supported":false,"matches":[]}`)
	})
	t.Run("destinations-empty-no-mode", func(t *testing.T) {
		src := tradingFixture()
		src.destinations = nil
		res := callTool[map[string]any](t, src, "list_trading_destinations", map[string]any{"account": "acc"})
		requireNotToolError(t, res.IsError)
		assertTradingWire(t, res.Wire, `{"destinations":[]}`)
		if !reflect.DeepEqual(src.arguments, []any{domain.AccountID("acc"), domain.TradingMode("")}) {
			t.Fatal("mode was defaulted")
		}
	})
	t.Run("destination-error", func(t *testing.T) {
		src := tradingFixture()
		src.destinations[0].State = "unavailable"
		src.destinations[0].Unavailable = "offline"
		res := callTool[map[string]any](t, src, "list_trading_destinations", map[string]any{"account": "acc", "mode": "live"})
		requireNotToolError(t, res.IsError)
		assertTradingWire(t, res.Wire, `{"destinations":[{"connection":"conn","label":"Paper","provider":"alpaca","mode":"paper","enabled":true,"venueAccount":"","state":"unavailable","error":"offline"}]}`)
		if src.arguments[1] != domain.TradingModeLive {
			t.Fatal("live mode not delegated")
		}
	})
	t.Run("update-without-settings", func(t *testing.T) {
		src := tradingFixture()
		res := callTool[map[string]any](t, src, "update_trading_connection", map[string]any{"connection": "conn"})
		requireNotToolError(t, res.IsError)
		if !reflect.DeepEqual(src.arguments, []any{"conn", "", ""}) {
			t.Fatalf("omitted settings: %#v", src.arguments)
		}
	})
}

func TestTradingRequiredFields(t *testing.T) {
	cases := []struct {
		name   string
		input  map[string]any
		fields []string
	}{
		{"create_trading_connection", map[string]any{"provider": "alpaca", "label": "Paper", "mode": "paper", "credentials": map[string]any{}, "enabled": false}, []string{"provider", "label", "mode"}},
		{"update_trading_connection", map[string]any{"connection": "conn", "label": "Paper"}, []string{"connection", "label"}},
		{"set_trading_connection_enabled", map[string]any{"connection": "conn", "enabled": false}, []string{"connection"}},
		{"check_trading_connection", map[string]any{"connection": "conn"}, []string{"connection"}},
		{"set_trading_instrument", map[string]any{"connection": "conn", "externalSymbol": "AAPL", "baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true}, []string{"connection", "externalSymbol", "baseAsset", "quoteAsset"}},
		{"delete_trading_instrument", map[string]any{"connection": "conn", "externalSymbol": "AAPL"}, []string{"connection", "externalSymbol"}},
		{"verify_trading_symbol", map[string]any{"connection": "conn", "externalSymbol": "AAPL"}, []string{"connection", "externalSymbol"}},
		{"search_trading_symbols", map[string]any{"connection": "conn", "query": "AAPL"}, []string{"connection", "query"}},
		{"grant_trading_access", map[string]any{"connection": "conn", "account": "acc"}, []string{"connection", "account"}},
		{"revoke_trading_access", map[string]any{"connection": "conn", "account": "acc"}, []string{"connection", "account"}},
		{"set_trading_fee_account", map[string]any{"connection": "conn", "account": "acc"}, []string{"connection", "account"}},
		{"clear_trading_fee_account", map[string]any{"connection": "conn"}, []string{"connection"}},
		{"list_trading_destinations", map[string]any{"account": "acc"}, []string{"account"}},
	}
	for _, tc := range cases {
		for _, field := range tc.fields {
			t.Run(tc.name+"/"+field, func(t *testing.T) {
				src := tradingFixture()
				input := make(map[string]any, len(tc.input))
				for key, value := range tc.input {
					input[key] = value
				}
				input[field] = " \t "
				res := callTool[map[string]any](t, src, tc.name, input)
				requireToolError(t, res.IsError)
				if got := textContent(res.Content); got != field+" is required" {
					t.Fatalf("refusal: %q", got)
				}
				if len(src.calls) != 0 {
					t.Fatalf("Source called: %v", src.calls)
				}
			})
		}
	}
}

func TestTradingCredentialsStayWriteOnly(t *testing.T) {
	const secret = "known-credential-value"
	for _, name := range []string{"create_trading_connection", "update_trading_connection"} {
		for _, failure := range []string{"success", "service", "snapshot"} {
			t.Run(name+"/"+failure, func(t *testing.T) {
				src := tradingFixture()
				input := map[string]any{"connection": "conn", "credentials": map[string]any{"key": secret, "nested": map[string]any{"keys": []any{secret + "-long", secret}}}}
				if name == "create_trading_connection" {
					input = map[string]any{"provider": "alpaca", "label": "Paper", "mode": "paper", "credentials": input["credentials"], "enabled": false}
				}
				if failure == "service" {
					src.err = fmt.Errorf("venue rejected credential %s: %w", secret, domain.ErrLiveTradingUnavailable)
				}
				if failure == "snapshot" {
					src.snapshotErr = fmt.Errorf("snapshot error %s", secret)
				}
				src.status.Connections[0].State = "unavailable"
				src.status.Connections[0].Unavailable = "venue rejected " + secret + "-long"
				res := callTool[map[string]any](t, src, name, input)
				wire, err := json.Marshal(res)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(wire), secret) {
					t.Fatal("credential leaked in tool result")
				}
				if failure == "success" {
					requireNotToolError(t, res.IsError)
				} else {
					requireToolError(t, res.IsError)
					if failure == "service" && !strings.HasPrefix(textContent(res.Content), "live_trading_unavailable: ") {
						t.Fatal("typed prefix lost during redaction")
					}
				}
			})
		}
	}
}

func TestTradingCredentialEscaping(t *testing.T) {
	const secret = "quoted\"credential\nvalue"
	for _, name := range []string{"create_trading_connection", "update_trading_connection"} {
		t.Run(name, func(t *testing.T) {
			src := tradingFixture()
			credentials := map[string]any{"key": secret}
			raw, err := json.Marshal(credentials)
			if err != nil {
				t.Fatal(err)
			}
			src.err = fmt.Errorf("invalid credentials: %s", raw)
			input := map[string]any{"connection": "conn", "credentials": credentials}
			if name == "create_trading_connection" {
				input = map[string]any{"provider": "alpaca", "label": "Paper", "mode": "paper", "credentials": credentials, "enabled": false}
			}
			res := callTool[map[string]any](t, src, name, input)
			requireToolError(t, res.IsError)
			text := textContent(res.Content)
			encoded, err := json.Marshal(secret)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(text, secret) || strings.Contains(text, string(encoded[1:len(encoded)-1])) || !strings.Contains(text, "[REDACTED]") {
				t.Fatal("credential escaped into the error text")
			}
		})
	}
}

func TestUpdateTradingConnectionPaddedID(t *testing.T) {
	src := tradingFixture()
	res := callTool[map[string]any](t, src, "update_trading_connection", map[string]any{"connection": " conn ", "label": "Updated"})
	requireNotToolError(t, res.IsError)
	if !reflect.DeepEqual(src.arguments, []any{"conn", "Updated", ""}) {
		t.Fatalf("padded connection id reached Source: %#v", src.arguments)
	}
	connection, ok := res.StructuredContent["connection"].(map[string]any)
	if !ok || connection["id"] != "conn" {
		t.Fatalf("updated connection missing from response: %s", res.Wire)
	}
}

func TestUpdateTradingConnectionUnknownFields(t *testing.T) {
	ctx, cancel := context.WithTimeout(systemCtx(), 5*time.Second)
	defer cancel()
	src := tradingFixture()
	server, err := frameworkmcp.Build(officerToolRegistry(), src, nil, httpx.AllowAll{}, operatorCaller())
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Run(ctx, serverTransport) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = session.Close()
		cancel()
		if err := <-serverErr; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}()
	for _, field := range []string{"credential", "enabled"} {
		t.Run(field, func(t *testing.T) {
			_, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
				Name:      "update_trading_connection",
				Arguments: map[string]any{"connection": "conn", field: true},
			})
			if err == nil || !strings.Contains(err.Error(), `unknown field "`+field+`"`) {
				t.Fatalf("unknown field %s was not refused: %v", field, err)
			}
			if len(src.calls) != 0 {
				t.Fatalf("unknown field reached Source: %v", src.calls)
			}
		})
	}
}

func TestTradingInvalidInputs(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		field string
	}{
		{"create_trading_connection", map[string]any{"provider": "alpaca", "label": "Paper", "mode": "", "credentials": map[string]any{}, "enabled": false}, "mode"},
		{"create_trading_connection", map[string]any{"provider": "alpaca", "label": "Paper", "mode": "demo", "credentials": map[string]any{}, "enabled": false}, "mode"},
		{"create_trading_connection", map[string]any{"provider": "alpaca", "label": "Paper", "mode": "paper", "enabled": false}, "credentials"},
		{"create_trading_connection", map[string]any{"provider": "alpaca", "label": "Paper", "mode": "paper", "credentials": nil, "enabled": false}, "credentials"},
		{"create_trading_connection", map[string]any{"provider": "alpaca", "label": "Paper", "mode": "paper", "credentials": map[string]any{}}, "enabled"},
		{"set_trading_connection_enabled", map[string]any{"connection": "conn"}, "enabled"},
		{"set_trading_instrument", map[string]any{"connection": "conn", "externalSymbol": "AAPL", "baseAsset": "AAPL", "quoteAsset": "USD"}, "enabled"},
		{"list_trading_destinations", map[string]any{"account": "acc", "mode": "demo"}, "mode"},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			src := tradingFixture()
			res := callTool[map[string]any](t, src, tc.name, tc.input)
			requireToolError(t, res.IsError)
			if !strings.Contains(textContent(res.Content), tc.field) {
				t.Fatalf("refusal does not name %s", tc.field)
			}
			if len(src.calls) != 0 {
				t.Fatalf("invalid input reached Source: %v", src.calls)
			}
		})
	}
	for _, name := range []string{"create_trading_connection", "update_trading_connection"} {
		t.Run(name+"/missing-snapshot", func(t *testing.T) {
			src := tradingFixture()
			src.status.Connections = nil
			input := map[string]any{"connection": "conn"}
			if name == "create_trading_connection" {
				input = map[string]any{"provider": "alpaca", "label": "Paper", "mode": "paper", "credentials": map[string]any{}, "enabled": false}
			}
			res := callTool[map[string]any](t, src, name, input)
			requireToolError(t, res.IsError)
			if !strings.Contains(textContent(res.Content), "missing from the snapshot") {
				t.Fatal("missing snapshot was not refused")
			}
		})
	}
}

func TestGetOrderVenueShape(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprint(linked), func(t *testing.T) {
			src := &fakeSource{}
			src.orderDetail.Order.ExternalID = domain.ExternalID("order")
			if linked {
				src.orderDetail.Venue = &domain.VenueOrder{Connection: domain.ExternalID("conn"), VenueAccount: "remote", Route: `{"timeInForce":"day"}`, ClientOrderID: "client", CreatedAt: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)}
			}
			res := callTool[map[string]any](t, src, "get_order", map[string]any{"id": "order"})
			requireNotToolError(t, res.IsError)
			venue, exists := res.StructuredContent["venueOrder"]
			if exists != linked {
				t.Fatalf("venueOrder presence: %v", exists)
			}
			if linked {
				raw, err := json.Marshal(venue)
				if err != nil {
					t.Fatal(err)
				}
				assertTradingWire(t, raw, `{"connection":"conn","venueAccount":"remote","route":{"timeInForce":"day"},"clientOrderId":"client","sendAttempted":false,"createdAt":"2026-10-05T00:00:00Z"}`)
				src.orderDetail.Venue.VenueOrderID = "venue-order"
				src.orderDetail.Venue.SendAttempted = true
				res = callTool[map[string]any](t, src, "get_order", map[string]any{"id": "order"})
				requireNotToolError(t, res.IsError)
				raw, err = json.Marshal(res.StructuredContent["venueOrder"])
				if err != nil {
					t.Fatal(err)
				}
				assertTradingWire(t, raw, `{"connection":"conn","venueAccount":"remote","route":{"timeInForce":"day"},"clientOrderId":"client","venueOrderId":"venue-order","sendAttempted":true,"createdAt":"2026-10-05T00:00:00Z"}`)
			}
		})
	}
	t.Run("invalid-route", func(t *testing.T) {
		src := &fakeSource{orderDetail: domain.OrderDetail{Venue: &domain.VenueOrder{Route: "[]"}}}
		res := callTool[map[string]any](t, src, "get_order", map[string]any{"id": "order"})
		requireToolError(t, res.IsError)
		if textContent(res.Content) != "stored venue order route is invalid" {
			t.Fatal("invalid route not refused")
		}
	})
}

func TestSendPaperOrderWire(t *testing.T) {
	for _, status := range []string{"acknowledged", "refused", "unknown", "reject"} {
		t.Run(status, func(t *testing.T) {
			src := tradingFixture()
			src.paperSendResult.Token = backend.ApprovalToken{Token: "approval", KeyID: "key", OrderExternalID: "order", Verdict: "accept"}
			want := `{"signedApproval":"approval","keyId":"key","id":"order","verdict":"accept"`
			if status == "reject" {
				src.paperSendResult.Token.Verdict = "reject"
				want = strings.Replace(want, `"accept"`, `"reject"`, 1) + `}`
			} else {
				src.paperSendResult.Send = &backend.VenueSend{Status: status}
				want += `,"venueSend":{"status":"` + status + `"`
				if status == "acknowledged" {
					src.paperSendResult.Send.Order = &domain.VenueOrder{Connection: "conn", Route: `{"timeInForce":"day"}`,
						ClientOrderID: "client", VenueOrderID: "venue", SendAttempted: true, CreatedAt: time.Unix(0, 0).UTC()}
					want += `,"venueOrder":{"connection":"conn","venueAccount":"","route":{"timeInForce":"day"},"clientOrderId":"client","venueOrderId":"venue","sendAttempted":true,"createdAt":"1970-01-01T00:00:00Z"}`
				} else {
					src.paperSendResult.Send.Error = "venue reason"
					want += `,"error":"venue reason"`
				}
				want += `}}`
			}
			input := map[string]any{"id": "order", "account": "desk", "baseAsset": "AAPL", "quoteAsset": "USD",
				"side": "buy", "amountKind": "quantity", "amountValue": "10", "price": "2",
				"destination": map[string]any{"connection": "conn", "venueAccount": "remote", "route": map[string]any{"timeInForce": "day"}}}
			result := callTool[map[string]any](t, src, "send_paper_order", input)
			if result.IsError {
				t.Fatal("send result became a tool error")
			}
			assertTradingWire(t, result.Wire, want)
			wantText := "order order: reject"
			if status != "reject" {
				wantText = "order order: accept; venue send " + status
				if status != "acknowledged" {
					wantText += ": venue reason"
				}
			}
			if len(result.Content) != 1 || textContent(result.Content) != wantText {
				t.Fatalf("send text = %q, want %q", textContent(result.Content), wantText)
			}
			if !reflect.DeepEqual(src.arguments, []any{domain.Order{ExternalID: "order", Account: "desk", BaseAsset: "AAPL", QuoteAsset: "USD",
				Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "10", Price: "2"},
				backend.SendDestination{Connection: "conn", VenueAccount: "remote", Route: `{"timeInForce":"day"}`}}) {
				t.Fatalf("paper send arguments = %+v", src.arguments)
			}
		})
	}
	for _, tc := range []struct {
		err  error
		code string
	}{
		{domain.ErrTradingModeMismatch, "trading_mode_mismatch"}, {domain.ErrNoDestination, "no_destination"},
		{&backend.DestinationAmbiguousError{Candidates: []backend.TradingDestinationStatus{{Connection: "conn", Label: "Paper"}}}, "destination_ambiguous"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			src := tradingFixture()
			src.err = tc.err
			res := callTool[map[string]any](t, src, "send_paper_order", map[string]any{"account": "desk", "baseAsset": "AAPL", "quoteAsset": "USD",
				"side": "buy", "amountKind": "quantity", "amountValue": "1", "destination": map[string]any{"route": map[string]any{}}})
			requireToolError(t, res.IsError)
			if !strings.HasPrefix(textContent(res.Content), tc.code+": ") {
				t.Fatal("typed refusal prefix omitted")
			}
		})
	}
}

func TestTradingTypedRefusals(t *testing.T) {
	for _, tc := range []struct {
		err    error
		prefix string
	}{
		{domain.ErrLiveTradingUnavailable, "live_trading_unavailable: "},
		{domain.ErrFeeAccountRequired, "fee_account_required: "},
		{domain.ErrVenueOrderManaged, "venue_order_managed: "},
	} {
		t.Run(tc.prefix, func(t *testing.T) {
			src := tradingFixture()
			src.err = fmt.Errorf("venue refused: %w", tc.err)
			res := callTool[map[string]any](t, src, "check_trading_connection", map[string]any{"connection": "conn"})
			requireToolError(t, res.IsError)
			if textContent(res.Content) != tc.prefix+src.err.Error() {
				t.Fatalf("typed refusal: %q", textContent(res.Content))
			}
		})
	}
	for _, name := range []string{"confirm_execution", "cancel"} {
		t.Run(name, func(t *testing.T) {
			err := fmt.Errorf("linked: %w", domain.ErrVenueOrderManaged)
			src := &approvalFakeSource{confirmErr: err, cancelErr: err}
			res := callTool[map[string]any](t, src, name, map[string]any{"id": "order"})
			requireToolError(t, res.IsError)
			if textContent(res.Content) != "venue_order_managed: "+err.Error() {
				t.Fatalf("venue refusal: %q", textContent(res.Content))
			}
		})
	}
}

func TestTradingSourceFailures(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
	}{
		{"get_trading", map[string]any{}},
		{"create_trading_connection", map[string]any{"provider": "alpaca", "label": "Paper", "mode": "paper", "credentials": map[string]any{}, "enabled": false}},
		{"update_trading_connection", map[string]any{"connection": "conn"}},
		{"set_trading_connection_enabled", map[string]any{"connection": "conn", "enabled": true}},
		{"check_trading_connection", map[string]any{"connection": "conn"}},
		{"set_trading_instrument", map[string]any{"connection": "conn", "externalSymbol": "AAPL", "baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true}},
		{"delete_trading_instrument", map[string]any{"connection": "conn", "externalSymbol": "AAPL"}},
		{"verify_trading_symbol", map[string]any{"connection": "conn", "externalSymbol": "AAPL"}},
		{"search_trading_symbols", map[string]any{"connection": "conn", "query": "AAPL"}},
		{"grant_trading_access", map[string]any{"connection": "conn", "account": "acc"}},
		{"revoke_trading_access", map[string]any{"connection": "conn", "account": "acc"}},
		{"set_trading_fee_account", map[string]any{"connection": "conn", "account": "acc"}},
		{"clear_trading_fee_account", map[string]any{"connection": "conn"}},
		{"list_trading_destinations", map[string]any{"account": "acc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tradingFixture()
			src.err = errors.New("offline")
			src.snapshotErr = src.err
			res := callTool[map[string]any](t, src, tc.name, tc.input)
			requireToolError(t, res.IsError)
			expected := strings.ReplaceAll(tc.name, "_", " ") + " failed: offline"
			if textContent(res.Content) != expected {
				t.Fatalf("failure: %q", textContent(res.Content))
			}
		})
	}
}

func TestTradingGates(t *testing.T) {
	for _, name := range []string{"create_trading_connection", "update_trading_connection", "set_trading_connection_enabled", "set_trading_instrument", "delete_trading_instrument", "grant_trading_access", "revoke_trading_access", "set_trading_fee_account", "clear_trading_fee_account"} {
		t.Run(name, func(t *testing.T) {
			src := tradingFixture()
			src.disabledCommands = map[string]bool{name: true}
			var inputs map[string]any
			switch name {
			case "create_trading_connection":
				inputs = map[string]any{"provider": "alpaca", "label": "Paper", "mode": "paper", "credentials": map[string]any{}, "enabled": false}
			case "update_trading_connection", "clear_trading_fee_account":
				inputs = map[string]any{"connection": "conn"}
			case "set_trading_connection_enabled":
				inputs = map[string]any{"connection": "conn", "enabled": false}
			case "set_trading_instrument":
				inputs = map[string]any{"connection": "conn", "externalSymbol": "AAPL", "baseAsset": "AAPL", "quoteAsset": "USD", "enabled": false}
			case "delete_trading_instrument":
				inputs = map[string]any{"connection": "conn", "externalSymbol": "AAPL"}
			default:
				inputs = map[string]any{"connection": "conn", "account": "acc"}
			}
			res := callTool[map[string]any](t, src, name, inputs)
			if !strings.Contains(textContent(res.Content), "disabled") {
				t.Fatalf("gate: %q", textContent(res.Content))
			}
			if len(src.calls) != 0 {
				t.Fatal("disabled tool reached Source")
			}
		})
	}
}
