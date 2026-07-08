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

import {
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithApi as render } from "@/test/apiClient";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type {
  Account,
  Adjustment,
  Balance,
  BalanceListFilters,
  BusinessCsvEntity,
  BusinessCsvExportFilters,
  BusinessCsvImportEntity,
  Group,
} from "@/api/types";
import type { PollingResult } from "@/api/usePolling";
import { useAccounts } from "@/api/useAccounts";
import { useAdjustmentsPage } from "@/api/useAdjustments";
import { useBalancesPage } from "@/api/useBalances";
import { SidebarProvider } from "@/components/SidebarContext";
import i18n from "@/i18n";
import { Positions } from "@/pages/Positions";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

vi.mock("@/api/useAccounts", () => ({ useAccounts: vi.fn() }));
vi.mock("@/api/useAdjustments", () => ({ useAdjustmentsPage: vi.fn() }));
vi.mock("@/api/useBalances", () => ({ useBalancesPage: vi.fn() }));
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
function ready<T>(data: T): PollingResult<T> {
  return { load: { state: "ready", data, error: null }, reload: vi.fn() };
}

function readyPage<T>(items: T[]): PollingResult<{ items: T[]; total: number }> {
  return ready({ items, total: items.length });
}

const useAccountsMock = vi.mocked(useAccounts);
const useAdjustmentsMock = vi.mocked(useAdjustmentsPage);
const useBalancesMock = vi.mocked(useBalancesPage);
const createAdjustmentMock = vi.fn();
const exportBusinessCsvMock = vi.fn();
const fetchAdjustmentsMock = vi.fn();
const importBusinessCsvMock = vi.fn();
const previewBusinessCsvImportMock = vi.fn();

function lastBalanceFilters(): BalanceListFilters | undefined {
  const calls = useBalancesMock.mock.calls;
  return calls[calls.length - 1]?.[0];
}

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

