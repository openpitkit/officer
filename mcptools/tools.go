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

// Package mcptools registers the Pit Officer MCP tool set.
package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// listDefaultLimit and listMaxLimit are the default and the cap of the rows a
// read tool returns: get_audit's entries and the list tools' page size.
const listDefaultLimit = 50
const listMaxLimit = 500

const healthToolName = "health"
const healthToolDescription = "Report Pit Officer deployment health: per-node " +
	"engine version, build profile and liveness, and store reachability, " +
	"schema version and path. Read-only status only - exposes no secrets " +
	"and no order-flow control."

const getAccountStateToolName = "get_account_state"
const getAccountStateToolDescription = "Return account row and its " +
	"account-scoped risk barriers. Read-only - no secrets, no order-flow " +
	"control."

const getLimitsToolName = "get_limits"
const getLimitsToolDescription = "Return risk barriers and P&L-bound currencies, " +
	"optionally filtered by account. Read-only - no secrets, no order-flow control."

const getOrderToolName = "get_order"
const getOrderToolDescription = "Return one order addressed by its id: " +
	"its status, its 1:1 signed approval (when issued), and its fills with their " +
	"display prices. Read-only - no secrets, no order-flow control. The opaque " +
	"pre-trade lock is never exposed; only backend-derived display prices are."

const getAuditToolName = "get_audit"
const getAuditToolDescription = "Return recent control-plane audit entries " +
	"(default 50, max 500). Read-only - no secrets, no order-flow control."

const checkOrderToolName = "check_order"
const checkOrderToolDescription = "Run a non-mutating pre-trade dry-run for an " +
	"order: report whether it would pass the engine's risk checks plus the " +
	"reasons (would-be lock prices on pass, structured rejects and any " +
	"would-be account block on reject). Submits nothing and changes no state - " +
	"no order is placed, no funds are reserved."

const setMarketDataInstrumentToolName = "set_market_data_instrument"
const setMarketDataInstrumentToolDescription = "Enable or disable one " +
	"configured market-data instrument. Mutates Officer control-plane config " +
	"only; protected and disabled by default."

const submitOrderToolName = "submit_order"
const submitOrderToolDescription = "Submit an order intent through pre-trade " +
	"and obtain an approval envelope. Mutates engine state and records the " +
	"pre-trade lock; protected and disabled by default."

const submitDropCopyOrderToolName = "submit_drop_copy_order"
const submitDropCopyOrderToolDescription = "Submit a drop-copy order through " +
	"the normal policy pipeline while ignoring policy rejects and existing " +
	"account or group kill-switch blocks. Policies retain their normal state " +
	"changes. The id is optional; when omitted, the store assigns it. No lifecycle " +
	"event is signed. Mutates engine state; protected and disabled by default."

const confirmExecutionToolName = "confirm_execution"
const confirmExecutionToolDescription = "Record confirmation history for a " +
	"workflow order from the signed hold approval Officer recorded at submit. " +
	"The shortcut is rejected after execution-report activity; protected and " +
	"disabled by default."

const cancelToolName = "cancel"
const cancelToolDescription = "Cancel an untouched workflow order from the " +
	"signed hold approval Officer recorded at submit. Optional caller-reported " +
	"leavesQuantity is recorded verbatim when supplied; the SDK receives the " +
	"order's own recorded reservation remainder. After execution-report activity, " +
	"submit an explicit report. " +
	"Protected and disabled by default."

// RegisterTools registers the Pit Officer MCP tools and catalog entries.
func RegisterTools(reg *frameworkmcp.ToolRegistry, src frameworkmcp.Source) {
	_ = src
	if reg == nil {
		return
	}
	registerTool(reg, descriptor(
		healthToolName,
		"Health",
		healthToolDescription,
		"Report Pit Officer deployment health (engine version/profile/liveness, store reachability).",
		false,
		false,
		true,
		true,
	), healthHandler)
	registerTool(reg, descriptor(
		getAccountStateToolName,
		"Get account state",
		getAccountStateToolDescription,
		"Read an account row and its account-scoped risk barriers.",
		false,
		false,
		true,
		true,
	), getAccountStateHandler)
	registerTool(reg, descriptor(
		getLimitsToolName,
		"Get limits",
		getLimitsToolDescription,
		"List configured risk barriers, optionally filtered by account.",
		false,
		false,
		true,
		true,
	), getLimitsHandler)
	registerTool(reg, descriptor(
		getOrderToolName,
		"Get order",
		getOrderToolDescription,
		"Read one order by id with its approval envelope and fills.",
		false,
		false,
		true,
		true,
	), getOrderHandler)
	registerTool(reg, descriptor(
		getAuditToolName,
		"Get audit",
		getAuditToolDescription,
		"Read recent control-plane audit-log entries.",
		false,
		false,
		true,
		true,
	), getAuditHandler)
	registerTool(reg, descriptor(
		listOrdersToolName,
		"List orders",
		listOrdersToolDescription,
		"List orders with filters, including the trading-connection filter, sorting and paging.",
		false,
		false,
		true,
		true,
	), listOrdersHandler)
	registerTool(reg, descriptor(
		listTradesToolName,
		"List trades",
		listTradesToolDescription,
		"List trades with filters, including the trading-connection filter, sorting and paging.",
		false,
		false,
		true,
		true,
	), listTradesHandler)
	registerTool(reg, descriptor(
		listAccountsToolName,
		"List accounts",
		listAccountsToolDescription,
		"List accounts with filters, including the trading-connection filter, sorting and paging.",
		false,
		false,
		true,
		true,
	), listAccountsHandler)
	registerTool(reg, descriptor(
		listGroupsToolName,
		"List groups",
		listGroupsToolDescription,
		"List account groups with filters, including the trading-connection filter, sorting and paging.",
		false,
		false,
		true,
		true,
	), listGroupsHandler)
	registerTool(reg, descriptor(
		checkOrderToolName,
		"Check order",
		checkOrderToolDescription,
		"Run a non-mutating pre-trade dry-run for an order (pass/reject + reasons); changes no state.",
		false,
		false,
		true,
		true,
	), checkOrderHandler)
	reg.Register(descriptor(
		"set_limit",
		"Set limit",
		"",
		"Create or change a risk-limit / policy barrier.",
		true,
		true,
		false,
		false,
	))
	registerTool(reg, descriptor(
		createAccountToolName,
		"Create account",
		createAccountToolDescription,
		"Create an account with a code, an optional title and an optional currency.",
		true,
		true,
		true,
		false,
	), createAccountHandler)
	registerTool(reg, descriptor(
		updateAccountToolName,
		"Update account",
		updateAccountToolDescription,
		"Change an account's code and title.",
		true,
		true,
		true,
		false,
	), updateAccountHandler)
	registerTool(reg, descriptor(
		deleteAccountToolName,
		"Delete account",
		deleteAccountToolDescription,
		"Delete an account, with force together with its dependent rows.",
		true,
		true,
		true,
		false,
	), deleteAccountHandler)
	registerTool(reg, descriptor(
		setAccountCurrencyToolName,
		"Set account currency",
		setAccountCurrencyToolDescription,
		"Set or clear an account's realized P&L currency.",
		true,
		true,
		true,
		false,
	), setAccountCurrencyHandler)
	registerTool(reg, descriptor(
		applyAdjustmentToolName,
		"Apply adjustment",
		applyAdjustmentToolDescription,
		"Apply a deposit, withdrawal, venue fee or correction to an account's balances.",
		true,
		true,
		true,
		false,
	), applyAdjustmentHandler)
	registerTool(reg, descriptor(
		createAssetToolName,
		"Create asset",
		createAssetToolDescription,
		"Create an asset with a code, an optional title and an optional asset class.",
		true,
		true,
		true,
		false,
	), createAssetHandler)
	registerTool(reg, descriptor(
		updateAssetToolName,
		"Update asset",
		updateAssetToolDescription,
		"Change an asset's code, title and asset class.",
		true,
		true,
		true,
		false,
	), updateAssetHandler)
	registerTool(reg, descriptor(
		deleteAssetToolName,
		"Delete asset",
		deleteAssetToolDescription,
		"Delete an asset, with force together with its dependent rows.",
		true,
		true,
		true,
		false,
	), deleteAssetHandler)
	registerTool(reg, descriptor(
		createGroupToolName,
		"Create group",
		createGroupToolDescription,
		"Create an account group with a code, an optional title, an optional currency and optional notes.",
		true,
		true,
		true,
		false,
	), createGroupHandler)
	registerTool(reg, descriptor(
		updateGroupToolName,
		"Update group",
		updateGroupToolDescription,
		"Change an account group's code and title.",
		true,
		true,
		true,
		false,
	), updateGroupHandler)
	registerTool(reg, descriptor(
		deleteGroupToolName,
		"Delete group",
		deleteGroupToolDescription,
		"Delete an account group, with force together with its dependent rows.",
		true,
		true,
		true,
		false,
	), deleteGroupHandler)
	registerTool(reg, descriptor(
		setMarketDataInstrumentToolName,
		"Set market-data instrument",
		setMarketDataInstrumentToolDescription,
		"Enable or disable one configured market-data instrument.",
		true,
		true,
		true,
		false,
	), setMarketDataInstrumentHandler)
	registerTool(reg, descriptor(
		submitOrderToolName,
		"Submit order",
		submitOrderToolDescription,
		"Submit an order intent through pre-trade and obtain an approval envelope.",
		true,
		true,
		true,
		false,
	), submitOrderHandler)
	registerTool(reg, descriptor(
		submitDropCopyOrderToolName,
		"Submit drop-copy order",
		submitDropCopyOrderToolDescription,
		"Submit a drop-copy order through the normal policy pipeline while "+
			"ignoring policy rejects and existing account or group kill-switch "+
			"blocks; policies retain normal state changes, id is optional and "+
			"the store assigns it when omitted, and no lifecycle event is signed.",
		true,
		true,
		true,
		false,
	), submitDropCopyOrderHandler)
	registerTool(reg, descriptor(
		confirmExecutionToolName,
		"Confirm execution",
		confirmExecutionToolDescription,
		"Record confirmation history for an untouched workflow order.",
		true,
		true,
		true,
		false,
	), confirmExecutionHandler)
	registerTool(reg, descriptor(
		cancelToolName,
		"Cancel",
		cancelToolDescription,
		"Cancel an untouched workflow order from Officer's recorded approval.",
		true,
		true,
		true,
		false,
	), cancelHandler)
	registerTool(reg, descriptor(
		reportFillToolName,
		"Report fill",
		reportFillToolDescription,
		"Apply an execution report - a fill, a commission or a lifecycle "+
			"status - to an order; attested on an ordinary order, signed unless "+
			"eSign is off, and unattested on a drop-copy order.",
		true,
		true,
		true,
		false,
	), reportFillHandler)
	registerTool(reg, descriptor("send_paper_order", "Send paper order",
		"Check, hold and send an order to a paper venue. Returns the approval and "+
			"acknowledged, refused or unknown send outcome. Mutating, protected and "+
			"disabled by default. send_live_order comes with the live-trading token.",
		"Check, hold and send an order to a paper venue.",
		true, true, true, false), sendPaperOrderHandler)
	registerTool(reg, descriptor("get_trading", "Get trading",
		"Read trading providers and connections. Read-only - credentials are never exposed.",
		"Read trading providers and connections.", false, false, true, true), getTradingHandler)
	registerTool(reg, descriptor("get_credential_storage", "Get credential storage",
		"Report whether stored credentials are sealed under the master key and, when they are not, whether trading connections or market-data sources hold keys in plain text. Read-only - credentials are never exposed.",
		"Read whether stored credentials are sealed and which hold plaintext keys.", false, false, true, true), getCredentialStorageHandler)
	registerTool(reg, descriptor("create_trading_connection", "Create trading connection",
		"Create a trading connection with write-only credentials. Mutates Officer control-plane config; protected and disabled by default.",
		"Create a trading connection with write-only credentials.", true, true, true, false), createTradingConnectionHandler)
	registerTool(reg, descriptor("update_trading_connection", "Update trading connection",
		"Update a trading connection label or write-only credentials. Mutates Officer control-plane config; protected and disabled by default.",
		"Update a trading connection label or write-only credentials.", true, true, true, false), updateTradingConnectionHandler)
	registerTool(reg, descriptor("set_trading_connection_enabled", "Set trading connection enabled",
		"Enable or disable a trading connection. Mutates Officer control-plane config; protected and disabled by default.",
		"Enable or disable a trading connection.", true, true, true, false), setTradingConnectionEnabledHandler)
	registerTool(reg, descriptor("check_trading_connection", "Check trading connection",
		"Check the venue identity and status of a trading connection. Read-only - credentials are never exposed.",
		"Check the venue identity and status of a trading connection.", false, false, true, true), checkTradingConnectionHandler)
	registerTool(reg, descriptor("set_trading_instrument", "Set trading instrument",
		"Set a trading instrument mapping. Mutates Officer control-plane config; protected and disabled by default.",
		"Set a trading instrument mapping.", true, true, true, false), setTradingInstrumentHandler)
	registerTool(reg, descriptor("delete_trading_instrument", "Delete trading instrument",
		"Delete a trading instrument mapping. Mutates Officer control-plane config; protected and disabled by default.",
		"Delete a trading instrument mapping.", true, true, true, false), deleteTradingInstrumentHandler)
	registerTool(reg, descriptor("verify_trading_symbol", "Verify trading symbol",
		"Verify whether a venue symbol exists and is tradable. Read-only - credentials are never exposed.",
		"Verify whether a venue symbol exists and is tradable.", false, false, true, true), verifyTradingSymbolHandler)
	registerTool(reg, descriptor("search_trading_symbols", "Search trading symbols",
		"Search venue symbols for a trading connection. Read-only - credentials are never exposed.",
		"Search venue symbols for a trading connection.", false, false, true, true), searchTradingSymbolsHandler)
	registerTool(reg, descriptor("grant_trading_access", "Grant trading access",
		"Grant an Officer account access to a trading connection, or an account group where the server offers account groups. A group grant needs the connection's fee account on providers with unlinked fees. Mutates Officer control-plane config; protected and disabled by default.",
		"Grant an Officer account access to a trading connection, or an account group where the server offers account groups; a group grant needs the connection's fee account on providers with unlinked fees.", true, true, true, false), grantTradingAccessHandler)
	registerTool(reg, descriptor("revoke_trading_access", "Revoke trading access",
		"Revoke the access of an Officer account to a trading connection, or of an account group where the server offers account groups. Mutates Officer control-plane config; protected and disabled by default.",
		"Revoke the access of an Officer account to a trading connection, or of an account group where the server offers account groups.", true, true, true, false), revokeTradingAccessHandler)
	registerTool(reg, descriptor("set_trading_fee_account", "Set trading fee account",
		"Set the Officer account receiving unlinked venue fees. Mutates Officer control-plane config; protected and disabled by default.",
		"Set the Officer account receiving unlinked venue fees.", true, true, true, false), setTradingFeeAccountHandler)
	registerTool(reg, descriptor("clear_trading_fee_account", "Clear trading fee account",
		"Clear the Officer account receiving unlinked venue fees. Mutates Officer control-plane config; protected and disabled by default.",
		"Clear the Officer account receiving unlinked venue fees.", true, true, true, false), clearTradingFeeAccountHandler)
	registerTool(reg, descriptor("list_trading_destinations", "List trading destinations",
		"List the trading destinations available to an Officer account. Read-only - credentials are never exposed.",
		"List the trading destinations available to an Officer account.", false, false, true, true), listTradingDestinationsHandler)
}

