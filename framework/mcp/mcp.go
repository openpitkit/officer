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
	"sync"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/mcp/catalog"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

const serverName = "pit-officer"

// Source is the Pit Officer-facing seam the MCP surface reads from.
//
// The built-in tools report an error a Source method returns through the
// shared REST mapping, httpapi.DescribeErr: the same code and client-visible
// message as REST, with internal causes only in the server log. A Source
// therefore keeps secrets out of the text that mapping publishes. A host tool
// that returns a Source error itself publishes its text as is, so it maps the
// error through httpapi.DescribeErr first.
type Source interface {
	// Status returns the node's engine and store health.
	Status(ctx context.Context) (Status, error)
	// AccountFeatures reports the account surfaces the control plane offers
	// clients.
	AccountFeatures() backend.AccountFeatures
	// GetAccountState returns the account row and its account-scoped barriers.
	GetAccountState(ctx context.Context, id domain.AccountID) (
		domain.Account, node.AccountLimits, error)
	// CreateAccount creates an account under its public code with its title and
	// currency and returns the stored row.
	CreateAccount(ctx context.Context, account domain.Account) (domain.Account, error)
	// UpdateAccount replaces the public code and title of the account oldID
	// names and returns the stored row.
	UpdateAccount(
		ctx context.Context, oldID domain.AccountID, account domain.Account,
	) (domain.Account, error)
	// DeleteAccount removes an account. Without force an account with dependent
	// rows is refused; force deletes them with it, the account's venue-fee
	// adjustments included, so fees it received may be booked again to the
	// connection's current fee account on the next restart. A trading
	// connection's fee account is refused even with force.
	DeleteAccount(ctx context.Context, id domain.AccountID, force bool) error
	// SetAccountCurrency sets the account's own realized P&L currency, or
	// clears it when currency is empty. A change of the account's effective
	// currency while the account is not economically empty, or while a
	// currency-valued limit applies to it, is refused with a
	// domain.CurrencyChangeBlockedError.
	SetAccountCurrency(ctx context.Context, id domain.AccountID, currency string) error
	// ApplyAdjustment applies one adjustment to an account's balances of one
	// asset through the engine and returns the recorded adjustment: one the
	// engine rejects is recorded with its reason and is not an error. A
	// non-zero externalID is the record's handle, a zero one leaves it to the
	// store, and missing decides whether an account that does not exist yet
	// is registered or refused. An adjustment the engine reports as changing
	// nothing is not recorded and returns domain.ErrNoChange.
	ApplyAdjustment(
		ctx context.Context,
		account domain.AccountID,
		externalID domain.ExternalID,
		req domain.AdjustmentRequest,
		missing domain.MissingAccountPolicy,
	) (domain.AccountAdjustmentRecord, error)
	// CreateAsset creates an asset under its public code with its title and
	// asset class and returns the stored row.
	CreateAsset(ctx context.Context, asset domain.Asset) (domain.Asset, error)
	// UpdateAsset replaces the public code, title and asset class of the asset
	// oldCode names and returns the stored row.
	UpdateAsset(
		ctx context.Context, oldCode string, asset domain.Asset,
	) (domain.Asset, error)
	// DeleteAsset removes an asset. Without force an asset with dependent rows
	// is refused; force deletes them with it, the market-data and trading
	// instruments that reference the asset included. An asset an account or
	// group holds as its currency is refused even with force.
	DeleteAsset(ctx context.Context, code string, force bool) error
	// CreateGroup creates an account group under its public code with its
	// title, currency and notes and returns the stored row.
	CreateGroup(
		ctx context.Context, group domain.AccountGroup,
	) (domain.AccountGroup, error)
	// UpdateGroup replaces the public code and title of the account group
	// oldCode names and returns the stored row.
	UpdateGroup(
		ctx context.Context, oldCode string, group domain.AccountGroup,
	) (domain.AccountGroup, error)
	// DeleteGroup removes an account group and detaches its member accounts.
	// Without force a group with dependent rows is refused; force deletes them
	// with it. A delete that would change the effective currency of a member
	// carrying protected state is refused with a
	// domain.CurrencyChangeBlockedError even with force.
	DeleteGroup(ctx context.Context, code string, force bool) error
	// ListGroups returns every account group with its block state. An account
	// row carries only its own latched block, so the tool surface joins the
	// group tier in to report the effective one.
	ListGroups(ctx context.Context) ([]domain.AccountGroup, error)
	// ListAccountRows returns one page of account rows matching the core list
	// filter, with the total before paging.
	ListAccountRows(
		ctx context.Context, filter store.AccountListFilter,
	) (store.AccountListPage, error)
	// ListGroupRows returns one page of account group rows matching the core
	// list filter, as store.Store.ListGroupRows defines it: on the first page
	// the synthetic default group is pinned first, outside the paging window,
	// and Total counts only the real groups matching the filter.
	ListGroupRows(
		ctx context.Context, filter store.GroupListFilter,
	) (store.GroupListPage, error)
	// ListOrderRows returns one page of order rows matching the core list
	// filter, with the total before paging. Each row carries the backend's
	// display price, signed flag and venue link.
	ListOrderRows(
		ctx context.Context, filter store.OrderListFilter,
	) (backend.OrderListPage, error)
	// ListTradeRows returns one page of trade rows matching the core list
	// filter, with the total before paging.
	ListTradeRows(
		ctx context.Context, filter store.TradeListFilter,
	) (store.TradeListPage, error)
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
	// GetTrading returns provider capabilities and connection status snapshots.
	GetTrading(ctx context.Context) (backend.TradingStatus, error)
	// CredentialStorage reports whether stored credentials are sealed and,
	// when they are not, whether trading connections or market-data sources
	// hold keys in plain text.
	CredentialStorage(ctx context.Context) (backend.CredentialStorage, error)
	// CreateTradingConnection stores a connection, assigning an id when empty.
	CreateTradingConnection(ctx context.Context, connection domain.TradingConnection) (domain.TradingConnection, error)
	// UpdateTradingConnectionSettings updates label and write-only credentials;
	// empty values keep the corresponding stored values.
	UpdateTradingConnectionSettings(ctx context.Context, id, label, credentials string) error
	// SetTradingConnectionEnabled enables or disables a stored connection.
	SetTradingConnectionEnabled(ctx context.Context, id string, enabled bool) error
	// CheckTradingConnection queries the venue's account identity and status.
	CheckTradingConnection(ctx context.Context, id string) (trading.AccountInfo, error)
	// UpsertTradingInstrument stores a venue symbol's Officer asset mapping.
	UpsertTradingInstrument(ctx context.Context, instrument domain.TradingInstrument) error
	// DeleteTradingInstrument removes the connection's venue symbol mapping.
	DeleteTradingInstrument(ctx context.Context, connection, externalSymbol string) error
	// VerifyTradingSymbol reports venue symbol existence and tradability.
	VerifyTradingSymbol(ctx context.Context, id, external string) (backend.TradingSymbolVerification, error)
	// SearchTradingSymbols queries venue symbols and reports search support.
	SearchTradingSymbols(ctx context.Context, id, query string) (backend.TradingSymbolSearch, error)
	// PutTradingAccess grants an Officer account or an account group access to
	// a venue connection.
	PutTradingAccess(ctx context.Context, access domain.TradingAccess) error
	// RemoveTradingAccess revokes the access of exactly one subject, account or
	// accountGroup with the other empty, to a connection.
	RemoveTradingAccess(ctx context.Context, connection domain.ExternalID, account domain.AccountID, accountGroup string) error
	// SetTradingFeeAccount assigns the unlinked-fee account; empty clears it.
	SetTradingFeeAccount(ctx context.Context, connection domain.ExternalID, account domain.AccountID) error
	// ListTradingDestinations returns account destinations; empty mode lists all.
	ListTradingDestinations(ctx context.Context, account domain.AccountID, mode domain.TradingMode) ([]backend.TradingDestinationStatus, error)
	// SendPaperOrder checks, holds and sends an order to a paper connection.
	SendPaperOrder(ctx context.Context, o domain.Order, dest backend.SendDestination) (backend.VenueSendResult, error)
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
	// ConfirmExecution authorizes the shortcut from Officer's recorded signed
	// hold-mode accept approval and records confirmation history for an order that
	// has no execution-report activity. It also returns the resulting attestation,
	// so an MCP caller receives the same proof an HTTP caller does.
	ConfirmExecution(
		ctx context.Context, orderExternalID string,
	) (domain.Order, Attestation, error)
	// CancelOrder authorizes the shortcut from Officer's recorded signed
	// hold-mode accept approval and synthesizes a cancellation report for an order
	// that has no execution-report activity. Optional caller leaves is recorded
	// verbatim when supplied, while the SDK receives the order's own recorded
	// reservation remainder. It also returns the resulting attestation, so an MCP
	// caller receives the same proof an HTTP caller does.
	CancelOrder(
		ctx context.Context,
		orderExternalID, leavesQuantity, reason string,
	) (domain.Order, Attestation, error)
	// ApplyExecutionReport applies one execution report to the order it names
	// and returns the engine result with its attestation. A report on a
	// drop-copy order stays unsigned and returns the zero Attestation, since
	// drop-copy never enforced a pre-trade verdict. A report on an ordinary
	// order is signed and attested fail-closed, its envelope unsigned under
	// eSign-off, so an MCP caller receives the same proof an HTTP caller does.
	ApplyExecutionReport(
		ctx context.Context, in domain.ExecutionReportInput,
	) (engine.ExecutionReportResult, Attestation, error)
}

