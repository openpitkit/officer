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

package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
)

func TestDecodeBodyRequiresOneNonNullValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"empty", "", http.StatusBadRequest},
		{"null", "null", http.StatusUnprocessableEntity},
		{"trailing value", `{"enabled":true}{"ignored":true}`, http.StatusBadRequest},
		{"trailing garbage", `{"enabled":true}garbage`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(
				http.MethodPut, "/", bytes.NewBufferString(tc.body),
			)
			var dst struct {
				Enabled bool `json:"enabled"`
			}
			if DecodeBody(rec, req, &dst) {
				t.Fatal("DecodeBody accepted an invalid mutation body")
			}
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("Content-Type = %q", got)
			}
		})
	}
}

func TestWriteErrSentinelCodes(t *testing.T) {
	for _, tc := range []struct {
		code   string
		err    error
		status int
	}{
		{"live_trading_unavailable", domain.ErrLiveTradingUnavailable, 403},
		{"trading_mode_mismatch", domain.ErrTradingModeMismatch, 409},
		{"fee_account_required", domain.ErrFeeAccountRequired, 409},
		{"master_key_required", domain.ErrMasterKeyRequired, 409},
		{"venue_order_managed", domain.ErrVenueOrderManaged, 409},
		{"no_destination", domain.ErrNoDestination, 409},
		{"destination_ambiguous", domain.ErrDestinationAmbiguous, 409},
	} {
		t.Run(tc.code, func(t *testing.T) {
			generic := domain.ErrConflict
			if tc.status == 403 {
				generic = domain.ErrForbidden
			}
			rec := httptest.NewRecorder()
			WriteErr(rec, fmt.Errorf("hidden-credential-value: %w: %w", generic, tc.err))
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d", rec.Code, tc.status)
			}
			var body struct {
				Error struct {
					Code, Message string
					Candidates    []any
				}
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != tc.code || body.Error.Message != tc.err.Error() {
				t.Fatalf("body=%s", rec.Body)
			}
			if strings.Contains(rec.Body.String(), "hidden-credential-value") {
				t.Fatal("error leaked credential")
			}
			if tc.code == "destination_ambiguous" && (body.Error.Candidates == nil || len(body.Error.Candidates) != 0) {
				t.Fatal("bare sentinel candidates must be []")
			}
		})
	}
}

// TestWriteErrBrowserGuardCodes maps each browser guard refusal to its own
// status and code, with the sentinel's text and not the wrapping context.
func TestWriteErrBrowserGuardCodes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		code   string
		err    error
		status int
	}{
		{"host_not_allowed", domain.ErrHostNotAllowed, http.StatusForbidden},
		{"origin_not_allowed", domain.ErrOriginNotAllowed, http.StatusForbidden},
		{"unsupported_media_type", domain.ErrUnsupportedMediaType, http.StatusUnsupportedMediaType},
	} {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			WriteErr(rec, fmt.Errorf("guard refusal: %w", tc.err))
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d", rec.Code, tc.status)
			}
			var body struct {
				Error struct{ Code, Message string }
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != tc.code || body.Error.Message != tc.err.Error() {
				t.Fatalf("body=%s", rec.Body)
			}
		})
	}
}

// TestDescribeErrAccessSentinels checks the status and code of each access
// refusal, matched before the generic forbidden and conflict cases it may also
// wrap, and its message: the full text for a wrong token, whose wrapper names
// the surface the token belongs to, and the sentinel text for the rest.
func TestDescribeErrAccessSentinels(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err      error
		status   int
		code     string
		fullText bool
	}{
		{domain.ErrUnauthorized, http.StatusUnauthorized, "unauthorized", false},
		{domain.ErrInvalidPassword, http.StatusUnauthorized, "invalid_password", false},
		{domain.ErrInvalidRecoveryCode, http.StatusUnauthorized, "invalid_recovery_code", false},
		{domain.ErrCurrentPasswordInvalid, http.StatusForbidden, "current_password_invalid", false},
		{domain.ErrWrongToken, http.StatusForbidden, "wrong_token", true},
		{domain.ErrTooManyAttempts, http.StatusTooManyRequests, "too_many_attempts", false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()
			err := fmt.Errorf(
				"the agent token on the REST API: %w: %w: %w",
				domain.ErrForbidden, domain.ErrConflict, tc.err,
			)
			message := tc.err.Error()
			if tc.fullText {
				message = err.Error()
			}
			want := PublicError{tc.status, tc.code, message}
			if got := DescribeErr(err); got != want {
				t.Fatalf("DescribeErr(%q) = %+v, want %+v", err, got, want)
			}
		})
	}
}

