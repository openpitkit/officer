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
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// recoveryLine is the one line a recovery request writes; the group captures
// the code as shown.
var recoveryLine = regexp.MustCompile(
	`\APit Officer panel recovery code: ` +
		`([A-Z2-7]{4}-[A-Z2-7]{4}-[A-Z2-7]{4}) ` +
		`\(single use, valid 15 minutes\)\n\z`,
)

// day is a calendar day of the test clock.
const day = 24 * time.Hour

// switchRows is the number of audit rows SwitchToToken writes.
const switchRows = 4

func assertValidation(t *testing.T, err error, pointer, constraint string) {
	t.Helper()
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if got := domain.ValidationPointer(err); got != pointer {
		t.Errorf("validation pointer = %q, want %q", got, pointer)
	}
	if got := domain.ValidationConstraint(err); got != constraint {
		t.Errorf("validation constraint = %q, want %q", got, constraint)
	}
}

func assertRetry(t *testing.T, err, refusal error, after time.Duration) {
	t.Helper()
	var retry RetryAfterError
	if !errors.As(err, &retry) || !errors.Is(err, refusal) {
		t.Fatalf("error = %v, want a RetryAfterError wrapping %v", err, refusal)
	}
	if retry.After != after {
		t.Errorf("retry after = %v, want %v", retry.After, after)
	}
}

// assertInternal fails unless err is an internal error: one that wraps no
// sentinel a client would read as its own mistake or as a refusal.
func assertInternal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = <nil>, want an internal error")
	}
	for _, sentinel := range []error{
		domain.ErrInvalid, domain.ErrNotFound, domain.ErrConflict,
		domain.ErrUnauthorized, domain.ErrInvalidPassword,
		domain.ErrCurrentPasswordInvalid, domain.ErrTooManyAttempts,
	} {
		if errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want an internal error, not %v", err, sentinel)
		}
	}
}

// assertAudit fails unless the rows are exactly want, in order, each stamped
// with caller.
func assertAudit(
	t *testing.T, records []auditRecord, caller domain.Caller,
	want ...store.AuditEntry,
) {
	t.Helper()
	if len(records) != len(want) {
		t.Fatalf("audit rows = %d, want %d", len(records), len(want))
	}
	for i, record := range records {
		// The row's own detail is never printed: a wrong one may carry a
		// secret.
		if record.entry != want[i] {
			t.Errorf("audit row %d = %s with another detail or field, want %s %q",
				i, record.entry.Action, want[i].Action, want[i].Detail)
		}
		if record.caller != caller {
			t.Errorf("audit row %d caller = %+v, want %+v", i, record.caller, caller)
		}
	}
}

// assertTokenFormat fails unless token is prefix followed by 32 bytes in
// unpadded base64url.
func assertTokenFormat(t *testing.T, name, token, prefix string) {
	t.Helper()
	body, ok := strings.CutPrefix(token, prefix)
	if !ok {
		t.Errorf("%s does not start with %q", name, prefix)
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || len(raw) != 32 {
		t.Errorf("%s body decodes to %d bytes (%v), want 32 bytes in unpadded "+
			"base64url", name, len(raw), err)
	}
}

func TestParseTokenKind(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]TokenKind{
		"api": TokenAPI, "agent": TokenAgent,
	} {
		if got, err := ParseTokenKind(value); err != nil || got != want {
			t.Errorf("ParseTokenKind(%q) = %q, %v; want %q", value, got, err, want)
		}
	}
	for _, value := range []string{"", "API", "Agent", "mcp", "api ", "pit_api_"} {
		if _, err := ParseTokenKind(value); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("ParseTokenKind(%q) error = %v, want domain.ErrNotFound",
				value, err)
		}
	}
}

func TestNewRefusesMissingDependency(t *testing.T) {
	t.Parallel()
	accessStore, auditor, out := newFakeStore(), &fakeAuditor{}, &bytes.Buffer{}
	for name, build := range map[string]func() (*Service, error){
		"store":   func() (*Service, error) { return New(nil, auditor, out) },
		"auditor": func() (*Service, error) { return New(accessStore, nil, out) },
		"output":  func() (*Service, error) { return New(accessStore, auditor, nil) },
	} {
		if service, err := build(); err == nil || service != nil {
			t.Errorf("New without the %s = %v, %v; want an error", name, service, err)
		}
	}
}

func TestSwitchToTokenIssuesBothTokensAndASession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := h.enableToken(firstPassword)

	snapshot := h.store.Snapshot()
	if snapshot.Mode != ModeToken {
		t.Fatalf("mode = %q, want %q", snapshot.Mode, ModeToken)
	}
	assertTokenFormat(t, "the API token", snapshot.APIToken, "pit_api_")
	assertTokenFormat(t, "the agent token", snapshot.AgentToken, "pit_mcp_")
	session, ok := h.store.session(t, id)
	if !ok {
		t.Fatal("the session issued for this browser is not stored")
	}
	if !session.CreatedAt.Equal(start) || !session.LastSeenAt.Equal(start) ||
		!session.ExpiresAt.Equal(start.Add(30*day)) {
		t.Errorf("session = %+v, want issued at %v and expiring 30 days later",
			session, start)
	}
	if err := h.service.CheckSession(context.Background(), id); err != nil {
		t.Errorf("CheckSession(issued session) = %v", err)
	}
	if got := h.store.storedPassword(t).SetAt; !got.Equal(start) {
		t.Errorf("password set at %v, want %v", got, start)
	}
	assertAudit(t, h.auditor.Records(), panelUser,
		store.AuditEntry{Action: domain.AuditActionSetAccessMode, Detail: "token"},
		store.AuditEntry{Action: domain.AuditActionSetPanelPassword},
		store.AuditEntry{Action: domain.AuditActionIssueAccessToken, Detail: "api"},
		store.AuditEntry{Action: domain.AuditActionIssueAccessToken, Detail: "agent"},
	)
	assertNoSecret(t, h.auditor.Records(), nil,
		firstPassword, snapshot.APIToken, snapshot.AgentToken, id)
}

