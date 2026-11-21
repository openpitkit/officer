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
	"go.openpit.dev/officer/framework/alerts"
	frameworkapp "go.openpit.dev/officer/framework/app"
	"go.openpit.dev/officer/framework/auth"
	fwbackend "go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	fwmarketdata "go.openpit.dev/officer/framework/marketdata"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/secret"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	fwtrading "go.openpit.dev/officer/framework/trading"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/httpapi"
	"go.openpit.dev/officer/internal/access"
	"go.openpit.dev/officer/internal/accountmode"
	"go.openpit.dev/officer/internal/jobs"
	appmarketdata "go.openpit.dev/officer/internal/marketdata"
	"go.openpit.dev/officer/internal/nonrelease"
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
	// InitialSetup finishes setup before serving requests. Supplying it when
	// setup is already complete is an error.
	InitialSetup *InitialSetup
	SQLitePath   string
	MasterKey    *secret.MasterKey
	// AllowedHosts are extra host names the HTTP surface answers besides
	// loopback; Register refuses an invalid one.
	AllowedHosts []string
	// AlertTypes and AlertChannels attach business decisions and enabled delivery
	// channels. The panel channel is always installed by the alert service.
	AlertTypes    []alerts.Type
	AlertChannels []alerts.Channel
	// AlertServiceReady connects producers after storage and registries are ready.
	AlertServiceReady func(*alerts.Service) error
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

// FundsLimitModeChosen distinguishes an explicit stored choice from the
// effective default displayed by the permanent policy row.
func (d setupDecisions) FundsLimitModeChosen(ctx context.Context) (bool, error) {
	state, err := d.node.SpotFundsLimitMode(ctx)
	if err != nil {
		return false, err
	}
	return state.Stored, nil
}

func (d setupDecisions) CredentialsSealed() bool { return d.node.CredentialsSealed() }

// fundsLimitModePutRoute stores the Spot Funds limit mode, the decision
// FundsLimitModeChosen reads.
const fundsLimitModePutRoute = "limits.spot-funds-limit-mode.put"

// serialiseWithSetup wraps the handler of route id in routes so that, while
// first-run setup is open, it runs inside gate, as accountmode.ApplyDuringSetup
// stores the account mode: a write and its rollback, when the engine refuses
// it, finish before setup can complete and read the decision. Once setup is
// complete the handler runs alone. A route id absent from routes is a
// composition defect and panics.
func serialiseWithSetup(routes *httpx.RouteRegistry, id string, gate accountmode.SetupGate) {
	all := routes.Routes()
	i := slices.IndexFunc(all, func(route httpx.Route) bool { return route.ID == id })
	if i < 0 {
		panic(fmt.Sprintf("officer: route %q is not registered", id))
	}
	route := all[i]
	next := route.Handler
	route.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := gate.WhileInSetup(func() error {
			next.ServeHTTP(w, r)
			return nil
		})
		switch {
		case errors.Is(err, domain.ErrSetupComplete):
			next.ServeHTTP(w, r)
		case err != nil:
			httpx.WriteErr(w, err)
		}
	})
	routes.Register(route)
}

