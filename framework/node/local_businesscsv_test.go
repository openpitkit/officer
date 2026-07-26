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
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

func TestUpsertMarketDataInstrumentCreatesAssets(t *testing.T) {
	t.Parallel()
	n, realm := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	instance, err := realm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBinance,
		Label:    "Binance",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}

	instrument := domain.MarketDataInstrument{
		Instance:       instance.ExternalID,
		ExternalSymbol: "BTCUSDT",
		BaseAsset:      "BTC",
		QuoteAsset:     "USDT",
		Enabled:        true,
	}
	if err := n.UpsertMarketDataInstrument(ctx, instrument, testCaller); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	for _, code := range []string{"BTC", "USDT"} {
		asset, ok, err := realm.GetAsset(ctx, code)
		if err != nil || !ok {
			t.Fatalf("GetAsset(%s) = ok %v, err %v; want created asset", code, ok, err)
		}
		if asset.AssetClass != autoCreatedAssetClassCode {
			t.Fatalf("asset %s class = %q, want %q", code, asset.AssetClass,
				autoCreatedAssetClassCode)
		}
	}
	if class, ok, err := realm.GetAssetClass(ctx, autoCreatedAssetClassCode); err != nil || !ok {
		t.Fatalf("GetAssetClass(%s) = ok %v, err %v; want created class",
			autoCreatedAssetClassCode, ok, err)
	} else if class.Title != autoCreatedAssetClassTitle {
		t.Fatalf("auto-created class title = %q, want %q", class.Title,
			autoCreatedAssetClassTitle)
	} else if class.Notes != autoCreatedAssetClassNotes {
		t.Fatalf("auto-created class notes = %q, want %q", class.Notes,
			autoCreatedAssetClassNotes)
	}
	instruments, err := realm.ListMarketDataInstruments(ctx, instance.ExternalID)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments: %v", err)
	}
	if len(instruments) != 1 || instruments[0].ExternalSymbol != "BTCUSDT" {
		t.Fatalf("instruments = %+v, want BTCUSDT", instruments)
	}
	rows, err := realm.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAsset},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create asset): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("create-asset audit rows = %+v, want BTC and USDT", rows)
	}
	for _, code := range []string{"BTC", "USDT"} {
		found := false
		for _, row := range rows {
			if row.Asset == code && strings.Contains(row.Detail,
				"by market-data instrument upsert") {
				found = true
			}
		}
		if !found {
			t.Fatalf("create-asset audit rows = %+v, want %s auto-created detail",
				rows, code)
		}
	}
}

// testCaller is the attribution the node tests stamp on mutations.
var testCaller = domain.Caller{Source: domain.SourceAPI, Principal: domain.PrincipalOperator}

// rateLimit builds a broker-scope rate-limit barrier with the given count/window
// for the tests that exercise the limit paths.
func rateLimit(scope domain.LimitScope, account domain.AccountID, asset string, max uint64, window time.Duration) domain.LimitRate {
	return domain.LimitRate{
		Scope:     scope,
		Account:   account,
		Asset:     asset,
		MaxOrders: max,
		Window:    window,
	}
}

