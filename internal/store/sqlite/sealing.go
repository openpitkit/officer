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
	"log/slog"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/secret"
)

const (
	signingKeyTable             = "signing_key"
	signingPrivateKeyColumn     = "private_key"
	marketDataInstanceTable     = "market_data_instance"
	marketDataCredentialsColumn = "credentials"
)

func (s *sqliteStore) sealValue(table, column, rowID string, plaintext []byte) ([]byte, error) {
	key := s.sealer.Load()
	if key == nil {
		return append([]byte{}, plaintext...), nil
	}
	return s.sealValueWithKey(*key, table, column, rowID, plaintext)
}

func (s *sqliteStore) sealValueWithKey(
	key secret.MasterKey, table, column, rowID string, plaintext []byte,
) ([]byte, error) {
	aad, err := secret.BuildAAD(string(s.realm), table, column, rowID)
	if err != nil {
		return nil, fmt.Errorf("store: build AAD for %s.%s row %q: %w", table, column, rowID, err)
	}
	sealed, err := secret.Seal(key, aad, plaintext)
	if err != nil {
		return nil, fmt.Errorf("store: seal %s.%s row %q: %w", table, column, rowID, err)
	}
	return sealed, nil
}

func (s *sqliteStore) openValue(table, column, rowID string, stored []byte) ([]byte, error) {
	key := s.sealer.Load()
	if key == nil {
		return stored, nil
	}
	return s.openValueWithKey(*key, string(s.realm), table, column, rowID, stored)
}

func (s *sqliteStore) openValueWithKey(
	key secret.MasterKey, realm, table, column, rowID string, stored []byte,
) ([]byte, error) {
	aad, err := secret.BuildAAD(realm, table, column, rowID)
	if err != nil {
		return nil, fmt.Errorf("store: build AAD for %s.%s row %q: %w", table, column, rowID, err)
	}
	plaintext, err := secret.Open(key, aad, stored)
	if err != nil {
		return nil, fmt.Errorf("store: open %s.%s row %q: %w", table, column, rowID, err)
	}
	return plaintext, nil
}

func (s *sqliteStore) setSealer(key *secret.MasterKey) {
	if key == nil {
		s.sealer.Store(nil)
		return
	}
	keyCopy := *key
	s.sealer.Store(&keyCopy)
}

// configureSealing decides four startup states: unsealed without a key stays
// plaintext; unsealed with a key seals; sealed without a key refuses; and sealed
// with a key verifies it and refuses a mismatch. It must run in migrateDB so
// Reset seals its fresh file.
// Sealing commits an owed scrub before VACUUM, which cannot run in a transaction,
// and records completion only after VACUUM succeeds. A matching-key restart must
// finish any owed scrub before installing the sealer.
func (s *sqliteStore) configureSealing(
	ctx context.Context, db *sql.DB, databaseCreated bool,
) error {
	var storedVerifier []byte
	var vacuumedAt sql.NullString
	err := db.QueryRowContext(
		ctx, `SELECT key_verifier, vacuumed_at FROM secret_state WHERE singleton = 1`,
	).Scan(&storedVerifier, &vacuumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		if s.configuredMasterKey == nil {
			return nil
		}
		if err := s.sealRealm(ctx, db, *s.configuredMasterKey); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `VACUUM`); err != nil {
			return fmt.Errorf("store: vacuum sqlite after sealing: %w", err)
		}
		if !databaseCreated {
			slog.Warn(
				"SQLite realm sealed; previously stored secrets must be considered exposed and should be reissued",
			)
		}
		if _, err := db.ExecContext(
			ctx, `UPDATE secret_state SET vacuumed_at = ? WHERE singleton = 1`, nowStr(),
		); err != nil {
			return fmt.Errorf("store: record sqlite sealing vacuum completion: %w", err)
		}
		s.setSealer(s.configuredMasterKey)
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: read secret sealing state: %w", err)
	}
	if s.configuredMasterKey == nil {
		return errors.New("store: database is sealed but no master key was supplied")
	}

	configuredVerifier, err := secret.KeyVerifier(*s.configuredMasterKey)
	if err != nil {
		return fmt.Errorf("store: derive configured master key verifier: %w", err)
	}
	match, err := secret.KeyVerifiersEqual(storedVerifier, configuredVerifier)
	if err != nil {
		return fmt.Errorf("store: compare stored master key verifier: %w", err)
	}
	if !match {
		return errors.New("store: supplied master key does not match the key used to seal this database")
	}
	if !vacuumedAt.Valid {
		if _, err := db.ExecContext(ctx, `VACUUM`); err != nil {
			return fmt.Errorf("store: vacuum sqlite after sealing: %w", err)
		}
		slog.Warn(
			"SQLite realm sealed; previously stored secrets must be considered exposed and should be reissued",
		)
		if _, err := db.ExecContext(
			ctx, `UPDATE secret_state SET vacuumed_at = ? WHERE singleton = 1`, nowStr(),
		); err != nil {
			return fmt.Errorf("store: record sqlite sealing vacuum completion: %w", err)
		}
	}
	s.setSealer(s.configuredMasterKey)
	return nil
}

