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
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.openpit.dev/officer/framework/alerts"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

var _ alerts.Store = (*realmStore)(nil)

const resolvedAlertAudit = `SELECT 1 FROM audit au JOIN audit_action aa ON aa.id=au.action_id
 WHERE aa.code='resolve_alert' AND json_extract(au.detail_params,'$.alert')=?`

func (r *realmStore) exportAlerts(ctx context.Context) ([]backup.ActionableAlert, error) {
	rows, err := r.ListAlerts(ctx, "")
	if err != nil {
		return nil, err
	}
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	out := make([]backup.ActionableAlert, 0, len(rows))
	for _, a := range rows {
		entry := backup.ActionableAlert{ID: a.ID, Type: a.Type, At: a.At, Source: a.Source, Payload: a.Payload, ReadStates: map[string]bool{}, Deliveries: map[string]bool{}}
		for _, d := range a.Deliveries {
			entry.Deliveries[d.Channel] = d.Failed
		}
		marks, err := db.QueryContext(ctx, `SELECT principal,is_read FROM alert_read_state r JOIN actionable_alert a ON a.id=r.alert_id WHERE a.external_id=?`, a.ID)
		if err != nil {
			return nil, err
		}
		for marks.Next() {
			var principal string
			var read bool
			if err := marks.Scan(&principal, &read); err != nil {
				return nil, errors.Join(err, marks.Close())
			}
			entry.ReadStates[principal] = read
		}
		if err := marks.Err(); err != nil {
			return nil, errors.Join(err, marks.Close())
		}
		if err := marks.Close(); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

func (rt *restoreTx) restoreAlerts(ctx context.Context, entries []backup.ActionableAlert) error {
	seen := make(map[string]bool, len(entries))
	for _, a := range entries {
		if err := domain.ValidateExternalID(a.ID); err != nil {
			return err
		}
		if seen[a.ID] || strings.TrimSpace(a.Type) == "" || a.At.IsZero() || !json.Valid(a.Payload) {
			return fmt.Errorf("store: invalid archived alert: %w", domain.ErrInvalid)
		}
		seen[a.ID] = true
		if err := domain.ValidateSource(a.Source); err != nil {
			return err
		}
		exists, err := rowExists(ctx, rt.tx, `SELECT 1 FROM actionable_alert WHERE external_id=?`, a.ID)
		if err != nil {
			return err
		}
		if rt.skipMachine(backup.SectionActivityHistory, exists) {
			continue
		}
		if _, err := rt.tx.ExecContext(ctx, `INSERT INTO actionable_alert(external_id,type,at,source,payload) VALUES(?,?,?,?,?)
 ON CONFLICT(external_id) DO UPDATE SET type=excluded.type,at=excluded.at,source=excluded.source,payload=excluded.payload`, a.ID, a.Type, timeStr(a.At), a.Source, string(a.Payload)); err != nil {
			return err
		}
		for _, table := range []string{"alert_read_state", "alert_delivery"} {
			if _, err := rt.tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE alert_id=(SELECT id FROM actionable_alert WHERE external_id=?)`, a.ID); err != nil {
				return err
			}
		}
		for principal, read := range a.ReadStates {
			if strings.TrimSpace(principal) == "" {
				return fmt.Errorf("store: archived alert has no principal: %w", domain.ErrInvalid)
			}
			if _, err := rt.tx.ExecContext(ctx, `INSERT INTO alert_read_state(alert_id,principal,is_read) SELECT id,?,? FROM actionable_alert WHERE external_id=?`, principal, read, a.ID); err != nil {
				return err
			}
		}
		for channel, failed := range a.Deliveries {
			if strings.TrimSpace(channel) == "" {
				return fmt.Errorf("store: archived alert has no channel: %w", domain.ErrInvalid)
			}
			if _, err := rt.tx.ExecContext(ctx, `INSERT INTO alert_delivery(alert_id,channel,failed) SELECT id,?,? FROM actionable_alert WHERE external_id=?`, channel, failed, a.ID); err != nil {
				return err
			}
		}
		rt.summary.AddApplied(backup.SectionActivityHistory, 1)
	}
	return nil
}

func (rt *restoreTx) pruneAlerts(ctx context.Context, entries []backup.ActionableAlert) error {
	keep := make(map[string]bool, len(entries))
	for _, a := range entries {
		keep[a.ID] = true
	}
	rows, err := rt.tx.QueryContext(ctx, `SELECT external_id FROM actionable_alert`)
	if err != nil {
		return err
	}
	var remove []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return errors.Join(err, rows.Close())
		}
		if !keep[id] {
			remove = append(remove, id)
		}
	}
	if err := rows.Err(); err != nil {
		return errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range remove {
		if _, err := rt.tx.ExecContext(ctx, `DELETE FROM actionable_alert WHERE external_id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

// CreateAlert refuses IDs already open or previously resolved.
func (r *realmStore) CreateAlert(ctx context.Context, e alerts.Event) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `INSERT INTO actionable_alert(external_id,type,at,source,payload)
	 SELECT ?,?,?,?,? WHERE NOT EXISTS(`+resolvedAlertAudit+`)`, e.ID, e.Type, timeStr(e.At), e.Source, string(e.Payload), e.ID)
	if err != nil {
		if isSQLiteUnique(err) {
			return fmt.Errorf("store: duplicate alert: %w", domain.ErrAlreadyExists)
		}
		return fmt.Errorf("store: create alert: %w", err)
	}
	// A resolved stable ID is a conflict, rather than a successful publication.
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("store: alert already resolved: %w", domain.ErrConflict)
	}
	return nil
}

// PublishAlert is the panel channel: its durable inbox receipt records that the
// persisted event is ready to be consumed by the panel's polling reader. Every
// open event remains listable even if this channel's write fails.
func (r *realmStore) PublishAlert(ctx context.Context, id string) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `INSERT INTO alert_delivery(alert_id,channel,failed)
 SELECT id,'panel',0 FROM actionable_alert WHERE external_id=?
 ON CONFLICT(alert_id,channel) DO UPDATE SET failed=0`, id)
	if err != nil {
		return fmt.Errorf("store: panel alert receipt: %w", err)
	}
	return requireAlertRow(result)
}

