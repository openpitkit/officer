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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func newSetupHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.store.seedSetup()
	return h
}

func setupRouter(t *testing.T, h *harness) http.Handler {
	t.Helper()
	panel := &httpx.RouteRegistry{}
	for _, route := range Routes(h.service) {
		panel.Register(route)
	}
	return newTestRouter(t, &httpx.RouteRegistry{}, panel, h.service.ResolveCaller, nil)
}

func TestSetupBookmarksAndEncryption(t *testing.T) {
	h := newSetupHarness(t)
	state, err := h.service.SetupState(context.Background())
	if err != nil || state.Step != SetupStepWelcome || state.EncryptionDeclinedAt != nil {
		t.Fatalf("initial SetupState = %+v, %v", state, err)
	}
	for _, step := range []SetupStep{
		SetupStepAccess, SetupStepWelcome, SetupStepAccounts, SetupStepEncryption,
		SetupStepSigning, SetupStepSigning,
	} {
		if err := h.service.SetSetupStep(context.Background(), step); err != nil {
			t.Fatalf("SetSetupStep(%q): %v", step, err)
		}
		state, err := h.service.SetupState(context.Background())
		if err != nil || state.Step != step {
			t.Fatalf("saved step = %q, %v, want %q", state.Step, err, step)
		}
	}
	assertValidation(t, h.service.SetSetupStep(context.Background(), "agent"), "/step", "enum")
	assertValidation(t, h.service.DeclineEncryption(context.Background(), false), "/acknowledged", "required")
	for range 2 {
		if err := h.service.DeclineEncryption(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(time.Hour)
	}
	state, err = h.service.SetupState(context.Background())
	if err != nil || state.EncryptionDeclinedAt == nil || !state.EncryptionDeclinedAt.Equal(start) {
		t.Fatalf("first encryption decline = %+v, %v, want %v", state, err, start)
	}
	if len(h.auditor.Records()) != 0 {
		t.Fatal("bookmarks and encryption declines wrote audit entries")
	}
}

func TestDeclineEncryptionRefusesSealedDatabase(t *testing.T) {
	h := newSetupHarness(t)
	h.decisions.sealed = true
	err := h.service.DeclineEncryption(context.Background(), true)
	if !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "master key is already in effect") {
		t.Fatalf("DeclineEncryption(sealed) = %v, want conflict naming the active master key", err)
	}
	state, readErr := h.service.SetupState(context.Background())
	if readErr != nil || state.EncryptionDeclinedAt != nil {
		t.Fatalf("sealed database recorded an encryption decline: %+v, %v", state, readErr)
	}
}

func TestCompleteSetupRequiresBothDecisions(t *testing.T) {
	for _, mode := range []Mode{ModeOpen, ModeToken} {
		for _, tc := range []struct {
			name           string
			chosen, sealed bool
			missing        []string
		}{
			{"both missing", false, false, []string{"account mode", "encryption"}},
			{"encryption missing", true, false, []string{"encryption"}},
			{"account mode missing", false, true, []string{"account mode"}},
		} {
			t.Run(string(mode)+"/"+tc.name, func(t *testing.T) {
				h := newSetupHarness(t)
				h.decisions.chosen, h.decisions.sealed = tc.chosen, tc.sealed
				id, err := h.service.CompleteSetup(panelCtx(), mode, firstPassword, true)
				if !errors.Is(err, domain.ErrConflict) {
					t.Fatalf("CompleteSetup = %v, want conflict for missing decisions", err)
				}
				for _, name := range tc.missing {
					if !strings.Contains(err.Error(), name) {
						t.Errorf("completion conflict = %q, want missing %s", err.Error(), name)
					}
				}
				for _, name := range []string{"account mode", "encryption"} {
					if !slices.Contains(tc.missing, name) && strings.Contains(err.Error(), name) {
						t.Errorf("completion conflict names already-made decision %s", name)
					}
				}
				if id != "" || h.service.Mode() != ModeSetup || h.store.hasPassword() ||
					h.store.sessionCount() != 0 || len(h.auditor.Records()) != 0 {
					t.Fatal("incomplete decisions published access credentials or audit entries")
				}
			})
		}
	}
}

