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
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
)

// oneAccountFeatures is a composition that fixes one account, so clients do
// not see accounts and an omitted account is the Source's to supply.
var oneAccountFeatures = backend.AccountFeatures{Account: "default"}

// listToolsFor lists, by name, the Officer MCP tools a client sees from a
// server built over src.
func listToolsFor(t *testing.T, src frameworkmcp.Source) map[string]*sdkmcp.Tool {
	t.Helper()
	ctx, cancel := context.WithTimeout(systemCtx(), 5*time.Second)
	defer cancel()
	session, closeSession := officerSession(t, ctx, src)
	list, listErr := session.ListTools(ctx, nil)
	closeSession()
	if listErr != nil {
		t.Fatalf("ListTools: %v", listErr)
	}
	tools := make(map[string]*sdkmcp.Tool, len(list.Tools))
	for _, tool := range list.Tools {
		tools[tool.Name] = tool
	}
	return tools
}

// inputShape is the part of a tool input schema the account features edit.
type inputShape struct {
	Required   []string `json:"required"`
	Properties map[string]struct {
		Description string `json:"description"`
	} `json:"properties"`
}

// Single-mode descriptions of the input properties a composition that fixes
// one account relaxes.
const (
	oneAccountAccountDescription = "Account code; may be omitted to act on the one account the server works with"
	oneAccountMissingAccountText = "Applies only when account is named, and is then required: "
)

func decodeInputShape(t *testing.T, tool *sdkmcp.Tool) (inputShape, string) {
	t.Helper()
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal %s input schema: %v", tool.Name, err)
	}
	var shape inputShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("decode %s input schema: %v", tool.Name, err)
	}
	return shape, string(raw)
}

// requiredAccountFields returns the account fields shape requires, in order.
func requiredAccountFields(shape inputShape) []string {
	var fields []string
	for _, name := range []string{"account", "missingAccount"} {
		if slices.Contains(shape.Required, name) {
			fields = append(fields, name)
		}
	}
	return fields
}

// TestInputSchemasByAccountFeatures covers every tool whose input names an
// account or a group. The default composition advertises today's schemas; a
// composition that fixes one account requires neither account nor
// missingAccount and offers no accountGroup, describes the account and policy
// it relaxes as such, and leaves every other tool's schema as it is.
func TestInputSchemasByAccountFeatures(t *testing.T) {
	t.Parallel()
	accountTools := []struct {
		name string
		// required lists the account fields the default composition requires.
		required []string
		// group reports whether the default composition offers accountGroup.
		group bool
		// missingValues is what the single-mode missingAccount description
		// says after it names when the policy applies.
		missingValues string
	}{
		{"get_account_state", []string{"account"}, false, ""},
		{"get_limits", nil, false, ""},
		{"get_audit", nil, false, ""},
		{"check_order", []string{"account"}, false, ""},
		{"update_account", []string{"account"}, false, ""},
		{"delete_account", []string{"account"}, false, ""},
		{"set_account_currency", []string{"account"}, false, ""},
		{"submit_order", []string{"account", "missingAccount"}, false,
			"create to register an account that does not exist yet, reject to fail the submit instead"},
		{"submit_drop_copy_order", []string{"account", "missingAccount"}, false,
			"must be create; a drop-copy reports an execution that already happened and cannot reject an account that does not exist yet"},
		{"send_paper_order", []string{"account"}, false, ""},
		{"grant_trading_access", nil, true, ""},
		{"revoke_trading_access", nil, true, ""},
		{"set_trading_fee_account", []string{"account"}, false, ""},
		{"list_trading_destinations", []string{"account"}, false, ""},
		{"list_orders", nil, false, ""},
		{"list_trades", nil, false, ""},
		{"list_groups", nil, false, ""},
	}
	everyAccount := listToolsFor(t, &fakeSource{})
	oneAccount := listToolsFor(t, &fakeSource{accountFeatures: oneAccountFeatures})
	if len(oneAccount) != len(everyAccount) {
		t.Fatalf("tool count: %d with one account, %d by default",
			len(oneAccount), len(everyAccount))
	}
	covered := make(map[string]bool, len(accountTools))
	for _, tc := range accountTools {
		covered[tc.name] = true
		t.Run(tc.name, func(t *testing.T) {
			def, defWire := decodeInputShape(t, everyAccount[tc.name])
			if got := requiredAccountFields(def); !reflect.DeepEqual(got, tc.required) {
				t.Fatalf("default requires %v, want %v: %s", got, tc.required, defWire)
			}
			if _, ok := def.Properties["accountGroup"]; ok != tc.group {
				t.Fatalf("default accountGroup offered %v, want %v: %s", ok, tc.group, defWire)
			}
			one, oneWire := decodeInputShape(t, oneAccount[tc.name])
			if got := requiredAccountFields(one); len(got) != 0 {
				t.Fatalf("one account requires %v: %s", got, oneWire)
			}
			if _, ok := one.Properties["accountGroup"]; ok {
				t.Fatalf("one account offers accountGroup: %s", oneWire)
			}
			account, ok := one.Properties["account"]
			if !ok {
				t.Fatalf("one account no longer accepts account: %s", oneWire)
			}
			// An account the default composition requires, or offers as one of
			// two subjects, is described as optional; an optional filter keeps
			// its text.
			wantAccount := def.Properties["account"].Description
			if len(tc.required) != 0 || tc.group {
				wantAccount = oneAccountAccountDescription
			}
			if account.Description != wantAccount {
				t.Fatalf("one account describes account as %q, want %q",
					account.Description, wantAccount)
			}
			missing, ok := one.Properties["missingAccount"]
			if ok != (tc.missingValues != "") {
				t.Fatalf("one account offers missingAccount %v: %s", ok, oneWire)
			}
			if want := oneAccountMissingAccountText + tc.missingValues; ok && missing.Description != want {
				t.Fatalf("one account describes missingAccount as %q, want %q",
					missing.Description, want)
			}
		})
	}
	for name, tool := range everyAccount {
		shape, wire := decodeInputShape(t, tool)
		_, account := shape.Properties["account"]
		_, group := shape.Properties["accountGroup"]
		_, missing := shape.Properties["missingAccount"]
		if (account || group || missing) && !covered[name] {
			t.Errorf("tool %s names an account but is not covered here: %s", name, wire)
		}
		if covered[name] {
			continue
		}
		if _, oneWire := decodeInputShape(t, oneAccount[name]); oneWire != wire {
			t.Errorf("tool %s schema changed with one account:\n%s\nwant:\n%s",
				name, oneWire, wire)
		}
	}
}

