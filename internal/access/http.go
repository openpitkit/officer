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
	"net/http"
	"strings"

	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// SessionCookie is the name of the cookie that carries the raw id of a panel
// session. Only the panel API reads it.
const SessionCookie = "pit_officer_session"

// sessionCookieValue returns the raw session id the request's SessionCookie
// cookie carries, or the empty string when the request carries none, the only
// failure of Request.Cookie.
func sessionCookieValue(r *http.Request) string {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// bearerChallenge is the WWW-Authenticate value of a request a bearer surface
// refuses as unauthorized.
const bearerChallenge = `Bearer realm="pit-officer"`

// errNoBearerToken refuses a request on a bearer surface whose Authorization
// header is missing or is not exactly one bearer token.
var errNoBearerToken = fmt.Errorf("no bearer token: %w", domain.ErrUnauthorized)

// operator is the caller every admitted request resolves to. The router
// stamps the source of the surface the request arrived on.
var operator = domain.Caller{Principal: domain.PrincipalOperator}

// ResolveCaller resolves the caller of a request to Officer's HTTP surfaces,
// with the auth.CallerResolver signature. It decides by the surface the router
// stamped into the request context, never by the request itself; a request
// without one, or with a surface other than the REST API, MCP and the panel
// API, is an internal error.
//
// Setup mode admits the panel without credentials only when the route's
// DuringSetup flag is stamped in the context. It refuses other panel routes,
// API and MCP with an addressed ErrSetupRequired. Open mode admits every
// surface without credentials. Token mode admits each surface only with its
// own credential:
//
//   - the REST API (domain.SourceAPI) the API token and MCP (domain.SourceMCP)
//     the agent token, each as "Authorization: Bearer <token>" with the scheme
//     in any case and exactly one token. A missing or malformed header, or a
//     token neither surface accepts, is an error wrapping
//     domain.ErrUnauthorized that carries the "WWW-Authenticate" bearer
//     challenge; the other surface's token is an error wrapping
//     domain.ErrWrongToken, which names the surface it is accepted on;
//   - the panel API (domain.SourcePanel) a live session in the SessionCookie
//     cookie. A missing, unknown or ended session is an error wrapping
//     domain.ErrUnauthorized, without a challenge.
//
// A cookie on a bearer surface and a bearer header on the panel API
// authenticate nothing. An admitted request resolves to the operator.
func (s *Service) ResolveCaller(r *http.Request) (domain.Caller, error) {
	surface, ok := httpx.SurfaceFromContext(r.Context())
	if !ok {
		return domain.Caller{}, errors.New("access: the request carries no surface")
	}
	var kind TokenKind
	switch surface {
	case domain.SourceAPI:
		kind = TokenAPI
	case domain.SourceMCP:
		kind = TokenAgent
	case domain.SourcePanel:
		// The panel API takes a session, never a token.
	default:
		return domain.Caller{}, fmt.Errorf(
			"access: unknown request surface %q", surface,
		)
	}
	switch mode := s.Mode(); mode {
	case ModeSetup:
		if surface == domain.SourcePanel && httpx.DuringSetupFromContext(r.Context()) {
			return operator, nil
		}
		return domain.Caller{}, setupRequiredError{host: r.Host}
	case ModeOpen:
		return operator, nil
	case ModeToken:
	default:
		return domain.Caller{}, fmt.Errorf("access: unknown access mode %q", mode)
	}
	if surface == domain.SourcePanel {
		return s.resolveSession(r)
	}
	return s.resolveBearer(r, kind)
}

type setupRequiredError struct{ host string }

func (e setupRequiredError) Error() string {
	// Replace the sentinel's panel reference with this request's address.
	return strings.Replace(domain.ErrSetupRequired.Error(), "the panel", "http://"+e.host, 1)
}

func (setupRequiredError) Unwrap() error { return domain.ErrSetupRequired }

// Authorize closes API and non-setup panel routes until setup completes.
// MCP passes here because stdio tool calls also use this authorizer.
func (s *Service) Authorize(ctx context.Context, caller domain.Caller, _ string) error {
	switch mode := s.Mode(); mode {
	case ModeOpen, ModeToken:
		return nil
	case ModeSetup:
		switch caller.Source {
		case domain.SourceMCP:
			return nil
		case domain.SourcePanel:
			if httpx.DuringSetupFromContext(ctx) {
				return nil
			}
			return domain.ErrSetupRequired
		case domain.SourceAPI:
			return domain.ErrSetupRequired
		default:
			return fmt.Errorf("access: unknown caller source %q", caller.Source)
		}
	default:
		return fmt.Errorf("access: unknown access mode %q", mode)
	}
}

// resolveBearer admits a request on the bearer surface that accepts the token
// of kind.
func (s *Service) resolveBearer(
	r *http.Request, kind TokenKind,
) (domain.Caller, error) {
	token, ok := bearerToken(r)
	if !ok {
		return domain.Caller{}, challenge(errNoBearerToken)
	}
	err := s.CheckToken(kind, token)
	switch {
	case err == nil:
		return operator, nil
	case errors.Is(err, domain.ErrWrongToken):
		// The token is valid, so the client is not challenged for another.
		return domain.Caller{}, err
	case errors.Is(err, domain.ErrUnauthorized):
		return domain.Caller{}, challenge(err)
	default:
		return domain.Caller{}, err
	}
}

// resolveSession admits a panel request whose session cookie names a live
// session.
func (s *Service) resolveSession(r *http.Request) (domain.Caller, error) {
	if err := s.CheckSession(r.Context(), sessionCookieValue(r)); err != nil {
		return domain.Caller{}, err
	}
	return operator, nil
}

// bearerToken returns the token of the request's Authorization header when
// the header is present once and holds the Bearer scheme, in any case,
// followed by exactly one token.
func bearerToken(r *http.Request) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	fields := strings.Fields(values[0])
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return "", false
	}
	return fields[1], true
}

// challenge attaches the bearer challenge to an unauthorized refusal.
func challenge(err error) error {
	return httpx.WithResponseHeader(err, "WWW-Authenticate", bearerChallenge)
}
