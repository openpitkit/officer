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
// Please see https://officer.openpit.dev and the OWNERS file for details.

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
	"go.openpit.dev/officer/framework/trading"
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

// NodeHook composes services over the opened store and built node.
type NodeHook func(context.Context, store.Store, node.Node) error

// SigningFactory builds the approval signing service.
type SigningFactory func(store.RealmStore) (signing.Service, error)

// NodeRuntime holds a running node and its services. All four parts are required
// by BuildOver; the signer has no separate shutdown operation.
type NodeRuntime struct {
	Node       node.Node
	Signing    signing.Service
	MarketData backend.MarketDataRuntime
	Trading    backend.TradingRuntime
}

// NodeRuntimeFactory supplies running parts using the builder's provider
// registries. On error it must release everything it created. On success it
// transfers ownership to BuildOver, including when a part is missing.
// A missing part is a nil interface value. A typed nil pointer stored in a field
// is a present part, and the factory must not return one.
type NodeRuntimeFactory func(context.Context, *marketdata.Registry, *trading.Registry) (NodeRuntime, error)

// ServiceFactory builds the framework control plane service over the node.
// The context is Build's or BuildOver's: the factory may read the node while it
// builds.
type ServiceFactory func(
	context.Context,
	node.Node,
	backend.MarketDataRuntime,
	backend.TradingRuntime,
	signing.Service,
	*marketdata.Registry,
	*frameworkmcp.ToolRegistry,
) (backend.ControlPlane, error)

// RouteConfig carries an app's registered HTTP surface.
type RouteConfig struct {
	// Routes is required and supplies Officer's V1 route pair.
	Routes *httpx.RouteRegistry
	// PanelRoutes are routes served only on the panel mirror /app/api/v1; nil
	// means none.
	PanelRoutes *httpx.RouteRegistry
	// Surfaces adds prefixes after Officer's V1 pair; see [httpx.Surface]
	// for the routing and caller-source contract.
	// Leave Manifest false: Officer's /api/v1 surface carries the single route
	// manifest, which also lists these routes.
	Surfaces []httpx.Surface
	// NotFound handles unmatched paths; see [httpx.RouterConfig]. The router
	// refuses it when a SPA is configured.
	NotFound http.Handler
	// MethodNotAllowed handles 405 responses; see [httpx.RouterConfig] for
	// the hook's status, body, and Allow header contract.
	MethodNotAllowed http.Handler
	BodyLimit        func(*http.Request) int64
	ExtraMounts      []httpx.ExtraMount
	// AccessMode reports the access state of the service. The router calls it
	// on every route manifest request, concurrently from request goroutines,
	// so it must be safe for concurrent use. A value that is not an
	// httpx.AccessMode constant answers an internal error; the router refuses
	// a nil one.
	AccessMode func() httpx.AccessMode
}

// RouteConfigBuilder builds the HTTP route registry for a service.
type RouteConfigBuilder func(backend.ControlPlane, httpx.LogSource) RouteConfig

// ToolRegistrar registers an app's MCP tools and catalogue entries.
type ToolRegistrar func(*frameworkmcp.ToolRegistry, frameworkmcp.Source)

// SPAFactory loads the dashboard filesystem. It is optional: without one,
// unmatched paths use the router's typed JSON 404 unless NotFound is set.
// A configured factory must return a non-nil filesystem or an error.
type SPAFactory func() (fs.FS, error)

// Builder collects the concrete hooks that make one composition.
type Builder struct {
	storeFactory    StoreFactory
	nodeBuilder     NodeBuilder
	nodeHooks       []NodeHook
	signingFactory  SigningFactory
	serviceFactory  ServiceFactory
	routeConfig     RouteConfigBuilder
	toolRegistrars  []ToolRegistrar
	spaFactory      SPAFactory
	authorizer      httpx.Authorizer
	callerResolver  auth.CallerResolver
	browserGuards   httpx.BrowserGuards
	mdRegistry      *marketdata.Registry
	tradingRegistry *trading.Registry
}

