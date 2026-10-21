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
import { usePolling, type PollingResult } from "@/api/usePolling";
import { useApiClient, type ApiClient } from "@/framework";

/** Stored account mode: one default account, or accounts named per request. */
export type AccountMode = "single" | "multi";

/** What the running process enforces; it never changes while it runs. */
export interface AccountFeatures {
  /** Whether clients see and choose accounts and account groups. */
  accounts: boolean;
  /** The default account requests act on; empty while accounts are shown. */
  account: string;
}

/** The answer of `GET /account-mode`. */
export interface AccountModeState {
  /** The stored mode; null until one is chosen, which behaves as multi. */
  mode: AccountMode | null;
  /** The stored default account. */
  account: string;
  /** What the running process enforces. */
  features: AccountFeatures;
  /** Whether the stored choice takes effect only after a restart. */
  restartRequired: boolean;
}

const RESPONSE = "account mode";

function decodeAccountMode(value: unknown): AccountModeState {
  if (!isRecord(value)) {
    throw invalid(RESPONSE, "body", "must be an object");
  }
  const { mode, account, features, restartRequired } = value;
  if (mode !== "single" && mode !== "multi" && mode !== null) {
    throw invalid(RESPONSE, "mode", 'must be "single", "multi" or null');
  }
  if (typeof account !== "string") {
    throw invalid(RESPONSE, "account", "must be a string");
  }
  if (!isRecord(features)) {
    throw invalid(RESPONSE, "features", "must be an object");
  }
  const { accounts, account: defaultAccount } = features;
  if (typeof accounts !== "boolean") {
    throw invalid(RESPONSE, "features.accounts", "must be a boolean");
  }
  if (typeof defaultAccount !== "string") {
    throw invalid(RESPONSE, "features.account", "must be a string");
  }
  if (!accounts && defaultAccount === "") {
    throw invalid(
      RESPONSE,
      "features.account",
      "must name the default account while accounts are hidden",
    );
  }
  if (typeof restartRequired !== "boolean") {
    throw invalid(RESPONSE, "restartRequired", "must be a boolean");
  }
  return {
    mode,
    account,
    features: { accounts, account: defaultAccount },
    restartRequired,
  };
}

/**
 * Fetch the stored account mode and the running process's features from
 * `GET /account-mode`.
 *
 * @throws Error naming the offending field when the response is malformed.
 */
export async function fetchAccountMode(
  client: ApiClient,
  signal?: AbortSignal,
): Promise<AccountModeState> {
  return decodeAccountMode(
    await client.request(`${client.baseUrl}/account-mode`, { signal }),
  );
}

/** The body of `PUT /account-mode`. */
export interface AccountModeRequest {
  mode: AccountMode;
  /** The default account of `single`; omitted, the server resolves it. */
  account?: string;
}

/**
 * Store the next account mode with `PUT /account-mode`. The running process
 * keeps its features: the answer's `restartRequired` says a restart applies it.
 *
 * @throws Error naming the offending field when the response is malformed.
 */
export async function putAccountMode(
  client: ApiClient,
  request: AccountModeRequest,
): Promise<AccountModeState> {
  return decodeAccountMode(
    await client.request(`${client.baseUrl}/account-mode`, {
      method: "PUT",
      body: request,
    }),
  );
}

/** Poll GET /account-mode. */
export function useAccountMode(): PollingResult<AccountModeState> {
  const client = useApiClient();
  const fetcher = useCallback(
    (signal: AbortSignal) => fetchAccountMode(client, signal),
    [client],
  );
  return usePolling(fetcher);
}
