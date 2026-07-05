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

import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Limit, PolicyListFilters } from "@/api/types";
import { useLimitsPage } from "@/api/useLimits";
import type { PollingResult } from "@/api/usePolling";
import { SidebarProvider } from "@/components/SidebarContext";
import { renderWithApi as render } from "@/test/apiClient";
import i18n from "@/i18n";
import { Limits } from "@/pages/Limits";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

vi.mock("@/api/useLimits", () => ({ useLimitsPage: vi.fn() }));
vi.mock("@/api/useMarketData", () => ({
  useMarketData: () => ({
    load: { state: "ready", data: { restartRequired: false }, error: null },
    reload: () => {},
  }),
}));

const useLimitsMock = vi.mocked(useLimitsPage);
const fetchAccountsMock = vi.fn();
const fetchAssetsMock = vi.fn();

function readyPage(items: Limit[] = []): PollingResult<{
  items: Limit[];
  total: number;
}> {
  return {
    load: { state: "ready", data: { items, total: items.length }, error: null },
    reload: vi.fn(),
  };
}

function lastPolicyFilters(): PolicyListFilters | undefined {
  const calls = useLimitsMock.mock.calls;
  return calls[calls.length - 1]?.[0];
}

function latestPolicyFiltersMatching(
  predicate: (filters: PolicyListFilters | undefined) => boolean,
): PolicyListFilters | undefined {
  for (const [filters] of [...useLimitsMock.mock.calls].reverse()) {
    if (predicate(filters)) {
      return filters;
    }
  }
  return undefined;
}

function renderLimits(initialEntry = "/policies") {
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-officer-test-density"
          tradeStyleStorageKey="pit-officer-test-trade-style"
        >
          <MemoryRouter initialEntries={[initialEntry]}>
            <SidebarProvider>
              <Limits />
            </SidebarProvider>
          </MemoryRouter>
        </DisplayPreferencesProvider>
      </ThemeProvider>
    </I18nextProvider>,
    {
      api: {
        fetchAccounts: fetchAccountsMock,
        fetchAssets: fetchAssetsMock,
      },
    },
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  await i18n.changeLanguage("en");
  useLimitsMock.mockReturnValue(readyPage());
  fetchAccountsMock.mockResolvedValue([]);
  fetchAssetsMock.mockResolvedValue([]);
});

describe("Limits identity filters", () => {
  it("applies account and asset filters when Enter is pressed", async () => {
    const user = userEvent.setup();
    renderLimits();

    await user.type(screen.getByLabelText("Filter by account"), "desk-alpha");
    await user.type(screen.getByLabelText("Filter by asset"), "AAPL");
    expect(lastPolicyFilters()).not.toEqual(
      expect.objectContaining({ account: "desk-alpha", asset: "AAPL" }),
    );

    await user.keyboard("{Enter}");

    await waitFor(() =>
      expect(
        latestPolicyFiltersMatching(
          (filters) =>
            filters?.account === "desk-alpha" && filters.asset === "AAPL",
        ),
      ).toEqual(
        expect.objectContaining({
          account: "desk-alpha",
          asset: "AAPL",
        }),
      ),
    );
  });

  it("applies an asset suggestion when it is selected", async () => {
    const user = userEvent.setup();
    fetchAssetsMock.mockResolvedValue([{ code: "AAPL" }]);
    renderLimits();

    await user.type(screen.getByLabelText("Filter by asset"), "AA");
    await waitFor(() => expect(fetchAssetsMock).toHaveBeenCalled());
    await user.click(await screen.findByRole("option", { name: "AAPL" }));

    await waitFor(() =>
      expect(
        latestPolicyFiltersMatching((filters) => filters?.asset === "AAPL"),
      ).toEqual(
        expect.objectContaining({
          asset: "AAPL",
        }),
      ),
    );
  });
});
