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

// exportAssets lists every asset as a portable archive asset (engine id
// dropped).
func (r *realmStore) exportAssets(ctx context.Context) ([]backup.Asset, error) {
	assets, err := r.ListAssets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]backup.Asset, 0, len(assets))
	for _, asset := range assets {
		out = append(out, backup.Asset{
			Code: asset.Code, Title: asset.Title, AssetClass: asset.AssetClass,
		})
	}
	return out, nil
}

// exportBalances lists every balance as a portable archive snapshot, excluding
// the account-currency read projection that the target derives for itself.
func (r *realmStore) exportBalances(ctx context.Context) ([]backup.Balance, error) {
	balances, err := r.ListBalances(ctx, "", "")
	if err != nil {
		return nil, err
	}
	out := make([]backup.Balance, 0, len(balances))
	for _, balance := range balances {
		out = append(out, backup.Balance{
			UpdatedAt:             balance.UpdatedAt,
			Available:             balance.Available,
			Held:                  balance.Held,
			Incoming:              balance.Incoming,
			RealizedPnl:           balance.RealizedPnl,
			RealizedPnlHaltReason: balance.RealizedPnlHaltReason,
			AverageEntryPrice:     balance.AverageEntryPrice,
			Asset:                 balance.Asset,
			Account:               balance.Account,
		})
	}
	return out, nil
}

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
			BlockPolicy:   a.BlockPolicy,
			BlockCode:     a.BlockCode,
			BlockDetails:  a.BlockDetails,
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

func (r *realmStore) exportExecutionReports(
	ctx context.Context,
) ([]backup.ExecutionReportRecord, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT er.external_id, o.external_id, er.at
		FROM execution_report er
		JOIN order_record o ON o.id = er.order_id
		ORDER BY er.at ASC, er.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: export execution reports: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]backup.ExecutionReportRecord, 0)
	for rows.Next() {
		var reportRaw, orderRaw []byte
		var atRaw string
		if err := rows.Scan(&reportRaw, &orderRaw, &atRaw); err != nil {
			return nil, fmt.Errorf("store: scan execution report export: %w", err)
		}
		reportID, err := domain.ExternalIDFromBytes(reportRaw)
		if err != nil {
			return nil, fmt.Errorf("store: scan execution report id: %w", err)
		}
		orderID, err := domain.ExternalIDFromBytes(orderRaw)
		if err != nil {
			return nil, fmt.Errorf("store: scan execution report order id: %w", err)
		}
		at, err := time.Parse(time.RFC3339Nano, atRaw)
		if err != nil {
			return nil, fmt.Errorf("store: scan execution report time: %w", err)
		}
		out = append(out, backup.ExecutionReportRecord{
			ExternalID: reportID,
			Order:      orderID,
			At:         at.UTC(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate execution reports for export: %w", err)
	}
	return out, nil
}

func (r *realmStore) exportExecutionReportEvents(
	ctx context.Context,
) ([]backup.ExecutionReportEventLink, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT er.external_id, oe.external_id
		FROM execution_report_event ere
		JOIN execution_report er ON er.id = ere.report_id
		JOIN order_event oe ON oe.id = ere.event_id
		ORDER BY er.id ASC, oe.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: export execution report events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]backup.ExecutionReportEventLink, 0)
	for rows.Next() {
		var reportRaw, eventRaw []byte
		if err := rows.Scan(&reportRaw, &eventRaw); err != nil {
			return nil, fmt.Errorf("store: scan execution report event export: %w", err)
		}
		reportID, err := domain.ExternalIDFromBytes(reportRaw)
		if err != nil {
			return nil, fmt.Errorf("store: scan linked report id: %w", err)
		}
		eventID, err := domain.ExternalIDFromBytes(eventRaw)
		if err != nil {
			return nil, fmt.Errorf("store: scan linked event id: %w", err)
		}
		out = append(out, backup.ExecutionReportEventLink{
			Report: reportID,
			Event:  eventID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate execution report events for export: %w", err)
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

// exportCredentialForm reports the one credential form held by the snapshot.
// The explicit store state decides the form; credential bytes are never
// inspected or interpreted. Reporting the stored form directly is deliberate;
// do not replace this raw query with a sealing accessor.
func (r *realmStore) exportCredentialForm(ctx context.Context) (string, error) {
	db, err := r.db()
	if err != nil {
		return "", err
	}
	var sealed bool
	if err := db.QueryRowContext(
		ctx, `SELECT EXISTS(SELECT 1 FROM secret_state WHERE singleton = 1)`,
	).Scan(&sealed); err != nil {
		return "", fmt.Errorf("store: read credential form for export: %w", err)
	}
	if sealed {
		return backup.CredentialFormSealed, nil
	}
	return backup.CredentialFormPlaintext, nil
}

// exportMarketDataInstances reads every credential BLOB exactly as the snapshot
// stores it. The archive-level credential form declares how all rows are held.
// This verbatim raw read is deliberate; do not replace it with sealing accessors.
func (r *realmStore) exportMarketDataInstances(
	ctx context.Context,
) ([]backup.MarketDataInstance, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(
		ctx,
		`SELECT external_id, provider, label, credentials, enabled
		 FROM market_data_instance ORDER BY external_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: export market-data instances: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]backup.MarketDataInstance, 0)
	for rows.Next() {
		var (
			instance backup.MarketDataInstance
			rawID    []byte
		)
		if err := rows.Scan(
			&rawID, &instance.Provider, &instance.Label,
			&instance.Credentials, &instance.Enabled,
		); err != nil {
			return nil, fmt.Errorf("store: scan market-data instance for export: %w", err)
		}
		externalID, err := domain.ExternalIDFromBytes(rawID)
		if err != nil {
			return nil, fmt.Errorf("store: decode market-data instance external id for export: %w", err)
		}
		instance.ExternalID = externalID
		out = append(out, instance)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate market-data instances for export: %w", err)
	}
	return out, nil
}

// exportInstruments lists every instrument of every instance in its portable
// archive form, linked back to its instance by the instance external id.
func (r *realmStore) exportInstruments(
	ctx context.Context,
) ([]backup.MarketDataInstrument, error) {
	instances, err := r.ListMarketDataInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]backup.MarketDataInstrument, 0)
	for _, inst := range instances {
		instruments, err := r.ListMarketDataInstruments(ctx, inst.ExternalID)
		if err != nil {
			return nil, err
		}
		for _, instrument := range instruments {
			out = append(out, backup.MarketDataInstrument{
				Instance:       instrument.Instance,
				ExternalSymbol: instrument.ExternalSymbol,
				BaseAsset:      instrument.BaseAsset,
				QuoteAsset:     instrument.QuoteAsset,
				ManualPrice:    instrument.ManualPrice,
				Enabled:        instrument.Enabled,
			})
		}
	}
	return out, nil
}

// exportSigningKeys reads only public signing material. A signing private key is
// installation identity and never enters an archive.
func (r *realmStore) exportSigningKeys(ctx context.Context) ([]backup.SigningKey, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(
		ctx,
		`SELECT key_id, alg, public_key, created_at, active
		 FROM signing_key ORDER BY created_at ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: export signing keys: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]backup.SigningKey, 0)
	for rows.Next() {
		var (
			key       backup.SigningKey
			publicKey []byte
			createdAt string
		)
		if err := rows.Scan(
			&key.KeyID, &key.Alg, &publicKey, &createdAt, &key.Active,
		); err != nil {
			return nil, fmt.Errorf("store: scan signing key for export: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("store: parse signing key created_at %q: %w", createdAt, err)
		}
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
