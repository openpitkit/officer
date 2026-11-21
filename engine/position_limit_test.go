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
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
)

func positionLimit(acct, asset, maximum string) domain.LimitPosition {
	return domain.LimitPosition{
		Scope: domain.ScopeAccountAsset, Account: domain.AccountID(acct),
		Asset: asset, MaxPosition: maximum,
	}
}

func positionLimitSnapshot() fwengine.Snapshot {
	return fwengine.Snapshot{
		Assets:   testAssets(),
		Accounts: []domain.Account{account("acc-1"), account("acc-2")},
		Balances: []domain.Balance{
			fundedBalance("acc-1", "USD", "10000"),
			fundedBalance("acc-1", "AAPL", "90"),
			fundedBalance("acc-1", "MSFT", "90"),
			fundedBalance("acc-2", "USD", "10000"),
			fundedBalance("acc-2", "AAPL", "90"),
			fundedBalance("acc-2", "MSFT", "90"),
		},
	}
}

func assertPositionLimitCheck(
	t *testing.T, eng fwengine.Engine, probe domain.OrderProbe, passed bool,
) {
	t.Helper()
	out, err := materializeCheckedOrder(eng, probe)
	if err != nil {
		t.Fatalf("CheckOrder(%+v): %v", probe, err)
	}
	if out.Passed != passed {
		t.Fatalf("CheckOrder(%s, %s, %s) = %+v, want passed=%t",
			probe.Account, probe.BaseAsset, probe.AmountValue, out, passed)
	}
	if out.WouldBlock != nil {
		t.Fatalf("position limit must not block the account: %+v", out.WouldBlock)
	}
	if passed {
		if len(out.Rejects) != 0 {
			t.Fatalf("passing order has rejects: %+v", out.Rejects)
		}
		return
	}
	if len(out.Rejects) != 1 {
		t.Fatalf("want one position-limit reject, got %+v", out.Rejects)
	}
	rejected := out.Rejects[0]
	if rejected.Code != domain.RejectCodePositionLimitExceeded ||
		rejected.Policy != nameSpotFunds || rejected.Scope != "order" {
		t.Fatalf("position-limit reject = %+v, want code=%s policy=%s scope=order",
			rejected, domain.RejectCodePositionLimitExceeded, nameSpotFunds)
	}
}

func TestPositionLimit_LongBoundaryAndNoAccountBlock(t *testing.T) {
	t.Parallel()
	eng, err := newTestOpenPitEngineBuildFunc(t)(positionLimitSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyPositionLimit,
		fwengine.LimitSet{PositionLimits: []domain.LimitPosition{positionLimit("acc-1", "AAPL", "100")}},
	); err != nil {
		t.Fatal(err)
	}
	assertPositionLimitCheck(t, eng, checkProbe("acc-1", domain.OrderSideBuy, "10", "1"), true)
	assertPositionLimitCheck(t, eng, checkProbe("acc-1", domain.OrderSideBuy, "10.00000001", "1"), false)
	order := testOrder()
	order.Account, order.AmountValue, order.Price = "acc-1", "10.00000001", "1"
	rejected, err := materializeOrderResult(eng, order)
	if err != nil {
		t.Fatal(err)
	}
	if (rejected.Verdict == domain.VerdictAccept) || len(rejected.Blocks) != 0 || len(rejected.Rejects) != 1 ||
		rejected.Rejects[0].Code != domain.RejectCodePositionLimitExceeded ||
		rejected.Rejects[0].Policy != nameSpotFunds || rejected.Rejects[0].Scope != "order" {
		t.Fatalf("submitted over-ceiling order = %+v, want order reject without account block", rejected)
	}
	order.AmountValue = "10"
	accepted, err := materializeOrderResult(eng, order)
	if err != nil || accepted.Verdict != domain.VerdictAccept || len(accepted.Blocks) != 0 {
		t.Fatalf("boundary order after rejection = %+v, %v, want accepted without block", accepted, err)
	}
}

