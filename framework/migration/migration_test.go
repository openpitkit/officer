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

package migration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type questionBinder struct{}

func (questionBinder) BindVar(int) string {
	return "?"
}

type positionalBinder struct{}

func (positionalBinder) BindVar(position int) string {
	return fmt.Sprintf("$%d", position)
}

type staticMigrationSource []Migration

func (s staticMigrationSource) Migrations() ([]Migration, error) {
	return s, nil
}

func TestVersionRecordInsertQuestionBinder(t *testing.T) {
	got := versionRecordInsert(`"schema_migration"`, questionBinder{})
	want := `INSERT INTO "schema_migration" ` +
		`(version, name, applied_at) VALUES (?, ?, ?)`
	if got != want {
		t.Errorf("versionRecordInsert() = %q, want %q", got, want)
	}
}

func TestVersionRecordInsertPositionalBinder(t *testing.T) {
	got := versionRecordInsert(`"schema_migration"`, positionalBinder{})
	want := `INSERT INTO "schema_migration" ` +
		`(version, name, applied_at) VALUES ($1, $2, $3)`
	if got != want {
		t.Errorf("versionRecordInsert() = %q, want %q", got, want)
	}
}

var tableNameCases = []struct {
	name string
	cfg  Config
	want string
}{
	{name: "default", want: `"schema_migration"`},
	{
		name: "explicit table",
		cfg:  Config{Table: "migration history"},
		want: `"migration history"`,
	},
	{
		name: "schema qualified",
		cfg:  Config{Schema: "registry", Table: "SchemaMigration"},
		want: `"registry"."SchemaMigration"`,
	},
	{
		name: "embedded quote",
		cfg:  Config{Table: `schema"migration`},
		want: `"schema""migration"`,
	},
	{
		name: "injection payload",
		cfg:  Config{Table: `x"; DROP TABLE accounts; --`},
		want: `"x""; DROP TABLE accounts; --"`,
	},
	{
		name: "hyphenated GUID schema",
		cfg: Config{
			Schema: "123e4567-e89b-12d3-a456-426614174000",
			Table:  "schema_migration",
		},
		want: `"123e4567-e89b-12d3-a456-426614174000".` +
			`"schema_migration"`,
	},
}

