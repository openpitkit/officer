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

import { useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, ExternalLink, Plus, RefreshCw } from "lucide-react";

import type {
  TradingConnection,
  TradingConnectionCheck,
  TradingInstrument,
  TradingProvider,
  TradingSymbolSearch,
  TradingSymbolVerification,
} from "@/api/types";
import { useTrading } from "@/api/useTrading";
import { Autocomplete } from "@/components/Autocomplete";
import { Page } from "@/components/Page";
import {
  EmptyState,
  ErrorBanner,
  ErrorState,
  StaleState,
  TableSkeleton,
} from "@/components/PageStates";
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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  ApiError,
  AUTOCOMPLETE_SUGGESTION_LIMIT,
  useOfficerApi,
} from "@/framework";
import { useAssetCodeSuggestions } from "@/lib/useAssetCodeSuggestions";
import {
  DEFAULT_SEARCH_DEBOUNCE_MS,
  useDebouncedValue,
} from "@/lib/useDebounce";

const TRADING_PROVIDER_GUIDES: Record<
  string,
  {
    siteUrl?: string;
    docsUrl?: string;
    steps?: Array<{
      textKey: string;
      links: Array<{ labelKey: string; href: string }>;
    }>;
  }
> = {
  alpaca: {
    siteUrl: "https://alpaca.markets",
    docsUrl: "https://docs.alpaca.markets/us/docs/paper-trading",
    steps: [
      {
        textKey: "create.alpaca.createAccount",
        links: [
          {
            labelKey: "create.alpaca.signUp",
            href: "https://app.alpaca.markets/signup",
          },
          {
            labelKey: "create.alpaca.logIn",
            href: "https://app.alpaca.markets/account/login",
          },
        ],
      },
      {
        textKey: "create.alpaca.openPaperAccount",
        links: [
          {
            labelKey: "create.alpaca.openDashboard",
            href: "https://app.alpaca.markets/dashboard/overview",
          },
          {
            labelKey: "create.alpaca.aboutPaperTrading",
            href: "https://docs.alpaca.markets/us/docs/paper-trading",
          },
        ],
      },
      {
        textKey: "create.alpaca.generateKeys",
        links: [],
      },
      {
        textKey: "create.alpaca.keepDashboardOpen",
        links: [],
      },
    ],
  },
};

type AccountSuggestionLookup = {
  code: string;
  suggestions: string[];
  error: string;
};

/** Warns about unsealed credentials without restricting key entry. */
export function UnsealedKeysWarning() {
  const { t } = useTranslation("trading");
  return (
    <div
      role="alert"
      className="space-y-2 rounded-card border border-[var(--danger)] bg-[var(--danger-dim)] p-3 text-sm text-[var(--danger)]"
    >
      <h3 className="flex items-center gap-2 font-bold">
        <AlertTriangle className="h-4 w-4 shrink-0" aria-hidden="true" />
        {t("unsealed.title")}
      </h3>
      <p>{t("unsealed.description")}</p>
      <a
        href="https://github.com/openpitkit/officer/blob/main/docs/configuration.md#master-key"
        target="_blank"
        rel="noopener noreferrer"
        className="underline underline-offset-4"
      >
        {t("unsealed.link")}
      </a>
      <p className="text-xs">{t("unsealed.caution")}</p>
    </div>
  );
}

