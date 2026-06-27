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

import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithApi as render } from "@/test/apiClient";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  MarketDataInstance,
  MarketDataStatus,
  MarketDataSymbolMatch,
} from "@/api/types";

import { SidebarProvider } from "@/components/SidebarContext";
import i18n from "@/i18n";
import {
  CreateInstanceDialog,
  ibSecTypeFields,
  ibSecTypePatch,
  InstanceCard,
  InstanceSettingsDialog,
  MarketData,
  ProviderGuideDialog,
} from "@/pages/MarketData";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

// Radix Select relies on pointer-capture and scrollIntoView, which jsdom does
// not implement; stub them so the secType/right dropdowns are operable.
beforeAll(() => {
  const proto = window.HTMLElement.prototype as unknown as Record<
    string,
    unknown
  >;
  proto.hasPointerCapture = () => false;
  proto.setPointerCapture = () => { };
  proto.releasePointerCapture = () => { };
  proto.scrollIntoView = () => { };
});

const fetchMarketDataMock = vi.fn();
const upsertMock = vi.fn();
const updateSettingsMock = vi.fn();
const deleteInstrumentMock = vi.fn();
const searchMock = vi.fn();

function renderMarketDataPage() {
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-officer-test-density"
          tradeStyleStorageKey="pit-officer-test-trade-style"
        >
          <SidebarProvider>
            <MarketData />
          </SidebarProvider>
        </DisplayPreferencesProvider>
      </ThemeProvider>
    </I18nextProvider>,
    {
      api: {
        deleteMarketDataInstrument: deleteInstrumentMock,
        fetchMarketData: fetchMarketDataMock,
        searchMarketDataSymbols: searchMock,
        updateMarketDataInstanceSettings: updateSettingsMock,
        upsertMarketDataInstrument: upsertMock,
      },
    },
  );
}

function ibInstance(
  overrides: Partial<MarketDataInstance> = {},
): MarketDataInstance {
  return {
    externalId: "ib-1",
    provider: "ib",
    label: "IB Gateway",
    credentials: "",
    settings: {
      host: "127.0.0.1",
      port: 7496,
      clientId: 7,
      marketDataType: "live",
    },
    enabled: true,
    state: "ok",
    verifiesSymbols: true,
    searchesSymbols: true,
    instruments: [],
    diagnostics: [],
    ...overrides,
  };
}

function status(instance: MarketDataInstance): MarketDataStatus {
  return {
    providers: [{ type: "ib", title: "Interactive Brokers" }],
    instances: [instance],
    freshnessSeconds: 10,
    restartRequired: false,
  };
}

function renderCard(
  instance: MarketDataInstance,
  handlers: Partial<
    Parameters<typeof InstanceCard>[0]
  > = {},
) {
  const props = {
    instance,
    pairUsageMap: {},
    busy: false,
    onEditSettings: vi.fn(),
    onVerifySymbol: vi.fn().mockResolvedValue(null),
    onSearchSymbols: vi.fn().mockResolvedValue([]),
    onToggleInstance: vi.fn(),
    onDeleteInstance: vi.fn(),
    onUpsertInstrument: vi.fn().mockResolvedValue(true),
    onUpsertIBInstrument: vi.fn().mockResolvedValue(true),
    onToggleInstrument: vi.fn(),
    onDeleteInstrument: vi.fn(),
    onDeleteIBInstrument: vi.fn(),
    onRestart: vi.fn(),
    ...handlers,
  };
  render(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter>
        <InstanceCard {...props} />
      </MemoryRouter>
    </I18nextProvider>,
  );
  return props;
}

beforeEach(async () => {
  vi.clearAllMocks();
  await i18n.changeLanguage("en");
  fetchMarketDataMock.mockResolvedValue(status(ibInstance()));
  upsertMock.mockResolvedValue(status(ibInstance()));
  updateSettingsMock.mockResolvedValue(status(ibInstance()));
  deleteInstrumentMock.mockResolvedValue(undefined);
  searchMock.mockResolvedValue({ supported: true, matches: [] });
});

