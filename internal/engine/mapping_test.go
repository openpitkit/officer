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
	eng, registered, err := buildEngine(byPolicy)
	if err != nil {
		t.Fatalf("buildEngine: %v", err)
	}
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

	// Retune the rate-limit policy with the same barrier-key set (broker only):
	// keys unchanged, so this is a retune on the live handle, not a rebuild.
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

// TestConfigurePolicy_RateLimitAddBarrierStub checks stub case (c): a rate-limit
// barrier-key change (here an added asset barrier) returns ErrNotImplemented,
// because the rate-limit Configure is patch-only.
func TestConfigurePolicy_RateLimitAddBarrierStub(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Limits: []domain.Limit{rateLimit(domain.ScopeBroker, "", "", "100", "1s")},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	// Add an asset barrier: the key set gains a key, which the patch-only
	// rate-limit Configure cannot express.
	added := []domain.Limit{
		rateLimit(domain.ScopeBroker, "", "", "100", "1s"),
		rateLimit(domain.ScopeAsset, "", "USD", "50", "1s"),
	}
	err = eng.ConfigurePolicy(context.Background(), domain.PolicyRateLimit, added)
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented for rate_limit add, got %v", err)
	}

	// A removed key is likewise unsupported.
	removed := []domain.Limit{rateLimit(domain.ScopeAsset, "", "USD", "50", "1s")}
	err = eng.ConfigurePolicy(context.Background(), domain.PolicyRateLimit, removed)
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented for rate_limit remove, got %v", err)
	}
}

func TestEngine_CheckOrderUnsupported(t *testing.T) {
	t.Parallel()
	eng, err := BuildOpenPitEngine("", Snapshot{})
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	_, err = eng.CheckOrder(context.Background(), domain.OrderProbe{})
	if !errors.Is(err, ErrCheckUnsupported) {
		t.Fatalf("want ErrCheckUnsupported, got %v", err)
	}
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("ErrCheckUnsupported must wrap ErrNotImplemented, got %v", err)
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
