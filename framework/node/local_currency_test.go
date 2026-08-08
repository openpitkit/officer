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
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

func TestLocalNode_SetAccountCurrencyAuditsAndGuardsOpenBalances(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	createCurrencyAssets(t, st, "USD", "EUR")

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetAccountCurrency(ctx, testKey(id), "USD", testCaller); err != nil {
		t.Fatalf("SetAccountCurrency: %v", err)
	}
	account, _, err := n.GetAccountState(ctx, testKey(id))
	if err != nil {
		t.Fatalf("GetAccountState: %v", err)
	}
	if account.Currency != "USD" ||
		account.EffectiveCurrency != "USD" ||
		account.CurrencyOrigin != domain.CurrencyOriginAccount {
		t.Fatalf("account currency = %+v", account)
	}
	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if rows[0].Action != domain.AuditActionSetAccountCurrency ||
		!strings.Contains(rows[0].Detail, "<unset> -> USD") {
		t.Fatalf("currency audit = %+v", rows[0])
	}

	if err := st.UpsertBalance(ctx, domain.Balance{
		Account:   id,
		Asset:     "USD",
		Available: "1",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	err = n.SetAccountCurrency(ctx, testKey(id), "EUR", testCaller)
	if !errors.Is(err, domain.ErrConflict) ||
		!strings.Contains(err.Error(), "carry state that prevents a currency change") {
		t.Fatalf("SetAccountCurrency guarded = %v, want ErrConflict", err)
	}
}

func TestLocalNode_UpdateAssetRenameUsesPlainStoreUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := newMemoryStore("asset-update-rename.db")
	t.Cleanup(func() { _ = base.Close() })
	probe := &assetUpdateProbeRealm{}
	st := newRealmWrapStore(base, func(realm store.RealmStore) store.RealmStore {
		probe.RealmStore = realm
		return probe
	})
	eng := newFakeEngine()
	n := newTestNodeWithStore(t, st, eng)
	createCurrencyAssets(t, n.realm, "AAPL", "USD")

	if _, err := n.realm.CreateGroup(ctx, domain.AccountGroup{
		Code: "desk", Currency: "USD",
	}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	const accountID domain.AccountID = "account-1"
	if _, err := n.realm.CreateAccount(ctx, domain.Account{
		Code: accountID, GroupCode: "desk",
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.realm.UpsertBalance(ctx, domain.Balance{
		Account: accountID, Asset: "USD", Available: "1",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	order, err := n.realm.CreateOrder(ctx, domain.Order{
		Account:     accountID,
		Source:      domain.SourceAPI,
		Principal:   "operator",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "2",
		Leaves:      "2",
		Price:       "400",
		Status:      domain.OrderStatusCommitted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	instance, err := n.realm.CreateMarketDataInstance(
		ctx,
		domain.MarketDataInstance{
			Provider: domain.MarketDataProviderBYO,
			Label:    "manual",
		},
	)
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	if err := n.realm.UpsertMarketDataInstrument(
		ctx,
		domain.MarketDataInstrument{
			Instance:       instance.ExternalID,
			ExternalSymbol: "AAPLUSD",
			BaseAsset:      "AAPL",
			QuoteAsset:     "USD",
		},
	); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	auditsBefore, err := n.realm.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("ListAudit before rename: %v", err)
	}

	buildCalls := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		buildCalls++
		return nil, errors.New("asset update must not rebuild the engine")
	}
	updated, err := n.UpdateAsset(
		ctx, "USD", domain.Asset{Code: "USDX"}, testCaller,
	)
	if err != nil || updated.Code != "USDX" {
		t.Fatalf("UpdateAsset = %+v, %v", updated, err)
	}
	if probe.updateCalls != 1 {
		t.Fatalf("store UpdateAsset calls = %d, want 1", probe.updateCalls)
	}
	rows, err := n.realm.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != len(auditsBefore)+1 ||
		rows[0].Action != domain.AuditActionUpdateAsset ||
		rows[0].Asset != "USDX" ||
		rows[0].Detail != "update asset USD -> USDX" {
		t.Fatalf("asset rename audit = %+v", rows)
	}
	if buildCalls != 0 || n.currentEngine() != eng || !eng.running {
		t.Fatalf(
			"asset rename build calls=%d current-original=%v original-running=%v",
			buildCalls,
			n.currentEngine() == eng,
			eng.running,
		)
	}
	account, ok, err := n.realm.GetAccount(ctx, accountID)
	if err != nil || !ok || account.EffectiveCurrency != "USDX" {
		t.Fatalf("account after rename = %+v ok=%v err=%v", account, ok, err)
	}
	detail, err := n.realm.GetOrder(ctx, order.ExternalID)
	if err != nil || detail.Order.QuoteAsset != "USDX" {
		t.Fatalf("order after rename = %+v err=%v", detail.Order, err)
	}
	instruments, err := n.realm.ListMarketDataInstruments(ctx, instance.ExternalID)
	if err != nil || len(instruments) != 1 || instruments[0].QuoteAsset != "USDX" {
		t.Fatalf("market-data instruments after rename = %+v err=%v", instruments, err)
	}
}

func TestLocalNode_UpdateAssetSameCodeAuditsPlainDetail(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	createCurrencyAssets(t, st, "USD")
	auditsBefore, err := st.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("ListAudit before update: %v", err)
	}

	updated, err := n.UpdateAsset(
		ctx,
		"USD",
		domain.Asset{Code: "USD", Title: "US Dollar"},
		testCaller,
	)
	if err != nil || updated.Title != "US Dollar" {
		t.Fatalf("UpdateAsset = %+v, %v", updated, err)
	}
	rows, err := st.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != len(auditsBefore)+1 ||
		rows[0].Action != domain.AuditActionUpdateAsset ||
		rows[0].Asset != "USD" ||
		rows[0].Detail != "update asset USD" {
		t.Fatalf("same-code asset update audit = %+v", rows)
	}
}

type assetUpdateProbeRealm struct {
	store.RealmStore
	updateCalls int
}

func (r *assetUpdateProbeRealm) UpdateAsset(
	ctx context.Context, oldCode string, asset domain.Asset,
) (domain.Asset, error) {
	r.updateCalls++
	return r.RealmStore.UpdateAsset(ctx, oldCode, asset)
}

func TestLocalNode_SetAccountCurrencyGuardsRealizedPnlRows(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	createCurrencyAssets(t, st, "USD", "EUR")

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetAccountCurrency(ctx, testKey(id), "USD", testCaller); err != nil {
		t.Fatalf("SetAccountCurrency: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account:     id,
		Asset:       "USD",
		RealizedPnl: "3.25",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	err := n.SetAccountCurrency(ctx, testKey(id), "EUR", testCaller)
	if !errors.Is(err, domain.ErrConflict) ||
		!strings.Contains(err.Error(), "state that prevents a currency change") {
		t.Fatalf("SetAccountCurrency guarded = %v, want ErrConflict", err)
	}
}

func TestLocalNode_SetAccountCurrencyGuardsRetainedBalancePnlHalt(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	createCurrencyAssets(t, st, "EUR", "USD")

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: id, Currency: "EUR",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account:               id,
		Asset:                 "USD",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingCostBasis,
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	err := n.SetAccountCurrency(ctx, testKey(id), "USD", testCaller)
	var blocked domain.CurrencyChangeBlockedError
	if !errors.Is(err, domain.ErrConflict) || !errors.As(err, &blocked) ||
		blocked.Scope != domain.ScopeAccount || blocked.TargetID != id.String() {
		t.Fatalf("SetAccountCurrency = %#v, want typed account currency guard", err)
	}
	account, _, err := n.GetAccountState(ctx, testKey(id))
	if err != nil {
		t.Fatalf("GetAccountState: %v", err)
	}
	if account.Currency != "EUR" || account.EffectiveCurrency != "EUR" {
		t.Fatalf("account currency after guard = %+v, want EUR", account)
	}
}

// A P&L halt rejects before currency, runtime state or audit can mutate.
func TestLocalNode_SetAccountCurrencyBlocksAccountPnlHalt(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	createCurrencyAssets(t, st, "EUR", "USD")

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: id, Currency: "EUR",
		PnlHaltReason: domain.PnlHaltReasonMissingAccountCurrency,
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	beforeAudit, err := n.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("ListAudit(before): %v", err)
	}

	err = n.SetAccountCurrency(ctx, testKey(id), "USD", testCaller)
	var blocked domain.CurrencyChangeBlockedError
	if !errors.Is(err, domain.ErrConflict) || !errors.As(err, &blocked) ||
		blocked.Scope != domain.ScopeAccount || blocked.TargetID != id.String() {
		t.Fatalf("SetAccountCurrency = %#v, want typed account refusal", err)
	}
	account, ok, getErr := st.GetAccount(ctx, id)
	if getErr != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, getErr)
	}
	if account.Currency != "EUR" || account.EffectiveCurrency != "EUR" {
		t.Fatalf("account after refusal = %+v, want EUR unchanged", account)
	}
	if len(eng.accountPnlStateCalls) != 0 {
		t.Fatalf("halt refusal restated runtime P&L: %v", eng.accountPnlStateCalls)
	}
	afterAudit, auditErr := n.ListAudit(ctx, 100)
	if auditErr != nil {
		t.Fatalf("ListAudit(after): %v", auditErr)
	}
	if !reflect.DeepEqual(afterAudit, beforeAudit) {
		t.Fatalf("audit changed on refusal: before=%+v after=%+v", beforeAudit, afterAudit)
	}
}

func TestLocalNode_InheritedCurrencyChangesBlockHaltedPnl(t *testing.T) {
	t.Parallel()

	t.Run("group currency", func(t *testing.T) {
		t.Parallel()
		eng := newFakeEngine()
		n, st := newTestNode(t, eng)
		ctx := context.Background()
		createCurrencyAssets(t, st, "EUR", "GBP")
		if _, err := n.CreateGroup(ctx, domain.AccountGroup{
			Code: "desk", Currency: "EUR",
		}, testCaller); err != nil {
			t.Fatalf("CreateGroup: %v", err)
		}
		if _, err := n.CreateAccount(ctx, domain.Account{
			Code: "acc-1", GroupCode: "desk",
			PnlHaltReason: domain.PnlHaltReasonMissingFx,
		}, testCaller); err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}
		eng.accountPnlStateCalls = nil
		assertAuditUnchanged := assertNoNewAudit(t, n, ctx)
		err := n.SetGroupCurrency(ctx, "desk", "GBP", testCaller)
		var blocked domain.CurrencyChangeBlockedError
		if !errors.Is(err, domain.ErrConflict) || !errors.As(err, &blocked) ||
			blocked.Scope != domain.ScopeAccountGroup || blocked.TargetID != "desk" {
			t.Fatalf("SetGroupCurrency = %#v, want typed group refusal", err)
		}
		group, ok, getErr := st.GetGroup(ctx, "desk")
		if getErr != nil || !ok || group.Currency != "EUR" {
			t.Fatalf("group after refusal = %+v, ok=%v err=%v", group, ok, getErr)
		}
		if len(eng.accountPnlStateCalls) != 0 {
			t.Fatalf("group refusal restated runtime P&L: %v", eng.accountPnlStateCalls)
		}
		assertAuditUnchanged()
	})

	t.Run("default group currency", func(t *testing.T) {
		t.Parallel()
		eng := newFakeEngine()
		n, st := newTestNode(t, eng)
		ctx := context.Background()
		createCurrencyAssets(t, st, "USD")
		if _, err := n.CreateAccount(ctx, domain.Account{
			Code:          "acc-1",
			PnlHaltReason: domain.PnlHaltReasonMissingAccountCurrency,
		}, testCaller); err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}
		eng.accountPnlStateCalls = nil
		assertAuditUnchanged := assertNoNewAudit(t, n, ctx)
		err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller)
		var blocked domain.CurrencyChangeBlockedError
		if !errors.Is(err, domain.ErrConflict) || !errors.As(err, &blocked) ||
			blocked.Scope != domain.ScopeAccountGroup || blocked.TargetID != "-" {
			t.Fatalf("SetDefaultGroupCurrency = %#v, want typed default refusal", err)
		}
		if _, ok, getErr := st.GetGroup(ctx, ""); getErr != nil || ok {
			t.Fatalf("default group after refusal: ok=%v err=%v", ok, getErr)
		}
		if len(eng.accountPnlStateCalls) != 0 {
			t.Fatalf("default refusal restated runtime P&L: %v", eng.accountPnlStateCalls)
		}
		assertAuditUnchanged()
	})
}

func TestLocalNode_CurrencyChangeBlocksActiveOrder(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	createCurrencyAssets(t, st, "EUR", "USD")
	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: id, Currency: "EUR",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.CreateOrder(ctx, domain.Order{
		Account: id, Status: domain.OrderStatusCommitted,
	}); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	beforeAudit, err := n.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("ListAudit(before): %v", err)
	}

	err = n.SetAccountCurrency(ctx, testKey(id), "USD", testCaller)
	var blocked domain.CurrencyChangeBlockedError
	if !errors.Is(err, domain.ErrConflict) || !errors.As(err, &blocked) ||
		blocked.Scope != domain.ScopeAccount || blocked.TargetID != id.String() {
		t.Fatalf("SetAccountCurrency = %#v, want active-order refusal", err)
	}
	account, ok, getErr := st.GetAccount(ctx, id)
	if getErr != nil || !ok || account.Currency != "EUR" {
		t.Fatalf("account after refusal = %+v, ok=%v err=%v", account, ok, getErr)
	}
	afterAudit, auditErr := n.ListAudit(ctx, 100)
	if auditErr != nil {
		t.Fatalf("ListAudit(after): %v", auditErr)
	}
	if !reflect.DeepEqual(afterAudit, beforeAudit) {
		t.Fatalf("audit changed on refusal: before=%+v after=%+v", beforeAudit, afterAudit)
	}
}

