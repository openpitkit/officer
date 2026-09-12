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

// Package mcp provides the framework MCP registry and guard used by Pit Officer
// applications.
package mcp

import (
	"context"
	"fmt"
	"net/http"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/mcp/catalog"
	"go.openpit.dev/officer/framework/node"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

var mcpCaller = domain.Caller{Source: domain.SourceMCP, Principal: domain.PrincipalOperator}

const serverName = "pit-officer"

// Source is the Pit Officer-facing seam the MCP surface reads from.
type Source interface {
	// Status returns the aggregate health of every node in the deployment.
	Status(ctx context.Context) (Status, error)
	// GetAccountState returns the account row and its account-scoped barriers.
	GetAccountState(ctx context.Context, id domain.AccountID) (
		domain.Account, node.AccountLimits, error)
	// ListGroups returns every account group with its block state. An account
	// row carries only its own latched block, so the tool surface joins the
	// group tier in to report the effective one.
	ListGroups(ctx context.Context) ([]domain.AccountGroup, error)
	// ListLimits returns the typed barriers filtered by account code.
	ListLimits(ctx context.Context, account domain.AccountID) (
		node.AccountLimits, error)
	// ListAudit returns the most recent n audit rows.
	ListAudit(ctx context.Context, n int) ([]domain.AuditRow, error)
	// ListAuditFiltered returns the most recent n audit rows matching the filter.
	ListAuditFiltered(
		ctx context.Context, filter domain.AuditFilter, n int,
	) ([]domain.AuditRow, error)
	// CheckOrder runs a non-mutating pre-trade dry-run.
	CheckOrder(ctx context.Context, probe domain.OrderProbe) (domain.CheckResult, error)
	// GetOrder returns one order addressed by its external id.
	GetOrder(ctx context.Context, externalID string) (domain.OrderDetail, error)
	// SetMarketDataInstrumentEnabled toggles one configured market-data instrument.
	SetMarketDataInstrumentEnabled(
		ctx context.Context, instanceID, externalSymbol string, enabled bool,
	) error
	// CommandEnabled reports whether the operator has the named MCP command enabled.
	CommandEnabled(ctx context.Context, command string) (bool, error)
	// SubmitOrderToken runs the pre-trade pipeline and issues an approval token.
	// missing is the caller's explicit choice for an order naming an account that
	// does not exist yet.
	SubmitOrderToken(
		ctx context.Context,
		o domain.Order,
		mode string,
		missing domain.MissingAccountPolicy,
	) (SubmitOrderTokenResult, error)
	// SubmitDropCopyOrder submits through the distinct unsigned operation. missing
	// accepts only domain.MissingAccountCreate: a drop-copy reports an execution
	// that already happened, so it cannot refuse an unknown account.
	SubmitDropCopyOrder(
		ctx context.Context, o domain.Order, missing domain.MissingAccountPolicy,
	) (SubmitDropCopyOrderResult, error)
	// ConfirmExecution verifies the token and records confirmation history for an
	// order that has no execution-report activity. It also returns the resulting
	// attestation, so an MCP caller receives the same proof an HTTP caller does.
	ConfirmExecution(
		ctx context.Context, orderExternalID, token string,
	) (domain.Order, Attestation, error)
	// CancelOrder verifies the token and synthesizes a cancellation report for an
	// order that has no execution-report activity. Optional caller leaves is
	// recorded verbatim when supplied, while the SDK receives the order's
	// own recorded reservation remainder. It also returns the resulting
	// attestation, so an MCP caller receives the same proof an HTTP caller does.
	CancelOrder(
		ctx context.Context,
		orderExternalID, token, leavesQuantity, reason string,
	) (domain.Order, Attestation, error)
}

// Attestation is the surface-agnostic proof an MCP caller receives for a
// workflow confirmation or cancellation. It mirrors the fields the HTTP
// surface exposes; it is the zero value (Token empty) when attestation was
// skipped or failed best-effort.
type Attestation struct {
	// Token is the base64url-encoded attestation envelope; empty when none.
	Token string
	// KeyID is the signing key id; empty under eSign-off or when none.
	KeyID string
	// Signed reports whether the envelope carries a signature.
	Signed bool
}

// SubmitOrderTokenResult is the surface-agnostic result of issuing an approval
// token.
type SubmitOrderTokenResult struct {
	// Token is the base64url-encoded approval envelope.
	Token string
	// KeyID is the signing key id; empty under eSign-off.
	KeyID string
	// OrderExternalID is the order's opaque public handle.
	OrderExternalID string
	// Verdict is the signed pre-trade decision: "accept" or "reject".
	Verdict string
	// Reasons carries engine reject reasons when Verdict is "reject".
	Reasons []domain.OrderReject
}

// SubmitDropCopyOrderResult identifies the unsigned drop-copy order recorded by
// a successful submission.
type SubmitDropCopyOrderResult struct {
	// OrderExternalID is the order's opaque public handle.
	OrderExternalID string
	// Status is the persisted order status returned by the submission.
	Status domain.OrderStatus
}

// VersionSource reports the engine version used as the MCP server version.
type VersionSource interface {
	// Version returns the engine SDK/runtime version string.
	Version() string
}

// Status is the aggregate health of the whole deployment.
type Status struct {
	// Nodes carries one health record per node.
	Nodes []NodeHealth
	// Healthy reports whether every node is live and reachable.
	Healthy bool
}

// NodeHealth is one node's engine and store health.
type NodeHealth struct {
	// Engine is the node's engine health.
	Engine EngineHealth
	// Store is the node's store health.
	Store StoreHealth
}

// EngineHealth is a node's engine health.
type EngineHealth struct {
	// Version is the engine SDK/runtime version.
	Version string
	// BuildProfile is the engine build profile.
	BuildProfile string
	// Running reports whether the engine handle is live.
	Running bool
}

// StoreHealth is a node's store health.
type StoreHealth struct {
	// Path is the on-disk database location.
	Path string
	// SchemaVersion is the applied schema version.
	SchemaVersion int
	// Reachable reports whether the store responded to its last probe.
	Reachable bool
}

// ToolDescriptor declares one MCP tool to the framework registry.
type ToolDescriptor struct {
	Name             string
	Title            string
	Description      string
	AgentDescription string
	Mutating         bool
	Protective       bool
	Implemented      bool
	DefaultEnabled   bool
	// Register adds the tool to the server. It is nil for design-only
	// placeholders that appear in the catalogue but have no handler yet.
	Register func(server *sdkmcp.Server, deps RegisterDeps)
}

// RegisterDeps carries the dependencies a descriptor's Register closure needs.
type RegisterDeps struct {
	Source     Source
	Authorizer httpx.Authorizer
}

// ToolRegistry stores descriptors in registration order, keyed by stable name.
type ToolRegistry struct {
	descriptors []ToolDescriptor
	index       map[string]int
}

// NewToolRegistry constructs an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{index: map[string]int{}}
}

