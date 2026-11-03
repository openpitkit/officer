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

package sqlite

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// ensurePrivateDatabaseFile creates an absent database file at path with
// databaseFileMode whatever the umask, before SQLite would create it with its
// default 0644. SQLite creates the journal and WAL sidecars with the database
// file's mode, but keeps the mode of a non-empty sidecar it finds, so the
// database file and every existing sidecar with another mode are set to
// databaseFileMode, and each change is logged with the path and both modes.
// A path that is not a regular file is an error. SQLite opens the target of a
// symbolic link at the database path and keeps the sidecars beside it, so that
// link is resolved first, and a link to a missing file is an error; a sidecar
// that is a symbolic link is an error.
func ensurePrivateDatabaseFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect database file %q: %w", path, err)
	}
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolve database path %q: %w", path, err)
		}
		path = target
	}
	if err := createPrivateFile(path); err != nil {
		return err
	}
	for _, file := range DatabaseFiles(path) {
		if err := narrowFileMode(file); err != nil {
			return err
		}
	}
	return nil
}

// createPrivateFile creates path with databaseFileMode, leaving an existing
// file to narrowFileMode.
func createPrivateFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, databaseFileMode)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create database file %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close created database file %q: %w", path, err)
	}
	// The umask may have cleared bits of the requested mode.
	if err := os.Chmod(path, databaseFileMode); err != nil {
		return fmt.Errorf("set created database file %q mode: %w", path, err)
	}
	return nil
}

// narrowFileMode sets an existing database file or sidecar to
// databaseFileMode and logs the change. An absent file is left absent. The
// path itself is inspected, so a symbolic link is not a regular file: chmod
// would follow it and change a file outside the database's control.
func narrowFileMode(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect database file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("database path %q is not a regular file (mode %v)", path, info.Mode())
	}
	previous := info.Mode().Perm()
	if previous == databaseFileMode {
		return nil
	}
	if err := os.Chmod(path, databaseFileMode); err != nil {
		return fmt.Errorf(
			"set database file %q mode %04o to %04o: %w; make the user running Officer the owner of the file and set its mode to %04o",
			path, previous, databaseFileMode, err, databaseFileMode,
		)
	}
	slog.Warn(
		"SQLite database file mode set to owner-only",
		"path", path,
		"old_mode", fmt.Sprintf("%04o", previous),
		"new_mode", fmt.Sprintf("%04o", databaseFileMode),
	)
	return nil
}
