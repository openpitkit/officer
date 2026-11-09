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

import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  getNav,
  getPage,
  getPolicies,
  getRoutes,
  getRowActions,
  getScopes,
  getWidgets,
  resetRegistries,
} from "@/framework";
import * as locales from "@/i18n";
import { setAccountScope } from "@/lib/accountScope";
import { Account } from "@/pages/Account";
import { Accounts } from "@/pages/Accounts";
import { registerOfficerDefaults, type OfficerDefaultPageId } from "@/register";

const pageEntries = {
  dashboard: {
    routes: ["dashboard"],
    primary: ["dashboard"],
    widgets: [
      "counts-row",
      "mcp-access-card",
      "trading-connections-card",
      "market-data-card",
      "activity-columns",
      "audit-strip",
    ],
  },
  accounts: {
    routes: ["accounts"],
    primary: ["accounts", "account"],
    pages: ["accounts"],
    actions: [
      "account-positions",
      "account-trading",
      "account-policies",
      "account-audit",
      "account-delete",
      "group-delete",
    ],
  },
  policies: {
    routes: ["policies", "limits-redirect"],
    primary: ["policies"],
    actions: ["limit-edit", "limit-delete"],
  },
  assets: { routes: ["assets"], primary: ["assets"] },
  positions: { routes: ["positions"], primary: ["positions"] },
  orders: { routes: ["orders", "trading-redirect"], primary: ["orders"] },
  "market-data": {
    routes: ["market-data"],
    footer: ["restart-required", "market-data"],
  },
  "trading-connections": {
    routes: ["trading-connections"],
    footer: ["trading-connections"],
  },
  audit: { routes: ["audit"], footer: ["audit"] },
  "mcp-access": { routes: ["mcp-access"], footer: ["mcp-access"] },
  "signing-keys": { routes: ["signing-keys"], footer: ["signing-keys"] },
  service: {
    routes: ["service"],
    footer: ["service", "sensitive-data", "non-release", "logout", "about"],
  },
} satisfies Record<OfficerDefaultPageId, Partial<Record<string, string[]>>>;

const pageIds = Object.keys(pageEntries) as OfficerDefaultPageId[];

beforeEach(() => {
  vi.restoreAllMocks();
  resetRegistries();
  setAccountScope({ accounts: true, account: "" });
  registerOfficerDefaults();
});

function registration() {
  return {
    routes: getRoutes(),
    primary: getNav("primary"),
    footer: getNav("footer"),
    pages: [getPage("accounts")],
    widgets: getWidgets(),
    actions: ["account", "group", "limit"].flatMap(getRowActions),
    scopes: getScopes(),
    policies: getPolicies(),
  };
}

function comparable(value: unknown): unknown {
  return JSON.parse(
    JSON.stringify(value, (_key, item) =>
      typeof item === "function" ? item.name : item,
    ),
  );
}

function indexOf(ids: string[], id: string): number {
  const index = ids.indexOf(id);
  expect(index, `${id} is registered`).toBeGreaterThanOrEqual(0);
  return index;
}

/** Ids of the primary menu entries the sidebar renders now. */
function shownPrimaryIds(): string[] {
  return getNav("primary")
    .filter((entry) => entry.when?.() ?? true)
    .map((entry) => entry.id);
}

function accountsRoute() {
  const route = getRoutes().find((entry) => entry.path === "/accounts");
  if (route === undefined) {
    throw new Error("/accounts is not routed");
  }
  return route;
}

describe("officer default registration", () => {
  it("preserves the complete default registration contract", () => {
    expect(registration()).toMatchSnapshot();
  });

  it.each([
    ...pageIds.map((id) => [id] as OfficerDefaultPageId[]),
    ["service", "accounts", "policies", "dashboard"] as OfficerDefaultPageId[],
    [...pageIds].reverse(),
  ])("registers only the selected pages: %j", (...selected) => {
    const complete = registration();
    resetRegistries();
    const resources = vi.spyOn(locales, "registerLocaleResourceMap");
    registerOfficerDefaults(selected);
    expect(resources).toHaveBeenCalledExactlyOnceWith(
      locales.appLocaleResources,
    );
    const actual = registration();
    for (const kind of [
      "routes",
      "primary",
      "footer",
      "pages",
      "widgets",
      "actions",
    ] as const) {
      const allowed = selected.flatMap((id) => {
        const entries: Partial<Record<typeof kind, string[]>> = pageEntries[id];
        return entries[kind] ?? [];
      });
      expect(
        comparable(actual[kind].filter((entry) => entry !== undefined)),
      ).toEqual(
        comparable(
          complete[kind].filter((entry) => entry && allowed.includes(entry.id)),
        ),
      );
    }
    expect(actual.scopes).toEqual(complete.scopes);
    expect(actual.policies).toEqual(complete.policies);
  });

  it("leaves existing registrations intact for an empty selection", () => {
    const before = registration();
    registerOfficerDefaults([]);
    expect(registration()).toEqual(before);
    resetRegistries();
    registerOfficerDefaults([]);
    expect(registration()).toEqual({
      routes: [],
      primary: [],
      footer: [],
      pages: [undefined],
      widgets: [],
      actions: [],
      scopes: [],
      policies: [],
    });
  });

  it("rejects unknown page ids before registering anything", () => {
    resetRegistries();
    const before = registration();
    expect(() =>
      registerOfficerDefaults(["accounts", "unknown" as OfficerDefaultPageId]),
    ).toThrow("Unknown Officer default page: unknown");
    expect(registration()).toEqual(before);
  });

  it("lists trading connections above market data in the menu", () => {
    const ids = getNav("footer").map((entry) => entry.id);

    expect(indexOf(ids, "trading-connections")).toBeLessThan(
      indexOf(ids, "market-data"),
    );
  });

  it("places the trading connections widget above the market data widget", () => {
    const ids = getWidgets().map((entry) => entry.id);

    expect(indexOf(ids, "trading-connections-card")).toBeLessThan(
      indexOf(ids, "market-data-card"),
    );
  });

  it("offers the Accounts entry and page while accounts are shown", () => {
    const ids = shownPrimaryIds();
    expect(ids).toContain("accounts");
    expect(ids).not.toContain("account");

    const route = accountsRoute();
    expect(route.Component).toBe(Accounts);
    expect(getPage(route.id)?.when?.()).toBe(false);
  });

  it("offers the Account entry and page in its place while accounts are hidden", () => {
    setAccountScope({ accounts: false, account: "main" });

    const ids = shownPrimaryIds();
    expect(ids).toContain("account");
    expect(ids).not.toContain("accounts");
    expect(ids[indexOf(ids, "account") - 1]).toBe("dashboard");
    expect(getNav("primary").find((entry) => entry.id === "account")?.to).toBe(
      "/accounts",
    );

    const page = getPage(accountsRoute().id);
    expect(page?.Component).toBe(Account);
    expect(page?.when?.()).toBe(true);
  });
});
