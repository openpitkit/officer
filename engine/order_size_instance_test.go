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

package engine

import (
	"context"
	"errors"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"go.openpit.dev/openpit/reject"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/store/sqlite"
)

func orderSizeInstanceSnapshot(modes ...domain.RuleMode) fwengine.Snapshot {
	snap := fwengine.Snapshot{
		Accounts: []domain.Account{account("acc-1")},
		Assets:   testAssets(),
		Balances: []domain.Balance{fundedBalance("acc-1", "USD", "1000000")},
	}
	for _, mode := range modes {
		cap := "10"
		if mode == domain.RuleModeReviewWithoutHold {
			cap = "5"
		}
		limit := orderSize(domain.ScopeAccountUnderlyingAsset, "acc-1", "AAPL", cap, "")
		limit.Mode = mode
		snap.OrderSizeLimits = append(snap.OrderSizeLimits, limit)
	}
	return snap
}

func buildOrderSizeInstanceEngine(t *testing.T, snap fwengine.Snapshot) *openPitEngine {
	t.Helper()
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatal(err)
	}
	return eng.(*openPitEngine)
}

func assertOrderSizeInstanceVerdict(
	t *testing.T, eng fwengine.Engine, qty string, want domain.Verdict,
) {
	t.Helper()
	order := domain.Order{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
		AmountValue: qty, Price: "1",
	}
	out, err := materializeOrderResult(eng, order)
	if err != nil || out.Verdict != want {
		t.Fatalf("quantity %s result = %+v, %v; want %s", qty, out, err, want)
	}
	if want == domain.VerdictReview {
		if len(out.Reviews) != 1 || out.Reviews[0].Mode != domain.RuleModeReviewWithoutHold ||
			out.Reviews[0].Policy != nameOrderSizeLimit {
			t.Fatalf("review marks = %+v, want one %s review_without_hold mark", out.Reviews, nameOrderSizeLimit)
		}
	}
	if want == domain.VerdictAccept && len(out.Reviews) != 0 {
		t.Fatalf("accepted order carries review marks: %+v", out.Reviews)
	}
	if want == domain.VerdictReject && !hasRejectCode(out.Rejects, "order_qty_exceeds_limit") {
		t.Fatalf("rejects = %+v, want order_qty_exceeds_limit", out.Rejects)
	}
}

func TestOrderSizeInstances_RejectOnly(t *testing.T) {
	eng := buildOrderSizeInstanceEngine(t, orderSizeInstanceSnapshot(domain.RuleModeReject))
	want := map[string]struct{}{nameSpotFunds: {}, nameOrderSizeLimit: {}}
	if !maps.Equal(eng.registered, want) {
		t.Fatalf("registered = %v, want %v", eng.registered, want)
	}
	assertOrderSizeInstanceVerdict(t, eng, "10", domain.VerdictAccept)
	assertOrderSizeInstanceVerdict(t, eng, "11", domain.VerdictReject)
}

func TestOrderSizeInstances_BothModes(t *testing.T) {
	eng := buildOrderSizeInstanceEngine(t, orderSizeInstanceSnapshot(
		domain.RuleModeReviewWithoutHold, domain.RuleModeReject,
	))
	want := map[string]struct{}{
		nameSpotFunds: {}, nameOrderSizeLimit: {},
		nameOrderSizeLimit + ".review_without_hold": {},
	}
	if !maps.Equal(eng.registered, want) {
		t.Fatalf("registered = %v, want %v", eng.registered, want)
	}
	assertOrderSizeInstanceVerdict(t, eng, "5", domain.VerdictAccept)
	assertOrderSizeInstanceVerdict(t, eng, "6", domain.VerdictReview)
	assertOrderSizeInstanceVerdict(t, eng, "11", domain.VerdictReject)
}

