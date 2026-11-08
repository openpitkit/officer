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

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"go.openpit.dev/officer/framework/auth"
	fwbackend "go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/internal/config"
	"go.openpit.dev/officer/internal/nonrelease"
	officerruntime "go.openpit.dev/officer/internal/runtime"
	"go.openpit.dev/openpit"
)

const testBuildCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// testSPA stands in for the embedded panel, which holds only the committed
// placeholder until the web build runs.
func testSPA() (fs.FS, error) {
	return fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}, nil
}

func TestDatabaseForRelease(t *testing.T) {
	dir := t.TempDir()
	releasePath := dir + string(filepath.Separator) + "." + string(filepath.Separator) + "officer.db"
	stale := filepath.Join(dir, "officer.db.non-release-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	files := snapshotDatabaseFiles(t, stale, stale+"-wal")
	path, _, _, err := databaseForBuild(releasePath, true, testBuildCommit)
	if err != nil {
		t.Fatal(err)
	}
	if path != releasePath {
		t.Fatalf("release path = %q, want configured path %q", path, releasePath)
	}
	assertDatabaseFilesUnchanged(t, files)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(files) {
		t.Fatalf("release selection created or removed files: got %d, want %d", len(entries), len(files))
	}
}

func TestDatabaseForNonRelease(t *testing.T) {
	for _, withRelease := range []bool{false, true} {
		t.Run(fmt.Sprintf("release-exists-%t", withRelease), func(t *testing.T) {
			dir := t.TempDir()
			releasePath := filepath.Join(dir, "officer.db")
			var files map[string]databaseFileSnapshot
			if withRelease {
				files = snapshotDatabaseFiles(t, releasePath, releasePath+"-wal", releasePath+"-shm", releasePath+"-journal")
			}
			path, ownExists, releaseExists, err := databaseForBuild(releasePath, false, testBuildCommit)
			if err != nil {
				t.Fatal(err)
			}
			want := releasePath + ".non-release-" + testBuildCommit
			if path != want || ownExists || releaseExists != withRelease {
				t.Fatalf("database selection = (%q, %t, %t), want (%q, false, %t)", path, ownExists, releaseExists, want, withRelease)
			}
			assertDatabaseFilesUnchanged(t, files)
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != len(files) {
				t.Fatalf("database selection created files: got %d, want %d", len(entries), len(files))
			}
		})
	}
}

func TestSetupServeRemovesStaleBeforeConfigChecks(t *testing.T) {
	cfg := nonReleaseTestConfig(t)
	cfg.RuntimeLibraryPath = "another-runtime-library"
	releasePath := cfg.SQLitePath
	own := releasePath + ".non-release-" + testBuildCommit
	files := snapshotDatabaseFiles(t, releasePath, releasePath+"-wal", own, own+"-wal", own+"-shm", own+"-journal", filepath.Join(filepath.Dir(releasePath), "unrelated"))
	stale := releasePath + ".non-release-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	snapshotDatabaseFiles(t, stale, stale+"-wal")
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	_, closeApp, err := setupServe(context.Background(), cfg, false, testBuildCommit, testSPA, logger, nil, nil, make(chan lifecycleAction, 1))
	if closeApp != nil {
		t.Cleanup(func() {
			if err := closeApp(); err != nil {
				t.Errorf("close serve: %v", err)
			}
		})
	}
	if err == nil || !strings.Contains(err.Error(), "PIT_RUNTIME_LIBRARY_PATH") {
		t.Fatalf("serve error = %v, want runtime-library path refusal after stale removal", err)
	}
	logged := make(map[string]bool)
	decoder := json.NewDecoder(&logs)
	for {
		var entry struct {
			Level string `json:"level"`
			Path  string `json:"path"`
		}
		if err := decoder.Decode(&entry); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		logged[entry.Path] = entry.Level == "INFO"
	}
	for _, path := range []string{stale, stale + "-wal"} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale file %q stat = %v, want not exist", path, err)
		}
		if !logged[path] {
			t.Fatalf("removed path %q missing from info logs: %v", path, logged)
		}
	}
	assertDatabaseFilesUnchanged(t, files)
}

