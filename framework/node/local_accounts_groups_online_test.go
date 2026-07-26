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
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

type rejectAccountRereadRealm struct {
	store.RealmStore
}

func (r *rejectAccountRereadRealm) GetAccount(
	context.Context, domain.AccountID,
) (domain.Account, bool, error) {
	return domain.Account{}, false, errors.New("unexpected account reread")
}

func TestLocalNode_GroupCreateEmergencyRebuildFailureIsFatal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	eng.groupCurrencyErr = errors.New("group currency mutation failed")
	n, _ := newTestNode(t, eng)
	rebuildErr := errors.New("group reconciliation rebuild failed")
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, rebuildErr
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	_, err := n.CreateGroup(ctx, domain.AccountGroup{
		Code:     "desk",
		Currency: "USD",
	}, testCaller)
	if !errors.Is(err, rebuildErr) {
		t.Fatalf("CreateGroup error = %v, want rebuild failure", err)
	}
	if fatalErr == nil || !errors.Is(fatalErr, rebuildErr) {
		t.Fatalf("fatal error = %v, want rebuild failure", fatalErr)
	}
}

func TestLocalNode_GroupCurrencyFailureCompensatesWithoutRebuild(t *testing.T) {
	t.Parallel()
	applyErr := errors.New("set group currency failed")
	eng := newFakeEngine()
	eng.groupCurrencyFails = map[int]error{2: applyErr}
	st := newMemoryStore("group-currency-online.db")
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	builds := 0
	var captured engine.Snapshot
	inner := fakeBuild(eng, &captured)
	nodeRaw, _, err := NewLocalNode(
		ctx, st, func(snap engine.Snapshot) (engine.Engine, error) {
			builds++
			return inner(snap)
		},
	)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nodeRaw.(*localNode)
	seedTestPrincipal(t, n.realm)

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "account", GroupCode: "desk",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetGroupCurrency(ctx, "desk", "EUR", testCaller); err != nil {
		t.Fatalf("SetGroupCurrency initial: %v", err)
	}
	if err := n.SetGroupCurrency(ctx, "desk", "USD", testCaller); !errors.Is(err, applyErr) {
		t.Fatalf("SetGroupCurrency error = %v, want apply failure", err)
	}
	group, ok, err := n.realm.GetGroup(ctx, "desk")
	if err != nil || !ok {
		t.Fatalf("GetGroup: ok=%v err=%v", ok, err)
	}
	if group.Currency != "EUR" {
		t.Fatalf("stored group currency = %q, want compensated EUR", group.Currency)
	}
	if got := eng.groupCurrencies["desk"]; got != "EUR" {
		t.Fatalf("live group currency = %q, want compensated EUR", got)
	}
	if builds != 1 {
		t.Fatalf("engine builds = %d, want initial build only", builds)
	}
}