describe("IB secType matrix (pure)", () => {
  it("maps each secType to its exchange default", () => {
    expect(ibSecTypePatch("STK")).toMatchObject({
      secType: "STK",
      exchange: "SMART",
    });
    expect(ibSecTypePatch("CASH")).toMatchObject({ exchange: "IDEALPRO" });
    expect(ibSecTypePatch("CRYPTO")).toMatchObject({ exchange: "PAXOS" });
  });

  it("clears the prior secType conditional keys on switch", () => {
    const patch = ibSecTypePatch("STK");
    expect(patch.lastTradeDateOrContractMonth).toBeUndefined();
    expect(patch.strike).toBeUndefined();
    expect(patch.right).toBeUndefined();
  });

  it("reveals expiry for FUT and strike+right for OPT", () => {
    expect(ibSecTypeFields("STK")).toMatchObject({
      lastTradeDate: false,
      strike: false,
      right: false,
    });
    expect(ibSecTypeFields("FUT")).toMatchObject({
      lastTradeDate: true,
      strike: false,
    });
    expect(ibSecTypeFields("OPT")).toMatchObject({
      lastTradeDate: true,
      strike: true,
      right: true,
    });
  });
});

describe("provider references", () => {
  it("links Alpaca symbols to the current assets documentation", () => {
    renderCard(
      ibInstance({
        externalId: "alpaca-1",
        provider: "alpaca",
        label: "Alpaca",
        searchesSymbols: false,
      }),
    );

    expect(
      screen.getByRole("link", { name: /valid symbols/i }),
    ).toHaveAttribute(
      "href",
      "https://docs.alpaca.markets/us/docs/working-with-assets",
    );
  });

  it("hides symbol verification buttons when the feed cannot verify", () => {
    renderCard(
      ibInstance({
        externalId: "alpaca-1",
        provider: "alpaca",
        label: "Alpaca",
        verifiesSymbols: false,
        searchesSymbols: false,
        instruments: [
          {
            instanceExternalId: "alpaca-1",
            externalSymbol: "AAPL",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            manualPrice: "",
            enabled: true,
            stale: false,
          },
        ],
      }),
    );

    expect(
      screen.queryByRole("button", { name: /verify symbol/i }),
    ).not.toBeInTheDocument();
  });

  it("keeps diagnostics for every provider in a bounded scroll panel", async () => {
    const user = userEvent.setup();
    renderCard(
      ibInstance({
        externalId: "binance-1",
        provider: "binance",
        label: "Binance",
        searchesSymbols: false,
        references: { docsUrl: "https://docs.example.com" },
        diagnostics: [
          {
            level: "error",
            code: "bad-symbol",
            kind: "config",
            title: "Bad symbol",
            detail: "BTCUSD is not valid for this feed.",
            actions: [{ type: "restart" }, { type: "open_docs" }],
            at: "2026-06-22T16:05:00Z",
          },
          {
            level: "error",
            code: "connect",
            kind: "provider",
            title: "Connection error",
            detail: "The provider is unreachable.",
            actions: [{ type: "restart" }, { type: "open_docs" }],
            at: "2026-06-22T16:06:00Z",
          },
        ],
      }),
    );

    const panel = screen.getByRole("region", { name: "Problems" });
    expect(panel).toHaveClass("max-h-96", "overflow-y-auto", "pr-1");
    expect(within(panel).getByText("Action needed")).toBeInTheDocument();
    expect(
      within(panel).getByText("Connection / provider"),
    ).toBeInTheDocument();
    expect(
      within(panel).queryByRole("button", { name: "Restart feeds" }),
    ).not.toBeInTheDocument();
    const actionBar = screen.getByRole("toolbar", {
      name: "Problem actions",
    });
    expect(
      within(actionBar).getAllByRole("button", { name: "Restart feeds" }),
    ).toHaveLength(1);
    expect(
      within(actionBar)
        .getAllByRole("link", { name: "Provider docs" })
        .filter(
          (link) => link.getAttribute("href") === "https://docs.example.com",
        ),
    ).toHaveLength(1);
    expect(
      within(actionBar).getAllByRole("link", { name: "Service logs" }),
    ).toHaveLength(1);

    await user.click(screen.getByRole("button", { name: /collapse/i }));

    expect(
      screen.queryByRole("region", { name: "Problems" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Problems: 2/i }),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /Problems: 2/i }));

    expect(screen.getByRole("region", { name: "Problems" })).toHaveClass(
      "max-h-96",
      "overflow-y-auto",
    );
  });

  it("shows IB connection settings without the advanced block when creating a source", () => {
    render(
      <I18nextProvider i18n={i18n}>
        <CreateInstanceDialog
          provider={{ type: "ib", title: "Interactive Brokers" }}
          existingLabels={[]}
          busy={false}
          onOpenChange={vi.fn()}
          onCreate={vi.fn().mockResolvedValue(true)}
        />
      </I18nextProvider>,
    );

    expect(screen.getByRole("dialog")).toHaveClass(
      "max-h-[90vh]",
      "max-w-[40rem]",
      "overflow-y-auto",
    );
    expect(screen.getByLabelText("Host")).toBeInTheDocument();
    expect(screen.queryByText("Advanced")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Generic ticks")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Contract symbol")).not.toBeInTheDocument();
  });

  it("shows structured instrument metadata and duplicate pair warnings", () => {
    renderCard(
      ibInstance({
        instruments: [
          {
            instanceExternalId: "ib-1",
            externalSymbol: "AAPL",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            manualPrice: "",
            enabled: true,
            stale: false,
          },
        ],
        settings: {
          contracts: {
            AAPL: {
              symbol: "AAPL",
              secType: "STK",
              exchange: "SMART",
              primaryExchange: "NASDAQ",
              currency: "USD",
              conId: "265598",
            },
          },
        },
      }),
      {
        pairUsageMap: {
          "AAPL/USD": [
            {
              instanceExternalId: "alpaca-1",
              instanceLabel: "Alpaca",
              providerType: "alpaca",
              externalSymbol: "AAPL",
            },
          ],
        },
      },
    );

    expect(screen.getByText(/STK · SMART\/NASDAQ · USD/)).toHaveTextContent(
      "conId 265598",
    );
    expect(
      screen.getByLabelText(/already used by Alpaca/i),
    ).toBeInTheDocument();
  });
});

