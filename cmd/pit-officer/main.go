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

// Command pit-officer is the Pit Officer control-plane binary. It has four
// subcommands:
//
//	pit-officer mcp           local stdio MCP server (no listener)
//	pit-officer serve         always-on dashboard + MCP-over-HTTP
//	pit-officer dashboard     discover a serve URL and open the browser
//	pit-officer healthcheck   probe a serve instance's /healthz
//
// The mcp and serve subcommands share the same startup: open the store, run
// migrations, then build the single local node, which builds the one engine
// seeded from the store and assembles the single-node control plane. They
// differ only in the surface they then expose. serve binds a loopback
// OS-assigned free port by default and publishes
// its real address as runtime state; dashboard and healthcheck read that state
// to find the live URL. dashboard and healthcheck open no engine and no store;
// healthcheck makes exactly one HTTP request.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"go.openpit.dev/officer"
	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/config"
	"go.openpit.dev/officer/internal/domain"
	"go.openpit.dev/officer/internal/engine"
	"go.openpit.dev/officer/internal/httpapi"
	"go.openpit.dev/officer/internal/logtail"
	"go.openpit.dev/officer/internal/marketdata"
	officermcp "go.openpit.dev/officer/internal/mcp"
	"go.openpit.dev/officer/internal/node"
	officerruntime "go.openpit.dev/officer/internal/runtime"
	"go.openpit.dev/officer/internal/store"
)

// mcpPath is the route the streamable-HTTP MCP handler is mounted under in
// serve mode.
const mcpPath = "/mcp"

// shutdownTimeout bounds the graceful HTTP shutdown before connections are
// forced closed.
const shutdownTimeout = 10 * time.Second

func main() {
	// Tee the logger into a bounded in-memory ring buffer so the serve surface
	// can read back a tail of recent lines. The stderr handler is unchanged; the
	// buffer is bounded by both an entry count and a byte size, so a runaway log
	// loop cannot exhaust memory.
	buf := logtail.New(0, 0)
	logger := slog.New(logtail.NewHandler(slog.NewTextHandler(os.Stderr, nil), buf))

	if err := run(os.Args[1:], logger, buf); err != nil {
		logger.Error("pit-officer exited with error", "err", err)
		os.Exit(1)
	}
}

// run dispatches to the requested subcommand. args excludes the program name.
// buf is the in-memory log tail; only serve reads it back over HTTP, so the
// other modes ignore it.
func run(args []string, logger *slog.Logger, buf *logtail.Buffer) error {
	if len(args) == 0 {
		return fmt.Errorf("missing subcommand: want one of mcp, serve, " +
			"dashboard, healthcheck")
	}

	command, rest := args[0], args[1:]
	switch command {
	case "mcp":
		return runMCP(rest, logger)
	case "serve":
		return runServe(rest, logger, buf)
	case "dashboard":
		return runDashboard(rest, logger)
	case "healthcheck":
		return runHealthcheck(rest)
	default:
		return fmt.Errorf("unknown subcommand %q: want one of mcp, serve, "+
			"dashboard, healthcheck", command)
	}
}

// controlPlane bundles the assembled control-plane components so the two run
// modes share one setup path and one shutdown path. It does not hold an engine
// handle directly: live engine state is reached through the node (Health,
// EngineVersion), never a captured handle. It holds the market-data manager so
// close can stop the quote producers before the node stops the engine and
// closes the market-data service.
type controlPlane struct {
	service    *backend.Service
	node       node.Node
	marketData *marketdata.Manager
	cfg        config.Config
}

