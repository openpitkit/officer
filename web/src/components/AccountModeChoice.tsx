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

import { AlertTriangle, Loader2, RefreshCw } from "lucide-react";
import { useEffect, useId, useState, type ComponentProps } from "react";
import { useTranslation } from "react-i18next";

import {
  putAccountMode,
  type AccountFeatures,
  type AccountMode,
  type AccountModeRequest,
  type AccountModeState,
} from "@/api/accountMode";
import type { LoadState, PollingResult } from "@/api/usePolling";
import { errMessage } from "@/components/AccountDialogs";
import { Autocomplete } from "@/components/Autocomplete";
import { CodeSuggestionFailure } from "@/components/CodeSuggestionFailure";
import { Segmented } from "@/components/data";
import { Notice } from "@/components/Notice";
import {
  CompactStaleState,
  ErrorBanner,
  ErrorState,
  LoadingStatus,
  StaleState,
} from "@/components/PageStates";
import { StatRow } from "@/components/StatRow";
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
import { Label } from "@/components/ui/label";
import { useApiClient, useOfficerApi } from "@/framework";
import { holdingScopeReload } from "@/lib/accountScope";
import { useAccountCodeSuggestions } from "@/lib/useAccountCodeSuggestions";

/** The account the server creates for `single` when no account exists. */
const CREATED_ACCOUNT = "default";

/** Account codes enough to tell none, one and several accounts apart. */
const ACCOUNT_SAMPLE = { limit: 2 };

const LOADING: LoadState<string[]> = {
  state: "loading",
  data: null,
  error: null,
};

/**
 * The request the choice stores, or null while it cannot be built yet. The
 * account is one the server reported or the operator picked; with no account
 * at all the server creates one.
 */
function choiceRequest(
  choice: AccountMode | null,
  features: AccountFeatures,
  accounts: LoadState<string[]>,
  picked: string,
): AccountModeRequest | null {
  if (choice === null) {
    return null;
  }
  if (choice === "multi") {
    return { mode: "multi" };
  }
  if (!features.accounts) {
    return { mode: "single", account: features.account };
  }
  if (accounts.state !== "ready") {
    return null;
  }
  if (accounts.data.length === 0) {
    return { mode: "single" };
  }
  if (accounts.data.length === 1) {
    return { mode: "single", account: accounts.data[0] };
  }
  const account = picked.trim();
  return account === "" ? null : { mode: "single", account };
}

function isStored(request: AccountModeRequest, state: AccountModeState) {
  return (
    request.mode === state.mode &&
    (request.mode === "multi" || request.account === state.account)
  );
}

/**
 * Whether storing `request` restarts Officer: it enforces other account
 * features than the running process does, as the server decides it, and the
 * server reported that the running process does not take a change at once
 * (`live`).
 */
function restarts(
  request: AccountModeRequest,
  features: AccountFeatures,
  live: boolean,
) {
  if (live) {
    return false;
  }
  return request.mode === "multi"
    ? !features.accounts
    : features.accounts || request.account !== features.account;
}

/**
 * Shows the stored account mode and lets the operator choose the next one,
 * then stores it and restarts Officer when the running mode has to change.
 * Only a save that restarts asks for confirmation first, and Officer restarts
 * only after one: a save the server answers with a restart unexpectedly asks
 * for it then.
 */
