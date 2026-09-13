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

package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

// seedLimitFixtures creates an account and an asset for limit tests.
func seedLimitFixtures(t *testing.T) (context.Context, fwstore.RealmStore) {
	t.Helper()
	ctx := context.Background()
	_, rs := newTestStore(t)
	if _, err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL"}); err != nil {
		t.Fatalf("CreateAsset(AAPL): %v", err)
	}
	if _, err := rs.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset(USD): %v", err)
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
	if _, err := r.rawDB().ExecContext(ctx, `DELETE FROM account WHERE code = 'acc-1'`); err != nil {
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
		Scope:       domain.ScopeAccountUnderlyingAsset,
		Account:     "acc-1",
		Asset:       "AAPL",
		MaxQuantity: "500",
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
	if got != limit {
		t.Fatalf("max_quantity=%q max_notional=%q", got.MaxQuantity, got.MaxNotional)
	}
	settlement := domain.LimitOrderSize{
		Scope:       domain.ScopeAccountSettlementAsset,
		Account:     "acc-1",
		Asset:       "AAPL",
		MaxNotional: "50000",
	}
	if err := rs.PutOrderSizeLimit(ctx, settlement); err != nil {
		t.Fatalf("PutOrderSizeLimit(settlement): %v", err)
	}

	// Upsert updates.
	if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeAccountUnderlyingAsset, Account: "acc-1", Asset: "AAPL",
		MaxQuantity: "1000",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit (upsert): %v", err)
	}
	list, _ = rs.ListOrderSizeLimits(ctx, "")
	if len(list) != 2 {
		t.Fatalf("after upsert len = %d, want 2 semantic rows", len(list))
	}

	if err := rs.DeleteOrderSizeLimit(
		ctx, domain.ScopeAccountUnderlyingAsset, "acc-1", "AAPL",
	); err != nil {
		t.Fatalf("DeleteOrderSizeLimit: %v", err)
	}
	list, _ = rs.ListOrderSizeLimits(ctx, "")
	if len(list) != 1 || list[0] != settlement {
		t.Fatalf("after underlying delete = %+v, want settlement row", list)
	}

	if err := rs.DeleteOrderSizeLimit(
		ctx, domain.ScopeAccountUnderlyingAsset, "acc-1", "AAPL",
	); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeleteOrderSizeLimit(missing) = %v, want ErrNotFound", err)
	}
	if err := rs.DeleteOrderSizeLimit(
		ctx, domain.ScopeAccountSettlementAsset, "acc-1", "AAPL",
	); err != nil {
		t.Fatalf("DeleteOrderSizeLimit(settlement): %v", err)
	}
}

func TestOrderSizeLimitAccountFilter(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	// One broker barrier and one account-underlying-asset barrier.
	if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeAccountUnderlyingAsset, Account: "acc-1", Asset: "AAPL",
		MaxQuantity: "100",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit(acc-1): %v", err)
	}
	if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeBroker, MaxNotional: "999",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit(broker): %v", err)
	}

	// Filter by account returns only the account-underlying-asset row.
	filtered, err := rs.ListOrderSizeLimits(ctx, "acc-1")
	if err != nil {
		t.Fatalf("ListOrderSizeLimits(acc-1): %v", err)
	}
	if len(filtered) != 1 || filtered[0].Scope != domain.ScopeAccountUnderlyingAsset {
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
		Scope: domain.ScopeAccountUnderlyingAsset, Account: "acc-1", Asset: "AAPL",
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

func TestDeleteAccountWithoutForceRejectsSpotFundsPnlBoundsDependent(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	if err := rs.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeAccount, Account: "acc-1", Currency: "USD", LowerBound: "-100",
	}); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}

	err := rs.DeleteAccount(ctx, "acc-1", false)
	var dependentErr domain.HasDependentsError
	if !errors.As(err, &dependentErr) {
		t.Fatalf("DeleteAccount(no force) error = %v, want HasDependentsError", err)
	}
	if len(dependentErr.Dependents) != 1 ||
		dependentErr.Dependents[0].Kind != "limit_spot_funds_pnl_bound" ||
		dependentErr.Dependents[0].Count != 1 {
		t.Fatalf("DeleteAccount(no force) dependents = %+v", dependentErr.Dependents)
	}
	if _, ok, getErr := rs.GetAccount(ctx, "acc-1"); getErr != nil || !ok {
		t.Fatalf("GetAccount after rejected delete: ok=%v err=%v", ok, getErr)
	}
	limits, listErr := rs.ListSpotFundsPnlBoundsLimits(ctx, "acc-1")
	if listErr != nil || len(limits) != 1 {
		t.Fatalf("ListSpotFundsPnlBoundsLimits after rejected delete = %+v err=%v",
			limits, listErr)
	}
}

