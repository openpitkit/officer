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

package native

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/internal/store/sqlite"
)

// newMissingAccountNode builds a node on the real OpenPit engine and a fresh
// sqlite store, so a barrier that names an unknown account is exercised end to
// end rather than against a fake.
func newMissingAccountNode(t *testing.T) (node.Node, domain.Caller) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.New(t.TempDir() + "/officer.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	n, _, err := node.NewLocalNode(
		ctx,
		st,
		NewOpenPitEngineBuildFunc(""),
	)
	if err != nil {
		t.Fatalf("NewLocalNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n, domain.Caller{
		Source:    domain.SourcePanel,
		Principal: domain.PrincipalOperator,
	}
}

// fundMissingAccount gives the account enough quote balance to place the test
// orders. It runs with the create choice because the barrier under test has
// already registered the account.
func fundMissingAccount(
	t *testing.T, n node.Node, caller domain.Caller, account domain.AccountID,
) {
	t.Helper()
	if _, err := n.ApplyAdjustment(
		context.Background(),
		node.Key{Account: account},
		domain.ExternalID(""),
		domain.AdjustmentRequest{
			Asset: "USDT",
			Balance: &domain.AdjustmentAmount{
				Mode:  domain.AdjustmentModeAbsolute,
				Value: "1000000",
			},
		},
		domain.MissingAccountCreate,
		caller,
	); err != nil {
		t.Fatalf("fund %q: %v", account, err)
	}
}

func missingAccountOrder(
	account domain.AccountID, quantity string,
) domain.Order {
	return domain.Order{
		Account:     account,
		BaseAsset:   "BTC",
		QuoteAsset:  "USDT",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: quantity,
		Price:       "100",
	}
}

// assertAutoCreatedByBarrier checks the barrier registered the account it named
// with default settings: no group and no currency of its own.
func assertAutoCreatedByBarrier(
	t *testing.T, n node.Node, account domain.AccountID,
) {
	t.Helper()
	stored, _, err := n.GetAccountState(
		context.Background(), node.Key{Account: account},
	)
	if err != nil {
		t.Fatalf("GetAccountState(%q): %v", account, err)
	}
	if stored.GroupCode != "" || stored.Currency != "" {
		t.Fatalf("auto-created account = %+v, want no group and no currency",
			stored)
	}
}

// TestPutOrderSizeLimit_CreatedAccountBarrierIsEnforced is the acceptance path
// for the order-size policy: the barrier names an account that
// does not exist, "create" registers it, and the live engine enforces the
// barrier on the very next order.
func TestPutOrderSizeLimit_CreatedAccountBarrierIsEnforced(t *testing.T) {
	ctx := context.Background()
	n, caller := newMissingAccountNode(t)
	const account = domain.AccountID("os-acc")

	// Quantity caps are keyed by the traded underlying asset.
	if _, err := n.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope:       domain.ScopeAccountUnderlyingAsset,
		Account:     account,
		Asset:       "BTC",
		MaxQuantity: "1",
	}, domain.MissingAccountCreate, caller); err != nil {
		t.Fatalf("PutOrderSizeLimit: %v", err)
	}
	assertAutoCreatedByBarrier(t, n, account)
	fundMissingAccount(t, n, caller, account)

	within, err := n.SubmitOrder(
		ctx, node.Key{Account: account}, missingAccountOrder(account, "0.5"),
		domain.MissingAccountCreate, caller,
	)
	if err != nil {
		t.Fatalf("SubmitOrder within barrier: %v", err)
	}
	if within.Status != domain.OrderStatusCommitted {
		t.Fatalf("order within barrier status = %q, want committed", within.Status)
	}

	over, err := n.SubmitOrder(
		ctx, node.Key{Account: account}, missingAccountOrder(account, "5"),
		domain.MissingAccountCreate, caller,
	)
	if err != nil {
		t.Fatalf("SubmitOrder over barrier: %v", err)
	}
	if over.Status != domain.OrderStatusRejected {
		t.Fatalf("order over barrier status = %q, want rejected", over.Status)
	}
}

