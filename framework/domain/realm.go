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

package domain

import (
	"fmt"
	"unicode/utf8"
)

// RealmID names one isolation boundary: a self-contained dataset whose records
// never link to records in any other realm. It is the routing handle the store
// resolves to a concrete schema or database; one schema or database holds
// exactly one realm, so every uniqueness rule on business records is per realm
// automatically. The single-realm store serves one fixed realm and rejects any
// other id.
//
// It is a generic dataset namespace, deliberately neutral: it carries no notion
// of an owning organisation and must never be exposed as a per-record column.
type RealmID string

// String returns the raw realm identifier.
func (id RealmID) String() string { return string(id) }

// DefaultRealm is the realm the default composition serves.
const DefaultRealm RealmID = "default"

// Realm is the single-row identity of the dataset a store schema holds. It is
// recorded once per schema for backup labelling and future placement; it is not
// a foreign key on any business record. ExternalID is the opaque, non-guessable
// handle; Code is the immutable operator-chosen name; Title is the mutable
// display string.
type Realm struct {
	// ExternalID is the opaque machine identity of this realm row.
	ExternalID ExternalID
	// Code is the immutable, operator-chosen realm name.
	Code string
	// Title is the mutable human-readable display name; may be empty.
	Title string
}

// ValidateRealmID returns an error wrapping ErrInvalid when id is not a
// well-formed realm identifier: non-empty and at most 64 code points.
func ValidateRealmID(id RealmID) error {
	s := string(id)
	if s == "" {
		return fmt.Errorf("realm id is empty: %w", ErrInvalid)
	}
	if utf8.RuneCountInString(s) > 64 {
		return fmt.Errorf("realm id exceeds 64 code points: %w", ErrInvalid)
	}
	return nil
}
