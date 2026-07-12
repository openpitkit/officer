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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

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
