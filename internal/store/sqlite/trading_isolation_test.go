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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
)

func isolationRegistry(t testing.TB) *trading.Registry {
	t.Helper()
	registry := trading.NewRegistry()
	for _, p := range []trading.Provider{
		{Type: "fixture-test", ExecutionPolicy: true},
		{Type: "fixture-venue", ExecutionPolicy: false},
	} {
		p.Build = func(trading.BuildContext) (trading.Connector, error) {
			return nil, errors.New("fixture connector must not be built")
		}
		if err := registry.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func isolationFixture(t testing.TB) (store.Store, *realmStore, [2]domain.ExternalID) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "isolation.db")
	migrated, err := migratedDatabaseBytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, migrated, 0o600); err != nil {
		t.Fatal(err)
	}
	st, r := isolationHandle(t, path)
	ctx := context.Background()
	for _, code := range []string{"g", "h"} {
		if _, err := r.CreateGroup(ctx, domain.AccountGroup{Code: code}); err != nil {
			t.Fatal(err)
		}
	}
	for _, code := range []domain.AccountID{"A", "B"} {
		if _, err := r.CreateAccount(ctx, domain.Account{Code: code}); err != nil {
			t.Fatal(err)
		}
	}
	var connections [2]domain.ExternalID
	for i, provider := range []string{"fixture-test", "fixture-venue"} {
		mode := domain.TradingModePaper
		if i == 1 {
			mode = domain.TradingModeLive
		}
		c, err := r.CreateTradingConnection(ctx, domain.TradingConnection{
			Provider: provider, Label: provider, VenueIdentity: provider,
			Mode: mode, Credentials: "{}",
		})
		if err != nil {
			t.Fatal(err)
		}
		connections[i] = c.ExternalID
	}
	return st, r, connections
}

func isolationHandle(t testing.TB, path string) (store.Store, *realmStore) {
	return isolationClassifiedHandle(t, path, isolationRegistry(t).SupportsExecutionPolicy)
}

