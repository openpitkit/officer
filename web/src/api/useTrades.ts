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

import type { PagedResult, Trade } from "@/api/types";
import { usePolling, type PollingResult } from "@/api/usePolling";
import { type TradesFilter, useOfficerApi } from "@/framework";

/** Poll GET /trades with server-side total and offset paging. */
export function useTradesPage(
  filter: TradesFilter = {},
): PollingResult<PagedResult<Trade>> {
  const api = useOfficerApi();
  const filterKey = JSON.stringify(filter);
  const fetcher = useCallback(
    (signal: AbortSignal) => api.fetchTradesPage(filter, signal),
    // filterKey stands in for the filter object identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [api, filterKey],
  );
  return usePolling(fetcher, 5000, filterKey);
}

/** Poll GET /trades, optionally filtered by account and/or source. */
export function useTrades(
  account?: string,
  source?: string,
  limit?: number,
): PollingResult<Trade[]> {
  const api = useOfficerApi();
  const fetcher = useCallback(
    (signal: AbortSignal) =>
      api.fetchTrades({ account, source, limit }, signal),
    [api, account, source, limit],
  );
  return usePolling(fetcher, 5000, JSON.stringify({ account, source, limit }));
}