func TestPasswordHashIsArgon2idPHC(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.enableToken(firstPassword)
	stored := h.store.storedPassword(t).Hash

	phc := regexp.MustCompile(`\A\$argon2id\$v=19\$m=19456,t=2,p=1\$` +
		`([A-Za-z0-9+/]{22})\$([A-Za-z0-9+/]{43})\z`)
	match := phc.FindStringSubmatch(stored)
	if match == nil {
		t.Fatal("the stored hash is not $argon2id$v=19$m=19456,t=2,p=1$<salt>" +
			"$<key> with a 16-byte salt and a 32-byte key in unpadded base64")
	}
	salt, err := base64.RawStdEncoding.DecodeString(match[1])
	if err != nil {
		t.Fatalf("decode salt: %v", err)
	}
	key, err := base64.RawStdEncoding.DecodeString(match[2])
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}
	want := argon2.IDKey([]byte(firstPassword), salt, 2, 19456, 1, 32)
	if !bytes.Equal(key, want) {
		t.Error("the stored key is not argon2id of the password and the salt " +
			"at m=19456, t=2, p=1")
	}
	if _, err := h.service.SignIn(context.Background(), firstPassword); err != nil {
		t.Errorf("SignIn with the password = %v", err)
	}

	other := newHarness(t)
	other.enableToken(firstPassword)
	if other.store.storedPassword(t).Hash == stored {
		t.Error("two hashes of one password are equal: the salt is not random")
	}
}

func TestMalformedStoredHashIsRefused(t *testing.T) {
	t.Parallel()
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	valid := phcPrefix + salt + "$" + key
	paddedSalt := base64.StdEncoding.EncodeToString(make([]byte, 16))
	shortSalt := base64.RawStdEncoding.EncodeToString(make([]byte, 8))
	shortKey := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	for name, hash := range map[string]string{
		"empty":          "",
		"not PHC":        "correct horse battery staple",
		"argon2i":        strings.Replace(valid, "$argon2id$", "$argon2i$", 1),
		"other version":  strings.Replace(valid, "$v=19$", "$v=16$", 1),
		"other memory":   strings.Replace(valid, "m=19456", "m=65536", 1),
		"other passes":   strings.Replace(valid, "t=2", "t=3", 1),
		"other lanes":    strings.Replace(valid, "p=1", "p=4", 1),
		"no key":         phcPrefix + salt,
		"padded salt":    phcPrefix + paddedSalt + "$" + key,
		"short salt":     phcPrefix + shortSalt + "$" + key,
		"short key":      phcPrefix + salt + "$" + shortKey,
		"trailing field": valid + "$more",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.store.seedTokenMode(hash)
			// The second attempt shows the refusal started no sign-in delay.
			for range 2 {
				_, err := h.service.SignIn(context.Background(), firstPassword)
				if !errors.Is(err, errMalformedHash) {
					t.Fatalf("SignIn over a malformed hash = %v, want %v",
						err, errMalformedHash)
				}
				assertInternal(t, err)
			}
		})
	}
}

func TestPasswordLengthIsCountedInCodePoints(t *testing.T) {
	t.Parallel()
	const twoByte = string(rune(0xe9)) // one code point, two UTF-8 bytes
	for name, tc := range map[string]struct {
		password   string
		constraint string
	}{
		"14 characters":           {strings.Repeat("a", 14), "min_length"},
		"15 characters":           {strings.Repeat("a", 15), ""},
		"256 characters":          {strings.Repeat("a", 256), ""},
		"257 characters":          {strings.Repeat("a", 257), "max_length"},
		"14 two-byte characters":  {strings.Repeat(twoByte, 14), "min_length"},
		"15 two-byte characters":  {strings.Repeat(twoByte, 15), ""},
		"256 two-byte characters": {strings.Repeat(twoByte, 256), ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			_, err := h.service.SwitchToToken(panelCtx(), tc.password)
			if tc.constraint == "" {
				if err != nil {
					t.Fatalf("SwitchToToken = %v, want the password accepted", err)
				}
				return
			}
			assertValidation(t, err, "/password", tc.constraint)
			assertNoSecret(t, nil, []error{err}, tc.password)
			if h.store.Snapshot().Mode != ModeOpen {
				t.Error("a refused password changed the mode")
			}
		})
	}
}

func TestCheckToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.enableToken(firstPassword)
	snapshot := h.store.Snapshot()
	tokens := []string{snapshot.APIToken, snapshot.AgentToken}
	garbageAPI := "pit_api_" + strings.Repeat("A", 43)
	garbageAgent := "pit_mcp_" + strings.Repeat("A", 43)

	for _, tc := range []struct {
		name      string
		kind      TokenKind
		presented string
		want      error
		// surface is the surface a wrong_token refusal names.
		surface string
	}{
		{"API token on the API", TokenAPI, snapshot.APIToken, nil, ""},
		{"agent token on MCP", TokenAgent, snapshot.AgentToken, nil, ""},
		{"agent token on the API", TokenAPI, snapshot.AgentToken, domain.ErrWrongToken, "/mcp"},
		{"API token on MCP", TokenAgent, snapshot.APIToken, domain.ErrWrongToken, "/api/v1"},
		{"API-prefixed guess on the API", TokenAPI, garbageAPI, domain.ErrUnauthorized, ""},
		{"agent-prefixed guess on the API", TokenAPI, garbageAgent, domain.ErrUnauthorized, ""},
		{"agent-prefixed guess on MCP", TokenAgent, garbageAgent, domain.ErrUnauthorized, ""},
		{"empty on the API", TokenAPI, "", domain.ErrUnauthorized, ""},
		{"empty on MCP", TokenAgent, "", domain.ErrUnauthorized, ""},
	} {
		err := h.service.CheckToken(tc.kind, tc.presented)
		if tc.want == nil {
			if err != nil {
				t.Errorf("%s: %s", tc.name, redact(t, err, tokens...))
			}
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %s, want %v", tc.name, redact(t, err, tokens...), tc.want)
			continue
		}
		if tc.surface != "" && !strings.Contains(err.Error(), tc.surface) {
			t.Errorf("%s: the refusal does not name %s", tc.name, tc.surface)
		}
		assertNoSecret(t, nil, []error{err}, tokens...)
	}
	err := h.service.CheckToken(TokenKind("other"), snapshot.APIToken)
	assertInternal(t, err)
}

func TestCheckTokenRefusesEveryTokenInOpenMode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, kind := range []TokenKind{TokenAPI, TokenAgent} {
		for _, presented := range []string{"", "pit_api_x", "pit_mcp_x"} {
			err := h.service.CheckToken(kind, presented)
			if !errors.Is(err, domain.ErrUnauthorized) {
				t.Errorf("CheckToken(%s, %q) in open mode = %v, want unauthorized",
					kind, presented, err)
			}
		}
	}
}

func TestSignInIssuesSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.enableToken(firstPassword)
	h.clock.Advance(time.Minute)
	now := h.clock.Now()

	id, err := h.service.SignIn(context.Background(), firstPassword)
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	session, ok := h.store.session(t, id)
	if !ok {
		t.Fatal("the store holds no session for the id's SHA-256")
	}
	if !session.CreatedAt.Equal(now) || !session.LastSeenAt.Equal(now) ||
		!session.ExpiresAt.Equal(now.Add(30*day)) {
		t.Errorf("session = %+v, want issued at %v and expiring 30 days later",
			session, now)
	}
	if err := h.service.CheckSession(context.Background(), id); err != nil {
		t.Errorf("CheckSession(signed-in session) = %v", err)
	}
}

func TestSessionEndsThirtyDaysAfterSignIn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := h.enableToken(firstPassword)
	ctx := context.Background()

	// Presented every six days, the session never idles.
	for range 4 {
		h.clock.Advance(6 * day)
		if err := h.service.CheckSession(ctx, id); err != nil {
			t.Fatalf("CheckSession at %v: %v", h.clock.Now(), err)
		}
	}
	h.clock.Advance(6*day - time.Second)
	if err := h.service.CheckSession(ctx, id); err != nil {
		t.Fatalf("CheckSession a second before 30 days: %v", err)
	}
	h.clock.Advance(time.Second)
	if err := h.service.CheckSession(ctx, id); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("CheckSession at 30 days = %v, want unauthorized", err)
	}
	if _, ok := h.store.session(t, id); ok {
		t.Error("the ended session's row was not deleted")
	}
}

func TestSessionEndsAfterSevenIdleDays(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := h.enableToken(firstPassword)
	ctx := context.Background()

	h.clock.Advance(7*day - time.Second)
	if err := h.service.CheckSession(ctx, id); err != nil {
		t.Fatalf("CheckSession a second before 7 idle days: %v", err)
	}
	// The request moved the idle window.
	h.clock.Advance(7*day - time.Second)
	if err := h.service.CheckSession(ctx, id); err != nil {
		t.Fatalf("CheckSession a second before 7 more idle days: %v", err)
	}
	h.clock.Advance(7 * day)
	if err := h.service.CheckSession(ctx, id); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("CheckSession after 7 idle days = %v, want unauthorized", err)
	}
	if _, ok := h.store.session(t, id); ok {
		t.Error("the idle session's row was not deleted")
	}
}

func TestSessionLastSeenIsWrittenAtMostHourly(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := h.enableToken(firstPassword)
	ctx := context.Background()

	for _, step := range []struct {
		advance time.Duration
		want    time.Time
	}{
		{time.Hour, start},
		{time.Second, start.Add(time.Hour + time.Second)},
		{59 * time.Minute, start.Add(time.Hour + time.Second)},
		{time.Minute + time.Second, start.Add(2*time.Hour + 2*time.Second)},
	} {
		h.clock.Advance(step.advance)
		if err := h.service.CheckSession(ctx, id); err != nil {
			t.Fatalf("CheckSession at %v: %v", h.clock.Now(), err)
		}
		session, _ := h.store.session(t, id)
		if !session.LastSeenAt.Equal(step.want) {
			t.Errorf("at %v last seen = %v, want %v",
				h.clock.Now(), session.LastSeenAt, step.want)
		}
	}
}

func TestCheckSessionRefusesWhatNamesNoSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := h.enableToken(firstPassword)
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		t.Fatalf("decode session id: %v", err)
	}
	ctx := context.Background()
	for name, cookie := range map[string]string{
		"empty":      "",
		"not base64": "not a session id!",
		"31 bytes":   base64.RawURLEncoding.EncodeToString(raw[:31]),
		"33 bytes":   base64.RawURLEncoding.EncodeToString(append(raw, 0)),
		"padded":     base64.URLEncoding.EncodeToString(raw),
	} {
		if err := h.service.CheckSession(ctx, cookie); !errors.Is(
			err, domain.ErrUnauthorized,
		) {
			t.Errorf("CheckSession(%s) = %v, want unauthorized", name, err)
		}
	}
	if got := h.store.lookupCount(); got != 0 {
		t.Errorf("session lookups = %d, want none: a value that is not 32 "+
			"bytes in unpadded base64url names no session", got)
	}
	unknown := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if err := h.service.CheckSession(ctx, unknown); !errors.Is(
		err, domain.ErrUnauthorized,
	) {
		t.Errorf("CheckSession(unknown session) = %v, want unauthorized", err)
	}
	if h.store.sessionCount() != 1 {
		t.Error("refusing a value changed the stored sessions")
	}
}

