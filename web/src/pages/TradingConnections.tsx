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
import { RefreshCw, Settings } from "lucide-react";

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
} from "@/api/types";
import { useCredentialStorage } from "@/api/useCredentialStorage";
import type { LoadState } from "@/api/usePolling";
import { useTrading } from "@/api/useTrading";
import { Autocomplete } from "@/components/Autocomplete";
import { ClearableInput } from "@/components/ClearableInput";
import { CodeSuggestionFailure } from "@/components/CodeSuggestionFailure";
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
const IB_DOCS_URL =
  "https://www.interactivebrokers.com/campus/ibkr-api-page/twsapi-doc/";
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
const ETORO_KEEP_DIALOG_OPEN: TradingGuideStep = {
  textKey: "create.etoro.keepDialogOpen",
  links: [],
};

/**
 * Venue links and account preparation steps, per trading mode: a live
 * connection needs a real account, which the venues set up differently from
 * a paper one. The provider picker opens a paper draft, so it links the paper
 * docs.
 */
const TRADING_PROVIDER_GUIDES: Record<
  string,
  {
    siteUrl?: string;
    docsUrl?: Record<TradingMode, string>;
    steps?: Record<TradingMode, TradingGuideStep[]>;
  }
> = {
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
              labelKey: "create.binance.live.apiKeyGuide",
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
  etoro: {
    siteUrl: "https://www.etoro.com",
    docsUrl: { paper: ETORO_DOCS_URL, live: ETORO_DOCS_URL },
    steps: {
      paper: [
        ETORO_LOG_IN,
        { textKey: "create.etoro.createUserKey", links: [] },
        ETORO_COPY_USER_KEY,
        ETORO_PUBLIC_API_KEY,
        ETORO_KEEP_DIALOG_OPEN,
      ],
      live: [
        ETORO_LOG_IN,
        { textKey: "create.etoro.live.createUserKey", links: [] },
        ETORO_COPY_USER_KEY,
        ETORO_PUBLIC_API_KEY,
        ETORO_KEEP_DIALOG_OPEN,
      ],
    },
  },
};

type TradingCredentialField = {
  key: string;
  labelKey: string;
  storedKey: string;
  notSetKey: string;
};

// Alpaca and Binance both take an {apiKey, apiSecret} pair.
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

// IB takes connection settings instead; any other provider without an entry
// must be blocked: guessing fields could send the wrong credentials.
const TRADING_PROVIDER_CREDENTIAL_FIELDS = new Map<
  string,
  TradingCredentialField[]
