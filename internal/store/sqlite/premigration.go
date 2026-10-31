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

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/migration"
)

const preMigrationStampLayout = "20060102T150405Z"

// PreMigrationCopy describes a published SQLite copy. CreatedAt is the UTC
// timestamp in its name; Size is the file size in bytes.
type PreMigrationCopy struct {
	Name      string
	CreatedAt time.Time
	Size      int64
}

func (s *sqliteStore) copyBeforeMigration(ctx context.Context, db *sql.DB) error {
	recorded, err := migrationRecords(ctx, db)
	if err != nil {
		return err
	}
	if !recorded[0] || !recorded[1] {
		return nil
	}
	sharedSource, sharedConfig := s.sharedMigrationSet()
	shared, err := migration.Pending(ctx, db, sharedSource, sharedConfig)
	if err != nil {
		return fmt.Errorf("store: inspect pending shared migrations: %w", err)
	}
	applicationSource, applicationConfig := applicationMigrationSet()
	application, err := migration.Pending(ctx, db, applicationSource, applicationConfig)
	if err != nil {
		return fmt.Errorf("store: inspect pending application migrations: %w", err)
	}
	if len(shared) == 0 && len(application) == 0 {
		return nil
	}
	return s.writePreMigrationCopy(ctx)
}

func (s *sqliteStore) writePreMigrationCopy(ctx context.Context) (result error) {
	copyPath := s.path + ".pre-migration-" + time.Now().UTC().Format(preMigrationStampLayout)
	defer func() {
		if result != nil {
			result = fmt.Errorf("store: pre-migration copy %q: %w", copyPath, result)
		}
	}()
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".preparing-migration-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	published := false
	defer func() {
		if err := RemoveDatabaseFiles(tmpPath); err != nil {
			result = errors.Join(result, fmt.Errorf(
				"remove temporary database (copy published: %t): %w", published, err,
			))
		}
	}()
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := Snapshot(ctx, s.path, tmpPath); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		return fmt.Errorf("set copy permissions: %w", err)
	}
	if err := os.Link(tmpPath, copyPath); err != nil {
		return fmt.Errorf("publish by hard link: %w", err)
	}
	published = true
	return nil
}

// ListPreMigrationCopies lists only regular files with this database's exact
// copy name and a valid UTC timestamp, newest first. Symlinks are ignored.
func ListPreMigrationCopies(databasePath string) ([]PreMigrationCopy, error) {
	entries, err := os.ReadDir(filepath.Dir(databasePath))
	if err != nil {
		return nil, fmt.Errorf("store: list pre-migration copies: %w", err)
	}
	copies := make([]PreMigrationCopy, 0)
	for _, entry := range entries {
		createdAt, err := preMigrationCopyTime(databasePath, entry.Name())
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("store: inspect pre-migration copy %q: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		copies = append(copies, PreMigrationCopy{
			Name: entry.Name(), CreatedAt: createdAt, Size: info.Size(),
		})
	}
	slices.SortFunc(copies, func(a, b PreMigrationCopy) int {
		return b.CreatedAt.Compare(a.CreatedAt)
	})
	return copies, nil
}

func preMigrationCopyTime(databasePath, name string) (time.Time, error) {
	prefix := filepath.Base(databasePath) + ".pre-migration-"
	stamp, ok := strings.CutPrefix(name, prefix)
	if ok {
		createdAt, err := time.Parse(preMigrationStampLayout, stamp)
		if err == nil && createdAt.Format(preMigrationStampLayout) == stamp {
			return createdAt, nil
		}
	}
	return time.Time{}, fmt.Errorf("store: invalid pre-migration copy name %q: %w", name, domain.ErrInvalid)
}

// OpenPreMigrationCopy opens a regular copy for reading and returns its size.
// The caller must close the file. Invalid names wrap domain.ErrInvalid;
// absent copies, non-regular files and replaced files wrap domain.ErrNotFound.
func OpenPreMigrationCopy(databasePath, name string) (*os.File, int64, error) {
	path, info, err := preMigrationCopyPath(databasePath, name)
	if err != nil {
		return nil, 0, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, fmt.Errorf("store: pre-migration copy %q: %w", name, domain.ErrNotFound)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("store: open pre-migration copy %q: %w", name, err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, 0, errors.Join(
			fmt.Errorf("store: inspect opened pre-migration copy %q: %w", name, err), file.Close(),
		)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		if err := file.Close(); err != nil {
			return nil, 0, fmt.Errorf("store: close pre-migration copy %q: %w", name, err)
		}
		return nil, 0, fmt.Errorf("store: pre-migration copy %q: %w", name, domain.ErrNotFound)
	}
	return file, openedInfo.Size(), nil
}

// RemovePreMigrationCopy removes one regular copy and its SQLite sidecars.
// Invalid names wrap domain.ErrInvalid; missing or non-regular copies wrap
// domain.ErrNotFound. Copies are never removed automatically.
func RemovePreMigrationCopy(databasePath, name string) error {
	path, _, err := preMigrationCopyPath(databasePath, name)
	if err != nil {
		return err
	}
	return RemoveDatabaseFiles(path)
}

func preMigrationCopyPath(databasePath, name string) (string, os.FileInfo, error) {
	if _, err := preMigrationCopyTime(databasePath, name); err != nil {
		return "", nil, err
	}
	path := filepath.Join(filepath.Dir(databasePath), name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return "", nil, fmt.Errorf("store: pre-migration copy %q: %w", name, domain.ErrNotFound)
	}
	if err != nil {
		return "", nil, fmt.Errorf("store: inspect pre-migration copy %q: %w", name, err)
	}
	return path, info, nil
}
