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

import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithApi as render } from "@/test/apiClient";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  Balance,
  BusinessCsvEntity,
  BusinessCsvExportFilters,
  Order,
  OrderEvent,
  OrderListFilters,
  Trade,
} from "@/api/types";
import type { PollingResult } from "@/api/usePolling";
import { ApiError } from "@/framework";
import { badgeVariants } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button";
import { SidebarProvider } from "@/components/SidebarContext";
import i18n from "@/i18n";
import { Orders } from "@/pages/Orders";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

// Replace the polling hooks with ready-empty stubs and the one-shot account
// fetch so the page renders without any network traffic.
vi.mock("@/api/useOrders", () => ({ useOrdersPage: vi.fn() }));
vi.mock("@/api/useTrades", () => ({ useTradesPage: vi.fn() }));
vi.mock("@/api/useBalances", () => ({ useBalances: vi.fn() }));
// The page chrome renders PendingRestartBanner, which polls market data on
// mount. Stub it to a settled, no-restart state so its async update does not
// fire outside act() in synchronous renders.
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

import { useBalances } from "@/api/useBalances";
import { useOrdersPage } from "@/api/useOrders";
import { useTradesPage } from "@/api/useTrades";

function readyEmpty<T>(data: T): PollingResult<T> {
  return { load: { state: "ready", data, error: null }, reload: vi.fn() };
}

function readyPage<T>(items: T[]): PollingResult<{ items: T[]; total: number }> {
  return readyEmpty({ items, total: items.length });
}

function readyPageWithTotal<T>(
  items: T[],
  total: number,
): PollingResult<{ items: T[]; total: number }> {
  return readyEmpty({ items, total });
}

const useOrdersMock = vi.mocked(useOrdersPage);
const useTradesMock = vi.mocked(useTradesPage);
const useBalancesMock = vi.mocked(useBalances);

function lastOrderFilters(): OrderListFilters | undefined {
  const calls = useOrdersMock.mock.calls;
  return calls[calls.length - 1]?.[0];
}

function lastTradeFilters() {
  const calls = useTradesMock.mock.calls;
  return calls[calls.length - 1]?.[0];
}

function orderFilterWasRequested(
  match: (filters: OrderListFilters) => boolean,
): boolean {
  return useOrdersMock.mock.calls.some(
    ([filters]) => filters !== undefined && match(filters),
  );
}
const checkOrderMock = vi.fn();
const createOrderMock = vi.fn();
const exportBusinessCsvMock = vi.fn();
const fetchAccountsMock = vi.fn();
const fetchAssetsMock = vi.fn();
const fetchOrderDetailMock = vi.fn();
const fetchEventReproductionMock = vi.fn();
const fetchPublicKeyByIdMock = vi.fn();
const submitExecutionReportMock = vi.fn();
const confirmOrderMock = vi.fn();
const cancelOrderMock = vi.fn();

const proto = window.HTMLElement.prototype as HTMLElement & {
  hasPointerCapture?: (pointerId: number) => boolean;
  setPointerCapture?: (pointerId: number) => void;
};
proto.hasPointerCapture = () => false;
proto.setPointerCapture = () => {};
proto.scrollIntoView = () => {};

const sampleOrder: Order = {
  id: "ord-alpha-1",
  account: "desk-alpha",
  at: "2026-06-24T00:00:00Z",
  source: "panel",
  baseAsset: "AAPL",
  quoteAsset: "USD",
  side: "buy",
  amountKind: "quantity",
  amountValue: "100",
  commissionSubtotals: [],
  leavesQuantity: "100",
  price: "0",
  status: "accepted",
  displayPrice: "",
  dropCopy: false,
  signed: false,
};

function renderOrders(initialEntry: string) {
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-officer-test-density"
          tradeStyleStorageKey="pit-officer-test-trade-style"
        >
          <MemoryRouter initialEntries={[initialEntry]}>
            <SidebarProvider>
              <Orders />
            </SidebarProvider>
          </MemoryRouter>
        </DisplayPreferencesProvider>
      </ThemeProvider>
    </I18nextProvider>,
    {
      api: {
        checkOrder: checkOrderMock,
        createOrder: createOrderMock,
        exportBusinessCsv: exportBusinessCsvMock,
        fetchAccounts: fetchAccountsMock,
        fetchAssets: fetchAssetsMock,
        fetchOrderDetail: fetchOrderDetailMock,
        fetchEventReproduction: fetchEventReproductionMock,
        fetchPublicKeyById: fetchPublicKeyByIdMock,
        submitExecutionReport: submitExecutionReportMock,
        confirmOrder: confirmOrderMock,
        cancelOrder: cancelOrderMock,
      },
    },
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  await i18n.changeLanguage("en");
  useOrdersMock.mockReturnValue(readyPage<Order>([]));
  useTradesMock.mockReturnValue(readyPage<Trade>([]));
  useBalancesMock.mockReturnValue(readyEmpty<Balance[]>([]));
  fetchAccountsMock.mockResolvedValue([]);
  fetchAssetsMock.mockResolvedValue([]);
  checkOrderMock.mockResolvedValue({
    passed: true,
    rejects: [],
    wouldDisplayPrice: "",
    wouldBlock: null,
  });
  createOrderMock.mockResolvedValue({
    order: sampleOrder,
    approval: {
      token: "hold-token",
      keyId: "key-1",
      id: sampleOrder.id,
      verdict: "accept",
      reasons: [],
    },
  });
  exportBusinessCsvMock.mockResolvedValue({
    blob: new Blob(["csv"]),
    filename: "business.csv",
  });
  submitExecutionReportMock.mockResolvedValue({
    id: "report-alpha-1",
    blocks: [],
    outcomes: [],
    attestationToken: "",
    attestationKeyId: "",
    signed: false,
  });
  confirmOrderMock.mockResolvedValue({
    order: { ...sampleOrder, status: "committed" },
    attestationToken: "",
    attestationKeyId: "",
    signed: false,
  });
  cancelOrderMock.mockResolvedValue({
    order: { ...sampleOrder, status: "rolled_back" },
    attestationToken: "",
    attestationKeyId: "",
    signed: false,
  });
  fetchOrderDetailMock.mockResolvedValue({
    order: sampleOrder,
    events: [],
    trades: [],
  });
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    value: vi.fn(() => "blob:orders-csv"),
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    configurable: true,
    value: vi.fn(),
  });
  HTMLAnchorElement.prototype.click = () => {};
});

afterEach(() => {
  vi.useRealTimers();
});

describe("Orders account pre-fill", () => {
  it("suggests accounts and assets inside the add order dialog", async () => {
    const user = userEvent.setup();
    fetchAccountsMock.mockResolvedValue([{ code: "desk-alpha" }]);
    fetchAssetsMock.mockImplementation(async (filters) => {
      if (filters.code === "AA") {
        return [{ code: "AAPL", title: "Apple Inc.", assetClass: "equity" }];
      }
      if (filters.code === "US") {
        return [{ code: "USD", title: "US Dollar", assetClass: "cash" }];
      }
      return [];
    });
    renderOrders("/orders");
    const dialog = await openDialog(user);

    await user.type(within(dialog).getByLabelText("Account"), "desk");
    expect(
      await within(dialog).findByRole("option", { name: "desk-alpha" }),
    ).toBeInTheDocument();

    await user.type(within(dialog).getByLabelText("Base asset"), "AA");
    expect(
      await within(dialog).findByRole("option", { name: "AAPL" }),
    ).toBeInTheDocument();

    await user.type(within(dialog).getByLabelText("Quote asset"), "US");
    expect(
      await within(dialog).findByRole("option", { name: "USD" }),
    ).toBeInTheDocument();
  });

  it("applies advanced order filters only after apply", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /more filters/i });
    await user.type(within(dialog).getAllByPlaceholderText("Value")[0], "100");

    expect(lastOrderFilters()).not.toEqual(
      expect.objectContaining({ amountMin: "100" }),
    );
    expect(screen.queryByText(/active filters/i)).not.toBeInTheDocument();

    await user.click(
      within(dialog).getByRole("button", {
        name: /apply advanced filter/i,
      }),
    );

    await waitFor(() =>
      expect(lastOrderFilters()).toEqual(
        expect.objectContaining({
          amountMode: "greater_than",
          amountMin: "100",
        }),
      ),
    );
    expect(screen.getByText(/active filters/i)).toBeInTheDocument();
    expect(screen.getByText(/amount: Greater than 100/i)).toBeInTheDocument();
  });

  it("disables advanced order filters while a numeric value is invalid", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /more filters/i });
    const amount = within(dialog).getAllByPlaceholderText("Value")[0];
    await user.type(amount, "word");

    expect(
      within(dialog).getByRole("button", {
        name: /apply advanced filter/i,
      }),
    ).toBeDisabled();

    expect(screen.getByRole("dialog", { name: /more filters/i })).toBeInTheDocument();
    expect(amount).toBeInvalid();
    expect(amount).toHaveProperty("validationMessage", "Enter a valid number.");
    expect(lastOrderFilters()).not.toEqual(
      expect.objectContaining({
        amountMode: "greater_than",
        amountMin: expect.any(String),
      }),
    );
    expect(screen.queryByText(/active filters/i)).not.toBeInTheDocument();
  });

  it("seeds the account filter from the ?account= query param", async () => {
    const user = userEvent.setup();
    renderOrders("/orders?account=desk-alpha");
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/filter by account/i)).toHaveValue(
        "desk-alpha",
      ),
    );
    expect(screen.getByText(/active filters/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /clear all filters/i }));
    expect(screen.getByPlaceholderText(/filter by account/i)).toHaveValue("");
    expect(screen.queryByText(/active filters/i)).not.toBeInTheDocument();
  });

  it("pre-fills the new-order account when a single account is filtered", async () => {
    const user = userEvent.setup();
    renderOrders("/orders?account=desk-alpha");

    await user.click(screen.getAllByRole("button", { name: /add order/i })[0]);

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("Account")).toHaveValue("desk-alpha");
  });

  it("opens a blank new-order form when no account filter is set", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await user.click(screen.getAllByRole("button", { name: /add order/i })[0]);

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("Account")).toHaveValue("");
  });

  it("drops the pre-fill once the account filter is cleared", async () => {
    const user = userEvent.setup();
    renderOrders("/orders?account=desk-alpha");

    const filter = screen.getByPlaceholderText(/filter by account/i);
    expect(screen.getByText(/active filters/i)).toBeInTheDocument();
    await user.clear(filter);
    await waitFor(() => expect(filter).toHaveValue(""));
    await user.keyboard("{Enter}");
    expect(screen.queryByText(/active filters/i)).not.toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: /add order/i })[0]);

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("Account")).toHaveValue("");
  });
});