func TestPositionLimit_ShortBoundary(t *testing.T) {
	t.Parallel()
	snap := positionLimitSnapshot()
	// TrackOnly disables funds-shortfall gating, so the independent position
	// ceiling can judge an order increasing an existing short position.
	snap.SpotFundsTrackOnly = true
	snap.Balances[1].Available = "-90"
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyPositionLimit,
		fwengine.LimitSet{PositionLimits: []domain.LimitPosition{positionLimit("acc-1", "AAPL", "100")}},
	); err != nil {
		t.Fatal(err)
	}
	assertPositionLimitCheck(t, eng, checkProbe("acc-1", domain.OrderSideSell, "10", "1"), true)
	assertPositionLimitCheck(t, eng, checkProbe("acc-1", domain.OrderSideSell, "10.00000001", "1"), false)
	order := testOrder()
	order.Account, order.Side, order.AmountValue, order.Price = "acc-1", domain.OrderSideSell, "10.00000001", "1"
	rejected, err := materializeOrderResult(eng, order)
	if err != nil || (rejected.Verdict == domain.VerdictAccept) || len(rejected.Blocks) != 0 ||
		len(rejected.Rejects) != 1 || rejected.Rejects[0].Code != domain.RejectCodePositionLimitExceeded ||
		rejected.Rejects[0].Policy != nameSpotFunds || rejected.Rejects[0].Scope != "order" {
		t.Fatalf("submitted short order = %+v, %v, want position-limit reject without block", rejected, err)
	}
	order.AmountValue = "10"
	accepted, err := materializeOrderResult(eng, order)
	if err != nil || accepted.Verdict != domain.VerdictAccept {
		t.Fatalf("short boundary order after rejection = %+v, %v, want accepted", accepted, err)
	}
}

func TestPositionLimit_IndependentPairs(t *testing.T) {
	t.Parallel()
	snap := positionLimitSnapshot()
	snap.PositionLimits = []domain.LimitPosition{positionLimit("acc-1", "AAPL", "100")}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	assertPositionLimitCheck(t, eng, checkProbe("acc-1", domain.OrderSideBuy, "11", "1"), false)
	assertPositionLimitCheck(t, eng, checkProbe("acc-2", domain.OrderSideBuy, "11", "1"), true)
	probe := checkProbe("acc-1", domain.OrderSideBuy, "11", "1")
	probe.BaseAsset = "MSFT"
	assertPositionLimitCheck(t, eng, probe, true)
}

func TestPositionLimit_ChangeAndClear(t *testing.T) {
	t.Parallel()
	snap := positionLimitSnapshot()
	snap.PositionLimits = []domain.LimitPosition{positionLimit("acc-1", "AAPL", "100")}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	probe := checkProbe("acc-1", domain.OrderSideBuy, "10.00000001", "1")
	assertPositionLimitCheck(t, eng, probe, false)
	for _, maximum := range []string{"100.00000001", "100", "0", ""} {
		var limits []domain.LimitPosition
		if maximum != "" {
			limits = []domain.LimitPosition{positionLimit("acc-1", "AAPL", maximum)}
		}
		if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyPositionLimit,
			fwengine.LimitSet{PositionLimits: limits},
		); err != nil {
			t.Fatalf("configure maximum %q: %v", maximum, err)
		}
		assertPositionLimitCheck(t, eng, probe, maximum == "" || maximum == "100.00000001")
	}
}

func TestPositionLimit_VanishedPair(t *testing.T) {
	t.Parallel()
	snap := positionLimitSnapshot()
	snap.PositionLimits = []domain.LimitPosition{
		positionLimit("acc-1", "AAPL", "100"), positionLimit("acc-2", "AAPL", "100"),
	}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	assertPositionLimitCheck(t, eng, checkProbe("acc-2", domain.OrderSideBuy, "11", "1"), false)
	if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyPositionLimit,
		fwengine.LimitSet{PositionLimits: snap.PositionLimits[:1]},
	); err != nil {
		t.Fatal(err)
	}
	assertPositionLimitCheck(t, eng, checkProbe("acc-2", domain.OrderSideBuy, "11", "1"), true)
	assertPositionLimitCheck(t, eng, checkProbe("acc-1", domain.OrderSideBuy, "11", "1"), false)
}

