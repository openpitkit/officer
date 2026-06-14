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

// Package backend implements the Pit Officer control-plane service. The service
// is the single entry point the MCP and HTTP surfaces call into. It depends
// only on the node.NodeRouter seam, never on a concrete engine or store, so the
// same control-plane logic runs over one in-process node or many remote shards.
package backend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.openpit.dev/officer/internal/auth"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
	"go.openpit.dev/officer/internal/marketdata"
	"go.openpit.dev/officer/internal/mcpcatalog"
	"go.openpit.dev/officer/internal/node"
)

// MarketDataRuntime is the live view of the connector manager the backend
// needs: the current per-instance subscription state, a way to re-apply
// configuration, and a way to push an operator-set manual mark into a running
// instance after an upsert. The *marketdata.Manager satisfies it.
type MarketDataRuntime interface {
	InstanceStatuses() map[string]marketdata.InstanceRuntimeStatus
	AppliedConfig() map[string]marketdata.AppliedInstanceConfig
	// QuoteUpdateInterval returns the elapsed time between the two most recent
	// ticks of the identified instrument's quote, and whether it is known yet.
	QuoteUpdateInterval(instanceID, external string) (time.Duration, bool)
	Restart() error
	// PushManual delivers one instrument's operator-set manual mark into the
	// running instance as a single quote. It is a no-op when the instance is not
	// running, its connector is not push-capable, or the instrument is disabled
	// or has no manual price.
	PushManual(instanceID string, instrument domain.MarketDataInstrument)
	// Stop halts running connectors before a control-plane restore swaps the
	// underlying engine sink.
	Stop()
	// UseSink replaces the quote sink used by the next Start/Restart.
	UseSink(sink marketdata.Sink) error
}

// Status is the aggregate health of the whole deployment, assembled for the
// operator dashboard from the health of every node behind the router.
type Status struct {
	// Nodes carries one health record per node, in router enumeration order.
	Nodes []node.Health
	// Healthy reports whether every node reported a live engine and a reachable
	// store.
	Healthy bool
}

// Service is the Pit Officer control plane. It is constructed once per process
// and is safe for concurrent use by the surface handlers.
type Service struct {
	router node.NodeRouter
	md     MarketDataRuntime
}

// New constructs a Service over the given node router and market-data runtime.
// The router is the seam through which the service reaches every execution
// target; md is the live connector-manager view used to surface per-instance
// subscription state and to re-apply configuration. md may be nil, in which
// case market-data state resolves to empty and RestartMarketData is a no-op.
func New(router node.NodeRouter, md MarketDataRuntime) *Service {
	return &Service{router: router, md: md}
}

// Status returns the aggregate health of every node behind the router, for the
// dashboard. It queries each node's health and reports the deployment as
// healthy only when all nodes are healthy.
func (s *Service) Status(ctx context.Context) (Status, error) {
	nodes := s.router.All()
	healths := make([]node.Health, 0, len(nodes))
	healthy := true
	for i, n := range nodes {
		health, err := n.Health(ctx)
		if err != nil {
			return Status{}, fmt.Errorf("backend: node %d health: %w", i, err)
		}
		if !health.Engine.Running || !health.Store.Reachable {
			healthy = false
		}
		healths = append(healths, health)
	}
	return Status{Nodes: healths, Healthy: healthy}, nil
}

// keyFor builds the routing key for an account in the default tenant. Tenant is
// an internal axis and is never exposed on a surface.
func keyFor(id domain.AccountID) node.Key {
	return node.Key{Tenant: domain.DefaultTenant, Account: id}
}

// ListAccounts returns every account aggregated across all nodes.
func (s *Service) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	accounts := make([]domain.Account, 0)
	for i, n := range s.router.All() {
		part, err := n.ListAccounts(ctx)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list accounts: %w", i, err)
		}
		accounts = append(accounts, part...)
	}
	return accounts, nil
}

// CreateAccount validates the id, routes to the owning node, and creates the
// account.
func (s *Service) CreateAccount(
	ctx context.Context, id domain.AccountID,
) (domain.Account, error) {
	if err := domain.ValidateAccountID(id); err != nil {
		return domain.Account{}, err
	}
	n, err := s.router.Route(keyFor(id))
	if err != nil {
		return domain.Account{}, fmt.Errorf("backend: route account: %w", err)
	}
	return n.CreateAccount(ctx, keyFor(id), auth.CallerFromContext(ctx))
}

// BlockAccount validates the id and blocks the account with reason.
func (s *Service) BlockAccount(
	ctx context.Context, id domain.AccountID, reason string,
) error {
	return s.setAccountBlocked(ctx, id, true, reason)
}

// UnblockAccount validates the id and unblocks the account.
func (s *Service) UnblockAccount(ctx context.Context, id domain.AccountID) error {
	return s.setAccountBlocked(ctx, id, false, "")
}

func (s *Service) setAccountBlocked(
	ctx context.Context, id domain.AccountID, blocked bool, reason string,
) error {
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	n, err := s.router.Route(keyFor(id))
	if err != nil {
		return fmt.Errorf("backend: route account: %w", err)
	}
	return n.SetAccountBlocked(ctx, keyFor(id), blocked, reason, auth.CallerFromContext(ctx))
}

// GetAccountState validates the id and returns the account row and its
// account-scoped barriers.
func (s *Service) GetAccountState(
	ctx context.Context, id domain.AccountID,
) (domain.Account, []domain.Limit, error) {
	if err := domain.ValidateAccountID(id); err != nil {
		return domain.Account{}, nil, err
	}
	n, err := s.router.Route(keyFor(id))
	if err != nil {
		return domain.Account{}, nil, fmt.Errorf("backend: route account: %w", err)
	}
	return n.GetAccountState(ctx, keyFor(id))
}

// ListLimits returns the barriers that reference account, aggregated across all
// nodes. An empty account returns all barriers.
func (s *Service) ListLimits(
	ctx context.Context, account domain.AccountID,
) ([]domain.Limit, error) {
	limits := make([]domain.Limit, 0)
	for i, n := range s.router.All() {
		part, err := n.ListLimits(ctx, account)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list limits: %w", i, err)
		}
		limits = append(limits, part...)
	}
	return limits, nil
}

// PutLimit validates the barrier, checks the referenced account exists when the
// scope has an account axis, routes to the owning node, and upserts it.
func (s *Service) PutLimit(ctx context.Context, limit domain.Limit) error {
	limit.Target.Tenant = domain.DefaultTenant
	if err := domain.ValidateLimit(limit); err != nil {
		return err
	}

	n, err := s.router.Route(keyFor(limit.Target.Account))
	if err != nil {
		return fmt.Errorf("backend: route limit: %w", err)
	}

	return n.PutLimit(ctx, limit, auth.CallerFromContext(ctx))
}

// DeleteLimit validates the target, routes to the owning node, and removes the
// barrier.
func (s *Service) DeleteLimit(ctx context.Context, target domain.LimitTarget) error {
	target.Tenant = domain.DefaultTenant
	if err := domain.ValidateLimit(domain.Limit{
		Target: target,
		Values: placeholderValues(target.Policy),
	}); err != nil {
		return err
	}

	n, err := s.router.Route(keyFor(target.Account))
	if err != nil {
		return fmt.Errorf("backend: route limit: %w", err)
	}
	return n.DeleteLimit(ctx, target, auth.CallerFromContext(ctx))
}

