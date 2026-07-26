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

import type {
  Asset,
  AssetClass,
  MarketDataInstance,
  MarketDataStatus,
} from "@/api/types";
import { WelcomeDialog } from "@/components/WelcomeDialog";
import { ApiClientProvider, type OfficerApi } from "@/framework";
import i18n from "@/i18n";
import { ThemeProvider } from "@/theme/ThemeProvider";

const createMarketDataInstanceMock = vi.fn();
const createAccountMock = vi.fn();
const createAdjustmentMock = vi.fn();
const createAssetMock = vi.fn();
const createAssetClassMock = vi.fn();
const fetchAccountsMock = vi.fn();
const fetchAssetsMock = vi.fn();
const fetchAssetClassesMock = vi.fn();
const fetchMarketDataMock = vi.fn();
const putLimitMock = vi.fn();
const restartMarketDataMock = vi.fn();
const setAccountCurrencyMock = vi.fn();
const setAccountNotesMock = vi.fn();
const setMarketDataInstanceEnabledMock = vi.fn();
const upsertMarketDataInstrumentMock = vi.fn();
const updateAssetMock = vi.fn();
const updateAssetClassMock = vi.fn();
const setWelcomeSeenMock = vi.fn();

function byoInstance(
  overrides: Partial<MarketDataInstance> = {},
): MarketDataInstance {
  return {
    id: "byo-1",
    provider: "byo",
    label: "FX (static)",
    credentials: "",
    settings: {},
    secrets: {},
    enabled: true,
    state: "ok",
    verifiesSymbols: false,
    searchesSymbols: false,
    instruments: [],
    diagnostics: [],
    references: {},
    ...overrides,
  };
}

function binanceInstance(
  overrides: Partial<MarketDataInstance> = {},
): MarketDataInstance {
  return {
    ...byoInstance({
      id: "binance-1",
      provider: "binance",
      label: "Binance spot",
      ...overrides,
    }),
  };
}

function marketDataStatus(
  overrides: Partial<MarketDataStatus> = {},
): MarketDataStatus {
  return {
    freshnessSeconds: 10,
    providers: [],
    instances: [],
    restartRequired: false,
    ...overrides,
  };
}

function asset(overrides: Partial<Asset> = {}): Asset {
  return {
    code: "USD",
    title: "US Dollar",
    assetClass: "currency",
    ...overrides,
  };
}

function assetClass(overrides: Partial<AssetClass> = {}): AssetClass {
  return {
    code: "currency",
    title: "Currencies",
    notes:
      "Government-issued cash currencies used for settlement, cash balances, and FX conversion.",
    assetCount: 0,
    ...overrides,
  };
}

function renderWelcome(onOpenChange = vi.fn()) {
  const api = {
    createAsset: createAssetMock,
    createAssetClass: createAssetClassMock,
    createAccount: createAccountMock,
    createAdjustment: createAdjustmentMock,
    createMarketDataInstance: createMarketDataInstanceMock,
    fetchAccounts: fetchAccountsMock,
    fetchAssetClasses: fetchAssetClassesMock,
    fetchAssets: fetchAssetsMock,
    fetchMarketData: fetchMarketDataMock,
    putLimit: putLimitMock,
    restartMarketData: restartMarketDataMock,
    setAccountCurrency: setAccountCurrencyMock,
    setAccountNotes: setAccountNotesMock,
    setMarketDataInstanceEnabled: setMarketDataInstanceEnabledMock,
    setWelcomeSeen: setWelcomeSeenMock,
    updateAsset: updateAssetMock,
    updateAssetClass: updateAssetClassMock,
    upsertMarketDataInstrument: upsertMarketDataInstrumentMock,
  } as unknown as OfficerApi;
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
        <MemoryRouter>
          <ApiClientProvider config={{ baseUrl: "/app/api/v1" }} api={api}>
            <WelcomeDialog open onOpenChange={onOpenChange} />
          </ApiClientProvider>
        </MemoryRouter>
      </ThemeProvider>
    </I18nextProvider>,
  );
  return onOpenChange;
}