func TestOrderSizeInstances_ModeMapping(t *testing.T) {
	for _, tc := range []struct {
		mode domain.RuleMode
		want reject.RuleMode
	}{
		{domain.RuleModeReject, reject.RuleModeReject},
		{domain.RuleModeReviewWithoutHold, reject.RuleModeReviewWithoutHold},
		{domain.RuleModeReviewWithHold, reject.RuleModeReviewWithHold},
	} {
		got, err := orderSizeInstanceMode(tc.mode)
		if err != nil || got != tc.want {
			t.Fatalf("mode %q = %v, %v; want %v", tc.mode, got, err, tc.want)
		}
		instances, err := splitOrderSizeLimits(orderSizeInstanceSnapshot(tc.mode).OrderSizeLimits)
		if err != nil || len(instances) != 1 || instances[0].sdkMode != tc.want {
			t.Fatalf("mode %q instances = %+v, %v; want SDK mode %v", tc.mode, instances, err, tc.want)
		}
	}
	for _, mode := range []domain.RuleMode{"", "unknown"} {
		if _, err := orderSizeInstanceMode(mode); err == nil || !strings.Contains(err.Error(), string(mode)) {
			t.Fatalf("unsupported mode %q error = %v", mode, err)
		}
		limit := orderSize(domain.ScopeBroker, "", "", "1", "")
		limit.Mode = mode
		if _, err := splitOrderSizeLimits([]domain.LimitOrderSize{limit}); err == nil {
			t.Fatalf("split accepted mode %q", mode)
		}
	}
	for _, mode := range []domain.RuleMode{domain.RuleModeReject, domain.RuleModeReviewWithoutHold} {
		snap := orderSizeInstanceSnapshot(mode)
		snap.OrderSizeLimits = append(snap.OrderSizeLimits, snap.OrderSizeLimits[0])
		if eng, err := newTestOpenPitEngineBuildFunc(t)(snap); err == nil {
			eng.Stop()
			t.Fatalf("duplicate metric in mode %q built successfully", mode)
		}
	}
}

func TestOrderSizeInstances_ConfigureReviewOnly(t *testing.T) {
	snap := orderSizeInstanceSnapshot(domain.RuleModeReject, domain.RuleModeReviewWithoutHold)
	eng := buildOrderSizeInstanceEngine(t, snap)
	rejectBefore := maps.Clone(eng.orderSizeLimits[nameOrderSizeLimit])
	assertOrderSizeInstanceVerdict(t, eng, "7", domain.VerdictReview)
	snap.OrderSizeLimits[1].MaxQuantity = "8"
	if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit,
		fwengine.LimitSet{OrderSizeLimits: snap.OrderSizeLimits}); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(rejectBefore, eng.orderSizeLimits[nameOrderSizeLimit]) {
		t.Fatal("review update changed the reject instance's applied barriers")
	}
	assertOrderSizeInstanceVerdict(t, eng, "7", domain.VerdictAccept)
	assertOrderSizeInstanceVerdict(t, eng, "9", domain.VerdictReview)
	assertOrderSizeInstanceVerdict(t, eng, "11", domain.VerdictReject)
}

func TestOrderSizeInstances_ConfigureRejectCap(t *testing.T) {
	snap := orderSizeInstanceSnapshot(domain.RuleModeReject, domain.RuleModeReviewWithoutHold)
	eng := buildOrderSizeInstanceEngine(t, snap)
	assertOrderSizeInstanceVerdict(t, eng, "11", domain.VerdictReject)
	snap.OrderSizeLimits[0].MaxQuantity = "12"
	if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit,
		fwengine.LimitSet{OrderSizeLimits: snap.OrderSizeLimits}); err != nil {
		t.Fatal(err)
	}
	assertOrderSizeInstanceVerdict(t, eng, "11", domain.VerdictReview)
	assertOrderSizeInstanceVerdict(t, eng, "13", domain.VerdictReject)
}

func TestOrderSizeInstances_ConfigureNotionalOnly(t *testing.T) {
	snap := orderSizeInstanceSnapshot(domain.RuleModeReject, domain.RuleModeReviewWithoutHold)
	snap.OrderSizeLimits[1].Scope = domain.ScopeAccountSettlementAsset
	snap.OrderSizeLimits[1].Asset = "USD"
	snap.OrderSizeLimits[1].MaxQuantity = ""
	snap.OrderSizeLimits[1].MaxNotional = "5"
	eng := buildOrderSizeInstanceEngine(t, snap)
	assertOrderSizeInstanceVerdict(t, eng, "7", domain.VerdictReview)
	snap.OrderSizeLimits[1].MaxNotional = "8"
	if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit,
		fwengine.LimitSet{OrderSizeLimits: snap.OrderSizeLimits}); err != nil {
		t.Fatal(err)
	}
	assertOrderSizeInstanceVerdict(t, eng, "7", domain.VerdictAccept)
	assertOrderSizeInstanceVerdict(t, eng, "9", domain.VerdictReview)
}

