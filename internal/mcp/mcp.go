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

// Package mcp is the Model Context Protocol surface of Pit Officer. It is a
// thin adapter over the official Go MCP SDK: it builds a server, registers
// Pit Officer's technical tools, and exposes two entry points - a stdio runner
// for the `mcp` run mode and an http.Handler factory for the streamable-HTTP
// transport used by the `serve` run mode.
//
// All SDK-specific types stay confined to this package, behind the Source
// seam. The AI surface exposes deployment status, account state, risk limits,
// and audit log (read-only tools), plus a small set of operator-gated mutating
// commands (market-data instrument toggle). It carries no secrets, no
// credentials, and no order-flow control.
//
// The MCP SDK (github.com/modelcontextprotocol/go-sdk) is pre-1.0 and pinned
// in go.mod; reconcile this package's calls if a different minor is resolved.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/internal/auth"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/node"
)

// mcpCaller attributes every MCP-originated service call to the MCP surface, so
// reads and actions taken through the AI surface carry the right source. The
// principal is a fixed placeholder until per-client MCP authentication lands.
var mcpCaller = domain.Caller{Source: domain.SourceMCP, Principal: "mcp"}

// serverName is the MCP server name advertised to clients.
const serverName = "pit-officer"

// auditDefaultLimit is the default number of audit rows returned when the
// caller omits the limit parameter.
const auditDefaultLimit = 50

// auditMaxLimit is the maximum number of audit rows a single call may request.
const auditMaxLimit = 500

// healthToolName is the name of the technical status tool.
const healthToolName = "health"

// healthToolDescription documents the health tool for MCP clients.
const healthToolDescription = "Report Pit Officer deployment health: per-node " +
	"engine version, build profile and liveness, and store reachability, " +
	"schema version and path. Read-only status only - exposes no secrets " +
	"and no order-flow control."

const getAccountStateToolName = "get_account_state"
const getAccountStateToolDescription = "Return account row and its " +
	"account-scoped risk barriers. Read-only - no secrets, no order-flow " +
	"control."

const getLimitsToolName = "get_limits"
const getLimitsToolDescription = "Return risk barriers, optionally filtered " +
	"by account. Read-only - no secrets, no order-flow control."

const getOrderToolName = "get_order"
const getOrderToolDescription = "Return one order addressed by its external id: " +
	"its status, its 1:1 signed approval (when issued), and its fills with their " +
	"display prices. Read-only - no secrets, no order-flow control. The opaque " +
	"reservation lock is never exposed; only backend-derived display prices are."

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
	"and obtain a signed approval token. Mutates engine state (holds or commits " +
	"funds); protected and disabled by default."

const confirmExecutionToolName = "confirm_execution"
const confirmExecutionToolDescription = "Confirm execution (commit) of a " +
	"previously approved hold-mode order, addressed by its external id, by " +
	"presenting the approval token. Mutates engine state; protected and disabled " +
	"by default."

const cancelToolName = "cancel"
const cancelToolDescription = "Cancel / revoke a pending approval token or " +
	"held reservation by presenting the token. Releases held funds. " +
	"Mutates engine state; protected and disabled by default."

// Source is the Pit Officer-facing seam the MCP surface reads from. It is
// satisfied by *backend.Service via an adapter in main.go; keeping it an
// interface keeps this package free of a dependency on the concrete
// control-plane package and makes the surface trivially fakeable.
//
// The mcp package may import internal/domain but must not import
// internal/backend.
type Source interface {
	// Status returns the aggregate health of every node in the deployment.
	Status(ctx context.Context) (Status, error)
	// GetAccountState returns the account row (a dictionary entity addressed by
	// its code) and its account-scoped typed barriers.
	GetAccountState(ctx context.Context, id domain.AccountID) (
		domain.Account, node.AccountLimits, error)
	// ListLimits returns the typed barriers filtered by account code; an empty
	// account returns all barriers.
	ListLimits(ctx context.Context, account domain.AccountID) (
		node.AccountLimits, error)
	// ListAudit returns the most recent n audit rows.
	ListAudit(ctx context.Context, n int) ([]domain.AuditRow, error)
	// ListAuditFiltered returns the most recent n audit rows matching the filter.
	ListAuditFiltered(
		ctx context.Context, filter domain.AuditFilter, n int,
	) ([]domain.AuditRow, error)
	// CheckOrder runs a non-mutating pre-trade dry-run for probe, returning
	// whether the order would pass plus the would-be lock or block. It mutates
	// no state.
	CheckOrder(ctx context.Context, probe domain.OrderProbe) (domain.CheckResult, error)
	// GetOrder returns the order addressed by its opaque external-id handle,
	// together with its 1:1 signed approval (when issued), events and trades. It
	// maps a missing order onto domain.ErrNotFound.
	GetOrder(ctx context.Context, externalID string) (domain.OrderDetail, error)
	// SetMarketDataInstrumentEnabled toggles one configured market-data
	// instrument in the Officer control-plane store.
	SetMarketDataInstrumentEnabled(
		ctx context.Context, instanceID, externalSymbol string, enabled bool,
	) error
	// CommandEnabled reports whether the operator has the named MCP command
	// enabled in the Pit Officer panel. A false result means the operator has
	// turned the command off; the surface returns a non-error disabled notice
	// rather than running it. An error is a transient access-store failure, on
	// which the surface fails open (treats the command as enabled) so reads are
	// not blocked by a flaky access read.
	CommandEnabled(ctx context.Context, command string) (bool, error)
	// SubmitOrderToken runs the pre-trade pipeline for o in the given mode and,
	// on accept, issues a signed approval token. mode "hold" or "immediate".
	SubmitOrderToken(ctx context.Context, o domain.Order, mode string) (SubmitOrderTokenResult, error)
	// ConfirmExecution verifies the token against the order addressed by its
	// external-id handle and commits the held reservation.
	ConfirmExecution(
		ctx context.Context, orderExternalID, token string, force bool,
	) (domain.Order, error)
	// CancelOrder verifies the token against the order addressed by its
	// external-id handle and rolls back the held reservation.
	CancelOrder(
		ctx context.Context, orderExternalID, token, reason string, force bool,
	) (domain.Order, error)
}

