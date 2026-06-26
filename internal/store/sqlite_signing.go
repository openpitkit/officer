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

// Signing group of the SQLite store: Ed25519 signing keys and the global signing
// configuration. A signing key is a dictionary entity, but unlike the code-keyed
// dictionaries it is addressed by its own UUID key_id (the handle it carries into
// approval envelopes), so the surrogate id never crosses the interface boundary
// here either. The private and public key material is stored verbatim as BLOBs
// and read back byte-identical. Active-key semantics are caller-driven: the
// signing service deactivates every key, then upserts one with Active set, so
// these methods only persist and surface the flag faithfully. signing_config is a
// plain key/value table whose key is a hardcoded enum stored as TEXT, never a
// dictionary.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.openpit.dev/officer/internal/domain"
)

// --- Signing keys -----------------------------------------------------------

// signingKeySelect is the shared projection for signing-key reads that need the
// private material (GetSigningKey, GetActiveSigningKey). The key_id UUID is the
// public handle; the surrogate id is never selected.
const signingKeySelect = `
SELECT key_id, alg, private_key, public_key, created_at, active
FROM signing_keys`

// UpsertSigningKey inserts or replaces a signing key row, keyed by its UUID
// key_id. The private and public key material is stored verbatim as BLOBs and
// the active flag is persisted exactly as supplied; active-key switching is the
// caller's responsibility (it deactivates all then upserts one as active).
func (r *realmStore) UpsertSigningKey(
	ctx context.Context, key domain.SigningKey,
) error {
	if _, err := r.db().ExecContext(
		ctx,
		`INSERT INTO signing_keys
		 (key_id, alg, private_key, public_key, created_at, active)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(key_id) DO UPDATE SET
		   alg         = excluded.alg,
		   private_key = excluded.private_key,
		   public_key  = excluded.public_key,
		   created_at  = excluded.created_at,
		   active      = excluded.active`,
		key.KeyID, key.Alg, key.PrivateKey, key.PublicKey,
		signingKeyCreatedAt(key.CreatedAt), key.Active,
	); err != nil {
		return fmt.Errorf("store: upsert signing key: %w", err)
	}
	return nil
}

// GetActiveSigningKey returns the single active key with PrivateKey populated.
// The bool is false when no key is active. Only one key is active at a time by
// the caller's discipline; LIMIT 1 guards against a stray duplicate.
func (r *realmStore) GetActiveSigningKey(
	ctx context.Context,
) (domain.SigningKey, bool, error) {
	row := r.db().QueryRowContext(
		ctx, signingKeySelect+` WHERE active = 1 LIMIT 1`,
	)
	key, err := scanSigningKeyRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SigningKey{}, false, nil
	}
	if err != nil {
		return domain.SigningKey{}, false, fmt.Errorf("store: get active signing key: %w", err)
	}
	return key, true, nil
}

// GetSigningKey returns the key with the given key id, PrivateKey populated.
// Returns domain.ErrNotFound when absent.
func (r *realmStore) GetSigningKey(
	ctx context.Context, keyID string,
) (domain.SigningKey, error) {
	row := r.db().QueryRowContext(
		ctx, signingKeySelect+` WHERE key_id = ?`, keyID,
	)
	key, err := scanSigningKeyRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SigningKey{}, fmt.Errorf("signing key %q: %w", keyID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.SigningKey{}, fmt.Errorf("store: get signing key: %w", err)
	}
	return key, nil
}

// ListSigningKeys returns all keys ordered by created_at DESC (newest first).
// PrivateKey is deliberately NOT populated: a listing never carries private
// material; callers use GetSigningKey/GetActiveSigningKey for that.
func (r *realmStore) ListSigningKeys(ctx context.Context) ([]domain.SigningKey, error) {
	rows, err := r.db().QueryContext(
		ctx,
		`SELECT key_id, alg, public_key, created_at, active
		 FROM signing_keys ORDER BY created_at DESC, id DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list signing keys: %w", err)
	}
	defer func() { _ = rows.Close() }()

	keys := make([]domain.SigningKey, 0)
	for rows.Next() {
		key, err := scanSigningKeyListRow(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate signing keys: %w", err)
	}
	return keys, nil
}

// DeactivateAllSigningKeys clears the active flag on every signing key. It is a
// no-op when there are no active keys (or no keys at all).
func (r *realmStore) DeactivateAllSigningKeys(ctx context.Context) error {
	if _, err := r.db().ExecContext(
		ctx, `UPDATE signing_keys SET active = 0 WHERE active = 1`,
	); err != nil {
		return fmt.Errorf("store: deactivate signing keys: %w", err)
	}
	return nil
}

// signingKeyCreatedAt formats the key's creation time as RFC3339Nano UTC text,
// falling back to now when the caller left it zero so the column is never empty.
func signingKeyCreatedAt(t time.Time) string {
	if t.IsZero() {
		return nowStr()
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// scanSigningKeyRow scans one private-material-bearing key projection from a
// single row, decoding the created_at timestamp and copying the BLOBs verbatim.
func scanSigningKeyRow(row *sql.Row) (domain.SigningKey, error) {
	var (
		key                   domain.SigningKey
		privateKey, publicKey []byte
		createdAt             string
	)
	if err := row.Scan(
		&key.KeyID, &key.Alg, &privateKey, &publicKey, &createdAt, &key.Active,
	); err != nil {
		return domain.SigningKey{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return domain.SigningKey{}, fmt.Errorf("store: parse signing key created_at %q: %w", createdAt, err)
	}
	key.PrivateKey = privateKey
	key.PublicKey = publicKey
	key.CreatedAt = parsed
	return key, nil
}

// scanSigningKeyListRow scans one listing projection (no private material).
func scanSigningKeyListRow(rows *sql.Rows) (domain.SigningKey, error) {
	var (
		key       domain.SigningKey
		publicKey []byte
		createdAt string
	)
	if err := rows.Scan(
		&key.KeyID, &key.Alg, &publicKey, &createdAt, &key.Active,
	); err != nil {
		return domain.SigningKey{}, fmt.Errorf("store: scan signing key: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return domain.SigningKey{}, fmt.Errorf("store: parse signing key created_at %q: %w", createdAt, err)
	}
	key.PublicKey = publicKey
	key.CreatedAt = parsed
	return key, nil
}

// --- Signing config ---------------------------------------------------------

// GetSigningConfig returns the value for the given enum config key. The bool is
// false when the key is absent. The key is a hardcoded enum stored as TEXT, not
// a dictionary code.
func (r *realmStore) GetSigningConfig(
	ctx context.Context, key string,
) (string, bool, error) {
	var value string
	err := r.db().QueryRowContext(
		ctx, `SELECT value FROM signing_config WHERE key = ?`, key,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: get signing config: %w", err)
	}
	return value, true, nil
}

// SetSigningConfig upserts the value for the given enum config key.
func (r *realmStore) SetSigningConfig(ctx context.Context, key, value string) error {
	if _, err := r.db().ExecContext(
		ctx,
		`INSERT INTO signing_config (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	); err != nil {
		return fmt.Errorf("store: set signing config: %w", err)
	}
	return nil
}

func resolveSigningKeyID(ctx context.Context, q sqlQueryer, keyID string) (int64, error) {
	var id int64
	err := q.QueryRowContext(
		ctx, `SELECT id FROM signing_keys WHERE key_id = ?`, keyID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("signing key %q: %w", keyID, domain.ErrInvalid)
	}
	if err != nil {
		return 0, fmt.Errorf("store: resolve signing key %q: %w", keyID, err)
	}
	return id, nil
}