// TestWriteErrSetsCarriedHeaders checks that WriteErr answers a sentinel
// wrapped with response headers as DescribeErr maps the bare sentinel, with
// every carried header set, and that errors.Is and errors.As see through the
// wrappers, a typed error keeping its structured answer.
func TestWriteErrSetsCarriedHeaders(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("sign in: %w", WithResponseHeader(
		WithResponseHeader(domain.ErrInvalidPassword, "Retry-After", "2"),
		"WWW-Authenticate", `Bearer realm="pit-officer"`,
	))
	if !errors.Is(err, domain.ErrInvalidPassword) {
		t.Fatal("errors.Is lost the sentinel under the header wrappers")
	}
	if got, want := DescribeErr(err), DescribeErr(domain.ErrInvalidPassword); got != want {
		t.Fatalf("DescribeErr = %+v, want the bare sentinel's %+v", got, want)
	}
	rec := httptest.NewRecorder()
	WriteErr(rec, err)
	var body struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnauthorized || body.Error.Code != "invalid_password" ||
		body.Error.Message != domain.ErrInvalidPassword.Error() {
		t.Fatalf("answer = %d %s, want 401 invalid_password", rec.Code, rec.Body)
	}
	for name, want := range map[string]string{
		"Retry-After":      "2",
		"WWW-Authenticate": `Bearer realm="pit-officer"`,
	} {
		if got := rec.Header().Values(name); !slices.Equal(got, []string{want}) {
			t.Fatalf("%s = %q, want [%s]", name, got, want)
		}
	}

	missing := WithResponseHeader(
		domain.NewAssetMissingError("USDT"), "Cache-Control", "no-store",
	)
	var assetMissing domain.AssetMissingError
	if !errors.As(missing, &assetMissing) || assetMissing.Asset != "USDT" {
		t.Fatalf("errors.As = %+v, want the asset missing error", assetMissing)
	}
	rec = httptest.NewRecorder()
	WriteErr(rec, missing)
	if rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(rec.Body.String(), `"asset":"USDT"`) {
		t.Fatalf("answer = %d %v %s, want 404 asset_missing with the header",
			rec.Code, rec.Header(), rec.Body)
	}
}

// TestWriteErrCarriedHeaderFirstReachedWins checks that a header carried more
// than once takes the value of the wrapper errors.Is reaches first: the outer
// over the inner, and the earlier of two wrapped errors over the later.
func TestWriteErrCarriedHeaderFirstReachedWins(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"outer over inner", WithResponseHeader(
			WithResponseHeader(domain.ErrTooManyAttempts, "Retry-After", "2"),
			"Retry-After", "30",
		)},
		{"earlier over later", fmt.Errorf("%w; %w",
			WithResponseHeader(domain.ErrTooManyAttempts, "Retry-After", "30"),
			WithResponseHeader(domain.ErrInvalidPassword, "Retry-After", "2"),
		)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			WriteErr(rec, tc.err)
			if got := rec.Header().Values("Retry-After"); !slices.Equal(got, []string{"30"}) {
				t.Fatalf("Retry-After = %q, want [30]", got)
			}
		})
	}
}