func TestLocalNode_AccountGroupCurrencyCRUDStaysOnline(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	eng.sink = &identityGateSink{}
	n, probe := newRebuildProbeNode(t, eng)
	ctx := context.Background()
	sink := n.CurrentMarketDataSink()

	createdGroup, err := n.CreateGroup(ctx, domain.AccountGroup{
		Code:     "desk-old",
		Currency: "USD",
	}, testCaller)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	createdAccount, err := n.CreateAccount(ctx, domain.Account{
		Code:      "account-old",
		GroupCode: createdGroup.Code,
	}, testCaller)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, ok := eng.accountCurrencies[createdAccount.Code]; ok {
		t.Fatal("created account inherited currency was materialized as an override")
	}
	if got := eng.effectiveAccountCurrency(createdAccount.Code); got != "USD" {
		t.Fatalf("created account effective currency = %q, want inherited USD", got)
	}
	if err := n.SetAccountBlocked(
		ctx, testKey(createdAccount.Code), true, "online", testCaller,
	); err != nil {
		t.Fatalf("new account lane: %v", err)
	}

	updatedAccount, err := n.UpdateAccount(
		ctx,
		testKey(createdAccount.Code),
		domain.Account{Code: "account-new", Title: "renamed"},
		testCaller,
	)
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	if updatedAccount.EngineAccountID != createdAccount.EngineAccountID {
		t.Fatalf(
			"account engine id changed: %d -> %d",
			createdAccount.EngineAccountID,
			updatedAccount.EngineAccountID,
		)
	}
	updatedGroup, err := n.UpdateGroup(
		ctx,
		createdGroup.Code,
		domain.AccountGroup{Code: "desk-new", Title: "renamed"},
		testCaller,
	)
	if err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	if updatedGroup.EngineGroupID != createdGroup.EngineGroupID {
		t.Fatalf(
			"group engine id changed: %d -> %d",
			createdGroup.EngineGroupID,
			updatedGroup.EngineGroupID,
		)
	}
	if err := n.SetGroupBlocked(
		ctx, updatedGroup.Code, true, "risk", testCaller,
	); err != nil {
		t.Fatalf("SetGroupBlocked: %v", err)
	}
	if err := n.SetGroupCurrency(ctx, updatedGroup.Code, "EUR", testCaller); err != nil {
		t.Fatalf("SetGroupCurrency: %v", err)
	}
	if err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller); err != nil {
		t.Fatalf("SetDefaultGroupCurrency: %v", err)
	}
	if err := n.DeleteGroup(ctx, updatedGroup.Code, testCaller); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}

	account, ok, err := n.realm.GetAccount(ctx, updatedAccount.Code)
	if err != nil || !ok {
		t.Fatalf("GetAccount after group delete: ok=%v err=%v", ok, err)
	}
	if account.GroupCode != "" || account.EffectiveCurrency != "USD" {
		t.Fatalf("account after group delete = %+v, want ungrouped USD", account)
	}
	if _, ok := eng.accountCurrencies[updatedAccount.Code]; ok {
		t.Fatal("group deletion materialized default currency as an account override")
	}
	if got := eng.effectiveAccountCurrency(updatedAccount.Code); got != "USD" {
		t.Fatalf("live effective currency = %q, want inherited USD", got)
	}
	if probe.builds != 1 {
		t.Fatalf("engine builds = %d, want initial build only", probe.builds)
	}
	if got := n.CurrentMarketDataSink(); got != sink {
		t.Fatal("online account/group CRUD replaced market-data sink")
	}
}

func TestLocalNode_CreateAccountUsesStoredCreateResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, realm := newTestNode(t, eng)
	n.realm = &rejectAccountRereadRealm{RealmStore: realm}

	created, err := n.CreateAccount(ctx, domain.Account{Code: "created"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if created.EngineAccountID == 0 {
		t.Fatalf("created account = %+v, want assigned engine id", created)
	}
	stored, ok, err := realm.GetAccount(ctx, created.Code)
	if err != nil || !ok || stored.EngineAccountID != created.EngineAccountID {
		t.Fatalf("stored account = %+v ok=%v err=%v, want create result", stored, ok, err)
	}
}

func TestLocalNode_ClearAccountCurrencyRevealsCurrentGroupCurrency(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, probe := newRebuildProbeNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{
		Code: "desk", Currency: "USD",
	}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	account, err := n.CreateAccount(ctx, domain.Account{
		Code: "account", GroupCode: "desk",
	}, testCaller)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetGroupCurrency(ctx, "desk", "EUR", testCaller); err != nil {
		t.Fatalf("SetGroupCurrency(EUR): %v", err)
	}
	if err := n.SetAccountCurrency(
		ctx, testKey(account.Code), "GBP", testCaller,
	); err != nil {
		t.Fatalf("SetAccountCurrency(GBP): %v", err)
	}
	if got := eng.effectiveAccountCurrency(account.Code); got != "GBP" {
		t.Fatalf("effective currency with override = %q, want GBP", got)
	}
	if err := n.SetAccountCurrency(
		ctx, testKey(account.Code), "", testCaller,
	); err != nil {
		t.Fatalf("ClearAccountCurrency: %v", err)
	}
	if got := eng.effectiveAccountCurrency(account.Code); got != "EUR" {
		t.Fatalf("effective currency after clear = %q, want inherited EUR", got)
	}
	if _, ok := eng.accountCurrencies[account.Code]; ok {
		t.Fatal("cleared account currency override remains in live engine")
	}
	if probe.builds != 1 {
		t.Fatalf("engine builds = %d, want initial build only", probe.builds)
	}
}

func TestLocalNode_DeleteGroupClearsCascadedSpotFundsBarrierOnline(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, probe := newRebuildProbeNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{
		Code: "desk", Currency: "USD",
	}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeAccountGroup, AccountGroup: "desk", LowerBound: "-10",
	}, testCaller); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}
	eng.configureCalls = nil

	if err := n.DeleteGroup(ctx, "desk", testCaller); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if len(eng.configureCalls) != 1 {
		t.Fatalf("policy configure calls = %+v, want one post-delete refresh", eng.configureCalls)
	}
	call := eng.configureCalls[0]
	if call.policy != domain.PolicySpotFundsPnlBoundsKillSwitch ||
		len(call.limits.SpotFundsPnlBoundsLimits) != 0 {
		t.Fatalf("post-delete policy configure = %+v, want empty SpotFunds barriers", call)
	}
	limits, err := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", err)
	}
	if len(limits) != 0 {
		t.Fatalf("stored SpotFunds barriers after delete = %+v, want none", limits)
	}
	if probe.builds != 1 {
		t.Fatalf("engine builds = %d, want initial build only", probe.builds)
	}
}

