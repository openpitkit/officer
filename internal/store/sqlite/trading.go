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

// Trading group of the SQLite store: connections, instrument mappings, account
// access and durable venue-order links. Public entities carry external ids and
// asset or account codes; surrogate keys are resolved only for internal joins
// and never cross the store boundary. SQLite uniqueness violations are mapped
// to the domain conflict sentinels at the write boundary.

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

const tradingConnectionSelect = `
SELECT external_id, provider, label, mode, credentials, enabled
FROM trading_connection`

const tradingInstrumentSelect = `
SELECT c.external_id, i.external_symbol,
       base.code, quote.code, i.enabled
FROM trading_instrument i
JOIN trading_connection c ON c.id = i.connection_id
JOIN asset base ON base.id = i.base_asset_id
JOIN asset quote ON quote.id = i.quote_asset_id`

const venueOrderSelect = `
SELECT o.external_id, c.external_id, vo.venue_account, vo.route,
       vo.client_order_id, vo.send_attempted, vo.venue_order_id, vo.created_at
FROM venue_order vo
JOIN order_record o ON o.id = vo.order_id
JOIN trading_connection c ON c.id = vo.connection_id`

func (r *realmStore) CreateTradingConnection(
	ctx context.Context, connection domain.TradingConnection,
) (domain.TradingConnection, error) {
	if err := domain.ValidateTradingConnection(connection); err != nil {
		return connection, err
	}
	xid, err := externalIDForInsert(connection.ExternalID)
	if err != nil {
		return connection, err
	}
	credentials, err := r.store.sealValue(
		tradingConnectionTable, tradingCredentialsColumn,
		xid.String(), []byte(connection.Credentials),
	)
	if err != nil {
		return connection, err
	}
	db, err := r.db()
	if err != nil {
		return connection, err
	}
	_, err = db.ExecContext(
		ctx,
		`INSERT INTO trading_connection
		 (external_id, provider, label, mode, credentials, enabled)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		xid.Bytes(), connection.Provider, connection.Label, connection.Mode,
		credentials, connection.Enabled,
	)
	if err != nil {
		if isSQLiteUniqueOn(err, "trading_connection", "external_id") {
			return connection, fmt.Errorf(
				"trading connection %q: %w",
				xid.String(), domain.ErrAlreadyExists,
			)
		}
		if isSQLiteUnique(err) {
			return connection, fmt.Errorf(
				"trading connection label %q: %w",
				connection.Label, domain.ErrAlreadyExists,
			)
		}
		return connection, fmt.Errorf("store: create trading connection: %w", err)
	}
	connection.ExternalID = xid
	return connection, nil
}

func (r *realmStore) GetTradingConnection(
	ctx context.Context, id domain.ExternalID,
) (domain.TradingConnection, bool, error) {
	db, err := r.db()
	if err != nil {
		return domain.TradingConnection{}, false, err
	}
	connection, err := scanTradingConnection(
		db.QueryRowContext(
			ctx, tradingConnectionSelect+` WHERE external_id = ?`, id.Bytes(),
		),
		r.store,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TradingConnection{}, false, nil
	}
	if err != nil {
		return domain.TradingConnection{}, false,
			fmt.Errorf("store: get trading connection: %w", err)
	}
	return connection, true, nil
}

func (r *realmStore) ListTradingConnections(
	ctx context.Context,
) ([]domain.TradingConnection, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(
		ctx, tradingConnectionSelect+` ORDER BY external_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list trading connections: %w", err)
	}
	defer func() { _ = rows.Close() }()

	connections := make([]domain.TradingConnection, 0)
	for rows.Next() {
		connection, err := scanTradingConnection(rows, r.store)
		if err != nil {
			return nil, fmt.Errorf("store: scan trading connection: %w", err)
		}
		connections = append(connections, connection)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate trading connections: %w", err)
	}
	return connections, nil
}

func (r *realmStore) SetTradingConnectionEnabled(
	ctx context.Context, id domain.ExternalID, enabled bool,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(
		ctx,
		`UPDATE trading_connection SET enabled = ? WHERE external_id = ?`,
		enabled, id.Bytes(),
	)
	if err != nil {
		return fmt.Errorf("store: set trading connection enabled: %w", err)
	}
	return notFoundIfNoRows(result, "trading connection", id.String())
}

