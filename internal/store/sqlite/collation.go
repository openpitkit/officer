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

package sqlite

import (
	"strings"

	sqlite "modernc.org/sqlite"

	fwstore "go.openpit.dev/officer/framework/store"
)

// decimalCollationName is the SQLite collation that {{DECIMAL}} columns carry.
const decimalCollationName = "DECIMAL"

// init registers the DECIMAL collation on the driver so every connection opened
// afterwards (the connector opens its single pooled connection later) compares
// {{DECIMAL}} columns numerically. modernc.org/sqlite makes a registered
// collation available to all new connections; registering once at package init
// is therefore sufficient. A duplicate registration of the same name is the only
// expected failure and is harmless (the driver surfaces it as a plain error
// matched by text); any other failure is a programming error surfaced loudly.
func init() {
	if err := sqlite.RegisterCollationUtf8(
		decimalCollationName, fwstore.CompareDecimalStrings,
	); err != nil && !strings.Contains(err.Error(), "already registered") {
		panic(err)
	}
}
