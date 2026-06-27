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

package native

import (
	"context"
	"errors"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"go.openpit.dev/openpit/param"

	"go.openpit.dev/officer/framework/domain"
)

// testEngineAccountID assigns a deterministic, distinct engine account id to a
// test account code so the resolver maps it without hashing. Tests address
// accounts by code; the connector would assign these ids collision-free.
func testEngineAccountID(code string) domain.EngineAccountID {
	switch code {
	case "acc-1", "1":
		return 1
	case "acc-2", "2":
		return 2
	case "acc-3", "3":
		return 3
	default:
		// A stable non-zero fallback for any other code used in a test.
		return domain.EngineAccountID(1000 + len(code))
	}
}

// account builds a snapshot account carrying its stored engine account id so the
// resolver has an entry for code.
func account(code string) domain.Account {
	return domain.Account{Code: domain.AccountID(code), EngineAccountID: testEngineAccountID(code)}
}

// blockedAccount builds a blocked snapshot account with a stored engine id.
func blockedAccount(code, reason string) domain.Account {
	a := account(code)
	a.Blocked = true
	a.BlockReason = reason
	return a
}

// testResolver builds an idResolver covering the given account codes, mirroring
// the resolver BuildOpenPitEngine builds from a Snapshot. It is used by the pure
// mapping tests that do not build a full engine.
func testResolver(codes ...string) idResolver {
	accounts := make([]domain.Account, 0, len(codes))
	for _, code := range codes {
		accounts = append(accounts, account(code))
	}
	res, err := newIDResolver(accounts, nil)
	if err != nil {
		panic(err)
	}
	return res
}

func rateLimit(scope, acct, asset string, maxOrders uint64, window time.Duration) domain.LimitRate {
	return domain.LimitRate{
		Scope:     scope,
		Account:   domain.AccountID(acct),
		Asset:     asset,
		MaxOrders: maxOrders,
		Window:    window,
	}
}

func orderSize(scope, acct, asset, maxQty, maxNotional string) domain.LimitOrderSize {
	return domain.LimitOrderSize{
		Scope:       scope,
		Account:     domain.AccountID(acct),
		Asset:       asset,
		MaxQuantity: maxQty,
		MaxNotional: maxNotional,
	}
}

func pnlBounds(scope, acct, asset, lower, upper string) domain.LimitPnlBounds {
	return domain.LimitPnlBounds{
		Scope:      scope,
		Account:    domain.AccountID(acct),
		Asset:      asset,
		LowerBound: lower,
		UpperBound: upper,
	}
}

func TestRateLimitReady_AllAxes(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1")
	limits := []domain.LimitRate{
		rateLimit(domain.ScopeBroker, "", "", 1000, time.Minute),
		rateLimit(domain.ScopeAsset, "", "USD", 500, time.Minute),
		rateLimit(domain.ScopeAccount, "acc-1", "", 200, time.Minute),
		rateLimit(domain.ScopeAccountAsset, "acc-1", "USD", 100, time.Minute),
	}
	ready, err := rateLimitReady(limits, res)
	if err != nil {
		t.Fatalf("rateLimitReady: %v", err)
	}
	if ready == nil {
		t.Fatalf("nil ready builder")
	}
}

// TestRateLimitReady_UnknownAccountInvalid checks an account-scoped barrier for a
// code the resolver does not cover (no stored engine id) wraps domain.ErrInvalid
// rather than hashing the string into an engine id.
func TestRateLimitReady_UnknownAccountInvalid(t *testing.T) {
	t.Parallel()
	res := testResolver() // empty: no accounts
	_, err := rateLimitReady(
		[]domain.LimitRate{rateLimit(domain.ScopeAccount, "ghost", "", 1, time.Minute)}, res)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unknown account, got %v", err)
	}
}

func TestOrderSizeValue_ParsesBoth(t *testing.T) {
	t.Parallel()
	limit, err := orderSizeValue(orderSize(domain.ScopeBroker, "", "", "10", "1000"))
	if err != nil {
		t.Fatalf("orderSizeValue: %v", err)
	}
	want, err := param.NewQuantityFromString("10")
	if err != nil {
		t.Fatalf("quantity: %v", err)
	}
	if !limit.MaxQuantity.Equal(want) {
		t.Fatalf("max_quantity mismatch")
	}
}

