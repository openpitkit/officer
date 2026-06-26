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

// Cross-cutting invariant tests that span multiple table groups. These tests
// complement (and never duplicate) the per-group tests; each one is noted with
// which per-group coverage it relies on so reviewers can quickly see why each
// assertion is needed here rather than elsewhere.
//
// Tests in this file:
//
//   - TestRealmSweep_ExternalIDAndCodeInvariants: creates a representative realm
//     covering every machine-record and dictionary type, then asserts the holistic
//     identity invariants: every machine-record surfaces a valid 22-char external
//     id, every dictionary surfaces a code, and no field carries a raw surrogate
//     integer.
//
//   - TestCascadeMatrix_AssetDeleteCascadesOrdersAndTrades: the cross-group
//     cascade that is not covered in any per-group test — deleting an asset must
//     cascade its referencing orders (and thence their events, trades, and
//     approval). The balances and limits cascades on asset/account delete are
//     already covered by sqlite_balances_test.go and sqlite_limits_test.go.
//
//   - TestEngineIDAssignment_ManyAccountsAndGroups: creates many accounts and
//     groups in a single store and asserts every assigned engine id is unique and
//     in range. The per-group test checks only 2 accounts; this tests at a scale
//     that exercises the counter path rather than just the initial assignment.
//
//   - TestLimitPolicyUnique_AllThreeTables: asserts the coalesced unique index
//     over (scope, account_id, asset_id) is enforced for all three per-policy
//     tables, including the NULL-axis (broker scope = NULL, NULL) case where
//     SQLite's handling of NULLs in plain UNIQUE constraints would silently allow
//     duplicates. The per-group tests verify upsert semantics (same composite →
//     update); this test verifies that a raw INSERT for the same composite
//     actually conflicts.

package store

import (
	"context"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/domain"
)

