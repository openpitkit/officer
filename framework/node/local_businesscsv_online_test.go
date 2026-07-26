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
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

type businessCSVOnlineSink struct{}

func (*businessCSVOnlineSink) Push(marketdata.QuoteUpdate) error { return nil }

type businessCSVOrderingEngine struct {
	*fakeEngine
	mu     sync.Mutex
	events []string
}

func (e *businessCSVOrderingEngine) record(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
}

func (e *businessCSVOrderingEngine) RunAccountSynchronized(
	ctx context.Context,
	account domain.AccountID,
	fn func(engine.AccountLane) error,
) error {
	return e.fakeEngine.RunAccountSynchronized(ctx, account, func(lane engine.AccountLane) error {
		return fn(businessCSVOrderingAccountLane{AccountLane: lane, owner: e})
	})
}

func (e *businessCSVOrderingEngine) RunGroupSynchronized(
	ctx context.Context,
	group string,
	fn func(engine.GroupLane) error,
) error {
	return e.fakeEngine.RunGroupSynchronized(ctx, group, func(lane engine.GroupLane) error {
		return fn(businessCSVOrderingGroupLane{GroupLane: lane, owner: e})
	})
}

type businessCSVOrderingAccountLane struct {
	engine.AccountLane
	owner *businessCSVOrderingEngine
}

func (l businessCSVOrderingAccountLane) SetAccountCurrency(
	ctx context.Context, account domain.AccountID, currency string,
) error {
	l.owner.record("currency:" + account.String())
	return l.AccountLane.SetAccountCurrency(ctx, account, currency)
}

func (l businessCSVOrderingAccountLane) ClearAccountCurrency(
	ctx context.Context, account domain.AccountID,
) error {
	l.owner.record("currency:" + account.String())
	return l.AccountLane.ClearAccountCurrency(ctx, account)
}

func (l businessCSVOrderingAccountLane) SetAccountPnlState(
	ctx context.Context,
	account domain.AccountID,
	pnl string,
	haltReason domain.PnlHaltReason,
) ([]domain.AccountBlock, error) {
	l.owner.record("pnl:" + account.String())
	return l.AccountLane.SetAccountPnlState(ctx, account, pnl, haltReason)
}

func (l businessCSVOrderingAccountLane) ApplyAccountAdjustmentBatch(
	ctx context.Context,
	account domain.AccountID,
	reqs []domain.AdjustmentRequest,
) ([]engine.AdjustmentResult, *engine.AdjustmentBatchReject, error) {
	l.owner.record("adjustment:" + account.String())
	return l.AccountLane.ApplyAccountAdjustmentBatch(ctx, account, reqs)
}

func (l businessCSVOrderingAccountLane) ApplyAccountAdjustment(
	ctx context.Context,
	account domain.AccountID,
	req domain.AdjustmentRequest,
) (engine.AdjustmentResult, error) {
	l.owner.record("adjustment:" + account.String())
	return l.AccountLane.ApplyAccountAdjustment(ctx, account, req)
}

type businessCSVOrderingGroupLane struct {
	engine.GroupLane
	owner *businessCSVOrderingEngine
}

func (l businessCSVOrderingGroupLane) SetGroupCurrency(
	ctx context.Context, group, currency string,
) error {
	l.owner.record("group-currency:" + group)
	return l.GroupLane.SetGroupCurrency(ctx, group, currency)
}

func (l businessCSVOrderingGroupLane) ClearGroupCurrency(
	ctx context.Context, group string,
) error {
	l.owner.record("group-currency:" + group)
	return l.GroupLane.ClearGroupCurrency(ctx, group)
}

func (l businessCSVOrderingGroupLane) RegisterGroup(
	ctx context.Context, accounts []domain.AccountID, group string,
) error {
	l.owner.record("register:" + group)
	return l.GroupLane.RegisterGroup(ctx, accounts, group)
}

