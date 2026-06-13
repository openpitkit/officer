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

// These tests exercise the OpenPit binding and so require the native runtime
// dylib at run time (set OPENPIT_RUNTIME_LIBRARY_PATH or build the workspace
// dylib first, as documented for the Go bindings). They build one real engine
// and reconfigure it in place; none of them rebuild the engine.

package engine

import (
	"context"
	"errors"
	"testing"
	"unicode"
	"unicode/utf8"

	"go.openpit.dev/openpit/param"

	"go.openpit.dev/officer/internal/domain"
)

func rateLimit(scope, account, asset, maxOrders, window string) domain.Limit {
	return domain.Limit{
		Target: domain.LimitTarget{
			Tenant:  domain.DefaultTenant,
			Policy:  domain.PolicyRateLimit,
			Scope:   scope,
			Account: domain.AccountID(account),
			Asset:   asset,
		},
		Values: []domain.LimitValue{
			{Kind: domain.KindMaxOrders, Value: maxOrders},
			{Kind: domain.KindWindow, Value: window},
		},
	}
}

func TestRateLimitReady_AllAxes(t *testing.T) {
	t.Parallel()
	limits := []domain.Limit{
		rateLimit(domain.ScopeBroker, "", "", "1000", "1m"),
		rateLimit(domain.ScopeAsset, "", "USD", "500", "1m"),
		rateLimit(domain.ScopeAccount, "acc-1", "", "200", "1m"),
		rateLimit(domain.ScopeAccountAsset, "acc-1", "USD", "100", "1m"),
	}
	ready, err := rateLimitReady(limits)
	if err != nil {
		t.Fatalf("rateLimitReady: %v", err)
	}
	if ready == nil {
		t.Fatalf("nil ready builder")
	}
}

func TestRateLimitFromValues_RequiresBoth(t *testing.T) {
	t.Parallel()
	_, err := rateLimitFromValues([]domain.LimitValue{
		{Kind: domain.KindMaxOrders, Value: "100"},
	})
	if err == nil {
		t.Fatalf("want error when window missing")
	}
}

func TestOrderSizeFromValues_ParsesBoth(t *testing.T) {
	t.Parallel()
	limit, err := orderSizeFromValues([]domain.LimitValue{
		{Kind: domain.KindMaxQuantity, Value: "10"},
		{Kind: domain.KindMaxNotional, Value: "1000"},
	})
	if err != nil {
		t.Fatalf("orderSizeFromValues: %v", err)
	}
	want, err := param.NewQuantityFromString("10")
	if err != nil {
		t.Fatalf("quantity: %v", err)
	}
	if !limit.MaxQuantity.Equal(want) {
		t.Fatalf("max_quantity mismatch")
	}
}

func TestPnlBoundsFromValues_Optionals(t *testing.T) {
	t.Parallel()
	lower, upper, err := pnlBoundsFromValues([]domain.LimitValue{
		{Kind: domain.KindLowerBound, Value: "-100"},
	})
	if err != nil {
		t.Fatalf("pnlBoundsFromValues: %v", err)
	}
	if _, ok := lower.Get(); !ok {
		t.Fatalf("want lower bound present")
	}
	if _, ok := upper.Get(); ok {
		t.Fatalf("want upper bound absent")
	}
}

// TestRateLimitAxes_AllAxes checks the rate-limit Configure axes are built with
// always-non-nil slices and the broker barrier set from the broker-scoped
// limit.
func TestRateLimitAxes_AllAxes(t *testing.T) {
	t.Parallel()
	limits := []domain.Limit{
		rateLimit(domain.ScopeBroker, "", "", "1000", "1m"),
		rateLimit(domain.ScopeAsset, "", "USD", "500", "1m"),
		rateLimit(domain.ScopeAccount, "acc-1", "", "200", "1m"),
		rateLimit(domain.ScopeAccountAsset, "acc-1", "USD", "100", "1m"),
	}
	broker, assets, accounts, accountAssets, err := rateLimitAxes(limits)
	if err != nil {
		t.Fatalf("rateLimitAxes: %v", err)
	}
	if broker == nil {
		t.Fatalf("want broker barrier")
	}
	if assets == nil || accounts == nil || accountAssets == nil {
		t.Fatalf("axes must be non-nil so Configure touches each axis")
	}
	if len(assets) != 1 || len(accounts) != 1 || len(accountAssets) != 1 {
		t.Fatalf("axis counts wrong: %d %d %d",
			len(assets), len(accounts), len(accountAssets))
	}
}

