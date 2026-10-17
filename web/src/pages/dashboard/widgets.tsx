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
import { Activity, Inbox, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";

import type {
  ActivityEntry,
  AuditEntry,
  McpCommand,
  Overview,
} from "@/api/types";
import { useAudit } from "@/api/useAudit";
import { useMarketData } from "@/api/useMarketData";
import { useMcpAccess } from "@/api/useMcpAccess";
import { useTrading } from "@/api/useTrading";
import {
  DashboardIssueCount,
  DashboardItemChips,
  DashboardNote,
  DashboardSourceError,
  DashboardSourceRow,
  DashboardStatusCard,
  type DashboardItem,
} from "@/components/dashboard";
import { CompactStaleState } from "@/components/PageStates";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { formatDateTime } from "@/i18n/format";
import { accountScope } from "@/lib/accountScope";
import {
  auditActionMeta,
  groupActivity,
  type ActivityGroup,
} from "@/pages/dashboardActivity";

export interface DashboardReadyWidgetProps {
  counts: Overview["counts"];
  activity: ActivityEntry[];
}

export function CountsRow({ counts }: DashboardReadyWidgetProps) {
  const { t } = useTranslation("dashboard");
  const navigate = useNavigate();
  const { showsAccounts } = accountScope();
  const tiles: {
    label: string;
    value: ReactNode;
    caption: string;
    route: string;
  }[] = [
    ...(showsAccounts
      ? [
          {
            label: t("counts.accounts"),
            value: (
              <SlashCount
                left={counts.accountsActive}
                right={counts.accounts}
              />
            ),
            caption: t("counts.captionActiveTotal"),
            route: "/accounts",
          },
          {
            label: t("counts.groups"),
            value: (
              <SlashCount left={counts.groupsActive} right={counts.groups} />
            ),
            caption: t("counts.captionActiveTotal"),
            route: "/accounts",
          },
        ]
      : []),
    {
      label: t("counts.orders"),
      value: (
        <SlashCount
          values={[counts.ordersToday, counts.ordersActive, counts.ordersTotal]}
        />
      ),
      caption: t("counts.captionTodayActiveTotal"),
      route: "/orders",
    },
  ];
  return (
    <div
      className={
        showsAccounts ? "grid grid-cols-3 gap-4" : "grid grid-cols-1 gap-4"
      }
    >
      {tiles.map(({ label, value, caption, route }) => (
        <button
          key={label}
          type="button"
          onClick={() => navigate(route)}
          className="animate-fade-in rounded-card text-left hover:bg-accent-dim focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Card className="h-full pointer-events-none">
            <CardContent className="flex flex-col items-center gap-1 py-4 text-center">
              <span
                className="nums text-2xl font-bold tracking-tight text-text"
                title={caption}
              >
                {value}
              </span>
              <span className="text-xs text-muted">{label}</span>
              <span className="text-[0.625rem] uppercase tracking-[0.08em] text-muted-lt">
                {caption}
              </span>
            </CardContent>
          </Card>
        </button>
      ))}
    </div>
  );
}

function SlashCount({
  left,
  right,
  values,
}: {
  left?: number;
  right?: number;
  values?: number[];
}) {
  const parts = values ?? [left ?? 0, right ?? 0];
  return (
    <>
      {parts.map((value, index) => (
        <span key={`${index}-${value}`}>
          {index > 0 && (
            <span className="text-xl font-normal text-muted-lt"> / </span>
          )}
          {value}
        </span>
      ))}
    </>
  );
}

function mcpChipVariant(cmd: McpCommand): "danger" | "warn" | "neutral" {
  if (cmd.protective) return "danger";
  if (cmd.mutating) return "warn";
  return "neutral";
}

export function McpAccessCard() {
  const { t } = useTranslation("dashboard");
  const { load } = useMcpAccess();

  return (
    <DashboardStatusCard title={t("mcp.title")} to="/mcp-access" load={load}>
      {(commands) => {
        const enabled = commands.filter((c) => c.enabled);
        return enabled.length === 0 ? (
          <DashboardNote>{t("mcp.none")}</DashboardNote>
        ) : (
          enabled.map((cmd) => (
            <Badge key={cmd.name} variant={mcpChipVariant(cmd)}>
              {cmd.name}
            </Badge>
          ))
        );
      }}
    </DashboardStatusCard>
  );
}

const MD_STALE_LIMIT = 4;
const ITEM_CHIP_LIMIT = 3;
const MANUAL_PROVIDER = "byo";

function instrumentItem(instr: {
  externalSymbol: string;
  baseAsset: string;
  quoteAsset: string;
}): DashboardItem {
  return {
    key: instr.externalSymbol,
    text: `${instr.baseAsset}/${instr.quoteAsset}`,
  };
}

export function TradingConnectionsCard() {
  const { t } = useTranslation("dashboard");
  const { load } = useTrading();

  return (
    <DashboardStatusCard
      title={t("trading.title")}
      to="/trading-connections"
      load={load}
    >
      {(data) => {
        const enabled = data.connections.filter((c) => c.enabled);
        if (enabled.length === 0) {
          return <DashboardNote>{t("trading.none")}</DashboardNote>;
        }
        return enabled.map((connection) => (
          <DashboardSourceRow key={connection.id} label={connection.label}>
            <Badge>{t(`trading:mode.${connection.mode}`)}</Badge>
            {connection.state === "degraded" && (
              <DashboardSourceError
                variant="warn"
                title={t("trading:state.degraded")}
                error={connection.error}
                diagnostic={connection.diagnostics.at(-1)?.title}
              />
            )}
            {connection.state === "unavailable" && (
              <DashboardSourceError
                title={t("trading:state.unavailable")}
                error={connection.error}
                diagnostic={connection.diagnostics.at(-1)?.title}
              />
            )}
            {connection.state === "stopped" && (
              <Badge variant="warn">{t("trading:state.stopped")}</Badge>
            )}
            {connection.state === "running" && (
              <>
                <Badge variant="ok">{t("trading:state.running")}</Badge>
                <DashboardItemChips
                  items={connection.instruments
                    .filter((i) => i.enabled)
                    .map(instrumentItem)}
                  variant="neutral"
                  limit={ITEM_CHIP_LIMIT}
                  moreLabel={(count) => t("trading.more", { count })}
                />
                {connection.diagnostics.length > 0 && (
                  <DashboardIssueCount count={connection.diagnostics.length} />
                )}
              </>
            )}
          </DashboardSourceRow>
        ));
      }}
    </DashboardStatusCard>
  );
}

export function MarketDataCard() {
  const { t } = useTranslation("dashboard");
  const { load } = useMarketData();

  return (
    <DashboardStatusCard
      title={t("marketData.title")}
      to="/market-data"
      load={load}
    >
      {(data) => {
        const enabled = data.instances.filter((i) => i.enabled);
        if (enabled.length === 0) {
          return <DashboardNote>{t("marketData.none")}</DashboardNote>;
        }

        const allStale = enabled.flatMap((inst) =>
          inst.state === "error" || inst.provider === MANUAL_PROVIDER
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
        let staleConsumed = 0;

        return (
          <>
            {data.restartRequired && (
              <Badge variant="warn">{t("marketData.restartRequired")}</Badge>
            )}
            {enabled.map((inst) => {
              const label = inst.label || inst.id;

              if (inst.state === "error") {
                const latest =
                  inst.diagnostics.length > 0
                    ? inst.diagnostics[inst.diagnostics.length - 1]
                    : null;
                return (
                  <DashboardSourceError
                    key={inst.id}
                    title={`${t("marketData.instanceError")} ${label}`}
                    error={inst.error}
                    diagnostic={latest?.title}
                  />
                );
              }

              if (inst.state === "pending") {
                return (
                  <Badge key={inst.id} variant="warn">
                    {label} - {t("marketData.instanceNotApplied")}
                  </Badge>
                );
              }

              const isManual = inst.provider === MANUAL_PROVIDER;
              const instStale = isManual
                ? []
                : inst.instruments.filter((i) => i.stale);
              const instOk = inst.instruments.filter(
                (i) => i.enabled && (isManual || !i.stale),
              );

              const instStaleSlot = Math.max(0, MD_STALE_LIMIT - staleConsumed);
              const shownInstStale = instStale.slice(0, instStaleSlot);
              staleConsumed += shownInstStale.length;

              return (
                <DashboardSourceRow key={inst.id} label={label}>
                  {shownInstStale.map((instr) => (
                    <Badge key={instr.externalSymbol} variant="warn">
                      {instr.baseAsset}/{instr.quoteAsset}
                    </Badge>
                  ))}
                  <DashboardItemChips
                    items={instOk.map(instrumentItem)}
                    variant={isManual ? "neutral" : "ok"}
                    limit={ITEM_CHIP_LIMIT}
                    moreLabel={(count) => t("marketData.moreOk", { count })}
                  />
                  {inst.diagnostics.length > 0 && (
                    <DashboardIssueCount count={inst.diagnostics.length} />
                  )}
                </DashboardSourceRow>
              );
            })}
            {extraStale > 0 && (
              <DashboardNote>
                {t("marketData.moreStale", { count: extraStale })}
              </DashboardNote>
            )}
          </>
        );
      }}
    </DashboardStatusCard>
  );
}

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
      <span className="mt-0.5 shrink-0 rounded-badge border border-border px-1.5 py-0.5 text-[0.6rem] uppercase tracking-wide text-muted">
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

export function ActivityColumns({ activity }: DashboardReadyWidgetProps) {
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

function AuditRow({ entry }: { entry: AuditEntry }) {
  const { t } = useTranslation("dashboard");
  const navigate = useNavigate();
  const { Icon, variant, titleKey } = auditActionMeta(entry.action);
  const { showsAccounts } = accountScope();

  return (
    <button
      type="button"
      onClick={() =>
        navigate(`/audit?source=${encodeURIComponent(entry.source)}`)
      }
      className="flex w-full items-center gap-3 border-b border-border py-2 text-left last:border-0 hover:bg-accent-dim"
    >
      <Badge variant={variant} className="shrink-0 gap-1 px-1.5 py-0.5">
        <Icon className="h-3 w-3" />
        <span className="sr-only">
          {t(titleKey, { defaultValue: titleKey })}
        </span>
      </Badge>

      {showsAccounts && (
        <span className="w-28 shrink-0 truncate text-xs text-text">
          {entry.accountTitle || entry.account || (
            <span className="text-muted-lt">-</span>
          )}
        </span>
      )}

      <span className="min-w-0 flex-1 truncate text-xs text-muted">
        {entry.detail || entry.actorTitle || entry.actor}
      </span>

      <span className="shrink-0 rounded-badge border border-border px-1.5 py-0.5 text-[0.6rem] uppercase tracking-wide text-muted">
        {entry.source}
      </span>
      <span className="nums w-36 shrink-0 text-right text-[0.6875rem] text-muted-lt">
        {formatDateTime(entry.at)}
      </span>
    </button>
  );
}

const AUDIT_LIMIT = 15;

export function AuditStrip() {
  const { t } = useTranslation("dashboard");
  const { load } = useAudit(AUDIT_LIMIT);

  return (
    <Card className="animate-fade-in">
      <CardHeader className="flex-row items-center gap-2">
        <Activity className="h-4 w-4 text-muted" />
        <CardTitle>{t("audit.title")}</CardTitle>
        <CompactStaleState load={load} />
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
