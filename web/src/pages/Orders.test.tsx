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

import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithApi as render } from "@/test/apiClient";
import { filterSetShare, shareLinkParams } from "@/test/pageHelpers";
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
  TradingDestination,
  VenueOrder,
} from "@/api/types";
import type { PollingResult } from "@/api/usePolling";
import { ApiError, type ApiErrorCode } from "@/framework";
import { badgeVariants } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button";
import { SidebarProvider } from "@/components/SidebarContext";
import i18n from "@/i18n";
import { Orders } from "@/pages/Orders";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";
import { setAccountScope } from "@/lib/accountScope";
import { setGlobalAccountFilter } from "@/lib/globalAccountFilter";
import { DEFAULT_SEARCH_DEBOUNCE_MS } from "@/lib/useDebounce";

// Replace the polling hooks with ready-empty stubs and the one-shot account
// fetch so the page renders without any network traffic.
vi.mock("@/api/useOrders", () => ({ useOrdersPage: vi.fn() }));
vi.mock("@/api/useTrades", () => ({ useTradesPage: vi.fn() }));
vi.mock("@/api/useBalances", () => ({ useBalances: vi.fn() }));
vi.mock("@/framework", async () => {
  const actual =
    await vi.importActual<typeof import("@/framework")>("@/framework");
  const { VenueFilterStandIn } = await import("@/test/venueFilterStandIn");
  return {
    ...actual,
    ShareLinkButton: ({ href, title }: { href: string; title?: string }) => (
      <a href={href} aria-label={title}>
        {title}
      </a>
    ),
    VenueFilter: VenueFilterStandIn,
  };
});
// The page chrome renders PendingRestartBanner, which polls market data on
// mount. Stub it to a settled, no-restart state so its async update does not
// fire outside act() in synchronous renders.
vi.mock("@/api/useMarketData", () => ({
  useMarketData: () => ({
    load: { state: "ready", data: { restartRequired: false }, error: null },
    reload: () => {},
  }),
}));
// SensitiveDataBanner polls GET /credential-storage.
vi.mock("@/api/useCredentialStorage", () => ({
  useCredentialStorage: () => ({
    load: {
      state: "ready",
      data: {
        credentialsSealed: true,
        plaintextTradingKeys: false,
        plaintextMarketDataKeys: false,
      },
      error: null,
    },
    reload: () => {},
  }),
}));
vi.mock("@/api/useTrading", () => ({
  useTrading: vi.fn(() => ({
    load: {
      state: "ready",
      data: {
        providers: [
          {
            type: "alpaca",
            title: "Alpaca",
            venueAccounts: true,
            verifiesSymbols: true,
            searchesSymbols: true,
            unlinkedFees: true,
          },
        ],
        connections: [
          {
            id: "alpaca-paper",
            provider: "alpaca",
            label: "Primary Alpaca",
            mode: "paper",
            enabled: true,
            venueIdentity: "PA-1",
            state: "running",
            secrets: { apiKey: true, apiSecret: true },
            instruments: [],
            access: [],
            diagnostics: [],
            feeAccount: "",
          },
        ],
      },
      error: null,
    },
    reload: () => {},
  })),
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

import { useBalances } from "@/api/useBalances";
import { useOrdersPage } from "@/api/useOrders";
import { useTradesPage } from "@/api/useTrades";
import { useTrading } from "@/api/useTrading";
import {
  VENUE_FILTER_ANY,
  VENUE_FILTER_NONE,
  venueConnectionFilterValue,
} from "@/components/data";

function readyEmpty<T>(data: T): PollingResult<T> {
  return { load: { state: "ready", data, error: null }, reload: vi.fn() };
}

function readyPage<T>(
  items: T[],
): PollingResult<{ items: T[]; total: number }> {
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
const useTradingMock = vi.mocked(useTrading);
const defaultTradingResult = useTradingMock.getMockImplementation()!();

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
const fetchTradingDestinationsMock = vi.fn();
const fetchOrderDetailMock = vi.fn();
const fetchEventReproductionMock = vi.fn();
const fetchPublicKeyByIdMock = vi.fn();
const submitExecutionReportMock = vi.fn();
const confirmOrderMock = vi.fn();
const cancelOrderMock = vi.fn();
const sendPaperOrderMock = vi.fn();

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

const sampleTrade: Trade = {
  id: "trade-venue-1",
  order: sampleOrder.id,
  account: sampleOrder.account,
  at: sampleOrder.at,
  source: sampleOrder.source,
  baseAsset: sampleOrder.baseAsset,
  quoteAsset: sampleOrder.quoteAsset,
  side: sampleOrder.side,
  quantity: "1",
  price: "100",
  lockPrice: "",
};

const alpacaDestination: TradingDestination = {
  connection: "alpaca-paper",
  label: "Primary Alpaca",
  provider: "alpaca",
  mode: "paper",
  enabled: true,
  venueAccount: "PA-1",
  state: "running",
};

const linkedVenueOrder: VenueOrder = {
  connection: "alpaca-paper",
  venueAccount: "PA-1",
  route: { timeInForce: "day" },
  clientOrderId: sampleOrder.id,
  venueOrderId: "venue-123",
  sendAttempted: true,
  createdAt: "2026-06-24T00:01:00Z",
};

function removeStoredPreference(key: string) {
  try {
    window.localStorage?.removeItem(key);
  } catch {
    // Test environments may expose no localStorage; the app falls back to cookies.
  }
  document.cookie = `${encodeURIComponent(key)}=; Max-Age=0; Path=/; SameSite=Lax`;
}

function setStoredPreference(key: string, value: string) {
  try {
    window.localStorage?.setItem(key, value);
  } catch {
    // Keep the cookie mirror in sync with the production storage fallback.
  }
  document.cookie = `${encodeURIComponent(key)}=${encodeURIComponent(value)}; Path=/; SameSite=Lax`;
}

function renderOrders(initialEntry: string) {
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-test-density"
          tradeStyleStorageKey="pit-test-style"
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
        fetchTradingDestinations: fetchTradingDestinationsMock,
        fetchOrderDetail: fetchOrderDetailMock,
        fetchEventReproduction: fetchEventReproductionMock,
        fetchPublicKeyById: fetchPublicKeyByIdMock,
        submitExecutionReport: submitExecutionReportMock,
        confirmOrder: confirmOrderMock,
        cancelOrder: cancelOrderMock,
        sendPaperOrder: sendPaperOrderMock,
      },
    },
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  useTradingMock.mockReturnValue(defaultTradingResult);
  await i18n.changeLanguage("en");
  useOrdersMock.mockReturnValue(readyPage<Order>([]));
  useTradesMock.mockReturnValue(readyPage<Trade>([]));
  useBalancesMock.mockReturnValue(readyEmpty<Balance[]>([]));
  setGlobalAccountFilter("");
  removeStoredPreference("pit-officer-orders-submit-tab");
  removeStoredPreference("pit-officer-orders-venue-mode");
  fetchAccountsMock.mockResolvedValue([]);
  fetchAssetsMock.mockResolvedValue([]);
  fetchTradingDestinationsMock.mockResolvedValue([]);
  checkOrderMock.mockResolvedValue({
    passed: true,
    rejects: [],
    wouldDisplayPrice: "",
    wouldBlock: null,
  });
  createOrderMock.mockResolvedValue({
    order: sampleOrder,
    approval: {
      signedApproval: "hold-envelope",
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
    signedAttestation: "",
    attestationKeyId: "",
    signed: false,
  });
  confirmOrderMock.mockResolvedValue({
    order: { ...sampleOrder, status: "committed" },
    signedAttestation: "",
    attestationKeyId: "",
    signed: false,
  });
  cancelOrderMock.mockResolvedValue({
    order: { ...sampleOrder, status: "rolled_back" },
    signedAttestation: "",
    attestationKeyId: "",
    signed: false,
  });
  sendPaperOrderMock.mockResolvedValue({
    signedApproval: "paper-envelope",
    keyId: "key-1",
    id: sampleOrder.id,
    verdict: "accept",
    reasons: [],
    venueSend: {
      status: "acknowledged",
      venueOrder: linkedVenueOrder,
    },
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

describe("Orders page tabs", () => {
  it("switches from orders to trades and back by clicking tabs", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    expect(
      screen.getByRole("tabpanel", { name: /^orders$/i }),
    ).toHaveTextContent(i18n.t("orders:empty.orders.title"));
    expect(
      screen.queryByRole("tabpanel", { name: /^trades$/i }),
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: /^trades$/i }));
    expect(
      screen.getByRole("tabpanel", { name: /^trades$/i }),
    ).toHaveTextContent(i18n.t("orders:empty.trades.title"));
    expect(
      screen.queryByRole("tabpanel", { name: /^orders$/i }),
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: /^orders$/i }));
    expect(
      screen.getByRole("tabpanel", { name: /^orders$/i }),
    ).toHaveTextContent(i18n.t("orders:empty.orders.title"));
    expect(
      screen.queryByRole("tabpanel", { name: /^trades$/i }),
    ).not.toBeInTheDocument();
  });
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

    expect(
      screen.getByRole("dialog", { name: /more filters/i }),
    ).toBeInTheDocument();
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
    await user.click(
      screen.getByRole("button", { name: /clear all filters/i }),
    );
    expect(screen.getByPlaceholderText(/filter by account/i)).toHaveValue("");
    expect(screen.queryByText(/active filters/i)).not.toBeInTheDocument();
  });

  it("pre-fills the new-order account when a single account is filtered", async () => {
    const user = userEvent.setup();
    renderOrders("/orders?account=desk-alpha");

    await user.click(screen.getAllByRole("button", { name: /add order/i })[0]);

    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("tab", { name: "Submit and settle" }),
    );
    expect(within(dialog).getByLabelText("Account")).toHaveValue("desk-alpha");
  });

  it("opens a blank new-order form when no account filter is set", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await user.click(screen.getAllByRole("button", { name: /add order/i })[0]);

    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("tab", { name: "Submit and settle" }),
    );
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
    await user.click(
      within(dialog).getByRole("tab", { name: "Submit and settle" }),
    );
    expect(within(dialog).getByLabelText("Account")).toHaveValue("");
  });
});

async function openDialog(
  user: ReturnType<typeof userEvent.setup>,
  pickTab = true,
) {
  await user.click(screen.getAllByRole("button", { name: /add order/i })[0]);
  const dialog = await screen.findByRole("dialog");
  if (
    pickTab &&
    within(dialog).queryByRole("tab", { selected: true }) === null
  ) {
    await user.click(
      within(dialog).getByRole("tab", { name: "Submit and settle" }),
    );
  }
  return dialog;
}

async function fillOrderFields(
  user: ReturnType<typeof userEvent.setup>,
  dialog: HTMLElement,
) {
  await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
  await fillOrderFieldsAfterAccount(user, dialog);
}

async function fillOrderFieldsAfterAccount(
  user: ReturnType<typeof userEvent.setup>,
  dialog: HTMLElement,
) {
  await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
  await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
  await user.click(within(dialog).getByRole("button", { name: "Buy" }));
  await user.click(
    within(dialog).getByRole("button", { name: "Quantity (base)" }),
  );
  await user.type(within(dialog).getByLabelText("Amount"), "10");
}

async function expectPriceRequired(
  user: ReturnType<typeof userEvent.setup>,
  dialog: HTMLElement,
  submit: HTMLElement,
  label: string,
  hint: string | RegExp,
) {
  const price = within(dialog).getByLabelText(label);
  expect(price).toHaveAttribute("placeholder", "required");
  expect(within(dialog).getByText(hint)).toBeInTheDocument();
  expect(submit).toBeDisabled();
  await user.type(price, "0");
  expect(submit).toBeDisabled();
  await user.clear(price);
  await user.type(price, "10");
  expect(submit).toBeEnabled();
}

async function enablePaperSending(
  user: ReturnType<typeof userEvent.setup>,
  dialog: HTMLElement,
) {
  await user.click(within(dialog).getByRole("tab", { name: "Send to venue" }));
  await user.click(within(dialog).getByRole("button", { name: "Paper" }));
}

