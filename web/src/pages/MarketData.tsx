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

import {
  ChevronDown,
  ChevronUp,
  ExternalLink,
  Logs,
  Plus,
  RefreshCw,
  RotateCcw,
  Send,
  Settings,
  Trash2,
  TriangleAlert,
} from "lucide-react";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import type {
  MarketDataDiagnostic,
  MarketDataInstance,
  MarketDataProvider,
  MarketDataSymbolMatch,
  MarketDataSymbolVerification,
} from "@/api/types";
import { useMarketData } from "@/api/useMarketData";
import { validateAsset } from "@/api/validate";
import { Page } from "@/components/Page";
import {
  ErrorBanner,
  ErrorState,
  StaleState,
  TableSkeleton,
} from "@/components/PageStates";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  AddProviderButton,
  DeleteInstrumentDialog,
  InstrumentDraftRow,
  InstrumentsTable,
  ProviderGuideDialog,
  SymbolSearchPanel,
  useSymbolSearch,
  useRowVerification,
  VerifyResult,
  type FailedVerificationOutcome,
  VenueCardHeader,
} from "@/components/venues";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { formatCompactDuration, formatDateTime } from "@/i18n/format";
import { useOfficerApi, type MarketDataSymbolSearchInput } from "@/framework";
import {
  isDecimalString,
  isOptionalPositiveDecimalString,
} from "@/lib/numberStep";

interface InstanceForm {
  provider: string;
  label: string;
  credentials: string;
  enabled: boolean;
}

interface SettingsForm {
  label: string;
  credentials: string;
}

type ProviderSettingsDraft = Record<string, string>;

interface InstrumentDraft {
  externalSymbol: string;
  baseAsset: string;
  quoteAsset: string;
  manualPrice: string;
  enabled: boolean;
}

interface DeleteInstrumentTarget {
  instance: MarketDataInstance;
  externalSymbol: string;
  ib: boolean;
}

const emptyInstrument: InstrumentDraft = {
  externalSymbol: "",
  baseAsset: "",
  quoteAsset: "USD",
  manualPrice: "",
  enabled: true,
};

function hasNonZeroDecimal(value?: string): boolean {
  const trimmed = value?.trim() ?? "";
  return trimmed !== "" && !/^0+(?:\.0+)?$/u.test(trimmed);
}

function isOptionalDecimal(value?: string): boolean {
  return isDecimalString(value ?? "");
}

function isOptionalInt64(value?: string): boolean {
  const trimmed = value?.trim() ?? "";
  if (trimmed === "") {
    return true;
  }
  if (!/^[+-]?\d+$/u.test(trimmed)) {
    return false;
  }
  const parsed = BigInt(trimmed);
  return parsed >= -9223372036854775808n && parsed <= 9223372036854775807n;
}

function isOptionalJSONInt64(value?: string): boolean {
  const trimmed = value?.trim() ?? "";
  if (trimmed === "") {
    return true;
  }
  if (!/^\+?\d+$/u.test(trimmed) || !isOptionalInt64(trimmed)) {
    return false;
  }
  return Number.isSafeInteger(Number(trimmed));
}

function isInstrumentDraftValid(
  draft: InstrumentDraft,
  isManual: boolean,
): boolean {
  return (
    draft.externalSymbol.trim() !== "" &&
    validateAsset(draft.baseAsset.trim()) === null &&
    validateAsset(draft.quoteAsset.trim()) === null &&
    (!isManual || isOptionalDecimal(draft.manualPrice))
  );
}

function isIBContractNumericValid(contract: IBContract): boolean {
  return (
    isOptionalInt64(contract.conId) &&
    isOptionalDecimal(contract.strike) &&
    isOptionalPositiveDecimalString(contract.multiplier ?? "")
  );
}

// Structured IB contract carried alongside the instrument draft. Mirrors the
// backend ibContractConfig shape persisted in the instance credentials JSON
// as the per-instrument contracts[external] override.
interface IBContract {
  symbol?: string;
  secType?: string;
  lastTradeDateOrContractMonth?: string;
  right?: string;
  multiplier?: string;
  exchange?: string;
  primaryExchange?: string;
  currency?: string;
  localSymbol?: string;
  tradingClass?: string;
  conId?: string;
  strike?: string;
  includeExpired?: boolean;
}

interface PairUsage {
  instanceExternalId: string;
  instanceLabel: string;
  providerType: string;
  externalSymbol: string;
}

type PairUsageMap = Record<string, PairUsage[]>;
type DiagnosticAction = MarketDataDiagnostic["actions"][number];

// secType set surfaced in the IB contract editor.
const IB_SEC_TYPES = ["STK", "CASH", "CRYPTO", "FUT", "IND", "OPT"] as const;

// Exchange/contract defaults applied when the operator switches secType. Keys
// not present here are cleared so a prior secType's exchange never leaks.
const IB_SEC_TYPE_DEFAULTS: Record<string, Partial<IBContract>> = {
  STK: { exchange: "SMART", primaryExchange: "" },
  CASH: { exchange: "IDEALPRO" },
  CRYPTO: { exchange: "PAXOS" },
  IND: { exchange: "" },
  FUT: { exchange: "" },
  OPT: { exchange: "", right: "" },
};

function newEmptyIBContract(): IBContract {
  return {
    secType: "STK",
    exchange: "SMART",
  };
}

// Provider type whose instruments carry an operator-set manual mark price. Only
// this (bring-your-own / manual) provider exposes the price input; streaming
// providers get their marks from the source.
const MANUAL_PROVIDER = "byo";
const IB_PROVIDER = "ib";
const BINANCE_PROVIDER = "binance";
const KRAKEN_PROVIDER = "kraken";
const COINBASE_PROVIDER = "coinbase";
const ALPACA_PROVIDER = "alpaca";
const OKX_PROVIDER = "okx";
const BYBIT_PROVIDER = "bybit";
const OANDA_PROVIDER = "oanda";
const FINNHUB_PROVIDER = "finnhub";
const MOCK_PROVIDER = "mock";

const IB_SITE_URL = "https://www.interactivebrokers.com";
const IB_DOCS_URL =
  "https://interactivebrokers.github.io/tws-api/md_request.html";
const BINANCE_DOCS_URL =
  "https://developers.binance.com/docs/binance-spot-api-docs/web-socket-streams";
const BINANCE_SYMBOLS_URL =
  "https://data-api.binance.vision/api/v3/exchangeInfo";
const BINANCE_SITE_URL = "https://www.binance.com";
const KRAKEN_SITE_URL = "https://www.kraken.com";
const KRAKEN_DOCS_URL =
  "https://docs.kraken.com/exchange/api-reference/spot-websocket-v2/ticker";
const KRAKEN_SYMBOLS_URL =
  "https://api.kraken.com/0/public/AssetPairs?assetVersion=1";
const COINBASE_SITE_URL = "https://www.coinbase.com";
const COINBASE_DOCS_URL =
  "https://docs.cdp.coinbase.com/exchange/websocket-feed/channels";
const COINBASE_SYMBOLS_URL = "https://api.exchange.coinbase.com/products";
const ALPACA_SITE_URL = "https://alpaca.markets";
const ALPACA_DOCS_URL =
  "https://docs.alpaca.markets/docs/real-time-stock-pricing-data";
const ALPACA_SYMBOLS_URL =
  "https://docs.alpaca.markets/us/docs/working-with-assets";
const ALPACA_DASHBOARD_URL =
  "https://app.alpaca.markets/user/profile#manage-accounts";
const OKX_SITE_URL = "https://www.okx.com";
const OKX_DOCS_URL =
  "https://www.okx.com/docs-v5/en/#websocket-api-public-channel-tickers-channel";
const OKX_SYMBOLS_URL =
  "https://www.okx.com/api/v5/public/instruments?instType=SPOT";
const BYBIT_SITE_URL = "https://www.bybit.com";
const BYBIT_DOCS_URL =
  "https://bybit-exchange.github.io/docs/v5/websocket/public/ticker";
const BYBIT_SYMBOLS_URL =
  "https://api.bybit.com/v5/market/instruments-info?category=spot";
const OANDA_SITE_URL = "https://www.oanda.com";
const OANDA_DOCS_URL = "https://developer.oanda.com/rest-live-v20/pricing-ep/";
const OANDA_SYMBOLS_URL =
  "https://developer.oanda.com/rest-live-v20/instrument-df/";
const OANDA_TOKEN_URL = "https://hub.oanda.com/tpa/personal_token";
const FINNHUB_SITE_URL = "https://finnhub.io";
const FINNHUB_DOCS_URL = "https://finnhub.io/docs/api/websocket-trades";
const FINNHUB_SYMBOLS_URL = "https://finnhub.io/docs/api";
const FINNHUB_DASHBOARD_URL = "https://finnhub.io/dashboard";

const MARKET_DATA_DIALOG_CONTENT_CLASS =
  "max-h-[90vh] max-w-[40rem] overflow-y-auto";
const MARKET_DATA_DIAGNOSTICS_PANEL_CLASS = "max-h-96 overflow-y-auto pr-1";

const PROVIDERS_WITH_SETTINGS = new Set([
  IB_PROVIDER,
  ALPACA_PROVIDER,
  BYBIT_PROVIDER,
  OANDA_PROVIDER,
  FINNHUB_PROVIDER,
]);

const PROVIDERS_WITH_CUSTOM_INSTRUMENT_DIALOG = new Set([IB_PROVIDER]);

const PROVIDER_ORDER = [
  IB_PROVIDER,
  BINANCE_PROVIDER,
  KRAKEN_PROVIDER,
  COINBASE_PROVIDER,
  ALPACA_PROVIDER,
  OKX_PROVIDER,
  BYBIT_PROVIDER,
  OANDA_PROVIDER,
  FINNHUB_PROVIDER,
  MANUAL_PROVIDER,
  MOCK_PROVIDER,
];
const FALLBACK_PROVIDERS: MarketDataProvider[] = [
  { type: IB_PROVIDER, title: "Interactive Brokers" },
  { type: BINANCE_PROVIDER, title: "Binance" },
  { type: KRAKEN_PROVIDER, title: "Kraken" },
  { type: COINBASE_PROVIDER, title: "Coinbase" },
  { type: ALPACA_PROVIDER, title: "Alpaca" },
  { type: OKX_PROVIDER, title: "OKX" },
  { type: BYBIT_PROVIDER, title: "Bybit" },
  { type: OANDA_PROVIDER, title: "OANDA" },
  { type: FINNHUB_PROVIDER, title: "Finnhub" },
  { type: MANUAL_PROVIDER, title: "BYO" },
  { type: MOCK_PROVIDER, title: "Mock" },
];

const PROVIDER_LINKS: Record<
  string,
  { siteUrl?: string; docsUrl?: string; symbolsUrl?: string }
