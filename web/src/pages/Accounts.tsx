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

import { useEffect, useMemo, useRef, useState } from "react";
import type { ComponentProps, CSSProperties } from "react";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { useNavigate, useSearchParams } from "react-router-dom";
import {
  Ban,
  CircleCheck,
  CircleAlert,
  Folder,
  Globe2,
  Plus,
  Search,
  Trash2,
  Users,
} from "lucide-react";

import type {
  Account,
  AccountListFilters,
  ApiErrorDependent,
  AuditEntry,
  Group,
  GroupListFilters,
  PositionCountFilterMode,
  SortOrder,
  StatusListFilter,
  TextMatchMode,
} from "@/api/types";
import { useAccountsPage } from "@/api/useAccounts";
import { useGroupsPage } from "@/api/useGroups";
import { validateAccountID } from "@/api/validate";
import { Autocomplete } from "@/components/Autocomplete";
import {
  EmptyState,
  ErrorBanner,
  ErrorState,
  TableSkeleton,
} from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
import { StatusDot } from "@/components/StatusDot";
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
  ActionButton,
  AUTOCOMPLETE_SUGGESTION_LIMIT,
  ApiError,
  ColumnHeader,
  DeleteButton,
  EditButton,
  FieldLabel,
  FilterBar,
  FilterChip,
  FilterByButton,
  FilterOperatorSelect,
  HistoryButton,
  IdCell,
  MoreFiltersButton,
  NumberRangeFilter,
  AutocompleteFilterField,
  PoliciesButton,
  reportInvalidFilterControls,
  PositionsButton,
  RowActions,
  Segmented,
  ShareLinkButton,
  SortableHeader,
  TextFilter,
  TradingButton,
  useOfficerApi,
} from "@/framework";
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
import { Textarea } from "@/components/ui/textarea";
import { ClearableInput } from "@/components/ClearableInput";
import { ClearFieldButton } from "@/components/ClearFieldButton";
import { useDisplayPreferences } from "@/theme/display-context";
import type { DensityMode } from "@/theme/display-context";
import { operatorOptions } from "@/lib/dataControlLabels";
import { isNonNegativeIntegerRangeValid } from "@/lib/numberStep";
import { knownPageCount } from "@/lib/tablePagination";
import { absoluteAppUrl, shareUrl } from "@/lib/shareLink";
import { sortDirection } from "@/lib/sortDirection";
import { formatDateTime } from "@/i18n/format";
import { usePersistentPageSize } from "@/lib/tablePageSize";
import { useGlobalAccountFilter } from "@/lib/globalAccountFilter";
import {
  DEFAULT_SEARCH_DEBOUNCE_MS,
  useDebouncedValue,
} from "@/lib/useDebounce";
import { cn } from "@/lib/utils";

type AccountsTab = "accounts" | "groups";

const ACCOUNT_SORT_KEYS = new Set([
  "blockReason",
  "code",
  "group",
  "positionCount",
  "status",
  "title",
]);
const GROUP_SORT_KEYS = new Set([
  "accountCount",
  "blockReason",
  "code",
  "notes",
  "positionCount",
  "status",
  "title",
]);

type AdvancedListFilters = {
  positionMode: PositionCountFilterMode;
  positionMin: string;
  positionMax: string;
  accountMode: PositionCountFilterMode;
  accountMin: string;
  accountMax: string;
  notes: string;
  notesMatch: TextMatchMode;
  blockReason: string;
  blockReasonMatch: TextMatchMode;
};

const DEFAULT_ADVANCED_FILTERS: AdvancedListFilters = {
  positionMode: "all",
  positionMin: "",
  positionMax: "",
  accountMode: "all",
  accountMin: "",
  accountMax: "",
  notes: "",
  notesMatch: "contains",
  blockReason: "",
  blockReasonMatch: "contains",
};

function trimmedOrUndefined(value: string): string | undefined {
  const trimmed = value.trim();
  return trimmed === "" ? undefined : trimmed;
}

const TEXT_MATCH_MODES: TextMatchMode[] = [
  "contains",
  "starts_with",
  "ends_with",
  "exact",
];

function isTextMatchMode(value: string | null): value is TextMatchMode {
  return value !== null && (TEXT_MATCH_MODES as string[]).includes(value);
}

const COUNT_MODES: PositionCountFilterMode[] = [
  "all",
  "eq",
  "neq",
  "gt",
  "gte",
  "lt",
  "lte",
  "greater_than",
  "less_than",
  "between",
];

function countModeFromParam(value: string | null): PositionCountFilterMode {
  return value !== null && (COUNT_MODES as string[]).includes(value)
    ? (value as PositionCountFilterMode)
    : "all";
}

function countModeNeedsMin(mode: PositionCountFilterMode): boolean {
  return (
    mode === "eq" ||
    mode === "neq" ||
    mode === "gt" ||
    mode === "gte" ||
    mode === "greater_than"
  );
}

function countModeNeedsMax(mode: PositionCountFilterMode): boolean {
  return mode === "lt" || mode === "lte" || mode === "less_than";
}

/** Read the shared `codeMatch` param, defaulting to "contains". */
function textMatchFromParams(params: URLSearchParams): TextMatchMode {
  const value = params.get("codeMatch");
  return isTextMatchMode(value) ? value : "contains";
}

/** Read the shared `status` param, defaulting to "all". */
function statusFromParams(params: URLSearchParams): StatusListFilter {
  const value = params.get("status");
  return value === "active" || value === "blocked" ? value : "all";
}

/** Read the shared `sort`/`order` params into a sort state object. */
function sortFromParams(
  params: URLSearchParams,
  allowed: Set<string>,
): {
  sort?: string;
  order?: SortOrder;
} {
  const sort = params.get("sort");
  const order = params.get("order");
  if (
    sort !== null &&
    allowed.has(sort) &&
    (order === "asc" || order === "desc")
  ) {
    return { sort, order };
  }
  return {};
}

/** Read the advanced (position/account count, notes, block reason) filters from
 *  the shared params, mirroring the API param names. */
function advancedFromParams(params: URLSearchParams): AdvancedListFilters {
  const positionMode = countModeFromParam(params.get("positionCountMode"));
  const accountMode = countModeFromParam(params.get("accountCountMode"));
  const notes = params.get("notes") ?? "";
  const blockReason = params.get("blockReason") ?? "";
  return {
    positionMode,
    positionMin: params.get("positionCountMin") ?? "",
    positionMax: params.get("positionCountMax") ?? "",
    accountMode,
    accountMin: params.get("accountCountMin") ?? "",
    accountMax: params.get("accountCountMax") ?? "",
    notes,
    notesMatch:
      notes !== "" && isTextMatchMode(params.get("notesMatch"))
        ? (params.get("notesMatch") as TextMatchMode)
        : DEFAULT_ADVANCED_FILTERS.notesMatch,
    blockReason,
    blockReasonMatch:
      blockReason !== "" && isTextMatchMode(params.get("blockReasonMatch"))
        ? (params.get("blockReasonMatch") as TextMatchMode)
        : DEFAULT_ADVANCED_FILTERS.blockReasonMatch,
  };
}

/** Serialize the advanced filters into a `URLSearchParams`, emitting only the
 *  active fields. `entity` gates the groups-only account-count range. */
function appendAdvancedParams(
  query: URLSearchParams,
  filters: AdvancedListFilters,
  entity: "accounts" | "groups",
): void {
  const positionMin = trimmedOrUndefined(filters.positionMin);
  const positionMax = trimmedOrUndefined(filters.positionMax);
  if (countModeNeedsMin(filters.positionMode) && positionMin !== undefined) {
    query.set("positionCountMode", filters.positionMode);
    query.set("positionCountMin", positionMin);
  } else if (
    countModeNeedsMax(filters.positionMode) &&
    positionMax !== undefined
  ) {
    query.set("positionCountMode", filters.positionMode);
    query.set("positionCountMax", positionMax);
  } else if (
    filters.positionMode === "between" &&
    positionMin !== undefined &&
    positionMax !== undefined
  ) {
    query.set("positionCountMode", filters.positionMode);
    query.set("positionCountMin", positionMin);
    query.set("positionCountMax", positionMax);
  }
  if (entity === "groups") {
    const accountMin = trimmedOrUndefined(filters.accountMin);
    const accountMax = trimmedOrUndefined(filters.accountMax);
    if (countModeNeedsMin(filters.accountMode) && accountMin !== undefined) {
      query.set("accountCountMode", filters.accountMode);
      query.set("accountCountMin", accountMin);
    } else if (
      countModeNeedsMax(filters.accountMode) &&
      accountMax !== undefined
    ) {
      query.set("accountCountMode", filters.accountMode);
      query.set("accountCountMax", accountMax);
    } else if (
      filters.accountMode === "between" &&
      accountMin !== undefined &&
      accountMax !== undefined
    ) {
      query.set("accountCountMode", filters.accountMode);
      query.set("accountCountMin", accountMin);
      query.set("accountCountMax", accountMax);
    }
  }
  const notes = trimmedOrUndefined(filters.notes);
  if (entity === "groups" && notes !== undefined) {
    query.set("notes", notes);
    if (filters.notesMatch !== "contains") {
      query.set("notesMatch", filters.notesMatch);
    }
  }
  const blockReason = trimmedOrUndefined(filters.blockReason);
  if (blockReason !== undefined) {
    query.set("blockReason", blockReason);
    if (filters.blockReasonMatch !== "contains") {
      query.set("blockReasonMatch", filters.blockReasonMatch);
    }
  }
}

function positionCountQuery(
  filters: AdvancedListFilters,
): Pick<
  AccountListFilters,
  "positionCountMode" | "positionCountMin" | "positionCountMax"
> {
  const min = trimmedOrUndefined(filters.positionMin);
  const max = trimmedOrUndefined(filters.positionMax);
  switch (filters.positionMode) {
    case "eq":
    case "neq":
    case "gt":
    case "gte":
    case "greater_than":
      return min === undefined
        ? {}
        : {
            positionCountMode: filters.positionMode,
            positionCountMin: min,
          };
    case "lt":
    case "lte":
    case "less_than":
      return min === undefined
        ? {}
        : {
            positionCountMode: filters.positionMode,
            positionCountMax: min,
          };
    case "between":
      return min === undefined || max === undefined
        ? {}
        : {
            positionCountMode: "between",
            positionCountMin: min,
            positionCountMax: max,
          };
    default:
      return {};
  }
}

function accountCountQuery(
  filters: AdvancedListFilters,
): Pick<
  GroupListFilters,
  "accountCountMode" | "accountCountMin" | "accountCountMax"
> {
  const min = trimmedOrUndefined(filters.accountMin);
  const max = trimmedOrUndefined(filters.accountMax);
  switch (filters.accountMode) {
    case "eq":
    case "neq":
    case "gt":
    case "gte":
    case "greater_than":
      return min === undefined
        ? {}
        : { accountCountMode: filters.accountMode, accountCountMin: min };
    case "lt":
    case "lte":
    case "less_than":
      return min === undefined
        ? {}
        : {
            accountCountMode: filters.accountMode,
            accountCountMax: min,
          };
    case "between":
      return min === undefined || max === undefined
        ? {}
        : {
            accountCountMode: "between",
            accountCountMin: min,
            accountCountMax: max,
          };
    default:
      return {};
  }
}

function errMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

function matchLabelKey(mode: TextMatchMode): string {
  switch (mode) {
    case "starts_with":
      return "filters.match.startsWith";
    case "ends_with":
      return "filters.match.endsWith";
    case "exact":
      return "filters.match.exact";
    default:
      return "filters.match.contains";
  }
}

function countModeFromNumberOperator(
  operator: string,
): PositionCountFilterMode {
  switch (operator) {
    case "eq":
    case "neq":
    case "gt":
    case "gte":
    case "lt":
    case "lte":
    case "between":
      return operator;
    default:
      return "all";
  }
}

function numberOperatorFromCountMode(
  mode: PositionCountFilterMode,
): "eq" | "neq" | "gt" | "gte" | "lt" | "lte" | "between" {
  switch (mode) {
    case "greater_than":
      return "gt";
    case "less_than":
      return "lt";
    case "eq":
    case "neq":
    case "gt":
    case "gte":
    case "lt":
    case "lte":
    case "between":
      return mode;
    default:
      return "eq";
  }
}

/** A single applied advanced-filter chip, tagged by which field it clears. */
type AdvancedSummaryEntry = {
  field: "position" | "account" | "notes" | "blockReason";
  text: string;
};

function advancedFilterSummary(
  filters: AdvancedListFilters,
  t: TFunction<"accounts">,
  includeNotes = true,
): AdvancedSummaryEntry[] {
  const out: AdvancedSummaryEntry[] = [];
  const min = trimmedOrUndefined(filters.positionMin);
  const max = trimmedOrUndefined(filters.positionMax);
  if (
    (filters.positionMode === "gt" ||
      filters.positionMode === "greater_than") &&
    min !== undefined
  ) {
    out.push({
      field: "position",
      text: t("filters.advanced.summary.positionGreater", { value: min }),
    });
  } else if (
    (filters.positionMode === "lt" || filters.positionMode === "less_than") &&
    max !== undefined
  ) {
    out.push({
      field: "position",
      text: t("filters.advanced.summary.positionLess", { value: max }),
    });
  } else if (countModeNeedsMin(filters.positionMode) && min !== undefined) {
    out.push({
      field: "position",
      text: t("filters.advanced.summary.text", {
        field: t("filters.advanced.positionLabel"),
        match: t(`common:operators.number.${filters.positionMode}`),
        value: min,
      }),
    });
  } else if (countModeNeedsMax(filters.positionMode) && max !== undefined) {
    out.push({
      field: "position",
      text: t("filters.advanced.summary.text", {
        field: t("filters.advanced.positionLabel"),
        match: t(`common:operators.number.${filters.positionMode}`),
        value: max,
      }),
    });
  } else if (
    filters.positionMode === "between" &&
    min !== undefined &&
    max !== undefined
  ) {
    out.push({
      field: "position",
      text: t("filters.advanced.summary.positionBetween", { min, max }),
    });
  }
  const accountMin = trimmedOrUndefined(filters.accountMin);
  const accountMax = trimmedOrUndefined(filters.accountMax);
  if (
    (filters.accountMode === "gt" || filters.accountMode === "greater_than") &&
    accountMin !== undefined
  ) {
    out.push({
      field: "account",
      text: t("filters.advanced.summary.accountGreater", { value: accountMin }),
    });
  } else if (
    (filters.accountMode === "lt" || filters.accountMode === "less_than") &&
    accountMax !== undefined
  ) {
    out.push({
      field: "account",
      text: t("filters.advanced.summary.accountLess", { value: accountMax }),
    });
  } else if (
    countModeNeedsMin(filters.accountMode) &&
    accountMin !== undefined
  ) {
    out.push({
      field: "account",
      text: t("filters.advanced.summary.text", {
        field: t("filters.advanced.accountLabel"),
        match: t(`common:operators.number.${filters.accountMode}`),
        value: accountMin,
      }),
    });
  } else if (
    countModeNeedsMax(filters.accountMode) &&
    accountMax !== undefined
  ) {
    out.push({
      field: "account",
      text: t("filters.advanced.summary.text", {
        field: t("filters.advanced.accountLabel"),
        match: t(`common:operators.number.${filters.accountMode}`),
        value: accountMax,
      }),
    });
  } else if (
    filters.accountMode === "between" &&
    accountMin !== undefined &&
    accountMax !== undefined
  ) {
    out.push({
      field: "account",
      text: t("filters.advanced.summary.accountBetween", {
        min: accountMin,
        max: accountMax,
      }),
    });
  }
  const notes = trimmedOrUndefined(filters.notes);
  if (includeNotes && notes !== undefined) {
    out.push({
      field: "notes",
      text: t("filters.advanced.summary.text", {
        field: t("filters.advanced.notesShort"),
        match: t(matchLabelKey(filters.notesMatch)),
        value: notes,
      }),
    });
  }
  const blockReason = trimmedOrUndefined(filters.blockReason);
  if (blockReason !== undefined) {
    out.push({
      field: "blockReason",
      text: t("filters.advanced.summary.text", {
        field: t("filters.advanced.blockReasonShort"),
        match: t(matchLabelKey(filters.blockReasonMatch)),
        value: blockReason,
      }),
    });
  }
  return out;
}

