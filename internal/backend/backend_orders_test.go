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
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

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
		svc, fn := newTestService()
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
	svc, fn := newTestService()
	fn.checkResult = domain.CheckResult{Passed: true, WouldLockPrices: []string{"100"}}
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
	if !out.Passed || len(out.WouldLockPrices) != 1 || out.WouldLockPrices[0] != "100" {
		t.Fatalf("pass result not propagated: %+v", out)
	}
	if len(fn.checkProbes) != 1 || fn.checkProbes[0].Account != "acc-1" {
		t.Fatalf("valid probe must route to node once with the account preserved")
	}
}

func TestService_CheckOrderReturnsRejectAndBlock(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
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
