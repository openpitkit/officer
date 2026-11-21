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

import { validateLimit } from "@/api/validate";
import type { Limit } from "@/api/types";
import {
  RULE_MODES,
  SPOT_FUNDS_LIMIT_MODES,
} from "@/framework/registries/vocabulary";

describe("validateLimit barrier mode", () => {
  const barrier: Limit = {
    policy: "order_size_limit",
    mode: "reject",
    scope: "broker",
    account: "",
    accountGroup: "",
    asset: "",
    values: { max_quantity: "10" },
  };

  it.each(RULE_MODES)("accepts order-size mode %s", (mode) => {
    expect(validateLimit({ ...barrier, mode })).toBeNull();
  });

  it.each([undefined, null, "unknown", 1])(
    "rejects invalid order-size mode %s",
    (mode) => {
      expect(validateLimit({ ...barrier, mode } as Limit)).toEqual({
        key: "limit.ruleModeInvalid",
      });
    },
  );

  it.each([undefined, null, "unknown"])(
    "rejects missing or invalid rate mode %s",
    (mode) => {
      expect(
        validateLimit({
          ...barrier,
          policy: "rate_limit",
          mode,
          values: { window: "1s", max_orders: "1" },
        } as Limit),
      ).toEqual({ key: "limit.ruleModeInvalid" });
    },
  );
});

describe("validateLimit", () => {
  const positionLimit = (values: Record<string, string>) =>
    validateLimit({
      policy: "position_limit",
      mode: "reject",
      scope: "account_asset",
      account: "acc-1",
      accountGroup: "",
      asset: "AAPL",
      values,
    });

  it.each(["100", "0", "10.00000001", "+10", ".5", "10."])(
    "accepts the exact non-negative position ceiling %s",
    (max_position) => {
      expect(positionLimit({ max_position })).toBeNull();
    },
  );

  it.each(["-1", "-0.00000001", "-0", "-0.00"])(
    "rejects negative position ceiling %s",
    (max_position) => {
      expect(positionLimit({ max_position })).toEqual({
        key: "kind.nonNegativeDecimal",
        values: { kind: "max_position" },
      });
    },
  );

  it.each(["abc", "1e2", "1,000"])(
    "rejects non-decimal position ceiling %s",
    (max_position) => {
      expect(positionLimit({ max_position })).toEqual({
        key: "kind.nonNegativeDecimal",
        values: { kind: "max_position" },
      });
    },
  );

  it.each<Record<string, string>>([
    {},
    { max_position: "" },
    { max_position: " " },
  ])("requires the position ceiling in %j", (values) => {
    expect(positionLimit(values)).toEqual({
      key: "limit.positionRequires",
      values: { policy: "position_limit", kind: "max_position" },
    });
  });

  it("validates semantic order-size scopes and cap applicability", () => {
    const limit = (
      scope: string,
      account: string,
      asset: string,
      values: Record<string, string>,
    ) =>
      validateLimit({
        policy: "order_size_limit",
        mode: "reject",
        scope,
        account,
        accountGroup: "",
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
        mode: "reject",
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
        mode: "reject",
        scope: "global",
        account: "",
        accountGroup: "",
        asset: "",
        values: { currency: "USD", upper_bound: "500" },
      }),
    ).toBeNull();
  });

  it("rejects an empty PnL barrier currency", () => {
    expect(
      validateLimit({
        policy: "spot_funds_pnl_bounds_kill_switch",
        mode: "reject",
        scope: "global",
        account: "",
        accountGroup: "",
        asset: "",
        values: { currency: "", lower_bound: "-1000" },
      }),
    ).toEqual({
      key: "limit.pnlCurrencyRequired",
      values: { policy: "spot_funds_pnl_bounds_kill_switch" },
    });
  });

  it("requires a PnL bound when the currency is present", () => {
    expect(
      validateLimit({
        policy: "spot_funds_pnl_bounds_kill_switch",
        mode: "reject",
        scope: "global",
        account: "",
        accountGroup: "",
        asset: "",
        values: { currency: "USD" },
      }),
    ).toEqual({
      key: "limit.pnlRequires",
      values: { policy: "spot_funds_pnl_bounds_kill_switch" },
    });
  });

  it.each([
    "spot_funds_unrealized_pnl_bounds_kill_switch",
    "spot_funds_total_pnl_bounds_kill_switch",
  ])("validates the %s barrier as the realized one", (policy) => {
    expect(
      validateLimit({
        policy,
        mode: "reject",
        scope: "account",
        account: "ACC-1",
        accountGroup: "",
        asset: "",
        values: { currency: "USD", lower_bound: "-1000" },
      }),
    ).toBeNull();
    expect(
      validateLimit({
        policy,
        mode: "reject",
        scope: "global",
        account: "",
        accountGroup: "",
        asset: "",
        values: { currency: "", lower_bound: "-1000" },
      }),
    ).toEqual({ key: "limit.pnlCurrencyRequired", values: { policy } });
    expect(
      validateLimit({
        policy,
        mode: "reject",
        scope: "global",
        account: "",
        accountGroup: "",
        asset: "",
        values: { currency: "USD" },
      }),
    ).toEqual({ key: "limit.pnlRequires", values: { policy } });
  });

  it("applies asset format validation to PnL currency values", () => {
    expect(
      validateLimit({
        policy: "spot_funds_pnl_bounds_kill_switch",
        mode: "reject",
        scope: "global",
        account: "",
        accountGroup: "",
        asset: "",
        values: { currency: "US D", lower_bound: "-1000" },
      }),
    ).toEqual({ key: "asset.whitespace" });

    expect(
      validateLimit({
        policy: "spot_funds_pnl_bounds_kill_switch",
        mode: "reject",
        scope: "global",
        account: "",
        accountGroup: "",
        asset: "",
        values: { currency: "USD", lower_bound: "-1000" },
      }),
    ).toBeNull();
  });
});

