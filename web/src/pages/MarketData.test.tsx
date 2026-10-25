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

import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithApi as render } from "@/test/apiClient";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  CredentialStorage,
  MarketDataInstance,
  MarketDataStatus,
  MarketDataSymbolMatch,
} from "@/api/types";
import { fetchCredentialStorage } from "@/api/credentialStorage";
import type { LoadState } from "@/api/usePolling";

import { SidebarProvider } from "@/components/SidebarContext";
import { ApiError } from "@/framework";
import i18n from "@/i18n";
import {
  CreateInstanceDialog,
  ibSecTypeFields,
  ibSecTypePatch,
  InstanceCard,
  InstanceSettingsDialog,
  MarketData,
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
  proto.setPointerCapture = () => {};
  proto.releasePointerCapture = () => {};
  proto.scrollIntoView = () => {};
});

const fetchMarketDataMock = vi.fn();
const upsertMock = vi.fn();
const updateSettingsMock = vi.fn();
const deleteInstrumentMock = vi.fn();
const deleteInstanceMock = vi.fn();
const searchMock = vi.fn();
vi.mock("@/api/credentialStorage");
const fetchCredentialStorageMock = vi.mocked(fetchCredentialStorage);
const createInstanceMock = vi.fn();

function storage(sealed: boolean): CredentialStorage {
  return {
    credentialsSealed: sealed,
    plaintextTradingKeys: false,
    plaintextMarketDataKeys: !sealed,
    sealedAt: null,
    resealedSecrets: 0,
  };
}

// Direct dialog renders take the page's credential storage load as a prop.
const sealedStorageLoad: LoadState<CredentialStorage> = {
  state: "ready",
  data: storage(true),
  error: null,
};

