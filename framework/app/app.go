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

// Package app composes an Officer distribution from framework registries and
// concrete implementation hooks.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/secret"
	"go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// Config is the framework-owned runtime configuration seam consumed by Builder.
type Config struct {
	SQLitePath         string
	RuntimeLibraryPath string
	MasterKey          *secret.MasterKey
}

// FatalShutdownHook is invoked by the business node on unrecoverable
// post-engine persistence failures.
type FatalShutdownHook func(error)

// StoreFactory opens the configured persistent store for cfg.
type StoreFactory func(Config) (store.Store, error)

// EngineBuildFactory returns the engine build function for cfg.
type EngineBuildFactory func(Config) engine.BuildFunc

// NodeBuilder builds the node and returns the initial engine handle.
type NodeBuilder func(
	context.Context, store.Store, engine.BuildFunc, FatalShutdownHook,
) (node.Node, engine.Engine, error)

// NodeRouterBuilder builds the routing seam over the configured node set.
type NodeRouterBuilder func(node.Node) (node.NodeRouter, error)

// SigningFactory builds the approval-token signing service.
type SigningFactory func(store.RealmStore) (signing.Service, error)

// ServiceFactory builds the framework control plane service.
type ServiceFactory func(
	node.NodeRouter,
	backend.MarketDataRuntime,
	signing.Service,
	*marketdata.Registry,
	*frameworkmcp.ToolRegistry,
) (ControlPlane, error)

// ControlPlane is the app-level service seam: HTTP consumes backend.ControlPlane,
// while MCP also needs the operator command-access read.
type ControlPlane interface {
	backend.ControlPlane
	CommandEnabled(ctx context.Context, command string) (bool, error)
}

// RouteConfig carries an app's registered HTTP surface.
type RouteConfig struct {
	Routes      *httpx.RouteRegistry
	Authorizer  httpx.Authorizer
	BodyLimit   func(*http.Request) int64
	ExtraMounts []httpx.ExtraMount
}

// RouteConfigBuilder builds the HTTP route registry for a service.
type RouteConfigBuilder func(backend.ControlPlane, httpx.LogSource) RouteConfig

// ToolRegistrar registers an app's MCP tools and catalogue entries.
type ToolRegistrar func(*frameworkmcp.ToolRegistry, frameworkmcp.Source)

// SPAFactory loads the embedded dashboard filesystem.
type SPAFactory func() (fs.FS, error)

// Builder collects the concrete hooks that make one Officer distribution.
type Builder struct {
	storeFactory      StoreFactory
	engineBuild       EngineBuildFactory
	nodeBuilder       NodeBuilder
	nodeRouterBuilder NodeRouterBuilder
	signingFactory    SigningFactory
	serviceFactory    ServiceFactory
	routeConfig       RouteConfigBuilder
	toolRegistrars    []ToolRegistrar
	spaFactory        SPAFactory
	authorizer        httpx.Authorizer
	mdRegistry        *marketdata.Registry
}

// NewBuilder constructs an empty composition builder.
func NewBuilder() *Builder {
	return &Builder{
		mdRegistry: marketdata.NewRegistry(),
		authorizer: httpx.AllowAll{},
	}
}

// SetStoreFactory sets the persistent store implementation.
func (b *Builder) SetStoreFactory(factory StoreFactory) {
	b.storeFactory = factory
}

// SetEngineBuildFactory sets the engine build hook.
func (b *Builder) SetEngineBuildFactory(factory EngineBuildFactory) {
	b.engineBuild = factory
}

// SetNodeBuilder sets the node implementation hook.
func (b *Builder) SetNodeBuilder(builder NodeBuilder) {
	b.nodeBuilder = builder
}

// SetNodeRouterBuilder sets the node router implementation hook.
func (b *Builder) SetNodeRouterBuilder(builder NodeRouterBuilder) {
	b.nodeRouterBuilder = builder
}

// SetSigningFactory sets the approval-token signing implementation hook.
func (b *Builder) SetSigningFactory(factory SigningFactory) {
	b.signingFactory = factory
}

// SetServiceFactory sets the control-plane service implementation hook.
func (b *Builder) SetServiceFactory(factory ServiceFactory) {
	b.serviceFactory = factory
}

// SetRouteConfigBuilder sets the HTTP route registration hook.
func (b *Builder) SetRouteConfigBuilder(builder RouteConfigBuilder) {
	b.routeConfig = builder
}

