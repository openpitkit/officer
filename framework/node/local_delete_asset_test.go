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
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

func newAssetDeleteTestNode(t *testing.T, eng *fakeEngine) *localNode {
	t.Helper()
	n, _ := newTestNode(t, eng)
	return n
}

type deleteCancellationProbeRealm struct {
	store.RealmStore
	deleteCtxErr error
	auditCtxErr  error
}

func (r *deleteCancellationProbeRealm) DeleteAsset(
	ctx context.Context,
	code string,
	force bool,
) error {
	r.deleteCtxErr = ctx.Err()
	if r.deleteCtxErr != nil {
		return r.deleteCtxErr
	}
	return r.RealmStore.DeleteAsset(ctx, code, force)
}

func (r *deleteCancellationProbeRealm) AppendAudit(
	ctx context.Context,
	entry store.AuditEntry,
) error {
	if entry.Action == domain.AuditActionDeleteAsset {
		r.auditCtxErr = ctx.Err()
		if r.auditCtxErr != nil {
			return r.auditCtxErr
		}
	}
	return r.RealmStore.AppendAudit(ctx, entry)
}

type failAssetDeleteRealm struct {
	store.RealmStore
	err error
}

func (r *failAssetDeleteRealm) DeleteAsset(
	context.Context, string, bool,
) error {
	return r.err
}

func assertInternalAssetFailureUnclassified(
	t *testing.T,
	err error,
	fragments ...string,
) {
	t.Helper()
	if err == nil {
		t.Fatal("asset mutation succeeded, want internal resolver failure")
	}
	for _, fragment := range fragments {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("asset mutation error = %v, want diagnostic %q", err, fragment)
		}
	}
	if errors.Is(err, domain.ErrInvalid) ||
		errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("asset mutation error = %v, must not expose domain errors", err)
	}
}

func TestInternalPostCommitNodeMutationFailureIsTerminalToDomainErrorsIs(
	t *testing.T,
) {
	t.Parallel()
	sentinels := []struct {
		name string
		err  error
	}{
		{name: "account_missing", err: domain.ErrAccountMissing},
		{name: "already_exists", err: domain.ErrAlreadyExists},
		{name: "conflict", err: domain.ErrConflict},
		{name: "engine_restarting", err: domain.ErrEngineRestarting},
		{name: "execution_report_required", err: domain.ErrExecutionReportRequired},
		{name: "forbidden", err: domain.ErrForbidden},
		{name: "has_dependents", err: domain.ErrHasDependents},
		{name: "invalid", err: domain.ErrInvalid},
		{name: "no_change", err: domain.ErrNoChange},
		{name: "not_found", err: domain.ErrNotFound},
		{name: "not_implemented", err: domain.ErrNotImplemented},
		{name: "reserved_group", err: domain.ErrReservedGroup},
		{name: "terminal_order", err: domain.ErrTerminalOrder},
		{name: "too_large", err: domain.ErrTooLarge},
		{name: "upstream", err: domain.ErrUpstream},
	}
	shapes := []struct {
		name  string
		build func(error) error
	}{
		{
			name: "direct",
			build: func(sentinel error) error {
				return internalPostCommitNodeMutationError(sentinel)
			},
		},
		{
			name: "wrapped",
			build: func(sentinel error) error {
				return internalPostCommitNodeMutationError(
					fmt.Errorf("post-commit failure: %w", sentinel),
				)
			},
		},
		{
			name: "nested",
			build: func(sentinel error) error {
				return internalPostCommitNodeMutationError(fmt.Errorf(
					"outer failure: %w",
					fmt.Errorf("inner failure: %w", sentinel),
				))
			},
		},
		{
			name: "joined",
			build: func(sentinel error) error {
				return internalPostCommitNodeMutationError(errors.Join(
					errors.New("non-domain joined cause"),
					sentinel,
				))
			},
		},
		{
			name: "double_wrapper",
			build: func(sentinel error) error {
				once := internalPostCommitNodeMutationError(fmt.Errorf(
					"post-commit failure: %w", sentinel,
				))
				return internalPostCommitNodeMutationError(once)
			},
		},
		{
			// A caller outside this package wraps the already-terminal error
			// (not the sentinel) further. errors.Is traversal must still stop
			// at the terminal value.
			name: "outer_wrap",
			build: func(sentinel error) error {
				terminal := internalPostCommitNodeMutationError(sentinel)
				return fmt.Errorf("caller context: %w", terminal)
			},
		},
		{
			// A caller joins the already-terminal error with an unrelated,
			// non-domain error. The terminal branch must still hide the
			// sentinel, and the other branch carries none, so the join as a
			// whole must not expose it either.
			name: "outer_join",
			build: func(sentinel error) error {
				terminal := internalPostCommitNodeMutationError(sentinel)
				return errors.Join(terminal, errors.New("unrelated caller failure"))
			},
		},
	}
	for _, sentinel := range sentinels {
		for _, shape := range shapes {
			t.Run(sentinel.name+"/"+shape.name, func(t *testing.T) {
				err := shape.build(sentinel.err)
				if errors.Is(err, sentinel.err) {
					t.Fatalf(
						"post-commit error = %v, must hide %v",
						err,
						sentinel.err,
					)
				}
			})
		}
	}
}

func TestInternalPostCommitNodeMutationFailurePreservesNonDomainErrorsIs(
	t *testing.T,
) {
	t.Parallel()
	cause := errors.New("non-domain mutation failure")
	err := internalPostCommitNodeMutationError(
		fmt.Errorf("post-commit failure: %w", cause),
	)
	if !errors.Is(err, cause) {
		t.Fatalf("post-commit error = %v, want non-domain cause", err)
	}
}

func TestInternalPostCommitNodeMutationFailureHasNoErrorsAsTraversal(
	t *testing.T,
) {
	t.Parallel()
	typed := domain.NewCurrencyChangeBlockedError(
		domain.ScopeAsset,
		"GOLD",
		domain.ErrConflict,
	)
	err := internalPostCommitNodeMutationError(
		fmt.Errorf("post-commit failure: %w", typed),
	)
	var target domain.CurrencyChangeBlockedError
	if errors.As(err, &target) {
		t.Fatalf("post-commit error = %v, must terminate errors.As traversal", err)
	}
}

type blockingDeleteAssetRealm struct {
	store.RealmStore
	entered chan struct{}
	release <-chan struct{}
}

func (r *blockingDeleteAssetRealm) DeleteAsset(
	ctx context.Context,
	code string,
	force bool,
) error {
	close(r.entered)
	<-r.release
	return r.RealmStore.DeleteAsset(ctx, code, force)
}

type assetDeleteQuoteConnector struct {
	updates chan marketdata.QuoteUpdate
}

func (c *assetDeleteQuoteConnector) Subscribe(
	context.Context,
	[]marketdata.Subscription,
) (<-chan marketdata.QuoteUpdate, error) {
	return c.updates, nil
}

func (c *assetDeleteQuoteConnector) Close() {
	close(c.updates)
}

type assetDeleteSinkResult struct {
	update marketdata.QuoteUpdate
	err    error
}

type assetDeleteResolverSink struct {
	engine  *fakeEngine
	results chan assetDeleteSinkResult
}

