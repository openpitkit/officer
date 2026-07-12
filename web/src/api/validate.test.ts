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

import { validateLimit } from "@/api/validate";

describe("validateLimit", () => {
  it("accepts self-computed account-group PnL limits", () => {
    expect(
      validateLimit({
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "account_group",
        account: "",
        accountGroup: "desk-a",
        asset: "",
        values: { lower_bound: "-1000" },
      }),
    ).toBeNull();
  });

  it("does not require account currency for self-computed PnL limits", () => {
    expect(
      validateLimit({
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "global",
        account: "",
        asset: "",
        values: { upper_bound: "500" },
      }),
    ).toBeNull();
  });

  it("allows initial_pnl only on account-scoped self-computed PnL limits", () => {
    expect(
      validateLimit({
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "account_group",
        account: "",
        accountGroup: "desk-a",
        asset: "",
        values: { lower_bound: "-1000", initial_pnl: "10" },
      }),
    ).toEqual({ key: "limit.spotFundsInitialPnlAccountOnly" });

    expect(
      validateLimit({
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "account",
        account: "acc-1",
        asset: "",
        values: { lower_bound: "-1000", initial_pnl: "10" },
      }),
    ).toBeNull();
  });
});