// TestOrderSizeAxes_EmptyAxesNonNil checks empty axes are empty non-nil slices
// so a Configure call clears them rather than leaving them unchanged.
func TestOrderSizeAxes_EmptyAxesNonNil(t *testing.T) {
	t.Parallel()
	broker, assets, accountAssets, err := orderSizeAxes(
		[]domain.Limit{orderSize(domain.ScopeBroker, "", "", "10", "")})
	if err != nil {
		t.Fatalf("orderSizeAxes: %v", err)
	}
	if broker == nil {
		t.Fatalf("want broker barrier")
	}
	if assets == nil || accountAssets == nil {
		t.Fatalf("empty axes must be non-nil slices")
	}
	if len(assets) != 0 || len(accountAssets) != 0 {
		t.Fatalf("want empty asset/account-asset axes")
	}
}

// TestPnlBoundsAxes_NonNil checks both P&L axes are non-nil slices.
func TestPnlBoundsAxes_NonNil(t *testing.T) {
	t.Parallel()
	brokers, accounts, err := pnlBoundsAxes(
		[]domain.Limit{pnlBounds(domain.ScopeAsset, "", "USD", "-100", "100")})
	if err != nil {
		t.Fatalf("pnlBoundsAxes: %v", err)
	}
	if brokers == nil || accounts == nil {
		t.Fatalf("axes must be non-nil")
	}
	if len(brokers) != 1 || len(accounts) != 0 {
		t.Fatalf("axis counts wrong: %d %d", len(brokers), len(accounts))
	}
}

func TestBuildEngine_RegistersRiskPolicies(t *testing.T) {
	t.Parallel()
	byPolicy := map[string][]domain.Limit{
		domain.PolicyRateLimit:      {rateLimit(domain.ScopeBroker, "", "", "100", "1s")},
		domain.PolicyOrderSizeLimit: {orderSize(domain.ScopeBroker, "", "", "10", "")},
		domain.PolicyPnlBoundsKillSwitch: {
			pnlBounds(domain.ScopeAsset, "", "USD", "-100", "100"),
		},
	}
	eng, service, registered, err := buildEngine(byPolicy)
	if err != nil {
		t.Fatalf("buildEngine: %v", err)
	}
	defer service.Close()
	defer eng.Stop()

	for _, name := range []string{
		nameRateLimit, nameOrderSizeLimit, namePnlBoundsKillSwitch,
	} {
		if _, ok := registered[name]; !ok {
			t.Fatalf("policy %q not registered", name)
		}
	}
}

// TestBuildOpenPitEngine_SeedsFromSnapshot builds the one engine from a seeded
// snapshot (a blocked account plus a rate-limit barrier) and retunes the
// rate-limit policy in place. It needs the native dylib at run time.
func TestBuildOpenPitEngine_SeedsFromSnapshot(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Accounts: []domain.Account{
			{Tenant: domain.DefaultTenant, ID: "acc-1", Blocked: true, BlockReason: "risk"},
		},
		Limits: []domain.Limit{rateLimit(domain.ScopeBroker, "", "", "100", "1s")},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()
	ctx := context.Background()

	// Retune the rate-limit policy (broker barrier kept) on the live handle: the
	// axes are replaced wholesale, no rebuild.
	newLimits := []domain.Limit{rateLimit(domain.ScopeBroker, "", "", "5", "1s")}
	if err := eng.ConfigurePolicy(ctx, domain.PolicyRateLimit, newLimits); err != nil {
		t.Fatalf("ConfigurePolicy retune: %v", err)
	}

	if err := eng.UnblockAccount(ctx, "acc-1"); err != nil {
		t.Fatalf("UnblockAccount: %v", err)
	}
	if err := eng.BlockAccount(ctx, "acc-2", "manual"); err != nil {
		t.Fatalf("BlockAccount: %v", err)
	}
}

