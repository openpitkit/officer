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
  BusinessCsvImportEntity,
  Order,
  OrderEvent,
  OrderListFilters,
  Trade,
} from "@/api/types";
import type { PollingResult } from "@/api/usePolling";
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
const submitExecutionReportMock = vi.fn();

const proto = window.HTMLElement.prototype as HTMLElement & {
  hasPointerCapture?: (pointerId: number) => boolean;
  setPointerCapture?: (pointerId: number) => void;
};
proto.hasPointerCapture = () => false;
proto.setPointerCapture = () => {};
proto.scrollIntoView = () => {};

const sampleOrder: Order = {
  externalId: "ord-alpha-1",
  account: "desk-alpha",
  at: "2026-06-24T00:00:00Z",
  source: "panel",
  baseAsset: "AAPL",
  quoteAsset: "USD",
  side: "buy",
  amountKind: "quantity",
  amountValue: "100",
  price: "0",
  status: "accepted",
  displayPrices: [],
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
        submitExecutionReport: submitExecutionReportMock,
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
    wouldDisplayPrices: [],
    wouldBlock: null,
  });
  createOrderMock.mockResolvedValue({ order: sampleOrder });
  exportBusinessCsvMock.mockResolvedValue({
    blob: new Blob(["csv"]),
    filename: "business.csv",
  });
  submitExecutionReportMock.mockResolvedValue({ blocks: [] });
  fetchOrderDetailMock.mockResolvedValue({
    order: sampleOrder,
    events: [],
    trades: [],
    approval: null,
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

  it("keeps advanced order filters open when a numeric value is invalid", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await user.click(screen.getByRole("button", { name: /more filters/i }));
    const dialog = screen.getByRole("dialog", { name: /more filters/i });
    const amount = within(dialog).getAllByPlaceholderText("Value")[0];
    await user.type(amount, "word");

    await user.click(
      within(dialog).getByRole("button", {
        name: /apply advanced filter/i,
      }),
    );

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

describe("Orders side & amount-kind toggles", () => {
  it("renders side as buy/sell radio buttons with buy preselected", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);

    const sideGroup = within(dialog).getByRole("radiogroup", { name: "Side" });
    expect(within(sideGroup).getByRole("radio", { name: "Buy" })).toHaveAttribute(
      "aria-checked",
      "true",
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

  it("keeps submit disabled until amount kind and submit mode are chosen", async () => {
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

    // Text fields filled, but neither amount kind nor mode chosen yet.
    expect(submitButton).toBeDisabled();

    const kindGroup = within(dialog).getByRole("radiogroup", { name: "Amount kind" });
    await user.click(within(kindGroup).getByRole("radio", { name: /quantity/i }));
    // Amount kind chosen, mode still missing.
    expect(submitButton).toBeDisabled();

    const modeGroup = within(dialog).getByRole("radiogroup", { name: "Submit mode" });
    await user.click(within(modeGroup).getByRole("radio", { name: /record executed trade/i }));
    // Everything chosen — submission is unblocked.
    expect(submitButton).toBeEnabled();

    await user.click(submitButton);
    await waitFor(() => expect(createOrderMock).toHaveBeenCalledTimes(1));
    const submitted = createOrderMock.mock.calls[0][0];
    const signal = createOrderMock.mock.calls[0][1];
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
    expect(signal).toBeInstanceOf(AbortSignal);
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
    await user.click(within(dialog).getByRole("radio", { name: /quantity/i }));
    await user.click(within(dialog).getByRole("radio", { name: /record executed trade/i }));
    await user.click(within(dialog).getByRole("button", { name: /add order/i }));

    expect(await screen.findByText(warning)).toBeInTheDocument();
  });

  it("clears busy after aborting an in-flight submit", async () => {
    const user = userEvent.setup();
    createOrderMock.mockImplementation((_body, signal) => (
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
    await user.click(within(dialog).getByRole("radio", { name: /quantity/i }));
    await user.click(within(dialog).getByRole("radio", { name: /record executed trade/i }));
    await user.click(within(dialog).getByRole("button", { name: /add order/i }));
    await waitFor(() => expect(createOrderMock).toHaveBeenCalledTimes(1));

    await user.click(within(dialog).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    dialog = await openDialog(user);
    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    await user.click(within(dialog).getByRole("radio", { name: /quantity/i }));
    await user.click(within(dialog).getByRole("radio", { name: /record executed trade/i }));

    expect(within(dialog).getByRole("button", { name: /add order/i })).toBeEnabled();
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
          externalId: "trd-alpha-1",
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
    const order = { ...sampleOrder, externalId: "ord-alpha-3", status: "filled", price: "99" };
    const events: OrderEvent[] = [
      {
        externalId: "evt-alpha-1",
        order: "ord-alpha-3",
        at: "2026-06-24T11:07:00Z",
        type: "submitted",
        source: "panel",
        principal: "",
      },
      {
        externalId: "evt-alpha-2",
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

describe("Execution report force submit", () => {
  it("sends force when submitting a cloned execution report with force enabled", async () => {
    const user = userEvent.setup();
    const order = {
      ...sampleOrder,
      externalId: "ord-alpha-4",
      status: "filled",
      price: "12",
      displayPrices: ["12"],
    };
    const trades: Trade[] = [
      {
        externalId: "trd-alpha-9",
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

    useOrdersMock.mockReturnValue(readyPage<Order>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events: [],
      trades,
      approval: null,
    });
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-4"));
    await screen.findByRole("dialog", { name: "Order ord-alpha-4" });
    await user.click(
      screen.getByLabelText("Clone execution report for trade trd-alpha-9"),
    );
    await user.click(
      await screen.findByLabelText(
        "Force: bypass Officer checks, send straight to the engine",
      ),
    );
    await user.click(screen.getByRole("button", { name: "Submit" }));

    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(
        "ord-alpha-4",
        expect.objectContaining({
          quantity: "2",
          price: "12",
          lockPrice: "12",
          force: true,
          final: true,
        }),
      ),
    );
  });
});

describe("Orders filter, debounce, sort and pagination", () => {
  it("applies an identity filter only after Enter", () => {
    vi.useFakeTimers();
    fetchAccountsMock.mockReturnValueOnce(new Promise(() => {}));
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

    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(
      orderFilterWasRequested((f) => f.account === "desk-zeta"),
    ).toBe(false);

    act(() => {
      fireEvent.keyDown(accountInput, { key: "Enter" });
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