func TestApplyBusinessCSVImport_BatchesAdjustmentsByAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{Code: "acc-1"},
		}},
		Balances: []domain.Balance{
			{Account: "acc-1", Asset: "USD", Available: "10"},
			{Account: "acc-1", Asset: "EUR", Available: "20"},
		},
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}
	if len(eng.adjustmentBatchCalls) != 1 {
		t.Fatalf("adjustment batch calls = %+v, want one", eng.adjustmentBatchCalls)
	}
	call := eng.adjustmentBatchCalls[0]
	if call.account != "acc-1" || len(call.reqs) != 2 ||
		call.reqs[0].Asset != "USD" || call.reqs[1].Asset != "EUR" {
		t.Fatalf("adjustment batch call = %+v", call)
	}
	// The node applies both position snapshots as one per-account engine batch
	// (covered above) and hands the resulting adjustment records to the store as
	// part of the import. The store persists those adjustments transactionally
	// alongside the import's account/balances, so both snapshots leave a durable
	// adjustment row keyed to acc-1, newest first by asset.
	if _, ok, err := st.GetAccount(ctx, "acc-1"); err != nil || !ok {
		t.Fatalf("GetAccount after import: ok=%v err=%v, want present", ok, err)
	}
	adj, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(adj) != 2 {
		t.Fatalf("persisted adjustments = %d, want 2 (one per imported position)", len(adj))
	}
	gotAssets := []string{adj[0].Asset, adj[1].Asset}
	wantAssets := []string{"EUR", "USD"} // newest first; EUR was applied last.
	if !slices.Equal(gotAssets, wantAssets) {
		t.Fatalf("persisted adjustment assets = %v, want %v", gotAssets, wantAssets)
	}
	for _, rec := range adj {
		if rec.Accepted == nil || rec.Rejected != nil {
			t.Fatalf("adjustment %s outcome = %+v, want accepted", rec.Asset, rec)
		}
		if rec.ExternalID.IsZero() {
			t.Fatalf("adjustment %s has no external id", rec.Asset)
		}
	}
}

func TestSnapshotAdjustmentRequestReplaysAuthoritativePnlState(t *testing.T) {
	t.Parallel()
	numeric := snapshotAdjustmentRequest(domain.Balance{
		Asset: "USD", RealizedPnl: "12.5",
	})
	if numeric.RealizedPnl != "12.5" || numeric.RealizedPnlHaltReason != "" {
		t.Fatalf("numeric request = %+v, want realized P&L 12.5", numeric)
	}
	halted := snapshotAdjustmentRequest(domain.Balance{
		Asset: "USD", RealizedPnl: "12.5",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
	})
	if halted.RealizedPnl != "" ||
		halted.RealizedPnlHaltReason != domain.PnlHaltReasonMissingFx {
		t.Fatalf("halted request = %+v, want halt-over-value replay", halted)
	}
}

func TestApplyBusinessCSVImport_AppliesSparseAdjustmentBatch(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentBatchResults = []engine.AdjustmentResult{{
		Accepted: &domain.AdjustmentOutcomeAccepted{
			BalanceResult: "10",
		},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{Code: "acc-1"},
		}},
		Balances: []domain.Balance{
			{Account: "acc-1", Asset: "USD", Available: "10"},
			{Account: "acc-1", Asset: "EUR", Available: "20"},
		},
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}

	adj, err := st.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	if len(adj) != 1 {
		t.Fatalf("persisted adjustments = %d, want only returned outcome", len(adj))
	}
	if adj[0].Asset != "USD" || adj[0].Accepted == nil {
		t.Fatalf("adjustment = %+v, want accepted USD only", adj[0])
	}
}

func TestApplyBusinessCSVImport_RoutesGroupMembershipByGroupLane(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	ctx := context.Background()

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{
			{Account: domain.Account{Code: "acc-1", GroupCode: "desk-a"}},
			{Account: domain.Account{Code: "acc-2", GroupCode: "desk-a"}},
		},
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}
	want := []groupCall{
		{accounts: []domain.AccountID{"acc-1"}, groupID: "desk-a"},
		{accounts: []domain.AccountID{"acc-2"}, groupID: "desk-a"},
	}
	if !slices.EqualFunc(eng.registerGroupCalls, want, groupCallEqual) {
		t.Fatalf("register group calls = %+v, want %+v", eng.registerGroupCalls, want)
	}
	membershipSyncs := eng.groupSyncCalls[len(eng.groupSyncCalls)-2:]
	if !slices.Equal(membershipSyncs, []string{"desk-a", "desk-a"}) {
		t.Fatalf("group sync calls = %+v, want membership syncs on desk-a", eng.groupSyncCalls)
	}
}

