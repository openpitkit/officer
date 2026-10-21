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

import { ACCESS_TOKEN_PREFIXES, type AccessTokenKind } from "@/api/access";

/** The prefix, the body's first and last four characters, dots between. */
export function maskAccessToken(kind: AccessTokenKind, token: string): string {
  const prefix = ACCESS_TOKEN_PREFIXES[kind];
  const body = token.slice(prefix.length);
  return `${prefix}${body.slice(0, 4)}${"•".repeat(14)}${body.slice(-4)}`;
}