async function openDialog(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getAllByRole("button", { name: /add order/i })[0]);
  return screen.findByRole("dialog");
}

describe("Orders status filter", () => {
  it("has no status filter by default and applies the active set on the quick button", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    // No status is sent until a filter is chosen.
    await waitFor(() => expect(lastOrderFilters()).toBeDefined());
    expect(lastOrderFilters()).not.toHaveProperty("status");

    await user.click(screen.getByRole("button", { name: /^active$/i }));

    await waitFor(() =>
      expect(lastOrderFilters()).toEqual(
        expect.objectContaining({
          status: "submitted,accepted,committed,partially_filled",
        }),
      ),
    );
    expect(screen.getByText(/active filters/i)).toBeInTheDocument();
    // The Active quick set is a shorthand, so it is not surfaced as an
    // advanced-filter chip.
    expect(screen.queryByText(/status:/i)).not.toBeInTheDocument();
  });

  it("clears the status filter with the All quick button", async () => {
    const user = userEvent.setup();
    renderOrders(
      "/orders?status=submitted,accepted,committed,partially_filled",
    );

    await waitFor(() =>
      expect(lastOrderFilters()).toEqual(
        expect.objectContaining({
          status: "submitted,accepted,committed,partially_filled",
        }),
      ),
    );

    await user.click(screen.getByRole("button", { name: /^all$/i }));

    await waitFor(() => expect(lastOrderFilters()).not.toHaveProperty("status"));
  });

  it("opens the advanced status draft with every status selected for All orders", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /more filters/i });

    for (const checkbox of within(dialog).getAllByRole("checkbox")) {
      expect(checkbox).toBeChecked();
    }
  });

  it("does not allow applying an empty advanced status set", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /more filters/i });
    await user.click(
      within(dialog).getAllByRole("button", { name: /clear all/i })[0],
    );

    expect(
      within(dialog).getByRole("button", { name: /apply advanced filter/i }),
    ).toBeDisabled();
    expect(lastOrderFilters()).not.toHaveProperty("status");
  });

  it("applies a custom status subset from the advanced dialog and shows it as a chip", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /more filters/i });
    await user.click(
      within(dialog).getAllByRole("button", { name: /clear all/i })[0],
    );
    await user.click(within(dialog).getByRole("checkbox", { name: "Filled" }));
    await user.click(
      within(dialog).getByRole("button", { name: /apply advanced filter/i }),
    );

    await waitFor(() =>
      expect(lastOrderFilters()).toEqual(
        expect.objectContaining({ status: "filled" }),
      ),
    );
    // A genuine custom subset (not All or Active) is surfaced as a chip.
    expect(screen.getByText(/status: Filled/i)).toBeInTheDocument();
  });

  it("selects the whole active group from the advanced group controls", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /more filters/i });
    await user.click(
      within(dialog).getAllByRole("button", { name: /clear all/i })[0],
    );
    await user.click(
      within(dialog).getAllByRole("button", { name: /select all/i })[1],
    );
    await user.click(
      within(dialog).getByRole("button", { name: /apply advanced filter/i }),
    );

    await waitFor(() =>
      expect(lastOrderFilters()).toEqual(
        expect.objectContaining({
          status: "submitted,accepted,committed,partially_filled",
        }),
      ),
    );
    // Equal to the Active quick set, so no status chip is shown.
    expect(screen.getByText(/active filters/i)).toBeInTheDocument();
    expect(screen.queryByText(/status:/i)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^active$/i })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("normalizes the full advanced status set to the All quick filter", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /more filters/i });
    await user.click(
      within(dialog).getAllByRole("button", { name: /select all/i })[0],
    );
    await user.click(
      within(dialog).getByRole("button", { name: /apply advanced filter/i }),
    );

    await waitFor(() =>
      expect(lastOrderFilters()).not.toHaveProperty("status"),
    );
    expect(screen.queryByText(/active filters/i)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^all$/i })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });
});

describe("Orders side & amount-kind toggles", () => {
  it("renders side as buy/sell radio buttons without a default selection", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);

    const sideGroup = within(dialog).getByRole("radiogroup", { name: "Side" });
    expect(within(sideGroup).getByRole("radio", { name: "Buy" })).toHaveAttribute(
      "aria-checked",
      "false",
    );
    expect(within(sideGroup).getByRole("radio", { name: "Sell" })).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  it("does not pre-select an amount kind", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);

    const kindGroup = within(dialog).getByRole("radiogroup", { name: "Amount kind" });
    for (const radio of within(kindGroup).getAllByRole("radio")) {
      expect(radio).toHaveAttribute("aria-checked", "false");
    }
  });

  it("keeps active account prefill without side or amount-kind defaults", async () => {
    const user = userEvent.setup();
    renderOrders("/orders?account=desk-alpha");
    const dialog = await openDialog(user);

    expect(within(dialog).getByLabelText("Account")).toHaveValue("desk-alpha");

    const sideGroup = within(dialog).getByRole("radiogroup", { name: "Side" });
    expect(within(sideGroup).getByRole("radio", { name: "Buy" })).toHaveAttribute(
      "aria-checked",
      "false",
    );
    expect(within(sideGroup).getByRole("radio", { name: "Sell" })).toHaveAttribute(
      "aria-checked",
      "false",
    );

    const kindGroup = within(dialog).getByRole("radiogroup", { name: "Amount kind" });
    for (const radio of within(kindGroup).getAllByRole("radio")) {
      expect(radio).toHaveAttribute("aria-checked", "false");
    }
  });

  it("clears typed order-entry fields with inline reset controls", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);

    const fields = [
      within(dialog).getByLabelText("ID (optional)"),
      within(dialog).getByLabelText("Account"),
      within(dialog).getByLabelText("Base asset"),
      within(dialog).getByLabelText("Quote asset"),
      within(dialog).getByLabelText("Amount"),
      within(dialog).getByLabelText("Limit price (optional)"),
    ];
    for (const field of fields) {
      await user.type(field, "A");
    }

    const clears = within(dialog).getAllByRole("button", {
      name: /clear field/i,
    });
    expect(clears).toHaveLength(fields.length);

    await user.click(clears[0]!);
    expect(fields[0]).toHaveValue("");
  });
});

