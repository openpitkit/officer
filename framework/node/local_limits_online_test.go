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

package node

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/openpit/asyncengine"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

const livePolicyTestTimeout = 5 * time.Second

func TestLocalNode_OrderSizeModeKeyLifecycle(t *testing.T) {
	for _, firstMode := range []domain.RuleMode{domain.RuleModeReject, domain.RuleModeReviewWithoutHold} {
		t.Run(string(firstMode), func(t *testing.T) {
			ctx := context.Background()
			n, st := newTestNode(t, newFakeEngine())
			builds := 0
			var current *fakeEngine
			var built engine.Snapshot
			n.build = func(snapshot engine.Snapshot) (engine.Engine, error) {
				builds++
				current = newFakeEngine()
				return fakeBuild(current, &built)(snapshot)
			}
			first := domain.LimitOrderSize{Mode: firstMode, Scope: domain.ScopeBroker, MaxQuantity: "10"}
			second := first
			second.Mode, second.MaxQuantity = domain.RuleModeReject, "20"
			if firstMode == domain.RuleModeReject {
				second.Mode = domain.RuleModeReviewWithoutHold
			}
			put := func(limit domain.LimitOrderSize, write domain.LimitWrite) error {
				_, err := n.PutOrderSizeLimit(ctx, limit, write, domain.MissingAccountCreate, testCaller)
				return err
			}
			assertStored := func(want ...domain.LimitOrderSize) {
				t.Helper()
				got, err := st.ListOrderSizeLimits(ctx, "")
				if err != nil {
					t.Fatal(err)
				}
				gotMap, err := limitMap(got)
				if err != nil {
					t.Fatal(err)
				}
				wantMap, err := limitMap(want)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(gotMap, wantMap) {
					t.Fatalf("stored barriers = %+v; want %+v", got, want)
				}
			}
			if err := put(first, domain.LimitWriteAdd); err != nil {
				t.Fatal(err)
			}
			if err := put(second, domain.LimitWriteAdd); err != nil {
				t.Fatal(err)
			}
			assertStored(first, second)
			if builds != 2 || len(built.OrderSizeLimits) != 2 {
				t.Fatalf("first barrier per mode: builds=%d snapshot=%+v", builds, built.OrderSizeLimits)
			}
			for _, limit := range []domain.LimitOrderSize{first, second} {
				key, err := limit.Key()
				if err != nil {
					t.Fatal(err)
				}
				found, ok, err := n.readOrderSizeBarrier(ctx, key)
				if err != nil || !ok || found != limit {
					t.Fatalf("read mode %s = %+v, %t, %v", limit.Mode, found, ok, err)
				}
			}
			second.MaxQuantity = "19"
			if err := put(second, domain.LimitWriteChange); err != nil {
				t.Fatal(err)
			}
			assertStored(first, second)
			if builds != 2 {
				t.Fatalf("update rebuilt engine: %d", builds)
			}
			configureErr := errors.New("mode update rejected")
			current.configureErr = configureErr
			failed := second
			failed.MaxQuantity = "18"
			if err := put(failed, domain.LimitWriteChange); !errors.Is(err, configureErr) {
				t.Fatalf("configure error = %v", err)
			}
			assertStored(first, second)
			current.configureErr = nil
			extra := second
			extra.Scope, extra.Asset = domain.ScopeUnderlyingAsset, "AAPL"
			if err := put(extra, domain.LimitWriteAdd); err != nil {
				t.Fatal(err)
			}
			assertStored(first, second, extra)
			if builds != 2 {
				t.Fatalf("second barrier of existing mode rebuilt engine: %d", builds)
			}
			if _, err := domain.NewLimitKey(domain.PolicyOrderSizeLimit, "", domain.ScopeBroker, "", "", ""); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("read without mode = %v, want ErrInvalid", err)
			}
			if _, err := n.DeleteLimit(ctx, LimitTarget{Policy: domain.PolicyOrderSizeLimit, Scope: domain.ScopeBroker}, testCaller); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("delete without mode = %v, want ErrInvalid", err)
			}
			assertStored(first, second, extra)
			for i, limit := range []domain.LimitOrderSize{extra, second, first} {
				if _, err := n.DeleteLimit(ctx, LimitTarget{Policy: domain.PolicyOrderSizeLimit, Mode: limit.Mode, Scope: limit.Scope, Asset: limit.Asset}, testCaller); err != nil {
					t.Fatal(err)
				}
				switch i {
				case 0:
					assertStored(first, second)
					if builds != 2 {
						t.Fatalf("non-last mode barrier delete rebuilt: %d", builds)
					}
				case 1:
					assertStored(first)
					if builds != 3 || len(built.OrderSizeLimits) != 1 || built.OrderSizeLimits[0] != first {
						t.Fatalf("last mode barrier delete: builds=%d snapshot=%+v", builds, built.OrderSizeLimits)
					}
				case 2:
					assertStored()
					if builds != 4 || len(built.OrderSizeLimits) != 0 {
						t.Fatalf("empty mode set: builds=%d snapshot=%+v", builds, built.OrderSizeLimits)
					}
				}
			}
		})
	}
}