// TestApplyBusinessCSVImport_CreatesBlocksAndMovesNewEntitiesWithResolver drives
// the import with the fake engine's resolver enforced, so BlockAccount/BlockGroup/
// RegisterGroup reject any account or group unknown to the engine. Importing a
// brand-new blocked account and group plus a group move for a new account only
// succeeds if the store write and online resolver publication register the
// entities before the engine effects run. It locks in that publication ordering
// without requiring an engine replacement.
func TestApplyBusinessCSVImport_CreatesBlocksAndMovesNewEntitiesWithResolver(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{
			{Account: domain.Account{
				Code: "acc-1", GroupCode: "desk-a", Blocked: true, BlockReason: "risk",
			}},
		},
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}
	if len(eng.blockCalls) != 1 || eng.blockCalls[0].id != "acc-1" {
		t.Fatalf("block account calls = %+v, want one for acc-1", eng.blockCalls)
	}
	wantRegister := []groupCall{{accounts: []domain.AccountID{"acc-1"}, groupID: "desk-a"}}
	if !slices.EqualFunc(eng.registerGroupCalls, wantRegister, groupCallEqual) {
		t.Fatalf("register group calls = %+v, want %+v", eng.registerGroupCalls, wantRegister)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount acc-1: ok=%v err=%v, want present", ok, err)
	}
	if !account.Blocked || account.GroupCode != "desk-a" {
		t.Fatalf("account after import = %+v, want blocked in desk-a", account)
	}
}

func TestApplyBusinessCSVImport_RollsBackRegisteredGroupsOnGroupError(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.failRegisterGroup = "desk-b"
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	for _, group := range []string{"old-a", "old-b"} {
		if _, err := st.CreateGroup(ctx, domain.AccountGroup{Code: group}); err != nil {
			t.Fatalf("CreateGroup(%s): %v", group, err)
		}
	}
	if _, err := st.CreateAccount(ctx, domain.Account{
		Code: "acc-1", GroupCode: "old-a",
	}); err != nil {
		t.Fatalf("CreateAccount(acc-1): %v", err)
	}
	if _, err := st.CreateAccount(ctx, domain.Account{
		Code: "acc-2", GroupCode: "old-b",
	}); err != nil {
		t.Fatalf("CreateAccount(acc-2): %v", err)
	}

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{
			{
				Exists:  true,
				Account: domain.Account{Code: "acc-1", GroupCode: "desk-a"},
			},
			{
				Exists:  true,
				Account: domain.Account{Code: "acc-2", GroupCode: "desk-b"},
			},
		},
	}, testCaller)
	if err == nil {
		t.Fatal("ApplyBusinessCSVImport error = nil, want group error")
	}
	wantUnregister := []groupCall{
		{accounts: []domain.AccountID{"acc-1"}, groupID: "old-a"},
		{accounts: []domain.AccountID{"acc-2"}, groupID: "old-b"},
	}
	if !slices.EqualFunc(eng.unregisterGroupCalls, wantUnregister, groupCallEqual) {
		t.Fatalf("unregister group calls = %+v, want %+v",
			eng.unregisterGroupCalls, wantUnregister)
	}
	wantRegister := []groupCall{
		{accounts: []domain.AccountID{"acc-1"}, groupID: "desk-a"},
		{accounts: []domain.AccountID{"acc-2"}, groupID: "old-b"},
	}
	if !slices.EqualFunc(eng.registerGroupCalls, wantRegister, groupCallEqual) {
		t.Fatalf("register group calls = %+v, want %+v",
			eng.registerGroupCalls, wantRegister)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok || account.GroupCode != "old-a" {
		t.Fatalf("GetAccount acc-1 after rollback: account=%+v ok=%v err=%v",
			account, ok, err)
	}
}