describe("Orders submit mode", () => {
  it("does not pre-select a submit mode", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);

    const modeGroup = within(dialog).getByRole("radiogroup", { name: "Submit mode" });
    for (const radio of within(modeGroup).getAllByRole("radio")) {
      expect(radio).toHaveAttribute("aria-checked", "false");
    }
  });

  it("keeps submit disabled until side, amount kind, and submit mode are chosen", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);

    await user.type(
      within(dialog).getByLabelText("ID (optional)"),
      "ord-supplied-000001",
    );
    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100");

    const submitButton = within(dialog).getByRole("button", { name: /add order/i });

    // Text fields filled, but no required choices have been made yet.
    expect(submitButton).toBeDisabled();

    const sideGroup = within(dialog).getByRole("radiogroup", { name: "Side" });
    await user.click(within(sideGroup).getByRole("radio", { name: /buy/i }));
    // Side chosen, amount kind and mode still missing.
    expect(submitButton).toBeDisabled();

    const kindGroup = within(dialog).getByRole("radiogroup", { name: "Amount kind" });
    await user.click(within(kindGroup).getByRole("radio", { name: /quantity/i }));
    // Amount kind chosen, mode still missing.
    expect(submitButton).toBeDisabled();

    const modeGroup = within(dialog).getByRole("radiogroup", { name: "Submit mode" });
    await user.click(
      within(modeGroup).getByRole("radio", { name: /submit and settle/i }),
    );
    // Everything chosen — submission is unblocked.
    expect(submitButton).toBeEnabled();

    await user.click(submitButton);
    await waitFor(() => expect(createOrderMock).toHaveBeenCalledTimes(1));
    const submitted = createOrderMock.mock.calls[0][0];
    const missingAccount = createOrderMock.mock.calls[0][1];
    const signal = createOrderMock.mock.calls[0][2];
    expect(submitted).toMatchObject({
      id: "ord-supplied-000001",
      account: "desk-alpha",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      side: "buy",
      amountKind: "quantity",
      amountValue: "100",
      mode: "immediate",
    });
    expect(submitted).not.toHaveProperty("submitMode");
    expect(missingAccount).toBe("reject");
    expect(signal).toBeInstanceOf(AbortSignal);
  });

  it("requires confirmation for the exclusive drop-copy mode", async () => {
    const user = userEvent.setup();
    createOrderMock.mockResolvedValueOnce({
      order: {
        ...sampleOrder,
        id: "ord-drop-copy-000001",
        source: "panel",
        dropCopy: true,
      },
    });
    renderOrders("/orders");
    const dialog = await openDialog(user);

    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    await user.click(within(dialog).getByRole("radio", { name: /buy/i }));
    await user.click(
      within(dialog).getByRole("radio", { name: /quantity/i }),
    );
    await user.click(
      within(dialog).getByRole("radio", { name: /^drop copy/i }),
    );

    const modeGroup = within(dialog).getByRole("radiogroup", {
      name: "Submit mode",
    });
    expect(within(modeGroup).getAllByRole("radio")).toHaveLength(3);
    expect(
      within(modeGroup).getByRole("radio", { name: /^drop copy/i }),
    ).toHaveAttribute("aria-checked", "true");
    expect(
      within(modeGroup).getByRole("radio", { name: /submit and wait/i }),
    ).toHaveAttribute("aria-checked", "false");
    expect(within(dialog).getByLabelText("ID (optional)")).toHaveAttribute(
      "placeholder",
      "server generated when blank",
    );
    expect(
      within(dialog).getByText(/use your own unique ID when you need a stable order handle/i),
    ).toBeInTheDocument();
    const submit = within(dialog).getByRole("button", {
      name: /submit drop-copy/i,
    });
    const price = within(dialog).getByLabelText(
      "Limit price (required for drop-copy)",
    );
    expect(price).toHaveAttribute("placeholder", "required");
    expect(
      within(dialog).getByText(/market orders cannot be submitted as drop-copy/i),
    ).toBeInTheDocument();
    expect(submit).toBeDisabled();
    await user.type(price, "0");
    expect(submit).toBeDisabled();
    await user.clear(price);
    await user.type(price, "10");
    expect(submit).toBeEnabled();

    await user.click(submit);
    expect(createOrderMock).not.toHaveBeenCalled();

    const confirmation = await screen.findByRole("alertdialog", {
      name: /submit without enforced pre-trade approval/i,
    });
    expect(
      within(confirmation).getByText(
        /neither this submit nor later execution reports are signed or attested/i,
      ),
    ).toBeInTheDocument();
    expect(
      within(confirmation).getByText(
        /if the account is unknown, it will be created with default settings/i,
      ),
    ).toBeInTheDocument();
    const confirmAction = within(confirmation).getByRole("button", {
      name: /submit drop-copy/i,
    });
    expect(confirmAction).toHaveClass(
      ...buttonVariants({ variant: "danger", size: null }).split(" "),
    );
    await user.click(confirmAction);

    await waitFor(() => expect(createOrderMock).toHaveBeenCalledTimes(1));
    expect(createOrderMock.mock.calls[0][0]).toMatchObject({
      mode: "drop_copy",
    });
    expect(createOrderMock.mock.calls[0][0]).not.toHaveProperty("id");
    expect(createOrderMock.mock.calls[0][0]).not.toHaveProperty("dropCopy");
    // Drop-copy reports a fact that already happened, so the account is
    // always created on demand rather than asked about.
    expect(createOrderMock.mock.calls[0][1]).toBe("create");
  });

  it("offers to create a missing account for an immediate submit and resubmits with create", async () => {
    const user = userEvent.setup();
    createOrderMock.mockRejectedValueOnce(
      new ApiError(
        'block account: account "desk-new" does not exist',
        "account_missing",
        404,
        undefined,
        "desk-new",
      ),
    );
    createOrderMock.mockResolvedValueOnce({ order: sampleOrder });
    renderOrders("/orders");
    const dialog = await openDialog(user);

    await user.type(within(dialog).getByLabelText("Account"), "desk-new");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    await user.click(within(dialog).getByRole("radio", { name: /buy/i }));
    await user.click(within(dialog).getByRole("radio", { name: /quantity/i }));
    await user.click(
      within(dialog).getByRole("radio", { name: /submit and settle/i }),
    );
    await user.click(within(dialog).getByRole("button", { name: /add order/i }));

    const confirmation = await screen.findByRole("alertdialog", {
      name: "Account does not exist",
    });
    expect(createOrderMock).toHaveBeenCalledTimes(1);
    expect(createOrderMock.mock.calls[0][1]).toBe("reject");

    await user.click(
      within(confirmation).getByRole("button", {
        name: "Create account and continue",
      }),
    );

    await waitFor(() => expect(createOrderMock).toHaveBeenCalledTimes(2));
    expect(createOrderMock.mock.calls[1][0]).toMatchObject({
      account: "desk-new",
      mode: "immediate",
    });
    expect(createOrderMock.mock.calls[1][1]).toBe("create");
  });

  it("surfaces a drop-copy validation error returned by the API", async () => {
    const user = userEvent.setup();
    const apiMessage = "Drop-copy market orders require a limit price.";
    createOrderMock.mockRejectedValueOnce(
      new ApiError(apiMessage, "validation", 400),
    );
    renderOrders("/orders");
    const dialog = await openDialog(user);

    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    await user.click(within(dialog).getByRole("radio", { name: /buy/i }));
    await user.click(
      within(dialog).getByRole("radio", { name: /quantity/i }),
    );
    await user.click(
      within(dialog).getByRole("radio", { name: /^drop copy/i }),
    );
    await user.type(
      within(dialog).getByLabelText("Limit price (required for drop-copy)"),
      "10",
    );
    await user.click(
      within(dialog).getByRole("button", { name: /submit drop-copy/i }),
    );
    const confirmation = await screen.findByRole("alertdialog", {
      name: /submit without enforced pre-trade approval/i,
    });
    await user.click(
      within(confirmation).getByRole("button", { name: /submit drop-copy/i }),
    );

    expect(await screen.findByText(apiMessage)).toBeInTheDocument();
  });

  it("disables order submission when a numeric field contains an invalid draft", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);

    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100x");
    await user.click(within(dialog).getByRole("radio", { name: /buy/i }));
    await user.click(
      within(dialog).getByRole("radio", { name: /quantity/i }),
    );
    await user.click(
      within(dialog).getByRole("radio", { name: /submit and settle/i }),
    );

    expect(within(dialog).getByLabelText("Amount")).toHaveValue("100x");
    expect(
      within(dialog).getByRole("button", { name: /add order/i }),
    ).toBeDisabled();
    expect(createOrderMock).not.toHaveBeenCalled();
  });

  it("requires a positive amount and a positive provided limit price", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);

    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "0");
    await user.click(within(dialog).getByRole("radio", { name: /buy/i }));
    await user.click(within(dialog).getByRole("radio", { name: /quantity/i }));
    await user.click(
      within(dialog).getByRole("radio", { name: /submit and settle/i }),
    );

    const submit = within(dialog).getByRole("button", { name: /add order/i });
    expect(submit).toBeDisabled();

    await user.clear(within(dialog).getByLabelText("Amount"));
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    expect(submit).toBeEnabled();

    await user.type(within(dialog).getByLabelText("Limit price (optional)"), "0");
    expect(submit).toBeDisabled();

    await user.clear(within(dialog).getByLabelText("Limit price (optional)"));
    await user.type(within(dialog).getByLabelText("Limit price (optional)"), "10");
    expect(submit).toBeEnabled();
    expect(createOrderMock).not.toHaveBeenCalled();
  });

  it("shows create-order enrichment warnings in the detail dialog", async () => {
    const user = userEvent.setup();
    const warning = "Order was created, but detail enrichment failed: store down";
    createOrderMock.mockResolvedValueOnce({ order: sampleOrder, warning });
    renderOrders("/orders");
    const dialog = await openDialog(user);

    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    await user.click(within(dialog).getByRole("radio", { name: /buy/i }));
    await user.click(within(dialog).getByRole("radio", { name: /quantity/i }));
    await user.click(
      within(dialog).getByRole("radio", { name: /submit and settle/i }),
    );
    await user.click(within(dialog).getByRole("button", { name: /add order/i }));

    expect(await screen.findByText(warning)).toBeInTheDocument();
  });

  it("clears busy after aborting an in-flight submit", async () => {
    const user = userEvent.setup();
    createOrderMock.mockImplementation((_body, _missingAccount, signal) => (
      new Promise((_, reject) => {
        signal?.addEventListener("abort", () => {
          reject(new DOMException("aborted", "AbortError"));
        }, { once: true });
      })
    ));
    renderOrders("/orders");
    let dialog = await openDialog(user);

    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    await user.click(within(dialog).getByRole("radio", { name: /buy/i }));
    await user.click(within(dialog).getByRole("radio", { name: /quantity/i }));
    await user.click(
      within(dialog).getByRole("radio", { name: /submit and settle/i }),
    );
    await user.click(within(dialog).getByRole("button", { name: /add order/i }));
    await waitFor(() => expect(createOrderMock).toHaveBeenCalledTimes(1));

    await user.click(within(dialog).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    dialog = await openDialog(user);
    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    await user.click(within(dialog).getByRole("radio", { name: /buy/i }));
    await user.click(within(dialog).getByRole("radio", { name: /quantity/i }));
    await user.click(
      within(dialog).getByRole("radio", { name: /submit and settle/i }),
    );

    expect(within(dialog).getByRole("button", { name: /add order/i })).toBeEnabled();
  });
});

