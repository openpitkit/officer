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

import { AlertTriangle, Inbox, RefreshCw, X } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { LoadState } from "@/api/usePolling";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";

/** A skeleton table body shown while the first page load is in flight. */
export function TableSkeleton({
  rows = 5,
  cols = 4,
}: {
  rows?: number;
  cols?: number;
}) {
  const { t } = useTranslation();
  return (
    <Card
      role="status"
      aria-busy="true"
      aria-label={t("states.loading")}
      className="animate-pulse"
    >
      <CardContent className="space-y-3 py-5">
        {Array.from({ length: rows }).map((_, r) => (
          <div key={r} className="flex gap-4">
            {Array.from({ length: cols }).map((_, c) => (
              <div key={c} className="h-3 flex-1 rounded bg-border" />
            ))}
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

/** Full-card error state with a retry action. */
export function ErrorState({
  message,
  onRetry,
  title,
}: {
  message: string;
  onRetry: () => void;
  title?: string;
}) {
  const { t } = useTranslation();
  return (
    <Card className="animate-fade-in">
      <CardContent className="flex flex-col items-center gap-4 py-12 text-center">
        <AlertTriangle className="h-8 w-8 text-[var(--danger)]" />
        <div className="space-y-1">
          <p className="text-sm font-bold text-text">
            {title ?? t("states.loadError")}
          </p>
          <p className="max-w-md text-xs text-muted-lt">{message}</p>
        </div>
        <Button variant="outline" size="sm" onClick={onRetry}>
          <RefreshCw className="h-3.5 w-3.5" />
          {t("actions.retry")}
        </Button>
      </CardContent>
    </Card>
  );
}

/** Full-card empty state with a hint and an optional primary action. */
export function EmptyState({
  title,
  hint,
  action,
}: {
  title: string;
  hint: ReactNode;
  action?: ReactNode;
}) {
  return (
    <Card className="animate-fade-in">
      <CardContent className="flex flex-col items-center gap-4 py-12 text-center">
        <Inbox className="h-8 w-8 text-muted" />
        <div className="space-y-1">
          <p className="text-sm font-bold text-text">{title}</p>
          <p className="max-w-md text-xs text-muted-lt">{hint}</p>
        </div>
        {action}
      </CardContent>
    </Card>
  );
}

/** A dismissible inline error banner for mutation failures (no toast lib). */
export function ErrorBanner({
  message,
  onDismiss,
  className,
}: {
  message: string;
  onDismiss?: () => void;
  className?: string;
}) {
  const { t } = useTranslation();
  return (
    <div
      role="alert"
      className={cn(
        "flex items-start gap-2 rounded-card border border-[var(--danger)]",
        "bg-[var(--danger-dim)] px-3 py-2 text-xs text-[var(--danger)]",
        className,
      )}
    >
      <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
      <span className="flex-1 break-words">{message}</span>
      {onDismiss && (
        <button
          type="button"
          onClick={onDismiss}
          aria-label={t("actions.dismissError")}
          className="shrink-0 rounded-badge p-0.5 hover:bg-[var(--danger-dim)]"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      )}
    </div>
  );
}

function hasStaleData(load: LoadState<unknown>) {
  return load.state === "ready" && load.error !== null;
}

/** Marks retained data as stale in compact layouts. */
export function CompactStaleState({ load }: { load: LoadState<unknown> }) {
  const { t } = useTranslation();
  if (!hasStaleData(load)) {
    return null;
  }
  return (
    <Badge
      role="status"
      aria-label={`${t("states.stale")}: ${load.error}`}
      variant="warn"
    >
      {t("states.stale")}
    </Badge>
  );
}

/** Marks retained data as stale when a background refresh fails. */
export function StaleState({
  load,
  reload,
}: {
  load: LoadState<unknown>;
  reload: () => void;
}) {
  const { t } = useTranslation();
  if (!hasStaleData(load)) {
    return null;
  }
  return (
    <div
      role="status"
      aria-label={t("states.stale")}
      className="flex items-center gap-3 rounded-card border border-border bg-surface-2 px-3 py-2 text-xs text-text"
    >
      <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
      <div className="flex-1">
        <p>{t("states.staleDescription")}</p>
        <p className="break-words text-muted-lt">{load.error}</p>
      </div>
      <Button size="sm" variant="outline" onClick={reload}>
        <RefreshCw className="h-3.5 w-3.5" />
        {t("actions.retry")}
      </Button>
    </div>
  );
}