// Register adds d keyed by Name. Re-registering an existing Name replaces the
// descriptor in place and preserves order.
func (r *ToolRegistry) Register(d ToolDescriptor) {
	if r.index == nil {
		r.index = map[string]int{}
	}
	if idx, ok := r.index[d.Name]; ok {
		r.descriptors[idx] = d
		return
	}
	r.index[d.Name] = len(r.descriptors)
	r.descriptors = append(r.descriptors, d)
}

// Unregister removes name from the registry and catalogue.
func (r *ToolRegistry) Unregister(name string) {
	if r == nil || r.index == nil {
		return
	}
	idx, ok := r.index[name]
	if !ok {
		return
	}
	copy(r.descriptors[idx:], r.descriptors[idx+1:])
	r.descriptors = r.descriptors[:len(r.descriptors)-1]
	delete(r.index, name)
	for i := idx; i < len(r.descriptors); i++ {
		r.index[r.descriptors[i].Name] = i
	}
}

// Descriptors returns descriptors in registration order.
func (r *ToolRegistry) Descriptors() []ToolDescriptor {
	if r == nil {
		return nil
	}
	return append([]ToolDescriptor(nil), r.descriptors...)
}

// Catalog projects registered descriptors to the SDK-free catalogue facade.
func (r *ToolRegistry) Catalog() catalog.Catalog {
	if r == nil {
		return catalog.New(nil)
	}
	commands := make([]catalog.CatalogCommand, 0, len(r.descriptors))
	for _, d := range r.descriptors {
		commands = append(commands, catalog.CatalogCommand{
			Name:             d.Name,
			Title:            d.Title,
			AgentDescription: d.AgentDescription,
			Mutating:         d.Mutating,
			Protective:       d.Protective,
			Implemented:      d.Implemented,
			DefaultEnabled:   d.DefaultEnabled,
		})
	}
	return catalog.New(commands)
}

