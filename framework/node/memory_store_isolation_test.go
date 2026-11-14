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

package node

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
)

func newMemoryIsolationRealm(t *testing.T) *memoryRealm {
	t.Helper()
	st := newMemoryStore("isolation.db")
	bindMemoryTradingProviders(t, st, map[string]bool{
		"test": true, "venue": false,
	})
	r := st.realm
	ctx := context.Background()
	for _, provider := range []string{"test", "venue", "unknown"} {
		mode := domain.TradingModePaper
		if provider == "venue" {
			mode = domain.TradingModeLive
		}
		if _, err := r.CreateTradingConnection(ctx, domain.TradingConnection{
			ExternalID: domain.ExternalID(provider), Provider: provider,
			Label: provider, Mode: mode, VenueIdentity: provider, Credentials: `{}`,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, code := range []string{"desk", "other"} {
		if _, err := r.CreateGroup(ctx, domain.AccountGroup{Code: code}); err != nil {
			t.Fatal(err)
		}
	}
	for _, code := range []domain.AccountID{"account", "other"} {
		if _, err := r.CreateAccount(ctx, domain.Account{Code: code}); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func putMemoryIsolationGrant(t *testing.T, r *memoryRealm, grant domain.TradingAccess) {
	t.Helper()
	if err := r.PutTradingAccess(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
}

func assertMemoryIsolationRefusal(t *testing.T, r *memoryRealm, want error, mutate func() error) {
	t.Helper()
	before := fmt.Sprintf("%#v", r)
	err := mutate()
	if !errors.Is(err, want) {
		t.Errorf("mutation error = %v, want %v", err, want)
	}
	if err != nil && fmt.Sprintf("%#v", r) != before {
		t.Error("refused mutation changed the realm")
	}
}

func TestMemoryTradingIsolationAccountGrants(t *testing.T) {
	for _, first := range []domain.ExternalID{"test", "venue"} {
		t.Run(string(first)+" first", func(t *testing.T) {
			r := newMemoryIsolationRealm(t)
			second := domain.ExternalID("test")
			if first == second {
				second = "venue"
			}
			putMemoryIsolationGrant(t, r, domain.TradingAccess{Account: "account", Connection: first})
			assertMemoryIsolationRefusal(t, r, nil, func() error {
				return r.PutTradingAccess(context.Background(), domain.TradingAccess{Account: "account", Connection: second})
			})
		})
	}
}

func TestMemoryTradingIsolationGroupGrant(t *testing.T) {
	for _, own := range []domain.ExternalID{"test", "venue"} {
		t.Run(string(own)+" own", func(t *testing.T) {
			r := newMemoryIsolationRealm(t)
			ctx := context.Background()
			for _, code := range []domain.AccountID{"account", "other"} {
				if err := r.SetAccountGroup(ctx, code, "desk"); err != nil {
					t.Fatal(err)
				}
			}
			group := domain.ExternalID("test")
			if own == group {
				group = "venue"
			}
			putMemoryIsolationGrant(t, r, domain.TradingAccess{Account: "other", Connection: own})
			assertMemoryIsolationRefusal(t, r, nil, func() error {
				return r.PutTradingAccess(ctx, domain.TradingAccess{AccountGroup: "desk", Connection: group})
			})
		})
	}
}

func TestMemoryTradingIsolationSetAccountGroup(t *testing.T) {
	r := newMemoryIsolationRealm(t)
	putMemoryIsolationGrant(t, r, domain.TradingAccess{Account: "account", Connection: "test"})
	putMemoryIsolationGrant(t, r, domain.TradingAccess{AccountGroup: "desk", Connection: "venue"})
	assertMemoryIsolationRefusal(t, r, nil, func() error {
		return r.SetAccountGroup(context.Background(), "account", "desk")
	})
}

func TestMemoryTradingIsolationCreateAccount(t *testing.T) {
	r := newMemoryIsolationRealm(t)
	putMemoryIsolationGrant(t, r, domain.TradingAccess{AccountGroup: "desk", Connection: "test"})
	putMemoryIsolationGrant(t, r, domain.TradingAccess{AccountGroup: "desk", Connection: "venue"})
	assertMemoryIsolationRefusal(t, r, nil, func() error {
		_, err := r.CreateAccount(context.Background(), domain.Account{Code: "created", GroupCode: "desk"})
		return err
	})
}

func TestMemoryTradingIsolationRestoreMembership(t *testing.T) {
	for _, mode := range []backup.RestoreMode{backup.RestoreModeReplaceAll, backup.RestoreModeOverwrite, backup.RestoreModeInsertMissing} {
		t.Run(string(mode), func(t *testing.T) {
			r := newMemoryIsolationRealm(t)
			putMemoryIsolationGrant(t, r, domain.TradingAccess{Account: "account", Connection: "test"})
			putMemoryIsolationGrant(t, r, domain.TradingAccess{AccountGroup: "desk", Connection: "venue"})
			archive := backup.Archive{CredentialForm: backup.CredentialFormPlaintext, Data: backup.Data{
				Groups:   []backup.AccountGroup{{Code: "desk"}},
				Accounts: []backup.Account{{Code: "account", GroupCode: "desk", Pnl: "0"}},
				Assets:   []backup.Asset{{Code: "SHOULD-NOT-LAND"}},
			}}
			assertMemoryIsolationRefusal(t, r, nil, func() error {
				_, err := r.RestoreBackup(context.Background(), archive, backup.RestoreOptions{
					Scope: backup.Scope{Sections: []backup.Section{backup.SectionAccountsGroups}}, Mode: mode,
				})
				return err
			})
		})
	}
}

func TestMemoryTradingIsolationUsesModeWithoutProviderMetadata(t *testing.T) {
	ctx := context.Background()
	for _, unbound := range []bool{false, true} {
		for _, group := range []bool{false, true} {
			t.Run(fmt.Sprintf("unbound=%v/group=%v", unbound, group), func(t *testing.T) {
				r := newMemoryIsolationRealm(t)
				if unbound {
					r.store = newMemoryStore("unbound.db")
				}
				grant := domain.TradingAccess{Connection: "unknown", Account: "account"}
				if group {
					grant.Account, grant.AccountGroup = "", "desk"
				}
				// Isolation uses the stored mode even when provider metadata is unavailable.
				putMemoryIsolationGrant(t, r, grant)
				if group {
					if err := r.SetAccountGroup(ctx, "account", "desk"); err != nil {
						t.Fatal(err)
					}
				}
				assertMemoryIsolationRefusal(t, r, nil, func() error {
					return r.PutTradingAccess(ctx, domain.TradingAccess{Account: "account", Connection: "venue"})
				})
			})
		}
	}
}
