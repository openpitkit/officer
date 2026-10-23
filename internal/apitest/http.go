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

// Package apitest holds test helpers that check Officer HTTP answers without
// printing their bodies.
package apitest

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// Refusal returns the error code of a refused request's answer and, for a
// validation problem, the pointer of its first error. Both are empty for an
// answer that carries no error.
func Refusal(rec *httptest.ResponseRecorder) (code, pointer string) {
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

// ExpectStatus checks the response status for call without printing its body.
// Call names the request by method and path.
func ExpectStatus(t testing.TB, call string, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		code, pointer := Refusal(rec)
		t.Fatalf("%s = %d %s %q, want %d", call, rec.Code, code, pointer, want)
	}
}
