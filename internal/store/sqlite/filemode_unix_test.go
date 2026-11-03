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

//go:build unix

// Database file permission tests: the database, its journal and the database
// Reset leaves must stay private to the process owner under any umask, and an
// existing database or sidecar with another mode is narrowed on open. Some
// change the process umask, working directory or default logger, so they must
// never run in parallel.

package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestDatabaseFilesArePrivateUnderAnyUmask(t *testing.T) {
	for _, umask := range []int{0o022, 0o000} {
		t.Run(fmt.Sprintf("umask %03o", umask), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			path := filepath.Join(dir, "officer.db")
			previous := syscall.Umask(umask)
			t.Cleanup(func() { syscall.Umask(previous) })

			store := openSQLiteStoreForTest(t, path)
			assertFileMode(t, path, 0o600)
			if err := store.Migrate(ctx); err != nil {
				t.Fatalf("Migrate: %v", err)
			}
			assertFileMode(t, path, 0o600)
			assertJournalMode(t, ctx, store, path)

			if err := store.Reset(ctx); err != nil {
				t.Fatalf("Reset: %v", err)
			}
			assertFileMode(t, path, 0o600)
			assertJournalMode(t, ctx, store, path)
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			for _, entry := range entries {
				assertFileMode(t, filepath.Join(dir, entry.Name()), 0o600)
			}
		})
	}
}

// TestEmptyTemporaryFileOpensPrivateUnderAnyUmask checks the premise Reset
// relies on for its temporary database: an empty os.CreateTemp file opened
// through openSQLiteDB is an empty database that keeps its mode, and its
// journal takes that mode.
func TestEmptyTemporaryFileOpensPrivateUnderAnyUmask(t *testing.T) {
	for _, umask := range []int{0o022, 0o000} {
		t.Run(fmt.Sprintf("umask %03o", umask), func(t *testing.T) {
			ctx := context.Background()
			previous := syscall.Umask(umask)
			t.Cleanup(func() { syscall.Umask(previous) })
			tmp, err := os.CreateTemp(t.TempDir(), "officer.db.reset-*.db")
			if err != nil {
				t.Fatalf("CreateTemp: %v", err)
			}
			if err := tmp.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			db, err := openSQLiteDB(tmp.Name())
			if err != nil {
				t.Fatalf("openSQLiteDB: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if _, err := db.ExecContext(ctx, "CREATE TABLE seed (x INTEGER)"); err != nil {
				t.Fatalf("seed: %v", err)
			}
			assertFileMode(t, tmp.Name(), 0o600)
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err := tx.ExecContext(ctx, "INSERT INTO seed VALUES (1)"); err != nil {
				t.Fatalf("write in transaction: %v", err)
			}
			assertFileMode(t, tmp.Name()+"-journal", 0o600)
		})
	}
}

func TestNewProtectsTheFileBehindAQueryPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "officer.db")
	previous := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(previous) })

	store := openSQLiteStoreForTest(t, path+"?_pragma=busy_timeout(5000)")
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	assertFileMode(t, path, 0o600)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "?") {
			t.Fatalf("query path left a stray file %q", entry.Name())
		}
	}
}

func TestNewRejectsPathsThatNameNoFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, path := range []string{"", ":memory:", "file:officer.db", "?mode=memory"} {
		if _, err := New(path, domain.DefaultRealm); err == nil ||
			!strings.Contains(err.Error(), "is not a file path") {
			t.Fatalf("New(%q) error = %v, want a not-a-file-path error", path, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected paths left %d files, want none", len(entries))
	}
}

func TestEnsurePrivateDatabaseFileNarrowsExistingSidecars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "officer.db")
	for _, file := range DatabaseFiles(path) {
		if err := os.WriteFile(file, []byte("pages"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q): %v", file, err)
		}
		if err := os.Chmod(file, 0o644); err != nil {
			t.Fatalf("Chmod(%q): %v", file, err)
		}
	}

	logs := captureSlogForTest(t)
	if err := ensurePrivateDatabaseFile(path); err != nil {
		t.Fatalf("ensurePrivateDatabaseFile: %v", err)
	}
	output := logs.String()
	for _, file := range DatabaseFiles(path) {
		assertFileMode(t, file, 0o600)
		if !strings.Contains(output, "path="+file+" old_mode=0644 new_mode=0600") {
			t.Fatalf("log %q does not record narrowing %q", output, file)
		}
	}
}

func TestNewRejectsDanglingDatabaseSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "officer.db")
	target := filepath.Join(dir, "volume.db")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := New(path, domain.DefaultRealm); err == nil ||
		!strings.Contains(err.Error(), "resolve database path") {
		t.Fatalf("New(dangling symlink) error = %v, want a resolve error", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("dangling symlink target exists after New: %v", err)
	}
}

func TestNewRejectsSymlinkedSidecarWithoutTouchingItsTarget(t *testing.T) {
	for _, sidecar := range []string{"-wal", "-shm", "-journal"} {
		t.Run(sidecar, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "officer.db")
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("not a database"), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if err := os.Chmod(outside, 0o644); err != nil {
				t.Fatalf("Chmod: %v", err)
			}
			if err := os.Symlink(outside, path+sidecar); err != nil {
				t.Fatalf("Symlink: %v", err)
			}
			_, err := New(path, domain.DefaultRealm)
			if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
				t.Fatalf("New(symlinked %s) error = %v, want a not-a-regular-file error", sidecar, err)
			}
			assertFileMode(t, outside, 0o644)
		})
	}
}

func TestNewNarrowsTheTargetOfADatabaseSymlink(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	target := filepath.Join(dir, "volume.db")
	path := filepath.Join(dir, "officer.db")
	store := openSQLiteStoreForTest(t, target)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	store = openSQLiteStoreForTest(t, path)
	assertFileMode(t, target, 0o600)
	// SQLite keeps the journal beside the link target.
	assertJournalMode(t, ctx, store, target)
}

func TestNewNarrowsWiderDatabaseFileMode(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	store := openSQLiteStoreForTest(t, path)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	logs := captureSlogForTest(t)
	openSQLiteStoreForTest(t, path)
	assertFileMode(t, path, 0o600)
	output := logs.String()
	for _, want := range []string{
		"SQLite database file mode set to owner-only",
		"path=" + path,
		"old_mode=0644",
		"new_mode=0600",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("log %q does not contain %q", output, want)
		}
	}

	logs.Reset()
	openSQLiteStoreForTest(t, path)
	if output := logs.String(); output != "" {
		t.Fatalf("opening a private database logged %q, want nothing", output)
	}
}

func TestNewRejectsDatabasePathThatIsNotARegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "officer.db")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	_, err := New(path, domain.DefaultRealm)
	if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("New(directory) error = %v, want a not-a-regular-file error", err)
	}
	assertFileMode(t, path, 0o755)
}

// assertJournalMode holds a write transaction open so SQLite keeps its
// rollback journal on disk, and checks the journal's mode.
func assertJournalMode(t *testing.T, ctx context.Context, store *sqliteStore, path string) {
	t.Helper()
	tx, err := store.currentDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "CREATE TABLE journal_probe (x INTEGER)"); err != nil {
		t.Fatalf("write in transaction: %v", err)
	}
	assertFileMode(t, path+"-journal", 0o600)
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q): %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%q mode %04o, want %04o", path, got, want)
	}
}