// Build creates the Pit Officer MCP server from registered descriptors.
func Build(
	reg *ToolRegistry,
	src Source,
	version VersionSource,
	authorizer httpx.Authorizer,
) (*sdkmcp.Server, error) {
	if src == nil {
		return nil, fmt.Errorf("mcp: nil source")
	}
	if authorizer == nil {
		authorizer = httpx.AllowAll{}
	}
	serverVersion := ""
	if version != nil {
		serverVersion = version.Version()
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)
	deps := RegisterDeps{Source: src, Authorizer: authorizer}
	for _, d := range reg.Descriptors() {
		if d.Register == nil {
			continue
		}
		d.Register(server, deps)
	}
	return server, nil
}

// ToolBody is the typed body wrapped by Guard.
type ToolBody[In, Out any] func(
	context.Context,
	*sdkmcp.ServerSession,
	In,
) (string, Out, error)

// Guard wraps a typed MCP handler with caller stamping, authorization, and the
// operator command-enable gate.
func Guard[In, Out any](
	d ToolDescriptor,
	deps RegisterDeps,
	inner ToolBody[In, Out],
) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[In],
) (*sdkmcp.CallToolResultFor[Out], error) {
	return func(
		ctx context.Context,
		ss *sdkmcp.ServerSession,
		p *sdkmcp.CallToolParamsFor[In],
	) (*sdkmcp.CallToolResultFor[Out], error) {
		ctx = auth.ContextWithCaller(ctx, mcpCaller)
		authorizer := deps.Authorizer
		if authorizer == nil {
			authorizer = httpx.AllowAll{}
		}
		if err := authorizer.Authorize(ctx, mcpCaller, d.Name); err != nil {
			return toolErr[Out]("permission denied"), nil
		}
		if ok, disabled := commandGate[Out](ctx, deps.Source, d); !ok {
			return disabled, nil
		}
		var args In
		if p != nil {
			args = p.Arguments
		}
		text, out, err := inner(ctx, ss, args)
		if err != nil {
			return toolErr[Out](err.Error()), nil
		}
		return toolOK(text, out), nil
	}
}

func commandGate[Out any](
	ctx context.Context,
	src Source,
	d ToolDescriptor,
) (bool, *sdkmcp.CallToolResultFor[Out]) {
	enabled, err := src.CommandEnabled(ctx, d.Name)
	if err != nil {
		if d.Mutating {
			return false, toolErr[Out]("command access check failed; command not executed")
		}
		// Read-only tools fail open when the command-access store cannot be read:
		// Guard has already authorized the caller, so the command toggle is an
		// operator safety gate only for commands that can mutate state.
		return true, nil
	}
	if !enabled {
		return false, toolDisabled[Out](d.Name)
	}
	return true, nil
}

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

// RunStdio serves the registered MCP surface over stdio.
func RunStdio(
	ctx context.Context,
	reg *ToolRegistry,
	src Source,
	version VersionSource,
	authorizer httpx.Authorizer,
) error {
	server, err := Build(reg, src, version, authorizer)
	if err != nil {
		return fmt.Errorf("mcp: build server: %w", err)
	}
	if err := server.Run(ctx, sdkmcp.NewStdioTransport()); err != nil {
		return fmt.Errorf("mcp: run stdio: %w", err)
	}
	return nil
}

// Handler builds the streamable-HTTP MCP handler.
func Handler(
	reg *ToolRegistry,
	src Source,
	version VersionSource,
	authorizer httpx.Authorizer,
) (http.Handler, error) {
	server, err := Build(reg, src, version, authorizer)
	if err != nil {
		return nil, fmt.Errorf("mcp: build server: %w", err)
	}
	handler := sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return server },
		nil,
	)
	return handler, nil
}
