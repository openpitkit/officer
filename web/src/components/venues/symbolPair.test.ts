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

import { describe, expect, it } from "vitest";
import {
  splitSymbolPair,
  symbolPairSource,
  venueSymbolPair,
} from "@/components/venues";

describe("symbolPairSource", () => {
  it("drops an exchange prefix", () => {
    expect(symbolPairSource("BINANCE:ETHUSDT")).toBe("ETHUSDT");
    expect(symbolPairSource("ETHUSDT")).toBe("ETHUSDT");
  });
});

describe("splitSymbolPair", () => {
  it.each([
    ["BTC/USD", "BTC", "USD"],
    ["eth_usdt", "ETH", "USDT"],
    ["ETH-USDT", "ETH", "USDT"],
    ["COINBASE:BTC-USD", "BTC", "USD"],
    [" eth / usdt ", "ETH", "USDT"],
  ])("splits %s at its separator", (symbol, base, quote) => {
    expect(splitSymbolPair(symbol)).toEqual({ base, quote });
  });

  it.each([
    "ETHUSDT",
    "GBTC",
    "BTC/USD/X",
    "BTC/",
    "/USD",
    "BTC / ",
    "BTC USD",
    "",
  ])("leaves %j unsplit", (symbol) => {
    expect(splitSymbolPair(symbol)).toBeNull();
  });
});

describe("venueSymbolPair", () => {
  it("takes the assets the venue states first", () => {
    expect(
      venueSymbolPair({
        symbol: "BINANCE:ETHUSDT",
        baseAsset: "ETH",
        quoteAsset: "USDT",
      }),
    ).toEqual({ base: "ETH", quote: "USDT" });
  });

  it("splits a separated symbol without stated assets", () => {
    expect(venueSymbolPair({ symbol: "BTC/USD" })).toEqual({
      base: "BTC",
      quote: "USD",
    });
  });

  it("ignores half a stated pair", () => {
    expect(venueSymbolPair({ symbol: "ETHUSDT", baseAsset: "ETH" })).toEqual({
      base: "ETHUSDT",
      quote: "",
    });
  });

  it.each(["ETHUSDT", "GBTC", "AAPL"])(
    "keeps %s whole with an empty quote",
    (symbol) => {
      expect(venueSymbolPair({ symbol })).toEqual({ base: symbol, quote: "" });
    },
  );

  it("drops the exchange prefix from an unsplit base", () => {
    expect(venueSymbolPair({ symbol: "BINANCE:ETHUSDT" })).toEqual({
      base: "ETHUSDT",
      quote: "",
    });
  });
});
