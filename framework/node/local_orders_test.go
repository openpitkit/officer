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
	"hash/fnv"
	"maps"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	openpit "go.openpit.dev/openpit"
	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/accounts"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/configure"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/pkg/optional"
	"go.openpit.dev/openpit/pretrade"
	"go.openpit.dev/openpit/pretrade/policies"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

var fakeOrderAsyncEngines sync.Map

type fakeOrderAsyncRuntime struct {
	async  *asyncengine.AsyncEngine
	driver asyncengine.Driver
}

type fakeOrderChainDriver struct {
	owner   *fakeEngine
	admin   asyncengine.Driver
	trading asyncengine.Driver
}

type rejectWithoutReasonAfterReservationEngine struct {
	*fakeEngine
}

func (e *rejectWithoutReasonAfterReservationEngine) ReservedOrder(
	order domain.Order, _ asyncengine.OperationResult,
) (engine.OrderResult, error) {
	if err := e.recordChainSubmit(order); err != nil {
		return engine.OrderResult{}, err
	}
	return engine.OrderResult{Accepted: false}, nil
}

type persistedAccountMismatchRealm struct {
	store.RealmStore
}

func (s *persistedAccountMismatchRealm) RecordOrderSubmission(
	ctx context.Context,
	o domain.Order,
	submitted domain.OrderEvent,
	apply func(domain.Order) (domain.OrderSettlement, error),
) (domain.Order, error) {
	return s.RealmStore.RecordOrderSubmission(
		ctx,
		o,
		submitted,
		func(persisted domain.Order) (domain.OrderSettlement, error) {
			persisted.Account = "acc-2"
			return apply(persisted)
		},
	)
}

func newPersistedAccountMismatchNode(
	t *testing.T,
) (*localNode, *fakeEngine) {
	t.Helper()
	st := newRealmWrapStore(
		newMemoryStore("persisted-account-mismatch.db"),
		func(realm store.RealmStore) store.RealmStore {
			return &persistedAccountMismatchRealm{RealmStore: realm}
		},
	)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := newFakeEngine()
	return newTestNodeWithStore(t, st, eng, failOnFatal(t)), eng
}

func persistedAccountMismatchOrder(dropCopy bool) domain.Order {
	return domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
		DropCopy:    dropCopy,
	}
}

func fakeStoredLock(t *testing.T) []byte {
	t.Helper()
	payload, err := pretrade.NewLock().MarshalMsgpack()
	if err != nil {
		t.Fatalf("marshal fake stored lock: %v", err)
	}
	return payload
}

func (e *fakeEngine) AsyncEngine() *asyncengine.AsyncEngine {
	if existing, ok := fakeOrderAsyncEngines.Load(e); ok {
		return existing.(*fakeOrderAsyncRuntime).async
	}
	admin, err := openpit.NewEngineBuilder().
		AccountSync().
		Builtin(policies.BuildOrderValidation()).
		Builtin(policies.BuildSpotFunds()).
		Build()
	if err != nil {
		panic(fmt.Sprintf("build fake administrative engine: %v", err))
	}
	trading, err := openpit.NewEngineBuilder().
		AccountSync().
		Builtin(policies.BuildOrderValidation()).
		Build()
	if err != nil {
		admin.Stop()
		panic(fmt.Sprintf("build fake trading engine: %v", err))
	}
	if err := e.seedAdministrativeDriver(admin); err != nil {
		admin.Stop()
		trading.Stop()
		panic(fmt.Sprintf("seed fake administrative engine: %v", err))
	}
	async, err := asyncengine.NewBuilder(
		&fakeOrderChainDriver{owner: e, admin: admin, trading: trading},
	).WithStopUnderlying(func() {
		admin.Stop()
		trading.Stop()
	}).Dynamic().MaxQueues(0).Build()
	if err != nil {
		panic(fmt.Sprintf("build fake order async engine: %v", err))
	}
	runtime := &fakeOrderAsyncRuntime{async: async, driver: admin}
	actual, loaded := fakeOrderAsyncEngines.LoadOrStore(e, runtime)
	if loaded {
		_ = async.StopGraceful(context.Background())
		return actual.(*fakeOrderAsyncRuntime).async
	}
	return async
}

func (e *fakeEngine) RestoredAccountBlockCause(
	block domain.AccountBlock,
) (reject.AccountBlock, error) {
	var code reject.Code
	switch block.Code {
	case domain.RejectCodePnlKillSwitchTriggered:
		code = reject.CodePnlKillSwitchTriggered
	case domain.RejectCodeRiskLimitExceeded:
		code = reject.CodeRiskLimitExceeded
	default:
		return reject.AccountBlock{}, fmt.Errorf("unknown persisted account block code %q", block.Code)
	}
	return reject.AccountBlock{
		Policy: block.Policy, Code: code, Reason: block.Reason, Details: block.Details,
	}, nil
}

func (e *fakeEngine) administrativeDriver() asyncengine.Driver {
	e.AsyncEngine()
	runtime, ok := fakeOrderAsyncEngines.Load(e)
	if !ok {
		panic("fake administrative driver was not initialized")
	}
	return runtime.(*fakeOrderAsyncRuntime).driver
}

func stopFakeAdministrativeDriver(t *testing.T, eng *fakeEngine) {
	t.Helper()
	driver, ok := eng.administrativeDriver().(*openpit.Engine)
	if !ok {
		t.Fatalf("administrative driver type = %T, want *openpit.Engine", driver)
	}
	driver.Stop()
}

func assertFakeAccountGroup(
	t *testing.T,
	eng *fakeEngine,
	account domain.AccountID,
	group string,
) {
	t.Helper()
	accountID, err := eng.AccountID(account)
	if err != nil {
		t.Fatalf("AccountID(%s): %v", account, err)
	}
	got, ok := eng.administrativeDriver().Accounts().GroupOf(accountID).Get()
	if group == "" {
		if ok {
			t.Fatalf("GroupOf(%s) = %v, want no group", account, got)
		}
		return
	}
	want, err := eng.ResolveGroup(group)
	if err != nil {
		t.Fatalf("ResolveGroup(%s): %v", group, err)
	}
	if !ok || got != want {
		t.Fatalf(
			"GroupOf(%s) = (%v, %v), want (%v, true)",
			account,
			got,
			ok,
			want,
		)
	}
}

func assertFakeOrderBlock(
	t *testing.T,
	eng *fakeEngine,
	account domain.AccountID,
	wantBlocked bool,
	wantReason string,
) {
	t.Helper()
	order, err := eng.OrderModel(domain.Order{
		Account:     account,
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       "100",
	})
	if err != nil {
		t.Fatalf("OrderModel(%s): %v", account, err)
	}
	request, rejects, err := eng.administrativeDriver().StartPreTrade(order)
	if request != nil {
		request.Close()
	}
	if err != nil {
		t.Fatalf("StartPreTrade(%s): %v", account, err)
	}
	if !wantBlocked {
		if len(rejects) != 0 {
			t.Fatalf("StartPreTrade(%s) rejects = %+v, want none", account, rejects)
		}
		return
	}
	if len(rejects) != 1 || rejects[0].Code != reject.CodeAccountBlocked {
		t.Fatalf(
			"StartPreTrade(%s) rejects = %+v, want account block",
			account,
			rejects,
		)
	}
	if rejects[0].Reason != wantReason {
		t.Fatalf(
			"StartPreTrade(%s) block reason = %q, want %q",
			account,
			rejects[0].Reason,
			wantReason,
		)
	}
}

func assertFakeTypedOrderBlock(
	t *testing.T,
	eng *fakeEngine,
	account domain.AccountID,
	want domain.AccountBlock,
) {
	t.Helper()
	order, err := eng.OrderModel(domain.Order{
		Account: account, BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
		AmountValue: "1", Price: "100",
	})
	if err != nil {
		t.Fatalf("OrderModel(%s): %v", account, err)
	}
	request, rejects, err := eng.administrativeDriver().StartPreTrade(order)
	if request != nil {
		request.Close()
	}
	if err != nil {
		t.Fatalf("StartPreTrade(%s): %v", account, err)
	}
	wantCause, err := eng.RestoredAccountBlockCause(want)
	if err != nil {
		t.Fatalf("RestoredAccountBlockCause(%s): %v", account, err)
	}
	if len(rejects) != 1 || rejects[0].Policy != want.Policy ||
		rejects[0].Code != wantCause.Code ||
		rejects[0].Reason != want.Reason || rejects[0].Details != want.Details {
		t.Fatalf("StartPreTrade(%s) reject = %+v, want typed cause %+v", account, rejects, want)
	}
}

func assertFakeEffectiveCurrency(
	t *testing.T,
	eng *fakeEngine,
	account domain.AccountID,
	want string,
) {
	t.Helper()
	driver := eng.administrativeDriver()
	accountID, err := eng.AccountID(account)
	if err != nil {
		t.Fatalf("AccountID(%s): %v", account, err)
	}
	currency, err := eng.ResolveAsset(want)
	if err != nil {
		t.Fatalf("ResolveAsset(%s): %v", want, err)
	}
	wasBlocked := fakeAdministrativeAccountBlocked(t, eng, account)
	previous := fakeAccountPnlState(t, eng, account)
	pnl, err := param.NewPnlFromString("-150")
	if err != nil {
		t.Fatalf("NewPnlFromString: %v", err)
	}
	if _, err := driver.Configure().SetSpotFundsAccountPnl(
		policies.SpotFundsPolicyName,
		accountID,
		model.NewPnlState(pnl),
	); err != nil {
		t.Fatalf("SetSpotFundsAccountPnl(%s): %v", account, err)
	}
	probeBlocked := false
	defer func() {
		restoreFakeCurrencyProbe(
			t, driver, accountID, previous, wasBlocked, probeBlocked,
		)
	}()
	lowerBound, err := param.NewPnlFromString("-100")
	if err != nil {
		t.Fatalf("NewPnlFromString: %v", err)
	}
	barrier := policies.SpotFundsPnlBoundsBarrier{
		Currency:   currency,
		LowerBound: optional.Some(lowerBound),
	}
	outcomes, err := driver.Configure().SpotFundsPnlBoundsKillSwitch(
		policies.SpotFundsPolicyName,
		optional.None[*policies.SpotFundsPnlBoundsBarrier](),
		nil,
		[]policies.SpotFundsPnlBoundsAccountBarrier{{
			AccountID: accountID,
			Barrier:   barrier,
		}},
	)
	if err != nil {
		t.Fatalf("SpotFundsPnlBoundsKillSwitch(%s): %v", account, err)
	}
	probeBlocked = len(outcomes.AccountBlocks) != 0
	if len(outcomes.AccountBlocks) != 1 ||
		outcomes.AccountBlocks[0].AccountID != accountID ||
		outcomes.AccountBlocks[0].Block.Code != reject.CodePnlKillSwitchTriggered {
		t.Fatalf(
			"effective currency probe for %s = %+v, want %s P&L block",
			account,
			outcomes.AccountBlocks,
			want,
		)
	}
}

func fakeAdministrativeAccountBlocked(
	t *testing.T,
	eng *fakeEngine,
	account domain.AccountID,
) bool {
	t.Helper()
	accountID, err := eng.AccountID(account)
	if err != nil {
		t.Fatalf("AccountID(%s): %v", account, err)
	}
	base, _ := param.NewAsset("PROBE_BASE")
	quote, _ := param.NewAsset("PROBE_QUOTE")
	quantity, _ := param.NewQuantityFromString("1")
	price, _ := param.NewPriceFromString("1")
	order := model.NewOrder()
	operation := order.EnsureOperationView()
	operation.SetInstrument(param.NewInstrument(base, quote))
	operation.SetAccountID(accountID)
	operation.SetSide(param.SideBuy)
	operation.SetTradeAmount(param.NewQuantityTradeAmount(quantity))
	operation.SetPrice(price)
	request, rejects, err := eng.administrativeDriver().StartPreTrade(order)
	if request != nil {
		request.Close()
	}
	if err != nil {
		t.Fatalf("StartPreTrade(%s): %v", account, err)
	}
	for _, item := range rejects {
		if item.Code == reject.CodeAccountBlocked {
			return true
		}
	}
	return false
}

func fakeAccountPnlState(
	t *testing.T,
	eng *fakeEngine,
	account domain.AccountID,
) model.PnlState {
	t.Helper()
	eng.stateMu.Lock()
	state, ok := eng.accountPnlStates[account]
	eng.stateMu.Unlock()
	if ok {
		return state
	}
	zero, err := param.NewPnlFromString("0")
	if err != nil {
		t.Fatalf("NewPnlFromString(0): %v", err)
	}
	return model.NewPnlState(zero)
}

