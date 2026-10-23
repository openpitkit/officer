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

// Access store tests: the access state of a clean database, every
// access.Store method against a real SQLite store, the publication of the
// access snapshot, sealing of the access credentials and the seal report,
// Reset, and the access tables' absence from backups.

package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/secret"
	"go.openpit.dev/officer/internal/access"
)

// The access secrets the tests store. They are fixtures, but no failure
// message prints them.
const (
	testPasswordHash  = "$argon2id$v=19$m=19456,t=2,p=1$Zml4dHVyZS1zYWx0$Zml4dHVyZS1oYXNo"
	testAPIToken      = "pit_api_fixture-first-api-token"
	testAgentToken    = "pit_mcp_fixture-first-agent-token"
	otherPasswordHash = "$argon2id$v=19$m=19456,t=2,p=1$b3RoZXItc2FsdA$b3RoZXItaGFzaA"
	otherAPIToken     = "pit_api_fixture-second-api-token"
	otherAgentToken   = "pit_mcp_fixture-second-agent-token"
)

var accessSecrets = []string{
	testPasswordHash, testAPIToken, testAgentToken,
	otherPasswordHash, otherAPIToken, otherAgentToken,
}

// accessTestStart is the time the tests issue sessions and passwords from.
var accessTestStart = time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC)

func TestAccessCleanDatabaseStartsSetup(t *testing.T) {
	ctx := context.Background()
	store := newAccessStore(t)

	assertSnapshot(t, store.Snapshot(), access.Snapshot{Mode: access.ModeSetup})
	assertStoredMode(t, store, access.ModeSetup)
	assertRowCount(t, store, "access_credential", 0)
	assertRowCount(t, store, "panel_session", 0)
	if _, err := store.PanelPassword(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("PanelPassword = %s, want ErrNotFound", redactAccess(t, err))
	}
}

func TestAccessMigrateRefusesDatabaseWithoutAccessState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	store := openSQLiteStoreForTest(t, path)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := store.currentDB().ExecContext(ctx, `DELETE FROM access_state`); err != nil {
		t.Fatalf("delete access state: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openSQLiteStoreForTest(t, path)
	err := reopened.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "holds no access state") {
		t.Fatalf("Migrate without the access state row = %v, want a refusal", err)
	}
	if reopened.accessSnapshot.Load() != nil {
		t.Fatal("a refused Migrate published an access snapshot")
	}
}

func TestAccessWritesRefuseInOpenModeAndChangeNothing(t *testing.T) {
	ctx := context.Background()
	store := newOpenAccessStore(t)
	session := testSession("open-mode-session", accessTestStart)
	password := access.Password{Hash: otherPasswordHash, SetAt: accessTestStart}

	for name, write := range map[string]func() error{
		"DisableTokenMode": func() error { return store.DisableTokenMode(ctx) },
		"ReplaceToken": func() error {
			return store.ReplaceToken(ctx, access.TokenAPI, otherAPIToken)
		},
		"ReplacePassword": func() error {
			return store.ReplacePassword(ctx, password, session.IDHash)
		},
		"ResetPassword": func() error { return store.ResetPassword(ctx, password, session) },
		"CreateSession": func() error { return store.CreateSession(ctx, session) },
	} {
		err := write()
		if !errors.Is(err, domain.ErrConflict) {
			t.Errorf("%s in open mode = %s, want ErrConflict", name, redactAccess(t, err))
		}
		assertNoAccessSecret(t, err)
	}
	assertSnapshot(t, store.Snapshot(), access.Snapshot{Mode: access.ModeOpen})
	assertStoredMode(t, store, access.ModeOpen)
	assertRowCount(t, store, "access_credential", 0)
	assertRowCount(t, store, "panel_session", 0)
}

func TestAccessEnableTokenModeRefusesInTokenModeAndChangesNothing(t *testing.T) {
	ctx := context.Background()
	store := newOpenAccessStore(t)
	enableTokenModeForTest(t, store, testSession("first-session", accessTestStart))

	second := testSession("second-session", accessTestStart.Add(time.Hour))
	err := store.EnableTokenMode(ctx, access.Credentials{
		Password:   access.Password{Hash: otherPasswordHash, SetAt: second.CreatedAt},
		APIToken:   otherAPIToken,
		AgentToken: otherAgentToken,
	}, second)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("EnableTokenMode in token mode = %s, want ErrConflict", redactAccess(t, err))
	}
	assertNoAccessSecret(t, err)
	// The refused write leaves the snapshot the last commit published.
	assertSnapshot(t, store.Snapshot(), fixtureTokenSnapshot())
	assertStoredPassword(t, store, testPasswordHash, accessTestStart)
	assertStoredCredential(t, store, apiTokenKind, testAPIToken)
	assertStoredCredential(t, store, agentTokenKind, testAgentToken)
	if _, err := store.Session(ctx, second.IDHash); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Session(refused session) = %s, want ErrNotFound", redactAccess(t, err))
	}
	assertRowCount(t, store, "panel_session", 1)
}

func TestAccessEnableTokenModeCommitsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	store := openSQLiteStoreForTest(t, path)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	session := testSession("enable-session", accessTestStart)
	if err := store.CompleteSetupOpen(ctx, accessTestStart); err != nil {
		t.Fatalf("CompleteSetupOpen: %s", redactAccess(t, err))
	}

	unblock := blockAccessModeChange(t, store)
	if err := store.EnableTokenMode(ctx, testCredentials(), session); err == nil {
		t.Fatal("EnableTokenMode with the mode change blocked returned nil")
	}
	assertSnapshot(t, store.Snapshot(), access.Snapshot{Mode: access.ModeOpen})
	assertStoredMode(t, store, access.ModeOpen)
	assertRowCount(t, store, "access_credential", 0)
	assertRowCount(t, store, "panel_session", 0)
	unblock()

	enableTokenModeForTest(t, store, session)
	assertSnapshot(t, store.Snapshot(), fixtureTokenSnapshot())
	assertStoredMode(t, store, access.ModeToken)
	assertStoredPassword(t, store, testPasswordHash, accessTestStart)
	assertStoredCredential(t, store, apiTokenKind, testAPIToken)
	assertStoredCredential(t, store, agentTokenKind, testAgentToken)
	got, err := store.Session(ctx, session.IDHash)
	if err != nil {
		t.Fatalf("Session: %s", redactAccess(t, err))
	}
	assertSessionEqual(t, got, session)

	// Migrate reads the snapshot of a token-mode database back.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened := openSQLiteStoreForTest(t, path)
	if err := reopened.Migrate(ctx); err != nil {
		t.Fatalf("Migrate token-mode database: %s", redactAccess(t, err))
	}
	assertSnapshot(t, reopened.Snapshot(), fixtureTokenSnapshot())
}