// TestConfigurePolicy_RateLimitRetuneUnchangedKeys checks the rate-limit retune
// path: same barrier-key set succeeds on the live handle.
func TestConfigurePolicy_RateLimitRetuneUnchangedKeys(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Limits: []domain.Limit{
			rateLimit(domain.ScopeBroker, "", "", "100", "1s"),
			rateLimit(domain.ScopeAsset, "", "USD", "50", "1s"),
		},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	// Same keys (broker + asset USD), retuned values.
	same := []domain.Limit{
		rateLimit(domain.ScopeBroker, "", "", "7", "1s"),
		rateLimit(domain.ScopeAsset, "", "USD", "3", "1s"),
	}
	if err := eng.ConfigurePolicy(context.Background(),
		domain.PolicyRateLimit, same); err != nil {
		t.Fatalf("ConfigurePolicy retune: %v", err)
	}
}

// TestConfigurePolicy_OrderSizeReplacesAxes checks the order-size full-axes
// replace path applies on the live handle.
func TestConfigurePolicy_OrderSizeReplacesAxes(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Limits: []domain.Limit{orderSize(domain.ScopeBroker, "", "", "10", "")},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	// Replace the whole order-size set: drop the broker barrier value and add an
	// asset barrier. The broker barrier stays (pointer axis cannot be cleared in
	// isolation), but the asset axis is replaced wholesale and at least one
	// barrier remains, so the Configure succeeds.
	replaced := []domain.Limit{
		orderSize(domain.ScopeBroker, "", "", "20", ""),
		orderSize(domain.ScopeAsset, "", "USD", "5", ""),
	}
	if err := eng.ConfigurePolicy(context.Background(),
		domain.PolicyOrderSizeLimit, replaced); err != nil {
		t.Fatalf("ConfigurePolicy replace: %v", err)
	}
}

// TestConfigurePolicy_OrderSizeDropBrokerStub checks the order-size broker-drop
// stub: when the new barrier set drops a broker barrier the live handle still
// carries (the Configure surface cannot clear a broker barrier in isolation),
// ConfigurePolicy returns ErrNotImplemented rather than silently diverging the
// engine from the store.
func TestConfigurePolicy_OrderSizeDropBrokerStub(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Limits: []domain.Limit{
			orderSize(domain.ScopeBroker, "", "", "10", ""),
			orderSize(domain.ScopeAsset, "", "USD", "5", ""),
		},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	// Drop the broker barrier, keeping the asset barrier: at least one barrier
	// remains, but the Configure surface cannot clear the broker barrier.
	dropped := []domain.Limit{orderSize(domain.ScopeAsset, "", "USD", "5", "")}
	err = eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit, dropped)
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented for order_size broker drop, got %v", err)
	}
}

// TestConfigurePolicy_OrderSizeNoBrokerReplace checks that an order-size set
// built without a broker barrier can be replaced wholesale (still without a
// broker barrier): the broker-drop stub only fires when an existing broker
// barrier would be dropped.
func TestConfigurePolicy_OrderSizeNoBrokerReplace(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Limits: []domain.Limit{orderSize(domain.ScopeAsset, "", "USD", "5", "")},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	replaced := []domain.Limit{
		orderSize(domain.ScopeAsset, "", "USD", "7", ""),
		orderSize(domain.ScopeAccountAsset, "acc-1", "USD", "3", ""),
	}
	if err := eng.ConfigurePolicy(context.Background(),
		domain.PolicyOrderSizeLimit, replaced); err != nil {
		t.Fatalf("ConfigurePolicy replace without broker: %v", err)
	}
}

