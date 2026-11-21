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

package domain_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestValidateAuditDecisionVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict domain.Verdict
		code    string
		invalid bool
	}{
		{name: "accept", verdict: domain.VerdictAccept},
		{name: "accept with code", verdict: domain.VerdictAccept, code: "reason", invalid: true},
		{name: "reject", verdict: domain.VerdictReject, code: "reason"},
		{name: "reject without code", verdict: domain.VerdictReject},
		{name: "review", verdict: domain.VerdictReview, code: "reason"},
		{name: "review without code", verdict: domain.VerdictReview},
		{name: "empty", invalid: true},
		{name: "unknown", verdict: "unknown", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, action := range []domain.AuditAction{domain.AuditActionSubmitOrder, domain.AuditActionSubmitDropCopy} {
				err := domain.ValidateAuditDecision(action, "order", tc.verdict, tc.code)
				if tc.invalid {
					if !errors.Is(err, domain.ErrInvalid) {
						t.Fatalf("ValidateAuditDecision(%q) = %v, want ErrInvalid", tc.verdict, err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestApprovalPayloadReviewOmittedForAcceptAndReject(t *testing.T) {
	for _, verdict := range []domain.Verdict{domain.VerdictAccept, domain.VerdictReject} {
		payload := domain.ApprovalPayload{Version: 1, Verdict: verdict}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		payload.Reviews = []domain.ReviewMark{}
		emptyEncoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded, emptyEncoded) || bytes.Contains(encoded, []byte(`"reviews"`)) {
			t.Fatalf("empty reviews changed %q envelope: %s / %s", verdict, encoded, emptyEncoded)
		}
	}
}
