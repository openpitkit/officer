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

import { describe, expect, it, vi } from "vitest";

import { fetchSpotFundsLimitMode } from "@/api/spotFundsLimitMode";
import type { SpotFundsLimitMode } from "@/api/types";
import { createApiClient } from "@/framework";
import { jsonResponse } from "@/test/apiClient";

const BASE_URL = "/app/api/v1";
const state = {
  mode: "enforce",
  stored: true,
  accountsWithoutCurrency: ["desk-a", "desk-b"],
};

function clientAnswering(body: unknown) {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse(body));
  const client = createApiClient({
    baseUrl: BASE_URL,
    fetch: fetchMock as unknown as typeof fetch,
  });
  return { client, fetchMock };
}

describe("fetchSpotFundsLimitMode", () => {
  it.each<SpotFundsLimitMode>(["enforce", "track_only"])(
    "decodes %s and the account list from the configured base URL",
    async (mode) => {
      const answer = { ...state, mode };
      const { client, fetchMock } = clientAnswering(answer);
      const controller = new AbortController();

      await expect(
        fetchSpotFundsLimitMode(client, controller.signal),
      ).resolves.toEqual(answer);

      const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
      expect(url).toBe(`${BASE_URL}/limits/spot-funds-limit-mode`);
      expect(init.method).toBe("GET");
      expect(init.signal).toBe(controller.signal);
    },
  );

  it("decodes a mode with no stored row", async () => {
    const answer = { ...state, stored: false };
    const { client } = clientAnswering(answer);
    await expect(fetchSpotFundsLimitMode(client)).resolves.toEqual(answer);
  });

  it("accepts an empty account list", async () => {
    const answer = {
      mode: "track_only",
      stored: true,
      accountsWithoutCurrency: [],
    };
    const { client } = clientAnswering(answer);
    await expect(fetchSpotFundsLimitMode(client)).resolves.toEqual(answer);
  });

  it.each([
    ["body", "a null body", null],
    ["body", "an array body", [state]],
    ["body", "a string body", "enforce"],
    ["mode", "an unknown mode", { ...state, mode: "always_allow" }],
    ["mode", "a missing mode", { accountsWithoutCurrency: [] }],
    ["mode", "a null mode", { ...state, mode: null }],
    [
      "stored",
      "a missing flag",
      { mode: "enforce", accountsWithoutCurrency: [] },
    ],
    ["stored", "a string flag", { ...state, stored: "true" }],
    [
      "accountsWithoutCurrency",
      "a missing list",
      { mode: "enforce", stored: false },
    ],
    [
      "accountsWithoutCurrency",
      "a non-array list",
      { ...state, accountsWithoutCurrency: "desk-a" },
    ],
    [
      "accountsWithoutCurrency",
      "a null list",
      { ...state, accountsWithoutCurrency: null },
    ],
    [
      "accountsWithoutCurrency[1]",
      "a non-string item",
      { ...state, accountsWithoutCurrency: ["desk-a", 42] },
    ],
  ])("rejects %s for %s", async (field, _case, body) => {
    const { client } = clientAnswering(body);
    await expect(fetchSpotFundsLimitMode(client)).rejects.toThrow(
      `Spot Funds limit mode response: ${field} `,
    );
  });
});
