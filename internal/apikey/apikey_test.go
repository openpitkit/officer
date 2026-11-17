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
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/internal/apikey"
)

func TestParseBybit(t *testing.T) {
	got, err := apikey.Parse(`{"apiKey":" test-key-marker ","apiSecret":" test-secret-marker "}`, "bybit")
	if err != nil || got != (apikey.Credentials{APIKey: "test-key-marker", APISecret: "test-secret-marker"}) {
		t.Fatal("valid Bybit credentials were refused or not trimmed")
	}
}

func TestParseWithPassphrase(t *testing.T) {
	got, fields, err := apikey.ParseWithPassphrase(`{"apiKey":" test-key-marker ","apiSecret":" test-secret-marker ","passphrase":" \t test-passphrase-marker \n "}`, "okx")
	if err != nil || len(fields) != 0 || got != (apikey.PassphraseCredentials{
		APIKey: "test-key-marker", APISecret: "test-secret-marker", Passphrase: "test-passphrase-marker",
	}) {
		t.Fatal("valid passphrase credentials were refused or not trimmed")
	}
}

func TestParseKey(t *testing.T) {
	got, err := apikey.ParseKey(`{"apiKey":" \t test-key-marker \n "}`, "openalgo")
	if err != nil || got != "test-key-marker" {
		t.Fatal("valid single-key credentials were refused or not trimmed")
	}
}

func TestCredentialShapeValidation(t *testing.T) {
	key := generateECKey(t, elliptic.P256())
	for _, shape := range []struct {
		name, vendor string
		names        []string
		values       []string
		parse        func(string, string) error
	}{
		{"passphrase", "okx", []string{"apiKey", "apiSecret", "passphrase"},
			[]string{"test-key-marker", "test-secret-marker", "test-passphrase-marker"},
			func(raw, vendor string) error { _, _, err := apikey.ParseWithPassphrase(raw, vendor); return err }},
		{"single_key", "openalgo", []string{"apiKey"}, []string{"test-key-marker"},
			func(raw, vendor string) error { _, err := apikey.ParseKey(raw, vendor); return err }},
		{"cdp", "coinbase", []string{"keyName", "privateKey"},
			[]string{"test-key-name-marker", encodeECKey(t, key)},
			func(raw, vendor string) error { _, err := apikey.ParseCDP(raw, vendor); return err }},
	} {
		t.Run(shape.name, func(t *testing.T) {
			for _, name := range shape.names {
				for _, blank := range []bool{false, true} {
					kind := "missing/"
					if blank {
						kind = "blank/"
					}
					t.Run(kind+name, func(t *testing.T) {
						fields := make(map[string]string)
						for i, field := range shape.names {
							fields[field] = shape.values[i]
						}
						delete(fields, name)
						if blank {
							fields[name] = " \t\r\n "
						}
						err := shape.parse(credentialsJSON(t, fields), shape.vendor)
						assertValidationError(t, err, "/"+name, "required", shape.vendor+": "+name+" is required")
					})
				}
			}
			for _, tc := range []struct {
				name, raw, pointer, code, message string
			}{
				{"required_order", `{}`, "/" + shape.names[0], "required", shape.vendor + ": " + shape.names[0] + " is required"},
				{"unknown_field", credentialsJSON(t, map[string]string{"unknown-member-marker": "test-value-marker"}), "", "unknown_field", shape.vendor + ": unknown credentials field"},
				{"invalid_JSON", `{"test-value-marker"`, "", "type", shape.vendor + ": invalid credentials JSON"},
				{"non_object", `[]`, "", "type", shape.vendor + ": invalid credentials JSON"},
				{"non_string", `{"` + shape.names[0] + `":42}`, "", "type", shape.vendor + ": invalid credentials JSON"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					assertValidationError(t, shape.parse(tc.raw, shape.vendor), tc.pointer, tc.code, tc.message)
				})
			}
			t.Run("unknown_extra_field", func(t *testing.T) {
				fields := map[string]string{"unknown-member-marker": "test-value-marker"}
				for i, name := range shape.names {
					fields[name] = shape.values[i]
				}
				assertValidationError(t, shape.parse(credentialsJSON(t, fields), shape.vendor), "", "unknown_field", shape.vendor+": unknown credentials field")
			})
		})
	}
}

