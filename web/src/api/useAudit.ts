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

import { useCallback } from "react";

import type { AuditActionGroup, AuditEntry, PagedResult } from "@/api/types";
import { usePolling, type PollingResult } from "@/api/usePolling";
import { type AuditFilter, useOfficerApi } from "@/framework";

/** Poll GET /audit with server-side total and offset paging. */
export function useAuditPage(
  filter: AuditFilter = {},
): PollingResult<PagedResult<AuditEntry>> {
  const api = useOfficerApi();
  const actionsKey = filter.actions?.join(",") ?? "";
  const filterKey = JSON.stringify({ ...filter, actions: actionsKey });
  const fetcher = useCallback(
    (signal: AbortSignal) => api.fetchAuditPage(filter, signal),
    // filterKey stands in for the filter object identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [api, filterKey],
  );
  return usePolling(fetcher, 5000, filterKey);
}

/** Poll GET /audit for the newest `limit` entries, optionally filtered. The
 *  actions array is the include-set of event types; an empty array selects
 *  nothing. The key derived from it keeps the fetcher stable across renders. */
export function useAudit(
  limit: number,
  account?: string,
  source?: string,
  actions?: string[],
): PollingResult<AuditEntry[]> {
  const api = useOfficerApi();
  const actionsKey = actions?.join(",") ?? "";
  const fetcher = useCallback(
    (signal: AbortSignal) =>
      api.fetchAudit(
        { limit, account, source, actions: actions ? [...actions] : undefined },
        signal,
      ),
    // actionsKey stands in for the actions array identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [api, limit, account, source, actionsKey],
  );
  return usePolling(
    fetcher,
    5000,
    JSON.stringify({ limit, account, source, actions: actionsKey }),
  );
}

/** Poll GET /audit/actions for the action catalogue used to build the type
 *  filter. The catalogue is small and static, but polling reuses the shared
 *  load lifecycle so the page can gate on it like any other fetch. */
export function useAuditActions(): PollingResult<AuditActionGroup[]> {
  const api = useOfficerApi();
  const fetcher = useCallback(
    (signal: AbortSignal) => api.fetchAuditActions(signal),
    [api],
  );
  return usePolling(fetcher);
}
