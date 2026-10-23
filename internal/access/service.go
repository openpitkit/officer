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

package access

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/backoff"
)

const (
	// sessionLifetime ends a session this long after its sign-in, whatever
	// its activity.
	sessionLifetime = 30 * 24 * time.Hour
	// sessionIdleTimeout ends a session that no request presented for this
	// long.
	sessionIdleTimeout = 7 * 24 * time.Hour
	// sessionTouchInterval spaces the writes of a session's last-seen time.
	sessionTouchInterval = time.Hour

	// After a failed sign-in the next one is accepted no earlier than a delay
	// that doubles from signInMinDelay up to signInMaxDelay. There is no
	// lockout: any local program can fail sign-ins, and a lockout would make
	// that denial of service permanent.
	signInMinDelay = time.Second
	signInMaxDelay = 30 * time.Second

	// recoveryCodeTTL is how long a recovery code is valid.
	recoveryCodeTTL = 15 * time.Minute
	// recoveryIssueInterval spaces new recovery codes, so a loop of requests
	// and wrong guesses cannot flood a container log that is not rotated.
	recoveryIssueInterval = time.Minute
	// recoveryMaxFailures wrong codes burn the live recovery code.
	recoveryMaxFailures = 5
)

// The refusals of a valid token on the other token's surface. A client sees
// their full text, so it names where the token is accepted and never carries
// the token.
var (
	errAgentTokenOnAPI = fmt.Errorf(
		"this is the agent token; it is accepted only on /mcp: %w",
		domain.ErrWrongToken,
	)
	errAPITokenOnMCP = fmt.Errorf(
		"this is the API token; it is accepted only on /api/v1: %w",
		domain.ErrWrongToken,
	)
)

var (
	errTokenNotAccepted  = fmt.Errorf("token not accepted: %w", domain.ErrUnauthorized)
	errNoSession         = fmt.Errorf("no session: %w", domain.ErrUnauthorized)
	errSessionEnded      = fmt.Errorf("session ended: %w", domain.ErrUnauthorized)
	errNoRecoveryCode    = fmt.Errorf("no live recovery code: %w", domain.ErrInvalidRecoveryCode)
	errWrongRecoveryCode = fmt.Errorf("wrong recovery code: %w", domain.ErrInvalidRecoveryCode)
)

// publicCaller is the caller password recovery, on the public panel routes,
// audits as: it runs before any session exists, so no caller is stamped in
// its context. Sign-in, the other public operation, writes no audit row.
var publicCaller = domain.Caller{
	Principal: domain.PrincipalOperator, Source: domain.SourcePanel,
}

// Auditor appends one audit row stamped with the caller. node.Node satisfies
// it.
type Auditor interface {
	AppendAudit(
		ctx context.Context, entry store.AuditEntry, caller domain.Caller,
	) error
}

// RetryAfterError is a refused sign-in or recovery request that carries the
// wait until the next attempt is accepted. It wraps domain.ErrTooManyAttempts
// or domain.ErrInvalidPassword.
type RetryAfterError struct {
	// Err is the refusal.
	Err error
	// After is the wait until the next attempt is accepted.
	After time.Duration
}

// Error returns the refusal and the wait.
func (e RetryAfterError) Error() string {
	return fmt.Sprintf("%v; retry after %v", e.Err, e.After)
}

// Unwrap returns the refusal.
func (e RetryAfterError) Unwrap() error { return e.Err }

// Status is the access state the panel reads before it renders.
type Status struct {
	// Mode is the access mode.
	Mode Mode
	// SetupComplete is false exactly in setup mode.
	SetupComplete bool
	// Authenticated reports whether the request's session cookie names a
	// live session. It is always false in setup and open modes.
	Authenticated bool
}

// AccessInfo is what the panel's security page shows about access.
type AccessInfo struct {
	// Mode is the access mode.
	Mode Mode
	// PasswordSetAt is when the panel password was set. It is nil in open
	// mode, which holds no password.
	PasswordSetAt *time.Time
	// Sessions is the number of live sessions.
	Sessions int
}

