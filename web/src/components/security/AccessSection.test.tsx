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

import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AccessMode } from "@/api/access";
import {
  AccessSection,
  AccessTokensProvider,
} from "@/components/security/AccessSection";
import { formatDate } from "@/i18n/format";
import { onCheckSession, setPanelSession } from "@/lib/panelSession";
import {
  errorResponse,
  jsonResponse,
  renderWithApi,
  serving,
  type Route,
} from "@/test/apiClient";

const API = "/app/api/v1";
const SET_AT = "2026-10-15T12:00:00Z";
const CHANGED_AT = "2026-10-16T12:00:00Z";
const AGENT_TOKEN = "pit_mcp_Q2x7bWv9R1t4YzK8pN3sJd6HfL0aEu5cXg2iTo7Vr4w";
const API_TOKEN = "pit_api_Lm4Ts9Qa2Xc7Vn1Bk6Hd3Wy8Pe5Rz0Gu2Jf7Os4Ni9w";
const NEW_API_TOKEN = "pit_api_Rb3Ny8Kq1Wd6Tm4Hx9Lc2Vp7Gz5Fs0Je3Ua8Ok1Mi6Q";
const DOTS = "•".repeat(14);
// Letters and hyphens only: a URL never percent-encodes them, so a password
// that reached the URL would show there as written.
const CURRENT = "the-panel-password-in-force";
const NEW_PASSWORD = "a-new-panel-password";
const REFUSAL = "password must be at least 15 characters";

function tokenRoutes(routes: Record<string, Route> = {}) {
  return {
    [`GET ${API}/access`]: () =>
      jsonResponse({ mode: "token", passwordSetAt: SET_AT, sessions: 2 }),
    [`GET ${API}/access/tokens/agent`]: () =>
      jsonResponse({ token: AGENT_TOKEN }),
    [`GET ${API}/access/tokens/api`]: () => jsonResponse({ token: API_TOKEN }),
    ...routes,
  };
}

function noContent() {
  return new Response(null, { status: 204 });
}

function problem(detail: string) {
  return new Response(
    JSON.stringify({
      type: "about:blank",
      title: "Unprocessable Entity",
      status: 422,
      detail,
      errors: [{ pointer: "/password", detail: "too short" }],
    }),
    { status: 422, headers: { "Content-Type": "application/problem+json" } },
  );
}

function renderSection(mode: AccessMode, routes: Record<string, Route>) {
  setPanelSession(mode);
  const fetchMock = serving(routes);
  const onUnauthorized = vi.fn();
  renderWithApi(
    <AccessTokensProvider>
      <AccessSection />
    </AccessTokensProvider>,
    {
      fetch: fetchMock as unknown as typeof fetch,
      config: { onUnauthorized },
    },
  );
  return { fetchMock, onUnauthorized };
}

function requests(fetchMock: ReturnType<typeof serving>) {
  return fetchMock.mock.calls.map(([url, init]) => `${init.method} ${url}`);
}

function sentBody(fetchMock: ReturnType<typeof serving>, request: string) {
  const call = fetchMock.mock.calls.find(
    ([url, init]) => `${init.method} ${url}` === request,
  );
  if (call === undefined) {
    throw new Error(`${request} was not sent`);
  }
  return JSON.parse(call[1].body as string) as unknown;
}

// Lets every request the section started reach the mock and settle.
async function settle() {
  await act(() => new Promise((resolve) => setTimeout(resolve, 0)));
}

function row(name: string) {
  return screen.getByRole("group", { name });
}