// Register populates b with the Pit Officer concrete implementations built
// from cfg. The composition serves domain.DefaultRealm through one local node.
//
// During setup the caller resolver refuses API, MCP over HTTP and non-setup
// panel routes, and the installed authorizer repeats the refusal for API and
// panel routes. An added node hook builds the access Service whatever factories
// a host replaces. Build fails if the store lacks access.Store or the
// seal-report reader; nothing serves without the access Service.
//
// The store opens cfg.SQLitePath as given. A non-release host passes its own
// database path, prepared through PrepareNonReleaseDatabase.
func Register(b *frameworkapp.Builder, cfg Config) error {
	if b == nil {
		return errors.New("officer: nil builder")
	}
	if cfg.InitialSetup != nil {
		if err := cfg.InitialSetup.Validate(cfg.MasterKey != nil); err != nil {
			return err
		}
	}
	// Build sets these in the node hook and the service factory before the
	// app serves; the resolver and the route config read them afterwards.
	var (
		sealReports   httpapi.SealReportSource
		accessService *access.Service
		alertService  *alerts.Service
		alertAccounts fwbackend.AccountRowReader
		backupNode    node.Node
		// Stays nil when a host replaces the service factory.
		accountScope *accountmode.Scope
		resetStore   jobs.StoreProvider
		resetHost    node.PnlResetHost
		resetControl fwbackend.AccountPnlResetControl
		scheduleTask func(context.Context, time.Time, func(time.Time)) (func() bool, error)
	)
	b.SetStoreFactory(func() (store.Store, error) {
		return newStore(cfg)
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
		if cfg.InitialSetup != nil && accessService.Mode() != access.ModeSetup {
			return fmt.Errorf("initial setup: configuration is already initialized; start without -init or reset the database")
		}
		sealReports = reports
		backupNode = n
		resetHost, resetStore, resetControl = nil, nil, nil
		host, supportedNode := n.(node.PnlResetHost)
		provider, supportedStore := st.(jobs.StoreProvider)
		if supportedNode && supportedStore {
			resetHost = host
			resetStore = provider
		}
		realmStore, err := st.ForRealm(ctx, domain.DefaultRealm)
		if err != nil {
			return err
		}
		alertState, ok := realmStore.(alerts.Store)
		if !ok {
			return errors.New("officer: the realm store does not implement alerts.Store")
		}
		alertAccountVisible := func(ctx context.Context, account domain.AccountID) (bool, error) {
			if alertAccounts == nil {
				return false, errors.New("officer: alert account visibility is not bound")
			}
			page, err := alertAccounts.ListAccountRows(ctx, store.AccountListFilter{
				Code: store.TextMatcher{Fragments: []string{account.String()}, AnchorStart: true, AnchorEnd: true},
			})
			if err != nil {
				return false, err
			}
			for _, row := range page.Rows {
				if row.Account.Code == account {
					return true, nil
				}
			}
			return false, nil
		}
		alertTypes := append([]alerts.Type{
			mixedTradingAccessType(alertAccountVisible),
			orderAwaitingReviewType(alertAccountVisible, n),
		}, cfg.AlertTypes...)
		alertService, err = alerts.New(alertState, alertTypes, cfg.AlertChannels)
		if err != nil {
			return fmt.Errorf("officer: build alert service: %w", err)
		}
		if host, ok := n.(node.TradingAccessObserverHost); ok {
			if err := host.InstallTradingAccessObserver(publishMixedTradingAccess(alertService)); err != nil {
				return fmt.Errorf("officer: attach trading access alert producer: %w", err)
			}
		}
		if host, ok := n.(node.OrderReviewObserverHost); ok {
			if err := host.InstallOrderReviewObserver(orderReviewAlertProducer{service: alertService}); err != nil {
				return fmt.Errorf("officer: attach order review alert producer: %w", err)
			}
		}
		if cfg.AlertServiceReady != nil {
			if err := cfg.AlertServiceReady(alertService); err != nil {
				return fmt.Errorf("officer: attach alert producers: %w", err)
			}
		}
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
	guards, err := browserGuards(cfg)
	if err != nil {
		return err
	}
	b.SetBrowserGuards(guards)
	for _, provider := range appmarketdata.FirstPartyProviders() {
		if err := b.RegisterMarketDataProvider(provider); err != nil {
			return fmt.Errorf("officer: register market data provider: %w", err)
		}
	}
	for _, provider := range apptrading.FirstPartyProviders() {
		build := provider.Build
		provider.Build = func(ctx fwtrading.BuildContext) (fwtrading.Connector, error) {
			ctx.Schedule = scheduleTask
			return build(ctx)
		}
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
		// The account mode is read once per process; once setup is complete
		// a stored change takes effect at the next start.
		scope, err := accountmode.Load(ctx, n)
		if err != nil {
			return nil, err
		}
		accountScope = scope
		opts := []fwbackend.Option{
			fwbackend.WithMarketDataRegistry(registry),
			fwbackend.WithTradingRuntime(rt),
			fwbackend.WithMCPCatalogProvider(mcpRegistry),
			fwbackend.WithAccountScope(scope),
			// Read at Build so a host's wrapper also governs forced operations.
			fwbackend.WithForceAuthorizer(b.Authorizer()),
		}
		if !fwbackend.ReleaseBuild() {
			opts = append(opts, fwbackend.WithNonReleaseBuild())
		}
		svc, err := fwbackend.New(
			n,
			md,
			signer,
			engine.LockSettlementPrice,
			opts...,
		)
		if err != nil {
			return nil, err
		}
		if cfg.InitialSetup != nil {
			if err := cfg.InitialSetup.apply(ctx, svc, scope, accessService); err != nil {
				return nil, err
			}
		}
		if resetHost != nil && resetStore != nil {
			jobStore, err := resetStore.SchedulerStore(ctx, n.Realm())
			if err != nil {
				return nil, err
			}
			runtime, err := jobs.New(n, resetHost, jobStore)
			if err != nil {
				return nil, err
			}
			scheduleTask = runtime.Schedule
			// Standalone operators configure a realm-wide schedule even when
			// the panel is presenting just one account. Manual resets retain scope.
			control, err := fwbackend.NewAccountPnlResetControl(svc, resetHost, runtime, fwbackend.EveryAccount{})
			if err != nil {
				return nil, err
			}
			if err := resetHost.InstallPnlResetScheduler(runtime); err != nil {
				return nil, err
			}
			if err := runtime.Start(); err != nil {
				return nil, err
			}
			resetControl = control
		}
		return svc, nil
	})
	// A host that customises routes wraps b.RouteConfigBuilder(), so the
	// account mode and the access mode Register enforces stay served.
	b.SetRouteConfigBuilder(func(
		svc fwbackend.ControlPlane, logs httpx.LogSource,
	) frameworkapp.RouteConfig {
		config := httpapi.RouteConfig(svc, logs, sealReports)
		for _, route := range httpapi.AlertRoutes(alertService) {
			config.Routes.Register(route)
		}
		if resetControl != nil {
			httpapi.RegisterAccountPnlResetRoutes(config.Routes, svc, resetControl)
		}
		serialiseWithSetup(config.Routes, fundsLimitModePutRoute, accessService)
		for _, route := range accountmode.Routes(svc, accountScope, accessService, resetControl != nil) {
			config.Routes.Register(route)
		}
		for _, route := range httpapi.PreMigrationBackupRoutes(preMigrationBackupSource(cfg.SQLitePath, backupNode)) {
			config.Routes.Register(route)
		}
		config.PanelRoutes = &httpx.RouteRegistry{}
		for _, route := range access.Routes(accessService) {
			config.PanelRoutes.Register(route)
		}
		config.PanelRoutes.Register(nonrelease.BuildRoute(func() nonrelease.Status {
			return nonrelease.Status{Release: fwbackend.ReleaseBuild(), Phase: nonrelease.PhaseReady}
		}))
		config.AccessMode = func() httpx.AccessMode {
			return manifestAccessMode(accessService)
		}
		return config
	})
	b.AddToolRegistrar(mcptools.RegisterTools)
	b.AddToolRegistrar(func(reg *frameworkmcp.ToolRegistry, source frameworkmcp.Source) {
		alertAccounts = source
		if resetControl != nil {
			mcptools.RegisterAccountPnlResetTools(reg, resetControl)
		}
		mcptools.RegisterAlertTools(reg, alertService)
	})
	b.SetSPAFactory(web.Dist)
	return nil
}

func browserGuards(cfg Config) (httpx.BrowserGuards, error) {
	host, err := httpx.AllowedHosts(slices.Concat(loopbackHosts, cfg.AllowedHosts))
	if err != nil {
		return httpx.BrowserGuards{}, fmt.Errorf("officer: browser guards: %w", err)
	}
	return httpx.BrowserGuards{
		Host:        host,
		Origin:      httpx.SameOrigin(),
		ContentType: httpx.JSONBody(),
	}, nil
}

func newStore(cfg Config) (store.Store, error) {
	var opts []sqlite.Option
	if fwbackend.ReleaseBuild() {
		opts = append(opts, sqlite.WithPreMigrationCopy())
	}
	if cfg.MasterKey != nil {
		opts = append(opts, sqlite.WithMasterKey(*cfg.MasterKey))
	}
	return sqlite.New(cfg.SQLitePath, domain.DefaultRealm, opts...)
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

func preMigrationBackupSource(path string, n node.Node) httpapi.PreMigrationBackupSource {
	return httpapi.PreMigrationBackupSource{
		List: func() ([]httpapi.PreMigrationBackup, error) {
			copies, err := sqlite.ListPreMigrationCopies(path)
			if err != nil {
				return nil, err
			}
			backups := make([]httpapi.PreMigrationBackup, 0, len(copies))
			for _, copy := range copies {
				backups = append(backups, httpapi.PreMigrationBackup{
					Name: copy.Name, CreatedAt: copy.CreatedAt, SizeBytes: copy.Size,
				})
			}
			return backups, nil
		},
		Open: func(name string) (*os.File, int64, error) {
			return sqlite.OpenPreMigrationCopy(path, name)
		},
		Remove: func(name string) error {
			return sqlite.RemovePreMigrationCopy(path, name)
		},
		Audit: func(ctx context.Context, action domain.AuditAction, detail domain.AuditDetail) error {
			if n == nil {
				return errors.New("officer: pre-migration backup: nil node")
			}
			caller, err := auth.CallerFromContext(ctx)
			if err != nil {
				return err
			}
			return n.AppendAudit(ctx, store.AuditEntry{Action: action, Detail: detail}, caller)
		},
	}
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