// TestWriteErrAccountsNotManaged maps a refused change of the account set to a
// 409 carrying its own code and the error's own text, so a refusal that says
// what to do instead reaches the client.
func TestWriteErrAccountsNotManaged(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("create account acc-1: %w", domain.ErrAccountsNotManaged)
	rec := httptest.NewRecorder()
	WriteErr(rec, err)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusConflict)
	}
	var body struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "accounts_not_managed" || body.Error.Message != err.Error() {
		t.Fatalf("body=%s", rec.Body)
	}
}

// TestWriteErrLoosening maps a refused loosening to a 403 carrying its own code
// and the error's own text, which names what the request would have loosened,
// also when the refusal wraps a sentinel DescribeErr would otherwise match
// first.
func TestWriteErrLoosening(t *testing.T) {
	t.Parallel()
	refusal := fmt.Errorf(
		"delete group %q: its block would stop applying to its members: %w",
		"desk", domain.ErrLoosening,
	)
	for name, err := range map[string]error{
		"refusal":           refusal,
		"over another code": fmt.Errorf("%w: %w", domain.ErrTooLarge, refusal),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			WriteErr(rec, err)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d want=%d", rec.Code, http.StatusForbidden)
			}
			var body struct {
				Error struct{ Code, Message string }
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != "loosening_refused" ||
				body.Error.Message != err.Error() {
				t.Fatalf("body=%s", rec.Body)
			}
		})
	}
}

func TestWriteErrDestinationCandidates(t *testing.T) {
	id, err := domain.ParseExternalID("connection-1")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	WriteErr(rec, fmt.Errorf("wrapped: %w", &backend.DestinationAmbiguousError{Candidates: []backend.TradingDestinationStatus{
		{Connection: id, Label: "Paper", Provider: "alpaca", Mode: domain.TradingModePaper, Enabled: true, VenueAccount: "sub-1", State: "running"},
		{Connection: id, Label: "Unavailable", Provider: "alpaca", Mode: domain.TradingModePaper, State: "unavailable", Error: "keys unavailable"},
		{Connection: id, Label: "Degraded", Provider: "alpaca", Mode: domain.TradingModePaper, State: "degraded", Error: "stream refused"},
	}}))
	if rec.Code != 409 {
		t.Fatalf("status=%d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	e := got["error"].(map[string]any)
	if e["code"] != "destination_ambiguous" || e["message"] != domain.ErrDestinationAmbiguous.Error() {
		t.Fatalf("body=%s", rec.Body)
	}
	candidates := e["candidates"].([]any)
	if len(candidates) != 3 {
		t.Fatalf("candidates=%v", candidates)
	}
	first := candidates[0].(map[string]any)
	if len(first) != 7 || first["connection"] != "connection-1" || first["label"] != "Paper" || first["provider"] != "alpaca" || first["mode"] != "paper" || first["enabled"] != true || first["venueAccount"] != "sub-1" || first["state"] != "running" {
		t.Fatalf("candidate=%v", first)
	}
	second := candidates[1].(map[string]any)
	if len(second) != 8 || second["error"] != "keys unavailable" {
		t.Fatalf("candidate=%v", second)
	}
	third := candidates[2].(map[string]any)
	if len(third) != 8 || third["state"] != "degraded" || third["error"] != "stream refused" {
		t.Fatalf("candidate=%v", third)
	}
}

func TestWriteErrUpstreamNeutralMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteErr(rec, domain.ErrUpstream)
	if rec.Code != 502 {
		t.Fatalf("status=%d", rec.Code)
	}
	var body struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "upstream" || body.Error.Message != "The provider couldn't complete the request. Check your provider access and the request, then try again." {
		t.Fatalf("body=%s", rec.Body)
	}
}

func TestDecodeBodyRejectsUnknownField(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut, "/",
		bytes.NewBufferString(`{"enabled":true,"ignored":true}`),
	)
	var dst struct {
		Enabled bool `json:"enabled"`
	}
	if DecodeBody(rec, req, &dst) {
		t.Fatal("DecodeBody accepted an unknown field")
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
	var problem ProblemDetails
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Type != "about:blank" || problem.Title != "Unprocessable Content" ||
		problem.Status != http.StatusUnprocessableEntity || len(problem.Errors) != 1 {
		t.Fatalf("problem = %+v", problem)
	}
	if item := problem.Errors[0]; item.Code != "validation" ||
		item.Constraint != "unknown_field" || item.Pointer != "/ignored" {
		t.Fatalf("problem error = %+v", item)
	}
}

func TestDecodeBodyRejectsNestedUnknownFieldWithFullPointer(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut, "/",
		bytes.NewBufferString(`{"scope":{"account":{"enabled":true,"ignored":1}}}`),
	)
	var dst struct {
		Scope struct {
			Account struct {
				Enabled bool `json:"enabled"`
			} `json:"account"`
		} `json:"scope"`
	}
	if DecodeBody(rec, req, &dst) {
		t.Fatal("DecodeBody accepted a nested unknown field")
	}
	var problem ProblemDetails
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if rec.Code != http.StatusUnprocessableEntity || len(problem.Errors) != 1 ||
		problem.Errors[0].Pointer != "/scope/account/ignored" {
		t.Fatalf("problem = %+v", problem)
	}
}

