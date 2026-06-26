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

// Orders-group tests: create/get/list round-trips addressed by external id,
// external-id opacity (22-char handle, surrogate id never surfaced), byte-exact
// lock round-trip, approval present/absent, cascade deletes (order and account),
// counts, settlement atomicity, and the unknown-FK-code error path.

package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/domain"
)

// seedOrderFixtures creates the dictionary rows the orders group references: an
// asset pair, a principal and an account. It returns the realm handle.
//
// Balances and signing keys belong to store groups that other sub-agents still
// stub; their public methods return ErrNotImplemented. So these tests seed and
// read those two tables directly with SQL through the shared connection (see
// seedBalance/getBalanceRow/seedSigningKey) rather than the stubbed public
// methods, which keeps the orders-group coverage self-contained.
func seedOrderFixtures(t *testing.T) (context.Context, RealmStore) {
	t.Helper()
	ctx := context.Background()
	_, rs := newTestStore(t)
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL", Title: "Apple"}); err != nil {
		t.Fatalf("CreateAsset(AAPL): %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "USD", Title: "Dollar"}); err != nil {
		t.Fatalf("CreateAsset(USD): %v", err)
	}
	if err := rs.CreatePrincipal(ctx, domain.Principal{Code: "operator"}); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return ctx, rs
}

// seedBalance inserts a balance row directly, resolving the account and asset to
// their surrogate ids; the public UpsertBalance is another sub-agent's stub.
func seedBalance(
	t *testing.T, ctx context.Context, rs RealmStore,
	account domain.AccountID, asset, available, held, incoming, realized string,
) {
	t.Helper()
	r := rs.(*realmStore)
	accountID, err := resolveAccountID(ctx, r.db(), account)
	if err != nil {
		t.Fatalf("resolve account %q: %v", account, err)
	}
	assetID, err := resolveAssetID(ctx, r.db(), asset)
	if err != nil {
		t.Fatalf("resolve asset %q: %v", asset, err)
	}
	if _, err := r.db().ExecContext(
		ctx,
		`INSERT OR REPLACE INTO balances
		 (account_id, asset_id, available, held, incoming, realized_pnl,
		  average_entry_price, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, '', ?)`,
		accountID, assetID, available, held, incoming, realized, nowStr(),
	); err != nil {
		t.Fatalf("seed balance: %v", err)
	}
}

// balanceRow holds the amount columns a test reads back from the balances table.
type balanceRow struct {
	available, held, incoming, realized string
}

// getBalanceRow reads a balance row directly; the public GetBalance is another
// sub-agent's stub. The bool is false when no row exists.
func getBalanceRow(
	t *testing.T, ctx context.Context, rs RealmStore, account domain.AccountID, asset string,
) (balanceRow, bool) {
	t.Helper()
	r := rs.(*realmStore)
	accountID, err := resolveAccountID(ctx, r.db(), account)
	if err != nil {
		t.Fatalf("resolve account %q: %v", account, err)
	}
	assetID, err := resolveAssetID(ctx, r.db(), asset)
	if err != nil {
		t.Fatalf("resolve asset %q: %v", asset, err)
	}
	var b balanceRow
	err = r.db().QueryRowContext(
		ctx,
		`SELECT available, held, incoming, realized_pnl
		 FROM balances WHERE account_id = ? AND asset_id = ?`,
		accountID, assetID,
	).Scan(&b.available, &b.held, &b.incoming, &b.realized)
	if errors.Is(err, sql.ErrNoRows) {
		return balanceRow{}, false
	}
	if err != nil {
		t.Fatalf("get balance row: %v", err)
	}
	return b, true
}

// seedSigningKey inserts a signing key row directly so an order_approvals row can
// satisfy its ON DELETE RESTRICT FK; the public UpsertSigningKey is another
// sub-agent's stub.
func seedSigningKey(t *testing.T, ctx context.Context, rs RealmStore, keyID string) {
	t.Helper()
	r := rs.(*realmStore)
	if _, err := r.db().ExecContext(
		ctx,
		`INSERT INTO signing_keys
		 (key_id, alg, private_key, public_key, created_at, active)
		 VALUES (?, 'ed25519', ?, ?, ?, 1)`,
		keyID, []byte{0x01}, []byte{0x02}, nowStr(),
	); err != nil {
		t.Fatalf("seed signing key: %v", err)
	}
}

