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

// These tests exercise reservation settlement against a real OpenPit engine and
// so require the native runtime dylib at run time (set
// OPENPIT_RUNTIME_LIBRARY_PATH or build the workspace dylib first, as documented
// for the Go bindings). They build one real engine through buildEngine, seed an
// account balance, and drive order and execution-report paths through the adapter.

package native

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/openpit"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pretrade/policies"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/internal/store/sqlite"
)

const (
	testAccount   = "1"
	testQuote     = "USD"
	testBase      = "AAPL"
	testQuoteFund = "1000"
	testQty       = "5"
	testLimit     = "100" // 5 * 100 = 500 quote, leaves 500 after one hold
)

// testOrderXID returns a deterministic non-zero external id for a reservation
// test order, standing in for the store-assigned handle so the intent carries a
// real order ref. seed is folded into the bytes so distinct orders differ.
func testOrderXID(seed byte) domain.ExternalID {
	var raw [domain.ExternalIDByteLen]byte
	raw[0] = seed
	raw[domain.ExternalIDByteLen-1] = 0x5a
	id, err := domain.ExternalIDFromBytes(raw[:])
	if err != nil {
		panic(err)
	}
	return id
}

func testAsyncEngine(t *testing.T, eng *openpit.Engine) *asyncengine.AsyncEngine {
	t.Helper()
	async, err := asyncengine.NewBuilder(eng).
		WithStopUnderlying(eng.Stop).
		Dynamic().
		Build()
	if err != nil {
		t.Fatalf("build async engine: %v", err)
	}
	return async
}

// newTestEngine builds a real engine adapter with one account ("1") that carries
// a stored engine id and is seeded with testQuoteFund of the quote asset, enough
// to reserve exactly two test orders. The adapter is stopped via t.Cleanup.
func newTestEngine(t *testing.T) *openPitEngine {
	t.Helper()
	snap := Snapshot{Accounts: []domain.Account{account(testAccount)}}
	res, err := newIDResolver(snap.Accounts, snap.Groups)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	eng, service, registered, _, err := buildEngine(snap, res)
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	adapter := newOpenPitEngine(
		eng, testAsyncEngine(t, eng), service, registered, nil, res,
	).(*openPitEngine)
	t.Cleanup(adapter.Stop)

	if err := seedBalances(eng, []domain.Balance{{
		Account:   domain.AccountID(testAccount),
		Asset:     testQuote,
		Available: testQuoteFund,
	}}, res); err != nil {
		t.Fatalf("seed balance: %v", err)
	}
	return adapter
}

// newUnpricedTestEngine builds the adapter with validation only, deliberately
// omitting SpotFunds and every other policy that could contribute a lock price.
func newUnpricedTestEngine(t *testing.T) *openPitEngine {
	t.Helper()
	snap := Snapshot{Accounts: []domain.Account{account(testAccount)}}
	res, err := newIDResolver(snap.Accounts, snap.Groups)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	eng, err := openpit.NewEngineBuilder().AccountSync().
		Builtin(policies.BuildOrderValidation()).
		Build()
	if err != nil {
		t.Fatalf("build unpriced engine: %v", err)
	}
	adapter := newOpenPitEngine(
		eng,
		testAsyncEngine(t, eng),
		nil,
		map[string]struct{}{},
		nil,
		res,
	).(*openPitEngine)
	t.Cleanup(adapter.Stop)
	return adapter
}

func TestOpenPitEngineDictionaryResolverMutationsKeepHandleAndSink(t *testing.T) {
	e := newTestEngine(t)
	var resolver engine.DictionaryResolver = e
	engineBefore := e.eng
	asyncBefore := e.async
	sinkBefore := e.MarketDataSink()

	account := domain.Account{Code: "added-account", EngineAccountID: 42}
	if err := resolver.AddAccountResolverEntry(account); err != nil {
		t.Fatalf("AddAccountResolverEntry: %v", err)
	}
	accountLaneRan := false
	if err := e.RunAccountSynchronized(
		context.Background(), account.Code, func(engine.AccountLane) error {
			accountLaneRan = true
			return nil
		},
	); err != nil {
		t.Fatalf("RunAccountSynchronized after add: %v", err)
	}
	if !accountLaneRan {
		t.Fatal("account resolver entry was not published before lane submission")
	}

	group := domain.AccountGroup{Code: "added-group", EngineGroupID: 43}
	if err := resolver.AddGroupResolverEntry(group); err != nil {
		t.Fatalf("AddGroupResolverEntry: %v", err)
	}
	if err := e.RunGroupSynchronized(
		context.Background(), group.Code, func(engine.GroupLane) error { return nil },
	); err != nil {
		t.Fatalf("RunGroupSynchronized after add: %v", err)
	}

	renamedAccount := account
	renamedAccount.Code = "renamed-account"
	if err := resolver.RenameAccountResolverEntry(account.Code, renamedAccount); err != nil {
		t.Fatalf("RenameAccountResolverEntry: %v", err)
	}
	if err := e.RunAccountSynchronized(
		context.Background(), renamedAccount.Code, func(engine.AccountLane) error { return nil },
	); err != nil {
		t.Fatalf("RunAccountSynchronized after rename: %v", err)
	}
	if err := e.RunAccountSynchronized(
		context.Background(), account.Code, func(engine.AccountLane) error { return nil },
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("old account alias error = %v, want ErrInvalid", err)
	}

	renamedGroup := group
	renamedGroup.Code = "renamed-group"
	if err := resolver.RenameGroupResolverEntry(group.Code, renamedGroup); err != nil {
		t.Fatalf("RenameGroupResolverEntry: %v", err)
	}
	if err := e.RunGroupSynchronized(
		context.Background(), renamedGroup.Code, func(engine.GroupLane) error { return nil },
	); err != nil {
		t.Fatalf("RunGroupSynchronized after rename: %v", err)
	}
	if err := e.RunGroupSynchronized(
		context.Background(), group.Code, func(engine.GroupLane) error { return nil },
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("old group alias error = %v, want ErrInvalid", err)
	}
	if err := resolver.RemoveGroupResolverEntry(renamedGroup); err != nil {
		t.Fatalf("RemoveGroupResolverEntry: %v", err)
	}
	if err := e.RunGroupSynchronized(
		context.Background(), renamedGroup.Code, func(engine.GroupLane) error { return nil },
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("removed group alias error = %v, want ErrInvalid", err)
	}

	if e.eng != engineBefore || e.async != asyncBefore {
		t.Fatal("resolver mutation replaced the engine handle or async dispatcher")
	}
	if got := e.MarketDataSink(); got != sinkBefore {
		t.Fatal("resolver mutation replaced MarketDataSink")
	}
}

func TestGroupLaneCurrencyNamedAndDefaultKeepHandleAndSink(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	engineBefore := e.eng
	asyncBefore := e.async
	sinkBefore := e.MarketDataSink()

	group := domain.AccountGroup{Code: "currency-group", EngineGroupID: 44}
	if err := e.AddGroupResolverEntry(group); err != nil {
		t.Fatalf("AddGroupResolverEntry: %v", err)
	}
	for _, alias := range []string{group.Code, ""} {
		if err := e.RunGroupSynchronized(
			ctx, alias, func(lane engine.GroupLane) error {
				if err := lane.SetGroupCurrency(ctx, alias, "USD"); err != nil {
					return err
				}
				return lane.ClearGroupCurrency(ctx, alias)
			},
		); err != nil {
			t.Fatalf("set and clear group currency %q: %v", alias, err)
		}
	}

	if e.eng != engineBefore || e.async != asyncBefore {
		t.Fatal("group currency mutation replaced engine handle or async dispatcher")
	}
	if got := e.MarketDataSink(); got != sinkBefore {
		t.Fatal("group currency mutation replaced MarketDataSink")
	}
}

