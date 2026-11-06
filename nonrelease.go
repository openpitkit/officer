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

package officer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	frameworkapp "go.openpit.dev/officer/framework/app"
	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/httpapi"
	"go.openpit.dev/officer/internal/nonrelease"
	"go.openpit.dev/officer/internal/store/sqlite"
)

// PrepareNonReleaseDatabase creates cfg.SQLitePath as a migrated database with
// all trading connections disabled, optionally copying the release database.
// It never overwrites an existing own file or opens the release file for
// writing. Preparation happens in a temporary file beside the own path.
// The own path's filesystem must support hard links: publication hard-links the
// temporary file to the own path so concurrent preparation cannot replace it.
// An error can follow successful publication only if removing the temporary file
// fails, and then states that the own database is in place.
func PrepareNonReleaseDatabase(
	ctx context.Context, cfg Config, releasePath string, fromRelease bool,
) (result error) {
	own, err := filepath.Abs(cfg.SQLitePath)
	if err != nil {
		return fmt.Errorf("officer: prepare non-release database: own path: %w", err)
	}
	release, err := filepath.Abs(releasePath)
	if err != nil {
		return fmt.Errorf("officer: prepare non-release database: release path: %w", err)
	}
	if own == release {
		return fmt.Errorf("officer: prepare non-release database: own path %q equals release path", own)
	}
	if _, err := os.Lstat(own); err == nil {
		return fmt.Errorf("officer: prepare non-release database: own path %q already exists", own)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("officer: prepare non-release database: stat own path %q: %w", own, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(own), filepath.Base(own)+".prepare-*")
	if err != nil {
		return fmt.Errorf("officer: prepare non-release database: create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	published := false
	defer func() {
		if err := sqlite.RemoveDatabaseFiles(tmpPath); err != nil {
			if published {
				err = fmt.Errorf("officer: prepare non-release database: own database %q is published; remove temporary database: %w", own, err)
			} else {
				err = fmt.Errorf("officer: prepare non-release database: remove temporary database: %w", err)
			}
			result = errors.Join(result, err)
		}
	}()
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("officer: prepare non-release database: close temporary file: %w", err)
	}
	if fromRelease {
		if err := sqlite.Snapshot(ctx, releasePath, tmpPath); err != nil {
			return fmt.Errorf("officer: prepare non-release database: snapshot release: %w", err)
		}
	}
	cfg.SQLitePath = tmpPath
	if err := func() (result error) {
		st, err := newStore(cfg)
		if err != nil {
			return fmt.Errorf("officer: prepare non-release database: open temporary store: %w", err)
		}
		defer func() {
			if err := st.Close(); err != nil {
				result = errors.Join(result, fmt.Errorf("officer: prepare non-release database: close temporary store: %w", err))
			}
		}()
		if err := st.Migrate(ctx); err != nil {
			return fmt.Errorf("officer: prepare non-release database: migrate: %w", err)
		}
		realm, err := st.ForRealm(ctx, domain.DefaultRealm)
		if err != nil {
			return fmt.Errorf("officer: prepare non-release database: bind realm: %w", err)
		}
		connections, err := realm.ListTradingConnections(ctx)
		if err != nil {
			return fmt.Errorf("officer: prepare non-release database: list trading connections: %w", err)
		}
		for _, connection := range connections {
			if connection.Enabled {
				if err := realm.SetTradingConnectionEnabled(ctx, connection.ExternalID, false); err != nil {
					return fmt.Errorf("officer: prepare non-release database: disable trading connection %q: %w", connection.ExternalID, err)
				}
			}
		}
		return nil
	}(); err != nil {
		return err
	}
	if err := publishNonReleaseDatabase(tmpPath, own); err != nil {
		return err
	}
	published = true
	return nil
}

func publishNonReleaseDatabase(tmpPath, own string) error {
	if err := os.Link(tmpPath, own); err != nil {
		return fmt.Errorf("officer: prepare non-release database: publish %q by hard link: %w", own, err)
	}
	return nil
}

// NonReleaseGate serves the panel shell, both build routes, health, the OpenAPI
// document and API docs before ready. Before ready it also returns 503
// setup_required on the MCP path and route manifest, and a typed 404 on other
// /api/v1 and /app/api/v1 paths.
// Its mutex owns every phase transition and preparation launch; its atomic
// handler publishes only a fully built application, once ready.
// A gate must not be copied after construction.
type NonReleaseGate struct {
	mu                    sync.Mutex
	ctx                   context.Context
	cancel                context.CancelFunc
	work                  sync.WaitGroup
	handler               atomic.Pointer[http.Handler]
	phase                 nonrelease.Phase
	failure               string
	releaseDatabaseExists bool
	cfg                   Config
	releasePath           string
	prepare               func(context.Context, Config, string, bool) error
	build                 func(context.Context) (http.Handler, error)
}

// NewNonReleaseGate serves a first-screen database choice on cfg.SQLitePath,
// which the caller must already have derived as its own per-commit path.
// mcpPath is where the application built by build mounts its MCP handler;
// before ready, the gate answers that path with 503 setup_required.
// spa loads the panel the gate serves until ready.
// Each accepted choice prepares the database at cfg.SQLitePath, then calls
// build with the gate's context.
// The caller owns the application returned by build and its eventual cleanup.
// Close cancels that context, so call it at shutdown after the server stops
// serving, not when the gate reaches ready. Before returning an error, build
// must close everything it opened: the gate then removes its own database files.
func NewNonReleaseGate(
	ctx context.Context, cfg Config, releasePath, mcpPath string,
	releaseDatabaseExists bool, spa frameworkapp.SPAFactory,
	build func(context.Context) (http.Handler, error),
) (*NonReleaseGate, error) {
	return newNonReleaseGate(ctx, cfg, releasePath, mcpPath, releaseDatabaseExists, spa, build,
		PrepareNonReleaseDatabase)
}

func newNonReleaseGate(
	ctx context.Context, cfg Config, releasePath, mcpPath string,
	releaseDatabaseExists bool, spaFactory frameworkapp.SPAFactory,
	build func(context.Context) (http.Handler, error),
	prepare func(context.Context, Config, string, bool) error,
) (*NonReleaseGate, error) {
	if spaFactory == nil || build == nil || prepare == nil {
		return nil, errors.New("officer: non-release gate requires SPA, prepare and build functions")
	}
	guards, err := browserGuards(cfg)
	if err != nil {
		return nil, err
	}
	spa, err := spaFactory()
	if err != nil {
		return nil, fmt.Errorf("officer: non-release gate: load panel: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	g := &NonReleaseGate{
		ctx: ctx, cancel: cancel, cfg: cfg, releasePath: releasePath,
		releaseDatabaseExists: releaseDatabaseExists, phase: nonrelease.PhaseChoice,
		prepare: prepare, build: build,
	}
	panel := &httpx.RouteRegistry{}
	panel.Register(nonrelease.BuildRoute(g.status))
	panel.Register(nonrelease.DatabaseRoute(g.choose))
	handler, err := httpx.NewRouter(httpx.RouterConfig{
		Surfaces: httpx.V1Surfaces(&httpx.RouteRegistry{}, panel), Guards: guards,
		SPA: spa, BodyLimit: httpapi.BodyLimitPolicy(),
		ExtraMounts: httpapi.ExtraMounts(),
		MCP: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			httpx.WriteErr(w, domain.ErrSetupRequired)
		}),
		MCPPath: mcpPath,
		Authorizer: accessAuthorizer(func(context.Context, domain.Caller, string) error {
			return domain.ErrSetupRequired
		}),
		CallerResolver: func(*http.Request) (domain.Caller, error) {
			return domain.Caller{}, domain.ErrSetupRequired
		},
		AccessMode: func() httpx.AccessMode { return httpx.AccessModeSetup },
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("officer: non-release gate: build router: %w", err)
	}
	g.handler.Store(&handler)
	return g, nil
}

// ServeHTTP atomically selects the gate router until ready, then the app.
func (g *NonReleaseGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*g.handler.Load()).ServeHTTP(w, r)
}

func (g *NonReleaseGate) status() nonrelease.Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.statusLocked()
}

