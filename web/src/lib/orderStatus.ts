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

// Order statuses split by lifecycle phase. Active/working orders can still
// change; terminal orders are resolved. The Orders status filter is a set over
// these values, and an empty set means "all statuses". This single source is
// shared by the Orders screen filter and by cross-screen links that pre-filter
// Orders to the active set.

/** Working order statuses: the order can still change. */
export const ACTIVE_ORDER_STATUSES = [
  "submitted",
  "accepted",
  "partially_filled",
] as const;

/** Resolved order statuses: no further work is expected. */
export const TERMINAL_ORDER_STATUSES = [
  "committed",
  "rolled_back",
  "filled",
  "rejected",
  "cancelled",
] as const;

/** All order statuses, active first then terminal, in canonical order. */
export const ALL_ORDER_STATUSES = [
  ...ACTIVE_ORDER_STATUSES,
  ...TERMINAL_ORDER_STATUSES,
] as const;

/** One recognized order status. */
export type OrderStatusValue = (typeof ALL_ORDER_STATUSES)[number];

/** The active-orders quick-filter set, in canonical order. */
export const ACTIVE_STATUS_SET: OrderStatusValue[] = [...ACTIVE_ORDER_STATUSES];

/** The `status` query value that pre-filters Orders to the active set. */
export const ACTIVE_STATUS_QUERY = ACTIVE_ORDER_STATUSES.join(",");

/** Whether value is a recognized order status. */
export function isOrderStatus(value: string): value is OrderStatusValue {
  return (ALL_ORDER_STATUSES as readonly string[]).includes(value);
}

/** Parse a comma-separated status filter into a canonical, deduplicated set.
 *  "all" and unknown values are dropped; an empty result means no filter. */
export function parseStatusSet(raw: string | null): OrderStatusValue[] {
  if (raw === null || raw.trim() === "") {
    return [];
  }
  const seen = new Set<OrderStatusValue>();
  for (const part of raw.split(",")) {
    const trimmed = part.trim();
    if (trimmed !== "" && trimmed !== "all" && isOrderStatus(trimmed)) {
      seen.add(trimmed);
    }
  }
  return ALL_ORDER_STATUSES.filter((status) => seen.has(status));
}

/** Whether two status sets hold the same members, order-independent. */
export function sameStatusSet(
  a: readonly string[],
  b: readonly string[],
): boolean {
  if (a.length !== b.length) {
    return false;
  }
  const set = new Set(a);
  return b.every((value) => set.has(value));
}