func TestDatabaseForBuildRefusesErrors(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Run("missing-commit", func(t *testing.T) {
		dir := t.TempDir()
		if _, _, _, err := databaseForBuild(filepath.Join(dir, "officer.db"), false, ""); err == nil {
			t.Fatal("database selection accepted a missing commit")
		}
		assertEmptyDirectory(t, dir)
	})
	t.Run("stale-removal", func(t *testing.T) {
		releasePath := filepath.Join(t.TempDir(), "officer.db")
		stale := releasePath + ".non-release-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		if err := os.Mkdir(stale, 0o700); err != nil {
			t.Fatal(err)
		}
		snapshotDatabaseFiles(t, filepath.Join(stale, "child"))
		cfg := config.Config{SQLitePath: releasePath}
		_, closeApp, err := setupServe(context.Background(), cfg, false, testBuildCommit, testSPA, logger, nil, nil, make(chan lifecycleAction, 1))
		if closeApp != nil {
			t.Cleanup(func() {
				if err := closeApp(); err != nil {
					t.Errorf("close serve: %v", err)
				}
			})
		}
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) || pathErr.Op != "remove" || pathErr.Path != stale || !strings.Contains(err.Error(), stale) {
			t.Fatalf("serve error = %v, want a remove *os.PathError for %q", err, stale)
		}
	})
	t.Run("release-is-directory", func(t *testing.T) {
		releasePath := filepath.Join(t.TempDir(), "officer.db")
		if err := os.Mkdir(releasePath, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := databaseForBuild(releasePath, false, testBuildCommit); err == nil {
			t.Fatal("database selection accepted a release path that is a directory")
		}
	})
}

func TestSetupRefusesLinkedDatabase(t *testing.T) {
	for _, command := range []string{"serve", "mcp"} {
		for _, link := range []string{"symlink", "hard-link", "hard-link-release-symlink"} {
			t.Run(command+"/"+link, func(t *testing.T) {
				cfg := nonReleaseTestConfig(t)
				dir := filepath.Dir(cfg.SQLitePath)
				releaseTarget := cfg.SQLitePath
				if link == "hard-link-release-symlink" {
					releaseTarget = filepath.Join(dir, "release-target.db")
				}
				logger := slog.New(slog.NewTextHandler(io.Discard, nil))
				fatalHook := func(err error) {
					t.Errorf("unexpected fatal shutdown: %v", err)
				}
				releaseConfig := cfg
				releaseConfig.SQLitePath = releaseTarget
				initial, err := setup(context.Background(), releaseConfig, logger, fatalHook)
				if err != nil {
					t.Fatal(err)
				}
				if err := initial.Close(); err != nil {
					t.Fatal(err)
				}
				releaseBytes, err := os.ReadFile(releaseTarget)
				if err != nil {
					t.Fatal(err)
				}
				releaseInfo, err := os.Stat(releaseTarget)
				if err != nil {
					t.Fatal(err)
				}
				files := map[string]databaseFileSnapshot{
					releaseTarget: {data: releaseBytes, modTime: releaseInfo.ModTime()},
				}
				makeSymlink := func(target, path string) {
					t.Helper()
					if err := os.Symlink(target, path); err != nil {
						if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314)) {
							t.Skipf("os.Symlink requires Windows privilege: %v", err)
						}
						t.Fatal(err)
					}
				}
				if releaseTarget != cfg.SQLitePath {
					makeSymlink(releaseTarget, cfg.SQLitePath)
				}
				own := cfg.SQLitePath + ".non-release-" + testBuildCommit
				reason := "is the release file itself"
				if link == "symlink" {
					makeSymlink(releaseTarget, own)
					reason = "is not a regular file (links and other file types are refused)"
				} else if err := os.Link(releaseTarget, own); err != nil {
					t.Fatal(err)
				}
				entries := snapshotDirectoryEntries(t, dir)
				var closeApp func() error
				if command == "serve" {
					_, closeApp, err = setupServe(context.Background(), cfg, false, testBuildCommit, testSPA, logger, fatalHook, nil, make(chan lifecycleAction, 1))
				} else {
					app, setupErr := setupMCP(context.Background(), cfg, false, testBuildCommit, logger, fatalHook)
					err = setupErr
					if app != nil {
						closeApp = app.Close
					}
				}
				assertDirectoryEntriesUnchanged(t, dir, entries)
				if closeApp != nil {
					if closeErr := closeApp(); closeErr != nil {
						t.Errorf("close unexpectedly opened %s: %v", command, closeErr)
					}
				}
				want := fmt.Sprintf("non-release database %q %s: remove that path before starting", own, reason)
				if err == nil || err.Error() != want {
					t.Errorf("linked database error = %v, want %q", err, want)
				}
				assertDatabaseFilesUnchanged(t, files)
			})
		}
	}
}

