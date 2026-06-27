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

import type { Limit } from "@/api/types";
import { usePolling, type PollingResult } from "@/api/usePolling";
import { useOfficerApi } from "@/framework";

/** Poll GET /api/v1/limits, optionally filtered to one account server-side. */
export function useLimits(account?: string): PollingResult<Limit[]> {
  const api = useOfficerApi();
  const fetcher = useCallback(
    (signal: AbortSignal) => api.fetchLimits(account || undefined, signal),
    [api, account],
  );
  return usePolling(fetcher);
}
