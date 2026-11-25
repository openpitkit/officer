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

import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { McpCommand } from "@/api/types";
import { useMcpAccess } from "@/api/useMcpAccess";
import { SidebarProvider } from "@/components/SidebarContext";
import i18n from "@/i18n";
import { McpAccess } from "@/pages/McpAccess";
import { renderWithApi as render } from "@/test/apiClient";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

vi.mock("@/api/useMcpAccess");
// The connect block requests the agent token; it is not under test here.
vi.mock("@/components/ConnectAgent", () => ({ ConnectAgent: () => null }));
// The page chrome polls market data and pre-migration backups on mount.
vi.mock("@/api/useMarketData", () => ({
  useMarketData: () => ({
    load: { state: "ready", data: { restartRequired: false }, error: null },
    reload: () => {},
  }),
}));
vi.mock("@/api/usePreMigrationBackups", () => ({
  usePreMigrationBackups: () => ({
    load: { state: "ready", data: [], error: null },
    reload: () => {},
  }),
}));

const useMcpAccessMock = vi.mocked(useMcpAccess);
const setMcpCommandMock = vi.fn();

const unblock: McpCommand = {
  name: "unblock_account",
  title: "Unblock account",
  agentDescription: "Lifts an account block.",
  mutating: true,
  protective: true,
  implemented: true,
  enabled: false,
};

function renderPage() {
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-test-density"
          tradeStyleStorageKey="pit-test-style"
        >
          <MemoryRouter>
            <SidebarProvider>
              <McpAccess />
            </SidebarProvider>
          </MemoryRouter>
        </DisplayPreferencesProvider>
      </ThemeProvider>
    </I18nextProvider>,
    { api: { setMcpCommand: setMcpCommandMock } },
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
  setMcpCommandMock.mockReset();
  useMcpAccessMock.mockReturnValue({
    load: { state: "ready", data: [unblock], error: null },
    reload: vi.fn(),
  });
});

describe("McpAccess protective command confirmation", () => {
  it("puts the can-an-agent-bypass guide before Cancel and enables nothing by itself", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("checkbox", { name: /unblock_account/ }));
    const dialog = await screen.findByRole("alertdialog");
    const guide = within(dialog).getByRole("link", {
      name: "Video guide on YouTube: can an agent bypass the limits",
    });
    expect(guide).toHaveAttribute(
      "href",
      "https://www.youtube.com/watch?v=jxoP79byyGc",
    );
    expect(guide.nextElementSibling).toBe(
      within(dialog).getByRole("button", { name: "Cancel" }),
    );
    expect(setMcpCommandMock).not.toHaveBeenCalled();
  });

  it("puts no guide on the page itself", () => {
    renderPage();

    expect(
      screen.queryByRole("link", { name: /^Video guide on YouTube:/ }),
    ).not.toBeInTheDocument();
  });
});