// WrapRouteConfigBuilder replaces the current HTTP route registration hook
// with wrapper(current). It returns an error when no route builder is installed.
func (b *Builder) WrapRouteConfigBuilder(
	wrapper func(RouteConfigBuilder) RouteConfigBuilder,
) error {
	if b.routeConfig == nil {
		return errors.New("app: nil route config builder")
	}
	if wrapper == nil {
		return nil
	}
	b.routeConfig = wrapper(b.routeConfig)
	return nil
}

// AddToolRegistrar appends an MCP tool registration hook.
func (b *Builder) AddToolRegistrar(registrar ToolRegistrar) {
	if registrar != nil {
		b.toolRegistrars = append(b.toolRegistrars, registrar)
	}
}

// SetSPAFactory sets the embedded dashboard filesystem hook.
func (b *Builder) SetSPAFactory(factory SPAFactory) {
	b.spaFactory = factory
}

// SetAuthorizer sets the app-wide authorizer default.
func (b *Builder) SetAuthorizer(authorizer httpx.Authorizer) {
	if authorizer == nil {
		authorizer = httpx.AllowAll{}
	}
	b.authorizer = authorizer
}

// RegisterMarketDataProvider adds or replaces a market-data provider.
func (b *Builder) RegisterMarketDataProvider(provider marketdata.Provider) error {
	if b.mdRegistry == nil {
		b.mdRegistry = marketdata.NewRegistry()
	}
	return b.mdRegistry.Register(provider)
}

// UnregisterMarketDataProvider removes a market-data provider.
func (b *Builder) UnregisterMarketDataProvider(providerType string) bool {
	if b.mdRegistry == nil {
		return false
	}
	return b.mdRegistry.Unregister(providerType)
}

// Build assembles the configured app and starts runtime services.
func (b *Builder) Build(
	ctx context.Context,
	cfg Config,
	logger *slog.Logger,
	fatalHook FatalShutdownHook,
) (*App, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}

	st, err := b.storeFactory(cfg)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("migrate store: %w", err)
	}
	logger.Info("store migrated", "path", st.Path())

	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("bind realm: %w", err)
	}

	localNode, eng, err := b.nodeBuilder(ctx, st, b.engineBuild(cfg), fatalHook)
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("build node: %w", err)
	}
	logger.Info(
		"engine built and seeded from store",
		"version", eng.Version(),
		"profile", eng.BuildProfile(),
	)

	signer, err := b.signingFactory(realm)
	if err != nil {
		_ = localNode.Close()
		return nil, fmt.Errorf("build signing service: %w", err)
	}

	router, err := b.nodeRouterBuilder(localNode)
	if err != nil {
		_ = localNode.Close()
		return nil, fmt.Errorf("build router: %w", err)
	}

	manager, err := marketdata.NewManager(
		b.mdRegistry,
		realm,
		eng.MarketDataSink(),
		logger,
	)
	if err != nil {
		_ = localNode.Close()
		return nil, fmt.Errorf("build market-data manager: %w", err)
	}
	// Resolve the live engine sink on every push: the engine (and its sink) is
	// rebuilt on account/group/asset changes, so a cached sink would go stale
	// until a restart. The provider keeps quotes flowing across rebuilds.
	manager.UseSinkProvider(localNode.CurrentMarketDataSink)
	if err := manager.Start(ctx); err != nil {
		manager.Stop()
		_ = localNode.Close()
		return nil, fmt.Errorf("start market-data manager: %w", err)
	}

	mcpRegistry := frameworkmcp.NewToolRegistry()
	service, err := b.serviceFactory(router, manager, signer, b.mdRegistry, mcpRegistry)
	if err != nil {
		manager.Stop()
		_ = localNode.Close()
		return nil, fmt.Errorf("build service: %w", err)
	}
	src := sourceAdapter{service: service}
	for _, registrar := range b.toolRegistrars {
		registrar(mcpRegistry, src)
	}

	return &App{
		service:     service,
		node:        localNode,
		marketData:  manager,
		mcpRegistry: mcpRegistry,
		source:      src,
		version:     nodeVersionSource{node: localNode},
		authorizer:  b.authorizer,
		routeConfig: b.routeConfig,
		spaFactory:  b.spaFactory,
	}, nil
}

func (b *Builder) validate() error {
	switch {
	case b == nil:
		return errors.New("app: nil builder")
	case b.storeFactory == nil:
		return errors.New("app: nil store factory")
	case b.engineBuild == nil:
		return errors.New("app: nil engine build factory")
	case b.nodeBuilder == nil:
		return errors.New("app: nil node builder")
	case b.nodeRouterBuilder == nil:
		return errors.New("app: nil node router builder")
	case b.signingFactory == nil:
		return errors.New("app: nil signing factory")
	case b.serviceFactory == nil:
		return errors.New("app: nil service factory")
	case b.routeConfig == nil:
		return errors.New("app: nil route config builder")
	case b.spaFactory == nil:
		return errors.New("app: nil SPA factory")
	}
	if b.authorizer == nil {
		b.authorizer = httpx.AllowAll{}
	}
	if b.mdRegistry == nil {
		b.mdRegistry = marketdata.NewRegistry()
	}
	return nil
}

