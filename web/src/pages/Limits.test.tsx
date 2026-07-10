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

import { fireEvent, screen, waitFor } from "@testing-library/react";
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
const fetchGroupsMock = vi.fn();

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

function renderLimits(
  initialEntry = "/policies",
  options: { includeGroupsApi?: boolean } = {},
) {
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
        ...(options.includeGroupsApi ? { fetchGroups: fetchGroupsMock } : {}),
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
  fetchGroupsMock.mockResolvedValue([]);
});

describe("Limits identity filters", () => {
  it("renders the self-computed PnL policy", () => {
    useLimitsMock.mockReturnValue(
      readyPage([
        {
          policy: "spot_funds_pnl_bounds_kill_switch",
          scope: "account_group",
          account: "",
          accountGroup: "desk-a",
          asset: "",
          accountCurrency: "USD",
          values: { lower_bound: "-1000" },
        },
      ]),
    );

    renderLimits();

    expect(screen.getByText("PnL Kill Switch")).toBeInTheDocument();
    expect(screen.getByText("desk-a")).toBeInTheDocument();
    expect(screen.getByText("USD")).toBeInTheDocument();
  });

  it("accepts the self-computed PnL policy filter from the URL", () => {
    renderLimits("/policies?policy=spot_funds_pnl_bounds");

    expect(
      latestPolicyFiltersMatching(
        (filters) => filters?.policy === "spot_funds_pnl_bounds",
      ),
    ).toEqual(
      expect.objectContaining({ policy: "spot_funds_pnl_bounds" }),
    );
  });

  it("accepts SpotFunds account-group and currency filters from the URL", () => {
    renderLimits(
      "/policies?policy=spot_funds_pnl_bounds&accountGroup=desk-a&accountCurrency=USD",
    );

    expect(
      latestPolicyFiltersMatching(
        (filters) =>
          filters?.policy === "spot_funds_pnl_bounds" &&
          filters.accountGroup === "desk-a" &&
          filters.accountCurrency === "USD",
      ),
    ).toEqual(
      expect.objectContaining({
        policy: "spot_funds_pnl_bounds",
        accountGroup: "desk-a",
        accountCurrency: "USD",
      }),
    );
  });

  it("loads group and currency suggestions with supported account sorting", async () => {
    renderLimits("/policies", { includeGroupsApi: true });

    await waitFor(() => expect(fetchGroupsMock).toHaveBeenCalled());
    expect(fetchAccountsMock).toHaveBeenCalledWith(
      expect.objectContaining({ limit: 1000, sort: "code" }),
      expect.any(AbortSignal),
    );
  });

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

  it("keeps account-group and currency in the shared filter link", () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    renderLimits(
      "/policies?policy=spot_funds_pnl_bounds&account=acc-1" +
        "&accountGroup=desk-a&asset=BTC&accountCurrency=USD",
    );

    fireEvent.click(
      screen.getByRole("button", {
        name: "Copy a link to the current filter set",
      }),
      { ctrlKey: true },
    );

    expect(open).toHaveBeenCalledTimes(1);
    const shared = new URL(open.mock.calls[0][0] as string);
    expect(shared.pathname).toBe("/policies");
    expect(shared.searchParams.get("account")).toBe("acc-1");
    expect(shared.searchParams.get("accountGroup")).toBe("desk-a");
    expect(shared.searchParams.get("asset")).toBe("BTC");
    expect(shared.searchParams.get("accountCurrency")).toBe("USD");
    expect(shared.searchParams.get("policy")).toBe("spot_funds_pnl_bounds");
  });
});
