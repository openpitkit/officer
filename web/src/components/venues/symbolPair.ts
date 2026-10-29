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

/** Base and quote asset codes of a currency pair. */
export interface SymbolPair {
  base: string;
  quote: string;
}

/** A venue symbol match that may state its own base and quote assets. */
export interface SymbolPairMatch {
  symbol: string;
  baseAsset?: string;
  quoteAsset?: string;
}

/**
 * Drops an exchange-qualified prefix ("BINANCE:ETHUSDT" -> "ETHUSDT") so only
 * the pair itself remains.
 */
export function symbolPairSource(symbol: string): string {
  const colon = symbol.lastIndexOf(":");
  return colon >= 0 ? symbol.slice(colon + 1) : symbol;
}

/**
 * Splits a separated pair ("BTC/USD", "BTC_USD", "BTC-USD", optionally
 * "EXCHANGE:"-prefixed) into upper-case codes. Returns null unless exactly one
 * separator divides two non-empty codes: a compact symbol ("ETHUSDT", "GBTC")
 * is never guessed apart.
 */
export function splitSymbolPair(symbol: string): SymbolPair | null {
  const parts = symbolPairSource(symbol)
    .split(/[/_-]/u)
    .map((part) => part.trim());
  if (parts.length !== 2 || !parts[0] || !parts[1]) {
    return null;
  }
  return { base: parts[0].toUpperCase(), quote: parts[1].toUpperCase() };
}

/**
 * Resolves the pair a picked venue match stands for: the assets the venue
 * states first, then a separator split of the symbol. Otherwise the symbol
 * without its exchange prefix is the base and the quote stays empty, so the
 * operator has to name it.
 */
export function venueSymbolPair(match: SymbolPairMatch): SymbolPair {
  if (match.baseAsset && match.quoteAsset) {
    return { base: match.baseAsset, quote: match.quoteAsset };
  }
  return (
    splitSymbolPair(match.symbol) ?? {
      base: symbolPairSource(match.symbol),
      quote: "",
    }
  );
}
