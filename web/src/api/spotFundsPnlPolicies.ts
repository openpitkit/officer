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

import type { AccountLimits } from "./types";

/** The Spot Funds P&L-bounds policies, one per P&L axis: realized, unrealized
 *  and total. They share one barrier shape and differ only in what they bound. */
export type SpotFundsPnlPolicy =
  | "spot_funds_pnl_bounds_kill_switch"
  | "spot_funds_unrealized_pnl_bounds_kill_switch"
  | "spot_funds_total_pnl_bounds_kill_switch";

/** The wire names of one Spot Funds P&L-bounds policy. */
export interface SpotFundsPnlPolicyWire {
  /** Route of its PUT under /limits. */
  path: string;
  /** Key of the stored barrier in the answer of that PUT. */
  putKey: string;
  /** Key of its barriers in the per-policy limits view. */
  listKey: Exclude<keyof AccountLimits, "rateLimits" | "orderSizeLimits">;
}

/** Every Spot Funds P&L-bounds policy with its wire names, realized first. */
export const SPOT_FUNDS_PNL_POLICIES: Record<
  SpotFundsPnlPolicy,
  SpotFundsPnlPolicyWire
> = {
  spot_funds_pnl_bounds_kill_switch: {
    path: "spot-funds-pnl-bounds",
    putKey: "spotFundsPnlBoundsLimit",
    listKey: "spotFundsPnlBoundsLimits",
  },
  spot_funds_unrealized_pnl_bounds_kill_switch: {
    path: "spot-funds-unrealized-pnl-bounds",
    putKey: "spotFundsUnrealizedPnlBoundsLimit",
    listKey: "spotFundsUnrealizedPnlBoundsLimits",
  },
  spot_funds_total_pnl_bounds_kill_switch: {
    path: "spot-funds-total-pnl-bounds",
    putKey: "spotFundsTotalPnlBoundsLimit",
    listKey: "spotFundsTotalPnlBoundsLimits",
  },
};

/** The Spot Funds P&L-bounds policy ids, realized first. */
export const SPOT_FUNDS_PNL_POLICY_IDS = Object.keys(
  SPOT_FUNDS_PNL_POLICIES,
) as SpotFundsPnlPolicy[];

/** Whether policy is one of the Spot Funds P&L-bounds policies. */
export function isSpotFundsPnlPolicy(
  policy: string,
): policy is SpotFundsPnlPolicy {
  return Object.hasOwn(SPOT_FUNDS_PNL_POLICIES, policy);
}