func TestQueuedAccountLaneKeepsRoutedIDAcrossAliasReuse(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	oldAlias := domain.AccountID(testAccount)
	routedID, err := e.res.account(oldAlias)
	if err != nil {
		t.Fatalf("resolve routed account: %v", err)
	}
	lane := accountLane{
		owner: e, eng: e.eng, accountAlias: oldAlias, accountID: routedID,
	}

	started := make(chan struct{})
	release := make(chan struct{})
	first := e.async.Submit(ctx, routedID, func() error {
		close(started)
		<-release
		return nil
	})
	<-started
	queued := e.async.Submit(ctx, routedID, func() error {
		return lane.BlockAccount(ctx, oldAlias, "queued block")
	})

	renamed := domain.Account{Code: "renamed-account", EngineAccountID: 1}
	if err := e.RenameAccountResolverEntry(oldAlias, renamed); err != nil {
		t.Fatalf("rename account resolver entry: %v", err)
	}
	const reusedEngineID domain.EngineAccountID = 42
	if err := e.AddAccountResolverEntry(domain.Account{
		Code: oldAlias, EngineAccountID: reusedEngineID,
	}); err != nil {
		t.Fatalf("reuse old account alias: %v", err)
	}
	close(release)
	if _, err := first.Await(ctx); err != nil {
		t.Fatalf("first account lane: %v", err)
	}
	if _, err := queued.Await(ctx); err != nil {
		t.Fatalf("queued account lane: %v", err)
	}

	if err := e.eng.Accounts().ReplaceBlockReason(routedID, "routed"); err != nil {
		t.Fatalf("queued work did not block routed account id: %v", err)
	}
	reusedID := param.NewAccountIDFromUint64(reusedEngineID.Uint64())
	if err := e.eng.Accounts().ReplaceBlockReason(reusedID, "reused"); err == nil {
		t.Fatal("queued work was redirected to the reused account alias")
	}
	if err := e.RunAccountSynchronized(
		ctx, renamed.Code, func(lane engine.AccountLane) error {
			return lane.UnblockAccount(ctx, renamed.Code)
		},
	); err != nil {
		t.Fatalf("new work through renamed account alias: %v", err)
	}
	if err := e.eng.Accounts().ReplaceBlockReason(routedID, "still blocked"); err == nil {
		t.Fatal("renamed alias did not route new work to the stable account id")
	}
}

func TestQueuedGroupLaneKeepsRoutedIDAcrossAliasReuse(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	oldAlias := "group-old"
	group := domain.AccountGroup{Code: oldAlias, EngineGroupID: 9}
	if err := e.AddGroupResolverEntry(group); err != nil {
		t.Fatalf("add group resolver entry: %v", err)
	}
	routedID, err := e.res.group(oldAlias)
	if err != nil {
		t.Fatalf("resolve routed group: %v", err)
	}
	lane := groupLane{
		owner: e, eng: e.eng,
		accountIDs: e.res.accountIDSnapshot(),
		groupAlias: oldAlias,
		groupID:    routedID,
	}
	routingKey := groupRoutingKey(routedID)

	started := make(chan struct{})
	release := make(chan struct{})
	first := e.async.Submit(ctx, routingKey, func() error {
		close(started)
		<-release
		return nil
	})
	<-started
	queued := e.async.Submit(ctx, routingKey, func() error {
		capturedID, err := lane.routedGroupID(oldAlias)
		if err != nil {
			return err
		}
		if capturedID.Handle() != routedID.Handle() {
			return errors.New("queued group lane lost its captured numeric id")
		}
		if err := lane.SetGroupCurrency(ctx, oldAlias, "EUR"); err != nil {
			return err
		}
		if err := lane.RegisterGroup(
			ctx, []domain.AccountID{testAccount}, oldAlias,
		); err != nil {
			return err
		}
		return lane.BlockGroup(ctx, oldAlias, "queued block")
	})

	renamedAccount := domain.Account{Code: "account-new", EngineAccountID: 1}
	if err := e.RenameAccountResolverEntry(testAccount, renamedAccount); err != nil {
		t.Fatalf("rename account resolver entry: %v", err)
	}
	if err := e.AddAccountResolverEntry(domain.Account{
		Code: testAccount, EngineAccountID: 42,
	}); err != nil {
		t.Fatalf("reuse old account alias: %v", err)
	}
	renamed := group
	renamed.Code = "group-new"
	if err := e.RenameGroupResolverEntry(oldAlias, renamed); err != nil {
		t.Fatalf("rename group resolver entry: %v", err)
	}
	const reusedEngineID domain.EngineGroupID = 10
	if err := e.AddGroupResolverEntry(domain.AccountGroup{
		Code: oldAlias, EngineGroupID: reusedEngineID,
	}); err != nil {
		t.Fatalf("reuse old group alias: %v", err)
	}
	close(release)
	if _, err := first.Await(ctx); err != nil {
		t.Fatalf("first group lane: %v", err)
	}
	if _, err := queued.Await(ctx); err != nil {
		t.Fatalf("queued group lane: %v", err)
	}

	if err := e.eng.Accounts().ReplaceGroupBlockReason(routedID, "routed"); err != nil {
		t.Fatalf("queued work did not block routed group id: %v", err)
	}
	reusedID, err := param.NewAccountGroupIDFromUint32(reusedEngineID.Uint32())
	if err != nil {
		t.Fatalf("reused group id: %v", err)
	}
	if err := e.eng.Accounts().ReplaceGroupBlockReason(reusedID, "reused"); err == nil {
		t.Fatal("queued work was redirected to the reused group alias")
	}
	order := testOrder()
	order.Account = renamedAccount.Code
	result, err := e.SubmitOrder(ctx, order)
	if err != nil {
		t.Fatalf("submit order through renamed account alias: %v", err)
	}
	if result.Accepted {
		t.Fatal("queued group registration was redirected to the reused account alias")
	}
	if err := e.RunGroupSynchronized(
		ctx, renamed.Code, func(lane engine.GroupLane) error {
			if err := lane.UnregisterGroup(
				ctx, []domain.AccountID{renamedAccount.Code}, renamed.Code,
			); err != nil {
				return err
			}
			return lane.UnblockGroup(ctx, renamed.Code)
		},
	); err != nil {
		t.Fatalf("new work through renamed group alias: %v", err)
	}
	if err := e.eng.Accounts().ReplaceGroupBlockReason(routedID, "still blocked"); err == nil {
		t.Fatal("renamed alias did not route new work to the stable group id")
	}
}