describe("IB feed resolver", () => {
  async function openIBAddDialog(user: ReturnType<typeof userEvent.setup>) {
    await user.click(screen.getByRole("button", { name: /add instrument/i }));
    return screen.findByRole("dialog");
  }

  it("keeps IB contract fields in the custom add dialog", async () => {
    const user = userEvent.setup();
    renderCard(ibInstance());

    expect(screen.queryByLabelText("Security type")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Exchange")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Currency")).not.toBeInTheDocument();

    const dialog = await openIBAddDialog(user);
    expect(within(dialog).getByLabelText("Security type")).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Exchange")).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Currency")).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Con ID")).toBeInTheDocument();
    expect(within(dialog).getByLabelText("State")).toHaveClass(
      "h-[var(--dens-field-h)]",
    );
  });

  it("resolves with the typed query and maps the contract on a STK pick", async () => {
    const user = userEvent.setup();
    const match: MarketDataSymbolMatch = {
      symbol: "AAPL",
      secType: "STK",
      exchange: "SMART",
      primaryExchange: "NASDAQ",
      currency: "USD",
      conId: "265598",
    };
    const onUpsertIBInstrument = vi.fn().mockResolvedValue(true);
    const onSearchSymbols = vi.fn().mockResolvedValue([match]);
    renderCard(ibInstance(), { onSearchSymbols, onUpsertIBInstrument });
    const dialog = await openIBAddDialog(user);

    await user.type(within(dialog).getByLabelText("Base"), "OLD");
    await user.type(
      within(dialog).getByLabelText("Resolve symbol in this feed"),
      "apple",
    );
    await user.click(within(dialog).getByRole("button", { name: /^resolve$/i }));

    // The resolve stays scoped to this feed and sends only the typed query.
    expect(onSearchSymbols).toHaveBeenCalledTimes(1);
    const [, input] = onSearchSymbols.mock.calls[0];
    expect(input).toEqual({ query: "apple" });

    await user.click(await within(dialog).findByRole("button", { name: /AAPL/ }));

    // External symbol and quote pick up the resolved contract.
    expect(within(dialog).getByLabelText("External symbol")).toHaveValue("AAPL");
    expect(within(dialog).getByLabelText("Base")).toHaveValue("AAPL");
    expect(within(dialog).getByLabelText("Quote")).toHaveValue("USD");
    expect(within(dialog).getByLabelText("Contract symbol")).toHaveValue("AAPL");
    expect(within(dialog).getByLabelText("Con ID")).toHaveValue("265598");
    expect(within(dialog).getByLabelText("Primary exchange")).toHaveValue(
      "NASDAQ",
    );

    // Adding hands the structured contract (incl. conId/exchange) to the
    // page handler.
    await user.click(
      within(dialog).getByRole("button", { name: /add instrument/i }),
    );
    await waitFor(() => {
      expect(onUpsertIBInstrument).toHaveBeenCalledTimes(1);
    });
    const [, draft, contract] = onUpsertIBInstrument.mock.calls[0];
    expect(draft.externalSymbol).toBe("AAPL");
    expect(contract).toMatchObject({
      symbol: "AAPL",
      secType: "STK",
      exchange: "SMART",
      primaryExchange: "NASDAQ",
      currency: "USD",
      conId: "265598",
    });
  });

  it("resolves crypto with exchange and currency from the selected match", async () => {
    const user = userEvent.setup();
    const match: MarketDataSymbolMatch = {
      symbol: "BTC",
      secType: "CRYPTO",
      exchange: "PAXOS",
      currency: "USD",
      conId: "12345",
    };
    const onSearchSymbols = vi.fn().mockResolvedValue([match]);
    renderCard(ibInstance(), { onSearchSymbols });
    const dialog = await openIBAddDialog(user);

    await user.type(
      within(dialog).getByLabelText("Resolve symbol in this feed"),
      "BTC",
    );
    await user.click(within(dialog).getByRole("button", { name: /^resolve$/i }));

    expect(onSearchSymbols).toHaveBeenCalledTimes(1);
    const [, input] = onSearchSymbols.mock.calls[0];
    expect(input).toEqual({ query: "BTC" });

    // The resolved listing exchange shows in the result row.
    await user.click(await within(dialog).findByRole("button", { name: /PAXOS/ }));
    expect(within(dialog).getByLabelText("External symbol")).toHaveValue("BTC");
    expect(within(dialog).getByLabelText("Base")).toHaveValue("BTC");
    expect(within(dialog).getByLabelText("Quote")).toHaveValue("USD");
    expect(within(dialog).getByLabelText("Exchange")).toHaveValue("PAXOS");
  });

  it("maps conId, exchange and expiry on a FUT pick", async () => {
    const user = userEvent.setup();
    const match: MarketDataSymbolMatch = {
      symbol: "ES",
      secType: "FUT",
      exchange: "CME",
      currency: "USD",
      conId: "495512563",
      lastTradeDateOrContractMonth: "20251219",
      multiplier: "50",
    };
    const onUpsertIBInstrument = vi.fn().mockResolvedValue(true);
    const onSearchSymbols = vi.fn().mockResolvedValue([match]);
    renderCard(ibInstance(), { onSearchSymbols, onUpsertIBInstrument });
    const dialog = await openIBAddDialog(user);

    await user.type(
      within(dialog).getByLabelText("Resolve symbol in this feed"),
      "es",
    );
    await user.click(within(dialog).getByRole("button", { name: /^resolve$/i }));

    const [, input] = onSearchSymbols.mock.calls[0];
    expect(input).toEqual({ query: "es" });

    await user.click(await within(dialog).findByRole("button", { name: /ES/ }));

    await user.click(
      within(dialog).getByRole("button", { name: /add instrument/i }),
    );
    await waitFor(() => {
      expect(onUpsertIBInstrument).toHaveBeenCalledTimes(1);
    });
    const [, , contract] = onUpsertIBInstrument.mock.calls[0];
    expect(contract).toMatchObject({
      symbol: "ES",
      secType: "FUT",
      exchange: "CME",
      currency: "USD",
      conId: "495512563",
      lastTradeDateOrContractMonth: "20251219",
      multiplier: "50",
    });
  });

  it("resolves a non-IB feed without leaving the feed context", async () => {
    const user = userEvent.setup();
    const match: MarketDataSymbolMatch = {
      symbol: "BTCUSDT",
      secType: "SPOT",
    };
    const onSearchSymbols = vi.fn().mockResolvedValue([match]);
    const onUpsertInstrument = vi.fn().mockResolvedValue(true);
    renderCard(
      ibInstance({
        externalId: "bn-1",
        provider: "binance",
        label: "Binance",
        searchesSymbols: true,
      }),
      { onSearchSymbols, onUpsertInstrument },
    );

    await user.type(
      screen.getByLabelText("Resolve symbol in this feed"),
      "BTC/USDT",
    );
    await user.click(screen.getByRole("button", { name: /^resolve$/i }));

    expect(onSearchSymbols).toHaveBeenCalledTimes(1);
    const [searchedInstance, input] = onSearchSymbols.mock.calls[0];
    expect(searchedInstance.externalId).toBe("bn-1");
    expect(input).toMatchObject({ query: "BTC/USDT" });

    const result = await screen.findByRole("button", { name: /BTCUSDT/ });
    expect(result).toHaveTextContent("BTCUSDT · SPOT");
    expect(result).not.toHaveTextContent("—");
    await user.click(result);

    expect(screen.getByPlaceholderText("External symbol")).toHaveValue(
      "BTCUSDT",
    );
    expect(screen.getByPlaceholderText("Base")).toHaveValue("BTC");
    expect(screen.getByPlaceholderText("Quote")).toHaveValue("USDT");

    await user.click(screen.getByRole("button", { name: /add instrument/i }));
    await waitFor(() => {
      expect(onUpsertInstrument).toHaveBeenCalledTimes(1);
    });
    const [, draft] = onUpsertInstrument.mock.calls[0];
    expect(draft).toMatchObject({
      externalSymbol: "BTCUSDT",
      baseAsset: "BTC",
      quoteAsset: "USDT",
    });
  });

  it("filters resolved results locally and keeps the result list scrollable", async () => {
    const user = userEvent.setup();
    const onSearchSymbols = vi.fn().mockResolvedValue([
      { symbol: "BTCUSDT", secType: "SPOT", name: "Bitcoin Tether" },
      { symbol: "ETHUSDT", secType: "SPOT", name: "Ether Tether" },
      { symbol: "SOLUSDT", secType: "SPOT", name: "Solana Tether" },
      { symbol: "AAPL", secType: "STK", name: "Apple Inc" },
      { symbol: "EURUSD", secType: "CASH", name: "Euro US Dollar" },
      { symbol: "BTCEUR", secType: "SPOT", name: "Bitcoin Euro" },
    ]);
    renderCard(
      ibInstance({
        externalId: "bn-1",
        provider: "binance",
        label: "Binance",
        searchesSymbols: true,
      }),
      { onSearchSymbols },
    );

    await user.type(
      screen.getByLabelText("Resolve symbol in this feed"),
      "USDT",
    );
    await user.click(screen.getByRole("button", { name: /^resolve$/i }));

    const list = await screen.findByRole("list");
    expect(list).toHaveClass("max-h-52", "overflow-y-auto");
    expect(within(list).getByText(/BTCUSDT/)).toBeInTheDocument();
    expect(within(list).getByText(/ETHUSDT/)).toBeInTheDocument();

    await user.type(screen.getByLabelText("Filter resolved symbols"), "btc");

    expect(onSearchSymbols).toHaveBeenCalledTimes(1);
    expect(within(list).getByText(/BTCUSDT/)).toBeInTheDocument();
    expect(within(list).getByText(/BTCEUR/)).toBeInTheDocument();
    expect(within(list).queryByText(/ETHUSDT/)).not.toBeInTheDocument();
    expect(within(list).queryByText(/AAPL/)).not.toBeInTheDocument();
  });

  it("derives base and quote from a compact resolved symbol", async () => {
    const user = userEvent.setup();
    const match: MarketDataSymbolMatch = {
      symbol: "AEETH",
      secType: "SPOT",
    };
    const onSearchSymbols = vi.fn().mockResolvedValue([match]);
    const onUpsertInstrument = vi.fn().mockResolvedValue(true);
    renderCard(
      ibInstance({
        externalId: "bn-1",
        provider: "binance",
        label: "Binance",
        searchesSymbols: true,
      }),
      { onSearchSymbols, onUpsertInstrument },
    );

    await user.type(screen.getByPlaceholderText("Base"), "ETHEUR");
    await user.type(screen.getByPlaceholderText("Quote"), "USD");
    await user.type(screen.getByLabelText("Resolve symbol in this feed"), "ETH");
    await user.click(screen.getByRole("button", { name: /^resolve$/i }));
    await user.click(await screen.findByRole("button", { name: /AEETH/ }));

    expect(screen.getByPlaceholderText("External symbol")).toHaveValue("AEETH");
    expect(screen.getByPlaceholderText("Base")).toHaveValue("AE");
    expect(screen.getByPlaceholderText("Quote")).toHaveValue("ETH");

    await user.click(screen.getByRole("button", { name: /add instrument/i }));
    await waitFor(() => {
      expect(onUpsertInstrument).toHaveBeenCalledTimes(1);
    });
    const [, draft] = onUpsertInstrument.mock.calls[0];
    expect(draft).toMatchObject({
      externalSymbol: "AEETH",
      baseAsset: "AE",
      quoteAsset: "ETH",
    });
  });

  it("fills the qualified symbol and venue-free pair from an exchange-prefixed match", async () => {
    const user = userEvent.setup();
    const match: MarketDataSymbolMatch = {
      symbol: "BINANCE:ETHUSDT",
      secType: "Crypto",
      exchange: "BINANCE",
    };
    const onSearchSymbols = vi.fn().mockResolvedValue([match]);
    renderCard(
      ibInstance({
        externalId: "fh-1",
        provider: "finnhub",
        label: "Finnhub",
        searchesSymbols: true,
      }),
      { onSearchSymbols },
    );

    await user.type(
      screen.getByLabelText("Resolve symbol in this feed"),
      "ETH",
    );
    await user.click(screen.getByRole("button", { name: /^resolve$/i }));

    // The candidate row shows the pair as the main ticker with the venue moved
    // to the grey metadata; the exchange prefix is not shown inline.
    const result = await screen.findByRole("button", { name: /ETHUSDT/ });
    expect(result).toHaveTextContent("Crypto · BINANCE");
    expect(result).not.toHaveTextContent("BINANCE:ETHUSDT");
    await user.click(result);

    // The fully-qualified symbol is kept; base/quote drop the venue prefix.
    expect(screen.getByPlaceholderText("External symbol")).toHaveValue(
      "BINANCE:ETHUSDT",
    );
    expect(screen.getByPlaceholderText("Base")).toHaveValue("ETH");
    expect(screen.getByPlaceholderText("Quote")).toHaveValue("USDT");
  });

  it("shows the source venue under an exchange-qualified instrument row", () => {
    renderCard(
      ibInstance({
        externalId: "fh-1",
        provider: "finnhub",
        label: "Finnhub",
        instruments: [
          {
            instanceExternalId: "fh-1",
            externalSymbol: "BINANCE:ETHUSDT",
            baseAsset: "ETH",
            quoteAsset: "USDT",
            manualPrice: "",
            enabled: true,
            stale: false,
          },
        ],
      }),
    );

    // The main ticker drops the venue prefix; the venue shows as grey metadata.
    const row = screen.getByText("ETHUSDT").closest("tr") as HTMLElement;
    expect(within(row).queryByText("BINANCE:ETHUSDT")).not.toBeInTheDocument();
    expect(within(row).getByText("BINANCE")).toBeInTheDocument();
  });

  it("hides the resolver action when the provider can't search", async () => {
    const user = userEvent.setup();
    renderCard(ibInstance({ searchesSymbols: false }));
    await user.click(screen.getByRole("button", { name: /add instrument/i }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).queryByLabelText("Resolve symbol in this feed"),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", { name: /^resolve$/i }),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).queryByText(/can't resolve symbols/i),
    ).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText("External symbol")).toBeInTheDocument();
  });
});

