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
	"fmt"
	"sync"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/mcp/catalog"
	"go.openpit.dev/officer/framework/node"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
)

// MarketDataRuntime is the live view of the connector manager the backend
// needs: the current per-instance subscription state, a way to re-apply
// configuration, and a way to push an operator-set manual mark into a running
// instance after an upsert. The *marketdata.Manager satisfies it.
type MarketDataRuntime interface {
	InstanceStatuses() map[string]marketdata.InstanceRuntimeStatus
	AppliedConfig() map[string]marketdata.AppliedInstanceConfig
	Registry() *marketdata.Registry
	// QuoteUpdateInterval returns the elapsed time between the two most recent
	// ticks of the identified instrument's quote, and whether it is known yet.
	QuoteUpdateInterval(instanceID, external string) (time.Duration, bool)
	Restart() error
	// PushManual delivers or clears one instrument's operator-set manual mark in
	// the running instance. It is a no-op when the instance is not running, its
	// connector is not push-capable, or the applied subscription does not match.
	PushManual(
		ctx context.Context, instanceID string, instrument domain.MarketDataInstrument,
	) error
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
	router         node.NodeRouter
	md             MarketDataRuntime
	registry       *marketdata.Registry
	signer         fwsigning.Service
	commands       catalog.Provider
	lockSettlement LockSettlementPrice
	marketDataMu   sync.Mutex
}

type adjustmentRowNode interface {
	ListAdjustmentRows(context.Context, store.AdjustmentListFilter) (store.AdjustmentListPage, error)
}

type tradeRowNode interface {
	ListTradeRows(context.Context, store.TradeListFilter) (store.TradeListPage, error)
}

type auditRowNode interface {
	ListAuditRows(context.Context, store.AuditListFilter) (store.AuditListPage, error)
}

// LockSettlementPrice derives a display settlement price from an opaque engine
// lock blob.
type LockSettlementPrice func([]byte, domain.Order) (string, error)

// Option configures Service composition seams.
type Option func(*Service)

// WithMarketDataRegistry sets the provider registry used by catalogue and
// validation paths.
func WithMarketDataRegistry(registry *marketdata.Registry) Option {
	return func(s *Service) {
		if registry != nil {
			s.registry = registry
		}
	}
}

// WithMCPCommands sets the command catalogue used by the MCP-access surface.
func WithMCPCommands(commands []Command) Option {
	return func(s *Service) {
		s.commands = staticCatalogProvider{catalog: catalog.New(commands)}
	}
}

// WithMCPCatalog sets the command catalogue used by the MCP-access surface.
func WithMCPCatalog(commands catalog.Catalog) Option {
	return func(s *Service) {
		s.commands = staticCatalogProvider{catalog: commands}
	}
}

// WithMCPCatalogProvider sets the live command catalogue provider used by the
// MCP-access surface.
func WithMCPCatalogProvider(commands catalog.Provider) Option {
	return func(s *Service) {
		if commands != nil {
			s.commands = commands
		}
	}
}

// WithLockSettlementPrice sets the engine-specific lock display seam.
func WithLockSettlementPrice(estimator LockSettlementPrice) Option {
	return func(s *Service) {
		s.lockSettlement = estimator
	}
}

// New constructs a Service over the given node router, market-data runtime, and
// signing service. The router is the seam through which the service reaches
// every execution target; md is the live connector-manager view used to surface
// per-instance subscription state and to re-apply configuration; signer is the
// framework signing seam backing the approval-token flow. md may be nil
// (market-data state resolves to empty and RestartMarketData is a no-op);
// signer may be nil (the signing and approval-token methods then report the
// feature unconfigured).
func New(
	router node.NodeRouter,
	md MarketDataRuntime,
	signer fwsigning.Service,
	opts ...Option,
) *Service {
	registry := marketdata.NewRegistry()
	if md != nil {
		if runtimeRegistry := md.Registry(); runtimeRegistry != nil {
			registry = runtimeRegistry
		}
	}
	s := &Service{
		router:   router,
		md:       md,
		registry: registry,
		signer:   fwsigning.ServiceOrUnavailable(signer),
		commands: staticCatalogProvider{catalog: catalog.New(nil)},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
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

// keyFor builds the routing key for an account. With the realm bound on the node
// the account code alone resolves the owning node; the realm is never exposed on
// a surface.
func keyFor(id domain.AccountID) node.Key {
	return node.Key{Account: id}
}

// validateMissingAccountPolicy enforces the missing-account request contract:
// the caller must choose explicitly exactly when the request names an account,
// and a value supplied for a request that names none is ignored.
func validateMissingAccountPolicy(
	account domain.AccountID, missing domain.MissingAccountPolicy,
) error {
	if account == "" {
		return nil
	}
	return domain.ValidateMissingAccountPolicy(missing)
}