async function chooseTimeInForce(
  user: ReturnType<typeof userEvent.setup>,
  dialog: HTMLElement,
  option: string,
) {
  const focusSpy = vi
    .spyOn(HTMLElement.prototype, "focus")
    .mockImplementation(() => {});
  try {
    const select = await within(dialog).findByRole("combobox", {
      name: "Time in force",
    });
    await user.click(select);
    await user.click(await screen.findByRole("option", { name: option }));
  } finally {
    focusSpy.mockRestore();
  }
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

    await waitFor(() =>
      expect(lastOrderFilters()).not.toHaveProperty("status"),
    );
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
  it("renders side as buy/sell segmented buttons without a default selection", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);

    const sideGroup = within(dialog).getByRole("group", { name: "Side" });
    expect(
      within(sideGroup).getByRole("button", { name: "Buy" }),
    ).toHaveAttribute("aria-pressed", "false");
    expect(
      within(sideGroup).getByRole("button", { name: "Sell" }),
    ).toHaveAttribute("aria-pressed", "false");
  });

  it("does not pre-select an amount kind", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);

    const kindGroup = within(dialog).getByRole("group", {
      name: "Amount kind",
    });
    for (const button of within(kindGroup).getAllByRole("button")) {
      expect(button).toHaveAttribute("aria-pressed", "false");
    }
  });

  it("keeps active account prefill without side or amount-kind defaults", async () => {
    const user = userEvent.setup();
    renderOrders("/orders?account=desk-alpha");
    const dialog = await openDialog(user);

    expect(within(dialog).getByLabelText("Account")).toHaveValue("desk-alpha");

    const sideGroup = within(dialog).getByRole("group", { name: "Side" });
    expect(
      within(sideGroup).getByRole("button", { name: "Buy" }),
    ).toHaveAttribute("aria-pressed", "false");
    expect(
      within(sideGroup).getByRole("button", { name: "Sell" }),
    ).toHaveAttribute("aria-pressed", "false");

    const kindGroup = within(dialog).getByRole("group", {
      name: "Amount kind",
    });
    for (const button of within(kindGroup).getAllByRole("button")) {
      expect(button).toHaveAttribute("aria-pressed", "false");
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
      within(dialog).getByLabelText("Limit price (required to settle)"),
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
  it.each([
    ["immediate", "Submit and settle"],
    ["drop_copy", "Drop copy"],
    ["hold", "Submit and wait"],
    ["venue", "Send to venue"],
  ])(
    "restores %s and focuses Account on the first open",
    async (value, label) => {
      const user = userEvent.setup();
      setStoredPreference("pit-officer-orders-submit-tab", value);
      renderOrders("/orders");
      const dialog = await openDialog(user, false);

      expect(within(dialog).getByRole("tab", { name: label })).toHaveAttribute(
        "aria-selected",
        "true",
      );
      expect(within(dialog).getByLabelText("Account")).toHaveFocus();
    },
  );

  it("treats an unknown stored submit tab as no selection", async () => {
    const user = userEvent.setup();
    setStoredPreference("pit-officer-orders-submit-tab", "unknown");
    renderOrders("/orders");
    const dialog = await openDialog(user, false);

    expect(
      within(dialog).queryByRole("tab", { selected: true }),
    ).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("tabpanel")).not.toBeInTheDocument();
    expect(
      within(dialog).getByText("Select a submit mode."),
    ).toBeInTheDocument();
  });

  it("persists an Officer tab across close, reopen, and a successful send", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    let dialog = await openDialog(user, false);
    await user.click(
      within(dialog).getByRole("tab", { name: "Submit and wait" }),
    );
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    dialog = await openDialog(user, false);
    expect(
      within(dialog).getByRole("tab", { name: "Submit and wait" }),
    ).toHaveAttribute("aria-selected", "true");
    expect(within(dialog).getByLabelText("Account")).toHaveFocus();
    await fillOrderFields(user, dialog);
    await user.click(within(dialog).getByRole("button", { name: "Add Order" }));
    await waitFor(() => expect(createOrderMock).toHaveBeenCalledTimes(1));
    const detail = await screen.findByRole("dialog", {
      name: `Order ${sampleOrder.id}`,
    });
    await within(detail).findByText("Account");
    await user.click(
      within(detail).getAllByRole("button", { name: "Close" })[0],
    );
    dialog = await openDialog(user, false);
    expect(
      within(dialog).getByRole("tab", { name: "Submit and wait" }),
    ).toHaveAttribute("aria-selected", "true");
  });

  it.each([
    [
      "Submit and settle",
      "Run pre-trade, reserve, and settle at the order price now.",
      "Limit price (required to settle)",
    ],
    [
      "Submit and wait",
      "Run pre-trade, record the lock, and wait for execution reports.",
      "Limit price (optional)",
    ],
  ])(
    "shows the own fields and help for %s",
    async (label, help, priceLabel) => {
      const user = userEvent.setup();
      renderOrders("/orders");
      const dialog = await openDialog(user, false);
      await user.click(within(dialog).getByRole("tab", { name: label }));
      const content = within(dialog).getByRole("tabpanel", { name: label });
      expect(within(content).getByText(help)).toBeInTheDocument();
      expect(
        within(content).getByRole("link", { name: "OpenAPI" }),
      ).toHaveAttribute("href", "/docs");
      expect(
        within(content).queryByRole("group", { name: "Mode" }),
      ).not.toBeInTheDocument();
      expect(within(content).getByLabelText(priceLabel)).toBeInTheDocument();
      await fillOrderFields(user, dialog);
      expect(
        await within(content).findByText("Preview: would pass"),
      ).toBeInTheDocument();
    },
  );

  it("shows the own drop-copy fields without a live check preview", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await fillOrderFields(user, dialog);
    await within(dialog).findByText("Preview: would pass");
    await user.click(within(dialog).getByRole("tab", { name: "Drop copy" }));
    checkOrderMock.mockClear();
    const content = within(dialog).getByRole("tabpanel", { name: "Drop copy" });
    expect(
      within(content).getByText(
        "Run every policy but enforce no rejects or account and group blocks. A positive limit price is required.",
      ),
    ).toBeInTheDocument();
    const price = within(content).getByLabelText(
      "Limit price (required for drop-copy)",
    );
    expect(price).toHaveAttribute("placeholder", "required");
    expect(
      within(content).getByText(
        "Market orders cannot be submitted as drop-copy.",
      ),
    ).toBeInTheDocument();
    expect(within(content).getByLabelText("Account")).toHaveValue("desk-alpha");
    await user.type(price, "100");
    await act(async () => {
      await new Promise((resolve) =>
        window.setTimeout(resolve, DEFAULT_SEARCH_DEBOUNCE_MS + 50),
      );
    });
    expect(checkOrderMock).not.toHaveBeenCalled();
    expect(
      within(content).queryByText("Preview: would pass"),
    ).not.toBeInTheDocument();
  });

  it("moves keyboard focus without selecting a tab until Enter or Space", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user, false);
    const first = within(dialog).getByRole("tab", {
      name: "Submit and settle",
    });
    expect(first).toHaveFocus();
    expect(first).toHaveAttribute("aria-selected", "false");
    await user.keyboard("{ArrowRight}");
    const dropCopy = within(dialog).getByRole("tab", { name: "Drop copy" });
    await waitFor(() => expect(dropCopy).toHaveFocus());
    expect(dropCopy).toHaveAttribute("aria-selected", "false");
    expect(within(dialog).queryByLabelText("Account")).not.toBeInTheDocument();
    await user.keyboard("{Enter}");
    expect(dropCopy).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{ArrowRight}");
    const hold = within(dialog).getByRole("tab", { name: "Submit and wait" });
    await waitFor(() => expect(hold).toHaveFocus());
    expect(hold).toHaveAttribute("aria-selected", "false");
    expect(dropCopy).toHaveAttribute("aria-selected", "true");
    await user.keyboard(" ");
    expect(hold).toHaveAttribute("aria-selected", "true");
  });

  it("does not pre-select a submit tab or mount fields", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user, false);
    const tabs = within(dialog).getAllByRole("tab");
    expect(tabs).toHaveLength(4);
    for (const tab of tabs) {
      expect(tab).toHaveAttribute("aria-selected", "false");
    }
    expect(within(dialog).queryByRole("tabpanel")).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText("Account")).not.toBeInTheDocument();
    expect(
      within(dialog).getByText("Select a submit mode."),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "Add Order" }),
    ).toBeDisabled();
  });

  it("keeps a fully seeded clone disabled and skips checks until a submit tab is chosen", async () => {
    const user = userEvent.setup();
    const detail = await openSampleOrderDetail(user, {
      order: { ...sampleOrder, price: "123" },
      events: [],
      trades: [],
    });
    await user.click(
      await within(detail).findByRole("button", {
        name: `Clone order ${sampleOrder.id}`,
      }),
    );
    const dialog = await screen.findByRole("dialog", { name: "Add order" });

    for (const tab of within(dialog).getAllByRole("tab")) {
      expect(tab).toHaveAttribute("aria-selected", "false");
    }
    expect(within(dialog).queryByRole("tabpanel")).not.toBeInTheDocument();
    expect(
      within(dialog).getByText("Select a submit mode."),
    ).toBeInTheDocument();
    const submitButton = within(dialog).getByRole("button", {
      name: "Add Order",
    });
    expect(submitButton).toBeDisabled();
    await act(async () => {
      await new Promise((resolve) =>
        window.setTimeout(resolve, DEFAULT_SEARCH_DEBOUNCE_MS + 50),
      );
    });
    expect(checkOrderMock).not.toHaveBeenCalled();

    await user.click(
      within(dialog).getByRole("tab", { name: "Submit and settle" }),
    );
    expect(submitButton).toBeEnabled();
    expect(
      within(dialog).queryByText("Select a submit mode."),
    ).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText("Account")).toHaveValue("desk-alpha");
    expect(within(dialog).getByLabelText("Base asset")).toHaveValue("AAPL");
    expect(within(dialog).getByLabelText("Quote asset")).toHaveValue("USD");
    expect(within(dialog).getByLabelText("Amount")).toHaveValue("100");
    expect(
      within(dialog).getByLabelText("Limit price (required to settle)"),
    ).toHaveValue("123");
    expect(within(dialog).getByRole("button", { name: "Buy" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(
      within(dialog).getByRole("button", { name: "Quantity (base)" }),
    ).toHaveAttribute("aria-pressed", "true");
    await within(dialog).findByText("Preview: would pass");
    expect(checkOrderMock).toHaveBeenCalledTimes(1);
  });

  it("keeps submit disabled until side and amount kind are chosen on the active tab", async () => {
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
    await user.type(
      within(dialog).getByLabelText("Limit price (required to settle)"),
      "100",
    );

    const submitButton = within(dialog).getByRole("button", {
      name: /add order/i,
    });

    // Text fields filled on the active tab, but side and amount kind are missing.
    expect(submitButton).toBeDisabled();

    const sideGroup = within(dialog).getByRole("group", { name: "Side" });
    await user.click(within(sideGroup).getByRole("button", { name: /buy/i }));
    // Side chosen, amount kind still missing.
    expect(submitButton).toBeDisabled();

    const kindGroup = within(dialog).getByRole("group", {
      name: "Amount kind",
    });
    await user.click(
      within(kindGroup).getByRole("button", { name: /quantity/i }),
    );
    // Amount kind chosen on the active tab.
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

  it("allows volume for modes that settle immediately", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);

    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "1000");
    await user.click(within(dialog).getByRole("button", { name: /buy/i }));
    await user.click(within(dialog).getByRole("button", { name: /volume/i }));
    await user.click(
      within(dialog).getByRole("tab", { name: /submit and settle/i }),
    );
    await user.type(
      within(dialog).getByLabelText("Limit price (required to settle)"),
      "10",
    );

    expect(
      within(dialog).getByRole("button", { name: /add order/i }),
    ).toBeEnabled();
    expect(createOrderMock).not.toHaveBeenCalled();

    await user.click(within(dialog).getByRole("tab", { name: /^drop copy/i }));
    await user.clear(
      within(dialog).getByLabelText("Limit price (required for drop-copy)"),
    );
    await user.type(
      within(dialog).getByLabelText("Limit price (required for drop-copy)"),
      "10",
    );
    expect(
      within(dialog).getByRole("button", { name: /submit drop-copy/i }),
    ).toBeEnabled();
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
    await user.click(within(dialog).getByRole("button", { name: /buy/i }));
    await user.click(within(dialog).getByRole("button", { name: /quantity/i }));
    await user.click(within(dialog).getByRole("tab", { name: /^drop copy/i }));

    const modeGroup = within(dialog).getByRole("tablist", {
      name: "Submit mode",
    });
    expect(within(modeGroup).getAllByRole("tab")).toHaveLength(4);
    expect(
      within(modeGroup).getByRole("tab", { name: /^drop copy/i }),
    ).toHaveAttribute("aria-selected", "true");
    expect(
      within(modeGroup).getByRole("tab", { name: /submit and wait/i }),
    ).toHaveAttribute("aria-selected", "false");
    expect(within(dialog).getByLabelText("ID (optional)")).toHaveAttribute(
      "placeholder",
      "server generated when blank",
    );
    expect(
      within(dialog).getByText(
        /use your own unique ID when you need a stable order handle/i,
      ),
    ).toBeInTheDocument();
    const submit = within(dialog).getByRole("button", {
      name: /submit drop-copy/i,
    });
    await expectPriceRequired(
      user,
      dialog,
      submit,
      "Limit price (required for drop-copy)",
      /market orders cannot be submitted as drop-copy/i,
    );

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

  it("requires a positive limit price to submit and settle", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);

    await fillOrderFields(user, dialog);
    await user.click(
      within(dialog).getByRole("tab", { name: /submit and settle/i }),
    );

    const submit = within(dialog).getByRole("button", { name: /add order/i });
    await expectPriceRequired(
      user,
      dialog,
      submit,
      "Limit price (required to settle)",
      "Market orders cannot be settled on submit. Use Submit and wait instead.",
    );
    expect(createOrderMock).not.toHaveBeenCalled();
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
    await user.click(within(dialog).getByRole("button", { name: /buy/i }));
    await user.click(within(dialog).getByRole("button", { name: /quantity/i }));
    await user.click(
      within(dialog).getByRole("tab", { name: /submit and settle/i }),
    );
    await user.type(
      within(dialog).getByLabelText("Limit price (required to settle)"),
      "100",
    );
    await user.click(
      within(dialog).getByRole("button", { name: /add order/i }),
    );

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
    await user.click(within(dialog).getByRole("button", { name: /buy/i }));
    await user.click(within(dialog).getByRole("button", { name: /quantity/i }));
    await user.click(within(dialog).getByRole("tab", { name: /^drop copy/i }));
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
    await user.click(within(dialog).getByRole("button", { name: /buy/i }));
    await user.click(within(dialog).getByRole("button", { name: /quantity/i }));
    await user.click(
      within(dialog).getByRole("tab", { name: /submit and settle/i }),
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
    await user.click(within(dialog).getByRole("button", { name: /buy/i }));
    await user.click(within(dialog).getByRole("button", { name: /quantity/i }));
    // The price is optional on hold; immediate requires it (tested apart).
    await user.click(
      within(dialog).getByRole("tab", { name: /submit and wait/i }),
    );

    const submit = within(dialog).getByRole("button", { name: /add order/i });
    expect(submit).toBeDisabled();

    await user.clear(within(dialog).getByLabelText("Amount"));
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    expect(submit).toBeEnabled();

    await user.type(
      within(dialog).getByLabelText("Limit price (optional)"),
      "0",
    );
    expect(submit).toBeDisabled();

    await user.clear(within(dialog).getByLabelText("Limit price (optional)"));
    await user.type(
      within(dialog).getByLabelText("Limit price (optional)"),
      "10",
    );
    expect(submit).toBeEnabled();
    expect(createOrderMock).not.toHaveBeenCalled();
  });

  it("shows create-order enrichment warnings in the detail dialog", async () => {
    const user = userEvent.setup();
    const warning =
      "Order was created, but detail enrichment failed: store down";
    createOrderMock.mockResolvedValueOnce({ order: sampleOrder, warning });
    renderOrders("/orders");
    const dialog = await openDialog(user);

    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    await user.click(within(dialog).getByRole("button", { name: /buy/i }));
    await user.click(within(dialog).getByRole("button", { name: /quantity/i }));
    await user.click(
      within(dialog).getByRole("tab", { name: /submit and settle/i }),
    );
    await user.type(
      within(dialog).getByLabelText("Limit price (required to settle)"),
      "100",
    );
    await user.click(
      within(dialog).getByRole("button", { name: /add order/i }),
    );

    expect(await screen.findByText(warning)).toBeInTheDocument();
  });

  it("clears busy after aborting an in-flight submit", async () => {
    const user = userEvent.setup();
    createOrderMock.mockImplementation(
      (_body, _missingAccount, signal) =>
        new Promise((_, reject) => {
          signal?.addEventListener(
            "abort",
            () => {
              reject(new DOMException("aborted", "AbortError"));
            },
            { once: true },
          );
        }),
    );
    renderOrders("/orders");
    let dialog = await openDialog(user);

    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    await user.click(within(dialog).getByRole("button", { name: /buy/i }));
    await user.click(within(dialog).getByRole("button", { name: /quantity/i }));
    await user.click(
      within(dialog).getByRole("tab", { name: /submit and settle/i }),
    );
    await user.type(
      within(dialog).getByLabelText("Limit price (required to settle)"),
      "100",
    );
    await user.click(
      within(dialog).getByRole("button", { name: /add order/i }),
    );
    await waitFor(() => expect(createOrderMock).toHaveBeenCalledTimes(1));

    await user.click(within(dialog).getByRole("button", { name: "Close" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );

    dialog = await openDialog(user);
    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    await user.click(within(dialog).getByRole("button", { name: /buy/i }));
    await user.click(within(dialog).getByRole("button", { name: /quantity/i }));
    await user.click(
      within(dialog).getByRole("tab", { name: /submit and settle/i }),
    );
    await user.type(
      within(dialog).getByLabelText("Limit price (required to settle)"),
      "100",
    );

    expect(
      within(dialog).getByRole("button", { name: /add order/i }),
    ).toBeEnabled();
  });
});

describe("Orders workflow confirm/cancel shortcuts", () => {
  const acceptedEvent: OrderEvent = {
    id: "evt-pre-trade-accepted",
    order: sampleOrder.id,
    at: "2026-06-24T11:08:00Z",
    type: "pre_trade_accepted",
    source: "panel",
    principal: "",
    signed: true,
    alg: "ed25519",
  };

  beforeEach(() => {
    fetchOrderDetailMock.mockResolvedValue({
      order: sampleOrder,
      events: [acceptedEvent],
      trades: [],
    });
  });

  // Fill the add-order dialog with a valid workflow order and submit it. The
  // resulting detail carries the recorded pre-trade accept attestation.
  async function submitWorkflowOrder(user: ReturnType<typeof userEvent.setup>) {
    const dialog = await openDialog(user);
    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await user.type(within(dialog).getByLabelText("Base asset"), "AAPL");
    await user.type(within(dialog).getByLabelText("Quote asset"), "USD");
    await user.type(within(dialog).getByLabelText("Amount"), "100");
    await user.click(within(dialog).getByRole("button", { name: /buy/i }));
    await user.click(within(dialog).getByRole("button", { name: /quantity/i }));
    await user.click(
      within(dialog).getByRole("tab", { name: /submit and wait/i }),
    );
    await user.click(
      within(dialog).getByRole("button", { name: /add order/i }),
    );
  }

  it("confirms and cancels from the recorded approval", async () => {
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
      expect(confirmOrderMock).toHaveBeenCalledWith("ord-alpha-1"),
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

  it("cancels with raw signed leaves without stepper nudging", async () => {
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
    await user.type(leaves, "+1.500");
    await user.keyboard("{ArrowUp}{ArrowDown}");
    expect(leaves).toHaveValue("+1.500");
    await user.click(
      within(detail).getByRole("button", { name: "Cancel order" }),
    );

    await waitFor(() =>
      expect(cancelOrderMock).toHaveBeenCalledWith("ord-alpha-1", {
        leavesQuantity: "+1.500",
      }),
    );
  });

  it("cancels with no leaves instead of inventing a replacement", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await submitWorkflowOrder(user);

    const detail = await screen.findByRole("dialog", {
      name: "Order ord-alpha-1",
    });
    await user.click(
      within(detail).getByRole("button", { name: "Cancel order" }),
    );
    const cancel = within(detail).getByRole("button", {
      name: "Cancel order",
    });
    expect(cancel).toBeEnabled();
    await user.click(cancel);

    await waitFor(() =>
      expect(cancelOrderMock).toHaveBeenCalledWith("ord-alpha-1", {}),
    );
  });

  it("does not offer shortcuts for a rejected workflow submit", async () => {
    const user = userEvent.setup();
    const rejectedOrder: Order = { ...sampleOrder, status: "rejected" };
    createOrderMock.mockResolvedValueOnce({
      order: rejectedOrder,
      approval: {
        signedApproval: "rejected-hold-envelope",
        keyId: "key-1",
        id: rejectedOrder.id,
        verdict: "reject",
        reasons: [
          {
            code: "policy_reject",
            scope: "account",
            policy: "daily_loss",
            reason: "Rejected by policy",
            details: "",
          },
        ],
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

  it("hides shortcuts for a terminal order even with a recorded approval", async () => {
    const user = userEvent.setup();
    const terminalOrder: Order = { ...sampleOrder, status: "filled" };
    createOrderMock.mockResolvedValueOnce({
      order: terminalOrder,
      approval: {
        signedApproval: "hold-envelope",
        keyId: "key-1",
        id: terminalOrder.id,
        verdict: "accept",
        reasons: [],
      },
    });
    fetchOrderDetailMock.mockResolvedValue({
      order: terminalOrder,
      events: [{ ...acceptedEvent, order: terminalOrder.id }],
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
    expect(confirmOrderMock).not.toHaveBeenCalled();
  });

  it("offers shortcuts for an attested hold order not submitted in this session", async () => {
    const user = userEvent.setup();
    useOrdersMock.mockReturnValue(readyPage<Order>([sampleOrder]));
    renderOrders("/orders");

    await user.click(screen.getByText("ord-alpha-1"));
    const detail = await screen.findByRole("dialog", {
      name: "Order ord-alpha-1",
    });

    expect(
      await within(detail).findByRole("button", { name: "Confirm" }),
    ).toBeInTheDocument();
    expect(
      within(detail).getByRole("button", { name: "Cancel order" }),
    ).toBeInTheDocument();
  });

  it("hides shortcuts without a recorded pre-trade accept attestation", async () => {
    const user = userEvent.setup();
    useOrdersMock.mockReturnValue(readyPage<Order>([sampleOrder]));
    fetchOrderDetailMock.mockResolvedValue({
      order: sampleOrder,
      events: [
        {
          id: "evt-pre-trade-accepted-unattested",
          order: sampleOrder.id,
          at: "2026-06-24T11:08:00Z",
          type: "pre_trade_accepted",
          source: "panel",
          principal: "",
          signed: false,
        },
      ],
      trades: [],
    });
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
    await user.click(within(dialog).getByRole("button", { name: /buy/i }));
    await user.click(within(dialog).getByRole("button", { name: /quantity/i }));
    await user.click(within(dialog).getByRole("tab", { name: /^drop copy/i }));
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

    await user.click(
      scope.getByRole("button", { name: /filter by aapl \/ usd/i }),
    );
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

    await user.click(screen.getByRole("tab", { name: /^trades$/i }));
    const link = screen.getByRole("link", { name: "ord-alpha-1" });
    expect(link).toHaveAttribute(
      "href",
      expect.stringContaining("order=ord-alpha-1"),
    );
    expect(link).toHaveAttribute(
      "title",
      expect.stringContaining("Ctrl-click"),
    );
  });
});

describe("Order detail account block placement", () => {
  it("shows the account block reason beside the fill event that caused it", async () => {
    const user = userEvent.setup();
    const order = {
      ...sampleOrder,
      id: "ord-alpha-3",
      status: "filled",
      price: "99",
    };
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
        rejectReason: "failed to access required field 'remaining quantity'",
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
    expect(
      within(fillEvent as HTMLElement).getByText("fill"),
    ).toBeInTheDocument();

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
        signedEnvelope: "env-evt-verbatim",
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
          signedAttestation: "env-evt-verbatim",
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
    await waitFor(() => expect(fetchEventReproductionMock).toHaveBeenCalled());
    const [orderId, eventId] = fetchEventReproductionMock.mock.calls[0];
    expect(orderId).toBe("ord-signed-evt");
    expect(eventId).toBe("evt-signed");

    // The verbatim envelope renders inside the opened panel.
    await waitFor(() =>
      expect(
        screen
          .getAllByRole("textbox")
          .some(
            (el) => (el as HTMLTextAreaElement).value === "env-evt-verbatim",
          ),
      ).toBe(true),
    );
  });
});

describe("Orders business CSV export", () => {
  it("exports orders with account and source filters but no display size", async () => {
    const user = userEvent.setup();
    renderOrders("/orders?account=desk-alpha&source=panel");

    await user.click(
      screen.getByRole("button", { name: /export orders csv/i }),
    );
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

    await user.click(
      screen.getByRole("button", { name: /export orders csv/i }),
    );
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

    await user.click(screen.getByRole("tab", { name: /^trades$/i }));
    await user.click(
      screen.getByRole("button", { name: /export trades csv/i }),
    );
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
    const createElementSpy = vi
      .spyOn(document, "createElement")
      .mockImplementation(((
        tagName: string,
        options?: ElementCreationOptions,
      ) => {
        const element = realCreateElement(tagName, options);
        if (tagName === "a") {
          anchors.push(element as HTMLAnchorElement);
        }
        return element;
      }) as typeof document.createElement);
    exportBusinessCsvMock.mockResolvedValueOnce({
      blob: new Blob(["orders"]),
      filename: "orders-export.csv",
    });
    renderOrders("/orders");

    await user.click(
      screen.getByRole("button", { name: /export orders csv/i }),
    );
    await user.click(screen.getByRole("button", { name: /^export$/i }));

    await waitFor(() => expect(exportBusinessCsvMock).toHaveBeenCalledTimes(1));
    expect(anchors.at(-1)?.download).toBe("orders-export.csv");
    createElementSpy.mockRestore();
  });
});

describe("Execution report leaves contract", () => {
  function alphaFourOrder() {
    return {
      ...sampleOrder,
      id: "ord-alpha-4",
      status: "filled" as const,
      price: "12",
      leavesQuantity: "2",
      displayPrice: "12",
    };
  }

  async function openReport(user: ReturnType<typeof userEvent.setup>) {
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
  }

  async function selectStatus(
    user: ReturnType<typeof userEvent.setup>,
    label: string,
  ) {
    const focusSpy = vi
      .spyOn(HTMLElement.prototype, "focus")
      .mockImplementation(() => {});
    await user.click(screen.getByLabelText("Target status"));
    await user.click(screen.getByRole("option", { name: label }));
    focusSpy.mockRestore();
  }

  it.each([
    ["Filled", "filled"],
    ["Partially filled", "partially_filled"],
  ] as const)(
    "forwards valid decimal drafts verbatim for %s",
    async (label, status) => {
      const user = userEvent.setup();
      await openReport(user);
      await selectStatus(user, label);

      const leaves = screen.getByLabelText("Leaves quantity");
      expect(leaves).toHaveValue("");
      expect(screen.getByRole("button", { name: "Submit" })).toBeDisabled();
      expect(
        screen.getByRole("button", { name: "Calculate leaves quantity" }),
      ).toBeInTheDocument();

      const quantity = screen.getByLabelText("Fill quantity");
      await user.clear(quantity);
      await user.type(quantity, "1.500");
      expect(quantity).toHaveValue("1.500");

      const price = screen.getByLabelText("Fill price");
      await user.clear(price);
      await user.type(price, "1.500");
      expect(price).toHaveValue("1.500");

      const lockPrice = screen.getByLabelText("Lock price (optional)");
      await user.clear(lockPrice);
      await user.type(lockPrice, "1.500");
      expect(lockPrice).toHaveValue("1.500");

      await user.type(leaves, "+1.500");
      expect(leaves).toHaveValue("+1.500");
      await user.click(screen.getByRole("button", { name: "Submit" }));

      await waitFor(() =>
        expect(submitExecutionReportMock).toHaveBeenLastCalledWith(
          "ord-alpha-4",
          {
            status,
            quantity: "1.500",
            price: "1.500",
            leavesQuantity: "+1.500",
            lockPrice: "1.500",
          },
        ),
      );
    },
  );

  it("rejects surrounding spaces without rewriting execution-report fields", async () => {
    const user = userEvent.setup();
    await openReport(user);

    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );
    const submit = screen.getByRole("button", { name: "Submit" });
    const fields = [
      [screen.getByLabelText("Fill quantity"), " 2", "2"],
      [screen.getByLabelText("Fill price"), " 12", "12"],
      [screen.getByLabelText("Lock price (optional)"), " 12", "12"],
      [screen.getByLabelText("Leaves quantity"), " 0", "0"],
    ] as const;

    for (const [field, invalidValue, validValue] of fields) {
      await user.clear(field);
      await user.type(field, invalidValue);

      expect(field).toHaveValue(invalidValue);
      expect(submit).toBeDisabled();
      expect(submitExecutionReportMock).not.toHaveBeenCalled();

      await user.clear(field);
      await user.type(field, validValue);
      expect(field).toHaveValue(validValue);
      expect(submit).toBeEnabled();
    }
  });

  it("refuses a commission-only fill draft the API would reject", async () => {
    const user = userEvent.setup();
    await openReport(user);

    await user.clear(screen.getByLabelText("Fill quantity"));
    await user.clear(screen.getByLabelText("Fill price"));
    await user.clear(screen.getByLabelText(/lock price/i));
    await user.type(screen.getByLabelText("Leaves quantity"), "1");
    await user.click(screen.getByRole("button", { name: "Fee" }));
    await user.type(screen.getByLabelText("Commission amount"), "0.50");
    await user.type(screen.getByLabelText("Commission currency"), "USD");

    expect(screen.getByRole("button", { name: "Submit" })).toBeDisabled();
    expect(submitExecutionReportMock).not.toHaveBeenCalled();
  });

  it("refuses leaves the report contract would not record", async () => {
    const user = userEvent.setup();
    await openReport(user);
    await selectStatus(user, "Accepted");

    const leaves = screen.getByLabelText("Leaves quantity");
    await user.type(leaves, "-1");
    expect(screen.getByRole("button", { name: "Submit" })).toBeDisabled();

    await user.clear(leaves);
    await user.type(leaves, "1");
    expect(screen.getByRole("button", { name: "Submit" })).toBeEnabled();
  });

  it("explains malformed leaves while Submit is disabled", async () => {
    const user = userEvent.setup();
    await openReport(user);
    await selectStatus(user, "Accepted");

    const leaves = screen.getByLabelText("Leaves quantity");
    const submit = screen.getByRole("button", { name: "Submit" });
    expect(submit).toBeEnabled();

    await user.type(leaves, " 1 ");

    expect(
      screen.getByText(
        "Leaves quantity must be a plain non-negative decimal without surrounding spaces.",
      ),
    ).toBeInTheDocument();
    expect(submit).toBeDisabled();
  });

  it("keeps terminal no-fill leaves optional and sends no synthetic value", async () => {
    const user = userEvent.setup();
    await openReport(user);
    await selectStatus(user, "Cancelled");

    expect(screen.queryByLabelText("Fill quantity")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Fill price")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("");
    expect(
      screen.queryByText(
        "Leaves quantity must be a plain non-negative decimal without surrounding spaces.",
      ),
    ).not.toBeInTheDocument();
    const submit = screen.getByRole("button", { name: "Submit" });
    expect(submit).toBeEnabled();
    await user.click(submit);

    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith("ord-alpha-4", {
        status: "cancelled",
      }),
    );
  });

  it("records optional workflow leaves exactly as the operator entered them", async () => {
    const user = userEvent.setup();
    await openReport(user);
    await selectStatus(user, "Accepted");

    expect(screen.queryByLabelText("Fill quantity")).not.toBeInTheDocument();
    const leaves = screen.getByLabelText("Leaves quantity");
    await user.type(leaves, "1.23000");
    await user.click(screen.getByRole("button", { name: "Submit" }));

    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith("ord-alpha-4", {
        status: "accepted",
        leavesQuantity: "1.23000",
      }),
    );
  });
  it("clones execution-report event commission and resulting status", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();
    const events: OrderEvent[] = [
      {
        id: "evt-alpha-fill",
        order: order.id,
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

    await user.click(screen.getByText(order.id));
    await screen.findByRole("dialog", { name: `Order ${order.id}` });
    await user.click(
      screen.getByLabelText(`Clone execution report from order ${order.id}`),
    );

    expect(await screen.findByLabelText("Fill quantity")).toHaveValue("1");
    expect(screen.getByLabelText("Fill price")).toHaveValue("12");
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("1");
    expect(screen.getByLabelText("Commission amount")).toHaveValue("0.50");
    expect(screen.getByRole("button", { name: "Rebate" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByLabelText("Commission currency")).toHaveValue("USD");

    await user.click(screen.getByRole("button", { name: "Submit" }));
    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(order.id, {
        status: "partially_filled",
        quantity: "1",
        price: "12",
        leavesQuantity: "1",
        lockPrice: "12",
        commission: { amount: "-0.50", currency: "USD" },
      }),
    );
  });

  it("clones a filled execution-report event without replacing fill quantity by leaves", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();
    const events: OrderEvent[] = [
      {
        id: "evt-alpha-filled",
        order: order.id,
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

    await user.click(screen.getByText(order.id));
    await screen.findByRole("dialog", { name: `Order ${order.id}` });
    await user.click(
      screen.getByLabelText(`Clone execution report from order ${order.id}`),
    );

    expect(await screen.findByLabelText("Fill quantity")).toHaveValue("1");
    expect(screen.getByLabelText("Fill price")).toHaveValue("12");
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("0");

    await user.click(screen.getByRole("button", { name: "Submit" }));
    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(order.id, {
        status: "filled",
        quantity: "1",
        price: "12",
        leavesQuantity: "0",
        lockPrice: "12",
      }),
    );
  });

  it("clones a terminal report without fill fields it cannot carry", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();
    const events: OrderEvent[] = [
      {
        id: "evt-alpha-cancel-fill",
        order: order.id,
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

    await user.click(screen.getByText(order.id));
    await screen.findByRole("dialog", { name: `Order ${order.id}` });
    await user.click(
      screen.getByLabelText(`Clone execution report from order ${order.id}`),
    );

    expect(await screen.findByLabelText("Leaves quantity")).toHaveValue("0.5");
    expect(screen.queryByLabelText("Fill quantity")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Fill price")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Commission amount")).toHaveValue("0.75");
    expect(screen.getByLabelText("Commission currency")).toHaveValue("USD");

    await user.click(screen.getByRole("button", { name: "Submit" }));
    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(order.id, {
        status: "cancelled",
        leavesQuantity: "0.5",
        commission: { amount: "-0.75", currency: "USD" },
      }),
    );
  });

  it("calculates leaves quantity for a partial fill", async () => {
    const user = userEvent.setup();
    await openReport(user);
    await selectStatus(user, "Partially filled");

    await user.clear(screen.getByLabelText("Fill quantity"));
    await user.type(screen.getByLabelText("Fill quantity"), "0.5");
    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );

    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("1.5");
    await user.click(screen.getByRole("button", { name: "Submit" }));

    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith("ord-alpha-4", {
        status: "partially_filled",
        quantity: "0.5",
        price: "12",
        leavesQuantity: "1.5",
        lockPrice: "12",
      }),
    );
  });

  it("leaves fill quantity empty when the remaining quantity is unknown", async () => {
    const user = userEvent.setup();
    const order = {
      ...alphaFourOrder(),
      amountKind: "volume" as const,
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

    await user.click(screen.getByText(order.id));
    await screen.findByRole("dialog", { name: `Order ${order.id}` });
    await user.click(
      screen.getByRole("button", { name: "Submit execution report" }),
    );

    expect(screen.getByLabelText("Fill quantity")).toHaveValue("");
    expect(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    ).toBeDisabled();
  });

  it("seeds fill quantity from remaining leaves and calculates zero", async () => {
    const user = userEvent.setup();
    await openReport(user);

    const submit = screen.getByRole("button", { name: "Submit" });
    expect(screen.getByLabelText("Fill quantity")).toHaveValue("2");
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("");
    expect(submit).toBeDisabled();
    expect(
      screen.queryByText(
        "Leaves quantity must be a plain non-negative decimal without surrounding spaces.",
      ),
    ).not.toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("0");
    expect(submit).toBeEnabled();
    await user.click(submit);

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

  it("submits structured commission when all optional fields are set", async () => {
    const user = userEvent.setup();
    await openReport(user);

    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );
    await user.click(screen.getByRole("button", { name: "Fee" }));
    await user.type(screen.getByLabelText("Commission amount"), "-0.50");
    expect(screen.getByLabelText("Commission amount")).toHaveValue("0.50");
    await user.type(screen.getByLabelText("Commission currency"), " USD ");
    await user.click(screen.getByRole("button", { name: "Submit" }));

    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith("ord-alpha-4", {
        status: "filled",
        quantity: "2",
        price: "12",
        leavesQuantity: "0",
        lockPrice: "12",
        commission: { amount: "0.50", currency: "USD" },
      }),
    );
  });

  it("disables submit while a structured commission is incomplete", async () => {
    const user = userEvent.setup();
    await openReport(user);

    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );
    await user.click(screen.getByRole("button", { name: "Fee" }));
    await user.type(screen.getByLabelText("Commission amount"), "0.50");

    expect(screen.getByRole("button", { name: "Submit" })).toBeDisabled();
    expect(submitExecutionReportMock).not.toHaveBeenCalled();
  });

  it("disables submit while a visible numeric draft is invalid", async () => {
    const user = userEvent.setup();
    await openReport(user);

    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );
    const submit = screen.getByRole("button", { name: "Submit" });
    expect(submit).toBeEnabled();

    const quantity = screen.getByLabelText("Fill quantity");
    await user.type(quantity, "x");

    expect(quantity).toHaveValue("2x");
    expect(submit).toBeDisabled();
    expect(submitExecutionReportMock).not.toHaveBeenCalled();
  });

  it("requires positive fill economics while allowing zero leaves and commission", async () => {
    const user = userEvent.setup();
    await openReport(user);

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
  });

  it("reflects the server's post-report state, not the requested one", async () => {
    const user = userEvent.setup();
    const order = { ...alphaFourOrder(), status: "partially_filled" as const };
    const serverResolved = {
      ...order,
      status: "committed" as const,
      leavesQuantity: "1",
    };
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

    const row = screen.getByText(order.id).closest("tr");
    expect(row).not.toBeNull();
    expect(
      within(row as HTMLElement).getByText("partially_filled"),
    ).toBeInTheDocument();

    await user.click(screen.getByText(order.id));
    await screen.findByRole("dialog", { name: `Order ${order.id}` });
    await user.click(
      screen.getByRole("button", { name: "Submit execution report" }),
    );
    await user.click(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    );
    await user.click(screen.getByRole("button", { name: "Submit" }));

    await waitFor(() => expect(swapToResolved).toHaveBeenCalled());
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
    expect(
      within(
        screen.getByRole("dialog", { name: `Order ${order.id}` }),
      ).getByText("committed"),
    ).toBeInTheDocument();
  });

  it("preserves commission and leaves draft after switching to a workflow status", async () => {
    const user = userEvent.setup();
    await openReport(user);

    await user.click(screen.getByRole("button", { name: "Fee" }));
    await user.type(screen.getByLabelText("Commission amount"), "0.50");
    await user.type(screen.getByLabelText("Commission currency"), "USD");
    await selectStatus(user, "Accepted");

    expect(screen.queryByLabelText("Fill quantity")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Fill price")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Commission amount")).toHaveValue("0.50");
    expect(screen.getByLabelText("Commission currency")).toHaveValue("USD");
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("");
    expect(
      screen.getByRole("button", { name: "Calculate leaves quantity" }),
    ).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "Submit" }));
    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith("ord-alpha-4", {
        status: "accepted",
        commission: { amount: "0.50", currency: "USD" },
      }),
    );
  });

  it("drops cloned fill fields when switching to a workflow status", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();
    const trades: Trade[] = [
      {
        id: "trd-alpha-9",
        order: order.id,
        account: order.account,
        at: "2026-06-24T11:08:00Z",
        source: "panel",
        baseAsset: order.baseAsset,
        quoteAsset: order.quoteAsset,
        side: order.side,
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

    await user.click(screen.getByText(order.id));
    await screen.findByRole("dialog", { name: `Order ${order.id}` });
    await user.click(
      screen.getByLabelText("Clone execution report for trade trd-alpha-9"),
    );
    await selectStatus(user, "Accepted");

    expect(screen.queryByLabelText("Fill quantity")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Fill price")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("");
    await user.click(screen.getByRole("button", { name: "Submit" }));

    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(order.id, {
        status: "accepted",
      }),
    );
  });

  it("keeps a trade row's fill quantity while seeding its leaves calculation", async () => {
    const user = userEvent.setup();
    const order = alphaFourOrder();
    const trades: Trade[] = [
      {
        id: "trd-alpha-9",
        order: order.id,
        account: order.account,
        at: "2026-06-24T11:08:00Z",
        source: "panel",
        baseAsset: order.baseAsset,
        quoteAsset: order.quoteAsset,
        side: order.side,
        quantity: "1",
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

    await user.click(screen.getByText(order.id));
    await screen.findByRole("dialog", { name: `Order ${order.id}` });
    await user.click(
      screen.getByLabelText("Clone execution report for trade trd-alpha-9"),
    );

    expect(await screen.findByLabelText("Fill quantity")).toHaveValue("1");
    expect(screen.getByLabelText("Fill price")).toHaveValue("12");
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("");

    const calculate = screen.getByRole("button", {
      name: "Calculate leaves quantity",
    });
    expect(calculate).toBeEnabled();
    await user.click(calculate);
    expect(screen.getByLabelText("Leaves quantity")).toHaveValue("1");

    await user.click(screen.getByRole("button", { name: "Submit" }));
    await waitFor(() =>
      expect(submitExecutionReportMock).toHaveBeenCalledWith(order.id, {
        status: "filled",
        quantity: "1",
        price: "12",
        leavesQuantity: "1",
        lockPrice: "12",
      }),
    );
  });

  it("keeps the parent dialog's modality lock when a nested info dialog closes", async () => {
    const user = userEvent.setup();
    await openReport(user);

    expect(document.body.style.pointerEvents).toBe("none");

    await user.click(
      screen.getByRole("button", { name: "Commission details" }),
    );
    const info = await screen.findByRole("dialog", { name: "Commission" });
    await user.keyboard("{Escape}");
    await waitFor(() => expect(info).not.toBeInTheDocument());

    expect(screen.getByLabelText("Target status")).toBeInTheDocument();
    await waitFor(() => expect(document.body.style.pointerEvents).toBe("none"));
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

    expect(orderFilterWasRequested((f) => f.account === "desk-zeta")).toBe(
      false,
    );

    act(() => {
      vi.advanceTimersByTime(299);
    });
    expect(orderFilterWasRequested((f) => f.account === "desk-zeta")).toBe(
      false,
    );

    await act(async () => {
      vi.advanceTimersByTime(1);
      await Promise.resolve();
    });
    expect(
      screen.getByRole("option", { name: "desk-zeta" }),
    ).toBeInTheDocument();
    expect(orderFilterWasRequested((f) => f.account === "desk-zeta")).toBe(
      false,
    );

    act(() => {
      fireEvent.keyDown(accountInput, { key: "Enter" });
      vi.runOnlyPendingTimers();
    });
    expect(orderFilterWasRequested((f) => f.account === "desk-zeta")).toBe(
      true,
    );
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
    await user.click(screen.getByRole("tab", { name: /^trades$/i }));

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
    useOrdersMock.mockReturnValue(
      readyPageWithTotal<Order>([sampleOrder], 120),
    );
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
    useOrdersMock.mockReturnValue(
      readyPageWithTotal<Order>([sampleOrder], 120),
    );
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

    await user.type(screen.getByPlaceholderText(/^exact id$/i), "ord-alpha-1");
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
    useOrdersMock.mockReturnValue(
      readyPageWithTotal<Order>([sampleOrder], 120),
    );
    renderOrders("/orders");

    // 120 rows at a page size of 50 → three known pages.
    expect(screen.getAllByText("Page 1 of 3").length).toBeGreaterThan(0);

    const next = screen.getAllByRole("button", { name: "Next" })[0];
    expect(next).toBeEnabled();
    await user.click(next);

    await waitFor(() => expect(lastOrderFilters()?.offset).toBe(50));
  });
});

describe("Orders venue list integration", () => {
  const connectionLabel = "Alpaca · Paper · Primary Alpaca";

  for (const tab of ["orders", "trades"] as const) {
    const filters = () =>
      tab === "orders" ? lastOrderFilters() : lastTradeFilters();
    const entry = (venue = "") =>
      `/orders?tab=${tab}${venue ? `&venue=${encodeURIComponent(venue)}` : ""}`;
    const seedRows = () => {
      useOrdersMock.mockReturnValue(
        readyPage([
          { ...sampleOrder, venueConnection: "alpaca-paper" },
          { ...sampleOrder, id: "unlinked-order" },
          {
            ...sampleOrder,
            id: "unknown-order",
            venueConnection: "new-connection",
          },
        ]),
      );
      useTradesMock.mockReturnValue(
        readyPage([
          { ...sampleTrade, venueConnection: "alpaca-paper" },
          { ...sampleTrade, id: "unlinked-trade" },
          {
            ...sampleTrade,
            id: "unknown-trade",
            venueConnection: "new-connection",
          },
        ]),
      );
    };

    it(`${tab}: shows linked, unlinked and unknown connection cells after Source`, () => {
      seedRows();
      renderOrders(entry());
      const table = screen.getByRole("table");
      const headers = within(table).getAllByRole("columnheader");
      const sourceIndex = headers.findIndex((header) =>
        header.textContent?.includes("Source"),
      );
      expect(headers[sourceIndex + 1]).toHaveTextContent("Venue");
      const rows = within(table).getAllByRole("row").slice(1);
      expect(
        within(rows[0]).getAllByRole("cell")[sourceIndex + 1],
      ).toHaveTextContent(connectionLabel);
      expect(within(rows[0]).getAllByRole("cell")[sourceIndex + 1]).toHaveClass(
        "whitespace-nowrap",
      );
      expect(
        within(rows[1]).getAllByRole("cell")[sourceIndex + 1].textContent,
      ).toBe(i18n.t("common:value.none"));
      expect(
        within(rows[2]).getAllByRole("cell")[sourceIndex + 1],
      ).toHaveTextContent("new-connection");
      expect(filters()).not.toHaveProperty("venue");
    });

    for (const [option, value] of [
      ["Any venue", VENUE_FILTER_ANY],
      ["Not through a venue", VENUE_FILTER_NONE],
      [connectionLabel, venueConnectionFilterValue("alpaca-paper")],
    ]) {
      it(`${tab}: applies and clears ${option} through the advanced draft`, async () => {
        const user = userEvent.setup();
        useOrdersMock.mockReturnValue(readyPageWithTotal([sampleOrder], 100));
        useTradesMock.mockReturnValue(readyPageWithTotal([sampleTrade], 100));
        renderOrders(entry());
        await user.click(screen.getAllByRole("button", { name: "Next" })[0]);
        expect(filters()?.offset).toBeGreaterThan(0);
        await user.click(screen.getByRole("button", { name: "More filters" }));
        const dialog = screen.getByRole("dialog");
        expect(
          within(dialog).getByRole("group", { name: "Venue" }),
        ).toHaveStyle({
          width: "320px",
        });
        await user.click(within(dialog).getByRole("button", { name: option }));
        expect(filters()).not.toHaveProperty("venue");
        await user.click(
          within(dialog).getByRole("button", { name: "Apply advanced filter" }),
        );
        expect(filters()).toMatchObject({ venue: value, offset: 0 });
        expect(screen.getByText(`Venue: ${option}`)).toBeInTheDocument();
        expect(
          screen.getByRole("button", { name: "More filters 1" }),
        ).toBeInTheDocument();
        await user.click(screen.getAllByRole("button", { name: "Next" })[0]);
        expect(filters()?.offset).toBeGreaterThan(0);
        await user.click(
          screen.getAllByRole("button", {
            name: "Remove advanced filter",
          })[0],
        );
        expect(filters()).not.toHaveProperty("venue");
        expect(filters()?.offset).toBe(0);
        expect(screen.queryByText(`Venue: ${option}`)).not.toBeInTheDocument();
      });
    }

    it(`${tab}: normalizes venue=all out of requests, chips, count and share link`, () => {
      renderOrders(entry("all"));
      expect(filters()).not.toHaveProperty("venue");
      expect(screen.queryByText(/^Venue:/)).not.toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "More filters" }),
      ).toBeInTheDocument();
      const share = screen.getByRole("link", {
        name: /Copy a link to the current filter set/i,
      });
      expect(
        new URL(
          share.getAttribute("href")!,
          window.location.origin,
        ).searchParams.has("venue"),
      ).toBe(false);
    });

    for (const action of tab === "orders"
      ? (["all"] as const)
      : (["all", "reset"] as const)) {
      it(`${tab}: clears an applied venue with dialog ${action}`, async () => {
        const user = userEvent.setup();
        const value = venueConnectionFilterValue("alpaca-paper");
        renderOrders(entry(value));
        expect(filters()?.venue).toBe(value);
        await user.click(
          screen.getByRole("button", { name: "More filters 1" }),
        );
        const dialog = screen.getByRole("dialog");
        await user.click(
          action === "all"
            ? within(
                within(dialog).getByRole("group", { name: "Venue" }),
              ).getByRole("button", { name: "All" })
            : within(dialog).getByRole("button", {
                name: "Remove advanced filter",
              }),
        );
        expect(
          within(dialog).getByLabelText("Applied venue draft").textContent,
        ).toBe("");
        expect(filters()?.venue).toBe(value);
        const apply = within(dialog).getByRole("button", {
          name: "Apply advanced filter",
        });
        expect(apply).toBeEnabled();
        await user.click(apply);
        expect(filters()).not.toHaveProperty("venue");
        expect(screen.queryByText(/^Venue:/)).not.toBeInTheDocument();
      });
    }

    if (tab === "orders") {
      it("orders: dialog reset clears the venue draft but cannot be applied while the status draft is empty", async () => {
        const user = userEvent.setup();
        const value = venueConnectionFilterValue("alpaca-paper");
        renderOrders(entry(value));
        expect(filters()?.venue).toBe(value);
        await user.click(
          screen.getByRole("button", { name: "More filters 1" }),
        );
        const dialog = screen.getByRole("dialog");
        await user.click(
          within(dialog).getByRole("button", {
            name: "Remove advanced filter",
          }),
        );
        expect(
          within(dialog).getByLabelText("Applied venue draft").textContent,
        ).toBe("");
        expect(filters()?.venue).toBe(value);
        const apply = within(dialog).getByRole("button", {
          name: "Apply advanced filter",
        });
        expect(apply).toBeDisabled();
        await user.click(apply);
        expect(filters()?.venue).toBe(value);
      });
    }

    it(`${tab}: seeds venue from URL and includes it in the share link`, async () => {
      const user = userEvent.setup();
      const value = venueConnectionFilterValue("alpaca-paper");
      renderOrders(entry(value));
      expect(filters()?.venue).toBe(value);
      expect(screen.getByText(`Venue: ${connectionLabel}`)).toBeInTheDocument();
      const share = screen.getByRole("link", {
        name: /Copy a link to the current filter set/i,
      });
      expect(
        new URL(
          share.getAttribute("href")!,
          window.location.origin,
        ).searchParams.get("venue"),
      ).toBe(value);
      await user.click(screen.getByRole("button", { name: "More filters 1" }));
      expect(
        within(screen.getByRole("dialog")).getByLabelText(
          "Applied venue draft",
        ),
      ).toHaveTextContent(value);
    });

    it(`${tab}: hides column and field without connections but preserves the URL filter and chip`, async () => {
      const user = userEvent.setup();
      if (defaultTradingResult.load.state !== "ready")
        throw new Error("Expected ready fixture");
      useTradingMock.mockReturnValue(
        readyEmpty({ ...defaultTradingResult.load.data, connections: [] }),
      );
      seedRows();
      renderOrders(entry(venueConnectionFilterValue("missing-connection")));
      expect(
        screen.queryByRole("columnheader", { name: "Venue" }),
      ).not.toBeInTheDocument();
      expect(filters()?.venue).toBe(
        venueConnectionFilterValue("missing-connection"),
      );
      expect(screen.getByText("Venue: missing-connection")).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "More filters 1" }));
      expect(
        within(screen.getByRole("dialog")).queryByRole("group", {
          name: "Venue",
        }),
      ).not.toBeInTheDocument();
      await user.keyboard("{Escape}");
      await user.click(
        screen.getAllByRole("button", { name: "Remove advanced filter" })[0],
      );
      expect(filters()).not.toHaveProperty("venue");
    });

    it(`${tab}: reports a trading load error and hides the column and field`, async () => {
      const user = userEvent.setup();
      useTradingMock.mockReturnValue({
        load: { state: "error", data: null, error: "Trading unavailable" },
        reload: vi.fn(),
      });
      seedRows();
      renderOrders(entry());
      expect(
        screen.getByText(
          "Trading connections could not be loaded: Trading unavailable",
        ),
      ).toBeInTheDocument();
      expect(
        screen.queryByRole("columnheader", { name: "Venue" }),
      ).not.toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "More filters" }));
      expect(
        within(screen.getByRole("dialog")).queryByRole("group", {
          name: "Venue",
        }),
      ).not.toBeInTheDocument();
    });

    it(`${tab}: hides the column while trading loads`, () => {
      useTradingMock.mockReturnValue({
        load: { state: "loading", data: null, error: null },
        reload: vi.fn(),
      });
      seedRows();
      renderOrders(entry());
      expect(
        screen.queryByRole("columnheader", { name: "Venue" }),
      ).not.toBeInTheDocument();
    });

    it(`${tab}: labels an unknown provider explicitly and includes disabled connections`, () => {
      if (defaultTradingResult.load.state !== "ready")
        throw new Error("Expected ready fixture");
      const snapshot = defaultTradingResult.load.data;
      useTradingMock.mockReturnValue(
        readyEmpty({
          ...snapshot,
          connections: snapshot.connections.map((connection) => ({
            ...connection,
            provider: "unknown-provider",
            enabled: false,
          })),
        }),
      );
      seedRows();
      renderOrders(entry(venueConnectionFilterValue("alpaca-paper")));
      const label = "Unknown venue unknown-provider · Paper · Primary Alpaca";
      expect(screen.getByRole("table")).toHaveTextContent(label);
      expect(screen.getByText(`Venue: ${label}`)).toBeInTheDocument();
    });
  }
});

