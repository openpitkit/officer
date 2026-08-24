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

import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithApi as render } from "@/test/apiClient";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter, useLocation } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type {
  Account,
  Adjustment,
  Balance,
  BalanceListFilters,
  BusinessCsvEntity,
  BusinessCsvExportFilters,
  Group,
} from "@/api/types";
import type { PollingResult } from "@/api/usePolling";
import { useAccounts } from "@/api/useAccounts";
import { useAdjustmentsPage } from "@/api/useAdjustments";
import { useBalancesPage } from "@/api/useBalances";
import { SidebarProvider } from "@/components/SidebarContext";
import { ApiError, MAX_LIST_LIMIT } from "@/framework";
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
  const actual = await vi.importActual<
    typeof import("@/components/TableControls")
  >("@/components/TableControls");
  const dialogs = await vi.importActual<
    typeof import("@/components/BusinessCsvDialogs")
  >("@/components/BusinessCsvDialogs");
  return {
    ...actual,
    CsvTransferMenu: ({
      exports,
    }: {
      exports?: {
        entity: BusinessCsvEntity;
        filters?: BusinessCsvExportFilters;
        label: string;
      }[];
    }) => (
      <>
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

function readyPage<T>(
  items: T[],
): PollingResult<{ items: T[]; total: number }> {
  return ready({ items, total: items.length });
}

const useAccountsMock = vi.mocked(useAccounts);
const useAdjustmentsMock = vi.mocked(useAdjustmentsPage);
const useBalancesMock = vi.mocked(useBalancesPage);
const createAdjustmentMock = vi.fn();
const setBalanceRealizedPnlMock = vi.fn();
const exportBusinessCsvMock = vi.fn();
const fetchAdjustmentsMock = vi.fn();
const fetchAssetsMock = vi.fn();

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
  blockSource: "none",
  accountBlocked: false,
  accountBlockReason: "",
  groupBlocked: false,
  groupBlockReason: "",
  group: "",
  notes: "",
  pnlHaltReason: "",
};

const balance: Balance = {
  account: "Bucks McMoneyface",
  asset: "AAPL",
  available: "0",
  held: "0",
  incoming: "500",
  averageEntryPrice: "",
  realizedPnl: "0",
  realizedPnlHaltReason: "",
  accountCurrency: "USD",
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
  const accepted: NonNullable<Adjustment["accepted"]> = {
    balanceDelta: "600",
    balanceResult: "600",
    heldDelta: "10",
    heldResult: "10",
    incomingDelta: "-50",
    incomingResult: "450",
  };
  if (request.realizedPnl) {
    accepted.realizedPnlResult = request.realizedPnl;
  }
  return {
    id: "adj-alpha-10",
    account: "Bucks McMoneyface",
    at: "2026-06-24T16:42:00Z",
    source: "panel",
    asset: request.asset,
    status: "accepted",
    request,
    accepted,
  };
}

function acceptedHaltedAdjustment(
  request: Adjustment["request"],
  reason: string,
): Adjustment {
  const adjustment = acceptedAdjustment(request);
  return {
    ...adjustment,
    accepted: {
      ...adjustment.accepted!,
      realizedPnlHaltReason: reason,
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
            <LocationProbe />
          </MemoryRouter>
        </DisplayPreferencesProvider>
      </ThemeProvider>
    </I18nextProvider>,
    {
      api: {
        createAdjustment: createAdjustmentMock,
        setBalanceRealizedPnl: setBalanceRealizedPnlMock,
        exportBusinessCsv: exportBusinessCsvMock,
        fetchAccounts: async () => [account],
        fetchAdjustmentsPage: fetchAdjustmentsMock,
        fetchAssets: fetchAssetsMock,
        fetchGroups: async () => [group],
      },
    },
  );
}

function LocationProbe() {
  const { pathname, search } = useLocation();
  return (
    <output
      data-testid="location"
      data-pathname={pathname}
      data-search={search}
    />
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
  setBalanceRealizedPnlMock.mockImplementation(
    async (_account, body: { asset: string; realizedPnl: string }) => ({
      ...balance,
      asset: body.asset,
      realizedPnl: body.realizedPnl,
    }),
  );
  exportBusinessCsvMock.mockResolvedValue({
    blob: new Blob(["csv"]),
    filename: "positions.csv",
  });
  fetchAssetsMock.mockResolvedValue([
    { code: balance.asset, title: balance.asset, assetClass: "" },
  ]);
  fetchAdjustmentsMock.mockResolvedValue({ items: [], total: 0 });
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
  it("cancels after closing an open identity autocomplete", async () => {
    const user = userEvent.setup();
    renderPositions();

    const draftRow = screen
      .getAllByRole("row")
      .find((row) => row.textContent?.includes("New position"));
    const openPanel = () =>
      user.click(
        within(draftRow!).getByRole("button", {
          name: /open new adjustment panel/i,
        }),
      );

    await openPanel();
    let panel = screen.getByRole("region", { name: "Adjustment" });
    const accountInput = within(panel).getByLabelText("Account");
    await user.type(accountInput, "B");
    await screen.findByRole("option", { name: "Bucks McMoneyface" });
    expect(accountInput).toHaveAttribute("aria-expanded", "true");

    await user.keyboard("{Escape}");
    expect(panel).toBeInTheDocument();
    expect(accountInput).toHaveAttribute("aria-expanded", "false");

    await user.keyboard("{Escape}");
    expect(
      screen.queryByRole("region", { name: "Adjustment" }),
    ).not.toBeInTheDocument();

    await openPanel();
    panel = screen.getByRole("region", { name: "Adjustment" });
    const bounds = within(panel).getByRole("button", {
      name: /bounds \(optional\)/i,
    });
    await user.click(bounds);
    bounds.focus();
    await user.keyboard("{Escape}");
    expect(
      screen.queryByRole("region", { name: "Adjustment" }),
    ).not.toBeInTheDocument();

    await openPanel();
    panel = screen.getByRole("region", { name: "Adjustment" });
    await user.click(within(panel).getByRole("button", { name: "Cancel" }));
    expect(
      screen.queryByRole("region", { name: "Adjustment" }),
    ).not.toBeInTheDocument();
    expect(createAdjustmentMock).not.toHaveBeenCalled();
  });

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
    await user.type(
      scope.getByLabelText("Average entry price (optional)"),
      "142.50",
    );
    await user.type(scope.getByLabelText("Realized PnL"), "-12.50");
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
    expect(createAdjustmentMock).toHaveBeenCalledWith(
      "Bucks McMoneyface",
      {
        asset: "AAPL",
        balance: { mode: "absolute", value: "600" },
        held: { mode: "absolute", value: "10" },
        incoming: { mode: "absolute", value: "-50" },
        averageEntryPrice: "142.50",
        realizedPnl: "-12.50",
        balanceBounds: { lower: "-100", upper: "1000" },
      },
      "reject",
    );
    expect(setBalanceRealizedPnlMock).not.toHaveBeenCalled();
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

    // The value stays put - no auto-reset - and the field is still editable.
    const amount = scope.getByLabelText("Available adjustment amount");
    expect(amount).toHaveValue("600");
    expect(amount).not.toBeDisabled();

    // The operator can tweak and submit again without reopening the panel.
    const submit = scope.getByRole("button", { name: /submit adjustment/i });
    expect(submit).toBeEnabled();
    await user.click(submit);
    await waitFor(() => expect(createAdjustmentMock).toHaveBeenCalledTimes(2));
  });

  it("disables adjustment submission while a numeric draft is invalid", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.click(
      screen.getByRole("button", {
        name: /open adjustment panel for bucks mcmoneyface aapl/i,
      }),
    );

    const scope = within(screen.getByRole("region", { name: "Adjustment" }));
    const amount = scope.getByLabelText("Available adjustment amount");
    const submit = scope.getByRole("button", { name: /submit adjustment/i });
    await user.type(amount, "600");
    expect(submit).toBeEnabled();

    await user.type(amount, "x");
    expect(amount).toHaveValue("600x");
    expect(amount).toBeInvalid();
    expect(submit).toBeDisabled();
    expect(createAdjustmentMock).not.toHaveBeenCalled();
  });

  it("submits realized PnL through one adjustment request", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.click(
      screen.getByRole("button", {
        name: /open adjustment panel for bucks mcmoneyface aapl/i,
      }),
    );

    const panel = screen.getByRole("region", { name: "Adjustment" });
    const scope = within(panel);
    await user.type(scope.getByLabelText("Realized PnL"), "-12.50");
    await user.click(scope.getByRole("button", { name: /submit adjustment/i }));

    await waitFor(() => expect(createAdjustmentMock).toHaveBeenCalledTimes(1));
    expect(createAdjustmentMock).toHaveBeenCalledWith(
      "Bucks McMoneyface",
      {
        asset: "AAPL",
        realizedPnl: "-12.50",
      },
      "reject",
    );
    expect(setBalanceRealizedPnlMock).not.toHaveBeenCalled();
    expect(scope.getByText("realized PnL")).toBeInTheDocument();
    expect(scope.getAllByText("-12.50").length).toBeGreaterThan(0);
  });

  it("shows an accepted PnL halt without a numeric result", async () => {
    const user = userEvent.setup();
    createAdjustmentMock.mockImplementationOnce(async (_account, body) =>
      acceptedHaltedAdjustment(
        body as unknown as Adjustment["request"],
        "missing_initial_pnl",
      ),
    );
    renderPositions();

    await user.click(
      screen.getByRole("button", {
        name: /open adjustment panel for bucks mcmoneyface aapl/i,
      }),
    );

    const scope = within(screen.getByRole("region", { name: "Adjustment" }));
    await user.type(
      scope.getByLabelText("Average entry price (optional)"),
      "142.50",
    );
    await user.click(scope.getByRole("button", { name: /submit adjustment/i }));

    const warning = await scope.findByRole("note", {
      name: /authoritative initial PnL value is unavailable/i,
    });
    expect(warning.parentElement).toHaveClass(
      "border-[var(--warn)]",
      "bg-[var(--warn-dim)]",
    );
    expect(warning.parentElement).not.toHaveClass("border-[var(--ok)]");
    expect(
      within(warning.parentElement as HTMLElement).queryByText(
        /^realized PnL$/i,
      ),
    ).not.toBeInTheDocument();
  });

  it("does not reapply a delta adjustment through a second realized-PnL call on retry", async () => {
    const user = userEvent.setup();
    const appliedDeltas: string[] = [];
    createAdjustmentMock
      .mockRejectedValueOnce(new Error("temporary failure"))
      .mockImplementationOnce(async (_account, body) => {
        const request = body as unknown as Adjustment["request"];
        if (request.balance?.mode === "delta") {
          appliedDeltas.push(request.balance.value);
        }
        return acceptedAdjustment(request);
      });

    renderPositions();

    await user.click(
      screen.getByRole("button", {
        name: /open adjustment panel for bucks mcmoneyface aapl/i,
      }),
    );

    const panel = screen.getByRole("region", { name: "Adjustment" });
    const scope = within(panel);
    await user.click(scope.getByLabelText("Available adjustment mode"));
    await user.click(screen.getByRole("option", { name: "delta" }));
    await user.type(scope.getByLabelText("Available adjustment amount"), "25");
    await user.type(scope.getByLabelText("Realized PnL"), "-12.50");

    const submit = scope.getByRole("button", { name: /submit adjustment/i });
    await user.click(submit);
    expect(await scope.findByText("temporary failure")).toBeInTheDocument();

    await user.click(submit);
    await waitFor(() => expect(createAdjustmentMock).toHaveBeenCalledTimes(2));
    expect(setBalanceRealizedPnlMock).not.toHaveBeenCalled();
    expect(appliedDeltas).toEqual(["25"]);
    expect(createAdjustmentMock).toHaveBeenLastCalledWith(
      "Bucks McMoneyface",
      {
        asset: "AAPL",
        balance: { mode: "delta", value: "25" },
        realizedPnl: "-12.50",
      },
      "reject",
    );
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

    // A clear control appears while the field has a value; one click clears it.
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

  it("requires a non-zero amount before creating a position", async () => {
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

    const scope = within(screen.getByRole("region", { name: "Adjustment" }));
    await user.type(scope.getByLabelText("Account"), "my");
    await user.type(scope.getByLabelText("Asset"), "AAPL");
    const average = scope.getByLabelText(/average entry price/i);
    await user.type(average, "150");

    const submit = scope.getByRole("button", { name: /submit adjustment/i });
    expect(submit).toBeDisabled();
    await user.keyboard("{Enter}");
    expect(
      scope.getByText(
        "Enter a non-zero Available, Held, or Incoming value to create a position.",
      ),
    ).toBeInTheDocument();
    expect(createAdjustmentMock).not.toHaveBeenCalled();

    const amount = scope.getByLabelText("Available adjustment amount");
    await user.type(amount, "0");
    expect(submit).toBeDisabled();
    await user.clear(amount);
    await user.type(amount, "1");
    expect(submit).toBeEnabled();
  });

  it("reports an invalid amount before the new-position amount rule", async () => {
    const user = userEvent.setup();
    renderPositions();

    const draftRow = screen
      .getAllByRole("row")
      .find((row) => row.textContent?.includes("New position"));
    await user.click(
      within(draftRow!).getByRole("button", {
        name: /open new adjustment panel/i,
      }),
    );

    const scope = within(screen.getByRole("region", { name: "Adjustment" }));
    await user.type(scope.getByLabelText("Account"), "my");
    await user.type(scope.getByLabelText("Asset"), "AAPL");
    await user.type(scope.getByLabelText(/average entry price/i), "150");
    await user.type(
      scope.getByLabelText("Available adjustment amount"),
      "invalid",
    );

    await user.keyboard("{Enter}");
    expect(scope.getByRole("alert")).toHaveTextContent("Invalid decimal");
    expect(
      scope.queryByText(
        "Enter a non-zero Available, Held, or Incoming value to create a position.",
      ),
    ).not.toBeInTheDocument();
    expect(createAdjustmentMock).not.toHaveBeenCalled();
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
    expect(createAdjustmentMock).toHaveBeenCalledWith(
      "my",
      {
        asset: "AAPL",
        balance: { mode: "absolute", value: "600" },
      },
      "reject",
    );
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

  it("offers to create a missing account from the new-position adjustment", async () => {
    const user = userEvent.setup();
    createAdjustmentMock
      .mockRejectedValueOnce(
        new ApiError(
          'adjustment: account "fresh-account" does not exist',
          "account_missing",
          404,
          undefined,
          "fresh-account",
        ),
      )
      .mockImplementationOnce(async (_account, body) =>
        acceptedAdjustment(body as unknown as Adjustment["request"]),
      );
    renderPositions();

    const draftRow = screen
      .getAllByRole("row")
      .find((row) => row.textContent?.includes("New position"));
    await user.click(
      within(draftRow!).getByRole("button", {
        name: /open new adjustment panel/i,
      }),
    );
    const scope = within(screen.getByRole("region", { name: "Adjustment" }));
    await user.type(scope.getByLabelText("Account"), "fresh-account");
    await user.type(scope.getByLabelText("Asset"), "AAPL");
    await user.type(scope.getByLabelText("Available adjustment amount"), "10");
    await user.click(scope.getByRole("button", { name: /submit adjustment/i }));

    const confirmDialog = await screen.findByRole("alertdialog", {
      name: "Account does not exist",
    });
    expect(createAdjustmentMock).toHaveBeenNthCalledWith(
      1,
      "fresh-account",
      {
        asset: "AAPL",
        balance: { mode: "absolute", value: "10" },
      },
      "reject",
    );

    await user.click(
      within(confirmDialog).getByRole("button", {
        name: "Create account and continue",
      }),
    );
    await waitFor(() => expect(createAdjustmentMock).toHaveBeenCalledTimes(2));
    expect(createAdjustmentMock).toHaveBeenNthCalledWith(
      2,
      "fresh-account",
      {
        asset: "AAPL",
        balance: { mode: "absolute", value: "10" },
      },
      "create",
    );
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
    await user.type(screen.getByLabelText("Filter by asset"), "MSFT");

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

  it("reports an asset-suggestion lookup failure until a later lookup succeeds", async () => {
    const user = userEvent.setup();
    const error = new Error("asset lookup failed");
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => {});
    fetchAssetsMock.mockRejectedValueOnce(error);
    renderPositions();

    await user.click(
      screen.getAllByRole("button", { name: /^adjustment$/i })[0],
    );
    const panel = screen.getByRole("region", { name: "Adjustment" });
    const asset = within(panel).getByLabelText("Asset");
    await user.type(asset, "AA");

    await waitFor(() => expect(consoleError).toHaveBeenCalledWith(error));
    expect(
      await within(panel).findByText("Asset suggestions could not be loaded."),
    ).toBeInTheDocument();

    await user.type(asset, "P");
    await waitFor(() =>
      expect(
        within(panel).queryByText("Asset suggestions could not be loaded."),
      ).not.toBeInTheDocument(),
    );
    consoleError.mockRestore();
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

  it("disables advanced position filters while a numeric value is invalid", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /more filters/i });
    const available = within(dialog).getAllByPlaceholderText("Value")[0];
    await user.type(available, "word");

    expect(
      within(dialog).getByRole("button", {
        name: /apply advanced filter/i,
      }),
    ).toBeDisabled();

    expect(
      screen.getByRole("dialog", { name: /more filters/i }),
    ).toBeInTheDocument();
    expect(available).toBeInvalid();
    expect(available).toHaveProperty(
      "validationMessage",
      "Enter a valid number.",
    );
    expect(lastBalanceFilters()).not.toEqual(
      expect.objectContaining({
        availableMode: "eq",
        availableMin: expect.any(String),
      }),
    );
    expect(screen.queryByText(/active filters/i)).not.toBeInTheDocument();
  });

  // Value inputs share the "Value" placeholder, so the advanced dialog's rows
  // are addressed by their order: available, held, incoming, avg entry price,
  // realized PnL, updated.
  it.each([
    [
      "avg entry price",
      3,
      /currency for the avg entry price threshold/i,
      {
        averageEntryPriceMode: "eq",
        averageEntryPriceMin: "10",
        averageEntryPriceCurrency: "USD",
      },
    ],
    [
      "realized pnl",
      4,
      /currency for the realized pnl threshold/i,
      {
        realizedPnlMode: "eq",
        realizedPnlMin: "10",
        realizedPnlCurrency: "USD",
      },
    ],
  ])(
    "blocks a %s threshold until it names a currency, then sends both",
    async (_label, valueIndex, currencyLabel, expected) => {
      const user = userEvent.setup();
      renderPositions();

      await user.click(screen.getByRole("button", { name: /more filters/i }));
      const dialog = screen.getByRole("dialog", { name: /more filters/i });
      const field = within(dialog).getAllByPlaceholderText("Value")[valueIndex];
      await user.type(field, "10");

      // The threshold is set but carries no unit: it cannot be compared
      // against a column each account keeps in its own currency.
      const currency = within(dialog).getByRole("combobox", {
        name: currencyLabel,
      });
      expect(currency).toBeInvalid();
      expect(
        within(dialog).getByText(
          /select the currency the threshold is given in/i,
        ),
      ).toBeInTheDocument();
      expect(
        within(dialog).getByRole("button", { name: /apply advanced filter/i }),
      ).toBeDisabled();

      await user.type(currency, "USD");

      expect(currency).not.toBeInvalid();
      expect(
        within(dialog).queryByText(
          /Only positions held in this account currency are compared/i,
        ),
      ).not.toBeInTheDocument();
      await user.click(
        within(dialog).getByRole("button", { name: /apply advanced filter/i }),
      );
      await waitFor(() =>
        expect(lastBalanceFilters()).toEqual(expect.objectContaining(expected)),
      );
    },
  );

  it("drops a denominated threshold without its currency from a shared link", async () => {
    // A link can carry a threshold whose currency was dropped. Ignore it
    // completely rather than showing a condition the backend never receives.
    renderPositions("/positions?realizedPnlMode=gt&realizedPnlMin=50");

    await waitFor(() => expect(lastBalanceFilters()).toBeDefined());
    expect(lastBalanceFilters()).not.toEqual(
      expect.objectContaining({ realizedPnlMode: "gt" }),
    );
    expect(lastBalanceFilters()).not.toEqual(
      expect.objectContaining({ realizedPnlMin: expect.any(String) }),
    );
    expect(
      screen.queryByText(/realized pnl: Greater than 50/i),
    ).not.toBeInTheDocument();
  });

  it("restores a denominated threshold and its currency from a shared link", async () => {
    renderPositions(
      "/positions?realizedPnlMode=gt&realizedPnlMin=50&realizedPnlCurrency=EUR",
    );

    await waitFor(() =>
      expect(lastBalanceFilters()).toEqual(
        expect.objectContaining({
          realizedPnlMode: "gt",
          realizedPnlMin: "50",
          realizedPnlCurrency: "EUR",
        }),
      ),
    );
    expect(
      screen.getByText(/realized pnl: Greater than 50 EUR/i),
    ).toBeInTheDocument();
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
    await user.click(
      screen.getByRole("button", { name: /clear all filters/i }),
    );
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
    await user.click(
      screen.getByRole("button", { name: /clear all filters/i }),
    );
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
    await user.click(
      screen.getByRole("button", { name: /export positions csv/i }),
    );
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

    await user.click(
      screen.getByRole("button", { name: /export history csv/i }),
    );

    await waitFor(() => expect(fetchAdjustmentsMock).toHaveBeenCalledTimes(1));
    expect(fetchAdjustmentsMock).toHaveBeenCalledWith({
      source: "mcp",
      limit: MAX_LIST_LIMIT,
      account: undefined,
      asset: undefined,
    });
    const blob = vi.mocked(URL.createObjectURL).mock.calls.at(-1)?.[0];
    if (!(blob instanceof Blob)) {
      throw new Error("history export did not create a CSV blob");
    }
    const body = await new Promise<string>((resolve, reject) => {
      const reader = new FileReader();
      reader.addEventListener("load", () => resolve(String(reader.result)));
      reader.addEventListener("error", () => reject(reader.error));
      reader.readAsText(blob);
    });
    expect(body).toMatch(/^id,at,account,asset,/);
  });

  // A cell the operator's spreadsheet would execute on open is neutralized with
  // the "treat as text" apostrophe, exactly as the server-side export does.
  // Decimal amounts are exempt, or every negative balance in the file would
  // become text the operator cannot sum.
  it("neutralizes spreadsheet formulas in the history CSV but not decimals", async () => {
    const user = userEvent.setup();
    fetchAdjustmentsMock.mockResolvedValue({
      items: [
        {
          id: "adj-formula",
          account: "Bucks McMoneyface",
          at: "2026-06-24T16:42:00Z",
          source: "panel",
          asset: "AAPL",
          status: "rejected",
          request: {
            asset: "AAPL",
            balance: { mode: "absolute", value: "-10.5" },
          },
          rejected: {
            code: "balance_bound",
            reason: "=cmd|calc!A1",
            details: "@SUM(1)",
          },
        },
      ],
      total: 1,
    });
    renderPositions("/positions?tab=history");

    await user.click(
      screen.getByRole("button", { name: /export history csv/i }),
    );

    await waitFor(() => expect(fetchAdjustmentsMock).toHaveBeenCalledTimes(1));
    const blob = vi.mocked(URL.createObjectURL).mock.calls.at(-1)?.[0];
    if (!(blob instanceof Blob)) {
      throw new Error("history export did not create a CSV blob");
    }
    const body = await new Promise<string>((resolve, reject) => {
      const reader = new FileReader();
      reader.addEventListener("load", () => resolve(String(reader.result)));
      reader.addEventListener("error", () => reject(reader.error));
      reader.readAsText(blob);
    });
    expect(body).toContain("'=cmd|calc!A1");
    expect(body).toContain("'@SUM(1)");
    expect(body).toContain(",-10.5,");
    expect(body).not.toContain("'-10.5");
  });
});

/** Find a balance data row by its account, defaulting to the seeded one. */
function balanceRow(account = "Bucks McMoneyface"): HTMLElement {
  const row = screen
    .getAllByRole("row")
    .find(
      (r) =>
        r.textContent?.includes(account) && r.textContent?.includes("AAPL"),
    );
  if (!row) {
    throw new Error(`balance row not found: ${account}`);
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

  it("renders a focusable realized-PnL halt warning", () => {
    useBalancesMock.mockReturnValue(
      readyPage([
        {
          ...balance,
          realizedPnlHaltReason: "arithmetic_overflow",
        },
      ]),
    );

    renderPositions();

    const warning = within(balanceRow()).getByRole("note", {
      name: /exact arithmetic exceeded the supported numeric range/i,
    });
    expect(warning).toHaveAttribute("tabindex", "0");
  });
});

describe("Positions account orders navigation", () => {
  it("opens all orders filtered to the balance account", async () => {
    const user = userEvent.setup();
    renderPositions();

    const button = within(balanceRow()).getByRole("button", {
      name: /open orders for Bucks McMoneyface/i,
    });
    await user.click(button);

    const location = screen.getByTestId("location");
    expect(location).toHaveAttribute("data-pathname", "/orders");
    expect(
      new URLSearchParams(location.getAttribute("data-search") ?? "").get(
        "account",
      ),
    ).toBe("Bucks McMoneyface");
  });

  it("opens the account orders link in a new tab with a modifier click", () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    renderPositions();

    const button = within(balanceRow()).getByRole("button", {
      name: /open orders for Bucks McMoneyface/i,
    });
    fireEvent.click(button, { metaKey: true });

    expect(open).toHaveBeenCalledWith(
      expect.stringContaining("/orders?account=Bucks+McMoneyface"),
      "_blank",
      "noopener,noreferrer",
    );
    open.mockRestore();
  });
});

describe("Positions value denomination", () => {
  it("does not offer sorting for account-currency values", () => {
    renderPositions();

    expect(
      screen.queryByRole("button", { name: "Sort by Avg entry price" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Sort by Realized PnL" }),
    ).not.toBeInTheDocument();
  });

  // The case the account currency exists for: one asset traded against several
  // quote assets. No single fill's quote asset denominates the slot, so the
  // account currency is what the table must show.
  it("labels avg entry price and realized PnL with the account currency", () => {
    useBalancesMock.mockReturnValue(
      readyPage<Balance>([
        {
          ...balance,
          averageEntryPrice: "185.25",
          realizedPnl: "42.10",
          accountCurrency: "USD",
        },
      ]),
    );
    renderPositions();

    const row = balanceRow();
    const scope = within(row);
    expect(scope.getByText("185.25")).toBeInTheDocument();
    expect(scope.getByText("42.10")).toBeInTheDocument();
    // One unit label per denominated value, and it is the account currency -
    // not the position asset and not any fill's quote asset.
    expect(scope.getAllByText("USD")).toHaveLength(2);
    expect(scope.queryByText("EUR")).not.toBeInTheDocument();
  });

  it("shows the same figures under each account's own currency", () => {
    useBalancesMock.mockReturnValue(
      readyPage<Balance>([
        {
          ...balance,
          account: "usd-desk",
          realizedPnl: "100",
          accountCurrency: "USD",
        },
        {
          ...balance,
          account: "eur-desk",
          realizedPnl: "100",
          accountCurrency: "EUR",
        },
      ]),
    );
    renderPositions();

    // Equal numbers, different units: the reader can tell they are not
    // comparable without being told the currency.
    expect(within(balanceRow("usd-desk")).getByText("USD")).toBeInTheDocument();
    expect(within(balanceRow("eur-desk")).getByText("EUR")).toBeInTheDocument();
  });

  it("marks a value whose account sets no currency as having no unit", () => {
    useBalancesMock.mockReturnValue(
      readyPage<Balance>([
        { ...balance, realizedPnl: "42.10", accountCurrency: "" },
      ]),
    );
    renderPositions();

    const scope = within(balanceRow());
    expect(scope.getByText("42.10")).toBeInTheDocument();
    expect(scope.queryByText("USD")).not.toBeInTheDocument();
    expect(scope.getByTitle(/account sets no currency/i)).toBeInTheDocument();
  });

  // A halted P&L and a denominated one are shown by the same cell. The halt
  // wins: there is no current number to label. The average entry price is a
  // separate value and keeps its unit regardless.
  it("labels no unit on a halted PnL but still denominates avg entry price", () => {
    useBalancesMock.mockReturnValue(
      readyPage<Balance>([
        {
          ...balance,
          averageEntryPrice: "185.25",
          realizedPnl: "42.10",
          realizedPnlHaltReason: "missing_fx",
          accountCurrency: "USD",
        },
      ]),
    );
    renderPositions();

    const scope = within(balanceRow());
    expect(
      scope.getByRole("note", { name: /a required FX quote is unavailable/i }),
    ).toBeInTheDocument();
    // The halted figure is withheld, so it carries no unit to misread.
    expect(scope.queryByText("42.10")).not.toBeInTheDocument();
    expect(scope.getByText("185.25")).toBeInTheDocument();
    expect(scope.getAllByText("USD")).toHaveLength(1);
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
        "/orders?account=Bucks+McMoneyface&status=submitted%2Caccepted%2Ccommitted%2Cpartially_filled",
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

  it("clones realized PnL into the dialog and submits one adjustment request", async () => {
    const user = userEvent.setup();
    useAdjustmentsMock.mockReturnValue(
      readyPage<Adjustment>([
        acceptedAdjustment({
          asset: "AAPL",
          balance: { mode: "delta", value: "25" },
          realizedPnl: "-12.50",
        }),
      ]),
    );
    renderPositions("/positions?tab=history");

    await user.click(
      screen.getByRole("button", { name: /clone adj-alpha-10/i }),
    );
    await user.click(screen.getByRole("button", { name: /submit/i }));

    await waitFor(() => expect(createAdjustmentMock).toHaveBeenCalledTimes(1));
    expect(createAdjustmentMock).toHaveBeenCalledWith(
      "Bucks McMoneyface",
      {
        asset: "AAPL",
        balance: { mode: "delta", value: "25" },
        realizedPnl: "-12.50",
      },
      "reject",
    );
    expect(setBalanceRealizedPnlMock).not.toHaveBeenCalled();
  });

  it("offers to create a missing account from the adjust dialog and resubmits with create", async () => {
    const user = userEvent.setup();
    useAdjustmentsMock.mockReturnValue(
      readyPage<Adjustment>([
        acceptedAdjustment({
          asset: "AAPL",
          balance: { mode: "delta", value: "25" },
          realizedPnl: "-12.50",
        }),
      ]),
    );
    createAdjustmentMock.mockRejectedValueOnce(
      new ApiError(
        'block account: account "Bucks McMoneyface" does not exist',
        "account_missing",
        404,
        undefined,
        "Bucks McMoneyface",
      ),
    );
    createAdjustmentMock.mockImplementationOnce(async (_account, body) =>
      acceptedAdjustment(body as unknown as Adjustment["request"]),
    );
    renderPositions("/positions?tab=history");

    await user.click(
      screen.getByRole("button", { name: /clone adj-alpha-10/i }),
    );
    await user.click(screen.getByRole("button", { name: /submit/i }));

    const confirmDialog = await screen.findByRole("alertdialog", {
      name: "Account does not exist",
    });
    expect(createAdjustmentMock).toHaveBeenCalledTimes(1);

    await user.click(
      within(confirmDialog).getByRole("button", {
        name: "Create account and continue",
      }),
    );

    await waitFor(() => expect(createAdjustmentMock).toHaveBeenCalledTimes(2));
    expect(createAdjustmentMock).toHaveBeenNthCalledWith(
      2,
      "Bucks McMoneyface",
      {
        asset: "AAPL",
        balance: { mode: "delta", value: "25" },
        realizedPnl: "-12.50",
      },
      "create",
    );
  });

  it("shows an accepted PnL halt without a numeric result", () => {
    useAdjustmentsMock.mockReturnValue(
      readyPage<Adjustment>([
        acceptedHaltedAdjustment(
          {
            asset: "AAPL",
            averageEntryPrice: "142.50",
          },
          "missing_initial_pnl",
        ),
      ]),
    );
    renderPositions("/positions?tab=history");

    const row = screen
      .getAllByRole("row")
      .find(
        (candidate) =>
          candidate.textContent?.includes("Bucks McMoneyface") &&
          candidate.textContent?.includes("AAPL"),
      );
    expect(row).toBeDefined();
    const scope = within(row as HTMLElement);
    expect(
      scope.getByText(/authoritative initial PnL value is unavailable/i),
    ).toHaveClass("text-[var(--warn)]");
    expect(scope.getByText(/accepted/i)).toHaveClass("border-[var(--warn)]");
  });
});

describe("Positions row navigation to history", () => {
  it("opens the history tab pre-filtered by the row account and asset", async () => {
    const user = userEvent.setup();
    renderPositions();

    await user.click(balanceRow());

    // History tab is now active: its source control (history-only) is shown.
    expect(await screen.findByText("Adjustment history")).toBeInTheDocument();
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
    expect(screen.getByLabelText("Filter by account")).toHaveValue(
      "desk-alpha",
    );
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

  it("ignores the legacy externalId history query parameter", async () => {
    renderPositions("/positions?tab=history&externalId=legacy-adjustment");

    await waitFor(() => {
      const filter =
        useAdjustmentsMock.mock.calls[
          useAdjustmentsMock.mock.calls.length - 1
        ]?.[0];
      expect(filter?.id).toBeUndefined();
    });
  });
});
