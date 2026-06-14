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

import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  createMarketDataInstance,
  searchMarketDataSymbols,
  updateMarketDataInstanceSettings,
  verifyMarketDataSymbol,
} from "@/api/client";

function okMarketDataResponse(): Response {
  return new Response(
    JSON.stringify({
      marketData: {
        providers: [],
        instances: [],
        freshnessSeconds: 10,
        restartRequired: false,
      },
    }),
    {
      status: 200,
      headers: { "Content-Type": "application/json" },
    },
  );
}

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

beforeEach(() => {
  vi.restoreAllMocks();
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(okMarketDataResponse()));
});

describe("market-data client settings payloads", () => {
  it("sends provider credentials when creating an IB source", async () => {
    await createMarketDataInstance({
      type: "ib",
      label: "IB Gateway",
      credentials: JSON.stringify({
        host: "127.0.0.1",
        port: 7496,
        clientId: 7,
      }),
      enabled: true,
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/market-data/instances",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          type: "ib",
          label: "IB Gateway",
          credentials: '{"host":"127.0.0.1","port":7496,"clientId":7}',
          enabled: true,
        }),
      }),
    );
  });

  it("sends blank secret placeholders on settings update", async () => {
    await updateMarketDataInstanceSettings("alpaca-1", {
      label: "Alpaca",
      credentials: JSON.stringify({
        apiKey: "new-key",
        apiSecret: "",
      }),
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/market-data/instances/alpaca-1/settings",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          label: "Alpaca",
          credentials: '{"apiKey":"new-key","apiSecret":""}',
        }),
      }),
    );
  });
});

describe("searchMarketDataSymbols", () => {
  it("posts the resolve criteria and normalizes the backend top-level response", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        supported: true,
        matches: [
          {
            symbol: "BTC",
            name: "Bitcoin",
            secType: "CRYPTO",
            exchange: "PAXOS",
            currency: "USD",
            conId: "12345",
            localSymbol: "BTC.USD",
            tradingClass: "BTC",
          },
        ],
      }),
    );

    const result = await searchMarketDataSymbols("ib-1", {
      query: "BTC",
      secType: "CRYPTO",
      exchange: "PAXOS",
      currency: "USD",
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/market-data/instances/ib-1/search-symbols",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          query: "BTC",
          secType: "CRYPTO",
          exchange: "PAXOS",
          currency: "USD",
        }),
      }),
    );
    expect(result.supported).toBe(true);
    expect(result.matches).toEqual([
      {
        symbol: "BTC",
        name: "Bitcoin",
        secType: "CRYPTO",
        exchange: "PAXOS",
        currency: "USD",
        conId: "12345",
        localSymbol: "BTC.USD",
        tradingClass: "BTC",
      },
    ]);
  });

  it("tolerates the older wrapped response shape", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        search: {
          supported: true,
          matches: [
            {
              symbol: "ES",
              secType: "FUT",
              exchange: "CME",
              currency: "USD",
              conId: "495512563",
              lastTradeDateOrContractMonth: "20251219",
              multiplier: "50",
              tradingClass: "ES",
            },
          ],
        },
      }),
    );

    const result = await searchMarketDataSymbols("ib-1", {
      query: "ES",
      secType: "FUT",
      exchange: "CME",
      currency: "USD",
      lastTradeDateOrContractMonth: "20251219",
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/market-data/instances/ib-1/search-symbols",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          query: "ES",
          secType: "FUT",
          exchange: "CME",
          currency: "USD",
          lastTradeDateOrContractMonth: "20251219",
        }),
      }),
    );
    expect(result.matches[0]).toMatchObject({
      symbol: "ES",
      secType: "FUT",
      exchange: "CME",
      conId: "495512563",
      lastTradeDateOrContractMonth: "20251219",
      multiplier: "50",
    });
  });

  it("posts a decimal-string strike and omits empty optionals", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({ search: { supported: true, matches: [] } }),
    );

    await searchMarketDataSymbols("ib-1", {
      query: "SPX",
      secType: "OPT",
      exchange: "",
      currency: "USD",
      strike: "5000.00",
      right: "C",
    });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/market-data/instances/ib-1/search-symbols",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          query: "SPX",
          secType: "OPT",
          currency: "USD",
          right: "C",
          strike: "5000.00",
        }),
      }),
    );
  });

  it("omits all optionals when only the query is given", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({ search: { supported: true, matches: [] } }),
    );

    await searchMarketDataSymbols("ib-1", { query: "msft" });

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/market-data/instances/ib-1/search-symbols",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ query: "msft" }),
      }),
    );
  });

  it("tolerates missing optional fields and an unsupported result", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        search: { supported: false, matches: [{ symbol: "X", secType: "STK" }] },
      }),
    );

    const result = await searchMarketDataSymbols("byo-1", { query: "x" });

    expect(result.supported).toBe(false);
    expect(result.matches).toEqual([{ symbol: "X", secType: "STK" }]);
  });

  it("carries a decimal-string strike through verbatim in a match", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        supported: true,
        matches: [
          {
            symbol: "SPX",
            secType: "OPT",
            exchange: "CBOE",
            currency: "USD",
            strike: "5000.00",
            right: "C",
          },
        ],
      }),
    );

    const result = await searchMarketDataSymbols("ib-1", {
      query: "SPX",
      secType: "OPT",
    });

    expect(result.matches[0].strike).toBe("5000.00");
  });

  it("omits strike from a match when the wire value is a number", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        supported: true,
        matches: [
          {
            symbol: "SPX",
            secType: "OPT",
            exchange: "CBOE",
            currency: "USD",
            strike: 5000,
            right: "C",
          },
        ],
      }),
    );

    const result = await searchMarketDataSymbols("ib-1", {
      query: "SPX",
      secType: "OPT",
    });

    // A numeric strike must never be coerced to string (lossy for decimals
    // like 5000.00 → "5000"). The field must be absent on the normalized
    // match instead.
    expect(result.matches[0].strike).toBeUndefined();
  });
});

describe("verifyMarketDataSymbol", () => {
  it("normalizes provider details for an existing symbol", async () => {
    vi.mocked(fetch).mockResolvedValue(
      jsonResponse({
        verification: {
          supported: true,
          exists: true,
          details: "BTC DIGITAL LTD, Common Stock",
        },
      }),
    );

    const result = await verifyMarketDataSymbol("finnhub-1", "BTC");

    expect(fetch).toHaveBeenCalledWith(
      "/app/api/v1/market-data/instances/finnhub-1/verify-symbol",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ externalSymbol: "BTC" }),
      }),
    );
    expect(result).toEqual({
      supported: true,
      exists: true,
      details: "BTC DIGITAL LTD, Common Stock",
    });
  });
});