func TestOrderSizeInstances_ConfigureScopeTracked(t *testing.T) {
	snap := orderSizeInstanceSnapshot(domain.RuleModeReject)
	snap.OrderSizeLimits = []domain.LimitOrderSize{
		orderSize(domain.ScopeUnderlyingAsset, "", "AAPL", "5", ""),
		orderSize(domain.ScopeSettlementAsset, "", "AAPL", "", "100"),
	}
	eng := buildOrderSizeInstanceEngine(t, snap)
	assertOrderSizeInstanceVerdict(t, eng, "7", domain.VerdictReject)
	snap.OrderSizeLimits[0].MaxQuantity = "8"
	if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit,
		fwengine.LimitSet{OrderSizeLimits: snap.OrderSizeLimits}); err != nil {
		t.Fatal(err)
	}
	assertOrderSizeInstanceVerdict(t, eng, "7", domain.VerdictAccept)
	assertOrderSizeInstanceVerdict(t, eng, "9", domain.VerdictReject)
}

func TestOrderSizeInstances_ConfigureValidatesAllBeforeUpdate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*domain.LimitOrderSize)
	}{
		{"missing_account", func(limit *domain.LimitOrderSize) { limit.Account = "missing-account" }},
		{"missing_asset", func(limit *domain.LimitOrderSize) { limit.Asset = "missing-asset" }},
		{"invalid_cap", func(limit *domain.LimitOrderSize) { limit.MaxQuantity = "invalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := orderSizeInstanceSnapshot(domain.RuleModeReject, domain.RuleModeReviewWithoutHold)
			eng := buildOrderSizeInstanceEngine(t, snap)
			before := maps.Clone(eng.orderSizeLimits)
			assertOrderSizeInstanceVerdict(t, eng, "11", domain.VerdictReject)
			snap.OrderSizeLimits[0].MaxQuantity = "20"
			tc.change(&snap.OrderSizeLimits[1])
			if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit,
				fwengine.LimitSet{OrderSizeLimits: snap.OrderSizeLimits}); err == nil {
				t.Fatal("invalid second instance update succeeded")
			}
			if !maps.EqualFunc(before, eng.orderSizeLimits, maps.Equal[map[orderSizeLimitKey]orderSizeLimitValue]) {
				t.Fatal("invalid second instance changed the applied barriers")
			}
			assertOrderSizeInstanceVerdict(t, eng, "11", domain.VerdictReject)
		})
	}
}

func TestOrderSizeInstances_ConfigureCallsOnlyChangedInstance(t *testing.T) {
	snap := orderSizeInstanceSnapshot(domain.RuleModeReject, domain.RuleModeReviewWithoutHold)
	eng := buildOrderSizeInstanceEngine(t, snap)
	reviewOnly := buildOrderSizeInstanceEngine(t, orderSizeInstanceSnapshot(domain.RuleModeReviewWithoutHold))
	// This real configurator has no reject instance. An unnecessary call to it
	// fails rather than being hidden by an idempotent native update.
	eng.configurator = reviewOnly.configurator
	snap.OrderSizeLimits[1].MaxQuantity = "8"
	if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit,
		fwengine.LimitSet{OrderSizeLimits: snap.OrderSizeLimits}); err != nil {
		t.Fatal(err)
	}
	assertOrderSizeInstanceVerdict(t, reviewOnly, "7", domain.VerdictAccept)
}

func TestOrderSizeInstances_ConfigureModeSetRequiresRebuild(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before []domain.RuleMode
		after  []domain.RuleMode
	}{
		{"add_review", []domain.RuleMode{domain.RuleModeReject}, []domain.RuleMode{domain.RuleModeReject, domain.RuleModeReviewWithoutHold}},
		{"remove_review", []domain.RuleMode{domain.RuleModeReject, domain.RuleModeReviewWithoutHold}, []domain.RuleMode{domain.RuleModeReject}},
		{"remove_all", []domain.RuleMode{domain.RuleModeReject, domain.RuleModeReviewWithoutHold}, nil},
		{"add_first", nil, []domain.RuleMode{domain.RuleModeReviewWithoutHold}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := buildOrderSizeInstanceEngine(t, orderSizeInstanceSnapshot(tc.before...))
			before := maps.Clone(eng.orderSizeLimits)
			limits := orderSizeInstanceSnapshot(tc.after...).OrderSizeLimits
			for i := range limits {
				limits[i].MaxQuantity = "1"
			}
			if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit,
				fwengine.LimitSet{OrderSizeLimits: limits}); !errors.Is(err, domain.ErrNotImplemented) {
				t.Fatalf("mode-set change error = %v, want ErrNotImplemented", err)
			}
			if !maps.EqualFunc(before, eng.orderSizeLimits, maps.Equal[map[orderSizeLimitKey]orderSizeLimitValue]) {
				t.Fatal("mode-set change updated last applied barriers")
			}
			want := domain.VerdictAccept
			if slices.Contains(tc.before, domain.RuleModeReviewWithoutHold) {
				want = domain.VerdictReview
			}
			assertOrderSizeInstanceVerdict(t, eng, "6", want)
			if slices.Contains(tc.before, domain.RuleModeReject) {
				assertOrderSizeInstanceVerdict(t, eng, "11", domain.VerdictReject)
			}
		})
	}
}