// TestPutRateLimit_CreatedAccountBarrierIsEnforced is the same acceptance path
// for the rate policy on the plain account scope.
func TestPutRateLimit_CreatedAccountBarrierIsEnforced(t *testing.T) {
	ctx := context.Background()
	n, caller := newMissingAccountNode(t)
	const account = domain.AccountID("rl-acc")

	if _, err := n.PutRateLimit(ctx, domain.LimitRate{
		Scope:     domain.ScopeAccount,
		Account:   account,
		MaxOrders: 1,
		Window:    time.Hour,
	}, domain.MissingAccountCreate, caller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}
	assertAutoCreatedByBarrier(t, n, account)
	fundMissingAccount(t, n, caller, account)

	first, err := n.SubmitOrder(
		ctx, node.Key{Account: account}, missingAccountOrder(account, "0.5"),
		domain.MissingAccountCreate, caller,
	)
	if err != nil {
		t.Fatalf("first SubmitOrder: %v", err)
	}
	if first.Status != domain.OrderStatusCommitted {
		t.Fatalf("first order status = %q, want committed", first.Status)
	}

	second, err := n.SubmitOrder(
		ctx, node.Key{Account: account}, missingAccountOrder(account, "0.5"),
		domain.MissingAccountCreate, caller,
	)
	if err != nil {
		t.Fatalf("second SubmitOrder: %v", err)
	}
	if second.Status != domain.OrderStatusRejected {
		t.Fatalf("second order status = %q, want rejected by the rate barrier",
			second.Status)
	}
}

// TestPutSpotFundsPnlBoundsLimit_CreatedAccountBarrierIsEnforced is the same
// acceptance path for the SpotFunds P&L-bounds policy: the created account
// inherits the reserved default-group currency, so the engine can evaluate its
// account-currency P&L and kill-switches it once a realized loss breaches the
// barrier the same call installed.
func TestPutSpotFundsPnlBoundsLimit_CreatedAccountBarrierIsEnforced(t *testing.T) {
	ctx := context.Background()
	n, caller := newMissingAccountNode(t)
	const account = domain.AccountID("pnl-acc")

	if err := n.SetDefaultGroupCurrency(ctx, "USDT", caller); err != nil {
		t.Fatalf("SetDefaultGroupCurrency: %v", err)
	}
	if _, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope:      domain.ScopeAccount,
		Account:    account,
		Currency:   "USDT",
		LowerBound: "-1",
	}, domain.MissingAccountCreate, caller); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}
	assertAutoCreatedByBarrier(t, n, account)
	fundMissingAccount(t, n, caller, account)

	// Buy high, then sell low: the round trip realizes an account-currency loss
	// well past the barrier the create call installed.
	buy := missingAccountOrder(account, "1")
	if _, result, err := n.SubmitImmediate(
		ctx, node.Key{Account: account}, buy,
		domain.MissingAccountCreate, caller,
	); err != nil || !result.Accepted {
		t.Fatalf("buy SubmitImmediate: err=%v result=%+v", err, result)
	}
	sell := buy
	sell.Side = domain.OrderSideSell
	sell.Price = "1"
	if _, result, err := n.SubmitImmediate(
		ctx, node.Key{Account: account}, sell,
		domain.MissingAccountCreate, caller,
	); err != nil || !result.Accepted {
		t.Fatalf("sell SubmitImmediate: err=%v result=%+v", err, result)
	}

	stored, _, err := n.GetAccountState(ctx, node.Key{Account: account})
	if err != nil {
		t.Fatalf("GetAccountState: %v", err)
	}
	if !stored.Blocked {
		t.Fatalf("account = %+v, want kill-switched by the P&L barrier", stored)
	}
}

// TestPutSpotFundsPnlBoundsLimit_RejectLeavesNoAccount covers the refusing
// choice against the real engine: the account stays unknown and no barrier is
// installed.
func TestPutSpotFundsPnlBoundsLimit_RejectLeavesNoAccount(t *testing.T) {
	ctx := context.Background()
	n, caller := newMissingAccountNode(t)
	const account = domain.AccountID("never-created")

	_, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope:      domain.ScopeAccount,
		Account:    account,
		Currency:   "USDT",
		LowerBound: "-1",
	}, domain.MissingAccountReject, caller)
	if err == nil {
		t.Fatal("PutSpotFundsPnlBoundsLimit: want ErrAccountMissing, got nil")
	}
	if !errors.Is(err, domain.ErrAccountMissing) {
		t.Fatalf("PutSpotFundsPnlBoundsLimit = %v, want ErrAccountMissing", err)
	}
	var typed domain.AccountMissingError
	if !errors.As(err, &typed) || typed.Account != account {
		t.Fatalf("error %v does not carry the missing account %q", err, account)
	}
	limits, err := n.ListLimits(ctx, account)
	if err != nil {
		t.Fatalf("ListLimits: %v", err)
	}
	if len(limits.SpotFundsPnlBoundsLimits) != 0 {
		t.Fatalf("stored P&L bounds limits = %+v, want none",
			limits.SpotFundsPnlBoundsLimits)
	}
}