// SubmitOrderTokenResult is the surface-agnostic result of issuing an approval
// token; the MCP adapter maps it from the backend's ApprovalToken.
type SubmitOrderTokenResult struct {
	// Token is the base64url-encoded approval envelope.
	Token string
	// KeyID is the signing key id; empty under eSign-off.
	KeyID string
	// ExpiresAt is when the token (and held reservation) expires.
	ExpiresAt time.Time
	// OrderExternalID is the order's opaque public handle (22-char base64url
	// external id), never a surrogate or engine id.
	OrderExternalID string
}

// VersionSource reports the engine version used as the MCP server version. It
// is satisfied by engine.Engine.
type VersionSource interface {
	// Version returns the engine SDK/runtime version string.
	Version() string
}

// Status mirrors backend.Status for the MCP surface so this package does not
// import the backend package. The caller adapts backend.Status into this shape.
type Status struct {
	// Nodes carries one health record per node.
	Nodes []NodeHealth
	// Healthy reports whether every node is live and reachable.
	Healthy bool
}

// NodeHealth is one node's engine and store health, for the MCP surface.
type NodeHealth struct {
	// Engine is the node's engine health.
	Engine EngineHealth
	// Store is the node's store health.
	Store StoreHealth
}

// EngineHealth is a node's engine health, for the MCP surface.
type EngineHealth struct {
	// Version is the engine SDK/runtime version.
	Version string
	// BuildProfile is the engine build profile.
	BuildProfile string
	// Running reports whether the engine handle is live.
	Running bool
}

// StoreHealth is a node's store health, for the MCP surface.
type StoreHealth struct {
	// Path is the on-disk database location.
	Path string
	// SchemaVersion is the applied schema version.
	SchemaVersion int
	// Reachable reports whether the store responded to its last probe.
	Reachable bool
}

// -- wire DTOs (JSON tags are the wire contract for MCP clients) --

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
	// jsonschema tag is the plain description - no key=value syntax.
	Account string `json:"account,omitempty" jsonschema:"Optional account-code filter; omit to return all barriers"`
}

type getLimitsOutput struct {
	Limits limitsDTO `json:"limits"`
}

type getOrderInput struct {
	OrderExternalID string `json:"orderExternalId" jsonschema:"Order external id (22-char opaque handle) returned by submit_order"`
}

type getOrderOutput struct {
	Order    orderDTO          `json:"order"`
	Approval *orderApprovalDTO `json:"approval"`
	Trades   []tradeDTO        `json:"trades"`
}

type getAuditInput struct {
	// Category selects the audit stream: "control" (default) hides the
	// high-volume trading activity, "trading" shows only order/execution rows,
	// "all" shows everything.
	Category string `json:"category,omitempty" jsonschema:"Audit stream: control (default) | trading | all"`
	// Account narrows the trail to one account; empty matches any.
	Account string `json:"account,omitempty" jsonschema:"Account code filter (optional)"`
	Limit   int    `json:"limit,omitempty" jsonschema:"Max rows to return (default 50 cap 500)"`
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
	WouldBlock         *checkOrderBlockDTO   `json:"wouldBlock"`
	Rejects            []checkOrderRejectDTO `json:"rejects"`
	WouldDisplayPrices []string              `json:"wouldDisplayPrices"`
	Passed             bool                  `json:"passed"`
}

type setMarketDataInstrumentInput struct {
	InstanceExternalID string `json:"instanceExternalId" jsonschema:"Market-data instance external identifier"`
	ExternalSymbol     string `json:"externalSymbol" jsonschema:"Provider-side instrument symbol"`
	Enabled            bool   `json:"enabled" jsonschema:"Whether the instrument should be enabled"`
}

type setMarketDataInstrumentOutput struct {
	InstanceExternalID string `json:"instanceExternalId"`
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
	Mode        string `json:"mode,omitempty" jsonschema:"hold or immediate (default immediate)"`
	// ExternalID is optional. Submit creates the order; when supplied this 22-char
	// opaque handle is used as-is (a duplicate is rejected), and omitting it has
	// the server generate and return one in orderExternalId. Never a surrogate id.
	ExternalID string `json:"externalId,omitempty" jsonschema:"Optional caller-supplied order external id (22-char opaque handle); omit to have the server generate one"`
}

