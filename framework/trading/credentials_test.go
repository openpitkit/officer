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

package trading_test

import (
	"reflect"
	"testing"

	"go.openpit.dev/officer/framework/trading"
)

func TestCredentialRedactorSpellings(t *testing.T) {
	// The apiKey is stored padded; its raw, JSON-escaped (HTML-escaped or
	// not), URL query-escaped and URL path-escaped spellings all differ.
	const credentials = `{"apiKey":"  echo \"q\" \\ <a&b> +c/d  ",` +
		`"apiSecret":"   ","extra":{"list":["nested-value"]}}`
	secrets, redact, err := trading.CredentialRedactor(credentials)
	if err != nil {
		t.Fatalf("CredentialRedactor: %v", err)
	}
	if want := map[string]bool{"apiKey": true, "extra": true}; !reflect.DeepEqual(secrets, want) {
		t.Fatalf("secrets = %v, want %v", secrets, want)
	}
	for _, tc := range []struct{ form, text string }{
		{"raw", `echo "q" \ <a&b> +c/d`},
		{"json", `echo \"q\" \\ \u003ca\u0026b\u003e +c/d`},
		{"json_unescaped_html", `echo \"q\" \\ <a&b> +c/d`},
		{"url_query", `echo+%22q%22+%5C+%3Ca%26b%3E+%2Bc%2Fd`},
		{"url_path", `echo%20%22q%22%20%5C%20%3Ca&b%3E%20+c%2Fd`},
		{"nested", "nested-value"},
	} {
		if got := redact.Replace("venue said " + tc.text + "."); got != "venue said [redacted]." {
			t.Errorf("%s spelling: got %q", tc.form, got)
		}
	}
	// A blank value holds no secret, so whitespace is left as it is.
	if got := redact.Replace("a   b"); got != "a   b" {
		t.Errorf("blank value redacted: got %q", got)
	}
}

func TestCredentialRedactorLongestFirst(t *testing.T) {
	for _, tc := range []struct{ name, credentials, text string }{
		{"key contains secret", `{"apiKey":"overlap-long","apiSecret":"overlap"}`, "refused overlap-long overlap"},
		{"secret contains key", `{"apiKey":"overlap","apiSecret":"overlap-long"}`, "refused overlap-long overlap"},
		// The raw secret is a prefix of the key's URL-escaped spelling.
		{"escaped key contains secret", `{"apiKey":"prefix/very-long-suffix","apiSecret":"prefix"}`, "refused prefix%2Fvery-long-suffix prefix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, redact, err := trading.CredentialRedactor(tc.credentials)
			if err != nil {
				t.Fatalf("CredentialRedactor: %v", err)
			}
			if got := redact.Replace(tc.text); got != "refused [redacted] [redacted]" {
				t.Fatalf("got %q, want both credentials redacted whole", got)
			}
		})
	}
}