// orderInput is an order tool input naming no account, with extra merged in.
func orderInput(extra map[string]any) map[string]any {
	in := map[string]any{
		"baseAsset": "AAPL", "quoteAsset": "USD", "side": "buy",
		"amountKind": "quantity", "amountValue": "1", "price": "100",
	}
	maps.Copy(in, extra)
	return in
}

// TestOmittedAccountReachesSourceWithOneAccount covers every tool that
// requires an account by default: with one account fixed, the call reaches the
// Source with the account omitted, or named as that account where the Source
// reads an empty one as a clear (the fee account). The core refuses the tools
// that change the account set there, and that refusal reaches the agent; a
// currency change acts on the one account and reads it back omitted too.
func TestOmittedAccountReachesSourceWithOneAccount(t *testing.T) {
	t.Parallel()
	t.Run("check_order", func(t *testing.T) {
		src := &fakeSource{
			accountFeatures: oneAccountFeatures,
			checkResult:     domain.CheckResult{Passed: true},
		}
		res := callTool[map[string]any](t, src, checkOrderToolName, orderInput(nil))
		requireNotToolError(t, res.IsError)
		if len(src.checkProbes) != 1 || src.checkProbes[0].Account != "" {
			t.Fatalf("probes = %+v, want one with the account omitted", src.checkProbes)
		}
		if got := textContent(res.Content); got != "check default: pass" {
			t.Fatalf("summary = %q, want it to name the one account", got)
		}
	})
	for _, tc := range []struct {
		name string
		in   map[string]any
	}{
		{submitOrderToolName, orderInput(map[string]any{"mode": "immediate"})},
		{submitDropCopyOrderToolName, orderInput(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &approvalFakeSource{}
			src.accountFeatures = oneAccountFeatures
			res := callTool[map[string]any](t, src, tc.name, tc.in)
			requireNotToolError(t, res.IsError)
			if len(src.submitCalls) != 1 || src.submitCalls[0].order.Account != "" ||
				src.submitCalls[0].missing != "" {
				t.Fatalf("submit calls = %+v, want one with account and policy omitted",
					src.submitCalls)
			}
		})
	}
	t.Run("get_account_state", func(t *testing.T) {
		src := &fakeSource{
			accountFeatures: oneAccountFeatures,
			accStateErr:     fmt.Errorf("account %q: %w", "default", domain.ErrNotFound),
		}
		res := callTool[map[string]any](t, src, getAccountStateToolName, map[string]any{})
		requireToolError(t, res.IsError)
		if got, want := textContent(res.Content), "not_found: "+src.accStateErr.Error(); got != want {
			t.Fatalf("error text = %q, want the Source's %q", got, want)
		}
	})
	t.Run("get_account_state_resolved", func(t *testing.T) {
		src := &fakeSource{
			accountFeatures: oneAccountFeatures,
			account:         domain.Account{Code: oneAccountFeatures.Account},
		}
		res := callTool[map[string]any](t, src, getAccountStateToolName, map[string]any{})
		requireNotToolError(t, res.IsError)
		if got, want := textContent(res.Content), "account default: 0 limit(s)"; got != want {
			t.Fatalf("summary = %q, want %q naming the one account", got, want)
		}
	})
	for _, tc := range []struct {
		name  string
		input map[string]any
		calls []string
		args  []any
	}{
		{"grant_trading_access", map[string]any{"connection": "conn"}, []string{"grant"},
			[]any{domain.TradingAccess{Connection: domain.ExternalID("conn")}}},
		{"revoke_trading_access", map[string]any{"connection": "conn"}, []string{"revoke"},
			[]any{domain.ExternalID("conn"), domain.AccountID(""), ""}},
		{"set_trading_fee_account", map[string]any{"connection": "conn"}, []string{"fee"},
			[]any{domain.ExternalID("conn"), oneAccountFeatures.Account}},
		{"list_trading_destinations", map[string]any{}, []string{"destinations"},
			[]any{domain.AccountID(""), domain.TradingMode("")}},
		{"send_paper_order", orderInput(map[string]any{
			"destination": map[string]any{"route": map[string]any{}},
		}), []string{"send-paper"}, []any{
			domain.Order{
				BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
				AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "100",
			},
			backend.SendDestination{Route: "{}"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := tradingFixture()
			src.accountFeatures = oneAccountFeatures
			res := callTool[map[string]any](t, src, tc.name, tc.input)
			requireNotToolError(t, res.IsError)
			if !reflect.DeepEqual(src.calls, tc.calls) || !reflect.DeepEqual(src.arguments, tc.args) {
				t.Fatalf("delegation mismatch: calls %v args %#v", src.calls, src.arguments)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		input map[string]any
		calls []string
		args  []any
	}{
		{"update_account", map[string]any{"code": "desk-2", "title": ""}, []string{"update"},
			[]any{domain.AccountID(""), domain.Account{Code: "desk-2"}}},
		{"delete_account", map[string]any{}, []string{"delete"},
			[]any{domain.AccountID(""), false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := accountsFixture()
			src.accountFeatures = oneAccountFeatures
			src.err = accountsNotManagedRefusal.err
			res := callTool[map[string]any](t, src, tc.name, tc.input)
			requireToolError(t, res.IsError)
			if !reflect.DeepEqual(src.calls, tc.calls) || !reflect.DeepEqual(src.arguments, tc.args) {
				t.Fatalf("delegation mismatch: calls %v args %#v", src.calls, src.arguments)
			}
			if got, want := textContent(res.Content), accountsNotManagedRefusal.want; got != want {
				t.Fatalf("error text = %q, want the Source's %q", got, want)
			}
		})
	}
	t.Run("set_account_currency", func(t *testing.T) {
		src := accountsFixture()
		src.accountFeatures = oneAccountFeatures
		res := callTool[map[string]any](t, src, "set_account_currency",
			map[string]any{"currency": "EUR"})
		requireNotToolError(t, res.IsError)
		wantCalls := []string{"currency", `read ""`}
		wantArgs := []any{domain.AccountID(""), "EUR"}
		if !reflect.DeepEqual(src.calls, wantCalls) || !reflect.DeepEqual(src.arguments, wantArgs) {
			t.Fatalf("delegation mismatch: calls %v args %#v", src.calls, src.arguments)
		}
	})
}

// TestRelaxedInputRefusalsWithOneAccount covers what a relaxed single-mode
// input still refuses before the Source sees it: an undeclared field, which
// decoding the input refuses now that the schema no longer does, and a named
// account without its missingAccount policy, which the handler requires as it
// does by default.
func TestRelaxedInputRefusalsWithOneAccount(t *testing.T) {
	t.Parallel()
	t.Run("undeclared field", func(t *testing.T) {
		src := &fakeSource{
			accountFeatures: oneAccountFeatures,
			checkResult:     domain.CheckResult{Passed: true},
		}
		ctx, cancel := context.WithTimeout(systemCtx(), 5*time.Second)
		defer cancel()
		session, closeSession := officerSession(t, ctx, src)
		_, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
			Name:      checkOrderToolName,
			Arguments: orderInput(map[string]any{"undeclared": "x"}),
		})
		closeSession()
		if err == nil || !strings.Contains(err.Error(), `unknown field "undeclared"`) {
			t.Fatalf("call error = %v, want the undeclared field refused", err)
		}
		if len(src.checkProbes) != 0 {
			t.Fatalf("probes = %+v, want the Source not reached", src.checkProbes)
		}
	})
	t.Run("named account without missingAccount", func(t *testing.T) {
		src := &approvalFakeSource{}
		src.accountFeatures = oneAccountFeatures
		res := callTool[map[string]any](t, src, submitOrderToolName, orderInput(map[string]any{
			"mode": "immediate", "account": string(oneAccountFeatures.Account),
		}))
		requireToolError(t, res.IsError)
		if got, want := textContent(res.Content), domain.ValidateMissingAccountPolicy("").Error(); got != want {
			t.Fatalf("error text = %q, want the /missingAccount required %q", got, want)
		}
		if len(src.submitCalls) != 0 {
			t.Fatalf("submit calls = %+v, want the Source not reached", src.submitCalls)
		}
	})
}

// TestSourceRefusalReachesAgent covers the read tools, create_account, the
// account tool that names no account, and set_account_currency naming an
// account one account excludes: a refusal, invalid input (the account scope's
// own included) or an absent or hidden resource, reaches the agent with the
// Source's message under the code REST answers.
func TestSourceRefusalReachesAgent(t *testing.T) {
	t.Parallel()
	t.Run("get_audit", func(t *testing.T) {
		src := &fakeSource{listAuditErr: fmt.Errorf(
			"account %q is not the one account: %w", "other", domain.ErrInvalid,
		)}
		res := callTool[map[string]any](t, src, getAuditToolName,
			map[string]any{"account": "other"})
		requireToolError(t, res.IsError)
		if got, want := textContent(res.Content), "validation: "+src.listAuditErr.Error(); got != want {
			t.Fatalf("error text = %q, want the Source's %q", got, want)
		}
	})
	t.Run("get_limits", func(t *testing.T) {
		src := &fakeSource{listLimitsErr: fmt.Errorf(
			"account %q: %w", "other", domain.ErrNotFound,
		)}
		res := callTool[map[string]any](t, src, getLimitsToolName,
			map[string]any{"account": "other"})
		requireToolError(t, res.IsError)
		if got, want := textContent(res.Content), "not_found: "+src.listLimitsErr.Error(); got != want {
			t.Fatalf("error text = %q, want the Source's %q", got, want)
		}
	})
	t.Run("create_account", func(t *testing.T) {
		src := accountsFixture()
		src.accountFeatures = oneAccountFeatures
		src.err = accountsNotManagedRefusal.err
		res := callTool[map[string]any](t, src, "create_account",
			map[string]any{"code": "desk-1"})
		requireToolError(t, res.IsError)
		if got, want := textContent(res.Content), accountsNotManagedRefusal.want; got != want {
			t.Fatalf("error text = %q, want the Source's %q", got, want)
		}
	})
	t.Run("set_account_currency", func(t *testing.T) {
		src := accountsFixture()
		src.accountFeatures = oneAccountFeatures
		src.err = domain.NewValidationError("/account", "single_account_mode",
			`single-account mode acts only on the default account "default": `+
				"another account is not accepted")
		res := callTool[map[string]any](t, src, "set_account_currency",
			map[string]any{"account": "other", "currency": "EUR"})
		requireToolError(t, res.IsError)
		if got, want := textContent(res.Content), "validation: "+src.err.Error(); got != want {
			t.Fatalf("error text = %q, want the Source's %q", got, want)
		}
	})
}
