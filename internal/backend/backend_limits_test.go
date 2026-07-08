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

package backend_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

func TestService_PutLimitValidatesBeforeRouting(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	// account scope on order_size_limit is not allowed - validation must reject
	// before the node is touched.
	bad := domain.LimitOrderSize{
		Scope:       domain.ScopeAccount,
		Account:     "acc-1",
		MaxQuantity: "1",
	}
	if err := svc.PutOrderSizeLimit(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if len(fn.putOrderSizeLimitCalls) != 0 {
		t.Fatalf("invalid limit must not reach the node")
	}
}

func TestService_PutLimitAcceptsNonExistentAccount(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	// Account "acc-new" does not exist in the fake node (getAccountErr is not set,
	// but no account record exists either). PutRateLimit must succeed regardless: a
	// policy rule may be created before the account is ever registered.
	limit := domain.LimitRate{
		Scope:     domain.ScopeAccountAsset,
		Account:   "acc-new",
		Asset:     "AAPL",
		MaxOrders: 100,
		Window:    time.Second,
	}
	if err := svc.PutRateLimit(ctx, limit); err != nil {
		t.Fatalf("PutRateLimit for non-existent account: %v", err)
	}
	if len(fn.putRateLimitCalls) != 1 {
		t.Fatalf("want 1 PutRateLimit call, got %d", len(fn.putRateLimitCalls))
	}
}

func TestService_PutLimitForwardsTypedBarrier(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	limit := domain.LimitRate{
		Scope:     domain.ScopeBroker,
		MaxOrders: 100,
		Window:    time.Second,
	}
	if err := svc.PutRateLimit(ctx, limit); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	if len(fn.putRateLimitCalls) != 1 {
		t.Fatalf("want one PutRateLimit call")
	}
	if fn.putRateLimitCalls[0].Scope != domain.ScopeBroker ||
		fn.putRateLimitCalls[0].MaxOrders != 100 {
		t.Fatalf("barrier not forwarded: %+v", fn.putRateLimitCalls[0])
	}
}

func TestService_PutLimitReconnectsMarketDataOnRebuild(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	ctx := context.Background()

	limit := domain.LimitRate{
		Scope:     domain.ScopeBroker,
		MaxOrders: 100,
		Window:    time.Second,
	}
	if err := svc.PutRateLimit(ctx, limit); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	if md.stops != 1 || md.restarts != 1 || md.sink != sink {
		t.Fatalf("market-data reconnect = stops:%d restarts:%d sink:%T",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_DeleteLimitValidatesTarget(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	// broker scope is not allowed for pnl_bounds; target validation must reject.
	bad := node.LimitTarget{
		Policy: domain.PolicyPnlBoundsKillSwitch,
		Scope:  domain.ScopeBroker,
	}
	if err := svc.DeleteLimit(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if len(fn.deleteLimitCalls) != 0 {
		t.Fatalf("invalid target must not reach the node")
	}

	good := node.LimitTarget{
		Policy: domain.PolicyRateLimit,
		Scope:  domain.ScopeBroker,
	}
	if err := svc.DeleteLimit(ctx, good); err != nil {
		t.Fatalf("DeleteLimit: %v", err)
	}
	if len(fn.deleteLimitCalls) != 1 {
		t.Fatalf("valid delete must route to node")
	}
	if fn.deleteLimitCalls[0].Policy != domain.PolicyRateLimit {
		t.Fatalf("delete target not forwarded: %+v", fn.deleteLimitCalls[0])
	}
}

func TestService_DeleteLimitReconnectsMarketDataOnRebuild(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	ctx := context.Background()

	target := node.LimitTarget{
		Policy: domain.PolicyRateLimit,
		Scope:  domain.ScopeBroker,
	}
	if err := svc.DeleteLimit(ctx, target); err != nil {
		t.Fatalf("DeleteLimit: %v", err)
	}
	if md.stops != 1 || md.restarts != 1 || md.sink != sink {
		t.Fatalf("market-data reconnect = stops:%d restarts:%d sink:%T",
			md.stops, md.restarts, md.sink)
	}
}