describe("Orders workflow confirm/cancel shortcuts", () => {
  // Fill the add-order dialog with a valid workflow order and submit it. The page
  // retains the returned approval token and opens the detail view.
  async function submitWorkflowOrder(
    user: ReturnType<typeof userEvent.setup>,
  ) {
    const dialog = await openDialog(user);
    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    await user.click(within(dialog).getByRole("radio", { name: /buy/i }));
    await user.click(within(dialog).getByRole("radio", { name: /quantity/i }));
    await user.click(
      within(dialog).getByRole("radio", { name: /submit and wait/i }),
    );
    await user.click(within(dialog).getByRole("button", { name: /add order/i }));
  }

  it("confirms a workflow order with its retained approval token", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await submitWorkflowOrder(user);

    const detail = await screen.findByRole("dialog", {
      name: "Order ord-alpha-1",
    });
    const confirm = await within(detail).findByRole("button", {
      name: "Confirm",
    });
    await user.click(confirm);

    await waitFor(() =>
      expect(confirmOrderMock).toHaveBeenCalledWith("ord-alpha-1", {
        token: "hold-token",
      }),
    );
    const cancel = await within(detail).findByRole("button", {
      name: "Cancel order",
    });
    await user.click(cancel);
    await user.type(within(detail).getByLabelText("Leaves quantity"), "100");
    await user.click(
      within(detail).getByRole("button", { name: "Cancel order" }),
    );
    await waitFor(() =>
      expect(cancelOrderMock).toHaveBeenCalledWith("ord-alpha-1", {
        token: "hold-token",
        leavesQuantity: "100",
      }),
    );
  });

  it("opens cancellation leaves empty", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await submitWorkflowOrder(user);

    const detail = await screen.findByRole("dialog", {
      name: "Order ord-alpha-1",
    });

    await user.click(
      within(detail).getByRole("button", { name: "Cancel order" }),
    );

    expect(within(detail).getByLabelText("Leaves quantity")).toHaveValue("");
  });

  it("cancels a workflow order with caller-supplied leaves", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await submitWorkflowOrder(user);

    const detail = await screen.findByRole("dialog", {
      name: "Order ord-alpha-1",
    });
    await user.click(
      within(detail).getByRole("button", { name: "Cancel order" }),
    );
    const leaves = within(detail).getByLabelText("Leaves quantity");
    await user.type(leaves, "7.5");
    await user.click(
      within(detail).getByRole("button", { name: "Cancel order" }),
    );

    await waitFor(() =>
      expect(cancelOrderMock).toHaveBeenCalledWith("ord-alpha-1", {
        token: "hold-token",
        leavesQuantity: "7.5",
      }),
    );
  });

  // The engine settles the cancellation and has no reject channel, so the
  // panel must not send a report without leaves.
  it("blocks cancellation while leaves is empty or malformed", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await submitWorkflowOrder(user);

    const detail = await screen.findByRole("dialog", {
      name: "Order ord-alpha-1",
    });
    await user.click(
      within(detail).getByRole("button", { name: "Cancel order" }),
    );
    const leaves = within(detail).getByLabelText("Leaves quantity");

    const cancel = within(detail).getByRole("button", {
      name: "Cancel order",
    });
    await waitFor(() => expect(cancel).toBeDisabled());

    await user.type(leaves, "not-a-number");
    await waitFor(() => expect(cancel).toBeDisabled());
    expect(cancelOrderMock).not.toHaveBeenCalled();
  });

  it("does not retain shortcuts for a rejected workflow submit", async () => {
    const user = userEvent.setup();
    const rejectedOrder: Order = { ...sampleOrder, status: "rejected" };
    createOrderMock.mockResolvedValueOnce({
      order: rejectedOrder,
      approval: {
        token: "rejected-hold-token",
        keyId: "key-1",
        id: rejectedOrder.id,
        verdict: "reject",
        reasons: [{
          code: "policy_reject",
          scope: "account",
          policy: "daily_loss",
          reason: "Rejected by policy",
          details: "",
        }],
      },
    });
    fetchOrderDetailMock.mockResolvedValue({
      order: rejectedOrder,
      events: [],
      trades: [],
    });
    renderOrders("/orders");

    await submitWorkflowOrder(user);

    const detail = await screen.findByRole("dialog", {
      name: "Order ord-alpha-1",
    });
    expect(
      within(detail).queryByRole("button", { name: "Confirm" }),
    ).not.toBeInTheDocument();
    expect(
      within(detail).queryByRole("button", { name: "Cancel order" }),
    ).not.toBeInTheDocument();
  });

  it("forgets workflow shortcuts after a successful explicit report", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await submitWorkflowOrder(user);
    const detail = await screen.findByRole("dialog", {
      name: "Order ord-alpha-1",
    });
    expect(
      await within(detail).findByRole("button", { name: "Confirm" }),
    ).toBeInTheDocument();

    await user.click(
      within(detail).getByRole("button", { name: "Submit execution report" }),
    );
    const focusSpy = vi
      .spyOn(HTMLElement.prototype, "focus")
      .mockImplementation(() => {});
    await user.click(await screen.findByLabelText("Target status"));
    await user.click(screen.getByRole("option", { name: "Accepted" }));
    focusSpy.mockRestore();
    await user.click(screen.getByRole("button", { name: "Submit" }));

    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(
        "ord-alpha-1",
        { status: "accepted" },
      ),
    );
    expect(await screen.findByText("Report ID")).toBeInTheDocument();
    expect(screen.getByText("report-alpha-1")).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Done" }));

    expect(
      within(detail).queryByRole("button", { name: "Confirm" }),
    ).not.toBeInTheDocument();
    expect(
      within(detail).queryByRole("button", { name: "Cancel order" }),
    ).not.toBeInTheDocument();
  });

  it("surfaces execution_report_required after report activity", async () => {
    const user = userEvent.setup();
    confirmOrderMock.mockRejectedValueOnce(
      new ApiError(
        "Submit an explicit execution report for this order.",
        "execution_report_required",
        409,
      ),
    );
    renderOrders("/orders");

    await submitWorkflowOrder(user);

    const detail = await screen.findByRole("dialog", {
      name: "Order ord-alpha-1",
    });
    await user.click(
      await within(detail).findByRole("button", { name: "Confirm" }),
    );

    expect(
      await within(detail).findByText(
        "Submit an explicit execution report for this order.",
      ),
    ).toBeInTheDocument();
  });

  it("lets the backend reject a terminal workflow order when a token is retained", async () => {
    const user = userEvent.setup();
    const terminalOrder: Order = { ...sampleOrder, status: "filled" };
    createOrderMock.mockResolvedValueOnce({
      order: terminalOrder,
      approval: {
        token: "hold-token",
        keyId: "key-1",
        id: terminalOrder.id,
        verdict: "accept",
        reasons: [],
      },
    });
    fetchOrderDetailMock.mockResolvedValue({
      order: terminalOrder,
      events: [],
      trades: [],
    });
    confirmOrderMock.mockRejectedValueOnce(
      new ApiError(
        "Submit an explicit execution report for this order.",
        "execution_report_required",
        409,
      ),
    );
    renderOrders("/orders");

    await submitWorkflowOrder(user);

    const detail = await screen.findByRole("dialog", {
      name: "Order ord-alpha-1",
    });
    await user.click(
      await within(detail).findByRole("button", { name: "Confirm" }),
    );

    await waitFor(() =>
      expect(confirmOrderMock).toHaveBeenCalledWith("ord-alpha-1", {
        token: "hold-token",
      }),
    );
    expect(
      await within(detail).findByText(
        "Submit an explicit execution report for this order.",
      ),
    ).toBeInTheDocument();
  });

  it("hides confirm/cancel when no token is retained for the order", async () => {
    const user = userEvent.setup();
    // A workflow order that exists in the table but whose token this session never
    // saw (e.g. created via API or lost on reload) offers no confirm/cancel.
    useOrdersMock.mockReturnValue(readyPage<Order>([sampleOrder]));
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-1"));
    const detail = await screen.findByRole("dialog", {
      name: "Order ord-alpha-1",
    });

    expect(
      within(detail).queryByRole("button", { name: "Confirm" }),
    ).not.toBeInTheDocument();
    expect(
      within(detail).queryByRole("button", { name: "Cancel order" }),
    ).not.toBeInTheDocument();
  });
});

describe("Orders row signed indicator", () => {
  it("shows a key icon next to the id only for a signed order", () => {
    useOrdersMock.mockReturnValue(
      readyPage<Order>([
        sampleOrder,
        { ...sampleOrder, id: "ord-alpha-signed", signed: true },
      ]),
    );
    renderOrders("/orders");

    const rows = screen.getAllByRole("row");
    const unsignedRow = rows.find((row) =>
      row.textContent?.includes("ord-alpha-1"),
    );
    const signedRow = rows.find((row) =>
      row.textContent?.includes("ord-alpha-signed"),
    );
    expect(unsignedRow).toBeDefined();
    expect(signedRow).toBeDefined();

    expect(
      within(unsignedRow as HTMLElement).queryByRole("img", { hidden: true }),
    ).not.toBeInTheDocument();
    expect(
      within(signedRow as HTMLElement).getByRole("img", { hidden: true }),
    ).toBeInTheDocument();
  });
});

