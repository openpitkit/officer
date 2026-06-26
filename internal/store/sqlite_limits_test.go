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

// Limits-group tests: put/list/update(upsert)/delete for each policy type,
// UNIQUE conflict upsert semantics, unknown FK code error, scope validation,
// per-scope NULL-axis handling, and CASCADE on account/asset delete.

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/domain"
)

// seedLimitFixtures creates an account and an asset for limit tests.
func seedLimitFixtures(t *testing.T) (context.Context, RealmStore) {
	t.Helper()
	ctx := context.Background()
	_, rs := newTestStore(t)
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL"}); err != nil {
		t.Fatalf("CreateAsset(AAPL): %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount(acc-1): %v", err)
	}
	return ctx, rs
}

// --- Rate limits (limit_rate) -------------------------------------------------

func TestRateLimitPutListDeleteRoundTrip(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	limit := domain.LimitRate{
		Scope:     domain.ScopeAccountAsset,
		Account:   "acc-1",
		Asset:     "AAPL",
		MaxOrders: 100,
		Window:    5 * time.Minute,
	}
	if err := rs.PutRateLimit(ctx, limit); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	list, err := rs.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListRateLimits len = %d, want 1", len(list))
	}
	got := list[0]
	if got.Scope != domain.ScopeAccountAsset || got.Account != "acc-1" || got.Asset != "AAPL" {
		t.Fatalf("scope/account/asset = %q/%q/%q", got.Scope, got.Account, got.Asset)
	}
	if got.MaxOrders != 100 || got.Window != 5*time.Minute {
		t.Fatalf("max_orders=%d window=%v", got.MaxOrders, got.Window)
	}

	// Filter by account.
	filtered, err := rs.ListRateLimits(ctx, "acc-1")
	if err != nil || len(filtered) != 1 {
		t.Fatalf("ListRateLimits(acc-1) = %v err=%v", filtered, err)
	}

	// Delete removes the row.
	if err := rs.DeleteRateLimit(ctx, domain.ScopeAccountAsset, "acc-1", "AAPL"); err != nil {
		t.Fatalf("DeleteRateLimit: %v", err)
	}
	list, _ = rs.ListRateLimits(ctx, "")
	if len(list) != 0 {
		t.Fatalf("expected 0 after delete, got %d", len(list))
	}

	// Double delete is ErrNotFound.
	if err := rs.DeleteRateLimit(ctx, domain.ScopeAccountAsset, "acc-1", "AAPL"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeleteRateLimit(missing) error = %v, want ErrNotFound", err)
	}
}

func TestRateLimitUpsertUpdatesValue(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	if err := rs.PutRateLimit(ctx, domain.LimitRate{
		Scope: domain.ScopeBroker, MaxOrders: 50, Window: time.Minute,
	}); err != nil {
		t.Fatalf("PutRateLimit (first): %v", err)
	}
	// Second put with same composite (broker, NULL, NULL) should update the
	// value columns, not fail with a duplicate error.
	if err := rs.PutRateLimit(ctx, domain.LimitRate{
		Scope: domain.ScopeBroker, MaxOrders: 200, Window: 2 * time.Minute,
	}); err != nil {
		t.Fatalf("PutRateLimit (upsert): %v", err)
	}

	list, err := rs.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 row after upsert, got %d", len(list))
	}
	if list[0].MaxOrders != 200 || list[0].Window != 2*time.Minute {
		t.Fatalf("upsert did not update value: max_orders=%d window=%v", list[0].MaxOrders, list[0].Window)
	}
}

func TestRateLimitBrokerScopeNullAxes(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	// broker scope: no account, no asset.
	limit := domain.LimitRate{
		Scope: domain.ScopeBroker, MaxOrders: 1000, Window: time.Hour,
	}
	if err := rs.PutRateLimit(ctx, limit); err != nil {
		t.Fatalf("PutRateLimit(broker): %v", err)
	}
	list, err := rs.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if len(list) != 1 || list[0].Account != "" || list[0].Asset != "" {
		t.Fatalf("broker limit axes = account=%q asset=%q", list[0].Account, list[0].Asset)
	}
	// Deleting by (broker, "", "") should find the row.
	if err := rs.DeleteRateLimit(ctx, domain.ScopeBroker, "", ""); err != nil {
		t.Fatalf("DeleteRateLimit(broker): %v", err)
	}
}