func TestApplyBusinessCSVImport_DictionaryTransactionFailureDoesNotRebuild(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	errStoreWrite := errors.New("business csv dictionary write failed")
	real := newMemoryStore("csv-dictionary-failure.db")
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })

	var failureRealm *failBusinessCSVImportRealm
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		failureRealm = &failBusinessCSVImportRealm{
			RealmStore:       r,
			err:              errStoreWrite,
			failDictionaries: true,
		}
		return failureRealm
	})
	eng := newFakeEngine()
	buildCount := 0
	build := func(snap engine.Snapshot) (engine.Engine, error) {
		buildCount++
		return fakeBuild(eng, new(engine.Snapshot))(snap)
	}
	nn, _, err := NewLocalNode(ctx, st, build)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n.realm)
	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "existing-group"}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "existing-account", GroupCode: "existing-group",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	instance := seedReplayInstance(t, n.realm)
	instrument := seedReplayInstrument(
		t, n.realm, instance.ExternalID, "EURUSD", "EUR", "USD", "2",
	)
	now := time.Now().UTC()
	quote := domain.MarketDataQuote{
		AsOf: now, ReceivedAt: now,
		Instance: instance.ExternalID, ExternalSymbol: instrument.ExternalSymbol,
		BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset, Mark: "2",
	}
	if err := n.realm.UpsertMarketDataQuote(ctx, quote); err != nil {
		t.Fatalf("UpsertMarketDataQuote: %v", err)
	}

	accountsBefore, err := n.realm.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts before: %v", err)
	}
	groupsBefore, err := n.realm.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups before: %v", err)
	}
	instancesBefore, err := n.realm.ListMarketDataInstances(ctx)
	if err != nil {
		t.Fatalf("ListMarketDataInstances before: %v", err)
	}
	instrumentsBefore, err := n.realm.ListMarketDataInstruments(ctx, instance.ExternalID)
	if err != nil {
		t.Fatalf("ListMarketDataInstruments before: %v", err)
	}
	quotesBefore, err := n.realm.ListMarketDataQuotes(ctx, instance.ExternalID)
	if err != nil {
		t.Fatalf("ListMarketDataQuotes before: %v", err)
	}
	auditsBefore, err := n.realm.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("ListAudit before: %v", err)
	}
	buildsBefore := buildCount
	engineBefore := n.currentEngine()
	sinkBefore := n.currentMarketDataSink()

	err = n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Groups: []store.BusinessCSVImportGroup{{
			Group: domain.AccountGroup{Code: "new-group"},
		}},
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{Code: "new-account", GroupCode: "new-group"},
		}},
	}, testCaller)
	if !errors.Is(err, errStoreWrite) {
		t.Fatalf("ApplyBusinessCSVImport error = %v, want dictionary failure", err)
	}
	if buildCount != buildsBefore {
		t.Fatalf("build count = %d, want unchanged %d", buildCount, buildsBefore)
	}
	if n.currentEngine() != engineBefore || n.currentMarketDataSink() != sinkBefore {
		t.Fatal("dictionary transaction failure replaced engine or market-data sink")
	}
	if len(failureRealm.restoreScopes) != 0 {
		t.Fatalf("rollback restore scopes = %+v, want none", failureRealm.restoreScopes)
	}

	accountsAfter, listErr := n.realm.ListAccounts(ctx)
	if listErr != nil {
		t.Fatalf("ListAccounts after: %v", listErr)
	}
	groupsAfter, listErr := n.realm.ListGroups(ctx)
	if listErr != nil {
		t.Fatalf("ListGroups after: %v", listErr)
	}
	instancesAfter, listErr := n.realm.ListMarketDataInstances(ctx)
	if listErr != nil {
		t.Fatalf("ListMarketDataInstances after: %v", listErr)
	}
	instrumentsAfter, listErr := n.realm.ListMarketDataInstruments(ctx, instance.ExternalID)
	if listErr != nil {
		t.Fatalf("ListMarketDataInstruments after: %v", listErr)
	}
	quotesAfter, listErr := n.realm.ListMarketDataQuotes(ctx, instance.ExternalID)
	if listErr != nil {
		t.Fatalf("ListMarketDataQuotes after: %v", listErr)
	}
	auditsAfter, listErr := n.realm.ListAudit(ctx, 100)
	if listErr != nil {
		t.Fatalf("ListAudit after: %v", listErr)
	}
	if !reflect.DeepEqual(accountsAfter, accountsBefore) ||
		!reflect.DeepEqual(groupsAfter, groupsBefore) ||
		!reflect.DeepEqual(auditsAfter, auditsBefore) {
		t.Fatalf("store changed after failed dictionary transaction")
	}
	if !reflect.DeepEqual(instancesAfter, instancesBefore) ||
		!reflect.DeepEqual(instrumentsAfter, instrumentsBefore) ||
		!reflect.DeepEqual(quotesAfter, quotesBefore) {
		t.Fatalf("market data changed after failed dictionary transaction")
	}
}