describe("Orders drop-copy marker", () => {
  // The badge tone only exists as cva classes, so compare against the exact
  // class set `variant="danger"` produces; `neutral` shares none of them.
  function expectDangerBadge(badge: HTMLElement): void {
    for (const className of badgeVariants({ variant: "danger" }).split(" ")) {
      expect(badge).toHaveClass(className);
    }
  }

  it("shows the danger badge in both the table and detail dialog", async () => {
    const user = userEvent.setup();
    const order: Order = {
      ...sampleOrder,
      id: "ord-drop-copy-1",
      source: "panel",
      dropCopy: true,
    };
    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    const row = screen
      .getAllByRole("row")
      .find((candidate) => candidate.textContent?.includes(order.id));
    expect(row).toBeDefined();
    const rowBadge = within(row as HTMLElement).getByText("DROP COPY");
    expect(rowBadge).toBeInTheDocument();
    expectDangerBadge(rowBadge);

    await user.click(within(row as HTMLElement).getByText(order.id));
    const detail = await screen.findByRole("dialog", {
      name: `Order ${order.id}`,
    });
    const detailBadge = within(detail).getByText("DROP COPY");
    expect(detailBadge).toBeInTheDocument();
    expectDangerBadge(detailBadge);
  });
});

describe("Orders drop-copy submission id", () => {
  async function submitDropCopy(externalId?: string) {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);

    if (externalId) {
      await user.type(
        within(dialog).getByLabelText("ID (optional)"),
        externalId,
      );
    }
    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    await user.click(within(dialog).getByRole("radio", { name: /buy/i }));
    await user.click(
      within(dialog).getByRole("radio", { name: /quantity/i }),
    );
    await user.click(
      within(dialog).getByRole("radio", { name: /^drop copy/i }),
    );
    await user.type(
      within(dialog).getByLabelText("Limit price (required for drop-copy)"),
      "10",
    );
    await user.click(
      within(dialog).getByRole("button", { name: /submit drop-copy/i }),
    );

    const confirmation = await screen.findByRole("alertdialog", {
      name: /submit without enforced pre-trade approval/i,
    });
    await user.click(
      within(confirmation).getByRole("button", { name: /submit drop-copy/i }),
    );

    await waitFor(() => expect(createOrderMock).toHaveBeenCalledTimes(1));
    return createOrderMock.mock.calls[0][0];
  }

  it("does not send an ID when the operator leaves it blank", async () => {
    const submitted = await submitDropCopy();

    expect(submitted).toMatchObject({ mode: "drop_copy" });
    expect(submitted).not.toHaveProperty("id");
  });

  it("forwards an operator-supplied ID", async () => {
    const submitted = await submitDropCopy("drop-copy-supplied-000001");

    expect(submitted).toMatchObject({
      id: "drop-copy-supplied-000001",
      mode: "drop_copy",
    });
  });
});

describe("Orders row filters", () => {
  it("filters orders by the row account and instrument", async () => {
    const user = userEvent.setup();
    useOrdersMock.mockReturnValue(readyPage<Order>([sampleOrder]));
    renderOrders("/orders");

    const row = screen
      .getAllByRole("row")
      .find((candidate) => candidate.textContent?.includes("ord-alpha-1"));
    expect(row).toBeDefined();
    const scope = within(row as HTMLElement);

    await user.click(
      scope.getByRole("button", { name: /filter by desk-alpha/i }),
    );
    expect(screen.getByPlaceholderText(/filter by account/i)).toHaveValue(
      "desk-alpha",
    );

    await user.click(scope.getByRole("button", { name: /filter by aapl \/ usd/i }));
    expect(screen.getByPlaceholderText("Base")).toHaveValue("AAPL");
    expect(screen.getByPlaceholderText("Quote")).toHaveValue("USD");
  });

  it("exposes order links from trades as new-tab friendly deep links", async () => {
    const user = userEvent.setup();
    useTradesMock.mockReturnValue(
      readyPage<Trade>([
        {
          id: "trd-alpha-1",
          order: "ord-alpha-1",
          account: "desk-alpha",
          at: "2026-06-24T00:00:00Z",
          source: "panel",
          baseAsset: "AAPL",
          quoteAsset: "USD",
          side: "buy",
          quantity: "2",
          price: "12",
          lockPrice: "12",
        },
      ]),
    );
    renderOrders("/orders?tab=trades");

    await user.click(screen.getByRole("button", { name: /^trades$/i }));
    const link = screen.getByRole("link", { name: "ord-alpha-1" });
    expect(link).toHaveAttribute("href", expect.stringContaining("order=ord-alpha-1"));
    expect(link).toHaveAttribute("title", expect.stringContaining("Ctrl-click"));
  });
});

describe("Order detail account block placement", () => {
  it("shows the account block reason beside the fill event that caused it", async () => {
    const user = userEvent.setup();
    const order = { ...sampleOrder, id: "ord-alpha-3", status: "filled", price: "99" };
    const events: OrderEvent[] = [
      {
        id: "evt-alpha-1",
        order: "ord-alpha-3",
        at: "2026-06-24T11:07:00Z",
        type: "submitted",
        source: "panel",
        principal: "",
        signed: false,
      },
      {
        id: "evt-alpha-2",
        order: "ord-alpha-3",
        at: "2026-06-24T11:08:00Z",
        type: "fill",
        source: "panel",
        principal: "",
        fillQuantity: "2",
        fillPrice: "99",
        fillLockPrice: "99",
        rejectCode: "missing_required_field",
        rejectScope: "account",
        rejectReason:
          "failed to access required field 'remaining quantity'",
        rejectDetails: "failed to access field 'fill.leaves_quantity'",
        signed: false,
      },
    ];

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events,
      trades: [],
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-3"));

    const dialog = await screen.findByRole("dialog");
    const blockReason = await within(dialog).findByText(
      /failed to access required field/i,
    );
    const fillEvent = blockReason.closest("li");

    expect(fillEvent).not.toBeNull();
    expect(within(fillEvent as HTMLElement).getByText("fill")).toBeInTheDocument();

    const orderFields = within(dialog).getByText("Account").closest(".grid");
    expect(orderFields).not.toBeNull();
    expect(
      within(orderFields as HTMLElement).queryByText(
        /failed to access required field/i,
      ),
    ).not.toBeInTheDocument();
  });
});

describe("Order detail per-event verification", () => {
  it("shows a key icon on a signed event and opens its per-event panel", async () => {
    const user = userEvent.setup();
    const order = { ...sampleOrder, id: "ord-signed-evt", signed: true };
    const events: OrderEvent[] = [
      {
        id: "evt-unsigned",
        order: "ord-signed-evt",
        at: "2026-06-24T11:07:00Z",
        type: "submitted",
        source: "panel",
        principal: "",
        signed: false,
      },
      {
        id: "evt-signed",
        order: "ord-signed-evt",
        at: "2026-06-24T11:08:00Z",
        type: "fill",
        source: "panel",
        principal: "",
        fillQuantity: "2",
        fillPrice: "99",
        signed: true,
        alg: "ed25519",
      },
    ];

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({ order, events, trades: [] });
    fetchEventReproductionMock.mockResolvedValue({
      requestType: "execution_report",
      event: events[1],
      attestation: {
        token: "tok-evt-verbatim",
        keyId: "key-e",
        alg: "ed25519",
        requestType: "execution_report",
        mode: "immediate",
        issuedAt: "2026-06-24T11:08:00Z",
        signed: true,
      },
      request: null,
      response: {
        submitResponse: null,
        executionReport: {
          blocks: [],
          attestationToken: "tok-evt-verbatim",
          attestationKeyId: "key-e",
          signed: true,
        },
        confirm: null,
        cancel: null,
      },
      canonicalApproval: '{"requestType":"execution_report"}',
      publicKey: {
        keyId: "key-e",
        alg: "ed25519",
        format: "pem-pkcs8",
        key: "PEM",
      },
      eSign: { alg: "ed25519", noESign: false, signed: true },
      signature: "sig-e",
      reason: "",
    });
    fetchPublicKeyByIdMock.mockResolvedValue({
      keyId: "key-e",
      alg: "ed25519",
      format: "raw-base64",
      key: "RAW",
    });

    renderOrders("/orders");
    await user.click(screen.getByText("ord-signed-evt"));

    const dialog = await screen.findByRole("dialog");
    // The signed fill event exposes a verify affordance; the unsigned submit
    // event does not.
    const signedButton = within(dialog).getByRole("button", {
      name: /Reproduce and verify this event's signature/i,
    });
    expect(
      within(dialog).queryByRole("button", {
        name: /Reproduce this event's unsigned attestation/i,
      }),
    ).not.toBeInTheDocument();

    await user.click(signedButton);

    // The per-event reproduction panel opens and requests the (order, event).
    await waitFor(() =>
      expect(fetchEventReproductionMock).toHaveBeenCalled(),
    );
    const [orderId, eventId] = fetchEventReproductionMock.mock.calls[0];
    expect(orderId).toBe("ord-signed-evt");
    expect(eventId).toBe("evt-signed");

    // The verbatim token renders inside the opened panel.
    await waitFor(() =>
      expect(
        screen
          .getAllByRole("textbox")
          .some(
            (el) => (el as HTMLTextAreaElement).value === "tok-evt-verbatim",
          ),
      ).toBe(true),
    );
  });
});