func scanTradingConnection(
	scanner sqlScanner, store *sqliteStore,
) (domain.TradingConnection, error) {
	var (
		connection  domain.TradingConnection
		rawID       []byte
		credentials []byte
	)
	if err := scanner.Scan(
		&rawID, &connection.Provider, &connection.Label, &connection.Mode,
		&credentials, &connection.Enabled,
	); err != nil {
		return domain.TradingConnection{}, err
	}
	xid, err := domain.ExternalIDFromBytes(rawID)
	if err != nil {
		return domain.TradingConnection{},
			fmt.Errorf("store: decode trading connection external id: %w", err)
	}
	connection.ExternalID = xid
	plaintext, err := store.openValue(
		tradingConnectionTable, tradingCredentialsColumn,
		xid.String(), credentials,
	)
	if err != nil {
		return domain.TradingConnection{}, err
	}
	connection.Credentials = string(plaintext)
	return connection, nil
}

func resolveTradingConnectionID(
	ctx context.Context, q sqlQueryer, id domain.ExternalID,
) (int64, error) {
	var connectionID int64
	err := q.QueryRowContext(
		ctx,
		`SELECT id FROM trading_connection WHERE external_id = ?`,
		id.Bytes(),
	).Scan(&connectionID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf(
			"trading connection %q: %w", id.String(), domain.ErrNotFound,
		)
	}
	if err != nil {
		return 0, fmt.Errorf(
			"store: resolve trading connection %q: %w", id.String(), err,
		)
	}
	return connectionID, nil
}

func resolveTradingAssetID(
	ctx context.Context, q sqlQueryer, code string,
) (int64, error) {
	id, err := resolveAssetID(ctx, q, code)
	if errors.Is(err, domain.ErrInvalid) {
		return 0, fmt.Errorf("asset %q: %w", code, domain.ErrNotFound)
	}
	return id, err
}

func (r *realmStore) UpsertTradingInstrument(
	ctx context.Context, instrument domain.TradingInstrument,
) error {
	if err := domain.ValidateTradingInstrument(instrument); err != nil {
		return err
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	connectionID, err := resolveTradingConnectionID(
		ctx, db, instrument.Connection,
	)
	if err != nil {
		return err
	}
	baseID, err := resolveTradingAssetID(ctx, db, instrument.BaseAsset)
	if err != nil {
		return err
	}
	quoteID, err := resolveTradingAssetID(ctx, db, instrument.QuoteAsset)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(
		ctx,
		`INSERT INTO trading_instrument
		 (connection_id, external_symbol, base_asset_id, quote_asset_id, enabled)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(connection_id, external_symbol) DO UPDATE SET
		   base_asset_id  = excluded.base_asset_id,
		   quote_asset_id = excluded.quote_asset_id,
		   enabled        = excluded.enabled`,
		connectionID, instrument.ExternalSymbol, baseID, quoteID,
		instrument.Enabled,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return fmt.Errorf(
				"trading instrument %q asset pair: %w",
				instrument.ExternalSymbol, domain.ErrAlreadyExists,
			)
		}
		return fmt.Errorf("store: upsert trading instrument: %w", err)
	}
	return nil
}

func (r *realmStore) ListTradingInstruments(
	ctx context.Context, connection domain.ExternalID,
) ([]domain.TradingInstrument, error) {
	return r.queryTradingInstruments(
		ctx,
		tradingInstrumentSelect+
			` WHERE c.external_id = ? ORDER BY i.external_symbol`,
		connection.Bytes(),
	)
}

func (r *realmStore) FindTradingInstrument(
	ctx context.Context,
	connection domain.ExternalID,
	baseAsset string,
	quoteAsset string,
) (domain.TradingInstrument, bool, error) {
	db, err := r.db()
	if err != nil {
		return domain.TradingInstrument{}, false, err
	}
	instrument, err := scanTradingInstrument(
		db.QueryRowContext(
			ctx,
			tradingInstrumentSelect+`
			 WHERE c.external_id = ? AND base.code = ? AND quote.code = ?`,
			connection.Bytes(), baseAsset, quoteAsset,
		),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TradingInstrument{}, false, nil
	}
	if err != nil {
		return domain.TradingInstrument{}, false,
			fmt.Errorf("store: find trading instrument: %w", err)
	}
	return instrument, true, nil
}

