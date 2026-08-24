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

// Package runtime records the live address of a running serve instance so other
// commands (dashboard, healthcheck) can discover it without knowing the port.
// The default serve bind is a loopback OS-assigned free port, so the address is
// not known until the listener is open; serve writes it here and peers read it.
//
// The state record carries no secrets: only the bound address, a client URL,
// the process id, and the start time.
package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"go.openpit.dev/officer/internal/config"
)

// stateFile is the runtime-state filename placed alongside the SQLite database.
const stateFile = "officer-runtime.json"

// State is the runtime record a serve instance publishes. Addr is the actual
// bound listen address (host:port, with the resolved port); URL is the
// client-reachable URL derived from it; PID is the serve process id; StartedAt
// is an RFC 3339 UTC timestamp.
type State struct {
	Addr      string `json:"addr"`
	URL       string `json:"url"`
	PID       int    `json:"pid"`
	StartedAt string `json:"startedAt"`
}

// StatePath returns the runtime-state file path derived from cfg. It is stable
// across serve, dashboard, and healthcheck: all derive it from the SQLite path
// without knowing the resolved port. It sits next to the database.
func StatePath(cfg config.Config) string {
	return filepath.Join(filepath.Dir(cfg.SQLitePath), stateFile)
}

// Write persists state atomically: it writes a temp file in the target
// directory and renames it into place, so a reader never observes a partial
// file. The rename is atomic only within one filesystem, hence the same dir.
func Write(cfg config.Config, state State) error {
	path := StatePath(cfg)
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("runtime: marshal state: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), stateFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("runtime: create temp state: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		cause := fmt.Errorf("runtime: write temp state: %w", err)
		if closeErr := tmp.Close(); closeErr != nil {
			cause = errors.Join(
				cause,
				fmt.Errorf("runtime: close temp state after write failure: %w", closeErr),
			)
		}
		return removeTempState(cause, tmpName)
	}
	if err := tmp.Close(); err != nil {
		return removeTempState(
			fmt.Errorf("runtime: close temp state: %w", err),
			tmpName,
		)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return removeTempState(
			fmt.Errorf("runtime: rename state: %w", err),
			tmpName,
		)
	}
	return nil
}

// removeTempState preserves both the operation failure and an unsuccessful cleanup.
func removeTempState(cause error, path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(
			cause,
			fmt.Errorf("runtime: remove temp state: %w", err),
		)
	}
	return cause
}

// Read loads the persisted state. The bool reports presence: it is false with a
// nil error when no serve instance has published state (the file is absent),
// which callers treat as "not running" rather than a failure.
func Read(cfg config.Config) (State, bool, error) {
	path := StatePath(cfg)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, false, nil
		}
		return State{}, false, fmt.Errorf("runtime: read state: %w", err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, false, fmt.Errorf("runtime: parse state: %w", err)
	}
	return state, true, nil
}

// Remove deletes the persisted state. It is idempotent: a missing file is not
// an error, so the serve shutdown path can call it unconditionally.
func Remove(cfg config.Config) error {
	if err := os.Remove(StatePath(cfg)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("runtime: remove state: %w", err)
	}
	return nil
}

// ClientURL turns a bind address into a client-reachable URL of the form
// "http://host:port/". Wildcard bind hosts (0.0.0.0, empty, "::") are not
// reachable as a destination, so they map to loopback 127.0.0.1.
// net.SplitHostPort strips the brackets from bracketed IPv6 addresses, so
// "[::]:port" yields host "::" and is covered by the "::" case.
func ClientURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Not host:port (no port); use as-is so the caller still gets a URL.
		return "http://" + addr + "/"
	}
	switch host {
	case "0.0.0.0", "", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}