func TestDeleteAccountForceCascadesSpotFundsPnlBoundsDependent(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)

	if err := rs.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeAccount, Account: "acc-1", Currency: "USD", LowerBound: "-100",
	}); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}
	if err := rs.DeleteAccount(ctx, "acc-1", true); err != nil {
		t.Fatalf("DeleteAccount(force): %v", err)
	}

	if _, ok, err := rs.GetAccount(ctx, "acc-1"); err != nil || ok {
		t.Fatalf("GetAccount after forced delete: ok=%v err=%v", ok, err)
	}
	limits, err := rs.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits after forced delete: %v", err)
	}
	if len(limits) != 0 {
		t.Fatalf("spot funds limits after forced delete = %+v, want none", limits)
	}
}

func policyKeys(rows []fwstore.PolicyListRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, string(row.Kind)+"|"+string(row.Account))
	}
	return out
}

// seedPolicyFixtures creates the accounts and asset plus one barrier of each
// kind across two accounts, so a policy list test can exercise the UNION,
// filters, and paging.
func seedPolicyFixtures(t *testing.T) (context.Context, fwstore.RealmStore) {
	t.Helper()
	ctx := context.Background()
	_, rs := newTestStore(t)
	if _, err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL"}); err != nil {
		t.Fatalf("CreateAsset(AAPL): %v", err)
	}
	for _, code := range []domain.AccountID{"acc-1", "acc-2"} {
		if _, err := rs.CreateAccount(ctx, domain.Account{Code: code}); err != nil {
			t.Fatalf("CreateAccount(%s): %v", code, err)
		}
	}
	// acc-1 carries both supported kinds; acc-2 carries only a rate barrier.
	if err := rs.PutRateLimit(ctx, domain.LimitRate{
		Scope: domain.ScopeAccount, Account: "acc-1",
		MaxOrders: 100, Window: time.Minute,
	}); err != nil {
		t.Fatalf("PutRateLimit(acc-1): %v", err)
	}
	if err := rs.PutRateLimit(ctx, domain.LimitRate{
		Scope: domain.ScopeAccount, Account: "acc-2",
		MaxOrders: 50, Window: time.Minute,
	}); err != nil {
		t.Fatalf("PutRateLimit(acc-2): %v", err)
	}
	if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeAccountUnderlyingAsset, Account: "acc-1", Asset: "AAPL",
		MaxQuantity: "500",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit(acc-1): %v", err)
	}
	return ctx, rs
}

func TestListPolicyRowsUnionOrderAndTotal(t *testing.T) {
	ctx, rs := seedPolicyFixtures(t)

	// Unfiltered: all three barriers, ordered by the default key (kind, then the
	// composite). Total counts every matching barrier before paging.
	page, err := rs.ListPolicyRows(ctx, fwstore.PolicyListFilter{})
	if err != nil {
		t.Fatalf("ListPolicyRows all: %v", err)
	}
	if page.Total != 3 {
		t.Fatalf("total = %d, want 3", page.Total)
	}
	// kind sorts lexically: order_size_limit, rate_limit.
	wantOrder := []string{
		"order_size_limit|acc-1",
		"rate_limit|acc-1",
		"rate_limit|acc-2",
	}
	if got := policyKeys(page.Rows); !equalStrings(got, wantOrder) {
		t.Fatalf("default order = %v, want %v", got, wantOrder)
	}
	// The order-size row reconstructs its typed value faithfully.
	if page.Rows[0].OrderSize == nil || page.Rows[0].OrderSize.MaxQuantity != "500" {
		t.Fatalf("order-size payload = %+v", page.Rows[0].OrderSize)
	}
	// The rate row reconstructs the count and window.
	rateRow := page.Rows[1]
	if rateRow.Rate == nil || rateRow.Rate.MaxOrders != 100 ||
		rateRow.Rate.Window != time.Minute {
		t.Fatalf("rate payload = %+v", rateRow.Rate)
	}
}

