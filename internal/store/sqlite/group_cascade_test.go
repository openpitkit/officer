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
	"errors"
	"fmt"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

func TestDeleteGroupDetachesMembersAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	_, realm := newTestStore(t)
	db := realm.(*realmStore).rawDB()

	assertAccountGroupDeleteSetsNull(t, ctx, db)
	for _, asset := range []string{"AAPL", "USD"} {
		if _, err := realm.CreateAsset(ctx, domain.Asset{Code: asset}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", asset, err)
		}
	}
	if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: "owned"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: "retained"}); err != nil {
		t.Fatalf("CreateGroup(retained): %v", err)
	}
	if _, err := realm.CreateAccount(ctx, domain.Account{
		Code: "member", Title: "Member Snapshot", GroupCode: "owned",
	}); err != nil {
		t.Fatalf("CreateAccount(member): %v", err)
	}
	if _, err := realm.CreateAccount(ctx, domain.Account{
		Code: "outside", Title: "Outside Snapshot", GroupCode: "retained",
	}); err != nil {
		t.Fatalf("CreateAccount(outside): %v", err)
	}

	seedAccountOperationalRows(t, ctx, db, "member", "owned", 1)
	seedAccountOperationalRows(t, ctx, db, "outside", "retained", 16)
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
	if err := realm.AppendAudit(ctx, fwstore.AuditEntry{
		Action:       domain.AuditActionCreateAccount,
		Account:      "outside",
		AccountTitle: "Outside Snapshot",
		Group:        "retained",
		Detail:       "outside audit survives group deletion",
		Source:       domain.SourcePanel,
	}); err != nil {
		t.Fatalf("AppendAudit(outside): %v", err)
	}

	if err := realm.DeleteGroup(ctx, "owned", true); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}

	assertTableCount(t, ctx, db, "account_group", 1)
	for _, table := range []string{
		"balance", "limit_rate", "limit_order_size", "adjustment",
		"order_record", "order_event", "event_attestation", "execution_report",
		"execution_report_event", "trade",
	} {
		assertTableCount(t, ctx, db, table, 2)
	}
	assertTableCount(t, ctx, db, "limit_spot_funds_pnl_bound", 3)
	assertTableCount(t, ctx, db, "account", 2)
	assertTableCount(t, ctx, db, "asset", 2)
	assertTableCount(t, ctx, db, "audit", 2)

	if _, ok, err := realm.GetGroup(ctx, "retained"); err != nil || !ok {
		t.Fatalf("retained group: ok=%v err=%v", ok, err)
	}
	member, ok, err := realm.GetAccount(ctx, "member")
	if err != nil || !ok || member.GroupCode != "" {
		t.Fatalf("detached member = %+v, ok=%v err=%v", member, ok, err)
	}
	assertAccountOperationalRows(t, ctx, db, "member", "")
	outside, ok, err := realm.GetAccount(ctx, "outside")
	if err != nil || !ok || outside.GroupCode != "retained" {
		t.Fatalf("outside account = %+v, ok=%v err=%v", outside, ok, err)
	}
	assertAccountOperationalRows(t, ctx, db, "outside", "retained")
	rows, err := realm.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("surviving audit rows = %+v", rows)
	}
	assertAuditSnapshot(t, rows, "member", "Member Snapshot", "owned")
	assertAuditSnapshot(t, rows, "outside", "Outside Snapshot", "retained")
}