func TestPositionLimit_RenameAndClear(t *testing.T) {
	t.Parallel()
	snap := positionLimitSnapshot()
	snap.PositionLimits = []domain.LimitPosition{positionLimit("acc-1", "AAPL", "100")}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	acct := snap.Accounts[0]
	acct.Code = "renamed"
	if err := eng.RenameAccountResolverEntry("acc-1", acct); err != nil {
		t.Fatal(err)
	}
	asset := testAsset("AAPL")
	asset.Code = "renamed-asset"
	if err := eng.RenameAssetResolverEntry("AAPL", asset); err != nil {
		t.Fatal(err)
	}
	probe := checkProbe("renamed", domain.OrderSideBuy, "10.00000001", "1")
	probe.BaseAsset = asset.Code
	assertPositionLimitCheck(t, eng, probe, false)
	if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyPositionLimit,
		fwengine.LimitSet{PositionLimits: []domain.LimitPosition{
			positionLimit("renamed", asset.Code, "100.00000001"),
		}},
	); err != nil {
		t.Fatal(err)
	}
	assertPositionLimitCheck(t, eng, probe, true)
	probe.AmountValue = "11"
	assertPositionLimitCheck(t, eng, probe, false)
	if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyPositionLimit, fwengine.LimitSet{}); err != nil {
		t.Fatal(err)
	}
	probe.AmountValue = "100"
	assertPositionLimitCheck(t, eng, probe, true)
}

func TestPositionLimit_RemovedResolverAssetCanBeCleared(t *testing.T) {
	t.Parallel()
	snap := positionLimitSnapshot()
	snap.PositionLimits = []domain.LimitPosition{positionLimit("acc-1", "AAPL", "100")}
	eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	asset := testAsset("AAPL")
	if err := eng.RemoveAssetResolverEntry(asset); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyPositionLimit, fwengine.LimitSet{}); err != nil {
		t.Fatalf("clear pair without resolver asset: %v", err)
	}
	if err := eng.AddAssetResolverEntry(asset); err != nil {
		t.Fatal(err)
	}
	assertPositionLimitCheck(t, eng, checkProbe("acc-1", domain.OrderSideBuy, "100", "1"), true)
}

func TestPositionLimit_BuildAndRebuild(t *testing.T) {
	t.Parallel()
	snap := positionLimitSnapshot()
	snap.PositionLimits = []domain.LimitPosition{positionLimit("acc-1", "AAPL", "100")}
	build := newTestOpenPitEngineBuildFunc(t)
	eng, err := build(snap)
	if err != nil {
		t.Fatal(err)
	}
	assertPositionLimitCheck(t, eng, checkProbe("acc-1", domain.OrderSideBuy, "10.00000001", "1"), false)
	eng.Stop()
	snap.PositionLimits = nil
	rebuilt, err := build(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer rebuilt.Stop()
	assertPositionLimitCheck(t, rebuilt, checkProbe("acc-1", domain.OrderSideBuy, "10.00000001", "1"), true)
}

func TestPositionLimit_BuildRejectsInvalidStoredSet(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		limits []domain.LimitPosition
	}{
		{
			name:   "unknown account",
			limits: []domain.LimitPosition{positionLimit("unknown", "AAPL", "100")},
		},
		{
			name:   "unknown asset",
			limits: []domain.LimitPosition{positionLimit("acc-1", "unknown", "100")},
		},
		{
			name: "duplicate pair",
			limits: []domain.LimitPosition{
				positionLimit("acc-1", "AAPL", "100"),
				positionLimit("acc-1", "AAPL", "200"),
			},
		},
		{
			name:   "invalid value",
			limits: []domain.LimitPosition{positionLimit("acc-1", "AAPL", "-1")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := positionLimitSnapshot()
			snap.PositionLimits = tc.limits
			eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
			if eng != nil {
				defer eng.Stop()
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("build invalid stored position limits = %v, want ErrInvalid", err)
			}
			if eng != nil {
				t.Error("build invalid stored position limits returned a non-nil engine")
			}
		})
	}
}

