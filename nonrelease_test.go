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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/internal/nonrelease"
	"go.openpit.dev/officer/internal/store/sqlite"
)

func TestNonReleaseGateConcurrentChoice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := Config{SQLitePath: filepath.Join(t.TempDir(), "own.db")}
		release := make(chan struct{})
		var prepares, builds atomic.Int32
		chosen := make(chan bool, 2)
		gate, err := newNonReleaseGate(t.Context(), cfg, "release.db", "/mcp", true, gateTestSPA,
			func(ctx context.Context) (http.Handler, error) {
				builds.Add(1)
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				return gateTestApp(ctx)
			}, func(ctx context.Context, got Config, path string, fromRelease bool) error {
				prepares.Add(1)
				chosen <- fromRelease
				<-release
				if got.SQLitePath != cfg.SQLitePath || path != "release.db" {
					return fmt.Errorf("prepare inputs = %q, %q", got.SQLitePath, path)
				}
				return ctx.Err()
			})
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Close()
		defer close(release)
		results := make(chan struct {
			fromRelease bool
			rec         *httptest.ResponseRecorder
		}, 2)
		for _, fromRelease := range []bool{true, false} {
			go func() {
				results <- struct {
					fromRelease bool
					rec         *httptest.ResponseRecorder
				}{fromRelease, serveExactPath(gate, http.MethodPost, "/app/api/v1/build/database",
					fmt.Sprintf(`{"fromRelease":%t}`, fromRelease))}
			}()
		}
		synctest.Wait()
		first, second := <-results, <-results
		if first.rec.Code == http.StatusConflict {
			first, second = second, first
		}
		if first.rec.Code != http.StatusAccepted || second.rec.Code != http.StatusConflict {
			t.Fatalf("concurrent choices = %d and %d, want one 202 and one 409",
				first.rec.Code, second.rec.Code)
		}
		if prepares.Load() != 1 || builds.Load() != 0 {
			t.Fatalf("while preparing: prepare calls = %d, build calls = %d, want 1 and 0",
				prepares.Load(), builds.Load())
		}
		if got := <-chosen; got != first.fromRelease {
			t.Fatalf("prepared choice = %v, accepted choice = %v", got, first.fromRelease)
		}
		gateTestStatus(t, gate, nonrelease.PhasePreparing, "")
		rec := serveExactPath(gate, http.MethodGet, "/application-only", "")
		if rec.Header().Get("X-Test-App") != "" {
			t.Fatal("application served before preparation completed")
		}
		release <- struct{}{}
		synctest.Wait()
		gateTestStatus(t, gate, nonrelease.PhaseReady, "")
		rec = serveExactPath(gate, http.MethodGet, "/application-only", "")
		if rec.Header().Get("X-Test-App") != "ready" || rec.Body.String() != "application" {
			t.Fatalf("ready request missed application handler: %d %s", rec.Code, rec.Body)
		}
		if builds.Load() != 1 {
			t.Fatalf("build calls = %d, want 1", builds.Load())
		}
	})
}

func TestNonReleaseGatePrepareFailureRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := Config{SQLitePath: filepath.Join(t.TempDir(), "own.db")}
		if err := os.WriteFile(cfg.SQLitePath, []byte("existing database"), 0600); err != nil {
			t.Fatal(err)
		}
		var prepares, builds atomic.Int32
		retry := make(chan struct{})
		gate, err := newNonReleaseGate(t.Context(), cfg, "release.db", "/mcp", true, gateTestSPA,
			func(ctx context.Context) (http.Handler, error) {
				builds.Add(1)
				return gateTestApp(ctx)
			}, func(context.Context, Config, string, bool) error {
				if prepares.Add(1) == 1 {
					return errors.New("snapshot failed")
				}
				<-retry
				return nil
			})
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Close()
		defer close(retry)
		rec := serveExactPath(gate, http.MethodPost, "/app/api/v1/build/database", `{"fromRelease":true}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("first choice = %d %s", rec.Code, rec.Body)
		}
		synctest.Wait()
		gateTestStatus(t, gate, nonrelease.PhaseFailed, "snapshot failed")
		if builds.Load() != 0 {
			t.Fatal("build ran after failed preparation")
		}
		if data, err := os.ReadFile(cfg.SQLitePath); err != nil || string(data) != "existing database" {
			t.Fatalf("prepare failure removed existing database: %q, %v", data, err)
		}
		rec = serveExactPath(gate, http.MethodPost, "/app/api/v1/build/database", `{"fromRelease":false}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("retry choice = %d %s, want 202", rec.Code, rec.Body)
		}
		gateTestStatus(t, gate, nonrelease.PhasePreparing, "")
		retry <- struct{}{}
		synctest.Wait()
		gateTestStatus(t, gate, nonrelease.PhaseReady, "")
		if prepares.Load() != 2 || builds.Load() != 1 {
			t.Fatalf("retry calls: prepare = %d, build = %d", prepares.Load(), builds.Load())
		}
	})
}