func TestLocalNode_GroupCurrencyIgnoresOwnCurrencyActiveOrder(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	createCurrencyAssets(t, st, "EUR", "JPY", "USD")

	if _, err := n.CreateGroup(
		ctx, domain.AccountGroup{Code: "desk"}, testCaller,
	); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := n.SetGroupCurrency(ctx, "desk", "USD", testCaller); err != nil {
		t.Fatalf("SetGroupCurrency initial USD: %v", err)
	}
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "own-currency", GroupCode: "desk", Currency: "JPY",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount own-currency: %v", err)
	}
	if _, err := st.CreateOrder(ctx, domain.Order{
		Account: "own-currency", Status: domain.OrderStatusCommitted,
	}); err != nil {
		t.Fatalf("CreateOrder own-currency: %v", err)
	}

	if err := n.SetGroupCurrency(ctx, "desk", "EUR", testCaller); err != nil {
		t.Fatalf("SetGroupCurrency with own-currency active order: %v", err)
	}

	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "inheriting", GroupCode: "desk",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount inheriting: %v", err)
	}
	if _, err := st.CreateOrder(ctx, domain.Order{
		Account: "inheriting", Status: domain.OrderStatusCommitted,
	}); err != nil {
		t.Fatalf("CreateOrder inheriting: %v", err)
	}

	err := n.SetGroupCurrency(ctx, "desk", "USD", testCaller)
	var blocked domain.CurrencyChangeBlockedError
	if !errors.Is(err, domain.ErrConflict) || !errors.As(err, &blocked) ||
		blocked.Scope != domain.ScopeAccountGroup ||
		blocked.TargetID != "desk" ||
		!strings.Contains(err.Error(), "1 account(s)") {
		t.Fatalf("SetGroupCurrency = %#v, want inheriting active-order refusal", err)
	}
	group, ok, getErr := st.GetGroup(ctx, "desk")
	if getErr != nil || !ok || group.Currency != "EUR" {
		t.Fatalf("group after refusal = %+v, ok=%v err=%v", group, ok, getErr)
	}
}