func TestRunGroupSynchronizedWaitsForCallbackAfterCallerCancellation(t *testing.T) {
	e := newTestEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	callbackEntered := make(chan struct{})
	callbackRelease := make(chan struct{})
	result := make(chan error, 1)
	callbackErr := errors.New("group callback result")

	go func() {
		result <- e.RunGroupSynchronized(
			ctx, "", func(engine.GroupLane) error {
				close(callbackEntered)
				<-callbackRelease
				return callbackErr
			},
		)
	}()
	<-callbackEntered
	cancel()

	select {
	case err := <-result:
		t.Fatalf("RunGroupSynchronized returned before callback completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(callbackRelease)
	select {
	case err := <-result:
		if !errors.Is(err, callbackErr) {
			t.Fatalf("RunGroupSynchronized error = %v, want callback result", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunGroupSynchronized did not return after callback completed")
	}
}

func TestAccountLaneSetAccountPnlState(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	sinkBefore := e.MarketDataSink()
	engineBefore := e.eng

	var numericBlocks []domain.AccountBlock
	if err := e.RunAccountSynchronized(
		ctx, testAccount, func(lane engine.AccountLane) error {
			var err error
			numericBlocks, err = lane.SetAccountPnlState(ctx, testAccount, "2.5", "")
			return err
		},
	); err != nil {
		t.Fatalf("numeric SetAccountPnlState: %v", err)
	}
	if len(numericBlocks) != 0 {
		t.Fatalf("numeric SetAccountPnlState blocks = %v, want none", numericBlocks)
	}

	var haltedBlocks []domain.AccountBlock
	if err := e.RunAccountSynchronized(
		ctx, testAccount, func(lane engine.AccountLane) error {
			var err error
			haltedBlocks, err = lane.SetAccountPnlState(
				ctx, testAccount, "", domain.PnlHaltReasonMissingFx,
			)
			return err
		},
	); err != nil {
		t.Fatalf("halted SetAccountPnlState: %v", err)
	}
	if len(haltedBlocks) != 0 {
		t.Fatalf("halted SetAccountPnlState blocks = %+v, want none", haltedBlocks)
	}

	for _, test := range []struct {
		name       string
		pnl        string
		haltReason domain.PnlHaltReason
	}{
		{name: "both", pnl: "1", haltReason: domain.PnlHaltReasonMissingFx},
		{name: "empty"},
		{name: "unknown reason", haltReason: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := e.RunAccountSynchronized(
				ctx, testAccount, func(lane engine.AccountLane) error {
					_, err := lane.SetAccountPnlState(
						ctx, testAccount, test.pnl, test.haltReason,
					)
					return err
				},
			)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("SetAccountPnlState error = %v, want ErrInvalid", err)
			}
		})
	}
	if e.eng != engineBefore || e.MarketDataSink() != sinkBefore {
		t.Fatal("SetAccountPnlState replaced the engine or MarketDataSink")
	}
}

// testOrder is a limit buy that costs 500 quote, so a 1000-quote balance funds
// exactly two of them. A limit price keeps the estimate source "limit" and
// avoids needing a live market quote.
func testOrder() domain.Order {
	return domain.Order{
		Account:     domain.AccountID(testAccount),
		BaseAsset:   testBase,
		QuoteAsset:  testQuote,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: testQty,
		Price:       testLimit,
	}
}

func TestSubmitImmediate_NetsHeldToZero(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	res, err := e.SubmitImmediate(ctx, testOrder())
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("SubmitImmediate rejected: %+v", res.Rejects)
	}
	if res.EstimateSource != domain.EstimateSourceLimit {
		t.Fatalf("estimate source = %q, want %q", res.EstimateSource, domain.EstimateSourceLimit)
	}
	if res.SettlementLockPrice == "" {
		t.Fatal("SubmitImmediate: empty settlement lock price")
	}
	if len(res.Lock) == 0 {
		t.Fatal("SubmitImmediate: empty serialized lock")
	}
	seen := make(map[string]struct{}, len(res.Outcomes))
	var quote *domain.AdjustmentOutcomeAccepted
	for i := range res.Outcomes {
		outcome := &res.Outcomes[i]
		if _, duplicate := seen[outcome.Asset]; duplicate {
			t.Fatalf("SubmitImmediate returned duplicate final asset %q: %+v",
				outcome.Asset, res.Outcomes)
		}
		seen[outcome.Asset] = struct{}{}
		if outcome.Asset == testQuote {
			quote = &outcome.Outcome
		}
	}
	if quote == nil || quote.HeldDelta != "0" || quote.HeldResult != "0" {
		t.Fatalf("quote outcome = %+v, want reservation and settlement held effects netted to zero",
			quote)
	}
	if res.AccountPnl != "" || res.AccountPnlHaltReason != "" {
		t.Fatalf(
			"account pnl outcome = (%q, %q), want no opening-fill P&L outcome",
			res.AccountPnl,
			res.AccountPnlHaltReason,
		)
	}
}

func TestSubmitImmediate_DropCopySettlesWhileAccountIsBlocked(t *testing.T) {
	acct := blockedAccount(testAccount, "account block")
	acct.Currency = testQuote
	eng, err := BuildOpenPitEngine("", Snapshot{Accounts: []domain.Account{acct}})
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	e := eng.(*openPitEngine)
	t.Cleanup(e.Stop)

	order := testOrder()
	order.DropCopy = true
	result, err := e.SubmitImmediate(context.Background(), order)
	if err != nil {
		t.Fatalf("SubmitImmediate(drop copy): %v", err)
	}
	if !result.Accepted || len(result.Rejects) != 0 {
		t.Fatalf("drop-copy immediate result = %+v, want accepted", result)
	}
	if result.FillQuantity != testQty {
		t.Fatalf("fill quantity = %q, want %q", result.FillQuantity, testQty)
	}
}

func TestSubmitOrder_DropCopyMarketOrderPropagatesNativeInputError(t *testing.T) {
	e := newTestEngine(t)
	order := testOrder()
	order.DropCopy = true
	order.Price = ""

	_, err := e.SubmitOrder(context.Background(), order)
	if err == nil {
		t.Fatal("SubmitOrder(drop-copy market): want admission error")
	}
	if !strings.Contains(err.Error(), "limit price") {
		t.Fatalf("SubmitOrder(drop-copy market) error = %v", err)
	}
}

func TestSubmitImmediate_SellCarriesReservationBaseBalance(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(t.TempDir() + "/officer.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	n, _, err := node.NewLocalNode(
		ctx,
		store,
		func(snapshot engine.Snapshot) (engine.Engine, error) {
			return BuildOpenPitEngine("", snapshot)
		},
	)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	accountID := domain.AccountID("my3")
	caller := domain.Caller{
		Source:    domain.SourcePanel,
		Principal: domain.PrincipalOperator,
	}
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code:     accountID,
		Currency: "USDT",
	}, caller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := n.ApplyAdjustment(
		ctx,
		node.Key{Account: accountID},
		testOrderXID(0x01),
		domain.AdjustmentRequest{
			Asset:             "USDT",
			AverageEntryPrice: "1",
			RealizedPnl:       "0",
			Balance: &domain.AdjustmentAmount{
				Mode:  domain.AdjustmentModeAbsolute,
				Value: "1000000",
			},
		},
		domain.MissingAccountCreate,
		caller,
	); err != nil {
		t.Fatalf("ApplyAdjustment: %v", err)
	}

	buy := domain.Order{
		Account:     accountID,
		BaseAsset:   "BTC",
		QuoteAsset:  "USDT",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       "73406.8115",
	}
	if _, result, err := n.SubmitImmediate(ctx, node.Key{Account: accountID}, buy, domain.MissingAccountCreate, caller); err != nil || !result.Accepted {
		t.Fatalf("buy SubmitImmediate: %v result=%+v", err, result)
	}

	sell := buy
	sell.Side = domain.OrderSideSell
	sell.Price = "54268.522"
	_, result, err := n.SubmitImmediate(ctx, node.Key{Account: accountID}, sell, domain.MissingAccountCreate, caller)
	if err != nil || !result.Accepted {
		t.Fatalf("sell SubmitImmediate: %v result=%+v", err, result)
	}
	balance, ok, err := n.GetBalance(ctx, accountID, "BTC")
	if err != nil || !ok {
		t.Fatalf("GetBalance BTC: ok=%v err=%v", ok, err)
	}
	if balance.Available != "0" || balance.Held != "0" || balance.Incoming != "0" {
		t.Fatalf("BTC balance = %+v, want no open position", balance)
	}
	if balance.AverageEntryPrice != "" {
		t.Fatalf("BTC average entry price = %q, want empty", balance.AverageEntryPrice)
	}
	if balance.RealizedPnl != "-19138.2895" {
		t.Fatalf("BTC realized PnL = %q, want -19138.2895", balance.RealizedPnl)
	}
}

