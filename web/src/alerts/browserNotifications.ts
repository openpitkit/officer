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

import type { TFunction } from "i18next";
import { useEffect, useLayoutEffect, useRef, useState } from "react";

import type { LoadState } from "@/api/usePolling";
import { formatNumber } from "@/i18n/format";

import type { AlertList } from "./api";
import unreadIcon from "./favicon-unread.svg?no-inline";
import { alertSummary } from "./registry";

let seenAlerts: Set<string> | null = null;
let notifierRequested = false;
let isNotifier = false;
let constructorCheck: "pending" | "checked" | "unsupported" = "pending";

function notificationPermission(): NotificationPermission | "unsupported" {
  if (
    typeof Notification === "undefined" ||
    typeof navigator.locks === "undefined" ||
    constructorCheck === "unsupported"
  )
    return "unsupported";
  return Notification.permission;
}

function failureMessage(failure: unknown): string {
  return failure instanceof Error ? failure.message : String(failure);
}

function useTabBadge(unread: number | undefined) {
  const updateBadge = useRef<(count: string | null) => void>(null);
  const count =
    unread !== undefined && unread > 0 ? formatNumber(unread) : null;

  useEffect(() => {
    const title = document.title;
    const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
    if (!icon) throw new Error('Missing link[rel="icon"]');
    const href = icon.getAttribute("href");
    const update = (count: string | null) => {
      document.title = count === null ? title : `(${count}) ${title}`;
      const nextHref = count === null ? href : unreadIcon;
      if (nextHref == null) icon.removeAttribute("href");
      else icon.setAttribute("href", nextHref);
    };
    updateBadge.current = update;
    return () => update(null);
  }, []);

  useEffect(() => {
    updateBadge.current?.(count);
  }, [count]);
}

/** Observe fresh alert lists for this tab and expose the browser permission. */
export function useBrowserNotifications(
  load: LoadState<AlertList>,
  t: TFunction,
  onClick: (id: string, notification: Notification) => void,
) {
  const fresh =
    load.state === "ready" && load.error === null ? load.data : null;
  useTabBadge(fresh?.unread);

  const permission = notificationPermission();
  const [, refreshPermission] = useState(0);
  const [failure, setFailure] = useState<string | null>(null);
  const latestClick = useRef<typeof onClick>(null);

  useLayoutEffect(() => {
    latestClick.current = onClick;
    return () => {
      latestClick.current = null;
    };
  }, [onClick]);

  useEffect(() => {
    if (notifierRequested || notificationPermission() === "unsupported") return;
    notifierRequested = true;
    void navigator.locks
      .request("pit-officer-alert-notifier", () => {
        isNotifier = true;
        // The browser releases the lock when this tab goes away.
        return new Promise<never>(() => {});
      })
      .catch((error: unknown) => setFailure(failureMessage(error)));
  }, []);

  useEffect(() => {
    // This effect reports each constructor failure once by updating state
    // synchronously.
    function reportConstructorError(error: unknown) {
      if (error instanceof TypeError) {
        constructorCheck = "unsupported";
        refreshPermission((version) => version + 1);
      } else {
        setFailure(failureMessage(error));
      }
    }

    if (
      constructorCheck === "pending" &&
      permission !== "unsupported" &&
      Notification.permission !== "granted"
    ) {
      constructorCheck = "checked";
      try {
        new Notification("").close();
      } catch (error) {
        reportConstructorError(error);
      }
    }

    if (fresh === null) return;
    const baseline = seenAlerts === null;
    seenAlerts ??= new Set();
    for (const alert of fresh.alerts) {
      if (seenAlerts.has(alert.id)) continue;
      seenAlerts.add(alert.id);
      if (
        baseline ||
        alert.read ||
        !isNotifier ||
        notificationPermission() !== "granted"
      )
        continue;

      const title = t("alerts:browserNotifications.notificationTitle");
      const body = alertSummary(alert, t);
      let notification: Notification;
      try {
        notification = new Notification(title, { body, tag: alert.id });
      } catch (error) {
        reportConstructorError(error);
        continue;
      }
      notification.addEventListener("error", (event) => {
        const message = "message" in event ? event.message : null;
        setFailure(
          typeof message === "string" && message !== ""
            ? message
            : t("alerts:browserNotifications.refused"),
        );
      });
      notification.addEventListener("click", () => {
        latestClick.current?.(alert.id, notification);
      });
    }
  }, [fresh, permission, t]);

  async function requestPermission() {
    setFailure(null);
    try {
      await Notification.requestPermission();
      refreshPermission((version) => version + 1);
    } catch (error) {
      setFailure(failureMessage(error));
    }
  }

  return { permission, failure, requestPermission };
}