func descriptor(
	name string,
	title string,
	description string,
	agentDescription string,
	mutating bool,
	protective bool,
	implemented bool,
	defaultEnabled bool,
) frameworkmcp.ToolDescriptor {
	return frameworkmcp.ToolDescriptor{
		Name:             name,
		Title:            title,
		Description:      description,
		AgentDescription: agentDescription,
		Mutating:         mutating,
		Protective:       protective,
		Implemented:      implemented,
		DefaultEnabled:   defaultEnabled,
	}
}

func registerTool[In, Out any](
	reg *frameworkmcp.ToolRegistry,
	d frameworkmcp.ToolDescriptor,
	handler func(frameworkmcp.Source) frameworkmcp.ToolBody[In, Out],
) {
	d.Register = func(server *sdkmcp.Server, deps frameworkmcp.RegisterDeps) {
		outputSchema, err := outputSchemaFor[Out]()
		if err != nil {
			// Register has no error path (frameworkmcp.ToolDescriptor), and
			// sdkmcp.AddTool already panics on this same failure - a schema it
			// cannot infer for In or Out - so a schema failure stays the one
			// failure mode registration has: a programming error, loud at
			// server build.
			panic(fmt.Errorf("adding tool %q: %w", d.Name, err))
		}
		tool := &sdkmcp.Tool{
			Name:         d.Name,
			Description:  d.Description,
			OutputSchema: outputSchema,
		}
		// A composition that offers clients accounts leaves the input schema
		// to the SDK, which infers it from In.
		if !deps.Source.AccountFeatures().Accounts() {
			tool.InputSchema = oneAccountInputSchema[In]()
		}
		sdkmcp.AddTool(server, tool, frameworkmcp.Guard(d, deps, handler(deps.Source)))
	}
	reg.Register(d)
}

// Input property descriptions of a composition that fixes one account. A
// required missingAccount opens with requiredDescriptionPrefix by default; with
// one account fixed it applies only when account is named, so that opening is
// replaced and the allowed values that follow it are kept.
const (
	oneAccountAccountDescription   = "Account code; may be omitted to act on the one account the server works with"
	requiredDescriptionPrefix      = "Required: "
	oneAccountMissingAccountPrefix = "Applies only when account is named, and is then required: "
)

// oneAccountInputSchema is the input schema inferred from In, edited for a
// composition that fixes one account: an omitted account means that account,
// so neither account nor its missingAccount policy is required, and account
// groups are not offered, so accountGroup is not a property. The descriptions
// of the properties it relaxes say so in place of the default text. A tool
// without these fields keeps the inferred schema unchanged.
func oneAccountInputSchema[In any]() *jsonschema.Schema {
	schema, err := jsonschema.For[In]()
	if err != nil {
		// sdkmcp.AddTool panics on the same inference failure.
		panic(err)
	}
	_, group := schema.Properties["accountGroup"]
	delete(schema.Properties, "accountGroup")
	account := slices.Contains(schema.Required, "account")
	missing := slices.Contains(schema.Required, "missingAccount")
	schema.Required = slices.DeleteFunc(schema.Required, func(name string) bool {
		return name == "account" || name == "missingAccount"
	})
	if account || group {
		// The default text names the account as required, or as one of two
		// subjects with accountGroup.
		schema.Properties["account"].Description = oneAccountAccountDescription
	}
	if missing {
		property := schema.Properties["missingAccount"]
		values, ok := strings.CutPrefix(property.Description, requiredDescriptionPrefix)
		if !ok {
			panic(fmt.Errorf(
				"missingAccount description %q does not open with %q",
				property.Description, requiredDescriptionPrefix,
			))
		}
		property.Description = oneAccountMissingAccountPrefix + values
	}
	if account || missing {
		// The SDK validates the decoded In value, where a field no longer
		// required is still present, empty when omitted, and it checks such an
		// empty optional field as an additional property. Decoding In already
		// refuses every undeclared field, so the schema stops refusing
		// additional properties instead.
		schema.AdditionalProperties = nil
	}
	return schema
}

// outputSchemaFor returns the output schema the SDK infers for Out
// (jsonschema.For, as sdkmcp.AddTool would), with every time.Time advertised
// as the RFC 3339 string it marshals to. The inference sees only time.Time's
// unexported fields and would advertise an object with no properties, which
// no marshalled time matches, so a client that validates structured content
// against the schema would reject every result carrying a time.
func outputSchemaFor[Out any]() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[Out]()
	if err != nil {
		return nil, err
	}
	if err := timesAsStrings(reflect.TypeFor[Out](), schema); err != nil {
		return nil, fmt.Errorf("output schema of %v: %w", reflect.TypeFor[Out](), err)
	}
	return schema, nil
}