describe("Orders verify envelope toolbar", () => {
  it("opens the standalone verify-envelope panel from the toolbar", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");

    await user.click(screen.getByRole("button", { name: "Verify envelope" }));

    // The panel opens in verify mode with only the verify tab and its paste box.
    expect(
      await screen.findByPlaceholderText("Paste a base64url envelope…"),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("tab", { name: "Reproduction" }),
    ).not.toBeInTheDocument();
  });
});

describe("Orders venue sending", () => {
  it("shows the own venue fields and mode-specific submit button", async () => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockResolvedValue([alpacaDestination]);
    renderOrders("/orders");
    const dialog = await openDialog(user, false);
    await user.click(
      within(dialog).getByRole("tab", { name: "Send to venue" }),
    );
    const content = within(dialog).getByRole("tabpanel", {
      name: "Send to venue",
    });
    expect(
      within(content).getByRole("group", { name: "Mode" }),
    ).toBeInTheDocument();
    expect(
      within(content).getByRole("button", { name: "Volume (quote)" }),
    ).toBeDisabled();
    expect(
      within(content).getByText("Orders are sent to the venue by quantity."),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "Submit order" }),
    ).toBeDisabled();
    await fillOrderFields(user, dialog);
    await user.click(within(content).getByRole("button", { name: "Paper" }));
    expect(
      await within(content).findByRole("combobox", { name: "Target" }),
    ).toHaveTextContent(/Primary Alpaca.*Alpaca.*PA-1/);
    expect(
      within(content).getByRole("combobox", { name: "Time in force" }),
    ).toHaveTextContent("Choose time in force");
    await chooseTimeInForce(user, dialog, "DAY - Day");
    expect(
      within(dialog).getByRole("button", { name: "Submit PAPER order" }),
    ).toBeEnabled();
    expect(
      await within(content).findByText("Preview: would pass"),
    ).toBeInTheDocument();
    await user.click(within(content).getByRole("button", { name: "Live" }));
    const live = within(dialog).getByRole("button", {
      name: "Submit LIVE order",
    });
    expect(live).toHaveClass("bg-[var(--danger)]");
    expect(live).toBeDisabled();
    await chooseTimeInForce(user, dialog, "DAY - Day");
    expect(live).toBeDisabled();
  });

  it("keeps the existing submit request unchanged while sending is off", async () => {
    const user = userEvent.setup();
    createOrderMock.mockRejectedValue(
      new ApiError("Request inspected", "validation", 400),
    );
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await fillOrderFields(user, dialog);
    await user.click(
      within(dialog).getByRole("tab", { name: /Submit and wait/ }),
    );

    await user.click(within(dialog).getByRole("button", { name: "Add Order" }));

    await waitFor(() =>
      expect(createOrderMock).toHaveBeenCalledWith(
        {
          account: "desk-alpha",
          baseAsset: "AAPL",
          quoteAsset: "USD",
          side: "buy",
          amountKind: "quantity",
          amountValue: "10",
          mode: "hold",
        },
        "reject",
        expect.any(AbortSignal),
      ),
    );
    expect(
      await within(dialog).findByText("Request inspected"),
    ).toBeInTheDocument();
    expect(sendPaperOrderMock).not.toHaveBeenCalled();
  });

  it("starts with no venue mode when no preference is stored", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);

    await user.click(
      within(dialog).getByRole("tab", { name: "Send to venue" }),
    );

    expect(
      within(dialog).getByRole("button", { name: "Paper" }),
    ).toHaveAttribute("aria-pressed", "false");
    expect(
      within(dialog).getByRole("button", { name: "Live" }),
    ).toHaveAttribute("aria-pressed", "false");
  });

  it("persists the venue tab and mode across close, reopen, and a successful send", async () => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockResolvedValue([alpacaDestination]);
    renderOrders("/orders");
    let dialog = await openDialog(user, false);

    await enablePaperSending(user, dialog);
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    dialog = await openDialog(user, false);

    expect(
      within(dialog).getByRole("tab", { name: "Send to venue" }),
    ).toHaveAttribute("aria-selected", "true");
    expect(
      within(dialog).getByRole("button", { name: "Paper" }),
    ).toHaveAttribute("aria-pressed", "true");

    await fillOrderFields(user, dialog);
    await waitFor(() =>
      expect(fetchTradingDestinationsMock).toHaveBeenCalledWith(
        "desk-alpha",
        "paper",
        expect.any(AbortSignal),
      ),
    );
    await within(dialog).findByRole("combobox", {
      name: "Target",
    });
    await chooseTimeInForce(user, dialog, "DAY - Day");
    await user.click(
      within(dialog).getByRole("button", {
        name: "Submit PAPER order",
      }),
    );

    const detail = await screen.findByRole("dialog", {
      name: "Order ord-alpha-1",
    });
    await within(detail).findByText("Account");
    await user.click(
      within(detail).getAllByRole("button", { name: "Close" })[0],
    );
    dialog = await openDialog(user, false);
    expect(
      within(dialog).getByRole("tab", { name: "Send to venue" }),
    ).toHaveAttribute("aria-selected", "true");
    expect(
      within(dialog).getByRole("button", { name: "Paper" }),
    ).toHaveAttribute("aria-pressed", "true");
  });

  it("starts a cloned order with no inherited target or TIF", async () => {
    const user = userEvent.setup();
    let resolveCloneDestinations!: (destinations: TradingDestination[]) => void;
    fetchTradingDestinationsMock
      .mockResolvedValueOnce([alpacaDestination])
      .mockImplementationOnce(
        () =>
          new Promise<TradingDestination[]>((resolve) => {
            resolveCloneDestinations = resolve;
          }),
      );
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await fillOrderFields(user, dialog);
    await enablePaperSending(user, dialog);
    await chooseTimeInForce(user, dialog, "GTC - Good till cancelled");
    await user.click(
      within(dialog).getByRole("button", {
        name: "Submit PAPER order",
      }),
    );

    const detail = await screen.findByRole("dialog", {
      name: `Order ${sampleOrder.id}`,
    });
    await act(async () => {
      await new Promise((resolve) =>
        window.setTimeout(resolve, DEFAULT_SEARCH_DEBOUNCE_MS + 50),
      );
    });
    await user.click(
      await within(detail).findByRole("button", {
        name: `Clone order ${sampleOrder.id}`,
      }),
    );

    const clone = await screen.findByRole("dialog", { name: "Add order" });
    expect(within(clone).getByLabelText("Account")).toHaveValue("desk-alpha");
    expect(
      within(clone).queryByRole("combobox", { name: "Time in force" }),
    ).not.toBeInTheDocument();
    expect(
      within(clone).getByRole("button", { name: "Submit PAPER order" }),
    ).toBeDisabled();

    await waitFor(() =>
      expect(fetchTradingDestinationsMock).toHaveBeenCalledTimes(2),
    );
    await act(async () => {
      resolveCloneDestinations([alpacaDestination]);
      await Promise.resolve();
    });

    expect(
      await within(clone).findByRole("combobox", {
        name: "Target",
      }),
    ).toHaveTextContent(/Primary Alpaca.*Alpaca.*PA-1/);
    expect(
      within(clone).getByRole("combobox", { name: "Time in force" }),
    ).toHaveTextContent("Choose time in force");
    expect(
      within(clone).getByRole("button", {
        name: "Submit PAPER order",
      }),
    ).toBeDisabled();
  });

  it("fetches destinations and preselects the only selectable target", async () => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockResolvedValue([alpacaDestination]);
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await fillOrderFields(user, dialog);
    await enablePaperSending(user, dialog);

    await waitFor(() =>
      expect(fetchTradingDestinationsMock).toHaveBeenCalledWith(
        "desk-alpha",
        "paper",
        expect.any(AbortSignal),
      ),
    );
    expect(
      await within(dialog).findByRole("combobox", {
        name: "Target",
      }),
    ).toHaveTextContent(/Primary Alpaca.*Alpaca.*PA-1/);
    const timeInForce = within(dialog).getByRole("combobox", {
      name: "Time in force",
    });
    expect(timeInForce).toHaveTextContent("Choose time in force");
    expect(timeInForce).toBeRequired();
    expect(
      within(dialog).getByRole("button", {
        name: "Submit PAPER order",
      }),
    ).toBeDisabled();

    await chooseTimeInForce(user, dialog, "DAY - Day");
    expect(
      within(dialog).getByRole("button", {
        name: "Submit PAPER order",
      }),
    ).toBeEnabled();
  });

  it("keeps current targets when the selected mode is chosen again", async () => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockResolvedValue([alpacaDestination]);
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await fillOrderFields(user, dialog);
    await enablePaperSending(user, dialog);

    expect(
      await within(dialog).findByRole("combobox", {
        name: "Target",
      }),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Paper" }));

    expect(
      within(dialog).getByRole("combobox", {
        name: "Target",
      }),
    ).toBeInTheDocument();
    expect(fetchTradingDestinationsMock).toHaveBeenCalledTimes(1);
  });

  it("keeps current targets when the exact account suggestion is picked", async () => {
    const user = userEvent.setup();
    fetchAccountsMock.mockResolvedValue([{ code: "desk-alpha" }]);
    fetchTradingDestinationsMock.mockResolvedValue([alpacaDestination]);
    renderOrders("/orders");
    const dialog = await openDialog(user);

    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await enablePaperSending(user, dialog);
    expect(
      await within(dialog).findByRole("combobox", {
        name: "Target",
      }),
    ).toBeInTheDocument();

    await user.click(within(dialog).getByLabelText("Account"));
    await user.click(
      await within(dialog).findByRole("option", { name: "desk-alpha" }),
    );

    expect(
      within(dialog).getByRole("combobox", {
        name: "Target",
      }),
    ).toBeInTheDocument();
    expect(fetchTradingDestinationsMock).toHaveBeenCalledTimes(1);
  });

  it("refetches targets on reopen with a pre-filled account and stored preferences", async () => {
    const user = userEvent.setup();
    setStoredPreference("pit-officer-orders-submit-tab", "venue");
    setStoredPreference("pit-officer-orders-venue-mode", "paper");
    fetchTradingDestinationsMock.mockResolvedValue([alpacaDestination]);
    renderOrders("/orders?account=desk-alpha");

    let dialog = await openDialog(user);
    expect(
      await within(dialog).findByRole("combobox", {
        name: "Target",
      }),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));

    dialog = await openDialog(user);
    expect(
      await within(dialog).findByRole("combobox", {
        name: "Target",
      }),
    ).toBeInTheDocument();
    expect(fetchTradingDestinationsMock).toHaveBeenCalledTimes(2);
  });

  it("does not send to the previous account target during the debounce", async () => {
    const user = userEvent.setup();
    let resolveAlpha!: (destinations: TradingDestination[]) => void;
    let resolveBeta!: (destinations: TradingDestination[]) => void;
    fetchTradingDestinationsMock.mockImplementation(
      (account: string) =>
        new Promise<TradingDestination[]>((resolve) => {
          if (account === "desk-alpha") {
            resolveAlpha = resolve;
          } else {
            resolveBeta = resolve;
          }
        }),
    );
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await fillOrderFields(user, dialog);
    await enablePaperSending(user, dialog);
    await waitFor(() =>
      expect(fetchTradingDestinationsMock).toHaveBeenCalledWith(
        "desk-alpha",
        "paper",
        expect.any(AbortSignal),
      ),
    );

    const account = within(dialog).getByLabelText("Account");
    fireEvent.change(account, { target: { value: "desk-beta" } });
    await act(async () => {
      resolveAlpha([alpacaDestination]);
      await Promise.resolve();
    });

    expect(
      within(dialog).queryByRole("combobox", {
        name: "Target",
      }),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).queryByRole("combobox", { name: "Time in force" }),
    ).not.toBeInTheDocument();
    const send = within(dialog).getByRole("button", {
      name: "Submit PAPER order",
    });
    expect(send).toBeDisabled();
    await user.click(send);
    expect(sendPaperOrderMock).not.toHaveBeenCalled();

    await waitFor(() =>
      expect(fetchTradingDestinationsMock).toHaveBeenCalledWith(
        "desk-beta",
        "paper",
        expect.any(AbortSignal),
      ),
    );
    expect(
      within(dialog).queryByRole("combobox", { name: "Time in force" }),
    ).not.toBeInTheDocument();
    await act(async () => {
      resolveBeta([
        {
          ...alpacaDestination,
          connection: "alpaca-beta",
          label: "Beta Alpaca",
          venueAccount: "PB-1",
        },
      ]);
      await Promise.resolve();
    });

    expect(
      await within(dialog).findByRole("combobox", {
        name: "Target",
      }),
    ).toHaveTextContent(/Beta Alpaca.*Alpaca.*PB-1/);
    expect(
      within(dialog).getByRole("combobox", { name: "Time in force" }),
    ).toHaveTextContent("Choose time in force");
    expect(
      within(dialog).getByRole("button", {
        name: "Submit PAPER order",
      }),
    ).toBeDisabled();
  });

  it("requires an explicit choice when several targets are selectable", async () => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockResolvedValue([
      alpacaDestination,
      {
        ...alpacaDestination,
        connection: "alpaca-secondary",
        label: "Secondary Alpaca",
        venueAccount: "PA-2",
      },
    ]);
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await fillOrderFields(user, dialog);
    await enablePaperSending(user, dialog);

    const target = await within(dialog).findByRole("combobox", {
      name: "Target",
    });
    expect(target).toHaveTextContent("Choose a target");
    expect(
      within(dialog).getByRole("button", { name: "Submit PAPER order" }),
    ).toBeDisabled();

    const focusSpy = vi
      .spyOn(HTMLElement.prototype, "focus")
      .mockImplementation(() => {});
    try {
      await user.click(target);
      const targets = await screen.findByRole("listbox");
      const primary = within(targets).getByRole("option", {
        name: /Primary Alpaca.*PA-1/,
      });
      const secondary = within(targets).getByRole("option", {
        name: /Secondary Alpaca.*PA-2/,
      });
      expect(primary).toHaveAttribute("data-state", "unchecked");
      expect(secondary).toHaveAttribute("data-state", "unchecked");
      await user.click(secondary);
    } finally {
      focusSpy.mockRestore();
    }
    expect(target).toHaveTextContent(/Secondary Alpaca.*PA-2/);
    await chooseTimeInForce(user, dialog, "GTC - Good till cancelled");
    expect(
      within(dialog).getByRole("button", {
        name: "Submit PAPER order",
      }),
    ).toBeEnabled();
  });

  it("does not allow disabled or unavailable destinations", async () => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockResolvedValue([
      { ...alpacaDestination, enabled: false, label: "Disabled Alpaca" },
      {
        ...alpacaDestination,
        connection: "alpaca-unavailable",
        label: "Unavailable Alpaca",
        state: "unavailable",
        error: "Invalid API key",
      },
    ]);
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await fillOrderFields(user, dialog);
    await enablePaperSending(user, dialog);

    expect(
      await within(dialog).findByText(
        "No paper targets for account desk-alpha.",
      ),
    ).toBeInTheDocument();
    const focusSpy = vi
      .spyOn(HTMLElement.prototype, "focus")
      .mockImplementation(() => {});
    try {
      await user.click(
        within(dialog).getByRole("combobox", { name: "Target" }),
      );
      const targets = await screen.findByRole("listbox");
      expect(
        within(targets).getByRole("option", {
          name: /Disabled Alpaca.*Disabled/,
        }),
      ).toHaveAttribute("data-disabled");
      expect(
        within(targets).getByRole("option", {
          name: /Unavailable Alpaca.*Unavailable: Invalid API key/,
        }),
      ).toHaveAttribute("data-disabled");
      await user.keyboard("{Escape}");
    } finally {
      focusSpy.mockRestore();
    }
  });

  it("keeps a degraded destination selectable and notes its error", async () => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockResolvedValue([
      alpacaDestination,
      {
        ...alpacaDestination,
        connection: "alpaca-degraded",
        label: "Degraded Alpaca",
        venueAccount: "PA-2",
        state: "degraded",
        error: "Trade updates refused",
      },
    ]);
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await fillOrderFields(user, dialog);
    await enablePaperSending(user, dialog);

    const target = await within(dialog).findByRole("combobox", {
      name: "Target",
    });
    const focusSpy = vi
      .spyOn(HTMLElement.prototype, "focus")
      .mockImplementation(() => {});
    try {
      await user.click(target);
      const targets = await screen.findByRole("listbox");
      expect(
        within(targets).getByRole("option", { name: /Primary Alpaca/ }),
      ).not.toHaveTextContent("Degraded");
      const degraded = within(targets).getByRole("option", {
        name: /Degraded Alpaca.*PA-2.*Degraded: Trade updates refused/,
      });
      expect(degraded).not.toHaveAttribute("data-disabled");
      await user.click(degraded);
    } finally {
      focusSpy.mockRestore();
    }
    expect(target).toHaveTextContent(
      /Degraded Alpaca.*Degraded: Trade updates refused/,
    );
    await chooseTimeInForce(user, dialog, "GTC - Good till cancelled");
    expect(
      within(dialog).getByRole("button", { name: "Submit PAPER order" }),
    ).toBeEnabled();
  });

  it("explains missing paper and live targets and links to setup", async () => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockResolvedValue([]);
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await enablePaperSending(user, dialog);

    expect(
      await within(dialog).findByText(
        "No paper targets for account desk-alpha.",
      ),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("link", {
        name: "Set up a trading connection",
      }),
    ).toHaveAttribute("href", "/trading-connections");

    await user.click(within(dialog).getByRole("button", { name: "Live" }));
    expect(
      within(dialog).getByText(
        "Live trading is not available yet: it arrives together with the live connection token.",
      ),
    ).toBeInTheDocument();
    expect(
      await within(dialog).findByText(
        "No live targets for account desk-alpha.",
      ),
    ).toBeInTheDocument();
  });

  it("shows a destination fetch error instead of a no-target explanation", async () => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockRejectedValue(
      new ApiError("Destination lookup failed", "upstream", 502),
    );
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await user.type(within(dialog).getByLabelText("Account"), "desk-alpha");
    await enablePaperSending(user, dialog);

    expect(
      await within(dialog).findByText("Destination lookup failed"),
    ).toBeInTheDocument();
    expect(
      within(dialog).queryByText(/No paper targets/),
    ).not.toBeInTheDocument();
  });

  it("disables and clears quote volume when venue sending turns on", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);
    let volume = within(dialog).getByRole("button", {
      name: "Volume (quote)",
    });
    await user.click(volume);
    expect(volume).toHaveAttribute("aria-pressed", "true");

    await user.click(
      within(dialog).getByRole("tab", { name: "Send to venue" }),
    );

    volume = within(dialog).getByRole("button", { name: "Volume (quote)" });
    const quantity = within(dialog).getByRole("button", {
      name: "Quantity (base)",
    });
    expect(volume).toBeDisabled();
    expect(volume).toHaveAttribute("aria-pressed", "false");
    expect(quantity).toHaveAttribute("aria-pressed", "false");
    expect(
      within(dialog).getByText("Orders are sent to the venue by quantity."),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("tab", { name: "Send to venue" }),
    ).toHaveAttribute("aria-selected", "true");
  });

  it("ignores the hidden drop-copy price rules while sending to a venue", async () => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockResolvedValue([alpacaDestination]);
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await fillOrderFields(user, dialog);
    await user.click(within(dialog).getByRole("tab", { name: /Drop copy/ }));
    expect(
      within(dialog).getByLabelText("Limit price (required for drop-copy)"),
    ).toBeInTheDocument();

    await enablePaperSending(user, dialog);
    await within(dialog).findByRole("combobox", {
      name: "Target",
    });
    await chooseTimeInForce(user, dialog, "DAY - Day");

    expect(
      within(dialog).getByLabelText("Limit price (optional)"),
    ).toHaveAttribute("placeholder", "leave empty = market");
    expect(
      within(dialog).queryByText(
        "Market orders cannot be submitted as drop-copy.",
      ),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", {
        name: "Submit PAPER order",
      }),
    ).toBeEnabled();
  });

  it("sends the exact paper-order body with the selected TIF", async () => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockResolvedValue([alpacaDestination]);
    sendPaperOrderMock.mockRejectedValue(
      new ApiError("Request inspected", "upstream", 502),
    );
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await user.type(within(dialog).getByLabelText("ID (optional)"), "client-9");
    await fillOrderFields(user, dialog);
    await user.type(
      within(dialog).getByLabelText("Limit price (required to settle)"),
      "42.5",
    );
    await enablePaperSending(user, dialog);
    await chooseTimeInForce(user, dialog, "IOC - Immediate or cancel");

    await user.click(
      within(dialog).getByRole("button", {
        name: "Submit PAPER order",
      }),
    );

    await waitFor(() =>
      expect(sendPaperOrderMock).toHaveBeenCalledWith(
        {
          id: "client-9",
          account: "desk-alpha",
          baseAsset: "AAPL",
          quoteAsset: "USD",
          side: "buy",
          amountKind: "quantity",
          amountValue: "10",
          price: "42.5",
          destination: {
            connection: "alpaca-paper",
            venueAccount: "PA-1",
            route: { timeInForce: "ioc" },
          },
        },
        expect.any(AbortSignal),
      ),
    );
    expect(
      await within(dialog).findByText("Request inspected"),
    ).toBeInTheDocument();
    expect(createOrderMock).not.toHaveBeenCalled();
  });

  it.each([
    [
      "acknowledged",
      {
        status: "acknowledged" as const,
        venueOrder: linkedVenueOrder,
      },
      "Sent to Alpaca · paper. Venue order venue-123.",
      "border-[var(--ok)]",
    ],
    [
      "venue-refused",
      {
        status: "refused" as const,
        error: "Buying power exceeded",
        venueOrder: { ...linkedVenueOrder, venueOrderId: undefined },
      },
      "The order was not placed on Alpaca: Buying power exceeded. Officer recorded it as cancelled.",
      "border-[var(--danger)]",
    ],
    [
      "not-sent refused",
      {
        status: "refused" as const,
        error: "order was not handed to the venue",
      },
      "The order was not placed on Alpaca: order was not handed to the venue. Officer recorded it as cancelled.",
      "border-[var(--danger)]",
    ],
    [
      "unknown",
      { status: "unknown" as const, error: "Request timed out" },
      "The venue did not confirm the order: Request timed out. Officer is reconciling it; the order status below is authoritative.",
      "border-[var(--warn)]",
    ],
    [
      "pre-trade reject",
      undefined,
      "Pre-trade checks rejected the order. Nothing was sent to the venue.",
      "border-[var(--danger)]",
    ],
  ])(
    "opens detail with the %s venue outcome banner",
    async (_, venueSend, text, toneClass) => {
      const user = userEvent.setup();
      fetchTradingDestinationsMock.mockResolvedValue([alpacaDestination]);
      sendPaperOrderMock.mockResolvedValue({
        signedApproval: "paper-envelope",
        keyId: "key-1",
        id: sampleOrder.id,
        verdict: venueSend ? "accept" : "reject",
        reasons: [],
        ...(venueSend ? { venueSend } : {}),
      });
      renderOrders("/orders");
      const dialog = await openDialog(user);
      await fillOrderFields(user, dialog);
      await enablePaperSending(user, dialog);
      await chooseTimeInForce(user, dialog, "DAY - Day");

      await user.click(
        within(dialog).getByRole("button", {
          name: "Submit PAPER order",
        }),
      );

      const banner = await screen.findByText(text);
      expect(banner).toHaveClass(toneClass);
      const detail = screen.getByRole("dialog", {
        name: `Order ${sampleOrder.id}`,
      });
      await within(detail).findByText("Account");
    },
  );

  it.each([
    ["trading_mode_mismatch", 409],
    ["precondition", 422],
    ["upstream", 502],
  ])("keeps the form after a %s send error", async (code, status) => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockResolvedValue([alpacaDestination]);
    sendPaperOrderMock.mockRejectedValue(
      new ApiError("Server kept this order out", code as ApiErrorCode, status),
    );
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await fillOrderFields(user, dialog);
    await enablePaperSending(user, dialog);
    await chooseTimeInForce(user, dialog, "DAY - Day");

    await user.click(
      within(dialog).getByRole("button", {
        name: "Submit PAPER order",
      }),
    );

    expect(
      await within(dialog).findByText("Server kept this order out"),
    ).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Account")).toHaveValue("desk-alpha");
    expect(sendPaperOrderMock).toHaveBeenCalledTimes(1);
  });

  it.each(["connection reset", "connection reset.", "connection reset。"])(
    "warns after a network error (%s) without doubling the stop and never retries the send",
    async (message) => {
      const user = userEvent.setup();
      fetchTradingDestinationsMock.mockResolvedValue([alpacaDestination]);
      sendPaperOrderMock.mockRejectedValue(new ApiError(message, "network"));
      renderOrders("/orders");
      const dialog = await openDialog(user);
      await fillOrderFields(user, dialog);
      await enablePaperSending(user, dialog);
      await chooseTimeInForce(user, dialog, "DAY - Day");

      await user.click(
        within(dialog).getByRole("button", {
          name: "Submit PAPER order",
        }),
      );

      expect(
        await within(dialog).findByText(
          "The send request did not complete: connection reset. Check Orders before sending again - the order may already exist.",
        ),
      ).toBeInTheDocument();
      expect(sendPaperOrderMock).toHaveBeenCalledTimes(1);
    },
  );

  it.each([
    [
      "an unreadable successful response",
      new ApiError("response body is not valid JSON.", "internal", 201),
      "response body is not valid JSON",
    ],
    [
      "a venue-send normalization failure",
      new ApiError("venue send payload is invalid。", "internal"),
      "venue send payload is invalid",
    ],
    [
      "a body-less gateway timeout",
      new ApiError("Gateway Timeout", "internal", 504),
      "HTTP 504",
    ],
    [
      "an internal server error",
      new ApiError("Officer internal error", "internal", 500),
      "HTTP 500",
    ],
    [
      "a body-less service unavailable response",
      new ApiError(
        "The risk engine is restarting; retry after it finishes.",
        "engine_restarting",
        503,
      ),
      "HTTP 503",
    ],
  ])("warns after %s and never retries", async (_, sendError, message) => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockResolvedValue([alpacaDestination]);
    sendPaperOrderMock.mockRejectedValue(sendError);
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await fillOrderFields(user, dialog);
    await enablePaperSending(user, dialog);
    await chooseTimeInForce(user, dialog, "DAY - Day");

    await user.click(
      within(dialog).getByRole("button", {
        name: "Submit PAPER order",
      }),
    );

    expect(
      await within(dialog).findByText(
        `The send request did not complete: ${message}. Check Orders before sending again - the order may already exist.`,
      ),
    ).toBeInTheDocument();
    expect(sendPaperOrderMock).toHaveBeenCalledTimes(1);
  });
});

