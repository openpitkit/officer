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

import type { LimitTarget } from "./types";

/** Copy exactly the six identity axes, excluding a barrier's values. */
export function limitTarget(limit: LimitTarget): LimitTarget {
  const { policy, mode, scope, account, accountGroup, asset } = limit;
  return { policy, mode, scope, account, accountGroup, asset };
}

/** Encode the axes from limitTarget without collisions inside codes. */
export function limitTargetKey(target: LimitTarget): string {
  return JSON.stringify(limitTarget(target));
}

/** Compare barrier identity independently of its values. */
export function sameLimitTarget(a: LimitTarget, b: LimitTarget): boolean {
  return limitTargetKey(a) === limitTargetKey(b);
}