// Service applies the access policy over a Store. It is safe for concurrent
// use.
//
// One mutex serialises every operation that changes access state: sign-in,
// which verifies the password and creates the session under it, sign-out,
// the mode switches, the password change, the recovery code and its reset,
// token regeneration, SetSetupStep, DeclineEncryption and CompleteSetup.
// Without it a sign-in could verify the old password, lose the race to a
// password change that deletes every session, and then create a session for
// the old password. The credential checks of ordinary requests, CheckToken
// and CheckSession, take no lock.
//
// An operation reached through an authenticated panel route audits as the
// caller stamped in its context, and fails as an internal error when there
// is none. CompleteSetup also audits as the caller stamped by the resolver
// on its credential-less setup route. Password recovery runs on the public
// panel routes before any session exists and audits as the operator on the
// panel; sign-in writes no audit row. An audit row is appended after the
// change commits; when appending fails, the operation returns that error and
// the change stays applied.
//
// No password, token, session id or recovery code reaches an audit row or an
// error.
type Service struct {
	store       Store
	auditor     Auditor
	recoveryOut io.Writer
	decisions   SetupDecisions
	now         func() time.Time

	mu sync.Mutex
	// signInFailures counts the failed sign-ins since the last success, and
	// nextSignIn is the earliest time the next sign-in is accepted.
	signInFailures int
	nextSignIn     time.Time
	// recovery is the one recovery code, held in memory only.
	recovery recoveryCode
}

// recoveryCode is the state of the one recovery code. code is empty when no
// code is held.
type recoveryCode struct {
	code      string
	expiresAt time.Time
	failures  int
	// issuedAt is when the last code was issued. It outlives a used or burnt
	// code, so that the next code waits out the minute after it.
	issuedAt time.Time
}

// live reports whether the code is held and still valid at now.
func (r recoveryCode) live(now time.Time) bool {
	return r.code != "" && now.Before(r.expiresAt)
}

// New returns the Service over accessStore. auditor records the access
// changes; recoveryOut receives the recovery code line, and the composition
// passes the process's standard error, which bypasses the log buffer the
// service log route serves. decisions reads the setup choices owned by other
// components. A nil dependency is an error.
func New(
	accessStore Store, auditor Auditor, recoveryOut io.Writer,
	decisions SetupDecisions,
) (*Service, error) {
	switch {
	case accessStore == nil:
		return nil, errors.New("access: store is nil")
	case auditor == nil:
		return nil, errors.New("access: auditor is nil")
	case recoveryOut == nil:
		return nil, errors.New("access: recovery output is nil")
	case decisions == nil:
		return nil, errors.New("access: setup decisions are nil")
	}
	return &Service{
		store:       accessStore,
		auditor:     auditor,
		recoveryOut: recoveryOut,
		decisions:   decisions,
		now:         time.Now,
	}, nil
}

// Mode returns the published access mode.
func (s *Service) Mode() Mode {
	return s.store.Snapshot().Mode
}