func restoreFakeCurrencyProbe(
	t *testing.T,
	driver asyncengine.Driver,
	account param.AccountID,
	previous model.PnlState,
	wasBlocked bool,
	probeBlocked bool,
) {
	t.Helper()
	if _, err := driver.Configure().SpotFundsPnlBoundsKillSwitch(
		policies.SpotFundsPolicyName,
		optional.None[*policies.SpotFundsPnlBoundsBarrier](),
		nil,
		[]policies.SpotFundsPnlBoundsAccountBarrier{},
	); err != nil {
		t.Errorf("clear currency probe barrier: %v", err)
	}
	if _, err := driver.Configure().SetSpotFundsAccountPnl(
		policies.SpotFundsPolicyName,
		account,
		previous,
	); err != nil {
		t.Errorf("restore currency probe P&L: %v", err)
	}
	if probeBlocked && !wasBlocked {
		driver.Accounts().Unblock(account)
	}
}

func assertFakeAccountPnl(
	t *testing.T,
	eng *fakeEngine,
	account domain.AccountID,
	currencyCode string,
	want string,
) {
	t.Helper()
	driver := eng.administrativeDriver()
	accountID, err := eng.AccountID(account)
	if err != nil {
		t.Fatalf("AccountID(%s): %v", account, err)
	}
	currency, err := eng.ResolveAsset(currencyCode)
	if err != nil {
		t.Fatalf("ResolveAsset(%s): %v", currencyCode, err)
	}
	pnl, err := param.NewPnlFromString(want)
	if err != nil {
		t.Fatalf("NewPnlFromString(%s): %v", want, err)
	}
	barrier := policies.SpotFundsPnlBoundsBarrier{
		Currency:   currency,
		LowerBound: optional.Some(pnl),
		UpperBound: optional.Some(pnl),
	}
	wasBlocked := fakeAdministrativeAccountBlocked(t, eng, account)
	outcomes, err := driver.Configure().SpotFundsPnlBoundsKillSwitch(
		policies.SpotFundsPolicyName,
		optional.None[*policies.SpotFundsPnlBoundsBarrier](),
		nil,
		[]policies.SpotFundsPnlBoundsAccountBarrier{{
			AccountID: accountID,
			Barrier:   barrier,
		}},
	)
	if err != nil {
		t.Fatalf("probe account P&L for %s: %v", account, err)
	}
	if _, err := driver.Configure().SpotFundsPnlBoundsKillSwitch(
		policies.SpotFundsPolicyName,
		optional.None[*policies.SpotFundsPnlBoundsBarrier](),
		nil,
		[]policies.SpotFundsPnlBoundsAccountBarrier{},
	); err != nil {
		t.Fatalf("clear account P&L probe: %v", err)
	}
	if len(outcomes.AccountBlocks) != 0 {
		if !wasBlocked {
			driver.Accounts().Unblock(accountID)
		}
		t.Fatalf(
			"account P&L probe for %s = %+v, want exact %s %s",
			account,
			outcomes.AccountBlocks,
			want,
			currencyCode,
		)
	}
}

func assertFakeBalances(
	t *testing.T,
	eng *fakeEngine,
	account domain.AccountID,
	assetCode string,
	wantBalance string,
	wantHeld string,
	wantIncoming string,
) {
	t.Helper()
	accountID, err := eng.AccountID(account)
	if err != nil {
		t.Fatalf("AccountID(%s): %v", account, err)
	}
	asset, err := eng.ResolveAsset(assetCode)
	if err != nil {
		t.Fatalf("ResolveAsset(%s): %v", assetCode, err)
	}
	zero, err := param.NewPositionSizeFromString("0")
	if err != nil {
		t.Fatalf("NewPositionSizeFromString(0): %v", err)
	}
	delta := param.NewDeltaAdjustmentAmount(zero)
	adjustment, err := model.NewAccountAdjustmentFromValues(
		model.AccountAdjustmentValues{
			BalanceOperation: optional.Some(
				model.NewAccountAdjustmentBalanceOperationFromValues(
					model.AccountAdjustmentBalanceOperationValues{
						Asset: optional.Some(asset),
					},
				),
			),
			Amount: optional.Some(
				model.NewAccountAdjustmentAmountFromValues(
					model.AccountAdjustmentAmountValues{
						Balance:  optional.Some(delta),
						Held:     optional.Some(delta),
						Incoming: optional.Some(delta),
					},
				),
			),
		},
	)
	if err != nil {
		t.Fatalf("build balance probe: %v", err)
	}
	result, err := eng.administrativeDriver().ApplyAccountAdjustment(
		accountID,
		[]model.AccountAdjustment{adjustment},
	)
	if err != nil {
		t.Fatalf("apply balance probe: %v", err)
	}
	if batchErr, ok := result.BatchError.Get(); ok {
		t.Fatalf("balance probe rejected: %+v", batchErr)
	}
	for _, outcome := range result.Outcomes {
		if outcome.Entry.Asset.String() != asset.String() {
			continue
		}
		assertFakeOutcomeAmount(
			t, "balance", outcome.Entry.Balance, wantBalance,
		)
		assertFakeOutcomeAmount(t, "held", outcome.Entry.Held, wantHeld)
		assertFakeOutcomeAmount(
			t, "incoming", outcome.Entry.Incoming, wantIncoming,
		)
		return
	}
	t.Fatalf(
		"balance probe outcomes = %+v, want asset %s",
		result.Outcomes,
		assetCode,
	)
}

func assertFakeOutcomeAmount(
	t *testing.T,
	field string,
	got optional.Option[accountadjustment.OutcomeAmount],
	want string,
) {
	t.Helper()
	value, ok := got.Get()
	if !ok || value.Absolute.String() != want {
		t.Fatalf(
			"balance probe %s = (%v, %v), want %s",
			field,
			value.Absolute,
			ok,
			want,
		)
	}
}

func assertFakeNoEffectiveCurrency(
	t *testing.T,
	eng *fakeEngine,
	account domain.AccountID,
	probeCurrency string,
) {
	t.Helper()
	driver := eng.administrativeDriver()
	accountID, err := eng.AccountID(account)
	if err != nil {
		t.Fatalf("AccountID(%s): %v", account, err)
	}
	currency, err := eng.ResolveAsset(probeCurrency)
	if err != nil {
		t.Fatalf("ResolveAsset(%s): %v", probeCurrency, err)
	}
	wasBlocked := fakeAdministrativeAccountBlocked(t, eng, account)
	previous := fakeAccountPnlState(t, eng, account)
	pnl, _ := param.NewPnlFromString("-150")
	if _, err := driver.Configure().SetSpotFundsAccountPnl(
		policies.SpotFundsPolicyName,
		accountID,
		model.NewPnlState(pnl),
	); err != nil {
		t.Fatalf("seed no-currency probe P&L: %v", err)
	}
	probeBlocked := false
	defer func() {
		if _, clearErr := driver.Configure().SpotFundsPnlBoundsKillSwitch(
			policies.SpotFundsPolicyName,
			optional.Some[*policies.SpotFundsPnlBoundsBarrier](nil),
			nil,
			nil,
		); clearErr != nil {
			t.Errorf("clear no-currency probe barrier: %v", clearErr)
		}
		if _, restoreErr := driver.Configure().SetSpotFundsAccountPnl(
			policies.SpotFundsPolicyName,
			accountID,
			previous,
		); restoreErr != nil {
			t.Errorf("restore no-currency probe P&L: %v", restoreErr)
		}
		if probeBlocked && !wasBlocked {
			driver.Accounts().Unblock(accountID)
		}
	}()
	lower, _ := param.NewPnlFromString("-100")
	barrier := policies.SpotFundsPnlBoundsBarrier{
		Currency:   currency,
		LowerBound: optional.Some(lower),
	}
	outcomes, err := driver.Configure().SpotFundsPnlBoundsKillSwitch(
		policies.SpotFundsPolicyName,
		optional.Some(&barrier),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("probe absent effective currency: %v", err)
	}
	probeBlocked = len(outcomes.AccountBlocks) != 0
	if len(outcomes.AccountBlocks) != 1 ||
		outcomes.AccountBlocks[0].AccountID != accountID {
		t.Fatalf(
			"no-currency probe for %s = %+v, want one fallback block",
			account,
			outcomes.AccountBlocks,
		)
	}
}

func TestAssertFakeEffectiveCurrencyRestoresPnl(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	const account domain.AccountID = "probe"
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code:     account,
		Currency: "USD",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	accountID, err := eng.AccountID(account)
	if err != nil {
		t.Fatalf("AccountID(%s): %v", account, err)
	}
	pnl, _ := param.NewPnlFromString("5")
	result, err := eng.administrativeDriver().Configure().SetSpotFundsAccountPnl(
		policies.SpotFundsPolicyName,
		accountID,
		model.NewPnlState(pnl),
	)
	if err != nil {
		t.Fatalf("SetSpotFundsAccountPnl: %v", err)
	}
	if _, err := eng.AppliedSpotFundsAccountPnl(account, "5", "", result); err != nil {
		t.Fatalf("AppliedSpotFundsAccountPnl: %v", err)
	}
	assertFakeEffectiveCurrency(t, eng, account, "USD")
	assertFakeAccountPnl(t, eng, account, "USD", "5")
}

func (e *fakeEngine) seedAdministrativeDriver(driver asyncengine.Driver) error {
	e.resolverMu.RLock()
	accountIDs := maps.Clone(e.accountResolverIDs)
	groupIDs := maps.Clone(e.groupResolverIDs)
	accountGroups := maps.Clone(e.accountGroups)
	e.resolverMu.RUnlock()

	admin := driver.Accounts()
	for code, groupCode := range accountGroups {
		if groupCode == "" {
			continue
		}
		account, err := fakeAdministrativeAccountID(code, accountIDs)
		if err != nil {
			return err
		}
		group, err := fakeAdministrativeGroupID(groupCode, groupIDs)
		if err != nil {
			return err
		}
		if err := admin.RegisterGroup([]param.AccountID{account}, group); err != nil {
			return fmt.Errorf("register account %q with group %q: %w", code, groupCode, err)
		}
	}
	return nil
}

func fakeAdministrativeAccountID(
	code domain.AccountID,
	ids map[domain.AccountID]domain.EngineAccountID,
) (param.AccountID, error) {
	id, ok := ids[code]
	if !ok {
		return param.AccountID{}, fmt.Errorf("account %q has no fake engine id", code)
	}
	return param.NewAccountIDFromUint64(id.Uint64()), nil
}

func fakeAdministrativeGroupID(
	code string,
	ids map[string]domain.EngineGroupID,
) (param.AccountGroupID, error) {
	if code == "" {
		return param.DefaultAccountGroup, nil
	}
	id, ok := ids[code]
	if !ok {
		return param.AccountGroupID{}, fmt.Errorf("group %q has no fake engine id", code)
	}
	return param.NewAccountGroupIDFromUint32(id.Uint32())
}

func (d *fakeOrderChainDriver) StartPreTrade(
	order model.Order,
) (*pretrade.Request, []reject.Reject, error) {
	return d.trading.StartPreTrade(order)
}

func (d *fakeOrderChainDriver) ExecutePreTrade(
	order model.Order,
) (*pretrade.Reservation, []reject.Reject, error) {
	if d.owner.failSubmit {
		return nil, nil, errors.New("submit failed")
	}
	if d.owner.submitReject != nil {
		return nil, []reject.Reject{{}}, nil
	}
	return d.trading.ExecutePreTrade(order)
}

func (d *fakeOrderChainDriver) ExecutePreTradeDryRun(
	order model.Order,
) (*pretrade.DryRunReport, error) {
	return d.trading.ExecutePreTradeDryRun(order)
}

func (d *fakeOrderChainDriver) ApplyDropCopy(
	order model.Order,
) (*pretrade.DropCopyOperation, []reject.Reject, error) {
	if d.owner.failSubmit {
		return nil, nil, errors.New("submit failed")
	}
	if d.owner.submitReject != nil {
		return nil, []reject.Reject{{}}, nil
	}
	return d.trading.ApplyDropCopy(order)
}

