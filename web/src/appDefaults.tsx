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

import { createElement } from "react";

import type { SpotFundsPnlPolicy } from "@/api/spotFundsPnlPolicies";
import type { Account, Group, Limit } from "@/api/types";
import { registerPolicy, registerRowAction, registerScope } from "@/framework";
import { siteLink } from "@/lib/siteLinks";
import type { OfficerDefaultPageId } from "@/register";
import {
  AccountAuditAction,
  AccountDeleteAction,
  AccountPoliciesAction,
  AccountPositionsAction,
  AccountTradingAction,
  GroupDeleteAction,
  LimitDeleteAction,
  LimitEditAction,
  type AccountRowActionContext,
  type GroupRowActionContext,
  type LimitRowActionContext,
} from "@/pages/rowActions";

/** Register the built-in vocabulary used by pages and validation. */
export function registerAppVocabulary(): void {
  registerScope("broker");
  registerScope("global");
  registerScope("asset");
  registerScope("account");
  registerScope("account_group");
  registerScope("account_asset");
  registerScope("underlying_asset");
  registerScope("settlement_asset");
  registerScope("account_underlying_asset");
  registerScope("account_settlement_asset");

  registerPolicy({
    id: "rate_limit",
    allowedScopes: ["broker", "asset", "account", "account_asset"],
    kinds: [{ kind: "max_orders" }, { kind: "window" }],
    catalog: {
      wikiUrl: siteLink("wiki", "/Policies/", "ratelimitpolicy"),
      fields: [{ key: "max_orders" }, { key: "window" }],
    },
  });
  registerPolicy({
    id: "order_size_limit",
    allowedScopes: [
      "broker",
      "underlying_asset",
      "settlement_asset",
      "account_underlying_asset",
      "account_settlement_asset",
    ],
    kinds: [{ kind: "max_quantity" }, { kind: "max_notional" }],
    catalog: {
      wikiUrl: siteLink("wiki", "/Policies/", "ordersizelimitpolicy"),
      fields: [{ key: "max_quantity" }, { key: "max_notional" }],
    },
  });
  for (const [id, anchor] of [
    ["spot_funds_pnl_bounds_kill_switch", "self-computed-pnl-kill-switch"],
    [
      "spot_funds_unrealized_pnl_bounds_kill_switch",
      "unrealized-and-total-pnl-bounds",
    ],
    [
      "spot_funds_total_pnl_bounds_kill_switch",
      "unrealized-and-total-pnl-bounds",
    ],
  ] satisfies [SpotFundsPnlPolicy, string][]) {
    registerPolicy({
      id,
      allowedScopes: ["global", "account_group", "account"],
      kinds: [
        { kind: "currency" },
        { kind: "lower_bound" },
        { kind: "upper_bound" },
      ],
      catalog: {
        wikiUrl: siteLink("wiki", "/Spot-Funds/", anchor),
        fields: [
          { key: "currency" },
          { key: "lower_bound" },
          { key: "upper_bound" },
        ],
      },
    });
  }
  registerPolicy({
    id: "spot_funds_limit_mode",
    allowedScopes: ["global"],
    kinds: [{ kind: "mode" }],
    catalog: {
      wikiUrl: siteLink("wiki", "/Spot-Funds/", "funds-limit-mode"),
      fields: [{ key: "mode" }],
    },
  });
}

/** Register the built-in row actions used by the default pages. */
export function registerAppRowActions(
  pages: readonly OfficerDefaultPageId[],
): void {
  if (pages.includes("accounts")) {
    registerRowAction<Account, AccountRowActionContext>({
      id: "account-positions",
      kind: "account",
      order: 10,
      render: (account, ctx) =>
        createElement(AccountPositionsAction, { account, ctx }),
    });
    registerRowAction<Account, AccountRowActionContext>({
      id: "account-trading",
      kind: "account",
      order: 20,
      render: (account, ctx) =>
        createElement(AccountTradingAction, { account, ctx }),
    });
    registerRowAction<Account, AccountRowActionContext>({
      id: "account-policies",
      kind: "account",
      order: 30,
      render: (account, ctx) =>
        createElement(AccountPoliciesAction, { account, ctx }),
    });
    registerRowAction<Account, AccountRowActionContext>({
      id: "account-audit",
      kind: "account",
      order: 40,
      render: (account, ctx) =>
        createElement(AccountAuditAction, { account, ctx }),
    });
    registerRowAction<Account, AccountRowActionContext>({
      id: "account-delete",
      kind: "account",
      order: 70,
      render: (account, ctx) =>
        createElement(AccountDeleteAction, { account, ctx }),
    });
    registerRowAction<Group, GroupRowActionContext>({
      id: "group-delete",
      kind: "group",
      order: 30,
      render: (group, ctx) => createElement(GroupDeleteAction, { group, ctx }),
    });
  }
  if (pages.includes("policies")) {
    registerRowAction<Limit, LimitRowActionContext>({
      id: "limit-edit",
      kind: "limit",
      order: 10,
      render: (limit, ctx) => createElement(LimitEditAction, { limit, ctx }),
    });
    registerRowAction<Limit, LimitRowActionContext>({
      id: "limit-delete",
      kind: "limit",
      order: 20,
      render: (limit, ctx) => createElement(LimitDeleteAction, { limit, ctx }),
    });
  }
}
