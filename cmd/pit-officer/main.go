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

// Command pit-officer is the Pit Officer control-plane binary. It has four
// subcommands:
//
//	pit-officer mcp           local stdio MCP server (no listener)
//	pit-officer serve         always-on dashboard + MCP-over-HTTP
//	pit-officer dashboard     discover a serve URL and open the browser
//	pit-officer healthcheck   probe a serve instance's /healthz
//
// Release builds open the configured store, run migrations, then build the
// single local node and its engine seeded from that store. Non-release builds
// use a separate store per commit: serve asks for a database choice before
// creating it, and mcp requires that choice to have been made already.
// serve binds a loopback
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
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.openpit.dev/officer"
	frameworkapp "go.openpit.dev/officer/framework/app"
	fwbackend "go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	officerhttp "go.openpit.dev/officer/httpapi"
	"go.openpit.dev/officer/internal/config"
	"go.openpit.dev/officer/internal/logtail"
	"go.openpit.dev/officer/internal/nonrelease"
	officerruntime "go.openpit.dev/officer/internal/runtime"
	"go.openpit.dev/officer/web"
	"go.openpit.dev/openpit"
)

// mcpPath is the route the streamable-HTTP MCP handler is mounted under in
// serve mode.
const mcpPath = "/mcp"

// shutdownTimeout bounds the graceful HTTP shutdown before connections are
// forced closed.
const shutdownTimeout = 10 * time.Second

type lifecycleAction string

const (
	lifecycleStop    lifecycleAction = "stop"
	lifecycleRestart lifecycleAction = "restart"
)

func main() {
	// Tee the logger into a bounded in-memory ring buffer so the serve surface
	// can read back a tail of recent lines. The stderr handler is unchanged; the
	// buffer is bounded by both an entry count and a byte size, so a runaway log
	// loop cannot exhaust memory.
	buf := logtail.New(0, 0)
	logger := slog.New(logtail.NewHandler(slog.NewTextHandler(os.Stderr, nil), buf))
	// Route package-level slog calls in libraries through the same tee'd handler.
	slog.SetDefault(logger)

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

type fatalShutdown struct {
	logger *slog.Logger
	once   sync.Once
}

func newFatalShutdown(logger *slog.Logger) *fatalShutdown {
	return &fatalShutdown{logger: logger}
}

func (f *fatalShutdown) handle(err error) {
	f.once.Do(func() {
		f.logger.Error("fatal post-engine persistence error; exiting", "err", err)
		os.Exit(1)
	})
}

// setup performs the shared startup through the framework app builder.
func setup(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	fatalHook func(error),
) (*frameworkapp.App, error) {
	appConfig, err := resolveAppConfig(cfg)
	if err != nil {
		return nil, err
	}
	return buildApp(ctx, appConfig, web.Dist, logger, fatalHook)
}

func resolveAppConfig(cfg config.Config) (officer.Config, error) {
	if err := checkRuntimeLibraryPath(
		cfg.RuntimeLibraryPath, openpit.RuntimeLibraryPath(),
	); err != nil {
		return officer.Config{}, err
	}
	masterKey, err := config.ResolveMasterKey(cfg, os.LookupEnv)
	if err != nil {
		return officer.Config{}, err
	}
	return officer.Config{
		SQLitePath:   cfg.SQLitePath,
		MasterKey:    masterKey,
		AllowedHosts: cfg.AllowedHosts,
	}, nil
}

func buildApp(
	ctx context.Context, cfg officer.Config, spa frameworkapp.SPAFactory,
	logger *slog.Logger, fatalHook func(error),
) (*frameworkapp.App, error) {
	builder := frameworkapp.NewBuilder()
	if err := officer.Register(builder, cfg); err != nil {
		return nil, err
	}
	builder.SetSPAFactory(spa)
	return builder.Build(ctx, logger, fatalHook)
}

// checkRuntimeLibraryPath fails when the configured runtime library path names
// a library other than loaded, the one the OpenPit SDK reports it loaded at
// process start. Nothing after process start can change that library. The
// configured path is an operator input: surrounding whitespace is trimmed, an
// empty result means not configured, and the rest is cleaned before comparing.
func checkRuntimeLibraryPath(configured, loaded string) error {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return nil
	}
	if filepath.Clean(configured) == loaded {
		return nil
	}
	return fmt.Errorf(
		"runtime library path %q does not match the library the OpenPit SDK "+
			"loaded at process start, %q: set %s to that path before starting "+
			"pit-officer",
		configured, loaded, config.EnvRuntimeLibraryPath,
	)
}

