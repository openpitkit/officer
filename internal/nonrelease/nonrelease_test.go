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

package nonrelease

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.openpit.dev/officer/internal/store/sqlite"
)

func TestDatabasePath(t *testing.T) {
	got, err := DatabasePath("data/officer.db", "commit-from-host")
	if err != nil || got != "data/officer.db.non-release-commit-from-host" {
		t.Fatalf("DatabasePath = %q, %v", got, err)
	}
	for _, path := range []string{"", ":memory:", "file:officer.db", "officer.db?mode=ro"} {
		t.Run("reject "+path, func(t *testing.T) {
			if _, err := DatabasePath(path, "commit"); err == nil || !strings.Contains(err.Error(), strconv.Quote(path)) {
				t.Fatalf("invalid path %q error = %v", path, err)
			}
		})
	}
	if _, err := DatabasePath("officer.db", ""); err == nil || !strings.Contains(err.Error(), `""`) {
		t.Fatalf("empty commit error = %v", err)
	}
}

func TestReleaseDatabaseExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "release.db")
	if got, err := ReleaseDatabaseExists(path); got || err != nil {
		t.Fatalf("absent file = %v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReleaseDatabaseExists(path); !got || err != nil {
		t.Fatalf("regular file = %v, %v", got, err)
	}
	if got, err := ReleaseDatabaseExists(dir); got || err == nil {
		t.Fatalf("directory = %v, %v", got, err)
	}
	invalidPath := filepath.Join(dir, "invalid\x00.db")
	_, err := ReleaseDatabaseExists(invalidPath)
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "stat" || pathErr.Path != invalidPath {
		t.Fatalf("ReleaseDatabaseExists: got %v, want a stat *os.PathError for %q", err, invalidPath)
	}
}

func TestRemoveStalePreservesOwnDatabaseFiles(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release[1].db")
	own, err := DatabasePath(release, "own")
	if err != nil {
		t.Fatal(err)
	}
	keep := append(sqlite.DatabaseFiles(release), sqlite.DatabaseFiles(own)...)
	keep = append(keep, filepath.Join(filepath.Dir(release), "other.db.non-release-x"))
	remove := []string{release + ".non-release-old", release + ".non-release-old-wal", own + ".prepare-crash"}
	for _, path := range append(slices.Clone(keep), remove...) {
		if err := os.WriteFile(path, []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := RemoveStale(release, "own")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(remove)
	if !slices.Equal(got, remove) {
		t.Errorf("removed paths = %q, want %q", got, remove)
	}
	for _, path := range keep {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != path {
			t.Errorf("preserved file %q changed or missing: %q, %v", path, data, err)
		}
	}
	for _, path := range remove {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stale file %q remains: %v", path, err)
		}
	}
}

func TestRemoveStaleReturnsDirectoryAndRemovalErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := RemoveStale(filepath.Join(dir, "absent", "release.db"), "own"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v, want os.ErrNotExist", err)
	}
	release := filepath.Join(dir, "release.db")
	blocked := release + ".non-release-a"
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "child"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	later := release + ".non-release-z"
	if err := os.WriteFile(later, nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := RemoveStale(release, "own")
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "remove" || pathErr.Path != blocked || !strings.Contains(err.Error(), blocked) {
		t.Fatalf("removal failure error = %v, want a remove *os.PathError for %q", err, blocked)
	}
	if _, err := os.Stat(later); err != nil {
		t.Fatalf("cleanup continued past removal failure: %v", err)
	}
}
