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

// Package access is Pit Officer's access policy. In setup mode first-run
// setup has not completed and only setup routes are available. Completing
// setup enters open or token mode. In open mode every surface admits a
// request without credentials. In token mode each authenticated
// surface admits only its own credential: the API token on the REST API, the
// agent token on MCP, and a session issued for the panel password on the
// panel API. The package signs the panel in and out, recovers a forgotten
// panel password with a one-time code printed to the process's standard
// error, shows and reissues the two tokens, and switches between the modes at
// runtime. Only recreating the database returns it to setup mode.
//
// Token mode holds exactly one panel password and two tokens, as three named
// fields. A token has no id, no name and no permissions, and nothing here
// extends to more tokens or more users.
package access

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

// Mode is the access mode: which credentials Officer's surfaces require.
type Mode string

const (
	// ModeSetup requires first-run setup, which completes into open or token
	// mode. Only recreating the database returns to setup.
	ModeSetup Mode = "setup"
	// ModeOpen admits every request without credentials, after completing
	// setup or switching from token mode. It can switch to token mode.
	ModeOpen Mode = "open"
	// ModeToken admits, on each authenticated surface, only that surface's
	// own credential, after completing setup or switching from open mode.
	// It can switch to open mode.
	ModeToken Mode = "token"
)

// SetupStep is the wizard's saved position, not a completed decision.
type SetupStep string

const (
	// SetupStepWelcome introduces Officer and its uses.
	SetupStepWelcome SetupStep = "welcome"
	// SetupStepAccounts shows the account mode choice.
	SetupStepAccounts SetupStep = "accounts"
	// SetupStepEncryption shows the master-key guide or encryption refusal.
	SetupStepEncryption SetupStep = "encryption"
	// SetupStepSigning shows signing keys and electronic signature settings.
	SetupStepSigning SetupStep = "signing"
	// SetupStepAccess shows token and password access or open access.
	SetupStepAccess SetupStep = "access"
)

// ParseSetupStep accepts only the five steps saved before setup completes.
// Any other value returns an error wrapping domain.ErrInvalid.
func ParseSetupStep(value string) (SetupStep, error) {
	switch step := SetupStep(value); step {
	case SetupStepWelcome, SetupStepAccounts, SetupStepEncryption,
		SetupStepSigning, SetupStepAccess:
		return step, nil
	default:
		return "", fmt.Errorf(
			"setup step must be %q, %q, %q, %q or %q: %w",
			SetupStepWelcome, SetupStepAccounts, SetupStepEncryption,
			SetupStepSigning, SetupStepAccess, domain.ErrInvalid,
		)
	}
}

// SetupState is the saved progress of first-run setup.
type SetupState struct {
	Step SetupStep
	// EncryptionDeclinedAt is nil until the operator declines encryption.
	EncryptionDeclinedAt *time.Time
}

// TokenKind names one of the two access tokens.
type TokenKind string

const (
	// TokenAPI is the API token, accepted on the REST API only. The panel
	// reaches the same access through a session issued for the panel
	// password.
	TokenAPI TokenKind = "api"
	// TokenAgent is the agent token, accepted on MCP only.
	TokenAgent TokenKind = "agent"
)

// errUnknownTokenKind refuses a token kind that is neither TokenAPI nor
// TokenAgent. It never echoes the value, which a caller may have filled with
// a token by mistake.
var errUnknownTokenKind = errors.New("access: unknown token kind")

// ParseTokenKind returns the token kind value names: "api" or "agent". Any
// other value names no token, and the error wraps domain.ErrNotFound.
func ParseTokenKind(value string) (TokenKind, error) {
	switch kind := TokenKind(value); kind {
	case TokenAPI, TokenAgent:
		return kind, nil
	default:
		return "", fmt.Errorf(
			"access token kind must be %q or %q: %w",
			TokenAPI, TokenAgent, domain.ErrNotFound,
		)
	}
}

// Snapshot is the access state the store last committed: the mode and the
// two token values. Both tokens are set in token mode and empty in setup
// and open modes.
type Snapshot struct {
	Mode       Mode
	APIToken   string
	AgentToken string
}

// token returns the value of the token of kind. A token-mode snapshot without
// that token breaks the store's contract, so the absence is an error rather
// than an empty token that could match an empty guess.
func (s Snapshot) token(kind TokenKind) (string, error) {
	var value string
	switch kind {
	case TokenAPI:
		value = s.APIToken
	case TokenAgent:
		value = s.AgentToken
	default:
		return "", errUnknownTokenKind
	}
	if value == "" {
		return "", fmt.Errorf("access: token mode holds no %s token", kind)
	}
	return value, nil
}

// Password is the panel password as the store holds it: its hash, never the
// password itself.
type Password struct {
	// Hash is the argon2id hash in PHC string form.
	Hash string
	// SetAt is when the password was set.
	SetAt time.Time
}