func TestDecodeBodyOmitsAmbiguousUnknownFieldPointer(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut, "/",
		bytes.NewBufferString(`{"left":{"ignored":1},"right":{"ignored":2}}`),
	)
	var dst struct {
		Left  struct{} `json:"left"`
		Right struct{} `json:"right"`
	}
	if DecodeBody(rec, req, &dst) {
		t.Fatal("DecodeBody accepted ambiguous unknown fields")
	}
	var problem ProblemDetails
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if len(problem.Errors) != 1 || problem.Errors[0].Pointer != "" {
		t.Fatalf("problem = %+v, want no ambiguous pointer", problem)
	}
}

func TestDecodeBodyBoundsMapKeyInUnknownFieldPointer(t *testing.T) {
	t.Parallel()
	longKey := string(bytes.Repeat([]byte("x"), 1100))
	body := fmt.Sprintf(`{"items":{"%s":{"ignored":1}}}`, longKey)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	var dst struct {
		Items map[string]struct{} `json:"items"`
	}
	if DecodeBody(rec, req, &dst) {
		t.Fatal("DecodeBody accepted nested unknown field")
	}
	var problem ProblemDetails
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if len(problem.Errors) != 1 || problem.Errors[0].Pointer != "" {
		t.Fatalf("problem = %+v, want bounded empty pointer", problem)
	}
}

func TestDecodeBodyRejectsMemberTypeWithPointer(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut, "/", bytes.NewBufferString(`{"enabled":"yes"}`),
	)
	var dst struct {
		Enabled bool `json:"enabled"`
	}
	if DecodeBody(rec, req, &dst) {
		t.Fatal("DecodeBody accepted an invalid member type")
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", rec.Code)
	}
	var problem ProblemDetails
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if len(problem.Errors) != 1 || problem.Errors[0].Pointer != "/enabled" ||
		problem.Errors[0].Constraint != "type" {
		t.Fatalf("problem = %+v", problem)
	}
}