> = {
  [IB_PROVIDER]: { siteUrl: IB_SITE_URL, docsUrl: IB_DOCS_URL },
  [BINANCE_PROVIDER]: {
    siteUrl: BINANCE_SITE_URL,
    docsUrl: BINANCE_DOCS_URL,
    symbolsUrl: BINANCE_SYMBOLS_URL,
  },
  [KRAKEN_PROVIDER]: {
    siteUrl: KRAKEN_SITE_URL,
    docsUrl: KRAKEN_DOCS_URL,
    symbolsUrl: KRAKEN_SYMBOLS_URL,
  },
  [COINBASE_PROVIDER]: {
    siteUrl: COINBASE_SITE_URL,
    docsUrl: COINBASE_DOCS_URL,
    symbolsUrl: COINBASE_SYMBOLS_URL,
  },
  [ALPACA_PROVIDER]: {
    siteUrl: ALPACA_SITE_URL,
    docsUrl: ALPACA_DOCS_URL,
    symbolsUrl: ALPACA_SYMBOLS_URL,
  },
  [OKX_PROVIDER]: {
    siteUrl: OKX_SITE_URL,
    docsUrl: OKX_DOCS_URL,
    symbolsUrl: OKX_SYMBOLS_URL,
  },
  [BYBIT_PROVIDER]: {
    siteUrl: BYBIT_SITE_URL,
    docsUrl: BYBIT_DOCS_URL,
    symbolsUrl: BYBIT_SYMBOLS_URL,
  },
  [OANDA_PROVIDER]: {
    siteUrl: OANDA_SITE_URL,
    docsUrl: OANDA_DOCS_URL,
    symbolsUrl: OANDA_SYMBOLS_URL,
  },
  [FINNHUB_PROVIDER]: {
    siteUrl: FINNHUB_SITE_URL,
    docsUrl: FINNHUB_DOCS_URL,
    symbolsUrl: FINNHUB_SYMBOLS_URL,
  },
};

function providerHasSettings(type: string): boolean {
  return PROVIDERS_WITH_SETTINGS.has(type);
}

function providerUsesCustomInstrumentDialog(type: string): boolean {
  return PROVIDERS_WITH_CUSTOM_INSTRUMENT_DIALOG.has(type);
}

function settingString(
  settings: Record<string, unknown> | undefined,
  key: string,
  fallback = "",
): string {
  const value = settings?.[key];
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "number" && Number.isFinite(value)) {
    return String(value);
  }
  return fallback;
}

function ibMarketDataTypeString(
  settings: Record<string, unknown> | undefined,
): string {
  const value = settings?.marketDataType;
  if (typeof value === "number") {
    if (value === 1) return "live";
    if (value === 2) return "frozen";
    if (value === 3) return "delayed";
    if (value === 4) return "delayed-frozen";
  }
  return settingString(settings, "marketDataType", "live");
}

function initialSettingsDraft(
  type: string,
  instance?: MarketDataInstance,
): ProviderSettingsDraft {
  const settings = instance?.settings;
  if (type === IB_PROVIDER) {
    return {
      host: settingString(settings, "host", "127.0.0.1"),
      port: settingString(settings, "port", "7496"),
      clientId: settingString(settings, "clientId"),
      marketDataType: ibMarketDataTypeString(settings),
    };
  }
  if (type === ALPACA_PROVIDER) {
    return { apiKey: "", apiSecret: "" };
  }
  if (type === BYBIT_PROVIDER) {
    return { category: settingString(settings, "category", "spot") };
  }
  if (type === OANDA_PROVIDER) {
    return {
      token: "",
      accountID: settingString(settings, "accountID"),
      environment: settingString(settings, "environment", "practice"),
    };
  }
  if (type === FINNHUB_PROVIDER) {
    return { token: "" };
  }
  return {};
}

function hasSecret(
  instance: MarketDataInstance | undefined,
  key: string,
): boolean {
  return instance?.secrets?.[key] === true;
}

function providerSettingsReady(
  type: string,
  draft: ProviderSettingsDraft,
  instance?: MarketDataInstance,
): boolean {
  if (type === IB_PROVIDER) {
    const port = Number(draft.port);
    return (
      draft.host?.trim() !== "" &&
      Number.isInteger(port) &&
      port >= 1 &&
      port <= 65535 &&
      isOptionalJSONInt64(draft.clientId)
    );
  }
  if (type === ALPACA_PROVIDER) {
    return (
      (draft.apiKey?.trim() !== "" || hasSecret(instance, "apiKey")) &&
      (draft.apiSecret?.trim() !== "" || hasSecret(instance, "apiSecret"))
    );
  }
  if (type === OANDA_PROVIDER) {
    return (
      (draft.token?.trim() !== "" || hasSecret(instance, "token")) &&
      draft.accountID?.trim() !== ""
    );
  }
  if (type === FINNHUB_PROVIDER) {
    return draft.token?.trim() !== "" || hasSecret(instance, "token");
  }
  return true;
}

function putIfFilled(
  target: Record<string, unknown>,
  key: string,
  value: string | undefined,
) {
  const trimmed = value?.trim() ?? "";
  if (trimmed !== "") {
    target[key] = trimmed;
  }
}

function putNumberIfFilled(
  target: Record<string, unknown>,
  key: string,
  value: string | undefined,
) {
  const trimmed = value?.trim() ?? "";
  if (trimmed !== "") {
    const numeric = Number(trimmed);
    if (Number.isFinite(numeric)) {
      target[key] = numeric;
    }
  }
}

// Reads the persisted per-instrument contracts map from instance settings.
// The map round-trips as a raw nested object through the client normalizer.
function readIBContractsFromInstance(
  instance?: MarketDataInstance,
): Record<string, IBContract> {
  const raw = instance?.settings?.contracts;
  if (raw && typeof raw === "object" && !Array.isArray(raw)) {
    return { ...(raw as Record<string, IBContract>) };
  }
  return {};
}

function buildProviderCredentials(
  type: string,
  draft: ProviderSettingsDraft,
  extra?: { contracts?: Record<string, IBContract> },
): string {
  const body: Record<string, unknown> = {};
  if (type === IB_PROVIDER) {
    putIfFilled(body, "host", draft.host);
    putNumberIfFilled(body, "port", draft.port);
    putNumberIfFilled(body, "clientId", draft.clientId);
    putIfFilled(body, "marketDataType", draft.marketDataType);
    if (extra?.contracts) {
      body.contracts = extra.contracts;
    }
  } else if (type === ALPACA_PROVIDER) {
    putIfFilled(body, "apiKey", draft.apiKey);
    putIfFilled(body, "apiSecret", draft.apiSecret);
  } else if (type === BYBIT_PROVIDER) {
    putIfFilled(body, "category", draft.category);
  } else if (type === OANDA_PROVIDER) {
    putIfFilled(body, "token", draft.token);
    putIfFilled(body, "accountID", draft.accountID);
    putIfFilled(body, "environment", draft.environment);
  } else if (type === FINNHUB_PROVIDER) {
    putIfFilled(body, "token", draft.token);
  }
  if (Object.keys(body).length === 0) {
    return "";
  }
  return JSON.stringify(body);
}

function providerDocsUrl(type: string): string {
  return PROVIDER_LINKS[type]?.docsUrl ?? "";
}

function providerSymbolsUrl(type: string): string {
  return PROVIDER_LINKS[type]?.symbolsUrl ?? "";
}

function providerSiteUrl(type: string): string {
  return PROVIDER_LINKS[type]?.siteUrl ?? "";
}

function sortProviders(providers: MarketDataProvider[]): MarketDataProvider[] {
  return [...providers].sort((a, b) => {
    const ai = PROVIDER_ORDER.indexOf(a.type);
    const bi = PROVIDER_ORDER.indexOf(b.type);
    const aOrder = ai === -1 ? PROVIDER_ORDER.length : ai;
    const bOrder = bi === -1 ? PROVIDER_ORDER.length : bi;
    return aOrder - bOrder || a.title.localeCompare(b.title);
  });
}

function providerTitle(instance: MarketDataInstance): string {
  return instance.label || instance.id;
}

function instrumentPairKey(baseAsset: string, quoteAsset: string): string {
  const base = baseAsset.trim().toUpperCase();
  const quote = quoteAsset.trim().toUpperCase();
  return base && quote ? `${base}/${quote}` : "";
}

function buildPairUsageMap(instances: MarketDataInstance[]): PairUsageMap {
  const map: PairUsageMap = {};
  for (const instance of instances) {
    for (const instrument of instance.instruments) {
      const key = instrumentPairKey(
        instrument.baseAsset,
        instrument.quoteAsset,
      );
      if (!key) {
        continue;
      }
      map[key] ??= [];
      map[key].push({
        instanceExternalId: instance.id,
        instanceLabel: providerTitle(instance),
        providerType: instance.provider,
        externalSymbol: instrument.externalSymbol,
      });
    }
  }
  return map;
}

function pairUsages(
  map: PairUsageMap,
  baseAsset: string,
  quoteAsset: string,
  exclude?: { instanceExternalId: string; externalSymbol: string },
): PairUsage[] {
  const usages = map[instrumentPairKey(baseAsset, quoteAsset)] ?? [];
  if (!exclude) {
    return usages;
  }
  return usages.filter(
    (usage) =>
      usage.instanceExternalId !== exclude.instanceExternalId ||
      usage.externalSymbol !== exclude.externalSymbol,
  );
}

function bestPriceLabel(
  quote: MarketDataInstance["instruments"][number]["quote"],
): string {
  if (!quote) return "";
  return quote.mark || quote.bid || quote.ask;
}

function syntheticPriceLabel(
  quote: MarketDataInstance["instruments"][number]["quote"],
  inverseQuote: MarketDataInstance["instruments"][number]["inverseQuote"],
): string {
  if (!quote || !inverseQuote) return "";
  const pairs = [
    [quote.mark, inverseQuote.mark],
    [quote.bid, inverseQuote.ask],
    [quote.ask, inverseQuote.bid],
  ];
  for (const [received, inverse] of pairs) {
    if (received && inverse) return `${received} -> ${inverse}`;
  }
  return "";
}

function displayedPriceLabel(
  instrument: MarketDataInstance["instruments"][number],
): string {
  const received = bestPriceLabel(instrument.quote);
  if (!received || !instrument.syntheticInverse) return received;
  return (
    syntheticPriceLabel(instrument.quote, instrument.inverseQuote) || received
  );
}

function diagnosticLevelVariant(level: string): "danger" | "warn" | "neutral" {
  if (level === "error") return "danger";
  if (level === "warn") return "warn";
  return "neutral";
}

// Derivative detail line for a resolved FUT/OPT contract: "<expiry> <strike><right>".
// Empty for non-derivatives or when no derivative field resolved.
function ibDerivativeSuffix(match: MarketDataSymbolMatch): string {
  if (match.secType !== "FUT" && match.secType !== "OPT") {
    return "";
  }
  const parts: string[] = [];
  if (match.lastTradeDateOrContractMonth) {
    parts.push(match.lastTradeDateOrContractMonth);
  }
  if (hasNonZeroDecimal(match.strike)) {
    parts.push(`${match.strike}${match.right ?? ""}`);
  } else if (match.right) {
    parts.push(match.right);
  }
  return parts.join(" ");
}

function splitSymbolQueryPair(
  value: string,
): { base: string; quote: string } | null {
  const parts = value
    .trim()
    .split(/[/\\\s_-]+/u)
    .filter(Boolean);
  if (parts.length !== 2) {
    return null;
  }
  return {
    base: parts[0].toUpperCase(),
    quote: parts[1].toUpperCase(),
  };
}

