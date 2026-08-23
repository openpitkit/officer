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

import { afterEach, describe, expect, it } from "vitest";
import { Info } from "lucide-react";

import {
  getAllowedScopes,
  getNav,
  getPolicies,
  getPage,
  getPolicyKinds,
  getRoutes,
  getRowActions,
  getScopes,
  getWidgets,
  registerNav,
  registerPage,
  registerPolicy,
  registerRoute,
  registerRowAction,
  registerScope,
  registerWidget,
  unregisterNav,
  unregisterPage,
  unregisterPolicy,
  unregisterRoute,
  unregisterRowAction,
  unregisterScope,
  unregisterWidget,
} from "./";

function Throwaway() {
  return null;
}

describe("framework registries", () => {
  it("registers semantic order-size scopes", () => {
    expect(getAllowedScopes("order_size_limit")).toEqual([
      "broker",
      "underlying_asset",
      "settlement_asset",
      "account_underlying_asset",
      "account_settlement_asset",
    ]);
  });

  afterEach(() => {
    unregisterRoute("smoke-route");
    unregisterNav("smoke-nav");
    unregisterPage("smoke-page");
    unregisterWidget("smoke-widget");
    unregisterRowAction("smoke-action");
    unregisterPolicy("smoke-policy");
    unregisterScope("smoke-scope");
  });

  it("adds, replaces, and removes route entries", () => {
    registerRoute({
      id: "smoke-route",
      path: "/smoke",
      order: 1000,
      Component: Throwaway,
    });
    expect(getRoutes().find((entry) => entry.id === "smoke-route")?.path).toBe(
      "/smoke",
    );

    registerRoute({
      id: "smoke-route",
      path: "/smoke-replaced",
      order: 1000,
      Component: Throwaway,
    });
    const matches = getRoutes().filter((entry) => entry.id === "smoke-route");
    expect(matches).toHaveLength(1);
    expect(matches[0]?.path).toBe("/smoke-replaced");

    unregisterRoute("smoke-route");
    expect(getRoutes().some((entry) => entry.id === "smoke-route")).toBe(false);
  });

  it("adds, replaces, and removes nav entries", () => {
    registerNav({
      id: "smoke-nav",
      to: "/smoke",
      labelKey: "nav.dashboard",
      icon: Info,
      section: "primary",
      order: 1000,
    });
    expect(
      getNav("primary").find((entry) => entry.id === "smoke-nav")?.to,
    ).toBe("/smoke");

    registerNav({
      id: "smoke-nav",
      to: "/smoke-replaced",
      labelKey: "nav.dashboard",
      icon: Info,
      section: "primary",
      order: 1000,
    });
    const matches = getNav("primary").filter(
      (entry) => entry.id === "smoke-nav",
    );
    expect(matches).toHaveLength(1);
    expect(matches[0]?.to).toBe("/smoke-replaced");

    unregisterNav("smoke-nav");
    expect(getNav("primary").some((entry) => entry.id === "smoke-nav")).toBe(
      false,
    );
  });

  it("adds, replaces, and removes page entries", () => {
    registerPage({
      id: "smoke-page",
      titleKey: "first",
      Component: Throwaway,
    });
    expect(getPage("smoke-page")?.titleKey).toBe("first");

    registerPage({
      id: "smoke-page",
      titleKey: "second",
      Component: Throwaway,
    });
    expect(getPage("smoke-page")?.titleKey).toBe("second");

    unregisterPage("smoke-page");
    expect(getPage("smoke-page")).toBeUndefined();
  });

  it("adds, replaces, and removes widget entries", () => {
    registerWidget({ id: "smoke-widget", order: 1000, Component: Throwaway });
    expect(
      getWidgets().find((entry) => entry.id === "smoke-widget")?.order,
    ).toBe(1000);

    registerWidget({ id: "smoke-widget", order: 1001, Component: Throwaway });
    const matches = getWidgets().filter((entry) => entry.id === "smoke-widget");
    expect(matches).toHaveLength(1);
    expect(matches[0]?.order).toBe(1001);

    unregisterWidget("smoke-widget");
    expect(getWidgets().some((entry) => entry.id === "smoke-widget")).toBe(
      false,
    );
  });

  it("adds, replaces, and removes row-action entries", () => {
    registerRowAction({
      id: "smoke-action",
      kind: "smoke",
      order: 1000,
      render: () => null,
    });
    expect(getRowActions("smoke")[0]?.order).toBe(1000);

    registerRowAction({
      id: "smoke-action",
      kind: "smoke",
      order: 1001,
      render: () => null,
    });
    expect(getRowActions("smoke")).toHaveLength(1);
    expect(getRowActions("smoke")[0]?.order).toBe(1001);

    unregisterRowAction("smoke-action");
    expect(getRowActions("smoke")).toHaveLength(0);
  });

  it("adds, replaces, and removes policy entries", () => {
    registerScope("smoke-scope");
    expect(getScopes()).toContain("smoke-scope");
    registerScope("smoke-scope");
    expect(getScopes().filter((id) => id === "smoke-scope")).toHaveLength(1);
    unregisterScope("smoke-scope");
    expect(getScopes()).not.toContain("smoke-scope");

    registerPolicy({
      id: "smoke-policy",
      allowedScopes: ["broker"],
      kinds: [{ kind: "first" }],
      catalog: {
        wikiUrl: "https://example.invalid/first",
        fields: [{ key: "first" }],
      },
    });
    expect(getPolicies()).toContain("smoke-policy");
    expect(getAllowedScopes("smoke-policy")).toEqual(["broker"]);

    registerPolicy({
      id: "smoke-policy",
      allowedScopes: ["asset"],
      kinds: [{ kind: "second" }],
      catalog: {
        wikiUrl: "https://example.invalid/second",
        fields: [{ key: "second" }],
      },
    });
    expect(getPolicies().filter((id) => id === "smoke-policy")).toHaveLength(1);
    expect(getAllowedScopes("smoke-policy")).toEqual(["asset"]);
    expect(getPolicyKinds("smoke-policy")).toEqual([{ kind: "second" }]);

    unregisterPolicy("smoke-policy");
    expect(getPolicies()).not.toContain("smoke-policy");
  });
});