// Credentials are the three secrets token mode holds.
type Credentials struct {
	// Password is the panel password.
	Password Password
	// APIToken is the value of the API token.
	APIToken string
	// AgentToken is the value of the agent token.
	AgentToken string
}

// Session is a stored panel session. The raw session id lives only in the
// browser's cookie; the store keeps its SHA-256.
type Session struct {
	// IDHash is the SHA-256 of the 32-byte session id.
	IDHash [sha256.Size]byte
	// CreatedAt is when the session was issued.
	CreatedAt time.Time
	// LastSeenAt is when a request last presented the session. It is written
	// at most once an hour.
	LastSeenAt time.Time
	// ExpiresAt is the absolute end of the session, 30 days after it was
	// issued.
	ExpiresAt time.Time
}

// Store persists the access state: setup progress, the mode, the credentials
// of token mode and the panel sessions. It is safe for concurrent use.
//
// Each method that writes is one database transaction: it commits all of its
// effect or none. The store holds the mode and the two token values in
// memory as the Snapshot and publishes a new one only after the transaction
// that changed them commits - in these methods and when the store recreates
// its database - so Snapshot never returns a mode or a token the database
// does not hold.
//
// A write that requires a mode reads the stored mode inside its own
// transaction; when it differs, the write changes nothing and returns an
// error wrapping domain.ErrConflict. A method requiring setup mode instead
// wraps domain.ErrSetupComplete: the database leaves setup only by completing
// it. SetupState also reads and checks the mode in its own transaction.
// A missing session or password wraps domain.ErrNotFound.
// No error a method returns carries a token value, a password hash or any
// other stored credential value: it names the kind and wraps the cause.
type Store interface {
	// Snapshot returns the published mode and token values. It reads memory
	// only: no I/O and no error. Its Mode is ModeSetup, ModeOpen or ModeToken.
	Snapshot() Snapshot

	// SetupState returns the saved step and first encryption decline time.
	// It requires setup mode.
	SetupState(ctx context.Context) (SetupState, error)

	// SetSetupStep saves the wizard's position without enforcing step order.
	// Repeating the current step succeeds. It requires setup mode.
	SetSetupStep(ctx context.Context, step SetupStep) error

	// DeclineEncryption records at on the first decline. Repeating it keeps
	// the first time and succeeds. It requires setup mode.
	DeclineEncryption(ctx context.Context, at time.Time) error

	// CompleteSetupOpen enters open mode and records when setup completed.
	// It requires setup mode.
	CompleteSetupOpen(ctx context.Context, completedAt time.Time) error

	// CompleteSetupToken stores the credentials and session, enters token
	// mode and records when setup completed. It requires setup mode.
	CompleteSetupToken(
		ctx context.Context, credentials Credentials, session Session,
		completedAt time.Time,
	) error

	// PanelPassword returns the panel password's hash and when it was set.
	PanelPassword(ctx context.Context) (Password, error)

	// EnableTokenMode stores the credentials and the session and sets the
	// mode to token. It requires open mode.
	EnableTokenMode(
		ctx context.Context, credentials Credentials, session Session,
	) error

	// DisableTokenMode deletes the panel password, both tokens and every
	// session, and sets the mode to open. It requires token mode.
	DisableTokenMode(ctx context.Context) error

	// ReplaceToken replaces the value of the token of kind. It requires token
	// mode.
	ReplaceToken(ctx context.Context, kind TokenKind, value string) error

	// ReplacePassword replaces the panel password and deletes every session
	// except the one whose id hash is keep. It requires token mode.
	ReplacePassword(
		ctx context.Context, password Password, keep [sha256.Size]byte,
	) error

	// ResetPassword replaces the panel password, deletes every session and
	// stores session. It requires token mode.
	ResetPassword(
		ctx context.Context, password Password, session Session,
	) error

	// CreateSession stores session. It requires token mode.
	CreateSession(ctx context.Context, session Session) error

	// Session returns the session whose id hash is idHash.
	Session(ctx context.Context, idHash [sha256.Size]byte) (Session, error)

	// TouchSession sets the last-seen time of the session whose id hash is
	// idHash.
	TouchSession(
		ctx context.Context, idHash [sha256.Size]byte, lastSeenAt time.Time,
	) error

	// DeleteSession deletes the session whose id hash is idHash. A missing
	// session is not an error.
	DeleteSession(ctx context.Context, idHash [sha256.Size]byte) error

	// DeleteSessions deletes every session.
	DeleteSessions(ctx context.Context) error

	// CountSessions counts the sessions that expire after expiresAfter and
	// were last seen after seenAfter.
	CountSessions(
		ctx context.Context, expiresAfter, seenAfter time.Time,
	) (int, error)
}
