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
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestSnapshotSourceURI(t *testing.T) {
	for _, tt := range []struct {
		name string
		path string
		want string
	}{
		{"Windows drive", "C:/data/release.db", "file:///C:/data/release.db?mode=ro"},
		{"Windows UNC", "//server/share/release.db", "file:////server/share/release.db?mode=ro"},
		{"escaped Unix path", "/data/release ?#%.db", "file:///data/release%20%3F%23%25.db?mode=ro"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := snapshotSourceURI(tt.path); got != tt.want {
				t.Errorf("snapshot source URI = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSnapshotCopiesWithoutChangingSource(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "release #%.db")
	seedPath := filepath.Join(filepath.Dir(source), "seed.db")
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	st, err := New(seedPath, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	want, err := realm.CreateAsset(ctx, domain.Asset{Code: "AAPL", Title: "Apple"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(seedPath, source); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := Snapshot(ctx, source, destination); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Error("snapshot changed source bytes")
	}
	if !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Error("snapshot changed source modification time")
	}
	for _, path := range DatabaseFiles(source)[1:] {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("source sidecar %q exists or cannot be checked: %v", path, err)
		}
	}
	copyStore, err := New(destination, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = copyStore.Close() })
	copyRealm, err := copyStore.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := copyRealm.GetAsset(ctx, "AAPL")
	if err != nil || !ok || got != want {
		t.Fatalf("snapshot asset = %+v, found=%v, err=%v; want %+v", got, ok, err, want)
	}
}

func TestSnapshotMissingSourceCreatesNothing(t *testing.T) {
	source := filepath.Join(t.TempDir(), "missing.db")
	err := Snapshot(context.Background(), source, filepath.Join(t.TempDir(), "copy.db"))
	if err == nil {
		t.Error("snapshot of missing source succeeded")
	}
	for _, path := range DatabaseFiles(source) {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("snapshot created missing source file %q: %v", path, err)
		}
	}
}

func TestSnapshotRejectsNonSQLiteSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "invalid.db")
	if err := os.WriteFile(source, []byte("not an SQLite database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Snapshot(context.Background(), source, filepath.Join(t.TempDir(), "copy.db")); err == nil {
		t.Fatal("snapshot accepted non-SQLite source")
	}
}
