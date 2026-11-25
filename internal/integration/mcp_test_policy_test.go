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
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
	apptrading "go.openpit.dev/officer/internal/trading"
)

type policyNoQuotes struct{}

func (policyNoQuotes) QuoteSnapshots() []marketdata.QuoteSnapshot { return nil }

type policyPreCheckRuntime struct {
	scopeTradingRuntime
	connector trading.Connector
}

func (r policyPreCheckRuntime) PreCheck(ctx context.Context, order domain.Order, dest domain.TradingDestination) error {
	return r.connector.(trading.PreChecker).PreCheck(ctx, trading.Order{AmountKind: order.AmountKind, Route: dest.Route})
}

func TestMCPTestPolicyKeepsPaperVolumeLegacyError(t *testing.T) {
	_, target, realm := policyControlPlane(t)
	connector, err := apptrading.TestProvider().Build(trading.BuildContext{
		Connection: domain.TradingConnection{ExternalID: "test-policy-connection", Mode: domain.TradingModePaper, Credentials: `{}`},
		Quotes:     policyNoQuotes{}, Store: realm,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connector.Close)
	service := newOfficerService(t, target, nil, nil, backend.WithTradingRuntime(policyPreCheckRuntime{connector: connector}))
	enablePolicyTools(t, service, "send_paper_order")
	result := callTool[map[string]any](t, frameworkmcp.SourceFor(service), "send_paper_order", map[string]any{
		"account": "test-desk", "baseAsset": "AAPL", "quoteAsset": "USD", "side": "buy", "amountKind": "volume", "amountValue": "1",
		"destination": map[string]any{"connection": "test-policy-connection", "route": map[string]any{}},
	})
	if !result.IsError || !strings.HasPrefix(textContent(result.Content), "validation: ") {
		t.Fatalf("volume result=%+v", result)
	}
	if strings.Contains(string(result.Wire), "pointer") || strings.Contains(string(result.Wire), "errors") || strings.Contains(string(result.Wire), `"error"`) {
		t.Fatalf("old volume error gained public body: %s", result.Wire)
	}
	for _, value := range result.StructuredContent {
		if value != "" {
			t.Fatalf("old volume error lost zero Out: %s", result.Wire)
		}
	}
}

type policyTradingRuntime struct {
	scopeTradingRuntime
	registry *trading.Registry
}

func (r policyTradingRuntime) SupportsExecutionPolicy(provider string) (bool, error) {
	return r.registry.SupportsExecutionPolicy(provider)
}

func (r policyTradingRuntime) Providers() []trading.Provider { return r.registry.Providers() }

func policyControlPlane(t *testing.T) (*backend.Service, node.Node, store.RealmStore) {
	t.Helper()
	return policyControlPlaneAt(t, filepath.Join(t.TempDir(), "officer.db"))
}

// policyControlPlaneAt is policyControlPlane over the database file at path.
func policyControlPlaneAt(t *testing.T, path string) (*backend.Service, node.Node, store.RealmStore) {
	t.Helper()
	registry := trading.NewRegistry()
	for _, p := range []trading.Provider{
		{Type: "test", ExecutionPolicy: true},
		{Type: "venue", ExecutionPolicy: false, UnlinkedFees: true},
	} {
		p.Build = func(trading.BuildContext) (trading.Connector, error) {
			t.Fatal("fixture built a connector")
			return nil, nil
		}
		if err := registry.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	target, realm := newLocalNodeAt(t, path, engine.NewOpenPitEngineBuildFunc(), trading.Provider{
		Type: "venue", ExecutionPolicy: false,
		Build: func(trading.BuildContext) (trading.Connector, error) {
			t.Fatal("fixture built a connector")
			return nil, nil
		},
	})
	service := newOfficerService(t, target, nil, nil, backend.WithTradingRuntime(policyTradingRuntime{registry: registry}))
	for _, code := range []string{"USD", "AAPL", "MSFT"} {
		if _, err := service.CreateAsset(systemCtx(), domain.Asset{Code: code}); err != nil {
			t.Fatal(err)
		}
	}
	for _, code := range []domain.AccountID{"test-desk", "venue-desk", "other", "fees"} {
		if _, err := service.CreateAccount(systemCtx(), domain.Account{Code: code}); err != nil {
			t.Fatal(err)
		}
	}
	for _, provider := range []string{"test", "venue"} {
		id := domain.ExternalID(provider + "-policy-connection")
		_, err := target.CreateTradingConnection(systemCtx(), domain.TradingConnection{
			ExternalID: id, Provider: provider, Label: provider, Mode: domain.TradingModePaper,
			VenueIdentity: provider + "-identity", Credentials: `{"apiKey":"stored-key","apiSecret":"stored-secret"}`,
		}, auth.SystemCaller())
		if err != nil {
			t.Fatal(err)
		}
		if err := service.UpsertTradingInstrument(systemCtx(), domain.TradingInstrument{
			Connection: id, ExternalSymbol: "AAPL", BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
		if err := service.PutTradingAccess(systemCtx(), domain.TradingAccess{Connection: id, Account: domain.AccountID(provider + "-desk")}); err != nil {
			t.Fatal(err)
		}
	}
	return service, target, realm
}

func enablePolicyTools(t *testing.T, service *backend.Service, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := service.SetMcpAccess(systemCtx(), name, true); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMCPTestPolicyLimitI9(t *testing.T) {
	service, target, realm := policyControlPlane(t)
	src := frameworkmcp.SourceFor(service)
	const connection = "test-policy-connection"
	arguments := map[string]any{"connection": connection, "policy": map[string]any{"rules": []any{}}}
	before, err := service.GetTestExecutionPolicy(systemCtx(), connection)
	if err != nil {
		t.Fatal(err)
	}
	refused := callTool[map[string]any](t, src, "set_test_execution_policy", arguments)
	if !refused.IsError || !strings.Contains(textContent(refused.Content), "disabled") {
		t.Fatalf("disabled=%+v", refused)
	}
	after, err := service.GetTestExecutionPolicy(systemCtx(), connection)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("disabled wrote policy: %+v %v", after, err)
	}
	enablePolicyTools(t, service, "set_test_execution_policy")
	stored := func() any {
		t.Helper()
		status, err := service.GetTrading(systemCtx())
		if err != nil {
			t.Fatal(err)
		}
		rows := make([]domain.TradingConnection, 0, 2)
		for _, id := range []domain.ExternalID{connection, "venue-policy-connection"} {
			row, found, err := target.GetTradingConnection(systemCtx(), id)
			if err != nil || !found {
				t.Fatalf("row=%+v found=%t err=%v", row, found, err)
			}
			rows = append(rows, row)
		}
		return struct {
			Status backend.TradingStatus
			Rows   []domain.TradingConnection
		}{status, rows}
	}
	initial := stored()
	set := callTool[map[string]any](t, src, "set_test_execution_policy", arguments)
	if set.IsError || set.StructuredContent["revision"] != "2" {
		t.Fatalf("set=%+v", set)
	}
	if got := stored(); !reflect.DeepEqual(got, initial) {
		t.Fatalf("policy replacement changed trading settings: %+v", got)
	}
	for _, tool := range []string{"get_test_execution_policy", "set_test_execution_policy"} {
		in := map[string]any{"connection": "venue-policy-connection"}
		if tool == "set_test_execution_policy" {
			in["policy"] = arguments["policy"]
		}
		result := callTool[map[string]any](t, src, tool, in)
		if !result.IsError || result.StructuredContent["error"].(map[string]any)["code"] != "execution_policy_unsupported" {
			t.Fatalf("venue %s=%+v", tool, result)
		}
	}
	if got := stored(); !reflect.DeepEqual(got, initial) {
		t.Fatalf("venue refusal wrote settings: %+v", got)
	}
	for _, provider := range []string{"test", "venue"} {
		id := provider + "-policy-connection"
		for _, tc := range []struct {
			name string
			in   map[string]any
		}{
			{"create_trading_connection", map[string]any{"provider": provider, "label": "New", "mode": "paper", "credentials": map[string]any{}, "enabled": false}},
			{"update_trading_connection", map[string]any{"connection": id, "credentials": map[string]any{"apiKey": "changed-key"}}},
			{"set_trading_connection_enabled", map[string]any{"connection": id, "enabled": true}},
			{"set_trading_instrument", map[string]any{"connection": id, "externalSymbol": "MSFT", "baseAsset": "MSFT", "quoteAsset": "USD", "enabled": true}},
			{"grant_trading_access", map[string]any{"connection": id, "account": "other"}},
			{"set_trading_fee_account", map[string]any{"connection": id, "account": "fees"}},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				enablePolicyTools(t, service, tc.name)
				result := callTool[map[string]any](t, src, tc.name, tc.in)
				got := stored()
				if !reflect.DeepEqual(got, initial) {
					t.Fatalf("refused mutation changed real settings/rights: %+v", got)
				}
				if provider == "test" && tc.name == "set_trading_fee_account" {
					if !result.IsError || !strings.HasPrefix(textContent(result.Content), "validation: trading provider has no unlinked fees") {
						t.Fatalf("test fee refusal=%+v", result)
					}
				} else {
					requireLooseningRefused(t, tc.name, result)
				}
			})
		}
	}
	policy, err := realm.GetTestExecutionPolicy(context.Background(), connection)
	if err != nil || policy.Revision != 2 {
		t.Fatalf("stored policy=%+v err=%v", policy, err)
	}
	enablePolicyTools(t, service, "update_trading_connection")
	label := callTool[map[string]any](t, src, "update_trading_connection", map[string]any{"connection": connection, "label": "Renamed"})
	if label.IsError {
		t.Fatalf("label-only update=%+v", label)
	}
	row, _, err := target.GetTradingConnection(systemCtx(), connection)
	if err != nil || row.Label != "Renamed" || row.Credentials != `{"apiKey":"stored-key","apiSecret":"stored-secret"}` {
		t.Fatalf("label row=%+v err=%v", row, err)
	}
	if json.Valid(label.Wire) == false {
		t.Fatal("label response is not JSON")
	}
}
