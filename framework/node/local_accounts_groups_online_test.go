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
	"strings"
	"testing"

	"go.openpit.dev/openpit/asyncengine"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

type failSelectedAccountRereadsRealm struct {
	store.RealmStore
	calls     int
	failCalls map[int]error
}

type failSelectedGroupRereadsRealm struct {
	store.RealmStore
	calls     int
	failCalls map[int]error
}

func TestLocalNode_GroupCreateEmergencyRebuildFailureIsFatal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	stopFakeAdministrativeDriver(t, eng)
	rebuildCause := errors.New("group reconciliation rebuild failed")
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, errors.Join(domain.ErrInvalid, rebuildCause)
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	_, err := n.CreateGroup(ctx, domain.AccountGroup{
		Code:     "desk",
		Currency: "USD",
	}, testCaller)
	if err == nil {
		t.Fatal("CreateGroup succeeded, want stopped-driver failure")
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateGroup error = %v, must hide domain sentinel", err)
	}
	if !errors.Is(err, asyncengine.ErrChainRetryUnsafe) ||
		!errors.Is(err, rebuildCause) {
		t.Fatalf(
			"CreateGroup error = %v, want retry-unsafe mutation and rebuild failure",
			err,
		)
	}
	if !strings.Contains(err.Error(), "apply created group runtime state") ||
		!strings.Contains(err.Error(), "rebuild engine from current store") {
		t.Fatalf("CreateGroup error = %v, want mutation and rebuild layers", err)
	}
	if fatalErr == nil ||
		!errors.Is(fatalErr, domain.ErrInvalid) ||
		!errors.Is(fatalErr, asyncengine.ErrChainRetryUnsafe) ||
		!errors.Is(fatalErr, rebuildCause) {
		t.Fatalf(
			"fatal error = %v, want sentinel, retry-unsafe mutation, and rebuild failure",
			fatalErr,
		)
	}
}

func (r *failSelectedAccountRereadsRealm) GetAccount(
	ctx context.Context, account domain.AccountID,
) (domain.Account, bool, error) {
	r.calls++
	if err := r.failCalls[r.calls]; err != nil {
		return domain.Account{}, false, err
	}
	return r.RealmStore.GetAccount(ctx, account)
}

func (r *failSelectedGroupRereadsRealm) GetGroup(
	ctx context.Context,
	group string,
) (domain.AccountGroup, bool, error) {
	r.calls++
	if err := r.failCalls[r.calls]; err != nil {
		return domain.AccountGroup{}, false, err
	}
	return r.RealmStore.GetGroup(ctx, group)
}

func TestLocalNode_CreateAccountWithoutRuntimeDoesNotReread(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	cause := errors.New("unexpected account reread")
	n.realm = &failSelectedAccountRereadsRealm{
		RealmStore: n.realm,
		failCalls:  map[int]error{1: cause},
	}

	created, err := n.CreateAccount(
		ctx,
		domain.Account{Code: "plain"},
		testCaller,
	)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if created.Code != "plain" {
		t.Fatalf("created account = %+v, want plain", created)
	}
}

func TestLocalNode_CreateAccountPreEngineReadFailureSkipsRebuild(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	n, probe := newRebuildProbeNode(t, eng)
	buildsBefore := probe.builds
	cause := errors.New("account reread failed")
	n.realm = &failSelectedAccountRereadsRealm{
		RealmStore: n.realm,
		failCalls: map[int]error{
			1: errors.Join(domain.ErrInvalid, cause),
		},
	}

	_, err := n.CreateAccount(ctx, domain.Account{
		Code:        "blocked",
		Blocked:     true,
		BlockReason: "risk",
	}, testCaller)
	if !errors.Is(err, cause) || !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateAccount error = %v, want original classified read failure", err)
	}
	if errors.Is(err, asyncengine.ErrChainRetryUnsafe) {
		t.Fatalf("CreateAccount error = %v, must not be retry-unsafe", err)
	}
	if probe.builds != buildsBefore {
		t.Fatalf("engine builds = %d, want no rebuild", probe.builds-buildsBefore)
	}
}