// NewBuilder constructs an empty composition builder.
func NewBuilder() *Builder {
	return &Builder{
		mdRegistry:      marketdata.NewRegistry(),
		tradingRegistry: trading.NewRegistry(),
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

// AddNodeHook appends a step Build runs after checking the node and engine,
// before building services from them. Hooks run in registration order; an
// error fails Build with that error after closing the node.
func (b *Builder) AddNodeHook(hook NodeHook) {
	if hook != nil {
		b.nodeHooks = append(b.nodeHooks, hook)
	}
}

// SetSigningFactory sets the approval signing implementation hook.
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

// RouteConfigBuilder returns the HTTP route registration hook currently set,
// nil when none is. A host wraps it to extend the surface a composition
// installed - its routes, mounts or access mode - instead of rebuilding that
// surface, and sets the wrapper with SetRouteConfigBuilder.
func (b *Builder) RouteConfigBuilder() RouteConfigBuilder {
	return b.routeConfig
}

// AddToolRegistrar appends an MCP tool registration hook.
func (b *Builder) AddToolRegistrar(registrar ToolRegistrar) {
	if registrar != nil {
		b.toolRegistrars = append(b.toolRegistrars, registrar)
	}
}

// SetSPAFactory sets the dashboard filesystem hook. Nil disables the SPA;
// unmatched paths use the router's typed JSON 404 unless NotFound is set.
func (b *Builder) SetSPAFactory(factory SPAFactory) {
	b.spaFactory = factory
}

// SetAuthorizer sets the app-wide authorizer.
func (b *Builder) SetAuthorizer(authorizer httpx.Authorizer) {
	b.authorizer = authorizer
}

// Authorizer returns the app-wide authorizer currently set, nil when none is.
// A host wraps it to compose its policy with the installed authorizer and sets
// the wrapper with SetAuthorizer.
func (b *Builder) Authorizer() httpx.Authorizer {
	return b.authorizer
}

// SetCallerResolver sets the app-wide request identity resolver.
func (b *Builder) SetCallerResolver(resolver auth.CallerResolver) {
	b.callerResolver = resolver
}

// SetBrowserGuards sets the app-wide browser request guards the HTTP router
// applies to every request; BuildServeHandler fails without all three.
func (b *Builder) SetBrowserGuards(guards httpx.BrowserGuards) {
	b.browserGuards = guards
}

// RegisterMarketDataProvider adds or replaces a market-data provider.
func (b *Builder) RegisterMarketDataProvider(provider marketdata.Provider) error {
	if b.mdRegistry == nil {
		b.mdRegistry = marketdata.NewRegistry()
	}
	return b.mdRegistry.Register(provider)
}

// RegisterTradingProvider adds or replaces a trading provider.
func (b *Builder) RegisterTradingProvider(provider trading.Provider) error {
	if b.tradingRegistry == nil {
		b.tradingRegistry = trading.NewRegistry()
	}
	return b.tradingRegistry.Register(provider)
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
	version, err := resolveVersion(logger)
	if err != nil {
		return nil, err
	}

	st, err := b.storeFactory()
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if err := st.Migrate(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("migrate store: %w", err), st.Close())
	}
	logger.Info("store migrated", "path", st.Path())

	localNode, eng, err := b.nodeBuilder(ctx, st, fatalHook)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("build node: %w", err), st.Close())
	}
	if localNode == nil {
		if eng != nil {
			eng.Stop()
			eng.CloseMarketDataService()
		}
		return nil, errors.Join(errors.New("build node: node builder returned no node"), st.Close())
	}
	if eng == nil {
		return nil, errors.Join(errors.New("build node: node builder returned no engine"), localNode.Close())
	}
	for _, hook := range b.nodeHooks {
		if err := hook(ctx, st, localNode); err != nil {
			return nil, errors.Join(err, localNode.Close())
		}
	}
	logger.Info(
		"engine built and seeded from store",
		"version", eng.Version(),
		"profile", eng.BuildProfile(),
	)

	realm, err := st.ForRealm(ctx, localNode.Realm())
	if err != nil {
		return nil, errors.Join(fmt.Errorf("bind realm: %w", err), localNode.Close())
	}

	signer, err := b.signingFactory(realm)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("build signing service: %w", err), localNode.Close())
	}

	manager, err := marketdata.NewManager(
		b.mdRegistry,
		realm,
		eng.MarketDataSink(),
		logger,
	)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("build market-data manager: %w", err), localNode.Close())
	}
	// Resolve the live engine sink on every push: the engine (and its sink) is
	// rebuilt on account/group/asset changes, so a cached sink would go stale
	// until a restart. The provider keeps quotes flowing across rebuilds.
	manager.UseSinkProvider(localNode.CurrentMarketDataSink)
	if err := manager.Start(ctx); err != nil {
		manager.Stop()
		return nil, errors.Join(fmt.Errorf("start market-data manager: %w", err), localNode.Close())
	}

	mcpRegistry := frameworkmcp.NewToolRegistry()
	var service backend.ControlPlane
	runtime, err := trading.NewRuntime(b.tradingRegistry, realm,
		func(ctx context.Context, in domain.ExecutionReportInput) error {
			_, _, err := service.ApplyExecutionReport(auth.ContextWithCaller(ctx, auth.SystemCaller()), in)
			return err
		},
		func(ctx context.Context, account domain.AccountID, id domain.ExternalID, req domain.AdjustmentRequest) error {
			record, err := service.ApplyAdjustment(auth.ContextWithCaller(ctx, auth.SystemCaller()), account, id, req, domain.MissingAccountReject)
			if err == nil && record.Rejected != nil {
				return fmt.Errorf("%s: %w", record.Rejected.Reason, trading.ErrAdjustmentRejected)
			}
			return err
		},
		func(ctx context.Context, connection domain.ExternalID, feeID, reason string) error {
			return localNode.RecordVenueFeeSkipped(ctx, connection, feeID, reason, auth.SystemCaller())
		}, logger)
	if err != nil {
		manager.Stop()
		return nil, errors.Join(fmt.Errorf("build trading runtime: %w", err), localNode.Close())
	}
	service, err = b.serviceFactory(ctx, localNode, manager, runtime, signer, b.mdRegistry, mcpRegistry)
	if err != nil {
		manager.Stop()
		return nil, errors.Join(fmt.Errorf("build service: %w", err), localNode.Close())
	}
	// Build's signal context can end before Close; Stop owns runtime
	// cancellation.
	if err := runtime.Start(context.WithoutCancel(ctx)); err != nil {
		runtime.Stop()
		manager.Stop()
		return nil, errors.Join(fmt.Errorf("start trading runtime: %w", err), localNode.Close())
	}
	return b.compose(NodeRuntime{
		Node: localNode, Signing: signer, MarketData: manager, Trading: runtime,
	}, service, mcpRegistry, version), nil
}