type submitOrderOutput struct {
	Token           string `json:"token"`
	KeyID           string `json:"keyId"`
	ExpiresAt       string `json:"expiresAt"`
	OrderExternalID string `json:"orderExternalId"`
}

type confirmExecutionInput struct {
	OrderExternalID string `json:"orderExternalId" jsonschema:"Order external id returned by submit_order"`
	Token           string `json:"token" jsonschema:"Approval token returned by submit_order"`
	Force           bool   `json:"force,omitempty" jsonschema:"Bypass Officer's safety checks and route the operation straight to the engine"`
}

type confirmExecutionOutput struct {
	OrderExternalID string `json:"orderExternalId"`
	Status          string `json:"status"`
}

type cancelInput struct {
	OrderExternalID string `json:"orderExternalId" jsonschema:"Order external id returned by submit_order"`
	Token           string `json:"token" jsonschema:"Approval token returned by submit_order"`
	Reason          string `json:"reason,omitempty" jsonschema:"Human-readable cancellation reason"`
	Force           bool   `json:"force,omitempty" jsonschema:"Bypass Officer's safety checks and route the operation straight to the engine"`
}

type cancelOutput struct {
	OrderExternalID string `json:"orderExternalId"`
	Status          string `json:"status"`
}

// accountDTO is the wire shape of a single account. An account is a dictionary
// entity: its public handle is the immutable Code, displayed under the mutable
// Title; Group links it to its group by the group's code. No surrogate or
// engine id is ever carried.
type accountDTO struct {
	Code        string `json:"code"`
	Title       string `json:"title"`
	Group       string `json:"group"`
	BlockReason string `json:"blockReason"`
	Blocked     bool   `json:"blocked"`
}

// limitsDTO is the wire shape of the three typed risk-barrier sets returned by
// the account-addressed limit reads. Each slice holds the barriers of one
// policy; an empty slice means the policy carries no matching barrier.
type limitsDTO struct {
	RateLimits      []rateLimitDTO      `json:"rateLimits"`
	OrderSizeLimits []orderSizeLimitDTO `json:"orderSizeLimits"`
	PnlBoundsLimits []pnlBoundsLimitDTO `json:"pnlBoundsLimits"`
}

// rateLimitDTO is the wire shape of one typed rate-limit barrier: at most
// MaxOrders new orders per rolling Window for the addressed scope. Window is a
// Go duration string (e.g. "1s"). Account is the account code; empty unless the
// scope carries it.
type rateLimitDTO struct {
	Scope     string `json:"scope"`
	Account   string `json:"account"`
	Asset     string `json:"asset"`
	Window    string `json:"window"`
	MaxOrders uint64 `json:"maxOrders"`
}

// orderSizeLimitDTO is the wire shape of one typed order-size barrier. Both
// ceilings are exact decimal strings; an unset ceiling is the empty string.
type orderSizeLimitDTO struct {
	Scope       string `json:"scope"`
	Account     string `json:"account"`
	Asset       string `json:"asset"`
	MaxQuantity string `json:"maxQuantity"`
	MaxNotional string `json:"maxNotional"`
}

// pnlBoundsLimitDTO is the wire shape of one typed P&L kill-switch barrier. All
// bounds are exact decimal strings; an unset bound is the empty string.
type pnlBoundsLimitDTO struct {
	Scope      string `json:"scope"`
	Account    string `json:"account"`
	Asset      string `json:"asset"`
	LowerBound string `json:"lowerBound"`
	UpperBound string `json:"upperBound"`
	InitialPnl string `json:"initialPnl"`
}

