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

const CONNECTION_PREFIX = "connection:";

/** Selects rows whose order is linked to any trading connection. */
export const VENUE_FILTER_ANY = "any";
/** Selects rows whose order has no trading connection link. */
export const VENUE_FILTER_NONE = "none";

/** Normalizes `all` to the empty unrestricted value, preserving other values. */
export function normalizeVenueFilterValue(value: string): string {
  return value === "all" ? "" : value;
}

/** Encodes a trading connection ID as a REST venue filter value. */
export function venueConnectionFilterValue(connectionId: string): string {
  return `${CONNECTION_PREFIX}${connectionId}`;
}

/** Decodes everything after the first `connection:` prefix, if present. */
export function venueFilterConnectionId(value: string): string | undefined {
  return value.startsWith(CONNECTION_PREFIX)
    ? value.slice(CONNECTION_PREFIX.length)
    : undefined;
}
