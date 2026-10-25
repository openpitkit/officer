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

// Package officer registers the Pit Officer concrete implementations.
package officer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"time"

	"go.openpit.dev/officer/engine"
	frameworkapp "go.openpit.dev/officer/framework/app"
	fwbackend "go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/secret"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/httpapi"
	"go.openpit.dev/officer/internal/access"
	"go.openpit.dev/officer/internal/accountmode"
	appmarketdata "go.openpit.dev/officer/internal/marketdata"
	"go.openpit.dev/officer/internal/store/sqlite"
	apptrading "go.openpit.dev/officer/internal/trading"
	"go.openpit.dev/officer/mcptools"
	"go.openpit.dev/officer/signing"
	"go.openpit.dev/officer/web"
)

// Config carries the default composition's runtime inputs: the SQLite database
// path, the master key that seals stored secrets (nil when none is
// configured), and the operator's extra host names. The native OpenPit runtime
// is not one of them: the SDK loads it at process start (see
// engine.NewOpenPitEngineBuildFunc).
type Config struct {
	SQLitePath string
	MasterKey  *secret.MasterKey
	// AllowedHosts are extra host names the HTTP surface answers besides
	// loopback; Register refuses an invalid one.
	AllowedHosts []string
}

// loopbackHosts are the host names the HTTP surface always answers to, since
// it listens on loopback by default; Register adds Config.AllowedHosts after
// them.
var loopbackHosts = []string{"localhost", "127.0.0.1", "::1"}

type accessAuthorizer func(context.Context, domain.Caller, string) error

func (a accessAuthorizer) Authorize(ctx context.Context, caller domain.Caller, identifier string) error {
	return a(ctx, caller, identifier)
}

type setupDecisions struct {
	node node.Node
}

func (d setupDecisions) AccountModeChosen(ctx context.Context) (bool, error) {
	return accountmode.Chosen(ctx, d.node)
}

func (d setupDecisions) CredentialsSealed() bool { return d.node.CredentialsSealed() }

