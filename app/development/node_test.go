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

package development_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.openpit.dev/officer/app/development"
	"go.openpit.dev/officer/framework/domain"
)

func TestNodeHydratesAndResetsItsConfiguredDataset(t *testing.T) {
	runtimeLibrary := os.Getenv("OPENPIT_RUNTIME_LIBRARY_PATH")
	if runtimeLibrary == "" {
		t.Fatal("OPENPIT_RUNTIME_LIBRARY_PATH is required; use the local SDK development gate")
	}
	ctx := context.Background()
	cfg := development.Config{
		Realm:              "development-tenant",
		DatabasePath:       filepath.Join(t.TempDir(), "node.sqlite"),
		RuntimeLibraryPath: runtimeLibrary,
	}
	first, err := development.NewNode(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
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
	second, err := development.NewNode(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
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

func TestNodeRequiresExplicitRuntimeResources(t *testing.T) {
	for _, cfg := range []development.Config{
		{Realm: "tenant"},
		{Realm: "tenant", DatabasePath: "node.sqlite"},
		{Realm: "tenant", RuntimeLibraryPath: "runtime"},
		{DatabasePath: "node.sqlite", RuntimeLibraryPath: "runtime"},
	} {
		if _, err := development.NewNode(context.Background(), cfg); err == nil {
			t.Fatalf("incomplete config accepted: %+v", cfg)
		}
	}
}
