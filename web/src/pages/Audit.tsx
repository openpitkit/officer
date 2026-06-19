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

import { useMemo, useState } from "react";
import { ListFilter } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import type { AuditEntry } from "@/api/types";
import { useAudit } from "@/api/useAudit";
import { Autocomplete } from "@/components/Autocomplete";
import { EmptyState, ErrorState, TableSkeleton } from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
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
import { formatDateTime } from "@/i18n/format";

const PAGE_SIZES = [50, 100, 500] as const;
const SOURCES = ["panel", "api", "mcp", "system"] as const;

// Audit action catalogue, grouped by category. Mirrors the server-side
// classification in internal/domain (AuditAction.Category): trading is the
// high-volume order/execution stream, control is everything else. New server
// actions must be added here so the type filter keeps offering them.
const TRADING_ACTIONS = ["submit_order", "execution_report"] as const;
const CONTROL_ACTIONS = [
  "hydrate",
  "create_account",
  "block",
  "unblock",
  "set_limit",
  "delete_limit",
  "set_group_notes",
  "block_group",
  "unblock_group",
  "set_notes",
  "set_group",
  "adjustment",
  "create_group",
  "delete_group",
  "set_mcp_access",
  "set_market_data",
  "export_backup",
  "restore_backup",
  "reset_database",
  "generate_signing_key",
  "import_signing_key",
  "set_signing_config",
  "approval_issued",
  "approval_confirmed",
  "approval_cancelled",
] as const;

const ACTION_GROUPS = [
  { key: "control", actions: CONTROL_ACTIONS },
  { key: "trading", actions: TRADING_ACTIONS },
] as const;

const ALL_ACTIONS: readonly string[] = [...CONTROL_ACTIONS, ...TRADING_ACTIONS];

// Trading activity is hidden by default; an operator opts into it explicitly.
function defaultActions(): Set<string> {
  return new Set<string>(CONTROL_ACTIONS);
}

/** Map an audit action to a badge tone. Blocking and deletions read as
 *  destructive; creations and unblocks as positive. */
function actionVariant(action: string): BadgeProps["variant"] {
  switch (action) {
    case "block":
    case "delete_limit":
    case "reset_database":
      return "danger";
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

function AuditTable({ entries }: { entries: AuditEntry[] }) {
  const { t } = useTranslation("audit");
  const { t: tc } = useTranslation();
  return (
    <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>{t("table.time")}</TableHead>
            <TableHead>{t("table.actor")}</TableHead>
            <TableHead>{t("table.action")}</TableHead>
            <TableHead>{t("table.account")}</TableHead>
            <TableHead>{t("table.source")}</TableHead>
            <TableHead>{t("table.detail")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {entries.map((entry) => (
            <TableRow key={entry.id} className="hover:bg-transparent">
              <TableCell className="nums whitespace-nowrap text-xs text-muted-lt">
                {formatDateTime(entry.at)}
              </TableCell>
              <TableCell className="text-xs text-muted-lt">
                {entry.actor || tc("value.none")}
              </TableCell>
              <TableCell>
                <Badge variant={actionVariant(entry.action)}>
                  {entry.action}
                </Badge>
              </TableCell>
              <TableCell className="nums text-xs">
                {entry.account || tc("value.none")}
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
}: {
  selected: Set<string>;
  onChange: (next: Set<string>) => void;
}) {
  const { t } = useTranslation("audit");

  const summary = (() => {
    if (selected.size === ALL_ACTIONS.length) {
      return t("filter.types.all");
    }
    if (
      selected.size === CONTROL_ACTIONS.length &&
      CONTROL_ACTIONS.every((a) => selected.has(a))
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
        {ACTION_GROUPS.map((group, i) => {
          const allOn = group.actions.every((a) => selected.has(a));
          return (
            <div key={group.key}>
              {i > 0 && <DropdownMenuSeparator />}
              <DropdownMenuCheckboxItem
                checked={allOn}
                onSelect={(e) => {
                  e.preventDefault();
                  toggleGroup(group.actions);
                }}
                className="font-bold uppercase tracking-[0.05em]"
              >
                {t(`filter.types.group.${group.key}`)}
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
  const [params] = useSearchParams();

  // Seed filter state from URL so dashboard activity links and per-account
  // quick-links land here pre-filtered.
  const [account, setAccount] = useState(params.get("account") ?? "");
  const [source, setSource] = useState(params.get("source") ?? "");
  const [size, setSize] = useState<number>(50);
  const [selectedActions, setSelectedActions] =
    useState<Set<string>>(defaultActions);

  const actions = useMemo(() => Array.from(selectedActions), [selectedActions]);
  const isFiltered =
    !!account || !!source || selectedActions.size !== ALL_ACTIONS.length;

  const { load, reload } = useAudit(
    size,
    account || undefined,
    source || undefined,
    actions,
  );

  // Build account suggestions from loaded entries.
  const accountSuggestions = (() => {
    if (load.state !== "ready") {
      return [];
    }
    const seen = new Set<string>();
    for (const e of load.data) {
      if (e.account) {
        seen.add(e.account);
      }
    }
    return Array.from(seen).sort();
  })();

  return (
    <Page
      title={t("title")}
      actions={
        <>
          <Select
            value={String(size)}
            onValueChange={(v) => setSize(Number(v))}
          >
            <SelectTrigger
              className="h-8 w-28 text-xs"
              aria-label={t("actions.pageSize.ariaLabel")}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PAGE_SIZES.map((n) => (
                <SelectItem key={n} value={String(n)}>
                  {t("actions.pageSize.rowCount", { count: n })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <RefreshButton onClick={reload} busy={load.state === "loading"} />
        </>
      }
    >
      <p className="text-xs text-muted-lt">{t("description")}</p>

      {/* Filters */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="w-48">
          <Autocomplete
            value={account}
            onChange={setAccount}
            suggestions={accountSuggestions}
            placeholder={t("filter.account.placeholder")}
            className="h-8 text-xs"
          />
        </div>
        <Select
          value={source || "_all"}
          onValueChange={(v) => setSource(v === "_all" ? "" : v)}
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
        <ActionTypeFilter
          selected={selectedActions}
          onChange={setSelectedActions}
        />
      </div>

      {load.state === "loading" && <TableSkeleton cols={6} />}
      {load.state === "error" && (
        <ErrorState message={load.error} onRetry={reload} />
      )}
      {load.state === "ready" &&
        (load.data.length === 0 ? (
          <EmptyState
            title={t("empty.title")}
            hint={isFiltered ? t("empty.hint.filtered") : t("empty.hint.blank")}
          />
        ) : (
          <AuditTable entries={load.data} />
        ))}
    </Page>
  );
}