// TestApplyBusinessCSVImport_ReconcilesEngineOnStoreFailure proves the atomicity
// seam: when the engine adjustments succeeded but the final transactional store
// write fails, the node rebuilds the engine from the persisted (rolled-back)
// store state so the engine and store do not diverge.
func TestApplyBusinessCSVImport_ReconcilesEngineOnStoreFailure(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "10"}
	errStoreWrite := errors.New("business csv store write failed")

	real := newMemoryStore("csv-reconcile.db")
	ctx := context.Background()
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	var failureRealm *failBusinessCSVImportRealm
	st := newRealmWrapStore(real, func(r store.RealmStore) store.RealmStore {
		failureRealm = &failBusinessCSVImportRealm{
			RealmStore: r,
			err:        errStoreWrite,
		}
		return failureRealm
	})

	var buildCount int
	build := func(snap engine.Snapshot) (engine.Engine, error) {
		buildCount++
		eng.knownAccounts = map[domain.AccountID]struct{}{}
		for _, account := range snap.Accounts {
			eng.knownAccounts[account.Code] = struct{}{}
		}
		eng.knownGroups = map[string]struct{}{}
		for _, group := range snap.Groups {
			eng.knownGroups[group.Code] = struct{}{}
		}
		return eng, nil
	}
	nn, _, err := NewLocalNode(ctx, st, build)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n.realm)
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.realm.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil &&
		!errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateAsset: %v", err)
	}
	buildsBefore := buildCount

	// The balance for acc-1 drives a successful engine adjustment on the lane; the
	// decorated realm then fails the final transactional store write, after the
	// engine already applied the adjustment, so the node must reconcile.
	err = n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{
			{Exists: true, Account: domain.Account{Code: "acc-1"}},
		},
		Balances: []domain.Balance{{Account: "acc-1", Asset: "USD", Available: "10"}},
	}, testCaller)
	if !errors.Is(err, errStoreWrite) {
		t.Fatalf("ApplyBusinessCSVImport error = %v, want store write failure", err)
	}
	if buildCount <= buildsBefore {
		t.Fatalf("engine was not rebuilt to reconcile after store failure: builds %d -> %d",
			buildsBefore, buildCount)
	}
	wantRollbackScope := businessCSVRollbackScope()
	if len(failureRealm.exportScopes) != 1 ||
		!reflect.DeepEqual(failureRealm.exportScopes[0], wantRollbackScope) {
		t.Fatalf("rollback export scopes = %+v, want %+v",
			failureRealm.exportScopes, wantRollbackScope)
	}
	wantRestoreScope := wantRollbackScope.Normalize()
	if len(failureRealm.restoreScopes) != 1 {
		t.Fatalf("rollback restore scopes = %+v, want %+v",
			failureRealm.restoreScopes, wantRestoreScope)
	}
	gotRestoreScope := failureRealm.restoreScopes[0]
	if !slices.Equal(gotRestoreScope.Sections, wantRestoreScope.Sections) ||
		!gotRestoreScope.Accounts.All || !gotRestoreScope.Positions.All ||
		gotRestoreScope.All {
		t.Fatalf("rollback restore scope = %+v, want %+v",
			gotRestoreScope, wantRestoreScope)
	}
	for _, section := range failureRealm.restoreScopes[0].Sections {
		if section == backup.SectionMarketData ||
			section == backup.SectionMarketDataQuotes {
			t.Fatalf("business CSV rollback includes market-data section %q", section)
		}
	}
}