const group: Group = {
  code: "equity-desks",
  title: "Equity desks",
  blocked: false,
  blockReason: "",
  notes: "",
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
  return render(
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
    {
      api: {
        createAdjustment: createAdjustmentMock,
        exportBusinessCsv: exportBusinessCsvMock,
        fetchAccounts: async () => [account],
        fetchAdjustmentsPage: fetchAdjustmentsMock,
        fetchAssets: async () => [{ code: balance.asset, title: balance.asset, assetClass: "" }],
        fetchGroups: async () => [group],
        importBusinessCsv: importBusinessCsvMock,
        previewBusinessCsvImport: previewBusinessCsvImportMock,
      },
    },
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  await i18n.changeLanguage("en");
  useAccountsMock.mockReturnValue(ready<Account[]>([account]));
  useAdjustmentsMock.mockReturnValue(readyPage<Adjustment>([]));
  useBalancesMock.mockReturnValue(readyPage<Balance>([balance]));
  createAdjustmentMock.mockImplementation(async (_account, body) =>
    acceptedAdjustment(body as unknown as Adjustment["request"]),
  );
  exportBusinessCsvMock.mockResolvedValue({
    blob: new Blob(["csv"]),
    filename: "positions.csv",
  });
  fetchAdjustmentsMock.mockResolvedValue({ items: [], total: 0 });
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
    expect(scope.getByText("balance")).toBeInTheDocument();
    expect(scope.getAllByText("600").length).toBeGreaterThan(0);
    expect(scope.queryByText(/Δ600/)).not.toBeInTheDocument();
    expect(createAdjustmentMock).toHaveBeenCalledWith("Bucks McMoneyface", {
      asset: "AAPL",
      balance: { mode: "absolute", value: "600" },
      held: { mode: "absolute", value: "10" },
      incoming: { mode: "absolute", value: "-50" },
      averageEntryPrice: "142.50",
      balanceBounds: { lower: "-100", upper: "1000" },
    });
  });

  it("keeps the entered values and allows resubmit after a successful submit", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.click(
      screen.getByRole("button", {
        name: /open adjustment panel for bucks mcmoneyface aapl/i,
      }),
    );

    const panel = screen.getByRole("region", { name: "Adjustment" });
    const scope = within(panel);

    await user.type(scope.getByLabelText("Available adjustment amount"), "600");
    await user.click(scope.getByRole("button", { name: /submit adjustment/i }));

    await waitFor(() => expect(createAdjustmentMock).toHaveBeenCalledTimes(1));

    // The value stays put — no auto-reset — and the field is still editable.
    const amount = scope.getByLabelText("Available adjustment amount");
    expect(amount).toHaveValue("600");
    expect(amount).not.toBeDisabled();

    // The operator can tweak and submit again without reopening the panel.
    const submit = scope.getByRole("button", { name: /submit adjustment/i });
    expect(submit).toBeEnabled();
    await user.click(submit);
    await waitFor(() => expect(createAdjustmentMock).toHaveBeenCalledTimes(2));
  });

  it("clears a single amount field with its inline reset control", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.click(
      screen.getByRole("button", {
        name: /open adjustment panel for bucks mcmoneyface aapl/i,
      }),
    );

    const scope = within(screen.getByRole("region", { name: "Adjustment" }));
    const amount = scope.getByLabelText("Available adjustment amount");
    await user.type(amount, "600");
    expect(amount).toHaveValue("600");

    // A ✕ reset appears while the field has a value; one click clears it.
    const clears = scope.getAllByRole("button", { name: /clear field/i });
    await user.click(clears[0]!);
    expect(amount).toHaveValue("");
  });

  it("renders the draft row as a single spanning 'New position' cell without a copy button", async () => {
    const user = userEvent.setup();
    renderPositions();

    const draftRow = screen
      .getAllByRole("row")
      .find((r) => r.textContent?.includes("New position"));
    expect(draftRow).toBeDefined();
    // The label lives in one spanning cell; the only control is the adjust edit.
    const cells = within(draftRow!).getAllByRole("cell");
    expect(cells).toHaveLength(2);
    expect(
      within(draftRow!).queryByRole("button", { name: /copy/i }),
    ).not.toBeInTheDocument();
    expect(
      within(draftRow!).getByRole("button", {
        name: /open new adjustment panel/i,
      }),
    ).toBeInTheDocument();
    await user.click(
      within(draftRow!).getByRole("button", {
        name: /open new adjustment panel/i,
      }),
    );
    expect(
      screen.getByRole("region", { name: "Adjustment" }),
    ).toBeInTheDocument();
  });

  it("keeps draft values after submit and clears them with reset all", async () => {
    const user = userEvent.setup();
    renderPositions();

    const draftRow = screen
      .getAllByRole("row")
      .find((r) => r.textContent?.includes("New position"));
    await user.click(
      within(draftRow!).getByRole("button", {
        name: /open new adjustment panel/i,
      }),
    );

    const panel = screen.getByRole("region", { name: "Adjustment" });
    const scope = within(panel);
    const resetAll = scope.getByRole("button", { name: /reset all/i });
    expect(resetAll).toBeDisabled();

    const account = scope.getByLabelText("Account");
    const asset = scope.getByLabelText("Asset");
    const amount = scope.getByLabelText("Available adjustment amount");

    await user.type(account, "my");
    await user.type(asset, "AAPL");
    await user.type(amount, "600");
    expect(resetAll).toBeEnabled();

    await user.click(scope.getByRole("button", { name: /submit adjustment/i }));
    await waitFor(() => expect(createAdjustmentMock).toHaveBeenCalledTimes(1));
    expect(createAdjustmentMock).toHaveBeenCalledWith("my", {
      asset: "AAPL",
      balance: { mode: "absolute", value: "600" },
    });
    expect(account).toHaveValue("my");
    expect(asset).toHaveValue("AAPL");
    expect(amount).toHaveValue("600");

    await user.click(resetAll);
    expect(account).toHaveValue("");
    expect(asset).toHaveValue("");
    expect(amount).toHaveValue("");
    expect(resetAll).toBeDisabled();
    expect(
      scope.getByRole("button", { name: /submit adjustment/i }),
    ).toBeDisabled();
  });

  it("opens the draft panel from the page action with current filters", async () => {
    const user = userEvent.setup();
    renderPositions("/positions?account=Bucks%20McMoneyface");

    await user.type(screen.getByLabelText("Filter by asset"), "AAPL");
    await user.click(screen.getByRole("button", { name: /apply filters/i }));
    await user.click(
      screen.getAllByRole("button", { name: /^adjustment$/i })[0],
    );

    expect(screen.getByLabelText("Account")).toHaveValue("Bucks McMoneyface");
    expect(screen.getByLabelText("Asset")).toHaveValue("AAPL");
  });

  it("does not reset an open draft panel when filters change later", async () => {
    const user = userEvent.setup();
    renderPositions("/positions?account=Bucks%20McMoneyface");

    await user.click(
      screen.getAllByRole("button", { name: /^adjustment$/i })[0],
    );

    const panel = screen.getByRole("region", { name: "Adjustment" });
    const accountInput = within(panel).getByLabelText("Account");
    const assetInput = within(panel).getByLabelText("Asset");
    expect(accountInput).toHaveValue("Bucks McMoneyface");
    expect(assetInput).toHaveValue("");

    await user.type(assetInput, "MSFT");
    await user.clear(screen.getByLabelText("Filter by account"));
    await user.type(screen.getByLabelText("Filter by account"), "desk-beta");
    await user.clear(screen.getByLabelText("Filter by asset"));
    await user.type(screen.getByLabelText("Filter by asset"), "AAPL");
    await user.click(screen.getByRole("button", { name: /apply filters/i }));

    expect(accountInput).toHaveValue("Bucks McMoneyface");
    expect(assetInput).toHaveValue("MSFT");
  });

  it("keeps an open draft panel when a new balance appears after reload", async () => {
    const user = userEvent.setup();
    useBalancesMock.mockReturnValue(readyPage<Balance>([]));
    renderPositions();

    await user.click(
      screen.getAllByRole("button", { name: /^adjustment$/i })[0],
    );

    let panel = screen.getByRole("region", { name: "Adjustment" });
    await user.type(within(panel).getByLabelText("Account"), "my");
    await user.type(within(panel).getByLabelText("Asset"), "AAPL");
    await user.type(
      within(panel).getByLabelText("Available adjustment amount"),
      "600",
    );

    useBalancesMock.mockReturnValue(readyPage<Balance>([balance]));
    await user.type(
      screen.getByLabelText("Filter by asset"),
      "MSFT",
    );

    panel = screen.getByRole("region", { name: "Adjustment" });
    expect(within(panel).getByLabelText("Account")).toHaveValue("my");
    expect(within(panel).getByLabelText("Asset")).toHaveValue("AAPL");
    expect(
      within(panel).getByLabelText("Available adjustment amount"),
    ).toHaveValue("600");
  });

  it("suggests known accounts and assets inside the draft panel", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.click(
      screen.getAllByRole("button", { name: /^adjustment$/i })[0],
    );

    const panel = screen.getByRole("region", { name: "Adjustment" });
    const accountInput = within(panel).getByLabelText("Account");
    const assetInput = within(panel).getByLabelText("Asset");

    await user.clear(accountInput);
    await user.type(accountInput, "Bu");
    await user.click(
      await screen.findByRole("option", { name: "Bucks McMoneyface" }),
    );
    expect(accountInput).toHaveValue("Bucks McMoneyface");

    await user.type(assetInput, "AA");
    await user.click(await screen.findByRole("option", { name: "AAPL" }));
    expect(assetInput).toHaveValue("AAPL");
  });
});

