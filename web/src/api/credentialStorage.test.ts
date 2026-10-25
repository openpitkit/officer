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

import { describe, expect, it } from "vitest";

import { fetchCredentialStorage } from "@/api/credentialStorage";
import type { CredentialStorage } from "@/api/types";
import { ApiError } from "@/framework";
import {
  clientAnswering,
  errorResponse,
  jsonResponse,
  sentRequest,
} from "@/test/apiClient";

const storage: CredentialStorage = {
  credentialsSealed: false,
  plaintextTradingKeys: true,
  plaintextMarketDataKeys: false,
  sealedAt: null,
  resealedSecrets: 0,
};

describe("fetchCredentialStorage", () => {
  it("reads all five app fields and forwards the abort signal", async () => {
    const signal = new AbortController().signal;
    const { client, fetchMock } = clientAnswering(
      jsonResponse({ ...storage, extra: "ignored" }),
    );

    await expect(fetchCredentialStorage(client, signal)).resolves.toEqual(
      storage,
    );
    expect(sentRequest(fetchMock)).toMatchObject({
      url: "/app/api/v1/credential-storage",
      init: { method: "GET", body: undefined, signal },
    });
  });

  it.each([
    ["2026-10-15T14:05:00Z", 3],
    ["2026-10-15T14:05:00.123456789+03:00", 0],
    [null, Number.MAX_SAFE_INTEGER],
  ])(
    "reads a sealing at %s that re-encrypted %i values",
    async (sealedAt, resealedSecrets) => {
      const sealed: CredentialStorage = {
        credentialsSealed: true,
        plaintextTradingKeys: false,
        plaintextMarketDataKeys: false,
        sealedAt,
        resealedSecrets,
      };
      const { client } = clientAnswering(jsonResponse(sealed));

      await expect(fetchCredentialStorage(client)).resolves.toEqual(sealed);
    },
  );

  describe.each([
    "credentialsSealed",
    "plaintextTradingKeys",
    "plaintextMarketDataKeys",
  ] as const)("base field %s", (field) => {
    it.each([undefined, null, "false", 0])(
      "rejects a missing or non-boolean value %s",
      async (value) => {
        const { client } = clientAnswering(
          jsonResponse({ ...storage, [field]: value }),
        );

        await expect(fetchCredentialStorage(client)).rejects.toMatchObject({
          code: "internal",
          message: `Invalid or missing credential storage ${field} in the API response`,
        });
      },
    );
  });

  it.each([null, [], "invalid", false])(
    "rejects a non-object body %s",
    async (body) => {
      const { client } = clientAnswering(jsonResponse(body));

      await expect(fetchCredentialStorage(client)).rejects.toMatchObject({
        code: "internal",
        message: "Credential storage is missing from the API response",
      });
    },
  );

  it.each([
    undefined,
    "",
    "2026-10-15",
    "2026-10-15 14:05:00Z",
    "2026-10-15T14:05:00",
    "2026-02-30T14:05:00Z",
    "2026-10-15T24:00:00Z",
    1760537100,
    false,
    {},
  ])("rejects a sealedAt %s that is not null or RFC 3339", async (sealedAt) => {
    const { client } = clientAnswering(jsonResponse({ ...storage, sealedAt }));
    const result = fetchCredentialStorage(client);

    await expect(result).rejects.toBeInstanceOf(ApiError);
    await expect(result).rejects.toMatchObject({
      code: "internal",
      message:
        "Invalid or missing credential storage sealedAt in the API response",
    });
  });

  it.each([undefined, null, -1, 1.5, "3", true, Number.MAX_SAFE_INTEGER + 1])(
    "rejects a resealedSecrets %s that is not a non-negative safe integer",
    async (resealedSecrets) => {
      const { client } = clientAnswering(
        jsonResponse({ ...storage, resealedSecrets }),
      );
      const result = fetchCredentialStorage(client);

      await expect(result).rejects.toBeInstanceOf(ApiError);
      await expect(result).rejects.toMatchObject({
        code: "internal",
        message:
          "Invalid or missing credential storage resealedSecrets in the API response",
      });
    },
  );

  it("rejects a framework-only answer on the app endpoint", async () => {
    const { client } = clientAnswering(
      jsonResponse({
        credentialsSealed: false,
        plaintextTradingKeys: false,
        plaintextMarketDataKeys: false,
      }),
    );

    await expect(fetchCredentialStorage(client)).rejects.toMatchObject({
      code: "internal",
      message:
        "Invalid or missing credential storage sealedAt in the API response",
    });
  });

  it("propagates an API error", async () => {
    const { client } = clientAnswering(errorResponse(403, "forbidden"));

    await expect(fetchCredentialStorage(client)).rejects.toMatchObject({
      code: "forbidden",
    });
  });
});