// sampleOrder is a fully-populated submitted limit order on the seeded fixtures.
func sampleOrder() domain.Order {
	return domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Principal:   "operator",
		Source:      domain.SourcePanel,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "10",
		Price:       "150.25",
		Status:      domain.OrderStatusSubmitted,
		Lock:        []byte{0x00, 0x01, 0x02, 0xff, 0x10},
	}
}

func TestCreateOrderGetListRoundTrip(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	want := sampleOrder()
	created, err := rs.CreateOrder(ctx, want)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	// External id populated, 22 chars; submission time stamped.
	if created.ExternalID.IsZero() {
		t.Fatal("CreateOrder did not populate ExternalID")
	}
	if got := len(created.ExternalID.String()); got != domain.ExternalIDStringLen {
		t.Fatalf("external id string len = %d, want %d", got, domain.ExternalIDStringLen)
	}
	if created.At.IsZero() {
		t.Fatal("CreateOrder did not populate At")
	}

	// GetOrder by external id round-trips every field, references as codes.
	detail, err := rs.GetOrder(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	got := detail.Order
	if got.ExternalID != created.ExternalID {
		t.Fatalf("GetOrder external id = %v, want %v", got.ExternalID, created.ExternalID)
	}
	if got.Account != "acc-1" || got.BaseAsset != "AAPL" ||
		got.QuoteAsset != "USD" || got.Principal != "operator" {
		t.Fatalf("GetOrder references = %+v, want code references", got)
	}
	if got.Source != domain.SourcePanel || got.Side != domain.OrderSideBuy ||
		got.AmountKind != domain.OrderAmountKindQuantity ||
		got.AmountValue != "10" || got.Price != "150.25" ||
		got.Status != domain.OrderStatusSubmitted {
		t.Fatalf("GetOrder fields = %+v", got)
	}

	// ListOrders and ListAllOrders return the order by account.
	list, err := rs.ListOrders(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if len(list) != 1 || list[0].ExternalID != created.ExternalID {
		t.Fatalf("ListOrders = %+v", list)
	}
	all, err := rs.ListAllOrders(ctx, "acc-1", domain.SourcePanel)
	if err != nil {
		t.Fatalf("ListAllOrders: %v", err)
	}
	if len(all) != 1 || all[0].ExternalID != created.ExternalID {
		t.Fatalf("ListAllOrders = %+v", all)
	}

	// A non-positive limit returns an empty (non-nil) slice.
	empty, err := rs.ListOrders(ctx, "acc-1", "", 0)
	if err != nil {
		t.Fatalf("ListOrders(0): %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("ListOrders(0) = %+v, want non-nil empty", empty)
	}

	// A source that no order carries returns nothing.
	none, err := rs.ListAllOrders(ctx, "acc-1", domain.SourceMCP)
	if err != nil {
		t.Fatalf("ListAllOrders(mcp): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("ListAllOrders(mcp) = %+v, want empty", none)
	}
}

func TestOrderLockBlobRoundTripsByteIdentical(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	// A lock with the full byte range, including an embedded NUL.
	lock := []byte{0x00, 0xde, 0xad, 0xbe, 0xef, 0x00, 0x7f, 0x80, 0xff}
	o := sampleOrder()
	o.Lock = lock
	created, err := rs.CreateOrder(ctx, o)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	detail, err := rs.GetOrder(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if !bytes.Equal(detail.Order.Lock, lock) {
		t.Fatalf("lock round-trip = %v, want %v", detail.Order.Lock, lock)
	}

	// SetOrderLock rewrites the blob verbatim.
	newLock := []byte{0x11, 0x22, 0x33}
	if err := rs.SetOrderLock(ctx, created.ExternalID, newLock); err != nil {
		t.Fatalf("SetOrderLock: %v", err)
	}
	detail, _ = rs.GetOrder(ctx, created.ExternalID)
	if !bytes.Equal(detail.Order.Lock, newLock) {
		t.Fatalf("lock after SetOrderLock = %v, want %v", detail.Order.Lock, newLock)
	}

	// A nil lock clears the column: read back as nil, not empty bytes.
	if err := rs.SetOrderLock(ctx, created.ExternalID, nil); err != nil {
		t.Fatalf("SetOrderLock(nil): %v", err)
	}
	detail, _ = rs.GetOrder(ctx, created.ExternalID)
	if detail.Order.Lock != nil {
		t.Fatalf("lock after clear = %v, want nil", detail.Order.Lock)
	}

	// SetOrderLock on a missing order is ErrNotFound.
	var ghost domain.ExternalID
	ghost[0] = 0x99
	if err := rs.SetOrderLock(ctx, ghost, newLock); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetOrderLock(missing) = %v, want ErrNotFound", err)
	}
}

func TestCreateOrderWithNilLock(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	o := sampleOrder()
	o.Lock = nil
	created, err := rs.CreateOrder(ctx, o)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	detail, err := rs.GetOrder(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Lock != nil {
		t.Fatalf("nil lock round-trip = %v, want nil", detail.Order.Lock)
	}
}

func TestUpdateOrderStatus(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if err := rs.UpdateOrderStatus(ctx, created.ExternalID, domain.OrderStatusAccepted); err != nil {
		t.Fatalf("UpdateOrderStatus: %v", err)
	}
	detail, _ := rs.GetOrder(ctx, created.ExternalID)
	if detail.Order.Status != domain.OrderStatusAccepted {
		t.Fatalf("status = %q, want accepted", detail.Order.Status)
	}

	var ghost domain.ExternalID
	ghost[0] = 0x42
	if err := rs.UpdateOrderStatus(
		ctx, ghost, domain.OrderStatusCancelled,
	); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("UpdateOrderStatus(missing) = %v, want ErrNotFound", err)
	}
}

func TestPutOrderApprovalPresentAndAbsent(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	// A signing key the approval references (ON DELETE RESTRICT FK).
	seedSigningKey(t, ctx, rs, "key-1")

	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	// Absent before any PutOrderApproval.
	rstore, ok := rs.(*realmStore)
	if !ok {
		t.Fatal("realm store is not *realmStore")
	}
	if _, has, err := rstore.getOrderApproval(ctx, created.ExternalID); err != nil || has {
		t.Fatalf("approval before put: has=%v err=%v, want absent", has, err)
	}

	env := domain.OrderApproval{
		Token:     "tok-abc",
		KeyID:     "key-1",
		Alg:       "ed25519",
		Mode:      "immediate",
		IssuedAt:  "2026-06-26T10:00:00Z",
		ExpiresAt: "2026-06-26T10:05:00Z",
	}
	if err := rs.PutOrderApproval(ctx, created.ExternalID, env); err != nil {
		t.Fatalf("PutOrderApproval: %v", err)
	}
	got, has, err := rstore.getOrderApproval(ctx, created.ExternalID)
	if err != nil || !has {
		t.Fatalf("approval after put: has=%v err=%v, want present", has, err)
	}
	if got != env {
		t.Fatalf("approval = %+v, want %+v", got, env)
	}

	// Write-once: a second put with different fields does not clobber.
	clobber := env
	clobber.Token = "tok-CLOBBER"
	if err := rs.PutOrderApproval(ctx, created.ExternalID, clobber); err != nil {
		t.Fatalf("PutOrderApproval(retry): %v", err)
	}
	got, _, _ = rstore.getOrderApproval(ctx, created.ExternalID)
	if got.Token != "tok-abc" {
		t.Fatalf("approval token after retry = %q, want the first (write-once)", got.Token)
	}

	// A missing order is a tolerated no-op, not an error.
	var ghost domain.ExternalID
	ghost[0] = 0x55
	if err := rs.PutOrderApproval(ctx, ghost, env); err != nil {
		t.Fatalf("PutOrderApproval(missing) = %v, want nil (no-op)", err)
	}
}

func TestOrderEventsRoundTrip(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	ev1, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order:     created.ExternalID,
		Type:      domain.OrderEventSubmitted,
		Source:    domain.SourcePanel,
		Principal: "operator",
		Payload:   domain.OrderEventPayload{},
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent(1): %v", err)
	}
	if ev1.ExternalID.IsZero() || ev1.At.IsZero() {
		t.Fatalf("AppendOrderEvent did not populate id/at: %+v", ev1)
	}
	if _, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order:   created.ExternalID,
		Type:    domain.OrderEventPreTradeRejected,
		Source:  domain.SourcePanel,
		Payload: domain.OrderEventPayload{RejectCode: "limit_exceeded", RejectReason: "too big"},
	}); err != nil {
		t.Fatalf("AppendOrderEvent(2): %v", err)
	}

	events, err := rs.ListOrderEvents(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("ListOrderEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ListOrderEvents len = %d, want 2", len(events))
	}
	// Oldest first; parent order id surfaced as the external handle.
	if events[0].Type != domain.OrderEventSubmitted ||
		events[1].Type != domain.OrderEventPreTradeRejected {
		t.Fatalf("event order = %q, %q", events[0].Type, events[1].Type)
	}
	if events[0].Order != created.ExternalID {
		t.Fatalf("event parent = %v, want %v", events[0].Order, created.ExternalID)
	}
	if events[1].Payload.RejectCode != "limit_exceeded" ||
		events[1].Payload.RejectReason != "too big" {
		t.Fatalf("event payload = %+v", events[1].Payload)
	}
	if events[0].Principal != "operator" || events[1].Principal != "" {
		t.Fatalf("event principals = %q, %q", events[0].Principal, events[1].Principal)
	}

	// AppendOrderEvent on a missing order is ErrNotFound.
	var ghost domain.ExternalID
	ghost[0] = 0x77
	if _, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order: ghost, Type: domain.OrderEventFill, Source: domain.SourcePanel,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("AppendOrderEvent(missing order) = %v, want ErrNotFound", err)
	}
}

