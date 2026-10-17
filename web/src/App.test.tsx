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
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import type { AccountMode } from "@/api/accountMode";
import App from "@/App";
import { jsonResponse, renderWithApi } from "@/test/apiClient";
import { ThemeProvider } from "@/theme/ThemeProvider";

// The shell's navigation and pages are tested on their own; this test covers
// the dialogs the app opens over them.
vi.mock("@/framework", async () => {
  const actual =
    await vi.importActual<typeof import("@/framework")>("@/framework");
  return { ...actual, AppRoutes: () => null, Sidebar: () => null };
});

const MODE_TITLE = "How many accounts will you use?";
const WELCOME_TITLE = "Welcome to Pit Officer";

function renderApp(mode: AccountMode | null, welcomeSeen: boolean) {
  const fetchMock = vi.fn(async () =>
    jsonResponse({
      mode,
      account: "",
      features: { accounts: true, account: "" },
      restartRequired: false,
    }),
  );
  const fetchWelcomeSeen = vi.fn().mockResolvedValue(welcomeSeen);
  renderWithApi(
    <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
      <MemoryRouter>
        <App />
      </MemoryRouter>
    </ThemeProvider>,
    { fetch: fetchMock as unknown as typeof fetch, api: { fetchWelcomeSeen } },
  );
  return { fetchMock, fetchWelcomeSeen };
}

// By the title text: a modal dialog hides the dialogs under it, and with them
// their accessible names.
function queryDialog(title: string) {
  return screen.queryByText(title);
}

// Lets pending answers and the renders they cause settle.
async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

describe("App", () => {
  it("asks for the account mode first and opens the welcome after it", async () => {
    const user = userEvent.setup();
    const { fetchWelcomeSeen } = renderApp(null, false);

    const modeDialog = await screen.findByRole("dialog", { name: MODE_TITLE });
    await settle();
    expect(fetchWelcomeSeen).toHaveBeenCalledTimes(1);
    expect(queryDialog(WELCOME_TITLE)).not.toBeInTheDocument();

    await user.click(within(modeDialog).getByRole("button", { name: "Close" }));

    expect(
      await screen.findByRole("dialog", { name: WELCOME_TITLE }),
    ).toBeInTheDocument();
    expect(queryDialog(MODE_TITLE)).not.toBeInTheDocument();
  });

  it("opens the welcome directly once a mode is stored, reading it once", async () => {
    const { fetchMock } = renderApp("multi", false);

    expect(
      await screen.findByRole("dialog", { name: WELCOME_TITLE }),
    ).toBeInTheDocument();
    await settle();
    expect(queryDialog(MODE_TITLE)).not.toBeInTheDocument();
    // The scope load and its first poll; no read of its own at startup.
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("opens neither dialog once a mode is stored and the welcome is seen", async () => {
    const { fetchMock, fetchWelcomeSeen } = renderApp("multi", true);

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    await settle();

    expect(fetchWelcomeSeen).toHaveBeenCalledTimes(1);
    expect(screen.queryAllByRole("dialog", { hidden: true })).toHaveLength(0);
  });
});