func (s *assetDeleteResolverSink) Push(update marketdata.QuoteUpdate) error {
	err := s.resolve(update.Base)
	if err == nil {
		err = s.resolve(update.Quote)
	}
	s.results <- assetDeleteSinkResult{update: update, err: err}
	return err
}

func (s *assetDeleteResolverSink) resolve(id domain.EngineAssetID) error {
	s.engine.resolverMu.RLock()
	defer s.engine.resolverMu.RUnlock()
	for _, currentID := range s.engine.assetResolverIDs {
		if currentID == id {
			return nil
		}
	}
	return fmt.Errorf("resolve market-data asset id %d: %w", id, domain.ErrNotFound)
}

func seedAssetPnlBound(t *testing.T, n *localNode, asset string) {
	t.Helper()
	if err := n.realm.PutSpotFundsPnlBoundsLimit(
		context.Background(),
		domain.LimitSpotFundsPnlBounds{
			Scope: domain.ScopeGlobal, Currency: asset, LowerBound: "-1",
		},
	); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}
}

func TestSnapshotWithoutAssetDropsCascadedRuntimeRows(t *testing.T) {
	t.Parallel()
	snapshot := snapshotWithoutAsset(engine.Snapshot{
		Assets: []domain.Asset{
			{Code: "AAPL", EngineAssetID: 1},
			{Code: "USD", EngineAssetID: 2},
		},
		Accounts: []domain.Account{{Code: "retained", Currency: "USD"}},
		Groups:   []domain.AccountGroup{{Code: "desk", Currency: "USD"}},
		Balances: []domain.Balance{
			{Account: "account", Asset: "AAPL"},
			{Account: "account", Asset: "USD"},
		},
		RateLimits: []domain.LimitRate{
			{Scope: domain.ScopeAsset, Asset: "AAPL"},
			{Scope: domain.ScopeAsset, Asset: "USD"},
		},
		OrderSizeLimits: []domain.LimitOrderSize{
			{Scope: domain.ScopeUnderlyingAsset, Asset: "AAPL", MaxQuantity: "1"},
			{Scope: domain.ScopeSettlementAsset, Asset: "USD", MaxNotional: "1"},
		},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			{Scope: domain.ScopeGlobal, Currency: "AAPL", LowerBound: "-1"},
			{Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-1"},
		},
	}, "AAPL")

	if len(snapshot.Assets) != 1 || snapshot.Assets[0].Code != "USD" ||
		len(snapshot.Accounts) != 1 || len(snapshot.Groups) != 1 ||
		len(snapshot.Balances) != 1 || snapshot.Balances[0].Asset != "USD" ||
		len(snapshot.RateLimits) != 1 || snapshot.RateLimits[0].Asset != "USD" ||
		len(snapshot.OrderSizeLimits) != 1 ||
		snapshot.OrderSizeLimits[0].Asset != "USD" ||
		len(snapshot.SpotFundsPnlBoundsLimits) != 1 ||
		snapshot.SpotFundsPnlBoundsLimits[0].Currency != "USD" {
		t.Fatalf("snapshot without asset = %+v", snapshot)
	}
}

