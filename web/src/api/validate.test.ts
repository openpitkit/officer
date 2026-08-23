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
  it("validates semantic order-size scopes and cap applicability", () => {
    const limit = (
      scope: string,
      account: string,
      asset: string,
      values: Record<string, string>,
    ) =>
      validateLimit({
        policy: "order_size_limit",
        scope,
        account,
        asset,
        values,
      });

    expect(limit("broker", "", "", { max_quantity: "10" })).toBeNull();
    expect(limit("broker", "", "", { max_notional: "1000" })).toBeNull();
    expect(
      limit("broker", "", "", {
        max_quantity: "10",
        max_notional: "1000",
      }),
    ).toBeNull();
    expect(
      limit("underlying_asset", "", "AAPL", { max_quantity: "10" }),
    ).toBeNull();
    expect(
      limit("settlement_asset", "", "USD", { max_notional: "1000" }),
    ).toBeNull();
    expect(
      limit("account_underlying_asset", "acc-1", "AAPL", {
        max_quantity: "10",
      }),
    ).toBeNull();
    expect(
      limit("account_settlement_asset", "acc-1", "USD", {
        max_notional: "1000",
      }),
    ).toBeNull();

    expect(limit("underlying_asset", "", "AAPL", {})).toEqual({
      key: "limit.orderSizeRequires",
    });
    expect(
      limit("underlying_asset", "", "AAPL", { max_notional: "1000" }),
    ).toEqual({ key: "limit.orderSizeUnderlyingRequires" });
    expect(
      limit("underlying_asset", "", "AAPL", {
        max_quantity: "10",
        max_notional: "1000",
      }),
    ).toEqual({ key: "limit.orderSizeUnderlyingNoNotional" });
    expect(
      limit("settlement_asset", "", "USD", { max_quantity: "10" }),
    ).toEqual({ key: "limit.orderSizeSettlementRequires" });
    expect(
      limit("settlement_asset", "", "USD", {
        max_quantity: "10",
        max_notional: "1000",
      }),
    ).toEqual({ key: "limit.orderSizeSettlementNoQuantity" });
    expect(
      limit("account_underlying_asset", "acc-1", "AAPL", {
        max_notional: "1000",
      }),
    ).toEqual({ key: "limit.orderSizeUnderlyingRequires" });
    expect(
      limit("account_settlement_asset", "acc-1", "USD", {
        max_quantity: "10",
      }),
    ).toEqual({ key: "limit.orderSizeSettlementRequires" });
  });

  it("accepts self-computed account-group PnL limits", () => {
    expect(
      validateLimit({
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "account_group",
        account: "",
        accountGroup: "desk-a",
        asset: "",
        values: { currency: "USD", lower_bound: "-1000" },
      }),
    ).toBeNull();
  });

  it("requires a PnL barrier currency independently of the account", () => {
    expect(
      validateLimit({
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "global",
        account: "",
        asset: "",
        values: { currency: "USD", upper_bound: "500" },
      }),
    ).toBeNull();
  });

  it("rejects an empty PnL barrier currency", () => {
    expect(
      validateLimit({
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "global",
        account: "",
        asset: "",
        values: { currency: "", lower_bound: "-1000" },
      }),
    ).toEqual({ key: "limit.pnlCurrencyRequired" });
  });

  it("requires a PnL bound when the currency is present", () => {
    expect(
      validateLimit({
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "global",
        account: "",
        asset: "",
        values: { currency: "USD" },
      }),
    ).toEqual({ key: "limit.pnlRequires" });
  });

  it("applies asset format validation to PnL currency values", () => {
    expect(
      validateLimit({
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "global",
        account: "",
        asset: "",
        values: { currency: "US D", lower_bound: "-1000" },
      }),
    ).toEqual({ key: "asset.whitespace" });

    expect(
      validateLimit({
        policy: "spot_funds_pnl_bounds_kill_switch",
        scope: "global",
        account: "",
        asset: "",
        values: { currency: "USD", lower_bound: "-1000" },
      }),
    ).toBeNull();
  });
});