// timesAsStrings walks t alongside s, the schema jsonschema.For inferred for
// t, the way the inference built it - through pointers, struct fields by
// their JSON names, slice and array items and map values - and rewrites the
// subschema of every time.Time into a date-time string, keeping its
// description and the null a pointer allows.
func timesAsStrings(t reflect.Type, s *jsonschema.Schema) error {
	if s == nil {
		return fmt.Errorf("no schema for %v", t)
	}
	nullable := false
	for t.Kind() == reflect.Pointer {
		nullable = true
		t = t.Elem()
	}
	switch {
	case t == reflect.TypeFor[time.Time]():
		*s = jsonschema.Schema{Description: s.Description, Format: "date-time"}
		if nullable {
			s.Types = []string{"null", "string"}
		} else {
			s.Type = "string"
		}
	case t.Kind() == reflect.Struct:
		for i := range t.NumField() {
			field := t.Field(i)
			name, ok := schemaFieldName(field)
			if !ok {
				continue
			}
			if err := timesAsStrings(field.Type, s.Properties[name]); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	case t.Kind() == reflect.Slice, t.Kind() == reflect.Array:
		return timesAsStrings(t.Elem(), s.Items)
	case t.Kind() == reflect.Map:
		return timesAsStrings(t.Elem(), s.AdditionalProperties)
	}
	return nil
}

// schemaFieldName returns the property name jsonschema.For gives a struct
// field, which is the name encoding/json gives it: the first element of the
// json tag, or the Go name when that is empty. ok is false for a field the
// inference leaves out - an unexported one or one tagged "-".
func schemaFieldName(f reflect.StructField) (string, bool) {
	if !f.IsExported() {
		return "", false
	}
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		return f.Name, true
	}
	name, _, found := strings.Cut(tag, ",")
	if name == "-" && !found {
		return "", false
	}
	if name == "" {
		return f.Name, true
	}
	return name, true
}

type healthInput struct{}

type healthOutput struct {
	Node    healthNode `json:"node"`
	Healthy bool       `json:"healthy"`
}

type healthNode struct {
	Engine healthEngine `json:"engine"`
	Store  healthStore  `json:"store"`
}

type healthEngine struct {
	Version      string `json:"version"`
	BuildProfile string `json:"buildProfile"`
	Running      bool   `json:"running"`
}

type healthStore struct {
	Path          string `json:"path"`
	SchemaVersion int    `json:"schemaVersion"`
	Reachable     bool   `json:"reachable"`
}

type getAccountStateInput struct {
	Account string `json:"account" jsonschema:"Account code"`
}

type getAccountStateOutput struct {
	Account accountDTO `json:"account"`
	Limits  limitsDTO  `json:"limits"`
}

type getLimitsInput struct {
	Account string `json:"account,omitempty" jsonschema:"Optional account-code filter; omit to return all barriers"`
}

type getLimitsOutput struct {
	Limits limitsDTO `json:"limits"`
}

type getOrderInput struct {
	OrderExternalID string `json:"id" jsonschema:"Order id returned by submit_order"`
}

type getOrderOutput struct {
	Order      orderDTO          `json:"order"`
	Approval   *orderApprovalDTO `json:"approval"`
	Trades     []tradeDTO        `json:"trades"`
	VenueOrder *venueOrderDTO    `json:"venueOrder,omitempty"`
}

type getAuditInput struct {
	Category string `json:"category,omitempty" jsonschema:"Audit stream: control (default) | trading | all"`
	Account  string `json:"account,omitempty" jsonschema:"Account code filter (optional)"`
	Limit    int    `json:"limit,omitempty" jsonschema:"Max rows to return (default 50 cap 500)"`
}

type getAuditOutput struct {
	Entries []auditDTO `json:"entries"`
}

type checkOrderInput struct {
	Account     string `json:"account" jsonschema:"Account code"`
	BaseAsset   string `json:"baseAsset" jsonschema:"Asset being bought or sold"`
	QuoteAsset  string `json:"quoteAsset" jsonschema:"Asset used for pricing"`
	Side        string `json:"side" jsonschema:"buy or sell"`
	AmountKind  string `json:"amountKind" jsonschema:"quantity or volume"`
	AmountValue string `json:"amountValue" jsonschema:"Order size as an exact decimal string"`
	Price       string `json:"price,omitempty" jsonschema:"Limit price as an exact decimal string; omit for market/unpriced"`
}

type checkOrderOutput struct {
	WouldBlock        *checkOrderBlockDTO   `json:"wouldBlock"`
	Rejects           []checkOrderRejectDTO `json:"rejects"`
	WouldDisplayPrice string                `json:"wouldDisplayPrice"`
	Passed            bool                  `json:"passed"`
}

type setMarketDataInstrumentInput struct {
	InstanceExternalID string `json:"id" jsonschema:"Market-data instance id"`
	ExternalSymbol     string `json:"externalSymbol" jsonschema:"Provider-side instrument symbol"`
	Enabled            bool   `json:"enabled" jsonschema:"Whether the instrument should be enabled"`
}

type setMarketDataInstrumentOutput struct {
	InstanceExternalID string `json:"id"`
	ExternalSymbol     string `json:"externalSymbol"`
	Enabled            bool   `json:"enabled"`
}

type submitOrderInput struct {
	Account     string `json:"account" jsonschema:"Account code"`
	BaseAsset   string `json:"baseAsset" jsonschema:"Asset being bought or sold"`
	QuoteAsset  string `json:"quoteAsset" jsonschema:"Asset used for pricing"`
	Side        string `json:"side" jsonschema:"buy or sell"`
	AmountKind  string `json:"amountKind" jsonschema:"quantity or volume"`
	AmountValue string `json:"amountValue" jsonschema:"Order size as an exact decimal string"`
	Price       string `json:"price,omitempty" jsonschema:"Limit price as an exact decimal string; omit for market"`
	Mode        string `json:"mode" jsonschema:"Required: immediate commits and settles at the lock price, hold waits for execution reports"`
	ExternalID  string `json:"id,omitempty" jsonschema:"Optional caller-supplied unique order id; omit to have the server generate one"`

	MissingAccount string `json:"missingAccount" jsonschema:"Required: create to register an account that does not exist yet, reject to fail the submit instead"`
}

type submitDropCopyOrderInput struct {
	Account     string `json:"account" jsonschema:"Account code"`
	BaseAsset   string `json:"baseAsset" jsonschema:"Asset being bought or sold"`
	QuoteAsset  string `json:"quoteAsset" jsonschema:"Asset used for pricing"`
	Side        string `json:"side" jsonschema:"buy or sell"`
	AmountKind  string `json:"amountKind" jsonschema:"quantity or volume"`
	AmountValue string `json:"amountValue" jsonschema:"Order size as an exact decimal string"`
	Price       string `json:"price,omitempty" jsonschema:"Required limit price as an exact decimal string; market orders are rejected for drop-copy"`
	ExternalID  string `json:"id,omitempty" jsonschema:"Optional caller-supplied unique order id; omit to have the store assign one"`

	MissingAccount string `json:"missingAccount" jsonschema:"Required: must be create; a drop-copy reports an execution that already happened and cannot reject an account that does not exist yet"`
}

type submitOrderOutput struct {
	SignedApproval  string                `json:"signedApproval"`
	KeyID           string                `json:"keyId"`
	OrderExternalID string                `json:"id"`
	Verdict         string                `json:"verdict"`
	Reasons         []checkOrderRejectDTO `json:"reasons,omitempty"`
}

type submitDropCopyOrderOutput struct {
	OrderExternalID string `json:"id"`
	Status          string `json:"status"`
}

type confirmExecutionInput struct {
	OrderExternalID string `json:"id" jsonschema:"Order id returned by submit_order"`
}

type confirmExecutionOutput struct {
	OrderExternalID   string `json:"id"`
	Status            string `json:"status"`
	SignedAttestation string `json:"signedAttestation,omitempty"`
	KeyID             string `json:"keyId,omitempty"`
}

type cancelInput struct {
	OrderExternalID string `json:"id" jsonschema:"Order id returned by submit_order"`
	LeavesQuantity  string `json:"leavesQuantity,omitempty" jsonschema:"Optional caller-reported open base quantity recorded verbatim when supplied; the SDK receives the order's own recorded reservation remainder; whitespace is supplied data and is validated by the report contract"`
	Reason          string `json:"reason,omitempty" jsonschema:"Human-readable cancellation reason"`
}

type cancelOutput struct {
	OrderExternalID   string `json:"id"`
	Status            string `json:"status"`
	SignedAttestation string `json:"signedAttestation,omitempty"`
	KeyID             string `json:"keyId,omitempty"`
}

// accountDTO mirrors the HTTP account shape for the block state: blocked,
// blockReason and blockSource are EFFECTIVE (the account's own block joined
// with its group's), so an agent reading only blocked learns whether the
// account can trade right now; the per-tier fields carry the two independent
// underlying blocks.
type accountDTO struct {
	Code               string `json:"code"`
	Title              string `json:"title"`
	Group              string `json:"group"`
	BlockReason        string `json:"blockReason"`
	BlockSource        string `json:"blockSource"`
	AccountBlockReason string `json:"accountBlockReason"`
	GroupBlockReason   string `json:"groupBlockReason"`
	Blocked            bool   `json:"blocked"`
	AccountBlocked     bool   `json:"accountBlocked"`
	GroupBlocked       bool   `json:"groupBlocked"`
}

type limitsDTO struct {
	RateLimits               []rateLimitDTO               `json:"rateLimits"`
	OrderSizeLimits          []orderSizeLimitDTO          `json:"orderSizeLimits"`
	SpotFundsPnlBoundsLimits []spotFundsPnlBoundsLimitDTO `json:"spotFundsPnlBoundsLimits"`
}

type rateLimitDTO struct {
	Scope     string `json:"scope"`
	Account   string `json:"account"`
	Asset     string `json:"asset"`
	Window    string `json:"window"`
	MaxOrders uint64 `json:"maxOrders"`
}

