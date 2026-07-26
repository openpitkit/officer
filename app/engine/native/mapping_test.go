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
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"go.openpit.dev/openpit/accountadjustment"
	"go.openpit.dev/openpit/model"
	"go.openpit.dev/openpit/param"
	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/domain"
)

func TestMergeBalanceOutcomes_PreservesOrderedPhaseEffects(t *testing.T) {
	got, err := mergeBalanceOutcomes(
		[]BalanceOutcome{
			{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceDelta: "-100", BalanceResult: "900",
				HeldDelta: "100", HeldResult: "100",
				RealizedPnlResult: "7", AverageEntryPrice: "1",
			}},
			{Asset: "BTC", Outcome: domain.AdjustmentOutcomeAccepted{
				IncomingDelta: "1", IncomingResult: "1",
				RealizedPnlResult: "3",
			}},
		},
		[]BalanceOutcome{
			{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceDelta: "25", BalanceResult: "925",
				HeldDelta: "-100", HeldResult: "0",
				RealizedPnlDelta: "-1", RealizedPnlResult: "6",
			}},
			{Asset: "BTC", Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceDelta: "1", BalanceResult: "1",
				IncomingDelta: "-1", IncomingResult: "0",
				RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
				AverageEntryPrice:     "100",
			}},
		},
	)
	if err != nil {
		t.Fatalf("mergeBalanceOutcomes: %v", err)
	}
	if len(got) != 2 || got[0].Asset != "USD" || got[1].Asset != "BTC" {
		t.Fatalf("merged outcomes = %+v, want one USD then one BTC", got)
	}
	usd := got[0].Outcome
	if usd.BalanceDelta != "-75" || usd.BalanceResult != "925" ||
		usd.HeldDelta != "0" || usd.HeldResult != "0" ||
		usd.RealizedPnlDelta != "-1" || usd.RealizedPnlResult != "6" ||
		usd.AverageEntryPrice != "1" {
		t.Fatalf("merged USD outcome = %+v", usd)
	}
	btc := got[1].Outcome
	if btc.BalanceDelta != "1" || btc.BalanceResult != "1" ||
		btc.IncomingDelta != "0" || btc.IncomingResult != "0" ||
		btc.RealizedPnlResult != "" ||
		btc.RealizedPnlHaltReason != domain.PnlHaltReasonMissingFx ||
		btc.AverageEntryPrice != "100" {
		t.Fatalf("merged BTC outcome = %+v", btc)
	}
}

func TestSpotFundsAccountPnlFromList_NoOutcomeDoesNotWarn(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	pnl, halt, err := spotFundsAccountPnlFromList(
		param.NewAccountIDFromUint64(7), nil,
	)
	if err != nil || pnl != "" || halt != "" {
		t.Fatalf("spotFundsAccountPnlFromList = (%q, %q, %v), want unchanged", pnl, halt, err)
	}
	if logs.Len() != 0 {
		t.Fatalf("normal missing P&L outcome logged: %s", logs.String())
	}
}

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

func TestSpotFundsPnlBoundsAxes_DistributesAndUsesNonNilSlices(t *testing.T) {
	t.Parallel()
	res, err := newIDResolver(
		[]domain.Account{account("acc-1")},
		[]domain.AccountGroup{{Code: "desk-a", EngineGroupID: 7}},
	)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	global, groups, accounts, err := spotFundsPnlBoundsAxes(
		[]domain.LimitSpotFundsPnlBounds{
			{
				Scope:      domain.ScopeGlobal,
				LowerBound: "-1000",
			},
			{
				Scope:        domain.ScopeAccountGroup,
				AccountGroup: "desk-a",
				UpperBound:   "500",
			},
			{
				Scope:      domain.ScopeAccount,
				Account:    "acc-1",
				LowerBound: "-100",
				UpperBound: "100",
			},
		},
		res,
	)
	if err != nil {
		t.Fatalf("spotFundsPnlBoundsAxes: %v", err)
	}
	globalBarrier, globalSet := global.Get()
	if !globalSet || globalBarrier == nil || groups == nil || accounts == nil {
		t.Fatalf("axes must set the global barrier and non-nil account slices")
	}
	if len(groups) != 1 || len(accounts) != 1 {
		t.Fatalf(
			"axis counts wrong: global=%+v groups=%d accounts=%d",
			globalBarrier,
			len(groups),
			len(accounts),
		)
	}
	if _, ok := globalBarrier.LowerBound.Get(); !ok {
		t.Fatalf("global lower bound not mapped: %+v", globalBarrier)
	}
	if _, ok := groups[0].Barrier.UpperBound.Get(); !ok {
		t.Fatalf("account-group upper bound not mapped: %+v", groups[0])
	}
	if _, ok := accounts[0].Barrier.LowerBound.Get(); !ok {
		t.Fatalf("P&L bounds not mapped: %+v %+v %+v", globalBarrier, groups, accounts)
	}
}

func TestBuildEngine_RegistersRiskPolicies(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		Accounts:        []domain.Account{account("acc-1")},
		RateLimits:      []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 100, time.Second)},
		OrderSizeLimits: []domain.LimitOrderSize{orderSize(domain.ScopeBroker, "", "", "10", "")},
	}
	res, err := newIDResolver(snap.Accounts, snap.Groups)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	eng, service, registered, _, err := buildEngine(snap, res)
	if err != nil {
		t.Fatalf("buildEngine: %v", err)
	}
	defer service.Close()
	defer eng.Stop()

	for _, name := range []string{
		nameRateLimit, nameOrderSizeLimit,
	} {
		if _, ok := registered[name]; !ok {
			t.Fatalf("policy %q not registered", name)
		}
	}
}

