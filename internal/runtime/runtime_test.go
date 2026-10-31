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

package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	fwbackend "go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/internal/config"
)

func TestStateFile(t *testing.T) {
	for _, tc := range []struct {
		release bool
		want    string
	}{
		{release: true, want: "officer-runtime.json"},
		{release: false, want: "officer-runtime.non-release.json"},
	} {
		if got := stateFile(tc.release); got != tc.want {
			t.Errorf("stateFile(%t) = %q, want %q", tc.release, got, tc.want)
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{SQLitePath: filepath.Join(dir, "officer.db")}
	wantName := "officer-runtime.non-release.json"
	otherName := "officer-runtime.json"
	if fwbackend.ReleaseBuild() {
		wantName, otherName = otherName, wantName
	}
	if path := StatePath(cfg); path != filepath.Join(dir, wantName) {
		t.Fatalf("StatePath = %q, want %q", path, filepath.Join(dir, wantName))
	}
	otherPath := filepath.Join(dir, otherName)
	if err := os.WriteFile(otherPath, []byte("other build state"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := State{Addr: "127.0.0.1:8787", URL: "http://127.0.0.1:8787/", PID: 42, StartedAt: "2026-10-27T12:00:00Z"}
	if err := Write(cfg, want); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(StatePath(cfg)); err != nil {
		t.Fatalf("state file stat: %v", err)
	}
	got, exists, err := Read(cfg)
	if err != nil || !exists || got != want {
		t.Fatalf("Read = (%+v, %t, %v), want (%+v, true, nil)", got, exists, err, want)
	}
	if err := Remove(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(StatePath(cfg)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed state stat = %v, want not exist", err)
	}
	data, err := os.ReadFile(otherPath)
	if err != nil || string(data) != "other build state" {
		t.Fatalf("other build state = %q, %v, want unchanged", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("runtime files after Remove = %d, want only other build state", len(entries))
	}
}
