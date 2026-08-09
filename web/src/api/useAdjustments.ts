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

import type { Adjustment, PagedResult } from "@/api/types";
import { usePolling, type PollingResult } from "@/api/usePolling";
import { type GlobalAdjustmentsFilter, useOfficerApi } from "@/framework";

/** Poll GET /adjustments with server-side total and offset paging. */
export function useAdjustmentsPage(
  filter: GlobalAdjustmentsFilter = {},
): PollingResult<PagedResult<Adjustment>> {
  const api = useOfficerApi();
  const filterKey = JSON.stringify(filter);
  const fetcher = useCallback(
    (signal: AbortSignal) => api.fetchAdjustmentsPage(filter, signal),
    // filterKey stands in for the filter object identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [api, filterKey],
  );
  return usePolling(fetcher, 5000, filterKey);
}

/** Poll GET /adjustments, optionally filtered by account and/or source. */
export function useAdjustments(
  account?: string,
  source?: string,
  limit?: number,
): PollingResult<Adjustment[]> {
  const api = useOfficerApi();
  const fetcher = useCallback(
    (signal: AbortSignal) =>
      api.fetchAdjustments({ account, source, limit }, signal),
    [api, account, source, limit],
  );
  return usePolling(fetcher, 5000, JSON.stringify({ account, source, limit }));
}
