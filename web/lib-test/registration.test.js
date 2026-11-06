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
import { describe, expect, it } from "vitest";

import * as source from "../src/framework";
import { registerOfficerDefaults } from "../src/register";

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
  it("resolves the package exports to the built library", () => {
    expect(officer.getRoutes).not.toBe(source.getRoutes);
    expect(officer.registerOfficerDefaults).toBeTypeOf("function");
  });

  it("registers nothing on import", () => {
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

  it("registers the same entries as the open panel", () => {
    officer.registerOfficerDefaults();
    registerOfficerDefaults();

    // Inspect predicates without running them: account scope and panel session
    // are deliberately not loaded in this consumer.
    expect(inventory(officer)).toEqual(inventory(source));
  });
});