export function AccountModeChoice({
  load,
  restart,
  disabled = false,
  onSaved,
}: {
  /** The polled account mode; a failed background poll marks it stale. */
  load: Extract<LoadState<AccountModeState>, { state: "ready" }>;
  /** Requests the restart a save needs; rejects when it is refused. */
  restart: () => Promise<void>;
  /** Blocks the choice, e.g. while the host restarts or stops Officer. */
  disabled?: boolean;
  /**
   * Called with the stored state once a save needs no restart. The page
   * reload other account features trigger waits until what it returns
   * settles, so a caller can reload the page itself once its follow-up is
   * done.
   */
  onSaved?: (state: AccountModeState) => Promise<void> | void;
}) {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const client = useApiClient();
  const { fetchAccounts } = useOfficerApi();
  const pickerId = useId();
  const state = load.data;
  const { features } = state;
  const [choice, setChoice] = useState<AccountMode | null>(state.mode);
  const [picked, setPicked] = useState(
    state.mode === "single" ? state.account : "",
  );
  const [accounts, setAccounts] = useState<LoadState<string[]>>(LOADING);
  const [accountsAttempt, setAccountsAttempt] = useState(0);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<"saved" | "restarting" | null>(null);

  const listNeeded = choice === "single" && features.accounts;
  const several =
    listNeeded && accounts.state === "ready" && accounts.data.length > 1;
  const suggestions = useAccountCodeSuggestions(picked, several);
  const request = choiceRequest(choice, features, accounts, picked);
  const blocked = busy || disabled;
  // A stored choice the running process does not apply yet saves again, so a
  // restart that was refused can be requested once more.
  const canSave =
    request !== null &&
    (!isStored(request, state) || state.restartRequired) &&
    !blocked;

  useEffect(() => {
    if (!listNeeded) {
      return;
    }
    const controller = new AbortController();
    fetchAccounts(ACCOUNT_SAMPLE, controller.signal).then(
      (items) => {
        if (!controller.signal.aborted) {
          setAccounts({
            state: "ready",
            data: items.map(({ code }) => code),
            error: null,
          });
        }
      },
      (err: unknown) => {
        if (!controller.signal.aborted) {
          setAccounts({ state: "error", data: null, error: errMessage(err) });
        }
      },
    );
    return () => controller.abort();
  }, [listNeeded, fetchAccounts, accountsAttempt]);

  const retryAccounts = () => {
    setAccounts(LOADING);
    setAccountsAttempt((n) => n + 1);
  };

  const choose = (value: string) => {
    if (value === choice) {
      return;
    }
    setChoice(value as AccountMode);
    setAccounts(LOADING);
    setOutcome(null);
    setError(null);
  };

  // `confirmed` is whether the operator confirmed a restart for this save.
  const save = async (next: AccountModeRequest, confirmed: boolean) => {
    setBusy(true);
    setError(null);
    let confirm = false;
    try {
      // A save the running process takes at once changes the account scope,
      // so the page reload it triggers waits for `onSaved`.
      await holdingScopeReload(async () => {
        const answer = await putAccountMode(client, next);
        if (!answer.restartRequired) {
          setOutcome("saved");
          await onSaved?.(answer);
        } else if (confirmed) {
          await restart();
          setOutcome("restarting");
        } else {
          // The running process did not take the mode at once after all:
          // it is stored, and the restart that applies it waits for a
          // confirmation.
          confirm = true;
        }
      });
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
      setConfirming(confirm);
    }
  };

  const requestSave = () => {
    if (request === null) {
      return;
    }
    if (restarts(request, features, state.live)) {
      setConfirming(true);
    } else {
      void save(request, false);
    }
  };

  const modeLabel = (mode: AccountMode | null, account: string) => {
    if (mode === null) {
      return t("accountMode.notChosen");
    }
    return mode === "multi"
      ? t("accountMode.modes.multi")
      : t("accountMode.singleAccount", { account });
  };

  return (
    <div className="flex flex-col gap-y-4">
      <div>
        <StatRow
          label={t("accountMode.stored")}
          value={modeLabel(state.mode, state.account)}
          mono={false}
        />
        {state.restartRequired && (
          <StatRow
            label={t("accountMode.running")}
            value={modeLabel(
              features.accounts ? "multi" : "single",
              features.account,
            )}
            mono={false}
          />
        )}
      </div>

      {outcome === "restarting" ? (
        <div role="status" className="flex items-center gap-2 text-xs">
          <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin" />
          <span>{t("accountMode.restarting")}</span>
          <CompactStaleState load={load} />
        </div>
      ) : (
        <div className="flex flex-col gap-y-3">
          <Segmented
            ariaLabel={t("accountMode.title")}
            value={choice ?? undefined}
            options={[
              { value: "single", label: t("accountMode.modes.single") },
              { value: "multi", label: t("accountMode.modes.multi") },
            ]}
            onChange={choose}
            disabled={blocked}
          />
          {/* Right under the choice, so the mode details never move it. */}
          <Button
            type="button"
            size="sm"
            onClick={requestSave}
            disabled={!canSave}
          >
            {tc("actions.save")}
          </Button>
          {outcome === "saved" && (
            <p role="status" className="text-xs font-medium text-[var(--ok)]">
              {t("accountMode.saved")}
            </p>
          )}
          {choice === "single" && !features.accounts && (
            <p className="text-xs text-muted-lt">
              {t("accountMode.onlyAccount", { account: features.account })}
            </p>
          )}
          {listNeeded && accounts.state === "loading" && <LoadingStatus />}
          {listNeeded && accounts.state === "error" && (
            <div className="flex flex-col gap-y-2">
              <ErrorBanner message={accounts.error} />
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={retryAccounts}
                disabled={blocked}
              >
                <RefreshCw className="h-3.5 w-3.5" />
                {tc("actions.retry")}
              </Button>
            </div>
          )}
          {listNeeded &&
            accounts.state === "ready" &&
            accounts.data.length < 2 && (
              <p className="text-xs text-muted-lt">
                {accounts.data.length === 0
                  ? t("accountMode.createsAccount", {
                      account: CREATED_ACCOUNT,
                    })
                  : t("accountMode.onlyAccount", {
                      account: accounts.data[0],
                    })}
              </p>
            )}
          {several && (
            <>
              <div className="flex flex-col gap-y-2">
                <Label htmlFor={pickerId}>{tc("fields.account")}</Label>
                <Autocomplete
                  id={pickerId}
                  value={picked}
                  suggestions={suggestions.codes}
                  disabled={blocked}
                  onChange={setPicked}
                  onClear={() => setPicked("")}
                  clearLabel={tc("filters.clearField")}
                />
                <CodeSuggestionFailure
                  failed={suggestions.failed}
                  kind="account"
                />
              </div>
              <Notice
                tone="warn"
                icon={AlertTriangle}
                title={t("accountMode.otherAccounts.title")}
              >
                <p>{t("accountMode.otherAccounts.detail")}</p>
              </Notice>
            </>
          )}
        </div>
      )}

      {error !== null && <ErrorBanner message={error} />}

      <AlertDialog
        open={confirming}
        onOpenChange={(open) => {
          if (!open && !busy) {
            setConfirming(false);
          }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("accountMode.confirm.title")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("accountMode.confirm.description")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>
              {tc("actions.cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="danger"
              disabled={!canSave}
              onClick={(event) => {
                event.preventDefault();
                if (canSave && request !== null) {
                  void save(request, true);
                }
              }}
            >
              {busy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
              {tc("actions.save")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/**
 * The polled account mode and its choice: a loading mark, a failed first load
 * with a retry, then the choice under a stale mark while a background poll
 * fails.
 */
export function AccountModePanel({
  poll: { load, reload },
  restart,
  disabled,
  onSaved,
}: {
  poll: PollingResult<AccountModeState>;
} & Omit<ComponentProps<typeof AccountModeChoice>, "load">) {
  const { t } = useTranslation();

  if (load.state === "loading") {
    return <LoadingStatus />;
  }
  if (load.state === "error") {
    return (
      <ErrorState
        title={t("states.accountModeLoadError")}
        message={load.error}
        onRetry={reload}
      />
    );
  }
  return (
    <div className="flex flex-col gap-y-4">
      <StaleState load={load} reload={reload} />
      <AccountModeChoice
        load={load}
        restart={restart}
        disabled={disabled}
        onSaved={onSaved}
      />
    </div>
  );
}