describe("IB instrument persistence (full-map send)", () => {
  async function waitForCard() {
    await screen.findByText("IB Gateway");
  }

  it("issues instrument upsert then a settings PUT carrying the full prior contracts map plus the new key", async () => {
    const user = userEvent.setup();
    const instance = ibInstance({
      instruments: [
        {
          instanceExternalId: "ib-1",
          externalSymbol: "MSFT",
          baseAsset: "MSFT",
          quoteAsset: "USD",
          manualPrice: "",
          enabled: true,
          stale: false,
        },
      ],
      settings: {
        host: "127.0.0.1",
        port: 7496,
        clientId: 7,
        marketDataType: "live",
        contracts: {
          MSFT: { symbol: "MSFT", secType: "STK", exchange: "SMART" },
        },
      },
    });
    fetchMarketDataMock.mockResolvedValue(status(instance));

    renderMarketDataPage();
    await waitForCard();

    await user.click(screen.getByRole("button", { name: /add instrument/i }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText("External symbol"), "AAPL");
    await user.click(
      within(dialog).getByRole("button", { name: /add instrument/i }),
    );

    await waitFor(() => {
      expect(upsertMock).toHaveBeenCalledTimes(1);
    });
    expect(updateSettingsMock).toHaveBeenCalledTimes(1);

    // Instrument upsert runs first, then the settings PUT.
    expect(upsertMock.mock.invocationCallOrder[0]).toBeLessThan(
      updateSettingsMock.mock.invocationCallOrder[0],
    );

    const [, form] = updateSettingsMock.mock.calls[0];
    const credentials = JSON.parse(form.credentials) as {
      contracts: Record<string, unknown>;
      marketDataType?: string;
    };
    // The PUT carries the FULL contracts map: the prior MSFT entry plus AAPL.
    expect(Object.keys(credentials.contracts).sort()).toEqual(["AAPL", "MSFT"]);
    expect(credentials.contracts.MSFT).toMatchObject({ symbol: "MSFT" });
    expect(credentials.contracts.AAPL).toMatchObject({
      symbol: "AAPL",
      secType: "STK",
      exchange: "SMART",
      currency: "USD",
    });
    // Existing scalar settings are carried over, not blanked.
    expect(credentials.marketDataType).toBe("live");
  });

  it("deletes an IB instrument and PUTs settings with the removed key omitted", async () => {
    const user = userEvent.setup();
    const instance = ibInstance({
      instruments: [
        {
          instanceExternalId: "ib-1",
          externalSymbol: "MSFT",
          baseAsset: "MSFT",
          quoteAsset: "USD",
          manualPrice: "",
          enabled: true,
          stale: false,
        },
        {
          instanceExternalId: "ib-1",
          externalSymbol: "AAPL",
          baseAsset: "AAPL",
          quoteAsset: "USD",
          manualPrice: "",
          enabled: true,
          stale: false,
        },
      ],
      settings: {
        host: "127.0.0.1",
        port: 7496,
        clientId: 7,
        marketDataType: "live",
        contracts: {
          MSFT: { symbol: "MSFT", secType: "STK", exchange: "SMART" },
          AAPL: { symbol: "AAPL", secType: "STK", exchange: "SMART" },
        },
      },
    });
    fetchMarketDataMock.mockResolvedValue(status(instance));

    renderMarketDataPage();
    await waitForCard();

    const aaplRow = screen.getByText("AAPL").closest("tr") as HTMLElement;
    await user.click(
      within(aaplRow).getByRole("button", { name: /delete instrument/i }),
    );
    await user.click(await screen.findByRole("button", { name: /^delete$/i }));

    await waitFor(() => {
      expect(deleteInstrumentMock).toHaveBeenCalledWith("ib-1", "AAPL");
    });
    expect(updateSettingsMock).toHaveBeenCalledTimes(1);
    const [, form] = updateSettingsMock.mock.calls[0];
    const credentials = JSON.parse(form.credentials) as {
      contracts: Record<string, unknown>;
    };
    // Full map minus the removed key: MSFT survives, AAPL is gone.
    expect(Object.keys(credentials.contracts)).toEqual(["MSFT"]);
  });
});

describe("IB advanced section removed", () => {
  it("omits the Advanced generic-ticks and default-contract block from settings", () => {
    render(
      <I18nextProvider i18n={i18n}>
        <InstanceSettingsDialog
          instance={ibInstance()}
          busy={false}
          existingLabels={[]}
          onOpenChange={vi.fn()}
          onSave={vi.fn().mockResolvedValue(true)}
        />
      </I18nextProvider>,
    );
    expect(screen.getByRole("dialog")).toHaveClass(
      "max-h-[90vh]",
      "max-w-[40rem]",
      "overflow-y-auto",
    );
    expect(screen.queryByText("Advanced")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Generic ticks")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Contract symbol")).not.toBeInTheDocument();
  });

  it("does not render generic ticks in the IB instrument add editor", async () => {
    const user = userEvent.setup();
    renderCard(ibInstance());
    await user.click(screen.getByRole("button", { name: /add instrument/i }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).queryByLabelText("Generic ticks"),
    ).not.toBeInTheDocument();
  });
});