func TestSetupServeRelease(t *testing.T) {
	for _, withStale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale-exists-%t", withStale), func(t *testing.T) {
			cfg := nonReleaseTestConfig(t)
			dir := filepath.Dir(cfg.SQLitePath)
			var files map[string]databaseFileSnapshot
			if withStale {
				stale := cfg.SQLitePath + ".non-release-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				files = snapshotDatabaseFiles(t, stale, stale+"-wal", stale+".prepare-1")
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			handler, closeApp, err := setupServe(context.Background(), cfg, true, "", testSPA, logger, func(err error) {
				t.Errorf("unexpected fatal shutdown: %v", err)
			}, nil, make(chan lifecycleAction, 1))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := closeApp(); err != nil {
					t.Errorf("close release serve: %v", err)
				}
			})
			assertApplicationSession(t, handler)
			if _, err := os.Stat(cfg.SQLitePath); err != nil {
				t.Fatalf("configured release database stat: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.Contains(entry.Name(), ".non-release-") {
					if _, exists := files[filepath.Join(dir, entry.Name())]; !exists {
						t.Errorf("release serve created non-release entry %q", entry.Name())
					}
				}
			}
			assertDatabaseFilesUnchanged(t, files)
		})
	}
}

func TestSetupMCPPreservesPreparation(t *testing.T) {
	cfg := nonReleaseTestConfig(t)
	own := cfg.SQLitePath + ".non-release-" + testBuildCommit
	ownConfig := cfg
	ownConfig.SQLitePath = own
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fatalHook := func(err error) {
		t.Errorf("unexpected fatal shutdown: %v", err)
	}
	initial, err := setup(context.Background(), ownConfig, logger, fatalHook)
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	stale := cfg.SQLitePath + ".non-release-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	files := snapshotDatabaseFiles(t, stale, own+".prepare-1")
	app, err := setupMCP(context.Background(), cfg, false, testBuildCommit, logger, fatalHook)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	assertDatabaseFilesUnchanged(t, files)
}

func snapshotDirectoryEntries(t *testing.T, dir string) map[string]os.FileInfo {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string]os.FileInfo, len(entries))
	for _, entry := range entries {
		info, err := os.Lstat(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name()] = info
	}
	return files
}

func assertDirectoryEntriesUnchanged(t *testing.T, dir string, want map[string]os.FileInfo) {
	t.Helper()
	got := snapshotDirectoryEntries(t, dir)
	if len(got) != len(want) {
		t.Errorf("directory entries changed: got %d, want %d", len(got), len(want))
	}
	for name, before := range want {
		after, exists := got[name]
		if !exists {
			t.Errorf("directory entry %q was removed", name)
			continue
		}
		if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			t.Errorf("directory entry %q changed", name)
		}
	}
}