func TestTradesRoundTrip(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	tr, err := rs.CreateTrade(ctx, domain.Trade{
		Order:      created.ExternalID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Principal:  "operator",
		Source:     domain.SourcePanel,
		Side:       domain.OrderSideBuy,
		Quantity:   "5",
		Price:      "151.00",
		LockPrice:  "150.25",
	})
	if err != nil {
		t.Fatalf("CreateTrade: %v", err)
	}
	if tr.ExternalID.IsZero() || tr.At.IsZero() {
		t.Fatalf("CreateTrade did not populate id/at: %+v", tr)
	}

	list, err := rs.ListTrades(ctx, "acc-1", domain.SourcePanel, 10)
	if err != nil {
		t.Fatalf("ListTrades: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListTrades len = %d, want 1", len(list))
	}
	got := list[0]
	// Order surfaced outward as the order's external id, never a surrogate.
	if got.Order != created.ExternalID {
		t.Fatalf("trade order ref = %v, want %v", got.Order, created.ExternalID)
	}
	if got.Account != "acc-1" || got.BaseAsset != "AAPL" ||
		got.QuoteAsset != "USD" || got.Principal != "operator" {
		t.Fatalf("trade references = %+v, want code references", got)
	}
	if got.Quantity != "5" || got.Price != "151.00" || got.LockPrice != "150.25" {
		t.Fatalf("trade decimals = %+v", got)
	}

	all, err := rs.ListAllTrades(ctx, "acc-1", "")
	if err != nil {
		t.Fatalf("ListAllTrades: %v", err)
	}
	if len(all) != 1 || all[0].ExternalID != tr.ExternalID {
		t.Fatalf("ListAllTrades = %+v", all)
	}

	// Non-positive n returns a non-nil empty slice.
	empty, err := rs.ListTrades(ctx, "acc-1", "", 0)
	if err != nil {
		t.Fatalf("ListTrades(0): %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("ListTrades(0) = %+v, want non-nil empty", empty)
	}

	// The trade also appears on GetOrder's trade list.
	detail, _ := rs.GetOrder(ctx, created.ExternalID)
	if len(detail.Trades) != 1 || detail.Trades[0].ExternalID != tr.ExternalID {
		t.Fatalf("GetOrder trades = %+v", detail.Trades)
	}
}

func TestCountOrders(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	if n, err := rs.CountOrders(ctx); err != nil || n != 0 {
		t.Fatalf("CountOrders(empty) = %d, err=%v, want 0", n, err)
	}

	before := time.Now().UTC()
	for i := 0; i < 3; i++ {
		if _, err := rs.CreateOrder(ctx, sampleOrder()); err != nil {
			t.Fatalf("CreateOrder(%d): %v", i, err)
		}
	}

	n, err := rs.CountOrders(ctx)
	if err != nil || n != 3 {
		t.Fatalf("CountOrders = %d, err=%v, want 3", n, err)
	}

	since, err := rs.CountOrdersSince(ctx, before)
	if err != nil || since != 3 {
		t.Fatalf("CountOrdersSince(before) = %d, err=%v, want 3", since, err)
	}
	future, err := rs.CountOrdersSince(ctx, time.Now().UTC().Add(time.Hour))
	if err != nil || future != 0 {
		t.Fatalf("CountOrdersSince(future) = %d, err=%v, want 0", future, err)
	}
}

func TestRecordOrderSettlement(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	// Seed an opening balance so the carry-forward path is exercised.
	seedBalance(t, ctx, rs, "acc-1", "USD", "1000", "0", "0", "0")

	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	st := domain.OrderSettlement{
		Order:       created.ExternalID,
		Account:     "acc-1",
		OrderStatus: domain.OrderStatusFilled,
		AllowedFrom: domain.OrderStatusesEligibleForFill(),
		Balances: []domain.BalanceSettlement{
			{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult:    "850",
					RealizedPnlDelta: "12.50",
				},
			},
		},
		Trade: &domain.Trade{
			Order: created.ExternalID, Account: "acc-1",
			BaseAsset: "AAPL", QuoteAsset: "USD",
			Source: domain.SourcePanel, Side: domain.OrderSideBuy,
			Quantity: "10", Price: "150.25", LockPrice: "150.25",
		},
		Events: []domain.OrderEvent{
			{
				Order:  created.ExternalID,
				Type:   domain.OrderEventFill,
				Source: domain.SourcePanel,
				Payload: domain.OrderEventPayload{
					FillQuantity: "10", FillPrice: "150.25",
				},
			},
		},
		SetLock: true,
		Lock:    []byte{}, // explicit clear
	}
	if err := rs.RecordOrderSettlement(ctx, st); err != nil {
		t.Fatalf("RecordOrderSettlement: %v", err)
	}

	// Status advanced, lock cleared, fill event and trade present.
	detail, err := rs.GetOrder(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("status = %q, want filled", detail.Order.Status)
	}
	if detail.Order.Lock != nil {
		t.Fatalf("lock after clear = %v, want nil", detail.Order.Lock)
	}
	if len(detail.Trades) != 1 {
		t.Fatalf("trades = %d, want 1", len(detail.Trades))
	}
	foundFill := false
	for _, ev := range detail.Events {
		if ev.Type == domain.OrderEventFill {
			foundFill = true
		}
	}
	if !foundFill {
		t.Fatalf("fill event not appended: %+v", detail.Events)
	}

	// Balance updated: available from *Result, realized P&L delta-accumulated.
	bal, ok := getBalanceRow(t, ctx, rs, "acc-1", "USD")
	if !ok {
		t.Fatal("balance row missing after settlement")
	}
	if bal.available != "850" {
		t.Fatalf("available = %q, want 850", bal.available)
	}
	if bal.realized != "12.5" {
		t.Fatalf("realized_pnl = %q, want 12.5", bal.realized)
	}

	// A second settlement from a now-terminal status is rejected by the guard
	// with ErrConflict and nothing written.
	conflict := st
	conflict.OrderStatus = domain.OrderStatusCancelled
	if err := rs.RecordOrderSettlement(ctx, conflict); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("RecordOrderSettlement(terminal) = %v, want ErrConflict", err)
	}
	detail, _ = rs.GetOrder(ctx, created.ExternalID)
	if detail.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("status after rejected settlement = %q, want still filled", detail.Order.Status)
	}
}