// BuildOver composes over a host's running node and runtimes, without opening a
// store or starting any runtime. It refuses local-node factories and hooks.
// After the factory succeeds, a failure stops trading, stops market data, then
// closes the node. On success the returned App owns that shutdown sequence.
func (b *Builder) BuildOver(ctx context.Context, logger *slog.Logger, factory NodeRuntimeFactory) (*App, error) {
	switch {
	case b == nil:
		return nil, errors.New("app: nil builder")
	case factory == nil:
		return nil, errors.New("app: nil node runtime factory")
	case b.storeFactory != nil:
		return nil, errors.New("app: BuildOver refuses a store factory")
	case b.nodeBuilder != nil:
		return nil, errors.New("app: BuildOver refuses a node builder")
	case len(b.nodeHooks) != 0:
		return nil, errors.New("app: BuildOver refuses node hooks")
	case b.signingFactory != nil:
		return nil, errors.New("app: BuildOver refuses a signing factory")
	}
	if err := b.validateCommon(false); err != nil {
		return nil, err
	}
	version, err := resolveVersion(logger)
	if err != nil {
		return nil, err
	}

	runtime, err := factory(ctx, b.mdRegistry, b.tradingRegistry)
	if err != nil {
		return nil, fmt.Errorf("build node runtime: %w", err)
	}
	closeRuntime := func() error {
		return (&App{node: runtime.Node, marketData: runtime.MarketData, trading: runtime.Trading}).Close()
	}
	var missing string
	switch {
	case runtime.Node == nil:
		missing = "node"
	case runtime.Signing == nil:
		missing = "signing service"
	case runtime.MarketData == nil:
		missing = "market-data runtime"
	case runtime.Trading == nil:
		missing = "trading runtime"
	}
	if missing != "" {
		return nil, errors.Join(fmt.Errorf("app: node runtime factory returned no %s", missing), closeRuntime())
	}
	mcpRegistry := frameworkmcp.NewToolRegistry()
	service, err := b.serviceFactory(ctx, runtime.Node, runtime.MarketData, runtime.Trading,
		runtime.Signing, b.mdRegistry, mcpRegistry)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("build service: %w", err), closeRuntime())
	}
	return b.compose(runtime, service, mcpRegistry, version), nil
}

func resolveVersion(logger *slog.Logger) (string, error) {
	version, err := backend.Version()
	if err != nil {
		return "", fmt.Errorf("resolve officer version: %w", err)
	}
	logger.Info("mcp server version", "version", version,
		"release_build", backend.ReleaseBuild())
	return version, nil
}

