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

package schema_test

import (
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/store/schema"
)

type testDialect struct{}

func (testDialect) PrimaryKey() string { return "TEST_PK" }
func (testDialect) ExternalID() string { return "TEST_XID" }
func (testDialect) Bool() string       { return "TEST_BOOL" }
func (testDialect) Decimal() string    { return "TEST_DECIMAL" }

func TestEmbeddedMigrationSourceRendersCanonicalSchema(t *testing.T) {
	migrations, err := schema.NewEmbeddedMigrationSource(testDialect{}).Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	if len(migrations) != 1 {
		t.Fatalf("migration count = %d, want 1", len(migrations))
	}
	text := migrations[0].SQL
	for _, want := range []string{"TEST_PK", "TEST_XID", "TEST_BOOL", "TEST_DECIMAL"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered schema does not contain %q", want)
		}
	}
	if strings.Contains(text, "{{") {
		t.Fatalf("rendered schema contains an unresolved token: %q", text)
	}
}