func TestOrderSizeInstances_ConfigureFailureKeepsAppliedBarriers(t *testing.T) {
	snap := orderSizeInstanceSnapshot(domain.RuleModeReject, domain.RuleModeReviewWithoutHold)
	eng := buildOrderSizeInstanceEngine(t, snap)
	withoutReview := buildOrderSizeInstanceEngine(t, orderSizeInstanceSnapshot(domain.RuleModeReject))
	before := maps.Clone(eng.orderSizeLimits[nameOrderSizeLimit+".review_without_hold"])
	configurator := eng.configurator
	eng.configurator = withoutReview.configurator
	snap.OrderSizeLimits[1].MaxQuantity = "8"
	if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit,
		fwengine.LimitSet{OrderSizeLimits: snap.OrderSizeLimits}); err == nil {
		t.Fatal("missing SDK review instance update succeeded")
	}
	if !maps.Equal(before, eng.orderSizeLimits[nameOrderSizeLimit+".review_without_hold"]) {
		t.Fatal("failed update replaced the last applied barriers")
	}
	assertOrderSizeInstanceVerdict(t, eng, "7", domain.VerdictReview)
	eng.configurator = configurator
	if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit,
		fwengine.LimitSet{OrderSizeLimits: snap.OrderSizeLimits}); err != nil {
		t.Fatal(err)
	}
	assertOrderSizeInstanceVerdict(t, eng, "7", domain.VerdictAccept)
}

func TestOrderSizeInstances_RenameSkipsSDK(t *testing.T) {
	for _, mode := range []domain.RuleMode{domain.RuleModeReject, domain.RuleModeReviewWithoutHold} {
		t.Run(string(mode), func(t *testing.T) {
			snap := orderSizeInstanceSnapshot(mode)
			eng := buildOrderSizeInstanceEngine(t, snap)
			withoutOrderSize := buildOrderSizeInstanceEngine(t, orderSizeInstanceSnapshot())
			// No order-size instance exists on this handle, so any SDK update
			// would fail. Alias-only changes must return before reaching it.
			eng.configurator = withoutOrderSize.configurator
			renamedAccount := snap.Accounts[0]
			renamedAccount.Code = "renamed-account"
			if err := eng.RenameAccountResolverEntry("acc-1", renamedAccount); err != nil {
				t.Fatal(err)
			}
			snap.OrderSizeLimits[0].Account = renamedAccount.Code
			if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit,
				fwengine.LimitSet{OrderSizeLimits: snap.OrderSizeLimits}); err != nil {
				t.Fatalf("account rename called SDK: %v", err)
			}
			renamedAsset := testAsset("AAPL")
			renamedAsset.Code = "renamed-asset"
			if err := eng.RenameAssetResolverEntry("AAPL", renamedAsset); err != nil {
				t.Fatal(err)
			}
			snap.OrderSizeLimits[0].Asset = renamedAsset.Code
			if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyOrderSizeLimit,
				fwengine.LimitSet{OrderSizeLimits: snap.OrderSizeLimits}); err != nil {
				t.Fatalf("asset rename called SDK: %v", err)
			}
		})
	}
}