beforeEach(() => {
  vi.clearAllMocks();
  createAccountMock.mockResolvedValue({});
  createAdjustmentMock.mockResolvedValue({});
  fetchAccountsMock.mockResolvedValue([]);
  fetchAssetClassesMock.mockResolvedValue([]);
  fetchAssetsMock.mockResolvedValue([]);
  createAssetClassMock.mockImplementation(async (code, title, notes) =>
    assetClass({ code, title, notes }),
  );
  createAssetMock.mockImplementation(async (code, title, assetClassCode) =>
    asset({ code, title, assetClass: assetClassCode }),
  );
  updateAssetClassMock.mockImplementation(async (_oldCode, code, title, notes) =>
    assetClass({ code, title, notes }),
  );
  updateAssetMock.mockImplementation(async (_oldCode, code, title, assetClassCode) =>
    asset({ code, title, assetClass: assetClassCode }),
  );
  fetchMarketDataMock.mockResolvedValue(marketDataStatus());
  putLimitMock.mockResolvedValue({});
  createMarketDataInstanceMock.mockImplementation(async (body) =>
    body.provider === "binance" ? binanceInstance() : byoInstance(),
  );
  setMarketDataInstanceEnabledMock.mockResolvedValue(undefined);
  upsertMarketDataInstrumentMock.mockResolvedValue(
    marketDataStatus({ instances: [byoInstance()] }),
  );
  restartMarketDataMock.mockResolvedValue(
    marketDataStatus({ instances: [byoInstance()] }),
  );
  setAccountCurrencyMock.mockResolvedValue({});
  setWelcomeSeenMock.mockResolvedValue(true);
});

