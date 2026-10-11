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

package trading

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"go.openpit.dev/officer/framework/domain"
)

// CredentialRedactor is the credential redactor that connectors and the
// backend boundary share. It reads a connection's stored credentials, a JSON
// object, and returns the members that hold a value and a replacer that
// redacts each non-blank credential value, trimmed, spelled raw, JSON-escaped
// (HTML-escaped or not), URL query- or path-escaped. The replacer tries the
// longest spelling first, so a credential inside a longer credential's
// spelling is redacted with it and leaves no fragment.
func CredentialRedactor(credentials string) (map[string]bool, *strings.Replacer, error) {
	var values map[string]any
	if err := json.Unmarshal([]byte(credentials), &values); err != nil || values == nil {
		return nil, nil, fmt.Errorf("stored trading credentials are not a JSON object: %w", domain.ErrInvalid)
	}
	secrets := make(map[string]bool)
	var credentialStrings []string
	var redact func(any)
	redact = func(value any) {
		switch v := value.(type) {
		case string:
			// A blank value holds no secret; redacting it would garble the
			// whitespace of every message that names the refused member.
			// Providers trim a value before use, so a venue echoes it
			// trimmed; the trimmed value also covers the padded one.
			if v = strings.TrimSpace(v); v != "" {
				credentialStrings = append(credentialStrings, v)
			}
		case []any:
			for _, item := range v {
				redact(item)
			}
		case map[string]any:
			for _, item := range v {
				redact(item)
			}
		}
	}
	for key, value := range values {
		set := value != nil
		switch v := value.(type) {
		case string:
			set = strings.TrimSpace(v) != ""
		case []any:
			set = len(v) > 0
		case map[string]any:
			set = len(v) > 0
		}
		if set {
			secrets[key] = true
		}
		redact(value)
	}
	spellings := make(map[string]bool)
	for _, value := range credentialStrings {
		spellings[value] = true
		spellings[url.QueryEscape(value)] = true
		spellings[url.PathEscape(value)] = true
		for _, escapeHTML := range []bool{true, false} {
			var quoted bytes.Buffer
			encoder := json.NewEncoder(&quoted)
			encoder.SetEscapeHTML(escapeHTML)
			if err := encoder.Encode(value); err != nil {
				return nil, nil, fmt.Errorf("encode stored trading credential: %w", err)
			}
			// Encode writes the quoted string and a newline.
			spellings[string(quoted.Bytes()[1:quoted.Len()-2])] = true
		}
	}
	ordered := make([]string, 0, len(spellings))
	for spelling := range spellings {
		ordered = append(ordered, spelling)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return len(ordered[i]) > len(ordered[j])
	})
	pairs := make([]string, 0, len(ordered)*2)
	for _, spelling := range ordered {
		pairs = append(pairs, spelling, "[redacted]")
	}
	return secrets, strings.NewReplacer(pairs...), nil
}