// TestConfigurePolicy_PnlReplacesAxes checks the P&L full-axes replace path
// applies on the live handle.
func TestConfigurePolicy_PnlReplacesAxes(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Limits: []domain.Limit{pnlBounds(domain.ScopeAsset, "", "USD", "-100", "100")},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	// Replace the broker (asset-scoped) axis wholesale with a different bound.
	replaced := []domain.Limit{pnlBounds(domain.ScopeAsset, "", "USD", "-50", "50")}
	if err := eng.ConfigurePolicy(context.Background(),
		domain.PolicyPnlBoundsKillSwitch, replaced); err != nil {
		t.Fatalf("ConfigurePolicy replace: %v", err)
	}
}

// TestConfigurePolicy_UnregisteredPolicyStub checks stub case (a): configuring a
// policy not registered at build time returns ErrNotImplemented and does not
// touch the handle.
func TestConfigurePolicy_UnregisteredPolicyStub(t *testing.T) {
	t.Parallel()
	// Build with only rate_limit registered; order_size_limit is absent.
	snap := Snapshot{
		Limits: []domain.Limit{rateLimit(domain.ScopeBroker, "", "", "100", "1s")},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	add := []domain.Limit{orderSize(domain.ScopeBroker, "", "", "10", "")}
	err = eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit, add)
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented for unregistered policy, got %v", err)
	}
}

// TestConfigurePolicy_RemoveLastBarrierStub checks stub case (b): an empty
// barrier set for a registered policy returns ErrNotImplemented.
func TestConfigurePolicy_RemoveLastBarrierStub(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Limits: []domain.Limit{rateLimit(domain.ScopeBroker, "", "", "100", "1s")},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	err = eng.ConfigurePolicy(context.Background(), domain.PolicyRateLimit, nil)
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented for empty settings, got %v", err)
	}
}

// TestConfigurePolicy_RateLimitAddRemoveBarrier checks the rate-limit axes are
// replaced wholesale on the live handle: a barrier can be added and removed at
// runtime as long as a barrier survives and no broker barrier is dropped in
// isolation.
func TestConfigurePolicy_RateLimitAddRemoveBarrier(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Limits: []domain.Limit{rateLimit(domain.ScopeBroker, "", "", "100", "1s")},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()
	ctx := context.Background()

	// Add an asset barrier alongside the broker barrier: the asset axis is
	// replaced wholesale, the broker barrier stays.
	added := []domain.Limit{
		rateLimit(domain.ScopeBroker, "", "", "100", "1s"),
		rateLimit(domain.ScopeAsset, "", "USD", "50", "1s"),
	}
	if err := eng.ConfigurePolicy(ctx, domain.PolicyRateLimit, added); err != nil {
		t.Fatalf("ConfigurePolicy add: %v", err)
	}

	// Remove the asset barrier again (broker barrier kept): the asset axis is
	// cleared by the empty non-nil slice; a barrier still remains.
	removed := []domain.Limit{rateLimit(domain.ScopeBroker, "", "", "100", "1s")}
	if err := eng.ConfigurePolicy(ctx, domain.PolicyRateLimit, removed); err != nil {
		t.Fatalf("ConfigurePolicy remove: %v", err)
	}
}

// TestConfigurePolicy_RateLimitDropBrokerStub checks the rate-limit broker-drop
// stub: dropping a broker barrier the live handle still carries (the Configure
// surface cannot clear it in isolation) returns ErrNotImplemented rather than
// diverging the engine from the store.
func TestConfigurePolicy_RateLimitDropBrokerStub(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Limits: []domain.Limit{
			rateLimit(domain.ScopeBroker, "", "", "100", "1s"),
			rateLimit(domain.ScopeAsset, "", "USD", "50", "1s"),
		},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	// Drop the broker barrier, keeping the asset barrier: at least one barrier
	// remains, but the Configure surface cannot clear the broker barrier.
	dropped := []domain.Limit{rateLimit(domain.ScopeAsset, "", "USD", "50", "1s")}
	err = eng.ConfigurePolicy(context.Background(), domain.PolicyRateLimit, dropped)
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented for rate_limit broker drop, got %v", err)
	}
}