function TradingProviderTile({
  provider,
  onSelect,
}: {
  provider: TradingProvider;
  onSelect: (provider: TradingProvider) => void;
}) {
  const { t } = useTranslation("trading");
  const guide = TRADING_PROVIDER_GUIDES[provider.type];

  return (
    <Card className="overflow-hidden">
      <button
        type="button"
        className="block w-full p-4 text-left transition-colors hover:bg-surface-hover"
        onClick={() => onSelect(provider)}
      >
        <p className="font-semibold text-text">{provider.title}</p>
      </button>
      {(guide?.siteUrl || guide?.docsUrl) && (
        <div className="flex flex-wrap gap-3 border-t border-border px-4 py-2">
          {guide.siteUrl && (
            <a
              href={guide.siteUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-xs text-accent hover:underline"
            >
              <ExternalLink className="h-3 w-3" aria-hidden="true" />
              {t("create.site")}
            </a>
          )}
          {guide.docsUrl && (
            <a
              href={guide.docsUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-xs text-accent hover:underline"
            >
              <ExternalLink className="h-3 w-3" aria-hidden="true" />
              {t("create.docs")}
            </a>
          )}
        </div>
      )}
    </Card>
  );
}

function AddTradingConnectionDialog({
  providers,
  credentialsSealed,
  onClose,
  onCreated,
}: {
  providers: TradingProvider[];
  credentialsSealed: boolean;
  onClose: () => void;
  onCreated: (connectionId: string) => void;
}) {
  const { t } = useTranslation("trading");
  const { t: tc } = useTranslation();
  const { createTradingConnection } = useOfficerApi();
  const id = useId();
  const [step, setStep] = useState<1 | 2 | 3>(1);
  const [provider, setProvider] = useState<TradingProvider | null>(null);
  const [label, setLabel] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [apiSecret, setApiSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const guide = provider ? TRADING_PROVIDER_GUIDES[provider.type] : undefined;
  const ready =
    provider !== null &&
    label.trim() !== "" &&
    apiKey.trim() !== "" &&
    apiSecret.trim() !== "";

  const submit = async () => {
    if (busy || !provider || !ready) return;
    setBusy(true);
    setError("");
    try {
      const connection = await createTradingConnection({
        provider: provider.type,
        label: label.trim(),
        mode: "paper",
        credentials: {
          apiKey: apiKey.trim(),
          apiSecret: apiSecret.trim(),
        },
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
      <DialogContent className="sm:max-w-2xl" hideClose={busy}>
        <DialogHeader>
          <DialogTitle>{t("actions.addConnection")}</DialogTitle>
          <DialogDescription>
            {t("create.step", { current: step })}
          </DialogDescription>
        </DialogHeader>
        {error && <ErrorBanner message={error} />}
        <form
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          {step === 1 && (
            <div className="space-y-3">
              <h2 className="text-sm font-bold">{t("create.chooseVenue")}</h2>
              <div className="grid gap-3 sm:grid-cols-2">
                {providers.map((item) => (
                  <TradingProviderTile
                    key={item.type}
                    provider={item}
                    onSelect={(selected) => {
                      setProvider(selected);
                      setStep(2);
                    }}
                  />
                ))}
              </div>
            </div>
          )}
          {step === 2 && provider && (
            <div className="space-y-3">
              <h2 className="text-sm font-bold">
                {t("create.prepareAccount", { provider: provider.title })}
              </h2>
              {guide?.steps && (
                <ol className="list-outside list-decimal space-y-4 pl-5 text-sm">
                  {guide.steps.map((instruction) => (
                    <li key={instruction.textKey} className="space-y-2 pl-1">
                      <p>{t(instruction.textKey)}</p>
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
          )}
          {step === 3 && provider && (
            <div className="space-y-4">
              <h2 className="text-sm font-bold">
                {t("create.connectionDetails")}
              </h2>
              <div className="space-y-2">
                <Label htmlFor={`${id}-label`}>{t("settings.label")}</Label>
                <Input
                  id={`${id}-label`}
                  value={label}
                  autoFocus
                  required
                  disabled={busy}
                  onChange={(event) => setLabel(event.target.value)}
                />
              </div>
              <fieldset className="space-y-2" disabled={busy}>
                <legend className="text-sm font-medium">
                  {t("create.mode")}
                </legend>
                <div className="flex flex-wrap gap-4">
                  <Label className="flex items-center gap-2">
                    <input
                      type="radio"
                      name={`${id}-mode`}
                      value="paper"
                      checked
                      readOnly
                    />
                    {t("mode.paper")}
                  </Label>
                  <Label className="flex items-center gap-2 text-muted">
                    <input
                      type="radio"
                      name={`${id}-mode`}
                      value="live"
                      disabled
                    />
                    {t("mode.live")}
                  </Label>
                </div>
                <p className="text-xs text-muted">{t("create.liveNote")}</p>
              </fieldset>
              {!credentialsSealed && <UnsealedKeysWarning />}
              <div className="space-y-2">
                <Label htmlFor={`${id}-api-key`}>
                  {t("connection.apiKey")}
                </Label>
                <Input
                  id={`${id}-api-key`}
                  type="password"
                  autoComplete="new-password"
                  value={apiKey}
                  required
                  disabled={busy}
                  onChange={(event) => setApiKey(event.target.value)}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor={`${id}-api-secret`}>
                  {t("connection.apiSecret")}
                </Label>
                <Input
                  id={`${id}-api-secret`}
                  type="password"
                  autoComplete="new-password"
                  value={apiSecret}
                  required
                  disabled={busy}
                  onChange={(event) => setApiSecret(event.target.value)}
                />
              </div>
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
            <Button
              type="button"
              variant="outline"
              disabled={step === 1 || busy}
              onClick={() => setStep((current) => (current === 3 ? 2 : 1))}
            >
              {t("create.back")}
            </Button>
            {step < 3 ? (
              <Button
                key="next"
                type="button"
                disabled={(step === 1 && !provider) || busy}
                onClick={() => setStep((current) => (current === 1 ? 2 : 3))}
              >
                {t("create.next")}
              </Button>
            ) : (
              <Button key="submit" type="submit" disabled={busy || !ready}>
                {t(busy ? "create.creating" : "create.submit")}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ConnectionSettingsDialog({
  connection,
  credentialsSealed,
  onClose,
  reload,
}: {
  connection: TradingConnection;
  credentialsSealed: boolean;
  onClose: () => void;
  reload: () => void;
}) {
  const { t } = useTranslation("trading");
  const { t: tc } = useTranslation();
  const { updateTradingConnectionSettings } = useOfficerApi();
  const id = useId();
  const [initialLabel] = useState(connection.label);
  const [label, setLabel] = useState(initialLabel);
  const [apiKey, setApiKey] = useState("");
  const [apiSecret, setApiSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const labelChanged = label.trim() !== "" && label.trim() !== initialLabel;
  const trimmedApiKey = apiKey.trim();
  const trimmedApiSecret = apiSecret.trim();
  const changed =
    labelChanged || trimmedApiKey !== "" || trimmedApiSecret !== "";

  const submit = async () => {
    if (busy || !changed) return;
    const body: { label?: string; credentials?: Record<string, string> } = {};
    if (labelChanged) body.label = label.trim();
    const credentials: Record<string, string> = {};
    if (trimmedApiKey !== "") credentials.apiKey = trimmedApiKey;
    if (trimmedApiSecret !== "") credentials.apiSecret = trimmedApiSecret;
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
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("actions.editSettings")}</DialogTitle>
            <DialogDescription>{t("settings.description")}</DialogDescription>
          </DialogHeader>
          {error && <ErrorBanner message={error} />}
          <div className="space-y-2">
            <Label htmlFor={`${id}-label`}>{t("settings.label")}</Label>
            <Input
              id={`${id}-label`}
              value={label}
              autoFocus
              onChange={(event) => setLabel(event.target.value)}
              disabled={busy}
            />
          </div>
          {!credentialsSealed && <UnsealedKeysWarning />}
          <div className="space-y-2">
            <Label htmlFor={`${id}-api-key`}>{t("connection.apiKey")}</Label>
            <Input
              id={`${id}-api-key`}
              type="password"
              autoComplete="new-password"
              value={apiKey}
              placeholder={
                connection.secrets.apiKey ? t("settings.keepStoredSecret") : ""
              }
              onChange={(event) => setApiKey(event.target.value)}
              disabled={busy}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor={`${id}-api-secret`}>
              {t("connection.apiSecret")}
            </Label>
            <Input
              id={`${id}-api-secret`}
              type="password"
              autoComplete="new-password"
              value={apiSecret}
              placeholder={
                connection.secrets.apiSecret
                  ? t("settings.keepStoredSecret")
                  : ""
              }
              onChange={(event) => setApiSecret(event.target.value)}
              disabled={busy}
            />
          </div>
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
              {t("actions.save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ConnectionCard({
  connection,
  provider,
  credentialsSealed,
  focusOnMount,
  reload,
}: {
  connection: TradingConnection;
  provider: TradingProvider;
  credentialsSealed: boolean;
  focusOnMount: boolean;
  reload: () => void;
}) {
  const { t } = useTranslation("trading");
  const { t: tc } = useTranslation();
  const api = useOfficerApi();
  const id = useId();
  const [busy, setBusy] = useState(false);
  const [checking, setChecking] = useState(false);
  const [searching, setSearching] = useState(false);
  const [verifying, setVerifying] = useState(false);
  const [error, setError] = useState("");
  const [feeRequired, setFeeRequired] = useState(false);
  const [missingAsset, setMissingAsset] = useState<string | null>(null);
  const [checkError, setCheckError] = useState("");
  const [searchError, setSearchError] = useState("");
  const [verifyError, setVerifyError] = useState<{
    symbol: string;
    message: string;
  } | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [check, setCheck] = useState<TradingConnectionCheck | null>(null);
  const [draft, setDraft] = useState<TradingInstrument>({
    externalSymbol: "",
    baseAsset: "",
    quoteAsset: "",
    enabled: true,
  });
  const [query, setQuery] = useState("");
  const [search, setSearch] = useState<TradingSymbolSearch | null>(null);
  const [verification, setVerification] = useState<{
    symbol: string;
    result: TradingSymbolVerification;
  } | null>(null);
  const [account, setAccount] = useState("");
  const [venueAccount, setVenueAccount] = useState("");
  const [feeCode, setFeeCode] = useState("");
  const [deleteSymbol, setDeleteSymbol] = useState<string | null>(null);
  const [revokeAccount, setRevokeAccount] = useState<string | null>(null);
  const [accountSuggestions, setAccountSuggestions] =
    useState<AccountSuggestionLookup | null>(null);
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
  const card = useRef<HTMLDivElement>(null);
  const baseSuggestions = useAssetCodeSuggestions(draft.baseAsset, true);
  const quoteSuggestions = useAssetCodeSuggestions(draft.quoteAsset, true);
  const accountQuery = useDebouncedValue(
    account.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
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
    const queries = Array.from(
      new Set([accountQuery, feeQuery].filter((code) => code !== "")),
    );
    if (queries.length === 0) return;
    const controller = new AbortController();
    for (const code of queries) {
      void fetchAccounts(
        {
          code,
          codeMatch: "starts_with",
          limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
          sort: "code",
        },
        controller.signal,
      )
        .then((accounts) => {
          if (controller.signal.aborted) return;
          const lookup = {
            code,
            suggestions: Array.from(
              new Set(accounts.map((item) => item.code)),
            ).sort(),
            error: "",
          };
          if (accountQuery === code) setAccountSuggestions(lookup);
          if (feeQuery === code) setFeeAccountSuggestions(lookup);
        })
        .catch((err: unknown) => {
          if (!controller.signal.aborted) {
            const lookup = {
              code,
              suggestions: [],
              error: err instanceof Error ? err.message : String(err),
            };
            if (accountQuery === code) setAccountSuggestions(lookup);
            if (feeQuery === code) setFeeAccountSuggestions(lookup);
          }
        });
    }
    return () => controller.abort();
  }, [accountQuery, feeQuery, fetchAccounts]);

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
  const accountCode = account.trim();
  const feeAccountCode = feeCode.trim();
  const accountSuggestionErrors = Array.from(
    new Set(
      [
        accountSuggestions?.code === accountCode
          ? accountSuggestions.error
          : "",
        feeAccountSuggestions?.code === feeAccountCode
          ? feeAccountSuggestions.error
          : "",
      ].filter((message) => message !== ""),
    ),
  );
  const feeBlocked =
    provider.unlinkedFees &&
    connection.feeAccount === "" &&
    connection.access.length > 0 &&
    accountCode !== "" &&
    !connection.access.some((grant) => grant.account === accountCode);
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
    {
      key: "access",
      label: t("access.title"),
      done: connection.access.length > 0,
      target: accessSection,
    },
    {
      key: "enable",
      label: t("actions.enable"),
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
  const searchSymbols = () => {
    if (searching || query.trim() === "") return;
    setSearch(null);
    void callVenue(
      async () => {
        setSearch(
          await api.searchTradingSymbols(connection.id, {
            query: query.trim(),
          }),
        );
      },
      setSearching,
      setSearchError,
    );
  };
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
      : null;
  const currentVerifyError =
    verifyError?.symbol === draft.externalSymbol.trim()
      ? verifyError.message
      : "";
  const draftComplete =
    draft.externalSymbol.trim() !== "" &&
    draft.baseAsset.trim() !== "" &&
    draft.quoteAsset.trim() !== "";
  // createAsset names an asset the operator chose to create before the save.
  const saveInstrument = async (createAsset?: string) => {
    if (busy || !draftComplete) return;
    setMissingAsset(null);
    const ok = await run(
      async () => {
        if (createAsset !== undefined) {
          await api.createAsset(createAsset, "", "");
        }
        await api.upsertTradingInstrument(connection.id, {
          externalSymbol: draft.externalSymbol.trim(),
          baseAsset: draft.baseAsset.trim(),
          quoteAsset: draft.quoteAsset.trim(),
          enabled: draft.enabled,
        });
      },
      // A missing asset is answered in the instrument form, not as raw text.
      (err) => {
        if (
          err instanceof ApiError &&
          err.code === "asset_missing" &&
          err.asset !== undefined
        ) {
          setMissingAsset(err.asset);
          return true;
        }
        return false;
      },
    );
    if (ok) {
      setDraft({
        externalSymbol: "",
        baseAsset: "",
        quoteAsset: "",
        enabled: true,
      });
      setVerification(null);
      setVerifyError(null);
    }
  };
  const grantAccess = async () => {
    if (busy || accountCode === "" || feeBlocked) return;
    const body: { account: string; venueAccount?: string } = {
      account: accountCode,
    };
    if (provider.venueAccounts && venueAccount.trim() !== "")
      body.venueAccount = venueAccount.trim();
    const ok = await run(() => api.grantTradingAccess(connection.id, body));
    if (ok) {
      setAccount("");
      setVenueAccount("");
    }
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
      <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
        <div className="space-y-2">
          <h2 id={`${id}-title`} className="text-sm font-bold text-text">
            {connection.label}
          </h2>
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <span className="text-muted">{provider.title}</span>
            <Badge>{t(`mode.${connection.mode}`)}</Badge>
            <Badge
              variant={
                connection.state === "running"
                  ? "ok"
                  : connection.state === "unavailable"
                    ? "danger"
                    : "neutral"
              }
            >
              {t(`state.${connection.state}`)}
            </Badge>
            <span>
              {t(
                connection.enabled
                  ? "connection.enabled"
                  : "connection.disabled",
              )}
            </span>
          </div>
          {connection.state === "unavailable" && connection.error && (
            <p className="break-words text-xs text-[var(--danger)]">
              {connection.error}
            </p>
          )}
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={busy}
            onClick={() => setSettingsOpen(true)}
          >
            {t("actions.editSettings")}
          </Button>
          <Button
            ref={enableButton}
            variant="outline"
            size="sm"
            disabled={busy}
            onClick={() => {
              void run(() =>
                api.setTradingConnectionEnabled(
                  connection.id,
                  !connection.enabled,
                ),
              );
            }}
          >
            {t(connection.enabled ? "actions.disable" : "actions.enable")}
          </Button>
        </div>
      </CardHeader>
      <CardContent className="space-y-6">
        <div className="space-y-1 text-xs">
          <dl className="flex flex-wrap gap-2">
            <dt className="text-muted">{t("connection.venueAccount")}</dt>
            <dd className="break-all">{connection.venueIdentity}</dd>
          </dl>
          <p>
            {t(
              connection.secrets.apiKey
                ? "connection.apiKeyStored"
                : "connection.apiKeyNotSet",
            )}
          </p>
          <p>
            {t(
              connection.secrets.apiSecret
                ? "connection.apiSecretStored"
                : "connection.apiSecretNotSet",
            )}
          </p>
        </div>
        {!connection.enabled && (
          <div className="space-y-2">
            <h3 className="text-sm font-medium">{t("setup.title")}</h3>
            <ol
              aria-label={t("setup.title")}
              className="list-inside list-decimal space-y-1 text-xs"
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
        {error && deleteSymbol === null && revokeAccount === null && (
          <div className="space-y-2">
            <ErrorBanner message={error} />
            {feeRequired && provider.unlinkedFees && (
              <Button
                variant="ghost"
                size="sm"
                className="text-accent underline underline-offset-4"
                onClick={focusFeeAccount}
              >
                {t("actions.setFeeAccount")}
              </Button>
            )}
          </div>
        )}
        {(baseSuggestions.failed || quoteSuggestions.failed) && (
          <ErrorBanner message={tc("suggestions.assetLookupFailed")} />
        )}
        {accountSuggestionErrors.map((message) => (
          <ErrorBanner key={message} message={message} />
        ))}
        {feeLookup?.code === feeAccountCode && "error" in feeLookup && (
          <ErrorBanner message={feeLookup.error} />
        )}
        <section
          ref={checkSection}
          tabIndex={-1}
          aria-label={t("actions.check")}
          className="space-y-2"
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
        <section
          ref={instrumentSection}
          tabIndex={-1}
          aria-labelledby={`${id}-instruments`}
          className="space-y-3 border-t border-border pt-4"
        >
          <h3 id={`${id}-instruments`} className="text-sm font-medium">
            {t("instruments.title")}
          </h3>
          <Table aria-label={t("instruments.title")}>
            <TableHeader>
              <TableRow>
                <TableHead>{t("instruments.externalSymbol")}</TableHead>
                <TableHead>{t("instruments.baseAsset")}</TableHead>
                <TableHead>{t("instruments.quoteAsset")}</TableHead>
                <TableHead>{t("connection.enabled")}</TableHead>
                <TableHead>{t("table.actions")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {connection.instruments.map((instrument) => (
                <TableRow key={instrument.externalSymbol}>
                  <TableCell>{instrument.externalSymbol}</TableCell>
                  <TableCell>{instrument.baseAsset}</TableCell>
                  <TableCell>{instrument.quoteAsset}</TableCell>
                  <TableCell>
                    {t(
                      instrument.enabled
                        ? "connection.enabled"
                        : "connection.disabled",
                    )}
                  </TableCell>
                  <TableCell>
                    <div className="flex gap-2">
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={busy}
                        onClick={() => {
                          void run(() =>
                            api.upsertTradingInstrument(connection.id, {
                              ...instrument,
                              enabled: !instrument.enabled,
                            }),
                          );
                        }}
                      >
                        {t(
                          instrument.enabled
                            ? "actions.disable"
                            : "actions.enable",
                        )}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={busy}
                        onClick={() => {
                          setError("");
                          setDeleteSymbol(instrument.externalSymbol);
                        }}
                      >
                        {tc("actions.delete")}
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
              {connection.instruments.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="text-muted">
                    {t("instruments.empty")}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
          <form
            aria-label={t("actions.addInstrument")}
            className="space-y-3"
            onSubmit={(event) => {
              event.preventDefault();
              void saveInstrument();
            }}
          >
            <h4 className="text-xs font-medium">
              {t("actions.addInstrument")}
            </h4>
            {provider.searchesSymbols && (
              <div className="space-y-2">
                <Label htmlFor={`${id}-search`}>{t("search.label")}</Label>
                <div className="flex gap-2">
                  <Input
                    id={`${id}-search`}
                    value={query}
                    onChange={(event) => {
                      setQuery(event.target.value);
                      setSearch(null);
                    }}
                    onKeyDown={(event) => {
                      if (event.key === "Enter") {
                        event.preventDefault();
                        searchSymbols();
                      }
                    }}
                    disabled={busy || searching}
                  />
                  <Button
                    type="button"
                    variant="outline"
                    disabled={busy || searching || query.trim() === ""}
                    onClick={searchSymbols}
                  >
                    {t("search.action")}
                  </Button>
                </div>
                {searchError && <ErrorBanner message={searchError} />}
                {search &&
                  (!search.supported ? (
                    <p className="text-xs">{t("search.unavailable")}</p>
                  ) : search.matches.length === 0 ? (
                    <p className="text-xs">{t("search.empty")}</p>
                  ) : (
                    <ul aria-label={t("search.results")} className="space-y-1">
                      {search.matches.map((match) => (
                        <li key={match.symbol}>
                          <Button
                            type="button"
                            variant="ghost"
                            className="h-auto flex-wrap justify-start text-left"
                            disabled={busy}
                            aria-label={match.symbol}
                            onClick={() => {
                              const pair = /^([^/\s]+)\/([^/\s]+)$/.exec(
                                match.symbol,
                              );
                              setDraft((previous) => ({
                                ...previous,
                                externalSymbol: match.symbol,
                                baseAsset: pair ? pair[1] : match.symbol,
                                quoteAsset: pair ? pair[2] : "",
                              }));
                              setVerification(null);
                              setVerifyError(null);
                            }}
                          >
                            <span>{match.symbol}</span>
                            {match.name && (
                              <span className="text-muted">{match.name}</span>
                            )}
                            {match.exchange && (
                              <span className="text-muted">
                                {match.exchange}
                              </span>
                            )}
                            {!match.tradable && (
                              <span className="text-[var(--warn)]">
                                {t("search.notTradable")}
                              </span>
                            )}
                          </Button>
                        </li>
                      ))}
                    </ul>
                  ))}
              </div>
            )}
            <div className="space-y-2">
              <Label htmlFor={`${id}-symbol`}>
                {t("instruments.externalSymbol")}
              </Label>
              <div className="flex gap-2">
                <Input
                  id={`${id}-symbol`}
                  value={draft.externalSymbol}
                  disabled={busy || verifying}
                  onChange={(event) => {
                    setDraft((previous) => ({
                      ...previous,
                      externalSymbol: event.target.value,
                    }));
                    setVerification(null);
                    setVerifyError(null);
                  }}
                />
                {provider.verifiesSymbols && (
                  <Button
                    type="button"
                    variant="outline"
                    disabled={
                      busy || verifying || draft.externalSymbol.trim() === ""
                    }
                    onClick={verifySymbol}
                  >
                    {t("verify.action")}
                  </Button>
                )}
              </div>
              {verdict && (
                <div role="status" className="space-y-1 text-xs">
                  <p>
                    {t(
                      !verdict.supported
                        ? "verify.unavailable"
                        : !verdict.exists
                          ? "verify.notFound"
                          : verdict.tradable
                            ? "verify.tradable"
                            : "verify.notTradable",
                    )}
                  </p>
                  {verdict.details && (
                    <p className="text-muted">{verdict.details}</p>
                  )}
                </div>
              )}
              {currentVerifyError && (
                <ErrorBanner message={currentVerifyError} />
              )}
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor={`${id}-base`}>
                  {t("instruments.baseAsset")}
                </Label>
                <Autocomplete
                  id={`${id}-base`}
                  value={draft.baseAsset}
                  suggestions={baseSuggestions.codes}
                  disabled={busy}
                  onChange={(value) => {
                    setDraft((previous) => ({ ...previous, baseAsset: value }));
                  }}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor={`${id}-quote`}>
                  {t("instruments.quoteAsset")}
                </Label>
                <Autocomplete
                  id={`${id}-quote`}
                  value={draft.quoteAsset}
                  suggestions={quoteSuggestions.codes}
                  disabled={busy}
                  onChange={(value) => {
                    setDraft((previous) => ({
                      ...previous,
                      quoteAsset: value,
                    }));
                  }}
                />
              </div>
            </div>
            <div className="flex items-center gap-2">
              <input
                id={`${id}-instrument-enabled`}
                type="checkbox"
                checked={draft.enabled}
                disabled={busy}
                onChange={(event) =>
                  setDraft((previous) => ({
                    ...previous,
                    enabled: event.target.checked,
                  }))
                }
              />
              <Label htmlFor={`${id}-instrument-enabled`}>
                {t("connection.enabled")}
              </Label>
            </div>
            {missingAsset !== null &&
              (draft.baseAsset.trim() === missingAsset ||
                draft.quoteAsset.trim() === missingAsset) && (
                <div
                  role="alert"
                  className="space-y-2 text-xs text-[var(--warn)]"
                >
                  <p>
                    {t("instruments.assetMissing", { asset: missingAsset })}
                  </p>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={busy || !draftComplete}
                    onClick={() => {
                      void saveInstrument(missingAsset);
                    }}
                  >
                    {t("instruments.createAssetAndSave", {
                      asset: missingAsset,
                    })}
                  </Button>
                </div>
              )}
            <Button type="submit" disabled={busy || !draftComplete}>
              {t("actions.save")}
            </Button>
          </form>
        </section>
        <section
          ref={accessSection}
          tabIndex={-1}
          aria-labelledby={`${id}-access`}
          className="space-y-3 border-t border-border pt-4"
        >
          <h3 id={`${id}-access`} className="text-sm font-medium">
            {t("access.title")}
          </h3>
          <Table aria-label={t("access.title")}>
            <TableHeader>
              <TableRow>
                <TableHead>{t("access.account")}</TableHead>
                {provider.venueAccounts && (
                  <TableHead>{t("connection.venueAccount")}</TableHead>
                )}
                <TableHead>{t("table.actions")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {connection.access.map((grant) => (
                <TableRow key={grant.account}>
                  <TableCell>{grant.account}</TableCell>
                  {provider.venueAccounts && (
                    <TableCell>{grant.venueAccount}</TableCell>
                  )}
                  <TableCell>
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={busy}
                      onClick={() => {
                        setError("");
                        setRevokeAccount(grant.account);
                      }}
                    >
                      {t("actions.revoke")}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
              {connection.access.length === 0 && (
                <TableRow>
                  <TableCell
                    colSpan={provider.venueAccounts ? 3 : 2}
                    className="text-muted"
                  >
                    {t("access.empty")}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
          <form
            aria-label={t("actions.addAccount")}
            className="space-y-3"
            onSubmit={(event) => {
              event.preventDefault();
              void grantAccess();
            }}
          >
            <h4 className="text-xs font-medium">{t("actions.addAccount")}</h4>
            <div className="space-y-2">
              <Label htmlFor={`${id}-account`}>{t("access.account")}</Label>
              <Autocomplete
                id={`${id}-account`}
                value={account}
                suggestions={
                  accountSuggestions?.code === accountCode
                    ? accountSuggestions.suggestions
                    : []
                }
                disabled={busy}
                onChange={setAccount}
              />
            </div>
            {provider.venueAccounts && (
              <div className="space-y-2">
                <Label htmlFor={`${id}-venue-account`}>
                  {t("connection.venueAccount")}
                </Label>
                <Input
                  id={`${id}-venue-account`}
                  value={venueAccount}
                  disabled={busy}
                  onChange={(event) => setVenueAccount(event.target.value)}
                />
              </div>
            )}
            {feeBlocked && (
              <div className="space-y-2 text-xs text-[var(--warn)]">
                <p>{t("access.feeRequired", { provider: provider.title })}</p>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="text-accent underline underline-offset-4"
                  onClick={focusFeeAccount}
                >
                  {t("actions.setFeeAccount")}
                </Button>
              </div>
            )}
            <Button
              type="submit"
              disabled={busy || accountCode === "" || feeBlocked}
            >
              {t("actions.addAccount")}
            </Button>
          </form>
        </section>
        {provider.unlinkedFees && (
          <section
            aria-labelledby={`${id}-fee`}
            className="space-y-3 border-t border-border pt-4"
          >
            <h3 id={`${id}-fee`} className="text-sm font-medium">
              {t("fee.title")}
            </h3>
            <p className="text-xs text-muted">
              {t("fee.description", { provider: provider.title })}
            </p>
            <p className="text-xs">
              {connection.feeAccount === ""
                ? t("fee.notSet")
                : connection.feeAccount}
            </p>
            <form
              aria-label={t("fee.title")}
              className="space-y-3"
              onSubmit={(event) => {
                event.preventDefault();
                void saveFeeAccount(false);
              }}
            >
              <div className="space-y-2">
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
                />
              </div>
              <div className="flex flex-wrap gap-2">
                <Button type="submit" disabled={busy || feeAccountCode === ""}>
                  {t("actions.save")}
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
                      void run(() => api.clearTradingFeeAccount(connection.id));
                    }}
                  >
                    {tc("actions.clear")}
                  </Button>
                )}
              </div>
            </form>
          </section>
        )}
      </CardContent>
      {settingsOpen && (
        <ConnectionSettingsDialog
          connection={connection}
          credentialsSealed={credentialsSealed}
          onClose={() => setSettingsOpen(false)}
          reload={reload}
        />
      )}
      <AlertDialog
        open={deleteSymbol !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteSymbol(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("instruments.deleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("instruments.deleteDescription", {
                symbol: deleteSymbol,
                label: connection.label,
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {error && <ErrorBanner message={error} />}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>
              {tc("actions.cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="danger"
              disabled={busy}
              onClick={(event) => {
                event.preventDefault();
                if (deleteSymbol === null) return;
                void run(async () => {
                  await api.deleteTradingInstrument(
                    connection.id,
                    deleteSymbol,
                  );
                  setDeleteSymbol(null);
                });
              }}
            >
              {tc("actions.delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <AlertDialog
        open={revokeAccount !== null}
        onOpenChange={(open) => {
          if (!open) setRevokeAccount(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("access.revokeTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("access.revokeDescription", {
                account: revokeAccount,
                label: connection.label,
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {error && <ErrorBanner message={error} />}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>
              {tc("actions.cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="danger"
              disabled={busy}
              onClick={(event) => {
                event.preventDefault();
                if (revokeAccount === null) return;
                void run(async () => {
                  await api.revokeTradingAccess(connection.id, revokeAccount);
                  setRevokeAccount(null);
                });
              }}
            >
              {t("actions.revoke")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  );
}

export function TradingConnections() {
  const { t } = useTranslation("trading");
  const { t: tc } = useTranslation();
  const { load, reload } = useTrading();
  const [addOpen, setAddOpen] = useState(false);
  const [focusConnectionId, setFocusConnectionId] = useState<string | null>(
    null,
  );
  const snapshot = load.state === "ready" ? load.data : null;
  return (
    <Page
      title={t("title")}
      actions={
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            size="sm"
            disabled={!snapshot}
            onClick={() => setAddOpen(true)}
          >
            <Plus />
            {t("actions.addConnection")}
          </Button>
          <Button type="button" variant="ghost" size="sm" onClick={reload}>
            <RefreshCw />
            {tc("actions.refresh")}
          </Button>
        </div>
      }
    >
      {load.state === "loading" && <TableSkeleton rows={3} cols={5} />}
      <StaleState load={load} reload={reload} />
      {load.state === "error" && (
        <ErrorState message={load.error} onRetry={reload} />
      )}
      {load.state === "ready" && (
        <div className="space-y-4">
          {load.data.connections.length === 0 && (
            <EmptyState
              title={t("empty.title")}
              hint={t("empty.description")}
              action={
                <Button type="button" onClick={() => setAddOpen(true)}>
                  <Plus />
                  {t("actions.addConnection")}
                </Button>
              }
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
                credentialsSealed={load.data.credentialsSealed}
                focusOnMount={connection.id === focusConnectionId}
                reload={reload}
              />
            );
          })}
        </div>
      )}
      {snapshot && addOpen && (
        <AddTradingConnectionDialog
          providers={snapshot.providers}
          credentialsSealed={snapshot.credentialsSealed}
          onClose={() => setAddOpen(false)}
          onCreated={(connectionId) => {
            setFocusConnectionId(connectionId);
            reload();
          }}
        />
      )}
    </Page>
  );
}