func TestSubmitImmediate_OpeningFillDoesNotEmitNoopAccountPnl(t *testing.T) {
	acct := account(testAccount)
	acct.Currency = testQuote
	acct.EffectiveCurrency = testQuote
	acct.Pnl = "7.25"
	engine, err := BuildOpenPitEngine("", Snapshot{
		Accounts: []domain.Account{acct},
		Balances: []domain.Balance{{
			Account: domain.AccountID(testAccount), Asset: testQuote,
			Available: testQuoteFund,
		}},
	})
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	e := engine.(*openPitEngine)
	t.Cleanup(e.Stop)

	res, err := e.SubmitImmediate(context.Background(), testOrder())
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("SubmitImmediate rejected: %+v", res.Rejects)
	}
	if res.AccountPnl != "" || res.AccountPnlHaltReason != "" {
		t.Fatalf(
			"account pnl outcome = (%q, %q), want no opening-fill P&L outcome",
			res.AccountPnl,
			res.AccountPnlHaltReason,
		)
	}
}

// TestApplyExecutionReport_SettlesFillNoBlock drives a fill end to end through
// the real engine and the real executionReportFrom mapping: submit a spot BUY
// (holding quote funds), then apply a fill report carrying an explicit leaves
// quantity. It asserts the report settles with no account
// block and produces a per-asset outcome for each spot leg. This locks in that
// the mapper sets leaves quantity and terminal order status; without them the engine
// rejects the fill with missing_required_field and blocks the account.
func TestApplyExecutionReport_SettlesFillNoBlock(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	submitted, err := e.SubmitOrder(ctx, testOrder())
	if err != nil || !submitted.Accepted {
		t.Fatalf("SubmitOrder: %v accepted=%v", err, submitted.Accepted)
	}

	// A full fill of the 5-unit order at the reservation's settlement lock price
	// nets the held quote to zero: leaves is 0 and the fill is final.
	result, err := e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		FillQuantity:   testQty,
		FillPrice:      submitted.SettlementLockPrice,
		LeavesQuantity: "0",
		LockPrice:      submitted.SettlementLockPrice,
		Account:        domain.AccountID(testAccount),
		Side:           domain.OrderSideBuy,
		OrderStatus:    domain.OrderStatusFilled,
	})
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if len(result.Blocks) != 0 {
		t.Fatalf("fill must not block the account, got blocks=%+v", result.Blocks)
	}
	// Both spot legs settle: the base (AAPL) and the quote (USD).
	outcomes := map[string]domain.AdjustmentOutcomeAccepted{}
	for _, o := range result.Outcomes {
		outcomes[o.Asset] = o.Outcome
	}
	if _, ok := outcomes[testBase]; !ok {
		t.Fatalf("want outcomes for %s and %s, got %+v", testBase, testQuote, result.Outcomes)
	}
	if _, ok := outcomes[testQuote]; !ok {
		t.Fatalf("want outcomes for %s and %s, got %+v", testBase, testQuote, result.Outcomes)
	}
	if base := outcomes[testBase]; base.BalanceDelta != testQty || base.BalanceResult != testQty {
		t.Fatalf("base outcome = %+v, want balance +%s result %s", base, testQty, testQty)
	}
	if quote := outcomes[testQuote]; quote.BalanceDelta != "" ||
		quote.HeldDelta != "-500" || quote.HeldResult != "0" {
		t.Fatalf("quote outcome = %+v, want held-only release without balance double count", quote)
	}
}

// TestApplyExecutionReport_UsesSeededRealizedPnlFromSDK reproduces a complete
// position close. Officer seeds average price and realized PnL into OpenPit,
// forwards the fill, and exposes the SDK outcome unchanged. Commission mapping
// is covered independently so this test does not assume how SDK versions assign
// fees to PnL.
func TestApplyExecutionReport_UsesSeededRealizedPnlFromSDK(t *testing.T) {
	acct := account(testAccount)
	acct.Currency = testQuote
	engine, err := BuildOpenPitEngine("", Snapshot{
		Accounts: []domain.Account{acct},
		Balances: []domain.Balance{
			{
				Account: domain.AccountID(testAccount), Asset: testBase,
				Available: "1", AverageEntryPrice: "99000", RealizedPnl: "7",
			},
			{
				Account: domain.AccountID(testAccount), Asset: testQuote,
				Available: "1000000", RealizedPnl: "0",
			},
		},
	})
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	e := engine.(*openPitEngine)
	t.Cleanup(e.Stop)

	ctx := context.Background()
	order := domain.Order{
		Account: domain.AccountID(testAccount), BaseAsset: testBase, QuoteAsset: testQuote,
		Side: domain.OrderSideSell, AmountKind: domain.OrderAmountKindQuantity,
		AmountValue: "1", Price: "50000",
	}
	submitted, err := e.SubmitOrder(ctx, order)
	if err != nil || !submitted.Accepted {
		t.Fatalf("SubmitOrder: %v accepted=%v rejects=%+v", err, submitted.Accepted, submitted.Rejects)
	}

	result, err := e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset: testBase, QuoteAsset: testQuote,
		FillQuantity: "1", FillPrice: "50000", LeavesQuantity: "0",
		LockPrice: submitted.SettlementLockPrice,
		Account:   domain.AccountID(testAccount), Side: domain.OrderSideSell,
		OrderStatus: domain.OrderStatusFilled,
	})
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	var base *domain.AdjustmentOutcomeAccepted
	for i := range result.Outcomes {
		if result.Outcomes[i].Asset == testBase {
			base = &result.Outcomes[i].Outcome
			break
		}
	}
	if base == nil {
		t.Fatalf("base outcome missing: %+v", result.Outcomes)
	}
	if base.RealizedPnlDelta != "-49000" || base.RealizedPnlResult != "-48993" {
		t.Fatalf("base PnL outcome = %+v, want SDK delta -49000 absolute -48993", base)
	}
}