// CheckToken checks a bearer token presented on the surface that accepts the
// token of kind. It returns nil for that token; an error wrapping
// domain.ErrWrongToken, which names the surface the token is accepted on, for
// the other kind's token; and an error wrapping domain.ErrUnauthorized for
// anything else, an empty token included. It compares the presented token
// with both tokens in constant time. Open mode issues no token, so there
// every token is refused as unauthorized. Setup returns the bare
// domain.ErrSetupRequired.
func (s *Service) CheckToken(kind TokenKind, presented string) error {
	snapshot := s.store.Snapshot()
	switch snapshot.Mode {
	case ModeSetup:
		return domain.ErrSetupRequired
	case ModeToken:
	case ModeOpen:
		return fmt.Errorf(
			"the access mode is %s, which issues no token: %w",
			snapshot.Mode, domain.ErrUnauthorized,
		)
	default:
		return fmt.Errorf("access: unknown access mode %q", snapshot.Mode)
	}
	var (
		other TokenKind
		wrong error
	)
	switch kind {
	case TokenAPI:
		other, wrong = TokenAgent, errAgentTokenOnAPI
	case TokenAgent:
		other, wrong = TokenAPI, errAPITokenOnMCP
	default:
		return errUnknownTokenKind
	}
	own, err := snapshot.token(kind)
	if err != nil {
		return err
	}
	foreign, err := snapshot.token(other)
	if err != nil {
		return err
	}
	// Both comparisons run whatever the first one finds, so the time taken
	// does not tell which token a guess matched.
	matchesOwn := secretsEqual(presented, own)
	matchesForeign := secretsEqual(presented, foreign)
	switch {
	case matchesOwn:
		return nil
	case matchesForeign:
		return wrong
	default:
		return errTokenNotAccepted
	}
}

// CheckSession checks the session id a panel request's cookie carries. It
// returns nil for a live session and records the session's last-seen time
// when the recorded one is more than an hour old. It returns an error
// wrapping domain.ErrUnauthorized for a value that names no session, for an
// unknown session and for an ended one, whose row it deletes. A session is
// live until 30 days after its sign-in and until 7 days pass without a
// request. Open mode issues no session, so there every value is refused as
// unauthorized. Setup returns the bare domain.ErrSetupRequired.
func (s *Service) CheckSession(ctx context.Context, cookie string) error {
	switch mode := s.Mode(); mode {
	case ModeSetup:
		return domain.ErrSetupRequired
	case ModeToken:
	case ModeOpen:
		return fmt.Errorf(
			"the access mode is %s, which issues no session: %w",
			mode, domain.ErrUnauthorized,
		)
	default:
		return fmt.Errorf("access: unknown access mode %q", mode)
	}
	idHash, ok := sessionIDHash(cookie)
	if !ok {
		return errNoSession
	}
	session, err := s.store.Session(ctx, idHash)
	if errors.Is(err, domain.ErrNotFound) {
		return errNoSession
	}
	if err != nil {
		return fmt.Errorf("access: read session: %w", err)
	}
	now := s.now()
	live := now.Before(session.ExpiresAt) &&
		now.Before(session.LastSeenAt.Add(sessionIdleTimeout))
	if !live {
		if err := s.store.DeleteSession(ctx, idHash); err != nil {
			return fmt.Errorf("access: delete ended session: %w", err)
		}
		return errSessionEnded
	}
	if now.Sub(session.LastSeenAt) > sessionTouchInterval {
		err := s.store.TouchSession(ctx, idHash, now)
		if errors.Is(err, domain.ErrNotFound) {
			// A sign-out or a password change deleted the session after it
			// was read.
			return errNoSession
		}
		if err != nil {
			return fmt.Errorf("access: record session activity: %w", err)
		}
	}
	return nil
}

// Status returns the access state for a request whose session cookie carries
// cookie, empty when it carries none.
func (s *Service) Status(ctx context.Context, cookie string) (Status, error) {
	status := Status{Mode: s.Mode()}
	switch status.Mode {
	case ModeSetup:
		return status, nil
	case ModeOpen:
		status.SetupComplete = true
		return status, nil
	case ModeToken:
		status.SetupComplete = true
	default:
		return Status{}, fmt.Errorf("access: unknown access mode %q", status.Mode)
	}
	err := s.CheckSession(ctx, cookie)
	switch {
	case err == nil:
		status.Authenticated = true
	case !errors.Is(err, domain.ErrUnauthorized):
		return Status{}, err
	}
	return status, nil
}