func TestApplyBusinessCSVImportPublishesLiveDictionaryWithoutRebuild(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	sink := &businessCSVOnlineSink{}
	eng.sink = sink
	n, probe := newRebuildProbeNode(t, eng)
	ctx := context.Background()

	err := n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Groups: []store.BusinessCSVImportGroup{{
			Group: domain.AccountGroup{
				Code: "desk-a", Currency: "USD", Blocked: true, BlockReason: "risk",
			},
		}},
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{
				Code:          "acc-1",
				GroupCode:     "desk-a",
				Pnl:           "12.5",
				PnlHaltReason: domain.PnlHaltReasonMissingFx,
			},
			PnlSpecified: true,
		}},
		Balances: []domain.Balance{{
			Account: "acc-1", Asset: "USD", Available: "10",
		}},
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}
	if probe.builds != 1 {
		t.Fatalf("engine builds = %d, want initial build only", probe.builds)
	}
	if got := n.currentEngine(); got != eng {
		t.Fatalf("engine = %T, want original fake engine", got)
	}
	if got := n.CurrentMarketDataSink(); got != sink {
		t.Fatalf("market-data sink = %T, want original sink", got)
	}

	account, ok, err := n.realm.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount(acc-1): account=%+v ok=%v err=%v", account, ok, err)
	}
	group, ok, err := n.realm.GetGroup(ctx, "desk-a")
	if err != nil || !ok {
		t.Fatalf("GetGroup(desk-a): group=%+v ok=%v err=%v", group, ok, err)
	}
	if account.EngineAccountID == 0 || group.EngineGroupID == 0 {
		t.Fatalf("persisted engine ids: account=%d group=%d, want assigned",
			account.EngineAccountID, group.EngineGroupID)
	}
	if got := eng.accountResolverIDs[account.Code]; got != account.EngineAccountID {
		t.Fatalf("account resolver id = %d, want %d", got, account.EngineAccountID)
	}
	if got := eng.groupResolverIDs[group.Code]; got != group.EngineGroupID {
		t.Fatalf("group resolver id = %d, want %d", got, group.EngineGroupID)
	}
	if got := eng.groupCurrencies[group.Code]; got != "USD" {
		t.Fatalf("live group currency = %q, want USD", got)
	}
	if _, ok := eng.accountCurrencies[account.Code]; ok {
		t.Fatal("inherited account currency was materialized as an account override")
	}
	if got := eng.effectiveAccountCurrency(account.Code); got != "USD" {
		t.Fatalf("live effective currency = %q, want USD", got)
	}
	if len(eng.accountPnlStateCalls) != 1 ||
		eng.accountPnlStateCalls[0].pnl != "" ||
		eng.accountPnlStateCalls[0].haltReason != domain.PnlHaltReasonMissingFx {
		t.Fatalf("account P&L state calls = %+v, want one missing-FX halt",
			eng.accountPnlStateCalls)
	}
	if account.Pnl != "12.5" || account.PnlHaltReason != domain.PnlHaltReasonMissingFx {
		t.Fatalf("persisted account P&L = %q/%q, want historical 12.5/missing_fx",
			account.Pnl, account.PnlHaltReason)
	}
	if len(eng.blockGroupCalls) != 1 || eng.blockGroupCalls[0].groupID != "desk-a" {
		t.Fatalf("group block calls = %+v, want desk-a", eng.blockGroupCalls)
	}
	if len(eng.registerGroupCalls) != 1 ||
		eng.registerGroupCalls[0].groupID != "desk-a" {
		t.Fatalf("group registration calls = %+v, want acc-1 -> desk-a",
			eng.registerGroupCalls)
	}
	if len(eng.adjustmentBatchCalls) != 1 ||
		eng.adjustmentBatchCalls[0].account != "acc-1" {
		t.Fatalf("adjustment batches = %+v, want acc-1", eng.adjustmentBatchCalls)
	}
	if err := eng.RunAccountSynchronized(
		ctx, "acc-1", func(engine.AccountLane) error { return nil },
	); err != nil {
		t.Fatalf("immediate account lane after import: %v", err)
	}
}