func (d *fakeOrderChainDriver) ApplyExecutionReport(
	report model.ExecutionReport,
) (pretrade.PostTradeResult, error) {
	if d.owner.failExecReport {
		return pretrade.PostTradeResult{}, errors.New("exec report failed")
	}
	reservationRemainder := ""
	if fill, ok := report.Fill().Get(); ok {
		if leaves, ok := fill.RemainingReservedQuantity().Get(); ok {
			reservationRemainder = leaves.String()
		}
	}
	d.owner.stateMu.Lock()
	d.owner.execReportLeaves = append(
		d.owner.execReportLeaves, reservationRemainder,
	)
	d.owner.stateMu.Unlock()
	return d.trading.ApplyExecutionReport(report)
}

func (d *fakeOrderChainDriver) ApplyAccountAdjustment(
	account param.AccountID, adjustments []model.AccountAdjustment,
) (accountadjustment.BatchResult, error) {
	if d.owner.failAdjustment {
		return accountadjustment.BatchResult{}, errors.New("adjustment failed")
	}
	return d.admin.ApplyAccountAdjustment(account, adjustments)
}

func (d *fakeOrderChainDriver) Accounts() accounts.Accounts {
	return d.admin.Accounts()
}

func (e *fakeEngine) AccountID(
	account domain.AccountID,
) (param.AccountID, error) {
	if err := e.checkKnownAccount(account); err != nil {
		return param.AccountID{}, err
	}
	e.resolverMu.RLock()
	accountID, ok := e.accountResolverIDs[account]
	e.resolverMu.RUnlock()
	if !ok {
		hash := fnv.New64a()
		_, _ = hash.Write([]byte(account))
		accountID = domain.EngineAccountID(hash.Sum64())
		if accountID == 0 {
			accountID = 1
		}
	}
	return param.NewAccountIDFromUint64(accountID.Uint64()), nil
}

func (e *fakeEngine) OrderModel(o domain.Order) (model.Order, error) {
	if err := e.checkKnownOrderAssets(o.BaseAsset, o.QuoteAsset); err != nil {
		return model.Order{}, err
	}
	accountID, err := e.AccountID(o.Account)
	if err != nil {
		return model.Order{}, err
	}
	e.resolverMu.RLock()
	baseID, baseOK := e.assetResolverIDs[o.BaseAsset]
	quoteID, quoteOK := e.assetResolverIDs[o.QuoteAsset]
	e.resolverMu.RUnlock()
	baseAlias := o.BaseAsset
	if baseOK {
		baseAlias = strconv.FormatUint(baseID.Uint64(), 10)
	}
	base, err := param.NewAsset(baseAlias)
	if err != nil {
		return model.Order{}, err
	}
	quoteAlias := o.QuoteAsset
	if quoteOK {
		quoteAlias = strconv.FormatUint(quoteID.Uint64(), 10)
	}
	quote, err := param.NewAsset(quoteAlias)
	if err != nil {
		return model.Order{}, err
	}
	var side param.Side
	switch o.Side {
	case domain.OrderSideBuy:
		side = param.SideBuy
	case domain.OrderSideSell:
		side = param.SideSell
	default:
		return model.Order{}, fmt.Errorf(
			"engine: unknown order side %q: %w", o.Side, domain.ErrInvalid,
		)
	}
	var amount param.TradeAmount
	switch o.AmountKind {
	case domain.OrderAmountKindQuantity:
		quantity, quantityErr := param.NewQuantityFromString(o.AmountValue)
		if quantityErr != nil {
			return model.Order{}, quantityErr
		}
		amount = param.NewQuantityTradeAmount(quantity)
	case domain.OrderAmountKindVolume:
		volume, volumeErr := param.NewVolumeFromString(o.AmountValue)
		if volumeErr != nil {
			return model.Order{}, volumeErr
		}
		amount = param.NewVolumeTradeAmount(volume)
	default:
		return model.Order{}, fmt.Errorf(
			"engine: unknown order amount kind %q: %w",
			o.AmountKind, domain.ErrInvalid,
		)
	}
	order := model.NewOrder()
	operation := order.EnsureOperationView()
	operation.SetInstrument(param.NewInstrument(base, quote))
	operation.SetAccountID(accountID)
	operation.SetSide(side)
	operation.SetTradeAmount(amount)
	if o.Price != "" {
		price, priceErr := param.NewPriceFromString(o.Price)
		if priceErr != nil {
			return model.Order{}, priceErr
		}
		operation.SetPrice(price)
	}
	return order, nil
}

func (e *fakeEngine) ExecutionReportModel(
	in domain.ExecutionReportInput,
	reservationRemainder string,
) (model.ExecutionReport, error) {
	if in.Commission != nil {
		if err := e.checkKnownAsset(in.Commission.Currency); err != nil {
			return model.ExecutionReport{}, err
		}
	}
	order, err := e.OrderModel(domain.Order{
		Account:     in.Account,
		BaseAsset:   in.BaseAsset,
		QuoteAsset:  in.QuoteAsset,
		Side:        in.Side,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
	})
	if err != nil {
		return model.ExecutionReport{}, err
	}
	orderOperation, _ := order.Operation().Get()
	instrument, _ := orderOperation.Instrument().Get()
	accountID, _ := orderOperation.AccountID().Get()
	if e.execReportAccountMismatch {
		accountID = param.NewAccountIDFromUint64(
			uint64(accountID.Handle()) + 1,
		)
	}
	side, _ := orderOperation.Side().Get()
	report := model.NewExecutionReport()
	operation := report.EnsureOperationView()
	operation.SetInstrument(instrument)
	operation.SetAccountID(accountID)
	operation.SetSide(side)
	fill := report.EnsureFillView()
	if in.FillQuantity != "" && in.FillPrice != "" {
		price, err := param.NewPriceFromString(in.FillPrice)
		if err != nil {
			return model.ExecutionReport{}, err
		}
		quantity, err := param.NewQuantityFromString(in.FillQuantity)
		if err != nil {
			return model.ExecutionReport{}, err
		}
		fill.SetLastTrade(model.NewExecutionReportTrade(price, quantity))
	}
	if reservationRemainder != "" {
		leaves, err := param.NewQuantityFromString(reservationRemainder)
		if err != nil {
			return model.ExecutionReport{}, err
		}
		fill.SetRemainingReservedQuantity(leaves)
	}
	fill.SetIsFinal(domain.OrderStatusTerminal(in.OrderStatus))
	if len(in.Lock) > 0 {
		lock, err := pretrade.NewLockFromMsgPack(in.Lock)
		if err != nil {
			return model.ExecutionReport{}, err
		}
		fill.SetLock(lock.Bytes())
	}
	return report, nil
}

func (e *fakeEngine) AccountAdjustmentModels(
	reqs []domain.AdjustmentRequest,
) ([]model.AccountAdjustment, error) {
	adjustments := make([]model.AccountAdjustment, 0, len(reqs))
	for _, req := range reqs {
		adjustment, err := e.fakeAccountAdjustmentModel(req)
		if err != nil {
			return nil, err
		}
		adjustments = append(adjustments, adjustment)
	}
	return adjustments, nil
}

func (e *fakeEngine) fakeAccountAdjustmentModel(
	req domain.AdjustmentRequest,
) (model.AccountAdjustment, error) {
	if err := e.checkKnownAsset(req.Asset); err != nil {
		return model.AccountAdjustment{}, err
	}
	asset, err := e.ResolveAsset(req.Asset)
	if err != nil {
		return model.AccountAdjustment{}, err
	}
	operation := model.AccountAdjustmentBalanceOperationValues{
		Asset: optional.Some(asset),
	}
	if req.AverageEntryPrice != "" {
		price, parseErr := param.NewPriceFromString(req.AverageEntryPrice)
		if parseErr != nil {
			return model.AccountAdjustment{}, parseErr
		}
		operation.AverageEntryPrice = optional.Some(price)
	}
	if req.RealizedPnl != "" || req.RealizedPnlHaltReason != "" {
		state, stateErr := fakePnlState(
			req.RealizedPnl,
			req.RealizedPnlHaltReason,
		)
		if stateErr != nil {
			return model.AccountAdjustment{}, stateErr
		}
		operation.RealizedPnl = optional.Some(state)
	}
	amounts, err := fakeAdjustmentAmounts(req)
	if err != nil {
		return model.AccountAdjustment{}, err
	}
	values := model.AccountAdjustmentValues{
		BalanceOperation: optional.Some(
			model.NewAccountAdjustmentBalanceOperationFromValues(operation),
		),
		Amount: optional.Some(
			model.NewAccountAdjustmentAmountFromValues(amounts),
		),
	}
	bounds, hasBounds, err := fakeAdjustmentBounds(req)
	if err != nil {
		return model.AccountAdjustment{}, err
	}
	if hasBounds {
		values.Bounds = optional.Some(
			model.NewAccountAdjustmentBoundsFromValues(bounds),
		)
	}
	return model.NewAccountAdjustmentFromValues(values)
}

func fakePnlState(
	value string,
	haltReason domain.PnlHaltReason,
) (model.PnlState, error) {
	if haltReason == "" {
		pnl, err := param.NewPnlFromString(value)
		if err != nil {
			return model.PnlState{}, err
		}
		return model.NewPnlState(pnl), nil
	}
	reasons := map[domain.PnlHaltReason]model.PnlHaltReason{
		domain.PnlHaltReasonMissingFx:              model.PnlHaltReasonMissingFx,
		domain.PnlHaltReasonMissingAccountCurrency: model.PnlHaltReasonMissingAccountCurrency,
		domain.PnlHaltReasonMissingInitialPnl:      model.PnlHaltReasonMissingInitialPnl,
		domain.PnlHaltReasonMissingCostBasis:       model.PnlHaltReasonMissingCostBasis,
		domain.PnlHaltReasonArithmeticOverflow:     model.PnlHaltReasonArithmeticOverflow,
	}
	reason, ok := reasons[haltReason]
	if !ok {
		return model.PnlState{}, domain.ErrInvalid
	}
	return model.NewPnlHaltedState(reason)
}

func fakeAdjustmentAmounts(
	req domain.AdjustmentRequest,
) (model.AccountAdjustmentAmountValues, error) {
	var values model.AccountAdjustmentAmountValues
	fields := []struct {
		input *domain.AdjustmentAmount
		set   func(param.AdjustmentAmount)
	}{
		{req.Balance, func(value param.AdjustmentAmount) {
			values.Balance = optional.Some(value)
		}},
		{req.Held, func(value param.AdjustmentAmount) {
			values.Held = optional.Some(value)
		}},
		{req.Incoming, func(value param.AdjustmentAmount) {
			values.Incoming = optional.Some(value)
		}},
	}
	for _, field := range fields {
		if field.input == nil {
			continue
		}
		amount, err := fakeAdjustmentAmount(*field.input)
		if err != nil {
			return values, err
		}
		field.set(amount)
	}
	return values, nil
}

func fakeAdjustmentAmount(
	amount domain.AdjustmentAmount,
) (param.AdjustmentAmount, error) {
	value, err := param.NewPositionSizeFromString(amount.Value)
	if err != nil {
		return param.AdjustmentAmount{}, err
	}
	switch amount.Mode {
	case domain.AdjustmentModeAbsolute:
		return param.NewAbsoluteAdjustmentAmount(value), nil
	case domain.AdjustmentModeDelta:
		return param.NewDeltaAdjustmentAmount(value), nil
	default:
		return param.AdjustmentAmount{}, domain.ErrInvalid
	}
}

func fakeAdjustmentBounds(
	req domain.AdjustmentRequest,
) (model.AccountAdjustmentBoundsValues, bool, error) {
	var values model.AccountAdjustmentBoundsValues
	hasBounds := false
	fields := []struct {
		input        *domain.AdjustmentBounds
		lower, upper *optional.Option[param.PositionSize]
	}{
		{req.BalanceBounds, &values.BalanceLower, &values.BalanceUpper},
		{req.HeldBounds, &values.HeldLower, &values.HeldUpper},
		{req.IncomingBounds, &values.IncomingLower, &values.IncomingUpper},
	}
	for _, field := range fields {
		if field.input == nil {
			continue
		}
		if field.input.Lower != "" {
			value, err := param.NewPositionSizeFromString(field.input.Lower)
			if err != nil {
				return values, false, err
			}
			*field.lower = optional.Some(value)
			hasBounds = true
		}
		if field.input.Upper != "" {
			value, err := param.NewPositionSizeFromString(field.input.Upper)
			if err != nil {
				return values, false, err
			}
			*field.upper = optional.Some(value)
			hasBounds = true
		}
	}
	return values, hasBounds, nil
}