func TestLocalNode_CurrencyChangeBlocksSpotFundsPnlBounds(t *testing.T) {
	t.Parallel()

	t.Run("account scope", func(t *testing.T) {
		t.Parallel()
		n, st := newTestNode(t, newFakeEngine())
		ctx := context.Background()
		createCurrencyAssets(t, st, "EUR", "USD")
		if _, err := n.CreateAccount(ctx, domain.Account{
			Code: "acc-1", Currency: "EUR",
		}, testCaller); err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}
		if err := st.PutSpotFundsPnlBoundsLimit(
			ctx,
			domain.LimitSpotFundsPnlBounds{
				Scope: domain.ScopeAccount, Account: "acc-1", LowerBound: "-10",
			},
		); err != nil {
			t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
		}
		assertAuditUnchanged := assertNoNewAudit(t, n, ctx)
		err := n.SetAccountCurrency(ctx, testKey("acc-1"), "USD", testCaller)
		var blocked domain.CurrencyChangeBlockedError
		if !errors.As(err, &blocked) || blocked.Scope != domain.ScopeAccount ||
			blocked.TargetID != "acc-1" {
			t.Fatalf("SetAccountCurrency = %#v, want account limit refusal", err)
		}
		account, ok, getErr := st.GetAccount(ctx, "acc-1")
		if getErr != nil || !ok || account.Currency != "EUR" {
			t.Fatalf("account after refusal = %+v, ok=%v err=%v", account, ok, getErr)
		}
		assertAuditUnchanged()
	})

	t.Run("group scope on group change", func(t *testing.T) {
		t.Parallel()
		n, st := newTestNode(t, newFakeEngine())
		ctx := context.Background()
		createCurrencyAssets(t, st, "EUR", "USD")
		if _, err := n.CreateGroup(ctx, domain.AccountGroup{
			Code: "desk", Currency: "EUR",
		}, testCaller); err != nil {
			t.Fatalf("CreateGroup: %v", err)
		}
		if _, err := n.CreateAccount(ctx, domain.Account{
			Code: "acc-1", GroupCode: "desk",
		}, testCaller); err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}
		if err := st.PutSpotFundsPnlBoundsLimit(
			ctx,
			domain.LimitSpotFundsPnlBounds{
				Scope: domain.ScopeAccountGroup, AccountGroup: "desk",
				UpperBound: "10",
			},
		); err != nil {
			t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
		}
		assertAuditUnchanged := assertNoNewAudit(t, n, ctx)
		err := n.SetGroupCurrency(ctx, "desk", "USD", testCaller)
		var blocked domain.CurrencyChangeBlockedError
		if !errors.As(err, &blocked) || blocked.Scope != domain.ScopeAccountGroup ||
			blocked.TargetID != "desk" {
			t.Fatalf("SetGroupCurrency = %#v, want group limit refusal", err)
		}
		group, ok, getErr := st.GetGroup(ctx, "desk")
		if getErr != nil || !ok || group.Currency != "EUR" {
			t.Fatalf("group after refusal = %+v, ok=%v err=%v", group, ok, getErr)
		}
		assertAuditUnchanged()
	})

	for _, tc := range []struct {
		name  string
		limit domain.LimitSpotFundsPnlBounds
	}{
		{
			name: "group scope on default change",
			limit: domain.LimitSpotFundsPnlBounds{
				Scope: domain.ScopeAccountGroup, AccountGroup: "desk",
				LowerBound: "-10",
			},
		},
		{
			name: "global scope on default change",
			limit: domain.LimitSpotFundsPnlBounds{
				Scope: domain.ScopeGlobal, UpperBound: "10",
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n, st := newTestNode(t, newFakeEngine())
			ctx := context.Background()
			createCurrencyAssets(t, st, "USD")
			if _, err := n.CreateGroup(ctx, domain.AccountGroup{
				Code: "desk",
			}, testCaller); err != nil {
				t.Fatalf("CreateGroup: %v", err)
			}
			if _, err := n.CreateAccount(ctx, domain.Account{
				Code: "acc-1", GroupCode: "desk",
			}, testCaller); err != nil {
				t.Fatalf("CreateAccount: %v", err)
			}
			if err := st.PutSpotFundsPnlBoundsLimit(ctx, tc.limit); err != nil {
				t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
			}
			assertAuditUnchanged := assertNoNewAudit(t, n, ctx)
			err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller)
			var blocked domain.CurrencyChangeBlockedError
			if !errors.As(err, &blocked) ||
				blocked.Scope != domain.ScopeAccountGroup ||
				blocked.TargetID != "-" {
				t.Fatalf("SetDefaultGroupCurrency = %#v, want default refusal", err)
			}
			if _, ok, getErr := st.GetGroup(ctx, ""); getErr != nil || ok {
				t.Fatalf("default group after refusal: ok=%v err=%v", ok, getErr)
			}
			assertAuditUnchanged()
		})
	}
}

