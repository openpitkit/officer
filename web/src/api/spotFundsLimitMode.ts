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

import { useCallback } from "react";

import { invalid, isRecord } from "@/api/decode";
import type { SpotFundsLimitMode } from "@/api/types";
import { usePolling, type PollingResult } from "@/api/usePolling";
import { useApiClient, type ApiClient } from "@/framework";
import { isSpotFundsLimitMode } from "@/framework/registries/vocabulary";

/** The effective mode and currency gaps reported by the server. */
export interface SpotFundsLimitModeState {
  /** The effective mode: enforce while no spot_funds_limit_mode policy row is stored. */
  mode: SpotFundsLimitMode;
  /** Whether the spot_funds_limit_mode policy row is stored. */
  stored: boolean;
  /** Visible accounts whose P&L bound blocks them for lack of a currency. */
  accountsWithoutCurrency: string[];
}

const RESPONSE = "Spot Funds limit mode";

function decodeMode(value: unknown): SpotFundsLimitMode {
  if (!isSpotFundsLimitMode(value)) {
    throw invalid(RESPONSE, "mode", 'must be "enforce" or "track_only"');
  }
  return value;
}

/** Reads the mode and the server's list of accounts missing a currency. */
export async function fetchSpotFundsLimitMode(
  client: ApiClient,
  signal?: AbortSignal,
): Promise<SpotFundsLimitModeState> {
  const value = await client.request(
    `${client.baseUrl}/limits/spot-funds-limit-mode`,
    { signal },
  );
  if (!isRecord(value)) {
    throw invalid(RESPONSE, "body", "must be an object");
  }
  const mode = decodeMode(value.mode);
  const { stored, accountsWithoutCurrency } = value;
  if (typeof stored !== "boolean") {
    throw invalid(RESPONSE, "stored", "must be a boolean");
  }
  if (!Array.isArray(accountsWithoutCurrency)) {
    throw invalid(RESPONSE, "accountsWithoutCurrency", "must be an array");
  }
  for (const [index, code] of accountsWithoutCurrency.entries()) {
    if (typeof code !== "string") {
      throw invalid(
        RESPONSE,
        `accountsWithoutCurrency[${index}]`,
        "must be a string",
      );
    }
  }
  return { mode, stored, accountsWithoutCurrency };
}

/** Polls the effective mode through the host's API client. */
export function useSpotFundsLimitMode(): PollingResult<SpotFundsLimitModeState> {
  const client = useApiClient();
  const fetcher = useCallback(
    (signal: AbortSignal) => fetchSpotFundsLimitMode(client, signal),
    [client],
  );
  return usePolling(fetcher);
}