func TestNonReleaseGateBuildFailureRemovesDatabase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := Config{SQLitePath: filepath.Join(t.TempDir(), "own.db")}
		gate, err := newNonReleaseGate(t.Context(), cfg, "release.db", "/mcp", false, gateTestSPA,
			func(context.Context) (http.Handler, error) {
				return nil, errors.New("build failed")
			}, func(context.Context, Config, string, bool) error {
				for _, path := range sqlite.DatabaseFiles(cfg.SQLitePath) {
					if err := os.WriteFile(path, []byte("prepared"), 0600); err != nil {
						return err
					}
				}
				return nil
			})
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Close()
		rec := serveExactPath(gate, http.MethodPost, "/app/api/v1/build/database", `{"fromRelease":false}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("choice = %d %s", rec.Code, rec.Body)
		}
		synctest.Wait()
		gateTestStatus(t, gate, nonrelease.PhaseFailed, "build failed")
		for _, path := range sqlite.DatabaseFiles(cfg.SQLitePath) {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("build failure left own database %q: %v", path, err)
			}
		}
	})
}

func TestNonReleaseGateCloseWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := Config{SQLitePath: filepath.Join(t.TempDir(), "own.db")}
		release := make(chan struct{})
		var builds atomic.Int32
		gate, err := newNonReleaseGate(t.Context(), cfg, "release.db", "/mcp", true, gateTestSPA,
			func(ctx context.Context) (http.Handler, error) {
				builds.Add(1)
				return gateTestApp(ctx)
			}, func(ctx context.Context, cfg Config, _ string, _ bool) error {
				<-ctx.Done()
				<-release
				return os.WriteFile(cfg.SQLitePath, []byte("prepared"), 0600)
			})
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Close()
		defer close(release)
		rec := serveExactPath(gate, http.MethodPost, "/app/api/v1/build/database", `{"fromRelease":false}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("choice = %d %s", rec.Code, rec.Body)
		}
		closed := make(chan struct{})
		go func() { gate.Close(); close(closed) }()
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("Close returned while prepare was still blocked")
		default:
		}
		release <- struct{}{}
		<-closed
		gate.Close()
		if builds.Load() != 0 {
			t.Fatal("build ran after cancellation between prepare and build")
		}
		if data, err := os.ReadFile(cfg.SQLitePath); err != nil || string(data) != "prepared" {
			t.Fatalf("cancelled gate did not keep prepared database: %q, %v", data, err)
		}
		gateTestStatus(t, gate, nonrelease.PhaseFailed, context.Canceled.Error())
		rec = serveExactPath(gate, http.MethodPost, "/app/api/v1/build/database", `{"fromRelease":false}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("choice after Close = %d %s, want 409", rec.Code, rec.Body)
		}
	})
}

func TestNonReleaseGateRouter(t *testing.T) {
	const mcpPath = "/agent/mcp"
	gate, err := newNonReleaseGate(t.Context(), Config{}, "release.db", mcpPath, false,
		gateTestSPA, gateTestApp, func(context.Context, Config, string, bool) error {
			t.Fatal("refused request reached prepare")
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	gateTestStatus(t, gate, nonrelease.PhaseChoice, "")
	for _, path := range []string{"/", "/unknown-client-route"} {
		rec := serveExactPath(gate, http.MethodGet, path, "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") ||
			!strings.Contains(rec.Body.String(), "<html") {
			t.Fatalf("SPA %s = %d %s", path, rec.Code, rec.Body)
		}
	}
	rec := serveExactPath(gate, http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("healthz = %d %s", rec.Code, rec.Body)
	}
	t.Run("OpenAPI", func(t *testing.T) {
		rec := serveExactPath(gate, http.MethodGet, "/api/openapi.yaml", "")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/yaml; charset=utf-8" ||
			!strings.Contains(rec.Body.String(), "openapi:") {
			t.Fatalf("OpenAPI = %d, Content-Type %q, want 200 application/yaml; charset=utf-8 and YAML document",
				rec.Code, rec.Header().Get("Content-Type"))
		}
	})
	for _, tc := range []struct {
		name, target, origin, contentType, body, code string
		method                                        string
		status                                        int
	}{
		{"foreign host", "http://attacker.example/app/api/v1/build", "", "", "",
			"host_not_allowed", http.MethodGet, http.StatusForbidden},
		{"cross origin", "http://127.0.0.1/app/api/v1/build/database", "http://attacker.example",
			"application/json", `{"fromRelease":false}`, "origin_not_allowed", http.MethodPost, http.StatusForbidden},
		{"content type", "http://127.0.0.1/app/api/v1/build/database", "",
			"text/plain", `{"fromRelease":false}`, "unsupported_media_type", http.MethodPost, http.StatusUnsupportedMediaType},
		{"API manifest", "http://127.0.0.1/api/v1/route-manifest.json", "", "", "",
			"setup_required", http.MethodGet, http.StatusServiceUnavailable},
		{"REST accounts", "http://127.0.0.1/api/v1/accounts", "", "", "",
			"not_found", http.MethodGet, http.StatusNotFound},
		{"MCP GET", "http://127.0.0.1" + mcpPath, "", "", "",
			"setup_required", http.MethodGet, http.StatusServiceUnavailable},
		{"MCP POST", "http://127.0.0.1" + mcpPath, "", "application/json", `{}`,
			"setup_required", http.MethodPost, http.StatusServiceUnavailable},
		{"missing release", "http://127.0.0.1/app/api/v1/build/database", "",
			"application/json", `{"fromRelease":true}`, "validation", http.MethodPost, http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			req.Header.Set("Origin", tc.origin)
			rec := httptest.NewRecorder()
			gate.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("%s status = %d, want %d %s", tc.name, rec.Code, tc.status, tc.code)
			}
			var answer struct {
				Error  struct{ Code string } `json:"error"`
				Errors []httpx.ProblemError  `json:"errors"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
				t.Fatal(err)
			}
			code := answer.Error.Code
			if tc.code == "validation" {
				if len(answer.Errors) != 1 {
					t.Fatalf("validation problems = %s, want one problem", rec.Body)
				}
				code = answer.Errors[0].Code
			}
			if rec.Code != tc.status || code != tc.code {
				t.Fatalf("%s = %d %s, want %d %s", tc.name, rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}
	gateTestStatus(t, gate, nonrelease.PhaseChoice, "")
}

