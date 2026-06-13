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

import i18n from "@/i18n";

// Locale-aware rendering helpers bound to the active UI language. Use these
// instead of raw `toLocale*` / `new Date()` so dates and counts follow the
// language the operator selected, and so the formatting locale changes live
// with the switcher.
//
// IMPORTANT: `formatNumber` is for counts and derived numbers only. API
// decimals (prices, quantities, P&L) arrive as strings to preserve precision
// and MUST NOT be re-parsed through `Number(...)` here — that would lose
// precision. Render those strings as-is (optionally grouped on the string
// side), never via this helper.

/** The active UI locale code, e.g. "en". */
function activeLocale(): string {
  return i18n.language || "en";
}

/** Format an ISO 8601 timestamp as a locale date (no time component). */
export function formatDate(iso: string): string {
  return new Intl.DateTimeFormat(activeLocale(), {
    dateStyle: "medium",
  }).format(new Date(iso));
}

/** Format an ISO 8601 timestamp as a locale time, including the timezone.
 *  Rendered in the operator's local zone, labeled so the zone is explicit. */
export function formatTime(iso: string): string {
  // Explicit component fields (not `timeStyle`): `Intl.DateTimeFormat` throws
  // if a style shorthand is combined with `timeZoneName`.
  return new Intl.DateTimeFormat(activeLocale(), {
    hour: "numeric",
    minute: "2-digit",
    second: "2-digit",
    timeZoneName: "short",
  }).format(new Date(iso));
}

/** Format an ISO 8601 timestamp as a locale date and time, including the
 *  timezone. Rendered in the operator's local zone, labeled explicitly. */
export function formatDateTime(iso: string): string {
  // Explicit component fields (not `dateStyle`/`timeStyle`):
  // `Intl.DateTimeFormat` throws if a style shorthand is combined with
  // `timeZoneName`.
  return new Intl.DateTimeFormat(activeLocale(), {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
    timeZoneName: "short",
  }).format(new Date(iso));
}

/** Format a count or derived number in the active locale. Not for API
 *  decimals — see the module note. */
export function formatNumber(
  value: number,
  opts?: Intl.NumberFormatOptions,
): string {
  return new Intl.NumberFormat(activeLocale(), opts).format(value);
}