func isolationClassifiedHandle(t testing.TB, path string, classifier domain.ExecutionPolicyProviderLookup) (store.Store, *realmStore) {
	t.Helper()
	st, err := New(path, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := st.BindExecutionPolicyProviders(classifier); err != nil {
		t.Fatal(err)
	}
	r, err := st.ForRealm(context.Background(), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	return st, r.(*realmStore)
}

func isolationGrant(t testing.TB, r store.RealmStore, c domain.ExternalID, account domain.AccountID, group string) {
	t.Helper()
	if err := r.PutTradingAccess(context.Background(), domain.TradingAccess{Account: account, AccountGroup: group, Connection: c}); err != nil {
		t.Fatal(err)
	}
}

func isolationRefused(t testing.TB, err error, account string) {
	t.Helper()
	if err != nil {
		t.Fatalf("mixed trading access for %s must be accepted: %v", account, err)
	}
}

func isolationState(t testing.TB, r *realmStore) []string {
	t.Helper()
	db, err := r.db()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := scanStrings(db.QueryContext(context.Background(),
		`SELECT 'account:' || code || ':' || COALESCE(group_id, 0) FROM account
		 UNION ALL SELECT 'grant:' || COALESCE(account_id, 0) || ':' ||
		 COALESCE(account_group_id, 0) || ':' || connection_id || ':' || venue_account
		 FROM trading_access ORDER BY 1`))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func assertIsolation(t testing.TB, r *realmStore) {
	t.Helper()
	accounts, err := r.ListAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accounts {
		grants, err := r.ListTradingAccess(context.Background(), account.Code)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[domain.ExternalID]bool{}
		for _, grant := range grants {
			if seen[grant.Connection] {
				t.Fatalf("duplicate effective connection %s for %s", grant.Connection, account.Code)
			}
			seen[grant.Connection] = true
			if grant.Account != account.Code && (account.GroupCode == "" || grant.AccountGroup != account.GroupCode) {
				t.Fatalf("unrelated grant for %s: %+v", account.Code, grant)
			}
		}
		if got := len(seen); got < isolationModeCount(t, r, account.Code) {
			t.Fatalf("mode summary exceeds effective connections for %s", account.Code)
		}
	}
}

func isolationModeCount(t testing.TB, r *realmStore, account domain.AccountID) int {
	t.Helper()
	db, err := r.db()
	if err != nil {
		t.Fatal(err)
	}
	var classes int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(DISTINCT c.mode) FROM account a
		 JOIN trading_access access ON
		 access.account_id = a.id OR access.account_group_id = a.group_id
		 JOIN trading_connection c ON c.id = access.connection_id
		 WHERE a.code = ?`, account.String()).Scan(&classes); err != nil {
		t.Fatal(err)
	}
	return classes
}

func TestTradingIsolationGrants(t *testing.T) {
	for _, tc := range []struct {
		name, firstGroup, secondGroup string
		first, second                 int
	}{
		{"test_then_venue", "", "", 0, 1},
		{"venue_then_test", "", "", 1, 0},
		{"own_then_group", "", "g", 0, 1},
		{"group_then_own", "g", "", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, r, c := isolationFixture(t)
			ctx := context.Background()
			if err := r.SetAccountGroup(ctx, "A", "g"); err != nil {
				t.Fatal(err)
			}
			account := domain.AccountID("A")
			if tc.firstGroup != "" {
				account = ""
			}
			isolationGrant(t, r, c[tc.first], account, tc.firstGroup)
			before := isolationState(t, r)
			account = "A"
			if tc.secondGroup != "" {
				account = ""
			}
			err := r.PutTradingAccess(ctx, domain.TradingAccess{Account: account, AccountGroup: tc.secondGroup, Connection: c[tc.second]})
			isolationRefused(t, err, "A")
			if reflect.DeepEqual(before, isolationState(t, r)) {
				t.Fatal("refused grant changed state")
			}
		})
	}
}

func TestTradingIsolationSameModeDifferentProviders(t *testing.T) {
	for _, mode := range []domain.TradingMode{domain.TradingModePaper, domain.TradingModeLive} {
		t.Run(string(mode), func(t *testing.T) {
			_, r, c := isolationFixture(t)
			primary, opposite := c[0], c[1]
			provider := "fixture-venue"
			if mode == domain.TradingModeLive {
				primary, opposite = opposite, primary
				provider = "fixture-test"
			}
			additional, err := r.CreateTradingConnection(context.Background(), domain.TradingConnection{
				Provider: provider, Label: "same-mode", VenueIdentity: "another-account", Mode: mode, Credentials: "{}",
			})
			if err != nil {
				t.Fatal(err)
			}
			isolationGrant(t, r, primary, "A", "")
			isolationGrant(t, r, additional.ExternalID, "A", "")
			isolationRefused(t, r.PutTradingAccess(context.Background(), domain.TradingAccess{Account: "A", Connection: opposite}), "A")
		})
	}
}

func TestTradingIsolationMove(t *testing.T) {
	_, r, c := isolationFixture(t)
	isolationGrant(t, r, c[0], "A", "")
	isolationGrant(t, r, c[1], "", "g")
	before := isolationState(t, r)
	isolationRefused(t, r.SetAccountGroup(context.Background(), "A", "g"), "A")
	if reflect.DeepEqual(before, isolationState(t, r)) {
		t.Fatal("refused move changed membership")
	}
}

func TestTradingIsolationCreateInMixedGroup(t *testing.T) {
	_, r, c := isolationFixture(t)
	for _, connection := range c {
		isolationGrant(t, r, connection, "", "g")
	}
	_, err := r.CreateAccount(context.Background(), domain.Account{Code: "new", GroupCode: "g"})
	isolationRefused(t, err, "new")
	if _, ok, err := r.GetAccount(context.Background(), "new"); err != nil || !ok {
		t.Fatalf("created account exists=%t, %v", ok, err)
	}
}

func TestTradingIsolationDisabledCounts(t *testing.T) {
	_, r, c := isolationFixture(t)
	isolationGrant(t, r, c[0], "A", "")
	isolationRefused(t, r.PutTradingAccess(context.Background(), domain.TradingAccess{Account: "A", Connection: c[1]}), "A")
}

func TestTradingIsolationUnknownAffectedOnly(t *testing.T) {
	_, r, c := isolationFixture(t)
	ctx := context.Background()
	isolationGrant(t, r, c[0], "A", "")
	db, err := r.db()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE trading_connection SET provider = 'missing-provider' WHERE external_id = ?`, c[0].Bytes()); err != nil {
		t.Fatal(err)
	}
	err = r.PutTradingAccess(ctx, domain.TradingAccess{Account: "A", Connection: c[1]})
	if err != nil {
		t.Fatalf("unknown provider refusal = %v", err)
	}
	isolationGrant(t, r, c[1], "B", "")
	if err := r.RemoveTradingAccess(ctx, c[0], "A", ""); err != nil {
		t.Fatal(err)
	}
	isolationGrant(t, r, c[1], "A", "")
	if err := r.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: "h", Connection: c[0]}); err != nil {
		t.Fatalf("empty group unknown provider = %v", err)
	}
}