async function openSampleOrderDetail(
  user: ReturnType<typeof userEvent.setup>,
  detail: {
    order: Order;
    events: OrderEvent[];
    trades: Trade[];
    venueOrder?: VenueOrder;
  },
) {
  useOrdersMock.mockReturnValue(readyPage<Order>([detail.order]));
  fetchOrderDetailMock.mockResolvedValue(detail);
  renderOrders("/orders");
  await user.click(screen.getByText(detail.order.id));
  return screen.findByRole("dialog", {
    name: `Order ${detail.order.id}`,
  });
}

describe("Order detail venue section", () => {
  it.each([
    ["acknowledged", linkedVenueOrder, sampleOrder, "venue-123"],
    [
      "awaiting acknowledgement",
      { ...linkedVenueOrder, venueOrderId: undefined },
      sampleOrder,
      "Awaiting venue acknowledgement",
    ],
    [
      "not acknowledged",
      { ...linkedVenueOrder, venueOrderId: undefined },
      { ...sampleOrder, status: "cancelled" },
      "Not acknowledged by the venue",
    ],
    [
      "not sent",
      {
        ...linkedVenueOrder,
        venueOrderId: undefined,
        sendAttempted: false,
      },
      sampleOrder,
      "Not sent yet",
    ],
  ])(
    "shows the %s venue-order state",
    async (_, venueOrder, order, expected) => {
      const user = userEvent.setup();
      const dialog = await openSampleOrderDetail(user, {
        order,
        events: [],
        trades: [],
        venueOrder,
      });

      expect(within(dialog).getByText("Venue")).toBeInTheDocument();
      expect(within(dialog).getByText("Primary Alpaca")).toBeInTheDocument();
      expect(within(dialog).getByText("PA-1")).toBeInTheDocument();
      expect(within(dialog).getByText(expected)).toBeInTheDocument();
      expect(within(dialog).getByText("DAY")).toBeInTheDocument();
      expect(within(dialog).getByText("Linked at")).toBeInTheDocument();
      expect(
        within(dialog).getByText("No fills from the venue yet."),
      ).toBeInTheDocument();
      expect(
        within(dialog).queryByText(
          "No fills recorded yet. Submit an execution report to create a trade.",
        ),
      ).not.toBeInTheDocument();
    },
  );

  it.each([
    ["submitted", "Submitted"],
    ["accepted", "Accepted"],
    ["committed", "Open"],
    ["partially_filled", "Open"],
    ["filled", "Filled"],
    ["cancelled", "Cancelled, rejected or expired at the venue"],
    ["rejected", "Rejected"],
  ])("maps %s to venue status %s", async (status, expected) => {
    const user = userEvent.setup();
    const dialog = await openSampleOrderDetail(user, {
      order: { ...sampleOrder, status },
      events: [],
      trades: [],
      venueOrder: linkedVenueOrder,
    });

    const venueStatus = within(dialog).getByText("Venue status").parentElement;
    expect(venueStatus).toHaveTextContent(expected);
  });

  it("links venue adjustments when the global account filter allows it", async () => {
    const user = userEvent.setup();
    const dialog = await openSampleOrderDetail(user, {
      order: sampleOrder,
      events: [],
      trades: [],
      venueOrder: linkedVenueOrder,
    });

    expect(
      within(dialog).getByText(
        "Alpaca fees that are not tied to orders are booked later as account adjustments.",
      ),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("link", { name: "View adjustments" }),
    ).toHaveAttribute(
      "href",
      "/positions?tab=history&account=desk-alpha&source=system",
    );
  });

  it("omits the adjustments link for a different global account", async () => {
    const user = userEvent.setup();
    setGlobalAccountFilter("desk-other");
    const dialog = await openSampleOrderDetail(user, {
      order: sampleOrder,
      events: [],
      trades: [],
      venueOrder: linkedVenueOrder,
    });

    expect(
      within(dialog).getByText(
        "Alpaca fees that are not tied to orders are booked later as account adjustments.",
      ),
    ).toBeInTheDocument();
    expect(
      within(dialog).queryByRole("link", { name: "View adjustments" }),
    ).not.toBeInTheDocument();
  });

  it("uses provider-neutral fee copy when the connection is unknown", async () => {
    const user = userEvent.setup();
    const dialog = await openSampleOrderDetail(user, {
      order: sampleOrder,
      events: [],
      trades: [],
      venueOrder: {
        ...linkedVenueOrder,
        connection: "missing-connection",
      },
    });

    expect(
      within(dialog).getByText(
        "Venue fees that are not tied to orders are booked later as account adjustments.",
      ),
    ).toBeInTheDocument();
    expect(
      within(dialog).queryByText(/missing-connection fees/),
    ).not.toBeInTheDocument();
  });

  it("hides manual execution actions for a venue-linked order", async () => {
    const user = userEvent.setup();
    const acceptedEvent: OrderEvent = {
      id: "evt-accepted",
      order: sampleOrder.id,
      at: sampleOrder.at,
      type: "pre_trade_accepted",
      source: "panel",
      principal: "",
      signed: true,
      alg: "ed25519",
    };
    const fillEvent: OrderEvent = {
      ...acceptedEvent,
      id: "evt-fill",
      type: "fill",
      fillQuantity: "1",
      fillPrice: "10",
    };
    const trade: Trade = {
      id: "trade-1",
      order: sampleOrder.id,
      account: sampleOrder.account,
      at: sampleOrder.at,
      source: "panel",
      baseAsset: sampleOrder.baseAsset,
      quoteAsset: sampleOrder.quoteAsset,
      side: sampleOrder.side,
      quantity: "1",
      price: "10",
      lockPrice: "10",
    };
    const dialog = await openSampleOrderDetail(user, {
      order: sampleOrder,
      events: [acceptedEvent, fillEvent],
      trades: [trade],
      venueOrder: linkedVenueOrder,
    });

    expect(
      within(dialog).getByText(
        "Fills and cancellation of this order come from the venue.",
      ),
    ).toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", { name: "Confirm" }),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", { name: "Cancel order" }),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", {
        name: "Submit execution report",
      }),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", {
        name: `Clone execution report from order ${sampleOrder.id}`,
      }),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: /clone order/i }),
    ).toBeInTheDocument();
  });

  it("keeps manual execution actions for an ordinary order", async () => {
    const user = userEvent.setup();
    const acceptedEvent: OrderEvent = {
      id: "evt-accepted",
      order: sampleOrder.id,
      at: sampleOrder.at,
      type: "pre_trade_accepted",
      source: "panel",
      principal: "",
      signed: true,
      alg: "ed25519",
    };
    const fillEvent: OrderEvent = {
      ...acceptedEvent,
      id: "evt-fill",
      type: "fill",
      fillQuantity: "1",
      fillPrice: "10",
    };
    const dialog = await openSampleOrderDetail(user, {
      order: sampleOrder,
      events: [acceptedEvent, fillEvent],
      trades: [],
    });

    expect(
      within(dialog).getByRole("button", { name: "Confirm" }),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "Cancel order" }),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", {
        name: "Submit execution report",
      }),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", {
        name: `Clone execution report from order ${sampleOrder.id}`,
      }),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText(
        "No fills recorded yet. Submit an execution report to create a trade.",
      ),
    ).toBeInTheDocument();
  });

  it("keeps the verification panel open when polling refreshes a venue order", async () => {
    const user = userEvent.setup();
    const signedEvent: OrderEvent = {
      id: "evt-signed",
      order: sampleOrder.id,
      at: sampleOrder.at,
      type: "fill",
      source: "panel",
      principal: "",
      fillQuantity: "1",
      fillPrice: "10",
      signed: true,
      alg: "ed25519",
    };
    useOrdersMock.mockReturnValue(readyPage<Order>([sampleOrder]));
    fetchOrderDetailMock
      .mockResolvedValueOnce({
        order: sampleOrder,
        events: [signedEvent],
        trades: [],
        venueOrder: linkedVenueOrder,
      })
      .mockResolvedValueOnce({
        order: { ...sampleOrder, status: "filled" },
        events: [signedEvent],
        trades: [],
        venueOrder: linkedVenueOrder,
      });
    fetchEventReproductionMock.mockRejectedValue(
      new Error("Verification fixture intentionally unavailable"),
    );
    let poll: (() => void) | undefined;
    const originalSetInterval = window.setInterval.bind(window);
    const setIntervalSpy = vi
      .spyOn(window, "setInterval")
      .mockImplementation((handler, delay, ...args) => {
        if (delay === 5000 && typeof handler === "function") {
          poll = () => handler();
          return 1 as unknown as ReturnType<typeof setInterval>;
        }
        return originalSetInterval(
          handler,
          delay,
          ...args,
        ) as unknown as ReturnType<typeof setInterval>;
      });
    const clearIntervalSpy = vi.spyOn(window, "clearInterval");

    renderOrders("/orders");
    await user.click(screen.getByText(sampleOrder.id));
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(setIntervalSpy).toHaveBeenCalledWith(expect.any(Function), 5000);

    const detail = screen.getByRole("dialog", {
      name: `Order ${sampleOrder.id}`,
    });
    await user.click(
      within(detail).getByRole("button", {
        name: "Reproduce and verify this event's signature",
      }),
    );
    const verification = await screen.findByRole("dialog", {
      name: "Reproduce and verify",
    });
    expect(poll).toBeDefined();

    await act(async () => {
      poll?.();
      await Promise.resolve();
    });

    expect(fetchOrderDetailMock).toHaveBeenCalledTimes(2);
    expect(
      within(detail).queryByText(/Loading order details/),
    ).not.toBeInTheDocument();
    expect(within(detail).getByText("Filled")).toBeInTheDocument();
    expect(verification).toBeInTheDocument();
    await waitFor(() => expect(clearIntervalSpy).toHaveBeenCalledWith(1));

    setIntervalSpy.mockRestore();
    clearIntervalSpy.mockRestore();
  });
});

