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

import { useEffect, useId, useRef, useState, type Ref } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import {
  AlertTriangle,
  ArrowUp,
  ArrowDown,
  Check,
  Clock,
  ChevronDown,
  ChevronUp,
  RefreshCw,
  Settings,
  Split,
  Trash2,
  X,
} from "lucide-react";

import type {
  CredentialStorage,
  TradingAccessGrant,
  TradingAccessSubject,
  TradingConnection,
  TradingConnectionCheck,
  TradingConnectionState,
  TradingMode,
  TradingProvider,
  TradingSymbolVerification,
  TestExecutionMode,
  TestExecutionPolicyState,
  TestExecutionRule,
  TestPriceVariant,
} from "@/api/types";
import { useCredentialStorage } from "@/api/useCredentialStorage";
import type { LoadState } from "@/api/usePolling";
import { useTrading } from "@/api/useTrading";
import { Autocomplete } from "@/components/Autocomplete";
import { ClearableInput } from "@/components/ClearableInput";
import { CodeSuggestionFailure } from "@/components/CodeSuggestionFailure";
import { SecretInput } from "@/components/SecretInput";
import { SecretTextarea } from "@/components/SecretTextarea";
import { ChoiceCards } from "@/components/ChoiceCards";
import { Notice } from "@/components/Notice";
import { AssetCodeField } from "@/components/AssetCodeField";
import { DeleteConnectionDialog } from "@/components/venues/DeleteConnectionDialog";
import { Segmented } from "@/components/data";
import {
  AccessBlockedHint,
  accessGrantBlock,
  AddProviderButton,
  DiagnosticPanel,
  ProviderGuideDialog,
  VenueCardHeader,
  SymbolSearchPanel,
  useSymbolSearch,
  useRowVerification,
  InstrumentDraftRow,
  VerifyResult,
  InstrumentsTable,
  DeleteInstrumentDialog,
  LinkButton,
  RevokeAccessDialog,
  UnsealedKeysWarning,
  VenueAccessList,
  venueSymbolPair,
  emptyInstrumentDraft,
  isInstrumentDraftFieldsValid,
  type AccessGrantBlock,
} from "@/components/venues";
import {
  isOptionalJSONInt64,
  putIfFilled,
  putNumberIfFilled,
  settingString,
} from "@/components/venues/providerSettings";
import { Page } from "@/components/Page";
import {
  EmptyState,
  ErrorBanner,
  ErrorState,
  StaleState,
  TableSkeleton,
} from "@/components/PageStates";
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { NumberStepper } from "@/components/ui/number-stepper";
import { Label } from "@/components/ui/label";
import {
  ApiError,
  AUTOCOMPLETE_SUGGESTION_LIMIT,
  useOfficerApi,
} from "@/framework";
import { accountScope } from "@/lib/accountScope";
import { useAccountCodeSuggestions } from "@/lib/useAccountCodeSuggestions";
import { useAssetCodeSuggestions } from "@/lib/useAssetCodeSuggestions";
import { useGroupCodeSuggestions } from "@/lib/useGroupCodeSuggestions";
import {
  DEFAULT_SEARCH_DEBOUNCE_MS,
  useDebouncedValue,
} from "@/lib/useDebounce";

const CONNECTION_STATE_BADGE: Record<
  TradingConnectionState,
  "ok" | "warn" | "danger" | "neutral"
> = {
  running: "ok",
  degraded: "warn",
  unavailable: "danger",
  stopped: "neutral",
};

type TradingGuideStep = {
  textKey: string;
  /** The step's text while accounts are hidden, when it differs. */
  singleAccountTextKey?: string;
  values?: Record<string, string>;
  links: Array<{ labelKey: string; href: string }>;
};

const IB_DOWNLOAD_URL =
  "https://www.interactivebrokers.com/en/trading/ibgateway-stable.php";
const IB_DOCS_URL = "https://www.interactivebrokers.com/docs/tws-api/doc/";
const IB_API_SETTINGS = {
  settingsPath: "Configure > Settings > API > Settings",
  socketClients: "Enable ActiveX and Socket Clients",
  readOnlyApi: "Read-Only API",
  trustedIPs: "Trusted IPs",
};
const IB_KEEP_RUNNING: TradingGuideStep = {
  textKey: "create.ib.keepRunning",
  links: [],
};
const ALPACA_SIGN_UP = {
  labelKey: "create.alpaca.signUp",
  href: "https://app.alpaca.markets/signup",
};
const ALPACA_LOG_IN = {
  labelKey: "create.alpaca.logIn",
  href: "https://app.alpaca.markets/account/login",
};
const ALPACA_OPEN_DASHBOARD = {
  labelKey: "create.alpaca.openDashboard",
  href: "https://app.alpaca.markets/dashboard/overview",
};
const ALPACA_PAPER_DOCS_URL =
  "https://docs.alpaca.markets/us/docs/paper-trading";
const ALPACA_LIVE_DOCS_URL =
  "https://docs.alpaca.markets/us/docs/authentication";
const ALPACA_KEEP_DASHBOARD_OPEN: TradingGuideStep = {
  textKey: "create.alpaca.keepDashboardOpen",
  links: [],
};
const BINANCE_TESTNET_URL = "https://testnet.binance.vision/";
const BINANCE_LIVE_DOCS_URL =
  "https://www.binance.com/en/support/faq/how-to-create-api-keys-on-binance-360002502072";
const BINANCE_KEEP_PAGE_OPEN: TradingGuideStep = {
  textKey: "create.binance.keepPageOpen",
  links: [],
};
const BYBIT_DOCS_URL = "https://bybit-exchange.github.io/docs/v5/guide";
const BYBIT_GLOBAL_API_KEY_GUIDE = {
  labelKey: "create.bybit.globalApiKeyGuide",
  href: "https://www.bybit.com/en/help-center/article/How-to-create-your-API-key",
};
const BYBIT_EU_API_KEY_GUIDE = {
  labelKey: "create.bybit.euApiKeyGuide",
  href: "https://www.bybit.eu/en-EU/learn/bybit-guide/what-is-api-trading-and-how-to-set-it-up-on-bybit-eu",
};
const BYBIT_KEEP_PAGE_OPEN: TradingGuideStep = {
  textKey: "create.bybit.keepPageOpen",
  links: [],
};
const KEEP_DIALOG_OPEN: TradingGuideStep = {
  textKey: "create.keepDialogOpen",
  links: [],
};
const ETORO_DOCS_URL =
  "https://api-portal.etoro.com/core/getting-started/authentication";
const ETORO_LOG_IN: TradingGuideStep = {
  textKey: "create.etoro.logInAccount",
  links: [
    { labelKey: "create.etoro.logIn", href: "https://www.etoro.com/login" },
  ],
};
// Steps that hold for a demo and a real eToro account alike.
const ETORO_COPY_USER_KEY: TradingGuideStep = {
  textKey: "create.etoro.copyUserKey",
  links: [],
};
const ETORO_PUBLIC_API_KEY: TradingGuideStep = {
  textKey: "create.etoro.publicApiKey",
  links: [
    { labelKey: "create.etoro.authenticationGuide", href: ETORO_DOCS_URL },
  ],
};

const COINBASE_DOCS_URL =
  "https://docs.cdp.coinbase.com/coinbase-app/authentication-authorization/api-key-authentication";

const OKX_PAPER_DOCS_URL =
  "https://www.okx.com/docs-v5/en/#overview-demo-trading-services";
const OKX_LIVE_DOCS_URL =
  "https://www.okx.com/docs-v5/en/#overview-api-key-creation";
const OKX_KEEP_PAGE_OPEN: TradingGuideStep = {
  textKey: "create.okx.keepPageOpen",
  links: [],
};

const OPENALGO_INSTALL: TradingGuideStep = {
  textKey: "create.openalgo.install",
  values: { selectBroker: "Select Broker", loginWith: "Login with" },
  links: [
    {
      labelKey: "create.openalgo.installationGuide",
      href: "https://docs.openalgo.in/installation-guidelines/getting-started",
    },
    {
      labelKey: "create.openalgo.brokerConfiguration",
      href: "https://docs.openalgo.in/connect-brokers/broker-configuration",
    },
  ],
};
const OPENALGO_API_KEY: TradingGuideStep = {
  textKey: "create.openalgo.apiKey",
  values: {
    apiKeyPage: "API Key",
    orderMode: "Order Execution Mode",
    auto: "Auto",
    semiAuto: "Semi-Auto",
    actionCenter: "Action Center",
  },
  links: [
    {
      labelKey: "create.openalgo.orderExecutionModes",
      href: "https://docs.openalgo.in/new-features/action-center",
    },
  ],
};
const OPENALGO_ADDRESSES: TradingGuideStep = {
  textKey: "create.openalgo.addresses",
  values: { hostServer: "HOST_SERVER", websocketUrl: "WEBSOCKET_URL" },
  links: [],
};
const OPENALGO_ANALYZER_LINK = {
  labelKey: "create.openalgo.aboutAnalyzer",
  href: "https://docs.openalgo.in/new-features/api-analyzer",
};

/**
 * Venue links and account preparation steps for each supported trading mode.
 * The provider picker links the docs for the mode its draft opens in.
 */
const TRADING_PROVIDER_GUIDES: Record<
  string,
  {
    siteUrl?: string;
    docsUrl?: Partial<Record<TradingMode, string>>;
    steps?: Partial<Record<TradingMode, TradingGuideStep[]>>;
  }
