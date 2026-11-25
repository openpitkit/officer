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
// Please see https://officer.openpit.dev and the OWNERS file for details.

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.openpit.dev/officer/framework/domain"
)

type resolvedOrderReview struct {
	modeID     int64
	held       bool
	connection any
	account    any
	route      any
}

func resolveOrderReview(ctx context.Context, dictionaries *enumDictionaries, tx *sql.Tx,
	order domain.ExternalID, review domain.OrderReview,
) (resolvedOrderReview, error) {
	modeID, err := dictionaries.id(attestationModeTable, "review mode", review.Mode)
	if err != nil {
		return resolvedOrderReview{}, fmt.Errorf("store: order %q review: %w", order, domain.Internal(err))
	}
	resolved := resolvedOrderReview{modeID: modeID, held: review.Held}
	if review.Destination != nil {
		destination := review.Destination
		if err := domain.ValidateTradingDestination(*destination); err != nil {
			return resolvedOrderReview{}, fmt.Errorf("store: order %q review destination: %w", order, err)
		}
		connectionID, err := resolveTradingConnectionID(ctx, tx, destination.Connection)
		if errors.Is(err, domain.ErrNotFound) {
			return resolvedOrderReview{}, fmt.Errorf("store: order %q review connection %q: %w", order, destination.Connection, domain.ErrInvalid)
		}
		if err != nil {
			return resolvedOrderReview{}, err
		}
		resolved.connection, resolved.account, resolved.route = connectionID, destination.VenueAccount, destination.Route
	}
	return resolved, nil
}

func insertOrderReview(ctx context.Context, tx *sql.Tx,
	orderID int64, order domain.ExternalID, review resolvedOrderReview,
) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO order_review (order_id, mode_id, held, connection_id, venue_account, route)
		 VALUES (?, ?, ?, ?, ?, ?)`, orderID, review.modeID, review.held, review.connection, review.account, review.route,
	); err != nil {
		return fmt.Errorf("store: insert review for order %q: %w", order, err)
	}
	return nil
}

func getOrderReview(ctx context.Context, q sqlQueryer, order domain.ExternalID) (*domain.OrderReview, error) {
	var review domain.OrderReview
	var connection []byte
	var account, route sql.NullString
	err := q.QueryRowContext(ctx,
		`SELECT m.code, r.held, c.external_id, r.venue_account, r.route
		 FROM order_review r
		 JOIN order_record o ON o.id = r.order_id
		 JOIN attestation_mode m ON m.id = r.mode_id
		 LEFT JOIN trading_connection c ON c.id = r.connection_id
		 WHERE o.external_id = ?`, order.Bytes(),
	).Scan(&review.Mode, &review.Held, &connection, &account, &route)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read review for order %q: %w", order, err)
	}
	if connection != nil {
		id, err := domain.ExternalIDFromBytes(connection)
		if err != nil {
			return nil, fmt.Errorf("store: decode review connection for order %q: %w", order, domain.Internal(err))
		}
		review.Destination = &domain.TradingDestination{Connection: id, VenueAccount: account.String, Route: route.String}
	}
	return &review, nil
}

// A zero order checks the whole realm at the end of restore.
func checkOrderReviewInvariant(ctx context.Context, dictionaries *enumDictionaries,
	q sqlQueryer, order domain.ExternalID,
) error {
	statusID, err := dictionaries.id(orderStatusTable, "order status", string(domain.OrderStatusAwaitingReview))
	if err != nil {
		return domain.Internal(err)
	}
	query := `SELECT o.external_id FROM order_record o
	 LEFT JOIN order_review r ON r.order_id = o.id
	 WHERE ((o.status_id = ?) <> (r.order_id IS NOT NULL))`
	args := []any{statusID}
	if !order.IsZero() {
		query += ` AND o.external_id = ?`
		args = append(args, order.Bytes())
	}
	query += ` LIMIT 1`
	var raw []byte
	err = q.QueryRowContext(ctx, query, args...).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: check order review invariant: %w", err)
	}
	id, err := domain.ExternalIDFromBytes(raw)
	if err != nil {
		return fmt.Errorf("store: decode review invariant order: %w", domain.Internal(err))
	}
	return fmt.Errorf("store: order %q status and order_review row disagree: %w", id, domain.ErrInvalid)
}