// SignIn checks the panel password and issues a session, returning its raw id
// for the session cookie.
//
// The delay after a failed sign-in is process-wide, because every client
// comes from loopback and nothing tells them apart: after each failure the
// next attempt is accepted no earlier than a delay that doubles from 1 s up
// to 30 s. An attempt during the delay returns a RetryAfterError wrapping
// domain.ErrTooManyAttempts with the time left; a wrong password returns a
// RetryAfterError wrapping domain.ErrInvalidPassword with the new delay. A
// success clears the delay. Open mode holds no password, so there the error
// wraps domain.ErrConflict. Setup returns the bare domain.ErrSetupRequired.
func (s *Service) SignIn(ctx context.Context, password string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := requireMode(s.store.Snapshot().Mode, ModeToken); err != nil {
		return "", err
	}
	now := s.now()
	if now.Before(s.nextSignIn) {
		return "", RetryAfterError{
			Err: domain.ErrTooManyAttempts, After: s.nextSignIn.Sub(now),
		}
	}
	stored, err := s.storedPassword(ctx)
	if err != nil {
		return "", err
	}
	matches, err := verifyPassword(stored.Hash, password)
	if err != nil {
		return "", err
	}
	if !matches {
		s.signInFailures++
		delay := backoff.Delay(s.signInFailures, signInMinDelay, signInMaxDelay)
		s.nextSignIn = now.Add(delay)
		return "", RetryAfterError{Err: domain.ErrInvalidPassword, After: delay}
	}
	s.signInFailures = 0
	s.nextSignIn = time.Time{}
	id, session, err := newSession(now)
	if err != nil {
		return "", err
	}
	if err := s.store.CreateSession(ctx, session); err != nil {
		return "", fmt.Errorf("access: create session: %w", err)
	}
	return id, nil
}

// SignOut ends the session the cookie names. It is idempotent: a value that
// names no session, or names one already ended, is not an error.
func (s *Service) SignOut(ctx context.Context, cookie string) error {
	idHash, ok := sessionIDHash(cookie)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.DeleteSession(ctx, idHash); err != nil {
		return fmt.Errorf("access: delete session: %w", err)
	}
	return nil
}

// SignOutEverywhere ends every session.
func (s *Service) SignOutEverywhere(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.DeleteSessions(ctx); err != nil {
		return fmt.Errorf("access: delete sessions: %w", err)
	}
	return nil
}

// RequestRecovery issues a recovery code for a forgotten panel password and
// writes it, as one line, to the recovery output. The code is valid for 15
// minutes and for one use, and lives in memory only. RequestRecovery returns
// whether it issued a code and when the live code expires.
//
// One code is live at a time. While it is, a request issues none and returns
// that code's expiry, so another program cannot replace the code the user is
// reading. A new code is issued at most once a minute, even after the
// previous one was used or burnt; a request sooner returns a RetryAfterError
// wrapping domain.ErrTooManyAttempts with the time left. Open mode holds no
// password to recover, so there the error wraps domain.ErrConflict. Setup
// returns the bare domain.ErrSetupRequired.
func (s *Service) RequestRecovery(
	ctx context.Context,
) (issued bool, expiresAt time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := requireMode(s.store.Snapshot().Mode, ModeToken); err != nil {
		return false, time.Time{}, err
	}
	now := s.now()
	if s.recovery.live(now) {
		return false, s.recovery.expiresAt, nil
	}
	next := s.recovery.issuedAt.Add(recoveryIssueInterval)
	if now.Before(next) {
		return false, time.Time{}, RetryAfterError{
			Err: domain.ErrTooManyAttempts, After: next.Sub(now),
		}
	}
	code, err := newRecoveryCode()
	if err != nil {
		return false, time.Time{}, err
	}
	if _, err := fmt.Fprintf(
		s.recoveryOut,
		"Pit Officer panel recovery code: %s (single use, valid 15 minutes)\n",
		formatRecoveryCode(code),
	); err != nil {
		return false, time.Time{}, fmt.Errorf("access: write recovery code: %w", err)
	}
	s.recovery = recoveryCode{
		code: code, expiresAt: now.Add(recoveryCodeTTL), issuedAt: now,
	}
	if err := s.audit(ctx, publicCaller, store.AuditEntry{
		Action: domain.AuditActionIssueRecoveryCode,
	}); err != nil {
		return false, time.Time{}, err
	}
	return true, s.recovery.expiresAt, nil
}