func TestDeleteAssetForceRebuildsWithoutCascadedRuntimeRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := newFakeEngine()
	old.enforceResolver = true
	n := newAssetDeleteTestNode(t, old)
	const asset = "AAPL"

	if _, err := n.realm.CreateAccount(ctx, domain.Account{Code: "account"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.realm.UpsertBalance(ctx, domain.Balance{
		Account: "account", Asset: asset, Available: "1",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	if err := n.realm.PutRateLimit(ctx, domain.LimitRate{
		Scope: domain.ScopeAsset, Asset: asset, MaxOrders: 1, Window: time.Minute,
	}); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	if err := n.realm.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeUnderlyingAsset, Asset: asset, MaxQuantity: "1",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit: %v", err)
	}
	seedAssetPnlBound(t, n, asset)
	instance, err := n.realm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO,
		Label:    "manual",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if err := n.realm.UpsertMarketDataInstrument(ctx, domain.MarketDataInstrument{
		Instance:       instance.ExternalID,
		ExternalSymbol: "AAPLUSD",
		BaseAsset:      asset,
		QuoteAsset:     "USD",
		ManualPrice:    "1",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	const (
		survivingBaseAsset  = "MSFT"
		survivingQuoteAsset = "USD"
		survivingMark       = "400"
	)
	if _, err := n.realm.CreateAsset(ctx, domain.Asset{Code: survivingBaseAsset}); err != nil {
		t.Fatalf("CreateAsset(%s): %v", survivingBaseAsset, err)
	}
	if err := n.realm.UpsertMarketDataInstrument(ctx, domain.MarketDataInstrument{
		Instance:       instance.ExternalID,
		ExternalSymbol: "MSFTUSD",
		BaseAsset:      survivingBaseAsset,
		QuoteAsset:     survivingQuoteAsset,
		ManualPrice:    survivingMark,
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertMarketDataInstrument(surviving): %v", err)
	}
	next := newFakeEngine()
	var rebuilt engine.Snapshot
	n.build = fakeBuild(next, &rebuilt)

	err = n.DeleteAsset(ctx, asset, false, testCaller)
	if !errors.Is(err, domain.ErrHasDependents) {
		t.Fatalf("DeleteAsset(no force) = %v, want ErrHasDependents", err)
	}
	if n.currentEngine() != old || !old.running {
		t.Fatalf("engine after rejected delete: current=%p old running=%v",
			n.currentEngine(), old.running)
	}

	if err := n.DeleteAsset(ctx, asset, true, testCaller); err != nil {
		t.Fatalf("DeleteAsset(force): %v", err)
	}
	if _, ok, getErr := n.realm.GetAsset(ctx, asset); getErr != nil || ok {
		t.Fatalf("asset after forced delete: ok=%v err=%v, want absent", ok, getErr)
	}
	if len(rebuilt.Balances) != 0 || len(rebuilt.RateLimits) != 0 ||
		len(rebuilt.OrderSizeLimits) != 0 ||
		len(rebuilt.SpotFundsPnlBoundsLimits) != 0 {
		t.Fatalf("rebuilt configuration retained deleted asset: %+v", rebuilt)
	}
	balances, balanceErr := n.realm.ListBalances(ctx, "", asset)
	if balanceErr != nil || len(balances) != 0 {
		t.Fatalf("balances after forced delete = %+v, err=%v", balances, balanceErr)
	}
	rateLimits, rateLimitErr := n.realm.ListRateLimits(ctx, "")
	if rateLimitErr != nil || len(rateLimits) != 0 {
		t.Fatalf("rate limits after forced delete = %+v, err=%v", rateLimits, rateLimitErr)
	}
	orderSizeLimits, orderSizeLimitErr := n.realm.ListOrderSizeLimits(ctx, "")
	if orderSizeLimitErr != nil || len(orderSizeLimits) != 0 {
		t.Fatalf(
			"order-size limits after forced delete = %+v, err=%v",
			orderSizeLimits,
			orderSizeLimitErr,
		)
	}
	pnlBounds, pnlBoundsErr := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
	if pnlBoundsErr != nil || len(pnlBounds) != 0 {
		t.Fatalf("P&L bounds after forced delete = %+v, err=%v", pnlBounds, pnlBoundsErr)
	}
	instruments, instrumentsErr := n.realm.ListMarketDataInstruments(ctx, instance.ExternalID)
	if instrumentsErr != nil || len(instruments) != 1 ||
		instruments[0].ExternalSymbol != "MSFTUSD" ||
		instruments[0].BaseAsset != survivingBaseAsset ||
		instruments[0].QuoteAsset != survivingQuoteAsset ||
		instruments[0].ManualPrice != survivingMark {
		t.Fatalf(
			"market-data instruments after forced delete = %+v, err=%v",
			instruments,
			instrumentsErr,
		)
	}
	if n.currentEngine() != next || !next.running || old.running {
		t.Fatalf("engine after forced delete: current=%p next running=%v old running=%v",
			n.currentEngine(), next.running, old.running)
	}
}

func TestDeleteAssetForceFailureKeepsStoreAndOldEngine(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		run  func(*localNode) (*fakeEngine, error)
	}{
		{
			name: "build",
			run: func(n *localNode) (*fakeEngine, error) {
				buildErr := errors.New("asset delete build failed")
				n.build = func(engine.Snapshot) (engine.Engine, error) {
					return nil, buildErr
				}
				return nil, buildErr
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			old := newFakeEngine()
			n := newAssetDeleteTestNode(t, old)
			seedAssetPnlBound(t, n, "AAPL")

			next, want := test.run(n)
			err := n.DeleteAsset(ctx, "AAPL", true, testCaller)
			if err == nil || (!errors.Is(err, want) &&
				!strings.Contains(err.Error(), want.Error())) {
				t.Fatalf("DeleteAsset(force) = %v, want %v", err, want)
			}
			if _, ok, getErr := n.realm.GetAsset(ctx, "AAPL"); getErr != nil || !ok {
				t.Fatalf("asset after failed delete: ok=%v err=%v, want retained", ok, getErr)
			}
			limits, listErr := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
			if listErr != nil || len(limits) != 1 || limits[0].Currency != "AAPL" {
				t.Fatalf("SpotFunds P&L bounds after failed delete = %+v, err=%v", limits, listErr)
			}
			if n.currentEngine() != old || !old.running {
				t.Fatalf("engine after failed delete: current=%p old running=%v",
					n.currentEngine(), old.running)
			}
			if next != nil && next.running {
				t.Fatalf("new engine after failed delete still running: %p", next)
			}
		})
	}
}

func TestDeleteAssetForceRejectsCurrencyDependentsBeforeBuild(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		kind    string
		arrange func(*testing.T, *localNode)
	}{
		{
			name: "account currency",
			kind: "account_currency",
			arrange: func(t *testing.T, n *localNode) {
				t.Helper()
				if _, err := n.CreateAccount(
					context.Background(),
					domain.Account{Code: "currency-account", Currency: "AAPL"},
					testCaller,
				); err != nil {
					t.Fatalf("CreateAccount: %v", err)
				}
			},
		},
		{
			name: "group currency",
			kind: "account_group_currency",
			arrange: func(t *testing.T, n *localNode) {
				t.Helper()
				if _, err := n.CreateGroup(
					context.Background(),
					domain.AccountGroup{Code: "currency-group", Currency: "AAPL"},
					testCaller,
				); err != nil {
					t.Fatalf("CreateGroup: %v", err)
				}
			},
		},
		{
			name: "default-group currency",
			kind: "account_group_currency",
			arrange: func(t *testing.T, n *localNode) {
				t.Helper()
				if err := n.SetDefaultGroupCurrency(
					context.Background(), "AAPL", testCaller,
				); err != nil {
					t.Fatalf("SetDefaultGroupCurrency: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			n := newAssetDeleteTestNode(t, newFakeEngine())
			test.arrange(t, n)
			buildCalls := 0
			n.build = func(engine.Snapshot) (engine.Engine, error) {
				buildCalls++
				return nil, errors.New("asset delete build must not run")
			}

			err := n.DeleteAsset(ctx, "AAPL", true, testCaller)
			var dependentErr domain.HasDependentsError
			if !errors.Is(err, domain.ErrHasDependents) ||
				!errors.As(err, &dependentErr) {
				t.Fatalf("DeleteAsset(force) = %v, want ErrHasDependents", err)
			}
			if len(dependentErr.Dependents) != 1 ||
				dependentErr.Dependents[0].Kind != test.kind ||
				dependentErr.Dependents[0].Count != 1 {
				t.Fatalf(
					"dependents = %+v, want one %s",
					dependentErr.Dependents,
					test.kind,
				)
			}
			if buildCalls != 0 {
				t.Fatalf("build calls = %d, want 0", buildCalls)
			}
			if _, ok, getErr := n.realm.GetAsset(ctx, "AAPL"); getErr != nil || !ok {
				t.Fatalf("asset after rejected delete: ok=%v err=%v, want retained", ok, getErr)
			}
		})
	}
}

func TestLocalNode_DeleteAssetRemovesLiveResolver(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	eng.enforceResolver = true
	eng.checkResult = domain.CheckResult{Passed: true}
	n := newAssetDeleteTestNode(t, eng)
	const (
		account domain.AccountID = "asset-delete-account"
		asset   string           = "EUR"
		quote   string           = "USD"
	)

	if _, err := n.CreateAccount(ctx, testAccount(account), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := n.DeleteAsset(ctx, asset, false, testCaller); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	_, err := n.CheckOrder(ctx, testKey(account), domain.OrderProbe{
		Account: account, BaseAsset: asset, QuoteAsset: quote,
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
		AmountValue: "1", Price: "1",
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CheckOrder with deleted asset = %v, want ErrInvalid", err)
	}
}

func TestLocalNode_CreateAssetResolverFailureRollbackIsUnclassified(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	eng.resolverMu.Lock()
	eng.knownAssets["GOLD"] = struct{}{}
	eng.resolverMu.Unlock()
	fatalCalls := 0
	n.fatal = func(error) { fatalCalls++ }

	_, err := n.CreateAsset(ctx, domain.Asset{Code: "GOLD"}, testCaller)
	assertInternalAssetFailureUnclassified(
		t,
		err,
		"publish asset resolver:",
		domain.ErrAlreadyExists.Error(),
	)
	if fatalCalls != 0 {
		t.Fatalf("fatal hook calls = %d, want 0", fatalCalls)
	}
	if _, ok, getErr := st.GetAsset(ctx, "GOLD"); getErr != nil || ok {
		t.Fatalf(
			"GetAsset(GOLD) after rollback = ok %v, err %v, want absent",
			ok,
			getErr,
		)
	}
}

func TestLocalNode_CreateAssetResolverFailureReconciliationIsUnclassified(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	rollbackErr := errors.New("asset create rollback failed")
	base := newMemoryStore("asset-create-rollback.db")
	t.Cleanup(func() { _ = base.Close() })
	st := newRealmWrapStore(base, func(realm store.RealmStore) store.RealmStore {
		return &failAssetDeleteRealm{
			RealmStore: realm,
			err:        rollbackErr,
		}
	})
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	eng := newFakeEngine()
	n := newTestNodeWithStore(t, st, eng)
	eng.resolverMu.Lock()
	eng.knownAssets["GOLD"] = struct{}{}
	eng.resolverMu.Unlock()
	buildErr := errors.New("asset create rebuild failed")
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, buildErr
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	_, err := n.CreateAsset(ctx, domain.Asset{Code: "GOLD"}, testCaller)
	assertInternalAssetFailureUnclassified(
		t,
		err,
		"publish asset resolver:",
		rollbackErr.Error(),
		buildErr.Error(),
	)
	if !errors.Is(err, rollbackErr) || !errors.Is(err, buildErr) {
		t.Fatalf("CreateAsset = %v, want rollback and rebuild causes", err)
	}
	if fatalErr == nil ||
		!errors.Is(fatalErr, domain.ErrAlreadyExists) ||
		!errors.Is(fatalErr, rollbackErr) ||
		!errors.Is(fatalErr, buildErr) {
		t.Fatalf(
			"fatal error = %v, want resolver, rollback, and rebuild chains",
			fatalErr,
		)
	}
	if _, ok, getErr := n.realm.GetAsset(ctx, "GOLD"); getErr != nil || !ok {
		t.Fatalf(
			"GetAsset(GOLD) after failed rollback = ok %v, err %v, want present",
			ok,
			getErr,
		)
	}
}

func TestLocalNode_RenameAssetResolverFailureRollbackIsUnclassified(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	if _, err := n.CreateAsset(
		ctx,
		domain.Asset{Code: "GOLD"},
		testCaller,
	); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	resolverErr := fmt.Errorf(
		"asset resolver rename failed: %w",
		domain.ErrAlreadyExists,
	)
	eng.renameAssetResolverErr = resolverErr
	fatalCalls := 0
	n.fatal = func(error) { fatalCalls++ }
	_, err := n.UpdateAsset(
		ctx,
		"GOLD",
		domain.Asset{Code: "GOLDX"},
		testCaller,
	)
	assertInternalAssetFailureUnclassified(t, err, resolverErr.Error())
	if !errors.Is(err, resolverErr) {
		t.Fatalf("UpdateAsset = %v, want exact resolver error", err)
	}
	if fatalCalls != 0 {
		t.Fatalf("fatal hook calls = %d, want 0", fatalCalls)
	}
	if _, ok, getErr := st.GetAsset(ctx, "GOLD"); getErr != nil || !ok {
		t.Fatalf("GetAsset(GOLD) after rollback = ok %v, err %v", ok, getErr)
	}
	if _, ok, getErr := st.GetAsset(ctx, "GOLDX"); getErr != nil || ok {
		t.Fatalf(
			"GetAsset(GOLDX) after rollback = ok %v, err %v, want absent",
			ok,
			getErr,
		)
	}
}

func TestLocalNode_RenameAssetResolverFailureReconciliationIsUnclassified(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	restoreErr := errors.New("asset restore failed")
	base := newMemoryStore("asset-rename-unclassified.db")
	t.Cleanup(func() { _ = base.Close() })
	st := newRealmWrapStore(base, func(realm store.RealmStore) store.RealmStore {
		return &failAssetRestoreRealm{RealmStore: realm, err: restoreErr}
	})
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	eng := newFakeEngine()
	n := newTestNodeWithStore(t, st, eng)
	if _, err := n.CreateAsset(
		ctx,
		domain.Asset{Code: "GOLD"},
		testCaller,
	); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	resolverErr := fmt.Errorf(
		"asset resolver rename failed: %w",
		domain.ErrInvalid,
	)
	eng.renameAssetResolverErr = resolverErr
	buildErr := errors.New("asset rename build failed")
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, buildErr
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	_, err := n.UpdateAsset(
		ctx,
		"GOLD",
		domain.Asset{Code: "GOLDX"},
		testCaller,
	)
	assertInternalAssetFailureUnclassified(
		t,
		err,
		resolverErr.Error(),
		restoreErr.Error(),
		buildErr.Error(),
	)
	if !errors.Is(err, resolverErr) ||
		!errors.Is(err, restoreErr) ||
		!errors.Is(err, buildErr) {
		t.Fatalf(
			"UpdateAsset = %v, want resolver, restore, and rebuild causes",
			err,
		)
	}
	if fatalErr == nil ||
		!errors.Is(fatalErr, domain.ErrInvalid) ||
		!errors.Is(fatalErr, restoreErr) ||
		!errors.Is(fatalErr, buildErr) {
		t.Fatalf(
			"fatal error = %v, want resolver, restore, and rebuild chains",
			fatalErr,
		)
	}
}

func TestLocalNode_CommittedAssetDeleteResolverFailureReconciles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	current := newFakeEngine()
	current.enforceResolver = true
	n := newAssetDeleteTestNode(t, current)
	deleted, err := n.CreateAsset(ctx, domain.Asset{Code: "GOLD"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAsset(GOLD): %v", err)
	}
	surviving, err := n.CreateAsset(
		ctx,
		domain.Asset{Code: "SILVER"},
		testCaller,
	)
	if err != nil {
		t.Fatalf("CreateAsset(SILVER): %v", err)
	}
	current.resolverMu.Lock()
	current.assetResolverIDs[deleted.Code] = deleted.EngineAssetID + 1
	current.resolverMu.Unlock()

	next := newFakeEngine()
	next.enforceResolver = true
	var snapshot engine.Snapshot
	n.build = fakeBuild(next, &snapshot)
	fatalCalls := 0
	n.fatal = func(error) { fatalCalls++ }
	err = n.DeleteAsset(ctx, deleted.Code, false, testCaller)
	if err == nil || !strings.Contains(err.Error(), "resolver removal failed:") {
		t.Fatalf("DeleteAsset = %v, want resolver removal failure", err)
	}
	if !strings.Contains(err.Error(), "engine resolver was reconciled") {
		t.Fatalf("DeleteAsset = %v, want successful reconciliation diagnostic", err)
	}
	if errors.Is(err, domain.ErrInvalid) ||
		errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("DeleteAsset = %v, must not classify as invalid input", err)
	}
	if fatalCalls != 0 {
		t.Fatalf("fatal hook calls = %d, want 0", fatalCalls)
	}
	if _, ok, getErr := n.realm.GetAsset(ctx, deleted.Code); getErr != nil || ok {
		t.Fatalf(
			"GetAsset after committed delete = ok %v, err %v, want absent",
			ok,
			getErr,
		)
	}
	if n.currentEngine() != next {
		t.Fatalf("current engine = %p, want reconciled engine %p", n.currentEngine(), next)
	}

	survivingInSnapshot := false
	deletedInSnapshot := false
	for _, asset := range snapshot.Assets {
		switch asset.Code {
		case surviving.Code:
			survivingInSnapshot = asset.EngineAssetID == surviving.EngineAssetID
		case deleted.Code:
			deletedInSnapshot = true
		}
	}
	if !survivingInSnapshot {
		t.Fatalf(
			"rebuild snapshot assets = %+v, want %s with engine id %d",
			snapshot.Assets,
			surviving.Code,
			surviving.EngineAssetID,
		)
	}
	if deletedInSnapshot {
		t.Fatalf("rebuild snapshot still contains deleted asset %q", deleted.Code)
	}

	next.resolverMu.RLock()
	survivingID, survivingExists := next.assetResolverIDs[surviving.Code]
	_, deletedExists := next.assetResolverIDs[deleted.Code]
	next.resolverMu.RUnlock()
	if !survivingExists || survivingID != surviving.EngineAssetID {
		t.Fatalf(
			"reconciled resolver %s = (%d, %v), want (%d, true)",
			surviving.Code,
			survivingID,
			survivingExists,
			surviving.EngineAssetID,
		)
	}
	if deletedExists {
		t.Fatalf("reconciled resolver still contains %q", deleted.Code)
	}
	if current.running {
		t.Fatal("previous engine still running after reconciliation")
	}
	rows, err := n.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	failureRecorded := false
	reconciledRecorded := false
	for _, row := range rows {
		if row.Action != domain.AuditActionDeleteAsset || row.Asset != deleted.Code {
			continue
		}
		switch row.Detail {
		case fmt.Sprintf(
			"delete asset %s "+
				"(resolver removal failed)",
			deleted.Code,
		):
			failureRecorded = true
		case fmt.Sprintf(
			"delete asset %s "+
				"(resolver removal failed, reconciled)",
			deleted.Code,
		):
			reconciledRecorded = true
		}
	}
	if !failureRecorded {
		t.Fatalf("audit rows = %+v, want initial resolver-removal-failed row for %s", rows, deleted.Code)
	}
	if !reconciledRecorded {
		t.Fatalf(
			"audit rows = %+v, want a corrected row recording the reconciled outcome for %s",
			rows,
			deleted.Code,
		)
	}
}

func TestLocalNode_CommittedAssetDeleteResolverFailureRebuildFailureFatals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	current := newFakeEngine()
	current.enforceResolver = true
	n := newAssetDeleteTestNode(t, current)
	deleted, err := n.CreateAsset(ctx, domain.Asset{Code: "GOLD"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	current.resolverMu.Lock()
	current.assetResolverIDs[deleted.Code] = deleted.EngineAssetID + 1
	current.resolverMu.Unlock()

	rebuildSentinel := errors.New("asset delete rebuild sentinel")
	rebuildErr := fmt.Errorf("asset delete rebuild failed: %w", rebuildSentinel)
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, rebuildErr
	}
	var fatalErr error
	fatalCalls := 0
	n.fatal = func(err error) {
		fatalCalls++
		fatalErr = err
	}

	err = n.DeleteAsset(ctx, deleted.Code, false, testCaller)
	if err == nil || !strings.Contains(err.Error(), "rebuild engine from current store:") {
		t.Fatalf("DeleteAsset = %v, want rebuild failure", err)
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("DeleteAsset = %v, must not classify as invalid input", err)
	}
	if !errors.Is(err, rebuildSentinel) {
		t.Fatalf("DeleteAsset = %v, want rebuild error chain", err)
	}
	if _, ok, getErr := n.realm.GetAsset(ctx, deleted.Code); getErr != nil || ok {
		t.Fatalf(
			"GetAsset after committed delete = ok %v, err %v, want absent",
			ok,
			getErr,
		)
	}
	if fatalCalls != 1 {
		t.Fatalf("fatal hook calls = %d, want 1", fatalCalls)
	}
	if fatalErr == nil {
		t.Fatal("asset delete reconciliation failure did not invoke the fatal hook")
	}
	if strings.Contains(fatalErr.Error(), "was reconciled") {
		t.Fatalf("fatal error = %q, must not claim successful reconciliation", fatalErr)
	}
	if !errors.Is(fatalErr, domain.ErrInvalid) {
		t.Fatalf("fatal error = %v, want resolver error chain", fatalErr)
	}
	if !errors.Is(fatalErr, rebuildSentinel) {
		t.Fatalf("fatal error = %v, want rebuild error chain", fatalErr)
	}

	rows, err := n.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	audited := false
	reconciledRecorded := false
	for _, row := range rows {
		if row.Action != domain.AuditActionDeleteAsset || row.Asset != deleted.Code {
			continue
		}
		switch row.Detail {
		case fmt.Sprintf(
			"delete asset %s "+
				"(resolver removal failed)",
			deleted.Code,
		):
			audited = true
		case fmt.Sprintf(
			"delete asset %s "+
				"(resolver removal failed, reconciled)",
			deleted.Code,
		):
			reconciledRecorded = true
		}
	}
	if !audited {
		t.Fatalf("audit rows = %+v, want committed delete of %s", rows, deleted.Code)
	}
	// The rebuild failed, so reconciliation never happened: a "reconciled" row
	// here would falsely claim the resolver was fixed up.
	if reconciledRecorded {
		t.Fatalf(
			"audit rows = %+v, must not contain a reconciled row when the rebuild failed for %s",
			rows,
			deleted.Code,
		)
	}
}

func TestLocalNode_CommittedAssetDeleteResolverFailureAuditFailureReconcilesBeforeFatal(
	t *testing.T,
) {
	t.Parallel()
	auditErr := fmt.Errorf(
		"resolver-failure delete asset audit failed: %w",
		domain.ErrInvalid,
	)
	st := newRealmWrapStore(newMemoryStore("resolver-failure-delete-asset-audit.db"), func(
		realm store.RealmStore,
	) store.RealmStore {
		return &failActionAuditRealm{
			RealmStore: realm,
			action:     domain.AuditActionDeleteAsset,
			err:        auditErr,
		}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	current := newFakeEngine()
	current.enforceResolver = true
	n := newTestNodeWithStore(t, st, current)
	deleted, err := n.CreateAsset(ctx, domain.Asset{Code: "GOLD"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAsset(GOLD): %v", err)
	}
	surviving, err := n.CreateAsset(
		ctx,
		domain.Asset{Code: "SILVER"},
		testCaller,
	)
	if err != nil {
		t.Fatalf("CreateAsset(SILVER): %v", err)
	}
	current.resolverMu.Lock()
	current.assetResolverIDs[deleted.Code] = deleted.EngineAssetID + 1
	current.resolverMu.Unlock()

	next := newFakeEngine()
	next.enforceResolver = true
	var rebuilt engine.Snapshot
	n.build = fakeBuild(next, &rebuilt)
	var fatalErr error
	fatalCalls := 0
	n.fatal = func(err error) {
		fatalCalls++
		fatalErr = err
	}

	err = n.DeleteAsset(ctx, deleted.Code, false, testCaller)
	if !errors.Is(err, auditErr) {
		t.Fatalf("DeleteAsset = %v, want audit failure", err)
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("DeleteAsset = %v, must not expose domain-classified failure", err)
	}
	if !strings.Contains(err.Error(), "resolver removal failed") {
		t.Fatalf("DeleteAsset = %v, want resolver failure chain", err)
	}
	if _, ok, getErr := n.realm.GetAsset(ctx, deleted.Code); getErr != nil || ok {
		t.Fatalf(
			"GetAsset after committed delete = ok %v, err %v, want absent",
			ok,
			getErr,
		)
	}
	if n.currentEngine() != next {
		t.Fatalf("current engine = %p, want reconciled engine %p", n.currentEngine(), next)
	}
	next.resolverMu.RLock()
	survivingID, survivingExists := next.assetResolverIDs[surviving.Code]
	_, deletedExists := next.assetResolverIDs[deleted.Code]
	next.resolverMu.RUnlock()
	if !survivingExists || survivingID != surviving.EngineAssetID {
		t.Fatalf(
			"reconciled resolver %s = (%d, %v), want (%d, true)",
			surviving.Code,
			survivingID,
			survivingExists,
			surviving.EngineAssetID,
		)
	}
	if deletedExists {
		t.Fatalf("reconciled resolver still contains %q", deleted.Code)
	}
	if current.running {
		t.Fatal("previous engine still running after reconciliation")
	}
	if fatalCalls != 1 {
		t.Fatalf("fatal hook calls = %d, want 1", fatalCalls)
	}
	if fatalErr == nil {
		t.Fatal("delete asset audit failure did not invoke the fatal hook")
	}
	if !strings.Contains(fatalErr.Error(), `operation="audit delete asset"`) ||
		!strings.Contains(fatalErr.Error(), "asset=GOLD") {
		t.Fatalf("fatal error = %q, want fatal-audit operation and asset", fatalErr)
	}
	if !errors.Is(fatalErr, domain.ErrInvalid) {
		t.Fatalf("fatal error = %v, want resolver failure chain", fatalErr)
	}
	if !errors.Is(fatalErr, auditErr) {
		t.Fatalf("fatal error = %v, want audit failure chain", fatalErr)
	}
}

// TestLocalNode_CommittedAssetDeleteResolverFailureReconciledAuditFailureIsReturned
// covers the second audit write on the resolver-failure-then-reconciled path:
// the store delete commits, resolver removal fails, the first ("resolver
// removal failed") row persists, reconciliation succeeds, and only the second
// ("... reconciled") row's write fails. Unlike
// TestLocalNode_CommittedAssetDeleteResolverFailureAuditFailureReconcilesBeforeFatal
// (which fails the first write and never reaches reconciliation's corrective
// row at all), this exercises the append that follows a successful
// reconciliation and returns its failure without invoking the fatal hook.
func TestLocalNode_CommittedAssetDeleteResolverFailureReconciledAuditFailureIsReturned(
	t *testing.T,
) {
	t.Parallel()
	auditErr := fmt.Errorf(
		"reconciled delete asset audit failed: %w",
		domain.ErrInvalid,
	)
	st := newRealmWrapStore(newMemoryStore("resolver-reconciled-delete-asset-audit.db"), func(
		realm store.RealmStore,
	) store.RealmStore {
		return &failNthActionAuditRealm{
			RealmStore: realm,
			action:     domain.AuditActionDeleteAsset,
			n:          2,
			err:        auditErr,
		}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	current := newFakeEngine()
	current.enforceResolver = true
	n := newTestNodeWithStore(t, st, current)
	deleted, err := n.CreateAsset(ctx, domain.Asset{Code: "GOLD"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAsset(GOLD): %v", err)
	}
	surviving, err := n.CreateAsset(
		ctx,
		domain.Asset{Code: "SILVER"},
		testCaller,
	)
	if err != nil {
		t.Fatalf("CreateAsset(SILVER): %v", err)
	}
	current.resolverMu.Lock()
	current.assetResolverIDs[deleted.Code] = deleted.EngineAssetID + 1
	current.resolverMu.Unlock()

	next := newFakeEngine()
	next.enforceResolver = true
	var rebuilt engine.Snapshot
	n.build = fakeBuild(next, &rebuilt)
	fatalCalls := 0
	n.fatal = func(error) {
		fatalCalls++
	}

	err = n.DeleteAsset(ctx, deleted.Code, false, testCaller)
	if err == nil ||
		!strings.Contains(err.Error(), "engine resolver was reconciled") {
		t.Fatalf("DeleteAsset = %v, want successful reconciliation diagnostic", err)
	}
	if !strings.Contains(err.Error(), "audit delete asset") ||
		!strings.Contains(err.Error(), deleted.Code) {
		t.Fatalf("DeleteAsset = %q, want returned audit operation and asset", err)
	}
	if !errors.Is(err, auditErr) {
		t.Fatalf("DeleteAsset = %v, want second-write audit failure chain", err)
	}
	if errors.Is(err, domain.ErrInvalid) ||
		errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("DeleteAsset = %v, must not expose domain-classified failure", err)
	}
	if _, ok, getErr := n.realm.GetAsset(ctx, deleted.Code); getErr != nil || ok {
		t.Fatalf(
			"GetAsset after committed delete = ok %v, err %v, want absent",
			ok,
			getErr,
		)
	}
	if n.currentEngine() != next {
		t.Fatalf("current engine = %p, want reconciled engine %p", n.currentEngine(), next)
	}
	next.resolverMu.RLock()
	survivingID, survivingExists := next.assetResolverIDs[surviving.Code]
	_, deletedExists := next.assetResolverIDs[deleted.Code]
	next.resolverMu.RUnlock()
	if !survivingExists || survivingID != surviving.EngineAssetID {
		t.Fatalf(
			"reconciled resolver %s = (%d, %v), want (%d, true)",
			surviving.Code,
			survivingID,
			survivingExists,
			surviving.EngineAssetID,
		)
	}
	if deletedExists {
		t.Fatalf("reconciled resolver still contains %q", deleted.Code)
	}
	if fatalCalls != 0 {
		t.Fatalf("fatal hook calls = %d, want 0", fatalCalls)
	}

	rows, err := n.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	failureRecorded := false
	reconciledRecorded := false
	for _, row := range rows {
		if row.Action != domain.AuditActionDeleteAsset || row.Asset != deleted.Code {
			continue
		}
		switch row.Detail {
		case fmt.Sprintf(
			"delete asset %s "+
				"(resolver removal failed)",
			deleted.Code,
		):
			failureRecorded = true
		case fmt.Sprintf(
			"delete asset %s "+
				"(resolver removal failed, reconciled)",
			deleted.Code,
		):
			reconciledRecorded = true
		}
	}
	if !failureRecorded {
		t.Fatalf(
			"audit rows = %+v, want the surviving first failure row for %s",
			rows,
			deleted.Code,
		)
	}
	// The second write is the one under test and it failed, so no corrected
	// row should exist - it would falsely claim the audit trail was fixed up.
	if reconciledRecorded {
		t.Fatalf(
			"audit rows = %+v, must not contain a reconciled row when the "+
				"second write failed for %s",
			rows,
			deleted.Code,
		)
	}
}

func TestLocalNode_CommittedAssetDeleteResolverAndAuditRebuildFailureFatalsOnce(
	t *testing.T,
) {
	t.Parallel()
	auditErr := errors.New("resolver-failure delete asset audit failed")
	st := newRealmWrapStore(newMemoryStore("resolver-audit-rebuild-failure.db"), func(
		realm store.RealmStore,
	) store.RealmStore {
		return &failActionAuditRealm{
			RealmStore: realm,
			action:     domain.AuditActionDeleteAsset,
			err:        auditErr,
		}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	current := newFakeEngine()
	current.enforceResolver = true
	n := newTestNodeWithStore(t, st, current)
	deleted, err := n.CreateAsset(ctx, domain.Asset{Code: "GOLD"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	current.resolverMu.Lock()
	current.assetResolverIDs[deleted.Code] = deleted.EngineAssetID + 1
	current.resolverMu.Unlock()

	rebuildSentinel := errors.New("asset delete audit rebuild sentinel")
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, fmt.Errorf("asset delete audit rebuild failed: %w", rebuildSentinel)
	}
	var fatalErr error
	fatalCalls := 0
	n.fatal = func(err error) {
		fatalCalls++
		fatalErr = err
	}

	err = n.DeleteAsset(ctx, deleted.Code, false, testCaller)
	if !errors.Is(err, auditErr) {
		t.Fatalf("DeleteAsset = %v, want audit failure", err)
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("DeleteAsset = %v, must not expose domain-classified failure", err)
	}
	if !errors.Is(err, rebuildSentinel) ||
		!strings.Contains(err.Error(), "resolver removal failed") {
		t.Fatalf(
			"DeleteAsset = %v, want resolver, audit, and rebuild chains",
			err,
		)
	}
	if _, ok, getErr := n.realm.GetAsset(ctx, deleted.Code); getErr != nil || ok {
		t.Fatalf(
			"GetAsset after committed delete = ok %v, err %v, want absent",
			ok,
			getErr,
		)
	}
	if fatalCalls != 1 {
		t.Fatalf("fatal hook calls = %d, want 1", fatalCalls)
	}
	if fatalErr == nil ||
		!errors.Is(fatalErr, domain.ErrInvalid) ||
		!errors.Is(fatalErr, auditErr) ||
		!errors.Is(fatalErr, rebuildSentinel) {
		t.Fatalf(
			"fatal error = %v, want resolver, audit, and rebuild chains",
			fatalErr,
		)
	}
	if !strings.Contains(fatalErr.Error(), `operation="audit delete asset"`) ||
		!strings.Contains(fatalErr.Error(), "asset=GOLD") {
		t.Fatalf("fatal error = %q, want fatal-audit operation and asset", fatalErr)
	}
}

func TestLocalNode_DeleteAssetAuditSurvivesRequestCancellation(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	base := newMemoryStore("delete-asset-cancellation.db")
	t.Cleanup(func() { _ = base.Close() })
	var probe *deleteCancellationProbeRealm
	st := newRealmWrapStore(base, func(realm store.RealmStore) store.RealmStore {
		probe = &deleteCancellationProbeRealm{RealmStore: realm}
		return probe
	})
	n := newTestNodeWithStore(t, st, eng)
	asset, err := n.CreateAsset(context.Background(), domain.Asset{Code: "GOLD"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.afterAssetResolverRemove = cancel
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	if err := n.DeleteAsset(ctx, asset.Code, false, testCaller); err != nil {
		t.Fatalf("DeleteAsset after request cancellation: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("asset resolver removal did not cancel the request context")
	}
	if fatalErr != nil {
		t.Fatalf("delete asset audit triggered fatal path: %v", fatalErr)
	}
	if probe.deleteCtxErr != nil || probe.auditCtxErr != nil {
		t.Fatalf(
			"durable delete contexts: delete=%v audit=%v",
			probe.deleteCtxErr,
			probe.auditCtxErr,
		)
	}
	rows, err := n.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, row := range rows {
		if row.Action == domain.AuditActionDeleteAsset &&
			row.Asset == "GOLD" &&
			row.Detail == "delete asset GOLD" {
			return
		}
	}
	t.Fatalf("audit rows = %+v, want deleted GOLD asset", rows)
}

func TestLocalNode_DeleteAssetAuditFailureFatals(t *testing.T) {
	t.Parallel()
	auditErr := errors.New("delete asset audit failed")
	st := newRealmWrapStore(newMemoryStore("delete-asset-audit.db"), func(
		realm store.RealmStore,
	) store.RealmStore {
		return &failActionAuditRealm{
			RealmStore: realm,
			action:     domain.AuditActionDeleteAsset,
			err:        auditErr,
		}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	n := newTestNodeWithStore(t, st, newFakeEngine())
	asset, err := n.CreateAsset(ctx, domain.Asset{Code: "GOLD"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }
	if err := n.DeleteAsset(ctx, asset.Code, false, testCaller); !errors.Is(err, auditErr) {
		t.Fatalf("DeleteAsset = %v, want audit failure", err)
	}
	if fatalErr == nil {
		t.Fatal("delete asset audit failure did not invoke the fatal hook")
	}
	if !strings.Contains(fatalErr.Error(), `operation="audit delete asset"`) ||
		!strings.Contains(fatalErr.Error(), "asset=GOLD") {
		t.Fatalf("fatal error = %q, want operation and asset", fatalErr)
	}
}

func TestLocalNode_DeleteAssetAllowsRecreationWithNewEngineID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n := newAssetDeleteTestNode(t, eng)

	deleted, err := n.CreateAsset(ctx, domain.Asset{Code: "GOLD"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	if err := n.DeleteAsset(ctx, deleted.Code, false, testCaller); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	recreated, err := n.CreateAsset(ctx, domain.Asset{Code: deleted.Code}, testCaller)
	if err != nil {
		t.Fatalf("recreate asset: %v", err)
	}
	if recreated.EngineAssetID == deleted.EngineAssetID {
		t.Fatalf(
			"recreated engine asset id = %d, want value distinct from %d",
			recreated.EngineAssetID, deleted.EngineAssetID,
		)
	}
}

func TestLocalNode_DeleteAssetAllowsOrderAutoCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n := newAssetDeleteTestNode(t, eng)
	const account domain.AccountID = "asset-autocreate-account"
	deleted, err := n.CreateAsset(ctx, domain.Asset{Code: "GOLD"}, testCaller)
	if err != nil {
		t.Fatalf("CreateAsset(GOLD): %v", err)
	}

	if _, err := n.CreateAccount(ctx, testAccount(account), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.DeleteAsset(ctx, deleted.Code, false, testCaller); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}

	order, err := n.SubmitOrder(ctx, testKey(account), domain.Order{
		BaseAsset: "GOLD", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "1",
	}, domain.MissingAccountReject, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("order status = %q, want committed", order.Status)
	}
	recreated, ok, err := n.realm.GetAsset(ctx, deleted.Code)
	if err != nil || !ok {
		t.Fatalf("GetAsset(GOLD) = %+v, ok=%v, err=%v", recreated, ok, err)
	}
	if recreated.EngineAssetID == deleted.EngineAssetID {
		t.Fatalf(
			"auto-created engine asset id = %d, want value distinct from %d",
			recreated.EngineAssetID, deleted.EngineAssetID,
		)
	}
}

func TestLocalNode_RefusedAssetDeleteDoesNotTouchLiveResolver(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	eng.enforceResolver = true
	base := newMemoryStore("refused-asset-delete.db")
	t.Cleanup(func() { _ = base.Close() })
	release := make(chan struct{})
	var blocking *blockingDeleteAssetRealm
	st := newRealmWrapStore(base, func(realm store.RealmStore) store.RealmStore {
		blocking = &blockingDeleteAssetRealm{
			RealmStore: realm,
			entered:    make(chan struct{}),
			release:    release,
		}
		return blocking
	})
	n := newTestNodeWithStore(t, st, eng)
	const (
		account domain.AccountID = "asset-delete-refusal-account"
		asset   string           = "EUR"
	)

	if _, err := n.CreateAccount(ctx, testAccount(account), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.realm.UpsertBalance(ctx, domain.Balance{
		Account: account, Asset: asset, Available: "1",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	eng.resolverMu.RLock()
	wantID, resolverExists := eng.assetResolverIDs[asset]
	eng.resolverMu.RUnlock()
	if !resolverExists {
		t.Fatalf("asset resolver %q missing before delete", asset)
	}

	deleteResult := make(chan error, 1)
	deleteDone := make(chan struct{})
	go func() {
		defer close(deleteDone)
		deleteResult <- n.DeleteAsset(ctx, asset, false, testCaller)
	}()
	released := false
	defer func() {
		if !released {
			close(release)
		}
		select {
		case <-deleteDone:
		case <-time.After(5 * time.Second):
			t.Errorf("DeleteAsset goroutine did not stop")
		}
	}()

	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("DeleteAsset did not enter the store")
	}
	eng.resolverMu.RLock()
	gotID, resolverExists := eng.assetResolverIDs[asset]
	eng.resolverMu.RUnlock()
	if !resolverExists || gotID != wantID {
		t.Fatalf(
			"asset resolver while store refuses delete = (%d, %v), want (%d, true)",
			gotID,
			resolverExists,
			wantID,
		)
	}

	close(release)
	released = true
	var err error
	select {
	case err = <-deleteResult:
	case <-time.After(5 * time.Second):
		t.Fatal("DeleteAsset did not return after store release")
	}
	if !errors.Is(err, domain.ErrHasDependents) ||
		!strings.Contains(err.Error(), "delete asset:") {
		t.Fatalf("DeleteAsset = %v, want wrapped ErrHasDependents", err)
	}
	eng.resolverMu.RLock()
	gotID, resolverExists = eng.assetResolverIDs[asset]
	eng.resolverMu.RUnlock()
	if !resolverExists || gotID != wantID {
		t.Fatalf(
			"asset resolver after refused delete = (%d, %v), want (%d, true)",
			gotID,
			resolverExists,
			wantID,
		)
	}
}

func TestLocalNode_RefusedAssetDeleteKeepsConcurrentQuoteConsistent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	eng.enforceResolver = true
	sink := &assetDeleteResolverSink{
		engine:  eng,
		results: make(chan assetDeleteSinkResult, 4),
	}
	eng.sink = sink
	base := newMemoryStore("asset-delete-quote-race.db")
	t.Cleanup(func() { _ = base.Close() })
	release := make(chan struct{})
	var blocking *blockingDeleteAssetRealm
	st := newRealmWrapStore(base, func(realm store.RealmStore) store.RealmStore {
		blocking = &blockingDeleteAssetRealm{
			RealmStore: realm,
			entered:    make(chan struct{}),
			release:    release,
		}
		return blocking
	})
	n := newTestNodeWithStore(t, st, eng)

	const provider = "asset-delete-race"
	instance, err := n.realm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: provider,
		Label:    "asset delete race",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	baseAsset, ok, err := n.realm.GetAsset(ctx, "EUR")
	if err != nil || !ok {
		t.Fatalf("GetAsset(EUR): ok=%v err=%v", ok, err)
	}
	quoteAsset, ok, err := n.realm.GetAsset(ctx, "USD")
	if err != nil || !ok {
		t.Fatalf("GetAsset(USD): ok=%v err=%v", ok, err)
	}
	instrument := domain.MarketDataInstrument{
		Instance:       instance.ExternalID,
		ExternalSymbol: "EURUSD",
		BaseAsset:      baseAsset.Code,
		QuoteAsset:     quoteAsset.Code,
		BaseAssetID:    baseAsset.EngineAssetID,
		QuoteAssetID:   quoteAsset.EngineAssetID,
		Enabled:        true,
	}
	if err := n.realm.UpsertMarketDataInstrument(ctx, instrument); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	connector := &assetDeleteQuoteConnector{
		updates: make(chan marketdata.QuoteUpdate, 1),
	}
	registry := marketdata.NewRegistry()
	if err := registry.Register(marketdata.Provider{
		Type: provider,
		Build: func(domain.MarketDataInstance) (marketdata.Connector, error) {
			return connector, nil
		},
	}); err != nil {
		t.Fatalf("Register provider: %v", err)
	}
	manager, err := marketdata.NewManager(
		registry,
		n.realm,
		n.CurrentMarketDataSink(),
		nil,
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start market-data manager: %v", err)
	}
	defer manager.Stop()

	deleteResult := make(chan error, 1)
	deleteDone := make(chan struct{})
	go func() {
		defer close(deleteDone)
		deleteResult <- n.DeleteAsset(ctx, instrument.BaseAsset, false, testCaller)
	}()
	released := false
	defer func() {
		if !released {
			close(release)
		}
		select {
		case <-deleteDone:
		case <-time.After(5 * time.Second):
			t.Errorf("DeleteAsset goroutine did not stop")
		}
	}()

	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("DeleteAsset did not enter the store")
	}
	update := marketdata.QuoteUpdate{
		AsOf: time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC),
		Base: instrument.BaseAssetID, Quote: instrument.QuoteAssetID,
		Mark: "1.25",
	}
	quoteQueued := make(chan struct{})
	go func() {
		connector.updates <- update
		close(quoteQueued)
	}()
	select {
	case <-quoteQueued:
	case <-time.After(5 * time.Second):
		t.Fatal("quote publisher did not enqueue the update")
	}

	var sinkResult assetDeleteSinkResult
	select {
	case sinkResult = <-sink.results:
	case <-time.After(5 * time.Second):
		t.Fatal("live sink did not receive the quote")
	}
	if sinkResult.err != nil {
		t.Fatalf("live sink push failed: %v", sinkResult.err)
	}
	if sinkResult.update != update {
		t.Fatalf("engine quote = %+v, want %+v", sinkResult.update, update)
	}
	quotes := manager.QuoteSnapshots()
	if len(quotes) != 1 {
		t.Fatalf("manager quote snapshots = %+v, want one quote", quotes)
	}
	accepted := quotes[0]
	if !accepted.AsOf.Equal(update.AsOf) ||
		accepted.ExternalSymbol != instrument.ExternalSymbol ||
		accepted.Mark != update.Mark {
		t.Fatalf(
			"accepted quote = %+v, engine quote = %+v",
			accepted,
			sinkResult.update,
		)
	}

	close(release)
	released = true
	select {
	case err = <-deleteResult:
	case <-time.After(5 * time.Second):
		t.Fatal("DeleteAsset did not return after store release")
	}
	if !errors.Is(err, domain.ErrHasDependents) ||
		!strings.Contains(err.Error(), "delete asset:") {
		t.Fatalf("DeleteAsset = %v, want wrapped ErrHasDependents", err)
	}
}