> = {
  openalgo: {
    docsUrl: {
      paper: OPENALGO_ANALYZER_LINK.href,
      live: "https://docs.openalgo.in/installation-guidelines/getting-started",
    },
    steps: {
      paper: [
        OPENALGO_INSTALL,
        {
          textKey: "create.openalgo.paperMode",
          values: { analyzer: "Analyzer" },
          links: [OPENALGO_ANALYZER_LINK],
        },
        OPENALGO_API_KEY,
        OPENALGO_ADDRESSES,
        {
          textKey: "create.openalgo.paperTradingDay",
          values: { analyzer: "Analyzer" },
          links: [],
        },
      ],
      live: [
        OPENALGO_INSTALL,
        {
          textKey: "create.openalgo.liveMode",
          values: { analyzer: "Analyzer" },
          links: [OPENALGO_ANALYZER_LINK],
        },
        OPENALGO_API_KEY,
        OPENALGO_ADDRESSES,
        { textKey: "create.openalgo.liveTradingDay", links: [] },
      ],
    },
  },
  test: {
    steps: {
      paper: [{ textKey: "create.test.prepare", links: [] }],
      live: [],
    },
  },
  ib: {
    siteUrl: "https://www.interactivebrokers.com",
    docsUrl: { paper: IB_DOCS_URL, live: IB_DOCS_URL },
    steps: {
      paper: [
        {
          textKey: "create.ib.install",
          links: [{ labelKey: "create.ib.download", href: IB_DOWNLOAD_URL }],
        },
        {
          textKey: "create.ib.enableApi",
          values: IB_API_SETTINGS,
          links: [{ labelKey: "create.ib.documentation", href: IB_DOCS_URL }],
        },
        IB_KEEP_RUNNING,
        {
          textKey: "create.ib.grantAccess",
          singleAccountTextKey: "create.ib.setVenueAccount",
          values: { paperAccountPrefix: "DU" },
          links: [],
        },
      ],
      live: [
        {
          textKey: "create.ib.live.install",
          links: [{ labelKey: "create.ib.download", href: IB_DOWNLOAD_URL }],
        },
        {
          textKey: "create.ib.live.enableApi",
          values: IB_API_SETTINGS,
          links: [{ labelKey: "create.ib.documentation", href: IB_DOCS_URL }],
        },
        IB_KEEP_RUNNING,
        {
          textKey: "create.ib.live.grantAccess",
          singleAccountTextKey: "create.ib.live.setVenueAccount",
          links: [],
        },
      ],
    },
  },
  alpaca: {
    siteUrl: "https://alpaca.markets",
    docsUrl: { paper: ALPACA_PAPER_DOCS_URL, live: ALPACA_LIVE_DOCS_URL },
    steps: {
      paper: [
        {
          textKey: "create.alpaca.createAccount",
          links: [ALPACA_SIGN_UP, ALPACA_LOG_IN],
        },
        {
          textKey: "create.alpaca.openPaperAccount",
          links: [
            ALPACA_OPEN_DASHBOARD,
            {
              labelKey: "create.alpaca.aboutPaperTrading",
              href: ALPACA_PAPER_DOCS_URL,
            },
          ],
        },
        {
          textKey: "create.alpaca.generateKeys",
          links: [],
        },
        ALPACA_KEEP_DASHBOARD_OPEN,
      ],
      live: [
        {
          textKey: "create.alpaca.live.openAccount",
          links: [ALPACA_SIGN_UP, ALPACA_LOG_IN],
        },
        {
          textKey: "create.alpaca.live.generateKeys",
          links: [
            ALPACA_OPEN_DASHBOARD,
            {
              labelKey: "create.alpaca.live.aboutCredentials",
              href: ALPACA_LIVE_DOCS_URL,
            },
          ],
        },
        ALPACA_KEEP_DASHBOARD_OPEN,
      ],
    },
  },
  binance: {
    siteUrl: "https://www.binance.com",
    docsUrl: { paper: BINANCE_TESTNET_URL, live: BINANCE_LIVE_DOCS_URL },
    steps: {
      paper: [
        {
          textKey: "create.binance.openTestnet",
          links: [
            {
              labelKey: "create.binance.openSpotTestnet",
              href: BINANCE_TESTNET_URL,
            },
          ],
        },
        {
          textKey: "create.binance.generateKeys",
          links: [],
        },
        BINANCE_KEEP_PAGE_OPEN,
      ],
      live: [
        {
          textKey: "create.binance.live.prepareAccount",
          links: [
            {
              labelKey: "create.apiKeyGuide",
              href: BINANCE_LIVE_DOCS_URL,
            },
          ],
        },
        {
          textKey: "create.binance.live.createKey",
          values: {
            account: "Account",
            apiManagement: "API Management",
            createApi: "Create API",
            systemGenerated: "System generated",
          },
          links: [],
        },
        {
          textKey: "create.binance.live.restrictKey",
          values: {
            editRestrictions: "Edit restrictions",
            trustedIPs: "Restrict access to trusted IPs only",
            spotTrading: "Enable Spot & Margin Trading",
          },
          links: [],
        },
        BINANCE_KEEP_PAGE_OPEN,
      ],
    },
  },
  coinbase: {
    siteUrl: "https://www.coinbase.com",
    docsUrl: { live: COINBASE_DOCS_URL },
    steps: {
      live: [
        {
          textKey: "create.coinbase.logIn",
          values: {
            portal: "Coinbase Developer Platform",
            live: "Live",
            sandbox: "Sandbox",
          },
          links: [
            {
              labelKey: "create.coinbase.openPortal",
              href: "https://portal.cdp.coinbase.com",
            },
            {
              labelKey: "create.coinbase.authenticationGuide",
              href: COINBASE_DOCS_URL,
            },
          ],
        },
        {
          textKey: "create.coinbase.createKey",
          values: {
            apiKeys: "API Keys",
            secretApiKeys: "Secret API Keys",
            createApiKey: "Create API key",
            nickname: "API key nickname",
            ipAllowlist: "IP allowlist",
          },
          links: [],
        },
        {
          textKey: "create.coinbase.restrictKey",
          values: {
            permissions: "Permissions",
            advancedTrade: "Coinbase App & Advanced Trade",
            read: "Read",
            trade: "Trade",
            transfer: "Transfer",
            signatureAlgorithm: "Signature algorithm",
            ecdsa: "ECDSA",
            ed25519: "Ed25519",
            create: "Create",
          },
          links: [
            {
              labelKey: "create.coinbase.keyTypes",
              href: `${COINBASE_DOCS_URL}#creating-api-keys`,
            },
          ],
        },
        {
          textKey: "create.coinbase.copyKeys",
          values: {
            apiKeyId: "API key ID",
            secret: "Secret",
            downloadApiKey: "Download API key",
          },
          links: [],
        },
        KEEP_DIALOG_OPEN,
      ],
    },
  },
  etoro: {
    siteUrl: "https://www.etoro.com",
    docsUrl: { paper: ETORO_DOCS_URL, live: ETORO_DOCS_URL },
    steps: {
      paper: [
        ETORO_LOG_IN,
        { textKey: "create.etoro.createUserKey", links: [] },
        ETORO_COPY_USER_KEY,
        ETORO_PUBLIC_API_KEY,
        KEEP_DIALOG_OPEN,
      ],
      live: [
        ETORO_LOG_IN,
        { textKey: "create.etoro.live.createUserKey", links: [] },
        ETORO_COPY_USER_KEY,
        ETORO_PUBLIC_API_KEY,
        KEEP_DIALOG_OPEN,
      ],
    },
  },
  bybit: {
    siteUrl: "https://www.bybit.com",
    docsUrl: { paper: BYBIT_DOCS_URL, live: BYBIT_DOCS_URL },
    steps: {
      paper: [
        {
          textKey: "create.bybit.openTestnet",
          links: [
            {
              labelKey: "create.bybit.openGlobalTestnet",
              href: "https://testnet.bybit.com/",
            },
            {
              labelKey: "create.bybit.openEuTestnet",
              href: "https://testnet.bybit.eu/en-EU/",
            },
          ],
        },
        {
          textKey: "create.bybit.generateKeys",
          links: [
            {
              labelKey: "create.bybit.openGlobalApiManagement",
              href: "https://testnet.bybit.com/app/user/api-management",
            },
            {
              labelKey: "create.bybit.openEuApiManagement",
              href: "https://testnet.bybit.eu/en-EU/app/user/api-management",
            },
            BYBIT_GLOBAL_API_KEY_GUIDE,
            BYBIT_EU_API_KEY_GUIDE,
          ],
        },
        BYBIT_KEEP_PAGE_OPEN,
      ],
      live: [
        {
          textKey: "create.bybit.generateKeys",
          links: [
            {
              labelKey: "create.bybit.openGlobalApiManagement",
              href: "https://www.bybit.com/app/user/api-management",
            },
            BYBIT_GLOBAL_API_KEY_GUIDE,
          ],
        },
        { textKey: "create.bybit.liveKeepPageOpen", links: [] },
      ],
    },
  },
  okx: {
    siteUrl: "https://www.okx.com",
    docsUrl: { paper: OKX_PAPER_DOCS_URL, live: OKX_LIVE_DOCS_URL },
    steps: {
      paper: [
        {
          textKey: "create.okx.openDemo",
          values: { trade: "Trade", demoTrading: "Demo Trading" },
          links: [
            {
              labelKey: "create.okx.demoTradingGuide",
              href: OKX_PAPER_DOCS_URL,
            },
          ],
        },
        {
          textKey: "create.okx.createDemoKey",
          values: {
            demoTradingApi: "Demo Trading API",
            createKey: "Create Demo Trading API Key",
            tradePermission: "Trade",
          },
          links: [],
        },
        OKX_KEEP_PAGE_OPEN,
      ],
      live: [
        {
          textKey: "create.okx.live.createKey",
          values: { tradePermission: "Trade" },
          links: [
            {
              labelKey: "create.apiKeyGuide",
              href: OKX_LIVE_DOCS_URL,
            },
          ],
        },
        { textKey: "create.okx.live.bindIp", links: [] },
        OKX_KEEP_PAGE_OPEN,
      ],
    },
  },
};

type TradingCredentialField = {
  key: string;
  labelKey: string;
  storedKey: string;
  notSetKey: string;
  multiline?: boolean;
};

// Alpaca, Binance and Bybit take an {apiKey, apiSecret} pair.
// OKX also requires a passphrase.
const API_KEY_SECRET_FIELDS: TradingCredentialField[] = [
  {
    key: "apiKey",
    labelKey: "venues:settings.apiKey",
    storedKey: "connection.apiKeyStored",
    notSetKey: "connection.apiKeyNotSet",
  },
  {
    key: "apiSecret",
    labelKey: "venues:settings.apiSecret",
    storedKey: "connection.apiSecretStored",
    notSetKey: "connection.apiSecretNotSet",
  },
];

// A known provider has settings, credentials, or both. Providers with neither
// are blocked: guessing fields could send the wrong credentials.
const TRADING_PROVIDER_CREDENTIAL_FIELDS = new Map<
  string,
  TradingCredentialField[]
>([
  ["test", []],
  ["alpaca", API_KEY_SECRET_FIELDS],
  ["binance", API_KEY_SECRET_FIELDS],
  ["bybit", API_KEY_SECRET_FIELDS],
  [
    "coinbase",
    [
      {
        key: "apiKeyId",
        labelKey: "settings.coinbase.apiKeyId",
        storedKey: "connection.coinbase.apiKeyIdStored",
        notSetKey: "connection.coinbase.apiKeyIdNotSet",
      },
      {
        key: "apiKeySecret",
        labelKey: "settings.coinbase.apiKeySecret",
        storedKey: "connection.coinbase.apiKeySecretStored",
        notSetKey: "connection.coinbase.apiKeySecretNotSet",
        multiline: true,
      },
    ],
  ],
  ["openalgo", [API_KEY_SECRET_FIELDS[0]]],
  [
    "okx",
    [
      ...API_KEY_SECRET_FIELDS,
      {
        key: "passphrase",
        labelKey: "settings.okx.passphrase",
        storedKey: "connection.okx.passphraseStored",
        notSetKey: "connection.okx.passphraseNotSet",
      },
    ],
  ],
  [
    "etoro",
    [
      {
        key: "apiKey",
        labelKey: "settings.etoro.apiKey",
        storedKey: "connection.etoro.apiKeyStored",
        notSetKey: "connection.etoro.apiKeyNotSet",
      },
      {
        key: "userKey",
        labelKey: "settings.etoro.userKey",
        storedKey: "connection.etoro.userKeyStored",
        notSetKey: "connection.etoro.userKeyNotSet",
      },
    ],
  ],
]);

type AccountSuggestionLookup = {
  code: string;
  suggestions: string[];
  error: string;
};

type AccessSubjectKind = "account" | "group";

/** IB Gateway's default socket port for each trading mode. */
const IB_GATEWAY_PORTS: Record<TradingMode, string> = {
  paper: "4002",
  live: "4001",
};

type TradingSettingField = {
  key: string;
  labelKey: string;
  kind: "text" | "port" | "optionalInt64";
  placeholder?: string;
  placeholderKey?: string;
  hint?: { create?: string; settings: string };
  fixed?: boolean;
};

const TRADING_PROVIDER_SETTING_FIELDS = new Map<string, TradingSettingField[]>([
  [
    "ib",
    [
      { key: "host", labelKey: "marketData:settings.host", kind: "text" },
      { key: "port", labelKey: "marketData:settings.port", kind: "port" },
      {
        key: "clientId",
        labelKey: "marketData:settings.clientId",
        kind: "optionalInt64",
        placeholderKey: "marketData:settings.autoClientId",
      },
    ],
  ],
  [
    "openalgo",
    [
      {
        key: "url",
        labelKey: "settings.openalgo.url",
        kind: "text",
        placeholder: "http://127.0.0.1:5000",
        fixed: true,
        hint: {
          create: "settings.openalgo.urlCreateHint",
          settings: "settings.openalgo.urlHint",
        },
      },
      {
        key: "wsUrl",
        labelKey: "settings.openalgo.wsUrl",
        kind: "text",
        placeholder: "ws://127.0.0.1:8765",
        hint: { settings: "settings.openalgo.addressKeyHint" },
      },
    ],
  ],
]);

function settingsReady(
  fields: TradingSettingField[],
  draft: Record<string, string>,
): boolean {
  return fields.every(({ key, kind }) => {
    if (kind === "optionalInt64") return isOptionalJSONInt64(draft[key]);
    if (kind === "text") return draft[key].trim() !== "";
    const port = Number(draft[key]);
    return Number.isInteger(port) && port >= 1 && port <= 65535;
  });
}

const OKX_SITES = [
  { value: "www.okx.com", labelKey: "settings.okx.sites.global" },
  { value: "my.okx.com", labelKey: "settings.okx.sites.eea" },
  { value: "app.okx.com", labelKey: "settings.okx.sites.us" },
];

const BYBIT_PAPER_SITES = [
  { value: "global", labelKey: "create.bybit.openGlobalTestnet" },
  { value: "eu", labelKey: "create.bybit.openEuTestnet" },
];

