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
  useDeferredValue,
  useEffect,
  useMemo,
  useState,
} from "react";
import {
  ChevronDown,
  ChevronRight,
  Coins,
  Copy,
  Download,
  Plus,
  SlidersHorizontal,
  X,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import { ApiError, useOfficerApi } from "@/framework";
import { formatDate, formatTime } from "@/i18n/format";
import type {
  Adjustment,
  AdjustmentMode,
  Balance,
  BoundsPair,
  Source,
} from "@/api/types";
import { useAccounts } from "@/api/useAccounts";
import { useAdjustments } from "@/api/useAdjustments";
import { useBalances } from "@/api/useBalances";
import { Autocomplete } from "@/components/Autocomplete";
import {
  EmptyState,
  ErrorBanner,
  ErrorState,
  TableSkeleton,
} from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
import {
  CsvTransferMenu,
  PageSizeSelect,
  TablePagination,
} from "@/components/TableControls";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { NumberStepper } from "@/components/ui/number-stepper";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  hasNextPage,
  knownPageCount,
  pageFetchLimit,
  slicePage,
} from "@/lib/tablePagination";
import { usePersistentPageSize } from "@/lib/tablePageSize";
import { cn } from "@/lib/utils";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function errMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

/** Split a localized timestamp so the date and time are rendered as
 *  separate unbreakable units that wrap as a whole, never split mid-value. */
function SplitTime({ iso }: { iso: string }) {
  if (!iso) {
    return <span className="text-muted-lt">—</span>;
  }
  let date: string;
  let time: string;
  try {
    date = formatDate(iso);
    time = formatTime(iso);
  } catch {
    return <span>{iso}</span>;
  }
  return (
    <span>
      <span className="inline-block whitespace-nowrap">{date}</span>{" "}
      <span className="inline-block whitespace-nowrap">{time}</span>
    </span>
  );
}

function dash(v: string | undefined): string {
  return v && v !== "" ? v : "—";
}

function csvCell(value: string | number | undefined): string {
  const text = value === undefined ? "" : String(value);
  if (/[",\n\r]/.test(text)) {
    return `"${text.replaceAll("\"", "\"\"")}"`;
  }
  return text;
}

function downloadCsv(filename: string, rows: string[][]): void {
  const body = rows.map((row) => row.map(csvCell).join(",")).join("\n");
  const blob = new Blob([body, "\n"], { type: "text/csv;charset=utf-8" });
  const href = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = href;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(href);
}

function adjustmentCsvRow(adj: Adjustment): string[] {
  return [
    adj.externalId,
    adj.at,
    adj.account,
    adj.asset,
    adj.source,
    adj.status,
    adj.request.balance?.mode ?? "",
    adj.request.balance?.value ?? "",
    adj.request.held?.mode ?? "",
    adj.request.held?.value ?? "",
    adj.request.incoming?.mode ?? "",
    adj.request.incoming?.value ?? "",
    adj.request.averageEntryPrice ?? "",
    adj.accepted?.balanceResult ?? "",
    adj.accepted?.heldResult ?? "",
    adj.accepted?.incomingResult ?? "",
    adj.rejected?.code ?? "",
    adj.rejected?.reason ?? "",
    adj.rejected?.details ?? "",
  ];
}

function pnlClass(v: string | undefined): string {
  const value = (v ?? "").trim();
  if (value === "" || /^0+(\.0+)?$/.test(value)) {
    return "text-[var(--pnl-flat)]";
  }
  if (value.startsWith("-") || value.startsWith("−")) {
    return "text-[var(--pnl-neg)]";
  }
  return "text-[var(--pnl-pos)]";
}

interface ParsedDecimal {
  units: bigint;
  scale: number;
}

function parseDecimal(value: string): ParsedDecimal | null {
  const trimmed = value.trim();
  const match = /^([+-]?)(\d+)(?:\.(\d+))?$/.exec(trimmed);
  if (!match) {
    return null;
  }
  const sign = match[1] === "-" ? -1n : 1n;
  const integer = match[2];
  const fraction = match[3] ?? "";
  return {
    units: sign * BigInt(`${integer}${fraction}`),
    scale: fraction.length,
  };
}

function pow10(exponent: number): bigint {
  let result = 1n;
  for (let i = 0; i < exponent; i += 1) {
    result *= 10n;
  }
  return result;
}

function alignDecimal(value: ParsedDecimal, scale: number): bigint {
  return value.units * pow10(scale - value.scale);
}

function formatScaledDecimal(units: bigint, scale: number): string {
  const sign = units < 0n ? "-" : "";
  const abs = units < 0n ? -units : units;
  if (scale === 0) {
    return `${sign}${abs.toString()}`;
  }
  const padded = abs.toString().padStart(scale + 1, "0");
  const whole = padded.slice(0, -scale);
  const fraction = padded.slice(-scale);
  return `${sign}${whole}.${fraction}`;
}

function addDecimalStrings(left: string, right: string): string | null {
  const parsedLeft = parseDecimal(left);
  const parsedRight = parseDecimal(right);
  if (!parsedLeft || !parsedRight) {
    return null;
  }
  const scale = Math.max(parsedLeft.scale, parsedRight.scale);
  return formatScaledDecimal(
    alignDecimal(parsedLeft, scale) + alignDecimal(parsedRight, scale),
    scale,
  );
}

function isDecimal(value: string): boolean {
  return parseDecimal(value) !== null;
}

function hasValue(value: string | undefined): boolean {
  return (value ?? "").trim() !== "";
}

// ---------------------------------------------------------------------------
// Balances table — adjustment panel
// ---------------------------------------------------------------------------

const BALANCE_TABLE_COLS = 9;
const DRAFT_ADJUSTMENT_KEY = "__draft__";

interface AmountDraftState {
  mode: AdjustmentMode;
  value: string;
}

interface BoundsDraftState {
  lower: string;
  upper: string;
}

function emptyAmountDraft(): AmountDraftState {
  return { mode: "absolute", value: "" };
}

function emptyBoundsDraft(): BoundsDraftState {
  return { lower: "", upper: "" };
}

function amountResult(
  current: string | undefined,
  field: AmountDraftState,
): string | null {
  const value = field.value.trim();
  if (!value || !isDecimal(value)) {
    return null;
  }
  if (field.mode === "absolute") {
    return value;
  }
  return addDecimalStrings(hasValue(current) ? current ?? "0" : "0", value);
}

function boundsHaveValue(bounds: BoundsDraftState): boolean {
  return hasValue(bounds.lower) || hasValue(bounds.upper);
}

function boundsAreValid(bounds: BoundsDraftState): boolean {
  return (
    (!hasValue(bounds.lower) || isDecimal(bounds.lower)) &&
    (!hasValue(bounds.upper) || isDecimal(bounds.upper))
  );
}

function buildBoundsDraft(bounds: BoundsDraftState): BoundsPair | undefined {
  const pair: BoundsPair = {};
  if (hasValue(bounds.lower)) {
    pair.lower = bounds.lower.trim();
  }
  if (hasValue(bounds.upper)) {
    pair.upper = bounds.upper.trim();
  }
  return pair.lower || pair.upper ? pair : undefined;
}

function AveragePriceIntentRow({
  current,
  value,
  valid,
  disabled,
  onChange,
  onSubmit,
}: {
  current: string | undefined;
  value: string;
  valid: boolean;
  disabled: boolean;
  onChange: (next: string) => void;
  onSubmit: () => void;
}) {
  const { t } = useTranslation("positions");
  const invalid = hasValue(value) && !valid;
  const result = hasValue(value) ? value.trim() : null;
  const label = t("balances.columns.avgEntryPrice");

  return (
    <div className="grid gap-2 border-t border-border px-3 py-3 md:grid-cols-[minmax(8rem,1fr)_8rem_minmax(10rem,1.2fr)_minmax(8rem,1fr)] md:items-end">
      <div>
        <p className="text-[0.625rem] font-bold uppercase tracking-[0.07em] text-muted">
          {label}
        </p>
        <p className="nums mt-1 text-sm text-text">{dash(current)}</p>
      </div>
      <div className="space-y-1">
        <p className="text-[0.625rem] font-bold uppercase tracking-[0.07em] text-muted md:hidden">
          {t("panel.columns.intent")}
        </p>
        <div
          className="flex h-8 items-center rounded-card border border-border bg-surface px-3 text-xs text-muted-lt"
          aria-label={t("panel.modeAriaLabel", { field: label })}
        >
          {t("dialog.mode.absolute")}
        </div>
      </div>
      <div className="space-y-1">
        <Label htmlFor="adjust-average-entry-price" className="md:hidden">
          {t("panel.columns.amount")}
        </Label>
        <NumberStepper
          id="adjust-average-entry-price"
          value={value}
          spellCheck={false}
          placeholder={t("panel.noChange")}
          inputClassName="h-8 text-right text-xs"
          disabled={disabled}
          aria-label={t("dialog.fields.avgEntryPrice")}
          onChange={onChange}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              onSubmit();
            }
          }}
        />
      </div>
      <div>
        <p className="text-[0.625rem] font-bold uppercase tracking-[0.07em] text-muted md:hidden">
          {t("panel.columns.result")}
        </p>
        <p
          className={cn(
            "nums mt-1 text-sm",
            invalid
              ? "text-[var(--danger)]"
              : result
                ? "text-text"
                : "text-muted-lt",
          )}
        >
          {invalid
            ? t("panel.invalidDecimal")
            : result
              ? result
              : t("panel.noChange")}
        </p>
      </div>
    </div>
  );
}

