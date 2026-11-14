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
	"io"
	"math"
	"strings"

	"go.openpit.dev/officer/framework/domain"
	httpapi "go.openpit.dev/officer/framework/web/httpapi/dto"
)

func (r *realmStore) GetTestExecutionPolicy(ctx context.Context, connection domain.ExternalID) (result domain.TestExecutionPolicyState, resultErr error) {
	db, err := r.db()
	if err != nil {
		return domain.TestExecutionPolicyState{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.TestExecutionPolicyState{}, fmt.Errorf("store: begin test policy read: %w", err)
	}
	defer rollbackTransaction(&resultErr, tx)
	id, class, err := r.executionPolicyConnection(ctx, tx, connection)
	if err != nil {
		return domain.TestExecutionPolicyState{}, err
	}
	if !class {
		return domain.TestExecutionPolicyState{}, fmt.Errorf("trading connection %s: %w", connection, domain.ErrExecutionPolicyUnsupported)
	}
	state, err := readTestExecutionPolicy(ctx, tx, id, connection)
	if err != nil {
		return domain.TestExecutionPolicyState{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.TestExecutionPolicyState{}, fmt.Errorf("store: finish test policy read: %w", err)
	}
	return state, nil
}

func (r *realmStore) ReplaceTestExecutionPolicy(
	ctx context.Context, connection domain.ExternalID, policy domain.TestExecutionPolicy,
) (oldState, newState domain.TestExecutionPolicyState, err error) {
	db, err := r.db()
	if err != nil {
		return oldState, newState, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return oldState, newState, fmt.Errorf("store: begin test policy write: %w", err)
	}
	defer rollbackTransaction(&err, tx)
	id, class, err := r.executionPolicyConnection(ctx, tx, connection)
	if err != nil {
		return oldState, newState, err
	}
	if !class {
		return oldState, newState, fmt.Errorf("trading connection %s: %w", connection, domain.ErrExecutionPolicyUnsupported)
	}
	if err := domain.ValidateTestExecutionPolicy(policy); err != nil {
		return oldState, newState, err
	}
	oldState, err = readTestExecutionPolicy(ctx, tx, id, connection)
	if err != nil {
		return oldState, newState, err
	}
	if oldState.Revision == math.MaxInt64 {
		return oldState, newState, fmt.Errorf("store: test policy revision overflow for connection %s: %w", connection, domain.ErrConflict)
	}
	dto := httpapi.ToTestExecutionPolicyStateDTO(connection, domain.TestExecutionPolicyState{Policy: policy})
	rules, err := json.Marshal(dto.Rules)
	if err != nil {
		return oldState, newState, fmt.Errorf("store: encode test policy: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE trading_test_policy SET revision = ?, rules = ? WHERE connection_id = ?`,
		oldState.Revision+1, string(rules), id,
	); err != nil {
		return oldState, newState, fmt.Errorf("store: replace test policy: %w", err)
	}
	newState, err = readTestExecutionPolicy(ctx, tx, id, connection)
	if err != nil {
		return oldState, newState, err
	}
	if err := tx.Commit(); err != nil {
		return oldState, newState, fmt.Errorf("store: commit test policy write: %w", err)
	}
	return oldState, newState, nil
}

func (r *realmStore) executionPolicyConnection(ctx context.Context, q sqlQueryer, connection domain.ExternalID) (int64, bool, error) {
	classifier, err := r.store.executionPolicyProviders()
	if err != nil {
		return 0, false, err
	}
	var id int64
	var provider string
	if err := q.QueryRowContext(ctx, `SELECT id, provider FROM trading_connection WHERE external_id = ?`, connection.Bytes()).Scan(&id, &provider); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, fmt.Errorf("trading connection %s: %w", connection, domain.ErrNotFound)
		}
		return 0, false, fmt.Errorf("store: read test policy connection: %w", err)
	}
	class, err := domain.ProviderSupportsExecutionPolicy(classifier, provider)
	return id, class, err
}

func readTestExecutionPolicy(ctx context.Context, q sqlQueryer, id int64, connection domain.ExternalID) (domain.TestExecutionPolicyState, error) {
	var state domain.TestExecutionPolicyState
	var rules string
	if err := q.QueryRowContext(ctx, `SELECT revision, rules FROM trading_test_policy WHERE connection_id = ?`, id).Scan(&state.Revision, &rules); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return state, fmt.Errorf("store: test policy missing for connection %s", connection)
		}
		return state, fmt.Errorf("store: read test policy: %w", err)
	}
	return decodeStoredTestExecutionPolicy(state.Revision, rules)
}

func decodeStoredTestExecutionPolicy(revision uint64, rules string) (domain.TestExecutionPolicyState, error) {
	if revision == 0 || revision > math.MaxInt64 {
		return domain.TestExecutionPolicyState{}, fmt.Errorf("store: invalid test policy revision %d", revision)
	}
	policy, err := decodeTestExecutionPolicy(rules)
	if err != nil {
		return domain.TestExecutionPolicyState{}, err
	}
	return domain.TestExecutionPolicyState{Revision: revision, Policy: policy}, nil
}

func decodeTestExecutionPolicy(rules string) (domain.TestExecutionPolicy, error) {
	var raw json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(rules))
	if err := decoder.Decode(&raw); err != nil {
		return domain.TestExecutionPolicy{}, fmt.Errorf("store: decode test policy: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return domain.TestExecutionPolicy{}, fmt.Errorf("store: trailing data in test policy")
	}
	var dto httpapi.TestExecutionPolicyDTO
	if err := json.Unmarshal([]byte(`{"rules":`+string(raw)+`}`), &dto); err != nil {
		return domain.TestExecutionPolicy{}, fmt.Errorf("store: decode test policy: %w", err)
	}
	policy, err := httpapi.FromTestExecutionPolicyDTO(dto)
	if err != nil {
		return domain.TestExecutionPolicy{}, fmt.Errorf("store: invalid test policy: %w", err)
	}
	return policy, nil
}
