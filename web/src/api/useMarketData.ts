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

import type { MarketDataStatus, MarketDataSymbolSearch } from "@/api/types";
import { usePolling, type PollingResult } from "@/api/usePolling";
import { useOfficerApi, type MarketDataSymbolSearchInput } from "@/framework";

/** Poll GET /market-data. */
export function useMarketData(): PollingResult<MarketDataStatus> {
  const api = useOfficerApi();
  const fetcher = useCallback(
    (signal: AbortSignal) => api.fetchMarketData(signal),
    [api],
  );
  return usePolling(fetcher);
}

/** Non-mutating contract resolve for an instance, mirroring the page-level
 *  symbol verification helper: it issues a single POST and never disturbs the
 *  running feed or the polling cycle. */
export function useMarketDataSymbolSearch(): (
  instanceId: string,
  input: MarketDataSymbolSearchInput,
) => Promise<MarketDataSymbolSearch> {
  const api = useOfficerApi();
  return useCallback(
    (instanceId: string, input: MarketDataSymbolSearchInput) =>
      api.searchMarketDataSymbols(instanceId, input),
    [api],
  );
}
