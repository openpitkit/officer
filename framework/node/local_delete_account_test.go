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

package node

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

type failAccountDeleteRealm struct {
	store.RealmStore
	err error
}

func (r *failAccountDeleteRealm) DeleteAccount(
	context.Context, domain.AccountID, bool,
) error {
	return r.err
}

func TestSnapshotWithoutAccountKeepsUnrelatedAndGroupRuntimeState(t *testing.T) {
	t.Parallel()
	snapshot := snapshotWithoutAccount(engine.Snapshot{
		Accounts: []domain.Account{{Code: "deleted"}, {Code: "retained"}},
		Groups:   []domain.AccountGroup{{Code: "desk"}},
		Balances: []domain.Balance{
			{Account: "deleted", Asset: "USD"},
			{Account: "retained", Asset: "USD"},
		},
		RateLimits: []domain.LimitRate{
			{Scope: domain.ScopeBroker, MaxOrders: 10, Window: time.Minute},
			{Scope: domain.ScopeAccount, Account: "deleted", MaxOrders: 1, Window: time.Minute},
			{Scope: domain.ScopeAccount, Account: "retained", MaxOrders: 2, Window: time.Minute},
		},
		OrderSizeLimits: []domain.LimitOrderSize{
			{Scope: domain.ScopeAccountAsset, Account: "deleted", Asset: "USD", MaxQuantity: "1"},
			{Scope: domain.ScopeAccountAsset, Account: "retained", Asset: "USD", MaxQuantity: "2"},
		},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			{Scope: domain.ScopeAccount, Account: "deleted", Currency: "USD", LowerBound: "-1"},
			{Scope: domain.ScopeAccount, Account: "retained", Currency: "USD", LowerBound: "-2"},
			{Scope: domain.ScopeAccountGroup, AccountGroup: "desk", Currency: "USD", LowerBound: "-3"},
		},
	}, "deleted")

	if len(snapshot.Accounts) != 1 || snapshot.Accounts[0].Code != "retained" ||
		len(snapshot.Groups) != 1 || len(snapshot.Balances) != 1 ||
		snapshot.Balances[0].Account != "retained" || len(snapshot.RateLimits) != 2 ||
		len(snapshot.OrderSizeLimits) != 1 || len(snapshot.SpotFundsPnlBoundsLimits) != 2 {
		t.Fatalf("filtered snapshot = %+v, want only deleted account runtime removed", snapshot)
	}
}

func TestDeleteAccountBuildFailureLeavesStoreAndOldEngineUntouched(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := newFakeEngine()
	n, realm := newTestNode(t, old)
	account, err := n.CreateAccount(ctx, testAccount("retained"), testCaller)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	buildErr := errors.New("account delete build failed")
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, buildErr
	}
	err = n.DeleteAccount(ctx, testKey(account.Code), false, testCaller)
	if !errors.Is(err, buildErr) {
		t.Fatalf("DeleteAccount error = %v, want build failure", err)
	}
	retained, ok, getErr := realm.GetAccount(ctx, account.Code)
	if getErr != nil || !ok || retained.EngineAccountID != account.EngineAccountID {
		t.Fatalf("retained account = %+v, ok=%v err=%v; want %+v",
			retained, ok, getErr, account)
	}
	if n.currentEngine() != old || !old.running {
		t.Fatalf("engine after failed delete: current=%p old running=%v",
			n.currentEngine(), old.running)
	}
	audits, auditErr := realm.ListAudit(ctx, 20)
	if auditErr != nil {
		t.Fatalf("ListAudit: %v", auditErr)
	}
	for _, audit := range audits {
		if audit.Action == domain.AuditActionDeleteAccount {
			t.Fatalf("failed delete wrote delete audit: %+v", audit)
		}
	}
}

func TestDeleteAccountStoreFailureKeepsPreparedEngineUncommitted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	real := newMemoryStore("delete-store-failure.db")
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	deleteErr := domain.NewHasDependentsError([]domain.DependentCount{
		{Kind: "balance", Count: 1},
	})
	wrapped := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failAccountDeleteRealm{RealmStore: r, err: deleteErr}
	})
	old := newFakeEngine()
	n := newTestNodeWithStore(t, wrapped, old)
	account, err := n.CreateAccount(ctx, testAccount("retained"), testCaller)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	next := newFakeEngine()
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)

	err = n.DeleteAccount(ctx, testKey(account.Code), false, testCaller)
	if !errors.Is(err, domain.ErrHasDependents) {
		t.Fatalf("DeleteAccount error = %v, want has-dependents failure", err)
	}
	if _, ok, getErr := realm.GetAccount(ctx, account.Code); getErr != nil || !ok {
		t.Fatalf("account after rejected delete: ok=%v err=%v, want retained", ok, getErr)
	}
	if n.currentEngine() != old || !old.running || next.running {
		t.Fatalf("engine after rejected delete: current=%p old running=%v next running=%v",
			n.currentEngine(), old.running, next.running)
	}
	if len(snapshot.Accounts) != 0 {
		t.Fatalf("prepared snapshot = %+v, want account filtered", snapshot)
	}
}

func TestDeleteAccountAuditFailureFailsStopAfterSuccessfulRebuild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	real := newMemoryStore("delete-audit-fatal.db")
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	realm, err := real.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	auditErr := errors.New("account delete audit failed")
	wrapped := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		return &failActionAuditRealm{
			RealmStore: r,
			action:     domain.AuditActionDeleteAccount,
			err:        auditErr,
		}
	})
	old := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, wrapped, old, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	account, err := n.CreateAccount(ctx, testAccount("deleted"), testCaller)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	next := newFakeEngine()
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)

	err = n.DeleteAccount(ctx, testKey(account.Code), false, testCaller)
	if !errors.Is(err, auditErr) {
		t.Fatalf("DeleteAccount error = %v, want audit failure", err)
	}
	if !errors.Is(fatalErr, auditErr) ||
		!strings.Contains(fatalErr.Error(), `operation="audit delete account"`) ||
		!strings.Contains(fatalErr.Error(), "account=deleted") {
		t.Fatalf("fatal error = %v, want audit operation, account, and cause", fatalErr)
	}
	if _, ok, getErr := realm.GetAccount(ctx, account.Code); getErr != nil || ok {
		t.Fatalf("account after committed delete: ok=%v err=%v, want absent", ok, getErr)
	}
	if n.currentEngine() != next || !next.running || old.running {
		t.Fatalf("engine after committed delete: current=%p next running=%v old running=%v",
			n.currentEngine(), next.running, old.running)
	}
	for _, seeded := range snapshot.Accounts {
		if seeded.Code == account.Code {
			t.Fatalf("rebuilt snapshot retained deleted account: %+v", seeded)
		}
	}
}
