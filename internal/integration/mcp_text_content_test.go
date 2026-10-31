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
	"maps"
	"slices"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
)

// textContentAccount is the account the successful calls below name where
// their fixture expects no account of its own.
const textContentAccount = "acc-text"

// textContentCall is one successful call of a catalogue command: the fixture
// answering it and the input.
type textContentCall struct {
	source func(t *testing.T) frameworkmcp.Source
	input  map[string]any
}

// The fixtures answering the successful calls: those the command families'
// own success tests drive.
var (
	textContentFake = func(*testing.T) frameworkmcp.Source {
		return &fakeSource{account: domain.Account{Code: textContentAccount}}
	}
	textContentLists       = func(*testing.T) frameworkmcp.Source { return listRowsFixture() }
	textContentAccounts    = func(*testing.T) frameworkmcp.Source { return accountsFixture() }
	textContentAssets      = func(*testing.T) frameworkmcp.Source { return assetsFixture() }
	textContentGroups      = func(*testing.T) frameworkmcp.Source { return groupsFixture() }
	textContentAdjustments = func(*testing.T) frameworkmcp.Source { return adjustmentsFixture() }
	textContentTrading     = func(*testing.T) frameworkmcp.Source { return tradingFixture() }
	textContentReports     = func(*testing.T) frameworkmcp.Source {
		src, _, _ := reportsAttestedBy(frameworkmcp.Attestation{})()
		return src
	}
	textContentControlPlane = func(t *testing.T) frameworkmcp.Source {
		_, src := controlPlaneTools(t)
		return src
	}
)

// textContentOrder is an order input the order commands accept.
var textContentOrder = map[string]any{
	"account": textContentAccount, "baseAsset": "BTC", "quoteAsset": "USD",
	"side": "buy", "amountKind": "quantity", "amountValue": "1", "price": "1",
}

// withOrder returns textContentOrder with the members of extra added.
func withOrder(extra map[string]any) map[string]any {
	in := maps.Clone(textContentOrder)
	maps.Copy(in, extra)
	return in
}

// textContentCalls holds a successful call of every command of the Officer MCP
// catalogue.
var textContentCalls = map[string]textContentCall{
	"health":            {textContentFake, map[string]any{}},
	"get_account_state": {textContentFake, map[string]any{"account": textContentAccount}},
	"get_limits":        {textContentFake, map[string]any{}},
	"get_order":         {textContentFake, map[string]any{"id": testOrderEID}},
	"get_audit":         {textContentFake, map[string]any{}},
	"list_orders":       {textContentLists, map[string]any{}},
	"list_trades":       {textContentLists, map[string]any{}},
	"list_accounts":     {textContentLists, map[string]any{}},
	"list_groups":       {textContentLists, map[string]any{}},
	"check_order":       {textContentFake, textContentOrder},
	"set_limit": {textContentControlPlane, map[string]any{
		"policy": "order_size_limit", "scope": "broker", "maxQuantity": "1",
	}},
	"create_account":       {textContentAccounts, accountToolInputs["create_account"]},
	"update_account":       {textContentAccounts, accountToolInputs["update_account"]},
	"delete_account":       {textContentAccounts, accountToolInputs["delete_account"]},
	"set_account_currency": {textContentAccounts, accountCurrencyInputs["set_account_currency"]},
	"apply_adjustment":     {textContentAdjustments, adjustmentInputs["apply_adjustment"]},
	"create_asset":         {textContentAssets, assetToolInputs["create_asset"]},
	"update_asset":         {textContentAssets, assetToolInputs["update_asset"]},
	"delete_asset":         {textContentAssets, assetToolInputs["delete_asset"]},
	"create_group":         {textContentGroups, groupToolInputs["create_group"]},
	"update_group":         {textContentGroups, groupToolInputs["update_group"]},
	"delete_group":         {textContentGroups, groupToolInputs["delete_group"]},
	"set_market_data_instrument": {textContentFake, map[string]any{
		"id": "mock-1", "externalSymbol": "AAPL", "enabled": true,
	}},
	"submit_order": {textContentFake, withOrder(map[string]any{
		"mode": "hold", "missingAccount": "reject",
	})},
	"submit_drop_copy_order": {textContentFake, withOrder(map[string]any{"missingAccount": "create"})},
	"confirm_execution":      {textContentFake, map[string]any{"id": testOrderEID}},
	"cancel":                 {textContentFake, map[string]any{"id": testOrderEID}},
	"report_fill":            {textContentReports, reportFillInputs["report_fill"]},
	"send_paper_order": {textContentTrading, withOrder(map[string]any{
		"destination": map[string]any{"route": map[string]any{}},
	})},
	"get_trading":            {textContentTrading, map[string]any{}},
	"get_credential_storage": {textContentControlPlane, map[string]any{}},
	"create_trading_connection": {textContentTrading, map[string]any{
		"provider": "alpaca", "label": "Paper", "mode": "paper",
		"credentials": map[string]any{"key": "value"}, "enabled": false,
	}},
	"update_trading_connection":      {textContentTrading, map[string]any{"connection": "conn", "label": "Paper"}},
	"set_trading_connection_enabled": {textContentTrading, map[string]any{"connection": "conn", "enabled": false}},
	"check_trading_connection":       {textContentTrading, map[string]any{"connection": "conn"}},
	"set_trading_instrument": {textContentTrading, map[string]any{
		"connection": "conn", "externalSymbol": "AAPL",
		"baseAsset": "AAPL", "quoteAsset": "USD", "enabled": true,
	}},
	"delete_trading_instrument": {textContentTrading, map[string]any{"connection": "conn", "externalSymbol": "AAPL"}},
	"verify_trading_symbol":     {textContentTrading, map[string]any{"connection": "conn", "externalSymbol": "AAPL"}},
	"search_trading_symbols":    {textContentTrading, map[string]any{"connection": "conn", "query": "AAPL"}},
	"grant_trading_access":      {textContentTrading, map[string]any{"connection": "conn", "account": "acc"}},
	"revoke_trading_access":     {textContentTrading, map[string]any{"connection": "conn", "account": "acc"}},
	"set_trading_fee_account":   {textContentTrading, map[string]any{"connection": "conn", "account": "acc"}},
	"clear_trading_fee_account": {textContentTrading, map[string]any{"connection": "conn"}},
	"list_trading_destinations": {textContentTrading, map[string]any{"account": "acc"}},
}

