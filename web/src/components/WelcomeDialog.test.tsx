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

import type { MarketDataInstance, MarketDataStatus } from "@/api/types";
import { WelcomeDialog } from "@/components/WelcomeDialog";
import { ApiClientProvider, type OfficerApi } from "@/framework";
import i18n from "@/i18n";
import { ThemeProvider } from "@/theme/ThemeProvider";

const createMarketDataInstanceMock = vi.fn();
const fetchMarketDataMock = vi.fn();
const restartMarketDataMock = vi.fn();
const setMarketDataInstanceEnabledMock = vi.fn();
const upsertMarketDataInstrumentMock = vi.fn();
const setWelcomeSeenMock = vi.fn();

function byoInstance(
  overrides: Partial<MarketDataInstance> = {},
): MarketDataInstance {
  return {
    externalId: "byo-1",
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
      externalId: "binance-1",
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

function renderWelcome(onOpenChange = vi.fn()) {
  const api = {
    createAccount: vi.fn().mockResolvedValue({}),
    createAdjustment: vi.fn().mockResolvedValue({}),
    createMarketDataInstance: createMarketDataInstanceMock,
    fetchAccounts: vi.fn().mockResolvedValue([]),
    fetchMarketData: fetchMarketDataMock,
    putLimit: vi.fn().mockResolvedValue({}),
    restartMarketData: restartMarketDataMock,
    setAccountNotes: vi.fn().mockResolvedValue({}),
    setMarketDataInstanceEnabled: setMarketDataInstanceEnabledMock,
    setWelcomeSeen: setWelcomeSeenMock,
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
  fetchMarketDataMock.mockResolvedValue(marketDataStatus());
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
          /Install FX and crypto static prices partially applied: 2 rows updated before failure: provider refused/i,
        ),
      ).toBeDefined();
    });
    expect(restartMarketDataMock).not.toHaveBeenCalled();
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