// A failed dry-run as the API returns it: the would-block comes with the
// reject that causes it.
function failedCheckBlocking(account: string, code: string, reason: string) {
  return {
    passed: false,
    rejects: [
      { code, scope: "account", policy: "daily_loss", reason, details: "" },
    ],
    wouldDisplayPrice: "",
    wouldBlock: { account, policy: "daily_loss", code, reason, details: "" },
  };
}

describe("Orders would-block preview with accounts shown", () => {
  it.each([
    ["account_blocked", "", "Account desk-alpha is blocked"],
    [
      "pnl_kill_switch_triggered",
      "Daily loss limit",
      "Would block account desk-alpha - Daily loss limit",
    ],
    ["pnl_kill_switch_triggered", "", "Would block account desk-alpha"],
  ])(
    "previews the %s block with reason %j naming the account",
    async (code, reason, text) => {
      const user = userEvent.setup();
      checkOrderMock.mockResolvedValue(
        failedCheckBlocking("desk-alpha", code, reason),
      );
      renderOrders("/orders");
      const dialog = await openDialog(user);
      await fillOrderFields(user, dialog);

      expect(await within(dialog).findByText(text)).toBeInTheDocument();
    },
  );
});

describe("Orders add-order dialog with accounts hidden", () => {
  beforeEach(() => {
    setAccountScope({ accounts: false, account: "main" });
  });

  it("shows no account field on any tab and fetches no account suggestions", async () => {
    const user = userEvent.setup();
    setStoredPreference("pit-officer-orders-submit-tab", "immediate");
    renderOrders("/orders");
    const dialog = await openDialog(user, false);

    expect(within(dialog).getByLabelText("Base asset")).toHaveFocus();
    for (const label of [
      "Submit and settle",
      "Drop copy",
      "Submit and wait",
      "Send to venue",
    ]) {
      await user.click(within(dialog).getByRole("tab", { name: label }));
      expect(
        within(dialog).queryByLabelText("Account"),
      ).not.toBeInTheDocument();
    }
    await user.type(within(dialog).getByLabelText("Base asset"), "AA");
    await act(async () => {
      await new Promise((resolve) =>
        window.setTimeout(resolve, DEFAULT_SEARCH_DEBOUNCE_MS + 50),
      );
    });
    expect(fetchAssetsMock).toHaveBeenCalled();
    expect(fetchAccountsMock).not.toHaveBeenCalled();
  });

  it("checks and creates a cloned order on the default account", async () => {
    const user = userEvent.setup();
    const detail = await openSampleOrderDetail(user, {
      order: { ...sampleOrder, price: "123" },
      events: [],
      trades: [],
    });
    await user.click(
      await within(detail).findByRole("button", {
        name: `Clone order ${sampleOrder.id}`,
      }),
    );
    const dialog = await screen.findByRole("dialog", { name: "Add order" });
    await user.click(
      within(dialog).getByRole("tab", { name: "Submit and wait" }),
    );
    await within(dialog).findByText("Preview: would pass");
    const order = {
      account: "main",
      baseAsset: "AAPL",
      quoteAsset: "USD",
      side: "buy",
      amountKind: "quantity",
      amountValue: "100",
      price: "123",
    };
    expect(checkOrderMock).toHaveBeenCalledWith(order, expect.any(AbortSignal));

    await user.click(within(dialog).getByRole("button", { name: "Add Order" }));

    await waitFor(() =>
      expect(createOrderMock).toHaveBeenCalledWith(
        { ...order, mode: "hold" },
        "reject",
        expect.any(AbortSignal),
      ),
    );
  });

  it("creates a drop copy on the default account", async () => {
    const user = userEvent.setup();
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await fillOrderFieldsAfterAccount(user, dialog);
    await user.click(within(dialog).getByRole("tab", { name: "Drop copy" }));
    await user.type(
      within(dialog).getByLabelText("Limit price (required for drop-copy)"),
      "100",
    );

    await user.click(
      within(dialog).getByRole("button", { name: "Submit drop-copy" }),
    );
    const confirmation = await screen.findByRole("alertdialog", {
      name: /submit without enforced pre-trade approval/i,
    });
    await user.click(
      within(confirmation).getByRole("button", { name: "Submit drop-copy" }),
    );

    await waitFor(() =>
      expect(createOrderMock).toHaveBeenCalledWith(
        {
          account: "main",
          baseAsset: "AAPL",
          quoteAsset: "USD",
          side: "buy",
          amountKind: "quantity",
          amountValue: "10",
          price: "100",
          mode: "drop_copy",
        },
        "create",
        expect.any(AbortSignal),
      ),
    );
  });

  it("looks up targets and sends a paper order on the default account", async () => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockResolvedValue([alpacaDestination]);
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await fillOrderFieldsAfterAccount(user, dialog);
    await enablePaperSending(user, dialog);

    await waitFor(() =>
      expect(fetchTradingDestinationsMock).toHaveBeenCalledWith(
        "main",
        "paper",
        expect.any(AbortSignal),
      ),
    );
    await within(dialog).findByRole("combobox", { name: "Target" });
    await chooseTimeInForce(user, dialog, "DAY - Day");
    await user.click(
      within(dialog).getByRole("button", { name: "Submit PAPER order" }),
    );

    await waitFor(() =>
      expect(sendPaperOrderMock).toHaveBeenCalledWith(
        {
          account: "main",
          baseAsset: "AAPL",
          quoteAsset: "USD",
          side: "buy",
          amountKind: "quantity",
          amountValue: "10",
          destination: {
            connection: "alpaca-paper",
            venueAccount: "PA-1",
            route: { timeInForce: "day" },
          },
        },
        expect.any(AbortSignal),
      ),
    );
    for (const [account] of fetchTradingDestinationsMock.mock.calls) {
      expect(account).toBe("main");
    }
  });

  it.each([
    ["account_blocked", "", "The account is blocked"],
    [
      "pnl_kill_switch_triggered",
      "Daily loss limit",
      "Would block the account - Daily loss limit",
    ],
    ["pnl_kill_switch_triggered", "", "Would block the account"],
  ])(
    "previews the %s block with reason %j without naming the account",
    async (code, reason, text) => {
      const user = userEvent.setup();
      checkOrderMock.mockResolvedValue(
        failedCheckBlocking("main", code, reason),
      );
      renderOrders("/orders");
      const dialog = await openDialog(user);
      await fillOrderFieldsAfterAccount(user, dialog);

      expect(await within(dialog).findByText(text)).toBeInTheDocument();
    },
  );

  it("explains missing targets without naming the account", async () => {
    const user = userEvent.setup();
    fetchTradingDestinationsMock.mockResolvedValue([]);
    renderOrders("/orders");
    const dialog = await openDialog(user);
    await enablePaperSending(user, dialog);

    expect(
      await within(dialog).findByText("No paper targets."),
    ).toBeInTheDocument();
    expect(fetchTradingDestinationsMock).toHaveBeenCalledWith(
      "main",
      "paper",
      expect.any(AbortSignal),
    );
  });
});

