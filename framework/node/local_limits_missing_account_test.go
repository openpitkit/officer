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
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// assertAutoCreatedAccount checks the account the barrier named was registered
// with default settings - no group and no currency - and that the audit trail
// names the triggering operation.
func assertAutoCreatedAccount(
	t *testing.T,
	st store.RealmStore,
	code domain.AccountID,
	operation string,
) {
	t.Helper()
	ctx := context.Background()
	account, ok, err := st.GetAccount(ctx, code)
	if err != nil || !ok {
		t.Fatalf("GetAccount(%q) = ok %v err %v, want auto-created", code, ok, err)
	}
	if account.GroupCode != "" || account.Currency != "" {
		t.Fatalf("auto-created account = %+v, want no group and no currency",
			account)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAccount},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create account): %v", err)
	}
	want := "auto-created account " + code.String() + " by " + operation
	if len(rows) != 1 || rows[0].Account != code ||
		!strings.Contains(rows[0].Detail, want) {
		t.Fatalf("create-account audit rows = %+v, want %q", rows, want)
	}
}

// TestLocalNode_PutRateLimitCreatesNamedAccount is the happy path for
// the rate policy: a barrier may name an account that does not exist yet, and
// "create" registers it and hands the engine the live barrier in the same call.
func TestLocalNode_PutRateLimitCreatesNamedAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	// The resolver enforcement proves the new account is published before the
	// barrier goes live, not merely written to the store.
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	_, snapshot := preparePolicyLifecycleBuild(n)
	ctx := context.Background()

	limit := rateLimit(domain.ScopeAccount, "fresh", "", 100, time.Second)
	if _, err := n.PutRateLimit(
		ctx, limit, domain.MissingAccountCreate, testCaller,
	); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	assertAutoCreatedAccount(t, st, "fresh", "put rate limit")
	stored, err := st.ListRateLimits(ctx, "fresh")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(stored) != 1 || stored[0] != limit {
		t.Fatalf("stored rate limits = %+v, want the new barrier", stored)
	}
	if len(snapshot.RateLimits) != 1 || snapshot.RateLimits[0] != limit {
		t.Fatalf("engine rate limits = %+v, want the new barrier",
			snapshot.RateLimits)
	}
}

// TestLocalNode_PutRateLimitRejectsMissingAccount covers the refusing choice:
// the barrier is not stored and the caller gets ErrAccountMissing.
func TestLocalNode_PutRateLimitRejectsMissingAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	preparePolicyLifecycleBuild(n)
	ctx := context.Background()

	_, err := n.PutRateLimit(
		ctx,
		rateLimit(domain.ScopeAccount, "fresh", "", 100, time.Second),
		domain.MissingAccountReject,
		testCaller,
	)
	if !errors.Is(err, domain.ErrAccountMissing) {
		t.Fatalf("PutRateLimit = %v, want ErrAccountMissing", err)
	}
	var typed domain.AccountMissingError
	if !errors.As(err, &typed) || typed.Account != "fresh" {
		t.Fatalf("error %v does not carry the account code", err)
	}
	if _, ok, getErr := st.GetAccount(ctx, "fresh"); getErr != nil || ok {
		t.Fatalf("GetAccount = ok %v err %v, want no account created", ok, getErr)
	}
	stored, err := st.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("stored rate limits = %+v, want none", stored)
	}
}

// TestLocalNode_PutOrderSizeLimitCreatesNamedAccount mirrors the rate-policy
// happy path for the order-size policy, whose account axis rides on the
// account_underlying_asset scope.
func TestLocalNode_PutOrderSizeLimitCreatesNamedAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	_, snapshot := preparePolicyLifecycleBuild(n)
	ctx := context.Background()

	limit := domain.LimitOrderSize{
		Scope:       domain.ScopeAccountUnderlyingAsset,
		Account:     "fresh",
		Asset:       "AAPL",
		MaxQuantity: "10",
	}
	if _, err := n.PutOrderSizeLimit(
		ctx, limit, domain.MissingAccountCreate, testCaller,
	); err != nil {
		t.Fatalf("PutOrderSizeLimit: %v", err)
	}

	assertAutoCreatedAccount(t, st, "fresh", "put order-size limit")
	stored, err := st.ListOrderSizeLimits(ctx, "fresh")
	if err != nil {
		t.Fatalf("ListOrderSizeLimits: %v", err)
	}
	if len(stored) != 1 || stored[0] != limit {
		t.Fatalf("stored order-size limits = %+v, want the new barrier", stored)
	}
	if len(snapshot.OrderSizeLimits) != 1 || snapshot.OrderSizeLimits[0] != limit {
		t.Fatalf("engine order-size limits = %+v, want the new barrier",
			snapshot.OrderSizeLimits)
	}
}