func TestLocalNode_CreateGroupPreEngineReadFailureSkipsRebuild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	n, probe := newRebuildProbeNode(t, eng)
	buildsBefore := probe.builds
	cause := errors.New("group reread failed")
	n.realm = &failSelectedGroupRereadsRealm{
		RealmStore: n.realm,
		failCalls: map[int]error{
			1: errors.Join(domain.ErrInvalid, cause),
		},
	}

	_, err := n.CreateGroup(ctx, domain.AccountGroup{
		Code:        "blocked",
		Blocked:     true,
		BlockReason: "risk",
	}, testCaller)
	if !errors.Is(err, cause) || !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateGroup error = %v, want original classified read failure", err)
	}
	if errors.Is(err, asyncengine.ErrChainRetryUnsafe) {
		t.Fatalf("CreateGroup error = %v, must not be retry-unsafe", err)
	}
	if probe.builds != buildsBefore {
		t.Fatalf("engine builds = %d, want no rebuild", probe.builds-buildsBefore)
	}
	if _, ok, getErr := n.realm.GetGroup(ctx, "blocked"); getErr != nil || ok {
		t.Fatalf("compensated group = ok %v error %v, want absent", ok, getErr)
	}
	if _, ok := eng.knownGroups["blocked"]; ok {
		t.Fatal("compensated group resolver entry remains published")
	}
}

func TestLocalNode_GroupCurrencyChainUpdatesWithoutRebuild(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
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
		ctx,
		domain.DefaultRealm, st, func(snap engine.Snapshot) (engine.Engine, error) {
			builds++
			return inner(snap)
		},
		failOnFatal(t),
	)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nodeRaw.(*localNode)
	seedTestPrincipal(t, n)

	if _, err := n.CreateGroup(
		ctx, domain.AccountGroup{Code: "desk"}, testCaller,
	); err != nil {
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
	if err := n.SetGroupCurrency(ctx, "desk", "USD", testCaller); err != nil {
		t.Fatalf("SetGroupCurrency update: %v", err)
	}
	group, ok, err := n.realm.GetGroup(ctx, "desk")
	if err != nil || !ok {
		t.Fatalf("GetGroup: ok=%v err=%v", ok, err)
	}
	if group.Currency != "USD" {
		t.Fatalf("stored group currency = %q, want USD", group.Currency)
	}
	if builds != 1 {
		t.Fatalf("engine builds = %d, want initial build only", builds)
	}
}

func TestLocalNode_GroupBlockChainUpdatesWithoutRebuild(t *testing.T) {
	t.Parallel()
	fake := newFakeEngine()
	st := newMemoryStore("group-block-online.db")
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var seed engine.Snapshot
	baseBuild := fakeBuild(fake, &seed)
	nodeRaw, _, err := NewLocalNode(
		ctx,
		domain.DefaultRealm,
		st,
		func(snapshot engine.Snapshot) (engine.Engine, error) {
			return baseBuild(snapshot)
		},
		failOnFatal(t),
	)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nodeRaw.(*localNode)
	seedTestPrincipal(t, n)
	if _, err := n.CreateGroup(
		ctx, domain.AccountGroup{Code: "desk"}, testCaller,
	); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	if err := n.SetGroupBlocked(ctx, "desk", true, "risk", testCaller); err != nil {
		t.Fatalf("SetGroupBlocked: %v", err)
	}
	group, ok, getErr := n.realm.GetGroup(ctx, "desk")
	if getErr != nil || !ok || !group.Blocked {
		t.Fatalf(
			"stored group = %+v ok=%v err=%v, want blocked",
			group, ok, getErr,
		)
	}
}