describe("Orders business CSV export", () => {
  it("exports orders with account and source filters but no display size", async () => {
    const user = userEvent.setup();
    renderOrders("/orders?account=desk-alpha&source=panel");

    await user.click(screen.getByRole("button", { name: /export orders csv/i }));
    await user.click(screen.getByRole("button", { name: /^export$/i }));

    await waitFor(() => expect(exportBusinessCsvMock).toHaveBeenCalledTimes(1));
    expect(exportBusinessCsvMock).toHaveBeenCalledWith(
      expect.objectContaining({
        entity: "orders",
        filters: {
          account: "desk-alpha",
          source: "panel",
        },
      }),
    );
    expect(exportBusinessCsvMock.mock.calls[0][0].filters).not.toHaveProperty(
      "limit",
    );
  });

  it("normalizes URL _all source before loading and exporting orders", async () => {
    const user = userEvent.setup();
    renderOrders("/orders?account=desk-alpha&source=_all");

    expect(useOrdersMock).toHaveBeenCalledWith(
      expect.objectContaining({ account: "desk-alpha", source: undefined }),
    );
    expect(useTradesMock).toHaveBeenCalledWith(
      expect.objectContaining({
        account: "desk-alpha",
        source: undefined,
        limit: 50,
        offset: 0,
      }),
    );

    await user.click(screen.getByRole("button", { name: /export orders csv/i }));
    await user.click(screen.getByRole("button", { name: /^export$/i }));

    await waitFor(() => expect(exportBusinessCsvMock).toHaveBeenCalledTimes(1));
    expect(exportBusinessCsvMock).toHaveBeenCalledWith(
      expect.objectContaining({
        entity: "orders",
        filters: { account: "desk-alpha" },
      }),
    );
    expect(exportBusinessCsvMock.mock.calls[0][0].filters).not.toHaveProperty(
      "source",
    );
    expect(exportBusinessCsvMock.mock.calls[0][0].filters).not.toHaveProperty(
      "limit",
    );
  });

  it("exports trades with account and source filters but no display size", async () => {
    const user = userEvent.setup();
    renderOrders("/orders?account=desk-alpha&source=api");

    await user.click(screen.getByRole("button", { name: /^trades$/i }));
    await user.click(screen.getByRole("button", { name: /export trades csv/i }));
    await user.click(screen.getByRole("button", { name: /^export$/i }));

    await waitFor(() => expect(exportBusinessCsvMock).toHaveBeenCalledTimes(1));
    expect(exportBusinessCsvMock).toHaveBeenCalledWith(
      expect.objectContaining({
        entity: "trades",
        filters: {
          account: "desk-alpha",
          source: "api",
        },
      }),
    );
    expect(exportBusinessCsvMock.mock.calls[0][0].filters).not.toHaveProperty(
      "limit",
    );
  });

  it("downloads using the returned business CSV filename", async () => {
    const user = userEvent.setup();
    const anchors: HTMLAnchorElement[] = [];
    const realCreateElement = document.createElement.bind(document);
    const createElementSpy = vi.spyOn(document, "createElement").mockImplementation(
      ((tagName: string, options?: ElementCreationOptions) => {
        const element = realCreateElement(tagName, options);
        if (tagName === "a") {
          anchors.push(element as HTMLAnchorElement);
        }
        return element;
      }) as typeof document.createElement,
    );
    exportBusinessCsvMock.mockResolvedValueOnce({
      blob: new Blob(["orders"]),
      filename: "orders-export.csv",
    });
    renderOrders("/orders");

    await user.click(screen.getByRole("button", { name: /export orders csv/i }));
    await user.click(screen.getByRole("button", { name: /^export$/i }));

    await waitFor(() => expect(exportBusinessCsvMock).toHaveBeenCalledTimes(1));
    expect(anchors.at(-1)?.download).toBe("orders-export.csv");
    createElementSpy.mockRestore();
  });
});

