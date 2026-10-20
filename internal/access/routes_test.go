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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// The attributes of the session cookie the panel routes set and clear, in
// the order net/http writes them.
const (
	issuedCookieAttributes  = "Path=/; Max-Age=2592000; HttpOnly; SameSite=Strict"
	clearedCookieAttributes = "Path=/; Max-Age=0; HttpOnly; SameSite=Strict"
)

// panelCall is one request to the panel API: its method, its path under
// /app/api/v1, its JSON body, empty for none, and the session cookie it
// carries, empty for none.
type panelCall struct {
	method string
	path   string
	body   string
	cookie string
}

// String names the call without its body or cookie, for a failure message.
func (c panelCall) String() string { return c.method + " " + c.path }

// TestPanelAccessRoutesFollowContract drives the panel access routes behind
// the framework router and the access resolver, over the in-memory store:
// open mode answers GET /session and GET /access and refuses what only token
// mode holds; PUT /access/mode validates its body, refusing the other mode's
// members, and switches to token mode with the session cookie set; the cookie
// authenticates the panel, which reads its access state and both tokens, by
// an escaped kind too, and regenerates one; sign-in delays carry Retry-After,
// rounded up; password recovery prints one code, resets the password with it
// and issues no new code within the minute; a wrong current password is
// refused, and a password change keeps the request's session and lets only
// the new password sign in; signing out clears the cookie and ends the
// session; and switching back to open mode clears it.
func TestPanelAccessRoutesFollowContract(t *testing.T) {
	h := newHarness(t)
	panelRoutes := &httpx.RouteRegistry{}
	for _, route := range Routes(h.service) {
		panelRoutes.Register(route)
	}
	router := newTestRouter(
		t, &httpx.RouteRegistry{}, panelRoutes, h.service.ResolveCaller, nil,
	)
	// Each step builds on the state the previous ones left, so the first
	// failing step ends the test.
	step := func(name string, run func(t *testing.T)) {
		t.Helper()
		if !t.Run(name, run) {
			t.FailNow()
		}
	}
	var session string
	tokens := make(map[TokenKind]string)

	step("open mode answers and refuses what token mode holds", func(t *testing.T) {
		expectJSON(t, router, panelCall{method: http.MethodGet, path: "/session"},
			map[string]any{
				"accessMode": "open", "setupComplete": true, "authenticated": false,
			})
		expectJSON(t, router, panelCall{method: http.MethodGet, path: "/access"},
			map[string]any{"mode": "open", "passwordSetAt": nil, "sessions": 0.0})
		for _, call := range []panelCall{
			{method: http.MethodPost, path: "/session",
				body: jsonBody(t, map[string]any{"password": firstPassword})},
			{method: http.MethodPost, path: "/session/recovery"},
			{method: http.MethodPost, path: "/session/recovery/reset",
				body: jsonBody(t, map[string]any{
					"code": "AAAA-BBBB-CCCC", "password": firstPassword,
				})},
			{method: http.MethodGet, path: "/access/tokens/api"},
			{method: http.MethodPost, path: "/access/tokens/api/regenerate"},
			{method: http.MethodPut, path: "/access/password",
				body: jsonBody(t, map[string]any{
					"currentPassword": firstPassword, "password": secondPassword,
				})},
		} {
			expectRefusal(t, router, call, http.StatusConflict, "conflict", "")
		}
	})

	step("PUT /access/mode validates its body", func(t *testing.T) {
		for _, tc := range []struct {
			body    map[string]any
			status  int
			code    string
			pointer string
		}{
			{map[string]any{}, http.StatusUnprocessableEntity, "validation", "/mode"},
			{map[string]any{"mode": "closed"},
				http.StatusUnprocessableEntity, "validation", "/mode"},
			{map[string]any{"mode": "token"},
				http.StatusUnprocessableEntity, "validation", "/password"},
			{map[string]any{"mode": "token", "password": "too short"},
				http.StatusUnprocessableEntity, "validation", "/password"},
			{map[string]any{"mode": "open", "acknowledged": true},
				http.StatusUnprocessableEntity, "validation", "/currentPassword"},
			{map[string]any{"mode": "open", "currentPassword": firstPassword},
				http.StatusUnprocessableEntity, "validation", "/acknowledged"},
			// A member of the other mode is refused, not ignored.
			{map[string]any{
				"mode": "open", "currentPassword": firstPassword, "acknowledged": true,
				"password": secondPassword,
			}, http.StatusUnprocessableEntity, "validation", "/password"},
			{map[string]any{
				"mode": "token", "password": firstPassword, "currentPassword": wrongPassword,
			}, http.StatusUnprocessableEntity, "validation", "/currentPassword"},
			{map[string]any{
				"mode": "token", "password": firstPassword, "acknowledged": true,
			}, http.StatusUnprocessableEntity, "validation", "/acknowledged"},
			{map[string]any{
				"mode": "open", "currentPassword": firstPassword, "acknowledged": true,
			}, http.StatusConflict, "conflict", ""},
		} {
			expectRefusal(t, router, panelCall{
				method: http.MethodPut, path: "/access/mode", body: jsonBody(t, tc.body),
			}, tc.status, tc.code, tc.pointer)
		}
	})

	step("switching to token mode sets the session cookie", func(t *testing.T) {
		call := panelCall{
			method: http.MethodPut, path: "/access/mode",
			body: jsonBody(t, map[string]any{"mode": "token", "password": firstPassword}),
		}
		session = issuedSession(t, call, expectStatus(t, router, call, http.StatusNoContent))
		if mode := h.service.Mode(); mode != ModeToken {
			t.Fatalf("mode after %s = %s, want token", call, mode)
		}
		call.cookie = session
		expectRefusal(t, router, call, http.StatusConflict, "conflict", "")
	})

	step("the session cookie authenticates the panel", func(t *testing.T) {
		expectJSON(t, router,
			panelCall{method: http.MethodGet, path: "/session", cookie: session},
			map[string]any{
				"accessMode": "token", "setupComplete": true, "authenticated": true,
			})
		expectJSON(t, router, panelCall{method: http.MethodGet, path: "/session"},
			map[string]any{
				"accessMode": "token", "setupComplete": true, "authenticated": false,
			})
		expectRefusal(t, router, panelCall{method: http.MethodGet, path: "/access"},
			http.StatusUnauthorized, "unauthorized", "")
	})

	step("GET /access reports token mode", func(t *testing.T) {
		expectJSON(t, router,
			panelCall{method: http.MethodGet, path: "/access", cookie: session},
			map[string]any{
				"mode": "token", "passwordSetAt": start.Format(time.RFC3339),
				"sessions": 1.0,
			})
	})

	step("the token routes return both tokens", func(t *testing.T) {
		for _, kind := range []TokenKind{TokenAPI, TokenAgent} {
			call := panelCall{
				method: http.MethodGet, path: "/access/tokens/" + string(kind),
				cookie: session,
			}
			token := tokenOf(t, router, call)
			want, err := h.service.Token(kind)
			if err != nil {
				t.Fatalf("Token(%s): %v", kind, err)
			}
			if token != want {
				t.Fatalf("%s returns another value than the %s token", call, kind)
			}
			tokens[kind] = token
		}
		// %61 is a non-canonical escape of "a", so the request keeps a RawPath,
		// which chi routes on without decoding the parameter.
		escaped := panelCall{
			method: http.MethodGet, path: "/access/tokens/%61pi", cookie: session,
		}
		if tokenOf(t, router, escaped) != tokens[TokenAPI] {
			t.Fatalf("%s returns another value than the api token", escaped)
		}
		for _, call := range []panelCall{
			{method: http.MethodGet, path: "/access/tokens/session", cookie: session},
			{method: http.MethodPost, path: "/access/tokens/session/regenerate",
				cookie: session},
		} {
			expectRefusal(t, router, call, http.StatusNotFound, "not_found", "")
		}
	})

	step("regenerating the API token retires the old value", func(t *testing.T) {
		call := panelCall{
			method: http.MethodPost, path: "/access/tokens/api/regenerate",
			cookie: session,
		}
		old := tokens[TokenAPI]
		fresh := tokenOf(t, router, call)
		if fresh == old {
			t.Fatalf("%s returned the old value", call)
		}
		if err := h.service.CheckToken(TokenAPI, old); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("CheckToken(old API token) = %s, want unauthorized",
				redact(t, err, old, fresh))
		}
		if err := h.service.CheckToken(TokenAPI, fresh); err != nil {
			t.Fatalf("CheckToken(new API token) = %s, want nil", redact(t, err, old, fresh))
		}
	})

	step("a wrong password is refused with Retry-After", func(t *testing.T) {
		call := panelCall{
			method: http.MethodPost, path: "/session",
			body: jsonBody(t, map[string]any{"password": wrongPassword}),
		}
		rec := expectRefusal(t, router, call,
			http.StatusUnauthorized, "invalid_password", "")
		expectRetryAfter(t, call, rec, "1")
		if cookies := rec.Header().Values("Set-Cookie"); len(cookies) != 0 {
			t.Fatalf("refused %s sets %d cookies, want none", call, len(cookies))
		}
		// 0.6 s of the 1 s delay remain, which Retry-After rounds up.
		h.clock.Advance(400 * time.Millisecond)
		call.body = jsonBody(t, map[string]any{"password": firstPassword})
		rec = expectRefusal(t, router, call,
			http.StatusTooManyRequests, "too_many_attempts", "")
		expectRetryAfter(t, call, rec, "1")
		// The second failure doubles the delay to 2 s; 1.6 s of it remain,
		// which Retry-After rounds up to 2, not down to 1.
		h.clock.Advance(600 * time.Millisecond)
		call.body = jsonBody(t, map[string]any{"password": wrongPassword})
		rec = expectRefusal(t, router, call,
			http.StatusUnauthorized, "invalid_password", "")
		expectRetryAfter(t, call, rec, "2")
		h.clock.Advance(400 * time.Millisecond)
		call.body = jsonBody(t, map[string]any{"password": firstPassword})
		rec = expectRefusal(t, router, call,
			http.StatusTooManyRequests, "too_many_attempts", "")
		expectRetryAfter(t, call, rec, "2")
	})

	var code string
	step("password recovery prints one code", func(t *testing.T) {
		call := panelCall{method: http.MethodPost, path: "/session/recovery"}
		expiresAt := h.clock.Now().Add(recoveryCodeTTL).UTC().Format(time.RFC3339Nano)
		expectJSON(t, router, call,
			map[string]any{"issued": true, "expiresAt": expiresAt})
		match := recoveryLine.FindStringSubmatch(h.out.String())
		if match == nil {
			t.Fatal("the recovery output is not exactly one recovery code line")
		}
		code = match[1]
		h.out.Reset()
		expectJSON(t, router, call,
			map[string]any{"issued": false, "expiresAt": expiresAt})
		if h.out.Len() != 0 {
			t.Fatalf("a second %s wrote to the recovery output", call)
		}
	})

	step("the recovery code resets the password and signs in", func(t *testing.T) {
		call := panelCall{
			method: http.MethodPost, path: "/session/recovery/reset",
			body: jsonBody(t, map[string]any{"code": code, "password": secondPassword}),
		}
		fresh := issuedSession(t, call, expectStatus(t, router, call, http.StatusNoContent))
		if fresh == session {
			t.Fatalf("%s set the session the reset ended", call)
		}
		expectJSON(t, router,
			panelCall{method: http.MethodGet, path: "/session", cookie: fresh},
			map[string]any{
				"accessMode": "token", "setupComplete": true, "authenticated": true,
			})
		expectJSON(t, router,
			panelCall{method: http.MethodGet, path: "/session", cookie: session},
			map[string]any{
				"accessMode": "token", "setupComplete": true, "authenticated": false,
			})
		session = fresh
	})

	step("a new recovery code waits for the issue interval", func(t *testing.T) {
		// The used code was issued at the clock's time, so the whole minute
		// between codes remains.
		call := panelCall{method: http.MethodPost, path: "/session/recovery"}
		rec := expectRefusal(t, router, call,
			http.StatusTooManyRequests, "too_many_attempts", "")
		expectRetryAfter(t, call, rec, "60")
	})

	step("a wrong current password is refused", func(t *testing.T) {
		expectRefusal(t, router, panelCall{
			method: http.MethodPut, path: "/access/password", cookie: session,
			body: jsonBody(t, map[string]any{
				"currentPassword": wrongPassword, "password": firstPassword,
			}),
		}, http.StatusForbidden, "current_password_invalid", "")
	})

	step("a password change keeps the session and replaces the password", func(t *testing.T) {
		expectStatus(t, router, panelCall{
			method: http.MethodPut, path: "/access/password", cookie: session,
			body: jsonBody(t, map[string]any{
				"currentPassword": secondPassword, "password": firstPassword,
			}),
		}, http.StatusNoContent)
		expectJSON(t, router,
			panelCall{method: http.MethodGet, path: "/session", cookie: session},
			map[string]any{
				"accessMode": "token", "setupComplete": true, "authenticated": true,
			})
		// Each sign-in waits out the delay the failure before it left.
		h.clock.Advance(signInMaxDelay)
		call := panelCall{
			method: http.MethodPost, path: "/session",
			body: jsonBody(t, map[string]any{"password": secondPassword}),
		}
		expectRefusal(t, router, call, http.StatusUnauthorized, "invalid_password", "")
		h.clock.Advance(signInMaxDelay)
		call.body = jsonBody(t, map[string]any{"password": firstPassword})
		issuedSession(t, call, expectStatus(t, router, call, http.StatusNoContent))
	})

	step("signing out clears the cookie and ends the session", func(t *testing.T) {
		call := panelCall{method: http.MethodDelete, path: "/session", cookie: session}
		expectCleared(t, call, expectStatus(t, router, call, http.StatusNoContent))
		expectRefusal(t, router,
			panelCall{method: http.MethodGet, path: "/access", cookie: session},
			http.StatusUnauthorized, "unauthorized", "")
	})

	signIn := func(t *testing.T) string {
		t.Helper()
		call := panelCall{
			method: http.MethodPost, path: "/session",
			body: jsonBody(t, map[string]any{"password": firstPassword}),
		}
		return issuedSession(t, call, expectStatus(t, router, call, http.StatusNoContent))
	}

	step("signing out everywhere ends every session", func(t *testing.T) {
		h.clock.Advance(signInMaxDelay)
		first, second := signIn(t), signIn(t)
		call := panelCall{method: http.MethodDelete, path: "/sessions", cookie: first}
		expectCleared(t, call, expectStatus(t, router, call, http.StatusNoContent))
		expectRefusal(t, router,
			panelCall{method: http.MethodGet, path: "/access", cookie: second},
			http.StatusUnauthorized, "unauthorized", "")
	})

	step("switching to open mode clears the cookie", func(t *testing.T) {
		call := panelCall{
			method: http.MethodPut, path: "/access/mode", cookie: signIn(t),
			body: jsonBody(t, map[string]any{
				"mode": "open", "currentPassword": firstPassword, "acknowledged": true,
			}),
		}
		expectCleared(t, call, expectStatus(t, router, call, http.StatusNoContent))
		if mode := h.service.Mode(); mode != ModeOpen {
			t.Fatalf("mode after %s = %s, want open", call, mode)
		}
		expectJSON(t, router, panelCall{method: http.MethodGet, path: "/session"},
			map[string]any{
				"accessMode": "open", "setupComplete": true, "authenticated": false,
			})
	})
}