// auditDTO is the wire shape of a single audit row. It is a machine record: its
// public handle is the opaque external id, never a surrogate id. Account and
// Actor are dictionary codes.
type auditDTO struct {
	At         time.Time `json:"at"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	Account    string    `json:"account"`
	Detail     string    `json:"detail"`
	ExternalID string    `json:"externalId"`
}

// orderDTO is the wire shape of one order record. It is a machine record: its
// public handle is the opaque external id, never a surrogate or engine id. The
// opaque reservation lock blob is never serialized; the order's display prices
// are carried on its trades. All monetary/size values are exact decimal strings.
type orderDTO struct {
	At          time.Time `json:"at"`
	ExternalID  string    `json:"externalId"`
	Account     string    `json:"account"`
	BaseAsset   string    `json:"baseAsset"`
	QuoteAsset  string    `json:"quoteAsset"`
	Side        string    `json:"side"`
	AmountKind  string    `json:"amountKind"`
	AmountValue string    `json:"amountValue"`
	Price       string    `json:"price"`
	Status      string    `json:"status"`
	Source      string    `json:"source"`
}

// orderApprovalDTO is the wire shape of an order's persisted signed approval
// envelope, read back from the order's 1:1 OrderApproval. Token is the exact
// base64url envelope bytes; the rest is the envelope metadata. Signed reports
// whether the envelope carries an Ed25519 signature (alg "ed25519") versus an
// eSign-off envelope (alg "none").
type orderApprovalDTO struct {
	Token     string `json:"token"`
	KeyID     string `json:"keyId"`
	Alg       string `json:"alg"`
	Mode      string `json:"mode"`
	IssuedAt  string `json:"issuedAt"`
	ExpiresAt string `json:"expiresAt"`
	Signed    bool   `json:"signed"`
}

// tradeDTO is the wire shape of one fill ("report") of an order. It is a machine
// record addressed by its opaque external id and linked to its order by the
// order's external id. Price and LockPrice are backend-derived display prices
// (exact decimal strings), not the opaque reservation lock.
type tradeDTO struct {
	At         time.Time `json:"at"`
	ExternalID string    `json:"externalId"`
	Side       string    `json:"side"`
	Quantity   string    `json:"quantity"`
	Price      string    `json:"price"`
	LockPrice  string    `json:"lockPrice"`
}

// checkOrderRejectDTO is the wire shape of one engine pre-trade reject from a
// dry-run (identical to httpapi).
type checkOrderRejectDTO struct {
	Code    string `json:"code"`
	Scope   string `json:"scope"`
	Policy  string `json:"policy"`
	Reason  string `json:"reason"`
	Details string `json:"details"`
}

// checkOrderBlockDTO is the wire shape of the account block a dry-run reports
// the engine would record (identical to httpapi).
type checkOrderBlockDTO struct {
	Account string `json:"account"`
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Details string `json:"details"`
}

// -- mappers --

func toAccountDTO(a domain.Account) accountDTO {
	return accountDTO{
		Code:        string(a.Code),
		Title:       a.Title,
		Group:       a.GroupCode,
		BlockReason: a.BlockReason,
		Blocked:     a.Blocked,
	}
}

// toLimitsDTO maps the three typed barrier sets onto the wire DTO. Each slice is
// allocated non-nil so the JSON encodes empty arrays rather than null.
func toLimitsDTO(l node.AccountLimits) limitsDTO {
	rate := make([]rateLimitDTO, 0, len(l.RateLimits))
	for _, r := range l.RateLimits {
		rate = append(rate, rateLimitDTO{
			Scope:     r.Scope,
			Account:   string(r.Account),
			Asset:     r.Asset,
			Window:    r.Window.String(),
			MaxOrders: r.MaxOrders,
		})
	}
	size := make([]orderSizeLimitDTO, 0, len(l.OrderSizeLimits))
	for _, o := range l.OrderSizeLimits {
		size = append(size, orderSizeLimitDTO{
			Scope:       o.Scope,
			Account:     string(o.Account),
			Asset:       o.Asset,
			MaxQuantity: o.MaxQuantity,
			MaxNotional: o.MaxNotional,
		})
	}
	pnl := make([]pnlBoundsLimitDTO, 0, len(l.PnlBoundsLimits))
	for _, p := range l.PnlBoundsLimits {
		pnl = append(pnl, pnlBoundsLimitDTO{
			Scope:      p.Scope,
			Account:    string(p.Account),
			Asset:      p.Asset,
			LowerBound: p.LowerBound,
			UpperBound: p.UpperBound,
			InitialPnl: p.InitialPnl,
		})
	}
	return limitsDTO{RateLimits: rate, OrderSizeLimits: size, PnlBoundsLimits: pnl}
}

// limitsCount returns the total number of typed barriers across the three sets,
// for the tool's human-readable summary line.
func limitsCount(l node.AccountLimits) int {
	return len(l.RateLimits) + len(l.OrderSizeLimits) + len(l.PnlBoundsLimits)
}

func toAuditDTO(row domain.AuditRow) auditDTO {
	return auditDTO{
		ExternalID: row.ExternalID.String(),
		At:         row.At,
		Actor:      row.Actor,
		Action:     string(row.Action),
		Account:    string(row.Account),
		Detail:     row.Detail,
	}
}

// toOrderDTO maps a domain.Order onto the wire DTO. It addresses the order by
// its external-id handle and never serializes the opaque reservation lock.
func toOrderDTO(o domain.Order) orderDTO {
	return orderDTO{
		At:          o.At,
		ExternalID:  o.ExternalID.String(),
		Account:     string(o.Account),
		BaseAsset:   o.BaseAsset,
		QuoteAsset:  o.QuoteAsset,
		Side:        string(o.Side),
		AmountKind:  string(o.AmountKind),
		AmountValue: o.AmountValue,
		Price:       o.Price,
		Status:      string(o.Status),
		Source:      string(o.Source),
	}
}

// toOrderApprovalDTO maps an order's 1:1 persisted envelope onto the wire DTO,
// or returns nil when the order carries no approval.
func toOrderApprovalDTO(a *domain.OrderApproval) *orderApprovalDTO {
	if a == nil {
		return nil
	}
	return &orderApprovalDTO{
		Token:     a.Token,
		KeyID:     a.KeyID,
		Alg:       a.Alg,
		Mode:      a.Mode,
		IssuedAt:  a.IssuedAt,
		ExpiresAt: a.ExpiresAt,
		Signed:    a.Alg == "ed25519",
	}
}

// toTradeDTOs maps an order's fills onto the wire DTOs, carrying the
// backend-derived display prices (never the opaque lock).
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
		rejects = append(rejects, checkOrderRejectDTO{
			Code:    rej.Code,
			Scope:   rej.Scope,
			Policy:  rej.Policy,
			Reason:  rej.Reason,
			Details: rej.Details,
		})
	}
	prices := r.WouldLockPrices
	if prices == nil {
		prices = []string{}
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
		WouldBlock:         block,
		Rejects:            rejects,
		WouldDisplayPrices: prices,
		Passed:             r.Passed,
	}
}

// -- tool result helpers --

func toolErr[Out any](msg string) *sdkmcp.CallToolResultFor[Out] {
	return &sdkmcp.CallToolResultFor[Out]{
		IsError: true,
		Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: msg},
		},
	}
}

func toolOK[Out any](text string, out Out) *sdkmcp.CallToolResultFor[Out] {
	return &sdkmcp.CallToolResultFor[Out]{
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: text}},
		StructuredContent: out,
	}
}

// toolDisabled returns a NON-error result telling the calling agent the command
// has been turned off in the Pit Officer panel by the operator. It is not an
// IsError result: the call succeeded, the command is simply unavailable, so the
// agent can report back to its orchestrator rather than treating it as a fault.
// The typed Out is left at its zero value; the text message carries the notice.
func toolDisabled[Out any](command string) *sdkmcp.CallToolResultFor[Out] {
	msg := fmt.Sprintf(
		"Command %q is disabled in the Pit Officer panel by the operator. It is "+
			"currently unavailable. Report this to your orchestrator: either adjust "+
			"the task to avoid this command, or ask the operator to enable it in the "+
			"panel.",
		command,
	)
	return &sdkmcp.CallToolResultFor[Out]{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: msg}},
	}
}

// commandGate reports whether handler execution for command should proceed. It
// consults the access source: an explicit false returns a disabled notice (so
// the caller short-circuits with a non-error result); on an access-store error
// it fails open (proceeds) so a transient read failure never blocks a read tool.
// The bool is true when the handler should run; the result is non-nil only when
// it should not.
func commandGate[Out any](
	ctx context.Context, src Source, command string,
) (bool, *sdkmcp.CallToolResultFor[Out]) {
	enabled, err := src.CommandEnabled(ctx, command)
	if err != nil {
		// Fail open: a transient access-store error must not block a read.
		return true, nil
	}
	if !enabled {
		return false, toolDisabled[Out](command)
	}
	return true, nil
}

// commandGateMutating is like commandGate but fails closed on an access-store
// error. Mutating commands must not execute when the gate cannot be checked: the
// safe default is to deny the operation rather than risk an unintended mutation.
func commandGateMutating[Out any](
	ctx context.Context, src Source, command string,
) (bool, *sdkmcp.CallToolResultFor[Out]) {
	enabled, err := src.CommandEnabled(ctx, command)
	if err != nil {
		return false, toolErr[Out]("command access check failed; command not executed")
	}
	if !enabled {
		return false, toolDisabled[Out](command)
	}
	return true, nil
}

// -- NewServer --

// NewServer builds the Pit Officer MCP server: it names the server
// "pit-officer", stamps it with the engine version, and registers all tools.
// The returned *sdkmcp.Server is ready to run over any transport.
func NewServer(src Source, version VersionSource) (*sdkmcp.Server, error) {
	if src == nil {
		return nil, fmt.Errorf("mcp: nil source")
	}
	serverVersion := ""
	if version != nil {
		serverVersion = version.Version()
	}

	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        healthToolName,
		Description: healthToolDescription,
	}, healthHandler(src))

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        getAccountStateToolName,
		Description: getAccountStateToolDescription,
	}, getAccountStateHandler(src))

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        getLimitsToolName,
		Description: getLimitsToolDescription,
	}, getLimitsHandler(src))

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        getOrderToolName,
		Description: getOrderToolDescription,
	}, getOrderHandler(src))

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        getAuditToolName,
		Description: getAuditToolDescription,
	}, getAuditHandler(src))

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        checkOrderToolName,
		Description: checkOrderToolDescription,
	}, checkOrderHandler(src))

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        setMarketDataInstrumentToolName,
		Description: setMarketDataInstrumentToolDescription,
	}, setMarketDataInstrumentHandler(src))

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        submitOrderToolName,
		Description: submitOrderToolDescription,
	}, submitOrderHandler(src))

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        confirmExecutionToolName,
		Description: confirmExecutionToolDescription,
	}, confirmExecutionHandler(src))

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        cancelToolName,
		Description: cancelToolDescription,
	}, cancelHandler(src))

	return server, nil
}

// -- tool handlers --

func healthHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[healthInput],
) (*sdkmcp.CallToolResultFor[healthOutput], error) {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		_ *sdkmcp.CallToolParamsFor[healthInput],
	) (*sdkmcp.CallToolResultFor[healthOutput], error) {
		ctx = auth.ContextWithCaller(ctx, mcpCaller)
		if ok, disabled := commandGate[healthOutput](ctx, src, healthToolName); !ok {
			return disabled, nil
		}
		status, err := src.Status(ctx)
		if err != nil {
			return toolErr[healthOutput]("officer status unavailable"), nil
		}
		out := toHealthOutput(status)
		return toolOK(summarizeHealth(out), out), nil
	}
}

func getAccountStateHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[getAccountStateInput],
) (*sdkmcp.CallToolResultFor[getAccountStateOutput], error) {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		p *sdkmcp.CallToolParamsFor[getAccountStateInput],
	) (*sdkmcp.CallToolResultFor[getAccountStateOutput], error) {
		ctx = auth.ContextWithCaller(ctx, mcpCaller)
		if ok, disabled := commandGate[getAccountStateOutput](ctx, src, getAccountStateToolName); !ok {
			return disabled, nil
		}
		id := domain.AccountID(strings.TrimSpace(p.Arguments.Account))
		if id == "" {
			return toolErr[getAccountStateOutput]("account is required"), nil
		}
		acc, limits, err := src.GetAccountState(ctx, id)
		if err != nil {
			if isNotFound(err) {
				return toolErr[getAccountStateOutput](
					fmt.Sprintf("account %q not found", id)), nil
			}
			return toolErr[getAccountStateOutput]("get account state failed"), nil
		}
		out := getAccountStateOutput{
			Account: toAccountDTO(acc),
			Limits:  toLimitsDTO(limits),
		}
		return toolOK(fmt.Sprintf("account %s: %d limit(s)", id, limitsCount(limits)), out), nil
	}
}

func getLimitsHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[getLimitsInput],
) (*sdkmcp.CallToolResultFor[getLimitsOutput], error) {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		p *sdkmcp.CallToolParamsFor[getLimitsInput],
	) (*sdkmcp.CallToolResultFor[getLimitsOutput], error) {
		ctx = auth.ContextWithCaller(ctx, mcpCaller)
		if ok, disabled := commandGate[getLimitsOutput](ctx, src, getLimitsToolName); !ok {
			return disabled, nil
		}
		account := domain.AccountID(strings.TrimSpace(p.Arguments.Account))
		limits, err := src.ListLimits(ctx, account)
		if err != nil {
			return toolErr[getLimitsOutput]("list limits failed"), nil
		}
		out := getLimitsOutput{Limits: toLimitsDTO(limits)}
		return toolOK(fmt.Sprintf("%d limit(s)", limitsCount(limits)), out), nil
	}
}

func getOrderHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[getOrderInput],
) (*sdkmcp.CallToolResultFor[getOrderOutput], error) {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		p *sdkmcp.CallToolParamsFor[getOrderInput],
	) (*sdkmcp.CallToolResultFor[getOrderOutput], error) {
		ctx = auth.ContextWithCaller(ctx, mcpCaller)
		if ok, disabled := commandGate[getOrderOutput](ctx, src, getOrderToolName); !ok {
			return disabled, nil
		}
		id := strings.TrimSpace(p.Arguments.OrderExternalID)
		if id == "" {
			return toolErr[getOrderOutput]("orderExternalId is required"), nil
		}
		detail, err := src.GetOrder(ctx, id)
		if err != nil {
			if isNotFound(err) {
				return toolErr[getOrderOutput](
					fmt.Sprintf("order %q not found", id)), nil
			}
			return toolErr[getOrderOutput]("get order failed"), nil
		}
		out := getOrderOutput{
			Order:    toOrderDTO(detail.Order),
			Approval: toOrderApprovalDTO(detail.Approval),
			Trades:   toTradeDTOs(detail.Trades),
		}
		return toolOK(
			fmt.Sprintf("order %s: %s (%d fill(s))",
				detail.Order.ExternalID.String(), detail.Order.Status, len(detail.Trades)),
			out,
		), nil
	}
}

// auditCategoryActions resolves the get_audit category argument to an action
// include-set via domain.AuditActionsForCategory. Unknown or empty category
// falls back to the control set so trading noise is hidden unless asked for;
// this tool is LLM-facing and stays lenient rather than erroring.
func auditCategoryActions(category string) []domain.AuditAction {
	actions, known := domain.AuditActionsForCategory(strings.TrimSpace(category))
	if !known {
		return domain.AuditActionsByCategory(domain.AuditCategoryControl)
	}
	return actions
}

func getAuditHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[getAuditInput],
) (*sdkmcp.CallToolResultFor[getAuditOutput], error) {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		p *sdkmcp.CallToolParamsFor[getAuditInput],
	) (*sdkmcp.CallToolResultFor[getAuditOutput], error) {
		ctx = auth.ContextWithCaller(ctx, mcpCaller)
		if ok, disabled := commandGate[getAuditOutput](ctx, src, getAuditToolName); !ok {
			return disabled, nil
		}
		n := p.Arguments.Limit
		if n <= 0 {
			n = auditDefaultLimit
		}
		if n > auditMaxLimit {
			n = auditMaxLimit
		}
		filter := domain.AuditFilter{
			Account: domain.AccountID(strings.TrimSpace(p.Arguments.Account)),
			Actions: auditCategoryActions(p.Arguments.Category),
		}
		rows, err := src.ListAuditFiltered(ctx, filter, n)
		if err != nil {
			return toolErr[getAuditOutput]("list audit failed"), nil
		}
		out := getAuditOutput{Entries: toAuditDTOs(rows)}
		return toolOK(fmt.Sprintf("%d audit entry/entries", len(rows)), out), nil
	}
}

func checkOrderHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[checkOrderInput],
) (*sdkmcp.CallToolResultFor[checkOrderOutput], error) {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		p *sdkmcp.CallToolParamsFor[checkOrderInput],
	) (*sdkmcp.CallToolResultFor[checkOrderOutput], error) {
		ctx = auth.ContextWithCaller(ctx, mcpCaller)
		if ok, disabled := commandGate[checkOrderOutput](ctx, src, checkOrderToolName); !ok {
			return disabled, nil
		}
		account := domain.AccountID(strings.TrimSpace(p.Arguments.Account))
		if account == "" {
			return toolErr[checkOrderOutput]("account is required"), nil
		}
		probe := domain.OrderProbe{
			Account:     account,
			BaseAsset:   strings.TrimSpace(p.Arguments.BaseAsset),
			QuoteAsset:  strings.TrimSpace(p.Arguments.QuoteAsset),
			Side:        domain.OrderSide(strings.TrimSpace(p.Arguments.Side)),
			AmountKind:  domain.OrderAmountKind(strings.TrimSpace(p.Arguments.AmountKind)),
			AmountValue: strings.TrimSpace(p.Arguments.AmountValue),
			Price:       strings.TrimSpace(p.Arguments.Price),
		}
		result, err := src.CheckOrder(ctx, probe)
		if err != nil {
			return toolErr[checkOrderOutput]("check order failed"), nil
		}
		out := toCheckOrderOutput(result)
		summary := fmt.Sprintf("check %s: pass", account)
		if !result.Passed {
			summary = fmt.Sprintf("check %s: reject - %d reason(s)", account, len(result.Rejects))
		}
		return toolOK(summary, out), nil
	}
}

func setMarketDataInstrumentHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[setMarketDataInstrumentInput],
) (*sdkmcp.CallToolResultFor[setMarketDataInstrumentOutput], error) {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		p *sdkmcp.CallToolParamsFor[setMarketDataInstrumentInput],
	) (*sdkmcp.CallToolResultFor[setMarketDataInstrumentOutput], error) {
		ctx = auth.ContextWithCaller(ctx, mcpCaller)
		if ok, disabled := commandGateMutating[setMarketDataInstrumentOutput](
			ctx, src, setMarketDataInstrumentToolName,
		); !ok {
			return disabled, nil
		}
		instanceID := strings.TrimSpace(p.Arguments.InstanceExternalID)
		externalSymbol := strings.TrimSpace(p.Arguments.ExternalSymbol)
		if instanceID == "" {
			return toolErr[setMarketDataInstrumentOutput]("instanceExternalId is required"), nil
		}
		if externalSymbol == "" {
			return toolErr[setMarketDataInstrumentOutput]("externalSymbol is required"), nil
		}
		if err := src.SetMarketDataInstrumentEnabled(
			ctx, instanceID, externalSymbol, p.Arguments.Enabled,
		); err != nil {
			return toolErr[setMarketDataInstrumentOutput]("set market-data instrument failed"), nil
		}
		out := setMarketDataInstrumentOutput{
			InstanceExternalID: instanceID,
			ExternalSymbol:     externalSymbol,
			Enabled:            p.Arguments.Enabled,
		}
		state := "disabled"
		if p.Arguments.Enabled {
			state = "enabled"
		}
		return toolOK(
			fmt.Sprintf("market-data instrument %s/%s %s",
				instanceID, externalSymbol, state),
			out,
		), nil
	}
}

func submitOrderHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[submitOrderInput],
) (*sdkmcp.CallToolResultFor[submitOrderOutput], error) {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		p *sdkmcp.CallToolParamsFor[submitOrderInput],
	) (*sdkmcp.CallToolResultFor[submitOrderOutput], error) {
		ctx = auth.ContextWithCaller(ctx, mcpCaller)
		if ok, disabled := commandGateMutating[submitOrderOutput](ctx, src, submitOrderToolName); !ok {
			return disabled, nil
		}
		account := domain.AccountID(strings.TrimSpace(p.Arguments.Account))
		if account == "" {
			return toolErr[submitOrderOutput]("account is required"), nil
		}
		o := domain.Order{
			Account:     account,
			BaseAsset:   strings.TrimSpace(p.Arguments.BaseAsset),
			QuoteAsset:  strings.TrimSpace(p.Arguments.QuoteAsset),
			Side:        domain.OrderSide(strings.TrimSpace(p.Arguments.Side)),
			AmountKind:  domain.OrderAmountKind(strings.TrimSpace(p.Arguments.AmountKind)),
			AmountValue: strings.TrimSpace(p.Arguments.AmountValue),
			Price:       strings.TrimSpace(p.Arguments.Price),
		}
		// A caller-supplied external id is optional. When present it must be a
		// well-formed wire form (a malformed one is a clear invalid error); the
		// backend uses it verbatim and rejects a duplicate. When absent the backend
		// generates one and returns it as orderExternalId.
		if supplied := strings.TrimSpace(p.Arguments.ExternalID); supplied != "" {
			id, err := domain.ParseExternalID(supplied)
			if err != nil {
				return toolErr[submitOrderOutput](
					fmt.Sprintf("invalid externalId: %s", err)), nil
			}
			o.ExternalID = id
		}
		mode := strings.TrimSpace(p.Arguments.Mode)
		res, err := src.SubmitOrderToken(ctx, o, mode)
		if err != nil {
			return toolErr[submitOrderOutput](fmt.Sprintf("submit order failed: %s", err)), nil
		}
		out := submitOrderOutput{
			Token:           res.Token,
			KeyID:           res.KeyID,
			ExpiresAt:       res.ExpiresAt.UTC().Format(time.RFC3339),
			OrderExternalID: res.OrderExternalID,
		}
		return toolOK(
			fmt.Sprintf("order %s approved token issued (expires %s)", res.OrderExternalID, out.ExpiresAt),
			out,
		), nil
	}
}

func confirmExecutionHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[confirmExecutionInput],
) (*sdkmcp.CallToolResultFor[confirmExecutionOutput], error) {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		p *sdkmcp.CallToolParamsFor[confirmExecutionInput],
	) (*sdkmcp.CallToolResultFor[confirmExecutionOutput], error) {
		ctx = auth.ContextWithCaller(ctx, mcpCaller)
		if ok, disabled := commandGateMutating[confirmExecutionOutput](ctx, src, confirmExecutionToolName); !ok {
			return disabled, nil
		}
		orderExternalID := strings.TrimSpace(p.Arguments.OrderExternalID)
		if orderExternalID == "" {
			return toolErr[confirmExecutionOutput]("orderExternalId is required"), nil
		}
		token := strings.TrimSpace(p.Arguments.Token)
		if token == "" {
			return toolErr[confirmExecutionOutput]("token is required"), nil
		}
		order, err := src.ConfirmExecution(
			ctx, orderExternalID, token, p.Arguments.Force)
		if err != nil {
			return toolErr[confirmExecutionOutput](fmt.Sprintf("confirm execution failed: %s", err)), nil
		}
		out := confirmExecutionOutput{
			OrderExternalID: order.ExternalID.String(),
			Status:          string(order.Status),
		}
		return toolOK(
			fmt.Sprintf("order %s confirmed: %s", order.ExternalID.String(), order.Status),
			out,
		), nil
	}
}

func cancelHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[cancelInput],
) (*sdkmcp.CallToolResultFor[cancelOutput], error) {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		p *sdkmcp.CallToolParamsFor[cancelInput],
	) (*sdkmcp.CallToolResultFor[cancelOutput], error) {
		ctx = auth.ContextWithCaller(ctx, mcpCaller)
		if ok, disabled := commandGateMutating[cancelOutput](ctx, src, cancelToolName); !ok {
			return disabled, nil
		}
		orderExternalID := strings.TrimSpace(p.Arguments.OrderExternalID)
		if orderExternalID == "" {
			return toolErr[cancelOutput]("orderExternalId is required"), nil
		}
		token := strings.TrimSpace(p.Arguments.Token)
		if token == "" {
			return toolErr[cancelOutput]("token is required"), nil
		}
		order, err := src.CancelOrder(ctx, orderExternalID, token,
			strings.TrimSpace(p.Arguments.Reason), p.Arguments.Force)
		if err != nil {
			return toolErr[cancelOutput](fmt.Sprintf("cancel failed: %s", err)), nil
		}
		out := cancelOutput{
			OrderExternalID: order.ExternalID.String(),
			Status:          string(order.Status),
		}
		return toolOK(
			fmt.Sprintf("order %s cancelled: %s", order.ExternalID.String(), order.Status),
			out,
		), nil
	}
}

// -- internal helpers --

func toHealthOutput(status Status) healthOutput {
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

// isNotFound reports whether err wraps domain.ErrNotFound.
func isNotFound(err error) bool {
	return errors.Is(err, domain.ErrNotFound)
}

// RunStdio builds the Pit Officer MCP server and serves it over stdio until
// the context is cancelled or the transport closes. It is the entry point for
// the `mcp` run mode, which speaks MCP over standard input/output and opens no
// network listener.
func RunStdio(ctx context.Context, src Source, version VersionSource) error {
	server, err := NewServer(src, version)
	if err != nil {
		return fmt.Errorf("mcp: build server: %w", err)
	}
	if err := server.Run(ctx, sdkmcp.NewStdioTransport()); err != nil {
		return fmt.Errorf("mcp: run stdio: %w", err)
	}
	return nil
}

// Handler builds the streamable-HTTP MCP handler for the `serve` run mode.
// The server is built once and reused for every request. Mount the result
// under a stable path (for example /mcp).
func Handler(src Source, version VersionSource) (http.Handler, error) {
	server, err := NewServer(src, version)
	if err != nil {
		return nil, fmt.Errorf("mcp: build server: %w", err)
	}
	handler := sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return server },
		nil,
	)
	return handler, nil
}
