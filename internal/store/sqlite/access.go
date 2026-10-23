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

// Access group of the SQLite store: the access mode, the credentials of token
// mode and the panel sessions, kept in the application's own tables. The
// mode and the two token values are also held in memory as the access
// snapshot, which a write replaces only after its transaction commits.

package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/internal/access"
)

// The access_credential kinds. Each is also the row id its sealed value is
// bound to.
const (
	panelPasswordKind = "panel_password"
	apiTokenKind      = "api_token"
	agentTokenKind    = "agent_token"
)

// Snapshot returns the access state the database last committed, from
// memory. Migrate publishes the first one, and the store must not be used
// before Migrate succeeds: until then there is no access state to report,
// and Snapshot panics rather than invent a mode.
func (s *sqliteStore) Snapshot() access.Snapshot {
	snapshot := s.accessSnapshot.Load()
	if snapshot == nil {
		panic("store: access snapshot read before Migrate")
	}
	return *snapshot
}

// SetupState reads first-run progress in the transaction checking setup mode.
func (s *sqliteStore) SetupState(ctx context.Context) (_ access.SetupState, err error) {
	tx, err := s.beginAccessWrite(ctx, access.ModeSetup)
	if err != nil {
		return access.SetupState{}, err
	}
	defer rollbackTransaction(&err, tx)

	var step string
	var declinedAt sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT setup_step, encryption_declined_at
		 FROM access_state WHERE singleton = 1`,
	).Scan(&step, &declinedAt); err != nil {
		return access.SetupState{}, fmt.Errorf("store: read setup state: %w", err)
	}
	state := access.SetupState{}
	state.Step, err = access.ParseSetupStep(step)
	if err != nil {
		return access.SetupState{}, fmt.Errorf("store: unknown setup step %q", step)
	}
	if declinedAt.Valid {
		at, err := parseStoredTime("access_state.encryption_declined_at", declinedAt.String)
		if err != nil {
			return access.SetupState{}, err
		}
		state.EncryptionDeclinedAt = &at
	}
	if err := tx.Commit(); err != nil {
		return access.SetupState{}, fmt.Errorf("store: commit setup read: %w", err)
	}
	return state, nil
}

// SetSetupStep saves progress without enforcing an order on the steps.
func (s *sqliteStore) SetSetupStep(
	ctx context.Context, step access.SetupStep,
) (err error) {
	tx, err := s.beginAccessWrite(ctx, access.ModeSetup)
	if err != nil {
		return err
	}
	defer rollbackTransaction(&err, tx)
	if _, err := access.ParseSetupStep(string(step)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE access_state SET setup_step = ? WHERE singleton = 1`, string(step),
	); err != nil {
		return fmt.Errorf("store: set setup step: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit setup step: %w", err)
	}
	return nil
}

// DeclineEncryption keeps the time of the first decline, including on repeats.
func (s *sqliteStore) DeclineEncryption(ctx context.Context, at time.Time) (err error) {
	tx, err := s.beginAccessWrite(ctx, access.ModeSetup)
	if err != nil {
		return err
	}
	defer rollbackTransaction(&err, tx)
	if _, err := tx.ExecContext(ctx,
		`UPDATE access_state
		 SET encryption_declined_at = COALESCE(encryption_declined_at, ?)
		 WHERE singleton = 1`, timeStr(at),
	); err != nil {
		return fmt.Errorf("store: record encryption decline: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit encryption decline: %w", err)
	}
	return nil
}

// CompleteSetupOpen completes first-run setup without credentials.
func (s *sqliteStore) CompleteSetupOpen(
	ctx context.Context, completedAt time.Time,
) (err error) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	tx, err := s.beginAccessWrite(ctx, access.ModeSetup)
	if err != nil {
		return err
	}
	defer rollbackTransaction(&err, tx)
	if err := setAccessMode(ctx, tx, access.ModeOpen); err != nil {
		return err
	}
	return s.commitSetup(ctx, tx, completedAt)
}

