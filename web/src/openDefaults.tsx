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

import { createElement } from "react";

import type { Account, Group, Limit } from "@/api/types";
import {
  registerPolicy,
  registerRowAction,
  registerScope,
} from "@/framework";
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

/** Register the open product vocabulary used by pages and validation. */
export function registerOpenVocabulary(): void {
  registerScope("broker");
  registerScope("global");
  registerScope("asset");
  registerScope("account");
  registerScope("account_group");
  registerScope("account_asset");

  registerPolicy({
    id: "rate_limit",
    allowedScopes: ["broker", "asset", "account", "account_asset"],
    kinds: [{ kind: "max_orders" }, { kind: "window" }],
    catalog: {
      wikiUrl: "https://wiki.openpit.dev/Policies/?officer#ratelimitpolicy",
      fields: [{ key: "max_orders" }, { key: "window" }],
    },
  });
  registerPolicy({
    id: "order_size_limit",
    allowedScopes: ["broker", "asset", "account_asset"],
    kinds: [{ kind: "max_quantity" }, { kind: "max_notional" }],
    catalog: {
      wikiUrl:
        "https://wiki.openpit.dev/Policies/?officer#ordersizelimitpolicy",
      fields: [{ key: "max_quantity" }, { key: "max_notional" }],
    },
  });
  registerPolicy({
    id: "spot_funds_pnl_bounds_kill_switch",
    allowedScopes: ["global", "account_group", "account"],
    kinds: [
      { kind: "lower_bound" },
      { kind: "upper_bound" },
      { kind: "initial_pnl" },
    ],
    catalog: {
      wikiUrl:
        "https://wiki.openpit.dev/Spot-Funds/?officer#self-computed-pnl-kill-switch",
      fields: [
        { key: "lower_bound" },
        { key: "upper_bound" },
        { key: "initial_pnl" },
      ],
    },
  });
}

/** Register the open product row actions used by the default pages. */
export function registerOpenRowActions(): void {
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
