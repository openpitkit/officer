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
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

func requireStoredIDFault(t *testing.T, err error, operation string) {
	t.Helper()
	if err == nil || errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("%s error = %v, want an unclassified storage fault", operation, err)
	}
	for _, text := range []string{operation, "external id must not be empty: invalid"} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("error = %q, want context %q", err, text)
		}
	}
}

func corruptStoredID(t *testing.T, ctx context.Context, rs fwstore.RealmStore, query string) {
	t.Helper()
	result, err := rs.(*realmStore).rawDB().ExecContext(ctx, query)
	if err != nil {
		t.Fatalf("plant corrupt stored id: %v", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("corrupt stored id affected %d rows, error %v; want one row", n, err)
	}
}

func TestStoredExternalIDFaults(t *testing.T) {
	for _, tc := range []struct {
		name      string
		query     string
		operation string
		read      func(context.Context, fwstore.RealmStore, domain.ExternalID) error
	}{
		{
			name: "adjustment", query: `UPDATE adjustment SET external_id = X''`,
			operation: "decode adjustment external id",
			read: func(ctx context.Context, rs fwstore.RealmStore, _ domain.ExternalID) error {
				_, err := rs.ListAdjustments(ctx, "acc-1", "", 10)
				return err
			},
		},
		{
			name: "audit", query: `UPDATE audit SET external_id = X''`,
			operation: "decode audit external id",
			read: func(ctx context.Context, rs fwstore.RealmStore, _ domain.ExternalID) error {
				_, err := rs.ListAudit(ctx, 10)
				return err
			},
		},
		{
			name: "order", query: `UPDATE order_record SET external_id = X''`,
			operation: "decode order external id",
			read: func(ctx context.Context, rs fwstore.RealmStore, _ domain.ExternalID) error {
				_, err := rs.ListOrderRows(ctx, fwstore.OrderListFilter{})
				return err
			},
		},
		{
			name: "order_event", query: `UPDATE order_event SET external_id = X''`,
			operation: "decode event external id",
			read: func(ctx context.Context, rs fwstore.RealmStore, order domain.ExternalID) error {
				_, err := rs.ListOrderEvents(ctx, order)
				return err
			},
		},
		{
			name: "trade", query: `UPDATE trade SET external_id = X''`,
			operation: "decode trade external id",
			read: func(ctx context.Context, rs fwstore.RealmStore, _ domain.ExternalID) error {
				_, err := rs.ListTrades(ctx, "acc-1", "", 10)
				return err
			},
		},
		{
			name: "trade_order", query: `UPDATE order_record SET external_id = X''`,
			operation: "decode trade order external id",
			read: func(ctx context.Context, rs fwstore.RealmStore, _ domain.ExternalID) error {
				_, err := rs.ListTrades(ctx, "acc-1", "", 10)
				return err
			},
		},
		{
			name: "market_data_instance", query: `UPDATE market_data_instance SET external_id = X''`,
			operation: "decode instance external id",
			read: func(ctx context.Context, rs fwstore.RealmStore, _ domain.ExternalID) error {
				_, err := rs.ListMarketDataInstances(ctx)
				return err
			},
		},
		{
			name: "market_data_instrument", query: `UPDATE market_data_instance SET external_id = X''`,
			operation: "decode instrument instance external id",
			read: func(ctx context.Context, rs fwstore.RealmStore, _ domain.ExternalID) error {
				_, err := rs.ListMarketDataInstruments(ctx, "")
				return err
			},
		},
		{
			name: "trading_connection", query: `UPDATE trading_connection SET external_id = X''`,
			operation: "decode trading connection external id",
			read: func(ctx context.Context, rs fwstore.RealmStore, _ domain.ExternalID) error {
				_, err := rs.ListTradingConnections(ctx)
				return err
			},
		},
		{
			name: "trading_instrument", query: `UPDATE trading_connection SET external_id = X''`,
			operation: "decode trading instrument connection external id",
			read: func(ctx context.Context, rs fwstore.RealmStore, _ domain.ExternalID) error {
				_, err := rs.ListTradingInstruments(ctx, "")
				return err
			},
		},
		{
			name: "trading_access", query: `UPDATE trading_connection SET external_id = X''`,
			operation: "decode trading access connection external id",
			read: func(ctx context.Context, rs fwstore.RealmStore, _ domain.ExternalID) error {
				_, err := rs.ListTradingAccess(ctx, "acc-1")
				return err
			},
		},
		{
			name: "venue_order", query: `UPDATE trading_connection SET external_id = X''`,
			operation: "decode venue order connection id",
			read: func(ctx context.Context, rs fwstore.RealmStore, order domain.ExternalID) error {
				_, _, err := rs.GetVenueOrder(ctx, order)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			_, rs := newTestStore(t)
			_, order := seedTradingBackupState(t, ctx, rs)
			corruptStoredID(t, ctx, rs, tc.query)
			requireStoredIDFault(t, tc.read(ctx, rs, order), tc.operation)
		})
	}
}

func TestBackupStoredExternalIDFaults(t *testing.T) {
	for _, tc := range []struct {
		name      string
		query     string
		operation string
		prune     bool
	}{
		{"export_report", `UPDATE execution_report SET external_id = X''`, "scan execution report id", false},
		{"export_trading", `UPDATE trading_connection SET external_id = X''`, "decode trading connection external id for export", false},
		{"export_market_data", `UPDATE market_data_instance SET external_id = X''`, "decode market-data instance external id for export", false},
		{"prune_report_link", `UPDATE execution_report SET external_id = X''`, "prune linked execution report id", true},
		{"prune_event_link", `UPDATE order_event SET external_id = X''`, "prune linked execution event id", true},
		{"prune_adjustment", `UPDATE adjustment SET external_id = X''`, "prune adjustment external id", true},
		{"prune_audit", `UPDATE audit SET external_id = X''`, "prune audit external id", true},
		{"prune_market_data", `UPDATE market_data_instance SET external_id = X''`, "prune md instrument external id", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			_, rs := newTestStore(t)
			seedTradingBackupState(t, ctx, rs)
			db := rs.(*realmStore).rawDB()
			if _, err := db.ExecContext(ctx, `
				INSERT INTO execution_report (external_id, order_id, at)
				SELECT X'01', id, at FROM order_record;
				INSERT INTO execution_report_event (report_id, event_id)
				SELECT er.id, oe.id FROM execution_report er
				JOIN order_event oe ON oe.order_id = er.order_id`); err != nil {
				t.Fatalf("seed execution report and link: %v", err)
			}
			archive, err := rs.ExportBackup(ctx, backup.Scope{All: true})
			if err != nil {
				t.Fatalf("ExportBackup before corruption: %v", err)
			}
			corruptStoredID(t, ctx, rs, tc.query)
			if tc.prune {
				_, err = rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
					Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
				})
			} else {
				_, err = rs.ExportBackup(ctx, backup.Scope{All: true})
			}
			requireStoredIDFault(t, err, tc.operation)
		})
	}
}