func (e *fakeEngine) AppliedAccountAdjustmentBatch(
	account domain.AccountID,
	reqs []domain.AdjustmentRequest,
	_ accountadjustment.BatchResult,
) ([]engine.AdjustmentResult, *domain.AdjustmentOutcomeRejected, error) {
	return e.materializeFakeAccountAdjustmentBatch(account, reqs)
}

func (e *fakeEngine) SpotFundsAccountPnlAssignment(
	pnl string,
	haltReason domain.PnlHaltReason,
) (asyncengine.SpotFundsAccountPnlAssignment, error) {
	if pnl != "" && haltReason != "" {
		return asyncengine.SpotFundsAccountPnlAssignment{}, domain.ErrInvalid
	}
	var state model.PnlState
	if haltReason == "" {
		value, err := param.NewPnlFromString(pnl)
		if err != nil {
			return asyncengine.SpotFundsAccountPnlAssignment{}, err
		}
		state = model.NewPnlState(value)
	} else {
		reasons := map[domain.PnlHaltReason]model.PnlHaltReason{
			domain.PnlHaltReasonMissingFx:              model.PnlHaltReasonMissingFx,
			domain.PnlHaltReasonMissingAccountCurrency: model.PnlHaltReasonMissingAccountCurrency,
			domain.PnlHaltReasonMissingInitialPnl:      model.PnlHaltReasonMissingInitialPnl,
			domain.PnlHaltReasonMissingCostBasis:       model.PnlHaltReasonMissingCostBasis,
			domain.PnlHaltReasonArithmeticOverflow:     model.PnlHaltReasonArithmeticOverflow,
		}
		reason, ok := reasons[haltReason]
		if !ok {
			return asyncengine.SpotFundsAccountPnlAssignment{}, domain.ErrInvalid
		}
		var err error
		state, err = model.NewPnlHaltedState(reason)
		if err != nil {
			return asyncengine.SpotFundsAccountPnlAssignment{}, err
		}
	}
	return asyncengine.SpotFundsAccountPnlAssignment{
		PolicyName: policies.SpotFundsPolicyName,
		State:      state,
	}, nil
}

func (e *fakeEngine) AppliedSpotFundsAccountPnl(
	account domain.AccountID,
	pnl string,
	haltReason domain.PnlHaltReason,
	_ configure.PolicyConfigurationResult,
) ([]domain.AccountBlock, error) {
	if e.accountPnlErr != nil {
		return nil, e.accountPnlErr
	}
	assignment, err := e.SpotFundsAccountPnlAssignment(pnl, haltReason)
	if err != nil {
		return nil, err
	}
	e.stateMu.Lock()
	if e.accountPnlStates == nil {
		e.accountPnlStates = make(map[domain.AccountID]model.PnlState)
	}
	e.accountPnlStates[account] = assignment.State
	e.stateMu.Unlock()
	e.accountPnlStateCalls = append(e.accountPnlStateCalls, accountPnlStateCall{
		id: account, pnl: pnl, haltReason: haltReason,
	})
	if haltReason == "" {
		e.accountPnlCalls = append(e.accountPnlCalls, accountPnlCall{account, pnl})
	}
	return e.accountPnlBlocks[account], nil
}

func (e *fakeEngine) CheckOrderModel(
	probe domain.OrderProbe,
) (model.Order, error) {
	return e.OrderModel(domain.Order{
		Account:     probe.Account,
		BaseAsset:   probe.BaseAsset,
		QuoteAsset:  probe.QuoteAsset,
		Side:        probe.Side,
		AmountKind:  probe.AmountKind,
		AmountValue: probe.AmountValue,
		Price:       probe.Price,
	})
}

func (e *fakeEngine) recordChainSubmit(o domain.Order) error {
	e.stateMu.Lock()
	e.submitCalls = append(e.submitCalls, o)
	e.stateMu.Unlock()
	if e.submitEntered != nil {
		e.submitEntered <- o.Account
	}
	if e.submitRelease != nil {
		<-e.submitRelease
	}
	return nil
}

func (e *fakeEngine) RejectedOrder(
	o domain.Order, _ []reject.Reject,
) engine.OrderResult {
	_ = e.recordChainSubmit(o)
	return engine.OrderResult{
		Accepted: false,
		Rejects:  []domain.OrderReject{*e.submitReject},
	}
}

func (e *fakeEngine) ReservedOrder(
	o domain.Order, _ asyncengine.OperationResult,
) (engine.OrderResult, error) {
	if err := e.recordChainSubmit(o); err != nil {
		return engine.OrderResult{}, err
	}
	return engine.OrderResult{
		Accepted:            true,
		Lock:                append([]byte(nil), e.submitLock...),
		Blocks:              append([]domain.AccountBlock(nil), e.submitBlocks...),
		Outcomes:            append([]engine.BalanceOutcome(nil), e.submitOutcomes...),
		SettlementLockPrice: o.Price,
	}, nil
}

func (e *fakeEngine) AppliedDropCopyOrder(
	o domain.Order, result asyncengine.DropCopyResult,
) (engine.OrderResult, error) {
	return e.ReservedOrder(o, result)
}

func (e *fakeEngine) RejectedImmediate(
	o domain.Order, _ []reject.Reject,
) engine.ImmediateResult {
	_ = e.recordChainSubmit(o)
	result := engine.ImmediateResult{Accepted: false}
	if e.submitReject != nil &&
		*e.submitReject != (domain.OrderReject{}) {
		result.Rejects = []domain.OrderReject{*e.submitReject}
	}
	return result
}

func (e *fakeEngine) PrepareImmediateReservation(
	o domain.Order, _ asyncengine.OperationResult,
) (engine.ImmediatePreparation, error) {
	return e.prepareFakeImmediate(o, nil)
}

func (e *fakeEngine) PrepareImmediateDropCopy(
	o domain.Order, _ asyncengine.DropCopyResult,
) (engine.ImmediatePreparation, error) {
	return e.prepareFakeImmediate(o, e.submitBlocks)
}

func (e *fakeEngine) prepareFakeImmediate(
	o domain.Order, blocks []domain.AccountBlock,
) (engine.ImmediatePreparation, error) {
	if err := e.recordChainSubmit(o); err != nil {
		return engine.ImmediatePreparation{}, err
	}
	settlementPrice := e.submitSettlementLockPrice
	if settlementPrice == "" {
		settlementPrice = o.Price
	}
	tradePrice := e.submitTradePrice
	if tradePrice == "" {
		tradePrice = o.Price
		if tradePrice == "" {
			tradePrice = settlementPrice
		}
	}
	reportInput := domain.ExecutionReportInput{
		BaseAsset:      o.BaseAsset,
		QuoteAsset:     o.QuoteAsset,
		FillQuantity:   o.AmountValue,
		FillPrice:      tradePrice,
		LeavesQuantity: "0",
		LockPrice:      settlementPrice,
		Lock:           append([]byte(nil), e.submitLock...),
		Order:          o.ExternalID,
		Account:        o.Account,
		Side:           o.Side,
		OrderStatus:    domain.OrderStatusFilled,
	}
	report, err := e.fakeImmediateReportModel(o, reportInput)
	if err != nil {
		return engine.ImmediatePreparation{}, err
	}
	state := "reservation committed"
	if o.DropCopy {
		state = "drop-copy committed"
	}
	return engine.ImmediatePreparation{
		ExecutionReport:     report,
		ReportInput:         reportInput,
		Outcomes:            append([]engine.BalanceOutcome(nil), e.submitOutcomes...),
		Blocks:              append([]domain.AccountBlock(nil), blocks...),
		ReconciliationState: state,
	}, nil
}

func (e *fakeEngine) fakeImmediateReportModel(
	o domain.Order, in domain.ExecutionReportInput,
) (model.ExecutionReport, error) {
	order, err := e.OrderModel(o)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	orderOperation, _ := order.Operation().Get()
	instrument, _ := orderOperation.Instrument().Get()
	accountID, _ := orderOperation.AccountID().Get()
	side, _ := orderOperation.Side().Get()
	report := model.NewExecutionReport()
	operation := report.EnsureOperationView()
	operation.SetInstrument(instrument)
	operation.SetAccountID(accountID)
	operation.SetSide(side)
	fill := report.EnsureFillView()
	priceValue := in.FillPrice
	if priceValue == "" {
		priceValue = "1"
	}
	price, err := param.NewPriceFromString(priceValue)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	quantity, err := param.NewQuantityFromString(in.FillQuantity)
	if err != nil {
		return model.ExecutionReport{}, err
	}
	fill.SetLastTrade(model.NewExecutionReportTrade(price, quantity))
	leaves, err := param.NewQuantityFromString("0")
	if err != nil {
		return model.ExecutionReport{}, err
	}
	fill.SetRemainingReservedQuantity(leaves)
	fill.SetIsFinal(true)
	if len(in.Lock) > 0 {
		fill.SetLock(in.Lock)
	}
	return report, nil
}

func (e *fakeEngine) SettleImmediate(
	o domain.Order,
	prepared engine.ImmediatePreparation,
	_ pretrade.PostTradeResult,
) (engine.ImmediateResult, error) {
	in := prepared.ReportInput
	request := domain.ExecutionReportRequestFromInput(in)
	persistence := engine.ExecutionReportPersistence{
		Trade: &domain.Trade{
			Order:      o.ExternalID,
			Account:    o.Account,
			BaseAsset:  o.BaseAsset,
			QuoteAsset: o.QuoteAsset,
			Side:       o.Side,
			Quantity:   in.FillQuantity,
			Price:      in.FillPrice,
			LockPrice:  in.LockPrice,
		},
		OrderStatus:          domain.OrderStatusFilled,
		AccountPnl:           e.submitAccountPnl,
		AccountPnlHaltReason: e.submitAccountPnlHaltReason,
		Leaves:               in.LeavesQuantity,
		Balances:             balanceSettlementsFrom(e.submitOutcomes),
		Events: []domain.OrderEvent{{
			Order: o.ExternalID,
			Type:  domain.OrderEventFill,
			Payload: domain.OrderEventPayload{
				FillQuantity:   in.FillQuantity,
				FillPrice:      in.FillPrice,
				FillLockPrice:  in.LockPrice,
				LeavesQuantity: in.LeavesQuantity,
				OrderStatus:    string(in.OrderStatus),
			},
		}},
	}
	var persistenceResult *engine.ExecutionReportPersistence
	if !e.emptyImmediatePersistence {
		persistenceResult = &persistence
	}
	return engine.ImmediateResult{
		Accepted:             true,
		Persistence:          persistenceResult,
		ExecutionReport:      request,
		Lock:                 append([]byte(nil), e.submitLock...),
		Blocks:               append([]domain.AccountBlock(nil), prepared.Blocks...),
		Outcomes:             append([]engine.BalanceOutcome(nil), e.submitOutcomes...),
		AccountPnl:           e.submitAccountPnl,
		AccountPnlHaltReason: e.submitAccountPnlHaltReason,
		SettlementLockPrice:  in.LockPrice,
		FillQuantity:         in.FillQuantity,
		TradePrice:           in.FillPrice,
	}, nil
}

func (e *fakeEngine) CheckedOrder(
	probe domain.OrderProbe, _ asyncengine.OrderCheckResult,
) (domain.CheckResult, error) {
	e.checkProbes = append(e.checkProbes, probe)
	return e.checkResult, nil
}

func TestOrderRejectedSettlementPreservesOrderedRejects(t *testing.T) {
	t.Parallel()
	rejects := []domain.OrderReject{
		{Code: "rate", Scope: "account", Policy: "rate", Details: "first"},
		{Code: "size", Scope: "order", Policy: "size", Details: "second"},
	}
	settlement := orderRejectedSettlement(
		"acc-1", domain.Order{ExternalID: "order-1"}, rejects, testCaller,
	)
	if len(settlement.Events) != 1 {
		t.Fatalf("events = %+v, want one reject event", settlement.Events)
	}
	got := settlement.Events[0].Payload.Rejects
	if len(got) != len(rejects) {
		t.Fatalf("rejects = %+v, want %+v", got, rejects)
	}
	for i := range rejects {
		if got[i] != rejects[i] {
			t.Fatalf("reject %d = %+v, want %+v", i, got[i], rejects[i])
		}
	}
}