func TestAccountLaneSetAccountCurrency_InvalidCurrencyIsDomainInvalid(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1")
	accountID, err := res.account("acc-1")
	if err != nil {
		t.Fatalf("resolve account: %v", err)
	}
	lane := accountLane{
		owner:        &openPitEngine{res: res},
		accountAlias: "acc-1",
		accountID:    accountID,
	}
	err = lane.SetAccountCurrency(context.Background(), "acc-1", "  ")
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SetAccountCurrency invalid currency = %v, want ErrInvalid", err)
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

func TestIDResolver_AddsAndRenamesAliases(t *testing.T) {
	t.Parallel()
	res, err := newIDResolver(
		[]domain.Account{{Code: "account-old", EngineAccountID: 7}},
		[]domain.AccountGroup{{Code: "group-old", EngineGroupID: 9}},
	)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	if err := res.addAccountResolverEntry(domain.Account{
		Code: "account-added", EngineAccountID: 8,
	}); err != nil {
		t.Fatalf("add account resolver entry: %v", err)
	}
	if err := res.addGroupResolverEntry(domain.AccountGroup{
		Code: "group-added", EngineGroupID: 10,
	}); err != nil {
		t.Fatalf("add group resolver entry: %v", err)
	}
	if err := res.renameAccountResolverEntry("account-old", domain.Account{
		Code: "account-new", EngineAccountID: 7,
	}); err != nil {
		t.Fatalf("rename account resolver entry: %v", err)
	}
	if err := res.renameGroupResolverEntry("group-old", domain.AccountGroup{
		Code: "group-new", EngineGroupID: 9,
	}); err != nil {
		t.Fatalf("rename group resolver entry: %v", err)
	}

	for code, want := range map[domain.AccountID]uint64{
		"account-added": 8,
		"account-new":   7,
	} {
		id, err := res.account(code)
		if err != nil {
			t.Fatalf("resolve account %q: %v", code, err)
		}
		if got := uint64(id.Handle()); got != want {
			t.Errorf("account %q engine id = %d, want %d", code, got, want)
		}
	}
	for code, want := range map[string]uint32{
		"group-added": 10,
		"group-new":   9,
	} {
		id, err := res.group(code)
		if err != nil {
			t.Fatalf("resolve group %q: %v", code, err)
		}
		if got := uint32(id.Handle()); got != want {
			t.Errorf("group %q engine id = %d, want %d", code, got, want)
		}
	}
	if _, err := res.account("account-old"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("old account alias error = %v, want ErrInvalid", err)
	}
	if _, err := res.group("group-old"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("old group alias error = %v, want ErrInvalid", err)
	}
}

func TestIDResolver_RejectsInvalidMutationsWithoutPartialChange(t *testing.T) {
	t.Parallel()
	res, err := newIDResolver(
		[]domain.Account{
			{Code: "account-a", EngineAccountID: 7},
			{Code: "account-b", EngineAccountID: 8},
		},
		[]domain.AccountGroup{
			{Code: "group-a", EngineGroupID: 9},
			{Code: "group-b", EngineGroupID: 10},
		},
	)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}

	accountMutations := []struct {
		name string
		err  error
	}{
		{
			name: "duplicate alias",
			err: res.addAccountResolverEntry(domain.Account{
				Code: "account-a", EngineAccountID: 11,
			}),
		},
		{
			name: "duplicate engine id",
			err: res.addAccountResolverEntry(domain.Account{
				Code: "account-c", EngineAccountID: 7,
			}),
		},
		{
			name: "unknown old alias",
			err: res.renameAccountResolverEntry("account-missing", domain.Account{
				Code: "account-c", EngineAccountID: 7,
			}),
		},
		{
			name: "mismatched target id",
			err: res.renameAccountResolverEntry("account-a", domain.Account{
				Code: "account-c", EngineAccountID: 12,
			}),
		},
		{
			name: "duplicate target alias",
			err: res.renameAccountResolverEntry("account-a", domain.Account{
				Code: "account-b", EngineAccountID: 7,
			}),
		},
		{
			name: "corrupt target id",
			err: res.renameAccountResolverEntry("account-a", domain.Account{
				Code: "account-c", EngineAccountID: 0,
			}),
		},
	}
	for _, mutation := range accountMutations {
		if mutation.err == nil {
			t.Errorf("%s: want error", mutation.name)
		}
	}
	for code, want := range map[domain.AccountID]uint64{
		"account-a": 7,
		"account-b": 8,
	} {
		id, err := res.account(code)
		if err != nil || uint64(id.Handle()) != want {
			t.Errorf("account %q after failed mutations = (%v, %v), want id %d", code, id, err, want)
		}
	}
	if _, err := res.account("account-c"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("partial account alias published: %v", err)
	}

	groupMutations := []error{
		res.addGroupResolverEntry(domain.AccountGroup{
			Code: "group-a", EngineGroupID: 11,
		}),
		res.addGroupResolverEntry(domain.AccountGroup{
			Code: "group-c", EngineGroupID: 9,
		}),
		res.renameGroupResolverEntry("group-missing", domain.AccountGroup{
			Code: "group-c", EngineGroupID: 9,
		}),
		res.renameGroupResolverEntry("group-a", domain.AccountGroup{
			Code: "group-c", EngineGroupID: 12,
		}),
		res.renameGroupResolverEntry("group-a", domain.AccountGroup{
			Code: "group-b", EngineGroupID: 9,
		}),
		res.renameGroupResolverEntry("group-a", domain.AccountGroup{
			Code: "group-c", EngineGroupID: 0,
		}),
	}
	for i, mutationErr := range groupMutations {
		if mutationErr == nil {
			t.Errorf("group mutation %d: want error", i)
		}
	}
	for code, want := range map[string]uint32{"group-a": 9, "group-b": 10} {
		id, err := res.group(code)
		if err != nil || uint32(id.Handle()) != want {
			t.Errorf("group %q after failed mutations = (%v, %v), want id %d", code, id, err, want)
		}
	}
	if _, err := res.group("group-c"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("partial group alias published: %v", err)
	}
}