// The account surfaces of both tabs, by name: the sortable column, the row's
// account cell and its filter-by button, and the filter-bar field with its
// global toggle.
async function accountSurfaces(
  user: ReturnType<typeof userEvent.setup>,
  account: string,
) {
  const surfaces: Record<string, HTMLElement | null> = {};
  for (const tab of ["orders", "trades"]) {
    await user.click(
      screen.getByRole("tab", { name: new RegExp(`^${tab}$`, "i") }),
    );
    const table = screen.getByRole("table");
    surfaces[`${tab} column`] = within(table).queryByRole("button", {
      name: "Sort by Account",
    });
    surfaces[`${tab} cell`] = within(table).queryByText(account);
    surfaces[`${tab} row filter`] = within(table).queryByRole("button", {
      name: `Filter by ${account}`,
    });
    surfaces[`${tab} field`] =
      screen.queryByPlaceholderText(/filter by account/i);
    surfaces[`${tab} global toggle`] = screen.queryByRole("button", {
      name: /global account filter/i,
    });
  }
  return surfaces;
}

// A fill the engine settled while blocking the order's account, as the order
// detail returns it.
function accountBlockingFill(order: Order): OrderEvent {
  return {
    id: "evt-block-1",
    order: order.id,
    at: "2026-06-24T11:08:00Z",
    type: "fill",
    source: "panel",
    principal: "",
    fillQuantity: "2",
    fillPrice: "99",
    rejectCode: "pnl_kill_switch_triggered",
    rejectScope: "account",
    rejectPolicy: "daily_loss",
    rejectReason: "Daily loss limit",
    signed: false,
  };
}

