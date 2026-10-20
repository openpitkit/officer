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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/secret"
	"go.openpit.dev/officer/framework/store"
)

// start is the time every test clock begins at.
var start = time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC)

// The panel passwords the tests use. They are fixtures, but the assertions
// still never print them.
const (
	firstPassword  = "correct horse battery staple"
	secondPassword = "tr0ub4dor and three more words"
	wrongPassword  = "not the panel password at all"
)

// panelUser is the caller an authenticated panel route stamps. It differs
// from operatorOnPanel, so a test can tell which one an audit row carries.
var panelUser = domain.Caller{Principal: "desk-lead", Source: domain.SourcePanel}

// operatorOnPanel is the caller password recovery, on the public panel
// routes, must audit as. Sign-in, the other public operation, writes no audit
// row.
var operatorOnPanel = domain.Caller{
	Principal: domain.PrincipalOperator, Source: domain.SourcePanel,
}

// panelCtx is the context of an authenticated panel request.
func panelCtx() context.Context {
	return auth.ContextWithCaller(context.Background(), panelUser)
}

// fakeClock is a clock that moves only when the test advances it.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// auditRecord is one appended audit row with the caller it was stamped with.
type auditRecord struct {
	entry  store.AuditEntry
	caller domain.Caller
}

// fakeAuditor records audit rows, or fails every append once err is set.
type fakeAuditor struct {
	mu      sync.Mutex
	records []auditRecord
	err     error
}

func (a *fakeAuditor) AppendAudit(
	_ context.Context, entry store.AuditEntry, caller domain.Caller,
) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	a.records = append(a.records, auditRecord{entry: entry, caller: caller})
	return nil
}

func (a *fakeAuditor) Records() []auditRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]auditRecord(nil), a.records...)
}

func (a *fakeAuditor) Fail(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

// fakeStore keeps the access state in memory and holds the Store contract:
// every method is atomic under one lock, the snapshot changes together with
// the state it describes, and a write that requires a mode changes nothing
// in another one and returns domain.ErrConflict.
type fakeStore struct {
	mu       sync.Mutex
	snapshot Snapshot
	password *Password
	sessions map[[sha256.Size]byte]Session
	// lookups counts the Session calls.
	lookups int
	// afterPasswordRead and afterSessionRead, when set, run once
	// PanelPassword or Session has read the state and before it returns, and
	// beforeSessionWrite runs before CreateSession writes. All three run
	// outside the lock, so a test changes the store there while the caller
	// holds what it read or waits to write.
	afterPasswordRead  func()
	afterSessionRead   func()
	beforeSessionWrite func()
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		snapshot: Snapshot{Mode: ModeOpen},
		sessions: map[[sha256.Size]byte]Session{},
	}
}

// requireMode refuses a write the stored mode does not allow. The caller
// holds the lock.
func (f *fakeStore) requireMode(want Mode) error {
	if f.snapshot.Mode != want {
		return fmt.Errorf(
			"fake store: mode is %s, not %s: %w",
			f.snapshot.Mode, want, domain.ErrConflict,
		)
	}
	return nil
}

func (f *fakeStore) Snapshot() Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot
}

func (f *fakeStore) PanelPassword(context.Context) (Password, error) {
	f.mu.Lock()
	password, afterRead := f.password, f.afterPasswordRead
	f.mu.Unlock()
	if afterRead != nil {
		afterRead()
	}
	if password == nil {
		return Password{}, fmt.Errorf("panel password: %w", domain.ErrNotFound)
	}
	return *password, nil
}

func (f *fakeStore) EnableTokenMode(
	_ context.Context, credentials Credentials, session Session,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.requireMode(ModeOpen); err != nil {
		return err
	}
	password := credentials.Password
	f.password = &password
	f.sessions[session.IDHash] = session
	f.snapshot = Snapshot{
		Mode:       ModeToken,
		APIToken:   credentials.APIToken,
		AgentToken: credentials.AgentToken,
	}
	return nil
}

func (f *fakeStore) DisableTokenMode(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.requireMode(ModeToken); err != nil {
		return err
	}
	f.password = nil
	f.sessions = map[[sha256.Size]byte]Session{}
	f.snapshot = Snapshot{Mode: ModeOpen}
	return nil
}