func TestCheckSessionRefusesSessionDeletedAfterRead(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := h.enableToken(firstPassword)
	ctx := context.Background()
	// Old enough that the check records the request's activity.
	h.clock.Advance(2 * time.Hour)
	h.store.setAfterSessionRead(func() {
		// A sign-out elsewhere deletes the session once it is read.
		if err := h.store.DeleteSessions(ctx); err != nil {
			t.Errorf("DeleteSessions: %v", err)
		}
	})
	if err := h.service.CheckSession(ctx, id); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("CheckSession(session deleted after it was read) = %v, want "+
			"unauthorized", err)
	}
}

func TestStatus(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	want := Status{Mode: ModeOpen, SetupComplete: true}
	if got, err := h.service.Status(ctx, "anything"); err != nil || got != want {
		t.Errorf("Status in open mode = %+v, %v; want %+v", got, err, want)
	}

	id := h.enableToken(firstPassword)
	for _, tc := range []struct {
		name   string
		cookie string
		want   bool
	}{
		{"live session", id, true},
		{"no cookie", "", false},
		{"unknown session", base64.RawURLEncoding.EncodeToString(make([]byte, 32)), false},
	} {
		got, err := h.service.Status(ctx, tc.cookie)
		want := Status{Mode: ModeToken, SetupComplete: true, Authenticated: tc.want}
		if err != nil || got != want {
			t.Errorf("Status with %s = %+v, %v; want %+v", tc.name, got, err, want)
		}
	}
	h.clock.Advance(7 * day)
	got, err := h.service.Status(ctx, id)
	if err != nil || got.Authenticated {
		t.Errorf("Status with an idle session = %+v, %v; want not authenticated",
			got, err)
	}
}

func TestSignInDelayDoublesUpToThirtySeconds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.enableToken(firstPassword)
	ctx := context.Background()

	var errs []error
	for _, seconds := range []time.Duration{1, 2, 4, 8, 16, 30, 30} {
		delay := seconds * time.Second
		_, err := h.service.SignIn(ctx, wrongPassword)
		assertRetry(t, err, domain.ErrInvalidPassword, delay)
		errs = append(errs, err)
		// During the delay even the right password is refused unchecked.
		h.clock.Advance(delay / 2)
		_, err = h.service.SignIn(ctx, firstPassword)
		assertRetry(t, err, domain.ErrTooManyAttempts, delay-delay/2)
		errs = append(errs, err)
		h.clock.Advance(delay - delay/2)
	}
	if _, err := h.service.SignIn(ctx, firstPassword); err != nil {
		t.Fatalf("SignIn after the delay = %v", err)
	}
	// The success cleared the delay.
	_, err := h.service.SignIn(ctx, wrongPassword)
	assertRetry(t, err, domain.ErrInvalidPassword, time.Second)
	assertNoSecret(t, nil, append(errs, err), firstPassword, wrongPassword)
}

func TestWrongCurrentPasswordDoesNotDelaySignIn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	current := h.enableToken(firstPassword)
	ctx := panelCtx()

	if err := h.service.SwitchToOpen(ctx, wrongPassword, true); !errors.Is(
		err, domain.ErrCurrentPasswordInvalid,
	) {
		t.Fatalf("SwitchToOpen(wrong password) = %v, want current_password_invalid", err)
	}
	if err := h.service.ChangePassword(
		ctx, current, wrongPassword, secondPassword,
	); !errors.Is(err, domain.ErrCurrentPasswordInvalid) {
		t.Fatalf("ChangePassword(wrong current) = %v, want current_password_invalid", err)
	}
	// A wrong sign-in now waits exactly the minimum: the wrong current
	// passwords neither armed a wait nor counted as failed sign-ins.
	_, err := h.service.SignIn(context.Background(), wrongPassword)
	assertRetry(t, err, domain.ErrInvalidPassword, time.Second)
}

func TestRecoveryRequestKeepsOneLiveCode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.enableToken(firstPassword)
	ctx := context.Background()
	h.clock.Advance(time.Hour)
	issuedAt := h.clock.Now()

	issued, expiresAt, err := h.service.RequestRecovery(ctx)
	if err != nil || !issued || !expiresAt.Equal(issuedAt.Add(15*time.Minute)) {
		t.Fatalf("RequestRecovery = %v, %v, %v; want issued, expiring at %v",
			issued, expiresAt, err, issuedAt.Add(15*time.Minute))
	}
	match := recoveryLine.FindStringSubmatch(h.out.String())
	if match == nil {
		t.Fatal("the recovery output is not exactly one recovery code line")
	}
	code := match[1]
	records := h.auditor.Records()
	assertAudit(t, records[switchRows:], operatorOnPanel,
		store.AuditEntry{Action: domain.AuditActionIssueRecoveryCode})
	assertNoSecret(t, records, nil, code, strings.ReplaceAll(code, "-", ""))

	h.out.Reset()
	h.clock.Advance(5 * time.Minute)
	issued, again, err := h.service.RequestRecovery(ctx)
	if err != nil || issued || !again.Equal(expiresAt) {
		t.Errorf("RequestRecovery with a live code = %v, %v, %v; want not "+
			"issued, expiring at %v", issued, again, err, expiresAt)
	}
	if h.out.Len() != 0 {
		t.Error("a request with a live code wrote to the recovery output")
	}
	if got := len(h.auditor.Records()); got != len(records) {
		t.Errorf("audit rows = %d, want %d: a request issued nothing", got, len(records))
	}
}