func TestApplyBusinessCSVImportMovesGroupBeforeAccountPnlState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newMemoryStore("csv-group-before-pnl.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	base := newFakeEngine()
	base.enforceResolver = true
	eng := &businessCSVOrderingEngine{fakeEngine: base}
	var captured engine.Snapshot
	inner := fakeBuild(base, &captured)
	nn, _, err := NewLocalNode(ctx, st, func(snap engine.Snapshot) (engine.Engine, error) {
		if _, err := inner(snap); err != nil {
			return nil, err
		}
		return eng, nil
	})
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n.realm)

	err = n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{{
			Account:      domain.Account{Code: "acc-1", GroupCode: "desk-a", Pnl: "5"},
			PnlSpecified: true,
		}},
	}, testCaller)
	if err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}
	eng.mu.Lock()
	events := append([]string(nil), eng.events...)
	eng.mu.Unlock()
	if len(events) != 4 || events[0] != "group-currency:desk-a" ||
		events[1] != "register:desk-a" || events[2] != "currency:acc-1" ||
		events[3] != "pnl:acc-1" {
		t.Fatalf(
			"runtime event order = %v, want group currency and membership before P&L",
			events,
		)
	}
	if _, ok := base.accountCurrencies["acc-1"]; ok {
		t.Fatal("CSV import materialized inherited currency as an account override")
	}
}

func TestAutoCreateInheritsDefaultCurrencyWithoutAccountOverride(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newMemoryStore("auto-create-currency-order.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	base := newFakeEngine()
	base.enforceResolver = true
	base.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "10"}
	eng := &businessCSVOrderingEngine{fakeEngine: base}
	var captured engine.Snapshot
	inner := fakeBuild(base, &captured)
	var builds int
	nn, _, err := NewLocalNode(ctx, st, func(snap engine.Snapshot) (engine.Engine, error) {
		builds++
		if _, err := inner(snap); err != nil {
			return nil, err
		}
		return eng, nil
	})
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n.realm)
	if err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller); err != nil {
		t.Fatalf("SetDefaultGroupCurrency: %v", err)
	}
	eng.mu.Lock()
	eng.events = nil
	eng.mu.Unlock()

	if _, err := n.ApplyAdjustment(ctx, Key{Account: "fresh"}, "",
		domain.AdjustmentRequest{
			Asset: "USD",
			Balance: &domain.AdjustmentAmount{
				Mode: domain.AdjustmentModeAbsolute, Value: "10",
			},
		}, testCaller); err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}
	eng.mu.Lock()
	events := append([]string(nil), eng.events...)
	eng.mu.Unlock()
	if len(events) != 1 || events[0] != "adjustment:fresh" {
		t.Fatalf("runtime event order = %v, want adjustment without account override", events)
	}
	if _, ok := base.accountCurrencies["fresh"]; ok {
		t.Fatal("auto-created account inherited currency was materialized as an override")
	}
	if got := base.effectiveAccountCurrency("fresh"); got != "USD" {
		t.Fatalf("fresh live currency = %q, want inherited USD", got)
	}
	if builds != 1 {
		t.Fatalf("engine builds = %d, want initial build only", builds)
	}
}

func TestAutoCreateDoesNotTouchAccountCurrencyOverride(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, probe := newRebuildProbeNode(t, eng)
	if err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller); err != nil {
		t.Fatalf("SetDefaultGroupCurrency: %v", err)
	}
	eng.accountCurrencyErr = errors.New("account currency must not be touched")

	err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, "fresh", "test", testCaller, "USD",
	)
	if err != nil {
		t.Fatalf("auto-create: %v", err)
	}
	if probe.builds != 1 {
		t.Fatalf("engine builds = %d, want initial build only", probe.builds)
	}
	if _, ok, getErr := n.realm.GetAccount(ctx, "fresh"); getErr != nil || !ok {
		t.Fatalf("GetAccount after auto-create: ok=%v err=%v, want present", ok, getErr)
	}
	if _, ok := eng.accountCurrencies["fresh"]; ok {
		t.Fatal("auto-created account has an explicit live currency override")
	}
	if got := eng.effectiveAccountCurrency("fresh"); got != "USD" {
		t.Fatalf("fresh effective currency = %q, want inherited USD", got)
	}
}

