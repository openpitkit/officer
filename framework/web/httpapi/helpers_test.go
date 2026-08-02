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
// Please see https://openpit.dev and the OWNERS file for details.

package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestDecodeBodyRequiresOneNonNullValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"null", "null"},
		{"trailing value", `{"enabled":true}{"ignored":true}`},
		{"trailing garbage", `{"enabled":true}garbage`},
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
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
		})
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
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
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
		{"surrogate pair", `{"value":"\ud83d\ude00"}`, "😀"},
		{"uppercase surrogate pair", `{"value":"\uD83D\uDE00"}`, "😀"},
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