// A pre-trade reject reserved nothing, so the submit records a zero open
// quantity rather than an empty one. The empty value would read as "unknown"
// and make a later forced terminal report demand a leaves the order never had.
func TestLocalNode_SubmitOrderRejectedRecordsZeroLeaves(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitReject = &domain.OrderReject{
		Code: "limit", Scope: "account", Policy: "order-size",
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order, err := n.SubmitOrder(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Status != domain.OrderStatusRejected {
		t.Fatalf("order status = %q, want rejected", order.Status)
	}
	if order.Leaves != "0" {
		t.Fatalf("rejected order leaves = %q, want a recorded zero", order.Leaves)
	}
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Leaves != "0" {
		t.Fatalf("stored leaves = %q, want a recorded zero", detail.Order.Leaves)
	}

	// The recorded zero preserves the engine's answer. A later forced terminal
	// no-fill report sends that pre-report value back without caller leaves.
	if _, err := n.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
		Order:       order.ExternalID,
		Force:       true,
		OrderStatus: domain.OrderStatusCancelled,
	}, testCaller); err != nil {
		t.Fatalf("forced terminal report after reject: %v", err)
	}
	if len(eng.execReportLeaves) != 1 || eng.execReportLeaves[0] != "0" {
		t.Fatalf(
			"engine terminal reservation remainder = %+v, want recorded zero",
			eng.execReportLeaves,
		)
	}
}

func TestLocalNode_SubmitOrderRejectsPersistedAccountMismatchBeforeEngine(
	t *testing.T,
) {
	t.Parallel()
	n, eng := newPersistedAccountMismatchNode(t)

	_, err := n.SubmitOrder(
		context.Background(),
		persistedAccountMismatchOrder(false),
		domain.MissingAccountCreate,
		testCaller,
	)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SubmitOrder error = %v, want ErrInvalid", err)
	}
	if len(eng.submitCalls) != 0 {
		t.Fatalf("engine mutations = %+v, want none", eng.submitCalls)
	}
}

func TestLocalNode_SubmitImmediateReservationRejectsPersistedAccountMismatchBeforeEngine(
	t *testing.T,
) {
	t.Parallel()
	n, eng := newPersistedAccountMismatchNode(t)

	_, _, err := n.SubmitImmediate(
		context.Background(),
		persistedAccountMismatchOrder(false),
		domain.MissingAccountCreate,
		testCaller,
	)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SubmitImmediate error = %v, want ErrInvalid", err)
	}
	if len(eng.submitCalls) != 0 {
		t.Fatalf("engine mutations = %+v, want none", eng.submitCalls)
	}
}

func TestLocalNode_SubmitImmediateDropCopyRejectsPersistedAccountMismatchBeforeEngine(
	t *testing.T,
) {
	t.Parallel()
	n, eng := newPersistedAccountMismatchNode(t)

	_, _, err := n.SubmitImmediate(
		context.Background(),
		persistedAccountMismatchOrder(true),
		domain.MissingAccountCreate,
		testCaller,
	)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SubmitImmediate error = %v, want ErrInvalid", err)
	}
	if len(eng.submitCalls) != 0 {
		t.Fatalf("engine mutations = %+v, want none", eng.submitCalls)
	}
}

// TestLocalNode_SubmitOrderPersistsPreTradeBalances verifies the direct submit
// path mirrors the engine's balance effects into the snapshot, so held funds
// and incoming quantity show up before any fill settles.
func TestLocalNode_SubmitOrderPersistsPreTradeBalances(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitLock = fakeStoredLock(t)
	eng.submitOutcomes = []engine.BalanceOutcome{
		{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceResult: "8000",
				HeldDelta:     "2000",
				HeldResult:    "2000",
			},
		},
		{
			Asset: "AAPL",
			Outcome: domain.AdjustmentOutcomeAccepted{
				IncomingDelta:  "20",
				IncomingResult: "20",
			},
		},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "10000",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	order, err := n.SubmitOrder(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("order status = %q, want committed", order.Status)
	}
	if order.Leaves != "20" {
		t.Fatalf("order leaves = %q, want engine delta 20", order.Leaves)
	}

	quote, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance USD: %v ok=%v", err, ok)
	}
	if quote.Held != "2000" {
		t.Fatalf("USD held = %q, want 2000", quote.Held)
	}
	base, ok, err := st.GetBalance(ctx, "acc-1", "AAPL")
	if err != nil || !ok {
		t.Fatalf("GetBalance AAPL: %v ok=%v", err, ok)
	}
	if base.Incoming != "20" {
		t.Fatalf("AAPL incoming = %q, want 20", base.Incoming)
	}
}