// ResetWithRecoveryCode sets a new panel password with the live recovery code
// and issues a session for this browser, returning its raw id for the session
// cookie. Every other session ends; the tokens stay as they are.
//
// The new password is validated first, so a refused password does not count
// against the code. The code is compared without the dashes and spaces a
// person may type, and regardless of case. Without a live code, or with a
// wrong one, the error wraps domain.ErrInvalidRecoveryCode, and the fifth
// wrong code burns the live one. Open mode holds no password, so there the
// error wraps domain.ErrConflict. Setup returns the bare
// domain.ErrSetupRequired.
func (s *Service) ResetWithRecoveryCode(
	ctx context.Context, code, password string,
) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := requireMode(s.store.Snapshot().Mode, ModeToken); err != nil {
		return "", err
	}
	if err := validatePassword(password); err != nil {
		return "", err
	}
	now := s.now()
	if !s.recovery.live(now) {
		// An expired code is forgotten here.
		s.burnRecoveryCode()
		return "", errNoRecoveryCode
	}
	if !secretsEqual(normalizeRecoveryCode(code), s.recovery.code) {
		s.recovery.failures++
		if s.recovery.failures >= recoveryMaxFailures {
			s.burnRecoveryCode()
		}
		return "", errWrongRecoveryCode
	}
	hash, err := hashPassword(password)
	if err != nil {
		return "", err
	}
	id, session, err := newSession(now)
	if err != nil {
		return "", err
	}
	if err := s.store.ResetPassword(
		ctx, Password{Hash: hash, SetAt: now}, session,
	); err != nil {
		return "", fmt.Errorf("access: reset panel password: %w", err)
	}
	s.burnRecoveryCode()
	if err := s.audit(ctx, publicCaller,
		store.AuditEntry{Action: domain.AuditActionUseRecoveryCode},
		store.AuditEntry{Action: domain.AuditActionSetPanelPassword},
	); err != nil {
		return "", err
	}
	return id, nil
}

// burnRecoveryCode forgets the recovery code. The time it was issued stays,
// for the minute between codes.
func (s *Service) burnRecoveryCode() {
	s.recovery = recoveryCode{issuedAt: s.recovery.issuedAt}
}

// AccessInfo returns what the panel's security page shows about access.
func (s *Service) AccessInfo(ctx context.Context) (AccessInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := AccessInfo{Mode: s.store.Snapshot().Mode}
	switch info.Mode {
	case ModeSetup:
		return AccessInfo{}, domain.ErrSetupRequired
	case ModeOpen:
	case ModeToken:
		password, err := s.storedPassword(ctx)
		if err != nil {
			return AccessInfo{}, err
		}
		info.PasswordSetAt = &password.SetAt
	default:
		return AccessInfo{}, fmt.Errorf("access: unknown access mode %q", info.Mode)
	}
	now := s.now()
	sessions, err := s.store.CountSessions(
		ctx, now, now.Add(-sessionIdleTimeout),
	)
	if err != nil {
		return AccessInfo{}, fmt.Errorf("access: count sessions: %w", err)
	}
	info.Sessions = sessions
	return info, nil
}

