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

// Reservation-group tests: intent upsert/get keyed by approval_id (the UUID
// handle, never a surrogate id), list-open by state, state transitions, the
// byte-exact lock BLOB round-trip, ResolveOrderReservation linking a held intent
// to a created order atomically (with the status guard, the missing-intent no-op
// and the in-memory-only-hold path), and the ON DELETE CASCADE on account and
// order deletes that removes the intent.

package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

// seedReservationOrder creates the dictionaries and one held-style order the
// reservation tests link to, returning the realm handle and the order handle.
func seedReservationOrder(t *testing.T) (context.Context, RealmStore, domain.ExternalID) {
	t.Helper()
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
	o, err := rs.CreateOrder(ctx, domain.Order{
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Source:      domain.SourcePanel,
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "10",
		Price:       "150",
		Status:      domain.OrderStatusAccepted,
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	return ctx, rs, o.ExternalID
}

func TestReservationUpsertGetAndLockRoundTrip(t *testing.T) {
	ctx, rs, order := seedReservationOrder(t)

	lock := []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01}
	intent := domain.ReservationIntent{
		ApprovalID: "appr-1",
		Order:      order,
		Account:    "acc-1",
		ParamsJSON: `{"side":"buy","qty":"10"}`,
		Lock:       lock,
		IssuedAt:   time.Now().UTC().Truncate(time.Nanosecond),
		ExpiresAt:  time.Now().UTC().Add(time.Minute).Truncate(time.Nanosecond),
		State:      domain.ReservationIntentStateHeld,
	}
	if err := rs.UpsertReservationIntent(ctx, intent); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}

	got, ok, err := rs.GetReservationIntent(ctx, "appr-1")
	if err != nil || !ok {
		t.Fatalf("GetReservationIntent: ok=%v err=%v", ok, err)
	}
	if got.ApprovalID != "appr-1" || got.Account != "acc-1" {
		t.Fatalf("intent identity = %+v", got)
	}
	if got.Order != order {
		t.Fatalf("intent order = %v, want %v", got.Order, order)
	}
	if got.ParamsJSON != intent.ParamsJSON {
		t.Fatalf("params round-trip = %q, want %q", got.ParamsJSON, intent.ParamsJSON)
	}
	if !bytes.Equal(got.Lock, lock) {
		t.Fatalf("lock BLOB round-trip = %v, want %v", got.Lock, lock)
	}
	if got.State != domain.ReservationIntentStateHeld {
		t.Fatalf("state = %q, want held", got.State)
	}
	if !got.IssuedAt.Equal(intent.IssuedAt) || !got.ExpiresAt.Equal(intent.ExpiresAt) {
		t.Fatalf("timestamps round-trip = (%v, %v)", got.IssuedAt, got.ExpiresAt)
	}

	// Missing intent reports ok=false, not an error.
	if _, ok, err := rs.GetReservationIntent(ctx, "ghost"); err != nil || ok {
		t.Fatalf("GetReservationIntent(missing): ok=%v err=%v, want ok=false", ok, err)
	}

	// Upsert replaces in place (no second row): change the params, re-list-open.
	intent.ParamsJSON = `{"side":"buy","qty":"20"}`
	if err := rs.UpsertReservationIntent(ctx, intent); err != nil {
		t.Fatalf("UpsertReservationIntent(replace): %v", err)
	}
	open, err := rs.ListOpenReservationIntents(ctx)
	if err != nil {
		t.Fatalf("ListOpenReservationIntents: %v", err)
	}
	if len(open) != 1 || open[0].ParamsJSON != intent.ParamsJSON {
		t.Fatalf("ListOpenReservationIntents after replace = %+v", open)
	}
}

func TestReservationInMemoryHoldHasNoOrder(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// A zero Order is an in-memory-only hold: order_id is NULL.
	if err := rs.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID: "appr-mem",
		Account:    "acc-1",
		ParamsJSON: "{}",
		IssuedAt:   time.Now().UTC(),
		ExpiresAt:  time.Now().UTC().Add(time.Minute),
		State:      domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent(in-memory): %v", err)
	}
	got, ok, err := rs.GetReservationIntent(ctx, "appr-mem")
	if err != nil || !ok {
		t.Fatalf("GetReservationIntent: ok=%v err=%v", ok, err)
	}
	if !got.Order.IsZero() {
		t.Fatalf("in-memory hold order = %v, want zero", got.Order)
	}
}

