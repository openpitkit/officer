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

//go:build unix

// Backup snapshot permission tests: the export snapshot must stay private to
// the process owner under any umask. They change the process umask, so they
// must never run in parallel.

package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
)

func TestBackupSnapshotIsPrivateUnderAnyUmask(t *testing.T) {
	for _, umask := range []int{0o022, 0o000} {
		t.Run(fmt.Sprintf("umask %03o", umask), func(t *testing.T) {
			ctx := context.Background()
			_, src := newRealmStore(t, domain.DefaultRealm)
			seedRealm(t, ctx, src)
			realm, ok := src.(*realmStore)
			if !ok {
				t.Fatalf("realm store type = %T, want *realmStore", src)
			}
			tempRoot := t.TempDir()
			t.Setenv("TMPDIR", tempRoot)
			previous := syscall.Umask(umask)
			t.Cleanup(func() { syscall.Umask(previous) })

			_, err := realm.exportBackup(ctx, backup.Scope{All: true}, func() error {
				entries, err := os.ReadDir(tempRoot)
				if err != nil {
					return err
				}
				if len(entries) != 1 {
					return fmt.Errorf(
						"temp dir holds %d entries, want one snapshot directory",
						len(entries),
					)
				}
				info, err := entries[0].Info()
				if err != nil {
					return err
				}
				if !info.IsDir() {
					return fmt.Errorf(
						"snapshot %q mode %v lies directly in the shared temp dir, want a private directory",
						info.Name(), info.Mode(),
					)
				}
				if perm := info.Mode().Perm(); perm&^0o700 != 0 {
					return fmt.Errorf(
						"snapshot directory %q mode %04o, want no wider than 0700",
						info.Name(), perm,
					)
				}
				snapshotFiles, err := os.ReadDir(filepath.Join(tempRoot, info.Name()))
				if err != nil {
					return err
				}
				if len(snapshotFiles) == 0 {
					return fmt.Errorf("snapshot directory %q is empty", info.Name())
				}
				return nil
			})
			if err != nil {
				t.Fatalf("exportBackup: %v", err)
			}
			entries, err := os.ReadDir(tempRoot)
			if err != nil {
				t.Fatalf("ReadDir(temp dir): %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("temp dir after export holds %d entries, want none", len(entries))
			}
		})
	}
}