func (r *realmStore) queryTradingInstruments(
	ctx context.Context, query string, args ...any,
) ([]domain.TradingInstrument, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list trading instruments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	instruments := make([]domain.TradingInstrument, 0)
	for rows.Next() {
		instrument, err := scanTradingInstrument(rows)
		if err != nil {
			return nil, err
		}
		instruments = append(instruments, instrument)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate trading instruments: %w", err)
	}
	return instruments, nil
}

func scanTradingInstrument(
	scanner sqlScanner,
) (domain.TradingInstrument, error) {
	var (
		instrument domain.TradingInstrument
		rawID      []byte
	)
	if err := scanner.Scan(
		&rawID, &instrument.ExternalSymbol,
		&instrument.BaseAsset, &instrument.QuoteAsset,
		&instrument.Enabled,
	); err != nil {
		return domain.TradingInstrument{}, err
	}
	connection, err := domain.ExternalIDFromBytes(rawID)
	if err != nil {
		return domain.TradingInstrument{}, fmt.Errorf(
			"store: decode trading instrument connection external id: %w", err,
		)
	}
	instrument.Connection = connection
	return instrument, nil
}

func (r *realmStore) AddTradingAccess(
	ctx context.Context, access domain.TradingAccess,
) error {
	if err := domain.ValidateTradingAccess(access); err != nil {
		return err
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	accountID, err := resolveAccountID(ctx, db, access.Account)
	if errors.Is(err, domain.ErrInvalid) {
		return fmt.Errorf("account %q: %w", access.Account, domain.ErrNotFound)
	}
	if err != nil {
		return err
	}
	connectionID, err := resolveTradingConnectionID(ctx, db, access.Connection)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(
		ctx,
		`INSERT INTO trading_access (account_id, connection_id, venue_account)
		 VALUES (?, ?, ?)`,
		accountID, connectionID, access.VenueAccount,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return fmt.Errorf("trading access: %w", domain.ErrAlreadyExists)
		}
		return fmt.Errorf("store: add trading access: %w", err)
	}
	return nil
}

func (r *realmStore) ListTradingAccess(
	ctx context.Context, account domain.AccountID,
) ([]domain.TradingAccess, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(
		ctx,
		`SELECT a.code, c.external_id, ta.venue_account
		 FROM trading_access ta
		 JOIN account a ON a.id = ta.account_id
		 JOIN trading_connection c ON c.id = ta.connection_id
		 WHERE a.code = ?
		 ORDER BY c.external_id, ta.venue_account`,
		account,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list trading access: %w", err)
	}
	defer func() { _ = rows.Close() }()

	accesses := make([]domain.TradingAccess, 0)
	for rows.Next() {
		var access domain.TradingAccess
		var rawID []byte
		if err := rows.Scan(
			&access.Account, &rawID, &access.VenueAccount,
		); err != nil {
			return nil, fmt.Errorf("store: scan trading access: %w", err)
		}
		connection, err := domain.ExternalIDFromBytes(rawID)
		if err != nil {
			return nil, fmt.Errorf(
				"store: decode trading access connection external id: %w", err,
			)
		}
		access.Connection = connection
		accesses = append(accesses, access)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate trading access: %w", err)
	}
	return accesses, nil
}