func TestAccessDisableTokenModeClearsCredentialsAndSessions(t *testing.T) {
	ctx := context.Background()
	store := newOpenAccessStore(t)
	enableTokenModeForTest(t, store, testSession("enable-session", accessTestStart))
	createSessionForTest(t, store, testSession("second-session", accessTestStart))

	unblock := blockAccessModeChange(t, store)
	if err := store.DisableTokenMode(ctx); err == nil {
		t.Fatal("DisableTokenMode with the mode change blocked returned nil")
	}
	assertSnapshot(t, store.Snapshot(), fixtureTokenSnapshot())
	assertStoredMode(t, store, access.ModeToken)
	assertRowCount(t, store, "access_credential", 3)
	assertRowCount(t, store, "panel_session", 2)
	unblock()

	if err := store.DisableTokenMode(ctx); err != nil {
		t.Fatalf("DisableTokenMode: %s", redactAccess(t, err))
	}
	assertSnapshot(t, store.Snapshot(), access.Snapshot{Mode: access.ModeOpen})
	assertStoredMode(t, store, access.ModeOpen)
	assertRowCount(t, store, "access_credential", 0)
	assertRowCount(t, store, "panel_session", 0)
	if _, err := store.PanelPassword(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("PanelPassword after DisableTokenMode = %s, want ErrNotFound", redactAccess(t, err))
	}
}

func TestAccessReplaceToken(t *testing.T) {
	ctx := context.Background()
	store := newOpenAccessStore(t)
	enableTokenModeForTest(t, store, testSession("enable-session", accessTestStart))

	if err := store.ReplaceToken(ctx, access.TokenAPI, otherAPIToken); err != nil {
		t.Fatalf("ReplaceToken(api): %s", redactAccess(t, err))
	}
	assertSnapshot(t, store.Snapshot(), access.Snapshot{
		Mode: access.ModeToken, APIToken: otherAPIToken, AgentToken: testAgentToken,
	})
	assertStoredCredential(t, store, apiTokenKind, otherAPIToken)
	assertStoredCredential(t, store, agentTokenKind, testAgentToken)

	if err := store.ReplaceToken(ctx, access.TokenAgent, otherAgentToken); err != nil {
		t.Fatalf("ReplaceToken(agent): %s", redactAccess(t, err))
	}
	replaced := access.Snapshot{
		Mode: access.ModeToken, APIToken: otherAPIToken, AgentToken: otherAgentToken,
	}
	assertSnapshot(t, store.Snapshot(), replaced)
	assertStoredCredential(t, store, apiTokenKind, otherAPIToken)
	assertStoredCredential(t, store, agentTokenKind, otherAgentToken)

	// Neither an unknown kind nor an empty value is stored, and a refusal
	// does not echo what it was given.
	err := store.ReplaceToken(ctx, access.TokenKind(testAPIToken), testAPIToken)
	if err == nil {
		t.Fatal("ReplaceToken(unknown kind) returned nil")
	}
	assertNoAccessSecret(t, err)
	if err := store.ReplaceToken(ctx, access.TokenAPI, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ReplaceToken(empty value) = %s, want ErrInvalid", redactAccess(t, err))
	}
	assertSnapshot(t, store.Snapshot(), replaced)
	assertStoredCredential(t, store, apiTokenKind, otherAPIToken)
}

func TestAccessReplacePasswordKeepsOnlyTheNamedSession(t *testing.T) {
	ctx := context.Background()
	store := newOpenAccessStore(t)
	first := testSession("first-session", accessTestStart)
	second := testSession("second-session", accessTestStart)
	third := testSession("third-session", accessTestStart)
	enableTokenModeForTest(t, store, first)
	createSessionForTest(t, store, second)
	createSessionForTest(t, store, third)

	setAt := accessTestStart.Add(time.Hour)
	if err := store.ReplacePassword(
		ctx, access.Password{Hash: otherPasswordHash, SetAt: setAt}, second.IDHash,
	); err != nil {
		t.Fatalf("ReplacePassword: %s", redactAccess(t, err))
	}
	assertStoredPassword(t, store, otherPasswordHash, setAt)
	if _, err := store.Session(ctx, second.IDHash); err != nil {
		t.Fatalf("Session(kept session): %s", redactAccess(t, err))
	}
	for _, ended := range []access.Session{first, third} {
		if _, err := store.Session(ctx, ended.IDHash); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Session(other session) = %s, want ErrNotFound", redactAccess(t, err))
		}
	}
	assertSnapshot(t, store.Snapshot(), fixtureTokenSnapshot())
}

func TestAccessResetPasswordReplacesHashAndEverySession(t *testing.T) {
	ctx := context.Background()
	store := newOpenAccessStore(t)
	first := testSession("first-session", accessTestStart)
	second := testSession("second-session", accessTestStart)
	enableTokenModeForTest(t, store, first)
	createSessionForTest(t, store, second)

	issued := testSession("recovered-session", accessTestStart.Add(time.Hour))
	if err := store.ResetPassword(
		ctx, access.Password{Hash: otherPasswordHash, SetAt: issued.CreatedAt}, issued,
	); err != nil {
		t.Fatalf("ResetPassword: %s", redactAccess(t, err))
	}
	assertStoredPassword(t, store, otherPasswordHash, issued.CreatedAt)
	for _, ended := range []access.Session{first, second} {
		if _, err := store.Session(ctx, ended.IDHash); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Session(earlier session) = %s, want ErrNotFound", redactAccess(t, err))
		}
	}
	got, err := store.Session(ctx, issued.IDHash)
	if err != nil {
		t.Fatalf("Session(issued session): %s", redactAccess(t, err))
	}
	assertSessionEqual(t, got, issued)
	assertRowCount(t, store, "panel_session", 1)
	assertSnapshot(t, store.Snapshot(), fixtureTokenSnapshot())
}