// TestEveryMCPToolTextCarriesStructuredContent holds, for the whole Officer
// MCP catalogue, that the text of a successful call carries what its
// structuredContent carries: LM Studio and Cursor hand their model only the
// text, so a field missing there - an order's reject reasons and limit values
// above all - never reaches the user. Every catalogue command needs an entry in
// textContentCalls; callTool requires the text to end with the structured
// content as JSON. The test fails naming every command without an entry, every
// entry naming no command of the catalogue, and every call that fails or whose
// text lacks its structured content.
func TestEveryMCPToolTextCarriesStructuredContent(t *testing.T) {
	t.Parallel()
	catalogue := make(map[string]bool)
	for _, d := range officerToolRegistry().Descriptors() {
		catalogue[d.Name] = true
		if _, ok := textContentCalls[d.Name]; !ok {
			t.Errorf("MCP command %s has no textContentCalls entry: add a successful call", d.Name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(textContentCalls)) {
		if !catalogue[name] {
			t.Errorf("textContentCalls entry %s names no command of the catalogue", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			call := textContentCalls[name]
			res := callTool[any](t, call.source(t), name, call.input)
			if res.IsError {
				t.Fatalf("%s failed: %s", name, textContent(res.Content))
			}
		})
	}
}

// textContentRejects are the reject reasons both order tests below answer: a
// limit carrying its values in details, and a second reason beside it.
var textContentRejects = []domain.OrderReject{
	{
		Code: "order_qty_exceeds_limit", Scope: "account", Policy: "order_size_limit",
		Reason:  "order quantity exceeds the limit",
		Details: "requested 0.002 > limit 0.001",
	},
	{
		Code: "insufficient_funds", Scope: "account", Policy: "spot_funds",
		Reason: "available funds below required amount",
	},
}

// TestRejectedOrderTextNamesEveryReason holds that the text of a rejected
// send_paper_order and of a rejected check_order names the outcome and every
// reject reason with its code, text and limit values, so a client that hands
// its model only the text tells the user which limit fired.
func TestRejectedOrderTextNamesEveryReason(t *testing.T) {
	t.Parallel()
	order := withOrder(map[string]any{"amountValue": "0.002"})
	t.Run("send_paper_order", func(t *testing.T) {
		t.Parallel()
		src := tradingFixture()
		src.paperSendResult.Approval = backend.SignedApproval{
			OrderExternalID: testOrderEID, Verdict: "reject", Reasons: textContentRejects,
		}
		in := maps.Clone(order)
		in["destination"] = map[string]any{"route": map[string]any{}}
		res := callTool[map[string]any](t, src, "send_paper_order", in)
		requireRejectReasonsInText(t, res.IsError, textContent(res.Content),
			"order "+testOrderEID+": reject", `"verdict":"reject"`)
	})
	t.Run("check_order", func(t *testing.T) {
		t.Parallel()
		src := &fakeSource{checkResult: domain.CheckResult{Rejects: textContentRejects}}
		res := callTool[map[string]any](t, src, "check_order", order)
		requireRejectReasonsInText(t, res.IsError, textContent(res.Content),
			"check "+textContentAccount+": reject", `"passed":false`)
	})
}

// requireRejectReasonsInText fails the test unless a successful call's text
// carries every fragment given and the code, reason and details of every
// reason of textContentRejects.
func requireRejectReasonsInText(t *testing.T, isErr bool, text string, fragments ...string) {
	t.Helper()
	requireNotToolError(t, isErr)
	for _, reject := range textContentRejects {
		fragments = append(fragments, `"code":"`+reject.Code+`"`, reject.Reason)
		if reject.Details != "" {
			fragments = append(fragments, reject.Details)
		}
	}
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Errorf("text %q lacks %q", text, fragment)
		}
	}
}
