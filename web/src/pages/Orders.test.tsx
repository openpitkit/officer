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

import type { PollingResult } from "@/api/usePolling";
import type { Balance, Order, OrderEvent, Trade } from "@/api/types";
import { SidebarProvider } from "@/components/SidebarContext";
import i18n from "@/i18n";
import { Orders } from "@/pages/Orders";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

// Replace the polling hooks with ready-empty stubs and the one-shot account
// fetch so the page renders without any network traffic.
vi.mock("@/api/useOrders", () => ({ useOrders: vi.fn() }));
vi.mock("@/api/useTrades", () => ({ useTrades: vi.fn() }));
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
vi.mock("@/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/api/client")>("@/api/client");
  return {
    ...actual,
    fetchAccounts: vi.fn().mockResolvedValue([]),
    fetchAccountState: vi.fn(),
    checkOrder: vi.fn(),
    createOrder: vi.fn(),
    fetchOrderDetail: vi.fn(),
  };
});

import {
  checkOrder,
  createOrder,
  fetchOrderDetail,
} from "@/api/client";
import { useBalances } from "@/api/useBalances";
import { useOrders } from "@/api/useOrders";
import { useTrades } from "@/api/useTrades";

function readyEmpty<T>(data: T): PollingResult<T> {
  return { load: { state: "ready", data, error: null }, reload: vi.fn() };
}

const useOrdersMock = vi.mocked(useOrders);
const useTradesMock = vi.mocked(useTrades);
const useBalancesMock = vi.mocked(useBalances);
const checkOrderMock = vi.mocked(checkOrder);
const createOrderMock = vi.mocked(createOrder);
const fetchOrderDetailMock = vi.mocked(fetchOrderDetail);

const sampleOrder: Order = {
  id: 1,
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
  lockPrices: [],
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
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  await i18n.changeLanguage("en");
  useOrdersMock.mockReturnValue(readyEmpty<Order[]>([]));
  useTradesMock.mockReturnValue(readyEmpty<Trade[]>([]));
  useBalancesMock.mockReturnValue(readyEmpty<Balance[]>([]));
  checkOrderMock.mockResolvedValue({
    passed: true,
    rejects: [],
    wouldLockPrices: [],
    wouldBlock: null,
  });
  createOrderMock.mockResolvedValue(sampleOrder);
  fetchOrderDetailMock.mockResolvedValue({ order: sampleOrder, events: [], trades: [] });
});

describe("Orders account pre-fill", () => {
  it("seeds the account filter from the ?account= query param", async () => {
    renderOrders("/orders?account=desk-alpha");
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/filter by account/i)).toHaveValue(
        "desk-alpha",
      ),
    );
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
    await user.clear(filter);
    await waitFor(() => expect(filter).toHaveValue(""));

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
  });
});

describe("Order detail account block placement", () => {
  it("shows the account block reason beside the fill event that caused it", async () => {
    const user = userEvent.setup();
    const order = { ...sampleOrder, id: 3, status: "filled", price: "99" };
    const events: OrderEvent[] = [
      {
        id: 1,
        orderId: 3,
        at: "2026-06-24T11:07:00Z",
        type: "submitted",
        source: "panel",
        principal: "",
      },
      {
        id: 2,
        orderId: 3,
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

    useOrdersMock.mockReturnValue(readyEmpty<Order[]>([order]));
    fetchOrderDetailMock.mockResolvedValueOnce({
      order,
      events,
      trades: [],
    });
    renderOrders("/orders");

    await user.click(screen.getByText("#3"));

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
