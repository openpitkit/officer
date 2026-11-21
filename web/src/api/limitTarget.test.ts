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

import { describe, expect, it } from "vitest";

import { limitTarget, limitTargetKey, sameLimitTarget } from "./limitTarget";
import type { Limit, LimitTarget } from "./types";

const target: LimitTarget = {
  policy: "order_size_limit",
  mode: "reject",
  scope: "account_underlying_asset",
  account: "desk-alpha",
  accountGroup: "",
  asset: "AAPL",
};

describe("limit target identity", () => {
  it.each<[keyof LimitTarget, string | null]>([
    ["policy", "rate_limit"],
    ["mode", "review_without_hold"],
    ["scope", "account_settlement_asset"],
    ["account", "desk-beta"],
    ["accountGroup", "group-alpha"],
    ["asset", "MSFT"],
  ])("distinguishes the %s axis", (axis, value) => {
    const other = { ...target, [axis]: value };
    expect(limitTargetKey(other)).not.toBe(limitTargetKey(target));
    expect(sameLimitTarget(target, other)).toBe(false);
  });

  it("gives equal targets the same key and comparison", () => {
    const other = { ...target };
    expect(limitTargetKey(other)).toBe(limitTargetKey(target));
    expect(sameLimitTarget(target, other)).toBe(true);
  });

  it("drops values when copying a barrier target", () => {
    const limit: Limit = { ...target, values: { max_quantity: "10" } };
    expect(limitTarget(limit)).toEqual(target);
    expect(limitTarget(limit)).not.toHaveProperty("values");
  });

  it("cannot collide when codes contain a join separator", () => {
    const left = { ...target, account: "desk|group", accountGroup: "alpha" };
    const right = { ...target, account: "desk", accountGroup: "group|alpha" };
    expect(limitTargetKey(left)).not.toBe(limitTargetKey(right));
    expect(sameLimitTarget(left, right)).toBe(false);
  });
});
