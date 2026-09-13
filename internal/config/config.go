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

// Package config defines Pit Officer's runtime configuration and how it is
// loaded from command-line flags and the environment.
package config

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"go.openpit.dev/officer/framework/secret"
)

// RunMode selects which long-running surface the process exposes.
type RunMode string

// Recognized run modes.
const (
	// RunModeMCP runs a local stdio MCP server. No network listener is opened.
	RunModeMCP RunMode = "mcp"
	// RunModeServe runs the always-on service: the MCP surface over streamable
	// HTTP plus the operator dashboard.
	RunModeServe RunMode = "serve"
)

// MCPTransport selects the transport the MCP server is exposed over.
type MCPTransport string

// Recognized MCP transports.
const (
	// MCPTransportStdio serves MCP over standard input/output. It is the
	// transport used by RunModeMCP.
	MCPTransportStdio MCPTransport = "stdio"
	// MCPTransportHTTP serves MCP over streamable HTTP. It is the transport
	// used by RunModeServe.
	MCPTransportHTTP MCPTransport = "http"
)

// DefaultHTTPAddr is the default HTTP listen address: loopback, OS-assigned
// free port; use 'pit-officer dashboard' to discover the URL. The operator may
// override this address to pin a port or widen the bind.
const DefaultHTTPAddr = "127.0.0.1:0"

// DefaultSQLitePath is the default on-disk SQLite database path used when none
// is supplied.
const DefaultSQLitePath = "pit-officer.db"

// Environment variable names honored by Load. Flags take precedence over the
// environment; the environment takes precedence over the defaults. These names
// are part of the deployment contract: the container image and docker-compose
// set them by these exact names.
const (
	// EnvHTTPAddr overrides Config.HTTPAddr. The container sets it to
	// 0.0.0.0:8787 to bind all interfaces inside the container network.
	EnvHTTPAddr = "PIT_OFFICER_HTTP_ADDR"
	// EnvSQLitePath overrides Config.SQLitePath. The container points it at the
	// persistent data volume.
	EnvSQLitePath = "PIT_OFFICER_SQLITE_PATH"
	// EnvRuntimeLibraryPath seeds Config.RuntimeLibraryPath. The OpenPit SDK
	// reads it itself at process start to load the native runtime from a
	// pre-extracted path.
	EnvRuntimeLibraryPath = "OPENPIT_RUNTIME_LIBRARY_PATH"
	// EnvOpenBrowser overrides Config.OpenBrowser. Set it to a falsey value
	// ("false", "0", "no", "off") to keep serve from opening the dashboard.
	EnvOpenBrowser = "PIT_OFFICER_OPEN_BROWSER"
	// EnvMasterKey supplies the operator master key as standard base64. It is
	// intentionally not mirrored by a command-line flag because process
	// arguments are visible to other local processes.
	EnvMasterKey = "PIT_OFFICER_MASTER_KEY"
	// EnvMasterKeyFile names a file holding the operator master key as standard
	// base64.
	EnvMasterKeyFile = "PIT_OFFICER_MASTER_KEY_FILE"
)

// Config is the fully resolved Pit Officer configuration for one process. A
// zero Config is not valid; obtain one from Load.
type Config struct {
	// Mode selects the run surface (mcp or serve).
	Mode RunMode
	// MCPTransport selects how the MCP server is exposed. It is derived from
	// Mode unless overridden: stdio for mcp, http for serve.
	MCPTransport MCPTransport
	// HTTPAddr is the HTTP listen address used in serve mode. It defaults to
	// DefaultHTTPAddr (loopback) and is ignored in mcp mode.
	HTTPAddr string
	// SQLitePath is the on-disk path of the SQLite database. It defaults to
	// DefaultSQLitePath.
	SQLitePath string
	// RuntimeLibraryPath is the native OpenPit runtime library the operator
	// expects. The SDK loads its runtime at process start from
	// OPENPIT_RUNTIME_LIBRARY_PATH, or its embedded runtime when that is unset,
	// so a non-empty value must name that same library; the binary refuses to
	// start otherwise.
	RuntimeLibraryPath string
	// OpenBrowser, when true, has serve open the dashboard URL in the default
	// browser on start. It defaults to true and is ignored in mcp mode.
	OpenBrowser bool
	// masterKeyFile is the resolved path from the flag or environment.
	masterKeyFile string
	// masterKeyFileConfigured distinguishes an absent file source from a
	// configured source whose path resolved to an invalid value.
	masterKeyFileConfigured bool
}

