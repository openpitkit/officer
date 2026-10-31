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

package apikey_test

import (
	"errors"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/internal/apikey"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name, raw, pointer, code, message string
		want                              apikey.Credentials
	}{
		{
			name: "trimmed",
			raw:  `{"apiKey":" \t test-key-marker \n ","apiSecret":"\n test-secret-marker \t "}`,
			want: apikey.Credentials{APIKey: "test-key-marker", APISecret: "test-secret-marker"},
		},
		{
			name: "invalid_JSON", raw: `{"apiKey":"test-key-marker","apiSecret":"test-secret-marker"`,
			code: "type", message: "acme: invalid credentials JSON",
		},
		{
			name: "unknown_field", raw: `{"apiKey":"test-key-marker","apiSecret":"test-secret-marker","unknown-member-marker":"test-value-marker"}`,
			code: "unknown_field", message: "acme: unknown credentials field",
		},
		{
			name: "unknown_field_before_required", raw: `{"apiSecret":"test-secret-marker","unknown-member-marker":"x"}`,
			code: "unknown_field", message: "acme: unknown credentials field",
		},
		{
			name: "required_key", raw: `{"apiKey":" \n ","apiSecret":"test-secret-marker"}`,
			pointer: "/apiKey", code: "required", message: "acme: apiKey is required",
		},
		{
			name: "required_secret", raw: `{"apiKey":"test-key-marker","apiSecret":" \t "}`,
			pointer: "/apiSecret", code: "required", message: "acme: apiSecret is required",
		},
		{
			name: "non_string", raw: `{"apiKey":"test-key-marker","apiSecret":1}`,
			code: "type", message: "acme: invalid credentials JSON",
		},
		{
			name: "non_object", raw: `[]`,
			code: "type", message: "acme: invalid credentials JSON",
		},
		{
			name: "null", raw: `null`,
			pointer: "/apiKey", code: "required", message: "acme: apiKey is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := apikey.Parse(tc.raw, "acme")
			if tc.code == "" {
				if err != nil || got != tc.want {
					t.Fatal("valid credentials were refused or not trimmed")
				}
				return
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
			if got := domain.ValidationPointer(err); got != tc.pointer {
				t.Fatalf("pointer = %q, want %q", got, tc.pointer)
			}
			if got := domain.ValidationConstraint(err); got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
			if got := err.Error(); got != tc.message {
				t.Fatalf("message = %q, want %q", got, tc.message)
			}
			for _, marker := range []string{"test-key-marker", "test-secret-marker", "unknown-member-marker", "test-value-marker"} {
				if strings.Contains(err.Error(), marker) {
					t.Fatal("error contains an input value or unknown field name")
				}
			}
		})
	}
}

func TestParseWithSecretField(t *testing.T) {
	got, err := apikey.ParseWithSecretField(`{"apiKey":" test-key-marker ","userKey":" test-secret-marker "}`, "acme", "userKey")
	if err != nil || got != (apikey.Credentials{APIKey: "test-key-marker", APISecret: "test-secret-marker"}) {
		t.Fatal("valid credentials were refused or not trimmed")
	}
	for _, tc := range []struct {
		name, raw, pointer, code, message string
	}{
		{
			name: "default_secret_is_unknown", raw: `{"apiKey":"test-key-marker","apiSecret":"test-secret-marker"}`,
			code: "unknown_field", message: "acme: unknown credentials field",
		},
		{
			name: "required_secret", raw: `{"apiKey":"test-key-marker","userKey":" "}`,
			pointer: "/userKey", code: "required", message: "acme: userKey is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := apikey.ParseWithSecretField(tc.raw, "acme", "userKey")
			if !errors.Is(err, domain.ErrInvalid) || domain.ValidationPointer(err) != tc.pointer ||
				domain.ValidationConstraint(err) != tc.code || err.Error() != tc.message {
				t.Fatalf("error = %v, want %s at %q: %s", err, tc.code, tc.pointer, tc.message)
			}
		})
	}
}
