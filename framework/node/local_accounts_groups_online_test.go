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
	"slices"
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

func TestLocalNode_AccountGroupCRUDRebuildsForCascadeDelete(t *testing.T) {
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
		ctx, testKey(createdAccount.Code), true, "online", domain.MissingAccountCreate, testCaller,
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
	next := newFakeEngine()
	next.enforceResolver = true
	next.sink = &identityGateSink{}
	prepareGroupDeleteRebuild(n, probe, next)
	if err := n.DeleteGroup(ctx, updatedGroup.Code, false, testCaller); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}

	detached, ok, err := n.realm.GetAccount(ctx, updatedAccount.Code)
	if err != nil || !ok || detached.GroupCode != "" {
		t.Fatalf("GetAccount after group delete: %+v ok=%v err=%v", detached, ok, err)
	}
	if len(probe.last.Accounts) != 1 || probe.last.Accounts[0].GroupCode != "" || slices.ContainsFunc(
		probe.last.Groups,
		func(group domain.AccountGroup) bool { return group.Code == updatedGroup.Code },
	) {
		t.Fatalf("detach rebuild snapshot = %+v", probe.last)
	}
	if probe.builds != 2 {
		t.Fatalf("engine builds = %d, want initial plus cascade rebuild", probe.builds)
	}
	if eng.running || n.currentEngine() != next || n.CurrentMarketDataSink() != sink {
		t.Fatalf(
			"cascade transition: old-running=%v current-next=%v sink-stable=%v",
			eng.running,
			n.currentEngine() == next,
			n.CurrentMarketDataSink() == sink,
		)
	}
}