>([
  ["alpaca", API_KEY_SECRET_FIELDS],
  ["binance", API_KEY_SECRET_FIELDS],
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

function ibSettingsReady(draft: Record<string, string>): boolean {
  const port = Number(draft.port);
  return (
    draft.host.trim() !== "" &&
    Number.isInteger(port) &&
    port >= 1 &&
    port <= 65535 &&
    isOptionalJSONInt64(draft.clientId)
  );
}

function IBConnectionFields({
  id,
  draft,
  busy,
  onChange,
}: {
  id: string;
  draft: Record<string, string>;
  busy: boolean;
  onChange: (patch: Record<string, string>) => void;
}) {
  const { t } = useTranslation("trading");
  return (
    <div className="grid gap-3 md:grid-cols-2">
      <div className="flex flex-col gap-y-2">
        <Label htmlFor={`${id}-host`}>{t("marketData:settings.host")}</Label>
        <Input
          id={`${id}-host`}
          value={draft.host}
          onChange={(event) => onChange({ host: event.target.value })}
          disabled={busy}
        />
      </div>
      <div className="flex flex-col gap-y-2">
        <Label htmlFor={`${id}-port`}>{t("marketData:settings.port")}</Label>
        <Input
          id={`${id}-port`}
          type="number"
          inputMode="numeric"
          min={1}
          max={65535}
          value={draft.port}
          onChange={(event) => onChange({ port: event.target.value })}
          disabled={busy}
        />
      </div>
      <div className="flex flex-col gap-y-2">
        <Label htmlFor={`${id}-client-id`}>
          {t("marketData:settings.clientId")}
        </Label>
        <Input
          id={`${id}-client-id`}
          type="number"
          inputMode="numeric"
          value={draft.clientId}
          placeholder={t("marketData:settings.autoClientId")}
          onChange={(event) => onChange({ clientId: event.target.value })}
          disabled={busy}
        />
      </div>
    </div>
  );
}

/** Card links of a connection: the venue docs for its trading mode. */
function tradingConnectionLinks(provider: string, mode: TradingMode) {
  const guide = TRADING_PROVIDER_GUIDES[provider];
  return { siteUrl: guide?.siteUrl, docsUrl: guide?.docsUrl?.[mode] };
}

function AddTradingConnectionDialog({
  provider,
  credentialStorage,
  onClose,
  onCreated,
}: {
  provider: TradingProvider;
  credentialStorage: LoadState<CredentialStorage>;
  onClose: () => void;
  onCreated: (connectionId: string) => void;
}) {
  const { t } = useTranslation("trading");
  const { t: tc } = useTranslation();
  const { t: tv } = useTranslation("venues");
  const { createTradingConnection } = useOfficerApi();
  const id = useId();
  const fields = TRADING_PROVIDER_CREDENTIAL_FIELDS.get(provider.type);
  const [label, setLabel] = useState(provider.title);
  const [mode, setMode] = useState<TradingMode>("paper");
  const [credentialDraft, setCredentialDraft] = useState<
    Record<string, string>
  >(() => Object.fromEntries(fields ? fields.map(({ key }) => [key, ""]) : []));
  const [ibSettings, setIBSettings] = useState({
    host: "127.0.0.1",
    port: IB_GATEWAY_PORTS.paper,
    clientId: "",
  });
  // The prefilled gateway port follows the mode; a port the operator typed
  // stays as typed.
  const changeMode = (next: TradingMode) => {
    setMode(next);
    setIBSettings((draft) =>
      draft.port === IB_GATEWAY_PORTS[mode]
        ? { ...draft, port: IB_GATEWAY_PORTS[next] }
        : draft,
    );
  };
  const isIB = provider.type === "ib";
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const errorBanner = useRef<HTMLDivElement>(null);
  const guide = TRADING_PROVIDER_GUIDES[provider.type];
  const ready =
    label.trim() !== "" &&
    (isIB
      ? ibSettingsReady(ibSettings)
      : fields !== undefined &&
        fields.every(({ key }) => credentialDraft[key].trim() !== ""));

  useEffect(() => {
    if (error !== "") errorBanner.current?.scrollIntoView({ block: "nearest" });
  }, [error]);

  const submit = async () => {
    if (busy || !ready) return;
    setBusy(true);
    setError("");
    try {
      const credentials: Record<string, string | number> = {};
      if (isIB) {
        putIfFilled(credentials, "host", ibSettings.host);
        putNumberIfFilled(credentials, "port", ibSettings.port);
        putNumberIfFilled(credentials, "clientId", ibSettings.clientId);
      } else if (fields) {
        for (const { key } of fields) {
          credentials[key] = credentialDraft[key].trim();
        }
      }
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
          {!isIB && !fields && (
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
          <fieldset className="flex flex-col gap-y-2" disabled={busy}>
            <Label asChild>
              <legend>{t("create.mode")}</legend>
            </Label>
            <Segmented
              value={mode}
              onChange={(value) => changeMode(value as TradingMode)}
              options={[
                { value: "paper", label: t("mode.paper") },
                { value: "live", label: t("mode.live") },
              ]}
            />
          </fieldset>
          <UnsealedKeysWarning storage={credentialStorage} />
          <div className="flex flex-col gap-y-3">
            <h2 className="text-sm font-bold">
              {t("create.prepareAccount", { provider: provider.title })}
            </h2>
            {guide?.steps && (
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
          {isIB ? (
            <IBConnectionFields
              id={id}
              draft={ibSettings}
              busy={busy}
              onChange={(patch) =>
                setIBSettings((draft) => ({ ...draft, ...patch }))
              }
            />
          ) : (
            fields?.map((field) => (
              <div key={field.key} className="flex flex-col gap-y-2">
                <Label htmlFor={`${id}-${field.key}`}>
                  {t(field.labelKey)}
                </Label>
                <Input
                  id={`${id}-${field.key}`}
                  type="password"
                  autoComplete="new-password"
                  value={credentialDraft[field.key]}
                  required
                  disabled={busy}
                  onChange={(event) =>
                    setCredentialDraft({
                      ...credentialDraft,
                      [field.key]: event.target.value,
                    })
                  }
                />
              </div>
            ))
          )}
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
            <Button type="submit" disabled={busy || !ready}>
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
  const [initialLabel] = useState(connection.label);
  const [label, setLabel] = useState(initialLabel);
  const [credentialDraft, setCredentialDraft] = useState<
    Record<string, string>
  >(() => Object.fromEntries(fields ? fields.map(({ key }) => [key, ""]) : []));
  const isIB = connection.provider === "ib";
  const [initialIBSettings] = useState(() => ({
    host: settingString(connection.settings, "host"),
    port: settingString(connection.settings, "port"),
    clientId: settingString(connection.settings, "clientId"),
  }));
  const [ibSettings, setIBSettings] = useState(initialIBSettings);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const labelChanged = label.trim() !== "" && label.trim() !== initialLabel;
  const changed = isIB
    ? label.trim() !== "" &&
      ibSettingsReady(ibSettings) &&
      (labelChanged ||
        ibSettings.host !== initialIBSettings.host ||
        ibSettings.port !== initialIBSettings.port ||
        ibSettings.clientId !== initialIBSettings.clientId)
    : fields !== undefined &&
      (labelChanged ||
        fields.some(({ key }) => credentialDraft[key].trim() !== ""));

  const submit = async () => {
    if (busy || !changed) return;
    const body: {
      label?: string;
      credentials?: Record<string, string | number>;
    } = {};
    if (labelChanged) body.label = label.trim();
    const credentials: Record<string, string | number> = {};
    if (isIB) {
      if (ibSettings.host !== initialIBSettings.host) {
        putIfFilled(credentials, "host", ibSettings.host);
      }
      if (ibSettings.port !== initialIBSettings.port) {
        putNumberIfFilled(credentials, "port", ibSettings.port);
      }
      if (ibSettings.clientId !== initialIBSettings.clientId) {
        putNumberIfFilled(credentials, "clientId", ibSettings.clientId);
      }
    } else if (fields) {
      for (const { key } of fields) {
        const value = credentialDraft[key].trim();
        if (value !== "") credentials[key] = value;
      }
    }
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
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <form
          className="flex flex-col gap-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <DialogHeader>
            <DialogTitle>{tc("actions.editSettings")}</DialogTitle>
            <DialogDescription>{t("settings.description")}</DialogDescription>
          </DialogHeader>
          {error && <ErrorBanner message={error} />}
          {!isIB && !fields && (
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
          {isIB ? (
            <IBConnectionFields
              id={id}
              draft={ibSettings}
              busy={busy}
              onChange={(patch) =>
                setIBSettings((draft) => ({ ...draft, ...patch }))
              }
            />
          ) : (
            <>
              <UnsealedKeysWarning storage={credentialStorage} />
              {fields?.map((field) => (
                <div key={field.key} className="flex flex-col gap-y-2">
                  <Label htmlFor={`${id}-${field.key}`}>
                    {t(field.labelKey)}
                  </Label>
                  <Input
                    id={`${id}-${field.key}`}
                    type="password"
                    autoComplete="new-password"
                    value={credentialDraft[field.key]}
                    placeholder={
                      connection.secrets[field.key]
                        ? tv("settings.keepStoredSecret")
                        : ""
                    }
                    onChange={(event) =>
                      setCredentialDraft({
                        ...credentialDraft,
                        [field.key]: event.target.value,
                      })
                    }
                    disabled={busy}
                  />
                </div>
              ))}
            </>
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
  checkSection,
  busy,
  checking,
  checkConnection,
  check,
  checkError,
}: {
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
          {t("check.result", {
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

function ConnectionCard({
  connection,
  provider,
  credentialStorage,
  focusOnMount,
  reload,
}: {
  connection: TradingConnection;
  provider: TradingProvider;
  credentialStorage: LoadState<CredentialStorage>;
  focusOnMount: boolean;
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
    {
      key: "instruments",
      label: t("instruments.title"),
      done: connection.instruments.length > 0,
      target: instrumentSection,
    },
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
          </>
        }
      />
      <CardContent className="flex flex-col gap-y-6">
        <div className="flex flex-col gap-y-1 text-xs">
          <dl className="flex flex-wrap gap-2">
            <dt className="text-muted">{tv("venueAccount")}</dt>
            <dd className="break-all">{connection.venueIdentity}</dd>
          </dl>
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
          ) : (
            TRADING_PROVIDER_CREDENTIAL_FIELDS.get(connection.provider)?.map(
              (field) => (
                <p key={field.key}>
                  {t(
                    connection.secrets[field.key]
                      ? field.storedKey
                      : field.notSetKey,
                  )}
                </p>
              ),
            )
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
        <ConnectionCheckSection
          checkSection={checkSection}
          busy={busy}
          checking={checking}
          checkConnection={checkConnection}
          check={check}
          checkError={checkError}
        />
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
                <ErrorBanner
                  key={connection.id}
                  message={t("missingProvider", {
                    provider: connection.provider,
                  })}
                />
              );
            return (
              <ConnectionCard
                key={connection.id}
                connection={connection}
                provider={provider}
                credentialStorage={credentialStorage}
                focusOnMount={connection.id === focusConnectionId}
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
            docsUrl: TRADING_PROVIDER_GUIDES[provider.type]?.docsUrl?.paper,
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
      {snapshot && selectedProvider && (
        <AddTradingConnectionDialog
          key={selectedProvider.type}
          provider={selectedProvider}
          credentialStorage={credentialStorage}
          onClose={() => setSelectedProvider(null)}
          onCreated={(connectionId) => {
            setFocusConnectionId(connectionId);
            reload();
          }}
        />
      )}
    </Page>
  );
}