func TestLocalNode_SetAccountCurrencyDoesNotRestateHaltWhenEffectiveUnchanged(
	t *testing.T,
) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	createCurrencyAssets(t, st, "EUR")
	if _, err := n.CreateGroup(ctx, domain.AccountGroup{
		Code: "desk", Currency: "EUR",
	}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: id, GroupCode: "desk", Currency: "EUR",
		PnlHaltReason: domain.PnlHaltReasonMissingFx,
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := n.SetAccountCurrency(ctx, testKey(id), "", testCaller); err != nil {
		t.Fatalf("SetAccountCurrency: %v", err)
	}
	if len(eng.accountPnlStateCalls) != 0 {
		t.Fatalf(
			"engine pnl state calls = %v, want none without effective change",
			eng.accountPnlStateCalls,
		)
	}
	account, ok, err := st.GetAccount(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Currency != "" || account.EffectiveCurrency != "EUR" {
		t.Fatalf("account currency = %+v, want inherited effective EUR", account)
	}
}

// A retained numeric value and its halt both belong to the current
// denomination, so the guard preserves them and rejects before mutation.
func TestLocalNode_SetAccountCurrencyBlocksStalePnlBehindHalt(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	createCurrencyAssets(t, st, "EUR", "USD")

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code:          id,
		Currency:      "EUR",
		Pnl:           "-50000",
		PnlHaltReason: domain.PnlHaltReasonMissingAccountCurrency,
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	err := n.SetAccountCurrency(ctx, testKey(id), "USD", testCaller)
	var blocked domain.CurrencyChangeBlockedError
	if !errors.As(err, &blocked) || blocked.Scope != domain.ScopeAccount ||
		blocked.TargetID != id.String() {
		t.Fatalf("SetAccountCurrency = %#v, want retained-P&L refusal", err)
	}
	if len(eng.accountPnlStateCalls) != 0 {
		t.Fatalf("refusal restated runtime P&L: %v", eng.accountPnlStateCalls)
	}
	account, ok, getErr := st.GetAccount(ctx, id)
	if getErr != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, getErr)
	}
	if account.Currency != "EUR" || account.Pnl != "-50000" ||
		account.PnlHaltReason != domain.PnlHaltReasonMissingAccountCurrency {
		t.Fatalf("account after refusal = %+v, want retained halted state", account)
	}
}

// A blocker lookup failure rejects before currency state can mutate.
func TestLocalNode_SetAccountCurrencyBlockerReadFailureLeavesStateUntouched(t *testing.T) {
	t.Parallel()
	readErr := errors.New("list currency blockers failed")
	st := newRealmWrapStore(newMemoryStore("currency.db"), func(r store.RealmStore) store.RealmStore {
		return &failCurrencyChangeBlockersRealm{RealmStore: r, err: readErr}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := newFakeEngine()
	n := newTestNodeWithStore(t, st, eng)
	createCurrencyAssets(t, n.realm, "EUR", "USD")
	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: id, Currency: "EUR",
		PnlHaltReason: domain.PnlHaltReasonMissingFx,
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	err := n.SetAccountCurrency(ctx, testKey(id), "USD", testCaller)
	if !errors.Is(err, readErr) {
		t.Fatalf("SetAccountCurrency error = %v, want open-balance read failure", err)
	}
	account, ok, getErr := n.realm.GetAccount(ctx, id)
	if getErr != nil || !ok || account.Currency != "EUR" || eng.accountCurrencies[id] != "EUR" {
		t.Fatalf("currency after preflight failure = store %+v engine %q ok=%v err=%v",
			account, eng.accountCurrencies[id], ok, getErr)
	}
}

func TestLocalNode_SetAccountCurrencyGuardsAverageEntryPriceOne(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	createCurrencyAssets(t, st, "EUR", "USD")
	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, domain.Account{Code: id, Currency: "EUR"}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: id, Asset: "USD", AverageEntryPrice: "1",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}
	if err := n.SetAccountCurrency(ctx, testKey(id), "USD", testCaller); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("SetAccountCurrency = %v, want average-entry-price guard", err)
	}
}

// A healthy P&L is a real number: setting the currency must not zero it, and no
// engine assignment is issued for it.
func TestLocalNode_SetAccountCurrencyKeepsHealthyPnl(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	createCurrencyAssets(t, st, "USD")

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, domain.Account{Code: id}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	// A healthy non-zero P&L would be guarded, so the account carries none: the
	// assertion is that a non-halted account is never reset.
	if err := n.SetAccountCurrency(ctx, testKey(id), "USD", testCaller); err != nil {
		t.Fatalf("SetAccountCurrency: %v", err)
	}
	if len(eng.accountPnlStateCalls) != 0 {
		t.Fatalf(
			"engine pnl calls = %v, want none for a healthy account",
			eng.accountPnlStateCalls,
		)
	}
	account, _, err := n.GetAccountState(ctx, testKey(id))
	if err != nil {
		t.Fatalf("GetAccountState: %v", err)
	}
	if account.PnlHaltReason != "" {
		t.Fatalf("account halt = %q, want none", account.PnlHaltReason)
	}
}

// A halted account still holding a number is guarded: the number is denominated
// in the old currency and would need recomputing.
func TestLocalNode_SetAccountCurrencyGuardsHaltedAccountHoldingPositions(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	createCurrencyAssets(t, st, "USD", "EUR")

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code:          id,
		Currency:      "USD",
		PnlHaltReason: domain.PnlHaltReasonMissingFx,
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account:               id,
		Asset:                 "USD",
		Available:             "4",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	err := n.SetAccountCurrency(ctx, testKey(id), "EUR", testCaller)
	if !errors.Is(err, domain.ErrConflict) ||
		!strings.Contains(err.Error(), "state that prevents a currency change") {
		t.Fatalf("SetAccountCurrency guarded = %v, want ErrConflict", err)
	}
}