func (r *realmStore) ListTradingAccessForConnection(
	ctx context.Context, connection domain.ExternalID,
) ([]domain.TradingAccess, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT a.code, ta.venue_account
		 FROM trading_access ta
		 JOIN account a ON a.id = ta.account_id
		 JOIN trading_connection c ON c.id = ta.connection_id
		 WHERE c.external_id = ?
		 ORDER BY a.code, ta.venue_account`, connection.Bytes(),
	)
	if err != nil {
		return nil, fmt.Errorf("store: list trading access for connection: %w", err)
	}
	defer func() { _ = rows.Close() }()
	accesses := make([]domain.TradingAccess, 0)
	for rows.Next() {
		access := domain.TradingAccess{Connection: connection}
		if err := rows.Scan(&access.Account, &access.VenueAccount); err != nil {
			return nil, fmt.Errorf("store: scan trading access for connection: %w", err)
		}
		accesses = append(accesses, access)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate trading access for connection: %w", err)
	}
	return accesses, nil
}

func (r *realmStore) EarliestVenueOrderTime(
	ctx context.Context, connection domain.ExternalID,
) (time.Time, bool, error) {
	db, err := r.db()
	if err != nil {
		return time.Time{}, false, err
	}
	var raw string
	err = db.QueryRowContext(ctx,
		`SELECT vo.created_at FROM venue_order vo
		 JOIN trading_connection c ON c.id = vo.connection_id
		 WHERE c.external_id = ? ORDER BY vo.created_at LIMIT 1`,
		connection.Bytes(),
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("store: earliest venue order time: %w", err)
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("store: parse earliest venue order time: %w", err)
	}
	return at, true, nil
}

func (r *realmStore) CreateVenueOrder(
	ctx context.Context, order domain.VenueOrder,
) (domain.VenueOrder, error) {
	if err := domain.ValidateVenueOrder(order); err != nil {
		return order, err
	}
	if order.SendAttempted || order.VenueOrderID != "" {
		return order, fmt.Errorf(
			"venue order must be unsent and unacknowledged at creation: %w",
			domain.ErrInvalid,
		)
	}
	db, err := r.db()
	if err != nil {
		return order, err
	}
	orderID, err := lookupOrderID(ctx, db, order.Order)
	if err != nil {
		return order, err
	}
	connectionID, err := resolveTradingConnectionID(ctx, db, order.Connection)
	if err != nil {
		return order, err
	}
	createdAt, createdAtText := nowStored()
	_, err = db.ExecContext(
		ctx,
		`INSERT INTO venue_order (
		   order_id, connection_id, venue_account, route,
		   client_order_id, created_at
		 ) VALUES (?, ?, ?, ?, ?, ?)`,
		orderID, connectionID, order.VenueAccount, order.Route,
		order.ClientOrderID, createdAtText,
	)
	if err != nil {
		if isSQLiteUnique(err) {
			return order, fmt.Errorf("venue order link: %w", domain.ErrAlreadyExists)
		}
		return order, fmt.Errorf("store: create venue order: %w", err)
	}
	order.CreatedAt = createdAt
	return order, nil
}

func (r *realmStore) SetVenueOrderID(
	ctx context.Context, order domain.ExternalID, venueOrderID string,
) error {
	if venueOrderID == "" || venueOrderID != strings.TrimSpace(venueOrderID) {
		return fmt.Errorf("venue order id: %w", domain.ErrInvalid)
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(
		ctx,
		`UPDATE venue_order SET venue_order_id = ?, send_attempted = 1
		 WHERE order_id = (
		   SELECT id FROM order_record WHERE external_id = ?
		 ) AND (venue_order_id IS NULL OR venue_order_id = ?)`,
		venueOrderID, order.Bytes(), venueOrderID,
	)
	if err != nil {
		return fmt.Errorf("store: set venue order id: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: count set venue order id: %w", err)
	}
	if rows > 0 {
		return nil
	}
	existing, ok, err := r.GetVenueOrder(ctx, order)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("venue order %q: %w", order.String(), domain.ErrNotFound)
	}
	if existing.VenueOrderID == venueOrderID {
		return nil
	}
	return fmt.Errorf("venue order id already set: %w", domain.ErrConflict)
}

func (r *realmStore) MarkVenueOrderSendAttempted(
	ctx context.Context, order domain.ExternalID,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(
		ctx,
		`UPDATE venue_order SET send_attempted = 1
		 WHERE order_id = (
		   SELECT id FROM order_record WHERE external_id = ?
		 )`,
		order.Bytes(),
	)
	if err != nil {
		return fmt.Errorf("store: mark venue order send attempted: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: count marked venue order send attempts: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("venue order %q: %w", order.String(), domain.ErrNotFound)
	}
	return nil
}

func (r *realmStore) DeleteVenueOrder(
	ctx context.Context, order domain.ExternalID,
) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(
		ctx,
		`DELETE FROM venue_order
		 WHERE order_id = (
		   SELECT id FROM order_record WHERE external_id = ?
		 ) AND send_attempted = 0 AND venue_order_id IS NULL`,
		order.Bytes(),
	)
	if err != nil {
		return fmt.Errorf("store: delete venue order: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: count deleted venue orders: %w", err)
	}
	if rows > 0 {
		return nil
	}
	_, ok, err := r.GetVenueOrder(ctx, order)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("venue order %q: %w", order.String(), domain.ErrNotFound)
	}
	return fmt.Errorf(
		"send-attempted or acknowledged venue order cannot be deleted: %w",
		domain.ErrConflict,
	)
}

func (r *realmStore) GetVenueOrder(
	ctx context.Context, order domain.ExternalID,
) (domain.VenueOrder, bool, error) {
	db, err := r.db()
	if err != nil {
		return domain.VenueOrder{}, false, err
	}
	venueOrder, err := scanVenueOrder(
		db.QueryRowContext(
			ctx, venueOrderSelect+` WHERE o.external_id = ?`, order.Bytes(),
		),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.VenueOrder{}, false, nil
	}
	if err != nil {
		return domain.VenueOrder{}, false,
			fmt.Errorf("store: get venue order: %w", err)
	}
	return venueOrder, true, nil
}

func (r *realmStore) FindVenueOrderByClientID(
	ctx context.Context,
	connection domain.ExternalID,
	clientOrderID string,
) (domain.VenueOrder, bool, error) {
	db, err := r.db()
	if err != nil {
		return domain.VenueOrder{}, false, err
	}
	venueOrder, err := scanVenueOrder(
		db.QueryRowContext(
			ctx,
			venueOrderSelect+`
			 WHERE c.external_id = ? AND vo.client_order_id = ?`,
			connection.Bytes(), clientOrderID,
		),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.VenueOrder{}, false, nil
	}
	if err != nil {
		return domain.VenueOrder{}, false,
			fmt.Errorf("store: find venue order by client id: %w", err)
	}
	return venueOrder, true, nil
}

func (r *realmStore) ListOpenVenueOrders(
	ctx context.Context, connection domain.ExternalID,
) ([]domain.VenueOrder, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	terminal, err := r.terminalOrderStatusIDs(ctx)
	if err != nil {
		return nil, err
	}
	placeholders := make([]string, len(terminal))
	args := make([]any, 0, len(terminal)+1)
	args = append(args, connection.Bytes())
	for i, statusID := range terminal {
		placeholders[i] = "?"
		args = append(args, statusID)
	}
	query := venueOrderSelect + `
	 WHERE c.external_id = ? AND o.status_id NOT IN (` +
		strings.Join(placeholders, ", ") + `)
	 ORDER BY vo.created_at, o.external_id`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list open venue orders: %w", err)
	}
	defer func() { _ = rows.Close() }()

	orders := make([]domain.VenueOrder, 0)
	for rows.Next() {
		order, err := scanVenueOrder(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan open venue order: %w", err)
		}
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate open venue orders: %w", err)
	}
	return orders, nil
}

func (r *realmStore) terminalOrderStatusIDs(
	ctx context.Context,
) ([]any, error) {
	dictionaries, err := r.dictionaries()
	if err != nil {
		return nil, err
	}
	statusIDs := make([]any, 0)
	for code, id := range dictionaries.codeToID[orderStatusTable] {
		if domain.OrderStatusTerminal(domain.OrderStatus(code)) {
			statusIDs = append(statusIDs, id)
		}
	}
	if len(statusIDs) == 0 {
		return nil, errors.New("store: terminal order status set is empty")
	}
	return statusIDs, nil
}

func scanVenueOrder(scanner sqlScanner) (domain.VenueOrder, error) {
	var (
		order         domain.VenueOrder
		rawOrder      []byte
		rawConnection []byte
		venueOrderID  sql.NullString
		createdAt     string
	)
	if err := scanner.Scan(
		&rawOrder, &rawConnection, &order.VenueAccount, &order.Route,
		&order.ClientOrderID, &order.SendAttempted, &venueOrderID, &createdAt,
	); err != nil {
		return domain.VenueOrder{}, err
	}
	orderID, err := domain.ExternalIDFromBytes(rawOrder)
	if err != nil {
		return domain.VenueOrder{},
			fmt.Errorf("store: decode venue order Officer id: %w", err)
	}
	connectionID, err := domain.ExternalIDFromBytes(rawConnection)
	if err != nil {
		return domain.VenueOrder{},
			fmt.Errorf("store: decode venue order connection id: %w", err)
	}
	parsedAt, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return domain.VenueOrder{},
			fmt.Errorf("store: parse venue order created_at: %w", err)
	}
	order.Order = orderID
	order.Connection = connectionID
	order.VenueOrderID = venueOrderID.String
	order.CreatedAt = parsedAt
	return order, nil
}
