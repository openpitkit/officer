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
	"sort"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

const auditDefaultLimit = 50
const auditMaxLimit = 500

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
	reg.Register(descriptor(
		"report_fill",
		"Report fill",
		"",
		"Report an execution fill to the engine (post-trade).",
		true,
		true,
		false,
		false,
	))
	registerTool(reg, descriptor("send_paper_order", "Send paper order",
		"Check, hold and send an order to a paper venue. Returns the approval and "+
			"acknowledged, refused or unknown send outcome. Mutating, protected and "+
			"disabled by default. send_live_order comes with the live-trading token.",
		"Check, hold and send an order to a paper venue.",
		true, true, true, false), sendPaperOrderHandler)
	registerTool(reg, descriptor("get_trading", "Get trading",
		"Read trading providers and connections. Read-only - credentials are never exposed.",
		"Read trading providers and connections.", false, false, true, true), getTradingHandler)
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
		"Grant an Officer account or an account group access to a trading connection. A group grant needs the connection's fee account on providers with unlinked fees. Mutates Officer control-plane config; protected and disabled by default.",
		"Grant an Officer account or an account group access to a trading connection; a group grant needs the connection's fee account on providers with unlinked fees.", true, true, true, false), grantTradingAccessHandler)
	registerTool(reg, descriptor("revoke_trading_access", "Revoke trading access",
		"Revoke the access of an Officer account or an account group to a trading connection. Mutates Officer control-plane config; protected and disabled by default.",
		"Revoke the access of an Officer account or an account group to a trading connection.", true, true, true, false), revokeTradingAccessHandler)
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
		sdkmcp.AddTool(server, &sdkmcp.Tool{
			Name:        d.Name,
			Description: d.Description,
		}, frameworkmcp.Guard(d, deps, handler(deps.Source)))
	}
	reg.Register(d)
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
		SignedEnvelope: att.Token,
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
			return "", healthOutput{}, fmt.Errorf("officer status unavailable")
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
		if id == "" {
			return "", getAccountStateOutput{}, fmt.Errorf("account is required")
		}
		acc, limits, err := src.GetAccountState(ctx, id)
		if err != nil {
			if isNotFound(err) {
				return "", getAccountStateOutput{}, fmt.Errorf("account %q not found", id)
			}
			// A rejected id is the caller's mistake to correct, not an outage
			// to retry, so the domain message must reach the agent.
			if errors.Is(err, domain.ErrInvalid) {
				return "", getAccountStateOutput{}, err
			}
			return "", getAccountStateOutput{}, fmt.Errorf("get account state failed")
		}
		block, err := accountBlock(ctx, src, acc)
		if err != nil {
			return "", getAccountStateOutput{},
				fmt.Errorf("resolve account block failed")
		}
		out := getAccountStateOutput{
			Account: toAccountDTO(acc, block),
			Limits:  toLimitsDTO(limits),
		}
		summary := fmt.Sprintf(
			"account %s: %d limit(s)", id, limitsCount(limits),
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
			return "", getLimitsOutput{}, fmt.Errorf("list limits failed")
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
			if isNotFound(err) {
				return "", getOrderOutput{}, fmt.Errorf("order %q not found", id)
			}
			return "", getOrderOutput{}, fmt.Errorf("get order failed")
		}
		out := getOrderOutput{
			Order:    toOrderDTO(detail),
			Approval: toOrderApprovalDTO(detail),
			Trades:   toTradeDTOs(detail.Trades),
		}
		if detail.Venue != nil {
			venue, err := toVenueOrderDTO(*detail.Venue)
			if err != nil {
				return "", getOrderOutput{}, err
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
			n = auditDefaultLimit
		}
		if n > auditMaxLimit {
			n = auditMaxLimit
		}
		filter := domain.AuditFilter{
			Account: domain.AccountID(strings.TrimSpace(in.Account)),
			Actions: auditCategoryActions(in.Category),
		}
		rows, err := src.ListAuditFiltered(ctx, filter, n)
		if err != nil {
			return "", getAuditOutput{}, fmt.Errorf("list audit failed")
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
		account := domain.AccountID(strings.TrimSpace(in.Account))
		if account == "" {
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
			return "", checkOrderOutput{}, fmt.Errorf("check order failed")
		}
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
			return "", setMarketDataInstrumentOutput{}, fmt.Errorf("set market-data instrument failed")
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
		account := domain.AccountID(strings.TrimSpace(in.Account))
		if account == "" {
			return "", submitOrderOutput{}, fmt.Errorf("account is required")
		}
		missing, err := domain.ParseMissingAccountPolicy(
			strings.TrimSpace(in.MissingAccount),
		)
		if err != nil {
			return "", submitOrderOutput{}, err
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
		res, err := src.SubmitOrderToken(ctx, o, mode, missing)
		if err != nil {
			return "", submitOrderOutput{}, fmt.Errorf("submit order failed: %s", err)
		}
		out := submitOrderOutput{
			SignedApproval:  res.Token,
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
		account := domain.AccountID(strings.TrimSpace(in.Account))
		if account == "" {
			return "", submitDropCopyOrderOutput{}, fmt.Errorf("account is required")
		}
		missing := domain.MissingAccountPolicy(strings.TrimSpace(in.MissingAccount))
		if err := domain.ValidateDropCopyMissingAccountPolicy(missing); err != nil {
			return "", submitDropCopyOrderOutput{}, err
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
			return "", submitDropCopyOrderOutput{}, fmt.Errorf(
				"submit drop-copy order failed: %w", err,
			)
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
			if errors.Is(err, domain.ErrVenueOrderManaged) {
				return "", confirmExecutionOutput{}, tradingToolError("confirm execution", err, nil)
			}
			return "", confirmExecutionOutput{}, fmt.Errorf("confirm execution failed: %s", err)
		}
		out := confirmExecutionOutput{
			OrderExternalID:   order.ExternalID.String(),
			Status:            string(order.Status),
			SignedAttestation: att.Token,
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
			if errors.Is(err, domain.ErrVenueOrderManaged) {
				return "", cancelOutput{}, tradingToolError("cancel", err, nil)
			}
			return "", cancelOutput{}, fmt.Errorf("cancel failed: %s", err)
		}
		out := cancelOutput{
			OrderExternalID:   order.ExternalID.String(),
			Status:            string(order.Status),
			SignedAttestation: att.Token,
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

type tradingDTO struct {
	Providers         []tradingProviderDTO   `json:"providers"`
	Connections       []tradingConnectionDTO `json:"connections"`
	CredentialsSealed bool                   `json:"credentialsSealed"`
}

type tradingProviderDTO struct {
	Type            string `json:"type"`
	Title           string `json:"title"`
	VenueAccounts   bool   `json:"venueAccounts"`
	VerifiesSymbols bool   `json:"verifiesSymbols"`
	SearchesSymbols bool   `json:"searchesSymbols"`
	UnlinkedFees    bool   `json:"unlinkedFees"`
}

type tradingConnectionDTO struct {
	ID            string                 `json:"id"`
	Provider      string                 `json:"provider"`
	Label         string                 `json:"label"`
	Mode          domain.TradingMode     `json:"mode"`
	Enabled       bool                   `json:"enabled"`
	VenueIdentity string                 `json:"venueIdentity"`
	FeeAccount    string                 `json:"feeAccount"`
	State         string                 `json:"state"`
	Error         string                 `json:"error,omitempty"`
	Secrets       map[string]bool        `json:"secrets"`
	Instruments   []tradingInstrumentDTO `json:"instruments"`
	Access        []tradingAccessDTO     `json:"access"`
}

type tradingInstrumentDTO struct {
	ExternalSymbol string `json:"externalSymbol"`
	BaseAsset      string `json:"baseAsset"`
	QuoteAsset     string `json:"quoteAsset"`
	Enabled        bool   `json:"enabled"`
}

// tradingAccessDTO is one grant of a connection. Exactly one of Account and
// AccountGroup names the subject; Group is the granted account's current group
// membership, empty for a group grant and for an account outside any group.
type tradingAccessDTO struct {
	Account      string `json:"account"`
	AccountGroup string `json:"accountGroup"`
	VenueAccount string `json:"venueAccount"`
	Group        string `json:"group"`
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

func toTradingDTO(status backend.TradingStatus) tradingDTO {
	out := tradingDTO{
		Providers:         make([]tradingProviderDTO, 0, len(status.Providers)),
		Connections:       make([]tradingConnectionDTO, 0, len(status.Connections)),
		CredentialsSealed: status.CredentialsSealed,
	}
	for _, p := range status.Providers {
		out.Providers = append(out.Providers, tradingProviderDTO{
			Type: p.Type, Title: p.Title, VenueAccounts: p.VenueAccounts,
			VerifiesSymbols: p.VerifiesSymbols, SearchesSymbols: p.SearchesSymbols,
			UnlinkedFees: p.UnlinkedFees,
		})
	}
	for _, c := range status.Connections {
		out.Connections = append(out.Connections, toTradingConnectionDTO(c))
	}
	return out
}

func toTradingConnectionDTO(c backend.TradingConnectionStatus) tradingConnectionDTO {
	out := tradingConnectionDTO{
		ID: c.ExternalID.String(), Provider: c.Provider, Label: c.Label,
		Mode: c.Mode, Enabled: c.Enabled, VenueIdentity: c.VenueIdentity,
		FeeAccount: c.FeeAccount.String(), State: c.State,
		Secrets:     make(map[string]bool, len(c.Secrets)),
		Instruments: make([]tradingInstrumentDTO, 0, len(c.Instruments)),
		Access:      make([]tradingAccessDTO, 0, len(c.Access)),
	}
	if c.State == "unavailable" {
		out.Error = c.Unavailable
	}
	for key, present := range c.Secrets {
		out.Secrets[key] = present
	}
	for _, i := range c.Instruments {
		out.Instruments = append(out.Instruments, tradingInstrumentDTO{
			ExternalSymbol: i.ExternalSymbol, BaseAsset: i.BaseAsset,
			QuoteAsset: i.QuoteAsset, Enabled: i.Enabled,
		})
	}
	for _, row := range c.Access {
		out.Access = append(out.Access, tradingAccessDTO{
			Account: row.Access.Account.String(), AccountGroup: row.Access.AccountGroup,
			VenueAccount: row.Access.VenueAccount, Group: row.Group,
		})
	}
	return out
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
	Connection tradingConnectionDTO `json:"connection"`
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

func tradingToolError(operation string, err error, redact func(string) string) error {
	code := ""
	switch {
	case errors.Is(err, domain.ErrLiveTradingUnavailable):
		code = "live_trading_unavailable"
	case errors.Is(err, domain.ErrTradingModeMismatch):
		code = "trading_mode_mismatch"
	case errors.Is(err, domain.ErrFeeAccountRequired):
		code = "fee_account_required"
	case errors.Is(err, domain.ErrVenueOrderManaged):
		code = "venue_order_managed"
	case errors.Is(err, domain.ErrNoDestination):
		code = "no_destination"
	case errors.Is(err, domain.ErrDestinationAmbiguous):
		code = "destination_ambiguous"
	case errors.As(err, new(domain.AssetMissingError)):
		code = "asset_missing"
	}
	message := err.Error()
	var ambiguous *backend.DestinationAmbiguousError
	if errors.As(err, &ambiguous) {
		for _, c := range ambiguous.Candidates {
			message += fmt.Sprintf("; %s (%s)", c.Connection.String(), c.Label)
		}
	}
	if redact != nil {
		message = redact(message)
	}
	if code != "" {
		return fmt.Errorf("%s: %s", code, message)
	}
	return fmt.Errorf("%s failed: %s", operation, message)
}

func tradingConnectionSnapshot(ctx context.Context, src frameworkmcp.Source, id string) (tradingConnectionDTO, error) {
	status, err := src.GetTrading(ctx)
	if err != nil {
		return tradingConnectionDTO{}, err
	}
	for _, c := range status.Connections {
		if c.ExternalID.String() == id {
			return toTradingConnectionDTO(c), nil
		}
	}
	return tradingConnectionDTO{}, fmt.Errorf("trading connection is missing from the snapshot")
}

func tradingCredentialRedactor(credentials map[string]any) func(string) string {
	var values []string
	var collect func(any)
	collect = func(value any) {
		switch v := value.(type) {
		case string:
			// A blank value holds no secret; redacting it would garble the
			// whitespace of every message that names the refused member.
			if strings.TrimSpace(v) != "" {
				values = append(values, v)
				encoded, _ := json.Marshal(v)
				values = append(values, string(encoded[1:len(encoded)-1]))
			}
		case map[string]any:
			for _, child := range v {
				collect(child)
			}
		case []any:
			for _, child := range v {
				collect(child)
			}
		}
	}
	collect(credentials)
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	var pairs []string
	for _, value := range values {
		pairs = append(pairs, value, "[REDACTED]")
	}
	return strings.NewReplacer(pairs...).Replace
}

func redactTradingConnection(c tradingConnectionDTO, redact func(string) string) tradingConnectionDTO {
	c.ID = redact(c.ID)
	c.Provider = redact(c.Provider)
	c.Label = redact(c.Label)
	c.VenueIdentity = redact(c.VenueIdentity)
	c.FeeAccount = redact(c.FeeAccount)
	c.State = redact(c.State)
	c.Error = redact(c.Error)
	c.Mode = domain.TradingMode(redact(string(c.Mode)))
	secrets := make(map[string]bool, len(c.Secrets))
	for key, value := range c.Secrets {
		secrets[redact(key)] = value
	}
	c.Secrets = secrets
	for i := range c.Instruments {
		item := &c.Instruments[i]
		item.ExternalSymbol = redact(item.ExternalSymbol)
		item.BaseAsset = redact(item.BaseAsset)
		item.QuoteAsset = redact(item.QuoteAsset)
	}
	for i := range c.Access {
		c.Access[i].Account = redact(c.Access[i].Account)
		c.Access[i].AccountGroup = redact(c.Access[i].AccountGroup)
		c.Access[i].VenueAccount = redact(c.Access[i].VenueAccount)
		c.Access[i].Group = redact(c.Access[i].Group)
	}
	return c
}

func getTradingHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[struct{}, tradingDTO] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in struct{}) (string, tradingDTO, error) {
		status, err := src.GetTrading(ctx)
		if err != nil {
			return "", tradingDTO{}, tradingToolError("get trading", err, nil)
		}
		return "trading connections", toTradingDTO(status), nil
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
		redact := tradingCredentialRedactor(in.Credentials)
		connection := domain.TradingConnection{ExternalID: domain.ExternalID(in.ID), Provider: in.Provider, Label: in.Label, Mode: in.Mode, Credentials: string(credentials), Enabled: *in.Enabled}
		created, err := src.CreateTradingConnection(ctx, connection)
		if err != nil {
			return "", tradingConnectionOutput{}, tradingToolError("create trading connection", err, redact)
		}
		snapshot, err := tradingConnectionSnapshot(ctx, src, created.ExternalID.String())
		if err != nil {
			return "", tradingConnectionOutput{}, tradingToolError("create trading connection", err, redact)
		}
		return "trading connection created", tradingConnectionOutput{Connection: redactTradingConnection(snapshot, redact)}, nil
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
		redact := tradingCredentialRedactor(in.Credentials)
		if err := src.UpdateTradingConnectionSettings(ctx, connection, label, credentials); err != nil {
			return "", tradingConnectionOutput{}, tradingToolError("update trading connection", err, redact)
		}
		snapshot, err := tradingConnectionSnapshot(ctx, src, connection)
		if err != nil {
			return "", tradingConnectionOutput{}, tradingToolError("update trading connection", err, redact)
		}
		return "trading connection updated", tradingConnectionOutput{Connection: redactTradingConnection(snapshot, redact)}, nil
	}
}
func checkTradingConnectionHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[tradingConnectionInput, checkTradingConnectionOutput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in tradingConnectionInput) (string, checkTradingConnectionOutput, error) {
		if strings.TrimSpace(in.Connection) == "" {
			return "", checkTradingConnectionOutput{}, fmt.Errorf("connection is required")
		}

		info, err := src.CheckTradingConnection(ctx, in.Connection)
		if err != nil {
			return "", checkTradingConnectionOutput{}, tradingToolError("check trading connection", err, nil)
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
			return "", setTradingConnectionEnabledInput{}, tradingToolError("set trading connection enabled", err, nil)
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
			return "", setTradingInstrumentInput{}, tradingToolError("set trading instrument", err, nil)
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
			return "", tradingSymbolInput{}, tradingToolError("delete trading instrument", err, nil)
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
		// second rule here would drift from it, as in the REST handler.
		if strings.TrimSpace(in.Account) == "" && strings.TrimSpace(in.AccountGroup) == "" {
			return "", grantTradingAccessInput{}, fmt.Errorf("account or accountGroup is required")
		}
		if err := src.PutTradingAccess(ctx, domain.TradingAccess{Connection: domain.ExternalID(in.Connection), Account: domain.AccountID(in.Account), AccountGroup: in.AccountGroup, VenueAccount: in.VenueAccount}); err != nil {
			return "", grantTradingAccessInput{}, tradingToolError("grant trading access", err, nil)
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
		// second rule here would drift from it, as in the REST handler.
		if strings.TrimSpace(in.Account) == "" && strings.TrimSpace(in.AccountGroup) == "" {
			return "", revokeTradingAccessInput{}, fmt.Errorf("account or accountGroup is required")
		}
		if err := src.RemoveTradingAccess(ctx, domain.ExternalID(in.Connection), domain.AccountID(in.Account), in.AccountGroup); err != nil {
			return "", revokeTradingAccessInput{}, tradingToolError("revoke trading access", err, nil)
		}
		return "revoke trading access", in, nil
	}
}
func setTradingFeeAccountHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[tradingAccountInput, tradingAccountInput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in tradingAccountInput) (string, tradingAccountInput, error) {
		if strings.TrimSpace(in.Connection) == "" {
			return "", tradingAccountInput{}, fmt.Errorf("connection is required")
		}
		if strings.TrimSpace(in.Account) == "" {
			return "", tradingAccountInput{}, fmt.Errorf("account is required")
		}
		if err := src.SetTradingFeeAccount(ctx, domain.ExternalID(in.Connection), domain.AccountID(in.Account)); err != nil {
			return "", tradingAccountInput{}, tradingToolError("set trading fee account", err, nil)
		}
		return "set trading fee account", in, nil
	}
}
func clearTradingFeeAccountHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[tradingConnectionInput, tradingConnectionInput] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in tradingConnectionInput) (string, tradingConnectionInput, error) {
		if strings.TrimSpace(in.Connection) == "" {
			return "", tradingConnectionInput{}, fmt.Errorf("connection is required")
		}
		if err := src.SetTradingFeeAccount(ctx, domain.ExternalID(in.Connection), ""); err != nil {
			return "", tradingConnectionInput{}, tradingToolError("clear trading fee account", err, nil)
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
			return "", verifyTradingSymbolOutput{}, tradingToolError("verify trading symbol", err, nil)
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
			return "", searchTradingSymbolsOutput{}, tradingToolError("search trading symbols", err, nil)
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
		if strings.TrimSpace(in.Account) == "" {
			return "", listTradingDestinationsOutput{}, fmt.Errorf("account is required")
		}

		if in.Mode != "" && !domain.TradingModeSupported(in.Mode) {
			return "", listTradingDestinationsOutput{}, fmt.Errorf("mode must be paper or live")
		}
		destinations, err := src.ListTradingDestinations(ctx, domain.AccountID(in.Account), in.Mode)
		if err != nil {
			return "", listTradingDestinationsOutput{}, tradingToolError("list trading destinations", err, nil)
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
			return "", sendPaperOrderOutput{}, tradingToolError("send paper order", err, nil)
		}
		tok := result.Token
		out := sendPaperOrderOutput{
			SignedApproval: tok.Token, KeyID: tok.KeyID, OrderExternalID: tok.OrderExternalID,
			Verdict: tok.Verdict, Reasons: toMCPRejectDTOs(tok.Reasons),
		}
		if result.Send != nil {
			out.VenueSend = &venueSendDTO{Status: result.Send.Status, Error: result.Send.Error}
			if result.Send.Order != nil {
				link, err := toVenueOrderDTO(*result.Send.Order)
				if err != nil {
					return "", sendPaperOrderOutput{}, err
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
		return fmt.Sprintf("order %s: %s", tok.OrderExternalID, outcome), out, nil
	}
}

func isNotFound(err error) bool {
	return errors.Is(err, domain.ErrNotFound)
}
