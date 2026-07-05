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

import "fmt"

// Engine-id bounds. The engine id is the row's surrogate key, a monotonic
// autoincrement rowid that starts at one and grows. The engine runs accounts on
// a uint64 and groups on a uint32, so the usable range excludes zero (reserved
// as "unassigned") and caps each id at the width the engine constructor accepts.
const (
	// EngineAccountIDMin is the smallest assignable engine account id.
	EngineAccountIDMin uint64 = 1
	// EngineAccountIDMax is the largest engine account id that still fits a
	// signed 64-bit column.
	EngineAccountIDMax uint64 = 1<<63 - 1
	// EngineGroupIDMin is the smallest assignable engine group id.
	EngineGroupIDMin uint32 = 1
	// EngineGroupIDMax is the largest engine group id; the full uint32 range
	// above zero fits a signed 64-bit column.
	EngineGroupIDMax uint32 = 1<<32 - 1
)

// EngineAccountID is the integer id the engine runs a single account on. It is
// the account row's surrogate id, passed straight to the engine via its uint64
// account-id constructor. It is internal: it is never the public handle and
// never leaves the store on the wire. Zero means "unassigned".
type EngineAccountID uint64

// Uint64 returns the raw engine account id.
func (id EngineAccountID) Uint64() uint64 { return uint64(id) }

// EngineGroupID is the integer id the engine runs an account group on. It is the
// group row's surrogate id, passed straight to the engine via its uint32 group-id
// constructor. It is internal: never the public handle, never on the wire. Zero
// means "unassigned".
type EngineGroupID uint32

// Uint32 returns the raw engine group id.
func (id EngineGroupID) Uint32() uint32 { return uint32(id) }

// ValidateEngineAccountID returns an error wrapping ErrInvalid when id is
// outside the assignable range [1, 2^63-1].
func ValidateEngineAccountID(id EngineAccountID) error {
	v := uint64(id)
	if v < EngineAccountIDMin || v > EngineAccountIDMax {
		return fmt.Errorf(
			"engine account id %d out of range [%d, %d]: %w",
			v, EngineAccountIDMin, EngineAccountIDMax, ErrInvalid,
		)
	}
	return nil
}

// ValidateEngineGroupID returns an error wrapping ErrInvalid when id is outside
// the assignable range [1, 2^32-1].
func ValidateEngineGroupID(id EngineGroupID) error {
	v := uint32(id)
	if v < EngineGroupIDMin || v > EngineGroupIDMax {
		return fmt.Errorf(
			"engine group id %d out of range [%d, %d]: %w",
			v, EngineGroupIDMin, EngineGroupIDMax, ErrInvalid,
		)
	}
	return nil
}