// setup performs the shared startup for the mcp and serve modes: open the
// store, migrate it, then build the single local node. NewLocalNode builds the
// one engine seeded from the store and the node assembles the single-node
// control plane. On any failure it releases whatever it has already opened so a
// partial setup never leaks the store handle or the engine.
//
// Order matters: Migrate must run before NewLocalNode, because the build reads
// the accounts and limits tables the migration creates.
func setup(ctx context.Context, cfg config.Config, logger *slog.Logger) (
	*controlPlane, error) {
	st, err := store.NewSQLiteStore(cfg.SQLitePath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	if err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("migrate store: %w", err)
	}
	logger.Info("store migrated", "path", st.Path())

	// Seed the predefined stablecoin cross-rates on first run, before the manager
	// starts, so the enabled source and its 1:1 marks are applied by the normal
	// startup push. Idempotent: re-running is a no-op and never overrides operator
	// edits.
	if err := store.SeedMarketDataDefaults(ctx, st); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("seed market-data defaults: %w", err)
	}

	build := func(snap engine.Snapshot) (engine.Engine, error) {
		return engine.BuildOpenPitEngine(cfg.RuntimeLibraryPath, snap)
	}

	localNode, eng, err := node.NewLocalNode(ctx, st, build)
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("build node: %w", err)
	}
	logger.Info("engine built and seeded from store",
		"version", eng.Version(), "profile", eng.BuildProfile())

	router, err := node.NewLocalRouter(localNode)
	if err != nil {
		_ = localNode.Close()
		return nil, fmt.Errorf("build router: %w", err)
	}

	// The manager brings up the enabled connectors and drains their quotes into
	// the current engine sink; restore can swap the sink and restart the manager.
	// Nothing enabled is a clean no-op, and a bad instance is logged and skipped,
	// so Start never fails setup on configuration alone.
	manager := marketdata.NewManager(st, eng.MarketDataSink(), logger)
	if err := manager.Start(ctx); err != nil {
		_ = localNode.Close()
		return nil, fmt.Errorf("start market-data manager: %w", err)
	}

	return &controlPlane{
		service:    backend.New(router, manager),
		node:       localNode,
		marketData: manager,
		cfg:        cfg,
	}, nil
}

// close shuts the control plane down. The market-data manager is stopped first
// so quote producers stop before node.Close stops the engine and closes the
// market-data service (pushing into a closed service would be use-after-free).
// Closing the node then stops the engine and closes the store. Both steps are
// idempotent.
func (cp *controlPlane) close() error {
	if cp.marketData != nil {
		cp.marketData.Stop()
	}
	if err := cp.node.Close(); err != nil {
		return fmt.Errorf("close node: %w", err)
	}
	return nil
}

