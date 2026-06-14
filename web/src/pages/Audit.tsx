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

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import type { AuditEntry } from "@/api/types";
import { useAudit } from "@/api/useAudit";
import { Autocomplete } from "@/components/Autocomplete";
import { EmptyState, ErrorState, TableSkeleton } from "@/components/PageStates";
import { Page } from "@/components/Page";
import { RefreshButton } from "@/components/RefreshButton";
import { Badge, type BadgeProps } from "@/components/ui/badge";
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

export function Audit() {
  const { t } = useTranslation("audit");
  const [params] = useSearchParams();

  // Seed filter state from URL so dashboard activity links and per-account
  // quick-links land here pre-filtered.
  const [account, setAccount] = useState(params.get("account") ?? "");
  const [source, setSource] = useState(params.get("source") ?? "");
  const [size, setSize] = useState<number>(50);

  const { load, reload } = useAudit(
    size,
    account || undefined,
    source || undefined,
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
      </div>

      {load.state === "loading" && <TableSkeleton cols={6} />}
      {load.state === "error" && (
        <ErrorState message={load.error} onRetry={reload} />
      )}
      {load.state === "ready" &&
        (load.data.length === 0 ? (
          <EmptyState
            title={t("empty.title")}
            hint={
              account || source
                ? t("empty.hint.filtered")
                : t("empty.hint.blank")
            }
          />
        ) : (
          <AuditTable entries={load.data} />
        ))}
    </Page>
  );
}