func TestLocalNode_CurrencyGuardsAccountPnlWithoutBalanceRows(t *testing.T) {
	t.Parallel()

	t.Run("direct account set and clear", func(t *testing.T) {
		t.Parallel()
		n, st := newTestNode(t, newFakeEngine())
		ctx := context.Background()
		createCurrencyAssets(t, st, "USD", "EUR")
		if _, err := n.CreateAccount(ctx, domain.Account{
			Code: "account", Currency: "USD", Pnl: "1",
		}, testCaller); err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}

		for _, currency := range []string{"EUR", ""} {
			assertAccountPnlCurrencyGuard(
				t,
				n.SetAccountCurrency(ctx, testKey("account"), currency, testCaller),
				"account",
			)
		}
	})

	t.Run("group assignment", func(t *testing.T) {
		t.Parallel()
		n, st := newTestNode(t, newFakeEngine())
		ctx := context.Background()
		createCurrencyAssets(t, st, "USD", "EUR")
		if err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller); err != nil {
			t.Fatalf("SetDefaultGroupCurrency: %v", err)
		}
		if _, err := n.CreateGroup(ctx, domain.AccountGroup{
			Code: "desk-eur", Currency: "EUR",
		}, testCaller); err != nil {
			t.Fatalf("CreateGroup: %v", err)
		}
		if _, err := n.CreateAccount(ctx, domain.Account{Code: "account", Pnl: "1"}, testCaller); err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}

		err := n.SetAccountGroup(
			ctx,
			testKey("account"),
			"desk-eur",
			domain.MissingAccountCreate,
			testCaller,
		)
		assertAccountPnlCurrencyGuard(t, err, "account")
		var blocked domain.CurrencyChangeBlockedError
		if !errors.As(err, &blocked) || blocked.Scope != domain.ScopeAccount ||
			blocked.TargetID != "account" {
			t.Fatalf("SetAccountGroup error = %#v, want typed account currency guard", err)
		}
	})

	t.Run("group currency mutation", func(t *testing.T) {
		t.Parallel()
		n, st := newTestNode(t, newFakeEngine())
		ctx := context.Background()
		createCurrencyAssets(t, st, "USD", "EUR")
		if err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller); err != nil {
			t.Fatalf("SetDefaultGroupCurrency: %v", err)
		}
		if _, err := n.CreateGroup(ctx, domain.AccountGroup{
			Code: "desk", Currency: "USD",
		}, testCaller); err != nil {
			t.Fatalf("CreateGroup: %v", err)
		}
		if _, err := n.CreateAccount(ctx, domain.Account{
			Code: "account", GroupCode: "desk", Pnl: "1",
		}, testCaller); err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}

		assertAccountPnlCurrencyGuard(
			t,
			n.SetGroupCurrency(ctx, "desk", "EUR", testCaller),
			"account",
		)
	})

	t.Run("default currency mutation and clear", func(t *testing.T) {
		t.Parallel()
		n, st := newTestNode(t, newFakeEngine())
		ctx := context.Background()
		createCurrencyAssets(t, st, "USD", "EUR")
		if err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller); err != nil {
			t.Fatalf("SetDefaultGroupCurrency: %v", err)
		}
		if _, err := n.CreateAccount(ctx, domain.Account{Code: "account", Pnl: "1"}, testCaller); err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}

		for _, currency := range []string{"EUR", ""} {
			assertAccountPnlCurrencyGuard(
				t,
				n.SetDefaultGroupCurrency(ctx, currency, testCaller),
				"account",
			)
		}
	})

	t.Run("group delete", func(t *testing.T) {
		t.Parallel()
		eng := newFakeEngine()
		n, probe := newRebuildProbeNode(t, eng)
		st := n.realm
		ctx := context.Background()
		createCurrencyAssets(t, st, "USD", "EUR")
		if err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller); err != nil {
			t.Fatalf("SetDefaultGroupCurrency: %v", err)
		}
		if _, err := n.CreateGroup(ctx, domain.AccountGroup{
			Code: "desk-eur", Currency: "EUR",
		}, testCaller); err != nil {
			t.Fatalf("CreateGroup: %v", err)
		}
		if _, err := n.CreateAccount(ctx, domain.Account{
			Code: "account", GroupCode: "desk-eur", Pnl: "1",
		}, testCaller); err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}

		next := newFakeEngine()
		prepareGroupDeleteRebuild(n, probe, next)
		assertAccountPnlCurrencyGuard(
			t, n.DeleteGroup(ctx, "desk-eur", false, testCaller), "account",
		)
		account, ok, err := st.GetAccount(ctx, "account")
		if err != nil || !ok || account.GroupCode != "desk-eur" {
			t.Fatalf("member after refused group deletion: %+v ok=%v err=%v", account, ok, err)
		}
	})
}

func assertNoNewAudit(
	t *testing.T, n *localNode, ctx context.Context,
) func() {
	t.Helper()
	before, err := n.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("ListAudit(before): %v", err)
	}
	return func() {
		t.Helper()
		after, err := n.ListAudit(ctx, 100)
		if err != nil {
			t.Fatalf("ListAudit(after): %v", err)
		}
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("audit changed on refusal: before=%+v after=%+v", before, after)
		}
	}
}

func assertAccountPnlCurrencyGuard(t *testing.T, err error, _ string) {
	t.Helper()
	if !errors.Is(err, domain.ErrConflict) ||
		!strings.Contains(err.Error(), "state that prevents a currency change") {
		t.Fatalf("currency change error = %v, want ErrConflict", err)
	}
}

func TestLocalNode_SetAccountCurrencyGuardDoesNotAutoCreateAsset(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	createCurrencyAssets(t, st, "USD")

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetAccountCurrency(ctx, testKey(id), "USD", testCaller); err != nil {
		t.Fatalf("SetAccountCurrency: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account:   id,
		Asset:     "USD",
		Available: "1",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	err := n.SetAccountCurrency(ctx, testKey(id), "CHF", testCaller)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("SetAccountCurrency guarded = %v, want ErrConflict", err)
	}
	if _, ok, err := st.GetAsset(ctx, "CHF"); err != nil || ok {
		t.Fatalf("GetAsset(CHF) = ok %v err %v, want absent", ok, err)
	}
}

func TestLocalNode_SetAccountCurrencyPreservesEngineInvalid(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.accountCurrencyErr = fmt.Errorf("engine invalid currency: %w", domain.ErrInvalid)
	n, st := newTestNode(t, eng)
	ctx := context.Background()
	createCurrencyAssets(t, st, "USD")

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	err := n.SetAccountCurrency(ctx, testKey(id), "USD", testCaller)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SetAccountCurrency = %v, want ErrInvalid", err)
	}
}