// Attestation is the surface-agnostic proof an MCP caller receives for a
// workflow confirmation, a cancellation or an execution report. It mirrors the
// fields the HTTP surface exposes; it is the zero value (Token empty) when
// attestation was skipped or failed best-effort.
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

// Status is the health of the node the service serves.
type Status struct {
	// Node is the node's engine and store health.
	Node NodeHealth
	// Healthy reports whether the node's engine is running and its store is
	// reachable.
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
	Caller     domain.Caller
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
	caller domain.Caller,
) (*sdkmcp.Server, error) {
	if src == nil {
		return nil, fmt.Errorf("mcp: nil source")
	}
	if authorizer == nil {
		return nil, fmt.Errorf("mcp: nil authorizer")
	}
	return buildServer(reg, src, version, authorizer, caller), nil
}

func buildServer(
	reg *ToolRegistry,
	src Source,
	version VersionSource,
	authorizer httpx.Authorizer,
	caller domain.Caller,
) *sdkmcp.Server {
	serverVersion := ""
	if version != nil {
		serverVersion = version.Version()
	}
	caller.Source = domain.SourceMCP
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)
	deps := RegisterDeps{Source: src, Authorizer: authorizer, Caller: caller}
	for _, d := range reg.Descriptors() {
		if d.Register == nil {
			continue
		}
		d.Register(server, deps)
	}
	return server
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
		ctx = auth.ContextWithCaller(ctx, deps.Caller)
		if deps.Authorizer == nil {
			return toolErr[Out]("permission denied"), nil
		}
		if err := deps.Authorizer.Authorize(ctx, deps.Caller, d.Name); err != nil {
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
	caller domain.Caller,
) error {
	server, err := Build(reg, src, version, authorizer, caller)
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
	callerResolver auth.CallerResolver,
) (http.Handler, error) {
	if reg == nil {
		return nil, fmt.Errorf("mcp: nil registry")
	}
	if src == nil {
		return nil, fmt.Errorf("mcp: nil source")
	}
	if authorizer == nil {
		return nil, fmt.Errorf("mcp: nil authorizer")
	}
	if callerResolver == nil {
		return nil, fmt.Errorf("mcp: nil caller resolver")
	}
	handler := &sessionBindingHandler{
		callers: make(map[string]domain.Caller),
	}
	handler.sdk = sdkmcp.NewStreamableHTTPHandler(
		func(r *http.Request) *sdkmcp.Server {
			caller, err := auth.CallerFromContext(r.Context())
			if err != nil {
				// serveHTTP stamps the resolved caller before the SDK sees the
				// request, so this is a wiring defect; the session opens with
				// every tool refused rather than under a guessed identity.
				return buildServer(reg, src, version, nil, domain.Caller{})
			}
			return buildServer(reg, src, version, authorizer, caller)
		},
		nil,
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, err := callerResolver(r)
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		caller.Source = domain.SourceMCP
		ctx := auth.ContextWithCaller(r.Context(), caller)
		handler.serveHTTP(w, r.WithContext(ctx), caller)
	}), nil
}