func TestLocalNode_AccountRenameFailureCompensatesWithoutRebuild(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(
		ctx, domain.Account{Code: "account-old"}, testCaller,
	); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	eng.resolverMu.Lock()
	eng.knownAccounts["account-new"] = struct{}{}
	eng.resolverMu.Unlock()

	_, err := n.UpdateAccount(
		ctx,
		"account-old",
		domain.Account{Code: "account-new"},
		testCaller,
	)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("UpdateAccount error = %v, want ErrAlreadyExists", err)
	}
	if _, ok, getErr := st.GetAccount(ctx, "account-old"); getErr != nil || !ok {
		t.Fatalf("old account ok=%v err=%v, want restored", ok, getErr)
	}
	if _, ok, getErr := st.GetAccount(ctx, "account-new"); getErr != nil || ok {
		t.Fatalf("new account ok=%v err=%v, want rolled back", ok, getErr)
	}
}

func TestLocalNode_GroupRenameFailureCompensatesWithoutRebuild(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateGroup(
		ctx, domain.AccountGroup{Code: "desk-old"}, testCaller,
	); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	eng.resolverMu.Lock()
	eng.knownGroups["desk-new"] = struct{}{}
	eng.resolverMu.Unlock()

	_, err := n.UpdateGroup(
		ctx,
		"desk-old",
		domain.AccountGroup{Code: "desk-new"},
		testCaller,
	)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("UpdateGroup error = %v, want ErrAlreadyExists", err)
	}
	if _, ok, getErr := st.GetGroup(ctx, "desk-old"); getErr != nil || !ok {
		t.Fatalf("old group ok=%v err=%v, want restored", ok, getErr)
	}
	if _, ok, getErr := st.GetGroup(ctx, "desk-new"); getErr != nil || ok {
		t.Fatalf("new group ok=%v err=%v, want rolled back", ok, getErr)
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
	if createdAccount.Currency != "" {
		t.Fatal("created account inherited currency was materialized as an override")
	}
	assertFakeEffectiveCurrency(t, eng, createdAccount.Code, "USD")
	if err := n.SetAccountBlocked(
		ctx, createdAccount.Code, true, "online", domain.MissingAccountCreate, testCaller,
	); err != nil {
		t.Fatalf("new account lane: %v", err)
	}

	updatedAccount, err := n.UpdateAccount(
		ctx,
		createdAccount.Code,
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
		created.Code,
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
		updated.Code,
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

func TestLocalNode_SetAccountGroupRegisterFailureRestoresPreviousGroup(
	t *testing.T,
) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newRebuildProbeNode(t, eng)
	ctx := context.Background()

	for _, code := range []string{"desk-old", "desk-new"} {
		if _, err := n.CreateGroup(
			ctx, domain.AccountGroup{Code: code}, testCaller,
		); err != nil {
			t.Fatalf("CreateGroup(%s): %v", code, err)
		}
	}
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "account", GroupCode: "desk-old",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cause := errors.New("register chain account reread failed")
	realm := n.realm
	n.realm = &failSelectedAccountRereadsRealm{
		RealmStore: realm,
		failCalls:  map[int]error{4: cause},
	}

	err := n.SetAccountGroup(
		ctx,
		"account",
		"desk-new",
		domain.MissingAccountReject,
		testCaller,
	)
	if !errors.Is(err, cause) {
		t.Fatalf("SetAccountGroup error = %v, want reread cause", err)
	}
	if isInternalPostCommitNodeMutation(err) {
		t.Fatalf("SetAccountGroup error = %v, want classified compensation", err)
	}
	if probe.builds != 1 {
		t.Fatalf("engine builds = %d, want no reconciliation", probe.builds)
	}
	stored, ok, getErr := realm.GetAccount(ctx, "account")
	if getErr != nil || !ok || stored.GroupCode != "desk-old" {
		t.Fatalf(
			"stored account = %+v ok=%v err=%v, want desk-old",
			stored,
			ok,
			getErr,
		)
	}
	assertFakeAccountGroup(t, eng, "account", "desk-old")
}

func TestLocalNode_SetAccountGroupRegisterAndCompensationFailureRebuilds(
	t *testing.T,
) {
	t.Parallel()
	eng := newFakeEngine()
	n, probe := newRebuildProbeNode(t, eng)
	ctx := context.Background()

	for _, code := range []string{"desk-old", "desk-new"} {
		if _, err := n.CreateGroup(
			ctx, domain.AccountGroup{Code: code}, testCaller,
		); err != nil {
			t.Fatalf("CreateGroup(%s): %v", code, err)
		}
	}
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "account", GroupCode: "desk-old",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	registerCause := errors.New("register chain account reread failed")
	compensationCause := errors.New("compensation account reread failed")
	realm := n.realm
	n.realm = &failSelectedAccountRereadsRealm{
		RealmStore: realm,
		failCalls: map[int]error{
			4: registerCause,
			5: compensationCause,
		},
	}
	next := newFakeEngine()
	var rebuilt engine.Snapshot
	n.build = func(snapshot engine.Snapshot) (engine.Engine, error) {
		probe.builds++
		probe.last = snapshot
		return fakeBuild(next, &rebuilt)(snapshot)
	}

	err := n.SetAccountGroup(
		ctx,
		"account",
		"desk-new",
		domain.MissingAccountReject,
		testCaller,
	)
	if !errors.Is(err, registerCause) || !errors.Is(err, compensationCause) {
		t.Fatalf(
			"SetAccountGroup error = %v, want register and compensation causes",
			err,
		)
	}
	if !isInternalPostCommitNodeMutation(err) {
		t.Fatalf("SetAccountGroup error = %v, want terminal classification", err)
	}
	if probe.builds != 2 || n.currentEngine() != next {
		t.Fatalf(
			"reconciliation = builds %d current %p, want 2 and %p",
			probe.builds,
			n.currentEngine(),
			next,
		)
	}
	stored, ok, getErr := realm.GetAccount(ctx, "account")
	if getErr != nil || !ok || stored.GroupCode != "desk-old" {
		t.Fatalf(
			"stored account = %+v ok=%v err=%v, want desk-old",
			stored,
			ok,
			getErr,
		)
	}
	assertFakeAccountGroup(t, next, "account", "desk-old")
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
		ctx, account.Code, "GBP", testCaller,
	); err != nil {
		t.Fatalf("SetAccountCurrency(GBP): %v", err)
	}
	stored, _, err := n.GetAccountState(ctx, account.Code)
	if err != nil || stored.EffectiveCurrency != "GBP" {
		t.Fatalf("account with override = %+v err=%v, want effective GBP", stored, err)
	}
	assertFakeEffectiveCurrency(t, eng, account.Code, "GBP")
	if err := n.SetAccountCurrency(
		ctx, account.Code, "", testCaller,
	); err != nil {
		t.Fatalf("ClearAccountCurrency: %v", err)
	}
	stored, _, err = n.GetAccountState(ctx, account.Code)
	if err != nil || stored.Currency != "" || stored.EffectiveCurrency != "EUR" {
		t.Fatalf("account after clear = %+v err=%v, want inherited EUR", stored, err)
	}
	assertFakeEffectiveCurrency(t, eng, account.Code, "EUR")
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
	if err := n.SetAccountGroup(ctx, account.Code, "auto", domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("SetAccountGroup auto-create: %v", err)
	}
	if err := n.SetGroupNotes(ctx, "notes-auto", "online", testCaller); err != nil {
		t.Fatalf("SetGroupNotes auto-create: %v", err)
	}
	if err := n.SetGroupBlocked(ctx, "block-auto", true, "risk", testCaller); err != nil {
		t.Fatalf("SetGroupBlocked auto-create: %v", err)
	}
	for _, code := range []string{"auto", "notes-auto", "block-auto"} {
		if _, err := eng.ResolveGroup(code); err != nil {
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
	if err := n.DeleteAccount(ctx, account.Code, true, testCaller); err != nil {
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
