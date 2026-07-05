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

// Reservation group of the SQLite store: held pre-trade reservation intents that
// must survive a process restart. The intent is addressed by its own UUID
// approval_id, which is the row's primary key and the external handle carried in
// approval tokens — there is no surrogate id to expose. The optional order link
// is resolved from the order's external id to its surrogate id (CASCADE: deleting
// the order removes the intent); the account link is resolved by code (CASCADE
// too). params is opaque JSON stored whole; lock is the SDK-serialized
// pretrade.Lock blob persisted verbatim and read back byte-identical — this
// package never (de)serializes it. ResolveOrderReservation links a held intent to
// a created order and advances the order atomically, reusing the order_record-group
// status guard so the intent flip, the order-status advance and the lifecycle
// events commit or roll back together.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

// reservationSelect is the shared projection for reservation reads. The optional
// order is surfaced as its external id (NULL for an in-memory-only hold); the
// account is surfaced as its code. The lock BLOB is read back verbatim.
const reservationSelect = `
SELECT ri.approval_id, o.external_id, a.code, ri.params, ri.lock,
       ri.issued_at, ri.expires_at, ri.state
FROM reservation_intent ri
LEFT JOIN order_record   o ON o.id = ri.order_id
JOIN account a      ON a.id = ri.account_id`

// UpsertReservationIntent inserts or replaces a reservation intent row keyed by
// its approval_id UUID. The account is resolved by code and the optional order by
// its external id; the lock blob is persisted verbatim. params is stored whole as
// opaque JSON.
func (r *realmStore) UpsertReservationIntent(
	ctx context.Context, intent domain.ReservationIntent,
) error {
	accountID, err := resolveAccountID(ctx, r.db(), intent.Account)
	if err != nil {
		return err
	}
	orderID, err := optionalOrderID(ctx, r.db(), intent.Order)
	if err != nil {
		return err
	}
	if _, err := r.db().ExecContext(
		ctx,
		`INSERT INTO reservation_intent
		 (approval_id, order_id, account_id, params, lock, issued_at, expires_at, state)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(approval_id) DO UPDATE SET
		   order_id   = excluded.order_id,
		   account_id = excluded.account_id,
		   params     = excluded.params,
		   lock       = excluded.lock,
		   issued_at  = excluded.issued_at,
		   expires_at = excluded.expires_at,
		   state      = excluded.state`,
		intent.ApprovalID, orderID, accountID, intent.ParamsJSON,
		nullableBlob(intent.Lock),
		reservationTimeStr(intent.IssuedAt), reservationTimeStr(intent.ExpiresAt),
		string(reservationState(intent.State)),
	); err != nil {
		return fmt.Errorf("store: upsert reservation intent: %w", err)
	}
	return nil
}

// GetReservationIntent returns the intent for approvalID regardless of state;
// found is false when absent.
func (r *realmStore) GetReservationIntent(
	ctx context.Context, approvalID string,
) (domain.ReservationIntent, bool, error) {
	row := r.db().QueryRowContext(
		ctx, reservationSelect+` WHERE ri.approval_id = ?`, approvalID,
	)
	intent, err := scanReservationRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReservationIntent{}, false, nil
	}
	if err != nil {
		return domain.ReservationIntent{}, false, fmt.Errorf("store: get reservation intent: %w", err)
	}
	return intent, true, nil
}

