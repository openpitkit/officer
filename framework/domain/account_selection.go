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

// Package domain holds the transport-agnostic value types shared across the
// Pit Officer control plane. These types are Pit Officer's own vocabulary; they
// are deliberately independent of the OpenPit binding's param types so that the
// store, node, and backend layers never depend on cgo or the native runtime.
package domain

import "slices"

// AccountSelection is the union of immutable accounts and current members of
// immutable groups. It is transported only on a trusted node boundary, never
// accepted as a public request's visibility. Empty is an empty set.
type AccountSelection struct {
	Accounts      []EngineAccountID
	AccountGroups []EngineGroupID
}

// Validate refuses unassigned or out-of-range identities.
func (s AccountSelection) Validate() error {
	for _, id := range s.Accounts {
		if err := ValidateEngineAccountID(id); err != nil {
			return err
		}
	}
	for _, id := range s.AccountGroups {
		if err := ValidateEngineGroupID(id); err != nil {
			return err
		}
	}
	return nil
}

// Clone returns an independent immutable snapshot.
func (s AccountSelection) Clone() *AccountSelection {
	return &AccountSelection{Accounts: slices.Clone(s.Accounts), AccountGroups: slices.Clone(s.AccountGroups)}
}

// Restricted reports whether a mandatory account restriction is active.
func (r AccountRestriction) Restricted() bool { return r.Account != "" || r.Selection != nil }