function AmountIntentRow({
  id,
  label,
  current,
  field,
  disabled,
  onChange,
  onSubmit,
}: {
  id: string;
  label: string;
  current: string | undefined;
  field: AmountDraftState;
  disabled: boolean;
  onChange: (next: AmountDraftState) => void;
  onSubmit: () => void;
}) {
  const { t } = useTranslation("positions");
  const result = amountResult(current, field);
  const invalid = hasValue(field.value) && result === null;

  return (
    <div className="grid gap-2 border-t border-border px-3 py-3 md:grid-cols-[minmax(8rem,1fr)_8rem_minmax(10rem,1.2fr)_minmax(8rem,1fr)] md:items-end">
      <div>
        <p className="text-[0.625rem] font-bold uppercase tracking-[0.07em] text-muted">
          {label}
        </p>
        <p className="nums mt-1 text-sm text-text">{dash(current)}</p>
      </div>
      <div className="space-y-1">
        <Label htmlFor={`adjust-${id}-mode`} className="md:hidden">
          {t("panel.columns.intent")}
        </Label>
        <Select
          value={field.mode}
          onValueChange={(v) =>
            onChange({ ...field, mode: v as AdjustmentMode })
          }
          disabled={disabled}
        >
          <SelectTrigger
            id={`adjust-${id}-mode`}
            className="h-8 text-xs"
            aria-label={t("panel.modeAriaLabel", { field: label })}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="absolute">{t("dialog.mode.absolute")}</SelectItem>
            <SelectItem value="delta">{t("dialog.mode.delta")}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div className="space-y-1">
        <Label htmlFor={`adjust-${id}-value`} className="md:hidden">
          {t("panel.columns.amount")}
        </Label>
        <NumberStepper
          id={`adjust-${id}-value`}
          value={field.value}
          min={null}
          spellCheck={false}
          placeholder={t("panel.noChange")}
          inputClassName="h-8 text-right text-xs"
          disabled={disabled}
          aria-label={t("panel.amountAriaLabel", { field: label })}
          onChange={(value) => onChange({ ...field, value })}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              onSubmit();
            }
          }}
        />
      </div>
      <div>
        <p className="text-[0.625rem] font-bold uppercase tracking-[0.07em] text-muted md:hidden">
          {t("panel.columns.result")}
        </p>
        <p
          className={cn(
            "nums mt-1 text-sm",
            invalid
              ? "text-[var(--danger)]"
              : result
                ? "text-text"
                : "text-muted-lt",
          )}
        >
          {invalid
            ? t("panel.invalidDecimal")
            : result
              ? result
              : t("panel.noChange")}
        </p>
      </div>
    </div>
  );
}

function BoundsIntentRow({
  id,
  label,
  field,
  disabled,
  onChange,
  onSubmit,
}: {
  id: string;
  label: string;
  field: BoundsDraftState;
  disabled: boolean;
  onChange: (next: BoundsDraftState) => void;
  onSubmit: () => void;
}) {
  const { t } = useTranslation("positions");
  const invalid = boundsHaveValue(field) && !boundsAreValid(field);

  return (
    <div className="grid gap-2 border-t border-border px-3 py-3 md:grid-cols-[minmax(8rem,1fr)_minmax(8rem,1fr)_minmax(8rem,1fr)] md:items-end">
      <div>
        <p className="text-[0.625rem] font-bold uppercase tracking-[0.07em] text-muted">
          {label}
        </p>
      </div>
      <div className="space-y-1">
        <Label htmlFor={`adjust-${id}-lower`}>
          {t("dialog.bounds.lower")}
        </Label>
        <NumberStepper
          id={`adjust-${id}-lower`}
          value={field.lower}
          min={null}
          spellCheck={false}
          placeholder={t("panel.noChange")}
          inputClassName="h-8 text-right text-xs"
          disabled={disabled}
          onChange={(lower) => onChange({ ...field, lower })}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              onSubmit();
            }
          }}
        />
      </div>
      <div className="space-y-1">
        <Label htmlFor={`adjust-${id}-upper`}>
          {t("dialog.bounds.upper")}
        </Label>
        <NumberStepper
          id={`adjust-${id}-upper`}
          value={field.upper}
          min={null}
          spellCheck={false}
          placeholder={t("panel.noChange")}
          inputClassName="h-8 text-right text-xs"
          disabled={disabled}
          onChange={(upper) => onChange({ ...field, upper })}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              onSubmit();
            }
          }}
        />
        {invalid && (
          <p className="text-[0.6875rem] text-[var(--danger)]">
            {t("panel.invalidDecimal")}
          </p>
        )}
      </div>
    </div>
  );
}

