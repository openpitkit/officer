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
// lock round-trip, event attestation present/absent, cascade deletes (order and
// account), counts, settlement atomicity, and the unknown-FK-code error path.

package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
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
	accountID, err := resolveAccountID(ctx, r.rawDB(), account)
	if err != nil {
		t.Fatalf("resolve account %q: %v", account, err)
	}
	assetID, err := resolveAssetID(ctx, r.rawDB(), asset)
	if err != nil {
		t.Fatalf("resolve asset %q: %v", asset, err)
	}
	if _, err := r.rawDB().ExecContext(
		ctx,
		`INSERT OR REPLACE INTO balance
		 (account_id, asset_id, available, held, incoming, realized_pnl,
		  average_entry_price, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, '', ?)`,
		accountID, assetID, available, held, incoming, realized, nowStr(),
	); err != nil {
		t.Fatalf("seed balance: %v", err)
	}
}

// balanceRow holds the amount columns a test reads back from the balance table.
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
	accountID, err := resolveAccountID(ctx, r.rawDB(), account)
	if err != nil {
		t.Fatalf("resolve account %q: %v", account, err)
	}
	assetID, err := resolveAssetID(ctx, r.rawDB(), asset)
	if err != nil {
		t.Fatalf("resolve asset %q: %v", asset, err)
	}
	var b balanceRow
	err = r.rawDB().QueryRowContext(
		ctx,
		`SELECT available, held, incoming, realized_pnl
		 FROM balance WHERE account_id = ? AND asset_id = ?`,
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

// seedSigningKey inserts a signing key row directly so an event_attestation row
// can satisfy its ON DELETE RESTRICT FK; the public UpsertSigningKey is another
// sub-agent's stub.
func seedSigningKey(t *testing.T, ctx context.Context, rs RealmStore, keyID string) {
	t.Helper()
	r := rs.(*realmStore)
	if _, err := r.rawDB().ExecContext(
		ctx,
		`INSERT INTO signing_key
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

func TestOrderListRowsSortFilterPageOrdersDecimalsNumerically(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	// A negative amount and an arbitrary-precision amount (beyond any fixed
	// sort-key window) prove the column's DECIMAL collation orders numerically.
	amounts := []string{
		"10", "2", "2.5", "2.5000000001", "-3",
		"1000000000000000000000000000000000.0001",
	}
	for _, value := range amounts {
		order := sampleOrder()
		order.AmountValue = value
		order.Price = "100"
		if _, err := rs.CreateOrder(ctx, order); err != nil {
			t.Fatalf("CreateOrder(%s): %v", value, err)
		}
	}

	page, err := rs.ListOrderRows(ctx, fwstore.OrderListFilter{
		Sort: fwstore.SortSpec{Column: "amountValue"},
		Page: fwstore.PageSpec{Limit: 1, Offset: 1},
	})
	if err != nil {
		t.Fatalf("ListOrderRows(sort page): %v", err)
	}
	if page.Total != 6 || len(page.Rows) != 1 {
		t.Fatalf("page total/len = %d/%d, want 6/1", page.Total, len(page.Rows))
	}
	// Ascending order is -3, 2, 2.5, ...; the second row is 2.
	if got := page.Rows[0].Order.AmountValue; got != "2" {
		t.Fatalf("second sorted amount = %q, want 2", got)
	}

	min := "2.5"
	page, err = rs.ListOrderRows(ctx, fwstore.OrderListFilter{
		Amount: fwstore.DecimalRangeFilter{Min: &min},
		Sort:   fwstore.SortSpec{Column: "amountValue"},
	})
	if err != nil {
		t.Fatalf("ListOrderRows(range): %v", err)
	}
	got := []string{}
	for _, row := range page.Rows {
		got = append(got, row.Order.AmountValue)
	}
	want := []string{
		"2.5", "2.5000000001", "10",
		"1000000000000000000000000000000000.0001",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("range sorted amounts = %v, want %v", got, want)
	}
}

func TestOrderListRowsExclusiveMinBoundExcludesEqual(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	for _, value := range []string{"2.5", "3"} {
		order := sampleOrder()
		order.AmountValue = value
		order.Price = "100"
		if _, err := rs.CreateOrder(ctx, order); err != nil {
			t.Fatalf("CreateOrder(%s): %v", value, err)
		}
	}

	bound := "2.5"
	// An exclusive min excludes the row equal to the bound (2.50 == 2.5).
	page, err := rs.ListOrderRows(ctx, fwstore.OrderListFilter{
		Amount: fwstore.DecimalRangeFilter{Min: &bound, MinExclusive: true},
		Sort:   fwstore.SortSpec{Column: "amountValue"},
	})
	if err != nil {
		t.Fatalf("ListOrderRows(exclusive min): %v", err)
	}
	got := []string{}
	for _, row := range page.Rows {
		got = append(got, row.Order.AmountValue)
	}
	if strings.Join(got, ",") != "3" {
		t.Fatalf("exclusive-min amounts = %v, want [3]", got)
	}

	// An inclusive min keeps the equal row.
	page, err = rs.ListOrderRows(ctx, fwstore.OrderListFilter{
		Amount: fwstore.DecimalRangeFilter{Min: &bound},
		Sort:   fwstore.SortSpec{Column: "amountValue"},
	})
	if err != nil {
		t.Fatalf("ListOrderRows(inclusive min): %v", err)
	}
	got = got[:0]
	for _, row := range page.Rows {
		got = append(got, row.Order.AmountValue)
	}
	if strings.Join(got, ",") != "2.5,3" {
		t.Fatalf("inclusive-min amounts = %v, want [2.5 3]", got)
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
	ghost := domain.ExternalID("missing-order-lock")
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

	ghost := domain.ExternalID("missing-order-status")
	if err := rs.UpdateOrderStatus(
		ctx, ghost, domain.OrderStatusCancelled,
	); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("UpdateOrderStatus(missing) = %v, want ErrNotFound", err)
	}
}

// eventAttestation returns the attestation folded onto the order's event with
// the given external id, failing the test if the event is absent.
func eventAttestation(
	t *testing.T, detail domain.OrderDetail, eventID domain.ExternalID,
) *domain.EventAttestation {
	t.Helper()
	for i := range detail.Events {
		if detail.Events[i].ExternalID == eventID {
			return detail.Events[i].Attestation
		}
	}
	t.Fatalf("event %q not found in order detail", eventID.String())
	return nil
}

func TestPutEventAttestationPresentAndAbsent(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	// A signing key the attestation references (ON DELETE RESTRICT FK).
	seedSigningKey(t, ctx, rs, "key-1")

	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	event, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order:  created.ExternalID,
		Type:   domain.OrderEventPreTradeAccepted,
		Source: domain.SourcePanel,
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent: %v", err)
	}

	// Absent before any PutEventAttestation: the event carries no attestation.
	events, err := rs.ListOrderEvents(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("ListOrderEvents(before put): %v", err)
	}
	if len(events) != 1 || events[0].Attestation != nil {
		t.Fatalf("attestation before put: %+v, want absent", events)
	}

	att := domain.EventAttestation{
		Token:       "tok-abc",
		KeyID:       "key-1",
		Alg:         "ed25519",
		RequestType: domain.AttestationRequestSubmit,
		Mode:        "immediate",
		IssuedAt:    "2026-06-26T10:00:00Z",
		ExpiresAt:   "2026-06-26T10:05:00Z",
	}
	if err := rs.PutEventAttestation(ctx, event.ExternalID, att); err != nil {
		t.Fatalf("PutEventAttestation: %v", err)
	}
	detail, err := rs.GetOrder(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder(signed attestation): %v", err)
	}
	got := eventAttestation(t, detail, event.ExternalID)
	if got == nil || *got != att {
		t.Fatalf("GetOrder signed attestation = %+v, want %+v", got, att)
	}

	// Write-once: a second put with a different token does not clobber.
	clobber := att
	clobber.Token = "tok-CLOBBER"
	if err := rs.PutEventAttestation(ctx, event.ExternalID, clobber); err != nil {
		t.Fatalf("PutEventAttestation(retry): %v", err)
	}
	detail, _ = rs.GetOrder(ctx, created.ExternalID)
	got = eventAttestation(t, detail, event.ExternalID)
	if got == nil || got.Token != "tok-abc" {
		t.Fatalf("attestation token after retry = %+v, want the first (write-once)", got)
	}

	// A missing event id is a tolerated no-op, not an error.
	ghost := domain.ExternalID("missing-attestation-event")
	if err := rs.PutEventAttestation(ctx, ghost, att); err != nil {
		t.Fatalf("PutEventAttestation(missing) = %v, want nil (no-op)", err)
	}

	unsignedEvent, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order:  created.ExternalID,
		Type:   domain.OrderEventPreTradeAccepted,
		Source: domain.SourcePanel,
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent(unsigned): %v", err)
	}
	unsigned := domain.EventAttestation{
		Token:       "tok-unsigned",
		Alg:         "none",
		RequestType: domain.AttestationRequestSubmit,
		Mode:        "immediate",
		IssuedAt:    "2026-06-26T10:10:00Z",
		ExpiresAt:   "2026-06-26T10:15:00Z",
	}
	if err := rs.PutEventAttestation(ctx, unsignedEvent.ExternalID, unsigned); err != nil {
		t.Fatalf("PutEventAttestation(unsigned): %v", err)
	}
	detail, err = rs.GetOrder(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder(unsigned attestation): %v", err)
	}
	gotUnsigned := eventAttestation(t, detail, unsignedEvent.ExternalID)
	if gotUnsigned == nil {
		t.Fatal("GetOrder unsigned attestation = nil, want present")
	}
	if *gotUnsigned != unsigned {
		t.Fatalf("GetOrder unsigned attestation = %+v, want %+v", gotUnsigned, unsigned)
	}
	if gotUnsigned.Alg != "none" || gotUnsigned.KeyID != "" {
		t.Fatalf(
			"unsigned attestation alg/key = %q/%q, want none/empty",
			gotUnsigned.Alg, gotUnsigned.KeyID,
		)
	}

	badAlgEvent, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order:  created.ExternalID,
		Type:   domain.OrderEventPreTradeAccepted,
		Source: domain.SourcePanel,
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent(bad alg): %v", err)
	}
	badAlg := unsigned
	badAlg.Alg = "bogus"
	if err := rs.PutEventAttestation(ctx, badAlgEvent.ExternalID, badAlg); err == nil {
		t.Fatal("PutEventAttestation(bad alg) = nil, want CHECK violation")
	}

	badKeyEvent, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order:  created.ExternalID,
		Type:   domain.OrderEventPreTradeAccepted,
		Source: domain.SourcePanel,
	})
	if err != nil {
		t.Fatalf("AppendOrderEvent(bad key): %v", err)
	}
	badKey := unsigned
	badKey.KeyID = "missing-key"
	if err := rs.PutEventAttestation(
		ctx, badKeyEvent.ExternalID, badKey,
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("PutEventAttestation(missing key) = %v, want ErrInvalid", err)
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
	ghost := domain.ExternalID("missing-event-order")
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

func TestTradeListRowsDBCountPageFilterSortDecimalsNumerically(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
		t.Fatalf("CreateAccount(acc-2): %v", err)
	}
	order := sampleOrder()
	created, err := rs.CreateOrder(ctx, order)
	if err != nil {
		t.Fatalf("CreateOrder(acc-1): %v", err)
	}
	order.Account = "acc-2"
	otherOrder, err := rs.CreateOrder(ctx, order)
	if err != nil {
		t.Fatalf("CreateOrder(acc-2): %v", err)
	}

	seedTrade := func(account domain.AccountID, orderID domain.ExternalID,
		source domain.Source, side domain.OrderSide,
		quantity string, price string, lockPrice string,
	) domain.Trade {
		t.Helper()
		trade, err := rs.CreateTrade(ctx, domain.Trade{
			Order:      orderID,
			Account:    account,
			BaseAsset:  "AAPL",
			QuoteAsset: "USD",
			Principal:  "operator",
			Source:     source,
			Side:       side,
			Quantity:   quantity,
			Price:      price,
			LockPrice:  lockPrice,
		})
		if err != nil {
			t.Fatalf("CreateTrade(%s): %v", quantity, err)
		}
		return trade
	}
	seedTrade("acc-1", created.ExternalID, domain.SourcePanel,
		domain.OrderSideBuy, "2", "9", "2")
	trade10 := seedTrade("acc-1", created.ExternalID, domain.SourcePanel,
		domain.OrderSideBuy, "10", "10", "10")
	seedTrade("acc-1", created.ExternalID, domain.SourcePanel,
		domain.OrderSideBuy, "100", "100", "100")
	seedTrade("acc-1", created.ExternalID, domain.SourceAPI,
		domain.OrderSideSell, "3", "3", "3")
	seedTrade("acc-2", otherOrder.ExternalID, domain.SourcePanel,
		domain.OrderSideBuy, "1", "1", "1")

	minQuantity := "2"
	side := domain.OrderSideBuy
	page, err := rs.ListTradeRows(ctx, fwstore.TradeListFilter{
		Account:    fwstore.ExactTextMatcher("acc-1"),
		BaseAsset:  fwstore.ExactTextMatcher("AAPL"),
		QuoteAsset: fwstore.ExactTextMatcher("USD"),
		Side:       &side,
		Source:     domain.SourcePanel,
		Quantity:   fwstore.DecimalRangeFilter{Min: &minQuantity},
		Sort:       fwstore.SortSpec{Column: "quantity"},
		Page:       fwstore.PageSpec{Limit: 2},
	})
	if err != nil {
		t.Fatalf("ListTradeRows(first): %v", err)
	}
	if page.Total != 3 || len(page.Rows) != 2 {
		t.Fatalf("page total/len = %d/%d, want 3/2", page.Total, len(page.Rows))
	}
	got := []string{page.Rows[0].Quantity, page.Rows[1].Quantity}
	if strings.Join(got, ",") != "2,10" {
		t.Fatalf("numeric quantity sort = %v, want [2 10]", got)
	}

	next, err := rs.ListTradeRows(ctx, fwstore.TradeListFilter{
		Account:    fwstore.ExactTextMatcher("acc-1"),
		BaseAsset:  fwstore.ExactTextMatcher("AAPL"),
		QuoteAsset: fwstore.ExactTextMatcher("USD"),
		Side:       &side,
		Source:     domain.SourcePanel,
		Quantity:   fwstore.DecimalRangeFilter{Min: &minQuantity},
		Sort:       fwstore.SortSpec{Column: "quantity"},
		Page:       fwstore.PageSpec{Limit: 2, Offset: 2},
	})
	if err != nil {
		t.Fatalf("ListTradeRows(next): %v", err)
	}
	if next.Total != 3 || len(next.Rows) != 1 || next.Rows[0].Quantity != "100" {
		t.Fatalf("next page = total %d rows %+v", next.Total, next.Rows)
	}

	maxPrice := "11"
	minLockPrice := "2.5"
	filtered, err := rs.ListTradeRows(ctx, fwstore.TradeListFilter{
		Price:     fwstore.DecimalRangeFilter{Max: &maxPrice, MaxExclusive: true},
		LockPrice: fwstore.DecimalRangeFilter{Min: &minLockPrice, MinExclusive: true},
		Sort:      fwstore.SortSpec{Column: "lockPrice"},
		Page:      fwstore.PageSpec{Limit: 10},
	})
	if err != nil {
		t.Fatalf("ListTradeRows(price lock filters): %v", err)
	}
	got = got[:0]
	for _, row := range filtered.Rows {
		got = append(got, row.LockPrice)
	}
	if strings.Join(got, ",") != "3,10" {
		t.Fatalf("price/lock filtered rows = %v, want [3 10]", got)
	}

	exact, err := rs.ListTradeRows(ctx, fwstore.TradeListFilter{
		ExternalID: trade10.ExternalID,
		Page:       fwstore.PageSpec{Limit: 10},
	})
	if err != nil {
		t.Fatalf("ListTradeRows(external id): %v", err)
	}
	if exact.Total != 1 || len(exact.Rows) != 1 ||
		exact.Rows[0].ExternalID != trade10.ExternalID {
		t.Fatalf("external id page = total %d rows %+v",
			exact.Total, exact.Rows)
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
					HeldResult:       "0",
					IncomingResult:   "0",
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

	// Balance updated from deltas; bogus absolute results are ignored.
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

func TestRecordOrderSettlementPersistsEngineAbsoluteBalances(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	seedBalance(t, ctx, rs, "acc-1", "USD", "800", "200", "5", "4")

	order := sampleOrder()
	order.Status = domain.OrderStatusAccepted
	created, err := rs.CreateOrder(ctx, order)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if err := rs.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID: "approval-1",
		Order:      created.ExternalID,
		Account:    "acc-1",
		ParamsJSON: "{}",
		IssuedAt:   time.Now().UTC(),
		ExpiresAt:  time.Now().UTC().Add(time.Minute),
		State:      domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}

	if err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Account:     "acc-1",
		Order:       created.ExternalID,
		OrderStatus: domain.OrderStatusRejected,
		Leaves:      "0",
		Balances: []domain.BalanceSettlement{{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceDelta:     "200",
				HeldDelta:        "-200",
				IncomingDelta:    "10",
				RealizedPnlDelta: "3",
				BalanceResult:    "999999",
			},
		}},
		Events: []domain.OrderEvent{
			{Order: created.ExternalID, Type: domain.OrderEventPreTradeRejected},
		},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement: %v", err)
	}

	intent, ok, err := rs.GetReservationIntent(ctx, "approval-1")
	if err != nil || !ok {
		t.Fatalf("GetReservationIntent: ok=%v err=%v", ok, err)
	}
	if intent.State != domain.ReservationIntentStateHeld {
		t.Fatalf("intent state = %q, want held", intent.State)
	}
	detail, err := rs.GetOrder(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusRejected ||
		detail.Order.Leaves != "0" {
		t.Fatalf("order = %+v, want rejected leaves=0", detail.Order)
	}
	if len(detail.Events) != 1 ||
		detail.Events[0].Type != domain.OrderEventPreTradeRejected {
		t.Fatalf("events = %+v, want rejected", detail.Events)
	}
	bal, ok := getBalanceRow(t, ctx, rs, "acc-1", "USD")
	if !ok {
		t.Fatal("balance row missing after settlement")
	}
	if bal.available != "999999" || bal.held != "200" ||
		bal.incoming != "5" || bal.realized != "7" {
		t.Fatalf("balance = %+v, want optional absolutes merged with previous values", bal)
	}
}

// TestRecordOrderSettlementLeaves verifies CreateOrder persists the caller
// supplied remaining open quantity and RecordOrderSettlement rewrites it only
// when a non-empty Leaves is supplied.
func TestRecordOrderSettlementLeaves(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	order := sampleOrder()
	order.Leaves = "7"
	created, err := rs.CreateOrder(ctx, order)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if created.Leaves != "7" {
		t.Fatalf("created leaves = %q, want request value 7", created.Leaves)
	}
	detail, err := rs.GetOrder(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Leaves != "7" {
		t.Fatalf("stored leaves = %q, want 7", detail.Order.Leaves)
	}

	// An empty Leaves leaves the column unchanged; the status advance still
	// applies.
	if err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Order:       created.ExternalID,
		Account:     "acc-1",
		OrderStatus: domain.OrderStatusAccepted,
		AllowedFrom: []domain.OrderStatus{domain.OrderStatusSubmitted},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement(no leaves): %v", err)
	}
	detail, _ = rs.GetOrder(ctx, created.ExternalID)
	if detail.Order.Leaves != "7" {
		t.Fatalf("leaves after empty settlement = %q, want unchanged 7", detail.Order.Leaves)
	}

	// A non-empty Leaves rewrites the column.
	if err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Order:       created.ExternalID,
		Account:     "acc-1",
		OrderStatus: domain.OrderStatusPartiallyFilled,
		AllowedFrom: []domain.OrderStatus{domain.OrderStatusAccepted},
		Leaves:      "4",
	}); err != nil {
		t.Fatalf("RecordOrderSettlement(leaves): %v", err)
	}
	detail, _ = rs.GetOrder(ctx, created.ExternalID)
	if detail.Order.Leaves != "4" {
		t.Fatalf("leaves after settlement = %q, want 4", detail.Order.Leaves)
	}
}

func TestRecordOrderSettlementAcceptsHighPrecisionBalance(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	const highPrecision = "263.1578947368421052631578947368421053"
	st := domain.OrderSettlement{
		Order:       created.ExternalID,
		Account:     "acc-1",
		OrderStatus: domain.OrderStatusCommitted,
		AllowedFrom: domain.OrderStatusesEligibleForFill(),
		Balances: []domain.BalanceSettlement{
			{
				Asset: "AAPL",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult:    highPrecision,
					RealizedPnlDelta: "0",
				},
			},
		},
	}
	if err := rs.RecordOrderSettlement(ctx, st); err != nil {
		t.Fatalf("RecordOrderSettlement: %v", err)
	}
	bal, ok := getBalanceRow(t, ctx, rs, "acc-1", "AAPL")
	if !ok {
		t.Fatal("balance row missing after high-precision settlement")
	}
	if bal.available != highPrecision {
		t.Fatalf("available = %q, want %q", bal.available, highPrecision)
	}
}

func TestRecordOrderSettlementMissingOrder(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	ghost := domain.ExternalID("missing-settlement-order")
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
			{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "1000", HeldResult: "100"}},
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

func TestRecordOrderSettlementPrunesEmptyBalance(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	seedBalance(t, ctx, rs, "acc-1", "USD", "10", "0", "0", "0")

	st := domain.OrderSettlement{
		Account: "acc-1",
		Balances: []domain.BalanceSettlement{
			{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult:    "0",
					HeldResult:       "0",
					IncomingResult:   "0",
					RealizedPnlDelta: "0",
				},
			},
		},
	}
	if err := rs.RecordOrderSettlement(ctx, st); err != nil {
		t.Fatalf("RecordOrderSettlement: %v", err)
	}
	if _, ok := getBalanceRow(t, ctx, rs, "acc-1", "USD"); ok {
		t.Fatal("balance row still exists, want empty settlement balance pruned")
	}
}

func TestDeleteOrderCascadesChildren(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	seedSigningKey(t, ctx, rs, "key-1")

	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	event, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order: created.ExternalID, Type: domain.OrderEventSubmitted, Source: domain.SourcePanel,
	})
	if err != nil {
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
	if err := rs.PutEventAttestation(ctx, event.ExternalID, domain.EventAttestation{
		Token: "t", KeyID: "key-1", Alg: "ed25519",
		RequestType: domain.AttestationRequestSubmit, Mode: "immediate",
		IssuedAt: "2026-06-26T10:00:00Z", ExpiresAt: "2026-06-26T10:05:00Z",
	}); err != nil {
		t.Fatalf("PutEventAttestation: %v", err)
	}

	rstore := rs.(*realmStore)
	if c := countRows(t, ctx, rstore, "order_event"); c != 1 {
		t.Fatalf("order_events before delete = %d, want 1", c)
	}
	if c := countRows(t, ctx, rstore, "trade"); c != 1 {
		t.Fatalf("trades before delete = %d, want 1", c)
	}
	if c := countRows(t, ctx, rstore, "event_attestation"); c != 1 {
		t.Fatalf("event_attestations before delete = %d, want 1", c)
	}

	// Delete the order by surrogate id (schema-cascade test via raw SQL; the
	// public interface has no DeleteOrder). The schema must cascade children.
	if _, err := rstore.rawDB().ExecContext(
		ctx, `DELETE FROM order_record WHERE external_id = ?`, created.ExternalID.Bytes(),
	); err != nil {
		t.Fatalf("delete order: %v", err)
	}
	if c := countRows(t, ctx, rstore, "order_event"); c != 0 {
		t.Fatalf("order_events after delete = %d, want 0 (cascade)", c)
	}
	if c := countRows(t, ctx, rstore, "trade"); c != 0 {
		t.Fatalf("trades after delete = %d, want 0 (cascade)", c)
	}
	if c := countRows(t, ctx, rstore, "event_attestation"); c != 0 {
		t.Fatalf("event_attestations after delete = %d, want 0 (cascade)", c)
	}
}

func TestDeleteAccountCascadesOrders(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	if _, err := rs.CreateOrder(ctx, sampleOrder()); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	rstore := rs.(*realmStore)
	if c := countRows(t, ctx, rstore, "order_record"); c != 1 {
		t.Fatalf("orders before account delete = %d, want 1", c)
	}

	// Deleting the account cascades to its orders (and thence their children).
	if _, err := rstore.rawDB().ExecContext(
		ctx, `DELETE FROM account WHERE code = ?`, "acc-1",
	); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	if c := countRows(t, ctx, rstore, "order_record"); c != 0 {
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
	ghost := domain.ExternalID("missing-trade-order")
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
	if err := r.rawDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestCreateOrderSuppliedExternalIDUsedVerbatim verifies that a caller-supplied
// external id is written verbatim: the returned id equals the supplied one and
// the row is addressable by it.
func TestCreateOrderSuppliedExternalIDUsedVerbatim(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	supplied := domain.ExternalID("caller-order-1")
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

	supplied := domain.ExternalID("caller-order-1")
	first := sampleOrder()
	first.ExternalID = supplied
	if _, err := rs.CreateOrder(ctx, first); err != nil {
		t.Fatalf("CreateOrder(first): %v", err)
	}

	second := sampleOrder()
	second.ExternalID = supplied
	_, err := rs.CreateOrder(ctx, second)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateOrder(dup id) = %v, want ErrAlreadyExists", err)
	}
	if msg := err.Error(); !strings.Contains(msg, supplied.String()) {
		t.Fatalf("conflict error %q does not name the external id %q",
			msg, supplied.String())
	}

	if n := countRows(t, ctx, r, "order_record"); n != 1 {
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
