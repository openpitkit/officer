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

package sqlite

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
		`SELECT available, held, incoming, COALESCE(realized_pnl, '')
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
		DropCopy:    true,
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
		got.Status != domain.OrderStatusSubmitted || !got.DropCopy {
		t.Fatalf("GetOrder fields = %+v", got)
	}

	// ListOrders and ListAllOrders return the order by account.
	list, err := rs.ListOrders(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if len(list) != 1 || list[0].ExternalID != created.ExternalID ||
		!list[0].DropCopy {
		t.Fatalf("ListOrders = %+v", list)
	}
	all, err := rs.ListAllOrders(ctx, "acc-1", domain.SourcePanel)
	if err != nil {
		t.Fatalf("ListAllOrders: %v", err)
	}
	if len(all) != 1 || all[0].ExternalID != created.ExternalID ||
		!all[0].DropCopy {
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

func TestCreateOrderDropCopyDefaultsFalse(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	order := sampleOrder()
	order.DropCopy = false
	created, err := rs.CreateOrder(ctx, order)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	detail, err := rs.GetOrder(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.DropCopy {
		t.Fatalf("GetOrder DropCopy = true, want false")
	}
	list, err := rs.ListOrders(ctx, "acc-1", "", 10)
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if len(list) != 1 || list[0].DropCopy {
		t.Fatalf("ListOrders = %+v, want false drop-copy flag", list)
	}
}

func TestCreateOrderRejectsUnknownDictionaryCode(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	order := sampleOrder()
	order.Source = domain.Source("unknown")

	_, err := rs.CreateOrder(ctx, order)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateOrder unknown source = %v, want ErrInvalid", err)
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

	// A missing event id is a hard error: an attestation without its event row
	// would otherwise make a committed event look signed when it is not.
	ghost := domain.ExternalID("missing-attestation-event")
	if err := rs.PutEventAttestation(ctx, ghost, att); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("PutEventAttestation(missing) = %v, want ErrNotFound", err)
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
		Commission: &domain.Commission{Amount: "-0.12", Currency: "USD"},
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
	if got.Commission == nil ||
		got.Commission.Amount != "-0.12" ||
		got.Commission.Currency != "USD" {
		t.Fatalf("trade commission = %+v", got.Commission)
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

func TestCreateTradeRejectsInvalidCommissionAmount(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	_, err = rs.CreateTrade(ctx, domain.Trade{
		Order:      created.ExternalID,
		Account:    "acc-1",
		BaseAsset:  "AAPL",
		QuoteAsset: "USD",
		Source:     domain.SourcePanel,
		Side:       domain.OrderSideBuy,
		Quantity:   "5",
		Price:      "151.00",
		Commission: &domain.Commission{Amount: "not-decimal", Currency: "USD"},
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateTrade(invalid commission) = %v, want ErrInvalid", err)
	}
	if c := countRows(t, ctx, rs.(*realmStore), "trade"); c != 0 {
		t.Fatalf("trades after invalid commission = %d, want 0", c)
	}
}

// TestCreateTradeRejectsOneSidedCommission asserts a commission carrying exactly
// one of amount/currency is rejected with ErrInvalid and nothing is persisted; a
// one-sided commission would otherwise be dropped from the subtotals rollup and
// understate the order commission.
func TestCreateTradeRejectsOneSidedCommission(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	cases := []struct {
		name       string
		commission *domain.Commission
	}{
		{"amount-only", &domain.Commission{Amount: "-0.12"}},
		{"currency-only", &domain.Commission{Currency: "USD"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rs.CreateTrade(ctx, domain.Trade{
				Order:      created.ExternalID,
				Account:    "acc-1",
				BaseAsset:  "AAPL",
				QuoteAsset: "USD",
				Source:     domain.SourcePanel,
				Side:       domain.OrderSideBuy,
				Quantity:   "5",
				Price:      "151.00",
				Commission: tc.commission,
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("CreateTrade(one-sided commission) = %v, want ErrInvalid", err)
			}
		})
	}
	if c := countRows(t, ctx, rs.(*realmStore), "trade"); c != 0 {
		t.Fatalf("trades after one-sided commission = %d, want 0", c)
	}
}

// TestCreateTradeAbsentCommissionRoundTrips asserts a commission with both fields
// empty is a valid no-commission trade: it persists and reads back with a nil
// Commission, matching the both-absent branch of the write invariant.
func TestCreateTradeAbsentCommissionRoundTrips(t *testing.T) {
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
		Source:     domain.SourcePanel,
		Side:       domain.OrderSideBuy,
		Quantity:   "5",
		Price:      "151.00",
		Commission: &domain.Commission{},
	})
	if err != nil {
		t.Fatalf("CreateTrade(absent commission): %v", err)
	}

	list, err := rs.ListTrades(ctx, "acc-1", domain.SourcePanel, 10)
	if err != nil {
		t.Fatalf("ListTrades: %v", err)
	}
	if len(list) != 1 || list[0].ExternalID != tr.ExternalID {
		t.Fatalf("ListTrades = %+v", list)
	}
	if list[0].Commission != nil {
		t.Fatalf("trade commission = %+v, want nil for both-empty", list[0].Commission)
	}
}

func TestOrderCommissionSubtotals(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	trades := []domain.Trade{
		{
			Order: created.ExternalID, Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Source: domain.SourcePanel,
			Side: domain.OrderSideBuy, Quantity: "1", Price: "100",
			Commission: &domain.Commission{Amount: "-0.12", Currency: "USD"},
		},
		{
			Order: created.ExternalID, Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Source: domain.SourcePanel,
			Side: domain.OrderSideBuy, Quantity: "1", Price: "101",
			Commission: &domain.Commission{Amount: "-1.00", Currency: "EUR"},
		},
		{
			Order: created.ExternalID, Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Source: domain.SourcePanel,
			Side: domain.OrderSideBuy, Quantity: "1", Price: "102",
			Commission: &domain.Commission{Amount: "-0.03", Currency: "USD"},
		},
	}
	for _, trade := range trades {
		if _, err := rs.CreateTrade(ctx, trade); err != nil {
			t.Fatalf("CreateTrade: %v", err)
		}
	}
	feeOnlyReport := &domain.ExecutionReportRequest{
		Commission:  &domain.Commission{Amount: "-0.25", Currency: "BNB"},
		Order:       created.ExternalID,
		OrderStatus: domain.OrderStatusAccepted,
	}
	if _, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order: created.ExternalID,
		Type:  domain.OrderEventPreTradeAccepted,
		Payload: domain.OrderEventPayload{
			Commission:      feeOnlyReport.Commission,
			ExecutionReport: feeOnlyReport,
		},
	}); err != nil {
		t.Fatalf("AppendOrderEvent(fee only): %v", err)
	}

	// A fill report may persist both a fill event and a terminal lifecycle event.
	// Its commission is already represented by the trade and must not be added
	// from either event.
	fillReport := &domain.ExecutionReportRequest{
		FillQuantity: "1",
		FillPrice:    "100",
		Commission:   &domain.Commission{Amount: "-0.12", Currency: "USD"},
		Order:        created.ExternalID,
		OrderStatus:  domain.OrderStatusCancelled,
	}
	for _, eventType := range []domain.OrderEventType{
		domain.OrderEventFill,
		domain.OrderEventCancelled,
	} {
		if _, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
			Order: created.ExternalID,
			Type:  eventType,
			Payload: domain.OrderEventPayload{
				FillQuantity:    fillReport.FillQuantity,
				FillPrice:       fillReport.FillPrice,
				Commission:      fillReport.Commission,
				ExecutionReport: fillReport,
			},
		}); err != nil {
			t.Fatalf("AppendOrderEvent(%s): %v", eventType, err)
		}
	}

	detail, err := rs.GetOrder(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	want := []domain.Commission{
		{Amount: "-0.25", Currency: "BNB"},
		{Amount: "-1", Currency: "EUR"},
		{Amount: "-0.15", Currency: "USD"},
	}
	if !commissionsEqual(detail.Order.CommissionSubtotals, want) {
		t.Fatalf("detail commission subtotals = %+v, want %+v",
			detail.Order.CommissionSubtotals, want)
	}

	page, err := rs.ListOrderRows(ctx, fwstore.OrderListFilter{
		Page: fwstore.PageSpec{Limit: 10},
	})
	if err != nil {
		t.Fatalf("ListOrderRows: %v", err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("ListOrderRows len = %d, want 1", len(page.Rows))
	}
	if !commissionsEqual(page.Rows[0].Order.CommissionSubtotals, want) {
		t.Fatalf("list commission subtotals = %+v, want %+v",
			page.Rows[0].Order.CommissionSubtotals, want)
	}
}

func TestOrderCommissionSubtotalsChunksOrderIDs(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	orders := make([]domain.ExternalID, 0, commissionSubtotalOrderBatchSize+1)
	for i := 0; i < commissionSubtotalOrderBatchSize+1; i++ {
		created, err := rs.CreateOrder(ctx, sampleOrder())
		if err != nil {
			t.Fatalf("CreateOrder(%d): %v", i, err)
		}
		orders = append(orders, created.ExternalID)
	}
	for _, order := range []domain.ExternalID{orders[0], orders[len(orders)-1]} {
		if _, err := rs.CreateTrade(ctx, domain.Trade{
			Order:      order,
			Account:    "acc-1",
			BaseAsset:  "AAPL",
			QuoteAsset: "USD",
			Source:     domain.SourcePanel,
			Side:       domain.OrderSideBuy,
			Quantity:   "1",
			Price:      "100",
			Commission: &domain.Commission{Amount: "-0.10", Currency: "USD"},
		}); err != nil {
			t.Fatalf("CreateTrade(%s): %v", order, err)
		}
	}
	lastOrder := orders[len(orders)-1]
	feeOnlyReport := &domain.ExecutionReportRequest{
		Commission:  &domain.Commission{Amount: "-0.20", Currency: "BNB"},
		Order:       lastOrder,
		OrderStatus: domain.OrderStatusAccepted,
	}
	if _, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
		Order: lastOrder,
		Type:  domain.OrderEventPreTradeAccepted,
		Payload: domain.OrderEventPayload{
			Commission:      feeOnlyReport.Commission,
			ExecutionReport: feeOnlyReport,
		},
	}); err != nil {
		t.Fatalf("AppendOrderEvent(last fee only): %v", err)
	}

	r := rs.(*realmStore)
	dictionaries, err := r.dictionaries()
	if err != nil {
		t.Fatalf("dictionaries: %v", err)
	}
	got, err := commissionSubtotalsByOrder(ctx, dictionaries, r.rawDB(), orders)
	if err != nil {
		t.Fatalf("commissionSubtotalsByOrder: %v", err)
	}
	want := []domain.Commission{{Amount: "-0.1", Currency: "USD"}}
	if !commissionsEqual(got[orders[0]], want) {
		t.Fatalf("first order commissions = %+v, want %+v", got[orders[0]], want)
	}
	wantLast := []domain.Commission{
		{Amount: "-0.2", Currency: "BNB"},
		{Amount: "-0.1", Currency: "USD"},
	}
	if !commissionsEqual(got[orders[len(orders)-1]], wantLast) {
		t.Fatalf("last order commissions = %+v, want %+v",
			got[orders[len(orders)-1]], wantLast)
	}
	if got[orders[1]] == nil || len(got[orders[1]]) != 0 {
		t.Fatalf("empty order commissions = %+v, want empty slice", got[orders[1]])
	}
}

// TestOrderCommissionSubtotalsFeeOnlyReportAgreesAcrossReads pins the detail
// read and the list read to the shared rollup implementation across trade-row,
// fee-only, mixed-currency, and canonical-event cases.
func TestOrderCommissionSubtotalsFeeOnlyReportAgreesAcrossReads(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	mixed, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder(mixed): %v", err)
	}
	feeOnly, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder(fee only): %v", err)
	}
	mixedID := mixed.ExternalID
	feeOnlyID := feeOnly.ExternalID

	addTrade := func(order domain.ExternalID, amount, currency string) {
		t.Helper()
		if _, err := rs.CreateTrade(ctx, domain.Trade{
			Order: order, Account: "acc-1", BaseAsset: "AAPL",
			QuoteAsset: "USD", Source: domain.SourcePanel,
			Side: domain.OrderSideBuy, Quantity: "1", Price: "100",
			Commission: &domain.Commission{Amount: amount, Currency: currency},
		}); err != nil {
			t.Fatalf("CreateTrade(%s %s): %v", amount, currency, err)
		}
	}
	addReportEvent := func(
		order domain.ExternalID,
		eventType domain.OrderEventType,
		report *domain.ExecutionReportRequest,
	) {
		t.Helper()
		if _, err := rs.AppendOrderEvent(ctx, domain.OrderEvent{
			Order: order,
			Type:  eventType,
			Payload: domain.OrderEventPayload{
				Commission:      report.Commission,
				OrderStatus:     string(report.OrderStatus),
				ExecutionReport: report,
			},
		}); err != nil {
			t.Fatalf("AppendOrderEvent(%s): %v", eventType, err)
		}
	}

	addTrade(mixedID, "-0.10", "USD")
	addTrade(mixedID, "-0.05", "USD")
	// A fee-only report in the currency of the trade fees must land in that same
	// currency bucket rather than open a second one.
	addReportEvent(mixedID, domain.OrderEventPreTradeAccepted,
		&domain.ExecutionReportRequest{
			Commission:  &domain.Commission{Amount: "-0.02", Currency: "USD"},
			Order:       mixedID,
			OrderStatus: domain.OrderStatusAccepted,
		})
	cancelFee := &domain.ExecutionReportRequest{
		Commission:  &domain.Commission{Amount: "-0.25", Currency: "BNB"},
		Order:       mixedID,
		OrderStatus: domain.OrderStatusCancelled,
	}
	addReportEvent(mixedID, domain.OrderEventCancelled, cancelFee)
	// The same report carried by a non-canonical event must not add its fee twice.
	addReportEvent(mixedID, domain.OrderEventSubmitted, cancelFee)

	addReportEvent(feeOnlyID, domain.OrderEventPreTradeAccepted,
		&domain.ExecutionReportRequest{
			Commission:  &domain.Commission{Amount: "-0.30", Currency: "USD"},
			Order:       feeOnlyID,
			OrderStatus: domain.OrderStatusAccepted,
		})
	addReportEvent(feeOnlyID, domain.OrderEventFill,
		&domain.ExecutionReportRequest{
			Commission:     &domain.Commission{Amount: "-0.20", Currency: "BNB"},
			Order:          feeOnlyID,
			OrderStatus:    domain.OrderStatusPartiallyFilled,
			LeavesQuantity: "1",
		})

	page, err := rs.ListOrderRows(ctx, fwstore.OrderListFilter{
		Page: fwstore.PageSpec{Limit: 10},
	})
	if err != nil {
		t.Fatalf("ListOrderRows: %v", err)
	}
	listed := make(map[domain.ExternalID][]domain.Commission, len(page.Rows))
	for _, row := range page.Rows {
		listed[row.Order.ExternalID] = row.Order.CommissionSubtotals
	}

	for _, testCase := range []struct {
		name  string
		order domain.ExternalID
		want  []domain.Commission
	}{
		{
			name:  "trade fees and fee-only report",
			order: mixedID,
			want: []domain.Commission{
				{Amount: "-0.25", Currency: "BNB"},
				{Amount: "-0.17", Currency: "USD"},
			},
		},
		{
			name:  "fee-only report alone",
			order: feeOnlyID,
			want: []domain.Commission{
				{Amount: "-0.2", Currency: "BNB"},
				{Amount: "-0.3", Currency: "USD"},
			},
		},
	} {
		detail, err := rs.GetOrder(ctx, testCase.order)
		if err != nil {
			t.Fatalf("GetOrder(%s): %v", testCase.name, err)
		}
		if !commissionsEqual(detail.Order.CommissionSubtotals, testCase.want) {
			t.Fatalf("%s: detail subtotals = %+v, want %+v",
				testCase.name, detail.Order.CommissionSubtotals, testCase.want)
		}
		if !commissionsEqual(listed[testCase.order], testCase.want) {
			t.Fatalf("%s: list subtotals = %+v, want %+v",
				testCase.name, listed[testCase.order], testCase.want)
		}
		if !commissionsEqual(
			listed[testCase.order], detail.Order.CommissionSubtotals,
		) {
			t.Fatalf("%s: list subtotals %+v disagree with detail %+v",
				testCase.name, listed[testCase.order],
				detail.Order.CommissionSubtotals)
		}
	}
}

func commissionsEqual(got, want []domain.Commission) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
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

	before := time.Now().UTC().Add(-time.Second)
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

func TestCountActiveOrders(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	activeStatuses := domain.OrderStatusesEligibleForFill()
	statuses := append(activeStatuses,
		domain.OrderStatusFilled,
		domain.OrderStatusRejected,
		domain.OrderStatusCancelled,
		domain.OrderStatusRolledBack,
	)
	for _, status := range statuses {
		order := sampleOrder()
		order.Status = status
		if _, err := rs.CreateOrder(ctx, order); err != nil {
			t.Fatalf("CreateOrder(%s): %v", status, err)
		}
	}

	n, err := rs.CountActiveOrders(ctx)
	if err != nil || n != len(activeStatuses) {
		t.Fatalf(
			"CountActiveOrders = %d, err=%v, want %d",
			n,
			err,
			len(activeStatuses),
		)
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
		AccountPnl:  "7.25",
		AllowedFrom: domain.OrderStatusesEligibleForFill(),
		Balances: []domain.BalanceSettlement{
			{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult:     "850",
					HeldResult:        "0",
					IncomingResult:    "0",
					RealizedPnlDelta:  "12.50",
					RealizedPnlResult: "12.5",
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
	if _, err := rs.RecordOrderSettlement(ctx, st); err != nil {
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

	// Balance updated from the engine absolute results.
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
	account, ok, err := rs.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "7.25" {
		t.Fatalf("account pnl = %q, want 7.25", account.Pnl)
	}

	// A second settlement from a now-terminal status is rejected by the guard
	// with ErrConflict and nothing written.
	conflict := st
	conflict.OrderStatus = domain.OrderStatusCancelled
	conflict.AccountPnl = "999"
	if _, err := rs.RecordOrderSettlement(ctx, conflict); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("RecordOrderSettlement(terminal) = %v, want ErrConflict", err)
	}
	detail, _ = rs.GetOrder(ctx, created.ExternalID)
	if detail.Order.Status != domain.OrderStatusFilled {
		t.Fatalf("status after rejected settlement = %q, want still filled", detail.Order.Status)
	}
	account, ok, err = rs.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount after rejected settlement: ok=%v err=%v", ok, err)
	}
	if account.Pnl != "7.25" {
		t.Fatalf("account pnl after rejected settlement = %q, want 7.25", account.Pnl)
	}
}

func TestRecordOrderSettlementPersistsExecutionReportIdentityAndEventLinks(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	supplied := domain.ExternalID("client-report-1")
	request := &domain.ExecutionReportRequest{}
	settlement := domain.OrderSettlement{
		ReportID:    &supplied,
		Order:       created.ExternalID,
		Account:     created.Account,
		OrderStatus: domain.OrderStatusPartiallyFilled,
		Events: []domain.OrderEvent{
			{
				Order: created.ExternalID, Type: domain.OrderEventFill,
				Source:  domain.SourceAPI,
				Payload: domain.OrderEventPayload{ExecutionReport: request},
			},
			{
				Order: created.ExternalID, Type: domain.OrderEventCommitted,
				Source:  domain.SourceAPI,
				Payload: domain.OrderEventPayload{ExecutionReport: request},
			},
		},
	}
	recorded, err := rs.RecordOrderSettlement(ctx, settlement)
	if err != nil {
		t.Fatalf("RecordOrderSettlement: %v", err)
	}
	if recorded != supplied {
		t.Fatalf("recorded id = %q, want supplied %q", recorded, supplied)
	}

	r := rs.(*realmStore)
	var reportCount, linkCount int
	if err := r.rawDB().QueryRowContext(
		ctx, `SELECT COUNT(*) FROM execution_report WHERE external_id = ?`, supplied.Bytes(),
	).Scan(&reportCount); err != nil {
		t.Fatalf("count execution report: %v", err)
	}
	if err := r.rawDB().QueryRowContext(
		ctx,
		`SELECT COUNT(*)
		 FROM execution_report_event ree
		 JOIN execution_report er ON er.id = ree.report_id
		 WHERE er.external_id = ?`,
		supplied.Bytes(),
	).Scan(&linkCount); err != nil {
		t.Fatalf("count execution report event links: %v", err)
	}
	if reportCount != 1 || linkCount != 2 {
		t.Fatalf("report rows = %d, links = %d, want 1 and 2", reportCount, linkCount)
	}

	detail, err := rs.GetOrder(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(detail.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(detail.Events))
	}
	for _, event := range detail.Events {
		if event.ExternalID == supplied {
			t.Fatalf("event reused report id %q", supplied)
		}
		if event.Payload.ExecutionReport == nil ||
			event.Payload.ExecutionReport.ExternalID != supplied {
			t.Fatalf("event report snapshot = %+v, want id %q", event.Payload.ExecutionReport, supplied)
		}
	}
}

func TestRecordOrderSubmissionPersistsExecutionReportIdentityAndEventLink(
	t *testing.T,
) {
	ctx, rs := seedOrderFixtures(t)
	reportID := domain.ExternalID("")
	request := &domain.ExecutionReportRequest{
		FillQuantity: "1", FillPrice: "100", LeavesQuantity: "0",
		OrderStatus: domain.OrderStatusFilled,
	}
	created, err := rs.(*realmStore).RecordOrderSubmission(
		ctx,
		sampleOrder(),
		domain.OrderEvent{Type: domain.OrderEventSubmitted},
		func(order domain.Order) (domain.OrderSettlement, error) {
			request.Order = order.ExternalID
			return domain.OrderSettlement{
				ReportID:    &reportID,
				Account:     order.Account,
				Order:       order.ExternalID,
				OrderStatus: domain.OrderStatusFilled,
				Leaves:      "0",
				Events: []domain.OrderEvent{{
					Order: order.ExternalID,
					Type:  domain.OrderEventFill,
					Payload: domain.OrderEventPayload{
						FillQuantity:    "1",
						FillPrice:       "100",
						LeavesQuantity:  "0",
						OrderStatus:     string(domain.OrderStatusFilled),
						ExecutionReport: request,
					},
				}},
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("RecordOrderSubmission: %v", err)
	}
	if reportID.IsZero() {
		t.Fatal("nested settlement did not return generated report id")
	}
	detail, err := rs.GetOrder(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	var fill *domain.OrderEvent
	for i := range detail.Events {
		if detail.Events[i].Type == domain.OrderEventFill {
			fill = &detail.Events[i]
			break
		}
	}
	if fill == nil || fill.Payload.ExecutionReport == nil ||
		fill.Payload.ExecutionReport.ExternalID != reportID {
		t.Fatalf("fill event = %+v, want linked report %q", fill, reportID)
	}
	if got := countRows(t, ctx, rs.(*realmStore), "execution_report"); got != 1 {
		t.Fatalf("execution reports = %d, want 1", got)
	}
	if got := countRows(t, ctx, rs.(*realmStore), "execution_report_event"); got != 1 {
		t.Fatalf("execution report links = %d, want 1", got)
	}
}

func TestRecordOrderSettlementGeneratesExecutionReportIDAtStoreBoundary(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	generated := domain.ExternalID("")
	recorded, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		ReportID:    &generated,
		Order:       created.ExternalID,
		Account:     created.Account,
		OrderStatus: domain.OrderStatusAccepted,
		Events: []domain.OrderEvent{{
			Order: created.ExternalID, Type: domain.OrderEventPreTradeAccepted,
			Source: domain.SourceAPI,
		}},
	})
	if err != nil {
		t.Fatalf("RecordOrderSettlement: %v", err)
	}
	if recorded.IsZero() || generated != recorded {
		t.Fatalf("recorded id = %q, settlement id = %q", recorded, generated)
	}
	if got := countRows(t, ctx, rs.(*realmStore), "execution_report"); got != 1 {
		t.Fatalf("execution reports = %d, want 1", got)
	}
}

func TestRecordOrderSettlementRejectsReportWithoutEventsAndRollsBack(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	r := rs.(*realmStore)
	wantReports := countRows(t, ctx, r, "execution_report")
	wantTrades := countRows(t, ctx, r, "trade")
	wantAccount, ok, err := rs.GetAccount(ctx, created.Account)
	if err != nil || !ok {
		t.Fatalf("GetAccount before settlement: ok=%v err=%v", ok, err)
	}

	reportID := domain.ExternalID("report-without-events")
	_, err = rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		ReportID:    &reportID,
		Order:       created.ExternalID,
		Account:     created.Account,
		AccountPnl:  "999",
		OrderStatus: domain.OrderStatusFilled,
		Balances: []domain.BalanceSettlement{{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceResult: "1", HeldResult: "0", IncomingResult: "0",
			},
		}},
		Trade: &domain.Trade{
			Order: created.ExternalID, Account: created.Account,
			BaseAsset: created.BaseAsset, QuoteAsset: created.QuoteAsset,
			Source: domain.SourceAPI, Side: created.Side, Quantity: "1", Price: "1",
		},
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RecordOrderSettlement = %v, want ErrInvalid", err)
	}
	if got := countRows(t, ctx, r, "execution_report"); got != wantReports {
		t.Fatalf("execution reports = %d, want unchanged %d", got, wantReports)
	}
	if got := countRows(t, ctx, r, "trade"); got != wantTrades {
		t.Fatalf("trades = %d, want unchanged %d", got, wantTrades)
	}
	if _, ok := getBalanceRow(t, ctx, rs, created.Account, "USD"); ok {
		t.Fatal("report without events wrote a balance")
	}
	detail, err := rs.GetOrder(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusSubmitted {
		t.Fatalf("status = %q, want submitted", detail.Order.Status)
	}
	account, ok, err := rs.GetAccount(ctx, created.Account)
	if err != nil || !ok {
		t.Fatalf("GetAccount after settlement: ok=%v err=%v", ok, err)
	}
	if account.Pnl != wantAccount.Pnl {
		t.Fatalf("account pnl = %q, want unchanged %q", account.Pnl, wantAccount.Pnl)
	}
}

func TestRecordOrderSettlementDuplicateReportRollsBackEverything(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	reportID := domain.ExternalID("duplicate-report-1")
	if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		ReportID:    &reportID,
		Order:       created.ExternalID,
		Account:     created.Account,
		OrderStatus: domain.OrderStatusAccepted,
		Events: []domain.OrderEvent{{
			Order: created.ExternalID, Type: domain.OrderEventPreTradeAccepted,
			Source: domain.SourceAPI,
		}},
	}); err != nil {
		t.Fatalf("first RecordOrderSettlement: %v", err)
	}
	duplicateOrder, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder for duplicate: %v", err)
	}

	r := rs.(*realmStore)
	wantReports := countRows(t, ctx, r, "execution_report")
	wantLinks := countRows(t, ctx, r, "execution_report_event")
	wantEvents := countRows(t, ctx, r, "order_event")
	wantTrades := countRows(t, ctx, r, "trade")
	wantAccount, ok, err := rs.GetAccount(ctx, created.Account)
	if err != nil || !ok {
		t.Fatalf("GetAccount before duplicate: ok=%v err=%v", ok, err)
	}
	_, err = rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		ReportID:    &reportID,
		Order:       duplicateOrder.ExternalID,
		Account:     duplicateOrder.Account,
		AccountPnl:  "999",
		OrderStatus: domain.OrderStatusFilled,
		Balances: []domain.BalanceSettlement{{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceResult: "1", HeldResult: "0", IncomingResult: "0",
			},
		}},
		Trade: &domain.Trade{
			Order: duplicateOrder.ExternalID, Account: duplicateOrder.Account,
			BaseAsset: duplicateOrder.BaseAsset, QuoteAsset: duplicateOrder.QuoteAsset,
			Source: domain.SourceAPI, Side: duplicateOrder.Side, Quantity: "1", Price: "1",
		},
		Events: []domain.OrderEvent{{
			Order: duplicateOrder.ExternalID, Type: domain.OrderEventFill, Source: domain.SourceAPI,
		}},
	})
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate RecordOrderSettlement = %v, want ErrAlreadyExists", err)
	}
	if got := countRows(t, ctx, r, "execution_report"); got != wantReports {
		t.Fatalf("execution reports = %d, want unchanged %d", got, wantReports)
	}
	if got := countRows(t, ctx, r, "execution_report_event"); got != wantLinks {
		t.Fatalf("execution report links = %d, want unchanged %d", got, wantLinks)
	}
	if got := countRows(t, ctx, r, "order_event"); got != wantEvents {
		t.Fatalf("events = %d, want unchanged %d", got, wantEvents)
	}
	if got := countRows(t, ctx, r, "trade"); got != wantTrades {
		t.Fatalf("trades = %d, want unchanged %d", got, wantTrades)
	}
	if _, ok := getBalanceRow(t, ctx, rs, created.Account, "USD"); ok {
		t.Fatal("duplicate report wrote a balance")
	}
	detail, err := rs.GetOrder(ctx, duplicateOrder.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusSubmitted {
		t.Fatalf("status = %q, want submitted", detail.Order.Status)
	}
	account, ok, err := rs.GetAccount(ctx, created.Account)
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if account.Pnl != wantAccount.Pnl {
		t.Fatalf("account pnl = %q, want unchanged %q", account.Pnl, wantAccount.Pnl)
	}
}

func TestRecordOrderSettlementSpotFillPersistsEngineRealizedPnlAsset(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	seedBalance(t, ctx, rs, "acc-1", "AAPL", "2", "0", "0", "0")
	seedBalance(t, ctx, rs, "acc-1", "USD", "800", "0", "0", "0")

	order := sampleOrder()
	order.Side = domain.OrderSideSell
	order.Status = domain.OrderStatusAccepted
	created, err := rs.CreateOrder(ctx, order)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Order:       created.ExternalID,
		Account:     "acc-1",
		OrderStatus: domain.OrderStatusFilled,
		AllowedFrom: domain.OrderStatusesEligibleForFill(),
		Balances: []domain.BalanceSettlement{
			{
				Asset: "AAPL",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult:     "1",
					RealizedPnlDelta:  "-10",
					RealizedPnlResult: "-10",
				},
			},
			{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult: "890",
				},
			},
		},
		Trade: &domain.Trade{
			Order: created.ExternalID, Account: "acc-1",
			BaseAsset: "AAPL", QuoteAsset: "USD",
			Source: domain.SourcePanel, Side: domain.OrderSideSell,
			Quantity: "1", Price: "90", LockPrice: "100",
		},
		Events: []domain.OrderEvent{{
			Order: created.ExternalID,
			Type:  domain.OrderEventFill,
			Payload: domain.OrderEventPayload{
				FillQuantity: "1",
				FillPrice:    "90",
			},
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement: %v", err)
	}

	base, ok := getBalanceRow(t, ctx, rs, "acc-1", "AAPL")
	if !ok {
		t.Fatal("base balance row missing after settlement")
	}
	if base.realized != "-10" {
		t.Fatalf("base realized_pnl = %q, want engine absolute -10", base.realized)
	}
	quote, ok := getBalanceRow(t, ctx, rs, "acc-1", "USD")
	if !ok {
		t.Fatal("quote balance row missing after settlement")
	}
	if quote.realized != "0" {
		t.Fatalf("quote realized_pnl = %q, want unchanged 0", quote.realized)
	}
}

func TestRecordOrderSettlementRealizedPnlZeroAndAbsent(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	seedBalance(t, ctx, rs, "acc-1", "AAPL", "1", "0", "0", "0")

	settle := func(realizedPnlResult string) {
		t.Helper()
		order := sampleOrder()
		order.Side = domain.OrderSideSell
		order.Status = domain.OrderStatusAccepted
		created, err := rs.CreateOrder(ctx, order)
		if err != nil {
			t.Fatalf("CreateOrder: %v", err)
		}
		if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
			Order:       created.ExternalID,
			Account:     "acc-1",
			OrderStatus: domain.OrderStatusCommitted,
			Balances: []domain.BalanceSettlement{{
				Asset: "AAPL",
				Outcome: domain.AdjustmentOutcomeAccepted{
					RealizedPnlResult: realizedPnlResult,
				},
			}},
		}); err != nil {
			t.Fatalf("RecordOrderSettlement: %v", err)
		}
	}

	assertRealizedPnl := func(want string) {
		t.Helper()
		balance, ok := getBalanceRow(t, ctx, rs, "acc-1", "AAPL")
		if !ok {
			t.Fatal("balance row missing after settlement")
		}
		if balance.realized != want {
			t.Fatalf("realized_pnl = %q, want %q", balance.realized, want)
		}
	}

	settle("0")
	assertRealizedPnl("0")
	settle("")
	assertRealizedPnl("0")
}

func TestRecordOrderSettlementRacesRealizedPnlAdjustmentNoDeadlock(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	seedBalance(t, ctx, rs, "acc-1", "USD", "800", "0", "0", "1")
	order := sampleOrder()
	order.Status = domain.OrderStatusAccepted
	created, err := rs.CreateOrder(ctx, order)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	start := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		<-start
		_, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
			Order:       created.ExternalID,
			Account:     "acc-1",
			OrderStatus: domain.OrderStatusFilled,
			AllowedFrom: domain.OrderStatusesEligibleForFill(),
			Balances: []domain.BalanceSettlement{{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult:     "802",
					RealizedPnlDelta:  "2",
					RealizedPnlResult: "12",
				},
			}},
			Events: []domain.OrderEvent{{
				Order: created.ExternalID,
				Type:  domain.OrderEventFill,
				Payload: domain.OrderEventPayload{
					FillQuantity: "1",
					FillPrice:    "2",
				},
			}},
		})
		done <- err
	}()
	go func() {
		<-start
		_, err := rs.RecordAccountAdjustment(ctx, fwstore.AccountAdjustmentPersistence{
			UpsertBalance: &domain.Balance{
				Account: "acc-1", Asset: "USD", Available: "800", RealizedPnl: "10",
			},
			Adjustment: domain.AccountAdjustmentRecord{
				Account: "acc-1",
				Asset:   "USD",
				Source:  domain.SourcePanel,
				Request: domain.AdjustmentRequest{
					Asset:       "USD",
					RealizedPnl: "10",
				},
				Accepted: &domain.AdjustmentOutcomeAccepted{
					RealizedPnlResult: "10",
				},
			},
			Audit: AuditEntry{
				Action:  domain.AuditActionAdjustment,
				Account: "acc-1",
				Asset:   "USD",
				Source:  domain.SourcePanel,
				Detail:  "set balance realized_pnl account acc-1 asset=USD realized_pnl=10",
			},
		})
		done <- err
	}()
	close(start)

	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("concurrent write %d: %v", i, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("settlement racing realized-pnl adjustment deadlocked on one SQLite connection")
		}
	}

	quote, ok := getBalanceRow(t, ctx, rs, "acc-1", "USD")
	if !ok {
		t.Fatal("quote balance row missing after concurrent writes")
	}
	switch quote.realized {
	case "10", "12":
	default:
		t.Fatalf("realized_pnl = %q, want serial outcome 10 or 12", quote.realized)
	}
	if quote.realized == "3" {
		t.Fatal("realized-pnl adjustment was lost")
	}
}

func TestRecordOrderSubmissionAttestationFailureRollsBack(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	seedSigningKey(t, ctx, rs, "key-1")

	order := sampleOrder()
	order.ExternalID = domain.ExternalID("rollback-order")
	errSign := errors.New("signing unavailable")
	attest := func(
		_ context.Context, event domain.OrderEvent,
	) (domain.EventAttestation, bool, error) {
		if event.Type == domain.OrderEventPreTradeAccepted {
			return domain.EventAttestation{}, false, errSign
		}
		return domain.EventAttestation{
			Token:       "tok-" + string(event.Type),
			KeyID:       "key-1",
			Alg:         "ed25519",
			RequestType: domain.AttestationRequestSubmit,
			Mode:        "immediate",
			IssuedAt:    "2026-06-26T10:00:00Z",
		}, true, nil
	}

	_, err := rs.(*realmStore).RecordOrderSubmissionWithAttestation(
		ctx,
		order,
		domain.OrderEvent{Type: domain.OrderEventSubmitted},
		func(persisted domain.Order) (domain.OrderSettlement, error) {
			return domain.OrderSettlement{
				Account:     persisted.Account,
				Order:       persisted.ExternalID,
				OrderStatus: domain.OrderStatusCommitted,
				Events: []domain.OrderEvent{{
					Order: persisted.ExternalID,
					Type:  domain.OrderEventPreTradeAccepted,
				}},
			}, nil
		},
		attest,
	)
	if !errors.Is(err, errSign) {
		t.Fatalf("RecordOrderSubmissionWithAttestation = %v, want signing error", err)
	}
	if _, err := rs.GetOrder(ctx, order.ExternalID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetOrder after rollback = %v, want ErrNotFound", err)
	}
	r := rs.(*realmStore)
	if got := countRows(t, ctx, r, "order_record"); got != 0 {
		t.Fatalf("order_record rows after rollback = %d, want 0", got)
	}
	if got := countRows(t, ctx, r, "order_event"); got != 0 {
		t.Fatalf("order_event rows after rollback = %d, want 0", got)
	}
	if got := countRows(t, ctx, r, "event_attestation"); got != 0 {
		t.Fatalf("event_attestation rows after rollback = %d, want 0", got)
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

	if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Account:     "acc-1",
		Order:       created.ExternalID,
		OrderStatus: domain.OrderStatusRejected,
		Leaves:      "0",
		Balances: []domain.BalanceSettlement{{
			Asset: "USD",
			Outcome: domain.AdjustmentOutcomeAccepted{
				BalanceDelta:      "200",
				HeldDelta:         "-200",
				IncomingDelta:     "10",
				RealizedPnlDelta:  "3",
				BalanceResult:     "999999",
				RealizedPnlResult: "123",
			},
		}},
		Events: []domain.OrderEvent{
			{Order: created.ExternalID, Type: domain.OrderEventPreTradeRejected},
		},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement: %v", err)
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
		bal.incoming != "5" || bal.realized != "123" {
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
	order.ReservedQuantity = "8"
	created, err := rs.CreateOrder(ctx, order)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if created.Leaves != "7" || created.ReservedQuantity != "8" {
		t.Fatalf("created quantities = %+v, want leaves 7 and reserve 8", created)
	}
	detail, err := rs.GetOrder(ctx, created.ExternalID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Leaves != "7" || detail.Order.ReservedQuantity != "8" {
		t.Fatalf("stored quantities = %+v, want leaves 7 and reserve 8", detail.Order)
	}

	// An empty Leaves leaves the column unchanged; the status advance still
	// applies.
	if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Order:       created.ExternalID,
		Account:     "acc-1",
		OrderStatus: domain.OrderStatusAccepted,
		AllowedFrom: []domain.OrderStatus{domain.OrderStatusSubmitted},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement(no leaves): %v", err)
	}
	detail, _ = rs.GetOrder(ctx, created.ExternalID)
	if detail.Order.Leaves != "7" || detail.Order.ReservedQuantity != "8" {
		t.Fatalf("quantities after empty settlement = %+v, want unchanged", detail.Order)
	}

	// A non-empty Leaves rewrites the column.
	if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Order:       created.ExternalID,
		Account:     "acc-1",
		OrderStatus: domain.OrderStatusPartiallyFilled,
		AllowedFrom: []domain.OrderStatus{domain.OrderStatusAccepted},
		Leaves:      "4",
	}); err != nil {
		t.Fatalf("RecordOrderSettlement(leaves): %v", err)
	}
	detail, _ = rs.GetOrder(ctx, created.ExternalID)
	if detail.Order.Leaves != "4" || detail.Order.ReservedQuantity != "8" {
		t.Fatalf("partial quantities = %+v, want leaves 4 and reserve 8", detail.Order)
	}

	if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Order:            created.ExternalID,
		Account:          "acc-1",
		OrderStatus:      domain.OrderStatusFilled,
		AllowedFrom:      []domain.OrderStatus{domain.OrderStatusSubmitted},
		Leaves:           "99",
		ReservedQuantity: "0",
		Trade: &domain.Trade{
			Order: created.ExternalID, Account: "acc-1",
			BaseAsset: order.BaseAsset, QuoteAsset: order.QuoteAsset,
			Side: order.Side, Quantity: "1", Price: "10",
		},
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("RecordOrderSettlement(conflict) = %v, want ErrConflict", err)
	}
	detail, _ = rs.GetOrder(ctx, created.ExternalID)
	if detail.Order.Status != domain.OrderStatusPartiallyFilled ||
		detail.Order.Leaves != "4" || detail.Order.ReservedQuantity != "8" ||
		len(detail.Trades) != 0 {
		t.Fatalf("conflicting settlement did not roll back atomically: %+v", detail)
	}

	if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Order:            created.ExternalID,
		Account:          "acc-1",
		OrderStatus:      domain.OrderStatusCancelled,
		AllowedFrom:      []domain.OrderStatus{domain.OrderStatusPartiallyFilled},
		Leaves:           "1.00",
		ReservedQuantity: "0",
	}); err != nil {
		t.Fatalf("RecordOrderSettlement(terminal): %v", err)
	}
	detail, _ = rs.GetOrder(ctx, created.ExternalID)
	if detail.Order.Leaves != "1.00" || detail.Order.ReservedQuantity != "0" {
		t.Fatalf("terminal quantities = %+v, want raw leaves 1.00 and zero reserve", detail.Order)
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
	if _, err := rs.RecordOrderSettlement(ctx, st); err != nil {
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

// TestRecordOrderSettlementNormalizesMirroredBlockReason pins the engine-mirror
// write seam. The engine-boundary scrub only strips control characters, so a
// format-class rune such as U+200B reaches this write; the restore path rejects
// it. Persisting it verbatim would leave the realm - including Officer's own
// rollback archive - unrestorable, so the mirror normalizes instead of rejecting.
func TestRecordOrderSettlementNormalizesMirroredBlockReason(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	created, err := rs.CreateOrder(ctx, sampleOrder())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	reason := "kill\u200bswitch [code=pnl_bound_breached, order " +
		created.ExternalID.String() + "]"
	if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
		Order:       created.ExternalID,
		Account:     "acc-1",
		OrderStatus: domain.OrderStatusCommitted,
		AllowedFrom: domain.OrderStatusesEligibleForFill(),
		Blocks: []domain.ExecutionAccountBlock{{
			Account: "acc-1",
			Code:    "pnl_bound_breached",
			Reason:  reason,
		}},
	}); err != nil {
		t.Fatalf("RecordOrderSettlement: %v", err)
	}

	account, ok, err := rs.GetAccount(ctx, "acc-1")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if !ok {
		t.Fatal("account missing after settlement")
	}
	if !account.Blocked {
		t.Fatal("mirrored engine block did not block the account")
	}
	if strings.Contains(account.BlockReason, "\u200b") {
		t.Fatalf("block reason = %q, want the non-printable rune dropped", account.BlockReason)
	}
	if !strings.HasPrefix(account.BlockReason, "killswitch [code=pnl_bound_breached") {
		t.Fatalf("block reason = %q, want the engine cause preserved", account.BlockReason)
	}
	if err := domain.ValidateBlockReason(account.BlockReason); err != nil {
		t.Fatalf("persisted block reason is not restorable: %v", err)
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
	if _, err := rs.RecordOrderSettlement(ctx, st); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("RecordOrderSettlement(missing order) = %v, want ErrNotFound", err)
	}
}

func TestRecordOrderSettlementRejectsMissingOrderHandle(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	seedBalance(t, ctx, rs, "acc-1", "USD", "1000", "0", "0", "0")

	st := domain.OrderSettlement{
		Account: "acc-1",
		Balances: []domain.BalanceSettlement{
			{Asset: "USD", Outcome: domain.AdjustmentOutcomeAccepted{BalanceResult: "1000", HeldResult: "100"}},
		},
	}
	if _, err := rs.RecordOrderSettlement(ctx, st); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RecordOrderSettlement(zero order) = %v, want ErrInvalid", err)
	}
	bal, ok := getBalanceRow(t, ctx, rs, "acc-1", "USD")
	if !ok {
		t.Fatal("seed balance missing")
	}
	if bal.held != "0" || bal.available != "1000" {
		t.Fatalf("rejected settlement changed balance: %+v", bal)
	}
}

func TestRecordOrderSettlementPrunesEconomicallyEmptyBalance(t *testing.T) {
	settle := func(
		t *testing.T, realizedPnl string, haltReason domain.PnlHaltReason,
	) (domain.Balance, bool) {
		t.Helper()
		ctx, rs := seedOrderFixtures(t)
		created, err := rs.CreateOrder(ctx, sampleOrder())
		if err != nil {
			t.Fatalf("CreateOrder: %v", err)
		}
		if realizedPnl != "" || haltReason != "" {
			seedBalance(t, ctx, rs, "acc-1", "USD", "0", "0", "0", realizedPnl)
		}
		if haltReason != "" {
			if _, err := rs.(*realmStore).rawDB().ExecContext(
				ctx,
				`UPDATE balance
				 SET realized_pnl = NULL, realized_pnl_halt_reason = ?
				 WHERE account_id = (SELECT id FROM account WHERE code = ?)
				   AND asset_id = (SELECT id FROM asset WHERE code = ?)`,
				haltReason, "acc-1", "USD",
			); err != nil {
				t.Fatalf("seed halted balance: %v", err)
			}
		}

		st := domain.OrderSettlement{
			Order:       created.ExternalID,
			Account:     "acc-1",
			OrderStatus: domain.OrderStatusCommitted,
			Balances: []domain.BalanceSettlement{
				{
					Asset: "USD",
					Outcome: domain.AdjustmentOutcomeAccepted{
						BalanceResult:  "0",
						HeldResult:     "0",
						IncomingResult: "0",
					},
				},
			},
		}
		if _, err := rs.RecordOrderSettlement(ctx, st); err != nil {
			t.Fatalf("RecordOrderSettlement: %v", err)
		}
		balance, ok, err := rs.GetBalance(ctx, "acc-1", "USD")
		if err != nil {
			t.Fatalf("GetBalance: %v", err)
		}
		return balance, ok
	}

	type balanceCase struct {
		name        string
		realized    string
		haltReason  domain.PnlHaltReason
		wantPresent bool
	}
	tests := []balanceCase{
		{name: "unset realized PnL"},
		{name: "exact zero realized PnL", realized: "0"},
	}
	for _, halt := range []domain.PnlHaltReason{
		domain.PnlHaltReasonMissingFx,
		domain.PnlHaltReasonMissingAccountCurrency,
		domain.PnlHaltReasonMissingInitialPnl,
		domain.PnlHaltReasonMissingCostBasis,
		domain.PnlHaltReasonArithmeticOverflow,
	} {
		tests = append(tests, balanceCase{
			name: string(halt), haltReason: halt, wantPresent: true,
		})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			balance, ok := settle(t, test.realized, test.haltReason)
			if ok != test.wantPresent {
				t.Fatalf("balance = %+v ok=%v, want present %v", balance, ok, test.wantPresent)
			}
			if ok && (balance.RealizedPnl != test.realized ||
				balance.RealizedPnlHaltReason != test.haltReason) {
				t.Fatalf(
					"balance = %+v, want realized PnL %q and halt %q",
					balance,
					test.realized,
					test.haltReason,
				)
			}
		})
	}
}

func TestBalanceAmountsEmptyRejectsEveryNonzeroEconomicValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		available         string
		held              string
		incoming          string
		realized          string
		pnlHaltReason     string
		averageEntryPrice string
		want              bool
	}{
		{name: "unset", want: true},
		{name: "exact zero pnl", realized: "0", want: true},
		{name: "missing fx halt", pnlHaltReason: "missing_fx"},
		{name: "missing account currency halt", pnlHaltReason: "missing_account_currency"},
		{name: "missing initial pnl halt", pnlHaltReason: "missing_initial_pnl"},
		{name: "missing cost basis halt", pnlHaltReason: "missing_cost_basis"},
		{name: "arithmetic overflow halt", pnlHaltReason: "arithmetic_overflow"},
		{name: "available", available: "1"},
		{name: "held", held: "-1"},
		{name: "incoming", incoming: "0.1"},
		{name: "realized pnl", realized: "-0.1"},
		{
			name:      "halt does not hide available",
			available: "1", pnlHaltReason: "missing_fx",
		},
		{name: "average entry price", averageEntryPrice: "10"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := balanceAmountsEmpty(
				test.available,
				test.held,
				test.incoming,
				test.realized,
				test.pnlHaltReason,
				test.averageEntryPrice,
			)
			if got != test.want {
				t.Fatalf("balanceAmountsEmpty() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRecordOrderSettlementAccountPnlHaltLifecycle(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)

	createOrder := func() domain.Order {
		t.Helper()
		order, err := rs.CreateOrder(ctx, sampleOrder())
		if err != nil {
			t.Fatalf("CreateOrder: %v", err)
		}
		return order
	}
	settle := func(order domain.Order, pnl string, reason domain.PnlHaltReason) {
		t.Helper()
		if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
			Order: order.ExternalID, Account: "acc-1",
			OrderStatus: domain.OrderStatusCommitted,
			AccountPnl:  pnl, AccountPnlHaltReason: reason,
		}); err != nil {
			t.Fatalf("RecordOrderSettlement: %v", err)
		}
	}
	getAccount := func() domain.Account {
		t.Helper()
		account, ok, err := rs.GetAccount(ctx, "acc-1")
		if err != nil || !ok {
			t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
		}
		return account
	}

	settle(createOrder(), "", domain.PnlHaltReasonMissingFx)
	account := getAccount()
	if account.PnlHaltReason != domain.PnlHaltReasonMissingFx {
		t.Fatalf("account halt reason = %q", account.PnlHaltReason)
	}
	if account.Pnl != "" {
		t.Fatalf("account pnl after halt = %q, want empty", account.Pnl)
	}
	var storedPnl any
	if err := rs.(*realmStore).rawDB().QueryRowContext(
		ctx, `SELECT pnl FROM account WHERE code = ?`, "acc-1",
	).Scan(&storedPnl); err != nil {
		t.Fatalf("read stored halted pnl: %v", err)
	}
	if storedPnl != nil {
		t.Fatalf("stored halted pnl = %#v, want NULL", storedPnl)
	}

	settle(createOrder(), "", "")
	account = getAccount()
	if account.PnlHaltReason != domain.PnlHaltReasonMissingFx {
		t.Fatalf("unrelated settlement cleared account halt: %q", account.PnlHaltReason)
	}

	settle(createOrder(), "0", "")
	account = getAccount()
	if account.Pnl != "0" || account.PnlHaltReason != "" {
		t.Fatalf("computed zero account PnL did not clear halt: %+v", account)
	}
	settle(createOrder(), "", "")
	account = getAccount()
	if account.Pnl != "0" || account.PnlHaltReason != "" {
		t.Fatalf("unrelated settlement overwrote zero account PnL: %+v", account)
	}

	settle(createOrder(), "7.250", "")
	account = getAccount()
	if account.Pnl != "7.250" || account.PnlHaltReason != "" {
		t.Fatalf("authoritative account PnL was not persisted: %+v", account)
	}
	settle(createOrder(), "", "")
	account = getAccount()
	if account.Pnl != "7.250" || account.PnlHaltReason != "" {
		t.Fatalf("unrelated settlement overwrote account PnL: %+v", account)
	}
}

func TestRecordOrderSettlementPreservesHaltOnlyAndPrunesZeroBalance(t *testing.T) {
	ctx, rs := seedOrderFixtures(t)
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingCostBasis,
	}); err != nil {
		t.Fatalf("UpsertBalance: %v", err)
	}

	createOrder := func() domain.Order {
		t.Helper()
		order, err := rs.CreateOrder(ctx, sampleOrder())
		if err != nil {
			t.Fatalf("CreateOrder: %v", err)
		}
		return order
	}
	settle := func(order domain.Order, realizedPnlResult string) {
		t.Helper()
		if _, err := rs.RecordOrderSettlement(ctx, domain.OrderSettlement{
			Order: order.ExternalID, Account: "acc-1",
			OrderStatus: domain.OrderStatusCommitted,
			Balances: []domain.BalanceSettlement{{
				Asset: "USD",
				Outcome: domain.AdjustmentOutcomeAccepted{
					BalanceResult: "0", HeldResult: "0", IncomingResult: "0",
					RealizedPnlResult: realizedPnlResult,
				},
			}},
		}); err != nil {
			t.Fatalf("RecordOrderSettlement: %v", err)
		}
	}

	settle(createOrder(), "")
	balance, ok, err := rs.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance(halted): ok=%v err=%v", ok, err)
	}
	if balance.RealizedPnl != "" ||
		balance.RealizedPnlHaltReason != domain.PnlHaltReasonMissingCostBasis {
		t.Fatalf("halted balance = %+v, want missing_cost_basis without value", balance)
	}

	settle(createOrder(), "0")
	if balance, ok, err := rs.GetBalance(ctx, "acc-1", "USD"); err != nil || ok {
		t.Fatalf("GetBalance(zero) = %+v ok=%v err=%v, want no row", balance, ok, err)
	}

	settle(createOrder(), "1")
	balance, ok, err = rs.GetBalance(ctx, "acc-1", "USD")
	if err != nil || !ok {
		t.Fatalf("GetBalance(nonzero): ok=%v err=%v", ok, err)
	}
	if balance.RealizedPnl != "1" || balance.RealizedPnlHaltReason != "" {
		t.Fatalf("balance = %+v, want authoritative realized_pnl 1", balance)
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
		IssuedAt: "2026-06-26T10:00:00Z",
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
