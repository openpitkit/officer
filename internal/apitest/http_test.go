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

package apitest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordingTB records the message of the first Fatalf. ExpectStatus returns
// right after it, as it does under a real test.
type recordingTB struct {
	testing.TB
	failed  bool
	message string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(format string, args ...any) {
	if r.failed {
		return
	}
	r.failed = true
	r.message = fmt.Sprintf(format, args...)
}

func answer(status int, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rec.WriteHeader(status)
	_, _ = rec.WriteString(body)
	return rec
}

func TestExpectStatusNamesABodyThatIsNotJSON(t *testing.T) {
	const marker = "distinctive-marker-9f3a"
	for _, tc := range []struct {
		name string
		body string
	}{
		{"html", "<html><body>" + marker + "</body></html>"},
		{"plain text", marker},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tb := &recordingTB{TB: t}
			ExpectStatus(tb, "GET /x", answer(http.StatusInternalServerError, tc.body), http.StatusOK)
			if !tb.failed {
				t.Fatal("ExpectStatus did not fail on a status mismatch")
			}
			if !strings.Contains(tb.message, "not JSON") {
				t.Errorf("message %q does not say the body is not JSON", tb.message)
			}
			if !strings.Contains(tb.message, fmt.Sprintf("(%d bytes)", len(tc.body))) {
				t.Errorf("message %q does not carry the body size %d", tb.message, len(tc.body))
			}
			if strings.Contains(tb.message, marker) {
				t.Error("message carries the body")
			}
		})
	}
}

func TestExpectStatusKeepsTheRefusalMessageForJSON(t *testing.T) {
	tb := &recordingTB{TB: t}
	body := `{"errors":[{"code":"invalid","pointer":"/password"}]}`
	ExpectStatus(tb, "POST /x", answer(http.StatusUnprocessableEntity, body), http.StatusOK)
	if !tb.failed {
		t.Fatal("ExpectStatus did not fail on a status mismatch")
	}
	for _, want := range []string{"invalid", "/password"} {
		if !strings.Contains(tb.message, want) {
			t.Errorf("message %q does not carry %q", tb.message, want)
		}
	}
	if strings.Contains(tb.message, "not JSON") {
		t.Errorf("message %q calls a JSON body not JSON", tb.message)
	}
}

func TestExpectStatusPassesOnAMatchingStatus(t *testing.T) {
	tb := &recordingTB{TB: t}
	ExpectStatus(tb, "GET /x", answer(http.StatusOK, "not json"), http.StatusOK)
	if tb.failed {
		t.Fatalf("ExpectStatus failed on a matching status: %s", tb.message)
	}
}
