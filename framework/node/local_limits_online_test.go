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
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

const livePolicyTestTimeout = 5 * time.Second

type livePolicyMarkerSink struct{}

type failPolicyLifecycleCommitRealm struct {
	store.RealmStore
	putRateErr    error
	deleteRateErr error
}

func (r *failPolicyLifecycleCommitRealm) PutRateLimit(
	ctx context.Context, limit domain.LimitRate,
) error {
	if r.putRateErr != nil {
		return r.putRateErr
	}
	return r.RealmStore.PutRateLimit(ctx, limit)
}

func (r *failPolicyLifecycleCommitRealm) DeleteRateLimit(
	ctx context.Context,
	scope domain.LimitScope,
	account domain.AccountID,
	asset string,
) error {
	if r.deleteRateErr != nil {
		return r.deleteRateErr
	}
	return r.RealmStore.DeleteRateLimit(ctx, scope, account, asset)
}

func (*livePolicyMarkerSink) Push(marketdata.QuoteUpdate) error { return nil }

type blockingPolicyConfigureEngine struct {
	*fakeEngine
	entered chan<- struct{}
	release <-chan struct{}
}

func (e *blockingPolicyConfigureEngine) ConfigurePolicy(
	ctx context.Context, policy string, limits engine.LimitSet,
) (engine.PolicyConfigurationResult, error) {
	e.entered <- struct{}{}
	<-e.release
	return e.fakeEngine.ConfigurePolicy(ctx, policy, limits)
}

func TestLocalNode_RateAndOrderSizeCRUDConfigureLive(t *testing.T) {
	t.Parallel()
	old := newFakeEngine()
	n, st := newTestNode(t, old)
	ctx := context.Background()
	builds := 0
	n.build = func(snap engine.Snapshot) (engine.Engine, error) {
		builds++
		next := newFakeEngine()
		next.sink = &livePolicyMarkerSink{}
		return fakeBuild(next, new(engine.Snapshot))(snap)
	}

	if _, err := n.PutRateLimit(
		ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), testCaller,
	); err != nil {
		t.Fatalf("put first rate: %v", err)
	}
	if _, err := n.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeBroker, MaxQuantity: "100",
	}, testCaller); err != nil {
		t.Fatalf("put first order size: %v", err)
	}
	if _, err := n.DeleteLimit(ctx, LimitTarget{
		Policy: domain.PolicyOrderSizeLimit,
		Scope:  domain.ScopeBroker,
	}, testCaller); err != nil {
		t.Fatalf("delete last order size: %v", err)
	}
	if _, err := n.DeleteLimit(ctx, LimitTarget{
		Policy: domain.PolicyRateLimit,
		Scope:  domain.ScopeBroker,
	}, testCaller); err != nil {
		t.Fatalf("delete last rate: %v", err)
	}
	if builds != 4 {
		t.Fatalf("policy lifecycle builds = %d, want 4", builds)
	}
	rateLimits, err := st.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	orderLimits, err := st.ListOrderSizeLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListOrderSizeLimits: %v", err)
	}
	if len(rateLimits) != 0 || len(orderLimits) != 0 {
		t.Fatalf("limits after lifecycle CRUD = rate %+v order %+v, want empty", rateLimits, orderLimits)
	}
}

func TestLocalNode_OrderSizeConfigureFailureRevertsWithoutRebuild(t *testing.T) {
	t.Parallel()
	configureErr := errors.New("configure rejected before mutation")
	old := newFakeEngine()
	n, st := newTestNode(t, old)
	current, _ := preparePolicyLifecycleBuild(n)
	ctx := context.Background()
	initial := domain.LimitOrderSize{Scope: domain.ScopeBroker, MaxQuantity: "50"}
	if _, err := n.PutOrderSizeLimit(ctx, initial, testCaller); err != nil {
		t.Fatalf("PutOrderSizeLimit initial: %v", err)
	}
	current.configureErr = configureErr
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}

	sink, err := n.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeBroker, MaxQuantity: "100",
	}, testCaller)
	if !errors.Is(err, configureErr) {
		t.Fatalf("PutOrderSizeLimit error = %v, want configure failure", err)
	}
	if sink != nil {
		t.Fatalf("PutOrderSizeLimit sink = %T, want nil without rebuild", sink)
	}
	if builds != 0 {
		t.Fatalf("builds = %d, want none for pre-mutation configure failure", builds)
	}
	stored, listErr := st.ListOrderSizeLimits(ctx, "")
	if listErr != nil {
		t.Fatalf("ListOrderSizeLimits: %v", listErr)
	}
	if len(stored) != 1 || stored[0] != initial {
		t.Fatalf("stored order-size limits = %+v, want initial barrier", stored)
	}
}