func TestAccessSessionsReadTouchAndDelete(t *testing.T) {
	ctx := context.Background()
	store := newOpenAccessStore(t)
	session := testSession("browser-session", accessTestStart)
	other := testSession("other-session", accessTestStart)
	enableTokenModeForTest(t, store, session)
	createSessionForTest(t, store, other)
	unknown := sha256.Sum256([]byte("unknown-session"))

	if _, err := store.Session(ctx, unknown); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Session(unknown) = %s, want ErrNotFound", redactAccess(t, err))
	}
	if err := store.TouchSession(ctx, unknown, accessTestStart); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("TouchSession(unknown) = %s, want ErrNotFound", redactAccess(t, err))
	}
	if err := store.DeleteSession(ctx, unknown); err != nil {
		t.Fatalf("DeleteSession(unknown) = %s, want nil", redactAccess(t, err))
	}

	seen := accessTestStart.Add(2 * time.Hour)
	if err := store.TouchSession(ctx, session.IDHash, seen); err != nil {
		t.Fatalf("TouchSession: %s", redactAccess(t, err))
	}
	got, err := store.Session(ctx, session.IDHash)
	if err != nil {
		t.Fatalf("Session: %s", redactAccess(t, err))
	}
	touched := session
	touched.LastSeenAt = seen
	assertSessionEqual(t, got, touched)

	if err := store.DeleteSession(ctx, session.IDHash); err != nil {
		t.Fatalf("DeleteSession: %s", redactAccess(t, err))
	}
	if _, err := store.Session(ctx, session.IDHash); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Session(deleted) = %s, want ErrNotFound", redactAccess(t, err))
	}
	if _, err := store.Session(ctx, other.IDHash); err != nil {
		t.Fatalf("Session(other) after DeleteSession: %s", redactAccess(t, err))
	}
	if err := store.DeleteSessions(ctx); err != nil {
		t.Fatalf("DeleteSessions: %s", redactAccess(t, err))
	}
	assertRowCount(t, store, "panel_session", 0)
}

func TestAccessCountSessionsFiltersByExpiryAndLastSeen(t *testing.T) {
	ctx := context.Background()
	store := newOpenAccessStore(t)
	session := func(name string, expiresIn, seenIn time.Duration) access.Session {
		return access.Session{
			IDHash:     sha256.Sum256([]byte(name)),
			CreatedAt:  accessTestStart,
			LastSeenAt: accessTestStart.Add(seenIn),
			ExpiresAt:  accessTestStart.Add(expiresIn),
		}
	}
	enableTokenModeForTest(t, store, session("enabling", 40*time.Hour, 0))
	for _, s := range []access.Session{
		session("live", 20*time.Hour, 3*time.Hour),
		session("expires-at-bound", 10*time.Hour, 3*time.Hour),
		session("seen-at-bound", 20*time.Hour, 2*time.Hour),
		session("expired", 5*time.Hour, 3*time.Hour),
		session("idle", 20*time.Hour, time.Hour),
		session("live-later", 30*time.Hour, 4*time.Hour),
	} {
		createSessionForTest(t, store, s)
	}

	count, err := store.CountSessions(
		ctx, accessTestStart.Add(10*time.Hour), accessTestStart.Add(2*time.Hour),
	)
	if err != nil {
		t.Fatalf("CountSessions: %v", err)
	}
	if count != 2 {
		t.Fatalf("CountSessions = %d, want 2", count)
	}
}

func TestAccessCreateSessionDeletesExpiredSessions(t *testing.T) {
	ctx := context.Background()
	store := newOpenAccessStore(t)
	session := func(name string, createdIn, expiresIn time.Duration) access.Session {
		createdAt := accessTestStart.Add(createdIn)
		return access.Session{
			IDHash:     sha256.Sum256([]byte(name)),
			CreatedAt:  createdAt,
			LastSeenAt: createdAt,
			ExpiresAt:  accessTestStart.Add(expiresIn),
		}
	}
	expired := session("expired", 0, time.Hour)
	expiresAtIssue := session("expires-at-issue", 0, 2*time.Hour)
	live := session("live", 0, 3*time.Hour)
	enableTokenModeForTest(t, store, expired)
	createSessionForTest(t, store, expiresAtIssue)
	createSessionForTest(t, store, live)

	issued := session("issued", 2*time.Hour, 50*time.Hour)
	createSessionForTest(t, store, issued)
	for _, ended := range []access.Session{expired, expiresAtIssue} {
		if _, err := store.Session(ctx, ended.IDHash); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Session(expired session) = %s, want ErrNotFound", redactAccess(t, err))
		}
	}
	for _, kept := range []access.Session{live, issued} {
		if _, err := store.Session(ctx, kept.IDHash); err != nil {
			t.Fatalf("Session(live session): %s", redactAccess(t, err))
		}
	}
}

func TestAccessCredentialsSealedOnlyWithMasterKey(t *testing.T) {
	for _, test := range []struct {
		name   string
		opts   []Option
		sealed bool
	}{
		{name: "plaintext"},
		{
			name:   "sealed",
			opts:   []Option{WithMasterKey(mustStoreMasterKey(t, 0x81))},
			sealed: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "officer.db")
			store := openSQLiteStoreForTest(t, path, test.opts...)
			if err := store.Migrate(ctx); err != nil {
				t.Fatalf("Migrate: %v", err)
			}
			if err := store.CompleteSetupOpen(ctx, accessTestStart); err != nil {
				t.Fatalf("CompleteSetupOpen: %s", redactAccess(t, err))
			}
			enableTokenModeForTest(t, store, testSession("enable-session", accessTestStart))

			for kind, plaintext := range fixtureCredentials() {
				stored := rawCredential(t, store, kind)
				if test.sealed && bytes.Contains(stored, []byte(plaintext)) {
					t.Errorf("sealed %s holds its plaintext", kind)
				}
				if !test.sealed && !bytes.Equal(stored, []byte(plaintext)) {
					t.Errorf("unsealed %s is not stored as its plaintext", kind)
				}
			}
			assertStoredPassword(t, store, testPasswordHash, accessTestStart)

			// A restart opens the stored tokens into its snapshot.
			if err := store.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			reopened := openSQLiteStoreForTest(t, path, test.opts...)
			if err := reopened.Migrate(ctx); err != nil {
				t.Fatalf("Migrate reopened store: %s", redactAccess(t, err))
			}
			assertSnapshot(t, reopened.Snapshot(), fixtureTokenSnapshot())
		})
	}
}

