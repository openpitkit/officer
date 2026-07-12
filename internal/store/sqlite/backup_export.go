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

// Backup group of the SQLite store: the export-side per-section helpers used by
// ExportBackup to gather one section of portable rows.

package sqlite

import (
	"context"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
)

// exportGroups lists every group as a portable archive group (engine id dropped).
func (r *realmStore) exportGroups(ctx context.Context) ([]backup.AccountGroup, error) {
	groups, err := r.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]backup.AccountGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, backup.AccountGroup{
			Code:        g.Code,
			Title:       g.Title,
			Currency:    g.Currency,
			Notes:       g.Notes,
			BlockReason: g.BlockReason,
			Blocked:     g.Blocked,
		})
	}
	return out, nil
}

// exportAccounts lists every account as a portable archive account (engine id
// dropped, group link kept by code).
func (r *realmStore) exportAccounts(ctx context.Context) ([]backup.Account, error) {
	accounts, err := r.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]backup.Account, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, backup.Account{
			Code:          a.Code.String(),
			Title:         a.Title,
			Pnl:           a.Pnl,
			PnlHaltReason: a.PnlHaltReason,
			Currency:      a.Currency,
			GroupCode:     a.GroupCode,
			Notes:         a.Notes,
			BlockReason:   a.BlockReason,
			Blocked:       a.Blocked,
		})
	}
	return out, nil
}

// exportAdjustments scans the whole adjustment table, oldest first, so a backup
// captures the full history rather than a UI page. It uses the adjustment-group
// projection and scan helper directly to avoid a per-account, limit-bounded read.
func (r *realmStore) exportAdjustments(
	ctx context.Context,
) ([]domain.AccountAdjustmentRecord, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(
		ctx, adjustmentSelect+` ORDER BY adj.at ASC, adj.id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: export adjustments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	dictionaries, err := r.dictionaries()
	if err != nil {
		return nil, err
	}

	out := make([]domain.AccountAdjustmentRecord, 0)
	for rows.Next() {
		rec, err := scanAdjustment(dictionaries, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate adjustments for export: %w", err)
	}
	return out, nil
}

// exportOrders lists every order across all account (no UI limit). Events (each
// carrying its optional 1:1 attestation) and trade travel in their own sections,
// linked back by the order's external id.
func (r *realmStore) exportOrders(ctx context.Context) ([]backup.OrderRecord, error) {
	orders, err := r.ListAllOrders(ctx, "", "")
	if err != nil {
		return nil, err
	}
	out := make([]backup.OrderRecord, 0, len(orders))
	for _, o := range orders {
		out = append(out, backup.OrderRecord{Order: o})
	}
	return out, nil
}

// exportOrderEvents lists every order's events (each carrying its optional 1:1
// attestation), oldest first within each order, linked to the parent by the
// order's external id.
func (r *realmStore) exportOrderEvents(ctx context.Context) ([]domain.OrderEvent, error) {
	orders, err := r.ListAllOrders(ctx, "", "")
	if err != nil {
		return nil, err
	}
	out := make([]domain.OrderEvent, 0)
	for _, o := range orders {
		events, err := r.ListOrderEvents(ctx, o.ExternalID)
		if err != nil {
			return nil, err
		}
		out = append(out, events...)
	}
	return out, nil
}

// exportAudit scans the whole audit trail, oldest first, so the archive restores
// in the order rows were recorded. It uses the audit-group projection and scan
// helper directly to avoid a limit-bounded, newest-first UI read.
func (r *realmStore) exportAudit(ctx context.Context) ([]domain.AuditRow, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(
		ctx, auditSelect+` ORDER BY au.at ASC, au.id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: export audit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	dictionaries, err := r.dictionaries()
	if err != nil {
		return nil, err
	}

	out := make([]domain.AuditRow, 0)
	for rows.Next() {
		row, err := scanAuditRow(dictionaries, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate audit for export: %w", err)
	}
	return out, nil
}

// exportInstruments lists every instrument of every instance, linked back to its
// instance by the instance external id.
func (r *realmStore) exportInstruments(
	ctx context.Context,
) ([]domain.MarketDataInstrument, error) {
	instances, err := r.ListMarketDataInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.MarketDataInstrument, 0)
	for _, inst := range instances {
		instruments, err := r.ListMarketDataInstruments(ctx, inst.ExternalID)
		if err != nil {
			return nil, err
		}
		out = append(out, instruments...)
	}
	return out, nil
}

// exportSigningKeys reads every signing key WITH its private material so the keys
// round-trip; the listing API omits private keys, so this is a dedicated read.
func (r *realmStore) exportSigningKeys(ctx context.Context) ([]backup.SigningKey, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(
		ctx,
		`SELECT key_id, alg, private_key, public_key, created_at, active
		 FROM signing_key ORDER BY created_at ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: export signing keys: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]backup.SigningKey, 0)
	for rows.Next() {
		var (
			key                   backup.SigningKey
			privateKey, publicKey []byte
			createdAt             string
		)
		if err := rows.Scan(
			&key.KeyID, &key.Alg, &privateKey, &publicKey, &createdAt, &key.Active,
		); err != nil {
			return nil, fmt.Errorf("store: scan signing key for export: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("store: parse signing key created_at %q: %w", createdAt, err)
		}
		key.PrivateKey = privateKey
		key.PublicKey = publicKey
		key.CreatedAt = parsed
		out = append(out, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate signing keys for export: %w", err)
	}
	return out, nil
}

// exportSigningConfig reads the whole signing-config table as portable entries.
func (r *realmStore) exportSigningConfig(
	ctx context.Context,
) ([]backup.SigningConfigEntry, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(
		ctx, `SELECT key, value FROM signing_config ORDER BY key`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: export signing config: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]backup.SigningConfigEntry, 0)
	for rows.Next() {
		var entry backup.SigningConfigEntry
		if err := rows.Scan(&entry.Key, &entry.Value); err != nil {
			return nil, fmt.Errorf("store: scan signing config: %w", err)
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate signing config: %w", err)
	}
	return out, nil
}