/** Reset one advanced-filter field group back to its default. */
function clearAdvancedField(
  filters: AdvancedListFilters,
  field: AdvancedSummaryEntry["field"],
): AdvancedListFilters {
  switch (field) {
    case "position":
      return {
        ...filters,
        positionMode: DEFAULT_ADVANCED_FILTERS.positionMode,
        positionMin: DEFAULT_ADVANCED_FILTERS.positionMin,
        positionMax: DEFAULT_ADVANCED_FILTERS.positionMax,
      };
    case "account":
      return {
        ...filters,
        accountMode: DEFAULT_ADVANCED_FILTERS.accountMode,
        accountMin: DEFAULT_ADVANCED_FILTERS.accountMin,
        accountMax: DEFAULT_ADVANCED_FILTERS.accountMax,
      };
    case "notes":
      return {
        ...filters,
        notes: DEFAULT_ADVANCED_FILTERS.notes,
        notesMatch: DEFAULT_ADVANCED_FILTERS.notesMatch,
      };
    case "blockReason":
      return {
        ...filters,
        blockReason: DEFAULT_ADVANCED_FILTERS.blockReason,
        blockReasonMatch: DEFAULT_ADVANCED_FILTERS.blockReasonMatch,
      };
  }
}

function ListFilters({
  entity,
  code,
  codeMatch,
  codeSuggestions,
  codeLoading,
  groupSearch,
  groupSuggestions,
  groupLoading,
  groupActive,
  status,
  advancedSummary,
  shareHref,
  onClearAll,
  onCode,
  onCodeMatch,
  onGroupSearch,
  onStatus,
  onClearAdvanced,
  onOpenAdvanced,
  accountGlobalToggle,
}: {
  entity: "accounts" | "groups";
  code: string;
  codeMatch: TextMatchMode;
  codeSuggestions?: string[];
  codeLoading?: boolean;
  groupSearch?: string;
  groupSuggestions?: string[];
  groupLoading?: boolean;
  groupActive?: boolean;
  status: StatusListFilter;
  advancedSummary: AdvancedSummaryEntry[];
  shareHref: string;
  onClearAll: () => void;
  onCode: (value: string) => void;
  onCodeMatch: (value: TextMatchMode) => void;
  onGroupSearch?: (value: string) => void;
  onStatus: (value: StatusListFilter) => void;
  onClearAdvanced: (field: AdvancedSummaryEntry["field"]) => void;
  onOpenAdvanced: () => void;
  accountGlobalToggle?: ComponentProps<
    typeof AutocompleteFilterField
  >["globalToggle"];
}) {
  const { t } = useTranslation("accounts");
  const { t: tc } = useTranslation("common");
  const maskHelp = t("filters.maskHelp");
  const accountGroupFilter =
    entity === "accounts" &&
    groupSearch !== undefined &&
    onGroupSearch !== undefined;
  const active =
    code.trim() !== "" ||
    (groupActive ?? (groupSearch?.trim() ?? "") !== "") ||
    status !== "all" ||
    advancedSummary.length > 0;
  const statusOptions = [
    { value: "all", label: t("filters.status.all") },
    { value: "active", label: t("filters.status.active") },
    { value: "blocked", label: t("filters.status.blocked") },
  ];
  return (
    <FilterBar
      active={active}
      activeLabel={tc("filters.active")}
      onClearActive={onClearAll}
      clearActiveLabel={tc("filters.clearAll")}
      chips={
        advancedSummary.length > 0 ? (
          <>
            {advancedSummary.map((entry) => (
              <FilterChip
                key={entry.field}
                label={entry.text}
                removeLabel={tc("filters.removeAdvanced")}
                onRemove={() => onClearAdvanced(entry.field)}
              />
            ))}
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-6 text-[0.6875rem]"
              onClick={() => {
                for (const entry of advancedSummary) {
                  onClearAdvanced(entry.field);
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
          <div className="grid gap-1.5">
            <FieldLabel>{t("filters.status.ariaLabel")}</FieldLabel>
            <Segmented
              value={status}
              options={statusOptions}
              onChange={(value) => onStatus(value as StatusListFilter)}
            />
          </div>
          <MoreFiltersButton
            count={advancedSummary.length}
            label={t("filters.advanced.trigger")}
            onClick={onOpenAdvanced}
          />
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
    >
      {accountGroupFilter && (
        <AutocompleteFilterField
          label={t("accounts.filters.groupLabel")}
          value={groupSearch}
          placeholder={t("accounts.filters.groupPlaceholder")}
          suggestions={groupSuggestions}
          loading={groupLoading}
          searchingLabel={tc("filters.onlineLoading", {
            field: t("accounts.filters.groupLabel"),
          })}
          onChange={onGroupSearch}
          onClear={() => onGroupSearch("")}
          clearLabel={tc("filters.clearField")}
          width={180}
        />
      )}
      <div className="grid gap-1.5">
        <FieldLabel>{t(`${entity}.filters.codeLabel`)}</FieldLabel>
        <div className="flex items-center gap-2">
          <AutocompleteFilterField
            value={code}
            placeholder={t(`${entity}.filters.codePlaceholder`)}
            title={maskHelp}
            ariaLabel={t(`${entity}.filters.codeLabel`)}
            suggestions={codeSuggestions}
            loading={codeLoading}
            searchingLabel={tc("filters.onlineLoading", {
              field: t(`${entity}.filters.codeLabel`),
            })}
            width={220}
            onChange={onCode}
            onClear={() => onCode("")}
            clearLabel={tc("filters.clearField")}
            globalToggle={
              entity === "accounts" ? accountGlobalToggle : undefined
            }
          />
          <FilterOperatorSelect
            value={codeMatch}
            options={operatorOptions(t, "text")}
            ariaLabel={
              entity === "groups"
                ? `${tc("fields.group")} ${tc("filters.operator")}`
                : `${tc("fields.account")} ${tc("filters.operator")}`
            }
            onChange={(value) => {
              if (isTextMatchMode(value)) {
                onCodeMatch(value);
              }
            }}
            width={140}
          />
        </div>
      </div>
    </FilterBar>
  );
}

function AdvancedFilterDialog({
  entity,
  open,
  draft,
  onDraftChange,
  onApply,
  onCancel,
}: {
  entity: "accounts" | "groups";
  open: boolean;
  draft: AdvancedListFilters;
  onDraftChange: (next: AdvancedListFilters) => void;
  onApply: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation("accounts");
  const formRef = useRef<HTMLFormElement | null>(null);
  const update = <K extends keyof AdvancedListFilters>(
    key: K,
    value: AdvancedListFilters[K],
  ) => onDraftChange({ ...draft, [key]: value });
  const updatePositionCount = (
    patch: Partial<
      Pick<AdvancedListFilters, "positionMode" | "positionMin" | "positionMax">
    >,
  ) =>
    onDraftChange({
      ...draft,
      positionMode:
        draft.positionMode === "all" && patch.positionMode === undefined
          ? "eq"
          : draft.positionMode,
      ...patch,
    });
  const updateAccountCount = (
    patch: Partial<
      Pick<AdvancedListFilters, "accountMode" | "accountMin" | "accountMax">
    >,
  ) =>
    onDraftChange({
      ...draft,
      accountMode:
        draft.accountMode === "all" && patch.accountMode === undefined
          ? "eq"
          : draft.accountMode,
      ...patch,
    });
  const positionRangeValid = isNonNegativeIntegerRangeValid(
    numberOperatorFromCountMode(draft.positionMode),
    draft.positionMin,
    draft.positionMax,
  );
  const accountRangeValid =
    entity === "accounts" ||
    isNonNegativeIntegerRangeValid(
      numberOperatorFromCountMode(draft.accountMode),
      draft.accountMin,
      draft.accountMax,
    );
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onCancel();
      }}
    >
      <DialogContent>
        <form
          ref={formRef}
          className="grid gap-4"
          onSubmit={(event) => {
            event.preventDefault();
            if (!reportInvalidFilterControls(formRef.current)) {
              return;
            }
            onApply();
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("filters.advanced.title")}</DialogTitle>
            <DialogDescription>
              {t(`${entity}.filters.advancedDescription`)}
            </DialogDescription>
            <p className="text-[0.6875rem] text-muted-lt">
              {t("filters.maskHelp")}
            </p>
          </DialogHeader>

          <div className="grid gap-2">
            <Label>{t("filters.advanced.positionLabel")}</Label>
            <div className="flex items-center gap-2">
              <NumberRangeFilter
                fluid
                operator={numberOperatorFromCountMode(draft.positionMode)}
                operators={operatorOptions(t, "number")}
                operatorAriaLabel={t("filters.advanced.positionLabel")}
                min={draft.positionMin}
                max={draft.positionMax}
                onOperatorChange={(value) =>
                  update("positionMode", countModeFromNumberOperator(value))
                }
                onMinChange={(value) =>
                  updatePositionCount({ positionMin: value })
                }
                onMaxChange={(value) =>
                  updatePositionCount({ positionMax: value })
                }
              />
            </div>
          </div>

          {entity === "groups" && (
            <div className="grid gap-2">
              <Label>{t("filters.advanced.accountLabel")}</Label>
              <div className="flex items-center gap-2">
                <NumberRangeFilter
                  fluid
                  operator={numberOperatorFromCountMode(draft.accountMode)}
                  operators={operatorOptions(t, "number")}
                  operatorAriaLabel={t("filters.advanced.accountLabel")}
                  min={draft.accountMin}
                  max={draft.accountMax}
                  onOperatorChange={(value) =>
                    update("accountMode", countModeFromNumberOperator(value))
                  }
                  onMinChange={(value) =>
                    updateAccountCount({ accountMin: value })
                  }
                  onMaxChange={(value) =>
                    updateAccountCount({ accountMax: value })
                  }
                />
              </div>
            </div>
          )}

          {entity === "groups" && (
            <label className="grid gap-2">
              <Label asChild>
                <span>{t("filters.advanced.notesLabel")}</span>
              </Label>
              <TextFilter
                fluid
                value={draft.notes}
                operator={draft.notesMatch}
                operators={operatorOptions(t, "text")}
                placeholder={t(`${entity}.filters.notesPlaceholder`)}
                onValueChange={(value) => update("notes", value)}
                onClear={() => update("notes", "")}
                clearLabel={t("common:filters.clearField")}
                onOperatorChange={(value) => {
                  if (isTextMatchMode(value)) {
                    update("notesMatch", value);
                  }
                }}
              />
            </label>
          )}

          <label className="grid gap-2">
            <Label asChild>
              <span>{t("filters.advanced.blockReasonLabel")}</span>
            </Label>
            <TextFilter
              fluid
              value={draft.blockReason}
              operator={draft.blockReasonMatch}
              operators={operatorOptions(t, "text")}
              placeholder={t(`${entity}.filters.blockReasonPlaceholder`)}
              onValueChange={(value) => update("blockReason", value)}
              onClear={() => update("blockReason", "")}
              clearLabel={t("common:filters.clearField")}
              onOperatorChange={(value) => {
                if (isTextMatchMode(value)) {
                  update("blockReasonMatch", value);
                }
              }}
            />
          </label>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onCancel}>
              {t("filters.advanced.cancel")}
            </Button>
            <Button
              type="submit"
              disabled={!positionRangeValid || !accountRangeValid}
            >
              <Search className="h-3.5 w-3.5" />
              {t("common:filters.applyAdvanced")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Create account dialog
// ---------------------------------------------------------------------------

// Exported for unit tests; rendered standalone inside Accounts otherwise.
export function CreateAccountDialog({
  groupSuggestions,
  onCreated,
}: {
  groupSuggestions: string[];
  onCreated: () => void;
}) {
  const { t } = useTranslation("validation");
  const { t: ta } = useTranslation("accounts");
  const { createAccount, setAccountGroup } = useOfficerApi();
  const [open, setOpen] = useState(false);
  const [code, setCode] = useState("");
  const [group, setGroup] = useState("");
  const [currency, setCurrency] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const assetSuggestions = useAssetCodeSuggestions(currency, open);

  const validation = code.length > 0 ? validateAccountID(code) : null;

  const reset = () => {
    setCode("");
    setGroup("");
    setCurrency("");
    setError(null);
    setBusy(false);
  };

  const submit = async () => {
    const v = validateAccountID(code);
    if (v) {
      setError(t(v.key, v.values));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await createAccount(code, "", currency.trim());
      if (group.trim().length > 0) {
        // The account was just created above, so it is guaranteed to exist.
        await setAccountGroup(code, group.trim(), "reject");
      }
      setOpen(false);
      reset();
      onCreated();
    } catch (err) {
      setError(errMessage(err));
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <Button size="sm" onClick={() => setOpen(true)}>
        <Plus className="h-3.5 w-3.5" />
        {ta("createAccount.trigger")}
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{ta("createAccount.title")}</DialogTitle>
          <DialogDescription>
            {ta("createAccount.description")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="account-code">
              {ta("createAccount.codeLabel")}
            </Label>
            <ClearableInput
              id="account-code"
              value={code}
              autoFocus
              spellCheck={false}
              placeholder="acc-aapl-desk"
              onChange={(e) => setCode(e.target.value)}
              onClear={() => setCode("")}
              clearLabel={ta("filters.clearField")}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !validation) {
                  void submit();
                }
              }}
            />
            <p className="text-[0.6875rem] text-muted">
              {ta("createAccount.codeHint")}
            </p>
            {validation && (
              <p className="text-[0.6875rem] text-[var(--danger)]">
                {t(validation.key, validation.values)}
              </p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="create-account-group">
              {ta("createAccount.groupLabel")}
            </Label>
            <Autocomplete
              id="create-account-group"
              value={group}
              onChange={setGroup}
              suggestions={groupSuggestions}
              placeholder="equity-desks"
              spellCheck={false}
              onClear={() => setGroup("")}
              clearLabel={ta("filters.clearField")}
            />
            <p className="text-[0.6875rem] text-muted">
              {ta("createAccount.groupHint")}
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="create-account-currency">
              {ta("createAccount.currencyLabel")}
            </Label>
            <Autocomplete
              id="create-account-currency"
              value={currency}
              onChange={setCurrency}
              suggestions={assetSuggestions}
              disabled={busy}
              placeholder="USD"
              spellCheck={false}
              onClear={() => setCurrency("")}
              clearLabel={ta("filters.clearField")}
            />
            <p className="text-[0.6875rem] text-muted">
              {ta("createAccount.currencyHint")}
            </p>
          </div>
        </div>
        {error && (
          <ErrorBanner message={error} onDismiss={() => setError(null)} />
        )}
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => setOpen(false)}
            disabled={busy}
          >
            {ta("createAccount.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || code.length === 0 || validation !== null}
          >
            {ta("createAccount.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Create group dialog
// ---------------------------------------------------------------------------

// The API spends this code on addressing the realm default group in a path
// position (PUT /groups/-/default/currency), so the backend refuses it as a
// group id. Rejecting it here explains why instead of showing a bare 400.
const RESERVED_GROUP_CODE = "-";

function CreateGroupDialog({ onCreated }: { onCreated: () => void }) {
  const { t } = useTranslation("accounts");
  const { createGroup } = useOfficerApi();
  const [open, setOpen] = useState(false);
  const [code, setCode] = useState("");
  const [title, setTitle] = useState("");
  const [currency, setCurrency] = useState("");
  const [notes, setNotes] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const assetSuggestions = useAssetCodeSuggestions(currency, open);

  const codeTrimmed = code.trim();
  const titleTrimmed = title.trim();
  const codeReserved = codeTrimmed === RESERVED_GROUP_CODE;

  const reset = () => {
    setCode("");
    setTitle("");
    setCurrency("");
    setNotes("");
    setError(null);
    setBusy(false);
  };

  const submit = async () => {
    if (codeTrimmed.length === 0 || codeReserved) return;
    setBusy(true);
    setError(null);
    try {
      await createGroup(
        codeTrimmed,
        titleTrimmed,
        notes.trim(),
        currency.trim(),
      );
      setOpen(false);
      reset();
      onCreated();
    } catch (err) {
      setError(errMessage(err));
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
        <Plus className="h-3.5 w-3.5" />
        {t("createGroup.trigger")}
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("createGroup.title")}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="group-code">{t("createGroup.codeLabel")}</Label>
            <Input
              id="group-code"
              value={code}
              autoFocus
              spellCheck={false}
              placeholder="equity-desks"
              onChange={(e) => setCode(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && codeTrimmed.length > 0) {
                  void submit();
                }
              }}
            />
            {codeReserved && (
              <p className="text-[0.6875rem] text-[var(--danger)]">
                {t("reservedGroupCode")}
              </p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="group-title">{t("createGroup.titleLabel")}</Label>
            <Input
              id="group-title"
              value={title}
              spellCheck={false}
              placeholder={t("createGroup.titlePlaceholder")}
              onChange={(e) => setTitle(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && codeTrimmed.length > 0) {
                  void submit();
                }
              }}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="create-group-currency">
              {t("createGroup.currencyLabel")}
            </Label>
            <Autocomplete
              id="create-group-currency"
              value={currency}
              onChange={setCurrency}
              suggestions={assetSuggestions}
              disabled={busy}
              placeholder="USD"
              spellCheck={false}
              onClear={() => setCurrency("")}
              clearLabel={t("filters.clearField")}
            />
            <p className="text-[0.6875rem] text-muted">
              {t("createGroup.currencyHint")}
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="group-notes">{t("createGroup.notesLabel")}</Label>
            <Textarea
              id="group-notes"
              value={notes}
              placeholder={t("createGroup.notesPlaceholder")}
              onChange={(e) => setNotes(e.target.value)}
            />
            <p className="text-[0.6875rem] text-muted">
              {t("createGroup.notesHint")}
            </p>
          </div>
        </div>
        {error && (
          <ErrorBanner message={error} onDismiss={() => setError(null)} />
        )}
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => setOpen(false)}
            disabled={busy}
          >
            {t("createGroup.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || codeTrimmed.length === 0 || codeReserved}
          >
            {t("createGroup.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Block / unblock account dialogs
// ---------------------------------------------------------------------------

function BlockAccountDialog({
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

function UnblockAccountConfirm({
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

// ---------------------------------------------------------------------------
// Block / unblock group dialogs
// ---------------------------------------------------------------------------

function BlockGroupDialog({
  group,
  open,
  onOpenChange,
  onDone,
}: {
  group: Group | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Group) => void;
}) {
  const { t } = useTranslation("accounts");
  const { blockGroup } = useOfficerApi();
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const trimmed = reason.trim();

  const submit = async () => {
    if (!group || trimmed.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await blockGroup(group.code, trimmed);
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
            {t("blockGroup.titlePrefix")}{" "}
            <span className="nums text-accent">{group?.code}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">{t("blockGroup.description")}</p>
        <div className="space-y-2">
          <Label htmlFor="block-group-reason">
            {t("blockGroup.reasonLabel")}
          </Label>
          <Textarea
            id="block-group-reason"
            value={reason}
            autoFocus
            placeholder={t("blockGroup.reasonPlaceholder")}
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
            {t("blockGroup.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || trimmed.length === 0}
          >
            <Ban className="h-3.5 w-3.5" />
            {t("blockGroup.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function UnblockGroupConfirm({
  group,
  open,
  onOpenChange,
  onDone,
}: {
  group: Group | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Group) => void;
}) {
  const { t } = useTranslation("accounts");
  const { unblockGroup } = useOfficerApi();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!group) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await unblockGroup(group.code);
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
          <AlertDialogTitle>{t("unblockGroup.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("unblockGroup.descriptionPrefix")}{" "}
            <span className="nums text-accent">{group?.code}</span>{" "}
            {t("unblockGroup.descriptionSuffix")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <ErrorBanner message={error} onDismiss={() => setError(null)} />
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>
            {t("unblockGroup.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              void submit();
            }}
            disabled={busy}
          >
            {t("unblockGroup.submit")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

// ---------------------------------------------------------------------------
// Delete group confirm
// ---------------------------------------------------------------------------

function DeleteGroupConfirm({
  group,
  open,
  onOpenChange,
  onDone,
}: {
  group: Group | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: () => void;
}) {
  const { t } = useTranslation("accounts");
  const { deleteGroup } = useOfficerApi();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!group) return;
    setBusy(true);
    setError(null);
    try {
      await deleteGroup(group.code);
      onOpenChange(false);
      onDone();
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
          <AlertDialogTitle>{t("deleteGroup.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("deleteGroup.descriptionPrefix")}{" "}
            <span className="nums text-accent">{group?.code}</span>{" "}
            {t("deleteGroup.descriptionSuffix")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <ErrorBanner message={error} onDismiss={() => setError(null)} />
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>
            {t("deleteGroup.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              void submit();
            }}
            disabled={busy}
            className="border-[var(--danger)] bg-[var(--danger)] text-bg hover:border-[var(--danger)] hover:bg-[var(--danger)]"
          >
            <Trash2 className="h-3.5 w-3.5" />
            {t("deleteGroup.submit")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

// ---------------------------------------------------------------------------
// Delete account confirm
// ---------------------------------------------------------------------------

function DeleteAccountConfirm({
  account,
  open,
  onOpenChange,
  onDone,
}: {
  account: Account | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: () => void;
}) {
  const { t } = useTranslation("accounts");
  const { deleteAccount } = useOfficerApi();
  const [error, setError] = useState<string | null>(null);
  const [dependents, setDependents] = useState<ApiErrorDependent[]>([]);
  const [busy, setBusy] = useState(false);

  const changeOpen = (next: boolean) => {
    if (!next) {
      setError(null);
      setDependents([]);
    }
    onOpenChange(next);
  };

  const submit = async (force: boolean) => {
    if (!account) return;
    setBusy(true);
    setError(null);
    try {
      await deleteAccount(account.code, force);
      changeOpen(false);
      onDone();
    } catch (err) {
      if (err instanceof ApiError && err.code === "has_dependents") {
        setDependents(err.dependents ?? []);
        setError(null);
      } else {
        setError(errMessage(err));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <AlertDialog open={open} onOpenChange={changeOpen}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("deleteAccount.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("deleteAccount.descriptionPrefix")}{" "}
            <span className="nums text-accent">{account?.code}</span>{" "}
            {t("deleteAccount.descriptionSuffix")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {dependents.length > 0 && (
          <div className="rounded-card border border-[var(--danger)] bg-[var(--danger-dim)] p-3 text-xs">
            <p className="font-medium text-[var(--danger)]">
              {t("deleteAccount.dependentsTitle")}
            </p>
            <ul className="mt-2 space-y-1">
              {dependents.map((dep) => (
                <li key={dep.kind} className="flex justify-between gap-4">
                  <span>
                    {t(`deleteAccount.dependentKinds.${dep.kind}`, {
                      defaultValue: dep.kind,
                    })}
                  </span>
                  <span className="nums">{dep.count}</span>
                </li>
              ))}
            </ul>
          </div>
        )}
        {error && (
          <ErrorBanner message={error} onDismiss={() => setError(null)} />
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>
            {t("deleteAccount.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              void submit(dependents.length > 0);
            }}
            disabled={busy}
            className="border-[var(--danger)] bg-[var(--danger)] text-bg hover:border-[var(--danger)] hover:bg-[var(--danger)]"
          >
            <Trash2 className="h-3.5 w-3.5" />
            {dependents.length > 0
              ? t("deleteAccount.forceSubmit")
              : t("deleteAccount.submit")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

// ---------------------------------------------------------------------------
// Edit account metadata dialog
// ---------------------------------------------------------------------------

function EditAccountMetadataDialog({
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
  const { t: tv } = useTranslation("validation");
  const { updateAccount } = useOfficerApi();
  const [code, setCode] = useState("");
  const [title, setTitle] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      /* eslint-disable react-hooks/set-state-in-effect */
      setCode(account?.code ?? "");
      setTitle(account?.title ?? "");
      /* eslint-enable react-hooks/set-state-in-effect */
    }
  }, [open, account]);

  const codeTrimmed = code.trim();
  const titleTrimmed = title.trim();
  const validation =
    codeTrimmed.length > 0 ? validateAccountID(codeTrimmed) : null;

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      setCode("");
      setTitle("");
      setError(null);
    }
    onOpenChange(next);
  };

  const submit = async () => {
    if (!account || codeTrimmed.length === 0 || validation !== null) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await updateAccount(
        account.code,
        codeTrimmed,
        titleTrimmed,
      );
      onOpenChange(false);
      onDone(updated);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("editAccount.titlePrefix")}{" "}
            <span className="nums text-accent">{account?.code}</span>
          </DialogTitle>
          <DialogDescription>{t("editAccount.description")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="account-edit-code">
              {t("editAccount.codeLabel")}
            </Label>
            <ClearableInput
              id="account-edit-code"
              value={code}
              autoFocus
              spellCheck={false}
              onChange={(e) => setCode(e.target.value)}
              onClear={() => setCode("")}
              clearLabel={t("filters.clearField")}
              disabled={busy}
            />
            {validation && (
              <p className="text-[0.6875rem] text-[var(--danger)]">
                {tv(validation.key, validation.values)}
              </p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="account-edit-title">
              {t("editAccount.titleLabel")}
            </Label>
            <ClearableInput
              id="account-edit-title"
              value={title}
              spellCheck={false}
              onChange={(e) => setTitle(e.target.value)}
              onClear={() => setTitle("")}
              clearLabel={t("filters.clearField")}
              disabled={busy}
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
            {t("editAccount.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || codeTrimmed.length === 0 || validation !== null}
          >
            {t("editAccount.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Edit group metadata dialog
// ---------------------------------------------------------------------------

function EditGroupMetadataDialog({
  group,
  open,
  onOpenChange,
  onDone,
}: {
  group: Group | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Group) => void;
}) {
  const { t } = useTranslation("accounts");
  const { updateGroup } = useOfficerApi();
  const [code, setCode] = useState("");
  const [title, setTitle] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      /* eslint-disable react-hooks/set-state-in-effect */
      setCode(group?.code ?? "");
      setTitle(group?.title ?? "");
      /* eslint-enable react-hooks/set-state-in-effect */
    }
  }, [open, group]);

  const codeTrimmed = code.trim();
  const titleTrimmed = title.trim();

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      setCode("");
      setTitle("");
      setError(null);
    }
    onOpenChange(next);
  };

  const submit = async () => {
    if (!group || codeTrimmed.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await updateGroup(group.code, codeTrimmed, titleTrimmed);
      onOpenChange(false);
      onDone(updated);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("editGroup.titlePrefix")}{" "}
            <span className="nums text-accent">{group?.code}</span>
          </DialogTitle>
          <DialogDescription>{t("editGroup.description")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="group-edit-code">{t("editGroup.codeLabel")}</Label>
            <ClearableInput
              id="group-edit-code"
              value={code}
              autoFocus
              spellCheck={false}
              onChange={(e) => setCode(e.target.value)}
              onClear={() => setCode("")}
              clearLabel={t("filters.clearField")}
              disabled={busy}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="group-edit-title">
              {t("editGroup.titleLabel")}
            </Label>
            <ClearableInput
              id="group-edit-title"
              value={title}
              spellCheck={false}
              onChange={(e) => setTitle(e.target.value)}
              onClear={() => setTitle("")}
              clearLabel={t("filters.clearField")}
              disabled={busy}
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
            {t("editGroup.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={() => void submit()}
            disabled={busy || codeTrimmed.length === 0}
          >
            {t("editGroup.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Edit group notes dialog
// ---------------------------------------------------------------------------

function EditGroupNotesDialog({
  group,
  open,
  onOpenChange,
  onDone,
}: {
  group: Group | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Group) => void;
}) {
  const { t } = useTranslation("accounts");
  const { setGroupNotes } = useOfficerApi();
  const [notes, setNotes] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    // Reseed the notes field from the edited group when the dialog opens.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (open) setNotes(group?.notes ?? "");
  }, [open, group]);

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      setNotes("");
      setError(null);
    }
    onOpenChange(next);
  };

  const submit = async () => {
    if (!group) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await setGroupNotes(group.code, notes);
      onOpenChange(false);
      setNotes("");
      onDone(updated);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("editGroupNotes.titlePrefix")}{" "}
            <span className="nums text-accent">{group?.code}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">
          {t("editGroupNotes.description")}
        </p>
        <div className="space-y-2">
          <Label htmlFor="group-notes-edit">
            {t("editGroupNotes.notesLabel")}
          </Label>
          <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_2rem]">
            <Textarea
              id="group-notes-edit"
              value={notes}
              autoFocus
              rows={4}
              onChange={(e) => setNotes(e.target.value)}
            />
            <ClearFieldButton
              label={t("filters.clearField")}
              onClick={() => setNotes("")}
              disabled={busy || notes.length === 0}
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
            {t("editGroupNotes.cancel")}
          </Button>
          <Button size="sm" onClick={() => void submit()} disabled={busy}>
            {t("editGroupNotes.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Assign group dialog (for accounts)
// ---------------------------------------------------------------------------

function AssignGroupDialog({
  account,
  groupSuggestions,
  open,
  onOpenChange,
  onDone,
}: {
  account: Account | null;
  groupSuggestions: string[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (updated: Account) => void;
}) {
  const { t } = useTranslation("accounts");
  const { setAccountGroup, createGroup } = useOfficerApi();
  const [group, setGroup] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setGroup(account?.group ?? "");
    }
  }, [open, account]);

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      setGroup("");
      setError(null);
    }
    onOpenChange(next);
  };

  const submit = async () => {
    if (!account) return;
    const trimmed = group.trim();
    if (trimmed === RESERVED_GROUP_CODE) {
      setError(t("reservedGroupCode"));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      // Assigning to a group the engine doesn't know yet creates it on the fly,
      // so the operator never has to pre-register a group before using it.
      if (trimmed.length > 0 && !groupSuggestions.includes(trimmed)) {
        try {
          await createGroup(trimmed, "", "");
        } catch (err) {
          // The group may already exist outside the loaded suggestions, which
          // the assignment below handles. Every other failure is real and must
          // reach the operator instead of being swallowed here.
          if (!(err instanceof ApiError) || err.code !== "conflict") {
            throw err;
          }
        }
      }
      const updated = await setAccountGroup(account.code, trimmed, "reject");
      onOpenChange(false);
      setGroup("");
      onDone(updated);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("assignGroup.titlePrefix")}{" "}
            <span className="nums text-accent">{account?.code}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">{t("assignGroup.description")}</p>
        <div className="space-y-2">
          <Label htmlFor="assign-group">{t("assignGroup.groupLabel")}</Label>
          <Autocomplete
            id="assign-group"
            value={group}
            onChange={setGroup}
            suggestions={groupSuggestions}
            placeholder="equity-desks"
            autoFocus
            spellCheck={false}
            disabled={busy}
            onClear={() => setGroup("")}
            clearLabel={t("filters.clearField")}
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
            {t("assignGroup.cancel")}
          </Button>
          <Button size="sm" onClick={() => void submit()} disabled={busy}>
            <Folder className="h-3.5 w-3.5" />
            {group.trim().length > 0
              ? t("assignGroup.assign")
              : t("assignGroup.clear")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function AccountCurrencyDialog({
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
  const assetSuggestions = useAssetCodeSuggestions(currency, open);

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
                {currencyText(account.effectiveCurrency ?? "")}
                {account.currencyOrigin
                  ? ` · ${t(`editCurrency.origins.${account.currencyOrigin}`)}`
                  : ""}
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
              suggestions={assetSuggestions}
              disabled={busy}
              placeholder="USD"
              spellCheck={false}
              onClear={() => setCurrency("")}
              clearLabel={t("filters.clearField")}
            />
            <p className="text-[0.6875rem] text-muted">
              {t("editCurrency.currencyHint")}
            </p>
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

function GroupCurrencyDialog({
  group,
  open,
  onOpenChange,
  onDone,
}: {
  group: Group;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: () => void;
}) {
  const { t } = useTranslation("accounts");
  const { setDefaultGroupCurrency, setGroupCurrency } = useOfficerApi();
  const [currency, setCurrency] = useState(group.currency ?? "");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const assetSuggestions = useAssetCodeSuggestions(currency, open);
  const hasOpenPositions = (group.positionCount ?? 0) > 0;

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      if (group.code === DEFAULT_GROUP_CODE) {
        await setDefaultGroupCurrency(currency.trim());
      } else {
        await setGroupCurrency(group.code, currency.trim());
      }
      onDone();
    } catch (err) {
      setError(errMessage(err));
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("editGroupCurrency.title")}</DialogTitle>
          <DialogDescription>
            {group.code === DEFAULT_GROUP_CODE
              ? t("editGroupCurrency.defaultDescription")
              : t("editGroupCurrency.description", { group: group.code })}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          {hasOpenPositions && (
            <p className="text-xs text-[var(--warning)]">
              {t("editGroupCurrency.blockedHint")}
            </p>
          )}
          <div className="space-y-2">
            <Label htmlFor="group-currency">
              {t("editGroupCurrency.currencyLabel")}
            </Label>
            <Autocomplete
              id="group-currency"
              value={currency}
              onChange={setCurrency}
              suggestions={assetSuggestions}
              disabled={busy}
              placeholder="USD"
              spellCheck={false}
              onClear={() => setCurrency("")}
              clearLabel={t("filters.clearField")}
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
            {t("editGroupCurrency.cancel")}
          </Button>
          <Button size="sm" onClick={() => void submit()} disabled={busy}>
            {t("editGroupCurrency.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Edit account notes dialog
// ---------------------------------------------------------------------------

function EditAccountNotesDialog({
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
  const { setAccountNotes } = useOfficerApi();
  const [notes, setNotes] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    // Reseed the notes field from the edited account when the dialog opens.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (open) setNotes(account?.notes ?? "");
  }, [open, account]);

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      setNotes("");
      setError(null);
    }
    onOpenChange(next);
  };

  const submit = async () => {
    if (!account) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await setAccountNotes(account.code, notes);
      onOpenChange(false);
      setNotes("");
      onDone(updated);
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("editAccountNotes.titlePrefix")}{" "}
            <span className="nums text-accent">{account?.code}</span>
          </DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-lt">
          {t("editAccountNotes.description")}
        </p>
        <div className="space-y-2">
          <Label htmlFor="account-notes">
            {t("editAccountNotes.notesLabel")}
          </Label>
          <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_2rem]">
            <Textarea
              id="account-notes"
              value={notes}
              autoFocus
              rows={4}
              onChange={(e) => setNotes(e.target.value)}
            />
            <ClearFieldButton
              label={t("filters.clearField")}
              onClick={() => setNotes("")}
              disabled={busy || notes.length === 0}
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
            {t("editAccountNotes.cancel")}
          </Button>
          <Button size="sm" onClick={() => void submit()} disabled={busy}>
            {t("editAccountNotes.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Block details dialog
// ---------------------------------------------------------------------------

type BlockedDetailsTarget =
  | { kind: "account"; account: Account }
  | { kind: "group"; group: Group };

function blockedDetailsCode(target: BlockedDetailsTarget): string {
  return target.kind === "account" ? target.account.code : target.group.code;
}

function blockedDetailsReason(target: BlockedDetailsTarget): string {
  return target.kind === "account"
    ? target.account.blockReason
    : target.group.blockReason;
}

// The unblock addresses the target's own tier only. An account blocked solely
// through its group holds no block of its own, so there is nothing here to
// lift and the operator must unblock the group instead.
function unblockableTarget(target: BlockedDetailsTarget): boolean {
  return target.kind === "group" || target.account.accountBlocked;
}

// A group-sourced block is recorded in the group's rows, so a group target and
// an account blocked through its group both address the audit by the group
// handle; an account filter would select rows this block never wrote.
function blockedDetailsAuditGroup(
  target: BlockedDetailsTarget | null,
): string | null {
  if (target === null) return null;
  if (target.kind === "group") return target.group.code;
  return target.account.blockSource === "group" ? target.account.group : null;
}

function BlockedDetailsDialog({
  target,
  open,
  onOpenChange,
  onUnblock,
}: {
  target: BlockedDetailsTarget | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onUnblock: (target: BlockedDetailsTarget) => void;
}) {
  const { t } = useTranslation("accounts");
  const { fetchAudit } = useOfficerApi();
  const navigate = useNavigate();
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open || target === null) {
      return;
    }

    let cancelled = false;
    const code = blockedDetailsCode(target);

    const load = async () => {
      setBusy(true);
      setError(null);
      try {
        let next: AuditEntry[] = [];
        if (target.kind === "account") {
          const { account } = target;
          if (account.blockSource === "group") {
            // The block was never written on this account, so its own rows
            // cannot carry it; the group's rows are its only true record.
            next = await fetchAudit({
              group: account.group,
              actions: ["block_group"],
              limit: 3,
            });
          } else {
            next = await fetchAudit({
              account: code,
              actions: ["block"],
              limit: 1,
            });
            if (next.length === 0) {
              next = await fetchAudit({ account: code, limit: 3 });
            }
          }
        } else {
          // A group's rows are selected by the structured group handle. The
          // free-form detail text is not a handle: one group code can appear
          // inside another's detail, so matching it can show a different
          // group's block as this group's.
          next = await fetchAudit({
            group: code,
            actions: ["block_group"],
            limit: 3,
          });
          if (next.length === 0) {
            next = await fetchAudit({ group: code, limit: 3 });
          }
        }
        // No realm-wide fallback: rows that belong to another target are not
        // this block's record, and presenting them as one misattributes it.
        if (!cancelled) {
          setEntries(next.slice(0, 3));
        }
      } catch (err) {
        if (!cancelled) {
          setError(errMessage(err));
        }
      } finally {
        if (!cancelled) {
          setBusy(false);
        }
      }
    };

    void load();

    return () => {
      cancelled = true;
    };
  }, [fetchAudit, open, target]);

  const changeOpen = (next: boolean) => {
    if (!next) {
      setEntries([]);
      setError(null);
      setBusy(false);
    }
    onOpenChange(next);
  };

  const targetAccount = target?.kind === "account" ? target.account : null;
  const auditGroup = blockedDetailsAuditGroup(target);
  const auditAccount = target === null ? "" : blockedDetailsCode(target);
  const auditPath =
    auditGroup !== null
      ? `/audit?group=${encodeURIComponent(auditGroup)}&actions=block_group`
      : `/audit?account=${encodeURIComponent(auditAccount)}&actions=block`;

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {target?.kind === "account"
              ? t("blockDetails.accountTitle")
              : t("blockDetails.groupTitle")}{" "}
            <span className="nums text-accent">
              {target === null ? "" : blockedDetailsCode(target)}
            </span>
          </DialogTitle>
          <DialogDescription>{t("blockDetails.description")}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {/* "none" means the backend named no tier - against a build that
              predates blockSource every value degrades to it - so the section
              is dropped rather than claiming a tier it does not know. */}
          {targetAccount !== null && targetAccount.blockSource !== "none" && (
            <section className="space-y-2">
              <p className="text-[0.6875rem] font-bold uppercase tracking-[0.07em] text-muted">
                {t("blockDetails.source")}
              </p>
              <p className="text-xs text-text">
                {targetAccount.blockSource === "group"
                  ? t("blockDetails.sourceGroup", {
                      group: targetAccount.group,
                    })
                  : t("blockDetails.sourceAccount")}
              </p>
              {targetAccount.blockSource === "account" &&
                targetAccount.groupBlocked && (
                  <p className="text-xs text-muted-lt">
                    {t("blockDetails.alsoGroupBlocked", {
                      group: targetAccount.group,
                    })}
                  </p>
                )}
            </section>
          )}

          <section className="space-y-2">
            <p className="text-[0.6875rem] font-bold uppercase tracking-[0.07em] text-muted">
              {t("blockDetails.reason")}
            </p>
            <p className="max-h-40 overflow-auto whitespace-pre-wrap rounded-card border border-border bg-surface-2 p-3 text-xs text-text">
              {target === null
                ? ""
                : blockedDetailsReason(target) || t("blockDetails.noReason")}
            </p>
          </section>

          <section className="space-y-2">
            <p className="text-[0.6875rem] font-bold uppercase tracking-[0.07em] text-muted">
              {t("blockDetails.audit")}
            </p>
            {busy ? (
              <p className="text-xs text-muted-lt">
                {t("blockDetails.auditLoading")}
              </p>
            ) : error !== null ? (
              <ErrorBanner message={error} onDismiss={() => setError(null)} />
            ) : entries.length === 0 ? (
              <p className="text-xs text-muted-lt">
                {t("blockDetails.noAudit")}
              </p>
            ) : (
              <div className="space-y-2">
                {entries.map((entry) => (
                  <div
                    key={entry.id}
                    className="rounded-card border border-border bg-surface-2 p-3 text-xs"
                  >
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="nums text-muted-lt">
                        {formatDateTime(entry.at)}
                      </span>
                      <Badge variant="danger">{entry.action}</Badge>
                      {entry.source && (
                        <Badge variant="neutral">{entry.source}</Badge>
                      )}
                    </div>
                    <p className="mt-2 text-text">
                      {entry.detail || t("blockDetails.noAuditDetail")}
                    </p>
                    {(entry.actorTitle || entry.actor) && (
                      <p className="mt-1 text-muted-lt">
                        {t("blockDetails.actor")}{" "}
                        {entry.actorTitle || entry.actor}
                      </p>
                    )}
                  </div>
                ))}
              </div>
            )}
          </section>
        </div>

        {/* No unblock is offered for a block this account does not hold, so
            the page has to say where the block actually lives. */}
        {targetAccount !== null && targetAccount.blockSource === "group" && (
          <p className="text-xs text-[var(--warn)]">
            {t("blockDetails.unblockGroupBlocked", {
              group: targetAccount.group,
            })}
          </p>
        )}

        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => changeOpen(false)}>
            {t("blockDetails.close")}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              changeOpen(false);
              navigate(auditPath);
            }}
          >
            {t("blockDetails.openAudit")}
          </Button>
          {target !== null && unblockableTarget(target) && (
            <Button
              size="sm"
              onClick={() => {
                onUnblock(target);
              }}
            >
              <CircleCheck className="h-3.5 w-3.5" />
              {t("blockDetails.unblock")}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Groups panel
// ---------------------------------------------------------------------------

// Sentinel code used internally to represent the "Default group" row.
const DEFAULT_GROUP_CODE = "";
const STATUS_COLUMN_CLASS = "accounts-status-cell w-px whitespace-nowrap";

// Notes wrap to a few lines when the density mode leaves vertical room, and
// stay single-line (truncated) in the terminal/dense mode.
const NOTES_CLAMP_LINES: Record<DensityMode, number> = {
  comfortable: 3,
  compact: 2,
  terminal: 1,
};

function NotesText({ text }: { text: string }) {
  const { density } = useDisplayPreferences();
  const lines = NOTES_CLAMP_LINES[density];
  if (lines <= 1) {
    return <span className="truncate">{text}</span>;
  }
  const style: CSSProperties = {
    display: "-webkit-box",
    WebkitLineClamp: lines,
    WebkitBoxOrient: "vertical",
    overflow: "hidden",
  };
  return (
    <span className="min-w-0" style={style}>
      {text}
    </span>
  );
}

type GroupRow =
  | {
      kind: "default";
      group: Group;
      memberCount: number;
      positionCount: number;
    }
  | { kind: "real"; group: Group; memberCount: number; positionCount: number };
type RealGroupRow = Extract<GroupRow, { kind: "real" }>;

function groupDisplayTitle(group: Group): string {
  return group.title;
}

function groupDisplayName(group: Group): string {
  return group.title !== "" ? group.title : group.code;
}

function useAssetCodeSuggestions(query: string, enabled: boolean): string[] {
  const api = useOfficerApi();
  const fetchAssets =
    typeof api.fetchAssets === "function" ? api.fetchAssets : undefined;
  const debouncedQuery = useDebouncedValue(query, DEFAULT_SEARCH_DEBOUNCE_MS);
  const trimmed = debouncedQuery.trim();
  const canFetch = enabled && fetchAssets && trimmed !== "";
  const [codes, setCodes] = useState<string[]>([]);
  useEffect(() => {
    if (!canFetch) {
      return;
    }
    const controller = new AbortController();
    void fetchAssets(
      {
        code: trimmed,
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      controller.signal,
    )
      .then((assets) => {
        setCodes(assets.map((asset) => asset.code));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error(err);
          setCodes([]);
        }
      });
    return () => {
      controller.abort();
    };
  }, [canFetch, fetchAssets, trimmed]);
  return canFetch ? codes : [];
}

function currencyText(currency: string): string {
  return currency.trim() === "" ? "—" : currency;
}

function pnlText(pnl: string | undefined): string {
  return pnl && pnl !== "" ? pnl : "—";
}

function pnlHaltText(t: TFunction, reason: string): string | null {
  if (!reason) return null;
  switch (reason) {
    case "missing_fx":
      return t("accounts.pnlHalt.missingFx");
    case "missing_account_currency":
      return t("accounts.pnlHalt.missingAccountCurrency");
    case "missing_initial_pnl":
      return t("accounts.pnlHalt.missingInitialPnl");
    case "missing_cost_basis":
      return t("accounts.pnlHalt.missingCostBasis");
    case "arithmetic_overflow":
      return t("accounts.pnlHalt.arithmeticOverflow");
    default:
      return t("accounts.pnlHalt.unknown");
  }
}

function pnlClass(pnl: string | undefined): string {
  const value = (pnl ?? "").trim();
  if (value === "" || /^0+(\.0+)?$/.test(value)) {
    return "text-[var(--pnl-flat)]";
  }
  if (value.startsWith("-") || value.startsWith("−")) {
    return "text-[var(--pnl-neg)]";
  }
  return "text-[var(--pnl-pos)]";
}

function CurrencyCell({
  currency,
  title,
  origin,
  originTitle,
  editTitle,
  noneLabel,
  onEdit,
}: {
  currency: string;
  title: string;
  origin?: string;
  originTitle?: string;
  editTitle: string;
  noneLabel?: string;
  onEdit: () => void;
}) {
  return (
    <div className="flex min-w-0 items-center gap-1" title={title}>
      <span
        className={cn(
          "nums min-w-0 truncate text-xs",
          currency === "" ? "italic text-muted" : "text-muted-lt",
        )}
        aria-label={currency === "" ? noneLabel : undefined}
      >
        {currencyText(currency)}
      </span>
      {origin && origin !== "account" && (
        <span className="inline-flex shrink-0" title={originTitle}>
          <AccountCurrencyOriginIcon origin={origin} />
        </span>
      )}
      <EditButton
        size={28}
        style={{ marginLeft: "auto" }}
        onClick={onEdit}
        title={editTitle}
      />
    </div>
  );
}

function AccountCurrencyOriginIcon({ origin }: { origin?: string }) {
  if (origin === "group") {
    return (
      <Users aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-muted" />
    );
  }
  if (origin === "default") {
    return (
      <Globe2 aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-muted" />
    );
  }
  return null;
}

function GroupsPanel({
  groupRows,
  selectedGroupCode,
  activeSort,
  activeOrder,
  onSelect,
  onSortChange,
  onEdit,
  onEditCurrency,
  onEditNotes,
  onBlock,
  onUnblock,
  onShowBlockDetails,
  onDelete,
}: {
  groupRows: GroupRow[];
  selectedGroupCode: string | null;
  activeSort?: string;
  activeOrder?: SortOrder;
  onSelect: (code: string | null) => void;
  onSortChange: (sort?: string, order?: SortOrder) => void;
  onEdit: (group: Group) => void;
  onEditCurrency: (group: Group) => void;
  onEditNotes: (group: Group) => void;
  onBlock: (group: Group) => void;
  onUnblock: (group: Group) => void;
  onShowBlockDetails: (group: Group) => void;
  onDelete: (group: Group) => void;
}) {
  const { t } = useTranslation("accounts");
  const { t: tc } = useTranslation("common");
  return (
    <Table className="min-w-[58rem]">
      <colgroup>
        <col className="w-[14rem]" />
        <col className="w-[5rem]" />
        <col className="w-[2.5rem]" />
        <col className="w-[7rem]" />
        <col className={STATUS_COLUMN_CLASS} />
        <col />
        <col className="w-[8.5rem]" />
      </colgroup>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead className="w-[14rem]">
            <SortableHeader
              field="code"
              label={t("groups.columns.group")}
              description={t("groups.columnDescriptions.group")}
              direction={sortDirection(activeSort, activeOrder, "code")}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead className="w-[5rem]">
            <ColumnHeader description={t("groups.columnDescriptions.accounts")}>
              {t("groups.columns.accounts")}
            </ColumnHeader>
          </TableHead>
          <TableHead className="w-[2.5rem]">
            <ColumnHeader
              description={t("groups.columnDescriptions.positions")}
            >
              {t("groups.columns.positions")}
            </ColumnHeader>
          </TableHead>
          <TableHead className="w-[7rem]">
            <ColumnHeader description={t("groups.columnDescriptions.currency")}>
              {t("groups.columns.currency")}
            </ColumnHeader>
          </TableHead>
          <TableHead className={STATUS_COLUMN_CLASS}>
            <SortableHeader
              field="status"
              label={t("groups.columns.status")}
              description={t("groups.columnDescriptions.status")}
              direction={sortDirection(activeSort, activeOrder, "status")}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead>
            <ColumnHeader description={t("groups.columnDescriptions.notes")}>
              {t("groups.columns.notes")}
            </ColumnHeader>
          </TableHead>
          <TableHead className="w-[8.5rem] text-right">
            <ColumnHeader
              align="right"
              description={t("groups.columnDescriptions.actions")}
            >
              {t("groups.columns.actions")}
            </ColumnHeader>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {groupRows.map((row) => {
          const code =
            row.kind === "default" ? DEFAULT_GROUP_CODE : row.group.code;
          const isSelected = selectedGroupCode === code;
          const isBlocked = row.kind === "real" && row.group.blocked;
          const title = row.kind === "real" ? groupDisplayTitle(row.group) : "";
          return (
            <TableRow
              key={code === DEFAULT_GROUP_CODE ? "__default__" : code}
              className={cn(
                isBlocked && "bg-accent-dim",
                isSelected && "ring-1 ring-inset ring-ring",
              )}
            >
              <TableCell className="w-[14rem] font-medium">
                {row.kind === "default" ? (
                  <span className="text-muted-lt italic">
                    {t("groups.defaultGroup")}
                  </span>
                ) : (
                  <div className="flex items-start gap-1">
                    <span className="flex min-w-0 flex-col">
                      {title !== "" && (
                        <span className="nums truncate">{title}</span>
                      )}
                      {title !== "" && (
                        <IdCell
                          value={row.group.code}
                          copyTitle={tc("rowActions.copyIdTitle", {
                            entity: row.group.code,
                          })}
                          copiedTitle={tc("rowActions.copiedId")}
                          gap={4}
                        >
                          <span className="text-[0.6875rem] text-accent">
                            {row.group.code}
                          </span>
                        </IdCell>
                      )}
                      {title === "" && (
                        <IdCell
                          value={row.group.code}
                          copyTitle={tc("rowActions.copyIdTitle", {
                            entity: row.group.code,
                          })}
                          copiedTitle={tc("rowActions.copiedId")}
                          gap={4}
                        />
                      )}
                    </span>
                    <span className="ml-auto flex shrink-0 items-center">
                      <EditButton
                        size={28}
                        onClick={() => onEdit(row.group)}
                        title={t("groups.actions.editTitle")}
                      />
                    </span>
                  </div>
                )}
              </TableCell>

              <TableCell className="w-[5rem] text-xs text-muted-lt">
                {row.memberCount}
              </TableCell>

              <TableCell className="w-[2.5rem] text-xs text-muted-lt">
                <span className="nums">{row.positionCount}</span>
              </TableCell>

              <TableCell className="w-[7rem]">
                <CurrencyCell
                  currency={row.group.currency ?? ""}
                  title={t("groups.currencyCell.title")}
                  editTitle={t("groups.actions.editCurrencyTitle")}
                  onEdit={() => onEditCurrency(row.group)}
                />
              </TableCell>

              <TableCell
                className={STATUS_COLUMN_CLASS}
                title={
                  row.kind === "real"
                    ? row.group.blockReason || undefined
                    : undefined
                }
              >
                <div className="flex items-center gap-1">
                  {row.kind === "real" && row.group.blocked ? (
                    <Badge variant="danger" className="shrink-0">
                      <StatusDot tone="danger" />
                      {t("groups.status.blocked")}
                    </Badge>
                  ) : (
                    <Badge variant="ok" className="shrink-0">
                      <StatusDot tone="ok" />
                      {t("groups.status.active")}
                    </Badge>
                  )}
                  {row.kind === "real" && (
                    <div className="accounts-status-action-area">
                      {row.group.blocked && (
                        <ActionButton
                          icon="view"
                          size={28}
                          title={t("groups.actions.viewBlockDetails")}
                          onClick={() => onShowBlockDetails(row.group)}
                        />
                      )}
                      <ActionButton
                        icon={row.group.blocked ? "check" : "block"}
                        size={28}
                        title={
                          row.group.blocked
                            ? t("groups.actions.unblock")
                            : t("groups.actions.block")
                        }
                        onClick={() => {
                          if (row.group.blocked) {
                            onUnblock(row.group);
                          } else {
                            onBlock(row.group);
                          }
                        }}
                        danger={!row.group.blocked}
                      />
                    </div>
                  )}
                </div>
              </TableCell>

              <TableCell
                className="text-xs text-muted-lt"
                title={
                  row.kind === "real" ? row.group.notes || undefined : undefined
                }
              >
                {row.kind === "real" ? (
                  <div className="flex min-w-0 items-start gap-1">
                    <NotesText text={row.group.notes || "—"} />
                    <EditButton
                      size={28}
                      style={{ marginLeft: "auto" }}
                      onClick={() => onEditNotes(row.group)}
                      title={t("groups.actions.editNotesTitle")}
                    />
                  </div>
                ) : (
                  <span className="italic">{t("groups.defaultGroupNote")}</span>
                )}
              </TableCell>

              <TableCell className="w-[8.5rem] text-right">
                {row.kind === "default" ? (
                  <RowActions>
                    <FilterByButton
                      title={tc("rowActions.filterByTitle", {
                        field: t("groups.defaultGroup"),
                      })}
                      href={absoluteAppUrl("/accounts?group=")}
                      onClick={() => onSelect(DEFAULT_GROUP_CODE)}
                    />
                    <ShareLinkButton
                      href={absoluteAppUrl("/accounts?group=")}
                      title={tc("rowActions.shareTitle", {
                        entity: t("groups.defaultGroup"),
                      })}
                      copiedTitle={tc("rowActions.copiedLink")}
                    />
                  </RowActions>
                ) : (
                  <RowActions>
                    <FilterByButton
                      title={tc("rowActions.filterByTitle", {
                        field: row.group.code,
                      })}
                      href={absoluteAppUrl(
                        `/accounts?group=${encodeURIComponent(row.group.code)}`,
                      )}
                      onClick={() => onSelect(code)}
                    />
                    <ShareLinkButton
                      href={absoluteAppUrl(
                        `/accounts?group=${encodeURIComponent(row.group.code)}`,
                      )}
                      title={tc("rowActions.shareTitle", {
                        entity: row.group.code,
                      })}
                      copiedTitle={tc("rowActions.copiedLink")}
                    />
                    <DeleteButton
                      title={tc("rowActions.deleteTitle", {
                        entity: row.group.code,
                      })}
                      onClick={() => onDelete(row.group)}
                    />
                  </RowActions>
                )}
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

// ---------------------------------------------------------------------------
// Accounts table
// ---------------------------------------------------------------------------

function AccountsTable({
  accounts,
  activeSort,
  activeOrder,
  selectedGroupCode,
  groupsByCode,
  onSortChange,
  onEditAccount,
  onAssignGroup,
  onEditCurrency,
  onEditNotes,
  onBlock,
  onUnblock,
  onShowBlockDetails,
  onOpenPositions,
  onOpenTrading,
  onOpenPolicies,
  onOpenHistory,
  onFilterGroup,
  onDelete,
}: {
  accounts: Account[];
  activeSort?: string;
  activeOrder?: SortOrder;
  selectedGroupCode: string | null;
  groupsByCode: Map<string, Group>;
  onSortChange: (sort?: string, order?: SortOrder) => void;
  onEditAccount: (account: Account) => void;
  onAssignGroup: (account: Account) => void;
  onEditCurrency: (account: Account) => void;
  onEditNotes: (account: Account) => void;
  onBlock: (account: Account) => void;
  onUnblock: (account: Account) => void;
  onShowBlockDetails: (account: Account) => void;
  onOpenPositions: (account: Account) => void;
  onOpenTrading: (account: Account) => void;
  onOpenPolicies: (account: Account) => void;
  onOpenHistory: (account: Account) => void;
  onFilterGroup: (group: string) => void;
  onDelete: (account: Account) => void;
}) {
  const { t } = useTranslation("accounts");
  const { t: tc } = useTranslation("common");
  const currencyOriginTitle = (account: Account): string | undefined => {
    if (account.currencyOrigin !== "group") {
      return account.currencyOrigin
        ? t(`accounts.currencyCell.origins.${account.currencyOrigin}`)
        : undefined;
    }
    const group = groupsByCode.get(account.group);
    return t("accounts.currencyCell.origins.groupNamed", {
      group: group ? groupDisplayName(group) : account.group,
    });
  };

  return (
    <Table className="min-w-[68rem]">
      <colgroup>
        <col className="w-[13rem]" />
        <col className="w-[9rem]" />
        <col className="w-[7rem]" />
        <col className="w-[6rem]" />
        <col className="w-[2.5rem]" />
        <col className={STATUS_COLUMN_CLASS} />
        <col />
        <col className="w-[12.5rem]" />
      </colgroup>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead className="w-[13rem]">
            <SortableHeader
              field="code"
              label={t("accounts.columns.account")}
              description={t("accounts.columnDescriptions.account")}
              direction={sortDirection(activeSort, activeOrder, "code")}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead className="w-[9rem]">
            <SortableHeader
              field="group"
              label={t("accounts.columns.group")}
              description={t("accounts.columnDescriptions.group")}
              direction={sortDirection(activeSort, activeOrder, "group")}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead className="w-[7rem]">
            <ColumnHeader
              description={t("accounts.columnDescriptions.currency")}
            >
              {t("accounts.columns.currency")}
            </ColumnHeader>
          </TableHead>
          <TableHead className="w-[6rem] text-right">
            <ColumnHeader
              align="right"
              description={t("accounts.columnDescriptions.pnl")}
            >
              {t("accounts.columns.pnl")}
            </ColumnHeader>
          </TableHead>
          <TableHead className="w-[2.5rem]">
            <SortableHeader
              field="positionCount"
              label={t("accounts.columns.positions")}
              description={t("accounts.columnDescriptions.positions")}
              direction={sortDirection(
                activeSort,
                activeOrder,
                "positionCount",
              )}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead className={STATUS_COLUMN_CLASS}>
            <SortableHeader
              field="status"
              label={t("accounts.columns.status")}
              description={t("accounts.columnDescriptions.status")}
              direction={sortDirection(activeSort, activeOrder, "status")}
              onSort={(field, next) =>
                onSortChange(
                  next === "none" ? undefined : field,
                  next === "none" ? undefined : next,
                )
              }
            />
          </TableHead>
          <TableHead>
            <ColumnHeader description={t("accounts.columnDescriptions.notes")}>
              {t("accounts.columns.notes")}
            </ColumnHeader>
          </TableHead>
          <TableHead className="w-[12.5rem] text-right">
            <ColumnHeader
              align="right"
              description={t("accounts.columnDescriptions.actions")}
            >
              {t("accounts.columns.actions")}
            </ColumnHeader>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {accounts.map((account) => {
          const title = account.title;
          const haltText = pnlHaltText(t, account.pnlHaltReason);
          return (
            <TableRow
              key={account.code}
              className={cn(account.blocked && "bg-accent-dim")}
            >
              <TableCell className="w-[13rem] font-medium">
                <div className="flex items-start gap-1">
                  <span className="flex min-w-0 flex-col">
                    <span className="nums truncate">{title}</span>
                    {title !== account.code && (
                      <IdCell
                        value={account.code}
                        copyTitle={tc("rowActions.copyIdTitle", {
                          entity: account.code,
                        })}
                        copiedTitle={tc("rowActions.copiedId")}
                        gap={4}
                      >
                        <span className="text-[0.6875rem] text-accent">
                          {account.code}
                        </span>
                      </IdCell>
                    )}
                    {title === account.code && (
                      <IdCell
                        value={account.code}
                        copyTitle={tc("rowActions.copyIdTitle", {
                          entity: account.code,
                        })}
                        copiedTitle={tc("rowActions.copiedId")}
                        gap={4}
                      />
                    )}
                  </span>
                  <span className="ml-auto flex shrink-0 items-center">
                    <EditButton
                      size={28}
                      onClick={() => onEditAccount(account)}
                      title={t("accounts.actions.editTitle")}
                    />
                  </span>
                </div>
              </TableCell>

              <TableCell className="w-[9rem]">
                <div className="flex min-w-0 items-center gap-1">
                  <span
                    className={cn(
                      "nums min-w-0 truncate text-xs",
                      selectedGroupCode === account.group
                        ? "text-accent"
                        : "text-muted-lt",
                    )}
                  >
                    {account.group ? (
                      account.group
                    ) : (
                      <span className="italic">
                        {t("accounts.groupCell.noGroup")}
                      </span>
                    )}
                  </span>
                  <span className="ml-auto flex shrink-0 items-center">
                    {account.group && selectedGroupCode !== account.group && (
                      <FilterByButton
                        size={28}
                        title={tc("rowActions.filterByTitle", {
                          field: account.group,
                        })}
                        href={absoluteAppUrl(
                          `/accounts?group=${encodeURIComponent(account.group)}`,
                        )}
                        onClick={() => onFilterGroup(account.group)}
                      />
                    )}
                    <EditButton
                      size={28}
                      onClick={() => onAssignGroup(account)}
                      title={t("accounts.groupCell.editTitle")}
                    />
                  </span>
                </div>
              </TableCell>

              <TableCell className="w-[7rem]">
                <CurrencyCell
                  currency={account.effectiveCurrency ?? ""}
                  title={t("accounts.currencyCell.title")}
                  origin={account.currencyOrigin}
                  originTitle={currencyOriginTitle(account)}
                  editTitle={t("accounts.actions.editCurrencyTitle")}
                  noneLabel={t("accounts.currencyCell.none")}
                  onEdit={() => onEditCurrency(account)}
                />
              </TableCell>

              <TableCell
                className={cn(
                  "w-[6rem] nums text-right text-xs",
                  pnlClass(account.pnl),
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
              </TableCell>

              <TableCell className="w-[2.5rem] text-xs text-muted-lt">
                <span className="nums">{account.positionCount ?? 0}</span>
              </TableCell>

              <TableCell
                className={STATUS_COLUMN_CLASS}
                title={account.blockReason || undefined}
              >
                <div className="flex items-center gap-1">
                  {/* The badge reports the effective state - what the engine
                        does with the account's orders right now. */}
                  {account.blocked ? (
                    <Badge variant="danger" className="shrink-0">
                      <StatusDot tone="danger" />
                      {t("accounts.status.blocked")}
                    </Badge>
                  ) : (
                    <Badge variant="ok" className="shrink-0">
                      <StatusDot tone="ok" />
                      {t("accounts.status.active")}
                    </Badge>
                  )}
                  <div className="accounts-status-action-area">
                    {account.blocked && (
                      <ActionButton
                        icon="view"
                        size={28}
                        onClick={() => onShowBlockDetails(account)}
                        title={t("accounts.actions.viewBlockDetails")}
                      />
                    )}
                    {/* The tiers are independent, so the action follows
                          the account's own block: an account held down only
                          by its group still needs a Block of its own. */}
                    {account.accountBlocked ? (
                      <ActionButton
                        icon="check"
                        size={28}
                        onClick={() => onUnblock(account)}
                        title={t("accounts.actions.unblock")}
                      />
                    ) : (
                      <ActionButton
                        icon="block"
                        size={28}
                        onClick={() => onBlock(account)}
                        title={t("accounts.actions.block")}
                        danger
                      />
                    )}
                  </div>
                </div>
              </TableCell>

              <TableCell
                className="text-xs text-muted-lt"
                title={account.notes || undefined}
              >
                <div className="flex min-w-0 items-start gap-1">
                  <NotesText text={account.notes || "—"} />
                  <EditButton
                    size={28}
                    style={{ marginLeft: "auto" }}
                    onClick={() => onEditNotes(account)}
                    title={t("accounts.actions.editNotesTitle")}
                  />
                </div>
              </TableCell>

              <TableCell className="w-[12.5rem] text-right">
                <RowActions>
                  <PositionsButton
                    title={t("accounts.links.positions")}
                    href={absoluteAppUrl(
                      `/positions?account=${encodeURIComponent(account.code)}`,
                    )}
                    onClick={() => onOpenPositions(account)}
                  />
                  <TradingButton
                    title={t("accounts.links.trading")}
                    href={absoluteAppUrl(
                      `/trading?account=${encodeURIComponent(account.code)}`,
                    )}
                    onClick={() => onOpenTrading(account)}
                  />
                  <PoliciesButton
                    title={t("accounts.links.policies")}
                    href={absoluteAppUrl(
                      `/policies?account=${encodeURIComponent(account.code)}`,
                    )}
                    onClick={() => onOpenPolicies(account)}
                  />
                  <HistoryButton
                    title={t("accounts.links.audit")}
                    href={absoluteAppUrl(
                      `/audit?account=${encodeURIComponent(account.code)}`,
                    )}
                    onClick={() => onOpenHistory(account)}
                  />
                  <DeleteButton
                    title={tc("rowActions.deleteTitle", {
                      entity: account.code,
                    })}
                    onClick={() => onDelete(account)}
                  />
                </RowActions>
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function Accounts() {
  const { t } = useTranslation("accounts");
  const { fetchAccounts, fetchGroups } = useOfficerApi();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const globalAccountFilter = useGlobalAccountFilter();
  // A shared deep link seeds the initial filter set for the active tab (defaults
  // to accounts); the operator owns it thereafter.
  const initialTab: AccountsTab =
    searchParams.get("tab") === "groups" ? "groups" : "accounts";
  const seedAccounts = initialTab === "accounts";
  const seedGroups = initialTab === "groups";
  // The legacy `?group=<code>` link seeds the exact-group filter on the accounts
  // tab, matching the "filter by group" action.
  const [selectedGroupCode, setSelectedGroupCode] = useState<string | null>(
    () => (seedAccounts ? searchParams.get("group") : null),
  );
  const [accountCode, setAccountCode] = useState(
    seedAccounts
      ? globalAccountFilter.account || searchParams.get("code") || ""
      : "",
  );
  const [accountCodeMatch, setAccountCodeMatch] = useState<TextMatchMode>(() =>
    seedAccounts ? textMatchFromParams(searchParams) : "contains",
  );
  const [accountStatus, setAccountStatus] = useState<StatusListFilter>(() =>
    seedAccounts ? statusFromParams(searchParams) : "all",
  );
  const [accountSort, setAccountSort] = useState<{
    sort?: string;
    order?: SortOrder;
  }>(() =>
    seedAccounts ? sortFromParams(searchParams, ACCOUNT_SORT_KEYS) : {},
  );
  const initialAccountAdvanced = useMemo(
    () =>
      seedAccounts
        ? advancedFromParams(searchParams)
        : DEFAULT_ADVANCED_FILTERS,
    [seedAccounts, searchParams],
  );
  const [accountAdvancedDraft, setAccountAdvancedDraft] =
    useState<AdvancedListFilters>(initialAccountAdvanced);
  const [accountAdvanced, setAccountAdvanced] = useState<AdvancedListFilters>(
    initialAccountAdvanced,
  );
  const [accountAdvancedOpen, setAccountAdvancedOpen] = useState(false);
  const [accountGroupSearch, setAccountGroupSearch] = useState("");
  const accountGroupSearchRef = useRef(accountGroupSearch);
  const [groupCode, setGroupCode] = useState(
    seedGroups ? (searchParams.get("code") ?? "") : "",
  );
  const [groupCodeMatch, setGroupCodeMatch] = useState<TextMatchMode>(() =>
    seedGroups ? textMatchFromParams(searchParams) : "contains",
  );
  const [groupStatus, setGroupStatus] = useState<StatusListFilter>(() =>
    seedGroups ? statusFromParams(searchParams) : "all",
  );
  const [groupSort, setGroupSort] = useState<{
    sort?: string;
    order?: SortOrder;
  }>(() => (seedGroups ? sortFromParams(searchParams, GROUP_SORT_KEYS) : {}));
  const initialGroupAdvanced = useMemo(
    () =>
      seedGroups ? advancedFromParams(searchParams) : DEFAULT_ADVANCED_FILTERS,
    [seedGroups, searchParams],
  );
  const [groupAdvancedDraft, setGroupAdvancedDraft] =
    useState<AdvancedListFilters>(initialGroupAdvanced);
  const [groupAdvanced, setGroupAdvanced] =
    useState<AdvancedListFilters>(initialGroupAdvanced);
  const [groupAdvancedOpen, setGroupAdvancedOpen] = useState(false);
  const debouncedAccountCode = useDebouncedValue(
    accountCode,
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedAccountGroupSearch = useDebouncedValue(
    accountGroupSearch,
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedGroupCode = useDebouncedValue(
    groupCode,
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const [accountCodeSuggestions, setAccountCodeSuggestions] = useState<
    string[]
  >([]);
  const [accountGroupSuggestions, setAccountGroupSuggestions] = useState<
    string[]
  >([]);
  const [groupCodeSuggestions, setGroupCodeSuggestions] = useState<string[]>(
    [],
  );
  const visibleAccountCodeSuggestions =
    debouncedAccountCode.trim() === "" ? [] : accountCodeSuggestions;
  const visibleAccountGroupSuggestions =
    debouncedAccountGroupSearch.trim() === "" ? [] : accountGroupSuggestions;
  const visibleGroupCodeSuggestions =
    debouncedGroupCode.trim() === "" ? [] : groupCodeSuggestions;

  useEffect(() => {
    const query = debouncedAccountCode.trim();
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
      .then((items) =>
        setAccountCodeSuggestions(items.map((account) => account.code)),
      )
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error(err);
          setAccountCodeSuggestions([]);
        }
      });
    return () => controller.abort();
  }, [debouncedAccountCode, fetchAccounts]);

  useEffect(() => {
    const query = debouncedAccountGroupSearch.trim();
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
      .then((items) => {
        const suggestions = items
          .map((group) => group.code)
          .filter((code) => code !== DEFAULT_GROUP_CODE);
        setAccountGroupSuggestions(suggestions);
        const exact = accountGroupSearchRef.current.trim();
        if (exact !== "" && suggestions.includes(exact)) {
          setSelectedGroupCode(exact);
        }
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error(err);
          setAccountGroupSuggestions([]);
        }
      });
    return () => controller.abort();
  }, [debouncedAccountGroupSearch, fetchGroups]);

  useEffect(() => {
    const query = debouncedGroupCode.trim();
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
      .then((items) =>
        setGroupCodeSuggestions(
          items
            .map((group) => group.code)
            .filter((code) => code !== DEFAULT_GROUP_CODE),
        ),
      )
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error(err);
          setGroupCodeSuggestions([]);
        }
      });
    return () => controller.abort();
  }, [debouncedGroupCode, fetchGroups]);

  const accountFilters = useMemo<AccountListFilters>(() => {
    const blockReason = trimmedOrUndefined(accountAdvanced.blockReason);
    const filters: AccountListFilters = {
      code: trimmedOrUndefined(debouncedAccountCode),
      codeMatch: accountCodeMatch,
      status: accountStatus,
      ...positionCountQuery(accountAdvanced),
    };
    if (blockReason !== undefined) {
      filters.blockReason = blockReason;
      filters.blockReasonMatch = accountAdvanced.blockReasonMatch;
    }
    if (selectedGroupCode !== null) {
      filters.group = selectedGroupCode;
    }
    return filters;
  }, [
    accountAdvanced,
    accountCodeMatch,
    accountStatus,
    debouncedAccountCode,
    selectedGroupCode,
  ]);
  const groupFilters = useMemo<GroupListFilters>(() => {
    const notes = trimmedOrUndefined(groupAdvanced.notes);
    const blockReason = trimmedOrUndefined(groupAdvanced.blockReason);
    const filters: GroupListFilters = {
      code: trimmedOrUndefined(debouncedGroupCode),
      codeMatch: groupCodeMatch,
      status: groupStatus,
      ...positionCountQuery(groupAdvanced),
      ...accountCountQuery(groupAdvanced),
    };
    if (notes !== undefined) {
      filters.notes = notes;
      filters.notesMatch = groupAdvanced.notesMatch;
    }
    if (blockReason !== undefined) {
      filters.blockReason = blockReason;
      filters.blockReasonMatch = groupAdvanced.blockReasonMatch;
    }
    return filters;
  }, [debouncedGroupCode, groupAdvanced, groupCodeMatch, groupStatus]);
  const [accountPage, setAccountPage] = useState(0);
  const [accountSize, setAccountSize] = usePersistentPageSize(
    "pit-officer-accounts-page-size",
  );
  const [groupPage, setGroupPage] = useState(0);
  const [groupSize, setGroupSize] = usePersistentPageSize(
    "pit-officer-groups-page-size",
  );
  const accountGlobalLocked =
    globalAccountFilter.account !== "" &&
    accountCode.trim() === globalAccountFilter.account;
  const accountGlobalToggle = {
    active:
      accountCode.trim() !== "" &&
      accountCode.trim() === globalAccountFilter.account,
    disabled: accountCode.trim() === "",
    activeLabel: t("common:filters.globalAccount.active"),
    inactiveLabel: t("common:filters.globalAccount.inactive"),
    disabledLabel: t("common:filters.globalAccount.disabled"),
    onToggle: () => {
      const nextAccount = accountCode.trim();
      if (nextAccount === "") {
        return;
      }
      if (globalAccountFilter.account === nextAccount) {
        globalAccountFilter.clear();
        return;
      }
      globalAccountFilter.setAccount(nextAccount);
      setAccountCode(nextAccount);
      setAccountPage(0);
    },
  };

  useEffect(() => {
    const nextAccount = globalAccountFilter.account;
    if (nextAccount === "") {
      return;
    }
    // This page keeps filter draft state locally; the external global-account
    // store is the source we intentionally mirror when it changes.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setAccountCode(nextAccount);
    setAccountPage(0);
  }, [globalAccountFilter.account]);
  const accountListFilters = useMemo<AccountListFilters>(
    () => ({
      ...accountFilters,
      sort:
        accountSort.sort !== undefined &&
        ACCOUNT_SORT_KEYS.has(accountSort.sort)
          ? accountSort.sort
          : undefined,
      order:
        accountSort.sort !== undefined &&
        ACCOUNT_SORT_KEYS.has(accountSort.sort)
          ? accountSort.order
          : undefined,
      limit: accountSize,
      offset: accountPage * accountSize,
    }),
    [
      accountFilters,
      accountPage,
      accountSize,
      accountSort.order,
      accountSort.sort,
    ],
  );
  const groupListFilters = useMemo<GroupListFilters>(
    () => ({
      ...groupFilters,
      sort:
        groupSort.sort !== undefined && GROUP_SORT_KEYS.has(groupSort.sort)
          ? groupSort.sort
          : undefined,
      order:
        groupSort.sort !== undefined && GROUP_SORT_KEYS.has(groupSort.sort)
          ? groupSort.order
          : undefined,
      limit: groupSize,
      offset: groupPage * groupSize,
    }),
    [groupFilters, groupPage, groupSize, groupSort.order, groupSort.sort],
  );
  const { load: accountsLoad, reload: reloadAccounts } =
    useAccountsPage(accountListFilters);
  const { load: groupsLoad, reload: reloadGroups } =
    useGroupsPage(groupListFilters);
  const accountFilterKey = useMemo(
    () => JSON.stringify(accountListFilters),
    [accountListFilters],
  );
  const groupFilterKey = useMemo(
    () => JSON.stringify(groupListFilters),
    [groupListFilters],
  );
  const accountAdvancedSummary = useMemo(
    () => advancedFilterSummary(accountAdvanced, t, false),
    [accountAdvanced, t],
  );
  const groupAdvancedSummary = useMemo(
    () => advancedFilterSummary(groupAdvanced, t),
    [groupAdvanced, t],
  );

  // Encode each tab's active filter set as a shareable deep link. Only
  // non-default params are emitted, mirroring the API param names; the active
  // tab's link is the one surfaced in the filter bar.
  const accountShareHref = useMemo(() => {
    const query = new URLSearchParams();
    const codeValue = trimmedOrUndefined(accountCode);
    if (codeValue !== undefined) {
      query.set("code", codeValue);
      if (accountCodeMatch !== "contains") {
        query.set("codeMatch", accountCodeMatch);
      }
    }
    if (accountStatus !== "all") {
      query.set("status", accountStatus);
    }
    if (selectedGroupCode !== null) {
      query.set("group", selectedGroupCode);
    }
    appendAdvancedParams(query, accountAdvanced, "accounts");
    if (
      accountSort.sort !== undefined &&
      ACCOUNT_SORT_KEYS.has(accountSort.sort)
    ) {
      query.set("sort", accountSort.sort);
      if (accountSort.order !== undefined) {
        query.set("order", accountSort.order);
      }
    }
    return shareUrl("/accounts", query);
  }, [
    accountAdvanced,
    accountCode,
    accountCodeMatch,
    accountSort.order,
    accountSort.sort,
    accountStatus,
    selectedGroupCode,
  ]);
  const clearGroupFilters = () => {
    setGroupCode("");
    setGroupCodeMatch("contains");
    setGroupStatus("all");
    setGroupAdvanced(DEFAULT_ADVANCED_FILTERS);
    setGroupAdvancedDraft(DEFAULT_ADVANCED_FILTERS);
    setGroupPage(0);
  };
  const clearAccountFilters = () => {
    if (!accountGlobalLocked) {
      setAccountCode("");
      setAccountCodeMatch("contains");
    }
    setAccountStatus("all");
    setSelectedGroupCode(null);
    accountGroupSearchRef.current = "";
    setAccountGroupSearch("");
    setAccountAdvanced(DEFAULT_ADVANCED_FILTERS);
    setAccountAdvancedDraft(DEFAULT_ADVANCED_FILTERS);
    setAccountPage(0);
  };
  const groupShareHref = useMemo(() => {
    const query = new URLSearchParams();
    query.set("tab", "groups");
    const codeValue = trimmedOrUndefined(groupCode);
    if (codeValue !== undefined) {
      query.set("code", codeValue);
      if (groupCodeMatch !== "contains") {
        query.set("codeMatch", groupCodeMatch);
      }
    }
    if (groupStatus !== "all") {
      query.set("status", groupStatus);
    }
    appendAdvancedParams(query, groupAdvanced, "groups");
    if (groupSort.sort !== undefined && GROUP_SORT_KEYS.has(groupSort.sort)) {
      query.set("sort", groupSort.sort);
      if (groupSort.order !== undefined) {
        query.set("order", groupSort.order);
      }
    }
    return shareUrl("/accounts", query);
  }, [
    groupAdvanced,
    groupCode,
    groupCodeMatch,
    groupSort.order,
    groupSort.sort,
    groupStatus,
  ]);

  // Local snapshots so individual rows update immediately from server responses.
  const [localAccounts, setLocalAccounts] = useState<{
    key: string;
    data: Account[];
  } | null>(null);
  const [localGroups, setLocalGroups] = useState<{
    key: string;
    data: Group[];
  } | null>(null);

  const accounts =
    localAccounts?.key === accountFilterKey
      ? localAccounts.data
      : accountsLoad.state === "ready"
        ? accountsLoad.data.items
        : null;
  const groups =
    localGroups?.key === groupFilterKey
      ? localGroups.data
      : groupsLoad.state === "ready"
        ? groupsLoad.data.items
        : null;

  const groupSuggestions: string[] =
    groups?.map((g) => g.code).filter((code) => code !== DEFAULT_GROUP_CODE) ??
    [];

  // Which group row is highlighted; null = show all accounts.
  const [tab, setTab] = useState<AccountsTab>(initialTab);

  // Account dialog targets.
  const [editAccountTarget, setEditAccountTarget] = useState<Account | null>(
    null,
  );
  const [blockAccountTarget, setBlockAccountTarget] = useState<Account | null>(
    null,
  );
  const [unblockAccountTarget, setUnblockAccountTarget] =
    useState<Account | null>(null);
  const [groupTarget, setGroupTarget] = useState<Account | null>(null);
  const [currencyAccountTarget, setCurrencyAccountTarget] =
    useState<Account | null>(null);
  const [notesAccountTarget, setNotesAccountTarget] = useState<Account | null>(
    null,
  );
  const [deleteAccountTarget, setDeleteAccountTarget] =
    useState<Account | null>(null);
  const [blockedDetailsTarget, setBlockedDetailsTarget] =
    useState<BlockedDetailsTarget | null>(null);

  // Group dialog targets.
  const [editGroupTarget, setEditGroupTarget] = useState<Group | null>(null);
  const [groupCurrencyTarget, setGroupCurrencyTarget] = useState<Group | null>(
    null,
  );
  const [groupNotesTarget, setGroupNotesTarget] = useState<Group | null>(null);
  const [blockGroupTarget, setBlockGroupTarget] = useState<Group | null>(null);
  const [unblockGroupTarget, setUnblockGroupTarget] = useState<Group | null>(
    null,
  );
  const [deleteGroupTarget, setDeleteGroupTarget] = useState<Group | null>(
    null,
  );

  function removeAccount(code: string) {
    setLocalAccounts((prev) => {
      const base =
        prev?.key === accountFilterKey
          ? prev.data
          : accountsLoad.state === "ready"
            ? accountsLoad.data.items
            : [];
      return {
        key: accountFilterKey,
        data: base.filter((a) => a.code !== code),
      };
    });
  }

  // Build group rows from the backend list only: Default first when returned,
  // then real persisted groups sorted by code.
  const defaultRows: GroupRow[] = (groups ?? [])
    .filter((g) => g.code === DEFAULT_GROUP_CODE)
    .map((g) => ({
      kind: "default" as const,
      group: g,
      memberCount: g.accountCount ?? 0,
      positionCount: g.positionCount ?? 0,
    }));
  const recordRows: RealGroupRow[] = (groups ?? [])
    .filter((g) => g.code !== DEFAULT_GROUP_CODE)
    .map((g) => ({
      kind: "real" as const,
      group: g,
      memberCount: g.accountCount ?? 0,
      positionCount: g.positionCount ?? 0,
    }));

  // Server returns real groups already sorted + paged; preserve that order.
  const allRealRows = recordRows;
  const groupRows: GroupRow[] = [...defaultRows, ...allRealRows];

  const pagedAccounts = accounts;
  const groupsByCode = new Map(
    (groups ?? []).map((group) => [group.code, group]),
  );
  const visibleAccountCount =
    accountsLoad.state === "ready" ? accountsLoad.data.total : 0;
  // Server already paged the real groups (Default pinned first on page 0);
  // render the returned rows directly. `total` counts real groups only.
  const groupsTotal = groupsLoad.state === "ready" ? groupsLoad.data.total : 0;
  const pagedGroups = groupRows;
  const hasMoreAccounts =
    accountsLoad.state === "ready" &&
    (accountPage + 1) * accountSize < accountsLoad.data.total;
  const hasMoreGroups = (groupPage + 1) * groupSize < groupsTotal;
  const accountPager = (
    <TablePagination
      page={accountPage}
      canPrevious={accountPage > 0}
      canNext={hasMoreAccounts}
      knownTotalPages={
        accountsLoad.state === "ready"
          ? knownPageCount(accountsLoad.data.total, accountSize)
          : undefined
      }
      onPrevious={() => setAccountPage((p) => Math.max(0, p - 1))}
      onNext={() => setAccountPage((p) => p + 1)}
      onPage={setAccountPage}
    />
  );
  const groupPager = (
    <TablePagination
      page={groupPage}
      canPrevious={groupPage > 0}
      canNext={hasMoreGroups}
      knownTotalPages={knownPageCount(groupsTotal, groupSize)}
      onPrevious={() => setGroupPage((p) => Math.max(0, p - 1))}
      onNext={() => setGroupPage((p) => p + 1)}
      onPage={setGroupPage}
    />
  );

  const reloadAll = () => {
    setLocalAccounts(null);
    setLocalGroups(null);
    reloadAccounts();
    reloadGroups();
  };
  const accountCsvFilters =
    selectedGroupCode === null ? undefined : { groupCode: selectedGroupCode };
  const isLoading =
    accountsLoad.state === "loading" || groupsLoad.state === "loading";
  const accountLoadError =
    accountsLoad.state === "error" ? accountsLoad.error : null;
  const groupLoadError = groupsLoad.state === "error" ? groupsLoad.error : null;

  return (
    <Page
      title={t("page.title")}
      actions={
        <>
          <PageSizeSelect
            value={tab === "accounts" ? accountSize : groupSize}
            onChange={(value) => {
              if (tab === "accounts") {
                setAccountSize(value);
                setAccountPage(0);
              } else {
                setGroupSize(value);
                setGroupPage(0);
              }
            }}
            ariaLabel={t("pagination.pageSize.ariaLabel")}
            rowCountLabel={(count) =>
              t("pagination.pageSize.rowCount", { count })
            }
          />
          <RefreshButton onClick={reloadAll} busy={isLoading} />
          <CsvTransferMenu
            exports={[
              {
                entity: "account_groups",
                label: t("businessCsv.exportGroupsCsv"),
              },
              {
                entity: "accounts",
                filters: accountCsvFilters,
                label: t("businessCsv.exportAccountsCsv"),
              },
            ]}
          />
          <CreateGroupDialog
            onCreated={() => {
              setLocalGroups(null);
              reloadGroups();
            }}
          />
          <CreateAccountDialog
            groupSuggestions={groupSuggestions}
            onCreated={() => {
              setLocalAccounts(null);
              reloadAccounts();
            }}
          />
        </>
      }
    >
      <p className="text-xs text-muted-lt">{t("page.description")}</p>

      <div className="flex w-fit gap-1 rounded-card border border-border bg-surface-2 p-1">
        {(["accounts", "groups"] as AccountsTab[]).map((tabId) => (
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

      {/* Groups panel */}
      {tab === "groups" && (
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted">
            {t("groups.heading")}
          </p>
          <ListFilters
            entity="groups"
            code={groupCode}
            codeMatch={groupCodeMatch}
            codeSuggestions={visibleGroupCodeSuggestions}
            codeLoading={
              groupsLoad.state === "loading" && groupCode.trim() !== ""
            }
            status={groupStatus}
            advancedSummary={groupAdvancedSummary}
            shareHref={groupShareHref}
            onClearAll={clearGroupFilters}
            onCode={(value) => {
              setGroupCode(value);
              setGroupPage(0);
            }}
            onCodeMatch={(value) => {
              setGroupCodeMatch(value);
              setGroupPage(0);
            }}
            onStatus={(value) => {
              setGroupStatus(value);
              setGroupPage(0);
            }}
            onClearAdvanced={(field) => {
              setGroupAdvanced((prev) => clearAdvancedField(prev, field));
              setGroupAdvancedDraft((prev) => clearAdvancedField(prev, field));
              setGroupPage(0);
            }}
            onOpenAdvanced={() => setGroupAdvancedOpen(true)}
          />
          {groupLoadError !== null && groups === null ? (
            <ErrorState message={groupLoadError} onRetry={reloadAll} />
          ) : groups === null ? (
            <TableSkeleton cols={6} />
          ) : (
            <>
              {groupPager}
              <GroupsPanel
                groupRows={pagedGroups}
                selectedGroupCode={selectedGroupCode}
                activeSort={groupSort.sort}
                activeOrder={groupSort.order}
                onSelect={(code) => {
                  setSelectedGroupCode(code);
                  accountGroupSearchRef.current = "";
                  setAccountGroupSearch("");
                  setAccountPage(0);
                  setTab("accounts");
                }}
                onSortChange={(sort, order) => {
                  setGroupSort({ sort, order });
                  setGroupPage(0);
                }}
                onEdit={setEditGroupTarget}
                onEditCurrency={setGroupCurrencyTarget}
                onEditNotes={setGroupNotesTarget}
                onBlock={setBlockGroupTarget}
                onUnblock={setUnblockGroupTarget}
                onShowBlockDetails={(group) =>
                  setBlockedDetailsTarget({ kind: "group", group })
                }
                onDelete={setDeleteGroupTarget}
              />
              {groupPager}
            </>
          )}
        </div>
      )}

      {/* Accounts panel */}
      {tab === "accounts" && (
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted">
            {t("accounts.heading")}
          </p>
          <ListFilters
            entity="accounts"
            code={accountCode}
            codeMatch={accountCodeMatch}
            codeSuggestions={visibleAccountCodeSuggestions}
            codeLoading={
              accountsLoad.state === "loading" && accountCode.trim() !== ""
            }
            groupSearch={
              selectedGroupCode !== null
                ? selectedGroupCode
                : accountGroupSearch
            }
            groupSuggestions={visibleAccountGroupSuggestions}
            groupLoading={
              groupsLoad.state === "loading" && accountGroupSearch.trim() !== ""
            }
            groupActive={selectedGroupCode !== null}
            status={accountStatus}
            advancedSummary={accountAdvancedSummary}
            shareHref={accountShareHref}
            onClearAll={clearAccountFilters}
            onCode={(value) => {
              if (accountGlobalLocked && value.trim() === "") {
                globalAccountFilter.clear();
              }
              setAccountCode(value);
              setAccountPage(0);
            }}
            onCodeMatch={(value) => {
              setAccountCodeMatch(value);
              setAccountPage(0);
            }}
            onGroupSearch={(value) => {
              const trimmed = value.trim();
              setSelectedGroupCode(
                trimmed !== "" &&
                  trimmed !== DEFAULT_GROUP_CODE &&
                  accountGroupSuggestions.includes(trimmed)
                  ? trimmed
                  : null,
              );
              accountGroupSearchRef.current = value;
              setAccountGroupSearch(value);
              setAccountPage(0);
            }}
            onStatus={(value) => {
              setAccountStatus(value);
              setAccountPage(0);
            }}
            onClearAdvanced={(field) => {
              setAccountAdvanced((prev) => clearAdvancedField(prev, field));
              setAccountAdvancedDraft((prev) =>
                clearAdvancedField(prev, field),
              );
              setAccountPage(0);
            }}
            onOpenAdvanced={() => setAccountAdvancedOpen(true)}
            accountGlobalToggle={accountGlobalToggle}
          />
          {accountLoadError !== null && accounts === null ? (
            <ErrorState message={accountLoadError} onRetry={reloadAll} />
          ) : pagedAccounts === null ? (
            <TableSkeleton cols={6} />
          ) : visibleAccountCount === 0 ? (
            <EmptyState
              title={t("accounts.empty.title")}
              hint={
                selectedGroupCode !== null
                  ? t("accounts.empty.hintFiltered")
                  : t("accounts.empty.hintEmpty")
              }
              action={
                selectedGroupCode === null ? (
                  <CreateAccountDialog
                    groupSuggestions={groupSuggestions}
                    onCreated={() => {
                      setLocalAccounts(null);
                      reloadAccounts();
                    }}
                  />
                ) : undefined
              }
            />
          ) : (
            <>
              {accountPager}
              <AccountsTable
                accounts={pagedAccounts}
                activeSort={accountSort.sort}
                activeOrder={accountSort.order}
                selectedGroupCode={selectedGroupCode}
                groupsByCode={groupsByCode}
                onSortChange={(sort, order) => {
                  setAccountSort({ sort, order });
                  setAccountPage(0);
                }}
                onEditAccount={setEditAccountTarget}
                onAssignGroup={setGroupTarget}
                onEditCurrency={setCurrencyAccountTarget}
                onEditNotes={setNotesAccountTarget}
                onBlock={setBlockAccountTarget}
                onUnblock={setUnblockAccountTarget}
                onShowBlockDetails={(account) =>
                  setBlockedDetailsTarget({ kind: "account", account })
                }
                onOpenPositions={(account) =>
                  navigate(
                    `/positions?account=${encodeURIComponent(account.code)}`,
                  )
                }
                onOpenTrading={(account) =>
                  navigate(
                    `/trading?account=${encodeURIComponent(account.code)}`,
                  )
                }
                onOpenPolicies={(account) =>
                  navigate(
                    `/policies?account=${encodeURIComponent(account.code)}`,
                  )
                }
                onOpenHistory={(account) =>
                  navigate(`/audit?account=${encodeURIComponent(account.code)}`)
                }
                onFilterGroup={(group) => {
                  setSelectedGroupCode(group);
                  accountGroupSearchRef.current = "";
                  setAccountGroupSearch("");
                  setAccountPage(0);
                }}
                onDelete={setDeleteAccountTarget}
              />
              {accountPager}
            </>
          )}
        </div>
      )}

      <AdvancedFilterDialog
        entity="accounts"
        open={accountAdvancedOpen}
        draft={accountAdvancedDraft}
        onDraftChange={setAccountAdvancedDraft}
        onCancel={() => setAccountAdvancedOpen(false)}
        onApply={() => {
          setAccountAdvanced(accountAdvancedDraft);
          setAccountPage(0);
          setAccountAdvancedOpen(false);
        }}
      />
      <AdvancedFilterDialog
        entity="groups"
        open={groupAdvancedOpen}
        draft={groupAdvancedDraft}
        onDraftChange={setGroupAdvancedDraft}
        onCancel={() => setGroupAdvancedOpen(false)}
        onApply={() => {
          setGroupAdvanced(groupAdvancedDraft);
          setGroupPage(0);
          setGroupAdvancedOpen(false);
        }}
      />

      {/* Account dialogs */}
      <EditAccountMetadataDialog
        account={editAccountTarget}
        open={editAccountTarget !== null}
        onOpenChange={(next) => {
          if (!next) setEditAccountTarget(null);
        }}
        onDone={() => {
          setEditAccountTarget(null);
          setLocalAccounts(null);
          reloadAccounts();
        }}
      />
      <BlockAccountDialog
        account={blockAccountTarget}
        open={blockAccountTarget !== null}
        onOpenChange={(next) => {
          if (!next) setBlockAccountTarget(null);
        }}
        onDone={() => {
          setBlockAccountTarget(null);
          setLocalAccounts(null);
          reloadAccounts();
        }}
      />
      <UnblockAccountConfirm
        account={unblockAccountTarget}
        open={unblockAccountTarget !== null}
        onOpenChange={(next) => {
          if (!next) setUnblockAccountTarget(null);
        }}
        onDone={() => {
          const unblocked = unblockAccountTarget;
          setUnblockAccountTarget(null);
          if (unblocked !== null) {
            setBlockedDetailsTarget((current) =>
              current?.kind === "account" &&
              current.account.code === unblocked.code
                ? null
                : current,
            );
          }
          setLocalAccounts(null);
          reloadAccounts();
        }}
      />
      <AssignGroupDialog
        account={groupTarget}
        groupSuggestions={groupSuggestions}
        open={groupTarget !== null}
        onOpenChange={(next) => {
          if (!next) setGroupTarget(null);
        }}
        onDone={() => {
          setGroupTarget(null);
          setLocalAccounts(null);
          setLocalGroups(null);
          reloadAccounts();
          reloadGroups();
        }}
      />
      {currencyAccountTarget !== null && (
        <AccountCurrencyDialog
          key={currencyAccountTarget.code}
          account={currencyAccountTarget}
          open
          onOpenChange={(next) => {
            if (!next) setCurrencyAccountTarget(null);
          }}
          onDone={() => {
            setCurrencyAccountTarget(null);
            setLocalAccounts(null);
            reloadAccounts();
          }}
        />
      )}
      <EditAccountNotesDialog
        account={notesAccountTarget}
        open={notesAccountTarget !== null}
        onOpenChange={(next) => {
          if (!next) setNotesAccountTarget(null);
        }}
        onDone={() => {
          setNotesAccountTarget(null);
          setLocalAccounts(null);
          reloadAccounts();
        }}
      />
      <DeleteAccountConfirm
        account={deleteAccountTarget}
        open={deleteAccountTarget !== null}
        onOpenChange={(next) => {
          if (!next) setDeleteAccountTarget(null);
        }}
        onDone={() => {
          if (deleteAccountTarget !== null) {
            removeAccount(deleteAccountTarget.code);
          }
          setDeleteAccountTarget(null);
        }}
      />
      <BlockedDetailsDialog
        target={blockedDetailsTarget}
        open={blockedDetailsTarget !== null}
        onOpenChange={(next) => {
          if (!next) setBlockedDetailsTarget(null);
        }}
        onUnblock={(target) => {
          if (target.kind === "account") {
            setUnblockAccountTarget(target.account);
          } else {
            setUnblockGroupTarget(target.group);
          }
        }}
      />
      {/* Group dialogs */}
      <EditGroupMetadataDialog
        group={editGroupTarget}
        open={editGroupTarget !== null}
        onOpenChange={(next) => {
          if (!next) setEditGroupTarget(null);
        }}
        onDone={(updated) => {
          if (
            editGroupTarget !== null &&
            selectedGroupCode === editGroupTarget.code
          ) {
            setSelectedGroupCode(updated.code);
            setAccountPage(0);
          }
          setEditGroupTarget(null);
          setLocalGroups(null);
          reloadGroups();
          setLocalAccounts(null);
          reloadAccounts();
        }}
      />
      {groupCurrencyTarget !== null && (
        <GroupCurrencyDialog
          key={groupCurrencyTarget.code}
          group={groupCurrencyTarget}
          open
          onOpenChange={(next) => {
            if (!next) setGroupCurrencyTarget(null);
          }}
          onDone={() => {
            setGroupCurrencyTarget(null);
            setLocalGroups(null);
            setLocalAccounts(null);
            reloadGroups();
            reloadAccounts();
          }}
        />
      )}
      <EditGroupNotesDialog
        group={groupNotesTarget}
        open={groupNotesTarget !== null}
        onOpenChange={(next) => {
          if (!next) setGroupNotesTarget(null);
        }}
        onDone={() => {
          setGroupNotesTarget(null);
          setLocalGroups(null);
          reloadGroups();
        }}
      />
      <BlockGroupDialog
        group={blockGroupTarget}
        open={blockGroupTarget !== null}
        onOpenChange={(next) => {
          if (!next) setBlockGroupTarget(null);
        }}
        onDone={() => {
          setBlockGroupTarget(null);
          setLocalGroups(null);
          reloadGroups();
          // A group block changes the effective block of every member account,
          // so the accounts tab must not keep showing them as tradable.
          setLocalAccounts(null);
          reloadAccounts();
        }}
      />
      <UnblockGroupConfirm
        group={unblockGroupTarget}
        open={unblockGroupTarget !== null}
        onOpenChange={(next) => {
          if (!next) setUnblockGroupTarget(null);
        }}
        onDone={() => {
          const unblocked = unblockGroupTarget;
          setUnblockGroupTarget(null);
          if (unblocked !== null) {
            setBlockedDetailsTarget((current) =>
              current?.kind === "group" && current.group.code === unblocked.code
                ? null
                : current,
            );
          }
          setLocalGroups(null);
          reloadGroups();
          setLocalAccounts(null);
          reloadAccounts();
        }}
      />
      <DeleteGroupConfirm
        group={deleteGroupTarget}
        open={deleteGroupTarget !== null}
        onOpenChange={(next) => {
          if (!next) setDeleteGroupTarget(null);
        }}
        onDone={() => {
          setDeleteGroupTarget(null);
          // If the deleted group was selected, clear the filter.
          if (
            deleteGroupTarget !== null &&
            selectedGroupCode === deleteGroupTarget.code
          ) {
            setSelectedGroupCode(null);
            setAccountPage(0);
          }
          setLocalGroups(null);
          reloadGroups();
          setLocalAccounts(null);
          reloadAccounts();
        }}
      />
    </Page>
  );
}
