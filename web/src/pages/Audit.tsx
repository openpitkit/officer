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
import { ListFilter } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import type { AuditActionGroup, AuditEntry } from "@/api/types";
import { useAuditActions, useAuditPage } from "@/api/useAudit";
import { EmptyState, ErrorState, TableSkeleton } from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
import {
  ColumnHeader,
  CopyIdButton,
  FieldLabel,
  ExactIdField,
  FilterBar,
  OnlineFilterField,
  ShareLinkButton,
  TimeRangeFilter,
  type AuditFilter,
} from "@/framework";
import {
  PageSizeSelect,
  TablePagination,
} from "@/components/TableControls";
import { knownPageCount } from "@/lib/tablePagination";
import { Badge, type BadgeProps } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
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
import { operatorOptions } from "@/lib/dataControlLabels";
import { formatDateTime } from "@/i18n/format";
import { shareUrl } from "@/lib/shareLink";
import { usePersistentPageSize } from "@/lib/tablePageSize";
import {
  DEFAULT_SEARCH_DEBOUNCE_MS,
  useDebouncedValue,
} from "@/lib/useDebounce";
import { useGlobalAccountFilter } from "@/lib/globalAccountFilter";

const SOURCES = ["panel", "api", "mcp", "system"] as const;
const EMPTY_ACTION_GROUPS: AuditActionGroup[] = [];
type ActionDefaultCategory = "control" | "all";

/** Map an audit action to a badge tone. Destructive control and unsigned
 *  submissions read as dangerous; creations and unblocks as positive. */
function actionVariant(action: string): BadgeProps["variant"] {
  switch (action) {
    case "block":
    case "delete_limit":
    case "reset_database":
    case "stop_service":
    case "submit_drop_copy_order":
      return "danger";
    case "restart_service":
      return "warn";
    case "create_account":
    case "unblock":
      return "ok";
    case "set_limit":
      return "accent";
    case "hydrate":
      return "neutral";
    default:
      return "neutral";
  }
}

/** Source → badge variant. */
function sourceVariant(source: string): BadgeProps["variant"] {
  switch (source) {
    case "panel":
      return "accent";
    case "mcp":
      return "warn";
    default:
      return "neutral";
  }
}

function localDateTimeToApi(value: string): string | undefined {
  if (value.trim() === "") {
    return undefined;
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return undefined;
  }
  return date.toISOString();
}