func TestPnlBoundsValues_Optionals(t *testing.T) {
	t.Parallel()
	lower, upper, initial, err := pnlBoundsValues(
		domain.LimitPnlBounds{Scope: domain.ScopeAsset, Asset: "USD", LowerBound: "-100"})
	if err != nil {
		t.Fatalf("pnlBoundsValues: %v", err)
	}
	if _, ok := lower.Get(); !ok {
		t.Fatalf("want lower bound present")
	}
	if _, ok := upper.Get(); ok {
		t.Fatalf("want upper bound absent")
	}
	if _, ok := initial.Get(); ok {
		t.Fatalf("want initial pnl absent")
	}
}

// TestRateLimitAxes_AllAxes checks the rate-limit Configure axes are built with
// always-non-nil slices and the broker barrier set from the broker-scoped
// limit.
func TestRateLimitAxes_AllAxes(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1")
	limits := []domain.LimitRate{
		rateLimit(domain.ScopeBroker, "", "", 1000, time.Minute),
		rateLimit(domain.ScopeAsset, "", "USD", 500, time.Minute),
		rateLimit(domain.ScopeAccount, "acc-1", "", 200, time.Minute),
		rateLimit(domain.ScopeAccountAsset, "acc-1", "USD", 100, time.Minute),
	}
	broker, assets, accounts, accountAssets, err := rateLimitAxes(limits, res)
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
		[]domain.LimitOrderSize{orderSize(domain.ScopeBroker, "", "", "10", "")}, testResolver())
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
		[]domain.LimitPnlBounds{pnlBounds(domain.ScopeAsset, "", "USD", "-100", "100")}, testResolver())
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
	snap := Snapshot{
		Accounts:        []domain.Account{account("acc-1")},
		RateLimits:      []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 100, time.Second)},
		OrderSizeLimits: []domain.LimitOrderSize{orderSize(domain.ScopeBroker, "", "", "10", "")},
		PnlBoundsLimits: []domain.LimitPnlBounds{pnlBounds(domain.ScopeAsset, "", "USD", "-100", "100")},
	}
	res, err := newIDResolver(snap.Accounts, snap.Groups)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	eng, service, registered, err := buildEngine(snap, res)
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

// TestNewIDResolver_RejectsUnassignedEngineID checks the resolver build rejects
// an account whose stored engine id is unassigned (zero), since that is
// corruption of our own persisted ids, not a hashable input.
func TestNewIDResolver_RejectsUnassignedEngineID(t *testing.T) {
	t.Parallel()
	_, err := newIDResolver(
		[]domain.Account{{Code: "acc-1", EngineAccountID: 0}}, nil)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unassigned engine id, got %v", err)
	}
}

// TestNewIDResolver_UsesStoredEngineIDs checks the resolver maps a code to a
// param.AccountID built from the stored uint engine id (param.NewAccountIDFromUint64),
// not a hash of the code: the engine id string form is the decimal of the stored
// integer.
func TestNewIDResolver_UsesStoredEngineIDs(t *testing.T) {
	t.Parallel()
	res, err := newIDResolver([]domain.Account{
		{Code: "acc-1", EngineAccountID: 7},
	}, []domain.AccountGroup{
		{Code: "grp-1", EngineGroupID: 9},
	})
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	got, err := res.account("acc-1")
	if err != nil {
		t.Fatalf("resolve account: %v", err)
	}
	if want := param.NewAccountIDFromUint64(7); got.String() != want.String() {
		t.Fatalf("account engine id = %s, want %s (from stored uint, not hash)", got, want)
	}
	grp, err := res.group("grp-1")
	if err != nil {
		t.Fatalf("resolve group: %v", err)
	}
	want, err := param.NewAccountGroupIDFromUint32(9)
	if err != nil {
		t.Fatalf("want group id: %v", err)
	}
	if grp.String() != want.String() {
		t.Fatalf("group engine id = %s, want %s", grp, want)
	}
}

