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

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

func TestDeleteGroupDatabaseCascadePreservesAuditSnapshots(t *testing.T) {
	ctx := context.Background()
	_, realm := newTestStore(t)
	db := realm.(*realmStore).rawDB()

	assertAccountGroupDeleteCascade(t, ctx, db)
	for _, asset := range []string{"AAPL", "USD"} {
		if err := realm.CreateAsset(ctx, domain.Asset{Code: asset}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", asset, err)
		}
	}
	if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: "owned"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := realm.CreateAccount(ctx, domain.Account{
		Code: "member", Title: "Member Snapshot", GroupCode: "owned",
	}); err != nil {
		t.Fatalf("CreateAccount(member): %v", err)
	}
	if _, err := realm.CreateAccount(ctx, domain.Account{Code: "survivor"}); err != nil {
		t.Fatalf("CreateAccount(survivor): %v", err)
	}

	seedGroupOwnedOperationalRows(t, ctx, db)
	if err := realm.AppendAudit(ctx, fwstore.AuditEntry{
		Action:       domain.AuditActionCreateAccount,
		Account:      "member",
		AccountTitle: "Member Snapshot",
		Group:        "owned",
		Detail:       "member audit survives group deletion",
		Source:       domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}

	if err := realm.DeleteGroup(ctx, "owned"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}

	for _, table := range []string{
		"account_group", "balance", "limit_rate",
		"limit_order_size", "limit_spot_funds_pnl_bound", "adjustment",
		"order_record", "order_event", "event_attestation",
		"execution_report", "execution_report_event", "trade",
	} {
		assertTableCount(t, ctx, db, table, 0)
	}
	assertTableCount(t, ctx, db, "account", 1)
	assertTableCount(t, ctx, db, "asset", 2)
	assertTableCount(t, ctx, db, "audit", 1)

	if _, ok, err := realm.GetAccount(ctx, "survivor"); err != nil || !ok {
		t.Fatalf("surviving account: ok=%v err=%v", ok, err)
	}
	rows, err := realm.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 || rows[0].Account != "member" ||
		rows[0].AccountTitle != "Member Snapshot" || rows[0].Group != "owned" {
		t.Fatalf("surviving audit snapshot = %+v", rows)
	}
	var linkedAccount any
	if err := db.QueryRowContext(ctx, `SELECT account_id FROM audit`).Scan(&linkedAccount); err != nil {
		t.Fatalf("read audit account link: %v", err)
	}
	if linkedAccount != nil {
		t.Fatalf("audit account_id = %#v, want NULL", linkedAccount)
	}
}

func assertAccountGroupDeleteCascade(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_list(account)`)
	if err != nil {
		t.Fatalf("foreign_key_list(account): %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan account foreign key: %v", err)
		}
		if table == "account_group" && from == "group_id" && onDelete == "CASCADE" {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate account foreign keys: %v", err)
	}
	t.Fatal("account.group_id has no ON DELETE CASCADE foreign key")
}

func seedGroupOwnedOperationalRows(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	exec := func(query string, args ...any) sql.Result {
		t.Helper()
		result, err := db.ExecContext(ctx, query, args...)
		if err != nil {
			t.Fatalf("seed operational row: %v\nquery: %s", err, query)
		}
		return result
	}

	accountID := queryRowID(t, ctx, db, `SELECT id FROM account WHERE code = 'member'`)
	groupID := queryRowID(t, ctx, db, `SELECT id FROM account_group WHERE code = 'owned'`)
	baseID := queryRowID(t, ctx, db, `SELECT id FROM asset WHERE code = 'AAPL'`)
	quoteID := queryRowID(t, ctx, db, `SELECT id FROM asset WHERE code = 'USD'`)
	exec(`INSERT INTO balance
		(account_id, asset_id, available, updated_at) VALUES (?, ?, '1', '2026-08-01T00:00:00Z')`,
		accountID, baseID)
	exec(`INSERT INTO limit_rate
		(scope, account_id, max_orders, window) VALUES ('account', ?, 1, '1s')`, accountID)
	exec(`INSERT INTO limit_order_size
		(scope, account_id, max_quantity) VALUES ('account', ?, '1')`, accountID)
	exec(`INSERT INTO limit_spot_funds_pnl_bound
		(scope, account_id, lower_bound) VALUES ('account', ?, '-1')`, accountID)
	exec(`INSERT INTO limit_spot_funds_pnl_bound
		(scope, account_group_id, lower_bound) VALUES ('account_group', ?, '-2')`, groupID)
	exec(`INSERT INTO adjustment
		(external_id, account_id, asset_id, at, source_id, status_id, request, outcome)
		VALUES (?, ?, ?, '2026-08-01T00:00:00Z',
		 (SELECT id FROM source_kind WHERE code = 'panel'),
		 (SELECT id FROM adjustment_status WHERE code = 'accepted'), '{}', '{}')`,
		externalID(1), accountID, baseID)
	orderResult := exec(`INSERT INTO order_record
		(external_id, account_id, base_asset_id, quote_asset_id, at, source_id,
		 side_id, amount_kind_id, amount_value, status_id)
		VALUES (?, ?, ?, ?, '2026-08-01T00:00:00Z',
		 (SELECT id FROM source_kind WHERE code = 'panel'),
		 (SELECT id FROM order_side WHERE code = 'buy'),
		 (SELECT id FROM order_amount_kind WHERE code = 'quantity'), '1',
		 (SELECT id FROM order_status WHERE code = 'submitted'))`,
		externalID(2), accountID, baseID, quoteID)
	orderID, err := orderResult.LastInsertId()
	if err != nil {
		t.Fatalf("order id: %v", err)
	}
	eventResult := exec(`INSERT INTO order_event
		(external_id, order_id, at, type_id, source_id, payload)
		VALUES (?, ?, '2026-08-01T00:00:00Z',
		 (SELECT id FROM order_event_type WHERE code = 'submitted'),
		 (SELECT id FROM source_kind WHERE code = 'panel'), '{}')`, externalID(3), orderID)
	eventID, err := eventResult.LastInsertId()
	if err != nil {
		t.Fatalf("event id: %v", err)
	}
	exec(`INSERT INTO event_attestation
		(event_id, token, alg_id, request_type_id, mode_id, issued_at)
		VALUES (?, 'unsigned',
		 (SELECT id FROM attestation_alg WHERE code = 'none'),
		 (SELECT id FROM attestation_request_type WHERE code = 'submit'),
		 (SELECT id FROM attestation_mode WHERE code = 'hold'),
		 '2026-08-01T00:00:00Z')`, eventID)
	reportResult := exec(`INSERT INTO execution_report
		(external_id, order_id, at) VALUES (?, ?, '2026-08-01T00:00:00Z')`,
		externalID(4), orderID)
	reportID, err := reportResult.LastInsertId()
	if err != nil {
		t.Fatalf("execution report id: %v", err)
	}
	exec(`INSERT INTO execution_report_event (report_id, event_id) VALUES (?, ?)`, reportID, eventID)
	exec(`INSERT INTO trade
		(external_id, order_id, account_id, base_asset_id, quote_asset_id, at,
		 source_id, side_id, quantity, price)
		VALUES (?, ?, ?, ?, ?, '2026-08-01T00:00:00Z',
		 (SELECT id FROM source_kind WHERE code = 'panel'),
		 (SELECT id FROM order_side WHERE code = 'buy'), '1', '10')`,
		externalID(5), orderID, accountID, baseID, quoteID)
}

func queryRowID(t *testing.T, ctx context.Context, db *sql.DB, query string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(ctx, query).Scan(&id); err != nil {
		t.Fatalf("query row id: %v", err)
	}
	return id
}

func externalID(seed byte) []byte {
	id := make([]byte, 16)
	id[len(id)-1] = seed
	return id
}

func assertTableCount(t *testing.T, ctx context.Context, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
	if err := db.QueryRowContext(ctx, query).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s rows = %d, want %d", table, got, want)
	}
}
