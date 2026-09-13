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

// Package app composes an Officer application from framework registries and
// concrete implementation hooks.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// FatalShutdownHook is invoked by the business node on unrecoverable
// post-engine persistence failures. Build requires one; node.NewLocalNode says
// why.
type FatalShutdownHook func(error)

// StoreFactory opens the composition's persistent store.
type StoreFactory func() (store.Store, error)

// NodeBuilder builds the composition's node over the opened store, together
// with the engine build function the node seeds and rebuilds from, and returns
// the initial engine handle. Build rejects a result without a node or without
// an engine, and binds the realm-scoped services to the node's realm.
type NodeBuilder func(
	context.Context, store.Store, FatalShutdownHook,
) (node.Node, engine.Engine, error)

// SigningFactory builds the approval-token signing service.
type SigningFactory func(store.RealmStore) (signing.Service, error)

// ServiceFactory builds the framework control plane service over the node.
type ServiceFactory func(
	node.Node,
	backend.MarketDataRuntime,
	signing.Service,
	*marketdata.Registry,
	*frameworkmcp.ToolRegistry,
) (backend.ControlPlane, error)

// RouteConfig carries an app's registered HTTP surface.
type RouteConfig struct {
	Routes      *httpx.RouteRegistry
	BodyLimit   func(*http.Request) int64
	ExtraMounts []httpx.ExtraMount
}

// RouteConfigBuilder builds the HTTP route registry for a service.
type RouteConfigBuilder func(backend.ControlPlane, httpx.LogSource) RouteConfig

// ToolRegistrar registers an app's MCP tools and catalogue entries.
type ToolRegistrar func(*frameworkmcp.ToolRegistry, frameworkmcp.Source)

// SPAFactory loads the embedded dashboard filesystem.
type SPAFactory func() (fs.FS, error)

// Builder collects the concrete hooks that make one composition.
type Builder struct {
	storeFactory   StoreFactory
	nodeBuilder    NodeBuilder
	signingFactory SigningFactory
	serviceFactory ServiceFactory
	routeConfig    RouteConfigBuilder
	toolRegistrars []ToolRegistrar
	spaFactory     SPAFactory
	authorizer     httpx.Authorizer
	callerResolver auth.CallerResolver
	mdRegistry     *marketdata.Registry
}

// NewBuilder constructs an empty composition builder.
func NewBuilder() *Builder {
	return &Builder{
		mdRegistry: marketdata.NewRegistry(),
	}
}

// SetStoreFactory sets the persistent store implementation.
func (b *Builder) SetStoreFactory(factory StoreFactory) {
	b.storeFactory = factory
}

