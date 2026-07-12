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

// Package schema provides the canonical Officer store schema shared by every
// database connector.
package schema

import "strings"

// Dialect renders the few backend-specific declarations in the canonical
// schema. External connectors implement this interface and use
// NewEmbeddedMigrationSource to apply the same schema without importing another
// connector package.
type Dialect interface {
	// PrimaryKey renders the {{PK}} token for an internal surrogate-key column.
	PrimaryKey() string
	// ExternalID renders the {{XID}} token for an external-id column.
	ExternalID() string
	// Bool renders the {{BOOL}} token for a boolean column.
	Bool() string
	// Decimal renders the {{DECIMAL}} token for an exact decimal column.
	Decimal() string
}

const (
	tokenPrimaryKey = "{{PK}}"
	tokenExternalID = "{{XID}}"
	tokenBool       = "{{BOOL}}"
	tokenDecimal    = "{{DECIMAL}}"
)

// Render substitutes the dialect tokens in the canonical schema text.
func Render(d Dialect, text string) string {
	return strings.NewReplacer(
		tokenPrimaryKey, d.PrimaryKey(),
		tokenExternalID, d.ExternalID(),
		tokenBool, d.Bool(),
		tokenDecimal, d.Decimal(),
	).Replace(text)
}