// TestLocalNode_PutOrderSizeLimitRejectsMissingAccount covers the refusing
// choice for the order-size policy.
func TestLocalNode_PutOrderSizeLimitRejectsMissingAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	preparePolicyLifecycleBuild(n)
	ctx := context.Background()

	_, err := n.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope:       domain.ScopeAccountUnderlyingAsset,
		Account:     "fresh",
		Asset:       "AAPL",
		MaxQuantity: "10",
	}, domain.MissingAccountReject, testCaller)
	if !errors.Is(err, domain.ErrAccountMissing) {
		t.Fatalf("PutOrderSizeLimit = %v, want ErrAccountMissing", err)
	}
	stored, err := st.ListOrderSizeLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListOrderSizeLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("stored order-size limits = %+v, want none", stored)
	}
}

// TestLocalNode_PutSpotFundsPnlBoundsLimitCreatesNamedAccount mirrors the happy
// path for the SpotFunds P&L-bounds policy, which reconfigures the live engine
// rather than rebuilding it, so the engine effect is the configure call.
func TestLocalNode_PutSpotFundsPnlBoundsLimitCreatesNamedAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	eng.enforceResolver = true
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	limit := domain.LimitSpotFundsPnlBounds{
		Scope:      domain.ScopeAccount,
		Account:    "fresh",
		Currency:   "USD",
		LowerBound: "-100",
	}
	if _, err := n.PutSpotFundsPnlBoundsLimit(
		ctx, limit, domain.MissingAccountCreate, testCaller,
	); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}

	assertAutoCreatedAccount(t, st, "fresh", "put spot-funds P&L bounds limit")
	stored, err := st.ListSpotFundsPnlBoundsLimits(ctx, "fresh")
	if err != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", err)
	}
	if len(stored) != 1 || stored[0] != limit {
		t.Fatalf("stored P&L bounds limits = %+v, want the new barrier", stored)
	}
	applied := false
	for _, call := range eng.configureCalls {
		if call.policy != domain.PolicySpotFundsPnlBoundsKillSwitch {
			continue
		}
		for _, got := range call.limits.SpotFundsPnlBoundsLimits {
			if got == limit {
				applied = true
			}
		}
	}
	if !applied {
		t.Fatalf("engine configure calls = %+v, want the new barrier",
			eng.configureCalls)
	}
}

// TestLocalNode_PutSpotFundsPnlBoundsLimitRejectsMissingAccount covers the
// refusing choice for the SpotFunds P&L-bounds policy: nothing is stored and the
// live policy is never reconfigured.
func TestLocalNode_PutSpotFundsPnlBoundsLimitRejectsMissingAccount(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	ctx := context.Background()

	_, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope:      domain.ScopeAccount,
		Account:    "fresh",
		Currency:   "USD",
		LowerBound: "-100",
	}, domain.MissingAccountReject, testCaller)
	if !errors.Is(err, domain.ErrAccountMissing) {
		t.Fatalf("PutSpotFundsPnlBoundsLimit = %v, want ErrAccountMissing", err)
	}
	stored, err := st.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("stored P&L bounds limits = %+v, want none", stored)
	}
	if len(eng.configureCalls) != 0 {
		t.Fatalf("engine configure calls = %+v, want none", eng.configureCalls)
	}
}

// TestLocalNode_PutBrokerRateLimitIgnoresMissingAccountChoice covers the scope
// boundary: a barrier with no account axis needs no decision, so an empty choice
// is accepted and never resolves an account.
func TestLocalNode_PutBrokerRateLimitIgnoresMissingAccountChoice(t *testing.T) {
	t.Parallel()
	eng := newFakeEngine()
	n, st := newTestNode(t, eng)
	preparePolicyLifecycleBuild(n)
	ctx := context.Background()

	limit := rateLimit(domain.ScopeBroker, "", "", 100, time.Second)
	if _, err := n.PutRateLimit(ctx, limit, "", testCaller); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	stored, err := st.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(stored) != 1 || stored[0] != limit {
		t.Fatalf("stored rate limits = %+v, want the broker barrier", stored)
	}
	rows, err := st.ListAuditFiltered(ctx, domain.AuditFilter{
		Actions: []domain.AuditAction{domain.AuditActionCreateAccount},
	}, 10)
	if err != nil {
		t.Fatalf("ListAuditFiltered(create account): %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("create-account audit rows = %+v, want none", rows)
	}
}
