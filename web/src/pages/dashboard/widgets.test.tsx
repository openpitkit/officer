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

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type {
  MarketDataInstance,
  MarketDataInstrument,
  MarketDataStatus,
  TradingConnection,
  TradingSnapshot,
} from "@/api/types";
import { useMarketData } from "@/api/useMarketData";
import { useTrading } from "@/api/useTrading";
import {
  DashboardItemChips,
  DashboardSourceError,
  DashboardSourceRow,
  DashboardStatusCard,
  type DashboardItemChipsProps,
  type DashboardSourceErrorProps,
  type DashboardSourceRowProps,
  type DashboardStatusCardProps,
} from "@/components/dashboard";
import {
  MarketDataCard,
  TradingConnectionsCard,
} from "@/pages/dashboard/widgets";

vi.mock("@/api/useMarketData", () => ({ useMarketData: vi.fn() }));
vi.mock("@/api/useTrading", () => ({ useTrading: vi.fn() }));
// Spies that render the real widgets: the market data cases read the
// rendered card, the trading cases read what the page passes.
vi.mock("@/components/dashboard", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/components/dashboard")>();
  return {
    ...actual,
    DashboardItemChips: vi.fn(actual.DashboardItemChips),
    DashboardSourceError: vi.fn(actual.DashboardSourceError),
    DashboardSourceRow: vi.fn(actual.DashboardSourceRow),
    DashboardStatusCard: vi.fn(actual.DashboardStatusCard),
  };
});

const useMarketDataMock = vi.mocked(useMarketData);
const useTradingMock = vi.mocked(useTrading);

function instrument(
  symbol: string,
  fields: Partial<MarketDataInstrument> = {},
): MarketDataInstrument {
  return {
    instanceId: "feed",
    externalSymbol: symbol,
    baseAsset: symbol,
    quoteAsset: "USD",
    manualPrice: "",
    enabled: true,
    syntheticInverse: false,
    stale: false,
    ...fields,
  };
}

function instance(
  id: string,
  fields: Partial<MarketDataInstance> = {},
): MarketDataInstance {
  return {
    id,
    provider: "alpaca",
    label: id,
    credentials: "",
    enabled: true,
    state: "running",
    verifiesSymbols: false,
    searchesSymbols: false,
    instruments: [],
    diagnostics: [],
    ...fields,
  };
}

function marketData(
  instances: MarketDataInstance[],
  restartRequired = false,
): MarketDataStatus {
  return { providers: [], instances, freshnessSeconds: 30, restartRequired };
}