func TestListPolicyRowsAccountAndKindFilter(t *testing.T) {
	ctx, rs := seedPolicyFixtures(t)

	// Exact account filter narrows to acc-1's two barriers.
	page, err := rs.ListPolicyRows(ctx, fwstore.PolicyListFilter{
		Account: fwstore.ExactTextMatcher("acc-1"),
	})
	if err != nil {
		t.Fatalf("ListPolicyRows account: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("acc-1 total = %d, want 2", page.Total)
	}

	// Kind filter narrows to the two rate barriers.
	rate := fwstore.PolicyKindRate
	page, err = rs.ListPolicyRows(ctx, fwstore.PolicyListFilter{Kind: &rate})
	if err != nil {
		t.Fatalf("ListPolicyRows kind: %v", err)
	}
	if got := policyKeys(page.Rows); !equalStrings(
		got, []string{"rate_limit|acc-1", "rate_limit|acc-2"},
	) {
		t.Fatalf("rate-only rows = %v", got)
	}

	// Account and kind combine.
	page, err = rs.ListPolicyRows(ctx, fwstore.PolicyListFilter{
		Account: fwstore.ExactTextMatcher("acc-2"),
		Kind:    &rate,
	})
	if err != nil {
		t.Fatalf("ListPolicyRows account+kind: %v", err)
	}
	if got := policyKeys(page.Rows); !equalStrings(got, []string{"rate_limit|acc-2"}) {
		t.Fatalf("acc-2 rate rows = %v", got)
	}
}

func TestListPolicyRowsScopeFilter(t *testing.T) {
	ctx, rs := seedPolicyFixtures(t)

	page, err := rs.ListPolicyRows(ctx, fwstore.PolicyListFilter{
		Scope: domain.ScopeAccountUnderlyingAsset,
	})
	if err != nil {
		t.Fatalf("ListPolicyRows scope: %v", err)
	}
	if got := policyKeys(page.Rows); !equalStrings(
		got, []string{"order_size_limit|acc-1"},
	) {
		t.Fatalf("account-underlying-asset rows = %v", got)
	}
	if page.Total != 1 {
		t.Fatalf("account-underlying-asset total = %d, want 1", page.Total)
	}
}

func TestListPolicyRowsRejectsUnknownScope(t *testing.T) {
	ctx, rs := seedPolicyFixtures(t)

	_, err := rs.ListPolicyRows(ctx, fwstore.PolicyListFilter{Scope: "unknown"})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ListPolicyRows unknown scope = %v, want ErrInvalid", err)
	}
}

