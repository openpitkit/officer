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
	"errors"
	"fmt"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
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
	"and obtain a signed approval token. Mutates engine state and records the " +
	"pre-trade lock; protected and disabled by default."

const submitDropCopyOrderToolName = "submit_drop_copy_order"
const submitDropCopyOrderToolDescription = "Submit a drop-copy order through " +
	"the normal policy pipeline while ignoring policy rejects and existing " +
	"account or group kill-switch blocks. Policies retain their normal state " +
	"changes. The id is optional; when omitted, the store assigns it. No lifecycle " +
	"event is signed. Mutates engine state; protected and disabled by default."

const confirmExecutionToolName = "confirm_execution"
const confirmExecutionToolDescription = "Record confirmation history for a " +
	"previously approved workflow order by presenting its approval token. The " +
	"shortcut is rejected after execution-report activity; protected and disabled " +
	"by default."

const cancelToolName = "cancel"
const cancelToolDescription = "Cancel an untouched workflow order by presenting " +
	"its approval token. Optional caller-reported leavesQuantity is recorded " +
	"verbatim when supplied; the SDK receives the order's own recorded " +
	"reservation remainder. After execution-report activity, submit an explicit " +
	"report. " +
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
		"Submit an order intent through pre-trade and obtain a signed approval token.",
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
		"Cancel an untouched workflow order with its approval token.",
		true,
		true,
		true,
		false,
	), cancelHandler)
	reg.Register(descriptor(
		"get_next_token",
		"Get next token",
		"",
		"Fetch the next approval token for a trade from the registry.",
		true,
		true,
		false,
		false,
	))
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
	Nodes   []healthNode `json:"nodes"`
	Healthy bool         `json:"healthy"`
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
	Order    orderDTO          `json:"order"`
	Approval *orderApprovalDTO `json:"approval"`
	Trades   []tradeDTO        `json:"trades"`
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
	Token           string                `json:"token"`
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
	Token           string `json:"token" jsonschema:"Approval token returned by submit_order"`
}

type confirmExecutionOutput struct {
	OrderExternalID  string `json:"id"`
	Status           string `json:"status"`
	AttestationToken string `json:"attestationToken,omitempty"`
	KeyID            string `json:"keyId,omitempty"`
}

type cancelInput struct {
	OrderExternalID string `json:"id" jsonschema:"Order id returned by submit_order"`
	Token           string `json:"token" jsonschema:"Approval token returned by submit_order"`
	LeavesQuantity  string `json:"leavesQuantity,omitempty" jsonschema:"Optional caller-reported open base quantity recorded verbatim when supplied; the SDK receives the order's own recorded reservation remainder; whitespace is supplied data and is validated by the report contract"`
	Reason          string `json:"reason,omitempty" jsonschema:"Human-readable cancellation reason"`
}

type cancelOutput struct {
	OrderExternalID  string `json:"id"`
	Status           string `json:"status"`
	AttestationToken string `json:"attestationToken,omitempty"`
	KeyID            string `json:"keyId,omitempty"`
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
	Token    string `json:"token"`
	KeyID    string `json:"keyId"`
	Alg      string `json:"alg"`
	Mode     string `json:"mode"`
	IssuedAt string `json:"issuedAt"`
	Signed   bool   `json:"signed"`
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
		Token:    att.Token,
		KeyID:    att.KeyID,
		Alg:      att.Alg,
		Mode:     att.Mode,
		IssuedAt: att.IssuedAt,
		Signed:   att.Alg == "ed25519",
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
			Token:           res.Token,
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
			"order %s approved token issued",
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
			"drop-copy order %s recorded without approval token",
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
		token := strings.TrimSpace(in.Token)
		if token == "" {
			return "", confirmExecutionOutput{}, fmt.Errorf("token is required")
		}
		order, att, err := src.ConfirmExecution(ctx, orderExternalID, token)
		if err != nil {
			return "", confirmExecutionOutput{}, fmt.Errorf("confirm execution failed: %s", err)
		}
		out := confirmExecutionOutput{
			OrderExternalID:  order.ExternalID.String(),
			Status:           string(order.Status),
			AttestationToken: att.Token,
			KeyID:            att.KeyID,
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
		token := strings.TrimSpace(in.Token)
		if token == "" {
			return "", cancelOutput{}, fmt.Errorf("token is required")
		}
		order, att, err := src.CancelOrder(
			ctx,
			orderExternalID,
			token,
			in.LeavesQuantity,
			strings.TrimSpace(in.Reason),
		)
		if err != nil {
			return "", cancelOutput{}, fmt.Errorf("cancel failed: %s", err)
		}
		out := cancelOutput{
			OrderExternalID:  order.ExternalID.String(),
			Status:           string(order.Status),
			AttestationToken: att.Token,
			KeyID:            att.KeyID,
		}
		return fmt.Sprintf(
			"order %s cancelled: %s",
			order.ExternalID.String(),
			order.Status,
		), out, nil
	}
}

func toHealthOutput(status frameworkmcp.Status) healthOutput {
	nodes := make([]healthNode, 0, len(status.Nodes))
	for _, n := range status.Nodes {
		nodes = append(nodes, healthNode{
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
		})
	}
	return healthOutput{Nodes: nodes, Healthy: status.Healthy}
}

func summarizeHealth(out healthOutput) string {
	state := "degraded"
	if out.Healthy {
		state = "healthy"
	}
	return fmt.Sprintf("officer %s: %d node(s)", state, len(out.Nodes))
}

func isNotFound(err error) bool {
	return errors.Is(err, domain.ErrNotFound)
}