func TestDeleteGroupRequiresForceForGroupScopedPnlBound(t *testing.T) {
	ctx := context.Background()
	_, realm := newTestStore(t)
	db := realm.(*realmStore).rawDB()

	if _, err := realm.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset(USD): %v", err)
	}
	if _, err := realm.CreateGroup(ctx, domain.AccountGroup{Code: "owned"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	groupID := queryRowID(
		t, ctx, db, `SELECT id FROM account_group WHERE code = 'owned'`,
	)
	currencyAssetID := queryRowID(t, ctx, db, `SELECT id FROM asset WHERE code = 'USD'`)
	if _, err := db.ExecContext(ctx, `INSERT INTO limit_spot_funds_pnl_bound
		(scope, account_group_id, currency_asset_id, lower_bound)
		VALUES ('account_group', ?, ?, '-2')`, groupID, currencyAssetID); err != nil {
		t.Fatalf("seed group P&L bound: %v", err)
	}

	err := realm.DeleteGroup(ctx, "owned", false)
	if !errors.Is(err, domain.ErrHasDependents) {
		t.Fatalf("DeleteGroup(without force) error = %v, want ErrHasDependents", err)
	}
	var dependentErr domain.HasDependentsError
	if !errors.As(err, &dependentErr) || len(dependentErr.Dependents) != 1 ||
		dependentErr.Dependents[0] != (domain.DependentCount{
			Kind: "limit_spot_funds_pnl_bound", Count: 1,
		}) {
		t.Fatalf("DeleteGroup dependents = %+v", dependentErr.Dependents)
	}
	if _, ok, getErr := realm.GetGroup(ctx, "owned"); getErr != nil || !ok {
		t.Fatalf("group after refusal: ok=%v err=%v", ok, getErr)
	}
	assertTableCount(t, ctx, db, "limit_spot_funds_pnl_bound", 1)
}

func assertAccountGroupDeleteSetsNull(t *testing.T, ctx context.Context, db *sql.DB) {
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
		if table == "account_group" && from == "group_id" && onDelete == "SET NULL" {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate account foreign keys: %v", err)
	}
	t.Fatal("account.group_id has no ON DELETE SET NULL foreign key")
}

func seedAccountOperationalRows(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	accountCode, groupCode string,
	seed byte,
) {
	t.Helper()
	exec := func(query string, args ...any) sql.Result {
		t.Helper()
		result, err := db.ExecContext(ctx, query, args...)
		if err != nil {
			t.Fatalf("seed operational row: %v\nquery: %s", err, query)
		}
		return result
	}

	accountID := queryRowID(
		t, ctx, db, `SELECT id FROM account WHERE code = ?`, accountCode,
	)
	groupID := queryRowID(
		t, ctx, db, `SELECT id FROM account_group WHERE code = ?`, groupCode,
	)
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
		(scope, account_id, currency_asset_id, lower_bound)
		VALUES ('account', ?, ?, '-1')`, accountID, quoteID)
	exec(`INSERT INTO limit_spot_funds_pnl_bound
		(scope, account_group_id, currency_asset_id, lower_bound)
		VALUES ('account_group', ?, ?, '-2')`, groupID, quoteID)
	exec(`INSERT INTO adjustment
		(external_id, account_id, asset_id, at, source_id, status_id, request, outcome)
		VALUES (?, ?, ?, '2026-08-01T00:00:00Z',
		 (SELECT id FROM source_kind WHERE code = 'panel'),
		 (SELECT id FROM adjustment_status WHERE code = 'accepted'), '{}', '{}')`,
		externalID(seed), accountID, baseID)
	orderResult := exec(`INSERT INTO order_record
		(external_id, account_id, base_asset_id, quote_asset_id, at, source_id,
		 side_id, amount_kind_id, amount_value, status_id)
		VALUES (?, ?, ?, ?, '2026-08-01T00:00:00Z',
		 (SELECT id FROM source_kind WHERE code = 'panel'),
		 (SELECT id FROM order_side WHERE code = 'buy'),
		 (SELECT id FROM order_amount_kind WHERE code = 'quantity'), '1',
		 (SELECT id FROM order_status WHERE code = 'submitted'))`,
		externalID(seed+1), accountID, baseID, quoteID)
	orderID, err := orderResult.LastInsertId()
	if err != nil {
		t.Fatalf("order id: %v", err)
	}
	eventResult := exec(`INSERT INTO order_event
		(external_id, order_id, at, type_id, source_id, payload)
		VALUES (?, ?, '2026-08-01T00:00:00Z',
		 (SELECT id FROM order_event_type WHERE code = 'submitted'),
		 (SELECT id FROM source_kind WHERE code = 'panel'), '{}')`, externalID(seed+2), orderID)
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
		externalID(seed+3), orderID)
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
		externalID(seed+4), orderID, accountID, baseID, quoteID)
}

func assertAccountOperationalRows(
	t *testing.T, ctx context.Context, db *sql.DB, accountCode, groupCode string,
) {
	t.Helper()
	accountID := queryRowID(
		t, ctx, db, `SELECT id FROM account WHERE code = ?`, accountCode,
	)
	checks := []struct {
		query string
		args  []any
	}{
		{`SELECT COUNT(*) FROM balance WHERE account_id = ?`, []any{accountID}},
		{`SELECT COUNT(*) FROM limit_rate WHERE account_id = ?`, []any{accountID}},
		{`SELECT COUNT(*) FROM limit_order_size WHERE account_id = ?`, []any{accountID}},
		{`SELECT COUNT(*) FROM limit_spot_funds_pnl_bound WHERE account_id = ?`, []any{accountID}},
		{`SELECT COUNT(*) FROM adjustment WHERE account_id = ?`, []any{accountID}},
		{`SELECT COUNT(*) FROM order_record WHERE account_id = ?`, []any{accountID}},
		{`SELECT COUNT(*) FROM order_event ev JOIN order_record o ON o.id = ev.order_id WHERE o.account_id = ?`, []any{accountID}},
		{`SELECT COUNT(*) FROM event_attestation ea
			JOIN order_event ev ON ev.id = ea.event_id
			JOIN order_record o ON o.id = ev.order_id WHERE o.account_id = ?`, []any{accountID}},
		{`SELECT COUNT(*) FROM execution_report er JOIN order_record o ON o.id = er.order_id WHERE o.account_id = ?`, []any{accountID}},
		{`SELECT COUNT(*) FROM execution_report_event ere
			JOIN execution_report er ON er.id = ere.report_id
			JOIN order_record o ON o.id = er.order_id WHERE o.account_id = ?`, []any{accountID}},
		{`SELECT COUNT(*) FROM trade WHERE account_id = ?`, []any{accountID}},
	}
	if groupCode != "" {
		groupID := queryRowID(
			t, ctx, db, `SELECT id FROM account_group WHERE code = ?`, groupCode,
		)
		checks = append(checks, struct {
			query string
			args  []any
		}{
			`SELECT COUNT(*) FROM limit_spot_funds_pnl_bound
			 WHERE account_group_id = ?`, []any{groupID},
		})
	}
	for _, check := range checks {
		assertQueryCount(t, ctx, db, check.query, 1, check.args...)
	}
}

func assertAuditSnapshot(
	t *testing.T,
	rows []domain.AuditRow,
	account domain.AccountID,
	title, group string,
) {
	t.Helper()
	for _, row := range rows {
		if row.Account == account && row.AccountTitle == title && row.Group == group {
			return
		}
	}
	t.Fatalf("audit snapshot for %q = %+v", account, rows)
}

func queryRowID(
	t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any,
) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
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
	assertQueryCount(t, ctx, db, fmt.Sprintf("SELECT COUNT(*) FROM %s", table), want)
}

func assertQueryCount(
	t *testing.T, ctx context.Context, db *sql.DB, query string, want int, args ...any,
) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&got); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	if got != want {
		t.Fatalf("query %q rows = %d, want %d", query, got, want)
	}
}
