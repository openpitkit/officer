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
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/secret"
)

const (
	signingKeyTable             = "signing_key"
	signingPrivateKeyColumn     = "private_key"
	marketDataInstanceTable     = "market_data_instance"
	marketDataCredentialsColumn = "credentials"
	tradingConnectionTable      = "trading_connection"
	tradingCredentialsColumn    = "credentials"
	accessCredentialTable       = "access_credential"
	accessCredentialValueColumn = "value"
)

// sealValue seals plaintext for its table, column and row with the installed
// master key. Without one it returns plaintext as is, a signing private key
// included.
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
// plaintext, a signing private key included; unsealed with a key seals; sealed
// without a key refuses; and sealed with a key verifies it and refuses a
// mismatch. Sealing always records a seal report, so a sealed database without
// one is refused with an error wrapping domain.ErrInvalid, whether or not its
// scrub finished. It must run in migrateDB so Reset seals its fresh file.
// Sealing commits an owed scrub before VACUUM, which cannot run in a transaction,
// and records completion only after VACUUM succeeds. A matching-key restart must
// finish any owed scrub before installing the sealer. The exposure warning is
// logged only when sealing re-sealed a stored secret that held a value: the
// count commits in seal_report with the sealed values, and a restart that
// finishes the scrub reads it from there.
func (s *sqliteStore) configureSealing(ctx context.Context, db *sql.DB) error {
	var storedVerifier []byte
	var vacuumedAt sql.NullString
	err := db.QueryRowContext(
		ctx, `SELECT key_verifier, vacuumed_at FROM secret_state WHERE singleton = 1`,
	).Scan(&storedVerifier, &vacuumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		if s.configuredMasterKey == nil {
			return nil
		}
		resealed, err := s.sealRealm(ctx, db, *s.configuredMasterKey)
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `VACUUM`); err != nil {
			return fmt.Errorf("store: vacuum sqlite after sealing: %w", err)
		}
		warnOfExposure(resealed)
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
	report, sealed, err := readSealReport(ctx, db)
	if err != nil {
		return err
	}
	if !sealed {
		return fmt.Errorf(
			"store: database at %q is sealed but holds no seal report; "+
				"recreate the database: %w",
			s.path, domain.ErrInvalid,
		)
	}
	if !vacuumedAt.Valid {
		if _, err := db.ExecContext(ctx, `VACUUM`); err != nil {
			return fmt.Errorf("store: vacuum sqlite after sealing: %w", err)
		}
		warnOfExposure(report.ResealedSecrets)
		if _, err := db.ExecContext(
			ctx, `UPDATE secret_state SET vacuumed_at = ? WHERE singleton = 1`, nowStr(),
		); err != nil {
			return fmt.Errorf("store: record sqlite sealing vacuum completion: %w", err)
		}
	}
	s.setSealer(s.configuredMasterKey)
	return nil
}

// sealRealm seals every stored secret in place under key, in one transaction
// that also records the key verifier and the seal report. It returns how many
// of the sealed values held a secret.
func (s *sqliteStore) sealRealm(
	ctx context.Context, db *sql.DB, key secret.MasterKey,
) (_ int, err error) {
	verifier, err := secret.KeyVerifier(key)
	if err != nil {
		return 0, fmt.Errorf("store: derive master key verifier for sealing: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin secret sealing: %w", err)
	}
	defer rollbackTransaction(&err, tx)

	signingKeys, err := s.sealTextKeyedSecrets(
		ctx, tx, key,
		signingKeyTable, "key_id", signingPrivateKeyColumn, "signing key",
	)
	if err != nil {
		return 0, err
	}
	marketData, err := s.sealCredentials(
		ctx, tx, key,
		marketDataInstanceTable, marketDataCredentialsColumn, "market data",
	)
	if err != nil {
		return 0, err
	}
	trading, err := s.sealCredentials(
		ctx, tx, key,
		tradingConnectionTable, tradingCredentialsColumn, "trading",
	)
	if err != nil {
		return 0, err
	}
	accessCredentials, err := s.sealTextKeyedSecrets(
		ctx, tx, key,
		accessCredentialTable, "kind", accessCredentialValueColumn, "access credential",
	)
	if err != nil {
		return 0, err
	}
	resealed := signingKeys + marketData + trading + accessCredentials
	sealedAt := nowStr()
	// The verifier, the owed scrub and the seal report commit with the sealed
	// values, so a restart can neither miss the scrub nor seal the values a
	// second time, and a restart that finishes the scrub warns from the same
	// count.
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO secret_state (
			singleton, key_verifier, sealed_at, vacuumed_at
		) VALUES (1, ?, ?, NULL)`,
		verifier, sealedAt,
	); err != nil {
		return 0, fmt.Errorf("store: record secret sealing state: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO seal_report (singleton, resealed_secrets, sealed_at)
		 VALUES (1, ?, ?)`,
		resealed, sealedAt,
	); err != nil {
		return 0, fmt.Errorf("store: record seal report: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit secret sealing: %w", err)
	}
	return resealed, nil
}