func TestLocalNode_DeleteGroupPolicyFailureRebuildsPostDeleteStore(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, probe := newRebuildProbeNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeAccountGroup, AccountGroup: "desk", UpperBound: "10",
	}, testCaller); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}
	configureErr := errors.New("partial configure failure")
	eng.configureErr = configureErr

	err := n.DeleteGroup(ctx, "desk", testCaller)
	if !errors.Is(err, configureErr) {
		t.Fatalf("DeleteGroup error = %v, want configure failure", err)
	}
	if probe.builds != 2 {
		t.Fatalf("engine builds = %d, want initial plus reconciliation", probe.builds)
	}
	if _, ok, getErr := n.realm.GetGroup(ctx, "desk"); getErr != nil || ok {
		t.Fatalf("GetGroup after reconciled delete: ok=%v err=%v, want absent", ok, getErr)
	}
	limits, listErr := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
	if listErr != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", listErr)
	}
	if len(limits) != 0 {
		t.Fatalf("stored SpotFunds barriers after reconciled delete = %+v, want none", limits)
	}
}

func TestLocalNode_ImplicitGroupPublicationStaysOnline(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, probe := newRebuildProbeNode(t, eng)
	ctx := context.Background()

	account, err := n.CreateAccount(ctx, domain.Account{Code: "account"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetAccountGroup(ctx, testKey(account.Code), "auto", testCaller); err != nil {
		t.Fatalf("SetAccountGroup auto-create: %v", err)
	}
	if err := n.SetGroupNotes(ctx, "notes-auto", "online", testCaller); err != nil {
		t.Fatalf("SetGroupNotes auto-create: %v", err)
	}
	if err := n.SetGroupBlocked(ctx, "block-auto", true, "risk", testCaller); err != nil {
		t.Fatalf("SetGroupBlocked auto-create: %v", err)
	}
	for _, code := range []string{"auto", "notes-auto", "block-auto"} {
		if err := eng.RunGroupSynchronized(
			ctx, code, func(engine.GroupLane) error { return nil },
		); err != nil {
			t.Fatalf("live resolver group %s: %v", code, err)
		}
	}
	if probe.builds != 1 {
		t.Fatalf("engine builds = %d, want initial build only", probe.builds)
	}
}

func TestLocalNode_DeleteAccountStillRebuilds(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newRebuildProbeNode(t, eng)
	ctx := context.Background()

	account, err := n.CreateAccount(ctx, domain.Account{Code: "retire-me"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if probe.builds != 1 {
		t.Fatalf("builds after create = %d, want 1", probe.builds)
	}
	next := newFakeEngine()
	var snapshot engine.Snapshot
	n.build = func(seed engine.Snapshot) (engine.Engine, error) {
		probe.builds++
		probe.last = seed
		return fakeBuild(next, &snapshot)(seed)
	}
	if err := n.DeleteAccount(ctx, testKey(account.Code), true, testCaller); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if probe.builds != 2 {
		t.Fatalf("builds after delete = %d, want 2", probe.builds)
	}
	if n.currentEngine() != next || !next.running || eng.running {
		t.Fatalf("delete engine swap: current=%p next running=%v old running=%v",
			n.currentEngine(), next.running, eng.running)
	}
}