func TestSetupDecisionReadFailure(t *testing.T) {
	h := newSetupHarness(t)
	failure := errors.New("account mode unavailable")
	h.decisions.err = failure
	if _, err := h.service.CompleteSetup(panelCtx(), ModeToken, firstPassword, false); !errors.Is(err, failure) {
		t.Fatalf("CompleteSetup error = %v, want the account mode failure", err)
	}
	if h.service.Mode() != ModeSetup || h.store.hasPassword() {
		t.Fatal("failed account mode read changed access")
	}
}

func TestCompleteSetupOpen(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		t.Run(fmt.Sprint(sealed), func(t *testing.T) {
			h := newSetupHarness(t)
			h.decisions.chosen, h.decisions.sealed = true, sealed
			if !sealed {
				if err := h.service.DeclineEncryption(panelCtx(), true); err != nil {
					t.Fatal(err)
				}
			}
			id, err := h.service.CompleteSetup(panelCtx(), ModeOpen, "", true)
			if err != nil || id != "" || h.service.Mode() != ModeOpen || h.store.hasPassword() {
				t.Fatalf("CompleteSetup(open) mode = %s, error = %v", h.service.Mode(), err)
			}
			assertAudit(t, h.auditor.Records(), panelUser,
				store.AuditEntry{Action: domain.AuditActionSetAccessMode, Detail: "open"},
			)
		})
	}
}