// SwitchToToken switches open mode to token mode with a new panel password.
// It issues both tokens and a session for this browser, which would
// otherwise be sent to sign in on its next request, stores them in one
// transaction, and returns the session's raw id for the session cookie. In
// token mode the error wraps domain.ErrConflict. Setup returns the bare
// domain.ErrSetupRequired.
func (s *Service) SwitchToToken(
	ctx context.Context, password string,
) (string, error) {
	caller, err := callerFrom(ctx)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := requireMode(s.store.Snapshot().Mode, ModeOpen); err != nil {
		return "", err
	}
	if err := validatePassword(password); err != nil {
		return "", err
	}
	return s.enableTokenMode(ctx, caller, password, s.store.EnableTokenMode)
}

// enableTokenMode runs with s.mu held after the caller checked its mode and
// validated the password with validatePassword.
func (s *Service) enableTokenMode(
	ctx context.Context, caller domain.Caller, password string,
	commit func(context.Context, Credentials, Session) error,
) (string, error) {
	hash, err := hashPassword(password)
	if err != nil {
		return "", err
	}
	apiToken, err := newToken(TokenAPI)
	if err != nil {
		return "", err
	}
	agentToken, err := newToken(TokenAgent)
	if err != nil {
		return "", err
	}
	now := s.now()
	id, session, err := newSession(now)
	if err != nil {
		return "", err
	}
	if err := commit(ctx, Credentials{
		Password:   Password{Hash: hash, SetAt: now},
		APIToken:   apiToken,
		AgentToken: agentToken,
	}, session); err != nil {
		return "", fmt.Errorf("access: enable token mode: %w", err)
	}
	// A recovery code belongs to the password it was issued for. The
	// database can also return to setup without this service, when it is
	// reset or recreated, so the new password's token mode forgets the code here
	// rather than on the way out.
	s.recovery = recoveryCode{}
	if err := s.audit(ctx, caller,
		store.AuditEntry{
			Action: domain.AuditActionSetAccessMode, Detail: string(ModeToken),
		},
		store.AuditEntry{Action: domain.AuditActionSetPanelPassword},
		store.AuditEntry{
			Action: domain.AuditActionIssueAccessToken, Detail: string(TokenAPI),
		},
		store.AuditEntry{
			Action: domain.AuditActionIssueAccessToken, Detail: string(TokenAgent),
		},
	); err != nil {
		return "", err
	}
	return id, nil
}

// SwitchToOpen switches token mode to open mode, which admits every local
// program. The caller confirms with an explicit acknowledgement, without
// which the error is a validation error, and with the current panel
// password, a wrong one returning an error wrapping
// domain.ErrCurrentPasswordInvalid. The panel password, both tokens and every
// session are deleted in one transaction. In open mode the error wraps
// domain.ErrConflict. Setup returns the bare domain.ErrSetupRequired.
func (s *Service) SwitchToOpen(
	ctx context.Context, currentPassword string, acknowledged bool,
) error {
	caller, err := callerFrom(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := requireMode(s.store.Snapshot().Mode, ModeToken); err != nil {
		return err
	}
	if !acknowledged {
		return domain.NewValidationError(
			"/acknowledged", "required",
			"switching to open mode must be acknowledged",
		)
	}
	if err := s.checkCurrentPassword(ctx, currentPassword); err != nil {
		return err
	}
	if err := s.store.DisableTokenMode(ctx); err != nil {
		return fmt.Errorf("access: disable token mode: %w", err)
	}
	return s.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetAccessMode, Detail: string(ModeOpen),
	})
}

// ChangePassword replaces the panel password, confirmed with the current one,
// and ends every session except the one the cookie names. A live recovery
// code was issued for the replaced password, so it is burnt; the minute
// between codes still runs from its issue. A wrong current password returns
// an error wrapping domain.ErrCurrentPasswordInvalid. Open mode holds no
// password, so there the error wraps domain.ErrConflict. Setup returns the
// bare domain.ErrSetupRequired.
func (s *Service) ChangePassword(
	ctx context.Context, cookie, currentPassword, password string,
) error {
	caller, err := callerFrom(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := requireMode(s.store.Snapshot().Mode, ModeToken); err != nil {
		return err
	}
	keep, ok := sessionIDHash(cookie)
	if !ok {
		return errNoSession
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	if err := s.checkCurrentPassword(ctx, currentPassword); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	if err := s.store.ReplacePassword(
		ctx, Password{Hash: hash, SetAt: s.now()}, keep,
	); err != nil {
		return fmt.Errorf("access: replace panel password: %w", err)
	}
	// A recovery code belongs to the password it was issued for.
	s.burnRecoveryCode()
	return s.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetPanelPassword,
	})
}