// CompleteSetupToken completes setup with all credentials and the first session.
func (s *sqliteStore) CompleteSetupToken(
	ctx context.Context, credentials access.Credentials, session access.Session,
	completedAt time.Time,
) (err error) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	tx, err := s.beginAccessWrite(ctx, access.ModeSetup)
	if err != nil {
		return err
	}
	defer rollbackTransaction(&err, tx)
	if err := s.storeTokenMode(ctx, tx, credentials, session); err != nil {
		return err
	}
	return s.commitSetup(ctx, tx, completedAt)
}

func (s *sqliteStore) commitSetup(
	ctx context.Context, tx *sql.Tx, completedAt time.Time,
) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE access_state SET completed_at = ? WHERE singleton = 1`,
		timeStr(completedAt),
	); err != nil {
		return fmt.Errorf("store: record setup completion: %w", err)
	}
	return s.commitAccess(ctx, tx)
}

// PanelPassword returns the panel password's hash and when it was set. Open
// mode holds none, and the error wraps domain.ErrNotFound.
func (s *sqliteStore) PanelPassword(ctx context.Context) (access.Password, error) {
	db, err := s.liveDB()
	if err != nil {
		return access.Password{}, err
	}
	hash, setAt, err := s.readAccessCredential(ctx, db, panelPasswordKind)
	if err != nil {
		return access.Password{}, err
	}
	return access.Password{Hash: string(hash), SetAt: setAt}, nil
}

// EnableTokenMode stores the credentials and the session and sets the mode to
// token. It requires open mode.
func (s *sqliteStore) EnableTokenMode(
	ctx context.Context, credentials access.Credentials, session access.Session,
) (err error) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	tx, err := s.beginAccessWrite(ctx, access.ModeOpen)
	if err != nil {
		return err
	}
	defer rollbackTransaction(&err, tx)

	if err := s.storeTokenMode(ctx, tx, credentials, session); err != nil {
		return err
	}
	return s.commitAccess(ctx, tx)
}

// storeTokenMode writes token mode inside the caller's checked transaction.
func (s *sqliteStore) storeTokenMode(
	ctx context.Context, tx *sql.Tx,
	credentials access.Credentials, session access.Session,
) error {
	if err := s.putAccessCredential(
		ctx, tx, panelPasswordKind,
		[]byte(credentials.Password.Hash), credentials.Password.SetAt,
	); err != nil {
		return err
	}
	issuedAt := time.Now()
	if err := s.putAccessCredential(
		ctx, tx, apiTokenKind, []byte(credentials.APIToken), issuedAt,
	); err != nil {
		return err
	}
	if err := s.putAccessCredential(
		ctx, tx, agentTokenKind, []byte(credentials.AgentToken), issuedAt,
	); err != nil {
		return err
	}
	if err := storeSession(ctx, tx, session); err != nil {
		return err
	}
	return setAccessMode(ctx, tx, access.ModeToken)
}

// DisableTokenMode deletes the panel password, both tokens and every session
// and sets the mode to open. It requires token mode.
func (s *sqliteStore) DisableTokenMode(ctx context.Context) (err error) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	tx, err := s.beginAccessWrite(ctx, access.ModeToken)
	if err != nil {
		return err
	}
	defer rollbackTransaction(&err, tx)

	if _, err := tx.ExecContext(ctx, `DELETE FROM access_credential`); err != nil {
		return fmt.Errorf("store: delete access credentials: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM panel_session`); err != nil {
		return fmt.Errorf("store: delete panel sessions: %w", err)
	}
	if err := setAccessMode(ctx, tx, access.ModeOpen); err != nil {
		return err
	}
	return s.commitAccess(ctx, tx)
}