func (f *fakeStore) ReplaceToken(
	_ context.Context, kind TokenKind, value string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.requireMode(ModeToken); err != nil {
		return err
	}
	switch kind {
	case TokenAPI:
		f.snapshot.APIToken = value
	case TokenAgent:
		f.snapshot.AgentToken = value
	default:
		return errUnknownTokenKind
	}
	return nil
}

func (f *fakeStore) ReplacePassword(
	_ context.Context, password Password, keep [sha256.Size]byte,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.requireMode(ModeToken); err != nil {
		return err
	}
	f.password = &password
	for idHash := range f.sessions {
		if idHash != keep {
			delete(f.sessions, idHash)
		}
	}
	return nil
}

func (f *fakeStore) ResetPassword(
	_ context.Context, password Password, session Session,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.requireMode(ModeToken); err != nil {
		return err
	}
	f.password = &password
	f.sessions = map[[sha256.Size]byte]Session{session.IDHash: session}
	return nil
}

func (f *fakeStore) CreateSession(_ context.Context, session Session) error {
	f.mu.Lock()
	beforeWrite := f.beforeSessionWrite
	f.mu.Unlock()
	if beforeWrite != nil {
		beforeWrite()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.requireMode(ModeToken); err != nil {
		return err
	}
	f.sessions[session.IDHash] = session
	return nil
}

func (f *fakeStore) Session(
	_ context.Context, idHash [sha256.Size]byte,
) (Session, error) {
	f.mu.Lock()
	f.lookups++
	session, ok := f.sessions[idHash]
	afterRead := f.afterSessionRead
	f.mu.Unlock()
	if afterRead != nil {
		afterRead()
	}
	if !ok {
		return Session{}, fmt.Errorf("session: %w", domain.ErrNotFound)
	}
	return session, nil
}

func (f *fakeStore) TouchSession(
	_ context.Context, idHash [sha256.Size]byte, lastSeenAt time.Time,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	session, ok := f.sessions[idHash]
	if !ok {
		return fmt.Errorf("session: %w", domain.ErrNotFound)
	}
	session.LastSeenAt = lastSeenAt
	f.sessions[idHash] = session
	return nil
}

func (f *fakeStore) DeleteSession(
	_ context.Context, idHash [sha256.Size]byte,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, idHash)
	return nil
}

func (f *fakeStore) DeleteSessions(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = map[[sha256.Size]byte]Session{}
	return nil
}

func (f *fakeStore) CountSessions(
	_ context.Context, expiresAfter, seenAfter time.Time,
) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, session := range f.sessions {
		if session.ExpiresAt.After(expiresAfter) &&
			session.LastSeenAt.After(seenAfter) {
			count++
		}
	}
	return count, nil
}

// seedTokenMode puts the store in token mode with the given password hash,
// for a test that needs a stored hash the Service did not write.
func (f *fakeStore) seedTokenMode(hash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.password = &Password{Hash: hash, SetAt: start}
	f.snapshot = Snapshot{
		Mode:       ModeToken,
		APIToken:   "pit_api_seeded",
		AgentToken: "pit_mcp_seeded",
	}
}

// storedPassword returns the stored panel password, failing the test when
// there is none.
func (f *fakeStore) storedPassword(t *testing.T) Password {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.password == nil {
		t.Fatal("the store holds no panel password")
	}
	return *f.password
}

func (f *fakeStore) hasPassword() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.password != nil
}

// session returns the stored session the raw session id names.
func (f *fakeStore) session(t *testing.T, id string) (Session, bool) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	session, ok := f.sessions[idHashOf(t, id)]
	return session, ok
}

func (f *fakeStore) sessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

func (f *fakeStore) lookupCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookups
}

func (f *fakeStore) setAfterPasswordRead(hook func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.afterPasswordRead = hook
}

func (f *fakeStore) setAfterSessionRead(hook func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.afterSessionRead = hook
}

func (f *fakeStore) setBeforeSessionWrite(hook func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beforeSessionWrite = hook
}

// idHashOf decodes a raw session id as the Service issues it - 32 bytes in
// unpadded base64url - and returns its SHA-256, failing the test on any
// other shape. It never prints the id.
func idHashOf(t *testing.T, id string) [sha256.Size]byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(raw) != 32 {
		t.Fatalf(
			"session id decodes to %d bytes (%v); want 32 bytes in unpadded "+
				"base64url", len(raw), err,
		)
	}
	return sha256.Sum256(raw)
}