func (g *NonReleaseGate) statusLocked() nonrelease.Status {
	return nonrelease.Status{
		Phase: g.phase, ReleaseDatabaseExists: g.releaseDatabaseExists,
		Error: g.failure,
	}
}

// choose accepts only choice or failed, rejects a missing release source,
// and launches exactly one preparation before returning the preparing status.
func (g *NonReleaseGate) choose(fromRelease bool) (nonrelease.Status, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.phase != nonrelease.PhaseChoice && g.phase != nonrelease.PhaseFailed {
		return nonrelease.Status{}, fmt.Errorf("%w: database choice in phase %q", domain.ErrConflict, g.phase)
	}
	if err := g.ctx.Err(); err != nil {
		return nonrelease.Status{}, fmt.Errorf("%w: non-release gate closed: %v", domain.ErrConflict, err)
	}
	if fromRelease && !g.releaseDatabaseExists {
		return nonrelease.Status{}, fmt.Errorf("%w: release database does not exist", domain.ErrInvalid)
	}
	g.phase, g.failure = nonrelease.PhasePreparing, ""
	g.work.Add(1)
	go g.prepareAndBuild(fromRelease)
	return g.statusLocked(), nil
}

// prepareAndBuild leaves preparation failures alone: prepare owns its cleanup
// and may have refused an existing file. A build failure removes the prepared
// own database and joins cleanup errors so a new choice can prepare again.
// Cancellation after preparation skips build and keeps the prepared database.
func (g *NonReleaseGate) prepareAndBuild(fromRelease bool) {
	defer g.work.Done()
	err := g.prepare(g.ctx, g.cfg, g.releasePath, fromRelease)
	var handler http.Handler
	if err == nil {
		err = g.ctx.Err()
		if err == nil {
			handler, err = g.build(g.ctx)
			if err == nil && handler == nil {
				err = errors.New("officer: non-release build returned no handler")
			}
			if err != nil {
				err = errors.Join(err, sqlite.RemoveDatabaseFiles(g.cfg.SQLitePath))
			}
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		g.phase, g.failure = nonrelease.PhaseFailed, err.Error()
		return
	}
	g.phase = nonrelease.PhaseReady
	g.handler.Store(&handler)
}

// Close cancels the gate context and waits until prepare and build have
// returned. Neither can run after Close returns, including for a later choice.
// build receives this same gate context, so Close cancels any contexts the
// application derived from it. The caller must still close the application itself.
// Call Close at shutdown after the server stops serving, not upon reaching ready.
// Close is idempotent and may be called concurrently.
func (g *NonReleaseGate) Close() {
	g.mu.Lock()
	g.cancel()
	g.mu.Unlock()
	g.work.Wait()
}
