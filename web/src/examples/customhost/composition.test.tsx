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

import { render, screen } from "@testing-library/react";
import i18n from "i18next";
import { afterEach, describe, expect, it } from "vitest";

import {
  getNav,
  getPage,
  getPolicies,
  getRowActions,
  getRoutes,
  getScopes,
  getWidgets,
  RegistryRowActions,
} from "@openpit/officer-web";

import { CustomHostAuthProvider } from "./components";
import {
  hiddenActionID,
  hostActionID,
  hostNavID,
  hostPageID,
  hostPolicyID,
  hostScopeID,
  hostWidgetID,
  removedRouteID,
} from "./ids";
import {
  registerCustomHostComposition,
  unregisterCustomHostComposition,
} from "./composition";

describe("custom host web composition", () => {
  afterEach(() => {
    unregisterCustomHostComposition();
  });

  it("adds host route, nav, action, widget, and vocabulary entries", () => {
    registerCustomHostComposition();

    expect(getRoutes().some((entry) => entry.id === hostPageID)).toBe(true);
    expect(getNav("primary").some((entry) => entry.id === hostNavID)).toBe(
      true,
    );
    expect(getWidgets().some((entry) => entry.id === hostWidgetID)).toBe(true);
    expect(getScopes()).toContain(hostScopeID);
    expect(getPolicies()).toContain(hostPolicyID);
    expect(i18n.t("customhost:page.title")).toBe("Host reference page");
  });

  it("replaces entries by re-registering the same stable id", () => {
    registerCustomHostComposition();

    const Page = getPage(hostPageID)?.Component;
    const Widget = getWidgets().find(
      (entry) => entry.id === hostWidgetID,
    )?.Component;

    expect(Page).toBeDefined();
    expect(Widget).toBeDefined();
    render(
      <>
        {Page ? <Page /> : null}
        {Widget ? <Widget /> : null}
      </>,
    );

    expect(
      screen.getByLabelText("customhost-replacement-page"),
    ).toHaveTextContent("Replacement host reference page");
    expect(
      screen.getByLabelText("customhost-replacement-widget"),
    ).toHaveTextContent("Replacement host reference widget");
  });

  it("hides entries through the auth predicate without removing them", () => {
    registerCustomHostComposition();

    render(
      <CustomHostAuthProvider>
        <RegistryRowActions kind="customhost" row={{}} ctx={{}} />
      </CustomHostAuthProvider>,
    );

    expect(screen.getByLabelText("customhost-action")).not.toBeNull();
    expect(screen.queryByLabelText("customhost-hidden")).toBeNull();
    expect(
      getRoutes().some((entry) => entry.permission === "customhost.host"),
    ).toBe(true);
  });

  it("removes entries through unregister APIs", () => {
    registerCustomHostComposition();

    expect(getRoutes().some((entry) => entry.id === removedRouteID)).toBe(
      false,
    );
    unregisterCustomHostComposition();
    expect(getRoutes().some((entry) => entry.id === hostPageID)).toBe(false);
    expect(getNav("primary").some((entry) => entry.id === hostNavID)).toBe(
      false,
    );
    expect(getPage(hostPageID)).toBeUndefined();
    expect(getWidgets().some((entry) => entry.id === hostWidgetID)).toBe(false);
  });

  it("keeps hidden and host actions structurally registered", () => {
    registerCustomHostComposition();

    render(
      <CustomHostAuthProvider>
        <RegistryRowActions kind="customhost" row={{}} ctx={{}} />
      </CustomHostAuthProvider>,
    );

    expect(screen.queryByLabelText("customhost-hidden")).toBeNull();
    expect(screen.getByLabelText("customhost-action")).not.toBeNull();
    expect(getRowActions("customhost").map((entry) => entry.id)).toEqual([
      hostActionID,
      hiddenActionID,
    ]);
  });
});
