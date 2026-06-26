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

import {
  createAdjustment,
  exportBusinessCsv,
  fetchAdjustments,
  importBusinessCsv,
  previewBusinessCsvImport,
} from "@/api/client";
import type {
  Account,
  Adjustment,
  Balance,
  BusinessCsvEntity,
  BusinessCsvExportFilters,
  BusinessCsvImportEntity,
} from "@/api/types";
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
vi.mock("@/components/TableControls", async () => {
  const actual =
    await vi.importActual<typeof import("@/components/TableControls")>(
      "@/components/TableControls",
    );
  const dialogs =
    await vi.importActual<typeof import("@/components/BusinessCsvDialogs")>(
      "@/components/BusinessCsvDialogs",
    );
  return {
    ...actual,
    CsvTransferMenu: ({
      exports,
      imports,
      onImported,
    }: {
      exports?: {
        entity: BusinessCsvEntity;
        filters?: BusinessCsvExportFilters;
        label: string;
      }[];
      imports?: {
        defaultEntity?: BusinessCsvImportEntity;
        entities: BusinessCsvImportEntity[];
        label: string;
      }[];
      onImported?: () => void;
    }) => (
      <>
        {imports?.map((item) => (
          <dialogs.BusinessCsvImportDialog
            key={`import-${item.label}`}
            defaultEntity={item.defaultEntity}
            entities={item.entities}
            onImported={onImported ?? (() => {})}
            trigger={(open) => (
              <button type="button" onClick={open}>
                {item.label}
              </button>
            )}
          />
        ))}
        {exports?.map((item) => (
          <dialogs.BusinessCsvExportDialog
            key={`export-${item.entity}-${item.label}`}
            entity={item.entity}
            filters={item.filters}
            trigger={(open) => (
              <button type="button" onClick={open}>
                {item.label}
              </button>
            )}
          />
        ))}
      </>
    ),
  };
});
vi.mock("@/api/client", async () => {
  const actual =
    await vi.importActual<typeof import("@/api/client")>("@/api/client");
  return {
    ...actual,
    createAdjustment: vi.fn(),
    exportBusinessCsv: vi.fn(),
    fetchAdjustments: vi.fn(),
    importBusinessCsv: vi.fn(),
    previewBusinessCsvImport: vi.fn(),
  };
});

function ready<T>(data: T): PollingResult<T> {
  return { load: { state: "ready", data, error: null }, reload: vi.fn() };
}

const useAccountsMock = vi.mocked(useAccounts);
const useAdjustmentsMock = vi.mocked(useAdjustments);
const useBalancesMock = vi.mocked(useBalances);
const createAdjustmentMock = vi.mocked(createAdjustment);
const exportBusinessCsvMock = vi.mocked(exportBusinessCsv);
const fetchAdjustmentsMock = vi.mocked(fetchAdjustments);
const importBusinessCsvMock = vi.mocked(importBusinessCsv);
const previewBusinessCsvImportMock = vi.mocked(previewBusinessCsvImport);

const proto = window.HTMLElement.prototype as HTMLElement & {
  hasPointerCapture?: (pointerId: number) => boolean;
  setPointerCapture?: (pointerId: number) => void;
};
proto.hasPointerCapture = () => false;
proto.setPointerCapture = () => {};
proto.scrollIntoView = () => {};