func TestLocalNode_SetAccountCurrencyRevertFailureFatals(t *testing.T) {
	t.Parallel()
	applyErr := errors.New("engine apply account currency failed")
	revertErr := errors.New("engine revert account currency failed")
	eng := &failSetAccountCurrencyEngine{
		fakeEngine: newFakeEngine(),
		failSetCalls: map[int]error{
			2: applyErr,
			3: revertErr,
		},
	}
	st := newMemoryStore("node.db")
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var fatalErr error
	nRaw, _, err := NewLocalNode(
		ctx,
		st,
		fakeBuildWithSetAccountCurrencyFailures(eng),
		WithFatalShutdownHook(func(err error) { fatalErr = err }),
	)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	n := nRaw.(*localNode)
	seedTestPrincipal(t, n.realm)
	createCurrencyAssets(t, n.realm, "EUR", "USD")

	const id domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, testAccount(id), testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := n.SetAccountCurrency(ctx, testKey(id), "EUR", testCaller); err != nil {
		t.Fatalf("SetAccountCurrency initial: %v", err)
	}

	err = n.SetAccountCurrency(ctx, testKey(id), "USD", testCaller)
	if !errors.Is(err, revertErr) {
		t.Fatalf("SetAccountCurrency error = %v, want revert failure", err)
	}
	if errors.Is(err, applyErr) {
		t.Fatalf("SetAccountCurrency error = %v, want fatal revert failure", err)
	}
	if fatalErr == nil {
		t.Fatal("fatal hook did not fire on account currency revert failure")
	}
	msg := fatalErr.Error()
	if !strings.Contains(msg, `operation="revert account currency"`) ||
		!strings.Contains(msg, "account=acc-1") ||
		!strings.Contains(msg, "post-engine persistence failure") ||
		!strings.Contains(msg, "engine revert account currency failed") {
		t.Fatalf("fatal error = %q, want operation, account code, and cause", msg)
	}
	if strings.Contains(msg, "account_id=") {
		t.Fatalf("fatal error = %q, must not leak the engine surrogate", msg)
	}
	if eng.setCalls != 3 {
		t.Fatalf("SetAccountCurrency calls = %d, want apply and revert attempts", eng.setCalls)
	}
}

func TestLocalNode_GroupCurrencyGuardsOnlyEffectiveCurrencyChanges(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	createCurrencyAssets(t, st, "USD", "EUR", "JPY")

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}, testCaller); err != nil {
		t.Fatalf("CreateGroup desk-a: %v", err)
	}
	if err := n.SetGroupCurrency(ctx, "desk-a", "USD", testCaller); err != nil {
		t.Fatalf("SetGroupCurrency initial USD: %v", err)
	}
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code:      "inheriting",
		GroupCode: "desk-a",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount inheriting: %v", err)
	}
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code:      "own-currency",
		GroupCode: "desk-a",
		Currency:  "JPY",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount own-currency: %v", err)
	}
	upsertNonzero := func(account domain.AccountID) {
		t.Helper()
		if err := st.UpsertBalance(ctx, domain.Balance{
			Account:   account,
			Asset:     "USD",
			Available: "2",
		}); err != nil {
			t.Fatalf("UpsertBalance %s: %v", account, err)
		}
	}
	upsertNonzero("inheriting")
	upsertNonzero("own-currency")

	if err := n.SetGroupCurrency(ctx, "desk-a", "USD", testCaller); err != nil {
		t.Fatalf("SetGroupCurrency same USD with nonempty members: %v", err)
	}

	if err := st.DeleteBalance(ctx, "own-currency", "USD"); err != nil {
		t.Fatalf("DeleteBalance own-currency: %v", err)
	}
	err := n.SetGroupCurrency(ctx, "desk-a", "EUR", testCaller)
	if !errors.Is(err, domain.ErrConflict) ||
		!strings.Contains(err.Error(), "1 account(s)") {
		t.Fatalf("SetGroupCurrency with inheriting member = %v, want blocker", err)
	}

	upsertNonzero("own-currency")
	if err := st.DeleteBalance(ctx, "inheriting", "USD"); err != nil {
		t.Fatalf("DeleteBalance inheriting: %v", err)
	}
	if err := n.SetGroupCurrency(ctx, "desk-a", "EUR", testCaller); err != nil {
		t.Fatalf("SetGroupCurrency with own-currency member: %v", err)
	}

	if err := st.DeleteBalance(ctx, "own-currency", "USD"); err != nil {
		t.Fatalf("DeleteBalance own-currency: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: "inheriting", Asset: "USD", RealizedPnl: "0",
	}); err != nil {
		t.Fatalf("UpsertBalance zero: %v", err)
	}
	if err := n.SetGroupCurrency(ctx, "desk-a", "EUR", testCaller); err != nil {
		t.Fatalf("SetGroupCurrency with zero-only row: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: "inheriting", Asset: "USD",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
	}); err != nil {
		t.Fatalf("UpsertBalance halt: %v", err)
	}
	err = n.SetGroupCurrency(ctx, "desk-a", "JPY", testCaller)
	var blocked domain.CurrencyChangeBlockedError
	if !errors.Is(err, domain.ErrConflict) || !errors.As(err, &blocked) ||
		blocked.Scope != domain.ScopeAccountGroup || blocked.TargetID != "desk-a" {
		t.Fatalf("SetGroupCurrency = %#v, want typed group currency guard", err)
	}
	if err := st.DeleteBalance(ctx, "inheriting", "USD"); err != nil {
		t.Fatalf("DeleteBalance halt: %v", err)
	}
	if err := n.SetGroupCurrency(ctx, "desk-a", "JPY", testCaller); err != nil {
		t.Fatalf("SetGroupCurrency after deleting halt row: %v", err)
	}
}

