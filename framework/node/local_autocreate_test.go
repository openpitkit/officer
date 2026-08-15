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
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

var testCaller = domain.Caller{
	Source: domain.SourceAPI, Principal: domain.PrincipalOperator,
}

func rateLimit(
	scope domain.LimitScope,
	account domain.AccountID,
	asset string,
	max uint64,
	window time.Duration,
) domain.LimitRate {
	return domain.LimitRate{
		Scope: scope, Account: account, Asset: asset,
		MaxOrders: max, Window: window,
	}
}

func TestEnsureAccountAndAssetsValidatesPolicyOnFastPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n, _ := newTestNode(t, newFakeEngine())
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	err := n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, "acc-1", "maybe", "test", testCaller,
	)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid policy error = %v, want ErrInvalid", err)
	}
}

func TestEnsureAccountRejectValidatesAccountIDBeforeExistence(t *testing.T) {
	t.Parallel()
	n, _ := newTestNode(t, newFakeEngine())

	err := n.ensureAccountAndAssetsRegisteredExclusive(
		context.Background(), "", domain.MissingAccountReject, "test", testCaller,
	)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty account error = %v, want ErrInvalid", err)
	}
	if errors.Is(err, domain.ErrAccountMissing) {
		t.Fatalf("empty account error = %v, must not be ErrAccountMissing", err)
	}
}

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
		Instance: instance.ExternalID, ExternalSymbol: "BTCUSDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", Enabled: true,
	}
	if err := n.UpsertMarketDataInstrument(ctx, instrument, testCaller); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	for _, code := range []string{"BTC", "USDT"} {
		asset, ok, err := realm.GetAsset(ctx, code)
		if err != nil || !ok {
			t.Fatalf("GetAsset(%s) = ok %v, err %v; want created", code, ok, err)
		}
		if asset.AssetClass != autoCreatedAssetClassCode {
			t.Fatalf("asset %s class = %q", code, asset.AssetClass)
		}
	}
	rows, err := realm.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAsset},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create asset): %v", err)
	}
	for _, code := range []string{"BTC", "USDT"} {
		found := false
		for _, row := range rows {
			if row.Asset == code && strings.Contains(
				row.Detail, "by market-data instrument upsert",
			) {
				found = true
			}
		}
		if !found {
			t.Fatalf("create-asset audit rows = %+v, want %s", rows, code)
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

type autoCreateOnlineSink struct{}

func (*autoCreateOnlineSink) Push(marketdata.QuoteUpdate) error { return nil }

type autoCreateOrderingEngine struct {
	*fakeEngine
	mu     sync.Mutex
	events []string
}

func (e *autoCreateOrderingEngine) record(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
}

func (e *autoCreateOrderingEngine) RunAccountSynchronized(
	ctx context.Context,
	account domain.AccountID,
	fn func(engine.AccountLane) error,
) error {
	return e.fakeEngine.RunAccountSynchronized(
		ctx,
		account,
		func(lane engine.AccountLane) error {
			return fn(autoCreateOrderingAccountLane{AccountLane: lane, owner: e})
		},
	)
}

type autoCreateOrderingAccountLane struct {
	engine.AccountLane
	owner *autoCreateOrderingEngine
}

func (l autoCreateOrderingAccountLane) ApplyAccountAdjustmentBatch(
	ctx context.Context,
	account domain.AccountID,
	reqs []domain.AdjustmentRequest,
) ([]engine.AdjustmentResult, *engine.AdjustmentBatchReject, error) {
	l.owner.record("adjustment:" + account.String())
	return l.AccountLane.ApplyAccountAdjustmentBatch(ctx, account, reqs)
}

func (l autoCreateOrderingAccountLane) ApplyAccountAdjustment(
	ctx context.Context,
	account domain.AccountID,
	req domain.AdjustmentRequest,
) (engine.AdjustmentResult, error) {
	l.owner.record("adjustment:" + account.String())
	return l.AccountLane.ApplyAccountAdjustment(ctx, account, req)
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
	eng := &autoCreateOrderingEngine{fakeEngine: base}
	var captured engine.Snapshot
	inner := fakeBuild(base, &captured)
	var builds int
	nn, _, err := NewLocalNode(
		ctx,
		st,
		func(snap engine.Snapshot) (engine.Engine, error) {
			builds++
			if _, buildErr := inner(snap); buildErr != nil {
				return nil, buildErr
			}
			return eng, nil
		},
	)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	if err := n.realm.CreatePrincipal(ctx, domain.Principal{Code: testCaller.Principal}); err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreatePrincipal(%s): %v", testCaller.Principal, err)
	}
	if _, err := n.CreateAsset(ctx, domain.Asset{Code: "USD"}, testCaller); err != nil {
		t.Fatalf("CreateAsset(USD): %v", err)
	}
	if err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller); err != nil {
		t.Fatalf("SetDefaultGroupCurrency: %v", err)
	}
	eng.mu.Lock()
	eng.events = nil
	eng.mu.Unlock()

	if _, err := n.ApplyAdjustment(
		ctx,
		Key{Account: "fresh"},
		"",
		domain.AdjustmentRequest{
			Asset: "USD",
			Balance: &domain.AdjustmentAmount{
				Mode: domain.AdjustmentModeAbsolute, Value: "10",
			},
		},
		domain.MissingAccountCreate,
		testCaller,
	); err != nil {
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
		ctx, "fresh", domain.MissingAccountCreate, "test", testCaller, "USD",
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
	const assetCode = "AUTOUSD"
	eng := newFakeEngine()
	eng.enforceResolver = true
	eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{BalanceResult: "10"}
	sink := &autoCreateOnlineSink{}
	eng.sink = sink
	n, probe := newRebuildProbeNode(t, eng)
	ctx := context.Background()

	if _, err := n.ApplyAdjustment(
		ctx,
		Key{Account: "fresh"},
		"",
		domain.AdjustmentRequest{
			Asset: assetCode,
			Balance: &domain.AdjustmentAmount{
				Mode: domain.AdjustmentModeAbsolute, Value: "10",
			},
		},
		domain.MissingAccountCreate,
		testCaller,
	); err != nil {
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
	asset, ok, err := n.realm.GetAsset(ctx, assetCode)
	if err != nil || !ok {
		t.Fatalf("GetAsset(%s): asset=%+v ok=%v err=%v", assetCode, asset, ok, err)
	}
	if got := eng.assetResolverIDs[assetCode]; got != asset.EngineAssetID {
		t.Fatalf("%s resolver id = %d, want %d", assetCode, got, asset.EngineAssetID)
	}
	if len(eng.adjustmentBatchCalls) != 1 ||
		eng.adjustmentBatchCalls[0].account != "fresh" {
		t.Fatalf("adjustment batches = %+v, want fresh account lane", eng.adjustmentBatchCalls)
	}
}

type accountMutationProbeRealm struct {
	store.RealmStore
	createCalls int
}

func (r *accountMutationProbeRealm) CreateAccount(
	ctx context.Context, account domain.Account,
) (domain.Account, error) {
	r.createCalls++
	return r.RealmStore.CreateAccount(ctx, account)
}

func TestLocalNode_SetAccountGroupRejectsUnsupportedAccountAutoCreateBeforeStoreWrite(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	base := newMemoryStore("auto-create-account-capability.db")
	var probe *accountMutationProbeRealm
	st := newRealmWrapStore(base, func(realm store.RealmStore) store.RealmStore {
		probe = &accountMutationProbeRealm{RealmStore: realm}
		return probe
	})
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	n := newResolverCapabilityTestNode(t, st)
	if probe == nil {
		t.Fatal("account mutation probe was not installed")
	}
	probe.createCalls = 0

	err := n.SetAccountGroup(
		ctx,
		testKey("fresh"),
		"",
		domain.MissingAccountCreate,
		testCaller,
	)
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("SetAccountGroup = %v, want ErrNotImplemented", err)
	}
	if probe.createCalls != 0 {
		t.Fatalf("CreateAccount store calls = %d, want 0", probe.createCalls)
	}
	if _, ok, getErr := n.realm.GetAccount(ctx, "fresh"); getErr != nil || ok {
		t.Fatalf(
			"GetAccount(fresh) = ok %v err %v, want absent",
			ok,
			getErr,
		)
	}
}

type failingAccountResolverEngine struct {
	*fakeEngine
	err error
}

func (e *failingAccountResolverEngine) AddAccountResolverEntry(
	domain.Account,
) error {
	return e.err
}

type failingAutoCreatedAccountDeleteRealm struct {
	store.RealmStore
	err error
}

func (r *failingAutoCreatedAccountDeleteRealm) DeleteAccount(
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
		return &failingAutoCreatedAccountDeleteRealm{RealmStore: realm, err: deleteErr}
	})
	base := newFakeEngine()
	base.enforceResolver = true
	resolverErr := errors.New("resolver publication failed")
	wrapper := &failingAccountResolverEngine{fakeEngine: base, err: resolverErr}
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
	seedTestPrincipal(t, n)

	err = n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, "fresh", domain.MissingAccountCreate, "test", testCaller, "USD",
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