// ReplaceToken replaces the value of the token of kind. It requires token
// mode.
func (s *sqliteStore) ReplaceToken(
	ctx context.Context, kind access.TokenKind, value string,
) (err error) {
	var credentialKind string
	switch kind {
	case access.TokenAPI:
		credentialKind = apiTokenKind
	case access.TokenAgent:
		credentialKind = agentTokenKind
	default:
		// The kind is not echoed: a caller may have filled it with a token.
		return errors.New("store: unknown access token kind")
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	tx, err := s.beginAccessWrite(ctx, access.ModeToken)
	if err != nil {
		return err
	}
	defer rollbackTransaction(&err, tx)

	if err := s.putAccessCredential(
		ctx, tx, credentialKind, []byte(value), time.Now(),
	); err != nil {
		return err
	}
	return s.commitAccess(ctx, tx)
}

// ReplacePassword replaces the panel password and deletes every session
// except the one whose id hash is keep. It requires token mode.
func (s *sqliteStore) ReplacePassword(
	ctx context.Context, password access.Password, keep [sha256.Size]byte,
) (err error) {
	tx, err := s.beginAccessWrite(ctx, access.ModeToken)
	if err != nil {
		return err
	}
	defer rollbackTransaction(&err, tx)

	if err := s.putAccessCredential(
		ctx, tx, panelPasswordKind, []byte(password.Hash), password.SetAt,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(
		ctx, `DELETE FROM panel_session WHERE id_hash != ?`, keep[:],
	); err != nil {
		return fmt.Errorf("store: delete other panel sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit panel password replacement: %w", err)
	}
	return nil
}

// ResetPassword replaces the panel password, deletes every session and
// stores session. It requires token mode.
func (s *sqliteStore) ResetPassword(
	ctx context.Context, password access.Password, session access.Session,
) (err error) {
	tx, err := s.beginAccessWrite(ctx, access.ModeToken)
	if err != nil {
		return err
	}
	defer rollbackTransaction(&err, tx)

	if err := s.putAccessCredential(
		ctx, tx, panelPasswordKind, []byte(password.Hash), password.SetAt,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM panel_session`); err != nil {
		return fmt.Errorf("store: delete panel sessions: %w", err)
	}
	if err := storeSession(ctx, tx, session); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit panel password reset: %w", err)
	}
	return nil
}

// CreateSession stores session. It requires token mode.
func (s *sqliteStore) CreateSession(
	ctx context.Context, session access.Session,
) (err error) {
	tx, err := s.beginAccessWrite(ctx, access.ModeToken)
	if err != nil {
		return err
	}
	defer rollbackTransaction(&err, tx)

	if err := storeSession(ctx, tx, session); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit panel session: %w", err)
	}
	return nil
}

// Session returns the session whose id hash is idHash. A missing session
// wraps domain.ErrNotFound.
func (s *sqliteStore) Session(
	ctx context.Context, idHash [sha256.Size]byte,
) (access.Session, error) {
	db, err := s.liveDB()
	if err != nil {
		return access.Session{}, err
	}
	var createdAt, lastSeenAt, expiresAt string
	err = db.QueryRowContext(
		ctx,
		`SELECT created_at, last_seen_at, expires_at FROM panel_session
		 WHERE id_hash = ?`,
		idHash[:],
	).Scan(&createdAt, &lastSeenAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return access.Session{}, fmt.Errorf("panel session: %w", domain.ErrNotFound)
	}
	if err != nil {
		return access.Session{}, fmt.Errorf("store: read panel session: %w", err)
	}
	session := access.Session{IDHash: idHash}
	if session.CreatedAt, err = parseStoredTime(
		"panel_session.created_at", createdAt,
	); err != nil {
		return access.Session{}, err
	}
	if session.LastSeenAt, err = parseStoredTime(
		"panel_session.last_seen_at", lastSeenAt,
	); err != nil {
		return access.Session{}, err
	}
	if session.ExpiresAt, err = parseStoredTime(
		"panel_session.expires_at", expiresAt,
	); err != nil {
		return access.Session{}, err
	}
	return session, nil
}

// TouchSession sets the last-seen time of the session whose id hash is
// idHash. A missing session wraps domain.ErrNotFound.
func (s *sqliteStore) TouchSession(
	ctx context.Context, idHash [sha256.Size]byte, lastSeenAt time.Time,
) error {
	db, err := s.liveDB()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(
		ctx, `UPDATE panel_session SET last_seen_at = ? WHERE id_hash = ?`,
		timeStr(lastSeenAt), idHash[:],
	)
	if err != nil {
		return fmt.Errorf("store: touch panel session: %w", err)
	}
	touched, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: count touched panel sessions: %w", err)
	}
	if touched == 0 {
		return fmt.Errorf("panel session: %w", domain.ErrNotFound)
	}
	return nil
}

// DeleteSession deletes the session whose id hash is idHash. A missing session
// is not an error.
func (s *sqliteStore) DeleteSession(
	ctx context.Context, idHash [sha256.Size]byte,
) error {
	db, err := s.liveDB()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(
		ctx, `DELETE FROM panel_session WHERE id_hash = ?`, idHash[:],
	); err != nil {
		return fmt.Errorf("store: delete panel session: %w", err)
	}
	return nil
}

// DeleteSessions deletes every session.
func (s *sqliteStore) DeleteSessions(ctx context.Context) error {
	db, err := s.liveDB()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM panel_session`); err != nil {
		return fmt.Errorf("store: delete panel sessions: %w", err)
	}
	return nil
}

// CountSessions counts the sessions that expire after expiresAfter and were
// last seen after seenAfter.
func (s *sqliteStore) CountSessions(
	ctx context.Context, expiresAfter, seenAfter time.Time,
) (int, error) {
	db, err := s.liveDB()
	if err != nil {
		return 0, err
	}
	var count int
	if err := db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM panel_session
		 WHERE expires_at > ? AND last_seen_at > ?`,
		timeStr(expiresAfter), timeStr(seenAfter),
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("store: count panel sessions: %w", err)
	}
	return count, nil
}

// beginAccessWrite begins a transaction on the live database and checks,
// inside it, that the stored access mode is want, so the write and its
// precondition see one state. When the mode differs it rolls the transaction
// back and returns an error wrapping domain.ErrSetupComplete when want is
// setup, and domain.ErrConflict otherwise.
func (s *sqliteStore) beginAccessWrite(
	ctx context.Context, want access.Mode,
) (*sql.Tx, error) {
	db, err := s.liveDB()
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin access write: %w", err)
	}
	mode, err := readAccessMode(ctx, tx)
	if err == nil && mode != want {
		cause := domain.ErrConflict
		if want == access.ModeSetup {
			cause = domain.ErrSetupComplete
		}
		err = fmt.Errorf(
			"store: access mode is %s, not %s: %w", mode, want, cause,
		)
	}
	if err != nil {
		rollbackTransaction(&err, tx)
		return nil, err
	}
	return tx, nil
}

// commitAccess commits tx and then publishes the access snapshot it leaves.
// The snapshot is read inside tx, so it is exactly what commits. The caller
// holds accessMu.
func (s *sqliteStore) commitAccess(ctx context.Context, tx *sql.Tx) error {
	snapshot, err := s.loadAccessSnapshot(ctx, tx)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit access state: %w", err)
	}
	s.accessSnapshot.Store(&snapshot)
	return nil
}

// loadAccessSnapshot reads the access mode and, in token mode, opens both
// tokens. A token-mode database missing a token is refused rather than read
// as one holding an empty token.
func (s *sqliteStore) loadAccessSnapshot(
	ctx context.Context, q sqlQueryer,
) (access.Snapshot, error) {
	mode, err := readAccessMode(ctx, q)
	if err != nil {
		return access.Snapshot{}, err
	}
	switch mode {
	case access.ModeSetup:
		return access.Snapshot{Mode: access.ModeSetup}, nil
	case access.ModeOpen:
		return access.Snapshot{Mode: access.ModeOpen}, nil
	case access.ModeToken:
	default:
		return access.Snapshot{}, fmt.Errorf("store: unknown access mode %q", mode)
	}
	apiToken, _, err := s.readAccessCredential(ctx, q, apiTokenKind)
	if err != nil {
		return access.Snapshot{}, err
	}
	agentToken, _, err := s.readAccessCredential(ctx, q, agentTokenKind)
	if err != nil {
		return access.Snapshot{}, err
	}
	return access.Snapshot{
		Mode:       access.ModeToken,
		APIToken:   string(apiToken),
		AgentToken: string(agentToken),
	}, nil
}

// readAccessMode returns the stored access mode, which the schema holds to
// setup, open or token. A database without the access state row is refused
// rather than given a mode.
func readAccessMode(ctx context.Context, q sqlQueryer) (access.Mode, error) {
	var mode string
	err := q.QueryRowContext(
		ctx, `SELECT mode FROM access_state WHERE singleton = 1`,
	).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New(
			"store: database holds no access state; recreate the database",
		)
	}
	if err != nil {
		return "", fmt.Errorf("store: read access mode: %w", err)
	}
	return access.Mode(mode), nil
}

func setAccessMode(ctx context.Context, tx *sql.Tx, mode access.Mode) error {
	if _, err := tx.ExecContext(
		ctx, `UPDATE access_state SET mode = ? WHERE singleton = 1`, string(mode),
	); err != nil {
		return fmt.Errorf("store: set access mode to %s: %w", mode, err)
	}
	return nil
}

// putAccessCredential stores value as the access credential of kind, sealed
// when the store has a master key. An empty value is refused: token mode
// holds all three credentials, and an empty token could match an empty guess.
// A token's issued_at is when the store writes it.
func (s *sqliteStore) putAccessCredential(
	ctx context.Context, tx *sql.Tx, kind string, value []byte, issuedAt time.Time,
) error {
	if len(value) == 0 {
		return fmt.Errorf(
			"store: access credential %s is empty: %w", kind, domain.ErrInvalid,
		)
	}
	stored, err := s.sealValue(
		accessCredentialTable, accessCredentialValueColumn, kind, value,
	)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO access_credential (kind, value, issued_at) VALUES (?, ?, ?)
		 ON CONFLICT (kind) DO UPDATE SET
		   value     = excluded.value,
		   issued_at = excluded.issued_at`,
		kind, stored, timeStr(issuedAt),
	); err != nil {
		return fmt.Errorf("store: write access credential %s: %w", kind, err)
	}
	return nil
}

// readAccessCredential returns the opened value of the access credential of
// kind and when it was issued. A missing credential wraps
// domain.ErrNotFound.
func (s *sqliteStore) readAccessCredential(
	ctx context.Context, q sqlQueryer, kind string,
) ([]byte, time.Time, error) {
	var stored []byte
	var issuedAt string
	err := q.QueryRowContext(
		ctx, `SELECT value, issued_at FROM access_credential WHERE kind = ?`, kind,
	).Scan(&stored, &issuedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, fmt.Errorf(
			"access credential %s: %w", kind, domain.ErrNotFound,
		)
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf(
			"store: read access credential %s: %w", kind, err,
		)
	}
	at, err := parseStoredTime("access_credential.issued_at", issuedAt)
	if err != nil {
		return nil, time.Time{}, err
	}
	value, err := s.openValue(
		accessCredentialTable, accessCredentialValueColumn, kind, stored,
	)
	if err != nil {
		return nil, time.Time{}, err
	}
	return value, at, nil
}

// storeSession stores session after deleting the sessions that expired by
// the time it was issued: a session that ended and is never presented again
// would otherwise stay forever.
func storeSession(ctx context.Context, tx *sql.Tx, session access.Session) error {
	if _, err := tx.ExecContext(
		ctx, `DELETE FROM panel_session WHERE expires_at <= ?`,
		timeStr(session.CreatedAt),
	); err != nil {
		return fmt.Errorf("store: delete expired panel sessions: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO panel_session (id_hash, created_at, last_seen_at, expires_at)
		 VALUES (?, ?, ?, ?)`,
		session.IDHash[:], timeStr(session.CreatedAt),
		timeStr(session.LastSeenAt), timeStr(session.ExpiresAt),
	); err != nil {
		return fmt.Errorf("store: insert panel session: %w", err)
	}
	return nil
}