func TestIDResolver_RemovesGroupAliasWithStableIDValidation(t *testing.T) {
	t.Parallel()
	res, err := newIDResolver(nil, []domain.AccountGroup{{
		Code: "group-a", EngineGroupID: 9,
	}})
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}

	if err := res.removeGroupResolverEntry(domain.AccountGroup{
		Code: "group-a", EngineGroupID: 10,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("mismatched removal error = %v, want ErrInvalid", err)
	}
	if id, err := res.group("group-a"); err != nil || uint32(id.Handle()) != 9 {
		t.Fatalf("mismatched removal changed alias = (%v, %v)", id, err)
	}

	if err := res.removeGroupResolverEntry(domain.AccountGroup{
		Code: "group-a", EngineGroupID: 9,
	}); err != nil {
		t.Fatalf("remove group resolver entry: %v", err)
	}
	if _, err := res.group("group-a"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("removed group alias error = %v, want ErrInvalid", err)
	}
	if err := res.removeGroupResolverEntry(domain.AccountGroup{
		Code: "group-a", EngineGroupID: 9,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown removal error = %v, want ErrInvalid", err)
	}
}

func TestIDResolver_ConcurrentLookupAndMutation(t *testing.T) {
	res, err := newIDResolver(
		[]domain.Account{
			{Code: "stable-account", EngineAccountID: 7},
			{Code: "moving-account-a", EngineAccountID: 8},
		},
		[]domain.AccountGroup{
			{Code: "stable-group", EngineGroupID: 9},
			{Code: "moving-group-a", EngineGroupID: 10},
		},
	)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}

	const iterations = 500
	errCh := make(chan error, 9)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func(resolver idResolver) {
			defer wg.Done()
			for range iterations {
				if id, err := resolver.account("stable-account"); err != nil ||
					uint64(id.Handle()) != 7 {
					errCh <- fmt.Errorf("stable account lookup = (%v, %v)", id, err)
					return
				}
				if id, err := resolver.group("stable-group"); err != nil ||
					uint32(id.Handle()) != 9 {
					errCh <- fmt.Errorf("stable group lookup = (%v, %v)", id, err)
					return
				}
			}
		}(res)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		accountOld, accountNew := domain.AccountID("moving-account-a"), domain.AccountID("moving-account-b")
		groupOld, groupNew := "moving-group-a", "moving-group-b"
		for range iterations {
			if err := res.renameAccountResolverEntry(accountOld, domain.Account{
				Code: accountNew, EngineAccountID: 8,
			}); err != nil {
				errCh <- err
				return
			}
			accountOld, accountNew = accountNew, accountOld
			if err := res.renameGroupResolverEntry(groupOld, domain.AccountGroup{
				Code: groupNew, EngineGroupID: 10,
			}); err != nil {
				errCh <- err
				return
			}
			groupOld, groupNew = groupNew, groupOld
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestApplyCurrenciesCallsAccountAndGroupTiers(t *testing.T) {
	t.Parallel()
	groupID, err := param.NewAccountGroupIDFromUint32(7)
	if err != nil {
		t.Fatalf("group id: %v", err)
	}
	snap := Snapshot{
		Accounts: []domain.Account{
			{
				Code:            "acc-1",
				EngineAccountID: 11,
				Currency:        "GBP",
			},
			{
				Code:            "acc-2",
				EngineAccountID: 12,
			},
		},
		Groups: []domain.AccountGroup{
			{Code: "", Currency: "USD"},
			{Code: "desk-a", EngineGroupID: 7, Currency: "EUR"},
			{Code: "desk-b", EngineGroupID: 8},
		},
	}
	res, err := newIDResolver(snap.Accounts, snap.Groups)
	if err != nil {
		t.Fatalf("newIDResolver: %v", err)
	}
	handle := &fakeCurrencyAccounts{}
	if err := applyCurrencies(handle, snap.Accounts, snap.Groups, res); err != nil {
		t.Fatalf("applyCurrencies: %v", err)
	}
	accountID := param.NewAccountIDFromUint64(11)
	wantGroups := []currencyCall{
		{id: param.DefaultAccountGroup.String(), currency: "USD"},
		{id: groupID.String(), currency: "EUR"},
	}
	if !slices.Equal(handle.groupCalls, wantGroups) {
		t.Fatalf("group currency calls = %+v, want %+v", handle.groupCalls, wantGroups)
	}
	wantAccounts := []currencyCall{{id: accountID.String(), currency: "GBP"}}
	if !slices.Equal(handle.accountCalls, wantAccounts) {
		t.Fatalf(
			"account currency calls = %+v, want %+v",
			handle.accountCalls,
			wantAccounts,
		)
	}
}

type currencyCall struct {
	id       string
	currency string
}

type fakeCurrencyAccounts struct {
	groupCalls   []currencyCall
	accountCalls []currencyCall
}

func (h *fakeCurrencyAccounts) SetGroupCurrency(
	id param.AccountGroupID,
	currency param.Asset,
) error {
	h.groupCalls = append(h.groupCalls, currencyCall{
		id:       id.String(),
		currency: currency.String(),
	})
	return nil
}

func (h *fakeCurrencyAccounts) SetCurrency(
	id param.AccountID,
	currency param.Asset,
) error {
	h.accountCalls = append(h.accountCalls, currencyCall{
		id:       id.String(),
		currency: currency.String(),
	})
	return nil
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
	if _, err := eng.ConfigurePolicy(ctx, domain.PolicyRateLimit, newLimits); err != nil {
		t.Fatalf("ConfigurePolicy retune: %v", err)
	}

	// Both accounts are in the snapshot, so the resolver covers them by their
	// stored engine ids - no string hashing.
	if err := unblockAccountOnLane(ctx, eng, "acc-1"); err != nil {
		t.Fatalf("UnblockAccount: %v", err)
	}
	if err := blockAccountOnLane(ctx, eng, "acc-2", "manual"); err != nil {
		t.Fatalf("BlockAccount: %v", err)
	}

	// An account the snapshot does not cover has no stored engine id, so the
	// resolver rejects it as invalid rather than hashing its code.
	if err := blockAccountOnLane(ctx, eng, "ghost", "manual"); !errors.Is(err, domain.ErrInvalid) {
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
	if _, err := eng.ConfigurePolicy(context.Background(),
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
	if _, err := eng.ConfigurePolicy(context.Background(),
		domain.PolicyOrderSizeLimit, replaced); err != nil {
		t.Fatalf("ConfigurePolicy replace: %v", err)
	}
}

// TestConfigurePolicy_OrderSizeDropsBrokerOnline checks the explicit optional
// update clears a broker axis while an asset axis keeps the policy non-empty.
func TestConfigurePolicy_OrderSizeDropsBrokerOnline(t *testing.T) {
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
	adapter := eng.(*openPitEngine)
	engineBefore := adapter.eng
	sinkBefore := adapter.MarketDataSink()

	dropped := LimitSet{OrderSizeLimits: []domain.LimitOrderSize{orderSize(domain.ScopeAsset, "", "USD", "5", "")}}
	if _, err := eng.ConfigurePolicy(
		context.Background(), domain.PolicyOrderSizeLimit, dropped,
	); err != nil {
		t.Fatalf("ConfigurePolicy drop order-size broker: %v", err)
	}
	if adapter.eng != engineBefore || adapter.MarketDataSink() != sinkBefore {
		t.Fatal("dropping order-size broker replaced engine or market-data sink")
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
	if _, err := eng.ConfigurePolicy(context.Background(),
		domain.PolicyOrderSizeLimit, replaced); err != nil {
		t.Fatalf("ConfigurePolicy replace without broker: %v", err)
	}
}

// TestConfigurePolicy_UnregisteredPolicyStub checks a policy absent from the
// cold snapshot still needs a rebuild because the SDK rejects empty policies.
func TestConfigurePolicy_UnregisteredPolicyStub(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		RateLimits: []domain.LimitRate{
			rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
		},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	add := LimitSet{OrderSizeLimits: []domain.LimitOrderSize{
		orderSize(domain.ScopeBroker, "", "", "10", ""),
	}}
	_, err = eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit, add)
	if !errors.Is(err, domain.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented for unregistered policy, got %v", err)
	}
}

// TestConfigurePolicy_RemoveLastBarrierStub checks an empty barrier set returns
// ErrNotImplemented before the SDK rejects the resulting empty policy.
func TestConfigurePolicy_RemoveLastBarrierStub(t *testing.T) {
	t.Parallel()
	snap := Snapshot{
		RateLimits: []domain.LimitRate{
			rateLimit(domain.ScopeAsset, "", "USD", 100, time.Second),
		},
	}
	eng, err := BuildOpenPitEngine("", snap)
	if err != nil {
		t.Fatalf("BuildOpenPitEngine: %v", err)
	}
	defer eng.Stop()

	_, err = eng.ConfigurePolicy(context.Background(), domain.PolicyRateLimit, LimitSet{})
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
	if _, err := eng.ConfigurePolicy(ctx, domain.PolicyRateLimit, added); err != nil {
		t.Fatalf("ConfigurePolicy add: %v", err)
	}

	removed := LimitSet{RateLimits: []domain.LimitRate{rateLimit(domain.ScopeBroker, "", "", 100, time.Second)}}
	if _, err := eng.ConfigurePolicy(ctx, domain.PolicyRateLimit, removed); err != nil {
		t.Fatalf("ConfigurePolicy remove: %v", err)
	}
}

// TestConfigurePolicy_RateLimitDropsBrokerOnline checks the explicit optional
// update clears a broker axis while an asset axis keeps the policy non-empty.
func TestConfigurePolicy_RateLimitDropsBrokerOnline(t *testing.T) {
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
	adapter := eng.(*openPitEngine)
	engineBefore := adapter.eng
	sinkBefore := adapter.MarketDataSink()

	dropped := LimitSet{RateLimits: []domain.LimitRate{rateLimit(domain.ScopeAsset, "", "USD", 50, time.Second)}}
	if _, err := eng.ConfigurePolicy(
		context.Background(), domain.PolicyRateLimit, dropped,
	); err != nil {
		t.Fatalf("ConfigurePolicy drop rate-limit broker: %v", err)
	}
	if adapter.eng != engineBefore || adapter.MarketDataSink() != sinkBefore {
		t.Fatal("dropping rate-limit broker replaced engine or market-data sink")
	}
}

func TestPolicyConfigurationBlocksFromCarriesAccountAndFallbackPolicy(t *testing.T) {
	t.Parallel()
	block := reject.NewAccountBlock(
		reject.CodePnlKillSwitchTriggered,
		"",
		"P&L bound breached",
		"lower=-100",
	)
	got := policyConfigurationBlocksFrom(
		[]reject.AccountBlock{block},
		"acc-1",
		domain.PolicySpotFundsPnlBoundsKillSwitch,
	)
	if len(got) != 1 {
		t.Fatalf("blocks = %+v, want one", got)
	}
	if got[0].Account != "acc-1" ||
		got[0].Policy != domain.PolicySpotFundsPnlBoundsKillSwitch ||
		got[0].Code != rejectCodeName(block.Code) ||
		got[0].Reason != block.Reason ||
		got[0].Details != block.Details {
		t.Fatalf("mapped block = %+v, want account and binding block fields", got[0])
	}
}

// TestSpotFundsPnlBoundsAxes_UnsupportedScope checks a scope the SpotFunds P&L
// bounds axes cannot express is rejected rather than silently dropped.
func TestSpotFundsPnlBoundsAxes_UnsupportedScope(t *testing.T) {
	t.Parallel()
	_, _, _, err := spotFundsPnlBoundsAxes(
		[]domain.LimitSpotFundsPnlBounds{
			{
				Scope:      domain.ScopeAsset,
				LowerBound: "-100",
			},
		},
		testResolver(),
	)
	if err == nil {
		t.Fatal("want error for unsupported scope, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported scope") {
		t.Fatalf("error = %v, want unsupported scope", err)
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

func blockAccountOnLane(
	ctx context.Context, eng Engine, account domain.AccountID, reason string,
) error {
	return eng.RunAccountSynchronized(ctx, account, func(lane AccountLane) error {
		return lane.BlockAccount(ctx, account, reason)
	})
}

func unblockAccountOnLane(ctx context.Context, eng Engine, account domain.AccountID) error {
	return eng.RunAccountSynchronized(ctx, account, func(lane AccountLane) error {
		return lane.UnblockAccount(ctx, account)
	})
}

func checkOrderOnLane(
	ctx context.Context, eng Engine, probe domain.OrderProbe,
) (domain.CheckResult, error) {
	var out domain.CheckResult
	err := eng.RunAccountSynchronized(ctx, probe.Account, func(lane AccountLane) error {
		var err error
		out, err = lane.CheckOrder(ctx, probe)
		return err
	})
	return out, err
}

func submitOrderOnLane(
	ctx context.Context, eng Engine, order domain.Order,
) (OrderResult, error) {
	var out OrderResult
	err := eng.RunAccountSynchronized(ctx, order.Account, func(lane AccountLane) error {
		var err error
		out, err = lane.SubmitOrder(ctx, order)
		return err
	})
	return out, err
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

	out, err := checkOrderOnLane(context.Background(), eng, checkProbe("acc-1", domain.OrderSideBuy, "1", "100"))
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

	out, err := checkOrderOnLane(context.Background(), eng, checkProbe("acc-1", domain.OrderSideBuy, "1", "100"))
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

	out, err := checkOrderOnLane(context.Background(), eng, checkProbe("acc-1", domain.OrderSideBuy, "1", "100"))
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

	out, err := checkOrderOnLane(context.Background(), eng, checkProbe("acc-1", domain.OrderSideBuy, "1", "100"))
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
		out, err := checkOrderOnLane(ctx, eng, checkProbe("acc-1", domain.OrderSideBuy, "1", "100"))
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

	first, err := submitOrderOnLane(ctx, eng, order)
	if err != nil {
		t.Fatalf("first SubmitOrder: %v", err)
	}
	if !first.Accepted {
		t.Fatalf("first submit must pass after dry-runs (budget intact), got %+v", first.Rejects)
	}

	second, err := submitOrderOnLane(ctx, eng, order)
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
// domain.ErrInvalid for a bad fill price, a bad fill quantity, an empty or bad
// leaves quantity, and a bad lock price (all caller input).
func TestExecutionReportFrom_InvalidInputs(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1")
	base := domain.ExecutionReportInput{
		BaseAsset: "AAPL", QuoteAsset: "USD", Account: "acc-1", Side: domain.OrderSideBuy,
		FillQuantity: "1", FillPrice: "100", LeavesQuantity: "0",
		OrderStatus: domain.OrderStatusFilled,
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

	emptyLeaves := base
	emptyLeaves.LeavesQuantity = ""
	if _, err := executionReportFrom(emptyLeaves, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty leaves quantity, got %v", err)
	}

	badLeaves := base
	badLeaves.LeavesQuantity = "not-a-number"
	if _, err := executionReportFrom(badLeaves, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad leaves quantity, got %v", err)
	}

	badLock := base
	badLock.LockPrice = "not-a-number"
	if _, err := executionReportFrom(badLock, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad lock price, got %v", err)
	}

	badCommissionAmount := base
	badCommissionAmount.Commission = &domain.Commission{Amount: "not-a-number", Currency: "USD"}
	if _, err := executionReportFrom(badCommissionAmount, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad commission amount, got %v", err)
	}

	badCommissionCurrency := base
	badCommissionCurrency.Commission = &domain.Commission{Amount: "-0.12", Currency: ""}
	if _, err := executionReportFrom(badCommissionCurrency, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad commission currency, got %v", err)
	}

	badOpaqueLock := base
	badOpaqueLock.Lock = []byte{0x01, 0x02, 0x03}
	if _, err := executionReportFrom(badOpaqueLock, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad opaque lock, got %v", err)
	}

	oneSidedFill := base
	oneSidedFill.FillPrice = ""
	if _, err := executionReportFrom(oneSidedFill, res); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for one-sided fill, got %v", err)
	}

	noTradeCancel := base
	noTradeCancel.FillQuantity = ""
	noTradeCancel.FillPrice = ""
	noTradeCancel.LeavesQuantity = "1"
	noTradeCancel.OrderStatus = domain.OrderStatusCancelled
	noTradeCancel.LockPrice = "100"
	if _, err := executionReportFrom(noTradeCancel, res); err != nil {
		t.Fatalf("no-trade cancel must map: %v", err)
	}
}

func TestExecutionReportFrom_CommissionUsesStructuredFee(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1")
	report, err := executionReportFrom(domain.ExecutionReportInput{
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		FillQuantity:   "1",
		FillPrice:      "100",
		LeavesQuantity: "0",
		Commission: &domain.Commission{
			Amount:   "-0.50",
			Currency: "USD",
		},
		Account:     "acc-1",
		Side:        domain.OrderSideBuy,
		OrderStatus: domain.OrderStatusFilled,
	}, res)
	if err != nil {
		t.Fatalf("executionReportFrom: %v", err)
	}
	if _, ok := report.FinancialImpact().Get(); ok {
		t.Fatal("FinancialImpact must remain unset")
	}
	fill, ok := report.Fill().Get()
	if !ok {
		t.Fatal("Fill unset")
	}
	commission, ok := fill.Fee().Get()
	if !ok {
		t.Fatal("Fill.Fee unset")
	}
	if commission.Amount.String() != "0.50" {
		t.Fatalf("commission amount = %q, want SDK fee 0.50", commission.Amount.String())
	}
	if commission.Currency.String() != "USD" {
		t.Fatalf("commission currency = %q, want USD", commission.Currency.String())
	}
}

func TestExecutionReportFrom_NoTradeCommissionUsesStructuredFee(t *testing.T) {
	t.Parallel()
	res := testResolver("acc-1")
	report, err := executionReportFrom(domain.ExecutionReportInput{
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		LeavesQuantity: "1",
		Commission: &domain.Commission{
			Amount:   "-0.50",
			Currency: "USD",
		},
		Account:     "acc-1",
		Side:        domain.OrderSideBuy,
		OrderStatus: domain.OrderStatusCancelled,
	}, res)
	if err != nil {
		t.Fatalf("executionReportFrom: %v", err)
	}
	fill, ok := report.Fill().Get()
	if !ok {
		t.Fatal("Fill unset")
	}
	if _, ok := fill.LastTrade().Get(); ok {
		t.Fatal("LastTrade set for a terminal report without a fill")
	}
	commission, ok := fill.Fee().Get()
	if !ok {
		t.Fatal("Fill.Fee unset")
	}
	if commission.Amount.String() != "0.50" {
		t.Fatalf("commission amount = %q, want SDK fee 0.50", commission.Amount.String())
	}
	if commission.Currency.String() != "USD" {
		t.Fatalf("commission currency = %q, want USD", commission.Currency.String())
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
// mapper wraps domain.ErrInvalid for a bad asset, a bad average-entry-price, a
// bad realized PnL, and a bad bound (all caller input).
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

	badRealizedPnl := domain.AdjustmentRequest{
		Asset: "USD", Balance: delta("1"), RealizedPnl: "not-a-number",
	}
	if _, err := accountAdjustmentFromRequest(badRealizedPnl); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad realized pnl, got %v", err)
	}

	badBound := domain.AdjustmentRequest{
		Asset: "USD", Balance: delta("1"),
		BalanceBounds: &domain.AdjustmentBounds{Lower: "not-a-number"},
	}
	if _, err := accountAdjustmentFromRequest(badBound); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad bound, got %v", err)
	}
}

func TestRejectCodeName_ArithmeticOverflow(t *testing.T) {
	t.Parallel()
	if got := rejectCodeName(reject.CodeArithmeticOverflow); got != "arithmetic_overflow" {
		t.Fatalf("reject code name = %q, want arithmetic_overflow", got)
	}
}

func TestExecutionBalanceSettlementsFrom_KeepsPositionPnl(t *testing.T) {
	t.Parallel()
	settlements := executionBalanceSettlementsFrom([]BalanceOutcome{{
		Asset: "USD",
		Outcome: domain.AdjustmentOutcomeAccepted{
			BalanceResult:     "100",
			RealizedPnlDelta:  "2",
			RealizedPnlResult: "7",
		},
	}})
	if len(settlements) != 1 {
		t.Fatalf("settlements = %+v, want one", settlements)
	}
	if settlements[0].Outcome.BalanceResult != "100" ||
		settlements[0].Outcome.RealizedPnlDelta != "2" ||
		settlements[0].Outcome.RealizedPnlResult != "7" {
		t.Fatalf("settlement outcome = %+v", settlements[0].Outcome)
	}
}

func TestAccountAdjustmentFromRequest_ForwardsRealizedPnl(t *testing.T) {
	t.Parallel()
	adjustment, err := accountAdjustmentFromRequest(domain.AdjustmentRequest{
		Asset: "BTC", RealizedPnl: "-12.50",
		Balance: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeAbsolute, Value: "1",
		},
	})
	if err != nil {
		t.Fatalf("accountAdjustmentFromRequest: %v", err)
	}
	operation, ok := adjustment.BalanceOperation().Get()
	if !ok {
		t.Fatal("balance operation is unset")
	}
	pnl, ok := operation.RealizedPnl().Get()
	value, authoritative := pnl.Value()
	if !ok || !authoritative || value.String() != "-12.50" {
		t.Fatalf("realized pnl = (%v, %v) set=%v, want authoritative -12.50", value, authoritative, ok)
	}
}

func TestBalanceSeedAdjustment_RestoresHaltedRealizedPnl(t *testing.T) {
	t.Parallel()
	adjustment, err := balanceSeedAdjustment(domain.Balance{
		Asset:                 "BTC",
		Available:             "1",
		RealizedPnl:           "not-a-number",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingFx,
	})
	if err != nil {
		t.Fatalf("balanceSeedAdjustment: %v", err)
	}
	operation, ok := adjustment.BalanceOperation().Get()
	if !ok {
		t.Fatal("balance operation is unset")
	}
	state, ok := operation.RealizedPnl().Get()
	if !ok {
		t.Fatal("realized pnl is unset, want restored halt")
	}
	reason, halted := state.HaltReason()
	if !halted || reason != model.PnlHaltReasonMissingFx {
		t.Fatalf("realized pnl state = (%v, %v), want missing-fx halt", reason, halted)
	}
}

func TestPnlHaltReasonFromSDKMapsEveryReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   model.PnlHaltReason
		want domain.PnlHaltReason
	}{
		{
			name: "missing fx",
			in:   model.PnlHaltReasonMissingFx,
			want: domain.PnlHaltReasonMissingFx,
		},
		{
			name: "missing account currency",
			in:   model.PnlHaltReasonMissingAccountCurrency,
			want: domain.PnlHaltReasonMissingAccountCurrency,
		},
		{
			name: "missing initial pnl",
			in:   model.PnlHaltReasonMissingInitialPnl,
			want: domain.PnlHaltReasonMissingInitialPnl,
		},
		{
			name: "missing cost basis",
			in:   model.PnlHaltReasonMissingCostBasis,
			want: domain.PnlHaltReasonMissingCostBasis,
		},
		{
			name: "arithmetic overflow",
			in:   model.PnlHaltReasonArithmeticOverflow,
			want: domain.PnlHaltReasonArithmeticOverflow,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := pnlHaltReasonFromSDK(test.in)
			if err != nil {
				t.Fatalf("pnlHaltReasonFromSDK(%v): %v", test.in, err)
			}
			if got != test.want {
				t.Fatalf("pnlHaltReasonFromSDK(%v) = %q, want %q", test.in, got, test.want)
			}
			if err := domain.ValidatePnlHaltReason(got); err != nil {
				t.Fatalf("ValidatePnlHaltReason(%q): %v", got, err)
			}
			// A mapped reason must round-trip: the engine rebuild at start
			// replays persisted halts, so a reason this seam accepts but
			// cannot return to the engine would block the next boot.
			if _, err := pnlHaltReasonToSDK(got); err != nil {
				t.Fatalf("pnlHaltReasonToSDK(%q): %v", got, err)
			}
		})
	}
}

// A halt reason the engine gains but this seam does not know must fail at the
// seam rather than reach storage as a value pnlHaltReasonToSDK would reject.
func TestPnlHaltReasonFromSDKRejectsUnrecognizedReason(t *testing.T) {
	t.Parallel()
	got, err := pnlHaltReasonFromSDK(model.PnlHaltReason(255))
	if err == nil {
		t.Fatalf("pnlHaltReasonFromSDK(255) = %q, want error", got)
	}
	if got != "" {
		t.Fatalf("pnlHaltReasonFromSDK(255) = %q, want empty reason", got)
	}
}

func TestSpotFundsAccountPnlFromListSelectsAuthoritativeOutcome(t *testing.T) {
	t.Parallel()
	accountID := param.NewAccountIDFromUint64(7)
	otherAccountID := param.NewAccountIDFromUint64(8)
	computed := func(
		id param.AccountID,
		deltaValue string,
		absolute string,
	) accountadjustment.AccountPnlOutcome {
		t.Helper()
		delta, err := param.NewPnlFromString(deltaValue)
		if err != nil {
			t.Fatalf("NewPnlFromString(delta): %v", err)
		}
		value, err := param.NewPnlFromString(absolute)
		if err != nil {
			t.Fatalf("NewPnlFromString(absolute): %v", err)
		}
		return accountadjustment.NewAccountPnlOutcome(
			1,
			id,
			accountadjustment.PnlOutcomeAmount{Delta: delta, Absolute: value},
		)
	}
	halted := func(
		id param.AccountID,
		reason model.PnlHaltReason,
	) accountadjustment.AccountPnlOutcome {
		t.Helper()
		outcome, err := accountadjustment.NewAccountPnlHaltedOutcome(
			1,
			id,
			reason,
		)
		if err != nil {
			t.Fatalf("NewAccountPnlHaltedOutcome: %v", err)
		}
		return outcome
	}

	tests := []struct {
		name       string
		outcomes   []accountadjustment.AccountPnlOutcome
		wantPnl    string
		wantReason domain.PnlHaltReason
	}{
		{
			name: "computed",
			outcomes: []accountadjustment.AccountPnlOutcome{
				computed(accountID, "1.250", "42.500"),
			},
			wantPnl: "42.500",
		},
		{
			name: "computed zero delta",
			outcomes: []accountadjustment.AccountPnlOutcome{
				computed(accountID, "0", "7.250"),
			},
		},
		{
			name: "halted",
			outcomes: []accountadjustment.AccountPnlOutcome{
				halted(accountID, model.PnlHaltReasonMissingCostBasis),
			},
			wantReason: domain.PnlHaltReasonMissingCostBasis,
		},
		{
			name: "mismatched account",
			outcomes: []accountadjustment.AccountPnlOutcome{
				computed(otherAccountID, "1.250", "9"),
			},
		},
		{
			name: "first duplicate wins",
			outcomes: []accountadjustment.AccountPnlOutcome{
				computed(accountID, "1.250", "17.250"),
				halted(accountID, model.PnlHaltReasonArithmeticOverflow),
			},
			wantPnl: "17.250",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			gotPnl, gotReason, err := spotFundsAccountPnlFromList(accountID, test.outcomes)
			if err != nil {
				t.Fatalf("spotFundsAccountPnlFromList: %v", err)
			}
			if gotPnl != test.wantPnl || gotReason != test.wantReason {
				t.Fatalf(
					"spotFundsAccountPnlFromList = (%q, %q), want (%q, %q)",
					gotPnl,
					gotReason,
					test.wantPnl,
					test.wantReason,
				)
			}
		})
	}
}

func TestExecutionReportPersistenceFromCarriesAccountPnlOutcome(t *testing.T) {
	t.Parallel()
	persistence := executionReportPersistenceFrom(
		domain.ExecutionReportInput{},
		nil,
		nil,
		"12.340",
		domain.PnlHaltReasonArithmeticOverflow,
	)
	if persistence.AccountPnl != "12.340" ||
		persistence.AccountPnlHaltReason != domain.PnlHaltReasonArithmeticOverflow {
		t.Fatalf("persistence = %+v, want exact account pnl outcome", persistence)
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

// TestExecutionReportPersistenceFrom_StatusOnlyNoAccountWrites proves the real mapper
// folds no account-side persistence into a status-only report: with empty engine
// outcomes and blocks, persistence.Balances is nil and persistence.Blocks is empty, so the
// node writes zero balance rows and zero account-block rows. The venue-owned
// bookkeeping (order status and its status-change event) is still carried.
func TestExecutionReportPersistenceFrom_StatusOnlyNoAccountWrites(t *testing.T) {
	t.Parallel()
	in := domain.ExecutionReportInput{
		Account:        domain.AccountID(testAccount),
		Order:          testOrderXID(0x11),
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		Side:           domain.OrderSideBuy,
		LeavesQuantity: "2",
		OrderStatus:    domain.OrderStatusCancelled,
	}
	persistence := executionReportPersistenceFrom(in, nil, nil, "", "")

	if persistence.Balances != nil {
		t.Fatalf("persistence.Balances = %+v, want nil for a status-only report", persistence.Balances)
	}
	if len(persistence.Blocks) != 0 {
		t.Fatalf("persistence.Blocks = %+v, want empty for a status-only report", persistence.Blocks)
	}
	if persistence.Trade != nil {
		t.Fatalf("persistence.Trade = %+v, want nil for a status-only report", persistence.Trade)
	}
	if persistence.OrderStatus != domain.OrderStatusCancelled {
		t.Fatalf("persistence.OrderStatus = %q, want cancelled", persistence.OrderStatus)
	}
	if persistence.Leaves != "0" {
		t.Fatalf("persistence.Leaves = %q, want zero after terminal release", persistence.Leaves)
	}
	if len(persistence.Events) == 0 {
		t.Fatal("persistence.Events is empty, want the status-change event")
	}
	if persistence.Events[0].Payload.LeavesQuantity != "2" {
		t.Fatalf(
			"event leaves = %q, want original terminal release quantity",
			persistence.Events[0].Payload.LeavesQuantity,
		)
	}
}

func TestExecutionReportPersistenceFrom_NoTradeCarriesCommission(t *testing.T) {
	t.Parallel()
	in := domain.ExecutionReportInput{
		Account:        domain.AccountID(testAccount),
		Order:          testOrderXID(0x13),
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		Side:           domain.OrderSideBuy,
		LeavesQuantity: "2",
		OrderStatus:    domain.OrderStatusCancelled,
		Commission:     &domain.Commission{Amount: "-0.30", Currency: "USDT"},
	}
	persistence := executionReportPersistenceFrom(in, nil, nil, "", "")

	if persistence.Trade != nil {
		t.Fatalf("persistence.Trade = %+v, want nil", persistence.Trade)
	}
	if persistence.Commission == nil ||
		persistence.Commission.Amount != "-0.30" ||
		persistence.Commission.Currency != "USDT" {
		t.Fatalf("report commission = %+v, want -0.30/USDT", persistence.Commission)
	}
	if len(persistence.Events) != 1 ||
		persistence.Events[0].Payload.Commission == nil {
		t.Fatalf("events lost report commission: %+v", persistence.Events)
	}
}

// TestExecutionReportPersistenceFrom_FillCarriesCommission proves the real mapper
// copies a fill's structured commission onto both the fill event payload (which
// the attestation is built from) and the persisted trade (which the trades
// listing is built from), so the two agree.
func TestExecutionReportPersistenceFrom_FillCarriesCommission(t *testing.T) {
	t.Parallel()
	in := domain.ExecutionReportInput{
		Account:        domain.AccountID(testAccount),
		Order:          testOrderXID(0x12),
		BaseAsset:      testBase,
		QuoteAsset:     testQuote,
		Side:           domain.OrderSideBuy,
		FillQuantity:   "3.5",
		FillPrice:      "150.20",
		LeavesQuantity: "6.5",
		OrderStatus:    domain.OrderStatusPartiallyFilled,
		Commission:     &domain.Commission{Amount: "-0.30", Currency: "USDT"},
	}
	persistence := executionReportPersistenceFrom(in, nil, nil, "", "")

	if persistence.Trade == nil {
		t.Fatal("persistence.Trade is nil, want a fill trade")
	}
	if persistence.Trade.Commission == nil ||
		persistence.Trade.Commission.Amount != "-0.30" ||
		persistence.Trade.Commission.Currency != "USDT" {
		t.Fatalf("trade commission = %+v, want -0.30/USDT",
			persistence.Trade.Commission)
	}
	var fill *domain.OrderEvent
	for i := range persistence.Events {
		if persistence.Events[i].Type == domain.OrderEventFill {
			fill = &persistence.Events[i]
		}
	}
	if fill == nil {
		t.Fatalf("no fill event recorded; events=%+v", persistence.Events)
	}
	if fill.Payload.Commission == nil ||
		fill.Payload.Commission.Amount != "-0.30" ||
		fill.Payload.Commission.Currency != "USDT" {
		t.Fatalf("fill event commission = %+v, want -0.30/USDT",
			fill.Payload.Commission)
	}
}