func TestLocalNode_LivePolicyConfigurationQuiescesAccountLanes(t *testing.T) {
	t.Parallel()
	base := newFakeEngine()
	n, _ := newTestNode(t, base)
	if err := n.realm.PutRateLimit(
		context.Background(),
		rateLimit(domain.ScopeBroker, "", "", 50, time.Second),
	); err != nil {
		t.Fatalf("seed rate limit: %v", err)
	}
	configureEntered := make(chan struct{}, 1)
	configureRelease := make(chan struct{})
	n.engineMu.Lock()
	n.engine = &blockingPolicyConfigureEngine{
		fakeEngine: base,
		entered:    configureEntered,
		release:    configureRelease,
	}
	n.engineMu.Unlock()

	laneEntered := make(chan struct{})
	laneRelease := make(chan struct{})
	laneDone := make(chan error, 1)
	go func() {
		eng, done, err := n.beginLane()
		if err != nil {
			laneDone <- err
			return
		}
		defer done()
		laneDone <- eng.RunAccountSynchronized(
			context.Background(), "acc-1", func(engine.AccountLane) error {
				close(laneEntered)
				<-laneRelease
				return nil
			},
		)
	}()
	waitLivePolicySignal(t, laneEntered, "account lane did not start")

	configureDone := make(chan error, 1)
	go func() {
		_, err := n.PutRateLimit(
			context.Background(),
			rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
			testCaller,
		)
		configureDone <- err
	}()
	assertLivePolicyBlocked(t, configureEntered, "policy configure entered before active lane left")
	close(laneRelease)
	if err := waitLivePolicyResult(t, laneDone, "account lane did not finish"); err != nil {
		t.Fatalf("account lane: %v", err)
	}
	waitLivePolicySignal(t, configureEntered, "policy configure did not start after lane left")

	nextLaneEntered := make(chan struct{})
	nextLaneDone := make(chan struct{})
	go func() {
		_, done, err := n.beginLane()
		if err == nil {
			close(nextLaneEntered)
			done()
		}
		close(nextLaneDone)
	}()
	assertLivePolicyBlocked(t, nextLaneEntered, "new lane entered during policy configure")
	close(configureRelease)
	if err := waitLivePolicyResult(t, configureDone, "policy configure did not finish"); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	waitLivePolicySignal(t, nextLaneEntered, "new lane did not enter after policy configure")
	waitLivePolicySignal(t, nextLaneDone, "new lane did not finish")
}

func TestLocalNode_RateLimitAuditFailureFatalsAfterConfigure(t *testing.T) {
	t.Parallel()
	auditErr := errors.New("set rate limit audit failed")
	st := newRealmWrapStore(newMemoryStore("rate-limit-audit.db"), func(r store.RealmStore) store.RealmStore {
		return &failActionAuditRealm{
			RealmStore: r,
			action:     domain.AuditActionSetLimit,
			err:        auditErr,
		}
	})
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := newFakeEngine()
	var fatalErr error
	n := newTestNodeWithStore(t, st, eng, WithFatalShutdownHook(func(err error) {
		fatalErr = err
	}))
	if err := n.realm.PutRateLimit(
		ctx, rateLimit(domain.ScopeBroker, "", "", 50, time.Second),
	); err != nil {
		t.Fatalf("seed rate limit: %v", err)
	}

	sink, err := n.PutRateLimit(
		ctx,
		rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
		testCaller,
	)
	if !errors.Is(err, auditErr) {
		t.Fatalf("PutRateLimit error = %v, want audit failure", err)
	}
	if sink != nil {
		t.Fatalf("PutRateLimit sink = %T, want nil without rebuild", sink)
	}
	if fatalErr == nil {
		t.Fatal("fatal error = nil after committed policy update lost its audit")
	}
	stored, listErr := n.realm.ListRateLimits(ctx, "")
	if listErr != nil {
		t.Fatalf("ListRateLimits: %v", listErr)
	}
	if len(stored) != 1 || len(eng.configureCalls) != 1 {
		t.Fatalf("committed state = limits %+v configure calls %+v, want both committed",
			stored, eng.configureCalls)
	}
}