func TestAccessSealingExistingDatabaseSealsAndCountsAccessCredentials(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	plaintextStore := openSQLiteStoreForTest(t, path)
	if err := plaintextStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate plaintext store: %v", err)
	}
	if err := plaintextStore.CompleteSetupOpen(ctx, accessTestStart); err != nil {
		t.Fatalf("CompleteSetupOpen: %s", redactAccess(t, err))
	}
	enableTokenModeForTest(t, plaintextStore, testSession("enable-session", accessTestStart))
	realm, err := plaintextStore.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm: %v", err)
	}
	// Six stored secrets hold a value: the three access credentials, the
	// signing private material, and the market-data and trading credentials.
	seedClearSigningKey(t, ctx, realm, domain.SigningKey{
		CreatedAt: time.Now().UTC(),
		KeyID:     "clear-signing-key", Alg: "ed25519",
		PrivateKey: []byte("clear-signing-private"), PublicKey: []byte("public"),
	})
	if _, err := realm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO, Label: "credentialed-feed",
		Credentials: `{"token":"feed-secret"}`,
	}); err != nil {
		t.Fatalf("CreateMarketDataInstance(credentialed): %v", err)
	}
	if _, err := realm.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider:      domain.TradingProviderAlpaca,
		Label:         "credentialed-trading",
		VenueIdentity: "credentialed-account",
		Mode:          domain.TradingModePaper,
		Credentials:   `{"key":"trading-secret"}`,
	}); err != nil {
		t.Fatalf("CreateTradingConnection(credentialed): %v", err)
	}
	// Their empty forms are sealed as well but hold nothing.
	if err := realm.UpsertSigningKey(ctx, domain.SigningKey{
		CreatedAt: time.Now().UTC(),
		KeyID:     "verify-only-key", Alg: "ed25519", PublicKey: []byte("public"),
	}); err != nil {
		t.Fatalf("UpsertSigningKey(verify-only): %v", err)
	}
	if _, err := realm.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBYO, Label: "credential-free-feed",
	}); err != nil {
		t.Fatalf("CreateMarketDataInstance(credential-free): %v", err)
	}
	if _, err := realm.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider:      domain.TradingProviderAlpaca,
		Label:         "credential-free-trading",
		VenueIdentity: "credential-free-account",
		Mode:          domain.TradingModePaper,
		Credentials:   `{}`,
	}); err != nil {
		t.Fatalf("CreateTradingConnection(credential-free): %v", err)
	}
	if err := plaintextStore.Close(); err != nil {
		t.Fatalf("close plaintext store: %v", err)
	}

	logOutput := captureSlogForTest(t)
	sealedStore := openSQLiteStoreForTest(
		t, path, WithMasterKey(mustStoreMasterKey(t, 0x82)),
	)
	if err := sealedStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate sealed store: %s", redactAccess(t, err))
	}
	if !strings.Contains(logOutput.String(), sealingExposureWarning) {
		t.Fatal("sealing stored secrets did not log the exposure warning")
	}
	report, sealed, err := sealedStore.SealReport(ctx)
	if err != nil || !sealed {
		t.Fatalf("SealReport = sealed %v, err %v; want a report", sealed, err)
	}
	if report.ResealedSecrets != 6 {
		t.Fatalf("ResealedSecrets = %d, want 6", report.ResealedSecrets)
	}
	assertSealedAtMatchesSecretState(t, sealedStore, report)
	for kind, plaintext := range fixtureCredentials() {
		if bytes.Contains(rawCredential(t, sealedStore, kind), []byte(plaintext)) {
			t.Errorf("sealing left %s in the clear", kind)
		}
	}
	assertSnapshot(t, sealedStore.Snapshot(), fixtureTokenSnapshot())
	assertStoredPassword(t, sealedStore, testPasswordHash, accessTestStart)
}

func TestSealReportAbsentUntilSealed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	plaintextStore := openSQLiteStoreForTest(t, path)
	if err := plaintextStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate plaintext store: %v", err)
	}
	if _, sealed, err := plaintextStore.SealReport(ctx); err != nil || sealed {
		t.Fatalf("SealReport before sealing = sealed %v, err %v; want absent", sealed, err)
	}
	if err := plaintextStore.Close(); err != nil {
		t.Fatalf("close plaintext store: %v", err)
	}

	sealedStore := openSQLiteStoreForTest(
		t, path, WithMasterKey(mustStoreMasterKey(t, 0x83)),
	)
	if err := sealedStore.Migrate(ctx); err != nil {
		t.Fatalf("Migrate sealed store: %v", err)
	}
	report, sealed, err := sealedStore.SealReport(ctx)
	if err != nil || !sealed {
		t.Fatalf("SealReport after sealing = sealed %v, err %v; want a report", sealed, err)
	}
	if report.ResealedSecrets != 0 {
		t.Fatalf("ResealedSecrets = %d, want 0", report.ResealedSecrets)
	}
	assertSealedAtMatchesSecretState(t, sealedStore, report)
}

func TestAccessResetReturnsToSetupMode(t *testing.T) {
	ctx := context.Background()
	store := newOpenAccessStore(t)
	enableTokenModeForTest(t, store, testSession("enable-session", accessTestStart))
	createSessionForTest(t, store, testSession("second-session", accessTestStart))

	if err := store.Reset(ctx); err != nil {
		t.Fatalf("Reset: %s", redactAccess(t, err))
	}
	assertSnapshot(t, store.Snapshot(), access.Snapshot{Mode: access.ModeSetup})
	assertStoredMode(t, store, access.ModeSetup)
	assertRowCount(t, store, "access_credential", 0)
	assertRowCount(t, store, "panel_session", 0)
	if _, err := store.PanelPassword(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("PanelPassword after Reset = %s, want ErrNotFound", redactAccess(t, err))
	}
}

func TestAccessFailedResetKeepsSnapshotAndData(t *testing.T) {
	ctx := context.Background()
	store := newOpenAccessStore(t)
	session := testSession("enable-session", accessTestStart)
	enableTokenModeForTest(t, store, session)

	resetCtx, cancel := context.WithCancel(ctx)
	cancel()
	err := store.Reset(resetCtx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Reset canceled context = %s, want context.Canceled", redactAccess(t, err))
	}
	assertSnapshot(t, store.Snapshot(), fixtureTokenSnapshot())
	assertStoredMode(t, store, access.ModeToken)
	assertStoredPassword(t, store, testPasswordHash, accessTestStart)
	if _, err := store.Session(ctx, session.IDHash); err != nil {
		t.Fatalf("Session after a failed Reset: %s", redactAccess(t, err))
	}
}

