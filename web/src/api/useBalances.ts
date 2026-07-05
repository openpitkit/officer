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

import { useCallback, useMemo } from "react";

import type { Balance, BalanceListFilters, PagedResult } from "@/api/types";
import { usePolling, type PollingResult } from "@/api/usePolling";
import { useOfficerApi } from "@/framework";

/** Poll GET /balances, optionally filtered by account and/or asset. */
export function useBalances(
  account?: string,
  asset?: string,
): PollingResult<Balance[]> {
  const api = useOfficerApi();
  const fetcher = useCallback(
    (signal: AbortSignal) => api.fetchBalances({ account, asset }, signal),
    [api, account, asset],
  );
  return usePolling(fetcher, 5000, JSON.stringify({ account, asset }));
}

/** Poll GET /balances with server-side total. */
export function useBalancesPage(
  filters?: BalanceListFilters,
): PollingResult<PagedResult<Balance>> {
  const api = useOfficerApi();
  const filterKey = useMemo(() => JSON.stringify(filters ?? {}), [filters]);
  const fetcher = useCallback(
    (signal: AbortSignal) => api.fetchBalancesPage(filters, signal),
    [api, filters],
  );
  return usePolling(fetcher, 5000, filterKey);
}