describe("stale quote rendering", () => {
  it("shows the price and stale badge when an instrument is stale with a quote", () => {
    renderCard(
      ibInstance({
        instruments: [
          {
            instanceExternalId: "ib-1",
            externalSymbol: "AAPL",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            manualPrice: "",
            enabled: true,
            stale: true,
            updateIntervalMs: 30_000,
            quote: {
              asOf: new Date(Date.now() - 30_000).toISOString(),
              receivedAt: new Date().toISOString(),
              mark: "298.01",
              bid: "297.99",
              ask: "298.03",
            },
          },
        ],
      }),
    );

    // Price must be visible, not "—".
    const row = screen.getByText("AAPL").closest("tr") as HTMLElement;
    expect(within(row).getByText("298.01")).toBeInTheDocument();

    // Exactly one stale badge in the row (in the State cell).
    const staleBadges = within(row).getAllByText(/^stale$/i);
    expect(staleBadges).toHaveLength(1);

    // Relative age should also be present.
    expect(within(row).getByText(/^-\d+s$/)).toBeInTheDocument();
  });

  it("shows the relative age next to the last-updated timestamp", () => {
    const asOf = new Date(Date.now() - 30_000).toISOString();
    renderCard(
      ibInstance({
        instruments: [
          {
            instanceExternalId: "ib-1",
            externalSymbol: "AAPL",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            manualPrice: "",
            enabled: true,
            stale: true,
            updateIntervalMs: 30_000,
            quote: {
              asOf,
              receivedAt: new Date().toISOString(),
              mark: "298.01",
              bid: "297.99",
              ask: "298.03",
            },
          },
        ],
      }),
    );

    const row = screen.getByText("AAPL").closest("tr") as HTMLElement;
    // Relative age in seconds should be visible (format: -Ns).
    expect(within(row).getByText(/^-\d+s$/)).toBeInTheDocument();
  });
});

describe("provider guide", () => {
  it("renders the provider tiles behind the dialog and selects one", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <I18nextProvider i18n={i18n}>
        <ProviderGuideDialog
          open
          providers={[
            { type: "finnhub", title: "Finnhub" },
            { type: "binance", title: "Binance" },
          ]}
          onSelect={onSelect}
          onOpenChange={vi.fn()}
        />
      </I18nextProvider>,
    );

    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Finnhub")).toBeInTheDocument();
    expect(within(dialog).getByText("Binance")).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: /Finnhub/ }));
    expect(onSelect).toHaveBeenCalledWith({
      type: "finnhub",
      title: "Finnhub",
    });
  });
});