// runMCP loads the mcp-mode configuration, assembles the control plane, and
// serves the MCP surface over stdio until the process is signalled. No HTTP
// listener is opened in this mode.
func runMCP(args []string, logger *slog.Logger) error {
	cfg, err := config.Load(append([]string{"-mode", string(config.RunModeMCP)},
		args...), os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cp, err := setup(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if err := cp.close(); err != nil {
			logger.Error("shutdown error", "err", err)
		}
	}()

	logger.Info("serving mcp over stdio")
	if err := officermcp.RunStdio(ctx, sourceAdapter{cp.service}, nodeVersionSource{cp.node}); err != nil {
		return fmt.Errorf("run mcp stdio: %w", err)
	}
	logger.Info("mcp server stopped")
	return nil
}

// runServe loads the serve-mode configuration, assembles the control plane, and
// runs the HTTP server (dashboard, /api/*, and the streamable-HTTP MCP handler)
// until the process is signalled, then shuts down gracefully.
func runServe(args []string, logger *slog.Logger, buf *logtail.Buffer) error {
	cfg, err := config.Load(append([]string{"-mode", string(config.RunModeServe)},
		args...), os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cp, err := setup(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if err := cp.close(); err != nil {
			logger.Error("shutdown error", "err", err)
		}
	}()

	handler, err := buildServeHandler(cp, buf)
	if err != nil {
		return fmt.Errorf("build http handler: %w", err)
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}

	// The default bind asks for an OS-assigned free port, so the real
	// address is known only now. Publish it so dashboard and healthcheck
	// can find the URL.
	actual := listener.Addr().String()
	url := officerruntime.ClientURL(actual)
	if err := officerruntime.Write(cfg, officerruntime.State{
		Addr:      actual,
		URL:       url,
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		_ = listener.Close()
		return fmt.Errorf("write runtime state: %w", err)
	}
	defer func() {
		if err := officerruntime.Remove(cfg); err != nil {
			logger.Error("remove runtime state", "err", err)
		}
	}()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("panel listening", "addr", actual, "url", url)
		if err := srv.Serve(listener); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	// The listener is already bound, so the URL is reachable. Opening the
	// browser is best-effort and runs off the startup path so it never delays
	// listening or fails serve.
	if cfg.OpenBrowser {
		go func() {
			if err := openBrowser(url); err != nil {
				logger.Warn("could not open browser", "url", url, "err", err)
			}
		}()
	}

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining http")
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(),
		shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	if err := <-serveErr; err != nil {
		return fmt.Errorf("http server: %w", err)
	}
	logger.Info("http server stopped")
	return nil
}

// buildServeHandler assembles the serve-mode HTTP handler: the embedded SPA,
// the dashboard API, and the streamable-HTTP MCP handler mounted under /mcp.
// buf is the in-memory log tail exposed under the v1 service routes.
func buildServeHandler(cp *controlPlane, buf *logtail.Buffer) (http.Handler, error) {
	spa, err := officer.WebDist()
	if err != nil {
		return nil, fmt.Errorf("load embedded dashboard: %w", err)
	}

	mcpHandler, err := officermcp.Handler(sourceAdapter{cp.service}, nodeVersionSource{cp.node})
	if err != nil {
		return nil, fmt.Errorf("build mcp handler: %w", err)
	}

	router, err := httpapi.NewRouter(httpapi.Options{
		Service: cp.service,
		SPA:     spa,
		MCP:     http.StripPrefix(mcpPath, mcpHandler),
		Logs:    buf,
	})
	if err != nil {
		return nil, fmt.Errorf("build router: %w", err)
	}
	return router, nil
}

// runHealthcheck probes a running serve instance's /healthz endpoint and
// returns nil on a 200 response. It is the container healthcheck: it opens no
// store and no engine and makes exactly one request. It prefers the live
// address from the published runtime state (so a free-port serve is reachable),
// falling back to the configured address with wildcard hosts normalized to
// loopback.
func runHealthcheck(args []string) error {
	cfg, err := config.Load(append([]string{"-mode", string(config.RunModeServe)},
		args...), os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	base := officerruntime.ClientURL(cfg.HTTPAddr)
	if state, ok, err := officerruntime.Read(cfg); err != nil {
		return fmt.Errorf("read runtime state: %w", err)
	} else if ok {
		base = state.URL
	}

	url := base + "healthz"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build healthcheck request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck request to %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck %s returned HTTP %d", url, resp.StatusCode)
	}
	return nil
}

// joinURL safely joins base and path: trims trailing slash from base, then
// concatenates with path. Used to build API endpoints from the base URL.
func joinURL(base, path string) string {
	if len(base) > 0 && base[len(base)-1] == '/' {
		return base[:len(base)-1] + path
	}
	return base + path
}

// runDashboard discovers the live serve URL from the published runtime state
// and, unless suppressed, opens it in the default browser. It loads the same
// config as healthcheck (to locate the state file) and accepts an extra
// -no-open flag. It opens no store and no engine.
func runDashboard(args []string, logger *slog.Logger) error {
	noOpen, rest := extractNoOpen(args)

	cfg, err := config.Load(append([]string{"-mode", string(config.RunModeServe)},
		rest...), os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	state, ok, err := officerruntime.Read(cfg)
	if err != nil {
		return fmt.Errorf("read runtime state: %w", err)
	}
	if !ok {
		return fmt.Errorf("pit-officer serve does not appear to be running "+
			"(no runtime state at %s)", officerruntime.StatePath(cfg))
	}

	fmt.Printf("Dashboard: %s\n", joinURL(state.URL, "/"))
	fmt.Printf("API:       %s\n", joinURL(state.URL, "/api/v1"))
	fmt.Printf("API docs:  %s\n", joinURL(state.URL, "/docs"))
	fmt.Printf("MCP:       %s\n", joinURL(state.URL, "/mcp"))
	fmt.Printf("Addr:      %s\n", state.Addr)

	if noOpen {
		return nil
	}
	// Opening the browser is best-effort: a headless host or a missing opener
	// is not a failure of the command, so the error is logged and swallowed.
	if err := openBrowser(state.URL); err != nil {
		logger.Warn("could not open browser", "url", state.URL, "err", err)
	}
	return nil
}

// extractNoOpen removes the -no-open flag (in its bare and =value forms) from
// args, returning whether it was set and the remaining args to hand to
// config.Load, which rejects flags it does not recognize.
func extractNoOpen(args []string) (bool, []string) {
	noOpen := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "-no-open", "--no-open", "-no-open=true", "--no-open=true":
			noOpen = true
		case "-no-open=false", "--no-open=false":
			noOpen = false
		default:
			rest = append(rest, a)
		}
	}
	return noOpen, rest
}

// openBrowser launches the platform's default URL opener for url. It returns
// the opener's start error, if any; the caller treats opening as best-effort.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// nodeVersionSource adapts a node.Node to the MCP surface's VersionSource seam.
// It reads the engine version through the node rather than a captured handle,
// so access observes the current engine even after a backup restore rebuild.
type nodeVersionSource struct {
	node node.Node
}

// Version returns the node's current engine version.
func (v nodeVersionSource) Version() string { return v.node.EngineVersion() }

// sourceAdapter adapts a *backend.Service to the MCP surface's Source seam.
// The MCP package defines its own status vocabulary so it never imports the
// backend package; this adapter maps backend types onto mcp types. It exposes
// read-only data only - no secrets, no order-flow control.
type sourceAdapter struct {
	service *backend.Service
}

// Status maps the backend's aggregate status onto the MCP surface's Status.
func (a sourceAdapter) Status(ctx context.Context) (officermcp.Status, error) {
	status, err := a.service.Status(ctx)
	if err != nil {
		return officermcp.Status{}, err
	}

	nodes := make([]officermcp.NodeHealth, 0, len(status.Nodes))
	for _, n := range status.Nodes {
		nodes = append(nodes, officermcp.NodeHealth{
			Engine: officermcp.EngineHealth{
				Version:      n.Engine.Version,
				BuildProfile: n.Engine.BuildProfile,
				Running:      n.Engine.Running,
			},
			Store: officermcp.StoreHealth{
				Path:          n.Store.Path,
				SchemaVersion: n.Store.SchemaVersion,
				Reachable:     n.Store.Reachable,
			},
		})
	}
	return officermcp.Status{Nodes: nodes, Healthy: status.Healthy}, nil
}

// GetAccountState delegates to the backend service.
func (a sourceAdapter) GetAccountState(
	ctx context.Context, id domain.AccountID,
) (domain.Account, []domain.Limit, error) {
	return a.service.GetAccountState(ctx, id)
}

// ListLimits delegates to the backend service.
func (a sourceAdapter) ListLimits(
	ctx context.Context, account domain.AccountID,
) ([]domain.Limit, error) {
	return a.service.ListLimits(ctx, account)
}

// ListAudit delegates to the backend service.
func (a sourceAdapter) ListAudit(ctx context.Context, n int) ([]domain.AuditRow, error) {
	return a.service.ListAudit(ctx, n)
}

// CheckOrder delegates to the backend service.
func (a sourceAdapter) CheckOrder(
	ctx context.Context, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	return a.service.CheckOrder(ctx, probe)
}

// SetMarketDataInstrumentEnabled delegates to the backend service.
func (a sourceAdapter) SetMarketDataInstrumentEnabled(
	ctx context.Context, instanceID, externalSymbol string, enabled bool,
) error {
	return a.service.SetMarketDataInstrumentEnabled(ctx, instanceID, externalSymbol, enabled)
}

// CommandEnabled delegates to the backend's effective MCP-access read so the
// MCP surface can gate each tool by the operator's panel toggles.
func (a sourceAdapter) CommandEnabled(
	ctx context.Context, command string,
) (bool, error) {
	return a.service.CommandEnabled(ctx, command)
}