// placeholderValues returns a minimal valid value set for policy so the target
// half of a delete request can be validated by domain.ValidateLimit without the
// caller supplying values. Delete addresses a barrier by target only.
func placeholderValues(policy string) []domain.LimitValue {
	switch policy {
	case domain.PolicyRateLimit:
		return []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: "1"},
			{Kind: domain.KindWindow, Value: "1s"},
		}
	case domain.PolicyOrderSizeLimit:
		return []domain.LimitValue{{Kind: domain.KindMaxQuantity, Value: "1"}}
	case domain.PolicyPnlBoundsKillSwitch:
		return []domain.LimitValue{{Kind: domain.KindLowerBound, Value: "0"}}
	default:
		return nil
	}
}

// ListAudit returns the most recent count audit rows aggregated across all
// nodes, newest first.
func (s *Service) ListAudit(
	ctx context.Context, count int,
) ([]domain.AuditRow, error) {
	rows := make([]domain.AuditRow, 0)
	for i, n := range s.router.All() {
		part, err := n.ListAudit(ctx, count)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list audit: %w", i, err)
		}
		rows = append(rows, part...)
	}
	return rows, nil
}

// ListAuditFiltered returns audit entries, newest first, optionally narrowed
// to an account and/or a source. The node surface exposes no audit filters, so
// the rows are aggregated and filtered here. count is the fetch limit passed to
// each node; the filtered result may be smaller. An empty account or source
// disables that filter.
func (s *Service) ListAuditFiltered(
	ctx context.Context, account domain.AccountID, source domain.Source, count int,
) ([]domain.AuditRow, error) {
	rows, err := s.ListAudit(ctx, count)
	if err != nil {
		return nil, err
	}
	if account == "" && source == "" {
		return rows, nil
	}
	out := make([]domain.AuditRow, 0, len(rows))
	for _, row := range rows {
		if account != "" && row.Account != account {
			continue
		}
		if source != "" && row.Source != source {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

// --- MCP access control -----------------------------------------------------

// McpCommand is one MCP command's catalogue metadata paired with its resolved
// effective enabled state, for the operator panel. It is the surface-facing
// view the HTTP layer maps onto its wire DTO.
type McpCommand struct {
	// Command is the catalogue entry (identity, agent description, risk flags,
	// implemented flag, default enabled state).
	Command mcpcatalog.Command
	// Enabled is the resolved effective state: the stored override when present,
	// otherwise the catalogue default.
	Enabled bool
}

// MarketDataFreshnessTTL is the quote freshness threshold surfaced to the panel.
// A quote older than this is flagged as stale (Stale == true in the status) but
// is still returned so the last known price remains visible. Configurable
// freshness is not yet implemented; it is the single market-data freshness
// window (marketdata.FreshnessTTL) shared with engine.MarketDataFreshnessTTL.
const MarketDataFreshnessTTL = marketdata.FreshnessTTL

// MarketDataProvider is one built-in provider type the operator can configure.
type MarketDataProvider struct {
	Type  string
	Title string
}

// MarketDataInstrumentStatus is one configured instrument plus its latest quote
// snapshot, when one has been received. Stale is only true when both the source
// and instrument are enabled. UpdateInterval, when non-nil, is the elapsed time
// between the two most recent ticks of this instrument's quote, as observed by
// the running manager; it is nil until a second tick has arrived since the last
// (re)subscribe.
type MarketDataInstrumentStatus struct {
	Instrument     domain.MarketDataInstrument
	Quote          *domain.MarketDataQuote
	UpdateInterval *time.Duration
	Stale          bool
}

// MarketDataInstanceStatus is one configured source, all of its instruments,
// and its current subscription state. State is one of "disabled", "pending",
// "ok", or "error" (empty when no runtime is wired); Error carries the short
// operator-facing message when State is "error". References is nil when the
// connector exposes no help links. VerifiesSymbols is true when the provider
// can check whether an external symbol exists in its catalogue. SearchesSymbols
// is true when the provider can search its catalogue for matching instruments.
type MarketDataInstanceStatus struct {
	Instance        domain.MarketDataInstance
	Instruments     []MarketDataInstrumentStatus
	References      *marketdata.ProviderReferences
	VerifiesSymbols bool
	SearchesSymbols bool
	State           string
	Error           string
	Diagnostics     []marketdata.Diagnostic
}

// MarketDataStatus is the operator-facing market-data control-plane snapshot.
type MarketDataStatus struct {
	Providers        []MarketDataProvider
	Instances        []MarketDataInstanceStatus
	FreshnessSeconds int
	RestartRequired  bool
}

// ListMcpAccess returns the full MCP command catalogue, each entry paired with
// its effective enabled state. The stored per-command overrides are read from
// the group node (MCP access is a control-plane-wide setting, not
// account-scoped) and merged over the catalogue defaults so every command
// resolves to a bool.
func (s *Service) ListMcpAccess(ctx context.Context) ([]McpCommand, error) {
	n, err := s.groupNode()
	if err != nil {
		return nil, err
	}
	stored, err := n.ListMcpAccess(ctx)
	if err != nil {
		return nil, fmt.Errorf("backend: list mcp access: %w", err)
	}
	effective := mcpcatalog.Effective(stored)
	catalogue := mcpcatalog.All()
	out := make([]McpCommand, 0, len(catalogue))
	for _, cmd := range catalogue {
		out = append(out, McpCommand{Command: cmd, Enabled: effective[cmd.Name]})
	}
	return out, nil
}

// CommandEnabled resolves the effective enabled state of one MCP command: the
// stored override when present, otherwise the catalogue default. An unknown
// command resolves to false. It is the read the MCP surface consults to gate a
// tool call.
func (s *Service) CommandEnabled(ctx context.Context, command string) (bool, error) {
	n, err := s.groupNode()
	if err != nil {
		return false, err
	}
	stored, err := n.ListMcpAccess(ctx)
	if err != nil {
		return false, fmt.Errorf("backend: read mcp access: %w", err)
	}
	return mcpcatalog.EnabledFor(command, stored), nil
}

// SetMcpAccess validates the command against the catalogue and upserts its
// enabled state on the group node. An unknown command is rejected with
// domain.ErrNotFound so the surface can map it onto a 404.
func (s *Service) SetMcpAccess(
	ctx context.Context, command string, enabled bool,
) error {
	if _, ok := mcpcatalog.Lookup(command); !ok {
		return fmt.Errorf("mcp command %q: %w", command, domain.ErrNotFound)
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.SetMcpAccess(ctx, command, enabled, auth.CallerFromContext(ctx))
}

// ListMarketData returns configured providers, instances, instruments, and the
// latest quote snapshot for each configured instrument.
func (s *Service) ListMarketData(ctx context.Context) (MarketDataStatus, error) {
	n, err := s.groupNode()
	if err != nil {
		return MarketDataStatus{}, err
	}
	instances, err := n.ListMarketDataInstances(ctx)
	if err != nil {
		return MarketDataStatus{}, fmt.Errorf("backend: list market-data instances: %w", err)
	}
	quotes, err := n.ListMarketDataQuotes(ctx, "")
	if err != nil {
		return MarketDataStatus{}, fmt.Errorf("backend: list market-data quotes: %w", err)
	}
	quoteByInstrument := make(map[string]domain.MarketDataQuote, len(quotes))
	for _, quote := range quotes {
		quoteByInstrument[marketDataKey(quote.InstanceID, quote.ExternalSymbol)] = quote
	}

	var runtimeStatuses map[string]marketdata.InstanceRuntimeStatus
	var appliedConfig map[string]marketdata.AppliedInstanceConfig
	if s.md != nil {
		runtimeStatuses = s.md.InstanceStatuses()
		appliedConfig = s.md.AppliedConfig()
	}

	now := time.Now().UTC()
	statuses := make([]MarketDataInstanceStatus, 0, len(instances))
	currentConfig := make(map[string]marketdata.AppliedInstanceConfig, len(instances))
	for _, instance := range instances {
		instruments, err := n.ListMarketDataInstruments(ctx, instance.ID)
		if err != nil {
			return MarketDataStatus{}, fmt.Errorf("backend: list market-data instruments: %w", err)
		}
		if instance.Enabled {
			currentConfig[instance.ID] = marketDataAppliedConfig(instance, instruments)
		}
		instStatuses := make([]MarketDataInstrumentStatus, 0, len(instruments))
		for _, instrument := range instruments {
			var quotePtr *domain.MarketDataQuote
			if quote, ok := quoteByInstrument[marketDataKey(instrument.InstanceID, instrument.ExternalSymbol)]; ok {
				q := quote
				quotePtr = &q
			}
			stale := marketDataInstrumentStale(
				instance.Enabled, instrument, quotePtr, now,
			)
			var interval *time.Duration
			if s.md != nil {
				if d, ok := s.md.QuoteUpdateInterval(
					instance.ID, instrument.ExternalSymbol,
				); ok {
					interval = &d
				}
			}
			instStatuses = append(instStatuses, MarketDataInstrumentStatus{
				Instrument:     instrument,
				Quote:          quotePtr,
				UpdateInterval: interval,
				Stale:          stale,
			})
		}
		rt := runtimeStatuses[instance.ID]
		state, errMsg := marketDataInstanceState(instance, s.md, runtimeStatuses)
		statuses = append(statuses, MarketDataInstanceStatus{
			Instance:        instance,
			Instruments:     instStatuses,
			References:      rt.References,
			VerifiesSymbols: marketdata.ProviderVerifiesSymbols(instance.Type),
			SearchesSymbols: marketdata.ProviderSearchesSymbols(instance.Type),
			State:           state,
			Error:           errMsg,
			Diagnostics:     rt.Diagnostics,
		})
	}
	return MarketDataStatus{
		Providers:        marketDataProviders(),
		Instances:        statuses,
		FreshnessSeconds: int(MarketDataFreshnessTTL.Seconds()),
		RestartRequired:  marketDataRestartRequired(s.md, currentConfig, appliedConfig),
	}, nil
}

// RestartMarketData re-applies the market-data configuration by stopping and
// restarting the connector manager. With no runtime wired it is a no-op.
func (s *Service) RestartMarketData(ctx context.Context) error {
	if s.md == nil {
		return nil
	}
	return s.md.Restart()
}

// MarketDataSymbolVerification is the outcome of a one-shot symbol check for
// one instance. Supported is false when the instance's provider cannot verify
// symbols, in which case Exists and Suggestion are zero and the call still
// succeeds. Details carries optional provider metadata for the matched symbol.
// Suggestion is a case-folded catalogue variant when the symbol was not found
// as typed.
type MarketDataSymbolVerification struct {
	Supported  bool
	Exists     bool
	Suggestion string
	Details    string
}

// VerifyMarketDataSymbol checks whether externalSymbol exists on the identified
// instance's provider. It loads the instance config and runs a stateless probe
// (a fresh connector that is never subscribed), so live feeds are untouched. A
// provider that cannot verify symbols yields Supported=false with no error; an
// error is reserved for an unknown instance or a catalogue-fetch failure.
func (s *Service) VerifyMarketDataSymbol(
	ctx context.Context, id, externalSymbol string,
) (MarketDataSymbolVerification, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return MarketDataSymbolVerification{}, fmt.Errorf("market-data instance id: %w", domain.ErrInvalid)
	}
	n, err := s.groupNode()
	if err != nil {
		return MarketDataSymbolVerification{}, err
	}
	instance, ok, err := n.GetMarketDataInstance(ctx, id)
	if err != nil {
		return MarketDataSymbolVerification{}, fmt.Errorf("backend: get market-data instance: %w", err)
	}
	if !ok {
		return MarketDataSymbolVerification{}, fmt.Errorf("market-data instance %q: %w", id, domain.ErrNotFound)
	}
	result, supported, err := marketdata.VerifySymbol(ctx, instance, strings.TrimSpace(externalSymbol))
	if err != nil {
		// A verify reaches the external provider; a provider/transport failure is
		// an expected operational condition, not a server bug, so it is surfaced
		// as upstream (502 + plain message) rather than a 500.
		return MarketDataSymbolVerification{}, fmt.Errorf(
			"%w: verify market-data symbol: %v", domain.ErrUpstream, err)
	}
	return MarketDataSymbolVerification{
		Supported:  supported,
		Exists:     result.Exists,
		Suggestion: result.Suggestion,
		Details:    result.Details,
	}, nil
}

// MarketDataSymbolMatch is one contract a symbol resolve returned. Name is the
// provider long name when available (empty otherwise); the remaining fields
// carry the resolved contract specifics.
type MarketDataSymbolMatch struct {
	Symbol                       string
	Name                         string
	SecType                      string
	Exchange                     string
	PrimaryExchange              string
	Currency                     string
	LastTradeDateOrContractMonth string
	Right                        string
	Multiplier                   string
	LocalSymbol                  string
	TradingClass                 string
	ConID                        string
	Strike                       string
}

// MarketDataSymbolSearch is the outcome of a symbol search for one instance.
// Supported is false when the instance's provider cannot search symbols, in
// which case Matches is empty and the call still succeeds.
type MarketDataSymbolSearch struct {
	Supported bool
	Matches   []MarketDataSymbolMatch
}

// MarketDataSymbolSearchInput carries the resolve criteria for one search: the
// typed Query (the symbol) plus the contract specifics that scope the resolve.
// Empty optional fields apply no constraint.
type MarketDataSymbolSearchInput struct {
	Query                        string
	SecType                      string
	Exchange                     string
	Currency                     string
	LastTradeDateOrContractMonth string
	Right                        string
	Strike                       string
}

// SearchMarketDataSymbols resolves the identified instance's provider catalogue
// for contracts matching the input criteria. It loads the instance config and
// runs a stateless probe (a fresh connector that is never subscribed), so live
// feeds are untouched. A provider that cannot search symbols yields
// Supported=false with no error; an error is reserved for an unknown instance or
// a transport failure. No matches is an empty slice with Supported=true.
func (s *Service) SearchMarketDataSymbols(
	ctx context.Context, id string, input MarketDataSymbolSearchInput,
) (MarketDataSymbolSearch, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return MarketDataSymbolSearch{}, fmt.Errorf("market-data instance id: %w", domain.ErrInvalid)
	}
	n, err := s.groupNode()
	if err != nil {
		return MarketDataSymbolSearch{}, err
	}
	instance, ok, err := n.GetMarketDataInstance(ctx, id)
	if err != nil {
		return MarketDataSymbolSearch{}, fmt.Errorf("backend: get market-data instance: %w", err)
	}
	if !ok {
		return MarketDataSymbolSearch{}, fmt.Errorf("market-data instance %q: %w", id, domain.ErrNotFound)
	}
	matches, supported, err := marketdata.SearchSymbols(ctx, instance, marketdata.SymbolSearchQuery{
		Query:                        strings.TrimSpace(input.Query),
		SecType:                      strings.TrimSpace(input.SecType),
		Exchange:                     strings.TrimSpace(input.Exchange),
		Currency:                     strings.TrimSpace(input.Currency),
		LastTradeDateOrContractMonth: strings.TrimSpace(input.LastTradeDateOrContractMonth),
		Right:                        strings.TrimSpace(input.Right),
		Strike:                       input.Strike,
	})
	if err != nil {
		// Like verify, a resolve hits the external provider; a provider/transport
		// failure is upstream (502 + plain message), not a 500.
		return MarketDataSymbolSearch{}, fmt.Errorf(
			"%w: search market-data symbols: %v", domain.ErrUpstream, err)
	}
	out := MarketDataSymbolSearch{Supported: supported}
	if len(matches) > 0 {
		out.Matches = make([]MarketDataSymbolMatch, 0, len(matches))
		for _, match := range matches {
			out.Matches = append(out.Matches, MarketDataSymbolMatch{
				Symbol:                       match.Symbol,
				Name:                         match.Name,
				SecType:                      match.SecType,
				Exchange:                     match.Exchange,
				PrimaryExchange:              match.PrimaryExchange,
				Currency:                     match.Currency,
				LastTradeDateOrContractMonth: match.LastTradeDateOrContractMonth,
				Right:                        match.Right,
				Multiplier:                   match.Multiplier,
				LocalSymbol:                  match.LocalSymbol,
				TradingClass:                 match.TradingClass,
				ConID:                        match.ConID,
				Strike:                       match.Strike,
			})
		}
	}
	return out, nil
}

// marketDataInstanceState resolves one instance's operator-facing state: a
// disabled instance is "disabled"; with no runtime wired the state is empty; an
// enabled instance the manager has applied reports the manager's state (and
// error message on error); an enabled instance the manager has not yet applied
// is "pending", signalling the operator must restart.
func marketDataInstanceState(
	instance domain.MarketDataInstance,
	md MarketDataRuntime,
	runtimeStatuses map[string]marketdata.InstanceRuntimeStatus,
) (string, string) {
	if !instance.Enabled {
		return "disabled", ""
	}
	if md == nil {
		return "", ""
	}
	status, ok := runtimeStatuses[instance.ID]
	if !ok {
		return "pending", ""
	}
	if status.State == marketdata.StateError {
		return status.State, status.Error
	}
	return status.State, ""
}

func marketDataAppliedConfig(
	instance domain.MarketDataInstance,
	instruments []domain.MarketDataInstrument,
) marketdata.AppliedInstanceConfig {
	subs := make([]marketdata.Subscription, 0, len(instruments))
	for _, instrument := range instruments {
		if !instrument.Enabled {
			continue
		}
		subs = append(subs, marketdata.Subscription{
			External: instrument.ExternalSymbol,
			Base:     instrument.BaseAsset,
			Quote:    instrument.QuoteAsset,
		})
	}
	return marketdata.AppliedInstanceConfig{
		Type:          instance.Type,
		Subscriptions: subs,
	}
}

func marketDataRestartRequired(
	md MarketDataRuntime,
	current, applied map[string]marketdata.AppliedInstanceConfig,
) bool {
	if md == nil {
		return false
	}
	if len(current) != len(applied) {
		return true
	}
	for id, currentConfig := range current {
		appliedConfig, ok := applied[id]
		if !ok {
			return true
		}
		if currentConfig.Type != appliedConfig.Type {
			return true
		}
		if !sameMarketDataSubscriptions(
			currentConfig.Subscriptions, appliedConfig.Subscriptions,
		) {
			return true
		}
	}
	return false
}

func sameMarketDataSubscriptions(a, b []marketdata.Subscription) bool {
	if len(a) != len(b) {
		return false
	}
	left := marketDataSubscriptionKeys(a)
	right := marketDataSubscriptionKeys(b)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func marketDataSubscriptionKeys(subs []marketdata.Subscription) []string {
	keys := make([]string, 0, len(subs))
	for _, sub := range subs {
		keys = append(keys, sub.External+"\x00"+sub.Base+"\x00"+sub.Quote)
	}
	sort.Strings(keys)
	return keys
}

// CreateMarketDataInstance validates and persists one source instance.
func (s *Service) CreateMarketDataInstance(
	ctx context.Context, instance domain.MarketDataInstance,
) error {
	instance.Type = strings.TrimSpace(instance.Type)
	instance.Label = strings.TrimSpace(instance.Label)
	instance.Credentials = strings.TrimSpace(instance.Credentials)
	title, ok := marketDataProviderTitle(instance.Type)
	if !ok {
		return fmt.Errorf("market-data provider %q: %w", instance.Type, domain.ErrInvalid)
	}
	if instance.Label == "" {
		instance.Label = title
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	instances, err := n.ListMarketDataInstances(ctx)
	if err != nil {
		return fmt.Errorf("backend: list market-data instances: %w", err)
	}
	if marketDataLabelTaken(instances, instance.Label) {
		return fmt.Errorf("market-data source label %q: %w", instance.Label, domain.ErrAlreadyExists)
	}
	instance.ID, err = newMarketDataInstanceID(instance.Type)
	if err != nil {
		return err
	}
	if err := validateMarketDataInstance(instance); err != nil {
		return err
	}
	return n.CreateMarketDataInstance(ctx, instance, auth.CallerFromContext(ctx))
}

// SetMarketDataInstanceEnabled toggles one source instance.
func (s *Service) SetMarketDataInstanceEnabled(
	ctx context.Context, id string, enabled bool,
) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("market-data instance id: %w", domain.ErrInvalid)
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.SetMarketDataInstanceEnabled(ctx, id, enabled, auth.CallerFromContext(ctx))
}

// UpdateMarketDataInstanceSettings validates and persists editable source
// settings. Empty values in the credentials update preserve existing keys so
// the UI can submit blank password fields without fetching stored secrets.
func (s *Service) UpdateMarketDataInstanceSettings(
	ctx context.Context, id, label, credentials string,
) error {
	id = strings.TrimSpace(id)
	label = strings.TrimSpace(label)
	credentials = strings.TrimSpace(credentials)
	if id == "" {
		return fmt.Errorf("market-data instance id: %w", domain.ErrInvalid)
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	instance, ok, err := n.GetMarketDataInstance(ctx, id)
	if err != nil {
		return fmt.Errorf("backend: get market-data instance: %w", err)
	}
	if !ok {
		return fmt.Errorf("market-data instance %q: %w", id, domain.ErrNotFound)
	}
	if label == "" {
		label = instance.Label
	}
	instances, err := n.ListMarketDataInstances(ctx)
	if err != nil {
		return fmt.Errorf("backend: list market-data instances: %w", err)
	}
	if marketDataLabelTakenExcept(instances, id, label) {
		return fmt.Errorf("market-data source label %q: %w", label, domain.ErrAlreadyExists)
	}
	mergedCredentials, err := mergeMarketDataCredentials(instance.Credentials, credentials)
	if err != nil {
		return err
	}
	instance.Label = label
	instance.Credentials = mergedCredentials
	if err := validateMarketDataInstance(instance); err != nil {
		return err
	}
	return n.UpdateMarketDataInstanceSettings(
		ctx, id, instance.Label, instance.Credentials, auth.CallerFromContext(ctx),
	)
}

// DeleteMarketDataInstance removes one source instance and its instruments.
func (s *Service) DeleteMarketDataInstance(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("market-data instance id: %w", domain.ErrInvalid)
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.DeleteMarketDataInstance(ctx, id, auth.CallerFromContext(ctx))
}

// UpsertMarketDataInstrument validates and persists one instrument mapping,
// then pushes its operator-set manual mark once into the running instance. The
// push is a no-op for streaming providers and for instruments without a manual
// price (see MarketDataRuntime.PushManual), so only a manual instrument with a
// price reaches the engine, and exactly once per upsert.
func (s *Service) UpsertMarketDataInstrument(
	ctx context.Context, instrument domain.MarketDataInstrument,
) error {
	instrument.InstanceID = strings.TrimSpace(instrument.InstanceID)
	instrument.ExternalSymbol = strings.TrimSpace(instrument.ExternalSymbol)
	instrument.BaseAsset = strings.TrimSpace(instrument.BaseAsset)
	instrument.QuoteAsset = strings.TrimSpace(instrument.QuoteAsset)
	instrument.ManualPrice = strings.TrimSpace(instrument.ManualPrice)
	if err := validateMarketDataInstrument(instrument); err != nil {
		return err
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	if err := n.UpsertMarketDataInstrument(ctx, instrument, auth.CallerFromContext(ctx)); err != nil {
		return err
	}
	if s.md != nil {
		s.md.PushManual(instrument.InstanceID, instrument)
	}
	return nil
}

// SetMarketDataInstrumentEnabled toggles one instrument mapping.
func (s *Service) SetMarketDataInstrumentEnabled(
	ctx context.Context, instanceID, externalSymbol string, enabled bool,
) error {
	instanceID = strings.TrimSpace(instanceID)
	externalSymbol = strings.TrimSpace(externalSymbol)
	if instanceID == "" || externalSymbol == "" {
		return fmt.Errorf("market-data instrument: %w", domain.ErrInvalid)
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.SetMarketDataInstrumentEnabled(
		ctx, instanceID, externalSymbol, enabled, auth.CallerFromContext(ctx),
	)
}

// DeleteMarketDataInstrument removes one instrument mapping.
func (s *Service) DeleteMarketDataInstrument(
	ctx context.Context, instanceID, externalSymbol string,
) error {
	instanceID = strings.TrimSpace(instanceID)
	externalSymbol = strings.TrimSpace(externalSymbol)
	if instanceID == "" || externalSymbol == "" {
		return fmt.Errorf("market-data instrument: %w", domain.ErrInvalid)
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.DeleteMarketDataInstrument(
		ctx, instanceID, externalSymbol, auth.CallerFromContext(ctx),
	)
}

func marketDataProviders() []MarketDataProvider {
	return []MarketDataProvider{
		{Type: domain.MarketDataProviderIB, Title: "Interactive Brokers"},
		{Type: domain.MarketDataProviderBinance, Title: "Binance"},
		{Type: domain.MarketDataProviderKraken, Title: "Kraken"},
		{Type: domain.MarketDataProviderCoinbase, Title: "Coinbase"},
		{Type: domain.MarketDataProviderAlpaca, Title: "Alpaca"},
		{Type: domain.MarketDataProviderOKX, Title: "OKX"},
		{Type: domain.MarketDataProviderBybit, Title: "Bybit"},
		{Type: domain.MarketDataProviderOANDA, Title: "OANDA"},
		{Type: domain.MarketDataProviderFinnhub, Title: "Finnhub"},
		{Type: domain.MarketDataProviderBYO, Title: "BYO"},
		{Type: domain.MarketDataProviderMock, Title: "Mock"},
	}
}

func marketDataProviderTitle(typ string) (string, bool) {
	for _, provider := range marketDataProviders() {
		if typ == provider.Type {
			return provider.Title, true
		}
	}
	return "", false
}

func newMarketDataInstanceID(typ string) (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("market-data instance id: %w", err)
	}
	return "md-" + typ + "-" + hex.EncodeToString(buf[:]), nil
}

func marketDataLabelTaken(instances []domain.MarketDataInstance, label string) bool {
	label = strings.TrimSpace(label)
	for _, instance := range instances {
		if strings.EqualFold(strings.TrimSpace(instance.Label), label) {
			return true
		}
	}
	return false
}

func marketDataLabelTakenExcept(
	instances []domain.MarketDataInstance, id, label string,
) bool {
	label = strings.TrimSpace(label)
	for _, instance := range instances {
		if instance.ID == id {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(instance.Label), label) {
			return true
		}
	}
	return false
}

func mergeMarketDataCredentials(existing, update string) (string, error) {
	existing = strings.TrimSpace(existing)
	update = strings.TrimSpace(update)
	if update == "" {
		return existing, nil
	}
	merged := make(map[string]any)
	if existing != "" {
		if err := json.Unmarshal([]byte(existing), &merged); err != nil {
			return "", fmt.Errorf("market-data stored credentials: %w", domain.ErrInvalid)
		}
	}
	var incoming map[string]any
	if err := json.Unmarshal([]byte(update), &incoming); err != nil {
		return "", fmt.Errorf("market-data credentials: %w", domain.ErrInvalid)
	}
	for key, value := range incoming {
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			if _, exists := merged[key]; exists {
				continue
			}
		}
		merged[key] = value
	}
	if len(merged) == 0 {
		return "", nil
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("market-data credentials: %w", err)
	}
	return string(out), nil
}

func validateMarketDataInstance(instance domain.MarketDataInstance) error {
	if instance.ID == "" {
		return fmt.Errorf("market-data instance id: %w", domain.ErrInvalid)
	}
	if instance.Label == "" {
		return fmt.Errorf("market-data source label: %w", domain.ErrInvalid)
	}
	if !marketDataProviderKnown(instance.Type) {
		return fmt.Errorf("market-data provider %q: %w", instance.Type, domain.ErrInvalid)
	}
	if instance.Credentials != "" && !json.Valid([]byte(instance.Credentials)) {
		return fmt.Errorf("market-data credentials: %w", domain.ErrInvalid)
	}
	if err := marketdata.ValidateProviderConfig(instance); err != nil {
		return fmt.Errorf("market-data credentials: %v: %w", err, domain.ErrInvalid)
	}
	return nil
}

func validateMarketDataInstrument(instrument domain.MarketDataInstrument) error {
	if instrument.InstanceID == "" || instrument.ExternalSymbol == "" {
		return fmt.Errorf("market-data instrument: %w", domain.ErrInvalid)
	}
	if err := domain.ValidateAsset(instrument.BaseAsset); err != nil {
		return err
	}
	if err := domain.ValidateAsset(instrument.QuoteAsset); err != nil {
		return err
	}
	if err := domain.ValidateMarketDataMark(instrument.ManualPrice); err != nil {
		return err
	}
	return nil
}

func marketDataProviderKnown(typ string) bool {
	for _, provider := range marketDataProviders() {
		if typ == provider.Type {
			return true
		}
	}
	return false
}

func marketDataInstrumentStale(
	instanceEnabled bool,
	instrument domain.MarketDataInstrument,
	quote *domain.MarketDataQuote,
	now time.Time,
) bool {
	if !instanceEnabled || !instrument.Enabled {
		return false
	}
	if quote == nil {
		return true
	}
	return now.Sub(quote.AsOf) > MarketDataFreshnessTTL
}

func marketDataKey(instanceID, externalSymbol string) string {
	return instanceID + "\x00" + externalSymbol
}

// --- Account group and notes -----------------------------------------------

// SetAccountGroup validates the account id, routes to the owning node, and sets
// or clears (empty groupID) the account's group membership.
func (s *Service) SetAccountGroup(
	ctx context.Context, id domain.AccountID, groupID string,
) error {
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	if groupID != "" {
		if err := domain.ValidateGroupID(groupID); err != nil {
			return err
		}
	}
	n, err := s.router.Route(keyFor(id))
	if err != nil {
		return fmt.Errorf("backend: route account: %w", err)
	}
	return n.SetAccountGroup(ctx, keyFor(id), groupID, auth.CallerFromContext(ctx))
}

// SetAccountNotes validates the account id and notes, routes to the owning
// node, and replaces the account's free-form notes.
func (s *Service) SetAccountNotes(
	ctx context.Context, id domain.AccountID, notes string,
) error {
	if err := domain.ValidateAccountID(id); err != nil {
		return err
	}
	if err := domain.ValidateNotes(notes); err != nil {
		return err
	}
	n, err := s.router.Route(keyFor(id))
	if err != nil {
		return fmt.Errorf("backend: route account: %w", err)
	}
	return n.SetAccountNotes(ctx, keyFor(id), notes, auth.CallerFromContext(ctx))
}

// --- Groups ----------------------------------------------------------------

// groupNode resolves the node that owns the default tenant's groups. Groups are
// a tenant-level concern; in the single-node deployment one node owns them. It
// routes via an empty account key, which the local router always owns.
func (s *Service) groupNode() (node.Node, error) {
	n, err := s.router.Route(keyFor(""))
	if err != nil {
		return nil, fmt.Errorf("backend: route group: %w", err)
	}
	return n, nil
}

// CreateGroup validates the id and notes, defaults the tenant, and creates the
// group.
func (s *Service) CreateGroup(
	ctx context.Context, group domain.AccountGroup,
) error {
	group.Tenant = domain.DefaultTenant
	if err := domain.ValidateGroupID(group.ID); err != nil {
		return err
	}
	if err := domain.ValidateNotes(group.Notes); err != nil {
		return err
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.CreateGroup(ctx, group, auth.CallerFromContext(ctx))
}

// ListGroups returns every group in the default tenant.
func (s *Service) ListGroups(ctx context.Context) ([]domain.AccountGroup, error) {
	n, err := s.groupNode()
	if err != nil {
		return nil, err
	}
	groups, err := n.ListGroups(ctx, domain.DefaultTenant)
	if err != nil {
		return nil, fmt.Errorf("backend: list groups: %w", err)
	}
	return groups, nil
}

// GetGroup validates the id and returns the group and its member accounts. It
// maps a missing group onto domain.ErrNotFound.
func (s *Service) GetGroup(
	ctx context.Context, id string,
) (domain.AccountGroup, []domain.Account, error) {
	if err := domain.ValidateGroupID(id); err != nil {
		return domain.AccountGroup{}, nil, err
	}
	n, err := s.groupNode()
	if err != nil {
		return domain.AccountGroup{}, nil, err
	}
	group, accounts, ok, err := n.GetGroup(ctx, domain.DefaultTenant, id)
	if err != nil {
		return domain.AccountGroup{}, nil, fmt.Errorf("backend: get group: %w", err)
	}
	if !ok {
		return domain.AccountGroup{}, nil, fmt.Errorf("group %q: %w", id, domain.ErrNotFound)
	}
	return group, accounts, nil
}

// SetGroupNotes validates the id and notes and replaces the group's notes.
func (s *Service) SetGroupNotes(ctx context.Context, id, notes string) error {
	if err := domain.ValidateGroupID(id); err != nil {
		return err
	}
	if err := domain.ValidateNotes(notes); err != nil {
		return err
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.SetGroupNotes(ctx, domain.DefaultTenant, id, notes, auth.CallerFromContext(ctx))
}

// SetGroupBlocked validates the id and blocks or unblocks the group with
// reason.
func (s *Service) SetGroupBlocked(
	ctx context.Context, id string, blocked bool, reason string,
) error {
	if err := domain.ValidateGroupID(id); err != nil {
		return err
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.SetGroupBlocked(ctx, domain.DefaultTenant, id, blocked, reason, auth.CallerFromContext(ctx))
}

// DeleteGroup validates the id and removes the group.
func (s *Service) DeleteGroup(ctx context.Context, id string) error {
	if err := domain.ValidateGroupID(id); err != nil {
		return err
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.DeleteGroup(ctx, domain.DefaultTenant, id, auth.CallerFromContext(ctx))
}

// --- Spot funds ------------------------------------------------------------

// ApplyAdjustment validates the account id and asset, routes to the owning
// node, and applies one spot-funds adjustment. The returned record carries the
// accepted-or-rejected outcome; a policy reject is a successful call, not an
// error. No existence check is performed on the account: the engine creates the
// balance on first adjustment.
func (s *Service) ApplyAdjustment(
	ctx context.Context, account domain.AccountID, req domain.AdjustmentRequest,
) (domain.AccountAdjustmentRecord, error) {
	if err := domain.ValidateAccountID(account); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	if err := domain.ValidateAdjustmentRequest(req); err != nil {
		return domain.AccountAdjustmentRecord{}, err
	}
	n, err := s.router.Route(keyFor(account))
	if err != nil {
		return domain.AccountAdjustmentRecord{}, fmt.Errorf("backend: route account: %w", err)
	}
	return n.ApplyAdjustment(ctx, keyFor(account), req, auth.CallerFromContext(ctx))
}

// ListBalances returns the balance rows for the default tenant, optionally
// narrowed to a non-empty account and/or asset. It aggregates across nodes.
func (s *Service) ListBalances(
	ctx context.Context, account domain.AccountID, asset string,
) ([]domain.Balance, error) {
	balances := make([]domain.Balance, 0)
	for i, n := range s.router.All() {
		part, err := n.ListBalances(ctx, domain.DefaultTenant, account, asset)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list balances: %w", i, err)
		}
		balances = append(balances, part...)
	}
	return balances, nil
}

// ListAdjustments validates the account id and returns the most recent n
// adjustments for the account, newest first; an empty source returns all.
func (s *Service) ListAdjustments(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.AccountAdjustmentRecord, error) {
	if err := domain.ValidateAccountID(account); err != nil {
		return nil, err
	}
	target, err := s.router.Route(keyFor(account))
	if err != nil {
		return nil, fmt.Errorf("backend: route account: %w", err)
	}
	return target.ListAdjustments(ctx, domain.DefaultTenant, account, source, n)
}

// ListAllAdjustments returns the most recent n adjustments aggregated across
// all nodes and accounts, optionally narrowed to a non-empty account and/or
// source.
// It backs GET /adjustments. Per-node results are already newest-first; the
// merged slice is sorted newest-first and bounded to n.
func (s *Service) ListAllAdjustments(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.AccountAdjustmentRecord, error) {
	if account != "" {
		if err := domain.ValidateAccountID(account); err != nil {
			return nil, err
		}
		return s.ListAdjustments(ctx, account, source, n)
	}
	recs := make([]domain.AccountAdjustmentRecord, 0)
	for i, target := range s.router.All() {
		part, err := target.ListAdjustments(ctx, domain.DefaultTenant, "", source, n)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list adjustments: %w", i, err)
		}
		recs = append(recs, part...)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].ID > recs[j].ID })
	if n > 0 && len(recs) > n {
		recs = recs[:n]
	}
	return recs, nil
}

// --- Trading ---------------------------------------------------------------

// SubmitOrder validates the order's account and assets, routes to the owning
// node, and runs the engine pre-trade. The returned order carries the recorded
// lifecycle status, including a rejected status; an engine reject is a
// successful call, not an error.
func (s *Service) SubmitOrder(
	ctx context.Context, o domain.Order,
) (domain.Order, error) {
	o.Tenant = domain.DefaultTenant
	// Officer validates only the boundary id/asset formats; the engine enforces
	// the real trading rules. Existence is never checked: any well-formed account
	// or asset is accepted (existing or not), per the surface contract.
	if err := domain.ValidateAccountID(o.Account); err != nil {
		return domain.Order{}, err
	}
	if err := domain.ValidateAsset(o.BaseAsset); err != nil {
		return domain.Order{}, err
	}
	if err := domain.ValidateAsset(o.QuoteAsset); err != nil {
		return domain.Order{}, err
	}
	n, err := s.router.Route(keyFor(o.Account))
	if err != nil {
		return domain.Order{}, fmt.Errorf("backend: route order: %w", err)
	}
	return n.SubmitOrder(ctx, keyFor(o.Account), o, auth.CallerFromContext(ctx))
}

// CheckOrder validates the probe's account and assets, routes to the owning
// node, and runs the engine pre-trade as a non-mutating dry-run. It mutates no
// state and writes no audit row; an engine reject is a successful call carrying
// the reasons, not an error.
func (s *Service) CheckOrder(
	ctx context.Context, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	// Officer validates only the boundary id/asset formats; the engine enforces
	// the real trading rules. Existence is never checked, mirroring SubmitOrder.
	if err := domain.ValidateAccountID(probe.Account); err != nil {
		return domain.CheckResult{}, err
	}
	if err := domain.ValidateAsset(probe.BaseAsset); err != nil {
		return domain.CheckResult{}, err
	}
	if err := domain.ValidateAsset(probe.QuoteAsset); err != nil {
		return domain.CheckResult{}, err
	}
	n, err := s.router.Route(keyFor(probe.Account))
	if err != nil {
		return domain.CheckResult{}, fmt.Errorf("backend: route check: %w", err)
	}
	return n.CheckOrder(ctx, keyFor(probe.Account), probe)
}

// ApplyExecutionReport validates the fill's account and assets, routes to the
// owning node, and settles the fill through the engine.
func (s *Service) ApplyExecutionReport(
	ctx context.Context, in domain.ExecutionReportInput,
) (engine.ExecutionReportResult, error) {
	if err := domain.ValidateAccountID(in.Account); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	if err := domain.ValidateAsset(in.BaseAsset); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	if err := domain.ValidateAsset(in.QuoteAsset); err != nil {
		return engine.ExecutionReportResult{}, err
	}
	n, err := s.router.Route(keyFor(in.Account))
	if err != nil {
		return engine.ExecutionReportResult{}, fmt.Errorf("backend: route report: %w", err)
	}
	return n.ApplyExecutionReport(ctx, keyFor(in.Account), in, auth.CallerFromContext(ctx))
}

// GetOrder returns the order with its events and trades. It maps a missing
// order onto the node's domain.ErrNotFound.
func (s *Service) GetOrder(
	ctx context.Context, id int64,
) (domain.OrderDetail, error) {
	n, err := s.router.Route(keyFor(""))
	if err != nil {
		return domain.OrderDetail{}, fmt.Errorf("backend: route order: %w", err)
	}
	return n.GetOrder(ctx, domain.DefaultTenant, id)
}

// ListOrders returns the most recent n orders, optionally narrowed to a
// non-empty account and/or source, newest first, aggregated across nodes.
func (s *Service) ListOrders(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.Order, error) {
	if account != "" {
		if err := domain.ValidateAccountID(account); err != nil {
			return nil, err
		}
	}
	orders := make([]domain.Order, 0)
	for i, target := range s.router.All() {
		part, err := target.ListOrders(ctx, domain.DefaultTenant, account, source, n)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list orders: %w", i, err)
		}
		orders = append(orders, part...)
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].ID > orders[j].ID })
	if n > 0 && len(orders) > n {
		orders = orders[:n]
	}
	return orders, nil
}

// ListTrades returns the most recent n trades, optionally narrowed to a
// non-empty account and/or source, newest first, aggregated across nodes.
func (s *Service) ListTrades(
	ctx context.Context, account domain.AccountID, source domain.Source, n int,
) ([]domain.Trade, error) {
	if account != "" {
		if err := domain.ValidateAccountID(account); err != nil {
			return nil, err
		}
	}
	trades := make([]domain.Trade, 0)
	for i, target := range s.router.All() {
		part, err := target.ListTrades(ctx, domain.DefaultTenant, account, source, n)
		if err != nil {
			return nil, fmt.Errorf("backend: node %d list trades: %w", i, err)
		}
		trades = append(trades, part...)
	}
	sort.Slice(trades, func(i, j int) bool { return trades[i].ID > trades[j].ID })
	if n > 0 && len(trades) > n {
		trades = trades[:n]
	}
	return trades, nil
}

// --- Dashboard / service ---------------------------------------------------

// Counts is the headline tally on the operator overview: how many accounts,
// groups, and risk barriers the deployment holds.
type Counts struct {
	// Accounts is the number of accounts across all nodes.
	Accounts int
	// Groups is the number of account groups.
	Groups int
	// Limits is the number of risk barriers.
	Limits int
	// OrdersToday is the number of orders recorded since the caller-supplied
	// today boundary, aggregated across nodes.
	OrdersToday int
	// OrdersTotal is the total number of orders recorded, aggregated across nodes.
	OrdersTotal int
}

// ActivityKind classifies one recent-activity entry on the overview feed.
type ActivityKind string

const (
	// ActivityKindAudit is a control-plane audit action.
	ActivityKindAudit ActivityKind = "audit"
	// ActivityKindOrder is an order submission.
	ActivityKindOrder ActivityKind = "order"
	// ActivityKindAdjustment is a spot-funds adjustment.
	ActivityKindAdjustment ActivityKind = "adjustment"
)

// Activity is one recent-activity entry, derived from the persisted log (audit
// rows, orders, adjustments) rather than any in-memory session registry. Newest
// entries come first.
type Activity struct {
	// At is when the underlying action was recorded.
	At time.Time
	// Source is the channel that originated the action.
	Source domain.Source
	// Kind classifies the entry (audit, order, adjustment).
	Kind ActivityKind
	// Ref is the human-readable reference (account id, order id).
	Ref string
	// Summary is a short description of the action.
	Summary string
}

// Overview is the operator dashboard summary: headline counts plus a recent
// activity feed attributed by source.
type Overview struct {
	// Counts are the headline tallies.
	Counts Counts
	// Activity is the recent-activity feed, newest first.
	Activity []Activity
}

// overviewActivityCap bounds the recent-activity feed assembled by Overview.
const overviewActivityCap = 20

// Overview assembles the operator dashboard summary: the counts of accounts,
// groups, barriers, and orders (today / total), and a source-attributed
// recent-activity feed merged from the most recent audit rows, orders, and
// adjustments. The feed is derived from the persisted log (newest first,
// capped), not an in-memory registry. The since boundary delimits "today" for
// the OrdersToday tally; the caller supplies it (request-local or server-local
// start-of-day).
func (s *Service) Overview(ctx context.Context, since time.Time) (Overview, error) {
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		return Overview{}, err
	}
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return Overview{}, err
	}
	limits, err := s.ListLimits(ctx, "")
	if err != nil {
		return Overview{}, err
	}

	var ordersTotal, ordersToday int
	for i, target := range s.router.All() {
		total, err := target.CountOrders(ctx, domain.DefaultTenant)
		if err != nil {
			return Overview{}, fmt.Errorf("backend: node %d count orders: %w", i, err)
		}
		today, err := target.CountOrdersSince(ctx, domain.DefaultTenant, since)
		if err != nil {
			return Overview{}, fmt.Errorf("backend: node %d count orders since: %w", i, err)
		}
		ordersTotal += total
		ordersToday += today
	}

	audit, err := s.ListAudit(ctx, overviewActivityCap)
	if err != nil {
		return Overview{}, err
	}
	orders, err := s.ListOrders(ctx, "", "", overviewActivityCap)
	if err != nil {
		return Overview{}, err
	}
	adjustments, err := s.ListAllAdjustments(ctx, "", "", overviewActivityCap)
	if err != nil {
		return Overview{}, err
	}

	activity := mergeActivity(audit, orders, adjustments)
	return Overview{
		Counts: Counts{
			Accounts:    len(accounts),
			Groups:      len(groups),
			Limits:      len(limits),
			OrdersToday: ordersToday,
			OrdersTotal: ordersTotal,
		},
		Activity: activity,
	}, nil
}