// App is a running Officer composition.
type App struct {
	service     ControlPlane
	node        node.Node
	marketData  *marketdata.Manager
	mcpRegistry *frameworkmcp.ToolRegistry
	source      frameworkmcp.Source
	version     frameworkmcp.VersionSource
	authorizer  httpx.Authorizer
	routeConfig RouteConfigBuilder
	spaFactory  SPAFactory
}

// Service returns the app control plane.
func (a *App) Service() backend.ControlPlane {
	return a.service
}

// RecordServiceLifecycle appends an audit row for a process-level lifecycle
// request accepted by the serve wrapper.
func (a *App) RecordServiceLifecycle(
	ctx context.Context,
	action domain.AuditAction,
	detail string,
	source domain.Source,
) error {
	if a == nil || a.node == nil {
		return errors.New("app: nil node")
	}
	return a.node.AppendAudit(ctx, store.AuditEntry{
		Action: action,
		Detail: detail,
	}, domain.Caller{
		Source:    source,
		Principal: domain.PrincipalOperator,
	})
}

// Close stops producers before closing the node and engine.
func (a *App) Close() error {
	// Quote producers must stop before the node closes the engine and its
	// market-data service; pushing into that closed service would be use-after-free.
	if a.marketData != nil {
		a.marketData.Stop()
	}
	if a.node != nil {
		if err := a.node.Close(); err != nil {
			return fmt.Errorf("close node: %w", err)
		}
	}
	return nil
}

// RunMCPStdio serves the registered MCP surface over stdio.
func (a *App) RunMCPStdio(ctx context.Context) error {
	return frameworkmcp.RunStdio(
		ctx,
		a.mcpRegistry,
		a.source,
		a.version,
		a.resolveAuthorizer(nil),
	)
}

// BuildServeHandler builds the HTTP route tree for serve mode. Extra routes
// are registered before the runtime route manifest and router are built.
func (a *App) BuildServeHandler(
	logs httpx.LogSource,
	mcpPath string,
	extraRoutes ...httpx.Route,
) (http.Handler, error) {
	spa, err := a.spaFactory()
	if err != nil {
		return nil, fmt.Errorf("load embedded dashboard: %w", err)
	}
	routes := a.routeConfig(a.service, logs)
	if routes.Routes != nil {
		for _, route := range extraRoutes {
			routes.Routes.Register(route)
		}
	}
	authorizer := a.resolveAuthorizerFromConfig(routes)
	mcpHandler, err := frameworkmcp.Handler(
		a.mcpRegistry,
		a.source,
		a.version,
		authorizer,
	)
	if err != nil {
		return nil, fmt.Errorf("build mcp handler: %w", err)
	}
	router, err := httpx.NewRouter(httpx.RouterConfig{
		Routes:      routes.Routes,
		Authorizer:  authorizer,
		SPA:         spa,
		MCP:         http.StripPrefix(mcpPath, mcpHandler),
		MCPPath:     mcpPath,
		Logs:        logs,
		BodyLimit:   routes.BodyLimit,
		ExtraMounts: routes.ExtraMounts,
	})
	if err != nil {
		return nil, fmt.Errorf("build router: %w", err)
	}
	return router, nil
}

func (a *App) resolveAuthorizer(logs httpx.LogSource) httpx.Authorizer {
	if a.routeConfig == nil {
		return a.authorizer
	}
	return a.resolveAuthorizerFromConfig(a.routeConfig(a.service, logs))
}

func (a *App) resolveAuthorizerFromConfig(routes RouteConfig) httpx.Authorizer {
	if routes.Authorizer != nil {
		return routes.Authorizer
	}
	return a.authorizer
}

type nodeVersionSource struct {
	node node.Node
}

func (v nodeVersionSource) Version() string {
	return v.node.EngineVersion()
}

type sourceAdapter struct {
	service ControlPlane
}

