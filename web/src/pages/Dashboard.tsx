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

import type { ReactNode } from "react";
import { Activity, Inbox, Loader2, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";

import type { ActivityEntry, AuditEntry, McpCommand, Overview } from "@/api/types";
import { useAudit } from "@/api/useAudit";
import { useMarketData } from "@/api/useMarketData";
import { useMcpAccess } from "@/api/useMcpAccess";
import { useOverview } from "@/api/useOverview";
import { Page } from "@/components/Page";
import { ErrorState, TableSkeleton } from "@/components/PageStates";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { formatDateTime } from "@/i18n/format";
import {
  auditActionMeta,
  groupActivity,
  type ActivityGroup,
} from "@/pages/dashboardActivity";

// ---------------------------------------------------------------------------
// Counts row
// ---------------------------------------------------------------------------

function CountsRow({ counts }: { counts: Overview["counts"] }) {
  const { t } = useTranslation("dashboard");
  const navigate = useNavigate();
  const tiles: { label: string; value: ReactNode; route: string }[] = [
    { label: t("counts.accounts"), value: counts.accounts, route: "/accounts" },
    { label: t("counts.groups"),   value: counts.groups,   route: "/accounts" },
    {
      label: t("counts.orders"),
      value: (
        <>
          {counts.ordersToday}
          <span className="text-xl font-normal text-muted-lt"> / </span>
          {counts.ordersTotal}
        </>
      ),
      route: "/orders",
    },
  ];
  return (
    <div className="grid grid-cols-3 gap-4">
      {tiles.map(({ label, value, route }) => (
        <button
          key={label}
          type="button"
          onClick={() => navigate(route)}
          className="animate-fade-in rounded-xl text-left hover:bg-accent-dim focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Card className="h-full pointer-events-none">
            <CardContent className="flex flex-col items-center gap-1 py-4 text-center">
              <span className="nums text-2xl font-bold tracking-tight text-text">
                {value}
              </span>
              <span className="text-xs text-muted">{label}</span>
            </CardContent>
          </Card>
        </button>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// MCP access card
// ---------------------------------------------------------------------------

/** Chip variant for a single enabled MCP command. */
function mcpChipVariant(cmd: McpCommand): "danger" | "warn" | "neutral" {
  if (cmd.protective) return "danger";
  if (cmd.mutating) return "warn";
  return "neutral";
}

/** Full-width card: title + enabled-command chips on one line. */
function McpAccessCard() {
  const { t } = useTranslation("dashboard");
  const navigate = useNavigate();
  const { load } = useMcpAccess();

  return (
    <button
      type="button"
      onClick={() => navigate("/mcp-access")}
      className="animate-fade-in w-full cursor-pointer rounded-xl text-left hover:bg-accent-dim focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <Card className="pointer-events-none">
        <CardContent className="flex flex-wrap items-center gap-2 py-3">
          <CardTitle className="shrink-0">{t("mcp.title")}</CardTitle>
          {load.state === "loading" && (
            <Loader2 className="h-3.5 w-3.5 animate-spin text-muted" />
          )}
          {load.state === "error" && (
            <span className="text-xs text-muted-lt">{load.error}</span>
          )}
          {load.state === "ready" && (() => {
            const enabled = load.data.filter((c) => c.enabled);
            return enabled.length === 0 ? (
              <span className="text-xs text-muted-lt">{t("mcp.none")}</span>
            ) : (
              enabled.map((cmd) => (
                <Badge key={cmd.name} variant={mcpChipVariant(cmd)}>
                  {cmd.name}
                </Badge>
              ))
            );
          })()}
        </CardContent>
      </Card>
    </button>
  );
}

// ---------------------------------------------------------------------------
// Market-data card
// ---------------------------------------------------------------------------

// Packing limits for the dashboard market-data card:
//   - stale (laggard) pairs: show up to 4, then "+N stale" counter
//   - ok pairs per datasource: show up to 3, then "+N OK" counter
const MD_STALE_LIMIT = 4;
const MD_OK_LIMIT = 3;

function MarketDataCard() {
  const { t } = useTranslation("dashboard");
  const navigate = useNavigate();
  const { load } = useMarketData();

  return (
    <button
      type="button"
      onClick={() => navigate("/market-data")}
      className="animate-fade-in w-full cursor-pointer rounded-xl text-left hover:bg-accent-dim focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <Card className="pointer-events-none">
        <CardContent className="flex flex-wrap items-center gap-2 py-3">
          <CardTitle className="shrink-0">{t("marketData.title")}</CardTitle>
          {load.state === "loading" && (
            <Loader2 className="h-3.5 w-3.5 animate-spin text-muted" />
          )}
          {load.state === "error" && (
            <span className="text-xs text-muted-lt">{load.error}</span>
          )}
          {load.state === "ready" && (() => {
            const enabled = load.data.instances.filter((i) => i.enabled);
            if (enabled.length === 0) {
              return (
                <span className="text-xs text-muted-lt">
                  {t("marketData.none")}
                </span>
              );
            }

            // Collect all stale pairs across all instances for global cap.
            const allStale = enabled.flatMap((inst) =>
              inst.state === "error"
                ? []
                : inst.instruments
                    .filter((instr) => instr.stale)
                    .map((instr) => ({
                      key: `${inst.id}/${instr.externalSymbol}`,
                      label: `${instr.baseAsset}/${instr.quoteAsset}`,
                    })),
            );
            const shownStale = allStale.slice(0, MD_STALE_LIMIT);
            const extraStale = allStale.length - shownStale.length;

            // Track how many stale we've already consumed from the global cap.
            let staleConsumed = 0;

            return (
              <>
                {enabled.map((inst) => {
                  const label = inst.label || inst.id;

                  if (inst.state === "error") {
                    const errSuffix = inst.error
                      ? `: ${inst.error.slice(0, 40)}${inst.error.length > 40 ? "…" : ""}`
                      : "";
                    const latest = inst.diagnostics.length > 0
                      ? inst.diagnostics[inst.diagnostics.length - 1]
                      : null;
                    const diagSuffix = latest
                      ? ` — ${latest.title.slice(0, 40)}${latest.title.length > 40 ? "…" : ""}`
                      : "";
                    return (
                      <Badge key={inst.id} variant="danger">
                        {t("marketData.instanceError")} {label}{errSuffix || diagSuffix}
                      </Badge>
                    );
                  }

                  if (inst.state === "pending") {
                    return (
                      <Badge key={inst.id} variant="warn">
                        {label} — {t("marketData.instanceNotApplied")}
                      </Badge>
                    );
                  }

                  const instStale = inst.instruments.filter((i) => i.stale);
                  const instOk = inst.instruments.filter(
                    (i) => i.enabled && !i.stale,
                  );

                  // Stale pairs for this instance, limited by remaining global cap.
                  const instStaleSlot = Math.max(
                    0,
                    MD_STALE_LIMIT - staleConsumed,
                  );
                  const shownInstStale = instStale.slice(0, instStaleSlot);
                  staleConsumed += shownInstStale.length;

                  const shownOk = instOk.slice(0, MD_OK_LIMIT);
                  const extraOk = instOk.length - shownOk.length;

                  return (
                    <span
                      key={inst.id}
                      className="flex flex-wrap items-center gap-1"
                    >
                      <Badge variant="neutral">{label}</Badge>
                      {shownInstStale.map((instr) => (
                        <Badge
                          key={instr.externalSymbol}
                          variant="warn"
                        >
                          {instr.baseAsset}/{instr.quoteAsset}
                        </Badge>
                      ))}
                      {shownOk.map((instr) => (
                        <Badge
                          key={instr.externalSymbol}
                          variant="ok"
                        >
                          {instr.baseAsset}/{instr.quoteAsset}
                        </Badge>
                      ))}
                      {extraOk > 0 && (
                        <span className="text-xs text-muted-lt">
                          {t("marketData.moreOk", { count: extraOk })}
                        </span>
                      )}
                      {inst.diagnostics.length > 0 && (
                        <span className="text-xs text-[var(--warn)]">
                          {t("marketData.issues", { count: inst.diagnostics.length })}
                        </span>
                      )}
                    </span>
                  );
                })}
                {extraStale > 0 && (
                  <span className="text-xs text-muted-lt">
                    {t("marketData.moreStale", { count: extraStale })}
                  </span>
                )}
              </>
            );
          })()}
        </CardContent>
      </Card>
    </button>
  );
}

// ---------------------------------------------------------------------------
// Activity — grouped columns
// ---------------------------------------------------------------------------

/** One entry inside an activity column. */
function ActivityItem({
  entry,
  route,
}: {
  entry: ActivityEntry;
  route: "trading" | "audit";
}) {
  const navigate = useNavigate();
  const href =
    route === "trading"
      ? `/trading?source=${encodeURIComponent(entry.source)}`
      : `/audit?source=${encodeURIComponent(entry.source)}`;

  return (
    <button
      type="button"
      onClick={() => navigate(href)}
      className="flex w-full items-start gap-2 border-b border-border py-2 text-left last:border-0 hover:bg-accent-dim"
    >
      <span className="mt-0.5 shrink-0 rounded-[3px] border border-border px-1.5 py-0.5 text-[0.6rem] uppercase tracking-wide text-muted">
        {entry.source}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-xs text-text">
          {entry.summary || entry.kind}
        </span>
        <span className="nums block text-[0.6875rem] text-muted-lt">
          {formatDateTime(entry.at)}
        </span>
      </span>
    </button>
  );
}

/** One column for a single entity-type group. */
function ActivityColumn({ group }: { group: ActivityGroup }) {
  const { t } = useTranslation("dashboard");
  return (
    <Card className="animate-fade-in flex flex-col">
      <CardHeader className="flex-row items-center gap-2 pb-2">
        <Activity className="h-3.5 w-3.5 shrink-0 text-muted" />
        <CardTitle className="text-xs">{t(group.labelKey)}</CardTitle>
      </CardHeader>
      <CardContent className="flex-1 space-y-0 pb-2">
        {group.entries.map((e) => (
          <ActivityItem key={e.ref} entry={e} route={group.route} />
        ))}
      </CardContent>
    </Card>
  );
}

/** Responsive multi-column grid of activity groups. */
function ActivityColumns({ activity }: { activity: ActivityEntry[] }) {
  const { t } = useTranslation("dashboard");
  const groups = groupActivity(activity);

  if (groups.length === 0) {
    return (
      <Card className="animate-fade-in">
        <CardContent className="flex flex-col items-center gap-3 py-10 text-center">
          <Inbox className="h-7 w-7 text-muted" />
          <p className="text-sm text-muted">{t("activity.noRecent")}</p>
        </CardContent>
      </Card>
    );
  }

  return (
    // auto-fit: single column on narrow, up to ~4 on wide screens.
    <div
      className="grid gap-4"
      style={{ gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))" }}
    >
      {groups.map((g) => (
        <ActivityColumn key={g.labelKey} group={g} />
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Audit strip
// ---------------------------------------------------------------------------

/** One row in the audit strip. */
function AuditRow({ entry }: { entry: AuditEntry }) {
  const { t } = useTranslation("dashboard");
  const navigate = useNavigate();
  const { Icon, variant, titleKey } = auditActionMeta(entry.action);

  return (
    <button
      type="button"
      onClick={() => navigate(`/audit?source=${encodeURIComponent(entry.source)}`)}
      className="flex w-full items-center gap-3 border-b border-border py-2 text-left last:border-0 hover:bg-accent-dim"
    >
      {/* Action icon / badge */}
      <Badge variant={variant} className="shrink-0 gap-1 px-1.5 py-0.5">
        <Icon className="h-3 w-3" />
        <span className="sr-only">{t(titleKey, { defaultValue: titleKey })}</span>
      </Badge>

      {/* Account */}
      <span className="w-28 shrink-0 truncate text-xs text-text">
        {entry.account || <span className="text-muted-lt">—</span>}
      </span>

      {/* Detail */}
      <span className="min-w-0 flex-1 truncate text-xs text-muted">
        {entry.detail || entry.actor}
      </span>

      {/* Source + time */}
      <span className="shrink-0 rounded-[3px] border border-border px-1.5 py-0.5 text-[0.6rem] uppercase tracking-wide text-muted">
        {entry.source}
      </span>
      <span className="nums w-36 shrink-0 text-right text-[0.6875rem] text-muted-lt">
        {formatDateTime(entry.at)}
      </span>
    </button>
  );
}

const AUDIT_LIMIT = 15;

/** Full-width recent audit strip. */
function AuditStrip() {
  const { t } = useTranslation("dashboard");
  const { load } = useAudit(AUDIT_LIMIT);

  return (
    <Card className="animate-fade-in">
      <CardHeader className="flex-row items-center gap-2">
        <Activity className="h-4 w-4 text-muted" />
        <CardTitle>{t("audit.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-0 pb-2">
        {load.state === "loading" && (
          <div className="flex items-center justify-center py-6">
            <Loader2 className="h-4 w-4 animate-spin text-muted" />
          </div>
        )}
        {load.state === "error" && (
          <p className="py-4 text-center text-xs text-muted-lt">{load.error}</p>
        )}
        {load.state === "ready" && load.data.length === 0 && (
          <div className="flex flex-col items-center gap-2 py-6 text-center">
            <Inbox className="h-6 w-6 text-muted" />
            <p className="text-xs text-muted">{t("audit.noEntries")}</p>
          </div>
        )}
        {load.state === "ready" &&
          load.data.map((e) => <AuditRow key={e.id} entry={e} />)}
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function Dashboard() {
  const { t } = useTranslation("dashboard");
  const { t: tc } = useTranslation();
  const { load, reload } = useOverview();
  const refreshing = load.state === "loading";

  return (
    <Page
      title={t("title")}
      actions={
        <Button
          variant="ghost"
          size="sm"
          onClick={reload}
          disabled={refreshing}
          aria-label={t("refreshAriaLabel")}
        >
          {refreshing ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <RefreshCw className="h-3.5 w-3.5" />
          )}
          {tc("actions.refresh")}
        </Button>
      }
    >
      {load.state === "loading" && <TableSkeleton rows={3} cols={3} />}
      {load.state === "error" && (
        <ErrorState message={load.error} onRetry={reload} />
      )}
      {load.state === "ready" && (
        <>
          <CountsRow counts={load.data.counts} />
          <McpAccessCard />
          <MarketDataCard />
          <ActivityColumns activity={load.data.activity} />
        </>
      )}
      <AuditStrip />
    </Page>
  );
}