const alertSelect = `SELECT a.external_id,a.type,a.at,a.source,a.payload,COALESCE(r.is_read,0)
 FROM actionable_alert a LEFT JOIN alert_read_state r ON r.alert_id=a.id AND r.principal=?`

func scanAlert(row interface{ Scan(...any) error }) (alerts.Alert, error) {
	var a alerts.Alert
	var at, payload string
	if err := row.Scan(&a.ID, &a.Type, &at, &a.Source, &payload, &a.Read); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return a, domain.ErrNotFound
		}
		return a, err
	}
	var err error
	a.At, err = parseStoredTime("actionable_alert.at", at)
	if err != nil {
		return a, err
	}
	if !json.Valid([]byte(payload)) {
		return a, fmt.Errorf("store: malformed alert payload: %w", domain.ErrInvalid)
	}
	a.Payload = json.RawMessage(payload)
	a.Deliveries = []alerts.Delivery{}
	return a, nil
}

// ListAlerts reads rows and delivery status in one snapshot.
func (r *realmStore) ListAlerts(ctx context.Context, principal string) (_ []alerts.Alert, err error) {
	tx, err := r.beginReadTx(ctx, "alert")
	if err != nil {
		return nil, err
	}
	defer rollbackTransaction(ctx, &err, tx)
	rows, err := tx.QueryContext(ctx, alertSelect+` ORDER BY a.at DESC,a.id DESC`, principal)
	if err != nil {
		return nil, err
	}
	out := []alerts.Alert{}
	for rows.Next() {
		a, e := scanAlert(rows)
		if e != nil {
			return nil, errors.Join(e, rows.Close())
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := readAlertDeliveries(ctx, tx, &out[i]); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func readAlertDeliveries(ctx context.Context, q sqlReadWriter, a *alerts.Alert) (err error) {
	rows, err := q.QueryContext(ctx, `SELECT d.channel,d.failed FROM alert_delivery d JOIN actionable_alert a ON a.id=d.alert_id WHERE a.external_id=? ORDER BY d.channel`, a.ID)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var d alerts.Delivery
		if err := rows.Scan(&d.Channel, &d.Failed); err != nil {
			return err
		}
		a.Deliveries = append(a.Deliveries, d)
	}
	return rows.Err()
}

// GetAlert reads only the supplied trusted principal's personal mark.
func (r *realmStore) GetAlert(ctx context.Context, id, principal string) (_ alerts.Alert, err error) {
	tx, err := r.beginReadTx(ctx, "alert")
	if err != nil {
		return alerts.Alert{}, err
	}
	defer rollbackTransaction(ctx, &err, tx)
	a, err := scanAlert(tx.QueryRowContext(ctx, alertSelect+` WHERE a.external_id=?`, principal, id))
	if err != nil {
		return a, err
	}
	if err := readAlertDeliveries(ctx, tx, &a); err != nil {
		return alerts.Alert{}, err
	}
	if err := tx.Commit(); err != nil {
		return alerts.Alert{}, err
	}
	return a, nil
}

// SetAlertRead changes no event fields and cannot resolve an alert.
func (r *realmStore) SetAlertRead(ctx context.Context, id, principal string, read bool) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `INSERT INTO alert_read_state(alert_id,principal,is_read)
 SELECT id,?,? FROM actionable_alert WHERE external_id=?
 ON CONFLICT(alert_id,principal) DO UPDATE SET is_read=excluded.is_read`, principal, read, id)
	if err != nil {
		return fmt.Errorf("store: mark alert read: %w", err)
	}
	return requireAlertRow(result)
}

