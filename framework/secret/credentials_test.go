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

package secret_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/secret"
)

func TestCredentialRedactorSpellings(t *testing.T) {
	// The apiKey is stored padded; its raw, JSON-escaped (HTML-escaped or
	// not), URL query-escaped and URL path-escaped spellings all differ.
	const credentials = `{"apiKey":"  echo \"q\" \\ <a&b> +c/d  ",` +
		`"apiSecret":"   ","extra":{"list":["nested-value"]}}`
	secrets, redact, err := secret.CredentialRedactor(credentials)
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

func TestCredentialRedactorFormattedSpellings(t *testing.T) {
	for _, key := range []struct{ name, value string }{
		{"quote", `key-"-marker`},
		{"backslash", `key-\-marker`},
		{"html", `key-<-marker`},
		{"non_printable", "key-\u0085-marker"},
		{"non_ascii", "key-é-marker"},
	} {
		t.Run(key.name, func(t *testing.T) {
			credentials, err := json.Marshal(map[string]string{"apiKey": key.value})
			if err != nil {
				t.Fatal("cannot encode test credentials")
			}
			_, redact, err := secret.CredentialRedactor(string(credentials))
			if err != nil {
				t.Fatal("cannot create credential redactor")
			}
			encoded, err := json.Marshal(key.value)
			if err != nil {
				t.Fatal("cannot encode test key")
			}
			for _, form := range []struct{ name, spelling string }{
				{"value", fmt.Sprintf("%v", key.value)},
				{"quoted", fmt.Sprintf("%q", key.value)},
				{"ascii_quoted", fmt.Sprintf("%+q", key.value)},
				{"go_quoted", fmt.Sprintf("%#q", key.value)},
				{"json", string(encoded)},
				{"url_query", url.QueryEscape(key.value)},
			} {
				t.Run(form.name, func(t *testing.T) {
					got := redact.Replace("venue said " + form.spelling + ".")
					if strings.Contains(got, form.spelling) || strings.Contains(got, key.value) {
						t.Fatal("formatted credential remains recoverable")
					}
					if !strings.HasPrefix(got, "venue said ") || !strings.HasSuffix(got, ".") ||
						!strings.Contains(got, "[redacted]") {
						t.Fatal("credential replacement or surrounding text changed")
					}
				})
			}
		})
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
			_, redact, err := secret.CredentialRedactor(tc.credentials)
			if err != nil {
				t.Fatalf("CredentialRedactor: %v", err)
			}
			if got := redact.Replace(tc.text); got != "refused [redacted] [redacted]" {
				t.Fatalf("got %q, want both credentials redacted whole", got)
			}
		})
	}
}

func TestCredentialRedactorSettings(t *testing.T) {
	for _, tc := range []struct {
		name, credentials, text, want string
		settings                      []string
		secrets                       map[string]bool
	}{
		{
			name: "top-level setting", settings: []string{"url"},
			credentials: `{"url":"http://gateway","apiKey":"k-secret"}`,
			text:        "http://gateway k-secret", want: "http://gateway [redacted]",
			secrets: map[string]bool{"apiKey": true},
		},
		{
			name: "setting subtree", settings: []string{"url"},
			credentials: `{"url":{"values":["gateway","ws-gateway"]},"apiKey":"k-secret"}`,
			text:        "gateway ws-gateway k-secret", want: "gateway ws-gateway [redacted]",
			secrets: map[string]bool{"apiKey": true},
		},
		{
			name: "nested setting name stays secret", settings: []string{"url"},
			credentials: `{"nested":{"url":"n-secret"},"apiKey":"k"}`,
			text:        "n-secret k", want: "[redacted] [redacted]",
			secrets: map[string]bool{"nested": true, "apiKey": true},
		},
		{
			name: "secret inside setting value", settings: []string{"url"},
			credentials: `{"url":"http://h/?t=k-secret","apiKey":"k-secret"}`,
			text:        "http://h/?t=k-secret k-secret", want: "http://h/?t=[redacted] [redacted]",
			secrets: map[string]bool{"apiKey": true},
		},
		{
			name:        "no settings",
			credentials: `{"url":"http://gateway","apiKey":"k-secret"}`,
			text:        "http://gateway k-secret", want: "[redacted] [redacted]",
			secrets: map[string]bool{"url": true, "apiKey": true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secrets, redact, err := secret.CredentialRedactor(tc.credentials, tc.settings...)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(secrets, tc.secrets) {
				t.Errorf("secrets = %v, want %v", secrets, tc.secrets)
			}
			if got := redact.Replace(tc.text); got != tc.want {
				t.Errorf("redacted text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCredentialFreeURL(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        bool
	}{
		{"http", "http://127.0.0.1:5000/path", true},
		{"ws", "ws://127.0.0.1:8765", true},
		{"padded_http", " \thttp://127.0.0.1:5000/path \n", true},
		{"padded_ws", " \nws://127.0.0.1:8765\t ", true},
		{"empty", "", false},
		{"blank", " \t", false},
		{"parse", "http://%", false},
		{"relative", "//host/path", false},
		{"no_host", "http:/path", false},
		{"userinfo", "http://user:password@host", false},
		{"empty_userinfo", "http://@host", false},
		{"query", "http://host/?token=value", false},
		{"bare_query", "http://host/?", false},
		{"fragment", "http://host/#token", false},
		{"bare_fragment", "http://host/#", false},
		{"padded_userinfo", " \thttp://user:password@host \n", false},
		{"padded_query", " \thttp://host/?token=value \n", false},
		{"padded_bare_query", " \thttp://host/? \n", false},
		{"padded_fragment", " \thttp://host/#token \n", false},
		{"padded_bare_fragment", " \thttp://host/# \n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			address, ok := secret.CredentialFreeURL(tc.value)
			if ok != tc.want || (address != nil) != tc.want {
				t.Fatal("unexpected credential-free URL acceptance")
			}
			if ok && address.String() != strings.TrimSpace(tc.value) {
				t.Fatal("clean address changed")
			}
		})
	}
}
