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

// Package nonrelease names and tidies the database files of a non-release
// Officer build.
package nonrelease

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.openpit.dev/officer/internal/store/sqlite"
)

// DatabasePath derives the own database path from a plain release file path
// and a commit already validated by the host's build metadata.
func DatabasePath(releasePath, commit string) (string, error) {
	if commit == "" {
		return "", fmt.Errorf("nonrelease: empty commit %q", commit)
	}
	if file, err := sqlite.DatabaseFilePath(releasePath); err != nil || file != releasePath {
		return "", fmt.Errorf("nonrelease: release path %q is not a plain file path", releasePath)
	}
	return releasePath + ".non-release-" + commit, nil
}

// ReleaseDatabaseExists reports whether the release path holds a regular file.
// A missing file is absent; any other file type or stat failure is an error.
func ReleaseDatabaseExists(releasePath string) (bool, error) {
	info, err := os.Stat(releasePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("nonrelease: stat release database %q: %w", releasePath, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("nonrelease: release database %q is not a regular file", releasePath)
	}
	return true, nil
}

// RemoveStale removes entries with the release database's non-release prefix,
// preserving the own database and its sidecars. Removed paths are sorted.
// It stops at the first removal error and names the failed path.
func RemoveStale(releasePath, commit string) ([]string, error) {
	own, err := DatabasePath(releasePath, commit)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(releasePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("nonrelease: read database directory %q: %w", dir, err)
	}
	prefix := filepath.Base(releasePath) + ".non-release-"
	preserved := sqlite.DatabaseFiles(filepath.Clean(own))
	var removed []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if slices.Contains(preserved, path) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return removed, fmt.Errorf("nonrelease: remove stale database entry %q: %w", path, err)
		}
		removed = append(removed, path)
	}
	slices.Sort(removed)
	return removed, nil
}