func TestAutoCreatePublishesAccountWithoutRebuild(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "10"}
	sink := &businessCSVOnlineSink{}
	eng.sink = sink
	n, probe := newRebuildProbeNode(t, eng)
	ctx := context.Background()

	if _, err := n.ApplyAdjustment(ctx, Key{Account: "fresh"}, "",
		domain.AdjustmentRequest{
			Asset: "USD",
			Balance: &domain.AdjustmentAmount{
				Mode: domain.AdjustmentModeAbsolute, Value: "10",
			},
		}, testCaller); err != nil {
		t.Fatalf("ApplyAdjustment on fresh account: %v", err)
	}
	if probe.builds != 1 {
		t.Fatalf("engine builds = %d, want initial build only", probe.builds)
	}
	if got := n.CurrentMarketDataSink(); got != sink {
		t.Fatalf("market-data sink = %T, want original sink", got)
	}
	account, ok, err := n.realm.GetAccount(ctx, "fresh")
	if err != nil || !ok {
		t.Fatalf("GetAccount(fresh): account=%+v ok=%v err=%v", account, ok, err)
	}
	if got := eng.accountResolverIDs["fresh"]; got != account.EngineAccountID {
		t.Fatalf("fresh resolver id = %d, want %d", got, account.EngineAccountID)
	}
	if len(eng.adjustmentBatchCalls) != 1 ||
		eng.adjustmentBatchCalls[0].account != "fresh" {
		t.Fatalf("adjustment batches = %+v, want fresh account lane",
			eng.adjustmentBatchCalls)
	}
}

type failBusinessCSVAccountResolverEngine struct {
	*fakeEngine
	err   error
	onAdd func()
}

func (e *failBusinessCSVAccountResolverEngine) AddAccountResolverEntry(
	domain.Account,
) error {
	if e.onAdd != nil {
		e.onAdd()
	}
	return e.err
}

func TestApplyBusinessCSVImportResolverFailureRollsBackWithDurableContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	base := newFakeEngine()
	base.enforceResolver = true
	wrapper := &failBusinessCSVAccountResolverEngine{
		fakeEngine: base,
		err:        errors.New("resolver publication failed"),
		onAdd:      cancel,
	}
	st := newMemoryStore("csv-resolver-rollback.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var builds int
	var captured engine.Snapshot
	inner := fakeBuild(base, &captured)
	nn, _, err := NewLocalNode(ctx, st, func(snap engine.Snapshot) (engine.Engine, error) {
		builds++
		if _, err := inner(snap); err != nil {
			return nil, err
		}
		return wrapper, nil
	})
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n.realm)

	err = n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
		Accounts: []store.BusinessCSVImportAccount{{
			Account: domain.Account{Code: "acc-1"},
		}},
	}, testCaller)
	if !errors.Is(err, wrapper.err) {
		t.Fatalf("ApplyBusinessCSVImport error = %v, want resolver failure", err)
	}
	if builds != 2 {
		t.Fatalf("engine builds = %d, want initial plus reconciliation", builds)
	}
	if _, ok, getErr := n.realm.GetAccount(context.Background(), "acc-1"); getErr != nil || ok {
		t.Fatalf("GetAccount after rollback: ok=%v err=%v, want absent", ok, getErr)
	}
}

type failAutoCreatedAccountDeleteRealm struct {
	store.RealmStore
	err error
}

func (r *failAutoCreatedAccountDeleteRealm) DeleteAccount(
	context.Context, domain.AccountID, bool,
) error {
	return r.err
}

