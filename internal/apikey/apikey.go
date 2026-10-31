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

// Package apikey holds API key credentials and parsing shared by the Alpaca,
// Binance and eToro connectors.
package apikey

import (
	"encoding/json"
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
	var fields map[string]string
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return Credentials{}, domain.NewValidationError("", "type", vendor+": invalid credentials JSON")
	}
	for field := range fields {
		if field != "apiKey" && field != secretField {
			return Credentials{}, domain.NewValidationError("", "unknown_field", vendor+": unknown credentials field")
		}
	}
	apiKey := strings.TrimSpace(fields["apiKey"])
	if apiKey == "" {
		return Credentials{}, domain.NewValidationError("/apiKey", "required", vendor+": apiKey is required")
	}
	apiSecret := strings.TrimSpace(fields[secretField])
	if apiSecret == "" {
		return Credentials{}, domain.NewValidationError("/"+secretField, "required", vendor+": "+secretField+" is required")
	}
	return Credentials{APIKey: apiKey, APISecret: apiSecret}, nil
}
