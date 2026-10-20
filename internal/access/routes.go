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
	"errors"
	"net/http"
	"strconv"
	"time"

	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// sessionCookieMaxAge is the Max-Age of the session cookie, in seconds: the
// lifetime of the session from its sign-in.
const sessionCookieMaxAge = int(sessionLifetime / time.Second)

// Routes returns the panel access routes over s, which belong in the
// panel-only route registry: sign-in and sign-out, password recovery, and the
// access state, mode switch, password change and tokens the panel's security
// page shows. GET /session, sign-in and password recovery are public, and GET
// /session, which the panel reads before it renders, also answers during
// first-run setup. The routes translate HTTP to the operations of s, which
// apply every rule of the access policy.
func Routes(s *Service) []httpx.Route {
	return []httpx.Route{
		{
			ID:          "session.get",
			Method:      http.MethodGet,
			Pattern:     "/session",
			Handler:     handleGetSession(s),
			Public:      true,
			DuringSetup: true,
		},
		{
			ID:      "session.create.post",
			Method:  http.MethodPost,
			Pattern: "/session",
			Handler: handleSignIn(s),
			Public:  true,
		},
		{
			ID:      "session.delete",
			Method:  http.MethodDelete,
			Pattern: "/session",
			Handler: handleSignOut(s),
		},
		{
			ID:      "sessions.delete",
			Method:  http.MethodDelete,
			Pattern: "/sessions",
			Handler: handleSignOutEverywhere(s),
		},
		{
			ID:      "session.recovery.post",
			Method:  http.MethodPost,
			Pattern: "/session/recovery",
			Handler: handleRequestRecovery(s),
			Public:  true,
		},
		{
			ID:      "session.recovery.reset.post",
			Method:  http.MethodPost,
			Pattern: "/session/recovery/reset",
			Handler: handleResetWithRecoveryCode(s),
			Public:  true,
		},
		{
			ID:      "access.get",
			Method:  http.MethodGet,
			Pattern: "/access",
			Handler: handleGetAccess(s),
		},
		{
			ID:      "access.mode.put",
			Method:  http.MethodPut,
			Pattern: "/access/mode",
			Handler: handleSetMode(s),
		},
		{
			ID:      "access.password.put",
			Method:  http.MethodPut,
			Pattern: "/access/password",
			Handler: handleChangePassword(s),
		},
		{
			ID:      "access.tokens.get",
			Method:  http.MethodGet,
			Pattern: "/access/tokens/{kind}",
			Handler: handleGetToken(s),
		},
		{
			ID:      "access.tokens.regenerate.post",
			Method:  http.MethodPost,
			Pattern: "/access/tokens/{kind}/regenerate",
			Handler: handleRegenerateToken(s),
		},
	}
}

// statusDTO is the wire shape of Status.
type statusDTO struct {
	AccessMode    Mode `json:"accessMode"`
	SetupComplete bool `json:"setupComplete"`
	Authenticated bool `json:"authenticated"`
}