// TestPnlBoundsAxes_AccountUpdateShape checks the account axis maps onto the
// Update shape (bounds without InitialPnl) so a runtime retune never resets the
// live accumulated P&L.
func TestPnlBoundsAxes_AccountUpdateShape(t *testing.T) {
	t.Parallel()
	brokers, accounts, err := pnlBoundsAxes(
		[]domain.Limit{pnlBounds(domain.ScopeAccountAsset, "acc-1", "USD", "-100", "100")})
	if err != nil {
		t.Fatalf("pnlBoundsAxes: %v", err)
	}
	if len(brokers) != 0 || len(accounts) != 1 {
		t.Fatalf("axis counts wrong: %d %d", len(brokers), len(accounts))
	}
	update := accounts[0]
	if update.Barrier.SettlementAsset.String() != "USD" {
		t.Fatalf("settlement asset mismatch: %s", update.Barrier.SettlementAsset)
	}
	if _, ok := update.Barrier.LowerBound.Get(); !ok {
		t.Fatalf("want lower bound present")
	}
}

// fundedBalance seeds an account with absolute holdings on one asset so a spot
// limit order can reserve against it.
func fundedBalance(account, asset, available string) domain.Balance {
	return domain.Balance{
		Tenant:    domain.DefaultTenant,
		Account:   domain.AccountID(account),
		Asset:     asset,
		Available: available,
	}
}

// checkProbe builds a buy/sell limit OrderProbe for the dry-run tests.
func checkProbe(account string, side domain.OrderSide, qty, price string) domain.OrderProbe {
	return domain.OrderProbe{
		Account:     domain.AccountID(account),
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        side,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: qty,
		Price:       price,
	}
}

// TestEngine_CheckOrderPassCapturesLock runs a non-mutating dry-run for a funded
// limit order and checks it passes, capturing the would-be reservation lock
// prices the same way SubmitOrder does.
func TestEngine_CheckOrderPassCapturesLock(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Balances: []domain.Balance{
			fundedBalance("acc-1", "USD", "1000000"),
			fundedBalance("acc-1", "AAPL", "1000000"),
		},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	out, err := eng.CheckOrder(context.Background(), checkProbe("acc-1", domain.OrderSideBuy, "1", "100"))
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if !out.Passed {
		t.Fatalf("want pass, got rejects=%+v block=%+v", out.Rejects, out.WouldBlock)
	}
	if len(out.Rejects) != 0 || out.WouldBlock != nil {
		t.Fatalf("pass must carry no rejects/block: %+v %+v", out.Rejects, out.WouldBlock)
	}
}

// TestEngine_CheckOrderRejectStructured runs a dry-run for an unfunded buy and
// checks it rejects with a structured reject (insufficient funds), not an error.
func TestEngine_CheckOrderRejectStructured(t *testing.T) {
	t.Parallel()
	eng, err := BuildOpenPitEngine("", Snapshot{})
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	out, err := eng.CheckOrder(context.Background(), checkProbe("acc-1", domain.OrderSideBuy, "1", "100"))
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if out.Passed {
		t.Fatalf("want reject for unfunded account")
	}
	if len(out.Rejects) == 0 {
		t.Fatalf("reject must carry structured rejects")
	}
	if out.Rejects[0].Code == "" {
		t.Fatalf("reject code must be a stable string: %+v", out.Rejects[0])
	}
}

