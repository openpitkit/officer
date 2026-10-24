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

package fixture

func databaseMessages() {
	// ruleid: no-legacy-database-probe
	_ = `SELECT COUNT(*) FROM pragma_table_info('event_attestation')`
	// ruleid: no-legacy-database-probe
	_ = "database created by an earlier build"
	// ruleid: no-legacy-database-probe
	_ = "database created by an EARLIER Officer BUILD"
	// ruleid: no-legacy-database-probe
	_ = `SELECT name
FROM pragma_table_info('access_state')`
	// ruleid: no-legacy-database-probe
	_ = `PRAGMA table_info('access_state')`
	// ruleid: no-legacy-database-probe
	_ = `SELECT name FROM PrAgMa_TaBlE_XiNfO('access_state')`
	// ruleid: no-legacy-database-probe
	_ = `pragma table_xinfo('access_state')`

	// ok: no-legacy-database-probe
	_ = "database created by a different schema version must be recreated"
	// ok: no-legacy-database-probe
	_ = "PRAGMA foreign_keys = ON"
	// ok: no-legacy-database-probe
	_ = `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name = 'schema_migration')`
	// An earlier build and pragma_table_info in a comment are not literals.
	// ok: no-legacy-database-probe
	pragma_table_info := true
	_ = pragma_table_info
}