func TestRecordOrderSettlementMissingOrder(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	var ghost domain.ExternalID
	ghost[0] = 0x88
	st := domain.OrderSettlement{
		Order:       ghost,
		Account:     "acc-1",
		OrderStatus: domain.OrderStatusFilled,
	}
	if err := rs.RecordOrderSettlement(ctx, st); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("RecordOrderSettlement(missing order) = %v, want ErrNotFound", err)
	}
}

func TestRecordOrderSettlementInMemoryHold(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	seedBalance(t, ctx, rs, "acc-1", "USD", "1000", "0", "0", "0")

	// A zero Order means an in-memory-only hold: only balance effects run, no
	// order/event writes, and a missing order is NOT an error.
	st := domain.OrderSettlement{
		Account: "acc-1",
		Balances: []domain.BalanceSettlement{
			{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{HeldResult: "100"}},
		},
	}
	if err := rs.RecordOrderSettlement(ctx, st); err != nil {
		t.Fatalf("RecordOrderSettlement(in-memory hold): %v", err)
	}
	bal, ok := getBalanceRow(t, ctx, rs, "acc-1", "USD")
	if !ok {
		t.Fatal("balance row missing after in-memory hold")
	}
	if bal.held != "100" || bal.available != "1000" {
		t.Fatalf("balance after hold = %+v", bal)
	}
}