type orderSizeLimitDTO struct {
	Scope       string `json:"scope"`
	Account     string `json:"account"`
	Asset       string `json:"asset"`
	MaxQuantity string `json:"maxQuantity"`
	MaxNotional string `json:"maxNotional"`
}

type spotFundsPnlBoundsLimitDTO struct {
	Scope        string `json:"scope"`
	Account      string `json:"account"`
	AccountGroup string `json:"accountGroup"`
	Currency     string `json:"currency"`
	LowerBound   string `json:"lowerBound"`
	UpperBound   string `json:"upperBound"`
}

type auditDTO struct {
	At      time.Time `json:"at"`
	Actor   string    `json:"actor"`
	Action  string    `json:"action"`
	Account string    `json:"account"`
	Asset   string    `json:"asset"`
	// Group is the structured group handle of a group action, so an agent
	// selects a group's rows by identity instead of matching the free-form
	// detail text, where one group's code can appear inside another's.
	Group      string `json:"group"`
	Detail     string `json:"detail"`
	ExternalID string `json:"id"`
}

type orderDTO struct {
	At                  time.Time       `json:"at"`
	ExternalID          string          `json:"id"`
	Account             string          `json:"account"`
	BaseAsset           string          `json:"baseAsset"`
	QuoteAsset          string          `json:"quoteAsset"`
	Side                string          `json:"side"`
	AmountKind          string          `json:"amountKind"`
	AmountValue         string          `json:"amountValue"`
	CommissionSubtotals []commissionDTO `json:"commissionSubtotals"`
	LeavesQuantity      string          `json:"leavesQuantity"`
	Price               string          `json:"price"`
	Status              string          `json:"status"`
	Source              string          `json:"source"`
	DisplayPrice        string          `json:"displayPrice"`
	DropCopy            bool            `json:"dropCopy"`
}

// commissionDTO mirrors the HTTP commission shape (amount + currency) so an MCP
// client sees the same commission data as the REST surface.
type commissionDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type orderApprovalDTO struct {
	SignedEnvelope string `json:"signedEnvelope"`
	KeyID          string `json:"keyId"`
	Alg            string `json:"alg"`
	Mode           string `json:"mode"`
	IssuedAt       string `json:"issuedAt"`
	Signed         bool   `json:"signed"`
}

type tradeDTO struct {
	At         time.Time      `json:"at"`
	ExternalID string         `json:"id"`
	Side       string         `json:"side"`
	Quantity   string         `json:"quantity"`
	Price      string         `json:"price"`
	LockPrice  string         `json:"lockPrice"`
	Commission *commissionDTO `json:"commission,omitempty"`
}

type checkOrderRejectDTO struct {
	Code    string `json:"code"`
	Scope   string `json:"scope"`
	Policy  string `json:"policy"`
	Reason  string `json:"reason"`
	Details string `json:"details"`
}

type checkOrderBlockDTO struct {
	Account string `json:"account"`
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Details string `json:"details"`
}

func toAccountDTO(a domain.Account, block domain.AccountBlockState) accountDTO {
	return accountDTO{
		Code:               string(a.Code),
		Title:              a.Title,
		Group:              a.GroupCode,
		BlockReason:        block.Reason,
		BlockSource:        string(block.Source),
		AccountBlockReason: block.AccountReason,
		GroupBlockReason:   block.GroupReason,
		Blocked:            block.Blocked,
		AccountBlocked:     block.AccountBlocked,
		GroupBlocked:       block.GroupBlocked,
	}
}

// accountBlock resolves the account's effective block. An account in no group
// needs no group read: nothing but its own flag can block it.
func accountBlock(
	ctx context.Context, src frameworkmcp.Source, a domain.Account,
) (domain.AccountBlockState, error) {
	if a.GroupCode == "" {
		return domain.ResolveAccountBlock(a, domain.AccountGroup{}), nil
	}
	groups, err := src.ListGroups(ctx)
	if err != nil {
		return domain.AccountBlockState{}, err
	}
	return domain.NewGroupBlockIndex(groups).Resolve(a), nil
}

func toLimitsDTO(l node.AccountLimits) limitsDTO {
	rate := make([]rateLimitDTO, 0, len(l.RateLimits))
	for _, r := range l.RateLimits {
		rate = append(rate, rateLimitDTO{
			Scope:     string(r.Scope),
			Account:   string(r.Account),
			Asset:     r.Asset,
			Window:    r.Window.String(),
			MaxOrders: r.MaxOrders,
		})
	}
	size := make([]orderSizeLimitDTO, 0, len(l.OrderSizeLimits))
	for _, o := range l.OrderSizeLimits {
		size = append(size, orderSizeLimitDTO{
			Scope:       string(o.Scope),
			Account:     string(o.Account),
			Asset:       o.Asset,
			MaxQuantity: o.MaxQuantity,
			MaxNotional: o.MaxNotional,
		})
	}
	spotFundsPnl := make(
		[]spotFundsPnlBoundsLimitDTO,
		0,
		len(l.SpotFundsPnlBoundsLimits),
	)
	for _, p := range l.SpotFundsPnlBoundsLimits {
		spotFundsPnl = append(spotFundsPnl, spotFundsPnlBoundsLimitDTO{
			Scope:        string(p.Scope),
			Account:      string(p.Account),
			AccountGroup: p.AccountGroup,
			Currency:     p.Currency,
			LowerBound:   p.LowerBound,
			UpperBound:   p.UpperBound,
		})
	}
	return limitsDTO{
		RateLimits:               rate,
		OrderSizeLimits:          size,
		SpotFundsPnlBoundsLimits: spotFundsPnl,
	}
}

func limitsCount(l node.AccountLimits) int {
	return len(l.RateLimits) + len(l.OrderSizeLimits) +
		len(l.SpotFundsPnlBoundsLimits)
}

func toAuditDTO(row domain.AuditRow) auditDTO {
	return auditDTO{
		ExternalID: row.ExternalID.String(),
		At:         row.At,
		Actor:      row.Actor,
		Action:     string(row.Action),
		Account:    string(row.Account),
		Asset:      row.Asset,
		Group:      row.Group,
		Detail:     row.Detail,
	}
}

func toOrderDTO(detail domain.OrderDetail) orderDTO {
	o := detail.Order
	return orderDTO{
		At:                  o.At,
		ExternalID:          o.ExternalID.String(),
		Account:             string(o.Account),
		BaseAsset:           o.BaseAsset,
		QuoteAsset:          o.QuoteAsset,
		Side:                string(o.Side),
		AmountKind:          string(o.AmountKind),
		AmountValue:         o.AmountValue,
		CommissionSubtotals: toCommissionDTOs(o.CommissionSubtotals),
		LeavesQuantity:      o.Leaves,
		Price:               o.Price,
		Status:              string(o.Status),
		Source:              string(o.Source),
		DisplayPrice:        detail.DisplayPrice,
		DropCopy:            o.DropCopy,
	}
}

func toCommissionDTO(c *domain.Commission) *commissionDTO {
	if c == nil {
		return nil
	}
	return &commissionDTO{Amount: c.Amount, Currency: c.Currency}
}

func toCommissionDTOs(in []domain.Commission) []commissionDTO {
	if in == nil {
		return []commissionDTO{}
	}
	out := make([]commissionDTO, 0, len(in))
	for i := range in {
		out = append(out, *toCommissionDTO(&in[i]))
	}
	return out
}

// toOrderApprovalDTO maps the order's submit-verdict attestation onto the MCP
// approval DTO, or returns nil when no event carries one. The submit verdict
// event (pre_trade_accepted / pre_trade_rejected) is preferred; failing that,
// the first attested event is used, so the MCP surface still reflects an
// attested order created through a non-submit path.
func toOrderApprovalDTO(detail domain.OrderDetail) *orderApprovalDTO {
	att := submitVerdictAttestation(detail)
	if att == nil {
		return nil
	}
	return &orderApprovalDTO{
		SignedEnvelope: att.SignedEnvelope,
		KeyID:          att.KeyID,
		Alg:            att.Alg,
		Mode:           att.Mode,
		IssuedAt:       att.IssuedAt,
		Signed:         att.Alg == "ed25519",
	}
}

// submitVerdictAttestation returns the attestation of the order's submit verdict
// event when present, else the first attested event's attestation, else nil.
func submitVerdictAttestation(detail domain.OrderDetail) *domain.EventAttestation {
	var first *domain.EventAttestation
	for i := range detail.Events {
		att := detail.Events[i].Attestation
		if att == nil {
			continue
		}
		if first == nil {
			first = att
		}
		switch detail.Events[i].Type {
		case domain.OrderEventPreTradeAccepted, domain.OrderEventPreTradeRejected:
			return att
		}
	}
	return first
}

func toTradeDTOs(trades []domain.Trade) []tradeDTO {
	out := make([]tradeDTO, 0, len(trades))
	for _, t := range trades {
		out = append(out, tradeDTO{
			At:         t.At,
			ExternalID: t.ExternalID.String(),
			Side:       string(t.Side),
			Quantity:   t.Quantity,
			Price:      t.Price,
			LockPrice:  t.LockPrice,
			Commission: toCommissionDTO(t.Commission),
		})
	}
	return out
}

func toAuditDTOs(rows []domain.AuditRow) []auditDTO {
	out := make([]auditDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAuditDTO(r))
	}
	return out
}

func toCheckOrderOutput(r domain.CheckResult) checkOrderOutput {
	rejects := make([]checkOrderRejectDTO, 0, len(r.Rejects))
	for _, rej := range r.Rejects {
		rejects = append(rejects, toMCPRejectDTO(rej))
	}
	var block *checkOrderBlockDTO
	if r.WouldBlock != nil {
		block = &checkOrderBlockDTO{
			Account: string(r.WouldBlock.Account),
			Code:    r.WouldBlock.Code,
			Reason:  r.WouldBlock.Reason,
			Details: r.WouldBlock.Details,
		}
	}
	return checkOrderOutput{
		WouldBlock:        block,
		Rejects:           rejects,
		WouldDisplayPrice: r.WouldLockPrice,
		Passed:            r.Passed,
	}
}