func TestBuildOpenPitEngine_SeedsAccountPnlWithoutPnlBounds(t *testing.T) {
	acct := account(testAccount)
	acct.Currency = testQuote
	acct.EffectiveCurrency = testQuote
	acct.Pnl = "7.25"
	eng, err := BuildOpenPitEngine("", Snapshot{Accounts: []domain.Account{acct}})
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	eng.Stop()
}

func TestBuildOpenPitEngine_PersistedPnlKeepsBoundsClear(t *testing.T) {
	acct := account(testAccount)
	acct.Currency = testQuote
	acct.Pnl = "5"
	limits := []domain.LimitSpotFundsPnlBounds{{
		Scope:      domain.ScopeAccount,
		Account:    testAccount,
		LowerBound: "-3",
	}}
	built, err := BuildOpenPitEngine("", Snapshot{
		Accounts: []domain.Account{acct}, SpotFundsPnlBoundsLimits: limits,
	})
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	eng := built.(*openPitEngine)
	t.Cleanup(eng.Stop)
	if blocks := eng.SeedAccountBlocks(); len(blocks) != 0 {
		t.Fatalf("seed blocks = %+v, want persisted +5 to stay clear of -3", blocks)
	}
	result, err := eng.ConfigurePolicy(
		context.Background(),
		domain.PolicySpotFundsPnlBoundsKillSwitch,
		LimitSet{SpotFundsPnlBoundsLimits: limits},
	)
	if err != nil {
		t.Fatalf("ConfigurePolicy unchanged after restart: %v", err)
	}
	if len(result.AccountBlocks) != 0 {
		t.Fatalf("unchanged restart config blocks = %+v, want none", result.AccountBlocks)
	}
}

func TestSpotFundsAccountPnlSeeds_RestoresHaltedAccountPnl(t *testing.T) {
	acct := account(testAccount)
	acct.Currency = testQuote
	acct.EffectiveCurrency = testQuote
	acct.Pnl = "not-a-number"
	acct.PnlHaltReason = domain.PnlHaltReasonMissingFx
	seeds, err := spotFundsAccountPnlSeeds(
		[]domain.Account{acct, {Code: "no-override"}},
		testResolver(testAccount, "no-override"),
	)
	if err != nil {
		t.Fatalf("spotFundsAccountPnlSeeds: %v", err)
	}
	if len(seeds) != 1 {
		t.Fatalf("seed count = %d, want 1 halted database state", len(seeds))
	}
	reason, halted := seeds[0].state.HaltReason()
	if !halted || reason != model.PnlHaltReasonMissingFx {
		t.Fatalf("seed state = (%v, %v), want restored missing-fx halt", reason, halted)
	}
	if seeds[0].account.Handle() != 1 {
		t.Fatalf("seed account = %d, want %d", seeds[0].account.Handle(), 1)
	}
}

func TestSpotFundsAccountPnlSeeds_RestoresPersistedNumericAccountPnl(t *testing.T) {
	acct := account(testAccount)
	acct.Pnl = "7.25"
	seeds, err := spotFundsAccountPnlSeeds(
		[]domain.Account{acct}, testResolver(testAccount),
	)
	if err != nil {
		t.Fatalf("spotFundsAccountPnlSeeds: %v", err)
	}
	if len(seeds) != 1 {
		t.Fatalf("seed count = %d, want one authoritative seed", len(seeds))
	}
	amount, ok := seeds[0].state.Value()
	if !ok || amount.String() != "7.25" {
		t.Fatalf("seed state = %+v, want persisted 7.25", seeds[0].state)
	}
}

func TestApplyExecutionReport_CanceledContextDoesNotEnterLane(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	submitted, err := e.SubmitOrder(ctx, testOrder())
	if err != nil || !submitted.Accepted {
		t.Fatalf("SubmitOrder: %v accepted=%v", err, submitted.Accepted)
	}

	reportCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.ApplyExecutionReport(reportCtx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		FillQuantity:   testQty,
		FillPrice:      submitted.SettlementLockPrice,
		LeavesQuantity: "0",
		LockPrice:      submitted.SettlementLockPrice,
		Account:        domain.AccountID(testAccount),
		Side:           domain.OrderSideBuy,
		Order:          "order-1",
		OrderStatus:    domain.OrderStatusFilled,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ApplyExecutionReport with canceled context = %v, want context canceled", err)
	}
}

func TestApplyExecutionReport_NoTradeFinalReleasesReservation(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	submitted, err := e.SubmitOrder(ctx, testOrder())
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if !submitted.Accepted {
		t.Fatalf("SubmitOrder rejected: %+v", submitted.Rejects)
	}

	result, err := e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		LeavesQuantity: testQty,
		Lock:           submitted.Lock,
		Account:        domain.AccountID(testAccount),
		Side:           domain.OrderSideBuy,
		OrderStatus:    domain.OrderStatusCancelled,
	})
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if len(result.Blocks) != 0 {
		t.Fatalf("cancel must not block the account, got blocks=%+v", result.Blocks)
	}

	outcomes := map[string]domain.AdjustmentOutcomeAccepted{}
	for _, outcome := range result.Outcomes {
		outcomes[outcome.Asset] = outcome.Outcome
	}
	base := outcomes[testBase]
	if base.IncomingDelta != "" && base.IncomingDelta != "-"+testQty {
		t.Fatalf("base incoming delta = %q, want empty or -%s", base.IncomingDelta, testQty)
	}
	quote := outcomes[testQuote]
	if quote.BalanceDelta != "500" || quote.HeldDelta != "-500" {
		t.Fatalf("quote outcome = %+v, want available +500 held -500", quote)
	}
}

// TestApplyExecutionReport_MissingLeavesRejected proves the mapper treats an
// empty leaves quantity as caller error: the report is rejected with
// domain.ErrInvalid before it reaches the engine, with no account block.
func TestApplyExecutionReport_MissingLeavesRejected(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	submitted, err := e.SubmitOrder(ctx, testOrder())
	if err != nil || !submitted.Accepted {
		t.Fatalf("SubmitOrder: %v accepted=%v", err, submitted.Accepted)
	}

	_, err = e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:    testBase,
		QuoteAsset:   testQuote,
		FillQuantity: testQty,
		FillPrice:    submitted.SettlementLockPrice,
		LockPrice:    submitted.SettlementLockPrice,
		Account:      domain.AccountID(testAccount),
		Side:         domain.OrderSideBuy,
		OrderStatus:  domain.OrderStatusFilled,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ApplyExecutionReport(empty leaves) = %v, want ErrInvalid", err)
	}
}

func newTestEngineWithSpotFundsPnlBounds(t *testing.T) *openPitEngine {
	t.Helper()
	acct := account(testAccount)
	acct.Currency = testQuote
	acct.GroupCode = "desk-a"
	acct.Pnl = "-5"
	snap := Snapshot{
		Accounts: []domain.Account{acct},
		Groups: []domain.AccountGroup{{
			Code:          "desk-a",
			EngineGroupID: 7,
			Currency:      testQuote,
		}},
		Balances: []domain.Balance{
			{
				Account:   domain.AccountID(testAccount),
				Asset:     testQuote,
				Available: "2000",
			},
			{
				Account:   domain.AccountID(testAccount),
				Asset:     testBase,
				Available: "10",
			},
		},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			{
				Scope:      domain.ScopeGlobal,
				LowerBound: "-1000000",
			},
			{
				Scope:        domain.ScopeAccountGroup,
				AccountGroup: "desk-a",
				LowerBound:   "-1000000",
			},
			{
				Scope:      domain.ScopeAccount,
				Account:    domain.AccountID(testAccount),
				LowerBound: "-6",
			},
		},
	}
	engine, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	adapter := engine.(*openPitEngine)
	t.Cleanup(adapter.Stop)
	return adapter
}

