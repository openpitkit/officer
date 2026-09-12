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

package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

type blockLifecycleOperation string

func TestAccountBlockLifecycleShortSequences(t *testing.T) {
	t.Parallel()
	const (
		typedFirst  blockLifecycleOperation = "engine typed first"
		typedSecond blockLifecycleOperation = "engine typed second"
		plain       blockLifecycleOperation = "operator plain"
		unblock     blockLifecycleOperation = "unblock"
		reload      blockLifecycleOperation = "persist and reload"
		backupMove  blockLifecycleOperation = "backup export and import"
	)
	sequences := [][]blockLifecycleOperation{
		{typedFirst, reload},
		{typedFirst, typedSecond, reload},
		{typedFirst, plain, reload},
		{plain, typedFirst, reload},
		{typedFirst, backupMove, typedSecond},
		{typedFirst, backupMove, plain},
		{typedFirst, unblock, typedSecond, reload},
		{typedFirst, unblock, plain, backupMove},
		{plain, unblock, typedFirst, backupMove},
		{typedFirst, typedSecond, backupMove, reload},
		{typedFirst, unblock, backupMove, reload},
	}
	causes := map[blockLifecycleOperation]domain.AccountBlock{
		typedFirst: {
			Account: "acc-1", Policy: "SpotFundsPolicy",
			Code:   domain.RejectCodePnlKillSwitchTriggered,
			Reason: "first reason", Details: "first details",
		},
		typedSecond: {
			Account: "acc-1", Policy: "OtherPolicy",
			Code:   domain.RejectCodeRiskLimitExceeded,
			Reason: "second reason", Details: "second details",
		},
	}

	for i, sequence := range sequences {
		sequence := sequence
		t.Run(sequenceName(i, sequence), func(t *testing.T) {
			ctx := context.Background()
			state := newBlockLifecycleStore(t, ctx)
			if _, err := state.realm.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
				t.Fatalf("CreateAccount: %v", err)
			}
			want := domain.Account{Code: "acc-1"}
			for step, op := range sequence {
				switch op {
				case typedFirst, typedSecond:
					cause := causes[op]
					if err := state.realm.SetAccountBlock(ctx, cause); err != nil {
						t.Fatalf("step %d %s: %v", step, op, err)
					}
					if !want.Blocked {
						want.Blocked = true
						want.BlockReason = cause.Reason
						want.BlockPolicy = cause.Policy
						want.BlockCode = cause.Code
						want.BlockDetails = cause.Details
					}
				case plain:
					err := state.realm.SetAccountBlocked(ctx, "acc-1", true, "operator reason")
					if want.Blocked && want.BlockCode != "" {
						if !errors.Is(err, domain.ErrConflict) {
							t.Fatalf("step %d %s = %v, want ErrConflict", step, op, err)
						}
					} else if err != nil {
						t.Fatalf("step %d %s: %v", step, op, err)
					} else {
						want.Blocked = true
						want.BlockReason = "operator reason"
					}
				case unblock:
					if err := state.realm.SetAccountBlocked(ctx, "acc-1", false, "ignored"); err != nil {
						t.Fatalf("step %d %s: %v", step, op, err)
					}
					want.Blocked = false
					want.BlockReason = ""
					want.BlockPolicy = ""
					want.BlockCode = ""
					want.BlockDetails = ""
				case reload:
					state.reload(t, ctx)
				case backupMove:
					state.backupMove(t, ctx)
				default:
					t.Fatalf("step %d: unknown operation %q", step, op)
				}
				assertStoredBlockCause(t, ctx, state.realm, step, op, want)
			}
		})
	}
}

func sequenceName(index int, sequence []blockLifecycleOperation) string {
	parts := make([]string, len(sequence))
	for i, operation := range sequence {
		parts[i] = string(operation)
	}
	return fmt.Sprintf("%02d_%s", index, strings.Join(parts, "_"))
}

type blockLifecycleStore struct {
	path  string
	store fwstore.Store
	realm fwstore.RealmStore
}

func newBlockLifecycleStore(
	t *testing.T, ctx context.Context,
) *blockLifecycleStore {
	t.Helper()
	state := &blockLifecycleStore{
		path: filepath.Join(t.TempDir(), "officer.db"),
	}
	state.open(t, ctx, true)
	t.Cleanup(func() {
		if state.store != nil {
			_ = state.store.Close()
		}
	})
	return state
}

func (s *blockLifecycleStore) open(
	t *testing.T, ctx context.Context, migrate bool,
) {
	t.Helper()
	opened, err := New(s.path)
	if err != nil {
		t.Fatalf("New(%s): %v", s.path, err)
	}
	if migrate {
		if err := opened.Migrate(ctx); err != nil {
			_ = opened.Close()
			t.Fatalf("Migrate(%s): %v", s.path, err)
		}
	}
	realm, err := opened.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		_ = opened.Close()
		t.Fatalf("ForRealm(%s): %v", s.path, err)
	}
	s.store = opened
	s.realm = realm
}

func (s *blockLifecycleStore) reload(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := s.store.Close(); err != nil {
		t.Fatalf("Close before reload: %v", err)
	}
	s.store = nil
	s.realm = nil
	s.open(t, ctx, false)
}

func (s *blockLifecycleStore) backupMove(t *testing.T, ctx context.Context) {
	t.Helper()
	scope := backup.Scope{Sections: []backup.Section{backup.SectionAccountsGroups}}
	archive, err := s.realm.ExportBackup(ctx, scope)
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	target := &blockLifecycleStore{
		path: filepath.Join(t.TempDir(), "restored.db"),
	}
	target.open(t, ctx, true)
	if _, err := target.realm.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope,
		Mode:  backup.RestoreModeReplaceAll,
	}); err != nil {
		_ = target.store.Close()
		t.Fatalf("RestoreBackup: %v", err)
	}
	if err := s.store.Close(); err != nil {
		_ = target.store.Close()
		t.Fatalf("Close before backup move: %v", err)
	}
	s.path = target.path
	s.store = target.store
	s.realm = target.realm
}

func assertStoredBlockCause(
	t *testing.T,
	ctx context.Context,
	realm fwstore.RealmStore,
	step int,
	op blockLifecycleOperation,
	want domain.Account,
) {
	t.Helper()
	got, ok, err := realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("step %d %s GetAccount = ok %v, err %v", step, op, ok, err)
	}
	if got.Blocked != want.Blocked || got.BlockReason != want.BlockReason ||
		got.BlockPolicy != want.BlockPolicy || got.BlockCode != want.BlockCode ||
		got.BlockDetails != want.BlockDetails {
		t.Fatalf("step %d %s block = %+v, want %+v", step, op, got, want)
	}
}
