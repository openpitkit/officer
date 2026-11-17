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

import { isInPlacePolicy } from "./spotFundsPnlPolicies";

describe("isInPlacePolicy", () => {
  it.each<[string, boolean]>([
    ["rate_limit", false],
    ["order_size_limit", false],
    ["position_limit", true],
    ["spot_funds_pnl_bounds_kill_switch", true],
    ["spot_funds_unrealized_pnl_bounds_kill_switch", true],
    ["spot_funds_total_pnl_bounds_kill_switch", true],
    ["spot_funds_limit_mode", true],
    ["", false],
    ["unknown", false],
  ])("classifies %s as in place: %s", (policy, expected) => {
    expect(isInPlacePolicy(policy)).toBe(expected);
  });
});