func TestRecoveryCodeIsIssuedAtMostOnceAMinute(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	burnt := newHarness(t)
	burnt.enableToken(firstPassword)
	burnt.requestCode()
	burnt.clock.Advance(10 * time.Second)
	for range 5 {
		if _, err := burnt.service.ResetWithRecoveryCode(
			ctx, "AAAA-AAAA-AAAA", secondPassword,
		); !errors.Is(err, domain.ErrInvalidRecoveryCode) {
			t.Fatalf("ResetWithRecoveryCode(wrong code) = %v", err)
		}
	}
	burnt.clock.Advance(10 * time.Second)
	_, _, err := burnt.service.RequestRecovery(ctx)
	assertRetry(t, err, domain.ErrTooManyAttempts, 40*time.Second)
	if burnt.out.Len() != 0 {
		t.Error("a refused request wrote to the recovery output")
	}
	burnt.clock.Advance(40 * time.Second)
	burnt.requestCode()

	used := newHarness(t)
	used.enableToken(firstPassword)
	code := used.requestCode()
	used.clock.Advance(10 * time.Second)
	if _, err := used.service.ResetWithRecoveryCode(
		ctx, code, secondPassword,
	); err != nil {
		t.Fatalf("ResetWithRecoveryCode: %v", err)
	}
	used.clock.Advance(20 * time.Second)
	_, _, err = used.service.RequestRecovery(ctx)
	assertRetry(t, err, domain.ErrTooManyAttempts, 30*time.Second)
}

func TestFifthWrongRecoveryCodeBurnsTheCode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.enableToken(firstPassword)
	ctx := context.Background()

	code := h.requestCode()
	var errs []error
	for range 5 {
		_, err := h.service.ResetWithRecoveryCode(ctx, "AAAA-AAAA-AAAA", secondPassword)
		if !errors.Is(err, domain.ErrInvalidRecoveryCode) {
			t.Fatalf("ResetWithRecoveryCode(wrong code) = %v", err)
		}
		errs = append(errs, err)
	}
	_, err := h.service.ResetWithRecoveryCode(ctx, code, secondPassword)
	if !errors.Is(err, domain.ErrInvalidRecoveryCode) {
		t.Fatalf("ResetWithRecoveryCode(burnt code) = %v, want invalid code", err)
	}
	assertNoSecret(t, nil, append(errs, err), code, secondPassword)

	h.clock.Advance(time.Minute)
	code = h.requestCode()
	for range 4 {
		if _, err := h.service.ResetWithRecoveryCode(
			ctx, "AAAA-AAAA-AAAA", secondPassword,
		); !errors.Is(err, domain.ErrInvalidRecoveryCode) {
			t.Fatalf("ResetWithRecoveryCode(wrong code) = %v", err)
		}
	}
	if _, err := h.service.ResetWithRecoveryCode(ctx, code, secondPassword); err != nil {
		t.Errorf("ResetWithRecoveryCode after four wrong codes = %v", err)
	}
}

func TestRecoveryCodeExpiresAfterFifteenMinutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	expired := newHarness(t)
	expired.enableToken(firstPassword)
	code := expired.requestCode()
	expired.clock.Advance(15 * time.Minute)
	if _, err := expired.service.ResetWithRecoveryCode(
		ctx, code, secondPassword,
	); !errors.Is(err, domain.ErrInvalidRecoveryCode) {
		t.Fatalf("ResetWithRecoveryCode(expired code) = %v, want invalid code", err)
	}
	expired.requestCode()

	live := newHarness(t)
	live.enableToken(firstPassword)
	code = live.requestCode()
	live.clock.Advance(15*time.Minute - time.Second)
	if _, err := live.service.ResetWithRecoveryCode(
		ctx, code, secondPassword,
	); err != nil {
		t.Errorf("ResetWithRecoveryCode a second before expiry = %v", err)
	}
}

func TestRefusedRecoveryPasswordKeepsTheCode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.enableToken(firstPassword)
	ctx := context.Background()

	code := h.requestCode()
	for _, attempt := range []string{"AAAA-AAAA-AAAA", code} {
		for range recoveryMaxFailures {
			_, err := h.service.ResetWithRecoveryCode(ctx, attempt, "too short")
			assertValidation(t, err, "/password", "min_length")
		}
	}
	if _, err := h.service.ResetWithRecoveryCode(ctx, code, secondPassword); err != nil {
		t.Errorf("ResetWithRecoveryCode after refused passwords = %v", err)
	}
}