const account: Account = {
  code: "Bucks McMoneyface",
  title: "Bucks McMoneyface",
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
    externalId: "adj-alpha-10",
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
  exportBusinessCsvMock.mockResolvedValue({
    blob: new Blob(["csv"]),
    filename: "positions.csv",
  });
  fetchAdjustmentsMock.mockResolvedValue([]);
  previewBusinessCsvImportMock.mockResolvedValue({
    file: { name: "positions.csv", type: "csv" },
    counts: {
      rows: 1,
      applied: 0,
      skipped: 0,
      conflicts: 0,
      stopped: false,
    },
    conflicts: [],
  });
  importBusinessCsvMock.mockResolvedValue({
    file: { name: "positions.csv", type: "csv" },
    counts: {
      rows: 1,
      applied: 1,
      skipped: 0,
      conflicts: 0,
      stopped: false,
    },
    conflicts: [],
  });
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    value: vi.fn(() => "blob:positions-csv"),
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    configurable: true,
    value: vi.fn(),
  });
  HTMLAnchorElement.prototype.click = () => {};
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
    expect(
      scope.getAllByRole("button", { name: /increase/i }).length,
    ).toBeGreaterThan(0);
    expect(
      scope.getAllByRole("button", { name: /decrease/i }).length,
    ).toBeGreaterThan(0);
    expect(scope.queryByLabelText("Lower")).not.toBeInTheDocument();

    await user.type(scope.getByLabelText("Available adjustment amount"), "600");
    await user.type(scope.getByLabelText("Held adjustment amount"), "10");
    await user.clear(scope.getByLabelText("Incoming adjustment amount"));
    await user.type(scope.getByLabelText("Incoming adjustment amount"), "-50");
    await user.type(scope.getByLabelText("Average entry price (optional)"), "142.50");
    await user.click(
      scope.getByRole("button", { name: /bounds \(optional\)/i }),
    );
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

describe("Positions business CSV", () => {
  it("exports positions with account and asset filters only", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.type(screen.getByLabelText("Filter by account"), "desk-alpha");
    await user.type(screen.getByLabelText("Filter by asset"), "AAPL");
    await user.click(screen.getByRole("button", { name: /export positions csv/i }));
    await user.click(screen.getByRole("button", { name: /^export$/i }));

    await waitFor(() => expect(exportBusinessCsvMock).toHaveBeenCalledTimes(1));
    expect(exportBusinessCsvMock).toHaveBeenCalledWith(
      expect.objectContaining({
        entity: "positions",
        filters: {
          account: "desk-alpha",
          asset: "AAPL",
        },
      }),
    );
  });

  it("exports adjustment history with the selected source filter", async () => {
    const user = userEvent.setup();
    useAdjustmentsMock.mockReturnValue(
      ready<Adjustment[]>([
        acceptedAdjustment({
          asset: "AAPL",
          balance: { mode: "absolute", value: "10" },
        }),
      ]),
    );
    renderPositions();

    await user.click(screen.getByRole("button", { name: "History" }));
    const sourceControl = screen.getAllByText("Source")[0]!.parentElement!;
    await user.click(within(sourceControl).getByRole("combobox"));
    await user.click(screen.getByRole("option", { name: "mcp" }));
    await user.click(screen.getByRole("button", { name: /export history csv/i }));

    await waitFor(() => expect(fetchAdjustmentsMock).toHaveBeenCalledTimes(1));
    expect(fetchAdjustmentsMock).toHaveBeenCalledWith({
      source: "mcp",
      limit: 1000,
    });
  });

  it("imports positions and reloads balances plus history after success", async () => {
    const user = userEvent.setup();
    const balancesReload = vi.fn();
    const adjustmentsReload = vi.fn();
    useBalancesMock.mockReturnValue({
      load: { state: "ready", data: [balance], error: null },
      reload: balancesReload,
    });
    useAdjustmentsMock.mockReturnValue({
      load: { state: "ready", data: [], error: null },
      reload: adjustmentsReload,
    });
    renderPositions();

    await user.click(
      screen.getByRole("button", { name: /import positions csv/i }),
    );
    await user.type(
      screen.getByPlaceholderText(/paste csv rows here/i),
      "account_id,asset,available\nBucks McMoneyface,AAPL,10\n",
    );
    await user.click(screen.getByRole("button", { name: /preview upload/i }));
    await waitFor(() =>
      expect(previewBusinessCsvImportMock).toHaveBeenCalledTimes(1),
    );
    expect(previewBusinessCsvImportMock).toHaveBeenCalledWith(
      expect.objectContaining({
        entity: "positions",
        filename: "pasted.csv",
      }),
    );

    await user.click(screen.getByRole("button", { name: /apply import/i }));
    await waitFor(() => expect(importBusinessCsvMock).toHaveBeenCalledTimes(1));
    expect(importBusinessCsvMock).toHaveBeenCalledWith(
      expect.objectContaining({
        entity: "positions",
        conflictPolicy: "skip",
      }),
    );
    expect(balancesReload).toHaveBeenCalledTimes(1);
    expect(adjustmentsReload).toHaveBeenCalledTimes(1);
  });
});
