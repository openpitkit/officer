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
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import type { TFunction } from "i18next";
import {
  ChevronDown,
  ChevronRight,
  CircleAlert,
  Coins,
  Download,
  Plus,
  RotateCcw,
  SlidersHorizontal,
  X,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { useNavigate, useSearchParams } from "react-router-dom";

import {
  ActionButton,
  ApiError,
  AutocompleteFilterField,
  AUTOCOMPLETE_SUGGESTION_LIMIT,
  CloneButton,
  ColumnHeader,
  ExactIdField,
  FieldLabel,
  FilterChip,
  FilterBar,
  FilterByButton,
  IdCell,
  MoreFiltersButton,
  MAX_LIST_LIMIT,
  NumberRangeFilter,
  OrdersButton,
  reportInvalidFilterControls,
  RowActions,
  Segmented,
  ShareLinkButton,
  SortableHeader,
  TimeRangeFilter,
  useOfficerApi,
  type MissingAccountPolicy,
} from "@/framework";
import { formatDate, formatTime } from "@/i18n/format";
import type {
  Adjustment,
  AdjustmentAmount,
  AdjustmentMode,
  Balance,
  BalanceListFilters,
  BoundsPair,
  RangeFilterMode,
  SortOrder,
  Source,
} from "@/api/types";
import { useAdjustmentsPage } from "@/api/useAdjustments";
import { useBalancesPage } from "@/api/useBalances";
import { Autocomplete } from "@/components/Autocomplete";
import { AssetCodeSuggestionFailure } from "@/components/AssetCodeSuggestionFailure";
import { sortDirection } from "@/lib/sortDirection";
import {
  EmptyState,
  ErrorBanner,
  ErrorState,
  StaleState,
  TableSkeleton,
} from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
import {
  CsvTransferMenu,
  PageSizeSelect,
  TablePagination,
} from "@/components/TableControls";
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
import { knownPageCount } from "@/lib/tablePagination";
import { usePersistentPageSize } from "@/lib/tablePageSize";
import { absoluteAppUrl, shareUrl } from "@/lib/shareLink";
import { ACTIVE_STATUS_QUERY } from "@/lib/orderStatus";
import {
  DEFAULT_SEARCH_DEBOUNCE_MS,
  useDebouncedValue,
} from "@/lib/useDebounce";
import { useAssetCodeSuggestions } from "@/lib/useAssetCodeSuggestions";
import { useGlobalAccountFilter } from "@/lib/globalAccountFilter";
import { operatorOptions } from "@/lib/dataControlLabels";
import { isDecimalRangeValid } from "@/lib/numberStep";
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

function positionsFilterHref({
  tab,
  account,
  asset,
  group,
}: {
  tab?: "positions" | "history";
  account?: string;
  asset?: string;
  group?: string;
}): string {
  const query = new URLSearchParams();
  if (tab === "history") {
    query.set("tab", "history");
  }
  if (account !== undefined && account !== "") {
    query.set("account", account);
  }
  if (asset !== undefined && asset !== "") {
    query.set("asset", asset);
  }
  if (group !== undefined && group !== "") {
    query.set("group", group);
  }
  return shareUrl("/positions", query);
}

/** Deep link to the Orders screen pre-filtered to this account and the active
 *  (working) order statuses, matching the Orders "Active" quick filter. */
function activeOrdersHref(account: string): string {
  const query = new URLSearchParams();
  query.set("account", account);
  query.set("status", ACTIVE_STATUS_QUERY);
  return absoluteAppUrl(`/orders?${query.toString()}`);
}

function ordersFilterHref(account: string): string {
  const query = new URLSearchParams();
  query.set("account", account);
  return shareUrl("/orders", query);
}

/** Split a localized timestamp so the date and time are rendered as
 *  separate unbreakable units that wrap as a whole, never split mid-value. */
function SplitTime({ iso }: { iso: string }) {
  if (!iso) {
    return <span className="text-muted-lt">-</span>;
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
  return v && v !== "" ? v : "-";
}

/** Render a position value next to the asset it is denominated in, so a figure
 *  kept in one account's currency is never read as one in another's. An account
 *  whose currency cascade sets no tier leaves the value with no unit at all;
 *  that shows as the project's unset-currency dash rather than as a bare
 *  number, which would read as though the unit were obvious. */
function DenominatedAmount({
  value,
  currency,
}: {
  value: string | undefined;
  currency: string;
}) {
  const { t } = useTranslation("positions");
  if (!value || value.trim() === "") {
    return <span className="text-muted-lt">-</span>;
  }
  const unit = currency.trim();
  return (
    <span className="inline-flex items-baseline justify-end gap-1">
      <span>{value}</span>
      {unit === "" ? (
        <span
          className="text-muted-lt"
          title={t("balances.noAccountCurrencyHint")}
        >
          -
        </span>
      ) : (
        <span className="text-muted-lt">{unit}</span>
      )}
    </span>
  );
}

/** Whether a decimal amount string represents a non-zero value. Operates on the
 *  precision string directly (no float parse): a value is zero only when every
 *  digit is 0, ignoring sign, decimal point, and surrounding whitespace. */
function isNonZeroAmount(value: string | undefined): boolean {
  if (value === undefined) {
    return false;
  }
  const trimmed = value.trim();
  if (trimmed === "") {
    return false;
  }
  return /[1-9]/.test(trimmed);
}

/** Leading characters a spreadsheet application reads as the start of a formula
 *  or DDE command. A field beginning with one of them executes when the
 *  operator opens the export, so the export neutralizes it. Mirrors the
 *  server-side businesscsv guard. */
const CSV_FORMULA_LEADERS = "=+-@\t\r";

/** A complete decimal number: the one exemption from formula quoting. A number
 *  is never a formula, and quoting it would turn every negative balance and
 *  P&L in the export into text the operator cannot sum. */
const CSV_DECIMAL = /^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$/;

function csvFieldIsFormula(text: string): boolean {
  // Leading apostrophes are the "treat as text" marker, so they are stripped
  // before the check and the prefix stacks, exactly as the server does.
  const bare = text.replace(/^'+/, "");
  if (bare === "" || !CSV_FORMULA_LEADERS.includes(bare.charAt(0))) {
    return false;
  }
  return !CSV_DECIMAL.test(bare);
}

function csvCell(value: string | number | undefined): string {
  const raw = value === undefined ? "" : String(value);
  const text = csvFieldIsFormula(raw) ? `'${raw}` : raw;
  if (/[",\n\r\t]/.test(text)) {
    return `"${text.replaceAll('"', '""')}"`;
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
    adj.id,
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

function sameStrings(left: string[], right: string[]): boolean {
  return (
    left.length === right.length &&
    left.every((value, index) => value === right[index])
  );
}

function mergeCodeSuggestions(...groups: string[][]): string[] {
  const seen = new Set<string>();
  const merged: string[] = [];
  for (const group of groups) {
    for (const value of group) {
      if (!seen.has(value)) {
        seen.add(value);
        merged.push(value);
      }
    }
  }
  return merged;
}

function useAccountCodeSuggestions(query: string, enabled: boolean): string[] {
  const { fetchAccounts } = useOfficerApi();
  const debouncedQuery = useDebouncedValue(
    query.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const [suggestions, setSuggestions] = useState<string[]>([]);

  useEffect(() => {
    if (!enabled || debouncedQuery === "") {
      return;
    }
    const controller = new AbortController();
    void fetchAccounts(
      {
        code: debouncedQuery,
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      controller.signal,
    )
      .then((accounts) => {
        const next = accounts.map((account) => account.code);
        setSuggestions((prev) => (sameStrings(prev, next) ? prev : next));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error(err);
          setSuggestions((prev) => (prev.length === 0 ? prev : []));
        }
      });
    return () => controller.abort();
  }, [debouncedQuery, enabled, fetchAccounts]);

  return enabled && debouncedQuery !== "" ? suggestions : [];
}

/** The distinct account currencies in use, offered as the units a denominated
 *  threshold can be given in. A currency no account keeps its P&L in would
 *  match no row, so the suggestions are drawn from the accounts themselves
 *  rather than from the whole asset dictionary. */
function useAccountCurrencySuggestions(enabled: boolean): string[] {
  const { fetchAccounts } = useOfficerApi();
  const [suggestions, setSuggestions] = useState<string[]>([]);

  useEffect(() => {
    if (!enabled) {
      return;
    }
    const controller = new AbortController();
    void fetchAccounts(
      { limit: MAX_LIST_LIMIT, sort: "code" },
      controller.signal,
    )
      .then((accounts) => {
        const codes = new Set<string>();
        for (const account of accounts) {
          if (account.effectiveCurrency) {
            codes.add(account.effectiveCurrency);
          }
        }
        const next = [...codes].sort();
        setSuggestions((prev) => (sameStrings(prev, next) ? prev : next));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error(err);
          setSuggestions((prev) => (prev.length === 0 ? prev : []));
        }
      });
    return () => controller.abort();
  }, [enabled, fetchAccounts]);

  return suggestions;
}

function isDecimal(value: string): boolean {
  return parseDecimal(value) !== null;
}

function hasValue(value: string | undefined): boolean {
  return (value ?? "").trim() !== "";
}

function outcomeAmountText(
  request: AdjustmentAmount | undefined,
  delta: string,
  result: string,
): string {
  if (request?.mode === "absolute") {
    return result || "-";
  }
  return `Δ${delta} → ${result}`;
}

function adjustmentResultParts(
  result: Exclude<
    NonNullable<Adjustment["accepted"]>["realizedPnlResult"],
    undefined
  >,
): {
  delta: string;
  result: string;
} {
  if (typeof result === "string") {
    return { delta: "", result };
  }
  return result;
}

function pnlHaltText(t: TFunction, reason: string): string | null {
  if (!reason) return null;
  switch (reason) {
    case "missing_fx":
      return t("balances.pnlHalt.missingFx");
    case "missing_account_currency":
      return t("balances.pnlHalt.missingAccountCurrency");
    case "missing_initial_pnl":
      return t("balances.pnlHalt.missingInitialPnl");
    case "missing_cost_basis":
      return t("balances.pnlHalt.missingCostBasis");
    case "arithmetic_overflow":
      return t("balances.pnlHalt.arithmeticOverflow");
    default:
      return t("balances.pnlHalt.unknown");
  }
}

type BalanceRangeKey =
  | "available"
  | "held"
  | "incoming"
  | "averageEntryPrice"
  | "realizedPnl"
  | "updatedAt";

type BalanceRangeDraft = {
  mode: RangeFilterMode;
  min: string;
  max: string;
  /** Asset the bounds are expressed in. Only meaningful for the keys in
   *  DENOMINATED_RANGE_KEYS, whose column is denominated per row rather than by
   *  the position asset, and required once such a condition is active. */
  currency: string;
};

type BalanceRangeDrafts = Record<BalanceRangeKey, BalanceRangeDraft>;

const EMPTY_BALANCE_RANGES: Record<BalanceRangeKey, BalanceRangeDraft> = {
  available: { mode: "all", min: "", max: "", currency: "" },
  held: { mode: "all", min: "", max: "", currency: "" },
  incoming: { mode: "all", min: "", max: "", currency: "" },
  averageEntryPrice: { mode: "all", min: "", max: "", currency: "" },
  realizedPnl: { mode: "all", min: "", max: "", currency: "" },
  updatedAt: { mode: "all", min: "", max: "", currency: "" },
};

const NUMERIC_RANGE_KEYS: Exclude<BalanceRangeKey, "updatedAt">[] = [
  "available",
  "held",
  "incoming",
  "averageEntryPrice",
  "realizedPnl",
];

/** Range keys whose column is denominated in the account currency rather than
 *  in the position asset. A threshold on one of these is not comparable until
 *  it names the asset it is expressed in, so the currency has no default. */
const DENOMINATED_RANGE_KEYS: BalanceRangeKey[] = [
  "averageEntryPrice",
  "realizedPnl",
];

function isDenominatedRangeKey(key: BalanceRangeKey): boolean {
  return DENOMINATED_RANGE_KEYS.includes(key);
}

/** Whether a denominated condition is missing the currency it needs. An
 *  inactive condition compares nothing and so needs no currency. */
function needsCurrency(
  denominated: boolean,
  draft: BalanceRangeDraft,
): boolean {
  return denominated && hasActiveRange(draft) && draft.currency.trim() === "";
}

/** Seed the balance range drafts from URL params so a shared link restores the
 *  full range filter set. Numeric ranges mirror the API `<key>Mode/Min/Max`
 *  params, plus `<key>Currency` for the denominated ones; the updatedAt range
 *  mirrors `updatedAtMode/updatedAfter/updatedBefore`. */
function balanceRangesFromParams(params: URLSearchParams): BalanceRangeDrafts {
  const ranges: BalanceRangeDrafts = {
    available: { ...EMPTY_BALANCE_RANGES.available },
    held: { ...EMPTY_BALANCE_RANGES.held },
    incoming: { ...EMPTY_BALANCE_RANGES.incoming },
    averageEntryPrice: { ...EMPTY_BALANCE_RANGES.averageEntryPrice },
    realizedPnl: { ...EMPTY_BALANCE_RANGES.realizedPnl },
    updatedAt: { ...EMPTY_BALANCE_RANGES.updatedAt },
  };
  for (const key of NUMERIC_RANGE_KEYS) {
    const mode = params.get(`${key}Mode`);
    if (mode !== null) {
      const draft = {
        mode: mode as RangeFilterMode,
        min: params.get(`${key}Min`) ?? "",
        max: params.get(`${key}Max`) ?? "",
        currency: params.get(`${key}Currency`) ?? "",
      };
      ranges[key] =
        isDenominatedRangeKey(key) && needsCurrency(true, draft)
          ? { ...EMPTY_BALANCE_RANGES[key] }
          : draft;
    }
  }
  const updatedMode = params.get("updatedAtMode");
  if (updatedMode !== null) {
    ranges.updatedAt = {
      mode: updatedMode as RangeFilterMode,
      min: params.get("updatedAfter") ?? "",
      max: params.get("updatedBefore") ?? "",
      currency: "",
    };
  }
  return ranges;
}

function cloneBalanceRanges(ranges: BalanceRangeDrafts): BalanceRangeDrafts {
  return {
    available: { ...ranges.available },
    held: { ...ranges.held },
    incoming: { ...ranges.incoming },
    averageEntryPrice: { ...ranges.averageEntryPrice },
    realizedPnl: { ...ranges.realizedPnl },
    updatedAt: { ...ranges.updatedAt },
  };
}

/** Serialize the active balance range drafts into a `URLSearchParams`, omitting
 *  ranges left at their default (`mode === "all"`). */
function appendBalanceRanges(
  query: URLSearchParams,
  ranges: BalanceRangeDrafts,
): void {
  for (const key of NUMERIC_RANGE_KEYS) {
    const draft = ranges[key];
    if (draft.mode === "all") {
      continue;
    }
    query.set(`${key}Mode`, draft.mode);
    if (draft.min.trim() !== "") {
      query.set(`${key}Min`, draft.min.trim());
    }
    if (draft.max.trim() !== "") {
      query.set(`${key}Max`, draft.max.trim());
    }
    if (isDenominatedRangeKey(key) && draft.currency.trim() !== "") {
      query.set(`${key}Currency`, draft.currency.trim());
    }
  }
  const updated = ranges.updatedAt;
  if (updated.mode !== "all") {
    query.set("updatedAtMode", updated.mode);
    if (updated.min.trim() !== "") {
      query.set("updatedAfter", updated.min.trim());
    }
    if (updated.max.trim() !== "") {
      query.set("updatedBefore", updated.max.trim());
    }
  }
}

function rangeValue(value: string): string {
  return value.trim();
}

function localDateTimeFilter(value: string): string {
  const trimmed = value.trim();
  if (trimmed === "") {
    return "";
  }
  const date = new Date(trimmed);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  return date.toISOString();
}

/** Translate one range draft into the API filter params. A denominated
 *  condition is dropped entirely while it names no currency: its bounds are not
 *  comparable against a per-row denominated column, so sending them would ask
 *  the server to compare across currencies. */
function addBalanceRange(
  filter: BalanceListFilters,
  key: Exclude<BalanceRangeKey, "updatedAt">,
  draft: BalanceRangeDraft,
) {
  const target = filter as Record<string, string | undefined>;
  const min = rangeValue(draft.min);
  const max = rangeValue(draft.max);
  const currency = draft.currency.trim();
  if (draft.mode === "all") {
    return;
  }
  if (isDenominatedRangeKey(key) && currency === "") {
    return;
  }
  if (draft.mode === "greater_than" && min !== "") {
    target[`${key}Mode`] = draft.mode;
    target[`${key}Min`] = min;
  } else if (
    (draft.mode === "lt" ||
      draft.mode === "lte" ||
      draft.mode === "less_than") &&
    min !== ""
  ) {
    target[`${key}Mode`] = draft.mode;
    target[`${key}Max`] = min;
  } else if (draft.mode === "between" && min !== "" && max !== "") {
    target[`${key}Mode`] = draft.mode;
    target[`${key}Min`] = min;
    target[`${key}Max`] = max;
  } else if (min !== "") {
    target[`${key}Mode`] = draft.mode;
    target[`${key}Min`] = min;
  }
  if (isDenominatedRangeKey(key) && target[`${key}Mode`] !== undefined) {
    target[`${key}Currency`] = currency;
  }
}

function addUpdatedAtRange(
  filter: BalanceListFilters,
  draft: BalanceRangeDraft,
) {
  const min = localDateTimeFilter(draft.min);
  const max = localDateTimeFilter(draft.max);
  if (draft.mode === "all") {
    return;
  }
  if ((draft.mode === "greater_than" || draft.mode === "after") && min !== "") {
    filter.updatedAtMode = draft.mode;
    filter.updatedAfter = min;
  } else if (draft.mode === "less_than" && max !== "") {
    filter.updatedAtMode = draft.mode;
    filter.updatedBefore = max;
  } else if (draft.mode === "before" && min !== "") {
    filter.updatedAtMode = draft.mode;
    filter.updatedBefore = min;
  } else if (draft.mode === "between" && min !== "" && max !== "") {
    filter.updatedAtMode = draft.mode;
    filter.updatedAfter = min;
    filter.updatedBefore = max;
  }
}

/** Summarize a range for its filter chip. A denominated threshold shows the
 *  currency it was given in, so the chip states the same unit the comparison
 *  actually ran in. */
function rangeChipValue(
  operator: string,
  mode: RangeFilterMode,
  min: string,
  max: string,
  currency = "",
): string {
  const from = min.trim();
  const to = max.trim();
  const unit = currency.trim();
  const parts =
    mode === "between"
      ? [operator, from, to]
      : mode === "less_than" || mode === "before"
        ? [operator, to || from]
        : [operator, from || to];
  return [...parts, unit].filter(Boolean).join(" ");
}

function hasActiveRange(draft: BalanceRangeDraft): boolean {
  return (
    draft.mode !== "all" && (draft.min.trim() !== "" || draft.max.trim() !== "")
  );
}

/** One advanced-filter row. When the column is denominated per row rather than
 *  by the position asset, the row also carries the currency its bounds are
 *  given in: the threshold cannot be compared without it, so an active
 *  condition with no currency is reported inline and blocks Apply. */
function PositionRangeFilter({
  label,
  draft,
  inputType = "text",
  denominated = false,
  currencySuggestions = [],
  onChange,
}: {
  label: string;
  draft: BalanceRangeDraft;
  inputType?: "text" | "datetime-local";
  denominated?: boolean;
  currencySuggestions?: string[];
  onChange: (next: BalanceRangeDraft) => void;
}) {
  const { t } = useTranslation("positions");
  const { t: tc } = useTranslation();
  const operator =
    draft.mode === "all"
      ? inputType === "datetime-local"
        ? "after"
        : "eq"
      : draft.mode;
  const currencyMissing = needsCurrency(denominated, draft);
  const currencyFieldId = `position-filter-currency-${useId()}`;
  const currencyErrorId = `${currencyFieldId}-error`;
  return (
    <div className="grid gap-1">
      <FieldLabel>{label}</FieldLabel>
      <div
        className={cn(
          "grid gap-2",
          denominated &&
            "md:grid-cols-[minmax(0,1fr)_minmax(14rem,18rem)] md:items-center",
        )}
      >
        <div className="min-w-0">
          {inputType === "datetime-local" ? (
            <TimeRangeFilter
              operator={operator}
              from={draft.min}
              to={draft.max}
              fluid
              showPresets={false}
              operators={operatorOptions(tc, "time")}
              operatorAriaLabel={label}
              clearLabel={tc("filters.clearField")}
              onOperatorChange={(next) =>
                onChange({ ...draft, mode: next as RangeFilterMode })
              }
              onFromChange={(value) =>
                onChange({
                  ...draft,
                  mode:
                    draft.mode === "all"
                      ? (operator as RangeFilterMode)
                      : draft.mode,
                  min: value,
                })
              }
              onToChange={(value) =>
                onChange({
                  ...draft,
                  mode:
                    draft.mode === "all"
                      ? (operator as RangeFilterMode)
                      : draft.mode,
                  max: value,
                })
              }
            />
          ) : (
            <NumberRangeFilter
              operator={operator}
              min={draft.min}
              max={draft.max}
              fluid
              operators={operatorOptions(tc, "number")}
              operatorAriaLabel={label}
              clearLabel={tc("filters.clearField")}
              onOperatorChange={(next) =>
                onChange({ ...draft, mode: next as RangeFilterMode })
              }
              onMinChange={(value) =>
                onChange({
                  ...draft,
                  mode:
                    draft.mode === "all"
                      ? (operator as RangeFilterMode)
                      : draft.mode,
                  min: value,
                })
              }
              onMaxChange={(value) =>
                onChange({
                  ...draft,
                  mode:
                    draft.mode === "all"
                      ? (operator as RangeFilterMode)
                      : draft.mode,
                  max: value,
                })
              }
            />
          )}
        </div>
        {denominated && (
          <div className="flex min-w-0 items-center gap-2">
            <Label
              htmlFor={currencyFieldId}
              className="shrink-0 whitespace-nowrap text-muted-lt"
            >
              {t("filters.currency.label")}
            </Label>
            <div className="min-w-0 flex-1">
              <Autocomplete
                id={currencyFieldId}
                value={draft.currency}
                spellCheck={false}
                placeholder={t("filters.currency.placeholder")}
                suggestions={currencySuggestions}
                aria-invalid={currencyMissing}
                aria-errormessage={
                  currencyMissing ? currencyErrorId : undefined
                }
                aria-label={t("filters.currency.ariaLabel", { field: label })}
                className="h-8 text-xs"
                clearLabel={tc("filters.clearField")}
                onClear={() => onChange({ ...draft, currency: "" })}
                onChange={(currency) => onChange({ ...draft, currency })}
              />
            </div>
          </div>
        )}
      </div>
      {denominated && currencyMissing && (
        <p
          id={currencyErrorId}
          className="w-full text-[0.6875rem] text-[var(--danger)]"
        >
          {t("filters.currency.required")}
        </p>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Balances table - adjustment panel
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
  return addDecimalStrings(hasValue(current) ? (current ?? "0") : "0", value);
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
  currency,
  value,
  valid,
  disabled,
  onChange,
  onSubmit,
}: {
  current: string | undefined;
  currency: string;
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
    <div className="grid gap-4 border-t border-border px-3 py-3 md:grid-cols-[minmax(8rem,1fr)_8rem_minmax(10rem,1.2fr)_minmax(8rem,1fr)] md:items-end">
      <div>
        <p className="text-[0.625rem] font-bold uppercase tracking-[0.07em] text-muted">
          {label}
        </p>
        <p className="nums mt-1 text-sm text-text">
          <DenominatedAmount value={current} currency={currency} />
        </p>
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
          clearLabel={t("common:filters.clearField")}
          onClear={() => onChange("")}
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
            "nums mt-1 text-sm md:pr-3 md:text-right",
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

function RealizedPnlIntentRow({
  current,
  currency,
  value,
  valid,
  disabled,
  onChange,
  onSubmit,
}: {
  current: string | undefined;
  currency: string;
  value: string;
  valid: boolean;
  disabled: boolean;
  onChange: (next: string) => void;
  onSubmit: () => void;
}) {
  const { t } = useTranslation("positions");
  const invalid = hasValue(value) && !valid;
  const result = hasValue(value) ? value.trim() : null;
  const label = t("balances.columns.realizedPnl");

  return (
    <div className="grid gap-4 border-t border-border px-3 py-3 md:grid-cols-[minmax(8rem,1fr)_8rem_minmax(10rem,1.2fr)_minmax(8rem,1fr)] md:items-end">
      <div>
        <p className="text-[0.625rem] font-bold uppercase tracking-[0.07em] text-muted">
          {label}
        </p>
        <p className="nums mt-1 text-sm text-text">
          <DenominatedAmount value={current} currency={currency} />
        </p>
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
        <Label htmlFor="adjust-realized-pnl" className="md:hidden">
          {t("panel.columns.amount")}
        </Label>
        <NumberStepper
          id="adjust-realized-pnl"
          value={value}
          min={null}
          spellCheck={false}
          placeholder={t("panel.noChange")}
          inputClassName="h-8 text-right text-xs"
          disabled={disabled}
          aria-label={t("dialog.fields.realizedPnl")}
          clearLabel={t("common:filters.clearField")}
          onClear={() => onChange("")}
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
            "nums mt-1 text-sm md:pr-3 md:text-right",
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
    <div className="grid gap-4 border-t border-border px-3 py-3 md:grid-cols-[minmax(8rem,1fr)_8rem_minmax(10rem,1.2fr)_minmax(8rem,1fr)] md:items-end">
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
            <SelectItem value="absolute">
              {t("dialog.mode.absolute")}
            </SelectItem>
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
          clearLabel={t("common:filters.clearField")}
          onClear={() => onChange({ ...field, value: "" })}
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
            "nums mt-1 text-sm md:pr-3 md:text-right",
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
        <Label htmlFor={`adjust-${id}-lower`}>{t("dialog.bounds.lower")}</Label>
        <NumberStepper
          id={`adjust-${id}-lower`}
          value={field.lower}
          min={null}
          spellCheck={false}
          placeholder={t("panel.noChange")}
          inputClassName="h-8 text-right text-xs"
          disabled={disabled}
          clearLabel={t("common:filters.clearField")}
          onClear={() => onChange({ ...field, lower: "" })}
          onChange={(lower) => onChange({ ...field, lower })}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              onSubmit();
            }
          }}
        />
      </div>
      <div className="space-y-1">
        <Label htmlFor={`adjust-${id}-upper`}>{t("dialog.bounds.upper")}</Label>
        <NumberStepper
          id={`adjust-${id}-upper`}
          value={field.upper}
          min={null}
          spellCheck={false}
          placeholder={t("panel.noChange")}
          inputClassName="h-8 text-right text-xs"
          disabled={disabled}
          clearLabel={t("common:filters.clearField")}
          onClear={() => onChange({ ...field, upper: "" })}
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
  const panelRef = useRef<HTMLDivElement>(null);
  const focusInsideRef = useRef(false);
  useEffect(() => {
    const previousFocus = document.activeElement;
    panelRef.current?.focus();
    return () => {
      if (
        focusInsideRef.current &&
        previousFocus instanceof HTMLElement &&
        previousFocus.isConnected
      ) {
        previousFocus.focus();
      }
    };
  }, []);
  const [account, setAccount] = useState(initialAccount);
  const [asset, setAsset] = useState(initialAsset);
  const [available, setAvailable] =
    useState<AmountDraftState>(emptyAmountDraft);
  const [held, setHeld] = useState<AmountDraftState>(emptyAmountDraft);
  const [incoming, setIncoming] = useState<AmountDraftState>(emptyAmountDraft);
  const [avgPrice, setAvgPrice] = useState("");
  const [realizedPnl, setRealizedPnl] = useState("");
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
  const [missingAccountConfirmOpen, setMissingAccountConfirmOpen] =
    useState(false);
  const localAccountSuggestions = useAccountCodeSuggestions(
    account,
    !lockIdentity,
  );
  const localAssetSuggestionResult = useAssetCodeSuggestions(
    asset,
    !lockIdentity,
  );
  const mergedAccountSuggestions = useMemo(
    () => mergeCodeSuggestions(accountSuggestions, localAccountSuggestions),
    [accountSuggestions, localAccountSuggestions],
  );
  const mergedAssetSuggestions = useMemo(
    () =>
      mergeCodeSuggestions(assetSuggestions, localAssetSuggestionResult.codes),
    [assetSuggestions, localAssetSuggestionResult.codes],
  );

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
  const averageEntryPriceAllowed =
    !hasValue(avgPrice) ||
    hasValue(realizedPnl) ||
    balance !== undefined ||
    amountFields.some(({ current, field }) => {
      const result = amountResult(current, field);
      const parsed = result === null ? null : parseDecimal(result);
      return parsed !== null && parsed.units !== 0n;
    });
  const amountFieldsValid = amountFields.every(({ current, field }) => {
    if (!hasValue(field.value)) {
      return true;
    }
    return amountResult(current, field) !== null;
  });
  const avgPriceValid = !hasValue(avgPrice) || isDecimal(avgPrice);
  const realizedPnlValid = !hasValue(realizedPnl) || isDecimal(realizedPnl);
  const allBoundsValid =
    boundsAreValid(balanceBounds) &&
    boundsAreValid(heldBounds) &&
    boundsAreValid(incomingBounds);
  const hasBoundsChange =
    boundsHaveValue(balanceBounds) ||
    boundsHaveValue(heldBounds) ||
    boundsHaveValue(incomingBounds);
  const boundsChangeCount = [balanceBounds, heldBounds, incomingBounds].filter(
    boundsHaveValue,
  ).length;
  const hasChanges =
    hasAmountChange ||
    hasValue(avgPrice) ||
    hasValue(realizedPnl) ||
    hasBoundsChange;
  const hasFilledField =
    (!lockIdentity && (hasValue(account) || hasValue(asset))) || hasChanges;
  const canSubmit =
    trimAccount !== "" &&
    trimAsset !== "" &&
    hasChanges &&
    averageEntryPriceAllowed &&
    amountFieldsValid &&
    avgPriceValid &&
    realizedPnlValid &&
    allBoundsValid &&
    !busy;

  const resetAllFields = () => {
    if (!lockIdentity) {
      setAccount("");
      setAsset("");
    }
    setAvailable(emptyAmountDraft());
    setHeld(emptyAmountDraft());
    setIncoming(emptyAmountDraft());
    setAvgPrice("");
    setRealizedPnl("");
    setBalanceBounds(emptyBoundsDraft());
    setHeldBounds(emptyBoundsDraft());
    setIncomingBounds(emptyBoundsDraft());
    setError(null);
    setOutcome(null);
    setMissingAccountConfirmOpen(false);
  };

  const submit = async (missingAccount: MissingAccountPolicy = "reject") => {
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
    if (
      !amountFieldsValid ||
      !avgPriceValid ||
      !realizedPnlValid ||
      !allBoundsValid
    ) {
      setError(t("panel.invalidDecimal"));
      return;
    }
    if (!averageEntryPriceAllowed) {
      setError(t("panel.positionAmountRequired"));
      return;
    }
    setBusy(true);
    setError(null);
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
      if (hasValue(realizedPnl)) {
        body.realizedPnl = realizedPnl.trim();
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
      const result = await createAdjustment(trimAccount, body, missingAccount);
      setOutcome(result);
      onDone();
    } catch (err) {
      if (
        !lockIdentity &&
        missingAccount === "reject" &&
        err instanceof ApiError &&
        err.code === "account_missing"
      ) {
        setMissingAccountConfirmOpen(true);
      } else {
        setError(errMessage(err));
      }
    } finally {
      setBusy(false);
    }
  };

  // Fields stay editable after a submit so the operator can tweak the same
  // values and submit again; the outcome banner reflects the latest result.
  const disabled = busy;

  return (
    <>
      <div
        ref={panelRef}
        tabIndex={-1}
        role="region"
        aria-label={t("panel.title")}
        className="space-y-4 border-l-2 border-l-accent bg-surface-2 px-4 py-4"
        onFocusCapture={() => {
          focusInsideRef.current = true;
        }}
        onBlurCapture={(event) => {
          const nextFocus = event.relatedTarget;
          if (
            !(nextFocus instanceof Node) ||
            !event.currentTarget.contains(nextFocus)
          ) {
            focusInsideRef.current = false;
          }
        }}
        onKeyDown={(event) => {
          if (event.key !== "Escape") {
            return;
          }
          const target = event.target;
          if (
            target instanceof HTMLElement &&
            target.getAttribute("role") === "combobox" &&
            target.getAttribute("aria-expanded") === "true"
          ) {
            return;
          }
          onClose();
        }}
      >
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <div className="flex flex-wrap items-center gap-2">
              <SlidersHorizontal className="h-3.5 w-3.5 text-accent" />
              <p className="text-sm font-bold text-text">{t("panel.title")}</p>
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
              suggestions={mergedAccountSuggestions}
              disabled={disabled || lockIdentity}
              onChange={setAccount}
              onClear={() => setAccount("")}
              clearLabel={t("common:filters.clearField")}
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
              suggestions={mergedAssetSuggestions}
              disabled={disabled || lockIdentity}
              onChange={setAsset}
              onClear={() => setAsset("")}
              clearLabel={t("common:filters.clearField")}
            />
            <AssetCodeSuggestionFailure
              failed={localAssetSuggestionResult.failed}
            />
          </div>
        </div>

        <div className="border border-border bg-bg">
          <div className="grid grid-cols-[minmax(8rem,1fr)_8rem_minmax(10rem,1.2fr)_minmax(8rem,1fr)] gap-4 px-3 py-2 text-[0.625rem] font-bold uppercase tracking-[0.07em] text-muted max-md:hidden">
            <span>{t("panel.columns.current")}</span>
            <span>{t("panel.columns.intent")}</span>
            <span>{t("panel.columns.amount")}</span>
            <span className="text-right">{t("panel.columns.result")}</span>
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
            currency={balance?.accountCurrency ?? ""}
            value={avgPrice}
            valid={avgPriceValid}
            disabled={disabled}
            onChange={setAvgPrice}
            onSubmit={() => void submit()}
          />
          <RealizedPnlIntentRow
            current={balance?.realizedPnl}
            currency={balance?.accountCurrency ?? ""}
            value={realizedPnl}
            valid={realizedPnlValid}
            disabled={disabled}
            onChange={setRealizedPnl}
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
        {error && (
          <ErrorBanner message={error} onDismiss={() => setError(null)} />
        )}

        <div className="flex flex-wrap justify-end gap-2 border-t border-border pt-3">
          <Button variant="outline" size="sm" onClick={onClose} disabled={busy}>
            {outcome
              ? t("dialog.footer.close")
              : t("actions.cancel", { ns: "common" })}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={resetAllFields}
            disabled={busy || !hasFilledField}
          >
            <RotateCcw className="h-3.5 w-3.5" />
            {t("panel.resetAll")}
          </Button>
          <Button size="sm" onClick={() => void submit()} disabled={!canSubmit}>
            {busy ? t("panel.saving") : t("panel.submit")}
          </Button>
        </div>
      </div>
      <AlertDialog
        open={missingAccountConfirmOpen}
        onOpenChange={setMissingAccountConfirmOpen}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("missingAccountConfirm.title")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("missingAccountConfirm.description", { account: trimAccount })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>
              {t("actions.cancel", { ns: "common" })}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                setMissingAccountConfirmOpen(false);
                void submit("create");
              }}
              disabled={busy}
            >
              {t("missingAccountConfirm.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function BalanceEditRow({
  balance,
  expanded,
  onToggle,
  onClose,
  onShowHistory,
  onFilterAccount,
  onFilterAsset,
  accountSuggestions,
  assetSuggestions,
  onApplied,
}: {
  balance: Balance;
  expanded: boolean;
  onToggle: () => void;
  onClose: () => void;
  onShowHistory: (account: string, asset: string) => void;
  onFilterAccount: (account: string) => void;
  onFilterAsset: (asset: string) => void;
  accountSuggestions: string[];
  assetSuggestions: string[];
  onApplied: () => void;
}) {
  const { t } = useTranslation("positions");
  const { t: tc } = useTranslation("common");
  const navigate = useNavigate();
  const b = balance;
  const haltText = pnlHaltText(t, b.realizedPnlHaltReason);
  const openOrders = () =>
    navigate(`/orders?account=${encodeURIComponent(b.account)}`);
  const openActiveOrders = () =>
    navigate(
      `/orders?account=${encodeURIComponent(b.account)}` +
        `&status=${ACTIVE_STATUS_QUERY}`,
    );
  return (
    <>
      {/* Clicking the row body opens this position's adjustment history,
          pre-filtered by account + asset. The adjust control below stops
          propagation so it stays a distinct, explicit action. */}
      <TableRow
        className={cn("cursor-pointer", expanded && "bg-accent-dim")}
        onClick={() => onShowHistory(b.account, b.asset)}
      >
        <TableCell className="nums text-xs">
          <div className="flex min-w-0 items-center gap-1">
            <IdCell
              value={b.account}
              copyTitle={t("common:rowActions.copyId")}
              copiedTitle={t("common:rowActions.copiedId")}
            />
            <span className="ml-auto flex shrink-0 items-center gap-1">
              <FilterByButton
                size={28}
                title={tc("rowActions.filterByTitle", { field: b.account })}
                href={positionsFilterHref({ account: b.account })}
                onClick={() => onFilterAccount(b.account)}
              />
              <OrdersButton
                size={28}
                title={t("balances.ordersTitle", { account: b.account })}
                href={ordersFilterHref(b.account)}
                onClick={openOrders}
              />
            </span>
          </div>
        </TableCell>
        <TableCell className="nums text-xs">
          <div className="flex min-w-0 items-center gap-1">
            <span className="min-w-0 truncate">{b.asset}</span>
            <span className="ml-auto flex shrink-0 items-center">
              <FilterByButton
                size={28}
                title={tc("rowActions.filterByTitle", { field: b.asset })}
                href={positionsFilterHref({ asset: b.asset })}
                onClick={() => onFilterAsset(b.asset)}
              />
            </span>
          </div>
        </TableCell>
        <TableCell className="nums text-right text-xs">{b.available}</TableCell>
        <TableCell className="nums text-right text-xs">
          <div className="flex items-center justify-end gap-1">
            <span className="min-w-0 truncate">{b.held}</span>
            {isNonZeroAmount(b.held) && (
              <span className="shrink-0">
                <OrdersButton
                  size={22}
                  title={t("balances.activeOrdersTitle", {
                    account: b.account,
                  })}
                  href={activeOrdersHref(b.account)}
                  onClick={openActiveOrders}
                />
              </span>
            )}
          </div>
        </TableCell>
        <TableCell className="nums text-right text-xs">
          <div className="flex items-center justify-end gap-1">
            <span className="min-w-0 truncate">{b.incoming}</span>
            {isNonZeroAmount(b.incoming) && (
              <span className="shrink-0">
                <OrdersButton
                  size={22}
                  title={t("balances.activeOrdersTitle", {
                    account: b.account,
                  })}
                  href={activeOrdersHref(b.account)}
                  onClick={openActiveOrders}
                />
              </span>
            )}
          </div>
        </TableCell>
        <TableCell className="nums text-right text-xs">
          <DenominatedAmount
            value={b.averageEntryPrice}
            currency={b.accountCurrency}
          />
        </TableCell>
        <TableCell
          className={cn("nums text-right text-xs", pnlClass(b.realizedPnl))}
        >
          {/* A halted P&L holds no current number, so none is shown and there
              is nothing to denominate; otherwise the figure carries the
              account currency it is expressed in. */}
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
            <DenominatedAmount
              value={b.realizedPnl}
              currency={b.accountCurrency}
            />
          )}
        </TableCell>
        <TableCell className="text-xs text-muted-lt">
          <SplitTime iso={b.updatedAt} />
        </TableCell>
        <TableCell className="text-right">
          <RowActions align="flex-end">
            <ActionButton
              icon="edit"
              title={t("panel.openAriaLabel", {
                account: b.account,
                asset: b.asset,
              })}
              active={expanded}
              onClick={() => {
                onToggle();
              }}
            />
          </RowActions>
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
  accountSuggestions,
  assetSuggestions,
  onToggle,
  onClose,
  onApplied,
}: {
  defaultAccount: string;
  defaultAsset: string;
  expanded: boolean;
  accountSuggestions: string[];
  assetSuggestions: string[];
  onToggle: () => void;
  onClose: () => void;
  onApplied: () => void;
}) {
  const { t } = useTranslation("positions");

  return (
    <>
      <TableRow
        className={cn("hover:bg-transparent", expanded && "bg-accent-dim")}
      >
        <TableCell
          className="text-xs italic text-muted-lt"
          colSpan={BALANCE_TABLE_COLS - 1}
        >
          {t("inline.newPosition")}
        </TableCell>
        <TableCell className="text-right">
          <RowActions align="flex-end">
            <ActionButton
              icon="edit"
              title={t("panel.openDraftAriaLabel")}
              active={expanded}
              onClick={onToggle}
            />
          </RowActions>
        </TableCell>
      </TableRow>
      {expanded && (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={BALANCE_TABLE_COLS} className="p-0">
            <AdjustmentPanel
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
  activeSort,
  activeOrder,
  defaultDraftAccount,
  defaultDraftAsset,
  draftOpenRequest,
  accountSuggestions,
  assetSuggestions,
  onSortChange,
  onApplied,
  onShowHistory,
  onFilterAccount,
  onFilterAsset,
}: {
  balances: Balance[];
  activeSort?: string;
  activeOrder?: SortOrder;
  defaultDraftAccount: string;
  defaultDraftAsset: string;
  draftOpenRequest: number;
  accountSuggestions: string[];
  assetSuggestions: string[];
  onSortChange: (sort?: string, order?: SortOrder) => void;
  onApplied: () => void;
  onShowHistory: (account: string, asset: string) => void;
  onFilterAccount: (account: string) => void;
  onFilterAsset: (asset: string) => void;
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
          <TableHead>
            <SortableHeader
              field="account"
              label={t("balances.columns.account")}
              description={t("balances.columnDescriptions.account")}
              direction={sortDirection(activeSort, activeOrder, "account")}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead>
            <SortableHeader
              field="asset"
              label={t("balances.columns.asset")}
              description={t("balances.columnDescriptions.asset")}
              direction={sortDirection(activeSort, activeOrder, "asset")}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead className="text-right">
            <SortableHeader
              field="available"
              label={t("balances.columns.available")}
              description={t("balances.columnDescriptions.available")}
              direction={sortDirection(activeSort, activeOrder, "available")}
              align="right"
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead className="text-right">
            <SortableHeader
              field="held"
              label={t("balances.columns.held")}
              description={t("balances.columnDescriptions.held")}
              direction={sortDirection(activeSort, activeOrder, "held")}
              align="right"
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead className="text-right">
            <SortableHeader
              field="incoming"
              label={t("balances.columns.incoming")}
              description={t("balances.columnDescriptions.incoming")}
              direction={sortDirection(activeSort, activeOrder, "incoming")}
              align="right"
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead className="text-right">
            <ColumnHeader
              align="right"
              description={t("balances.columnDescriptions.avgEntryPrice")}
            >
              {t("balances.columns.avgEntryPrice")}
            </ColumnHeader>
          </TableHead>
          <TableHead className="text-right">
            <ColumnHeader
              align="right"
              description={t("balances.columnDescriptions.realizedPnl")}
            >
              {t("balances.columns.realizedPnl")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <SortableHeader
              field="updatedAt"
              label={t("balances.columns.updated")}
              description={t("balances.columnDescriptions.updated")}
              direction={sortDirection(activeSort, activeOrder, "updatedAt")}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead className="text-right">
            <ColumnHeader
              align="right"
              description={t("balances.columnDescriptions.adjust")}
            >
              {t("balances.columns.adjust")}
            </ColumnHeader>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        <BalanceDraftRow
          defaultAccount={defaultDraftAccount}
          defaultAsset={defaultDraftAsset}
          expanded={openKey === DRAFT_ADJUSTMENT_KEY}
          accountSuggestions={accountSuggestions}
          assetSuggestions={assetSuggestions}
          onToggle={() =>
            setOpenKey((current) =>
              current === DRAFT_ADJUSTMENT_KEY ? null : DRAFT_ADJUSTMENT_KEY,
            )
          }
          onClose={() => setOpenKey(null)}
          onApplied={onApplied}
        />
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
            onShowHistory={onShowHistory}
            onFilterAccount={onFilterAccount}
            onFilterAsset={onFilterAsset}
            accountSuggestions={accountSuggestions}
            assetSuggestions={assetSuggestions}
            onApplied={onApplied}
          />
        ))}
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
    const haltText = pnlHaltText(t, accepted.realizedPnlHaltReason ?? "");
    const rows: {
      label: string;
      request: AdjustmentAmount | undefined;
      delta: string;
      result: string;
    }[] = [];
    if (accepted.balanceDelta || accepted.balanceResult) {
      rows.push({
        label: t("dialog.outcome.fieldBalance"),
        request: adjustment.request.balance,
        delta: accepted.balanceDelta,
        result: accepted.balanceResult,
      });
    }
    if (accepted.heldDelta || accepted.heldResult) {
      rows.push({
        label: t("dialog.outcome.fieldHeld"),
        request: adjustment.request.held,
        delta: accepted.heldDelta,
        result: accepted.heldResult,
      });
    }
    if (accepted.incomingDelta || accepted.incomingResult) {
      rows.push({
        label: t("dialog.outcome.fieldIncoming"),
        request: adjustment.request.incoming,
        delta: accepted.incomingDelta,
        result: accepted.incomingResult,
      });
    }
    if (accepted.realizedPnlResult) {
      const realizedPnl = adjustmentResultParts(accepted.realizedPnlResult);
      rows.push({
        label: t("dialog.outcome.fieldRealizedPnl"),
        request: {
          mode: "absolute",
          value: adjustment.request.realizedPnl ?? "",
        },
        delta: realizedPnl.delta,
        result: realizedPnl.result,
      });
    }
    return (
      <div
        className={cn(
          "rounded-card border p-3 text-xs",
          haltText
            ? "border-[var(--warn)] bg-[var(--warn-dim)]"
            : "border-[var(--ok)] bg-[var(--ok-dim)]",
        )}
      >
        <p
          className={cn(
            "font-medium",
            haltText ? "text-[var(--warn)]" : "text-[var(--ok)]",
          )}
        >
          {t("dialog.outcome.acceptedTitle")}
        </p>
        {rows.length > 0 && (
          <div className="mt-2 space-y-1">
            {rows.map((r) => (
              <div key={r.label} className="flex gap-2">
                <span className="w-28 text-muted">{r.label}</span>
                <span className="nums text-text">
                  {outcomeAmountText(r.request, r.delta, r.result)}
                </span>
              </div>
            ))}
          </div>
        )}
        {haltText && (
          <p
            className="mt-2 text-[var(--warn)]"
            aria-label={haltText}
            role="note"
          >
            {haltText}
          </p>
        )}
      </div>
    );
  }
  return null;
}

// ---------------------------------------------------------------------------
// Amount field row - mode toggle + value input
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
              <SelectItem value="absolute">
                {t("dialog.mode.absolute")}
              </SelectItem>
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
            onClear={() => onChange({ ...field, value: "" })}
            clearLabel={t("common:filters.clearField")}
          />
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Bounds field row - optional lower / upper
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
            <Label className="text-[0.6875rem] text-muted">
              {t("dialog.bounds.lower")}
            </Label>
            <NumberStepper
              value={field.lower}
              min={null}
              spellCheck={false}
              placeholder="-"
              inputClassName="h-7 text-xs"
              onChange={(lower) => onChange({ ...field, lower })}
              onClear={() => onChange({ ...field, lower: "" })}
              clearLabel={t("common:filters.clearField")}
            />
          </div>
          <div className="space-y-1">
            <Label className="text-[0.6875rem] text-muted">
              {t("dialog.bounds.upper")}
            </Label>
            <NumberStepper
              value={field.upper}
              min={null}
              spellCheck={false}
              placeholder="-"
              inputClassName="h-7 text-xs"
              onChange={(upper) => onChange({ ...field, upper })}
              onClear={() => onChange({ ...field, upper: "" })}
              clearLabel={t("common:filters.clearField")}
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
  initialRealizedPnl?: string;
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
  initialRealizedPnl,
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
  const seedAmount = (
    mode: AdjustmentMode | undefined,
    value: string | undefined,
  ): AmountFieldState =>
    value ? { enabled: true, mode: mode ?? "absolute", value } : emptyAmount();

  const seedBounds = (
    lower: string | undefined,
    upper: string | undefined,
  ): BoundsFieldState =>
    lower || upper
      ? { enabled: true, lower: lower ?? "", upper: upper ?? "" }
      : emptyBounds();

  const [account, setAccount] = useState(initialAccount);
  const [asset, setAsset] = useState(initialAsset);
  const [avgPrice, setAvgPrice] = useState(initialAvgPrice ?? "");
  const [realizedPnl, setRealizedPnl] = useState(initialRealizedPnl ?? "");
  const [balance, setBalance] = useState<AmountFieldState>(() =>
    seedAmount(initialBalanceMode, initialBalanceValue),
  );
  const [held, setHeld] = useState<AmountFieldState>(() =>
    seedAmount(initialHeldMode, initialHeldValue),
  );
  const [incoming, setIncoming] = useState<AmountFieldState>(() =>
    seedAmount(initialIncomingMode, initialIncomingValue),
  );
  const [balanceBounds, setBalanceBounds] = useState<BoundsFieldState>(() =>
    seedBounds(initialBalanceBoundsLower, initialBalanceBoundsUpper),
  );
  const [heldBounds, setHeldBounds] = useState<BoundsFieldState>(() =>
    seedBounds(initialHeldBoundsLower, initialHeldBoundsUpper),
  );
  const [incomingBounds, setIncomingBounds] = useState<BoundsFieldState>(() =>
    seedBounds(initialIncomingBoundsLower, initialIncomingBoundsUpper),
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<Adjustment | null>(null);
  const [missingAccountConfirmOpen, setMissingAccountConfirmOpen] =
    useState(false);
  const localAccountSuggestions = useAccountCodeSuggestions(account, open);
  const localAssetSuggestionResult = useAssetCodeSuggestions(asset, open);
  const mergedAccountSuggestions = useMemo(
    () => mergeCodeSuggestions(accountSuggestions, localAccountSuggestions),
    [accountSuggestions, localAccountSuggestions],
  );
  const mergedAssetSuggestions = useMemo(
    () =>
      mergeCodeSuggestions(assetSuggestions, localAssetSuggestionResult.codes),
    [assetSuggestions, localAssetSuggestionResult.codes],
  );

  // Reseed whenever the dialog opens.
  useEffect(() => {
    if (open) {
      // Reset all form fields from props each time the dialog opens.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setAccount(initialAccount);
      setAsset(initialAsset);
      setAvgPrice(initialAvgPrice ?? "");
      setRealizedPnl(initialRealizedPnl ?? "");
      setBalance(seedAmount(initialBalanceMode, initialBalanceValue));
      setHeld(seedAmount(initialHeldMode, initialHeldValue));
      setIncoming(seedAmount(initialIncomingMode, initialIncomingValue));
      setBalanceBounds(
        seedBounds(initialBalanceBoundsLower, initialBalanceBoundsUpper),
      );
      setHeldBounds(seedBounds(initialHeldBoundsLower, initialHeldBoundsUpper));
      setIncomingBounds(
        seedBounds(initialIncomingBoundsLower, initialIncomingBoundsUpper),
      );
      setBusy(false);
      setError(null);
      setOutcome(null);
      setMissingAccountConfirmOpen(false);
    }
  }, [
    open,
    initialAccount,
    initialAsset,
    initialBalanceMode,
    initialBalanceValue,
    initialHeldMode,
    initialHeldValue,
    initialIncomingMode,
    initialIncomingValue,
    initialAvgPrice,
    initialRealizedPnl,
    initialBalanceBoundsLower,
    initialBalanceBoundsUpper,
    initialHeldBoundsLower,
    initialHeldBoundsUpper,
    initialIncomingBoundsLower,
    initialIncomingBoundsUpper,
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
  const hasModalAdjustmentChange =
    hasValue(avgPrice) ||
    (balance.enabled && hasValue(balance.value)) ||
    (held.enabled && hasValue(held.value)) ||
    (incoming.enabled && hasValue(incoming.value)) ||
    (balanceBounds.enabled &&
      (hasValue(balanceBounds.lower) || hasValue(balanceBounds.upper))) ||
    (heldBounds.enabled &&
      (hasValue(heldBounds.lower) || hasValue(heldBounds.upper))) ||
    (incomingBounds.enabled &&
      (hasValue(incomingBounds.lower) || hasValue(incomingBounds.upper)));
  const hasModalChanges = hasModalAdjustmentChange || hasValue(realizedPnl);
  const amountFieldValid = (field: AmountFieldState) =>
    !field.enabled || !hasValue(field.value) || isDecimal(field.value);
  const boundsFieldValid = (field: BoundsFieldState) =>
    !field.enabled ||
    ((!hasValue(field.lower) || isDecimal(field.lower)) &&
      (!hasValue(field.upper) || isDecimal(field.upper)));
  const modalFieldsValid =
    (!hasValue(avgPrice) || isDecimal(avgPrice)) &&
    (!hasValue(realizedPnl) || isDecimal(realizedPnl)) &&
    amountFieldValid(balance) &&
    amountFieldValid(held) &&
    amountFieldValid(incoming) &&
    boundsFieldValid(balanceBounds) &&
    boundsFieldValid(heldBounds) &&
    boundsFieldValid(incomingBounds);

  const resetAllFields = () => {
    setAccount("");
    setAsset("");
    setAvgPrice("");
    setRealizedPnl("");
    setBalance(emptyAmount());
    setHeld(emptyAmount());
    setIncoming(emptyAmount());
    setBalanceBounds(emptyBounds());
    setHeldBounds(emptyBounds());
    setIncomingBounds(emptyBounds());
    setError(null);
    setOutcome(null);
    setMissingAccountConfirmOpen(false);
  };

  const submit = async (missingAccount: MissingAccountPolicy = "reject") => {
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
    if (!hasModalChanges) {
      setError(t("panel.noChangesError"));
      return;
    }
    if (!modalFieldsValid) {
      setError(t("panel.invalidDecimal"));
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
      if (realizedPnl.trim()) {
        body.realizedPnl = realizedPnl.trim();
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
      const result = await createAdjustment(trimAccount, body, missingAccount);
      setOutcome(result);
      onDone();
    } catch (err) {
      if (
        missingAccount === "reject" &&
        err instanceof ApiError &&
        err.code === "account_missing"
      ) {
        setMissingAccountConfirmOpen(true);
      } else {
        setError(errMessage(err));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("dialog.title")}</DialogTitle>
            <DialogDescription>{t("dialog.description")}</DialogDescription>
          </DialogHeader>

          <div className="space-y-4">
            {/* Account + asset */}
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="adj-account">
                  {t("dialog.fields.account")}
                </Label>
                <Autocomplete
                  id="adj-account"
                  value={account}
                  spellCheck={false}
                  placeholder="acc-1"
                  suggestions={mergedAccountSuggestions}
                  onChange={setAccount}
                  disabled={busy}
                  onClear={() => setAccount("")}
                  clearLabel={t("common:filters.clearField")}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="adj-asset">{t("dialog.fields.asset")}</Label>
                <Autocomplete
                  id="adj-asset"
                  value={asset}
                  spellCheck={false}
                  placeholder="AAPL"
                  suggestions={mergedAssetSuggestions}
                  onChange={setAsset}
                  disabled={busy}
                  onClear={() => setAsset("")}
                  clearLabel={t("common:filters.clearField")}
                />
                <AssetCodeSuggestionFailure
                  failed={localAssetSuggestionResult.failed}
                />
              </div>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="adj-realized-pnl">
                {t("dialog.fields.realizedPnl")}
              </Label>
              <NumberStepper
                id="adj-realized-pnl"
                value={realizedPnl}
                min={null}
                spellCheck={false}
                placeholder={t("dialog.fields.realizedPnlPlaceholder")}
                inputClassName="text-xs"
                disabled={busy}
                onChange={setRealizedPnl}
                onClear={() => setRealizedPnl("")}
                clearLabel={t("common:filters.clearField")}
              />
            </div>

            {/* Average entry price */}
            <div className="space-y-1.5">
              <Label htmlFor="adj-aep">
                {t("dialog.fields.avgEntryPrice")}
              </Label>
              <NumberStepper
                id="adj-aep"
                value={avgPrice}
                spellCheck={false}
                placeholder="e.g. 142.50"
                inputClassName="text-xs"
                disabled={busy}
                onChange={setAvgPrice}
                onClear={() => setAvgPrice("")}
                clearLabel={t("common:filters.clearField")}
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
              <AmountField
                id="balance"
                label={t("dialog.amounts.balance")}
                field={balance}
                onChange={setBalance}
              />
              <AmountField
                id="held"
                label={t("dialog.amounts.held")}
                field={held}
                onChange={setHeld}
              />
              <AmountField
                id="incoming"
                label={t("dialog.amounts.incoming")}
                field={incoming}
                onChange={setIncoming}
              />
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
              {outcome
                ? t("dialog.footer.close")
                : t("actions.cancel", { ns: "common" })}
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={resetAllFields}
              disabled={busy}
            >
              {t("actions.clearAll", { ns: "common" })}
            </Button>
            <Button
              size="sm"
              onClick={() => void submit()}
              disabled={busy || !hasModalChanges || !modalFieldsValid}
            >
              {t("dialog.footer.submit")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <AlertDialog
        open={missingAccountConfirmOpen}
        onOpenChange={setMissingAccountConfirmOpen}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("missingAccountConfirm.title")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("missingAccountConfirm.description", {
                account: account.trim(),
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>
              {t("actions.cancel", { ns: "common" })}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                setMissingAccountConfirmOpen(false);
                void submit("create");
              }}
              disabled={busy}
            >
              {t("missingAccountConfirm.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

// ---------------------------------------------------------------------------
// Adjustment history row - inline outcome
// ---------------------------------------------------------------------------

function HistoryRowOutcome({ adj }: { adj: Adjustment }) {
  const { t } = useTranslation("positions");
  const { accepted, rejected } = adj;
  if (rejected) {
    return (
      <span className="text-[var(--danger)]">{rejected.reason || "-"}</span>
    );
  }
  if (accepted) {
    const haltText = pnlHaltText(t, accepted.realizedPnlHaltReason ?? "");
    const parts: string[] = [];
    if (accepted.balanceDelta || accepted.balanceResult) {
      parts.push(
        `${t("dialog.outcome.fieldBalance")} ${outcomeAmountText(
          adj.request.balance,
          accepted.balanceDelta,
          accepted.balanceResult,
        )}`,
      );
    }
    if (accepted.heldDelta || accepted.heldResult) {
      parts.push(
        `${t("dialog.outcome.fieldHeld")} ${outcomeAmountText(
          adj.request.held,
          accepted.heldDelta,
          accepted.heldResult,
        )}`,
      );
    }
    if (accepted.incomingDelta || accepted.incomingResult) {
      parts.push(
        `${t("dialog.outcome.fieldIncoming")} ${outcomeAmountText(
          adj.request.incoming,
          accepted.incomingDelta,
          accepted.incomingResult,
        )}`,
      );
    }
    if (accepted.realizedPnlResult) {
      const realizedPnl = adjustmentResultParts(accepted.realizedPnlResult);
      parts.push(
        `${t("dialog.outcome.fieldRealizedPnl")} ${outcomeAmountText(
          { mode: "absolute", value: adj.request.realizedPnl ?? "" },
          realizedPnl.delta,
          realizedPnl.result,
        )}`,
      );
    }
    if (haltText) {
      parts.push(haltText);
    }
    return (
      <span className={haltText ? "text-[var(--warn)]" : "nums text-text"}>
        {parts.length > 0 ? parts.join(" · ") : "-"}
      </span>
    );
  }
  return <span className="text-muted-lt">-</span>;
}

function HistoryRow({
  adj,
  onClone,
  onFilterAccount,
  onFilterAsset,
}: {
  adj: Adjustment;
  onClone: (adj: Adjustment) => void;
  onFilterAccount: (account: string) => void;
  onFilterAsset: (asset: string) => void;
}) {
  const { t } = useTranslation("positions");
  const { t: tc } = useTranslation();
  const isRejected = !!adj.rejected;
  const isAccepted = !!adj.accepted && !isRejected;
  const hasPnlHalt = !!adj.accepted?.realizedPnlHaltReason;

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
    reqParts.push(
      t("history.request.avgPrice", { value: req.averageEntryPrice }),
    );
  }
  if (req.realizedPnl) {
    reqParts.push(t("history.request.realizedPnl", { value: req.realizedPnl }));
  }

  return (
    <TableRow className="hover:bg-transparent">
      <TableCell className="text-xs text-muted-lt">
        <SplitTime iso={adj.at} />
      </TableCell>
      <TableCell className="nums text-xs">
        <div className="flex min-w-0 items-center gap-1">
          <IdCell
            value={adj.account}
            copyTitle={t("common:rowActions.copyId")}
            copiedTitle={t("common:rowActions.copiedId")}
          />
          <span className="ml-auto flex shrink-0 items-center">
            <FilterByButton
              size={28}
              title={tc("rowActions.filterByTitle", { field: adj.account })}
              href={positionsFilterHref({
                tab: "history",
                account: adj.account,
              })}
              onClick={() => onFilterAccount(adj.account)}
            />
          </span>
        </div>
      </TableCell>
      <TableCell className="nums text-xs">
        <div className="flex min-w-0 items-center gap-1">
          <span className="min-w-0 truncate">{adj.asset}</span>
          <span className="ml-auto flex shrink-0 items-center">
            <FilterByButton
              size={28}
              title={tc("rowActions.filterByTitle", { field: adj.asset })}
              href={positionsFilterHref({ tab: "history", asset: adj.asset })}
              onClick={() => onFilterAsset(adj.asset)}
            />
          </span>
        </div>
      </TableCell>
      <TableCell className="w-[var(--positions-source-column-width)]">
        <Badge variant="neutral" className="text-[0.6875rem]">
          {adj.source}
        </Badge>
      </TableCell>
      <TableCell className="nums text-xs text-muted-lt">
        {reqParts.length > 0 ? reqParts.join(" · ") : "-"}
      </TableCell>
      <TableCell className="w-[var(--positions-status-column-width)]">
        {isRejected ? (
          <Badge variant="danger">{t("history.status.rejected")}</Badge>
        ) : isAccepted ? (
          <Badge variant={hasPnlHalt ? "warn" : "ok"}>
            {t("history.status.accepted")}
          </Badge>
        ) : (
          <Badge variant="neutral">{adj.status}</Badge>
        )}
      </TableCell>
      <TableCell className="text-xs">
        <HistoryRowOutcome adj={adj} />
      </TableCell>
      <TableCell className="text-muted-lt">
        <IdCell
          value={adj.id}
          copyTitle={t("common:rowActions.copyId")}
          copiedTitle={t("common:rowActions.copiedId")}
        />
      </TableCell>
      <TableCell className="text-right">
        <RowActions align="flex-end">
          <CloneButton
            title={tc("rowActions.cloneTitle", { entity: adj.id })}
            onClick={() => onClone(adj)}
          />
        </RowActions>
      </TableCell>
    </TableRow>
  );
}

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

const SOURCES: Source[] = ["panel", "api", "mcp", "system"];
type PositionsTab = "positions" | "history";

const BALANCE_SORT_KEYS = new Set([
  "account",
  "asset",
  "available",
  "held",
  "incoming",
  "updatedAt",
]);
const HISTORY_SORT_KEYS = new Set([
  "account",
  "asset",
  "at",
  "principal",
  "source",
  "status",
]);

function sortFromParams(
  params: URLSearchParams,
  allowed: Set<string>,
  fallback: { sort?: string; order?: SortOrder },
): { sort?: string; order?: SortOrder } {
  const sort = params.get("sort");
  const order = params.get("order");
  if (
    sort !== null &&
    allowed.has(sort) &&
    (order === "asc" || order === "desc")
  ) {
    return { sort, order };
  }
  return fallback;
}

export function Positions() {
  const { t } = useTranslation("positions");
  const { t: tc } = useTranslation("common");
  const { fetchAccounts, fetchAdjustmentsPage, fetchAssets, fetchGroups } =
    useOfficerApi();
  const [searchParams] = useSearchParams();
  const globalAccountFilter = useGlobalAccountFilter();
  const initialAccount =
    globalAccountFilter.account || searchParams.get("account") || "";
  const initialGroup = searchParams.get("group") ?? "";
  const initialAsset = searchParams.get("asset") ?? "";
  const initialTab: PositionsTab =
    searchParams.get("tab") === "history" ? "history" : "positions";

  const [tab, setTab] = useState<PositionsTab>(initialTab);
  const [accountFilter, setAccountFilter] = useState(initialAccount);
  const [groupFilter, setGroupFilter] = useState(initialGroup);
  const [assetFilter, setAssetFilter] = useState(initialAsset);
  const [accountDraft, setAccountDraft] = useState(initialAccount);
  const [groupDraft, setGroupDraft] = useState(initialGroup);
  const [assetDraft, setAssetDraft] = useState(initialAsset);
  const [sourceFilter, setSourceFilter] = useState<Source | "__all__">(() => {
    const source = searchParams.get("source");
    return SOURCES.includes(source as Source) ? (source as Source) : "__all__";
  });
  const [historyExternalId, setHistoryExternalId] = useState(
    searchParams.get("id") ?? "",
  );
  const [appliedHistoryExternalId, setAppliedHistoryExternalId] = useState(
    searchParams.get("id") ?? "",
  );
  const [historyStatusFilter, setHistoryStatusFilter] = useState<
    Adjustment["status"] | "__all__"
  >(() => {
    const status = searchParams.get("status");
    return status === "accepted" || status === "rejected" ? status : "__all__";
  });
  const [historyAtMode, setHistoryAtMode] = useState<RangeFilterMode>(
    (searchParams.get("atMode") as RangeFilterMode | null) ?? "after",
  );
  const [historyAtMin, setHistoryAtMin] = useState(
    searchParams.get("atMin") ?? "",
  );
  const [historyAtMax, setHistoryAtMax] = useState(
    searchParams.get("atMax") ?? "",
  );
  const [draftOpenRequest, setDraftOpenRequest] = useState(0);
  const [balancePage, setBalancePage] = useState(0);
  const [historyPage, setHistoryPage] = useState(0);
  const [balanceSize, setBalanceSize] = usePersistentPageSize(
    "pit-officer-positions-page-size",
  );
  const [historySize, setHistorySize] = usePersistentPageSize(
    "pit-officer-position-history-page-size",
  );
  const [balanceSort, setBalanceSort] = useState<{
    sort?: string;
    order?: SortOrder;
  }>(() =>
    initialTab === "positions"
      ? sortFromParams(searchParams, BALANCE_SORT_KEYS, {
          sort: "account",
          order: "asc",
        })
      : { sort: "account", order: "asc" },
  );
  const [historySort, setHistorySort] = useState<{
    sort?: string;
    order?: SortOrder;
  }>(() =>
    initialTab === "history"
      ? sortFromParams(searchParams, HISTORY_SORT_KEYS, {
          sort: "at",
          order: "desc",
        })
      : { sort: "at", order: "desc" },
  );
  const [balanceRanges, setBalanceRanges] = useState(() =>
    balanceRangesFromParams(searchParams),
  );
  const [balanceRangeDrafts, setBalanceRangeDrafts] = useState(() =>
    balanceRangesFromParams(searchParams),
  );
  const advancedFilterDialogRef = useRef<HTMLDivElement | null>(null);
  const [moreFiltersOpen, setMoreFiltersOpen] = useState(false);
  const [historyExportBusy, setHistoryExportBusy] = useState(false);
  const [historyExportError, setHistoryExportError] = useState<string | null>(
    null,
  );

  const resetHistoryPage = () => {
    setHistoryPage(0);
  };

  useEffect(() => {
    const account = globalAccountFilter.account;
    if (account === "") {
      return;
    }
    // Mirror the external global-account store into this page-local filter.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setAccountDraft(account);
    setAccountFilter(account);
    setBalancePage(0);
    resetHistoryPage();
  }, [globalAccountFilter.account]);

  const [adjustOpen, setAdjustOpen] = useState(false);
  const [adjustAccount, setAdjustAccount] = useState("");
  const [adjustAsset, setAdjustAsset] = useState("");
  const [adjustBalanceMode, setAdjustBalanceMode] =
    useState<AdjustmentMode>("absolute");
  const [adjustBalanceValue, setAdjustBalanceValue] = useState("");
  // Extended clone-prefill props (undefined = leave empty / disabled).
  const [adjustHeldMode, setAdjustHeldMode] = useState<
    AdjustmentMode | undefined
  >(undefined);
  const [adjustHeldValue, setAdjustHeldValue] = useState<string | undefined>(
    undefined,
  );
  const [adjustIncomingMode, setAdjustIncomingMode] = useState<
    AdjustmentMode | undefined
  >(undefined);
  const [adjustIncomingValue, setAdjustIncomingValue] = useState<
    string | undefined
  >(undefined);
  const [adjustAvgPrice, setAdjustAvgPrice] = useState<string | undefined>(
    undefined,
  );
  const [adjustRealizedPnl, setAdjustRealizedPnl] = useState<
    string | undefined
  >(undefined);
  const [adjustBalanceBoundsLower, setAdjustBalanceBoundsLower] = useState<
    string | undefined
  >(undefined);
  const [adjustBalanceBoundsUpper, setAdjustBalanceBoundsUpper] = useState<
    string | undefined
  >(undefined);
  const [adjustHeldBoundsLower, setAdjustHeldBoundsLower] = useState<
    string | undefined
  >(undefined);
  const [adjustHeldBoundsUpper, setAdjustHeldBoundsUpper] = useState<
    string | undefined
  >(undefined);
  const [adjustIncomingBoundsLower, setAdjustIncomingBoundsLower] = useState<
    string | undefined
  >(undefined);
  const [adjustIncomingBoundsUpper, setAdjustIncomingBoundsUpper] = useState<
    string | undefined
  >(undefined);

  const deferredAccountDraft = useDebouncedValue(
    accountDraft.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const deferredGroupDraft = useDebouncedValue(
    groupDraft.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const deferredAssetDraft = useDebouncedValue(
    assetDraft.trim(),
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const deferredAccount = accountFilter;
  const deferredGroup = groupFilter;
  const deferredAsset = assetFilter;
  const debouncedBalanceRanges = useDebouncedValue(
    balanceRanges,
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedHistoryAtMin = useDebouncedValue(
    historyAtMin,
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedHistoryAtMax = useDebouncedValue(
    historyAtMax,
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );

  const balanceListFilters = useMemo<BalanceListFilters>(() => {
    const filter: BalanceListFilters = {
      account: deferredAccount || undefined,
      asset: deferredAsset || undefined,
      groupCode: deferredGroup || undefined,
      limit: balanceSize,
      offset: balancePage * balanceSize,
      sort: balanceSort.sort,
      order: balanceSort.order,
    };
    addBalanceRange(filter, "available", debouncedBalanceRanges.available);
    addBalanceRange(filter, "held", debouncedBalanceRanges.held);
    addBalanceRange(filter, "incoming", debouncedBalanceRanges.incoming);
    addBalanceRange(
      filter,
      "averageEntryPrice",
      debouncedBalanceRanges.averageEntryPrice,
    );
    addBalanceRange(filter, "realizedPnl", debouncedBalanceRanges.realizedPnl);
    addUpdatedAtRange(filter, debouncedBalanceRanges.updatedAt);
    return filter;
  }, [
    balancePage,
    balanceSize,
    balanceSort.order,
    balanceSort.sort,
    debouncedBalanceRanges,
    deferredAccount,
    deferredAsset,
    deferredGroup,
  ]);
  const balancesLoad = useBalancesPage(balanceListFilters);

  const deferredSource = sourceFilter === "__all__" ? undefined : sourceFilter;
  const historyAtFrom = localDateTimeFilter(debouncedHistoryAtMin);
  const historyAtTo = localDateTimeFilter(debouncedHistoryAtMax);
  const requestHistoryAt =
    historyAtMode === "between"
      ? historyAtFrom !== "" && historyAtTo !== ""
        ? { atMode: historyAtMode, atMin: historyAtFrom, atMax: historyAtTo }
        : {}
      : historyAtMode === "before"
        ? historyAtFrom !== ""
          ? { atMode: historyAtMode, atMax: historyAtFrom }
          : {}
        : historyAtFrom !== ""
          ? { atMode: historyAtMode, atMin: historyAtFrom }
          : {};
  const adjustmentsLoad = useAdjustmentsPage({
    id: appliedHistoryExternalId.trim() || undefined,
    account: deferredAccount || undefined,
    asset: deferredAsset || undefined,
    source: deferredSource,
    status: historyStatusFilter === "__all__" ? undefined : historyStatusFilter,
    ...requestHistoryAt,
    sort: historySort.sort,
    order: historySort.order,
    limit: historySize,
    offset: historyPage * historySize,
  });

  const [accountSuggestions, setAccountSuggestions] = useState<string[]>([]);
  const [groupSuggestions, setGroupSuggestions] = useState<string[]>([]);
  const [assetSuggestions, setAssetSuggestions] = useState<string[]>([]);
  const accountCurrencySuggestions =
    useAccountCurrencySuggestions(moreFiltersOpen);
  const visibleAccountSuggestions =
    deferredAccountDraft.trim() === "" ? [] : accountSuggestions;
  const visibleGroupSuggestions =
    deferredGroupDraft.trim() === "" ? [] : groupSuggestions;
  const visibleAssetSuggestions =
    deferredAssetDraft.trim() === "" ? [] : assetSuggestions;

  useEffect(() => {
    const query = deferredAccountDraft.trim();
    if (query === "") {
      return;
    }
    const controller = new AbortController();
    void fetchAccounts(
      {
        code: query,
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      controller.signal,
    )
      .then((accounts) => {
        const next = accounts.map((account) => account.code);
        setAccountSuggestions((prev) =>
          sameStrings(prev, next) ? prev : next,
        );
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error(err);
          setAccountSuggestions((prev) => (prev.length === 0 ? prev : []));
        }
      });
    return () => controller.abort();
  }, [deferredAccountDraft, fetchAccounts]);

  useEffect(() => {
    const query = deferredGroupDraft.trim();
    if (query === "") {
      return;
    }
    const controller = new AbortController();
    void fetchGroups(
      {
        code: query,
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      controller.signal,
    )
      .then((groups) => {
        const next = groups.map((group) => group.code);
        setGroupSuggestions((prev) => (sameStrings(prev, next) ? prev : next));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error(err);
          setGroupSuggestions((prev) => (prev.length === 0 ? prev : []));
        }
      });
    return () => controller.abort();
  }, [deferredGroupDraft, fetchGroups]);

  useEffect(() => {
    const query = deferredAssetDraft.trim();
    if (query === "") {
      return;
    }
    const controller = new AbortController();
    void fetchAssets(
      {
        code: query,
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      controller.signal,
    )
      .then((assets) => {
        const next = assets.map((asset) => asset.code);
        setAssetSuggestions((prev) => (sameStrings(prev, next) ? prev : next));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error(err);
          setAssetSuggestions((prev) => (prev.length === 0 ? prev : []));
        }
      });
    return () => controller.abort();
  }, [deferredAssetDraft, fetchAssets]);

  const openNewAdjust = () => {
    setDraftOpenRequest((current) => current + 1);
  };

  // Clone an adjustment record - open dialog prefilled with all request fields.
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
    setAdjustRealizedPnl(req.realizedPnl);
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
  // Open this position's adjustment history, pre-filtered by account + asset.
  // The asset filter is applied client-side on the history list, so no new
  // server parameter is introduced.
  const showAccountAssetHistory = (account: string, asset: string) => {
    setAccountFilter(account);
    setAccountDraft(account);
    setAssetFilter(asset);
    setAssetDraft(asset);
    resetHistoryPage();
    setTab("history");
  };
  const filterHistoryAccount = (account: string) => {
    setAccountFilter(account);
    setAccountDraft(account);
    resetHistoryPage();
    setTab("history");
  };
  const filterHistoryAsset = (asset: string) => {
    setAssetFilter(asset);
    setAssetDraft(asset);
    resetHistoryPage();
    setTab("history");
  };
  const reloadActive = () => {
    if (tab === "positions") {
      balancesLoad.reload();
      return;
    }
    adjustmentsLoad.reload();
  };
  const updateBalanceRange = (
    key: BalanceRangeKey,
    next: BalanceRangeDraft,
  ) => {
    setBalanceRanges((current) => ({ ...current, [key]: next }));
    setBalancePage(0);
  };
  const updateBalanceRangeDraft = (
    key: BalanceRangeKey,
    next: BalanceRangeDraft,
  ) => {
    setBalanceRangeDrafts((current) => ({ ...current, [key]: next }));
  };
  const openAdvancedFilters = () => {
    setBalanceRangeDrafts(cloneBalanceRanges(balanceRanges));
    setMoreFiltersOpen(true);
  };
  const applyAdvancedFilters = () => {
    if (!reportInvalidFilterControls(advancedFilterDialogRef.current)) {
      return;
    }
    setBalanceRanges(cloneBalanceRanges(balanceRangeDrafts));
    setBalancePage(0);
    setMoreFiltersOpen(false);
  };
  const advancedNumericFiltersValid = NUMERIC_RANGE_KEYS.every((key) => {
    const draft = balanceRangeDrafts[key];
    const operator = draft.mode === "all" ? "eq" : draft.mode;
    return (
      isDecimalRangeValid(operator, draft.min, draft.max) &&
      !needsCurrency(isDenominatedRangeKey(key), draft)
    );
  });
  const filterDraftChanged =
    accountDraft.trim() !== accountFilter ||
    groupDraft.trim() !== groupFilter ||
    assetDraft.trim() !== assetFilter;
  const accountGlobalLocked =
    globalAccountFilter.account !== "" &&
    accountFilter.trim() === globalAccountFilter.account;
  const accountGlobalToggle = {
    active:
      accountDraft.trim() !== "" &&
      accountDraft.trim() === globalAccountFilter.account,
    disabled: accountDraft.trim() === "",
    activeLabel: tc("filters.globalAccount.active"),
    inactiveLabel: tc("filters.globalAccount.inactive"),
    disabledLabel: tc("filters.globalAccount.disabled"),
    onToggle: () => {
      const nextAccount = accountDraft.trim();
      if (nextAccount === "") {
        return;
      }
      if (globalAccountFilter.account === nextAccount) {
        globalAccountFilter.clear();
        return;
      }
      globalAccountFilter.setAccount(nextAccount);
      setAccountDraft(nextAccount);
      setAccountFilter(nextAccount);
      setBalancePage(0);
      resetHistoryPage();
    },
  };
  const applyIdentityFilters = () => {
    const nextAccount = accountDraft.trim();
    const nextGroup = groupDraft.trim();
    const nextAsset = assetDraft.trim();
    setAccountFilter(nextAccount);
    setGroupFilter(nextGroup);
    setAssetFilter(nextAsset);
    setBalancePage(0);
    resetHistoryPage();
  };
  const applyIdentityField = (
    field: "account" | "group" | "asset",
    value: string,
  ) => {
    const nextValue = value.trim();
    if (field === "account") {
      setAccountDraft(nextValue);
      setAccountFilter(nextValue);
      resetHistoryPage();
    } else if (field === "group") {
      setGroupDraft(nextValue);
      setGroupFilter(nextValue);
    } else {
      setAssetDraft(nextValue);
      setAssetFilter(nextValue);
      resetHistoryPage();
    }
    setBalancePage(0);
  };
  const applyIdentityFiltersOnEnter = (
    event: KeyboardEvent<HTMLInputElement>,
  ) => {
    if (event.key === "Enter") {
      applyIdentityFilters();
    }
  };
  const exportHistory = async () => {
    setHistoryExportBusy(true);
    setHistoryExportError(null);
    try {
      const page = await fetchAdjustmentsPage({
        account: deferredAccount || undefined,
        asset: deferredAsset || undefined,
        source: deferredSource,
        limit: MAX_LIST_LIMIT,
      });
      downloadCsv("position-adjustment-history.csv", [
        [
          "id",
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
        ...page.items.map(adjustmentCsvRow),
      ]);
    } catch (err) {
      setHistoryExportError(errMessage(err));
    } finally {
      setHistoryExportBusy(false);
    }
  };
  const positionCsvFilters = {
    account: accountFilter.trim() || undefined,
    asset: assetFilter.trim() || undefined,
    groupCode: groupFilter.trim() || undefined,
  };

  // Encode the active filter set (both tabs) as a shareable deep link. Only
  // non-default params are emitted, mirroring the API param names.
  const shareHref = useMemo(() => {
    const query = new URLSearchParams();
    if (tab === "history") {
      query.set("tab", "history");
    }
    if (appliedHistoryExternalId.trim() !== "") {
      query.set("id", appliedHistoryExternalId.trim());
    }
    if (accountFilter.trim() !== "") {
      query.set("account", accountFilter.trim());
    }
    if (groupFilter.trim() !== "") {
      query.set("group", groupFilter.trim());
    }
    if (assetFilter.trim() !== "") {
      query.set("asset", assetFilter.trim());
    }
    appendBalanceRanges(query, balanceRanges);
    if (sourceFilter !== "__all__") {
      query.set("source", sourceFilter);
    }
    if (historyStatusFilter !== "__all__") {
      query.set("status", historyStatusFilter);
    }
    if (historyAtMin.trim() !== "") {
      query.set("atMode", historyAtMode);
      query.set("atMin", historyAtMin.trim());
    }
    if (historyAtMax.trim() !== "") {
      query.set("atMode", historyAtMode);
      query.set("atMax", historyAtMax.trim());
    }
    const activeSort = tab === "history" ? historySort : balanceSort;
    if (activeSort.sort !== undefined) {
      query.set("sort", activeSort.sort);
      if (activeSort.order !== undefined) {
        query.set("order", activeSort.order);
      }
    }
    return shareUrl("/positions", query);
  }, [
    accountFilter,
    appliedHistoryExternalId,
    assetFilter,
    balanceRanges,
    groupFilter,
    historyAtMax,
    historyAtMin,
    historyAtMode,
    historySort,
    historyStatusFilter,
    balanceSort,
    sourceFilter,
    tab,
  ]);
  const balances =
    balancesLoad.load.state === "ready" ? balancesLoad.load.data.items : [];
  const pagedBalances = balances;
  const hasMoreBalances =
    balancesLoad.load.state === "ready" &&
    (balancePage + 1) * balanceSize < balancesLoad.load.data.total;
  const balancePager = (
    <TablePagination
      page={balancePage}
      canPrevious={balancePage > 0}
      canNext={hasMoreBalances}
      knownTotalPages={
        balancesLoad.load.state === "ready"
          ? knownPageCount(balancesLoad.load.data.total, balanceSize)
          : undefined
      }
      onPrevious={() => setBalancePage((p) => Math.max(0, p - 1))}
      onNext={() => setBalancePage((p) => p + 1)}
      onPage={setBalancePage}
    />
  );
  const adjustmentsPage =
    adjustmentsLoad.load.state === "ready" ? adjustmentsLoad.load.data : null;
  const pagedAdjustments = adjustmentsPage?.items ?? [];
  const historyPager = (
    <TablePagination
      page={historyPage}
      canPrevious={historyPage > 0}
      canNext={
        adjustmentsPage !== null &&
        (historyPage + 1) * historySize < adjustmentsPage.total
      }
      knownTotalPages={
        adjustmentsPage !== null
          ? knownPageCount(adjustmentsPage.total, historySize)
          : undefined
      }
      onPrevious={() => setHistoryPage((p) => Math.max(0, p - 1))}
      onNext={() => setHistoryPage((p) => p + 1)}
      onPage={setHistoryPage}
    />
  );
  const balanceRangeLabels: Record<BalanceRangeKey, string> = {
    available: t("balances.columns.available"),
    held: t("balances.columns.held"),
    incoming: t("balances.columns.incoming"),
    averageEntryPrice: t("balances.columns.avgEntryPrice"),
    realizedPnl: t("balances.columns.realizedPnl"),
    updatedAt: t("balances.columns.updated"),
  };
  const balanceRangeOperatorLabel = (
    key: BalanceRangeKey,
    mode: RangeFilterMode,
  ) => {
    if (mode === "greater_than" || mode === "less_than") {
      return t(`filters.range.${mode}`);
    }
    if (key === "updatedAt") {
      return tc(`operators.time.${mode}`);
    }
    return tc(`operators.number.${mode}`);
  };
  const activeFilterChips = [
    tab === "history" && appliedHistoryExternalId.trim() !== ""
      ? {
          key: "externalId",
          label: t("history.columns.externalId"),
          value: appliedHistoryExternalId.trim(),
          clear: () => {
            setHistoryExternalId("");
            setAppliedHistoryExternalId("");
            resetHistoryPage();
          },
        }
      : null,
    tab === "positions" && groupFilter.trim() !== ""
      ? {
          key: "group",
          label: t("filters.byGroup"),
          value: groupFilter.trim(),
          clear: () => {
            setGroupDraft("");
            setGroupFilter("");
            setBalancePage(0);
          },
        }
      : null,
    accountFilter.trim() !== ""
      ? {
          key: "account",
          label: t("filters.byAccount"),
          value: accountFilter.trim(),
          clear: () => {
            setAccountDraft("");
            setAccountFilter("");
            setBalancePage(0);
            resetHistoryPage();
          },
        }
      : null,
    assetFilter.trim() !== ""
      ? {
          key: "asset",
          label: t("filters.byAsset"),
          value: assetFilter.trim(),
          clear: () => {
            setAssetDraft("");
            setAssetFilter("");
            setBalancePage(0);
            resetHistoryPage();
          },
        }
      : null,
    tab === "history" && sourceFilter !== "__all__"
      ? {
          key: "source",
          label: t("history.sourceLabel"),
          value: sourceFilter,
          clear: () => {
            setSourceFilter("__all__");
            resetHistoryPage();
          },
        }
      : null,
    tab === "history" && historyStatusFilter !== "__all__"
      ? {
          key: "status",
          label: t("history.columns.status"),
          value: t(`history.status.${historyStatusFilter}`),
          clear: () => {
            setHistoryStatusFilter("__all__");
            resetHistoryPage();
          },
        }
      : null,
    tab === "history" &&
    (historyAtMin.trim() !== "" || historyAtMax.trim() !== "")
      ? {
          key: "historyAt",
          label: t("history.columns.time"),
          value: rangeChipValue(
            tc(`operators.time.${historyAtMode}`),
            historyAtMode,
            historyAtMin,
            historyAtMax,
          ),
          clear: () => {
            setHistoryAtMode("after");
            setHistoryAtMin("");
            setHistoryAtMax("");
            resetHistoryPage();
          },
        }
      : null,
    ...(tab === "positions"
      ? (Object.keys(balanceRanges) as BalanceRangeKey[])
          .filter((key) => hasActiveRange(balanceRanges[key]))
          .map((key) => {
            const draft = balanceRanges[key];
            return {
              key,
              label: balanceRangeLabels[key],
              value: rangeChipValue(
                balanceRangeOperatorLabel(key, draft.mode),
                draft.mode,
                draft.min,
                draft.max,
                isDenominatedRangeKey(key) ? draft.currency : "",
              ),
              clear: () =>
                updateBalanceRange(key, {
                  mode: "all",
                  min: "",
                  max: "",
                  currency: "",
                }),
            };
          })
      : []),
  ].filter(
    (
      entry,
    ): entry is {
      key: string;
      label: string;
      value: string;
      clear: () => void;
    } => entry !== null,
  );
  const visibleFilterChips =
    tab === "positions"
      ? activeFilterChips.filter((entry) =>
          [
            "available",
            "held",
            "incoming",
            "averageEntryPrice",
            "realizedPnl",
            "updatedAt",
          ].includes(entry.key),
        )
      : [];
  const advancedFilterCount = visibleFilterChips.length;
  const clearActiveFilters = () => {
    for (const entry of activeFilterChips) {
      if (entry.key === "account" && accountGlobalLocked) {
        continue;
      }
      entry.clear();
    }
  };

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
                resetHistoryPage();
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
                exports={[
                  {
                    entity: "positions",
                    filters: positionCsvFilters,
                    label: t("actions.exportCsv"),
                  },
                ]}
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
      <FilterBar
        active={activeFilterChips.length > 0}
        activeLabel={tc("filters.active")}
        onClearActive={clearActiveFilters}
        clearActiveLabel={tc("filters.clearAll")}
        chips={
          visibleFilterChips.length > 0 ? (
            <>
              {visibleFilterChips.map((entry) => (
                <FilterChip
                  key={entry.key}
                  label={`${entry.label}: ${entry.value}`}
                  removeLabel={tc("filters.removeAdvanced")}
                  onRemove={entry.clear}
                />
              ))}
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-6 text-[0.6875rem]"
                onClick={() => {
                  for (const entry of visibleFilterChips) {
                    entry.clear();
                  }
                }}
              >
                {tc("filters.removeAdvanced")}
              </Button>
            </>
          ) : undefined
        }
        trailing={
          <>
            {tab === "positions" && (
              <MoreFiltersButton
                count={advancedFilterCount}
                label={tc("filters.more")}
                onClick={openAdvancedFilters}
              />
            )}
            <div className="flex items-end">
              <ShareLinkButton
                href={shareHref}
                title={tc("rowActions.shareFilters")}
                copiedTitle={tc("rowActions.copiedLink")}
                size={32}
              />
            </div>
          </>
        }
        bottom={
          tab === "history" ? (
            <ExactIdField
              label={t("common:exactLookup.label")}
              value={historyExternalId}
              placeholder={t("common:exactLookup.placeholder", {
                entity: t("history.columns.externalId"),
              })}
              openLabel={t("common:exactLookup.open")}
              width={360}
              style={{ width: "100%" }}
              onChange={setHistoryExternalId}
              onOpen={(value) => {
                setAppliedHistoryExternalId(value.trim());
                resetHistoryPage();
              }}
            />
          ) : undefined
        }
      >
        {tab === "positions" && (
          <AutocompleteFilterField
            label={t("filters.byGroup")}
            value={groupDraft}
            placeholder="equity-desks"
            suggestions={visibleGroupSuggestions}
            onChange={setGroupDraft}
            onSuggestionSelect={(value) => applyIdentityField("group", value)}
            onKeyDown={applyIdentityFiltersOnEnter}
            onClear={() => {
              setGroupDraft("");
              setGroupFilter("");
              setBalancePage(0);
            }}
            clearLabel={t("common:filters.clearField")}
          />
        )}
        <AutocompleteFilterField
          label={t("filters.byAccount")}
          value={accountDraft}
          placeholder="acc-1"
          suggestions={visibleAccountSuggestions}
          onChange={(value) => {
            if (accountGlobalLocked && value.trim() === "") {
              globalAccountFilter.clear();
            }
            setAccountDraft(value);
          }}
          onSuggestionSelect={(value) => applyIdentityField("account", value)}
          onKeyDown={applyIdentityFiltersOnEnter}
          onClear={() => {
            if (accountGlobalLocked) {
              globalAccountFilter.clear();
            }
            setAccountDraft("");
            setAccountFilter("");
            setBalancePage(0);
            resetHistoryPage();
          }}
          clearLabel={t("common:filters.clearField")}
          globalToggle={accountGlobalToggle}
        />
        <AutocompleteFilterField
          label={t("filters.byAsset")}
          value={assetDraft}
          placeholder="AAPL"
          suggestions={visibleAssetSuggestions}
          width={160}
          onChange={setAssetDraft}
          onSuggestionSelect={(value) => applyIdentityField("asset", value)}
          onKeyDown={applyIdentityFiltersOnEnter}
          onClear={() => {
            setAssetDraft("");
            setAssetFilter("");
            setBalancePage(0);
            resetHistoryPage();
          }}
          clearLabel={t("common:filters.clearField")}
        />
        <div className="flex items-end">
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={applyIdentityFilters}
            disabled={!filterDraftChanged}
          >
            {tc("filters.apply")}
          </Button>
        </div>
        {tab === "history" && (
          <>
            <div className="grid gap-1">
              <FieldLabel>{t("history.sourceLabel")}</FieldLabel>
              <Segmented
                value={sourceFilter}
                options={[
                  { value: "__all__", label: t("history.sourceAll") },
                  ...SOURCES.map((source) => ({
                    value: source,
                    label: source,
                  })),
                ]}
                onChange={(value) => {
                  setSourceFilter(value as Source | "__all__");
                  resetHistoryPage();
                }}
              />
            </div>
            <div className="grid gap-1">
              <FieldLabel>{t("history.columns.status")}</FieldLabel>
              <Segmented
                value={historyStatusFilter}
                options={[
                  { value: "__all__", label: t("history.sourceAll") },
                  { value: "accepted", label: t("history.status.accepted") },
                  { value: "rejected", label: t("history.status.rejected") },
                ]}
                onChange={(value) => {
                  setHistoryStatusFilter(
                    value as Adjustment["status"] | "__all__",
                  );
                  resetHistoryPage();
                }}
              />
            </div>
            <div className="grid gap-1">
              <FieldLabel>{t("history.columns.time")}</FieldLabel>
              <TimeRangeFilter
                operator={historyAtMode}
                from={historyAtMin}
                to={historyAtMax}
                showPresets={false}
                operators={operatorOptions(tc, "time")}
                onOperatorChange={(value) => {
                  setHistoryAtMode(value as RangeFilterMode);
                  resetHistoryPage();
                }}
                onFromChange={(value) => {
                  setHistoryAtMin(value);
                  resetHistoryPage();
                }}
                onToChange={(value) => {
                  setHistoryAtMax(value);
                  resetHistoryPage();
                }}
                clearLabel={tc("filters.clearField")}
              />
            </div>
          </>
        )}
      </FilterBar>

      {tab === "positions" && (
        <Dialog
          open={moreFiltersOpen}
          onOpenChange={(next) => {
            if (next) {
              setBalanceRangeDrafts(cloneBalanceRanges(balanceRanges));
            }
            setMoreFiltersOpen(next);
          }}
        >
          <DialogContent className="max-w-2xl">
            <DialogHeader>
              <DialogTitle>{tc("filters.more")}</DialogTitle>
              <DialogDescription>
                {t("filters.advancedDescription")}
              </DialogDescription>
            </DialogHeader>
            <div ref={advancedFilterDialogRef} className="grid gap-3">
              <PositionRangeFilter
                label={t("balances.columns.available")}
                draft={balanceRangeDrafts.available}
                onChange={(next) => updateBalanceRangeDraft("available", next)}
              />
              <PositionRangeFilter
                label={t("balances.columns.held")}
                draft={balanceRangeDrafts.held}
                onChange={(next) => updateBalanceRangeDraft("held", next)}
              />
              <PositionRangeFilter
                label={t("balances.columns.incoming")}
                draft={balanceRangeDrafts.incoming}
                onChange={(next) => updateBalanceRangeDraft("incoming", next)}
              />
              <PositionRangeFilter
                label={t("balances.columns.avgEntryPrice")}
                draft={balanceRangeDrafts.averageEntryPrice}
                denominated
                currencySuggestions={accountCurrencySuggestions}
                onChange={(next) =>
                  updateBalanceRangeDraft("averageEntryPrice", next)
                }
              />
              <PositionRangeFilter
                label={t("balances.columns.realizedPnl")}
                draft={balanceRangeDrafts.realizedPnl}
                denominated
                currencySuggestions={accountCurrencySuggestions}
                onChange={(next) =>
                  updateBalanceRangeDraft("realizedPnl", next)
                }
              />
              <PositionRangeFilter
                label={t("balances.columns.updated")}
                draft={balanceRangeDrafts.updatedAt}
                inputType="datetime-local"
                onChange={(next) => updateBalanceRangeDraft("updatedAt", next)}
              />
            </div>
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  setBalanceRangeDrafts(
                    cloneBalanceRanges(EMPTY_BALANCE_RANGES),
                  );
                }}
              >
                {tc("filters.removeAdvanced")}
              </Button>
              <Button
                type="button"
                onClick={applyAdvancedFilters}
                disabled={!advancedNumericFiltersValid}
              >
                {tc("filters.applyAdvanced")}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}

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

          {balancesLoad.load.state === "loading" && <TableSkeleton cols={9} />}
          <StaleState {...balancesLoad} />
          {balancesLoad.load.state === "error" && (
            <ErrorState
              message={balancesLoad.load.error}
              onRetry={balancesLoad.reload}
            />
          )}
          {balancesLoad.load.state === "ready" && (
            <>
              {balances.length === 0 ? (
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
              ) : (
                balancePager
              )}
              <BalancesTable
                balances={pagedBalances}
                activeSort={balanceSort.sort}
                activeOrder={balanceSort.order}
                defaultDraftAccount={accountFilter.trim()}
                defaultDraftAsset={assetFilter.trim()}
                draftOpenRequest={draftOpenRequest}
                accountSuggestions={accountSuggestions}
                assetSuggestions={assetSuggestions}
                onSortChange={(sort, order) => {
                  setBalanceSort({ sort, order });
                  setBalancePage(0);
                }}
                onApplied={handleAdjustDone}
                onShowHistory={showAccountAssetHistory}
                onFilterAccount={(account) => {
                  setAccountFilter(account);
                  setBalancePage(0);
                }}
                onFilterAsset={(asset) => {
                  setAssetFilter(asset);
                  setBalancePage(0);
                }}
              />
              {balances.length > 0 && balancePager}
            </>
          )}
        </>
      )}

      {tab === "history" && (
        <>
          <p className="text-[0.6875rem] uppercase tracking-[0.07em] text-muted">
            {t("history.sectionLabel")}
          </p>

          {historyExportError && <ErrorBanner message={historyExportError} />}
          {adjustmentsLoad.load.state === "loading" && (
            <TableSkeleton cols={9} />
          )}
          <StaleState {...adjustmentsLoad} />
          {adjustmentsLoad.load.state === "error" && (
            <ErrorState
              message={adjustmentsLoad.load.error}
              onRetry={adjustmentsLoad.reload}
            />
          )}
          {adjustmentsLoad.load.state === "ready" &&
            (adjustmentsLoad.load.data.total === 0 ? (
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
                      <TableHead>
                        <SortableHeader
                          field="at"
                          label={t("history.columns.time")}
                          description={t("history.columnDescriptions.time")}
                          direction={sortDirection(
                            historySort.sort,
                            historySort.order,
                            "at",
                          )}
                          onSort={(field, next) => {
                            setHistorySort(
                              next === "none"
                                ? {}
                                : { sort: field, order: next },
                            );
                            resetHistoryPage();
                          }}
                        />
                      </TableHead>
                      <TableHead>
                        <SortableHeader
                          field="account"
                          label={t("history.columns.account")}
                          description={t("history.columnDescriptions.account")}
                          direction={sortDirection(
                            historySort.sort,
                            historySort.order,
                            "account",
                          )}
                          onSort={(field, next) => {
                            setHistorySort(
                              next === "none"
                                ? {}
                                : { sort: field, order: next },
                            );
                            resetHistoryPage();
                          }}
                        />
                      </TableHead>
                      <TableHead>
                        <SortableHeader
                          field="asset"
                          label={t("history.columns.asset")}
                          description={t("history.columnDescriptions.asset")}
                          direction={sortDirection(
                            historySort.sort,
                            historySort.order,
                            "asset",
                          )}
                          onSort={(field, next) => {
                            setHistorySort(
                              next === "none"
                                ? {}
                                : { sort: field, order: next },
                            );
                            resetHistoryPage();
                          }}
                        />
                      </TableHead>
                      <TableHead className="w-[var(--positions-source-column-width)]">
                        <SortableHeader
                          field="source"
                          label={t("history.columns.source")}
                          description={t("history.columnDescriptions.source")}
                          direction={sortDirection(
                            historySort.sort,
                            historySort.order,
                            "source",
                          )}
                          onSort={(field, next) => {
                            setHistorySort(
                              next === "none"
                                ? {}
                                : { sort: field, order: next },
                            );
                            resetHistoryPage();
                          }}
                        />
                      </TableHead>
                      <TableHead>
                        <ColumnHeader
                          description={t("history.columnDescriptions.request")}
                        >
                          {t("history.columns.request")}
                        </ColumnHeader>
                      </TableHead>
                      <TableHead className="w-[var(--positions-status-column-width)]">
                        <SortableHeader
                          field="status"
                          label={t("history.columns.status")}
                          description={t("history.columnDescriptions.status")}
                          direction={sortDirection(
                            historySort.sort,
                            historySort.order,
                            "status",
                          )}
                          onSort={(field, next) => {
                            setHistorySort(
                              next === "none"
                                ? {}
                                : { sort: field, order: next },
                            );
                            resetHistoryPage();
                          }}
                        />
                      </TableHead>
                      <TableHead>
                        <ColumnHeader
                          description={t("history.columnDescriptions.outcome")}
                        >
                          {t("history.columns.outcome")}
                        </ColumnHeader>
                      </TableHead>
                      <TableHead>
                        <ColumnHeader
                          description={t(
                            "history.columnDescriptions.externalId",
                          )}
                        >
                          {t("history.columns.externalId")}
                        </ColumnHeader>
                      </TableHead>
                      <TableHead className="text-right" />
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {pagedAdjustments.map((adj) => (
                      <HistoryRow
                        key={adj.id}
                        adj={adj}
                        onClone={openCloneAdjust}
                        onFilterAccount={filterHistoryAccount}
                        onFilterAsset={filterHistoryAsset}
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
        initialRealizedPnl={adjustRealizedPnl}
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
