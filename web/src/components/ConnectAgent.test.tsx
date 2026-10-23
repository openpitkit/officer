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

import { act, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AccessMode } from "@/api/access";
import { ConnectAgent } from "@/components/ConnectAgent";
import { maskAccessToken } from "@/lib/accessTokenMask";
import {
  claudeDesktopConfig,
  claudeMcpAddCommand,
  codexConfig,
  cursorConfig,
} from "@/lib/agentArtifacts";
import { buildMcpAgentPrompt } from "@/lib/mcpAgentPrompt";
import { onCheckSession, setPanelSession } from "@/lib/panelSession";
import {
  errorResponse,
  jsonResponse,
  renderWithApi,
  serving,
  type Route,
} from "@/test/apiClient";

const API = "/app/api/v1";
const AGENT_TOKEN = "pit_mcp_Q2x7bWv9R1t4YzK8pN3sJd6HfL0aEu5cXg2iTo7Vr4w";
const MASKED = `pit_mcp_Q2x7${"•".repeat(14)}Vr4w`;
const TOKEN_REQUEST = `GET ${API}/access/tokens/agent`;

const CLI = "Add the connector with the Claude CLI";
const DESKTOP = "Or add this entry to your Claude Desktop config";
const CODEX = "Add this entry to ~/.codex/config.toml";
const CURSOR = "Add this entry to ~/.cursor/mcp.json";
const OPEN_NOTE =
  "Officer runs without a token: this agent needs none - and neither does any other program, container or agent on this computer.";
const TOKEN_NOTE =
  "Works only for MCP. It cannot call the REST API or open the panel.";

/** The block derives the MCP address from the page's own origin. */
function mcpUrl(): string {
  return `${window.location.origin}/mcp`;
}

// The exact strings live in agentArtifacts.test.ts; here only the wiring of
// each builder to the URL and the shown or copied token is asserted.
type Build = (url: string, token: string | null) => string;

function renderBlock(mode: AccessMode, routes: Record<string, Route> = {}) {
  setPanelSession(mode);
  const fetchMock = serving(routes);
  renderWithApi(
    <MemoryRouter>
      <ConnectAgent />
    </MemoryRouter>,
    { fetch: fetchMock as unknown as typeof fetch },
  );
  return fetchMock;
}

function requests(fetchMock: ReturnType<typeof serving>) {
  return fetchMock.mock.calls.map(([url, init]) => `${init.method} ${url}`);
}

// Lets every request the block started reach the mock and settle.
async function settle() {
  await act(() => new Promise((resolve) => setTimeout(resolve, 0)));
}

function snippet(label: string): HTMLElement {
  return screen.getByRole("textbox", { name: label });
}

/** The Copy button of the snippet labelled `label`. */
function snippetCopy(label: string): HTMLElement {
  const block = snippet(label).parentElement;
  if (block === null) {
    throw new Error(`snippet ${label} has no container`);
  }
  return within(block).getByRole("button", { name: "Copy" });
}

function tokenRoutes(): Record<string, Route> {
  return { [TOKEN_REQUEST]: () => jsonResponse({ token: AGENT_TOKEN }) };
}

