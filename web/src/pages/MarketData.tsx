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
  BriefcaseBusiness,
  Check,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  ExternalLink,
  FlaskConical,
  Logs,
  Plus,
  RefreshCw,
  RotateCcw,
  SearchCheck,
  Send,
  Settings,
  Trash2,
  TriangleAlert,
  Upload,
  X,
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
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
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

// secType set surfaced in the IB contract editor (Locked decision 4).
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

function IBKRIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#050608" />
      <path
        d="M2.8 3.2h5.8L4.1 13.4l4.7 7.4H3.1L0 16.4V8.3L2.8 3.2Z"
        fill="#d10f1f"
      />
      <path
        d="M8.6 3.2h3.8L7.9 12l4.5 8.8H8.6L4 13.4 8.6 3.2Z"
        fill="#ef1b2d"
      />
      <circle cx="15.8" cy="11.4" r="3.6" fill="#ef1b2d" />
    </svg>
  );
}

function BinanceIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#0b0e11" />
      <g fill="#f0b90b">
        <path d="M12 3.2 15 6.2 12 9.2 9 6.2 12 3.2Z" />
        <path d="M6.2 9 9.2 12 6.2 15 3.2 12 6.2 9Z" />
        <path d="M17.8 9 20.8 12 17.8 15 14.8 12 17.8 9Z" />
        <path d="M12 14.8 15 17.8 12 20.8 9 17.8 12 14.8Z" />
        <path d="M12 9.4 14.6 12 12 14.6 9.4 12 12 9.4Z" />
      </g>
    </svg>
  );
}

function KrakenIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#fff" />
      <path
        d="M4.8 15.7v-3.4c0-4.3 3.2-7.4 7.2-7.4s7.2 3.1 7.2 7.4v3.4a1.9 1.9 0 1 1-3.8 0v-3.1a1.5 1.5 0 0 0-3 0v3.1a1.9 1.9 0 1 1-3.8 0v-3.1a1.5 1.5 0 0 0-3 0v3.1a1.9 1.9 0 1 1-3.8 0Z"
        fill="#5841d8"
      />
    </svg>
  );
}

function CoinbaseIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#0052ff" />
      <circle cx="12" cy="12" r="7.2" fill="#fff" />
      <circle cx="12" cy="12" r="3.35" fill="#0052ff" />
      <rect x="12" y="10.15" width="6.4" height="3.7" fill="#0052ff" />
    </svg>
  );
}

function AlpacaIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#ffd200" />
      <path
        d="M10.1 20V9.8c0-2.8 2.2-5 5-5 2.6 0 4.8 2.1 4.8 4.8V20h-3.2V9.7c0-.9-.7-1.7-1.7-1.7s-1.7.8-1.7 1.7V20h-3.2Z"
        fill="#fff"
      />
      <path d="M4.2 11.8c.5-2.4 2.5-4.1 5.1-4.1h3v4.1H4.2Z" fill="#fff" />
      <path d="M13.7 4.4 15.1 2l.9 2.8-1.1 1-1.2-1.4Z" fill="#fff" />
      <path d="M16.1 4.8 17.7 2.8l.5 2.9-1.3.9-.8-1.8Z" fill="#fff" />
      <rect
        x="8.9"
        y="9.4"
        width="1.8"
        height="0.75"
        rx="0.35"
        fill="#d8d8d8"
      />
    </svg>
  );
}

function OKXIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#000" />
      <g fill="#fff">
        <rect x="5.25" y="5.25" width="4.5" height="4.5" rx="0.8" />
        <rect x="14.25" y="5.25" width="4.5" height="4.5" rx="0.8" />
        <rect x="9.75" y="9.75" width="4.5" height="4.5" rx="0.8" />
        <rect x="5.25" y="14.25" width="4.5" height="4.5" rx="0.8" />
        <rect x="14.25" y="14.25" width="4.5" height="4.5" rx="0.8" />
      </g>
    </svg>
  );
}

function BybitIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#151827" />
      <text
        x="11.2"
        y="13.9"
        textAnchor="middle"
        fontFamily="Arial, sans-serif"
        fontSize="6.1"
        fontWeight="800"
        fill="#fff"
      >
        BYB
      </text>
      <rect
        x="14.8"
        y="7.25"
        width="1.35"
        height="9.5"
        rx="0.15"
        fill="#f7a600"
      />
      <text
        x="18.65"
        y="13.9"
        textAnchor="middle"
        fontFamily="Arial, sans-serif"
        fontSize="6.1"
        fontWeight="800"
        fill="#fff"
      >
        T
      </text>
    </svg>
  );
}

function OANDAIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#fff" />
      <circle cx="6.4" cy="12" r="3.4" stroke="#111" strokeWidth="1.35" />
      <text
        x="14.2"
        y="13.7"
        textAnchor="middle"
        fontFamily="Arial, sans-serif"
        fontSize="5.6"
        fontWeight="700"
        fill="#111"
      >
        ANDA
      </text>
    </svg>
  );
}

function FinnhubIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#fff" />
      <path
        d="M8.2 16.7h10.9c1.5 0 2.7-1.2 2.7-2.7 0-1.4-1.1-2.6-2.5-2.7-.5-3.3-3.3-5.8-6.7-5.8-3.8 0-6.8 3-6.8 6.8v.1h2.4v4.3Z"
        fill="#37b34a"
      />
      <rect x="2.2" y="8.9" width="10.3" height="2.1" rx="1.05" fill="#fff" />
      <rect x="2.2" y="12.1" width="8.3" height="2" rx="1" fill="#fff" />
      <rect x="2.2" y="15.1" width="5.2" height="1.9" rx="0.95" fill="#fff" />
    </svg>
  );
}

function ProviderIcon({
  type,
  className,
}: {
  type: string;
  className?: string;
}) {
  if (type === IB_PROVIDER) {
    return <IBKRIcon className={className} />;
  }
  if (type === BINANCE_PROVIDER) {
    return <BinanceIcon className={className} />;
  }
  if (type === KRAKEN_PROVIDER) {
    return <KrakenIcon className={className} />;
  }
  if (type === COINBASE_PROVIDER) {
    return <CoinbaseIcon className={className} />;
  }
  if (type === ALPACA_PROVIDER) {
    return <AlpacaIcon className={className} />;
  }
  if (type === OKX_PROVIDER) {
    return <OKXIcon className={className} />;
  }
  if (type === BYBIT_PROVIDER) {
    return <BybitIcon className={className} />;
  }
  if (type === OANDA_PROVIDER) {
    return <OANDAIcon className={className} />;
  }
  if (type === FINNHUB_PROVIDER) {
    return <FinnhubIcon className={className} />;
  }
  if (type === MOCK_PROVIDER) {
    return <FlaskConical className={className} />;
  }
  if (type === MANUAL_PROVIDER) {
    return <Upload className={className} />;
  }
  return <BriefcaseBusiness className={className} />;
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

function ProviderTile({
  provider,
  onSelect,
}: {
  provider: MarketDataProvider;
  onSelect: (provider: MarketDataProvider) => void;
}) {
  const { t } = useTranslation("marketData");
  const siteUrl = providerSiteUrl(provider.type);
  const docsUrl = providerDocsUrl(provider.type);
  const symbolsUrl = providerSymbolsUrl(provider.type);

  return (
    <Card className="overflow-hidden">
      <button
        type="button"
        className="block w-full p-4 text-left transition-colors hover:bg-surface-hover"
        onClick={() => onSelect(provider)}
      >
        <div className="flex items-start gap-3">
          <ProviderIcon
            type={provider.type}
            className="mt-0.5 h-5 w-5 shrink-0 text-accent"
          />
          <div className="min-w-0">
            <p className="font-semibold text-text">
              {t(`providers.${provider.type}.title`, {
                defaultValue: provider.title,
              })}
            </p>
            <p className="mt-1 text-xs text-muted">
              {t(`providers.${provider.type}.description`, {
                defaultValue: "",
              })}
            </p>
          </div>
        </div>
      </button>
      {(siteUrl || docsUrl || symbolsUrl) && (
        <div className="flex flex-wrap gap-3 border-t border-border px-4 py-2">
          {siteUrl && (
            <a
              href={siteUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-xs text-accent hover:underline"
            >
              <ExternalLink className="h-3 w-3" />
              {t("providers.site")}
            </a>
          )}
          {docsUrl && (
            <a
              href={docsUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-xs text-accent hover:underline"
            >
              <ExternalLink className="h-3 w-3" />
              {t("providers.docs")}
            </a>
          )}
          {symbolsUrl && (
            <a
              href={symbolsUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-xs text-accent hover:underline"
            >
              <ExternalLink className="h-3 w-3" />
              {t("providers.symbols")}
            </a>
          )}
        </div>
      )}
    </Card>
  );
}

// ProviderGuideDialog holds the provider catalogue behind a compact trigger so
// it does not occupy the page once sources are configured. Picking a tile closes
// the guide and opens that provider's create dialog.
export function ProviderGuideDialog({
  open,
  providers,
  onSelect,
  onOpenChange,
}: {
  open: boolean;
  providers: MarketDataProvider[];
  onSelect: (provider: MarketDataProvider) => void;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation("marketData");
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={MARKET_DATA_DIALOG_CONTENT_CLASS}>
        <DialogHeader>
          <DialogTitle>{t("instances.title")}</DialogTitle>
          <DialogDescription>{t("providers.guideTitle")}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3 sm:grid-cols-2">
          {providers.map((provider) => (
            <ProviderTile
              key={provider.type}
              provider={provider}
              onSelect={onSelect}
            />
          ))}
        </div>
      </DialogContent>
    </Dialog>
  );
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

// The set of conditional fields each secType reveals (Locked decision 4).
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
      <Label htmlFor={id}>{t("table.state")}</Label>
      <Button
        id={id}
        type="button"
        variant={enabled ? "default" : "outline"}
        className="h-[var(--dens-field-h)] w-full justify-center px-[var(--dens-field-px)] text-[length:var(--dens-field-fz)]"
        onClick={onToggle}
        disabled={busy}
      >
        {enabled ? t("state.enabled") : t("state.disabled")}
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

function ResolvePanel({
  id,
  busy,
  resolving,
  resolved,
  query,
  results,
  filterQuery,
  onQueryChange,
  onFilterChange,
  onResolve,
  onPick,
}: {
  id: string;
  busy: boolean;
  resolving: boolean;
  resolved: boolean;
  query: string;
  results: MarketDataSymbolMatch[];
  filterQuery: string;
  onQueryChange: (value: string) => void;
  onFilterChange: (value: string) => void;
  onResolve: () => void;
  onPick: (match: MarketDataSymbolMatch) => void;
}) {
  const { t } = useTranslation("marketData");
  const normalizedFilter = filterQuery.trim().toLowerCase();
  const filteredResults =
    normalizedFilter === ""
      ? results
      : results.filter((match) =>
          resolvedSymbolSearchText(match).includes(normalizedFilter),
        );

  return (
    <div className="space-y-2 rounded-card border border-border bg-bg/50 p-3">
      <Label htmlFor={id}>{t("resolve.label")}</Label>
      <div className="flex gap-2">
        <Input
          id={id}
          value={query}
          placeholder={t("resolve.placeholder")}
          onChange={(e) => onQueryChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              onResolve();
            }
          }}
          disabled={busy || resolving}
        />
        <Button
          type="button"
          variant="outline"
          onClick={onResolve}
          disabled={busy || resolving || query.trim() === ""}
        >
          <SearchCheck />
          {t("resolve.action")}
        </Button>
      </div>
      {resolving && (
        <p className="text-xs text-muted">{t("resolve.searching")}</p>
      )}
      {!resolving && resolved && results.length === 0 && (
        <p className="text-xs text-muted">{t("resolve.noResults")}</p>
      )}
      {results.length > 0 && (
        <div className="space-y-2">
          <Input
            value={filterQuery}
            placeholder={t("resolve.filterPlaceholder")}
            aria-label={t("resolve.filterLabel")}
            onChange={(e) => onFilterChange(e.target.value)}
            disabled={busy || resolving}
          />
          {filteredResults.length === 0 ? (
            <p className="text-xs text-muted">
              {t("resolve.noFilteredResults")}
            </p>
          ) : (
            <ul className="max-h-52 divide-y divide-border overflow-y-auto rounded-card border border-border">
              {filteredResults.map((match) => {
                const metadata = resolvedSymbolMetadata(match);
                return (
                  <li key={resolvedMatchKey(match)}>
                    <button
                      type="button"
                      className="flex w-full items-center gap-3 px-3 py-2 text-left text-sm transition-colors hover:bg-surface-hover"
                      onClick={() => onPick(match)}
                      disabled={busy}
                    >
                      <span className="min-w-0">
                        <span className="block truncate text-text">
                          {symbolPairSource(match.symbol)}
                          {metadata && (
                            <span className="text-muted">
                              {" · "}
                              {metadata}
                            </span>
                          )}
                        </span>
                        {match.name && (
                          <span className="mt-0.5 block truncate text-xs text-muted">
                            {match.name}
                          </span>
                        )}
                      </span>
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}
    </div>
  );
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

/** Inline outcome of a symbol verification: success, not-found, or not-found
 *  with a case-folded suggestion. Renders nothing until a result is present. */
// VerifyOutcome is a verification verdict, or a sentinel for a verify call that
// failed at the transport layer. The failed case guarantees the row always
// shows a reaction instead of silently clearing when the request errors.
type VerifyOutcome = MarketDataSymbolVerification | { failed: true };

function VerifyResult({ result }: { result: VerifyOutcome | null }) {
  const { t } = useTranslation("marketData");
  if (!result) return null;
  if ("failed" in result) {
    return (
      <span className="inline-flex flex-wrap items-center gap-1 text-xs text-[var(--danger)]">
        <X className="h-3 w-3" />
        {t("verify.failed")}
      </span>
    );
  }
  if (!result.supported) return null;
  if (result.exists) {
    return (
      <span className="inline-flex flex-wrap items-center gap-1 text-xs">
        <span className="inline-flex items-center gap-1 text-[var(--ok)]">
          <Check className="h-3 w-3" />
          {t("verify.exists")}
        </span>
        {result.details && (
          <span className="text-muted">({result.details})</span>
        )}
      </span>
    );
  }
  return (
    <span className="inline-flex flex-wrap items-center gap-1 text-xs text-[var(--danger)]">
      <X className="h-3 w-3" />
      {result.details ? t("verify.unavailable") : t("verify.notFound")}
      {result.suggestion && (
        <span className="text-muted">
          {t("verify.didYouMean", { suggestion: result.suggestion })}
        </span>
      )}
      {result.details && <span className="text-muted">({result.details})</span>}
    </span>
  );
}

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
  // Verification is presentation-only state local to this card: the draft-row
  // result, the row currently verifying, and per-row results keyed by symbol.
  const [draftVerify, setDraftVerify] = useState<VerifyOutcome | null>(null);
  const [verifying, setVerifying] = useState<string | null>(null);
  const [rowVerify, setRowVerify] = useState<Record<string, VerifyOutcome>>({});
  const [manualDrafts, setManualDrafts] = useState<Record<string, string>>({});
  // IB-only structured contract for the draft instrument plus the live-search
  // state. Both are presentation-only and reset after a successful add.
  const [contractDraft, setContractDraft] =
    useState<IBContract>(newEmptyIBContract);
  const [resolveQuery, setResolveQuery] = useState("");
  const [resolveResults, setResolveResults] = useState<MarketDataSymbolMatch[]>(
    [],
  );
  const [resolveFilterQuery, setResolveFilterQuery] = useState("");
  const [resolving, setResolving] = useState(false);
  const [resolved, setResolved] = useState(false);
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
    setResolveQuery("");
    setResolveResults([]);
    setResolveFilterQuery("");
    setResolved(false);
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

  const runResolve = async () => {
    const query = resolveQuery.trim();
    if (query === "") {
      return;
    }
    setResolving(true);
    setResolved(true);
    setResolveResults([]);
    setResolveFilterQuery("");
    try {
      const matches = await onSearchSymbols(instance, { query });
      setResolveResults(matches ?? []);
    } finally {
      setResolving(false);
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

  // Draft-row verify: marker key "" tracks the draft slot in `verifying`.
  const verifyDraft = async () => {
    setVerifying("");
    setDraftVerify(null);
    try {
      // A null return means the call failed (the page handler already surfaced
      // the detail in the banner); keep an inline failed marker so the draft row
      // still reacts.
      const result = await onVerifySymbol(instance, draft.externalSymbol);
      setDraftVerify(result ?? { failed: true });
    } finally {
      setVerifying(null);
    }
  };

  const verifyRow = async (externalSymbol: string) => {
    setVerifying(externalSymbol);
    try {
      // Always record an outcome: a real verdict, or a failed marker when the
      // request errored, so the row never silently shows nothing after a click.
      const result = await onVerifySymbol(instance, externalSymbol);
      setRowVerify((prev) => ({
        ...prev,
        [externalSymbol]: result ?? { failed: true },
      }));
    } finally {
      setVerifying(null);
    }
  };

  const manualPriceValue = (externalSymbol: string, fallback: string): string =>
    manualDrafts[externalSymbol] ?? fallback;

  const handleResolveQueryChange = (value: string) => {
    setResolveQuery(value);
    setResolveResults([]);
    setResolveFilterQuery("");
    setResolved(false);
  };

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
      <CardHeader className="flex-row items-center justify-between gap-3">
        <div className="flex min-w-0 items-start gap-3">
          <ProviderIcon
            type={instance.provider}
            className="mt-0.5 h-5 w-5 shrink-0 text-accent"
          />
          <div className="min-w-0">
            <CardTitle className="truncate">
              {providerTitle(instance)}
            </CardTitle>
            <p className="mt-1 max-w-3xl text-xs text-muted">
              {t(`providers.${instance.provider}.description`, {
                defaultValue: "",
              })}
            </p>
            <div className="mt-1 flex flex-wrap items-center gap-2">
              <Badge variant="neutral">{instance.provider}</Badge>
              <Badge variant={instance.enabled ? "ok" : "neutral"}>
                {instance.enabled ? t("state.enabled") : t("state.disabled")}
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
            </div>
            {instance.state === "error" && instance.error && (
              <p className="mt-1.5 text-xs text-[var(--danger)]">
                {t("instanceError", { message: instance.error })}
              </p>
            )}
            {(siteUrl || docsUrl || symbolsUrl) && (
              <div className="mt-1.5 flex flex-wrap gap-3">
                {siteUrl && (
                  <a
                    href={siteUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-1 text-xs text-muted hover:text-text"
                  >
                    <ExternalLink className="h-3 w-3" />
                    {t("references.site")}
                  </a>
                )}
                {docsUrl && (
                  <a
                    href={docsUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-1 text-xs text-muted hover:text-text"
                  >
                    <ExternalLink className="h-3 w-3" />
                    {t("references.docs")}
                  </a>
                )}
                {symbolsUrl && (
                  <a
                    href={symbolsUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-1 text-xs text-muted hover:text-text"
                  >
                    <ExternalLink className="h-3 w-3" />
                    {t("references.symbols")}
                  </a>
                )}
              </div>
            )}
          </div>
        </div>
        <div className="flex shrink-0 gap-2">
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
            {instance.enabled ? t("actions.disable") : t("actions.enable")}
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
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {!usesInstrumentDialog && canSearch && (
          <ResolvePanel
            id={`md-resolve-${instance.id}`}
            busy={busy}
            resolving={resolving}
            resolved={resolved}
            query={resolveQuery}
            results={resolveResults}
            filterQuery={resolveFilterQuery}
            onQueryChange={handleResolveQueryChange}
            onFilterChange={setResolveFilterQuery}
            onResolve={() => {
              void runResolve();
            }}
            onPick={pickResolvedMatch}
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
              {t("actions.addInstrument")}
            </Button>
          </div>
        ) : (
          <div
            className={
              isManual
                ? "grid gap-2 md:grid-cols-[1.1fr_0.8fr_0.8fr_0.8fr_auto_auto]"
                : "grid gap-2 md:grid-cols-[1.1fr_0.8fr_0.8fr_auto_auto]"
            }
          >
            <div className="flex gap-2">
              <Input
                value={draft.externalSymbol}
                onChange={(e) => {
                  const externalSymbol = e.target.value;
                  setDraft((d) => ({ ...d, externalSymbol }));
                  setDraftVerify(null);
                }}
                placeholder={t("instrument.externalSymbol")}
                disabled={busy}
              />
              {canVerify && (
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  aria-label={t("verify.action")}
                  disabled={
                    busy ||
                    draft.externalSymbol.trim() === "" ||
                    verifying === ""
                  }
                  onClick={() => {
                    void verifyDraft();
                  }}
                >
                  <SearchCheck />
                </Button>
              )}
            </div>
            <Input
              value={draft.baseAsset}
              onChange={(e) =>
                setDraft((d) => ({ ...d, baseAsset: e.target.value }))
              }
              placeholder={t("instrument.baseAsset")}
              disabled={busy}
            />
            <div className="flex items-center gap-2">
              <Input
                value={draft.quoteAsset}
                onChange={(e) =>
                  setDraft((d) => ({ ...d, quoteAsset: e.target.value }))
                }
                placeholder={t("instrument.quoteAsset")}
                disabled={busy}
              />
              <PairUsageWarning usages={draftPairUsages} />
            </div>
            {isManual && (
              <Input
                value={draft.manualPrice}
                inputMode="decimal"
                onChange={(e) =>
                  setDraft((d) => ({ ...d, manualPrice: e.target.value }))
                }
                placeholder={t("instrument.manualPrice")}
                disabled={busy}
              />
            )}
            <Button
              type="button"
              variant={draft.enabled ? "default" : "outline"}
              onClick={() => setDraft((d) => ({ ...d, enabled: !d.enabled }))}
              disabled={busy}
            >
              {draft.enabled ? t("state.enabled") : t("state.disabled")}
            </Button>
            <Button
              type="button"
              onClick={() => {
                void (isIB ? submitIBInstrument() : submitInstrument());
              }}
              disabled={busy || !instrumentDraftValid}
            >
              <Plus />
              {t("actions.addInstrument")}
            </Button>
          </div>
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
                  <ResolvePanel
                    id={`md-resolve-dialog-${instance.id}`}
                    busy={busy}
                    resolving={resolving}
                    resolved={resolved}
                    query={resolveQuery}
                    results={resolveResults}
                    filterQuery={resolveFilterQuery}
                    onQueryChange={handleResolveQueryChange}
                    onFilterChange={setResolveFilterQuery}
                    onResolve={() => {
                      void runResolve();
                    }}
                    onPick={pickResolvedMatch}
                  />
                )}
                <div className="grid items-end gap-3 md:grid-cols-[1fr_0.7fr_0.7fr_0.5fr]">
                  <div className="space-y-2">
                    <Label htmlFor={`md-add-${instance.id}-external`}>
                      {t("instrument.externalSymbol")}
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
                      {t("instrument.baseAsset")}
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
                        {t("instrument.quoteAsset")}
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
                  {t("actions.addInstrument")}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        )}

        {draftVerify && (
          <div className="-mt-2">
            <VerifyResult result={draftVerify} />
          </div>
        )}

        <div className="overflow-x-auto">
          <table className="w-full min-w-[840px] text-left text-sm">
            <thead className="border-b border-border text-xs uppercase text-muted">
              <tr>
                <th className="py-2 pr-3">{t("table.external")}</th>
                <th className="py-2 pr-3">{t("table.instrument")}</th>
                {isManual && (
                  <th className="py-2 pr-3">{t("table.manualPrice")}</th>
                )}
                <th className="py-2 pr-3">{t("table.price")}</th>
                <th className="py-2 pr-3">{t("table.asOf")}</th>
                <th className="py-2 pr-3">{t("table.state")}</th>
                <th className="py-2 pr-0 text-right">{t("table.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {instance.instruments.length === 0 && (
                <tr>
                  <td
                    className="py-5 text-center text-muted"
                    colSpan={isManual ? 7 : 6}
                  >
                    {t("emptyInstruments")}
                  </td>
                </tr>
              )}
              {instance.instruments.map((instrument) => {
                const metadata = instrumentMetadata(
                  instance,
                  instrument.externalSymbol,
                );
                const duplicatePairUsages = pairUsages(
                  pairUsageMap,
                  instrument.baseAsset,
                  instrument.quoteAsset,
                  {
                    instanceExternalId: instance.id,
                    externalSymbol: instrument.externalSymbol,
                  },
                );
                return (
                  <tr
                    key={instrument.externalSymbol}
                    className="border-b border-border"
                  >
                    <td className="py-2 pr-3 font-medium text-text">
                      <div>
                        {instrumentDisplaySymbol(
                          instance,
                          instrument.externalSymbol,
                        )}
                      </div>
                      {metadata && (
                        <div className="mt-0.5 text-xs font-normal text-muted">
                          {metadata}
                        </div>
                      )}
                    </td>
                    <td className="py-2 pr-3 text-muted">
                      <div className="flex items-center gap-1.5">
                        <span>
                          {instrument.baseAsset}/{instrument.quoteAsset}
                        </span>
                        <PairUsageWarning usages={duplicatePairUsages} />
                      </div>
                    </td>
                    {isManual && (
                      <td className="py-2 pr-3">
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
                      </td>
                    )}
                    <td className="nums py-2 pr-3 text-text">
                      {displayedPriceLabel(instrument) || "-"}
                    </td>
                    <td className="nums py-2 pr-3 text-muted">
                      {instrument.quote ? (
                        <span>
                          {formatDateTime(instrument.quote.asOf)}{" "}
                          {instrument.updateIntervalMs ? (
                            <span
                              className="text-xs"
                              title={t("table.updateIntervalTooltip")}
                            >
                              {formatCompactDuration(
                                instrument.updateIntervalMs,
                              )}
                            </span>
                          ) : null}
                        </span>
                      ) : (
                        "-"
                      )}
                    </td>
                    <td className="py-2 pr-3">
                      <div className="flex flex-wrap items-center gap-2">
                        <Badge
                          variant={
                            instrument.enabled && instance.enabled
                              ? "ok"
                              : "neutral"
                          }
                        >
                          {!instrument.enabled
                            ? t("state.disabled")
                            : instance.enabled
                              ? t("state.enabled")
                              : t("state.feedDisabled")}
                        </Badge>
                        {instance.enabled && instrument.stale && !isManual && (
                          <Badge variant="warn" title={t("state.staleTooltip")}>
                            {t("state.stale")}
                          </Badge>
                        )}
                        <VerifyResult
                          result={rowVerify[instrument.externalSymbol] ?? null}
                        />
                      </div>
                    </td>
                    <td className="py-2 pr-0">
                      <div className="flex justify-end gap-2">
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          onClick={() =>
                            onToggleInstrument(
                              instance,
                              instrument.externalSymbol,
                              !instrument.enabled,
                            )
                          }
                          disabled={busy}
                        >
                          {instrument.enabled
                            ? t("actions.disable")
                            : t("actions.enable")}
                        </Button>
                        {canVerify && (
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon"
                            aria-label={t("verify.action")}
                            onClick={() => {
                              void verifyRow(instrument.externalSymbol);
                            }}
                            disabled={
                              busy || verifying === instrument.externalSymbol
                            }
                          >
                            <SearchCheck />
                          </Button>
                        )}
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          aria-label={t("actions.deleteInstrument")}
                          onClick={() =>
                            isIB
                              ? onDeleteIBInstrument(
                                  instance,
                                  instrument.externalSymbol,
                                )
                              : onDeleteInstrument(
                                  instance,
                                  instrument.externalSymbol,
                                )
                          }
                          disabled={busy}
                        >
                          <Trash2 />
                        </Button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>

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

function DeleteInstrumentDialog({
  target,
  busy,
  onOpenChange,
  onSubmit,
}: {
  target: DeleteInstrumentTarget | null;
  busy: boolean;
  onOpenChange: (open: boolean) => void;
  onSubmit: () => void;
}) {
  const { t } = useTranslation("marketData");
  return (
    <Dialog open={target !== null} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("deleteInstrument.title")}</DialogTitle>
          <DialogDescription>
            {t("deleteInstrument.description", {
              symbol: target?.externalSymbol ?? "",
            })}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {t("deleteInstrument.cancel")}
          </Button>
          <Button
            onClick={onSubmit}
            disabled={busy}
            className="border-[var(--danger)] bg-[var(--danger)] text-bg hover:border-[var(--danger)] hover:bg-[var(--danger)]"
          >
            <Trash2 />
            {t("deleteInstrument.submit")}
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
      <button
        type="button"
        onClick={() => setGuideOpen(true)}
        className="flex w-full items-center justify-between gap-3 rounded-card border border-border bg-surface px-4 py-3 text-left transition-colors hover:bg-surface-hover"
      >
        <span className="flex items-center gap-2 text-sm font-medium text-text">
          <Plus className="h-4 w-4 shrink-0 text-accent" />
          {t("providers.addSource")}
          <span className="ml-1 flex items-center gap-1.5">
            {providerOptions.map((provider) => (
              <ProviderIcon
                key={provider.type}
                type={provider.type}
                className="h-4 w-4 shrink-0"
              />
            ))}
          </span>
        </span>
        <span className="flex items-center gap-1.5 text-xs text-muted">
          {t("providers.count", { n: providerOptions.length })}
          <ChevronRight className="h-3.5 w-3.5" />
        </span>
      </button>
      <ProviderGuideDialog
        open={guideOpen}
        providers={providerOptions}
        onSelect={(provider) => {
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
            target={deleteInstrumentTarget}
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