// runMCP loads the mcp-mode configuration, assembles the app, and serves the
// MCP surface over stdio until the process is signalled. No HTTP listener is
// opened in this mode.
func runMCP(args []string, logger *slog.Logger) error {
	release, commit, err := buildIdentity()
	if err != nil {
		return err
	}
	cfg, err := config.Load(append([]string{"-mode", string(config.RunModeMCP)},
		args...), os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fatal := newFatalShutdown(logger)
	app, err := setupMCP(ctx, cfg, release, commit, logger, fatal.handle)
	if err != nil {
		return err
	}
	closeApp := func() error {
		if app == nil {
			return nil
		}
		defer func() { app = nil }()
		return app.Close()
	}
	defer func() {
		if err := closeApp(); err != nil {
			logger.Error("shutdown error", "err", err)
		}
	}()

	logger.Info("serving mcp over stdio")
	if err := app.RunMCPStdio(ctx, domain.Caller{
		Principal: domain.PrincipalOperator,
	}); err != nil {
		return fmt.Errorf("run mcp stdio: %w", err)
	}
	logger.Info("mcp server stopped")
	return nil
}

// runServe loads the serve-mode configuration, assembles the app, and runs the
// HTTP dashboard/API/MCP listener until the process is signalled.
func runServe(args []string, logger *slog.Logger, buf *logtail.Buffer) error {
	release, commit, err := buildIdentity()
	if err != nil {
		return err
	}
	cfg, err := config.Load(append([]string{"-mode", string(config.RunModeServe)},
		args...), os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fatal := newFatalShutdown(logger)
	lifecycleRequests := make(chan lifecycleAction, 1)
	handler, closeApp, err := setupServe(
		ctx, cfg, release, commit, web.Dist, logger, fatal.handle, buf, lifecycleRequests,
	)
	if err != nil {
		return err
	}
	defer func() {
		if err := closeApp(); err != nil {
			logger.Error("shutdown error", "err", err)
		}
	}()

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	httpStopped := false
	shutdownHTTP := func() error {
		if httpStopped {
			return nil
		}
		httpStopped = true
		shutdownCtx, cancel := context.WithTimeout(context.Background(),
			shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return errors.Join(err, srv.Close())
		}
		return nil
	}
	// Drain HTTP before closing the gate and its asynchronously built app,
	// including when startup or Serve fails.
	defer func() {
		if err := shutdownHTTP(); err != nil {
			logger.Error("http shutdown", "err", err)
		}
	}()

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
		err = errors.Join(err, listener.Close())
		return fmt.Errorf("write runtime state: %w", err)
	}
	runtimeStateWritten := true
	removeRuntimeState := func() error {
		if !runtimeStateWritten {
			return nil
		}
		runtimeStateWritten = false
		return officerruntime.Remove(cfg)
	}
	defer func() {
		if err := removeRuntimeState(); err != nil {
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

	var lifecycle lifecycleAction
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining http")
	case lifecycle = <-lifecycleRequests:
		logger.Warn("service lifecycle request received", "action", lifecycle)
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	}

	if err := shutdownHTTP(); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	if err := <-serveErr; err != nil {
		return fmt.Errorf("http server: %w", err)
	}
	logger.Info("http server stopped")
	if lifecycle == lifecycleRestart {
		if err := removeRuntimeState(); err != nil {
			return fmt.Errorf("remove runtime state before restart: %w", err)
		}
		if err := closeApp(); err != nil {
			return fmt.Errorf("shutdown app before restart: %w", err)
		}
		stop()
		logger.Warn("pit-officer restart requested; replacing current process")
		args := restartProcessArgs(os.Args, actual)
		if err := replaceCurrentProcess(args); err != nil {
			return fmt.Errorf("replace current process: %w", err)
		}
	}
	return nil
}

func buildIdentity() (release bool, commit string, err error) {
	release = fwbackend.ReleaseBuild()
	if !release {
		commit, err = fwbackend.BuildCommit()
	}
	return release, commit, err
}

func databaseForBuild(
	releasePath string, release bool, commit string,
) (path string, ownExists, releaseExists bool, err error) {
	if release {
		return releasePath, false, false, nil
	}
	path, err = nonrelease.DatabasePath(releasePath, commit)
	if err != nil {
		return "", false, false, err
	}
	if ownInfo, err := os.Lstat(path); err == nil {
		if !ownInfo.Mode().IsRegular() {
			return "", false, false, fmt.Errorf("non-release database %q is not a regular file (links and other file types are refused): remove that path before starting", path)
		}
		releaseInfo, err := os.Stat(releasePath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", false, false, err
		}
		if err == nil && os.SameFile(ownInfo, releaseInfo) {
			return "", false, false, fmt.Errorf("non-release database %q is the release file itself: remove that path before starting", path)
		}
		ownExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, false, err
	}
	releaseExists, err = nonrelease.ReleaseDatabaseExists(releasePath)
	if err != nil {
		return "", false, false, err
	}
	return path, ownExists, releaseExists, nil
}

func setupMCP(
	ctx context.Context, cfg config.Config, release bool, commit string,
	logger *slog.Logger, fatalHook func(error),
) (*frameworkapp.App, error) {
	path, ownExists, _, err := databaseForBuild(cfg.SQLitePath, release, commit)
	if err != nil {
		return nil, err
	}
	if !release && !ownExists {
		return nil, fmt.Errorf("non-release database %q is missing: choose the database on the first screen of pit-officer serve", path)
	}
	cfg.SQLitePath = path
	return setup(ctx, cfg, logger, fatalHook)
}

func setupServe(
	ctx context.Context, cfg config.Config, release bool, commit string,
	spa frameworkapp.SPAFactory, logger *slog.Logger, fatalHook func(error), buf *logtail.Buffer,
	lifecycleRequests chan<- lifecycleAction,
) (http.Handler, func() error, error) {
	releasePath := cfg.SQLitePath
	if !release {
		removed, err := nonrelease.RemoveStale(releasePath, commit)
		for _, path := range removed {
			logger.Info("removed stale non-release database file", "path", path)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	path, ownExists, releaseExists, err := databaseForBuild(releasePath, release, commit)
	if err != nil {
		return nil, nil, err
	}
	cfg.SQLitePath = path
	appConfig, err := resolveAppConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	var app *frameworkapp.App
	var gate *officer.NonReleaseGate
	closeApp := func() error {
		if gate != nil {
			gate.Close()
		}
		// Close waits for the callback before this app variable is read.
		if app == nil {
			return nil
		}
		defer func() { app = nil }()
		return app.Close()
	}
	build := func(ctx context.Context) (http.Handler, error) {
		built, err := buildApp(ctx, appConfig, spa, logger, fatalHook)
		if err != nil {
			return nil, err
		}
		handler, err := buildServeHandler(built, buf, lifecycleRequests)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("build http handler: %w", err), built.Close())
		}
		app = built
		return handler, nil
	}
	if release || ownExists {
		handler, err := build(ctx)
		if err != nil {
			return nil, nil, err
		}
		return handler, closeApp, nil
	}
	gate, err = officer.NewNonReleaseGate(ctx, appConfig, releasePath, mcpPath, releaseExists, spa, build)
	if err != nil {
		return nil, nil, err
	}
	return gate, closeApp, nil
}

func restartProcessArgs(args []string, httpAddr string) []string {
	out := append([]string(nil), args...)
	for i := 1; i < len(out); i++ {
		switch {
		case out[i] == "-http-addr" || out[i] == "--http-addr":
			if i+1 < len(out) {
				out[i+1] = httpAddr
				return append(out, "-open-browser=false")
			}
			return append(out, httpAddr, "-open-browser=false")
		case strings.HasPrefix(out[i], "-http-addr="):
			out[i] = "-http-addr=" + httpAddr
			return append(out, "-open-browser=false")
		case strings.HasPrefix(out[i], "--http-addr="):
			out[i] = "--http-addr=" + httpAddr
			return append(out, "-open-browser=false")
		}
	}
	return append(out, "-http-addr", httpAddr, "-open-browser=false")
}

func buildServeHandler(
	app *frameworkapp.App,
	buf *logtail.Buffer,
	lifecycleRequests chan<- lifecycleAction,
) (http.Handler, error) {
	recordLifecycle := func(ctx context.Context, action lifecycleAction) error {
		auditAction, detail, err := lifecycleAudit(action)
		if err != nil {
			return err
		}
		return app.RecordServiceLifecycle(ctx, auditAction, detail)
	}
	controller := &serviceLifecycleController{
		requests: lifecycleRequests,
		record:   recordLifecycle,
	}
	return app.BuildServeHandler(
		buf,
		mcpPath,
		officerhttp.ServiceLifecycleRoutes(
			controller.handler(lifecycleRestart),
			controller.handler(lifecycleStop),
		)...,
	)
}

type lifecycleRecorder func(context.Context, lifecycleAction) error

type serviceLifecycleController struct {
	requests chan<- lifecycleAction
	record   lifecycleRecorder
	mu       sync.Mutex
	pending  bool
}

func (c *serviceLifecycleController) handler(action lifecycleAction) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.pending {
			http.Error(w, "lifecycle request already pending", http.StatusConflict)
			return
		}
		if err := c.record(r.Context(), action); err != nil {
			http.Error(w, "audit lifecycle request", http.StatusInternalServerError)
			return
		}
		c.pending = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		if _, err := w.Write([]byte(`{"accepted":true}` + "\n")); err != nil {
			slog.Error("write lifecycle acceptance response", "error", err)
		}
		c.requests <- action
	})
}

func lifecycleAudit(action lifecycleAction) (domain.AuditAction, domain.AuditDetail, error) {
	switch action {
	case lifecycleRestart:
		return domain.AuditActionRestartService,
			domain.NewAuditDetail(domain.AuditDetailRestartService, nil), nil
	case lifecycleStop:
		return domain.AuditActionStopService,
			domain.NewAuditDetail(domain.AuditDetailStopService, nil), nil
	default:
		return "", domain.AuditDetail{}, fmt.Errorf(
			"unknown service lifecycle action %q", action,
		)
	}
}

// runHealthcheck probes a running serve instance's /healthz endpoint and
// returns nil on a 200 response. It is the container healthcheck: it opens no
// store and no engine and makes exactly one request. It prefers the live
// address from the published runtime state (so a free-port serve is reachable),
// falling back to the configured address with wildcard hosts normalized to
// loopback.
func runHealthcheck(args []string) (resultErr error) {
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
	defer func() { resultErr = errors.Join(resultErr, resp.Body.Close()) }()

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