func TestReservationUpsertUnknownAccountIsInvalid(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	if err := rs.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID: "appr-x",
		Account:    "ghost",
		ParamsJSON: "{}",
		IssuedAt:   time.Now().UTC(),
		ExpiresAt:  time.Now().UTC().Add(time.Minute),
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("UpsertReservationIntent(unknown account) error = %v, want ErrInvalid", err)
	}
}

func TestReservationStateTransitionAndListOpen(t *testing.T) {
	ctx, rs, order := seedReservationOrder(t)

	mk := func(id string) domain.ReservationIntent {
		return domain.ReservationIntent{
			ApprovalID: id,
			Order:      order,
			Account:    "acc-1",
			ParamsJSON: "{}",
			IssuedAt:   time.Now().UTC(),
			ExpiresAt:  time.Now().UTC().Add(time.Minute),
			State:      domain.ReservationIntentStateHeld,
		}
	}
	if err := rs.UpsertReservationIntent(ctx, mk("a")); err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	if err := rs.UpsertReservationIntent(ctx, mk("b")); err != nil {
		t.Fatalf("upsert b: %v", err)
	}

	open, err := rs.ListOpenReservationIntents(ctx)
	if err != nil {
		t.Fatalf("ListOpenReservationIntents: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("open count = %d, want 2", len(open))
	}

	// Flip one to committed: it drops out of the open listing.
	if err := rs.SetReservationIntentState(ctx, "a", domain.ReservationIntentStateCommitted); err != nil {
		t.Fatalf("SetReservationIntentState: %v", err)
	}
	open, _ = rs.ListOpenReservationIntents(ctx)
	if len(open) != 1 || open[0].ApprovalID != "b" {
		t.Fatalf("open after commit = %+v, want only b", open)
	}
	// GetReservationIntent still returns the committed row regardless of state.
	got, ok, _ := rs.GetReservationIntent(ctx, "a")
	if !ok || got.State != domain.ReservationIntentStateCommitted {
		t.Fatalf("committed intent = %+v ok=%v", got, ok)
	}

	// A transition on a missing intent is ErrNotFound.
	if err := rs.SetReservationIntentState(
		ctx, "ghost", domain.ReservationIntentStateRolledBack,
	); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetReservationIntentState(missing) error = %v, want ErrNotFound", err)
	}
}

func TestResolveOrderReservation(t *testing.T) {
	ctx, rs, order := seedReservationOrder(t)

	if err := rs.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID: "appr-1",
		Order:      order,
		Account:    "acc-1",
		ParamsJSON: "{}",
		IssuedAt:   time.Now().UTC(),
		ExpiresAt:  time.Now().UTC().Add(time.Minute),
		State:      domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}

	// Resolve: flip the intent to committed, advance the order accepted->committed
	// (guarded), and append a lifecycle event — all atomic.
	if err := rs.ResolveOrderReservation(ctx, domain.ReservationResolution{
		ApprovalID:  "appr-1",
		IntentState: domain.ReservationIntentStateCommitted,
		OrderStatus: domain.OrderStatusCommitted,
		Order:       order,
		AllowedFrom: []domain.OrderStatus{domain.OrderStatusAccepted},
		Events: []domain.OrderEvent{{
			Order:  order,
			Type:   domain.OrderEventReservationCommitted,
			Source: domain.SourcePanel,
		}},
	}); err != nil {
		t.Fatalf("ResolveOrderReservation: %v", err)
	}

	// Intent flipped.
	intent, _, _ := rs.GetReservationIntent(ctx, "appr-1")
	if intent.State != domain.ReservationIntentStateCommitted {
		t.Fatalf("intent state = %q, want committed", intent.State)
	}
	// Order advanced and the event landed.
	detail, err := rs.GetOrder(ctx, order)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Status != domain.OrderStatusCommitted {
		t.Fatalf("order status = %q, want committed", detail.Order.Status)
	}
	if len(detail.Events) != 1 || detail.Events[0].Type != domain.OrderEventReservationCommitted {
		t.Fatalf("order events = %+v", detail.Events)
	}
}