func gateTestStatus(t *testing.T, gate *NonReleaseGate, phase nonrelease.Phase, failure string) {
	t.Helper()
	rec := serveExactPath(gate, http.MethodGet, "/app/api/v1/build", "")
	var got nonrelease.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || got.Release || got.Phase != phase || got.Error != failure ||
		(got.Phase != nonrelease.PhaseReady && got.ReleaseDatabaseExists != gate.releaseDatabaseExists) {
		t.Fatalf("build status = %d %s, want non-release %s (%q)", rec.Code, rec.Body, phase, failure)
	}
}

// gateTestSPA stands in for the embedded panel, which holds only the committed
// placeholder until the web build runs.
func gateTestSPA() (fs.FS, error) {
	return fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}, nil
}

func gateTestApp(context.Context) (http.Handler, error) {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-App", "ready")
		if r.URL.Path == "/app/api/v1/build" {
			httpx.WriteJSON(w, http.StatusOK, nonrelease.Status{Phase: nonrelease.PhaseReady})
			return
		}
		_, _ = w.Write([]byte("application"))
	}), nil
}

func TestPrepareNonReleaseDatabase(t *testing.T) {
	for _, fromRelease := range []bool{true, false} {
		name := "empty"
		if fromRelease {
			name = "from release"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			release := filepath.Join(t.TempDir(), "release.db")
			key := testMasterKey(t)
			cfg := Config{SQLitePath: release, MasterKey: &key}
			st, realm := openNonReleaseTestStore(t, cfg, true)
			// Use the same store seeding path as TestRegisterTradingComposition.
			for _, enabled := range []bool{true, false} {
				label := "disabled"
				if enabled {
					label = "enabled"
				}
				if _, err := realm.CreateTradingConnection(ctx, domain.TradingConnection{
					Provider: domain.TradingProviderAlpaca, Label: label,
					Mode: domain.TradingModePaper, VenueIdentity: "paper-" + label,
					Credentials: "{}", Enabled: enabled,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			before, modified := nonReleaseFileState(t, release)
			own, err := nonrelease.DatabasePath(release, "own")
			if err != nil {
				t.Fatal(err)
			}
			cfg.SQLitePath = own
			if err := PrepareNonReleaseDatabase(ctx, cfg, release, fromRelease); err != nil {
				t.Fatal(err)
			}
			assertNonReleaseFileUnchanged(t, release, before, modified)
			assertNoPrepareFiles(t, own)
			ownStore, ownRealm := openNonReleaseTestStore(t, cfg, false)
			version, err := ownStore.SchemaVersion(ctx)
			if err != nil || version == 0 {
				t.Fatalf("own database is not migrated: version=%d, err=%v", version, err)
			}
			connections, err := ownRealm.ListTradingConnections(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 0
			if fromRelease {
				wantCount = 2
			}
			if len(connections) != wantCount {
				t.Errorf("own connections = %d, want %d", len(connections), wantCount)
			}
			for _, connection := range connections {
				if connection.Enabled {
					t.Errorf("own trading connection %q remains enabled", connection.Label)
				}
			}
			if err := ownStore.Close(); err != nil {
				t.Fatal(err)
			}
			cfg.SQLitePath = release
			releaseStore, releaseRealm := openNonReleaseTestStore(t, cfg, false)
			original, err := releaseRealm.ListTradingConnections(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(original) != 2 {
				t.Fatalf("release connections = %d, want 2", len(original))
			}
			for _, connection := range original {
				if connection.Enabled != (connection.Label == "enabled") {
					t.Errorf("release trading connection %q enabled=%v", connection.Label, connection.Enabled)
				}
			}
			if err := releaseStore.Close(); err != nil {
				t.Fatal(err)
			}
			for _, path := range sqlite.DatabaseFiles(own)[1:] {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("own sidecar remains %q: %v", path, err)
				}
			}
		})
	}
}

func TestPrepareNonReleaseDatabaseFailureCleansTemporaryFiles(t *testing.T) {
	for _, name := range []string{"invalid release", "missing release", "migration canceled"} {
		t.Run(name, func(t *testing.T) {
			release := filepath.Join(t.TempDir(), "release.db")
			own := release + ".non-release-own"
			ctx := context.Background()
			fromRelease := true
			if name == "invalid release" {
				if err := os.WriteFile(release, []byte("not an SQLite database"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "migration canceled" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
				fromRelease = false
			}
			err := PrepareNonReleaseDatabase(ctx, Config{SQLitePath: own}, release, fromRelease)
			if err == nil {
				t.Fatal("preparation unexpectedly succeeded")
			}
			if !strings.Contains(err.Error(), "officer: prepare non-release database:") {
				t.Fatalf("missing preparation step in error: %v", err)
			}
			if _, err := os.Lstat(own); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("failed preparation published own database: %v", err)
			}
			assertNoPrepareFiles(t, own)
			if name == "missing release" {
				if _, err := os.Lstat(release); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("preparation created missing release: %v", err)
				}
			}
		})
	}
}

func TestPrepareNonReleaseDatabaseRefusesExistingOrReleasePath(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "release.db")
	own := release + ".non-release-own"
	for _, path := range []string{release, own} {
		if err := os.WriteFile(path, []byte("leave untouched"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	releaseSpelling := dir + string(filepath.Separator) + "." + string(filepath.Separator) + "release.db"
	for _, path := range []string{own, releaseSpelling} {
		before, modified := nonReleaseFileState(t, filepath.Join(dir, filepath.Base(path)))
		if err := PrepareNonReleaseDatabase(context.Background(), Config{SQLitePath: path}, release, false); err == nil {
			t.Errorf("preparation accepted existing or release path %q", path)
		}
		assertNonReleaseFileUnchanged(t, filepath.Join(dir, filepath.Base(path)), before, modified)
		assertNoPrepareFiles(t, own)
	}
	if err := os.Remove(release); err != nil {
		t.Fatal(err)
	}
	if err := PrepareNonReleaseDatabase(context.Background(), Config{SQLitePath: releaseSpelling}, release, false); err == nil {
		t.Error("preparation accepted missing release path as own database")
	}
	for _, path := range sqlite.DatabaseFiles(release) {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("preparation created release database file %q: %v", path, err)
		}
	}
	assertNoPrepareFiles(t, release)
}

func TestPublishNonReleaseDatabaseRefusesExistingFile(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "temporary.db")
	own := filepath.Join(dir, "own.db")
	if err := os.WriteFile(tmp, []byte("new database"), 0600); err != nil {
		t.Fatal(err)
	}
	want := []byte("existing database")
	if err := os.WriteFile(own, want, 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishNonReleaseDatabase(tmp, own); err == nil {
		t.Error("publication accepted existing own database")
	}
	got, err := os.ReadFile(own)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("own database bytes = %q, want %q", got, want)
	}
}

func openNonReleaseTestStore(t *testing.T, cfg Config, migrate bool) (store.Store, store.RealmStore) {
	t.Helper()
	st, err := newStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if migrate {
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	realm, err := st.ForRealm(context.Background(), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	return st, realm
}

func nonReleaseFileState(t *testing.T, path string) ([32]byte, time.Time) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data), info.ModTime()
}

func assertNonReleaseFileUnchanged(t *testing.T, path string, before [32]byte, modified time.Time) {
	t.Helper()
	after, afterModified := nonReleaseFileState(t, path)
	if after != before {
		t.Errorf("release database bytes changed: %q", path)
	}
	if !afterModified.Equal(modified) {
		t.Errorf("release database modification time changed: %q", path)
	}
	for _, sidecar := range sqlite.DatabaseFiles(path)[1:] {
		if _, err := os.Lstat(sidecar); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("release sidecar %q appeared: %v", sidecar, err)
		}
	}
}

func assertNoPrepareFiles(t *testing.T, own string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(own))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), filepath.Base(own)+".prepare-") {
			t.Errorf("temporary preparation file remains: %s", entry.Name())
		}
	}
}