func TestOrderSizeInstances_SplitPartitionsRows(t *testing.T) {
	random := rand.New(rand.NewPCG(11, 643))
	res := testResolver("acc-1")
	for trial := 0; trial < 100; trial++ {
		var limits []domain.LimitOrderSize
		for _, mode := range []domain.RuleMode{domain.RuleModeReject, domain.RuleModeReviewWithoutHold} {
			for _, asset := range []string{"AAPL", "BTC", "MSFT"} {
				if random.IntN(2) == 0 {
					continue
				}
				limit := orderSize(domain.ScopeUnderlyingAsset, "", asset, "10", "")
				limit.Mode = mode
				limits = append(limits, limit)
				limit.Scope, limit.MaxQuantity, limit.MaxNotional = domain.ScopeSettlementAsset, "", "100"
				limits = append(limits, limit)
			}
		}
		random.Shuffle(len(limits), func(i, j int) { limits[i], limits[j] = limits[j], limits[i] })
		instances, err := splitOrderSizeLimits(limits)
		if err != nil {
			t.Fatal(err)
		}
		remaining := make(map[domain.LimitOrderSize]int, len(limits))
		for _, limit := range limits {
			remaining[limit]++
		}
		count := 0
		for i, instance := range instances {
			if instance.mode == domain.RuleModeReject && i != 0 {
				t.Fatal("reject instance is not first")
			}
			if i > 0 && instances[i-1].mode != domain.RuleModeReject && instances[i-1].mode >= instance.mode {
				t.Fatal("non-reject instances are not ordered by mode")
			}
			for _, limit := range instance.limits {
				if remaining[limit] == 0 {
					t.Fatalf("trial %d: duplicate or unexpected row %+v", trial, limit)
				}
				remaining[limit]--
			}
			var sameMode []domain.LimitOrderSize
			for _, limit := range limits {
				if limit.Mode == instance.mode {
					sameMode = append(sameMode, limit)
				}
			}
			if !slices.Equal(instance.limits, sameMode) {
				t.Fatalf("trial %d: split lost or mixed modes", trial)
			}
			count += len(instance.limits)
			broker, assets, accounts, err := orderSizeAxes(instance.limits, res)
			if err != nil || broker != nil || len(accounts) != 0 || len(assets)*2 != len(sameMode) {
				t.Fatalf("trial %d: merge = %+v, %v", trial, assets, err)
			}
			for _, asset := range assets {
				quantity, hasQuantity := asset.Limit.MaxQuantity.Get()
				notional, hasNotional := asset.Limit.MaxNotional.Get()
				if !hasQuantity || !hasNotional || quantity.String() != "10" || notional.String() != "100" {
					t.Fatalf("trial %d: complementary caps lost: %+v", trial, asset.Limit)
				}
			}
		}
		if count != len(limits) {
			t.Fatalf("trial %d: split row count = %d, want %d", trial, count, len(limits))
		}
		for limit, count := range remaining {
			if count != 0 {
				t.Fatalf("trial %d: row %+v missing from split %d times", trial, limit, count)
			}
		}
	}
}

