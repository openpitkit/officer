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

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { createAdjustment } from "@/api/client";
import type { Account, Adjustment, Balance } from "@/api/types";
import type { PollingResult } from "@/api/usePolling";
import { useAccounts } from "@/api/useAccounts";
import { useAdjustments } from "@/api/useAdjustments";
import { useBalances } from "@/api/useBalances";
import { SidebarProvider } from "@/components/SidebarContext";
import i18n from "@/i18n";
import { Positions } from "@/pages/Positions";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

vi.mock("@/api/useAccounts", () => ({ useAccounts: vi.fn() }));
vi.mock("@/api/useAdjustments", () => ({ useAdjustments: vi.fn() }));
vi.mock("@/api/useBalances", () => ({ useBalances: vi.fn() }));
vi.mock("@/api/useMarketData", () => ({
  useMarketData: () => ({
    load: { state: "ready", data: { restartRequired: false }, error: null },
    reload: () => {},
  }),
}));
vi.mock("@/api/client", async () => {
  const actual =
    await vi.importActual<typeof import("@/api/client")>("@/api/client");
  return {
    ...actual,
    createAdjustment: vi.fn(),
  };
});

function ready<T>(data: T): PollingResult<T> {
  return { load: { state: "ready", data, error: null }, reload: vi.fn() };
}

const useAccountsMock = vi.mocked(useAccounts);
const useAdjustmentsMock = vi.mocked(useAdjustments);
const useBalancesMock = vi.mocked(useBalances);
const createAdjustmentMock = vi.mocked(createAdjustment);

const account: Account = {
  id: "Bucks McMoneyface",
  blocked: false,
  blockReason: "",
  group: "",
  notes: "",
};

const balance: Balance = {
  account: "Bucks McMoneyface",
  asset: "AAPL",
  available: "0",
  held: "0",
  incoming: "500",
  averageEntryPrice: "",
  realizedPnl: "0",
  updatedAt: "2026-06-24T16:41:52Z",
};

function acceptedAdjustment(request: Adjustment["request"]): Adjustment {
  return {
    id: 10,
    account: "Bucks McMoneyface",
    at: "2026-06-24T16:42:00Z",
    source: "panel",
    asset: request.asset,
    status: "accepted",
    request,
    accepted: {
      balanceDelta: "600",
      balanceResult: "600",
      heldDelta: "10",
      heldResult: "10",
      incomingDelta: "-50",
      incomingResult: "450",
    },
  };
}

function renderPositions(initialEntry = "/positions") {
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-officer-test-density"
          tradeStyleStorageKey="pit-officer-test-trade-style"
        >
          <MemoryRouter initialEntries={[initialEntry]}>
            <SidebarProvider>
              <Positions />
            </SidebarProvider>
          </MemoryRouter>
        </DisplayPreferencesProvider>
      </ThemeProvider>
    </I18nextProvider>,
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  await i18n.changeLanguage("en");
  useAccountsMock.mockReturnValue(ready<Account[]>([account]));
  useAdjustmentsMock.mockReturnValue(ready<Adjustment[]>([]));
  useBalancesMock.mockReturnValue(ready<Balance[]>([balance]));
  createAdjustmentMock.mockImplementation(async (_account, body) =>
    acceptedAdjustment(body as unknown as Adjustment["request"]),
  );
});

describe("Positions adjustment panel", () => {
  it("opens from a balance row and submits the intended payload", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.click(
      screen.getByRole("button", {
        name: /open adjustment panel for bucks mcmoneyface aapl/i,
      }),
    );

    const panel = screen.getByRole("region", { name: "Adjustment" });
    const scope = within(panel);

    expect(scope.getAllByText("500").length).toBeGreaterThan(0);
    expect(scope.getAllByRole("button", { name: /increase/i }).length).toBeGreaterThan(0);
    expect(scope.getAllByRole("button", { name: /decrease/i }).length).toBeGreaterThan(0);

    await user.type(scope.getByLabelText("Available adjustment amount"), "600");
    await user.type(scope.getByLabelText("Held adjustment amount"), "10");
    await user.clear(scope.getByLabelText("Incoming adjustment amount"));
    await user.type(scope.getByLabelText("Incoming adjustment amount"), "-50");
    await user.type(scope.getByLabelText("Average entry price (optional)"), "142.50");
    await user.type(scope.getAllByLabelText("Lower")[0], "-100");
    await user.type(scope.getAllByLabelText("Upper")[0], "1000");

    expect(scope.getByLabelText("Average entry price (optional)")).toHaveValue(
      "142.50",
    );

    await user.click(scope.getByRole("button", { name: /submit adjustment/i }));

    await waitFor(() => expect(createAdjustmentMock).toHaveBeenCalledTimes(1));
    expect(createAdjustmentMock).toHaveBeenCalledWith("Bucks McMoneyface", {
      asset: "AAPL",
      balance: { mode: "absolute", value: "600" },
      held: { mode: "absolute", value: "10" },
      incoming: { mode: "absolute", value: "-50" },
      averageEntryPrice: "142.50",
      balanceBounds: { lower: "-100", upper: "1000" },
    });
  });

  it("opens the draft panel from the page action with current filters", async () => {
    const user = userEvent.setup();
    renderPositions("/positions?account=Bucks%20McMoneyface");

    await user.type(screen.getByLabelText("Filter by asset"), "AAPL");
    await user.click(screen.getByRole("button", { name: /^adjustment$/i }));

    expect(screen.getByLabelText("Account")).toHaveValue("Bucks McMoneyface");
    expect(screen.getByLabelText("Asset")).toHaveValue("AAPL");
  });
});