func TestSpotFundsPnlBoundsBuildConfiguresBasePolicyAndAccountPnl(t *testing.T) {
	e := newTestEngineWithSpotFundsPnlBounds(t)
	ctx := context.Background()

	if _, ok := e.registered[nameSpotFunds]; !ok || len(e.registered) != 1 {
		t.Fatalf("registered policies = %+v, want only %s", e.registered, nameSpotFunds)
	}
	if err := e.sink.Push(marketdata.QuoteUpdate{
		Base: testBase, Quote: testQuote, Mark: "100",
	}); err != nil {
		t.Fatalf("Push quote: %v", err)
	}
	market := testOrder()
	market.Price = ""
	market.AmountValue = "1"
	if result, err := e.SubmitOrder(ctx, market); err != nil || !result.Accepted {
		t.Fatalf("market SubmitOrder: err=%v result=%+v", err, result)
	}

	feeOrder := testOrder()
	feeOrder.AmountValue = "1"
	submitted, err := e.SubmitOrder(ctx, feeOrder)
	if err != nil {
		t.Fatalf("SubmitOrder fee order: %v", err)
	}
	if !submitted.Accepted {
		t.Fatalf("fee order rejected before fill: %+v", submitted.Rejects)
	}

	result, err := e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		FillQuantity:   "1",
		FillPrice:      submitted.SettlementLockPrice,
		LeavesQuantity: "0",
		LockPrice:      submitted.SettlementLockPrice,
		Commission:     &domain.Commission{Amount: "-2", Currency: testQuote},
		Account:        domain.AccountID(testAccount),
		Side:           domain.OrderSideBuy,
		OrderStatus:    domain.OrderStatusFilled,
	})
	if err != nil {
		t.Fatalf("ApplyExecutionReport fee order: %v", err)
	}
	if len(result.Blocks) == 0 {
		t.Fatalf("fee fill did not block account; outcomes=%+v", result.Outcomes)
	}
	if result.Blocks[0].Account != domain.AccountID(testAccount) {
		t.Fatalf("block account = %q, want %q", result.Blocks[0].Account, testAccount)
	}
}

func TestConfigurePolicy_SpotFundsPnlBoundsClearsLastBarrierOnline(t *testing.T) {
	e := newTestEngineWithSpotFundsPnlBounds(t)
	ctx := context.Background()

	engBefore := e.eng
	if _, err := e.ConfigurePolicy(
		ctx,
		domain.PolicySpotFundsPnlBoundsKillSwitch,
		LimitSet{},
	); err != nil {
		t.Fatalf("ConfigurePolicy clear spot funds pnl bounds: %v", err)
	}
	if e.eng != engBefore {
		t.Fatal("ConfigurePolicy replaced engine handle, want online reconfigure")
	}

	feeOrder := testOrder()
	feeOrder.AmountValue = "1"
	submitted, err := e.SubmitOrder(ctx, feeOrder)
	if err != nil {
		t.Fatalf("SubmitOrder fee order: %v", err)
	}
	if !submitted.Accepted {
		t.Fatalf("fee order rejected before fill: %+v", submitted.Rejects)
	}

	result, err := e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		FillQuantity:   "1",
		FillPrice:      submitted.SettlementLockPrice,
		LeavesQuantity: "0",
		LockPrice:      submitted.SettlementLockPrice,
		Commission:     &domain.Commission{Amount: "-2", Currency: testQuote},
		Account:        domain.AccountID(testAccount),
		Side:           domain.OrderSideBuy,
		OrderStatus:    domain.OrderStatusFilled,
	})
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if len(result.Blocks) != 0 {
		t.Fatalf("cleared spot funds bound still blocked fill: %+v", result.Blocks)
	}
}

// newTestEngineGlobalSpotFundsPnlBounds builds a real engine whose SpotFunds P&L
// bounds carry only permissive global and account-group barriers - no
// account-scope barrier. A global or group barrier is enough for the policy to
// accumulate per-account P&L, so an account-scope barrier introduced later at
// runtime observes whatever P&L has already accrued.
func newTestEngineGlobalSpotFundsPnlBounds(t *testing.T) *openPitEngine {
	t.Helper()
	acct := account(testAccount)
	acct.Currency = testQuote
	acct.GroupCode = "desk-a"
	snap := Snapshot{
		Accounts: []domain.Account{acct},
		Groups: []domain.AccountGroup{{
			Code:          "desk-a",
			EngineGroupID: 7,
			Currency:      testQuote,
		}},
		Balances: []domain.Balance{
			{
				Account:   domain.AccountID(testAccount),
				Asset:     testQuote,
				Available: "2000",
			},
			{
				Account:   domain.AccountID(testAccount),
				Asset:     testBase,
				Available: "10",
			},
		},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			{
				Scope:      domain.ScopeGlobal,
				LowerBound: "-1000000",
			},
			{
				Scope:        domain.ScopeAccountGroup,
				AccountGroup: "desk-a",
				LowerBound:   "-1000000",
			},
		},
	}
	engine, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	adapter := engine.(*openPitEngine)
	t.Cleanup(adapter.Stop)
	return adapter
}

// commitSpotFundsFeeFill runs one buy fill of the base asset carrying a -2 quote
// commission, so the SpotFunds policy accrues -2 of realized account-currency
// P&L. It returns the execution result so a caller can assert the kill-switch
// block state.
func commitSpotFundsFeeFill(t *testing.T, e *openPitEngine) ExecutionReportResult {
	t.Helper()
	ctx := context.Background()
	order := testOrder()
	order.AmountValue = "1"
	submitted, err := e.SubmitOrder(ctx, order)
	if err != nil {
		t.Fatalf("SubmitOrder fee order: %v", err)
	}
	if !submitted.Accepted {
		t.Fatalf("fee order rejected before fill: %+v", submitted.Rejects)
	}
	result, err := e.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		FillQuantity:   "1",
		FillPrice:      submitted.SettlementLockPrice,
		LeavesQuantity: "0",
		LockPrice:      submitted.SettlementLockPrice,
		Commission:     &domain.Commission{Amount: "-2", Currency: testQuote},
		Account:        domain.AccountID(testAccount),
		Side:           domain.OrderSideBuy,
		OrderStatus:    domain.OrderStatusFilled,
	})
	if err != nil {
		t.Fatalf("ApplyExecutionReport fee order: %v", err)
	}
	return result
}

