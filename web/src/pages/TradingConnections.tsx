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
import { AlertTriangle, RefreshCw, Settings } from "lucide-react";

import type {
  TradingAccessSubject,
  TradingConnection,
  TradingConnectionCheck,
  TradingProvider,
  TradingSymbolVerification,
} from "@/api/types";
import { useTrading } from "@/api/useTrading";
import { Autocomplete } from "@/components/Autocomplete";
import { ClearableInput } from "@/components/ClearableInput";
import { CodeSuggestionFailure } from "@/components/CodeSuggestionFailure";
import { Segmented } from "@/components/data";
import {
  AccessBlockedHint,
  accessGrantBlock,
  AddProviderButton,
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
  VenueAccessList,
  type AccessGrantBlock,
  type InstrumentDraftFields,
} from "@/components/venues";
import { Page } from "@/components/Page";
import {
  EmptyState,
  ErrorBanner,
  ErrorState,
  StaleState,
  TableSkeleton,
} from "@/components/PageStates";
import { Badge } from "@/components/ui/badge";
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
import { useAccountCodeSuggestions } from "@/lib/useAccountCodeSuggestions";
import { useAssetCodeSuggestions } from "@/lib/useAssetCodeSuggestions";
import { useGroupCodeSuggestions } from "@/lib/useGroupCodeSuggestions";
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

type AccessSubjectKind = "account" | "group";

/** Warns about unsealed credentials without restricting key entry. */
export function UnsealedKeysWarning() {
  const { t } = useTranslation("trading");
  const { t: tc } = useTranslation();
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
        {tc("sensitiveData.masterKeyLink")}
      </a>
      <p className="text-xs">{t("unsealed.caution")}</p>
    </div>
  );
}