// sealTextKeyedSecrets seals every value of valueColumn in table in place,
// each bound to its row's text id in idColumn, and returns how many held a
// secret. noun names a row of table in errors. Rows are sealed in rowid
// order, which for signing_key is its surrogate id.
func (s *sqliteStore) sealTextKeyedSecrets(
	ctx context.Context,
	tx *sql.Tx,
	key secret.MasterKey,
	table string,
	idColumn string,
	valueColumn string,
	noun string,
) (int, error) {
	type storedSecret struct {
		id    string
		value []byte
	}

	rows, err := tx.QueryContext(
		ctx,
		fmt.Sprintf("SELECT %s, %s FROM %s ORDER BY rowid", idColumn, valueColumn, table),
	)
	if err != nil {
		return 0, fmt.Errorf("store: read %s rows for sealing: %w", noun, err)
	}
	secrets := make([]storedSecret, 0)
	for rows.Next() {
		var item storedSecret
		if err := rows.Scan(&item.id, &item.value); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("store: scan %s for sealing: %w", noun, err)
		}
		secrets = append(secrets, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("store: iterate %s rows for sealing: %w", noun, err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("store: close %s rows for sealing: %w", noun, err)
	}

	resealed := 0
	for _, item := range secrets {
		sealed, err := s.sealValueWithKey(key, table, valueColumn, item.id, item.value)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(
			ctx,
			fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?", table, valueColumn, idColumn),
			sealed, item.id,
		); err != nil {
			return 0, fmt.Errorf("store: seal %s %q: %w", noun, item.id, err)
		}
		if holdsSecret(item.value) {
			resealed++
		}
	}
	return resealed, nil
}

func (s *sqliteStore) sealCredentials(
	ctx context.Context,
	tx *sql.Tx,
	key secret.MasterKey,
	table string,
	column string,
	noun string,
) (int, error) {
	type storedCredential struct {
		externalID  domain.ExternalID
		credentials []byte
	}

	rows, err := tx.QueryContext(
		ctx, fmt.Sprintf("SELECT external_id, %s FROM %s", column, table),
	)
	if err != nil {
		return 0, fmt.Errorf("store: read %s credentials for sealing: %w", noun, err)
	}
	secrets := make([]storedCredential, 0)
	for rows.Next() {
		var rawID, credentials []byte
		if err := rows.Scan(&rawID, &credentials); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("store: scan %s credentials for sealing: %w", noun, err)
		}
		externalID, err := domain.ExternalIDFromBytes(rawID)
		if err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf(
				"store: decode %s external id for sealing: %w", noun, err,
			)
		}
		secrets = append(secrets, storedCredential{
			externalID: externalID, credentials: credentials,
		})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("store: iterate %s credentials for sealing: %w", noun, err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("store: close %s credentials for sealing: %w", noun, err)
	}

	resealed := 0
	for _, item := range secrets {
		sealed, err := s.sealValueWithKey(
			key, table, column,
			item.externalID.String(), item.credentials,
		)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(
			ctx,
			fmt.Sprintf(
				"UPDATE %s SET %s = ? WHERE external_id = ?", table, column,
			),
			sealed, item.externalID.Bytes(),
		); err != nil {
			return 0, fmt.Errorf("store: seal %s credentials: %w", noun, err)
		}
		if holdsSecret(item.credentials) {
			resealed++
		}
	}
	return resealed, nil
}

// holdsSecret reports whether a stored secret value holds anything that
// sealing it after the fact leaves exposed. Every secret column passes
// through sealing, its empty form included, and the store writes these
// empty forms: zero bytes for a verify-only signing key's private material,
// zero bytes for a market-data instance without credentials (BYO, mock, or a
// restore that could not open the archived ones), and {} for a trading
// connection without them, whose credentials are always a JSON object. An
// access credential is never empty.
func holdsSecret(value []byte) bool {
	return len(value) > 0 && string(value) != "{}"
}

// warnOfExposure logs that the secrets sealing found stored in the clear must
// be considered exposed. It logs nothing when sealing found none.
func warnOfExposure(resealed int) {
	if resealed > 0 {
		slog.Warn(
			"SQLite realm sealed; previously stored secrets must be considered exposed and should be reissued",
		)
	}
}

// SealReport is what sealing the database recorded.
type SealReport struct {
	// SealedAt is when the database was sealed.
	SealedAt time.Time
	// ResealedSecrets is how many stored secrets held a value when the
	// database was sealed. They had been stored in the clear, so they must be
	// considered exposed.
	ResealedSecrets int
}

// SealReport returns what sealing the database recorded. The bool is false
// when the database has not been sealed.
func (s *sqliteStore) SealReport(ctx context.Context) (SealReport, bool, error) {
	db, err := s.liveDB()
	if err != nil {
		return SealReport{}, false, err
	}
	return readSealReport(ctx, db)
}

func readSealReport(ctx context.Context, q sqlQueryer) (SealReport, bool, error) {
	var resealed int
	var sealedAt string
	err := q.QueryRowContext(
		ctx, `SELECT resealed_secrets, sealed_at FROM seal_report WHERE singleton = 1`,
	).Scan(&resealed, &sealedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SealReport{}, false, nil
	}
	if err != nil {
		return SealReport{}, false, fmt.Errorf("store: read seal report: %w", err)
	}
	at, err := parseStoredTime("seal_report.sealed_at", sealedAt)
	if err != nil {
		return SealReport{}, false, err
	}
	return SealReport{SealedAt: at, ResealedSecrets: resealed}, true, nil
}