// recoveryDTO is the answer of a recovery request: whether it issued a code
// and when the live code expires.
type recoveryDTO struct {
	Issued    bool      `json:"issued"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// accessDTO is the wire shape of AccessInfo. PasswordSetAt is null in open
// mode.
type accessDTO struct {
	Mode          Mode       `json:"mode"`
	PasswordSetAt *time.Time `json:"passwordSetAt"`
	Sessions      int        `json:"sessions"`
}

// tokenDTO carries the value of one token.
type tokenDTO struct {
	Token string `json:"token"`
}

// signInRequest is the body of POST /session.
type signInRequest struct {
	Password *string `json:"password"`
}

// recoveryResetRequest is the body of POST /session/recovery/reset.
type recoveryResetRequest struct {
	Code     *string `json:"code"`
	Password *string `json:"password"`
}

// modeRequest is the body of PUT /access/mode: the mode to switch to, with
// the current password and the acknowledgement for open mode, or the new
// password for token mode.
type modeRequest struct {
	Mode            Mode    `json:"mode"`
	CurrentPassword *string `json:"currentPassword"`
	Acknowledged    *bool   `json:"acknowledged"`
	Password        *string `json:"password"`
}

// passwordRequest is the body of PUT /access/password.
type passwordRequest struct {
	CurrentPassword *string `json:"currentPassword"`
	Password        *string `json:"password"`
}

// handleGetSession handles GET /app/api/v1/session: the access state the
// panel reads before it renders.
func handleGetSession(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := s.Status(r.Context(), sessionCookieValue(r))
		if err != nil {
			writeErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, statusDTO{
			AccessMode:    status.Mode,
			SetupComplete: status.SetupComplete,
			Authenticated: status.Authenticated,
		})
	}
}

// handleSignIn handles POST /app/api/v1/session: it signs the panel in with
// the password and sets the session cookie.
func handleSignIn(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req signInRequest
		if !httpx.DecodeBody(w, r, &req) ||
			!requireMember(w, req.Password, "password") {
			return
		}
		id, err := s.SignIn(r.Context(), *req.Password)
		if err != nil {
			writeErr(w, err)
			return
		}
		setSessionCookie(w, id)
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleSignOut handles DELETE /app/api/v1/session: it ends the request's
// session and clears the session cookie.
func handleSignOut(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.SignOut(r.Context(), sessionCookieValue(r)); err != nil {
			writeErr(w, err)
			return
		}
		clearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleSignOutEverywhere handles DELETE /app/api/v1/sessions: it ends every
// session and clears the session cookie.
func handleSignOutEverywhere(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.SignOutEverywhere(r.Context()); err != nil {
			writeErr(w, err)
			return
		}
		clearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleRequestRecovery handles POST /app/api/v1/session/recovery: it issues
// a recovery code to the process's standard error, unless one is live, and
// answers when the live code expires.
func handleRequestRecovery(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		issued, expiresAt, err := s.RequestRecovery(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, recoveryDTO{
			Issued:    issued,
			ExpiresAt: expiresAt.UTC(),
		})
	}
}

// handleResetWithRecoveryCode handles POST /app/api/v1/session/recovery/reset:
// it sets a new password with the recovery code and sets the cookie of the
// session issued for this browser.
func handleResetWithRecoveryCode(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req recoveryResetRequest
		if !httpx.DecodeBody(w, r, &req) ||
			!requireMember(w, req.Code, "code") ||
			!requireMember(w, req.Password, "password") {
			return
		}
		id, err := s.ResetWithRecoveryCode(r.Context(), *req.Code, *req.Password)
		if err != nil {
			writeErr(w, err)
			return
		}
		setSessionCookie(w, id)
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleGetAccess handles GET /app/api/v1/access: the access state the
// panel's security page shows.
func handleGetAccess(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		info, err := s.AccessInfo(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		dto := accessDTO{Mode: info.Mode, Sessions: info.Sessions}
		if info.PasswordSetAt != nil {
			setAt := info.PasswordSetAt.UTC()
			dto.PasswordSetAt = &setAt
		}
		httpx.WriteJSON(w, http.StatusOK, dto)
	}
}

// handleSetMode handles PUT /app/api/v1/access/mode. A body member the chosen
// mode does not take is refused. Switching to open mode clears the session
// cookie; switching to token mode sets the cookie of the session issued for
// this browser.
func handleSetMode(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req modeRequest
		if !httpx.DecodeBody(w, r, &req) {
			return
		}
		switch req.Mode {
		case ModeOpen:
			if !refuseMember(w, req.Password, "password", ModeOpen) ||
				!requireMember(w, req.CurrentPassword, "currentPassword") ||
				!requireMember(w, req.Acknowledged, "acknowledged") {
				return
			}
			if err := s.SwitchToOpen(
				r.Context(), *req.CurrentPassword, *req.Acknowledged,
			); err != nil {
				writeErr(w, err)
				return
			}
			clearSessionCookie(w)
		case ModeToken:
			if !refuseMember(w, req.CurrentPassword, "currentPassword", ModeToken) ||
				!refuseMember(w, req.Acknowledged, "acknowledged", ModeToken) ||
				!requireMember(w, req.Password, "password") {
				return
			}
			id, err := s.SwitchToToken(r.Context(), *req.Password)
			if err != nil {
				writeErr(w, err)
				return
			}
			setSessionCookie(w, id)
		case "":
			httpx.WriteValidationProblem(w, "mode is required", "/mode", "required")
			return
		default:
			httpx.WriteValidationProblem(
				w, "mode must be open or token", "/mode", "enum",
			)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleChangePassword handles PUT /app/api/v1/access/password: it replaces
// the password, keeping only the request's session.
func handleChangePassword(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req passwordRequest
		if !httpx.DecodeBody(w, r, &req) ||
			!requireMember(w, req.CurrentPassword, "currentPassword") ||
			!requireMember(w, req.Password, "password") {
			return
		}
		if err := s.ChangePassword(
			r.Context(), sessionCookieValue(r), *req.CurrentPassword, *req.Password,
		); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleGetToken handles GET /app/api/v1/access/tokens/{kind}: the value of
// the token of kind.
func handleGetToken(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := httpx.PathTokenKind(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		kind, err := ParseTokenKind(raw)
		if err != nil {
			writeErr(w, err)
			return
		}
		token, err := s.Token(kind)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeToken(w, token)
	}
}

// handleRegenerateToken handles POST
// /app/api/v1/access/tokens/{kind}/regenerate: it replaces the token of kind
// and answers the new value.
func handleRegenerateToken(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := httpx.PathTokenKind(r)
		if err != nil {
			httpx.WriteBadRequestProblem(w, err.Error(), "url_encoding")
			return
		}
		kind, err := ParseTokenKind(raw)
		if err != nil {
			writeErr(w, err)
			return
		}
		token, err := s.RegenerateToken(r.Context(), kind)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeToken(w, token)
	}
}

// writeToken answers the value of a token. The value is a credential, so no
// cache may keep the response.
func writeToken(w http.ResponseWriter, token string) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, tokenDTO{Token: token})
}

// writeErr answers err through httpx.WriteErr. A RetryAfterError also sets
// Retry-After to the whole seconds until the next accepted attempt, rounded
// up and at least one.
func writeErr(w http.ResponseWriter, err error) {
	var retry RetryAfterError
	if errors.As(err, &retry) {
		seconds := max((retry.After+time.Second-1)/time.Second, 1)
		err = httpx.WithResponseHeader(
			err, "Retry-After", strconv.FormatInt(int64(seconds), 10),
		)
	}
	httpx.WriteErr(w, err)
}

// requireMember reports whether the request body member name is present,
// and answers the validation problem that names it when it is not.
func requireMember[T any](w http.ResponseWriter, value *T, name string) bool {
	if value != nil {
		return true
	}
	httpx.WriteValidationProblem(w, name+" is required", "/"+name, "required")
	return false
}

// refuseMember reports whether the request body member name, which mode does
// not take, is absent, and answers the validation problem that names it when
// it is present.
func refuseMember[T any](
	w http.ResponseWriter, value *T, name string, mode Mode,
) bool {
	if value == nil {
		return true
	}
	httpx.WriteValidationProblem(
		w, string(mode)+" mode takes no "+name, "/"+name, "unsupported_value",
	)
	return false
}

// setSessionCookie hands the browser the session whose raw id is id. The
// cookie carries neither the Secure attribute nor the __Host- prefix, which
// both require HTTPS: Officer serves plain HTTP.
func setSessionCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    id,
		Path:     "/",
		MaxAge:   sessionCookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// clearSessionCookie tells the browser to drop the session cookie. A negative
// MaxAge is written as Max-Age=0.
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}