function renderMarketDataCard(data: MarketDataStatus) {
  useMarketDataMock.mockReturnValue({
    load: { state: "ready", data, error: null },
    reload: vi.fn(),
  });
  return render(
    <MemoryRouter>
      <Routes>
        <Route path="/" element={<MarketDataCard />} />
        <Route path="/market-data" element={<p>market data page</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("MarketDataCard", () => {
  it("says no source is enabled and hides disabled sources", () => {
    renderMarketDataCard(
      marketData([instance("Off feed", { enabled: false })]),
    );

    expect(screen.getByText("Market data")).toBeInTheDocument();
    expect(screen.getByText("No sources enabled")).toBeInTheDocument();
    expect(screen.queryByText("Off feed")).not.toBeInTheDocument();
  });

  it("clips a source error and otherwise names its latest diagnostic", () => {
    const diagnostic = {
      level: "error",
      code: "auth",
      kind: "auth",
      detail: "",
      actions: [],
      at: "2026-10-05T00:00:00Z",
    };
    renderMarketDataCard(
      marketData([
        instance("Feed A", { state: "error", error: "e".repeat(45) }),
        instance("Feed B", {
          state: "error",
          diagnostics: [
            { ...diagnostic, title: "Old" },
            { ...diagnostic, title: "Key rejected" },
          ],
        }),
      ]),
    );

    expect(
      screen.getByText(`Error Feed A: ${"e".repeat(40)}…`),
    ).toBeInTheDocument();
    expect(screen.getByText("Error Feed B - Key rejected")).toBeInTheDocument();
  });

  it("marks a pending source and the required restart", () => {
    renderMarketDataCard(
      marketData([instance("Feed A", { state: "pending" })], true),
    );

    expect(screen.getByText("Restart needed")).toBeInTheDocument();
    expect(screen.getByText("Feed A - Not applied")).toBeInTheDocument();
  });

  it("shows healthy instruments up to the limit and counts the rest", () => {
    renderMarketDataCard(
      marketData([
        instance("Feed A", {
          instruments: [
            instrument("AAA"),
            instrument("BBB"),
            instrument("CCC"),
            instrument("DDD"),
            instrument("EEE"),
            instrument("OFF", { enabled: false }),
          ],
          diagnostics: [
            {
              level: "warn",
              code: "gap",
              kind: "gap",
              title: "Gap",
              detail: "",
              actions: [],
              at: "2026-10-05T00:00:00Z",
            },
          ],
        }),
      ]),
    );

    expect(screen.getByText("Feed A")).toBeInTheDocument();
    expect(screen.getByText("AAA/USD")).toHaveClass("text-[var(--ok)]");
    expect(screen.getByText("CCC/USD")).toBeInTheDocument();
    expect(screen.queryByText("DDD/USD")).not.toBeInTheDocument();
    expect(screen.queryByText("OFF/USD")).not.toBeInTheDocument();
    expect(screen.getByText("+2 OK")).toBeInTheDocument();
    expect(screen.getByText("1 issue(s)")).toBeInTheDocument();
  });

  it("caps stale instruments across sources and counts the rest", () => {
    const stale = (symbol: string) => instrument(symbol, { stale: true });
    renderMarketDataCard(
      marketData([
        instance("Feed A", {
          instruments: [stale("S1"), stale("S2"), stale("S3")],
        }),
        instance("Feed B", {
          instruments: [stale("S4"), stale("S5"), stale("S6")],
        }),
      ]),
    );

    expect(screen.getByText("S1/USD")).toHaveClass("text-[var(--warn)]");
    expect(screen.getByText("S4/USD")).toBeInTheDocument();
    expect(screen.queryByText("S5/USD")).not.toBeInTheDocument();
    expect(screen.getByText("+2 stale")).toBeInTheDocument();
  });

  it("shows manual instruments as neutral and never as stale", () => {
    renderMarketDataCard(
      marketData([
        instance("Manual", {
          provider: "byo",
          instruments: [instrument("MAN", { stale: true })],
        }),
      ]),
    );

    expect(screen.getByText("MAN/USD")).toHaveClass("text-muted");
    expect(screen.queryByText(/stale/)).not.toBeInTheDocument();
  });

  it("opens the market data page", async () => {
    renderMarketDataCard(marketData([]));

    await userEvent.click(screen.getByRole("button"));

    expect(screen.getByText("market data page")).toBeInTheDocument();
  });
});

function connection(
  label: string,
  fields: Partial<TradingConnection> = {},
): TradingConnection {
  return {
    id: label,
    provider: "alpaca",
    label,
    mode: "paper",
    enabled: true,
    venueIdentity: "",
    state: "running",
    secrets: { apiKey: true, apiSecret: true },
    instruments: [],
    access: [],
    diagnostics: [],
    feeAccount: "",
    ...fields,
  };
}

function tradingInstrument(
  symbol: string,
  enabled = true,
): TradingConnection["instruments"][number] {
  return {
    externalSymbol: symbol,
    baseAsset: symbol,
    quoteAsset: "USD",
    enabled,
  };
}

function tradingDiagnostic(
  title: string,
): TradingConnection["diagnostics"][number] {
  return {
    level: "error",
    code: "connection_error",
    kind: "provider",
    title,
    detail: "",
    actions: [],
    at: "2026-10-13T08:00:00Z",
  };
}

function renderTradingCard(connections: TradingConnection[]) {
  const data: TradingSnapshot = {
    providers: [],
    connections,
  };
  const load = { state: "ready" as const, data, error: null };
  useTradingMock.mockReturnValue({ load, reload: vi.fn() });
  render(
    <MemoryRouter>
      <TradingConnectionsCard />
    </MemoryRouter>,
  );
  return load;
}

function lastProps<P>(component: { mock: { calls: unknown[][] } }): P {
  const calls = component.mock.calls;
  expect(calls.length).toBeGreaterThan(0);
  return calls[calls.length - 1][0] as P;
}

describe("TradingConnectionsCard", () => {
  it("opens the trading connections page with the polled snapshot", () => {
    const load = renderTradingCard([]);

    expect(
      lastProps<DashboardStatusCardProps<TradingSnapshot>>(
        vi.mocked(DashboardStatusCard),
      ),
    ).toMatchObject({
      title: "Trading connections",
      to: "/trading-connections",
      load,
    });
  });

  it("says no connection is enabled and hides disabled connections", () => {
    renderTradingCard([connection("Off venue", { enabled: false })]);

    expect(screen.getByText("No connections enabled")).toBeInTheDocument();
    expect(vi.mocked(DashboardSourceRow)).not.toHaveBeenCalled();
  });

  it("passes a running connection's enabled instruments to the chips", () => {
    renderTradingCard([
      connection("Alpaca paper", {
        instruments: [
          tradingInstrument("AAA"),
          tradingInstrument("OFF", false),
          tradingInstrument("BBB"),
        ],
      }),
    ]);

    expect(
      lastProps<DashboardSourceRowProps>(vi.mocked(DashboardSourceRow)),
    ).toMatchObject({ label: "Alpaca paper" });
    expect(screen.getByText("Paper")).toBeInTheDocument();
    expect(screen.getByText("Running")).toHaveClass("text-[var(--ok)]");
    const chips = lastProps<DashboardItemChipsProps>(
      vi.mocked(DashboardItemChips),
    );
    expect(chips).toMatchObject({
      items: [
        { key: "AAA", text: "AAA/USD" },
        { key: "BBB", text: "BBB/USD" },
      ],
      variant: "neutral",
      limit: 3,
    });
    expect(chips.moreLabel(2)).toBe("+2 more");
    expect(vi.mocked(DashboardSourceError)).not.toHaveBeenCalled();
  });

  it("passes an unavailable connection's error to the error chip", () => {
    renderTradingCard([
      connection("Alpaca live", {
        mode: "live",
        state: "unavailable",
        error: "Venue offline",
        instruments: [tradingInstrument("AAA")],
      }),
    ]);

    expect(screen.getByText("Live")).toBeInTheDocument();
    expect(
      lastProps<DashboardSourceErrorProps>(vi.mocked(DashboardSourceError)),
    ).toEqual({ title: "Unavailable", error: "Venue offline" });
    expect(screen.queryByText("Running")).not.toBeInTheDocument();
    expect(vi.mocked(DashboardItemChips)).not.toHaveBeenCalled();
  });

  it("passes an unavailable connection's latest diagnostic to the error chip", () => {
    renderTradingCard([
      connection("Alpaca live", {
        state: "unavailable",
        error: "Venue offline",
        diagnostics: [tradingDiagnostic("Old"), tradingDiagnostic("Latest")],
      }),
    ]);

    expect(
      lastProps<DashboardSourceErrorProps>(vi.mocked(DashboardSourceError)),
    ).toEqual({
      title: "Unavailable",
      error: "Venue offline",
      diagnostic: "Latest",
    });
  });

  it("passes a degraded connection's error and latest diagnostic to the warn chip", () => {
    renderTradingCard([
      connection("Alpaca paper", {
        state: "degraded",
        error: "Trade updates refused",
        instruments: [tradingInstrument("AAA")],
        diagnostics: [tradingDiagnostic("Old"), tradingDiagnostic("Latest")],
      }),
    ]);

    expect(
      lastProps<DashboardSourceErrorProps>(vi.mocked(DashboardSourceError)),
    ).toEqual({
      variant: "warn",
      title: "Degraded",
      error: "Trade updates refused",
      diagnostic: "Latest",
    });
    expect(screen.queryByText("Running")).not.toBeInTheDocument();
    expect(vi.mocked(DashboardItemChips)).not.toHaveBeenCalled();
  });

  it("counts a running connection's diagnostics without nesting a panel in the card", () => {
    const consoleError = vi.spyOn(console, "error");
    try {
      renderTradingCard([
        connection("Alpaca paper", {
          instruments: [tradingInstrument("AAA")],
          diagnostics: [tradingDiagnostic("Gap"), tradingDiagnostic("Lag")],
        }),
      ]);

      expect(screen.getByText("Running")).toHaveClass("text-[var(--ok)]");
      expect(screen.getByText("2 issue(s)")).toBeInTheDocument();
      expect(
        screen.queryByRole("region", { name: "Problems" }),
      ).not.toBeInTheDocument();
      expect(
        consoleError.mock.calls.filter((args) =>
          String(args[0]).includes("cannot be a descendant of"),
        ),
      ).toEqual([]);
    } finally {
      consoleError.mockRestore();
    }
  });

  it("warns about an enabled connection that is stopped", () => {
    renderTradingCard([
      connection("Alpaca paper", {
        state: "stopped",
        instruments: [tradingInstrument("AAA")],
      }),
    ]);

    expect(screen.getByText("Paper")).toBeInTheDocument();
    expect(screen.getByText("Stopped")).toHaveClass("text-[var(--warn)]");
    expect(vi.mocked(DashboardItemChips)).not.toHaveBeenCalled();
    expect(vi.mocked(DashboardSourceError)).not.toHaveBeenCalled();
  });
});