func newOrderSizeInstanceNode(t *testing.T) (node.Node, domain.Caller, *int) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.New(t.TempDir()+"/officer.db", domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.BindExecutionPolicyProviders(fwtrading.NewRegistry().SupportsExecutionPolicy); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	builds := 0
	build := NewOpenPitEngineBuildFunc()
	n, _, err := node.NewLocalNode(ctx, domain.DefaultRealm, store,
		func(snap fwengine.Snapshot) (fwengine.Engine, error) {
			builds++
			return build(snap)
		}, func(err error) { t.Errorf("unexpected fatal shutdown: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n, domain.Caller{Source: domain.SourcePanel, Principal: domain.PrincipalOperator}, &builds
}

func assertOrderSizeNodeStatus(
	t *testing.T, n node.Node, caller domain.Caller, qty string, want domain.OrderStatus,
) {
	t.Helper()
	out, err := n.SubmitOrder(context.Background(), missingAccountOrder("instance-account", qty),
		domain.MissingAccountReject, caller)
	if err != nil || out.Status != want {
		t.Fatalf("quantity %s submit = %+v, %v; want %s", qty, out, err, want)
	}
}

func TestOrderSizeInstances_NodeLifecycle(t *testing.T) {
	ctx := context.Background()
	n, caller, builds := newOrderSizeInstanceNode(t)
	limit := orderSize(domain.ScopeAccountUnderlyingAsset, "instance-account", "BTC", "10", "")
	if _, err := n.PutOrderSizeLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountCreate, caller); err != nil {
		t.Fatal(err)
	}
	fundMissingAccount(t, n, caller, limit.Account)
	assertOrderSizeNodeStatus(t, n, caller, "11", domain.OrderStatusRejected)
	before := *builds
	limit.Mode, limit.MaxQuantity = domain.RuleModeReviewWithoutHold, "5"
	if sink, err := n.PutOrderSizeLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountReject, caller); err != nil || sink == nil || *builds != before+1 {
		t.Fatalf("first review barrier: sink %T, builds %d -> %d, error %v", sink, before, *builds, err)
	}
	assertOrderSizeNodeStatus(t, n, caller, "6", domain.OrderStatusAwaitingReview)
	assertOrderSizeNodeStatus(t, n, caller, "11", domain.OrderStatusRejected)
	before = *builds
	second := limit
	second.Scope, second.Account, second.Asset = domain.ScopeUnderlyingAsset, "", "USDT"
	if sink, err := n.PutOrderSizeLimit(ctx, second, domain.LimitWriteReplace, domain.MissingAccountReject, caller); err != nil || sink != nil || *builds != before {
		t.Fatalf("second review barrier: sink %T, builds %d -> %d, error %v", sink, before, *builds, err)
	}
	otherTarget := missingAccountOrder(limit.Account, "6")
	otherTarget.BaseAsset, otherTarget.QuoteAsset = "USDT", "BTC"
	otherTarget.Side = domain.OrderSideSell
	if out, err := n.SubmitOrder(ctx, otherTarget, domain.MissingAccountReject, caller); err != nil || out.Status != domain.OrderStatusAwaitingReview {
		t.Fatalf("second target submit = %+v, %v; want awaiting_review", out, err)
	}
	assertOrderSizeNodeStatus(t, n, caller, "11", domain.OrderStatusRejected)
	if _, err := n.DeleteLimit(ctx, node.LimitTarget{
		Policy: domain.PolicyOrderSizeLimit, Mode: second.Mode, Scope: second.Scope, Asset: second.Asset,
	}, caller); err != nil || *builds != before {
		t.Fatalf("delete non-last review: builds %d -> %d, error %v", before, *builds, err)
	}
	if out, err := n.SubmitOrder(ctx, otherTarget, domain.MissingAccountReject, caller); err != nil || out.Status != domain.OrderStatusCommitted {
		t.Fatalf("deleted review target submit = %+v, %v; want committed", out, err)
	}
	assertOrderSizeNodeStatus(t, n, caller, "6", domain.OrderStatusAwaitingReview)
	assertOrderSizeNodeStatus(t, n, caller, "11", domain.OrderStatusRejected)
	if sink, err := n.DeleteLimit(ctx, node.LimitTarget{
		Policy: domain.PolicyOrderSizeLimit, Mode: limit.Mode, Scope: limit.Scope, Account: limit.Account, Asset: limit.Asset,
	}, caller); err != nil || sink == nil || *builds != before+1 {
		t.Fatalf("delete last review: sink %T, builds %d -> %d, error %v", sink, before, *builds, err)
	}
	assertOrderSizeNodeStatus(t, n, caller, "6", domain.OrderStatusCommitted)
	assertOrderSizeNodeStatus(t, n, caller, "11", domain.OrderStatusRejected)
}

func TestOrderSizeInstances_OnlineRestoreAddsReview(t *testing.T) {
	ctx := context.Background()
	n, caller, builds := newOrderSizeInstanceNode(t)
	limit := orderSize(domain.ScopeAccountUnderlyingAsset, "instance-account", "BTC", "10", "")
	if _, err := n.PutOrderSizeLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountCreate, caller); err != nil {
		t.Fatal(err)
	}
	fundMissingAccount(t, n, caller, limit.Account)
	limit.Mode, limit.MaxQuantity = domain.RuleModeReviewWithoutHold, "5"
	if _, err := n.PutOrderSizeLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountReject, caller); err != nil {
		t.Fatal(err)
	}
	archive, err := n.ExportBackup(ctx, backup.Scope{All: true}, caller)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.DeleteLimit(ctx, node.LimitTarget{
		Policy: domain.PolicyOrderSizeLimit, Mode: limit.Mode, Scope: limit.Scope, Account: limit.Account, Asset: limit.Asset,
	}, caller); err != nil {
		t.Fatal(err)
	}
	before := *builds
	if summary, sink, err := n.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeOverwrite,
	}, caller); err != nil || !summary.RestartRequired || sink == nil || *builds != before+1 {
		t.Fatalf("online restore = %+v, sink %T, builds %d -> %d, error %v", summary, sink, before, *builds, err)
	}
	assertOrderSizeNodeStatus(t, n, caller, "6", domain.OrderStatusAwaitingReview)
	assertOrderSizeNodeStatus(t, n, caller, "11", domain.OrderStatusRejected)
}