describe("WelcomeDialog", () => {
  it("renders first-run actions", () => {
    renderWelcome();
    const dialog = screen.getByRole("dialog");

    expect(within(dialog).getByText("Welcome to Pit Officer")).toBeDefined();
    expect(
      within(dialog).getByRole("button", {
        name: /Create demo account and positions/i,
      }),
    ).toBeDefined();
    expect(
      within(dialog).getByRole("button", {
        name: /Install FX and crypto static prices/i,
      }),
    ).toBeDefined();
    expect(
      within(dialog).getByRole("button", {
        name: /Get FX and crypto rates from Binance/i,
      }),
    ).toBeDefined();
    expect(
      within(dialog).getByRole("button", {
        name: /Start working in Pit Officer/i,
      }),
    ).toBeDefined();
  });

  it("applies static market-data preset without closing", async () => {
    const user = userEvent.setup();
    const onOpenChange = renderWelcome();

    await user.click(
      screen.getByRole("button", {
        name: /Install FX and crypto static prices/i,
      }),
    );

    await waitFor(() => {
      expect(createMarketDataInstanceMock).toHaveBeenCalledWith({
        provider: "byo",
        label: "FX (static)",
        credentials: "",
        enabled: true,
      });
      expect(createAssetClassMock).toHaveBeenCalledWith(
        "currency",
        "Currencies",
        "Government-issued cash currencies used for settlement, cash balances, and FX conversion.",
      );
      expect(createAssetClassMock).toHaveBeenCalledWith(
        "stablecoin",
        "Stablecoins",
        "Tokenized cash-equivalent settlement assets used by crypto venues.",
      );
      expect(createAssetClassMock).toHaveBeenCalledWith(
        "crypto",
        "Crypto assets",
        "Native crypto assets used for demo balances and crypto venue risk checks.",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "USD",
        "US Dollar",
        "currency",
      );
      expect(createAssetMock).toHaveBeenCalledWith("EUR", "Euro", "currency");
      expect(createAssetMock).toHaveBeenCalledWith(
        "BTC",
        "Bitcoin",
        "crypto",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "ETH",
        "Ethereum",
        "crypto",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "USDT",
        "Tether USD",
        "stablecoin",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "USDC",
        "USD Coin",
        "stablecoin",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "EURI",
        "Eurite",
        "stablecoin",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "AEUR",
        "Anchored Coins AEUR",
        "stablecoin",
      );
      expect(upsertMarketDataInstrumentMock).toHaveBeenCalledTimes(6);
      expect(restartMarketDataMock).toHaveBeenCalledTimes(1);
      expect(upsertMarketDataInstrumentMock).not.toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({ baseAsset: "SOL" }),
      );
      expect(upsertMarketDataInstrumentMock).not.toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({ baseAsset: "GBP" }),
      );
      expect(upsertMarketDataInstrumentMock).not.toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({ quoteAsset: "JPY" }),
      );
      expect(upsertMarketDataInstrumentMock).not.toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({ externalSymbol: "EUR/USD" }),
      );
      expect(upsertMarketDataInstrumentMock).toHaveBeenCalledWith(
        "byo-1",
        expect.objectContaining({
          externalSymbol: "USDC/USD",
          baseAsset: "USDC",
          quoteAsset: "USD",
        }),
      );
      expect(upsertMarketDataInstrumentMock).toHaveBeenCalledWith(
        "byo-1",
        expect.objectContaining({
          externalSymbol: "EURI/EUR",
          baseAsset: "EURI",
          quoteAsset: "EUR",
        }),
      );
      expect(upsertMarketDataInstrumentMock).toHaveBeenCalledWith(
        "byo-1",
        expect.objectContaining({
          externalSymbol: "EURI/AEUR",
          baseAsset: "EURI",
          quoteAsset: "AEUR",
        }),
      );
    });
    expect(onOpenChange).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeDefined();
  });

  it("reports how far a preset got before a partial failure", async () => {
    const user = userEvent.setup();
    upsertMarketDataInstrumentMock
      .mockResolvedValueOnce(marketDataStatus({ instances: [byoInstance()] }))
      .mockRejectedValueOnce(new Error("provider refused"));
    renderWelcome();

    await user.click(
      screen.getByRole("button", {
        name: /Install FX and crypto static prices/i,
      }),
    );

    await waitFor(() => {
      expect(
        screen.getByText(
          /Install FX and crypto static prices partially applied: 19 rows updated before failure: provider refused/i,
        ),
      ).toBeDefined();
    });
    expect(restartMarketDataMock).not.toHaveBeenCalled();
  });

  it("reclasses auto-created preset assets before upserting feeds", async () => {
    const user = userEvent.setup();
    fetchAssetClassesMock.mockResolvedValue([
      assetClass(),
      assetClass({
        code: "stablecoin",
        title: "Stablecoins",
        notes:
          "Tokenized cash-equivalent settlement assets used by crypto venues.",
      }),
      assetClass({
        code: "crypto",
        title: "Crypto assets",
        notes:
          "Native crypto assets used for demo balances and crypto venue risk checks.",
      }),
      assetClass({
        code: "equity",
        title: "Equities",
        notes:
          "Company shares and private-market equity positions used in demo portfolios.",
      }),
    ]);
    fetchAssetsMock.mockResolvedValue([
      asset({ code: "USD", title: "", assetClass: "auto-created" }),
      asset({ code: "EUR", title: "", assetClass: "auto-created" }),
      asset({ code: "BTC", title: "", assetClass: "auto-created" }),
      asset({ code: "ETH", title: "", assetClass: "auto-created" }),
      asset({ code: "USDT", title: "", assetClass: "auto-created" }),
      asset({ code: "USDC", title: "", assetClass: "auto-created" }),
      asset({ code: "EURI", title: "", assetClass: "auto-created" }),
      asset({ code: "AEUR", title: "", assetClass: "auto-created" }),
      asset({ code: "AAPL", title: "", assetClass: "auto-created" }),
      asset({ code: "NVDA", title: "", assetClass: "auto-created" }),
      asset({ code: "SPCX", title: "", assetClass: "auto-created" }),
      asset({ code: "META", title: "", assetClass: "auto-created" }),
      asset({ code: "KO", title: "", assetClass: "auto-created" }),
    ]);
    renderWelcome();

    await user.click(
      screen.getByRole("button", {
        name: /Install FX and crypto static prices/i,
      }),
    );

    await waitFor(() => {
      expect(updateAssetMock).toHaveBeenCalledWith(
        "USD",
        "USD",
        "US Dollar",
        "currency",
      );
      expect(updateAssetMock).toHaveBeenCalledWith(
        "USDT",
        "USDT",
        "Tether USD",
        "stablecoin",
      );
      expect(updateAssetMock).toHaveBeenCalledWith(
        "BTC",
        "BTC",
        "Bitcoin",
        "crypto",
      );
      expect(createAssetMock).not.toHaveBeenCalled();
      expect(upsertMarketDataInstrumentMock).toHaveBeenCalledTimes(6);
    });
  });

  it("seeds demo account assets with explicit classes before positions", async () => {
    const user = userEvent.setup();
    renderWelcome();

    await user.click(
      screen.getByRole("button", {
        name: /Create demo account and positions/i,
      }),
    );

    await waitFor(() => {
      expect(createAccountMock).toHaveBeenCalledWith("demo-main", "", "USD");
      expect(setAccountCurrencyMock).toHaveBeenCalledWith("demo-main", "USD");
      expect(createAssetClassMock).toHaveBeenCalledWith(
        "crypto",
        "Crypto assets",
        "Native crypto assets used for demo balances and crypto venue risk checks.",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "BTC",
        "Bitcoin",
        "crypto",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "ETH",
        "Ethereum",
        "crypto",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "USD",
        "US Dollar",
        "currency",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "USDT",
        "Tether USD",
        "stablecoin",
      );
      expect(createAssetClassMock).toHaveBeenCalledWith(
        "equity",
        "Equities",
        "Company shares and private-market equity positions used in demo portfolios.",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "AAPL",
        "Apple Inc.",
        "equity",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "NVDA",
        "NVIDIA Corp.",
        "equity",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "SPCX",
        "SpaceX",
        "equity",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "META",
        "Facebook / Meta Platforms",
        "equity",
      );
      expect(createAssetMock).toHaveBeenCalledWith(
        "KO",
        "The Coca-Cola Company",
        "equity",
      );
      expect(createAdjustmentMock).toHaveBeenCalledTimes(9);
      expect(createAdjustmentMock).toHaveBeenCalledWith("demo-main", {
        asset: "USD",
        balance: { mode: "absolute", value: "100000" },
        averageEntryPrice: "1",
      });
      expect(createAdjustmentMock).toHaveBeenCalledWith("demo-main", {
        asset: "AAPL",
        balance: { mode: "absolute", value: "100" },
        averageEntryPrice: "200",
      });
      expect(createAdjustmentMock).toHaveBeenCalledWith("demo-main", {
        asset: "NVDA",
        balance: { mode: "absolute", value: "50" },
        averageEntryPrice: "150",
      });
      expect(createAdjustmentMock).toHaveBeenCalledWith("demo-main", {
        asset: "SPCX",
        balance: { mode: "absolute", value: "10" },
        averageEntryPrice: "300",
      });
      expect(createAdjustmentMock).toHaveBeenCalledWith("demo-main", {
        asset: "META",
        balance: { mode: "absolute", value: "25" },
        averageEntryPrice: "500",
      });
      expect(createAdjustmentMock).toHaveBeenCalledWith("demo-main", {
        asset: "KO",
        balance: { mode: "absolute", value: "100" },
        averageEntryPrice: "70",
      });
    });
    expect(createAssetMock.mock.invocationCallOrder[0]).toBeLessThan(
      createAdjustmentMock.mock.invocationCallOrder[0],
    );
    expect(createAssetMock.mock.invocationCallOrder[0]).toBeLessThan(
      setAccountCurrencyMock.mock.invocationCallOrder[0],
    );
  });

  it("applies Binance preset with provider-native FX symbols", async () => {
    const user = userEvent.setup();
    renderWelcome();

    await user.click(
      screen.getByRole("button", {
        name: /Get FX and crypto rates from Binance/i,
      }),
    );

    await waitFor(() => {
      expect(createMarketDataInstanceMock).toHaveBeenCalledWith({
        provider: "binance",
        label: "Binance spot",
        credentials: "",
        enabled: true,
      });
      expect(upsertMarketDataInstrumentMock).toHaveBeenCalledTimes(7);
      expect(restartMarketDataMock).toHaveBeenCalledTimes(1);
      expect(upsertMarketDataInstrumentMock).toHaveBeenCalledWith(
        "binance-1",
        expect.objectContaining({
          externalSymbol: "EURUSDT",
          baseAsset: "EUR",
          quoteAsset: "USDT",
          manualPrice: "",
        }),
      );
      expect(upsertMarketDataInstrumentMock).toHaveBeenCalledWith(
        "binance-1",
        expect.objectContaining({
          externalSymbol: "EURUSDC",
          baseAsset: "EUR",
          quoteAsset: "USDC",
          manualPrice: "",
        }),
      );
      expect(upsertMarketDataInstrumentMock).toHaveBeenCalledWith(
        "binance-1",
        expect.objectContaining({
          externalSymbol: "EUREURI",
          baseAsset: "EUR",
          quoteAsset: "EURI",
          manualPrice: "",
        }),
      );
      expect(upsertMarketDataInstrumentMock).not.toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({ baseAsset: "GBP" }),
      );
      expect(upsertMarketDataInstrumentMock).toHaveBeenCalledWith(
        "binance-1",
        expect.objectContaining({
          externalSymbol: "USDCUSDT",
          baseAsset: "USDC",
          quoteAsset: "USDT",
          manualPrice: "",
        }),
      );
      expect(upsertMarketDataInstrumentMock).toHaveBeenCalledWith(
        "binance-1",
        expect.objectContaining({
          externalSymbol: "USDCUSD",
          baseAsset: "USDC",
          quoteAsset: "USD",
          manualPrice: "",
        }),
      );
      expect(upsertMarketDataInstrumentMock).toHaveBeenCalledWith(
        "binance-1",
        expect.objectContaining({
          externalSymbol: "EURIUSDT",
          baseAsset: "EURI",
          quoteAsset: "USDT",
          manualPrice: "",
        }),
      );
      expect(upsertMarketDataInstrumentMock).toHaveBeenCalledWith(
        "binance-1",
        expect.objectContaining({
          externalSymbol: "USDTUSD",
          baseAsset: "USDT",
          quoteAsset: "USD",
          manualPrice: "",
        }),
      );
    });
  });

  it("persists the opt-out only when 'don't show again' is checked", async () => {
    const user = userEvent.setup();
    const onOpenChange = renderWelcome();

    await user.click(screen.getByRole("checkbox", { name: /don't show this again/i }));
    await user.click(screen.getByRole("button", { name: /Start working in Pit Officer/i }));

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(setWelcomeSeenMock).toHaveBeenCalledWith(true);
  });

  it("does not persist when closed without checking the box", async () => {
    const user = userEvent.setup();
    const onOpenChange = renderWelcome();

    await user.click(screen.getByRole("button", { name: /Start working in Pit Officer/i }));

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(setWelcomeSeenMock).not.toHaveBeenCalled();
  });
});