// Each round races the writers that change the access snapshot against one
// Reset, and sees a write that does not hold accessMu in one of two ways. A
// write that took its connection before Reset renamed the fresh database over
// the old one, and writes after, is refused by SQLite (the file it opened has
// moved), so the round fails on that write error. Once all of them have
// returned, the published snapshot must be the access state the live database
// holds: a write that committed to the replaced database and published after
// the swap leaves a snapshot the database does not hold.
func TestAccessSnapshotMatchesDatabaseAfterConcurrentWritesAndReset(t *testing.T) {
	ctx := context.Background()
	const rounds = 20
	const writers = 3
	for _, operation := range []string{"mode switches", "complete open", "complete token"} {
		t.Run(operation, func(t *testing.T) {
			var store *sqliteStore
			if operation == "mode switches" {
				store = newAccessStore(t)
			}
			for round := range rounds {
				wantErr := domain.ErrConflict
				if operation == "mode switches" {
					if err := store.CompleteSetupOpen(ctx, accessTestStart); err != nil {
						t.Fatalf("round %d: CompleteSetupOpen: %s", round, redactAccess(t, err))
					}
				} else {
					store = newAccessStore(t)
					wantErr = domain.ErrSetupComplete
				}
				resetDone := make(chan struct{})
				writeErrs := make(chan error, writers)
				var wg sync.WaitGroup
				for writer := range writers {
					wg.Go(func() {
						for i := 0; ; i++ {
							select {
							case <-resetDone:
								return
							default:
							}
							// Every write stores tokens of its own, so a snapshot
							// published out of order differs from the stored one.
							name := fmt.Sprintf("race-%d-%d-%d", round, writer, i)
							var err error
							switch operation {
							case "complete open":
								err = store.CompleteSetupOpen(ctx, accessTestStart)
							case "complete token":
								err = store.CompleteSetupToken(ctx, access.Credentials{
									Password:   access.Password{Hash: testPasswordHash, SetAt: accessTestStart},
									APIToken:   "pit_api_" + name,
									AgentToken: "pit_mcp_" + name,
								}, testSession(name, accessTestStart), accessTestStart)
							case "mode switches":
								switch (writer + i) % 4 {
								case 0:
									err = store.EnableTokenMode(ctx, access.Credentials{
										Password:   access.Password{Hash: testPasswordHash, SetAt: accessTestStart},
										APIToken:   "pit_api_" + name,
										AgentToken: "pit_mcp_" + name,
									}, testSession(name, accessTestStart))
								case 1:
									err = store.ReplaceToken(ctx, access.TokenAPI, "pit_api_"+name)
								case 2:
									err = store.ReplaceToken(ctx, access.TokenAgent, "pit_mcp_"+name)
								case 3:
									err = store.DisableTokenMode(ctx)
								}
							}
							// A write whose mode precondition no longer holds is refused
							// and changes nothing. Any other error fails the round: it is
							// how a write that ran outside accessMu against the replaced
							// database shows.
							if err != nil && !errors.Is(err, wantErr) {
								writeErrs <- err
								return
							}
						}
					})
				}
				resetErr := store.Reset(ctx)
				close(resetDone)
				wg.Wait()
				close(writeErrs)
				if resetErr != nil {
					t.Fatalf("round %d: Reset: %s", round, redactAccess(t, resetErr))
				}
				for err := range writeErrs {
					t.Fatalf("round %d: access write beside Reset: %s", round, redactAccess(t, err))
				}
				stored, err := store.loadAccessSnapshot(ctx, store.currentDB())
				if err != nil {
					t.Fatalf("round %d: read the stored access state: %s", round, redactAccess(t, err))
				}
				assertSnapshot(t, store.Snapshot(), stored)
				if t.Failed() {
					t.Fatalf("round %d: the published access snapshot is not the stored one", round)
				}
			}
		})
	}
}

func TestAccessStateStaysOutOfBackups(t *testing.T) {
	ctx := context.Background()
	source := newOpenAccessStore(t)
	sourceRealm, err := source.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm(source): %v", err)
	}
	seedRealm(t, ctx, sourceRealm)
	sourceSession := testSession("source-session", accessTestStart)
	enableTokenModeForTest(t, source, sourceSession)
	archive, err := sourceRealm.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}
	encoded, err := json.Marshal(archive)
	if err != nil {
		t.Fatalf("encode archive: %v", err)
	}
	var document any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("decode archive: %v", err)
	}
	for _, secret := range accessSecrets {
		if jsonCarries(document, secret) {
			t.Error("the backup archive carries an access secret")
		}
	}

	key := mustStoreMasterKey(t, 0x84)
	path := filepath.Join(t.TempDir(), "officer.db")
	target := openSQLiteStoreForTest(t, path, WithMasterKey(key))
	if err := target.Migrate(ctx); err != nil {
		t.Fatalf("Migrate target: %v", err)
	}
	if err := target.CompleteSetupOpen(ctx, accessTestStart); err != nil {
		t.Fatalf("CompleteSetupOpen(target): %s", redactAccess(t, err))
	}
	targetSession := testSession("target-session", accessTestStart)
	if err := target.EnableTokenMode(ctx, access.Credentials{
		Password:   access.Password{Hash: otherPasswordHash, SetAt: accessTestStart},
		APIToken:   otherAPIToken,
		AgentToken: otherAgentToken,
	}, targetSession); err != nil {
		t.Fatalf("EnableTokenMode(target): %s", redactAccess(t, err))
	}
	reportBefore, sealed, err := target.SealReport(ctx)
	if err != nil || !sealed {
		t.Fatalf("SealReport(target) = sealed %v, err %v; want a report", sealed, err)
	}
	targetRealm, err := target.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm(target): %v", err)
	}
	if _, err := targetRealm.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll,
	}); err != nil {
		t.Fatalf("RestoreBackup: %s", redactAccess(t, err))
	}

	targetState := access.Snapshot{
		Mode: access.ModeToken, APIToken: otherAPIToken, AgentToken: otherAgentToken,
	}
	assertSnapshot(t, target.Snapshot(), targetState)
	assertStoredMode(t, target, access.ModeToken)
	assertStoredPassword(t, target, otherPasswordHash, accessTestStart)
	assertStoredCredential(t, target, apiTokenKind, otherAPIToken)
	assertStoredCredential(t, target, agentTokenKind, otherAgentToken)
	if _, err := target.Session(ctx, targetSession.IDHash); err != nil {
		t.Fatalf("Session(target session) after restore: %s", redactAccess(t, err))
	}
	if _, err := target.Session(ctx, sourceSession.IDHash); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Session(source session) after restore = %s, want ErrNotFound", redactAccess(t, err))
	}
	reportAfter, sealed, err := target.SealReport(ctx)
	if err != nil || !sealed {
		t.Fatalf("SealReport(target) after restore = sealed %v, err %v; want a report", sealed, err)
	}
	if reportAfter.ResealedSecrets != reportBefore.ResealedSecrets ||
		!reportAfter.SealedAt.Equal(reportBefore.SealedAt) {
		t.Fatalf("seal report after restore = %+v, want %+v", reportAfter, reportBefore)
	}

	// A restart reads the same access state back from the restored database.
	if err := target.Close(); err != nil {
		t.Fatalf("close target: %v", err)
	}
	reopened := openSQLiteStoreForTest(t, path, WithMasterKey(key))
	if err := reopened.Migrate(ctx); err != nil {
		t.Fatalf("Migrate restored target: %s", redactAccess(t, err))
	}
	assertSnapshot(t, reopened.Snapshot(), targetState)
}

func TestAccessSetupStateStartsAtWelcome(t *testing.T) {
	store := newAccessStore(t)
	state, err := store.SetupState(context.Background())
	assertNoAccessSecret(t, err)
	if err != nil {
		t.Fatalf("SetupState: %s", redactAccess(t, err))
	}
	if state.Step != access.SetupStepWelcome || state.EncryptionDeclinedAt != nil {
		t.Fatalf("initial setup state = %+v, want welcome without a decline", state)
	}
	assertStoredSetupState(t, store, state, nil)
}