// TestBuildOpenPitEngine_SeedsFromSnapshot builds the one engine from a seeded
// snapshot (a blocked account plus a rate-limit barrier) and retunes the
// rate-limit policy in place. It needs the native dylib at run time.
func TestBuildOpenPitEngine_SeedsFromSnapshot(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Accounts:   []domain.Account{blockedAccount("acc-1", "risk"), account("acc-2")},
		RateLimits: []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 100, time.Second)},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()
	ctx := context.Background()

	// Retune the rate-limit policy (broker barrier kept) on the live handle: the
	// axes are replaced wholesale, no rebuild.
	newLimits := LimitSet{RateLimits: []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 5, time.Second)}}
	if err := eng.ConfigurePolicy(ctx, domain.PolicyRateLimit, newLimits); err != nil {
		t.Fatalf("ConfigurePolicy retune: %v", err)
	}

	// Both accounts are in the snapshot, so the resolver covers them by their
	// stored engine ids - no string hashing.
	if err := eng.UnblockAccount(ctx, "acc-1"); err != nil {
		t.Fatalf("UnblockAccount: %v", err)
	}
	if err := eng.BlockAccount(ctx, "acc-2", "manual"); err != nil {
		t.Fatalf("BlockAccount: %v", err)
	}

	// An account the snapshot does not cover has no stored engine id, so the
	// resolver rejects it as invalid rather than hashing its code.
	if err := eng.BlockAccount(ctx, "ghost", "manual"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("BlockAccount(unknown) = %v, want ErrInvalid", err)
	}
}

// TestConfigurePolicy_RateLimitRetuneUnchangedKeys checks the rate-limit retune
// path: same barrier-key set succeeds on the live handle.
func TestConfigurePolicy_RateLimitRetuneUnchangedKeys(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		RateLimits: []domain.LimitRate{
			rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
			rateLimit(domain.ScopeAsset, "", "USD", 50, time.Second),
		},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	same := LimitSet{RateLimits: []domain.LimitRate{
		rateLimit(domain.ScopeBroker, "", "", 7, time.Second),
		rateLimit(domain.ScopeAsset, "", "USD", 3, time.Second),
	}}
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
		OrderSizeLimits: []domain.LimitOrderSize{orderSize(domain.ScopeBroker, "", "", "10", "")},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	replaced := LimitSet{OrderSizeLimits: []domain.LimitOrderSize{
		orderSize(domain.ScopeBroker, "", "", "20", ""),
		orderSize(domain.ScopeAsset, "", "USD", "5", ""),
	}}
	if err := eng.ConfigurePolicy(context.Background(),
		domain.PolicyOrderSizeLimit, replaced); err != nil {
		t.Fatalf("ConfigurePolicy replace: %v", err)
	}
}

// TestConfigurePolicy_OrderSizeDropBrokerStub checks the order-size broker-drop
// stub returns ErrNotImplemented.
func TestConfigurePolicy_OrderSizeDropBrokerStub(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		OrderSizeLimits: []domain.LimitOrderSize{
			orderSize(domain.ScopeBroker, "", "", "10", ""),
			orderSize(domain.ScopeAsset, "", "USD", "5", ""),
		},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	dropped := LimitSet{OrderSizeLimits: []domain.LimitOrderSize{orderSize(domain.ScopeAsset, "", "USD", "5", "")}}
	err = eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit, dropped)
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented for order_size broker drop, got %v", err)
	}
}

// TestConfigurePolicy_OrderSizeNoBrokerReplace checks an order-size set built
// without a broker barrier can be replaced wholesale.
func TestConfigurePolicy_OrderSizeNoBrokerReplace(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Accounts:        []domain.Account{account("acc-1")},
		OrderSizeLimits: []domain.LimitOrderSize{orderSize(domain.ScopeAsset, "", "USD", "5", "")},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	replaced := LimitSet{OrderSizeLimits: []domain.LimitOrderSize{
		orderSize(domain.ScopeAsset, "", "USD", "7", ""),
		orderSize(domain.ScopeAccountAsset, "acc-1", "USD", "3", ""),
	}}
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
		PnlBoundsLimits: []domain.LimitPnlBounds{pnlBounds(domain.ScopeAsset, "", "USD", "-100", "100")},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	replaced := LimitSet{PnlBoundsLimits: []domain.LimitPnlBounds{pnlBounds(domain.ScopeAsset, "", "USD", "-50", "50")}}
	if err := eng.ConfigurePolicy(context.Background(),
		domain.PolicyPnlBoundsKillSwitch, replaced); err != nil {
		t.Fatalf("ConfigurePolicy replace: %v", err)
	}
}