describe("validateLimit for the funds limit mode", () => {
  const modeLimit = (
    overrides: Partial<Parameters<typeof validateLimit>[0]> = {},
  ) => ({
    policy: "spot_funds_limit_mode",
    mode: null,
    scope: "global",
    account: "",
    accountGroup: "",
    asset: "",
    values: { mode: "track_only" },
    ...overrides,
  });

  it.each(SPOT_FUNDS_LIMIT_MODES)("accepts the %s mode", (mode) => {
    expect(validateLimit(modeLimit({ values: { mode } }))).toBeNull();
  });

  it.each(RULE_MODES)("rejects %s as a rule mode on the global row", (mode) => {
    expect(validateLimit(modeLimit({ mode }))).toEqual({
      key: "limit.policyNoMode",
      values: { policy: "spot_funds_limit_mode" },
    });
  });

  it("requires a mode", () => {
    expect(validateLimit(modeLimit({ values: {} }))).toEqual({
      key: "limit.modeRequired",
    });
    expect(validateLimit(modeLimit({ values: { mode: "  " } }))).toEqual({
      key: "limit.modeRequired",
    });
  });

  it("rejects an unknown mode", () => {
    expect(
      validateLimit(modeLimit({ values: { mode: "always_allow" } })),
    ).toEqual({ key: "kind.mode" });
  });

  it.each(["broker", "account", "account_group", "asset"])(
    "allows only the global scope, not %s",
    (scope) => {
      expect(validateLimit(modeLimit({ scope }))).toEqual({
        key: "limit.scopeNotAllowed",
        values: { scope, policy: "spot_funds_limit_mode" },
      });
    },
  );

  it("carries no account, asset or account group", () => {
    expect(validateLimit(modeLimit({ account: "acc-1" }))).toEqual({
      key: "limit.scopeNoAccount",
      values: { scope: "global" },
    });
    expect(validateLimit(modeLimit({ asset: "USD" }))).toEqual({
      key: "limit.scopeNoAsset",
      values: { scope: "global" },
    });
    expect(validateLimit(modeLimit({ accountGroup: "desk-a" }))).toEqual({
      key: "limit.policyNoAccountGroup",
      values: { policy: "spot_funds_limit_mode" },
    });
  });
});