func TestRecoveryResetReplacesPasswordAndSessions(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	first := h.enableToken(firstPassword)
	second := h.addSession()
	tokens := h.store.Snapshot()
	ctx := context.Background()

	code := h.requestCode()
	h.clock.Advance(time.Minute)
	typed := strings.ToLower(strings.Replace(code, "-", " ", 1))
	id, err := h.service.ResetWithRecoveryCode(ctx, typed, secondPassword)
	if err != nil {
		t.Fatalf("ResetWithRecoveryCode(lower case, spaced): %v", err)
	}
	if h.store.sessionCount() != 1 {
		t.Errorf("sessions = %d, want only the new one", h.store.sessionCount())
	}
	if err := h.service.CheckSession(ctx, id); err != nil {
		t.Errorf("CheckSession(new session) = %v", err)
	}
	for _, old := range []string{first, second} {
		if err := h.service.CheckSession(ctx, old); !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("CheckSession(earlier session) = %v, want unauthorized", err)
		}
	}
	if h.store.Snapshot() != tokens {
		t.Error("the reset changed the tokens")
	}
	if got := h.store.storedPassword(t).SetAt; !got.Equal(h.clock.Now()) {
		t.Errorf("password set at %v, want %v", got, h.clock.Now())
	}
	if _, err := h.service.SignIn(ctx, secondPassword); err != nil {
		t.Errorf("SignIn with the new password = %v", err)
	}
	if _, err := h.service.ResetWithRecoveryCode(
		ctx, code, firstPassword,
	); !errors.Is(err, domain.ErrInvalidRecoveryCode) {
		t.Errorf("ResetWithRecoveryCode(used code) = %v, want invalid code", err)
	}
	records := h.auditor.Records()
	assertAudit(t, records[switchRows:], operatorOnPanel,
		store.AuditEntry{Action: domain.AuditActionIssueRecoveryCode},
		store.AuditEntry{Action: domain.AuditActionUseRecoveryCode},
		store.AuditEntry{Action: domain.AuditActionSetPanelPassword},
	)
	assertNoSecret(t, records, nil, code, strings.ReplaceAll(code, "-", ""),
		secondPassword, id)
}

func TestSwitchToOpenNeedsAcknowledgementAndPassword(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id := h.enableToken(firstPassword)
	tokens := h.store.Snapshot()
	ctx := panelCtx()

	err := h.service.SwitchToOpen(ctx, firstPassword, false)
	assertValidation(t, err, "/acknowledged", "required")
	err = h.service.SwitchToOpen(ctx, wrongPassword, true)
	if !errors.Is(err, domain.ErrCurrentPasswordInvalid) {
		t.Fatalf("SwitchToOpen(wrong password) = %v, want current_password_invalid", err)
	}
	if h.store.Snapshot() != tokens || h.store.sessionCount() != 1 {
		t.Fatal("a refused switch changed the stored state")
	}

	if err := h.service.SwitchToOpen(ctx, firstPassword, true); err != nil {
		t.Fatalf("SwitchToOpen: %v", err)
	}
	if got := h.store.Snapshot(); got != (Snapshot{Mode: ModeOpen}) {
		t.Errorf("mode = %q and tokens kept = %t; want open without tokens",
			got.Mode, got.APIToken != "" || got.AgentToken != "")
	}
	if h.store.hasPassword() || h.store.sessionCount() != 0 {
		t.Error("open mode kept the password or a session")
	}
	if err := h.service.CheckToken(TokenAPI, tokens.APIToken); !errors.Is(
		err, domain.ErrUnauthorized,
	) {
		t.Errorf("CheckToken(old API token) = %v, want unauthorized", err)
	}
	if err := h.service.CheckSession(context.Background(), id); !errors.Is(
		err, domain.ErrUnauthorized,
	) {
		t.Errorf("CheckSession(old session) = %v, want unauthorized", err)
	}
	records := h.auditor.Records()
	assertAudit(t, records[switchRows:], panelUser,
		store.AuditEntry{Action: domain.AuditActionSetAccessMode, Detail: "open"})
	assertNoSecret(t, records, nil, firstPassword)
}

func TestChangePasswordKeepsOnlyTheCurrentSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	current := h.enableToken(firstPassword)
	h.addSession()
	ctx := panelCtx()

	err := h.service.ChangePassword(ctx, current, firstPassword, "too short")
	assertValidation(t, err, "/password", "min_length")
	err = h.service.ChangePassword(ctx, current, wrongPassword, secondPassword)
	if !errors.Is(err, domain.ErrCurrentPasswordInvalid) {
		t.Fatalf("ChangePassword(wrong current) = %v, want current_password_invalid", err)
	}
	err = h.service.ChangePassword(ctx, "not a session", firstPassword, secondPassword)
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("ChangePassword(no session) = %v, want unauthorized", err)
	}
	if h.store.sessionCount() != 2 {
		t.Fatal("a refused change ended a session")
	}

	h.clock.Advance(time.Hour)
	if err := h.service.ChangePassword(ctx, current, firstPassword, secondPassword); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, ok := h.store.session(t, current); !ok || h.store.sessionCount() != 1 {
		t.Errorf("sessions = %d, current kept = %t; want only the current one",
			h.store.sessionCount(), ok)
	}
	if got := h.store.storedPassword(t).SetAt; !got.Equal(h.clock.Now()) {
		t.Errorf("password set at %v, want %v", got, h.clock.Now())
	}
	if _, err := h.service.SignIn(context.Background(), secondPassword); err != nil {
		t.Errorf("SignIn with the new password = %v", err)
	}
	records := h.auditor.Records()
	assertAudit(t, records[switchRows:], panelUser,
		store.AuditEntry{Action: domain.AuditActionSetPanelPassword})
	assertNoSecret(t, records, nil, firstPassword, secondPassword, current)
}

func TestChangePasswordBurnsTheRecoveryCode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	current := h.enableToken(firstPassword)
	ctx := context.Background()

	code := h.requestCode()
	if err := h.service.ChangePassword(
		panelCtx(), current, firstPassword, secondPassword,
	); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, err := h.service.ResetWithRecoveryCode(
		ctx, code, firstPassword,
	); !errors.Is(err, domain.ErrInvalidRecoveryCode) {
		t.Errorf("ResetWithRecoveryCode(code issued before the change) = %v, "+
			"want invalid code", err)
	}
	// The minute between codes still runs from the burnt code's issue.
	_, _, err := h.service.RequestRecovery(ctx)
	assertRetry(t, err, domain.ErrTooManyAttempts, time.Minute)
}