// TestConfigurePolicy_UnregisteredPolicyStub checks configuring a policy not
// registered at build time returns ErrNotImplemented.
func TestConfigurePolicy_UnregisteredPolicyStub(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		RateLimits: []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 100, time.Second)},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	add := LimitSet{OrderSizeLimits: []domain.LimitOrderSize{orderSize(domain.ScopeBroker, "", "", "10", "")}}
	err = eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit, add)
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented for unregistered policy, got %v", err)
	}
}

// TestConfigurePolicy_RemoveLastBarrierStub checks an empty barrier set for a
// registered policy returns ErrNotImplemented.
func TestConfigurePolicy_RemoveLastBarrierStub(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		RateLimits: []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 100, time.Second)},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	err = eng.ConfigurePolicy(context.Background(), domain.PolicyRateLimit, LimitSet{})
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented for empty settings, got %v", err)
	}
}

// TestConfigurePolicy_RateLimitAddRemoveBarrier checks the rate-limit axes are
// replaced wholesale on the live handle.
func TestConfigurePolicy_RateLimitAddRemoveBarrier(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		RateLimits: []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 100, time.Second)},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()
	ctx := context.Background()

	added := LimitSet{RateLimits: []domain.LimitRate{
		rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
		rateLimit(domain.ScopeAsset, "", "USD", 50, time.Second),
	}}
	if err := eng.ConfigurePolicy(ctx, domain.PolicyRateLimit, added); err != nil {
		t.Fatalf("ConfigurePolicy add: %v", err)
	}

	removed := LimitSet{RateLimits: []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 100, time.Second)}}
	if err := eng.ConfigurePolicy(ctx, domain.PolicyRateLimit, removed); err != nil {
		t.Fatalf("ConfigurePolicy remove: %v", err)
	}
}

// TestConfigurePolicy_RateLimitDropBrokerStub checks the rate-limit broker-drop
// stub returns ErrNotImplemented.
func TestConfigurePolicy_RateLimitDropBrokerStub(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		RateLimits: []domain.LimitRate{
			rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
			rateLimit(domain.ScopeAsset, "", "USD", 50, time.Second),
		},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	dropped := LimitSet{RateLimits: []domain.LimitRate{rateLimit(domain.ScopeAsset, "", "USD", 50, time.Second)}}
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
		[]domain.LimitPnlBounds{pnlBounds(domain.ScopeAccountAsset, "acc-1", "USD", "-100", "100")},
		testResolver("acc-1"))
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
func fundedBalance(acct, asset, available string) domain.Balance {
	return domain.Balance{
		Account:   domain.AccountID(acct),
		Asset:     asset,
		Available: available,
	}
}

// checkProbe builds a buy/sell limit OrderProbe for the dry-run tests.
func checkProbe(acct string, side domain.OrderSide, qty, price string) domain.OrderProbe {
	return domain.OrderProbe{
		Account:     domain.AccountID(acct),
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
		Accounts: []domain.Account{account("acc-1")},
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
	eng, err := BuildOpenPitEngine("", Snapshot{Accounts: []domain.Account{account("acc-1")}})
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
		Accounts: []domain.Account{blockedAccount("acc-1", "risk")},
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
	if out.WouldBlock.Reason != "risk" {
		t.Fatalf("would-block reason = %q, want risk", out.WouldBlock.Reason)
	}
}

func TestEngine_CheckOrderDropsGarbledAccountBlockReason(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Accounts: []domain.Account{blockedAccount("acc-1", "0}")},
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
	if len(out.Rejects) != 1 {
		t.Fatalf("rejects len = %d, want 1", len(out.Rejects))
	}
	if out.Rejects[0].Reason != "" || out.Rejects[0].Details != "" {
		t.Fatalf("garbled reject text must be empty, got %+v", out.Rejects[0])
	}
	if out.WouldBlock == nil {
		t.Fatalf("blocked account must surface a would-be block")
	}
	if out.WouldBlock.Reason != "" || out.WouldBlock.Details != "" {
		t.Fatalf("garbled would-block text must be empty, got %+v", out.WouldBlock)
	}
}

// TestEngine_CheckOrderIsNonMutating runs against the REAL native engine. A
// rate_limit broker barrier of max_orders=1 governs a funded account. Repeated
// CheckOrder dry-runs must consume none of the budget: afterwards the first real
// SubmitOrder still passes, and only the second SubmitOrder is throttled -
// proving the checks reserved nothing and left engine state unchanged.
func TestEngine_CheckOrderIsNonMutating(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Accounts:   []domain.Account{account("acc-1")},
		RateLimits: []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 1, time.Minute)},
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
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
		AmountValue: "1", Price: "100",
	}

	first, err := eng.SubmitOrder(ctx, order)
	if err != nil {
		t.Fatalf("first SubmitOrder: %v", err)
	}
	if !first.Accepted {
		t.Fatalf("first submit must pass after dry-runs (budget intact), got %+v", first.Rejects)
	}

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

