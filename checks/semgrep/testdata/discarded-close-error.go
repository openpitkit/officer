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

import (
	"database/sql"
	"errors"
	"io"
)

func discardedClose(f io.Closer) {
	// ruleid: discarded-close-error
	_ = f.Close()
}

func deferredDiscardedClose(rows *sql.Rows) {
	// ruleid: discarded-close-error
	defer func() { _ = rows.Close() }()
}

func joinedClose(f io.Closer, cause error) error {
	// ok: discarded-close-error
	return errors.Join(cause, f.Close())
}

func returnedClose(f io.Closer) error {
	// ok: discarded-close-error
	return f.Close()
}

func deferredJoinedClose(rows *sql.Rows) (err error) {
	// ok: discarded-close-error
	defer func() { err = errors.Join(err, rows.Close()) }()
	return nil
}