func TestApplyBusinessCSVImport_BatchRejectIsRecoverable(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentReject = &domain.AdjustmentOutcomeRejected{
		Code:   "bounds",
		Reason: "outside bounds",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{Code: "acc-1"},
		}},
		Balances: []domain.Balance{{
			Account: "acc-1", Asset: "USD", Available: "10",
		}},
	}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyBusinessCSVImport error = %v, want invalid", err)
	}
	if _, ok, err := st.GetAccount(ctx, "acc-1"); err != nil || ok {
		t.Fatalf("GetAccount after reject: ok=%v err=%v, want absent", ok, err)
	}
}

// A position snapshot whose force-set lands out of bounds kill-switches the
// account inside the engine. The import's own account write carries the CSV's
// blocked=false flag, so the mirror must survive it rather than be clobbered.
func TestApplyBusinessCSVImport_MirrorsEngineAccountBlock(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.adjustmentBatchResults = []engine.AdjustmentResult{{
		Accepted: &domain.AdjustmentOutcomeAccepted{BalanceResult: "10"},
		AccountBlocks: []domain.AccountBlock{{
			Account: "acc-1",
			Policy:  domain.PolicySpotFundsPnlBoundsKillSwitch,
			Code:    "pnl_bounds",
			Reason:  "realized pnl below bound",
		}},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	if err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{Code: "acc-1"},
		}},
		Balances: []domain.Balance{{Account: "acc-1", Asset: "USD", Available: "10"}},
	}, testCaller); err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}

	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount = ok %v, err %v; want present", ok, err)
	}
	if !account.Blocked {
		t.Fatalf("account = %+v, want blocked by the engine kill-switch", account)
	}
	if !strings.Contains(account.BlockReason, "realized pnl below bound") {
		t.Fatalf("block reason = %q, want the engine reason", account.BlockReason)
	}
}

func TestApplyBusinessCSVImport_StaleSeedBlockDoesNotOverrideLiveUnblock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newMemoryStore("csv-seed-block.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := newSeedBlockEngine()
	nRaw, _, err := NewLocalNode(ctx, st, seedBlockBuild(eng))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nRaw.(*localNode)
	seedTestPrincipal(t, n.realm)
	eng.seedBlocks = []domain.AccountBlock{seedBlockOf("acc-1")}

	if err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{Code: "acc-1", Blocked: false},
		}},
	}, testCaller); err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}
	account, ok, err := n.realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Blocked {
		t.Fatalf("account = %+v, want live CSV unblock", account)
	}
	if !slices.Equal(eng.unblockCalls, []domain.AccountID{"acc-1"}) {
		t.Fatalf("engine unblock calls = %+v, want acc-1", eng.unblockCalls)
	}
	rows, err := n.realm.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionUnblock}, Account: "acc-1",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("unblock audits = %+v, want one live CSV audit", rows)
	}
}

// An import the engine accepts without any kill-switch must not touch the
// block state the CSV rows themselves define.
func TestApplyBusinessCSVImport_WithoutBlocksLeavesBlockState(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	if err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{Code: "acc-1"},
		}},
		Balances: []domain.Balance{{Account: "acc-1", Asset: "USD", Available: "10"}},
	}, testCaller); err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}

	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount = ok %v, err %v; want present", ok, err)
	}
	if account.Blocked {
		t.Fatalf("account = %+v, want unblocked", account)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionBlock},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(block): %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("block audit rows = %+v, want none", rows)
	}
}

func TestApplyBusinessCSVImport_DuplicatePositionSnapshotIsInvalid(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{Code: "acc-1"},
		}},
		Balances: []domain.Balance{
			{Account: "acc-1", Asset: "USD", Available: "10"},
			{Account: "acc-1", Asset: "USD", Available: "20"},
		},
	}, testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyBusinessCSVImport error = %v, want invalid", err)
	}
	if len(eng.adjustmentBatchCalls) != 0 {
		t.Fatalf("adjustment batch calls = %+v, want none", eng.adjustmentBatchCalls)
	}
	if _, ok, err := st.GetAccount(ctx, "acc-1"); err != nil || ok {
		t.Fatalf("GetAccount after duplicate snapshot: ok=%v err=%v, want absent",
			ok, err)
	}
}
