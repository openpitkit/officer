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

package integration_test

import (
	"path/filepath"
	"testing"
	"time"

	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/secret"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/store/sqlite"
)

// newLocalNode builds a local node that runs the OpenPit engine on a fresh
// SQLite store of the default realm, and returns it with the store bound to
// that realm, where the application keeps its signing keys. The store is sealed
// with a master key, which signing keys need. The node is closed when the test
// ends.
func newLocalNode(t *testing.T) (node.Node, store.RealmStore) {
	t.Helper()
	return newLocalNodeBuiltBy(t, engine.NewOpenPitEngineBuildFunc())
}

// newLocalNodeBuiltBy is newLocalNode with every engine of the node, the first
// one and each rebuild, built by build.
func newLocalNodeBuiltBy(
	t *testing.T, build fwengine.BuildFunc,
) (node.Node, store.RealmStore) {
	t.Helper()
	ctx := systemCtx()
	masterKey, err := secret.ParseMasterKey("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}
	st, err := sqlite.New(
		filepath.Join(t.TempDir(), "officer.db"),
		domain.DefaultRealm,
		sqlite.WithMasterKey(masterKey),
	)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		t.Fatalf("initialize node store: %v", err)
	}
	target, _, err := node.NewLocalNode(
		ctx,
		domain.DefaultRealm,
		st,
		build,
		func(err error) { t.Errorf("unexpected fatal shutdown: %v", err) },
	)
	if err != nil {
		_ = st.Close()
		t.Fatalf("node.NewLocalNode: %v", err)
	}
	t.Cleanup(func() { _ = target.Close() })
	realm, err := st.ForRealm(ctx, target.Realm())
	if err != nil {
		t.Fatalf("bind the node store to its realm: %v", err)
	}
	return target, realm
}

func TestLocalNode_RestoreBackupNarrowedGeneralSettingsWithTradingConnection(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	target, realm := newLocalNode(t)
	if _, err := realm.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider:      domain.TradingProviderAlpaca,
		Label:         "Alpaca paper",
		Mode:          domain.TradingModePaper,
		VenueIdentity: "venue-account-1",
		Credentials:   `{"key":"secret"}`,
	}); err != nil {
		t.Fatalf("CreateTradingConnection: %v", err)
	}
	scope := backup.Scope{
		Sections: []backup.Section{backup.SectionGeneralSettings},
		Accounts: backup.EntitySelector{Accounts: []string{"selected-account"}},
	}
	archive := backup.NewArchive(
		time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
		"test",
		backup.RealmLabel{Code: string(domain.DefaultRealm)},
		scope,
		backup.Data{Settings: []backup.SettingEntry{{
			Key: "no_esign", Value: "0",
		}}},
		backup.CredentialFormPlaintext,
	)

	if _, _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeReplaceAll,
	}, auth.SystemCaller()); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	connections, err := realm.ListTradingConnections(ctx)
	if err != nil || len(connections) != 1 {
		t.Fatalf("trading connections after restore = %+v, err=%v", connections, err)
	}
}