// TestEngine_CheckOrderWouldBlock runs a dry-run for an account the engine has
// kill-switched and checks the account-scoped reject surfaces a would-be block
// stamped with the probe's account.
func TestEngine_CheckOrderWouldBlock(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Accounts: []domain.Account{
			{Tenant: domain.DefaultTenant, ID: "acc-1", Blocked: true, BlockReason: "risk"},
		},
		Balances: []domain.Balance{
			fundedBalance("acc-1", "USD", "1000000"),
			fundedBalance("acc-1", "AAPL", "1000000"),
		},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	out, err := eng.CheckOrder(context.Background(), checkProbe("acc-1", domain.OrderSideBuy, "1", "100"))
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if out.Passed {
		t.Fatalf("want reject for blocked account")
	}
	if out.WouldBlock == nil {
		t.Fatalf("blocked account must surface a would-be block")
	}
	if out.WouldBlock.Account != "acc-1" {
		t.Fatalf("would-block must be stamped with the probe account, got %q", out.WouldBlock.Account)
	}
}

// TestEngine_CheckOrderIsNonMutating is the ticket's headline acceptance test.
// It runs against the REAL native engine (BuildOpenPitEngine), not a fake. A
// rate_limit broker barrier of max_orders=1 governs a funded account. Repeated
// CheckOrder dry-runs must consume none of the budget: afterwards the first real
// SubmitOrder still passes (the one allowed slot is intact), and only the second
// SubmitOrder is throttled - proving the checks reserved nothing and left engine
// state unchanged.
func TestEngine_CheckOrderIsNonMutating(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Limits: []domain.Limit{rateLimit(domain.ScopeBroker, "", "", "1", "1m")},
		Balances: []domain.Balance{
			fundedBalance("acc-1", "USD", "1000000"),
			fundedBalance("acc-1", "AAPL", "1000000"),
		},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()
	ctx := context.Background()

	// Many dry-runs: each must pass and consume no rate-limit budget.
	for i := 0; i < 5; i++ {
		out, err := eng.CheckOrder(ctx, checkProbe("acc-1", domain.OrderSideBuy, "1", "100"))
		if err != nil {
			t.Fatalf("CheckOrder #%d: %v", i, err)
		}
		if !out.Passed {
			t.Fatalf("dry-run #%d must pass, got rejects=%+v", i, out.Rejects)
		}
	}

	order := domain.Order{
		Tenant: domain.DefaultTenant, Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
		AmountValue: "1", Price: "100",
	}

	// First real submit: the single allowed slot is still available because the
	// dry-runs consumed nothing.
	first, err := eng.SubmitOrder(ctx, order)
	if err != nil {
		t.Fatalf("first SubmitOrder: %v", err)
	}
	if !first.Accepted {
		t.Fatalf("first submit must pass after dry-runs (budget intact), got %+v", first.Rejects)
	}

	// Second real submit: now the one slot is spent, so the rate limit trips.
	second, err := eng.SubmitOrder(ctx, order)
	if err != nil {
		t.Fatalf("second SubmitOrder: %v", err)
	}
	if second.Accepted {
		t.Fatalf("second submit must be throttled by max_orders=1")
	}
	if !hasRejectCode(second.Rejects, "rate_limit_exceeded") {
		t.Fatalf("want rate_limit_exceeded, got %+v", second.Rejects)
	}
}

// hasRejectCode reports whether rejects carry a reject with the given code.
func hasRejectCode(rejects []domain.OrderReject, code string) bool {
	for _, r := range rejects {
		if r.Code == code {
			return true
		}
	}
	return false
}

// TestNewAsset_BadFormatIsInvalid checks a malformed asset code (caller input)
// wraps domain.ErrInvalid so the HTTP surface reports 400, not 500.
func TestNewAsset_BadFormatIsInvalid(t *testing.T) {
	t.Parallel()
	if _, err := newAsset("bad asset"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad asset, got %v", err)
	}
}

// TestNewAccountID_BadFormatIsInvalid checks a malformed account id (caller
// input) wraps domain.ErrInvalid.
func TestNewAccountID_BadFormatIsInvalid(t *testing.T) {
	t.Parallel()
	if _, err := newAccountID(domain.AccountID("bad account")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad account id, got %v", err)
	}
}

