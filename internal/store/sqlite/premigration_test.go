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

package sqlite_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/store/sqlite"
)

const copyStampLayout = "20060102T150405Z"

func preMigrationStore(t *testing.T, path string, opts ...sqlite.Option) fwstore.Store {
	t.Helper()
	st, err := sqlite.New(path, domain.DefaultRealm, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return st
}

func preMigrationFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "officer.db")
	st := preMigrationStore(t, path)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("build pending fixture: initial Migrate: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := db.Exec(`
		DROP TABLE access_state;
		DROP TABLE access_credential;
		DROP TABLE panel_session;
		DROP TABLE seal_report;
		UPDATE app_schema_migration SET version = 0;
	`); err != nil {
		t.Fatalf("build pending fixture with raw SQL: %v", err)
	}
	return path
}

func assertPreMigrationState(t *testing.T, path string, wantVersion, wantTables int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("SQLite integrity = %q, error = %v", integrity, err)
	}
	var version, tables int
	if err := db.QueryRow(`SELECT MAX(version) FROM app_schema_migration`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != wantVersion {
		t.Errorf("application version = %d, want %d", version, wantVersion)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table'
		AND name IN ('access_state', 'access_credential', 'panel_session', 'seal_report')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != wantTables {
		t.Errorf("application table count = %d, want %d", tables, wantTables)
	}
}

func preMigrationCopies(t *testing.T, path string) []sqlite.PreMigrationCopy {
	t.Helper()
	copies, err := sqlite.ListPreMigrationCopies(path)
	if err != nil {
		t.Fatal(err)
	}
	return copies
}

func assertNoPreMigrationTemps(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".preparing-migration-") {
			t.Errorf("temporary database left: %s", entry.Name())
		}
	}
}

func TestPreMigrationPendingCopy(t *testing.T) {
	path := preMigrationFixture(t)
	assertPreMigrationState(t, path, 0, 0)
	st := preMigrationStore(t, path, sqlite.WithPreMigrationCopy())
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	copies := preMigrationCopies(t, path)
	if len(copies) != 1 {
		t.Fatalf("published copies = %d, want 1 before migration", len(copies))
	}
	copyPath := filepath.Join(filepath.Dir(path), copies[0].Name)
	info, err := os.Stat(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Errorf("copy permissions = %o, want 600", info.Mode().Perm())
	}
	if copies[0].Size != info.Size() || info.Size() == 0 {
		t.Errorf("listed size = %d, file size = %d", copies[0].Size, info.Size())
	}
	assertPreMigrationState(t, copyPath, 0, 0)
	assertPreMigrationState(t, path, 1, 4)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := preMigrationCopies(t, path); !slices.Equal(got, copies) {
		t.Errorf("second migration changed copies: %v, want %v", got, copies)
	}
	assertNoPreMigrationTemps(t, path)
}

func TestPreMigrationNoPendingCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "officer.db")
	st := preMigrationStore(t, path, sqlite.WithPreMigrationCopy())
	for range 2 {
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		if copies := preMigrationCopies(t, path); len(copies) != 0 {
			t.Fatalf("copies with no pending migration = %d, want 0", len(copies))
		}
	}
	assertNoPreMigrationTemps(t, path)
}

func TestPreMigrationOptionOff(t *testing.T) {
	path := preMigrationFixture(t)
	st := preMigrationStore(t, path)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if copies := preMigrationCopies(t, path); len(copies) != 0 {
		t.Fatalf("copies with option off = %d, want 0", len(copies))
	}
	assertPreMigrationState(t, path, 1, 4)
	assertNoPreMigrationTemps(t, path)
}

func TestPreMigrationUnwritableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not deny writes on Windows")
	}
	path := preMigrationFixture(t)
	st := preMigrationStore(t, path, sqlite.WithPreMigrationCopy())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0700); err != nil {
			t.Error(err)
		}
	})
	err = st.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), path+".pre-migration-") ||
		!strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Migrate error = %v, want copy path and permission denial", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("failed copy changed database bytes")
	}
	if copies := preMigrationCopies(t, path); len(copies) != 0 {
		t.Errorf("failed copy published %d files", len(copies))
	}
	assertNoPreMigrationTemps(t, path)
}

func TestPreMigrationExistingName(t *testing.T) {
	path := preMigrationFixture(t)
	st := preMigrationStore(t, path, sqlite.WithPreMigrationCopy())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte("existing copy must survive")
	now := time.Now().UTC()
	for second := -5; second <= 30; second++ {
		name := path + ".pre-migration-" + now.Add(time.Duration(second)*time.Second).Format(copyStampLayout)
		if err := os.WriteFile(name, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	err = st.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), path+".pre-migration-") ||
		!strings.Contains(err.Error(), "hard link") {
		t.Fatalf("Migrate error = %v, want copy path and failed hard link", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("name collision changed database bytes")
	}
	copies := preMigrationCopies(t, path)
	if len(copies) != 36 {
		t.Fatalf("copies after collision = %d, want 36 untouched files", len(copies))
	}
	for _, copy := range copies {
		got, err := os.ReadFile(filepath.Join(filepath.Dir(path), copy.Name))
		if err != nil || !bytes.Equal(got, contents) {
			t.Errorf("existing copy %q changed: %q, error = %v", copy.Name, got, err)
		}
	}
	assertNoPreMigrationTemps(t, path)
}

func TestPreMigrationCopies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "officer.db")
	oldName := "officer.db.pre-migration-20260101T010203Z"
	newName := "officer.db.pre-migration-20261028T111213Z"
	oldContent, newContent := []byte("old"), []byte("newer copy")
	for name, content := range map[string][]byte{oldName: oldContent, newName: newContent} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Creation time comes from the name even when mtimes say the opposite.
	if err := os.Chtimes(filepath.Join(dir, oldName), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, newName), time.Unix(0, 0), time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	invalid := []string{
		"../" + oldName,
		filepath.Join(dir, oldName),
		"another.db.pre-migration-20261028T111213Z",
		"officer.db.pre-migration-20260230T111213Z",
		"officer.db.pre-migration-20261028T111213Z.tmp",
		"officer.db.pre-migration-20261028T111213z",
		"officer.db.preparing-migration-12345",
		"",
	}
	for _, name := range []string{invalid[2], invalid[3], invalid[4], invalid[6]} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("ignored"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	linkName := "officer.db.pre-migration-20261029T111213Z"
	symlinkErr := os.Symlink(newName, filepath.Join(dir, linkName))
	if symlinkErr != nil && (runtime.GOOS != "windows" || !errors.Is(symlinkErr, syscall.Errno(1314))) {
		t.Fatal(symlinkErr)
	}
	directoryName := "officer.db.pre-migration-20261030T111213Z"
	if err := os.Mkdir(filepath.Join(dir, directoryName), 0700); err != nil {
		t.Fatal(err)
	}
	copies := preMigrationCopies(t, path)
	want := []sqlite.PreMigrationCopy{
		{Name: newName, CreatedAt: time.Date(2026, 10, 28, 11, 12, 13, 0, time.UTC), Size: int64(len(newContent))},
		{Name: oldName, CreatedAt: time.Date(2026, 1, 1, 1, 2, 3, 0, time.UTC), Size: int64(len(oldContent))},
	}
	if len(copies) != len(want) {
		t.Fatalf("listed copy count = %d, want %d", len(copies), len(want))
	}
	if !slices.Equal(copies, want) {
		t.Fatalf("listed copies = %v, want %v", copies, want)
	}
	t.Run("open", func(t *testing.T) {
		file, size, err := sqlite.OpenPreMigrationCopy(path, newName)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		}()
		if size != int64(len(newContent)) {
			t.Errorf("opened copy size = %d, want %d", size, len(newContent))
		}
		content, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, newContent) {
			t.Errorf("opened copy content = %q, want %q", content, newContent)
		}
	})
	for _, name := range invalid {
		t.Run("invalid/"+name, func(t *testing.T) {
			file, _, err := sqlite.OpenPreMigrationCopy(path, name)
			if file != nil {
				if err := file.Close(); err != nil {
					t.Error(err)
				}
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("open %q error = %v, want ErrInvalid", name, err)
			}
			if err := sqlite.RemovePreMigrationCopy(path, name); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("remove %q error = %v, want ErrInvalid", name, err)
			}
		})
	}
	for _, name := range []string{linkName, directoryName, "officer.db.pre-migration-20261031T111213Z"} {
		t.Run("not-found/"+name, func(t *testing.T) {
			if name == linkName && symlinkErr != nil {
				t.Skipf("os.Symlink requires Windows privilege: %v", symlinkErr)
			}
			file, _, err := sqlite.OpenPreMigrationCopy(path, name)
			if file != nil {
				if err := file.Close(); err != nil {
					t.Error(err)
				}
			}
			if !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("open %q error = %v, want ErrNotFound", name, err)
			}
			if err := sqlite.RemovePreMigrationCopy(path, name); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("remove %q error = %v, want ErrNotFound", name, err)
			}
		})
	}
	copyPath := filepath.Join(dir, newName)
	for _, candidate := range sqlite.DatabaseFiles(copyPath) {
		if candidate != copyPath {
			if err := os.WriteFile(candidate, []byte("sidecar"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := sqlite.RemovePreMigrationCopy(path, newName); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range sqlite.DatabaseFiles(copyPath) {
		if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("removed copy file %q still exists: %v", candidate, err)
		}
	}
	if got := preMigrationCopies(t, path); !slices.Equal(got, want[1:]) {
		t.Errorf("copies after removal = %v, want %v", got, want[1:])
	}
	if symlinkErr == nil {
		if _, err := os.Lstat(filepath.Join(dir, linkName)); err != nil {
			t.Errorf("refused symlink was removed: %v", err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, directoryName)); err != nil {
		t.Errorf("refused directory was removed: %v", err)
	}
}