// TestConfigurePolicy_SpotFundsPnlBoundsNewAccountBarrierPreservesLivePnl proves
// that adding an account-scope barrier at runtime does not reset the account's
// live accumulated P&L: the barrier arms against P&L already accrued rather
// than starting from zero.
func TestConfigurePolicy_SpotFundsPnlBoundsNewAccountBarrierPreservesLivePnl(t *testing.T) {
	e := newTestEngineGlobalSpotFundsPnlBounds(t)
	ctx := context.Background()

	// The first fill accrues -2 of account P&L under the permissive global/group
	// barriers; nothing breaches yet.
	if first := commitSpotFundsFeeFill(t, e); len(first.Blocks) != 0 {
		t.Fatalf("first fill blocked unexpectedly: %+v", first.Blocks)
	}

	// Introduce an account-scope barrier. It must not reset the -2 already
	// accrued, so its -3 lower bound stays armed against live P&L.
	limits := LimitSet{SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
		{
			Scope:      domain.ScopeGlobal,
			LowerBound: "-1000000",
		},
		{
			Scope:        domain.ScopeAccountGroup,
			AccountGroup: "desk-a",
			LowerBound:   "-1000000",
		},
		{
			Scope:      domain.ScopeAccount,
			Account:    domain.AccountID(testAccount),
			LowerBound: "-3",
		},
	}}
	if _, err := e.ConfigurePolicy(
		ctx, domain.PolicySpotFundsPnlBoundsKillSwitch, limits,
	); err != nil {
		t.Fatalf("ConfigurePolicy add account barrier: %v", err)
	}

	// The second fill drives accrued P&L to -4, breaching the -3 bound. A reset to
	// zero would leave it at -2 and pass, so a block proves the live P&L survived.
	second := commitSpotFundsFeeFill(t, e)
	if len(second.Blocks) == 0 {
		t.Fatal("account barrier did not block on preserved live P&L; " +
			"ConfigurePolicy reset the accumulator")
	}
	if second.Blocks[0].Account != domain.AccountID(testAccount) {
		t.Fatalf("block account = %q, want %q", second.Blocks[0].Account, testAccount)
	}
}

// TestSubmitOrder_AcceptCapturesSettlement checks SubmitOrder returns the
// durable lock and the canonical settlement inputs a later report needs.
func TestSubmitOrder_AcceptCapturesSettlement(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	res, err := e.SubmitOrder(ctx, testOrder())
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("SubmitOrder rejected: %+v", res.Rejects)
	}
	if len(res.Lock) == 0 {
		t.Fatal("SubmitOrder: empty serialized lock on accept")
	}
	prices, err := LockDisplayPrices(res.Lock)
	if err != nil {
		t.Fatalf("LockDisplayPrices: %v", err)
	}
	if len(prices) == 0 {
		t.Fatal("serialized lock carries no prices")
	}
	if res.EstimateSource != domain.EstimateSourceLimit {
		t.Fatalf("estimate source = %q, want limit", res.EstimateSource)
	}
	if res.SettlementLockPrice == "" {
		t.Fatal("SubmitOrder: empty settlement lock price")
	}
	gotSettlement, err := param.NewPriceFromString(res.SettlementLockPrice)
	if err != nil {
		t.Fatalf("parse settlement lock price: %v", err)
	}
	wantSettlement, err := param.NewPriceFromString(prices[len(prices)-1])
	if err != nil {
		t.Fatalf("parse serialized settlement lock price: %v", err)
	}
	if gotSettlement.Compare(wantSettlement) != 0 {
		t.Fatalf(
			"settlement lock price = %s, want serialized lock price %s",
			gotSettlement.String(), wantSettlement.String(),
		)
	}
	if res.LeavesQuantity != testQty {
		t.Fatalf("leaves quantity = %q, want %q", res.LeavesQuantity, testQty)
	}
}

func TestSubmitOrder_DropCopyIgnoresBlocksAndKeepsNegativeAvailable(t *testing.T) {
	acct := blockedAccount(testAccount, "account block")
	acct.Currency = testQuote
	acct.GroupCode = "desk-a"
	eng, err := BuildOpenPitEngine("", Snapshot{
		Accounts: []domain.Account{acct},
		Groups: []domain.AccountGroup{{
			Code: "desk-a", EngineGroupID: 7,
			Blocked: true, BlockReason: "group block",
		}},
	})
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	e := eng.(*openPitEngine)
	t.Cleanup(e.Stop)

	order := testOrder()
	order.DropCopy = true
	result, err := e.SubmitOrder(context.Background(), order)
	if err != nil {
		t.Fatalf("SubmitOrder(drop copy): %v", err)
	}
	if !result.Accepted || len(result.Rejects) != 0 {
		t.Fatalf("drop-copy result = %+v, want accepted without rejects", result)
	}
	var quote *domain.AdjustmentOutcomeAccepted
	for i := range result.Outcomes {
		if result.Outcomes[i].Asset == testQuote {
			quote = &result.Outcomes[i].Outcome
			break
		}
	}
	if quote == nil {
		t.Fatalf("quote outcome missing: %+v", result.Outcomes)
	}
	if quote.BalanceResult != "-500" || quote.HeldResult != "500" {
		t.Fatalf(
			"quote outcome = %+v, want available -500 and held 500",
			quote,
		)
	}

	ordinary := testOrder()
	ordinaryResult, err := e.SubmitOrder(context.Background(), ordinary)
	if err != nil {
		t.Fatalf("SubmitOrder(ordinary): %v", err)
	}
	if ordinaryResult.Accepted || len(ordinaryResult.Rejects) == 0 {
		t.Fatalf("ordinary blocked order = %+v, want reject", ordinaryResult)
	}
}

// A drop-copy order never reports policy rejects, but an account block a
// policy derives while it runs must still reach the caller so Officer can
// mirror it. The halted PnL state already latches the block when it is set, so
// this pins the surfacing, not the moment of latching.
func TestSubmitOrder_DropCopySurfacesHaltedPnlAccountBlock(t *testing.T) {
	// A PnL barrier must be configured for a halted PnL state to matter.
	e := newTestEngineWithSpotFundsPnlBounds(t)
	ctx := context.Background()
	var setupBlocks []domain.AccountBlock
	if err := e.RunAccountSynchronized(
		ctx, testAccount, func(lane engine.AccountLane) error {
			var err error
			setupBlocks, err = lane.SetAccountPnlState(
				ctx, testAccount, "", domain.PnlHaltReasonMissingFx,
			)
			return err
		},
	); err != nil {
		t.Fatalf("SetAccountPnlState: %v", err)
	}
	// Recorded precondition: the halt assignment itself latches the block, so
	// the block seen below cannot be attributed to the drop-copy order alone.
	if len(setupBlocks) != 1 {
		t.Fatalf("setup blocks = %+v, want exactly one", setupBlocks)
	}

	order := testOrder()
	order.DropCopy = true
	result, err := e.SubmitOrder(ctx, order)
	if err != nil {
		t.Fatalf("SubmitOrder(drop copy): %v", err)
	}
	if !result.Accepted || len(result.Rejects) != 0 {
		t.Fatalf("drop-copy result = %+v, want accepted without rejects", result)
	}
	if len(result.Blocks) != 1 {
		t.Fatalf("drop-copy blocks = %+v, want exactly one", result.Blocks)
	}
	block := result.Blocks[0]
	if block.Account != domain.AccountID(testAccount) {
		t.Fatalf("block account = %q, want %q", block.Account, testAccount)
	}
	if block.Code != "account_blocked" {
		t.Fatalf("block code = %q, want account_blocked", block.Code)
	}
	if block.Policy == "" || block.Reason == "" {
		t.Fatalf("block = %+v, want policy and reason", block)
	}
}