func TestOpeningLeavesComeFromEngineBaseDelta(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		side     domain.OrderSide
		outcomes []engine.BalanceOutcome
		want     string
		wantErr  bool
	}{
		{
			name: "buy incoming", side: domain.OrderSideBuy,
			outcomes: []engine.BalanceOutcome{
				{
					Asset:   "USD",
					Outcome: domain.AdjustmentOutcomeAccepted{HeldDelta: "200"},
				},
				{
					Asset: "AAPL",
					Outcome: domain.AdjustmentOutcomeAccepted{
						HeldDelta: "999", IncomingDelta: "2",
					},
				},
			},
			want: "2",
		},
		{
			name: "sell held", side: domain.OrderSideSell,
			outcomes: []engine.BalanceOutcome{{
				Asset: "AAPL",
				Outcome: domain.AdjustmentOutcomeAccepted{
					HeldDelta: "2", IncomingDelta: "999",
				},
			}},
			want: "2",
		},
		{name: "missing base delta", side: domain.OrderSideBuy, want: "0"},
		{
			name: "unsupported side", side: domain.OrderSide("swap"),
			outcomes: []engine.BalanceOutcome{{
				Asset:   "AAPL",
				Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "2"},
			}},
			wantErr: true,
		},
		{
			name: "several base deltas", side: domain.OrderSideBuy,
			outcomes: []engine.BalanceOutcome{
				{
					Asset:   "AAPL",
					Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "2"},
				},
				{
					Asset:   "AAPL",
					Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "3"},
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := openingLeavesFromOutcomes(domain.Order{
				BaseAsset: "AAPL",
				Side:      tt.side,
			}, tt.outcomes)
			if tt.wantErr {
				if err == nil {
					t.Fatal("openingLeavesFromOutcomes accepted an unusable engine outcome")
				}
				if errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("engine outcome error = %v, want internal error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("openingLeavesFromOutcomes: %v", err)
			}
			if got != tt.want {
				t.Fatalf("opening leaves = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLocalNode_SubmitImmediateNilPersistenceIsInternal(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.emptyImmediatePersistence = true
	n, _ := newTestNode(t, eng)
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	_, _, err := n.SubmitImmediate(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err == nil {
		t.Fatal("SubmitImmediate succeeded with no persistence write set")
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SubmitImmediate error = %v, want internal error", err)
	}
	if len(eng.submitCalls) != 1 {
		t.Fatalf("submit calls = %+v, want one engine apply", eng.submitCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook not invoked for incomplete immediate persistence")
	}
}

// An accepted engine delta sets opening leaves; execution reports later replace
// them with the venue's reported value unchanged.
func TestLocalNode_VolumeOrderRecordsEngineOpeningLeaves(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitLock = fakeStoredLock(t)
	eng.submitOutcomes = []engine.BalanceOutcome{{
		Asset:   "AAPL",
		Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "5"},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	order, err := n.SubmitOrder(ctx, domain.Order{
		Account:   "acc-1",
		BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindVolume, AmountValue: "500", Price: "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Leaves != "5" {
		t.Fatalf("volume order leaves = %q, want engine delta 5", order.Leaves)
	}

	if _, err := n.ApplyExecutionReport(ctx,
		domain.ExecutionReportInput{
			Order: order.ExternalID, FillQuantity: "2", FillPrice: "100",
			LeavesQuantity: "3", OrderStatus: domain.OrderStatusPartiallyFilled,
		}, testCaller); err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Leaves != "3" {
		t.Fatalf("reported leaves = %q, want 3", detail.Order.Leaves)
	}
}

func TestLocalNode_CancelVolumeOrderUsesPreReportLeaves(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitLock = fakeStoredLock(t)
	eng.submitOutcomes = []engine.BalanceOutcome{{
		Asset:   "AAPL",
		Outcome: domain.AdjustmentOutcomeAccepted{IncomingDelta: "5"},
	}}
	n, _ := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	order, err := n.SubmitOrder(ctx, domain.Order{
		Account:   "acc-1",
		BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindVolume, AmountValue: "500", Price: "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Leaves != "5" {
		t.Fatalf("volume order leaves = %q, want engine delta 5", order.Leaves)
	}

	if _, _, err := n.CancelOrder(ctx, order.ExternalID, "0", testCaller); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
	}
	if got := eng.execReportLeaves[0]; got != "5" {
		t.Fatalf("cancellation reservation remainder = %q, want stored reservation 5", got)
	}
}

// An engine block does not make Officer derive, withhold, or annotate a
// different pre-report cancellation leaves value.
func TestLocalNode_CancelBlockOnlyResultAuditsPreReportLeaves(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.execReportBlocks = []domain.AccountBlock{
		{
			Account: "acc-1", Policy: "SpotFundsPolicy",
			Code: domain.RejectCodePnlKillSwitchTriggered, Reason: "account block triggered",
			Details: "account pnl below lower bound",
		},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	order := testOrder(t, st, "acc-1")

	if _, _, err := n.CancelOrder(
		ctx, order.ExternalID, "0", testCaller,
	); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if len(eng.execReportCalls) != 1 {
		t.Fatalf("engine calls = %+v, want one", eng.execReportCalls)
	}
	if got := eng.execReportLeaves[0]; got != "2" {
		t.Fatalf("cancellation reservation remainder = %q, want stored reservation 2", got)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionExecutionReport},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(execution report): %v", err)
	}
	if len(rows) != 1 ||
		!strings.Contains(rows[0].Detail, "leavesQty=0") ||
		!strings.Contains(rows[0].Detail, "reservationRemainder=2") ||
		strings.Contains(rows[0].Detail, "releaseApplied=") {
		t.Fatalf(
			"blocked cancellation audit = %+v, want no local release marker",
			rows,
		)
	}
}

func TestLocalNode_SubmitImmediatePassesVolumeToEngine(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, _ := newTestNode(t, eng)
	_, result, err := n.SubmitImmediate(
		context.Background(), domain.Order{
			Account:   "acc-1",
			BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
			AmountKind: domain.OrderAmountKindVolume, AmountValue: "500", Price: "100",
		}, domain.MissingAccountCreate, testCaller,
	)
	if err != nil {
		t.Fatalf("SubmitImmediate(volume): %v", err)
	}
	if !result.Accepted {
		t.Fatalf("SubmitImmediate(volume) = %+v, want accepted", result)
	}
	if len(eng.submitCalls) != 1 {
		t.Fatalf("volume immediate engine calls = %+v, want one", eng.submitCalls)
	}
	got := eng.submitCalls[0]
	if got.AmountKind != domain.OrderAmountKindVolume || got.AmountValue != "500" {
		t.Fatalf(
			"volume immediate engine amount = (%q, %q), want (volume, 500)",
			got.AmountKind,
			got.AmountValue,
		)
	}
}

func TestLocalNode_SubmitImmediateSeparatesTradeAndLockPrices(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitTradePrice = "99"
	eng.submitSettlementLockPrice = "101"
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	order, result, err := n.SubmitImmediate(ctx, domain.Order{
		Account:   "acc-1",
		BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, AmountValue: "2", Price: "99",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	if result.TradePrice != "99" || result.SettlementLockPrice != "101" {
		t.Fatalf("immediate result = %+v, want trade 99 and lock 101", result)
	}
	if order.Status != domain.OrderStatusFilled || order.Leaves != "0" {
		t.Fatalf("immediate order = %+v, want filled with zero leaves", order)
	}
	detail, err := st.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(detail.Trades) != 1 || detail.Trades[0].Price != "99" ||
		detail.Trades[0].LockPrice != "101" {
		t.Fatalf("trades = %+v, want price 99 and lock price 101", detail.Trades)
	}
	if detail.Order.Status != domain.OrderStatusFilled || detail.Order.Leaves != "0" {
		t.Fatalf("stored immediate order = %+v, want filled with zero leaves", detail.Order)
	}
	if result.ExecutionReport == nil || result.ExecutionReport.ExternalID.IsZero() {
		t.Fatalf("immediate execution report identity = %+v, want assigned id", result.ExecutionReport)
	}
	foundReport := false
	for _, event := range detail.Events {
		if event.Type != domain.OrderEventFill {
			continue
		}
		foundReport = true
		if event.Payload.FillPrice != "99" ||
			event.Payload.FillLockPrice != "101" ||
			event.Payload.LeavesQuantity != "0" ||
			event.Payload.OrderStatus != string(domain.OrderStatusFilled) ||
			event.Payload.ExecutionReport == nil ||
			event.Payload.ExecutionReport.ExternalID !=
				result.ExecutionReport.ExternalID {
			t.Fatalf("fill event = %+v, want complete linked execution report", event)
		}
	}
	if !foundReport {
		t.Fatal("immediate fill event missing")
	}
	audit, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionExecutionReport},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(execution report): %v", err)
	}
	// The immediate report settles its own fill, so the audited leaves are the
	// zero in the adapter-built report request.
	if len(audit) != 1 || audit[0].Account != "acc-1" ||
		!strings.Contains(audit[0].Detail, "qty=2 leavesQty=0 filled") ||
		!strings.Contains(audit[0].Detail, "reservationRemainder=0") {
		t.Fatalf("immediate execution-report audit = %+v", audit)
	}
}

func TestLocalNode_SubmitDropCopyPersistsBlockCallerAndAudit(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitBlocks = []domain.AccountBlock{{
		Account: "acc-1", Policy: "pnl_bounds", Code: "account_blocked",
		Reason: "kill-switch tripped", Details: "daily loss",
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	caller := domain.Caller{
		Source: domain.SourceAPI, Principal: domain.PrincipalOperator,
	}
	id := domain.ExternalID("drop-copy-order")
	order, err := n.SubmitOrder(ctx, domain.Order{
		ExternalID: id, Account: "acc-1", DropCopy: true,
		BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "100",
	}, domain.MissingAccountCreate, caller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Source != domain.SourceAPI || !order.DropCopy {
		t.Fatalf("order = %+v", order)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if !account.Blocked || !strings.Contains(account.BlockReason, "kill-switch tripped") {
		t.Fatalf("account = %+v", account)
	}
	detail, err := st.GetOrder(ctx, id)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	for _, event := range detail.Events {
		if event.Source != domain.SourceAPI {
			t.Fatalf("event = %+v, want api source", event)
		}
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Source:  domain.SourceAPI,
		Actions: []domain.AuditAction{domain.AuditActionSubmitDropCopy},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered: %v", err)
	}
	if len(rows) != 1 || rows[0].Account != "acc-1" ||
		rows[0].Actor != domain.PrincipalOperator {
		t.Fatalf("drop-copy audit rows = %+v", rows)
	}
}

func TestLocalNode_SubmitOrderAndImmediateRecordDecisionAudit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		submitImmediate bool
		externalID      domain.ExternalID
		dropCopy        bool
		rejected        bool
		action          domain.AuditAction
		wantExecution   int
		wantBlocks      int
	}{
		{
			name:       "submit order/accepted order",
			externalID: "regular-accepted-order",
			action:     domain.AuditActionSubmitOrder,
			wantBlocks: 1,
		},
		{
			name:       "submit order/rejected order",
			externalID: "regular-rejected-order",
			rejected:   true,
			action:     domain.AuditActionSubmitOrder,
		},
		{
			name:       "submit order/accepted drop copy",
			externalID: "regular-accepted-drop-copy",
			dropCopy:   true,
			action:     domain.AuditActionSubmitDropCopy,
			wantBlocks: 1,
		},
		{
			name:       "submit order/rejected drop copy",
			externalID: "regular-rejected-drop-copy",
			dropCopy:   true,
			rejected:   true,
			action:     domain.AuditActionSubmitDropCopy,
		},
		{
			name:            "submit immediate/accepted order",
			submitImmediate: true,
			externalID:      "immediate-accepted-order",
			action:          domain.AuditActionSubmitOrder,
			wantExecution:   1,
		},
		{
			name:            "submit immediate/rejected order",
			submitImmediate: true,
			externalID:      "immediate-rejected-order",
			rejected:        true,
			action:          domain.AuditActionSubmitOrder,
		},
		{
			name:            "submit immediate/accepted drop copy",
			submitImmediate: true,
			externalID:      "immediate-accepted-drop-copy",
			dropCopy:        true,
			action:          domain.AuditActionSubmitDropCopy,
			wantExecution:   1,
			wantBlocks:      1,
		},
		{
			name:            "submit immediate/rejected drop copy",
			submitImmediate: true,
			externalID:      "immediate-rejected-drop-copy",
			dropCopy:        true,
			rejected:        true,
			action:          domain.AuditActionSubmitDropCopy,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng := newFakeEngine()
			eng.submitBlocks = []domain.AccountBlock{{
				Account: "acc-1", Policy: "pnl_bounds",
				Code: "account_blocked", Reason: "kill-switch tripped",
			}}
			if tc.rejected {
				eng.submitReject = &domain.OrderReject{
					Code: "order_size", Scope: "account",
					Policy: "order-size",
				}
			}
			n, st := newTestNode(t, eng)
			ctx := context.Background()
			caller := domain.Caller{
				Source:    domain.SourceAPI,
				Principal: domain.PrincipalOperator,
			}
			input := domain.Order{
				ExternalID: tc.externalID,
				Account:    "acc-1", DropCopy: tc.dropCopy,
				BaseAsset: "AAPL", QuoteAsset: "USD",
				Side:        domain.OrderSideBuy,
				AmountKind:  domain.OrderAmountKindQuantity,
				AmountValue: "1", Price: "100",
			}
			var order domain.Order
			var err error
			if tc.submitImmediate {
				var result engine.ImmediateResult
				order, result, err = n.SubmitImmediate(
					ctx,
					input,
					domain.MissingAccountCreate,
					caller,
				)
				if err != nil {
					t.Fatalf("SubmitImmediate: %v", err)
				}
				if result.Accepted == tc.rejected {
					t.Fatalf(
						"accepted = %v, want %v",
						result.Accepted,
						!tc.rejected,
					)
				}
			} else {
				order, err = n.SubmitOrder(
					ctx,
					input,
					domain.MissingAccountCreate,
					caller,
				)
				if err != nil {
					t.Fatalf("SubmitOrder: %v", err)
				}
			}

			decisionRows, err := st.ListAuditFiltered(
				ctx,
				domain.AuditFilter{Actions: []domain.AuditAction{
					domain.AuditActionSubmitOrder,
					domain.AuditActionSubmitDropCopy,
				}},
				10,
			)
			if err != nil {
				t.Fatalf("ListAuditFiltered(decisions): %v", err)
			}
			if len(decisionRows) != 1 {
				t.Fatalf(
					"decision audit rows = %+v, want exactly one",
					decisionRows,
				)
			}
			got := decisionRows[0]
			wantVerdict := "accept"
			wantRejectCode := ""
			if tc.rejected {
				wantVerdict = "reject"
				wantRejectCode = "order_size"
			}
			if got.Action != tc.action ||
				got.OrderID != order.ExternalID.String() ||
				got.Verdict != wantVerdict ||
				got.RejectCode != wantRejectCode ||
				got.Account != "acc-1" ||
				got.Actor != domain.PrincipalOperator ||
				got.Source != domain.SourceAPI {
				t.Fatalf(
					"decision audit row = %+v, want action=%q order=%q "+
						"verdict=%q reject=%q account and caller",
					got,
					tc.action,
					order.ExternalID,
					wantVerdict,
					wantRejectCode,
				)
			}

			executionRows, err := st.ListAuditFiltered(
				ctx,
				domain.AuditFilter{Actions: []domain.AuditAction{
					domain.AuditActionExecutionReport,
				}},
				10,
			)
			if err != nil {
				t.Fatalf("ListAuditFiltered(execution): %v", err)
			}
			if len(executionRows) != tc.wantExecution {
				t.Fatalf(
					"execution audit rows = %+v, want %d",
					executionRows,
					tc.wantExecution,
				)
			}
			blockRows, err := st.ListAuditFiltered(
				ctx,
				domain.AuditFilter{Actions: []domain.AuditAction{
					domain.AuditActionBlock,
				}},
				10,
			)
			if err != nil {
				t.Fatalf("ListAuditFiltered(blocks): %v", err)
			}
			if len(blockRows) != tc.wantBlocks {
				t.Fatalf(
					"block audit rows = %+v, want %d",
					blockRows,
					tc.wantBlocks,
				)
			}
		})
	}
}

func TestLocalNode_SubmitImmediateDecisionAuditFailureFatals(t *testing.T) {
	t.Parallel()
	auditCause := errors.New("immediate decision audit failed")
	st := newRealmWrapStore(
		newMemoryStore("node.db"),
		func(r store.RealmStore) store.RealmStore {
			return &failActionAuditRealm{
				RealmStore: r,
				action:     domain.AuditActionSubmitOrder,
				err:        auditCause,
			}
		},
	)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(
		t,
		st,
		eng,
		func(err error) {
			fatalErr = err
		},
	)
	_, _, err := n.SubmitImmediate(
		ctx,
		domain.Order{
			Account:   "acc-1",
			BaseAsset: "AAPL", QuoteAsset: "USD",
			Side:        domain.OrderSideBuy,
			AmountKind:  domain.OrderAmountKindQuantity,
			AmountValue: "1", Price: "100",
		},
		domain.MissingAccountCreate,
		testCaller,
	)
	if !errors.Is(err, auditCause) {
		t.Fatalf("SubmitImmediate error = %v, want audit cause", err)
	}
	if !errors.Is(err, asyncengine.ErrChainRetryUnsafe) {
		t.Fatalf("SubmitImmediate error = %v, want retry-unsafe marker", err)
	}
	if fatalErr == nil || !errors.Is(fatalErr, auditCause) {
		t.Fatalf("fatal error = %v, want audit cause", fatalErr)
	}
	if !strings.Contains(fatalErr.Error(), `operation="audit submit order"`) {
		t.Fatalf("fatal error = %q, want audit operation", fatalErr)
	}
	if len(eng.submitCalls) != 1 {
		t.Fatalf("submit calls = %+v, want one engine apply", eng.submitCalls)
	}
}

func TestLocalNode_SubmitImmediateRejectWithoutReasonFailsBeforePersistence(
	t *testing.T,
) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitReject = &domain.OrderReject{}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	const externalID domain.ExternalID = "immediate-reject-without-reason"

	_, _, err := n.SubmitImmediate(
		ctx,
		domain.Order{
			Account:    "acc-1",
			ExternalID: externalID,
			BaseAsset:  "AAPL", QuoteAsset: "USD",
			Side:        domain.OrderSideBuy,
			AmountKind:  domain.OrderAmountKindQuantity,
			AmountValue: "1", Price: "100",
		},
		domain.MissingAccountCreate,
		testCaller,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "engine rejection has no reject reason") {
		t.Fatalf("SubmitImmediate error = %v, want missing reject reason", err)
	}
	if _, getErr := st.GetOrder(ctx, externalID); !errors.Is(
		getErr,
		domain.ErrNotFound,
	) {
		t.Fatalf("GetOrder after rejected submission = %v, want not found", getErr)
	}
	decisionRows, listErr := st.ListAuditFiltered(
		ctx,
		domain.AuditFilter{Actions: []domain.AuditAction{
			domain.AuditActionSubmitOrder,
			domain.AuditActionSubmitDropCopy,
		}},
		10,
	)
	if listErr != nil {
		t.Fatalf("ListAuditFiltered: %v", listErr)
	}
	if len(decisionRows) != 0 {
		t.Fatalf(
			"missing-reason submission persisted decision rows: %+v",
			decisionRows,
		)
	}
	if len(eng.submitCalls) != 1 {
		t.Fatalf("submit calls = %+v, want one engine rejection", eng.submitCalls)
	}
}

func TestLocalNode_SubmitOrderAcceptedPathRejectWithoutReasonFatals(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	st := newMemoryStore("node.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	baseEngine := newFakeEngine()
	eng := &rejectWithoutReasonAfterReservationEngine{fakeEngine: baseEngine}
	var captured engine.Snapshot
	baseBuild := fakeBuild(baseEngine, &captured)
	build := func(snapshot engine.Snapshot) (engine.Engine, error) {
		if _, err := baseBuild(snapshot); err != nil {
			return nil, err
		}
		return eng, nil
	}
	var fatalErr error
	service, _, err := NewLocalNode(
		ctx,
		domain.DefaultRealm,
		st,
		build,
		func(err error) {
			fatalErr = err
		},
	)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	n := service.(*localNode)
	seedTestPrincipal(t, n)

	const externalID domain.ExternalID = "accepted-path-reject-without-reason"
	_, err = n.SubmitOrder(
		ctx,
		domain.Order{
			Account:     "acc-1",
			ExternalID:  externalID,
			BaseAsset:   "AAPL",
			QuoteAsset:  "USD",
			Side:        domain.OrderSideBuy,
			AmountKind:  domain.OrderAmountKindQuantity,
			AmountValue: "1",
			Price:       "100",
		},
		domain.MissingAccountCreate,
		testCaller,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "engine rejection has no reject reason") {
		t.Fatalf("SubmitOrder error = %v, want missing reject reason", err)
	}
	if !errors.Is(err, asyncengine.ErrChainRetryUnsafe) {
		t.Fatalf("SubmitOrder error = %v, want retry-unsafe marker", err)
	}
	if len(baseEngine.submitCalls) != 1 {
		t.Fatalf(
			"submit calls = %+v, want one applied reservation",
			baseEngine.submitCalls,
		)
	}
	if fatalErr == nil ||
		!strings.Contains(fatalErr.Error(), "operation=\"audit submit order\"") ||
		!strings.Contains(fatalErr.Error(), "engine rejection has no reject reason") {
		t.Fatalf("fatal error = %v, want audit operation and missing reason", fatalErr)
	}
	if _, getErr := n.realm.GetOrder(ctx, externalID); getErr != nil {
		t.Fatalf("GetOrder after fatal audit failure: %v", getErr)
	}
}

func TestLocalNode_SubmitImmediatePanelDoesNotInferAccountPnl(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitOutcomes = []engine.BalanceOutcome{
		{
			Asset: "AAPL",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceResult: "2", RealizedPnlResult: "9",
			},
		},
		{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceResult: "800", RealizedPnlResult: "2.5",
			},
		},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Currency: "USD",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	panelCaller := testCaller
	panelCaller.Source = domain.SourcePanel
	order, result, err := n.SubmitImmediate(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, panelCaller)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	if !result.Accepted || order.Source != domain.SourcePanel {
		t.Fatalf("immediate result=%+v order=%+v, want accepted panel order", result, order)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "0" {
		t.Fatalf("account pnl = %q, want unchanged 0", account.Pnl)
	}
	quote, ok, err := st.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance USD: ok=%v err=%v", ok, err)
	}
	if quote.RealizedPnl != "2.5" {
		t.Fatalf("quote realized pnl = %q, want 2.5", quote.RealizedPnl)
	}
}

func TestLocalNode_SubmitImmediatePersistsAuthoritativeAccountPnl(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitAccountPnl = "12.340"
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code:          "acc-1",
		Currency:      "USD",
		Pnl:           "7",
		PnlHaltReason: domain.PnlHaltReasonMissingFx,
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, result, err := n.SubmitImmediate(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	if result.AccountPnl != "12.340" || result.AccountPnlHaltReason != "" {
		t.Fatalf("immediate result = %+v, want authoritative account pnl", result)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "12.340" || account.PnlHaltReason != "" {
		t.Fatalf("account = %+v, want pnl 12.340 and cleared halt", account)
	}
}

func TestLocalNode_SubmitImmediatePersistsAuthoritativeAccountPnlHalt(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitAccountPnlHaltReason = domain.PnlHaltReasonArithmeticOverflow
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Currency: "USD", Pnl: "7",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, result, err := n.SubmitImmediate(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	if result.AccountPnl != "" ||
		result.AccountPnlHaltReason != domain.PnlHaltReasonArithmeticOverflow {
		t.Fatalf("immediate result = %+v, want arithmetic-overflow halt", result)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "7" ||
		account.PnlHaltReason != domain.PnlHaltReasonArithmeticOverflow {
		t.Fatalf("account = %+v, want preserved pnl and arithmetic-overflow halt", account)
	}
}

func TestLocalNode_SubmitImmediateLeavesAccountPnlWithoutMatchingOutcome(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitOutcomes = []engine.BalanceOutcome{{
		Asset: "AAPL",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceResult: "2", RealizedPnlResult: "99",
		},
	}}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Currency: "USD", Pnl: "7",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, _, err := n.SubmitImmediate(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "7" {
		t.Fatalf("account pnl = %q, want unchanged 7", account.Pnl)
	}
}

func TestLocalNode_SubmitImmediateDoesNotSelectAccountPnlFromBalanceOutcomes(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitOutcomes = []engine.BalanceOutcome{
		{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceResult: "800", RealizedPnlResult: "2.5",
			},
		},
		{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				RealizedPnlResult: "3.5",
			},
		},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Currency: "USD",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, _, err := n.SubmitImmediate(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "0" {
		t.Fatalf("account pnl = %q, want unchanged 0", account.Pnl)
	}
}

func TestLocalNode_SubmitImmediateDoesNotUseEffectiveCurrencyToInferAccountPnl(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.submitOutcomes = []engine.BalanceOutcome{
		{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				RealizedPnlResult: "2.5",
			},
		},
		{
			Asset: "EUR",
			Outcome: domain.AdjustmentOutcomeAccepted{
				RealizedPnlResult: "4.5",
			},
		},
	}
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Currency: "USD",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	n.engineMu.Lock()
	n.engine = &currencyChangingOrderModelEngine{
		fakeEngine: eng,
		beforeLane: func() error {
			return st.SetAccountCurrency(ctx, "acc-1", "EUR")
		},
	}
	n.engineMu.Unlock()

	_, _, err := n.SubmitImmediate(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	account, ok, err := st.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.EffectiveCurrency != "EUR" || account.Pnl != "0" {
		t.Fatalf("account = %+v, want effective EUR and unchanged pnl", account)
	}
}

type currencyChangingOrderModelEngine struct {
	*fakeEngine
	beforeLane func() error
}

func (e *currencyChangingOrderModelEngine) OrderModel(
	order domain.Order,
) (model.Order, error) {
	if err := e.beforeLane(); err != nil {
		return model.Order{}, err
	}
	return e.fakeEngine.OrderModel(order)
}

func TestLocalNode_SubmitOrderPostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("record order submission failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failOrderSubmissionAfterApplyRealm{RealmStore: r, err: storeErr}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, func(err error) {
		fatalErr = err
	})
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, err := n.SubmitOrder(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, storeErr) {
		t.Fatalf("SubmitOrder error = %v, want store failure", err)
	}
	if !errors.Is(err, asyncengine.ErrChainRetryUnsafe) {
		t.Fatalf("SubmitOrder error = %v, want retry-unsafe marker", err)
	}
	if len(eng.submitCalls) != 1 {
		t.Fatalf("submit calls = %+v, want one engine apply", eng.submitCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine order persistence failure")
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="record order submission"`) ||
		!strings.Contains(msg, "account=acc-1") ||
		!strings.Contains(msg, "record order submission failed") {
		t.Fatalf("fatal error = %q, want operation and cause", msg)
	}
}

// TestLocalNode_SubmitImmediateDoesNotReadAccountDuringSubmissionApply guards
// against reading the realm while RecordOrderSubmission owns its transaction.
func TestLocalNode_SubmitImmediateDoesNotReadAccountDuringSubmissionApply(
	t *testing.T,
) {
	t.Parallel()
	var guard *accountReadGuardRealm
	st := newRealmWrapStore(
		newMemoryStore("node.db"),
		func(realm store.RealmStore) store.RealmStore {
			guard = &accountReadGuardRealm{RealmStore: realm}
			return guard
		},
	)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	n := newTestNodeWithStore(t, st, newFakeEngine(), failOnFatal(t))
	if _, err := n.CreateAccount(
		ctx, testAccount("acc-1"), testCaller,
	); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	order, result, err := n.SubmitImmediate(
		ctx,
		domain.Order{
			Account:     "acc-1",
			BaseAsset:   "AAPL",
			QuoteAsset:  "USD",
			Side:        domain.OrderSideBuy,
			AmountKind:  domain.OrderAmountKindQuantity,
			AmountValue: "20",
			Price:       "100",
		},
		domain.MissingAccountCreate, testCaller,
	)
	if err != nil {
		t.Fatalf("SubmitImmediate: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("order=%+v result=%+v, want accepted", order, result)
	}
	if guard == nil {
		t.Fatal("account read guard was not installed")
	}
	if guard.accountReadDuringApply {
		t.Fatal("GetAccount was called during order submission apply")
	}
}

// TestLocalNode_SubmitImmediatePostEngineStoreFailureFatals proves the engine evaluates
// inside RecordOrderSubmission's apply callback, then the store write fails, so
// the settlement can never be persisted and the node must fail-stop with the
// "record immediate submission" operation label.
func TestLocalNode_SubmitImmediatePostEngineStoreFailureFatals(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("record immediate submission failed")
	st := newRealmWrapStore(newMemoryStore("node.db"), func(r store.RealmStore) store.RealmStore {
		return &failOrderSubmissionAfterApplyRealm{RealmStore: r, err: storeErr}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, func(err error) {
		fatalErr = err
	})
	if _, err := n.CreateAccount(ctx, testAccount("acc-1"), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_, _, err := n.SubmitImmediate(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, storeErr) {
		t.Fatalf("SubmitImmediate error = %v, want store failure", err)
	}
	if !errors.Is(err, asyncengine.ErrChainRetryUnsafe) {
		t.Fatalf("SubmitImmediate error = %v, want retry-unsafe marker", err)
	}
	if len(eng.submitCalls) != 1 {
		t.Fatalf("submit calls = %+v, want one engine apply", eng.submitCalls)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on post-engine immediate persistence failure")
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="record immediate submission"`) ||
		!strings.Contains(msg, "account=acc-1") ||
		!strings.Contains(msg, "record immediate submission failed") {
		t.Fatalf("fatal error = %q, want operation and cause", msg)
	}
}

func TestLocalNode_SubmitOrderAutoCreatesUnknownAsset(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	order, err := n.SubmitOrder(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "GOLD",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("order status = %q, want committed", order.Status)
	}

	asset, ok, err := st.GetAsset(ctx, "GOLD")
	if err != nil || !ok {
		t.Fatalf("GetAsset(GOLD) = ok %v err %v, want auto-created", ok, err)
	}
	if asset.AssetClass != autoCreatedAssetClassCode {
		t.Fatalf("auto-created asset class = %q, want %q", asset.AssetClass,
			autoCreatedAssetClassCode)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAsset},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create asset): %v", err)
	}
	if len(rows) != 1 ||
		rows[0].Asset != "GOLD" ||
		!strings.Contains(rows[0].Detail, "auto-created asset GOLD by submit order") {
		t.Fatalf("create-asset audit rows = %+v, want submit-order auto-create", rows)
	}
}

type failingAssetResolverEngine struct {
	*fakeEngine
	err error
}

func (e *failingAssetResolverEngine) AddAssetResolverEntry(domain.Asset) error {
	return e.err
}

// TestLocalNode_SubmitOrderAutoCreateAssetRollbackSuccessIsInternal covers the
// successful-rollback branch of rollbackAutoCreatedAssetPublication: the store
// write for the auto-created asset already committed before the resolver
// publish failed, so even though the compensating delete restores a consistent
// state, the failure is Officer's to own. The caller must not see it classified
// as the domain sentinel the resolver failure carried (which would surface as a
// 409 for an order that referenced a perfectly fine, if unknown, asset code),
// while the underlying diagnostic cause must still be reachable for logs.
func TestLocalNode_SubmitOrderAutoCreateAssetRollbackSuccessIsInternal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newMemoryStore("auto-create-asset-rollback.db")
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	base := newFakeEngine()
	diagnosticCause := errors.New("asset dictionary desync detail")
	resolverErr := fmt.Errorf(
		"engine: asset resolver alias %q already exists: %w: %w",
		"GOLD", domain.ErrAlreadyExists, diagnosticCause,
	)
	var captured engine.Snapshot
	inner := fakeBuild(base, &captured)
	nn, _, err := NewLocalNode(ctx, domain.DefaultRealm, st, func(snap engine.Snapshot) (engine.Engine, error) {
		if _, buildErr := inner(snap); buildErr != nil {
			return nil, buildErr
		}
		return &failingAssetResolverEngine{fakeEngine: base, err: resolverErr}, nil
	}, failOnFatal(t))
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nn.(*localNode)
	seedTestPrincipal(t, n)
	seedTestAccount(t, n.realm, "acc-1")
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	_, submitErr := n.SubmitOrder(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "GOLD",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if submitErr == nil {
		t.Fatal("SubmitOrder succeeded, want asset resolver publication failure")
	}
	if errors.Is(submitErr, domain.ErrAlreadyExists) {
		t.Fatalf(
			"SubmitOrder error = %v, must not expose the domain sentinel after a "+
				"committed auto-create rollback",
			submitErr,
		)
	}
	if !errors.Is(submitErr, diagnosticCause) {
		t.Fatalf("SubmitOrder error = %v, want diagnostic cause preserved", submitErr)
	}
	if _, ok, getErr := n.realm.GetAsset(ctx, "GOLD"); getErr != nil || ok {
		t.Fatalf("GetAsset(GOLD) after rollback: ok=%v err=%v, want removed", ok, getErr)
	}
	if fatalErr != nil {
		t.Fatalf(
			"fatal error = %v, want successful rollback without a fatal", fatalErr,
		)
	}
}

func TestLocalNode_AutoCreatedAssetAuditSurvivesRequestCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := newFakeEngine()
	eng.afterAssetResolverAdd = cancel
	n, _ := newTestNode(t, eng)
	var fatalErr error
	n.fatal = func(err error) { fatalErr = err }

	if _, err := n.CreateAccount(ctx, domain.Account{
		Code:     "asset-audit-cancel",
		Currency: "GOLD",
	}, testCaller); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateAccount error = %v, want context cancellation", err)
	}
	if ctx.Err() == nil {
		t.Fatal("asset resolver publication did not cancel the request context")
	}
	if fatalErr != nil {
		t.Fatalf("auto-created asset audit triggered fatal path: %v", fatalErr)
	}
	rows, err := n.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, row := range rows {
		if row.Action == domain.AuditActionCreateAsset && row.Asset == "GOLD" {
			return
		}
	}
	t.Fatalf("audit rows = %+v, want auto-created GOLD asset", rows)
}

// TestLocalNode_SubmitOrderAutoCreatesUnknownAccount checks that an order
// submitted for an account Officer does not know yet auto-creates it (like a
// fresh adjustment target), so the engine resolves the account and the order
// is processed rather than rejected as invalid, and the creation is audited.
func TestLocalNode_SubmitOrderAutoCreatesUnknownAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	// Enforce the resolver so a submit against an unknown account would error
	// unless the auto-create publishes the account before the lane starts.
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	order, err := n.SubmitOrder(ctx, domain.Order{
		Account:     "fresh",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.Status != domain.OrderStatusCommitted {
		t.Fatalf("order status = %q, want committed", order.Status)
	}

	if _, ok, err := st.GetAccount(ctx, "fresh"); err != nil || !ok {
		t.Fatalf("GetAccount(fresh) = ok %v err %v, want auto-created", ok, err)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAccount},
		Account: "fresh",
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create account): %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "auto-created account fresh by submit order") {
		t.Fatalf("create-account audit rows = %+v, want submit-order auto-create", rows)
	}
}

// TestLocalNode_SubmitOrderHonorsSuppliedExternalID covers the create-once
// id-honoring path end-to-end through the real store: a caller-supplied order
// external id is used verbatim and the returned order carries it, and exactly
// one order row is created under it.
func TestLocalNode_SubmitOrderHonorsSuppliedExternalID(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	supplied := externalID(t, "supplied-order-id")
	order, err := n.SubmitOrder(ctx, domain.Order{
		Account:     "acc-1",
		ExternalID:  supplied,
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.ExternalID != supplied {
		t.Fatalf("returned order id = %q, want supplied %q", order.ExternalID, supplied)
	}
	detail, err := st.GetOrder(ctx, supplied)
	if err != nil {
		t.Fatalf("GetOrder by supplied id: %v", err)
	}
	if detail.Order.ExternalID != supplied {
		t.Fatalf("stored order id = %q, want supplied %q", detail.Order.ExternalID, supplied)
	}
}

// TestLocalNode_SubmitOrderGeneratesExternalIDWhenAbsent covers the absent-id
// path: a zero id leaves the store to mint a canonical one.
func TestLocalNode_SubmitOrderGeneratesExternalIDWhenAbsent(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	order, err := n.SubmitOrder(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "20",
		Price:       "100",
	}, domain.MissingAccountCreate, testCaller)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if order.ExternalID.IsZero() {
		t.Fatalf("order id is zero, want a generated id")
	}
	if err := domain.ValidateExternalID(order.ExternalID.String()); err != nil {
		t.Fatalf("generated order id not canonical: %v", err)
	}
}

// TestLocalNode_SubmitOrderDuplicateSuppliedIDConflicts covers the conflict
// path: a second submit reusing a supplied id surfaces domain.ErrAlreadyExists
// from the store, propagated unchanged.
func TestLocalNode_SubmitOrderDuplicateSuppliedIDConflicts(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	supplied := externalID(t, "dup-order-id")
	mk := func() domain.Order {
		return domain.Order{
			Account:     "acc-1",
			ExternalID:  supplied,
			BaseAsset:   "AAPL",
			QuoteAsset:  "USD",
			Side:        domain.OrderSideBuy,
			AmountKind:  domain.OrderAmountKindQuantity,
			AmountValue: "20",
			Price:       "100",
		}
	}
	if _, err := n.SubmitOrder(ctx, mk(), domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("first SubmitOrder: %v", err)
	}
	_, err := n.SubmitOrder(ctx, mk(), domain.MissingAccountCreate, testCaller)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate supplied id error = %v, want ErrAlreadyExists", err)
	}
}

func TestLocalNode_SubmitOrderDifferentAccountsProceedConcurrently(t *testing.T) {
	t.Parallel()
	entered := make(chan domain.AccountID, 2)
	release := make(chan struct{})
	eng := newFakeEngine()
	eng.submitEntered = entered
	eng.submitRelease = release
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")
	seedTestAccount(t, st, "acc-2")

	submit := func(account domain.AccountID, errs chan<- error) {
		_, err := n.SubmitOrder(ctx, domain.Order{
			Account:   account,
			BaseAsset: "AAPL", QuoteAsset: "USD",
			Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
			AmountValue: "1", Price: "10",
		}, domain.MissingAccountCreate, testCaller)
		errs <- err
	}
	errs := make(chan error, 2)
	go submit("acc-1", errs)
	if got := <-entered; got != "acc-1" {
		t.Fatalf("first entered account = %s, want acc-1", got)
	}
	go submit("acc-2", errs)
	select {
	case got := <-entered:
		if got != "acc-2" {
			t.Fatalf("second entered account = %s, want acc-2", got)
		}
	case <-time.After(time.Second):
		t.Fatal("second account did not enter SubmitOrder while first account was in-flight")
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("SubmitOrder #%d: %v", i, err)
		}
	}
}

func TestLocalNode_SubmitOrderSameAccountSerializes(t *testing.T) {
	t.Parallel()
	entered := make(chan domain.AccountID, 2)
	release := make(chan struct{})
	eng := newFakeEngine()
	eng.submitEntered = entered
	eng.submitRelease = release
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	seedTestAccount(t, st, "acc-1")

	submit := func(errs chan<- error) {
		_, err := n.SubmitOrder(ctx, domain.Order{
			Account:   "acc-1",
			BaseAsset: "AAPL", QuoteAsset: "USD",
			Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
			AmountValue: "1", Price: "10",
		}, domain.MissingAccountCreate, testCaller)
		errs <- err
	}
	errs := make(chan error, 2)
	go submit(errs)
	if got := <-entered; got != "acc-1" {
		t.Fatalf("first entered account = %s, want acc-1", got)
	}
	go submit(errs)
	select {
	case got := <-entered:
		t.Fatalf("second same-account SubmitOrder entered early as %s", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if got := <-entered; got != "acc-1" {
		t.Fatalf("second entered account = %s, want acc-1", got)
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("SubmitOrder #%d: %v", i, err)
		}
	}
}

func occupyOrderChainLane(
	t *testing.T, n *localNode, eng *fakeEngine,
) (func(), <-chan error) {
	t.Helper()
	entered := make(chan domain.AccountID, 2)
	release := make(chan struct{})
	eng.submitEntered = entered
	eng.submitRelease = release
	var releaseOnce sync.Once
	releaseLane := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseLane)
	result := make(chan error, 1)
	go func() {
		_, err := n.SubmitOrder(
			context.Background(),
			domain.Order{
				Account:   "acc-1",
				BaseAsset: "AAPL", QuoteAsset: "USD",
				Side:        domain.OrderSideBuy,
				AmountKind:  domain.OrderAmountKindQuantity,
				AmountValue: "1", Price: "10",
			},
			domain.MissingAccountCreate,
			testCaller,
		)
		result <- err
	}()
	select {
	case account := <-entered:
		if account != "acc-1" {
			t.Fatalf("occupied lane account = %s, want acc-1", account)
		}
	case <-time.After(time.Second):
		t.Fatal("first submission did not occupy the account lane")
	}
	return releaseLane, result
}

func TestLocalNode_SubmitOrderCancellationWhileLaneOccupied(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	seedTestAccount(t, st, "acc-1")
	release, first := occupyOrderChainLane(t, n, eng)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		_, err := n.SubmitOrder(
			ctx,
			domain.Order{
				Account:   "acc-1",
				BaseAsset: "AAPL", QuoteAsset: "USD",
				Side:        domain.OrderSideBuy,
				AmountKind:  domain.OrderAmountKindQuantity,
				AmountValue: "1", Price: "10",
			},
			domain.MissingAccountCreate,
			testCaller,
		)
		result <- err
	}()
	<-started
	cancel()
	release()
	if err := <-first; err != nil {
		t.Fatalf("occupying SubmitOrder: %v", err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled SubmitOrder error = %v, want context.Canceled", err)
	}
	if len(eng.submitCalls) != 1 {
		t.Fatalf("engine submissions = %d, want only the occupying call", len(eng.submitCalls))
	}
}

func TestLocalNode_SubmitImmediateCancellationWhileLaneOccupied(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	seedTestAccount(t, st, "acc-1")
	release, first := occupyOrderChainLane(t, n, eng)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		_, _, err := n.SubmitImmediate(
			ctx,
			domain.Order{
				Account:   "acc-1",
				BaseAsset: "AAPL", QuoteAsset: "USD",
				Side:        domain.OrderSideBuy,
				AmountKind:  domain.OrderAmountKindQuantity,
				AmountValue: "1", Price: "10",
			},
			domain.MissingAccountCreate,
			testCaller,
		)
		result <- err
	}()
	<-started
	cancel()
	release()
	if err := <-first; err != nil {
		t.Fatalf("occupying SubmitOrder: %v", err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled SubmitImmediate error = %v, want context.Canceled", err)
	}
	if len(eng.submitCalls) != 1 {
		t.Fatalf("engine submissions = %d, want only the occupying call", len(eng.submitCalls))
	}
}

func TestLocalNode_CheckOrderCancellationWhileLaneOccupied(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	seedTestAccount(t, st, "acc-1")
	release, first := occupyOrderChainLane(t, n, eng)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		_, err := n.CheckOrder(ctx, domain.OrderProbe{
			Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
			Side:        domain.OrderSideBuy,
			AmountKind:  domain.OrderAmountKindQuantity,
			AmountValue: "1", Price: "10",
		})

		result <- err
	}()
	<-started
	cancel()
	release()
	if err := <-first; err != nil {
		t.Fatalf("occupying SubmitOrder: %v", err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled CheckOrder error = %v, want context.Canceled", err)
	}
	if len(eng.checkProbes) != 0 {
		t.Fatalf("engine dry-runs = %d, want none", len(eng.checkProbes))
	}
}

func TestChainRootCausePreservesNonChainDiagnostics(t *testing.T) {
	t.Parallel()
	settlement := fmt.Errorf("settle broker leg: %w", errors.New("EOF"))
	rollback := errors.New("native rollback failed")
	cause := chainRootCause(errors.Join(
		fmt.Errorf("async chain apply execution report: %w", settlement),
		asyncengine.ErrChainRetryUnsafe,
		rollback,
	))
	if cause == nil {
		t.Fatal("chainRootCause returned nil")
	}
	diagnostic := cause.Error()
	if !strings.Contains(diagnostic, "settle broker leg: EOF") {
		t.Fatalf("cause = %q, want wrapped settlement diagnostic", diagnostic)
	}
	if !strings.Contains(diagnostic, rollback.Error()) {
		t.Fatalf("cause = %q, want rollback diagnostic", diagnostic)
	}
	if strings.Contains(diagnostic, asyncengine.ErrChainRetryUnsafe.Error()) {
		t.Fatalf("cause = %q, must omit retry-unsafe sentinel", diagnostic)
	}
}

// TestLocalNode_ApplyAdjustmentHonorsSuppliedExternalID covers the user-create
// adjustment path end-to-end: a supplied id is carried onto the record and
// persisted verbatim; a duplicate surfaces domain.ErrAlreadyExists.