describe("Execution report status-driven fields", () => {
  function alphaFourOrder() {
    return {
      ...sampleOrder,
      id: "ord-alpha-4",
      status: "filled",
      price: "12",
      leavesQuantity: "2",
      displayPrice: "12",
    };
  }

  const alphaFourTrades: Trade[] = [
    {
      id: "trd-alpha-9",
      order: "ord-alpha-4",
      account: "desk-alpha",
      at: "2026-06-24T11:08:00Z",
      source: "panel",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      side: "buy",
      quantity: "2",
      price: "12",
      lockPrice: "12",
    },
  ];

  it("clones execution-report event commission and resulting status", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();
    const events: OrderEvent[] = [
      {
        id: "evt-alpha-fill",
        order: "ord-alpha-4",
        at: "2026-06-24T11:08:00Z",
        type: "fill",
        source: "panel",
        principal: "",
        fillQuantity: "1",
        fillPrice: "12",
        fillLockPrice: "12",
        leavesQuantity: "1",
        orderStatus: "partially_filled",
        commission: { amount: "-0.50", currency: "USD" },
        signed: false,
      },
    ];

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events,
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByLabelText("Clone execution report from order ord-alpha-4"),
    );

    expect(await screen.findByLabelText("Fill quantity")).toHaveValue("1");
    expect(screen.getByLabelText("Fill price")).toHaveValue("12");
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("1");
    expect(screen.getByLabelText("Commission amount")).toHaveValue("0.50");
    expect(screen.getByRole("button", { name: "Fee" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByLabelText("Commission currency")).toHaveValue("USD");

    await user.click(screen.getByRole("button", { name: "Submit" }));
    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(
        "ord-alpha-4",
        {
          status: "partially_filled",
          quantity: "1",
          price: "12",
          leavesQuantity: "1",
          lockPrice: "12",
          commission: { amount: "-0.50", currency: "USD" },
        },
      ),
    );
  });

  it("clones a filled execution-report event without replacing fill quantity by leaves", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();
    const events: OrderEvent[] = [
      {
        id: "evt-alpha-filled",
        order: "ord-alpha-4",
        at: "2026-06-24T11:09:00Z",
        type: "fill",
        source: "panel",
        principal: "",
        fillQuantity: "1",
        fillPrice: "12",
        fillLockPrice: "12",
        leavesQuantity: "0",
        orderStatus: "filled",
        signed: false,
      },
    ];

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events,
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByLabelText("Clone execution report from order ord-alpha-4"),
    );

    expect(await screen.findByLabelText("Fill quantity")).toHaveValue("1");
    expect(screen.getByLabelText("Fill price")).toHaveValue("12");
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("0");

    await user.click(screen.getByRole("button", { name: "Submit" }));
    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(
        "ord-alpha-4",
        {
          status: "filled",
          quantity: "1",
          price: "12",
          leavesQuantity: "0",
          lockPrice: "12",
        },
      ),
    );
  });

  it("clones a terminal report with its fill settlement fields", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();
    const events: OrderEvent[] = [
      {
        id: "evt-alpha-cancel-fill",
        order: "ord-alpha-4",
        at: "2026-06-24T11:10:00Z",
        type: "fill",
        source: "panel",
        principal: "",
        fillQuantity: "1.5",
        fillPrice: "12",
        fillLockPrice: "12",
        leavesQuantity: "0.5",
        orderStatus: "cancelled",
        commission: { amount: "-0.75", currency: "USD" },
        signed: false,
      },
    ];

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events,
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByLabelText("Clone execution report from order ord-alpha-4"),
    );

    await screen.findByLabelText("Target status");
    expect(screen.getByLabelText("Fill quantity")).toHaveValue("1.5");
    expect(screen.getByLabelText("Fill price")).toHaveValue("12");
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("0.5");
    expect(screen.getByLabelText("Commission amount")).toHaveValue("0.75");
    expect(screen.getByLabelText("Commission currency")).toHaveValue("USD");

    await user.click(screen.getByRole("button", { name: "Submit" }));
    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(
        "ord-alpha-4",
        {
          status: "cancelled",
          quantity: "1.5",
          price: "12",
          leavesQuantity: "0.5",
          lockPrice: "12",
          commission: { amount: "-0.75", currency: "USD" },
        },
      ),
    );
  });

  it("requires leaves for a terminal status without fill fields", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByRole("button", { name: "Submit execution report" }),
    );
    const focusSpy = vi
      .spyOn(HTMLElement.prototype, "focus")
      .mockImplementation(() => {});
    await user.click(await screen.findByLabelText("Target status"));
    for (const label of [
      "Submitted",
      "Accepted",
      "Rejected",
      "Committed",
      "Rolled back",
      "Filled",
      "Partially filled",
      "Cancelled",
    ]) {
      expect(screen.getByRole("option", { name: label })).toBeInTheDocument();
    }
    await user.click(screen.getByRole("option", { name: "Cancelled" }));
    focusSpy.mockRestore();

    // Terminal no-fill reports still settle through the engine.
    expect(screen.queryByLabelText("Fill quantity")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Fill price")).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/lock price/i)).not.toBeInTheDocument();
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("");
    const submit = screen.getByRole("button", { name: "Submit" });
    expect(submit).toBeDisabled();
    expect(submitExecutionReportMock).not.toHaveBeenCalled();
    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("2");
    expect(submit).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "Fee" }));
    await user.type(screen.getByLabelText("Commission amount"), "0.25");
    await user.type(screen.getByLabelText("Commission currency"), "USD");

    await user.click(
      await screen.findByLabelText(
        "Force: bypass the check that prevents reports on finalized orders",
      ),
    );
    await user.click(submit);

    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(
        "ord-alpha-4",
        {
          status: "cancelled",
          leavesQuantity: "2",
          commission: { amount: "-0.25", currency: "USD" },
          force: true,
        },
      ),
    );
  });

  it("calculates leaves quantity for a partial fill", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByRole("button", { name: "Submit execution report" }),
    );

    await screen.findByLabelText("Target status");
    expect(screen.getByLabelText("Fill quantity")).toHaveValue("2");
    expect(screen.getByLabelText("Fill price")).toHaveValue("12");
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("");

    const focusSpy = vi
      .spyOn(HTMLElement.prototype, "focus")
      .mockImplementation(() => {});
    await user.click(screen.getByLabelText("Target status"));
    await user.click(screen.getByRole("option", { name: "Partially filled" }));
    focusSpy.mockRestore();

    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("");
    await user.clear(screen.getByLabelText("Fill quantity"));
    await user.type(screen.getByLabelText("Fill quantity"), "0.5");
    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("1.5");
    await user.click(screen.getByRole("button", { name: "Submit" }));

    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(
        "ord-alpha-4",
        {
          status: "partially_filled",
          quantity: "0.5",
          price: "12",
          leavesQuantity: "1.5",
          lockPrice: "12",
        },
      ),
    );
  });

  it("calculates leaves from the order quantity before a leaves value is stored", async () => {
    const user = userEvent.setup();
    const order = {
      ...alphaFourOrder(),
      amountValue: "3",
      leavesQuantity: "",
    };

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByRole("button", { name: "Submit execution report" }),
    );

    expect(screen.getByLabelText("Fill quantity")).toHaveValue("3");
    expect(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    ).toBeEnabled();
  });

  it("requires explicit leaves for the filled status and can calculate zero", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByRole("button", { name: "Submit execution report" }),
    );

    await screen.findByLabelText("Target status");
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("");
    const submit = screen.getByRole("button", { name: "Submit" });
    expect(submit).toBeDisabled();
    expect(submitExecutionReportMock).not.toHaveBeenCalled();

    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("0");
    expect(submit).toBeEnabled();
    await user.click(submit);

    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(
        "ord-alpha-4",
        {
          status: "filled",
          quantity: "2",
          price: "12",
          leavesQuantity: "0",
          lockPrice: "12",
        },
      ),
    );
  });

  it("submits structured commission when all optional fields are set", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByRole("button", { name: "Submit execution report" }),
    );

    await screen.findByLabelText("Target status");
    expect(screen.getAllByText("Commission").length).toBeGreaterThan(0);
    await user.click(screen.getByRole("button", { name: "Commission details" }));
    expect(
      await screen.findByText(
        /Commission is entered in its own currency/,
      ),
    ).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );
    expect(screen.queryByLabelText("Commission amount")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Fee" }));
    await user.type(
      screen.getByLabelText("Commission amount"),
      "-0.50",
    );
    expect(screen.getByLabelText("Commission amount")).toHaveValue("0.50");
    await user.type(screen.getByLabelText("Commission currency"), " USD ");
    await user.click(screen.getByRole("button", { name: "Submit" }));

    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(
        "ord-alpha-4",
        {
          status: "filled",
          quantity: "2",
          price: "12",
          leavesQuantity: "0",
          lockPrice: "12",
          commission: { amount: "-0.50", currency: "USD" },
        },
      ),
    );
  });

  it("disables submit while a structured commission is incomplete", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByRole("button", { name: "Submit execution report" }),
    );

    await screen.findByLabelText("Target status");
    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );
    await user.click(screen.getByRole("button", { name: "Fee" }));
    await user.type(
      screen.getByLabelText("Commission amount"),
      "0.50",
    );
    expect(screen.getByRole("button", { name: "Submit" })).toBeDisabled();
    expect(submitExecutionReportMock).not.toHaveBeenCalled();
  });

  it("disables submit while a visible numeric draft is invalid", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByRole("button", { name: "Submit execution report" }),
    );
    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );

    const submit = screen.getByRole("button", { name: "Submit" });
    expect(submit).toBeEnabled();
    const quantity = screen.getByLabelText("Fill quantity");
    await user.type(quantity, "x");

    expect(quantity).toHaveValue("2x");
    expect(quantity).toBeInvalid();
    expect(submit).toBeDisabled();
    expect(submitExecutionReportMock).not.toHaveBeenCalled();
  });

  it("requires positive fill economics while allowing zero leaves and commission", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByRole("button", { name: "Submit execution report" }),
    );
    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );

    const submit = screen.getByRole("button", { name: "Submit" });
    const quantity = screen.getByLabelText("Fill quantity");
    const price = screen.getByLabelText("Fill price");
    const lockPrice = screen.getByLabelText("Lock price (optional)");
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("0");
    expect(submit).toBeEnabled();

    await user.clear(quantity);
    await user.type(quantity, "0");
    expect(submit).toBeDisabled();
    await user.clear(quantity);
    await user.type(quantity, "2");
    expect(submit).toBeEnabled();

    await user.clear(price);
    await user.type(price, "0");
    expect(submit).toBeDisabled();
    await user.clear(price);
    await user.type(price, "12");
    expect(submit).toBeEnabled();

    await user.clear(lockPrice);
    await user.type(lockPrice, "0");
    expect(submit).toBeDisabled();
    await user.clear(lockPrice);
    expect(submit).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "Fee" }));
    await user.type(screen.getByLabelText("Commission amount"), "0");
    await user.type(screen.getByLabelText("Commission currency"), "USD");
    expect(submit).toBeEnabled();
    expect(submitExecutionReportMock).not.toHaveBeenCalled();
  });

  it("reflects the server's post-report state, not the requested one", async () => {
    const user = userEvent.setup();
    const order = { ...alphaFourOrder(), status: "partially_filled" as const };
    // The engine's resulting state, published once the page refetches. It
    // differs from the requested "filled" status, so the table must show THIS
    // value rather than optimistically echoing the request.
    const serverResolved = {
      ...order,
      status: "committed" as const,
      leavesQuantity: "1",
    };

    // reload() swaps the polled data to the engine's resolved row, mimicking a
    // real refetch; the page re-renders and picks up the new server state.
    const swapToResolved = vi.fn(() => {
      useOrdersMock.mockReturnValue(readyPage<Order>([serverResolved]));
    });
    useOrdersMock.mockReturnValue({
      load: { state: "ready", data: { items: [order], total: 1 }, error: null },
      reload: swapToResolved,
    });
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: [],
      approval: null,
    });
    fetchOrderDetailMock.mockResolvedValueOnce({
      order: serverResolved,
      events: [],
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    const row = screen.getByText("ord-alpha-4").closest("tr");
    expect(row).not.toBeNull();
    expect(
      within(row as HTMLElement).getByText("partially_filled"),
    ).toBeInTheDocument();

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByRole("button", { name: "Submit execution report" }),
    );
    await screen.findByLabelText("Target status");
    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );
    await user.click(screen.getByRole("button", { name: "Submit" }));

    await waitFor(() => expect(swapToResolved).toHaveBeenCalled());
    // The row shows the engine's resolved status, never the requested "filled".
    await waitFor(() =>
      expect(
        within(row as HTMLElement).getByText("committed"),
      ).toBeInTheDocument(),
    );
    expect(
      within(row as HTMLElement).queryByText("filled"),
    ).not.toBeInTheDocument();
    await waitFor(() => expect(fetchOrderDetailMock).toHaveBeenCalledTimes(2));
    await user.click(screen.getByRole("button", { name: "Done" }));
    const detailDialog = screen.getByRole("dialog", {
      name: "Order ord-alpha-4",
    });
    expect(within(detailDialog).getByText("committed")).toBeInTheDocument();
  });

  it("submits a non-terminal workflow status without leaves", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByRole("button", { name: "Submit execution report" }),
    );
    const focusSpy = vi
      .spyOn(HTMLElement.prototype, "focus")
      .mockImplementation(() => {});
    await user.click(await screen.findByLabelText("Target status"));
    await user.click(screen.getByRole("option", { name: "Accepted" }));
    focusSpy.mockRestore();

    // Non-fill workflow statuses do not carry engine settlement fields.
    expect(screen.queryByLabelText("Fill quantity")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Leaves quantity")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Submit" }));

    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(
        "ord-alpha-4",
        { status: "accepted" },
      ),
    );
  });

  it("preserves commission and requires leaves after switching to a workflow status", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByRole("button", { name: "Submit execution report" }),
    );

    await user.click(screen.getByRole("button", { name: "Fee" }));
    await user.type(screen.getByLabelText("Commission amount"), "0.50");
    await user.type(screen.getByLabelText("Commission currency"), "USD");

    const focusSpy = vi
      .spyOn(HTMLElement.prototype, "focus")
      .mockImplementation(() => {});
    await user.click(await screen.findByLabelText("Target status"));
    await user.click(screen.getByRole("option", { name: "Accepted" }));
    focusSpy.mockRestore();

    expect(screen.queryByLabelText("Fill quantity")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Fill price")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Commission amount")).toHaveValue("0.50");
    expect(screen.getByLabelText("Commission currency")).toHaveValue("USD");
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("");
    const submit = screen.getByRole("button", { name: "Submit" });
    expect(submit).toBeDisabled();
    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("2");
    expect(submit).toBeEnabled();
    await user.click(submit);

    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(
        "ord-alpha-4",
        {
          status: "accepted",
          leavesQuantity: "2",
          commission: { amount: "-0.50", currency: "USD" },
        },
      ),
    );
  });

  it("drops cloned fill fields when switching to a workflow status", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: alphaFourTrades,
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByLabelText("Clone execution report for trade trd-alpha-9"),
    );
    const focusSpy = vi
      .spyOn(HTMLElement.prototype, "focus")
      .mockImplementation(() => {});
    await user.click(await screen.findByLabelText("Target status"));
    await user.click(screen.getByRole("option", { name: "Accepted" }));
    focusSpy.mockRestore();

    expect(screen.queryByLabelText("Fill quantity")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Fill price")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Leaves quantity")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Submit" }));

    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(
        "ord-alpha-4",
        { status: "accepted" },
      ),
    );
  });

  it("seeds the order leaves when cloning from a trade row so calculate works", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: alphaFourTrades,
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByLabelText("Clone execution report for trade trd-alpha-9"),
    );

    await screen.findByLabelText("Target status");
    // Fill fields carry the trade; leaves opens empty for the operator.
    expect(screen.getByLabelText("Fill quantity")).toHaveValue("2");
    expect(screen.getByLabelText("Fill price")).toHaveValue("12");
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("");

    // The order's remaining leaves (2) seeds the calculator: 2 - 2 = 0.
    const calculate = screen.getByRole("button", {
      name: "Calculate leaves quantity",
    });
    expect(calculate).toBeEnabled();
    await user.click(calculate);
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("0");

    await user.click(screen.getByRole("button", { name: "Submit" }));
    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith("ord-alpha-4", {
        status: "filled",
        quantity: "2",
        price: "12",
        leavesQuantity: "0",
        lockPrice: "12",
      }),
    );
  });

  it("keeps the parent dialog's modality lock when a nested info dialog closes", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades: [],
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByRole("button", { name: "Submit execution report" }),
    );
    await screen.findByLabelText("Target status");

    // Radix locks background pointer events while the modal chain is open.
    expect(document.body.style.pointerEvents).toBe("none");

    // Open a nested info dialog inside the exec-report dialog, then close only
    // that inner dialog.
    await user.click(
      screen.getByRole("button", { name: "Commission details" }),
    );
    const info = await screen.findByRole("dialog", { name: "Commission" });
    await user.keyboard("{Escape}");
    await waitFor(() => expect(info).not.toBeInTheDocument());

    // The still-open parent dialog must keep the modality lock so it keeps
    // receiving clicks; the inner close must not clear it.
    expect(screen.getByLabelText("Target status")).toBeInTheDocument();
    await waitFor(() =>
      expect(document.body.style.pointerEvents).toBe("none"),
    );
  });
});