func TestAccessSetSetupStepRoundTrips(t *testing.T) {
	ctx := context.Background()
	store := newAccessStore(t)
	for _, step := range []access.SetupStep{
		access.SetupStepWelcome, access.SetupStepAccounts,
		access.SetupStepEncryption, access.SetupStepSigning, access.SetupStepAccess,
	} {
		t.Run(string(step), func(t *testing.T) {
			for range 2 {
				err := store.SetSetupStep(ctx, step)
				assertNoAccessSecret(t, err)
				if err != nil {
					t.Fatalf("SetSetupStep(%s): %s", step, redactAccess(t, err))
				}
				state, err := store.SetupState(ctx)
				assertNoAccessSecret(t, err)
				if err != nil {
					t.Fatalf("SetupState: %s", redactAccess(t, err))
				}
				if state.Step != step || state.EncryptionDeclinedAt != nil {
					t.Fatalf("setup state = %+v, want %s without a decline", state, step)
				}
				assertStoredSetupState(t, store, state, nil)
			}
		})
	}
	assertSnapshot(t, store.Snapshot(), access.Snapshot{Mode: access.ModeSetup})
}

func TestAccessSetSetupStepRefusesUnknownStep(t *testing.T) {
	store := newAccessStore(t)
	err := store.SetSetupStep(context.Background(), access.SetupStep(testAPIToken))
	assertNoAccessSecret(t, err)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SetSetupStep(unknown) = %s, want ErrInvalid", redactAccess(t, err))
	}
	assertStoredSetupState(t, store, access.SetupState{Step: access.SetupStepWelcome}, nil)
}

func TestAccessDeclineEncryptionKeepsFirstTime(t *testing.T) {
	ctx := context.Background()
	store := newAccessStore(t)
	first := accessTestStart.Add(123456789 * time.Nanosecond).
		In(time.FixedZone("operator", 2*60*60))
	for _, at := range []time.Time{first, first.Add(time.Hour)} {
		err := store.DeclineEncryption(ctx, at)
		assertNoAccessSecret(t, err)
		if err != nil {
			t.Fatalf("DeclineEncryption: %s", redactAccess(t, err))
		}
		state, err := store.SetupState(ctx)
		assertNoAccessSecret(t, err)
		if err != nil {
			t.Fatalf("SetupState: %s", redactAccess(t, err))
		}
		if state.EncryptionDeclinedAt == nil || !state.EncryptionDeclinedAt.Equal(first) {
			t.Fatalf("encryption decline = %v, want first time %v", state.EncryptionDeclinedAt, first)
		}
		assertStoredSetupState(t, store, access.SetupState{
			Step: access.SetupStepWelcome, EncryptionDeclinedAt: &first,
		}, nil)
	}
}

func TestAccessCompleteSetupOpen(t *testing.T) {
	store := newAccessStore(t)
	completedAt := accessTestStart.Add(time.Hour + 123456789*time.Nanosecond)
	err := store.CompleteSetupOpen(context.Background(), completedAt)
	assertNoAccessSecret(t, err)
	if err != nil {
		t.Fatalf("CompleteSetupOpen: %s", redactAccess(t, err))
	}
	assertSnapshot(t, store.Snapshot(), access.Snapshot{Mode: access.ModeOpen})
	assertSetupMethodsRefuseAndChangeNothing(t, store, &completedAt)
}

func TestAccessCompleteSetupTokenStoresCredentialsAndSession(t *testing.T) {
	for _, test := range []struct {
		name   string
		opts   []Option
		sealed bool
	}{
		{name: "plaintext"},
		{
			name:   "sealed",
			opts:   []Option{WithMasterKey(mustStoreMasterKey(t, 0x81))},
			sealed: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newAccessStore(t, test.opts...)
			session := testSession("setup-session", accessTestStart)
			completedAt := accessTestStart.Add(time.Hour)
			err := store.CompleteSetupToken(context.Background(), testCredentials(), session, completedAt)
			assertNoAccessSecret(t, err)
			if err != nil {
				t.Fatalf("CompleteSetupToken: %s", redactAccess(t, err))
			}
			assertSnapshot(t, store.Snapshot(), fixtureTokenSnapshot())
			for kind, plaintext := range fixtureCredentials() {
				stored := rawCredential(t, store, kind)
				if test.sealed && bytes.Contains(stored, []byte(plaintext)) {
					t.Errorf("sealed %s holds its plaintext", kind)
				}
				if !test.sealed && !bytes.Equal(stored, []byte(plaintext)) {
					t.Errorf("unsealed %s is not stored as its plaintext", kind)
				}
			}
			assertSetupMethodsRefuseAndChangeNothing(t, store, &completedAt)
		})
	}
}

func TestAccessSetupMethodsRefuseInOpenAndTokenModesAndChangeNothing(t *testing.T) {
	for _, mode := range []access.Mode{access.ModeOpen, access.ModeToken} {
		t.Run(string(mode), func(t *testing.T) {
			store := newOpenAccessStore(t)
			if mode == access.ModeToken {
				enableTokenModeForTest(t, store, testSession("setup-session", accessTestStart))
			}
			assertSetupMethodsRefuseAndChangeNothing(t, store, &accessTestStart)
		})
	}
}

func TestAccessEnableTokenModeRefusesInSetupAndChangesNothing(t *testing.T) {
	store := newAccessStore(t)
	err := store.EnableTokenMode(context.Background(), testCredentials(),
		testSession("setup-session", accessTestStart))
	assertNoAccessSecret(t, err)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("EnableTokenMode in setup = %s, want ErrConflict", redactAccess(t, err))
	}
	assertSnapshot(t, store.Snapshot(), access.Snapshot{Mode: access.ModeSetup})
	assertStoredMode(t, store, access.ModeSetup)
	assertStoredSetupState(t, store, access.SetupState{Step: access.SetupStepWelcome}, nil)
	assertRowCount(t, store, "access_credential", 0)
	assertRowCount(t, store, "panel_session", 0)
}

func TestAccessCompleteSetupTokenCommitsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	store := newAccessStore(t)
	session := testSession("setup-session", accessTestStart)
	completedAt := accessTestStart.Add(time.Hour)
	unblock := blockAccessModeChange(t, store)
	err := store.CompleteSetupToken(ctx, testCredentials(), session, completedAt)
	assertNoAccessSecret(t, err)
	if err == nil {
		t.Fatal("CompleteSetupToken with the mode change blocked returned nil")
	}
	assertSnapshot(t, store.Snapshot(), access.Snapshot{Mode: access.ModeSetup})
	assertStoredMode(t, store, access.ModeSetup)
	assertStoredSetupState(t, store, access.SetupState{Step: access.SetupStepWelcome}, nil)
	assertRowCount(t, store, "access_credential", 0)
	assertRowCount(t, store, "panel_session", 0)
	unblock()

	err = store.CompleteSetupToken(ctx, testCredentials(), session, completedAt)
	assertNoAccessSecret(t, err)
	if err != nil {
		t.Fatalf("CompleteSetupToken after unblocking: %s", redactAccess(t, err))
	}
	assertSnapshot(t, store.Snapshot(), fixtureTokenSnapshot())
	assertSetupMethodsRefuseAndChangeNothing(t, store, &completedAt)
}

