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

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/internal/config"
	officerruntime "go.openpit.dev/officer/internal/runtime"
)

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
	processPath := os.Getenv(config.EnvRuntimeLibraryPath)
	for _, want := range []string{
		fmt.Sprintf("%q", configured),
		fmt.Sprintf("%s=%q", config.EnvRuntimeLibraryPath, processPath),
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

func TestCheckRuntimeLibraryPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		configured  string
		processPath string
		wantErr     bool
	}{
		{name: "not configured", configured: "", processPath: "/a/b"},
		{name: "neither set", configured: "", processPath: ""},
		{name: "same path", configured: "/a/b", processPath: "/a/b"},
		{name: "trailing slash", configured: "/a/b/", processPath: "/a/b"},
		{name: "unclean process path", configured: "/a/b", processPath: " /a/./b/ "},
		{name: "different path", configured: "/a/c", processPath: "/a/b", wantErr: true},
		{name: "variable unset", configured: "/a/b", processPath: "", wantErr: true},
		{name: "blank against set", configured: " ", processPath: "/a/b", wantErr: true},
		{name: "blank against unset", configured: " ", processPath: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkRuntimeLibraryPath(tt.configured, tt.processPath)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("checkRuntimeLibraryPath(%q, %q) = %v, want nil",
						tt.configured, tt.processPath, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkRuntimeLibraryPath(%q, %q) = nil, want a mismatch",
					tt.configured, tt.processPath)
			}
			for _, want := range []string{
				fmt.Sprintf("%q", tt.configured),
				fmt.Sprintf("%s=%q", config.EnvRuntimeLibraryPath, tt.processPath),
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
			recordedAction = action
			recordedCaller = auth.CallerFromContext(ctx)
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