func TestDeleteOrderCascadesChildren(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	seedSigningKey(t, ctx, rs, "key-1")

	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if _, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order: created.ExternalID, Type: domain.OrderEventSubmitted, Source: domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendOrderEvent: %v", err)
	}
	if _, err := rs.CreateTrade(ctx, domain.Trade{
		Order: created.ExternalID, Account: "acc-1",
		BaseAsset: "AAPL", QuoteAsset: "USD",
		Source: domain.SourcePanel, Side: domain.OrderSideBuy,
		Quantity: "1", Price: "150",
	}); err != nil {
		t.Fatalf("CreateTrade: %v", err)
	}
	if err := rs.PutOrderApproval(ctx, created.ExternalID, domain.OrderApproval{
		Token: "t", KeyID: "key-1", Alg: "ed25519", Mode: "immediate",
		IssuedAt: "2026-06-26T10:00:00Z", ExpiresAt: "2026-06-26T10:05:00Z",
	}); err != nil {
		t.Fatalf("PutOrderApproval: %v", err)
	}

	rstore := rs.(*realmStore)
	if c := countRows(t, ctx, rstore, "order_events"); c != 1 {
		t.Fatalf("order_events before delete = %d, want 1", c)
	}
	if c := countRows(t, ctx, rstore, "trades"); c != 1 {
		t.Fatalf("trades before delete = %d, want 1", c)
	}
	if c := countRows(t, ctx, rstore, "order_approvals"); c != 1 {
		t.Fatalf("order_approvals before delete = %d, want 1", c)
	}

	// Delete the order by surrogate id (schema-cascade test via raw SQL; the
	// public interface has no DeleteOrder). The schema must cascade children.
	if _, err := rstore.db().ExecContext(
		ctx, `DELETE FROM orders WHERE external_id = ?`, created.ExternalID.Bytes(),
	); err != nil {
		t.Fatalf("delete order: %v", err)
	}
	if c := countRows(t, ctx, rstore, "order_events"); c != 0 {
		t.Fatalf("order_events after delete = %d, want 0 (cascade)", c)
	}
	if c := countRows(t, ctx, rstore, "trades"); c != 0 {
		t.Fatalf("trades after delete = %d, want 0 (cascade)", c)
	}
	if c := countRows(t, ctx, rstore, "order_approvals"); c != 0 {
		t.Fatalf("order_approvals after delete = %d, want 0 (cascade)", c)
	}
}