func assertStoredSetupState(
	t *testing.T, store *sqliteStore, want access.SetupState, completedAt *time.Time,
) {
	t.Helper()
	var step string
	var declined, completed sql.NullString
	err := store.currentDB().QueryRowContext(context.Background(),
		`SELECT setup_step, encryption_declined_at, completed_at
		 FROM access_state WHERE singleton = 1`,
	).Scan(&step, &declined, &completed)
	if err != nil {
		t.Fatalf("read stored setup state: %s", redactAccess(t, err))
	}
	if step != string(want.Step) {
		t.Errorf("stored setup step = %q, want %q", step, want.Step)
	}
	for name, value := range map[string]struct {
		got  sql.NullString
		want *time.Time
	}{
		"encryption_declined_at": {declined, want.EncryptionDeclinedAt},
		"completed_at":           {completed, completedAt},
	} {
		if value.want == nil {
			if value.got.Valid {
				t.Errorf("stored %s = %q, want NULL", name, value.got.String)
			}
		} else if !value.got.Valid || value.got.String != timeStr(*value.want) {
			t.Errorf("stored %s = %q, want %q", name, value.got.String, timeStr(*value.want))
		}
	}
}

func assertSetupMethodsRefuseAndChangeNothing(
	t *testing.T, store *sqliteStore, completedAt *time.Time,
) {
	t.Helper()
	ctx := context.Background()
	want := store.Snapshot()
	session := testSession("setup-session", accessTestStart)
	for name, call := range map[string]func() error{
		"SetupState": func() error {
			state, err := store.SetupState(ctx)
			if state.Step != "" || state.EncryptionDeclinedAt != nil {
				t.Error("refused SetupState returned progress")
			}
			return err
		},
		"SetSetupStep": func() error {
			return store.SetSetupStep(ctx, access.SetupStepSigning)
		},
		"DeclineEncryption": func() error {
			return store.DeclineEncryption(ctx, accessTestStart.Add(24*time.Hour))
		},
		"CompleteSetupOpen": func() error {
			return store.CompleteSetupOpen(ctx, accessTestStart.Add(24*time.Hour))
		},
		"CompleteSetupToken": func() error {
			return store.CompleteSetupToken(ctx, access.Credentials{
				Password: access.Password{Hash: otherPasswordHash, SetAt: session.CreatedAt},
				APIToken: otherAPIToken, AgentToken: otherAgentToken,
			}, testSession("refused-session", session.CreatedAt), session.CreatedAt)
		},
	} {
		err := call()
		assertNoAccessSecret(t, err)
		if !errors.Is(err, domain.ErrSetupComplete) {
			t.Errorf("%s in %s mode = %s, want ErrSetupComplete", name, want.Mode, redactAccess(t, err))
		}
		assertSnapshot(t, store.Snapshot(), want)
		assertStoredMode(t, store, want.Mode)
		assertStoredSetupState(t, store, access.SetupState{Step: access.SetupStepWelcome}, completedAt)
		if want.Mode == access.ModeToken {
			assertRowCount(t, store, "access_credential", 3)
			assertStoredPassword(t, store, testPasswordHash, accessTestStart)
			assertStoredCredential(t, store, apiTokenKind, testAPIToken)
			assertStoredCredential(t, store, agentTokenKind, testAgentToken)
			assertRowCount(t, store, "panel_session", 1)
			got, err := store.Session(ctx, session.IDHash)
			if err != nil {
				t.Fatalf("Session after refused setup call: %s", redactAccess(t, err))
			}
			assertSessionEqual(t, got, session)
		} else {
			assertRowCount(t, store, "access_credential", 0)
			assertRowCount(t, store, "panel_session", 0)
		}
	}
}

// newAccessStore opens a fresh migrated store in a temp file.
func newAccessStore(t *testing.T, opts ...Option) *sqliteStore {
	t.Helper()
	store := openSQLiteStoreForTest(t, filepath.Join(t.TempDir(), "officer.db"), opts...)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return store
}

func newOpenAccessStore(t *testing.T) *sqliteStore {
	t.Helper()
	store := newAccessStore(t)
	if err := store.CompleteSetupOpen(context.Background(), accessTestStart); err != nil {
		t.Fatalf("CompleteSetupOpen: %s", redactAccess(t, err))
	}
	return store
}

// testSession is a session issued at createdAt that expires 30 days later.
func testSession(name string, createdAt time.Time) access.Session {
	return access.Session{
		IDHash:     sha256.Sum256([]byte(name)),
		CreatedAt:  createdAt,
		LastSeenAt: createdAt,
		ExpiresAt:  createdAt.Add(30 * 24 * time.Hour),
	}
}

func testCredentials() access.Credentials {
	return access.Credentials{
		Password:   access.Password{Hash: testPasswordHash, SetAt: accessTestStart},
		APIToken:   testAPIToken,
		AgentToken: testAgentToken,
	}
}

// fixtureCredentials maps each access_credential kind to the value
// testCredentials gives it.
func fixtureCredentials() map[string]string {
	return map[string]string{
		panelPasswordKind: testPasswordHash,
		apiTokenKind:      testAPIToken,
		agentTokenKind:    testAgentToken,
	}
}

func fixtureTokenSnapshot() access.Snapshot {
	return access.Snapshot{
		Mode: access.ModeToken, APIToken: testAPIToken, AgentToken: testAgentToken,
	}
}

func enableTokenModeForTest(t *testing.T, store *sqliteStore, session access.Session) {
	t.Helper()
	if err := store.EnableTokenMode(
		context.Background(), testCredentials(), session,
	); err != nil {
		t.Fatalf("EnableTokenMode: %s", redactAccess(t, err))
	}
}

func createSessionForTest(t *testing.T, store *sqliteStore, session access.Session) {
	t.Helper()
	if err := store.CreateSession(context.Background(), session); err != nil {
		t.Fatalf("CreateSession: %s", redactAccess(t, err))
	}
}

// blockAccessModeChange makes every change of the stored access mode fail,
// so a write that reaches it must roll back everything it did before. The
// returned function lifts the block.
func blockAccessModeChange(t *testing.T, store *sqliteStore) func() {
	t.Helper()
	db := store.currentDB()
	if _, err := db.ExecContext(context.Background(), `
		CREATE TRIGGER block_access_mode_change
		BEFORE UPDATE OF mode ON access_state
		BEGIN
			SELECT RAISE(ABORT, 'access mode change blocked');
		END
	`); err != nil {
		t.Fatalf("create access mode trigger: %v", err)
	}
	return func() {
		t.Helper()
		if _, err := db.ExecContext(
			context.Background(), `DROP TRIGGER block_access_mode_change`,
		); err != nil {
			t.Fatalf("drop access mode trigger: %v", err)
		}
	}
}

