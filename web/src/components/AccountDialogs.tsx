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

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Ban } from "lucide-react";

import type { Account } from "@/api/types";
import { Autocomplete } from "@/components/Autocomplete";
import { CodeSuggestionFailure } from "@/components/CodeSuggestionFailure";
import { ErrorBanner } from "@/components/PageStates";
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
import { ApiError, useOfficerApi } from "@/framework";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { useAssetCodeSuggestions } from "@/lib/useAssetCodeSuggestions";

/** Returns the text to show for a failed request. */
// eslint-disable-next-line react-refresh/only-export-components -- Shared helper.
export function errMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

/** Returns the currency code, or "-" when it is blank. */
// eslint-disable-next-line react-refresh/only-export-components -- Shared helper.
export function currencyText(currency: string): string {
  return currency.trim() === "" ? "-" : currency;
}

/** The account's effective currency and the tier it comes from. */
export function EffectiveCurrency({ account }: { account: Account }) {
  const { t } = useTranslation("accounts");
  return (
    <>
      {currencyText(account.effectiveCurrency ?? "")}
      {account.currencyOrigin
        ? ` · ${t(`editCurrency.origins.${account.currencyOrigin}`)}`
        : ""}
    </>
  );
}

/** Blocks the account's trading with the reason the operator enters. */
export function BlockAccountDialog({
  account,
  open,
  onOpenChange,
  onDone,
}: {
  account: Account | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Account) => void;
}) {
  const { t } = useTranslation("accounts");
  const { blockAccount } = useOfficerApi();
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const trimmed = reason.trim();

  const submit = async () => {
    if (!account || trimmed.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await blockAccount(account.code, trimmed, "reject");
      onOpenChange(false);
      setReason("");
      onDone(updated);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        onOpenChange(next);
        if (!next) {
          setReason("");
          setError(null);
        }
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("blockAccount.titlePrefix")}{" "}
            <span className="nums text-accent">{account?.code}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">{t("blockAccount.description")}</p>
        <div className="space-y-2">
          <Label htmlFor="block-account-reason">
            {t("blockAccount.reasonLabel")}
          </Label>
          <Textarea
            id="block-account-reason"
            value={reason}
            autoFocus
            placeholder={t("blockAccount.reasonPlaceholder")}
            onChange={(e) => setReason(e.target.value)}
          />
        </div>
        {error && (
          <ErrorBanner message={error} onDismiss={() => setError(null)} />
        )}
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {t("blockAccount.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || trimmed.length === 0}
          >
            <Ban className="h-3.5 w-3.5" />
            {t("blockAccount.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Confirms and lifts the account's own trading block. */
export function UnblockAccountConfirm({
  account,
  open,
  onOpenChange,
  onDone,
}: {
  account: Account | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Account) => void;
}) {
  const { t } = useTranslation("accounts");
  const { unblockAccount } = useOfficerApi();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!account) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await unblockAccount(account.code, "reject");
      onOpenChange(false);
      onDone(updated);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("unblockAccount.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("unblockAccount.descriptionPrefix")}{" "}
            <span className="nums text-accent">{account?.code}</span>{" "}
            {t("unblockAccount.descriptionSuffix")}
            {/* The action stays enabled while the account carries its own
                block, but lifting it leaves the group block in force, so the
                confirmation must not promise that orders flow again. */}
            {account?.groupBlocked && (
              <span className="mt-2 block text-[var(--warn)]">
                {t("unblockAccount.groupStillBlocked", {
                  group: account.group,
                })}
              </span>
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <ErrorBanner message={error} onDismiss={() => setError(null)} />
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>
            {t("unblockAccount.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              void submit();
            }}
            disabled={busy}
          >
            {t("unblockAccount.submit")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

/** Shows the account's currency cascade and sets its own currency. */
export function AccountCurrencyDialog({
  account,
  open,
  onOpenChange,
  onDone,
}: {
  account: Account;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: () => void;
}) {
  const { t } = useTranslation("accounts");
  const { setAccountCurrency } = useOfficerApi();
  const [currency, setCurrency] = useState(account.currency ?? "");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const assetSuggestionResult = useAssetCodeSuggestions(currency, open);

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      await setAccountCurrency(account.code, currency.trim());
      onDone();
    } catch (err) {
      setError(errMessage(err));
      setBusy(false);
    }
  };

  const hasOpenPositions = (account.positionCount ?? 0) > 0;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("editCurrency.title")}</DialogTitle>
          <DialogDescription>
            {t("editCurrency.description", { account: account.code })}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="rounded-card border border-border bg-surface-2 p-3 text-xs">
            <p className="font-medium text-text">
              {t("editCurrency.cascadeTitle")}
            </p>
            <dl className="mt-2 grid grid-cols-[7rem_1fr] gap-1 text-muted-lt">
              <dt>{t("editCurrency.accountTier")}</dt>
              <dd className="nums">{currencyText(account.currency ?? "")}</dd>
              <dt>{t("editCurrency.groupTier")}</dt>
              <dd className="nums">
                {currencyText(account.currencyCascade?.group ?? "")}
              </dd>
              <dt>{t("editCurrency.defaultTier")}</dt>
              <dd className="nums">
                {currencyText(account.currencyCascade?.default ?? "")}
              </dd>
              <dt>{t("editCurrency.effective")}</dt>
              <dd className="nums">
                <EffectiveCurrency account={account} />
              </dd>
            </dl>
          </div>
          {hasOpenPositions && (
            <p className="text-xs text-[var(--warning)]">
              {t("editCurrency.blockedHint")}
            </p>
          )}
          <div className="space-y-2">
            <Label htmlFor="account-currency">
              {t("editCurrency.currencyLabel")}
            </Label>
            <Autocomplete
              id="account-currency"
              value={currency}
              onChange={setCurrency}
              suggestions={assetSuggestionResult.codes}
              disabled={busy}
              placeholder="USD"
              spellCheck={false}
              onClear={() => setCurrency("")}
              clearLabel={t("filters.clearField")}
            />
            <p className="text-[0.6875rem] text-muted">
              {t("editCurrency.currencyHint")}
            </p>
            <CodeSuggestionFailure
              kind="asset"
              failed={assetSuggestionResult.failed}
            />
          </div>
        </div>
        {error && (
          <ErrorBanner message={error} onDismiss={() => setError(null)} />
        )}
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {t("editCurrency.cancel")}
          </Button>
          <Button size="sm" onClick={() => void submit()} disabled={busy}>
            {t("editCurrency.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