func TestRateLimitInvalidScopeRejected(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	err := rs.PutRateLimit(ctx, domain.LimitRate{
		Scope: "bad_scope", MaxOrders: 1, Window: time.Minute,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("PutRateLimit(bad scope) error = %v, want ErrInvalid", err)
	}
}

func TestRateLimitUnknownAccountIsInvalid(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	err := rs.PutRateLimit(ctx, domain.LimitRate{
		Scope: domain.ScopeAccount, Account: "ghost",
		MaxOrders: 1, Window: time.Minute,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("PutRateLimit(unknown account) error = %v, want ErrInvalid", err)
	}
}

func TestRateLimitUnknownAssetIsInvalid(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	err := rs.PutRateLimit(ctx, domain.LimitRate{
		Scope: domain.ScopeAsset, Asset: "GHOST",
		MaxOrders: 1, Window: time.Minute,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("PutRateLimit(unknown asset) error = %v, want ErrInvalid", err)
	}
}

func TestRateLimitCascadeOnAccountDelete(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	if err := rs.PutRateLimit(ctx, domain.LimitRate{
		Scope:     domain.ScopeAccount,
		Account:   "acc-1",
		MaxOrders: 10,
		Window:    time.Minute,
	}); err != nil {
		t.Fatalf("PutRateLimit: %v", err)
	}

	r := rs.(*realmStore)
	if _, err := r.db().ExecContext(ctx, `DELETE FROM accounts WHERE code = 'acc-1'`); err != nil {
		t.Fatalf("delete account: %v", err)
	}

	list, err := rs.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits after account delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 limits after account cascade, got %d", len(list))
	}
}

// --- Order-size limits (limit_order_size) ------------------------------------

func TestOrderSizeLimitPutListDeleteRoundTrip(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	limit := domain.LimitOrderSize{
		Scope:       domain.ScopeAccountAsset,
		Account:     "acc-1",
		Asset:       "AAPL",
		MaxQuantity: "500",
		MaxNotional: "50000",
	}
	if err := rs.PutOrderSizeLimit(ctx, limit); err != nil {
		t.Fatalf("PutOrderSizeLimit: %v", err)
	}

	list, err := rs.ListOrderSizeLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListOrderSizeLimits: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}
	got := list[0]
	if got.MaxQuantity != "500" || got.MaxNotional != "50000" {
		t.Fatalf("max_quantity=%q max_notional=%q", got.MaxQuantity, got.MaxNotional)
	}

	// Upsert updates.
	if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
		MaxQuantity: "1000",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit (upsert): %v", err)
	}
	list, _ = rs.ListOrderSizeLimits(ctx, "")
	if len(list) != 1 || list[0].MaxQuantity != "1000" {
		t.Fatalf("after upsert max_quantity = %q", list[0].MaxQuantity)
	}

	if err := rs.DeleteOrderSizeLimit(ctx, domain.ScopeAccountAsset, "acc-1", "AAPL"); err != nil {
		t.Fatalf("DeleteOrderSizeLimit: %v", err)
	}
	list, _ = rs.ListOrderSizeLimits(ctx, "")
	if len(list) != 0 {
		t.Fatalf("expected 0 after delete, got %d", len(list))
	}

	if err := rs.DeleteOrderSizeLimit(ctx, domain.ScopeAccountAsset, "acc-1", "AAPL"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeleteOrderSizeLimit(missing) = %v, want ErrNotFound", err)
	}
}

func TestOrderSizeLimitAccountFilter(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	// One broker barrier and one account_asset barrier.
	if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
		MaxQuantity: "100",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit(acc-1): %v", err)
	}
	if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeBroker, MaxNotional: "999",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit(broker): %v", err)
	}

	// Filter by account returns only the account_asset row.
	filtered, err := rs.ListOrderSizeLimits(ctx, "acc-1")
	if err != nil {
		t.Fatalf("ListOrderSizeLimits(acc-1): %v", err)
	}
	if len(filtered) != 1 || filtered[0].Scope != domain.ScopeAccountAsset {
		t.Fatalf("filtered = %+v", filtered)
	}
}

func TestOrderSizeLimitInvalidRejected(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	// Both max_quantity and max_notional empty is invalid.
	err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeBroker,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("PutOrderSizeLimit(no values) error = %v, want ErrInvalid", err)
	}
}

func TestOrderSizeLimitCascadeOnAssetDelete(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
		MaxQuantity: "100",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit: %v", err)
	}

	if err := rs.DeleteAsset(ctx, "AAPL", true); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}

	list, err := rs.ListOrderSizeLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListOrderSizeLimits after asset delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 limits after asset cascade, got %d", len(list))
	}
}

// --- P&L-bounds limits (limit_pnl_bounds) ------------------------------------