func TestTradingIsolationRestoreRollback(t *testing.T) {
	for _, site := range []string{"membership", "grant"} {
		for _, mode := range []backup.RestoreMode{backup.RestoreModeOverwrite, backup.RestoreModeInsertMissing} {
			if site == "membership" && mode == backup.RestoreModeInsertMissing {
				continue
			}
			t.Run(site+"/"+string(mode), func(t *testing.T) {
				_, r, c := isolationFixture(t)
				ctx := context.Background()
				isolationGrant(t, r, c[0], "A", "")
				isolationGrant(t, r, c[1], "", "g")
				before := snapshotBackupData(t, ctx, r)
				archive := exportPolicyArchive(t, r)
				var scope backup.Scope
				if site == "membership" {
					scope.Sections = []backup.Section{backup.SectionAccountsGroups}
					archive.Data.Accounts[0].GroupCode = "g"
					archive.Data.Accounts[0].Title = "must roll back"
				} else {
					scope.Sections = []backup.Section{backup.SectionTrading}
					archive.Data = backup.Data{TradingAccess: []backup.TradingAccess{
						{Account: "B", Connection: c[1]}, {Account: "A", Connection: c[1]},
					}}
				}
				_, err := r.RestoreBackup(ctx, archive, backup.RestoreOptions{Scope: scope, Mode: mode,
					TradingProviderRequiresFeeAccount: func(string) (bool, error) { return false, nil },
				})
				isolationRefused(t, err, "A")
				if reflect.DeepEqual(before, snapshotBackupData(t, ctx, r)) {
					t.Fatal("refused restore changed prior state")
				}
			})
		}
	}
}