const knownQuoteAssetSuffixes = [
  "USDT",
  "USDC",
  "BUSD",
  "FDUSD",
  "TUSD",
  "USD",
  "EUR",
  "GBP",
  "JPY",
  "CHF",
  "AUD",
  "CAD",
  "BTC",
  "ETH",
  "BNB",
];

function splitCompactSymbolPair(
  symbol: string,
): { base: string; quote: string } | null {
  const upper = symbol.trim().toUpperCase();
  for (const quote of knownQuoteAssetSuffixes) {
    if (upper.length > quote.length && upper.endsWith(quote)) {
      return {
        base: upper.slice(0, -quote.length),
        quote,
      };
    }
  }
  return null;
}

// symbolPairSource strips an exchange-qualified prefix ("BINANCE:ETHUSDT" ->
// "ETHUSDT") so base/quote splitting sees only the trading pair, not the venue.
function symbolPairSource(symbol: string): string {
  const colon = symbol.lastIndexOf(":");
  return colon >= 0 ? symbol.slice(colon + 1) : symbol;
}

function resolvedInstrumentPair(
  match: MarketDataSymbolMatch,
  query: string,
): { base: string; quote: string } {
  const pairSource = symbolPairSource(match.symbol);
  const symbolPair =
    splitSymbolQueryPair(pairSource) || splitCompactSymbolPair(pairSource);
  const queryPair = splitSymbolQueryPair(query);
  return {
    base: symbolPair?.base || pairSource,
    quote: match.currency || symbolPair?.quote || queryPair?.quote || "",
  };
}

function resolvedVenueLabel(match: MarketDataSymbolMatch): string {
  if (
    match.exchange &&
    match.primaryExchange &&
    match.exchange !== match.primaryExchange
  ) {
    return `${match.exchange}/${match.primaryExchange}`;
  }
  return match.exchange || match.primaryExchange || "";
}

function resolvedSymbolMetadata(match: MarketDataSymbolMatch): string {
  const parts = [
    match.secType,
    resolvedVenueLabel(match),
    match.currency,
    ibDerivativeSuffix(match),
  ].filter((part): part is string => Boolean(part));
  return parts.join(" · ");
}

function ibContractVenueLabel(contract: IBContract): string {
  if (
    contract.exchange &&
    contract.primaryExchange &&
    contract.exchange !== contract.primaryExchange
  ) {
    return `${contract.exchange}/${contract.primaryExchange}`;
  }
  return contract.exchange || contract.primaryExchange || "";
}

function ibContractDerivativeSuffix(contract: IBContract): string {
  if (contract.secType !== "FUT" && contract.secType !== "OPT") {
    return "";
  }
  const parts: string[] = [];
  if (contract.lastTradeDateOrContractMonth) {
    parts.push(contract.lastTradeDateOrContractMonth);
  }
  if (hasNonZeroDecimal(contract.strike)) {
    parts.push(`${contract.strike}${contract.right ?? ""}`);
  } else if (contract.right) {
    parts.push(contract.right);
  }
  return parts.join(" ");
}

function ibContractMetadata(contract: IBContract): string {
  const parts = [
    contract.secType,
    ibContractVenueLabel(contract),
    contract.currency,
    ibContractDerivativeSuffix(contract),
    contract.conId ? `conId ${contract.conId}` : "",
    contract.localSymbol ? `local ${contract.localSymbol}` : "",
    contract.tradingClass ? `class ${contract.tradingClass}` : "",
  ].filter(Boolean);
  return parts.join(" · ");
}

// finnhubSymbolVenue surfaces the price source encoded in an exchange-qualified
// Finnhub symbol ("BINANCE:ETHUSDT" -> "BINANCE"). Bare equity tickers carry no
// venue prefix and report nothing.
function finnhubSymbolVenue(symbol: string): string {
  const colon = symbol.indexOf(":");
  return colon > 0 ? symbol.slice(0, colon).trim() : "";
}

function instrumentMetadata(
  instance: MarketDataInstance,
  externalSymbol: string,
): string {
  if (instance.provider === IB_PROVIDER) {
    const contract =
      readIBContractsFromInstance(instance)[externalSymbol] ?? {};
    return ibContractMetadata(contract);
  }
  if (instance.provider === FINNHUB_PROVIDER) {
    return finnhubSymbolVenue(externalSymbol);
  }
  return "";
}

// instrumentDisplaySymbol is the bold ticker shown for an instrument. For an
// exchange-qualified Finnhub symbol it drops the venue prefix
// ("BINANCE:ETHUSDT" -> "ETHUSDT") so the pair reads cleanly; the venue moves to
// the grey metadata line. Other providers show the external symbol verbatim.
function instrumentDisplaySymbol(
  instance: MarketDataInstance,
  externalSymbol: string,
): string {
  if (instance.provider === FINNHUB_PROVIDER) {
    return symbolPairSource(externalSymbol);
  }
  return externalSymbol;
}

function resolvedSymbolSearchText(match: MarketDataSymbolMatch): string {
  return [
    match.symbol,
    match.name,
    match.secType,
    match.exchange,
    match.primaryExchange,
    match.currency,
    ibDerivativeSuffix(match),
  ]
    .filter((part): part is string => Boolean(part))
    .join(" ")
    .toLowerCase();
}

// Builds the patch for a secType switch: the new secType and its matching
// exchange/contract defaults, with the previous secType's conditional keys
// cleared (set to undefined) so they never leak across types. Callers merge
// this patch into the current contract.
// eslint-disable-next-line react-refresh/only-export-components
export function ibSecTypePatch(
  secType: string,
  currentCurrency?: string,
): Partial<IBContract> {
  return {
    secType,
    exchange: undefined,
    primaryExchange: undefined,
    lastTradeDateOrContractMonth: undefined,
    strike: undefined,
    right: undefined,
    ...(IB_SEC_TYPE_DEFAULTS[secType] ?? {}),
    currency: currentCurrency,
  };
}

// The set of conditional fields each secType reveals.
// eslint-disable-next-line react-refresh/only-export-components
export function ibSecTypeFields(secType: string | undefined): {
  primaryExchange: boolean;
  exchange: boolean;
  lastTradeDate: boolean;
  strike: boolean;
  right: boolean;
} {
  return {
    primaryExchange: secType === "STK",
    exchange:
      secType === "STK" ||
      secType === "CASH" ||
      secType === "CRYPTO" ||
      secType === "IND" ||
      secType === "FUT" ||
      secType === "OPT",
    lastTradeDate: secType === "FUT" || secType === "OPT",
    strike: secType === "OPT",
    right: secType === "OPT",
  };
}