func TestResolveOrderReservationGuardConflict(t *testing.T) {
	ctx, rs, order := seedReservationOrder(t)
	if err := rs.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID: "appr-1", Order: order, Account: "acc-1", ParamsJSON: "{}",
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute),
		State: domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}
	// The order is in 'accepted'; a guard that excludes it yields Conflict with
	// nothing written (TOCTOU-safe against a fill that already advanced it).
	err := rs.ResolveOrderReservation(ctx, domain.ReservationResolution{
		ApprovalID:  "appr-1",
		IntentState: domain.ReservationIntentStateCommitted,
		OrderStatus: domain.OrderStatusCommitted,
		Order:       order,
		AllowedFrom: []domain.OrderStatus{domain.OrderStatusCommitted},
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("ResolveOrderReservation(guard) error = %v, want ErrConflict", err)
	}
	// Rolled back: the intent stays held and the order stays accepted.
	intent, _, _ := rs.GetReservationIntent(ctx, "appr-1")
	if intent.State != domain.ReservationIntentStateHeld {
		t.Fatalf("intent state after conflict = %q, want held (rolled back)", intent.State)
	}
	detail, _ := rs.GetOrder(ctx, order)
	if detail.Order.Status != domain.OrderStatusAccepted {
		t.Fatalf("order status after conflict = %q, want accepted (rolled back)", detail.Order.Status)
	}
}

func TestResolveOrderReservationMissingIntentTolerated(t *testing.T) {
	ctx, rs, order := seedReservationOrder(t)
	// No intent persisted: a resolve naming a missing approval is a no-op on the
	// intent side but still advances the order (the caller may resolve a hold whose
	// intent was already swept).
	if err := rs.ResolveOrderReservation(ctx, domain.ReservationResolution{
		ApprovalID:  "never-persisted",
		IntentState: domain.ReservationIntentStateRolledBack,
		OrderStatus: domain.OrderStatusCancelled,
		Order:       order,
		AllowedFrom: []domain.OrderStatus{domain.OrderStatusAccepted},
	}); err != nil {
		t.Fatalf("ResolveOrderReservation(missing intent): %v", err)
	}
	detail, _ := rs.GetOrder(ctx, order)
	if detail.Order.Status != domain.OrderStatusCancelled {
		t.Fatalf("order status = %q, want cancelled", detail.Order.Status)
	}
}

func TestResolveOrderReservationInMemoryHold(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := rs.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID: "appr-mem", Account: "acc-1", ParamsJSON: "{}",
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute),
		State: domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}
	// A zero Order skips the order/event writes and only flips the intent.
	if err := rs.ResolveOrderReservation(ctx, domain.ReservationResolution{
		ApprovalID:  "appr-mem",
		IntentState: domain.ReservationIntentStateRolledBack,
	}); err != nil {
		t.Fatalf("ResolveOrderReservation(in-memory): %v", err)
	}
	intent, _, _ := rs.GetReservationIntent(ctx, "appr-mem")
	if intent.State != domain.ReservationIntentStateRolledBack {
		t.Fatalf("in-memory intent state = %q, want rolled_back", intent.State)
	}
}

func TestReservationCascadeOnOrderDelete(t *testing.T) {
	ctx, rs, order := seedReservationOrder(t)
	if err := rs.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID: "appr-1", Order: order, Account: "acc-1", ParamsJSON: "{}",
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute),
		State: domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}

	// Deleting the order cascades to its reservation intent.
	r := rs.(*realmStore)
	if _, err := r.db().ExecContext(
		ctx, `DELETE FROM order_record WHERE external_id = ?`, order.Bytes(),
	); err != nil {
		t.Fatalf("delete order: %v", err)
	}
	if _, ok, err := rs.GetReservationIntent(ctx, "appr-1"); err != nil || ok {
		t.Fatalf("intent after order delete: ok=%v err=%v, want ok=false (cascaded)", ok, err)
	}
}

func TestReservationCascadeOnAccountDelete(t *testing.T) {
	ctx, rs, order := seedReservationOrder(t)
	if err := rs.UpsertReservationIntent(ctx, domain.ReservationIntent{
		ApprovalID: "appr-1", Order: order, Account: "acc-1", ParamsJSON: "{}",
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute),
		State: domain.ReservationIntentStateHeld,
	}); err != nil {
		t.Fatalf("UpsertReservationIntent: %v", err)
	}

	// Deleting the account cascades through the order (and the intent's own account
	// FK) to remove the intent.
	r := rs.(*realmStore)
	if _, err := r.db().ExecContext(
		ctx, `DELETE FROM account WHERE code = ?`, "acc-1",
	); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	if _, ok, err := rs.GetReservationIntent(ctx, "appr-1"); err != nil || ok {
		t.Fatalf("intent after account delete: ok=%v err=%v, want ok=false (cascaded)", ok, err)
	}
}