func TestOrderSizeModeRestoreDelta(t *testing.T) {
	reject := domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, MaxQuantity: "10"}
	review := reject
	review.Mode, review.MaxQuantity = domain.RuleModeReviewWithoutHold, "20"
	changedReject := reject
	changedReject.MaxQuantity = "9"
	extra := reject
	extra.Scope, extra.Asset = domain.ScopeUnderlyingAsset, "AAPL"
	for _, tc := range []struct {
		name             string
		before, after    []domain.LimitOrderSize
		rebuild, changed bool
	}{
		{"add_mode", []domain.LimitOrderSize{reject}, []domain.LimitOrderSize{reject, review}, true, true},
		{"remove_mode", []domain.LimitOrderSize{reject, review}, []domain.LimitOrderSize{reject}, true, true},
		{"replace_mode", []domain.LimitOrderSize{reject}, []domain.LimitOrderSize{review}, true, true},
		{"values_same_modes", []domain.LimitOrderSize{reject, review}, []domain.LimitOrderSize{changedReject, review}, false, true},
		{"more_rows_same_modes", []domain.LimitOrderSize{reject, review}, []domain.LimitOrderSize{reject, review, extra}, false, true},
		{"row_order", []domain.LimitOrderSize{reject, review}, []domain.LimitOrderSize{review, reject}, false, false},
		{"first_mode", nil, []domain.LimitOrderSize{review}, true, true},
		{"last_mode", []domain.LimitOrderSize{review}, nil, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := classifyRestoreRuntimeDelta(
				restoreRuntimeSnapshot{data: backup.Data{OrderSizeLimits: tc.before}},
				restoreRuntimeSnapshot{data: backup.Data{OrderSizeLimits: tc.after}},
			)
			if err != nil {
				t.Fatal(err)
			}
			if plan.rebuild != tc.rebuild || plan.orderSizeChanged != tc.changed {
				t.Fatalf("restore plan = %+v, want rebuild=%t changed=%t", plan, tc.rebuild, tc.changed)
			}
		})
	}
}