func (a sourceAdapter) Status(ctx context.Context) (frameworkmcp.Status, error) {
	status, err := a.service.Status(ctx)
	if err != nil {
		return frameworkmcp.Status{}, err
	}

	nodes := make([]frameworkmcp.NodeHealth, 0, len(status.Nodes))
	for _, n := range status.Nodes {
		nodes = append(nodes, frameworkmcp.NodeHealth{
			Engine: frameworkmcp.EngineHealth{
				Version:      n.Engine.Version,
				BuildProfile: n.Engine.BuildProfile,
				Running:      n.Engine.Running,
			},
			Store: frameworkmcp.StoreHealth{
				Path:          n.Store.Path,
				SchemaVersion: n.Store.SchemaVersion,
				Reachable:     n.Store.Reachable,
			},
		})
	}
	return frameworkmcp.Status{Nodes: nodes, Healthy: status.Healthy}, nil
}

func (a sourceAdapter) GetAccountState(
	ctx context.Context, id domain.AccountID,
) (domain.Account, node.AccountLimits, error) {
	return a.service.GetAccountState(ctx, id)
}

func (a sourceAdapter) ListGroups(
	ctx context.Context,
) ([]domain.AccountGroup, error) {
	return a.service.ListGroups(ctx)
}

func (a sourceAdapter) ListLimits(
	ctx context.Context, account domain.AccountID,
) (node.AccountLimits, error) {
	return a.service.ListLimits(ctx, account)
}

func (a sourceAdapter) ListAudit(ctx context.Context, n int) ([]domain.AuditRow, error) {
	return a.service.ListAudit(ctx, n)
}

func (a sourceAdapter) ListAuditFiltered(
	ctx context.Context, filter domain.AuditFilter, n int,
) ([]domain.AuditRow, error) {
	return a.service.ListAuditFiltered(ctx, filter, n)
}

func (a sourceAdapter) CheckOrder(
	ctx context.Context, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	return a.service.CheckOrder(ctx, probe)
}

func (a sourceAdapter) GetOrder(
	ctx context.Context, externalID string,
) (domain.OrderDetail, error) {
	return a.service.GetOrder(ctx, externalID)
}

func (a sourceAdapter) SetMarketDataInstrumentEnabled(
	ctx context.Context, instanceID, externalSymbol string, enabled bool,
) error {
	return a.service.SetMarketDataInstrumentEnabled(ctx, instanceID, externalSymbol, enabled)
}

func (a sourceAdapter) CommandEnabled(ctx context.Context, command string) (bool, error) {
	return a.service.CommandEnabled(ctx, command)
}

func (a sourceAdapter) SubmitOrderToken(
	ctx context.Context,
	o domain.Order,
	mode string,
	missing domain.MissingAccountPolicy,
) (frameworkmcp.SubmitOrderTokenResult, error) {
	tok, err := a.service.SubmitOrderToken(ctx, o, mode, missing)
	if err != nil {
		return frameworkmcp.SubmitOrderTokenResult{}, err
	}
	return frameworkmcp.SubmitOrderTokenResult{
		Token:           tok.Token,
		KeyID:           tok.KeyID,
		OrderExternalID: tok.OrderExternalID,
		Verdict:         tok.Verdict,
		Reasons:         tok.Reasons,
	}, nil
}

func (a sourceAdapter) SubmitDropCopyOrder(
	ctx context.Context, o domain.Order, missing domain.MissingAccountPolicy,
) (frameworkmcp.SubmitDropCopyOrderResult, error) {
	order, err := a.service.SubmitDropCopyOrder(ctx, o, missing)
	if err != nil {
		return frameworkmcp.SubmitDropCopyOrderResult{}, err
	}
	return frameworkmcp.SubmitDropCopyOrderResult{
		OrderExternalID: order.ExternalID.String(),
		Status:          order.Status,
	}, nil
}

func (a sourceAdapter) ConfirmExecution(
	ctx context.Context, orderExternalID, token string,
) (domain.Order, frameworkmcp.Attestation, error) {
	order, att, err := a.service.ConfirmExecution(ctx, orderExternalID, token)
	if err != nil {
		return domain.Order{}, frameworkmcp.Attestation{}, err
	}
	return order, attestationForMCP(att), nil
}

func (a sourceAdapter) CancelOrder(
	ctx context.Context, orderExternalID, token, leavesQuantity, reason string,
) (domain.Order, frameworkmcp.Attestation, error) {
	order, att, err := a.service.CancelOrder(
		ctx, orderExternalID, token, leavesQuantity, reason,
	)
	if err != nil {
		return domain.Order{}, frameworkmcp.Attestation{}, err
	}
	return order, attestationForMCP(att), nil
}

// attestationForMCP maps the backend attestation onto the surface-agnostic MCP
// carrier, mirroring the token and key id the HTTP surface exposes.
func attestationForMCP(att backend.Attestation) frameworkmcp.Attestation {
	return frameworkmcp.Attestation{
		Token:  att.Token,
		KeyID:  att.KeyID,
		Signed: att.Signed,
	}
}