func (b *Builder) compose(runtime NodeRuntime, service backend.ControlPlane, mcpRegistry *frameworkmcp.ToolRegistry, version string) *App {
	src := frameworkmcp.SourceFor(service)
	for _, registrar := range b.toolRegistrars {
		registrar(mcpRegistry, src)
	}

	return &App{
		service:        service,
		node:           runtime.Node,
		marketData:     runtime.MarketData,
		trading:        runtime.Trading,
		mcpRegistry:    mcpRegistry,
		source:         src,
		version:        officerVersion(version),
		authorizer:     b.authorizer,
		callerResolver: b.callerResolver,
		browserGuards:  b.browserGuards,
		routeConfig:    b.routeConfig,
		spaFactory:     b.spaFactory,
	}
}

func (b *Builder) validate() error {
	return b.validateCommon(true)
}

func (b *Builder) validateCommon(local bool) error {
	switch {
	case b == nil:
		return errors.New("app: nil builder")
	case b.authorizer == nil:
		return errors.New("app: nil authorizer")
	case b.callerResolver == nil:
		return errors.New("app: nil caller resolver")
	case local && b.storeFactory == nil:
		return errors.New("app: nil store factory")
	case local && b.nodeBuilder == nil:
		return errors.New("app: nil node builder")
	case local && b.signingFactory == nil:
		return errors.New("app: nil signing factory")
	case b.serviceFactory == nil:
		return errors.New("app: nil service factory")
	case b.routeConfig == nil:
		return errors.New("app: nil route config builder")
	}
	if b.mdRegistry == nil {
		b.mdRegistry = marketdata.NewRegistry()
	}
	if b.tradingRegistry == nil {
		b.tradingRegistry = trading.NewRegistry()
	}
	return nil
}

// App is a running Officer composition.
type App struct {
	service        backend.ControlPlane
	node           node.Node
	marketData     backend.MarketDataRuntime
	trading        backend.TradingRuntime
	mcpRegistry    *frameworkmcp.ToolRegistry
	source         frameworkmcp.Source
	version        frameworkmcp.VersionSource
	authorizer     httpx.Authorizer
	callerResolver auth.CallerResolver
	browserGuards  httpx.BrowserGuards
	routeConfig    RouteConfigBuilder
	spaFactory     SPAFactory
}

// Service returns the app control plane.
func (a *App) Service() backend.ControlPlane {
	return a.service
}

// RecordServiceLifecycle appends an audit row for a process-level lifecycle
// request accepted by the serve wrapper, attributed to the caller stamped into
// ctx as it is, an explicit system caller included. A ctx with no stamped
// caller is rejected before anything is written.
func (a *App) RecordServiceLifecycle(
	ctx context.Context,
	action domain.AuditAction,
	detail domain.AuditDetail,
) error {
	if a == nil || a.node == nil {
		return errors.New("app: nil node")
	}
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return fmt.Errorf("app: service lifecycle request: %w", err)
	}
	return a.node.AppendAudit(ctx, store.AuditEntry{
		Action: action,
		Detail: detail,
	}, caller)
}

// Close stops producers before closing the node and engine.
func (a *App) Close() error {
	if a.trading != nil {
		a.trading.Stop()
	}
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
	var spa fs.FS
	if a.spaFactory != nil {
		var err error
		spa, err = a.spaFactory()
		if err != nil {
			return nil, fmt.Errorf("load embedded dashboard: %w", err)
		}
		if spa == nil {
			return nil, errors.New("load embedded dashboard: nil filesystem")
		}
	}
	routes := a.routeConfig(a.service, logs)
	if routes.Routes == nil {
		return nil, errors.New("app: nil route registry")
	}
	for _, route := range extraRoutes {
		routes.Routes.Register(route)
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
		Surfaces:         append(httpx.V1Surfaces(routes.Routes, routes.PanelRoutes), routes.Surfaces...),
		Authorizer:       a.authorizer,
		CallerResolver:   a.callerResolver,
		SPA:              spa,
		NotFound:         routes.NotFound,
		MethodNotAllowed: routes.MethodNotAllowed,
		MCP:              http.StripPrefix(mcpPath, mcpHandler),
		MCPPath:          mcpPath,
		Logs:             logs,
		BodyLimit:        routes.BodyLimit,
		ExtraMounts:      routes.ExtraMounts,
		Guards:           a.browserGuards,
		AccessMode:       routes.AccessMode,
	})
	if err != nil {
		return nil, fmt.Errorf("build router: %w", err)
	}
	return router, nil
}

// officerVersion is the build's Officer version from backend.Version, served
// as the MCP server version.
type officerVersion string

func (v officerVersion) Version() string {
	return string(v)
}
