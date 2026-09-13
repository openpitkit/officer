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

package integration_test

import (
	"context"
	"errors"
	"testing"

	fwbackend "go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func TestService_OrderPresentationUsesConfiguredSeam(t *testing.T) {
	t.Parallel()
	id := mdID("order-display")
	order := domain.Order{ExternalID: id, Lock: []byte{0x01}}
	fn := &fakeNode{
		orders: map[domain.ExternalID]domain.Order{id: order},
		orderRowsPage: store.OrderListPage{
			Rows:  []store.OrderListRow{{Order: order}},
			Total: 1,
		},
	}
	decodeCalls := 0
	svc, err := fwbackend.New(
		fn, nil, nil,
		func(lock []byte, got domain.Order) (string, error) {
			decodeCalls++
			if len(lock) != 1 || lock[0] != 0x01 || got.ExternalID != id {
				t.Fatalf("display seam input = %x, %q", lock, got.ExternalID)
			}
			return "101.25", nil
		},
	)
	if err != nil {
		t.Fatalf("backend.New: %v", err)
	}

	detail, err := svc.GetOrder(context.Background(), id.String())
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.DisplayPrice != "101.25" {
		t.Fatalf("detail display price = %q, want 101.25", detail.DisplayPrice)
	}
	page, err := svc.ListOrderRows(context.Background(), store.OrderListFilter{})
	if err != nil {
		t.Fatalf("ListOrderRows: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].DisplayPrice != "101.25" {
		t.Fatalf("list rows = %+v, want display price 101.25", page.Rows)
	}
	if decodeCalls != 2 {
		t.Fatalf("display seam calls = %d, want 2", decodeCalls)
	}
}

func TestService_OrderPresentationSurfacesDecodeError(t *testing.T) {
	t.Parallel()
	id := mdID("order-corrupt-lock")
	order := domain.Order{ExternalID: id, Lock: []byte{0xff}}
	fn := &fakeNode{
		orders: map[domain.ExternalID]domain.Order{id: order},
		orderRowsPage: store.OrderListPage{
			Rows:  []store.OrderListRow{{Order: order}},
			Total: 1,
		},
	}
	decodeErr := errors.New("corrupt lock")
	svc, err := fwbackend.New(
		fn, nil, nil,
		func([]byte, domain.Order) (string, error) {
			return "", decodeErr
		},
	)
	if err != nil {
		t.Fatalf("backend.New: %v", err)
	}

	if _, err := svc.GetOrder(context.Background(), id.String()); !errors.Is(err, decodeErr) {
		t.Fatalf("GetOrder error = %v, want corrupt lock", err)
	}
	if _, err := svc.ListOrderRows(context.Background(), store.OrderListFilter{}); !errors.Is(err, decodeErr) {
		t.Fatalf("ListOrderRows error = %v, want corrupt lock", err)
	}
}

func TestService_CheckOrderForwardsWithoutFormatValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Officer no longer pre-validates the probe account/asset format; the engine
	// seam parses and rejects bad values downstream. Every probe now reaches the
	// node, including ones Officer used to reject (empty/interior-space asset).
	probes := []domain.OrderProbe{
		{Account: "", BaseAsset: "AAPL", QuoteAsset: "USD"},
		{Account: "acc-1", BaseAsset: "", QuoteAsset: "USD"},
		{Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "bad asset"},
	}
	for _, probe := range probes {
		svc, fn := newTestService(t)
		if _, err := svc.CheckOrder(ctx, probe); err != nil {
			t.Fatalf("CheckOrder %+v: unexpected error: %v", probe, err)
		}
		if len(fn.checkProbes) != 1 {
			t.Fatalf("probe must reach the node, got %d calls", len(fn.checkProbes))
		}
	}
}

func TestService_CheckOrderRoutesAndReturnsPass(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	fn.checkResult = domain.CheckResult{Passed: true, WouldLockPrice: "100"}
	ctx := context.Background()

	probe := domain.OrderProbe{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       "100",
	}
	out, err := svc.CheckOrder(ctx, probe)
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if !out.Passed || out.WouldLockPrice != "100" {
		t.Fatalf("pass result not propagated: %+v", out)
	}
	if len(fn.checkProbes) != 1 || fn.checkProbes[0].Account != "acc-1" {
		t.Fatalf("valid probe must route to node once with the account preserved")
	}
}

func TestService_CheckOrderReturnsRejectAndBlock(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	fn.checkResult = domain.CheckResult{
		Passed: false,
		Rejects: []domain.OrderReject{
			{Code: "rate_limit_exceeded", Scope: "account", Policy: "rate_limit"},
		},
		WouldBlock: &domain.ExecutionAccountBlock{
			Account: "acc-1", Code: "account_blocked", Reason: "kill switch",
		},
	}
	ctx := context.Background()

	out, err := svc.CheckOrder(ctx, domain.OrderProbe{
		Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1",
	})
	if err != nil {
		t.Fatalf("CheckOrder: %v", err)
	}
	if out.Passed {
		t.Fatalf("want passed=false")
	}
	if len(out.Rejects) != 1 || out.Rejects[0].Code != "rate_limit_exceeded" {
		t.Fatalf("reject not propagated: %+v", out.Rejects)
	}
	if out.WouldBlock == nil || out.WouldBlock.Account != "acc-1" {
		t.Fatalf("would-block not propagated: %+v", out.WouldBlock)
	}
}