func TestLocalNode_OrderSizeModeRollback(t *testing.T) {
	ctx := context.Background()
	n, st := newTestNode(t, newFakeEngine())
	var current *fakeEngine
	n.build = func(snapshot engine.Snapshot) (engine.Engine, error) {
		current = newFakeEngine()
		return fakeBuild(current, new(engine.Snapshot))(snapshot)
	}
	reject := domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, MaxQuantity: "10"}
	review := reject
	review.Mode, review.MaxQuantity = domain.RuleModeReviewWithoutHold, "20"
	assetReject := reject
	assetReject.Scope, assetReject.Asset = domain.ScopeUnderlyingAsset, "AAPL"
	put := func(limit domain.LimitOrderSize) error {
		_, err := n.PutOrderSizeLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountCreate, testCaller)
		return err
	}
	for _, limit := range []domain.LimitOrderSize{reject, review, assetReject} {
		if err := put(limit); err != nil {
			t.Fatal(err)
		}
	}
	before, err := st.ListOrderSizeLimits(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	configureErr := errors.New("mode configure failed")
	current.configureErr = configureErr
	assetReview := assetReject
	assetReview.Mode, assetReview.MaxQuantity = review.Mode, "30"
	if err := put(assetReview); !errors.Is(err, configureErr) {
		t.Fatalf("new mode-key rollback error = %v", err)
	}
	after, err := st.ListOrderSizeLimits(ctx, "")
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("new-key rollback touched neighbour: before=%+v after=%+v err=%v", before, after, err)
	}
	current.configureErr = nil
	if err := put(assetReview); err != nil {
		t.Fatal(err)
	}
	before, err = st.ListOrderSizeLimits(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	current.configureErr = configureErr
	_, err = n.DeleteLimit(ctx, LimitTarget{Policy: domain.PolicyOrderSizeLimit, Mode: assetReview.Mode, Scope: assetReview.Scope, Asset: assetReview.Asset}, testCaller)
	if !errors.Is(err, configureErr) {
		t.Fatalf("delete rollback error = %v", err)
	}
	after, err = st.ListOrderSizeLimits(ctx, "")
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("delete rollback touched neighbour: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestLocalNode_OrderSizeModeBuildFailureIsAtomic(t *testing.T) {
	ctx := context.Background()
	n, st := newTestNode(t, newFakeEngine())
	preparePolicyLifecycleBuild(n)
	reject := domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, MaxQuantity: "10"}
	review := reject
	review.Mode, review.MaxQuantity = domain.RuleModeReviewWithoutHold, "20"
	put := func(limit domain.LimitOrderSize) error {
		_, err := n.PutOrderSizeLimit(ctx, limit, domain.LimitWriteAdd, domain.MissingAccountCreate, testCaller)
		return err
	}
	if err := put(reject); err != nil {
		t.Fatal(err)
	}
	old := n.currentEngine()
	buildErr := errors.New("new mode build failed")
	n.build = func(snapshot engine.Snapshot) (engine.Engine, error) {
		if len(snapshot.OrderSizeLimits) != 2 {
			t.Fatalf("new-mode build lost neighbour: %+v", snapshot.OrderSizeLimits)
		}
		stored, err := st.ListOrderSizeLimits(ctx, "")
		if err != nil || len(stored) != 1 || stored[0] != reject {
			t.Fatalf("store mutated before build: %+v, %v", stored, err)
		}
		return nil, buildErr
	}
	if err := put(review); !errors.Is(err, buildErr) {
		t.Fatalf("new mode failure = %v", err)
	}
	stored, err := st.ListOrderSizeLimits(ctx, "")
	if err != nil || len(stored) != 1 || stored[0] != reject || n.currentEngine() != old {
		t.Fatalf("failed mode build changed state: %+v, %v", stored, err)
	}
	preparePolicyLifecycleBuild(n)
	if err := put(review); err != nil {
		t.Fatal(err)
	}
	old = n.currentEngine()
	n.build = func(snapshot engine.Snapshot) (engine.Engine, error) {
		if len(snapshot.OrderSizeLimits) != 1 || snapshot.OrderSizeLimits[0] != reject {
			t.Fatalf("last-mode delete build lost neighbour: %+v", snapshot.OrderSizeLimits)
		}
		return nil, buildErr
	}
	_, err = n.DeleteLimit(ctx, LimitTarget{Policy: domain.PolicyOrderSizeLimit, Mode: review.Mode, Scope: review.Scope}, testCaller)
	if !errors.Is(err, buildErr) {
		t.Fatalf("last mode delete failure = %v", err)
	}
	stored, err = st.ListOrderSizeLimits(ctx, "")
	if err != nil || len(stored) != 2 || n.currentEngine() != old {
		t.Fatalf("failed delete build changed state: %+v, %v", stored, err)
	}
}

func TestOrderSizeModeAuditDetail(t *testing.T) {
	for _, mode := range []domain.RuleMode{domain.RuleModeReject, domain.RuleModeReviewWithoutHold} {
		limit := domain.LimitOrderSize{Mode: mode, Scope: domain.ScopeBroker, MaxQuantity: "10"}
		key, err := limit.Key()
		if err != nil {
			t.Fatal(err)
		}
		for _, detail := range []domain.AuditDetail{
			setOrderSizeLimitDetail(key, limit),
			deleteLimitDetail(key),
		} {
			if err := detail.Validate(); err != nil {
				t.Fatal(err)
			}
			if detail.Params["scope"] != "broker" || !strings.Contains(detail.Text, "mode="+string(mode)) {
				t.Fatalf("audit detail does not identify mode: %+v", detail)
			}
		}
	}
}

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