function ConnectionSiteField({
  id,
  provider,
  site,
  busy,
  onChange,
}: {
  id: string;
  provider: "okx" | "bybit";
  site: string;
  busy: boolean;
  onChange: (site: string) => void;
}) {
  const { t } = useTranslation("trading");
  const isOKX = provider === "okx";
  const sites = isOKX ? OKX_SITES : BYBIT_PAPER_SITES;
  return (
    <div className="flex flex-col gap-y-2">
      <Label htmlFor={`${id}-site`}>
        {t(isOKX ? "settings.okx.site" : "create.bybit.site")}
      </Label>
      <Select value={site} onValueChange={onChange} disabled={busy} required>
        <SelectTrigger id={`${id}-site`}>
          <SelectValue
            placeholder={t(
              isOKX
                ? "settings.okx.sitePlaceholder"
                : "create.bybit.sitePlaceholder",
            )}
          />
        </SelectTrigger>
        <SelectContent>
          {sites.map(({ value, labelKey }) => (
            <SelectItem key={value} value={value}>
              {t(labelKey, { site: value })}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

function putSettingIfFilled(
  credentials: Record<string, string | number>,
  field: TradingSettingField,
  value: string,
) {
  if (field.kind === "text") putIfFilled(credentials, field.key, value);
  else putNumberIfFilled(credentials, field.key, value);
}

function ConnectionSettingFields({
  id,
  fields,
  draft,
  busy,
  onChange,
  existing = false,
}: {
  id: string;
  fields: TradingSettingField[];
  draft: Record<string, string>;
  busy: boolean;
  onChange: (patch: Record<string, string>) => void;
  existing?: boolean;
}) {
  const { t } = useTranslation("trading");
  return (
    <div className="grid gap-3 md:grid-cols-2">
      {fields.map((field) => {
        const fieldId = `${id}-${field.key.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)}`;
        const hint = field.hint?.[existing ? "settings" : "create"];
        return (
          <div key={field.key} className="flex flex-col gap-y-2">
            <Label htmlFor={fieldId}>{t(field.labelKey)}</Label>
            {field.kind === "text" ? (
              <Input
                id={fieldId}
                value={draft[field.key]}
                placeholder={
                  field.placeholderKey
                    ? t(field.placeholderKey)
                    : field.placeholder
                }
                readOnly={existing && field.fixed}
                aria-describedby={hint ? `${fieldId}-hint` : undefined}
                onChange={(event) =>
                  onChange({ [field.key]: event.target.value })
                }
                disabled={busy}
              />
            ) : (
              <NumberStepper
                id={fieldId}
                integerOnly
                min={field.kind === "port" ? "1" : undefined}
                max={field.kind === "port" ? 65535 : undefined}
                value={draft[field.key]}
                placeholder={
                  field.placeholderKey
                    ? t(field.placeholderKey)
                    : field.placeholder
                }
                onChange={(value) => onChange({ [field.key]: value })}
                disabled={busy}
              />
            )}
            {hint && (
              <p id={`${fieldId}-hint`} className="text-xs text-muted">
                {t(hint)}
              </p>
            )}
          </div>
        );
      })}
    </div>
  );
}

/** Card links of a connection: the venue docs for its trading mode. */
function tradingConnectionLinks(provider: string, mode: TradingMode) {
  const guide = TRADING_PROVIDER_GUIDES[provider];
  return { siteUrl: guide?.siteUrl, docsUrl: guide?.docsUrl?.[mode] };
}

function draftMode(provider: TradingProvider): TradingMode {
  return provider.liveOnly ? "live" : "paper";
}

/** A labelled, masked credential field with a Show/Hide toggle. */
function CredentialField({
  id,
  label,
  multiline,
  value,
  onChange,
  disabled,
  required,
  placeholder,
}: {
  id: string;
  label: string;
  multiline?: boolean;
  value: string;
  onChange: (value: string) => void;
  disabled: boolean;
  required?: boolean;
  placeholder?: string;
}) {
  if (!multiline) {
    return (
      <SecretInput
        id={id}
        label={label}
        value={value}
        onChange={onChange}
        disabled={disabled}
        required={required}
        placeholder={placeholder}
        autoComplete="new-password"
      />
    );
  }
  return (
    <div className="flex flex-col gap-y-2">
      <Label htmlFor={id}>{label}</Label>
      <SecretTextarea
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        disabled={disabled}
        required={required}
        placeholder={placeholder}
      />
    </div>
  );
}

function AddTradingConnectionDialog({
  provider,
  credentialStorage,
  onClose,
  onCreated,
  reload,
}: {
  provider: TradingProvider;
  credentialStorage: LoadState<CredentialStorage>;
  onClose: () => void;
  onCreated: (connectionId: string) => void;
  reload: () => void;
}) {
  const { t } = useTranslation("trading");
  const { t: tc } = useTranslation();
  const { t: tv } = useTranslation("venues");
  const { createTradingConnection } = useOfficerApi();
  const id = useId();
  const fields = TRADING_PROVIDER_CREDENTIAL_FIELDS.get(provider.type);
  const settingFields = TRADING_PROVIDER_SETTING_FIELDS.get(provider.type);
  const [label, setLabel] = useState(provider.title);
  const [createdWithError, setCreatedWithError] = useState(false);
  const [mode, setMode] = useState<TradingMode>(draftMode(provider));
  const [credentialDraft, setCredentialDraft] = useState<
    Record<string, string>
  >(() => Object.fromEntries(fields ? fields.map(({ key }) => [key, ""]) : []));
  const [settingDraft, setSettingDraft] = useState<Record<string, string>>(
    () =>
      provider.type === "ib"
        ? { host: "127.0.0.1", port: IB_GATEWAY_PORTS.paper, clientId: "" }
        : Object.fromEntries(
            settingFields ? settingFields.map(({ key }) => [key, ""]) : [],
          ),
  );
  // The prefilled gateway port follows the mode; a port the operator typed
  // stays as typed.
  const changeMode = (next: TradingMode) => {
    setMode(next);
    setSettingDraft((draft) =>
      provider.type === "ib" && draft.port === IB_GATEWAY_PORTS[mode]
        ? { ...draft, port: IB_GATEWAY_PORTS[next] }
        : draft,
    );
  };
  const siteProvider =
    provider.type === "okx" || (provider.type === "bybit" && mode === "paper")
      ? provider.type
      : null;
  const [site, setSite] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const errorBanner = useRef<HTMLDivElement>(null);
  const guide = TRADING_PROVIDER_GUIDES[provider.type];
  const ready =
    label.trim() !== "" &&
    (siteProvider === null || site !== "") &&
    (settingFields !== undefined || fields !== undefined) &&
    (settingFields === undefined ||
      settingsReady(settingFields, settingDraft)) &&
    (fields === undefined ||
      fields.every(({ key }) => credentialDraft[key].trim() !== ""));

  useEffect(() => {
    if (error !== "") errorBanner.current?.scrollIntoView({ block: "nearest" });
  }, [error]);

  const submit = async () => {
    if (busy || createdWithError || !ready) return;
    setBusy(true);
    setError("");
    try {
      const credentials: Record<string, string | number> = {};
      for (const field of settingFields ?? []) {
        putSettingIfFilled(credentials, field, settingDraft[field.key]);
      }
      if (fields) {
        for (const { key } of fields) {
          credentials[key] = credentialDraft[key].trim();
        }
      }
      if (siteProvider !== null) credentials.site = site;
      const connection = await createTradingConnection({
        provider: provider.type,
        label: label.trim(),
        mode,
        credentials,
        enabled: false,
      });
      onClose();
      onCreated(connection.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      if (
        err instanceof ApiError &&
        (err.code === "trading_access_notification_failed" ||
          err.code === "trading_access_completion_failed")
      ) {
        setCreatedWithError(true);
        reload();
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next && !busy) onClose();
      }}
    >
      <DialogContent
        className="max-h-[90vh] overflow-y-auto sm:max-w-2xl"
        hideClose={busy}
        aria-describedby={undefined}
      >
        <DialogHeader>
          <DialogTitle>
            {t("create.title", { provider: provider.title })}
          </DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          {!settingFields && !fields && (
            <ErrorBanner message={t("settings.unknownProviderCredentials")} />
          )}
          <div className="flex flex-col gap-y-2">
            <Label htmlFor={`${id}-label`}>{tv("settings.label")}</Label>
            <Input
              id={`${id}-label`}
              value={label}
              autoFocus
              required
              disabled={busy}
              onChange={(event) => setLabel(event.target.value)}
            />
          </div>
          {provider.executionPolicy !== true && (
            <fieldset className="flex flex-col gap-y-2" disabled={busy}>
              <Label asChild>
                <legend>{t("create.mode")}</legend>
              </Label>
              <Segmented
                value={mode}
                onChange={(value) => changeMode(value as TradingMode)}
                options={
                  provider.liveOnly
                    ? [{ value: "live", label: t("mode.live") }]
                    : [
                        { value: "paper", label: t("mode.paper") },
                        { value: "live", label: t("mode.live") },
                      ]
                }
              />
            </fieldset>
          )}
          {fields?.length !== 0 && (
            <UnsealedKeysWarning storage={credentialStorage} />
          )}
          <div className="flex flex-col gap-y-3">
            <h2 className="text-sm font-bold">
              {t("create.prepareAccount", { provider: provider.title })}
            </h2>
            {guide?.steps?.[mode] && (
              <ol className="list-outside list-decimal flex flex-col gap-y-4 pl-5 text-sm">
                {guide.steps[mode].map((instruction) => (
                  <li
                    key={instruction.textKey}
                    className="flex flex-col gap-y-2 pl-1"
                  >
                    <p>
                      {t(
                        !accountScope().showsAccounts &&
                          instruction.singleAccountTextKey !== undefined
                          ? instruction.singleAccountTextKey
                          : instruction.textKey,
                        instruction.values,
                      )}
                    </p>
                    {instruction.links.length > 0 && (
                      <div className="flex flex-wrap gap-3">
                        {instruction.links.map((link) => (
                          <a
                            key={link.labelKey}
                            href={link.href}
                            target="_blank"
                            rel="noopener noreferrer"
                            className="text-accent underline underline-offset-4"
                          >
                            {t(link.labelKey)}
                          </a>
                        ))}
                      </div>
                    )}
                  </li>
                ))}
              </ol>
            )}
          </div>
          {settingFields && (
            <ConnectionSettingFields
              id={id}
              fields={settingFields}
              draft={settingDraft}
              busy={busy}
              onChange={(patch) =>
                setSettingDraft((draft) => ({ ...draft, ...patch }))
              }
            />
          )}
          {siteProvider !== null && (
            <ConnectionSiteField
              id={id}
              provider={siteProvider}
              site={site}
              busy={busy}
              onChange={setSite}
            />
          )}
          {fields?.map((field) => (
            <CredentialField
              key={field.key}
              id={`${id}-${field.key}`}
              label={t(field.labelKey)}
              multiline={field.multiline}
              value={credentialDraft[field.key]}
              required
              disabled={busy}
              onChange={(value) =>
                setCredentialDraft({
                  ...credentialDraft,
                  [field.key]: value,
                })
              }
            />
          ))}
          {error && (
            <div ref={errorBanner}>
              <ErrorBanner message={error} />
            </div>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={onClose}
            >
              {tc("actions.cancel")}
            </Button>
            <Button type="submit" disabled={busy || createdWithError || !ready}>
              {tc("actions.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ConnectionSettingsDialog({
  connection,
  credentialStorage,
  onClose,
  reload,
}: {
  connection: TradingConnection;
  credentialStorage: LoadState<CredentialStorage>;
  onClose: () => void;
  reload: () => void;
}) {
  const { t } = useTranslation("trading");
  const { t: tc } = useTranslation();
  const { t: tv } = useTranslation("venues");
  const { updateTradingConnectionSettings } = useOfficerApi();
  const id = useId();
  const fields = TRADING_PROVIDER_CREDENTIAL_FIELDS.get(connection.provider);
  const settingFields = TRADING_PROVIDER_SETTING_FIELDS.get(
    connection.provider,
  );
  const [initialLabel] = useState(connection.label);
  const [label, setLabel] = useState(initialLabel);
  const [credentialDraft, setCredentialDraft] = useState<
    Record<string, string>
  >(() => Object.fromEntries(fields ? fields.map(({ key }) => [key, ""]) : []));
  const isOKX = connection.provider === "okx";
  const [initialOKXSite] = useState(() =>
    settingString(connection.settings, "site"),
  );
  const [okxSite, setOKXSite] = useState(initialOKXSite);
  const siteChanged = isOKX && okxSite !== initialOKXSite;
  const [initialSettings] = useState(() =>
    Object.fromEntries(
      settingFields
        ? settingFields.map(({ key }) => [
            key,
            settingString(connection.settings, key),
          ])
        : [],
    ),
  );
  const [settingDraft, setSettingDraft] = useState(initialSettings);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const labelChanged = label.trim() !== "" && label.trim() !== initialLabel;
  const changed =
    (settingFields !== undefined || fields !== undefined) &&
    (settingFields === undefined ||
      (label.trim() !== "" && settingsReady(settingFields, settingDraft))) &&
    (connection.provider !== "openalgo" ||
      settingDraft.wsUrl.trim() === initialSettings.wsUrl.trim() ||
      credentialDraft.apiKey.trim() !== "") &&
    (labelChanged ||
      siteChanged ||
      settingFields?.some(
        ({ key }) => settingDraft[key] !== initialSettings[key],
      ) ||
      fields?.some(({ key }) => credentialDraft[key].trim() !== ""));

  const submit = async () => {
    if (busy || !changed) return;
    const body: {
      label?: string;
      credentials?: Record<string, string | number>;
    } = {};
    if (labelChanged) body.label = label.trim();
    const credentials: Record<string, string | number> = {};
    for (const field of settingFields ?? []) {
      if (settingDraft[field.key] !== initialSettings[field.key]) {
        putSettingIfFilled(credentials, field, settingDraft[field.key]);
      }
    }
    if (fields) {
      for (const { key } of fields) {
        const value = credentialDraft[key].trim();
        if (value !== "") credentials[key] = value;
      }
    }
    if (siteChanged) credentials.site = okxSite;
    if (Object.keys(credentials).length > 0) body.credentials = credentials;
    setBusy(true);
    setError("");
    try {
      await updateTradingConnectionSettings(connection.id, body);
      reload();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DialogContent
        className="max-h-[90vh] overflow-y-auto"
        {...(fields?.length === 0 ? { "aria-describedby": undefined } : {})}
      >
        <form
          className="flex flex-col gap-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <DialogHeader>
            <DialogTitle>{tc("actions.editSettings")}</DialogTitle>
            {fields?.length !== 0 && (
              <DialogDescription>{t("settings.description")}</DialogDescription>
            )}
          </DialogHeader>
          {error && <ErrorBanner message={error} />}
          {!settingFields && !fields && (
            <ErrorBanner message={t("settings.unknownProviderCredentials")} />
          )}
          <div className="flex flex-col gap-y-2">
            <Label htmlFor={`${id}-label`}>{tv("settings.label")}</Label>
            <Input
              id={`${id}-label`}
              value={label}
              autoFocus
              onChange={(event) => setLabel(event.target.value)}
              disabled={busy}
            />
          </div>
          {settingFields && (
            <ConnectionSettingFields
              id={id}
              fields={settingFields}
              draft={settingDraft}
              existing
              busy={busy}
              onChange={(patch) =>
                setSettingDraft((draft) => ({ ...draft, ...patch }))
              }
            />
          )}
          {fields && (
            <>
              {fields?.length !== 0 && (
                <UnsealedKeysWarning storage={credentialStorage} />
              )}
              {fields?.map((field) => (
                <CredentialField
                  key={field.key}
                  id={`${id}-${field.key}`}
                  label={t(field.labelKey)}
                  multiline={field.multiline}
                  value={credentialDraft[field.key]}
                  placeholder={
                    connection.secrets[field.key]
                      ? tv("settings.keepStoredSecret")
                      : ""
                  }
                  onChange={(value) =>
                    setCredentialDraft({
                      ...credentialDraft,
                      [field.key]: value,
                    })
                  }
                  disabled={busy}
                />
              ))}
            </>
          )}
          {isOKX && (
            <ConnectionSiteField
              id={id}
              provider="okx"
              site={okxSite}
              busy={busy}
              onChange={setOKXSite}
            />
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={onClose}
              disabled={busy}
            >
              {tc("actions.cancel")}
            </Button>
            <Button type="submit" disabled={busy || !changed}>
              {tc("actions.save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ConnectionCheckSection({
  executionPolicy,
  checkSection,
  busy,
  checking,
  checkConnection,
  check,
  checkError,
}: {
  executionPolicy: TradingConnection["executionPolicy"];
  checkSection: Ref<HTMLElement>;
  busy: boolean;
  checking: boolean;
  checkConnection: () => void;
  check: TradingConnectionCheck | null;
  checkError: string;
}) {
  const { t } = useTranslation("trading");
  return (
    <section
      ref={checkSection}
      tabIndex={-1}
      aria-label={t("actions.check")}
      className="flex flex-col gap-y-2"
    >
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={busy || checking}
        onClick={checkConnection}
      >
        {t("actions.check")}
      </Button>
      {check && (
        <p role="status" className="text-xs">
          {executionPolicy === true
            ? t("check.testResult")
            : t("check.result", {
                id: check.venueIdentity,
                status: check.venueStatus,
              })}
        </p>
      )}
      {checkError && <ErrorBanner message={checkError} />}
    </section>
  );
}

/** Optional venue-side account bound by a new access grant. */
function VenueAccountField({
  id,
  busy,
  venueAccount,
  setVenueAccount,
  inputRef,
}: {
  id: string;
  busy: boolean;
  venueAccount: string;
  setVenueAccount: (value: string) => void;
  inputRef: Ref<HTMLDivElement>;
}) {
  const { t: tv } = useTranslation("venues");
  const { t: tc } = useTranslation();
  return (
    <div ref={inputRef} className="flex flex-col gap-y-2">
      <Label htmlFor={`${id}-venue-account`}>{tv("venueAccount")}</Label>
      <ClearableInput
        id={`${id}-venue-account`}
        onClear={() => setVenueAccount("")}
        clearLabel={tc("filters.clearField")}
        value={venueAccount}
        disabled={busy}
        onChange={(event) => setVenueAccount(event.target.value)}
      />
    </div>
  );
}

function ConnectionAccessSection({
  accessSection,
  id,
  connection,
  provider,
  busy,
  subjectKind,
  setSubjectKind,
  subjectInput,
  setSubjectInput,
  subjectCode,
  venueAccount,
  setVenueAccount,
  venueAccountInput,
  feeBlock,
  focusFeeAccount,
  focusVenueAccount,
  grantAccess,
  onRevoke,
}: {
  accessSection: Ref<HTMLElement>;
  id: string;
  connection: TradingConnection;
  provider: TradingProvider;
  busy: boolean;
  subjectKind: AccessSubjectKind;
  setSubjectKind: (value: AccessSubjectKind) => void;
  subjectInput: string;
  setSubjectInput: (value: string) => void;
  subjectCode: string;
  venueAccount: string;
  setVenueAccount: (value: string) => void;
  venueAccountInput: Ref<HTMLDivElement>;
  feeBlock: AccessGrantBlock | null;
  focusFeeAccount: () => void;
  focusVenueAccount: () => void;
  grantAccess: () => Promise<void>;
  onRevoke: (subject: TradingAccessSubject) => void;
}) {
  const { t } = useTranslation("trading");
  const { t: tc } = useTranslation();
  const { t: tv } = useTranslation("venues");
  const accountSuggestions = useAccountCodeSuggestions(
    subjectInput,
    subjectKind === "account",
  );
  const groupSuggestions = useGroupCodeSuggestions(
    subjectInput,
    subjectKind === "group",
  );
  const suggestions =
    subjectKind === "account" ? accountSuggestions : groupSuggestions;
  return (
    <section
      ref={accessSection}
      tabIndex={-1}
      aria-labelledby={`${id}-access`}
      className="flex flex-col gap-y-3 border-t border-border pt-4"
    >
      <h4 id={`${id}-access`} className="text-sm font-medium">
        {t("access.title")}
      </h4>
      <VenueAccessList
        grants={connection.access}
        showVenueAccount={provider.venueAccounts}
        busy={busy}
        onRevoke={onRevoke}
      />
      <form
        aria-label={tv("actions.grantAccess")}
        className="flex flex-col gap-y-3"
        onSubmit={(event) => {
          event.preventDefault();
          void grantAccess();
        }}
      >
        <h5 className="text-xs font-medium">{tv("actions.grantAccess")}</h5>
        <fieldset className="flex flex-col gap-y-2" disabled={busy}>
          <Label asChild>
            <legend>{t("access.grantTo")}</legend>
          </Label>
          <Segmented
            value={subjectKind}
            options={[
              { value: "account", label: tc("fields.account") },
              { value: "group", label: tc("fields.group") },
            ]}
            onChange={(value) => setSubjectKind(value as AccessSubjectKind)}
          />
        </fieldset>
        <div className="flex flex-col gap-y-2">
          <Label htmlFor={`${id}-subject`}>
            {tc(subjectKind === "account" ? "fields.account" : "fields.group")}
          </Label>
          <Autocomplete
            id={`${id}-subject`}
            value={subjectInput}
            suggestions={suggestions.codes}
            disabled={busy}
            onChange={setSubjectInput}
            onClear={() => setSubjectInput("")}
            clearLabel={tc("filters.clearField")}
          />
          <CodeSuggestionFailure
            failed={suggestions.failed}
            kind={subjectKind}
          />
        </div>
        {provider.venueAccounts && (
          <VenueAccountField
            id={id}
            busy={busy}
            venueAccount={venueAccount}
            setVenueAccount={setVenueAccount}
            inputRef={venueAccountInput}
          />
        )}
        {feeBlock !== null && (
          <AccessBlockedHint
            reason={feeBlock}
            providerTitle={provider.title}
            onSetFeeAccount={
              feeBlock === "needsVenueAccount"
                ? focusVenueAccount
                : focusFeeAccount
            }
          />
        )}
        <Button
          type="submit"
          disabled={busy || subjectCode === "" || feeBlock !== null}
        >
          {tv("actions.grantAccess")}
        </Button>
      </form>
    </section>
  );
}

/** The grant giving the default account access to `connection` while
 *  accounts are hidden: its own, which wins, else one to its group - the only
 *  group the server lists then; undefined for none. */
function defaultAccountGrant(
  connection: TradingConnection,
): TradingAccessGrant | undefined {
  const { account } = accountScope();
  const own = connection.access.find((grant) => grant.account === account);
  if (own !== undefined) return own;
  return connection.access.find((grant) => grant.accountGroup !== "");
}

/** The venue account of the default account's grant while accounts are
 *  hidden, for a provider that binds one per grant: the server writes the
 *  grant of any other provider itself but cannot pick this account, so saving
 *  the field writes the grant. A grant to its group shows read-only. */
function DefaultVenueAccountSection({
  accessSection,
  id,
  busy,
  grant,
  venueAccount,
  setVenueAccount,
  venueAccountInput,
  saveVenueAccount,
}: {
  accessSection: Ref<HTMLElement>;
  id: string;
  busy: boolean;
  grant: TradingAccessGrant | undefined;
  venueAccount: string;
  setVenueAccount: (value: string) => void;
  venueAccountInput: Ref<HTMLDivElement>;
  saveVenueAccount: () => Promise<void>;
}) {
  const { t } = useTranslation("trading");
  const { t: tv } = useTranslation("venues");
  const { t: tc } = useTranslation();
  const inherited = grant !== undefined && grant.accountGroup !== "";
  return (
    <section
      ref={accessSection}
      tabIndex={-1}
      aria-labelledby={`${id}-access`}
      className="flex flex-col gap-y-3 border-t border-border pt-4"
    >
      <h4 id={`${id}-access`} className="text-sm font-medium">
        {tv("venueAccount")}
      </h4>
      {grant !== undefined && grant.venueAccount !== "" && (
        <p className="text-xs">
          {tv("accessDialog.venueAccount", {
            venueAccount: grant.venueAccount,
          })}
        </p>
      )}
      {inherited ? (
        <p className="text-xs text-muted">{t("access.inherited")}</p>
      ) : (
        <form
          aria-label={tv("venueAccount")}
          className="flex flex-col gap-y-3"
          onSubmit={(event) => {
            event.preventDefault();
            void saveVenueAccount();
          }}
        >
          <VenueAccountField
            id={id}
            busy={busy}
            venueAccount={venueAccount}
            setVenueAccount={setVenueAccount}
            inputRef={venueAccountInput}
          />
          <Button type="submit" disabled={busy || venueAccount.trim() === ""}>
            {tc("actions.save")}
          </Button>
        </form>
      )}
    </section>
  );
}

function ConnectionFeeAccountSection({
  id,
  connection,
  provider,
  busy,
  feeCode,
  setFeeCode,
  feeAccountCode,
  feeAccountSuggestions,
  feeLookup,
  feeInput,
  saveFeeAccount,
  onClearFeeAccount,
}: {
  id: string;
  connection: TradingConnection;
  provider: TradingProvider;
  busy: boolean;
  feeCode: string;
  setFeeCode: (value: string) => void;
  feeAccountCode: string;
  feeAccountSuggestions: AccountSuggestionLookup | null;
  feeLookup:
    | { code: string; exists: boolean }
    | { code: string; error: string }
    | null;
  feeInput: Ref<HTMLInputElement>;
  saveFeeAccount: (create: boolean) => Promise<void>;
  onClearFeeAccount: () => void;
}) {
  const { t } = useTranslation("trading");
  const { t: tc } = useTranslation();
  return (
    <section
      aria-labelledby={`${id}-fee`}
      className="flex flex-col gap-y-3 border-t border-border pt-4"
    >
      <h4 id={`${id}-fee`} className="text-sm font-medium">
        {t("fee.title")}
      </h4>
      <p className="text-xs text-muted">
        {t("fee.description", { provider: provider.title })}
      </p>
      <p className="text-xs">
        {connection.feeAccount === "" ? t("fee.notSet") : connection.feeAccount}
      </p>
      <form
        aria-label={t("fee.title")}
        className="flex flex-col gap-y-3"
        onSubmit={(event) => {
          event.preventDefault();
          void saveFeeAccount(false);
        }}
      >
        <div className="flex flex-col gap-y-2">
          <Label htmlFor={`${id}-fee-account`}>{t("fee.title")}</Label>
          <Autocomplete
            ref={feeInput}
            id={`${id}-fee-account`}
            value={feeCode}
            suggestions={
              feeAccountSuggestions?.code === feeAccountCode
                ? feeAccountSuggestions.suggestions
                : []
            }
            disabled={busy}
            onChange={setFeeCode}
            onClear={() => setFeeCode("")}
            clearLabel={tc("filters.clearField")}
          />
        </div>
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={busy || feeAccountCode === ""}>
            {tc("actions.save")}
          </Button>
          {feeAccountCode !== "" &&
            feeLookup?.code === feeAccountCode &&
            "exists" in feeLookup &&
            !feeLookup.exists && (
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => {
                  void saveFeeAccount(true);
                }}
              >
                {t("fee.createAndUse", { code: feeAccountCode })}
              </Button>
            )}
          {connection.feeAccount !== "" && (
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={() => {
                onClearFeeAccount();
              }}
            >
              {tc("actions.clear")}
            </Button>
          )}
        </div>
      </form>
    </section>
  );
}

function ConnectionInstrumentsSection({
  connection,
  provider,
  id,
  instrumentSection,
  busy,
  run,
  callVenue,
  error,
  setError,
  deleteSymbol,
  setDeleteSymbol,
}: {
  connection: TradingConnection;
  provider: TradingProvider;
  id: string;
  instrumentSection: Ref<HTMLElement>;
  busy: boolean;
  run: (
    fn: () => Promise<unknown>,
    handled?: (err: unknown) => boolean,
  ) => Promise<boolean>;
  callVenue: (
    fn: () => Promise<void>,
    setPending: (pending: boolean) => void,
    setFailure: (message: string) => void,
  ) => Promise<void>;
  error: string;
  setError: (message: string) => void;
  deleteSymbol: string | null;
  setDeleteSymbol: (symbol: string | null) => void;
}) {
  const { t } = useTranslation("trading");
  const api = useOfficerApi();
  const [verifying, setVerifying] = useState(false);
  const [searchError, setSearchError] = useState("");
  const [verifyError, setVerifyError] = useState<{
    symbol: string;
    message: string;
  } | null>(null);
  // The add row starts as a market-data feed's does.
  const [draft, setDraft] = useState(emptyInstrumentDraft);
  const [searchSupported, setSearchSupported] = useState<boolean | null>(null);
  const symbolSearch = useSymbolSearch(async (query) => {
    setSearchSupported(null);
    setSearchError("");
    try {
      const result = await api.searchTradingSymbols(connection.id, { query });
      setSearchSupported(result.supported);
      return result.supported ? result.matches : [];
    } catch (err) {
      setSearchError(err instanceof Error ? err.message : String(err));
      return null;
    }
  });
  const [verification, setVerification] = useState<{
    symbol: string;
    result: TradingSymbolVerification;
  } | null>(null);
  const baseSuggestions = useAssetCodeSuggestions(draft.baseAsset, true);
  const quoteSuggestions = useAssetCodeSuggestions(draft.quoteAsset, true);
  const {
    outcomes: rowVerify,
    isVerifying,
    verifyRow,
    forget,
  } = useRowVerification((symbol) =>
    api.verifyTradingSymbol(connection.id, symbol),
  );
  const verifySymbol = () => {
    const symbol = draft.externalSymbol.trim();
    if (verifying || symbol === "") return;
    setVerification(null);
    void callVenue(
      async () => {
        setVerification({
          symbol,
          result: await api.verifyTradingSymbol(connection.id, symbol),
        });
      },
      setVerifying,
      (message) => setVerifyError(message === "" ? null : { symbol, message }),
    );
  };
  const verdict =
    verification?.symbol === draft.externalSymbol.trim()
      ? verification.result
      : verifyError?.symbol === draft.externalSymbol.trim()
        ? { failed: true as const }
        : null;
  const currentVerifyError =
    verifyError?.symbol === draft.externalSymbol.trim()
      ? verifyError.message
      : "";
  // As on a market-data feed, an asset code Officer cannot accept keeps the
  // add button disabled.
  const draftComplete = isInstrumentDraftFieldsValid(draft);
  // As on a market-data feed, the server creates a base or quote asset Officer
  // does not know yet.
  const saveInstrument = async () => {
    if (busy || !draftComplete) return;
    const ok = await run(() =>
      api.upsertTradingInstrument(connection.id, {
        externalSymbol: draft.externalSymbol.trim(),
        baseAsset: draft.baseAsset.trim(),
        quoteAsset: draft.quoteAsset.trim(),
        enabled: draft.enabled,
      }),
    );
    if (ok) {
      setDraft(emptyInstrumentDraft);
      setVerification(null);
      setVerifyError(null);
    }
  };

  return (
    <section
      ref={instrumentSection}
      tabIndex={-1}
      aria-labelledby={`${id}-instruments`}
      className="flex flex-col gap-y-3 border-t border-border pt-4"
    >
      <h4 id={`${id}-instruments`} className="text-sm font-medium">
        {t("instruments.title")}
      </h4>
      {provider.searchesSymbols && (
        <>
          <SymbolSearchPanel
            {...symbolSearch}
            id={`${id}-search`}
            busy={busy}
            searched={symbolSearch.searched && searchSupported === true}
            onQueryChange={(value) => {
              symbolSearch.onQueryChange(value);
              setSearchSupported(null);
            }}
            onSearch={() => {
              void symbolSearch.onSearch();
            }}
            onPick={(match) => {
              const pair = venueSymbolPair(match);
              setDraft((previous) => ({
                ...previous,
                externalSymbol: match.symbol,
                baseAsset: pair.base,
                quoteAsset: pair.quote,
              }));
              setVerification(null);
              setVerifyError(null);
            }}
            label={t("search.label")}
            placeholder={t("search.placeholder")}
            noResultsText={t("search.empty")}
            notice={
              searchSupported === false ? t("search.unavailable") : undefined
            }
            getKey={(match) => match.symbol}
            getLabel={(match) => match.symbol}
            getMetadata={(match) =>
              [match.exchange, match.class].filter(Boolean).join(" · ")
            }
            getWarning={(match) =>
              match.tradable === false ? t("search.notTradable") : undefined
            }
            getName={(match) => match.name}
            getSearchText={(match) =>
              [match.symbol, match.name, match.exchange, match.class]
                .join(" ")
                .toLowerCase()
            }
          />
          {searchError && <ErrorBanner message={searchError} />}
        </>
      )}
      <InstrumentDraftRow
        draft={draft}
        onDraftChange={(patch) => {
          setDraft((previous) => ({ ...previous, ...patch }));
          if ("externalSymbol" in patch) {
            setVerification(null);
            setVerifyError(null);
          }
        }}
        busy={busy}
        valid={draftComplete}
        onSubmit={() => {
          void saveInstrument();
        }}
        verify={
          provider.verifiesSymbols
            ? { verifying, onVerify: verifySymbol }
            : undefined
        }
        enabled={{
          value: draft.enabled,
          onToggle: () =>
            setDraft((previous) => ({
              ...previous,
              enabled: !previous.enabled,
            })),
        }}
        baseSuggestions={baseSuggestions.codes}
        quoteSuggestions={quoteSuggestions.codes}
        baseSuggestionsFailed={baseSuggestions.failed}
        quoteSuggestionsFailed={quoteSuggestions.failed}
      />
      <div role={verdict === null ? undefined : "status"}>
        <VerifyResult
          result={verdict}
          unsupportedLabel={t("verify.unavailable")}
        />
      </div>
      {currentVerifyError && <ErrorBanner message={currentVerifyError} />}
      {connection.instruments.map(({ externalSymbol }) => {
        const outcome = Object.hasOwn(rowVerify, externalSymbol)
          ? rowVerify[externalSymbol]
          : undefined;
        return outcome &&
          "failed" in outcome &&
          outcome.message &&
          !isVerifying(externalSymbol) ? (
          <ErrorBanner
            key={externalSymbol}
            message={`${externalSymbol}: ${outcome.message}`}
          />
        ) : null;
      })}
      <InstrumentsTable
        instruments={connection.instruments}
        ariaLabel={t("instruments.title")}
        busy={busy}
        parentEnabled={connection.enabled}
        parentDisabledLabel={t("instruments.connectionDisabled")}
        onToggle={(instrument) => {
          void run(() =>
            api.upsertTradingInstrument(connection.id, {
              ...instrument,
              enabled: !instrument.enabled,
            }),
          );
        }}
        onVerify={
          provider.verifiesSymbols
            ? (instrument) => {
                void verifyRow(instrument.externalSymbol);
              }
            : undefined
        }
        isVerifying={isVerifying}
        stateAdornment={(instrument) => (
          <VerifyResult
            unsupportedLabel={t("verify.unavailable")}
            result={
              Object.hasOwn(rowVerify, instrument.externalSymbol)
                ? rowVerify[instrument.externalSymbol]
                : null
            }
          />
        )}
        onDelete={(instrument) => {
          setError("");
          setDeleteSymbol(instrument.externalSymbol);
        }}
      />
      <DeleteInstrumentDialog
        open={deleteSymbol !== null}
        description={t("instruments.deleteDescription", {
          symbol: deleteSymbol,
          label: connection.label,
        })}
        busy={busy}
        error={error}
        onOpenChange={(open) => {
          if (!open && !busy) setDeleteSymbol(null);
        }}
        onSubmit={() => {
          if (deleteSymbol === null || busy) return;
          void run(async () => {
            await api.deleteTradingInstrument(connection.id, deleteSymbol);
            forget(deleteSymbol);
            setDeleteSymbol(null);
          });
        }}
      />
    </section>
  );
}

type TestRuleDraft = {
  key: string;
  saved: boolean;
  expanded: boolean;
  mask: TestExecutionRule["mask"];
  mode: TestExecutionMode;
  priceVariant?: TestPriceVariant;
  fills?: number;
  waitKind: "finite" | "infinite" | null;
  seconds: string;
};

function testRuleDraft(rule: TestExecutionRule, key: string): TestRuleDraft {
  return {
    key,
    saved: true,
    expanded: false,
    mask: { ...rule.mask },
    mode: rule.mode,
    priceVariant: rule.priceVariant,
    fills: rule.fills,
    waitKind: rule.wait === undefined ? null : rule.wait.kind,
    seconds:
      rule.wait?.kind === "finite" && rule.wait.seconds !== undefined
        ? String(rule.wait.seconds)
        : "",
  };
}

function testRuleFromDraft(draft: TestRuleDraft): TestExecutionRule {
  const rule: TestExecutionRule = { mask: { ...draft.mask }, mode: draft.mode };
  if (!(draft.mode === "rest" && draft.waitKind === "infinite")) {
    if (draft.priceVariant !== undefined)
      rule.priceVariant = draft.priceVariant;
  }
  if (draft.mode === "split" && draft.fills !== undefined)
    rule.fills = draft.fills;
  if (draft.mode === "rest" && draft.waitKind !== null) {
    rule.wait =
      draft.waitKind === "infinite"
        ? { kind: "infinite" }
        : {
            kind: "finite",
            ...(draft.seconds === "" ? {} : { seconds: Number(draft.seconds) }),
          };
  }
  return rule;
}

function validExecutionDelay(seconds: string): boolean {
  return /^[1-9][0-9]*$/.test(seconds) && Number(seconds) <= 9223372036;
}

function ConnectionTestPolicySection({
  connectionId,
}: {
  connectionId: string;
}) {
  const { t } = useTranslation("trading");
  const { t: tc } = useTranslation();
  const { fetchTestExecutionPolicy, updateTestExecutionPolicy } =
    useOfficerApi();
  const id = useId();
  const nextRuleKey = useRef(0);
  const [state, setState] = useState<TestExecutionPolicyState | null>(null);
  const [rules, setRules] = useState<TestRuleDraft[]>([]);
  const [error, setError] = useState("");
  const [errorDetails, setErrorDetails] = useState<ApiError["errors"]>();
  const [readError, setReadError] = useState("");
  const [needsRead, setNeedsRead] = useState(false);
  const [busy, setBusy] = useState(true);
  const [loadAttempt, setLoadAttempt] = useState(0);
  const errorBanner = useRef<HTMLDivElement>(null);
  const ruleRows = useRef<(HTMLLIElement | null)[]>([]);
  const [invalidRule, setInvalidRule] = useState<number | null>(null);

  useEffect(() => {
    if (error !== "") errorBanner.current?.scrollIntoView({ block: "nearest" });
  }, [error]);

  useEffect(() => {
    const controller = new AbortController();
    void fetchTestExecutionPolicy(connectionId, controller.signal)
      .then((loaded) => {
        if (controller.signal.aborted) return;
        setState(loaded);
        setRules(
          loaded.rules.map((rule) =>
            testRuleDraft(rule, String(nextRuleKey.current++)),
          ),
        );
        setError("");
        setErrorDetails(undefined);
        setReadError("");
        setNeedsRead(false);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setError(err instanceof Error ? err.message : String(err));
          setErrorDetails(undefined);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setBusy(false);
      });
    return () => controller.abort();
  }, [connectionId, fetchTestExecutionPolicy, loadAttempt]);

  const editRule = (index: number, patch: Partial<TestRuleDraft>) => {
    setError("");
    setErrorDetails(undefined);
    setRules((current) =>
      current.map((rule, position) =>
        position === index
          ? {
              ...rule,
              ...patch,
              ...(Object.keys(patch).some((key) => key !== "expanded")
                ? { saved: false, expanded: true }
                : {}),
            }
          : rule,
      ),
    );
  };
  const moveRule = (index: number, offset: number) => {
    setError("");
    setErrorDetails(undefined);
    setRules((current) => {
      const reordered = [...current];
      const target = index + offset;
      [reordered[index], reordered[target]] = [
        reordered[target],
        reordered[index],
      ];
      return reordered;
    });
  };
  const applySavedRules = (savedRules: TestExecutionRule[]) => {
    setRules((current) =>
      savedRules.map((rule, index) => ({
        ...testRuleDraft(
          rule,
          index < current.length
            ? current[index].key
            : String(nextRuleKey.current++),
        ),
        ...(index < current.length
          ? { key: current[index].key, expanded: current[index].expanded }
          : {}),
      })),
    );
  };
  const reconcilePolicy = async () => {
    setBusy(true);
    setNeedsRead(true);
    setReadError("");
    try {
      const saved = await fetchTestExecutionPolicy(connectionId);
      if (saved.revision !== state?.revision) {
        applySavedRules(saved.rules);
      }
      setState(saved);
      setNeedsRead(false);
    } catch (err) {
      setReadError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };
  const save = async () => {
    if (busy || needsRead) return;
    setInvalidRule(null);
    for (const [index, rule] of rules.entries()) {
      const field = !rule.mask.baseAsset.trim()
        ? "baseAsset"
        : !rule.mask.quoteAsset.trim()
          ? "quoteAsset"
          : rule.mode === "split" && rule.fills === undefined
            ? "fills"
            : rule.mode === "rest" && rule.waitKind === null
              ? "wait"
              : rule.mode === "rest" &&
                  rule.waitKind === "finite" &&
                  !validExecutionDelay(rule.seconds)
                ? "seconds"
                : !(rule.mode === "rest" && rule.waitKind === "infinite") &&
                    rule.priceVariant === undefined
                  ? "priceVariant"
                  : null;
      if (field !== null) {
        editRule(index, { expanded: true });
        setError(
          t("testPolicy.completeField", {
            number: index + 1,
            field: t(`testPolicy.${field}`),
          }),
        );
        setErrorDetails(undefined);
        setInvalidRule(index);
        return;
      }
    }
    setBusy(true);
    setError("");
    setErrorDetails(undefined);
    setReadError("");
    try {
      const saved = await updateTestExecutionPolicy(connectionId, {
        rules: rules.map(testRuleFromDraft),
      });
      setState(saved);
      applySavedRules(saved.rules);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setErrorDetails(err instanceof ApiError ? err.errors : undefined);
      if (
        !(err instanceof ApiError) ||
        err.status === undefined ||
        err.status < 400 ||
        err.status === 408 ||
        err.status >= 500
      ) {
        await reconcilePolicy();
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <section
      aria-labelledby={`${id}-title`}
      className="flex flex-col gap-4 border-t border-border pt-4"
    >
      <h4 id={`${id}-title`} className="text-sm font-medium">
        {t("testPolicy.title")}
      </h4>
      <p className="text-xs text-muted">
        {t("testPolicy.matching", { noMatch: "test_policy_no_match" })}{" "}
        {t("testPolicy.marketDataTitle")}.{" "}
        {t("testPolicy.marketDataWarning", {
          code: "test_quote_ambiguous",
          restart: tc("actions.restartFeeds"),
        })}{" "}
        <Link to="/market-data" className="text-accent underline">
          {tc("nav.marketData")}
        </Link>
      </p>
      {state === null && error !== "" && <ErrorBanner message={error} />}
      {state === null ? (
        busy ? (
          <p className="text-xs text-muted">{tc("states.loading")}</p>
        ) : (
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => {
              setBusy(true);
              setError("");
              setErrorDetails(undefined);
              setLoadAttempt((attempt) => attempt + 1);
            }}
          >
            {tc("actions.retry")}
          </Button>
        )
      ) : (
        <>
          <p className="text-xs text-muted">
            {t("testPolicy.revision", { revision: state.revision })}
          </p>
          {rules.length === 0 && (
            <Notice
              tone="warn"
              icon={AlertTriangle}
              title={t("testPolicy.emptyTitle")}
            >
              {t("testPolicy.emptyDescription")}
            </Notice>
          )}
          <ol
            className="flex flex-col gap-4"
            aria-label={t("testPolicy.rules")}
          >
            {rules.map((rule, index) => (
              <li
                key={rule.key}
                ref={(element) => {
                  ruleRows.current[index] = element;
                }}
                tabIndex={-1}
              >
                <fieldset
                  disabled={busy || needsRead}
                  aria-label={t("testPolicy.rule", { number: index + 1 })}
                  className="flex flex-col gap-4 rounded-card border border-border p-4"
                >
                  <legend className="px-1 text-sm font-medium">
                    {rule.mask.baseAsset}/{rule.mask.quoteAsset} -{" "}
                    {t(`testPolicy.modes.${rule.mode}`)}
                  </legend>
                  <div className="flex gap-2">
                    {rule.saved && (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        aria-expanded={rule.expanded}
                        aria-controls={`${id}-${rule.key}-body`}
                        onClick={() =>
                          editRule(index, { expanded: !rule.expanded })
                        }
                      >
                        {rule.expanded ? <ChevronUp /> : <ChevronDown />}
                        {tc(
                          rule.expanded ? "actions.collapse" : "actions.expand",
                        )}
                      </Button>
                    )}
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={busy || needsRead || index === 0}
                      aria-label={t("testPolicy.moveUp", { number: index + 1 })}
                      onClick={() => moveRule(index, -1)}
                    >
                      <ArrowUp />
                      {t("testPolicy.higherPriority")}
                    </Button>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={busy || needsRead || index === rules.length - 1}
                      aria-label={t("testPolicy.moveDown", {
                        number: index + 1,
                      })}
                      onClick={() => moveRule(index, 1)}
                    >
                      <ArrowDown />
                      {t("testPolicy.lowerPriority")}
                    </Button>
                  </div>
                  <div
                    id={`${id}-${rule.key}-body`}
                    hidden={rule.saved && !rule.expanded}
                    className={
                      rule.saved && !rule.expanded
                        ? "hidden"
                        : "flex flex-col gap-4"
                    }
                  >
                    {errorDetails?.map((detail) => {
                      const prefix = `/rules/${index}/`;
                      if (!detail.pointer.startsWith(prefix)) return null;
                      const fields: Record<string, string> = {
                        "mask/baseAsset": "baseAsset",
                        "mask/quoteAsset": "quoteAsset",
                        mode: "mode",
                        priceVariant: "priceVariant",
                        fills: "fills",
                        wait: "wait",
                        "wait/kind": "wait",
                        "wait/seconds": "seconds",
                      };
                      const member = detail.pointer.slice(prefix.length);
                      if (!Object.hasOwn(fields, member)) return null;
                      const field = fields[member];
                      return (
                        <p
                          key={detail.pointer}
                          className="text-xs text-[var(--danger)]"
                        >
                          {t(`testPolicy.${field}`)}: {error}
                        </p>
                      );
                    })}
                    <div className="grid gap-3 sm:grid-cols-2">
                      <AssetCodeField
                        id={`${id}-${index}-base`}
                        label={t("testPolicy.baseAsset")}
                        disabled={busy}
                        value={rule.mask.baseAsset}
                        onChange={(baseAsset) =>
                          editRule(index, { mask: { ...rule.mask, baseAsset } })
                        }
                      />
                      <AssetCodeField
                        id={`${id}-${index}-quote`}
                        label={t("testPolicy.quoteAsset")}
                        disabled={busy}
                        value={rule.mask.quoteAsset}
                        onChange={(quoteAsset) =>
                          editRule(index, {
                            mask: { ...rule.mask, quoteAsset },
                          })
                        }
                      />
                    </div>
                    <p className="text-xs text-muted">
                      {t("testPolicy.maskHint")}
                    </p>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={busy}
                      onClick={() =>
                        editRule(index, {
                          mask: { baseAsset: "*", quoteAsset: "*" },
                        })
                      }
                    >
                      {t("testPolicy.allInstruments")}
                    </Button>
                    <div className="flex flex-col gap-2">
                      <p className="text-xs font-medium">
                        {t("testPolicy.mode")}
                      </p>
                      <ChoiceCards<TestExecutionMode>
                        label={t("testPolicy.mode")}
                        value={rule.mode}
                        disabled={busy}
                        onChange={(mode) => editRule(index, { mode })}
                        options={[
                          {
                            value: "execute",
                            title: t("testPolicy.modes.execute"),
                            description: t("testPolicy.modeDetails.execute"),
                            icon: Check,
                          },
                          {
                            value: "split",
                            title: t("testPolicy.modes.split"),
                            description: t("testPolicy.modeDetails.split"),
                            icon: Split,
                          },
                          {
                            value: "reject_even",
                            title: t("testPolicy.modes.reject_even"),
                            description: t(
                              "testPolicy.modeDetails.reject_even",
                            ),
                            icon: X,
                          },
                          {
                            value: "rest",
                            title: t("testPolicy.modes.rest"),
                            description: t("testPolicy.modeDetails.rest"),
                            icon: Clock,
                          },
                        ]}
                      />
                    </div>
                    {rule.mode === "split" && (
                      <div className="flex flex-col gap-2">
                        <p className="text-xs font-medium">
                          {t("testPolicy.fills")}
                        </p>
                        <Segmented
                          ariaLabel={t("testPolicy.fills")}
                          value={
                            rule.fills === undefined
                              ? undefined
                              : String(rule.fills)
                          }
                          disabled={busy}
                          options={[
                            { value: "2", label: "2" },
                            { value: "3", label: "3" },
                          ]}
                          onChange={(value) =>
                            editRule(index, { fills: Number(value) })
                          }
                        />
                      </div>
                    )}
                    {rule.mode === "rest" && (
                      <>
                        <div className="flex flex-col gap-2">
                          <p className="text-xs font-medium">
                            {t("testPolicy.wait")}
                          </p>
                          <ChoiceCards<"finite" | "infinite">
                            label={t("testPolicy.wait")}
                            value={rule.waitKind}
                            disabled={busy}
                            onChange={(waitKind) =>
                              editRule(index, { waitKind })
                            }
                            options={[
                              {
                                value: "finite",
                                title: t("testPolicy.afterSeconds"),
                                description: t("testPolicy.finiteDescription"),
                                icon: Clock,
                              },
                              {
                                value: "infinite",
                                title: t("testPolicy.withoutExecution"),
                                description: t(
                                  "testPolicy.infiniteDescription",
                                ),
                                icon: X,
                              },
                            ]}
                          />
                        </div>
                        {rule.waitKind === "finite" && (
                          <div className="flex flex-col gap-y-2">
                            <Label htmlFor={`${id}-${index}-seconds`}>
                              {t("testPolicy.seconds")}
                            </Label>
                            <NumberStepper
                              id={`${id}-${index}-seconds`}
                              integerOnly
                              min="1"
                              disabled={busy}
                              value={rule.seconds}
                              onChange={(seconds) =>
                                editRule(index, { seconds })
                              }
                              onClear={() => editRule(index, { seconds: "" })}
                              clearLabel={t("common:filters.clearField")}
                              customValidity={
                                rule.seconds === "" ||
                                validExecutionDelay(rule.seconds)
                                  ? ""
                                  : t("testPolicy.completeField", {
                                      number: index + 1,
                                      field: t("testPolicy.seconds"),
                                    })
                              }
                            />
                          </div>
                        )}
                      </>
                    )}
                    {!(
                      rule.mode === "rest" && rule.waitKind === "infinite"
                    ) && (
                      <div className="flex flex-col gap-2">
                        <p className="text-xs font-medium">
                          {t("testPolicy.priceVariant")}
                        </p>
                        <Segmented
                          ariaLabel={t("testPolicy.priceVariant")}
                          value={rule.priceVariant}
                          disabled={busy}
                          options={(["normal", "best", "worst"] as const).map(
                            (value) => ({
                              value,
                              label: t(`testPolicy.prices.${value}`),
                            }),
                          )}
                          onChange={(value) =>
                            editRule(index, {
                              priceVariant: value as TestPriceVariant,
                            })
                          }
                        />
                        <p className="text-xs text-muted">
                          {t("testPolicy.priceExplanation")}
                        </p>
                      </div>
                    )}
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={busy}
                      aria-label={t("testPolicy.removeRule", {
                        number: index + 1,
                      })}
                      onClick={() => {
                        setError("");
                        setErrorDetails(undefined);
                        setRules((current) =>
                          current.filter((_, position) => position !== index),
                        );
                      }}
                    >
                      {tc("actions.delete")}
                    </Button>
                  </div>
                </fieldset>
              </li>
            ))}
          </ol>
          {error !== "" && (
            <div ref={errorBanner}>
              <ErrorBanner message={error} />
              {invalidRule !== null && (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    const row = ruleRows.current[invalidRule];
                    row?.scrollIntoView({ block: "center" });
                    row?.focus({ preventScroll: true });
                  }}
                >
                  {t("testPolicy.goToRule", { number: invalidRule + 1 })}
                </Button>
              )}
              {errorDetails?.map((detail) => (
                <p
                  key={detail.pointer}
                  className="mt-1 text-xs text-[var(--danger)]"
                >
                  {detail.pointer}
                  {detail.constraint === undefined
                    ? ""
                    : ` (${detail.constraint})`}
                </p>
              ))}
              {readError !== "" && <ErrorBanner message={readError} />}
            </div>
          )}
          <div className="flex flex-wrap gap-2">
            {needsRead && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={() => void reconcilePolicy()}
              >
                {tc("actions.retry")}
              </Button>
            )}
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={busy || needsRead}
              onClick={() => {
                setError("");
                setErrorDetails(undefined);
                setRules((current) => [
                  ...current,
                  {
                    key: String(nextRuleKey.current++),
                    saved: false,
                    expanded: true,
                    mask: { baseAsset: "", quoteAsset: "" },
                    mode: "execute",
                    waitKind: null,
                    seconds: "",
                  },
                ]);
              }}
            >
              {t("testPolicy.addRule")}
            </Button>
            <Button
              type="button"
              size="sm"
              disabled={busy || needsRead}
              onClick={() => void save()}
            >
              {tc("actions.save")}
            </Button>
          </div>
        </>
      )}
    </section>
  );
}

function ConnectionCard({
  connection,
  provider,
  credentialStorage,
  focusOnMount,
  expanded,
  onToggle,
  reload,
}: {
  connection: TradingConnection;
  provider: TradingProvider;
  credentialStorage: LoadState<CredentialStorage>;
  focusOnMount: boolean;
  expanded: boolean;
  onToggle: () => void;
  reload: () => void;
}) {
  const { t } = useTranslation("trading");
  const { t: tc } = useTranslation();
  const { t: tv } = useTranslation("venues");
  const api = useOfficerApi();
  const id = useId();
  const [busy, setBusy] = useState(false);
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState("");
  const [feeRequired, setFeeRequired] = useState(false);
  const [confirmingNonRelease, setConfirmingNonRelease] = useState(false);
  const [checkError, setCheckError] = useState("");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const [check, setCheck] = useState<TradingConnectionCheck | null>(null);
  const [subjectKind, setSubjectKind] = useState<AccessSubjectKind>("account");
  const [subjectInput, setSubjectInput] = useState("");
  const [venueAccount, setVenueAccount] = useState("");
  const [feeCode, setFeeCode] = useState("");
  const [deleteSymbol, setDeleteSymbol] = useState<string | null>(null);
  const [revokeSubject, setRevokeSubject] =
    useState<TradingAccessSubject | null>(null);
  const [feeAccountSuggestions, setFeeAccountSuggestions] =
    useState<AccountSuggestionLookup | null>(null);
  const [feeLookup, setFeeLookup] = useState<
    { code: string; exists: boolean } | { code: string; error: string } | null
  >(null);
  const checkSection = useRef<HTMLElement>(null);
  const instrumentSection = useRef<HTMLElement>(null);
  const accessSection = useRef<HTMLElement>(null);
  const enableButton = useRef<HTMLButtonElement>(null);
  const feeInput = useRef<HTMLInputElement>(null);
  const venueAccountInput = useRef<HTMLDivElement>(null);
  const card = useRef<HTMLDivElement>(null);
  const feeQuery = useDebouncedValue(
    feeCode.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const { fetchAccounts, fetchAccountState } = api;

  useEffect(() => {
    if (!focusOnMount) return;
    card.current?.scrollIntoView({ block: "nearest" });
    card.current?.focus();
  }, [focusOnMount]);

  useEffect(() => {
    if (feeQuery === "") return;
    const controller = new AbortController();
    void fetchAccounts(
      {
        code: feeQuery,
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      controller.signal,
    )
      .then((accounts) => {
        if (controller.signal.aborted) return;
        setFeeAccountSuggestions({
          code: feeQuery,
          suggestions: Array.from(
            new Set(accounts.map((item) => item.code)),
          ).sort(),
          error: "",
        });
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setFeeAccountSuggestions({
            code: feeQuery,
            suggestions: [],
            error: err instanceof Error ? err.message : String(err),
          });
        }
      });
    return () => controller.abort();
  }, [feeQuery, fetchAccounts]);

  useEffect(() => {
    if (feeQuery === "") return;
    const controller = new AbortController();
    void fetchAccountState(feeQuery, controller.signal)
      .then(() => {
        if (!controller.signal.aborted) {
          setFeeLookup({ code: feeQuery, exists: true });
        }
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted) return;
        if (err instanceof ApiError && err.code === "not_found") {
          setFeeLookup({ code: feeQuery, exists: false });
        } else {
          setFeeLookup({
            code: feeQuery,
            error: err instanceof Error ? err.message : String(err),
          });
        }
      });
    return () => controller.abort();
  }, [feeQuery, fetchAccountState]);

  // handled answers a failure the caller presents itself instead of the banner.
  const run = async (
    fn: () => Promise<unknown>,
    handled?: (err: unknown) => boolean,
  ): Promise<boolean> => {
    setBusy(true);
    setError("");
    setFeeRequired(false);
    try {
      await fn();
      reload();
      return true;
    } catch (err) {
      if (!handled?.(err)) {
        setError(err instanceof Error ? err.message : String(err));
      }
      setFeeRequired(
        err instanceof ApiError && err.code === "fee_account_required",
      );
      return false;
    } finally {
      setBusy(false);
    }
  };
  const callVenue = async (
    fn: () => Promise<void>,
    setPending: (pending: boolean) => void,
    setFailure: (message: string) => void,
  ) => {
    setPending(true);
    setFailure("");
    try {
      await fn();
    } catch (err) {
      setFailure(err instanceof Error ? err.message : String(err));
    } finally {
      setPending(false);
    }
  };

  const focusFeeAccount = () => {
    feeInput.current?.scrollIntoView({ block: "nearest" });
    feeInput.current?.focus();
  };
  const focusVenueAccount = () => {
    venueAccountInput.current?.scrollIntoView({ block: "nearest" });
    venueAccountInput.current?.querySelector("input")?.focus();
  };
  const { showsAccounts } = accountScope();
  // Hidden accounts leave no fee-account section to send the operator to.
  const showsFeeAccount = showsAccounts && provider.unlinkedFees;
  const subjectCode = subjectInput.trim();
  const feeAccountCode = feeCode.trim();
  // A group is refused whatever its code, so its reason shows before typing.
  // Hidden accounts grant nothing from the panel: the server writes the
  // default account's grant, or the venue account field saves it.
  const feeBlock = !showsAccounts
    ? null
    : subjectKind === "group"
      ? accessGrantBlock(provider, connection, {
          accountGroup: subjectCode,
          venueAccount,
        })
      : subjectCode === ""
        ? null
        : accessGrantBlock(provider, connection, {
            account: subjectCode,
            venueAccount,
          });
  const steps = [
    {
      key: "check",
      label: t("actions.check"),
      done: check !== null,
      target: checkSection,
    },
    ...(connection.executionPolicy === true
      ? []
      : [
          {
            key: "instruments",
            label: t("instruments.title"),
            done: connection.instruments.length > 0,
            target: instrumentSection,
          },
        ]),
    // Hidden accounts take no access step: the server grants the default
    // account itself, and only a venue account still has to be named. The
    // list then holds only the default account's own grants and its group's.
    ...(showsAccounts || provider.venueAccounts
      ? [
          {
            key: "access",
            label: showsAccounts ? t("access.title") : tv("venueAccount"),
            // Hidden accounts: done only once the default account's grant
            // names the venue account an order needs.
            done: showsAccounts
              ? connection.access.length > 0
              : (defaultAccountGrant(connection)?.venueAccount ?? "") !== "",
            target: accessSection,
          },
        ]
      : []),
    {
      key: "enable",
      label: tv("actions.enable"),
      done: connection.enabled,
      target: enableButton,
    },
  ];

  const checkConnection = () => {
    if (checking) return;
    setCheck(null);
    void callVenue(
      async () => {
        setCheck(await api.checkTradingConnection(connection.id));
        reload();
      },
      setChecking,
      setCheckError,
    );
  };
  const grant = async (subject: TradingAccessSubject) => {
    if (busy) return;
    const venueAccountCode = venueAccount.trim();
    const body =
      provider.venueAccounts && venueAccountCode !== ""
        ? { ...subject, venueAccount: venueAccountCode }
        : subject;
    const ok = await run(() => api.grantTradingAccess(connection.id, body));
    if (ok) {
      setSubjectInput("");
      setVenueAccount("");
    }
  };
  const grantAccess = async () => {
    if (subjectCode === "" || feeBlock !== null) return;
    await grant(
      subjectKind === "account"
        ? { account: subjectCode }
        : { accountGroup: subjectCode },
    );
  };
  const openRevoke = (subject: TradingAccessSubject) => {
    setError("");
    setRevokeSubject(subject);
  };
  const saveFeeAccount = async (create: boolean) => {
    if (busy || feeAccountCode === "") return;
    const ok = await run(async () => {
      if (create) {
        await api.createAccount(feeAccountCode);
        setFeeLookup({ code: feeAccountCode, exists: true });
        reload();
      }
      await api.setTradingFeeAccount(connection.id, feeAccountCode);
    });
    if (ok) setFeeCode("");
  };

  return (
    <Card
      ref={card}
      role="region"
      aria-labelledby={`${id}-title`}
      tabIndex={-1}
    >
      <VenueCardHeader
        providerType={connection.provider}
        title={connection.label}
        titleId={`${id}-title`}
        disclosure={{ expanded, controls: `${id}-body`, onToggle }}
        badges={
          <>
            <Badge variant="neutral">{provider.title}</Badge>
            <Badge>{t(`mode.${connection.mode}`)}</Badge>
            {/* The feed's enabled/disabled badge, worded to agree with
                "connection" where a locale inflects it. */}
            <Badge variant={connection.enabled ? "ok" : "neutral"}>
              {t(
                connection.enabled
                  ? "connection.enabled"
                  : "connection.disabled",
              )}
            </Badge>
            {/* As a feed's: the runtime state reads only while enabled. A
                disabled connection's worker may still track its open orders. */}
            {connection.enabled && (
              <Badge variant={CONNECTION_STATE_BADGE[connection.state]}>
                {t(`state.${connection.state}`)}
              </Badge>
            )}
          </>
        }
        error={connection.error}
        errorTone={connection.state === "degraded" ? "warn" : "danger"}
        links={tradingConnectionLinks(connection.provider, connection.mode)}
        actions={
          <>
            <Button
              ref={enableButton}
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => {
                void run(
                  () =>
                    api.setTradingConnectionEnabled(
                      connection.id,
                      !connection.enabled,
                    ),
                  (err) => {
                    if (
                      !connection.enabled &&
                      err instanceof ApiError &&
                      err.code === "non_release_confirmation_required"
                    ) {
                      setConfirmingNonRelease(true);
                      return true;
                    }
                    return false;
                  },
                );
              }}
            >
              {tv(connection.enabled ? "actions.disable" : "actions.enable")}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              disabled={busy}
              aria-label={tc("actions.editSettings")}
              title={tc("actions.editSettings")}
              onClick={() => setSettingsOpen(true)}
            >
              <Settings />
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              disabled={busy}
              aria-label={t("actions.deleteConnection")}
              onClick={() => {
                setDeleteError("");
                setDeleting(true);
              }}
            >
              <Trash2 />
            </Button>
          </>
        }
      />
      <CardContent
        id={`${id}-body`}
        hidden={!expanded}
        className={expanded ? "flex flex-col gap-y-6" : "hidden"}
      >
        <div className="flex flex-col gap-y-1 text-xs">
          <dl className="flex flex-wrap gap-2">
            <dt className="text-muted">{tv("venueAccount")}</dt>
            <dd className="break-all">{connection.venueIdentity}</dd>
          </dl>
          {connection.provider === "okx" && (
            <p>
              {t("connection.okx.site", {
                site: settingString(connection.settings, "site"),
              })}
            </p>
          )}
          {connection.provider === "ib" ? (
            <>
              <p>
                {t("connection.ibEndpoint", {
                  host: settingString(connection.settings, "host"),
                  port: settingString(connection.settings, "port"),
                })}
              </p>
              <p>
                {t("connection.ibClientId", {
                  clientId:
                    settingString(connection.settings, "clientId") ||
                    t("marketData:settings.autoClientId"),
                })}
              </p>
            </>
          ) : connection.provider === "openalgo" ? (
            <>
              <p>
                {t("connection.openalgo.url", {
                  url: settingString(connection.settings, "url"),
                })}
              </p>
              <p>
                {t("connection.openalgo.wsUrl", {
                  wsUrl: settingString(connection.settings, "wsUrl"),
                })}
              </p>
            </>
          ) : null}
          {TRADING_PROVIDER_CREDENTIAL_FIELDS.get(connection.provider)?.map(
            (field) => (
              <p key={field.key}>
                {t(
                  connection.secrets[field.key]
                    ? field.storedKey
                    : field.notSetKey,
                )}
              </p>
            ),
          )}
        </div>
        {!connection.enabled && (
          <div className="flex flex-col gap-y-2">
            <h4 className="text-sm font-medium">{t("setup.title")}</h4>
            <ol
              aria-label={t("setup.title")}
              className="list-inside list-decimal flex flex-col gap-y-1 text-xs"
            >
              {steps.map((step) => (
                <li key={step.key}>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    aria-label={t("setup.goToStep", { step: step.label })}
                    onClick={() => {
                      step.target.current?.scrollIntoView({ block: "nearest" });
                      step.target.current?.focus();
                    }}
                  >
                    {step.label}
                  </Button>
                  <span
                    className={step.done ? "text-[var(--ok)]" : "text-muted"}
                  >
                    {t(step.done ? "setup.done" : "setup.notDone")}
                  </span>
                </li>
              ))}
            </ol>
          </div>
        )}
        {error && deleteSymbol === null && revokeSubject === null && (
          <div className="flex flex-col gap-y-2">
            <ErrorBanner message={error} />
            {feeRequired && showsFeeAccount && (
              <LinkButton onClick={focusFeeAccount}>
                {tv("actions.setFeeAccount")}
              </LinkButton>
            )}
          </div>
        )}
        {feeAccountSuggestions?.code === feeAccountCode &&
          feeAccountSuggestions.error !== "" && (
            <ErrorBanner message={feeAccountSuggestions.error} />
          )}
        {feeLookup?.code === feeAccountCode && "error" in feeLookup && (
          <ErrorBanner message={feeLookup.error} />
        )}
        {connection.executionPolicy === true && (
          <ConnectionTestPolicySection
            key={connection.id}
            connectionId={connection.id}
          />
        )}
        <ConnectionCheckSection
          executionPolicy={connection.executionPolicy}
          checkSection={checkSection}
          busy={busy}
          checking={checking}
          checkConnection={checkConnection}
          check={check}
          checkError={checkError}
        />
        {connection.executionPolicy !== true && (
          <ConnectionInstrumentsSection
            connection={connection}
            provider={provider}
            id={id}
            instrumentSection={instrumentSection}
            busy={busy}
            run={run}
            callVenue={callVenue}
            error={error}
            setError={setError}
            deleteSymbol={deleteSymbol}
            setDeleteSymbol={setDeleteSymbol}
          />
        )}
        {showsAccounts ? (
          <ConnectionAccessSection
            accessSection={accessSection}
            id={id}
            connection={connection}
            provider={provider}
            busy={busy}
            subjectKind={subjectKind}
            setSubjectKind={setSubjectKind}
            subjectInput={subjectInput}
            setSubjectInput={setSubjectInput}
            subjectCode={subjectCode}
            venueAccount={venueAccount}
            setVenueAccount={setVenueAccount}
            venueAccountInput={venueAccountInput}
            feeBlock={feeBlock}
            focusFeeAccount={focusFeeAccount}
            focusVenueAccount={focusVenueAccount}
            grantAccess={grantAccess}
            onRevoke={openRevoke}
          />
        ) : provider.venueAccounts ? (
          <DefaultVenueAccountSection
            accessSection={accessSection}
            id={id}
            busy={busy}
            grant={defaultAccountGrant(connection)}
            venueAccount={venueAccount}
            setVenueAccount={setVenueAccount}
            venueAccountInput={venueAccountInput}
            saveVenueAccount={() => grant({ account: accountScope().account })}
          />
        ) : (
          // The server grants the default account on create and enable; an
          // enabled connection can still lack it, e.g. after a revoke through
          // the API or a switch from multi-account mode.
          connection.enabled &&
          defaultAccountGrant(connection) === undefined && (
            <p
              role="alert"
              className="border-t border-border pt-4 text-xs text-[var(--warn)]"
            >
              {t("access.missing")}
            </p>
          )
        )}
        {showsFeeAccount && (
          <ConnectionFeeAccountSection
            id={id}
            connection={connection}
            provider={provider}
            busy={busy}
            feeCode={feeCode}
            setFeeCode={setFeeCode}
            feeAccountCode={feeAccountCode}
            feeAccountSuggestions={feeAccountSuggestions}
            feeLookup={feeLookup}
            feeInput={feeInput}
            saveFeeAccount={saveFeeAccount}
            onClearFeeAccount={() => {
              void run(() => api.clearTradingFeeAccount(connection.id));
            }}
          />
        )}
        <DiagnosticPanel items={connection.diagnostics} />
      </CardContent>
      {settingsOpen && (
        <ConnectionSettingsDialog
          connection={connection}
          credentialStorage={credentialStorage}
          onClose={() => setSettingsOpen(false)}
          reload={reload}
        />
      )}
      <DeleteConnectionDialog
        open={deleting}
        title={t("deleteConnection.title")}
        description={t("deleteConnection.description", {
          label: connection.label,
        })}
        busy={busy}
        error={deleteError}
        onOpenChange={setDeleting}
        onSubmit={() => {
          setBusy(true);
          setDeleteError("");
          void api
            .deleteTradingConnection(connection.id)
            .then(() => {
              setDeleting(false);
              reload();
            })
            .catch((err: unknown) =>
              setDeleteError(err instanceof Error ? err.message : String(err)),
            )
            .finally(() => setBusy(false));
        }}
      />
      <AlertDialog
        open={confirmingNonRelease}
        onOpenChange={setConfirmingNonRelease}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("nonReleaseConfirm.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("nonReleaseConfirm.description")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tc("actions.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="danger"
              onClick={() => {
                setConfirmingNonRelease(false);
                void run(() =>
                  api.setTradingConnectionEnabled(connection.id, true, true),
                );
              }}
            >
              {t("nonReleaseConfirm.action")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <RevokeAccessDialog
        open={revokeSubject !== null}
        subject={revokeSubject}
        connectionLabel={connection.label}
        busy={busy}
        error={error}
        onOpenChange={(open) => {
          if (!open) setRevokeSubject(null);
        }}
        onConfirm={() => {
          if (revokeSubject === null) return;
          void run(async () => {
            await api.revokeTradingAccess(connection.id, revokeSubject);
            setRevokeSubject(null);
          });
        }}
      />
    </Card>
  );
}

export function TradingConnections() {
  const { t } = useTranslation("trading");
  const { t: tc } = useTranslation();
  const { t: tv } = useTranslation("venues");
  const { load, reload } = useTrading();
  const { load: credentialStorage } = useCredentialStorage();
  const [addOpen, setAddOpen] = useState(false);
  const [selectedProvider, setSelectedProvider] =
    useState<TradingProvider | null>(null);
  const [focusConnectionId, setFocusConnectionId] = useState<string | null>(
    null,
  );
  const [expandedConnectionId, setExpandedConnectionId] = useState<
    string | null
  >(null);
  const snapshot = load.state === "ready" ? load.data : null;
  return (
    <Page
      title={tc("nav.tradingConnections")}
      actions={
        <Button type="button" variant="ghost" size="sm" onClick={reload}>
          <RefreshCw />
          {tc("actions.refresh")}
        </Button>
      }
    >
      {load.state === "loading" && <TableSkeleton rows={3} cols={5} />}
      <StaleState load={load} reload={reload} />
      {load.state === "error" && (
        <ErrorState message={load.error} onRetry={reload} />
      )}
      {load.state === "ready" && (
        <div className="flex flex-col gap-y-4">
          <AddProviderButton
            label={t("actions.addConnection")}
            providerTypes={load.data.providers.map((provider) => provider.type)}
            onClick={() => setAddOpen(true)}
          />
          {load.data.connections.length === 0 && (
            <EmptyState
              title={tv("emptyConnections")}
              hint={t("empty.description")}
            />
          )}
          {load.data.connections.map((connection) => {
            const provider = load.data.providers.find(
              (item) => item.type === connection.provider,
            );
            if (!provider)
              return (
                <div key={connection.id} className="flex flex-col gap-2">
                  <p className="text-xs font-medium">
                    {t("missingProvider", { provider: connection.provider })}
                  </p>
                  <ErrorBanner
                    message={
                      connection.error ||
                      t("missingProvider", { provider: connection.provider })
                    }
                  />
                </div>
              );
            return (
              <ConnectionCard
                key={connection.id}
                connection={connection}
                provider={provider}
                credentialStorage={credentialStorage}
                focusOnMount={connection.id === focusConnectionId}
                expanded={connection.id === expandedConnectionId}
                onToggle={() =>
                  setExpandedConnectionId((current) =>
                    current === connection.id ? null : connection.id,
                  )
                }
                reload={reload}
              />
            );
          })}
        </div>
      )}
      {snapshot && (
        <ProviderGuideDialog
          open={addOpen}
          title={t("actions.addConnection")}
          description={t("create.chooseVenue")}
          providers={snapshot.providers.map((provider) => ({
            type: provider.type,
            title: provider.title,
            siteUrl: TRADING_PROVIDER_GUIDES[provider.type]?.siteUrl,
            docsUrl:
              TRADING_PROVIDER_GUIDES[provider.type]?.docsUrl?.[
                draftMode(provider)
              ],
          }))}
          onOpenChange={setAddOpen}
          onSelect={(entry) => {
            const provider = snapshot.providers.find(
              (provider) => provider.type === entry.type,
            );
            if (!provider)
              throw new Error(`Unknown trading provider: ${entry.type}`);
            setAddOpen(false);
            setSelectedProvider(provider);
          }}
        />
      )}
      {selectedProvider && (
        <AddTradingConnectionDialog
          key={selectedProvider.type}
          provider={selectedProvider}
          credentialStorage={credentialStorage}
          reload={reload}
          onClose={() => setSelectedProvider(null)}
          onCreated={(connectionId) => {
            setFocusConnectionId(connectionId);
            setExpandedConnectionId(connectionId);
            reload();
          }}
        />
      )}
    </Page>
  );
}