func TestCompleteSetupTokenIssuesCredentialsAndClearsRecovery(t *testing.T) {
	h := newSetupHarness(t)
	h.decisions.chosen, h.decisions.sealed = true, true
	h.service.recovery = recoveryCode{code: "earlier-code", issuedAt: start, expiresAt: start.Add(time.Hour)}
	id, err := h.service.CompleteSetup(panelCtx(), ModeToken, firstPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := h.store.Snapshot()
	if snapshot.Mode != ModeToken {
		t.Fatalf("completed mode = %s, want token", snapshot.Mode)
	}
	assertTokenFormat(t, "API token", snapshot.APIToken, "pit_api_")
	assertTokenFormat(t, "agent token", snapshot.AgentToken, "pit_mcp_")
	if snapshot.APIToken == snapshot.AgentToken {
		t.Fatal("completion issued the same token twice")
	}
	password := h.store.storedPassword(t)
	matches, err := verifyPassword(password.Hash, firstPassword)
	if err != nil || !matches || !password.SetAt.Equal(start) {
		t.Fatal("completion did not hash and timestamp the password")
	}
	session, ok := h.store.session(t, id)
	if !ok || !session.CreatedAt.Equal(start) || !session.LastSeenAt.Equal(start) ||
		!session.ExpiresAt.Equal(start.Add(sessionLifetime)) {
		t.Fatal("completion did not store the browser session with its lifetime")
	}
	if err := h.service.CheckSession(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if h.service.recovery != (recoveryCode{}) {
		t.Fatal("completion kept an earlier recovery code")
	}
	assertAudit(t, h.auditor.Records(), panelUser,
		store.AuditEntry{Action: domain.AuditActionSetAccessMode, Detail: "token"},
		store.AuditEntry{Action: domain.AuditActionSetPanelPassword},
		store.AuditEntry{Action: domain.AuditActionIssueAccessToken, Detail: "api"},
		store.AuditEntry{Action: domain.AuditActionIssueAccessToken, Detail: "agent"},
	)
	assertNoSecret(t, h.auditor.Records(), nil, firstPassword, password.Hash, snapshot.APIToken, snapshot.AgentToken, id)
}

func TestSetupOperationsRefuseCompletedStateBeforeInput(t *testing.T) {
	for _, mode := range []Mode{ModeOpen, ModeToken} {
		t.Run(string(mode), func(t *testing.T) {
			h := newHarness(t)
			if mode == ModeToken {
				h.enableToken(firstPassword)
			}
			h.decisions.err = errors.New("must not read decisions after completion")
			ctx := context.Background()
			_, stateErr := h.service.SetupState(ctx)
			_, completeErr := h.service.CompleteSetup(ctx, "unknown", "", false)
			for name, err := range map[string]error{
				"state":      stateErr,
				"progress":   h.service.SetSetupStep(ctx, "invalid"),
				"encryption": h.service.DeclineEncryption(ctx, false),
				"complete":   completeErr,
			} {
				if !errors.Is(err, domain.ErrSetupComplete) {
					t.Errorf("%s after completion = %v, want setup_complete before invalid input or missing caller", name, err)
				}
				if err != domain.ErrSetupComplete {
					t.Errorf("%s after completion = %v, want bare setup_complete", name, err)
				}
			}
		})
	}
}

func TestSetupRoutesHappyPaths(t *testing.T) {
	h := newSetupHarness(t)
	router := setupRouter(t, h)
	expectJSON(t, router, panelCall{method: "GET", path: "/session"}, map[string]any{
		"accessMode": "setup", "setupComplete": false, "authenticated": false,
	})
	expectJSON(t, router, panelCall{method: "GET", path: "/setup"}, map[string]any{
		"step": "welcome", "encryptionDeclinedAt": nil,
	})
	for _, step := range []string{"access", "welcome", "accounts", "encryption", "signing", "signing"} {
		expectStatus(t, router, panelCall{method: "PUT", path: "/setup/progress",
			body: jsonBody(t, map[string]any{"step": step})}, 204)
	}
	for range 2 {
		expectStatus(t, router, panelCall{method: "PUT", path: "/setup/encryption",
			body: `{"encryption":"none","acknowledged":true}`}, 204)
		h.clock.Advance(time.Hour)
	}
	expectJSON(t, router, panelCall{method: "GET", path: "/setup"}, map[string]any{
		"step": "signing", "encryptionDeclinedAt": start.Format(time.RFC3339),
	})
	h.decisions.chosen = true
	rec := expectStatus(t, router, panelCall{method: "POST", path: "/setup/complete",
		body: `{"access":"open","acknowledged":true}`}, 204)
	if rec.Body.Len() != 0 || len(rec.Result().Cookies()) != 0 {
		t.Fatal("open completion returned a body or a session cookie")
	}
}

func TestSetupRoutesValidation(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, pointer string
	}{
		{"missing step", "/setup/progress", `{}`, "/step"},
		{"null step", "/setup/progress", `{"step":null}`, "/step"},
		{"invalid step", "/setup/progress", `{"step":"agent"}`, "/step"},
		{"missing encryption", "/setup/encryption", `{"acknowledged":true}`, "/encryption"},
		{"null encryption", "/setup/encryption", `{"encryption":null,"acknowledged":true}`, "/encryption"},
		{"invalid encryption", "/setup/encryption", `{"encryption":"key","acknowledged":true}`, "/encryption"},
		{"missing encryption ack", "/setup/encryption", `{"encryption":"none"}`, "/acknowledged"},
		{"null encryption ack", "/setup/encryption", `{"encryption":"none","acknowledged":null}`, "/acknowledged"},
		{"false encryption ack", "/setup/encryption", `{"encryption":"none","acknowledged":false}`, "/acknowledged"},
		{"missing access", "/setup/complete", `{}`, "/access"},
		{"invalid access", "/setup/complete", `{"access":"setup"}`, "/access"},
		{"missing password", "/setup/complete", `{"access":"token"}`, "/password"},
		{"null password", "/setup/complete", `{"access":"token","password":null}`, "/password"},
		{"short password", "/setup/complete", jsonBody(t, map[string]any{"access": "token", "password": strings.Repeat("a", 14)}), "/password"},
		{"long password", "/setup/complete", jsonBody(t, map[string]any{"access": "token", "password": strings.Repeat("a", 257)}), "/password"},
		{"token takes no ack", "/setup/complete", jsonBody(t, map[string]any{"access": "token", "password": firstPassword, "acknowledged": true}), "/acknowledged"},
		{"open takes no password", "/setup/complete", jsonBody(t, map[string]any{"access": "open", "password": firstPassword, "acknowledged": true}), "/password"},
		{"missing open ack", "/setup/complete", `{"access":"open"}`, "/acknowledged"},
		{"null open ack", "/setup/complete", `{"access":"open","acknowledged":null}`, "/acknowledged"},
		{"false open ack", "/setup/complete", `{"access":"open","acknowledged":false}`, "/acknowledged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSetupHarness(t)
			h.decisions.chosen, h.decisions.sealed = true, true
			method := http.MethodPut
			if tc.path == "/setup/complete" {
				method = http.MethodPost
			}
			rec := expectRefusal(t, setupRouter(t, h), panelCall{method: method, path: tc.path, body: tc.body},
				422, "validation", tc.pointer)
			if rec.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("validation Content-Type = %q, want application/problem+json", rec.Header().Get("Content-Type"))
			}
			if tc.name == "false encryption ack" || tc.name == "false open ack" {
				var problem struct {
					Errors []struct {
						Constraint string `json:"constraint"`
					} `json:"errors"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
					t.Fatal(err)
				}
				if len(problem.Errors) != 1 || problem.Errors[0].Constraint != "required" {
					t.Fatalf("acknowledgement errors = %+v, want required", problem.Errors)
				}
			}
			if h.service.Mode() != ModeSetup || h.store.hasPassword() || h.store.sessionCount() != 0 {
				t.Fatal("invalid setup body changed access state")
			}
		})
	}
}

func TestSetupCompletionValidationBeforeDecisions(t *testing.T) {
	for _, tc := range []struct {
		name, body, pointer string
	}{
		{"token", `{"access":"token","password":"short"}`, "/password"},
		{"open", `{"access":"open","acknowledged":false}`, "/acknowledged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSetupHarness(t)
			expectRefusal(t, setupRouter(t, h), panelCall{
				method: "POST", path: "/setup/complete", body: tc.body,
			}, 422, "validation", tc.pointer)
		})
	}
}

func TestSetupRoutesConflicts(t *testing.T) {
	h := newSetupHarness(t)
	router := setupRouter(t, h)
	call := panelCall{method: "POST", path: "/setup/complete", body: `{"access":"open","acknowledged":true}`}
	for _, tc := range []struct {
		chosen, sealed bool
		missing        []string
	}{
		{false, false, []string{"account mode", "encryption"}},
		{true, false, []string{"encryption"}},
		{false, true, []string{"account mode"}},
	} {
		h.decisions.chosen, h.decisions.sealed = tc.chosen, tc.sealed
		rec := expectRefusal(t, router, call, 409, "conflict", "")
		for _, name := range tc.missing {
			if !strings.Contains(rec.Body.String(), name) {
				t.Errorf("completion conflict does not name %s", name)
			}
		}
		for _, name := range []string{"account mode", "encryption"} {
			if !slices.Contains(tc.missing, name) && strings.Contains(rec.Body.String(), name) {
				t.Errorf("completion conflict names already-made decision %s", name)
			}
		}
	}
	rec := expectRefusal(t, router, panelCall{method: "PUT", path: "/setup/encryption",
		body: `{"encryption":"none","acknowledged":true}`}, 409, "conflict", "")
	if !strings.Contains(rec.Body.String(), "master key is already in effect") {
		t.Fatal("sealed encryption refusal does not explain the active master key")
	}
}

func TestSetupRoutesAfterCompletionRefuseMalformedBody(t *testing.T) {
	for _, mode := range []Mode{ModeOpen, ModeToken} {
		t.Run(string(mode), func(t *testing.T) {
			h := newSetupHarness(t)
			h.decisions.chosen, h.decisions.sealed = true, true
			router := setupRouter(t, h)
			body := `{"access":"open","acknowledged":true}`
			if mode == ModeToken {
				body = jsonBody(t, map[string]any{"access": "token", "password": firstPassword})
			}
			call := panelCall{method: "POST", path: "/setup/complete", body: body}
			rec := expectStatus(t, router, call, 204)
			var cookie string
			if mode == ModeToken {
				cookie = issuedSession(t, call, rec)
				snapshot := h.store.Snapshot()
				if err := h.service.CheckToken(TokenAPI, snapshot.APIToken); err != nil {
					t.Fatal(err)
				}
				if err := h.service.CheckToken(TokenAgent, snapshot.AgentToken); err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{firstPassword, snapshot.APIToken, snapshot.AgentToken, cookie} {
					if strings.Contains(rec.Body.String(), secret) {
						t.Fatal("completion response body contains a credential")
					}
				}
			}
			if rec.Body.Len() != 0 {
				t.Fatal("setup completion returned a response body")
			}
			for _, call := range []panelCall{
				{method: "GET", path: "/setup", body: "{", cookie: cookie},
				{method: "PUT", path: "/setup/progress", body: "{", cookie: cookie},
				{method: "PUT", path: "/setup/encryption", body: "{", cookie: cookie},
				{method: "POST", path: "/setup/complete", body: "{", cookie: cookie},
			} {
				expectRefusal(t, router, call, 409, "setup_complete", "")
			}
		})
	}
}

func TestSetupPublicSignInAndRecoveryRefuseBeforeBody(t *testing.T) {
	h := newSetupHarness(t)
	router := setupRouter(t, h)
	for _, path := range []string{"/session", "/session/recovery", "/session/recovery/reset"} {
		t.Run(path, func(t *testing.T) {
			rec := expectRefusal(t, router, panelCall{method: "POST", path: path, body: "{"}, 503, "setup_required", "")
			assertAddressedSetupMessage(t, rec.Body.Bytes(), "127.0.0.1")
		})
	}
}

func assertAddressedSetupMessage(t *testing.T, body []byte, host string) {
	t.Helper()
	var answer struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatal(err)
	}
	want := "Pit Officer is not set up yet. Open http://" + host +
		" in a browser on this computer and finish the first-run setup."
	if answer.Error.Message != want {
		t.Fatalf("setup_required message = %q, want exactly %q", answer.Error.Message, want)
	}
}

func TestResolveCallerDuringSetup(t *testing.T) {
	h := newSetupHarness(t)
	for _, source := range []domain.Source{domain.SourceAPI, domain.SourceMCP, domain.SourcePanel, domain.SourceSystem} {
		t.Run(string(source), func(t *testing.T) {
			ctx := httpx.ContextWithSurface(context.Background(), source)
			r := httptest.NewRequestWithContext(ctx, "GET", "http://localhost:8787/probe", nil)
			r.Header.Set("Authorization", "Bearer ignored-in-setup")
			r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "ignored-in-setup"})
			caller, err := h.service.ResolveCaller(r)
			switch source {
			case domain.SourceAPI, domain.SourceMCP, domain.SourcePanel:
				if !errors.Is(err, domain.ErrSetupRequired) || caller != (domain.Caller{}) {
					t.Fatalf("setup %s resolved a caller or returned %v, want setup_required", source, err)
				}
				want := "Pit Officer is not set up yet. Open http://localhost:8787 in a browser on this computer and finish the first-run setup."
				if err.Error() != want {
					t.Fatalf("addressed setup error = %q, want exactly %q", err.Error(), want)
				}
				public := httpx.DescribeErr(err)
				if public.Status != 503 || public.Code != "setup_required" || public.Message != want {
					t.Fatalf("DescribeErr(setup) = %+v, want exact addressed 503 setup_required", public)
				}
			default:
				if err == nil || errors.Is(err, domain.ErrSetupRequired) {
					t.Fatalf("unknown setup surface = %v, want an internal error", err)
				}
			}
		})
	}
}

func TestSetupPanelRouteAdmission(t *testing.T) {
	h := newSetupHarness(t)
	router := setupRouter(t, h)
	expectStatus(t, router, panelCall{method: "GET", path: "/setup"}, 200)
	rec := expectRefusal(t, router, panelCall{
		method: "GET", path: "/access/tokens/api",
	}, 503, "setup_required", "")
	assertAddressedSetupMessage(t, rec.Body.Bytes(), "127.0.0.1")
}

type setupThenTokenStore struct {
	Store
	reads int
}

func (s *setupThenTokenStore) Snapshot() Snapshot {
	s.reads++
	if s.reads == 1 {
		return Snapshot{Mode: ModeSetup}
	}
	return s.Store.Snapshot()
}

func TestSetupPanelAdmissionBeforeTokenCompletion(t *testing.T) {
	h := newHarness(t)
	h.enableToken(firstPassword)
	transition := &setupThenTokenStore{Store: h.store}
	h.service.store = transition
	panel := &httpx.RouteRegistry{}
	for _, route := range Routes(h.service) {
		if !route.Public {
			route.Handler = httpx.AuthorizeMiddleware(h.service, route.ID)(route.Handler)
		}
		panel.Register(route)
	}
	router := newTestRouter(t, &httpx.RouteRegistry{}, panel, h.service.ResolveCaller, nil)
	rec := expectRefusal(t, router, panelCall{
		method: "GET", path: "/access/tokens/api",
	}, 503, "setup_required", "")
	assertAddressedSetupMessage(t, rec.Body.Bytes(), "127.0.0.1")
	if transition.reads != 1 || transition.Snapshot().Mode != ModeToken {
		t.Fatal("setup refusal must use one read before the token snapshot")
	}
}

func TestAuthorizeModeSourceAndSetupFlag(t *testing.T) {
	for _, mode := range []Mode{ModeSetup, ModeOpen, ModeToken, "unknown"} {
		for _, source := range []domain.Source{domain.SourcePanel, domain.SourceAPI, domain.SourceMCP, domain.SourceSystem, ""} {
			for _, flag := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/%t", mode, source, flag), func(t *testing.T) {
					h := newSetupHarness(t)
					h.store.snapshot.Mode = mode
					var result error
					panel := &httpx.RouteRegistry{}
					panel.Register(httpx.Route{
						ID: "authorize.get", Method: "GET", Pattern: "/authorize", DuringSetup: flag,
						Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							result = h.service.Authorize(r.Context(), domain.Caller{Source: source}, "probe")
							w.WriteHeader(http.StatusNoContent)
						}),
					})
					// The resolver needs no credential for this authorizer-only table.
					router := newTestRouter(t, &httpx.RouteRegistry{}, panel,
						func(*http.Request) (domain.Caller, error) { return operator, nil }, nil)
					expectStatus(t, router, panelCall{method: "GET", path: "/authorize"}, 204)
					switch {
					case mode == ModeOpen || mode == ModeToken:
						if result != nil {
							t.Fatalf("Authorize(%s,%s,%t) = %v, want allowed", mode, source, flag, result)
						}
					case mode == ModeSetup && (source == domain.SourceMCP || source == domain.SourcePanel && flag):
						if result != nil {
							t.Fatalf("Authorize(%s,%s,%t) = %v, want allowed", mode, source, flag, result)
						}
					case mode == ModeSetup && (source == domain.SourceAPI || source == domain.SourcePanel):
						if result != domain.ErrSetupRequired {
							t.Fatalf("Authorize(%s,%s,%t) = %v, want bare setup_required", mode, source, flag, result)
						}
					default:
						if result == nil || errors.Is(result, domain.ErrSetupRequired) {
							t.Fatalf("Authorize(%s,%s,%t) = %v, want an internal error", mode, source, flag, result)
						}
					}
				})
			}
		}
	}
}

func TestSetupStatusAndModePreconditions(t *testing.T) {
	h := newSetupHarness(t)
	status, err := h.service.Status(context.Background(), "ignored")
	if err != nil || status != (Status{Mode: ModeSetup}) {
		t.Fatalf("Status(setup) = %+v, %v, want setup incomplete and unauthenticated", status, err)
	}
	_, signInErr := h.service.SignIn(context.Background(), firstPassword)
	_, switchErr := h.service.SwitchToToken(panelCtx(), firstPassword)
	_, recoveryErr := h.service.ResetWithRecoveryCode(context.Background(), "", "")
	_, _, requestErr := h.service.RequestRecovery(context.Background())
	_, infoErr := h.service.AccessInfo(context.Background())
	for name, err := range map[string]error{
		"requireMode": requireMode(ModeSetup, ModeOpen),
		"token":       h.service.CheckToken(TokenAPI, ""),
		"session":     h.service.CheckSession(context.Background(), ""),
		"sign in":     signInErr, "switch": switchErr, "recovery": recoveryErr, "request": requestErr, "info": infoErr,
	} {
		if err != domain.ErrSetupRequired {
			t.Errorf("%s in setup = %v, want bare setup_required", name, err)
		}
	}
}

type failingSetupCommitStore struct {
	Store
	failure error
}

func (s failingSetupCommitStore) CompleteSetupOpen(context.Context, time.Time) error {
	return s.failure
}

func (s failingSetupCommitStore) CompleteSetupToken(context.Context, Credentials, Session, time.Time) error {
	return s.failure
}

func TestSetupCompletionCommitFailureDoesNotPublishMode(t *testing.T) {
	for _, mode := range []Mode{ModeOpen, ModeToken} {
		t.Run(string(mode), func(t *testing.T) {
			h := newSetupHarness(t)
			h.decisions.chosen, h.decisions.sealed = true, true
			failure := errors.New("completion commit failed")
			h.service.store = failingSetupCommitStore{Store: h.store, failure: failure}
			id, err := h.service.CompleteSetup(panelCtx(), mode, firstPassword, true)
			if !errors.Is(err, failure) || id != "" {
				t.Fatalf("failed completion = %v, want the commit error without a session id", err)
			}
			if h.service.Mode() != ModeSetup || h.store.hasPassword() ||
				h.store.sessionCount() != 0 || len(h.auditor.Records()) != 0 {
				t.Fatal("failed completion published a mode, credential, session or audit entry")
			}
		})
	}
}

func TestSetupCompletionAuditFailureKeepsCommittedMode(t *testing.T) {
	for _, mode := range []Mode{ModeOpen, ModeToken} {
		t.Run(string(mode), func(t *testing.T) {
			h := newSetupHarness(t)
			h.decisions.chosen, h.decisions.sealed = true, true
			failure := errors.New("audit unavailable")
			h.auditor.Fail(failure)
			id, err := h.service.CompleteSetup(panelCtx(), mode, firstPassword, true)
			if !errors.Is(err, failure) || id != "" || h.service.Mode() != mode {
				t.Fatalf("audit failure = %v, mode = %s, want committed %s without a session id", err, h.service.Mode(), mode)
			}
		})
	}
}

func TestUnknownModeFailsClosed(t *testing.T) {
	h := newHarness(t)
	h.store.snapshot.Mode = "unknown"
	_, statusErr := h.service.Status(context.Background(), "")
	_, infoErr := h.service.AccessInfo(context.Background())
	_, setupErr := h.service.SetupState(context.Background())
	_, resolveErr := h.service.ResolveCaller(httptest.NewRequestWithContext(
		httpx.ContextWithSurface(context.Background(), domain.SourcePanel), "GET", "http://127.0.0.1/probe", nil,
	))
	for name, err := range map[string]error{
		"status": statusErr, "info": infoErr, "setup": setupErr, "resolver": resolveErr,
		"requireMode": requireMode("unknown", ModeOpen),
		"token":       h.service.CheckToken(TokenAPI, ""),
		"session":     h.service.CheckSession(context.Background(), ""),
	} {
		if err == nil || errors.Is(err, domain.ErrSetupRequired) || errors.Is(err, domain.ErrSetupComplete) {
			t.Errorf("%s with unknown mode = %v, want an internal error", name, err)
		}
	}
}
