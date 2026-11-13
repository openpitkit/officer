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

import { useCallback, useState } from "react";
import { useTranslation } from "react-i18next";
import { CircleAlert } from "lucide-react";
import { ActionButton, useApiClient } from "@/framework";
import { cn } from "@/lib/utils";
import {
  fetchResetSchedule,
  recoverResetSchedule,
  saveResetSchedule,
  resetRealizedAccountPnl,
  type ResetScheduleState,
  type ResetTermination,
} from "@/api/accountPnlReset";
import { usePolling } from "@/api/usePolling";
import type { Account } from "@/api/types";
import { errMessage } from "@/components/AccountDialogs";
import { ErrorBanner } from "@/components/PageStates";
import { SearchableSelect } from "@/components/SearchableSelect";
import { DateTimeField } from "@/components/data/FilterOperators";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
} from "@/components/ui/alert-dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { formatDateTime } from "@/i18n/format";
import { pnlHaltText } from "@/i18n/codeLabel";

function pnlText(pnl: string | undefined): string {
  return pnl && pnl !== "" ? pnl : "-";
}

// eslint-disable-next-line react-refresh/only-export-components -- Shared account value styling.
export function accountPnlClass(pnl: string | undefined): string {
  const value = (pnl ?? "").trim();
  if (value === "" || /^0+(\.0+)?$/.test(value))
    return "text-[var(--pnl-flat)]";
  if (value.startsWith("-") || value.startsWith("−"))
    return "text-[var(--pnl-neg)]";
  return "text-[var(--pnl-pos)]";
}

/** The same authoritative value/halt renderer in both account views. */
export function AccountRealizedPnl({
  account,
  onReset,
}: {
  account: Account;
  onReset?: () => void;
}) {
  const { t } = useTranslation("accounts");
  const haltText = pnlHaltText(t, account.pnlHaltReason);
  return (
    <span
      className={cn(
        "inline-flex items-center justify-end gap-1 nums",
        accountPnlClass(account.pnl),
      )}
    >
      {haltText ? (
        <span
          className="inline-flex text-[var(--warn)]"
          title={haltText}
          aria-label={haltText}
          role="note"
          tabIndex={0}
        >
          <CircleAlert className="size-4" aria-hidden="true" />
        </span>
      ) : (
        pnlText(account.pnl)
      )}
      {onReset && (
        <ActionButton
          icon="reset-pnl"
          title={t("pnlReset.action", { account: account.code })}
          size={24}
          onClick={onReset}
        />
      )}
    </span>
  );
}

