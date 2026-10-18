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

package httpapi

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"maps"
	"strings"
	"testing"
)

//go:embed swaggerui
var vendoredSwaggerUI embed.FS

// TestVendoredSwaggerUIMatchesSums fails on a vendored Swagger UI file edited,
// added or removed by hand: every file under swaggerui/ must hash to its
// SHA256SUMS line, and every line must name a file there. `just
// vendor-swagger-ui` rewrites the files and the sums together.
func TestVendoredSwaggerUIMatchesSums(t *testing.T) {
	sums, err := vendoredSwaggerUI.ReadFile("swaggerui/SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for line := range strings.Lines(string(sums)) {
		digest, name, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "  ")
		if !ok {
			t.Fatalf("SHA256SUMS line %q is not \"<sha256>  <file>\"", line)
		}
		if _, listed := want[name]; listed {
			t.Fatalf("SHA256SUMS lists %s twice", name)
		}
		want[name] = digest
	}
	entries, err := vendoredSwaggerUI.ReadDir("swaggerui")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, entry := range entries {
		if entry.Name() == "SHA256SUMS" {
			continue
		}
		content, err := vendoredSwaggerUI.ReadFile("swaggerui/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		got[entry.Name()] = hex.EncodeToString(sum[:])
	}
	if !maps.Equal(got, want) {
		t.Fatalf("vendored Swagger UI does not match SHA256SUMS; "+
			"refresh it with just vendor-swagger-ui\nfiles: %v\nsums:  %v",
			got, want)
	}
}