func TestListPolicyRowsSpotFundsAxesFilter(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	if _, err := rs.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset(USD): %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}); err != nil {
		t.Fatalf("CreateGroup(desk-a): %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "desk-b"}); err != nil {
		t.Fatalf("CreateGroup(desk-b): %v", err)
	}
	for _, limit := range []domain.LimitSpotFundsPnlBounds{
		{
			Scope:        domain.ScopeAccountGroup,
			AccountGroup: "desk-a",
			Currency:     "USD",
			LowerBound:   "-1000",
		},
		{
			Scope:        domain.ScopeAccountGroup,
			AccountGroup: "desk-b",
			Currency:     "USD",
			LowerBound:   "-500",
		},
	} {
		if err := rs.PutSpotFundsPnlBoundsLimit(ctx, limit); err != nil {
			t.Fatalf("PutSpotFundsPnlBoundsLimit(%s): %v", limit.AccountGroup, err)
		}
	}

	page, err := rs.ListPolicyRows(ctx, fwstore.PolicyListFilter{
		AccountGroup: fwstore.ExactTextMatcher("desk-a"),
	})
	if err != nil {
		t.Fatalf("ListPolicyRows spot funds axes: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("spot funds total = %d, want 1", page.Total)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("spot funds rows len = %d, want 1: %+v", len(page.Rows), page.Rows)
	}
	row := page.Rows[0]
	if row.Kind != fwstore.PolicyKindSpotFundsPnlBounds {
		t.Fatalf("spot funds kind = %q, want %q", row.Kind, fwstore.PolicyKindSpotFundsPnlBounds)
	}
	if row.SpotFundsPnlBounds == nil {
		t.Fatalf("spot funds payload is nil: %+v", row)
	}
	if row.AccountGroup != "desk-a" {
		t.Fatalf("row axes = group %q", row.AccountGroup)
	}
	if row.SpotFundsPnlBounds.AccountGroup != "desk-a" {
		t.Fatalf("payload axes = %+v", row.SpotFundsPnlBounds)
	}
	if row.SpotFundsPnlBounds.Currency != "USD" {
		t.Fatalf("payload currency = %q, want USD", row.SpotFundsPnlBounds.Currency)
	}
}

func TestListPolicyRowsAssetFilterIncludesPnlCurrency(t *testing.T) {
	ctx, rs := seedPolicyFixtures(t)
	for _, code := range []string{"USD", "EUR"} {
		if _, err := rs.CreateAsset(ctx, domain.Asset{Code: code}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", code, err)
		}
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}); err != nil {
		t.Fatalf("CreateGroup(desk-a): %v", err)
	}
	if err := rs.PutRateLimit(ctx, domain.LimitRate{
		Scope: domain.ScopeAsset, Asset: "USD", MaxOrders: 10, Window: time.Minute,
	}); err != nil {
		t.Fatalf("PutRateLimit(USD): %v", err)
	}
	for _, limit := range []domain.LimitSpotFundsPnlBounds{
		{
			Scope: domain.ScopeAccount, Account: "acc-1",
			Currency: "USD", LowerBound: "-100",
		},
		{
			Scope:        domain.ScopeAccountGroup,
			AccountGroup: "desk-a",
			Currency:     "EUR",
			LowerBound:   "-100",
		},
	} {
		if err := rs.PutSpotFundsPnlBoundsLimit(ctx, limit); err != nil {
			t.Fatalf("PutSpotFundsPnlBoundsLimit(%s): %v", limit.Currency, err)
		}
	}

	page, err := rs.ListPolicyRows(ctx, fwstore.PolicyListFilter{
		Asset: fwstore.ExactTextMatcher("USD"),
		Sort:  fwstore.SortSpec{Column: "asset"},
	})
	if err != nil {
		t.Fatalf("ListPolicyRows(asset=USD): %v", err)
	}
	if got := policyKeys(page.Rows); !equalStrings(
		got,
		[]string{
			"rate_limit|",
			"spot_funds_pnl_bounds_kill_switch|acc-1",
		},
	) {
		t.Fatalf("asset=USD rows = %v", got)
	}
	if page.Rows[1].SpotFundsPnlBounds == nil ||
		page.Rows[1].SpotFundsPnlBounds.Currency != "USD" {
		t.Fatalf("asset=USD P&L barrier = %+v", page.Rows[1].SpotFundsPnlBounds)
	}

	page, err = rs.ListPolicyRows(ctx, fwstore.PolicyListFilter{
		Sort: fwstore.SortSpec{Column: "asset"},
	})
	if err != nil {
		t.Fatalf("ListPolicyRows sort asset: %v", err)
	}
	if got := policyKeys(page.Rows); !equalStrings(
		got,
		[]string{
			"rate_limit|acc-1",
			"rate_limit|acc-2",
			"order_size_limit|acc-1",
			"spot_funds_pnl_bounds_kill_switch|",
			"rate_limit|",
			"spot_funds_pnl_bounds_kill_switch|acc-1",
		},
	) {
		t.Fatalf("asset order = %v", got)
	}
}

func TestSpotFundsPnlBoundsCurrencyRoundTripAllScopes(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)
	if _, err := rs.CreateAsset(ctx, domain.Asset{Code: "EUR"}); err != nil {
		t.Fatalf("CreateAsset(EUR): %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "desk-a"}); err != nil {
		t.Fatalf("CreateGroup(desk-a): %v", err)
	}

	for _, limit := range []domain.LimitSpotFundsPnlBounds{
		{Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-1"},
		{
			Scope: domain.ScopeAccountGroup, AccountGroup: "desk-a",
			Currency: "EUR", LowerBound: "-2",
		},
		{
			Scope: domain.ScopeAccount, Account: "acc-1",
			Currency: "USD", LowerBound: "-3",
		},
	} {
		if err := rs.PutSpotFundsPnlBoundsLimit(ctx, limit); err != nil {
			t.Fatalf("PutSpotFundsPnlBoundsLimit(%s): %v", limit.Scope, err)
		}
	}

	limits, err := rs.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", err)
	}
	if len(limits) != 3 {
		t.Fatalf("limits len = %d, want 3: %+v", len(limits), limits)
	}
	gotCurrency := make(map[domain.LimitScope]string, len(limits))
	for _, limit := range limits {
		gotCurrency[limit.Scope] = limit.Currency
	}
	for scope, want := range map[domain.LimitScope]string{
		domain.ScopeGlobal:       "USD",
		domain.ScopeAccountGroup: "EUR",
		domain.ScopeAccount:      "USD",
	} {
		if got := gotCurrency[scope]; got != want {
			t.Fatalf("%s currency = %q, want %q", scope, got, want)
		}
	}

	err = rs.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeGlobal, Currency: "GHOST", LowerBound: "-4",
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("PutSpotFundsPnlBoundsLimit(unknown currency) error = %v, want ErrInvalid", err)
	}
}

