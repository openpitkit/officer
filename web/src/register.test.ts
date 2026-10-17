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

import { getNav, getPage, getRoutes, getWidgets } from "@/framework";
import { setAccountScope } from "@/lib/accountScope";
import { Account } from "@/pages/Account";
import { Accounts } from "@/pages/Accounts";
// Side-effect import: the application's own registration.
import "@/register";

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
