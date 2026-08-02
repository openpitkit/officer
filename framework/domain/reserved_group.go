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

// ErrReservedGroup marks an operation refused because it targets the reserved
// default account group. That group is the terminal tier of the currency
// cascade every account resolves through rather than an operator-owned group,
// so it can never be renamed, deleted, blocked, or unblocked - the engine
// refuses the same operations on its own reserved group.
//
// It wraps ErrForbidden: the request is well-formed and the resource exists,
// but the operation is categorically forbidden for this resource in every
// state, so a surface without a dedicated mapping still answers 403 instead of
// a generic failure.
var ErrReservedGroup = fmt.Errorf(
	"the default account group is reserved: %w", ErrForbidden,
)
