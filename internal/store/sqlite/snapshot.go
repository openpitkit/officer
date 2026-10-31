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
	"net/url"
	"path/filepath"
	"strings"

	sqlitedriver "modernc.org/sqlite"
)

// Snapshot copies source into destination through SQLite's online backup API.
// The source file is opened read-only and never written. The context bounds
// only acquiring the connection; the Step(-1) copy is not interruptible.
// Snapshot creates no temporary files of its own.
func Snapshot(ctx context.Context, source, destination string) (result error) {
	abs, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("store: snapshot source path: %w", err)
	}
	db, err := sql.Open(driverName, snapshotSourceURI(filepath.ToSlash(abs)))
	if err != nil {
		return fmt.Errorf("store: snapshot open source: %w", err)
	}
	defer func() { result = errors.Join(result, db.Close()) }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: snapshot acquire source connection: %w", err)
	}
	defer func() { result = errors.Join(result, conn.Close()) }()
	if err := conn.Raw(func(driverConn any) error {
		copier, ok := driverConn.(interface {
			NewBackup(string) (*sqlitedriver.Backup, error)
		})
		if !ok {
			return fmt.Errorf("store: sqlite driver does not support online backup")
		}
		backup, err := copier.NewBackup(destination)
		if err != nil {
			return err
		}
		return completeBackup(backup, nil)
	}); err != nil {
		return fmt.Errorf("store: snapshot copy: %w", err)
	}
	return nil
}

func snapshotSourceURI(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	return uri.String()
}

func completeBackup(backup *sqlitedriver.Backup, onCommit func()) error {
	_, err := backup.Step(-1)
	if err == nil && onCommit != nil {
		onCommit()
	}
	return errors.Join(err, backup.Finish())
}