func TestSetupServeNonRelease(t *testing.T) {
	cfg := nonReleaseTestConfig(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, closeApp, err := setupServe(context.Background(), cfg, false, testBuildCommit, testSPA, logger, func(err error) {
		t.Errorf("unexpected fatal shutdown: %v", err)
	}, nil, make(chan lifecycleAction, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeApp(); err != nil {
			t.Errorf("close serve: %v", err)
		}
	})
	if got := serveBuildStatus(t, handler); got.Phase != nonrelease.PhaseChoice || got.Release || got.ReleaseDatabaseExists {
		t.Fatalf("initial build status = %+v, want non-release choice without release database", got)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/app/api/v1/build/database", strings.NewReader(`{"fromRelease":false}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("choose empty database = %d %s, want 202", rec.Code, rec.Body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		got := serveBuildStatus(t, handler)
		if got.Phase == nonrelease.PhaseReady {
			break
		}
		if got.Phase != nonrelease.PhasePreparing {
			t.Fatalf("build status after choice = %+v, want preparing or ready", got)
		}
		if ctx.Err() != nil {
			t.Fatalf("build did not reach ready before deadline: %+v", got)
		}
	}
	assertApplicationSession(t, handler)
	own := cfg.SQLitePath + ".non-release-" + testBuildCommit
	if _, err := os.Stat(own); err != nil {
		t.Fatalf("own database stat: %v", err)
	}
	if _, err := os.Stat(cfg.SQLitePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("release database stat = %v, want not exist", err)
	}
	if err := closeApp(); err != nil {
		t.Fatal(err)
	}

	// The next start must reuse the database without asking for a choice.
	reused, closeReused, err := setupServe(context.Background(), cfg, false, testBuildCommit, testSPA, logger, func(err error) {
		t.Errorf("unexpected fatal shutdown: %v", err)
	}, nil, make(chan lifecycleAction, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeReused(); err != nil {
			t.Errorf("close reused serve: %v", err)
		}
	})
	if got := serveBuildStatus(t, reused); got.Phase != nonrelease.PhaseReady || got.Release {
		t.Fatalf("reused build status = %+v, want non-release ready", got)
	}
	assertApplicationSession(t, reused)
	if err := closeReused(); err != nil {
		t.Fatal(err)
	}
	mcpApp, err := setupMCP(context.Background(), cfg, false, testBuildCommit, logger, func(err error) {
		t.Errorf("unexpected fatal shutdown: %v", err)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mcpApp.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSetupServeClosePreventsChoice(t *testing.T) {
	cfg := nonReleaseTestConfig(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, closeApp, err := setupServe(context.Background(), cfg, false, testBuildCommit, testSPA, logger, func(err error) {
		t.Errorf("unexpected fatal shutdown: %v", err)
	}, nil, make(chan lifecycleAction, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeApp(); err != nil {
			t.Errorf("close serve: %v", err)
		}
	})
	gate, ok := handler.(interface{ Close() })
	if !ok {
		t.Fatal("missing database did not produce a gate")
	}
	// Also drain the gate if the cleanup under test fails to close it.
	t.Cleanup(gate.Close)
	if err := closeApp(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/app/api/v1/build/database", strings.NewReader(`{"fromRelease":false}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "gate closed") {
		t.Fatalf("database choice after serve cleanup = %d %s, want 409 gate closed", rec.Code, rec.Body)
	}
	assertEmptyDirectory(t, filepath.Dir(cfg.SQLitePath))
}

func TestSetupServeChecksBeforeChoice(t *testing.T) {
	for _, check := range []string{"runtime-library", "master-key"} {
		t.Run(check, func(t *testing.T) {
			cfg := nonReleaseTestConfig(t)
			want := "loaded at process start"
			if check == "runtime-library" {
				cfg.RuntimeLibraryPath = filepath.Join(t.TempDir(), "other-runtime")
			} else {
				t.Setenv(config.EnvMasterKey, "not-base64!")
				want = "master key environment variable"
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			_, closeApp, err := setupServe(context.Background(), cfg, false, testBuildCommit, testSPA, logger, func(err error) {
				t.Errorf("unexpected fatal shutdown: %v", err)
			}, nil, make(chan lifecycleAction, 1))
			if closeApp != nil {
				if err := closeApp(); err != nil {
					t.Fatal(err)
				}
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("serve config error = %v, want %q before choice", err, want)
			}
			assertEmptyDirectory(t, filepath.Dir(cfg.SQLitePath))
		})
	}
}

func TestRunWithoutBuildCommit(t *testing.T) {
	_, want := fwbackend.BuildCommit()
	if want == nil || !strings.Contains(want.Error(), "just build") {
		t.Fatalf("test binary BuildCommit error = %v, want missing commit and recipe", want)
	}
	for _, command := range []string{"serve", "mcp"} {
		t.Run(command, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv(config.EnvSQLitePath, filepath.Join(dir, "officer.db"))
			t.Setenv(config.EnvMasterKeyFile, filepath.Join(dir, "master-key"))
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			var err error
			if command == "serve" {
				err = runServe([]string{"-open-browser=false"}, logger, nil)
			} else {
				err = runMCP(nil, logger)
			}
			if err == nil || err.Error() != want.Error() {
				t.Fatalf("%s error = %v, want BuildCommit error %v", command, err, want)
			}
			assertEmptyDirectory(t, dir)
		})
	}
}

func TestMCPRefusesMissingNonReleaseDatabase(t *testing.T) {
	cfg := nonReleaseTestConfig(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := setupMCP(context.Background(), cfg, false, testBuildCommit, logger, func(err error) {
		t.Errorf("unexpected fatal shutdown: %v", err)
	})
	if app != nil {
		if err := app.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err == nil {
		t.Fatal("mcp accepted a missing non-release database")
	}
	for _, want := range []string{"first screen", "pit-officer serve", fmt.Sprintf("%q", cfg.SQLitePath+".non-release-"+testBuildCommit)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("mcp error = %v, want %q", err, want)
		}
	}
	assertEmptyDirectory(t, filepath.Dir(cfg.SQLitePath))
}

func nonReleaseTestConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	encodedKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	keyPath := filepath.Join(t.TempDir(), "master-key")
	if err := os.WriteFile(keyPath, []byte(encodedKey), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvMasterKey, encodedKey)
	t.Setenv(config.EnvMasterKeyFile, keyPath)
	cfg, err := config.Load([]string{"-mode", "serve", "-sqlite-path", filepath.Join(dir, "officer.db")}, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func serveBuildStatus(t *testing.T, handler http.Handler) nonrelease.Status {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost/app/api/v1/build", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /build = %d %s, want 200", rec.Code, rec.Body)
	}
	var got nonrelease.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func assertApplicationSession(t *testing.T, handler http.Handler) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost/app/api/v1/session", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"accessMode":"setup"`) {
		t.Fatalf("application session = %d %s, want setup session", rec.Code, rec.Body)
	}
}

type databaseFileSnapshot struct {
	data    []byte
	modTime time.Time
}

func snapshotDatabaseFiles(t *testing.T, paths ...string) map[string]databaseFileSnapshot {
	t.Helper()
	files := make(map[string]databaseFileSnapshot, len(paths))
	for _, path := range paths {
		data := []byte("preserve " + filepath.Base(path))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		files[path] = databaseFileSnapshot{data: data, modTime: info.ModTime()}
	}
	return files
}

func assertDatabaseFilesUnchanged(t *testing.T, files map[string]databaseFileSnapshot) {
	t.Helper()
	for path, want := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("preserved file %q: %v", path, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, want.data) || !info.ModTime().Equal(want.modTime) {
			t.Fatalf("file %q changed: data = %q, modtime = %v; want %q, %v", path, data, info.ModTime(), want.data, want.modTime)
		}
	}
}

func assertEmptyDirectory(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory %q has %d entries, want no files created", dir, len(entries))
	}
}

func TestSetupResolvesMasterKey(t *testing.T) {
	const malformedKey = "not-base64!"
	t.Setenv(config.EnvMasterKey, malformedKey)
	t.Setenv(config.EnvMasterKeyFile, filepath.Join(t.TempDir(), "missing-master-key"))

	cfg, err := config.Load([]string{"-mode", "mcp"}, os.LookupEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := setup(context.Background(), cfg, logger, func(err error) {
		t.Errorf("unexpected fatal shutdown: %v", err)
	}); err == nil {
		t.Fatal("setup() error = nil")
	} else {
		if !strings.Contains(err.Error(), "master key environment variable") {
			t.Fatalf("setup() error = %v, want environment source", err)
		}
		if strings.Contains(err.Error(), malformedKey) {
			t.Fatalf("setup() error exposes key material: %v", err)
		}
	}
}

func TestSetupRejectsARuntimeLibraryPathTheProcessDidNotStartWith(t *testing.T) {
	const configured = "/nonexistent/openpit-runtime-other"
	sqlitePath := filepath.Join(t.TempDir(), "officer.db")
	cfg, err := config.Load([]string{
		"-mode", "serve", "-runtime-library-path", configured, "-sqlite-path", sqlitePath,
	}, os.LookupEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := setup(context.Background(), cfg, logger, func(err error) {
		t.Errorf("unexpected fatal shutdown: %v", err)
	})
	if err == nil {
		_ = app.Close()
		t.Fatal("setup accepted a runtime library path the process did not start with")
	}
	for _, want := range []string{
		fmt.Sprintf("%q", configured),
		fmt.Sprintf("%q", openpit.RuntimeLibraryPath()),
		"loaded at process start",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("setup() error = %v, want it to contain %s", err, want)
		}
	}
	if _, statErr := os.Stat(sqlitePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("database stat = %v, want setup to fail before opening the store", statErr)
	}
}

func TestSetupHandsAllowedHostsToRegister(t *testing.T) {
	cfg, err := config.Load([]string{
		"-mode", "serve", "-sqlite-path", filepath.Join(t.TempDir(), "officer.db"),
	}, os.LookupEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Load refuses this name itself; setting it after Load shows the name
	// reaches Register.
	cfg.AllowedHosts = []string{"officer:8787"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := setup(context.Background(), cfg, logger, func(err error) {
		t.Errorf("unexpected fatal shutdown: %v", err)
	})
	if err == nil {
		_ = app.Close()
		t.Fatal("setup accepted the allowed host officer:8787")
	}
	if !strings.Contains(err.Error(), `"officer:8787"`) {
		t.Fatalf("setup() error = %v, want it to name the refused host", err)
	}
}

func TestCheckRuntimeLibraryPath(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	loaded := filepath.Join(directory, "runtime.dll")
	separator := string(filepath.Separator)
	tests := []struct {
		name       string
		configured string
		loaded     string
		wantErr    bool
	}{
		{name: "not configured", configured: "", loaded: loaded},
		{name: "same path", configured: loaded, loaded: loaded},
		{name: "trailing slash", configured: loaded + separator, loaded: loaded},
		{name: "forward slashes", configured: filepath.ToSlash(loaded), loaded: loaded},
		{name: "padded", configured: " " + directory + separator + "." + separator + "runtime.dll ", loaded: loaded},
		{name: "different path", configured: filepath.Join(directory, "other.dll"), loaded: loaded, wantErr: true},
		{name: "blank", configured: " ", loaded: loaded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkRuntimeLibraryPath(tt.configured, tt.loaded)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("checkRuntimeLibraryPath(%q, %q) = %v, want nil",
						tt.configured, tt.loaded, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkRuntimeLibraryPath(%q, %q) = nil, want a mismatch",
					tt.configured, tt.loaded)
			}
			for _, want := range []string{
				fmt.Sprintf("%q", strings.TrimSpace(tt.configured)),
				fmt.Sprintf("%q", tt.loaded),
				"loaded at process start",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want it to contain %s", err, want)
				}
			}
		})
	}
}

func TestUtilityCommandsDoNotResolveMasterKey(t *testing.T) {
	t.Setenv(config.EnvMasterKey, "not-base64!")
	t.Setenv(config.EnvMasterKeyFile, filepath.Join(t.TempDir(), "missing-master-key"))

	t.Run("healthcheck", func(t *testing.T) {
		t.Setenv(config.EnvSQLitePath, filepath.Join(t.TempDir(), "officer.db"))
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/healthz" {
				t.Errorf("request path = %q, want /healthz", r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		addr := strings.TrimPrefix(server.URL, "http://")
		if err := runHealthcheck([]string{"-http-addr", addr}); err != nil {
			t.Fatalf("runHealthcheck: %v", err)
		}
	})

	t.Run("dashboard", func(t *testing.T) {
		sqlitePath := filepath.Join(t.TempDir(), "officer.db")
		t.Setenv(config.EnvSQLitePath, sqlitePath)
		cfg := config.Config{SQLitePath: sqlitePath}
		if err := officerruntime.Write(cfg, officerruntime.State{
			Addr: "127.0.0.1:8787",
			URL:  "http://127.0.0.1:8787/",
		}); err != nil {
			t.Fatalf("Write runtime state: %v", err)
		}

		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		if err := runDashboard([]string{"-no-open"}, logger); err != nil {
			t.Fatalf("runDashboard: %v", err)
		}
	})
}

func TestServiceLifecycleHandlerAuditsBeforeAccepting(t *testing.T) {
	t.Parallel()
	requests := make(chan lifecycleAction, 1)
	var recordedAction lifecycleAction
	var recordedCaller domain.Caller
	controller := &serviceLifecycleController{
		requests: requests,
		record: func(ctx context.Context, action lifecycleAction) error {
			caller, err := auth.CallerFromContext(ctx)
			if err != nil {
				return err
			}
			recordedAction = action
			recordedCaller = caller
			return nil
		},
	}
	handler := controller.handler(lifecycleRestart)

	req := httptest.NewRequest(http.MethodPost, "/app/api/v1/service/restart", nil)
	req = req.WithContext(auth.ContextWithCaller(req.Context(), domain.Caller{
		Source:    domain.SourcePanel,
		Principal: domain.PrincipalOperator,
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if recordedAction != lifecycleRestart ||
		recordedCaller.Source != domain.SourcePanel ||
		recordedCaller.Principal != domain.PrincipalOperator {
		t.Fatalf("recorded = (%q, %+v), want restart from panel operator",
			recordedAction, recordedCaller)
	}
	select {
	case got := <-requests:
		if got != lifecycleRestart {
			t.Fatalf("request = %q, want %q", got, lifecycleRestart)
		}
	default:
		t.Fatalf("lifecycle request was not queued")
	}
}

func TestServiceLifecycleHandlerRejectsWhenAuditFails(t *testing.T) {
	t.Parallel()
	requests := make(chan lifecycleAction, 1)
	controller := &serviceLifecycleController{
		requests: requests,
		record: func(context.Context, lifecycleAction) error {
			return errors.New("audit failed")
		},
	}
	handler := controller.handler(lifecycleRestart)

	req := httptest.NewRequest(http.MethodPost, "/app/api/v1/service/restart", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	select {
	case got := <-requests:
		t.Fatalf("queued lifecycle request despite audit failure: %q", got)
	default:
	}
}

func TestLifecycleAuditMapsKnownActionsAndRejectsUnknown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		action     lifecycleAction
		wantAction domain.AuditAction
		wantDetail string
	}{
		{lifecycleRestart, domain.AuditActionRestartService, "Service restart requested"},
		{lifecycleStop, domain.AuditActionStopService, "Service stop requested"},
	}
	for _, tt := range tests {
		gotAction, gotDetail, err := lifecycleAudit(tt.action)
		if err != nil {
			t.Fatalf("lifecycleAudit(%q) error = %v", tt.action, err)
		}
		if gotAction != tt.wantAction || gotDetail.Text != tt.wantDetail {
			t.Fatalf("lifecycleAudit(%q) = (%q, %q), want (%q, %q)",
				tt.action, gotAction, gotDetail.Text, tt.wantAction, tt.wantDetail)
		}
	}

	gotAction, gotDetail, err := lifecycleAudit(lifecycleAction("pause"))
	if err == nil {
		t.Fatalf("lifecycleAudit(%q) = (%q, %q), want an error", "pause", gotAction, gotDetail)
	}
	if gotAction != "" {
		t.Fatalf("lifecycleAudit(%q) audit action = %q, want none", "pause", gotAction)
	}
	if !strings.Contains(err.Error(), `"pause"`) {
		t.Fatalf("lifecycleAudit error = %v, want it to name the action", err)
	}
}

func TestRestartProcessArgsPinsHTTPAddr(t *testing.T) {
	t.Parallel()
	const addr = "127.0.0.1:54321"
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "append",
			in:   []string{"pit-officer", "serve", "-sqlite-path", "db.sqlite"},
			want: []string{
				"pit-officer", "serve", "-sqlite-path", "db.sqlite",
				"-http-addr", addr, "-open-browser=false",
			},
		},
		{
			name: "replace separate short flag",
			in: []string{
				"pit-officer", "serve", "-http-addr", "127.0.0.1:0",
			},
			want: []string{
				"pit-officer", "serve", "-http-addr", addr,
				"-open-browser=false",
			},
		},
		{
			name: "replace separate long flag",
			in: []string{
				"pit-officer", "serve", "--http-addr", "127.0.0.1:0",
			},
			want: []string{
				"pit-officer", "serve", "--http-addr", addr,
				"-open-browser=false",
			},
		},
		{
			name: "replace joined short flag",
			in:   []string{"pit-officer", "serve", "-http-addr=127.0.0.1:0"},
			want: []string{
				"pit-officer", "serve", "-http-addr=" + addr,
				"-open-browser=false",
			},
		},
		{
			name: "replace joined long flag",
			in:   []string{"pit-officer", "serve", "--http-addr=127.0.0.1:0"},
			want: []string{
				"pit-officer", "serve", "--http-addr=" + addr,
				"-open-browser=false",
			},
		},
		{
			name: "complete missing value",
			in:   []string{"pit-officer", "serve", "-http-addr"},
			want: []string{
				"pit-officer", "serve", "-http-addr", addr,
				"-open-browser=false",
			},
		},
		{
			name: "override open browser",
			in: []string{
				"pit-officer", "serve", "-open-browser=true",
				"-http-addr", "127.0.0.1:0",
			},
			want: []string{
				"pit-officer", "serve", "-open-browser=true",
				"-http-addr", addr, "-open-browser=false",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			original := append([]string(nil), tt.in...)
			got := restartProcessArgs(tt.in, addr)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("restartProcessArgs() = %#v, want %#v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.in, original) {
				t.Fatalf("restartProcessArgs mutated input: %#v", tt.in)
			}
		})
	}
}