// TestOrderSide_UnknownIsInvalid checks an unknown order side (caller input)
// wraps domain.ErrInvalid.
func TestOrderSide_UnknownIsInvalid(t *testing.T) {
	t.Parallel()
	if _, err := orderSide(domain.OrderSide("sideways")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unknown side, got %v", err)
	}
}

// TestTradeAmountFrom_InvalidInputs checks the order amount mapper wraps
// domain.ErrInvalid for an unknown amount kind (the "base" bug) and for a
// non-decimal quantity/volume value, so malformed input surfaces as 400.
func TestTradeAmountFrom_InvalidInputs(t *testing.T) {
	t.Parallel()
	if _, err := tradeAmountFrom(domain.OrderAmountKind("base"), "1"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unknown amount kind, got %v", err)
	}
	if _, err := tradeAmountFrom(domain.OrderAmountKindQuantity, "not-a-number"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad quantity, got %v", err)
	}
	if _, err := tradeAmountFrom(domain.OrderAmountKindVolume, "not-a-number"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad volume, got %v", err)
	}
}

// TestOrderModelFrom_InvalidInputs checks the order mapper wraps
// domain.ErrInvalid for a bad asset, a bad amount, and a bad limit price.
func TestOrderModelFrom_InvalidInputs(t *testing.T) {
	t.Parallel()
	base := domain.Order{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
	}

	badAsset := base
	badAsset.BaseAsset = "bad asset"
	if _, err := orderModelFrom(badAsset); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad base asset, got %v", err)
	}

	badAmount := base
	badAmount.AmountValue = "not-a-number"
	if _, err := orderModelFrom(badAmount); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad amount, got %v", err)
	}

	badPrice := base
	badPrice.Price = "not-a-number"
	if _, err := orderModelFrom(badPrice); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad price, got %v", err)
	}
}

// TestExecutionReportFrom_InvalidInputs checks the execution-report mapper wraps
// domain.ErrInvalid for a bad fill price, a bad fill quantity, and a bad lock
// price (all caller input).
func TestExecutionReportFrom_InvalidInputs(t *testing.T) {
	t.Parallel()
	base := domain.ExecutionReportInput{
		BaseAsset: "AAPL", QuoteAsset: "USD", Account: "acc-1", Side: domain.OrderSideBuy,
		FillQuantity: "1", FillPrice: "100", OrderID: 1,
	}

	badPrice := base
	badPrice.FillPrice = "not-a-number"
	if _, err := executionReportFrom(badPrice); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad fill price, got %v", err)
	}

	badQty := base
	badQty.FillQuantity = "not-a-number"
	if _, err := executionReportFrom(badQty); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad fill quantity, got %v", err)
	}

	badLock := base
	badLock.LockPrice = "not-a-number"
	if _, err := executionReportFrom(badLock); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad lock price, got %v", err)
	}
}

// TestAdjustmentAmountFrom_InvalidInputs checks the adjustment amount mapper
// wraps domain.ErrInvalid for a non-decimal value and for an unknown mode.
func TestAdjustmentAmountFrom_InvalidInputs(t *testing.T) {
	t.Parallel()
	if _, err := adjustmentAmountFrom(
		domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "not-a-number"}, "balance",
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad adjustment value, got %v", err)
	}
	if _, err := adjustmentAmountFrom(
		domain.AdjustmentAmount{Mode: domain.AdjustmentAmountMode("sideways"), Value: "1"}, "balance",
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unknown adjustment mode, got %v", err)
	}
}