function AdjustmentPanel({
  balance,
  initialAccount,
  initialAsset,
  accountSuggestions,
  assetSuggestions,
  lockIdentity,
  onDone,
  onClose,
}: {
  balance?: Balance;
  initialAccount: string;
  initialAsset: string;
  accountSuggestions: string[];
  assetSuggestions: string[];
  lockIdentity: boolean;
  onDone: () => void;
  onClose: () => void;
}) {
  const { t } = useTranslation("positions");
  const { t: tc } = useTranslation();
  const { createAdjustment } = useOfficerApi();
  const [account, setAccount] = useState(initialAccount);
  const [asset, setAsset] = useState(initialAsset);
  const [available, setAvailable] =
    useState<AmountDraftState>(emptyAmountDraft);
  const [held, setHeld] = useState<AmountDraftState>(emptyAmountDraft);
  const [incoming, setIncoming] = useState<AmountDraftState>(emptyAmountDraft);
  const [avgPrice, setAvgPrice] = useState("");
  const [balanceBounds, setBalanceBounds] =
    useState<BoundsDraftState>(emptyBoundsDraft);
  const [heldBounds, setHeldBounds] =
    useState<BoundsDraftState>(emptyBoundsDraft);
  const [incomingBounds, setIncomingBounds] =
    useState<BoundsDraftState>(emptyBoundsDraft);
  const [boundsOpen, setBoundsOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<Adjustment | null>(null);

  const trimAccount = account.trim();
  const trimAsset = asset.trim();
  const amountFields = [
    { current: balance?.available, field: available },
    { current: balance?.held, field: held },
    { current: balance?.incoming, field: incoming },
  ];
  const hasAmountChange = amountFields.some(({ field }) =>
    hasValue(field.value),
  );
  const amountFieldsValid = amountFields.every(({ current, field }) => {
    if (!hasValue(field.value)) {
      return true;
    }
    return amountResult(current, field) !== null;
  });
  const avgPriceValid = !hasValue(avgPrice) || isDecimal(avgPrice);
  const allBoundsValid =
    boundsAreValid(balanceBounds) &&
    boundsAreValid(heldBounds) &&
    boundsAreValid(incomingBounds);
  const hasBoundsChange =
    boundsHaveValue(balanceBounds) ||
    boundsHaveValue(heldBounds) ||
    boundsHaveValue(incomingBounds);
  const boundsChangeCount = [
    balanceBounds,
    heldBounds,
    incomingBounds,
  ].filter(boundsHaveValue).length;
  const hasChanges =
    hasAmountChange || hasValue(avgPrice) || hasBoundsChange;
  const canSubmit =
    trimAccount !== "" &&
    trimAsset !== "" &&
    hasChanges &&
    amountFieldsValid &&
    avgPriceValid &&
    allBoundsValid &&
    !busy &&
    outcome === null;

  const submit = async () => {
    if (!trimAccount) {
      setError(t("dialog.error.accountRequired"));
      return;
    }
    if (!trimAsset) {
      setError(t("dialog.error.assetRequired"));
      return;
    }
    if (!hasChanges) {
      setError(t("panel.noChangesError"));
      return;
    }
    if (!amountFieldsValid || !avgPriceValid || !allBoundsValid) {
      setError(t("panel.invalidDecimal"));
      return;
    }
    setBusy(true);
    setError(null);
    setOutcome(null);
    try {
      const body: Parameters<typeof createAdjustment>[1] = {
        asset: trimAsset,
      };
      if (hasValue(available.value)) {
        body.balance = {
          mode: available.mode,
          value: available.value.trim(),
        };
      }
      if (hasValue(held.value)) {
        body.held = { mode: held.mode, value: held.value.trim() };
      }
      if (hasValue(incoming.value)) {
        body.incoming = {
          mode: incoming.mode,
          value: incoming.value.trim(),
        };
      }
      if (hasValue(avgPrice)) {
        body.averageEntryPrice = avgPrice.trim();
      }
      const bb = buildBoundsDraft(balanceBounds);
      if (bb) {
        body.balanceBounds = bb;
      }
      const hb = buildBoundsDraft(heldBounds);
      if (hb) {
        body.heldBounds = hb;
      }
      const ib = buildBoundsDraft(incomingBounds);
      if (ib) {
        body.incomingBounds = ib;
      }
      const result = await createAdjustment(trimAccount, body);
      setOutcome(result);
      onDone();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const disabled = busy || outcome !== null;

  return (
    <div
      role="region"
      aria-label={t("panel.title")}
      className="space-y-4 border-l-2 border-l-accent bg-surface-2 px-4 py-4"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <SlidersHorizontal className="h-3.5 w-3.5 text-accent" />
            <p className="text-sm font-bold text-text">
              {t("panel.title")}
            </p>
            <Badge variant="neutral">
              {trimAccount || t("panel.emptyAccount")}
            </Badge>
            <Badge variant="neutral">
              {trimAsset || t("panel.emptyAsset")}
            </Badge>
          </div>
        </div>
        <Button
          variant="ghost"
          size="sm"
          onClick={onClose}
          aria-label={tc("actions.close")}
        >
          <X className="h-3.5 w-3.5" />
        </Button>
      </div>

      {lockIdentity ? null : (
        <div className="grid gap-3 md:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="adjust-panel-account">
              {t("dialog.fields.account")}
            </Label>
            <Autocomplete
              id="adjust-panel-account"
              value={account}
              spellCheck={false}
              placeholder="acc-1"
              suggestions={accountSuggestions}
              disabled={disabled}
              onChange={setAccount}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="adjust-panel-asset">
              {t("dialog.fields.asset")}
            </Label>
            <Autocomplete
              id="adjust-panel-asset"
              value={asset}
              spellCheck={false}
              placeholder="AAPL"
              suggestions={assetSuggestions}
              disabled={disabled}
              onChange={setAsset}
            />
          </div>
        </div>
      )}

      <div className="border border-border bg-bg">
        <div className="grid grid-cols-[minmax(8rem,1fr)_8rem_minmax(10rem,1.2fr)_minmax(8rem,1fr)] gap-2 px-3 py-2 text-[0.625rem] font-bold uppercase tracking-[0.07em] text-muted max-md:hidden">
          <span>{t("panel.columns.current")}</span>
          <span>{t("panel.columns.intent")}</span>
          <span>{t("panel.columns.amount")}</span>
          <span>{t("panel.columns.result")}</span>
        </div>
        <AmountIntentRow
          id="available"
          label={t("balances.columns.available")}
          current={balance?.available}
          field={available}
          disabled={disabled}
          onChange={setAvailable}
          onSubmit={() => void submit()}
        />
        <AmountIntentRow
          id="held"
          label={t("balances.columns.held")}
          current={balance?.held}
          field={held}
          disabled={disabled}
          onChange={setHeld}
          onSubmit={() => void submit()}
        />
        <AmountIntentRow
          id="incoming"
          label={t("balances.columns.incoming")}
          current={balance?.incoming}
          field={incoming}
          disabled={disabled}
          onChange={setIncoming}
          onSubmit={() => void submit()}
        />
        <AveragePriceIntentRow
          current={balance?.averageEntryPrice}
          value={avgPrice}
          valid={avgPriceValid}
          disabled={disabled}
          onChange={setAvgPrice}
          onSubmit={() => void submit()}
        />
      </div>

      <div className="border border-border bg-bg">
        <button
          type="button"
          className="flex w-full items-center justify-between gap-3 px-3 py-2 text-left transition-colors hover:bg-surface-hover"
          aria-expanded={boundsOpen}
          aria-controls="adjust-bounds-panel"
          onClick={() => setBoundsOpen((open) => !open)}
        >
          <span className="flex items-center gap-2">
            {boundsOpen ? (
              <ChevronDown className="h-3.5 w-3.5 text-muted-lt" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5 text-muted-lt" />
            )}
            <span className="text-[0.625rem] font-bold uppercase tracking-[0.07em] text-muted">
              {t("dialog.bounds.sectionLabel")}
            </span>
          </span>
          <Badge
            variant={
              allBoundsValid
                ? boundsChangeCount > 0
                  ? "accent"
                  : "neutral"
                : "danger"
            }
          >
            {allBoundsValid
              ? boundsChangeCount > 0
                ? `${boundsChangeCount}/3`
                : t("panel.noChange")
              : t("panel.invalidDecimal")}
          </Badge>
        </button>
        {boundsOpen && (
          <div id="adjust-bounds-panel">
            <BoundsIntentRow
              id="balance-bounds"
              label={t("dialog.bounds.balanceLabel")}
              field={balanceBounds}
              disabled={disabled}
              onChange={setBalanceBounds}
              onSubmit={() => void submit()}
            />
            <BoundsIntentRow
              id="held-bounds"
              label={t("dialog.bounds.heldLabel")}
              field={heldBounds}
              disabled={disabled}
              onChange={setHeldBounds}
              onSubmit={() => void submit()}
            />
            <BoundsIntentRow
              id="incoming-bounds"
              label={t("dialog.bounds.incomingLabel")}
              field={incomingBounds}
              disabled={disabled}
              onChange={setIncomingBounds}
              onSubmit={() => void submit()}
            />
          </div>
        )}
      </div>

      {outcome && <AdjustOutcomeView adjustment={outcome} />}
      {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}

      <div className="flex justify-end gap-2 border-t border-border pt-3">
        <Button variant="outline" size="sm" onClick={onClose} disabled={busy}>
          {outcome ? t("dialog.footer.close") : t("actions.cancel", { ns: "common" })}
        </Button>
        {!outcome && (
          <Button size="sm" onClick={() => void submit()} disabled={!canSubmit}>
            {busy ? t("panel.saving") : t("panel.submit")}
          </Button>
        )}
      </div>
    </div>
  );
}

function BalanceEditRow({
  balance,
  expanded,
  onToggle,
  onClose,
  accountSuggestions,
  assetSuggestions,
  onApplied,
}: {
  balance: Balance;
  expanded: boolean;
  onToggle: () => void;
  onClose: () => void;
  accountSuggestions: string[];
  assetSuggestions: string[];
  onApplied: () => void;
}) {
  const { t } = useTranslation("positions");
  const b = balance;
  return (
    <>
      <TableRow className={cn("hover:bg-transparent", expanded && "bg-accent-dim")}>
        <TableCell className="nums text-xs">{b.account}</TableCell>
        <TableCell className="nums text-xs">{b.asset}</TableCell>
        <TableCell className="nums text-right text-xs">{b.available}</TableCell>
        <TableCell className="nums text-right text-xs">{b.held}</TableCell>
        <TableCell className="nums text-right text-xs">{b.incoming}</TableCell>
        <TableCell className="nums text-right text-xs">
          {dash(b.averageEntryPrice)}
        </TableCell>
        <TableCell
          className={cn("nums text-right text-xs", pnlClass(b.realizedPnl))}
        >
          {dash(b.realizedPnl)}
        </TableCell>
        <TableCell className="text-xs text-muted-lt">
          <SplitTime iso={b.updatedAt} />
        </TableCell>
        <TableCell className="text-right">
          <Button
            variant="outline"
            size="sm"
            className={cn(expanded && "border-accent bg-accent-dim text-accent")}
            onClick={onToggle}
            aria-expanded={expanded}
            aria-label={t("panel.openAriaLabel", {
              account: b.account,
              asset: b.asset,
            })}
          >
            <SlidersHorizontal className="h-3.5 w-3.5" />
            {t("panel.open")}
          </Button>
        </TableCell>
      </TableRow>
      {expanded && (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={BALANCE_TABLE_COLS} className="p-0">
            <AdjustmentPanel
              balance={b}
              initialAccount={b.account}
              initialAsset={b.asset}
              accountSuggestions={accountSuggestions}
              assetSuggestions={assetSuggestions}
              lockIdentity
              onDone={onApplied}
              onClose={onClose}
            />
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

/** A draft row to seed a balance for an account/asset not yet listed. */
function BalanceDraftRow({
  defaultAccount,
  defaultAsset,
  expanded,
  requestId,
  accountSuggestions,
  assetSuggestions,
  onToggle,
  onClose,
  onApplied,
}: {
  defaultAccount: string;
  defaultAsset: string;
  expanded: boolean;
  requestId: number;
  accountSuggestions: string[];
  assetSuggestions: string[];
  onToggle: () => void;
  onClose: () => void;
  onApplied: () => void;
}) {
  const { t } = useTranslation("positions");

  return (
    <>
      <TableRow className={cn("hover:bg-transparent", expanded && "bg-accent-dim")}>
        <TableCell className="nums text-xs text-muted-lt">
          {defaultAccount || t("panel.emptyAccount")}
        </TableCell>
        <TableCell className="nums text-xs text-muted-lt">
          {defaultAsset || t("panel.emptyAsset")}
        </TableCell>
        <TableCell className="text-right text-xs text-muted-lt" colSpan={6}>
          {t("inline.newRowHint")}
        </TableCell>
        <TableCell className="text-right">
          <Button
            variant="outline"
            size="sm"
            className={cn(expanded && "border-accent bg-accent-dim text-accent")}
            onClick={onToggle}
            aria-expanded={expanded}
            aria-label={t("panel.openDraftAriaLabel")}
          >
            <SlidersHorizontal className="h-3.5 w-3.5" />
            {t("panel.open")}
          </Button>
        </TableCell>
      </TableRow>
      {expanded && (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={BALANCE_TABLE_COLS} className="p-0">
            <AdjustmentPanel
              key={requestId}
              initialAccount={defaultAccount}
              initialAsset={defaultAsset}
              accountSuggestions={accountSuggestions}
              assetSuggestions={assetSuggestions}
              lockIdentity={false}
              onDone={onApplied}
              onClose={onClose}
            />
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

function BalancesTable({
  balances,
  defaultDraftAccount,
  defaultDraftAsset,
  draftOpenRequest,
  accountSuggestions,
  assetSuggestions,
  onApplied,
}: {
  balances: Balance[];
  defaultDraftAccount: string;
  defaultDraftAsset: string;
  draftOpenRequest: number;
  accountSuggestions: string[];
  assetSuggestions: string[];
  onApplied: () => void;
}) {
  const { t } = useTranslation("positions");
  const [openKey, setOpenKey] = useState<string | null>(null);

  useEffect(() => {
    if (draftOpenRequest > 0) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setOpenKey(DRAFT_ADJUSTMENT_KEY);
    }
  }, [draftOpenRequest]);

  return (
    <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>{t("balances.columns.account")}</TableHead>
            <TableHead>{t("balances.columns.asset")}</TableHead>
            <TableHead className="text-right">{t("balances.columns.available")}</TableHead>
            <TableHead className="text-right">{t("balances.columns.held")}</TableHead>
            <TableHead className="text-right">{t("balances.columns.incoming")}</TableHead>
            <TableHead className="text-right">{t("balances.columns.avgEntryPrice")}</TableHead>
            <TableHead className="text-right">{t("balances.columns.realizedPnl")}</TableHead>
            <TableHead>{t("balances.columns.updated")}</TableHead>
            <TableHead className="text-right">{t("balances.columns.adjust")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {balances.map((b) => (
            <BalanceEditRow
              key={`${b.account}|${b.asset}`}
              balance={b}
              expanded={openKey === `${b.account}|${b.asset}`}
              onToggle={() =>
                setOpenKey((current) =>
                  current === `${b.account}|${b.asset}`
                    ? null
                    : `${b.account}|${b.asset}`,
                )
              }
              onClose={() => setOpenKey(null)}
              accountSuggestions={accountSuggestions}
              assetSuggestions={assetSuggestions}
              onApplied={onApplied}
            />
          ))}
          <BalanceDraftRow
            defaultAccount={defaultDraftAccount}
            defaultAsset={defaultDraftAsset}
            expanded={openKey === DRAFT_ADJUSTMENT_KEY}
            requestId={draftOpenRequest}
            accountSuggestions={accountSuggestions}
            assetSuggestions={assetSuggestions}
            onToggle={() =>
              setOpenKey((current) =>
                current === DRAFT_ADJUSTMENT_KEY
                  ? null
                  : DRAFT_ADJUSTMENT_KEY,
              )
            }
            onClose={() => setOpenKey(null)}
            onApplied={onApplied}
          />
        </TableBody>
    </Table>
  );
}

// ---------------------------------------------------------------------------
// Adjustment outcome display (used in dialog after submit)
// ---------------------------------------------------------------------------

interface AdjustOutcome {
  adjustment: Adjustment;
}

function AdjustOutcomeView({ adjustment }: AdjustOutcome) {
  const { t } = useTranslation("positions");
  const { accepted, rejected } = adjustment;
  if (rejected) {
    return (
      <div className="rounded-card border border-[var(--danger)] bg-[var(--danger-dim)] p-3 text-xs text-[var(--danger)]">
        <p className="font-medium">{t("dialog.outcome.rejectedTitle")}</p>
        <p className="mt-1 text-[var(--danger)]">{rejected.reason}</p>
      </div>
    );
  }
  if (accepted) {
    const rows: { label: string; delta: string; result: string }[] = [];
    if (accepted.balanceDelta || accepted.balanceResult) {
      rows.push({ label: t("dialog.outcome.fieldBalance"), delta: accepted.balanceDelta, result: accepted.balanceResult });
    }
    if (accepted.heldDelta || accepted.heldResult) {
      rows.push({ label: t("dialog.outcome.fieldHeld"), delta: accepted.heldDelta, result: accepted.heldResult });
    }
    if (accepted.incomingDelta || accepted.incomingResult) {
      rows.push({ label: t("dialog.outcome.fieldIncoming"), delta: accepted.incomingDelta, result: accepted.incomingResult });
    }
    return (
      <div className="rounded-card border border-[var(--ok)] bg-[var(--ok-dim)] p-3 text-xs">
        <p className="font-medium text-[var(--ok)]">{t("dialog.outcome.acceptedTitle")}</p>
        {rows.length > 0 && (
          <div className="mt-2 space-y-1">
            {rows.map((r) => (
              <div key={r.label} className="flex gap-2">
                <span className="w-28 text-muted">{r.label}</span>
                <span className="nums text-text">
                  Δ{r.delta} → {r.result}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    );
  }
  return null;
}

// ---------------------------------------------------------------------------
// Amount field row — mode toggle + value input
// ---------------------------------------------------------------------------

interface AmountFieldState {
  enabled: boolean;
  mode: AdjustmentMode;
  value: string;
}

function AmountField({
  id,
  label,
  field,
  onChange,
}: {
  id: string;
  label: string;
  field: AmountFieldState;
  onChange: (next: AmountFieldState) => void;
}) {
  const { t } = useTranslation("positions");
  return (
    <div className="space-y-1.5">
      <div className="flex items-center gap-2">
        <input
          type="checkbox"
          id={`chk-${id}`}
          checked={field.enabled}
          onChange={(e) => onChange({ ...field, enabled: e.target.checked })}
          className="h-3.5 w-3.5 cursor-pointer accent-[var(--accent)]"
        />
        <Label htmlFor={`chk-${id}`} className="cursor-pointer capitalize">
          {label}
        </Label>
      </div>
      {field.enabled && (
        <div className="ml-5 flex items-center gap-2">
          <Select
            value={field.mode}
            onValueChange={(v) =>
              onChange({ ...field, mode: v as AdjustmentMode })
            }
          >
            <SelectTrigger className="h-7 w-24 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="absolute">{t("dialog.mode.absolute")}</SelectItem>
              <SelectItem value="delta">{t("dialog.mode.delta")}</SelectItem>
            </SelectContent>
          </Select>
          <NumberStepper
            value={field.value}
            min={null}
            spellCheck={false}
            placeholder="0"
            className="flex-1"
            inputClassName="h-7 text-xs"
            onChange={(value) => onChange({ ...field, value })}
          />
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Bounds field row — optional lower / upper
// ---------------------------------------------------------------------------

interface BoundsFieldState {
  enabled: boolean;
  lower: string;
  upper: string;
}

function BoundsField({
  id,
  label,
  field,
  onChange,
}: {
  id: string;
  label: string;
  field: BoundsFieldState;
  onChange: (next: BoundsFieldState) => void;
}) {
  const { t } = useTranslation("positions");
  return (
    <div className="space-y-1.5">
      <div className="flex items-center gap-2">
        <input
          type="checkbox"
          id={`chkb-${id}`}
          checked={field.enabled}
          onChange={(e) => onChange({ ...field, enabled: e.target.checked })}
          className="h-3.5 w-3.5 cursor-pointer accent-[var(--accent)]"
        />
        <Label htmlFor={`chkb-${id}`} className="cursor-pointer capitalize">
          {label}
        </Label>
      </div>
      {field.enabled && (
        <div className="ml-5 grid grid-cols-2 gap-2">
          <div className="space-y-1">
            <Label className="text-[0.6875rem] text-muted">{t("dialog.bounds.lower")}</Label>
            <NumberStepper
              value={field.lower}
              min={null}
              spellCheck={false}
              placeholder="—"
              inputClassName="h-7 text-xs"
              onChange={(lower) => onChange({ ...field, lower })}
            />
          </div>
          <div className="space-y-1">
            <Label className="text-[0.6875rem] text-muted">{t("dialog.bounds.upper")}</Label>
            <NumberStepper
              value={field.upper}
              min={null}
              spellCheck={false}
              placeholder="—"
              inputClassName="h-7 text-xs"
              onChange={(upper) => onChange({ ...field, upper })}
            />
          </div>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Adjust dialog
// ---------------------------------------------------------------------------

function emptyAmount(): AmountFieldState {
  return { enabled: false, mode: "absolute", value: "" };
}

function emptyBounds(): BoundsFieldState {
  return { enabled: false, lower: "", upper: "" };
}

interface AdjustDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  initialAccount: string;
  initialAsset: string;
  /** Balance amount to preseed from a row (empty leaves the field disabled). */
  initialBalanceMode: AdjustmentMode;
  initialBalanceValue: string;
  /** Full-clone prefill for held, incoming, avgPrice, and bounds. */
  initialHeldMode?: AdjustmentMode;
  initialHeldValue?: string;
  initialIncomingMode?: AdjustmentMode;
  initialIncomingValue?: string;
  initialAvgPrice?: string;
  initialBalanceBoundsLower?: string;
  initialBalanceBoundsUpper?: string;
  initialHeldBoundsLower?: string;
  initialHeldBoundsUpper?: string;
  initialIncomingBoundsLower?: string;
  initialIncomingBoundsUpper?: string;
  assetSuggestions: string[];
  accountSuggestions: string[];
  onDone: () => void;
}

function AdjustDialog({
  open,
  onOpenChange,
  initialAccount,
  initialAsset,
  initialBalanceMode,
  initialBalanceValue,
  initialHeldMode,
  initialHeldValue,
  initialIncomingMode,
  initialIncomingValue,
  initialAvgPrice,
  initialBalanceBoundsLower,
  initialBalanceBoundsUpper,
  initialHeldBoundsLower,
  initialHeldBoundsUpper,
  initialIncomingBoundsLower,
  initialIncomingBoundsUpper,
  assetSuggestions,
  accountSuggestions,
  onDone,
}: AdjustDialogProps) {
  // A preseeded amount (e.g. from an inline row or clone) opens the field
  // already enabled so the operator only adjusts what they need.
  const seedAmount = (mode: AdjustmentMode | undefined, value: string | undefined): AmountFieldState =>
    value
      ? { enabled: true, mode: mode ?? "absolute", value }
      : emptyAmount();

  const seedBounds = (lower: string | undefined, upper: string | undefined): BoundsFieldState =>
    lower || upper
      ? { enabled: true, lower: lower ?? "", upper: upper ?? "" }
      : emptyBounds();

  const [account, setAccount] = useState(initialAccount);
  const [asset, setAsset] = useState(initialAsset);
  const [avgPrice, setAvgPrice] = useState(initialAvgPrice ?? "");
  const [balance, setBalance] = useState<AmountFieldState>(() => seedAmount(initialBalanceMode, initialBalanceValue));
  const [held, setHeld] = useState<AmountFieldState>(() => seedAmount(initialHeldMode, initialHeldValue));
  const [incoming, setIncoming] = useState<AmountFieldState>(() => seedAmount(initialIncomingMode, initialIncomingValue));
  const [balanceBounds, setBalanceBounds] = useState<BoundsFieldState>(() => seedBounds(initialBalanceBoundsLower, initialBalanceBoundsUpper));
  const [heldBounds, setHeldBounds] = useState<BoundsFieldState>(() => seedBounds(initialHeldBoundsLower, initialHeldBoundsUpper));
  const [incomingBounds, setIncomingBounds] = useState<BoundsFieldState>(() => seedBounds(initialIncomingBoundsLower, initialIncomingBoundsUpper));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<Adjustment | null>(null);

  // Reseed whenever the dialog opens.
  useEffect(() => {
    if (open) {
      // Reset all form fields from props each time the dialog opens.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setAccount(initialAccount);
      setAsset(initialAsset);
      setAvgPrice(initialAvgPrice ?? "");
      setBalance(seedAmount(initialBalanceMode, initialBalanceValue));
      setHeld(seedAmount(initialHeldMode, initialHeldValue));
      setIncoming(seedAmount(initialIncomingMode, initialIncomingValue));
      setBalanceBounds(seedBounds(initialBalanceBoundsLower, initialBalanceBoundsUpper));
      setHeldBounds(seedBounds(initialHeldBoundsLower, initialHeldBoundsUpper));
      setIncomingBounds(seedBounds(initialIncomingBoundsLower, initialIncomingBoundsUpper));
      setBusy(false);
      setError(null);
      setOutcome(null);
    }
  }, [
    open,
    initialAccount, initialAsset,
    initialBalanceMode, initialBalanceValue,
    initialHeldMode, initialHeldValue,
    initialIncomingMode, initialIncomingValue,
    initialAvgPrice,
    initialBalanceBoundsLower, initialBalanceBoundsUpper,
    initialHeldBoundsLower, initialHeldBoundsUpper,
    initialIncomingBoundsLower, initialIncomingBoundsUpper,
  ]);

  function buildBounds(f: BoundsFieldState): BoundsPair | undefined {
    if (!f.enabled) {
      return undefined;
    }
    const pair: BoundsPair = {};
    if (f.lower.trim()) {
      pair.lower = f.lower.trim();
    }
    if (f.upper.trim()) {
      pair.upper = f.upper.trim();
    }
    if (!pair.lower && !pair.upper) {
      return undefined;
    }
    return pair;
  }

  const { t } = useTranslation("positions");
  const { createAdjustment } = useOfficerApi();

  const submit = async () => {
    const trimAccount = account.trim();
    const trimAsset = asset.trim();
    if (!trimAccount) {
      setError(t("dialog.error.accountRequired"));
      return;
    }
    if (!trimAsset) {
      setError(t("dialog.error.assetRequired"));
      return;
    }
    setBusy(true);
    setError(null);
    setOutcome(null);
    try {
      const body: Parameters<typeof createAdjustment>[1] = { asset: trimAsset };
      if (avgPrice.trim()) {
        body.averageEntryPrice = avgPrice.trim();
      }
      if (balance.enabled && balance.value.trim()) {
        body.balance = { mode: balance.mode, value: balance.value.trim() };
      }
      if (held.enabled && held.value.trim()) {
        body.held = { mode: held.mode, value: held.value.trim() };
      }
      if (incoming.enabled && incoming.value.trim()) {
        body.incoming = { mode: incoming.mode, value: incoming.value.trim() };
      }
      const bb = buildBounds(balanceBounds);
      if (bb) {
        body.balanceBounds = bb;
      }
      const hb = buildBounds(heldBounds);
      if (hb) {
        body.heldBounds = hb;
      }
      const ib = buildBounds(incomingBounds);
      if (ib) {
        body.incomingBounds = ib;
      }
      const result = await createAdjustment(trimAccount, body);
      setOutcome(result);
      onDone();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("dialog.title")}</DialogTitle>
          <DialogDescription>
            {t("dialog.description")}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {/* Account + asset */}
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="adj-account">{t("dialog.fields.account")}</Label>
              <Autocomplete
                id="adj-account"
                value={account}
                spellCheck={false}
                placeholder="acc-1"
                suggestions={accountSuggestions}
                onChange={setAccount}
                disabled={busy || outcome !== null}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="adj-asset">{t("dialog.fields.asset")}</Label>
              <Autocomplete
                id="adj-asset"
                value={asset}
                spellCheck={false}
                placeholder="AAPL"
                suggestions={assetSuggestions}
                onChange={setAsset}
                disabled={busy || outcome !== null}
              />
            </div>
          </div>

          {/* Average entry price */}
          <div className="space-y-1.5">
            <Label htmlFor="adj-aep">{t("dialog.fields.avgEntryPrice")}</Label>
            <NumberStepper
              id="adj-aep"
              value={avgPrice}
              spellCheck={false}
              placeholder="e.g. 142.50"
              inputClassName="text-xs"
              disabled={busy || outcome !== null}
              onChange={setAvgPrice}
            />
            <p className="text-[0.6875rem] text-muted">
              {t("dialog.fields.avgEntryPriceHint")}
            </p>
          </div>

          {/* Amount fields */}
          <div className="space-y-3 rounded-card border border-border p-3">
            <p className="text-[0.6875rem] uppercase tracking-[0.07em] text-muted">
              {t("dialog.amounts.sectionLabel")}
            </p>
            <AmountField id="balance" label={t("dialog.amounts.balance")} field={balance} onChange={setBalance} />
            <AmountField id="held" label={t("dialog.amounts.held")} field={held} onChange={setHeld} />
            <AmountField id="incoming" label={t("dialog.amounts.incoming")} field={incoming} onChange={setIncoming} />
          </div>

          {/* Bounds fields */}
          <div className="space-y-3 rounded-card border border-border p-3">
            <p className="text-[0.6875rem] uppercase tracking-[0.07em] text-muted">
              {t("dialog.bounds.sectionLabel")}
            </p>
            <BoundsField
              id="balance"
              label={t("dialog.bounds.balanceLabel")}
              field={balanceBounds}
              onChange={setBalanceBounds}
            />
            <BoundsField
              id="held"
              label={t("dialog.bounds.heldLabel")}
              field={heldBounds}
              onChange={setHeldBounds}
            />
            <BoundsField
              id="incoming"
              label={t("dialog.bounds.incomingLabel")}
              field={incomingBounds}
              onChange={setIncomingBounds}
            />
          </div>

          {/* Outcome */}
          {outcome && <AdjustOutcomeView adjustment={outcome} />}

          {error && (
            <ErrorBanner message={error} onDismiss={() => setError(null)} />
          )}
        </div>

        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {outcome ? t("dialog.footer.close") : t("actions.cancel", { ns: "common" })}
          </Button>
          {!outcome && (
            <Button
              size="sm"
              onClick={() => void submit()}
              disabled={busy}
            >
              {t("dialog.footer.submit")}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Adjustment history row — inline outcome
// ---------------------------------------------------------------------------

function HistoryRowOutcome({ adj }: { adj: Adjustment }) {
  const { t } = useTranslation("positions");
  const { accepted, rejected } = adj;
  if (rejected) {
    return (
      <span className="text-[var(--danger)]">
        {rejected.reason || "—"}
      </span>
    );
  }
  if (accepted) {
    const parts: string[] = [];
    if (accepted.balanceDelta || accepted.balanceResult) {
      parts.push(
        `${t("dialog.outcome.fieldBalance")} Δ${accepted.balanceDelta} → ${accepted.balanceResult}`,
      );
    }
    if (accepted.heldDelta || accepted.heldResult) {
      parts.push(
        `${t("dialog.outcome.fieldHeld")} Δ${accepted.heldDelta} → ${accepted.heldResult}`,
      );
    }
    if (accepted.incomingDelta || accepted.incomingResult) {
      parts.push(
        `${t("dialog.outcome.fieldIncoming")} Δ${accepted.incomingDelta} → ${accepted.incomingResult}`,
      );
    }
    return (
      <span className="nums text-text">
        {parts.length > 0 ? parts.join(" · ") : "—"}
      </span>
    );
  }
  return <span className="text-muted-lt">—</span>;
}

function HistoryRow({ adj, onClone }: { adj: Adjustment; onClone: (adj: Adjustment) => void }) {
  const { t } = useTranslation("positions");
  const isRejected = !!adj.rejected;
  const isAccepted = !!adj.accepted && !isRejected;

  const req = adj.request;
  const reqParts: string[] = [];
  if (req.balance) {
    reqParts.push(
      req.balance.mode === "delta"
        ? t("history.request.balanceDelta", { value: req.balance.value })
        : t("history.request.balanceAbsolute", { value: req.balance.value }),
    );
  }
  if (req.held) {
    reqParts.push(
      req.held.mode === "delta"
        ? t("history.request.heldDelta", { value: req.held.value })
        : t("history.request.heldAbsolute", { value: req.held.value }),
    );
  }
  if (req.incoming) {
    reqParts.push(
      req.incoming.mode === "delta"
        ? t("history.request.incomingDelta", { value: req.incoming.value })
        : t("history.request.incomingAbsolute", { value: req.incoming.value }),
    );
  }
  if (req.averageEntryPrice) {
    reqParts.push(t("history.request.avgPrice", { value: req.averageEntryPrice }));
  }

  return (
    <TableRow className="hover:bg-transparent">
      <TableCell className="text-xs text-muted-lt"><SplitTime iso={adj.at} /></TableCell>
      <TableCell className="nums text-xs">{adj.account}</TableCell>
      <TableCell className="nums text-xs">{adj.asset}</TableCell>
      <TableCell>
        <Badge variant="neutral" className="text-[0.6875rem]">
          {adj.source}
        </Badge>
      </TableCell>
      <TableCell className="nums text-xs text-muted-lt">
        {reqParts.length > 0 ? reqParts.join(" · ") : "—"}
      </TableCell>
      <TableCell>
        {isRejected ? (
          <Badge variant="danger">{t("history.status.rejected")}</Badge>
        ) : isAccepted ? (
          <Badge variant="ok">{t("history.status.accepted")}</Badge>
        ) : (
          <Badge variant="neutral">{adj.status}</Badge>
        )}
      </TableCell>
      <TableCell className="text-xs">
        <HistoryRowOutcome adj={adj} />
      </TableCell>
      <TableCell>
        <Button
          variant="ghost"
          size="sm"
          aria-label={t("history.clone.ariaLabel", { id: adj.externalId })}
          onClick={() => onClone(adj)}
        >
          <Copy className="h-3.5 w-3.5" />
        </Button>
      </TableCell>
    </TableRow>
  );
}

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

const SOURCES: Source[] = ["panel", "api", "mcp", "system"];
type PositionsTab = "positions" | "history";

export function Positions() {
  const { t } = useTranslation("positions");
  const { fetchAdjustments } = useOfficerApi();
  const [searchParams] = useSearchParams();
  const initialAccount = searchParams.get("account") ?? "";

  const [tab, setTab] = useState<PositionsTab>("positions");
  const [accountFilter, setAccountFilter] = useState(initialAccount);
  const [assetFilter, setAssetFilter] = useState("");
  const [sourceFilter, setSourceFilter] = useState<Source | "__all__">("__all__");
  const [draftOpenRequest, setDraftOpenRequest] = useState(0);
  const [balancePage, setBalancePage] = useState(0);
  const [historyPage, setHistoryPage] = useState(0);
  const [balanceSize, setBalanceSize] = usePersistentPageSize(
    "pit-officer-positions-page-size",
  );
  const [historySize, setHistorySize] = usePersistentPageSize(
    "pit-officer-position-history-page-size",
  );
  const [historyExportBusy, setHistoryExportBusy] = useState(false);
  const [historyExportError, setHistoryExportError] = useState<string | null>(
    null,
  );

  const [adjustOpen, setAdjustOpen] = useState(false);
  const [adjustAccount, setAdjustAccount] = useState("");
  const [adjustAsset, setAdjustAsset] = useState("");
  const [adjustBalanceMode, setAdjustBalanceMode] =
    useState<AdjustmentMode>("absolute");
  const [adjustBalanceValue, setAdjustBalanceValue] = useState("");
  // Extended clone-prefill props (undefined = leave empty / disabled).
  const [adjustHeldMode, setAdjustHeldMode] = useState<AdjustmentMode | undefined>(undefined);
  const [adjustHeldValue, setAdjustHeldValue] = useState<string | undefined>(undefined);
  const [adjustIncomingMode, setAdjustIncomingMode] = useState<AdjustmentMode | undefined>(undefined);
  const [adjustIncomingValue, setAdjustIncomingValue] = useState<string | undefined>(undefined);
  const [adjustAvgPrice, setAdjustAvgPrice] = useState<string | undefined>(undefined);
  const [adjustBalanceBoundsLower, setAdjustBalanceBoundsLower] = useState<string | undefined>(undefined);
  const [adjustBalanceBoundsUpper, setAdjustBalanceBoundsUpper] = useState<string | undefined>(undefined);
  const [adjustHeldBoundsLower, setAdjustHeldBoundsLower] = useState<string | undefined>(undefined);
  const [adjustHeldBoundsUpper, setAdjustHeldBoundsUpper] = useState<string | undefined>(undefined);
  const [adjustIncomingBoundsLower, setAdjustIncomingBoundsLower] = useState<string | undefined>(undefined);
  const [adjustIncomingBoundsUpper, setAdjustIncomingBoundsUpper] = useState<string | undefined>(undefined);

  const deferredAccount = useDeferredValue(accountFilter.trim());
  const deferredAsset = useDeferredValue(assetFilter.trim());

  const balancesLoad = useBalances(
    deferredAccount || undefined,
    deferredAsset || undefined,
  );

  const deferredSource =
    sourceFilter === "__all__" ? undefined : sourceFilter;
  const adjustmentsLoad = useAdjustments(
    deferredAccount || undefined,
    deferredSource,
    pageFetchLimit(historyPage, historySize),
  );

  // Account suggestions from the accounts hook.
  const accountsLoad = useAccounts();
  const accountSuggestions = useMemo(() => {
    if (accountsLoad.load.state !== "ready") {
      return [];
    }
    return accountsLoad.load.data.map((a) => a.code);
  }, [accountsLoad.load]);

  // Asset suggestions: union from balances + adjustments history.
  const assetSuggestions = useMemo(() => {
    const set = new Set<string>();
    if (balancesLoad.load.state === "ready") {
      for (const b of balancesLoad.load.data) {
        if (b.asset) {
          set.add(b.asset);
        }
      }
    }
    if (adjustmentsLoad.load.state === "ready") {
      for (const a of adjustmentsLoad.load.data) {
        if (a.asset) {
          set.add(a.asset);
        }
      }
    }
    return Array.from(set).sort();
  }, [balancesLoad.load, adjustmentsLoad.load]);

  const openNewAdjust = () => {
    setDraftOpenRequest((current) => current + 1);
  };

  // Clone an adjustment record — open dialog prefilled with all request fields.
  const openCloneAdjust = (adj: Adjustment) => {
    const req = adj.request;
    setAdjustAccount(adj.account);
    setAdjustAsset(adj.asset);
    setAdjustBalanceMode(req.balance?.mode ?? "absolute");
    setAdjustBalanceValue(req.balance?.value ?? "");
    setAdjustHeldMode(req.held?.mode);
    setAdjustHeldValue(req.held?.value);
    setAdjustIncomingMode(req.incoming?.mode);
    setAdjustIncomingValue(req.incoming?.value);
    setAdjustAvgPrice(req.averageEntryPrice);
    setAdjustBalanceBoundsLower(req.balanceBounds?.lower);
    setAdjustBalanceBoundsUpper(req.balanceBounds?.upper);
    setAdjustHeldBoundsLower(req.heldBounds?.lower);
    setAdjustHeldBoundsUpper(req.heldBounds?.upper);
    setAdjustIncomingBoundsLower(req.incomingBounds?.lower);
    setAdjustIncomingBoundsUpper(req.incomingBounds?.upper);
    setAdjustOpen(true);
  };

  const handleAdjustDone = () => {
    balancesLoad.reload();
    adjustmentsLoad.reload();
  };
  const reloadPositions = () => {
    balancesLoad.reload();
    adjustmentsLoad.reload();
  };
  const reloadActive = () => {
    if (tab === "positions") {
      balancesLoad.reload();
      return;
    }
    adjustmentsLoad.reload();
  };
  const exportHistory = async () => {
    setHistoryExportBusy(true);
    setHistoryExportError(null);
    try {
      const rows = await fetchAdjustments({
        account: deferredAccount || undefined,
        source: deferredSource,
        limit: 1000,
      });
      downloadCsv("position-adjustment-history.csv", [
        [
          "external_id",
          "at",
          "account",
          "asset",
          "source",
          "status",
          "balance_mode",
          "balance_value",
          "held_mode",
          "held_value",
          "incoming_mode",
          "incoming_value",
          "average_entry_price",
          "balance_result",
          "held_result",
          "incoming_result",
          "rejected_code",
          "rejected_reason",
          "rejected_details",
        ],
        ...rows.map(adjustmentCsvRow),
      ]);
    } catch (err) {
      setHistoryExportError(errMessage(err));
    } finally {
      setHistoryExportBusy(false);
    }
  };
  const positionCsvFilters = {
    account: deferredAccount.trim() || undefined,
    asset: deferredAsset.trim() || undefined,
  };
  const balances =
    balancesLoad.load.state === "ready" ? balancesLoad.load.data : [];
  const pagedBalances = slicePage(balances, balancePage, balanceSize);
  const hasMoreBalances = hasNextPage(balances, balancePage, balanceSize);
  const balancePager = (
    <TablePagination
      page={balancePage}
      canPrevious={balancePage > 0}
      canNext={hasMoreBalances}
      knownTotalPages={knownPageCount(balances.length, balanceSize)}
      onPrevious={() => setBalancePage((p) => Math.max(0, p - 1))}
      onNext={() => setBalancePage((p) => p + 1)}
      onPage={setBalancePage}
    />
  );
  const adjustments =
    adjustmentsLoad.load.state === "ready" ? adjustmentsLoad.load.data : [];
  const pagedAdjustments = slicePage(adjustments, historyPage, historySize);
  const hasMoreAdjustments = hasNextPage(adjustments, historyPage, historySize);
  const historyPager = (
    <TablePagination
      page={historyPage}
      canPrevious={historyPage > 0}
      canNext={hasMoreAdjustments}
      onPrevious={() => setHistoryPage((p) => Math.max(0, p - 1))}
      onNext={() => setHistoryPage((p) => p + 1)}
      onPage={setHistoryPage}
    />
  );

  return (
    <Page
      title={t("title")}
      actions={
        <>
          <PageSizeSelect
            value={tab === "positions" ? balanceSize : historySize}
            onChange={(value) => {
              if (tab === "positions") {
                setBalanceSize(value);
                setBalancePage(0);
              } else {
                setHistorySize(value);
                setHistoryPage(0);
              }
            }}
            ariaLabel={t("pagination.pageSize.ariaLabel")}
            rowCountLabel={(count) =>
              t("pagination.pageSize.rowCount", { count })
            }
          />
          <RefreshButton
            onClick={reloadActive}
            busy={
              tab === "positions"
                ? balancesLoad.load.state === "loading"
                : adjustmentsLoad.load.state === "loading"
            }
          />
          {tab === "positions" ? (
            <>
              <CsvTransferMenu
                imports={[
                  {
                    entities: ["positions"],
                    label: t("actions.importCsv"),
                  },
                ]}
                exports={[
                  {
                    entity: "positions",
                    filters: positionCsvFilters,
                    label: t("actions.exportCsv"),
                  },
                ]}
                onImported={reloadPositions}
              />
              <Button size="sm" onClick={openNewAdjust}>
                <Plus className="h-3.5 w-3.5" />
                {t("actions.adjust")}
              </Button>
            </>
          ) : (
            <Button
              size="sm"
              variant="outline"
              onClick={() => void exportHistory()}
              disabled={historyExportBusy}
            >
              <Download className="h-3.5 w-3.5" />
              {historyExportBusy
                ? t("actions.historyExporting")
                : t("actions.exportHistoryCsv")}
            </Button>
          )}
        </>
      }
    >
      {/* Filters */}
      <Card className="flex flex-wrap items-end gap-4 p-4">
        <div className="space-y-1.5">
          <Label htmlFor="pos-account">{t("filters.byAccount")}</Label>
          <Autocomplete
            id="pos-account"
            value={accountFilter}
            spellCheck={false}
            placeholder="acc-1"
            className="h-8 w-48 text-xs"
            suggestions={accountSuggestions}
            onChange={(value) => {
              setAccountFilter(value);
              setBalancePage(0);
              setHistoryPage(0);
            }}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="pos-asset">{t("filters.byAsset")}</Label>
          <Autocomplete
            id="pos-asset"
            value={assetFilter}
            spellCheck={false}
            placeholder="AAPL"
            className="h-8 w-32 text-xs"
            suggestions={assetSuggestions}
            onChange={(value) => {
              setAssetFilter(value);
              setBalancePage(0);
            }}
          />
        </div>
      </Card>

      <div className="flex w-fit gap-1 rounded-card border border-border bg-surface-2 p-1">
        {(["positions", "history"] as PositionsTab[]).map((tabId) => (
          <button
            key={tabId}
            type="button"
            onClick={() => setTab(tabId)}
            className={[
              "rounded-badge px-3 py-1 text-xs font-medium transition-colors duration-[180ms]",
              tab === tabId
                ? "bg-accent-dim text-accent"
                : "text-muted-lt hover:bg-surface-hover hover:text-text",
            ].join(" ")}
          >
            {t(`tabs.${tabId}`)}
          </button>
        ))}
      </div>

      {tab === "positions" && (
        <>
          <div className="flex items-center gap-2">
            <Coins className="h-3.5 w-3.5 text-muted" />
            <p className="text-[0.6875rem] uppercase tracking-[0.07em] text-muted">
              {t("balances.sectionLabel")}
            </p>
          </div>

          {balancesLoad.load.state === "loading" && (
            <TableSkeleton cols={9} />
          )}
          {balancesLoad.load.state === "error" && (
            <ErrorState
              message={balancesLoad.load.error}
              onRetry={balancesLoad.reload}
            />
          )}
          {balancesLoad.load.state === "ready" &&
            (balancesLoad.load.data.length === 0 ? (
              <>
                <EmptyState
                  title={t("balances.empty.title")}
                  hint={t("balances.empty.hint")}
                  action={
                    <Button size="sm" onClick={openNewAdjust}>
                      <Plus className="h-3.5 w-3.5" />
                      {t("actions.adjust")}
                    </Button>
                  }
                />
                <BalancesTable
                  balances={[]}
                  defaultDraftAccount={accountFilter.trim()}
                  defaultDraftAsset={assetFilter.trim()}
                  draftOpenRequest={draftOpenRequest}
                  accountSuggestions={accountSuggestions}
                  assetSuggestions={assetSuggestions}
                  onApplied={handleAdjustDone}
                />
              </>
            ) : (
              <>
                {balancePager}
                <BalancesTable
                  balances={pagedBalances}
                  defaultDraftAccount={accountFilter.trim()}
                  defaultDraftAsset={assetFilter.trim()}
                  draftOpenRequest={draftOpenRequest}
                  accountSuggestions={accountSuggestions}
                  assetSuggestions={assetSuggestions}
                  onApplied={handleAdjustDone}
                />
                {balancePager}
              </>
            ))}
        </>
      )}

      {tab === "history" && (
        <>
          <div className="flex flex-wrap items-center gap-3">
            <p className="text-[0.6875rem] uppercase tracking-[0.07em] text-muted">
              {t("history.sectionLabel")}
            </p>
            <div className="ml-auto flex items-center gap-2">
              <Label className="text-xs">{t("history.sourceLabel")}</Label>
              <Select
                value={sourceFilter}
                onValueChange={(v) => {
                  setSourceFilter(v as Source | "__all__");
                  setHistoryPage(0);
                }}
              >
                <SelectTrigger className="h-7 w-28 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__all__">
                    {t("history.sourceAll")}
                  </SelectItem>
                  {SOURCES.map((s) => (
                    <SelectItem key={s} value={s}>
                      {s}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          {historyExportError && <ErrorBanner message={historyExportError} />}
          {adjustmentsLoad.load.state === "loading" && (
            <TableSkeleton cols={7} />
          )}
          {adjustmentsLoad.load.state === "error" && (
            <ErrorState
              message={adjustmentsLoad.load.error}
              onRetry={adjustmentsLoad.reload}
            />
          )}
          {adjustmentsLoad.load.state === "ready" &&
            (adjustmentsLoad.load.data.length === 0 ? (
              <EmptyState
                title={t("history.empty.title")}
                hint={t("history.empty.hint")}
              />
            ) : (
              <>
                {historyPager}
                <Table>
                  <TableHeader>
                    <TableRow className="hover:bg-transparent">
                      <TableHead>{t("history.columns.time")}</TableHead>
                      <TableHead>{t("history.columns.account")}</TableHead>
                      <TableHead>{t("history.columns.asset")}</TableHead>
                      <TableHead>{t("history.columns.source")}</TableHead>
                      <TableHead>{t("history.columns.request")}</TableHead>
                      <TableHead>{t("history.columns.status")}</TableHead>
                      <TableHead>{t("history.columns.outcome")}</TableHead>
                      <TableHead />
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {pagedAdjustments.map((adj) => (
                      <HistoryRow
                        key={adj.externalId}
                        adj={adj}
                        onClone={openCloneAdjust}
                      />
                    ))}
                  </TableBody>
                </Table>
                {historyPager}
              </>
            ))}
        </>
      )}

      <AdjustDialog
        open={adjustOpen}
        onOpenChange={setAdjustOpen}
        initialAccount={adjustAccount}
        initialAsset={adjustAsset}
        initialBalanceMode={adjustBalanceMode}
        initialBalanceValue={adjustBalanceValue}
        initialHeldMode={adjustHeldMode}
        initialHeldValue={adjustHeldValue}
        initialIncomingMode={adjustIncomingMode}
        initialIncomingValue={adjustIncomingValue}
        initialAvgPrice={adjustAvgPrice}
        initialBalanceBoundsLower={adjustBalanceBoundsLower}
        initialBalanceBoundsUpper={adjustBalanceBoundsUpper}
        initialHeldBoundsLower={adjustHeldBoundsLower}
        initialHeldBoundsUpper={adjustHeldBoundsUpper}
        initialIncomingBoundsLower={adjustIncomingBoundsLower}
        initialIncomingBoundsUpper={adjustIncomingBoundsUpper}
        assetSuggestions={assetSuggestions}
        accountSuggestions={accountSuggestions}
        onDone={handleAdjustDone}
      />
    </Page>
  );
}
