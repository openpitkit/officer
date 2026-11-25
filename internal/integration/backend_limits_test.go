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

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/httpapi"
)

func TestRESTDeleteOrderSizeLimitMode(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		status      int
		remaining   domain.RuleMode
	}{
		{"review", "policy=order_size_limit&scope=broker&mode=review_without_hold", http.StatusNoContent, domain.RuleModeReject},
		{"default reject", "policy=order_size_limit&scope=broker", http.StatusNoContent, domain.RuleModeReviewWithoutHold},
		{"empty reject", "policy=order_size_limit&scope=broker&mode=", http.StatusNoContent, domain.RuleModeReviewWithoutHold},
		{"foreign mode", "policy=spot_funds_limit_mode&scope=global&mode=reject", http.StatusUnprocessableEntity, ""},
		{"unknown mode", "policy=order_size_limit&scope=broker&mode=unknown", http.StatusUnprocessableEntity, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, realm := newLocalNode(t)
			svc := newOfficerService(t, target, nil, nil)
			for _, mode := range []domain.RuleMode{domain.RuleModeReject, domain.RuleModeReviewWithoutHold} {
				if _, err := svc.PutOrderSizeLimit(systemCtx(), domain.LimitOrderSize{
					Mode: mode, Scope: domain.ScopeBroker, MaxQuantity: "10",
				}, domain.LimitWriteAdd, domain.MissingAccountReject); err != nil {
					t.Fatal(err)
				}
			}
			registry := httpapi.NewRouteRegistry(svc, nil, func(context.Context) (time.Time, int, bool, error) {
				return time.Time{}, 0, false, nil
			})
			var handler http.Handler
			for _, route := range registry.Routes() {
				if route.Method == http.MethodDelete && route.Pattern == "/limits" {
					handler = route.Handler
				}
			}
			if handler == nil {
				t.Fatal("DELETE /limits route is missing")
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequestWithContext(systemCtx(), http.MethodDelete, "/limits?"+tc.query, nil))
			if rec.Code != tc.status {
				t.Fatalf("DELETE %s = %d, want %d: %s", tc.query, rec.Code, tc.status, rec.Body)
			}
			if tc.status == http.StatusUnprocessableEntity {
				var problem struct {
					Errors []struct{ Code, Pointer string } `json:"errors"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
					t.Fatal(err)
				}
				if len(problem.Errors) != 1 || problem.Errors[0].Code != "validation" || problem.Errors[0].Pointer != "/mode" {
					t.Fatalf("delete validation = %+v", problem)
				}
			}
			limits, err := realm.ListOrderSizeLimits(systemCtx(), "")
			if err != nil {
				t.Fatal(err)
			}
			if tc.status == http.StatusNoContent {
				if len(limits) != 1 || limits[0].Mode != tc.remaining {
					t.Fatalf("DELETE %s left %+v, want only mode %s", tc.query, limits, tc.remaining)
				}
			} else if len(limits) != 2 {
				t.Fatalf("refused delete changed barriers: %+v", limits)
			}
		})
	}
}

func TestService_DeleteLimitModeValidation(t *testing.T) {
	for _, target := range []node.LimitTarget{
		{Policy: domain.PolicyOrderSizeLimit, Scope: domain.ScopeBroker, Mode: "unknown"},
		{Policy: domain.PolicyOrderSizeLimit, Scope: domain.ScopeBroker},
		{Policy: domain.PolicySpotFundsLimitMode, Scope: domain.ScopeGlobal, Mode: domain.RuleModeReject},
	} {
		svc, fn := newTestService(t)
		err := svc.DeleteLimit(systemCtx(), target)
		public := httpx.DescribeErr(err)
		if public.Status != http.StatusUnprocessableEntity || public.Code != "validation" || domain.ValidationPointer(err) != "/mode" || len(fn.deleteLimitCalls) != 0 {
			t.Fatalf("mode validation for %+v = %+v / %v; node calls=%+v", target, public, err, fn.deleteLimitCalls)
		}
	}
}

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
	svc, fn := newTestService(t)
	ctx := systemCtx()

	// account scope on order_size_limit is not allowed - validation must reject
	// before the node is touched.
	bad := domain.LimitOrderSize{
		Mode:        domain.RuleModeReject,
		Scope:       domain.ScopeAccount,
		Account:     "acc-1",
		MaxQuantity: "1",
	}
	if _, err := svc.PutOrderSizeLimit(ctx, bad, domain.LimitWriteReplace, domain.MissingAccountCreate); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if len(fn.putOrderSizeLimitCalls) != 0 {
		t.Fatalf("invalid limit must not reach the node")
	}
	for _, tc := range []struct {
		name, maxPosition string
		scope             domain.LimitScope
		missing           domain.MissingAccountPolicy
	}{
		{"scope", "1", domain.ScopeAccount, domain.MissingAccountCreate},
		{"negative", "-1", domain.ScopeAccountAsset, domain.MissingAccountCreate},
		{"decimal", "no-number", domain.ScopeAccountAsset, domain.MissingAccountCreate},
		{"missing policy", "1", domain.ScopeAccountAsset, ""},
	} {
		t.Run("position/"+tc.name, func(t *testing.T) {
			limit := domain.LimitPosition{Mode: domain.RuleModeReject, Scope: tc.scope, Account: "acc-1", Asset: "BTC", MaxPosition: tc.maxPosition}
			if _, err := svc.PutPositionLimit(ctx, limit, domain.LimitWriteReplace, tc.missing); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("PutPositionLimit = %v, want ErrInvalid", err)
			}
			if len(fn.putPositionLimitCalls) != 0 {
				t.Fatalf("invalid position limit reached the node: %+v", fn.putPositionLimitCalls)
			}
		})
	}
}

func TestService_PutLimitAcceptsNonExistentAccount(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	ctx := systemCtx()

	// Account "acc-new" does not exist in the fake node (getAccountErr is not set,
	// but no account record exists either). PutRateLimit must succeed regardless: a
	// policy rule may be created before the account is ever registered.
	limit := domain.LimitRate{Mode: domain.RuleModeReject,
		Scope:     domain.ScopeAccountAsset,
		Account:   "acc-new",
		Asset:     "AAPL",
		MaxOrders: 100,
		Window:    time.Second,
	}
	if _, err := svc.PutRateLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountCreate); err != nil {
		t.Fatalf("PutRateLimit for non-existent account: %v", err)
	}
	if len(fn.putRateLimitCalls) != 1 {
		t.Fatalf("want 1 PutRateLimit call, got %d", len(fn.putRateLimitCalls))
	}
	position := domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "acc-new", Asset: "BTC", MaxPosition: "0"}
	if _, err := svc.PutPositionLimit(ctx, position, domain.LimitWriteReplace, domain.MissingAccountCreate); err != nil {
		t.Fatalf("PutPositionLimit for non-existent account: %v", err)
	}
	if len(fn.putPositionLimitCalls) != 1 || fn.putPositionLimitCalls[0] != position {
		t.Fatalf("position barrier not forwarded: %+v", fn.putPositionLimitCalls)
	}
}

func TestService_PutLimitForwardsTypedBarrier(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	ctx := systemCtx()

	limit := domain.LimitRate{Mode: domain.RuleModeReject,
		Scope:     domain.ScopeBroker,
		MaxOrders: 100,
		Window:    time.Second,
	}
	if _, err := svc.PutRateLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountCreate); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	if len(fn.putRateLimitCalls) != 1 {
		t.Fatalf("want one PutRateLimit call")
	}
	if fn.putRateLimitCalls[0].Scope != domain.ScopeBroker ||
		fn.putRateLimitCalls[0].MaxOrders != 100 {
		t.Fatalf("barrier not forwarded: %+v", fn.putRateLimitCalls[0])
	}
	for _, missing := range []domain.MissingAccountPolicy{domain.MissingAccountCreate, domain.MissingAccountReject} {
		position := domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "BTC", MaxPosition: "123.000000000000000001"}
		if _, err := svc.PutPositionLimit(ctx, position, domain.LimitWriteReplace, missing); err != nil {
			t.Fatalf("PutPositionLimit: %v", err)
		}
		if got := fn.putPositionLimitCalls[len(fn.putPositionLimitCalls)-1]; got != position {
			t.Fatalf("position barrier = %+v, want %+v", got, position)
		}
		if got := fn.missingAccountCalls[len(fn.missingAccountCalls)-1]; got != missing {
			t.Fatalf("missing account = %q, want %q", got, missing)
		}
	}
}

func TestService_PutPositionLimitScopesAccountAndMissingPolicy(t *testing.T) {
	t.Parallel()
	scope := &recordingScope{account: scopeResolvedAccount, policy: domain.MissingAccountReject}
	svc, fn, _ := newScopeTestService(t, scope)
	limit := domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: scopeClientAccount, Asset: "BTC", MaxPosition: "10"}
	if _, err := svc.PutPositionLimit(systemCtx(), limit, domain.LimitWriteReplace, domain.MissingAccountCreate); err != nil {
		t.Fatal(err)
	}
	limit.Account = scopeResolvedAccount
	if len(fn.putPositionLimitCalls) != 1 || fn.putPositionLimitCalls[0] != limit {
		t.Fatalf("scoped barrier = %+v, want %+v", fn.putPositionLimitCalls, limit)
	}
	if len(fn.missingAccountCalls) != 1 || fn.missingAccountCalls[0] != domain.MissingAccountReject {
		t.Fatalf("scoped missing account = %v, want reject", fn.missingAccountCalls)
	}
}

func TestService_PutLimitReconnectsMarketDataOnRebuild(t *testing.T) {
	t.Parallel()
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(t, md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	ctx := systemCtx()

	limit := domain.LimitRate{Mode: domain.RuleModeReject,
		Scope:     domain.ScopeBroker,
		MaxOrders: 100,
		Window:    time.Second,
	}
	if _, err := svc.PutRateLimit(ctx, limit, domain.LimitWriteReplace, domain.MissingAccountCreate); err != nil {
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
	svc, fn := newTestServiceWithMarketDataRuntime(t, md)
	fn.restoreSink = &backendTestSink{}
	limit := domain.LimitRate{Mode: domain.RuleModeReject,
		Scope: domain.ScopeBroker, MaxOrders: 100, Window: time.Second,
	}
	putDone := make(chan error, 1)
	go func() {
		_, err := svc.PutRateLimit(systemCtx(), limit, domain.LimitWriteReplace, domain.MissingAccountCreate)
		putDone <- err
	}()
	select {
	case <-md.stopEntered: // PutRateLimit entered market-data reconnect.
	case err := <-putDone:
		t.Fatalf("PutRateLimit returned before market-data reconnect: %v", err)
	}

	restartDone := make(chan error, 1)
	go func() {
		restartDone <- svc.RestartMarketData(systemCtx())
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
	if err := <-restartDone; err != nil {
		t.Fatalf("RestartMarketData: %v", err)
	}
	select {
	case <-md.stopEntered:
	default:
		t.Fatal("RestartMarketData returned without market-data reconnect")
	}
}

func TestService_PutLimitSerializesNodeMutationWithMarketDataReconnect(t *testing.T) {
	md := &fakeMarketDataRuntime{}
	svc, fn := newTestServiceWithMarketDataRuntime(t, md)
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
		_, err := svc.PutRateLimit(systemCtx(), domain.LimitRate{Mode: domain.RuleModeReject,
			Scope: domain.ScopeBroker, MaxOrders: 100, Window: time.Second,
		}, domain.LimitWriteReplace, domain.MissingAccountCreate)
		rateDone <- err
	}()
	select {
	case <-rateEntered: // PutRateLimit entered node mutation.
	case err := <-rateDone:
		t.Fatalf("PutRateLimit returned before node mutation: %v", err)
	}

	orderDone := make(chan error, 1)
	go func() {
		_, err := svc.PutOrderSizeLimit(systemCtx(), domain.LimitOrderSize{
			Mode:        domain.RuleModeReject,
			Scope:       domain.ScopeAccountUnderlyingAsset,
			Account:     "acc-1",
			Asset:       "AAPL",
			MaxQuantity: "1",
		}, domain.LimitWriteReplace, domain.MissingAccountCreate)
		orderDone <- err
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
	if err := <-orderDone; err != nil {
		t.Fatalf("PutOrderSizeLimit: %v", err)
	}
	select {
	case <-orderEntered:
	default:
		t.Fatal("PutOrderSizeLimit returned without node mutation")
	}
}

func TestService_DeleteLimitValidatesTarget(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	ctx := systemCtx()

	// broker scope is not allowed for SpotFunds P&L bounds; target validation must reject.
	bad := node.LimitTarget{Mode: domain.RuleModeReject,
		Policy: domain.PolicySpotFundsPnlBoundsKillSwitch,
		Scope:  domain.ScopeBroker,
	}
	if err := svc.DeleteLimit(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if len(fn.deleteLimitCalls) != 0 {
		t.Fatalf("invalid target must not reach the node")
	}

	good := node.LimitTarget{Mode: domain.RuleModeReject,
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
	svc, fn := newTestServiceWithMarketDataRuntime(t, md)
	sink := &backendTestSink{}
	fn.restoreSink = sink
	ctx := systemCtx()

	target := node.LimitTarget{Mode: domain.RuleModeReject,
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