func TestPnlBoundsLimitPutListDeleteRoundTrip(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	limit := domain.LimitPnlBounds{
		Scope:      domain.ScopeAccountAsset,
		Account:    "acc-1",
		Asset:      "AAPL",
		LowerBound: "-1000",
		UpperBound: "5000",
		InitialPnl: "0",
	}
	if err := rs.PutPnlBoundsLimit(ctx, limit); err != nil {
		t.Fatalf("PutPnlBoundsLimit: %v", err)
	}

	list, err := rs.ListPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListPnlBoundsLimits: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}
	got := list[0]
	if got.LowerBound != "-1000" || got.UpperBound != "5000" || got.InitialPnl != "0" {
		t.Fatalf("bounds = lower=%q upper=%q initial=%q",
			got.LowerBound, got.UpperBound, got.InitialPnl)
	}
	if got.Account != "acc-1" || got.Asset != "AAPL" {
		t.Fatalf("account=%q asset=%q", got.Account, got.Asset)
	}

	// Upsert updates bounds.
	if err := rs.PutPnlBoundsLimit(ctx, domain.LimitPnlBounds{
		Scope:      domain.ScopeAccountAsset,
		Account:    "acc-1",
		Asset:      "AAPL",
		LowerBound: "-2000",
		UpperBound: "10000",
	}); err != nil {
		t.Fatalf("PutPnlBoundsLimit (upsert): %v", err)
	}
	list, _ = rs.ListPnlBoundsLimits(ctx, "")
	if len(list) != 1 || list[0].LowerBound != "-2000" || list[0].UpperBound != "10000" {
		t.Fatalf("after upsert bounds = lower=%q upper=%q",
			list[0].LowerBound, list[0].UpperBound)
	}

	if err := rs.DeletePnlBoundsLimit(ctx, domain.ScopeAccountAsset, "acc-1", "AAPL"); err != nil {
		t.Fatalf("DeletePnlBoundsLimit: %v", err)
	}
	list, _ = rs.ListPnlBoundsLimits(ctx, "")
	if len(list) != 0 {
		t.Fatalf("expected 0 after delete, got %d", len(list))
	}

	if err := rs.DeletePnlBoundsLimit(ctx, domain.ScopeAccountAsset, "acc-1", "AAPL"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeletePnlBoundsLimit(missing) = %v, want ErrNotFound", err)
	}
}

func TestPnlBoundsLimitAssetScopeNullAccountAxis(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	// asset scope: no account axis.
	if err := rs.PutPnlBoundsLimit(ctx, domain.LimitPnlBounds{
		Scope: domain.ScopeAsset, Asset: "AAPL",
		LowerBound: "-100", UpperBound: "100",
	}); err != nil {
		t.Fatalf("PutPnlBoundsLimit(asset): %v", err)
	}
	list, err := rs.ListPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListPnlBoundsLimits: %v", err)
	}
	if len(list) != 1 || list[0].Account != "" {
		t.Fatalf("account axis = %q, want empty for asset scope", list[0].Account)
	}
	// Delete by (asset, "", "AAPL").
	if err := rs.DeletePnlBoundsLimit(ctx, domain.ScopeAsset, "", "AAPL"); err != nil {
		t.Fatalf("DeletePnlBoundsLimit(asset): %v", err)
	}
}

func TestPnlBoundsLimitInitialPnlOnlyForAccountAsset(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	// initial_pnl on a non-account_asset scope must be rejected by Validate().
	err := rs.PutPnlBoundsLimit(ctx, domain.LimitPnlBounds{
		Scope: domain.ScopeAsset, Asset: "AAPL",
		LowerBound: "-100",
		InitialPnl: "5",
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("PutPnlBoundsLimit(initial_pnl on asset scope) error = %v, want ErrInvalid", err)
	}
}

func TestPnlBoundsLimitInvalidBoundsRejected(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	// Both lower and upper empty is invalid.
	err := rs.PutPnlBoundsLimit(ctx, domain.LimitPnlBounds{
		Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("PutPnlBoundsLimit(no bounds) error = %v, want ErrInvalid", err)
	}
}

func TestPnlBoundsLimitUnknownAccountIsInvalid(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	err := rs.PutPnlBoundsLimit(ctx, domain.LimitPnlBounds{
		Scope: domain.ScopeAccountAsset, Account: "ghost", Asset: "AAPL",
		LowerBound: "-100",
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("PutPnlBoundsLimit(unknown account) error = %v, want ErrInvalid", err)
	}
}

func TestPnlBoundsLimitCascadeOnAccountDelete(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	if err := rs.PutPnlBoundsLimit(ctx, domain.LimitPnlBounds{
		Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
		LowerBound: "-100",
	}); err != nil {
		t.Fatalf("PutPnlBoundsLimit: %v", err)
	}

	r := rs.(*realmStore)
	if _, err := r.db().ExecContext(ctx, `DELETE FROM accounts WHERE code = 'acc-1'`); err != nil {
		t.Fatalf("delete account: %v", err)
	}

	list, err := rs.ListPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListPnlBoundsLimits after account delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 limits after account cascade, got %d", len(list))
	}
}

func TestPnlBoundsLimitAccountFilter(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	if err := rs.PutPnlBoundsLimit(ctx, domain.LimitPnlBounds{
		Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL",
		LowerBound: "-100",
	}); err != nil {
		t.Fatalf("PutPnlBoundsLimit(acc-1): %v", err)
	}
	if err := rs.PutPnlBoundsLimit(ctx, domain.LimitPnlBounds{
		Scope: domain.ScopeAsset, Asset: "AAPL",
		UpperBound: "10000",
	}); err != nil {
		t.Fatalf("PutPnlBoundsLimit(asset): %v", err)
	}

	filtered, err := rs.ListPnlBoundsLimits(ctx, "acc-1")
	if err != nil {
		t.Fatalf("ListPnlBoundsLimits(acc-1): %v", err)
	}
	if len(filtered) != 1 || filtered[0].Scope != domain.ScopeAccountAsset {
		t.Fatalf("filter returned %+v", filtered)
	}
}