describe("AccessSection", () => {
  const reload = vi.fn();
  const check = vi.fn();
  let unsubscribe = () => {};
  // The URL hash at the moment the page reloads.
  let hashAtReload: string | null = null;

  beforeEach(() => {
    hashAtReload = null;
    reload.mockImplementation(() => {
      hashAtReload = new URL(document.URL).hash;
    });
    vi.stubGlobal("location", { ...window.location, reload });
    unsubscribe = onCheckSession(check);
  });

  afterEach(() => {
    unsubscribe();
    vi.unstubAllGlobals();
    reload.mockReset();
    check.mockReset();
    window.history.replaceState(null, "", "/");
  });

  describe("in open access", () => {
    it("holds the choice inline with no token selected and requests nothing", async () => {
      const { fetchMock } = renderSection("open", {});
      await settle();

      const access = row("Access");
      expect(
        within(access).getByRole("radio", { name: "No token" }),
      ).toBeChecked();
      expect(
        within(access).getByText(
          "Without a token, Officer is open to everything on this computer",
        ),
      ).toBeInTheDocument();
      expect(within(access).queryByRole("checkbox")).not.toBeInTheDocument();
      for (const name of [
        "Panel password",
        "Browser sessions",
        "Agent token · MCP",
        "API token · REST",
      ]) {
        expect(screen.queryByRole("group", { name })).not.toBeInTheDocument();
      }
      expect(
        screen.queryByRole("button", { name: "Turn on token access" }),
      ).not.toBeInTheDocument();
      expect(fetchMock).not.toHaveBeenCalled();
    });

    async function chooseToken(user: ReturnType<typeof userEvent.setup>) {
      await user.click(
        screen.getByRole("radio", { name: "Token and password" }),
      );
      await user.type(
        screen.getByLabelText("New panel password"),
        NEW_PASSWORD,
      );
      await user.type(
        screen.getByLabelText("Repeat the new password"),
        NEW_PASSWORD,
      );
    }

    it("turns on token access with the new password, then reloads onto the tab", async () => {
      const user = userEvent.setup();
      const { fetchMock } = renderSection("open", {
        [`PUT ${API}/access/mode`]: noContent,
      });
      const turnOn = () =>
        screen.getByRole("button", { name: "Turn on token access" });

      await user.click(
        screen.getByRole("radio", { name: "Token and password" }),
      );
      expect(turnOn()).toBeDisabled();
      await chooseToken(user);
      expect(turnOn()).toBeEnabled();
      await user.click(turnOn());

      expect(sentBody(fetchMock, `PUT ${API}/access/mode`)).toEqual({
        mode: "token",
        password: NEW_PASSWORD,
      });
      expect(reload).toHaveBeenCalledTimes(1);
      expect(hashAtReload).toBe("#security");
    });

    it("shows a refused password under the field and drops it", async () => {
      const user = userEvent.setup();
      renderSection("open", {
        [`PUT ${API}/access/mode`]: () => problem(REFUSAL),
      });

      await chooseToken(user);
      await user.click(
        screen.getByRole("button", { name: "Turn on token access" }),
      );

      const password = await screen.findByLabelText("New panel password");
      expect(password).toHaveAccessibleDescription(REFUSAL);
      expect(password).toHaveValue("");
      expect(password).toHaveFocus();
      expect(screen.getByLabelText("Repeat the new password")).toHaveValue("");
      expect(
        screen.getByRole("button", { name: "Turn on token access" }),
      ).toBeDisabled();
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(reload).not.toHaveBeenCalled();
    });

    it("shows any other failure in a banner and drops the password", async () => {
      const user = userEvent.setup();
      renderSection("open", {
        [`PUT ${API}/access/mode`]: () => errorResponse(500, "internal"),
      });

      await chooseToken(user);
      await user.click(
        screen.getByRole("button", { name: "Turn on token access" }),
      );

      expect(await screen.findByRole("alert")).toHaveTextContent("internal");
      expect(screen.getByLabelText("New panel password")).toHaveValue("");
      expect(reload).not.toHaveBeenCalled();
      expect(check).not.toHaveBeenCalled();
    });

    it("asks the startup gate to check the session when token access is on already", async () => {
      const user = userEvent.setup();
      renderSection("open", {
        [`PUT ${API}/access/mode`]: () => errorResponse(409, "conflict"),
      });

      await chooseToken(user);
      await user.click(
        screen.getByRole("button", { name: "Turn on token access" }),
      );

      await waitFor(() => expect(check).toHaveBeenCalledTimes(1));
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(reload).not.toHaveBeenCalled();
    });
  });

  describe("in token access", () => {
    it("shows the mode, the password, the sessions and both tokens masked", async () => {
      const { fetchMock } = renderSection("token", tokenRoutes());

      const agent = await screen.findByRole("group", {
        name: "Agent token · MCP",
      });
      await screen.findByRole("group", { name: "Panel password" });

      const access = row("Access");
      expect(within(access).getByText("Token")).toBeInTheDocument();
      expect(
        within(access).getByRole("button", { name: "Switch to no token…" }),
      ).toBeInTheDocument();
      expect(screen.queryByRole("radiogroup")).not.toBeInTheDocument();
      const password = row("Panel password");
      expect(
        within(password).getByText(`Set ${formatDate(SET_AT)}`),
      ).toBeInTheDocument();
      expect(
        within(password).getByRole("button", { name: "Change…" }),
      ).toBeInTheDocument();
      const sessions = row("Browser sessions");
      expect(
        within(sessions).getByText("Logged in from 2 browsers"),
      ).toBeInTheDocument();
      expect(
        within(sessions).getByRole("button", { name: "Log out everywhere" }),
      ).toBeInTheDocument();
      expect(
        within(agent).getByText(`pit_mcp_Q2x7${DOTS}Vr4w`),
      ).toBeInTheDocument();
      expect(
        within(row("API token · REST")).getByText(`pit_api_Lm4T${DOTS}Ni9w`),
      ).toBeInTheDocument();
      expect(document.body.innerHTML).not.toContain(AGENT_TOKEN);
      expect(document.body.innerHTML).not.toContain(API_TOKEN);
      expect(requests(fetchMock).sort()).toEqual([
        `GET ${API}/access`,
        `GET ${API}/access/tokens/agent`,
        `GET ${API}/access/tokens/api`,
      ]);
    });

    it("counts one browser in the singular", async () => {
      renderSection(
        "token",
        tokenRoutes({
          [`GET ${API}/access`]: () =>
            jsonResponse({ mode: "token", passwordSetAt: SET_AT, sessions: 1 }),
        }),
      );

      expect(
        await screen.findByText("Logged in from 1 browser"),
      ).toBeInTheDocument();
    });

    it("copies a token fetched once, without asking for it again", async () => {
      const user = userEvent.setup();
      const { fetchMock } = renderSection("token", tokenRoutes());
      const agent = await screen.findByRole("group", {
        name: "Agent token · MCP",
      });

      await user.click(within(agent).getByRole("button", { name: "Show" }));
      await user.click(within(agent).getByRole("button", { name: "Copy" }));

      expect(await navigator.clipboard.readText()).toBe(AGENT_TOKEN);
      expect(within(agent).getByText(AGENT_TOKEN)).toBeInTheDocument();
      expect(
        requests(fetchMock).filter(
          (request) => request === `GET ${API}/access/tokens/agent`,
        ),
      ).toHaveLength(1);
    });

    it("replaces a token with its regenerated value", async () => {
      const user = userEvent.setup();
      const { fetchMock } = renderSection(
        "token",
        tokenRoutes({
          [`POST ${API}/access/tokens/api/regenerate`]: () =>
            jsonResponse({ token: NEW_API_TOKEN }),
        }),
      );
      const apiRow = await screen.findByRole("group", {
        name: "API token · REST",
      });

      await user.click(
        within(apiRow).getByRole("button", { name: "Regenerate" }),
      );
      await user.click(
        within(screen.getByRole("alertdialog")).getByRole("button", {
          name: "Regenerate",
        }),
      );

      expect(
        await within(apiRow).findByText(`pit_api_Rb3N${DOTS}Mi6Q`),
      ).toBeInTheDocument();
      expect(
        within(row("Agent token · MCP")).getByText(`pit_mcp_Q2x7${DOTS}Vr4w`),
      ).toBeInTheDocument();
      expect(requests(fetchMock)).toContain(
        `POST ${API}/access/tokens/api/regenerate`,
      );
    });

    it("asks the startup gate to check the session when Regenerate answers 409", async () => {
      const user = userEvent.setup();
      renderSection(
        "token",
        tokenRoutes({
          [`POST ${API}/access/tokens/agent/regenerate`]: () =>
            errorResponse(409, "conflict"),
        }),
      );
      const agent = await screen.findByRole("group", {
        name: "Agent token · MCP",
      });

      await user.click(
        within(agent).getByRole("button", { name: "Regenerate" }),
      );
      await user.click(
        within(screen.getByRole("alertdialog")).getByRole("button", {
          name: "Regenerate",
        }),
      );

      await waitFor(() => expect(check).toHaveBeenCalledTimes(1));
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("logs out everywhere, then asks the startup gate to check the session", async () => {
      const user = userEvent.setup();
      const { fetchMock } = renderSection(
        "token",
        tokenRoutes({ [`DELETE ${API}/sessions`]: noContent }),
      );
      const sessions = await screen.findByRole("group", {
        name: "Browser sessions",
      });

      await user.click(
        within(sessions).getByRole("button", { name: "Log out everywhere" }),
      );

      expect(requests(fetchMock)).toContain(`DELETE ${API}/sessions`);
      expect(check).toHaveBeenCalledTimes(1);
    });

    it("keeps the session when logging out everywhere fails", async () => {
      const user = userEvent.setup();
      renderSection(
        "token",
        tokenRoutes({
          [`DELETE ${API}/sessions`]: () => errorResponse(500, "internal"),
        }),
      );
      const sessions = await screen.findByRole("group", {
        name: "Browser sessions",
      });

      await user.click(
        within(sessions).getByRole("button", { name: "Log out everywhere" }),
      );

      expect(await within(sessions).findByRole("alert")).toHaveTextContent(
        "internal",
      );
      expect(check).not.toHaveBeenCalled();
    });

    it("shows a failed token load where the tokens are and keeps the password and sessions usable", async () => {
      const user = userEvent.setup();
      const { fetchMock } = renderSection(
        "token",
        tokenRoutes({
          [`GET ${API}/access/tokens/api`]: () =>
            errorResponse(500, "internal"),
          [`DELETE ${API}/sessions`]: noContent,
        }),
      );

      expect(await screen.findByRole("alert")).toHaveTextContent("internal");
      expect(
        screen.queryByRole("group", { name: "Agent token · MCP" }),
      ).not.toBeInTheDocument();
      expect(
        within(row("Panel password")).getByText(`Set ${formatDate(SET_AT)}`),
      ).toBeInTheDocument();
      const sessions = row("Browser sessions");
      expect(
        within(sessions).getByText("Logged in from 2 browsers"),
      ).toBeInTheDocument();

      await user.click(
        within(row("Panel password")).getByRole("button", { name: "Change…" }),
      );
      expect(
        screen.getByRole("dialog", { name: "Change the panel password" }),
      ).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Cancel" }));
      await user.click(
        within(sessions).getByRole("button", { name: "Log out everywhere" }),
      );

      expect(requests(fetchMock)).toContain(`DELETE ${API}/sessions`);
      expect(check).toHaveBeenCalledTimes(1);
    });

    it("asks for no token and asks the startup gate to check the session when the mode switched elsewhere", async () => {
      const { fetchMock } = renderSection(
        "token",
        tokenRoutes({
          [`GET ${API}/access`]: () =>
            jsonResponse({ mode: "open", passwordSetAt: null, sessions: 0 }),
        }),
      );
      await settle();

      expect(requests(fetchMock)).toEqual([`GET ${API}/access`]);
      expect(check).toHaveBeenCalledTimes(1);
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(
        screen.queryByRole("group", { name: "Panel password" }),
      ).not.toBeInTheDocument();
    });

    describe("switching to no token", () => {
      async function openSwitch(user: ReturnType<typeof userEvent.setup>) {
        await user.click(
          await screen.findByRole("button", { name: "Switch to no token…" }),
        );
        return screen.getByRole("alertdialog", { name: "Switch to no token?" });
      }

      it("needs the acknowledgement and the current password, sends both, then reloads onto the tab", async () => {
        const user = userEvent.setup();
        const { fetchMock } = renderSection(
          "token",
          tokenRoutes({ [`PUT ${API}/access/mode`]: noContent }),
        );

        const dialog = await openSwitch(user);
        expect(
          within(dialog).getByText(
            "Without a token, Officer is open to everything on this computer",
          ),
        ).toBeInTheDocument();
        const confirm = within(dialog).getByRole("button", {
          name: "Switch to no token",
        });
        expect(confirm).toBeDisabled();
        await user.type(
          within(dialog).getByLabelText("Current password"),
          CURRENT,
        );
        expect(confirm).toBeDisabled();
        await user.click(
          within(dialog).getByRole("checkbox", {
            name: "I understand: any software on this computer can control Officer.",
          }),
        );
        expect(confirm).toBeEnabled();
        await user.click(confirm);

        expect(sentBody(fetchMock, `PUT ${API}/access/mode`)).toEqual({
          mode: "open",
          currentPassword: CURRENT,
          acknowledged: true,
        });
        expect(reload).toHaveBeenCalledTimes(1);
        expect(hashAtReload).toBe("#security");
      });

      it("shows a wrong current password inline without logging out", async () => {
        const user = userEvent.setup();
        const { onUnauthorized } = renderSection(
          "token",
          tokenRoutes({
            [`PUT ${API}/access/mode`]: () =>
              errorResponse(403, "current_password_invalid"),
          }),
        );

        const dialog = await openSwitch(user);
        const current = within(dialog).getByLabelText("Current password");
        await user.type(current, CURRENT);
        await user.click(within(dialog).getByRole("checkbox"));
        await user.click(
          within(dialog).getByRole("button", { name: "Switch to no token" }),
        );

        expect(current).toHaveAccessibleDescription(
          "The current password is incorrect.",
        );
        expect(current).toHaveValue("");
        expect(current).toHaveFocus();
        expect(dialog).toBeInTheDocument();
        expect(reload).not.toHaveBeenCalled();
        expect(onUnauthorized).not.toHaveBeenCalled();
        expect(check).not.toHaveBeenCalled();
      });

      it("closes on Cancel without a request", async () => {
        const user = userEvent.setup();
        const { fetchMock } = renderSection("token", tokenRoutes());

        const dialog = await openSwitch(user);
        await user.type(
          within(dialog).getByLabelText("Current password"),
          CURRENT,
        );
        await user.click(
          within(dialog).getByRole("button", { name: "Cancel" }),
        );

        expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
        expect(requests(fetchMock)).not.toContain(`PUT ${API}/access/mode`);
        const reopened = await openSwitch(user);
        expect(within(reopened).getByLabelText("Current password")).toHaveValue(
          "",
        );
      });
    });

    describe("changing the password", () => {
      async function fillChange(user: ReturnType<typeof userEvent.setup>) {
        await user.click(
          await screen.findByRole("button", { name: "Change…" }),
        );
        const dialog = screen.getByRole("dialog", {
          name: "Change the panel password",
        });
        await user.type(
          within(dialog).getByLabelText("Current password"),
          CURRENT,
        );
        await user.type(
          within(dialog).getByLabelText("New panel password"),
          NEW_PASSWORD,
        );
        await user.type(
          within(dialog).getByLabelText("Repeat the new password"),
          NEW_PASSWORD,
        );
        return dialog;
      }

      it("sends both passwords, closes, says so and reloads the access state", async () => {
        const user = userEvent.setup();
        let changed = false;
        const { fetchMock } = renderSection(
          "token",
          tokenRoutes({
            [`GET ${API}/access`]: () =>
              jsonResponse({
                mode: "token",
                passwordSetAt: changed ? CHANGED_AT : SET_AT,
                sessions: changed ? 1 : 2,
              }),
            [`PUT ${API}/access/password`]: () => {
              changed = true;
              return noContent();
            },
          }),
        );

        const dialog = await fillChange(user);
        await user.click(
          within(dialog).getByRole("button", { name: "Change the password" }),
        );

        expect(sentBody(fetchMock, `PUT ${API}/access/password`)).toEqual({
          currentPassword: CURRENT,
          password: NEW_PASSWORD,
        });
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
        expect(
          await screen.findByText(`Set ${formatDate(CHANGED_AT)}`),
        ).toBeInTheDocument();
        expect(screen.getByRole("status")).toHaveTextContent(
          "The panel password is changed. Every other browser is logged out.",
        );
        expect(
          screen.getByText("Logged in from 1 browser"),
        ).toBeInTheDocument();
      });

      it("drops the old access info when its reload fails, with a retry, and keeps the tokens", async () => {
        const user = userEvent.setup();
        let answer: "first" | "failing" | "changed" = "first";
        const { fetchMock } = renderSection(
          "token",
          tokenRoutes({
            [`GET ${API}/access`]: () =>
              answer === "failing"
                ? errorResponse(500, "internal")
                : jsonResponse({
                    mode: "token",
                    passwordSetAt: answer === "first" ? SET_AT : CHANGED_AT,
                    sessions: answer === "first" ? 2 : 1,
                  }),
            [`PUT ${API}/access/password`]: () => {
              answer = "failing";
              return noContent();
            },
          }),
        );

        const dialog = await fillChange(user);
        await user.click(
          within(dialog).getByRole("button", { name: "Change the password" }),
        );

        expect(await screen.findByText("Could not load data")).toBeVisible();
        expect(screen.getByText("internal")).toBeInTheDocument();
        expect(
          screen.queryByText("Logged in from 2 browsers"),
        ).not.toBeInTheDocument();
        expect(
          screen.queryByText(`Set ${formatDate(SET_AT)}`),
        ).not.toBeInTheDocument();
        expect(
          screen.getByText(
            "The panel password is changed. Every other browser is logged out.",
          ),
        ).toBeInTheDocument();
        expect(row("Agent token · MCP")).toBeInTheDocument();

        answer = "changed";
        await user.click(screen.getByRole("button", { name: "Retry" }));

        expect(
          await screen.findByText(`Set ${formatDate(CHANGED_AT)}`),
        ).toBeInTheDocument();
        expect(
          screen.getByText("Logged in from 1 browser"),
        ).toBeInTheDocument();
        expect(
          requests(fetchMock).filter((request) =>
            request.startsWith(`GET ${API}/access/tokens/`),
          ),
        ).toHaveLength(2);
      });

      it("shows a wrong current password inline, drops both passwords and keeps the session", async () => {
        const user = userEvent.setup();
        const { onUnauthorized } = renderSection(
          "token",
          tokenRoutes({
            [`PUT ${API}/access/password`]: () =>
              errorResponse(403, "current_password_invalid"),
          }),
        );

        const dialog = await fillChange(user);
        await user.click(
          within(dialog).getByRole("button", { name: "Change the password" }),
        );

        const current = within(dialog).getByLabelText("Current password");
        expect(current).toHaveAccessibleDescription(
          "The current password is incorrect.",
        );
        expect(current).toHaveValue("");
        expect(current).toHaveFocus();
        expect(within(dialog).getByLabelText("New panel password")).toHaveValue(
          "",
        );
        expect(
          within(dialog).getByLabelText("Repeat the new password"),
        ).toHaveValue("");
        expect(onUnauthorized).not.toHaveBeenCalled();
        expect(check).not.toHaveBeenCalled();
      });

      it("shows a refused new password inline", async () => {
        const user = userEvent.setup();
        renderSection(
          "token",
          tokenRoutes({
            [`PUT ${API}/access/password`]: () => problem(REFUSAL),
          }),
        );

        const dialog = await fillChange(user);
        await user.click(
          within(dialog).getByRole("button", { name: "Change the password" }),
        );

        const password =
          await within(dialog).findByLabelText("New panel password");
        expect(password).toHaveAccessibleDescription(REFUSAL);
        expect(password).toHaveValue("");
        expect(password).toHaveFocus();
        expect(within(dialog).getByLabelText("Current password")).toHaveValue(
          "",
        );
        expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
      });
    });
  });
});