func TestTableName(t *testing.T) {
	for _, test := range tableNameCases {
		t.Run(test.name, func(t *testing.T) {
			got, err := tableName(test.cfg)
			if err != nil {
				t.Fatalf("tableName() error = %v, want nil", err)
			}
			if got != test.want {
				t.Errorf("tableName() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTableNameRejectsNUL(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		invalid string
	}{
		{
			name:    "schema",
			cfg:     Config{Schema: "registry\x00shadow"},
			invalid: "registry\x00shadow",
		},
		{
			name:    "table",
			cfg:     Config{Table: "migration\x00shadow"},
			invalid: "migration\x00shadow",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := tableName(test.cfg)
			if !errors.Is(err, errInvalidIdentifier) {
				t.Errorf(
					"tableName() error = %v, want invalid identifier error",
					err,
				)
			}
			if got != "" {
				t.Errorf("tableName() = %q, want empty name", got)
			}
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", test.invalid)) {
				t.Errorf("tableName() error = %v, want offending name", err)
			}
			if err == nil || !strings.Contains(err.Error(), "NUL byte") {
				t.Errorf("tableName() error = %v, want NUL byte reason", err)
			}
		})
	}
}

func FuzzTableNameQuoting(f *testing.F) {
	for _, test := range tableNameCases {
		f.Add(test.cfg.Schema, test.cfg.Table)
	}
	f.Add("registry\x00shadow", "schema_migration")
	f.Add("registry", "migration\x00shadow")

	f.Fuzz(func(t *testing.T, schema, table string) {
		effectiveTable := table
		if effectiveTable == "" {
			effectiveTable = defaultTable
		}

		got, err := tableName(Config{Schema: schema, Table: table})
		if strings.ContainsRune(schema, '\x00') ||
			strings.ContainsRune(effectiveTable, '\x00') {
			if !errors.Is(err, errInvalidIdentifier) {
				t.Errorf(
					"tableName(%q, %q) error = %v, want invalid identifier",
					schema, table, err,
				)
			}
			return
		}
		if err != nil {
			t.Fatalf("tableName(%q, %q): %v", schema, table, err)
		}

		wantParts := []string{effectiveTable}
		if schema != "" {
			wantParts = []string{schema, effectiveTable}
		}
		parts := scanQuotedIdentifiers(t, got)
		if len(parts) != len(wantParts) {
			t.Fatalf(
				"tableName(%q, %q) parts = %q, want %q",
				schema, table, parts, wantParts,
			)
		}
		for i := range parts {
			if parts[i] != wantParts[i] {
				t.Errorf(
					"tableName(%q, %q) part %d = %q, want %q",
					schema, table, i, parts[i], wantParts[i],
				)
			}
		}
	})
}

func scanQuotedIdentifiers(t *testing.T, rendered string) []string {
	t.Helper()
	parts := make([]string, 0, 2)
	for offset := 0; offset < len(rendered); {
		if rendered[offset] != '"' {
			t.Fatalf("identifier %q starts with %q, want double quote", rendered, rendered[offset])
		}
		offset++

		var part strings.Builder
		for {
			if offset == len(rendered) {
				t.Fatalf("identifier %q has no closing double quote", rendered)
			}
			if rendered[offset] != '"' {
				part.WriteByte(rendered[offset])
				offset++
				continue
			}
			if offset+1 < len(rendered) && rendered[offset+1] == '"' {
				part.WriteByte('"')
				offset += 2
				continue
			}
			offset++
			break
		}
		parts = append(parts, part.String())
		if offset == len(rendered) {
			break
		}
		if rendered[offset] != '.' {
			t.Fatalf(
				"identifier %q has %q outside quotes, want dot",
				rendered, rendered[offset],
			)
		}
		offset++
		if offset == len(rendered) {
			t.Fatalf("identifier %q ends after separator", rendered)
		}
	}
	return parts
}

func TestEntryPointsRejectInvalidTableName(t *testing.T) {
	// db is deliberately nil: access would panic, so a returned error proves the
	// guard runs first.
	cfg := Config{Table: "schema\x00migration"}
	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "Apply",
			run: func() error {
				return Apply(
					context.Background(), nil, nil, cfg, questionBinder{},
				)
			},
		},
		{
			name: "SchemaVersion",
			run: func() error {
				_, err := SchemaVersion(context.Background(), nil, cfg)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, errInvalidIdentifier) {
				t.Errorf(
					"%s() error = %v, want invalid identifier error",
					test.name, err,
				)
			}
		})
	}
}

func TestApplyRequiresBinder(t *testing.T) {
	// db is deliberately nil: access would panic, so a returned error proves the
	// guard runs first.
	err := Apply(context.Background(), nil, nil, Config{}, nil)
	if !errors.Is(err, errMissingBinder) {
		t.Errorf("Apply() error = %v, want missing binder error", err)
	}
}

func TestApplyRejectsEmptyMigrationSQL(t *testing.T) {
	tests := []struct {
		name          string
		version       int
		migrationName string
		sql           string
	}{
		{
			name:          "empty",
			version:       41,
			migrationName: "empty.sql",
		},
		{
			name:          "whitespace only",
			version:       42,
			migrationName: "whitespace.sql",
			sql:           " \t\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// db is deliberately nil: access would panic, so a returned error proves
			// the guard runs before the database is touched.
			err := Apply(
				context.Background(), nil,
				staticMigrationSource{{
					Version: test.version,
					Name:    test.migrationName,
					SQL:     test.sql,
				}},
				Config{}, questionBinder{},
			)
			if !errors.Is(err, errEmptyMigrationSQL) {
				t.Errorf("Apply() error = %v, want empty migration SQL error", err)
			}
			if err == nil || !strings.Contains(
				err.Error(), fmt.Sprintf("%d", test.version),
			) {
				t.Errorf("Apply() error = %v, want version %d", err, test.version)
			}
			if err == nil || !strings.Contains(err.Error(), test.migrationName) {
				t.Errorf(
					"Apply() error = %v, want name %q",
					err, test.migrationName,
				)
			}
		})
	}
}