function AddTradingConnectionDialog({
  provider,
  credentialsSealed,
  onClose,
  onCreated,
}: {
  provider: TradingProvider;
  credentialsSealed: boolean;
  onClose: () => void;
  onCreated: (connectionId: string) => void;
}) {
  const { t } = useTranslation("trading");
  const { t: tc } = useTranslation();
  const { t: tv } = useTranslation("venues");
  const { createTradingConnection } = useOfficerApi();
  const id = useId();
  const [label, setLabel] = useState(provider.title);
  const [apiKey, setApiKey] = useState("");
  const [apiSecret, setApiSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const errorBanner = useRef<HTMLDivElement>(null);
  const guide = TRADING_PROVIDER_GUIDES[provider.type];
  const ready =
    label.trim() !== "" && apiKey.trim() !== "" && apiSecret.trim() !== "";

  useEffect(() => {
    if (error !== "") errorBanner.current?.scrollIntoView({ block: "nearest" });
  }, [error]);

  const submit = async () => {
    if (busy || !ready) return;
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
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <div className="space-y-2">
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
          <fieldset className="space-y-2" disabled={busy}>
            <Label asChild>
              <legend>{t("create.mode")}</legend>
            </Label>
            <Segmented
              value="paper"
              options={[
                { value: "paper", label: t("mode.paper") },
                { value: "live", label: t("mode.live"), disabled: true },
              ]}
            />
            <p className="text-xs text-muted">{t("create.liveNote")}</p>
          </fieldset>
          {!credentialsSealed && <UnsealedKeysWarning />}
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
          <div className="space-y-2">
            <Label htmlFor={`${id}-api-key`}>{tv("settings.apiKey")}</Label>
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
              {tv("settings.apiSecret")}
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
  const { t: tv } = useTranslation("venues");
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
            <DialogTitle>{tc("actions.editSettings")}</DialogTitle>
            <DialogDescription>{t("settings.description")}</DialogDescription>
          </DialogHeader>
          {error && <ErrorBanner message={error} />}
          <div className="space-y-2">
            <Label htmlFor={`${id}-label`}>{tv("settings.label")}</Label>
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
            <Label htmlFor={`${id}-api-key`}>{tv("settings.apiKey")}</Label>
            <Input
              id={`${id}-api-key`}
              type="password"
              autoComplete="new-password"
              value={apiKey}
              placeholder={
                connection.secrets.apiKey ? tv("settings.keepStoredSecret") : ""
              }
              onChange={(event) => setApiKey(event.target.value)}
              disabled={busy}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor={`${id}-api-secret`}>
              {tv("settings.apiSecret")}
            </Label>
            <Input
              id={`${id}-api-secret`}
              type="password"
              autoComplete="new-password"
              value={apiSecret}
              placeholder={
                connection.secrets.apiSecret
                  ? tv("settings.keepStoredSecret")
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
  feeBlock,
  focusFeeAccount,
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
  feeBlock: AccessGrantBlock | null;
  focusFeeAccount: () => void;
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
      className="space-y-3 border-t border-border pt-4"
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
        className="space-y-3"
        onSubmit={(event) => {
          event.preventDefault();
          void grantAccess();
        }}
      >
        <h5 className="text-xs font-medium">{tv("actions.grantAccess")}</h5>
        <fieldset className="space-y-2" disabled={busy}>
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
        <div className="space-y-2">
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
          <div className="space-y-2">
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
        )}
        {feeBlock !== null && (
          <AccessBlockedHint
            reason={feeBlock}
            providerTitle={provider.title}
            onSetFeeAccount={focusFeeAccount}
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
      className="space-y-3 border-t border-border pt-4"
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
  const [missingAsset, setMissingAsset] = useState<string | null>(null);
  const [searchError, setSearchError] = useState("");
  const [verifyError, setVerifyError] = useState<{
    symbol: string;
    message: string;
  } | null>(null);
  const [draft, setDraft] = useState<InstrumentDraftFields>({
    externalSymbol: "",
    baseAsset: "",
    quoteAsset: "",
  });
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
  const draftComplete =
    draft.externalSymbol.trim() !== "" &&
    draft.baseAsset.trim() !== "" &&
    draft.quoteAsset.trim() !== "";
  // createAsset names an asset the operator chose to create before the save.
  const saveInstrument = async (createAsset?: string) => {
    if (busy || !draftComplete) return;
    setMissingAsset(null);
    const existingInstrument = connection.instruments.find(
      (instrument) => instrument.externalSymbol === draft.externalSymbol.trim(),
    );
    const ok = await run(
      async () => {
        if (createAsset !== undefined) {
          await api.createAsset(createAsset, "", "");
        }
        await api.upsertTradingInstrument(connection.id, {
          externalSymbol: draft.externalSymbol.trim(),
          baseAsset: draft.baseAsset.trim(),
          quoteAsset: draft.quoteAsset.trim(),
          enabled: existingInstrument ? existingInstrument.enabled : true,
        });
      },
      // A missing draft asset is answered by the explicit create-and-save offer.
      (err) => {
        if (
          err instanceof ApiError &&
          err.code === "asset_missing" &&
          err.asset !== undefined &&
          (err.asset === draft.baseAsset.trim() ||
            err.asset === draft.quoteAsset.trim())
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
      });
      setVerification(null);
      setVerifyError(null);
    }
  };

  return (
    <section
      ref={instrumentSection}
      tabIndex={-1}
      aria-labelledby={`${id}-instruments`}
      className="space-y-3 border-t border-border pt-4"
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
              const pair = /^([^/\s]+)\/([^/\s]+)$/.exec(match.symbol);
              setDraft({
                externalSymbol: match.symbol,
                baseAsset: pair ? pair[1] : match.symbol,
                quoteAsset: pair ? pair[2] : "",
              });
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
      {missingAsset !== null &&
        (draft.baseAsset.trim() === missingAsset ||
          draft.quoteAsset.trim() === missingAsset) && (
          <div role="alert" className="space-y-2 text-xs text-[var(--warn)]">
            <p>{t("instruments.assetMissing", { asset: missingAsset })}</p>
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
  const { t: tv } = useTranslation("venues");
  const api = useOfficerApi();
  const id = useId();
  const [busy, setBusy] = useState(false);
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState("");
  const [feeRequired, setFeeRequired] = useState(false);
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
  const subjectCode = subjectInput.trim();
  const feeAccountCode = feeCode.trim();
  // A group is refused whatever its code, so its reason shows before typing.
  const feeBlock =
    subjectKind === "group"
      ? accessGrantBlock(provider, connection, { accountGroup: subjectCode })
      : subjectCode === ""
        ? null
        : accessGrantBlock(provider, connection, { account: subjectCode });
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
  const grantAccess = async () => {
    if (busy || subjectCode === "" || feeBlock !== null) return;
    const subject: TradingAccessSubject =
      subjectKind === "account"
        ? { account: subjectCode }
        : { accountGroup: subjectCode };
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
            <Badge variant={connection.enabled ? "ok" : "neutral"}>
              {t(
                connection.enabled
                  ? "connection.enabled"
                  : "connection.disabled",
              )}
            </Badge>
          </>
        }
        error={
          connection.state === "unavailable" ? connection.error : undefined
        }
        links={TRADING_PROVIDER_GUIDES[connection.provider]}
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
                void run(() =>
                  api.setTradingConnectionEnabled(
                    connection.id,
                    !connection.enabled,
                  ),
                );
              }}
            >
              {tv(connection.enabled ? "actions.disable" : "actions.enable")}
            </Button>
          </>
        }
      />
      <CardContent className="space-y-6">
        <div className="space-y-1 text-xs">
          <dl className="flex flex-wrap gap-2">
            <dt className="text-muted">{tv("venueAccount")}</dt>
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
            <h4 className="text-sm font-medium">{t("setup.title")}</h4>
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
        {error && deleteSymbol === null && revokeSubject === null && (
          <div className="space-y-2">
            <ErrorBanner message={error} />
            {feeRequired && provider.unlinkedFees && (
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
          feeBlock={feeBlock}
          focusFeeAccount={focusFeeAccount}
          grantAccess={grantAccess}
          onRevoke={(subject) => {
            setError("");
            setRevokeSubject(subject);
          }}
        />
        {provider.unlinkedFees && (
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
      </CardContent>
      {settingsOpen && (
        <ConnectionSettingsDialog
          connection={connection}
          credentialsSealed={credentialsSealed}
          onClose={() => setSettingsOpen(false)}
          reload={reload}
        />
      )}
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
        <div className="space-y-4">
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
                credentialsSealed={load.data.credentialsSealed}
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
            docsUrl: TRADING_PROVIDER_GUIDES[provider.type]?.docsUrl,
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
          credentialsSealed={snapshot.credentialsSealed}
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