function renderMarketDataPage() {
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-test-density"
          tradeStyleStorageKey="pit-test-style"
        >
          <SidebarProvider>
            <MemoryRouter>
              <MarketData />
            </MemoryRouter>
          </SidebarProvider>
        </DisplayPreferencesProvider>
      </ThemeProvider>
    </I18nextProvider>,
    {
      api: {
        createMarketDataInstance: createInstanceMock,
        deleteMarketDataInstance: deleteInstanceMock,
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
    id: "ib-1",
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
  handlers: Partial<Parameters<typeof InstanceCard>[0]> = {},
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
  deleteInstanceMock.mockResolvedValue(undefined);
  searchMock.mockResolvedValue({ supported: true, matches: [] });
  fetchCredentialStorageMock.mockResolvedValue(storage(true));
  createInstanceMock.mockResolvedValue(ibInstance());
});

describe("MD instrument block texts", () => {
  const instrument = {
    instanceId: "bn-1",
    externalSymbol: "BTCUSDT",
    baseAsset: "BTC",
    quoteAsset: "USDT",
    manualPrice: "",
    enabled: true,
    syntheticInverse: false,
    stale: false,
  };
  const instance = ibInstance({
    id: "bn-1",
    provider: "binance",
    label: "Binance",
    instruments: [instrument],
  });
  const unavailable = {
    supported: true,
    exists: false,
    details: "Quote expired",
  };

  it("uses the MD unavailable wording for the draft verdict", async () => {
    const user = userEvent.setup();
    const onVerifySymbol = vi.fn().mockResolvedValue(unavailable);
    renderCard({ ...instance, instruments: [] }, { onVerifySymbol });

    await user.type(screen.getByPlaceholderText("External symbol"), "BTCUSDT");
    await user.click(screen.getByRole("button", { name: "Verify symbol" }));

    expect(
      await screen.findByText("No fresh price for symbol"),
    ).toBeInTheDocument();
    expect(onVerifySymbol).toHaveBeenCalledWith(
      { ...instance, instruments: [] },
      "BTCUSDT",
    );
  });

  it("uses the MD unavailable wording for a table-row verdict", async () => {
    const user = userEvent.setup();
    const onVerifySymbol = vi.fn().mockResolvedValue(unavailable);
    renderCard(instance, { onVerifySymbol });
    const row = screen.getByText("BTCUSDT").closest("tr")!;

    await user.click(
      within(row).getByRole("button", { name: "Verify symbol" }),
    );

    expect(
      await within(row).findByText("No fresh price for symbol"),
    ).toBeInTheDocument();
    expect(onVerifySymbol).toHaveBeenCalledWith(instance, "BTCUSDT");
  });

  it("names the disabled feed on an enabled instrument row", () => {
    renderCard({ ...instance, enabled: false });
    const row = screen.getByText("BTCUSDT").closest("tr")!;

    expect(within(row).getByText("Feed disabled")).toBeInTheDocument();
  });

  it("names the symbol and source in the instrument delete description", async () => {
    const user = userEvent.setup();
    fetchMarketDataMock.mockResolvedValue(status(instance));
    renderMarketDataPage();
    const row = (await screen.findByText("BTCUSDT")).closest("tr")!;

    await user.click(
      within(row).getByRole("button", { name: "Delete instrument" }),
    );

    const dialog = await screen.findByRole("alertdialog", {
      name: "Delete instrument?",
    });
    expect(
      within(dialog).getByText(
        "BTCUSDT will be permanently removed from this source.",
      ),
    ).toBeInTheDocument();
  });

  it.each(["binance", "ib"])(
    "uses the MD empty resolve text for %s",
    async (provider) => {
      const user = userEvent.setup();
      const onSearchSymbols = vi.fn().mockResolvedValue([]);
      renderCard(ibInstance({ provider }), { onSearchSymbols });
      if (provider === "ib") {
        await user.click(
          screen.getByRole("button", { name: "Add instrument" }),
        );
      }

      await user.type(
        screen.getByLabelText("Resolve symbol in this feed"),
        "UNKNOWN",
      );
      await user.click(screen.getByRole("button", { name: "Resolve" }));

      expect(
        await screen.findByText("No matching symbols in this feed"),
      ).toBeInTheDocument();
      expect(onSearchSymbols).toHaveBeenCalledTimes(1);
    },
  );
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
    expect(patch.currency).toBeUndefined();
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
  it("opens the provider guide and selects a translated provider to create", async () => {
    const user = userEvent.setup();
    fetchMarketDataMock.mockResolvedValue({
      ...status(ibInstance()),
      providers: [
        { type: "ib", title: "Interactive Brokers" },
        { type: "alpaca", title: "API Alpaca title" },
      ],
    });
    renderMarketDataPage();
    await screen.findByRole("heading", { name: "IB Gateway" });

    const add = screen.getByRole("button", {
      name: /Add a market-data source/,
    });
    expect(within(add).getByText("2 providers")).toBeInTheDocument();
    expect(add.querySelectorAll("span.ml-1 > svg")).toHaveLength(2);
    const providerIcons = add.querySelectorAll("span.ml-1 > svg");
    expect(providerIcons[0].querySelector("rect")).toHaveAttribute(
      "fill",
      "#050608",
    );
    expect(providerIcons[1].querySelector("rect")).toHaveAttribute(
      "fill",
      "#ffd200",
    );
    await user.click(add);
    const guide = screen.getByRole("dialog", { name: "Sources" });
    expect(guide).toHaveAccessibleDescription("Provider guide");
    const tile = within(guide).getByRole("button", { name: /^Alpaca/ });
    expect(within(tile).getByText("Alpaca")).toBeInTheDocument();
    expect(
      within(tile).getByText(
        "Live US equity prices from Alpaca IEX. Requires API key credentials; external symbols use stock tickers such as AAPL.",
      ),
    ).toBeInTheDocument();
    const card = tile.parentElement!;
    for (const [name, href] of [
      ["Site", "https://alpaca.markets"],
      ["Docs", "https://docs.alpaca.markets/docs/real-time-stock-pricing-data"],
      ["Symbols", "https://docs.alpaca.markets/us/docs/working-with-assets"],
    ]) {
      expect(within(card).getByRole("link", { name })).toHaveAttribute(
        "href",
        href,
      );
    }

    await user.click(tile);
    expect(
      screen.queryByRole("dialog", { name: "Sources" }),
    ).not.toBeInTheDocument();
    const create = screen.getByRole("dialog", { name: "Add Alpaca source" });
    expect(within(create).getByLabelText("Label")).toHaveValue("Alpaca");
  });

  it("links Alpaca symbols to the current assets documentation", () => {
    renderCard(
      ibInstance({
        id: "alpaca-1",
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
        id: "oanda-1",
        provider: "oanda",
        label: "OANDA",
        verifiesSymbols: false,
        searchesSymbols: false,
        instruments: [
          {
            instanceId: "oanda-1",
            externalSymbol: "AAPL",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            manualPrice: "",
            enabled: true,
            syntheticInverse: false,
            stale: false,
          },
        ],
      }),
    );

    expect(
      screen.queryByRole("button", { name: /verify symbol/i }),
    ).not.toBeInTheDocument();
  });

  describe("diagnostics wiring", () => {
    const docsUrl = "https://docs.example.com";
    const instance = ibInstance({
      id: "binance-1",
      provider: "binance",
      label: "Binance",
      searchesSymbols: false,
      references: { docsUrl },
      diagnostics: [
        {
          level: "error",
          code: "bad-symbol",
          kind: "config",
          title: "Bad symbol",
          detail: "BTCUSD is not valid for this feed.",
          actions: [
            { type: "restart" },
            { type: "remove_instrument", target: "BTCUSD" },
            { type: "open_docs" },
          ],
          at: "2026-06-22T16:05:00Z",
        },
      ],
    });

    function toolbar() {
      return within(screen.getByRole("toolbar", { name: "Problem actions" }));
    }

    it("passes the card's restart, remove and docs targets to the panel", async () => {
      const user = userEvent.setup();
      const onRestart = vi.fn();
      const onDeleteInstrument = vi.fn();
      renderCard(instance, { onRestart, onDeleteInstrument });

      await user.click(
        toolbar().getByRole("button", { name: "Restart feeds" }),
      );
      expect(onRestart).toHaveBeenCalledOnce();

      await user.click(
        toolbar().getByRole("button", { name: "Remove BTCUSD" }),
      );
      expect(onDeleteInstrument).toHaveBeenCalledExactlyOnceWith(
        instance,
        "BTCUSD",
      );
      expect(onDeleteInstrument.mock.calls[0][0]).toBe(instance);

      expect(
        toolbar().getByRole("link", { name: "Provider docs" }),
      ).toHaveAttribute("href", docsUrl);
    });

    it("disables the panel's action buttons while the card is busy", () => {
      renderCard(instance, { busy: true });

      expect(
        toolbar().getByRole("button", { name: "Restart feeds" }),
      ).toBeDisabled();
      expect(
        toolbar().getByRole("button", { name: "Remove BTCUSD" }),
      ).toBeDisabled();
    });
  });

  it("shows IB connection settings without the advanced block when creating a source", () => {
    render(
      <I18nextProvider i18n={i18n}>
        <CreateInstanceDialog
          provider={{ type: "ib", title: "Interactive Brokers" }}
          existingLabels={[]}
          credentialStorage={sealedStorageLoad}
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

  it("requires a JSON-safe non-negative integer IB client ID when creating", async () => {
    const user = userEvent.setup();
    const onCreate = vi.fn().mockResolvedValue(true);
    render(
      <I18nextProvider i18n={i18n}>
        <CreateInstanceDialog
          provider={{ type: "ib", title: "Interactive Brokers" }}
          existingLabels={[]}
          credentialStorage={sealedStorageLoad}
          busy={false}
          onOpenChange={vi.fn()}
          onCreate={onCreate}
        />
      </I18nextProvider>,
    );

    const submit = screen.getByRole("button", { name: "Create" });
    const clientId = screen.getByLabelText("Client ID");
    expect(submit).toBeEnabled();

    await user.type(clientId, "1.5");
    expect(submit).toBeDisabled();
    await user.clear(clientId);
    await user.type(clientId, "-1");
    expect(submit).toBeDisabled();
    await user.clear(clientId);
    await user.type(clientId, "9007199254740992");
    expect(submit).toBeDisabled();
    await user.clear(clientId);
    await user.type(clientId, "7");
    expect(submit).toBeEnabled();
    expect(onCreate).not.toHaveBeenCalled();
  });

  it("shows structured instrument metadata and duplicate pair warnings", () => {
    renderCard(
      ibInstance({
        instruments: [
          {
            instanceId: "ib-1",
            externalSymbol: "AAPL",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            manualPrice: "",
            enabled: true,
            syntheticInverse: false,
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

describe("manual market-data numeric guards", () => {
  function manualInstance(
    overrides: Partial<MarketDataInstance> = {},
  ): MarketDataInstance {
    return ibInstance({
      id: "manual-1",
      provider: "byo",
      label: "Manual",
      verifiesSymbols: false,
      searchesSymbols: false,
      settings: {},
      ...overrides,
    });
  }

  it("blocks instrument add until identifiers and the optional mark are valid", async () => {
    const user = userEvent.setup();
    const onUpsertInstrument = vi.fn().mockResolvedValue(true);
    renderCard(manualInstance(), { onUpsertInstrument });

    const add = screen.getByRole("button", { name: /add instrument/i });
    await user.type(screen.getByPlaceholderText("External symbol"), "AAPL");
    expect(add).toBeDisabled();
    await user.type(screen.getByPlaceholderText("Base"), "AAPL");
    expect(add).toBeEnabled();

    await user.type(screen.getByPlaceholderText("Manual price"), "bad");
    expect(add).toBeDisabled();
    await user.clear(screen.getByPlaceholderText("Manual price"));
    await user.type(screen.getByPlaceholderText("Manual price"), "-10.5");
    expect(add).toBeEnabled();
    expect(onUpsertInstrument).not.toHaveBeenCalled();
  });

  it("blocks sending a malformed manual mark on an existing instrument", async () => {
    const user = userEvent.setup();
    const onUpsertInstrument = vi.fn().mockResolvedValue(true);
    renderCard(
      manualInstance({
        instruments: [
          {
            instanceId: "manual-1",
            externalSymbol: "AAPL",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            manualPrice: "150",
            enabled: true,
            syntheticInverse: false,
            stale: false,
          },
        ],
      }),
      { onUpsertInstrument },
    );

    const mark = screen.getByLabelText("Manual price for AAPL");
    const send = screen.getByRole("button", { name: "Send price" });
    expect(send).toBeEnabled();
    await user.clear(mark);
    await user.type(mark, "bad");
    expect(send).toBeDisabled();
    expect(onUpsertInstrument).not.toHaveBeenCalled();
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

  it("requires a configured IB contract currency and valid numeric fields", async () => {
    const user = userEvent.setup();
    const onUpsertIBInstrument = vi.fn().mockResolvedValue(true);
    renderCard(ibInstance(), { onUpsertIBInstrument });
    const dialog = await openIBAddDialog(user);

    await user.type(within(dialog).getByLabelText("External symbol"), "AAPL");
    await user.type(within(dialog).getByLabelText("Base"), "AAPL");
    const add = within(dialog).getByRole("button", { name: /add instrument/i });
    expect(add).toBeDisabled();
    expect(
      within(dialog).getByText("IB contract currency is required."),
    ).toBeInTheDocument();

    await user.type(within(dialog).getByLabelText("Currency"), "usd");
    expect(add).toBeEnabled();

    await user.type(within(dialog).getByLabelText("Con ID"), "1.5");
    expect(add).toBeDisabled();
    await user.clear(within(dialog).getByLabelText("Con ID"));
    await user.type(within(dialog).getByLabelText("Con ID"), "265598");
    expect(add).toBeEnabled();

    await user.type(within(dialog).getByLabelText("Multiplier"), "0");
    expect(add).toBeDisabled();
    await user.clear(within(dialog).getByLabelText("Multiplier"));
    await user.type(within(dialog).getByLabelText("Multiplier"), "50");
    expect(add).toBeEnabled();

    const focusSpy = vi
      .spyOn(HTMLElement.prototype, "focus")
      .mockImplementation(() => {});
    await user.click(within(dialog).getByLabelText("Security type"));
    await user.click(screen.getByRole("option", { name: /Option/ }));
    focusSpy.mockRestore();
    await user.type(within(dialog).getByLabelText("Strike"), "bad");
    expect(add).toBeDisabled();
    expect(onUpsertIBInstrument).not.toHaveBeenCalled();
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
    await user.click(
      within(dialog).getByRole("button", { name: /^resolve$/i }),
    );

    // The resolve stays scoped to this feed and sends only the typed query.
    expect(onSearchSymbols).toHaveBeenCalledTimes(1);
    const [, input] = onSearchSymbols.mock.calls[0];
    expect(input).toEqual({ query: "apple" });

    await user.click(
      await within(dialog).findByRole("button", { name: /AAPL/ }),
    );

    // External symbol and quote pick up the resolved contract.
    expect(within(dialog).getByLabelText("External symbol")).toHaveValue(
      "AAPL",
    );
    expect(within(dialog).getByLabelText("Base")).toHaveValue("AAPL");
    expect(within(dialog).getByLabelText("Quote")).toHaveValue("USD");
    expect(within(dialog).getByLabelText("Contract symbol")).toHaveValue(
      "AAPL",
    );
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
    await user.click(
      within(dialog).getByRole("button", { name: /^resolve$/i }),
    );

    expect(onSearchSymbols).toHaveBeenCalledTimes(1);
    const [, input] = onSearchSymbols.mock.calls[0];
    expect(input).toEqual({ query: "BTC" });

    // The resolved listing exchange shows in the result row.
    await user.click(
      await within(dialog).findByRole("button", { name: /PAXOS/ }),
    );
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
    await user.click(
      within(dialog).getByRole("button", { name: /^resolve$/i }),
    );

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
        id: "bn-1",
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
    expect(searchedInstance.id).toBe("bn-1");
    expect(input).toMatchObject({ query: "BTC/USDT" });

    const result = await screen.findByRole("button", { name: /BTCUSDT/ });
    expect(result).toHaveTextContent(/^BTCUSDT · SPOT$/);
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
        id: "bn-1",
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
        id: "bn-1",
        provider: "binance",
        label: "Binance",
        searchesSymbols: true,
      }),
      { onSearchSymbols, onUpsertInstrument },
    );

    await user.type(screen.getByPlaceholderText("Base"), "ETHEUR");
    await user.type(screen.getByPlaceholderText("Quote"), "USD");
    await user.type(
      screen.getByLabelText("Resolve symbol in this feed"),
      "ETH",
    );
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
        id: "fh-1",
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
        id: "fh-1",
        provider: "finnhub",
        label: "Finnhub",
        instruments: [
          {
            instanceId: "fh-1",
            externalSymbol: "BINANCE:ETHUSDT",
            baseAsset: "ETH",
            quoteAsset: "USDT",
            manualPrice: "",
            enabled: true,
            syntheticInverse: false,
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
    expect(
      within(dialog).getByLabelText("External symbol"),
    ).toBeInTheDocument();
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
          instanceId: "ib-1",
          externalSymbol: "MSFT",
          baseAsset: "MSFT",
          quoteAsset: "USD",
          manualPrice: "",
          enabled: true,
          syntheticInverse: false,
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
    await user.type(within(dialog).getByLabelText("Base"), "AAPL");
    await user.type(within(dialog).getByLabelText("Currency"), "USD");
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
          instanceId: "ib-1",
          externalSymbol: "MSFT",
          baseAsset: "MSFT",
          quoteAsset: "USD",
          manualPrice: "",
          enabled: true,
          syntheticInverse: false,
          stale: false,
        },
        {
          instanceId: "ib-1",
          externalSymbol: "AAPL",
          baseAsset: "AAPL",
          quoteAsset: "USD",
          manualPrice: "",
          enabled: true,
          syntheticInverse: false,
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
          credentialStorage={sealedStorageLoad}
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

  it("keeps save disabled for a fractional IB client ID", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(true);
    render(
      <I18nextProvider i18n={i18n}>
        <InstanceSettingsDialog
          instance={ibInstance()}
          busy={false}
          existingLabels={[]}
          credentialStorage={sealedStorageLoad}
          onOpenChange={vi.fn()}
          onSave={onSave}
        />
      </I18nextProvider>,
    );

    const clientId = screen.getByLabelText("Client ID");
    const save = screen.getByRole("button", { name: "Save" });
    expect(save).toBeEnabled();
    await user.clear(clientId);
    await user.type(clientId, "1.5");
    expect(save).toBeDisabled();
    expect(onSave).not.toHaveBeenCalled();
  });
});

describe("stale quote rendering", () => {
  it("shows the price and stale badge when an instrument is stale with a quote", () => {
    renderCard(
      ibInstance({
        instruments: [
          {
            instanceId: "ib-1",
            externalSymbol: "AAPL",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            manualPrice: "",
            enabled: true,
            syntheticInverse: false,
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

    // Price must be visible, not "-".
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
            instanceId: "ib-1",
            externalSymbol: "AAPL",
            baseAsset: "AAPL",
            quoteAsset: "USD",
            manualPrice: "",
            enabled: true,
            syntheticInverse: false,
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

describe("synthetic inverse price rendering", () => {
  it.each(["0", "not-a-price"])(
    "falls through an uninvertible mark %s to bid-to-ask semantics",
    (mark) => {
      renderCard(
        ibInstance({
          instruments: [
            {
              instanceId: "ib-1",
              externalSymbol: "EURUSD",
              baseAsset: "EUR",
              quoteAsset: "USD",
              manualPrice: "",
              enabled: true,
              syntheticInverse: true,
              stale: false,
              quote: {
                asOf: new Date().toISOString(),
                receivedAt: new Date().toISOString(),
                mark,
                bid: "4",
                ask: "8",
              },
              inverseQuote: {
                asOf: new Date().toISOString(),
                receivedAt: new Date().toISOString(),
                mark: "",
                bid: "0.125",
                ask: "0.25",
              },
            },
          ],
        }),
      );

      const row = screen.getByText("EURUSD").closest("tr") as HTMLElement;
      expect(within(row).getByText("4 -> 0.25")).toBeInTheDocument();
      expect(within(row).queryByText(`${mark} ->`)).not.toBeInTheDocument();
    },
  );

  it("keeps the single received price when the inverse is suppressed", () => {
    renderCard(
      ibInstance({
        instruments: [
          {
            instanceId: "ib-1",
            externalSymbol: "EURUSD",
            baseAsset: "EUR",
            quoteAsset: "USD",
            manualPrice: "",
            enabled: true,
            syntheticInverse: false,
            stale: false,
            quote: {
              asOf: new Date().toISOString(),
              receivedAt: new Date().toISOString(),
              mark: "2",
              bid: "",
              ask: "",
            },
            inverseQuote: {
              asOf: new Date().toISOString(),
              receivedAt: new Date().toISOString(),
              mark: "0.5",
              bid: "",
              ask: "",
            },
          },
        ],
      }),
    );

    const row = screen.getByText("EURUSD").closest("tr") as HTMLElement;
    expect(within(row).getByText("2")).toBeInTheDocument();
    expect(within(row).queryByText(/->/u)).not.toBeInTheDocument();
  });
});

describe("source deletion", () => {
  it("deletes a source with configured instruments without force confirmation", async () => {
    const user = userEvent.setup();
    fetchMarketDataMock.mockResolvedValue(
      status(
        ibInstance({
          instruments: [
            {
              instanceId: "ib-1",
              externalSymbol: "AAPL",
              baseAsset: "AAPL",
              quoteAsset: "USD",
              manualPrice: "",
              enabled: true,
              syntheticInverse: false,
              stale: false,
            },
          ],
        }),
      ),
    );

    renderMarketDataPage();
    await screen.findByText("IB Gateway");
    await user.click(screen.getByRole("button", { name: "Delete source" }));

    const dialog = await screen.findByRole("dialog", {
      name: "Delete market-data source?",
    });
    expect(
      within(dialog).queryByRole("button", { name: "Force delete" }),
    ).not.toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    expect(deleteInstanceMock).toHaveBeenCalledWith("ib-1");
  });
});

describe("unsealed key warning", () => {
  const warningText = "Keys will be stored unencrypted";

  // Every provider whose settings take keys, with the fields that make it
  // savable, the credentials those fields produce, and the keys an existing
  // source of that provider has stored.
  const keyedProviders: {
    type: string;
    title: string;
    fields: Record<string, string>;
    credentials: string;
    secrets: Record<string, boolean>;
  }[] = [
    {
      type: "alpaca",
      title: "Alpaca",
      fields: { "API key": "usable-key", "API secret": "usable-secret" },
      credentials: '{"apiKey":"usable-key","apiSecret":"usable-secret"}',
      secrets: { apiKey: true, apiSecret: true },
    },
    {
      type: "oanda",
      title: "OANDA",
      fields: { Token: "usable-token", "Account ID": "001-001" },
      credentials:
        '{"token":"usable-token","accountID":"001-001","environment":"practice"}',
      secrets: { token: true },
    },
    {
      type: "finnhub",
      title: "Finnhub",
      fields: { Token: "usable-token" },
      credentials: '{"token":"usable-token"}',
      secrets: { token: true },
    },
  ];
  const keyedCases = keyedProviders.flatMap((provider) => [
    { ...provider, sealed: false, warning: true },
    { ...provider, sealed: true, warning: false },
  ]);

  function expectWarning(dialog: ReturnType<typeof within>, warning: boolean) {
    if (warning) {
      expect(dialog.getByRole("alert")).toHaveTextContent(warningText);
    } else {
      expect(dialog.queryByText(warningText)).not.toBeInTheDocument();
    }
  }

  async function fillEnabled(
    user: ReturnType<typeof userEvent.setup>,
    dialog: ReturnType<typeof within>,
    fields: Record<string, string>,
  ) {
    for (const [label, value] of Object.entries(fields)) {
      const input = dialog.getByLabelText(label);
      expect(input).toBeEnabled();
      await user.type(input, value);
    }
  }

  async function openCreateSource(
    user: ReturnType<typeof userEvent.setup>,
    provider: string,
  ) {
    fetchMarketDataMock.mockResolvedValue({
      ...status(ibInstance()),
      providers: [
        { type: "ib", title: "Interactive Brokers" },
        ...keyedProviders.map(({ type, title }) => ({ type, title })),
      ],
    });
    renderMarketDataPage();
    await screen.findByRole("heading", { name: "IB Gateway" });
    await user.click(
      screen.getByRole("button", { name: /Add a market-data source/ }),
    );
    const guide = screen.getByRole("dialog", { name: "Sources" });
    await user.click(
      within(guide).getByRole("button", { name: new RegExp(`^${provider}`) }),
    );
    return within(
      screen.getByRole("dialog", { name: `Add ${provider} source` }),
    );
  }

  it.each(keyedCases)(
    "warns on $title source creation only when credential storage is unsealed (sealed $sealed)",
    async ({ type, title, fields, credentials, sealed, warning }) => {
      const user = userEvent.setup();
      fetchCredentialStorageMock.mockResolvedValue(storage(sealed));
      const dialog = await openCreateSource(user, title);
      expectWarning(dialog, warning);
      await fillEnabled(user, dialog, fields);
      await user.click(dialog.getByRole("button", { name: "Create" }));
      expect(createInstanceMock).toHaveBeenCalledExactlyOnceWith({
        provider: type,
        label: title,
        credentials,
        enabled: true,
      });
    },
  );

  it.each(keyedCases)(
    "warns in $title source settings only when credential storage is unsealed (sealed $sealed)",
    async ({ type, title, fields, credentials, secrets, sealed, warning }) => {
      const user = userEvent.setup();
      fetchCredentialStorageMock.mockResolvedValue(storage(sealed));
      const instance = ibInstance({
        id: `${type}-1`,
        provider: type,
        label: title,
        settings: {},
        secrets,
      });
      fetchMarketDataMock.mockResolvedValue({
        ...status(instance),
        providers: [{ type, title }],
      });
      renderMarketDataPage();
      await screen.findByRole("heading", { name: title });
      await user.click(screen.getByRole("button", { name: "Edit settings" }));
      const dialog = within(
        screen.getByRole("dialog", { name: "Edit source settings" }),
      );
      expectWarning(dialog, warning);
      await fillEnabled(user, dialog, fields);
      await user.click(dialog.getByRole("button", { name: "Save" }));
      expect(updateSettingsMock).toHaveBeenCalledExactlyOnceWith(`${type}-1`, {
        label: title,
        credentials,
      });
    },
  );

  it("never warns on keyless source creation", async () => {
    const user = userEvent.setup();
    fetchCredentialStorageMock.mockResolvedValue(storage(false));
    const dialog = await openCreateSource(user, "Interactive Brokers");
    expect(dialog.getByLabelText("Host")).toBeInTheDocument();
    expectWarning(dialog, false);
  });

  it("never warns in keyless source settings", async () => {
    const user = userEvent.setup();
    fetchCredentialStorageMock.mockResolvedValue(storage(false));
    renderMarketDataPage();
    await screen.findByRole("heading", { name: "IB Gateway" });
    await user.click(screen.getByRole("button", { name: "Edit settings" }));
    const dialog = within(
      screen.getByRole("dialog", { name: "Edit source settings" }),
    );
    expect(dialog.getByLabelText("Host")).toBeInTheDocument();
    expectWarning(dialog, false);
  });

  it("shows a failed credential storage load and keeps key entry enabled", async () => {
    const user = userEvent.setup();
    fetchCredentialStorageMock.mockRejectedValue(
      new ApiError("Credential storage unavailable", "internal"),
    );
    const dialog = await openCreateSource(user, "Finnhub");
    expect(dialog.getByRole("alert")).toHaveTextContent(
      "Could not check whether stored keys are encrypted: Credential storage unavailable",
    );
    expect(dialog.queryByText(warningText)).not.toBeInTheDocument();
    await user.type(dialog.getByLabelText("Token"), "usable-token");
    expect(dialog.getByRole("button", { name: "Create" })).toBeEnabled();
  });
});

describe("stored secret placeholder", () => {
  it("marks only the stored secret in a source's settings form", () => {
    render(
      <I18nextProvider i18n={i18n}>
        <InstanceSettingsDialog
          instance={ibInstance({
            id: "alpaca-1",
            provider: "alpaca",
            label: "Alpaca",
            settings: {},
            secrets: { apiKey: true },
          })}
          busy={false}
          existingLabels={[]}
          credentialStorage={sealedStorageLoad}
          onOpenChange={vi.fn()}
          onSave={vi.fn().mockResolvedValue(true)}
        />
      </I18nextProvider>,
    );

    expect(screen.getByLabelText("API key")).toHaveAttribute(
      "placeholder",
      "Keep stored secret",
    );
    expect(screen.getByLabelText("API secret")).toHaveAttribute(
      "placeholder",
      "",
    );
  });
});
