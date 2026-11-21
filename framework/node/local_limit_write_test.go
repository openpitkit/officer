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
	"sync/atomic"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/store"
)

type limitWriteCase struct {
	name   string
	values [2]AccountLimits
	put    func(context.Context, Node, AccountLimits, domain.LimitWrite, domain.Caller) (marketdata.Sink, error)
}

func TestCheckLimitWriteErrorText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		target LimitTarget
		key    string
	}{
		{
			name:   "broker",
			target: LimitTarget{Policy: domain.PolicyRateLimit, Scope: domain.ScopeBroker},
			key:    "rate_limit at broker",
		},
		{
			name:   "account",
			target: LimitTarget{Policy: domain.PolicyRateLimit, Scope: domain.ScopeAccount, Account: "acc-1"},
			key:    `rate_limit at account "acc-1"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, refusal := range []struct {
				write  domain.LimitWrite
				exists bool
				want   error
			}{
				{domain.LimitWriteAdd, true, domain.ErrLimitExists},
				{domain.LimitWriteChange, false, domain.ErrNotFound},
			} {
				err := checkLimitWrite(refusal.write, tc.target, refusal.exists)
				want := tc.key + ": " + refusal.want.Error()
				if !errors.Is(err, refusal.want) || err.Error() != want {
					t.Fatalf("intent %q error = %v, want %q", refusal.write, err, want)
				}
			}
		})
	}
}

func limitWriteCases() []limitWriteCase {
	cases := []limitWriteCase{
		{
			name: "rate",
			values: [2]AccountLimits{
				{RateLimits: []domain.LimitRate{rateLimit(domain.ScopeAccountAsset, "intent-account", "INTENT", 10, time.Second)}},
				{RateLimits: []domain.LimitRate{rateLimit(domain.ScopeAccountAsset, "intent-account", "INTENT", 20, time.Second)}},
			},
			put: func(ctx context.Context, n Node, value AccountLimits, write domain.LimitWrite, caller domain.Caller) (marketdata.Sink, error) {
				return n.PutRateLimit(ctx, value.RateLimits[0], write, domain.MissingAccountCreate, caller)
			},
		},
		{
			name: "order-size",
			values: [2]AccountLimits{
				{OrderSizeLimits: []domain.LimitOrderSize{{Scope: domain.ScopeAccountUnderlyingAsset, Account: "intent-account", Asset: "INTENT", MaxQuantity: "10"}}},
				{OrderSizeLimits: []domain.LimitOrderSize{{Scope: domain.ScopeAccountUnderlyingAsset, Account: "intent-account", Asset: "INTENT", MaxQuantity: "20"}}},
			},
			put: func(ctx context.Context, n Node, value AccountLimits, write domain.LimitWrite, caller domain.Caller) (marketdata.Sink, error) {
				return n.PutOrderSizeLimit(ctx, value.OrderSizeLimits[0], write, domain.MissingAccountCreate, caller)
			},
		},
		{
			name: "position",
			values: [2]AccountLimits{
				{PositionLimits: []domain.LimitPosition{{Scope: domain.ScopeAccountAsset, Account: "intent-account", Asset: "INTENT", MaxPosition: "10"}}},
				{PositionLimits: []domain.LimitPosition{{Scope: domain.ScopeAccountAsset, Account: "intent-account", Asset: "INTENT", MaxPosition: "20"}}},
			},
			put: func(ctx context.Context, n Node, value AccountLimits, write domain.LimitWrite, caller domain.Caller) (marketdata.Sink, error) {
				return n.PutPositionLimit(ctx, value.PositionLimits[0], write, domain.MissingAccountCreate, caller)
			},
		},
	}
	for _, axis := range []domain.SpotFundsPnlAxis{
		domain.SpotFundsPnlAxisRealized,
		domain.SpotFundsPnlAxisUnrealized,
		domain.SpotFundsPnlAxisTotal,
	} {
		cases = append(cases, limitWriteCase{
			name: "pnl-" + string(axis),
			values: [2]AccountLimits{
				{SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{{Axis: axis, Scope: domain.ScopeAccount, Account: "intent-account", Currency: "USD", LowerBound: "-10"}}},
				{SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{{Axis: axis, Scope: domain.ScopeAccount, Account: "intent-account", Currency: "USD", LowerBound: "-20"}}},
			},
			put: func(ctx context.Context, n Node, value AccountLimits, write domain.LimitWrite, caller domain.Caller) (marketdata.Sink, error) {
				return n.PutSpotFundsPnlBoundsLimit(ctx, value.SpotFundsPnlBoundsLimits[0], write, domain.MissingAccountCreate, caller)
			},
		})
	}
	return cases
}

func newLimitWriteNode(t *testing.T) (*localNode, *rebuildProbe) {
	t.Helper()
	n, probe := newLimitNode(t, AccountLimits{})
	build := fakeBuild(newFakeEngine(), &probe.last)
	n.build = func(snapshot engine.Snapshot) (engine.Engine, error) {
		probe.builds++
		return build(snapshot)
	}
	if err := n.realm.SetGroupCurrency(context.Background(), "", "USD"); err != nil {
		t.Fatal(err)
	}
	return n, probe
}

func requireAppliedLimit(
	t *testing.T, n *localNode, probe *rebuildProbe, want AccountLimits,
) {
	t.Helper()
	eng := n.currentEngine().(*fakeEngine)
	got := engine.LimitSet{
		RateLimits: probe.last.RateLimits, OrderSizeLimits: probe.last.OrderSizeLimits,
		PositionLimits:           probe.last.PositionLimits,
		SpotFundsPnlBoundsLimits: probe.last.SpotFundsPnlBoundsLimits,
	}
	if len(eng.configureCalls) != 0 {
		got = eng.configureCalls[len(eng.configureCalls)-1].limits
	}
	expected := engine.LimitSet{
		RateLimits: want.RateLimits, OrderSizeLimits: want.OrderSizeLimits,
		PositionLimits:           want.PositionLimits,
		SpotFundsPnlBoundsLimits: want.SpotFundsPnlBoundsLimits,
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("engine limits = %+v, want %+v", got, expected)
	}
}

func TestLocalNode_LimitWriteIntent(t *testing.T) {
	t.Parallel()
	for _, tc := range limitWriteCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n, probe := newLimitWriteNode(t)
			refuse := func(value AccountLimits, write domain.LimitWrite, caller domain.Caller, want error) {
				t.Helper()
				unchanged := refusalKeepsRiskState(t, n, probe)
				eng := n.currentEngine().(*fakeEngine)
				calls := len(eng.configureCalls)
				sink, err := tc.put(context.Background(), n, value, write, caller)
				if !errors.Is(err, want) || sink != nil {
					t.Fatalf("refusal = (%T, %v), want (nil, %v)", sink, err, want)
				}
				if want != domain.ErrInvalid && !strings.Contains(err.Error(), "intent-account") {
					t.Errorf("refusal %q does not name its key", err)
				}
				unchanged()
				if len(eng.configureCalls) != calls {
					t.Fatal("refused write configured the engine")
				}
			}
			for _, write := range []domain.LimitWrite{domain.LimitWriteChange, "", "unknown"} {
				want := domain.ErrInvalid
				if write == domain.LimitWriteChange {
					want = domain.ErrNotFound
				}
				refuse(tc.values[0], write, testCaller, want)
			}
			if _, err := tc.put(context.Background(), n, tc.values[0], domain.LimitWriteAdd, testCaller); err != nil {
				t.Fatalf("Add new key: %v", err)
			}
			requireStored(t, n, tc.values[0])
			requireAppliedLimit(t, n, probe, tc.values[0])
			for _, caller := range []domain.Caller{testCaller, agentCaller} {
				refuse(tc.values[1], domain.LimitWriteAdd, caller, domain.ErrLimitExists)
				requireStored(t, n, tc.values[0])
			}
			if _, err := tc.put(context.Background(), n, tc.values[1], domain.LimitWriteChange, testCaller); err != nil {
				t.Fatalf("Change existing key: %v", err)
			}
			requireStored(t, n, tc.values[1])
			requireAppliedLimit(t, n, probe, tc.values[1])
		})
	}
}

type limitWriteContextKey struct{}

// Hold the first barrier snapshot while the second writer attempts its read.
// Context IDs distinguish writer reads from snapshot reads in the same write.
type concurrentLimitRealm struct {
	store.RealmStore
	firstRead      chan struct{}
	secondStarted  chan struct{}
	secondRead     chan struct{}
	firstObserved  atomic.Bool
	secondObserved atomic.Bool
	overlap        atomic.Bool
}

func (r *concurrentLimitRealm) observeBarrierRead(ctx context.Context) {
	switch ctx.Value(limitWriteContextKey{}) {
	case 0:
		if r.firstObserved.CompareAndSwap(false, true) {
			close(r.firstRead)
			<-r.secondStarted
			select {
			case <-r.secondRead:
				r.overlap.Store(true)
			case <-time.After(250 * time.Millisecond):
				// The mutation lock must keep the second read out of this window.
			}
		}
	case 1:
		if r.secondObserved.CompareAndSwap(false, true) {
			close(r.secondRead)
		}
	}
}

func (r *concurrentLimitRealm) ListRateLimits(ctx context.Context, account domain.AccountID) ([]domain.LimitRate, error) {
	limits, err := r.RealmStore.ListRateLimits(ctx, account)
	r.observeBarrierRead(ctx)
	return limits, err
}

func (r *concurrentLimitRealm) ListOrderSizeLimits(ctx context.Context, account domain.AccountID) ([]domain.LimitOrderSize, error) {
	limits, err := r.RealmStore.ListOrderSizeLimits(ctx, account)
	r.observeBarrierRead(ctx)
	return limits, err
}

func (r *concurrentLimitRealm) ListPositionLimits(ctx context.Context, account domain.AccountID) ([]domain.LimitPosition, error) {
	limits, err := r.RealmStore.ListPositionLimits(ctx, account)
	r.observeBarrierRead(ctx)
	return limits, err
}

func (r *concurrentLimitRealm) ListSpotFundsPnlBoundsLimits(ctx context.Context, account domain.AccountID) ([]domain.LimitSpotFundsPnlBounds, error) {
	limits, err := r.RealmStore.ListSpotFundsPnlBoundsLimits(ctx, account)
	r.observeBarrierRead(ctx)
	return limits, err
}

func TestLocalNode_ConcurrentLimitAdds(t *testing.T) {
	t.Parallel()
	for _, tc := range limitWriteCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n, probe := newLimitWriteNode(t)
			builds := probe.builds
			realm := &concurrentLimitRealm{
				RealmStore: n.realm,
				firstRead:  make(chan struct{}), secondStarted: make(chan struct{}),
				secondRead: make(chan struct{}),
			}
			n.realm = realm
			type result struct {
				value int
				err   error
			}
			results := make(chan result, 2)
			go func() {
				ctx := context.WithValue(context.Background(), limitWriteContextKey{}, 0)
				_, err := tc.put(ctx, n, tc.values[0], domain.LimitWriteAdd, testCaller)
				results <- result{0, err}
			}()
			select {
			case <-realm.firstRead:
			case r := <-results:
				t.Fatalf("first Add returned before its barrier read: %v", r.err)
			case <-time.After(10 * time.Second):
				t.Fatal("first Add never reached its barrier read")
			}
			go func() {
				ctx := context.WithValue(context.Background(), limitWriteContextKey{}, 1)
				close(realm.secondStarted)
				_, err := tc.put(ctx, n, tc.values[1], domain.LimitWriteAdd, testCaller)
				results <- result{1, err}
			}()
			winner, successes, conflicts := -1, 0, 0
			for range 2 {
				r := <-results
				switch {
				case r.err == nil:
					winner = r.value
					successes++
				case errors.Is(r.err, domain.ErrLimitExists):
					conflicts++
				default:
					t.Errorf("concurrent Add: %v", r.err)
				}
			}
			if realm.overlap.Load() {
				t.Fatal("second barrier read entered while the first writer held its barrier snapshot")
			}
			if successes != 1 || conflicts != 1 {
				t.Fatalf("concurrent Adds: successes=%d conflicts=%d, want 1 each", successes, conflicts)
			}
			requireStored(t, n, tc.values[winner])
			requireAppliedLimit(t, n, probe, tc.values[winner])
			audit, err := n.realm.ListAudit(context.Background(), 100)
			if err != nil {
				t.Fatal(err)
			}
			writes := 0
			for _, entry := range audit {
				if entry.Action == domain.AuditActionSetLimit {
					writes++
				}
			}
			if writes != 1 {
				t.Fatalf("set_limit audit entries = %d, want 1", writes)
			}
			calls := len(n.currentEngine().(*fakeEngine).configureCalls)
			if tc.name == "rate" || tc.name == "order-size" {
				if probe.builds != builds+1 || calls != 0 {
					t.Fatalf("first barrier rebuilds = %d, configure calls = %d", probe.builds-builds, calls)
				}
			} else if calls != 1 {
				t.Fatalf("engine configure calls = %d, want 1", calls)
			}
		})
	}
}

func TestLocalNode_LimitAddPrecedesPnlCurrencyCheck(t *testing.T) {
	t.Parallel()
	for _, tc := range limitWriteCases() {
		if len(tc.values[0].SpotFundsPnlBoundsLimits) == 0 {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n, probe := newLimitWriteNode(t)
			eng := n.currentEngine().(*fakeEngine)
			first := tc.values[0].SpotFundsPnlBoundsLimits[0]
			if _, err := n.CreateAccount(context.Background(), domain.Account{
				Code: first.Account,
			}, testCaller); err != nil {
				t.Fatal(err)
			}
			other := first
			other.Currency = "EUR"
			unchanged := refusalKeepsRiskState(t, n, probe)
			calls := len(eng.configureCalls)
			sink, err := n.PutSpotFundsPnlBoundsLimit(context.Background(), other,
				domain.LimitWriteChange, domain.MissingAccountReject, testCaller)
			if !errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrPnlBoundCurrencyMismatch) || sink != nil {
				t.Fatalf("missing Change in EUR = (%T, %v), want (nil, ErrNotFound)", sink, err)
			}
			unchanged()
			if len(eng.configureCalls) != calls {
				t.Fatal("missing Change configured the engine")
			}
			if _, err := n.PutSpotFundsPnlBoundsLimit(context.Background(), first,
				domain.LimitWriteAdd, domain.MissingAccountCreate, testCaller); err != nil {
				t.Fatal(err)
			}
			unchanged = refusalKeepsRiskState(t, n, probe)
			calls = len(eng.configureCalls)
			sink, err = n.PutSpotFundsPnlBoundsLimit(context.Background(), other,
				domain.LimitWriteAdd, domain.MissingAccountCreate, testCaller)
			if !errors.Is(err, domain.ErrLimitExists) || errors.Is(err, domain.ErrPnlBoundCurrencyMismatch) || sink != nil {
				t.Fatalf("duplicate Add in EUR = %v, want ErrLimitExists", err)
			}
			unchanged()
			if len(eng.configureCalls) != calls {
				t.Fatalf("currency-conflicting duplicate Add had effects: %v", err)
			}
			requireStored(t, n, tc.values[0])
		})
	}
}