func TestAutoCreateResolverRollbackFailureReconcilesAndFailsStop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	baseStore := newMemoryStore("auto-create-resolver-rollback.db")
	if err := baseStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = baseStore.Close() })
	deleteErr := errors.New("delete auto-created account failed")
	wrappedStore := newRealmWrapStore(baseStore, func(realm store.RealmStore) store.RealmStore {
		return &failAutoCreatedAccountDeleteRealm{RealmStore: realm, err: deleteErr}
	})
	base := newFakeEngine()
	base.enforceResolver = true
	resolverErr := errors.New("resolver publication failed")
	wrapper := &failBusinessCSVAccountResolverEngine{
		fakeEngine: base,
		err:        resolverErr,
	}
	var builds int
	var captured engine.Snapshot
	inner := fakeBuild(base, &captured)
	var fatalErr error
	nn, _, err := NewLocalNode(
		ctx,
		wrappedStore,
		func(snap engine.Snapshot) (engine.Engine, error) {
			builds++
			if _, err := inner(snap); err != nil {
				return nil, err
			}
			return wrapper, nil
		},
		WithFatalShutdownHook(func(err error) { fatalErr = err }),
	)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n.realm)

	err = n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, "fresh", "test", testCaller, "USD",
	)
	if !errors.Is(err, resolverErr) || !errors.Is(err, deleteErr) {
		t.Fatalf("auto-create error = %v, want resolver and delete failures", err)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire after auto-create rollback failure")
	}
	if builds != 2 {
		t.Fatalf("engine builds = %d, want initial plus reconciliation", builds)
	}
	account, ok, getErr := n.realm.GetAccount(ctx, "fresh")
	if getErr != nil || !ok {
		t.Fatalf("GetAccount(fresh): account=%+v ok=%v err=%v", account, ok, getErr)
	}
	if got := base.accountResolverIDs["fresh"]; got != account.EngineAccountID {
		t.Fatalf("reconciled resolver id = %d, want %d", got, account.EngineAccountID)
	}
}

type blockingBusinessCSVExportRealm struct {
	store.RealmStore
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (r *blockingBusinessCSVExportRealm) ExportBackup(
	ctx context.Context, scope backup.Scope,
) (backup.Archive, error) {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
	case <-ctx.Done():
		return backup.Archive{}, ctx.Err()
	}
	return r.RealmStore.ExportBackup(ctx, scope)
}

func TestApplyBusinessCSVImportBlocksNewLaneAdmissions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	baseStore := newMemoryStore("csv-live-gate.db")
	if err := baseStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = baseStore.Close() })
	entered := make(chan struct{})
	release := make(chan struct{})
	wrappedStore := newRealmWrapStore(baseStore, func(realm store.RealmStore) store.RealmStore {
		return &blockingBusinessCSVExportRealm{
			RealmStore: realm,
			entered:    entered,
			release:    release,
		}
	})
	eng := newFakeEngine()
	var captured engine.Snapshot
	nn, _, err := NewLocalNode(ctx, wrappedStore, fakeBuild(eng, &captured))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n.realm)

	importResult := make(chan error, 1)
	go func() {
		importResult <- n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{}, testCaller)
	}()
	<-entered

	type laneResult struct {
		end func()
		err error
	}
	laneResults := make(chan laneResult, 1)
	go func() {
		_, end, err := n.beginLane()
		laneResults <- laneResult{end: end, err: err}
	}()
	select {
	case result := <-laneResults:
		if result.end != nil {
			result.end()
		}
		t.Fatalf("account lane entered during import: %v", result.err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-importResult; err != nil {
		t.Fatalf("ApplyBusinessCSVImport: %v", err)
	}
	select {
	case result := <-laneResults:
		if result.err != nil {
			t.Fatalf("beginLane after import: %v", result.err)
		}
		result.end()
	case <-time.After(time.Second):
		t.Fatal("account lane stayed blocked after import")
	}
}

func TestApplyBusinessCSVImportDrainsActiveLaneBeforeStoreWrite(t *testing.T) {
	t.Parallel()
	n, _ := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	_, endLane, err := n.beginLane()
	if err != nil {
		t.Fatalf("beginLane: %v", err)
	}
	laneReleased := false
	defer func() {
		if !laneReleased {
			endLane()
		}
	}()

	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		result <- n.ApplyBusinessCSVImport(ctx, store.BusinessCSVImport{
			Accounts: []store.BusinessCSVImportAccount{{
				Account: domain.Account{Code: "acc-1"},
			}},
		}, testCaller)
	}()
	<-started
	select {
	case err := <-result:
		t.Fatalf("import completed before active lane drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	endLane()
	laneReleased = true
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ApplyBusinessCSVImport after lane drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("import stayed blocked after active lane drained")
	}
}
