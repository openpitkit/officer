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
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

type blockingStopMarketDataRuntime struct {
	*fakeMarketDataRuntime
	mu           sync.Mutex
	stopCalls    int
	stopEntered  chan struct{}
	firstRelease chan struct{}
}

func (r *blockingStopMarketDataRuntime) Stop() {
	r.mu.Lock()
	r.stopCalls++
	call := r.stopCalls
	r.mu.Unlock()
	r.stopEntered <- struct{}{}
	if call == 1 {
		<-r.firstRelease
	}
}

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
	if err := svc.PutOrderSizeLimit(ctx, bad, domain.MissingAccountCreate); !errors.Is(err, domain.ErrInvalid) {
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
	if err := svc.PutRateLimit(ctx, limit, domain.MissingAccountCreate); err != nil {
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
	if err := svc.PutRateLimit(ctx, limit, domain.MissingAccountCreate); err != nil {
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
	if err := svc.PutRateLimit(ctx, limit, domain.MissingAccountCreate); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	if md.stops != 1 || md.restarts != 1 || md.sink != sink {
		t.Fatalf("market-data reconnect = stops:%d restarts:%d sink:%T",
			md.stops, md.restarts, md.sink)
	}
}

func TestService_PutLimitSerializesMarketDataReconnect(t *testing.T) {
	md := &blockingStopMarketDataRuntime{
		fakeMarketDataRuntime: &fakeMarketDataRuntime{},
		stopEntered:           make(chan struct{}, 2),
		firstRelease:          make(chan struct{}),
	}
	released := false
	t.Cleanup(func() {
		if !released {
			close(md.firstRelease)
		}
	})
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.restoreSink = &backendTestSink{}
	limit := domain.LimitRate{
		Scope: domain.ScopeBroker, MaxOrders: 100, Window: time.Second,
	}
	putDone := make(chan error, 1)
	go func() {
		putDone <- svc.PutRateLimit(context.Background(), limit, domain.MissingAccountCreate)
	}()
	select {
	case <-md.stopEntered:
	case <-time.After(time.Second):
		t.Fatal("PutRateLimit did not enter market-data reconnect")
	}

	restartDone := make(chan error, 1)
	go func() {
		restartDone <- svc.RestartMarketData(context.Background())
	}()
	select {
	case <-md.stopEntered:
		t.Fatal("concurrent market-data reconnect was not serialized")
	case <-time.After(20 * time.Millisecond):
	}
	close(md.firstRelease)
	released = true
	if err := <-putDone; err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	select {
	case <-md.stopEntered:
	case <-time.After(time.Second):
		t.Fatal("RestartMarketData did not continue after PutRateLimit reconnect")
	}
	if err := <-restartDone; err != nil {
		t.Fatalf("RestartMarketData: %v", err)
	}
}

func TestService_PutLimitSerializesNodeMutationWithMarketDataReconnect(t *testing.T) {
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(md)
	fn.restoreSink = &backendTestSink{}
	rateEntered := make(chan struct{})
	rateRelease := make(chan struct{})
	orderEntered := make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(rateRelease)
		}
	})
	fn.putRateLimitHook = func() {
		close(rateEntered)
		<-rateRelease
	}
	fn.putOrderSizeLimitHook = func() {
		close(orderEntered)
	}

	rateDone := make(chan error, 1)
	go func() {
		rateDone <- svc.PutRateLimit(context.Background(), domain.LimitRate{
			Scope: domain.ScopeBroker, MaxOrders: 100, Window: time.Second,
		}, domain.MissingAccountCreate)
	}()
	select {
	case <-rateEntered:
	case <-time.After(time.Second):
		t.Fatal("PutRateLimit did not enter node mutation")
	}

	orderDone := make(chan error, 1)
	go func() {
		orderDone <- svc.PutOrderSizeLimit(context.Background(), domain.LimitOrderSize{
			Scope:       domain.ScopeAccountAsset,
			Account:     "acc-1",
			Asset:       "AAPL",
			MaxQuantity: "1",
		}, domain.MissingAccountCreate)
	}()
	select {
	case <-orderEntered:
		t.Fatal("concurrent limit mutation reached node before first reconnect")
	case <-time.After(20 * time.Millisecond):
	}

	close(rateRelease)
	released = true
	if err := <-rateDone; err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	select {
	case <-orderEntered:
	case <-time.After(time.Second):
		t.Fatal("PutOrderSizeLimit did not continue after first reconnect")
	}
	if err := <-orderDone; err != nil {
		t.Fatalf("PutOrderSizeLimit: %v", err)
	}
}

func TestService_DeleteLimitValidatesTarget(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := context.Background()

	// broker scope is not allowed for SpotFunds P&L bounds; target validation must reject.
	bad := node.LimitTarget{
		Policy: domain.PolicySpotFundsPnlBoundsKillSwitch,
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