// TestAccountAdjustmentFromRequest_InvalidInputs checks the adjustment request
// mapper wraps domain.ErrInvalid for a bad asset, a bad average-entry-price, and
// a bad bound (all caller input).
func TestAccountAdjustmentFromRequest_InvalidInputs(t *testing.T) {
	t.Parallel()
	delta := func(v string) *domain.AdjustmentAmount {
		return &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: v}
	}

	badAsset := domain.AdjustmentRequest{Asset: "bad asset", Balance: delta("1")}
	if _, err := accountAdjustmentFromRequest(badAsset); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad asset, got %v", err)
	}

	badPrice := domain.AdjustmentRequest{
		Asset: "USD", Balance: delta("1"), AverageEntryPrice: "not-a-number",
	}
	if _, err := accountAdjustmentFromRequest(badPrice); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad average entry price, got %v", err)
	}

	badBound := domain.AdjustmentRequest{
		Asset: "USD", Balance: delta("1"),
		BalanceBounds: &domain.AdjustmentBounds{Lower: "not-a-number"},
	}
	if _, err := accountAdjustmentFromRequest(badBound); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad bound, got %v", err)
	}
}

// TestSanitizeText_CleansInvalidUTF8AndControls checks the boundary sanitizer
// drops invalid UTF-8 bytes and strips control characters while preserving
// normal printable text and spaces. This guards the control plane against the
// upstream bug where the SDK binding returns garbage reason/details for an
// account-blocked (Engine-scope) dry-run reject.
func TestSanitizeText_CleansInvalidUTF8AndControls(t *testing.T) {
	t.Parallel()

	// Mimics the observed mojibake: "account_blocked Engine" followed by
	// non-UTF-8 bytes (0x80, 0xff) interleaved with control characters.
	garbage := "account_blocked Engine \x80\x16V \x07\xff\x78x"
	got := sanitizeText(garbage)
	if !utf8.ValidString(got) {
		t.Fatalf("sanitized text must be valid UTF-8, got %q", got)
	}
	for _, r := range got {
		if unicode.IsControl(r) {
			t.Fatalf("sanitized text must carry no control runes, got %q", got)
		}
	}
	// The printable prefix and the surviving printable bytes are preserved; the
	// invalid/control bytes are dropped.
	if want := "account_blocked Engine V xx"; got != want {
		t.Fatalf("sanitized text = %q, want %q", got, want)
	}

	// Clean input is returned unchanged.
	if got := sanitizeText("insufficient funds"); got != "insufficient funds" {
		t.Fatalf("clean text must pass through, got %q", got)
	}
	// Fully garbage input collapses to empty, which is acceptable.
	if got := sanitizeText("\x80\xff\x01"); got != "" {
		t.Fatalf("all-garbage text must sanitize to empty, got %q", got)
	}
}

func orderSize(scope, account, asset, maxQty, maxNotional string) domain.Limit {
	values := []domain.LimitValue{}
	if maxQty != "" {
		values = append(values, domain.LimitValue{Kind: domain.KindMaxQuantity, Value: maxQty})
	}
	if maxNotional != "" {
		values = append(values, domain.LimitValue{Kind: domain.KindMaxNotional, Value: maxNotional})
	}
	return domain.Limit{
		Target: domain.LimitTarget{
			Tenant:  domain.DefaultTenant,
			Policy:  domain.PolicyOrderSizeLimit,
			Scope:   scope,
			Account: domain.AccountID(account),
			Asset:   asset,
		},
		Values: values,
	}
}

func pnlBounds(scope, account, asset, lower, upper string) domain.Limit {
	values := []domain.LimitValue{}
	if lower != "" {
		values = append(values, domain.LimitValue{Kind: domain.KindLowerBound, Value: lower})
	}
	if upper != "" {
		values = append(values, domain.LimitValue{Kind: domain.KindUpperBound, Value: upper})
	}
	return domain.Limit{
		Target: domain.LimitTarget{
			Tenant:  domain.DefaultTenant,
			Policy:  domain.PolicyPnlBoundsKillSwitch,
			Scope:   scope,
			Account: domain.AccountID(account),
			Asset:   asset,
		},
		Values: values,
	}
}