// serve sends call through router. It fails, without printing the answer,
// when the answer carries one of the panel passwords.
func serve(
	t *testing.T, router http.Handler, call panelCall,
) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if call.body != "" {
		body = strings.NewReader(call.body)
	}
	request := httptest.NewRequest(
		call.method, "http://127.0.0.1/app/api/v1"+call.path, body,
	)
	if call.body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if call.cookie != "" {
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: call.cookie})
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, request)
	for _, password := range []string{firstPassword, secondPassword, wrongPassword} {
		if strings.Contains(rec.Body.String(), password) {
			t.Fatalf("%s answered with a panel password", call)
		}
	}
	return rec
}

// expectStatus serves call and checks that it is answered with status.
func expectStatus(
	t *testing.T, router http.Handler, call panelCall, status int,
) *httptest.ResponseRecorder {
	t.Helper()
	rec := serve(t, router, call)
	if rec.Code != status {
		code, pointer := refusalOf(rec)
		t.Fatalf("%s = %d %s %q, want %d", call, rec.Code, code, pointer, status)
	}
	return rec
}

// expectJSON serves call and checks that it is answered 200 with exactly the
// JSON object want. It prints the answer, so call must not answer a secret.
func expectJSON(
	t *testing.T, router http.Handler, call panelCall, want map[string]any,
) {
	t.Helper()
	rec := expectStatus(t, router, call, http.StatusOK)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%s: decode the answer: %v", call, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %v, want %v", call, got, want)
	}
}