// mergeActivity folds recent audit rows, orders, and adjustments into one feed
// sorted newest-first by timestamp and bounded to overviewActivityCap.
func mergeActivity(
	audit []domain.AuditRow, orders []domain.Order, adjustments []domain.AccountAdjustmentRecord,
) []Activity {
	out := make([]Activity, 0, len(audit)+len(orders)+len(adjustments))
	for _, row := range audit {
		out = append(out, Activity{
			At:      row.At,
			Source:  row.Source,
			Kind:    ActivityKindAudit,
			Ref:     string(row.Account),
			Summary: row.Detail,
		})
	}
	for _, o := range orders {
		out = append(out, Activity{
			At:      o.At,
			Source:  o.Source,
			Kind:    ActivityKindOrder,
			Ref:     strconv.FormatInt(o.ID, 10),
			Summary: fmt.Sprintf("%s %s %s/%s %s", o.Side, o.AmountValue, o.BaseAsset, o.QuoteAsset, o.Status),
		})
	}
	for _, a := range adjustments {
		status := domain.AdjustmentStatusAccepted
		if a.Rejected != nil {
			status = domain.AdjustmentStatusRejected
		}
		out = append(out, Activity{
			At:      a.At,
			Source:  a.Source,
			Kind:    ActivityKindAdjustment,
			Ref:     string(a.Account),
			Summary: fmt.Sprintf("adjustment %s %s", a.Request.Asset, status),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > overviewActivityCap {
		out = out[:overviewActivityCap]
	}
	return out
}

// ServiceDatabase is the database facet of ServiceInfo: where the store lives
// and whether it answered its last probe.
type ServiceDatabase struct {
	// Path is the on-disk database location.
	Path string
	// Reachable reports whether the store responded to its last health probe.
	Reachable bool
}

// ServiceInfo is the static identity and build posture of the running service.
// Pit Officer is monolithic, so it reports one engine build profile and one
// database rather than per-node detail.
type ServiceInfo struct {
	// Name is the service name.
	Name string
	// EngineVersion is the engine SDK/runtime version.
	EngineVersion string
	// EngineBuildProfile is the engine build profile (for example "release").
	EngineBuildProfile string
	// Database is where the store lives and whether it is reachable.
	Database ServiceDatabase
	// Release reports whether the engine build profile is "release".
	Release bool
}

// ServiceInfo reports the service identity and build posture. The engine
// version, build profile, and the database path/reachability are sourced from
// the node health already gathered by Status; in the single-node deployment the
// first node supplies them.
func (s *Service) ServiceInfo(ctx context.Context) (ServiceInfo, error) {
	status, err := s.Status(ctx)
	if err != nil {
		return ServiceInfo{}, err
	}
	info := ServiceInfo{Name: "Pit Officer"}
	if len(status.Nodes) > 0 {
		h := status.Nodes[0]
		info.EngineVersion = h.Engine.Version
		info.EngineBuildProfile = h.Engine.BuildProfile
		info.Release = h.Engine.BuildProfile == "release"
		info.Database = ServiceDatabase{
			Path:      h.Store.Path,
			Reachable: h.Store.Reachable,
		}
	}
	return info, nil
}