func TestLocalNode_GroupCurrencyClearAllowsUnchangedEffectiveCurrency(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	createCurrencyAssets(t, st, "USD")

	if err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller); err != nil {
		t.Fatalf("SetDefaultGroupCurrency: %v", err)
	}
	if _, err := n.CreateGroup(ctx, domain.AccountGroup{
		Code: "desk", Currency: "USD",
	}, testCaller); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	const accountID domain.AccountID = "acc-1"
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: accountID, GroupCode: "desk",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := st.UpsertBalance(ctx, domain.Balance{
		Account: accountID, Asset: "USD", Available: "1",
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	if err := n.SetGroupCurrency(ctx, "desk", "", testCaller); err != nil {
		t.Fatalf("clear group currency with unchanged effective currency: %v", err)
	}
	group, ok, err := st.GetGroup(ctx, "desk")
	if err != nil || !ok || group.Currency != "" {
		t.Fatalf("group after clear = %+v, ok=%v err=%v", group, ok, err)
	}
	account, ok, err := st.GetAccount(ctx, accountID)
	if err != nil || !ok || account.EffectiveCurrency != "USD" ||
		account.CurrencyOrigin != domain.CurrencyOriginDefault {
		t.Fatalf("account after clear = %+v, ok=%v err=%v, want effective USD/default", account, ok, err)
	}
}

func TestLocalNode_DeleteGroupDetachesMembersWithCurrencyGuard(t *testing.T) {
	t.Parallel()

	t.Run("with open balances", func(t *testing.T) {
		t.Parallel()
		eng := newFakeEngine()
		n, probe := newRebuildProbeNode(t, eng)
		st := n.realm
		ctx := context.Background()
		createCurrencyAssets(t, st, "USD", "EUR")

		if err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller); err != nil {
			t.Fatalf("SetDefaultGroupCurrency: %v", err)
		}
		if _, err := n.CreateGroup(ctx, domain.AccountGroup{
			Code:     "desk-a",
			Currency: "EUR",
		}, testCaller); err != nil {
			t.Fatalf("CreateGroup desk-a: %v", err)
		}
		if _, err := n.CreateAccount(ctx, domain.Account{
			Code:      "blocked",
			GroupCode: "desk-a",
		}, testCaller); err != nil {
			t.Fatalf("CreateAccount blocked: %v", err)
		}
		if err := st.UpsertBalance(ctx, domain.Balance{
			Account:   "blocked",
			Asset:     "USD",
			Available: "1",
		}); err != nil {
			t.Fatalf("UpsertBalance blocked: %v", err)
		}

		next := newFakeEngine()
		prepareGroupDeleteRebuild(n, probe, next)
		if err := n.DeleteGroup(ctx, "desk-a", false, testCaller); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("DeleteGroup with open balance = %v, want ErrConflict", err)
		}
		if _, ok, err := st.GetGroup(ctx, "desk-a"); err != nil || !ok {
			t.Fatalf("group after refused delete: ok=%v err=%v, want present", ok, err)
		}
		if account, ok, err := st.GetAccount(ctx, "blocked"); err != nil || !ok || account.GroupCode != "desk-a" {
			t.Fatalf("member after refused delete: %+v ok=%v err=%v", account, ok, err)
		}
	})

	t.Run("allowed without open balances", func(t *testing.T) {
		t.Parallel()
		eng := newFakeEngine()
		n, probe := newRebuildProbeNode(t, eng)
		st := n.realm
		ctx := context.Background()
		createCurrencyAssets(t, st, "USD", "EUR")

		if err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller); err != nil {
			t.Fatalf("SetDefaultGroupCurrency: %v", err)
		}
		if _, err := n.CreateGroup(ctx, domain.AccountGroup{
			Code:     "desk-a",
			Currency: "EUR",
		}, testCaller); err != nil {
			t.Fatalf("CreateGroup desk-a: %v", err)
		}
		if _, err := n.CreateAccount(ctx, domain.Account{
			Code:      "clear",
			GroupCode: "desk-a",
		}, testCaller); err != nil {
			t.Fatalf("CreateAccount clear: %v", err)
		}
		if _, ok := eng.accountCurrencies["clear"]; ok {
			t.Fatal("inherited group currency was materialized as an account override")
		}
		if got := eng.effectiveAccountCurrency("clear"); got != "EUR" {
			t.Fatalf("live effective currency before delete = %q, want EUR", got)
		}

		next := newFakeEngine()
		prepareGroupDeleteRebuild(n, probe, next)
		if err := n.DeleteGroup(ctx, "desk-a", false, testCaller); err != nil {
			t.Fatalf("DeleteGroup without balances: %v", err)
		}
		account, ok, err := st.GetAccount(ctx, "clear")
		if err != nil || !ok || account.GroupCode != "" || account.EffectiveCurrency != "USD" {
			t.Fatalf("member after detach: %+v ok=%v err=%v", account, ok, err)
		}
	})

	t.Run("allowed with own account currency", func(t *testing.T) {
		t.Parallel()
		eng := newFakeEngine()
		n, probe := newRebuildProbeNode(t, eng)
		st := n.realm
		ctx := context.Background()
		createCurrencyAssets(t, st, "USD", "EUR", "JPY")

		if err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller); err != nil {
			t.Fatalf("SetDefaultGroupCurrency: %v", err)
		}
		if _, err := n.CreateGroup(ctx, domain.AccountGroup{
			Code:     "desk-a",
			Currency: "EUR",
		}, testCaller); err != nil {
			t.Fatalf("CreateGroup desk-a: %v", err)
		}
		if _, err := n.CreateAccount(ctx, domain.Account{
			Code:      "own-currency",
			GroupCode: "desk-a",
			Currency:  "JPY",
		}, testCaller); err != nil {
			t.Fatalf("CreateAccount own-currency: %v", err)
		}
		if err := st.UpsertBalance(ctx, domain.Balance{
			Account:   "own-currency",
			Asset:     "USD",
			Available: "1",
		}); err != nil {
			t.Fatalf("UpsertBalance own-currency: %v", err)
		}

		next := newFakeEngine()
		prepareGroupDeleteRebuild(n, probe, next)
		if err := n.DeleteGroup(ctx, "desk-a", false, testCaller); err != nil {
			t.Fatalf("DeleteGroup with own currency: %v", err)
		}
		account, ok, err := st.GetAccount(ctx, "own-currency")
		if err != nil || !ok || account.GroupCode != "" || account.EffectiveCurrency != "JPY" {
			t.Fatalf("member after detach: %+v ok=%v err=%v", account, ok, err)
		}
	})
}

