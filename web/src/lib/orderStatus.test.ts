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

import { describe, expect, it } from "vitest";

import {
  ACTIVE_ORDER_STATUSES,
  ACTIVE_STATUS_QUERY,
  ALL_ORDER_STATUSES,
  parseStatusSet,
  sameStatusSet,
  TERMINAL_ORDER_STATUSES,
} from "@/lib/orderStatus";

describe("orderStatus groups", () => {
  it("partitions all statuses into active and terminal without overlap", () => {
    const active = new Set(ACTIVE_ORDER_STATUSES);
    const terminal = new Set<string>(TERMINAL_ORDER_STATUSES);
    for (const status of ACTIVE_ORDER_STATUSES) {
      expect(terminal.has(status)).toBe(false);
    }
    expect(ALL_ORDER_STATUSES).toHaveLength(active.size + terminal.size);
  });

  it("exposes the active query as a stable comma-separated value", () => {
    expect(ACTIVE_STATUS_QUERY).toBe(
      "submitted,accepted,committed,partially_filled",
    );
  });
});

describe("parseStatusSet", () => {
  it("returns an empty set for null, blank, or all", () => {
    expect(parseStatusSet(null)).toEqual([]);
    expect(parseStatusSet("")).toEqual([]);
    expect(parseStatusSet("   ")).toEqual([]);
    expect(parseStatusSet("all")).toEqual([]);
  });

  it("keeps known statuses in canonical order and drops unknowns", () => {
    expect(parseStatusSet("filled,submitted,bogus")).toEqual([
      "submitted",
      "filled",
    ]);
  });

  it("deduplicates and trims whitespace around values", () => {
    expect(parseStatusSet(" accepted , accepted ,submitted")).toEqual([
      "submitted",
      "accepted",
    ]);
  });
});

describe("sameStatusSet", () => {
  it("is order-independent and length-sensitive", () => {
    expect(sameStatusSet(["a", "b"], ["b", "a"])).toBe(true);
    expect(sameStatusSet(["a"], ["a", "b"])).toBe(false);
    expect(sameStatusSet([], [])).toBe(true);
  });
});