func TestRestoreStoredTradingIDFault(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	seedTradingBackupState(t, ctx, rs)
	scope := backup.Scope{Sections: []backup.Section{backup.SectionTrading}}
	archive, err := rs.ExportBackup(ctx, scope)
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	archive.Data.TradingConnections = nil
	archive.Data.TradingInstruments = nil
	archive.Data.TradingAccess = nil
	archive.Data.VenueOrders = nil
	corruptStoredID(t, ctx, rs,
		`UPDATE trading_connection SET external_id = X'', fee_account_id = NULL`)
	_, err = rs.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeInsertMissing,
	})
	requireStoredIDFault(t, err, "decode restored trading connection external id")
}

func TestSealingStoredExternalIDFaults(t *testing.T) {
	for _, table := range []string{"market_data_instance", "trading_connection"} {
		t.Run(table, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "officer.db")
			plain := openSQLiteStoreForTest(t, path)
			if err := plain.Migrate(ctx); err != nil {
				t.Fatalf("Migrate plaintext store: %v", err)
			}
			rs, err := plain.ForRealm(ctx, domain.DefaultRealm)
			if err != nil {
				t.Fatalf("ForRealm: %v", err)
			}
			seedTradingBackupState(t, ctx, rs)
			corruptStoredID(t, ctx, rs, `UPDATE `+table+` SET external_id = X''`)
			if err := plain.Close(); err != nil {
				t.Fatalf("close plaintext store: %v", err)
			}
			sealed := openSQLiteStoreForTest(t, path, WithMasterKey(mustStoreMasterKey(t, 0x73)))
			requireStoredIDFault(t, sealed.Migrate(ctx), "external id for sealing")
		})
	}
}