func TestLocalNode_DefaultGroupCurrencyGuardsOnlyDefaultResolvedAccounts(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()
	createCurrencyAssets(t, st, "USD", "EUR", "JPY", "GBP")

	if err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller); err != nil {
		t.Fatalf("SetDefaultGroupCurrency initial: %v", err)
	}
	if _, err := n.CreateGroup(ctx, domain.AccountGroup{
		Code:     "desk-a",
		Currency: "JPY",
	}, testCaller); err != nil {
		t.Fatalf("CreateGroup desk-a: %v", err)
	}
	for _, account := range []domain.Account{
		{Code: "through-default"},
		{Code: "own-currency", Currency: "GBP"},
		{Code: "group-currency", GroupCode: "desk-a"},
	} {
		if _, err := n.CreateAccount(ctx, account, testCaller); err != nil {
			t.Fatalf("CreateAccount %s: %v", account.Code, err)
		}
		if err := st.UpsertBalance(ctx, domain.Balance{
			Account:   account.Code,
			Asset:     "USD",
			Available: "1",
		}); err != nil {
			t.Fatalf("UpsertBalance %s: %v", account.Code, err)
		}
	}

	err := n.SetDefaultGroupCurrency(ctx, "EUR", testCaller)
	if !errors.Is(err, domain.ErrConflict) ||
		!strings.Contains(err.Error(), "1 account(s)") {
		t.Fatalf("SetDefaultGroupCurrency guarded = %v, want only through-default", err)
	}
	if err := st.DeleteBalance(ctx, "through-default", "USD"); err != nil {
		t.Fatalf("DeleteBalance through-default: %v", err)
	}
	if err := n.SetDefaultGroupCurrency(ctx, "EUR", testCaller); err != nil {
		t.Fatalf("SetDefaultGroupCurrency with non-default blockers only: %v", err)
	}

	if err := st.UpsertBalance(ctx, domain.Balance{
		Account:   "through-default",
		Asset:     "USD",
		Available: "1",
	}); err != nil {
		t.Fatalf("UpsertBalance through-default second: %v", err)
	}
	err = n.SetDefaultGroupCurrency(ctx, "", testCaller)
	if !errors.Is(err, domain.ErrConflict) ||
		!strings.Contains(err.Error(), "1 account(s)") {
		t.Fatalf("Clear default group currency guarded = %v, want through-default", err)
	}
}

func TestLocalNode_CurrencyAutoCreatesAssets(t *testing.T) {
	t.Parallel()
	n, st := newTestNode(t, newFakeEngine())
	ctx := context.Background()

	if _, err := n.CreateGroup(ctx, domain.AccountGroup{
		Code:     "desk-a",
		Currency: "CHF",
	}, testCaller); err != nil {
		t.Fatalf("CreateGroup with currency: %v", err)
	}
	if _, ok, err := st.GetAsset(ctx, "CHF"); err != nil || !ok {
		t.Fatalf("GetAsset(CHF) = ok %v err %v, want auto-created", ok, err)
	}

	if _, err := n.CreateAccount(ctx, domain.Account{
		Code:     "acc-1",
		Currency: "CAD",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount with currency: %v", err)
	}
	if _, ok, err := st.GetAsset(ctx, "CAD"); err != nil || !ok {
		t.Fatalf("GetAsset(CAD) = ok %v err %v, want auto-created", ok, err)
	}

	if err := n.SetAccountCurrency(ctx, testKey("acc-1"), "SEK", testCaller); err != nil {
		t.Fatalf("SetAccountCurrency auto-create: %v", err)
	}
	if _, ok, err := st.GetAsset(ctx, "SEK"); err != nil || !ok {
		t.Fatalf("GetAsset(SEK) = ok %v err %v, want auto-created", ok, err)
	}

	if err := n.SetGroupCurrency(ctx, "desk-a", "NOK", testCaller); err != nil {
		t.Fatalf("SetGroupCurrency auto-create: %v", err)
	}
	if _, ok, err := st.GetAsset(ctx, "NOK"); err != nil || !ok {
		t.Fatalf("GetAsset(NOK) = ok %v err %v, want auto-created", ok, err)
	}

	if err := n.SetDefaultGroupCurrency(ctx, "DKK", testCaller); err != nil {
		t.Fatalf("SetDefaultGroupCurrency auto-create: %v", err)
	}
	if _, ok, err := st.GetAsset(ctx, "DKK"); err != nil || !ok {
		t.Fatalf("GetAsset(DKK) = ok %v err %v, want auto-created", ok, err)
	}
}

func createCurrencyAssets(t *testing.T, st interface {
	CreateAsset(context.Context, domain.Asset) error
}, codes ...string) {
	t.Helper()
	for _, code := range codes {
		if err := st.CreateAsset(context.Background(), domain.Asset{Code: code}); err != nil &&
			!errors.Is(err, domain.ErrAlreadyExists) {
			t.Fatalf("CreateAsset(%s): %v", code, err)
		}
	}
}

type failSetAccountCurrencyEngine struct {
	*fakeEngine
	failSetCalls map[int]error
	setCalls     int
}

type failCurrencyChangeBlockersRealm struct {
	store.RealmStore
	err error
}

func (r *failCurrencyChangeBlockersRealm) ListAccountsBlockingCurrencyChange(
	context.Context, []domain.AccountID,
) ([]domain.AccountID, error) {
	return nil, r.err
}

func (e *failSetAccountCurrencyEngine) RunAccountSynchronized(
	_ context.Context, account domain.AccountID, fn func(engine.AccountLane) error,
) error {
	base := e.fakeEngine
	if err := base.checkKnownAccount(account); err != nil {
		return err
	}
	lane := base.accountLane(account)
	lane.Lock()
	defer lane.Unlock()
	base.stateMu.Lock()
	base.accountSyncCalls = append(base.accountSyncCalls, account)
	base.inAccountSync++
	base.laneDepth++
	base.stateMu.Unlock()
	defer func() {
		base.stateMu.Lock()
		base.inAccountSync--
		base.laneDepth--
		base.stateMu.Unlock()
	}()
	return fn(e)
}

func (e *failSetAccountCurrencyEngine) SetAccountCurrency(
	ctx context.Context, id domain.AccountID, currency string,
) error {
	e.setCalls++
	if err := e.failSetCalls[e.setCalls]; err != nil {
		return err
	}
	return e.fakeEngine.SetAccountCurrency(ctx, id, currency)
}

func fakeBuildWithSetAccountCurrencyFailures(
	eng *failSetAccountCurrencyEngine,
) engine.BuildFunc {
	return func(snap engine.Snapshot) (engine.Engine, error) {
		base := eng.fakeEngine
		base.knownAccounts = map[domain.AccountID]struct{}{}
		for _, account := range snap.Accounts {
			base.knownAccounts[account.Code] = struct{}{}
		}
		base.knownGroups = map[string]struct{}{}
		base.groupCurrencies = map[string]string{}
		for _, group := range snap.Groups {
			base.knownGroups[group.Code] = struct{}{}
			if group.Currency != "" {
				base.groupCurrencies[group.Code] = group.Currency
			}
		}
		base.accountCurrencies = map[domain.AccountID]string{}
		base.accountGroups = map[domain.AccountID]string{}
		for _, account := range snap.Accounts {
			if account.Currency != "" {
				base.accountCurrencies[account.Code] = account.Currency
			}
			base.accountGroups[account.Code] = account.GroupCode
		}
		return eng, nil
	}
}