func toMCPRejectDTOs(rejects []domain.OrderReject) []checkOrderRejectDTO {
	if len(rejects) == 0 {
		return nil
	}
	out := make([]checkOrderRejectDTO, 0, len(rejects))
	for _, rej := range rejects {
		out = append(out, toMCPRejectDTO(rej))
	}
	return out
}

func toMCPRejectDTO(rej domain.OrderReject) checkOrderRejectDTO {
	return checkOrderRejectDTO{
		Code:    rej.Code,
		Scope:   rej.Scope,
		Policy:  rej.Policy,
		Reason:  rej.Reason,
		Details: rej.Details,
	}
}

func healthHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[healthInput, healthOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		_ healthInput,
	) (string, healthOutput, error) {
		status, err := src.Status(ctx)
		if err != nil {
			return "", healthOutput{}, toolError(err)
		}
		out := toHealthOutput(status)
		return summarizeHealth(out), out, nil
	}
}

func getAccountStateHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[getAccountStateInput, getAccountStateOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in getAccountStateInput,
	) (string, getAccountStateOutput, error) {
		id := domain.AccountID(strings.TrimSpace(in.Account))
		// A composition that fixes one account resolves an omitted one in the
		// Source.
		if id == "" && src.AccountFeatures().Accounts() {
			return "", getAccountStateOutput{}, fmt.Errorf("account is required")
		}
		acc, limits, err := src.GetAccountState(ctx, id)
		if err != nil {
			return "", getAccountStateOutput{}, toolError(err)
		}
		block, err := accountBlock(ctx, src, acc)
		if err != nil {
			return "", getAccountStateOutput{}, toolError(err)
		}
		out := getAccountStateOutput{
			Account: toAccountDTO(acc, block),
			Limits:  toLimitsDTO(limits),
		}
		summary := fmt.Sprintf(
			"account %s: %d limit(s)", acc.Code, limitsCount(limits),
		)
		if block.Blocked {
			// Name the blocking tier only; the operator-supplied reason stays
			// in the structured content.
			summary += fmt.Sprintf(" - blocked (%s)", block.Source)
		}
		return summary, out, nil
	}
}

func getLimitsHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[getLimitsInput, getLimitsOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in getLimitsInput,
	) (string, getLimitsOutput, error) {
		account := domain.AccountID(strings.TrimSpace(in.Account))
		limits, err := src.ListLimits(ctx, account)
		if err != nil {
			return "", getLimitsOutput{}, toolError(err)
		}
		out := getLimitsOutput{Limits: toLimitsDTO(limits)}
		return fmt.Sprintf("%d limit(s)", limitsCount(limits)), out, nil
	}
}

func getOrderHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[getOrderInput, getOrderOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in getOrderInput,
	) (string, getOrderOutput, error) {
		id := strings.TrimSpace(in.OrderExternalID)
		if id == "" {
			return "", getOrderOutput{}, fmt.Errorf("id is required")
		}
		detail, err := src.GetOrder(ctx, id)
		if err != nil {
			return "", getOrderOutput{}, toolError(err)
		}
		out := getOrderOutput{
			Order:    toOrderDTO(detail),
			Approval: toOrderApprovalDTO(detail),
			Trades:   toTradeDTOs(detail.Trades),
		}
		if detail.Venue != nil {
			venue, err := toVenueOrderDTO(*detail.Venue)
			if err != nil {
				return "", getOrderOutput{}, toolError(err)
			}
			out.VenueOrder = &venue
		}
		return fmt.Sprintf(
			"order %s: %s (%d fill(s))",
			detail.Order.ExternalID.String(),
			detail.Order.Status,
			len(detail.Trades),
		), out, nil
	}
}

func auditCategoryActions(category string) []domain.AuditAction {
	actions, known := domain.AuditActionsForCategory(strings.TrimSpace(category))
	if !known {
		return domain.AuditActionsByCategory(domain.AuditCategoryControl)
	}
	return actions
}

func getAuditHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[getAuditInput, getAuditOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in getAuditInput,
	) (string, getAuditOutput, error) {
		n := in.Limit
		if n <= 0 {
			n = listDefaultLimit
		}
		if n > listMaxLimit {
			n = listMaxLimit
		}
		filter := domain.AuditFilter{
			Account: domain.AccountID(strings.TrimSpace(in.Account)),
			Actions: auditCategoryActions(in.Category),
		}
		rows, err := src.ListAuditFiltered(ctx, filter, n)
		if err != nil {
			return "", getAuditOutput{}, toolError(err)
		}
		out := getAuditOutput{Entries: toAuditDTOs(rows)}
		return fmt.Sprintf("%d audit entry/entries", len(rows)), out, nil
	}
}

func checkOrderHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[checkOrderInput, checkOrderOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in checkOrderInput,
	) (string, checkOrderOutput, error) {
		features := src.AccountFeatures()
		account := domain.AccountID(strings.TrimSpace(in.Account))
		// A composition that fixes one account resolves an omitted one in the
		// Source.
		if account == "" && features.Accounts() {
			return "", checkOrderOutput{}, fmt.Errorf("account is required")
		}
		probe := domain.OrderProbe{
			Account:     account,
			BaseAsset:   strings.TrimSpace(in.BaseAsset),
			QuoteAsset:  strings.TrimSpace(in.QuoteAsset),
			Side:        domain.OrderSide(strings.TrimSpace(in.Side)),
			AmountKind:  domain.OrderAmountKind(strings.TrimSpace(in.AmountKind)),
			AmountValue: strings.TrimSpace(in.AmountValue),
			Price:       strings.TrimSpace(in.Price),
		}
		result, err := src.CheckOrder(ctx, probe)
		if err != nil {
			return "", checkOrderOutput{}, toolError(err)
		}
		// The Source checked the composition's one account when none was named.
		account = features.AccountFor(account)
		out := toCheckOrderOutput(result)
		summary := fmt.Sprintf("check %s: pass", account)
		if !result.Passed {
			summary = fmt.Sprintf("check %s: reject - %d reason(s)", account, len(result.Rejects))
		}
		return summary, out, nil
	}
}

func setMarketDataInstrumentHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[setMarketDataInstrumentInput, setMarketDataInstrumentOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in setMarketDataInstrumentInput,
	) (string, setMarketDataInstrumentOutput, error) {
		instanceID := strings.TrimSpace(in.InstanceExternalID)
		externalSymbol := strings.TrimSpace(in.ExternalSymbol)
		if instanceID == "" {
			return "", setMarketDataInstrumentOutput{}, fmt.Errorf("id is required")
		}
		if externalSymbol == "" {
			return "", setMarketDataInstrumentOutput{}, fmt.Errorf("externalSymbol is required")
		}
		if err := src.SetMarketDataInstrumentEnabled(
			ctx, instanceID, externalSymbol, in.Enabled,
		); err != nil {
			return "", setMarketDataInstrumentOutput{}, toolError(err)
		}
		out := setMarketDataInstrumentOutput{
			InstanceExternalID: instanceID,
			ExternalSymbol:     externalSymbol,
			Enabled:            in.Enabled,
		}
		state := "disabled"
		if in.Enabled {
			state = "enabled"
		}
		return fmt.Sprintf(
			"market-data instrument %s/%s %s",
			instanceID,
			externalSymbol,
			state,
		), out, nil
	}
}

func submitOrderHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[submitOrderInput, submitOrderOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in submitOrderInput,
	) (string, submitOrderOutput, error) {
		// A composition that fixes one account resolves an omitted one in the
		// Source. The missing-account policy belongs to an account the caller
		// named, so only then is it parsed, as over REST.
		account := domain.AccountID(strings.TrimSpace(in.Account))
		if account == "" && src.AccountFeatures().Accounts() {
			return "", submitOrderOutput{}, fmt.Errorf("account is required")
		}
		var missing domain.MissingAccountPolicy
		if account != "" {
			var err error
			missing, err = domain.ParseMissingAccountPolicy(
				strings.TrimSpace(in.MissingAccount),
			)
			if err != nil {
				return "", submitOrderOutput{}, err
			}
		}
		o := domain.Order{
			Account:     account,
			BaseAsset:   strings.TrimSpace(in.BaseAsset),
			QuoteAsset:  strings.TrimSpace(in.QuoteAsset),
			Side:        domain.OrderSide(strings.TrimSpace(in.Side)),
			AmountKind:  domain.OrderAmountKind(strings.TrimSpace(in.AmountKind)),
			AmountValue: strings.TrimSpace(in.AmountValue),
			Price:       strings.TrimSpace(in.Price),
		}
		if supplied := strings.TrimSpace(in.ExternalID); supplied != "" {
			id, err := domain.ParseExternalID(supplied)
			if err != nil {
				return "", submitOrderOutput{}, fmt.Errorf("invalid id: %s", err)
			}
			o.ExternalID = id
		}
		mode := strings.TrimSpace(in.Mode)
		res, err := src.SubmitOrder(ctx, o, mode, missing)
		if err != nil {
			return "", submitOrderOutput{}, toolError(err)
		}
		out := submitOrderOutput{
			SignedApproval:  res.SignedApproval,
			KeyID:           res.KeyID,
			OrderExternalID: res.OrderExternalID,
			Verdict:         res.Verdict,
			Reasons:         toMCPRejectDTOs(res.Reasons),
		}
		if res.Verdict == "reject" {
			return fmt.Sprintf(
				"order %s rejected - %d reason(s)",
				res.OrderExternalID,
				len(res.Reasons),
			), out, nil
		}
		return fmt.Sprintf(
			"order %s approved - approval envelope issued",
			res.OrderExternalID,
		), out, nil
	}
}

func submitDropCopyOrderHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[submitDropCopyOrderInput, submitDropCopyOrderOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in submitDropCopyOrderInput,
	) (string, submitDropCopyOrderOutput, error) {
		// A composition that fixes one account resolves an omitted one in the
		// Source. The missing-account policy belongs to an account the caller
		// named, so only then is it checked, as over REST.
		account := domain.AccountID(strings.TrimSpace(in.Account))
		if account == "" && src.AccountFeatures().Accounts() {
			return "", submitDropCopyOrderOutput{}, fmt.Errorf("account is required")
		}
		var missing domain.MissingAccountPolicy
		if account != "" {
			missing = domain.MissingAccountPolicy(strings.TrimSpace(in.MissingAccount))
			if err := domain.ValidateDropCopyMissingAccountPolicy(missing); err != nil {
				return "", submitDropCopyOrderOutput{}, err
			}
		}
		o := domain.Order{
			Account:     account,
			BaseAsset:   strings.TrimSpace(in.BaseAsset),
			QuoteAsset:  strings.TrimSpace(in.QuoteAsset),
			Side:        domain.OrderSide(strings.TrimSpace(in.Side)),
			AmountKind:  domain.OrderAmountKind(strings.TrimSpace(in.AmountKind)),
			AmountValue: strings.TrimSpace(in.AmountValue),
			Price:       strings.TrimSpace(in.Price),
		}
		if supplied := strings.TrimSpace(in.ExternalID); supplied != "" {
			id, err := domain.ParseExternalID(supplied)
			if err != nil {
				return "", submitDropCopyOrderOutput{}, fmt.Errorf("invalid id: %s", err)
			}
			o.ExternalID = id
		}
		res, err := src.SubmitDropCopyOrder(ctx, o, missing)
		if err != nil {
			return "", submitDropCopyOrderOutput{}, toolError(err)
		}
		out := submitDropCopyOrderOutput{
			OrderExternalID: res.OrderExternalID,
			Status:          string(res.Status),
		}
		return fmt.Sprintf(
			"drop-copy order %s recorded without approval envelope",
			res.OrderExternalID,
		), out, nil
	}
}

func confirmExecutionHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[confirmExecutionInput, confirmExecutionOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in confirmExecutionInput,
	) (string, confirmExecutionOutput, error) {
		orderExternalID := strings.TrimSpace(in.OrderExternalID)
		if orderExternalID == "" {
			return "", confirmExecutionOutput{}, fmt.Errorf("id is required")
		}
		order, att, err := src.ConfirmExecution(ctx, orderExternalID)
		if err != nil {
			return "", confirmExecutionOutput{}, toolError(err)
		}
		out := confirmExecutionOutput{
			OrderExternalID:   order.ExternalID.String(),
			Status:            string(order.Status),
			SignedAttestation: att.SignedAttestation,
			KeyID:             att.KeyID,
		}
		return fmt.Sprintf(
			"order %s confirmed: %s",
			order.ExternalID.String(),
			order.Status,
		), out, nil
	}
}

func cancelHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[cancelInput, cancelOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in cancelInput,
	) (string, cancelOutput, error) {
		orderExternalID := strings.TrimSpace(in.OrderExternalID)
		if orderExternalID == "" {
			return "", cancelOutput{}, fmt.Errorf("id is required")
		}
		order, att, err := src.CancelOrder(
			ctx,
			orderExternalID,
			in.LeavesQuantity,
			strings.TrimSpace(in.Reason),
		)
		if err != nil {
			return "", cancelOutput{}, toolError(err)
		}
		out := cancelOutput{
			OrderExternalID:   order.ExternalID.String(),
			Status:            string(order.Status),
			SignedAttestation: att.SignedAttestation,
			KeyID:             att.KeyID,
		}
		return fmt.Sprintf(
			"order %s cancelled: %s",
			order.ExternalID.String(),
			order.Status,
		), out, nil
	}
}

func toHealthOutput(status frameworkmcp.Status) healthOutput {
	n := status.Node
	return healthOutput{
		Node: healthNode{
			Engine: healthEngine{
				Version:      n.Engine.Version,
				BuildProfile: n.Engine.BuildProfile,
				Running:      n.Engine.Running,
			},
			Store: healthStore{
				Path:          n.Store.Path,
				SchemaVersion: n.Store.SchemaVersion,
				Reachable:     n.Store.Reachable,
			},
		},
		Healthy: status.Healthy,
	}
}

func summarizeHealth(out healthOutput) string {
	if out.Healthy {
		return "officer healthy"
	}
	return "officer degraded"
}

type venueOrderDTO struct {
	Connection    string         `json:"connection"`
	VenueAccount  string         `json:"venueAccount"`
	Route         map[string]any `json:"route"`
	ClientOrderID string         `json:"clientOrderId"`
	VenueOrderID  string         `json:"venueOrderId,omitempty"`
	SendAttempted bool           `json:"sendAttempted"`
	CreatedAt     string         `json:"createdAt"`
}

func toVenueOrderDTO(v domain.VenueOrder) (venueOrderDTO, error) {
	if err := domain.ValidateTradingRoute(v.Route); err != nil {
		return venueOrderDTO{}, fmt.Errorf("stored venue order route is invalid")
	}
	var route map[string]any
	decoder := json.NewDecoder(strings.NewReader(v.Route))
	decoder.UseNumber()
	if err := decoder.Decode(&route); err != nil {
		return venueOrderDTO{}, fmt.Errorf("stored venue order route is invalid")
	}
	return venueOrderDTO{
		Connection: v.Connection.String(), VenueAccount: v.VenueAccount,
		Route: route, ClientOrderID: v.ClientOrderID,
		VenueOrderID: v.VenueOrderID, SendAttempted: v.SendAttempted,
		CreatedAt: v.CreatedAt.Format(time.RFC3339Nano),
	}, nil
}

type tradingConnectionOutput struct {
	Connection httpx.TradingConnectionDTO `json:"connection"`
}
type tradingConnectionInput struct {
	Connection string `json:"connection" jsonschema:"Trading connection id"`
}
type createTradingConnectionInput struct {
	ID          string             `json:"id,omitempty" jsonschema:"Optional connection id; omitted lets the store assign one"`
	Provider    string             `json:"provider" jsonschema:"Trading provider type; e.g. alpaca"`
	Label       string             `json:"label" jsonschema:"Display label for the trading connection"`
	Mode        domain.TradingMode `json:"mode" jsonschema:"paper or live"`
	Credentials map[string]any     `json:"credentials" jsonschema:"Write-only provider credential object; Alpaca uses {apiKey, apiSecret}"`
	Enabled     *bool              `json:"enabled" jsonschema:"Required: whether the trading connection is enabled"`
}
type updateTradingConnectionInput struct {
	Connection  string         `json:"connection" jsonschema:"Trading connection id"`
	Label       *string        `json:"label,omitempty" jsonschema:"Optional display label; omitted keeps the stored value"`
	Credentials map[string]any `json:"credentials,omitempty" jsonschema:"Optional write-only provider credential object; Alpaca uses {apiKey, apiSecret}; omitted keeps the stored value"`
}