describe("Positions business CSV", () => {
  it("applies advanced position filters only after apply", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /more filters/i });
    await user.type(within(dialog).getAllByPlaceholderText("Value")[0], "10");

    expect(lastBalanceFilters()).not.toEqual(
      expect.objectContaining({ availableMin: "10" }),
    );
    expect(screen.queryByText(/active filters/i)).not.toBeInTheDocument();

    await user.click(
      within(dialog).getByRole("button", {
        name: /apply advanced filter/i,
      }),
    );

    await waitFor(() =>
      expect(lastBalanceFilters()).toEqual(
        expect.objectContaining({
          availableMode: "eq",
          availableMin: "10",
        }),
      ),
    );
    expect(screen.getByText(/active filters/i)).toBeInTheDocument();
    expect(screen.getByText(/available: Equals 10/i)).toBeInTheDocument();
  });

  it("maps less-than position ranges to the max query bound", async () => {
    renderPositions(
      "/positions?availableMode=lt&availableMin=10&availableMax=99",
    );

    await waitFor(() =>
      expect(lastBalanceFilters()).toEqual(
        expect.objectContaining({
          availableMode: "lt",
          availableMax: "10",
        }),
      ),
    );
  });

  it("keeps advanced position filters open when a numeric value is invalid", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /more filters/i });
    const available = within(dialog).getAllByPlaceholderText("Value")[0];
    await user.type(available, "word");

    await user.click(
      within(dialog).getByRole("button", {
        name: /apply advanced filter/i,
      }),
    );

    expect(screen.getByRole("dialog", { name: /more filters/i })).toBeInTheDocument();
    expect(available).toBeInvalid();
    expect(available).toHaveProperty("validationMessage", "Enter a valid number.");
    expect(lastBalanceFilters()).not.toEqual(
      expect.objectContaining({
        availableMode: "eq",
        availableMin: expect.any(String),
      }),
    );
    expect(screen.queryByText(/active filters/i)).not.toBeInTheDocument();
  });

  it("suggests known groups while typing the group filter", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.type(screen.getByLabelText("Filter by group"), "equ");

    await user.click(
      await screen.findByRole("option", { name: "equity-desks" }),
    );
    await waitFor(() =>
      expect(lastBalanceFilters()).toEqual(
        expect.objectContaining({ groupCode: "equity-desks" }),
      ),
    );
    expect(screen.getByText(/active filters/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /clear all filters/i }));
    expect(screen.getByLabelText("Filter by group")).toHaveValue("");
    expect(screen.queryByText(/active filters/i)).not.toBeInTheDocument();
  });

  it("applies identity position filters when Enter is pressed", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.type(screen.getByLabelText("Filter by account"), "desk-alpha");
    expect(lastBalanceFilters()).not.toEqual(
      expect.objectContaining({ account: "desk-alpha" }),
    );
    await user.keyboard("{Enter}");

    await waitFor(() =>
      expect(lastBalanceFilters()).toEqual(
        expect.objectContaining({ account: "desk-alpha" }),
      ),
    );
    expect(screen.getByText(/active filters/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /clear all filters/i }));
    expect(screen.getByLabelText("Filter by account")).toHaveValue("");
    expect(screen.queryByText(/active filters/i)).not.toBeInTheDocument();
  });

  it("exports positions with account, group, and asset filters", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.type(screen.getByLabelText("Filter by account"), "desk-alpha");
    await user.type(screen.getByLabelText("Filter by group"), "equity-desks");
    await user.type(screen.getByLabelText("Filter by asset"), "AAPL");
    await user.click(screen.getByRole("button", { name: /apply filters/i }));
    await user.click(screen.getByRole("button", { name: /export positions csv/i }));
    await user.click(screen.getByRole("button", { name: /^export$/i }));

    await waitFor(() => expect(exportBusinessCsvMock).toHaveBeenCalledTimes(1));
    expect(exportBusinessCsvMock).toHaveBeenCalledWith(
      expect.objectContaining({
        entity: "positions",
        filters: {
          account: "desk-alpha",
          asset: "AAPL",
          groupCode: "equity-desks",
        },
      }),
    );
  });

  it("exports adjustment history with the selected source filter", async () => {
    const user = userEvent.setup();
    useAdjustmentsMock.mockReturnValue(
      readyPage<Adjustment>([
        acceptedAdjustment({
          asset: "AAPL",
          balance: { mode: "absolute", value: "10" },
        }),
      ]),
    );
    renderPositions("/positions?tab=history&source=mcp");

    await user.click(screen.getByRole("button", { name: /export history csv/i }));

    await waitFor(() => expect(fetchAdjustmentsMock).toHaveBeenCalledTimes(1));
    expect(fetchAdjustmentsMock).toHaveBeenCalledWith({
      source: "mcp",
      limit: 1000,
      account: undefined,
      asset: undefined,
    });
  });

  it("imports positions and reloads balances plus history after success", async () => {
    const user = userEvent.setup();
    const balancesReload = vi.fn();
    const adjustmentsReload = vi.fn();
    useBalancesMock.mockReturnValue({
      load: { state: "ready", data: { items: [balance], total: 1 }, error: null },
      reload: balancesReload,
    });
    useAdjustmentsMock.mockReturnValue({
      load: { state: "ready", data: { items: [], total: 0 }, error: null },
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

/** Find the balance data row for the seeded account/asset by its text. */
function balanceRow(): HTMLElement {
  const row = screen
    .getAllByRole("row")
    .find(
      (r) =>
        r.textContent?.includes("Bucks McMoneyface") &&
        r.textContent?.includes("AAPL"),
    );
  if (!row) {
    throw new Error("balance row not found");
  }
  return row;
}

describe("Positions balance row asset cell", () => {
  it("keeps the account copy button but drops it from the asset cell", () => {
    renderPositions();

    const row = balanceRow();
    const scope = within(row);
    // The account keeps its inline copy control; the asset cell no longer does.
    const copyButtons = scope.getAllByRole("button", { name: /copy/i });
    expect(copyButtons).toHaveLength(1);
    // The asset filter action is still present.
    expect(
      scope.getByRole("button", { name: /filter by aapl/i }),
    ).toBeInTheDocument();
  });
});

describe("Positions active-orders navigation", () => {
  it("shows the control only for the non-zero incoming balance", () => {
    // The seeded balance has held "0" and incoming "500", so exactly one
    // active-orders control renders (on the incoming cell).
    renderPositions();

    const row = balanceRow();
    expect(
      within(row).getAllByRole("button", {
        name: /active orders for Bucks McMoneyface/i,
      }),
    ).toHaveLength(1);
  });

  it("opens Orders pre-filtered to the account and active statuses in a new tab", () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    renderPositions();

    const row = balanceRow();
    const [button] = within(row).getAllByRole("button", {
      name: /active orders for Bucks McMoneyface/i,
    });
    // Modifier-click opens the deep link in a new tab, exactly like the other
    // cross-screen row-action buttons.
    fireEvent.click(button, { metaKey: true });

    expect(open).toHaveBeenCalledWith(
      expect.stringContaining(
        "/orders?account=Bucks+McMoneyface&status=submitted%2Caccepted%2Cpartially_filled",
      ),
      "_blank",
      "noopener,noreferrer",
    );
    open.mockRestore();
  });

  it("hides the control for zero held and incoming balances", () => {
    useBalancesMock.mockReturnValue(
      readyPage<Balance>([{ ...balance, held: "0", incoming: "0.00" }]),
    );
    renderPositions();

    const row = balanceRow();
    expect(
      within(row).queryByRole("button", { name: /active orders/i }),
    ).not.toBeInTheDocument();
  });
});

describe("Positions history row actions", () => {
  it("keeps external id separate and filters history by row account or asset", async () => {
    const user = userEvent.setup();
    useAdjustmentsMock.mockReturnValue(
      readyPage<Adjustment>([
        acceptedAdjustment({
          asset: "AAPL",
          balance: { mode: "absolute", value: "10" },
        }),
      ]),
    );
    renderPositions("/positions?tab=history");

    expect(
      screen.getByRole("columnheader", { name: "ID" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("columnheader", { name: "Actions" }),
    ).not.toBeInTheDocument();
    const row = screen
      .getAllByRole("row")
      .find(
        (candidate) =>
          candidate.textContent?.includes("Bucks McMoneyface") &&
          candidate.textContent?.includes("AAPL"),
    );
    expect(row).toBeDefined();
    const scope = within(row as HTMLElement);
    expect(scope.getByText(/balance 600/)).toBeInTheDocument();
    expect(scope.queryByText(/balance Δ/)).not.toBeInTheDocument();

    await user.click(
      scope.getByRole("button", { name: /filter by bucks mcmoneyface/i }),
    );
    expect(screen.getByLabelText("Filter by account")).toHaveValue(
      "Bucks McMoneyface",
    );

    await user.click(scope.getByRole("button", { name: /filter by aapl/i }));
    expect(screen.getByLabelText("Filter by asset")).toHaveValue("AAPL");
  });
});

describe("Positions row navigation to history", () => {
  it("opens the history tab pre-filtered by the row account and asset", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.click(balanceRow());

    // History tab is now active: its source control (history-only) is shown.
    expect(
      await screen.findByText("Adjustment history"),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Filter by account")).toHaveValue(
      "Bucks McMoneyface",
    );
    expect(screen.getByLabelText("Filter by asset")).toHaveValue("AAPL");
  });

  it("does not navigate when the adjust control is used", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.click(
      screen.getByRole("button", {
        name: /open adjustment panel for bucks mcmoneyface aapl/i,
      }),
    );

    // The adjust panel opens in place; the history view did not take over.
    expect(
      screen.getByRole("region", { name: "Adjustment" }),
    ).toBeInTheDocument();
    expect(screen.queryByText("Adjustment history")).not.toBeInTheDocument();
  });
});

describe("Positions share filter set", () => {
  it("copies a deep link that encodes the active filters", async () => {
    const user = userEvent.setup();
    // Install the spy after userEvent.setup so its own clipboard stub does not
    // shadow it; the share button click uses fireEvent for the same reason.
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    renderPositions();

    await user.type(screen.getByLabelText("Filter by account"), "desk-alpha");
    await user.type(screen.getByLabelText("Filter by asset"), "AAPL");
    await user.click(screen.getByRole("button", { name: /apply filters/i }));
    // fireEvent (not userEvent) so the user-event clipboard stub does not
    // shadow the writeText spy asserted below.
    fireEvent.click(
      screen.getByRole("button", {
        name: /copy a link to the current filter set/i,
      }),
    );

    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    const url = new URL(writeText.mock.calls[0][0]);
    expect(url.pathname).toBe("/positions");
    expect(url.searchParams.get("account")).toBe("desk-alpha");
    expect(url.searchParams.get("asset")).toBe("AAPL");
    expect(url.searchParams.get("sort")).toBe("account");
    expect(url.searchParams.get("order")).toBe("asc");
    // No non-default range or tab params leak into the clean link.
    expect(url.searchParams.get("tab")).toBeNull();
    expect(url.searchParams.get("availableMode")).toBeNull();
  });

  it("restores position sort from query params on mount", async () => {
    renderPositions("/positions?sort=available&order=desc");

    await waitFor(() =>
      expect(lastBalanceFilters()).toEqual(
        expect.objectContaining({ sort: "available", order: "desc" }),
      ),
    );
  });

  it("restores the filter set from query params on mount", async () => {
    renderPositions(
      "/positions?tab=history&account=desk-alpha&asset=AAPL&status=rejected&source=mcp&sort=status&order=asc",
    );

    // The history tab is active and its filters are seeded.
    expect(screen.getByLabelText("Filter by account")).toHaveValue("desk-alpha");
    expect(screen.getByLabelText("Filter by asset")).toHaveValue("AAPL");
    await waitFor(() => {
      const filter =
        useAdjustmentsMock.mock.calls[
          useAdjustmentsMock.mock.calls.length - 1
        ]?.[0];
      expect(filter).toMatchObject({
        account: "desk-alpha",
        order: "asc",
        status: "rejected",
        sort: "status",
        source: "mcp",
      });
    });
  });
});