func (r *failPolicyLifecycleCommitRealm) DeleteLimit(
	ctx context.Context, key domain.LimitKey,
) error {
	if key.Policy() == domain.PolicyRateLimit && r.deleteRateErr != nil {
		return r.deleteRateErr
	}
	return r.RealmStore.DeleteLimit(ctx, key)
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
		ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), domain.LimitWriteReplace, domain.MissingAccountCreate, testCaller,
	); err != nil {
		t.Fatalf("put first rate: %v", err)
	}
	if _, err := n.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Mode:  domain.RuleModeReject,
		Scope: domain.ScopeBroker, MaxQuantity: "100",
	}, domain.LimitWriteReplace, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("put first order size: %v", err)
	}
	if _, err := n.DeleteLimit(ctx, LimitTarget{
		Policy: domain.PolicyOrderSizeLimit,
		Mode:   domain.RuleModeReject,
		Scope:  domain.ScopeBroker,
	}, testCaller); err != nil {
		t.Fatalf("delete last order size: %v", err)
	}
	if _, err := n.DeleteLimit(ctx, LimitTarget{Mode: domain.RuleModeReject,
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
	initial := domain.LimitOrderSize{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, MaxQuantity: "50"}
	if _, err := n.PutOrderSizeLimit(ctx, initial, domain.LimitWriteReplace, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutOrderSizeLimit initial: %v", err)
	}
	current.configureErr = configureErr
	builds := 0
	n.build = func(engine.Snapshot) (engine.Engine, error) {
		builds++
		return newFakeEngine(), nil
	}

	sink, err := n.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Mode:  domain.RuleModeReject,
		Scope: domain.ScopeBroker, MaxQuantity: "100",
	}, domain.LimitWriteReplace, domain.MissingAccountCreate, testCaller)
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

func TestLocalNode_LivePolicyConfigurationQuiescesAccountChains(t *testing.T) {
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
		accountID, err := eng.AccountID("acc-1")
		if err != nil {
			laneDone <- err
			return
		}
		type laneState struct{}
		builder := asyncengine.Chain(
			accountID,
			func(context.Context) (*laneState, error) {
				return &laneState{}, nil
			},
		)
		builder.Then(func(context.Context, *laneState) error {
			close(laneEntered)
			<-laneRelease
			return nil
		})
		_, err = builder.Run(
			context.Background(), eng.AsyncEngine(),
		).Await(context.Background())
		laneDone <- err
	}()
	waitLivePolicySignal(t, laneEntered, "account lane did not start")

	configureDone := make(chan error, 1)
	go func() {
		_, err := n.PutRateLimit(
			context.Background(),
			rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
			domain.LimitWriteReplace,
			domain.MissingAccountCreate, testCaller,
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
	n := newTestNodeWithStore(t, st, eng, func(err error) {
		fatalErr = err
	})
	if err := n.realm.PutRateLimit(
		ctx, rateLimit(domain.ScopeBroker, "", "", 50, time.Second),
	); err != nil {
		t.Fatalf("seed rate limit: %v", err)
	}

	sink, err := n.PutRateLimit(
		ctx,
		rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
		domain.LimitWriteReplace,
		domain.MissingAccountCreate, testCaller,
	)
	if !errors.Is(err, auditErr) {
		t.Fatalf("PutRateLimit error = %v, want audit failure", err)
	}
	if sink != nil {
		t.Fatalf("PutRateLimit sink = %T, want nil without rebuild", sink)
	}
	if !errors.Is(fatalErr, auditErr) {
		t.Fatalf("fatal policy audit error: got %v, want %v", fatalErr, auditErr)
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
		ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), domain.LimitWriteReplace, domain.MissingAccountCreate, testCaller,
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
	n := newTestNodeWithStore(t, st, old, failOnFatal(t))
	next := newFakeEngine()
	n.build = fakeBuild(next, new(engine.Snapshot))

	_, err := n.PutRateLimit(
		ctx, rateLimit(domain.ScopeBroker, "", "", 100, time.Second), domain.LimitWriteReplace, domain.MissingAccountCreate, testCaller,
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
	n := newTestNodeWithStore(t, st, old, failOnFatal(t))
	current := newFakeEngine()
	n.build = fakeBuild(current, new(engine.Snapshot))
	limit := rateLimit(domain.ScopeBroker, "", "", 100, time.Second)
	if _, err := n.PutRateLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountCreate, testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	commitErr := errors.New("delete rate limit failed")
	wrapped.deleteRateErr = commitErr
	next := newFakeEngine()
	n.build = fakeBuild(next, new(engine.Snapshot))
	_, err := n.DeleteLimit(ctx, LimitTarget{Mode: domain.RuleModeReject,
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