// assertSnapshot fails unless got is want. It never prints a token.
func assertSnapshot(t *testing.T, got, want access.Snapshot) {
	t.Helper()
	if got.Mode != want.Mode {
		t.Errorf("snapshot mode = %q, want %q", got.Mode, want.Mode)
	}
	if got.APIToken != want.APIToken {
		t.Error("snapshot API token is not the expected one")
	}
	if got.AgentToken != want.AgentToken {
		t.Error("snapshot agent token is not the expected one")
	}
}

func assertStoredMode(t *testing.T, store *sqliteStore, want access.Mode) {
	t.Helper()
	var mode string
	if err := store.currentDB().QueryRowContext(
		context.Background(), `SELECT mode FROM access_state WHERE singleton = 1`,
	).Scan(&mode); err != nil {
		t.Fatalf("read stored access mode: %v", err)
	}
	if access.Mode(mode) != want {
		t.Fatalf("stored access mode = %q, want %q", mode, want)
	}
}

func assertRowCount(t *testing.T, store *sqliteStore, table string, want int) {
	t.Helper()
	var count int
	if err := store.currentDB().QueryRowContext(
		context.Background(), `SELECT COUNT(*) FROM `+table,
	).Scan(&count); err != nil {
		t.Fatalf("count %s rows: %v", table, err)
	}
	if count != want {
		t.Fatalf("%s rows = %d, want %d", table, count, want)
	}
}

// assertStoredPassword fails unless the store holds the panel password hash
// set at setAt. It never prints the hash.
func assertStoredPassword(
	t *testing.T, store *sqliteStore, hash string, setAt time.Time,
) {
	t.Helper()
	got, err := store.PanelPassword(context.Background())
	if err != nil {
		t.Fatalf("PanelPassword: %s", redactAccess(t, err))
	}
	if got.Hash != hash {
		t.Error("stored panel password hash is not the expected one")
	}
	if !got.SetAt.Equal(setAt) {
		t.Errorf("panel password SetAt = %v, want %v", got.SetAt, setAt)
	}
}

// assertStoredCredential fails unless the access credential of kind opens to
// want. It never prints the value.
func assertStoredCredential(t *testing.T, store *sqliteStore, kind, want string) {
	t.Helper()
	got, _, err := store.readAccessCredential(
		context.Background(), store.currentDB(), kind,
	)
	if err != nil {
		t.Fatalf("read access credential %s: %s", kind, redactAccess(t, err))
	}
	if string(got) != want {
		t.Errorf("stored %s is not the expected one", kind)
	}
}

// rawCredential returns the access credential of kind as the database holds
// it, sealed or not.
func rawCredential(t *testing.T, store *sqliteStore, kind string) []byte {
	t.Helper()
	var stored []byte
	if err := store.currentDB().QueryRowContext(
		context.Background(),
		`SELECT value FROM access_credential WHERE kind = ?`, kind,
	).Scan(&stored); err != nil {
		t.Fatalf("read stored %s: %v", kind, err)
	}
	return stored
}

func assertSessionEqual(t *testing.T, got, want access.Session) {
	t.Helper()
	if got.IDHash != want.IDHash {
		t.Error("session id hash is not the expected one")
	}
	if !got.CreatedAt.Equal(want.CreatedAt) ||
		!got.LastSeenAt.Equal(want.LastSeenAt) ||
		!got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf(
			"session times = created %v, seen %v, expires %v; want %v, %v, %v",
			got.CreatedAt, got.LastSeenAt, got.ExpiresAt,
			want.CreatedAt, want.LastSeenAt, want.ExpiresAt,
		)
	}
}

// assertSealedAtMatchesSecretState fails unless report was recorded at the
// time the database was sealed.
func assertSealedAtMatchesSecretState(t *testing.T, store *sqliteStore, report SealReport) {
	t.Helper()
	var sealedAt string
	if err := store.currentDB().QueryRowContext(
		context.Background(), `SELECT sealed_at FROM secret_state WHERE singleton = 1`,
	).Scan(&sealedAt); err != nil {
		t.Fatalf("read secret_state sealed_at: %v", err)
	}
	want, err := time.Parse(time.RFC3339Nano, sealedAt)
	if err != nil {
		t.Fatalf("parse secret_state sealed_at: %v", err)
	}
	if report.SealedAt.IsZero() || !report.SealedAt.Equal(want) {
		t.Fatalf("seal report SealedAt = %v, want %v", report.SealedAt, want)
	}
}

// jsonCarries reports whether a decoded JSON value carries secret, as text or
// inside a base64 string, the form encoding/json gives a []byte field. Numbers,
// booleans and null cannot carry it.
func jsonCarries(value any, secret string) bool {
	switch value := value.(type) {
	case string:
		if strings.Contains(value, secret) {
			return true
		}
		decoded, err := base64.StdEncoding.DecodeString(value)
		return err == nil && bytes.Contains(decoded, []byte(secret))
	case []any:
		for _, item := range value {
			if jsonCarries(item, secret) {
				return true
			}
		}
	case map[string]any:
		for key, item := range value {
			if strings.Contains(key, secret) || jsonCarries(item, secret) {
				return true
			}
		}
	}
	return false
}

// redactAccess returns err's text with every fixture secret replaced, for a
// failure message that must not print one.
func redactAccess(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return "<nil>"
	}
	credentials, encodeErr := json.Marshal(map[string][]string{"values": accessSecrets})
	if encodeErr != nil {
		t.Fatal("cannot encode fixture credentials for redaction")
	}
	_, replacer, redactErr := secret.CredentialRedactor(string(credentials))
	if redactErr != nil {
		t.Fatal("cannot build fixture credential redactor")
	}
	return replacer.Replace(err.Error())
}

func TestRedactAccessErrorDiagnostics(t *testing.T) {
	if redactAccess(t, nil) != "<nil>" {
		t.Fatal("nil error diagnostics changed")
	}
	if redactAccess(t, errors.New("store unavailable")) != "store unavailable" {
		t.Fatal("ordinary error diagnostics changed")
	}
	for _, value := range accessSecrets {
		for _, spelling := range []string{value, url.QueryEscape(value)} {
			if redactAccess(t, errors.New(spelling)) != "[redacted]" {
				t.Fatal("error diagnostics retained a fixture credential")
			}
		}
	}
}

// assertNoAccessSecret fails when err carries a fixture secret, without
// printing it.
func assertNoAccessSecret(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, secret := range accessSecrets {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("an access store error carries a secret: %s", redactAccess(t, err))
		}
	}
}