// SetNodeBuilder sets the node implementation hook.
func (b *Builder) SetNodeBuilder(builder NodeBuilder) {
	b.nodeBuilder = builder
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

// SetAuthorizer sets the app-wide authorizer.
func (b *Builder) SetAuthorizer(authorizer httpx.Authorizer) {
	b.authorizer = authorizer
}

// SetCallerResolver sets the app-wide request identity resolver.
func (b *Builder) SetCallerResolver(resolver auth.CallerResolver) {
	b.callerResolver = resolver
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

// Build assembles the configured app and starts runtime services. The signer
// and the market-data manager are bound to the realm of the node that the node
// hook builds. fatalHook is handed to the node hook; a nil one is rejected
// before anything is opened.
func (b *Builder) Build(
	ctx context.Context,
	logger *slog.Logger,
	fatalHook FatalShutdownHook,
) (*App, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	if fatalHook == nil {
		return nil, errors.New("app: nil fatal shutdown hook")
	}

	st, err := b.storeFactory()
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("migrate store: %w", err)
	}
	logger.Info("store migrated", "path", st.Path())

	localNode, eng, err := b.nodeBuilder(ctx, st, fatalHook)
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("build node: %w", err)
	}
	if localNode == nil {
		if eng != nil {
			eng.Stop()
			eng.CloseMarketDataService()
		}
		_ = st.Close()
		return nil, errors.New("build node: node builder returned no node")
	}
	if eng == nil {
		_ = localNode.Close()
		return nil, errors.New("build node: node builder returned no engine")
	}
	logger.Info(
		"engine built and seeded from store",
		"version", eng.Version(),
		"profile", eng.BuildProfile(),
	)

	realm, err := st.ForRealm(ctx, localNode.Realm())
	if err != nil {
		_ = localNode.Close()
		return nil, fmt.Errorf("bind realm: %w", err)
	}

	signer, err := b.signingFactory(realm)
	if err != nil {
		_ = localNode.Close()
		return nil, fmt.Errorf("build signing service: %w", err)
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
	service, err := b.serviceFactory(localNode, manager, signer, b.mdRegistry, mcpRegistry)
	if err != nil {
		manager.Stop()
		_ = localNode.Close()
		return nil, fmt.Errorf("build service: %w", err)
	}
	src := frameworkmcp.SourceFor(service)
	for _, registrar := range b.toolRegistrars {
		registrar(mcpRegistry, src)
	}

	return &App{
		service:        service,
		node:           localNode,
		marketData:     manager,
		mcpRegistry:    mcpRegistry,
		source:         src,
		version:        nodeVersionSource{node: localNode},
		authorizer:     b.authorizer,
		callerResolver: b.callerResolver,
		routeConfig:    b.routeConfig,
		spaFactory:     b.spaFactory,
	}, nil
}

func (b *Builder) validate() error {
	switch {
	case b == nil:
		return errors.New("app: nil builder")
	case b.authorizer == nil:
		return errors.New("app: nil authorizer")
	case b.callerResolver == nil:
		return errors.New("app: nil caller resolver")
	case b.storeFactory == nil:
		return errors.New("app: nil store factory")
	case b.nodeBuilder == nil:
		return errors.New("app: nil node builder")
	case b.signingFactory == nil:
		return errors.New("app: nil signing factory")
	case b.serviceFactory == nil:
		return errors.New("app: nil service factory")
	case b.routeConfig == nil:
		return errors.New("app: nil route config builder")
	case b.spaFactory == nil:
		return errors.New("app: nil SPA factory")
	}
	if b.mdRegistry == nil {
		b.mdRegistry = marketdata.NewRegistry()
	}
	return nil
}

// App is a running Officer composition.
type App struct {
	service        backend.ControlPlane
	node           node.Node
	marketData     *marketdata.Manager
	mcpRegistry    *frameworkmcp.ToolRegistry
	source         frameworkmcp.Source
	version        frameworkmcp.VersionSource
	authorizer     httpx.Authorizer
	callerResolver auth.CallerResolver
	routeConfig    RouteConfigBuilder
	spaFactory     SPAFactory
}

// Service returns the app control plane.
func (a *App) Service() backend.ControlPlane {
	return a.service
}

// RecordServiceLifecycle appends an audit row for a process-level lifecycle
// request accepted by the serve wrapper, attributed to the caller stamped into
// ctx (auth.LookupCaller) as it is, an explicit system caller included. A ctx
// with no stamped caller is rejected before anything is written.
func (a *App) RecordServiceLifecycle(
	ctx context.Context,
	action domain.AuditAction,
	detail string,
) error {
	if a == nil || a.node == nil {
		return errors.New("app: nil node")
	}
	caller, ok := auth.LookupCaller(ctx)
	if !ok {
		return errors.New("app: service lifecycle request has no resolved caller")
	}
	return a.node.AppendAudit(ctx, store.AuditEntry{
		Action: action,
		Detail: detail,
	}, caller)
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
func (a *App) RunMCPStdio(ctx context.Context, caller domain.Caller) error {
	return frameworkmcp.RunStdio(
		ctx,
		a.mcpRegistry,
		a.source,
		a.version,
		a.authorizer,
		caller,
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
	mcpHandler, err := frameworkmcp.Handler(
		a.mcpRegistry,
		a.source,
		a.version,
		a.authorizer,
		a.callerResolver,
	)
	if err != nil {
		return nil, fmt.Errorf("build mcp handler: %w", err)
	}
	router, err := httpx.NewRouter(httpx.RouterConfig{
		Routes:         routes.Routes,
		Authorizer:     a.authorizer,
		CallerResolver: a.callerResolver,
		SPA:            spa,
		MCP:            http.StripPrefix(mcpPath, mcpHandler),
		MCPPath:        mcpPath,
		Logs:           logs,
		BodyLimit:      routes.BodyLimit,
		ExtraMounts:    routes.ExtraMounts,
	})
	if err != nil {
		return nil, fmt.Errorf("build router: %w", err)
	}
	return router, nil
}

type nodeVersionSource struct {
	node node.Node
}

func (v nodeVersionSource) Version() string {
	return v.node.EngineVersion()
}