func requireAlertRow(result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// RecordAlertDelivery records a receipt and audit for an open event. A channel
// can finish after resolution: its resolution audit proves that lifecycle state,
// so only the late outcome audit is written. Unknown IDs remain explicit errors.
func (r *realmStore) RecordAlertDelivery(ctx context.Context, id string, d alerts.Delivery, audit fwstore.AuditEntry) (err error) {
	db, err := r.db()
	if err != nil {
		return err
	}
	dict, err := r.dictionaries()
	if err != nil {
		return err
	}
	tx, err := beginTx(ctx, db, nil)
	if err != nil {
		return err
	}
	defer rollbackTransaction(ctx, &err, tx)
	result, err := tx.ExecContext(ctx, `INSERT INTO alert_delivery(alert_id,channel,failed)
 SELECT id,?,? FROM actionable_alert WHERE external_id=?
 ON CONFLICT(alert_id,channel) DO UPDATE SET failed=excluded.failed`, d.Channel, d.Failed, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var one int
		err := tx.QueryRowContext(ctx, resolvedAlertAudit, id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
	}
	if err := appendAudit(ctx, tx, dict, audit); err != nil {
		return err
	}
	return tx.Commit()
}

// ResolveAlert serialises handlers within this store. The handler runs outside
// the SQL transaction so domain operations can use the single pooled connection.
// External side effects need their own idempotency after a crash or rollback.
func (r *realmStore) ResolveAlert(ctx context.Context, id string, decide func(alerts.Event) (alerts.Resolution, fwstore.AuditEntry, error)) (_ alerts.Resolution, err error) {
	r.store.alertResolutionMu.Lock()
	defer r.store.alertResolutionMu.Unlock()
	db, err := r.db()
	if err != nil {
		return alerts.Resolution{}, err
	}
	dict, err := r.dictionaries()
	if err != nil {
		return alerts.Resolution{}, err
	}
	a, err := scanAlert(db.QueryRowContext(ctx, alertSelect+` WHERE a.external_id=?`, "", id))
	if err != nil {
		return alerts.Resolution{}, err
	}
	result, audit, err := decide(a.Event)
	if err != nil {
		return alerts.Resolution{}, err
	}
	tx, err := beginTx(ctx, db, nil)
	if err != nil {
		return alerts.Resolution{}, err
	}
	defer rollbackTransaction(ctx, &err, tx)
	if err := appendAudit(ctx, tx, dict, audit); err != nil {
		return alerts.Resolution{}, err
	}
	deleted, err := tx.ExecContext(ctx, `DELETE FROM actionable_alert WHERE external_id=?`, id)
	if err != nil {
		return alerts.Resolution{}, err
	}
	if err := requireAlertRow(deleted); err != nil {
		return alerts.Resolution{}, err
	}
	if err := tx.Commit(); err != nil {
		return alerts.Resolution{}, err
	}
	return result, nil
}