func TestPositionLimit_AtomicValidation(t *testing.T) {
	t.Parallel()
	for _, bad := range []domain.LimitPosition{
		positionLimit("acc-2", "unknown", "100"),
		positionLimit("unknown", "AAPL", "100"),
		positionLimit("acc-2", "AAPL", "-1"),
		positionLimit("acc-2", "AAPL", ""),
		{Scope: domain.ScopeGlobal, MaxPosition: "100"},
		positionLimit("acc-1", "AAPL", "200"),
	} {
		t.Run(string(bad.Account)+"/"+bad.Asset+"/"+bad.MaxPosition, func(t *testing.T) {
			snap := positionLimitSnapshot()
			snap.PositionLimits = []domain.LimitPosition{
				positionLimit("acc-1", "AAPL", "100"), positionLimit("acc-2", "MSFT", "100"),
			}
			eng, err := newTestOpenPitEngineBuildFunc(t)(snap)
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Stop()
			_, err = eng.ConfigurePolicy(context.Background(), domain.PolicyPositionLimit,
				fwengine.LimitSet{PositionLimits: []domain.LimitPosition{
					positionLimit("acc-1", "AAPL", "110"), bad,
				}},
			)
			if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), domain.PolicyPositionLimit) {
				t.Fatalf("invalid complete set = %v, want position_limit ErrInvalid", err)
			}
			assertPositionLimitCheck(t, eng, checkProbe("acc-1", domain.OrderSideBuy, "11", "1"), false)
			probe := checkProbe("acc-2", domain.OrderSideBuy, "11", "1")
			probe.BaseAsset = "MSFT"
			assertPositionLimitCheck(t, eng, probe, false)
		})
	}
}

func TestPositionLimit_RandomizedCompleteSets(t *testing.T) {
	t.Parallel()
	eng, err := newTestOpenPitEngineBuildFunc(t)(positionLimitSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	rng := rand.New(rand.NewSource(281))
	ceilings := []string{"", "0", "99.99999999", "100", "100.00000001"}
	for round := range 50 {
		var limits []domain.LimitPosition
		var wants [4]bool
		for pair := range wants {
			maximum := ceilings[rng.Intn(len(ceilings))]
			wants[pair] = maximum == "" || maximum == "100" || maximum == "100.00000001"
			if maximum != "" {
				limits = append(limits, positionLimit(
					[]string{"acc-1", "acc-2"}[pair/2], []string{"AAPL", "MSFT"}[pair%2], maximum,
				))
			}
		}
		rng.Shuffle(len(limits), func(i, j int) { limits[i], limits[j] = limits[j], limits[i] })
		for repeat := range 2 {
			if _, err := eng.ConfigurePolicy(context.Background(), domain.PolicyPositionLimit,
				fwengine.LimitSet{PositionLimits: limits},
			); err != nil {
				t.Fatalf("round %d repeat %d: %v", round, repeat, err)
			}
			for pair, want := range wants {
				probe := checkProbe([]string{"acc-1", "acc-2"}[pair/2], domain.OrderSideBuy, "10", "1")
				probe.BaseAsset = []string{"AAPL", "MSFT"}[pair%2]
				assertPositionLimitCheck(t, eng, probe, want)
			}
		}
	}
}

func TestPositionLimit_RetuneWaitsForLaneAndFinishesAfterCancellation(t *testing.T) {
	eng, err := newTestOpenPitEngineBuildFunc(t)(positionLimitSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	acct, err := eng.AccountID("acc-1")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	freeLane := func() { releaseOnce.Do(func() { close(release) }) }
	defer freeLane()
	held := eng.AsyncEngine().Submit(context.Background(), acct, func() error {
		close(started)
		<-release
		return nil
	})
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	retuned := make(chan error, 1)
	go func() {
		_, err := eng.ConfigurePolicy(ctx, domain.PolicyPositionLimit,
			fwengine.LimitSet{PositionLimits: []domain.LimitPosition{positionLimit("acc-1", "AAPL", "100")}},
		)
		retuned <- err
	}()
	adapter := eng.(*openPitEngine)
	deadline := time.Now().Add(5 * time.Second)
	for adapter.mu.TryLock() {
		adapter.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("position retune did not acquire the adapter mutex")
		}
		runtime.Gosched()
	}
	select {
	case err := <-retuned:
		t.Fatalf("position retune finished while a lane was busy: %v", err)
	default:
	}
	cancel()
	freeLane()
	select {
	case err := <-retuned:
		if err != nil {
			t.Fatalf("position retune after cancellation = %v, want completed update", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("position retune did not finish after the lane was freed")
	}
	waitCtx, stopWait := context.WithTimeout(context.Background(), time.Second)
	defer stopWait()
	if _, err := held.Await(waitCtx); err != nil {
		t.Fatal(err)
	}
	assertPositionLimitCheck(t, eng, checkProbe("acc-1", domain.OrderSideBuy, "11", "1"), false)
}