func TestSubmitOrder_DropCopyVolumeCapturesCanonicalBaseLeaves(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	order := testOrder()
	order.DropCopy = true
	order.AmountKind = domain.OrderAmountKindVolume
	order.AmountValue = "500.00"

	res, err := e.SubmitOrder(ctx, order)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("SubmitOrder rejected: %+v", res.Rejects)
	}
	if res.SettlementLockPrice == "" {
		t.Fatal("SubmitOrder: empty settlement lock price")
	}
	gotLeaves, err := param.NewQuantityFromString(res.LeavesQuantity)
	if err != nil {
		t.Fatalf("parse base leaves quantity: %v", err)
	}
	wantLeaves, _ := param.NewQuantityFromString("5")
	if gotLeaves.Compare(wantLeaves) != 0 {
		t.Fatalf("base leaves quantity = %q, want 5", res.LeavesQuantity)
	}
}

func TestSubmitOrder_UnpricedVolumeReturnsRecordedReject(t *testing.T) {
	e := newUnpricedTestEngine(t)
	order := testOrder()
	order.AmountKind = domain.OrderAmountKindVolume
	order.AmountValue = "500"
	order.Price = ""

	res, err := e.SubmitOrder(context.Background(), order)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	assertUnpricedVolumeReject(t, res.Accepted, res.Rejects)
}

func TestSubmitImmediate_UnpricedVolumeReturnsRecordedReject(t *testing.T) {
	e := newUnpricedTestEngine(t)
	order := testOrder()
	order.AmountKind = domain.OrderAmountKindVolume
	order.AmountValue = "500"
	order.Price = ""

	res, err := e.SubmitImmediate(context.Background(), order)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	assertUnpricedVolumeReject(t, res.Accepted, res.Rejects)
}

func assertUnpricedVolumeReject(
	t *testing.T, accepted bool, rejects []domain.OrderReject,
) {
	t.Helper()
	if accepted {
		t.Fatal("unpriced volume order accepted")
	}
	if len(rejects) != 1 {
		t.Fatalf("rejects = %+v, want one", rejects)
	}
	reject := rejects[0]
	if reject.Code != "order_value_calculation_failed" ||
		reject.Scope != "order" ||
		reject.Reason != "volume order requires a settlement price" {
		t.Fatalf("reject = %+v, want order sizing reject", reject)
	}
}

func TestImmediateExecutionReport_VolumeSizing(t *testing.T) {
	res := testResolver(testAccount)
	quantity, err := immediateFillQuantity(domain.Order{
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "5.00",
	}, "")
	if err != nil {
		t.Fatalf("immediateFillQuantity(quantity): %v", err)
	}
	if quantity != "5.00" {
		t.Fatalf("immediateFillQuantity(quantity) = %q, want 5.00", quantity)
	}

	// A volume order with no settlement price cannot be sized into a fill.
	_, err = immediateExecutionReport(domain.Order{
		Account:     domain.AccountID(testAccount),
		BaseAsset:   testBase,
		QuoteAsset:  testQuote,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindVolume,
		AmountValue: "500",
	}, "", res)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("immediateExecutionReport(volume, no price) = %v, want ErrInvalid", err)
	}

	// With a settlement price the volume sizes to base quantity (500/100 = 5).
	if _, err := immediateExecutionReport(domain.Order{
		Account:     domain.AccountID(testAccount),
		BaseAsset:   testBase,
		QuoteAsset:  testQuote,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindVolume,
		AmountValue: "500",
	}, "100", res); err != nil {
		t.Fatalf("immediateExecutionReport(volume): %v", err)
	}

	qty, err := immediateFillQuantity(domain.Order{
		AmountKind:  domain.OrderAmountKindVolume,
		AmountValue: "500",
	}, "100")
	if err != nil {
		t.Fatalf("immediateFillQuantity: %v", err)
	}
	got, err := param.NewQuantityFromString(qty)
	if err != nil {
		t.Fatalf("parse fill quantity %q: %v", qty, err)
	}
	want, _ := param.NewQuantityFromString("5")
	if got.Compare(want) != 0 {
		t.Fatalf("immediateFillQuantity = %q, want 5", qty)
	}
}

// TestRunAccountSynchronized_SerializesSameAccount drives the real SDK account
// lane (not a fake global mutex): two goroutines submit work for the same
// account through RunAccountSynchronized concurrently, and the test asserts the
// lane never runs both callbacks at once. A callback that observed a sibling
// already inside the lane would flip overlap; the lane's per-account ordering
// keeps the increments race-free without any additional lock in the callback.
func TestRunAccountSynchronized_SerializesSameAccount(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	const goroutines = 8
	const iterations = 50

	var (
		active  int
		overlap bool
		counter int
		guard   sync.Mutex
	)
	// The callback intentionally reads-modifies-writes counter without a lock: if
	// the lane serializes, no data race occurs and the final value is exact.
	work := func(AccountLane) error {
		guard.Lock()
		active++
		if active > 1 {
			overlap = true
		}
		guard.Unlock()

		v := counter
		time.Sleep(time.Millisecond)
		counter = v + 1

		guard.Lock()
		active--
		guard.Unlock()
		return nil
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if err := e.RunAccountSynchronized(
					ctx, domain.AccountID(testAccount), work,
				); err != nil {
					t.Errorf("RunAccountSynchronized: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if overlap {
		t.Fatal("account lane ran two callbacks concurrently for one account")
	}
	if want := goroutines * iterations; counter != want {
		t.Fatalf("counter = %d, want %d (lane did not serialize increments)", counter, want)
	}
}

func TestRunAccountSynchronized_CancellationWaitsForStartedCallback(t *testing.T) {
	e := newTestEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- e.RunAccountSynchronized(ctx, testAccount, func(AccountLane) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		t.Fatalf("RunAccountSynchronized returned while callback was running: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RunAccountSynchronized after callback completion: %v", err)
	}
}

func TestRunAccountSynchronized_CancellationWaitsForQueuedCallback(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- e.RunAccountSynchronized(ctx, testAccount, func(AccountLane) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	queuedCtx, cancel := context.WithCancel(ctx)
	callStarted := make(chan struct{})
	callbackStarted := make(chan struct{})
	queuedDone := make(chan error, 1)
	go func() {
		close(callStarted)
		queuedDone <- e.RunAccountSynchronized(
			queuedCtx, testAccount, func(AccountLane) error {
				close(callbackStarted)
				return nil
			},
		)
	}()
	<-callStarted
	// The first callback keeps the account worker occupied while the second call
	// reaches AsyncEngine.Submit and waits in its queue.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-queuedDone:
		t.Fatalf("queued callback released its caller gate on cancellation: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first RunAccountSynchronized: %v", err)
	}
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("accepted queued callback did not run")
	}
	if err := <-queuedDone; err != nil {
		t.Fatalf("queued RunAccountSynchronized after callback completion: %v", err)
	}
}

// TestRunAccountSynchronized_RejectsUnknownAccount proves the real lane resolves
// the account before entering the callback: an account the resolver does not
// know rejects with domain.ErrInvalid and the callback never runs, which is the
// seam the node's pre-lane auto-create/rebuild exists to satisfy.
func TestRunAccountSynchronized_RejectsUnknownAccount(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	ran := false
	err := e.RunAccountSynchronized(ctx, "unknown-account", func(AccountLane) error {
		ran = true
		return nil
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RunAccountSynchronized(unknown) error = %v, want ErrInvalid", err)
	}
	if ran {
		t.Fatal("callback ran for an account the resolver does not know")
	}
}