export function ResetAccountPnlConfirm({
  account,
  onClose,
  onDone,
}: {
  account: Account | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useTranslation("accounts");
  const client = useApiClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const reset = async () => {
    if (!account) return;
    setBusy(true);
    setError(null);
    try {
      await resetRealizedAccountPnl(client, account.code);
      onDone();
      onClose();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <AlertDialog
      open={account !== null}
      onOpenChange={(open) => {
        if (!open && !busy) {
          setError(null);
          onClose();
        }
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t("pnlReset.confirmTitle", { account: account?.code })}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t("pnlReset.confirmDescription")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <ErrorBanner message={error} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>
            {t("common:actions.cancel")}
          </AlertDialogCancel>
          <Button disabled={busy} onClick={() => void reset()}>
            {t("pnlReset.reset")}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

export function AccountPnlResetSchedule() {
  const { t } = useTranslation("accounts");
  const client = useApiClient();
  const fetcher = useCallback(
    (signal: AbortSignal) => fetchResetSchedule(client, signal),
    [client],
  );
  const { load, reload } = usePolling(fetcher);
  return (
    <section className="flex flex-col gap-y-4 border-t border-border pt-6">
      <h2 className="text-sm font-medium">{t("pnlReset.schedule")}</h2>
      <p className="text-xs text-muted-lt">
        {t("pnlReset.scheduleDescription")}
      </p>
      {load.state === "loading" && <p>{t("pnlReset.loading")}</p>}
      {load.state === "error" && <ErrorBanner message={load.error} />}
      {load.state === "ready" && (
        <ScheduleEditor
          state={load.data}
          loadError={load.error}
          reload={reload}
        />
      )}
    </section>
  );
}

function resetScheduleEnabled(state: ResetScheduleState): boolean {
  return (
    state.job !== null &&
    state.job.state !== "paused" &&
    state.job.state !== "completed"
  );
}

function ScheduleEditor({
  state,
  loadError,
  reload,
}: {
  state: ResetScheduleState;
  loadError: string | null;
  reload: () => void;
}) {
  const { t } = useTranslation("accounts");
  const client = useApiClient();
  const load = { state: "ready", data: state, error: loadError } as const;
  const [editing, setEditing] = useState(state);
  const [time, setTime] = useState(state.job?.spec.schedule.time ?? "");
  const [timezone, setTimezone] = useState(
    state.job?.spec.schedule.timezone ?? "",
  );
  const [enabled, setEnabled] = useState(resetScheduleEnabled(state));
  const [mode, setMode] = useState<ResetTermination["mode"]>(
    state.job?.spec.termination.mode ?? "forever",
  );
  const [count, setCount] = useState(
    state.job?.spec.termination.maxRuns?.toString() ?? "",
  );
  const [until, setUntil] = useState(
    state.job?.spec.termination.until?.replace(/Z$/, "") ?? "",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const beginEdit = useCallback((state: ResetScheduleState) => {
    setEditing(state);
    setTime(state.job?.spec.schedule.time ?? "");
    setTimezone(state.job?.spec.schedule.timezone ?? "");
    setEnabled(resetScheduleEnabled(state));
    const term = state.job?.spec.termination;
    setMode(term?.mode ?? "forever");
    setCount(term?.maxRuns?.toString() ?? "");
    setUntil(
      term?.mode === "until" && term.until ? term.until.replace(/Z$/, "") : "",
    );
  }, []);
  const save = async () => {
    if (!editing) return;
    setBusy(true);
    setError(null);
    try {
      const termination: ResetTermination = { mode };
      if (mode === "count") {
        if (
          !/^[1-9][0-9]*$/.test(count) ||
          !Number.isSafeInteger(Number(count))
        )
          throw new Error(t("pnlReset.invalidCount"));
        termination.maxRuns = Number(count);
      }
      if (mode === "until") {
        const utc = until + "Z";
        if (
          !until ||
          !Number.isFinite(Date.parse(utc)) ||
          new Date(utc).toISOString().slice(0, 16) !== until.slice(0, 16)
        )
          throw new Error(t("pnlReset.invalidUntil"));
        termination.until = new Date(utc).toISOString();
      }
      if (!timezone) throw new Error(t("pnlReset.invalidTimezone"));
      if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(time))
        throw new Error(t("pnlReset.invalidTime"));
      const saved = await saveResetSchedule(client, {
        enabled,
        time,
        timezone,
        termination,
        revision: editing.job?.revision ?? 0,
      });
      beginEdit(saved);
      reload();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const recover = async (action: "retry" | "skip") => {
    if (load.state !== "ready" || !load.data.job) return;
    setBusy(true);
    setError(null);
    try {
      const recovered = await recoverResetSchedule(
        client,
        load.data.job.revision,
        action,
      );
      beginEdit(recovered);
      reload();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const job = load.state === "ready" ? load.data.job : null;
  const recovery = job?.state === "failed" || job?.state === "interrupted";
  const fieldsDisabled =
    busy ||
    !enabled ||
    recovery ||
    job?.state === "running" ||
    !!(load.state === "ready" && load.data.runtimeError);
  const timezones = [
    ...new Set([
      "UTC",
      ...Intl.supportedValuesOf("timeZone"),
      ...(timezone ? [timezone] : []),
    ]),
  ];
  return (
    <>
      {load.state === "ready" && (
        <>
          {load.error && <ErrorBanner message={load.error} />}
          {load.data.runtimeError && (
            <ErrorBanner message={load.data.runtimeError} />
          )}
          <div className="flex flex-col gap-y-2 text-sm">
            <p>
              {t("pnlReset.status")}:{" "}
              {job ? t("pnlReset.states." + job.state) : t("pnlReset.absent")}
            </p>
            {job && (
              <>
                <p>
                  {t("pnlReset.completedRuns")}: {job.completedRuns}
                </p>
                {job.state !== "completed" && (
                  <p>
                    {t("pnlReset.next")}: {formatDateTime(job.next.at)}
                  </p>
                )}
              </>
            )}
            {job?.last && (
              <>
                <p>
                  {t("pnlReset.last")}: {formatDateTime(job.last.finishedAt)} ·{" "}
                  {t("pnlReset.states." + job.last.outcome)}
                </p>
                {job.last.reason && (
                  <p>
                    {job.last.reason === "missed_boundary"
                      ? t("pnlReset.missedBoundary")
                      : job.last.reason}
                  </p>
                )}
                {job.last.result?.accounts.map((result) => (
                  <p key={result.account}>
                    {result.account}:{" "}
                    {result.error || t("pnlReset.accountSucceeded")}
                  </p>
                ))}
              </>
            )}
            {(job?.error || job?.last?.error) && (
              <ErrorBanner message={job.error || job.last?.error || ""} />
            )}
          </div>
          {editing && !recovery ? (
            <div className="flex max-w-xl flex-col gap-y-4">
              <label className="flex items-center gap-2 text-sm">
                <Input
                  type="checkbox"
                  checked={enabled}
                  disabled={
                    busy || job?.state === "running" || !!load.data.runtimeError
                  }
                  onChange={(event) => setEnabled(event.target.checked)}
                />
                {t("pnlReset.enableDaily")}
              </label>
              <fieldset
                disabled={fieldsDisabled}
                className="flex flex-col gap-y-4"
              >
                <div className="flex flex-col gap-y-2">
                  <Label htmlFor="pnl-reset-zone">
                    {t("pnlReset.timezone")}
                  </Label>
                  <SearchableSelect
                    id="pnl-reset-zone"
                    value={timezone}
                    onChange={setTimezone}
                    options={timezones}
                    disabled={fieldsDisabled}
                    placeholder={t("pnlReset.invalidTimezone")}
                    searchLabel={t("common:filters.searchField", {
                      field: t("pnlReset.timezone"),
                    })}
                    emptyLabel={t("common:exactLookup.notFoundTitle", {
                      entity: t("pnlReset.timezone"),
                    })}
                  />
                </div>
                <div className="flex flex-col gap-y-2">
                  <Label htmlFor="pnl-reset-time">{t("pnlReset.time")}</Label>
                  <Input
                    id="pnl-reset-time"
                    type="time"
                    value={time}
                    onChange={(event) => setTime(event.target.value)}
                    disabled={fieldsDisabled}
                  />
                </div>
                <div className="flex flex-col gap-y-2">
                  <Label htmlFor="pnl-reset-end">{t("pnlReset.end")}</Label>
                  <Select
                    value={mode}
                    onValueChange={(value) => {
                      if (
                        value === "forever" ||
                        value === "count" ||
                        value === "until"
                      )
                        setMode(value);
                    }}
                    disabled={fieldsDisabled}
                  >
                    <SelectTrigger id="pnl-reset-end">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {(["forever", "count", "until"] as const).map((value) => (
                        <SelectItem key={value} value={value}>
                          {t("pnlReset.ends." + value)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                {mode === "count" && (
                  <div className="flex flex-col gap-y-2">
                    <Label htmlFor="pnl-reset-count">
                      {t("pnlReset.count")}
                    </Label>
                    <Input
                      id="pnl-reset-count"
                      type="number"
                      min={1}
                      step={1}
                      value={count}
                      onChange={(event) => setCount(event.target.value)}
                      disabled={fieldsDisabled}
                    />
                  </div>
                )}
                {mode === "until" && (
                  <div className="flex flex-col gap-y-2">
                    <Label htmlFor="pnl-reset-until">
                      {t("pnlReset.until")}
                    </Label>
                    <DateTimeField
                      id="pnl-reset-until"
                      value={until}
                      onChange={setUntil}
                      disabled={fieldsDisabled}
                    />
                  </div>
                )}
              </fieldset>
            </div>
          ) : null}
        </>
      )}
      {error && <ErrorBanner message={error} />}
      <div className="flex gap-2">
        {load.state === "ready" &&
          (recovery ? (
            <>
              <Button
                variant="outline"
                disabled={busy}
                onClick={() => void recover("skip")}
              >
                {t("pnlReset.skip")}
              </Button>
              <Button disabled={busy} onClick={() => void recover("retry")}>
                {t("pnlReset.retry")}
              </Button>
            </>
          ) : (
            <Button
              disabled={
                busy ||
                !editing ||
                job?.state === "running" ||
                !!load.data.runtimeError
              }
              onClick={() => void save()}
            >
              {t("common:actions.save")}
            </Button>
          ))}
      </div>
    </>
  );
}