// TestNewAsset_BadFormatIsInvalid checks a core-rejected asset code (caller
// input) wraps domain.ErrInvalid so the HTTP surface reports 400, not 500.
func TestNewAsset_BadFormatIsInvalid(t *testing.T) {
	t.Parallel()
	if _, err := newAsset(" "); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for blank asset, got %v", err)
	}
}

// TestResolverUnknownAccountIsInvalid checks resolving a code the resolver does
// not cover wraps domain.ErrInvalid (so an order for an unknown account is a 400,
// not a 500, and is never silently hashed into an engine id).
func TestResolverUnknownAccountIsInvalid(t *testing.T) {
	t.Parallel()
	if _, err := testResolver().account(domain.AccountID("nope")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unknown account, got %v", err)
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
// domain.ErrInvalid for an unknown amount kind and for a non-decimal
// quantity/volume value.
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
	res := testResolver("acc-1")
	base := domain.Order{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
	}

	badAsset := base
	badAsset.BaseAsset = " "
	if _, err := orderModelFrom(badAsset, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for blank base asset, got %v", err)
	}

	badAmount := base
	badAmount.AmountValue = "not-a-number"
	if _, err := orderModelFrom(badAmount, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad amount, got %v", err)
	}

	badPrice := base
	badPrice.Price = "not-a-number"
	if _, err := orderModelFrom(badPrice, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad price, got %v", err)
	}

	badAccount := base
	badAccount.Account = "ghost"
	if _, err := orderModelFrom(badAccount, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unknown account, got %v", err)
	}
}

// TestExecutionReportFrom_InvalidInputs checks the execution-report mapper wraps
// domain.ErrInvalid for a bad fill price, a bad fill quantity, and a bad lock
// price (all caller input).
func TestExecutionReportFrom_InvalidInputs(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1")
	base := domain.ExecutionReportInput{
		BaseAsset: "AAPL", QuoteAsset: "USD", Account: "acc-1", Side: domain.OrderSideBuy,
		FillQuantity: "1", FillPrice: "100",
	}

	badPrice := base
	badPrice.FillPrice = "not-a-number"
	if _, err := executionReportFrom(badPrice, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad fill price, got %v", err)
	}

	badQty := base
	badQty.FillQuantity = "not-a-number"
	if _, err := executionReportFrom(badQty, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad fill quantity, got %v", err)
	}

	badLock := base
	badLock.LockPrice = "not-a-number"
	if _, err := executionReportFrom(badLock, res); !errors.Is(err, domain.ErrInvalid) {
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

	badAsset := domain.AdjustmentRequest{Asset: " ", Balance: delta("1")}
	if _, err := accountAdjustmentFromRequest(badAsset); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for blank asset, got %v", err)
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
// normal printable text and spaces.
func TestSanitizeText_CleansInvalidUTF8AndControls(t *testing.T) {
	t.Parallel()

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
	if want := "account_blocked Engine V xx"; got != want {
		t.Fatalf("sanitized text = %q, want %q", got, want)
	}

	if got := sanitizeText("insufficient funds"); got != "insufficient funds" {
		t.Fatalf("clean text must pass through, got %q", got)
	}
	if got := sanitizeText("404"); got != "404" {
		t.Fatalf("clean numeric text must pass through, got %q", got)
	}
	for _, input := range []string{"\x80\xff\x01", "0}", "}"} {
		if got := sanitizeText(input); got != "" {
			t.Fatalf("garbage text %q must sanitize to empty, got %q", input, got)
		}
	}
}
