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

package domain_test

import (
	"errors"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

// TestParseMissingAccountPolicyAccepted covers the two wire values.
func TestParseMissingAccountPolicyAccepted(t *testing.T) {
	t.Parallel()

	for _, want := range []domain.MissingAccountPolicy{
		domain.MissingAccountCreate,
		domain.MissingAccountReject,
	} {
		t.Run(string(want), func(t *testing.T) {
			t.Parallel()
			got, err := domain.ParseMissingAccountPolicy(string(want))
			if err != nil {
				t.Fatalf("ParseMissingAccountPolicy(%q): %v", want, err)
			}
			if got != want {
				t.Fatalf("policy = %q, want %q", got, want)
			}
		})
	}
}

// TestParseMissingAccountPolicyEmpty covers the required-parameter boundary: an
// absent value is never defaulted, so an automated caller has to choose.
func TestParseMissingAccountPolicyEmpty(t *testing.T) {
	t.Parallel()

	got, err := domain.ParseMissingAccountPolicy("")
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty policy error = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "missingAccount is required") {
		t.Fatalf("empty policy message = %q, want the required-parameter text", err)
	}
	if got != "" {
		t.Fatalf("policy = %q, want empty on error", got)
	}
}

// TestParseMissingAccountPolicyUnknown covers an unrecognised value: the error
// names both accepted values so a caller can correct the request.
func TestParseMissingAccountPolicyUnknown(t *testing.T) {
	t.Parallel()

	_, err := domain.ParseMissingAccountPolicy("maybe")
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown policy error = %v, want ErrInvalid", err)
	}
	msg := err.Error()
	for _, want := range []string{
		"maybe",
		string(domain.MissingAccountCreate),
		string(domain.MissingAccountReject),
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("unknown policy message %q does not name %q", msg, want)
		}
	}
}

// TestAccountMissingErrorMatching covers the sentinel contract: the typed error
// matches ErrAccountMissing, carries the offending code, and is deliberately not
// an ErrNotFound so the surface can emit a distinct error code.
func TestAccountMissingErrorMatching(t *testing.T) {
	t.Parallel()

	err := domain.NewAccountMissingError("acc-1")
	if !errors.Is(err, domain.ErrAccountMissing) {
		t.Fatalf("error = %v, want ErrAccountMissing", err)
	}
	if errors.Is(err, domain.ErrNotFound) {
		t.Fatal("ErrAccountMissing must not match ErrNotFound")
	}
	var typed domain.AccountMissingError
	if !errors.As(err, &typed) {
		t.Fatalf("error %v does not carry AccountMissingError", err)
	}
	if typed.Account != domain.AccountID("acc-1") {
		t.Fatalf("carried account = %q, want acc-1", typed.Account)
	}
	if !strings.Contains(err.Error(), "acc-1") {
		t.Fatalf("message %q does not name the account", err)
	}
}
