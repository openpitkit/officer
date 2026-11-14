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
// Please see https://officer.openpit.dev and the OWNERS file for details.

import { AlertTriangle, Bell } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { usePolling } from "@/api/usePolling";
import { Notice } from "@/components/Notice";
import { EmptyState, ErrorState, LoadingStatus } from "@/components/PageStates";
import { useSidebar } from "@/components/sidebar-context";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { useApiClient } from "@/framework/api/ApiClientProvider";
import { formatDateTime, formatNumber } from "@/i18n/format";
import { cn } from "@/lib/utils";

import { listAlerts, resolveAlert, setAlertRead } from "./api";
import { getAlertRenderer } from "./registry";

export function AlertsLauncher() {
  const { t } = useTranslation();
  const client = useApiClient();
  const { close, collapsed, setIndicator, menuButtonRef } = useSidebar();
  const { load, reload } = usePolling((signal) => listAlerts(client, signal));
  const [open, setOpen] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inFlight = useRef(false);
  const unread = load.state === "ready" ? load.data.unread : null;
  const launcherCaption =
    load.state === "ready"
      ? t("alerts:unresolved", { count: load.data.total })
      : load.state === "error"
        ? t("alerts:loadFailed")
        : t("states.loading");
  const launcherLabel =
    `${t("alerts:title")}: ${launcherCaption}` +
    (unread === null ? "" : ` - ${t("alerts:unread", { count: unread })}`);

  useEffect(() => {
    if (!setIndicator) return;
    setIndicator(
      unread !== null && unread > 0 ? (
        <Badge
          variant="warn"
          aria-label={t("alerts:unread", { count: unread })}
        >
          {formatNumber(unread)}
        </Badge>
      ) : null,
    );
    return () => setIndicator(null);
  }, [unread, setIndicator, t]);

  async function mutate(operation: () => Promise<unknown>): Promise<boolean> {
    if (inFlight.current) return false;
    inFlight.current = true;
    setPending(true);
    setError(null);
    try {
      await operation();
      reload();
      return true;
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
      return false;
    } finally {
      inFlight.current = false;
      setPending(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        setError(null);
        if (next) {
          close();
          reload();
        }
      }}
    >
      <DialogTrigger asChild>
        <button
          type="button"
          title={collapsed ? launcherLabel : undefined}
          aria-label={collapsed ? launcherLabel : undefined}
          className={cn(
            "mb-3 flex w-full items-center gap-2 rounded-[3px] border p-3 text-left text-xs transition-colors",
            collapsed && "justify-center gap-0 p-2",
            unread !== null && unread > 0
              ? "border-[var(--warn)] bg-[var(--warn-dim)] text-text"
              : "border-border bg-bg/30 text-muted-lt hover:bg-surface-hover",
          )}
        >
          <Bell className="h-4 w-4 shrink-0" />
          <span className={collapsed ? "sr-only" : "min-w-0 flex-1"}>
            <span className="block font-bold">{t("alerts:title")}</span>
            <span className="block text-[10px]">{launcherCaption}</span>
          </span>
          {!collapsed && unread !== null && (
            <Badge variant={unread > 0 ? "warn" : "neutral"}>
              {formatNumber(unread)}
            </Badge>
          )}
        </button>
      </DialogTrigger>
      <DialogContent
        onCloseAutoFocus={(event) => {
          if (window.innerWidth < 768 && menuButtonRef?.current) {
            event.preventDefault();
            menuButtonRef.current.focus();
          }
        }}
        className="inset-y-0 left-auto right-0 flex h-full max-h-none w-full max-w-xl translate-x-0 translate-y-0 flex-col gap-4 rounded-none p-5"
      >
        <DialogTitle>{t("alerts:title")}</DialogTitle>
        <DialogDescription>{t("alerts:description")}</DialogDescription>
        {load.state === "loading" && <LoadingStatus />}
        {load.state === "error" && (
          <ErrorState message={load.error} onRetry={reload} />
        )}
        {load.state === "ready" && (
          <>
            {load.error && (
              <Notice
                tone="danger"
                icon={AlertTriangle}
                title={load.error}
                role="alert"
                action={
                  <Button size="sm" variant="outline" onClick={reload}>
                    {t("actions.retry")}
                  </Button>
                }
              />
            )}
            {error && (
              <Notice
                tone="danger"
                icon={AlertTriangle}
                title={error}
                role="alert"
              />
            )}
            {load.data.total === 0 && (
              <EmptyState
                title={t("alerts:emptyTitle")}
                hint={t("alerts:emptyHint")}
              />
            )}
            <div className="min-h-0 flex-1 overflow-y-auto">
              {load.data.alerts.map((alert) => {
                const renderer = getAlertRenderer(alert.type);
                const selected = expanded === alert.id;
                const Detail = renderer?.Detail;
                return (
                  <section
                    key={alert.id}
                    className="border-b border-border py-3"
                  >
                    <Button
                      variant="ghost"
                      disabled={pending}
                      aria-expanded={selected}
                      className="h-auto w-full justify-start whitespace-normal text-left"
                      onClick={() => {
                        if (selected) {
                          setExpanded(null);
                          return;
                        }
                        setExpanded(alert.id);
                        if (!alert.read)
                          void mutate(() =>
                            setAlertRead(client, alert.id, true),
                          );
                      }}
                    >
                      <span className="min-w-0 flex-1">
                        <span
                          className={cn("block", !alert.read && "font-bold")}
                        >
                          {renderer
                            ? renderer.summary(alert)
                            : t("alerts:unsupported", { type: alert.type })}
                        </span>
                        <span className="block text-[10px] text-muted">
                          {formatDateTime(alert.at)}
                        </span>
                      </span>
                      {!alert.read && (
                        <Badge variant="warn">{t("alerts:unreadLabel")}</Badge>
                      )}
                    </Button>
                    {selected && (
                      <div className="flex flex-col gap-y-3 px-3 pt-3">
                        {alert.deliveries
                          .filter((d) => d.failed)
                          .map((d) => (
                            <Notice
                              key={d.channel}
                              tone="warn"
                              icon={AlertTriangle}
                              title={t("alerts:deliveryFailed", {
                                channel: d.channel,
                              })}
                            />
                          ))}
                        {!renderer && (
                          <Notice
                            tone="danger"
                            icon={AlertTriangle}
                            title={t("alerts:unsupported", {
                              type: alert.type,
                            })}
                            role="alert"
                          />
                        )}
                        {Detail && (
                          <Detail
                            alert={alert}
                            pending={pending}
                            resolve={async (action, input) => {
                              if (!alert.actions.includes(action)) {
                                setError(t("alerts:unsupportedAction"));
                                return;
                              }
                              if (
                                await mutate(() =>
                                  resolveAlert(client, alert.id, action, input),
                                )
                              )
                                setExpanded(null);
                            }}
                          />
                        )}
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={pending}
                          onClick={() =>
                            void mutate(() =>
                              setAlertRead(client, alert.id, !alert.read),
                            )
                          }
                        >
                          {t(
                            alert.read
                              ? "alerts:markUnread"
                              : "alerts:markRead",
                          )}
                        </Button>
                      </div>
                    )}
                  </section>
                );
              })}
            </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