// Submit an execution report the engine answers with a block on the order's
// account; resolves once the accepted report is shown.
async function submitReportBlockingAccount(
  user: ReturnType<typeof userEvent.setup>,
  order: Order,
) {
  submitExecutionReportMock.mockResolvedValue({
    id: "report-alpha-1",
    blocks: [
      {
        account: order.account,
        code: "pnl_kill_switch_triggered",
        policy: "daily_loss",
        reason: "Daily loss limit",
        details: "",
      },
    ],
    outcomes: [],
    signedAttestation: "",
    attestationKeyId: "",
    signed: false,
  });
  await openSampleOrderDetail(user, {
    order: { ...order, price: "12", displayPrice: "12", leavesQuantity: "2" },
    events: [],
    trades: [],
  });
  await user.click(
    screen.getByRole("button", { name: "Submit execution report" }),
  );
  await user.click(
    screen.getByRole("button", { name: "Calculate leaves quantity" }),
  );
  await user.click(screen.getByRole("button", { name: "Submit" }));
  await screen.findByText("Execution report accepted.");
}

describe("Orders account surfaces with accounts shown", () => {
  it("shows the account column, row filter, filter field and global toggle on both tabs", async () => {
    const user = userEvent.setup();
    useOrdersMock.mockReturnValue(readyPage<Order>([sampleOrder]));
    useTradesMock.mockReturnValue(readyPage<Trade>([sampleTrade]));
    renderOrders("/orders");

    for (const [name, surface] of Object.entries(
      await accountSurfaces(user, sampleOrder.account),
    )) {
      expect(surface, name).not.toBeNull();
    }
  });

  it.each(["orders", "trades"])(
    "carries the %s account filter into suggestions and the share link",
    async (tab) => {
      renderOrders(`/orders?tab=${tab}&account=desk-alpha`);

      await waitFor(() =>
        expect(fetchAccountsMock).toHaveBeenCalledWith(
          expect.objectContaining({ code: "desk-alpha" }),
          expect.any(AbortSignal),
        ),
      );
      expect(shareLinkParams(filterSetShare("link")).get("account")).toBe(
        "desk-alpha",
      );
    },
  );

  it("names the account in the timeline block banner", async () => {
    const user = userEvent.setup();
    const dialog = await openSampleOrderDetail(user, {
      order: sampleOrder,
      events: [accountBlockingFill(sampleOrder)],
      trades: [],
    });

    expect(
      await within(dialog).findByText("Account desk-alpha blocked:"),
    ).toBeInTheDocument();
  });

  it("names the account in execution-report blocks", async () => {
    const user = userEvent.setup();
    await submitReportBlockingAccount(user, sampleOrder);

    expect(screen.getByText("Account desk-alpha blocked:")).toBeInTheDocument();
  });
});