func TestTradingIsolationRestoreUnknownAffectedOnly(t *testing.T) {
	_, r, c := isolationFixture(t)
	ctx := context.Background()
	isolationGrant(t, r, c[0], "A", "")
	archive := exportPolicyArchive(t, r)
	db, err := r.db()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE trading_connection SET provider = 'missing-provider' WHERE external_id = ?`, c[0].Bytes()); err != nil {
		t.Fatal(err)
	}
	opts := backup.RestoreOptions{Mode: backup.RestoreModeOverwrite,
		Scope: backup.Scope{Sections: []backup.Section{backup.SectionAccountsGroups}},
	}
	archive.Data = backup.Data{Accounts: []backup.Account{{Code: "B", GroupCode: "g"}}}
	if _, err := r.RestoreBackup(ctx, archive, opts); err != nil {
		t.Fatalf("unrelated membership blocked: %v", err)
	}
	archive.Data = backup.Data{Accounts: []backup.Account{{Code: "A", GroupCode: "g"}}}
	if _, err := r.RestoreBackup(ctx, archive, opts); err != nil {
		t.Fatalf("same-mode membership depends on provider metadata: %v", err)
	}
	before := snapshotBackupData(t, ctx, r)
	opts.Scope.Sections = []backup.Section{backup.SectionTrading}
	opts.TradingProviderRequiresFeeAccount = func(string) (bool, error) { return false, nil }
	archive.Data = backup.Data{TradingAccess: []backup.TradingAccess{{Account: "A", Connection: c[1]}}}
	_, err = r.RestoreBackup(ctx, archive, opts)
	if err != nil {
		t.Fatalf("mixed access restore = %v", err)
	}
	if reflect.DeepEqual(before, snapshotBackupData(t, ctx, r)) {
		t.Fatal("refused access restore changed state")
	}
}

func TestTradingIsolationIndependentHandles(t *testing.T) {
	for _, scenario := range []string{"grants", "group_move", "own_group", "disabled"} {
		t.Run(scenario, func(t *testing.T) {
			st, r, c := isolationFixture(t)
			_, other := isolationHandle(t, st.Path())
			ctx := context.Background()
			for i, connection := range c {
				if err := r.SetTradingConnectionEnabled(ctx, connection, scenario != "disabled" || i != 0); err != nil {
					t.Fatal(err)
				}
			}
			detections := 0
			for iteration := range 100 {
				account := domain.AccountID(fmt.Sprintf("concurrent-%d", iteration))
				group := fmt.Sprintf("group-%d", iteration)
				if _, err := r.CreateAccount(ctx, domain.Account{Code: account}); err != nil {
					t.Fatal(err)
				}
				if _, err := r.CreateGroup(ctx, domain.AccountGroup{Code: group}); err != nil {
					t.Fatal(err)
				}
				if scenario == "group_move" {
					isolationGrant(t, r, c[0], account, "")
				}
				if scenario == "own_group" {
					if err := r.SetAccountGroup(ctx, account, group); err != nil {
						t.Fatal(err)
					}
				}
				start := make(chan struct{})
				var errs [2]error
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-start
					if scenario != "group_move" {
						errs[0] = r.PutTradingAccess(ctx, domain.TradingAccess{Account: account, Connection: c[0]})
					} else {
						errs[0] = r.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: group, Connection: c[1]})
					}
				}()
				go func() {
					defer wg.Done()
					<-start
					if scenario == "own_group" {
						errs[1] = other.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: group, Connection: c[1]})
					} else if scenario != "group_move" {
						errs[1] = other.PutTradingAccess(ctx, domain.TradingAccess{Account: account, Connection: c[1]})
					} else {
						errs[1] = other.SetAccountGroup(ctx, account, group)
					}
				}()
				close(start)
				wg.Wait()
				// Independent SQLite handles can contend for the write lock. The
				// rejected statement must succeed when submitted serially; neither
				// handle may impose a paper/live restriction.
				for index, writeErr := range errs {
					if writeErr == nil {
						continue
					}
					if !strings.Contains(writeErr.Error(), "SQLITE_BUSY") {
						t.Fatal(writeErr)
					}
					if index == 0 && scenario != "group_move" {
						errs[index] = r.PutTradingAccess(ctx, domain.TradingAccess{Account: account, Connection: c[0]})
					} else if index == 0 || scenario == "own_group" {
						errs[index] = r.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: group, Connection: c[1]})
					} else if scenario == "group_move" {
						errs[index] = other.SetAccountGroup(ctx, account, group)
					} else {
						errs[index] = other.PutTradingAccess(ctx, domain.TradingAccess{Account: account, Connection: c[1]})
					}
				}
				classes := isolationModeCount(t, r, account)
				if errs[0] != nil || errs[1] != nil || classes != 2 {
					detections++
					t.Errorf("iteration %d: incompatible writes committed, classes=%d, errors=%v", iteration, classes, errs)
				}
			}
			t.Logf("iterations=100 detections=%d", detections)
		})
	}
}

func FuzzTradingIsolation(f *testing.F) {
	// Bytes encode operation (low 3 bits), provider (bit 3), subject (bit 4).
	for _, seed := range [][]byte{
		{0, 8}, {1, 8, 2}, {0, 9, 2}, {1, 9, 3}, {0, 4, 8}, {1, 12, 9, 3},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, sequence []byte) {
		_, r, connections := isolationFixture(t)
		ctx := context.Background()
		if len(sequence) > 32 {
			sequence = sequence[:32]
		}
		for i, b := range sequence {
			account := domain.AccountID("A")
			group := "g"
			if b&16 != 0 {
				account, group = "B", "h"
			}
			connection := connections[(b>>3)&1]
			before := isolationState(t, r)
			var err error
			switch b % 8 {
			case 0, 5:
				err = r.PutTradingAccess(ctx, domain.TradingAccess{Account: account, Connection: connection})
			case 1, 6:
				err = r.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: group, Connection: connection})
			case 2:
				err = r.SetAccountGroup(ctx, account, group)
			case 3:
				_, err = r.CreateAccount(ctx, domain.Account{Code: domain.AccountID(fmt.Sprintf("created-%d", i)), GroupCode: group})
			case 4:
				err = r.RemoveTradingAccess(ctx, connection, account, "")
			case 7:
				err = r.RemoveTradingAccess(ctx, connection, "", group)
			}
			if err != nil && ((b%8 != 4 && b%8 != 7) || !errors.Is(err, domain.ErrNotFound)) {
				t.Fatalf("valid mixed-access operation %d refused: %v", i, err)
			}
			if err != nil && !reflect.DeepEqual(before, isolationState(t, r)) {
				t.Fatalf("refused operation %d changed state: %v", i, err)
			}
			assertIsolation(t, r)
		}
	})
}