func (s *sqliteStore) sealRealm(
	ctx context.Context, db *sql.DB, key secret.MasterKey,
) (err error) {
	verifier, err := secret.KeyVerifier(key)
	if err != nil {
		return fmt.Errorf("store: derive master key verifier for sealing: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin secret sealing: %w", err)
	}
	defer rollbackTransaction(&err, tx)

	if err := s.sealSigningPrivateKeys(ctx, tx, key); err != nil {
		return err
	}
	if err := s.sealMarketDataCredentials(ctx, tx, key); err != nil {
		return err
	}
	// The verifier and owed scrub must commit with the sealed values so a restart
	// can neither miss the scrub nor seal the values a second time.
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO secret_state (
			singleton, key_verifier, sealed_at, vacuumed_at
		) VALUES (1, ?, ?, NULL)`,
		verifier, nowStr(),
	); err != nil {
		return fmt.Errorf("store: record secret sealing state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit secret sealing: %w", err)
	}
	return nil
}

func (s *sqliteStore) sealSigningPrivateKeys(
	ctx context.Context, tx *sql.Tx, key secret.MasterKey,
) error {
	type signingSecret struct {
		keyID      string
		privateKey []byte
	}

	rows, err := tx.QueryContext(ctx, `SELECT key_id, private_key FROM signing_key ORDER BY id`)
	if err != nil {
		return fmt.Errorf("store: read signing keys for sealing: %w", err)
	}
	secrets := make([]signingSecret, 0)
	for rows.Next() {
		var item signingSecret
		if err := rows.Scan(&item.keyID, &item.privateKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: scan signing key for sealing: %w", err)
		}
		secrets = append(secrets, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("store: iterate signing keys for sealing: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: close signing keys for sealing: %w", err)
	}

	for _, item := range secrets {
		sealed, err := s.sealValueWithKey(
			key, signingKeyTable, signingPrivateKeyColumn, item.keyID, item.privateKey,
		)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(
			ctx, `UPDATE signing_key SET private_key = ? WHERE key_id = ?`,
			sealed, item.keyID,
		); err != nil {
			return fmt.Errorf("store: seal signing key private material: %w", err)
		}
	}
	return nil
}

func (s *sqliteStore) sealMarketDataCredentials(
	ctx context.Context, tx *sql.Tx, key secret.MasterKey,
) error {
	type marketDataSecret struct {
		externalID  domain.ExternalID
		credentials []byte
	}

	rows, err := tx.QueryContext(
		ctx, `SELECT external_id, credentials FROM market_data_instance`,
	)
	if err != nil {
		return fmt.Errorf("store: read market data credentials for sealing: %w", err)
	}
	secrets := make([]marketDataSecret, 0)
	for rows.Next() {
		var rawID, credentials []byte
		if err := rows.Scan(&rawID, &credentials); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: scan market data credentials for sealing: %w", err)
		}
		externalID, err := domain.ExternalIDFromBytes(rawID)
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: decode market data external id for sealing: %w", err)
		}
		secrets = append(secrets, marketDataSecret{
			externalID: externalID, credentials: credentials,
		})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("store: iterate market data credentials for sealing: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: close market data credentials for sealing: %w", err)
	}

	for _, item := range secrets {
		sealed, err := s.sealValueWithKey(
			key, marketDataInstanceTable, marketDataCredentialsColumn,
			item.externalID.String(), item.credentials,
		)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(
			ctx, `UPDATE market_data_instance SET credentials = ? WHERE external_id = ?`,
			sealed, item.externalID.Bytes(),
		); err != nil {
			return fmt.Errorf("store: seal market data credentials: %w", err)
		}
	}
	return nil
}