func TestRegenerateTokenInvalidatesTheOldValue(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.enableToken(firstPassword)
	ctx := panelCtx()
	before := h.store.Snapshot()

	for _, tc := range []struct {
		kind   TokenKind
		prefix string
		old    string
	}{
		{TokenAPI, "pit_api_", before.APIToken},
		{TokenAgent, "pit_mcp_", before.AgentToken},
	} {
		shown, err := h.service.Token(tc.kind)
		if err != nil || shown != tc.old {
			t.Fatalf("Token(%s) is not the stored token (%v)", tc.kind, err)
		}
		fresh, err := h.service.RegenerateToken(ctx, tc.kind)
		if err != nil {
			t.Fatalf("RegenerateToken(%s): %v", tc.kind, err)
		}
		assertTokenFormat(t, "the regenerated token", fresh, tc.prefix)
		if fresh == tc.old {
			t.Errorf("RegenerateToken(%s) returned the old value", tc.kind)
		}
		if err := h.service.CheckToken(tc.kind, tc.old); !errors.Is(
			err, domain.ErrUnauthorized,
		) {
			t.Errorf("CheckToken(old %s token) is not refused as unauthorized", tc.kind)
		}
		if err := h.service.CheckToken(tc.kind, fresh); err != nil {
			t.Errorf("CheckToken(new %s token) is refused", tc.kind)
		}
		if shown, err := h.service.Token(tc.kind); err != nil || shown != fresh {
			t.Errorf("Token(%s) is not the new token (%v)", tc.kind, err)
		}
	}
	after := h.store.Snapshot()
	records := h.auditor.Records()
	assertAudit(t, records[switchRows:], panelUser,
		store.AuditEntry{Action: domain.AuditActionIssueAccessToken, Detail: "api"},
		store.AuditEntry{Action: domain.AuditActionIssueAccessToken, Detail: "agent"},
	)
	assertNoSecret(t, records, nil, before.APIToken, before.AgentToken,
		after.APIToken, after.AgentToken)

	if _, err := h.service.Token(TokenKind("other")); err == nil {
		t.Error("Token(unknown kind) = <nil>, want an error")
	}
	if _, err := h.service.RegenerateToken(ctx, TokenKind("other")); err == nil {
		t.Error("RegenerateToken(unknown kind) = <nil>, want an error")
	}
	if h.store.Snapshot() != after {
		t.Error("an unknown kind changed a token")
	}
}

func TestOpenModeRefusesTokenModeOperations(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := panelCtx()
	for name, call := range map[string]func() error{
		"SignIn": func() error {
			_, err := h.service.SignIn(ctx, firstPassword)
			return err
		},
		"RequestRecovery": func() error {
			_, _, err := h.service.RequestRecovery(ctx)
			return err
		},
		"ResetWithRecoveryCode": func() error {
			_, err := h.service.ResetWithRecoveryCode(ctx, "AAAA-AAAA-AAAA", firstPassword)
			return err
		},
		"ChangePassword": func() error {
			return h.service.ChangePassword(ctx, "", firstPassword, secondPassword)
		},
		"Token": func() error {
			_, err := h.service.Token(TokenAPI)
			return err
		},
		"RegenerateToken": func() error {
			_, err := h.service.RegenerateToken(ctx, TokenAgent)
			return err
		},
		"SwitchToOpen": func() error {
			return h.service.SwitchToOpen(ctx, firstPassword, true)
		},
	} {
		if err := call(); !errors.Is(err, domain.ErrConflict) {
			t.Errorf("%s in open mode = %v, want conflict", name, err)
		}
	}
	if h.out.Len() != 0 || len(h.auditor.Records()) != 0 {
		t.Error("a refused operation wrote a recovery code or an audit row")
	}

	h.enableToken(firstPassword)
	if _, err := h.service.SwitchToToken(ctx, secondPassword); !errors.Is(
		err, domain.ErrConflict,
	) {
		t.Errorf("SwitchToToken in token mode = %v, want conflict", err)
	}
}

func TestAuthenticatedOperationWithoutCallerIsInternalError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	_, err := h.service.SwitchToToken(ctx, firstPassword)
	assertInternal(t, err)
	if h.service.Mode() != ModeOpen {
		t.Fatal("SwitchToToken without a caller changed the mode")
	}

	session := h.enableToken(firstPassword)
	before := h.store.Snapshot()
	for name, call := range map[string]func() error{
		"SwitchToOpen": func() error {
			return h.service.SwitchToOpen(ctx, firstPassword, true)
		},
		"ChangePassword": func() error {
			return h.service.ChangePassword(ctx, session, firstPassword, secondPassword)
		},
		"RegenerateToken": func() error {
			_, err := h.service.RegenerateToken(ctx, TokenAPI)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			assertInternal(t, call())
		})
	}
	if h.store.Snapshot() != before || len(h.auditor.Records()) != switchRows {
		t.Error("an operation without a caller changed the state or audited")
	}
}

func TestAuditFailureKeepsTheChange(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	failure := errors.New("audit log unavailable")
	h.auditor.Fail(failure)
	if _, err := h.service.SwitchToToken(panelCtx(), firstPassword); !errors.Is(
		err, failure,
	) {
		t.Fatalf("SwitchToToken with a failing audit = %v, want %v", err, failure)
	}
	if h.service.Mode() != ModeToken {
		t.Error("the change was not kept when its audit failed")
	}
}