func TestLocalNode_FirstRateLimitBuildFailureIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := newFakeEngine()
	n, st := newTestNode(t, old)
	buildErr := errors.New("policy lifecycle build failed")
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		return nil, buildErr
	}

	sink, err := n.PutRateLimit(
		ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), testCaller,
	)
	if !errors.Is(err, buildErr) {
		t.Fatalf("PutRateLimit error = %v, want build failure", err)
	}
	if sink != nil {
		t.Fatalf("PutRateLimit sink = %T, want nil", sink)
	}
	if n.currentEngine() != old || !old.running {
		t.Fatal("first-barrier build failure replaced or stopped the old engine")
	}
	limits, listErr := st.ListRateLimits(ctx, "")
	if listErr != nil {
		t.Fatalf("ListRateLimits: %v", listErr)
	}
	if len(limits) != 0 {
		t.Fatalf("stored rate limits = %+v, want empty", limits)
	}
}

func TestLocalNode_FirstRateLimitStoreCommitFailureIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	commitErr := errors.New("put rate limit failed")
	real := newMemoryStore("first-rate-commit.db")
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	st := newRealmWrapStore(real, func(realm store.RealmStore) store.RealmStore {
		return &failPolicyLifecycleCommitRealm{
			RealmStore: realm,
			putRateErr: commitErr,
		}
	})
	old := newFakeEngine()
	n := newTestNodeWithStore(t, st, old)
	next := newFakeEngine()
	n.build = fakeBuild(next, new(engine.Snapshot))

	_, err := n.PutRateLimit(
		ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), testCaller,
	)
	if !errors.Is(err, commitErr) {
		t.Fatalf("PutRateLimit error = %v, want store commit failure", err)
	}
	if n.currentEngine() != old || !old.running || next.running {
		t.Fatalf(
			"engine state after commit failure: current=%p old=%v next=%v",
			n.currentEngine(), old.running, next.running,
		)
	}
	limits, listErr := n.realm.ListRateLimits(ctx, "")
	if listErr != nil {
		t.Fatalf("ListRateLimits: %v", listErr)
	}
	if len(limits) != 0 {
		t.Fatalf("stored rate limits = %+v, want empty", limits)
	}
}

func TestLocalNode_LastRateLimitStoreCommitFailureIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	real := newMemoryStore("last-rate-commit.db")
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	var wrapped *failPolicyLifecycleCommitRealm
	st := newRealmWrapStore(real, func(realm store.RealmStore) store.RealmStore {
		wrapped = &failPolicyLifecycleCommitRealm{RealmStore: realm}
		return wrapped
	})
	old := newFakeEngine()
	n := newTestNodeWithStore(t, st, old)
	current := newFakeEngine()
	n.build = fakeBuild(current, new(engine.Snapshot))
	limit := rateLimit(domain.ScopeBroker, "", "", 100, time.Second)
	if _, err := n.PutRateLimit(ctx, limit, testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	commitErr := errors.New("delete rate limit failed")
	wrapped.deleteRateErr = commitErr
	next := newFakeEngine()
	n.build = fakeBuild(next, new(engine.Snapshot))
	_, err := n.DeleteLimit(ctx, LimitTarget{
		Policy: domain.PolicyRateLimit,
		Scope:  domain.ScopeBroker,
	}, testCaller)
	if !errors.Is(err, commitErr) {
		t.Fatalf("DeleteLimit error = %v, want store commit failure", err)
	}
	if n.currentEngine() != current || !current.running || next.running {
		t.Fatalf(
			"engine state after commit failure: current=%p serving=%v next=%v",
			n.currentEngine(), current.running, next.running,
		)
	}
	limits, listErr := n.realm.ListRateLimits(ctx, "")
	if listErr != nil {
		t.Fatalf("ListRateLimits: %v", listErr)
	}
	if len(limits) != 1 || limits[0] != limit {
		t.Fatalf("stored rate limits = %+v, want original barrier", limits)
	}
}

func assertLivePolicyBlocked[Value any](t *testing.T, ch <-chan Value, failure string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal(failure)
	case <-time.After(50 * time.Millisecond):
	}
}

func waitLivePolicySignal(t *testing.T, ch <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(livePolicyTestTimeout):
		t.Fatal(failure)
	}
}

func waitLivePolicyResult(t *testing.T, ch <-chan error, failure string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(livePolicyTestTimeout):
		t.Fatal(failure)
		return nil
	}
}