func (in *updateTradingConnectionInput) UnmarshalJSON(data []byte) error {
	var raw struct {
		Connection  string          `json:"connection"`
		Label       json.RawMessage `json:"label"`
		Credentials json.RawMessage `json:"credentials"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	*in = updateTradingConnectionInput{Connection: raw.Connection}
	if len(raw.Label) != 0 {
		var label string
		if err := json.Unmarshal(raw.Label, &label); err != nil {
			return fmt.Errorf("label must be a string")
		}
		in.Label = &label
	}
	if len(raw.Credentials) != 0 {
		if err := json.Unmarshal(raw.Credentials, &in.Credentials); err != nil || in.Credentials == nil {
			return fmt.Errorf("credentials must be a JSON object")
		}
	}
	return nil
}

type setTradingConnectionEnabledInput struct {
	Connection string `json:"connection" jsonschema:"Trading connection id"`
	Enabled    *bool  `json:"enabled" jsonschema:"Required: whether the trading connection is enabled"`
}
type checkTradingConnectionOutput struct {
	VenueIdentity string `json:"venueIdentity"`
	VenueStatus   string `json:"venueStatus"`
}
type setTradingInstrumentInput struct {
	Connection     string `json:"connection" jsonschema:"Trading connection id"`
	ExternalSymbol string `json:"externalSymbol" jsonschema:"Provider's venue symbol"`
	BaseAsset      string `json:"baseAsset" jsonschema:"Officer asset being bought or sold"`
	QuoteAsset     string `json:"quoteAsset" jsonschema:"Officer asset used for pricing"`
	Enabled        *bool  `json:"enabled" jsonschema:"Required: whether the instrument mapping is enabled"`
}
type tradingSymbolInput struct {
	Connection     string `json:"connection" jsonschema:"Trading connection id"`
	ExternalSymbol string `json:"externalSymbol" jsonschema:"Provider's venue symbol"`
}
type verifyTradingSymbolOutput struct {
	Supported bool   `json:"supported"`
	Exists    bool   `json:"exists"`
	Tradable  bool   `json:"tradable"`
	Details   string `json:"details,omitempty"`
}
type searchTradingSymbolsInput struct {
	Connection string `json:"connection" jsonschema:"Trading connection id"`
	Query      string `json:"query" jsonschema:"Venue symbol search query"`
}
type tradingSymbolMatchDTO struct {
	Symbol   string `json:"symbol"`
	Name     string `json:"name,omitempty"`
	Class    string `json:"class,omitempty"`
	Exchange string `json:"exchange,omitempty"`
	Tradable bool   `json:"tradable"`
}
type searchTradingSymbolsOutput struct {
	Supported bool                    `json:"supported"`
	Matches   []tradingSymbolMatchDTO `json:"matches"`
}
type grantTradingAccessInput struct {
	Connection   string `json:"connection" jsonschema:"Trading connection id"`
	Account      string `json:"account,omitempty" jsonschema:"Officer account code; set exactly one of account or accountGroup"`
	AccountGroup string `json:"accountGroup,omitempty" jsonschema:"Account group code; set exactly one of account or accountGroup"`
	VenueAccount string `json:"venueAccount,omitempty" jsonschema:"Optional provider venue account id"`
}
type revokeTradingAccessInput struct {
	Connection   string `json:"connection" jsonschema:"Trading connection id"`
	Account      string `json:"account,omitempty" jsonschema:"Officer account code; set exactly one of account or accountGroup"`
	AccountGroup string `json:"accountGroup,omitempty" jsonschema:"Account group code; set exactly one of account or accountGroup"`
}
type tradingAccountInput struct {
	Connection string `json:"connection" jsonschema:"Trading connection id"`
	Account    string `json:"account" jsonschema:"Officer account code"`
}
type listTradingDestinationsInput struct {
	Account string             `json:"account" jsonschema:"Officer account code"`
	Mode    domain.TradingMode `json:"mode,omitempty" jsonschema:"Optional paper or live filter; omitted lists every mode"`
}
type listTradingDestinationsOutput struct {
	Destinations []httpx.TradingDestinationDTO `json:"destinations"`
}

// toolError reports err, an error from the Source, as "<code>: <message>" with
// the code and message REST answers for it. REST returns the candidates of an
// ambiguous destination and the dependents blocking a delete as members; the
// text lists them instead, each candidate as "<connection> (<label>)" and each
// dependent as "<kind> (<count>)".
func toolError(err error) error {
	public := httpx.DescribeErr(err)
	message := public.Message
	var ambiguous *backend.DestinationAmbiguousError
	if public.Code == "destination_ambiguous" && errors.As(err, &ambiguous) {
		for _, c := range ambiguous.Candidates {
			message += fmt.Sprintf("; %s (%s)", c.Connection.String(), c.Label)
		}
	}
	var dependents domain.HasDependentsError
	if public.Code == "has_dependents" && errors.As(err, &dependents) {
		for _, d := range dependents.Dependents {
			message += fmt.Sprintf("; %s (%d)", d.Kind, d.Count)
		}
	}
	return fmt.Errorf("%s: %s", public.Code, message)
}

func tradingConnectionSnapshot(ctx context.Context, src frameworkmcp.Source, id string) (httpx.TradingConnectionDTO, error) {
	status, err := src.GetTrading(ctx)
	if err != nil {
		return httpx.TradingConnectionDTO{}, err
	}
	for _, c := range status.Connections {
		if c.ExternalID.String() == id {
			return httpx.ToTradingConnectionDTO(c), nil
		}
	}
	return httpx.TradingConnectionDTO{}, fmt.Errorf("trading connection is missing from the snapshot")
}

func getTradingHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[struct{}, httpx.TradingDTO] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in struct{}) (string, httpx.TradingDTO, error) {
		status, err := src.GetTrading(ctx)
		if err != nil {
			return "", httpx.TradingDTO{}, toolError(err)
		}
		return "trading connections", httpx.ToTradingDTO(status), nil
	}
}

func getCredentialStorageHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[struct{}, httpx.CredentialStorageDTO] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, _ struct{}) (string, httpx.CredentialStorageDTO, error) {
		storage, err := src.CredentialStorage(ctx)
		if err != nil {
			return "", httpx.CredentialStorageDTO{}, toolError(err)
		}
		return "credential storage", httpx.ToCredentialStorageDTO(storage), nil
	}
}
func createTradingConnectionHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[createTradingConnectionInput, tradingConnectionOutput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in createTradingConnectionInput) (string, tradingConnectionOutput, error) {
		if strings.TrimSpace(in.Provider) == "" {
			return "", tradingConnectionOutput{}, fmt.Errorf("provider is required")
		}
		if strings.TrimSpace(in.Label) == "" {
			return "", tradingConnectionOutput{}, fmt.Errorf("label is required")
		}
		if strings.TrimSpace(string(in.Mode)) == "" {
			return "", tradingConnectionOutput{}, fmt.Errorf("mode is required")
		}

		if !domain.TradingModeSupported(in.Mode) {
			return "", tradingConnectionOutput{}, fmt.Errorf("mode must be paper or live")
		}
		if in.Credentials == nil {
			return "", tradingConnectionOutput{}, fmt.Errorf("credentials is required")
		}
		if in.Enabled == nil {
			return "", tradingConnectionOutput{}, fmt.Errorf("enabled is required")
		}
		credentials, err := json.Marshal(in.Credentials)
		if err != nil {
			return "", tradingConnectionOutput{}, fmt.Errorf("credentials must be a JSON object")
		}
		connection := domain.TradingConnection{ExternalID: domain.ExternalID(in.ID), Provider: in.Provider, Label: in.Label, Mode: in.Mode, Credentials: string(credentials), Enabled: *in.Enabled}
		created, err := src.CreateTradingConnection(ctx, connection)
		if err != nil {
			return "", tradingConnectionOutput{}, toolError(err)
		}
		snapshot, err := tradingConnectionSnapshot(ctx, src, created.ExternalID.String())
		if err != nil {
			return "", tradingConnectionOutput{}, toolError(err)
		}
		return "trading connection created", tradingConnectionOutput{Connection: snapshot}, nil
	}
}
func updateTradingConnectionHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[updateTradingConnectionInput, tradingConnectionOutput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in updateTradingConnectionInput) (string, tradingConnectionOutput, error) {
		connection := strings.TrimSpace(in.Connection)
		if connection == "" {
			return "", tradingConnectionOutput{}, fmt.Errorf("connection is required")
		}

		label := ""
		if in.Label != nil {
			label = *in.Label
			if strings.TrimSpace(label) == "" {
				return "", tradingConnectionOutput{}, fmt.Errorf("label is required")
			}
		}
		credentials := ""
		if in.Credentials != nil {
			raw, err := json.Marshal(in.Credentials)
			if err != nil {
				return "", tradingConnectionOutput{}, fmt.Errorf("credentials must be a JSON object")
			}
			credentials = string(raw)
		}
		if err := src.UpdateTradingConnectionSettings(ctx, connection, label, credentials); err != nil {
			return "", tradingConnectionOutput{}, toolError(err)
		}
		snapshot, err := tradingConnectionSnapshot(ctx, src, connection)
		if err != nil {
			return "", tradingConnectionOutput{}, toolError(err)
		}
		return "trading connection updated", tradingConnectionOutput{Connection: snapshot}, nil
	}
}
func checkTradingConnectionHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[tradingConnectionInput, checkTradingConnectionOutput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in tradingConnectionInput) (string, checkTradingConnectionOutput, error) {
		if strings.TrimSpace(in.Connection) == "" {
			return "", checkTradingConnectionOutput{}, fmt.Errorf("connection is required")
		}

		info, err := src.CheckTradingConnection(ctx, in.Connection)
		if err != nil {
			return "", checkTradingConnectionOutput{}, toolError(err)
		}
		return "trading connection checked", checkTradingConnectionOutput{VenueIdentity: info.Identity, VenueStatus: info.Status}, nil
	}
}
func setTradingConnectionEnabledHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[setTradingConnectionEnabledInput, setTradingConnectionEnabledInput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in setTradingConnectionEnabledInput) (string, setTradingConnectionEnabledInput, error) {
		if strings.TrimSpace(in.Connection) == "" {
			return "", setTradingConnectionEnabledInput{}, fmt.Errorf("connection is required")
		}
		if in.Enabled == nil {
			return "", setTradingConnectionEnabledInput{}, fmt.Errorf("enabled is required")
		}
		if err := src.SetTradingConnectionEnabled(ctx, in.Connection, *in.Enabled); err != nil {
			return "", setTradingConnectionEnabledInput{}, toolError(err)
		}
		return "set trading connection enabled", in, nil
	}
}
func setTradingInstrumentHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[setTradingInstrumentInput, setTradingInstrumentInput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in setTradingInstrumentInput) (string, setTradingInstrumentInput, error) {
		if strings.TrimSpace(in.Connection) == "" {
			return "", setTradingInstrumentInput{}, fmt.Errorf("connection is required")
		}
		if strings.TrimSpace(in.ExternalSymbol) == "" {
			return "", setTradingInstrumentInput{}, fmt.Errorf("externalSymbol is required")
		}
		if strings.TrimSpace(in.BaseAsset) == "" {
			return "", setTradingInstrumentInput{}, fmt.Errorf("baseAsset is required")
		}
		if strings.TrimSpace(in.QuoteAsset) == "" {
			return "", setTradingInstrumentInput{}, fmt.Errorf("quoteAsset is required")
		}
		if in.Enabled == nil {
			return "", setTradingInstrumentInput{}, fmt.Errorf("enabled is required")
		}
		if err := src.UpsertTradingInstrument(ctx, domain.TradingInstrument{Connection: domain.ExternalID(in.Connection), ExternalSymbol: in.ExternalSymbol, BaseAsset: in.BaseAsset, QuoteAsset: in.QuoteAsset, Enabled: *in.Enabled}); err != nil {
			return "", setTradingInstrumentInput{}, toolError(err)
		}
		return "set trading instrument", in, nil
	}
}
func deleteTradingInstrumentHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[tradingSymbolInput, tradingSymbolInput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in tradingSymbolInput) (string, tradingSymbolInput, error) {
		if strings.TrimSpace(in.Connection) == "" {
			return "", tradingSymbolInput{}, fmt.Errorf("connection is required")
		}
		if strings.TrimSpace(in.ExternalSymbol) == "" {
			return "", tradingSymbolInput{}, fmt.Errorf("externalSymbol is required")
		}
		if err := src.DeleteTradingInstrument(ctx, in.Connection, in.ExternalSymbol); err != nil {
			return "", tradingSymbolInput{}, toolError(err)
		}
		return "delete trading instrument", in, nil
	}
}
func grantTradingAccessHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[grantTradingAccessInput, grantTradingAccessInput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in grantTradingAccessInput) (string, grantTradingAccessInput, error) {
		if strings.TrimSpace(in.Connection) == "" {
			return "", grantTradingAccessInput{}, fmt.Errorf("connection is required")
		}
		// Both subjects reach the Source, whose validation refuses them; a
		// second rule here would drift from it, as in the REST handler. When
		// the composition fixes one account, an omitted subject reaches the
		// Source, whose account scope supplies that account.
		if src.AccountFeatures().Accounts() &&
			strings.TrimSpace(in.Account) == "" && strings.TrimSpace(in.AccountGroup) == "" {
			return "", grantTradingAccessInput{}, fmt.Errorf("account or accountGroup is required")
		}
		if err := src.PutTradingAccess(ctx, domain.TradingAccess{Connection: domain.ExternalID(in.Connection), Account: domain.AccountID(in.Account), AccountGroup: in.AccountGroup, VenueAccount: in.VenueAccount}); err != nil {
			return "", grantTradingAccessInput{}, toolError(err)
		}
		return "grant trading access", in, nil
	}
}
func revokeTradingAccessHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[revokeTradingAccessInput, revokeTradingAccessInput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in revokeTradingAccessInput) (string, revokeTradingAccessInput, error) {
		if strings.TrimSpace(in.Connection) == "" {
			return "", revokeTradingAccessInput{}, fmt.Errorf("connection is required")
		}
		// Both subjects reach the Source, whose validation refuses them; a
		// second rule here would drift from it, as in the REST handler. When
		// the composition fixes one account, an omitted subject reaches the
		// Source, whose account scope supplies that account.
		if src.AccountFeatures().Accounts() &&
			strings.TrimSpace(in.Account) == "" && strings.TrimSpace(in.AccountGroup) == "" {
			return "", revokeTradingAccessInput{}, fmt.Errorf("account or accountGroup is required")
		}
		if err := src.RemoveTradingAccess(ctx, domain.ExternalID(in.Connection), domain.AccountID(in.Account), in.AccountGroup); err != nil {
			return "", revokeTradingAccessInput{}, toolError(err)
		}
		return "revoke trading access", in, nil
	}
}
func setTradingFeeAccountHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[tradingAccountInput, tradingAccountInput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in tradingAccountInput) (string, tradingAccountInput, error) {
		if strings.TrimSpace(in.Connection) == "" {
			return "", tradingAccountInput{}, fmt.Errorf("connection is required")
		}
		features := src.AccountFeatures()
		if features.Accounts() && strings.TrimSpace(in.Account) == "" {
			return "", tradingAccountInput{}, fmt.Errorf("account is required")
		}
		// The Source reads an empty fee account as a clear, so an omitted one
		// is named here as the composition's one account, as over REST.
		account := features.AccountFor(domain.AccountID(in.Account))
		if err := src.SetTradingFeeAccount(ctx, domain.ExternalID(in.Connection), account); err != nil {
			return "", tradingAccountInput{}, toolError(err)
		}
		in.Account = string(account)
		return "set trading fee account", in, nil
	}
}
func clearTradingFeeAccountHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[tradingConnectionInput, tradingConnectionInput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in tradingConnectionInput) (string, tradingConnectionInput, error) {
		if strings.TrimSpace(in.Connection) == "" {
			return "", tradingConnectionInput{}, fmt.Errorf("connection is required")
		}
		if err := src.SetTradingFeeAccount(ctx, domain.ExternalID(in.Connection), ""); err != nil {
			return "", tradingConnectionInput{}, toolError(err)
		}
		return "clear trading fee account", in, nil
	}
}
func verifyTradingSymbolHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[tradingSymbolInput, verifyTradingSymbolOutput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in tradingSymbolInput) (string, verifyTradingSymbolOutput, error) {
		if strings.TrimSpace(in.Connection) == "" {
			return "", verifyTradingSymbolOutput{}, fmt.Errorf("connection is required")
		}
		if strings.TrimSpace(in.ExternalSymbol) == "" {
			return "", verifyTradingSymbolOutput{}, fmt.Errorf("externalSymbol is required")
		}

		result, err := src.VerifyTradingSymbol(ctx, in.Connection, in.ExternalSymbol)
		if err != nil {
			return "", verifyTradingSymbolOutput{}, toolError(err)
		}
		return "trading symbol verified", verifyTradingSymbolOutput{Supported: result.Supported, Exists: result.Exists, Tradable: result.Tradable, Details: result.Details}, nil
	}
}
func searchTradingSymbolsHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[searchTradingSymbolsInput, searchTradingSymbolsOutput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in searchTradingSymbolsInput) (string, searchTradingSymbolsOutput, error) {
		if strings.TrimSpace(in.Connection) == "" {
			return "", searchTradingSymbolsOutput{}, fmt.Errorf("connection is required")
		}
		if strings.TrimSpace(in.Query) == "" {
			return "", searchTradingSymbolsOutput{}, fmt.Errorf("query is required")
		}

		result, err := src.SearchTradingSymbols(ctx, in.Connection, in.Query)
		if err != nil {
			return "", searchTradingSymbolsOutput{}, toolError(err)
		}
		out := searchTradingSymbolsOutput{Supported: result.Supported, Matches: make([]tradingSymbolMatchDTO, 0, len(result.Matches))}
		for _, m := range result.Matches {
			out.Matches = append(out.Matches, tradingSymbolMatchDTO{Symbol: m.Symbol, Name: m.Name, Class: m.Class, Exchange: m.Exchange, Tradable: m.Tradable})
		}
		return "trading symbols", out, nil
	}
}
func listTradingDestinationsHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[listTradingDestinationsInput, listTradingDestinationsOutput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in listTradingDestinationsInput) (string, listTradingDestinationsOutput, error) {
		// A composition that fixes one account resolves an omitted one in the
		// Source.
		if strings.TrimSpace(in.Account) == "" && src.AccountFeatures().Accounts() {
			return "", listTradingDestinationsOutput{}, fmt.Errorf("account is required")
		}

		if in.Mode != "" && !domain.TradingModeSupported(in.Mode) {
			return "", listTradingDestinationsOutput{}, fmt.Errorf("mode must be paper or live")
		}
		destinations, err := src.ListTradingDestinations(ctx, domain.AccountID(in.Account), in.Mode)
		if err != nil {
			return "", listTradingDestinationsOutput{}, toolError(err)
		}
		out := listTradingDestinationsOutput{Destinations: httpx.ToTradingDestinationDTOs(destinations)}
		return "trading destinations", out, nil
	}
}

type sendPaperOrderInput struct {
	ExternalID  string               `json:"id,omitempty" jsonschema:"Optional unique order id; omitted lets the store assign one"`
	Account     string               `json:"account" jsonschema:"Officer account code"`
	BaseAsset   string               `json:"baseAsset" jsonschema:"Asset being bought or sold"`
	QuoteAsset  string               `json:"quoteAsset" jsonschema:"Asset used for pricing"`
	Side        string               `json:"side" jsonschema:"buy or sell"`
	AmountKind  string               `json:"amountKind" jsonschema:"quantity or volume"`
	AmountValue string               `json:"amountValue" jsonschema:"Order size as an exact decimal string"`
	Price       string               `json:"price,omitempty" jsonschema:"Limit price as an exact decimal string; omit for market"`
	Destination sendDestinationInput `json:"destination" jsonschema:"Paper venue destination with an explicit route"`
}

type sendDestinationInput struct {
	Connection   string         `json:"connection,omitempty" jsonschema:"Connection id; omit only when one enabled paper destination exists"`
	VenueAccount string         `json:"venueAccount,omitempty" jsonschema:"Venue account; omitted uses the binding, supplied must match it"`
	Route        map[string]any `json:"route" jsonschema:"Required venue route JSON object; Alpaca requires timeInForce"`
}

type sendPaperOrderOutput struct {
	SignedApproval  string                `json:"signedApproval"`
	KeyID           string                `json:"keyId"`
	OrderExternalID string                `json:"id"`
	Verdict         string                `json:"verdict"`
	Reasons         []checkOrderRejectDTO `json:"reasons,omitempty"`
	VenueSend       *venueSendDTO         `json:"venueSend,omitempty"`
}

type venueSendDTO struct {
	Status     string         `json:"status"`
	Error      string         `json:"error,omitempty"`
	VenueOrder *venueOrderDTO `json:"venueOrder,omitempty"`
}

func sendPaperOrderHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[sendPaperOrderInput, sendPaperOrderOutput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in sendPaperOrderInput) (string, sendPaperOrderOutput, error) {
		if in.Destination.Route == nil {
			return "", sendPaperOrderOutput{}, fmt.Errorf("destination route must be a JSON object")
		}
		route, err := json.Marshal(in.Destination.Route)
		if err != nil {
			return "", sendPaperOrderOutput{}, fmt.Errorf("destination route must be a JSON object")
		}
		o := domain.Order{Account: domain.AccountID(in.Account),
			BaseAsset: in.BaseAsset, QuoteAsset: in.QuoteAsset,
			Side: domain.OrderSide(in.Side), AmountKind: domain.OrderAmountKind(in.AmountKind),
			AmountValue: in.AmountValue, Price: in.Price}
		if in.ExternalID != "" {
			o.ExternalID, err = domain.ParseExternalID(in.ExternalID)
			if err != nil {
				return "", sendPaperOrderOutput{}, err
			}
		}
		dest := backend.SendDestination{VenueAccount: in.Destination.VenueAccount, Route: string(route)}
		if in.Destination.Connection != "" {
			dest.Connection, err = domain.ParseExternalID(in.Destination.Connection)
			if err != nil {
				return "", sendPaperOrderOutput{}, err
			}
		}
		result, err := src.SendPaperOrder(ctx, o, dest)
		if err != nil {
			return "", sendPaperOrderOutput{}, toolError(err)
		}
		approval := result.Approval
		out := sendPaperOrderOutput{
			SignedApproval: approval.SignedApproval, KeyID: approval.KeyID, OrderExternalID: approval.OrderExternalID,
			Verdict: approval.Verdict, Reasons: toMCPRejectDTOs(approval.Reasons),
		}
		if result.Send != nil {
			out.VenueSend = &venueSendDTO{Status: result.Send.Status, Error: result.Send.Error}
			if result.Send.Order != nil {
				link, err := toVenueOrderDTO(*result.Send.Order)
				if err != nil {
					return "", sendPaperOrderOutput{}, toolError(err)
				}
				out.VenueSend.VenueOrder = &link
			}
		}
		outcome := "reject"
		if result.Send != nil {
			outcome = "accept; venue send " + result.Send.Status
			if result.Send.Error != "" {
				outcome += ": " + result.Send.Error
			}
		}
		return fmt.Sprintf("order %s: %s", approval.OrderExternalID, outcome), out, nil
	}
}
