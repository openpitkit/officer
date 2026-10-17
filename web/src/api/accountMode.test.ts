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

import { fetchAccountMode, putAccountMode } from "@/api/accountMode";
import { createApiClient } from "@/framework";
import { jsonResponse } from "@/test/apiClient";

const BASE_URL = "/app/api/v1";

function clientAnswering(body: unknown) {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse(body));
  const client = createApiClient({
    baseUrl: BASE_URL,
    fetch: fetchMock as unknown as typeof fetch,
  });
  return { client, fetchMock };
}

const single = {
  mode: "single",
  account: "main",
  features: { accounts: false, account: "main" },
  restartRequired: false,
};

describe("fetchAccountMode", () => {
  it("decodes a single-account response from GET /account-mode", async () => {
    const { client, fetchMock } = clientAnswering(single);

    await expect(fetchAccountMode(client)).resolves.toEqual(single);

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(`${BASE_URL}/account-mode`);
    expect(init.method).toBe("GET");
  });

  it("decodes a response with no mode chosen yet", async () => {
    const unchosen = {
      mode: null,
      account: "",
      features: { accounts: true, account: "" },
      restartRequired: true,
    };
    const { client } = clientAnswering(unchosen);

    await expect(fetchAccountMode(client)).resolves.toEqual(unchosen);
  });

  it.each([
    ["body", "a non-object body", ["single"]],
    ["mode", "an unknown mode", { ...single, mode: "solo" }],
    ["mode", "a missing mode", { ...single, mode: undefined }],
    ["account", "a non-string account", { ...single, account: 7 }],
    ["features", "missing features", { ...single, features: undefined }],
    ["features", "non-object features", { ...single, features: "main" }],
    [
      "features.accounts",
      "a non-boolean features.accounts",
      { ...single, features: { accounts: "false", account: "main" } },
    ],
    [
      "features.account",
      "a non-string features.account",
      { ...single, features: { accounts: false, account: null } },
    ],
    [
      "features.account",
      "hidden accounts without a default account",
      { ...single, features: { accounts: false, account: "" } },
    ],
    [
      "restartRequired",
      "a non-boolean restartRequired",
      { ...single, restartRequired: "no" },
    ],
  ])("rejects %s for %s", async (field, _case, body) => {
    const { client } = clientAnswering(body);

    await expect(fetchAccountMode(client)).rejects.toThrow(
      `account mode response: ${field} must`,
    );
  });
});

describe("putAccountMode", () => {
  it("sends the request to PUT /account-mode and decodes the answer", async () => {
    const stored = {
      mode: "single",
      account: "main",
      features: { accounts: true, account: "" },
      restartRequired: true,
    };
    const { client, fetchMock } = clientAnswering(stored);

    await expect(
      putAccountMode(client, { mode: "single", account: "main" }),
    ).resolves.toEqual(stored);

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(`${BASE_URL}/account-mode`);
    expect(init.method).toBe("PUT");
    expect(JSON.parse(init.body as string)).toEqual({
      mode: "single",
      account: "main",
    });
  });

  it("rejects a malformed answer", async () => {
    const { client } = clientAnswering({ ...single, restartRequired: "yes" });

    await expect(putAccountMode(client, { mode: "multi" })).rejects.toThrow(
      "account mode response: restartRequired must",
    );
  });
});
