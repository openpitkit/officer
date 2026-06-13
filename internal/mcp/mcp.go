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
	// GetAccountState returns the account row and its account-scoped barriers.
	GetAccountState(ctx context.Context, id domain.AccountID) (
		domain.Account, []domain.Limit, error)
	// ListLimits returns barriers filtered by account; empty account = all.
	ListLimits(ctx context.Context, account domain.AccountID) (
		[]domain.Limit, error)
	// ListAudit returns the most recent n audit rows.
	ListAudit(ctx context.Context, n int) ([]domain.AuditRow, error)
	// CheckOrder runs a non-mutating pre-trade dry-run for probe, returning
	// whether the order would pass plus the would-be lock or block. It mutates
	// no state.
	CheckOrder(ctx context.Context, probe domain.OrderProbe) (domain.CheckResult, error)
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
	Account string `json:"account" jsonschema:"Account identifier"`
}

type getAccountStateOutput struct {
	Account accountDTO `json:"account"`
	Limits  []limitDTO `json:"limits"`
}

type getLimitsInput struct {
	// jsonschema tag is the plain description - no key=value syntax.
	Account string `json:"account,omitempty" jsonschema:"Optional account filter; omit to return all barriers"`
}

type getLimitsOutput struct {
	Limits []limitDTO `json:"limits"`
}

type getAuditInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"Max rows to return (default 50 cap 500)"`
}

type getAuditOutput struct {
	Entries []auditDTO `json:"entries"`
}

type checkOrderInput struct {
	Account     string `json:"account" jsonschema:"Account identifier"`
	BaseAsset   string `json:"baseAsset" jsonschema:"Asset being bought or sold"`
	QuoteAsset  string `json:"quoteAsset" jsonschema:"Asset used for pricing"`
	Side        string `json:"side" jsonschema:"buy or sell"`
	AmountKind  string `json:"amountKind" jsonschema:"quantity or volume"`
	AmountValue string `json:"amountValue" jsonschema:"Order size as an exact decimal string"`
	Price       string `json:"price,omitempty" jsonschema:"Limit price as an exact decimal string; omit for market/unpriced"`
}

type checkOrderOutput struct {
	WouldBlock      *checkOrderBlockDTO   `json:"wouldBlock"`
	Rejects         []checkOrderRejectDTO `json:"rejects"`
	WouldLockPrices []string              `json:"wouldLockPrices"`
	Passed          bool                  `json:"passed"`
}

type setMarketDataInstrumentInput struct {
	InstanceID     string `json:"instanceId" jsonschema:"Market-data instance identifier"`
	ExternalSymbol string `json:"externalSymbol" jsonschema:"Provider-side instrument symbol"`
	Enabled        bool   `json:"enabled" jsonschema:"Whether the instrument should be enabled"`
}

type setMarketDataInstrumentOutput struct {
	InstanceID     string `json:"instanceId"`
	ExternalSymbol string `json:"externalSymbol"`
	Enabled        bool   `json:"enabled"`
}

// accountDTO is the wire shape of a single account (identical to httpapi).
type accountDTO struct {
	ID          string `json:"id"`
	Blocked     bool   `json:"blocked"`
	BlockReason string `json:"blockReason"`
}

// limitDTO is the wire shape of a single risk barrier (identical to httpapi).
type limitDTO struct {
	Policy  string            `json:"policy"`
	Scope   string            `json:"scope"`
	Account string            `json:"account"`
	Asset   string            `json:"asset"`
	Values  map[string]string `json:"values"`
}

// auditDTO is the wire shape of a single audit row (identical to httpapi).
type auditDTO struct {
	At      time.Time `json:"at"`
	Actor   string    `json:"actor"`
	Action  string    `json:"action"`
	Account string    `json:"account"`
	Detail  string    `json:"detail"`
	ID      int64     `json:"id"`
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
		ID:          string(a.ID),
		Blocked:     a.Blocked,
		BlockReason: a.BlockReason,
	}
}

func toLimitDTO(l domain.Limit) limitDTO {
	vals := make(map[string]string, len(l.Values))
	for _, v := range l.Values {
		vals[v.Kind] = v.Value
	}
	return limitDTO{
		Policy:  l.Target.Policy,
		Scope:   l.Target.Scope,
		Account: string(l.Target.Account),
		Asset:   l.Target.Asset,
		Values:  vals,
	}
}

func toAuditDTO(row domain.AuditRow) auditDTO {
	return auditDTO{
		ID:      row.ID,
		At:      row.At,
		Actor:   row.Actor,
		Action:  string(row.Action),
		Account: string(row.Account),
		Detail:  row.Detail,
	}
}

func toLimitDTOs(limits []domain.Limit) []limitDTO {
	out := make([]limitDTO, 0, len(limits))
	for _, l := range limits {
		out = append(out, toLimitDTO(l))
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
		WouldBlock:      block,
		Rejects:         rejects,
		WouldLockPrices: prices,
		Passed:          r.Passed,
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
			Limits:  toLimitDTOs(limits),
		}
		return toolOK(fmt.Sprintf("account %s: %d limit(s)", id, len(limits)), out), nil
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
		out := getLimitsOutput{Limits: toLimitDTOs(limits)}
		return toolOK(fmt.Sprintf("%d limit(s)", len(limits)), out), nil
	}
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
		rows, err := src.ListAudit(ctx, n)
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
		instanceID := strings.TrimSpace(p.Arguments.InstanceID)
		externalSymbol := strings.TrimSpace(p.Arguments.ExternalSymbol)
		if instanceID == "" {
			return toolErr[setMarketDataInstrumentOutput]("instanceId is required"), nil
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
			InstanceID:     instanceID,
			ExternalSymbol: externalSymbol,
			Enabled:        p.Arguments.Enabled,
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
