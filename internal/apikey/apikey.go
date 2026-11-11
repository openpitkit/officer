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

// Package apikey holds API key credential shapes for venue connectors and their
// parsing.
package apikey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"slices"
	"strings"

	"go.openpit.dev/officer/framework/domain"
)

// Credentials identifies an account by its API key and secret.
type Credentials struct{ APIKey, APISecret string }

// Parse accepts only a JSON object with apiKey and apiSecret, each non-blank
// after trimming. Errors are domain validation errors prefixed with vendor;
// their JSON pointer is relative to the credentials object, and they never
// include input values or unknown field names.
func Parse(raw, vendor string) (Credentials, error) {
	return ParseWithSecretField(raw, vendor, "apiSecret")
}

// ParseWithSecretField is Parse for a venue whose secret field is named
// secretField instead of apiSecret; the secret is returned as APISecret.
func ParseWithSecretField(raw, vendor, secretField string) (Credentials, error) {
	values, err := parseFields(raw, vendor, "apiKey", secretField)
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{APIKey: values[0], APISecret: values[1]}, nil
}

// PassphraseCredentials identifies an account by its API key, secret and
// passphrase.
type PassphraseCredentials struct{ APIKey, APISecret, Passphrase string }

// ParseWithPassphrase accepts only apiKey, apiSecret and passphrase, each
// non-blank after trimming, and reports validation errors in that order.
func ParseWithPassphrase(raw, vendor string) (PassphraseCredentials, error) {
	values, err := parseFields(raw, vendor, "apiKey", "apiSecret", "passphrase")
	if err != nil {
		return PassphraseCredentials{}, err
	}
	return PassphraseCredentials{APIKey: values[0], APISecret: values[1], Passphrase: values[2]}, nil
}

// ParseKey accepts only apiKey, non-blank after trimming, and returns the
// trimmed key.
func ParseKey(raw, vendor string) (string, error) {
	values, err := parseFields(raw, vendor, "apiKey")
	if err != nil {
		return "", err
	}
	return values[0], nil
}

// CDPCredentials identifies a Coinbase CDP key by its name and P-256 private
// key.
type CDPCredentials struct {
	KeyName    string
	PrivateKey *ecdsa.PrivateKey
}

// ParseCDP accepts only keyName and privateKey, each non-blank after trimming.
// The private key must be one unencrypted SEC1 PEM block on P-256. Validation
// errors never include key material or decoder messages.
func ParseCDP(raw, vendor string) (CDPCredentials, error) {
	values, err := parseFields(raw, vendor, "keyName", "privateKey")
	if err != nil {
		return CDPCredentials{}, err
	}
	invalidKey := domain.NewValidationError("/privateKey", "format", vendor+": privateKey must be a PEM EC P-256 private key")
	privateKey := values[1]
	block, rest := pem.Decode([]byte(privateKey))
	// Decode skips leading text and malformed blocks before a valid block.
	if block == nil || !strings.HasPrefix(privateKey, "-----BEGIN ") ||
		strings.Count(privateKey, "-----BEGIN ") != 1 || strings.TrimSpace(string(rest)) != "" ||
		block.Type != "EC PRIVATE KEY" || len(block.Headers) != 0 {
		return CDPCredentials{}, invalidKey
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return CDPCredentials{}, invalidKey
	}
	if key.Curve != elliptic.P256() {
		return CDPCredentials{}, invalidKey
	}
	return CDPCredentials{KeyName: values[0], PrivateKey: key}, nil
}

func parseFields(raw, vendor string, names ...string) ([]string, error) {
	var fields map[string]string
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, domain.NewValidationError("", "type", vendor+": invalid credentials JSON")
	}
	for field := range fields {
		if !slices.Contains(names, field) {
			return nil, domain.NewValidationError("", "unknown_field", vendor+": unknown credentials field")
		}
	}
	values := make([]string, len(names))
	for i, name := range names {
		values[i] = strings.TrimSpace(fields[name])
		if values[i] == "" {
			return nil, domain.NewValidationError("/"+name, "required", vendor+": "+name+" is required")
		}
	}
	return values, nil
}