// tokenOf serves call, a request for a token value, checks that it is
// answered 200 with exactly {"token"} and without caching, and returns the
// token. Its failures never print the token.
func tokenOf(t *testing.T, router http.Handler, call panelCall) string {
	t.Helper()
	rec := expectStatus(t, router, call, http.StatusOK)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("%s Cache-Control = %q, want no-store", call, got)
	}
	var answer struct {
		Token string `json:"token"`
	}
	decoder := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&answer); err != nil || answer.Token == "" {
		t.Fatalf("%s answered no token object", call)
	}
	return answer.Token
}

// expectRefusal serves call and checks that it is refused with status and
// the error code; pointer is the member a validation problem names, empty
// for any other refusal.
func expectRefusal(
	t *testing.T,
	router http.Handler,
	call panelCall,
	status int,
	code, pointer string,
) *httptest.ResponseRecorder {
	t.Helper()
	rec := serve(t, router, call)
	gotCode, gotPointer := refusalOf(rec)
	if rec.Code != status || gotCode != code || gotPointer != pointer {
		t.Fatalf("%s = %d %s %q, want %d %s %q",
			call, rec.Code, gotCode, gotPointer, status, code, pointer)
	}
	return rec
}

// refusalOf returns the error code of a refused request's answer and, for a
// validation problem, the pointer of its first error. Both are empty for an
// answer that carries no error.
func refusalOf(rec *httptest.ResponseRecorder) (code, pointer string) {
	var answer struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Errors []struct {
			Code    string `json:"code"`
			Pointer string `json:"pointer"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		return "", ""
	}
	if len(answer.Errors) > 0 {
		return answer.Errors[0].Code, answer.Errors[0].Pointer
	}
	return answer.Error.Code, ""
}

// expectRetryAfter checks the Retry-After header of the answer to call.
func expectRetryAfter(
	t *testing.T, call panelCall, rec *httptest.ResponseRecorder, want string,
) {
	t.Helper()
	if got := rec.Header().Get("Retry-After"); got != want {
		t.Fatalf("%s Retry-After = %q, want %q", call, got, want)
	}
}

// issuedSession checks that the answer to call sets the session cookie with
// a value and the issued attributes, and returns the value: the raw session
// id. Its failures never print the id.
func issuedSession(
	t *testing.T, call panelCall, rec *httptest.ResponseRecorder,
) string {
	t.Helper()
	id := sessionCookieSet(t, call, rec, issuedCookieAttributes)
	if id == "" {
		t.Fatalf("%s sets the session cookie without a value", call)
	}
	return id
}

// expectCleared checks that the answer to call clears the session cookie.
func expectCleared(t *testing.T, call panelCall, rec *httptest.ResponseRecorder) {
	t.Helper()
	if sessionCookieSet(t, call, rec, clearedCookieAttributes) != "" {
		t.Fatalf("%s sets a session instead of clearing the cookie", call)
	}
}

// sessionCookieSet checks that the answer to call sets exactly one cookie,
// the session cookie, with exactly attributes, and returns its value. Its
// failures never print the value.
func sessionCookieSet(
	t *testing.T, call panelCall, rec *httptest.ResponseRecorder, attributes string,
) string {
	t.Helper()
	cookies := rec.Header().Values("Set-Cookie")
	if len(cookies) != 1 {
		t.Fatalf("%s sets %d cookies, want 1", call, len(cookies))
	}
	pair, gotAttributes, _ := strings.Cut(cookies[0], "; ")
	name, value, _ := strings.Cut(pair, "=")
	if name != SessionCookie || gotAttributes != attributes {
		t.Fatalf("%s sets cookie %q with %q, want %s with %q",
			call, name, gotAttributes, SessionCookie, attributes)
	}
	return value
}

// jsonBody encodes members as a JSON request body.
func jsonBody(t *testing.T, members map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(members)
	if err != nil {
		t.Fatalf("encode a request body: %v", err)
	}
	return string(raw)
}
