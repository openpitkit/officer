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

import type { ApiClient } from "@/framework/api/createApiClient";
import type { ActionableAlert } from "./registry";

export interface AlertList {
  alerts: ActionableAlert[];
  unread: number;
  total: number;
}

function object(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Reject malformed responses instead of inventing empty data or counts. */
export function parseAlertList(value: unknown): AlertList {
  if (
    !object(value) ||
    !Array.isArray(value.alerts) ||
    !Number.isSafeInteger(value.total) ||
    !Number.isSafeInteger(value.unread) ||
    (value.total as number) < 0 ||
    (value.unread as number) < 0 ||
    (value.unread as number) > (value.total as number)
  ) {
    throw new Error("Invalid alert list response");
  }
  for (const a of value.alerts) {
    if (
      !object(a) ||
      typeof a.id !== "string" ||
      !a.id ||
      typeof a.type !== "string" ||
      !a.type ||
      typeof a.at !== "string" ||
      !Number.isFinite(Date.parse(a.at)) ||
      typeof a.source !== "string" ||
      typeof a.read !== "boolean" ||
      !("payload" in a) ||
      !Array.isArray(a.actions) ||
      !a.actions.every((x: unknown) => typeof x === "string") ||
      !Array.isArray(a.deliveries) ||
      !a.deliveries.every(
        (d: unknown) =>
          object(d) &&
          typeof d.channel === "string" &&
          typeof d.failed === "boolean",
      )
    ) {
      throw new Error("Invalid alert response");
    }
  }
  if (
    value.total !== value.alerts.length ||
    value.unread !== value.alerts.filter((a) => !a.read).length
  ) {
    throw new Error("Inconsistent alert counts");
  }
  return value as unknown as AlertList;
}

export async function listAlerts(
  client: ApiClient,
  signal: AbortSignal,
): Promise<AlertList> {
  return parseAlertList(
    await client.request(`${client.baseUrl}/alerts`, { signal }),
  );
}

export function setAlertRead(
  client: ApiClient,
  id: string,
  read: boolean,
): Promise<unknown> {
  return client.request(
    `${client.baseUrl}/alerts/${encodeURIComponent(id)}/read`,
    {
      method: "PUT",
      body: { read },
    },
  );
}

export function resolveAlert(
  client: ApiClient,
  id: string,
  action: string,
  input: unknown,
): Promise<unknown> {
  return client.request(
    `${client.baseUrl}/alerts/${encodeURIComponent(id)}/actions/${encodeURIComponent(action)}`,
    {
      method: "POST",
      body: { input },
    },
  );
}