// Register populates b with the Pit Officer concrete implementations built
// from cfg. The composition serves domain.DefaultRealm through one local node.
//
// During setup the caller resolver refuses API, MCP over HTTP and non-setup
// panel routes, and the installed authorizer repeats the refusal for API and
// panel routes. An added node hook builds the access Service whatever factories
// a host replaces. Build fails if the store lacks access.Store or the
// seal-report reader; nothing serves without the access Service.
func Register(b *frameworkapp.Builder, cfg Config) error {
	if b == nil {
		return errors.New("officer: nil builder")
	}
	// Build sets these in the node hook before the app
	// serves; the resolver and the route config read them afterwards.
	var (
		sealReports   httpapi.SealReportSource
		accessService *access.Service
	)
	b.SetStoreFactory(func() (store.Store, error) {
		var opts []sqlite.Option
		if cfg.MasterKey != nil {
			opts = append(opts, sqlite.WithMasterKey(*cfg.MasterKey))
		}
		return sqlite.New(cfg.SQLitePath, domain.DefaultRealm, opts...)
	})
	b.SetNodeBuilder(func(
		ctx context.Context,
		st store.Store,
		fatalHook frameworkapp.FatalShutdownHook,
	) (node.Node, fwengine.Engine, error) {
		return node.NewLocalNode(
			ctx,
			domain.DefaultRealm,
			st,
			engine.NewOpenPitEngineBuildFunc(),
			fatalHook,
		)
	})
	b.AddNodeHook(func(ctx context.Context, st store.Store, n node.Node) error {
		accessState, ok := st.(access.Store)
		if !ok {
			return errors.New("officer: the store does not implement access.Store")
		}
		reports, err := sealReportSource(st)
		if err != nil {
			return err
		}
		accessService, err = access.New(accessState, n, os.Stderr, setupDecisions{node: n})
		if err != nil {
			return fmt.Errorf("officer: build access service: %w", err)
		}
		sealReports = reports
		return nil
	})
	b.SetSigningFactory(func(st store.RealmStore) (fwsigning.Service, error) {
		return signing.New(st)
	})
	b.SetAuthorizer(accessAuthorizer(func(ctx context.Context, caller domain.Caller, identifier string) error {
		return accessService.Authorize(ctx, caller, identifier)
	}))
	b.SetCallerResolver(func(r *http.Request) (domain.Caller, error) {
		return accessService.ResolveCaller(r)
	})
	host, err := httpx.AllowedHosts(slices.Concat(loopbackHosts, cfg.AllowedHosts))
	if err != nil {
		return fmt.Errorf("officer: browser guards: %w", err)
	}
	b.SetBrowserGuards(httpx.BrowserGuards{
		Host:        host,
		Origin:      httpx.SameOrigin(),
		ContentType: httpx.JSONBody(),
	})
	for _, provider := range appmarketdata.FirstPartyProviders() {
		if err := b.RegisterMarketDataProvider(provider); err != nil {
			return fmt.Errorf("officer: register market data provider: %w", err)
		}
	}
	for _, provider := range apptrading.FirstPartyProviders() {
		if err := b.RegisterTradingProvider(provider); err != nil {
			return fmt.Errorf("officer: register trading provider: %w", err)
		}
	}
	b.SetServiceFactory(func(
		ctx context.Context,
		n node.Node,
		md fwbackend.MarketDataRuntime,
		rt fwbackend.TradingRuntime,
		signer fwsigning.Service,
		registry *fwmarketdata.Registry,
		mcpRegistry *frameworkmcp.ToolRegistry,
	) (fwbackend.ControlPlane, error) {
		// The account mode is read once per process; a stored change takes
		// effect at the next start.
		scope, err := accountmode.Load(ctx, n)
		if err != nil {
			return nil, err
		}
		return fwbackend.New(
			n,
			md,
			signer,
			engine.LockSettlementPrice,
			fwbackend.WithMarketDataRegistry(registry),
			fwbackend.WithTradingRuntime(rt),
			fwbackend.WithMCPCatalogProvider(mcpRegistry),
			fwbackend.WithAccountScope(scope),
		)
	})
	// A host that customises routes wraps b.RouteConfigBuilder(), so the
	// account mode and the access mode Register enforces stay served.
	b.SetRouteConfigBuilder(func(
		svc fwbackend.ControlPlane, logs httpx.LogSource,
	) frameworkapp.RouteConfig {
		config := httpapi.RouteConfig(svc, logs, sealReports)
		for _, route := range accountmode.Routes(svc) {
			config.Routes.Register(route)
		}
		config.PanelRoutes = &httpx.RouteRegistry{}
		for _, route := range access.Routes(accessService) {
			config.PanelRoutes.Register(route)
		}
		config.AccessMode = func() httpx.AccessMode {
			return manifestAccessMode(accessService)
		}
		return config
	})
	b.AddToolRegistrar(mcptools.RegisterTools)
	b.SetSPAFactory(web.Dist)
	return nil
}

// sealReportSource adapts the seal report of st, the store the store factory
// opened, to the source GET /credential-storage reads. It fails when st does
// not implement SealReport.
func sealReportSource(st store.Store) (httpapi.SealReportSource, error) {
	reader, ok := st.(interface {
		SealReport(ctx context.Context) (sqlite.SealReport, bool, error)
	})
	if !ok {
		return nil, errors.New("officer: the store does not implement SealReport")
	}
	return func(ctx context.Context) (time.Time, int, bool, error) {
		report, sealed, err := reader.SealReport(ctx)
		return report.SealedAt, report.ResealedSecrets, sealed, err
	}, nil
}

// manifestAccessMode is the route manifest's access mode for service's mode.
// An unknown mode maps to the empty mode, which the manifest refuses as an
// internal error.
func manifestAccessMode(service *access.Service) httpx.AccessMode {
	switch service.Mode() {
	case access.ModeSetup:
		return httpx.AccessModeSetup
	case access.ModeOpen:
		return httpx.AccessModeOpen
	case access.ModeToken:
		return httpx.AccessModeToken
	default:
		return ""
	}
}