// harness is a Service over the fakes, with the clock at start and the
// recovery output in a buffer.
type harness struct {
	t       *testing.T
	store   *fakeStore
	auditor *fakeAuditor
	clock   *fakeClock
	out     *bytes.Buffer
	service *Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:       t,
		store:   newFakeStore(),
		auditor: &fakeAuditor{},
		clock:   &fakeClock{now: start},
		out:     &bytes.Buffer{},
	}
	service, err := New(h.store, h.auditor, h.out)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	service.now = h.clock.Now
	h.service = service
	return h
}

// enableToken switches to token mode with password and returns the session
// issued for this browser.
func (h *harness) enableToken(password string) string {
	h.t.Helper()
	id, err := h.service.SwitchToToken(panelCtx(), password)
	if err != nil {
		h.t.Fatalf("SwitchToToken: %v", err)
	}
	return id
}

// addSession stores one more session issued at the clock's time and returns
// its raw id.
func (h *harness) addSession() string {
	h.t.Helper()
	id, session, err := newSession(h.clock.Now())
	if err != nil {
		h.t.Fatalf("newSession: %v", err)
	}
	if err := h.store.CreateSession(context.Background(), session); err != nil {
		h.t.Fatalf("CreateSession: %v", err)
	}
	return id
}

// requestCode issues a recovery code, checks the one line written for it,
// and returns the code as the line shows it.
func (h *harness) requestCode() string {
	h.t.Helper()
	issued, _, err := h.service.RequestRecovery(context.Background())
	if err != nil || !issued {
		h.t.Fatalf("RequestRecovery = %v, %v; want a code issued", issued, err)
	}
	match := recoveryLine.FindStringSubmatch(h.out.String())
	if match == nil {
		h.t.Fatal("the recovery output is not exactly one recovery code line")
	}
	h.out.Reset()
	return match[1]
}

// redact uses the credential redactor for error diagnostics. A nonempty
// fixture credential must contain a non-whitespace character.
func redact(t *testing.T, err error, values ...string) string {
	t.Helper()
	if err == nil {
		return "<nil>"
	}
	for _, value := range values {
		if value != "" && strings.TrimSpace(value) == "" {
			t.Fatal("cannot redact a whitespace-only fixture credential")
		}
	}
	credentials, encodeErr := json.Marshal(map[string][]string{"values": values})
	if encodeErr != nil {
		t.Fatal("cannot encode fixture credentials for redaction")
	}
	_, replacer, redactErr := secret.CredentialRedactor(string(credentials))
	if redactErr != nil {
		t.Fatal("cannot build fixture credential redactor")
	}
	return replacer.Replace(err.Error())
}

func TestRedactErrorDiagnostics(t *testing.T) {
	const short = "fixture-token"
	const long = "fixture-token-with-suffix"
	const escaped = `quoted "value" <&>`
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "<nil>"},
		{"ordinary", errors.New("store unavailable"), "store unavailable"},
		{"overlap", errors.New(long + " " + short), "[redacted] [redacted]"},
		{"raw", errors.New(escaped), "[redacted]"},
		{"json", errors.New(`quoted \"value\" <&>`), "[redacted]"},
		{"json_html", errors.New(`quoted \"value\" \u003c\u0026\u003e`), "[redacted]"},
		{"url", errors.New("quoted+%22value%22+%3C%26%3E"), "[redacted]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if redact(t, tc.err, short, long, escaped, "") != tc.want {
				t.Fatal("error diagnostics did not match the redacted form")
			}
		})
	}
}

// assertNoSecret fails when any audit row, or any of the errors, carries one
// of the secrets. The failure never prints the secret.
func assertNoSecret(
	t *testing.T, records []auditRecord, errs []error, secrets ...string,
) {
	t.Helper()
	for _, secret := range secrets {
		if secret == "" {
			t.Fatal("assertNoSecret was given an empty secret")
		}
		for _, record := range records {
			if strings.Contains(fmt.Sprintf("%+v", record), secret) {
				t.Errorf("the %s audit row carries a secret", record.entry.Action)
			}
		}
		for _, err := range errs {
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Errorf("an error carries a secret: %s", redact(t, err, secrets...))
			}
		}
	}
}