describe("ConnectAgent", () => {
  const check = vi.fn();
  let unsubscribe = () => {};

  beforeEach(() => {
    unsubscribe = onCheckSession(check);
  });

  afterEach(() => {
    unsubscribe();
    check.mockReset();
  });

  describe("in open access", () => {
    it("shows one plain line with a link to Service › Security, no token row and no token request", async () => {
      const fetchMock = renderBlock("open");
      await settle();

      expect(screen.getByText(OPEN_NOTE)).toBeInTheDocument();
      expect(
        screen.getByRole("link", { name: "Service › Security" }),
      ).toHaveAttribute("href", "/service#security");
      expect(
        screen.queryByRole("group", { name: "Agent token" }),
      ).not.toBeInTheDocument();
      expect(
        screen.queryByRole("group", { name: "MCP address" }),
      ).not.toBeInTheDocument();
      expect(screen.queryByText(TOKEN_NOTE)).not.toBeInTheDocument();
      expect(requests(fetchMock)).toEqual([]);
    });

    it.each<[string, string, Build]>([
      ["Claude", CLI, claudeMcpAddCommand],
      ["Claude", DESKTOP, claudeDesktopConfig],
      ["Codex", CODEX, codexConfig],
      ["Cursor", CURSOR, cursorConfig],
    ])(
      "gives the %s snippet %s without a token and copies it as shown",
      async (tab, label, build) => {
        const user = userEvent.setup();
        renderBlock("open");
        const expected = build(mcpUrl(), null);

        await user.click(screen.getByRole("button", { name: tab }));
        const text = snippet(label);
        expect(text).toHaveValue(expected);
        expect((text as HTMLTextAreaElement).value).not.toMatch(
          /--header|headers|http_headers|"env"|Bearer|pit_mcp_/,
        );
        await user.click(snippetCopy(label));

        expect(await navigator.clipboard.readText()).toBe(expected);
      },
    );
  });

  describe("in token access", () => {
    it("fetches the agent token once and shows it masked until Show, copying the real value", async () => {
      const user = userEvent.setup();
      const fetchMock = renderBlock("token", tokenRoutes());

      // Nothing that carries the token renders before it has loaded.
      expect(screen.getByRole("status")).toBeInTheDocument();
      expect(
        screen.queryByRole("textbox", { name: CLI }),
      ).not.toBeInTheDocument();

      const tokenRow = await screen.findByRole("group", {
        name: "Agent token",
      });
      expect(within(tokenRow).getByText(MASKED)).toBeInTheDocument();
      expect(screen.getByText(TOKEN_NOTE)).toBeInTheDocument();
      expect(document.body.innerHTML).not.toContain(AGENT_TOKEN);
      expect(screen.queryByText(OPEN_NOTE)).not.toBeInTheDocument();

      await user.click(within(tokenRow).getByRole("button", { name: "Copy" }));
      expect(await navigator.clipboard.readText()).toBe(AGENT_TOKEN);

      await user.click(within(tokenRow).getByRole("button", { name: "Show" }));
      expect(within(tokenRow).getByText(AGENT_TOKEN)).toBeInTheDocument();
      await user.click(within(tokenRow).getByRole("button", { name: "Hide" }));
      expect(within(tokenRow).getByText(MASKED)).toBeInTheDocument();
      expect(document.body.innerHTML).not.toContain(AGENT_TOKEN);

      const addressRow = screen.getByRole("group", { name: "MCP address" });
      expect(within(addressRow).getByText(mcpUrl())).toBeInTheDocument();
      await user.click(
        within(addressRow).getByRole("button", { name: "Copy" }),
      );
      expect(await navigator.clipboard.readText()).toBe(mcpUrl());

      expect(requests(fetchMock)).toEqual([TOKEN_REQUEST]);
    });

    it.each<[string, string, Build]>([
      ["Claude", CLI, claudeMcpAddCommand],
      ["Claude", DESKTOP, claudeDesktopConfig],
      ["Codex", CODEX, codexConfig],
      ["Cursor", CURSOR, cursorConfig],
    ])(
      "shows the %s snippet %s with the token masked until Show and copies it with the real token",
      async (tab, label, build) => {
        const user = userEvent.setup();
        renderBlock("token", tokenRoutes());
        await screen.findByRole("group", { name: "Agent token" });
        const url = mcpUrl();

        await user.click(screen.getByRole("button", { name: tab }));
        expect(snippet(label)).toHaveValue(
          build(url, maskAccessToken("agent", AGENT_TOKEN)),
        );
        await user.click(snippetCopy(label));
        expect(await navigator.clipboard.readText()).toBe(
          build(url, AGENT_TOKEN),
        );

        await user.click(screen.getByRole("button", { name: "Show" }));
        expect(snippet(label)).toHaveValue(build(url, AGENT_TOKEN));
      },
    );

    it("shows the error instead of any token-carrying snippet when the token fails to load", async () => {
      const user = userEvent.setup();
      renderBlock("token", {
        [TOKEN_REQUEST]: () => errorResponse(500, "internal"),
      });

      expect(await screen.findByRole("alert")).toBeInTheDocument();
      expect(
        screen.queryByRole("group", { name: "Agent token" }),
      ).not.toBeInTheDocument();
      expect(screen.queryByRole("textbox", { name: CLI })).toBeNull();
      expect(screen.queryByRole("textbox", { name: DESKTOP })).toBeNull();
      // The skill carries no token, so its download stays.
      expect(
        screen.getByRole("button", { name: "Download Claude Skill" }),
      ).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Codex" }));
      expect(screen.queryByRole("textbox", { name: CODEX })).toBeNull();
      await user.click(screen.getByRole("button", { name: "Cursor" }));
      expect(screen.queryByRole("textbox", { name: CURSOR })).toBeNull();

      // Snippets that never carry the token render as before.
      await user.click(screen.getByRole("button", { name: "ChatGPT" }));
      expect(snippet("MCP connector URL")).toHaveValue(mcpUrl());
      expect(check).not.toHaveBeenCalled();
    });

    it("checks the session again on a 409 conflict, which means the access mode changed", async () => {
      renderBlock("token", {
        [TOKEN_REQUEST]: () => errorResponse(409, "conflict"),
      });
      await settle();

      expect(check).toHaveBeenCalledTimes(1);
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(screen.queryByRole("textbox", { name: CLI })).toBeNull();
    });
  });

  it("switches tabs and keeps the ChatGPT and Prompt tabs as they were", async () => {
    const user = userEvent.setup();
    renderBlock("open");

    expect(
      screen.getByRole("button", { name: "Download Claude Skill" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Claude Desktop reaches a local server through the mcp-remote bridge, which needs Node.js.",
      ),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "ChatGPT" }));
    expect(snippet("MCP connector URL")).toHaveValue(mcpUrl());
    expect(
      screen.getByText(
        "Hosted ChatGPT runs outside this computer and reaches only a public HTTPS address. Officer stays on this computer and serves plain HTTP, so hosted ChatGPT cannot connect to it. Connect an agent that runs on this computer instead.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: CLI })).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Download Claude Skill" }),
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Prompt" }));
    expect(snippet("Paste into your agent's prompt")).toHaveValue(
      buildMcpAgentPrompt(),
    );

    await user.click(screen.getByRole("button", { name: "Claude" }));
    expect(snippet(CLI)).toHaveValue(claudeMcpAddCommand(mcpUrl(), null));
  });
});