// ListOpenReservationIntents returns all intents whose state is held, ordered by
// issued_at (oldest first) so the TTL sweeper sees the longest-held first.
func (r *realmStore) ListOpenReservationIntents(
	ctx context.Context,
) ([]domain.ReservationIntent, error) {
	rows, err := r.db().QueryContext(
		ctx,
		reservationSelect+` WHERE ri.state = ? ORDER BY ri.issued_at ASC, ri.approval_id ASC`,
		string(domain.ReservationIntentStateHeld),
	)
	if err != nil {
		return nil, fmt.Errorf("store: list open reservation intents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.ReservationIntent, 0)
	for rows.Next() {
		intent, err := scanReservationRows(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, intent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate reservation intents: %w", err)
	}
	return result, nil
}

// SetReservationIntentState updates the state of the identified intent. Returns
// domain.ErrNotFound when absent.
func (r *realmStore) SetReservationIntentState(
	ctx context.Context, approvalID string, state domain.ReservationIntentState,
) error {
	res, err := r.db().ExecContext(
		ctx,
		`UPDATE reservation_intent SET state = ? WHERE approval_id = ?`,
		string(state), approvalID,
	)
	if err != nil {
		return fmt.Errorf("store: set reservation intent state: %w", err)
	}
	return notFoundIfNoRows(res, "reservation intent", approvalID)
}

// ResolveOrderReservation resolves one reservation atomically in a single
// transaction: the intent state flip, the order status advance, and the
// lifecycle event(s) commit or roll back together. The order status UPDATE is
// guarded by AllowedFrom (callers pass {OrderStatusAccepted}); a current status
// outside the guard yields domain.ErrConflict with nothing written, and a missing
// order yields domain.ErrNotFound. A missing intent row is tolerated as a no-op;
// a zero Order skips the order/event writes and only flips the intent.
func (r *realmStore) ResolveOrderReservation(
	ctx context.Context, res domain.ReservationResolution,
) error {
	tx, err := r.db().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin resolve_order_reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Intent state flip. A missing intent is tolerated (no-op), mirroring
	// SetReservationIntentState's NotFound tolerance on the resolve paths: the
	// reservation may have already been swept or never persisted.
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE reservation_intent SET state = ? WHERE approval_id = ?`,
		string(res.IntentState), res.ApprovalID,
	); err != nil {
		return fmt.Errorf("store: resolve reservation intent state: %w", err)
	}

	// An in-memory-only hold carries no order row: only the intent flip applies.
	if res.Order.IsZero() {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit resolve_order_reservation: %w", err)
		}
		return nil
	}

	// Resolve the order surrogate id once; events and the guarded status advance
	// reuse it.
	orderID, err := lookupOrderID(ctx, tx, res.Order)
	if err != nil {
		return err
	}

	// Lifecycle event(s) (e.g. reservation_committed, or reservation_rolled_back +
	// cancelled).
	for _, ev := range res.Events {
		if err := appendOrderEventTx(ctx, tx, orderID, ev); err != nil {
			return err
		}
	}

	// Order status advance last; guarded only when AllowedFrom is set, reusing the
	// order_record-group guard that distinguishes a missing order (NotFound) from a
	// disallowed current status (Conflict).
	if err := guardedOrderStatus(
		ctx, tx, orderID, res.Order, res.OrderStatus, res.AllowedFrom,
	); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit resolve_order_reservation: %w", err)
	}
	return nil
}

// --- Reservation helpers ----------------------------------------------------

// optionalOrderID resolves an optional order external id to a nullable surrogate
// id: the zero external id yields a NULL order link (an in-memory-only hold); a
// non-zero external id that names no order wraps domain.ErrNotFound.
func optionalOrderID(
	ctx context.Context, q sqlQueryer, id domain.ExternalID,
) (sql.NullInt64, error) {
	if id.IsZero() {
		return sql.NullInt64{}, nil
	}
	orderID, err := lookupOrderID(ctx, q, id)
	if err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: orderID, Valid: true}, nil
}

// reservationState defaults an empty state to held so a freshly issued intent is
// never stored with an empty state.
func reservationState(state domain.ReservationIntentState) domain.ReservationIntentState {
	if state == "" {
		return domain.ReservationIntentStateHeld
	}
	return state
}

// reservationTimeStr formats a reservation timestamp as RFC3339Nano UTC text.
func reservationTimeStr(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// scanReservationRow scans one intent from a single row.
func scanReservationRow(row *sql.Row) (domain.ReservationIntent, error) {
	return scanReservationInto(row.Scan)
}

// scanReservationRows scans one intent from a multi-row result.
func scanReservationRows(rows *sql.Rows) (domain.ReservationIntent, error) {
	intent, err := scanReservationInto(rows.Scan)
	if err != nil {
		return domain.ReservationIntent{}, fmt.Errorf("store: scan reservation intent: %w", err)
	}
	return intent, nil
}

// scanReservationInto scans one reservation projection through scan, decoding the
// nullable order external-id BLOB, the lock BLOB (verbatim) and the timestamps.
// The scan func is *sql.Row.Scan or *sql.Rows.Scan.
func scanReservationInto(scan func(...any) error) (domain.ReservationIntent, error) {
	var (
		approvalID, account, params, state string
		orderExtID                         []byte
		lock                               []byte
		issuedAt, expiresAt                string
	)
	if err := scan(
		&approvalID, &orderExtID, &account, &params, &lock,
		&issuedAt, &expiresAt, &state,
	); err != nil {
		return domain.ReservationIntent{}, err
	}
	var order domain.ExternalID
	if orderExtID != nil {
		xid, err := domain.ExternalIDFromBytes(orderExtID)
		if err != nil {
			return domain.ReservationIntent{}, fmt.Errorf(
				"store: decode reservation order external id: %w", err,
			)
		}
		order = xid
	}
	parsedIssued, err := time.Parse(time.RFC3339Nano, issuedAt)
	if err != nil {
		return domain.ReservationIntent{}, fmt.Errorf(
			"store: parse reservation issued_at %q: %w", issuedAt, err,
		)
	}
	parsedExpires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return domain.ReservationIntent{}, fmt.Errorf(
			"store: parse reservation expires_at %q: %w", expiresAt, err,
		)
	}
	return domain.ReservationIntent{
		ApprovalID: approvalID,
		Order:      order,
		Account:    domain.AccountID(account),
		ParamsJSON: params,
		Lock:       lock,
		IssuedAt:   parsedIssued,
		ExpiresAt:  parsedExpires,
		State:      domain.ReservationIntentState(state),
	}, nil
}
