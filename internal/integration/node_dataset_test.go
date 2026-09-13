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

package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/internal/store/sqlite"
)

func TestNodeHydratesAndResetsItsConfiguredDataset(t *testing.T) {
	if os.Getenv("OPENPIT_RUNTIME_LIBRARY_PATH") == "" {
		t.Fatal("OPENPIT_RUNTIME_LIBRARY_PATH is required; use the local SDK development gate")
	}
	ctx := context.Background()
	const realm domain.RealmID = "development-tenant"
	databasePath := filepath.Join(t.TempDir(), "node.sqlite")
	newNode := func() node.Node {
		t.Helper()
		st, err := sqlite.New(databasePath, realm)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Migrate(ctx); err != nil {
			_ = st.Close()
			t.Fatal(err)
		}
		target, _, err := node.NewLocalNode(
			ctx,
			realm,
			st,
			engine.NewOpenPitEngineBuildFunc(),
			func(err error) { t.Errorf("unexpected fatal shutdown: %v", err) },
		)
		if err != nil {
			_ = st.Close()
			t.Fatal(err)
		}
		return target
	}

	first := newNode()
	health, err := first.Health(ctx)
	if err != nil || !health.Engine.Running || !health.Store.Reachable {
		t.Fatalf("Health = %+v, %v", health, err)
	}
	caller := domain.Caller{Source: domain.SourceSystem}
	if _, err := first.CreateAccount(ctx, domain.Account{Code: "persisted"}, caller); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	second := newNode()
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
	})
	accounts, err := second.ListAccounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].Code != "persisted" {
		t.Fatalf("rehydrated accounts = %+v, %v", accounts, err)
	}
	if _, err := second.ResetDatabase(ctx, caller); err != nil {
		t.Fatal(err)
	}
	accounts, err = second.ListAccounts(ctx)
	if err != nil || len(accounts) != 0 {
		t.Fatalf("accounts after reset = %+v, %v", accounts, err)
	}
}