type sessionBindingHandler struct {
	sdk     http.Handler
	mu      sync.Mutex
	callers map[string]domain.Caller
}

func (h *sessionBindingHandler) serveHTTP(
	w http.ResponseWriter,
	r *http.Request,
	caller domain.Caller,
) {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID != "" {
		h.mu.Lock()
		boundCaller, exists := h.callers[sessionID]
		h.mu.Unlock()
		if !exists {
			httpx.WriteErr(w, fmt.Errorf("mcp: session %q: %w", sessionID, domain.ErrNotFound))
			return
		}
		if boundCaller != caller {
			httpx.WriteErr(w, domain.ErrForbidden)
			return
		}
	}

	recorder := &sessionResponseWriter{ResponseWriter: w}
	if sessionID == "" {
		recorder.beforeWrite = func() {
			openedSessionID := recorder.Header().Get("Mcp-Session-Id")
			if openedSessionID == "" {
				return
			}
			h.mu.Lock()
			h.callers[openedSessionID] = caller
			h.mu.Unlock()
		}
	}
	h.sdk.ServeHTTP(recorder, r)

	if r.Method == http.MethodDelete && recorder.status == http.StatusNoContent {
		h.mu.Lock()
		delete(h.callers, sessionID)
		h.mu.Unlock()
	}
}