function AuditTable({
  activeExternalId,
  entries,
}: {
  activeExternalId?: string;
  entries: AuditEntry[];
}) {
  const { t } = useTranslation("audit");
  const { t: tc } = useTranslation();
  const rowRefs = useRef(new Map<string, HTMLTableRowElement>());
  const exactExternalId = activeExternalId?.trim() ?? "";
  useEffect(() => {
    if (exactExternalId === "") {
      return;
    }
    const row = rowRefs.current.get(exactExternalId);
    row?.scrollIntoView({ block: "center" });
    row?.focus();
  }, [exactExternalId, entries]);
  return (
    <Table>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead>
            <ColumnHeader description={t("table.columnDescriptions.externalId")}>
              {tc("fields.externalId")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <ColumnHeader description={t("table.columnDescriptions.time")}>
              {t("table.time")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <ColumnHeader description={t("table.columnDescriptions.actor")}>
              {t("table.actor")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <ColumnHeader description={t("table.columnDescriptions.action")}>
              {t("table.action")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <ColumnHeader description={t("table.columnDescriptions.account")}>
              {t("table.account")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <ColumnHeader description={t("table.columnDescriptions.source")}>
              {t("table.source")}
            </ColumnHeader>
          </TableHead>
          <TableHead>
            <ColumnHeader description={t("table.columnDescriptions.detail")}>
              {t("table.detail")}
            </ColumnHeader>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {entries.map((entry) => (
          <TableRow
            key={entry.id}
            ref={(node) => {
              if (node === null) {
                rowRefs.current.delete(entry.id);
                return;
              }
              rowRefs.current.set(entry.id, node);
            }}
            tabIndex={entry.id === exactExternalId ? 0 : -1}
            className={
              entry.id === exactExternalId
                ? "hover:bg-transparent ring-1 ring-inset ring-ring focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                : "hover:bg-transparent"
            }
          >
            <TableCell className="nums whitespace-nowrap text-xs">
              <div className="flex min-w-0 items-center gap-1">
                <span className="min-w-0 truncate">{entry.id}</span>
                <span className="ml-auto flex shrink-0 items-center">
                  <CopyIdButton
                    value={entry.id}
                    size={22}
                    title={t("common:rowActions.copyId")}
                    copiedTitle={t("common:rowActions.copiedId")}
                  />
                </span>
              </div>
            </TableCell>
            <TableCell className="nums whitespace-nowrap text-xs text-muted-lt">
              {formatDateTime(entry.at)}
            </TableCell>
            <TableCell className="text-xs text-muted-lt">
              {entry.actorTitle || entry.actor || tc("value.none")}
            </TableCell>
            <TableCell>
              <Badge variant={actionVariant(entry.action)}>
                {entry.action}
              </Badge>
            </TableCell>
            <TableCell className="nums text-xs">
              {entry.account ? (
                <div className="flex min-w-0 items-center gap-1">
                  <span className="min-w-0 truncate">
                    {entry.accountTitle || entry.account}
                  </span>
                  <span className="ml-auto flex shrink-0 items-center">
                    <CopyIdButton
                      value={entry.account}
                      size={22}
                      title={t("common:rowActions.copyId")}
                      copiedTitle={t("common:rowActions.copiedId")}
                    />
                  </span>
                </div>
              ) : (
                tc("value.none")
              )}
            </TableCell>
            <TableCell>
              {entry.source ? (
                <Badge variant={sourceVariant(entry.source)}>
                  {entry.source}
                </Badge>
              ) : (
                <span className="text-xs text-muted-lt">{tc("value.none")}</span>
              )}
            </TableCell>
            <TableCell className="text-xs text-text">
              {entry.detail || tc("value.none")}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

/** Grouped multiselect of audit action types. Each group header toggles its
 *  whole category; individual rows toggle one type. The menu stays open across
 *  toggles so an operator can refine the selection in one pass. */
function ActionTypeFilter({
  selected,
  onChange,
  groups,
  allActions,
  controlActions,
  loading,
}: {
  selected: Set<string>;
  onChange: (next: Set<string>) => void;
  groups: AuditActionGroup[];
  allActions: string[];
  controlActions: string[];
  loading: boolean;
}) {
  const { t } = useTranslation("audit");

  const summary = (() => {
    if (allActions.length > 0 && selected.size === allActions.length) {
      return t("filter.types.all");
    }
    if (
      controlActions.length > 0 &&
      selected.size === controlActions.length &&
      controlActions.every((a) => selected.has(a))
    ) {
      return t("filter.types.controlOnly");
    }
    return t("filter.types.count", { count: selected.size });
  })();

  const toggle = (action: string) => {
    const next = new Set(selected);
    if (next.has(action)) {
      next.delete(action);
    } else {
      next.add(action);
    }
    onChange(next);
  };

  const toggleGroup = (actions: readonly string[]) => {
    const allOn = actions.every((a) => selected.has(a));
    const next = new Set(selected);
    for (const a of actions) {
      if (allOn) {
        next.delete(a);
      } else {
        next.add(a);
      }
    }
    onChange(next);
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          className="h-8 gap-1.5 text-xs"
          aria-label={t("filter.types.ariaLabel")}
          disabled={loading}
        >
          <ListFilter className="h-3.5 w-3.5" />
          {t("filter.types.label")}
          <span className="text-muted-lt">· {summary}</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        className="max-h-96 w-60 overflow-y-auto"
      >
        {groups.map((group, i) => {
          const allOn = group.actions.every((a) => selected.has(a));
          return (
            <div key={group.category}>
              {i > 0 && <DropdownMenuSeparator />}
              <DropdownMenuCheckboxItem
                checked={allOn}
                onSelect={(e) => {
                  e.preventDefault();
                  toggleGroup(group.actions);
                }}
                className="font-bold uppercase tracking-[0.05em]"
              >
                {t(`filter.types.group.${group.category}`)}
              </DropdownMenuCheckboxItem>
              {group.actions.map((action) => (
                <DropdownMenuCheckboxItem
                  key={action}
                  checked={selected.has(action)}
                  onSelect={(e) => {
                    e.preventDefault();
                    toggle(action);
                  }}
                >
                  {action}
                </DropdownMenuCheckboxItem>
              ))}
            </div>
          );
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export function Audit() {
  const { t } = useTranslation("audit");
  const { t: tc } = useTranslation();
  const [params] = useSearchParams();
  const globalAccountFilter = useGlobalAccountFilter();

  // Seed filter state from URL so dashboard activity links and per-account
  // quick-links land here pre-filtered.
  const [externalId, setExternalId] = useState(params.get("id") ?? "");
  const [appliedExternalId, setAppliedExternalId] = useState(
    params.get("id") ?? "",
  );
  const [account, setAccount] = useState(
    globalAccountFilter.account || params.get("account") || "",
  );
  const [asset, setAsset] = useState(params.get("asset") ?? "");
  const [actor, setActor] = useState(params.get("actor") ?? "");
  const [source, setSource] = useState(params.get("source") ?? "");
  const [atMode, setAtMode] = useState(params.get("atMode") ?? "after");
  const [atMin, setAtMin] = useState(params.get("atMin") ?? "");
  const [atMax, setAtMax] = useState(params.get("atMax") ?? "");
  const initialActions = useMemo(
    () =>
      (params.get("actions") ?? "")
        .split(",")
        .map((action) => action.trim())
        .filter((action) => action.length > 0),
    [params],
  );
  const initialActionCategory = useMemo<ActionDefaultCategory>(
    () =>
      initialActions.length === 0 && params.get("category") === "all"
        ? "all"
        : "control",
    [initialActions.length, params],
  );
  const [size, setSize] = usePersistentPageSize("pit-officer-audit-page-size");
  const [page, setPage] = useState(0);
  const [selectedActions, setSelectedActions] = useState<Set<string> | null>(
    initialActions.length > 0 ? new Set(initialActions) : null,
  );
  const [defaultActionCategory, setDefaultActionCategory] =
    useState<ActionDefaultCategory>(initialActionCategory);
  const debouncedAccount = useDebouncedValue(
    account,
    DEFAULT_SEARCH_DEBOUNCE_MS,
  );
  const debouncedAsset = useDebouncedValue(asset, DEFAULT_SEARCH_DEBOUNCE_MS);
  const debouncedActor = useDebouncedValue(actor, DEFAULT_SEARCH_DEBOUNCE_MS);
  const debouncedAtMin = useDebouncedValue(atMin, DEFAULT_SEARCH_DEBOUNCE_MS);
  const debouncedAtMax = useDebouncedValue(atMax, DEFAULT_SEARCH_DEBOUNCE_MS);

  const resetPage = () => {
    setPage(0);
  };

  useEffect(() => {
    const nextAccount = globalAccountFilter.account;
    if (nextAccount === "") {
      return;
    }
    // Mirror the external global-account store into this page-local filter.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setAccount(nextAccount);
    resetPage();
  }, [globalAccountFilter.account]);

  // Load the action catalogue from the server; the type filter is built
  // entirely from it.
  const catalogue = useAuditActions();
  const actionGroups =
    catalogue.load.state === "ready"
      ? catalogue.load.data
      : EMPTY_ACTION_GROUPS;
  const allActions = useMemo(
    () => actionGroups.flatMap((g) => g.actions),
    [actionGroups],
  );
  const controlActions = useMemo(
    () => actionGroups.find((g) => g.category === "control")?.actions ?? [],
    [actionGroups],
  );
  const effectiveSelectedActions = useMemo(
    () =>
      selectedActions ??
      new Set<string>(
        defaultActionCategory === "all" ? allActions : controlActions,
      ),
    [allActions, controlActions, defaultActionCategory, selectedActions],
  );
  const actionRequest = useMemo(() => {
    if (selectedActions === null) {
      return { category: defaultActionCategory, actions: undefined };
    }
    if (selectedActions.size === 0) {
      return { category: undefined, actions: [] };
    }
    if (
      allActions.length > 0 &&
      selectedActions.size === allActions.length &&
      allActions.every((action) => selectedActions.has(action))
    ) {
      return { category: "all", actions: undefined };
    }
    if (
      controlActions.length > 0 &&
      selectedActions.size === controlActions.length &&
      controlActions.every((action) => selectedActions.has(action))
    ) {
      return { category: "control", actions: undefined };
    }
    return { category: undefined, actions: Array.from(selectedActions) };
  }, [allActions, controlActions, defaultActionCategory, selectedActions]);
  const actionFilterActive =
    allActions.length > 0 &&
    !allActions.every((action) => effectiveSelectedActions.has(action));
  const isFiltered =
    !!appliedExternalId ||
    !!account ||
    !!asset ||
    !!actor ||
    !!source ||
    atMin.trim() !== "" ||
    atMax.trim() !== "" ||
    actionFilterActive;
  const clearFilters = () => {
    setExternalId("");
    setAppliedExternalId("");
    if (globalAccountFilter.account === "" || account !== globalAccountFilter.account) {
      setAccount("");
    }
    setAsset("");
    setActor("");
    setSource("");
    setAtMode("after");
    setAtMin("");
    setAtMax("");
    setSelectedActions(null);
    setDefaultActionCategory("all");
    resetPage();
  };
  const accountGlobalLocked =
    globalAccountFilter.account !== "" && account.trim() === globalAccountFilter.account;
  const accountGlobalToggle = {
    active: accountGlobalLocked,
    disabled: account.trim() === "",
    activeLabel: tc("filters.globalAccount.active"),
    inactiveLabel: tc("filters.globalAccount.inactive"),
    disabledLabel: tc("filters.globalAccount.disabled"),
    onToggle: () => {
      const nextAccount = account.trim();
      if (nextAccount === "") {
        return;
      }
      if (accountGlobalLocked) {
        globalAccountFilter.clear();
        return;
      }
      globalAccountFilter.setAccount(nextAccount);
    },
  };
  const requestAtMode =
    atMode === "between"
      ? debouncedAtMin.trim() !== "" && debouncedAtMax.trim() !== ""
        ? atMode
        : undefined
      : atMode === "after"
        ? debouncedAtMin.trim() !== ""
          ? atMode
          : undefined
        : atMode === "before"
          ? debouncedAtMax.trim() !== ""
            ? atMode
            : undefined
          : undefined;

  const auditFilter = useMemo<AuditFilter>(
    () => ({
      id: appliedExternalId.trim() || undefined,
      account: debouncedAccount.trim() || undefined,
      asset: debouncedAsset.trim() || undefined,
      actor: debouncedActor.trim() || undefined,
      actorMatch: debouncedActor.trim() ? "contains" : undefined,
      source: source || undefined,
      category: actionRequest.category,
      actions: actionRequest.actions,
      atMode: requestAtMode,
      atMin: localDateTimeToApi(debouncedAtMin),
      atMax: localDateTimeToApi(debouncedAtMax),
      limit: size,
      offset: page * size,
    }),
    [
      actionRequest,
      appliedExternalId,
      debouncedAccount,
      debouncedActor,
      debouncedAsset,
      debouncedAtMax,
      debouncedAtMin,
      page,
      requestAtMode,
      size,
      source,
    ],
  );
  const { load, reload } = useAuditPage(auditFilter);
  const auditPage = load.state === "ready" ? load.data : null;
  const pagedEntries = auditPage?.items ?? [];
  useEffect(() => {
    if (load.state !== "ready" || page === 0 || load.data.items.length > 0) {
      return;
    }
    const lastPage = Math.max(0, knownPageCount(load.data.total, size) - 1);
    const timer = window.setTimeout(() => {
      setPage(Math.min(page - 1, lastPage));
    }, 0);
    return () => window.clearTimeout(timer);
  }, [load, page, size]);

  // Encode the active filter set as a shareable deep link. Only non-default
  // params are emitted, mirroring the API param names; `actions` is included
  // only when the operator has narrowed to a custom subset.
  const shareHref = useMemo(() => {
    const query = new URLSearchParams();
    const externalIdValue = appliedExternalId.trim();
    if (externalIdValue !== "") {
      query.set("id", externalIdValue);
    }
    if (account.trim() !== "") {
      query.set("account", account.trim());
    }
    if (actor.trim() !== "") {
      query.set("actor", actor.trim());
    }
    if (asset.trim() !== "") {
      query.set("asset", asset.trim());
    }
    if (source !== "") {
      query.set("source", source);
    }
    if (actionRequest.actions !== undefined) {
      query.set("actions", actionRequest.actions.join(","));
    } else if (actionRequest.category === "all") {
      query.set("category", "all");
    }
    if (atMin.trim() !== "") {
      query.set("atMode", atMode);
      query.set("atMin", atMin.trim());
    }
    if (atMax.trim() !== "") {
      query.set("atMode", atMode);
      query.set("atMax", atMax.trim());
    }
    return shareUrl("/audit", query);
  }, [
    account,
    actionRequest,
    actor,
    appliedExternalId,
    asset,
    atMax,
    atMin,
    atMode,
    source,
  ]);
  const pager = (
    <TablePagination
      page={page}
      canPrevious={page > 0}
      canNext={auditPage !== null && (page + 1) * size < auditPage.total}
      knownTotalPages={
        auditPage !== null ? knownPageCount(auditPage.total, size) : undefined
      }
      onPrevious={() => setPage((p) => Math.max(0, p - 1))}
      onNext={() => setPage((p) => p + 1)}
      onPage={setPage}
    />
  );

  return (
    <Page
      title={t("title")}
      actions={
        <>
          <PageSizeSelect
            value={size}
            onChange={(value) => {
              setSize(value);
              resetPage();
            }}
            ariaLabel={t("actions.pageSize.ariaLabel")}
            rowCountLabel={(count) =>
              t("actions.pageSize.rowCount", { count })
            }
          />
          <RefreshButton onClick={reload} busy={load.state === "loading"} />
        </>
      }
    >
      <p className="text-xs text-muted-lt">{t("description")}</p>

      <FilterBar
        active={isFiltered}
        activeLabel={tc("filters.active")}
        onClearActive={clearFilters}
        clearActiveLabel={tc("filters.clearAll")}
        trailing={
          <div className="flex items-end">
            <ShareLinkButton
              href={shareHref}
              title={tc("rowActions.shareFilters")}
              copiedTitle={tc("rowActions.copiedLink")}
              size={32}
            />
          </div>
        }
        bottom={
          <>
            <OnlineFilterField
              label={tc("fields.account")}
              value={account}
              onChange={(value) => {
                if (accountGlobalLocked && value.trim() === "") {
                  globalAccountFilter.clear();
                }
                setAccount(value);
                resetPage();
              }}
              onClear={() => {
                if (accountGlobalLocked) {
                  globalAccountFilter.clear();
                }
                setAccount("");
                resetPage();
              }}
              clearLabel={tc("filters.clearField")}
              placeholder={t("filter.account.placeholder")}
              loading={load.state === "loading" && account.trim() !== ""}
              searchingLabel={tc("filters.onlineLoading", {
                field: tc("fields.account"),
              })}
              globalToggle={accountGlobalToggle}
            />
            <OnlineFilterField
              label={tc("fields.asset")}
              value={asset}
              onChange={(value) => {
                setAsset(value);
                resetPage();
              }}
              onClear={() => {
                setAsset("");
                resetPage();
              }}
              clearLabel={tc("filters.clearField")}
              placeholder={t("filter.asset.placeholder")}
              loading={load.state === "loading" && asset.trim() !== ""}
              searchingLabel={tc("filters.onlineLoading", {
                field: tc("fields.asset"),
              })}
            />
            <OnlineFilterField
              label={t("table.actor")}
              value={actor}
              onChange={(value) => {
                setActor(value);
                resetPage();
              }}
              onClear={() => {
                setActor("");
                resetPage();
              }}
              clearLabel={tc("filters.clearField")}
              placeholder={t("table.actor")}
              loading={load.state === "loading" && actor.trim() !== ""}
              searchingLabel={tc("filters.onlineLoading", {
                field: t("table.actor"),
              })}
            />
            <div className="grid gap-1.5">
              <FieldLabel>{tc("fields.source")}</FieldLabel>
              <Select
                value={source || "_all"}
                onValueChange={(v) => {
                  setSource(v === "_all" ? "" : v);
                  resetPage();
                }}
              >
                <SelectTrigger
                  className="h-8 w-32 text-xs"
                  aria-label={t("filter.source.ariaLabel")}
                >
                  <SelectValue placeholder={t("filter.source.all")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="_all">{t("filter.source.all")}</SelectItem>
                  {SOURCES.map((s) => (
                    <SelectItem key={s} value={s}>
                      {s}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <FieldLabel>{tc("fields.time")}</FieldLabel>
              <TimeRangeFilter
                operator={atMode}
                operators={operatorOptions(tc, "time")}
                operatorAriaLabel={tc("fields.time")}
                from={atMode === "before" ? atMax : atMin}
                to={atMax}
                onOperatorChange={(value) => {
                  setAtMode(value);
                  resetPage();
                }}
                onFromChange={(value) => {
                  if (atMode === "before") {
                    setAtMax(value);
                  } else {
                    setAtMin(value);
                  }
                  resetPage();
                }}
                onToChange={(value) => {
                  setAtMax(value);
                  resetPage();
                }}
                clearLabel={tc("filters.clearField")}
                showPresets={false}
              />
            </div>
            <ExactIdField
              label={tc("exactLookup.label")}
              value={externalId}
              placeholder={tc("exactLookup.placeholder", {
                entity: tc("entities.auditEntry"),
              })}
              openLabel={tc("exactLookup.open")}
              onChange={setExternalId}
              onOpen={(value) => {
                setAppliedExternalId(value.trim());
                resetPage();
              }}
              style={{ width: "100%" }}
              width={360}
            />
          </>
        }
      >
        <div className="grid gap-1.5">
          <ActionTypeFilter
            selected={effectiveSelectedActions}
            onChange={(next) => {
              setSelectedActions(next);
              resetPage();
            }}
            groups={actionGroups}
            allActions={allActions}
            controlActions={controlActions}
            loading={catalogue.load.state !== "ready"}
          />
        </div>
      </FilterBar>

      {/* Gate the list on the catalogue so the first audit fetch carries the
          seeded control default rather than flashing an empty result. */}
      {(catalogue.load.state === "loading" || load.state === "loading") && (
        <TableSkeleton cols={7} />
      )}
      {catalogue.load.state === "error" && (
        <ErrorState message={catalogue.load.error} onRetry={catalogue.reload} />
      )}
      {catalogue.load.state === "ready" &&
        load.state === "error" && (
          <ErrorState message={load.error} onRetry={reload} />
        )}
      {catalogue.load.state === "ready" &&
        load.state === "ready" &&
        (pagedEntries.length === 0 ? (
          <EmptyState
            title={t("empty.title")}
            hint={isFiltered ? t("empty.hint.filtered") : t("empty.hint.blank")}
          />
        ) : (
          <>
            {pager}
            <AuditTable
              entries={pagedEntries}
              activeExternalId={appliedExternalId}
            />
            {pager}
          </>
        ))}
    </Page>
  );
}
