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

// Fatal-hook tests: ApplyBusinessCSVImport fires the process-level fatal hook on
// an unexpected (infrastructure) store failure, but never on a domain rejection
// the caller maps to a 4xx.

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

// newFatalHookStore opens a fresh migrated store wired with a fatal hook that
// records its invocation instead of terminating the process. It returns the
// realm handle, the store and a pointer that the hook sets when fired.
func newFatalHookStore(t *testing.T) (context.Context, RealmStore, Store, *error) {
	t.Helper()
	ctx := context.Background()
	var fired error
	path := filepath.Join(t.TempDir(), "officer.db")
	s, err := NewSQLiteStore(path, WithFatalShutdownHook(func(e error) {
		fired = e
	}))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rs, err := s.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	return ctx, rs, s, &fired
}

// TestBusinessCSVImportFiresFatalOnUnexpectedError cancels the context so the
// import's BeginTx fails with an infrastructure error (context.Canceled, not a
// domain rejection), then asserts the fatal hook fired.
func TestBusinessCSVImportFiresFatalOnUnexpectedError(t *testing.T) {
	ctx, rs, _, fired := newFatalHookStore(t)
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	err := rs.ApplyBusinessCSVImport(canceled, BusinessCSVImport{
		Accounts: []BusinessCSVImportAccount{
			{Account: domain.Account{Code: "acc-1"}},
		},
	})
	if err == nil {
		t.Fatal("ApplyBusinessCSVImport succeeded, want an infrastructure error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if *fired == nil {
		t.Fatal("fatal hook did not fire on an unexpected store error")
	}
}

// TestBusinessCSVImportDomainErrorDoesNotFireFatal imports an account that
// references an unknown group code and asserts the domain rejection (ErrInvalid)
// does not fire the fatal hook.
func TestBusinessCSVImportDomainErrorDoesNotFireFatal(t *testing.T) {
	ctx, rs, _, fired := newFatalHookStore(t)

	err := rs.ApplyBusinessCSVImport(ctx, BusinessCSVImport{
		Accounts: []BusinessCSVImportAccount{
			{Account: domain.Account{Code: "acc-1", GroupCode: "ghost"}},
		},
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyBusinessCSVImport error = %v, want ErrInvalid", err)
	}
	if *fired != nil {
		t.Fatalf("fatal hook fired on a domain rejection: %v", *fired)
	}
}
