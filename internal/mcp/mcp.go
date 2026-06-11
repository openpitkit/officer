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
// seam. The AI surface is read-only: it exposes deployment status, account
// state, risk limits, and audit log. It carries no secrets, no credentials,
// and no order-flow control.
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
