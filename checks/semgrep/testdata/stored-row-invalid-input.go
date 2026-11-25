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

func scanAlert(row interface{ Scan(...any) error }) (Alert, error) {
	var alert Alert
	var payload string
	if err := row.Scan(&payload); err != nil {
		return alert, err
	}
	if !json.Valid([]byte(payload)) {
		// ruleid: stored-row-invalid-input
		return alert, fmt.Errorf("malformed alert payload: %w", domain.ErrInvalid)
	}
	return alert, nil
}

func decodeStoredPayload(payload string) error {
	var value map[string]any
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		// ruleid: stored-row-invalid-input
		return fmt.Errorf("stored payload: %v: %w", err, domain.ErrInvalid)
	}
	return nil
}

func decodeStoredMasked(payload string) error {
	var value map[string]any
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		// ok: stored-row-invalid-input
		return domain.Internal(fmt.Errorf("stored payload: %v: %w", err, domain.ErrInvalid))
	}
	return nil
}

func (r *realmStore) readID(rows *sql.Rows) (domain.ExternalID, error) {
	var raw []byte
	if err := rows.Scan(&raw); err != nil {
		return domain.ExternalID{}, err
	}
	id, err := domain.ExternalIDFromBytes(raw)
	if err != nil {
		// ruleid: stored-row-invalid-input
		return id, fmt.Errorf("stored ID: %w", err)
	}
	return id, nil
}

func scanMasked(row Row) error {
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		return err
	}
	_, err := domain.ExternalIDFromBytes(raw)
	if err != nil {
		// ok: stored-row-invalid-input
		return fmt.Errorf("stored ID: %w", domain.Internal(err))
	}
	return nil
}

func scanAssigned(row Row) (domain.ExternalID, error) {
	var raw []byte
	var id domain.ExternalID
	if err := row.Scan(&raw); err != nil {
		return id, err
	}
	if id, err = domain.ExternalIDFromBytes(raw); err != nil {
		// ruleid: stored-row-invalid-input
		return id, fmt.Errorf("stored ID: %w", err)
	}
	return id, nil
}

func scanContext(row Row) error {
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		return err
	}
	_, err := domain.ExternalIDFromBytes(raw)
	if err != nil {
		// ok: stored-row-invalid-input
		return fmt.Errorf("operation: %w; stored ID: %v", otherInternalError, err)
	}
	return nil
}

func (rt *restoreTx) restoreExecutionReportEvent(link Link) error {
	var reportOrderID, eventOrderID int64
	if err := rt.tx.QueryRowContext(ctx, "SELECT order_id FROM execution_report WHERE id = ?", link.Report).Scan(&reportOrderID); err != nil {
		return err
	}
	if err := rt.tx.QueryRowContext(ctx, "SELECT order_id FROM order_event WHERE id = ?", link.Event).Scan(&eventOrderID); err != nil {
		return err
	}
	if reportOrderID != eventOrderID {
		// ok: stored-row-invalid-input
		return fmt.Errorf("report and event belong to different orders: %w", domain.ErrInvalid)
	}
	return nil
}

func validateRestoredSigningKey(key Key) error {
	if key.CreatedAt.IsZero() {
		// ok: stored-row-invalid-input
		return fmt.Errorf("created at is required: %w", domain.ErrInvalid)
	}
	return nil
}

func parseRequestID(raw []byte) (domain.ExternalID, error) {
	id, err := domain.ExternalIDFromBytes(raw)
	if err != nil {
		// ok: stored-row-invalid-input
		return id, fmt.Errorf("input ID: %w", err)
	}
	return id, nil
}

func decodeTestExecutionPolicy(rules string) (Policy, error) {
	var dto DTO
	if err := json.Unmarshal([]byte(rules), &dto); err != nil {
		// ok: stored-row-invalid-input
		return Policy{}, fmt.Errorf("decode policy: %w", err)
	}
	policy, err := fromDTO(dto)
	if err != nil {
		// ok: stored-row-invalid-input
		return Policy{}, fmt.Errorf("invalid policy: %w", err)
	}
	return policy, nil
}

func resolveDictID(q Queryer, code string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, "SELECT id FROM dictionary WHERE code = ?", code).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// ok: stored-row-invalid-input
		return 0, fmt.Errorf("unknown code %q: %w", code, domain.ErrInvalid)
	}
	return id, err
}

func resolveSigningKeyID(q Queryer, keyID string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, "SELECT id FROM signing_key WHERE key_id = ?", keyID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// ok: stored-row-invalid-input
		return 0, fmt.Errorf("signing key %q: %w", keyID, domain.ErrInvalid)
	}
	return id, err
}

func ensureSchedulerExecution(q Queryer, execution Execution) error {
	if err := execution.Validate(); err != nil {
		// ok: stored-row-invalid-input
		return fmt.Errorf("restore scheduled execution: %v: %w", err, domain.ErrInvalid)
	}
	var stored string
	if err := q.QueryRowContext(ctx, "SELECT record FROM scheduler_execution WHERE run_id = ?", execution.ID).Scan(&stored); err != nil {
		return err
	}
	if stored != execution.Record {
		// ok: stored-row-invalid-input
		return fmt.Errorf("execution history conflicts: %w", domain.ErrInvalid)
	}
	return nil
}

func checkOrderReviewInvariant(q Queryer) error {
	var raw []byte
	if err := q.QueryRowContext(ctx, "SELECT external_id FROM inconsistent_orders").Scan(&raw); err != nil {
		return err
	}
	id, err := domain.ExternalIDFromBytes(raw)
	if err != nil {
		// ruleid: stored-row-invalid-input
		return fmt.Errorf("decode review invariant order: %w", err)
	}
	// ok: stored-row-invalid-input
	return fmt.Errorf("order %q status and review disagree: %w", id, domain.ErrInvalid)
}

func (r *realmStore) loadPayload(row Row) error {
	var payload string
	if err := row.Scan(&payload); err != nil {
		return err
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		// ruleid: stored-row-invalid-input
		return fmt.Errorf("stored payload: %v: %w", err, domain.ErrInvalid)
	}
	return nil
}

func decodeJoined(payload string) error {
	// ruleid: stored-row-invalid-input
	return errors.Join(errors.New("corrupt stored payload"), domain.ErrInvalid)
}