func TestSwitchToTokenForgetsEarlierRecoveryCode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.enableToken(firstPassword)
	code := h.requestCode()
	if err := h.service.SwitchToOpen(panelCtx(), firstPassword, true); err != nil {
		t.Fatalf("SwitchToOpen: %v", err)
	}
	h.enableToken(secondPassword)

	if _, err := h.service.ResetWithRecoveryCode(
		context.Background(), code, firstPassword,
	); !errors.Is(err, domain.ErrInvalidRecoveryCode) {
		t.Errorf("ResetWithRecoveryCode(earlier code) = %v, want invalid code", err)
	}
	h.requestCode()
}

func TestAccessInfo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	info, err := h.service.AccessInfo(ctx)
	if err != nil || info != (AccessInfo{Mode: ModeOpen}) {
		t.Errorf("AccessInfo in open mode = %+v, %v; want open, no password, "+
			"no sessions", info, err)
	}

	h.clock.Advance(time.Hour)
	setAt := h.clock.Now()
	h.enableToken(firstPassword)
	for _, session := range []Session{
		// Idle for eight days.
		{IDHash: [32]byte{1}, LastSeenAt: setAt.Add(-8 * day), ExpiresAt: setAt.Add(day)},
		// Past its absolute end.
		{IDHash: [32]byte{2}, LastSeenAt: setAt, ExpiresAt: setAt},
	} {
		if err := h.store.CreateSession(ctx, session); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
	}
	info, err = h.service.AccessInfo(ctx)
	if err != nil || info.Mode != ModeToken || info.Sessions != 1 ||
		info.PasswordSetAt == nil || !info.PasswordSetAt.Equal(setAt) {
		t.Errorf("AccessInfo in token mode = %+v, %v; want token, password set "+
			"at %v, one live session", info, err, setAt)
	}
}

func TestSignOut(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	first := h.enableToken(firstPassword)
	second := h.addSession()
	ctx := context.Background()

	for _, cookie := range []string{first, first, "not a session"} {
		if err := h.service.SignOut(ctx, cookie); err != nil {
			t.Fatalf("SignOut = %v, want it idempotent", err)
		}
	}
	if _, ok := h.store.session(t, first); ok {
		t.Error("SignOut kept the session")
	}
	if _, ok := h.store.session(t, second); !ok {
		t.Error("SignOut ended another session")
	}
	if err := h.service.SignOutEverywhere(ctx); err != nil {
		t.Fatalf("SignOutEverywhere: %v", err)
	}
	if h.store.sessionCount() != 0 {
		t.Error("SignOutEverywhere kept a session")
	}
}

// TestConcurrentOperationsShareState runs the operations that change the
// Service's own state - the sign-in delay and the recovery code - alongside
// the unlocked credential checks. Under the race detector it fails when one
// of them reaches that state outside the mutex.
func TestConcurrentOperationsShareState(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	session := h.enableToken(firstPassword)
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 3 {
				_, err := h.service.SignIn(ctx, wrongPassword)
				if !errors.Is(err, domain.ErrInvalidPassword) &&
					!errors.Is(err, domain.ErrTooManyAttempts) {
					t.Errorf("SignIn(wrong password) = %v", err)
				}
				// Once wrong codes burn the code, the minute between codes
				// refuses a new one.
				_, _, err = h.service.RequestRecovery(ctx)
				if err != nil && !errors.Is(err, domain.ErrTooManyAttempts) {
					t.Errorf("RequestRecovery: %v", err)
				}
				_, err = h.service.ResetWithRecoveryCode(ctx, "AAAA-AAAA-AAAA", secondPassword)
				if !errors.Is(err, domain.ErrInvalidRecoveryCode) {
					t.Errorf("ResetWithRecoveryCode(wrong code) = %v", err)
				}
				if err := h.service.CheckSession(ctx, session); err != nil {
					t.Errorf("CheckSession: %v", err)
				}
			}
		})
	}
	wg.Wait()
}

// TestSignInHoldsTheLockAcrossVerifyAndSession probes the Service's lock at
// two points of a sign-in: once it has read the password, and before it
// writes its session. Where the lock is free, a password change runs to its
// end right there, and the session the sign-in then writes would outlive the
// change. The test pins that the sign-in holds the lock at both points; it
// does not see the lock released and taken again between them.
func TestSignInHoldsTheLockAcrossVerifyAndSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	current := h.enableToken(firstPassword)

	// change runs the password change once, from another goroutine, and
	// waits for its end.
	var changed atomic.Bool
	change := func() {
		if changed.Swap(true) {
			return
		}
		done := make(chan error, 1)
		go func() {
			done <- h.service.ChangePassword(
				panelCtx(), current, firstPassword, secondPassword,
			)
		}()
		if err := <-done; err != nil {
			t.Errorf("ChangePassword: %v", err)
		}
	}
	// lockFree reports whether nothing holds the Service's lock right now.
	lockFree := func() bool {
		if !h.service.mu.TryLock() {
			return false
		}
		h.service.mu.Unlock()
		return true
	}
	var reads atomic.Int32
	h.store.setAfterPasswordRead(func() {
		// Only the first read is the sign-in's.
		if reads.Add(1) == 1 && lockFree() {
			change()
		}
	})
	h.store.setBeforeSessionWrite(func() {
		if lockFree() {
			change()
		}
	})

	id, err := h.service.SignIn(context.Background(), firstPassword)
	// With the lock held at both points, the change runs after the sign-in.
	change()

	switch {
	case err == nil:
		if _, ok := h.store.session(t, id); ok {
			t.Error("the sign-in kept its session after a password change that " +
				"ran while it did not hold the lock")
		}
	case !errors.Is(err, domain.ErrInvalidPassword):
		t.Errorf("SignIn = %v, want a session or invalid_password", err)
	}
	if _, ok := h.store.session(t, current); !ok {
		t.Error("the password change ended its own session")
	}
}