func TestSpotFundsPnlBoundsUpsertOverwritesCurrency(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)
	if _, err := rs.CreateAsset(ctx, domain.Asset{Code: "EUR"}); err != nil {
		t.Fatalf("CreateAsset(EUR): %v", err)
	}
	for _, currency := range []string{"USD", "EUR"} {
		if err := rs.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
			Scope:      domain.ScopeAccount,
			Account:    "acc-1",
			Currency:   currency,
			LowerBound: "-100",
		}); err != nil {
			t.Fatalf("PutSpotFundsPnlBoundsLimit(%s): %v", currency, err)
		}
	}

	limits, err := rs.ListSpotFundsPnlBoundsLimits(ctx, "acc-1")
	if err != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", err)
	}
	if len(limits) != 1 {
		t.Fatalf("limits len = %d, want 1: %+v", len(limits), limits)
	}
	if limits[0].Currency != "EUR" {
		t.Fatalf("overwritten currency = %q, want EUR", limits[0].Currency)
	}
}

func TestDeleteAssetRequiresForceForSpotFundsPnlBoundsCurrencyDependent(t *testing.T) {
	ctx, rs := seedLimitFixtures(t)
	if err := rs.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-100",
	}); err != nil {
		t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
	}

	err := rs.DeleteAsset(ctx, "USD", false)
	var dependentErr domain.HasDependentsError
	if !errors.As(err, &dependentErr) {
		t.Fatalf("DeleteAsset(no force) error = %v, want HasDependentsError", err)
	}
	if len(dependentErr.Dependents) != 1 ||
		dependentErr.Dependents[0] != (domain.DependentCount{
			Kind: "limit_spot_funds_pnl_bound", Count: 1,
		}) {
		t.Fatalf("DeleteAsset(no force) dependents = %+v", dependentErr.Dependents)
	}
	if err := rs.DeleteAsset(ctx, "USD", true); err != nil {
		t.Fatalf("DeleteAsset(force): %v", err)
	}
	limits, err := rs.ListSpotFundsPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", err)
	}
	if len(limits) != 0 {
		t.Fatalf("limits after forced asset delete = %+v, want none", limits)
	}
}

func TestListPolicyRowsSortAndPage(t *testing.T) {
	ctx, rs := seedPolicyFixtures(t)

	// Sort by account ascending: acc-1's two barriers (ordered by the kind
	// tiebreak) then acc-2's rate barrier.
	page, err := rs.ListPolicyRows(ctx, fwstore.PolicyListFilter{
		Sort: fwstore.SortSpec{Column: "account"},
	})
	if err != nil {
		t.Fatalf("ListPolicyRows sort account: %v", err)
	}
	wantOrder := []string{
		"order_size_limit|acc-1",
		"rate_limit|acc-1",
		"rate_limit|acc-2",
	}
	if got := policyKeys(page.Rows); !equalStrings(got, wantOrder) {
		t.Fatalf("account order = %v, want %v", got, wantOrder)
	}

	// First page of two; Total still reports the full match count.
	page, err = rs.ListPolicyRows(ctx, fwstore.PolicyListFilter{
		Sort: fwstore.SortSpec{Column: "account"},
		Page: fwstore.PageSpec{Limit: 2, Offset: 0},
	})
	if err != nil {
		t.Fatalf("ListPolicyRows page 0: %v", err)
	}
	if page.Total != 3 {
		t.Fatalf("page 0 total = %d, want 3", page.Total)
	}
	if got := policyKeys(page.Rows); !equalStrings(got, wantOrder[:2]) {
		t.Fatalf("page 0 = %v, want %v", got, wantOrder[:2])
	}

	// Second page continues from the offset window.
	page, err = rs.ListPolicyRows(ctx, fwstore.PolicyListFilter{
		Sort: fwstore.SortSpec{Column: "account"},
		Page: fwstore.PageSpec{Limit: 2, Offset: 2},
	})
	if err != nil {
		t.Fatalf("ListPolicyRows page 1: %v", err)
	}
	if got := policyKeys(page.Rows); !equalStrings(got, wantOrder[2:]) {
		t.Fatalf("page 1 = %v, want %v", got, wantOrder[2:])
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