// Shared IB contract editor: a secType selector plus the conditional fields
// that secType reveals. `idPrefix` keeps input ids unique when several editors
// render on the same page (per-instance instrument editor + settings dialog).
function IBContractFields({
  contract,
  busy,
  idPrefix,
  onChange,
}: {
  contract: IBContract;
  busy: boolean;
  idPrefix: string;
  onChange: (patch: Partial<IBContract>) => void;
}) {
  const { t } = useTranslation("marketData");
  const fields = ibSecTypeFields(contract.secType);
  const currencyMissing = !contract.currency?.trim();
  const currencyErrorID = `${idPrefix}-currency-error`;
  return (
    <div className="grid gap-3 md:grid-cols-2">
      <div className="space-y-2">
        <Label>{t("settings.secType")}</Label>
        <Select
          value={contract.secType ?? "STK"}
          onValueChange={(value) =>
            onChange(ibSecTypePatch(value, contract.currency))
          }
          disabled={busy}
        >
          <SelectTrigger aria-label={t("settings.secType")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {IB_SEC_TYPES.map((secType) => (
              <SelectItem key={secType} value={secType}>
                {t(`settings.secTypes.${secType}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="space-y-2">
        <Label htmlFor={`${idPrefix}-currency`}>{t("settings.currency")}</Label>
        <Input
          id={`${idPrefix}-currency`}
          value={contract.currency ?? ""}
          placeholder="USD"
          required
          aria-describedby={currencyMissing ? currencyErrorID : undefined}
          aria-invalid={currencyMissing}
          onChange={(e) => onChange({ currency: e.target.value })}
          disabled={busy}
        />
        {currencyMissing && (
          <p id={currencyErrorID} className="text-xs text-[var(--danger)]">
            {t("instrumentDialog.currencyRequired")}
          </p>
        )}
      </div>
      {fields.exchange && (
        <div className="space-y-2">
          <Label htmlFor={`${idPrefix}-exchange`}>
            {t("settings.exchange")}
          </Label>
          <Input
            id={`${idPrefix}-exchange`}
            value={contract.exchange ?? ""}
            onChange={(e) => onChange({ exchange: e.target.value })}
            disabled={busy}
          />
        </div>
      )}
      {fields.primaryExchange && (
        <div className="space-y-2">
          <Label htmlFor={`${idPrefix}-primary-exchange`}>
            {t("settings.primaryExchange")}
          </Label>
          <Input
            id={`${idPrefix}-primary-exchange`}
            value={contract.primaryExchange ?? ""}
            onChange={(e) => onChange({ primaryExchange: e.target.value })}
            disabled={busy}
          />
        </div>
      )}
      {fields.lastTradeDate && (
        <div className="space-y-2">
          <Label htmlFor={`${idPrefix}-last-trade-date`}>
            {t("settings.lastTradeDate")}
          </Label>
          <Input
            id={`${idPrefix}-last-trade-date`}
            value={contract.lastTradeDateOrContractMonth ?? ""}
            placeholder="20251219"
            onChange={(e) =>
              onChange({ lastTradeDateOrContractMonth: e.target.value })
            }
            disabled={busy}
          />
        </div>
      )}
      {fields.strike && (
        <div className="space-y-2">
          <Label htmlFor={`${idPrefix}-strike`}>{t("settings.strike")}</Label>
          <Input
            id={`${idPrefix}-strike`}
            type="text"
            inputMode="decimal"
            value={contract.strike ?? ""}
            onChange={(e) => {
              const value = e.target.value.trim();
              onChange({ strike: value === "" ? undefined : value });
            }}
            disabled={busy}
          />
        </div>
      )}
      {fields.right && (
        <div className="space-y-2">
          <Label>{t("settings.right")}</Label>
          <Select
            value={contract.right ?? ""}
            onValueChange={(value) => onChange({ right: value })}
            disabled={busy}
          >
            <SelectTrigger aria-label={t("settings.right")}>
              <SelectValue placeholder={t("settings.right")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="C">{t("settings.rights.C")}</SelectItem>
              <SelectItem value="P">{t("settings.rights.P")}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      )}
    </div>
  );
}

function IBContractIdentityFields({
  contract,
  busy,
  idPrefix,
  onChange,
}: {
  contract: IBContract;
  busy: boolean;
  idPrefix: string;
  onChange: (patch: Partial<IBContract>) => void;
}) {
  const { t } = useTranslation("marketData");
  const updateText = (value: string): string | undefined => {
    const trimmed = value.trim();
    return trimmed === "" ? undefined : trimmed;
  };
  return (
    <div className="grid gap-3 md:grid-cols-3">
      <div className="space-y-2">
        <Label htmlFor={`${idPrefix}-symbol`}>
          {t("settings.contractSymbol")}
        </Label>
        <Input
          id={`${idPrefix}-symbol`}
          value={contract.symbol ?? ""}
          onChange={(e) => onChange({ symbol: e.target.value })}
          disabled={busy}
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor={`${idPrefix}-con-id`}>{t("settings.conId")}</Label>
        <Input
          id={`${idPrefix}-con-id`}
          type="text"
          inputMode="numeric"
          value={contract.conId ?? ""}
          onChange={(e) => onChange({ conId: updateText(e.target.value) })}
          disabled={busy}
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor={`${idPrefix}-local-symbol`}>
          {t("settings.localSymbol")}
        </Label>
        <Input
          id={`${idPrefix}-local-symbol`}
          value={contract.localSymbol ?? ""}
          onChange={(e) => onChange({ localSymbol: e.target.value })}
          disabled={busy}
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor={`${idPrefix}-trading-class`}>
          {t("settings.tradingClass")}
        </Label>
        <Input
          id={`${idPrefix}-trading-class`}
          value={contract.tradingClass ?? ""}
          onChange={(e) => onChange({ tradingClass: e.target.value })}
          disabled={busy}
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor={`${idPrefix}-multiplier`}>
          {t("settings.multiplier")}
        </Label>
        <Input
          id={`${idPrefix}-multiplier`}
          value={contract.multiplier ?? ""}
          onChange={(e) => onChange({ multiplier: e.target.value })}
          disabled={busy}
        />
      </div>
      <label className="flex items-center gap-2 pt-7 text-sm text-text">
        <input
          type="checkbox"
          checked={contract.includeExpired ?? false}
          onChange={(e) => onChange({ includeExpired: e.target.checked })}
          disabled={busy}
          className="h-4 w-4 accent-[var(--accent)]"
        />
        {t("settings.includeExpired")}
      </label>
    </div>
  );
}

function IBContractEditor({
  contract,
  busy,
  idPrefix,
  onChange,
}: {
  contract: IBContract;
  busy: boolean;
  idPrefix: string;
  onChange: (patch: Partial<IBContract>) => void;
}) {
  return (
    <div className="space-y-3">
      <IBContractFields
        contract={contract}
        busy={busy}
        idPrefix={idPrefix}
        onChange={onChange}
      />
      <IBContractIdentityFields
        contract={contract}
        busy={busy}
        idPrefix={`${idPrefix}-identity`}
        onChange={onChange}
      />
    </div>
  );
}

function ProviderSettingsFields({
  providerType,
  draft,
  instance,
  busy,
  onChange,
}: {
  providerType: string;
  draft: ProviderSettingsDraft;
  instance?: MarketDataInstance;
  busy: boolean;
  onChange: (patch: ProviderSettingsDraft) => void;
}) {
  const { t } = useTranslation("marketData");
  const set = (key: string, value: string) => onChange({ [key]: value });
  if (providerType === IB_PROVIDER) {
    return (
      <div className="space-y-4">
        <div className="grid gap-3 md:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="md-settings-host">{t("settings.host")}</Label>
            <Input
              id="md-settings-host"
              value={draft.host ?? ""}
              onChange={(e) => set("host", e.target.value)}
              disabled={busy}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="md-settings-port">{t("settings.port")}</Label>
            <Input
              id="md-settings-port"
              type="number"
              inputMode="numeric"
              min={1}
              max={65535}
              value={draft.port ?? ""}
              onChange={(e) => set("port", e.target.value)}
              disabled={busy}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="md-settings-client-id">
              {t("settings.clientId")}
            </Label>
            <Input
              id="md-settings-client-id"
              type="number"
              inputMode="numeric"
              value={draft.clientId ?? ""}
              placeholder={t("settings.autoClientId")}
              onChange={(e) => set("clientId", e.target.value)}
              disabled={busy}
            />
          </div>
          <div className="space-y-2">
            <Label>{t("settings.marketDataType")}</Label>
            <Select
              value={draft.marketDataType ?? "live"}
              onValueChange={(value) => set("marketDataType", value)}
              disabled={busy}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="live">
                  {t("settings.marketData.live")}
                </SelectItem>
                <SelectItem value="frozen">
                  {t("settings.marketData.frozen")}
                </SelectItem>
                <SelectItem value="delayed">
                  {t("settings.marketData.delayed")}
                </SelectItem>
                <SelectItem value="delayed-frozen">
                  {t("settings.marketData.delayedFrozen")}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
      </div>
    );
  }
  if (providerType === ALPACA_PROVIDER) {
    return (
      <div className="space-y-3">
        <Button asChild variant="outline" size="sm" className="w-fit">
          <a
            href={ALPACA_DASHBOARD_URL}
            target="_blank"
            rel="noopener noreferrer"
          >
            <ExternalLink className="h-3 w-3" />
            {t("settings.getApiKeys")}
          </a>
        </Button>
        <div className="grid gap-3 md:grid-cols-2">
          <SecretInput
            id="md-settings-api-key"
            label={t("settings.apiKey")}
            value={draft.apiKey ?? ""}
            hasStored={hasSecret(instance, "apiKey")}
            disabled={busy}
            onChange={(value) => set("apiKey", value)}
          />
          <SecretInput
            id="md-settings-api-secret"
            label={t("settings.apiSecret")}
            value={draft.apiSecret ?? ""}
            hasStored={hasSecret(instance, "apiSecret")}
            disabled={busy}
            onChange={(value) => set("apiSecret", value)}
          />
        </div>
      </div>
    );
  }
  if (providerType === BYBIT_PROVIDER) {
    return (
      <div className="space-y-2">
        <Label>{t("settings.category")}</Label>
        <Select
          value={draft.category ?? "spot"}
          onValueChange={(value) => set("category", value)}
          disabled={busy}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="spot">
              {t("settings.categories.spot")}
            </SelectItem>
            <SelectItem value="linear">
              {t("settings.categories.linear")}
            </SelectItem>
            <SelectItem value="inverse">
              {t("settings.categories.inverse")}
            </SelectItem>
            <SelectItem value="option">
              {t("settings.categories.option")}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
    );
  }
  if (providerType === OANDA_PROVIDER) {
    return (
      <div className="space-y-3">
        <Button asChild variant="outline" size="sm" className="w-fit">
          <a href={OANDA_TOKEN_URL} target="_blank" rel="noopener noreferrer">
            <ExternalLink className="h-3 w-3" />
            {t("settings.getApiToken")}
          </a>
        </Button>
        <div className="grid gap-3 md:grid-cols-2">
          <SecretInput
            id="md-settings-token"
            label={t("settings.token")}
            value={draft.token ?? ""}
            hasStored={hasSecret(instance, "token")}
            disabled={busy}
            onChange={(value) => set("token", value)}
          />
          <div className="space-y-2">
            <Label htmlFor="md-settings-account-id">
              {t("settings.accountID")}
            </Label>
            <Input
              id="md-settings-account-id"
              value={draft.accountID ?? ""}
              onChange={(e) => set("accountID", e.target.value)}
              disabled={busy}
            />
          </div>
          <div className="space-y-2">
            <Label>{t("settings.environment")}</Label>
            <Select
              value={draft.environment ?? "practice"}
              onValueChange={(value) => set("environment", value)}
              disabled={busy}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="practice">
                  {t("settings.environments.practice")}
                </SelectItem>
                <SelectItem value="live">
                  {t("settings.environments.live")}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
      </div>
    );
  }
  if (providerType === FINNHUB_PROVIDER) {
    return (
      <div className="space-y-3">
        <Button asChild variant="outline" size="sm" className="w-fit">
          <a
            href={FINNHUB_DASHBOARD_URL}
            target="_blank"
            rel="noopener noreferrer"
          >
            <ExternalLink className="h-3 w-3" />
            {t("settings.getApiToken")}
          </a>
        </Button>
        <SecretInput
          id="md-settings-finnhub-token"
          label={t("settings.token")}
          value={draft.token ?? ""}
          hasStored={hasSecret(instance, "token")}
          disabled={busy}
          onChange={(value) => set("token", value)}
        />
      </div>
    );
  }
  return null;
}

function SecretInput({
  id,
  label,
  value,
  hasStored,
  disabled,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  hasStored: boolean;
  disabled: boolean;
  onChange: (value: string) => void;
}) {
  const { t } = useTranslation("marketData");
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type="password"
        value={value}
        placeholder={hasStored ? t("settings.keepStoredSecret") : ""}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
      />
    </div>
  );
}

function StateToggleField({
  id,
  enabled,
  busy,
  onToggle,
}: {
  id: string;
  enabled: boolean;
  busy: boolean;
  onToggle: () => void;
}) {
  const { t } = useTranslation("marketData");
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{t("venues:table.state")}</Label>
      <Button
        id={id}
        type="button"
        variant={enabled ? "default" : "outline"}
        className="h-[var(--dens-field-h)] w-full justify-center px-[var(--dens-field-px)] text-[length:var(--dens-field-fz)]"
        onClick={onToggle}
        disabled={busy}
      >
        {enabled ? t("venues:state.enabled") : t("venues:state.disabled")}
      </Button>
    </div>
  );
}

export function CreateInstanceDialog({
  provider,
  existingLabels,
  busy,
  onOpenChange,
  onCreate,
}: {
  provider: MarketDataProvider | null;
  existingLabels: string[];
  busy: boolean;
  onOpenChange: (open: boolean) => void;
  onCreate: (form: InstanceForm) => Promise<boolean>;
}) {
  const { t } = useTranslation("marketData");
  const providerLabel = provider
    ? t(`providers.${provider.type}.title`, { defaultValue: provider.title })
    : "";
  const [draft, setDraft] = useState({
    label: providerLabel,
    enabled: true,
  });
  const [settingsDraft, setSettingsDraft] = useState<ProviderSettingsDraft>(
    provider ? initialSettingsDraft(provider.type) : {},
  );
  const normalizedLabel = draft.label.trim();
  const labelTaken =
    normalizedLabel !== "" &&
    existingLabels.some(
      (label) => label.trim().toLowerCase() === normalizedLabel.toLowerCase(),
    );

  const open = provider !== null;
  const close = () => {
    onOpenChange(false);
  };
  const submit = async () => {
    if (!provider) {
      return;
    }
    if (
      normalizedLabel === "" ||
      labelTaken ||
      !providerSettingsReady(provider.type, settingsDraft)
    ) {
      return;
    }
    const ok = await onCreate({
      provider: provider.type,
      label: normalizedLabel,
      credentials: buildProviderCredentials(provider.type, settingsDraft),
      enabled: draft.enabled,
    });
    if (ok) {
      close();
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (next) {
          onOpenChange(true);
        } else {
          close();
        }
      }}
    >
      <DialogContent
        className={
          provider?.type === IB_PROVIDER
            ? MARKET_DATA_DIALOG_CONTENT_CLASS
            : undefined
        }
      >
        <DialogHeader>
          <DialogTitle>
            {provider
              ? t("create.title", {
                  provider: t(`providers.${provider.type}.title`, {
                    defaultValue: provider.title,
                  }),
                })
              : t("actions.addInstance")}
          </DialogTitle>
          <DialogDescription>
            {provider
              ? t(`providers.${provider.type}.createHint`, {
                  defaultValue: t("create.defaultHint"),
                })
              : t("create.defaultHint")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="md-create-label">{t("instance.label")}</Label>
            <Input
              id="md-create-label"
              value={draft.label}
              autoFocus
              onChange={(e) =>
                setDraft((prev) => ({ ...prev, label: e.target.value }))
              }
              disabled={busy}
            />
            {labelTaken && (
              <p className="text-xs text-[var(--danger)]">
                {t("create.duplicateLabel")}
              </p>
            )}
          </div>
          <StateToggleField
            id="md-create-enabled"
            enabled={draft.enabled}
            busy={busy}
            onToggle={() =>
              setDraft((prev) => ({ ...prev, enabled: !prev.enabled }))
            }
          />
          {provider && providerHasSettings(provider.type) && (
            <div className="space-y-3">
              <p className="text-xs font-medium uppercase tracking-wide text-muted">
                {t("settings.title")}
              </p>
              <ProviderSettingsFields
                providerType={provider.type}
                draft={settingsDraft}
                busy={busy}
                onChange={(patch) =>
                  setSettingsDraft((prev) => ({ ...prev, ...patch }))
                }
              />
            </div>
          )}
        </div>
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={close}
            disabled={busy}
          >
            {t("actions.cancel")}
          </Button>
          <Button
            type="button"
            onClick={() => {
              void submit();
            }}
            disabled={
              busy ||
              normalizedLabel === "" ||
              labelTaken ||
              (provider
                ? !providerSettingsReady(provider.type, settingsDraft)
                : false)
            }
          >
            <Plus />
            {t("actions.addInstance")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DiagnosticGroup({
  title,
  items,
}: {
  title: string;
  items: MarketDataDiagnostic[];
}) {
  const { t } = useTranslation("marketData");
  if (items.length === 0) return null;
  return (
    <div>
      <p className="mb-1.5 text-xs font-medium uppercase tracking-wide text-muted">
        {title}
      </p>
      <div className="space-y-2">
        {items.map((d) => (
          <div
            key={[
              d.code,
              d.kind,
              d.level,
              d.instrument ?? "",
              d.title,
              d.detail,
              d.at,
            ].join("|")}
            className="rounded-card border border-border bg-surface p-3 text-sm"
          >
            <div className="flex flex-wrap items-start gap-2">
              <Badge
                variant={diagnosticLevelVariant(d.level)}
                className="mt-px shrink-0"
              >
                {d.level === "error"
                  ? t("diagnostics.levelError")
                  : d.level === "warn"
                    ? t("diagnostics.levelWarn")
                    : t("diagnostics.levelInfo")}
              </Badge>
              <span className="font-semibold text-text">{d.title}</span>
              {d.instrument && (
                <Badge variant="neutral" className="shrink-0 font-mono text-xs">
                  {d.instrument}
                </Badge>
              )}
              <span className="nums ml-auto shrink-0 text-xs text-muted">
                {formatDateTime(d.at)}
              </span>
            </div>
            <p className="mt-1.5 text-text">{d.detail}</p>
            {d.remediation && (
              <p className="mt-1.5 text-xs text-muted">
                <span className="font-medium">{t("diagnostics.howToFix")}</span>{" "}
                {d.remediation}
              </p>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

function resolvedMatchKey(match: MarketDataSymbolMatch): string {
  return [
    match.symbol,
    match.secType,
    match.exchange ?? "",
    match.primaryExchange ?? "",
    match.currency ?? "",
    match.conId ?? "",
    match.lastTradeDateOrContractMonth ?? "",
    match.strike ?? "",
    match.right ?? "",
    match.localSymbol ?? "",
    match.tradingClass ?? "",
  ].join("|");
}

function diagnosticActionKey(action: DiagnosticAction): string {
  return `${action.type}:${action.target ?? ""}`;
}

function diagnosticSummaryVariant(
  items: MarketDataDiagnostic[],
): "danger" | "neutral" | "warn" {
  if (items.some((item) => item.level === "error")) {
    return "danger";
  }
  if (items.some((item) => item.level === "warn")) {
    return "warn";
  }
  return "neutral";
}

function diagnosticActions(items: MarketDataDiagnostic[]): DiagnosticAction[] {
  const seen = new Set<string>();
  const actions: DiagnosticAction[] = [];
  for (const item of items) {
    for (const action of item.actions) {
      if (action.type === "remove_instrument" && action.target === undefined) {
        continue;
      }
      const key = diagnosticActionKey(action);
      if (seen.has(key)) {
        continue;
      }
      seen.add(key);
      actions.push(action);
    }
  }
  return actions;
}

function DiagnosticActions({
  actions,
  instance,
  busy,
  onRestart,
  onDeleteInstrument,
}: {
  actions: DiagnosticAction[];
  instance: MarketDataInstance;
  busy: boolean;
  onRestart: () => void;
  onDeleteInstrument: (
    instance: MarketDataInstance,
    externalSymbol: string,
  ) => void;
}) {
  const { t } = useTranslation("marketData");
  return (
    <div
      role="toolbar"
      aria-label={t("diagnostics.actions")}
      className="flex flex-wrap gap-2 border-t border-border pt-2"
    >
      {actions.map((action) => {
        if (action.type === "restart") {
          return (
            <Button
              key={diagnosticActionKey(action)}
              type="button"
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={onRestart}
            >
              {t("diagnostics.actionRestart")}
            </Button>
          );
        }
        if (action.type === "open_docs" && instance.references?.docsUrl) {
          return (
            <a
              key={diagnosticActionKey(action)}
              href={instance.references.docsUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex h-8 items-center rounded-card border border-border bg-transparent px-3 text-xs transition-colors hover:bg-surface-hover"
            >
              <ExternalLink className="mr-1 h-3 w-3" />
              {t("diagnostics.actionDocs")}
            </a>
          );
        }
        if (action.type === "open_symbols" && instance.references?.symbolsUrl) {
          return (
            <a
              key={diagnosticActionKey(action)}
              href={instance.references.symbolsUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex h-8 items-center rounded-card border border-border bg-transparent px-3 text-xs transition-colors hover:bg-surface-hover"
            >
              <ExternalLink className="mr-1 h-3 w-3" />
              {t("diagnostics.actionSymbols")}
            </a>
          );
        }
        if (
          action.type === "remove_instrument" &&
          action.target !== undefined
        ) {
          const target = action.target;
          return (
            <Button
              key={diagnosticActionKey(action)}
              type="button"
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => onDeleteInstrument(instance, target)}
            >
              {t("diagnostics.actionRemoveTarget", { target })}
            </Button>
          );
        }
        return null;
      })}
      <Button asChild variant="outline" size="sm">
        <Link to="/service#logs">
          <Logs className="mr-1 h-3 w-3" />
          {t("diagnostics.actionLogs")}
        </Link>
      </Button>
    </div>
  );
}

function DiagnosticPanel({
  items,
  instance,
  busy,
  onRestart,
  onDeleteInstrument,
  children,
}: {
  items: MarketDataDiagnostic[];
  instance: MarketDataInstance;
  busy: boolean;
  onRestart: () => void;
  onDeleteInstrument: (
    instance: MarketDataInstance,
    externalSymbol: string,
  ) => void;
  children: ReactNode;
}) {
  const { t } = useTranslation("marketData");
  const [collapsed, setCollapsed] = useState(false);
  const actions = diagnosticActions(items);
  const summaryVariant = diagnosticSummaryVariant(items);

  if (collapsed) {
    return (
      <button
        type="button"
        aria-expanded={false}
        onClick={() => setCollapsed(false)}
        className="flex w-full items-center justify-between gap-3 rounded-card border border-border bg-surface px-3 py-2 text-left text-sm transition-colors hover:bg-surface-hover"
      >
        <span className="flex min-w-0 items-center gap-2">
          <Badge variant={summaryVariant} className="shrink-0">
            {items.length}
          </Badge>
          <span className="truncate font-medium text-text">
            {t("diagnostics.collapsedSummary", { count: items.length })}
          </span>
        </span>
        <span className="inline-flex items-center gap-1 text-xs text-muted">
          {t("diagnostics.expand")}
          <ChevronDown className="h-3 w-3" />
        </span>
      </button>
    );
  }

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={summaryVariant}>{items.length}</Badge>
        <span className="text-sm font-medium text-text">
          {t("diagnostics.summary", { count: items.length })}
        </span>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="ml-auto"
          aria-expanded={true}
          onClick={() => setCollapsed(true)}
        >
          {t("diagnostics.collapse")}
          <ChevronUp />
        </Button>
      </div>
      <div
        role="region"
        aria-label={t("diagnostics.title")}
        className={MARKET_DATA_DIAGNOSTICS_PANEL_CLASS}
      >
        <div className="space-y-4">{children}</div>
      </div>
      <DiagnosticActions
        actions={actions}
        instance={instance}
        busy={busy}
        onRestart={onRestart}
        onDeleteInstrument={onDeleteInstrument}
      />
    </div>
  );
}

// Verification verdict or a transport-failure sentinel for an inline outcome.
type VerifyOutcome = MarketDataSymbolVerification | FailedVerificationOutcome;

function PairUsageWarning({ usages }: { usages: PairUsage[] }) {
  const { t } = useTranslation("marketData");
  if (usages.length === 0) {
    return null;
  }
  const feeds = usages
    .map(
      (usage) =>
        `${usage.instanceLabel} (${usage.providerType}: ${usage.externalSymbol})`,
    )
    .join(", ");
  const title = t("instrument.pairAlreadyUsed", { feeds });
  return (
    <span title={title} aria-label={title} className="inline-flex">
      <TriangleAlert className="h-4 w-4 shrink-0 text-[var(--warn)]" />
    </span>
  );
}

export function InstanceSettingsDialog({
  instance,
  busy,
  existingLabels,
  onOpenChange,
  onSave,
}: {
  instance: MarketDataInstance | null;
  busy: boolean;
  existingLabels: string[];
  onOpenChange: (open: boolean) => void;
  onSave: (
    instance: MarketDataInstance,
    form: SettingsForm,
  ) => Promise<boolean>;
}) {
  const { t } = useTranslation("marketData");
  const [draft, setDraft] = useState({
    label: instance?.label ?? "",
  });
  const [settingsDraft, setSettingsDraft] = useState<ProviderSettingsDraft>(
    instance ? initialSettingsDraft(instance.provider, instance) : {},
  );
  const normalizedLabel = draft.label.trim();
  const labelTaken =
    normalizedLabel !== "" &&
    existingLabels.some(
      (label) =>
        label.trim().toLowerCase() === normalizedLabel.toLowerCase() &&
        label.trim().toLowerCase() !== instance?.label.trim().toLowerCase(),
    );

  const close = () => onOpenChange(false);
  const submit = async () => {
    if (
      !instance ||
      normalizedLabel === "" ||
      labelTaken ||
      !providerSettingsReady(instance.provider, settingsDraft, instance)
    ) {
      return;
    }
    const ok = await onSave(instance, {
      label: normalizedLabel,
      credentials: buildProviderCredentials(instance.provider, settingsDraft),
    });
    if (ok) {
      close();
    }
  };

  return (
    <Dialog
      open={instance !== null}
      onOpenChange={(next) => {
        if (!next) {
          close();
        }
      }}
    >
      <DialogContent
        className={
          instance?.provider === IB_PROVIDER
            ? MARKET_DATA_DIALOG_CONTENT_CLASS
            : undefined
        }
      >
        <DialogHeader>
          <DialogTitle>{t("settings.editTitle")}</DialogTitle>
          <DialogDescription>{t("settings.editDescription")}</DialogDescription>
        </DialogHeader>
        {instance && (
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="md-edit-label">{t("instance.label")}</Label>
              <Input
                id="md-edit-label"
                value={draft.label}
                autoFocus
                onChange={(e) =>
                  setDraft((prev) => ({ ...prev, label: e.target.value }))
                }
                disabled={busy}
              />
              {labelTaken && (
                <p className="text-xs text-[var(--danger)]">
                  {t("create.duplicateLabel")}
                </p>
              )}
            </div>
            {providerHasSettings(instance.provider) && (
              <div className="space-y-3">
                <p className="text-xs font-medium uppercase tracking-wide text-muted">
                  {t("settings.title")}
                </p>
                <ProviderSettingsFields
                  providerType={instance.provider}
                  draft={settingsDraft}
                  instance={instance}
                  busy={busy}
                  onChange={(patch) =>
                    setSettingsDraft((prev) => ({ ...prev, ...patch }))
                  }
                />
              </div>
            )}
          </div>
        )}
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={close}
            disabled={busy}
          >
            {t("actions.cancel")}
          </Button>
          <Button
            type="button"
            onClick={() => {
              void submit();
            }}
            disabled={
              busy ||
              !instance ||
              normalizedLabel === "" ||
              labelTaken ||
              !providerSettingsReady(instance.provider, settingsDraft, instance)
            }
          >
            {t("actions.saveSettings")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function InstanceCard({
  instance,
  pairUsageMap,
  busy,
  onEditSettings,
  onVerifySymbol,
  onSearchSymbols,
  onToggleInstance,
  onDeleteInstance,
  onUpsertInstrument,
  onUpsertIBInstrument,
  onToggleInstrument,
  onDeleteInstrument,
  onDeleteIBInstrument,
  onRestart,
}: {
  instance: MarketDataInstance;
  pairUsageMap: PairUsageMap;
  busy: boolean;
  onEditSettings: (instance: MarketDataInstance) => void;
  onVerifySymbol: (
    instance: MarketDataInstance,
    externalSymbol: string,
  ) => Promise<MarketDataSymbolVerification | null>;
  onSearchSymbols: (
    instance: MarketDataInstance,
    input: MarketDataSymbolSearchInput,
  ) => Promise<MarketDataSymbolMatch[] | null>;
  onToggleInstance: (instance: MarketDataInstance) => void;
  onDeleteInstance: (instance: MarketDataInstance) => void;
  onUpsertInstrument: (
    instance: MarketDataInstance,
    draft: InstrumentDraft,
    options?: { reload?: boolean },
  ) => Promise<boolean>;
  onUpsertIBInstrument: (
    instance: MarketDataInstance,
    draft: InstrumentDraft,
    contract: IBContract,
  ) => Promise<boolean>;
  onToggleInstrument: (
    instance: MarketDataInstance,
    externalSymbol: string,
    enabled: boolean,
  ) => void;
  onDeleteInstrument: (
    instance: MarketDataInstance,
    externalSymbol: string,
  ) => void;
  onDeleteIBInstrument: (
    instance: MarketDataInstance,
    externalSymbol: string,
  ) => void;
  onRestart: () => void;
}) {
  const { t } = useTranslation("marketData");
  const [draft, setDraft] = useState<InstrumentDraft>(emptyInstrument);
  const [draftVerify, setDraftVerify] = useState<VerifyOutcome | null>(null);
  const [draftVerifying, setDraftVerifying] = useState(false);
  const {
    outcomes: rowVerify,
    isVerifying,
    verifyRow,
  } = useRowVerification((symbol) => onVerifySymbol(instance, symbol));
  const [manualDrafts, setManualDrafts] = useState<Record<string, string>>({});
  // IB-only structured contract for the draft instrument plus the live-search
  // state. Both are presentation-only and reset after a successful add.
  const [contractDraft, setContractDraft] =
    useState<IBContract>(newEmptyIBContract);
  const {
    query: resolveQuery,
    results: resolveResults,
    filterQuery: resolveFilterQuery,
    searching: resolving,
    searched: resolved,
    onQueryChange: handleResolveQueryChange,
    onFilterChange: setResolveFilterQuery,
    onSearch: runResolve,
  } = useSymbolSearch((query) => onSearchSymbols(instance, { query }));
  const [instrumentDialogOpen, setInstrumentDialogOpen] = useState(false);

  // Some providers cannot verify symbols, so their verify controls are hidden.
  const canVerify = instance.verifiesSymbols;

  // Only the manual (bring-your-own) provider exposes the operator-set mark; for
  // streaming providers the price comes from the source, so the input and column
  // are hidden.
  const isManual = instance.provider === MANUAL_PROVIDER;
  // IB instruments carry a structured contract editor alongside the plain
  // feed-scoped resolver.
  const isIB = instance.provider === IB_PROVIDER;
  const usesInstrumentDialog = providerUsesCustomInstrumentDialog(
    instance.provider,
  );
  const canSearch = instance.searchesSymbols;
  const siteUrl = providerSiteUrl(instance.provider);
  const docsUrl =
    instance.references?.docsUrl || providerDocsUrl(instance.provider);
  const symbolsUrl =
    instance.references?.symbolsUrl || providerSymbolsUrl(instance.provider);
  const draftPairUsages = pairUsages(
    pairUsageMap,
    draft.baseAsset,
    draft.quoteAsset,
  );
  const instrumentDraftValid = isInstrumentDraftValid(draft, isManual);
  const ibContractNumericValid = isIBContractNumericValid(contractDraft);
  const ibContractValid =
    ibContractNumericValid && !!contractDraft.currency?.trim();

  const resetDraft = () => {
    setDraft(emptyInstrument);
    setDraftVerify(null);
    setContractDraft(newEmptyIBContract());
    handleResolveQueryChange("");
  };

  const closeInstrumentDialog = () => {
    setInstrumentDialogOpen(false);
    resetDraft();
  };

  const submitInstrument = async () => {
    if (!instrumentDraftValid) {
      return;
    }
    if (await onUpsertInstrument(instance, draft)) {
      setDraft(emptyInstrument);
      setDraftVerify(null);
    }
  };

  // IB add: hand the draft and the structured contract to the page handler,
  // which persists the instrument first and then PUTs the full contracts map.
  const submitIBInstrument = async () => {
    if (!instrumentDraftValid || !ibContractValid) {
      return;
    }
    const symbol = contractDraft.symbol?.trim() || draft.externalSymbol.trim();
    const contract: IBContract = { ...contractDraft, symbol };
    if (await onUpsertIBInstrument(instance, draft, contract)) {
      resetDraft();
      setInstrumentDialogOpen(false);
    }
  };

  const pickResolvedMatch = (match: MarketDataSymbolMatch) => {
    const pair = resolvedInstrumentPair(match, resolveQuery);
    setDraft((d) => ({
      ...d,
      externalSymbol: match.symbol,
      baseAsset: pair.base,
      quoteAsset: pair.quote,
    }));
    setContractDraft({
      symbol: match.symbol,
      secType: match.secType || undefined,
      exchange: match.exchange || undefined,
      primaryExchange: match.primaryExchange || undefined,
      currency: match.currency || undefined,
      conId: match.conId,
      lastTradeDateOrContractMonth: match.lastTradeDateOrContractMonth,
      strike: match.strike,
      right: match.right || undefined,
      multiplier: match.multiplier || undefined,
      localSymbol: match.localSymbol || undefined,
      tradingClass: match.tradingClass || undefined,
    });
    setDraftVerify(null);
  };

  const verifyDraft = async () => {
    setDraftVerifying(true);
    setDraftVerify(null);
    try {
      // A null return means the call failed (the page handler already surfaced
      // the detail in the banner); keep an inline failed marker so the draft row
      // still reacts.
      const result = await onVerifySymbol(instance, draft.externalSymbol);
      setDraftVerify(result ?? { failed: true });
    } finally {
      setDraftVerifying(false);
    }
  };

  const manualPriceValue = (externalSymbol: string, fallback: string): string =>
    manualDrafts[externalSymbol] ?? fallback;

  const setManualPriceDraft = (externalSymbol: string, manualPrice: string) => {
    setManualDrafts((prev) => ({ ...prev, [externalSymbol]: manualPrice }));
  };

  const submitManualPrice = async (
    instrument: MarketDataInstance["instruments"][number],
  ) => {
    const manualPrice = manualPriceValue(
      instrument.externalSymbol,
      instrument.manualPrice,
    );
    if (!isOptionalDecimal(manualPrice)) {
      return;
    }
    const ok = await onUpsertInstrument(
      instance,
      {
        externalSymbol: instrument.externalSymbol,
        baseAsset: instrument.baseAsset,
        quoteAsset: instrument.quoteAsset,
        manualPrice,
        enabled: instrument.enabled,
      },
      { reload: false },
    );
    if (ok) {
      setManualDrafts((prev) => {
        const next = { ...prev };
        delete next[instrument.externalSymbol];
        return next;
      });
    }
  };

  return (
    <Card>
      <VenueCardHeader
        providerType={instance.provider}
        title={providerTitle(instance)}
        description={t(`providers.${instance.provider}.description`, {
          defaultValue: "",
        })}
        badges={
          <>
            <Badge variant="neutral">{instance.provider}</Badge>
            <Badge variant={instance.enabled ? "ok" : "neutral"}>
              {instance.enabled
                ? t("venues:state.enabled")
                : t("venues:state.disabled")}
            </Badge>
            {instance.state === "ok" && (
              <Badge variant="ok">{t("state.live")}</Badge>
            )}
            {instance.state === "pending" && (
              <Badge variant="warn">{t("state.notApplied")}</Badge>
            )}
            {instance.state === "error" && (
              <Badge variant="danger">{t("state.error")}</Badge>
            )}
          </>
        }
        error={
          instance.state === "error" && instance.error
            ? t("instanceError", { message: instance.error })
            : undefined
        }
        links={{ siteUrl, docsUrl, symbolsUrl }}
        actions={
          <>
            {providerHasSettings(instance.provider) && (
              <Button
                type="button"
                variant="ghost"
                size="icon"
                aria-label={t("actions.editSettings")}
                title={t("actions.editSettings")}
                onClick={() => onEditSettings(instance)}
                disabled={busy}
              >
                <Settings />
              </Button>
            )}
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => onToggleInstance(instance)}
              disabled={busy}
            >
              {instance.enabled
                ? t("venues:actions.disable")
                : t("venues:actions.enable")}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              aria-label={t("actions.deleteInstance")}
              onClick={() => onDeleteInstance(instance)}
              disabled={busy}
            >
              <Trash2 />
            </Button>
          </>
        }
      />
      <CardContent className="space-y-4">
        {!usesInstrumentDialog && canSearch && (
          <SymbolSearchPanel
            id={`md-resolve-${instance.id}`}
            busy={busy}
            searching={resolving}
            searched={resolved}
            query={resolveQuery}
            results={resolveResults}
            filterQuery={resolveFilterQuery}
            onQueryChange={handleResolveQueryChange}
            onFilterChange={setResolveFilterQuery}
            onSearch={() => {
              void runResolve();
            }}
            onPick={pickResolvedMatch}
            label={t("resolve.label")}
            placeholder={t("resolve.placeholder")}
            noResultsText={t("resolve.noResults")}
            getKey={resolvedMatchKey}
            getLabel={(match) => symbolPairSource(match.symbol)}
            getMetadata={resolvedSymbolMetadata}
            getName={(match) => match.name}
            getSearchText={resolvedSymbolSearchText}
          />
        )}
        {usesInstrumentDialog ? (
          <div className="flex justify-end">
            <Button
              type="button"
              onClick={() => setInstrumentDialogOpen(true)}
              disabled={busy}
            >
              <Plus />
              {t("venues:actions.addInstrument")}
            </Button>
          </div>
        ) : (
          <InstrumentDraftRow
            draft={draft}
            onDraftChange={(patch) => {
              setDraft((d) => ({ ...d, ...patch }));
              if ("externalSymbol" in patch) setDraftVerify(null);
            }}
            busy={busy}
            valid={instrumentDraftValid}
            onSubmit={() => {
              void (isIB ? submitIBInstrument() : submitInstrument());
            }}
            verify={
              canVerify
                ? {
                    verifying: draftVerifying,
                    onVerify: () => {
                      void verifyDraft();
                    },
                  }
                : undefined
            }
            enabled={{
              value: draft.enabled,
              onToggle: () => setDraft((d) => ({ ...d, enabled: !d.enabled })),
            }}
            quoteAdornment={<PairUsageWarning usages={draftPairUsages} />}
            extraField={
              isManual ? (
                <Input
                  value={draft.manualPrice}
                  inputMode="decimal"
                  onChange={(e) =>
                    setDraft((d) => ({ ...d, manualPrice: e.target.value }))
                  }
                  placeholder={t("instrument.manualPrice")}
                  disabled={busy}
                />
              ) : undefined
            }
          />
        )}

        {isIB && (
          <Dialog
            open={instrumentDialogOpen}
            onOpenChange={(next) => {
              if (next) {
                setInstrumentDialogOpen(true);
              } else {
                closeInstrumentDialog();
              }
            }}
          >
            <DialogContent className={MARKET_DATA_DIALOG_CONTENT_CLASS}>
              <DialogHeader>
                <DialogTitle>{t("instrumentDialog.ibTitle")}</DialogTitle>
                <DialogDescription>
                  {t("instrumentDialog.ibDescription")}
                </DialogDescription>
              </DialogHeader>
              <div className="space-y-4">
                {canSearch && (
                  <SymbolSearchPanel
                    id={`md-resolve-dialog-${instance.id}`}
                    busy={busy}
                    searching={resolving}
                    searched={resolved}
                    query={resolveQuery}
                    results={resolveResults}
                    filterQuery={resolveFilterQuery}
                    onQueryChange={handleResolveQueryChange}
                    onFilterChange={setResolveFilterQuery}
                    onSearch={() => {
                      void runResolve();
                    }}
                    onPick={pickResolvedMatch}
                    label={t("resolve.label")}
                    placeholder={t("resolve.placeholder")}
                    noResultsText={t("resolve.noResults")}
                    getKey={resolvedMatchKey}
                    getLabel={(match) => symbolPairSource(match.symbol)}
                    getMetadata={resolvedSymbolMetadata}
                    getName={(match) => match.name}
                    getSearchText={resolvedSymbolSearchText}
                  />
                )}
                <div className="grid items-end gap-3 md:grid-cols-[1fr_0.7fr_0.7fr_0.5fr]">
                  <div className="space-y-2">
                    <Label htmlFor={`md-add-${instance.id}-external`}>
                      {t("venues:instrument.externalSymbol")}
                    </Label>
                    <Input
                      id={`md-add-${instance.id}-external`}
                      value={draft.externalSymbol}
                      onChange={(e) => {
                        const externalSymbol = e.target.value;
                        setDraft((d) => ({ ...d, externalSymbol }));
                        setDraftVerify(null);
                      }}
                      disabled={busy}
                    />
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor={`md-add-${instance.id}-base`}>
                      {t("venues:instrument.baseAsset")}
                    </Label>
                    <Input
                      id={`md-add-${instance.id}-base`}
                      value={draft.baseAsset}
                      onChange={(e) =>
                        setDraft((d) => ({ ...d, baseAsset: e.target.value }))
                      }
                      disabled={busy}
                    />
                  </div>
                  <div className="space-y-2">
                    <div className="flex items-center gap-2">
                      <Label htmlFor={`md-add-${instance.id}-quote`}>
                        {t("venues:instrument.quoteAsset")}
                      </Label>
                      <PairUsageWarning usages={draftPairUsages} />
                    </div>
                    <Input
                      id={`md-add-${instance.id}-quote`}
                      value={draft.quoteAsset}
                      onChange={(e) =>
                        setDraft((d) => ({ ...d, quoteAsset: e.target.value }))
                      }
                      disabled={busy}
                    />
                  </div>
                  <StateToggleField
                    id={`md-add-${instance.id}-enabled`}
                    enabled={draft.enabled}
                    busy={busy}
                    onToggle={() =>
                      setDraft((d) => ({ ...d, enabled: !d.enabled }))
                    }
                  />
                </div>
                <div className="space-y-3 rounded-card border border-border bg-bg/50 p-3">
                  <p className="text-xs font-medium uppercase tracking-wide text-muted">
                    {t("instrumentDialog.contract")}
                  </p>
                  <IBContractEditor
                    contract={contractDraft}
                    busy={busy}
                    idPrefix={`md-ib-add-${instance.id}`}
                    onChange={(patch) =>
                      setContractDraft((prev) => ({ ...prev, ...patch }))
                    }
                  />
                </div>
              </div>
              <DialogFooter>
                <Button
                  type="button"
                  variant="outline"
                  onClick={closeInstrumentDialog}
                  disabled={busy}
                >
                  {t("actions.cancel")}
                </Button>
                <Button
                  type="button"
                  onClick={() => {
                    void submitIBInstrument();
                  }}
                  disabled={busy || !instrumentDraftValid || !ibContractValid}
                >
                  <Plus />
                  {t("venues:actions.addInstrument")}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        )}

        {draftVerify && (
          <div className="-mt-2">
            <VerifyResult
              result={draftVerify}
              unavailableLabel={t("verify.unavailable")}
            />
          </div>
        )}

        <InstrumentsTable
          instruments={instance.instruments}
          busy={busy}
          displaySymbol={(instrument) =>
            instrumentDisplaySymbol(instance, instrument.externalSymbol)
          }
          metadata={(instrument) =>
            instrumentMetadata(instance, instrument.externalSymbol)
          }
          pairAdornment={(instrument) => (
            <PairUsageWarning
              usages={pairUsages(
                pairUsageMap,
                instrument.baseAsset,
                instrument.quoteAsset,
                {
                  instanceExternalId: instance.id,
                  externalSymbol: instrument.externalSymbol,
                },
              )}
            />
          )}
          extraColumns={[
            ...(isManual
              ? [
                  {
                    id: "manualPrice",
                    header: t("table.manualPrice"),
                    renderCell: (
                      instrument: MarketDataInstance["instruments"][number],
                    ) => (
                      <div className="flex min-w-40 gap-2">
                        <Input
                          value={manualPriceValue(
                            instrument.externalSymbol,
                            instrument.manualPrice,
                          )}
                          inputMode="decimal"
                          aria-label={t("instrument.manualPriceFor", {
                            symbol: instrument.externalSymbol,
                          })}
                          onChange={(e) =>
                            setManualPriceDraft(
                              instrument.externalSymbol,
                              e.target.value,
                            )
                          }
                          disabled={busy}
                          className="nums h-8"
                        />
                        <Button
                          type="button"
                          variant="outline"
                          size="icon"
                          aria-label={t("actions.sendPrice")}
                          title={t("actions.sendPrice")}
                          disabled={
                            busy ||
                            !isOptionalDecimal(
                              manualPriceValue(
                                instrument.externalSymbol,
                                instrument.manualPrice,
                              ),
                            )
                          }
                          onClick={() => {
                            void submitManualPrice(instrument);
                          }}
                        >
                          <Send />
                        </Button>
                      </div>
                    ),
                  },
                ]
              : []),
            {
              id: "price",
              header: t("table.price"),
              cellClassName: "nums text-text",
              renderCell: (instrument) =>
                displayedPriceLabel(instrument) || "-",
            },
            {
              id: "asOf",
              header: t("table.asOf"),
              cellClassName: "nums text-muted",
              renderCell: (instrument) => (
                <>
                  {instrument.quote ? (
                    <span>
                      {formatDateTime(instrument.quote.asOf)}{" "}
                      {instrument.updateIntervalMs ? (
                        <span
                          className="text-xs"
                          title={t("table.updateIntervalTooltip")}
                        >
                          {formatCompactDuration(instrument.updateIntervalMs)}
                        </span>
                      ) : null}
                    </span>
                  ) : (
                    "-"
                  )}
                </>
              ),
            },
          ]}
          parentEnabled={instance.enabled}
          parentDisabledLabel={t("state.feedDisabled")}
          stateAdornment={(instrument) => (
            <>
              {instance.enabled && instrument.stale && !isManual && (
                <Badge variant="warn" title={t("state.staleTooltip")}>
                  {t("state.stale")}
                </Badge>
              )}
              <VerifyResult
                result={rowVerify[instrument.externalSymbol] ?? null}
                unavailableLabel={t("verify.unavailable")}
              />
            </>
          )}
          onToggle={(instrument) =>
            onToggleInstrument(
              instance,
              instrument.externalSymbol,
              !instrument.enabled,
            )
          }
          onVerify={
            canVerify
              ? (instrument) => {
                  void verifyRow(instrument.externalSymbol);
                }
              : undefined
          }
          isVerifying={isVerifying}
          onDelete={(instrument) =>
            isIB
              ? onDeleteIBInstrument(instance, instrument.externalSymbol)
              : onDeleteInstrument(instance, instrument.externalSymbol)
          }
        />

        {instance.diagnostics.length > 0 &&
          (() => {
            const configGroup = instance.diagnostics.filter(
              (d) => d.kind === "config",
            );
            const providerGroup = instance.diagnostics.filter(
              (d) => d.kind === "environment" || d.kind === "provider",
            );
            return (
              <DiagnosticPanel
                items={instance.diagnostics}
                instance={instance}
                busy={busy}
                onRestart={onRestart}
                onDeleteInstrument={onDeleteInstrument}
              >
                <DiagnosticGroup
                  title={t("diagnostics.groupActionNeeded")}
                  items={configGroup}
                />
                <DiagnosticGroup
                  title={t("diagnostics.groupProvider")}
                  items={providerGroup}
                />
              </DiagnosticPanel>
            );
          })()}
      </CardContent>
    </Card>
  );
}

function DeleteInstanceDialog({
  target,
  busy,
  onOpenChange,
  onSubmit,
}: {
  target: MarketDataInstance | null;
  busy: boolean;
  onOpenChange: (open: boolean) => void;
  onSubmit: () => void;
}) {
  const { t } = useTranslation("marketData");
  return (
    <Dialog open={target !== null} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("deleteInstance.title")}</DialogTitle>
          <DialogDescription>
            {t("deleteInstance.description", {
              label: target ? providerTitle(target) : "",
            })}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {t("deleteInstance.cancel")}
          </Button>
          <Button
            onClick={onSubmit}
            disabled={busy}
            className="border-[var(--danger)] bg-[var(--danger)] text-bg hover:border-[var(--danger)] hover:bg-[var(--danger)]"
          >
            <Trash2 />
            {t("deleteInstance.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Market-data control-plane page. */
export function MarketData() {
  const { t } = useTranslation("marketData");
  const { t: tc } = useTranslation();
  const { load, reload } = useMarketData();
  const {
    createMarketDataInstance,
    deleteMarketDataInstance,
    deleteMarketDataInstrument,
    restartMarketData,
    searchMarketDataSymbols,
    setMarketDataInstanceEnabled,
    setMarketDataInstrumentEnabled,
    upsertMarketDataInstrument,
    updateMarketDataInstanceSettings,
    verifyMarketDataSymbol,
  } = useOfficerApi();
  const [busy, setBusy] = useState(false);
  const [mutationError, setMutationError] = useState("");
  const [selectedProvider, setSelectedProvider] =
    useState<MarketDataProvider | null>(null);
  const [settingsInstance, setSettingsInstance] =
    useState<MarketDataInstance | null>(null);
  const [guideOpen, setGuideOpen] = useState(false);
  const [deleteInstanceTarget, setDeleteInstanceTarget] =
    useState<MarketDataInstance | null>(null);
  const [deleteInstrumentTarget, setDeleteInstrumentTarget] =
    useState<DeleteInstrumentTarget | null>(null);

  const providers = load.state === "ready" ? load.data.providers : [];
  const providerOptions = sortProviders(
    providers.length > 0 ? providers : FALLBACK_PROVIDERS,
  );
  const existingLabels =
    load.state === "ready"
      ? load.data.instances.map((instance) => instance.label)
      : [];
  const pairUsageMap =
    load.state === "ready" ? buildPairUsageMap(load.data.instances) : {};

  const run = async (
    fn: () => Promise<void>,
    options: { reload?: boolean } = {},
  ): Promise<boolean> => {
    setBusy(true);
    setMutationError("");
    try {
      await fn();
      if (options.reload !== false) {
        reload();
      }
      return true;
    } catch (err) {
      setMutationError(err instanceof Error ? err.message : String(err));
      return false;
    } finally {
      setBusy(false);
    }
  };

  const createInstance = (form: InstanceForm) =>
    run(async () => {
      await createMarketDataInstance(form);
    });

  const updateInstanceSettings = (
    instance: MarketDataInstance,
    form: SettingsForm,
  ) =>
    run(async () => {
      await updateMarketDataInstanceSettings(instance.id, form);
    });

  const restart = () =>
    run(async () => {
      await restartMarketData();
    });

  // Verify is non-mutating: it neither toggles page busy nor reloads, so the
  // operator can keep editing. A transport/catalogue failure surfaces in the
  // shared error banner and yields no result.
  const verifySymbol = async (
    instance: MarketDataInstance,
    externalSymbol: string,
  ): Promise<MarketDataSymbolVerification | null> => {
    setMutationError("");
    try {
      return await verifyMarketDataSymbol(instance.id, externalSymbol);
    } catch (err) {
      setMutationError(err instanceof Error ? err.message : String(err));
      return null;
    }
  };

  // Search is non-mutating like verify: no busy toggle, no reload. A null result
  // signals a failure already surfaced in the banner; an empty array is a valid
  // "no matches" outcome.
  const searchSymbols = async (
    instance: MarketDataInstance,
    input: MarketDataSymbolSearchInput,
  ): Promise<MarketDataSymbolMatch[] | null> => {
    setMutationError("");
    try {
      const result = await searchMarketDataSymbols(instance.id, input);
      return result.supported ? result.matches : [];
    } catch (err) {
      setMutationError(err instanceof Error ? err.message : String(err));
      return null;
    }
  };

  // IB add: persist the instrument first, then PUT settings carrying the FULL
  // contracts map (existing entries plus the new one). The server merges
  // credentials with a top-level shallow merge, so a partial contracts object
  // would silently drop every other instrument's contract. Existing scalar
  // settings are read back so they are not blanked. Both calls share one run().
  const upsertIBInstrument = (
    instance: MarketDataInstance,
    draft: InstrumentDraft,
    contract: IBContract,
  ) =>
    run(async () => {
      const externalSymbol = draft.externalSymbol.trim();
      await upsertMarketDataInstrument(instance.id, {
        externalSymbol,
        baseAsset: draft.baseAsset.trim(),
        quoteAsset: draft.quoteAsset.trim(),
        manualPrice: "",
        enabled: draft.enabled,
      });
      const contracts = {
        ...readIBContractsFromInstance(instance),
        [externalSymbol]: contract,
      };
      await updateMarketDataInstanceSettings(instance.id, {
        label: instance.label,
        credentials: buildProviderCredentials(
          IB_PROVIDER,
          initialSettingsDraft(IB_PROVIDER, instance),
          { contracts },
        ),
      });
    });

  const submitDeleteInstance = async () => {
    if (!deleteInstanceTarget) return;
    setBusy(true);
    setMutationError("");
    try {
      await deleteMarketDataInstance(deleteInstanceTarget.id);
      setDeleteInstanceTarget(null);
      reload();
    } catch (err) {
      setMutationError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const submitDeleteInstrument = () =>
    run(async () => {
      if (!deleteInstrumentTarget) return;
      if (deleteInstrumentTarget.ib) {
        await deleteMarketDataInstrument(
          deleteInstrumentTarget.instance.id,
          deleteInstrumentTarget.externalSymbol,
        );
        const contracts = readIBContractsFromInstance(
          deleteInstrumentTarget.instance,
        );
        delete contracts[deleteInstrumentTarget.externalSymbol];
        await updateMarketDataInstanceSettings(
          deleteInstrumentTarget.instance.id,
          {
            label: deleteInstrumentTarget.instance.label,
            credentials: buildProviderCredentials(
              IB_PROVIDER,
              initialSettingsDraft(
                IB_PROVIDER,
                deleteInstrumentTarget.instance,
              ),
              { contracts },
            ),
          },
        );
      } else {
        await deleteMarketDataInstrument(
          deleteInstrumentTarget.instance.id,
          deleteInstrumentTarget.externalSymbol,
        );
      }
      setDeleteInstrumentTarget(null);
    });

  return (
    <Page
      title={t("title")}
      actions={
        <>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={restart}
            disabled={busy}
          >
            <RotateCcw />
            {t("actions.restart")}
          </Button>
          <Button type="button" variant="ghost" size="sm" onClick={reload}>
            <RefreshCw />
            {tc("actions.refresh")}
          </Button>
        </>
      }
    >
      <AddProviderButton
        label={t("providers.addSource")}
        providerTypes={providerOptions.map((provider) => provider.type)}
        onClick={() => setGuideOpen(true)}
      />
      <ProviderGuideDialog
        open={guideOpen}
        title={t("instances.title")}
        description={t("providers.guideTitle")}
        providers={providerOptions.map((provider) => ({
          type: provider.type,
          title: t(`providers.${provider.type}.title`, {
            defaultValue: provider.title,
          }),
          description: t(`providers.${provider.type}.description`, {
            defaultValue: "",
          }),
          siteUrl: providerSiteUrl(provider.type),
          docsUrl: providerDocsUrl(provider.type),
          symbolsUrl: providerSymbolsUrl(provider.type),
        }))}
        onSelect={(entry) => {
          const provider = providerOptions.find(
            (provider) => provider.type === entry.type,
          );
          if (!provider) {
            throw new Error(`Unknown market-data provider: ${entry.type}`);
          }
          setGuideOpen(false);
          setSelectedProvider(provider);
        }}
        onOpenChange={setGuideOpen}
      />
      <CreateInstanceDialog
        key={selectedProvider?.type ?? "create-none"}
        provider={selectedProvider}
        existingLabels={existingLabels}
        busy={busy}
        onOpenChange={(open) => {
          if (!open) {
            setSelectedProvider(null);
          }
        }}
        onCreate={createInstance}
      />
      <InstanceSettingsDialog
        key={settingsInstance?.id ?? "settings-none"}
        instance={settingsInstance}
        busy={busy}
        existingLabels={existingLabels}
        onOpenChange={(open) => {
          if (!open) {
            setSettingsInstance(null);
          }
        }}
        onSave={updateInstanceSettings}
      />

      {mutationError && <ErrorBanner message={mutationError} />}

      {load.state === "loading" && <TableSkeleton rows={3} cols={5} />}
      <StaleState load={load} reload={reload} />
      {load.state === "error" && (
        <ErrorState message={load.error} onRetry={reload} />
      )}
      {load.state === "ready" && (
        <div className="space-y-4">
          {load.data.instances.length === 0 && (
            <Card>
              <CardContent className="py-8 text-center text-sm text-muted">
                {t("emptyInstances")}
              </CardContent>
            </Card>
          )}
          {load.data.instances.map((instance) => (
            <InstanceCard
              key={instance.id}
              instance={instance}
              pairUsageMap={pairUsageMap}
              busy={busy}
              onEditSettings={setSettingsInstance}
              onVerifySymbol={verifySymbol}
              onSearchSymbols={searchSymbols}
              onToggleInstance={(target) =>
                run(() =>
                  setMarketDataInstanceEnabled(target.id, !target.enabled),
                )
              }
              onDeleteInstance={(target) => {
                setDeleteInstanceTarget(target);
              }}
              onUpsertInstrument={(target, draft, options) =>
                run(
                  () =>
                    upsertMarketDataInstrument(target.id, draft).then(() => {}),
                  options,
                )
              }
              onUpsertIBInstrument={upsertIBInstrument}
              onToggleInstrument={(target, externalSymbol, enabled) =>
                run(() =>
                  setMarketDataInstrumentEnabled(
                    target.id,
                    externalSymbol,
                    enabled,
                  ),
                )
              }
              onDeleteInstrument={(target, externalSymbol) =>
                setDeleteInstrumentTarget({
                  instance: target,
                  externalSymbol,
                  ib: false,
                })
              }
              onDeleteIBInstrument={(target, externalSymbol) =>
                setDeleteInstrumentTarget({
                  instance: target,
                  externalSymbol,
                  ib: true,
                })
              }
              onRestart={restart}
            />
          ))}
          <DeleteInstanceDialog
            target={deleteInstanceTarget}
            busy={busy}
            onOpenChange={(open) => {
              if (!open) {
                setDeleteInstanceTarget(null);
              }
            }}
            onSubmit={() => {
              void submitDeleteInstance();
            }}
          />
          <DeleteInstrumentDialog
            open={deleteInstrumentTarget !== null}
            description={t("deleteInstrument.description", {
              symbol: deleteInstrumentTarget?.externalSymbol ?? "",
            })}
            busy={busy}
            onOpenChange={(open) => {
              if (!open) setDeleteInstrumentTarget(null);
            }}
            onSubmit={() => {
              void submitDeleteInstrument();
            }}
          />
        </div>
      )}
    </Page>
  );
}