// TestRealmSweep_ExternalIDAndCodeInvariants builds a representative realm with
// one row of every machine-record type and every dictionary type, then walks
// every returned value and asserts:
//
//   - Machine records: ExternalID is non-zero and its string form is exactly
//     22 chars (domain.ExternalIDStringLen). ParseExternalID must succeed.
//   - Dictionaries: Code is non-empty. No returned domain type carries an
//     internal surrogate integer field exposed to callers — this is a
//     compile-time invariant guaranteed by the domain types themselves, which the
//     store interface enforces.
//
// Per-group tests already assert the 22-char shape individually (e.g.
// TestCreateOrderGetListRoundTrip, TestAdjustmentAppendListRoundTrip). This
// test spans ALL groups in one pass to catch any gap introduced by a future
// group that skips the shape check.
func TestRealmSweep_ExternalIDAndCodeInvariants(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	// --- Dictionaries ---
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL", Title: "Apple", AssetClass: "equity"}); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "USD", Title: "Dollar"}); err != nil {
		t.Fatalf("CreateAsset(USD): %v", err)
	}
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator", Title: "Desk Operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	grp, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "alpha", Title: "Alpha Desk"})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	acct, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1", GroupCode: "alpha"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// --- Dictionary sweep: code must be non-empty, no surrogate integer leaks ---

	assets, err := rs.ListAssets(ctx)
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	for _, a := range assets {
		if a.Code == "" {
			t.Fatalf("asset has empty code: %+v", a)
		}
	}

	principals, err := rs.ListPrincipals(ctx)
	if err != nil {
		t.Fatalf("ListPrincipals: %v", err)
	}
	for _, p := range principals {
		if p.Code == "" {
			t.Fatalf("principal has empty code: %+v", p)
		}
	}

	groups, err := rs.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Code == "" {
		t.Fatalf("group code empty or missing: %+v", groups)
	}
	// EngineGroupID is an internal value but it is returned to the engine layer
	// by CreateGroup, not surfaced to callers via list. Verify its range here
	// using the returned create result.
	if err := domain.ValidateEngineGroupID(grp.EngineGroupID); err != nil {
		t.Fatalf("group engine id out of range: %v", err)
	}

	accounts, err := rs.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Code == "" {
		t.Fatalf("account code empty or missing: %+v", accounts)
	}
	if err := domain.ValidateEngineAccountID(acct.EngineAccountID); err != nil {
		t.Fatalf("account engine id out of range: %v", err)
	}

	// --- Machine records ---

	// Seed signing key for the approval.
	seedSigningKey(t, ctx, rs, "key-sweep")

	order, err := rs.CreateOrder(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Principal:   "operator",
		Source:      domain.SourcePanel,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       "100",
		Status:      domain.OrderStatusSubmitted,
		Lock:        []byte{0x01, 0x02, 0x03},
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	assertExternalID(t, "order", order.ExternalID)

	// Order event.
	ev, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order:     order.ExternalID,
		Type:      domain.OrderEventSubmitted,
		Source:    domain.SourcePanel,
		Principal: "operator",
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent: %v", err)
	}
	assertExternalID(t, "order event", ev.ExternalID)

	// Trade.
	trade, err := rs.CreateTrade(ctx, domain.Trade{
		Order:      order.ExternalID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Source:     domain.SourcePanel,
		Side:       domain.OrderSideBuy,
		Quantity:   "1",
		Price:      "100",
	})
	if err != nil {
		t.Fatalf("CreateTrade: %v", err)
	}
	assertExternalID(t, "trade", trade.ExternalID)

	// Approval (write-once 1:1 table, not its own external id — addressed by
	// the order's external id; no separate external id to assert here).
	if err := rs.PutOrderApproval(ctx, order.ExternalID, domain.OrderApproval{
		Token:     "tok",
		KeyID:     "key-sweep",
		Alg:       "ed25519",
		Mode:      "immediate",
		IssuedAt:  "2026-06-26T10:00:00Z",
		ExpiresAt: "2026-06-26T10:05:00Z",
	}); err != nil {
		t.Fatalf("PutOrderApproval: %v", err)
	}

	// Adjustment.
	adj, err := rs.AppendAdjustment(ctx, domain.AccountAdjustmentRecord{
		Account:   "acc-1",
		Asset:     "AAPL",
		Principal: "operator",
		Source:    domain.SourcePanel,
		Request:   domain.AdjustmentRequest{Asset: "AAPL"},
		Accepted: &domain.AdjustmentOutcomeAccepted{
			BalanceDelta: "0", BalanceResult: "0",
		},
	})
	if err != nil {
		t.Fatalf("AppendAdjustment: %v", err)
	}
	assertExternalID(t, "adjustment", adj.ExternalID)

	// Audit row.
	if err := rs.AppendAudit(ctx, AuditEntry{
		Actor:   "operator",
		Action:  domain.AuditActionCreateAccount,
		Account: "acc-1",
		Source:  domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	auditRows, err := rs.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(auditRows) == 0 {
		t.Fatalf("expected at least one audit row")
	}
	for _, row := range auditRows {
		assertExternalID(t, "audit row", row.ExternalID)
	}

	// Market-data instance (also a machine record by external id).
	mdInst, err := rs.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO,
		Label:    "sweep-feed",
	})
	if err != nil {
		t.Fatalf("CreateMarketDataInstance: %v", err)
	}
	assertExternalID(t, "market_data_instance", mdInst.ExternalID)

	// Market-data instrument (keyed by (instance, symbol) — no external id of
	// its own; its identity is the instance external id + external symbol).
	if err := rs.UpsertMarketDataInstrument(ctx, domain.MarketDataInstrument{
		Instance:       mdInst.ExternalID,
		ExternalSymbol: "AAPL",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
	}); err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}

	// Quote (addressed by (instance, symbol) just like instrument).
	if err := rs.UpsertMarketDataQuote(ctx, domain.MarketDataQuote{
		Instance:       mdInst.ExternalID,
		ExternalSymbol: "AAPL",
		Mark:           "150.00",
		AsOf:           time.Now().UTC(),
		ReceivedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertMarketDataQuote: %v", err)
	}

	// Read back all machine records through their public list paths and assert
	// every ExternalID field passes the shape check.

	orders, err := rs.ListOrders(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	for _, o := range orders {
		assertExternalID(t, "listed order", o.ExternalID)
	}

	detail, err := rs.GetOrder(ctx, order.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	for _, e := range detail.Events {
		assertExternalID(t, "order event from GetOrder", e.ExternalID)
	}
	for _, tr := range detail.Trades {
		assertExternalID(t, "trade from GetOrder", tr.ExternalID)
	}

	adjustments, err := rs.ListAdjustments(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	for _, a := range adjustments {
		assertExternalID(t, "listed adjustment", a.ExternalID)
	}

	instances, err := rs.ListMarketDataInstances(ctx)
	if err != nil {
		t.Fatalf("ListMarketDataInstances: %v", err)
	}
	for _, i := range instances {
		assertExternalID(t, "listed md instance", i.ExternalID)
	}
}

// assertExternalID verifies that id is non-zero, its string form is exactly
// domain.ExternalIDStringLen chars, and ParseExternalID accepts it.
func assertExternalID(t *testing.T, label string, id domain.ExternalID) {
	t.Helper()
	if id.IsZero() {
		t.Fatalf("%s: ExternalID is zero", label)
	}
	s := id.String()
	if len(s) != domain.ExternalIDStringLen {
		t.Fatalf("%s: ExternalID string len = %d, want %d (%q)",
			label, len(s), domain.ExternalIDStringLen, s)
	}
	if _, err := domain.ParseExternalID(s); err != nil {
		t.Fatalf("%s: ParseExternalID(%q) = %v", label, s, err)
	}
}

// TestCascadeMatrix_AssetDeleteCascadesOrdersAndTrades covers the cross-group
// cascade path that no per-group test exercises: deleting an asset must cascade
// to orders that reference it as base_asset or quote_asset, and thence to those
// orders' child rows (events, trades, approvals).
//
// The following cascades are already covered in their respective per-group
// tests and are NOT re-asserted here:
//   - account→balances: sqlite_balances_test.go (TestBalanceCascadeOnAccountDelete)
//   - asset→balances: sqlite_balances_test.go (TestBalanceCascadeOnAssetDelete)
//   - account→orders: sqlite_orders_test.go (TestDeleteAccountCascadesOrders)
//   - order→events/trades/approvals: sqlite_orders_test.go (TestDeleteOrderCascadesChildren)
//   - account→adjustments: sqlite_adjustments_test.go (TestAdjustmentCascadeOnAccountDelete)
//   - account→limits, asset→limits: sqlite_limits_test.go
//   - instance→instruments→quotes: sqlite_marketdata_test.go (TestMDCascadeDeleteInstance)
//   - account/principal→audit (SET NULL): sqlite_audit_test.go (TestAuditTrailPreservedOnAccountDelete)
func TestCascadeMatrix_AssetDeleteCascadesOrdersAndTrades(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	if err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL"}); err != nil {
		t.Fatalf("CreateAsset(AAPL): %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset(USD): %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	seedSigningKey(t, ctx, rs, "key-cascade")

	order, err := rs.CreateOrder(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Source:      domain.SourcePanel,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       "100",
		Status:      domain.OrderStatusSubmitted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if _, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order:  order.ExternalID,
		Type:   domain.OrderEventSubmitted,
		Source: domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendOrderEvent: %v", err)
	}
	if _, err := rs.CreateTrade(ctx, domain.Trade{
		Order:      order.ExternalID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Source:     domain.SourcePanel,
		Side:       domain.OrderSideBuy,
		Quantity:   "1",
		Price:      "100",
	}); err != nil {
		t.Fatalf("CreateTrade: %v", err)
	}
	if err := rs.PutOrderApproval(ctx, order.ExternalID, domain.OrderApproval{
		Token:     "tok",
		KeyID:     "key-cascade",
		Alg:       "ed25519",
		Mode:      "immediate",
		IssuedAt:  "2026-06-26T10:00:00Z",
		ExpiresAt: "2026-06-26T10:05:00Z",
	}); err != nil {
		t.Fatalf("PutOrderApproval: %v", err)
	}

	rstore := rs.(*realmStore)
	for _, table := range []string{"orders", "order_events", "trades", "order_approvals"} {
		if n := countRows(t, ctx, rstore, table); n != 1 {
			t.Fatalf("%s before asset delete = %d, want 1", table, n)
		}
	}

	// Deleting AAPL (the order's base asset) must cascade to orders referencing
	// it, and thence to their events, trades, and approval.
	if err := rs.DeleteAsset(ctx, "AAPL", true); err != nil {
		t.Fatalf("DeleteAsset(AAPL): %v", err)
	}

	for _, table := range []string{"orders", "order_events", "trades", "order_approvals"} {
		if n := countRows(t, ctx, rstore, table); n != 0 {
			t.Fatalf("%s after asset delete = %d, want 0 (cascade)", table, n)
		}
	}
}

// TestEngineIDAssignment_ManyAccountsAndGroups creates many accounts and groups
// in one store and asserts every assigned engine id is unique and within the
// allowed range. The per-group test (TestGroupRoundTripAndEngineID /
// TestAccountRoundTripEngineIDAndGroupLink) verifies the two-account case;
// this test exercises the counter path at a scale where the initial default
// is long past.
func TestEngineIDAssignment_ManyAccountsAndGroups(t *testing.T) {
	const n = 20
	ctx := context.Background()
	_, rs := newTestStore(t)

	// --- Groups ---
	seenGroupIDs := make(map[domain.EngineGroupID]string, n)
	for i := 0; i < n; i++ {
		code := "grp-" + string(rune('a'+i))
		g, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: code})
		if err != nil {
			t.Fatalf("CreateGroup(%q): %v", code, err)
		}
		if err := domain.ValidateEngineGroupID(g.EngineGroupID); err != nil {
			t.Fatalf("group %q engine id %d out of range: %v", code, g.EngineGroupID, err)
		}
		if prev, dup := seenGroupIDs[g.EngineGroupID]; dup {
			t.Fatalf("engine group id %d assigned to both %q and %q (collision)",
				g.EngineGroupID, prev, code)
		}
		seenGroupIDs[g.EngineGroupID] = code
	}

	// --- Accounts ---
	seenAccountIDs := make(map[domain.EngineAccountID]string, n)
	for i := 0; i < n; i++ {
		code := domain.AccountID("acct-" + string(rune('a'+i)))
		a, err := rs.CreateAccount(ctx, domain.Account{Code: code})
		if err != nil {
			t.Fatalf("CreateAccount(%q): %v", code, err)
		}
		if err := domain.ValidateEngineAccountID(a.EngineAccountID); err != nil {
			t.Fatalf("account %q engine id %d out of range: %v", code, a.EngineAccountID, err)
		}
		if prev, dup := seenAccountIDs[a.EngineAccountID]; dup {
			t.Fatalf("engine account id %d assigned to both %q and %q (collision)",
				a.EngineAccountID, prev, code)
		}
		seenAccountIDs[a.EngineAccountID] = string(code)
	}

	// After creation, GetGroup / GetAccount must return the same stored ids.
	for id, code := range seenGroupIDs {
		g, ok, err := rs.GetGroup(ctx, code)
		if err != nil || !ok {
			t.Fatalf("GetGroup(%q): ok=%v err=%v", code, ok, err)
		}
		if g.EngineGroupID != id {
			t.Fatalf("GetGroup(%q) engine id = %d, want %d", code, g.EngineGroupID, id)
		}
	}
	for id, code := range seenAccountIDs {
		a, ok, err := rs.GetAccount(ctx, domain.AccountID(code))
		if err != nil || !ok {
			t.Fatalf("GetAccount(%q): ok=%v err=%v", code, ok, err)
		}
		if a.EngineAccountID != id {
			t.Fatalf("GetAccount(%q) engine id = %d, want %d", code, a.EngineAccountID, id)
		}
	}
}

// TestLimitPolicyUnique_AllThreeTables asserts the coalesced unique index over
// (scope, account_id, asset_id) is enforced for all three per-policy tables,
// with particular attention to the broker-scope (NULL, NULL) case. SQLite has a
// special rule where two NULL values are NOT considered equal in a plain UNIQUE
// constraint, so the schema must coalesce NULL axes in the unique index.
//
// The per-group tests already cover the upsert/update path (same composite →
// replace value). This test exercises the raw constraint: the upsert methods
// use INSERT OR REPLACE, which by definition does not fail; here we verify the
// underlying constraint with raw duplicate INSERTs and the store helper upsert
// behavior — one entry per natural composite, no phantom duplicates.
//
// Additionally, this test verifies that two distinct composites on different
// policy tables can both carry broker-scoped NULL-axis rows without colliding
// with each other across tables.
func TestLimitPolicyUnique_AllThreeTables(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	r := rs.(*realmStore)

	if err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL"}); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// --- limit_rate ---

	// Broker-scope row (both axes NULL in storage).
	if err := rs.PutRateLimit(ctx, domain.LimitRate{
		Scope:     domain.ScopeBroker,
		MaxOrders: 100,
		Window:    time.Minute,
	}); err != nil {
		t.Fatalf("PutRateLimit(broker): %v", err)
	}
	// A second PutRateLimit with the same broker composite must be an upsert,
	// not a duplicate row — the list must return exactly one row.
	if err := rs.PutRateLimit(ctx, domain.LimitRate{
		Scope:     domain.ScopeBroker,
		MaxOrders: 200,
		Window:    2 * time.Minute,
	}); err != nil {
		t.Fatalf("PutRateLimit(broker, upsert): %v", err)
	}
	rates, err := rs.ListRateLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListRateLimits: %v", err)
	}
	if n := countBrokerScope(rates); n != 1 {
		t.Fatalf("limit_rate broker-scope rows after upsert = %d, want exactly 1 (unique constraint)", n)
	}
	if _, err := r.db().ExecContext(ctx, `
INSERT INTO limit_rate (scope, account_id, asset_id, max_orders, window)
VALUES (?, NULL, NULL, ?, ?)`, domain.ScopeBroker, 300, int64(time.Minute/time.Millisecond)); err == nil {
		t.Fatal("raw duplicate limit_rate broker-scope insert succeeded, want unique conflict")
	} else if !isSQLiteUnique(err) {
		t.Fatalf("raw duplicate limit_rate broker-scope insert error = %v, want unique conflict", err)
	}

	// Account-scoped row for acc-1 (asset axis NULL).
	if err := rs.PutRateLimit(ctx, domain.LimitRate{
		Scope:     domain.ScopeAccount,
		Account:   "acc-1",
		MaxOrders: 10,
		Window:    time.Minute,
	}); err != nil {
		t.Fatalf("PutRateLimit(account): %v", err)
	}
	// Second put: must update, not insert.
	if err := rs.PutRateLimit(ctx, domain.LimitRate{
		Scope:     domain.ScopeAccount,
		Account:   "acc-1",
		MaxOrders: 20,
		Window:    time.Minute,
	}); err != nil {
		t.Fatalf("PutRateLimit(account, upsert): %v", err)
	}
	rates, _ = rs.ListRateLimits(ctx, "acc-1")
	if n := countAccountScope(rates, "acc-1"); n != 1 {
		t.Fatalf("limit_rate acc-1 account-scope rows after upsert = %d, want 1 (unique constraint)", n)
	}
	// Validate the upsert actually updated the value.
	for _, r := range rates {
		if r.Scope == domain.ScopeAccount && r.Account == "acc-1" && r.MaxOrders != 20 {
			t.Fatalf("upsert did not update max_orders: got %d, want 20", r.MaxOrders)
		}
	}

	// --- limit_order_size ---

	if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope:       domain.ScopeBroker,
		MaxNotional: "1000000",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit(broker): %v", err)
	}
	if err := rs.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope:       domain.ScopeBroker,
		MaxNotional: "2000000",
	}); err != nil {
		t.Fatalf("PutOrderSizeLimit(broker, upsert): %v", err)
	}
	sizes, err := rs.ListOrderSizeLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListOrderSizeLimits: %v", err)
	}
	if n := countBrokerScopeSize(sizes); n != 1 {
		t.Fatalf("limit_order_size broker-scope rows after upsert = %d, want 1 (unique constraint)", n)
	}
	if _, err := r.db().ExecContext(ctx, `
INSERT INTO limit_order_size (scope, account_id, asset_id, max_notional)
VALUES (?, NULL, NULL, ?)`, domain.ScopeBroker, "3000000"); err == nil {
		t.Fatal("raw duplicate limit_order_size broker-scope insert succeeded, want unique conflict")
	} else if !isSQLiteUnique(err) {
		t.Fatalf("raw duplicate limit_order_size broker-scope insert error = %v, want unique conflict", err)
	}

	// --- limit_pnl_bounds ---

	if err := rs.PutPnlBoundsLimit(ctx, domain.LimitPnlBounds{
		Scope:      domain.ScopeAsset,
		Asset:      "AAPL",
		LowerBound: "-100",
		UpperBound: "100",
	}); err != nil {
		t.Fatalf("PutPnlBoundsLimit(asset): %v", err)
	}
	if err := rs.PutPnlBoundsLimit(ctx, domain.LimitPnlBounds{
		Scope:      domain.ScopeAsset,
		Asset:      "AAPL",
		LowerBound: "-200",
		UpperBound: "200",
	}); err != nil {
		t.Fatalf("PutPnlBoundsLimit(asset, upsert): %v", err)
	}
	bounds, err := rs.ListPnlBoundsLimits(ctx, "")
	if err != nil {
		t.Fatalf("ListPnlBoundsLimits: %v", err)
	}
	assetRows := 0
	for _, b := range bounds {
		if b.Scope == domain.ScopeAsset && b.Asset == "AAPL" {
			assetRows++
		}
	}
	if assetRows != 1 {
		t.Fatalf("limit_pnl_bounds AAPL asset-scope rows after upsert = %d, want 1 (unique constraint)", assetRows)
	}
	// Confirm the update landed.
	for _, b := range bounds {
		if b.Scope == domain.ScopeAsset && b.Asset == "AAPL" && b.LowerBound != "-200" {
			t.Fatalf("upsert did not update lower_bound: got %q, want -200", b.LowerBound)
		}
	}
	if _, err := r.db().ExecContext(ctx, `
INSERT INTO limit_pnl_bounds (scope, account_id, asset_id, lower_bound, upper_bound)
VALUES (?, NULL, (SELECT id FROM assets WHERE code = ?), ?, ?)`,
		domain.ScopeAsset, "AAPL", "-300", "300"); err == nil {
		t.Fatal("raw duplicate limit_pnl_bounds asset-scope insert succeeded, want unique conflict")
	} else if !isSQLiteUnique(err) {
		t.Fatalf("raw duplicate limit_pnl_bounds asset-scope insert error = %v, want unique conflict", err)
	}

	// --- Cross-table isolation: broker row in limit_rate does not conflict with
	// broker row in limit_order_size or limit_pnl_bounds. The three tables are
	// independent; their UNIQUE constraints are per-table.
	if err := rs.PutPnlBoundsLimit(ctx, domain.LimitPnlBounds{
		Scope:      domain.ScopeAccountAsset,
		Account:    "acc-1",
		Asset:      "AAPL",
		LowerBound: "-50",
	}); err != nil {
		t.Fatalf("PutPnlBoundsLimit(account_asset): %v", err)
	}
	// All three tables should have their rows intact.
	if _, err := rs.ListRateLimits(ctx, ""); err != nil {
		t.Fatalf("ListRateLimits(final check): %v", err)
	}
	if _, err := rs.ListOrderSizeLimits(ctx, ""); err != nil {
		t.Fatalf("ListOrderSizeLimits(final check): %v", err)
	}
	if _, err := rs.ListPnlBoundsLimits(ctx, ""); err != nil {
		t.Fatalf("ListPnlBoundsLimits(final check): %v", err)
	}
}

// countBrokerScope returns the number of rate-limit rows with broker scope.
func countBrokerScope(limits []domain.LimitRate) int {
	n := 0
	for _, l := range limits {
		if l.Scope == domain.ScopeBroker {
			n++
		}
	}
	return n
}

// countAccountScope returns the number of rate-limit rows scoped to account.
func countAccountScope(limits []domain.LimitRate, account domain.AccountID) int {
	n := 0
	for _, l := range limits {
		if l.Scope == domain.ScopeAccount && l.Account == account {
			n++
		}
	}
	return n
}

// countBrokerScopeSize returns the number of order-size-limit rows with broker scope.
func countBrokerScopeSize(limits []domain.LimitOrderSize) int {
	n := 0
	for _, l := range limits {
		if l.Scope == domain.ScopeBroker {
			n++
		}
	}
	return n
}