describe("Orders filter, debounce, sort and pagination", () => {
  it("ignores the legacy externalId trade query parameter", async () => {
    renderOrders("/orders?externalId=legacy-trade");

    await waitFor(() => expect(lastTradeFilters()?.id).toBeUndefined());
  });

  it("applies an identity filter only after Enter", async () => {
    vi.useFakeTimers();
    fetchAccountsMock.mockResolvedValueOnce([{ code: "desk-zeta" }]);
    renderOrders("/orders");

    const accountInput = screen.getByPlaceholderText(/filter by account/i);
    fireEvent.change(accountInput, { target: { value: "desk-zeta" } });

    expect(
      orderFilterWasRequested((f) => f.account === "desk-zeta"),
    ).toBe(false);

    act(() => {
      vi.advanceTimersByTime(299);
    });
    expect(
      orderFilterWasRequested((f) => f.account === "desk-zeta"),
    ).toBe(false);

    await act(async () => {
      vi.advanceTimersByTime(1);
      await Promise.resolve();
    });
    expect(
      screen.getByRole("option", { name: "desk-zeta" }),
    ).toBeInTheDocument();
    expect(
      orderFilterWasRequested((f) => f.account === "desk-zeta"),
    ).toBe(false);

    act(() => {
      fireEvent.keyDown(accountInput, { key: "Enter" });
      vi.runOnlyPendingTimers();
    });
    expect(
      orderFilterWasRequested((f) => f.account === "desk-zeta"),
    ).toBe(true);
  });

  it("applies an order account suggestion when it is selected", async () => {
    const user = userEvent.setup();
    fetchAccountsMock.mockResolvedValueOnce([{ code: "desk-alpha" }]);
    renderOrders("/orders");

    await user.type(screen.getByPlaceholderText(/filter by account/i), "des");
    await user.click(await screen.findByRole("option", { name: "desk-alpha" }));

    await waitFor(() =>
      expect(lastOrderFilters()).toEqual(
        expect.objectContaining({ account: "desk-alpha" }),
      ),
    );
  });

  it("applies the first order account suggestion on Enter", async () => {
    const user = userEvent.setup();
    fetchAccountsMock.mockResolvedValueOnce([{ code: "desk-alpha" }]);
    renderOrders("/orders");

    await user.type(screen.getByPlaceholderText(/filter by account/i), "DES");
    await screen.findByRole("option", { name: "desk-alpha" });
    await user.keyboard("{Enter}");

    await waitFor(() =>
      expect(lastOrderFilters()).toEqual(
        expect.objectContaining({ account: "desk-alpha" }),
      ),
    );
  });

  it("applies all edited order identity filters when Enter accepts a suggestion", async () => {
    const user = userEvent.setup();
    fetchAssetsMock.mockResolvedValueOnce([{ code: "AAPL" }]);
    renderOrders("/orders");

    await user.type(
      screen.getByPlaceholderText(/filter by account/i),
      "desk-alpha",
    );
    await user.type(screen.getByPlaceholderText("Base"), "AA");
    await screen.findByRole("option", { name: "AAPL" });
    await user.keyboard("{Enter}");

    await waitFor(() =>
      expect(lastOrderFilters()).toEqual(
        expect.objectContaining({
          account: "desk-alpha",
          baseAsset: "AAPL",
        }),
      ),
    );
  });

  it("applies a trade asset suggestion when it is selected", async () => {
    const user = userEvent.setup();
    fetchAssetsMock.mockResolvedValueOnce([{ code: "AAPL" }]);
    renderOrders("/orders?tab=trades");
    await user.click(screen.getByRole("button", { name: /^trades$/i }));

    await user.type(screen.getByPlaceholderText("Base"), "AA");
    await user.click(await screen.findByRole("option", { name: "AAPL" }));

    await waitFor(() =>
      expect(lastTradeFilters()).toEqual(
        expect.objectContaining({ baseAsset: "AAPL" }),
      ),
    );
  });

  it("resets to the first page when a filter changes", async () => {
    const user = userEvent.setup();
    useOrdersMock.mockReturnValue(readyPageWithTotal<Order>([sampleOrder], 120));
    renderOrders("/orders");

    // Page size is 50, total 120 → advance to the second page first.
    await user.click(screen.getAllByRole("button", { name: "Next" })[0]);
    await waitFor(() => expect(lastOrderFilters()?.offset).toBe(50));

    // Any filter change must drop back to offset 0.
    const baseInput = screen.getByPlaceholderText("Base");
    fireEvent.change(baseInput, {
      target: { value: "AAPL" },
    });
    fireEvent.keyDown(baseInput, { key: "Enter" });
    await waitFor(() => expect(lastOrderFilters()?.offset).toBe(0));
  });

  it("keeps the active sort across a filter change", async () => {
    useOrdersMock.mockReturnValue(readyPageWithTotal<Order>([sampleOrder], 120));
    renderOrders("/orders");

    // Default sort is at/desc; it must survive a filter edit.
    expect(lastOrderFilters()).toMatchObject({ sort: "at", order: "desc" });

    const baseInput = screen.getByPlaceholderText("Base");
    fireEvent.change(baseInput, {
      target: { value: "AAPL" },
    });
    fireEvent.keyDown(baseInput, { key: "Enter" });
    await waitFor(() =>
      expect(lastOrderFilters()).toMatchObject({ sort: "at", order: "desc" }),
    );
  });

  it("opens the detail dialog on an exact-lookup hit", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await user.type(
      screen.getByPlaceholderText(/^exact id$/i),
      "ord-alpha-1",
    );
    await user.click(screen.getByRole("button", { name: /^open$/i }));

    expect(
      await screen.findByRole("dialog", { name: "Order ord-alpha-1" }),
    ).toBeInTheDocument();
    expect(fetchOrderDetailMock).toHaveBeenCalledWith("ord-alpha-1");
  });

  it("shows the not-found dialog on an exact-lookup miss", async () => {
    const user = userEvent.setup();
    fetchOrderDetailMock.mockRejectedValueOnce(new Error("not found"));
    renderOrders("/orders");

    await user.type(
      screen.getByPlaceholderText(/^exact id$/i),
      "ord-missing-9",
    );
    await user.click(screen.getByRole("button", { name: /^open$/i }));

    expect(
      await screen.findByText(/no order with id ord-missing-9/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("dialog", { name: "Order ord-missing-9" }),
    ).not.toBeInTheDocument();
  });

  it("reads the server total to drive pagination", async () => {
    const user = userEvent.setup();
    useOrdersMock.mockReturnValue(readyPageWithTotal<Order>([sampleOrder], 120));
    renderOrders("/orders");

    // 120 rows at a page size of 50 → three known pages.
    expect(screen.getAllByText("Page 1 of 3").length).toBeGreaterThan(0);

    const next = screen.getAllByRole("button", { name: "Next" })[0];
    expect(next).toBeEnabled();
    await user.click(next);

    await waitFor(() => expect(lastOrderFilters()?.offset).toBe(50));
  });
});

describe("Orders verify token toolbar", () => {
  it("opens the standalone verify-token panel from the toolbar", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await user.click(screen.getByRole("button", { name: "Verify token" }));

    // The panel opens in verify mode with only the verify tab and its paste box.
    expect(
      await screen.findByPlaceholderText("Paste a base64url token…"),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("tab", { name: "Reproduction" }),
    ).not.toBeInTheDocument();
  });
});
