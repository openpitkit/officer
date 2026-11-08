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
import React from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  delete document.documentElement.dataset.density;
  delete document.documentElement.dataset.tradeStyle;
});

it("renders the built asset page with the public display provider", async () => {
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener() {},
    removeEventListener() {},
  }));
  await officer.initOfficerI18n();
  officer.registerOfficerDefaults();
  officer.setAccountScope({ accounts: true, account: "" });
  officer.setPanelSession("token");
  const assets = officer.getRoutes().find(({ id }) => id === "assets");
  expect(assets?.Component).toBeTypeOf("function");
  const fetch = vi.fn(async () =>
    Response.json({ error: { code: "not_implemented" } }, { status: 501 }),
  );

  const view = render(
    React.createElement(
      MemoryRouter,
      { initialEntries: ["/assets"] },
      React.createElement(
        officer.ThemeProvider,
        null,
        React.createElement(
          officer.DisplayPreferencesProvider,
          {
            defaultDensity: "terminal",
            defaultTradeStyle: "ladder",
            densityStorageKey: "host-test-density",
            tradeStyleStorageKey: "host-test-trade-style",
          },
          React.createElement(
            officer.SidebarProvider,
            null,
            React.createElement(
              officer.ApiClientProvider,
              { config: { baseUrl: "/host/api", fetch } },
              React.createElement(
                officer.AuthProvider,
                null,
                React.createElement(assets.Component),
              ),
            ),
          ),
        ),
      ),
    ),
  );

  expect(await view.findByRole("heading", { name: "Assets" })).toBeTruthy();
  expect(document.documentElement.dataset.density).toBe("terminal");
  expect(document.documentElement.dataset.tradeStyle).toBe("ladder");
  await view.findAllByText("The requested operation is not implemented.");
  expect(fetch).toHaveBeenCalled();
});