func TestDeleteAccountCascadesOrders(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	if _, err := rs.CreateOrder(ctx, sampleOrder()); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	rstore := rs.(*realmStore)
	if c := countRows(t, ctx, rstore, "orders"); c != 1 {
		t.Fatalf("orders before account delete = %d, want 1", c)
	}

	// Deleting the account cascades to its orders (and thence their children).
	if _, err := rstore.db().ExecContext(
		ctx, `DELETE FROM accounts WHERE code = ?`, "acc-1",
	); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	if c := countRows(t, ctx, rstore, "orders"); c != 0 {
		t.Fatalf("orders after account delete = %d, want 0 (cascade)", c)
	}
}

func TestOrderUnknownFKCodeIsInvalid(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	// Unknown account code.
	bad := sampleOrder()
	bad.Account = "ghost"
	if _, err := rs.CreateOrder(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateOrder(unknown account) = %v, want ErrInvalid", err)
	}

	// Unknown base asset code.
	bad = sampleOrder()
	bad.BaseAsset = "GHOST"
	if _, err := rs.CreateOrder(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateOrder(unknown base asset) = %v, want ErrInvalid", err)
	}

	// Unknown principal code.
	bad = sampleOrder()
	bad.Principal = "nobody"
	if _, err := rs.CreateOrder(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateOrder(unknown principal) = %v, want ErrInvalid", err)
	}

	// A trade naming an unknown order is ErrNotFound (the order is addressed by
	// external id, resolved via lookupOrderID).
	var ghost domain.ExternalID
	ghost[0] = 0x33
	if _, err := rs.CreateTrade(ctx, domain.Trade{
		Order: ghost, Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Source: domain.SourcePanel, Side: domain.OrderSideBuy, Quantity: "1", Price: "1",
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("CreateTrade(unknown order) = %v, want ErrNotFound", err)
	}
}

// countRows returns the row count of a table, used to assert cascade behavior
// through the shared connection.
func countRows(t *testing.T, ctx context.Context, r *realmStore, table string) int {
	t.Helper()
	var n int
	if err := r.db().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestCreateOrderSuppliedExternalIDUsedVerbatim verifies that a caller-supplied
// external id is written verbatim: the returned id equals the supplied one and
// the row is addressable by it.
func TestCreateOrderSuppliedExternalIDUsedVerbatim(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	supplied, err := domain.ParseExternalID("AAECAwQFBgcICQoLDA0ODw")
	if err != nil {
		t.Fatalf("ParseExternalID: %v", err)
	}
	o := sampleOrder()
	o.ExternalID = supplied

	created, err := rs.CreateOrder(ctx, o)
	if err != nil {
		t.Fatalf("CreateOrder(supplied id): %v", err)
	}
	if created.ExternalID != supplied {
		t.Fatalf("returned id = %q, want supplied %q",
			created.ExternalID.String(), supplied.String())
	}

	got, err := rs.GetOrder(ctx, supplied)
	if err != nil {
		t.Fatalf("GetOrder(supplied id): %v", err)
	}
	if got.Order.ExternalID != supplied {
		t.Fatalf("row external id = %q, want %q",
			got.Order.ExternalID.String(), supplied.String())
	}
	if got.Order.AmountValue != o.AmountValue {
		t.Fatalf("AmountValue = %q, want %q", got.Order.AmountValue, o.AmountValue)
	}
	if got.Order.Price != o.Price {
		t.Fatalf("Price = %q, want %q", got.Order.Price, o.Price)
	}
}

// TestCreateOrderDuplicateSuppliedExternalIDConflicts verifies that creating a
// second order with the same supplied external id is rejected with
// domain.ErrAlreadyExists and that no second row is written.
func TestCreateOrderDuplicateSuppliedExternalIDConflicts(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	r := rs.(*realmStore)

	supplied, err := domain.ParseExternalID("AAECAwQFBgcICQoLDA0ODw")
	if err != nil {
		t.Fatalf("ParseExternalID: %v", err)
	}
	first := sampleOrder()
	first.ExternalID = supplied
	if _, err := rs.CreateOrder(ctx, first); err != nil {
		t.Fatalf("CreateOrder(first): %v", err)
	}

	second := sampleOrder()
	second.ExternalID = supplied
	_, err = rs.CreateOrder(ctx, second)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateOrder(dup id) = %v, want ErrAlreadyExists", err)
	}
	if msg := err.Error(); !strings.Contains(msg, supplied.String()) {
		t.Fatalf("conflict error %q does not name the external id %q",
			msg, supplied.String())
	}

	if n := countRows(t, ctx, r, "orders"); n != 1 {
		t.Fatalf("orders row count = %d, want 1 (no second row)", n)
	}
}

// TestCreateOrderGeneratesExternalIDWhenAbsent verifies that a zero supplied id
// is replaced by a freshly generated 22-char external id.
func TestCreateOrderGeneratesExternalIDWhenAbsent(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	o := sampleOrder()
	if !o.ExternalID.IsZero() {
		t.Fatal("sample order must carry a zero external id")
	}
	created, err := rs.CreateOrder(ctx, o)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if created.ExternalID.IsZero() {
		t.Fatal("CreateOrder returned a zero external id")
	}
	if got := len(created.ExternalID.String()); got != domain.ExternalIDStringLen {
		t.Fatalf("generated id len = %d, want %d", got, domain.ExternalIDStringLen)
	}
}
