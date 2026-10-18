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

import { useCallback, useMemo } from "react";

import type {
  Account,
  AccountListFilters,
  Limit,
  PagedResult,
} from "@/api/types";
import { usePolling, type PollingResult } from "@/api/usePolling";
import { ApiError, useOfficerApi } from "@/framework";

/** Poll GET /accounts. */
export function useAccounts(
  filters?: AccountListFilters,
): PollingResult<Account[]> {
  const api = useOfficerApi();
  const filterKey = useMemo(() => JSON.stringify(filters ?? {}), [filters]);
  const fetcher = useCallback(
    (signal: AbortSignal) => api.fetchAccounts(filters, signal),
    [api, filters],
  );
  return usePolling(fetcher, 5000, filterKey);
}

/** Poll GET /accounts with server-side total. */
export function useAccountsPage(
  filters?: AccountListFilters,
): PollingResult<PagedResult<Account>> {
  const api = useOfficerApi();
  const filterKey = useMemo(() => JSON.stringify(filters ?? {}), [filters]);
  const fetcher = useCallback(
    (signal: AbortSignal) => api.fetchAccountsPage(filters, signal),
    [api, filters],
  );
  return usePolling(fetcher, 5000, filterKey);
}

/** Poll GET /accounts/{code}. The data is null once the API answers that the
 *  account does not exist (`not_found`); any other error is a failed poll. */
export function useAccountState(
  code: string,
): PollingResult<{ account: Account; limits: Limit[] } | null> {
  const api = useOfficerApi();
  const fetcher = useCallback(
    async (signal: AbortSignal) => {
      try {
        return await api.fetchAccountState(code, signal);
      } catch (err) {
        if (err instanceof ApiError && err.code === "not_found") return null;
        throw err;
      }
    },
    [api, code],
  );
  return usePolling(fetcher, 5000, code);
}