// Token returns the value of the token of kind, for the panel to show. Open
// mode issues no token, so there the error wraps domain.ErrConflict. Setup
// returns the bare domain.ErrSetupRequired.
func (s *Service) Token(kind TokenKind) (string, error) {
	snapshot := s.store.Snapshot()
	if err := requireMode(snapshot.Mode, ModeToken); err != nil {
		return "", err
	}
	return snapshot.token(kind)
}

// RegenerateToken replaces the token of kind with a new value and returns it;
// the old value stops working at once. Open mode issues no token, so there
// the error wraps domain.ErrConflict. Setup returns the bare
// domain.ErrSetupRequired.
func (s *Service) RegenerateToken(
	ctx context.Context, kind TokenKind,
) (string, error) {
	caller, err := callerFrom(ctx)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := requireMode(s.store.Snapshot().Mode, ModeToken); err != nil {
		return "", err
	}
	token, err := newToken(kind)
	if err != nil {
		return "", err
	}
	if err := s.store.ReplaceToken(ctx, kind, token); err != nil {
		return "", fmt.Errorf("access: replace %s token: %w", kind, err)
	}
	if err := s.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionIssueAccessToken, Detail: string(kind),
	}); err != nil {
		return "", err
	}
	return token, nil
}

// requireMode refuses an operation the current mode does not allow.
func requireMode(current, want Mode) error {
	switch current {
	case ModeSetup:
		return domain.ErrSetupRequired
	case ModeOpen, ModeToken:
	default:
		return fmt.Errorf("access: unknown access mode %q", current)
	}
	if current != want {
		return fmt.Errorf(
			"the access mode is %s, not %s: %w", current, want, domain.ErrConflict,
		)
	}
	return nil
}

// storedPassword reads the panel password token mode holds. Its absence
// breaks the store's contract, so it is an internal error rather than a
// missing resource.
func (s *Service) storedPassword(ctx context.Context) (Password, error) {
	password, err := s.store.PanelPassword(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return Password{}, errors.New("access: token mode holds no panel password")
	}
	if err != nil {
		return Password{}, fmt.Errorf("access: read panel password: %w", err)
	}
	return password, nil
}

// checkCurrentPassword confirms an authenticated change with the current
// panel password.
func (s *Service) checkCurrentPassword(ctx context.Context, password string) error {
	stored, err := s.storedPassword(ctx)
	if err != nil {
		return err
	}
	matches, err := verifyPassword(stored.Hash, password)
	if err != nil {
		return err
	}
	if !matches {
		return domain.ErrCurrentPasswordInvalid
	}
	return nil
}

// callerFrom returns the caller an authenticated panel route stamps in ctx.
// Its absence is a composition defect, so the error is internal: it does not
// wrap the validation error auth.CallerFromContext reports, which a client
// would see as its own mistake.
func callerFrom(ctx context.Context) (domain.Caller, error) {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return domain.Caller{}, fmt.Errorf(
			"access: authenticated operation without a caller: %v", err,
		)
	}
	return caller, nil
}

// audit appends the entries in order, stamped with caller.
func (s *Service) audit(
	ctx context.Context, caller domain.Caller, entries ...store.AuditEntry,
) error {
	for _, entry := range entries {
		if err := s.auditor.AppendAudit(ctx, entry, caller); err != nil {
			return fmt.Errorf("access: audit %s: %w", entry.Action, err)
		}
	}
	return nil
}
