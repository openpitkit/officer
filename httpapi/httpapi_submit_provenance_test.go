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
	"net/http"
	"net/http/httptest"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	fwsigning "go.openpit.dev/officer/framework/signing"
)

func TestSubmitOrderTokenSignsSurfacePrincipalAndOmitsAcceptReasons(t *testing.T) {
	t.Parallel()
	handler, realm := newRealServiceRouter(t)
	seedRealAccountAndAssets(t, realm, "acc-1")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/orders/submit?missingAccount=reject",
		bytes.NewBufferString(
			`{"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD",`+
				`"side":"buy","amountKind":"quantity","amountValue":"1","mode":"immediate",`+
				`"price":"10","mode":"hold"}`,
		),
	))
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit status = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	body := bodyMap(t, rec.Result())
	if _, present := body["reasons"]; present {
		t.Fatalf("accepted submit emitted reasons: %v", body)
	}
	token, _ := body["token"].(string)
	envelope, err := fwsigning.DecodeEnvelope(token)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if envelope.Approval.Verdict != "accept" ||
		envelope.Approval.Principal != domain.PrincipalOperator {
		t.Fatalf("approval payload = %+v", envelope.Approval)
	}
}