func TestParseCDPPrivateKey(t *testing.T) {
	key := generateECKey(t, elliptic.P256())
	validPEM := encodeECKey(t, key)
	p384PEM := encodeECKey(t, generateECKey(t, elliptic.P384()))
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal("could not encode test P-256 key")
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal("could not generate test Ed25519 key")
	}
	edPKCS8, err := x509.MarshalPKCS8PrivateKey(edKey)
	if err != nil {
		t.Fatal("could not encode test Ed25519 key")
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal("could not generate test RSA key")
	}
	block, _ := pem.Decode([]byte(validPEM))
	for _, tc := range []struct {
		name, privateKey string
		valid            bool
	}{
		{"LF", validPEM, true},
		{"CRLF", strings.ReplaceAll(validPEM, "\n", "\r\n"), true},
		{"surrounding_whitespace", " \t\n" + validPEM + " \r\n\t", true},
		{"P384", p384PEM, false},
		{"P256_PKCS8", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})), false},
		{"Ed25519_PKCS8", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: edPKCS8})), false},
		{"RSA", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)})), false},
		{"garbage", "test-private-key-marker", false},
		{"leading_text", "test-leading-marker\n" + validPEM, false},
		{"leading_malformed_block", "-----BEGIN EC PRIVATE KEY-----\ntest-malformed-marker\n" + validPEM, false},
		{"second_block", validPEM + validPEM, false},
		{"trailing_text", validPEM + "test-trailing-marker", false},
		{"headers", string(pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: block.Bytes,
			Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "test-encryption-marker"}})), false},
		{"other_header", string(pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: block.Bytes,
			Headers: map[string]string{"Test-Header": "test-header-marker"}})), false},
		{"wrong_block_type", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: block.Bytes})), false},
		{"invalid_SEC1", string(pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: []byte("test-DER-marker")})), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := credentialsJSON(t, map[string]string{"keyName": " \t test-key-name-marker \n ", "privateKey": tc.privateKey})
			got, err := apikey.ParseCDP(raw, "coinbase")
			if tc.valid {
				if err != nil || got.KeyName != "test-key-name-marker" || got.PrivateKey == nil || !got.PrivateKey.Equal(key) {
					t.Fatal("valid CDP credentials were refused, changed or not trimmed")
				}
				return
			}
			assertValidationError(t, err, "/privateKey", "format", "coinbase: privateKey must be a PEM EC P-256 private key")
			if got != (apikey.CDPCredentials{}) {
				t.Fatal("invalid private key returned credentials")
			}
		})
	}
}

func assertValidationError(t *testing.T, err error, pointer, code, message string) {
	t.Helper()
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("expected ErrInvalid")
	}
	if domain.ValidationPointer(err) != pointer {
		t.Fatal("unexpected validation pointer")
	}
	if domain.ValidationConstraint(err) != code {
		t.Fatal("unexpected validation constraint")
	}
	if err.Error() != message {
		t.Fatal("unexpected validation message or leaked input")
	}
}

func credentialsJSON(t *testing.T, fields map[string]string) string {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal("could not encode test credentials")
	}
	return string(raw)
}

func generateECKey(t *testing.T, curve elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal("could not generate test EC key")
	}
	return key
}

func encodeECKey(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal("could not encode test EC key")
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

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

func TestParseWithPassphraseExtraFields(t *testing.T) {
	raw := `{"apiKey":" test-key-marker ","apiSecret":" test-secret-marker ","passphrase":" test-passphrase-marker ","site":" example.test ","region":" region-marker "}`
	for _, tc := range []struct {
		names, want []string
	}{
		{[]string{"site", "region"}, []string{"example.test", "region-marker"}},
		{[]string{"region", "site"}, []string{"region-marker", "example.test"}},
	} {
		got, fields, err := apikey.ParseWithPassphrase(raw, "test", tc.names...)
		if err != nil || got != (apikey.PassphraseCredentials{
			APIKey: "test-key-marker", APISecret: "test-secret-marker",
			Passphrase: "test-passphrase-marker",
		}) || len(fields) != 2 || fields[0] != tc.want[0] || fields[1] != tc.want[1] {
			t.Fatal("required non-secret fields were not trimmed or returned in order")
		}
	}
	if _, _, err := apikey.ParseWithPassphrase(raw, "test"); err == nil {
		t.Fatal("passphrase parser accepted unnamed extra fields")
	}
	base := `{"apiKey":"test-key-marker","apiSecret":"test-secret-marker","passphrase":"test-passphrase-marker"}`
	plain, extra, err := apikey.ParseWithPassphrase(base, "test")
	if err != nil || plain != (apikey.PassphraseCredentials{
		APIKey: "test-key-marker", APISecret: "test-secret-marker",
		Passphrase: "test-passphrase-marker",
	}) || len(extra) != 0 {
		t.Fatal("zero-extra-field shape changed passphrase parsing")
	}
}

func TestParseWithPassphraseExtraFieldsValidation(t *testing.T) {
	for _, tc := range []struct{ name, raw, pointer, code, message string }{
		{"missing", `{"apiKey":"test-key-marker","apiSecret":"test-secret-marker","passphrase":"test-passphrase-marker"}`, "/site", "required", "test: site is required"},
		{"blank", `{"apiKey":"test-key-marker","apiSecret":"test-secret-marker","passphrase":"test-passphrase-marker","site":"  "}`, "/site", "required", "test: site is required"},
		{"null", `{"apiKey":"test-key-marker","apiSecret":"test-secret-marker","passphrase":"test-passphrase-marker","site":null}`, "/site", "required", "test: site is required"},
		{"type", `{"apiKey":"test-key-marker","apiSecret":"test-secret-marker","passphrase":"test-passphrase-marker","site":42}`, "", "type", "test: invalid credentials JSON"},
		{"unknown", `{"apiKey":"test-key-marker","apiSecret":"test-secret-marker","passphrase":"test-passphrase-marker","site":"example.test","unknown-name-marker":"unknown-value-marker"}`, "", "unknown_field", "test: unknown credentials field"},
		{"missing_secret", `{"apiKey":"test-key-marker","apiSecret":"test-secret-marker","site":"example.test"}`, "/passphrase", "required", "test: passphrase is required"},
		{"malformed", `{"apiKey":"test-key-marker"`, "", "type", "test: invalid credentials JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credentials, fields, err := apikey.ParseWithPassphrase(tc.raw, "test", "site")
			assertValidationError(t, err, tc.pointer, tc.code, tc.message)
			if credentials != (apikey.PassphraseCredentials{}) || fields != nil {
				t.Fatal("failed parse returned partial credentials or fields")
			}
		})
	}
}
