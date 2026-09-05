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

package schema

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"go.openpit.dev/officer/framework/migration"
)

//go:embed fragments/principal.sql
var principalSQL string

//go:embed migrations/*.sql
var migrationsFS embed.FS

// EmbeddedMigrationSource renders the canonical embedded migrations for one
// database dialect.
type EmbeddedMigrationSource struct {
	dialect Dialect
}

// NewEmbeddedMigrationSource creates the canonical migration source for d.
func NewEmbeddedMigrationSource(d Dialect) EmbeddedMigrationSource {
	return EmbeddedMigrationSource{dialect: d}
}

// PrincipalTableSQL renders the canonical principal dictionary independently of
// the rest of the store schema. primaryKey is trusted SQL for the surrogate key;
// tableName is trusted SQL for a table identifier, optionally schema-qualified.
// Dynamic identifiers must already be validated and quoted. Callers must never
// pass unvalidated application input as either argument.
func PrincipalTableSQL(primaryKey, tableName string) string {
	return strings.NewReplacer("{{PK}}", primaryKey, "{{TABLE}}", tableName).
		Replace(principalSQL)
}

// Migrations returns the canonical migrations with the dialect tokens rendered.
func (s EmbeddedMigrationSource) Migrations() ([]migration.Migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store schema: read embedded migrations: %w", err)
	}

	migrations := make([]migration.Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := migration.ParseVersion(entry.Name())
		if err != nil {
			return nil, err
		}
		body, err := fs.ReadFile(migrationsFS, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf(
				"store schema: read migration %q: %w", entry.Name(), err,
			)
		}
		migrations = append(migrations, migration.Migration{
			Version: version,
			Name:    entry.Name(),
			SQL:     Render(s.dialect, string(body)),
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})
	return migrations, nil
}
