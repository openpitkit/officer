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

import * as officer from "@openpit/officer-web";
import { cleanup, render } from "@testing-library/react";
import i18next from "i18next";
import React from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import * as source from "../src/index";

const sections = ["primary", "footer"];
const rowActionScopes = ["account", "group", "limit"];

function inventory(registry) {
  const routes = registry.getRoutes();
  return {
    routes: routes.map(({ id, path, order, redirectTo, Component }) => ({
      id,
      path,
      order,
      redirectTo,
      hasComponent: Component !== undefined,
    })),
    navigation: sections.map((section) =>
      registry
        .getNav(section)
        .map(({ id, to, labelKey, section, order, end, when, render }) => ({
          id,
          to,
          labelKey,
          section,
          order,
          end,
          hasWhen: when !== undefined,
          hasRender: render !== undefined,
        })),
    ),
    widgets: registry.getWidgets().map(({ id, order }) => ({ id, order })),
    pages: routes.map(({ id }) => {
      const page = registry.getPage(id);
      return {
        id,
        page: page && {
          id: page.id,
          titleKey: page.titleKey,
          hasComponent: page.Component !== undefined,
          hasWhen: page.when !== undefined,
        },
      };
    }),
    rowActions: rowActionScopes.map((scope) => ({
      scope,
      ids: registry.getRowActions(scope).map(({ id }) => id),
    })),
  };
}

describe("published officer defaults", () => {
  afterEach(() => {
    cleanup();
  });

  it("resolves the package exports to the built library", () => {
    expect(officer.getRoutes).not.toBe(source.getRoutes);
    expect(officer.registerOfficerDefaults).toBeTypeOf("function");
  });

  it("initializes and registers nothing on import", () => {
    expect(i18next.isInitialized, "i18next before explicit init").toBeFalsy();
    expect(i18next.modules.languageDetector).toBeUndefined();
    expect(localStorage.getItem("pit-officer-lang")).toBeNull();
    expect(() =>
      officer.registerLocaleResources("en", "host", { title: "Host" }),
    ).toThrowError(
      "i18next is not initialized; call initOfficerI18n() or the host's own i18next init() first",
    );

    for (const registry of [officer, source]) {
      expect(registry.getRoutes(), "routes before registration").toEqual([]);
      expect(
        registry.getPage("accounts"),
        "accounts page override before registration",
      ).toBeUndefined();
      expect(registry.getScopes(), "scopes before registration").toEqual([]);
      expect(registry.getPolicies(), "policies before registration").toEqual(
        [],
      );
      for (const section of sections) {
        expect(
          registry.getNav(section),
          `${section} navigation before registration`,
        ).toEqual([]);
      }
      expect(registry.getWidgets(), "widgets before registration").toEqual([]);
      for (const scope of rowActionScopes) {
        expect(
          registry.getRowActions(scope),
          `${scope} row actions before registration`,
        ).toEqual([]);
      }
    }
  });

  it("explicitly initializes the shared i18next with the panel detector", async () => {
    await officer.initOfficerI18n();

    expect(i18next.isInitialized).toBe(true);
    expect(i18next.modules.languageDetector).toBeDefined();
    expect(i18next.t("common:nav.accounts", { lng: "en" })).toBe("Accounts");
    expect(localStorage.getItem("pit-officer-lang")).toBe(i18next.language);
  });

  it("registers the same entries as the open panel", () => {
    officer.registerOfficerDefaults();
    source.registerOfficerDefaults();

    expect(inventory(officer)).toEqual(inventory(source));
  });

  it("uses the account scope and session provided through the package", () => {
    expect(officer.setAccountScope).toBeTypeOf("function");
    expect(officer.setPanelSession).toBeTypeOf("function");
    const accounts = officer
      .getNav("primary")
      .find(({ id }) => id === "accounts");
    const account = officer
      .getNav("primary")
      .find(({ id }) => id === "account");
    const logout = officer.getNav("footer").find(({ id }) => id === "logout");

    officer.setAccountScope({ accounts: false, account: "primary" });
    officer.setPanelSession("token");
    expect(accounts.when()).toBe(false);
    expect(account.when()).toBe(true);
    expect(logout.when()).toBe(true);

    officer.setAccountScope({ accounts: true, account: "" });
    officer.setPanelSession("open");
    expect(accounts.when()).toBe(true);
    expect(account.when()).toBe(false);
    expect(logout.when()).toBe(false);
  });

  it("loads the account scope and session through the exported StartupGate", async () => {
    expect(officer.StartupGate).toBeTypeOf("function");
    officer.setAccountScope({ accounts: true, account: "" });
    officer.setPanelSession("open");
    const fetch = vi.fn(async (url) => {
      if (url === "/host/api/session") {
        return Response.json({
          accessMode: "token",
          setupComplete: true,
          authenticated: true,
        });
      }
      if (url === "/host/api/account-mode") {
        return Response.json({
          mode: "single",
          account: "loaded",
          features: { accounts: false, account: "loaded" },
          restartRequired: false,
          live: false,
        });
      }
      throw new Error(`unexpected request: ${url}`);
    });
    const view = render(
      React.createElement(
        officer.ApiClientProvider,
        {
          config: {
            baseUrl: "/host/api",
            fetch,
            onUnauthorized: officer.checkSession,
          },
        },
        React.createElement(
          officer.StartupGate,
          null,
          React.createElement("div", null, "Host content"),
        ),
      ),
    );

    await view.findByText("Host content");
    expect(fetch).toHaveBeenCalledWith("/host/api/session", expect.any(Object));
    expect(fetch).toHaveBeenCalledWith(
      "/host/api/account-mode",
      expect.any(Object),
    );
    expect(
      officer
        .getNav("primary")
        .find(({ id }) => id === "accounts")
        .when(),
    ).toBe(false);
    expect(
      officer
        .getNav("primary")
        .find(({ id }) => id === "account")
        .when(),
    ).toBe(true);
    expect(
      officer
        .getNav("footer")
        .find(({ id }) => id === "logout")
        .when(),
    ).toBe(true);
  });
});