// Load resolves the configuration for one invocation from the given
// command-line arguments (excluding the program name and any subcommand) and an
// environment lookup function. Passing os.LookupEnv wires it to the process
// environment; tests pass a stub.
//
// Precedence is flags > environment > defaults. The recognized environment
// variables are EnvHTTPAddr, EnvSQLitePath, EnvRuntimeLibraryPath,
// EnvOpenBrowser, and EnvMasterKeyFile; the recognized flags are -mode,
// -http-addr, -sqlite-path, -runtime-library-path, -open-browser, and
// -master-key-file. The master key itself is intentionally not accepted as a
// flag because process arguments are visible to other local processes. The MCP
// transport is derived from the run mode (stdio for mcp, http for serve) and is
// not separately configurable.
//
// Load applies defaults (loopback HTTP bind, default SQLite path), records
// whether the master key file source is configured, and resolves its path
// without reading or parsing it. It returns an error for unknown flags, a
// missing or unknown run mode, a flag parse failure, or a configured master key
// file whose resolved path is empty. Commands that build the application call
// ResolveMasterKey to read and parse the file and environment sources.
func Load(args []string, lookupEnv func(string) (string, bool)) (Config, error) {
	if lookupEnv == nil {
		lookupEnv = func(string) (string, bool) { return "", false }
	}

	// Seed flag defaults from the environment so an unset flag falls back to
	// the environment, and an absent environment value falls back to the
	// literal default. A flag on the command line overrides both.
	httpAddrDefault := envOr(lookupEnv, EnvHTTPAddr, DefaultHTTPAddr)
	sqlitePathDefault := envOr(lookupEnv, EnvSQLitePath, DefaultSQLitePath)
	runtimeLibraryDefault := envOr(lookupEnv, EnvRuntimeLibraryPath, "")
	masterKeyFileDefault, masterKeyFileEnvironmentConfigured := lookupEnv(EnvMasterKeyFile)
	// Default on unless the env explicitly sets a falsey value; the flag
	// (bare or =value) overrides whatever this seed resolves to.
	openBrowserDefault := envBool(lookupEnv, EnvOpenBrowser, true)

	fs := flag.NewFlagSet("officer", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	mode := fs.String("mode", "", "run mode: mcp or serve")
	httpAddr := fs.String("http-addr", httpAddrDefault,
		"HTTP listen address (serve mode)")
	sqlitePath := fs.String("sqlite-path", sqlitePathDefault,
		"on-disk SQLite database path")
	runtimeLibraryPath := fs.String("runtime-library-path", runtimeLibraryDefault,
		"explicit native OpenPit runtime library path")
	masterKeyFile := fs.String("master-key-file", masterKeyFileDefault,
		"file containing the operator master key")
	openBrowser := fs.Bool("open-browser", openBrowserDefault,
		"open the dashboard in the browser on serve start")

	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("config: parse flags: %w", err)
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("config: unexpected argument %q", fs.Arg(0))
	}
	masterKeyFileFlagConfigured := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "master-key-file" {
			masterKeyFileFlagConfigured = true
		}
	})

	parsedMode, err := parseMode(*mode)
	if err != nil {
		return Config{}, err
	}
	masterKeyFileConfigured := masterKeyFileFlagConfigured || masterKeyFileEnvironmentConfigured
	if masterKeyFileConfigured && *masterKeyFile == "" {
		return Config{}, fmt.Errorf("config: master key file is configured but path is empty")
	}

	cfg := Config{
		Mode:                    parsedMode,
		MCPTransport:            transportForMode(parsedMode),
		HTTPAddr:                *httpAddr,
		SQLitePath:              *sqlitePath,
		RuntimeLibraryPath:      *runtimeLibraryPath,
		OpenBrowser:             *openBrowser,
		masterKeyFile:           *masterKeyFile,
		masterKeyFileConfigured: masterKeyFileConfigured,
	}
	return cfg, nil
}

// ResolveMasterKey reads EnvMasterKey directly and parses it alongside the
// optional file source recorded by Load. When both are configured, they must
// contain the same key.
func ResolveMasterKey(
	cfg Config, lookupEnv func(string) (string, bool),
) (*secret.MasterKey, error) {
	if lookupEnv == nil {
		lookupEnv = func(string) (string, bool) { return "", false }
	}

	var environmentKey *secret.MasterKey
	if encoded, ok := lookupEnv(EnvMasterKey); ok {
		key, err := secret.ParseMasterKey(encoded)
		if err != nil {
			return nil, fmt.Errorf("config: parse master key environment variable: %w", err)
		}
		environmentKey = &key
	}

	var fileKey *secret.MasterKey
	if cfg.masterKeyFileConfigured {
		if cfg.masterKeyFile == "" {
			return nil, fmt.Errorf("config: master key file is configured but path is empty")
		}
		contents, err := os.ReadFile(cfg.masterKeyFile)
		if err != nil {
			return nil, fmt.Errorf("config: read master key file: %w", err)
		}
		key, err := secret.ParseMasterKeyFile(contents)
		if err != nil {
			return nil, fmt.Errorf("config: parse master key file: %w", err)
		}
		fileKey = &key
	}

	if environmentKey == nil {
		return fileKey, nil
	}
	if fileKey == nil {
		return environmentKey, nil
	}
	same, err := environmentKey.Equal(*fileKey)
	if err != nil {
		return nil, fmt.Errorf("config: compare master key sources: %w", err)
	}
	if !same {
		return nil, fmt.Errorf("config: master key environment variable and master key file differ")
	}
	return environmentKey, nil
}

// envOr returns the environment value for key when present and non-empty,
// otherwise fallback. An empty environment value is treated as unset so it does
// not shadow a meaningful default.
func envOr(lookupEnv func(string) (string, bool), key, fallback string) string {
	if v, ok := lookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// envBool returns fallback unless key is present and set to a recognized
// falsey value ("false", "0", "no", "off", case-insensitive), in which case it
// returns false. Any other present value (including empty) yields fallback.
func envBool(lookupEnv func(string) (string, bool), key string, fallback bool) bool {
	v, ok := lookupEnv(key)
	if !ok {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "0", "no", "off":
		return false
	default:
		return fallback
	}
}

// parseMode validates the run-mode string and maps it to a RunMode.
func parseMode(mode string) (RunMode, error) {
	switch RunMode(mode) {
	case RunModeMCP:
		return RunModeMCP, nil
	case RunModeServe:
		return RunModeServe, nil
	case "":
		return "", fmt.Errorf("config: missing run mode (want %q or %q)",
			RunModeMCP, RunModeServe)
	default:
		return "", fmt.Errorf("config: unknown run mode %q (want %q or %q)",
			mode, RunModeMCP, RunModeServe)
	}
}

// transportForMode returns the MCP transport implied by a run mode: stdio for
// mcp, http for serve.
func transportForMode(mode RunMode) MCPTransport {
	if mode == RunModeServe {
		return MCPTransportHTTP
	}
	return MCPTransportStdio
}