type sessionResponseWriter struct {
	http.ResponseWriter
	beforeWrite func()
	wroteHeader bool
	status      int
}

func (w *sessionResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	if w.beforeWrite != nil {
		w.beforeWrite()
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *sessionResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (w *sessionResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *sessionResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// SourceFor adapts a control plane to the MCP Source seam. Health maps onto the
// deployment Status shape, the approval, drop-copy, and attestation results map
// onto their MCP carriers, and every other call passes through unchanged.
func SourceFor(cp backend.ControlPlane) Source {
	return controlPlaneSource{cp: cp}
}

type controlPlaneSource struct {
	cp backend.ControlPlane
}

func (s controlPlaneSource) Status(ctx context.Context) (Status, error) {
	status, err := s.cp.Status(ctx)
	if err != nil {
		return Status{}, err
	}

	n := status.Node
	return Status{
		Node: NodeHealth{
			Engine: EngineHealth{
				Version:      n.Engine.Version,
				BuildProfile: n.Engine.BuildProfile,
				Running:      n.Engine.Running,
			},
			Store: StoreHealth{
				Path:          n.Store.Path,
				SchemaVersion: n.Store.SchemaVersion,
				Reachable:     n.Store.Reachable,
			},
		},
		Healthy: status.Healthy,
	}, nil
}

func (s controlPlaneSource) AccountFeatures() backend.AccountFeatures {
	return s.cp.AccountFeatures()
}

func (s controlPlaneSource) GetAccountState(
	ctx context.Context, id domain.AccountID,
) (domain.Account, node.AccountLimits, error) {
	return s.cp.GetAccountState(ctx, id)
}

func (s controlPlaneSource) CreateAccount(
	ctx context.Context, account domain.Account,
) (domain.Account, error) {
	return s.cp.CreateAccount(ctx, account)
}

func (s controlPlaneSource) UpdateAccount(
	ctx context.Context, oldID domain.AccountID, account domain.Account,
) (domain.Account, error) {
	return s.cp.UpdateAccount(ctx, oldID, account)
}

func (s controlPlaneSource) DeleteAccount(
	ctx context.Context, id domain.AccountID, force bool,
) error {
	return s.cp.DeleteAccount(ctx, id, force)
}

func (s controlPlaneSource) SetAccountCurrency(
	ctx context.Context, id domain.AccountID, currency string,
) error {
	return s.cp.SetAccountCurrency(ctx, id, currency)
}

func (s controlPlaneSource) ApplyAdjustment(
	ctx context.Context,
	account domain.AccountID,
	externalID domain.ExternalID,
	req domain.AdjustmentRequest,
	missing domain.MissingAccountPolicy,
) (domain.AccountAdjustmentRecord, error) {
	return s.cp.ApplyAdjustment(ctx, account, externalID, req, missing)
}

func (s controlPlaneSource) CreateAsset(
	ctx context.Context, asset domain.Asset,
) (domain.Asset, error) {
	return s.cp.CreateAsset(ctx, asset)
}

func (s controlPlaneSource) UpdateAsset(
	ctx context.Context, oldCode string, asset domain.Asset,
) (domain.Asset, error) {
	return s.cp.UpdateAsset(ctx, oldCode, asset)
}

func (s controlPlaneSource) DeleteAsset(
	ctx context.Context, code string, force bool,
) error {
	return s.cp.DeleteAsset(ctx, code, force)
}

func (s controlPlaneSource) CreateGroup(
	ctx context.Context, group domain.AccountGroup,
) (domain.AccountGroup, error) {
	return s.cp.CreateGroup(ctx, group)
}

func (s controlPlaneSource) UpdateGroup(
	ctx context.Context, oldCode string, group domain.AccountGroup,
) (domain.AccountGroup, error) {
	return s.cp.UpdateGroup(ctx, oldCode, group)
}

func (s controlPlaneSource) DeleteGroup(
	ctx context.Context, code string, force bool,
) error {
	return s.cp.DeleteGroup(ctx, code, force)
}

func (s controlPlaneSource) ListGroups(
	ctx context.Context,
) ([]domain.AccountGroup, error) {
	return s.cp.ListGroups(ctx)
}

func (s controlPlaneSource) ListAccountRows(
	ctx context.Context, filter store.AccountListFilter,
) (store.AccountListPage, error) {
	return s.cp.ListAccountRows(ctx, filter)
}

func (s controlPlaneSource) ListGroupRows(
	ctx context.Context, filter store.GroupListFilter,
) (store.GroupListPage, error) {
	return s.cp.ListGroupRows(ctx, filter)
}

func (s controlPlaneSource) ListOrderRows(
	ctx context.Context, filter store.OrderListFilter,
) (backend.OrderListPage, error) {
	return s.cp.ListOrderRows(ctx, filter)
}

func (s controlPlaneSource) ListTradeRows(
	ctx context.Context, filter store.TradeListFilter,
) (store.TradeListPage, error) {
	return s.cp.ListTradeRows(ctx, filter)
}

func (s controlPlaneSource) ListLimits(
	ctx context.Context, account domain.AccountID,
) (node.AccountLimits, error) {
	return s.cp.ListLimits(ctx, account)
}

func (s controlPlaneSource) ListAudit(ctx context.Context, n int) ([]domain.AuditRow, error) {
	return s.cp.ListAudit(ctx, n)
}

func (s controlPlaneSource) ListAuditFiltered(
	ctx context.Context, filter domain.AuditFilter, n int,
) ([]domain.AuditRow, error) {
	return s.cp.ListAuditFiltered(ctx, filter, n)
}

func (s controlPlaneSource) CheckOrder(
	ctx context.Context, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	return s.cp.CheckOrder(ctx, probe)
}

func (s controlPlaneSource) GetOrder(
	ctx context.Context, externalID string,
) (domain.OrderDetail, error) {
	return s.cp.GetOrder(ctx, externalID)
}

func (s controlPlaneSource) SetMarketDataInstrumentEnabled(
	ctx context.Context, instanceID, externalSymbol string, enabled bool,
) error {
	return s.cp.SetMarketDataInstrumentEnabled(ctx, instanceID, externalSymbol, enabled)
}

func (s controlPlaneSource) CommandEnabled(ctx context.Context, command string) (bool, error) {
	return s.cp.CommandEnabled(ctx, command)
}

func (s controlPlaneSource) SubmitOrderToken(
	ctx context.Context,
	o domain.Order,
	mode string,
	missing domain.MissingAccountPolicy,
) (SubmitOrderTokenResult, error) {
	tok, err := s.cp.SubmitOrderToken(ctx, o, mode, missing)
	if err != nil {
		return SubmitOrderTokenResult{}, err
	}
	return SubmitOrderTokenResult{
		Token:           tok.Token,
		KeyID:           tok.KeyID,
		OrderExternalID: tok.OrderExternalID,
		Verdict:         tok.Verdict,
		Reasons:         tok.Reasons,
	}, nil
}

func (s controlPlaneSource) SubmitDropCopyOrder(
	ctx context.Context, o domain.Order, missing domain.MissingAccountPolicy,
) (SubmitDropCopyOrderResult, error) {
	order, err := s.cp.SubmitDropCopyOrder(ctx, o, missing)
	if err != nil {
		return SubmitDropCopyOrderResult{}, err
	}
	return SubmitDropCopyOrderResult{
		OrderExternalID: order.ExternalID.String(),
		Status:          order.Status,
	}, nil
}

func (s controlPlaneSource) ConfirmExecution(
	ctx context.Context, orderExternalID string,
) (domain.Order, Attestation, error) {
	order, att, err := s.cp.ConfirmExecution(ctx, orderExternalID)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	return order, attestationFromBackend(att), nil
}

func (s controlPlaneSource) CancelOrder(
	ctx context.Context, orderExternalID, leavesQuantity, reason string,
) (domain.Order, Attestation, error) {
	order, att, err := s.cp.CancelOrder(
		ctx, orderExternalID, leavesQuantity, reason,
	)
	if err != nil {
		return domain.Order{}, Attestation{}, err
	}
	return order, attestationFromBackend(att), nil
}

func (s controlPlaneSource) ApplyExecutionReport(
	ctx context.Context, in domain.ExecutionReportInput,
) (engine.ExecutionReportResult, Attestation, error) {
	result, att, err := s.cp.ApplyExecutionReport(ctx, in)
	if err != nil {
		return engine.ExecutionReportResult{}, Attestation{}, err
	}
	return result, attestationFromBackend(att), nil
}

// attestationFromBackend maps the backend attestation onto the surface-agnostic
// MCP carrier, mirroring the token and key id the HTTP surface exposes.
func attestationFromBackend(att backend.Attestation) Attestation {
	return Attestation{
		Token:  att.Token,
		KeyID:  att.KeyID,
		Signed: att.Signed,
	}
}

func (s controlPlaneSource) GetTrading(ctx context.Context) (backend.TradingStatus, error) {
	return s.cp.GetTrading(ctx)
}

func (s controlPlaneSource) CredentialStorage(ctx context.Context) (backend.CredentialStorage, error) {
	return s.cp.CredentialStorage(ctx)
}

func (s controlPlaneSource) CreateTradingConnection(ctx context.Context, connection domain.TradingConnection) (domain.TradingConnection, error) {
	return s.cp.CreateTradingConnection(ctx, connection)
}

func (s controlPlaneSource) UpdateTradingConnectionSettings(ctx context.Context, id, label, credentials string) error {
	return s.cp.UpdateTradingConnectionSettings(ctx, id, label, credentials)
}

func (s controlPlaneSource) SetTradingConnectionEnabled(ctx context.Context, id string, enabled bool) error {
	return s.cp.SetTradingConnectionEnabled(ctx, id, enabled)
}

func (s controlPlaneSource) CheckTradingConnection(ctx context.Context, id string) (trading.AccountInfo, error) {
	return s.cp.CheckTradingConnection(ctx, id)
}

func (s controlPlaneSource) UpsertTradingInstrument(ctx context.Context, instrument domain.TradingInstrument) error {
	return s.cp.UpsertTradingInstrument(ctx, instrument)
}

func (s controlPlaneSource) DeleteTradingInstrument(ctx context.Context, connection, externalSymbol string) error {
	return s.cp.DeleteTradingInstrument(ctx, connection, externalSymbol)
}

func (s controlPlaneSource) VerifyTradingSymbol(ctx context.Context, id, external string) (backend.TradingSymbolVerification, error) {
	return s.cp.VerifyTradingSymbol(ctx, id, external)
}

func (s controlPlaneSource) SearchTradingSymbols(ctx context.Context, id, query string) (backend.TradingSymbolSearch, error) {
	return s.cp.SearchTradingSymbols(ctx, id, query)
}

func (s controlPlaneSource) PutTradingAccess(ctx context.Context, access domain.TradingAccess) error {
	return s.cp.PutTradingAccess(ctx, access)
}

func (s controlPlaneSource) RemoveTradingAccess(ctx context.Context, connection domain.ExternalID, account domain.AccountID, accountGroup string) error {
	return s.cp.RemoveTradingAccess(ctx, connection, account, accountGroup)
}

func (s controlPlaneSource) SetTradingFeeAccount(ctx context.Context, connection domain.ExternalID, account domain.AccountID) error {
	return s.cp.SetTradingFeeAccount(ctx, connection, account)
}

func (s controlPlaneSource) ListTradingDestinations(ctx context.Context, account domain.AccountID, mode domain.TradingMode) ([]backend.TradingDestinationStatus, error) {
	return s.cp.ListTradingDestinations(ctx, account, mode)
}

func (s controlPlaneSource) SendPaperOrder(ctx context.Context, o domain.Order, dest backend.SendDestination) (backend.VenueSendResult, error) {
	return s.cp.SendPaperOrder(ctx, o, dest)
}