// TestAutoCreateResolverRollbackSuccessIsInternal covers the
// successful-rollback branch of rollbackAutoCreatedAccountPublication: the
// store write for the auto-created account already committed before the
// resolver publish failed, so even though the compensating delete restores a
// consistent state, the failure is Officer's to own. The caller must not see
// it classified as the domain sentinel the resolver failure carried (which
// would surface as a 409 for a request that merely referenced an unknown
// account code), while the underlying diagnostic cause must still be reachable
// for logs.
func TestAutoCreateResolverRollbackSuccessIsInternal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	baseStore := newMemoryStore("auto-create-resolver-rollback-success.db")
	if err := baseStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = baseStore.Close() })
	base := newFakeEngine()
	diagnosticCause := errors.New("account dictionary desync detail")
	resolverErr := fmt.Errorf(
		"engine: account resolver alias %q already exists: %w: %w",
		"fresh", domain.ErrAlreadyExists, diagnosticCause,
	)
	wrapper := &failingAccountResolverEngine{fakeEngine: base, err: resolverErr}
	var captured engine.Snapshot
	inner := fakeBuild(base, &captured)
	nn, _, err := NewLocalNode(
		ctx,
		baseStore,
		func(snap engine.Snapshot) (engine.Engine, error) {
			if _, buildErr := inner(snap); buildErr != nil {
				return nil, buildErr
			}
			return wrapper, nil
		},
	)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n)
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	err = n.ensureAccountAndAssetsRegisteredExclusive(
		ctx, "fresh", domain.MissingAccountCreate, "test", testCaller, "USD",
	)
	if err == nil {
		t.Fatal("auto-create succeeded, want resolver publication failure")
	}
	if errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf(
			"auto-create error = %v, must not expose the domain sentinel after a "+
				"committed auto-create rollback",
			err,
		)
	}
	if !errors.Is(err, diagnosticCause) {
		t.Fatalf("auto-create error = %v, want diagnostic cause preserved", err)
	}
	if _, ok, getErr := n.realm.GetAccount(ctx, "fresh"); getErr != nil || ok {
		t.Fatalf("GetAccount(fresh) after rollback: ok=%v err=%v, want removed", ok, getErr)
	}
	if fatalErr != nil {
		t.Fatalf(
			"fatal error = %v, want successful rollback without a fatal", fatalErr,
		)
	}
}