describe("Orders page with accounts hidden", () => {
  const mainOrder: Order = { ...sampleOrder, account: "main" };
  const mainTrade: Trade = { ...sampleTrade, account: "main" };

  beforeEach(() => {
    setAccountScope({ accounts: false, account: "main" });
  });

  it("shows no account column, row filter, filter field or global toggle on either tab", async () => {
    const user = userEvent.setup();
    useOrdersMock.mockReturnValue(readyPage<Order>([mainOrder]));
    useTradesMock.mockReturnValue(readyPage<Trade>([mainTrade]));
    renderOrders("/orders");

    for (const [name, surface] of Object.entries(
      await accountSurfaces(user, mainOrder.account),
    )) {
      expect(surface, name).toBeNull();
    }
  });

  it.each([
    ["orders", /export orders csv/i],
    ["trades", /export trades csv/i],
  ] as const)(
    "keeps a stale account filter out of %s requests, export and share link",
    async (tab, exportButton) => {
      const user = userEvent.setup();
      setGlobalAccountFilter("desk-alpha");
      renderOrders(`/orders?tab=${tab}&account=desk-alpha`);

      const requested = [
        ...useOrdersMock.mock.calls.map(([filters]) => filters?.account),
        ...useTradesMock.mock.calls.map(([filters]) => filters?.account),
      ];
      expect(requested.length).toBeGreaterThan(0);
      expect(requested.filter((account) => account !== undefined)).toEqual([]);
      expect(screen.queryByText(/active filters/i)).not.toBeInTheDocument();
      expect(shareLinkParams(filterSetShare("link")).has("account")).toBe(
        false,
      );
      expect(fetchAccountsMock).not.toHaveBeenCalled();

      await user.click(screen.getByRole("button", { name: exportButton }));
      await user.click(screen.getByRole("button", { name: /^export$/i }));
      await waitFor(() =>
        expect(exportBusinessCsvMock).toHaveBeenCalledTimes(1),
      );
      expect(exportBusinessCsvMock).toHaveBeenCalledWith(
        expect.objectContaining({ entity: tab, filters: {} }),
      );
    },
  );

  it("shows the order detail without the account and links adjustments without it", async () => {
    const user = userEvent.setup();
    const dialog = await openSampleOrderDetail(user, {
      order: mainOrder,
      events: [],
      trades: [],
      venueOrder: linkedVenueOrder,
    });

    expect(
      await within(dialog).findByRole("link", { name: "View adjustments" }),
    ).toHaveAttribute("href", "/positions?tab=history&source=system");
    expect(within(dialog).queryByText("Account")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("main")).not.toBeInTheDocument();
  });

  it("names no account in the timeline block banner", async () => {
    const user = userEvent.setup();
    const dialog = await openSampleOrderDetail(user, {
      order: mainOrder,
      events: [accountBlockingFill(mainOrder)],
      trades: [],
    });

    expect(
      await within(dialog).findByText("Account blocked:"),
    ).toBeInTheDocument();
    expect(within(dialog).queryByText(/Account main/)).not.toBeInTheDocument();
  });

  it("names no account in execution-report blocks", async () => {
    const user = userEvent.setup();
    await submitReportBlockingAccount(user, mainOrder);

    expect(screen.getByText("Account blocked:")).toBeInTheDocument();
    expect(screen.queryByText(/Account main/)).not.toBeInTheDocument();
  });
});