func TestLocalNode_AccountRenameKeepsDependentsOnStableEngineIdentity(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	created, err := n.CreateAccount(
		ctx,
		domain.Account{Code: "account-old", Title: "Before"},
		testCaller,
	)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.realm.UpsertBalance(ctx, domain.Balance{
		Account:           created.Code,
		Asset:             "AAPL",
		Available:         "2",
		RealizedPnl:       "3",
		AverageEntryPrice: "10",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	order, err := n.realm.CreateOrder(ctx, domain.Order{
		Account:     created.Code,
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Principal:   testCaller.Principal,
		Source:      testCaller.Source,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       "10",
		Status:      domain.OrderStatusSubmitted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	updated, err := n.UpdateAccount(
		ctx,
		testKey(created.Code),
		domain.Account{Code: "account-new", Title: "After"},
		testCaller,
	)
	if err != nil {
		t.Fatalf("UpdateAccount with dependents: %v", err)
	}
	if updated.EngineAccountID != created.EngineAccountID {
		t.Fatalf(
			"engine account id changed: %d -> %d",
			created.EngineAccountID,
			updated.EngineAccountID,
		)
	}
	if err := n.SetAccountBlocked(
		ctx,
		testKey(updated.Code),
		true,
		"renamed identity",
		domain.MissingAccountReject,
		testCaller,
	); err != nil {
		t.Fatalf("SetAccountBlocked through renamed alias: %v", err)
	}
	balance, ok, err := n.realm.GetBalance(ctx, updated.Code, "AAPL")
	if err != nil || !ok || balance.Available != "2" {
		t.Fatalf("renamed balance = %+v ok %v err %v", balance, ok, err)
	}
	detail, err := n.realm.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder after rename: %v", err)
	}
	if detail.Order.Account != updated.Code {
		t.Fatalf("order account = %q, want %q", detail.Order.Account, updated.Code)
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

func TestLocalNode_ForcedDeleteGroupAuditsDetachedAccounts(t *testing.T) {
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
	// Each member owns its currency, so detaching them changes no effective
	// currency: the currency invariant is not what this cascade is about, and it
	// holds regardless of force.
	for _, code := range []domain.AccountID{"member-a", "member-b"} {
		if _, err := n.CreateAccount(ctx, domain.Account{
			Code: code, GroupCode: "desk", Currency: "USD",
		}, testCaller); err != nil {
			t.Fatalf("CreateAccount(%s): %v", code, err)
		}
	}
	if _, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeAccountGroup, AccountGroup: "desk", Currency: "USD", LowerBound: "-10",
	}, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}
	for _, limit := range []domain.LimitSpotFundsPnlBounds{
		{Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-20"},
		{Scope: domain.ScopeAccount, Account: "member-a", Currency: "USD", UpperBound: "20"},
	} {
		if _, err := n.PutSpotFundsPnlBoundsLimit(
			ctx, limit, domain.MissingAccountCreate, testCaller,
		); err != nil {
			t.Fatalf("PutSpotFundsPnlBoundsLimit(%+v): %v", limit, err)
		}
	}
	eng.configureCalls = nil
	next := newFakeEngine()
	next.enforceResolver = true
	prepareGroupDeleteRebuild(n, probe, next)

	if err := n.DeleteGroup(ctx, "desk", true, testCaller); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if len(probe.last.SpotFundsPnlBoundsLimits) != 2 {
		t.Fatalf(
			"cascade rebuild barriers = %+v, want retained global and account bounds",
			probe.last.SpotFundsPnlBoundsLimits,
		)
	}
	limits, err := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", err)
	}
	retained := map[domain.LimitScope]domain.AccountID{}
	for _, limit := range limits {
		retained[limit.Scope] = limit.Account
	}
	if len(limits) != 2 || len(retained) != 2 ||
		retained[domain.ScopeGlobal] != "" ||
		retained[domain.ScopeAccount] != "member-a" {
		t.Fatalf(
			"stored SpotFunds barriers after delete = %+v, want retained global and account bounds",
			limits,
		)
	}
	auditRows, err := n.realm.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	const wantDetail = "delete group desk; detachedAccounts=2 destroyedSpotFundsPnlBounds=1"
	auditFound := false
	accountAudits := map[domain.AccountID]bool{}
	for _, row := range auditRows {
		if row.Action == domain.AuditActionDeleteGroup && row.Group == "desk" &&
			row.Detail == wantDetail {
			auditFound = true
		}
		if row.Action == domain.AuditActionDeleteGroup && row.Group == "desk" &&
			row.Account != "" {
			accountAudits[row.Account] = true
		}
	}
	if !auditFound || !accountAudits["member-a"] || !accountAudits["member-b"] {
		t.Fatalf(
			"group delete audit = %+v, want summary %q and both account links",
			auditRows,
			wantDetail,
		)
	}
	if probe.builds != 2 || eng.running || n.currentEngine() != next {
		t.Fatalf(
			"cascade engine transition: builds=%d old-running=%v current-next=%v",
			probe.builds, eng.running, n.currentEngine() == next,
		)
	}
}

func TestLocalNode_DeleteGroupBuildFailureLeavesStoreAndEngineUntouched(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, probe := newRebuildProbeNode(t, eng)
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeAccountGroup, AccountGroup: "desk", Currency: "USD", UpperBound: "10",
	}, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}
	buildErr := errors.New("group cascade build failure")
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		probe.builds++
		return nil, buildErr
	}

	err := n.DeleteGroup(ctx, "desk", true, testCaller)
	if !errors.Is(err, buildErr) {
		t.Fatalf("DeleteGroup error = %v, want build failure", err)
	}
	if probe.builds != 2 {
		t.Fatalf("engine builds = %d, want initial plus failed preparation", probe.builds)
	}
	if _, ok, getErr := n.realm.GetGroup(ctx, "desk"); getErr != nil || !ok {
		t.Fatalf("GetGroup after failed delete: ok=%v err=%v, want preserved", ok, getErr)
	}
	limits, listErr := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
	if listErr != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", listErr)
	}
	if len(limits) != 1 {
		t.Fatalf("stored SpotFunds barriers after failed delete = %+v, want preserved", limits)
	}
	if !eng.running || n.currentEngine() != eng {
		t.Fatal("failed cascade preparation replaced or stopped the live engine")
	}
}

func prepareGroupDeleteRebuild(
	n *localNode,
	probe *rebuildProbe,
	next *fakeEngine,
) {
	n.build = func(snapshot engine.Snapshot) (engine.Engine, error) {
		probe.builds++
		probe.last = snapshot
		return fakeBuild(next, &probe.last)(snapshot)
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
	if err := n.SetAccountGroup(ctx, testKey(account.Code), "auto", domain.MissingAccountCreate, testCaller); err != nil {
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
