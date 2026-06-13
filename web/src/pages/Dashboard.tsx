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

import { Activity, Inbox, Loader2, RefreshCw } from "lucide-react";
import { useNavigate } from "react-router-dom";

import type { ActivityEntry, AuditEntry, Overview } from "@/api/types";
import { useAudit } from "@/api/useAudit";
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
import {
  auditActionMeta,
  groupActivity,
  type ActivityGroup,
} from "@/pages/dashboardActivity";

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

/** Render an ISO timestamp as a compact UTC string. */
function formatUtc(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toISOString().replace("T", " ").replace("Z", " UTC");
}

// ---------------------------------------------------------------------------
// Counts row (unchanged)
// ---------------------------------------------------------------------------

function CountsRow({ counts }: { counts: Overview["counts"] }) {
  const items = [
    { label: "Accounts", value: counts.accounts },
    { label: "Groups",   value: counts.groups   },
    { label: "Policies", value: counts.limits   },
  ];
  return (
    <div className="grid grid-cols-4 gap-4">
      {items.map(({ label, value }) => (
        <Card key={label} className="animate-fade-in">
          <CardContent className="flex flex-col items-center gap-1 py-6 text-center">
            <span className="nums text-3xl font-bold tracking-tight text-text">
              {value}
            </span>
            <span className="text-xs text-muted">{label}</span>
          </CardContent>
        </Card>
      ))}
      <Card className="animate-fade-in">
        <CardContent className="flex flex-col items-center gap-1 py-6 text-center">
          <span className="nums text-3xl font-bold tracking-tight text-text">
            {counts.ordersToday}
            <span className="text-xl font-normal text-muted-lt"> / </span>
            {counts.ordersTotal}
          </span>
          <span className="text-xs text-muted">Orders</span>
        </CardContent>
      </Card>
    </div>
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
          {formatUtc(entry.at)}
        </span>
      </span>
    </button>
  );
}

/** One column for a single entity-type group. */
function ActivityColumn({ group }: { group: ActivityGroup }) {
  return (
    <Card className="animate-fade-in flex flex-col">
      <CardHeader className="flex-row items-center gap-2 pb-2">
        <Activity className="h-3.5 w-3.5 shrink-0 text-muted" />
        <CardTitle className="text-xs">{group.label}</CardTitle>
      </CardHeader>
      <CardContent className="flex-1 space-y-0 pb-2">
        {group.entries.map((e, i) => (
          <ActivityItem key={i} entry={e} route={group.route} />
        ))}
      </CardContent>
    </Card>
  );
}

/** Responsive multi-column grid of activity groups. */
function ActivityColumns({ activity }: { activity: ActivityEntry[] }) {
  const groups = groupActivity(activity);

  if (groups.length === 0) {
    return (
      <Card className="animate-fade-in">
        <CardContent className="flex flex-col items-center gap-3 py-10 text-center">
          <Inbox className="h-7 w-7 text-muted" />
          <p className="text-sm text-muted">No recent activity</p>
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
        <ActivityColumn key={g.label} group={g} />
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Audit strip
// ---------------------------------------------------------------------------

/** One row in the audit strip. */
function AuditRow({ entry }: { entry: AuditEntry }) {
  const navigate = useNavigate();
  const { Icon, variant, title } = auditActionMeta(entry.action);

  return (
    <button
      type="button"
      onClick={() => navigate(`/audit?source=${encodeURIComponent(entry.source)}`)}
      className="flex w-full items-center gap-3 border-b border-border py-2 text-left last:border-0 hover:bg-accent-dim"
    >
      {/* Action icon / badge */}
      <Badge variant={variant} className="shrink-0 gap-1 px-1.5 py-0.5">
        <Icon className="h-3 w-3" />
        <span className="sr-only">{title}</span>
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
        {formatUtc(entry.at)}
      </span>
    </button>
  );
}

const AUDIT_LIMIT = 15;

/** Full-width recent audit strip. */
function AuditStrip() {
  const { load } = useAudit(AUDIT_LIMIT);

  return (
    <Card className="animate-fade-in">
      <CardHeader className="flex-row items-center gap-2">
        <Activity className="h-4 w-4 text-muted" />
        <CardTitle>Recent audit</CardTitle>
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
            <p className="text-xs text-muted">No audit entries</p>
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
  const { load, reload } = useOverview();
  const refreshing = load.state === "loading";

  return (
    <Page
      title="Dashboard"
      actions={
        <Button
          variant="ghost"
          size="sm"
          onClick={reload}
          disabled={refreshing}
          aria-label="Refresh"
        >
          {refreshing ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <RefreshCw className="h-3.5 w-3.5" />
          )}
          Refresh
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
          <ActivityColumns activity={load.data.activity} />
        </>
      )}
      <AuditStrip />
    </Page>
  );
}