func TestDecodeBodyRejectsInvalidUnicode(t *testing.T) {
	t.Parallel()

	invalidUTF8 := append([]byte(`{"value":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	tests := []struct {
		name string
		body []byte
	}{
		{"invalid UTF-8", invalidUTF8},
		{"lone high surrogate", []byte(`{"value":"\ud800"}`)},
		{"lone low surrogate", []byte(`{"value":"\udc00"}`)},
		{"mismatched surrogate pair", []byte(`{"value":"\ud800\u0041"}`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(
				http.MethodPut, "/", bytes.NewReader(tc.body),
			)
			var dst struct {
				Value string `json:"value"`
			}
			if DecodeBody(rec, req, &dst) {
				t.Fatal("DecodeBody accepted invalid Unicode")
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestDecodeBodyAcceptsValidUnicodeEscapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{"surrogate pair", `{"value":"\ud83d\ude00"}`, "\U0001F600"},
		{"uppercase surrogate pair", `{"value":"\uD83D\uDE00"}`, "\U0001F600"},
		{"replacement character", `{"value":"\ufffd"}`, "\ufffd"},
		{"escaped backslash", `{"value":"\\ud800"}`, `\ud800`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(
				http.MethodPut, "/", bytes.NewBufferString(tc.body),
			)
			var dst struct {
				Value string `json:"value"`
			}
			if !DecodeBody(rec, req, &dst) {
				t.Fatalf("valid Unicode rejected: %s", rec.Body.String())
			}
			if dst.Value != tc.want {
				t.Fatalf("value = %q, want %q", dst.Value, tc.want)
			}
		})
	}
}

func TestDecodeBodyAllowUnknownFields(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost, "/",
		bytes.NewBufferString(`{"enabled":true,"future":{"value":1}}`),
	)
	var dst struct {
		Enabled bool `json:"enabled"`
	}
	if !DecodeBodyAllowUnknownFields(rec, req, &dst) {
		t.Fatalf("unknown field rejected: %s", rec.Body.String())
	}
	if !dst.Enabled {
		t.Fatal("declared field was not decoded")
	}
}

func TestDecodeBodyAllowUnknownFieldsRejectsInvalidUnicode(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost, "/",
		bytes.NewBufferString(`{"enabled":true,"future":"\ud800"}`),
	)
	var dst struct {
		Enabled bool `json:"enabled"`
	}
	if DecodeBodyAllowUnknownFields(rec, req, &dst) {
		t.Fatal("unknown field with invalid Unicode was accepted")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestPathAccountIDDoesNotDecodeChiParamTwice(t *testing.T) {
	t.Parallel()

	router := chi.NewRouter()
	router.Get("/accounts/{code}", func(w http.ResponseWriter, r *http.Request) {
		accountID, err := PathAccountID(r)
		if err != nil {
			t.Fatalf("PathAccountID: %v", err)
		}
		_, _ = w.Write([]byte(accountID))
	})
	tests := []struct {
		path string
		want string
	}{
		{path: "/accounts/acc%25401", want: "acc%401"},
		{path: "/accounts/acc%401", want: "acc@1"},
	}
	for _, tc := range tests {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if got := rec.Body.String(); got != tc.want {
			t.Fatalf("PathAccountID(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestWriteErrAccountMissing covers the account_missing envelope: a 404 with its
// own code and the offending account code as a structured field, so a client
// matches on structure instead of parsing the message.
func TestWriteErrAccountMissing(t *testing.T) {
	rec := httptest.NewRecorder()

	cause := fmt.Errorf(
		"block account: %w", domain.NewAccountMissingError("acc-1"),
	)
	WriteErr(rec, cause)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Account string `json:"account"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "account_missing" {
		t.Fatalf("code = %q, want account_missing", body.Error.Code)
	}
	if body.Error.Account != "acc-1" {
		t.Fatalf("account = %q, want acc-1", body.Error.Account)
	}
	if body.Error.Message != cause.Error() {
		t.Fatalf("message = %q, want %q", body.Error.Message, cause)
	}
}

// TestWriteErrAccountMissingWinsOverNotFound guards the case ordering: an error
// that is both an account_missing and a not_found (a caller wrapping both) must
// still report the more specific code.
func TestWriteErrAccountMissingWinsOverNotFound(t *testing.T) {
	rec := httptest.NewRecorder()

	WriteErr(rec, fmt.Errorf(
		"%w: %w", domain.NewAccountMissingError("acc-2"), domain.ErrNotFound,
	))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Account string `json:"account"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "account_missing" {
		t.Fatalf("code = %q, want account_missing", body.Error.Code)
	}
	if body.Error.Account != "acc-2" {
		t.Fatalf("account = %q, want acc-2", body.Error.Account)
	}
}

// TestWriteErrAssetMissing covers the asset_missing envelope: a 404 with its own
// code and the missing asset code as a structured field.
func TestWriteErrAssetMissing(t *testing.T) {
	rec := httptest.NewRecorder()

	cause := fmt.Errorf(
		"upsert trading instrument: %w", domain.NewAssetMissingError("USDT"),
	)
	WriteErr(rec, cause)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Asset   string `json:"asset"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "asset_missing" {
		t.Fatalf("code = %q, want asset_missing", body.Error.Code)
	}
	if body.Error.Asset != "USDT" {
		t.Fatalf("asset = %q, want USDT", body.Error.Asset)
	}
	if body.Error.Message != cause.Error() {
		t.Fatalf("message = %q, want %q", body.Error.Message, cause)
	}
}

func TestWriteErrExecutionReportRequired(t *testing.T) {
	rec := httptest.NewRecorder()

	WriteErr(rec, domain.ErrExecutionReportRequired)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "execution_report_required" {
		t.Fatalf("code = %q, want execution_report_required", body.Error.Code)
	}
	if body.Error.Message != domain.ErrExecutionReportRequired.Error() {
		t.Fatalf("message = %q, want %q",
			body.Error.Message, domain.ErrExecutionReportRequired.Error())
	}
}

func TestWriteErrApprovalRequired(t *testing.T) {
	rec := httptest.NewRecorder()
	cause := fmt.Errorf("confirm order: %w", domain.ErrApprovalRequired)

	WriteErr(rec, cause)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "approval_required" {
		t.Fatalf("code = %q, want approval_required", body.Error.Code)
	}
	if body.Error.Message != cause.Error() {
		t.Fatalf("message = %q, want %q", body.Error.Message, cause)
	}
}

func TestWriteErrValidationMetadata(t *testing.T) {
	rec := httptest.NewRecorder()

	_, err := domain.ExecutionReportRequiresEngine(domain.ExecutionReportInput{
		OrderStatus: domain.OrderStatusFilled,
	})
	if err == nil {
		t.Fatal("expected execution-report validation error")
	}
	WriteErr(rec, err)
	want := httptest.NewRecorder()
	WriteValidationProblem(
		want,
		err.Error(),
		domain.ValidationPointer(err),
		domain.ValidationConstraint(err),
	)
	if rec.Code != want.Code || !bytes.Equal(rec.Body.Bytes(), want.Body.Bytes()) {
		t.Fatalf(
			"WriteErr response = (%d, %q), want (%d, %q)",
			rec.Code, rec.Body, want.Code, want.Body,
		)
	}

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf(
			"status = %d, want %d",
			rec.Code,
			http.StatusUnprocessableEntity,
		)
	}
	var body ProblemDetails
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Errors) != 1 {
		t.Fatalf("errors = %+v, want one validation error", body.Errors)
	}
	got := body.Errors[0]
	if got.Code != "validation" ||
		got.Pointer != "/quantity" ||
		got.Constraint != "required_for_status" {
		t.Fatalf(
			"validation metadata = %+v, want code, pointer, and constraint",
			got,
		)
	}
	if body.Detail != err.Error() {
		t.Fatalf("detail = %q, want %q", body.Detail, err)
	}
}

func TestWriteErrValidationNestedPointer(t *testing.T) {
	rec := httptest.NewRecorder()
	err := domain.NewValidationError(
		"/commission/amount",
		"format",
		"commission amount must be a valid decimal",
	)

	WriteErr(rec, err)

	var body ProblemDetails
	if decodeErr := json.NewDecoder(rec.Body).Decode(&body); decodeErr != nil {
		t.Fatalf("decode response: %v", decodeErr)
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf(
			"status = %d, want %d",
			rec.Code,
			http.StatusUnprocessableEntity,
		)
	}
	if len(body.Errors) != 1 {
		t.Fatalf("errors = %+v, want one validation error", body.Errors)
	}
	got := body.Errors[0]
	if got.Pointer != "/commission/amount" || got.Constraint != "format" {
		t.Fatalf(
			"validation metadata = %+v, want nested pointer and format constraint",
			got,
		)
	}
}
